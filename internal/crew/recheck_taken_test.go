package crew

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// RecheckTaken answers what Check again would answer now, by its own rule,
// and records nothing: the standing failed candidate is taken, at the
// run's revision; a candidate the task moved on from is stale; a run the
// engine does not hold is not found, a stopped one refused. RecheckRepeat
// answers a Check again's kept receipt again for the same request, and is
// a conflict for another under its id.
func TestRecheckTakenIsTheCommandsRule(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", mark)}
	run := launchManaged(t, e, repoCrew(dir, build))
	failed := spend(t, e, run.ID, "build", "built.txt")
	c3 := failed.Attempt.Result
	ctx := context.Background()
	rq := RecheckRequest{Task: "build", Attempt: 3, Commit: c3}
	rev, err := e.RecheckTaken(run.ID, rq)
	if err != nil {
		t.Fatalf("the standing failed candidate: %v", err)
	}
	if got, _ := e.Get(run.ID); got.Managed == nil || got.Managed.Revision != rev {
		t.Fatalf("revision %d, the run's %+v", rev, got.Managed)
	}
	if _, err := e.RecheckTaken(run.ID, RecheckRequest{Task: "build", Attempt: 2, Commit: c3}); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("attempt 2: %v", err)
	}
	if _, err := e.RecheckTaken(run.ID, RecheckRequest{Task: "nope", Attempt: 3, Commit: c3}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("an unknown task: %v", err)
	}
	if _, err := e.RecheckTaken("nope-00000000", rq); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("an unknown run: %v", err)
	}
	if n := l.count(run.ID, "result.reverified"); n != 0 {
		t.Fatalf("%d checks again recorded by a read", n)
	}
	rc, err := recheck(e, run.ID, "r1", "build", 3, c3)
	if err != nil {
		t.Fatal(err)
	}
	row, ok, err := l.Receipt(ctx, run.ID, "admin", "r1")
	if err != nil || !ok {
		t.Fatalf("the command's row: %v %v", ok, err)
	}
	if again, err := RecheckRepeat(row, rq); err != nil || !again.Repeat || again.CommandID != "r1" || again.Status != rc.Status {
		t.Fatalf("the same request: %+v %v", again, err)
	}
	if _, err := RecheckRepeat(row, RecheckRequest{Task: "build", Attempt: 2, Commit: c3}); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("another request under the id: %v", err)
	}
	// The same request named in a continuation (Imported) is the same content.
	if _, err := RecheckRepeat(row, RecheckRequest{Task: "build", Attempt: 3, Commit: c3, Imported: run.ID + "/build/3"}); err != nil {
		t.Fatalf("the request with its reference: %v", err)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, 4)
	stopRun(t, e, run.ID)
	if _, err := e.RecheckTaken(run.ID, RecheckRequest{Task: "build", Attempt: 4, Commit: c3}); !errors.Is(err, ErrTaskState) {
		t.Fatalf("a stopped run: %v", err)
	}
}
