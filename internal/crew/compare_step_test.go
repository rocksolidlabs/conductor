package crew

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// settles are the run's committed transactions that wrote compare.settled,
// in order.
func (l *fakeLedger) settles(runID string) []fakeCommit {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []fakeCommit
	for _, fc := range l.commits {
		if fc.runID == runID && slices.ContainsFunc(fc.events, func(ev fakeEvent) bool { return ev.kind == eventCompareSettled }) {
			out = append(out, fc)
		}
	}
	return out
}

// settledIn is the set a commit's compare.settled wrote.
func settledIn(t *testing.T, fc fakeCommit) []SettledCandidate {
	t.Helper()
	for _, ev := range fc.events {
		if ev.kind != eventCompareSettled {
			continue
		}
		p, _ := ev.payload.(map[string]any)
		set, ok := p["candidates"].([]SettledCandidate)
		if !ok {
			t.Fatalf("compare.settled's payload: %#v", ev.payload)
		}
		return set
	}
	t.Fatal("no compare.settled in the commit")
	return nil
}

// carriesRow reports whether the commit holds the task's row in the state.
func carriesRow(fc fakeCommit, task, state string) bool {
	return slices.ContainsFunc(fc.tasks, func(r ledger.TaskRow) bool { return r.ID == task && r.State == state })
}

// tasksOfSet are a settled set's tasks.
func tasksOfSet(set []SettledCandidate) []string {
	var out []string
	for _, sc := range set {
		out = append(out, sc.Task)
	}
	return out
}

// failingCompareCrew is a comparison whose candidates' one check fails on
// a result holding broken.txt.
func failingCompareCrew(t *testing.T, dir string, n int, reviews bool) Crew {
	t.Helper()
	c := compareCrew(t, dir, n, reviews)
	for _, cd := range c.Compare.Candidates {
		taskByID(&c, cd.Task).Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test ! -e broken.txt"}}}
	}
	return c
}

// attemptRunning waits for the task's attempt n to be running.
func attemptRunning(t *testing.T, e *Engine, runID, task string, n int) TaskState {
	t.Helper()
	return waitTaskWhere(t, e, runID, task, fmt.Sprintf("attempt %d running", n), func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
	})
}

// failAttempts has the task's attempts from..to submit a result its check
// fails.
func failAttempts(t *testing.T, e *Engine, runID, task string, from, to int) {
	t.Helper()
	for n := from; n <= to; n++ {
		submitEdits(t, e, attemptRunning(t, e, runID, task, n), fmt.Sprintf("broken %d", n), put("broken.txt", fmt.Sprintf("%s %d\n", task, n)))
	}
}

// compare.settled is written once for each settling of the lanes, by the
// compare step of the change that ends the last lane, with its task row:
// a review's needs_changes delays it; a failed lane counts as ended; a
// retried lane that fails again writes it again with the same set, and
// one that then succeeds with the new set; a pause and an unpause write
// none, nor does a stop's own change.
func TestCompareSettledIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	e, _, l := compareEngine(t)
	run := launchManaged(t, e, failingCompareCrew(t, dir, 2, true))
	submitEdits(t, e, attemptRunning(t, e, run.ID, "approach-a", 1), "a", put("a.txt", "a\n"))
	rv := attemptRunning(t, e, run.ID, "review-a", 1)
	submit(t, e, rv.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Summary: "say why", Findings: []Finding{{Severity: "major", Path: "a.txt", Line: 1, Message: "why"}}})

	// approach-b fails every attempt: its lane ends, its review doomed;
	// approach-a's lane is open again, so nothing is settled.
	failAttempts(t, e, run.ID, "approach-b", 1, maxAttemptsPerTask)
	l.taskWhen(t, run.ID, "approach-b", "failed", func(r ledger.TaskRow) bool { return r.State == ledger.TaskFailed })
	if n := len(l.settles(run.ID)); n != 0 {
		t.Fatalf("settled with approach-a reopened: %d", n)
	}

	// approach-a's repair, approved: the last lane's change settles them,
	// with review-a's row.
	submitEdits(t, e, attemptRunning(t, e, run.ID, "approach-a", 2), "a2", put("a.txt", "a, because\n"))
	rv = attemptRunning(t, e, run.ID, "review-a", 2)
	submit(t, e, rv.Attempt.SessionID, Result{Verdict: VerdictApproved, Summary: "good"})
	waitFake(t, "the first settling", func() bool { return len(l.settles(run.ID)) == 1 })
	first := l.settles(run.ID)[0]
	a := waitTask(t, e, run.ID, "approach-a", ledger.TaskSucceeded, 2)
	if !carriesRow(first, "review-a", ledger.TaskSucceeded) || !slices.Equal(settledIn(t, first), []SettledCandidate{{Task: "approach-a", Run: run.ID, Attempt: 2, Commit: a.Result.Commit}}) {
		t.Fatalf("the first settling: %+v", first)
	}

	// A pause and an unpause: the lanes as they were, nothing written.
	if _, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p1"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p2"}, false); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := l.Receipt(ctx, run.ID, "admin", "p2"); !found || len(l.settles(run.ID)) != 1 {
		t.Fatalf("after a pause and an unpause: %d settlings", len(l.settles(run.ID)))
	}

	// approach-b retried and failing again: settled again, the same set,
	// with its failed row.
	if _, err := e.Retry(ctx, run.ID, "admin", Command{ID: "r1"}, "approach-b"); err != nil {
		t.Fatal(err)
	}
	failAttempts(t, e, run.ID, "approach-b", maxAttemptsPerTask+1, maxAttemptsPerTask+1)
	waitFake(t, "the second settling", func() bool { return len(l.settles(run.ID)) == 2 })
	second := l.settles(run.ID)[1]
	if !carriesRow(second, "approach-b", ledger.TaskFailed) || !slices.Equal(settledIn(t, second), settledIn(t, first)) {
		t.Fatalf("the second settling: %+v", second)
	}

	// Retried again and succeeding, its review approving: the new set.
	if _, err := e.Retry(ctx, run.ID, "admin", Command{ID: "r2"}, "approach-b"); err != nil {
		t.Fatal(err)
	}
	b := attemptRunning(t, e, run.ID, "approach-b", maxAttemptsPerTask+2)
	submitEdits(t, e, b, "b", rm("broken.txt"), put("b.txt", "b\n"))
	rvb := attemptRunning(t, e, run.ID, "review-b", 1)
	submit(t, e, rvb.Attempt.SessionID, Result{Verdict: VerdictApproved, Summary: "good"})
	waitFake(t, "the third settling", func() bool { return len(l.settles(run.ID)) == 3 })
	third := l.settles(run.ID)[2]
	if got := tasksOfSet(settledIn(t, third)); !carriesRow(third, "review-b", ledger.TaskSucceeded) || !slices.Equal(got, []string{"approach-a", "approach-b"}) {
		t.Fatalf("the third settling: %v %+v", got, third)
	}

	// The stop's own change ends no lane: nothing written.
	stopRun(t, e, run.ID)
	if n := len(l.settles(run.ID)); n != 3 {
		t.Fatalf("after the stop: %d settlings", n)
	}
}

// A lane that ends while its run is being stopped (cancelled at a seam
// between the stop's start and its record) settles the lanes in its own
// change, with that lane's row; the stop's own change writes none.
func TestALaneEndingDuringAStopSettlesInItsOwnChange(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	e, _, l := compareEngine(t)
	run := launchManaged(t, e, compareCrew(t, dir, 2, false))
	submitEdits(t, e, attemptRunning(t, e, run.ID, "approach-a", 1), "a", put("a.txt", "a\n"))
	l.taskWhen(t, run.ID, "approach-a", "succeeded", func(r ledger.TaskRow) bool { return r.State == ledger.TaskSucceeded })
	attemptRunning(t, e, run.ID, "approach-b", 1)
	e.cmpMeasure.stopping = func(id string) {
		if _, err := e.Cancel(ctx, id, "admin", Command{ID: "c1"}, "approach-b"); err != nil {
			t.Error(err)
		}
	}
	stopRun(t, e, run.ID)
	got := l.settles(run.ID)
	if len(got) != 1 || !carriesRow(got[0], "approach-b", ledger.TaskCancelled) || !slices.Equal(tasksOfSet(settledIn(t, got[0])), []string{"approach-a"}) {
		t.Fatalf("settlings: %+v", got)
	}
	l.mu.Lock()
	last := l.commits[len(l.commits)-1]
	l.mu.Unlock()
	if slices.ContainsFunc(last.events, func(ev fakeEvent) bool { return ev.kind == eventCompareSettled }) {
		t.Fatalf("the stop's own change wrote compare.settled: %+v", last)
	}
}

// A continuation whose lanes are imported as the chain settled them writes
// no compare.settled (its compare step starts from the chain's newest);
// one that finds every lane ended with no settling recorded writes it in
// its first transaction.
func TestAContinuationSettlesOnlyWhatTheChainDidNot(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		t.Run(fmt.Sprintf("recorded=%v", recorded), func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := compareEngine(t)
			run := launchManaged(t, e, failingCompareCrew(t, dir, 2, false))
			submitEdits(t, e, attemptRunning(t, e, run.ID, "approach-a", 1), "a", put("a.txt", "a\n"))
			failAttempts(t, e, run.ID, "approach-b", 1, maxAttemptsPerTask)
			waitFake(t, "the settling", func() bool { return len(l.settles(run.ID)) == 1 })
			waitRunOutcome(t, l, run.ID)
			if !recorded {
				// As a ledger whose run settled before compare.settled was
				// written: the event is not there.
				l.mu.Lock()
				l.events = slices.DeleteFunc(l.events, func(ev fakeEvent) bool { return ev.kind == eventCompareSettled })
				l.mu.Unlock()
			}
			next, err := e.ContinueRun(t.Context(), run.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			got := l.settles(next.ID)
			switch {
			case recorded && len(got) != 0:
				t.Fatalf("the continuation settled again: %+v", got)
			case !recorded && (len(got) != 1 || !slices.ContainsFunc(got[0].events, func(ev fakeEvent) bool { return ev.kind == "run.continued" })):
				t.Fatalf("the continuation's first transaction did not settle: %+v", got)
			}
			if !recorded {
				set := settledIn(t, got[0])
				if len(set) != 1 || set[0].Task != "approach-a" || set[0].Run != run.ID || set[0].Attempt != 1 {
					t.Fatalf("its set: %+v", set)
				}
			}
		})
	}
}

// waitRunOutcome waits for the run's row to have an outcome.
func waitRunOutcome(t *testing.T, l *fakeLedger, runID string) {
	t.Helper()
	waitFake(t, "the run's outcome", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.runs[runID].Outcome != ""
	})
}

// With reviews on, a failed or cancelled candidate ends its lane: its
// review is blocked behind it (doomed) and never started, and the lanes
// settle with the succeeded candidate alone.
func TestAFailedCandidateEndsItsLane(t *testing.T) {
	for _, how := range []string{"failed", "cancelled"} {
		t.Run(how, func(t *testing.T) {
			ctx := context.Background()
			dir := newRepo(t)
			e, fl, l := compareEngine(t)
			run := launchManaged(t, e, failingCompareCrew(t, dir, 2, true))
			defer stopRun(t, e, run.ID)
			submitEdits(t, e, attemptRunning(t, e, run.ID, "approach-a", 1), "a", put("a.txt", "a\n"))
			rv := attemptRunning(t, e, run.ID, "review-a", 1)
			submit(t, e, rv.Attempt.SessionID, Result{Verdict: VerdictApproved, Summary: "good"})
			if how == "failed" {
				failAttempts(t, e, run.ID, "approach-b", 1, maxAttemptsPerTask)
			} else {
				attemptRunning(t, e, run.ID, "approach-b", 1)
				if _, err := e.Cancel(ctx, run.ID, "admin", Command{ID: "c1"}, "approach-b"); err != nil {
					t.Fatal(err)
				}
			}
			waitFake(t, "the settling", func() bool { return len(l.settles(run.ID)) == 1 })
			if set := settledIn(t, l.settles(run.ID)[0]); !slices.Equal(tasksOfSet(set), []string{"approach-a"}) {
				t.Fatalf("the set: %+v", set)
			}
			if rb := taskOf(t, e, run.ID, "review-b"); rb.State != ledger.TaskBlocked || rb.Attempts != 0 || rb.Attempt != nil {
				t.Fatalf("review-b: %+v", rb)
			}
			fl.mu.Lock()
			for name := range fl.procs {
				if len(name) >= 5 && name[:5] == "rev-b" {
					t.Errorf("a session for approach-b's reviewer: %s", name)
				}
			}
			fl.mu.Unlock()
			l.mu.Lock()
			_, hasAttempt := l.attempts[run.ID+"/review-b/1"]
			l.mu.Unlock()
			if hasAttempt {
				t.Fatal("review-b has an attempt row")
			}
			cmp := comparisonOf(t, e, run.ID)
			if !cmp.Settled || !column(t, cmp, "approach-a").Selectable || column(t, cmp, "approach-b").Selectable {
				t.Fatalf("the comparison: settled %v, %+v", cmp.Settled, cmp.Candidates)
			}
		})
	}
}
