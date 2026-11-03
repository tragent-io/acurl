package tap

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
)

// Control endpoints served next to the provider routes.
const (
	PingPath   = "/_acurl/ping"   // GET: Info, so wrappers and viewers can tell the proxy is up
	EventsPath = "/_acurl/events" // GET: a JSON line per Event until the proxy stops
	StopPath   = "/_acurl/stop"   // POST: stop the proxy
)

// Info describes a running proxy.
type Info struct {
	Name            string `json:"name"` // always "acurl tap"
	Version         string `json:"version"`
	PID             int    `json:"pid"`
	MaxBody         int    `json:"max_body"`
	KeepCredentials bool   `json:"keep_credentials"`
	Viewers         int    `json:"viewers"`
}

// Event is one line of the events stream.
type Event struct {
	Exchange *Exchange `json:"exchange,omitempty"`
	// Dropped counts exchanges this viewer missed because it read too slowly.
	Dropped int64 `json:"dropped,omitempty"`
}

// subscriberBuffer is how many exchanges a slow viewer may fall behind by
// before it starts missing them. The proxy never waits for a viewer.
const subscriberBuffer = 1024

type subscriber struct {
	ch      chan *Exchange
	dropped atomic.Int64
}

// hub fans finished exchanges out to the connected viewers.
type hub struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
}

func (h *hub) subscribe() *subscriber {
	s := &subscriber{ch: make(chan *Exchange, subscriberBuffer)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(s.ch)
		return s
	}
	if h.subs == nil {
		h.subs = map[*subscriber]struct{}{}
	}
	h.subs[s] = struct{}{}
	return s
}

func (h *hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.ch)
	}
}

func (h *hub) publish(x *Exchange) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.ch <- x:
		default:
			s.dropped.Add(1)
		}
	}
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// close ends every events stream, so http.Server.Shutdown does not wait
// for viewers that would never hang up on their own.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		close(s.ch)
	}
	h.subs = nil
}

func (p *Proxy) serveControl(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case PingPath:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Info{
			Name: "acurl tap", Version: p.opts.Version, PID: os.Getpid(),
			MaxBody: p.opts.MaxBody, KeepCredentials: p.opts.KeepCredentials, Viewers: p.hub.count(),
		})
	case EventsPath:
		p.serveEvents(w, r)
	case StopPath:
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return true
		}
		if p.opts.OnStop == nil {
			http.Error(w, "this proxy cannot be stopped remotely", http.StatusNotImplemented)
			return true
		}
		w.WriteHeader(http.StatusNoContent)
		go p.opts.OnStop()
	default:
		return false
	}
	return true
}

func (p *Proxy) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	s := p.hub.subscribe()
	defer p.hub.unsubscribe(s)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case x, ok := <-s.ch:
			if !ok {
				return
			}
			if n := s.dropped.Swap(0); n > 0 {
				if enc.Encode(Event{Dropped: n}) != nil {
					return
				}
			}
			if enc.Encode(Event{Exchange: x}) != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// Close ends every events stream. Call it before shutting the server down.
func (p *Proxy) Close() {
	p.processes.close()
	p.hub.close()
}
