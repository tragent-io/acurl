//go:build linux || darwin || windows

package tap

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessClientHelper(t *testing.T) {
	addr := os.Getenv("ACURL_TEST_PROCESS_ADDR")
	if addr == "" {
		return
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for range 2 {
		_, err := fmt.Fprintf(conn, "GET /test/x HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("ready")
	// Keep the socket alive until its owner has been identified.
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestProcessNativeClient(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(addr, func(t *testing.T) {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				t.Skipf("loopback family unavailable: %v", err)
			}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
			defer up.Close()
			got := make(chan *Exchange, 4)
			p, err := New(Options{Routes: []Route{{Name: "test", Prefix: "/test", Upstream: up.URL}}, OnExchange: func(x *Exchange) { got <- x }})
			if err != nil {
				ln.Close()
				t.Fatal(err)
			}
			p.processes.timeout = 5 * time.Second
			srv := httptest.NewUnstartedServer(p)
			srv.Listener.Close()
			srv.Listener = ln
			srv.Config.ConnContext = p.ConnContext
			srv.Start()
			defer srv.Close()
			defer p.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessClientHelper$")
			cmd.Env = append(os.Environ(), "ACURL_TEST_PROCESS_ADDR="+ln.Addr().String())
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				stdin.Close()
				if err := cmd.Wait(); err != nil {
					t.Errorf("client helper: %v", err)
				}
			}()
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
				t.Fatalf("client not ready: %q, %v", line, err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Base(exe)
			if runtime.GOOS == "windows" {
				name = strings.TrimSuffix(name, filepath.Ext(name))
			}
			for range 2 {
				x := nextProcessExchange(t, got)
				if x.PID != cmd.Process.Pid || x.Process != name {
					t.Fatalf("attribution = %q:%d, want client %q:%d", x.Process, x.PID, name, cmd.Process.Pid)
				}
			}
		})
	}
}

func benchmarkProcessSocket(b *testing.B) (netip.AddrPort, netip.AddrPort) {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { ln.Close() })
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { client.Close() })
	server, err := ln.Accept()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { server.Close() })
	return server.LocalAddr().(*net.TCPAddr).AddrPort(), server.RemoteAddr().(*net.TCPAddr).AddrPort()
}

func BenchmarkProcessLookup(b *testing.B) {
	local, remote := benchmarkProcessSocket(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		identity, err := lookupProcess(context.Background(), local, remote)
		if err != nil || identity.pid != os.Getpid() || identity.name == "" {
			b.Fatalf("native lookup: %+v, %v", identity, err)
		}
	}
}

func BenchmarkProcessCache(b *testing.B) {
	tracker := newProcessTracker(func(*Exchange) {})
	s := &connectionProcess{tracker: tracker, started: true, ready: true, identity: processIdentity{name: "claude", pid: 42}}
	var x Exchange
	b.ReportAllocs()
	for b.Loop() {
		s.start()
		s.publish(&x)
	}
}

func BenchmarkAttributionProxy(b *testing.B) {
	for _, mode := range []string{"disabled", "native", "slow"} {
		for _, cold := range []bool{false, true} {
			name := mode + "/keepalive"
			if cold {
				name = mode + "/short-lived"
			}
			b.Run(name, func(b *testing.B) { benchmarkAttributionProxy(b, mode, cold, false) })
		}
	}
}

func BenchmarkAttributionConcurrent(b *testing.B) {
	for _, mode := range []string{"disabled", "native", "slow"} {
		b.Run(mode, func(b *testing.B) { benchmarkAttributionProxy(b, mode, false, true) })
	}
}

func benchmarkAttributionProxy(b *testing.B, mode string, cold, parallel bool) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer up.Close()
	var published, attributed, lag atomic.Int64
	upstreamTransport := http.DefaultTransport.(*http.Transport).Clone()
	if parallel {
		upstreamTransport.MaxIdleConnsPerHost = 32
		upstreamTransport.MaxIdleConns = 32
	}
	defer upstreamTransport.CloseIdleConnections()
	p, err := New(Options{Routes: []Route{{Name: "test", Prefix: "/test", Upstream: up.URL}}, Transport: upstreamTransport, OnExchange: func(x *Exchange) {
		if x.PID != 0 {
			attributed.Add(1)
		}
		lag.Add(int64(time.Since(x.Start) - x.Duration))
		// Observing this count also observes all of this callback's metrics.
		published.Add(1)
	}})
	if err != nil {
		b.Fatal(err)
	}
	switch mode {
	case "disabled":
		p.processes.lookup = nil
	case "slow":
		p.processes.lookup = func(ctx context.Context, _, _ netip.AddrPort) (processIdentity, error) {
			<-ctx.Done()
			return processIdentity{}, ctx.Err()
		}
	}
	srv := httptest.NewUnstartedServer(p)
	srv.Config.ConnContext = p.ConnContext
	srv.Start()
	defer srv.Close()
	defer p.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = cold
	if parallel {
		// Keep concurrent clients' connections alive. The default idle pool of
		// two otherwise turns this case into a second connection-churn test.
		transport.MaxIdleConnsPerHost = 32
		transport.MaxIdleConns = 32
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request := func() {
		resp, err := client.Get(srv.URL + "/test/x")
		if err != nil {
			b.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			b.Fatal(err)
		}
	}
	// Warm the connection and attribution before measuring the cache path.
	if !cold {
		request()
		awaitBenchmarkPublications(b, &published, 1)
		attributed.Store(0)
		lag.Store(0)
		published.Store(0)
	}
	samples := make([]int64, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	if parallel {
		var next atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				start := time.Now()
				request()
				samples[next.Add(1)-1] = int64(time.Since(start))
			}
		})
	} else {
		for i := range b.N {
			start := time.Now()
			request()
			samples[i] = int64(time.Since(start))
		}
	}
	b.StopTimer()
	// Shutdown would cancel unfinished lookups and bias attribution downward.
	awaitBenchmarkPublications(b, &published, int64(b.N))
	p.Close()
	slices.Sort(samples)
	b.ReportMetric(float64(samples[(len(samples)-1)*95/100]), "p95-ns/op")
	b.ReportMetric(float64(lag.Load())/float64(b.N), "event-delay-ns/op")
	b.ReportMetric(100*float64(attributed.Load())/float64(b.N), "attributed-%")
}

func awaitBenchmarkPublications(b *testing.B, published *atomic.Int64, want int64) {
	b.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for published.Load() < want {
		if time.Now().After(deadline) {
			b.Fatalf("only %d/%d exchanges published before timeout", published.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
	if got := published.Load(); got != want {
		b.Fatalf("published %d exchanges, want exactly %d", got, want)
	}
}

// BenchmarkAttributionFreshHeld keeps each fresh connection alive until its
// exchange is published, separating response latency from discovery latency.
func BenchmarkAttributionFreshHeld(b *testing.B) {
	for _, mode := range []string{"disabled", "native"} {
		b.Run(mode, func(b *testing.B) { benchmarkAttributionFreshHeld(b, mode) })
	}
}

func benchmarkAttributionFreshHeld(b *testing.B, mode string) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer up.Close()
	type publication struct {
		x   *Exchange
		lag int64
	}
	got := make(chan publication, 1)
	p, err := New(Options{Routes: []Route{{Name: "test", Prefix: "/test", Upstream: up.URL}}, OnExchange: func(x *Exchange) {
		got <- publication{x: x, lag: int64(time.Since(x.Start) - x.Duration)}
	}})
	if err != nil {
		b.Fatal(err)
	}
	if mode == "disabled" {
		p.processes.lookup = nil
	}
	srv := httptest.NewUnstartedServer(p)
	srv.Config.ConnContext = p.ConnContext
	srv.Start()
	defer srv.Close()
	defer p.Close()
	samples := make([]int64, b.N)
	eventSamples := make([]int64, b.N)
	attributed := 0
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for i := range b.N {
		// A separate transport guarantees a new connection without asking the
		// server to close it before the asynchronous socket lookup finishes.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		b.StartTimer()
		start := time.Now()
		resp, requestErr := client.Get(srv.URL + "/test/x")
		if requestErr == nil {
			_, requestErr = io.Copy(io.Discard, resp.Body)
		}
		samples[i] = int64(time.Since(start))
		b.StopTimer()
		if resp != nil {
			resp.Body.Close()
		}
		if requestErr != nil {
			transport.CloseIdleConnections()
			b.Fatal(requestErr)
		}
		timer := time.NewTimer(5 * time.Second)
		var event publication
		select {
		case event = <-got:
			timer.Stop()
		case <-timer.C:
			transport.CloseIdleConnections()
			b.Fatal("fresh held connection exchange not published before timeout")
		}
		transport.CloseIdleConnections()
		eventSamples[i] = event.lag
		if event.x.PID == os.Getpid() && event.x.Process != "" {
			attributed++
		} else if mode == "native" {
			b.Fatalf("fresh held connection attribution = %q:%d, want nonempty name and PID %d", event.x.Process, event.x.PID, os.Getpid())
		}
	}
	slices.Sort(samples)
	slices.Sort(eventSamples)
	b.ReportMetric(float64(samples[(len(samples)-1)*95/100]), "p95-ns/op")
	b.ReportMetric(float64(eventSamples[(len(eventSamples)-1)*50/100]), "event-delay-p50-ns/op")
	b.ReportMetric(float64(eventSamples[(len(eventSamples)-1)*95/100]), "event-delay-p95-ns/op")
	b.ReportMetric(100*float64(attributed)/float64(b.N), "attributed-%")
	b.ReportMetric(0, "publication-timeout-%")
}
