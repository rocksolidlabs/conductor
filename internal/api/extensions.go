package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// Extensions: the providers the composition root read from the registry
// (package ext), each wrapped in the guard. The server holds a provider in
// every slot, the free default (ext/local) where nothing is registered, and
// calls none of them past the guard: a direct call goes through extCall,
// which bounds it by its wait, recovers its panic, checks its answer and
// answers the free default's instead on any failure; a call fed from a
// queue (Notify, NoteTouch) goes through an extQueue, which never makes its
// producer wait. The guard keeps nothing about a failure but the
// once-a-minute log, so a provider whose service comes back is used again
// at its next call, with no restart (article 8 of the Program).

// Option configures a server New makes.
type Option func(*options)

// options is what the options of New set.
type options struct {
	providers ext.Providers
}

// WithExtensions gives the server the providers p, as the composition root
// read them and local.Fill filled them: it asks nothing about them. Without
// it, the server holds local.Fill(ext.Providers{}), the free build.
func WithExtensions(p ext.Providers) Option {
	return func(o *options) { o.providers = p }
}

// extInFlight bounds the direct calls of one provider and kind that run at
// once: past it, the free default is answered at once. A call that hangs
// holds its slot until it returns, so a hung provider holds at most this
// many goroutines of one kind.
const extInFlight = 16

// extLogEvery is how often a provider's failures of one kind are logged.
const extLogEvery = time.Minute

// extErrText bounds what a failure's log quotes of a provider's error.
const extErrText = 200

// extSlots is what the server holds of the providers, each behind the guard.
type extSlots struct {
	identity ext.IdentityProvider
	router   ext.DecisionRouter
	context  []ext.ContextProvider
	sinks    []ext.NotificationSink
	sync     ext.ContinuityStore
	media    ext.MediaRoomProvider
	// all is every registered provider (ext.Providers.All), unwrapped: what
	// the providers' Start (PR-4) and GET /api/capabilities (C9) read. No
	// call is made through it.
	all []ext.Entry
}

// extGuard is what the guard keeps per provider and kind: the slots of its
// calls in flight, and when its failures were last logged and how many
// there were since. Nothing else about a failure is kept.
type extGuard struct {
	slots chan struct{}

	mu    sync.Mutex
	last  time.Time // when a failure was last logged; zero for never
	since int       // failures not yet logged
}

// installExtensions wraps every provider of p in the guard and holds it,
// defaults included, so no call site tells a default from a provider. A
// queue-fed provider's queues drain at once when it is no ext.Starter;
// a Starter's wait until its Start has succeeded (beginQueues).
func (s *Server) installExtensions(p ext.Providers) {
	s.extCtx, s.extStop = context.WithCancel(context.Background())
	s.extQueues = map[string][]func(){}
	s.exts = extSlots{
		identity: guardIdentity{s: s, name: providerName(p.Identity), p: p.Identity},
		router:   guardRouter{s: s, name: providerName(p.Router), p: p.Router},
		sync:     guardSync{s: s, name: providerName(p.Continuity), p: p.Continuity},
		media:    guardMedia{s: s, name: providerName(p.Media), p: p.Media},
		all:      append([]ext.Entry(nil), p.All...),
	}
	for _, c := range p.Context {
		g := &guardContext{s: s, name: providerName(c), p: c}
		g.touches = newExtQueue(s, ext.KindContext, g.name, ext.TouchQueue, func(ctx context.Context, f ext.TouchedFile) error {
			c.NoteTouch(ctx, f)
			return nil
		})
		s.holdQueue(c, g.name, g.touches.begin)
		s.exts.context = append(s.exts.context, g)
	}
	for _, n := range p.Notifications {
		g := &guardSink{s: s, name: providerName(n), p: n}
		g.queue = newExtQueue(s, ext.KindNotifications, g.name, ext.SinkQueue, n.Notify)
		s.holdQueue(n, g.name, g.queue.begin)
		s.exts.sinks = append(s.exts.sinks, g)
	}
}

// holdQueue begins a queue of the provider v named name at once when v is
// no ext.Starter, and keeps it for beginQueues otherwise.
func (s *Server) holdQueue(v any, name string, begin func()) {
	if _, ok := v.(ext.Starter); !ok {
		begin()
		return
	}
	s.extMu.Lock()
	s.extQueues[name] = append(s.extQueues[name], begin)
	s.extMu.Unlock()
}

// beginQueues begins the drains of the queues the provider name holds,
// once its Start has succeeded (PR-4). It is safe to call more than once.
func (s *Server) beginQueues(name string) {
	s.extMu.Lock()
	begins := s.extQueues[name]
	delete(s.extQueues, name)
	s.extMu.Unlock()
	for _, b := range begins {
		b()
	}
}

// stopExtensions ends the queues' drains and every delivery in flight:
// Shutdown and WaitBackground call it before they wait for the server's
// goroutines (track). What is still queued is not delivered.
func (s *Server) stopExtensions() {
	if s.extStop != nil {
		s.extStop()
	}
}

// providerName is p's name, read without trusting p: a name that panics or
// that ext.ValidName refuses is "invalid". A registered provider's name was
// checked when it registered; this reads it again only for the logs.
func providerName(p ext.Provider) (name string) {
	defer func() {
		if recover() != nil {
			name = "invalid"
		}
	}()
	if n := p.Name(); ext.ValidName(n) {
		return n
	}
	return "invalid"
}

// guard is the guard's state for the provider name of kind.
func (s *Server) guard(kind, name string) *extGuard {
	s.extMu.Lock()
	defer s.extMu.Unlock()
	if s.extGuards == nil {
		s.extGuards = map[string]*extGuard{}
	}
	key := kind + "/" + name
	g := s.extGuards[key]
	if g == nil {
		g = &extGuard{slots: make(chan struct{}, extInFlight)}
		s.extGuards[key] = g
	}
	return g
}

// extFailed notes a failed call of the provider name of kind, and logs it
// at warn when its last log is a minute old or more, with how many failed
// since. The free default has been answered.
func (s *Server) extFailed(kind, name, why string, err error) {
	g := s.guard(kind, name)
	now := time.Now()
	if s.extClock != nil {
		now = s.extClock()
	}
	g.mu.Lock()
	g.since++
	if !g.last.IsZero() && now.Sub(g.last) < extLogEvery {
		g.mu.Unlock()
		return
	}
	failures := g.since
	g.since, g.last = 0, now
	g.mu.Unlock()
	attrs := []any{"kind", kind, "provider", name, "failure", why, "failures", failures}
	if err != nil {
		msg := err.Error()
		if len(msg) > extErrText {
			msg = msg[:extErrText] + "…"
		}
		attrs = append(attrs, "err", msg)
	}
	s.log.Warn("extension call failed: its free default answered", attrs...)
}

// extCall runs fn, a call of the provider name of kind, within wait and
// answers its result, or fallback when fn errs, panics, returns what valid
// refuses (a nil valid takes everything), or has not returned by then. The
// call's context is ctx's, ending after wait and once extCall returns, so a
// late fn's goroutine finishes on its own and its answer is not used. At
// most extInFlight calls of one provider and kind run at once: past it,
// fallback at once. A failure is logged at warn once a minute per provider
// and kind (extFailed); one that ends because ctx did, the caller gone, is
// not the provider's and is not logged.
//
// The plan's extCall takes no ctx; this one takes the caller's, so a
// request that ends ends its provider call too.
func extCall[T any](ctx context.Context, s *Server, kind, name string, wait time.Duration, fallback T, valid func(T) error, fn func(context.Context) (T, error)) T {
	g := s.guard(kind, name)
	select {
	case g.slots <- struct{}{}:
	default:
		s.extFailed(kind, name, "busy", fmt.Errorf("%d calls in flight", extInFlight))
		return fallback
	}
	cctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	type result struct {
		v    T
		err  error
		late bool
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-g.slots }()
		var r result
		defer func() {
			if p := recover(); p != nil {
				r = result{err: fmt.Errorf("panic: %v", p)}
			}
			done <- r
		}()
		r.v, r.err = fn(cctx)
		// An answer given once its context ended is late, however soon
		// after: the caller may already have the fallback.
		r.late = cctx.Err() != nil
	}()
	var r result
	select {
	case r = <-done:
	case <-cctx.Done():
		r.late = true
	}
	switch {
	case r.late:
		if ctx.Err() == nil {
			s.extFailed(kind, name, "late", fmt.Errorf("no answer within %s", wait))
		}
		return fallback
	case r.err != nil:
		s.extFailed(kind, name, "error", r.err)
		return fallback
	}
	if valid != nil {
		if err := valid(r.v); err != nil {
			s.extFailed(kind, name, "invalid", err)
			return fallback
		}
	}
	return r.v
}

// extDeliver runs fn, a delivery to the provider name of kind, on the
// calling goroutine (a queue's drain, or a list that drops nothing) with a
// context that ends after ext.DeliveryWait or with ctx, and answers its
// error: fn's own, a panic recovered, or the context's end. A failure is
// logged as extCall logs one. A provider that ignores its context holds
// only the goroutine that delivers to it.
func extDeliver(ctx context.Context, s *Server, kind, name string, fn func(context.Context) error) (err error) {
	cctx, cancel := context.WithTimeout(ctx, ext.DeliveryWait)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
		if err == nil && cctx.Err() != nil {
			err = cctx.Err()
		}
		if err != nil && ctx.Err() == nil {
			s.extFailed(kind, name, "delivery", err)
		}
	}()
	return fn(cctx)
}

// extQueue holds what is to be delivered to one provider of one kind, in
// order: a ring filled without waiting, whose oldest entry is dropped when
// it is full (the first drop and every hundredth logged, as a webhook's
// queue does), drained by one goroutine of its own once begun, each
// delivery through extDeliver.
type extQueue[T any] struct {
	s       *Server
	kind    string
	name    string
	deliver func(context.Context, T) error

	mu      sync.Mutex
	ring    []T // n entries from head
	head, n int
	dropped uint64
	begun   bool
	wake    chan struct{} // something was queued
}

// newExtQueue is a queue of size entries delivered by deliver; it drains
// once begun.
func newExtQueue[T any](s *Server, kind, name string, size int, deliver func(context.Context, T) error) *extQueue[T] {
	return &extQueue[T]{s: s, kind: kind, name: name, deliver: deliver, ring: make([]T, size), wake: make(chan struct{}, 1)}
}

// push queues v, dropping the oldest entry when the ring is full. It never
// waits on the provider.
func (q *extQueue[T]) push(v T) {
	var zero T
	q.mu.Lock()
	dropped := uint64(0)
	if q.n == len(q.ring) {
		q.ring[q.head] = zero
		q.head = (q.head + 1) % len(q.ring)
		q.n--
		q.dropped++
		dropped = q.dropped
	}
	q.ring[(q.head+q.n)%len(q.ring)] = v
	q.n++
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	if dropped > 0 && (dropped == 1 || dropped%100 == 0) {
		q.s.log.Warn("extension queue full: dropped the oldest entry", "kind", q.kind, "provider", q.name, "dropped", dropped)
	}
}

// pop takes the oldest entry.
func (q *extQueue[T]) pop() (T, bool) {
	var zero T
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.n == 0 {
		return zero, false
	}
	v := q.ring[q.head]
	q.ring[q.head] = zero
	q.head = (q.head + 1) % len(q.ring)
	q.n--
	return v, true
}

// stats reports how many entries wait and how many were dropped so far.
func (q *extQueue[T]) stats() (queued int, dropped uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n, q.dropped
}

// begin starts the queue's drain, once, on a goroutine of the server's own
// (track) that ends when the server's extensions stop.
func (q *extQueue[T]) begin() {
	q.mu.Lock()
	if q.begun {
		q.mu.Unlock()
		return
	}
	q.begun = true
	q.mu.Unlock()
	ctx := q.s.extCtx
	q.s.track(func() { q.run(ctx) })
}

// run delivers what is queued, in order, until ctx ends.
func (q *extQueue[T]) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		v, ok := q.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			}
			continue
		}
		_ = extDeliver(ctx, q.s, q.kind, q.name, func(ctx context.Context) error { return q.deliver(ctx, v) })
	}
}

// errNoGrant is an identity provider's principal that may do nothing here.
var errNoGrant = errors.New("principal: no grant")

// guardIdentity is an identity provider behind the guard.
type guardIdentity struct {
	s    *Server
	name string
	p    ext.IdentityProvider
}

func (g guardIdentity) Name() string { return g.name }

// Authenticate answers the provider's principal for token, or ok false (as
// the free default answers) when it fails, is late or answers a principal
// that is not valid or holds no grant. It never answers an error.
func (g guardIdentity) Authenticate(ctx context.Context, token string) (ext.Principal, bool, error) {
	type answer struct {
		p  ext.Principal
		ok bool
	}
	a := extCall(ctx, g.s, ext.KindIdentity, g.name, ext.IdentityWait, answer{}, func(a answer) error {
		if !a.ok {
			return nil
		}
		if err := a.p.Valid(); err != nil {
			return err
		}
		if len(a.p.Grants) == 0 {
			return errNoGrant
		}
		return nil
	}, func(ctx context.Context) (answer, error) {
		p, ok, err := g.p.Authenticate(ctx, token)
		return answer{p, ok}, err
	})
	if !a.ok {
		return ext.Principal{}, false, nil
	}
	return a.p, true, nil
}

// guardRouter is a decision router behind the guard.
type guardRouter struct {
	s    *Server
	name string
	p    ext.DecisionRouter
}

func (g guardRouter) Name() string { return g.name }

// Route answers the provider's route for d, or the empty route (the free
// default's) when it fails, is late or answers a route Route.Valid refuses
// for d. It never answers an error.
func (g guardRouter) Route(ctx context.Context, d ext.Decision) (ext.Route, error) {
	return extCall(ctx, g.s, ext.KindDecisions, g.name, ext.RouteWait, ext.Route{}, func(r ext.Route) error {
		return r.Valid(d)
	}, func(ctx context.Context) (ext.Route, error) {
		return g.p.Route(ctx, d)
	}), nil
}

// Resolved tells the provider of r on the calling goroutine (the router's
// resolutions, which drop nothing, drain through it in order: C5), within
// ext.DeliveryWait, its panic recovered.
func (g guardRouter) Resolved(ctx context.Context, r ext.Resolution) {
	_ = extDeliver(ctx, g.s, ext.KindDecisions, g.name, func(ctx context.Context) error {
		g.p.Resolved(ctx, r)
		return nil
	})
}

// guardContext is a context provider behind the guard.
type guardContext struct {
	s       *Server
	name    string
	p       ext.ContextProvider
	touches *extQueue[ext.TouchedFile]
}

func (g *guardContext) Name() string { return g.name }

// NoteTouch queues f for the provider, never waiting.
func (g *guardContext) NoteTouch(_ context.Context, f ext.TouchedFile) { g.touches.push(f) }

// Context answers the provider's blocks for q, or none (the free default's)
// when it fails, is late or answers more than ext.MaxContextBlocks or a
// block ContextBlock.Valid refuses. It never answers an error.
func (g *guardContext) Context(ctx context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error) {
	return extCall(ctx, g.s, ext.KindContext, g.name, ext.ContextWait, nil, func(bs []ext.ContextBlock) error {
		if len(bs) > ext.MaxContextBlocks {
			return fmt.Errorf("context: %d blocks, past %d", len(bs), ext.MaxContextBlocks)
		}
		for _, b := range bs {
			if err := b.Valid(); err != nil {
				return err
			}
		}
		return nil
	}, func(ctx context.Context) ([]ext.ContextBlock, error) {
		return g.p.Context(ctx, q)
	}), nil
}

// guardSink is a notification sink behind the guard.
type guardSink struct {
	s     *Server
	name  string
	p     ext.NotificationSink
	queue *extQueue[ext.Notification]
}

func (g *guardSink) Name() string { return g.name }

// Events answers what the sink takes, as a copy, or nothing (an empty list,
// not nil, which would take everything) when the call fails or is late.
// It is read once, at start (C3).
func (g *guardSink) Events() []string {
	return extCall(context.Background(), g.s, ext.KindNotifications, g.name, ext.StartWait, []string{}, nil, func(context.Context) ([]string, error) {
		evs := g.p.Events()
		if evs == nil {
			return nil, nil
		}
		return append([]string{}, evs...), nil
	})
}

// Notify queues n for the sink, never waiting; it answers nil.
func (g *guardSink) Notify(_ context.Context, n ext.Notification) error {
	g.queue.push(n)
	return nil
}

// guardSync is a sync store behind the guard.
type guardSync struct {
	s    *Server
	name string
	p    ext.ContinuityStore
}

func (g guardSync) Name() string { return g.name }

// Saved delivers d on the calling goroutine (the sync adapter's pending
// set, which drops nothing, drains through it: C6), within
// ext.DeliveryWait, its panic recovered; an error has d delivered again.
func (g guardSync) Saved(ctx context.Context, d ext.Doc) error {
	return extDeliver(ctx, g.s, ext.KindSync, g.name, func(ctx context.Context) error { return g.p.Saved(ctx, d) })
}

// Deleted is Saved for a document the data directory no longer holds.
func (g guardSync) Deleted(ctx context.Context, d ext.Doc) error {
	return extDeliver(ctx, g.s, ext.KindSync, g.name, func(ctx context.Context) error { return g.p.Deleted(ctx, d) })
}

// Restore is the provider's, its panic recovered and no wait applied: only
// `conductor restore --provider` calls it, at a person's word, until it
// ends or the person stops it (C6).
func (g guardSync) Restore(ctx context.Context, r ext.RestoreRequest, w io.Writer) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return g.p.Restore(ctx, r, w)
}

// guardMedia is a media room provider behind the guard.
type guardMedia struct {
	s    *Server
	name string
	p    ext.MediaRoomProvider
}

func (g guardMedia) Name() string { return g.name }

// ICEServers answers the provider's servers for r, or none (the free
// default's) when it fails, is late or answers more than ext.MaxICEServers
// or a server ICEServer.Valid refuses. It never answers an error.
func (g guardMedia) ICEServers(ctx context.Context, r ext.ICERequest) ([]ext.ICEServer, error) {
	return extCall(ctx, g.s, ext.KindMedia, g.name, ext.ICEWait, nil, func(ss []ext.ICEServer) error {
		if len(ss) > ext.MaxICEServers {
			return fmt.Errorf("ice: %d servers, past %d", len(ss), ext.MaxICEServers)
		}
		for _, sv := range ss {
			if err := sv.Valid(); err != nil {
				return err
			}
		}
		return nil
	}, func(ctx context.Context) ([]ext.ICEServer, error) {
		return g.p.ICEServers(ctx, r)
	}), nil
}

// Room answers the provider's room for r, or ok false (the free default's)
// when it fails or is late. RoomJoin has no Valid yet: Room has no caller
// until Program item CL1, which adds its check.
func (g guardMedia) Room(ctx context.Context, r ext.RoomRequest) (ext.RoomJoin, bool, error) {
	type answer struct {
		j  ext.RoomJoin
		ok bool
	}
	a := extCall(ctx, g.s, ext.KindMedia, g.name, ext.ICEWait, answer{}, nil, func(ctx context.Context) (answer, error) {
		j, ok, err := g.p.Room(ctx, r)
		return answer{j, ok}, err
	})
	if !a.ok {
		return ext.RoomJoin{}, false, nil
	}
	return a.j, true, nil
}

// The guarded providers implement their interfaces, so a seam calls one as
// it would the provider.
var (
	_ ext.IdentityProvider  = guardIdentity{}
	_ ext.DecisionRouter    = guardRouter{}
	_ ext.ContextProvider   = (*guardContext)(nil)
	_ ext.NotificationSink  = (*guardSink)(nil)
	_ ext.ContinuityStore   = guardSync{}
	_ ext.MediaRoomProvider = guardMedia{}
)

// freeBuild is the providers of a server made without WithExtensions.
func freeBuild() ext.Providers { return local.Fill(ext.Providers{}) }
