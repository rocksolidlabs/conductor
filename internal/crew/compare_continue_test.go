package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// I9, door 15: a comparison's continuation repairs no candidate before an
// applied selection selects it. Through the seam where each task's import
// and repair are decided, a continuation that leaves a succeeded lane task
// unimported, or gives a lane task a repair, is refused with its words and
// makes no run; so is RV1's repair continuation of a candidate (the shape
// its options take), with no command kept; a plain Continue of the same
// comparison is taken, the succeeded candidate imported.
func TestAComparisonsContinuationRepairsOnlyTheSelected(t *testing.T) {
	dir := newRepo(t)
	e, _, l := compareEngine(t)
	launched := launchManaged(t, e, compareCrew(t, dir, 2, false))
	runID := launched.ID
	a := waitAttempt(t, e, runID, "approach-a", ledger.AttemptRunning)
	waitAttempt(t, e, runID, "approach-b", ledger.AttemptRunning)
	commitResult(t, e, a, "a.txt", "keyed by locale\n", "keyed by locale")
	done := waitTask(t, e, runID, "approach-a", ledger.TaskSucceeded, 1)
	stopRun(t, e, runID)
	commit := done.Result.Commit

	const repaired = "a trial's candidate is repaired once it is selected"
	refused := func(what string, err error, words string) {
		t.Helper()
		if !errors.Is(err, ErrNotSelected) || errors.Is(err, ErrTaskState) || err.Error() != words {
			t.Fatalf("%s: %v", what, err)
		}
		if got := l.continuing(runID); len(got) != 0 {
			t.Fatalf("%s: the ledger holds the continuations %v", what, got)
		}
		if got := resumedIn(e, runID); len(got) != 0 {
			t.Fatalf("%s: the engine holds the continuations %v", what, got)
		}
	}
	ctx := context.Background()
	e.beforeCompareRule = func(r *run) { r.task("approach-a").imported = "" }
	_, err := e.ContinueRun(ctx, runID, "")
	refused("a succeeded candidate left unimported", err, "approach-a's result, attempt 1, is not in the ledger: a trial's candidate goes on only with its result; run a new trial")
	e.beforeCompareRule = func(r *run) { r.task("approach-b").repair = &repair{summary: "make it faster", commit: commit} }
	_, err = e.ContinueRun(ctx, runID, "")
	refused("a candidate given a repair", err, repaired)
	e.beforeCompareRule = nil
	_, err = continueWithRepair(e, runID, "rq1", "approach-a", 1, commit, "", personsRequest("rv-0001"))
	refused("RV1's repair continuation", err, repaired)
	if got := l.commandRuns("admin", "rq1"); len(got) != 0 {
		t.Fatalf("the repair's command is kept in %v", got)
	}

	next, err := e.ContinueRun(ctx, runID, "")
	if err != nil {
		t.Fatalf("the plain continuation: %v", err)
	}
	defer stopRun(t, e, next.ID)
	if ts := taskOf(t, e, next.ID, "approach-a"); ts.State != ledger.TaskSucceeded || ts.ImportedFrom == "" {
		t.Fatalf("approach-a in the continuation: %+v", ts)
	}
	// A run of no comparison is not judged by the rule.
	if err := (&run{}).compareContinued(nil, "", nil); err != nil {
		t.Fatal(err)
	}
}

// A review's request for changes reopens its candidate in the run, and
// the repair it gives is the run's own: stopped while the candidate's next
// attempt runs, or once that candidate has failed with its attempts spent
// still holding the repair, the comparison is continued, the repair
// carried as its row keeps it. A repair the continuation itself adds is
// still refused.
func TestAComparisonsReviewedRepairIsContinued(t *testing.T) {
	for _, spent := range []bool{false, true} {
		t.Run(map[bool]string{false: "repairing", true: "failed with the repair"}[spent], func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := compareEngine(t)
			launched := launchManaged(t, e, failingCompareCrew(t, dir, 2, true))
			submitEdits(t, e, attemptRunning(t, e, launched.ID, "approach-a", 1), "a", put("a.txt", "a\n"))
			rv := attemptRunning(t, e, launched.ID, "review-a", 1)
			submit(t, e, rv.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Summary: "say why", Findings: []Finding{{Severity: "major", Path: "a.txt", Line: 1, Message: "why"}}})
			attemptRunning(t, e, launched.ID, "approach-a", 2)
			if spent {
				failAttempts(t, e, launched.ID, "approach-a", 2, maxAttemptsPerTask)
				l.taskWhen(t, launched.ID, "approach-a", "failed keeping its repair", func(r ledger.TaskRow) bool { return r.State == ledger.TaskFailed && r.Repair != "" })
			} else {
				l.taskWhen(t, launched.ID, "approach-a", "with its repair", func(r ledger.TaskRow) bool { return r.Repair != "" })
			}
			stopRun(t, e, launched.ID)
			// A repair the continuation adds, beside the carried one, is refused.
			e.beforeCompareRule = func(r *run) { r.task("approach-b").repair = &repair{summary: "make it faster"} }
			if _, err := e.ContinueRun(context.Background(), launched.ID, ""); !errors.Is(err, ErrNotSelected) {
				t.Fatalf("a repair the continuation adds: %v", err)
			}
			e.beforeCompareRule = nil
			next, err := e.ContinueRun(context.Background(), launched.ID, "")
			if err != nil {
				t.Fatalf("the continuation: %v", err)
			}
			defer stopRun(t, e, next.ID)
			l.taskWhen(t, next.ID, "approach-a", "its repair carried", func(r ledger.TaskRow) bool { return r.Repair != "" })
		})
	}
}

// A comparison's chain holds at most maxCompareChain runs (the lead's
// decision: continued at most 255 times, no more than DR1's chain bound):
// the continuation of a run whose chain holds 256 is refused with its
// words and makes no run; one whose chain holds 255 is taken. The chain is
// the ledger's, read back along ContinuedFrom from the run continued.
func TestAComparisonsChainIsBounded(t *testing.T) {
	if maxCompareChain != 256 || maxCompareChain > maxChain {
		t.Fatalf("maxCompareChain %d, DR1's maxChain %d", maxCompareChain, maxChain)
	}
	for _, tc := range []struct {
		before int // runs before the one continued
		taken  bool
	}{{maxCompareChain - 1, false}, {maxCompareChain - 2, true}} {
		t.Run(fmt.Sprint(tc.before+1), func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := compareEngine(t)
			run := launchManaged(t, e, compareCrew(t, dir, 2, false))
			waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning)
			stopRun(t, e, run.ID)
			// The runs before it in the ledger, each continued by the next:
			// the chain is the ledger's, whatever the engine holds.
			l.mu.Lock()
			row := l.runs[run.ID]
			prev := ""
			for i := range tc.before {
				id := fmt.Sprintf("api-sweep-%08x", i+1)
				l.runs[id] = ledger.RunRow{ID: id, CrewID: row.CrewID, Crew: row.Crew, Base: row.Base, State: ledger.RunStopped, ContinuedFrom: prev, ContinuedBy: run.ID, CreatedAt: row.CreatedAt}
				if prev != "" {
					p := l.runs[prev]
					p.ContinuedBy = id
					l.runs[prev] = p
				}
				prev = id
			}
			row.ContinuedFrom = prev
			l.runs[run.ID] = row
			l.mu.Unlock()

			next, err := e.ContinueRun(context.Background(), run.ID, "")
			if tc.taken {
				if err != nil {
					t.Fatalf("a chain of %d runs: %v", tc.before+1, err)
				}
				stopRun(t, e, next.ID)
				return
			}
			want := fmt.Sprintf("this trial has been continued %d times: run a new trial", maxCompareChain-1)
			if !errors.Is(err, ErrChainTooLong) || err.Error() != want {
				t.Fatalf("a chain of %d runs: %v, want %q", tc.before+1, err, want)
			}
			if got := l.continuing(run.ID); len(got) != 0 || len(resumedIn(e, run.ID)) != 0 {
				t.Fatalf("a refused continuation made %v", got)
			}
			if strings.Contains(err.Error(), "continues more than") {
				t.Fatal("refused by DR1's bound, not the comparison's")
			}
		})
	}
}

// The bound on the ledger's own chain: 256 runs of a comparison, each
// continued by the next, as the ledger keeps them. The last one's
// continuation is refused with the comparison's words and makes no run,
// and its comparison is read whole, from the launched run.
func TestAComparisonsChainOf256IsReadWhole(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	raw, err := json.Marshal(compareCrew(t, dir, 2, false))
	if err != nil {
		t.Fatal(err)
	}
	base := head(t, dir)
	ctx := context.Background()
	var ids []string
	for i := range maxCompareChain {
		id := fmt.Sprintf("api-sweep-%08x", i+1)
		prev := ""
		if i > 0 {
			prev = ids[i-1]
		}
		if _, err := ld.Transition(ctx, id, func(tx *ledger.Tx) error {
			if err := tx.CreateRun(ledger.RunRow{ID: id, CrewID: "api-sweep", Crew: string(raw), Base: base, State: ledger.RunStopped, ContinuedFrom: prev, CreatedAt: tx.Now()}); err != nil {
				return err
			}
			if prev != "" {
				return tx.Continued(prev)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	last := ids[len(ids)-1]
	_, err = e.ContinueRun(ctx, last, "")
	if want := fmt.Sprintf("this trial has been continued %d times: run a new trial", maxCompareChain-1); !errors.Is(err, ErrChainTooLong) || err.Error() != want {
		t.Fatalf("the continuation of the 256th run: %v", err)
	}
	if rows, err := ld.Runs(ctx); err != nil || len(rows) != maxCompareChain {
		t.Fatalf("the ledger's runs: %d %v", len(rows), err)
	}
	c, err := e.Comparison(ctx, last)
	if err != nil || c.Run != ids[0] || len(c.Candidates) != 2 {
		t.Fatalf("the comparison of the last run: %+v %v", c, err)
	}
}
