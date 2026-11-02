package crew

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// waitStopped waits for the session to have ended.
func waitStopped(t *testing.T, local *session.Local) {
	t.Helper()
	select {
	case <-local.Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not stopped")
	}
}

// waitOutcome waits for the run to have the outcome.
func waitOutcome(t *testing.T, e *Engine, runID, outcome string) Run {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		r, ok := e.Get(runID)
		if !ok {
			t.Fatalf("run %s is gone", runID)
		}
		if r.Outcome == outcome {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run is %s/%q after 20 s, not %s: %+v", r.State, r.Outcome, outcome, r.Tasks)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// commitResult commits a file in the attempt's worktree and submits it.
func commitResult(t *testing.T, e *Engine, ts TaskState, name, content, summary string) TaskState {
	t.Helper()
	writeFile(t, filepath.Join(ts.Attempt.Worktree, name), content)
	commitAll(t, ts.Attempt.Worktree, summary)
	return submit(t, e, ts.Attempt.SessionID, Result{Summary: summary})
}

// taskFileOf reads the attempt's task files.
func taskFileOf(t *testing.T, ts TaskState) (string, TaskFile) {
	t.Helper()
	md, err := os.ReadFile(filepath.Join(ts.Attempt.Worktree, taskDir, "task.md"))
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile(filepath.Join(ts.Attempt.Worktree, taskDir, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f TaskFile
	if err := json.Unmarshal(js, &f); err != nil {
		t.Fatal(err)
	}
	return string(md), f
}

// A review's needs_changes reopens the task it reviewed: its next attempt
// starts at the reviewed commit with the findings in its task file, and
// what rested on the result is withdrawn (the review waits again; a
// dependent's attempt in flight ends, not counted); the review runs again
// on the repair, and once it approves, the run succeeds (row 6 of #334).
func TestAReviewsNeedsChangesReopensTheTaskAndWithdrawsWhatRestedOnIt(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("review", "critic", "build"), work("docs", "writer", "build")))
	first := commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "yes\n", "built")
	first = waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	rev := waitAttempt(t, e, run.ID, "review", ledger.AttemptRunning)
	docs := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	docsSession, _ := fl.attemptSession(t, docs)
	findings := []Finding{{Severity: "major", Path: "built.txt", Line: 1, Message: "say why"}}
	submit(t, e, rev.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Summary: "one thing", Findings: findings})

	// build: attempt 2 at the reviewed commit, the findings in its task file, no result.
	second := waitTaskWhere(t, e, run.ID, "build", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempts == 2 && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
	})
	if second.Result != nil || second.Attempt.Start != first.Result.Commit {
		t.Fatalf("build reopened: %+v", second)
	}
	if r, _ := e.Get(run.ID); !slices.ContainsFunc(r.Log, func(l session.ActivityEntry) bool {
		return l.Message == "task build: review review asked for changes (1 findings); attempt 2 starts at the reviewed commit"
	}) || slices.ContainsFunc(r.Log, func(l session.ActivityEntry) bool { return strings.HasPrefix(l.Message, "every task is done") }) {
		t.Fatalf("the run log after the reopen: %+v", r.Log)
	}
	md, f := taskFileOf(t, second)
	for _, want := range []string{"## Findings to address", `Review "review" (attempt 1) asked for changes to the commit ` + first.Result.Commit, ": one thing.", "- major built.txt:1: say why", "the review runs again on your result"} {
		if !strings.Contains(md, want) {
			t.Errorf("task.md lacks %q:\n%s", want, md)
		}
	}
	if f.Repair == nil || f.Repair.Review != "review" || f.Repair.Attempt != 1 || f.Repair.Commit != first.Result.Commit || len(f.Repair.Findings) != 1 || f.Repair.Findings[0].Message != "say why" {
		t.Fatalf("task.json's repair: %+v", f.Repair)
	}
	// The review waits again, its result gone; docs' attempt is withdrawn, its session stopped, and the attempt not counted.
	if r := taskOf(t, e, run.ID, "review"); r.State != ledger.TaskBlocked || r.Result != nil || r.Reason != "needs build" {
		t.Fatalf("the review after the reopen: %+v", r)
	}
	withdrawn := waitTaskWhere(t, e, run.ID, "docs", "withdrawn", func(ts TaskState) bool {
		return ts.State == ledger.TaskBlocked && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptCancelled
	})
	if withdrawn.Attempt.Stage != "withdrawn" || withdrawn.Attempts != 1 {
		t.Fatalf("docs withdrawn: %+v", withdrawn)
	}
	waitStopped(t, docsSession)
	if row := l.attemptRow(t, run.ID, "docs", 1); row.State != ledger.AttemptCancelled || row.Stage != "withdrawn" {
		t.Fatalf("docs' attempt in the ledger: %+v", row)
	}
	kinds := l.kinds(run.ID)
	for _, want := range []string{"task.reopened", "task.withdrawn", "attempt.withdrawn"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("no %s event in %v", want, kinds)
		}
	}
	if r, _ := e.Get(run.ID); r.Outcome != "" || r.State != RunRunning {
		t.Fatalf("the run after the reopen: %s %q", r.State, r.Outcome)
	}

	// The repair: build's second result, the review again on it (approved), docs again at the new commit; the run succeeds.
	commitResult(t, e, second, "built.txt", "yes, because\n", "explained")
	repaired := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2)
	rev2 := waitTaskWhere(t, e, run.ID, "review", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempts == 2 && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
	})
	if rev2.Attempt.Start != repaired.Result.Commit {
		t.Fatalf("the review's second attempt is at %s, the repair is %s", rev2.Attempt.Start, repaired.Result.Commit)
	}
	if md, f := taskFileOf(t, rev2); f.Repair != nil || strings.Contains(md, "Findings to address") {
		t.Fatalf("the review's task file carries a repair: %+v", f.Repair)
	}
	submit(t, e, rev2.Attempt.SessionID, Result{Verdict: VerdictApproved, Summary: "fine"})
	if r := waitTask(t, e, run.ID, "review", ledger.TaskSucceeded, 2); r.Result == nil || r.Result.Verdict != VerdictApproved {
		t.Fatalf("the review's result: %+v", r.Result)
	}
	docs2 := waitTaskWhere(t, e, run.ID, "docs", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempts == 2 && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
	})
	if docs2.Attempt.Start != repaired.Result.Commit {
		t.Fatalf("docs' second attempt is at %s, the repair is %s", docs2.Attempt.Start, repaired.Result.Commit)
	}
	commitResult(t, e, docs2, "docs.txt", "documented\n", "documented")
	waitTask(t, e, run.ID, "docs", ledger.TaskSucceeded, 2)
	waitOutcome(t, e, run.ID, "succeeded")
	// The ledger kept the first review's verdict and findings on its attempt.
	var kept Result
	if row := l.attemptRow(t, run.ID, "review", 1); row.State != ledger.AttemptSucceeded || json.Unmarshal([]byte(row.Result), &kept) != nil || kept.Verdict != VerdictNeedsChanges || len(kept.Findings) != 1 {
		t.Fatalf("the first review in the ledger: %+v", row)
	}
	// TaskOf reports every attempt with what it submitted.
	if _, attempts, err := e.TaskOf(run.ID, "review"); err != nil || len(attempts) != 2 || attempts[0].Submitted == nil || attempts[0].Submitted.Verdict != VerdictNeedsChanges || attempts[1].Submitted.Verdict != VerdictApproved {
		t.Fatalf("TaskOf(review): %+v %v", attempts, err)
	}
	stopRun(t, e, run.ID)
}

// A task reopened with no attempt left fails, keeping the repair: a retry
// allows one more attempt, which starts at the reviewed commit with the
// findings.
func TestAReopenWithNoAttemptLeftFailsTheTaskUntilARetry(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	build := work("build", "lead")
	// The check passes once the commit has passed.txt, which the third attempt adds.
	build.Checks = []Check{{ID: "passed", Argv: []string{"sh", "-c", "test -e passed.txt"}}}
	run := launchManaged(t, e, repoCrew(dir, build, review("review", "critic", "build")))
	for n := 1; n <= 2; n++ {
		a := waitTaskWhere(t, e, run.ID, "build", "running", func(ts TaskState) bool {
			return ts.Attempts == n && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
		})
		commitResult(t, e, a, "built.txt", strings.Repeat("yes\n", n), "built")
	}
	third := waitTaskWhere(t, e, run.ID, "build", "at attempt 3, running", func(ts TaskState) bool {
		return ts.Attempts == 3 && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
	})
	passed := commitResult(t, e, third, "passed.txt", "yes\n", "passed")
	passed = waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 3)
	rev := waitAttempt(t, e, run.ID, "review", ledger.AttemptRunning)
	submit(t, e, rev.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Summary: "not yet", Findings: []Finding{{Severity: "minor", Message: "more"}}})
	failed := waitTask(t, e, run.ID, "build", ledger.TaskFailed, 3)
	if failed.Result != nil || !strings.Contains(failed.Reason, "review review asked for changes and no attempt is left; retry it to allow one more") {
		t.Fatalf("build after the reopen: %+v", failed)
	}
	// The review is blocked for good on a failed task: the run fails.
	waitOutcome(t, e, run.ID, "failed")
	if r := taskOf(t, e, run.ID, "review"); r.State != ledger.TaskBlocked || r.Result != nil {
		t.Fatalf("the review: %+v", r)
	}
	// A retry: one more attempt, at the reviewed commit, with the findings; the run is open again.
	if err := e.RetryTask(context.Background(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	fourth := waitTaskWhere(t, e, run.ID, "build", "at attempt 4, running", func(ts TaskState) bool {
		return ts.Attempts == 4 && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
	})
	if fourth.Attempt.Start != passed.Result.Commit {
		t.Fatalf("attempt 4 starts at %s, the reviewed commit is %s", fourth.Attempt.Start, passed.Result.Commit)
	}
	if md, f := taskFileOf(t, fourth); f.Repair == nil || f.Repair.Attempt != 1 || !strings.Contains(md, "- minor: more") {
		t.Fatalf("attempt 4's task file: %+v\n%s", f.Repair, md)
	}
	if r, _ := e.Get(run.ID); r.Outcome != "" {
		t.Fatalf("the run after the retry: %q", r.Outcome)
	}
	stopRun(t, e, run.ID)
}

// Commands with receipts: the same id again is the same answer, other
// content a conflict, a stale revision refused, a failure its receipt;
// Accept takes a failed task's last submitted result, and what needs it
// goes on.
func TestCommandsKeepReceiptsAndAcceptTakesAFailedResult(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "never", Argv: []string{"sh", "-c", "exit 1"}}}
	run := launchManaged(t, e, repoCrew(dir, build, work("docs", "writer", "build")))
	ctx := context.Background()
	cmd := func(id string) Command { return Command{ID: id} }

	// Pause: a receipt; again, the same; other content with the id, a conflict; a stale revision; a bad id.
	rc, err := e.Pause(ctx, run.ID, "admin", cmd("p1"), true)
	if err != nil || rc.Status != ledger.CommandDone || rc.Intent != "pause" || rc.Paused == nil || !*rc.Paused || rc.Repeat || rc.Revision < 1 || rc.Task != nil {
		t.Fatalf("pause: %+v %v", rc, err)
	}
	if again, err := e.Pause(ctx, run.ID, "admin", cmd("p1"), true); err != nil || !again.Repeat || again.Revision != rc.Revision || again.Status != ledger.CommandDone {
		t.Fatalf("pause again: %+v %v", again, err)
	}
	if _, err := e.Pause(ctx, run.ID, "admin", cmd("p1"), false); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("other content: %v", err)
	}
	if _, err := e.Pause(ctx, run.ID, "other", cmd("p1"), false); err != nil {
		t.Fatalf("another actor's id: %v", err)
	}
	if _, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p2", ExpectedRevision: rc.Revision + 50}, false); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p 2"}, false); !errors.Is(err, ErrBadCommandID) {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := e.Pause(ctx, "nope-00000000", "admin", cmd("p3"), false); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("no run: %v", err)
	}
	if r, _ := e.Get(run.ID); r.Paused {
		t.Fatal("the run is paused after the other actor's unpause: the other actor's command was not taken")
	}
	// Three attempts fail the check; the task fails; the run fails with it.
	for n := 1; n <= 3; n++ {
		a := waitTaskWhere(t, e, run.ID, "build", "running", func(ts TaskState) bool {
			return ts.Attempts == n && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
		})
		commitResult(t, e, a, "built.txt", strings.Repeat("try\n", n), "try")
	}
	failed := waitTask(t, e, run.ID, "build", ledger.TaskFailed, 3)
	waitOutcome(t, e, run.ID, "failed")
	// Accept on a task that is not failed: the receipt is the failure.
	if rc, err := e.Accept(ctx, run.ID, "admin", cmd("a0"), "docs"); !errors.Is(err, ErrTaskState) || rc.Status != ledger.CommandFailed || rc.Task == nil || rc.Task.ID != "docs" || rc.Error == "" {
		t.Fatalf("accept of a blocked task: %+v %v", rc, err)
	}
	if rc, err := e.Accept(ctx, run.ID, "admin", cmd("a0"), "docs"); err != nil || !rc.Repeat || rc.Status != ledger.CommandFailed {
		t.Fatalf("the failed receipt again: %+v %v", rc, err)
	}
	if _, err := e.Accept(ctx, run.ID, "admin", cmd("a0"), "build"); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("the id with another task: %v", err)
	}
	// Accept anyway: build succeeds with attempt 3's commit, docs goes on at it, the run is open again.
	rc, err = e.Accept(ctx, run.ID, "admin", cmd("a1"), "build")
	if err != nil || rc.Status != ledger.CommandDone || rc.Task == nil || rc.Task.State != ledger.TaskSucceeded || rc.Task.Result == nil || rc.Task.Result.Commit != failed.Attempt.Result || rc.Task.Result.Attempt != 3 {
		t.Fatalf("accept: %+v %v", rc, err)
	}
	if b := taskOf(t, e, run.ID, "build"); !strings.Contains(b.Reason, "accepted by admin with attempt 3's result") {
		t.Fatalf("build: %+v", b)
	}
	if r, _ := e.Get(run.ID); r.Outcome != "" {
		t.Fatalf("the run after the accept: %q", r.Outcome)
	}
	// An unpause of a run not paused: taken, changing nothing.
	if rc, err := e.Pause(ctx, run.ID, "admin", cmd("p4"), false); err != nil || rc.Paused == nil || *rc.Paused || rc.Status != ledger.CommandDone {
		t.Fatalf("unpause: %+v %v", rc, err)
	}
	docs := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	if docs.Attempt.Start != failed.Attempt.Result {
		t.Fatalf("docs starts at %s, the accepted commit is %s", docs.Attempt.Start, failed.Attempt.Result)
	}
	// Cancel docs with a receipt carrying the task; retry it; the ledger holds every command.
	if rc, err := e.Cancel(ctx, run.ID, "admin", cmd("c1"), "docs"); err != nil || rc.Task == nil || rc.Task.State != ledger.TaskCancelled {
		t.Fatalf("cancel: %+v %v", rc, err)
	}
	waitOutcome(t, e, run.ID, "cancelled")
	if rc, err := e.Cancel(ctx, run.ID, "admin", cmd("c2"), "docs"); !errors.Is(err, ErrTaskState) || rc.Status != ledger.CommandFailed {
		t.Fatalf("cancel again: %+v %v", rc, err)
	}
	if rc, err := e.Retry(ctx, run.ID, "admin", cmd("r1"), "docs"); err != nil || rc.Task == nil || rc.Task.State == ledger.TaskCancelled {
		t.Fatalf("retry: %+v %v", rc, err)
	}
	waitTaskWhere(t, e, run.ID, "docs", "at attempt 2", func(ts TaskState) bool { return ts.Attempts == 2 })
	for _, id := range []string{"admin/p1", "other/p1", "admin/a0", "admin/a1", "admin/p4", "admin/c1", "admin/c2", "admin/r1"} {
		actor, cid, _ := strings.Cut(id, "/")
		if row, ok, err := l.Receipt(ctx, run.ID, actor, cid); err != nil || !ok || row.Receipt == "" {
			t.Errorf("command %s in the ledger: %+v %v %v", id, row, ok, err)
		}
	}
	if row, ok, _ := l.Receipt(ctx, run.ID, "admin", "p2"); !ok || row.Status != ledger.CommandFailed || !strings.Contains(row.Receipt, RefusalStaleRevision) {
		t.Fatalf("the stale command's receipt: %+v %v", row, ok)
	}
	kinds := l.kinds(run.ID)
	for _, want := range []string{"command.pause", "command.unpause", "command.accept", "task.accepted", "command.cancel", "command.retry"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("no %s event in %v", want, kinds)
		}
	}
	stopRun(t, e, run.ID)
}

// A legacy run takes no command, and without a ledger none is taken.
func TestCommandsNeedAManagedRunAndTheLedger(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	c := repoCrew(dir)
	c.Tasks = nil
	c.Members = []Member{immediate("lead", "go")}
	run := launchManaged(t, e, c)
	if _, err := e.Pause(context.Background(), run.ID, "admin", Command{ID: "p1"}, true); !errors.Is(err, ErrTaskState) {
		t.Fatalf("a legacy run: %v", err)
	}
	stopRun(t, e, run.ID)
	e2, _ := newEngine(t)
	if _, err := e2.Pause(context.Background(), "nope-00000000", "admin", Command{ID: "p1"}, true); !errors.Is(err, ErrNoLedger) {
		t.Fatalf("no ledger: %v", err)
	}
}

// A retry takes a task in the states RetryableStates names, failed or
// cancelled, and refuses it in every other (ErrTaskState): the operating
// guide's task_retry says them from RetryableStates.
func TestARetryTakesTheRetryableStatesOnly(t *testing.T) {
	if got := RetryableStates(); !slices.Equal(got, []string{ledger.TaskFailed, ledger.TaskCancelled}) {
		t.Fatalf("retryable states %v", got)
	}
	for _, st := range []string{ledger.TaskBlocked, ledger.TaskReady, ledger.TaskRunning, ledger.TaskVerifying, ledger.TaskSucceeded, ledger.TaskFailed, ledger.TaskCancelled} {
		err := retryable(&task{state: st})
		if want := slices.Contains(RetryableStates(), st); (err == nil) != want || (err != nil && !errors.Is(err, ErrTaskState)) {
			t.Errorf("a %s task: %v", st, err)
		}
	}
}
