package crew

import (
	"fmt"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The functions the history's index applies (docs/plans/history.md,
// PR-3) are the engine's own rule: over the rows an engine wrote, the
// receipt FailingReceipt finds for the attempt that failed at its check is
// the one the engine handed the next attempt, in the run and in a
// continuation, and an attempt whose worktree could not be made usable is
// one UncountedAttempt passes over.
func TestFailingReceiptIsTheEnginesRule(t *testing.T) {
	handedOf := func(t *testing.T, ld *ledger.Ledger, run string, c *TaskCheck) {
		t.Helper()
		row := ledgerAttempt(t, ld, run, "build", 1, ledger.AttemptFailed)
		if phase, id, ok := FailedAtCheck(row); !ok || phase != ledger.PhaseCheck || id != "unit" || UncountedAttempt(row) {
			t.Fatalf("attempt 1's row: %s %s %v, uncounted %v", phase, id, ok, UncountedAttempt(row))
		}
		rows, err := ld.Checks(t.Context(), run, "build", 1, false)
		if err != nil {
			t.Fatal(err)
		}
		fr, ok := FailingReceipt(row, rows)
		if !ok || !FailedOutcome(fr) {
			t.Fatalf("no failing receipt of %d rows", len(rows))
		}
		if want := fmt.Sprintf("%s/%s/%d/%s/%s/%d", run, fr.TaskID, fr.N, fr.Phase, fr.ID, fr.K); c.Receipt != want {
			t.Fatalf("the engine handed %s; FailingReceipt finds %s", c.Receipt, want)
		}
	}
	t.Run("in the run", func(t *testing.T) {
		e, ld := sqlEngine(t)
		build := work("build", "lead")
		build.Checks = []Check{shStep("unit", fiveKiB)}
		run := launchManaged(t, e, repoCrew(newRepo(t), build))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		commitResult(t, e, a, "built.txt", "b\n", "built")
		c, _ := previousCheckOf(t, attemptN(t, e, run.ID, "build", 2))
		handedOf(t, ld, run.ID, c)
		stopRun(t, e, run.ID)
	})
	t.Run("in a continuation", func(t *testing.T) {
		src, _, ld, a2 := failAndContinue(t, shStep("unit", fiveKiB))
		c, _ := previousCheckOf(t, a2)
		handedOf(t, ld, src, c)
	})
	t.Run("an attempt not counted", func(t *testing.T) {
		for _, stage := range []string{"setup deps", "manifest", receiptStage} {
			if !UncountedAttempt(ledger.AttemptRow{State: ledger.AttemptFailed, Stage: stage}) {
				t.Fatalf("an attempt failed at %q with no result is counted", stage)
			}
		}
		if UncountedAttempt(ledger.AttemptRow{State: ledger.AttemptFailed, Stage: "check unit", ResultCommit: "abc"}) {
			t.Fatal("an attempt failed at its check is not counted")
		}
	})
}
