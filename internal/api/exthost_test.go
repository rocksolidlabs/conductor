package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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

// testHost is the ext.Host a try of the provider name's Start would get,
// for as long as ctx lasts.
func testHost(srv *Server, ctx context.Context, name string) *extHost {
	return &extHost{s: srv, name: name, ctx: ctx, mayNotify: true}
}

// ada is who a test's provider acts for.
var ada = ext.Principal{Subject: "u-ada", Name: "Ada"}

// fakeClock is the server's clock seam for the providers' starts: every
// wait asked for is handed to the test (reqs), which fires it.
type fakeClock struct{ reqs chan fakeTimer }

type fakeTimer struct {
	d    time.Duration
	fire chan time.Time
}

func newFakeClock(srv *Server) *fakeClock {
	c := &fakeClock{reqs: make(chan fakeTimer, 64)}
	srv.extAfter = func(d time.Duration) <-chan time.Time {
		tm := fakeTimer{d: d, fire: make(chan time.Time, 1)}
		c.reqs <- tm
		return tm.fire
	}
	return c
}

// next is the next wait the server asks for, which must be of d.
func (c *fakeClock) next(t *testing.T, d time.Duration) fakeTimer {
	t.Helper()
	select {
	case tm := <-c.reqs:
		if tm.d != d {
			t.Fatalf("the server waits %s, want %s", tm.d, d)
		}
		return tm
	case <-time.After(testWait):
		t.Fatalf("the server asked for no wait of %s within %s", d, testWait)
	}
	return fakeTimer{}
}

// none checks the server has asked for no wait since the last.
func (c *fakeClock) none(t *testing.T) {
	t.Helper()
	select {
	case tm := <-c.reqs:
		t.Fatalf("the server waits %s, want no wait", tm.d)
	default:
	}
}

// testWait bounds what a test waits for a step the server takes at once:
// a hang, never a timing.
const testWait = 20 * time.Second

// waitState waits until the provider name is in state, which holds once
// reached.
func waitState(t *testing.T, srv *Server, name, state string) {
	t.Helper()
	end := time.Now().Add(testWait)
	for srv.extState(name) != state {
		if time.Now().After(end) {
			t.Fatalf("%s is %s, want %s", name, srv.extState(name), state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A provider launches a saved crew as the route does: the same run (its
// crew, its members, its label), noted in its log as launched by whom
// through which provider, and answered with its page's URL; a crew that
// is not there, a bad request, a switchyard, a server with no data
// directory and a given-up try's Host are refused, each as ext names it.
func TestHostLaunch(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	id := e.sendCrew("POST", "/api/crews", map[string]any{
		"name": "Nightly", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none",
		"members": []any{catMember("core", "immediately"), catMember("tests", "manual")},
	}, http.StatusCreated)["id"].(string)
	h := testHost(e.srv, t.Context(), "sched")

	ref, err := h.Launch(t.Context(), ext.LaunchRequest{Crew: id, Label: "  nightly  ", By: ada})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Crew != id || ref.ID == "" || ref.URL != "http://example.test/runs/"+ref.ID {
		t.Fatalf("launch answered %+v", ref)
	}
	run, ok := e.srv.runs.Get(ref.ID)
	if !ok {
		t.Fatalf("no run %s", ref.ID)
	}
	noted := false
	for _, l := range run.Log {
		noted = noted || l.Type == session.ActivityStatus && l.Message == "launched by Ada through sched"
	}
	if !noted {
		t.Fatalf("the run's log does not say who launched it: %+v", run.Log)
	}
	// The route's run of the same crew: the same shape.
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, map[string]any{"label": "nightly"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("route launch: %d %v", resp.StatusCode, out)
	}
	viaRoute, _ := e.srv.runs.Get(out["run"].(map[string]any)["id"].(string))
	if viaRoute.CrewID != run.CrewID || viaRoute.Label != run.Label || run.Label != "nightly" || len(viaRoute.Members) != len(run.Members) {
		t.Fatalf("host's run %+v, route's %+v", run, viaRoute)
	}
	for i, m := range run.Members {
		if r := viaRoute.Members[i]; m.Name != r.Name || (m.SessionID == "") != (r.SessionID == "") {
			t.Fatalf("member %d: host's %+v, route's %+v", i, m, r)
		}
	}

	for _, tc := range []struct {
		r    ext.LaunchRequest
		want error
		says string
	}{
		{ext.LaunchRequest{Crew: "nope", By: ada}, ext.ErrNotFound, "no such crew"},
		{ext.LaunchRequest{Crew: "Bad!", By: ada}, ext.ErrRefused, "not a crew id"},
		{ext.LaunchRequest{Crew: id, Label: strings.Repeat("x", 61), By: ada}, ext.ErrRefused, "label"},
		{ext.LaunchRequest{Crew: id}, ext.ErrRefused, "by:"},
	} {
		if _, err := h.Launch(t.Context(), tc.r); !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.says) {
			t.Fatalf("launch %+v: %v, want %v saying %q", tc.r, err, tc.want, tc.says)
		}
	}

	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	if _, err := testHost(yard.srv, t.Context(), "sched").Launch(t.Context(), ext.LaunchRequest{Crew: id, By: ada}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a switchyard launched: %v", err)
	}
	bare := newExtServer(t, nil)
	if _, err := testHost(bare, t.Context(), "sched").Launch(t.Context(), ext.LaunchRequest{Crew: id, By: ada}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a server with no data directory launched: %v", err)
	}
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := testHost(e.srv, ended, "sched").Launch(t.Context(), ext.LaunchRequest{Crew: id, By: ada}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a given-up try's host launched: %v", err)
	}
}

// A managed crew launches through the host as through the route: its run
// is managed and its first task's attempt runs; with crews.managed off it
// is refused with the route's words.
func TestHostLaunchManaged(t *testing.T) {
	body := func(root string) map[string]any {
		return map[string]any{"name": "Gated", "goal": "g", "cwd": root, "where": "server", "isolation": "worktree",
			"members": []any{map[string]any{"name": "lead", "agentId": "ready-cat", "prompt": "", "start": map[string]any{"when": "task"}}},
			"tasks":   []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."}}}
	}
	on := readyEnv(t, nil)
	on.gitRepo(t)
	id := on.sendCrew("POST", "/api/crews", body(on.root), http.StatusCreated)["id"].(string)
	ref, err := testHost(on.srv, t.Context(), "sched").Launch(t.Context(), ext.LaunchRequest{Crew: id, By: ada})
	if err != nil {
		t.Fatal(err)
	}
	if run, ok := on.srv.runs.Get(ref.ID); !ok || run.Managed == nil {
		t.Fatalf("not managed: %+v", run)
	}
	runningAttempt(t, on, ref.ID, "build", 1)
	if resp, out := on.do("POST", "/api/runs/"+ref.ID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}

	off := readyEnv(t, func(c *config.Config) { no := false; c.Crews.Managed = &no })
	off.gitRepo(t)
	id = off.sendCrew("POST", "/api/crews", body(off.root), http.StatusCreated)["id"].(string)
	if _, err := testHost(off.srv, t.Context(), "sched").Launch(t.Context(), ext.LaunchRequest{Crew: id, By: ada}); !errors.Is(err, ext.ErrRefused) || !strings.Contains(err.Error(), "crews.managed") {
		t.Fatalf("launch with managed off: %v", err)
	}
}

// inputs is the session's input entries' texts.
func inputs(l *session.Local) []string {
	var out []string
	for _, a := range l.Activity() {
		if a.Type == session.ActivityInput {
			out = append(out, a.Message+" by "+a.ByName)
		}
	}
	return out
}

// A provider's line is typed into a server session, recorded as by its
// principal, and never into a question the session shows; a session that
// is not there, a hosted session and a choice's keys are refused as ext
// names them.
func TestHostSubmitLine(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	l := e.local(id)
	h := testHost(e.srv, t.Context(), "sched")

	res, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Text: "hello", By: ada})
	if err != nil || res.Status != "entered" {
		t.Fatalf("submit: %+v %v", res, err)
	}
	if got := inputs(l); len(got) != 1 || got[0] != "hello by Ada" {
		t.Fatalf("inputs %q", got)
	}

	l.SetAttentionFull(session.AttentionNeedsInput, "Allow Bash?", session.SourceAPI, session.KindPermission, []session.Option{{Label: "Yes", Input: "1"}, {Label: "No", Input: "3"}})
	if _, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Text: "rm -rf", By: ada}); !errors.Is(err, ext.ErrRefused) || !strings.Contains(err.Error(), "asks a question") {
		t.Fatalf("a line into a question: %v", err)
	}
	if got := inputs(l); len(got) != 1 {
		t.Fatalf("a line was typed into the question: %q", got)
	}
	if a := l.Info().Attention; a.State != session.AttentionNeedsInput {
		t.Fatalf("the question went: %+v", a)
	}
	q := l.Info().Attention.ID
	if _, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Question: q, Input: "1", By: ada}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a choice's keys: %v", err)
	}
	if _, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Input: "1", By: ada}); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("keys answering no question: %v", err)
	}
	if _, err := h.Submit(t.Context(), ext.SubmitRequest{Session: "nope", Text: "x", By: ada}); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("no such session: %v", err)
	}

	_, hosted := dialRawHost(t, e)
	if _, err := h.Submit(t.Context(), ext.SubmitRequest{Session: hosted, Text: "x", By: ada}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a hosted session: %v", err)
	}
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := testHost(e.srv, ended, "sched").Submit(t.Context(), ext.SubmitRequest{Session: id, Text: "x", By: ada}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a given-up try's host typed: %v", err)
	}
}

// A provider's answer to a question goes through the question's claim, as
// a viewer's does: it is entered once, the question resolved; the same
// answer again, one to a question a viewer answered first, and the loser
// of two at once are refused ext.ErrQuestionAnswered.
func TestHostSubmitAnswer(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	l := e.local(id)
	h := testHost(e.srv, t.Context(), "switchman")
	ask := func(msg string) string {
		l.SetAttentionFull(session.AttentionNeedsInput, msg, session.SourceAPI, session.KindPrompt, nil)
		return l.Info().Attention.ID
	}

	q := ask("Which colour?")
	res, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Question: q, Text: "blue", By: ada})
	if err != nil || res.Status != "entered" {
		t.Fatalf("answer: %+v %v", res, err)
	}
	if a := l.Info().Attention; a.State == session.AttentionNeedsInput && a.ID == q {
		t.Fatalf("the question still waits: %+v", a)
	}
	if _, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Question: q, Text: "blue", By: ada}); !errors.Is(err, ext.ErrQuestionAnswered) {
		t.Fatalf("the same answer again: %v", err)
	}

	// A person answers first.
	q = ask("Which size?")
	if _, err := l.Submit(t.Context(), session.Submission{Text: "large", ByName: "Nate", Question: q}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Question: q, Text: "small", By: ada}); !errors.Is(err, ext.ErrQuestionAnswered) {
		t.Fatalf("after a person's answer: %v", err)
	}

	// Two at once: one is entered, the other refused.
	q = ask("Which day?")
	var entered, lost atomic.Int32
	var wg sync.WaitGroup
	for _, text := range []string{"monday", "tuesday"} {
		wg.Go(func() {
			_, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Question: q, Text: text, By: ada})
			switch {
			case err == nil:
				entered.Add(1)
			case errors.Is(err, ext.ErrQuestionAnswered):
				lost.Add(1)
			default:
				t.Errorf("answer %s: %v", text, err)
			}
		})
	}
	wg.Wait()
	if entered.Load() != 1 || lost.Load() != 1 {
		t.Fatalf("%d entered, %d refused: want one of each", entered.Load(), lost.Load())
	}
	answered := 0
	for _, in := range inputs(l) {
		if in == "monday by Ada" || in == "tuesday by Ada" {
			answered++
		}
	}
	if answered != 1 {
		t.Fatalf("the question was answered %d times: %q", answered, inputs(l))
	}
}

// A provider's directory is <dataDir>/ext/<name>, 0700, the same at each
// call; no data directory, or a given-up try, refuses.
func TestHostDataDir(t *testing.T) {
	e := newTestEnv(t, nil)
	h := testHost(e.srv, t.Context(), "sched")
	dir, err := h.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(e.srv.cfg.DataDir, "ext", "sched"); dir != want {
		t.Fatalf("data dir %s, want %s", dir, want)
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, fi, err)
		}
	}
	if again, err := h.DataDir(); err != nil || again != dir {
		t.Fatalf("again: %s %v", again, err)
	}
	if _, err := testHost(newExtServer(t, nil), t.Context(), "sched").DataDir(); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("no data directory: %v", err)
	}
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := testHost(e.srv, ended, "sched").DataDir(); !errors.Is(err, context.Canceled) {
		t.Fatalf("a given-up try's host: %v", err)
	}
}

// A provider's notice is checked; a provider one of whose sinks takes
// provider notices may not send one.
func TestHostNotify(t *testing.T) {
	takes := &takingSink{fakeSink: fakeSink{name: "relay", got: make(chan ext.Notification, 1)}}
	sends := &countingStarter{name: "pager"}
	p := ext.Providers{
		Notifications: []ext.NotificationSink{takes},
		All: []ext.Entry{
			{Kind: ext.KindNotifications, Name: "relay", Value: takes},
			{Kind: ext.KindEntitlements, Name: "pager", Value: sends},
		},
	}
	srv := newExtServer(t, nil, WithExtensions(local.Fill(p)))
	srv.StartExtensions(t.Context())
	hosts := map[string]ext.Host{}
	for _, name := range []string{"relay", "pager"} {
		waitState(t, srv, name, extStarted)
	}
	hosts["relay"], hosts["pager"] = takes.host.Load().(ext.Host), sends.host.Load().(ext.Host)
	if err := hosts["relay"].Notify(t.Context(), ext.Notification{Message: "hi"}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a provider that takes notices sent one: %v", err)
	}
	if err := hosts["pager"].Notify(t.Context(), ext.Notification{Message: "build green", URL: "https://example.test/run"}); err != nil {
		t.Fatalf("a notice: %v", err)
	}
	for _, n := range []ext.Notification{{}, {Message: "two\nlines"}, {Message: strings.Repeat("x", ext.MaxNoticeMessage+1)}, {Message: "x", URL: strings.Repeat("u", ext.MaxNoticeURL+1)}} {
		if err := hosts["pager"].Notify(t.Context(), n); !errors.Is(err, ext.ErrRefused) {
			t.Fatalf("notice %q: %v", n.Message, err)
		}
	}
}

// takingSink is a sink that takes provider notices, and a Starter.
type takingSink struct {
	fakeSink
	host atomic.Value
}

func (*takingSink) Events() []string { return []string{ext.TypeProvider} }
func (s *takingSink) Start(_ context.Context, h ext.Host) error {
	s.host.Store(h)
	return nil
}

// countingStarter is an entitlement source that is a Starter, counting
// its Starts.
type countingStarter struct {
	name   string
	starts atomic.Int32
	host   atomic.Value
}

func (c *countingStarter) Name() string { return c.name }
func (c *countingStarter) Entitled(context.Context, string) (ext.Entitlement, error) {
	return ext.Entitlement{Allowed: true}, nil
}
func (c *countingStarter) Start(_ context.Context, h ext.Host) error {
	c.starts.Add(1)
	c.host.Store(h)
	return nil
}

// both is one value in two slots, identity and media, and a Starter.
type both struct{ starts atomic.Int32 }

func (*both) Name() string { return "cloud" }
func (*both) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}
func (*both) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) { return nil, nil }
func (*both) Room(context.Context, ext.RoomRequest) (ext.RoomJoin, bool, error) {
	return ext.RoomJoin{}, false, nil
}
func (b *both) Start(context.Context, ext.Host) error {
	b.starts.Add(1)
	return nil
}

// One value in two slots is started once; a provider registered only as
// the entitlement source is started too; a second StartExtensions starts
// nothing.
func TestStartOncePerValue(t *testing.T) {
	b := &both{}
	plans := &countingStarter{name: "plans"}
	p := ext.Providers{
		Identity: b, Media: b,
		All: []ext.Entry{
			{Kind: ext.KindIdentity, Name: "cloud", Value: b},
			{Kind: ext.KindEntitlements, Name: "plans", Value: plans},
			{Kind: ext.KindMedia, Name: "cloud", Value: b},
		},
	}
	srv := newExtServer(t, nil, WithExtensions(local.Fill(p)))
	if len(srv.extStarts) != 2 {
		t.Fatalf("%d providers to start, want 2", len(srv.extStarts))
	}
	srv.StartExtensions(t.Context())
	srv.StartExtensions(t.Context())
	waitState(t, srv, "cloud", extStarted)
	waitState(t, srv, "plans", extStarted)
	// Every supervisor has returned once its provider started.
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	srv.WaitBackground(ctx)
	if b.starts.Load() != 1 || plans.starts.Load() != 1 {
		t.Fatalf("cloud started %d times, plans %d: want once each", b.starts.Load(), plans.starts.Load())
	}
}

// flaky is an identity provider and a Starter whose Start does what the
// test's step says: it hands its context and Host over (began), then
// errs, panics, waits for its context, or succeeds.
type flaky struct {
	began chan startCall
	step  atomic.Int32
	asked atomic.Int32
}

type startCall struct {
	ctx context.Context
	h   ext.Host
}

func (*flaky) Name() string { return "cloud" }
func (f *flaky) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	f.asked.Add(1)
	return ext.Principal{Subject: "u-ada", Name: "Ada", Grants: []ext.Grant{ext.GrantAdmin}}, true, nil
}
func (f *flaky) Start(ctx context.Context, h ext.Host) error {
	f.began <- startCall{ctx, h}
	switch f.step.Add(1) {
	case 1:
		return errors.New("bad gateway")
	case 2:
		panic("the provider broke")
	case 3:
		<-ctx.Done()
		return nil // late: ignored
	}
	return nil
}

// A Start that errs, panics or is late is tried again with backoff, 1 s
// doubling, driven by the clock seam; the given-up try's context is
// cancelled and its Host refuses before the next begins; meanwhile the
// provider's slot answers the free default without asking it; once a try
// succeeds, the slot is the provider's from its next call, in the same
// server; and the server's end ends the try that succeeded.
func TestStartRetriedUntilItSucceeds(t *testing.T) {
	f := &flaky{began: make(chan startCall, 1)}
	p := ext.Providers{Identity: f, All: []ext.Entry{{Kind: ext.KindIdentity, Name: "cloud", Value: f}}}
	var logs syncLog
	srv := newExtServer(t, logTo(&logs), WithExtensions(local.Fill(p)))
	clock := newFakeClock(srv)
	// Each failure a minute after the last, so each is logged.
	var minutes atomic.Int64
	srv.extClock = func() time.Time { return time.Unix(0, 0).Add(time.Duration(minutes.Add(1)) * time.Minute) }
	srv.StartExtensions(t.Context())

	stillDefault := func(try int) {
		t.Helper()
		if _, ok, err := srv.exts.identity.Authenticate(t.Context(), "tok"); ok || err != nil {
			t.Fatalf("try %d: the slot answered the provider's: %v %v", try, ok, err)
		}
		if n := f.asked.Load(); n != 0 {
			t.Fatalf("try %d: the provider was asked %d times before it started", try, n)
		}
		if st := srv.extState("cloud"); st != extStarting {
			t.Fatalf("try %d: %s, want starting", try, st)
		}
	}
	revoked := func(try int, c startCall) {
		t.Helper()
		if c.ctx.Err() == nil {
			t.Fatalf("try %d: its context was not cancelled", try)
		}
		if _, err := c.h.DataDir(); !errors.Is(err, context.Canceled) {
			t.Fatalf("try %d: its host still acts: %v", try, err)
		}
	}
	backoff := extStartFirst
	var last startCall
	for try := 1; try <= 3; try++ {
		wait := clock.next(t, ext.StartWait)
		c := <-f.began
		if try > 1 {
			revoked(try-1, last)
		}
		stillDefault(try)
		if try == 3 {
			wait.fire <- time.Now() // past StartWait: given up
			// Its log's first minute is asked for as the try is waited
			// for; the try returns before it passes.
			clock.next(t, extLogEvery)
		}
		next := clock.next(t, backoff)
		revoked(try, c)
		stillDefault(try)
		last = c
		next.fire <- time.Now()
		backoff *= 2
	}
	clock.next(t, ext.StartWait)
	won := <-f.began
	revoked(3, last)
	waitState(t, srv, "cloud", extStarted)
	if pr, ok, err := srv.exts.identity.Authenticate(t.Context(), "tok"); !ok || err != nil || pr.Subject != "u-ada" {
		t.Fatalf("the started provider's slot answered %+v %v %v", pr, ok, err)
	}
	if won.ctx.Err() != nil {
		t.Fatal("the try that succeeded has its context ended")
	}
	if _, err := won.h.DataDir(); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("the started provider's host: %v", err)
	}
	for _, why := range []string{"failure=error", "failure=panic", "failure=late"} {
		if !strings.Contains(logs.String(), why) {
			t.Fatalf("no %s logged:\n%s", why, logs.String())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	srv.WaitBackground(ctx)
	revoked(4, won)
}

// A Starter's queues hold what reaches them, and its other slots answer
// the free default, until its Start has succeeded; then the queue drains
// and the slots are its own.
func TestStartQueuesHeldUntilStarted(t *testing.T) {
	open := make(chan struct{})
	g := &gatedProvider{
		fakeSink: fakeSink{name: "push", got: make(chan ext.Notification, 1)},
		open:     open,
	}
	p := ext.Providers{
		Notifications: []ext.NotificationSink{g},
		Context:       []ext.ContextProvider{g},
		All: []ext.Entry{
			{Kind: ext.KindContext, Name: "push", Value: g},
			{Kind: ext.KindNotifications, Name: "push", Value: g},
		},
	}
	began := make(chan string, 4)
	f := func(name string) { began <- name }
	extBegan.Store(&f)
	t.Cleanup(func() { extBegan.Store(nil) })
	srv := newExtServer(t, nil, WithExtensions(local.Fill(p)))
	srv.StartExtensions(t.Context())
	sink := srv.exts.sinks[0].(*guardSink)
	sink.Notify(t.Context(), ext.Notification{Message: "early"})
	if bs, err := srv.exts.context[0].Context(t.Context(), ext.ContextQuery{}); bs != nil || err != nil || g.asked.Load() != 0 {
		t.Fatalf("context before start: %v %v, asked %d", bs, err, g.asked.Load())
	}
	if n, _ := sink.queue.stats(); n != 1 {
		t.Fatalf("%d queued before start, want 1", n)
	}
	select {
	case name := <-began:
		t.Fatalf("%s's queue drains before its Start", name)
	default:
	}
	close(open)
	for range 2 {
		select {
		case name := <-began:
			if name != "push" {
				t.Fatalf("%s began", name)
			}
		case <-time.After(testWait):
			t.Fatal("the queues did not begin once Start succeeded")
		}
	}
	select {
	case n := <-g.got:
		if n.Message != "early" {
			t.Fatalf("delivered %+v", n)
		}
	case <-time.After(testWait):
		t.Fatal("the held notice was not delivered")
	}
	if bs, _ := srv.exts.context[0].Context(t.Context(), ext.ContextQuery{}); len(bs) != 1 || g.asked.Load() != 1 {
		t.Fatalf("context once started: %v, asked %d", bs, g.asked.Load())
	}
}

// gatedProvider is a sink and a context provider whose Start waits for
// open.
type gatedProvider struct {
	fakeSink
	open  chan struct{}
	asked atomic.Int32
}

func (g *gatedProvider) Start(ctx context.Context, _ ext.Host) error {
	select {
	case <-g.open:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*gatedProvider) NoteTouch(context.Context, ext.TouchedFile) {}
func (g *gatedProvider) Context(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) {
	g.asked.Add(1)
	return []ext.ContextBlock{{Title: "Owners", Body: "Ada owns this."}}, nil
}

// stuck is a Starter whose first Start ignores its context and returns
// only once gate opens; it counts its Starts and how many run at once.
type stuck struct {
	gate      chan struct{}
	began     chan startCall
	calls     atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
}

func (*stuck) Name() string { return "stuck" }
func (*stuck) Entitled(context.Context, string) (ext.Entitlement, error) {
	return ext.Entitlement{Allowed: true}, nil
}
func (s *stuck) Start(ctx context.Context, h ext.Host) error {
	n := s.active.Add(1)
	for m := s.maxActive.Load(); n > m && !s.maxActive.CompareAndSwap(m, n); m = s.maxActive.Load() {
	}
	defer s.active.Add(-1)
	// Counted before it is announced: a test that has heard of this Start
	// reads it in calls.
	n = s.calls.Add(1)
	s.began <- startCall{ctx, h}
	if n == 1 {
		<-s.gate
	}
	return nil
}

// stuckGoroutines counts the goroutines inside a stuck's Start.
func stuckGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte("internal/api.(*stuck).Start("))
		}
		buf = make([]byte, 2*len(buf))
	}
}

// A Start gated shut that ignores its context is given up past StartWait
// (its context cancelled, its Host refusing, the provider starting, logged
// late once a minute), and no second Start begins while it has not
// returned, however long: the server asks for no backoff until it has;
// once it returns, the next try begins after the backoff, and never two at
// once.
func TestStartNeverOverlaps(t *testing.T) {
	s := &stuck{gate: make(chan struct{}), began: make(chan startCall, 2)}
	p := ext.Providers{All: []ext.Entry{{Kind: ext.KindEntitlements, Name: "stuck", Value: s}}}
	abandoned := make(chan string, 1)
	f := func(name string) {
		if name == "stuck" {
			abandoned <- name
		}
	}
	extAbandoned.Store(&f)
	t.Cleanup(func() { extAbandoned.Store(nil) })
	var logs syncLog
	srv := newExtServer(t, logTo(&logs), WithExtensions(local.Fill(p)))
	clock := newFakeClock(srv)
	// Each log a minute after the last, so each is written.
	var minutes atomic.Int64
	srv.extClock = func() time.Time { return time.Unix(0, 0).Add(time.Duration(minutes.Add(1)) * time.Minute) }
	srv.StartExtensions(t.Context())

	wait := clock.next(t, ext.StartWait)
	first := <-s.began
	wait.fire <- time.Now()
	select {
	case <-abandoned:
	case <-time.After(testWait):
		t.Fatal("the late try was not given up")
	}
	if first.ctx.Err() == nil {
		t.Fatal("the given-up try's context was not cancelled")
	}
	if err := first.h.Notify(t.Context(), ext.Notification{Message: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("the given-up try's host: %v", err)
	}
	// The supervisor waits for the try to return, told it is given up
	// (abandoned), asking only for the minutes of its log: it has asked
	// for no backoff, so no clock driven however far can begin a second
	// Start. Each minute is logged; the next is asked for once it is.
	for range 2 {
		clock.next(t, extLogEvery).fire <- time.Now()
	}
	clock.next(t, extLogEvery)
	clock.none(t)
	if n := strings.Count(logs.String(), "failure=late"); n != 3 {
		t.Fatalf("logged late %d times, want 3 (given up, then two minutes):\n%s", n, logs.String())
	}
	if st := srv.extState("stuck"); st != extStarting {
		t.Fatalf("%s, want starting", st)
	}
	if n, g := s.calls.Load(), stuckGoroutines(); n != 1 || g != 1 {
		t.Fatalf("%d Starts, %d goroutines in Start: want one of each", n, g)
	}

	close(s.gate)
	next := clock.next(t, extStartFirst)
	if n, a := s.calls.Load(), s.active.Load(); n != 1 || a != 0 {
		t.Fatalf("before the backoff: %d Starts, %d running", n, a)
	}
	next.fire <- time.Now()
	clock.next(t, ext.StartWait)
	<-s.began
	waitState(t, srv, "stuck", extStarted)
	if m := s.maxActive.Load(); m != 1 {
		t.Fatalf("%d Starts ran at once", m)
	}
	if n := s.calls.Load(); n != 2 {
		t.Fatalf("%d Starts, want 2", n)
	}
}

// slowEvents is a sink that takes provider notices, and a Starter, whose
// Events answers only once open is closed.
type slowEvents struct {
	fakeSink
	open chan struct{}
	host atomic.Value
}

func (s *slowEvents) Events() []string {
	<-s.open
	return []string{ext.TypeProvider}
}
func (s *slowEvents) Start(_ context.Context, h ext.Host) error {
	s.host.Store(h)
	return nil
}

// StartExtensions asks no provider anything: two sinks whose Events have
// not answered hold up neither it, nor so the listeners, nor each other;
// each is read on its own provider's supervisor before that provider's
// first Start, and what it answers still decides that the provider may
// not send notices.
func TestStartNeverWaitsOnEvents(t *testing.T) {
	open := make(chan struct{})
	a := &slowEvents{fakeSink: fakeSink{name: "relay-a", got: make(chan ext.Notification, 1)}, open: open}
	b := &slowEvents{fakeSink: fakeSink{name: "relay-b", got: make(chan ext.Notification, 1)}, open: open}
	p := ext.Providers{
		Notifications: []ext.NotificationSink{a, b},
		All: []ext.Entry{
			{Kind: ext.KindNotifications, Name: "relay-a", Value: a},
			{Kind: ext.KindNotifications, Name: "relay-b", Value: b},
		},
	}
	srv := newExtServer(t, nil, WithExtensions(local.Fill(p)))
	// Returned with neither Events answered: open is still shut.
	srv.StartExtensions(t.Context())
	for _, name := range []string{"relay-a", "relay-b"} {
		if st := srv.extState(name); st != extStarting {
			t.Fatalf("%s is %s before its Events answered, want starting", name, st)
		}
	}
	if a.host.Load() != nil || b.host.Load() != nil {
		t.Fatal("a provider was started before its Events answered")
	}
	close(open)
	for _, s := range []*slowEvents{a, b} {
		waitState(t, srv, s.name, extStarted)
		if err := s.host.Load().(ext.Host).Notify(t.Context(), ext.Notification{Message: "hi"}); !errors.Is(err, ext.ErrUnsupported) {
			t.Fatalf("%s takes provider notices and sent one: %v", s.name, err)
		}
	}
}

// goroutinesIn counts the goroutines whose stack holds frame.
func goroutinesIn(frame string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte(frame))
		}
		buf = make([]byte, 2*len(buf))
	}
}

// waitIn waits until a goroutine's stack holds frame: the call under test
// has reached it.
func waitIn(t *testing.T, frame string) {
	t.Helper()
	end := time.Now().Add(testWait)
	for goroutinesIn(frame) == 0 {
		if time.Now().After(end) {
			t.Fatalf("no goroutine reached %s within %s", frame, testWait)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A Host call already under way when its try is given up ends then,
// before it acts, whatever the caller's context: a line waiting for the
// session's turn answers context.Canceled and is never typed, and a
// launch waiting on an agent's lookup answers context.Canceled and starts
// no run.
func TestHostCallsEndWithTheirTry(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)

	// Nate's line holds the session's turn at its Enter.
	id := e.createSession("cat")
	l := e.local(id)
	atEnter, gate := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := l.Submit(context.Background(), session.Submission{Text: "first", ByName: "Nate", BeforeEnter: func() {
			close(atEnter)
			<-gate
		}})
		first <- err
	}()
	select {
	case <-atEnter:
	case <-time.After(testWait):
		t.Fatal("the first line never reached its Enter")
	}
	try, giveUp := context.WithCancel(t.Context())
	late := make(chan error, 1)
	go func() {
		_, err := testHost(e.srv, try, "sched").Submit(context.Background(), ext.SubmitRequest{Session: id, Text: "late", By: ada})
		late <- err
	}()
	waitIn(t, "session.(*Local).awaitTurn(")
	giveUp()
	select {
	case err := <-late:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a given-up try's waiting line: %v, want context.Canceled", err)
		}
	case <-time.After(testWait):
		t.Fatal("a given-up try's line still waits for the session's turn")
	}
	close(gate)
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(testWait):
		t.Fatal("the first line was not entered")
	}
	if got := inputs(l); len(got) != 1 || got[0] != "first by Nate" {
		t.Fatalf("inputs %q, want Nate's alone", got)
	}

	// A launch waits on its member's lookup.
	crewID := e.sendCrew("POST", "/api/crews", map[string]any{
		"name": "Nightly", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none",
		"members": []any{catMember("core", "immediately")},
	}, http.StatusCreated)["id"].(string)
	looking, answer := make(chan string, 8), make(chan struct{})
	t.Cleanup(func() { close(answer) })
	e.srv.lookups.stubWait(hangWait)
	e.srv.lookups.stub(func(program string) (string, error) {
		looking <- program
		<-answer
		return program, nil
	})
	try, giveUp = context.WithCancel(t.Context())
	launched := make(chan error, 1)
	go func() {
		_, err := testHost(e.srv, try, "sched").Launch(context.Background(), ext.LaunchRequest{Crew: crewID, By: ada})
		launched <- err
	}()
	select {
	case <-looking:
	case <-time.After(testWait):
		t.Fatal("the launch looked up no member's program")
	}
	giveUp()
	select {
	case err := <-launched:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a given-up try's waiting launch: %v, want context.Canceled", err)
		}
	case <-time.After(testWait):
		t.Fatal("a given-up try's launch still waits on its lookup")
	}
	for _, r := range e.srv.runs.List() {
		if r.CrewID == crewID {
			t.Fatalf("a given-up try's launch started run %s", r.ID)
		}
	}
}

// logTo is a logger writing text lines to w.
func logTo(w *syncLog) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }
