package exttest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
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
	check string       // the check function's name: Identity, Router, …
	name  string       // the provider's
	p     ext.Provider // the provider, its Name asked again with the network blocked by done

	// secrets are what a check is given and never prints (Identity's
	// tokens): errorf writes [token] in their place, as they are and
	// query- or path-escaped, wherever the provider's words put them.
	secrets []string

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
	c.name, c.p = name, p
	if !ext.ValidName(name) {
		c.errorf("Name %q is not a provider's name: 1 to %d of a-z, 0-9 and '-' beginning with a letter or a digit", name, ext.MaxProviderName)
	}
	return c, true
}

// errorf says a failure once, naming the check and the provider. It is
// called on the check's goroutine; the calls' goroutines use found. What
// it says is kept from tokens (keep): every URL in it is shown by its
// scheme, host and path, and c's secrets are stood in for.
func (c *checker) errorf(format string, args ...any) {
	c.t.Helper()
	msg := c.keep(fmt.Sprintf("exttest: %s %q: ", c.check, c.name) + fmt.Sprintf(format, args...))
	c.mu.Lock()
	seen := c.said[msg]
	c.said[msg] = true
	c.mu.Unlock()
	if !seen {
		c.t.Errorf("%s", msg)
	}
}

// urlIn is a URL in a message, as an *url.Error and most clients print
// one: a scheme, "://", and what follows up to a space, a quote or an
// angle bracket.
var urlIn = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>` + "`" + `]+`)

// keep is msg as a check may print it: each URL in it shown by its scheme,
// host and path, never its user information, query or fragment (a token,
// the one asked about or the provider's own, may ride there; one that does
// not parse is cut at its first '?' or '#' and loses what comes before
// an '@'), and each of c's secrets, as it is and query- or path-escaped,
// stood in for by [token]. A token the provider's words print bare, not in
// a URL, and not one the check was given, is beyond what it can know.
func (c *checker) keep(msg string) string {
	msg = urlIn.ReplaceAllStringFunc(msg, func(raw string) string {
		if u, err := url.Parse(raw); err == nil {
			return shownURL(u)
		}
		if i := strings.IndexAny(raw, "?#"); i >= 0 {
			raw = raw[:i]
		}
		if scheme, rest, ok := strings.Cut(raw, "://"); ok {
			if i := strings.LastIndex(rest, "@"); i >= 0 {
				rest = rest[i+1:]
			}
			raw = scheme + "://" + rest
		}
		return raw
	})
	for _, s := range c.secrets {
		if s == "" {
			continue
		}
		for _, form := range []string{s, url.QueryEscape(s), url.PathEscape(s)} {
			msg = strings.ReplaceAll(msg, form, "[token]")
		}
	}
	return msg
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

// done ends the check: Name asked once more with the network blocked (it
// too answers from what the provider holds); what the provider's
// goroutines dialled under a call's context since, and every failure
// found, said; the network open again.
func (c *checker) done() {
	c.t.Helper()
	if c.p != nil {
		c.n.Block()
		run(c, "Name", ext.StartWait, blocked, func(context.Context) string { return c.p.Name() })
	}
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
// (Network). known and unknown are never printed, not even inside the
// provider's own words: [token] stands in their place, as they are and
// query- or path-escaped, and a URL the words name (an error naming the
// request a token was sent in) is shown without its query, user
// information or fragment, whatever credential rides there. With the
// network blocked a provider may
// answer an error for a known token (it cannot tell), never ok false: a
// provider then still holds a current lease, and ok false is for an
// admission that has ended, which ends the token's connections. A
// provider that offers a sign-in (ext.SignIn) has it checked after
// (SignIn).
func Identity(t T, n *Network, p ext.IdentityProvider, known, unknown []string) {
	t.Helper()
	c, ok := begin(t, n, "Identity", p)
	if !ok {
		return
	}
	if s, ok := p.(ext.SignIn); ok {
		defer SignIn(t, n, s)
	}
	c.secrets = append(append([]string(nil), known...), unknown...)
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
			c.found("Authenticate said ok false for %s with the network blocked: a provider that cannot tell answers an error, never ok false, which is for an admission that has ended and ends the token's connections", which)
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

// Router checks a decision router against questions like the decision d,
// each a question of its own (d's ID with "-1", "-2", … after it), since
// Route is told each question once: a valid name; Route answering a route
// valid for its question (Route.Valid) whenever it answers no error, from
// Goroutines goroutines at once too; Resolved returning, told once of each
// question Route was given, in order from one goroutine; and article 8
// (Network). Each question is asked both as d's Request has it and as the
// other: a session's question (Request empty) and a durable decision's
// record (Request its ID, a valid reference dr-<n> of its own, Kind
// "decision", no Source, each option picked by its place, Program item
// DR1), told Resolved with its Request. A record is routed twice and told
// Resolved twice, as a server tells a router again whenever it holds the
// ledger (ext.DecisionRouter): a repeat is answered as the first was. A
// router that fetches a question's route on the call's path is caught on
// the questions asked with the network blocked, which it has never been
// given before.
// decisionRef is a durable decision's reference, dr-<n>.
var decisionRef = regexp.MustCompile(`^dr-[1-9][0-9]{0,17}$`)

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
	var (
		mu     sync.Mutex
		asked  int
		routed []ext.Decision // given to Route, not yet to Resolved
	)
	// A record's reference is dr-<n>, each its own: past d's own number
	// when d is a record.
	base := int64(0)
	if n, err := strconv.ParseInt(strings.TrimPrefix(d.Request, "dr-"), 10, 64); err == nil && n > 0 && n < 1<<40 {
		base = n * 1000
	}
	question := func(record bool) ext.Decision {
		mu.Lock()
		defer mu.Unlock()
		asked++
		q := d
		q.ID = fmt.Sprintf("%s-%d", d.ID, asked)
		q.Options = append([]ext.DecisionOption(nil), d.Options...)
		if record {
			q.ID = fmt.Sprintf("dr-%d", base+int64(asked))
		}
		if record != (d.Request != "") {
			// The other kind of question than d: a record for a session's
			// question, a session's question for a record.
			q.Request, q.Kind, q.Source = "", "prompt", "api"
			if record {
				q.Request, q.Kind, q.Source = q.ID, "decision", ""
				for i := range q.Options {
					q.Options[i].Input = strconv.Itoa(i + 1)
				}
			}
		} else if record {
			q.Request = q.ID
		}
		if record && !decisionRef.MatchString(q.Request) {
			panic(fmt.Sprintf("exttest: a record's reference %q is not dr-<n>", q.Request))
		}
		routed = append(routed, q)
		return q
	}
	routeOne := func(ph phase, record bool) {
		q := question(record)
		times := 1
		if record {
			times = 2 // told again, as a server that takes the ledger over tells it
		}
		for range times {
			a, returned := run(c, "Route", ext.RouteWait, ph, func(ctx context.Context) answer {
				rt, err := r.Route(ctx, q)
				return answer{rt, err}
			})
			if returned && a.err == nil {
				if err := a.r.Valid(q); err != nil {
					c.found("Route answered a route not valid for its decision%s: %v", ph.words(), err)
				}
			}
		}
	}
	route := func(ph phase) {
		routeOne(ph, false)
		routeOne(ph, true)
	}
	resolved := func(ph phase) {
		mu.Lock()
		qs := routed
		routed = nil
		mu.Unlock()
		for _, q := range qs {
			times := 1
			if q.Request != "" {
				times = 2
			}
			for range times {
				run(c, "Resolved", ext.DeliveryWait, ph, func(ctx context.Context) struct{} {
					r.Resolved(ctx, ext.Resolution{ID: q.ID, Session: q.Session, At: time.Now().UTC(), Request: q.Request})
					return struct{}{}
				})
			}
		}
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
// list, taking nothing); Notify returning; and article 8 (Network). Notify
// is handed what the server would hand the sink (notices): a notification
// of each event or type its Events lists, as the server types one, since
// the server hands a sink nothing else; the attention entry needs_input
// when it takes every entry, or nothing.
func Sink(t T, n *Network, s ext.NotificationSink) {
	t.Helper()
	c, ok := begin(t, n, "Sink", s)
	if !ok {
		return
	}
	defer c.done()
	first, firstOK := run(c, "Events", ext.StartWait, open, func(context.Context) []string { return s.Events() })
	if first != nil {
		// A copy, nil and empty kept apart: a sink that changes the array it
		// answered, and answers it again, must not change first with it.
		first = append([]string{}, first...)
	}
	events := func(ph phase) {
		evs, ok := run(c, "Events", ext.StartWait, ph, func(context.Context) []string { return s.Events() })
		if ok && firstOK && !reflect.DeepEqual(evs, first) {
			c.found("Events answered %q%s, after %q: what a sink takes is read once and must not change", evs, ph.words(), first)
		}
	}
	var takes []string
	if firstOK {
		takes = first
	}
	given := notices(takes)
	notify := func(ph phase) {
		for _, notice := range given {
			run(c, "Notify", ext.DeliveryWait, ph, func(ctx context.Context) error { return s.Notify(ctx, notice) })
		}
	}
	c.phases(func(ph phase) {
		if ph != cancelled {
			events(ph)
		}
		notify(ph)
	})
}

// notices is a notification of each name in events, once each, as the
// server types it (internal/api's eventTypeOf): an attention state
// (needs_input, working, done) an attention entry recording it;
// exit_nonzero a status entry of a process that exited with 1; a provider
// notice for ext.TypeProvider; one of the entry types the Events page
// names by their type (progress, artifact, handoff, tool_use,
// tool_denied, error) an entry of that type named so; any other name an
// entry of that type with no Events name. With no name (events nil, or a
// sink taking nothing, which the server hands nothing), the attention
// entry needs_input.
func notices(events []string) []ext.Notification {
	base := ext.Notification{
		At:      time.Now().UTC(),
		Session: ext.SessionRef{ID: "exttest-session", Name: "exttest"},
		Message: "Allow the edit?",
	}
	var out []ext.Notification
	seen := map[string]bool{}
	for _, ev := range events {
		if seen[ev] {
			continue
		}
		seen[ev] = true
		n := base
		switch ev {
		case "needs_input", "working", "done":
			n.Event, n.Type, n.State = ev, "attention", ev
		case "exit_nonzero":
			n.Event, n.Type, n.Message = ev, "status", "exited (exit 1)"
		case ext.TypeProvider:
			n.Type, n.Origin, n.Message = ext.TypeProvider, "exttest-provider", "A provider's notice"
		case "progress", "artifact", "handoff", "tool_use", "tool_denied", "error":
			n.Event, n.Type = ev, ev
		default:
			n.Type = ev
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		n := base
		n.Event, n.Type, n.State = "needs_input", "attention", "needs_input"
		out = append(out, n)
	}
	return out
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
	// Every writer is judged again for late writes once every Restore's
	// context has ended (this defer runs after the others): a goroutine of
	// Restore's that writes once its context ends is seen when it writes
	// before Continuity returns, not only when it wrote before its Restore
	// was judged.
	type writer struct {
		w     *afterWriter
		words string
	}
	var writers []writer
	defer func() {
		c.t.Helper()
		for _, w := range writers {
			if late := w.w.late(); late > 0 {
				c.errorf("Restore wrote %d bytes after it had returned%s: what it writes, it writes before it returns", late, w.words)
			}
		}
	}()
	newWriter := func(words string) *afterWriter {
		w := &afterWriter{}
		writers = append(writers, writer{w, words})
		return w
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
	judge := func(a answer, words string) {
		if a.panicked != nil {
			c.errorf("Restore panicked%s: %v", words, a.panicked)
		}
		if errors.Is(a.err, ext.ErrNothingToRestore) && a.wrote > 0 {
			c.errorf("Restore wrote %d bytes and answered ErrNothingToRestore%s: a store that keeps no copy writes none", a.wrote, words)
		}
	}

	// The network open: it returns, whatever its answer, and with no bound:
	// only the person's Ctrl-C ends a download.
	ctx, cancel := context.WithCancel(mayWait(context.Background(), c.label("Restore")))
	w := newWriter("")
	judge(<-call(ctx, w), "")
	cancel()

	// The network blocked: it answers from what it holds, or its download
	// waits on the gate until ctx ends, and then it returns ctx's error.
	c.n.Block()
	defer c.n.Unblock()
	label := c.label("Restore blocked")
	ctx, cancel = context.WithCancel(mayWait(context.Background(), label))
	defer cancel()
	w = newWriter(" with the network blocked")
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
		cancel()
		judge(a, " with the network blocked")
		return
	case <-waiting:
	case <-timer.C:
	}
	cancel()
	timer.Reset(ext.DeliveryWait)
	select {
	case a := <-answered:
		judge(a, " once its context ended with the network blocked")
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

// Runtime checks a runtime without starting anything: a valid name,
// the same every time; Where valid and the same every time, with the
// network blocked too; and Takes, asked of a sample ref from Goroutines
// goroutines at once, with the network open and blocked, answering within
// ext.TakesWait, the wait the server's guard gives it (it answers from the
// ref alone, with no I/O and no wait). Name and Where take no context: no
// guard bounds them, so they are held to StartWait, the longest wait ext
// names for a direct call. RuntimeExec checks the methods that start
// processes, and RunAll runs it with WithRuntimeCase's case.
func Runtime(t T, n *Network, r ext.Runtime) {
	t.Helper()
	runtimeStatic(t, n, r)
}

// sampleRef is the ref Runtime asks Takes about: a managed run's.
var sampleRef = ext.ProcessRef{Run: "exttest-00000001"}

// runtimeStatic is Runtime, answering the Name and the Where r first
// answered (ok false when it gave none, the failure said), so a caller
// names r without calling it outside the checks' bounds.
func runtimeStatic(t T, n *Network, r ext.Runtime) (string, ext.Where, bool) {
	t.Helper()
	c, ok := begin(t, n, "Runtime", r)
	if !ok {
		return c.name, "", false
	}
	defer c.done()
	first, firstOK := run(c, "Where", ext.StartWait, open, func(context.Context) ext.Where { return r.Where() })
	if firstOK {
		if err := first.Valid(); err != nil {
			c.found("Where is not valid: %v", err)
		}
	}
	takes := func(ph phase) {
		together(func() {
			run(c, "Takes", ext.TakesWait, ph, func(context.Context) bool { return r.Takes(sampleRef) })
		})
	}
	takes(open)
	c.n.Block()
	w, ok := run(c, "Where", ext.StartWait, blocked, func(context.Context) ext.Where { return r.Where() })
	if ok && firstOK && w != first {
		c.found("Where answered %q with the network blocked, after %q: a runtime's Where does not change", w, first)
	}
	if name, ok := run(c, "Name", ext.StartWait, blocked, func(context.Context) string { return r.Name() }); ok && name != c.name {
		c.found("Name answered %q with the network blocked, after %q: a runtime's Name does not change", name, c.name)
	}
	takes(blocked)
	c.n.Unblock()
	return c.name, first, firstOK
}

// Starter checks a provider's Start: with a FakeHost and the network both
// blocked, Start returns within ext.StartWait (the gate shut until it has
// returned); then its context is cancelled, the host closed and the
// network opened, and Starter goes on only once every call the host held
// has been answered with the closed host's refusal (context.Canceled) and
// every dial and byte the network held has been let go, so each refusal
// reaches the provider while the check runs; a second try, with a new
// context and a new host, as the server makes after a failure, returns the
// same way. Start may answer an error (for what no retry mends); it may
// not wait. What a provider's goroutine does with a refusal once it has it
// runs on that goroutine, which no check can wait on or recover: a panic
// there ends the test binary, failing the run, whenever it comes before
// the binary exits, and is missed when it comes later. Give Starter a
// value not yet started: it starts it twice itself.
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
			c.name, c.p = name, p
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
		// The calls held at the close answer at once (the fake lets them go
		// on Close), as the held dials do on Unblock or their context's end:
		// StartWait only bounds a hang of the suite's own.
		drain, stop := context.WithTimeout(context.Background(), ext.StartWait)
		if err := Await(drain, h, func() bool { return h.Waiting() == 0 }); err != nil {
			c.errorf("the closed host still held %d of try %d's calls after %s: exttest's FakeHost lets every held call go on Close", h.Waiting(), try, ext.StartWait)
		}
		select {
		case <-c.n.settled():
		case <-drain.Done():
			c.errorf("the network still held a dial or a connection's bytes %s after try %d's end: exttest's Network lets them go on Unblock", ext.StartWait, try)
		}
		stop()
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
