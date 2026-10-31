package crew

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A withdrawal takes back every attempt of every task that rested on the
// withdrawn result (docs/plans/issue-334-withdrawal.md): nothing from
// them is used again, in the run or a continuation of it.

// pauseRun pauses or unpauses the run's dispatch.
func pauseRun(t *testing.T, e *Engine, runID string, paused bool) {
	t.Helper()
	if err := e.PauseRun(context.Background(), runID, paused); err != nil {
		t.Fatal(err)
	}
}

// attemptN waits for the task's attempt n to be running.
func attemptN(t *testing.T, e *Engine, runID, taskID string, n int) TaskState {
	t.Helper()
	return waitTaskWhere(t, e, runID, taskID, "at attempt "+strconv.Itoa(n)+", running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
	})
}

// attemptFailed waits for the task's attempt n to have failed.
func attemptFailed(t *testing.T, e *Engine, runID, taskID string, n int) TaskState {
	t.Helper()
	return waitTaskWhere(t, e, runID, taskID, "at attempt "+strconv.Itoa(n)+", failed", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptFailed
	})
}

// asksChanges submits a review's needs_changes.
func asksChanges(t *testing.T, e *Engine, ts TaskState) {
	t.Helper()
	submit(t, e, ts.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Findings: []Finding{{Severity: "major", Message: "redo it"}}})
}

// approves submits a review's approval.
func approves(t *testing.T, e *Engine, ts TaskState) {
	t.Helper()
	submit(t, e, ts.Attempt.SessionID, Result{Verdict: VerdictApproved})
}

// rebuilt waits for build to be reopened (ready again, with a repair) and
// unpaused makes its attempt 2, commits it and waits for its success:
// the new input.
func rebuilt(t *testing.T, e *Engine, runID string, n int) TaskState {
	t.Helper()
	b := attemptN(t, e, runID, "build", n)
	commitResult(t, e, b, "built.txt", "built again "+strconv.Itoa(n)+"\n", "built again")
	return waitTaskWhere(t, e, runID, "build", "succeeded again", func(ts TaskState) bool {
		return ts.State == ledger.TaskSucceeded && ts.Result != nil && ts.Result.Attempt == n
	})
}

// withdrawalCrew is build, its review check and docs needing build, docs
// failing its check (docs-ok.txt is never made) or, made with broken.txt,
// refused by the crew's setup.
func withdrawalCrew(dir string) Crew {
	docs := work("docs", "writer", "build")
	docs.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test -e docs-ok.txt"}}}
	c := repoCrew(dir, work("build", "lead"), review("check", "critic", "build"), docs)
	c.Setup = []Check{{ID: "deps", Argv: []string{"sh", "-c", "test ! -e broken.txt"}}}
	return c
}

// docsFailsThenBuildIsReopened runs the crew to the withdrawal: build's
// first result; docs' attempt 1 commits, saves a checkpoint and fails
// (its check, or the setup refusing its result) while the run is paused,
// so no attempt 2 starts; then the review asks changes of build, which
// takes docs' attempt 1 back. It returns docs' attempt 1.
func docsFailsThenBuildIsReopened(t *testing.T, e *Engine, runID, refusal string) TaskState {
	t.Helper()
	b1 := waitAttempt(t, e, runID, "build", ledger.AttemptRunning)
	commitResult(t, e, b1, "built.txt", "built\n", "built")
	waitTask(t, e, runID, "build", ledger.TaskSucceeded, 1)
	rv := waitAttempt(t, e, runID, "check", ledger.AttemptRunning)
	d1 := waitAttempt(t, e, runID, "docs", ledger.AttemptRunning)
	pauseRun(t, e, runID, true)
	name := "docs.txt"
	if refusal == "setup" {
		name = "broken.txt"
	}
	writeFile(t, filepath.Join(d1.Attempt.Worktree, name), "docs\n")
	commitAll(t, d1.Attempt.Worktree, "docs")
	if _, err := e.SaveCheckpoint(context.Background(), d1.Attempt.SessionID, "wrote the docs", "nothing", ""); err != nil {
		t.Fatal(err)
	}
	submit(t, e, d1.Attempt.SessionID, Result{Summary: "docs"})
	attemptFailed(t, e, runID, "docs", 1)
	asksChanges(t, e, rv)
	waitTaskWhere(t, e, runID, "docs", "blocked by the withdrawal", func(ts TaskState) bool { return ts.State == ledger.TaskBlocked })
	return d1
}

// startsAtTheNewInput checks docs' attempt starts at build's new result,
// its task file naming no attempt before and no checkpoint.
func startsAtTheNewInput(t *testing.T, d TaskState, b TaskState, d1 TaskState) {
	t.Helper()
	if d.Attempt.Start != b.Result.Commit {
		t.Fatalf("docs' attempt %d starts at %s, not build's new result %s (attempt 1 started at %s)", d.Attempt.N, short(d.Attempt.Start), short(b.Result.Commit), short(d1.Attempt.Start))
	}
	if _, f := taskFileOf(t, d); f.Previous != nil || f.Checkpoint != nil || f.StartFrom != `task "build"'s result` {
		t.Fatalf("docs' task file: starts from %q, the attempt before %+v, checkpoint %+v", f.StartFrom, f.Previous, f.Checkpoint)
	}
}

// In a run: docs' result fails its check (or the setup refuses it), the
// review asks changes of build, build's repair succeeds, and docs' next
// attempt starts at build's new result, not at its own attempt 1's result
// or start, which rested on the result withdrawn.
func TestAWithdrawnDependentStartsAtTheNewInput(t *testing.T) {
	for _, refusal := range []string{"check", "setup"} {
		t.Run(refusal, func(t *testing.T) {
			dir := newRepo(t)
			e, _, _ := managedEngine(t)
			run := launchManaged(t, e, withdrawalCrew(dir))
			d1 := docsFailsThenBuildIsReopened(t, e, run.ID, refusal)
			pauseRun(t, e, run.ID, false)
			b := rebuilt(t, e, run.ID, 2)
			startsAtTheNewInput(t, attemptN(t, e, run.ID, "docs", 2), b, d1)
			stopRun(t, e, run.ID)
		})
	}
}

// The same across continuations, through one that made no attempt: the
// withdrawal is in docs' row, so neither its attempt 1's checkpoint nor
// its refusal comes back.
func TestAWithdrawnDependentStartsAtTheNewInputAcrossContinuations(t *testing.T) {
	for _, refusal := range []string{"check", "setup"} {
		t.Run(refusal, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run := launchManaged(t, e, withdrawalCrew(dir))
			d1 := docsFailsThenBuildIsReopened(t, e, run.ID, refusal)
			stopRun(t, e, run.ID)
			if row := l.tasks[run.ID+"/docs"]; row.Withdrawn != 1 {
				t.Fatalf("docs' row: %+v", row)
			}
			ctx := context.Background()
			e2, _ := readyEngine(t)
			e2.UseLedger(l)
			mid, err := e2.ContinueRun(ctx, run.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			stopRun(t, e2, mid.ID)
			e3, _ := readyEngine(t)
			e3.UseLedger(l)
			next, err := e3.ContinueRun(ctx, mid.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			pauseRun(t, e3, next.ID, false)
			b := rebuilt(t, e3, next.ID, 2)
			startsAtTheNewInput(t, attemptN(t, e3, next.ID, "docs", 2), b, d1)
			stopRun(t, e3, next.ID)
		})
	}
}

// A dependent's own repair is dropped by a withdrawal of its input (its
// review's findings were on the old input); the reopened subject keeps
// its own.
func TestAWithdrawalDropsADependentsRepairAndKeepsTheSubjects(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("rb", "critic", "build"), work("docs", "writer", "build"), review("rd", "editor", "docs")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	rb := waitAttempt(t, e, run.ID, "rb", ledger.AttemptRunning)
	commitResult(t, e, waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning), "docs.txt", "d\n", "docs")
	asksChanges(t, e, waitAttempt(t, e, run.ID, "rd", ledger.AttemptRunning))
	attemptN(t, e, run.ID, "docs", 2)
	// The engine's view is one change ahead of the ledger while that change
	// commits: the task rows are waited for in the ledger.
	l.taskWhen(t, run.ID, "docs", "with a repair after its review", func(r ledger.TaskRow) bool { return r.Repair != "" })
	asksChanges(t, e, rb)
	waitTaskWhere(t, e, run.ID, "docs", "blocked by the withdrawal", func(ts TaskState) bool { return ts.State == ledger.TaskBlocked })
	docs := l.taskWhen(t, run.ID, "docs", "blocked by the withdrawal", func(r ledger.TaskRow) bool { return r.State == ledger.TaskBlocked })
	build := l.taskWhen(t, run.ID, "build", "reopened with a repair", func(r ledger.TaskRow) bool { return r.Repair != "" })
	if docs.Repair != "" || docs.Withdrawn != 2 || build.Repair == "" || build.Withdrawn != 0 {
		t.Fatalf("after the withdrawal: docs %+v, build %+v", docs, build)
	}
	stopRun(t, e, run.ID)
}

// An imported candidate waiting behind the attempt cap when its input is
// withdrawn is never verified: docs' result was being verified when the
// run stopped; the continuation dispatches four attempts ahead of it (the
// cap) and one of them, a review, asks changes of build; once build's
// repair succeeds, docs runs a fresh attempt at the new result.
func TestAWithdrawnCandidateIsNeverVerified(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passed")
	docs := work("docs", "writer", "build")
	// Until the mark exists the check takes long: the run stops during it.
	docs.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test -e " + mark + " || sleep 30"}}}
	// Plan order: the review and three fillers before docs, so the
	// continuation's four attempts are theirs.
	c := repoCrew(dir, review("rb", "critic", "build"), work("f1", "m1"), work("f2", "m2"), work("f3", "m3"), work("build", "lead"), docs)
	run := launchManaged(t, e, c)
	ctx := context.Background()
	// The four wait in this run, cancelled, and are retried in the
	// continuation.
	for _, id := range []string{"rb", "f1", "f2", "f3"} {
		if err := e.CancelTask(ctx, run.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	d1 := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	writeFile(t, filepath.Join(d1.Attempt.Worktree, "docs.txt"), "d\n")
	commitAll(t, d1.Attempt.Worktree, "docs")
	submit(t, e, d1.Attempt.SessionID, Result{Summary: "docs"})
	verifying := waitAttempt(t, e, run.ID, "docs", ledger.AttemptVerifying)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if row := l.attemptRow(t, run.ID, "docs", 1); row.State == ledger.AttemptVerifying && row.Result != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the result was not recorded as verifying")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pauseRun(t, e, run.ID, true)
	stopRun(t, e, run.ID)
	interrupt(l, run.ID, map[string][2]string{"docs/1": {ledger.AttemptInterrupted, "verifying: the owner stopped; the candidate is verified again"}})
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"rb", "f1", "f2", "f3"} {
		if err := e2.RetryTask(ctx, next.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	pauseRun(t, e2, next.ID, false)
	rb := waitAttempt(t, e2, next.ID, "rb", ledger.AttemptRunning)
	for _, id := range []string{"f1", "f2", "f3"} {
		waitAttempt(t, e2, next.ID, id, ledger.AttemptRunning)
	}
	if d := taskOf(t, e2, next.ID, "docs"); d.State != ledger.TaskReady || d.Attempt != nil {
		t.Fatalf("docs is not waiting behind the cap: %+v %+v", d, d.Attempt)
	}
	asksChanges(t, e2, rb)
	// The fillers done, docs is not held behind the cap any more.
	for _, id := range []string{"f1", "f2", "f3"} {
		commitResult(t, e2, waitAttempt(t, e2, next.ID, id, ledger.AttemptRunning), id+".txt", "f\n", id)
	}
	b := rebuilt(t, e2, next.ID, 2)
	d2 := attemptN(t, e2, next.ID, "docs", 2)
	if d2.Attempt.N != 2 || d2.Attempt.SessionID == "" || d2.Attempt.Start != b.Result.Commit || d2.Attempt.Result == verifying.Attempt.Result {
		t.Fatalf("docs after the withdrawal: %+v", d2.Attempt)
	}
	for _, kind := range l.kinds(next.ID) {
		if kind == "result.reverified" {
			t.Fatalf("the withdrawn candidate was verified: %v", l.kinds(next.ID))
		}
	}
	stopRun(t, e2, next.ID)
}

// A dependent that failed its attempts before the withdrawal stays failed
// with them taken back: Accept anyway finds no result since, and a retry
// starts at the new input, whose result Accept anyway then takes.
func TestAFailedDependentStaysFailedAndARetryStartsAtTheNewInput(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, withdrawalCrew(dir))
	ctx := context.Background()
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	rv := waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	// Each failure starts the next attempt, until none is left.
	for n := 1; n <= maxAttemptsPerTask; n++ {
		commitResult(t, e, attemptN(t, e, run.ID, "docs", n), "docs.txt", "docs "+strconv.Itoa(n)+"\n", "docs")
	}
	waitTask(t, e, run.ID, "docs", ledger.TaskFailed, maxAttemptsPerTask)
	asksChanges(t, e, rv)
	b := rebuilt(t, e, run.ID, 2)
	if d := taskOf(t, e, run.ID, "docs"); d.State != ledger.TaskFailed {
		t.Fatalf("docs after the withdrawal: %+v", d)
	}
	if _, err := e.Accept(ctx, run.ID, "admin", Command{ID: "accept-1"}, "docs"); !errors.Is(err, ErrTaskState) || !strings.Contains(err.Error(), "since the withdrawal") {
		t.Fatalf("Accept anyway of a withdrawn result: %v", err)
	}
	if err := e.RetryTask(ctx, run.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	d := attemptN(t, e, run.ID, "docs", maxAttemptsPerTask+1)
	if d.Attempt.Start != b.Result.Commit {
		t.Fatalf("the retry starts at %s, not build's new result %s", short(d.Attempt.Start), short(b.Result.Commit))
	}
	if _, f := taskFileOf(t, d); f.Previous != nil {
		t.Fatalf("the retry's task file names the attempt before: %+v", f.Previous)
	}
	commitResult(t, e, d, "docs.txt", "docs again\n", "docs again")
	failed := attemptFailed(t, e, run.ID, "docs", maxAttemptsPerTask+1)
	if _, err := e.Accept(ctx, run.ID, "admin", Command{ID: "accept-2"}, "docs"); err != nil {
		t.Fatal(err)
	}
	if got := taskOf(t, e, run.ID, "docs"); got.State != ledger.TaskSucceeded || got.Result == nil || got.Result.Attempt != maxAttemptsPerTask+1 || got.Result.Commit != failed.Attempt.Result {
		t.Fatalf("docs accepted: %+v %+v", got, got.Result)
	}
	stopRun(t, e, run.ID)
}

// An imported review withdrawn in the continuation, re-run to success
// and continued again: its result is this run's (no import kept), and a
// dependent of it starts at the newly reviewed commit, not the old one,
// even from a row that still names the old reference.
func TestAWithdrawnImportedReviewIsTakenFromWhereItWasRemade(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("rb", "critic", "build"), review("rb2", "critic2", "build"), work("site", "web", "rb", "rb2")))
	ctx := context.Background()
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	approves(t, e, waitAttempt(t, e, run.ID, "rb", ledger.AttemptRunning))
	waitTask(t, e, run.ID, "rb", ledger.TaskSucceeded, 1)
	waitAttempt(t, e, run.ID, "rb2", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	mid, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if rb := taskOf(t, e2, mid.ID, "rb"); rb.ImportedFrom != run.ID+"/rb/1" {
		t.Fatalf("rb in the continuation: %+v", rb)
	}
	asksChanges(t, e2, attemptN(t, e2, mid.ID, "rb2", 2))
	b := rebuilt(t, e2, mid.ID, 2)
	rb := attemptN(t, e2, mid.ID, "rb", 2)
	rb2 := attemptN(t, e2, mid.ID, "rb2", 3)
	pauseRun(t, e2, mid.ID, true)
	approves(t, e2, rb)
	approves(t, e2, rb2)
	if got := waitTask(t, e2, mid.ID, "rb", ledger.TaskSucceeded, 2); got.ImportedFrom != "" {
		t.Fatalf("rb's result made in this run is still imported: %+v", got)
	}
	waitTask(t, e2, mid.ID, "rb2", ledger.TaskSucceeded, 3)
	stopRun(t, e2, mid.ID)
	// As version 3 left it: the old reference kept beside the new result.
	// The continuation finds the result where it was made.
	l.mu.Lock()
	row := l.tasks[mid.ID+"/rb"]
	row.ImportedFrom = run.ID + "/rb/1"
	l.tasks[mid.ID+"/rb"] = row
	l.mu.Unlock()
	e3, _ := readyEngine(t)
	e3.UseLedger(l)
	next, err := e3.ContinueRun(ctx, mid.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := taskOf(t, e3, next.ID, "rb"); got.ImportedFrom != mid.ID+"/rb/2" {
		t.Fatalf("rb in the last continuation: %+v", got)
	}
	pauseRun(t, e3, next.ID, false)
	if s := attemptN(t, e3, next.ID, "site", 1); s.Attempt.Start != b.Result.Commit {
		t.Fatalf("site starts at %s, not the commit rb reviewed last (%s)", short(s.Attempt.Start), short(b.Result.Commit))
	}
	stopRun(t, e3, next.ID)
}

// A run once continued takes no change of its tasks or its pause: its
// continuation owns that history. The refusal holds from the moment the
// continuation is being made: a retry under way when it starts commits
// first, and the continuation reads it.
func TestAContinuedRunTakesNoChange(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer"), work("site", "web")))
	ctx := context.Background()
	for _, id := range []string{"docs", "site"} {
		if err := e.CancelTask(ctx, run.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
	// A retry whose commit waits while the continuation starts.
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "task.retried", make(chan struct{}, 1), make(chan struct{})
	l.mu.Unlock()
	retried := make(chan error, 1)
	go func() { retried <- e.RetryTask(ctx, run.ID, "docs") }()
	<-l.blocked
	marked := make(chan struct{})
	var once sync.Once
	var during error
	e.beforeContinue = func(string) {
		once.Do(func() {
			// Being continued: marked, not continued yet. A change now is
			// refused too.
			during = e.RetryTask(ctx, run.ID, "site")
			close(marked)
		})
	}
	continued := make(chan *Run, 1)
	go func() {
		next, err := e.ContinueRun(ctx, run.ID, "")
		if err != nil {
			t.Error(err)
		}
		continued <- next
	}()
	// Given a moment to run ahead (it must not: the retry holds the run).
	select {
	case <-marked:
	case <-time.After(500 * time.Millisecond):
	}
	close(l.release)
	if err := <-retried; err != nil {
		t.Fatal(err)
	}
	next := <-continued
	if next == nil {
		t.FailNow()
	}
	if d := taskOf(t, e, next.ID, "docs"); d.State == ledger.TaskCancelled {
		t.Fatalf("the continuation did not read the retry that committed before it: %+v", d)
	}
	if !errors.Is(during, ErrRunContinued) {
		t.Fatalf("a retry while the run was being continued: %v", during)
	}
	// Continued: every change is refused, with a receipt that says so.
	if err := e.RetryTask(ctx, run.ID, "site"); !errors.Is(err, ErrRunContinued) {
		t.Fatalf("a retry of the continued run: %v", err)
	}
	for i, do := range []func() (Receipt, error){
		func() (Receipt, error) { return e.Retry(ctx, run.ID, "admin", Command{ID: "r1"}, "docs") },
		func() (Receipt, error) { return e.Cancel(ctx, run.ID, "admin", Command{ID: "c1"}, "docs") },
		func() (Receipt, error) { return e.Accept(ctx, run.ID, "admin", Command{ID: "a1"}, "docs") },
		func() (Receipt, error) { return e.Pause(ctx, run.ID, "admin", Command{ID: "p1"}, true) },
	} {
		if rc, err := do(); !errors.Is(err, ErrRunContinued) || rc.Refusal != RefusalRunContinued {
			t.Fatalf("command %d on the continued run: %+v %v", i, rc, err)
		}
	}
	stopRun(t, e, next.ID)
}

// A ledger whose ancestor was given an attempt after it was continued
// (version 3 let a retry land there) is not taken past the attempts the
// continuation's row counts: the continuation of a continuation that
// made no attempt starts docs at its own checkpoint, not the later one.
func TestAContinuationTakesNoAncestorAttemptPastTheRowsCount(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("docs", "writer")))
	ctx := context.Background()
	d1 := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	writeFile(t, filepath.Join(d1.Attempt.Worktree, "half.txt"), "half\n")
	commitAll(t, d1.Attempt.Worktree, "half")
	if _, err := e.SaveCheckpoint(ctx, d1.Attempt.SessionID, "half", "the rest", ""); err != nil {
		t.Fatal(err)
	}
	kept := l.attemptRow(t, run.ID, "docs", 1)
	pauseRun(t, e, run.ID, true)
	stopRun(t, e, run.ID)
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	mid, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, e2, mid.ID)
	// The ancestor given attempt 2 after the continuation, its checkpoint
	// at the run's base.
	l.mu.Lock()
	base := l.runs[run.ID].Base
	later := kept
	later.N, later.Checkpoint = 2, `{"done":"later","remaining":"","commit":"`+base+`","at":"2026-10-25T00:00:00Z"}`
	l.attempts[run.ID+"/docs/2"] = later
	l.mu.Unlock()
	if mid.ID == "" || l.tasks[mid.ID+"/docs"].Attempts != 1 {
		t.Fatalf("the continuation's docs row: %+v", l.tasks[mid.ID+"/docs"])
	}
	e3, _ := readyEngine(t)
	e3.UseLedger(l)
	next, err := e3.ContinueRun(ctx, mid.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	pauseRun(t, e3, next.ID, false)
	d2 := attemptN(t, e3, next.ID, "docs", 2)
	if want := decodeCheckpoint(kept.Checkpoint).commit; d2.Attempt.Start != want {
		t.Fatalf("docs' attempt 2 starts at %s, not its attempt 1's checkpoint %s (the later one was at %s)", short(d2.Attempt.Start), short(want), short(base))
	}
	stopRun(t, e3, next.ID)
}

// A withdrawal visits each task once: a dense graph, every task needing
// build and every task before it, is walked once, not once per path.
func TestAWithdrawalVisitsEachTaskOnce(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	tasks := []Task{work("build", "lead"), review("rb", "critic", "build")}
	var ids []string
	for i := range 16 {
		id := "t" + string(rune('a'+i))
		tk := work(id, "w", append([]string{"build"}, ids...)...)
		tasks = append(tasks, tk)
		ids = append(ids, id)
	}
	var mu sync.Mutex
	visits := map[string]int{}
	e.afterVisit = func(id string) {
		mu.Lock()
		visits[id]++
		mu.Unlock()
	}
	run := launchManaged(t, e, repoCrew(dir, tasks...))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	asksChanges(t, e, waitAttempt(t, e, run.ID, "rb", ledger.AttemptRunning))
	waitTaskWhere(t, e, run.ID, "build", "reopened", func(ts TaskState) bool { return ts.Result == nil })
	mu.Lock()
	defer mu.Unlock()
	for _, id := range append([]string{"rb"}, ids...) {
		if visits[id] != 1 {
			t.Fatalf("visits: %v", visits)
		}
	}
	stopRun(t, e, run.ID)
}

// A version 3 ledger (no withdrawn column) is migrated with the
// conservative backfill and continued: docs' attempt, taken back by the
// review's reopening of build, is not reused, its checkpoint neither. The
// reopen's event is gone too (a failed commit could save the rows without
// it): the review attempt's verdict says it. In the second case version 3
// had continued the run and started docs' attempt 2 at that withdrawn
// checkpoint, saving one of its own there (Codex's review): the
// continuation's attempt is taken back as well.
func TestAMigratedVersion3LedgerReusesNothingWithdrawn(t *testing.T) {
	for _, chained := range []bool{false, true} {
		t.Run(map[bool]string{false: "the run", true: "its version 3 continuation"}[chained], func(t *testing.T) {
			dir := newRepo(t)
			e, _ := readyEngine(t)
			ldir := t.TempDir()
			ld, err := ledger.Open(ldir)
			if err != nil {
				t.Fatal(err)
			}
			e.UseLedger(SQLLedger(ld))
			run := launchManaged(t, e, withdrawalCrew(dir))
			d1 := docsFailsThenBuildIsReopened(t, e, run.ID, "check")
			stopRun(t, e, run.ID)
			ctx := context.Background()
			from := run.ID
			if chained {
				from = v3Continuation(t, ld, run.ID)
			}
			ld.Close()
			// As a version 3 Conductor left it: no withdrawn column.
			db, err := sql.Open("sqlite", filepath.Join(ldir, ledger.File))
			if err != nil {
				t.Fatal(err)
			}
			// What versions 4 and 5 added dropped (the ledger package's own
			// tests keep the same list, version5Added).
			v5 := []string{"ALTER TABLE checks DROP COLUMN phase", "ALTER TABLE checks DROP COLUMN manifest", "ALTER TABLE checks DROP COLUMN ports", "ALTER TABLE checks DROP COLUMN tries",
				"ALTER TABLE checks DROP COLUMN error", "ALTER TABLE checks DROP COLUMN url", "ALTER TABLE runs DROP COLUMN paused_why", "ALTER TABLE runs DROP COLUMN manifest", "DROP TABLE prerequisites", "DROP TABLE trees"}
			for _, stmt := range append(v5, "ALTER TABLE tasks DROP COLUMN withdrawn", "DELETE FROM events WHERE kind = 'task.reopened'", "UPDATE schema_version SET version = 3") {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			ld2, err := ledger.Open(ldir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ld2.Close() })
			want := map[string]int{"build": 0, "check": 1, "docs": 1}
			if chained {
				want["docs"] = 2
			}
			if rows, err := ld2.Tasks(ctx, from); err != nil || len(rows) != 3 {
				t.Fatalf("tasks after the migration: %v %v", rows, err)
			} else {
				for _, r := range rows {
					if r.Withdrawn != want[r.ID] {
						t.Fatalf("task %s migrated withdrawn %d, want %d", r.ID, r.Withdrawn, want[r.ID])
					}
				}
			}
			e2, _ := readyEngine(t)
			e2.UseLedger(SQLLedger(ld2))
			next, err := e2.ContinueRun(ctx, from, "")
			if err != nil {
				t.Fatal(err)
			}
			pauseRun(t, e2, next.ID, false)
			b := rebuilt(t, e2, next.ID, 2)
			startsAtTheNewInput(t, attemptN(t, e2, next.ID, "docs", want["docs"]+1), b, d1)
			stopRun(t, e2, next.ID)
		})
	}
}

// v3Continuation writes the continuation of runID as version 3 made it:
// paused, every task as the run left it but docs, whose attempt 2 started
// at its attempt 1's checkpoint (withdrawn, which version 3 did not know)
// and saved a checkpoint there before the continuation stopped.
func v3Continuation(t *testing.T, ld *ledger.Ledger, runID string) string {
	t.Helper()
	ctx := context.Background()
	src, _, err := ld.Run(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := ld.Tasks(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := ld.Attempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	cp := byN(attempts, "docs", 1).Checkpoint
	at := decodeCheckpoint(cp)
	if at == nil {
		t.Fatalf("docs' attempt 1 has no checkpoint: %+v", byN(attempts, "docs", 1))
	}
	id := runID[:strings.LastIndexByte(runID, '-')] + "-0000000b"
	if _, err := ld.Transition(ctx, id, func(tx *ledger.Tx) error {
		if err := tx.PutRun(ledger.RunRow{ID: id, CrewID: src.CrewID, Crew: src.Crew, Base: src.Base, State: ledger.RunStopped, Paused: true, ContinuedFrom: runID, CreatedAt: tx.Now(), ClosedAt: tx.Now()}); err != nil {
			return err
		}
		for _, row := range tasks {
			row.RunID, row.Withdrawn = id, 0
			if row.ID == "docs" {
				row.State, row.Reason, row.Attempts = ledger.TaskReady, "", 2
			}
			if err := tx.PutTask(row); err != nil {
				return err
			}
		}
		if err := tx.PutAttempt(ledger.AttemptRow{RunID: id, TaskID: "docs", N: 2, Member: "writer", State: ledger.AttemptStopped, StartCommit: at.commit, ReservedAt: tx.Now(), ClosedAt: tx.Now(), Checkpoint: cp}); err != nil {
			return err
		}
		return tx.Continued(runID)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A result whose attempt the ledger does not have is not imported: the
// task fails, for a person to retry, never a success with nothing behind
// it, and what needs it waits.
func TestAResultWhoseAttemptIsGoneIsNotImported(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	pauseRun(t, e, run.ID, true)
	commitResult(t, e, b, "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
	l.mu.Lock()
	delete(l.attempts, run.ID+"/build/1")
	l.mu.Unlock()
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := taskOf(t, e2, next.ID, "build"); got.State != ledger.TaskFailed || got.Result != nil || !strings.Contains(got.Reason, "its result's attempt 1 is not in the ledger") {
		t.Fatalf("build in the continuation: %+v", got)
	}
	if got := taskOf(t, e2, next.ID, "docs"); got.State != ledger.TaskBlocked {
		t.Fatalf("docs in the continuation: %+v", got)
	}
	stopRun(t, e2, next.ID)
}

// A change that looked its run up before the engine let the run go
// (evicted, idle and stopped) is refused, nothing changed: the run could
// be continued from the ledger meanwhile, with no mark on it (Codex's
// review). The command and the direct retry both.
func TestAChangeOfARunLetGoIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	launched := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	ctx := context.Background()
	if err := e.CancelTask(ctx, launched.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	commitResult(t, e, waitAttempt(t, e, launched.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	waitTask(t, e, launched.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, launched.ID)
	held := func() *run {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.runs[launched.ID]
	}()
	if held == nil {
		t.Fatal("the run is not held")
	}
	// Let go once looked up, as evict does; put back for the next case.
	e.afterLookup = func(string) {
		e.mu.Lock()
		e.forget(held)
		e.mu.Unlock()
	}
	putBack := func() {
		e.mu.Lock()
		e.runs[held.id] = held
		e.order = append(e.order, held)
		e.mu.Unlock()
	}
	if rc, err := e.Retry(ctx, launched.ID, "admin", Command{ID: "r1"}, "docs"); !errors.Is(err, ErrRunNotFound) || rc.Refusal != RefusalNotFound {
		t.Fatalf("a command on a run let go: %+v %v", rc, err)
	}
	putBack()
	if err := e.RetryTask(ctx, launched.ID, "docs"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("a retry of a run let go: %v", err)
	}
	if row := l.tasks[launched.ID+"/docs"]; row.State != ledger.TaskCancelled {
		t.Fatalf("docs changed after its run was let go: %+v", row)
	}
}

// A result whose attempt is withdrawn in its row (a version 3 ledger,
// migrated) is not imported: the task is taken up as new, at its input.
func TestAWithdrawnResultIsNotImported(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	b := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	commitResult(t, e, waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning), "docs.txt", "d\n", "docs")
	d := waitTask(t, e, run.ID, "docs", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
	l.mu.Lock()
	row := l.tasks[run.ID+"/docs"]
	row.Withdrawn = 1
	l.tasks[run.ID+"/docs"] = row
	l.mu.Unlock()
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := taskOf(t, e2, next.ID, "build"); got.State != ledger.TaskSucceeded || got.ImportedFrom != run.ID+"/build/1" {
		t.Fatalf("build in the continuation: %+v", got)
	}
	d2 := attemptN(t, e2, next.ID, "docs", 2)
	if d2.Result != nil || d2.Attempt.Start != b.Result.Commit || d2.Attempt.Start == d.Result.Commit {
		t.Fatalf("docs in the continuation: %+v %+v", d2, d2.Attempt)
	}
	stopRun(t, e2, next.ID)
}

// A run whose change the ledger refused, and refused again as the next
// commit carried it, is not continued while it holds that change: the
// continuation would read the run without it, and a later commit of the
// source would write it after (Codex's review). Once the ledger takes
// writes again, the continuation writes it first and reads it.
func TestARunWithChangesTheLedgerRefusedIsNotContinued(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	ctx := context.Background()
	if err := e.CancelTask(ctx, run.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
	// The disk is full: the retry's commit fails, and so does every commit
	// carrying it.
	l.mu.Lock()
	l.failOn = "task.retried"
	l.mu.Unlock()
	if err := e.RetryTask(ctx, run.ID, "docs"); err == nil {
		t.Fatal("the retry committed on a full disk")
	}
	if _, err := e.ContinueRun(ctx, run.ID, ""); !errors.Is(err, ErrRunUnsettled) {
		t.Fatalf("a continuation of a run holding a refused change: %v", err)
	}
	l.mu.Lock()
	l.failOn = ""
	row := l.tasks[run.ID+"/docs"]
	l.mu.Unlock()
	if row.State != ledger.TaskCancelled {
		t.Fatalf("docs' row before the ledger recovered: %+v", row)
	}
	next, err := e.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if row := l.tasks[run.ID+"/docs"]; row.State == ledger.TaskCancelled {
		t.Fatalf("the retry was not written before the continuation: %+v", row)
	}
	if d := taskOf(t, e, next.ID, "docs"); d.State == ledger.TaskCancelled {
		t.Fatalf("the continuation read the run without its retry: %+v", d)
	}
	stopRun(t, e, next.ID)
}

// An ancestor the ledger cannot read ends the continuation before it is
// made (Codex's review): read without it, docs, its last attempt there
// (the middle run made none), would start at the base, not its
// checkpoint, and the link made would leave no second try. Once the
// ledger reads again, the continuation is made and starts docs at its
// checkpoint.
func TestAContinuationIsNotMadeWhenAnAncestorCannotBeRead(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("docs", "writer")))
	ctx := context.Background()
	d1 := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	writeFile(t, filepath.Join(d1.Attempt.Worktree, "half.txt"), "half\n")
	commitAll(t, d1.Attempt.Worktree, "half")
	if _, err := e.SaveCheckpoint(ctx, d1.Attempt.SessionID, "half", "the rest", ""); err != nil {
		t.Fatal(err)
	}
	cp := decodeCheckpoint(l.attemptRow(t, run.ID, "docs", 1).Checkpoint).commit
	pauseRun(t, e, run.ID, true)
	stopRun(t, e, run.ID)
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	mid, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, e2, mid.ID)
	e3, _ := readyEngine(t)
	e3.UseLedger(l)
	l.mu.Lock()
	l.failLoad = run.ID
	runs := len(l.runs)
	l.mu.Unlock()
	if _, err := e3.ContinueRun(ctx, mid.ID, ""); err == nil || !strings.Contains(err.Error(), "could not read run "+run.ID) {
		t.Fatalf("a continuation with its ancestor unread: %v", err)
	}
	// Refused with nothing made: no run in the ledger or the engine, no link.
	l.mu.Lock()
	after, row := len(l.runs), l.runs[mid.ID]
	l.mu.Unlock()
	if after != runs || row.ContinuedBy != "" || len(e3.List()) != 0 {
		t.Fatalf("the refused continuation left %d runs (was %d), the source %+v, the engine's %v", after, runs, row, e3.List())
	}
	next, err := e3.ContinueRun(ctx, mid.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	pauseRun(t, e3, next.ID, false)
	if d2 := attemptN(t, e3, next.ID, "docs", 2); d2.Attempt.Start != cp {
		t.Fatalf("docs' attempt 2 starts at %s, not its checkpoint %s", short(d2.Attempt.Start), short(cp))
	}
	stopRun(t, e3, next.ID)
}

// The run an imported result was made in, unread, ends the continuation
// too: the result is not taken as gone (its task failed for a retry).
// The walk back reads that run first (an imported task has no attempt in
// the run continued), so it is the walk that refuses; the import's own
// read refuses the same way, should it be the first to read a run.
func TestAContinuationIsNotMadeWhenAnImportsRunCannotBeRead(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	ctx := context.Background()
	if err := e.CancelTask(ctx, run.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	mid, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, e2, mid.ID)
	e3, _ := readyEngine(t)
	e3.UseLedger(l)
	l.mu.Lock()
	l.failLoad = run.ID
	l.mu.Unlock()
	if _, err := e3.ContinueRun(ctx, mid.ID, ""); err == nil || !strings.Contains(err.Error(), "could not read run "+run.ID) {
		t.Fatalf("a continuation with its import's run unread: %v", err)
	}
	next, err := e3.ContinueRun(ctx, mid.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if b := taskOf(t, e3, next.ID, "build"); b.State != ledger.TaskSucceeded || b.ImportedFrom != run.ID+"/build/1" {
		t.Fatalf("build in the continuation: %+v", b)
	}
	stopRun(t, e3, next.ID)
}
