package exttest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// T is the part of *testing.T the checks use, so a test can record what
// fails. Every call of it is made on the goroutine that called the check.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	TempDir() string
}

// Goroutines is how many goroutines call a method that must be safe for
// concurrent use at once (Authenticate, Route, Context, ICEServers), for
// the race detector to see what they share.
const Goroutines = 32

// UnknownToken is the token RunAll asks an identity provider about, which
// no provider should know.
const UnknownToken = "exttest-a-token-no-provider-issued"

// A phase is how a call is made: with the network open, blocked, or
// blocked with the call's context already cancelled.
type phase int

const (
	open phase = iota
	blocked
	cancelled
)

func (p phase) words() string {
	switch p {
	case blocked:
		return " with the network blocked"
	case cancelled:
		return " with its context cancelled and the network blocked"
	}
	return ""
}

// checker is one check function's run against one provider.
type checker struct {
	t     T
	n     *Network
	check string // the check function's name: Identity, Router, …
	name  string // the provider's

	mu   sync.Mutex
	said map[string]bool // what errorf said, said once
	errs []string        // what the calls' goroutines found, said by flush
}

// begin starts check against p: its name valid and asked within StartWait.
// ok false: there is nothing further to check (the failure said).
func begin(t T, n *Network, check string, p ext.Provider) (*checker, bool) {
	t.Helper()
	c := &checker{t: t, n: n, check: check, said: map[string]bool{}}
	if p == nil || nilValue(p) {
		t.Errorf("exttest: %s: a nil provider", check)
		return c, false
	}
	c.name = "?"
	name, ok := run(c, "Name", ext.StartWait, open, func(context.Context) string { return p.Name() })
	if !ok {
		c.flush()
		return c, false
	}
	c.name = name
	if !ext.ValidName(name) {
		c.errorf("Name %q is not a provider's name: 1 to %d of a-z, 0-9 and '-' beginning with a letter or a digit", name, ext.MaxProviderName)
	}
	return c, true
}

// errorf says a failure once, naming the check and the provider. It is
// called on the check's goroutine; the calls' goroutines use found.
func (c *checker) errorf(format string, args ...any) {
	c.t.Helper()
	msg := fmt.Sprintf("exttest: %s %q: ", c.check, c.name) + fmt.Sprintf(format, args...)
	c.mu.Lock()
	seen := c.said[msg]
	c.said[msg] = true
	c.mu.Unlock()
	if !seen {
		c.t.Errorf("%s", msg)
	}
}

// found keeps a failure a call's goroutine saw, for flush to say.
func (c *checker) found(format string, args ...any) {
	c.mu.Lock()
	c.errs = append(c.errs, fmt.Sprintf(format, args...))
	c.mu.Unlock()
}

// flush says what the calls' goroutines found.
func (c *checker) flush() {
	c.t.Helper()
	c.mu.Lock()
	errs := c.errs
	c.errs = nil
	c.mu.Unlock()
	for _, e := range errs {
		c.errorf("%s", e)
	}
}

// label is the mark of c's calls of method; every label of c begins with
// c.prefix().
func (c *checker) label(method string) string { return c.prefix() + method }
func (c *checker) prefix() string             { return c.check + " " + c.name + " " }

// breaches says each dial or request made under the context of c's calls
// of method.
func (c *checker) breaches(label, method string) {
	for _, d := range c.n.takeBreaches(label) {
		c.found("%s %s under its call's context: a call answers from what the provider holds, never with a round trip to its service on the call's path (article 8)", method, d.what())
	}
}

// done ends the check: what the provider's goroutines dialled under a
// call's context since, and every failure found, said; the network open
// again.
func (c *checker) done() {
	c.t.Helper()
	for _, d := range c.n.takeBreaches(c.prefix()) {
		c.found("%s under the context of its call %q, after that call returned: a call answers from what the provider holds, never with a round trip to its service on the call's path (article 8)", d.what(), d.Call)
	}
	c.n.Unblock()
	c.flush()
}

// run makes one call of method on a goroutine of its own, its context
// marked as that call's (and cancelled first in the cancelled phase), and
// waits for it to return for at most wait: the wait the server's guard
// gives that method, the product's cap. A call not back by then, one that
// panicked or ended its goroutine, and every dial or request made under
// its context are found; ok false when it did not return a value.
func run[R any](c *checker, method string, wait time.Duration, ph phase, fn func(context.Context) R) (R, bool) {
	var zero R
	label := c.label(method)
	ctx, cancel := context.WithCancel(onCall(context.Background(), label))
	defer cancel()
	if ph == cancelled {
		cancel()
	}
	type result struct {
		v        R
		returned bool
		panicked bool
		p        any
	}
	ch := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			if !r.returned {
				if p := recover(); p != nil {
					r.panicked, r.p = true, p
				}
			}
			ch <- r
		}()
		r.v = fn(ctx)
		r.returned = true
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var r result
	select {
	case r = <-ch:
	case <-timer.C:
		cancel()
		c.found("%s did not return within %s%s, the wait the server's guard gives it: it waits on its service, or something else, on the call's path (article 8: a call answers from what the provider holds)", method, wait, ph.words())
		c.breaches(label, method)
		return zero, false
	}
	c.breaches(label, method)
	switch {
	case r.panicked:
		c.found("%s panicked%s: %v", method, ph.words(), r.p)
		return zero, false
	case !r.returned:
		c.found("%s ended its goroutine without returning (runtime.Goexit)%s", method, ph.words())
		return zero, false
	}
	return r.v, true
}

// together makes Goroutines calls through call at once and waits for all
// of them: what each finds is c's.
func together(call func()) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range Goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			call()
		}()
	}
	close(start)
	wg.Wait()
}

// phases runs each of the three phases through call: the network open,
// blocked, and blocked with the call's context cancelled. The network is
// open again after.
func (c *checker) phases(call func(ph phase)) {
	call(open)
	c.n.Block()
	call(blocked)
	call(cancelled)
	c.n.Unblock()
}

// Identity checks an identity provider: a valid name; each of known
// recognised, as a valid principal with at least one grant, with the
// network open; none of unknown, and never the empty token, recognised;
// whenever it answers ok, a valid principal with a grant; and article 8
// (Network). known and unknown are never printed. With the network
// blocked a provider may answer an error for a known token (it cannot
// tell), never ok false.
func Identity(t T, n *Network, p ext.IdentityProvider, known, unknown []string) {
	t.Helper()
	c, ok := begin(t, n, "Identity", p)
	if !ok {
		return
	}
	defer c.done()
	type answer struct {
		p   ext.Principal
		ok  bool
		err error
	}
	auth := func(ph phase, token, which string, isKnown bool) {
		a, returned := run(c, "Authenticate", ext.IdentityWait, ph, func(ctx context.Context) answer {
			pr, ok, err := p.Authenticate(ctx, token)
			return answer{pr, ok, err}
		})
		if !returned {
			return
		}
		switch {
		case a.ok && a.err != nil:
			c.found("Authenticate answered ok and an error for %s%s: an error is for a token it cannot tell", which, ph.words())
		case a.ok && token == "":
			c.found("Authenticate said ok for the empty token%s: nothing is recognised for an empty token", ph.words())
		case a.ok && !isKnown:
			c.found("Authenticate said ok for %s, a token it was told it does not know%s", which, ph.words())
		case !a.ok && isKnown && ph == open:
			if a.err != nil {
				c.found("Authenticate answered an error for %s with the network open: %v", which, a.err)
			} else {
				c.found("Authenticate did not recognise %s with the network open", which)
			}
		case !a.ok && isKnown && ph == blocked && a.err == nil:
			c.found("Authenticate said ok false for %s with the network blocked: a provider that cannot tell answers an error, never ok false, which would end the token's connections", which)
		}
		if a.ok {
			if err := a.p.Valid(); err != nil {
				c.found("Authenticate answered a principal that is not valid for %s%s: %v", which, ph.words(), err)
			} else if len(a.p.Grants) == 0 {
				c.found("Authenticate answered a principal with no grant for %s%s: an admitted principal has at least one", which, ph.words())
			}
		}
	}
	all := func(ph phase) {
		auth(ph, "", "the empty token", false)
		for i, tok := range known {
			auth(ph, tok, fmt.Sprintf("known token %d", i), true)
		}
		for i, tok := range unknown {
			auth(ph, tok, fmt.Sprintf("unknown token %d", i), false)
		}
	}
	all(open)
	together(func() { all(open) })
	c.n.Block()
	all(blocked)
	all(cancelled)
	c.n.Unblock()
}

// Router checks a decision router against the decision d: a valid name;
// Route answering a route valid for d (Route.Valid) whenever it answers
// no error, from Goroutines goroutines at once too; Resolved returning; and
// article 8 (Network).
func Router(t T, n *Network, r ext.DecisionRouter, d ext.Decision) {
	t.Helper()
	c, ok := begin(t, n, "Router", r)
	if !ok {
		return
	}
	defer c.done()
	type answer struct {
		r   ext.Route
		err error
	}
	route := func(ph phase) {
		a, returned := run(c, "Route", ext.RouteWait, ph, func(ctx context.Context) answer {
			rt, err := r.Route(ctx, d)
			return answer{rt, err}
		})
		if returned && a.err == nil {
			if err := a.r.Valid(d); err != nil {
				c.found("Route answered a route not valid for its decision%s: %v", ph.words(), err)
			}
		}
	}
	resolved := func(ph phase) {
		run(c, "Resolved", ext.DeliveryWait, ph, func(ctx context.Context) struct{} {
			r.Resolved(ctx, ext.Resolution{ID: d.ID, Session: d.Session, At: time.Now().UTC()})
			return struct{}{}
		})
	}
	route(open)
	together(func() { route(open) })
	c.phases(func(ph phase) {
		if ph != open {
			route(ph)
		}
		resolved(ph)
	})
}

// Context checks a context provider against the query q: a valid name;
// Context answering at most ext.MaxContextBlocks blocks, each valid, from
// Goroutines goroutines at once too; NoteTouch returning; and article 8
// (Network).
func Context(t T, n *Network, p ext.ContextProvider, q ext.ContextQuery) {
	t.Helper()
	c, ok := begin(t, n, "Context", p)
	if !ok {
		return
	}
	defer c.done()
	touch := ext.TouchedFile{
		Session: ext.SessionRef{ID: "exttest-session"},
		Path:    filepath.Join(t.TempDir(), "main.go"),
		Op:      "edit",
		Tool:    "Edit",
		At:      time.Now().UTC(),
	}
	type answer struct {
		bs  []ext.ContextBlock
		err error
	}
	blocks := func(ph phase) {
		a, returned := run(c, "Context", ext.ContextWait, ph, func(ctx context.Context) answer {
			bs, err := p.Context(ctx, q)
			return answer{bs, err}
		})
		if !returned || a.err != nil {
			return
		}
		if len(a.bs) > ext.MaxContextBlocks {
			c.found("Context answered %d blocks%s, past MaxContextBlocks (%d)", len(a.bs), ph.words(), ext.MaxContextBlocks)
		}
		for i, b := range a.bs {
			if err := b.Valid(); err != nil {
				c.found("Context's block %d is not valid%s: %v", i, ph.words(), err)
			}
		}
	}
	noteTouch := func(ph phase) {
		run(c, "NoteTouch", ext.DeliveryWait, ph, func(ctx context.Context) struct{} {
			p.NoteTouch(ctx, touch)
			return struct{}{}
		})
	}
	blocks(open)
	together(func() { blocks(open) })
	c.phases(func(ph phase) {
		noteTouch(ph)
		if ph != open {
			blocks(ph)
		}
	})
}

// Sink checks a notification sink: a valid name; Events stable (the same
// answer every time, nil, taking everything, never confused with an empty
// list, taking nothing); Notify returning; and article 8 (Network).
func Sink(t T, n *Network, s ext.NotificationSink) {
	t.Helper()
	c, ok := begin(t, n, "Sink", s)
	if !ok {
		return
	}
	defer c.done()
	first, firstOK := run(c, "Events", ext.StartWait, open, func(context.Context) []string { return s.Events() })
	events := func(ph phase) {
		evs, ok := run(c, "Events", ext.StartWait, ph, func(context.Context) []string { return s.Events() })
		if ok && firstOK && !reflect.DeepEqual(evs, first) {
			c.found("Events answered %q%s, after %q: what a sink takes is read once and must not change", evs, ph.words(), first)
		}
	}
	notice := ext.Notification{
		Event:   "needs_input",
		Type:    "attention",
		At:      time.Now().UTC(),
		Session: ext.SessionRef{ID: "exttest-session", Name: "exttest"},
		State:   "open",
		Message: "Allow the edit?",
	}
	notify := func(ph phase) {
		run(c, "Notify", ext.DeliveryWait, ph, func(ctx context.Context) error { return s.Notify(ctx, notice) })
	}
	c.phases(func(ph phase) {
		if ph != cancelled {
			events(ph)
		}
		notify(ph)
	})
}

// Continuity checks a sync store: a valid name; Saved and Deleted
// returning; article 8 (Network); and Restore apart, the one call that may
// use the network on its own path: with the network open it returns,
// writing nothing when it answers ErrNothingToRestore and nothing once it
// has returned; with the network blocked it returns from what it holds or,
// when its download waits on the shut gate, returns ctx's error once ctx
// ends. Restore is given a directory of t.TempDir() and a logger that
// discards.
func Continuity(t T, n *Network, cs ext.ContinuityStore) {
	t.Helper()
	c, ok := begin(t, n, "Continuity", cs)
	if !ok {
		return
	}
	defer c.done()
	data := []byte("{}")
	saved := ext.Doc{Store: "data", Path: "catalog.json", Epoch: "0123456789abcdef0123456789abcdef", Rev: 1, At: time.Now().UTC(), Size: int64(len(data)), Data: data}
	deleted := ext.Doc{Store: "data", Path: "crews/exttest.json", Epoch: saved.Epoch, Rev: 2, At: saved.At}
	c.phases(func(ph phase) {
		run(c, "Saved", ext.DeliveryWait, ph, func(ctx context.Context) error { return cs.Saved(ctx, saved) })
		run(c, "Deleted", ext.DeliveryWait, ph, func(ctx context.Context) error { return cs.Deleted(ctx, deleted) })
	})
	c.flush()
	restore(c, cs)
}

// restore is Continuity's check of Restore.
func restore(c *checker, cs ext.ContinuityStore) {
	c.t.Helper()
	req := func() ext.RestoreRequest {
		return ext.RestoreRequest{Dir: c.t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	}
	type answer struct {
		err      error
		panicked any
		wrote    int64
	}
	call := func(ctx context.Context, w *afterWriter) <-chan answer {
		ch := make(chan answer, 1)
		r := req()
		go func() {
			var a answer
			defer func() {
				if p := recover(); p != nil {
					a.panicked = p
				}
				a.wrote = w.end()
				ch <- a
			}()
			a.err = cs.Restore(ctx, r, w)
		}()
		return ch
	}
	judge := func(a answer, w *afterWriter, words string) {
		if a.panicked != nil {
			c.errorf("Restore panicked%s: %v", words, a.panicked)
		}
		if errors.Is(a.err, ext.ErrNothingToRestore) && a.wrote > 0 {
			c.errorf("Restore wrote %d bytes and answered ErrNothingToRestore%s: a store that keeps no copy writes none", a.wrote, words)
		}
		if late := w.late(); late > 0 {
			c.errorf("Restore wrote %d bytes after it had returned%s: what it writes, it writes before it returns", late, words)
		}
	}

	// The network open: it returns, whatever its answer, and with no bound:
	// only the person's Ctrl-C ends a download.
	ctx, cancel := context.WithCancel(mayWait(context.Background(), c.label("Restore")))
	w := &afterWriter{}
	judge(<-call(ctx, w), w, "")
	cancel()

	// The network blocked: it answers from what it holds, or its download
	// waits on the gate until ctx ends, and then it returns ctx's error.
	c.n.Block()
	defer c.n.Unblock()
	label := c.label("Restore blocked")
	ctx, cancel = context.WithCancel(mayWait(context.Background(), label))
	defer cancel()
	w = &afterWriter{}
	answered := call(ctx, w)
	var waiting <-chan struct{}
	if c.n != nil {
		waiting = c.n.whenWaiting(label)
	}
	// A Restore that neither returns nor waits on the gate under its
	// context is ended the same way, once DeliveryWait has passed: the
	// wait `conductor restore` gives a Restore its person stopped, before
	// it lets it go.
	timer := time.NewTimer(ext.DeliveryWait)
	defer timer.Stop()
	select {
	case a := <-answered:
		judge(a, w, " with the network blocked")
		return
	case <-waiting:
	case <-timer.C:
	}
	cancel()
	timer.Reset(ext.DeliveryWait)
	select {
	case a := <-answered:
		judge(a, w, " once its context ended with the network blocked")
		if a.panicked == nil && !errors.Is(a.err, context.Canceled) {
			c.errorf("Restore answered %v once its context ended with the network blocked, not its context's error", a.err)
		}
	case <-timer.C:
		c.errorf("Restore did not return within %s of its context's end with the network blocked: it must end when its context does", ext.DeliveryWait)
	}
}

// afterWriter takes what a Restore writes, counting what it writes after
// it returned.
type afterWriter struct {
	mu    sync.Mutex
	n     int64
	ended bool
	after int64
}

func (w *afterWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended {
		w.after += int64(len(p))
		return 0, errors.New("exttest: Restore has returned")
	}
	w.n += int64(len(p))
	return len(p), nil
}

// end marks the Restore returned and answers what it wrote.
func (w *afterWriter) end() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ended = true
	return w.n
}

func (w *afterWriter) late() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.after
}

// Entitlements checks an entitlement source: a valid name; Entitled
// answering a limit that is not negative; and article 8 (Network). No
// guard of this repository calls it (only providers do), so it is held to
// StartWait, the longest wait ext names for a direct call.
func Entitlements(t T, n *Network, e ext.EntitlementSource) {
	t.Helper()
	c, ok := begin(t, n, "Entitlements", e)
	if !ok {
		return
	}
	defer c.done()
	type answer struct {
		e   ext.Entitlement
		err error
	}
	c.phases(func(ph phase) {
		a, returned := run(c, "Entitled", ext.StartWait, ph, func(ctx context.Context) answer {
			en, err := e.Entitled(ctx, "exttest-feature")
			return answer{en, err}
		})
		if returned && a.err == nil && a.e.Limit < 0 {
			c.found("Entitled answered a limit of %d%s: 0 is none, never below", a.e.Limit, ph.words())
		}
	})
}

// Media checks a media room provider: a valid name; ICEServers answering
// at most ext.MaxICEServers servers, each valid, from Goroutines
// goroutines at once too; Room returning; and article 8 (Network).
func Media(t T, n *Network, m ext.MediaRoomProvider) {
	t.Helper()
	c, ok := begin(t, n, "Media", m)
	if !ok {
		return
	}
	defer c.done()
	type answer struct {
		ss  []ext.ICEServer
		err error
	}
	ice := func(ph phase) {
		a, returned := run(c, "ICEServers", ext.ICEWait, ph, func(ctx context.Context) answer {
			ss, err := m.ICEServers(ctx, ext.ICERequest{Purpose: "viewer", Session: "exttest-session", Role: "control", Viewer: "exttest-viewer"})
			return answer{ss, err}
		})
		if !returned || a.err != nil {
			return
		}
		if len(a.ss) > ext.MaxICEServers {
			c.found("ICEServers answered %d servers%s, past MaxICEServers (%d)", len(a.ss), ph.words(), ext.MaxICEServers)
		}
		for i, s := range a.ss {
			if err := s.Valid(); err != nil {
				c.found("ICEServers' server %d is not valid%s: %v", i, ph.words(), err)
			}
		}
	}
	room := func(ph phase) {
		run(c, "Room", ext.ICEWait, ph, func(ctx context.Context) struct{} {
			m.Room(ctx, ext.RoomRequest{Kind: "run", ID: "exttest-run", By: ext.Principal{Subject: "exttest", Name: "exttest"}})
			return struct{}{}
		})
	}
	ice(open)
	together(func() { ice(open) })
	c.phases(func(ph phase) {
		if ph != open {
			ice(ph)
		}
		room(ph)
	})
}

// Runtime checks a runtime: a valid name; Where valid and the same every
// time, with the network blocked too. Where takes no context: no guard
// bounds it yet, so it is held to StartWait, the longest wait ext names
// for a direct call. The Runtime seam (docs/plans/runtime.md) adds the
// checks of its methods.
func Runtime(t T, n *Network, r ext.Runtime) {
	t.Helper()
	c, ok := begin(t, n, "Runtime", r)
	if !ok {
		return
	}
	defer c.done()
	first, firstOK := run(c, "Where", ext.StartWait, open, func(context.Context) ext.Where { return r.Where() })
	if firstOK {
		if err := first.Valid(); err != nil {
			c.found("Where is not valid: %v", err)
		}
	}
	c.n.Block()
	w, ok := run(c, "Where", ext.StartWait, blocked, func(context.Context) ext.Where { return r.Where() })
	if ok && firstOK && w != first {
		c.found("Where answered %q with the network blocked, after %q: a runtime's Where does not change", w, first)
	}
	c.n.Unblock()
}

// Starter checks a provider's Start: with a FakeHost and the network both
// blocked, Start returns within ext.StartWait (the gate shut until it has
// returned), and, once its context is cancelled and the host closed, the
// provider takes the closed host's refusals (context.Canceled) without a
// panic; a second try, with a new context and a new host, as the server
// makes after a failure, returns the same way. Start may answer an error
// (for what no retry mends); it may not wait.
func Starter(t T, n *Network, s ext.Starter) {
	t.Helper()
	if s == nil || nilValue(s) {
		t.Errorf("exttest: Starter: a nil provider")
		return
	}
	c := &checker{t: t, n: n, check: "Starter", said: map[string]bool{}}
	c.name = "?"
	if p, ok := s.(ext.Provider); ok {
		if name, ok := run(c, "Name", ext.StartWait, open, func(context.Context) string { return p.Name() }); ok {
			c.name = name
		}
	}
	defer c.done()
	for try := 1; try <= 2; try++ {
		h := NewFakeHost(t)
		h.Block()
		c.n.Block()
		ctx, cancel := context.WithCancel(context.Background())
		type answer struct {
			err      error
			returned bool
			panicked any
		}
		ch := make(chan answer, 1)
		go func() {
			var a answer
			defer func() {
				if !a.returned {
					a.panicked = recover()
				}
				ch <- a
			}()
			a.err = s.Start(ctx, h)
			a.returned = true
		}()
		timer := time.NewTimer(ext.StartWait)
		var a answer
		late := false
		select {
		case a = <-ch:
		case <-timer.C:
			late = true
		}
		timer.Stop()
		cancel()
		h.Close()
		c.n.Unblock()
		switch {
		case late:
			c.errorf("Start (try %d) did not return within StartWait (%s) with its host and the network blocked: Start returns at once, and reaches its service and its host on goroutines of its own", try, ext.StartWait)
			return
		case a.panicked != nil:
			c.errorf("Start (try %d) panicked: %v", try, a.panicked)
			return
		case !a.returned:
			c.errorf("Start (try %d) ended its goroutine without returning (runtime.Goexit)", try)
			return
		}
	}
}

// nilValue reports whether v, an interface that is not nil, holds a nil
// pointer, map, slice, func or channel.
func nilValue(v any) bool {
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return rv.IsNil()
	}
	return false
}
