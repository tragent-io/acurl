package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tragent-io/acurl/internal/output"
	"github.com/tragent-io/acurl/internal/tap"
)

// freeAddr returns a loopback address nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestTapStartsProxyWatchesAndStops(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LocalAppData", cache)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"content":"hi"}`)
	}))
	defer upstream.Close()

	addr := freeAddr(t)
	jsonl := t.TempDir() + "/s.jsonl"
	var out, errb bytes.Buffer
	a := &app{info: BuildInfo{Version: "test"}, stdout: &out, stderr: &errb, getenv: os.Getenv}
	var started []string
	a.startTapProcess = func(args []string, _ string) error {
		started = args
		ctx, stop := context.WithCancel(context.Background())
		p, err := tap.New(tap.Options{
			Routes:  []tap.Route{{Name: "anthropic", Prefix: "/anthropic", Upstream: upstream.URL}},
			Version: "test",
			OnStop:  stop,
		})
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		go func() { _ = serveUntilDone(ctx, ln, p, p.Close) }()
		return nil
	}

	root := newRootCmd(a)
	root.SetArgs([]string{"tap", "--listen", addr, "-w", jsonl, "--redact", "acme"})
	root.SetOut(&out)
	root.SetErr(&errb)
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(context.Background()) }()

	waitUntil(t, func() bool {
		info, err := pingTap(context.Background(), addr)
		return err == nil && info.Viewers == 1
	})
	if strings.Join(started, " ") != "tap serve --listen "+addr+" --max-body 1048576" {
		t.Errorf("started %q", started)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/anthropic/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5","system":"acme rules"}`))
	req.Header.Set("X-Api-Key", "sk-ant-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	waitUntil(t, func() bool {
		b, _ := os.ReadFile(jsonl)
		return bytes.Contains(b, []byte("\n"))
	})
	if code, _, stderr := run(t, "tap", "stop", "--listen", addr); code != 0 {
		t.Fatalf("tap stop: exit %d: %s", code, stderr)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tap: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tap kept running after the proxy stopped")
	}

	if !strings.Contains(out.String(), "anthropic POST /v1/messages claude-opus-5-5 200") {
		t.Errorf("stdout:\n%s", out.String())
	}
	b, _ := os.ReadFile(jsonl)
	var x tap.Exchange
	if err := json.Unmarshal(bytes.TrimSpace(b), &x); err != nil {
		t.Fatalf("jsonl: %v\n%s", err, b)
	}
	if x.ReqHeader.Get("X-Api-Key") != tap.Redacted || strings.Contains(x.ReqBody.Text, "acme") {
		t.Errorf("secrets left in capture: %s", b)
	}
	if !strings.Contains(errb.String(), "started the acurl tap proxy") || !strings.Contains(errb.String(), "the proxy stopped") {
		t.Errorf("stderr:\n%s", errb.String())
	}
}

func TestTapStatusAndStopWhenNotRunning(t *testing.T) {
	addr := freeAddr(t)
	if code, stdout, _ := run(t, "tap", "status", "--listen", addr); code != 1 || !strings.Contains(stdout, "not running") {
		t.Errorf("status: code %d, %q", code, stdout)
	}
	if code, _, stderr := run(t, "tap", "stop", "--listen", addr); code != 0 || !strings.Contains(stderr, "not running") {
		t.Errorf("stop: code %d, %q", code, stderr)
	}
}

func TestTapRefusesForeignListener(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if code, _, stderr := run(t, "tap", "--listen", addr); code != output.ExitError || !strings.Contains(stderr, "something other than acurl tap") {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}

func TestTapRejectsInvalidProcessBeforeStartingProxy(t *testing.T) {
	var out, errb bytes.Buffer
	a := &app{stdout: &out, stderr: &errb, getenv: os.Getenv}
	a.startTapProcess = func(_ []string, _ string) error {
		t.Fatal("started proxy before validating --process")
		return nil
	}
	root := newRootCmd(a)
	root.SetArgs([]string{"tap", "--listen", freeAddr(t), "--process", "node:0"})
	root.SetOut(&out)
	root.SetErr(&errb)
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid --process") {
		t.Fatalf("invalid selector error = %v", err)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
