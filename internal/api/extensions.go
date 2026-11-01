package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
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
	// the providers' Start (StartExtensions, through extStarters) and GET
	// /api/capabilities (C9) read. No call is made through it.
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
// defaults included, so no call site tells a default from a provider. It
// calls no provider: each name is the one the registry checked when the
// provider registered (extNames). No queue drains yet: a queue-fed
// provider's queues begin when New can no longer fail (beginFreeQueues)
// when it is no ext.Starter, and once its Start has succeeded
// (beginQueues) when it is one.
func (s *Server) installExtensions(p ext.Providers) {
	s.extCtx, s.extStop = context.WithCancel(context.Background())
	s.extQueues = map[string][]func(){}
	s.extStarts = extStarters(p)
	name := extNames(p)
	idName, routerName := name(ext.KindIdentity, 0, p.Identity), name(ext.KindDecisions, 0, p.Router)
	syncName, mediaName := name(ext.KindSync, 0, p.Continuity), name(ext.KindMedia, 0, p.Media)
	s.exts = extSlots{
		identity: guardIdentity{s: s, name: idName, p: p.Identity, ready: s.readyOf(idName, p.Identity)},
		router:   guardRouter{s: s, name: routerName, p: p.Router, ready: s.readyOf(routerName, p.Router)},
		sync:     guardSync{s: s, name: syncName, p: p.Continuity, ready: s.readyOf(syncName, p.Continuity)},
		media:    guardMedia{s: s, name: mediaName, p: p.Media, ready: s.readyOf(mediaName, p.Media)},
		all:      append([]ext.Entry(nil), p.All...),
	}
	for i, c := range p.Context {
		n := name(ext.KindContext, i, c)
		g := &guardContext{s: s, name: n, p: c, ready: s.readyOf(n, c)}
		g.touches = newExtQueue(s, ext.KindContext, g.name, ext.TouchQueue, func(ctx context.Context, f ext.TouchedFile) error {
			c.NoteTouch(ctx, f)
			return nil
		})
		s.holdQueue(c, g.name, g.touches.begin)
		s.exts.context = append(s.exts.context, g)
	}
	for i, n := range p.Notifications {
		g := &guardSink{s: s, name: name(ext.KindNotifications, i, n), p: n}
		g.queue = newExtQueue(s, ext.KindNotifications, g.name, ext.SinkQueue, g.deliver)
		g.queue.direct = true // deliver reads Events, then calls extDeliver itself
		s.holdQueue(n, g.name, g.queue.begin)
		s.exts.sinks = append(s.exts.sinks, g)
	}
}

// holdQueue keeps a queue of the provider v named name for
// beginFreeQueues when v is no ext.Starter, and for beginQueues otherwise.
func (s *Server) holdQueue(v any, name string, begin func()) {
	s.extMu.Lock()
	defer s.extMu.Unlock()
	if _, ok := v.(ext.Starter); !ok {
		s.extFree = append(s.extFree, begin)
		return
	}
	s.extQueues[name] = append(s.extQueues[name], begin)
}

// beginFreeQueues begins the drains of the queues of every provider that
// is no ext.Starter. New calls it last, once nothing can fail, so a New
// that fails leaves no drain running and nothing to stop.
func (s *Server) beginFreeQueues() {
	s.extMu.Lock()
	begins := s.extFree
	s.extFree = nil
	s.extMu.Unlock()
	for _, b := range begins {
		b()
	}
}

// beginQueues begins the drains of the queues the provider name holds,
// once its Start has succeeded (superviseStart). It is safe to call more
// than once.
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

// extNames answers the name of the i-th provider of kind in p, v: the one
// p.All gives it, which the registry checked when v registered, so no
// registered provider is asked again (a Name that blocks would hold New).
// A provider All does not name is a free default (local.Fill puts only
// those in a slot, and every list entry is registered), whose name is a
// constant, read through providerName.
func extNames(p ext.Providers) func(kind string, i int, v ext.Provider) string {
	byKind := map[string][]string{}
	for _, e := range p.All {
		byKind[e.Kind] = append(byKind[e.Kind], e.Name)
	}
	return func(kind string, i int, v ext.Provider) string {
		if names := byKind[kind]; i < len(names) {
			return names[i]
		}
		return providerName(v)
	}
}

// providerName is p's name, read without trusting p: a name that panics or
// that ext.ValidName refuses is "invalid". Only a slot All does not name
// is read this way (extNames).
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

// extText is v, a provider's error or panic value, as the guard's log
// quotes it, at most extErrText bytes. Its Error or String method is the
// provider's code, so it is read only inside the guard (on the goroutine
// of the call or delivery it came from), and a panic in it is recovered:
// the text then says only that.
func extText(v any) (text string) {
	defer func() {
		if recover() != nil {
			text = "(its text panicked)"
		}
	}()
	switch v := v.(type) {
	case string:
		text = v
	case error:
		text = v.Error()
	case fmt.Stringer:
		text = v.String()
	default:
		text = fmt.Sprintf("%v", v)
	}
	if len(text) > extErrText {
		text = text[:extErrText] + "…"
	}
	return text
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
// since, and detail (extText's, or the guard's own words; "" for none).
// It calls nothing of the provider's. The free default has been answered.
func (s *Server) extFailed(kind, name, why, detail string) {
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
	if detail != "" {
		attrs = append(attrs, "err", detail)
	}
	s.log.Warn("extension call failed: its free default answered", attrs...)
}

// extCall runs fn, a call of the provider name of kind, within wait and
// answers its result, or fallback when fn errs, panics, returns what valid
// refuses (a nil valid takes everything), or has not returned and been
// checked by then. The call's context is ctx's, ending after wait and once
// extCall returns, so a late fn's goroutine finishes on its own and its
// answer is not used. valid runs on that goroutine too, inside the wait
// and the slot, since what it reads is the provider's (a name of any
// length): a check still running when the wait ends is late, and holds its
// slot until it returns. valid must read only what fn answered and what
// the guard owns, never the caller's, which may change once extCall has
// returned. At most extInFlight calls of one provider and kind run at
// once: past it, fallback at once. A failure is logged at warn once a
// minute per provider and kind (extFailed); one that ends because ctx did,
// the caller gone, is not the provider's and is not logged.
//
// The plan's extCall takes no ctx; this one takes the caller's, so a
// request that ends ends its provider call too.
func extCall[T any](ctx context.Context, s *Server, kind, name string, wait time.Duration, fallback T, valid func(T) error, fn func(context.Context) (T, error)) T {
	g := s.guard(kind, name)
	select {
	case g.slots <- struct{}{}:
	default:
		s.extFailed(kind, name, "busy", fmt.Sprintf("%d calls in flight", extInFlight))
		return fallback
	}
	cctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	// failed is a failure's text, read on the call's goroutine (extText):
	// the caller never runs a method of the provider's. invalid is valid's
	// refusal, bounded the same way.
	type result struct {
		v       T
		failed  string
		invalid string
		err     bool
		late    bool
	}
	done := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			if p := recover(); p != nil {
				r = result{err: true, failed: "panic: " + extText(p)}
			}
			// The slot is free before the answer is sent: a caller that has
			// its answer may call again at once and find it.
			<-g.slots
			done <- r
		}()
		if f := extCalling.Load(); f != nil {
			(*f)(kind, name)
		}
		v, err := fn(cctx)
		r.v = v
		if err != nil {
			r.err, r.failed = true, extText(err)
		} else if valid != nil {
			// valid is the guard's own (ext's checks), over the guard's copy
			// of the answer (the guarded providers copy what they are
			// given), never the provider's; it runs here, bounded by the
			// wait and holding the slot, because its cost is the answer's.
			if verr := valid(v); verr != nil {
				r.invalid = extText(verr)
			}
		}
		// An answer given or checked once its context ended is late,
		// however soon after: the caller may already have the fallback.
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
			s.extFailed(kind, name, "late", fmt.Sprintf("no answer within %s", wait))
		}
		return fallback
	case r.err:
		s.extFailed(kind, name, "error", r.failed)
		return fallback
	case r.invalid != "":
		s.extFailed(kind, name, "invalid", r.invalid)
		return fallback
	}
	return r.v
}

// extCalling, when a test sets it, is told the kind and provider name of
// every direct call as its goroutine begins, before fn runs, and may hold
// it there; nil outside tests.
var extCalling atomic.Pointer[func(kind, name string)]

// extDeliver runs fn, a delivery to the provider name of kind, on the
// calling goroutine (a queue's drain, or a list that drops nothing) with a
// context that ends after ext.DeliveryWait or with ctx, and answers its
// error: fn's own as its text (extText, read here, a panic in it
// recovered), a panic recovered, or the context's end. A failure is
// logged as extCall logs one. A provider that ignores its context, or
// whose error's text blocks, holds only the goroutine that delivers to it.
func extDeliver(ctx context.Context, s *Server, kind, name string, fn func(context.Context) error) (err error) {
	cctx, cancel := context.WithTimeout(ctx, ext.DeliveryWait)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = errors.New("panic: " + extText(p))
		}
		if err == nil && cctx.Err() != nil {
			err = cctx.Err()
		}
		if err != nil && ctx.Err() == nil {
			s.extFailed(kind, name, "delivery", err.Error())
		}
	}()
	// An error once the context ended is the context's end (the deferred
	// check answers it), whatever the provider made of it.
	if err := fn(cctx); err != nil && cctx.Err() == nil {
		return errors.New(extText(err))
	}
	return nil
}

// extQueue holds what is to be delivered to one provider of one kind, in
// order: a ring filled without waiting, whose oldest entry is dropped when
// it is full (the first drop and every hundredth logged, as a webhook's
// queue does), drained by one goroutine of its own once begun, each
// delivery through extDeliver. A webhook's queue is one too (webhooks.go):
// direct, it delivers without extDeliver, a webhook bounding and logging
// its own deliveries, and onDrop logs its drops as a webhook's. A
// notification sink's is direct as well: its delivery reads the sink's
// Events first and only then calls extDeliver (guardSink.deliver).
type extQueue[T any] struct {
	s       *Server
	kind    string
	name    string
	deliver func(context.Context, T) error
	direct  bool                 // deliver is called as it is, with the drain's context
	onDrop  func(dropped uint64) // logs a drop; nil for the extension line

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
		if q.onDrop != nil {
			q.onDrop(dropped)
			return
		}
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
	if f := extBegan.Load(); f != nil {
		(*f)(q.name)
	}
	ctx := q.s.extCtx
	q.s.track(func() { q.run(ctx) })
}

// extBegan, when a test sets it, is told the provider name of every queue
// whose drain begins; nil outside tests.
var extBegan atomic.Pointer[func(name string)]

// extDelivering, when a test sets it, is told the drain's context and the
// kind and provider name of every queued delivery (a webhook's kind is
// "webhook", its name its host) as it begins, and may hold it there; nil
// outside tests.
var extDelivering atomic.Pointer[func(ctx context.Context, kind, name string)]

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
		if f := extDelivering.Load(); f != nil {
			(*f)(ctx, q.kind, q.name)
		}
		if q.direct {
			_ = q.deliver(ctx, v)
			continue
		}
		_ = extDeliver(ctx, q.s, q.kind, q.name, func(ctx context.Context) error { return q.deliver(ctx, v) })
	}
}

// errNoGrant is an identity provider's principal that may do nothing here.
var errNoGrant = errors.New("principal: no grant")

// errNotStarted is a delivery to a provider whose Start has not yet
// succeeded: not made, to be made again.
var errNotStarted = errors.New("extension: not started yet")

// guardIdentity is an identity provider behind the guard.
type guardIdentity struct {
	s     *Server
	name  string
	p     ext.IdentityProvider
	ready *atomic.Bool // nil, or the provider's Start (exthost.go)
}

func (g guardIdentity) Name() string { return g.name }

// Authenticate answers the provider's principal for token, or ok false (as
// the free default answers) when it fails, is late or answers a principal
// that is not valid or holds no grant, and, without asking it, until its
// Start has succeeded. It never answers an error.
func (g guardIdentity) Authenticate(ctx context.Context, token string) (ext.Principal, bool, error) {
	if !live(g.ready) {
		return ext.Principal{}, false, nil
	}
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
		return answer{copyPrincipal(p), ok}, err
	})
	if !a.ok {
		return ext.Principal{}, false, nil
	}
	return a.p, true, nil
}

// guardRouter is a decision router behind the guard.
type guardRouter struct {
	s     *Server
	name  string
	p     ext.DecisionRouter
	ready *atomic.Bool
}

func (g guardRouter) Name() string { return g.name }

// Route answers the provider's route for d, or the empty route (the free
// default's) when it fails, is late or answers a route Route.Valid refuses
// for d. It never answers an error. The provider is given a copy of d and
// the route is checked against another, the guard's own: a provider that
// changes the options it was given changes neither the caller's question
// nor what its answer is checked against.
func (g guardRouter) Route(ctx context.Context, d ext.Decision) (ext.Route, error) {
	if !live(g.ready) {
		return ext.Route{}, nil
	}
	own := copyDecision(d)
	return extCall(ctx, g.s, ext.KindDecisions, g.name, ext.RouteWait, ext.Route{}, func(r ext.Route) error {
		return r.Valid(own)
	}, func(ctx context.Context) (ext.Route, error) {
		r, err := g.p.Route(ctx, copyDecision(own))
		return copyRoute(r), err
	}), nil
}

// Resolved tells the provider of r on the calling goroutine (the router's
// resolutions, which drop nothing, drain through it in order: C5), within
// ext.DeliveryWait, its panic recovered. A router not yet started routed
// nothing, so it is told nothing.
func (g guardRouter) Resolved(ctx context.Context, r ext.Resolution) {
	if !live(g.ready) {
		return
	}
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
	ready   *atomic.Bool
	touches *extQueue[ext.TouchedFile]
}

func (g *guardContext) Name() string { return g.name }

// NoteTouch queues f for the provider, never waiting.
func (g *guardContext) NoteTouch(_ context.Context, f ext.TouchedFile) { g.touches.push(f) }

// Context answers the provider's blocks for q, or none (the free default's)
// when it fails, is late or answers more than ext.MaxContextBlocks or a
// block ContextBlock.Valid refuses. It never answers an error.
func (g *guardContext) Context(ctx context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error) {
	if !live(g.ready) {
		return nil, nil
	}
	// Copied here, before the call's goroutine begins: a caller answered
	// the default (its context ended) may reuse q's inputs at once.
	own := copyQuery(q)
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
		bs, err := g.p.Context(ctx, own)
		return copyOrNil(bs), err
	}), nil
}

// guardSink is a notification sink behind the guard.
type guardSink struct {
	s     *Server
	name  string
	p     ext.NotificationSink
	queue *extQueue[ext.Notification]

	eventsOnce sync.Once
	events     []string
	// known is what events takes, once read (sinkFilter, notify.go): nil
	// until then.
	known atomic.Pointer[sinkFilter]
}

func (g *guardSink) Name() string { return g.name }

// Events answers what the sink takes, as a copy, or nothing (an empty list,
// not nil, which would take everything) when the call fails or is late.
// The provider is asked once, at its first call (its Start's supervisor,
// which decides from it whether the provider may send notices, or its
// queue's first delivery, notify.go), and every call after answers that
// same list, so what a sink takes and whether its provider may send
// notices never disagree.
func (g *guardSink) Events() []string { return g.eventsWithin(context.Background()) }

// eventsWithin is Events, its one ask of the provider ending with ctx too.
func (g *guardSink) eventsWithin(ctx context.Context) []string {
	g.eventsOnce.Do(func() {
		g.events = extCall(ctx, g.s, ext.KindNotifications, g.name, ext.StartWait, []string{}, nil, func(context.Context) ([]string, error) {
			evs := g.p.Events()
			if evs == nil {
				return nil, nil
			}
			return append([]string{}, evs...), nil
		})
		g.known.Store(newSinkFilter(g.events))
	})
	return copyOrNil(g.events)
}

// Notify queues n for the sink, never waiting; it answers nil.
func (g *guardSink) Notify(_ context.Context, n ext.Notification) error {
	g.queue.push(n)
	return nil
}

// guardSync is a sync store behind the guard.
type guardSync struct {
	s     *Server
	name  string
	p     ext.ContinuityStore
	ready *atomic.Bool
}

func (g guardSync) Name() string { return g.name }

// Saved delivers d on the calling goroutine (the sync adapter's pending
// set, which drops nothing, drains through it: C6), within
// ext.DeliveryWait, its panic recovered; an error has d delivered again.
// Until the store's Start has succeeded it is not asked: errNotStarted,
// so the pending set keeps d for a later delivery.
func (g guardSync) Saved(ctx context.Context, d ext.Doc) error {
	if !live(g.ready) {
		return errNotStarted
	}
	return extDeliver(ctx, g.s, ext.KindSync, g.name, func(ctx context.Context) error { return g.p.Saved(ctx, d) })
}

// Deleted is Saved for a document the data directory no longer holds.
func (g guardSync) Deleted(ctx context.Context, d ext.Doc) error {
	if !live(g.ready) {
		return errNotStarted
	}
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
	s     *Server
	name  string
	p     ext.MediaRoomProvider
	ready *atomic.Bool
}

func (g guardMedia) Name() string { return g.name }

// ICEServers answers the provider's servers for r, or none (the free
// default's) when it fails, is late or answers more than ext.MaxICEServers
// or a server ICEServer.Valid refuses. It never answers an error.
func (g guardMedia) ICEServers(ctx context.Context, r ext.ICERequest) ([]ext.ICEServer, error) {
	if !live(g.ready) {
		return nil, nil
	}
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
		ss, err := g.p.ICEServers(ctx, r)
		ss = copyOrNil(ss)
		for i := range ss {
			ss[i].URLs = copyOrNil(ss[i].URLs)
		}
		return ss, err
	}), nil
}

// Room answers the provider's room for r, or ok false (the free default's)
// when it fails or is late. RoomJoin has no Valid yet: Room has no caller
// until Program item CL1, which adds its check. The provider is given a
// copy of r's grants, made before the call's goroutine begins.
func (g guardMedia) Room(ctx context.Context, r ext.RoomRequest) (ext.RoomJoin, bool, error) {
	if !live(g.ready) {
		return ext.RoomJoin{}, false, nil
	}
	own := r
	own.By = copyPrincipal(r.By)
	type answer struct {
		j  ext.RoomJoin
		ok bool
	}
	a := extCall(ctx, g.s, ext.KindMedia, g.name, ext.ICEWait, answer{}, nil, func(ctx context.Context) (answer, error) {
		j, ok, err := g.p.Room(ctx, own)
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

// The guarded providers answer copies, made on the call's goroutine as the
// provider returns, so what the guard checks (valid) and answers is the
// guard's own: a provider that changes the slices it answered afterwards
// changes nothing the seam holds. A direct call's provider is given a copy
// of what it is asked about too, made on the caller's goroutine before
// extCall begins the call's (the caller may reuse its slices as soon as
// extCall returns, which may be before that goroutine runs), so a provider
// that changes the slices it was given changes nothing of the caller's,
// nor what the guard checks.

// copyOrNil is a copy of s, nil for nil.
func copyOrNil[S ~[]E, E any](s S) S {
	if s == nil {
		return nil
	}
	return append(S{}, s...)
}

// copyPrincipal is p with its grants copied.
func copyPrincipal(p ext.Principal) ext.Principal {
	p.Grants = copyOrNil(p.Grants)
	return p
}

// copyDecision is d with its options copied.
func copyDecision(d ext.Decision) ext.Decision {
	d.Options = copyOrNil(d.Options)
	return d
}

// copyQuery is q with its inputs copied.
func copyQuery(q ext.ContextQuery) ext.ContextQuery {
	q.Inputs = copyOrNil(q.Inputs)
	return q
}

// copyRoute is r with its destinations and its answer copied.
func copyRoute(r ext.Route) ext.Route {
	r.To = copyOrNil(r.To)
	if r.Answer != nil {
		a := *r.Answer
		a.By = copyPrincipal(a.By)
		r.Answer = &a
	}
	return r
}

// Registered is every provider the composition root gave the server
// (WithExtensions), its kind and name, in the registry's order: none for
// the free build. It is what GET /api/capabilities (C9) will list.
func (s *Server) Registered() []ext.Registration {
	out := make([]ext.Registration, 0, len(s.exts.all))
	for _, e := range s.exts.all {
		out = append(out, ext.Registration{Kind: e.Kind, Name: e.Name})
	}
	return out
}

// freeBuild is the providers of a server made without WithExtensions.
func freeBuild() ext.Providers { return local.Fill(ext.Providers{}) }
