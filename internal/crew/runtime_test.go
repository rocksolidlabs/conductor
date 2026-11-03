package crew

import (
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proc"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// offer gives e the fake to offer its managed runs.
func offer(t *testing.T, e *Engine, f *sessiontest.FakeRuntime) {
	t.Helper()
	if err := e.SetRuntime(f); err != nil {
		t.Fatal(err)
	}
}

// methods is the methods of calls, in order.
func methods(calls []sessiontest.RuntimeCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Method)
	}
	return out
}

// lineOf is the line of sh a command call ran (shStep's argv), "" for
// another.
func lineOf(c sessiontest.RuntimeCall) string {
	s, ok := c.Spec.(ext.CommandSpec)
	if !ok || len(s.Argv) != 3 || s.Argv[0] != "sh" {
		return ""
	}
	return s.Argv[2]
}

// Every process of a managed run goes through the Runtime the engine
// offered and that took it (docs/plans/runtime.md, PR-2): Takes asked
// once, with the run alone; the prerequisite with the run alone; the
// worktree's setup step, its service, its probe and its port with the
// attempt and the tree "worktree"; the checkout's setup step, service,
// probe and port and the task's check with the tree "checkout". Each
// command's environment is Conductor's (proc.Prepare), the tree's port in
// it, no token of the server's.
func TestEveryProcessOfARunGoesThroughTheRuntime(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	const id = "api-sweep-0000c10a"
	e.newRunID = func(string) string { return id }
	worktree := ext.ProcessRef{Run: id, Task: "build", Attempt: 1, Tree: ledger.TreeWorktree}
	checkout := ext.ProcessRef{Run: id, Task: "build", Attempt: 1, Tree: ledger.TreeCheckout}
	fake := &sessiontest.FakeRuntime{Base: local.Native(),
		Ports:  map[ext.ProcessRef]int{worktree: 41001, checkout: 41002},
		Probes: map[string]int{"http://127.0.0.1:41001/health": 200, "http://127.0.0.1:41002/health": 200}}
	offer(t, e, fake)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "echo unit")}
	m := Manifest{Prerequisites: []Check{shStep("tools", "echo tools")}, Setup: []Check{shStep("deps", "echo deps")}, Ports: []string{"web"},
		Services: []Check{serviceLoop("web", "web.pid", false)},
		Ready:    []Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/health", IntervalSeconds: 1}}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, build))
	if run.ID != id {
		t.Fatalf("the run is %s", run.ID)
	}
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	waitTreesEnded(t, e, run.ID)
	if r := runOf(t, e, run.ID); !r.taken || r.rt.Where() != "fake" {
		t.Fatalf("the run's Runtime: %q, taken %v", r.rt.Where(), r.taken)
	}
	if c := fake.Asked("Takes"); len(c) != 1 || c[0].Ref != (ext.ProcessRef{Run: id}) {
		t.Fatalf("Takes: %+v", c)
	}
	runs := map[string][]ext.ProcessRef{}
	for _, c := range fake.Asked("Run") {
		runs[lineOf(c)] = append(runs[lineOf(c)], c.Ref)
		s := c.Spec.(ext.CommandSpec)
		if !slices.Contains(s.Env, "TERM=dumb") || slices.ContainsFunc(s.Env, func(kv string) bool { return strings.HasPrefix(kv, "CONDUCTOR_NOTIFY") }) {
			t.Fatalf("%q's environment: %v", lineOf(c), s.Env)
		}
		if c.Ref.Tree != "" && !slices.Contains(s.Env, map[string]string{ledger.TreeWorktree: "CONDUCTOR_PORT_WEB=41001", ledger.TreeCheckout: "CONDUCTOR_PORT_WEB=41002"}[c.Ref.Tree]) {
			t.Fatalf("%q in the %s has no port of its tree: %v", lineOf(c), c.Ref.Tree, s.Env)
		}
	}
	want := map[string][]ext.ProcessRef{"echo tools": {{Run: id}}, "echo deps": {worktree, checkout}, "echo unit": {checkout}}
	if !reflect.DeepEqual(runs, want) {
		t.Fatalf("the commands run: %+v", runs)
	}
	for _, method := range []string{"Serve", "Probe", "Port"} {
		var refs []ext.ProcessRef
		for _, c := range fake.Asked(method) {
			refs = append(refs, c.Ref)
		}
		if !reflect.DeepEqual(refs, []ext.ProcessRef{worktree, checkout}) {
			t.Fatalf("%s: %+v", method, refs)
		}
	}
	if n := len(fake.Asked("StartTerminal")); n != 0 {
		t.Fatalf("%d terminals started through the Runtime: the attempt's session is the launcher's", n)
	}
	// The receipts are the Runtime's processes: the check ran, natively
	// under the fake's Base, with a process of its own.
	if c, ok := rowOf(l.stepRows(run.ID, "build", 1), "unit", 1); !ok || c.Phase != ledger.PhaseCheck || c.Exit != 0 || c.PID == 0 || string(c.Log) != "unit\n" {
		t.Fatalf("the check's receipt: %+v", c)
	}
}

// A run the offered Runtime declines runs natively: the fake is asked
// Takes and nothing else, and the run ends as it does natively, its
// prerequisite, setup steps and check run on this machine.
func TestADeclinedRunRunsNatively(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	fake := &sessiontest.FakeRuntime{TakesRun: func(ext.ProcessRef) bool { return false }}
	offer(t, e, fake)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "test -f dep.txt")}
	m := Manifest{Prerequisites: []Check{shStep("tools", "echo tools")}, Setup: []Check{shStep("deps", "echo dep > dep.txt")}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	waitTreesEnded(t, e, run.ID)
	if got := methods(fake.Calls()); !reflect.DeepEqual(got, []string{"Takes"}) {
		t.Fatalf("the declining fake was asked %v", got)
	}
	if r := runOf(t, e, run.ID); r.taken || r.rt.Where() != ext.WhereNative {
		t.Fatalf("the declined run's Runtime: %q, taken %v", r.rt.Where(), r.taken)
	}
	if p := l.prereqRows(run.ID); len(p) != 1 || p[0].Exit != 0 || p[0].PID == 0 {
		t.Fatalf("the prerequisite's receipt: %+v", p)
	}
	if c, ok := rowOf(l.stepRows(run.ID, "build", 1), "unit", 1); !ok || c.Exit != 0 || c.PID == 0 {
		t.Fatalf("the check's receipt: %+v", c)
	}
}

// A legacy crew's run is offered nothing: its members start as they do,
// the offered Runtime is asked nothing, and the run's Runtime is the
// native one.
func TestALegacyRunIsOfferedNothing(t *testing.T) {
	e, fl := readyEngine(t)
	fake := &sessiontest.FakeRuntime{}
	offer(t, e, fake)
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Own the plan.")))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fl.launched()); n != 1 {
		t.Fatalf("%d sessions launched", n)
	}
	if c := fake.Calls(); len(c) != 0 {
		t.Fatalf("the fake was asked %v", methods(c))
	}
	if r := runOf(t, e, run.ID); r.taken || r.rt.Where() != ext.WhereNative {
		t.Fatalf("the legacy run's Runtime: %q, taken %v", r.rt.Where(), r.taken)
	}
}

// A result crosses the Runtime whole: every exported field of proc.Result
// has its ext.CommandResult field of the same name and kind (the process's
// fields too), and a result a Runtime gives lands in the check's receipt
// unchanged, its process as Started told it and as the result says it.
func TestAResultCrossesTheRuntimeWhole(t *testing.T) {
	same := func(a, b reflect.Type) {
		t.Helper()
		if a.NumField() != b.NumField() {
			t.Fatalf("%s has %d fields, %s %d", a, a.NumField(), b, b.NumField())
		}
		for i := range a.NumField() {
			f := a.Field(i)
			g, ok := b.FieldByName(f.Name)
			if !ok {
				t.Fatalf("%s has no %s", b, f.Name)
			}
			if f.Type != g.Type && f.Type.Kind() != reflect.Struct {
				t.Fatalf("%s.%s is %s, %s.%s %s", a, f.Name, f.Type, b, g.Name, g.Type)
			}
		}
	}
	same(reflect.TypeFor[proc.Result](), reflect.TypeFor[ext.CommandResult]())
	same(reflect.TypeFor[proc.Process](), reflect.TypeFor[ext.ProcessInfo]())
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	process := ext.ProcessInfo{PID: 4242, Group: 4243, StartedAt: at}
	scripted := ext.CommandResult{Process: process, Exit: 3, Signal: "", Head: []byte("head\n"), Tail: []byte("the tail\n"), Total: 12345, Truncated: true,
		Started: at, Duration: 1500 * time.Millisecond, TimedOut: true, Killed: true, Err: "", LogErr: ""}
	// Every field set, so a field dropped on the way is seen.
	full := scripted
	full.Signal, full.Canceled, full.Err, full.LogErr = "KILL", true, "went wrong", "log short"
	if got := procResult(full); !reflect.DeepEqual(commandResultOf(got), full) {
		t.Fatalf("procResult dropped a field:\n%+v\n%+v", got, full)
	}

	dir := newRepo(t)
	e, _, l := managedEngine(t)
	fake := &sessiontest.FakeRuntime{Result: func(s ext.CommandSpec) ext.CommandResult {
		if s.Started != nil {
			s.Started(process)
		}
		_, _ = io.WriteString(s.Log, "the log\n")
		return scripted
	}}
	offer(t, e, fake)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "echo unit")}
	run := launchManaged(t, e, repoCrew(dir, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	// On the ledger's row, which holds: the task goes on to attempt 2 at
	// once, so the attempt's state in the task passes through failed.
	waitFake(t, "attempt 1 failed in the ledger", func() bool { return l.attemptRow(t, run.ID, "build", 1).State == ledger.AttemptFailed })
	c, ok := rowOf(l.stepRows(run.ID, "build", 1), "unit", 1)
	if !ok || c.State != ledger.CheckFinished || c.Exit != 3 || c.TimedOut != true || c.Killed != true || c.Truncated != true || c.Total != 12345 ||
		c.Tail != "the tail\n" || string(c.Log) != "the log\n" || c.PID != 4242 || c.Group != 4243 || !c.ProcessStartedAt.Equal(at) {
		t.Fatalf("the check's receipt: %+v", c)
	}
	if r := fake.Asked("Run"); len(r) != 1 || lineOf(r[0]) != "echo unit" {
		t.Fatalf("the Runtime ran %+v", r)
	}
}

// commandResultOf is r as a Runtime gives it, field for field: the inverse
// of procResult, for the test.
func commandResultOf(r proc.Result) ext.CommandResult {
	return ext.CommandResult{Process: ext.ProcessInfo{PID: r.Process.PID, Group: r.Process.Group, StartedAt: r.Process.StartedAt},
		Exit: r.Exit, Signal: r.Signal, Head: r.Head, Tail: r.Tail, Total: r.Total, Truncated: r.Truncated, Started: r.Started, Duration: r.Duration,
		TimedOut: r.TimedOut, Canceled: r.Canceled, Killed: r.Killed, Err: r.Err, LogErr: r.LogErr}
}

// A spec proc.Prepare refuses is answered by the executor as proc.Run and
// proc.Serve answer it on their own (Exit -1, the same words; the same
// error), and the Runtime is never called.
func TestARefusedSpecKeepsItsWords(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	fake := &sessiontest.FakeRuntime{}
	x := execFor(fake)
	ref := ext.ProcessRef{Run: "r", Task: "build", Attempt: 1, Tree: ledger.TreeCheckout}
	for name, s := range map[string]proc.Spec{
		"an Inject name not Conductor's": {Argv: []string{"true"}, Dir: dir, Timeout: time.Second, Inject: map[string]string{"NOT_CONDUCTORS": "x"}},
		"no command":                     {Dir: dir, Timeout: time.Second},
		"a relative directory":           {Argv: []string{"true"}, Dir: "rel", Timeout: time.Second},
		"a Conductor name in Env":        {Argv: []string{"true"}, Dir: dir, Timeout: time.Second, Env: map[string]string{"CONDUCTOR_NOTIFY_TOKEN": "x"}},
	} {
		got, want := x.run(ctx, ref, s), proc.Run(ctx, s)
		if !reflect.DeepEqual(got, want) || got.Exit != -1 || got.Err == "" {
			t.Fatalf("%s: the executor's run answered %+v, proc.Run %+v", name, got, want)
		}
		svc, err := x.serve(ctx, ref, s)
		native, nerr := proc.Serve(ctx, s)
		if native != nil {
			_ = native.Stop(ctx)
		}
		if svc != nil || err == nil || nerr == nil || err.Error() != nerr.Error() {
			t.Fatalf("%s: the executor's serve answered %v, proc.Serve %v", name, err, nerr)
		}
	}
	if c := fake.Calls(); len(c) != 0 {
		t.Fatalf("the Runtime was called: %v", methods(c))
	}
	// The timeout is Run's alone: a service may have none.
	if got := x.run(ctx, ref, proc.Spec{Argv: []string{"true"}, Dir: dir}); got.Exit != -1 || !strings.Contains(got.Err, "timeout") {
		t.Fatalf("a run with no timeout: %+v", got)
	}
}

// SetRuntime refuses nil, a Runtime whose Where is not valid, and any
// call once the engine has made a run.
func TestSetRuntimeRefusals(t *testing.T) {
	e, _ := readyEngine(t)
	if err := e.SetRuntime(nil); err == nil {
		t.Fatal("nil was taken")
	}
	for _, w := range []ext.Where{"Not-Valid", "9lives", "has space", ext.Where(strings.Repeat("a", ext.MaxWhere+1))} {
		if err := e.SetRuntime(&sessiontest.FakeRuntime{Place: w}); err == nil {
			t.Fatalf("the where %q was taken", w)
		}
	}
	if e.offered.Where() != ext.WhereNative {
		t.Fatalf("a refusal changed the offered Runtime: %q", e.offered.Where())
	}
	fake := &sessiontest.FakeRuntime{}
	offer(t, e, fake)
	if _, err := e.Launch(t.Context(), testCrew(immediate("lead", "Own the plan."))); err != nil {
		t.Fatal(err)
	}
	if err := e.SetRuntime(&sessiontest.FakeRuntime{Place: "other"}); err == nil || !strings.Contains(err.Error(), "first run") {
		t.Fatalf("SetRuntime after a launch: %v", err)
	}
	if e.offered != ext.Runtime(fake) {
		t.Fatal("the refusal after a launch changed the offered Runtime")
	}
}

// An engine runs natively by default: NewEngine offers its runs the native
// Runtime (local.Native), which takes them, and a run's check runs on this
// machine through it.
func TestAnEngineRunsNativelyByDefault(t *testing.T) {
	e := NewEngine(nil, nil)
	if e.offered.Name() != local.Name || e.offered.Where() != ext.WhereNative || e.native.Where() != ext.WhereNative {
		t.Fatalf("a new engine offers %q (%q), and keeps %q", e.offered.Name(), e.offered.Where(), e.native.Where())
	}
	dir := newRepo(t)
	m, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "echo $$")}
	run := launchManaged(t, m, repoCrew(dir, build))
	a := waitAttempt(t, m, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, m, a, "built.txt", "yes\n", "built")
	waitTask(t, m, run.ID, "build", ledger.TaskSucceeded, 1)
	if r := runOf(t, m, run.ID); !r.taken || r.rt.Where() != ext.WhereNative {
		t.Fatalf("the run's Runtime: %q, taken %v", r.rt.Where(), r.taken)
	}
	if c, ok := rowOf(l.stepRows(run.ID, "build", 1), "unit", 1); !ok || c.Exit != 0 || c.PID == 0 || strings.TrimSpace(string(c.Log)) == "" {
		t.Fatalf("the check's receipt: %+v", c)
	}
}
