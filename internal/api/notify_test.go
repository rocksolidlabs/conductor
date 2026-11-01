package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// newNotifyEnv is a test server with the webhooks hooks and the
// notification sinks sinks, registered in order, each a provider of its
// own name.
func newNotifyEnv(t *testing.T, log *slog.Logger, hooks []config.Webhook, sinks ...ext.NotificationSink) *testEnv {
	t.Helper()
	p := ext.Providers{Notifications: sinks}
	for _, s := range sinks {
		p.All = append(p.All, ext.Entry{Kind: ext.KindNotifications, Name: s.Name(), Value: s})
	}
	return newTestEnvAgents(t, func(c *config.Config) { c.Webhooks = hooks }, log, nil, WithExtensions(local.Fill(p)))
}

// recSink is a notification sink that keeps what it is given, in order.
// Its Events answers events (counting the asks); block, when set, holds
// every Notify until it is closed, whatever its context says (a sink that
// never returns), entered closed at the first. With breaks, a
// notification whose message is "panic" panics.
type recSink struct {
	name    string
	events  []string
	breaks  bool
	block   chan struct{}
	entered chan struct{}
	asked   atomic.Int32

	mu  sync.Mutex
	got []ext.Notification
}

func (s *recSink) Name() string { return s.name }
func (s *recSink) Events() []string {
	s.asked.Add(1)
	return s.events
}
func (s *recSink) Notify(_ context.Context, n ext.Notification) error {
	if s.block != nil {
		select {
		case <-s.entered:
		default:
			close(s.entered)
		}
		<-s.block
	}
	if s.breaks && n.Message == "panic" {
		panic("the sink broke")
	}
	s.mu.Lock()
	s.got = append(s.got, n)
	s.mu.Unlock()
	return nil
}

// all is what the sink was given so far.
func (s *recSink) all() []ext.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

// waitFor waits until what the sink was given holds what done says, which
// holds once reached, and answers it.
func (s *recSink) waitFor(t *testing.T, what string, done func([]ext.Notification) bool) []ext.Notification {
	t.Helper()
	end := time.Now().Add(testWait)
	for {
		got := s.all()
		if done(got) {
			return got
		}
		if time.Now().After(end) {
			t.Fatalf("%s was never given %s: it has %d", s.name, what, len(got))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// messages is the messages of the notifications of type typ in ns.
func messages(ns []ext.Notification, typ string) []string {
	var out []string
	for _, n := range ns {
		if n.Type == typ {
			out = append(out, n.Message)
		}
	}
	return out
}

// heldSink is a recSink that is an ext.Starter: its queue holds what
// reaches it until its Start has succeeded, which no test of it asks
// for unless it calls StartExtensions.
type heldSink struct{ recSink }

func (*heldSink) Start(context.Context, ext.Host) error { return nil }

// sinkOf is the guarded sink of the provider name on srv.
func sinkOf(t *testing.T, srv *Server, name string) *guardSink {
	t.Helper()
	for _, g := range srv.notifier.sinks {
		if g.name == name {
			return g
		}
	}
	t.Fatalf("no sink %s", name)
	return nil
}

// drain takes everything q holds, in order.
func drain[T any](q *extQueue[T]) []T {
	var out []T
	for {
		v, ok := q.pop()
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

// mainPayload and mainSession are a delivery's body as main built it,
// kept here so that the body a webhook is sent is checked against main's
// construction, not against the code under test.
type mainPayload struct {
	SessionID string                `json:"sessionId"`
	Session   mainSession           `json:"session"`
	Entry     session.ActivityEntry `json:"entry"`
}

type mainSession struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	AgentID string `json:"agentId"`
}

// A webhook is the first kind of sink and is sent what it was sent before
// the providers' sinks: for every kind of entry, the body is main's
// webhookPayload byte for byte, X-Conductor-Event the event name the
// webhook lists or else the entry's own type, the signature the HMAC of
// that body, with or without a provider's sink beside it. Every field of
// an entry survives its notification. (Every other test of
// webhooks_test.go runs unchanged beside this one.)
func TestWebhooksUnchanged(t *testing.T) {
	const secret = "s3cret"
	at := time.Date(2026, 10, 31, 12, 0, 0, 123456789, time.UTC)
	cases := []struct {
		name  string
		entry session.ActivityEntry
		state session.AttentionState
		event string // X-Conductor-Event
	}{
		{"needs input", session.ActivityEntry{At: at, Type: session.ActivityAttention, Message: "approve?"}, session.AttentionNeedsInput, "needs_input"},
		{"working", session.ActivityEntry{At: at, Type: session.ActivityAttention, Message: "compiling"}, session.AttentionWorking, "working"},
		{"a state the entry names", session.ActivityEntry{At: at, Type: session.ActivityAttention, Message: "done"}, "", "done"},
		{"an attention entry with no state", session.ActivityEntry{At: at, Type: session.ActivityAttention, Message: "Waiting"}, "", "attention"},
		{"exit nonzero", session.ActivityEntry{At: at, Type: session.ActivityStatus, Message: "exited (exit 3)"}, "", "exit_nonzero"},
		{"exit zero", session.ActivityEntry{At: at, Type: session.ActivityStatus, Message: "exited (exit 0)"}, "", "status"},
		{"progress", session.ActivityEntry{At: at, Type: session.ActivityProgress, Message: "1/3 \"quoted\" <b>&</b>", ByName: "agent"}, "", "progress"},
		{"artifact", session.ActivityEntry{At: at, Type: session.ActivityArtifact, URL: "https://example.test/a?b=c&d=e"}, "", "artifact"},
		{"handoff", session.ActivityEntry{At: at, Type: session.ActivityHandoff, To: "tests", Message: "over to you"}, "", "handoff"},
		{"tool use", session.ActivityEntry{At: at, Type: session.ActivityToolUse, Tool: "Bash"}, "", "tool_use"},
		{"tool denied", session.ActivityEntry{At: at, Type: session.ActivityToolDenied, Tool: "Write"}, "", "tool_denied"},
		{"error", session.ActivityEntry{At: at, Type: session.ActivityError, Message: "boom ünïcode"}, "", "error"},
		{"file", session.ActivityEntry{At: at, Type: session.ActivityFile, Op: session.FileOpEdit, Path: "src/a.go", Tool: "Edit"}, "", "file"},
		{"join", session.ActivityEntry{At: at, Type: session.ActivityJoin, By: "sub-1", ByName: "Ada"}, "", "join"},
		{"leave", session.ActivityEntry{At: at, Type: session.ActivityLeave, By: "sub-1", ByName: "Ada"}, "", "leave"},
		{"typed input", session.ActivityEntry{At: at, Type: session.ActivityInput, Op: session.OpTyped, Message: "go on", ByName: "crew"}, "", "input"},
		{"link", session.ActivityEntry{At: at, Type: session.ActivityLink, Message: "link created"}, "", "link"},
	}
	every := []string{"needs_input", "working", "done", "exit_nonzero", "attention", "status", "progress", "artifact",
		"handoff", "tool_use", "tool_denied", "error", "file", "join", "leave", "input", "link"}
	for _, beside := range []bool{false, true} {
		t.Run(fmt.Sprintf("a provider's sink beside: %v", beside), func(t *testing.T) {
			rcv := newReceiver(t, nil)
			hooks := []config.Webhook{{URL: rcv.URL, Events: every, Secret: secret, AllowPrivate: true}}
			var sinks []ext.NotificationSink
			other := &recSink{name: "push"}
			if beside {
				sinks = append(sinks, other)
			}
			e := newNotifyEnv(t, nil, hooks, sinks...)
			id := e.createSession("cat")
			info := e.srv.registry.List()
			name := ""
			for _, i := range info {
				if i.ID == id {
					name = i.Name
				}
			}
			for _, sid := range []string{id, "not-a-session"} {
				for _, tc := range cases {
					e.srv.events.activity(sid, tc.entry, tc.state)
					d := rcv.next(t)
					ms := mainSession{ID: sid}
					if sid == id {
						ms.Name, ms.AgentID = name, "cat"
					}
					want, err := json.Marshal(mainPayload{SessionID: sid, Session: ms, Entry: tc.entry})
					if err != nil {
						t.Fatal(err)
					}
					if string(d.body) != string(want) {
						t.Fatalf("%s (%s): body\n%s\nwant\n%s", tc.name, sid, d.body, want)
					}
					if got := d.header.Get("X-Conductor-Event"); got != tc.event {
						t.Fatalf("%s: X-Conductor-Event %q, want %q", tc.name, got, tc.event)
					}
					mac := hmac.New(sha256.New, []byte(secret))
					mac.Write(want)
					if got := d.header.Get("X-Conductor-Signature"); got != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
						t.Fatalf("%s: signature %q", tc.name, got)
					}
					if d.method != http.MethodPost || d.header.Get("Content-Type") != "application/json" {
						t.Fatalf("%s: %s %s", tc.name, d.method, d.header.Get("Content-Type"))
					}
				}
			}
			rcv.none(t, 100*time.Millisecond)
			if beside {
				// The sink beside was given every one of them too.
				other.waitFor(t, "every entry", func(ns []ext.Notification) bool {
					n := 0
					for _, x := range ns {
						if x.At.Equal(at) {
							n++
						}
					}
					return n == 2*len(cases)
				})
			}
		})
	}

	// Every field of an entry survives its notification: an entry with
	// each field set comes back whole from it, so a field ActivityEntry
	// gains and the notification drops fails here.
	var full session.ActivityEntry
	v := reflect.ValueOf(&full).Elem()
	for i := range v.NumField() {
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			f.SetString(fmt.Sprintf("field-%d", i))
		case reflect.Struct:
			f.Set(reflect.ValueOf(at))
		default:
			t.Fatalf("ActivityEntry.%s is a %s: give it a value here", v.Type().Field(i).Name, f.Kind())
		}
	}
	nt := &notifier{registry: session.NewRegistry(1)}
	if back := notificationEntry(nt.notification("s1", full, "", "")); !reflect.DeepEqual(back, full) {
		t.Fatalf("the entry from its notification\n%+v\nwant\n%+v", back, full)
	}
}

// Every attention change of ten server sessions reaches every sink that
// takes it, a thousand of them, each sink's in the order each session
// made them, typed as the Events page types them; nothing is dropped.
func TestSinkGetsEveryAttentionEvent(t *testing.T) {
	all := &recSink{name: "all"}
	states := &recSink{name: "states", events: []string{"needs_input", "working"}}
	asks := &recSink{name: "asks", events: []string{"needs_input"}}
	e := newNotifyEnv(t, nil, nil, all, states, asks)
	const sessions, changes = 10, 100
	ids := make([]string, sessions)
	locals := make([]*session.Local, sessions)
	for i := range ids {
		ids[i] = e.createSession("cat")
		d, _ := e.srv.registry.Get(ids[i])
		locals[i] = d.(*session.Local)
	}
	queues := []*extQueue[ext.Notification]{sinkOf(t, e.srv, "all").queue, sinkOf(t, e.srv, "states").queue, sinkOf(t, e.srv, "asks").queue}
	// A change waits while a sink's queue is half full: the test makes
	// changes as fast as the sessions take them, never faster than the
	// sinks are drained, so nothing is dropped whatever the machine's
	// load (the queues' drops are checked below).
	roomy := func() {
		for {
			full := false
			for _, q := range queues {
				if n, _ := q.stats(); n >= ext.SinkQueue/2 {
					full = true
				}
			}
			if !full {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	want := make([][]string, sessions)
	wantAsks := make([][]string, sessions)
	var wg sync.WaitGroup
	for i, l := range locals {
		for c := range changes {
			if c%2 == 0 {
				m := fmt.Sprintf("q %d.%d", i, c)
				want[i] = append(want[i], m)
				wantAsks[i] = append(wantAsks[i], m)
			} else {
				want[i] = append(want[i], fmt.Sprintf("w %d.%d", i, c))
			}
		}
		wg.Go(func() {
			for c, m := range want[i] {
				roomy()
				state := session.AttentionWorking
				if c%2 == 0 {
					state = session.AttentionNeedsInput
				}
				l.SetAttention(state, m, session.SourceAPI)
			}
		})
	}
	wg.Wait()
	// ours is what a sink was given of session i's changes, in order.
	ours := func(ns []ext.Notification, i int) []string {
		var out []string
		for _, n := range ns {
			if n.Session.ID == ids[i] && n.Type == session.ActivityAttention && (strings.HasPrefix(n.Message, "q ") || strings.HasPrefix(n.Message, "w ")) {
				if wantEv := map[byte]string{'q': "needs_input", 'w': "working"}[n.Message[0]]; n.Event != wantEv || n.State != wantEv {
					panic(fmt.Sprintf("%q typed %s, state %s", n.Message, n.Event, n.State))
				}
				out = append(out, n.Message)
			}
		}
		return out
	}
	for _, s := range []*recSink{all, states, asks} {
		expect := want
		if s == asks {
			expect = wantAsks
		}
		got := s.waitFor(t, "every change", func(ns []ext.Notification) bool {
			for i := range ids {
				if len(ours(ns, i)) < len(expect[i]) {
					return false
				}
			}
			return true
		})
		for i := range ids {
			if g := ours(got, i); !slices.Equal(g, expect[i]) {
				t.Fatalf("%s got session %d's changes\n%q\nwant\n%q", s.name, i, g, expect[i])
			}
		}
		if s != all {
			for _, n := range got {
				if !slices.Contains(s.events, n.Event) {
					t.Fatalf("%s, listing %v, was given %+v", s.name, s.events, n)
				}
			}
		}
	}
	for _, q := range queues {
		if _, dropped := q.stats(); dropped != 0 {
			t.Fatalf("%s dropped %d", q.name, dropped)
		}
	}
	// The sinks took the session's name and agent from the registry.
	if n := all.all()[0]; n.Session.AgentID != "cat" || n.Session.Name == "" {
		t.Fatalf("the session of a notification: %+v", n.Session)
	}
}

// A sink that never returns holds only its own queue: the hub, the
// sessions, the webhooks and the other sinks go on, and the blocked
// sink's queue keeps its newest 256, the drops counted.
func TestBlockedSinkNeverHoldsTheHub(t *testing.T) {
	stuck := &recSink{name: "stuck", block: make(chan struct{}), entered: make(chan struct{})}
	fast := &recSink{name: "fast"}
	rcv := newReceiver(t, nil)
	e := newNotifyEnv(t, nil, []config.Webhook{{URL: rcv.URL, Events: []string{"error", "needs_input"}, AllowPrivate: true}}, stuck, fast)
	// Let go before the server's cleanup waits for the sink's drain.
	t.Cleanup(func() { close(stuck.block) })
	id := e.createSession("cat")
	progress := func(m string) session.ActivityEntry {
		return session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityProgress, Message: m}
	}
	// Recorded off the test's goroutine, so a hub that waited would fail
	// the test, not hang it.
	go e.srv.events.activity(id, progress("0"), "")
	select {
	case <-stuck.entered:
	case <-time.After(testWait):
		t.Fatal("the stuck sink was never handed its first entry")
	}
	fastQ := sinkOf(t, e.srv, "fast").queue
	const more = 500
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= more; i++ {
			// Never faster than the fast sink drains, so it drops nothing.
			for n, _ := fastQ.stats(); n >= ext.SinkQueue/2; n, _ = fastQ.stats() {
				time.Sleep(time.Millisecond)
			}
			e.srv.events.activity(id, progress(fmt.Sprint(i)), "")
		}
	}()
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("the hub waited for the stuck sink")
	}
	if queued, dropped := sinkOf(t, e.srv, "stuck").queue.stats(); queued != ext.SinkQueue || dropped != more-ext.SinkQueue {
		t.Fatalf("the stuck sink holds %d, dropped %d; want %d and %d", queued, dropped, ext.SinkQueue, more-ext.SinkQueue)
	}
	var want []string
	for i := 0; i <= more; i++ {
		want = append(want, fmt.Sprint(i))
	}
	got := fast.waitFor(t, "every entry", func(ns []ext.Notification) bool { return len(messages(ns, session.ActivityProgress)) >= len(want) })
	if g := messages(got, session.ActivityProgress); !slices.Equal(g, want) {
		t.Fatalf("the fast sink got %v", g)
	}
	// The webhooks deliver.
	e.srv.events.activity(id, session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityError, Message: "boom"}, "")
	if d := rcv.next(t); d.message(t) != "boom" {
		t.Fatalf("the webhook got %s", d.body)
	}
	// And the session changes as ever: its change is shown, recorded and
	// delivered.
	d, _ := e.srv.registry.Get(id)
	changed := make(chan struct{})
	go func() {
		defer close(changed)
		d.(*session.Local).SetAttention(session.AttentionNeedsInput, "still here", session.SourceAPI)
	}()
	select {
	case <-changed:
	case <-time.After(testWait):
		t.Fatal("the session's change waited for the stuck sink")
	}
	if a := d.Info().Attention; a.State != session.AttentionNeedsInput || a.Message != "still here" {
		t.Fatalf("the session shows %+v", a)
	}
	if dd := rcv.next(t); dd.header.Get("X-Conductor-Event") != "needs_input" || dd.message(t) != "still here" {
		t.Fatalf("the webhook got %s %s", dd.header.Get("X-Conductor-Event"), dd.body)
	}
	fast.waitFor(t, "the change", func(ns []ext.Notification) bool {
		return slices.Contains(messages(ns, session.ActivityAttention), "still here")
	})
}

// A sink takes what its Events lists, the rule a webhook applies: nil
// takes every session entry and no notice; a list takes the entries whose
// event name or own type it names, and a provider's notice only when it
// names ext.TypeProvider. Events is read once. A notification queued
// before a sink's Events was read is decided as it is delivered.
func TestSinkEventsFilter(t *testing.T) {
	sinks := map[string]*heldSink{}
	var order []ext.NotificationSink
	for _, s := range []struct {
		name   string
		events []string
	}{
		{"every", nil},
		{"asks", []string{"needs_input"}},
		{"attention", []string{"attention"}},
		{"failures", []string{"exit_nonzero", "error"}},
		{"files", []string{"file", "join"}},
		{"nothing", []string{}},
		{"notices", []string{ext.TypeProvider}},
	} {
		h := &heldSink{recSink{name: s.name, events: s.events}}
		sinks[s.name] = h
		order = append(order, h)
	}
	e := newNotifyEnv(t, nil, nil, order...)
	// Read as a provider's start reads them; none is started, so each
	// queue holds what reaches it.
	for name := range sinks {
		sinkOf(t, e.srv, name).Events()
	}
	id := e.createSession("cat")
	// The entries the session made itself as it started are not the test's.
	for name := range sinks {
		drain(sinkOf(t, e.srv, name).queue)
	}
	now := time.Now().UTC()
	entries := []struct {
		e     session.ActivityEntry
		state session.AttentionState
	}{
		{session.ActivityEntry{At: now, Type: session.ActivityAttention, Message: "ask"}, session.AttentionNeedsInput},
		{session.ActivityEntry{At: now, Type: session.ActivityAttention, Message: "work"}, session.AttentionWorking},
		{session.ActivityEntry{At: now, Type: session.ActivityStatus, Message: "exited (exit 3)"}, ""},
		{session.ActivityEntry{At: now, Type: session.ActivityStatus, Message: "exited (exit 0)"}, ""},
		{session.ActivityEntry{At: now, Type: session.ActivityError, Message: "boom"}, ""},
		{session.ActivityEntry{At: now, Type: session.ActivityFile, Message: "f", Op: session.FileOpRead, Path: "a.go"}, ""},
		{session.ActivityEntry{At: now, Type: session.ActivityJoin, Message: "j", ByName: "Ada"}, ""},
		{session.ActivityEntry{At: now, Type: session.ActivityProgress, Message: "p"}, ""},
	}
	for _, x := range entries {
		e.srv.events.activity(id, x.e, x.state)
	}
	// A notice names an event, which no sink but those taking notices
	// counts it by.
	e.srv.deliverNotice(ext.Notification{Type: ext.TypeProvider, Origin: "pager", Event: "needs_input", Message: "notice", At: now})
	want := map[string][]string{
		"every":     {"ask", "work", "exited (exit 3)", "exited (exit 0)", "boom", "f", "j", "p"},
		"asks":      {"ask"},
		"attention": {"ask", "work"},
		"failures":  {"exited (exit 3)", "boom"},
		"files":     {"f", "j"},
		"nothing":   nil,
		"notices":   {"notice"},
	}
	for name, s := range sinks {
		got := drain(sinkOf(t, e.srv, name).queue)
		var ms []string
		for _, n := range got {
			ms = append(ms, n.Message)
		}
		if !slices.Equal(ms, want[name]) {
			t.Fatalf("%s (%v) was handed %q, want %q", name, s.events, ms, want[name])
		}
		if n := s.asked.Load(); n != 1 {
			t.Fatalf("%s's Events was asked %d times", name, n)
		}
		if name == "every" {
			ask := got[0]
			if ask.Event != "needs_input" || ask.State != "needs_input" || ask.Type != session.ActivityAttention || ask.Session.ID != id || ask.Session.AgentID != "cat" || !ask.At.Equal(now) {
				t.Fatalf("the notification %+v", ask)
			}
			if exit := got[2]; exit.Event != config.EventExitNonZero || exit.State != "" {
				t.Fatalf("the exit %+v", exit)
			}
			if p := got[7]; p.Event != session.ActivityProgress {
				t.Fatalf("the progress %+v", p)
			}
		}
	}

	// A sink whose Events has not been read is handed everything, and its
	// delivery decides: started (Events read before its Start), it is
	// delivered only what it takes, in order.
	late := &heldSink{recSink{name: "late", events: []string{"error"}}}
	e2 := newNotifyEnv(t, nil, nil, late)
	for _, m := range []string{"p1", "e1", "p2", "e2"} {
		typ := session.ActivityProgress
		if m[0] == 'e' {
			typ = session.ActivityError
		}
		e2.srv.events.activity("s1", session.ActivityEntry{At: now, Type: typ, Message: m}, "")
	}
	if n, _ := sinkOf(t, e2.srv, "late").queue.stats(); n != 4 {
		t.Fatalf("an unread sink holds %d, want all 4", n)
	}
	e2.srv.StartExtensions(t.Context())
	waitState(t, e2.srv, "late", extStarted)
	e2.srv.events.activity("s1", session.ActivityEntry{At: now, Type: session.ActivityError, Message: "e3"}, "")
	e2.srv.events.activity("s1", session.ActivityEntry{At: now, Type: session.ActivityProgress, Message: "p3"}, "")
	got := late.waitFor(t, "e3", func(ns []ext.Notification) bool { return len(ns) > 0 && ns[len(ns)-1].Message == "e3" })
	if ms := messages(got, session.ActivityError); len(got) != 3 || !slices.Equal(ms, []string{"e1", "e2", "e3"}) {
		t.Fatalf("the late sink was delivered %+v", got)
	}
	if n, _ := sinkOf(t, e2.srv, "late").queue.stats(); n != 0 {
		t.Fatalf("p3 was queued for a sink known to refuse it (%d waiting)", n)
	}
}

// noticeSink is a sink and a Starter that keeps what it is given and,
// with answer, answers every provider notice with one of its own through
// its Host, keeping what that answered.
type noticeSink struct {
	recSink
	answer bool
	host   atomic.Value

	answers chan error
}

func (s *noticeSink) Start(_ context.Context, h ext.Host) error {
	s.host.Store(h)
	return nil
}

func (s *noticeSink) Notify(ctx context.Context, n ext.Notification) error {
	_ = s.recSink.Notify(ctx, n)
	if s.answer && n.Type == ext.TypeProvider {
		s.answers <- s.host.Load().(ext.Host).Notify(ctx, ext.Notification{Message: "re: " + n.Message})
	}
	return nil
}

func newNoticeSink(name string, events []string, answer bool) *noticeSink {
	return &noticeSink{recSink: recSink{name: name, events: events}, answer: answer, answers: make(chan error, 16)}
}

// Notices never loop: two providers that each take provider notices and
// answer one with a notice of their own are both refused, so a notice is
// delivered to each once and nothing goes round; a sender and a taker:
// each notice delivered once, no echo, and the sender's sink, which takes
// every session entry, given no provider notice.
func TestProviderNoticesCannotLoop(t *testing.T) {
	t.Run("two takers", func(t *testing.T) {
		a := newNoticeSink("relay-a", []string{ext.TypeProvider}, true)
		b := newNoticeSink("relay-b", []string{ext.TypeProvider}, true)
		pager := &countingStarter{name: "pager"}
		p := ext.Providers{
			Notifications: []ext.NotificationSink{a, b},
			All: []ext.Entry{
				{Kind: ext.KindNotifications, Name: "relay-a", Value: a},
				{Kind: ext.KindNotifications, Name: "relay-b", Value: b},
				{Kind: ext.KindEntitlements, Name: "pager", Value: pager},
			},
		}
		e := newTestEnvAgents(t, nil, nil, nil, WithExtensions(local.Fill(p)))
		e.srv.StartExtensions(t.Context())
		for _, name := range []string{"relay-a", "relay-b", "pager"} {
			waitState(t, e.srv, name, extStarted)
		}
		for _, s := range []*noticeSink{a, b} {
			if err := s.host.Load().(ext.Host).Notify(t.Context(), ext.Notification{Message: "first"}); !errors.Is(err, ext.ErrUnsupported) {
				t.Fatalf("%s sent a notice: %v", s.name, err)
			}
		}
		send := pager.host.Load().(ext.Host)
		for _, m := range []string{"one", "two"} {
			if err := send.Notify(t.Context(), ext.Notification{Message: m}); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range []*noticeSink{a, b} {
			// "two" is last: what came before it is all there is.
			got := s.waitFor(t, "two", func(ns []ext.Notification) bool { return len(ns) > 0 && ns[len(ns)-1].Message == "two" })
			if ms := messages(got, ext.TypeProvider); len(got) != 2 || !slices.Equal(ms, []string{"one", "two"}) || got[0].Origin != "pager" {
				t.Fatalf("%s was given %+v", s.name, got)
			}
			for range 2 {
				if err := <-s.answers; !errors.Is(err, ext.ErrUnsupported) {
					t.Fatalf("%s's answer: %v", s.name, err)
				}
			}
		}
	})
	t.Run("a sender and a taker", func(t *testing.T) {
		sender := newNoticeSink("sender", nil, false)
		taker := newNoticeSink("taker", []string{ext.TypeProvider}, true)
		e := newNotifyEnv(t, nil, nil, sender, taker)
		e.srv.StartExtensions(t.Context())
		for _, name := range []string{"sender", "taker"} {
			waitState(t, e.srv, name, extStarted)
		}
		h := sender.host.Load().(ext.Host)
		if err := h.Notify(t.Context(), ext.Notification{Message: "hello"}); err != nil {
			t.Fatal(err)
		}
		if err := <-taker.answers; !errors.Is(err, ext.ErrUnsupported) {
			t.Fatalf("the taker's answer: %v", err)
		}
		// A session entry after the notice: the sender's sink, which takes
		// every entry, is handed it and was handed nothing before it.
		e.srv.events.activity("s1", session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityProgress, Message: "after"}, "")
		got := sender.waitFor(t, "the entry", func(ns []ext.Notification) bool { return len(ns) > 0 })
		if len(got) != 1 || got[0].Message != "after" || got[0].Type != session.ActivityProgress {
			t.Fatalf("the sender's sink was given %+v", got)
		}
		if err := h.Notify(t.Context(), ext.Notification{Message: "again"}); err != nil {
			t.Fatal(err)
		}
		got = taker.waitFor(t, "again", func(ns []ext.Notification) bool { return len(ns) > 0 && ns[len(ns)-1].Message == "again" })
		if ms := messages(got, ext.TypeProvider); len(got) != 2 || !slices.Equal(ms, []string{"hello", "again"}) || got[0].Origin != "sender" {
			t.Fatalf("the taker was given %+v", got)
		}
		if err := <-taker.answers; !errors.Is(err, ext.ErrUnsupported) {
			t.Fatalf("the taker's answer: %v", err)
		}
		if got := sender.all(); len(got) != 1 {
			t.Fatalf("the sender's sink was given %+v", got)
		}
	})
}

// panicEvents is a sink whose Events panics, counting its Notify calls.
type panicEvents struct {
	name     string
	notified atomic.Int32
}

func (s *panicEvents) Name() string     { return s.name }
func (s *panicEvents) Events() []string { panic("no events") }
func (s *panicEvents) Notify(context.Context, ext.Notification) error {
	s.notified.Add(1)
	return nil
}

// A sink's panic is recovered and logged: one in Notify loses only that
// notification, the sink delivered the next; one in Events leaves the
// sink taking nothing; the hub and the other sinks never see either.
func TestSinkPanicRecovered(t *testing.T) {
	var logs syncLog
	breaks := &recSink{name: "breaks", breaks: true}
	noEvents := &panicEvents{name: "no-events"}
	other := &recSink{name: "other"}
	e := newNotifyEnv(t, logTo(&logs), nil, breaks, noEvents, other)
	for _, m := range []string{"panic", "after"} {
		e.srv.events.activity("s1", session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityProgress, Message: m}, "")
	}
	got := breaks.waitFor(t, "after", func(ns []ext.Notification) bool { return len(ns) > 0 })
	if len(got) != 1 || got[0].Message != "after" {
		t.Fatalf("the sink that broke was given %+v", got)
	}
	got = other.waitFor(t, "both", func(ns []ext.Notification) bool { return len(ns) == 2 })
	if ms := messages(got, session.ActivityProgress); !slices.Equal(ms, []string{"panic", "after"}) {
		t.Fatalf("the other sink was given %v", ms)
	}
	g := sinkOf(t, e.srv, "no-events")
	end := time.Now().Add(testWait)
	for g.known.Load() == nil {
		if time.Now().After(end) {
			t.Fatal("the sink whose Events panics was never read")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Its filter takes nothing: no notification can reach its Notify.
	if f := g.known.Load(); f.all || len(f.names) != 0 || noEvents.notified.Load() != 0 {
		t.Fatalf("the sink whose Events panics takes %+v, notified %d", f, noEvents.notified.Load())
	}
	for _, want := range []string{"provider=breaks", "panic: the sink broke", "provider=no-events", "panic: no events"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("the log lacks %q:\n%s", want, logs.String())
		}
	}
}

// funcName is the name of the function f is.
func funcName(f any) string { return runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name() }

// The hub's sinks are in main's order: the notifier where the webhooks'
// sink was, then the run engine's OnActivity, and the webhooks (and the
// providers' sinks after them) are handed an entry before the engine is.
// A server with no webhook and no sink has the engine's alone, as main.
func TestSinksInMainsOrder(t *testing.T) {
	plain := newTestEnv(t, nil)
	engine := funcName(plain.srv.runs.OnActivity)
	if len(plain.srv.events.sinks) != 1 || funcName(plain.srv.events.sinks[0]) != engine {
		t.Fatalf("the free build's sinks: %d", len(plain.srv.events.sinks))
	}

	release := make(chan struct{})
	stalled := make(chan struct{})
	var once sync.Once
	rcv := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			close(stalled)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() { close(release) })
	held := &heldSink{recSink{name: "held"}}
	e := newNotifyEnv(t, nil, []config.Webhook{{URL: rcv.URL, Events: []string{"progress"}, AllowPrivate: true}}, held)
	e.srv.events.mu.Lock()
	sinks := e.srv.events.sinks
	if len(sinks) != 2 || funcName(sinks[0]) != funcName((&notifier{}).offer) || funcName(sinks[1]) != engine {
		e.srv.events.mu.Unlock()
		t.Fatalf("the hub's sinks are not the notifier, then the engine")
	}
	// The engine, as it is handed an entry, sees it queued already for
	// the webhook and the sink.
	type seen struct{ webhook, sink int }
	saw := make(chan seen, 4)
	hook, sink := e.srv.webhooks.hooks[0], sinkOf(t, e.srv, "held")
	runs := sinks[1]
	e.srv.events.sinks = []activitySink{sinks[0], func(id string, en session.ActivityEntry, st session.AttentionState) {
		if id == "probe" {
			w, _ := hook.stats()
			s, _ := sink.queue.stats()
			saw <- seen{w, s}
		}
		runs(id, en, st)
	}}
	e.srv.events.mu.Unlock()
	progress := func(m string) session.ActivityEntry {
		return session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityProgress, Message: m}
	}
	e.srv.events.activity("probe", progress("0"), "")
	<-saw
	select {
	case <-stalled:
	case <-time.After(testWait):
		t.Fatal("the webhook was never called")
	}
	// The webhook is busy with 0; 1 waits in its queue when the engine has it.
	e.srv.events.activity("probe", progress("1"), "")
	if s := <-saw; s.webhook != 1 || s.sink != 2 {
		t.Fatalf("as the engine was handed the entry, the webhook held %d and the sink %d: want 1 and 2", s.webhook, s.sink)
	}
}
