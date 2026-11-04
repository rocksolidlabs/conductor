package crew

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proc"
)

// runOf is the engine's run, for a look at what a read does not show.
func runOf(t *testing.T, e *Engine, runID string) *run {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.runs[runID]
	if r == nil {
		t.Fatalf("no run %s", runID)
	}
	return r
}

// prereqState reads the run's prerequisites' flag and generations.
func prereqState(e *Engine, r *run) (state string, gen, inFlight int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return r.prereq, r.prereqGen, r.prereqs
}

// gatedPrerequisite is a prerequisite that writes its pid to pidFile and
// waits for gate.
func gatedPrerequisite(id, gate, pidFile string) Check {
	return shStep(id, `echo $$ > "`+pidFile+`"; `+gated(gate))
}

// alive reports whether the process pid is alive (a signal 0 reaches it).
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// pidIn reads the pid a step wrote, once it is there.
func pidIn(t *testing.T, path string) int {
	t.Helper()
	var pid int
	waitFake(t, "the pid file", func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		pid = n
		return err == nil && n > 0
	})
	return pid
}

// The prerequisites run once per run, before the first dispatch, on a
// goroutine of the run's: the launch answers while one is held at its
// gate (the run says it runs, no attempt is reserved, a pump dispatches
// nothing); opened, prerequisites.passed is recorded before the first
// attempt.reserved, each a receipt with its log and hash.
func TestThePrerequisitesRunBeforeTheFirstDispatch(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	c := repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("node", "echo v22"), gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead"))
	run := launchManaged(t, e, c)
	if run.Manifest == nil || len(run.Manifest.Prerequisites) != 2 || run.Manifest.Prerequisites[1].State != "running" {
		t.Fatalf("the launch's answer: %+v", run.Manifest)
	}
	pidIn(t, pidFile)
	r := runOf(t, e, run.ID)
	if st, _, n := prereqState(e, r); st != prereqRunning || n != 1 {
		t.Fatalf("the prerequisites: %s, %d in flight", st, n)
	}
	e.pumpLocked(r)
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != 0 || l.count(run.ID, "attempt.reserved") != 0 {
		t.Fatalf("dispatched before the prerequisites passed: %+v", ts)
	}
	open(t, gate)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	kinds := l.kinds(run.ID)
	if p, a := slices.Index(kinds, "prerequisites.passed"), slices.Index(kinds, "attempt.reserved"); p < 0 || p > a {
		t.Fatalf("prerequisites.passed before the first attempt.reserved: %v", kinds)
	}
	if p := l.payloads(run.ID, "prerequisites.passed"); len(p) != 1 || p[0]["manifest"] != "crew:0" {
		t.Fatalf("prerequisites.passed: %v", p)
	}
	rows := l.prereqRows(run.ID)
	if len(rows) != 2 || rows[0].ID != "node" || rows[0].K != 1 || string(rows[0].Log) != "v22\n" || rows[0].LogSHA256 != sha(rows[0].Log) || rows[0].Dir != dir || rows[0].Subject != runGit(t, dir, "rev-parse", "HEAD") || rows[0].Manifest != "crew:0" || rows[1].State != ledger.CheckFinished {
		t.Fatalf("the prerequisites' rows: %+v", rows)
	}
	if r, _ := e.Get(run.ID); r.Manifest.Prerequisites[0].State != "passed" || r.Manifest.Prerequisites[0].K != 1 || *r.Manifest.Prerequisites[0].Exit != 0 {
		t.Fatalf("the run's prerequisites: %+v", r.Manifest.Prerequisites)
	}
	stopRun(t, e, run.ID)
}

// A failing prerequisite pauses the run's dispatch with its words, in the
// transaction of its finished row, and spends nothing; fixed and unpaused,
// they run again (k 2) and the run goes on.
func TestAFailingPrerequisitePausesAndSpendsNothing(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	ok := filepath.Join(t.TempDir(), "node-installed")
	c := repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("node", `test -e "`+ok+`" || { echo node: not found >&2; exit 127; }`)}}, work("build", "lead"))
	run := launchManaged(t, e, c)
	why := "the prerequisite node failed: node exited 127: node: not found"
	waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == why })
	waitFake(t, "the pause committed", func() bool { return l.count(run.ID, "run.paused") == 1 })
	l.mu.Lock()
	var kinds []string
	for _, ev := range l.events {
		if ev.runID == run.ID {
			kinds = append(kinds, ev.kind)
		}
	}
	l.mu.Unlock()
	if f := slices.Index(kinds, "run.paused"); f < 1 || kinds[f-1] != "step.finished" {
		t.Fatalf("the pause beside the step's end: %v", kinds)
	}
	r := runOf(t, e, run.ID)
	if st, _, _ := prereqState(e, r); st != prereqFailed {
		t.Fatalf("the prerequisites: %s", st)
	}
	e.pumpLocked(r)
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != 0 || l.count(run.ID, "attempt.reserved") != 0 {
		t.Fatalf("something was spent: %+v", ts)
	}
	if rows := l.prereqRows(run.ID); len(rows) != 1 || rows[0].Exit != 127 || rows[0].Error != "node exited 127: node: not found" {
		t.Fatalf("the prerequisite's row: %+v", rows)
	}
	open(t, ok)
	pauseRun(t, e, run.ID, false)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if rows := l.prereqRows(run.ID); len(rows) != 2 || rows[1].K != 2 || rows[1].Exit != 0 {
		t.Fatalf("the prerequisites after the unpause: %+v", rows)
	}
	if r, _ := e.Get(run.ID); r.Paused || r.PausedWhy != "" {
		t.Fatalf("the run after the unpause: %v %q", r.Paused, r.PausedWhy)
	}
	stopRun(t, e, run.ID)
}

// A stop during a prerequisite ends it: its process killed with its group,
// its row interrupted with why.
func TestAStopEndsAPrerequisiteHeldAtItsGate(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead")))
	pid := pidIn(t, pidFile)
	stopRun(t, e, run.ID)
	rows := l.prereqRows(run.ID)
	if len(rows) != 1 || rows[0].State != ledger.CheckInterrupted || rows[0].Error != "tools was interrupted: the run was stopped" {
		t.Fatalf("the prerequisite after the stop: %+v", rows)
	}
	if alive(pid) {
		t.Fatalf("the prerequisite's process %d outlived the stop", pid)
	}
}

// The sole task cancelled while a prerequisite is held at its gate
// finishes the run in that commit, which ends the generation: its process
// killed, its row interrupted with the words. The run is held under
// eviction pressure until the generation's last commit, and forgotten only
// after.
func TestACancelThatFinishesTheRunEndsItsPrerequisites(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	old := maxRuns
	t.Cleanup(func() { maxRuns = old })
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead")))
	pid := pidIn(t, pidFile)
	// The generation's last commit, the interrupted row, is held.
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "step.finished[phase=prerequisite]", make(chan struct{}, 1), make(chan struct{})
	l.mu.Unlock()
	if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.Get(run.ID); r.Outcome != ledger.OutcomeCancelled {
		t.Fatalf("the run after the cancel: %q", r.Outcome)
	}
	<-l.blocked
	if alive(pid) {
		t.Fatalf("the prerequisite's process %d outlived the run's end", pid)
	}
	maxRuns = 1
	other := testCrew(manual("lead", ""))
	if _, err := e.Launch(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); !ok {
		t.Fatal("the run was forgotten with its prerequisites' last commit in flight")
	}
	close(l.release)
	r := runOf(t, e, run.ID)
	waitTrue(t, "the generation's end", func() bool { _, _, n := prereqState(e, r); return n == 0 })
	if rows := l.prereqRows(run.ID); len(rows) != 1 || rows[0].State != ledger.CheckInterrupted || rows[0].Error != "tools was interrupted: the run finished before the prerequisite ended" {
		t.Fatalf("the prerequisite after the run's end: %+v", rows)
	}
	if _, err := e.Launch(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("the finished run was kept past the bound")
	}
}

// A pause and an unpause while a generation is held at its gate start no
// second one: one run of the prerequisite in the executor, k 1; opened,
// its passed stands and the run dispatches.
func TestAnUnpauseWhileTheirGenerationRunsStartsNoSecond(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	var mu sync.Mutex
	runs := 0
	native := e.exec.run
	e.exec.run = func(ctx context.Context, ref ext.ProcessRef, s proc.Spec) proc.Result {
		if len(s.Argv) == 3 && strings.Contains(s.Argv[2], gate) {
			mu.Lock()
			runs++
			mu.Unlock()
		}
		return native(ctx, ref, s)
	}
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead")))
	pidIn(t, pidFile)
	pauseRun(t, e, run.ID, true)
	pauseRun(t, e, run.ID, false)
	r := runOf(t, e, run.ID)
	if st, gen, n := prereqState(e, r); st != prereqRunning || gen != 1 || n != 1 {
		t.Fatalf("after the unpause: %s, generation %d, %d in flight", st, gen, n)
	}
	open(t, gate)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	mu.Lock()
	n := runs
	mu.Unlock()
	if rows := l.prereqRows(run.ID); n != 1 || len(rows) != 1 || rows[0].K != 1 {
		t.Fatalf("%d runs of the prerequisite, rows %+v", n, rows)
	}
	stopRun(t, e, run.ID)
}

// A commit from a generation that is no longer the run's is dropped, its
// rows kept: the prerequisites are not passed by it, and nothing is
// dispatched.
func TestAStaleGenerationsCommitIsDropped(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead")))
	pidIn(t, pidFile)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	r.prereqGen++
	e.mu.Unlock()
	open(t, gate)
	waitTrue(t, "the stale generation's end", func() bool { _, _, n := prereqState(e, r); return n == 0 })
	if rows := l.prereqRows(run.ID); len(rows) != 1 || rows[0].State != ledger.CheckFinished || rows[0].Exit != 0 {
		t.Fatalf("the stale generation's row: %+v", rows)
	}
	if st, _, _ := prereqState(e, r); st != prereqRunning || l.count(run.ID, "prerequisites.passed") != 0 {
		t.Fatalf("a stale generation passed the prerequisites: %s", st)
	}
	e.pumpLocked(r)
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != 0 {
		t.Fatalf("dispatched on a stale generation: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// A continuation runs its prerequisites again, at its base, k 1 in the new
// run.
func TestAContinuationRunsThePrerequisitesAgain(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	c := repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("tools", "true")}}, work("build", "lead"))
	run := launchManaged(t, e, c)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	attemptN(t, e2, next.ID, "build", 2)
	if rows := l.prereqRows(next.ID); len(rows) != 1 || rows[0].K != 1 || rows[0].RunID != next.ID {
		t.Fatalf("the continuation's prerequisites: %+v", rows)
	}
	kinds := l.kinds(next.ID)
	if p, a := slices.Index(kinds, "prerequisites.passed"), slices.Index(kinds, "attempt.reserved"); p < 0 || p > a {
		t.Fatalf("the continuation dispatched before its prerequisites: %v", kinds)
	}
	stopRun(t, e2, next.ID)
}

// While a generation runs (an unpause's), another attempt's close pumps
// the run and dispatches nothing; passed, the task its result readied is
// dispatched.
func TestAnAttemptsCloseDispatchesNothingWhileThePrerequisitesRun(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	open(t, gate)
	c := repoManifestCrew(dir, Manifest{Prerequisites: []Check{gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead"), work("docs", "writer", "build"))
	run := launchManaged(t, e, c)
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pidFile); err != nil {
		t.Fatal(err)
	}
	pauseRun(t, e, run.ID, true)
	pauseRun(t, e, run.ID, false)
	pidIn(t, pidFile)
	commitResult(t, e, b, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	e.pumpLocked(runOf(t, e, run.ID))
	if d := taskOf(t, e, run.ID, "docs"); d.Attempts != 0 {
		t.Fatalf("docs dispatched while the prerequisites ran: %+v", d)
	}
	open(t, gate)
	waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	if rows := l.prereqRows(run.ID); len(rows) != 2 || rows[1].K != 2 {
		t.Fatalf("the prerequisites: %+v", rows)
	}
	stopRun(t, e, run.ID)
}

// A step whose program cannot start says why on its row: a prerequisite's
// and a setup step's alike, the setup's uncounted in the worktree.
func TestAStepWhoseProgramCannotStartSaysWhy(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	missing := filepath.Join(t.TempDir(), "no-such-program")
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Setup: []Check{{ID: "deps", Argv: []string{missing}}}}, work("build", "lead")))
	attemptFailed(t, e, run.ID, "build", 1)
	rows := l.stepRows(run.ID, "build", 1)
	if len(rows) != 1 || rows[0].State != ledger.CheckFinished || !strings.HasPrefix(rows[0].Error, "deps could not run: ") || !strings.Contains(rows[0].Error, "no-such-program") {
		t.Fatalf("the step's row:\n%s", rowWords(rows))
	}
	if row := l.attemptRow(t, run.ID, "build", 1); row.Stage != "setup deps" || row.Error != rows[0].Error {
		t.Fatalf("attempt 1: %+v", row)
	}
	waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused })
	stopRun(t, e, run.ID)
}

// An unpause that lands after a failed generation's last commit and before
// its goroutine ends runs the prerequisites again all the same: the
// generation armed starts as the last one ends.
func TestAnUnpauseRightAfterAFailureRunsThemAgain(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	ok := filepath.Join(t.TempDir(), "ok")
	ending, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	e.beforePrereqEnd = func() {
		once.Do(func() {
			ending <- struct{}{}
			<-release
		})
	}
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("node", `test -e "`+ok+`"`)}}, work("build", "lead")))
	<-ending
	waitFake(t, "the pause committed", func() bool { return l.count(run.ID, "run.paused") == 1 })
	open(t, ok)
	pauseRun(t, e, run.ID, false)
	r := runOf(t, e, run.ID)
	if st, gen, n := prereqState(e, r); st != prereqPending || gen != 1 || n != 1 {
		t.Fatalf("armed behind the ending generation: %s, generation %d, %d in flight", st, gen, n)
	}
	close(release)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if rows := l.prereqRows(run.ID); len(rows) != 2 || rows[0].Exit != 1 || rows[1].K != 2 || rows[1].Exit != 0 {
		t.Fatalf("the prerequisites: %+v", rows)
	}
	stopRun(t, e, run.ID)
}

// An unpause that lands after a prerequisite's failure and before the
// generation's look for a row the ledger refused (prerequisiteRefused) is
// not taken back by that failure. The step's finished row took the
// failure, whether the ledger took the row or refused it (its rows then go
// with the unpause's commit), so the look fails nothing a second time. The
// run stays unpaused, the generation the unpause armed starts as the
// failed one ends (k 2), and the task is dispatched once they pass
// (Codex's review of #422, round 4: the look failed the generation again,
// paused the run over the unpause and left nothing armed to start).
func TestAnUnpauseAfterAFailureIsNotTakenBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refused bool
	}{
		{"its row committed", false},
		{"its row refused", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			if tc.refused {
				l.mu.Lock()
				l.failOn, l.failOnce = "step.finished[phase=prerequisite]", true
				l.mu.Unlock()
			}
			ok := filepath.Join(t.TempDir(), "ok")
			looking, release := make(chan struct{}, 1), make(chan struct{})
			var once sync.Once
			e.beforePrereqRefused = func() {
				once.Do(func() {
					looking <- struct{}{}
					<-release
				})
			}
			run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("node", `test -e "`+ok+`"`)}}, work("build", "lead")))
			<-looking
			why := "the prerequisite node failed: node exited 1"
			if got, _ := e.Get(run.ID); !got.Paused || got.PausedWhy != why {
				t.Fatalf("the run at the look: paused %v %q", got.Paused, got.PausedWhy)
			}
			l.mu.Lock()
			failOn := l.failOn
			l.mu.Unlock()
			// Committed, the failure's row and pause are in the ledger;
			// refused, they wait for the next commit, the unpause's.
			want := 1
			if tc.refused {
				want = 0
			}
			if failOn != "" || l.count(run.ID, "step.finished") != want || l.count(run.ID, "run.paused") != want {
				t.Fatalf("the ledger at the look (refusal pending %q): %v", failOn, l.kinds(run.ID))
			}
			open(t, ok)
			pauseRun(t, e, run.ID, false)
			r := runOf(t, e, run.ID)
			if st, gen, n := prereqState(e, r); st != prereqPending || gen != 1 || n != 1 {
				t.Fatalf("armed behind the failed generation: %s, generation %d, %d in flight", st, gen, n)
			}
			close(release)
			waitTrue(t, "the generations' end", func() bool {
				st, _, n := prereqState(e, r)
				return n == 0 && (st == prereqPassed || st == prereqFailed)
			})
			if st, gen, _ := prereqState(e, r); st != prereqPassed || gen != 2 {
				t.Fatalf("after the look: %s, generation %d", st, gen)
			}
			if got, _ := e.Get(run.ID); got.Paused || got.PausedWhy != "" {
				t.Fatalf("the unpause was taken back: paused %v %q", got.Paused, got.PausedWhy)
			}
			waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			rows := l.prereqRows(run.ID)
			if len(rows) != 2 || rows[0].K != 1 || rows[0].Exit != 1 || rows[0].Error != "node exited 1" || rows[1].K != 2 || rows[1].Exit != 0 || rows[1].Error != "" {
				t.Fatalf("the prerequisites: %+v", rows)
			}
			if n := l.count(run.ID, "run.paused"); n != 2 {
				t.Fatalf("%d run.paused events, not the failure's and the unpause's alone: %v", n, l.kinds(run.ID))
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A prerequisite's row the ledger refuses ends its generation failed and
// pauses the dispatch with why; unpaused, they run again and the run goes
// on.
func TestAPrerequisiteTheLedgerCouldNotRecordPauses(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	l.mu.Lock()
	l.failOn, l.failOnce = "step.started[phase=prerequisite]", true
	l.mu.Unlock()
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("tools", "true")}}, work("build", "lead")))
	waitTrue(t, "the run paused", func() bool {
		r, _ := e.Get(run.ID)
		return r.Paused && strings.Contains(r.PausedWhy, "the ledger could not record the step tools")
	})
	r := runOf(t, e, run.ID)
	waitTrue(t, "the generation's end", func() bool { st, _, n := prereqState(e, r); return st == prereqFailed && n == 0 })
	pauseRun(t, e, run.ID, false)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if rows := l.prereqRows(run.ID); len(rows) != 1 || rows[0].K != 2 || rows[0].State != ledger.CheckFinished {
		t.Fatalf("the prerequisites: %+v", rows)
	}
	stopRun(t, e, run.ID)
}

// The sole task cancelled while a prerequisite is held at its gate
// finishes the run, and the commit that finishes it ends the generation
// with no verdict: the prerequisites read interrupted, none in flight. A
// retry reopens the run and runs them again (k 2), and the task is
// dispatched once they pass (the independent review of #422 and Codex's:
// the run stayed open with nothing ever dispatched).
func TestARetryAfterTheRunsEndRunsThePrerequisitesAgain(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead")))
	pidIn(t, pidFile)
	if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	r := runOf(t, e, run.ID)
	waitTrue(t, "the generation's end", func() bool { st, _, n := prereqState(e, r); return st == prereqInterrupted && n == 0 })
	if got, _ := e.Get(run.ID); got.Outcome != ledger.OutcomeCancelled || got.Manifest == nil || got.Manifest.Prerequisites[0].State != "interrupted" {
		t.Fatalf("the finished run: %q %+v", got.Outcome, got.Manifest)
	}
	open(t, gate)
	if err := e.RetryTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	rows := l.prereqRows(run.ID)
	if len(rows) != 2 || rows[0].State != ledger.CheckInterrupted || rows[1].K != 2 || rows[1].State != ledger.CheckFinished || rows[1].Exit != 0 {
		t.Fatalf("the prerequisites: %+v", rows)
	}
	kinds := l.kinds(run.ID)
	if p, a := slices.Index(kinds, "prerequisites.passed"), slices.Index(kinds, "attempt.reserved"); p < 0 || p > a || slices.Index(kinds, "task.retried") > p {
		t.Fatalf("the retry, the prerequisites and the dispatch: %v", kinds)
	}
	stopRun(t, e, run.ID)
}

// A retry that lands after the interrupted generation's last commit and
// before its end starts no second generation beside it; the ending one,
// finding the run reopened, runs them again as it ends, and the task is
// dispatched once they pass.
func TestARetryWhileTheInterruptedGenerationEndsRunsThemAgain(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	ending, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	e.beforePrereqEnd = func() {
		once.Do(func() {
			ending <- struct{}{}
			<-release
		})
	}
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead")))
	pidIn(t, pidFile)
	if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	<-ending
	open(t, gate)
	if err := e.RetryTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	r := runOf(t, e, run.ID)
	if st, gen, n := prereqState(e, r); st != prereqRunning || gen != 1 || n != 1 {
		t.Fatalf("beside the ending generation: %s, generation %d, %d in flight", st, gen, n)
	}
	close(release)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if rows := l.prereqRows(run.ID); len(rows) != 2 || rows[0].State != ledger.CheckInterrupted || rows[1].K != 2 || rows[1].Exit != 0 {
		t.Fatalf("the prerequisites: %+v", rows)
	}
	stopRun(t, e, run.ID)
}

// A continuation of a run whose every task ended is finished by the commit
// that arms its prerequisites, so that generation never begins: it reads
// as one the run's end interrupted, none in flight, the prerequisite
// pending with no receipt, never running. A retry reopens the
// continuation and runs them (k 1), and the task is dispatched once they
// pass (Codex's review of #422, round 3: the generation stayed pending
// with nothing to run it, and the pump, which dispatches only on passed,
// waited for good).
func TestARetryInAContinuationFinishedAtItsStartRunsThePrerequisites(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{gatedPrerequisite("tools", gate, pidFile)}}, work("build", "lead")))
	pidIn(t, pidFile)
	if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	r := runOf(t, e, run.ID)
	waitTrue(t, "the generation's end", func() bool { st, _, n := prereqState(e, r); return st == prereqInterrupted && n == 0 })
	open(t, gate)
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	nr := runOf(t, e, next.ID)
	if st, gen, n := prereqState(e, nr); next.Outcome != ledger.OutcomeCancelled || st != prereqInterrupted || gen != 0 || n != 0 {
		t.Fatalf("the continuation finished at its start: outcome %q, prerequisites %s, generation %d, %d in flight", next.Outcome, st, gen, n)
	}
	if got, _ := e.Get(next.ID); got.Manifest == nil || len(got.Manifest.Prerequisites) != 1 || got.Manifest.Prerequisites[0].State != "pending" || len(l.prereqRows(next.ID)) != 0 {
		t.Fatalf("the finished continuation's prerequisites: %+v, rows %+v", got.Manifest, l.prereqRows(next.ID))
	}
	if err := e.RetryTask(t.Context(), next.ID, "build"); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
	if rows := l.prereqRows(next.ID); len(rows) != 1 || rows[0].K != 1 || rows[0].State != ledger.CheckFinished || rows[0].Exit != 0 {
		t.Fatalf("the continuation's prerequisites after the retry: %+v", rows)
	}
	kinds := l.kinds(next.ID)
	if p, a := slices.Index(kinds, "prerequisites.passed"), slices.Index(kinds, "attempt.reserved"); p < 0 || p > a || slices.Index(kinds, "task.retried") > p {
		t.Fatalf("the retry, the prerequisites and the dispatch: %v", kinds)
	}
	stopRun(t, e, next.ID)
}

// An unpause the ledger refuses while a failed generation is ending starts
// no generation (Codex's review of #422): the generation's end waits for
// the unpause's commit and reads the run as its undo left it, never the
// change the ledger did not take, so no prerequisite runs on it.
func TestARefusedUnpauseWhileAGenerationEndsStartsNothing(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	var mu sync.Mutex
	runs := 0
	native := e.exec.run
	e.exec.run = func(ctx context.Context, ref ext.ProcessRef, s proc.Spec) proc.Result {
		if len(s.Argv) == 3 && s.Argv[2] == "exit 1" {
			mu.Lock()
			runs++
			mu.Unlock()
		}
		return native(ctx, ref, s)
	}
	ending, proceed, resumed := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.beforePrereqEnd = func() {
		once.Do(func() {
			ending <- struct{}{}
			<-proceed
			resumed <- struct{}{}
		})
	}
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("tools", "exit 1")}}, work("build", "lead")))
	<-ending
	waitFake(t, "the pause committed", func() bool { return l.count(run.ID, "run.paused") == 1 })
	// The unpause's change is made and its commit held in the ledger, which
	// then refuses it.
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "run.paused[paused=false]", make(chan struct{}, 1), make(chan struct{})
	l.failOn, l.failOnce = "run.paused[paused=false]", true
	blocked, release := l.blocked, l.release
	l.mu.Unlock()
	unpaused := make(chan error, 1)
	go func() { unpaused <- e.PauseRun(context.Background(), run.ID, false) }()
	<-blocked
	// The generation's end goes on with the unpause's commit under way, and
	// only then does the ledger refuse it.
	close(proceed)
	<-resumed
	close(release)
	if err := <-unpaused; err == nil {
		t.Fatal("the unpause was not refused")
	}
	r := runOf(t, e, run.ID)
	waitTrue(t, "the generation's end", func() bool { _, _, n := prereqState(e, r); return n == 0 })
	if st, gen, _ := prereqState(e, r); st != prereqFailed || gen != 1 {
		t.Fatalf("after the refused unpause: %s, generation %d", st, gen)
	}
	if got, _ := e.Get(run.ID); !got.Paused {
		t.Fatalf("the run after the refused unpause: %+v", got)
	}
	mu.Lock()
	n := runs
	mu.Unlock()
	if rows := l.prereqRows(run.ID); n != 1 || len(rows) != 1 {
		t.Fatalf("%d runs of the prerequisite, rows %+v", n, rows)
	}
	stopRun(t, e, run.ID)
}

// A stale command whose receipt the ledger refuses made no change, so its
// refusal puts nothing back (Codex's review of #422, round 2): the run
// keeps its prerequisites' state, with a project manifest's prerequisite
// or with none, the refusal pauses it as any refused commit does, and an
// unpause after dispatches again.
func TestAStaleCommandWhoseReceiptIsRefusedPutsNothingBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *Manifest
	}{
		{"no manifest", nil},
		{"a prerequisite", &Manifest{Prerequisites: []Check{shStep("tools", "true")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			tasks := []Task{work("build", "lead"), work("test", "lead", "build")}
			c := repoCrew(dir, tasks...)
			if tc.m != nil {
				c = repoManifestCrew(dir, *tc.m, tasks...)
			}
			run := launchManaged(t, e, c)
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			r := runOf(t, e, run.ID)
			ctx := context.Background()
			if _, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p1"}, true); err != nil {
				t.Fatal(err)
			}
			// An unpause expecting a revision the run has passed: refused
			// stale, no change made, and the commit of that refusal's
			// receipt refused by the ledger.
			l.mu.Lock()
			l.failOn, l.failOnce = "command.unpause", true
			l.mu.Unlock()
			if _, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p2", ExpectedRevision: 1}, false); err == nil {
				t.Fatal("a receipt the ledger refused was taken")
			}
			if st, _, _ := prereqState(e, r); st != prereqPassed {
				t.Fatalf("the prerequisites after the refused receipt: %q", st)
			}
			if got, _ := e.Get(run.ID); !got.Paused || !strings.Contains(got.PausedWhy, "disk full") {
				t.Fatalf("the run after the refused receipt: paused %v %q", got.Paused, got.PausedWhy)
			}
			commitResult(t, e, a, "built.txt", "yes\n", "built")
			waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
			if _, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p3"}, false); err != nil {
				t.Fatal(err)
			}
			waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
			stopRun(t, e, run.ID)
		})
	}
}

// A prerequisite's running row the ledger refuses ends its process with
// its group and fails it with the ledger's words, which pause the run
// (Codex's review of #422): nothing runs that the ledger does not record.
// The refused row and its event go with the next commit; unpaused, they
// run again (k 2) and the run goes on.
func TestARefusedRunningRowEndsAndFailsThePrerequisite(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate := filepath.Join(t.TempDir(), "gate")
	l.mu.Lock()
	l.failOn, l.failOnce = "step.running[phase=prerequisite]", true
	l.mu.Unlock()
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("tools", gated(gate))}}, work("build", "lead")))
	words := "the ledger could not record the step tools: ledger: disk full (injected)"
	waitTrue(t, "the run paused", func() bool {
		r, _ := e.Get(run.ID)
		return r.Paused && r.PausedWhy == "the prerequisite tools failed: "+words
	})
	r := runOf(t, e, run.ID)
	waitTrue(t, "the generation's end", func() bool { st, _, n := prereqState(e, r); return st == prereqFailed && n == 0 })
	rows := l.prereqRows(run.ID)
	if len(rows) != 1 || rows[0].State != ledger.CheckFinished || rows[0].Error != words || rows[0].PID == 0 {
		t.Fatalf("the prerequisite's row: %+v", rows)
	}
	if alive(rows[0].PID) {
		t.Fatalf("the prerequisite's process %d outlived its refused row", rows[0].PID)
	}
	if n := l.count(run.ID, "step.running"); n != 1 {
		t.Fatalf("the refused row's event, carried by the next commit: %d in %v", n, l.kinds(run.ID))
	}
	open(t, gate)
	pauseRun(t, e, run.ID, false)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if rows := l.prereqRows(run.ID); len(rows) != 2 || rows[1].K != 2 || rows[1].Exit != 0 || rows[1].Error != "" {
		t.Fatalf("the prerequisites after the unpause: %+v", rows)
	}
	stopRun(t, e, run.ID)
}
