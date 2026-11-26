package crew

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A blocker shows on the task until the attempt works on; an attempt
// that is not running takes none (row 10 of #334).
func TestABlockerShowsOnTheTaskUntilTheAttemptWorksOn(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	ctx := context.Background()
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	st, err := e.Blocked(ctx, b.Attempt.SessionID, "no token for the registry", "Set NPM_TOKEN in the worktree's .env, then answer here")
	if err != nil || st.Blocker == nil || st.Blocker.Reason != "no token for the registry" || st.Blocker.By != "lead" || st.Blocker.Message == "" || st.State != ledger.TaskRunning {
		t.Fatalf("blocked: %+v %v", st, err)
	}
	if got := taskOf(t, e, run.ID, "build"); got.Blocker == nil {
		t.Fatalf("the run does not show the blocker: %+v", got)
	}
	for _, tc := range []struct{ reason, message string }{{"", "x"}, {strings.Repeat("r", 501), ""}, {"ok", strings.Repeat("m", maxCheckpointField+1)}} {
		if _, err := e.Blocked(ctx, b.Attempt.SessionID, tc.reason, tc.message); !errors.Is(err, ErrBadBlocker) {
			t.Fatalf("%q/%d: %v", tc.reason, len(tc.message), err)
		}
	}
	// A working report from the attempt clears it.
	e.OnActivity(b.Attempt.SessionID, session.ActivityEntry{Type: session.ActivityAttention, At: time.Now().UTC()}, session.AttentionWorking, "")
	waitTaskWhere(t, e, run.ID, "build", "unblocked", func(ts TaskState) bool { return ts.Blocker == nil })
	kinds := l.kinds(run.ID)
	for _, want := range []string{"task.blocked", "task.unblocked"} {
		if !strings.Contains(strings.Join(kinds, " "), want) {
			t.Errorf("no %s in %v", want, kinds)
		}
	}
	// A result ends the attempt and takes any blocker with it.
	if _, err := e.Blocked(ctx, b.Attempt.SessionID, "again", ""); err != nil {
		t.Fatal(err)
	}
	commitResult(t, e, b, "built.txt", "yes\n", "built")
	if done := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1); done.Blocker != nil {
		t.Fatalf("a blocker outlived the attempt: %+v", done.Blocker)
	}
	if _, err := e.Blocked(ctx, b.Attempt.SessionID, "late", ""); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("a blocker on an ended attempt: %v", err)
	}
	if _, err := e.Blocked(ctx, "nope", "x", ""); !errors.Is(err, ErrNotAnAttempt) {
		t.Fatalf("no session: %v", err)
	}
	stopRun(t, e, run.ID)
}

// A checkpoint is kept on the attempt and in the ledger; an attempt after
// one that ended without a result starts at its checkpoint's commit, its
// task file saying where the last one got to.
func TestACheckpointIsKeptAndTheNextAttemptStartsThere(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	ctx := context.Background()
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(b.Attempt.Worktree, "half.txt"), "half\n")
	commitAll(t, b.Attempt.Worktree, "half way")
	head := strings.TrimSpace(runGit(t, b.Attempt.Worktree, "rev-parse", "HEAD"))
	a, err := e.SaveCheckpoint(ctx, b.Attempt.SessionID, "the handlers", "the tests and the docs", "the docs page is docs/api.md")
	if err != nil || a.Checkpoint == nil || a.Checkpoint.Commit != head || a.Checkpoint.Done != "the handlers" || a.N != 1 {
		t.Fatalf("checkpoint: %+v %v", a, err)
	}
	if row := l.attemptRow(t, run.ID, "build", 1); !strings.Contains(row.Checkpoint, "the handlers") || !strings.Contains(row.Checkpoint, head) {
		t.Fatalf("the row's checkpoint: %q", row.Checkpoint)
	}
	for _, tc := range []struct{ done, remaining, notes string }{{"", "r", ""}, {"d", strings.Repeat("r", maxCheckpointField+1), ""}, {"d", "r", strings.Repeat("n", maxCheckpointField+1)}} {
		if _, err := e.SaveCheckpoint(ctx, b.Attempt.SessionID, tc.done, tc.remaining, tc.notes); !errors.Is(err, ErrBadCheckpoint) {
			t.Fatalf("%q: %v", tc.done, err)
		}
	}
	if got := taskOf(t, e, run.ID, "build"); got.Attempt == nil || got.Attempt.Checkpoint == nil || got.Attempt.Checkpoint.Commit != head {
		t.Fatalf("the run does not show the checkpoint: %+v", got.Attempt)
	}
	// The session ends without a result: attempt 2 starts at the checkpoint, told where the last one got to.
	_, p := fl.attemptSession(t, b)
	p.End(0)
	b2 := waitTaskWhere(t, e, run.ID, "build", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if b2.Attempt.Start != head {
		t.Fatalf("attempt 2 starts at %s, the checkpoint is %s", b2.Attempt.Start, head)
	}
	md, f := taskFileOf(t, b2)
	if f.Checkpoint == nil || f.Checkpoint.Commit != head || f.Checkpoint.Remaining != "the tests and the docs" {
		t.Fatalf("task.json's checkpoint: %+v", f.Checkpoint)
	}
	for _, want := range []string{"## Where the last attempt got to", "where this attempt starts", "- Done: the handlers", "- Remaining: the tests and the docs", "- Notes: the docs page is docs/api.md"} {
		if !strings.Contains(md, want) {
			t.Errorf("task.md lacks %q:\n%s", want, md)
		}
	}
	if _, err := e.SaveCheckpoint(ctx, b.Attempt.SessionID, "late", "x", ""); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("a checkpoint on an ended attempt: %v", err)
	}
	stopRun(t, e, run.ID)
}

// A continuation's first attempt at a task starts at the last checkpoint.
func TestAContinuationStartsAtTheLastCheckpoint(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	ctx := context.Background()
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(b.Attempt.Worktree, "half.txt"), "half\n")
	commitAll(t, b.Attempt.Worktree, "half way")
	head := strings.TrimSpace(runGit(t, b.Attempt.Worktree, "rev-parse", "HEAD"))
	if _, err := e.SaveCheckpoint(ctx, b.Attempt.SessionID, "half", "the rest", ""); err != nil {
		t.Fatal(err)
	}
	interrupt(l, run.ID, map[string][2]string{"build/1": {ledger.AttemptInterrupted, "running: the owner stopped"}})
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	b2 := waitAttempt(t, e2, next.ID, "build", ledger.AttemptRunning)
	if b2.Attempt.N != 2 || b2.Attempt.Start != head {
		t.Fatalf("the continuation's attempt: %+v", b2.Attempt)
	}
	if md, f := taskFileOf(t, b2); f.Checkpoint == nil || f.Checkpoint.Commit != head || !strings.Contains(md, "- Done: half") {
		t.Fatalf("the task file: %+v\n%s", f.Checkpoint, md)
	}
	stopRun(t, e2, next.ID)
}
