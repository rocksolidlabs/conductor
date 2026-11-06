package crew

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Check again (docs/plans/review-repair.md, RV1 PR-2a): a failed task's
// standing failed candidate verified again as a new attempt of its own.

// markedCheck is a check that fails until mark exists: a file outside the
// repository the test owns, made when the check is to pass.
func markedCheck(id, mark string) Check {
	return Check{ID: id, Argv: []string{"sh", "-c", "test -e " + mark}}
}

// spend has task's attempts each commit a result of its own until the
// task fails with every attempt spent, and returns it as failed.
func spend(t *testing.T, e *Engine, runID, taskID, file string) TaskState {
	t.Helper()
	for n := 1; n <= maxAttemptsPerTask; n++ {
		a := waitTaskWhere(t, e, runID, taskID, fmt.Sprintf("at attempt %d, running", n), func(ts TaskState) bool {
			return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
		})
		commitResult(t, e, a, file, strings.Repeat("yes\n", n), fmt.Sprintf("attempt %d", n))
	}
	failed := waitTask(t, e, runID, taskID, ledger.TaskFailed, maxAttemptsPerTask)
	if failed.Attempt == nil || failed.Attempt.N != maxAttemptsPerTask || failed.Attempt.Result == "" {
		t.Fatalf("the failed task: %+v %+v", failed, failed.Attempt)
	}
	return failed
}

// recheck is Check again of the task's attempt n at commit, as admin.
func recheck(e *Engine, runID, id, taskID string, n int, commit string) (Receipt, error) {
	return e.Recheck(context.Background(), runID, "admin", Command{ID: id}, RecheckRequest{Task: taskID, Attempt: n, Commit: commit})
}

// receiptWords is an attempt's receipts as the fake ledger last has them,
// in words: each one's identity, subject and outcome.
func receiptWords(l *fakeLedger, runID, taskID string, n int) string {
	var out []string
	for _, c := range l.stepRows(runID, taskID, n) {
		out = append(out, fmt.Sprintf("%s/%d/%s/%s/%d %s %s exit %d", c.RunID, c.N, c.Phase, c.ID, c.K, c.Subject, c.State, c.Exit))
	}
	return strings.Join(out, "\n")
}

// mustMark makes the file a marked check waits for.
func mustMark(t *testing.T, mark string) {
	t.Helper()
	if err := os.WriteFile(mark, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A failed candidate checked again is a new attempt of its own at the same
// commit, with no session: failing, the task fails again at it; passing,
// the task succeeds with its result. The candidate's own receipts never
// change.
func TestRecheckVerifiesTheFailedCandidateAgain(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", mark)}
	run := launchManaged(t, e, repoCrew(dir, build))
	failed := spend(t, e, run.ID, "build", "built.txt")
	c3, start3 := failed.Attempt.Result, failed.Attempt.Start
	l.attemptWhen(t, run.ID, "build", 3, failedRow)
	third := receiptWords(l, run.ID, "build", 3)
	sessions := len(fl.launched())

	// Failing again: attempt 4 at c3, failed at the check, its receipt its own.
	rc, err := recheck(e, run.ID, "r1", "build", 3, c3)
	if err != nil || rc.Status != ledger.CommandDone || rc.Task == nil || rc.Task.State != ledger.TaskVerifying || rc.Task.Attempts != 4 {
		t.Fatalf("Check again: %+v %v", rc, err)
	}
	again := waitTask(t, e, run.ID, "build", ledger.TaskFailed, 4)
	if a := again.Attempt; a.N != 4 || a.SessionID != "" || a.Result != c3 || a.Start != start3 || a.Stage != "check test" {
		t.Fatalf("attempt 4: %+v", a)
	}
	if row := l.attemptWhen(t, run.ID, "build", 4, failedRow); row.ResultCommit != c3 || row.SessionID != "" || row.Stage != "check test" {
		t.Fatalf("attempt 4's row: %+v", row)
	}
	if got, want := receiptWords(l, run.ID, "build", 4), fmt.Sprintf("%s/4/check/test/1 %s finished exit 1", run.ID, c3); got != want {
		t.Fatalf("attempt 4's receipts:\n%s\nwant:\n%s", got, want)
	}
	if row := l.taskWhen(t, run.ID, "build", "failed at 4", func(r ledger.TaskRow) bool { return r.State == ledger.TaskFailed && r.Attempts == 4 }); row.Extra != 1 {
		t.Fatalf("the task's row: %+v", row)
	}

	// Passing: attempt 5 at c3 is the task's result; what attempts 3 and 4 kept is theirs.
	mustMark(t, mark)
	if rc, err := recheck(e, run.ID, "r2", "build", 4, c3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again of attempt 4: %+v %v", rc, err)
	}
	passed := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 5)
	if passed.Result == nil || passed.Result.Attempt != 5 || passed.Result.Commit != c3 || passed.Result.Summary != "attempt 3" {
		t.Fatalf("the result: %+v", passed.Result)
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	l.attemptWhen(t, run.ID, "build", 5, succeededRow)
	if got, want := receiptWords(l, run.ID, "build", 5), fmt.Sprintf("%s/5/check/test/1 %s finished exit 0", run.ID, c3); got != want {
		t.Fatalf("attempt 5's receipts:\n%s\nwant:\n%s", got, want)
	}
	if got := receiptWords(l, run.ID, "build", 3); got != third {
		t.Fatalf("attempt 3's receipts changed:\n%s\nwere:\n%s", got, third)
	}
	if got := receiptWords(l, run.ID, "build", 4); !strings.HasSuffix(got, "exit 1") {
		t.Fatalf("attempt 4's receipts changed: %s", got)
	}
	if n := len(fl.launched()); n != sessions {
		t.Fatalf("Check again started sessions: %d, were %d", n, sessions)
	}
	payloads := l.payloads(run.ID, "result.reverified")
	if len(payloads) != 2 || payloads[0]["attempt"] != 4 || payloads[0]["of"] != 3 || payloads[0]["commit"] != c3 || payloads[1]["attempt"] != 5 || payloads[1]["of"] != 4 {
		t.Fatalf("the result.reverified events: %+v", payloads)
	}
	stopRun(t, e, run.ID)
}

// runRowWhen waits for the run's row in the fake ledger to satisfy ok.
func (l *fakeLedger) runRowWhen(t *testing.T, runID, what string, ok func(ledger.RunRow) bool) ledger.RunRow {
	t.Helper()
	var row ledger.RunRow
	waitFake(t, "the run's row "+what, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		row = l.runs[runID]
		return ok(row)
	})
	return row
}

// A run its one task's spent attempts finished failed is open again once
// Check again is recorded, in the engine and in its row, and finishes as
// the attempt checked again decides; a Check again whose commit is refused
// leaves it finished failed.
func TestRecheckReopensAFinishedRun(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", mark)}
	run := launchManaged(t, e, repoCrew(dir, build))
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	l.runRowWhen(t, run.ID, "finished failed", func(r ledger.RunRow) bool {
		return r.State == ledger.RunFinished && r.Outcome == ledger.OutcomeFailed
	})

	// Refused by the ledger: the run stays finished failed.
	l.mu.Lock()
	l.failOn, l.failOnce = "command.recheck", true
	l.mu.Unlock()
	if _, err := recheck(e, run.ID, "r0", "build", 3, c3); err == nil {
		t.Fatal("a Check again the ledger refused succeeded")
	}
	if r, _ := e.Get(run.ID); r.State != RunFinished || r.Outcome != ledger.OutcomeFailed {
		t.Fatalf("the run after the refused commit: %s %q", r.State, r.Outcome)
	}
	if row := l.runRowWhen(t, run.ID, "paused by the refusal", func(r ledger.RunRow) bool { return r.Paused }); row.State != ledger.RunFinished || row.Outcome != ledger.OutcomeFailed {
		t.Fatalf("the run's row after the refused commit: %+v", row)
	}

	// Failing again: open while attempt 4 is verified, finished failed again.
	rc, err := recheck(e, run.ID, "r1", "build", 3, c3)
	if err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again: %+v %v", rc, err)
	}
	if r, _ := e.Get(run.ID); r.State != RunRunning || r.Outcome != "" {
		t.Fatalf("the run once Check again is recorded: %s %q", r.State, r.Outcome)
	}
	l.mu.Lock()
	row := l.runs[run.ID]
	l.mu.Unlock()
	if row.State != ledger.RunRunning || row.Outcome != "" || !row.ClosedAt.IsZero() {
		t.Fatalf("the run's row once Check again is recorded: %+v", row)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, 4)
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	l.runRowWhen(t, run.ID, "finished failed again", func(r ledger.RunRow) bool {
		return r.State == ledger.RunFinished && r.Outcome == ledger.OutcomeFailed
	})

	// Passing: open, then succeeded.
	mustMark(t, mark)
	if rc, err := recheck(e, run.ID, "r2", "build", 4, c3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again of attempt 4: %+v %v", rc, err)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 5)
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	l.runRowWhen(t, run.ID, "finished succeeded", func(r ledger.RunRow) bool {
		return r.State == ledger.RunFinished && r.Outcome == ledger.OutcomeSucceeded
	})
	stopRun(t, e, run.ID)
}

// Check again is bound to the candidate the person named: one the task
// moved on from is refused (stale_candidate) and nothing is checked; the
// same command id naming another candidate is a conflict; the same command
// sent again is its receipt again, never a second check.
func TestRecheckIsBoundToTheChosenCandidate(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", mark)}
	run := launchManaged(t, e, repoCrew(dir, build))
	failed := spend(t, e, run.ID, "build", "built.txt")
	c3 := failed.Attempt.Result
	// Another commit, the task's attempt 2's: not the standing candidate.
	c2 := l.attemptRow(t, run.ID, "build", 2).ResultCommit
	if rc, err := recheck(e, run.ID, "r0", "build", 2, c2); !errors.Is(err, ErrStaleCandidate) || rc.Refusal != RefusalStaleCandidate || rc.Status != ledger.CommandFailed {
		t.Fatalf("an earlier attempt: %+v %v", rc, err)
	}
	if rc, err := recheck(e, run.ID, "r0b", "build", 3, c2); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("the attempt with another commit: %+v %v", rc, err)
	}
	if rc, err := recheck(e, run.ID, "r1", "build", 3, c3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again: %+v %v", rc, err)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, 4)
	l.attemptWhen(t, run.ID, "build", 4, failedRow)

	// Attempt 4 failed since: attempt 3 is no longer the candidate.
	rc, err := recheck(e, run.ID, "r2", "build", 3, c3)
	if !errors.Is(err, ErrStaleCandidate) || rc.Refusal != RefusalStaleCandidate || !strings.Contains(err.Error(), "attempt 4 failed since") {
		t.Fatalf("a candidate moved on from: %+v %v", rc, err)
	}
	if got := taskOf(t, e, run.ID, "build"); got.State != ledger.TaskFailed || got.Attempts != 4 {
		t.Fatalf("the task after the stale Check again: %+v", got)
	}
	// r1 sent again: its receipt, nothing checked; r1 naming attempt 4: a conflict.
	if again, err := recheck(e, run.ID, "r1", "build", 3, c3); err != nil || !again.Repeat || again.Status != ledger.CommandDone {
		t.Fatalf("r1 again: %+v %v", again, err)
	}
	if _, err := recheck(e, run.ID, "r1", "build", 4, c3); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("r1 naming another candidate: %v", err)
	}
	if got := taskOf(t, e, run.ID, "build"); got.State != ledger.TaskFailed || got.Attempts != 4 {
		t.Fatalf("the task after the resends: %+v", got)
	}
	if n := l.count(run.ID, "result.reverified"); n != 1 {
		t.Fatalf("%d checks again recorded, not 1", n)
	}
	stopRun(t, e, run.ID)
}

// A candidate refused before its checks (its checkout's setup failed) is
// checked again as one that failed a check is: the setup and the checks
// run again in a checkout of the same commit.
func TestRecheckAfterAFailureBeforeTheChecks(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "fixed")
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
	c := repoCrew(dir, build)
	// The setup fails in a checkout of a result with broken.txt until the
	// mark is made (the worktree, made before the result, has none).
	c.Setup = []Check{{ID: "deps", Argv: []string{"sh", "-c", "test -e " + mark + " || test ! -e broken.txt"}}}
	run := launchManaged(t, e, c)
	failed := spend(t, e, run.ID, "build", "broken.txt")
	if failed.Attempt.Stage != "setup deps" {
		t.Fatalf("the last attempt failed at %q", failed.Attempt.Stage)
	}
	c3, start3 := failed.Attempt.Result, failed.Attempt.Start
	mustMark(t, mark)
	if rc, err := recheck(e, run.ID, "r1", "build", 3, c3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again: %+v %v", rc, err)
	}
	passed := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 4)
	if passed.Result == nil || passed.Result.Commit != c3 || passed.Attempt.Start != start3 {
		t.Fatalf("checked again: %+v %+v", passed.Result, passed.Attempt)
	}
	l.attemptWhen(t, run.ID, "build", 4, succeededRow)
	if rows := l.stepRows(run.ID, "build", 4); len(rows) != 2 || rows[0].ID != "deps" || rows[0].Exit != 0 || rows[0].Subject != c3 || rows[1].ID != "ok" || rows[1].Exit != 0 {
		t.Fatalf("attempt 4's receipts:\n%s", rowWords(rows))
	}
	stopRun(t, e, run.ID)
}

// Check again takes only a failed work task whose verification failed it,
// in a run the engine runs: a running task, a review task, a task whose
// attempts failed before any result, an unknown task, a stopped run and a
// run the engine does not hold are each refused, with nothing recorded.
func TestRecheckRefusesATaskThatDidNotFailInItsVerification(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("review", "critic", "build")))
	running := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if rc, err := recheck(e, run.ID, "r0", "build", 1, ""); !errors.Is(err, ErrTaskState) || rc.Refusal != RefusalTaskState || !strings.Contains(err.Error(), "it is running") {
		t.Fatalf("a running task: %+v %v", rc, err)
	}
	if _, err := recheck(e, run.ID, "r1", "review", 1, ""); !errors.Is(err, ErrTaskState) || !strings.Contains(err.Error(), "review task") {
		t.Fatalf("a review task: %v", err)
	}
	if _, err := recheck(e, run.ID, "r2", "nope", 1, ""); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("an unknown task: %v", err)
	}
	// Each attempt's session ends before a result: failed, but not in a verification.
	for n := 1; n <= maxAttemptsPerTask; n++ {
		ts := running
		if n > 1 {
			ts = waitTaskWhere(t, e, run.ID, "build", fmt.Sprintf("at attempt %d, running", n), func(ts TaskState) bool {
				return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
			})
		}
		_, p := fl.attemptSession(t, ts)
		waitTyped(t, p, 10*time.Second)
		p.End(0)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, maxAttemptsPerTask)
	rc, err := recheck(e, run.ID, "r3", "build", 3, "")
	if !errors.Is(err, ErrTaskState) || rc.Refusal != RefusalTaskState || !strings.Contains(err.Error(), "not in its verification") {
		t.Fatalf("a task failed before any result: %+v %v", rc, err)
	}
	if got := taskOf(t, e, run.ID, "build"); got.State != ledger.TaskFailed || got.Attempts != maxAttemptsPerTask {
		t.Fatalf("the task after the refusals: %+v", got)
	}
	// A stopped run, and a run another engine over the same ledger does not hold.
	stopRun(t, e, run.ID)
	if _, err := recheck(e, run.ID, "r4", "build", 3, ""); !errors.Is(err, ErrTaskState) || !strings.Contains(err.Error(), "the run is stopped") {
		t.Fatalf("a stopped run: %v", err)
	}
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	if _, err := recheck(e2, run.ID, "r5", "build", 3, ""); !errors.Is(err, ErrTaskState) || !strings.Contains(err.Error(), "continue it") {
		t.Fatalf("a run not held: %v", err)
	}
	if _, err := recheck(e2, "nope-00000000", "r6", "build", 3, ""); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("an unknown run: %v", err)
	}
	if n := l.count(run.ID, "result.reverified"); n != 0 {
		t.Fatalf("%d checks again recorded", n)
	}
}

// A continuation's failed task, which has made no attempt of its own, has
// the continued run's last attempt as its standing failed candidate: Check
// again names it by its reference there and verifies it in the
// continuation as attempt n+1; the continued run refuses it, and so does
// an engine that does not hold the run.
func TestRecheckInAContinuation(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", mark)}
	run := launchManaged(t, e, repoCrew(dir, build))
	failed := spend(t, e, run.ID, "build", "built.txt")
	c3, start3 := failed.Attempt.Result, failed.Attempt.Start
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	l.runRowWhen(t, run.ID, "finished", func(r ledger.RunRow) bool { return r.State == ledger.RunFinished })
	third := receiptWords(l, run.ID, "build", 3)
	ctx := context.Background()
	next, err := e.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if b := waitTask(t, e, next.ID, "build", ledger.TaskFailed, maxAttemptsPerTask); b.Attempt != nil {
		t.Fatalf("the continuation's task has an attempt of its own: %+v", b.Attempt)
	}
	waitOutcome(t, e, next.ID, ledger.OutcomeFailed)
	if _, err := recheck(e, run.ID, "r0", "build", 3, c3); !errors.Is(err, ErrRunContinued) {
		t.Fatalf("Check again of the continued run: %v", err)
	}
	// Named as an attempt of the continuation, it is not the candidate.
	if _, err := recheck(e, next.ID, "r1", "build", 3, c3); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("the candidate not named by its reference: %v", err)
	}
	mustMark(t, mark)
	rc, err := e.Recheck(ctx, next.ID, "admin", Command{ID: "r2"}, RecheckRequest{Task: "build", Attempt: 3, Commit: c3, Imported: run.ID + "/build/3"})
	if err != nil || rc.Status != ledger.CommandDone || rc.Task == nil || rc.Task.Attempts != 4 {
		t.Fatalf("Check again in the continuation: %+v %v", rc, err)
	}
	passed := waitTask(t, e, next.ID, "build", ledger.TaskSucceeded, 4)
	if passed.Result == nil || passed.Result.Commit != c3 || passed.Result.Attempt != 4 || passed.Attempt.Start != start3 || passed.Attempt.SessionID != "" {
		t.Fatalf("checked again in the continuation: %+v %+v", passed.Result, passed.Attempt)
	}
	waitOutcome(t, e, next.ID, ledger.OutcomeSucceeded)
	if row := l.attemptWhen(t, next.ID, "build", 4, succeededRow); row.ResultCommit != c3 {
		t.Fatalf("attempt 4's row: %+v", row)
	}
	if p := l.payloads(next.ID, "result.reverified"); len(p) != 1 || p[0]["from"] != run.ID || p[0]["of"] != 3 {
		t.Fatalf("the result.reverified event: %+v", p)
	}
	if got := receiptWords(l, run.ID, "build", 3); got != third {
		t.Fatalf("the continued run's receipts changed:\n%s\nwere:\n%s", got, third)
	}
	stopRun(t, e, next.ID)
}

// A server that ends once Check again is committed, before its
// verification starts, leaves attempt n+1 in the ledger verifying with its
// result: the recovery interrupts it and the continuation verifies it
// again. One that ends before the commit changed nothing: no attempt n+1,
// the command taken and never settled.
func TestRecheckSurvivesARestartBeforeItsVerification(t *testing.T) {
	t.Run("after the commit", func(t *testing.T) {
		dir := newRepo(t)
		e, _ := readyEngine(t)
		ldir := t.TempDir()
		ld, err := ledger.Open(ldir)
		if err != nil {
			t.Fatal(err)
		}
		e.UseLedger(SQLLedger(ld))
		held, release := make(chan struct{}, 1), make(chan struct{})
		e.beforeRecheck = func(string) {
			held <- struct{}{}
			<-release
		}
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
		})
		mark := filepath.Join(t.TempDir(), "passes")
		build := work("build", "lead")
		build.Checks = []Check{markedCheck("test", mark)}
		run := launchManaged(t, e, repoCrew(dir, build))
		c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
		waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
		if rc, err := recheck(e, run.ID, "r1", "build", 3, c3); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("Check again: %+v %v", rc, err)
		}
		<-held
		// The owner ends here: its ledger closed, then the run stopped
		// with nothing more written, and the verification let go only
		// once the stop is under way, so it never starts.
		if err := ld.Close(); err != nil {
			t.Fatal(err)
		}
		stopped := make(chan error, 1)
		go func() { stopped <- e.Stop(context.Background(), run.ID) }()
		r := runOf(t, e, run.ID)
		waitFake(t, "the run stopping", func() bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			return r.stopping
		})
		close(release)
		<-stopped
		again, err := ledger.Open(ldir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { again.Close() })
		ctx := context.Background()
		if _, err := again.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		attempts, err := again.Attempts(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		a4 := byN(attempts, "build", 4)
		if a4.State != ledger.AttemptInterrupted || !strings.HasPrefix(a4.Stage, "verifying") || a4.ResultCommit != c3 || a4.SessionID != "" || a4.Result == "" {
			t.Fatalf("attempt 4 after the recovery: %+v", a4)
		}
		if cmd, ok, err := again.Receipt(ctx, run.ID, "admin", "r1"); err != nil || !ok || cmd.Status != ledger.CommandDone {
			t.Fatalf("the command after the recovery: %+v %v %v", cmd, ok, err)
		}
		mustMark(t, mark)
		e2, _ := readyEngine(t)
		e2.UseLedger(SQLLedger(again))
		next, err := e2.ContinueRun(ctx, run.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		b := waitTask(t, e2, next.ID, "build", ledger.TaskSucceeded, 5)
		if b.Result == nil || b.Result.Commit != c3 || b.Result.Attempt != 5 || b.Attempt.SessionID != "" {
			t.Fatalf("verified again in the continuation: %+v %+v", b.Result, b.Attempt)
		}
		waitOutcome(t, e2, next.ID, ledger.OutcomeSucceeded)
		stopRun(t, e2, next.ID)
	})
	t.Run("before the commit", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		build := work("build", "lead")
		build.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "never"))}
		run := launchManaged(t, e, repoCrew(dir, build))
		c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
		waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
		l.mu.Lock()
		l.failOn, l.failOnce = "command.recheck", true
		l.mu.Unlock()
		if _, err := recheck(e, run.ID, "r1", "build", 3, c3); err == nil {
			t.Fatal("a Check again the ledger refused succeeded")
		}
		stopRun(t, e, run.ID)
		l.mu.Lock()
		_, has4 := l.attempts[run.ID+"/build/4"]
		cmd := l.commands[run.ID+"/admin/r1"]
		task := l.tasks[run.ID+"/build"]
		l.mu.Unlock()
		if has4 || cmd.Status != ledger.CommandAccepted || task.State != ledger.TaskFailed || task.Attempts != maxAttemptsPerTask || task.Extra != 0 {
			t.Fatalf("the ledger after the refused commit: attempt 4 %v, command %+v, task %+v", has4, cmd, task)
		}
		e2, _ := readyEngine(t)
		e2.UseLedger(l)
		next, err := e2.ContinueRun(context.Background(), run.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if b := waitTask(t, e2, next.ID, "build", ledger.TaskFailed, maxAttemptsPerTask); b.Attempt != nil {
			t.Fatalf("the continuation's task: %+v %+v", b, b.Attempt)
		}
		stopRun(t, e2, next.ID)
	})
}

// A run whose scheduling is paused verifies a Check again all the same,
// and dispatches nothing else until it is unpaused.
func TestRecheckOnAPausedRun(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", mark)}
	run := launchManaged(t, e, repoCrew(dir, build, work("docs", "writer", "build")))
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	ctx := context.Background()
	if rc, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p1"}, true); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("pause: %+v %v", rc, err)
	}
	mustMark(t, mark)
	if rc, err := recheck(e, run.ID, "r1", "build", 3, c3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again: %+v %v", rc, err)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 4)
	l.attemptWhen(t, run.ID, "build", 4, succeededRow)
	l.taskWhen(t, run.ID, "build", "succeeded", func(r ledger.TaskRow) bool { return r.State == ledger.TaskSucceeded })
	if d := taskOf(t, e, run.ID, "docs"); d.State != ledger.TaskReady || d.Attempts != 0 {
		t.Fatalf("docs while the run is paused: %+v", d)
	}
	if r, _ := e.Get(run.ID); !r.Paused {
		t.Fatal("the run is not paused")
	}
	if rc, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p2"}, false); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("unpause: %+v %v", rc, err)
	}
	waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// A Check again whose commit the ledger refuses retains nothing: the run's
// next commits write no attempt n+1 and never settle its receipt, and the
// next Check again makes attempt n+1 as if the first had not been asked.
func TestRecheckRefusedCommitRetainsNothing(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "never"))}
	run := launchManaged(t, e, repoCrew(dir, build))
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	l.mu.Lock()
	l.failOn, l.failOnce = "command.recheck", true
	l.mu.Unlock()
	if _, err := recheck(e, run.ID, "r1", "build", 3, c3); err == nil {
		t.Fatal("a Check again the ledger refused succeeded")
	}
	// The refusal paused the run (its own commit); an unpause commits again.
	l.runRowWhen(t, run.ID, "paused", func(r ledger.RunRow) bool { return r.Paused })
	if rc, err := e.Pause(context.Background(), run.ID, "admin", Command{ID: "p1"}, false); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("unpause: %+v %v", rc, err)
	}
	l.runRowWhen(t, run.ID, "unpaused", func(r ledger.RunRow) bool { return !r.Paused })
	l.mu.Lock()
	_, has4 := l.attempts[run.ID+"/build/4"]
	cmd := l.commands[run.ID+"/admin/r1"]
	task := l.tasks[run.ID+"/build"]
	l.mu.Unlock()
	if has4 || cmd.Status != ledger.CommandAccepted || task.State != ledger.TaskFailed || task.Attempts != maxAttemptsPerTask || task.Extra != 0 {
		t.Fatalf("after the run's next commits: attempt 4 %v, command %+v, task %+v", has4, cmd, task)
	}
	if kinds := l.kinds(run.ID); slices.Contains(kinds, "result.reverified") || slices.Contains(kinds, "command.recheck") {
		t.Fatalf("the refused change's events were written: %v", kinds)
	}
	if got, _ := e.Get(run.ID); got.Outcome != ledger.OutcomeFailed {
		t.Fatalf("the run after the refused commit: %q", got.Outcome)
	}
	if got := taskOf(t, e, run.ID, "build"); got.State != ledger.TaskFailed || got.Attempts != maxAttemptsPerTask {
		t.Fatalf("the task after the refused commit: %+v", got)
	}
	if rc, err := recheck(e, run.ID, "r2", "build", 3, c3); err != nil || rc.Status != ledger.CommandDone || rc.Task.Attempts != 4 {
		t.Fatalf("the next Check again: %+v %v", rc, err)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, 4)
	stopRun(t, e, run.ID)
}

// Checks again that fail, then one that passes: each attempt has its own
// receipts, k from 1, on the same commit, past the 16 receipts an attempt's
// candidate shows, and the pass is the task's result.
func TestRecheckRepeatedFailuresThenAPass(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{{ID: "vet", Argv: []string{"true"}}, {ID: "lint", Argv: []string{"true"}}, {ID: "unit", Argv: []string{"true"}}, markedCheck("test", mark)}
	run := launchManaged(t, e, repoCrew(dir, build))
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	for n := 3; n < 6; n++ {
		if rc, err := recheck(e, run.ID, fmt.Sprintf("r%d", n), "build", n, c3); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("Check again of attempt %d: %+v %v", n, rc, err)
		}
		waitTask(t, e, run.ID, "build", ledger.TaskFailed, n+1)
		l.attemptWhen(t, run.ID, "build", n+1, failedRow)
	}
	mustMark(t, mark)
	if rc, err := recheck(e, run.ID, "r6", "build", 6, c3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again of attempt 6: %+v %v", rc, err)
	}
	passed := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 7)
	if passed.Result == nil || passed.Result.Attempt != 7 || passed.Result.Commit != c3 {
		t.Fatalf("the result: %+v", passed.Result)
	}
	l.attemptWhen(t, run.ID, "build", 7, succeededRow)
	total := 0
	for n := 3; n <= 7; n++ {
		rows := l.stepRows(run.ID, "build", n)
		total += len(rows)
		if len(rows) != 4 {
			t.Fatalf("attempt %d's receipts:\n%s", n, rowWords(rows))
		}
		for i, r := range rows {
			if r.N != n || r.K != 1 || r.ID != build.Checks[i].ID || r.Subject != c3 || r.State != ledger.CheckFinished {
				t.Fatalf("attempt %d's receipt %d: %+v", n, i, r)
			}
		}
		if last := rows[3]; (n < 7) != (last.Exit != 0) {
			t.Fatalf("attempt %d's test exited %d", n, last.Exit)
		}
	}
	if total+4*2 <= 16 {
		t.Fatalf("only %d receipts", total)
	}
	stopRun(t, e, run.ID)
}

// TestClassifyFailure classes a failed receipt from what the ledger keeps,
// over every class and the cases that must not be told for one another.
func TestClassifyFailure(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	failedA := ledger.AttemptRow{RunID: "r-a", TaskID: "build", N: 2, State: ledger.AttemptFailed, Stage: "check test", ResultCommit: "c2"}
	argv := []string{"go", "test", "./..."}
	row := ledger.CheckRow{RunID: "r-a", TaskID: "build", N: 2, ID: "test", K: 1, Phase: ledger.PhaseCheck, Argv: argv, Subject: "c2", State: ledger.CheckFinished, Exit: 1, Stable: true, Tail: "FAIL", StartedAt: at, FinishedAt: at.Add(3 * time.Second)}
	with := func(f func(*ledger.CheckRow)) ledger.CheckRow {
		r := row
		r.Argv = slices.Clone(row.Argv)
		f(&r)
		return r
	}
	attempt := func(state, stage string) ledger.AttemptRow {
		a := failedA
		a.State, a.Stage = state, stage
		return a
	}
	passed := func(f func(*ledger.CheckRow)) ledger.CheckRow {
		return with(func(r *ledger.CheckRow) {
			r.RunID, r.N, r.Exit, r.Tail = "r-b", 3, 0, ""
			if f != nil {
				f(r)
			}
		})
	}
	for _, c := range []struct {
		name  string
		a     ledger.AttemptRow
		r     ledger.CheckRow
		same  []ledger.CheckRow
		class string
		words string
	}{
		{"a code failure", failedA, row, nil, FailureCode, "exited 1"},
		{"a stop, the check interrupted and not killed", attempt(ledger.AttemptStopped, "verifying"), with(func(r *ledger.CheckRow) { r.State, r.Exit, r.Killed = ledger.CheckInterrupted, -1, false }), nil, FailureCancelled, "stopped"},
		{"a cancel", attempt(ledger.AttemptCancelled, "verifying"), row, nil, FailureCancelled, "cancelled"},
		{"a withdrawal", attempt(ledger.AttemptCancelled, "withdrawn"), row, nil, FailureCancelled, "withdrawn"},
		{"the server ended under the check", attempt(ledger.AttemptInterrupted, "verifying: the owner stopped; the candidate is verified again"), with(func(r *ledger.CheckRow) { r.State, r.Exit = ledger.CheckInterrupted, -1 }), nil, FailureInfrastructure, "the server ended"},
		{"an outcome never recorded", failedA, with(func(r *ledger.CheckRow) { r.State, r.Exit = ledger.CheckRunning, -1 }), nil, FailureInfrastructure, "never recorded"},
		{"a check that never ran", failedA, with(func(r *ledger.CheckRow) { r.Exit, r.Error = -1, "could not run: exec: \"go\": not found" }), nil, FailureInfrastructure, "never ran"},
		{"a setup step", attempt(ledger.AttemptFailed, "setup deps"), with(func(r *ledger.CheckRow) { r.ID, r.Phase, r.Error = "deps", ledger.PhaseSetup, "deps exited 1" }), nil, FailureInfrastructure, "the setup step deps"},
		{"a ready probe", attempt(ledger.AttemptFailed, "ready web"), with(func(r *ledger.CheckRow) { r.ID, r.Phase = "web", ledger.PhaseReady }), nil, FailureInfrastructure, "the ready step web"},
		{"a verify step is a check", attempt(ledger.AttemptFailed, "verify e2e"), with(func(r *ledger.CheckRow) { r.ID, r.Phase, r.Manifest = "e2e", ledger.PhaseVerify, "crew:1" }), nil, FailureCode, "exited 1"},
		{"flaky: passed on the same commit", failedA, row, []ledger.CheckRow{row, passed(nil)}, FailureFlaky, "attempt 3 of run r-b"},
		{"another task's check of the same id never makes it flaky", failedA, row, []ledger.CheckRow{passed(func(r *ledger.CheckRow) { r.TaskID = "docs" })}, FailureCode, "exited 1"},
		{"a check whose argv changed never makes it flaky", failedA, row, []ledger.CheckRow{passed(func(r *ledger.CheckRow) { r.Argv = []string{"go", "test", "./internal/..."} })}, FailureCode, "exited 1"},
		{"a pass on another commit never makes it flaky", failedA, row, []ledger.CheckRow{passed(func(r *ledger.CheckRow) { r.Subject = "c1" })}, FailureCode, "exited 1"},
		{"another failure on the same commit is no pass", failedA, row, []ledger.CheckRow{passed(func(r *ledger.CheckRow) { r.Exit = 2 })}, FailureCode, "exited 1"},
		{"a verify step of another manifest never makes it flaky", attempt(ledger.AttemptFailed, "verify e2e"), with(func(r *ledger.CheckRow) { r.ID, r.Phase, r.Manifest = "e2e", ledger.PhaseVerify, "crew:1" }),
			[]ledger.CheckRow{passed(func(r *ledger.CheckRow) { r.ID, r.Phase, r.Manifest = "e2e", ledger.PhaseVerify, "crew:2" })}, FailureCode, "exited 1"},
		{"a verify step of the same manifest passed", attempt(ledger.AttemptFailed, "verify e2e"), with(func(r *ledger.CheckRow) { r.ID, r.Phase, r.Manifest = "e2e", ledger.PhaseVerify, "crew:1" }),
			[]ledger.CheckRow{passed(func(r *ledger.CheckRow) { r.ID, r.Phase, r.Manifest = "e2e", ledger.PhaseVerify, "crew:1" })}, FailureFlaky, "passed"},
		{"no receipt in the ledger", failedA, ledger.CheckRow{}, nil, FailureUnavailable, "no receipt"},
		{"a dropped log with no output", failedA, with(func(r *ledger.CheckRow) { r.LogDropped, r.Tail = true, "" }), nil, FailureUnavailable, "dropped"},
		{"a dropped log with its tail kept", failedA, with(func(r *ledger.CheckRow) { r.LogDropped = true }), nil, FailureCode, "exited 1"},
		{"a signal", failedA, with(func(r *ledger.CheckRow) { r.Exit, r.Signal = -1, "killed" }), nil, FailureCode, "signal killed"},
		{"a timeout", failedA, with(func(r *ledger.CheckRow) { r.Exit, r.TimedOut, r.FinishedAt = -1, true, at.Add(600*time.Second) }), nil, FailureCode, "timed out after 10m0s: a hang in the code, or a machine too slow"},
		{"a changed checkout", failedA, with(func(r *ledger.CheckRow) { r.Exit, r.Stable, r.Error = 0, false, "changed tracked files: a.txt" }), nil, FailureCode, "left the checkout changed"},
		{"killed and exited non-zero", failedA, with(func(r *ledger.CheckRow) { r.Killed = true }), nil, FailureCode, "exited 1; a process it left behind"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyFailure(c.a, c.r, c.same)
			if got.Class != c.class || !strings.Contains(got.Words, c.words) {
				t.Fatalf("%+v, want %s with %q", got, c.class, c.words)
			}
		})
	}
}
