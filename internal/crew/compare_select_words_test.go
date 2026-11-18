package crew

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A selection's words (docs/plans/compare-crew.md, PR-6a, Which record):
// each option, and the record's context, say of the candidate what the
// comparison says of its checks: each check's outcome and its row's
// verdict, with why in the comparison's own words. A person answering on
// Needs you, the ticket or a phone reads them there and never sees the
// comparison's rows. Each test reads the words from the decision record in
// the ledger, and the verdict from the comparison itself.

// launchWords launches a comparison of two candidates, without reviews,
// whose one check runs argv, over a base that also holds files.
func launchWords(t *testing.T, argv []string, files ...edit) *selectEnv {
	t.Helper()
	se := &selectEnv{dir: newRepo(t), ldir: t.TempDir()}
	if len(files) > 0 {
		applyEdits(t, se.dir, files)
		commitAll(t, se.dir, "the check")
	}
	se.e, se.fl, se.ld = decisionEngineAt(t, se.ldir)
	se.e.compareOpen = true
	se.c = compareCrew(t, se.dir, 2, false)
	for i := range se.c.Tasks {
		se.c.Tasks[i].Checks = []Check{{ID: "ok", Argv: argv}}
	}
	se.run = launchPlanned(t, se.e, se.c, planned(t, cmpRunID, head(t, se.dir)))
	return se
}

// verdictOf is the comparison's one check row as it reads now.
func verdictOf(t *testing.T, se *selectEnv) ComparedCheck {
	t.Helper()
	c, err := se.e.Comparison(t.Context(), se.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 1 {
		t.Fatalf("the comparison's rows: %+v", c.Checks)
	}
	return c.Checks[0]
}

// optionWords are the tradeoffs of a selection's options, by label.
func optionWords(b selectionBody) map[string]string {
	out := map[string]string{}
	for _, o := range b.Options {
		out[o.Label] = o.Tradeoffs
	}
	return out
}

func TestTheSelectionSaysTheComparisonsVerdict(t *testing.T) {
	t.Run("every check comparable", func(t *testing.T) {
		se := launchSelect(t, 2)
		defer stopRun(t, se.e, se.run.ID)
		a := se.pass(t, se.run.ID, "approach-a", 1)
		b := se.pass(t, se.run.ID, "approach-b", 1)
		body := selectBody(t, selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID))
		if rw := verdictOf(t, se); rw.Verdict != VerdictComparable {
			t.Fatalf("the comparison's row: %+v", rw)
		}
		words := optionWords(body)
		for _, c := range []TaskState{a, b} {
			label := c.ID + " at " + c.Result.Commit[:7]
			if got, want := words[label], "attempt 1; check ok: passed, comparable"; got != want {
				t.Fatalf("%s says %q, want %q", label, got, want)
			}
			if want := fmt.Sprintf("%s: %s, attempt 1; check ok: passed, comparable.", c.ID, c.Result.Commit[:7]); !strings.Contains(body.Context, want) {
				t.Fatalf("the context %q has no %q", body.Context, want)
			}
		}
	})

	t.Run("a check not comparable", func(t *testing.T) {
		// The check runs check.sh, and approach-a adds a case to it: the
		// comparison's row is not comparable, though both passed.
		se := launchWords(t, []string{"sh", "check.sh"}, put("check.sh", "test ! -e broken\n"))
		defer stopRun(t, se.e, se.run.ID)
		ts := waitTaskWhere(t, se.e, se.run.ID, "approach-a", "at attempt 1, running", func(ts TaskState) bool {
			return ts.Attempt != nil && ts.Attempt.N == 1 && ts.Attempt.State == ledger.AttemptRunning
		})
		commitResult(t, se.e, ts, "check.sh", "test ! -e broken\ntest ! -e broken.txt\n", "one more case")
		a := waitTask(t, se.e, se.run.ID, "approach-a", ledger.TaskSucceeded, 0)
		b := se.pass(t, se.run.ID, "approach-b", 1)
		body := selectBody(t, selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID))
		why := "approach-a changed check.sh, which the check runs"
		if rw := verdictOf(t, se); rw.Verdict != VerdictNotComparable || !whyHas(rw, why) {
			t.Fatalf("the comparison's row: %+v", rw)
		}
		words := optionWords(body)
		want := "attempt 1; check ok: passed, not comparable (" + why + ")"
		for _, c := range []TaskState{a, b} {
			label := c.ID + " at " + c.Result.Commit[:7]
			if got := words[label]; got != want {
				t.Fatalf("%s says %q, want %q", label, got, want)
			}
			if w := fmt.Sprintf("%s: %s, %s.", c.ID, c.Result.Commit[:7], want); !strings.Contains(body.Context, w) {
				t.Fatalf("the context %q has no %q", body.Context, w)
			}
		}
		if strings.Contains(body.Context, "its checks passed") {
			t.Fatalf("the context says its checks passed: %q", body.Context)
		}
	})

	t.Run("a failed check", func(t *testing.T) {
		// approach-b's every attempt fails the check: the selection has
		// approach-a, and its context says what approach-b's last commit
		// did; accepted anyway, approach-b's option says its check failed.
		se := launchSelect(t, 2)
		defer stopRun(t, se.e, se.run.ID)
		a := se.pass(t, se.run.ID, "approach-a", 1)
		failed := se.fail(t, se.run.ID, "approach-b")
		first := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		if rw := verdictOf(t, se); rw.Verdict != VerdictComparable {
			t.Fatalf("the comparison's row: %+v", rw)
		}
		fb := selectBody(t, first)
		passed := "attempt 1; check ok: passed, comparable"
		if got := optionWords(fb)["approach-a at "+a.Result.Commit[:7]]; got != passed {
			t.Fatalf("approach-a says %q", got)
		}
		n := failed.Attempt.N
		failedWords := fmt.Sprintf("approach-b: no result (failed); at %s, attempt %d: check ok: failed (exit 1), comparable.", failed.Attempt.Result[:7], n)
		if !strings.Contains(fb.Context, failedWords) {
			t.Fatalf("the context %q has no %q", fb.Context, failedWords)
		}

		if rc, err := se.e.Accept(t.Context(), se.run.ID, "admin", Command{ID: "a1"}, "approach-b"); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("accept: %+v %v", rc, err)
		}
		next := selectionWhen(t, se.ld, "a second, open", func(row ledger.DecisionRow) bool { return row.N != first.N && row.State == ledger.DecisionOpen }, se.run.ID)
		nb := selectBody(t, next)
		words := optionWords(nb)
		if got := words["approach-a at "+a.Result.Commit[:7]]; got != passed {
			t.Fatalf("approach-a says %q", got)
		}
		want := fmt.Sprintf("attempt %d; check ok: failed (exit 1), comparable; accepted after a failed check", n)
		if got := words["approach-b at "+failed.Attempt.Result[:7]]; got != want {
			t.Fatalf("approach-b says %q, want %q", got, want)
		}
	})
}

// With no comparison read before the opening, or one read before a change
// of the run committed, an option says that its checks were not compared:
// never a bare "its checks passed".
func TestAnOptionReadsOnlyACurrentComparison(t *testing.T) {
	exit0 := 0
	c := SettledCandidate{Task: "approach-a", Run: "r1", Attempt: 1, Commit: "abcdef0123"}
	cmp := Comparison{
		Candidates: []ComparedCandidate{{Task: "approach-a", Run: "r1", Attempt: 1, Commit: "abcdef0123"}},
		Checks: []ComparedCheck{{Phase: ledger.PhaseCheck, ID: "ok", Verdict: VerdictNotComparable, Why: []string{"approach-a changed check.sh, which the check runs"},
			Cells: []CheckCell{{Task: "approach-a", Attempt: 1, State: ledger.CheckFinished, Exit: &exit0}}}},
	}
	r := &run{id: "r1", revision: 7}
	fallback := "attempt 1; its checks passed, " + notCompared
	if got := r.tradeoffs(c); got != fallback {
		t.Fatalf("with none read: %q", got)
	}
	r.sel.measured = &aheadMeasure{rev: 6, cmp: cmp}
	if got := r.tradeoffs(c); got != fallback {
		t.Fatalf("with one read before a change: %q", got)
	}
	r.sel.measured.rev = 7
	if got, want := r.tradeoffs(c), "attempt 1; check ok: passed, not comparable (approach-a changed check.sh, which the check runs)"; got != want {
		t.Fatalf("with a current one: %q, want %q", got, want)
	}
	// Another commit of the lane than the comparison shows: not its words.
	other := c
	other.Commit = "0123abcdef"
	if got := r.tradeoffs(other); got != fallback {
		t.Fatalf("for another commit: %q", got)
	}
	// A change carried ahead of the ledger: the read is not the state.
	r.pendingEvents = []event{{"x", nil}}
	if got := r.tradeoffs(c); got != fallback {
		t.Fatalf("with a change carried: %q", got)
	}
}
