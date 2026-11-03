package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// Sessions start through the Runtime (docs/plans/runtime.md, PR-3): the
// server's native one for every session but an attempt of a managed run
// its offered one took, the offered one for those.

// withNativeRuntime has the server start natively through rt, a test's
// stand-in for local.Native.
func withNativeRuntime(rt ext.Runtime) Option {
	return func(o *options) { o.native = rt }
}

// withRuntime registers rt as the extensions' Runtime, named as it names
// itself.
func withRuntime(rt ext.Runtime) Option {
	return WithExtensions(local.Fill(ext.Providers{Runtime: rt, All: []ext.Entry{{Kind: ext.KindRuntime, Name: rt.Name(), Value: rt}}}))
}

// nativeSeam is a FakeRuntime that starts natively, the server's native
// Runtime in its place.
func nativeSeam() *sessiontest.FakeRuntime {
	return &sessiontest.FakeRuntime{Place: "seam", Base: local.Native()}
}

// runtimeEnv is readyEnv with the server's native Runtime the seam given,
// logging to log (nil discards), made with opts.
func runtimeEnv(t *testing.T, seam *sessiontest.FakeRuntime, log *slog.Logger, opts ...Option) *testEnv {
	t.Helper()
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return newTestEnvAgents(t, nil, log, []catalog.Agent{{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}},
		append([]Option{withNativeRuntime(seam)}, opts...)...)
}

// oneTaskCrew is a managed crew of one task, build, its member a ready cat.
func oneTaskCrew(id, root string, checks ...crew.Check) crew.Crew {
	return crew.Crew{ID: id, Name: id, Goal: "build it", Cwd: root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}},
		Tasks:   []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build it.", Checks: checks}}}
}

// launchOne launches c through the server's engine, stopped when the test
// ends, and answers the run's id.
func launchOne(t *testing.T, e *testEnv, c crew.Crew) string {
	t.Helper()
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	return run.ID
}

// attemptRef is the ProcessRef of attempt n of the run's build task's
// session.
func attemptRef(sessionID, runID string, n int) ext.ProcessRef {
	return ext.ProcessRef{Session: sessionID, Run: runID, Task: "build", Attempt: n, Tree: ledger.TreeWorktree}
}

// takesAnswered gives the guard's calls the test's minute (extWaiting) in
// place of ext.TakesWait, for a test that needs the offered Runtime's
// answer to Takes: the answer comes from a goroutine of the guard's, and a
// loaded machine that schedules it late must not turn a run taken into one
// declined. TestATakesThatPanicsOrStallsRunsNatively's late run keeps the
// product's bound, the one test of it.
func takesAnswered(t *testing.T) {
	t.Helper()
	wait := time.Minute
	extWaiting.Store(&wait)
	t.Cleanup(func() { extWaiting.Store(nil) })
}

// terminalRefs is the refs of the terminals f was asked to start.
func terminalRefs(f *sessiontest.FakeRuntime) []ext.ProcessRef {
	var out []ext.ProcessRef
	for _, c := range f.Asked("StartTerminal") {
		out = append(out, c.Ref)
	}
	return out
}

// methodsOf is the methods of the calls f was made, in order.
func methodsOf(f *sessiontest.FakeRuntime) []string {
	var out []string
	for _, c := range f.Calls() {
		out = append(out, c.Method)
	}
	return out
}

// A person's session starts through the server's native Runtime: the argv,
// the directory and the folder opened and checked, the environment
// Conductor built (its own names, none of the server's CONDUCTOR_), the
// size, and the session as the ref; the session runs on what it started.
func TestASessionStartsThroughTheNativeRuntime(t *testing.T) {
	t.Setenv("CONDUCTOR_SERVERS_OWN", "leaked")
	seam := nativeSeam()
	e := runtimeEnv(t, seam, nil)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cols": 100, "rows": 30})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	t.Cleanup(func() { e.stopAndWait(id) })
	calls := seam.Asked("StartTerminal")
	if len(calls) != 1 {
		t.Fatalf("%d terminals started", len(calls))
	}
	s := calls[0].Spec.(ext.TerminalSpec)
	if !slices.Equal(s.Argv, []string{"/bin/cat"}) || s.Dir != e.root || s.DirFile == nil || s.Cols != 100 || s.Rows != 30 || calls[0].Ref != (ext.ProcessRef{Session: id}) {
		t.Fatalf("the terminal's spec: argv %q dir %q folder %v %dx%d ref %+v", s.Argv, s.Dir, s.DirFile != nil, s.Cols, s.Rows, calls[0].Ref)
	}
	for _, want := range []string{"CONDUCTOR_SESSION_ID=" + id, "CONDUCTOR_NOTIFY_TOKEN=", "CONDUCTOR_NOTIFY_URL=", "CONDUCTOR_AGENT=cat"} {
		if !slices.ContainsFunc(s.Env, func(kv string) bool { return strings.HasPrefix(kv, want) }) {
			t.Fatalf("the environment has no %s: %q", want, s.Env)
		}
	}
	if slices.ContainsFunc(s.Env, func(kv string) bool { return strings.HasPrefix(kv, "CONDUCTOR_SERVERS_OWN=") }) {
		t.Fatalf("the server's own CONDUCTOR_ name reached the session: %q", s.Env)
	}
	if calls[0].Process.PID <= 0 {
		t.Fatalf("the native start reported no process: %+v", calls[0].Process)
	}
	if _, out := e.do("GET", "/api/sessions/"+id, adminToken, nil); out["session"].(map[string]any)["status"] != "running" {
		t.Fatalf("the session: %v", out["session"])
	}
}

// A registered Runtime is offered managed runs alone: a person's session,
// its resume and a legacy crew's member start natively, and the offered
// one is asked nothing.
func TestAPersonsSessionIsNotOffered(t *testing.T) {
	seam := nativeSeam()
	offered := &sessiontest.FakeRuntime{Base: local.Native()}
	e := runtimeEnv(t, seam, nil, withRuntime(offered))
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "exit"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	first := out["id"].(string)
	e.stopAndWait(first)
	resp, out = e.do("POST", "/api/sessions/"+first+"/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume: %d %v", resp.StatusCode, out)
	}
	resumed := out["session"].(map[string]any)["id"].(string)
	e.stopAndWait(resumed)
	e.gitRepo(t)
	legacy := crew.Crew{ID: "legacy", Name: "Legacy", Goal: "g", Cwd: e.root, Where: crew.WhereServer, Isolation: crew.IsolationNone,
		Members: []crew.Member{{Name: "lead", AgentID: "cat", Start: crew.Start{When: crew.StartImmediately}}}}
	runID := launchOne(t, e, legacy)
	deadline := time.Now().Add(20 * time.Second)
	for len(seam.Asked("StartTerminal")) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the legacy member did not start: %+v", terminalRefs(seam))
		}
		time.Sleep(10 * time.Millisecond)
	}
	refs := terminalRefs(seam)
	if refs[0] != (ext.ProcessRef{Session: first}) || refs[1] != (ext.ProcessRef{Session: resumed}) || refs[2].Run != runID || refs[2].Session == "" || refs[2].Task != "" || refs[2].Tree != "" {
		t.Fatalf("the native starts: %+v", refs)
	}
	if got := methodsOf(offered); len(got) != 0 {
		t.Fatalf("the offered Runtime was asked %v", got)
	}
}

// An attempt of a run the offered Runtime took starts in it, named by its
// attempt; of a run it declined, natively, the offered one asked Takes
// alone.
func TestAnAttemptStartsInItsRunsRuntime(t *testing.T) {
	for _, takes := range []bool{true, false} {
		name := map[bool]string{true: "taken", false: "declined"}[takes]
		t.Run(name, func(t *testing.T) {
			takesAnswered(t)
			seam := nativeSeam()
			offered := &sessiontest.FakeRuntime{Base: local.Native(), TakesRun: func(ext.ProcessRef) bool { return takes }}
			e := runtimeEnv(t, seam, nil, withRuntime(offered))
			e.gitRepo(t)
			runID := launchOne(t, e, oneTaskCrew("one", e.root))
			sid := runningAttempt(t, e, runID, "build", 1)["sessionId"].(string)
			want := []ext.ProcessRef{attemptRef(sid, runID, 1)}
			in, other := offered, seam
			if !takes {
				in, other = seam, offered
			}
			if got := terminalRefs(in); !slices.Equal(got, want) {
				t.Fatalf("the attempt's session started in %+v, want %+v", got, want)
			}
			if got := terminalRefs(other); len(got) != 0 {
				t.Fatalf("the other Runtime started %+v", got)
			}
			if c := offered.Asked("Takes"); len(c) != 1 || c[0].Ref != (ext.ProcessRef{Run: runID}) {
				t.Fatalf("Takes: %+v", c)
			}
		})
	}
}

// A start the Runtime refuses is 502 start_failed, nothing registered, and
// no session is made; a launch naming a Runtime the server has not is
// refused the same way before anything starts.
func TestARefusedStartIsStartFailed(t *testing.T) {
	seam := &sessiontest.FakeRuntime{Place: "seam", Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
		return nil, errors.New("the seam refuses")
	}}
	e := runtimeEnv(t, seam, nil)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat"})
	if resp.StatusCode != http.StatusBadGateway || errCodeOf(out) != "start_failed" {
		t.Fatalf("a refused start: %d %v", resp.StatusCode, out)
	}
	if n := len(e.srv.registry.List()); n != 0 {
		t.Fatalf("%d sessions after a refused start", n)
	}
	if _, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: "cat", where: "elsewhere"}, nil); aerr == nil || aerr.status != http.StatusBadGateway || aerr.Code != "start_failed" {
		t.Fatalf("a launch in no Runtime of the server's: %+v", aerr)
	}
	if n := len(seam.Asked("StartTerminal")); n != 1 {
		t.Fatalf("%d starts asked, want the first alone", n)
	}
}

// New given WithExtensions with a Runtime offers the engine it: a run it
// takes has its attempt's session and its check in it.
func TestWithExtensionsOffersTheEngineTheRuntime(t *testing.T) {
	takesAnswered(t)
	seam := nativeSeam()
	offered := &sessiontest.FakeRuntime{Base: local.Native()}
	e := runtimeEnv(t, seam, nil, withRuntime(offered))
	e.gitRepo(t)
	runID := launchOne(t, e, oneTaskCrew("checked", e.root, crew.Check{ID: "unit", Argv: []string{"sh", "-c", "echo checked"}}))
	sid := runningAttempt(t, e, runID, "build", 1)["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, runID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	if got := terminalRefs(offered); !slices.Equal(got, []ext.ProcessRef{attemptRef(sid, runID, 1)}) {
		t.Fatalf("the attempt's session: %+v", got)
	}
	var checks []ext.ProcessRef
	for _, c := range offered.Asked("Run") {
		if s := c.Spec.(ext.CommandSpec); slices.Equal(s.Argv, []string{"sh", "-c", "echo checked"}) {
			checks = append(checks, c.Ref)
		}
	}
	if want := (ext.ProcessRef{Run: runID, Task: "build", Attempt: 1, Tree: ledger.TreeCheckout}); !slices.Equal(checks, []ext.ProcessRef{want}) {
		t.Fatalf("the check ran as %+v, want %+v", checks, want)
	}
	if n := len(seam.Calls()); n != 0 {
		t.Fatalf("the native Runtime was asked %v", methodsOf(seam))
	}
}

// With nothing registered the server offers its engine the free default,
// and every session, a person's and an attempt's, starts natively.
func TestAServerWithNothingRegisteredStartsNatively(t *testing.T) {
	seam := nativeSeam()
	e := runtimeEnv(t, seam, nil)
	if e.srv.offered.Name() != local.Name || e.srv.offered.Where() != ext.WhereNative {
		t.Fatalf("the engine is offered %s (%s)", e.srv.offered.Name(), e.srv.offered.Where())
	}
	e.gitRepo(t)
	runID := launchOne(t, e, oneTaskCrew("native", e.root))
	sid := runningAttempt(t, e, runID, "build", 1)["sessionId"].(string)
	id := e.createSession("cat")
	if got, want := terminalRefs(seam), []ext.ProcessRef{attemptRef(sid, runID, 1), {Session: id}}; !slices.Equal(got, want) {
		t.Fatalf("the native starts: %+v, want %+v", got, want)
	}
}

// starterRuntime is a Runtime that is an ext.Starter: its Start counts
// its calls and waits for release.
type starterRuntime struct {
	*sessiontest.FakeRuntime
	release chan struct{}
	starts  *atomic.Int32
}

func (s starterRuntime) Start(ctx context.Context, _ ext.Host) error {
	s.starts.Add(1)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A Runtime that is a Starter is started first: a run launched before its
// Start has succeeded is not offered it and runs natively; one launched
// after is offered it, and its attempt's session starts in it.
// StartExtensions calls its Start once. The Start's ext.StartWait is the
// test's clock, never fired, so a slow early run cannot have the try
// given up and Start called again.
func TestARuntimeThatIsAStarterIsStartedFirst(t *testing.T) {
	takesAnswered(t)
	seam := nativeSeam()
	rt := starterRuntime{FakeRuntime: &sessiontest.FakeRuntime{Base: local.Native()}, release: make(chan struct{}), starts: &atomic.Int32{}}
	e := runtimeEnv(t, seam, nil, withRuntime(rt))
	e.gitRepo(t)
	clock := newFakeClock(e.srv)
	e.srv.StartExtensions(t.Context())
	clock.next(t, ext.StartWait)
	early := launchOne(t, e, oneTaskCrew("early", e.root))
	sid := runningAttempt(t, e, early, "build", 1)["sessionId"].(string)
	if got := terminalRefs(seam); !slices.Equal(got, []ext.ProcessRef{attemptRef(sid, early, 1)}) {
		t.Fatalf("the early run's attempt: %+v", got)
	}
	if got := methodsOf(rt.FakeRuntime); len(got) != 0 {
		t.Fatalf("the Runtime was asked %v before its Start succeeded", got)
	}
	close(rt.release)
	deadline := time.Now().Add(20 * time.Second)
	for e.srv.extState(rt.Name()) != extStarted {
		if time.Now().After(deadline) {
			t.Fatal("the Runtime's Start did not succeed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	late := launchOne(t, e, oneTaskCrew("late", e.root))
	sid = runningAttempt(t, e, late, "build", 1)["sessionId"].(string)
	if got := terminalRefs(rt.FakeRuntime); !slices.Equal(got, []ext.ProcessRef{attemptRef(sid, late, 1)}) {
		t.Fatalf("the late run's attempt: %+v", got)
	}
	if c := rt.Asked("Takes"); len(c) != 1 || c[0].Ref != (ext.ProcessRef{Run: late}) {
		t.Fatalf("Takes: %+v", c)
	}
	if n := rt.starts.Load(); n != 1 {
		t.Fatalf("the Runtime's Start was called %d times, want once", n)
	}
}

// The guard answers a Takes that panics, or that does not answer within
// ext.TakesWait, as a run declined: the run runs natively, and the failure
// is logged as the guard logs every provider's.
func TestATakesThatPanicsOrStallsRunsNatively(t *testing.T) {
	for _, failure := range []string{"panic", "late"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "panic" {
				takesAnswered(t)
			}
			stall := make(chan struct{})
			t.Cleanup(func() { close(stall) })
			offered := &sessiontest.FakeRuntime{Base: local.Native(), TakesRun: func(ext.ProcessRef) bool {
				if failure == "panic" {
					panic("the fake's Takes panics")
				}
				<-stall
				return true
			}}
			seam := nativeSeam()
			logs := &logBuffer{}
			e := runtimeEnv(t, seam, slog.New(slog.NewTextHandler(logs, nil)), withRuntime(offered))
			e.gitRepo(t)
			runID := launchOne(t, e, oneTaskCrew("guarded", e.root))
			sid := runningAttempt(t, e, runID, "build", 1)["sessionId"].(string)
			if got := terminalRefs(seam); !slices.Equal(got, []ext.ProcessRef{attemptRef(sid, runID, 1)}) {
				t.Fatalf("the attempt's session: %+v", got)
			}
			if got := methodsOf(offered); !slices.Equal(got, []string{"Takes"}) {
				t.Fatalf("the offered Runtime was asked %v", got)
			}
			logs.wait(t, "failure="+failure)
			if l := logs.String(); !strings.Contains(l, "kind=runtime provider=fake failure="+failure) {
				t.Fatalf("the guard's log:\n%s", l)
			}
		})
	}
}

// A person's session outlives its request: the request's context ended as
// the start is made still starts the session, as before the seam.
func TestAPersonsSessionOutlivesItsRequest(t *testing.T) {
	seam := nativeSeam()
	e := runtimeEnv(t, seam, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	onLaunch(t, "checked", cancel)
	req := httptest.NewRequestWithContext(ctx, "POST", "/api/sessions", strings.NewReader(`{"agentId": "cat"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.srv.handleCreateSession(rec, req)
	if ctx.Err() == nil {
		t.Fatal("the request's context did not end as the start was made")
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	calls := seam.Asked("StartTerminal")
	if len(calls) != 1 || calls[0].Process.PID <= 0 {
		t.Fatalf("the start: %+v", calls)
	}
	e.stopAndWait(calls[0].Ref.Session)
}

// stubbornPanicker is a Terminal whose Read panics and whose first Stop
// fails, leaving it running (its Done open), as a process that ignores
// its signal would; each Stop is counted, and its number sent on stopped
// once it has returned.
type stubbornPanicker struct {
	ext.Terminal
	stops   *atomic.Int32
	stopped chan int32
}

func (p stubbornPanicker) Read([]byte) (int, error) { panic("the fake's terminal panics in Read") }

func (p stubbornPanicker) Stop(ctx context.Context, grace time.Duration) error {
	n := p.stops.Add(1)
	defer func() { p.stopped <- n }()
	if n == 1 {
		return errors.New("the process did not exit")
	}
	return p.Terminal.Stop(ctx, grace)
}

// stubbornRuntime is a Runtime whose terminals are stubbornPanickers of
// FakeProcs.
type stubbornRuntime struct {
	*sessiontest.FakeRuntime
	stops   *atomic.Int32
	stopped chan int32
}

func (r stubbornRuntime) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	t, err := r.FakeRuntime.StartTerminal(ctx, s)
	if err != nil {
		return nil, err
	}
	return stubbornPanicker{Terminal: t, stops: r.stops, stopped: r.stopped}, nil
}

// A registered Runtime's terminal that panics and whose stop then fails
// is still running, and its session says so: the session's process is
// not done, and the session's next Stop asks the Runtime's Stop again,
// whose success ends it; a Stop after that asks nothing.
func TestATerminalThatPanicsAndWhoseStopFailsIsStoppedAgain(t *testing.T) {
	fp := sessiontest.NewFakeProc()
	t.Cleanup(func() { fp.End(0) })
	rt := stubbornRuntime{FakeRuntime: &sessiontest.FakeRuntime{Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) { return fp, nil }},
		stops: &atomic.Int32{}, stopped: make(chan int32, 4)}
	e := runtimeEnv(t, nativeSeam(), nil, withRuntime(rt))
	l, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: "cat", where: rt.Where()}, nil)
	if aerr != nil {
		t.Fatalf("the start: %+v", aerr)
	}
	stop := func(want int32) {
		t.Helper()
		select {
		case n := <-rt.stopped:
			if n != want {
				t.Fatalf("stop %d returned, want %d", n, want)
			}
		case <-time.After(testWait):
			t.Fatalf("stop %d not asked within %s", want, testWait)
		}
	}
	stop(1) // the guard's, once Read has panicked; it fails
	select {
	case <-l.ProcessDone():
		t.Fatal("the session's process is done although its stop failed")
	default:
	}
	if fp.Stopped() {
		t.Fatal("the process is stopped although its stop failed")
	}
	if err := l.Stop(t.Context()); err != nil {
		t.Fatalf("the session's stop: %v", err)
	}
	stop(2)
	if !fp.Stopped() {
		t.Fatal("the session's stop did not reach the process")
	}
	select {
	case <-l.ProcessDone():
	case <-time.After(testWait):
		t.Fatal("the session's process not done once its stop succeeded")
	}
	if err := l.Stop(t.Context()); err != nil {
		t.Fatalf("a stop once the process is done: %v", err)
	}
	if n := rt.stops.Load(); n != 2 {
		t.Fatalf("the Runtime's Stop was called %d times, want 2", n)
	}
}

// panickyService is a ServiceProcess whose Result panics; its Stop is
// counted and ends it.
type panickyService struct {
	done  chan struct{}
	stops *atomic.Int32
}

func (p panickyService) Done() <-chan struct{} { return p.done }

func (p panickyService) Result() ext.CommandResult { panic("the fake's service panics in Result") }

func (p panickyService) Stop(context.Context) error {
	if p.stops.Add(1) == 1 {
		close(p.done)
	}
	return nil
}

// A registered Runtime's service that panics is ended, not the server:
// the guard logs the panic at error, naming the Runtime and the method,
// stops the service through the Runtime's Stop once, and answers from then
// on as a service that has ended, its result Exit -1 and the words.
func TestAServiceThatPanicsIsEndedNotTheServer(t *testing.T) {
	stops := &atomic.Int32{}
	svc := panickyService{done: make(chan struct{}), stops: stops}
	rt := &sessiontest.FakeRuntime{Service: func(ext.CommandSpec) (ext.ServiceProcess, error) { return svc, nil }}
	logs := &logBuffer{}
	e := runtimeEnv(t, nativeSeam(), slog.New(slog.NewTextHandler(logs, nil)), withRuntime(rt))
	sp, err := e.srv.offered.Serve(t.Context(), ext.CommandSpec{Argv: []string{"serve"}})
	if err != nil {
		t.Fatal(err)
	}
	if r := sp.Result(); r.Exit != -1 || r.Err != "the Runtime fake panicked in ServiceProcess.Result" {
		t.Fatalf("the result of a service that panicked: %+v", r)
	}
	<-sp.Done()
	if err := sp.Stop(t.Context()); err != nil {
		t.Fatalf("a Stop after the panic: %v", err)
	}
	if n := stops.Load(); n != 1 {
		t.Fatalf("the Runtime's Stop was called %d times, want once", n)
	}
	if l := logs.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "provider=fake call=ServiceProcess.Result failure=panic") {
		t.Fatalf("the guard's log:\n%s", l)
	}
}
