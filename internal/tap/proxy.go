package tap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultMaxBody is how much of each body a capture keeps.
const DefaultMaxBody = 1 << 20

// Options configures a Proxy.
type Options struct {
	Routes  []Route // DefaultRoutes when empty
	MaxBody int     // DefaultMaxBody when zero
	// KeepCredentials leaves API keys, tokens and cookies in captures.
	KeepCredentials bool
	// Secrets are extra strings removed from headers, URLs and bodies.
	Secrets []string
	// OnExchange receives every finished exchange after publication, as do
	// viewers connected to EventsPath. It is called from many goroutines at
	// once; the exchange must be treated as read-only.
	OnExchange func(*Exchange)
	// OnStop is called when a client asks the proxy to stop through
	// StopPath. Without it, StopPath is refused.
	OnStop func()
	// Version is reported by PingPath.
	Version string
	// Transport sends requests upstream; a clone of http.DefaultTransport
	// when nil.
	Transport http.RoundTripper
}

// Proxy forwards CLI requests to their providers and records each exchange.
type Proxy struct {
	opts      Options
	rp        *httputil.ReverseProxy
	red       redactor
	hub       hub
	nextID    atomic.Int64
	processes *processTracker
}

type ctxKey struct{}

// pending is an exchange in flight, carried in the request context.
type pending struct {
	x        Exchange
	route    Route
	req      *capture
	resp     *capture
	finished sync.Once
	process  *connectionProcess
}

// New returns a Proxy.
func New(opts Options) (*Proxy, error) {
	if len(opts.Routes) == 0 {
		opts.Routes = DefaultRoutes
	}
	if opts.MaxBody == 0 {
		opts.MaxBody = DefaultMaxBody
	}
	if opts.Transport == nil {
		opts.Transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	for _, r := range opts.Routes {
		if u, err := url.Parse(r.Upstream); err != nil || u.Host == "" {
			return nil, fmt.Errorf("route %s: invalid upstream %q", r.Name, r.Upstream)
		}
		if !strings.HasPrefix(r.Prefix, "/") || strings.HasSuffix(r.Prefix, "/") {
			return nil, fmt.Errorf("route %s: prefix %q must start and not end with /", r.Name, r.Prefix)
		}
	}
	p := &Proxy{opts: opts, red: redactor{credentials: !opts.KeepCredentials, secrets: opts.Secrets}}
	p.processes = newProcessTracker(p.emit)
	p.processes.notify = opts.OnExchange
	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		Transport:      opts.Transport,
		FlushInterval:  -1, // pass server-sent events through as they arrive
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   p.errorHandler,
	}
	return p, nil
}

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Browsers send Origin; CLIs do not. Refusing it keeps web pages from
	// using the proxy through DNS rebinding or a cross-site POST.
	if r.Header.Get("Origin") != "" || !loopbackHost(r.Host) {
		http.Error(w, "acurl tap only serves local command line tools", http.StatusForbidden)
		return
	}
	if p.serveControl(w, r) {
		return
	}
	route, ok := p.match(r.URL.Path)
	if !ok {
		http.Error(w, fmt.Sprintf("acurl tap: no route for %s; routes are %s", r.URL.Path, p.prefixes()), http.StatusNotFound)
		return
	}
	pd := &pending{
		x: Exchange{
			ID:        p.nextID.Add(1),
			Start:     time.Now(),
			Route:     route.Name,
			Method:    r.Method,
			ReqHeader: r.Header.Clone(),
		},
		route: route,
		req:   &capture{max: p.opts.MaxBody},
		resp:  &capture{max: p.opts.MaxBody},
	}
	pd.process, _ = r.Context().Value(processContextKey{}).(*connectionProcess)
	if pd.process != nil {
		pd.process.start()
	}
	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, pd)))
}

func (p *Proxy) match(path string) (Route, bool) {
	for _, r := range p.opts.Routes {
		if path == r.Prefix || strings.HasPrefix(path, r.Prefix+"/") {
			return r, true
		}
	}
	return Route{}, false
}

func (p *Proxy) prefixes() string {
	out := make([]string, len(p.opts.Routes))
	for i, r := range p.opts.Routes {
		out[i] = r.Prefix
	}
	return strings.Join(out, ", ")
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	pd := pr.In.Context().Value(ctxKey{}).(*pending)
	up, _ := url.Parse(pd.route.Upstream)
	pr.SetURL(up)
	pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, pd.route.Prefix)
	pr.Out.URL.RawPath = ""
	if pr.Out.URL.Path == "" {
		pr.Out.URL.Path = "/"
	}
	// Let the transport negotiate gzip and decode it, so captures are
	// readable and the CLI still gets a plain response.
	pr.Out.Header.Del("Accept-Encoding")
	if pr.Out.Body != nil && pr.Out.Body != http.NoBody {
		pr.Out.Body = teeReadCloser{Reader: io.TeeReader(pr.Out.Body, pd.req), Closer: pr.Out.Body}
	}
	pd.x.URL = pr.Out.URL.String()
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	pd := resp.Request.Context().Value(ctxKey{}).(*pending)
	pd.x.Status = resp.StatusCode
	pd.x.RespHeader = resp.Header.Clone()
	pd.x.Streaming = strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	if resp.StatusCode == http.StatusSwitchingProtocols {
		// A WebSocket: ReverseProxy needs the body to stay writable to
		// splice the two connections, so record the handshake and let the
		// frames through unrecorded.
		pd.x.Upgrade = resp.Header.Get("Upgrade")
		p.finish(pd, nil)
		return nil
	}
	resp.Body = &finishingBody{
		Reader: io.TeeReader(resp.Body, pd.resp),
		body:   resp.Body,
		finish: func(err error) { p.finish(pd, err) },
	}
	return nil
}

func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	pd := r.Context().Value(ctxKey{}).(*pending)
	p.finish(pd, err)
	http.Error(w, "acurl tap: "+err.Error(), http.StatusBadGateway)
}

func (p *Proxy) finish(pd *pending, err error) {
	pd.finished.Do(func() {
		x := pd.x
		x.Duration = time.Since(x.Start)
		x.ReqBody = decode(pd.req.body(), x.ReqHeader.Get("Content-Encoding"), p.opts.MaxBody)
		x.RespBody = decode(pd.resp.body(), x.RespHeader.Get("Content-Encoding"), p.opts.MaxBody)
		switch {
		case err == nil, errors.Is(err, io.EOF):
		case errors.Is(err, context.Canceled):
			x.Canceled = true
		default:
			x.Err = err.Error()
		}
		u, _ := url.Parse(x.URL)
		if u != nil {
			x.Model = modelOf(u.Path, x.ReqBody)
		}
		if pd.process != nil {
			pd.process.publish(&x)
		} else {
			p.publish(&x)
		}
	})
}

func (p *Proxy) publish(x *Exchange) {
	p.emit(x)
	if p.opts.OnExchange != nil {
		p.opts.OnExchange(x)
	}
}

func (p *Proxy) emit(x *Exchange) {
	p.red.apply(x)
	p.hub.publish(x)
}

type teeReadCloser struct {
	io.Reader
	io.Closer
}

// finishingBody records the exchange once the response body is read to the
// end or closed, which ReverseProxy always does.
type finishingBody struct {
	io.Reader
	body   io.Closer
	finish func(error)
}

func (b *finishingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err != nil {
		b.finish(err)
	}
	return n, err
}

func (b *finishingBody) Close() error {
	err := b.body.Close()
	b.finish(nil)
	return err
}

// loopbackHost reports whether a Host header names this machine.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
