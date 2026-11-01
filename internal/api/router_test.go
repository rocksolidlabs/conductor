package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// The decision router (C5, router.go) over server sessions: the tests
// drive sessions over a process in memory and wait on the router's own
// steps (routerSeam) and the sessions' state, never on the clock.

// dbChoices are a question's two choices.
var dbChoices = []session.Option{{Label: "Postgres", Input: "Postgres\r"}, {Label: "SQLite", Input: "SQLite\r"}}

// fakeRouter records what it is told. route, when set, answers Route;
// gate, when set, holds the first Resolved until it is closed.
type fakeRouter struct {
	route func(ctx context.Context, d ext.Decision) (ext.Route, error)
	gate  chan struct{}

	mu       sync.Mutex
	routed   []ext.Decision
	resolved []ext.Resolution
	calls    int // Resolved calls begun
}

func (f *fakeRouter) Name() string { return "switchman" }

func (f *fakeRouter) Route(ctx context.Context, d ext.Decision) (ext.Route, error) {
	f.mu.Lock()
	f.routed = append(f.routed, d)
	route := f.route
	f.mu.Unlock()
	if route == nil {
		return ext.Route{}, nil
	}
	return route(ctx, d)
}

func (f *fakeRouter) Resolved(_ context.Context, r ext.Resolution) {
	f.mu.Lock()
	f.calls++
	first := f.calls == 1
	f.mu.Unlock()
	if first && f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	f.resolved = append(f.resolved, r)
	f.mu.Unlock()
}

// routedIDs is the ids of the questions Route was given, in order.
func (f *fakeRouter) routedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, d := range f.routed {
		out = append(out, d.ID)
	}
	return out
}

// resolvedIDs is the ids Resolved was told, in order.
func (f *fakeRouter) resolvedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.resolved {
		out = append(out, r.ID)
	}
	return out
}

// resolution is what Resolved was told of the question id.
func (f *fakeRouter) resolution(id string) (ext.Resolution, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var got ext.Resolution
	n := 0
	for _, r := range f.resolved {
		if r.ID == id {
			got, n = r, n+1
		}
	}
	return got, n
}

// count is how many times id is in ids.
func count(ids []string, id string) int {
	n := 0
	for _, x := range ids {
		if x == id {
			n++
		}
	}
	return n
}

// routerEnv is a server whose decision router is f.
func routerEnv(t *testing.T, f *fakeRouter) *testEnv {
	t.Helper()
	if f.gate != nil {
		// A gate the test left shut is opened as it ends, so the server's
		// goroutines end.
		t.Cleanup(func() {
			select {
			case <-f.gate:
			default:
				close(f.gate)
			}
		})
	}
	return newTestEnvAgents(t, nil, slog.New(slog.DiscardHandler), nil, WithExtensions(local.Fill(ext.Providers{
		Router: f,
		All:    []ext.Entry{{Kind: ext.KindDecisions, Name: "switchman", Value: f}},
	})))
}

// until waits for ok, polling the state the test needs, failing after
// testWait.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// seamLog keeps the router's steps (routerSeam) for the test's sessions;
// hold, when set, is asked at every step and may hold the router there.
type seamLog struct {
	mu    sync.Mutex
	steps []seamStep
	hold  func(step, sess string)
}

type seamStep struct {
	step, sess, question string
	rt                   ext.Route
	err                  error
}

// watchRouter installs a seamLog for the test.
func watchRouter(t *testing.T) *seamLog {
	t.Helper()
	l := &seamLog{}
	f := func(step, sess, question string, rt ext.Route, err error) {
		l.mu.Lock()
		l.steps = append(l.steps, seamStep{step, sess, question, rt, err})
		hold := l.hold
		l.mu.Unlock()
		if hold != nil {
			hold(step, sess)
		}
	}
	routerSeam.Store(&f)
	t.Cleanup(func() { routerSeam.Store(nil) })
	return l
}

// setHold sets what is asked at every step.
func (l *seamLog) setHold(h func(step, sess string)) {
	l.mu.Lock()
	l.hold = h
	l.mu.Unlock()
}

// count is how many times the step was taken for the session.
func (l *seamLog) count(step, sess string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.steps {
		if s.step == step && s.sess == sess {
			n++
		}
	}
	return n
}

// find is the first step for the question, false when none was taken.
func (l *seamLog) find(step, question string) (seamStep, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.steps {
		if s.step == step && s.question == question {
			return s, true
		}
	}
	return seamStep{}, false
}

// wait waits for the step for the question and answers it.
func (l *seamLog) wait(t *testing.T, step, question string) seamStep {
	t.Helper()
	var got seamStep
	until(t, step+" "+question, func() bool {
		s, ok := l.find(step, question)
		got = s
		return ok
	})
	return got
}

// settle marks the session changed and waits until the router has read it
// once since: whatever it held, it judged the session as it is now.
func (l *seamLog) settle(t *testing.T, e *testEnv, id string) {
	t.Helper()
	before := l.count("read", id)
	e.srv.router.mark(id)
	until(t, "the router read "+id+" again", func() bool { return l.count("done", id) > before })
}

// fakeSession is a server session over a process in memory, as the
// server's own are wired (its changes and activity to the server), and the
// writes the program got.
type fakeSession struct {
	l    *session.Local
	proc *sessiontest.FakeProc

	mu     sync.Mutex
	writes []string
}

// newFakeSession adds a server session id to e's registry, with o.
func newFakeSession(t *testing.T, e *testEnv, id string, o session.Options) *fakeSession {
	t.Helper()
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	o.ScrollbackBytes, o.InputWait, o.Log = 4096, testWait, slog.New(slog.DiscardHandler)
	o.OnChange, o.OnActivity = e.srv.localChange, e.srv.localActivity
	fs := &fakeSession{proc: proc}
	fs.l = session.NewLocal(session.Info{ID: id, Name: id, Cwd: e.root, Cols: 80, Rows: 24, Status: session.StatusRunning}, proc, o)
	go func() {
		for b := range proc.Input {
			fs.mu.Lock()
			fs.writes = append(fs.writes, string(b))
			fs.mu.Unlock()
		}
	}()
	if err := e.srv.registry.Add(fs.l); err != nil {
		t.Fatal(err)
	}
	e.srv.router.mark(id)
	return fs
}

// written is every write the program got.
func (fs *fakeSession) written() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return slices.Clone(fs.writes)
}

// ask shows the question words with choices, and answers its id. The
// session's own calls spend no token of the reports' bucket, so a test
// asks past its burst.
func (fs *fakeSession) ask(t *testing.T, words string, choices []session.Option) string {
	t.Helper()
	fs.l.SetAttentionFull(session.AttentionNeedsInput, words, session.SourceAPI, session.KindPrompt, choices)
	a := fs.l.Info().Attention
	if a.State != session.AttentionNeedsInput || a.ID == "" {
		t.Fatalf("asked %q: %+v", words, a)
	}
	return a.ID
}

// askOwn asks words as the agent's own question, and answers its id.
func (fs *fakeSession) askOwn(t *testing.T, words string, choices []session.Option) string {
	t.Helper()
	fs.l.Ask(words, session.SourceAPI, session.KindPrompt, choices)
	return fs.l.Info().Attention.ID
}

// report reports state as the agent's hooks do.
func (fs *fakeSession) report(t *testing.T, state session.AttentionState, words string) {
	t.Helper()
	kind := ""
	if state == session.AttentionDone {
		kind = session.KindDone
	}
	fs.l.SetAttentionFull(state, words, session.SourceAPI, kind, nil)
}

// owner attaches a controller named name.
func (fs *fakeSession) owner(t *testing.T, name string) *session.Subscription {
	t.Helper()
	sub, err := fs.l.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: name, Owner: true}, &gapSink{})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// answer answers the question id with keys as the person sub.
func (fs *fakeSession) answer(t *testing.T, sub *session.Subscription, id, keys string) {
	t.Helper()
	if err := fs.l.AnswerInput(context.Background(), sub, id, []byte(keys)); err != nil {
		t.Fatalf("answering %s: %v", id, err)
	}
}

// statuses is the session's status entries.
func (fs *fakeSession) statuses() []string {
	var out []string
	for _, a := range fs.l.Activity() {
		if a.Type == session.ActivityStatus {
			out = append(out, a.Message)
		}
	}
	return out
}

// codexQuestions is Codex's questions asked during the work, from the
// catalog, as the server gives a session them.
func codexQuestions(t *testing.T) []session.StartupQuestion {
	t.Helper()
	codex, ok := catalog.Default().Get("codex")
	if !ok {
		t.Fatal("no codex in the catalog")
	}
	var out []session.StartupQuestion
	for _, q := range codex.Questions {
		if !q.Anytime {
			continue
		}
		answers := make([]session.Option, len(q.Answers))
		for i, a := range q.Answers {
			answers[i] = session.Option{Label: a.Label, Input: a.Input}
		}
		out = append(out, session.StartupQuestion{Pattern: regexp.MustCompile(q.Prompt), Words: regexp.MustCompile(q.Words), Anytime: true, Answers: answers})
	}
	return out
}

// codexApproval is Codex's command approval as it draws it, and
// codexApprovalGone the screen moved on from it.
func codexApproval(cmd string) string {
	return "\x1b[?2026h\x1b[12;1H\x1b[J\x1b[13;3H\x1b[1mWould you like to run the following command?\x1b[22m" +
		"\x1b[15;3HEnvironment: local\x1b[17;3HReason: May I rerun the curl command with network access?" +
		"\x1b[19;3H\x1b[2m$\x1b[22m " + cmd +
		"\x1b[20;1H\x1b[36m› 1. Yes, proceed (\x1b[1my\x1b[22m)" +
		"\x1b[21;3H\x1b[39m2. Yes, and don't ask again for commands that start with" +
		"\x1b[22;6H`" + cmd + "` (\x1b[1mp\x1b[22m)" +
		"\x1b[23;3H3. No, and tell Codex what to do differently (\x1b[1mesc\x1b[22m)" +
		"\x1b[24;3H\x1b[2mPress enter to confirm or esc to cancel\x1b[22m\x1b[?2026l"
}

const codexApprovalGone = "\x1b[12;1H\x1b[J› "

// shownDialog waits for the dialog read off the screen and answers it.
func shownDialog(t *testing.T, fs *fakeSession) session.Attention {
	t.Helper()
	var a session.Attention
	until(t, "the dialog asked", func() bool {
		a = fs.l.Info().Attention
		return a.State == session.AttentionNeedsInput && a.Source == session.SourceQuestion && a.ID != ""
	})
	return a
}

// With no router registered (the free build), the people watching answer:
// the first answer wins, and the activity shows no route.
func TestNoRouterHumansAnswer(t *testing.T) {
	seams := watchRouter(t)
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, "free", session.Options{})
	nate, ada := fs.owner(t, "Nate"), fs.owner(t, "Ada")
	q := fs.ask(t, "Which database?", dbChoices)
	if rt := seams.wait(t, "routed", q).rt; rt.To != nil || rt.Answer != nil || rt.Rule != "" {
		t.Fatalf("the free default routed %+v", rt)
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Go(func() { errs[0] = fs.l.AnswerInput(context.Background(), nate, q, []byte("Postgres\r")) })
	wg.Go(func() { errs[1] = fs.l.AnswerInput(context.Background(), ada, q, []byte("SQLite\r")) })
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, session.ErrQuestionAnswered):
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("%d answers taken: %v", won, errs)
	}
	until(t, "the answer written", func() bool { return len(fs.written()) == 1 })
	seams.settle(t, e, "free")
	if got := fs.statuses(); len(got) != 0 {
		t.Fatalf("status entries %q", got)
	}
	if got := fs.written(); len(got) != 1 {
		t.Fatalf("written %q", got)
	}
}

// A route's answer is typed once: Route is told the question once however
// often the session changes while it waits, and the same answer again
// through Host.Submit is refused.
func TestRouteAppliedOnce(t *testing.T) {
	seams := watchRouter(t)
	gate := make(chan struct{})
	f := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		<-gate
		return ext.Route{Answer: &ext.Answer{Input: "Postgres\r", By: ada}}, nil
	}}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "once", session.Options{})
	q := fs.ask(t, "Which database?", dbChoices)
	until(t, "Route told", func() bool { return len(f.routedIDs()) == 1 })
	for range 50 {
		// The same question reported again, and changes besides.
		fs.l.SetAttentionFull(session.AttentionNeedsInput, "Which database?", session.SourceAPI, session.KindPrompt, dbChoices)
		e.srv.router.mark("once")
	}
	close(gate)
	if s := seams.wait(t, "applied", q); s.err != nil {
		t.Fatalf("applied: %v", s.err)
	}
	if got := fs.written(); !slices.Equal(got, []string{"Postgres\r"}) {
		t.Fatalf("written %q", got)
	}
	if a := fs.l.Info().LastAnswer; a == nil || a.ByName != "Ada" {
		t.Fatalf("the last answer: %+v", a)
	}
	h := testHost(e.srv, t.Context(), "switchman")
	if _, err := h.Submit(t.Context(), ext.SubmitRequest{Session: "once", Question: q, Input: "Postgres\r", By: ada}); !errors.Is(err, ext.ErrQuestionAnswered) {
		t.Fatalf("the same answer again: %v", err)
	}
	seams.settle(t, e, "once")
	if got := f.routedIDs(); count(got, q) != 1 {
		t.Fatalf("routed %q", got)
	}
	if got := fs.written(); len(got) != 1 {
		t.Fatalf("written %q", got)
	}
}

// A person's answer that comes before the route's wins: the route's is
// refused through the claim, nothing more written.
func TestPersonAnswersBeforeTheRoute(t *testing.T) {
	seams := watchRouter(t)
	gate := make(chan struct{})
	f := &fakeRouter{route: func(context.Context, ext.Decision) (ext.Route, error) {
		<-gate
		return ext.Route{Answer: &ext.Answer{Input: "SQLite\r", By: ada}}, nil
	}}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "person", session.Options{})
	nate := fs.owner(t, "Nate")
	q := fs.ask(t, "Which database?", dbChoices)
	until(t, "Route told", func() bool { return len(f.routedIDs()) == 1 })
	fs.answer(t, nate, q, "Postgres\r")
	close(gate)
	if s := seams.wait(t, "applied", q); !errors.Is(s.err, ext.ErrQuestionAnswered) {
		t.Fatalf("the route's answer: %v", s.err)
	}
	if got := fs.written(); !slices.Equal(got, []string{"Postgres\r"}) {
		t.Fatalf("written %q", got)
	}
	if a := fs.l.Info().LastAnswer; a == nil || a.ByName != "Nate" {
		t.Fatalf("the last answer: %+v", a)
	}
}

// A route with destinations is one status entry in the session's activity;
// the empty route records nothing.
func TestRouteRecorded(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		switch d.Message {
		case "Which database?":
			return ext.Route{To: []ext.Destination{{Kind: "person", Name: "ada"}, {Kind: "role", Name: "db"}}, Rule: "owner of db"}, nil
		case "Which colour?":
			return ext.Route{To: []ext.Destination{{Kind: "crew", Name: "design"}}}, nil
		}
		return ext.Route{}, nil
	}}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "recorded", session.Options{})
	seams.wait(t, "routed", fs.ask(t, "Which database?", dbChoices))
	seams.wait(t, "routed", fs.ask(t, "Which size?", nil))
	seams.wait(t, "routed", fs.ask(t, "Which colour?", nil))
	want := []string{"routed: person ada, role db (owner of db)", "routed: crew design"}
	until(t, "the entries", func() bool { return len(fs.statuses()) >= len(want) })
	if got := fs.statuses(); !slices.Equal(got, want) {
		t.Fatalf("status entries %q, want %q", got, want)
	}
}

// A route the guard refuses is the empty route: late, panicking, failing,
// an answer whose input is none of the question's choices, or one given by
// no valid principal. Nothing is typed and nothing recorded.
func TestRouteBounds(t *testing.T) {
	for _, c := range []struct {
		name  string
		route func(ctx context.Context, d ext.Decision) (ext.Route, error)
	}{
		{"late", func(ctx context.Context, _ ext.Decision) (ext.Route, error) {
			<-ctx.Done()
			return ext.Route{To: []ext.Destination{{Kind: "person", Name: "ada"}}, Answer: &ext.Answer{Input: "Postgres\r", By: ada}}, nil
		}},
		{"panicking", func(context.Context, ext.Decision) (ext.Route, error) { panic("route") }},
		{"failing", func(context.Context, ext.Decision) (ext.Route, error) {
			return ext.Route{Answer: &ext.Answer{Input: "Postgres\r", By: ada}}, errors.New("down")
		}},
		{"no choice", func(context.Context, ext.Decision) (ext.Route, error) {
			return ext.Route{To: []ext.Destination{{Kind: "person", Name: "ada"}}, Answer: &ext.Answer{Input: "rm -rf /\r", By: ada}}, nil
		}},
		{"no principal", func(context.Context, ext.Decision) (ext.Route, error) {
			return ext.Route{Answer: &ext.Answer{Input: "Postgres\r"}}, nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			seams := watchRouter(t)
			f := &fakeRouter{route: c.route}
			e := routerEnv(t, f)
			// A late call is cut at the router's own wait, not RouteWait's.
			e.srv.router.wait = 50 * time.Millisecond
			fs := newFakeSession(t, e, "bounds", session.Options{})
			q := fs.ask(t, "Which database?", dbChoices)
			if rt := seams.wait(t, "routed", q).rt; rt.To != nil || rt.Answer != nil {
				t.Fatalf("routed %+v", rt)
			}
			if _, applied := seams.find("applied", q); applied {
				t.Fatal("an answer was applied")
			}
			seams.settle(t, e, "bounds")
			if got := fs.written(); len(got) != 0 {
				t.Fatalf("written %q", got)
			}
			if got := fs.statuses(); len(got) != 0 {
				t.Fatalf("status entries %q", got)
			}
			if a := fs.l.Info().Attention; a.ID != q {
				t.Fatalf("the question went: %+v", a)
			}
		})
	}
}

// A routed question is told Resolved once, however it stops waiting:
// answered, withdrawn, its session ended or gone from the registry.
func TestResolvedOnce(t *testing.T) {
	for _, c := range []struct {
		name  string
		ended bool
		end   func(t *testing.T, e *testEnv, fs *fakeSession, q string)
	}{
		{"answered", false, func(t *testing.T, _ *testEnv, fs *fakeSession, q string) {
			fs.answer(t, fs.owner(t, "Nate"), q, "Postgres\r")
		}},
		{"withdrawn", false, func(t *testing.T, _ *testEnv, fs *fakeSession, _ string) {
			fs.report(t, session.AttentionWorking, "")
		}},
		{"ended", true, func(t *testing.T, _ *testEnv, fs *fakeSession, _ string) {
			fs.proc.End(0)
		}},
		{"removed", true, func(t *testing.T, e *testEnv, fs *fakeSession, _ string) {
			e.srv.registry.Remove("resolved")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			seams := watchRouter(t)
			f := &fakeRouter{}
			e := routerEnv(t, f)
			fs := newFakeSession(t, e, "resolved", session.Options{})
			q := fs.ask(t, "Which database?", dbChoices)
			seams.wait(t, "routed", q)
			if _, n := f.resolution(q); n != 0 {
				t.Fatal("resolved while it waits")
			}
			c.end(t, e, fs, q)
			until(t, "Resolved", func() bool { _, n := f.resolution(q); return n == 1 })
			seams.settle(t, e, "resolved")
			r, n := f.resolution(q)
			if n != 1 || r.Ended != c.ended || r.Session.ID != "resolved" || r.At.IsZero() {
				t.Fatalf("told %d times, %+v", n, r)
			}
		})
	}
}

// The agent's own question A set aside by work, a bell's wait B over it,
// A shown again at the turn's done: one Route each, B resolved at the
// done, A once, at its answer.
func TestRouteOncePerQuestionSetAside(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "aside", session.Options{})
	a := fs.askOwn(t, "Which database?", dbChoices)
	seams.wait(t, "routed", a)
	fs.report(t, session.AttentionWorking, "")
	if err := fs.proc.Print("\a"); err != nil {
		t.Fatal(err)
	}
	var b string
	until(t, "the bell's wait", func() bool {
		att := fs.l.Info().Attention
		b = att.ID
		return att.State == session.AttentionNeedsInput && att.Source == session.SourceBell && b != ""
	})
	seams.wait(t, "routed", b)
	fs.report(t, session.AttentionDone, "done for now")
	if got := fs.l.Info().Attention; got.ID != a {
		t.Fatalf("at the done: %+v, want the question again", got)
	}
	until(t, "B resolved", func() bool { _, n := f.resolution(b); return n == 1 })
	seams.settle(t, e, "aside")
	if _, n := f.resolution(a); n != 0 {
		t.Fatal("A resolved before its answer")
	}
	fs.answer(t, fs.owner(t, "Nate"), a, "Postgres\r")
	until(t, "A resolved", func() bool { _, n := f.resolution(a); return n == 1 })
	seams.settle(t, e, "aside")
	if got := f.routedIDs(); !slices.Equal(got, []string{a, b}) {
		t.Fatalf("routed %q, want %q", got, []string{a, b})
	}
	if got := f.resolvedIDs(); !slices.Equal(got, []string{b, a}) {
		t.Fatalf("resolved %q, want %q", got, []string{b, a})
	}
}

// A question the screen withdraws, recording nothing, the terminal quiet
// after: exactly one Resolved, from the attention change alone.
func TestResolvedOnWithdrawalWithNoActivity(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "withdrawn", session.Options{Questions: codexQuestions(t)})
	if err := fs.proc.Print(codexApproval("curl -sI https://example.com")); err != nil {
		t.Fatal(err)
	}
	q := shownDialog(t, fs).ID
	seams.wait(t, "routed", q)
	entries := len(fs.l.Activity())
	if err := fs.proc.Print(codexApprovalGone); err != nil {
		t.Fatal(err)
	}
	until(t, "the dialog withdrawn", func() bool { return fs.l.Info().Attention.ID != q })
	until(t, "Resolved", func() bool { _, n := f.resolution(q); return n == 1 })
	seams.settle(t, e, "withdrawn")
	if _, n := f.resolution(q); n != 1 {
		t.Fatalf("told %d times", n)
	}
	if got := fs.l.Activity(); len(got) != entries {
		t.Fatalf("the withdrawal recorded %+v", got[entries:])
	}
}

// A routed question, the router's 16 workers held in Route by other
// sessions, changes from 1,000 more sessions, then the question withdrawn
// and its session quiet: once the gate opens, exactly one Resolved.
func TestResolvedAfterABurstOfOtherSessions(t *testing.T) {
	seams := watchRouter(t)
	gate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	var held atomic.Int32
	f := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		if strings.HasPrefix(d.Session.ID, "busy-") {
			held.Add(1)
			<-gate
		}
		return ext.Route{}, nil
	}}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "target", session.Options{})
	q := fs.ask(t, "Which database?", dbChoices)
	seams.wait(t, "routed", q)
	seams.settle(t, e, "target")
	for i := range routerWorkers {
		busy := newFakeSession(t, e, fmt.Sprintf("busy-%d", i), session.Options{})
		busy.ask(t, "Which size?", nil)
	}
	until(t, "every worker held in Route", func() bool { return held.Load() == routerWorkers })
	for i := range 1000 {
		e.srv.router.mark(fmt.Sprintf("other-%d", i))
	}
	fs.report(t, session.AttentionWorking, "")
	if _, n := f.resolution(q); n != 0 {
		t.Fatal("resolved while every worker is held")
	}
	close(gate)
	until(t, "Resolved", func() bool { _, n := f.resolution(q); return n == 1 })
	seams.settle(t, e, "target")
	if _, n := f.resolution(q); n != 1 {
		t.Fatalf("told %d times", n)
	}
}

// A set aside, 70 questions routed and answered before A shows again at
// the done: A is not routed again (the router forgets the oldest answered,
// never one the session keeps), and is resolved once, at its answer.
func TestRouteOncePerQuestionSetAsidePastTheBound(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "bound", session.Options{})
	nate := fs.owner(t, "Nate")
	a := fs.askOwn(t, "Which database?", dbChoices)
	seams.wait(t, "routed", a)
	fs.report(t, session.AttentionWorking, "")
	for i := range 70 {
		q := fs.ask(t, fmt.Sprintf("Question %d?", i), dbChoices)
		seams.wait(t, "routed", q)
		fs.answer(t, nate, q, "SQLite\r")
		until(t, "resolved", func() bool { _, n := f.resolution(q); return n == 1 })
	}
	seams.settle(t, e, "bound")
	e.srv.router.mu.Lock()
	kept := len(e.srv.router.sessions["bound"].order)
	e.srv.router.mu.Unlock()
	if kept > routedKept {
		t.Fatalf("the router keeps %d questions of the session, past %d", kept, routedKept)
	}
	fs.report(t, session.AttentionDone, "done for now")
	if got := fs.l.Info().Attention; got.ID != a {
		t.Fatalf("at the done: %+v, want A again", got)
	}
	seams.settle(t, e, "bound")
	if n := count(f.routedIDs(), a); n != 1 {
		t.Fatalf("A routed %d times", n)
	}
	if _, n := f.resolution(a); n != 0 {
		t.Fatal("A resolved before its answer")
	}
	fs.answer(t, nate, a, "Postgres\r")
	until(t, "A resolved", func() bool { _, n := f.resolution(a); return n == 1 })
	seams.settle(t, e, "bound")
	if _, n := f.resolution(a); n != 1 {
		t.Fatalf("A told %d times", n)
	}
}

// A router whose Resolved blocks at its first call, then 300 routed
// questions answered: once it lets go, every one is told once, in order.
func TestResolvedThroughABlockedConsumer(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{gate: make(chan struct{})}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "blocked", session.Options{})
	nate := fs.owner(t, "Nate")
	var asked []string
	for i := range 300 {
		q := fs.ask(t, fmt.Sprintf("Question %d?", i), dbChoices)
		seams.wait(t, "routed", q)
		fs.answer(t, nate, q, "SQLite\r")
		asked = append(asked, q)
	}
	seams.settle(t, e, "blocked")
	until(t, "the first Resolved held", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.calls == 1
	})
	close(f.gate)
	until(t, "every one told", func() bool { return len(f.resolvedIDs()) >= len(asked) })
	seams.settle(t, e, "blocked")
	if got := f.resolvedIDs(); !slices.Equal(got, asked) {
		t.Fatalf("told %d, want the %d in order", len(got), len(asked))
	}
}

// With the resolutions held shut and the bound of routed and untold
// questions reached, the next question is not routed, never told, and not
// routed after the gate opens; nothing past the bound is held.
func TestRouteSkippedAtTheBound(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{gate: make(chan struct{})}
	e := routerEnv(t, f)
	e.srv.router.bound = 3
	fs := newFakeSession(t, e, "skipped", session.Options{})
	nate := fs.owner(t, "Nate")
	var routed []string
	for i := range 3 {
		q := fs.ask(t, fmt.Sprintf("Question %d?", i), dbChoices)
		seams.wait(t, "routed", q)
		fs.answer(t, nate, q, "SQLite\r")
		routed = append(routed, q)
	}
	seams.settle(t, e, "skipped")
	skipped := fs.ask(t, "Past the bound?", dbChoices)
	seams.settle(t, e, "skipped")
	if got := f.routedIDs(); !slices.Equal(got, routed) {
		t.Fatalf("routed %q", got)
	}
	e.srv.router.mu.Lock()
	untold, queued := e.srv.router.untold, len(e.srv.router.told)
	e.srv.router.mu.Unlock()
	if untold > 3 || queued > 3 {
		t.Fatalf("%d untold, %d queued: past the bound", untold, queued)
	}
	close(f.gate)
	until(t, "the three told", func() bool { return len(f.resolvedIDs()) == 3 })
	seams.settle(t, e, "skipped")
	if got := f.routedIDs(); slices.Contains(got, skipped) {
		t.Fatalf("routed after the gate opened: %q", got)
	}
	fs.answer(t, nate, skipped, "Postgres\r")
	seams.settle(t, e, "skipped")
	if got := f.resolvedIDs(); !slices.Equal(got, routed) {
		t.Fatalf("told %q, want %q", got, routed)
	}
}

// A set aside while B shows, the router held just after its read of the
// session while A is shown again at the done: it judges from the snapshot
// it read, where A is kept, so A is not resolved, and is resolved once, at
// its answer.
func TestRouterNeverResolvesARestoredQuestion(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "restored", session.Options{})
	a := fs.askOwn(t, "Which database?", dbChoices)
	seams.wait(t, "routed", a)
	fs.report(t, session.AttentionWorking, "")
	if err := fs.proc.Print("\a"); err != nil {
		t.Fatal(err)
	}
	var b string
	until(t, "the bell's wait", func() bool {
		att := fs.l.Info().Attention
		b = att.ID
		return att.Source == session.SourceBell && b != ""
	})
	seams.wait(t, "routed", b)
	seams.settle(t, e, "restored")

	atJudge, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	seams.setHold(func(step, sess string) {
		if step == "judge" && sess == "restored" {
			once.Do(func() {
				close(atJudge)
				<-release
			})
		}
	})
	e.srv.router.mark("restored")
	<-atJudge
	fs.report(t, session.AttentionDone, "done for now")
	if got := fs.l.Info().Attention; got.ID != a {
		t.Fatalf("at the done: %+v", got)
	}
	close(release)
	seams.settle(t, e, "restored")
	seams.settle(t, e, "restored")
	if _, n := f.resolution(a); n != 0 {
		t.Fatal("A resolved while the session shows it")
	}
	fs.answer(t, fs.owner(t, "Nate"), a, "Postgres\r")
	until(t, "A resolved", func() bool { _, n := f.resolution(a); return n == 1 })
	seams.settle(t, e, "restored")
	if _, n := f.resolution(a); n != 1 {
		t.Fatalf("A told %d times", n)
	}
}

// A routed question answered by keys typed in the terminal, the dialog
// still drawn and the program quiet after: Resolved once, at the keys.
func TestResolvedWhenAnsweredInTheTerminal(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	fs := newFakeSession(t, e, "terminal", session.Options{Questions: codexQuestions(t)})
	if err := fs.proc.Print(codexApproval("curl -sI https://example.com")); err != nil {
		t.Fatal(err)
	}
	q := shownDialog(t, fs).ID
	seams.wait(t, "routed", q)
	sub := fs.owner(t, "Nate")
	if err := fs.l.Input(sub, []byte("y")); err != nil {
		t.Fatal(err)
	}
	until(t, "answered in the terminal", func() bool {
		a := fs.l.Info().Attention
		return a.ID == q && a.Answered == session.AnsweredTerminal
	})
	until(t, "Resolved", func() bool { _, n := f.resolution(q); return n == 1 })
	seams.settle(t, e, "terminal")
	if _, n := f.resolution(q); n != 1 {
		t.Fatalf("told %d times", n)
	}
	if a := fs.l.Info().Attention; a.ID != q {
		t.Fatalf("the dialog went: %+v", a)
	}
}

// A question keys typed in the terminal reached before the router first
// read it is never routed, and never told.
func TestAnsweredInTheTerminalNeverRouted(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	atRead, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	seams.setHold(func(step, sess string) {
		if step == "read" && sess == "consumed" {
			once.Do(func() {
				close(atRead)
				<-release
			})
		}
	})
	fs := newFakeSession(t, e, "consumed", session.Options{Questions: codexQuestions(t)})
	<-atRead
	if err := fs.proc.Print(codexApproval("curl -sI https://example.com")); err != nil {
		t.Fatal(err)
	}
	q := shownDialog(t, fs).ID
	if err := fs.l.Input(fs.owner(t, "Nate"), []byte("y")); err != nil {
		t.Fatal(err)
	}
	until(t, "answered in the terminal", func() bool { return fs.l.Info().Attention.Answered == session.AnsweredTerminal })
	close(release)
	seams.settle(t, e, "consumed")
	if err := fs.proc.Print(codexApprovalGone); err != nil {
		t.Fatal(err)
	}
	until(t, "the dialog gone", func() bool { return fs.l.Info().Attention.ID != q })
	seams.settle(t, e, "consumed")
	if got := f.routedIDs(); len(got) != 0 {
		t.Fatalf("routed %q", got)
	}
	if got := f.resolvedIDs(); len(got) != 0 {
		t.Fatalf("told %q", got)
	}
}
