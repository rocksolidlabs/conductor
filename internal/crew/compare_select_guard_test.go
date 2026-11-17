package crew

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Who may answer a comparison's selection and what its lanes take once it
// is applied (compare_select_guard.go), over the real ledger, with the
// helpers of compare_select_test.go: the selection kept over a stop and a
// restart and across the chain, refusing the lanes not selected.

func refusedUnselected(t *testing.T, se *selectEnv, runID, id, words string, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotSelected) || errors.Is(err, ErrTaskState) || !strings.Contains(err.Error(), words) {
		t.Fatalf("%s: %v, want not_selected %q", id, err, words)
	}
	if _, found, lerr := se.ld.Receipt(t.Context(), runID, "admin", id); lerr != nil || found {
		t.Fatalf("%s left a command row: %v %v", id, found, lerr)
	}
}

func TestUnselectedLanesRefuseCommands(t *testing.T) {
	t.Run("a candidate", func(t *testing.T) {
		se := launchSelect(t, 3)
		se.pass(t, se.run.ID, "approach-a", 1)
		failed := se.fail(t, se.run.ID, "approach-b")
		se.cancel(t, se.run.ID, "approach-c", "c1")
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		selectAs(t, se.e, row.ID(), 0, row.Rev)
		decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
		before := countKind(eventsOf(t, se.ld, se.run.ID), "command.accepted")
		ctx := t.Context()
		_, err := se.e.Retry(ctx, se.run.ID, "admin", Command{ID: "r1"}, "approach-c")
		refusedUnselected(t, se, se.run.ID, "r1", "approach-c was not selected in "+row.ID()+": launch a new comparison to try it again", err)
		_, err = se.e.Retry(ctx, se.run.ID, "admin", Command{ID: "r2"}, "approach-b")
		refusedUnselected(t, se, se.run.ID, "r2", "approach-b was not selected in "+row.ID(), err)
		_, err = se.e.Accept(ctx, se.run.ID, "admin", Command{ID: "a1"}, "approach-b")
		refusedUnselected(t, se, se.run.ID, "a1", "approach-b was not selected", err)
		_, err = recheck(se.e, se.run.ID, "k1", "approach-b", failed.Attempt.N, failed.Attempt.Result)
		refusedUnselected(t, se, se.run.ID, "k1", "approach-b was not selected", err)
		if n := countKind(eventsOf(t, se.ld, se.run.ID), "command.accepted"); n != before {
			t.Fatalf("command.accepted %d, was %d", n, before)
		}
		// The selected lane's commands are judged as any run's: a Retry of
		// a task that succeeded is taken, and refused by its state.
		rc, err := se.e.Retry(ctx, se.run.ID, "admin", Command{ID: "r3"}, "approach-a")
		if errors.Is(err, ErrNotSelected) || rc.CommandID != "r3" || rc.Status != ledger.CommandFailed {
			t.Fatalf("a retry of the selected lane: %+v %v", rc, err)
		}
	})
	t.Run("none of these", func(t *testing.T) {
		se := launchSelect(t, 2)
		se.pass(t, se.run.ID, "approach-a", 1)
		se.cancel(t, se.run.ID, "approach-b", "c1")
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		selectAs(t, se.e, row.ID(), 1, row.Rev)
		decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
		for i, task := range []string{"approach-a", "approach-b"} {
			id := fmt.Sprintf("r%d", i)
			_, err := se.e.Retry(t.Context(), se.run.ID, "admin", Command{ID: id}, task)
			refusedUnselected(t, se, se.run.ID, id, "no candidate was selected in "+row.ID()+" (None of these): launch a new comparison to try "+task+" again", err)
		}
	})
}

func TestASelectionNeedsARationale(t *testing.T) {
	se := launchSelect(t, 2)
	defer stopRun(t, se.e, se.run.ID)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	for name, why := range map[string]string{"empty": "", "blank": " \t ", "past 2 KiB": strings.Repeat("x", maxRationale+1)} {
		_, err := se.e.RespondDecision(t.Context(), row.ID(), Response{ResponseID: "r-" + strings.ReplaceAll(name, " ", "-"), Choice: choice(0), Rationale: why, ExpectedRev: row.Rev}, "admin", workbench(se.e))
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeInvalid {
			t.Fatalf("%s rationale: %v", name, err)
		}
		if why == "" && de.Words != selectRationaleSay {
			t.Fatalf("the words: %q", de.Words)
		}
	}
	rec := decisionWhen(t, se.ld, row.ID(), "open", inState(ledger.DecisionOpen))
	if len(rec.Responses) != 0 || kindCount(rec, ledger.HistoryResponded) != 0 {
		t.Fatalf("a refused response left rows: %+v", rec.Responses)
	}
	selectAs(t, se.e, row.ID(), 0, row.Rev)
	decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
}

func TestOnlyTheOperatorAnswersTheSelection(t *testing.T) {
	se := launchSelect(t, 2)
	defer stopRun(t, se.e, se.run.ID)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	ctx := t.Context()
	decider := Authority{Grant: string(ext.GrantDecide), Via: ViaIdentity, Subject: "ana", Boot: se.e.Boot()}
	rc, err := se.e.RespondDecision(ctx, row.ID(), Response{ResponseID: "d1", Choice: choice(0), Rationale: "mine", ExpectedRev: row.Rev}, ext.SubjectActor+"ana", decider)
	if de := (*DecisionError)(nil); !errors.As(err, &de) || de.Code != CodeNotApprover || rc.Status == ledger.ResponseAccepted {
		t.Fatalf("the decide grant's answer: %+v %v", rc, err)
	}
	rc, err = se.e.RespondDecision(ctx, row.ID(), Response{ResponseID: RouterResponseID, Choice: choice(0), Rationale: "routed", ExpectedRev: row.Rev}, ext.SubjectActor+"bot", Authority{Via: ViaRouter, Subject: "bot"})
	if de := (*DecisionError)(nil); !errors.As(err, &de) || de.Code != CodeNotApprover || rc.Status == ledger.ResponseAccepted {
		t.Fatalf("a router's answer: %+v %v", rc, err)
	}
	for _, tc := range []struct {
		actor string
		to    []Destination
	}{
		{"admin", []Destination{{Kind: DestGrant, Grant: string(ext.GrantDecide)}}},
		{"admin", []Destination{{Kind: "person", Name: "ana"}}},
		{"admin", []Destination{{Kind: "role", Name: "architecture"}}},
		{"admin", []Destination{{Kind: "operator"}}},
		{ledger.TurnActor, []Destination{{Kind: DestGrant, Grant: string(ext.GrantDecide)}}},
		{ledger.TurnActor, []Destination{{Kind: "operator"}}},
	} {
		_, err := se.e.RouteDecision(ctx, row.ID(), "op-"+tc.actor+"-"+tc.to[0].Kind, row.Rev, tc.to, "", tc.actor)
		if de := (*DecisionError)(nil); !errors.As(err, &de) || de.Code != CodeInvalid || !strings.Contains(de.Words, "it is not routed") {
			t.Fatalf("a route by %s to %+v: %v", tc.actor, tc.to, err)
		}
	}
	rec := decisionWhen(t, se.ld, row.ID(), "open", inState(ledger.DecisionOpen))
	if rec.Rev != row.Rev || kindCount(rec, ledger.HistoryRouted) != 1 || GivenToGrant(rec.Request) {
		t.Fatalf("the refusals changed the selection: %+v %q", rec.DecisionRow, historyKinds(rec))
	}
	for _, r := range rec.Responses {
		if r.Status == ledger.ResponseAccepted {
			t.Fatalf("an accepted response: %+v", r)
		}
	}
	// Whatever its request were to say, the grant admits nobody to it.
	given := rec
	given.Request = strings.Replace(rec.Request, `"recommendation":-1`, `"recommendation":-1,"for":{"label":"grant:decide","person":""}`, 1)
	who := Admittee{Grants: []string{string(ext.GrantDecide)}, Via: ViaIdentity, Subject: "ana"}
	if AdmissionOf(given, who) != AdmitNone || Admits(EntriesOf(given), who) {
		t.Fatal("the decide grant is admitted to a selection")
	}
	if AdmissionOf(given, Admittee{Grants: []string{grantAdmin}, Via: ViaWorkbench}) != AdmitWhole {
		t.Fatal("the operator is not admitted")
	}
	selectAs(t, se.e, row.ID(), 0, row.Rev)
	decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
}

func TestOnlyAnAnswerEndsTheSelection(t *testing.T) {
	se := launchSelect(t, 2)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	cancelRefused := func(id string, rev int) {
		t.Helper()
		before := decisionWhen(t, se.ld, row.ID(), "read", func(ledger.DecisionRecord) bool { return true })
		_, err := se.e.CancelDecision(t.Context(), row.ID(), id, rev, true, "not needed", "admin")
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeInvalid || de.Words != row.ID()+": "+selectNoCancel {
			t.Fatalf("the cancel: %v", err)
		}
		after := decisionWhen(t, se.ld, row.ID(), "read", func(ledger.DecisionRecord) bool { return true })
		if after.State != before.State || after.Rev != before.Rev || len(after.History) != len(before.History) {
			t.Fatalf("the cancel changed the record: %+v", after.DecisionRow)
		}
	}
	cancelRefused("x1", row.Rev)
	at, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	se.e.beforeApply = func(int64) { once.Do(func() { close(at); <-gate }) }
	done := make(chan struct{})
	go func() {
		defer close(done)
		selectAs(t, se.e, row.ID(), 2, row.Rev)
	}()
	<-at
	answered := decisionWhen(t, se.ld, row.ID(), "answered", inState(ledger.DecisionAnswered))
	cancelRefused("x2", answered.Rev)
	close(gate)
	<-done
	decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
	waitOutcome(t, se.e, se.run.ID, ledger.OutcomeSucceeded)
	for i, on := range []bool{true, false} {
		if rc, err := se.e.Pause(t.Context(), se.run.ID, "admin", Command{ID: fmt.Sprintf("p%d", i)}, on); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("pause %v: %+v %v", on, rc, err)
		}
	}
	se.e.ForgetForTest(se.run.ID)
	next := continueRun(t, se.e, se.run.ID)
	defer stopRun(t, se.e, next.ID)
	waitOutcome(t, se.e, next.ID, ledger.OutcomeSucceeded)
	if rows := selections(t, se.ld, se.run.ID, next.ID); len(rows) != 1 {
		t.Fatalf("records after None of these: %+v", rows)
	}
	if st := stateOf(t, se.e, next.ID); st.State != CompareNone || st.Decision != row.ID() {
		t.Fatalf("the continued comparison: %+v", st)
	}
}

func TestCheckAgainSupersedesTheSelection(t *testing.T) {
	se := launchSelect(t, 2)
	defer stopRun(t, se.e, se.run.ID)
	se.pass(t, se.run.ID, "approach-a", 1)
	failed := se.fail(t, se.run.ID, "approach-b")
	first := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	held, release := holdRechecks(t, se.e)
	rc, err := recheck(se.e, se.run.ID, "k1", "approach-b", failed.Attempt.N, failed.Attempt.Result)
	if err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("check again: %+v %v", rc, err)
	}
	rec := decisionWhen(t, se.ld, first.ID(), "superseded", inState(ledger.DecisionSuperseded))
	if !strings.Contains(rec.Why, "approach-b runs again") {
		t.Fatalf("the words: %q", rec.Why)
	}
	<-held
	release()
	// The check fails again (the commit is the one refused): the lane ends,
	// and the next selection opens.
	next := selectionWhen(t, se.ld, "a second, open", func(row ledger.DecisionRow) bool { return row.N != first.N && row.State == ledger.DecisionOpen }, se.run.ID)
	selectAs(t, se.e, next.ID(), 0, next.Rev)
	decisionWhen(t, se.ld, next.ID(), "applied", inState(ledger.DecisionApplied))
	b := waitTask(t, se.e, se.run.ID, "approach-b", ledger.TaskFailed, 0)
	_, err = recheck(se.e, se.run.ID, "k2", "approach-b", b.Attempt.N, b.Attempt.Result)
	refusedUnselected(t, se, se.run.ID, "k2", "approach-b was not selected in "+next.ID(), err)
}

func TestAStopKeepsTheSelection(t *testing.T) {
	t.Run("answered on the stopped run", func(t *testing.T) {
		se := launchSelect(t, 3)
		se.pass(t, se.run.ID, "approach-a", 1)
		se.pass(t, se.run.ID, "approach-b", 1)
		se.cancel(t, se.run.ID, "approach-c", "c1")
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		stopRun(t, se.e, se.run.ID)
		rec := decisionWhen(t, se.ld, row.ID(), "open", inState(ledger.DecisionOpen))
		if rec.Request != row.Request || rec.Rev != row.Rev {
			t.Fatalf("the stop changed it: %+v", rec.DecisionRow)
		}
		// The operator's cancel is refused on the stopped run too, before
		// anything is written.
		if _, err := se.e.CancelDecision(t.Context(), row.ID(), "x1", row.Rev, false, "", "admin"); err == nil || !strings.Contains(err.Error(), selectNoCancel) {
			t.Fatalf("a cancel on the stopped run: %v", err)
		}
		if got := decisionWhen(t, se.ld, row.ID(), "open", inState(ledger.DecisionOpen)); len(got.History) != len(rec.History) {
			t.Fatalf("the cancel wrote history: %q", historyKinds(got))
		}
		selectAs(t, se.e, row.ID(), 0, row.Rev)
		decisionWhen(t, se.ld, row.ID(), "answered", inState(ledger.DecisionAnswered))
		if st := stateOf(t, se.e, se.run.ID); st.State != CompareAnswered {
			t.Fatalf("the comparison: %+v", st)
		}
		next := continueRun(t, se.e, se.run.ID)
		defer stopRun(t, se.e, next.ID)
		got := decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
		if app := applied(t, got); app.Run != next.ID || app.Selected == nil || app.Selected.Task != "approach-a" || got.CurrentRun != next.ID || kindCount(got, ledger.HistoryApplied) != 1 {
			t.Fatalf("the application: %+v in %s", app, got.CurrentRun)
		}
		if n := countKind(eventsOf(t, se.ld, next.ID), "compare.selected"); n != 1 {
			t.Fatalf("compare.selected %d times in the continuation", n)
		}
		_, err := se.e.Retry(t.Context(), next.ID, "admin", Command{ID: "r1"}, "approach-c")
		refusedUnselected(t, se, next.ID, "r1", "approach-c was not selected in "+row.ID(), err)
		if ts := taskOf(t, se.e, next.ID, "approach-c"); ts.State != ledger.TaskCancelled {
			t.Fatalf("approach-c in the continuation: %+v", ts)
		}
	})
	t.Run("a stop as it is applied", func(t *testing.T) {
		se := launchSelect(t, 2)
		se.pass(t, se.run.ID, "approach-a", 1)
		se.pass(t, se.run.ID, "approach-b", 1)
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		at, gate := make(chan struct{}), make(chan struct{})
		var once sync.Once
		se.e.beforeApply = func(int64) { once.Do(func() { close(at); <-gate }) }
		done := make(chan struct{})
		go func() {
			defer close(done)
			selectAs(t, se.e, row.ID(), 1, row.Rev)
		}()
		<-at
		stopped := make(chan error, 1)
		go func() { stopped <- se.e.Stop(context.Background(), se.run.ID) }()
		close(gate)
		<-done
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		// Applied once, by the run that held it, or left answered for the
		// continuation to apply: never twice, never lost.
		decisionWhen(t, se.ld, row.ID(), "answered or applied", func(rec ledger.DecisionRecord) bool {
			return rec.State == ledger.DecisionApplied || rec.State == ledger.DecisionAnswered
		})
		next := continueRun(t, se.e, se.run.ID)
		defer stopRun(t, se.e, next.ID)
		rec := decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
		if kindCount(rec, ledger.HistoryApplied) != 1 || countKind(eventsOf(t, se.ld, se.run.ID), "compare.selected")+countKind(eventsOf(t, se.ld, next.ID), "compare.selected") != 1 {
			t.Fatalf("applied %d times: %q", kindCount(rec, ledger.HistoryApplied), historyKinds(rec))
		}
	})
	t.Run("open over a stop and a continue", func(t *testing.T) {
		se := launchSelect(t, 2)
		se.pass(t, se.run.ID, "approach-a", 1)
		se.pass(t, se.run.ID, "approach-b", 1)
		row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		stopRun(t, se.e, se.run.ID)
		next := continueRun(t, se.e, se.run.ID)
		defer stopRun(t, se.e, next.ID)
		rec := decisionWhen(t, se.ld, row.ID(), "moved", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == next.ID })
		if rec.State != ledger.DecisionOpen || len(selections(t, se.ld, se.run.ID, next.ID)) != 1 {
			t.Fatalf("the moved selection: %+v", rec.DecisionRow)
		}
		if outcomeOf(t, se.e, next.ID) != "" {
			t.Fatal("the continuation settled with its selection open")
		}
	})
}

func TestARestartWhileSelecting(t *testing.T) {
	se := launchSelect(t, 3)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	se.cancel(t, se.run.ID, "approach-c", "c1")
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	e2, _, ld2 := restart(t, se.e, se.ld, se.ldir, se.run.ID)
	e2.compareOpen = true
	if st := stateOf(t, e2, se.run.ID); st.State != CompareSelecting {
		t.Fatalf("after the restart: %+v", st)
	}
	selectAs(t, e2, row.ID(), 1, row.Rev)
	decisionWhen(t, ld2, row.ID(), "answered", inState(ledger.DecisionAnswered))
	if st := stateOf(t, e2, se.run.ID); st.State != CompareAnswered {
		t.Fatalf("answered on the run not held: %+v", st)
	}
	next := continueRun(t, e2, se.run.ID)
	defer stopRun(t, e2, next.ID)
	rec := decisionWhen(t, ld2, row.ID(), "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.Run != next.ID || app.Selected == nil || app.Selected.Task != "approach-b" || app.Selected.Run != se.run.ID {
		t.Fatalf("the application: %+v", app)
	}
	if countKind(eventsOf(t, ld2, next.ID), "compare.selected") != 1 {
		t.Fatal("no compare.selected in the continuation")
	}
	_, err := e2.Retry(t.Context(), next.ID, "admin", Command{ID: "r1"}, "approach-c")
	if !errors.Is(err, ErrNotSelected) {
		t.Fatalf("a retry of a lane not selected: %v", err)
	}
}

func TestTheSelectionCrossesTheChain(t *testing.T) {
	if maxCompareChain > maxChain {
		t.Fatalf("a comparison's chain of %d runs passes the %d a continuation reads", maxCompareChain, maxChain)
	}
	se := launchSelect(t, 2)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.cancel(t, se.run.ID, "approach-b", "c1")
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	selectAs(t, se.e, row.ID(), 0, row.Rev)
	decisionWhen(t, se.ld, row.ID(), "applied", inState(ledger.DecisionApplied))
	waitOutcome(t, se.e, se.run.ID, ledger.OutcomeCancelled)
	se.e.ForgetForTest(se.run.ID)
	plain := continueRun(t, se.e, se.run.ID)
	_, err := se.e.Retry(t.Context(), plain.ID, "admin", Command{ID: "r1"}, "approach-b")
	refusedUnselected(t, se, plain.ID, "r1", "approach-b was not selected in "+row.ID(), err)
	waitOutcome(t, se.e, plain.ID, ledger.OutcomeCancelled)
	e2, _, ld2 := restart(t, se.e, se.ld, se.ldir, plain.ID)
	e2.compareOpen = true
	again := continueRun(t, e2, plain.ID)
	defer stopRun(t, e2, again.ID)
	if _, err := e2.Retry(t.Context(), again.ID, "admin", Command{ID: "r2"}, "approach-b"); !errors.Is(err, ErrNotSelected) {
		t.Fatalf("a retry in the restart's continuation: %v", err)
	}
	if sel, ok, err := e2.SelectedLane(t.Context(), se.run.ID); err != nil || !ok || sel.Task != "approach-a" {
		t.Fatalf("the launched run's chain: %+v %v %v", sel, ok, err)
	}
	if rows := selections(t, ld2, se.run.ID, plain.ID, again.ID); len(rows) != 1 {
		t.Fatalf("records along the chain: %+v", rows)
	}
}
