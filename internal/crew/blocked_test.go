package crew

import (
	"context"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// forGoodWords reports whether a task's reason is the one the engine writes
// for a blocked task that will never start.
func forGoodWords(ts TaskState) bool { return strings.HasSuffix(ts.Reason, ", which will not succeed") }

// sameAsWords fails the test unless every task of the run's read carries
// blockedForGood exactly where its reason says it will not succeed.
func sameAsWords(t *testing.T, e *Engine, runID string) Run {
	t.Helper()
	r, _ := e.Get(runID)
	for _, ts := range r.Tasks {
		if ts.BlockedForGood != (ts.State == ledger.TaskBlocked && forGoodWords(ts)) {
			t.Fatalf("%s: blockedForGood %v beside %s %q", ts.ID, ts.BlockedForGood, ts.State, ts.Reason)
		}
	}
	return r
}

// A task blocked behind a need that was cancelled, and one behind that
// (a chain of three), read blockedForGood, set from the condition their
// reason's "…, which will not succeed" is written from; a task blocked
// for now behind a need still running does not, nor does any task before
// the cancel.
func TestATaskBehindACancelledNeedReadsBlockedForGood(t *testing.T) {
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(newRepo(t), work("build", "lead"), work("docs", "writer", "build"), work("notes", "scribe", "docs"),
		work("other", "odd"), work("side", "sider", "other")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	waitAttempt(t, e, run.ID, "other", ledger.AttemptRunning)
	for _, ts := range sameAsWords(t, e, run.ID).Tasks {
		if ts.BlockedForGood {
			t.Fatalf("%s reads blockedForGood before anything ended: %q", ts.ID, ts.Reason)
		}
	}
	if err := e.CancelTask(context.Background(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	waitTaskWhere(t, e, run.ID, "notes", "blocked for good", func(ts TaskState) bool { return ts.BlockedForGood })
	r := sameAsWords(t, e, run.ID)
	for _, ts := range r.Tasks {
		switch ts.ID {
		case "docs", "notes":
			if !ts.BlockedForGood || ts.State != ledger.TaskBlocked {
				t.Fatalf("%s behind the cancelled build: %+v", ts.ID, ts)
			}
		case "side":
			if ts.BlockedForGood || ts.State != ledger.TaskBlocked || ts.Reason != "needs other" {
				t.Fatalf("side, blocked for now behind a running task: %+v", ts)
			}
		default:
			if ts.BlockedForGood {
				t.Fatalf("%s is not blocked, yet reads blockedForGood: %+v", ts.ID, ts)
			}
		}
	}
}

// A task behind a need that failed (out of its attempts) reads
// blockedForGood, beside its reason's words.
func TestATaskBehindAFailedNeedReadsBlockedForGood(t *testing.T) {
	e, _, _ := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "never", Argv: []string{"sh", "-c", "exit 1"}}}
	run := launchManaged(t, e, repoCrew(newRepo(t), build, work("docs", "writer", "build")))
	if docs := taskOf(t, e, run.ID, "docs"); docs.BlockedForGood {
		t.Fatalf("docs before build failed: %+v", docs)
	}
	for n := 1; n <= 3; n++ {
		a := waitTaskWhere(t, e, run.ID, "build", "running", func(ts TaskState) bool {
			return ts.Attempts == n && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
		})
		commitResult(t, e, a, "built.txt", strings.Repeat("try\n", n), "try")
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, 3)
	waitTaskWhere(t, e, run.ID, "docs", "blocked for good", func(ts TaskState) bool { return ts.BlockedForGood })
	sameAsWords(t, e, run.ID)
}

// A record written again as the ledger has the run (Recovered) is judged
// by the same rule: behind a failed, a cancelled, a missing or a doomed
// need, for good; behind one still under way, or ready, not; a task that
// is not blocked, never.
func TestMarkBlockedForGoodJudgesARecordByTheEngineRule(t *testing.T) {
	tasks := []TaskState{
		{ID: "a", State: ledger.TaskFailed},
		{ID: "b", State: ledger.TaskBlocked, Needs: []string{"a"}},
		{ID: "c", State: ledger.TaskBlocked, Needs: []string{"b"}},
		{ID: "d", State: ledger.TaskCancelled},
		{ID: "e", State: ledger.TaskBlocked, Needs: []string{"d"}},
		{ID: "f", State: ledger.TaskRunning},
		{ID: "g", State: ledger.TaskBlocked, Needs: []string{"f"}},
		{ID: "h", State: ledger.TaskBlocked, Needs: []string{"gone"}},
		{ID: "i", State: ledger.TaskReady, Needs: []string{"a"}},
		{ID: "j", State: ledger.TaskBlocked, Needs: []string{"g"}},
	}
	markBlockedForGood(tasks)
	want := map[string]bool{"b": true, "c": true, "e": true, "h": true}
	for _, ts := range tasks {
		if ts.BlockedForGood != want[ts.ID] {
			t.Fatalf("%s (%s, needs %v): blockedForGood %v", ts.ID, ts.State, ts.Needs, ts.BlockedForGood)
		}
	}
}
