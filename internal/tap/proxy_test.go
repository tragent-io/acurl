package tap

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// harness runs a proxy with one "test" route in front of upstream and
// collects the exchanges it records.
type harness struct {
	proxy *httptest.Server
	mu    sync.Mutex
	got   []*Exchange
	done  chan struct{}
}

func newHarness(t *testing.T, upstream http.Handler, opts Options) *harness {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	h := &harness{done: make(chan struct{}, 16)}
	opts.Routes = []Route{{Name: "test", Prefix: "/test", Upstream: up.URL}}
	opts.OnExchange = func(x *Exchange) {
		h.mu.Lock()
		h.got = append(h.got, x)
		h.mu.Unlock()
		h.done <- struct{}{}
	}
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	h.proxy = httptest.NewUnstartedServer(p)
	h.proxy.Config.ConnContext = p.ConnContext
	h.proxy.Start()
	t.Cleanup(h.proxy.Close)
	t.Cleanup(p.Close)
	return h
}

// next waits for the next recorded exchange.
func (h *harness) next(t *testing.T) *Exchange {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no exchange recorded")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.got[len(h.got)-1]
}

func TestProxyForwardsAndRecords(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/messages" || r.URL.RawQuery != "beta=true" {
			t.Errorf("upstream got %s, want /v1/messages?beta=true", r.URL)
		}
		if r.Header.Get("X-Api-Key") != "sk-secret" {
			t.Errorf("upstream x-api-key = %q, want the real key", r.Header.Get("X-Api-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append([]byte(`{"echo":`), append(body, '}')...))
	}), Options{})

	req, _ := http.NewRequest(http.MethodPost, h.proxy.URL+"/test/v1/messages?beta=true", strings.NewReader(`{"model":"claude-opus-5-5","max_tokens":5}`))
	req.Header.Set("X-Api-Key", "sk-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if want := `{"echo":{"model":"claude-opus-5-5","max_tokens":5}}`; string(got) != want {
		t.Errorf("client got %s, want %s", got, want)
	}

	x := h.next(t)
	if x.Route != "test" || x.Method != http.MethodPost || x.Status != 200 || x.Model != "claude-opus-5-5" {
		t.Errorf("exchange = %+v", x)
	}
	if !strings.HasSuffix(x.URL, "/v1/messages?beta=true") {
		t.Errorf("URL = %q", x.URL)
	}
	if x.ReqBody.Text != `{"model":"claude-opus-5-5","max_tokens":5}` || x.RespBody.Text != string(got) {
		t.Errorf("bodies = %q / %q", x.ReqBody.Text, x.RespBody.Text)
	}
	if v := x.ReqHeader.Get("X-Api-Key"); v != Redacted {
		t.Errorf("recorded x-api-key = %q, want %q", v, Redacted)
	}
}

func TestProxyStreamsEventsAsTheyArrive(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: first\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "event: last\ndata: {}\n\n")
	}), Options{})

	resp, err := http.Post(h.proxy.URL+"/test/v1/messages", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "event: first\n" {
		t.Fatalf("first line = %q, %v: events are being buffered", line, err)
	}
	close(release)
	_, _ = io.Copy(io.Discard, resp.Body)

	x := h.next(t)
	if !x.Streaming || !strings.Contains(x.RespBody.Text, "event: last") {
		t.Errorf("exchange = streaming %v, body %q", x.Streaming, x.RespBody.Text)
	}
}

func TestProxyDecodesGzipForCaptureAndClient(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Errorf("upstream Accept-Encoding = %q, want gzip", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write([]byte(`{"ok":true}`))
		_ = zw.Close()
	}), Options{})

	req, _ := http.NewRequest(http.MethodGet, h.proxy.URL+"/test/v1/models", nil)
	req.Header.Set("Accept-Encoding", "br")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != `{"ok":true}` {
		t.Errorf("client got %q", got)
	}
	if x := h.next(t); x.RespBody.Text != `{"ok":true}` {
		t.Errorf("captured %q", x.RespBody.Text)
	}
}

func TestProxyTruncatesLargeBodies(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("b"), 100))
	}), Options{MaxBody: 10})

	resp, err := http.Post(h.proxy.URL+"/test/x", "text/plain", strings.NewReader(strings.Repeat("a", 50)))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(got) != 100 {
		t.Errorf("client got %d bytes, want all 100", len(got))
	}
	x := h.next(t)
	if x.ReqBody != (Body{Text: "aaaaaaaaaa", Size: 50, Truncated: true}) {
		t.Errorf("request body = %+v", x.ReqBody)
	}
	if x.RespBody.Size != 100 || len(x.RespBody.Text) != 10 || !x.RespBody.Truncated {
		t.Errorf("response body = %+v", x.RespBody)
	}
}

func TestProxyRecordsUpstreamErrors(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	up.Close() // nothing listens there now
	var got *Exchange
	p, err := New(Options{
		Routes:     []Route{{Name: "down", Prefix: "/down", Upstream: up.URL}},
		OnExchange: func(x *Exchange) { got = x },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8899/down/v1", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if got == nil || got.Err == "" {
		t.Errorf("exchange = %+v, want an error", got)
	}
}

func TestProxyRefusesNonCLIRequests(t *testing.T) {
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		target string
		origin string
		want   int
	}{
		{"ping", "http://127.0.0.1:8899" + PingPath, "", http.StatusOK},
		{"localhost ping", "http://localhost:8899" + PingPath, "", http.StatusOK},
		{"unknown route", "http://127.0.0.1:8899/nope/v1", "", http.StatusNotFound},
		{"browser origin", "http://127.0.0.1:8899" + PingPath, "https://evil.example", http.StatusForbidden},
		{"rebound host", "http://evil.example:8899" + PingPath, "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestNewRejectsBadRoutes(t *testing.T) {
	for _, r := range []Route{
		{Name: "a", Prefix: "/a", Upstream: "not a url"},
		{Name: "b", Prefix: "b", Upstream: "https://example.com"},
		{Name: "c", Prefix: "/c/", Upstream: "https://example.com"},
	} {
		if _, err := New(Options{Routes: []Route{r}}); err == nil {
			t.Errorf("New(%+v) = nil error", r)
		}
	}
}

func TestModelOf(t *testing.T) {
	tests := []struct {
		path string
		body string
		want string
	}{
		{"/v1/messages", `{"model":"claude-opus-5-5"}`, "claude-opus-5-5"},
		{"/v1beta/models/gemini-3-pro:streamGenerateContent", `{}`, "gemini-3-pro"},
		{"/v1/models", "", ""},
		{"/v1/messages", `not json`, ""},
		{"/v1/messages", `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"cut off he`, "claude-opus-5-5"},
		{"/v1/messages", `{"messages":[],"model":"a\"b"}`, `a"b`},
	}
	for _, tt := range tests {
		if got := modelOf(tt.path, Body{Text: tt.body, Size: int64(len(tt.body))}); got != tt.want {
			t.Errorf("modelOf(%q, %q) = %q, want %q", tt.path, tt.body, got, tt.want)
		}
	}
}

func TestProxyDecodesCompressedRequests(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "zstd" {
			t.Errorf("upstream Content-Encoding = %q, want zstd passed through", r.Header.Get("Content-Encoding"))
		}
		_, _ = io.Copy(io.Discard, r.Body)
	}), Options{})

	enc, _ := zstd.NewWriter(nil)
	body := enc.EncodeAll([]byte(`{"model":"grok-4.6","input":[]}`), nil)
	req, _ := http.NewRequest(http.MethodPost, h.proxy.URL+"/test/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "zstd")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	x := h.next(t)
	if x.Model != "grok-4.6" || x.ReqBody.Encoding != "zstd" || x.ReqBody.Size != int64(len(body)) {
		t.Errorf("exchange model %q, body %+v", x.Model, x.ReqBody)
	}
}

func TestProxyMarksClientCancels(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}), Options{})

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.proxy.URL+"/test/v1/messages", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bufio.NewReader(resp.Body).ReadString('\n')
	cancel()
	resp.Body.Close()

	if x := h.next(t); !x.Canceled || x.Err != "" {
		t.Errorf("exchange canceled %v, error %q", x.Canceled, x.Err)
	}
}

func TestDecode(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("hello world"))
	_ = zw.Close()
	tests := []struct {
		name     string
		in       Body
		encoding string
		want     Body
	}{
		{"identity", Body{Text: "x", Size: 1}, "", Body{Text: "x", Size: 1}},
		{"gzip", Body{Text: gz.String(), Size: int64(gz.Len())}, "gzip", Body{Text: "hello world", Size: int64(gz.Len()), Encoding: "gzip"}},
		{"gzip over limit", Body{Text: gz.String(), Size: int64(gz.Len())}, "gzip", Body{Text: "hello", Size: int64(gz.Len()), Encoding: "gzip", Truncated: true}},
		{"not gzip", Body{Text: "plain", Size: 5}, "gzip", Body{Text: "plain", Size: 5}},
		{"unknown", Body{Text: "x", Size: 1}, "compress", Body{Text: "x", Size: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := 100
			if tt.name == "gzip over limit" {
				limit = 5
			}
			if got := decode(tt.in, tt.encoding, limit); got != tt.want {
				t.Errorf("decode = %+v, want %+v", got, tt.want)
			}
		})
	}
}
