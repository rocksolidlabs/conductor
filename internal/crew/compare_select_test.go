package crew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A comparison's selection (docs/plans/compare-crew.md, PR-6a): a DR1
// record of origin compare that Yardmaster opens in the change ending the
// last lane, kept current with the lanes, answered only by the operator
// with a rationale, applied as how selected, kept over a stop and a
// restart, and never cancelled. Every test runs over the real ledger and
// waits on the ledger's record or the run's state for what it needs.

// cmpRunID is the run every test of this file launches.
const cmpRunID = "api-sweep-0000c0de"

// selectEnv is a comparison launched from CND-12's revision, its engine
// over the real ledger in ldir.
type selectEnv struct {
	e    *Engine
	fl   *fakeLauncher
	ld   *ledger.Ledger
	dir  string
	ldir string
	run  Run
	c    Crew
	ends atomic.Int32
}

// launchSelect launches a comparison of n candidates, without reviews,
// whose one check fails on a commit that holds the file "broken".
func launchSelect(t *testing.T, n int) *selectEnv {
	t.Helper()
	se := &selectEnv{dir: newRepo(t), ldir: t.TempDir()}
	se.e, se.fl, se.ld = decisionEngineAt(t, se.ldir)
	se.e.compareOpen = true
	se.e.OnEnd = func(Run) { se.ends.Add(1) }
	se.c = compareCrew(t, se.dir, n, false)
	for i := range se.c.Tasks {
		se.c.Tasks[i].Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test ! -e broken"}}}
	}
	se.run = launchPlanned(t, se.e, se.c, planned(t, cmpRunID, head(t, se.dir)))
	return se
}

// pass has the lane's running attempt n submit a result that passes its
// check, and waits for the task to succeed.
func (se *selectEnv) pass(t *testing.T, runID, task string, n int) TaskState {
	t.Helper()
	a := waitTaskWhere(t, se.e, runID, task, fmt.Sprintf("at attempt %d, running", n), func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
	})
	commitResult(t, se.e, a, task+".txt", fmt.Sprintf("%s %d\n", task, n), "the approach")
	return waitTask(t, se.e, runID, task, ledger.TaskSucceeded, 0)
}

// cancel cancels the lane's task: the lane ends with no result.
func (se *selectEnv) cancel(t *testing.T, runID, task, id string) {
	t.Helper()
	if rc, err := se.e.Cancel(t.Context(), runID, "admin", Command{ID: id}, task); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("cancel %s: %+v %v", task, rc, err)
	}
}

// fail has every attempt of the lane submit a result its check refuses,
// and waits for the task to fail: a lane ended with a submission, no
// standing result.
func (se *selectEnv) fail(t *testing.T, runID, task string) TaskState {
	t.Helper()
	return spend(t, se.e, runID, task, "broken")
}

// selections is the comparison's records in the runs given, oldest first.
func selections(t *testing.T, ld *ledger.Ledger, runs ...string) []ledger.DecisionRow {
	t.Helper()
	var out []ledger.DecisionRow
	for _, run := range runs {
		rows, err := ld.Decisions(context.Background(), ledger.DecisionFilter{CurrentRun: run})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Origin == ledger.OriginCompare {
				out = append(out, row)
			}
		}
	}
	slices.SortFunc(out, func(a, b ledger.DecisionRow) int { return int(a.N - b.N) })
	return out
}

// selectionWhen waits for the runs' newest selection to satisfy ok.
func selectionWhen(t *testing.T, ld *ledger.Ledger, what string, ok func(ledger.DecisionRow) bool, runs ...string) ledger.DecisionRow {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rows := selections(t, ld, runs...)
		if n := len(rows); n > 0 && ok(rows[n-1]) {
			return rows[n-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no selection of %v is %s after 20 s: %+v", runs, what, rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func rowIn(state string) func(ledger.DecisionRow) bool {
	return func(row ledger.DecisionRow) bool { return row.State == state }
}

// selectionsStanding counts the runs' selections open or answered.
func selectionsStanding(t *testing.T, ld *ledger.Ledger, runs ...string) int {
	t.Helper()
	n := 0
	for _, row := range selections(t, ld, runs...) {
		if row.State == ledger.DecisionOpen || row.State == ledger.DecisionAnswered {
			n++
		}
	}
	return n
}

// selectBody is a selection's request as the ledger keeps it.
func selectBody(t *testing.T, row ledger.DecisionRow) selectionBody {
	t.Helper()
	var b selectionBody
	if err := json.Unmarshal([]byte(row.Request), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// labels is a selection's options' labels.
func labels(b selectionBody) []string {
	var out []string
	for _, o := range b.Options {
		out = append(out, o.Label)
	}
	return out
}

// selectAs answers selection id with option i and a rationale, as the
// operator, and waits for nothing.
func selectAs(t *testing.T, e *Engine, id string, i, rev int) ResponseReceipt {
	t.Helper()
	return answer(t, e, id, Response{ResponseID: "pick-" + id, Choice: choice(i), Rationale: "the smaller change", ExpectedRev: rev})
}

// stateOf is the comparison's state read from the ledger.
func stateOf(t *testing.T, e *Engine, runID string) ComparisonState {
	t.Helper()
	st, err := e.ComparisonStateOf(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// eventsOf is the run's events as the ledger keeps them.
func eventsOf(t *testing.T, ld *ledger.Ledger, runID string) []ledger.EventRow {
	t.Helper()
	evs, err := ld.Events(context.Background(), runID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func countKind(evs []ledger.EventRow, kind string) int {
	n := 0
	for _, ev := range evs {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// stateWhen waits for the comparison's state, read from the ledger, to be
// state, and answers it.
func stateWhen(t *testing.T, e *Engine, runID, state string) ComparisonState {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := stateOf(t, e, runID)
		if st.State == state {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("the comparison is %+v after 20 s, not %s", st, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// outcomeOf is the run's outcome as the engine holds it, "" for none.
func outcomeOf(t *testing.T, e *Engine, runID string) string {
	t.Helper()
	r, ok := e.Get(runID)
	if !ok {
		t.Fatalf("run %s is gone", runID)
	}
	return r.Outcome
}

// The selection opens with every lane ended: one record of origin compare,
// an approval, its options the candidates with a standing result in the
// block's order, each naming its exact commit, then None of these; the
// comparison its subject with the digest of the options' subjects;
// recommending none, citing no evidence, holding no task, routed to the
// operator. With no lane standing none opens and the run settles; with one
// the record has it and None of these.
func TestSettledComparisonOpensItsSelection(t *testing.T) {
	t.Run("two of three stand", func(t *testing.T) {
		se := launchSelect(t, 3)
		defer stopRun(t, se.e, se.run.ID)
		a := se.pass(t, se.run.ID, "approach-a", 1)
		se.cancel(t, se.run.ID, "approach-b", "c1")
		if rows := selections(t, se.ld, se.run.ID); len(rows) != 0 {
			t.Fatalf("a selection while a lane runs: %+v", rows)
		}
		c := se.pass(t, se.run.ID, "approach-c", 1)
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		b := selectBody(t, row)
		if row.Type != ledger.DecisionApproval || row.TaskID != "" || row.Gate != "" || row.Member != "" || row.Ticket != testTicketID || row.Plan != testPlanID || row.Evidence != "[]" {
			t.Fatalf("the record: %+v", row)
		}
		if b.Question != "Which approach does CND-12 continue with?" || b.Recommendation == nil || *b.Recommendation != -1 || b.Authorizes != selectAuthorizes {
			t.Fatalf("the request: %+v", b)
		}
		want := []string{"approach-a at " + a.Result.Commit[:7], "approach-c at " + c.Result.Commit[:7], noneOfThese}
		if !slices.Equal(labels(b), want) {
			t.Fatalf("the options: %q, want %q", labels(b), want)
		}
		subjects := []*selectionSubject{
			{Kind: "candidate", Run: se.run.ID, Task: "approach-a", N: 1, Commit: a.Result.Commit},
			{Kind: "candidate", Run: se.run.ID, Task: "approach-c", N: 1, Commit: c.Result.Commit},
		}
		for i, s := range subjects {
			if got := b.Options[i].Subject; got == nil || *got != *s {
				t.Fatalf("option %d's subject: %+v, want %+v", i, got, s)
			}
		}
		if b.Options[2].Subject != nil {
			t.Fatalf("None of these names %+v", b.Options[2].Subject)
		}
		raw, _ := json.Marshal(subjects)
		sum := sha256.Sum256(raw)
		if s := b.Subject; s == nil || s.Kind != "comparison" || s.Run != se.run.ID || s.Base != head(t, se.dir) || s.Digest != hex.EncodeToString(sum[:]) {
			t.Fatalf("the record's subject: %+v", b.Subject)
		}
		for _, words := range []string{"no-deps: No new dependency.", "approach-b: no result (cancelled)", "approach-a: " + a.Result.Commit[:7]} {
			if !strings.Contains(b.Context, words) {
				t.Fatalf("the context %q has no %q", b.Context, words)
			}
		}
		rec := decisionWhen(t, se.ld, row.ID(), "open", inState(ledger.DecisionOpen))
		if !slices.Equal(historyKinds(rec), []string{ledger.HistoryOpened, ledger.HistoryRouted}) || len(rec.Held) != 0 || len(activeHolds(t, se.ld, se.run.ID)) != 0 {
			t.Fatalf("the record's history %q, holds %v", historyKinds(rec), rec.Held)
		}
		if !strings.Contains(rec.History[1].Detail, "local/1: the operator answers") {
			t.Fatalf("routed: %s", rec.History[1].Detail)
		}
		if st := stateOf(t, se.e, se.run.ID); st.State != CompareSelecting || st.Decision != row.ID() {
			t.Fatalf("the comparison: %+v", st)
		}
	})
	t.Run("none stands", func(t *testing.T) {
		se := launchSelect(t, 2)
		se.cancel(t, se.run.ID, "approach-a", "c1")
		se.cancel(t, se.run.ID, "approach-b", "c2")
		waitOutcome(t, se.e, se.run.ID, ledger.OutcomeCancelled)
		if rows := selections(t, se.ld, se.run.ID); len(rows) != 0 {
			t.Fatalf("a selection with no candidate: %+v", rows)
		}
		if st := stateOf(t, se.e, se.run.ID); st.State != CompareEnded || st.Decision != "" {
			t.Fatalf("the comparison: %+v", st)
		}
		if _, ok, err := se.e.SelectedLane(t.Context(), se.run.ID); ok || err != nil {
			t.Fatalf("a lane selected with none: %v %v", ok, err)
		}
	})
	t.Run("one stands", func(t *testing.T) {
		se := launchSelect(t, 2)
		defer stopRun(t, se.e, se.run.ID)
		se.cancel(t, se.run.ID, "approach-b", "c1")
		a := se.pass(t, se.run.ID, "approach-a", 1)
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		if got := labels(selectBody(t, row)); !slices.Equal(got, []string{"approach-a at " + a.Result.Commit[:7], noneOfThese}) {
			t.Fatalf("the options: %q", got)
		}
	})
}

// F1 of round 3: the change that ends the last lane opens the selection in
// its own transaction, read from the ledger: the record's decision.opened
// beside the lane's task.succeeded with nothing of another change between,
// no run.finished, no end told (OnEnd), the outcome empty; with no lane
// standing, the same change records run.finished and opens nothing.
func TestTheLastLaneOpensTheSelectionBeforeTheRunEnds(t *testing.T) {
	se := launchSelect(t, 2)
	defer stopRun(t, se.e, se.run.ID)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	evs := eventsOf(t, se.ld, se.run.ID)
	opened, last := -1, -1
	for i, ev := range evs {
		switch {
		case ev.Kind == "decision.opened" && strings.Contains(ev.Payload, row.ID()):
			opened = i
		case ev.Kind == "task.succeeded" && strings.Contains(ev.Payload, `"approach-b"`):
			last = i
		}
	}
	if opened < 0 || last < 0 || opened > last {
		t.Fatalf("decision.opened at %d, the last lane's task.succeeded at %d", opened, last)
	}
	for _, ev := range evs[opened+1 : last] {
		if ev.Kind != "compare.settled" {
			t.Fatalf("%s between the opening and the lane's end: not one change", ev.Kind)
		}
	}
	if n := countKind(evs, "run.finished"); n != 0 || outcomeOf(t, se.e, se.run.ID) != "" {
		t.Fatalf("run.finished %d, outcome %q", n, outcomeOf(t, se.e, se.run.ID))
	}
	// A later change commits after it: no end was pending.
	if rc, err := se.e.Pause(t.Context(), se.run.ID, "admin", Command{ID: "p1"}, true); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("pause: %+v %v", rc, err)
	}
	if n := se.ends.Load(); n != 0 {
		t.Fatalf("OnEnd called %d times before the selection", n)
	}

	t.Run("no lane stands", func(t *testing.T) {
		se := launchSelect(t, 2)
		se.cancel(t, se.run.ID, "approach-a", "c1")
		se.cancel(t, se.run.ID, "approach-b", "c2")
		waitOutcome(t, se.e, se.run.ID, ledger.OutcomeCancelled)
		evs := eventsOf(t, se.ld, se.run.ID)
		if countKind(evs, "run.finished") != 1 || countKind(evs, "decision.opened") != 0 {
			t.Fatalf("events: %d run.finished, %d decision.opened", countKind(evs, "run.finished"), countKind(evs, "decision.opened"))
		}
		// The end is the cancel's change: written after its command's event
		// and its settling's, nothing of another change between.
		i := slices.IndexFunc(evs, func(ev ledger.EventRow) bool { return ev.Kind == "run.finished" })
		j := slices.IndexFunc(evs, func(ev ledger.EventRow) bool {
			return ev.Kind == "command.cancel" && strings.Contains(ev.Payload, `"c2"`)
		})
		if j < 0 || i < j || slices.ContainsFunc(evs[j+1:i], func(ev ledger.EventRow) bool { return ev.Kind != "compare.settled" }) {
			t.Fatalf("run.finished at %d, the last cancel's command at %d", i, j)
		}
	})
}

// The selection holds the run open: running and idle with every lane
// ended, the ended-work rule never cancelling it, a pause and an unpause
// leaving it; a Stop leaves it open, the stopped run with no outcome and
// no run.finished.
func TestAnOpenSelectionKeepsTheRunOpen(t *testing.T) {
	se := launchSelect(t, 2)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	for i, on := range []bool{true, false} {
		if rc, err := se.e.Pause(t.Context(), se.run.ID, "admin", Command{ID: fmt.Sprintf("p%d", i)}, on); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("pause %v: %+v %v", on, rc, err)
		}
	}
	got, _ := se.e.Get(se.run.ID)
	if got.State != RunRunning || got.Outcome != "" {
		t.Fatalf("the run while selecting: %s %q", got.State, got.Outcome)
	}
	stopRun(t, se.e, se.run.ID)
	rec := decisionWhen(t, se.ld, row.ID(), "open", inState(ledger.DecisionOpen))
	if rec.Rev != row.Rev || kindCount(rec, ledger.HistoryCancelled) != 0 {
		t.Fatalf("the stop changed the selection: %+v %q", rec.DecisionRow, historyKinds(rec))
	}
	runRow, ok, err := se.ld.Run(t.Context(), se.run.ID)
	if err != nil || !ok || runRow.Outcome != "" || countKind(eventsOf(t, se.ld, se.run.ID), "run.finished") != 0 {
		t.Fatalf("the stopped run: %+v %v %v", runRow, ok, err)
	}
	if st := stateOf(t, se.e, se.run.ID); st.State != CompareSelecting {
		t.Fatalf("the stopped comparison: %+v", st)
	}
}

// I8: any change to a lane before the application supersedes the
// selection in that change, read from the ledger with the command's own
// row: a Retry of a cancelled lane supersedes it and opens nothing until
// every lane has ended again, then one record whose request names the old;
// an Accept anyway of a failed lane supersedes it and opens the next in
// the same change. At most one is ever open or answered, and a response to
// a superseded one is refused.
func TestAnyLaneChangeSupersedesTheSelection(t *testing.T) {
	t.Run("a retry", func(t *testing.T) {
		se := launchSelect(t, 3)
		defer stopRun(t, se.e, se.run.ID)
		se.pass(t, se.run.ID, "approach-a", 1)
		se.pass(t, se.run.ID, "approach-b", 1)
		se.cancel(t, se.run.ID, "approach-c", "c1")
		first := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		if rc, err := se.e.Retry(t.Context(), se.run.ID, "admin", Command{ID: "r1"}, "approach-c"); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("retry: %+v %v", rc, err)
		}
		// The Retry's own change superseded it: read once the command returns.
		rec := decisionWhen(t, se.ld, first.ID(), "superseded", inState(ledger.DecisionSuperseded))
		if !strings.Contains(rec.Why, "approach-c runs again: "+first.ID()+" no longer lists what is compared") {
			t.Fatalf("the words: %q", rec.Why)
		}
		evs := eventsOf(t, se.ld, se.run.ID)
		sup := slices.IndexFunc(evs, func(ev ledger.EventRow) bool { return ev.Kind == "decision.superseded" })
		cmd := slices.IndexFunc(evs, func(ev ledger.EventRow) bool { return ev.Kind == "command.retry" })
		if sup < 0 || cmd < sup || slices.ContainsFunc(evs[sup:cmd], func(ev ledger.EventRow) bool { return ev.Kind == "command.accepted" }) {
			t.Fatalf("the supersession at %d, the retry's command at %d", sup, cmd)
		}
		if n := selectionsStanding(t, se.ld, se.run.ID); n != 0 {
			t.Fatalf("%d selections stand while approach-c runs", n)
		}
		if st := stateOf(t, se.e, se.run.ID); st.State != CompareRunning {
			t.Fatalf("the comparison while approach-c runs: %+v", st)
		}
		_, err := se.e.RespondDecision(t.Context(), first.ID(), Response{ResponseID: "late", Choice: choice(0), Rationale: "late", ExpectedRev: rec.Rev}, "admin", workbench(se.e))
		if de := (*DecisionError)(nil); !errors.As(err, &de) || de.Code != CodeDecisionClosed {
			t.Fatalf("a response to the superseded one: %v", err)
		}
		se.pass(t, se.run.ID, "approach-c", 2)
		next := selectionWhen(t, se.ld, "a second, open", func(row ledger.DecisionRow) bool { return row.N != first.N && row.State == ledger.DecisionOpen }, se.run.ID)
		if b := selectBody(t, next); b.Supersedes != first.ID() || len(b.Options) != 4 {
			t.Fatalf("the next one: supersedes %q, options %q", b.Supersedes, labels(b))
		}
		if n := selectionsStanding(t, se.ld, se.run.ID); n != 1 {
			t.Fatalf("%d selections stand", n)
		}
	})
	t.Run("an accept anyway after none stood", func(t *testing.T) {
		se := launchSelect(t, 2)
		defer stopRun(t, se.e, se.run.ID)
		se.fail(t, se.run.ID, "approach-a")
		se.fail(t, se.run.ID, "approach-b")
		waitOutcome(t, se.e, se.run.ID, ledger.OutcomeFailed)
		stateWhen(t, se.e, se.run.ID, CompareEnded)
		if rows := selections(t, se.ld, se.run.ID); len(rows) != 0 {
			t.Fatalf("a selection with no candidate: %+v", rows)
		}
		finished := countKind(eventsOf(t, se.ld, se.run.ID), "run.finished")
		if rc, err := se.e.Accept(t.Context(), se.run.ID, "admin", Command{ID: "a1"}, "approach-a"); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("accept: %+v %v", rc, err)
		}
		// Opened in the accept's own change: there once the command returns.
		rows := selections(t, se.ld, se.run.ID)
		a := taskOf(t, se.e, se.run.ID, "approach-a")
		if len(rows) != 1 || rows[0].State != ledger.DecisionOpen || !slices.Equal(labels(selectBody(t, rows[0])), []string{"approach-a at " + a.Result.Commit[:7], noneOfThese}) {
			t.Fatalf("the records after the accept: %+v", rows)
		}
		if st := stateOf(t, se.e, se.run.ID); st.State != CompareSelecting {
			t.Fatalf("the comparison after the accept: %+v", st)
		}
		if n := countKind(eventsOf(t, se.ld, se.run.ID), "run.finished"); n != finished || outcomeOf(t, se.e, se.run.ID) != "" {
			t.Fatalf("run.finished %d after the accept, was %d; outcome %q", n, finished, outcomeOf(t, se.e, se.run.ID))
		}
	})
	t.Run("an accept anyway", func(t *testing.T) {
		se := launchSelect(t, 2)
		defer stopRun(t, se.e, se.run.ID)
		se.pass(t, se.run.ID, "approach-a", 1)
		failed := se.fail(t, se.run.ID, "approach-b")
		first := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		if got := labels(selectBody(t, first)); len(got) != 2 {
			t.Fatalf("a failed lane is an option: %q", got)
		}
		if rc, err := se.e.Accept(t.Context(), se.run.ID, "admin", Command{ID: "a1"}, "approach-b"); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("accept: %+v %v", rc, err)
		}
		rec := decisionWhen(t, se.ld, first.ID(), "superseded", inState(ledger.DecisionSuperseded))
		if !strings.Contains(rec.Why, "approach-b now has a result at "+failed.Attempt.Result[:7]) {
			t.Fatalf("the words: %q", rec.Why)
		}
		// Opened in the same change: there as soon as the command returns.
		rows := selections(t, se.ld, se.run.ID)
		if len(rows) != 2 || rows[1].State != ledger.DecisionOpen {
			t.Fatalf("the records after the accept: %+v", rows)
		}
		b := selectBody(t, rows[1])
		if b.Supersedes != first.ID() || !slices.Equal(labels(b)[1:], []string{"approach-b at " + failed.Attempt.Result[:7], noneOfThese}) {
			t.Fatalf("the next one: %+v", b)
		}
		if !strings.Contains(b.Options[1].Tradeoffs, "accepted after a failed check") {
			t.Fatalf("its tradeoffs: %q", b.Options[1].Tradeoffs)
		}
	})
}

// I8: a response recorded, then a lane retried before the application's
// transaction (held at its seam): the record superseded with its response
// kept, nothing applied, no compare.selected.
func TestAChangeBetweenAnswerAndApplicationAppliesNothing(t *testing.T) {
	se := launchSelect(t, 3)
	defer stopRun(t, se.e, se.run.ID)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	se.cancel(t, se.run.ID, "approach-c", "c1")
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	at, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	se.e.beforeApply = func(n int64) {
		if n == row.N {
			once.Do(func() { close(at); <-gate })
		}
	}
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		selectAs(t, se.e, row.ID(), 0, row.Rev)
	}()
	<-at
	decisionWhen(t, se.ld, row.ID(), "answered", inState(ledger.DecisionAnswered))
	if rc, err := se.e.Retry(t.Context(), se.run.ID, "admin", Command{ID: "r1"}, "approach-c"); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("retry: %+v %v", rc, err)
	}
	close(gate)
	<-answered
	rec := decisionWhen(t, se.ld, row.ID(), "superseded", inState(ledger.DecisionSuperseded))
	if rec.Applied != "" || len(rec.Responses) != 1 || rec.Responses[0].Status != ledger.ResponseAccepted || kindCount(rec, ledger.HistoryApplied) != 0 {
		t.Fatalf("the superseded selection: %+v %q", rec.DecisionRow, historyKinds(rec))
	}
	// The pump that follows the held application has run: nothing applied.
	if rc, err := se.e.Pause(t.Context(), se.run.ID, "admin", Command{ID: "p1"}, true); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("pause: %+v %v", rc, err)
	}
	if n := countKind(eventsOf(t, se.ld, se.run.ID), "compare.selected"); n != 0 {
		t.Fatalf("compare.selected %d times", n)
	}
	if _, ok, _ := se.e.SelectedLane(t.Context(), se.run.ID); ok {
		t.Fatal("a lane is selected")
	}
}

// The application records how selected with the candidate (selected), or
// None of these (none), writes compare.selected and lets the run settle in
// its own transaction.
func TestTheSelectionIsApplied(t *testing.T) {
	t.Run("a candidate", func(t *testing.T) {
		se := launchSelect(t, 2)
		se.pass(t, se.run.ID, "approach-a", 1)
		b := se.pass(t, se.run.ID, "approach-b", 1)
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		selectAs(t, se.e, row.ID(), 1, row.Rev)
		rec := decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
		want := SettledCandidate{Task: "approach-b", Run: se.run.ID, Attempt: 1, Commit: b.Result.Commit}
		if app := applied(t, rec); app.How != HowSelected || app.Selected == nil || *app.Selected != want || app.None || app.Words != "approach-b selected at "+b.Result.Commit[:7] {
			t.Fatalf("the application: %+v", app)
		}
		waitOutcome(t, se.e, se.run.ID, ledger.OutcomeSucceeded)
		evs := eventsOf(t, se.ld, se.run.ID)
		i := slices.IndexFunc(evs, func(ev ledger.EventRow) bool { return ev.Kind == "compare.selected" })
		if i < 0 || countKind(evs, "compare.selected") != 1 || countKind(evs, "run.finished") != 1 || evs[i+1].Kind != "run.finished" {
			t.Fatalf("events: %+v", evs)
		}
		var got map[string]any
		_ = json.Unmarshal([]byte(evs[i].Payload), &got)
		if got["decision"] != row.ID() || got["task"] != "approach-b" || got["commit"] != b.Result.Commit || got["run"] != se.run.ID {
			t.Fatalf("compare.selected: %s", evs[i].Payload)
		}
		sel, ok, err := se.e.SelectedLane(t.Context(), se.run.ID)
		if err != nil || !ok || sel.Task != "approach-b" || sel.Commit != b.Result.Commit || sel.None || sel.Decision != row.ID() {
			t.Fatalf("the selected lane: %+v %v %v", sel, ok, err)
		}
		if st := stateOf(t, se.e, se.run.ID); st.State != CompareSelected {
			t.Fatalf("the comparison: %+v", st)
		}
	})
	t.Run("none of these", func(t *testing.T) {
		se := launchSelect(t, 2)
		se.pass(t, se.run.ID, "approach-a", 1)
		se.pass(t, se.run.ID, "approach-b", 1)
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		selectAs(t, se.e, row.ID(), 2, row.Rev)
		rec := decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
		if app := applied(t, rec); app.How != HowSelected || app.Selected != nil || !app.None {
			t.Fatalf("the application: %+v", app)
		}
		waitOutcome(t, se.e, se.run.ID, ledger.OutcomeSucceeded)
		evs := eventsOf(t, se.ld, se.run.ID)
		i := slices.IndexFunc(evs, func(ev ledger.EventRow) bool { return ev.Kind == "compare.selected" })
		if i < 0 || evs[i].Payload != `{"decision":"`+row.ID()+`","none":true}` {
			t.Fatalf("compare.selected: %+v", evs)
		}
		if sel, ok, _ := se.e.SelectedLane(t.Context(), se.run.ID); !ok || !sel.None {
			t.Fatalf("the selection: %+v %v", sel, ok)
		}
		if st := stateOf(t, se.e, se.run.ID); st.State != CompareNone {
			t.Fatalf("the comparison: %+v", st)
		}
	})
}

// refusedUnselected says err is the not_selected refusal in its words,
// with no command row written for id.
// I9: after the application every task command on a lane not selected is
// refused not_selected before any write, and after None of these on every
// lane; the selected lane's commands are its own.
// I7: the engine refuses a response to a selection with no rationale,
// invalid with its words and no row, whatever path calls it; DR1's 2 KiB
// bound is kept; one with a rationale is taken.
// I7: only the operator answers a selection. An identity's principal with
// the decide grant and not the admin's, and a decision router's answer,
// are refused not_approver with no answer; no route takes the record,
// to the decide grant, a person, a role or the operator, from the operator
// or a Brakeman turn, and the decide grant reads nothing of it, whatever
// its request names.
// Only an answer ends a selection: the operator's cancel of an open and of
// an answered one is refused invalid with its words and no history row,
// the record unchanged; None of these, with its rationale, ends the
// comparison, after which a pause and an unpause, a Stop and a Continue
// open no record.
// RV1's Check again on a comparison: before an applied selection, a Check
// again of a failed lane makes a new attempt, which supersedes the record
// in its change; after it, one of a lane not selected is refused
// not_selected before any write.
// A run with no room for one more decision opens no selection when its
// last lane ends: the comparison ends with none, in words, and the run
// settles; its continuation, with room of its own, opens the selection in
// its first transaction.
func TestAFullRunOpensNoSelection(t *testing.T) {
	se := launchSelect(t, 2)
	se.pass(t, se.run.ID, "approach-a", 1)
	fillDecisions(t, se.ld, se.run.ID, ledger.MaxDecisionsPerRun)
	se.e.mu.Lock()
	r := se.e.runs[se.run.ID]
	r.dec.opened += ledger.MaxDecisionsPerRun
	se.e.mu.Unlock()
	se.pass(t, se.run.ID, "approach-b", 1)
	waitOutcome(t, se.e, se.run.ID, ledger.OutcomeSucceeded)
	if rows := selections(t, se.ld, se.run.ID); len(rows) != 0 {
		t.Fatalf("a selection past the bound: %+v", rows)
	}
	se.e.mu.Lock()
	words := r.sel.ended
	se.e.mu.Unlock()
	if words != "this run has recorded 1,000 decisions: no room for a selection: continue the run to select in its continuation, or launch a new comparison" {
		t.Fatalf("the words: %q", words)
	}
	// The ledger's own read, once the change that ended the last lane has
	// committed: the engine's view is ahead of it until then.
	stateWhen(t, se.e, se.run.ID, CompareEnded)
	if _, ok, _ := se.e.SelectedLane(t.Context(), se.run.ID); ok {
		t.Fatal("a lane is selected")
	}
	se.e.ForgetForTest(se.run.ID)
	next := continueRun(t, se.e, se.run.ID)
	defer stopRun(t, se.e, next.ID)
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), next.ID)
	if row.RunID != next.ID || len(selectBody(t, row).Options) != 3 {
		t.Fatalf("the continuation's selection: %+v", row)
	}
	if outcomeOf(t, se.e, next.ID) != "" {
		t.Fatal("the continuation settled with its selection open")
	}
}

// A Stop with the selection open leaves it open, not cancelled, with the
// same options; the comparison is selecting on the stopped run; the
// operator's answer through the ledger is recorded there and nothing is
// applied; Continue moves it answered and applies it right after its first
// transaction, the lanes not selected starting nothing. A Stop of an open
// selection and a Continue with no answer move it, still open.
// A restart between the selection's opening and its answer: the record
// open, the comparison selecting on the run not held; the workbench
// token's answer recorded there and nothing applied; Continue applies it
// right after the continuation's first transaction, compare.selected in
// the application's, the lanes not selected starting nothing.
// A continuation keeps an open selection: after a restart, Continue with
// every candidate imported unchanged moves the record, still open and not
// superseded, opens no other, and its run waits, running and idle; the
// selection answered there is applied there.
func TestAContinuationKeepsAnOpenSelection(t *testing.T) {
	se := launchSelect(t, 2)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	e2, _, ld2 := restart(t, se.e, se.ld, se.ldir, se.run.ID)
	e2.compareOpen = true
	next := continueRun(t, e2, se.run.ID)
	defer stopRun(t, e2, next.ID)
	rec := decisionWhen(t, ld2, row.ID(), "moved", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == next.ID })
	if rec.State != ledger.DecisionOpen || len(selections(t, ld2, se.run.ID, next.ID)) != 1 {
		t.Fatalf("the moved selection: %+v", rec.DecisionRow)
	}
	got, _ := e2.Get(next.ID)
	if got.State != RunRunning || got.Outcome != "" {
		t.Fatalf("the continuation while selecting: %s %q", got.State, got.Outcome)
	}
	selectAs(t, e2, row.ID(), 0, rec.Rev)
	if app := applied(t, decisionWhen(t, ld2, row.ID(), "applied", inState(ledger.DecisionApplied))); app.Run != next.ID {
		t.Fatalf("applied in %s", app.Run)
	}
	waitOutcome(t, e2, next.ID, ledger.OutcomeSucceeded)
}

// The applied selection crosses the chain: a plain Continue and a
// restart's Continue each read it before scheduling, and a Retry of a lane
// not selected is refused there. The chain a comparison may grow to
// (maxCompareChain) is within what a continuation reads of its decisions
// whole (DR1's maxChain), so the applied record is always read.
