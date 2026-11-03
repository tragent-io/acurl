package tap

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Discovery is best effort: never queue OS work behind other connections or
// hold an unbounded number of captured bodies while discovery is pending.
const (
	processLookupTimeout = 500 * time.Millisecond
	processLookupLimit   = 4
	processPendingLimit  = 8
)

type processIdentity struct {
	name string
	pid  int
}

type processContextKey struct{}

type processTracker struct {
	mu      sync.Mutex
	active  map[*connectionProcess]struct{}
	closed  bool
	slots   chan struct{}
	pending sync.WaitGroup
	lookup  func(context.Context, netip.AddrPort, netip.AddrPort) (processIdentity, error)
	timeout time.Duration
	emit    func(*Exchange)
	notify  func(*Exchange)
}

func newProcessTracker(emit func(*Exchange)) *processTracker {
	return &processTracker{
		active: make(map[*connectionProcess]struct{}),
		slots:  make(chan struct{}, processLookupLimit),
		lookup: lookupProcess, timeout: processLookupTimeout, emit: emit,
	}
}

// ConnContext attaches a fresh attribution cache to each accepted TCP
// connection. Set http.Server.ConnContext to this method when serving p.
// Discovery starts only when a provider request is accepted, not for control
// endpoints, and never blocks forwarding. Without this hook, attribution is empty.
func (p *Proxy) ConnContext(ctx context.Context, conn net.Conn) context.Context {
	local, lok := conn.LocalAddr().(*net.TCPAddr)
	remote, rok := conn.RemoteAddr().(*net.TCPAddr)
	if !lok || !rok {
		return ctx
	}
	s := &connectionProcess{tracker: p.processes, local: local.AddrPort(), remote: remote.AddrPort()}
	return context.WithValue(ctx, processContextKey{}, s)
}

type connectionProcess struct {
	tracker       *processTracker
	local, remote netip.AddrPort
	once          sync.Once
	mu            sync.Mutex
	ready         bool
	identity      processIdentity
	waiting       []*Exchange
	cancel        context.CancelFunc
	done          chan struct{}
}

func (s *connectionProcess) start() {
	s.once.Do(func() {
		s.done = make(chan struct{})
		t := s.tracker
		t.mu.Lock()
		if t.closed || t.lookup == nil {
			t.mu.Unlock()
			s.complete(processIdentity{})
			return
		}
		select {
		case t.slots <- struct{}{}:
		default:
			t.mu.Unlock()
			s.complete(processIdentity{})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
		s.cancel = cancel
		t.active[s] = struct{}{}
		t.mu.Unlock()

		// The deadline watcher also finalizes captures when a native system call
		// cannot be interrupted. Its slot stays occupied until that call returns,
		// so even stalled native calls cannot create unlimited goroutines.
		go func() {
			<-ctx.Done()
			s.complete(processIdentity{})
		}()
		go func() {
			identity, err := t.lookup(ctx, s.local, s.remote)
			if err != nil || ctx.Err() != nil || identity.name == "" || identity.pid <= 0 {
				identity = processIdentity{}
			}
			s.complete(identity)
			cancel()
			// A deadline watcher may own finalization. Keep its capacity until
			// it also finishes callbacks, even if the native lookup has returned.
			<-s.done
			<-t.slots
			t.mu.Lock()
			delete(t.active, s)
			t.mu.Unlock()
		}()
	})
}

// publish never waits for discovery. A completed exchange can wait for metadata
// off the forwarding path; excess captures are published without attribution.
func (s *connectionProcess) publish(x *Exchange) {
	s.mu.Lock()
	if !s.ready && len(s.waiting) < processPendingLimit {
		s.tracker.pending.Add(1)
		s.waiting = append(s.waiting, x)
		s.mu.Unlock()
		return
	}
	identity := s.identity
	s.mu.Unlock()
	x.Process, x.PID = identity.name, identity.pid
	s.tracker.emit(x)
	if s.tracker.notify != nil {
		s.tracker.notify(x)
	}
}

func (s *connectionProcess) complete(identity processIdentity) {
	s.mu.Lock()
	if s.ready {
		s.mu.Unlock()
		return
	}
	s.ready, s.identity = true, identity
	waiting := s.waiting
	s.waiting = nil
	s.mu.Unlock()
	defer close(s.done)
	for _, x := range waiting {
		x.Process, x.PID = identity.name, identity.pid
		s.tracker.emit(x)
		s.tracker.pending.Done()
	}
	// Deliver the whole batch before invoking callbacks: a callback can close
	// the proxy, and must not wait on its own (or this batch's) publication.
	if s.tracker.notify != nil {
		for _, x := range waiting {
			s.tracker.notify(x)
		}
	}
}

func (t *processTracker) close() {
	t.mu.Lock()
	t.closed = true
	active := make([]*connectionProcess, 0, len(t.active))
	for s := range t.active {
		active = append(active, s)
	}
	t.mu.Unlock()
	for _, s := range active {
		s.cancel()
		s.complete(processIdentity{})
	}
	t.pending.Wait()
}
