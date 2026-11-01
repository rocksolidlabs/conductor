package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// newExtServer is a server with no data directory made with opts, ended
// when the test is (its queues' goroutines waited for), logging to log.
func newExtServer(t *testing.T, log *slog.Logger, opts ...Option) *Server {
	t.Helper()
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.PublicURL = "http://example.test"
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}}}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, cat, log, nil, nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.WaitBackground(ctx)
	})
	return srv
}

// fakeSink is a notification sink that hands every notice it is given to
// got, after gate (when set) lets it.
type fakeSink struct {
	name string
	gate chan struct{}
	got  chan ext.Notification
}

func (f *fakeSink) Name() string     { return f.name }
func (f *fakeSink) Events() []string { return nil }
func (f *fakeSink) Notify(ctx context.Context, n ext.Notification) error {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if n.Message == "panic" {
		panic("the sink broke")
	}
	f.got <- n
	return nil
}

// startingSink is a fakeSink that is an ext.Starter: its queue is held
// until its Start has succeeded.
type startingSink struct{ fakeSink }

func (*startingSink) Start(context.Context, ext.Host) error { return nil }

// fakeMedia answers servers, or what answer does.
type fakeMedia struct {
	answer func(context.Context) ([]ext.ICEServer, error)
}

func (fakeMedia) Name() string { return "turn" }
func (f fakeMedia) ICEServers(ctx context.Context, _ ext.ICERequest) ([]ext.ICEServer, error) {
	return f.answer(ctx)
}
func (fakeMedia) Room(context.Context, ext.RoomRequest) (ext.RoomJoin, bool, error) {
	return ext.RoomJoin{}, false, nil
}

// fakeIdentity answers what answer does.
type fakeIdentity struct {
	answer func(context.Context, string) (ext.Principal, bool, error)
}

func (fakeIdentity) Name() string { return "cloud" }
func (f fakeIdentity) Authenticate(ctx context.Context, token string) (ext.Principal, bool, error) {
	return f.answer(ctx, token)
}

// A server made without WithExtensions is the free build: the default in
// every slot, behind the guard, no list, nothing registered, no queue.
func TestNewWithoutExtensionsIsTheFreeBuild(t *testing.T) {
	srv := newExtServer(t, nil)
	x := srv.exts
	if g, ok := x.identity.(guardIdentity); !ok || g.p != (local.Identity{}) || g.Name() != local.Name {
		t.Fatalf("identity: %#v", x.identity)
	}
	if g, ok := x.router.(guardRouter); !ok || g.p != (local.Router{}) || g.Name() != local.Name {
		t.Fatalf("router: %#v", x.router)
	}
	if g, ok := x.sync.(guardSync); !ok || g.p != (local.Continuity{}) || g.Name() != local.Name {
		t.Fatalf("sync: %#v", x.sync)
	}
	if g, ok := x.media.(guardMedia); !ok || g.p != (local.Media{}) || g.Name() != local.Name {
		t.Fatalf("media: %#v", x.media)
	}
	if len(x.context) != 0 || len(x.sinks) != 0 || len(x.all) != 0 || len(srv.extQueues) != 0 {
		t.Fatalf("lists %d %d, registered %d, held queues %d: want none", len(x.context), len(x.sinks), len(x.all), len(srv.extQueues))
	}
	// Through the guard, each answers as its default does.
	ctx := t.Context()
	if p, ok, err := x.identity.Authenticate(ctx, "any"); ok || err != nil || p.Subject != "" {
		t.Fatalf("identity answered %+v %v %v", p, ok, err)
	}
	if r, err := x.router.Route(ctx, ext.Decision{}); err != nil || len(r.To) != 0 || r.Answer != nil {
		t.Fatalf("router answered %+v %v", r, err)
	}
	if ss, err := x.media.ICEServers(ctx, ext.ICERequest{Purpose: "viewer"}); err != nil || ss != nil {
		t.Fatalf("media answered %v %v", ss, err)
	}
	if _, ok, err := x.media.Room(ctx, ext.RoomRequest{}); ok || err != nil {
		t.Fatalf("room answered %v %v", ok, err)
	}
	if err := x.sync.Saved(ctx, ext.Doc{}); err != nil {
		t.Fatalf("sync answered %v", err)
	}
	if err := x.sync.Restore(ctx, ext.RestoreRequest{}, io.Discard); !errors.Is(err, ext.ErrNothingToRestore) {
		t.Fatalf("restore answered %v", err)
	}
}

// WithExtensions keeps the filled set as it is: the defaults where nothing
// is registered, the providers where something is, each behind the guard,
// and a sink that is no Starter drained at once.
func TestWithExtensionsKeepsDefaults(t *testing.T) {
	sink := &fakeSink{name: "push", got: make(chan ext.Notification, 1)}
	media := fakeMedia{answer: func(context.Context) ([]ext.ICEServer, error) {
		return []ext.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}, nil
	}}
	held := &startingSink{fakeSink{name: "later", got: make(chan ext.Notification, 1)}}
	p := local.Fill(ext.Providers{
		Media:         media,
		Notifications: []ext.NotificationSink{sink, held},
		All: []ext.Entry{
			{Kind: ext.KindNotifications, Name: "push", Value: sink},
			{Kind: ext.KindNotifications, Name: "later", Value: held},
			{Kind: ext.KindMedia, Name: "turn", Value: media},
		},
	})
	srv := newExtServer(t, nil, WithExtensions(p))
	x := srv.exts
	if g, ok := x.identity.(guardIdentity); !ok || g.p != (local.Identity{}) {
		t.Fatalf("identity: %#v", x.identity)
	}
	if g, ok := x.router.(guardRouter); !ok || g.p != (local.Router{}) {
		t.Fatalf("router: %#v", x.router)
	}
	if g, ok := x.sync.(guardSync); !ok || g.p != (local.Continuity{}) {
		t.Fatalf("sync: %#v", x.sync)
	}
	if g, ok := x.media.(guardMedia); !ok || g.Name() != "turn" {
		t.Fatalf("media: %#v", x.media)
	}
	if len(x.all) != 3 || x.all[0].Name != "push" || x.all[2].Kind != ext.KindMedia {
		t.Fatalf("registered: %+v", x.all)
	}
	ss, _ := x.media.ICEServers(t.Context(), ext.ICERequest{Purpose: "viewer"})
	if len(ss) != 1 || ss[0].URLs[0] != "stun:stun.example.test:3478" {
		t.Fatalf("the provider's servers: %+v", ss)
	}
	if len(x.sinks) != 2 || x.sinks[0].Name() != "push" || x.sinks[1].Name() != "later" {
		t.Fatalf("sinks: %+v", x.sinks)
	}
	_ = x.sinks[0].Notify(t.Context(), ext.Notification{Message: "hello"})
	if n := <-sink.got; n.Message != "hello" {
		t.Fatalf("the sink got %+v", n)
	}
	// The Starter's queue waits for its Start (PR-4): it fills and is not
	// delivered until its drain begins.
	_ = x.sinks[1].Notify(t.Context(), ext.Notification{Message: "held"})
	q := x.sinks[1].(*guardSink).queue
	q.mu.Lock()
	begun := q.begun
	q.mu.Unlock()
	if n, _ := q.stats(); begun || n != 1 {
		t.Fatalf("the starter's queue is begun %v and holds %d: want held, with 1", begun, n)
	}
	srv.beginQueues("later")
	if n := <-held.got; n.Message != "held" {
		t.Fatalf("the starter's sink got %+v", n)
	}
}

// A call that has not answered by its wait is answered with the fallback,
// its context ended, without waiting for its goroutine, which finishes on
// its own; its late answer is not used.
func TestExtCallWait(t *testing.T) {
	srv := newExtServer(t, nil)
	release := make(chan struct{})
	finished := make(chan struct{})
	got := extCall(t.Context(), srv, ext.KindMedia, "slow", time.Millisecond, "fallback", nil, func(ctx context.Context) (string, error) {
		defer close(finished)
		<-ctx.Done()
		<-release // the goroutine is still here when extCall returns
		return "late", nil
	})
	if got != "fallback" {
		t.Fatalf("a late call answered %q", got)
	}
	close(release)
	<-finished
	// An answer given only once the context ended is late too, however
	// soon after.
	got = extCall(t.Context(), srv, ext.KindMedia, "slow", time.Millisecond, "fallback", nil, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "late", nil
	})
	if got != "fallback" {
		t.Fatalf("an answer after the wait was used: %q", got)
	}
	// The caller gone ends the call too, and is not the provider's failure.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := extCall(ctx, srv, ext.KindMedia, "slow", time.Hour, "fallback", nil, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "late", nil
	}); got != "fallback" {
		t.Fatalf("a call whose caller left answered %q", got)
	}
	// Through a guarded provider: an identity provider that hangs is no
	// principal.
	g := guardIdentity{s: srv, name: "cloud", p: fakeIdentity{answer: func(ctx context.Context, _ string) (ext.Principal, bool, error) {
		<-ctx.Done()
		return ext.Principal{Subject: "u1", Name: "U", Grants: []ext.Grant{ext.GrantAdmin}}, true, nil
	}}}
	ctx, cancel = context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if p, ok, err := g.Authenticate(ctx, "tok"); ok || err != nil || p.Subject != "" {
		t.Fatalf("a hung identity provider answered %+v %v %v", p, ok, err)
	}
}

// A call that panics is answered with the fallback; the panic never
// escapes; the failure is logged at warn once a minute per provider and
// kind, with how many since; and nothing else is remembered, so the next
// good call is answered as if none had failed.
func TestExtCallPanic(t *testing.T) {
	var logs syncLog
	srv := newExtServer(t, slog.New(slog.NewTextHandler(&logs, nil)))
	var now atomic.Pointer[time.Time]
	start := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	now.Store(&start)
	srv.extClock = func() time.Time { return *now.Load() }
	boom := func(context.Context) (int, error) { panic("the provider broke") }
	for range 3 {
		if got := extCall(t.Context(), srv, ext.KindContext, "graph", time.Hour, -1, nil, boom); got != -1 {
			t.Fatalf("a panic answered %d", got)
		}
	}
	if n := strings.Count(logs.String(), "extension call failed"); n != 1 {
		t.Fatalf("three panics in a minute logged %d times, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "provider=graph") || !strings.Contains(logs.String(), "failures=1") || !strings.Contains(logs.String(), "the provider broke") {
		t.Fatalf("the log does not name the provider and the panic:\n%s", logs.String())
	}
	// Another kind of the same provider is logged on its own.
	_ = extCall(t.Context(), srv, ext.KindNotifications, "graph", time.Hour, -1, nil, boom)
	if n := strings.Count(logs.String(), "extension call failed"); n != 2 {
		t.Fatalf("another kind's panic logged %d lines in all, want 2", n)
	}
	// A minute on, the next failure is logged with the two not logged.
	later := start.Add(extLogEvery)
	now.Store(&later)
	_ = extCall(t.Context(), srv, ext.KindContext, "graph", time.Hour, -1, nil, boom)
	if !strings.Contains(logs.String(), "failures=3") {
		t.Fatalf("the minute's log does not count the failures since:\n%s", logs.String())
	}
	if got := extCall(t.Context(), srv, ext.KindContext, "graph", time.Hour, -1, nil, func(context.Context) (int, error) { return 7, nil }); got != 7 {
		t.Fatalf("a good call after the panics answered %d", got)
	}
	// An error is a failure too.
	if got := extCall(t.Context(), srv, ext.KindContext, "graph", time.Hour, -1, nil, func(context.Context) (int, error) { return 7, errors.New("down") }); got != -1 {
		t.Fatalf("an error answered %d", got)
	}
}

// An answer valid refuses is never used: the guarded providers check their
// records against ext's bounds.
func TestExtCallInvalid(t *testing.T) {
	srv := newExtServer(t, nil)
	refuse := func(v int) error {
		if v > 10 {
			return errors.New("past 10")
		}
		return nil
	}
	if got := extCall(t.Context(), srv, ext.KindMedia, "turn", time.Hour, 0, refuse, func(context.Context) (int, error) { return 11, nil }); got != 0 {
		t.Fatalf("a refused answer was used: %d", got)
	}
	if got := extCall(t.Context(), srv, ext.KindMedia, "turn", time.Hour, 0, refuse, func(context.Context) (int, error) { return 9, nil }); got != 9 {
		t.Fatalf("a valid answer was not used: %d", got)
	}
	// A principal with no grant, or one past its bounds, is no principal.
	for _, p := range []ext.Principal{
		{Subject: "u1", Name: "U"},
		{Subject: "has space", Name: "U", Grants: []ext.Grant{ext.GrantAdmin}},
		{Subject: "u1", Name: "U", Grants: []ext.Grant{"root"}},
	} {
		g := guardIdentity{s: srv, name: "cloud", p: fakeIdentity{answer: func(context.Context, string) (ext.Principal, bool, error) { return p, true, nil }}}
		if got, ok, _ := g.Authenticate(t.Context(), "tok"); ok {
			t.Fatalf("an invalid principal %+v was used: %+v", p, got)
		}
	}
	good := ext.Principal{Subject: "u1", Name: "U", Grants: []ext.Grant{ext.GrantAdmin}}
	g := guardIdentity{s: srv, name: "cloud", p: fakeIdentity{answer: func(context.Context, string) (ext.Principal, bool, error) { return good, true, nil }}}
	if got, ok, _ := g.Authenticate(t.Context(), "tok"); !ok || got.Subject != "u1" {
		t.Fatalf("a valid principal was refused: %+v %v", got, ok)
	}
	// ICE servers among which one a browser refuses are none; past the
	// bound they are cut to it (TestMediaBounds).
	stun := ext.ICEServer{URLs: []string{"stun:stun.example.test:3478"}}
	ss := []ext.ICEServer{stun, {URLs: []string{"http://example.test"}}}
	m := guardMedia{s: srv, name: "turn", p: fakeMedia{answer: func(context.Context) ([]ext.ICEServer, error) { return ss, nil }}}
	if got, _ := m.ICEServers(t.Context(), ext.ICERequest{Purpose: "viewer"}); got != nil {
		t.Fatalf("invalid servers were used: %+v", got)
	}
	// A route whose answer is no choice the question offers is the empty
	// route.
	r := guardRouter{s: srv, name: "switchman", p: routeAnswer{ext.Route{Answer: &ext.Answer{Input: "9", By: good}}}}
	if got, _ := r.Route(t.Context(), ext.Decision{ID: "q1", Options: []ext.DecisionOption{{Input: "1"}}}); got.Answer != nil {
		t.Fatalf("a route typing a key the question does not offer was used: %+v", got)
	}
	// Context past its bounds is none.
	c := &guardContext{s: srv, name: "graph", p: contextAnswer(make([]ext.ContextBlock, ext.MaxContextBlocks+1))}
	if got, _ := c.Context(t.Context(), ext.ContextQuery{}); got != nil {
		t.Fatalf("too many blocks were used: %d", len(got))
	}
}

// routeAnswer is a router that answers its route.
type routeAnswer struct{ r ext.Route }

func (routeAnswer) Name() string                                             { return "switchman" }
func (a routeAnswer) Route(context.Context, ext.Decision) (ext.Route, error) { return a.r, nil }
func (routeAnswer) Resolved(context.Context, ext.Resolution)                 {}

// contextAnswer is a context provider that answers its blocks.
type contextAnswer []ext.ContextBlock

func (contextAnswer) Name() string                               { return "graph" }
func (contextAnswer) NoteTouch(context.Context, ext.TouchedFile) {}
func (a contextAnswer) Context(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) {
	return a, nil
}

// At most extInFlight calls of one provider and kind run at once: past it,
// the fallback at once, without calling the provider; another kind and
// another provider are not held; once the calls return, calls go through
// again.
func TestExtCallInFlight(t *testing.T) {
	srv := newExtServer(t, nil)
	gate := make(chan struct{})
	in := make(chan struct{}, extInFlight)
	var wg sync.WaitGroup
	answers := make(chan int, extInFlight)
	for range extInFlight {
		wg.Go(func() {
			answers <- extCall(t.Context(), srv, ext.KindIdentity, "cloud", time.Hour, -1, nil, func(context.Context) (int, error) {
				in <- struct{}{}
				<-gate
				return 1, nil
			})
		})
	}
	for range extInFlight {
		<-in
	}
	called := false
	if got := extCall(t.Context(), srv, ext.KindIdentity, "cloud", time.Hour, -1, nil, func(context.Context) (int, error) {
		called = true
		return 1, nil
	}); got != -1 || called {
		t.Fatalf("a call past %d in flight answered %d, called %v", extInFlight, got, called)
	}
	if got := extCall(t.Context(), srv, ext.KindMedia, "cloud", time.Hour, -1, nil, func(context.Context) (int, error) { return 2, nil }); got != 2 {
		t.Fatalf("another kind of the provider was held: %d", got)
	}
	if got := extCall(t.Context(), srv, ext.KindIdentity, "other", time.Hour, -1, nil, func(context.Context) (int, error) { return 3, nil }); got != 3 {
		t.Fatalf("another provider was held: %d", got)
	}
	close(gate)
	wg.Wait()
	close(answers)
	for a := range answers {
		if a != 1 {
			t.Fatalf("a call in flight answered %d", a)
		}
	}
	if got := extCall(t.Context(), srv, ext.KindIdentity, "cloud", time.Hour, -1, nil, func(context.Context) (int, error) { return 4, nil }); got != 4 {
		t.Fatalf("a call once the others returned answered %d", got)
	}
}

// A full queue drops its oldest, never making its producer wait; the first
// drop and every hundredth are logged.
func TestExtQueueDropsOldest(t *testing.T) {
	var logs syncLog
	srv := newExtServer(t, slog.New(slog.NewTextHandler(&logs, nil)))
	got := make(chan int, 8)
	q := newExtQueue(srv, ext.KindNotifications, "push", 4, func(_ context.Context, v int) error {
		got <- v
		return nil
	})
	// Not begun: nothing drains, and pushing past the ring never waits.
	for i := 1; i <= 6; i++ {
		q.push(i)
	}
	if n, dropped := q.stats(); n != 4 || dropped != 2 {
		t.Fatalf("queued %d, dropped %d: want 4 and 2", n, dropped)
	}
	if n := strings.Count(logs.String(), "dropped the oldest entry"); n != 1 || !strings.Contains(logs.String(), "dropped=1") {
		t.Fatalf("two drops logged %d times, want the first only:\n%s", n, logs.String())
	}
	for i := 7; i <= 104; i++ {
		q.push(i)
	}
	if n := strings.Count(logs.String(), "dropped the oldest entry"); n != 2 || !strings.Contains(logs.String(), "dropped=100") {
		t.Fatalf("a hundred drops logged %d times, want the first and the hundredth:\n%s", n, logs.String())
	}
	q.begin()
	for want := 101; want <= 104; want++ {
		if v := <-got; v != want {
			t.Fatalf("delivered %d, want %d: the newest four, in order", v, want)
		}
	}
}

// A queue delivers in order, from one goroutine; a delivery that panics or
// errs costs that delivery alone.
func TestExtQueueOrder(t *testing.T) {
	srv := newExtServer(t, nil)
	var inFlight, most atomic.Int32
	got := make(chan int, 100)
	q := newExtQueue(srv, ext.KindContext, "graph", ext.TouchQueue, func(_ context.Context, v int) error {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		if n > most.Load() {
			most.Store(n)
		}
		switch v {
		case 30:
			panic("the provider broke")
		case 60:
			return errors.New("down")
		}
		got <- v
		return nil
	})
	q.begin()
	for i := range 100 {
		q.push(i)
	}
	for want := range 100 {
		if want == 30 || want == 60 {
			continue
		}
		if v := <-got; v != want {
			t.Fatalf("delivered %d, want %d", v, want)
		}
	}
	if m := most.Load(); m != 1 {
		t.Fatalf("%d deliveries ran at once, want one at a time", m)
	}
	// Through a guarded sink, as a seam queues: the notices in order.
	sink := &fakeSink{name: "push", got: make(chan ext.Notification, 10)}
	srv2 := newExtServer(t, nil, WithExtensions(local.Fill(ext.Providers{Notifications: []ext.NotificationSink{sink}})))
	for _, m := range []string{"a", "b", "c"} {
		if err := srv2.exts.sinks[0].Notify(t.Context(), ext.Notification{Message: m}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		if n := <-sink.got; n.Message != want {
			t.Fatalf("the sink got %q, want %q", n.Message, want)
		}
	}
}

// Shutdown ends the queues' goroutines and waits for them: a delivery in
// flight has its context ended, what is still queued is not delivered, and
// a push after it never waits.
func TestExtQueueShutdown(t *testing.T) {
	srv := newExtServer(t, nil)
	gate := make(chan struct{})
	sink := &fakeSink{name: "push", gate: gate, got: make(chan ext.Notification, 4)}
	ended := make(chan error, 1)
	started := make(chan struct{})
	q := newExtQueue(srv, ext.KindNotifications, "push", 4, func(ctx context.Context, n ext.Notification) error {
		if n.Message == "first" {
			close(started)
		}
		err := sink.Notify(ctx, n)
		if n.Message == "first" {
			ended <- err
		}
		return err
	})
	q.push(ext.Notification{Message: "first"})
	q.push(ext.Notification{Message: "second"})
	q.begin()
	<-started // the first is in flight, held by the gate
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	if ctx.Err() != nil {
		t.Fatal("Shutdown waited out its context for the queue's goroutine")
	}
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Fatalf("the delivery in flight ended with %v, want its context cancelled", err)
	}
	close(gate)
	q.push(ext.Notification{Message: "after"})
	if n, _ := q.stats(); n != 2 {
		t.Fatalf("after shutdown %d wait, want the second and the one pushed after", n)
	}
	select {
	case n := <-sink.got:
		t.Fatalf("delivered %+v after shutdown", n)
	default:
	}
}

// A New that fails after the providers are installed (here the records'
// directory is a file) leaves no queue's drain running, since none begins
// before New can no longer fail; a New that succeeds begins the drain of a
// sink that is no Starter, once.
func TestNewFailingBeginsNoDrain(t *testing.T) {
	var began []string
	var mu sync.Mutex
	hook := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		began = append(began, name)
	}
	extBegan.Store(&hook)
	t.Cleanup(func() { extBegan.Store(nil) })
	beganNow := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), began...)
	}
	sink := &fakeSink{name: "failing-new", got: make(chan ext.Notification, 1)}
	p := local.Fill(ext.Providers{
		Notifications: []ext.NotificationSink{sink},
		All:           []ext.Entry{{Kind: ext.KindNotifications, Name: "failing-new", Value: sink}},
	})
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.PublicURL = "http://example.test"
	cfg.DataDir = t.TempDir()
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}}}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "runs"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if srv, err := New(cfg, cat, nil, nil, st, WithExtensions(p)); err == nil {
		srv.WaitBackground(t.Context())
		t.Fatal("New succeeded with the records' directory a file")
	}
	if b := beganNow(); len(b) != 0 {
		t.Fatalf("a failed New began the drains of %q", b)
	}
	// The same providers, on a New that succeeds: the sink drains.
	srv := newExtServer(t, nil, WithExtensions(p))
	if b := beganNow(); !slices.Equal(b, []string{"failing-new"}) {
		t.Fatalf("a New that succeeded began %q, want the sink's drain once", b)
	}
	_ = srv.exts.sinks[0].Notify(t.Context(), ext.Notification{Message: "after"})
	if n := <-sink.got; n.Message != "after" {
		t.Fatalf("the sink got %+v", n)
	}
}

// New asks no registered provider its name again: a provider whose Name
// blocks once it has registered holds nothing, and the guard names it as
// the registry did (Providers.All).
func TestNewAsksNoNameAgain(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	id := blockingName{fakeIdentity{answer: func(context.Context, string) (ext.Principal, bool, error) {
		return ext.Principal{}, false, nil
	}}, block}
	sink := &blockingSink{fakeSink{name: "push", got: make(chan ext.Notification, 1)}, block}
	p := local.Fill(ext.Providers{
		Identity:      id,
		Notifications: []ext.NotificationSink{sink},
		All: []ext.Entry{
			{Kind: ext.KindIdentity, Name: "cloud", Value: id},
			{Kind: ext.KindNotifications, Name: "push", Value: sink},
		},
	})
	srv := newExtServer(t, nil, WithExtensions(p)) // a Name call would hold New here for good
	if n := srv.exts.identity.Name(); n != "cloud" {
		t.Fatalf("the identity provider is named %q, want the registry's cloud", n)
	}
	if n := srv.exts.sinks[0].Name(); n != "push" {
		t.Fatalf("the sink is named %q, want the registry's push", n)
	}
	if n := srv.exts.router.Name(); n != local.Name {
		t.Fatalf("the default router is named %q", n)
	}
}

// blockingName is an identity provider whose Name blocks until block
// closes, as if it had answered once, at its registration.
type blockingName struct {
	fakeIdentity
	block chan struct{}
}

func (b blockingName) Name() string { <-b.block; return "cloud" }

// blockingSink is a fakeSink whose Name blocks until block closes.
type blockingSink struct {
	fakeSink
	block chan struct{}
}

func (b *blockingSink) Name() string { <-b.block; return "push" }

// badError is an error whose Error method is the provider's code at its
// worst: it panics, or blocks until block closes.
type badError struct {
	panics bool
	block  chan struct{}
}

func (e badError) Error() string {
	if e.panics {
		panic(e) // a panic whose value is itself an error that panics
	}
	<-e.block
	return "late text"
}

// A provider's error is read only inside the guard: an Error that panics
// never escapes, from a call or from a delivery (a queue's drain among
// them), and one that blocks holds only its own goroutine, the caller
// answered the fallback within its wait.
func TestExtErrorTextGuarded(t *testing.T) {
	var logs syncLog
	srv := newExtServer(t, slog.New(slog.NewTextHandler(&logs, nil)))
	panics := badError{panics: true}
	if got := extCall(t.Context(), srv, ext.KindMedia, "turn", time.Hour, -1, nil, func(context.Context) (int, error) { return 1, panics }); got != -1 {
		t.Fatalf("an error whose text panics answered %d", got)
	}
	if got := extCall(t.Context(), srv, ext.KindContext, "graph", time.Hour, -1, nil, func(context.Context) (int, error) { panic(panics) }); got != -1 {
		t.Fatalf("a panic whose text panics answered %d", got)
	}
	if !strings.Contains(logs.String(), "its text panicked") {
		t.Fatalf("the log does not say the text panicked:\n%s", logs.String())
	}
	block := make(chan struct{})
	defer close(block)
	blocks := badError{block: block}
	if got := extCall(t.Context(), srv, ext.KindIdentity, "cloud", time.Millisecond, -1, nil, func(context.Context) (int, error) { return 1, blocks }); got != -1 {
		t.Fatalf("an error whose text blocks answered %d", got)
	}
	// A delivery: the error answered is the guard's text, never the
	// provider's value.
	err := extDeliver(t.Context(), srv, ext.KindSync, "mirror", func(context.Context) error { return panics })
	if err == nil || err.Error() != "(its text panicked)" {
		t.Fatalf("a delivery whose error's text panics answered %v", err)
	}
	if _, ok := err.(badError); ok {
		t.Fatal("a delivery answered the provider's own error")
	}
	if err := extDeliver(t.Context(), srv, ext.KindSync, "mirror", func(context.Context) error { panic(panics) }); err == nil || !strings.Contains(err.Error(), "its text panicked") {
		t.Fatalf("a delivery whose panic's text panics answered %v", err)
	}
	// Through a queue's drain, which a panic escaping would end with the
	// process: the next entry is delivered.
	got := make(chan int, 1)
	q := newExtQueue(srv, ext.KindNotifications, "push", 4, func(_ context.Context, v int) error {
		if v == 1 {
			return panics
		}
		got <- v
		return nil
	})
	q.begin()
	q.push(1)
	q.push(2)
	if v := <-got; v != 2 {
		t.Fatalf("delivered %d after the error, want 2", v)
	}
}

// The guarded providers answer copies: a provider that changes the slices
// it answered after the call changes nothing the seam was given, which is
// what the guard checked.
func TestExtCallCopiesAnswers(t *testing.T) {
	srv := newExtServer(t, nil)
	ctx := t.Context()

	principal := ext.Principal{Subject: "u1", Name: "U", Grants: []ext.Grant{ext.GrantHost}}
	id := guardIdentity{s: srv, name: "cloud", p: fakeIdentity{answer: func(context.Context, string) (ext.Principal, bool, error) { return principal, true, nil }}}
	gotP, ok, _ := id.Authenticate(ctx, "tok")
	principal.Grants[0] = "root"
	if !ok || gotP.Grants[0] != ext.GrantHost {
		t.Fatalf("the provider's later change reached the principal: %+v", gotP)
	}

	servers := []ext.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}
	m := guardMedia{s: srv, name: "turn", p: fakeMedia{answer: func(context.Context) ([]ext.ICEServer, error) { return servers, nil }}}
	gotS, _ := m.ICEServers(ctx, ext.ICERequest{Purpose: "viewer"})
	servers[0].URLs[0] = "http://example.test"
	servers[0].Username = "changed"
	if len(gotS) != 1 || gotS[0].URLs[0] != "stun:stun.example.test:3478" || gotS[0].Username != "" {
		t.Fatalf("the provider's later change reached the servers: %+v", gotS)
	}

	blocks := []ext.ContextBlock{{Title: "t", Body: "b"}}
	c := &guardContext{s: srv, name: "graph", p: contextAnswer(blocks)}
	gotB, _ := c.Context(ctx, ext.ContextQuery{})
	blocks[0].Body = ""
	if len(gotB) != 1 || gotB[0].Body != "b" {
		t.Fatalf("the provider's later change reached the blocks: %+v", gotB)
	}

	by := ext.Principal{Subject: "u1", Name: "U", Grants: []ext.Grant{ext.GrantAdmin}}
	route := ext.Route{To: []ext.Destination{{Kind: "person", Name: "ann"}}, Answer: &ext.Answer{Input: "1", By: by}}
	r := guardRouter{s: srv, name: "switchman", p: routeAnswer{route}}
	gotR, _ := r.Route(ctx, ext.Decision{ID: "q1", Options: []ext.DecisionOption{{Input: "1"}}})
	route.To[0].Name = "bob"
	route.Answer.Input = "9"
	by.Grants[0] = "root"
	if len(gotR.To) != 1 || gotR.To[0].Name != "ann" || gotR.Answer == nil || gotR.Answer.Input != "1" || gotR.Answer.By.Grants[0] != ext.GrantAdmin {
		t.Fatalf("the provider's later change reached the route: %+v %+v", gotR, gotR.Answer)
	}
}

// The check of an answer runs inside the bounded call: a check held (a
// provider's long name being read) ends the call with the fallback when
// the caller leaves or the wait ends, and holds its slot until it returns,
// so held checks count against extInFlight like held calls.
func TestExtCallValidInsideTheCall(t *testing.T) {
	srv := newExtServer(t, nil)
	gate := make(chan struct{})
	in := make(chan struct{}, extInFlight)
	held := func(int) error {
		in <- struct{}{}
		<-gate
		return nil
	}
	answer := func(context.Context) (int, error) { return 1, nil }
	// The caller leaving while the check is held answers the fallback.
	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan int, extInFlight)
	for range extInFlight {
		go func() { got <- extCall(ctx, srv, ext.KindIdentity, "cloud", time.Hour, -1, held, answer) }()
	}
	for range extInFlight {
		<-in
	}
	cancel()
	for range extInFlight {
		if v := <-got; v != -1 {
			t.Fatalf("a call whose caller left during its check answered %d", v)
		}
	}
	// The checks still held hold their slots: the next call is busy.
	called := false
	if v := extCall(t.Context(), srv, ext.KindIdentity, "cloud", time.Hour, -1, nil, func(context.Context) (int, error) {
		called = true
		return 2, nil
	}); v != -1 || called {
		t.Fatalf("a call while %d checks are held answered %d, called %v", extInFlight, v, called)
	}
	close(gate)
	g := srv.guard(ext.KindIdentity, "cloud")
	if !waitFor(time.Minute, func() bool { return len(g.slots) == 0 }) {
		t.Fatalf("the released checks still hold %d slots", len(g.slots))
	}
	if v := extCall(t.Context(), srv, ext.KindIdentity, "cloud", time.Hour, -1, nil, answer); v != 1 {
		t.Fatalf("a call once the checks returned answered %d", v)
	}
	// The wait ending while the check is held answers the fallback too.
	gate2 := make(chan struct{})
	defer close(gate2)
	in2 := make(chan struct{}, 1)
	if v := extCall(t.Context(), srv, ext.KindIdentity, "late", time.Millisecond, -1, func(int) error {
		in2 <- struct{}{}
		<-gate2
		return nil
	}, answer); v != -1 {
		t.Fatalf("a check past the wait answered %d", v)
	}
	<-in2
	// Through the identity guard: a principal whose name is long is
	// checked inside the call, and refused.
	long := ext.Principal{Subject: "u1", Name: strings.Repeat("n", 1<<20), Grants: []ext.Grant{ext.GrantAdmin}}
	id := guardIdentity{s: srv, name: "cloud", p: fakeIdentity{answer: func(context.Context, string) (ext.Principal, bool, error) { return long, true, nil }}}
	if p, ok, _ := id.Authenticate(t.Context(), "tok"); ok {
		t.Fatalf("a principal with a name past its bound was used: %d bytes", len(p.Name))
	}
}

// A provider is given its own copy of what it is asked about: a router that
// changes the options of the question it was given, to make its answer one
// of them, has its answer checked against the question as asked, and the
// caller's question is unchanged; a context provider that changes its
// query's inputs changes nothing of the caller's.
func TestExtCallCopiesInputs(t *testing.T) {
	srv := newExtServer(t, nil)
	by := ext.Principal{Subject: "u1", Name: "U"}
	r := guardRouter{s: srv, name: "switchman", p: mutatingRouter{by: by}}
	d := ext.Decision{ID: "q1", Options: []ext.DecisionOption{{Label: "Yes", Input: "1"}}}
	if got, _ := r.Route(t.Context(), d); got.Answer != nil {
		t.Fatalf("a router that rewrote the question's options had its answer used: %+v", got.Answer)
	}
	if d.Options[0].Input != "1" {
		t.Fatalf("the router changed the caller's question: %+v", d.Options)
	}
	c := &guardContext{s: srv, name: "graph", p: mutatingContext{}}
	q := ext.ContextQuery{Task: "t", Inputs: []ext.ContextInput{{Task: "a", Commit: "c1"}}}
	if _, err := c.Context(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if q.Inputs[0].Commit != "c1" {
		t.Fatalf("the context provider changed the caller's query: %+v", q.Inputs)
	}
}

// A caller whose context ended before the call's goroutine read what it
// was asked (held at its start, through extCalling) has the default at
// once and may reuse its slices: the provider is given what the caller
// asked, never what it changed after, and what the provider changes of
// what it was given, once the caller is gone, reaches nothing of the
// caller's. Context's inputs and Room's grants both.
func TestExtCallCopiesInputsBeforeTheCall(t *testing.T) {
	srv := newExtServer(t, nil)
	hold := make(chan struct{})
	hook := func(_, name string) {
		if name == "held-graph" || name == "held-room" {
			<-hold
		}
	}
	extCalling.Store(&hook)
	t.Cleanup(func() { extCalling.Store(nil) })
	gone, cancel := context.WithCancel(t.Context())
	cancel()

	seenInput := make(chan string, 1)
	c := &guardContext{s: srv, name: "held-graph", p: seeingContext{seen: seenInput}}
	q := ext.ContextQuery{Task: "t", Inputs: []ext.ContextInput{{Task: "a", Commit: "c1"}}}
	if bs, err := c.Context(gone, q); bs != nil || err != nil {
		t.Fatalf("a call whose caller was gone answered %+v, %v; want the default", bs, err)
	}
	q.Inputs[0].Commit = "reused"

	seenGrant := make(chan ext.Grant, 1)
	m := guardMedia{s: srv, name: "held-room", p: seeingMedia{seen: seenGrant}}
	r := ext.RoomRequest{Kind: "run", ID: "r1", By: ext.Principal{Subject: "u1", Name: "U", Grants: []ext.Grant{ext.GrantHost}}}
	if _, ok, err := m.Room(gone, r); ok || err != nil {
		t.Fatalf("a room whose caller was gone answered ok %v, %v; want the default", ok, err)
	}
	r.By.Grants[0] = "reused"

	close(hold)
	if got := <-seenInput; got != "c1" {
		t.Fatalf("the context provider was given %q, what the caller changed after its call, want c1", got)
	}
	if got := <-seenGrant; got != ext.GrantHost {
		t.Fatalf("the media provider was given grant %q, what the caller changed after its call, want host", got)
	}
	if q.Inputs[0].Commit != "reused" || r.By.Grants[0] != "reused" {
		t.Fatalf("a provider changed the caller's request after its call: %+v, %+v", q.Inputs, r.By.Grants)
	}
}

// seeingContext hands the first input's commit of the query it is given to
// seen, after rewriting it.
type seeingContext struct{ seen chan<- string }

func (seeingContext) Name() string                               { return "held-graph" }
func (seeingContext) NoteTouch(context.Context, ext.TouchedFile) {}
func (s seeingContext) Context(_ context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error) {
	c := q.Inputs[0].Commit
	q.Inputs[0].Commit = "changed"
	s.seen <- c
	return nil, nil
}

// seeingMedia hands the first grant of the room request it is given to
// seen, after rewriting it to admin.
type seeingMedia struct{ seen chan<- ext.Grant }

func (seeingMedia) Name() string { return "held-room" }
func (seeingMedia) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
	return nil, nil
}
func (s seeingMedia) Room(_ context.Context, r ext.RoomRequest) (ext.RoomJoin, bool, error) {
	g := r.By.Grants[0]
	r.By.Grants[0] = ext.GrantAdmin
	s.seen <- g
	return ext.RoomJoin{}, false, nil
}

// mutatingRouter rewrites the first option of the question it is given to
// "9" and answers "9".
type mutatingRouter struct{ by ext.Principal }

func (mutatingRouter) Name() string { return "switchman" }
func (m mutatingRouter) Route(_ context.Context, d ext.Decision) (ext.Route, error) {
	d.Options[0].Input = "9"
	return ext.Route{Answer: &ext.Answer{Input: "9", By: m.by}}, nil
}
func (mutatingRouter) Resolved(context.Context, ext.Resolution) {}

// mutatingContext rewrites the first input of the query it is given.
type mutatingContext struct{}

func (mutatingContext) Name() string                               { return "graph" }
func (mutatingContext) NoteTouch(context.Context, ext.TouchedFile) {}
func (mutatingContext) Context(_ context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error) {
	q.Inputs[0].Commit = "changed"
	return nil, nil
}

// syncLog collects what a server logs while a test reads it.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
