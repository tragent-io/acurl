package tap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testProcessTracker(t *testing.T, lookup func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error)) (*processTracker, chan *Exchange) {
	t.Helper()
	got := make(chan *Exchange, 128)
	tracker := newProcessTracker(func(x *Exchange) { got <- x })
	tracker.lookup = lookup
	t.Cleanup(tracker.close)
	return tracker, got
}

func nextProcessExchange(t *testing.T, got <-chan *Exchange) *Exchange {
	t.Helper()
	select {
	case x := <-got:
		return x
	case <-time.After(5 * time.Second):
		t.Fatal("exchange was not published")
		return nil
	}
}

func TestProcessConnectionCachesSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			var calls atomic.Int32
			tracker, got := testProcessTracker(t, func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error) {
				calls.Add(1)
				if fail {
					return processIdentity{name: "partial", pid: 9}, errors.New("denied")
				}
				return processIdentity{name: "claude", pid: 42}, nil
			})
			s := &connectionProcess{tracker: tracker}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() { s.start(); s.publish(&Exchange{}) })
			}
			wg.Wait()
			for range 4 {
				x := nextProcessExchange(t, got)
				if fail && (x.Process != "" || x.PID != 0) || !fail && (x.Process != "claude" || x.PID != 42) {
					t.Fatalf("unexpected attribution: %+v", x)
				}
			}
			// Same endpoints still belong to a fresh connection cache.
			fresh := &connectionProcess{tracker: tracker}
			fresh.start()
			fresh.publish(&Exchange{})
			nextProcessExchange(t, got)
			if calls.Load() != 2 {
				t.Fatalf("lookups = %d, want one per connection", calls.Load())
			}
		})
	}
}

func TestProcessDeadlineIgnoresLateResult(t *testing.T) {
	release := make(chan struct{})
	returned := make(chan struct{})
	tracker, got := testProcessTracker(t, func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error) {
		<-release // Model a native API that cannot be canceled.
		defer close(returned)
		return processIdentity{name: "late", pid: 7}, nil
	})
	tracker.timeout = 10 * time.Millisecond
	s := &connectionProcess{tracker: tracker}
	s.start()
	s.publish(&Exchange{})
	x := nextProcessExchange(t, got)
	if x.Process != "" || x.PID != 0 {
		t.Fatal("deadline published attribution")
	}
	close(release)
	<-returned
	tracker.close()
	s.publish(&Exchange{})
	if later := nextProcessExchange(t, got); later.Process != "" || x.Process != "" {
		t.Fatal("late result changed the cached or published attribution")
	}
}

func TestProcessBusyConnectionRetriesAdmission(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	tracker, got := testProcessTracker(t, func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error) {
		calls.Add(1)
		<-release
		return processIdentity{name: "client", pid: 42}, nil
	})
	tracker.timeout = time.Hour
	for range processLookupLimit {
		s := &connectionProcess{tracker: tracker}
		s.start()
	}
	busy := &connectionProcess{tracker: tracker}
	busy.start()
	busy.publish(&Exchange{ID: 1})
	if x := nextProcessExchange(t, got); x.Process != "" || x.PID != 0 {
		t.Fatal("busy lookup must publish without metadata")
	}
	close(release)
	waitFor(t, func() bool { return len(tracker.slots) == 0 })
	// Retry the same connection, including simultaneous requests. Only one
	// discovery is admitted, and its successful result is then cached.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { busy.start(); busy.publish(&Exchange{ID: 2}) })
	}
	wg.Wait()
	for range 4 {
		if x := nextProcessExchange(t, got); x.Process != "client" || x.PID != 42 {
			t.Fatalf("later request was not attributed: %+v", x)
		}
	}
	if calls.Load() != processLookupLimit+1 {
		t.Fatalf("lookups = %d, want %d", calls.Load(), processLookupLimit+1)
	}
}

func TestProcessCapacityAndClosePreserveExchanges(t *testing.T) {
	tracker, got := testProcessTracker(t, func(ctx context.Context, _, _ netip.AddrPort) (processIdentity, error) {
		<-ctx.Done()
		return processIdentity{}, ctx.Err()
	})
	tracker.timeout = time.Hour
	for range processLookupLimit + 1 {
		s := &connectionProcess{tracker: tracker}
		s.start()
		for range processPendingLimit + 1 {
			s.publish(&Exchange{})
		}
	}
	tracker.close()
	for range (processLookupLimit + 1) * (processPendingLimit + 1) {
		if x := nextProcessExchange(t, got); x.Process != "" || x.PID != 0 {
			t.Fatal("expected unknown attribution")
		}
	}
	select {
	case <-got:
		t.Fatal("duplicate exchange")
	default:
	}
	// A request racing shutdown is still published without discovery.
	s := &connectionProcess{tracker: tracker}
	s.start()
	s.publish(&Exchange{})
	nextProcessExchange(t, got)
}

func TestProcessCallbackCanCloseProxy(t *testing.T) {
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := p.hub.subscribe()
	defer p.hub.unsubscribe(got)
	release := make(chan struct{})
	p.processes.lookup = func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error) {
		<-release
		return processIdentity{name: "client", pid: 7}, nil
	}
	var callbacks atomic.Int32
	finished := make(chan struct{})
	p.processes.notify = func(*Exchange) {
		p.Close()
		if callbacks.Add(1) == 2 {
			close(finished)
		}
	}
	s := &connectionProcess{tracker: p.processes}
	s.start()
	s.publish(&Exchange{ID: 1})
	s.publish(&Exchange{ID: 2})
	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("callback deadlocked while closing proxy")
	}
	var count int
	for range got.ch {
		count++
	}
	if count != 2 {
		t.Fatalf("published %d exchanges, want both before callback closes hub", count)
	}
}

func TestProcessSlowCallbackRetainsLookupCapacity(t *testing.T) {
	returnNative := make(chan struct{})
	nativeReturned := make(chan struct{})
	callbackEntered := make(chan struct{})
	returnCallback := make(chan struct{})
	defer close(returnCallback)
	var calls atomic.Int32
	tracker, got := testProcessTracker(t, func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error) {
		calls.Add(1)
		<-returnNative
		close(nativeReturned)
		return processIdentity{}, nil
	})
	tracker.timeout = 10 * time.Millisecond
	tracker.slots = make(chan struct{}, 1)
	tracker.notify = func(x *Exchange) {
		if x.ID == 1 {
			close(callbackEntered)
			<-returnCallback
		}
	}
	s := &connectionProcess{tracker: tracker}
	s.start()
	s.publish(&Exchange{ID: 1})
	<-callbackEntered
	close(returnNative)
	<-nativeReturned
	other := &connectionProcess{tracker: tracker}
	other.start()
	other.publish(&Exchange{ID: 2})
	for range 2 {
		nextProcessExchange(t, got)
	}
	if calls.Load() != 1 || len(tracker.slots) != 1 {
		t.Fatal("lookup capacity released while deadline callback was still blocked")
	}
}

func TestProcessLookupDoesNotDelayForwarding(t *testing.T) {
	got := make(chan *Exchange, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "complete") }))
	defer up.Close()
	p, err := New(Options{Routes: []Route{{Name: "test", Prefix: "/test", Upstream: up.URL}}, OnExchange: func(x *Exchange) { got <- x }})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	p.processes.lookup = func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error) {
		<-release
		return processIdentity{name: "client", pid: 9}, nil
	}
	p.processes.timeout = time.Hour
	srv := httptest.NewUnstartedServer(p)
	srv.Config.ConnContext = p.ConnContext
	srv.Start()
	defer srv.Close()
	defer p.Close()
	defer close(release)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(srv.URL + "/test/x")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(body) != "complete" {
		t.Fatalf("client did not receive complete response: %q, %v", body, err)
	}
	select {
	case <-got:
		t.Fatal("exchange published before metadata resolved")
	default:
	}
	p.Close()
	x := nextProcessExchange(t, got)
	if x.Process != "" || x.Status != 200 || x.RespBody.Text != "complete" {
		t.Fatalf("shutdown lost capture: %+v", x)
	}
}

func TestProcessControlRequestsDoNotLookup(t *testing.T) {
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.processes.lookup = func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error) {
		t.Error("lookup started for control or rejected request")
		return processIdentity{}, nil
	}
	for _, path := range []string{PingPath, "/unknown"} {
		s := &connectionProcess{tracker: p.processes}
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		r = r.WithContext(context.WithValue(r.Context(), processContextKey{}, s))
		p.ServeHTTP(httptest.NewRecorder(), r)
	}
}

func TestExchangeProcessJSONCompatibility(t *testing.T) {
	var old Exchange
	if err := json.Unmarshal([]byte(`{"id":1,"route":"anthropic"}`), &old); err != nil || old.Process != "" || old.PID != 0 {
		t.Fatalf("older exchange: %+v, %v", old, err)
	}
	x := Exchange{Process: "claude", PID: 42}
	data, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Exchange
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Process != x.Process || decoded.PID != x.PID {
		t.Fatalf("attribution round trip: %+v, %v", decoded, err)
	}
}

func TestConnContextUsesConnectionEndpoints(t *testing.T) {
	p, _ := New(Options{})
	defer p.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx := p.ConnContext(context.Background(), server)
	s := ctx.Value(processContextKey{}).(*connectionProcess)
	if s.local.String() != server.LocalAddr().String() || s.remote.String() != server.RemoteAddr().String() {
		t.Fatalf("wrong endpoints: %v, %v", s.local, s.remote)
	}
	other := p.ConnContext(context.Background(), server).Value(processContextKey{}).(*connectionProcess)
	if other == s {
		t.Fatal("reused connection state")
	}
}
