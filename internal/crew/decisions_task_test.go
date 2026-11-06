package crew

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Whose a decision is (#637, review round 3): every key an attempt's agent
// supplies (a request id, a decision's id) is judged against the
// attempt's own task where it is looked up, so another task's decision is
// answered as one that does not exist; and a withdrawal is taken only
// while the attempt runs, never once its run has stopped or is continued.

// refusedAs is err's decision refusal, its id replaced by "dr-?", or fails
// t.
func refusedAs(t *testing.T, err error, id string) DecisionError {
	t.Helper()
	var de *DecisionError
	if !errors.As(err, &de) {
		t.Fatalf("not a decision's refusal: %v", err)
	}
	return DecisionError{Code: de.Code, Words: strings.ReplaceAll(de.Words, id, "dr-?")}
}

// A request id is its asking task's: the docs task, worked by the member
// that worked build, asks under build's request id and opens a decision of
// its own, with the same body or another, and is answered for its own
// request after that; build's decision is read, withdrawn and superseded
// by docs exactly as an id no decision has is.
func TestARequestIDIsItsTasks(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	built := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	reportDone(e, b.Attempt.SessionID)
	waitGivenBack(t, e, run.ID, "build", 1, built.ID)
	d, _, _ := attemptOn(t, e, fl, run.ID, "docs")
	sid := d.Attempt.SessionID
	ctx := t.Context()

	for _, req := range []DecisionRequest{clarification("q1", "Which index?"), clarification("q1", "Which heading?")} {
		if got, sent, err := e.DecisionSent(ctx, sid, req); sent || err != nil {
			t.Fatalf("docs' %q under build's request id was sent before: %+v %v", req.Question, got, err)
		}
	}
	mine := ask(t, e, sid, clarification("q1", "Which index?"))
	if mine.ID == built.ID || mine.Asker == nil || mine.Asker.Task != "docs" {
		t.Fatalf("docs' request under build's request id: %+v (build's %s)", mine, built.ID)
	}
	if again, created, err := e.RequestDecision(ctx, sid, clarification("q1", "Which index?")); created || err != nil || again.ID != mine.ID {
		t.Fatalf("docs' request sent again: %+v %v %v", again, created, err)
	}
	if _, _, err := e.RequestDecision(ctx, sid, clarification("q1", "Which heading?")); refusedAs(t, err, mine.ID) != (DecisionError{Code: CodeDecisionConflict, Words: "request q1 was sent before with another body, as dr-?"}) {
		t.Fatalf("docs' request under its own id with another body: %v", err)
	}
	if got := activeHolds(t, ld, run.ID); strings.Join(got, ",") != "build "+built.ID+",docs "+mine.ID {
		t.Fatalf("holds: %v", got)
	}

	const none = "dr-999"
	for _, c := range []struct {
		what string
		do   func(id string) error
	}{
		{"read", func(id string) error { _, err := e.TaskDecision(ctx, sid, id); return err }},
		{"withdraw", func(id string) error { _, err := e.WithdrawDecision(ctx, sid, id); return err }},
		{"supersede", func(id string) error {
			req := clarification("q2", "Which index, then?")
			req.Supersedes = id
			_, _, err := e.RequestDecision(ctx, sid, req)
			return err
		}},
	} {
		theirs, unknown := refusedAs(t, c.do(built.ID), built.ID), refusedAs(t, c.do(none), none)
		if theirs != unknown {
			t.Fatalf("%s of build's decision: %+v; of none: %+v", c.what, theirs, unknown)
		}
	}
	if rec := decisionWhen(t, ld, built.ID, "read", func(ledger.DecisionRecord) bool { return true }); rec.State != ledger.DecisionOpen || rec.Rev != 1 {
		t.Fatalf("build's decision after docs' tries: %s rev %d %q", rec.State, rec.Rev, historyKinds(rec))
	}
	if got, err := e.TaskDecision(ctx, sid, mine.ID); err != nil || got.ID != mine.ID {
		t.Fatalf("docs reads its own: %+v %v", got, err)
	}
	stopRun(t, e, run.ID)
}

// A withdrawal is judged against the continuation's start, on each side
// of it: taken while the run runs, the decision is cancelled and the
// continuation neither moves it nor holds the task with it; once the run
// is stopped, before its continuation, after the continuation's mark and
// after it has read the decisions (held at each seam), it is refused
// not_running and changes nothing, and the continuation moves the
// decision open, with its hold.
func TestAWithdrawalIsJudgedAgainstTheContinuation(t *testing.T) {
	notRunning := func(t *testing.T, e *Engine, sid, id string) {
		t.Helper()
		_, err := e.WithdrawDecision(t.Context(), sid, id)
		if de := refusedAs(t, err, id); de.Code != CodeNotRunning || !strings.Contains(de.Words, "stop here") {
			t.Fatalf("a withdrawal once the run stopped: %v", err)
		}
	}
	t.Run("before", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
		b, _, _ := attemptOn(t, e, fl, run.ID, "build")
		d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
		if w, err := e.WithdrawDecision(t.Context(), b.Attempt.SessionID, d.ID); err != nil || w.State != ledger.DecisionCancelled {
			t.Fatalf("the withdrawal: %+v %v", w, err)
		}
		stopRun(t, e, run.ID)
		next := continueRun(t, e, run.ID)
		rec := decisionWhen(t, ld, d.ID, "cancelled", inState(ledger.DecisionCancelled))
		if rec.CurrentRun != run.ID || len(activeHolds(t, ld, next.ID)) != 0 || len(taskOf(t, e, next.ID, "build").Holds) != 0 {
			t.Fatalf("a withdrawn decision moved: run %s, holds %v %+v", rec.CurrentRun, activeHolds(t, ld, next.ID), taskOf(t, e, next.ID, "build").Holds)
		}
		stopRun(t, e, next.ID)
	})
	for _, seam := range []string{"stopped", "marked", "read"} {
		t.Run(seam, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, ld := decisionEngine(t)
			run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
			b, _, _ := attemptOn(t, e, fl, run.ID, "build")
			d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
			stopRun(t, e, run.ID)
			at, gate := make(chan struct{}), make(chan struct{})
			var once sync.Once
			hold := func(string) { once.Do(func() { close(at); <-gate }) }
			switch seam {
			case "marked":
				e.beforeContinue = hold
			case "read":
				e.afterAdd = hold
			}
			out := make(chan Run, 1)
			go func() {
				if seam == "stopped" {
					// Not begun until the withdrawal has been refused.
					<-gate
				}
				next, err := e.ContinueRun(context.Background(), run.ID, "")
				if err != nil {
					t.Error(err)
					close(out)
					return
				}
				out <- *next
			}()
			if seam != "stopped" {
				<-at
			}
			notRunning(t, e, b.Attempt.SessionID, d.ID)
			close(gate)
			next, ok := <-out
			if !ok {
				t.FailNow()
			}
			rec := decisionWhen(t, ld, d.ID, "moved", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == next.ID })
			if rec.State != ledger.DecisionOpen || rec.Rev != 1 || strings.Contains(strings.Join(historyKinds(rec), ","), ledger.HistoryCancelled) {
				t.Fatalf("the refused withdrawal changed %s: %s rev %d %q", d.ID, rec.State, rec.Rev, historyKinds(rec))
			}
			if got := activeHolds(t, ld, next.ID); len(got) != 1 || got[0] != "build "+d.ID {
				t.Fatalf("the continuation's holds: %v", got)
			}
			if hs := taskOf(t, e, next.ID, "build").Holds; len(hs) != 1 || hs[0].ID != d.ID {
				t.Fatalf("the continuation's task holds: %+v", hs)
			}
			stopRun(t, e, next.ID)
		})
	}
}
