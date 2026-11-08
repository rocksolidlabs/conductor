package crew

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Decisions across a restart and a continuation (docs/plans/decisions.md,
// Continuations and recovery; O6, I3, I6, I16): what a restart keeps
// (everything), what it costs (nothing), and what it never does twice.
// A restart is the server's end under the run: its engine lets the run go
// and its ledger closes; another opens the same ledger, recovers it, and
// continues the run.

// decisionEngineAt is decisionEngine over the ledger in ldir.
func decisionEngineAt(t *testing.T, ldir string) (*Engine, *fakeLauncher, *ledger.Ledger) {
	t.Helper()
	e, fl := readyEngine(t)
	ld, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ld.Close() })
	if ro, why := ld.ReadOnly(); ro {
		t.Fatalf("the ledger opened read-only: %s", why)
	}
	e.UseLedger(SQLLedger(ld))
	return e, fl, ld
}

// restart ends the server under run (its engine lets it go, its ledger
// closes) and starts another over the same ledger, recovered.
func restart(t *testing.T, e *Engine, ld *ledger.Ledger, ldir, runID string) (*Engine, *fakeLauncher, *ledger.Ledger) {
	t.Helper()
	e.ForgetForTest(runID)
	if err := ld.Close(); err != nil {
		t.Fatal(err)
	}
	e2, fl2, ld2 := decisionEngineAt(t, ldir)
	if _, err := ld2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e2, fl2, ld2
}

// continueRun continues the run and returns the continuation.
func continueRun(t *testing.T, e *Engine, runID string) Run {
	t.Helper()
	next, err := e.ContinueRun(t.Context(), runID, "")
	if err != nil {
		t.Fatal(err)
	}
	return *next
}

// An answer recorded while the run is interrupted (through the ledger, the
// workbench token's) is applied once the run is continued: the
// interrupted attempt given back, its task's next attempt started with the
// answer in its task file.
func TestAnAnswerRecordedWhileInterruptedIsAppliedOnContinue(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e2, fl2, ld2 := restart(t, e, ld, ldir, run.ID)
	answer(t, e2, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	decisionWhen(t, ld2, d.ID, "answered", inState(ledger.DecisionAnswered))
	next := continueRun(t, e2, run.ID)
	rec := decisionWhen(t, ld2, d.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowTaskFile || app.Run != next.ID || rec.CurrentRun != next.ID || kindCount(rec, ledger.HistoryApplied) != 1 {
		t.Fatalf("the application: %+v in %s", app, rec.CurrentRun)
	}
	b2 := attemptLine(t, e2, fl2, next.ID, "build", 2)
	if _, f := taskFileOf(t, b2); decisionIn(t, f, d.ID).Answer != "btree" {
		t.Fatalf("the task file: %+v", f.Decisions)
	}
	if x := extraOf(t, e2, next.ID, "build"); x != 1 {
		t.Fatalf("the interrupted attempt given back %d times", x)
	}
	stopRun(t, e2, next.ID)
}

// A response during a continuation is applied once, whichever commits
// first: one committed on the old run while the continuation is held
// between its read and its first transaction is moved as answered and
// applied; one held between its choice of run and its transaction while
// the continuation commits is answered again through the continuation.
func TestAResponseDuringAContinuationIsAppliedOnce(t *testing.T) {
	setup := func(t *testing.T) (*Engine, *fakeLauncher, *ledger.Ledger, Run, Decision) {
		dir, ldir := newRepo(t), t.TempDir()
		e, fl, ld := decisionEngineAt(t, ldir)
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
		b, _, _ := attemptOn(t, e, fl, run.ID, "build")
		d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
		e2, fl2, ld2 := restart(t, e, ld, ldir, run.ID)
		return e2, fl2, ld2, run, d
	}
	appliedOnce := func(t *testing.T, ld *ledger.Ledger, id, runID string) {
		t.Helper()
		rec := decisionWhen(t, ld, id, "applied", inState(ledger.DecisionApplied))
		if kindCount(rec, ledger.HistoryApplied) != 1 || len(rec.Responses) != 1 || rec.CurrentRun != runID {
			t.Fatalf("%s: %+v %q", id, rec.DecisionRow, historyKinds(rec))
		}
	}
	t.Run("the response first", func(t *testing.T) {
		e, fl, ld, run, d := setup(t)
		at, gate := make(chan struct{}), make(chan struct{})
		var once sync.Once
		e.afterAdd = func(string) { once.Do(func() { close(at); <-gate }) }
		out := make(chan Run, 1)
		go func() {
			next, err := e.ContinueRun(context.Background(), run.ID, "")
			if err != nil {
				t.Error(err)
				close(out)
				return
			}
			out <- *next
		}()
		<-at
		answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
		close(gate)
		next, ok := <-out
		if !ok {
			t.FailNow()
		}
		appliedOnce(t, ld, d.ID, next.ID)
		attemptLine(t, e, fl, next.ID, "build", 2)
		stopRun(t, e, next.ID)
	})
	t.Run("the continuation first", func(t *testing.T) {
		e, fl, ld, run, d := setup(t)
		at, gate := make(chan struct{}), make(chan struct{})
		var once sync.Once
		e.beforeRespond = func(int64) { once.Do(func() { close(at); <-gate }) }
		got := make(chan error, 1)
		go func() {
			_, err := e.RespondDecision(context.Background(), d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1, ByName: "Nate"}, "admin", workbench(e))
			got <- err
		}()
		<-at
		next := continueRun(t, e, run.ID)
		close(gate)
		if err := <-got; err != nil {
			t.Fatalf("the response held across the continuation: %v", err)
		}
		appliedOnce(t, ld, d.ID, next.ID)
		attemptLine(t, e, fl, next.ID, "build", 2)
		stopRun(t, e, next.ID)
	})
}

// An attempt the server's end cut while it waited is given back by the
// continuation, its refund recorded; the task stays held, no attempt
// reserved, until the answer.
func TestAnInterruptedWaitingAttemptIsGivenBack(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e2, fl2, ld2 := restart(t, e, ld, ldir, run.ID)
	next := continueRun(t, e2, run.ID)
	rec := decisionWhen(t, ld2, d.ID, "refunded", hasKind(ledger.HistoryRefunded))
	if rec.CurrentRun != next.ID || extraOf(t, e2, next.ID, "build") != 1 {
		t.Fatalf("%s in %s, extra %d", d.ID, rec.CurrentRun, extraOf(t, e2, next.ID, "build"))
	}
	e2.pumpLocked(runOf(t, e2, next.ID))
	if ts := taskOf(t, e2, next.ID, "build"); ts.Attempts != 1 || ts.Reason != "waits for decision "+d.ID {
		t.Fatalf("build while %s is open: %+v", d.ID, ts)
	}
	answer(t, e2, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	attemptLine(t, e2, fl2, next.ID, "build", 2)
	stopRun(t, e2, next.ID)
}

// A gate and an agent's decision held across two continuations: after
// each, one active hold per task and decision, under the newest run; the
// gate applied and the agent's decision cancelled by the operator, every
// hold row of the three runs is released, and the ancestors' recovered
// records report no task held.
func TestHoldsFollowTheDecisionAcrossContinuations(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000e1", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	want := []string{"build " + d.ID, "deploy " + g.ID}
	runs := []string{run.ID}
	for range 2 {
		stopped := runs[len(runs)-1]
		stopRun(t, e, stopped)
		// A stop gives the run no outcome and ends no decision: both stay
		// open, held, for the continuation to take (the compare-approaches
		// plan rests on it).
		if got, _ := e.Get(stopped); got.Outcome != "" {
			t.Fatalf("the stop gave %s the outcome %q", stopped, got.Outcome)
		}
		for _, id := range []string{d.ID, g.ID} {
			if rec := decisionWhen(t, ld, id, "open", inState(ledger.DecisionOpen)); rec.CurrentRun != stopped {
				t.Fatalf("%s after the stop: %+v", id, rec.DecisionRow)
			}
		}
		next := continueRun(t, e, stopped)
		runs = append(runs, next.ID)
		for i, id := range runs {
			got := activeHolds(t, ld, id)
			if i == len(runs)-1 && !equalStrings(got, want) || i < len(runs)-1 && len(got) != 0 {
				t.Fatalf("holds under %s after %d continuations: %q", id, len(runs)-1, got)
			}
			// The engine's view of each run continued shows no hold either:
			// its tasks' holds went with its decisions (#636, review round
			// 3).
			if i < len(runs)-1 {
				rec, _ := e.Get(id)
				for _, ts := range rec.Tasks {
					if len(ts.Holds) != 0 {
						t.Fatalf("the engine shows %s's %s held after %d continuations: %+v", id, ts.ID, len(runs)-1, ts.Holds)
					}
				}
			}
		}
	}
	last := runs[len(runs)-1]
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	if _, err := e.CancelDecision(t.Context(), d.ID, "c1", 0, false, "", "admin"); err != nil {
		t.Fatal(err)
	}
	decisionWhen(t, ld, d.ID, "cancelled", inState(ledger.DecisionCancelled))
	for _, id := range runs {
		rows, err := ld.Holds(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range rows {
			if h.ReleasedAt.IsZero() {
				t.Fatalf("a hold of %s left active: %+v", id, h)
			}
		}
	}
	for _, id := range runs[:len(runs)-1] {
		from, ok, err := SQLLedger(ld).Load(t.Context(), id)
		if err != nil || !ok {
			t.Fatalf("%s: %v", id, err)
		}
		for _, ts := range Recovered(Run{}, from).Tasks {
			if len(ts.Holds) != 0 {
				t.Fatalf("%s's record holds %s: %+v", id, ts.ID, ts.Holds)
			}
		}
	}
	stopRun(t, e, last)
}

// equalStrings says two lists hold the same strings in order.
func equalStrings(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00") && len(a) == len(b)
}

// A gate's rejection keeps its words in a continuation (#641, review round
// 1): the task it cancelled is imported cancelled with "rejected in dr-N by
// NAME: why", the reason the ledger holds, not the bare word; a task
// cancelled with no reason of its own reads "cancelled" as before.
func TestARejectionKeepsItsWordsInAContinuation(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	run := launchGated(t, e, dir, "api-sweep-000000e3", repoCrew(dir, work("build", "lead"), work("deploy", "ops"), work("docs", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(1), Rationale: "not this week", ExpectedRev: 1})
	decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	want := "rejected in " + g.ID + " by Nate: not this week"
	waitTrue(t, "deploy's row cancelled with its words", func() bool {
		row := ledgerTask(t, ld, run.ID, "deploy")
		return row.State == ledger.TaskCancelled && row.Reason == want
	})
	if err := e.CancelTask(t.Context(), run.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "docs's row cancelled", func() bool { return ledgerTask(t, ld, run.ID, "docs").State == ledger.TaskCancelled })
	e2, _, _ := restart(t, e, ld, ldir, run.ID)
	next := continueRun(t, e2, run.ID)
	if ts := taskOf(t, e2, next.ID, "deploy"); ts.State != ledger.TaskCancelled || ts.Reason != want {
		t.Fatalf("deploy in the continuation: %+v, want cancelled with %q", ts, want)
	}
	if ts := taskOf(t, e2, next.ID, "docs"); ts.State != ledger.TaskCancelled || ts.Reason != "cancelled" {
		t.Fatalf("docs in the continuation: %+v, want cancelled with the bare word", ts)
	}
	stopRun(t, e2, next.ID)
}

// An interrupted waiting attempt is given back once: continued, stopped
// before any dispatch (its task held), continued again, the task has one
// attempt more, not two.
func TestAnInterruptedAttemptIsGivenBackOnce(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	next := continueRun(t, e2, run.ID)
	stopRun(t, e2, next.ID)
	again := continueRun(t, e2, next.ID)
	if x := extraOf(t, e2, again.ID, "build"); x != 1 {
		t.Fatalf("given back %d times", x)
	}
	if rec := decisionWhen(t, ld2, d.ID, "moved twice", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == again.ID }); kindCount(rec, ledger.HistoryRefunded) != 1 {
		t.Fatalf("history %q", historyKinds(rec))
	}
	stopRun(t, e2, again.ID)
}

// The refund is the attempt's, not the decision's: an identity's response
// cut by the server's end, the continuation gives the attempt back and
// supersedes the decision by a fresh one (its authority cannot be
// rechecked); stopped and continued again, the attempt is not given back
// again, and the fresh decision's history holds no refund.
func TestAReplacedDecisionKeepsTheRefund(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1}, "user:nate", identity(e, "nate")); err != nil {
		t.Fatal(err)
	}
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	next := continueRun(t, e2, run.ID)
	old := decisionWhen(t, ld2, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	if old.Applied != "" || old.SupersededBy == "" || kindCount(old, ledger.HistoryRefunded) != 1 {
		t.Fatalf("%s: %+v %q", d.ID, old.DecisionRow, historyKinds(old))
	}
	stopRun(t, e2, next.ID)
	again := continueRun(t, e2, next.ID)
	if x := extraOf(t, e2, again.ID, "build"); x != 1 {
		t.Fatalf("given back %d times", x)
	}
	fresh := decisionWhen(t, ld2, old.SupersededBy, "moved", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == again.ID })
	if kindCount(fresh, ledger.HistoryRefunded) != 0 || fresh.State != ledger.DecisionOpen {
		t.Fatalf("%s: %+v %q", fresh.ID(), fresh.DecisionRow, historyKinds(fresh))
	}
	stopRun(t, e2, again.ID)
}

// A typing the server's end cut (delivery.typing without its end) is
// never typed again: the continuation writes delivery.uncertain, and the
// task's next attempt reads the answer in its task file.
func TestATypingCutByARestartIsNeverTypedAgain(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.beforeTyping = func(int64) { once.Do(func() { close(held); <-release }) }
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	<-held
	old := runOf(t, e, run.ID)
	e2, fl2, ld2 := restart(t, e, ld, ldir, run.ID)
	close(release)
	old.deliveries.Wait()
	next := continueRun(t, e2, run.ID)
	decisionWhen(t, ld2, d.ID, "uncertain", hasKind(ledger.HistoryDeliveryUncertain))
	b2 := attemptLine(t, e2, fl2, next.ID, "build", 2)
	if _, f := taskFileOf(t, b2); decisionIn(t, f, d.ID).Answer != "btree" {
		t.Fatalf("the task file: %+v", f.Decisions)
	}
	rec := decisionWhen(t, ld2, d.ID, "carried", hasKind(ledger.HistoryDeliveryTaskFile))
	if kindCount(rec, ledger.HistoryDeliveryEntered) != 0 || kindCount(rec, ledger.HistoryDeliveryUntyped) != 0 {
		t.Fatalf("history %q", historyKinds(rec))
	}
	for _, line := range typed(p) {
		if strings.Contains(line, "Decision "+d.ID) {
			t.Fatalf("the cut line was typed: %q", line)
		}
	}
	stopRun(t, e2, next.ID)
}

// An identity's response the server's end left unapplied is never applied
// after the restart (its token cannot be rechecked): the continuation
// supersedes it by a fresh gate with its holds.
func TestAnIdentityAnswerIsNotAppliedAfterARestart(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	run := launchGated(t, e, dir, "api-sweep-000000e2", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	if _, err := e.RespondDecision(t.Context(), g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", identity(e, "nate")); err != nil {
		t.Fatal(err)
	}
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	next := continueRun(t, e2, run.ID)
	old := decisionWhen(t, ld2, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	if old.Applied != "" || old.SupersededBy == "" {
		t.Fatalf("%s: %+v", g.ID, old.DecisionRow)
	}
	if got := activeHolds(t, ld2, next.ID); !equalStrings(got, []string{"deploy " + old.SupersededBy}) {
		t.Fatalf("holds %q", got)
	}
	if ts := taskOf(t, e2, next.ID, "deploy"); ts.Attempts != 0 {
		t.Fatalf("deploy released: %+v", ts)
	}
	stopRun(t, e2, next.ID)
}

// toldLog keeps what SQLLedgerTold's told was handed, in its order.
type toldLog struct {
	mu  sync.Mutex
	got []DecisionChange
}

func (l *toldLog) told(cs []DecisionChange) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, cs...)
}

// of is what was told of decision id, in order.
func (l *toldLog) of(id string) []ledger.DecisionRow {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ledger.DecisionRow
	for _, c := range l.got {
		if c.Row.ID() == id {
			out = append(out, c.Row)
		}
	}
	return out
}

// Every committed change of a decision is told, as its transaction left
// it, in the order of the commits, by the writers themselves: the opening,
// an answer recorded while the run is interrupted, a refused response
// (its receipt and history, no event), the continuation's move (the run
// that holds it) and its application, the last told being the record as it
// ends (#637, review round 1).
func TestEveryChangeOfADecisionIsTold(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e.ForgetForTest(run.ID)
	if err := ld.Close(); err != nil {
		t.Fatal(err)
	}
	e2, fl2 := readyEngine(t)
	ld2, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ld2.Close() })
	if _, err := ld2.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	log := &toldLog{}
	e2.UseLedger(SQLLedgerTold(ld2, log.told))
	answer(t, e2, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	if got := log.of(d.ID); len(got) != 1 || got[0].State != ledger.DecisionAnswered || got[0].Rev != 2 || got[0].CurrentRun != run.ID {
		t.Fatalf("told of the answer: %+v", got)
	}
	// A refused response writes its receipt and history, and no event: told.
	if rc, err := e2.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r2", Text: "hash", ExpectedRev: 2}, "admin", workbench(e2)); err == nil || rc.Status != "refused" {
		t.Fatalf("the second response: %+v %v", rc, err)
	}
	if got := log.of(d.ID); len(got) != 2 || got[1].State != ledger.DecisionAnswered || got[1].Rev != 2 {
		t.Fatalf("told of the refusal: %+v", got)
	}
	next := continueRun(t, e2, run.ID)
	rec := decisionWhen(t, ld2, d.ID, "applied", inState(ledger.DecisionApplied))
	attemptLine(t, e2, fl2, next.ID, "build", 2)
	got := log.of(d.ID)
	moved := false
	for i, row := range got {
		if i > 0 && row.Rev < got[i-1].Rev {
			t.Fatalf("told out of the commits' order: %+v", got)
		}
		moved = moved || row.CurrentRun == next.ID
	}
	last := got[len(got)-1]
	if !moved || last.State != rec.State || last.Rev != rec.Rev || last.CurrentRun != rec.CurrentRun {
		t.Fatalf("told %+v; the record ends %s rev %d in %s", got, rec.State, rec.Rev, rec.CurrentRun)
	}
	stopRun(t, e2, next.ID)
}

// answeredReadsFail is the real ledger whose read of a decision fails
// while the decision is answered: a read made after a response commits
// and before its application.
type answeredReadsFail struct {
	sqlLedger
}

func (l *answeredReadsFail) Decision(ctx context.Context, n int64) (ledger.DecisionRecord, bool, error) {
	rec, ok, err := l.sqlLedger.Decision(ctx, n)
	if err == nil && ok && rec.State == ledger.DecisionAnswered {
		return ledger.DecisionRecord{}, false, errors.New("the ledger could not read the decision (test)")
	}
	return rec, ok, err
}

// A response committed on the old run while the continuation is held
// between its read and its first transaction is moved as answered with
// its answer, read in the move's transaction: no read after the move,
// which here fails, stands between it and its application (#636, review
// round 2).
func TestAMovedAnswerIsReadWithItsMove(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e2, fl2, ld2 := restart(t, e, ld, ldir, run.ID)
	e2.UseLedger(&answeredReadsFail{sqlLedger{l: ld2}})
	at, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e2.afterAdd = func(string) { once.Do(func() { close(at); <-gate }) }
	out := make(chan Run, 1)
	go func() {
		next, err := e2.ContinueRun(context.Background(), run.ID, "")
		if err != nil {
			t.Error(err)
			close(out)
			return
		}
		out <- *next
	}()
	<-at
	answer(t, e2, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	decisionWhen(t, ld2, d.ID, "answered", inState(ledger.DecisionAnswered))
	close(gate)
	next, ok := <-out
	if !ok {
		t.FailNow()
	}
	rec := decisionWhen(t, ld2, d.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.Run != next.ID || app.How != HowTaskFile || kindCount(rec, ledger.HistoryApplied) != 1 {
		t.Fatalf("the application: %+v", app)
	}
	if _, f := taskFileOf(t, attemptLine(t, e2, fl2, next.ID, "build", 2)); decisionIn(t, f, d.ID).Answer != "btree" {
		t.Fatalf("the task file: %+v", f.Decisions)
	}
	stopRun(t, e2, next.ID)
}

// An identity's answer whose recheck answers while its run is stopped and
// continued in the same process is applied by the continuation, which
// holds the decision: the recheck answered after the move, and the
// recheck answered while the continuation is held between its read and
// its first transaction, when only the old run has a copy (#636, review
// round 2).
func TestAConfirmationFollowsItsDecisionIntoTheContinuation(t *testing.T) {
	for i, when := range []string{"after the move", "during the move"} {
		t.Run(when, func(t *testing.T) {
			dir := newRepo(t)
			e, _, ld := decisionEngine(t)
			run := launchGated(t, e, dir, fmt.Sprintf("api-sweep-000000f%d", i), repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
			g := gateOf(t, e, ld, run.ID)
			rc, err := e.RespondDecision(t.Context(), g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"}, "user:nate", identity(e, "nate"))
			if err != nil || rc.Status != "accepted" {
				t.Fatalf("the identity's response: %+v %v", rc, err)
			}
			stopRun(t, e, run.ID)
			var next Run
			if when == "after the move" {
				next = continueRun(t, e, run.ID)
				e.ConfirmAuthority(g.ID, rc.K, true)
			} else {
				at, gate := make(chan struct{}), make(chan struct{})
				var once sync.Once
				e.afterAdd = func(string) { once.Do(func() { close(at); <-gate }) }
				out := make(chan Run, 1)
				go func() {
					next, err := e.ContinueRun(context.Background(), run.ID, "")
					if err != nil {
						t.Error(err)
						close(out)
						return
					}
					out <- *next
				}()
				<-at
				e.ConfirmAuthority(g.ID, rc.K, true)
				close(gate)
				var ok bool
				if next, ok = <-out; !ok {
					t.FailNow()
				}
			}
			rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
			if app := applied(t, rec); app.Run != next.ID || app.How != HowReleased || rec.CurrentRun != next.ID {
				t.Fatalf("the application: %+v in %s", app, rec.CurrentRun)
			}
			waitAttempt(t, e, next.ID, "deploy", ledger.AttemptRunning)
			stopRun(t, e, next.ID)
		})
	}
}

// continueHeld starts the run's continuation and holds it once it has read
// the run's history (afterAdd): resume lets it go on, and done answers it.
func continueHeld(t *testing.T, e *Engine, runID string) (resume func(), done func() Run) {
	t.Helper()
	at, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.afterAdd = func(string) { once.Do(func() { close(at); <-gate }) }
	out := make(chan Run, 1)
	go func() {
		next, err := e.ContinueRun(context.Background(), runID, "")
		if err != nil {
			t.Error(err)
			close(out)
			return
		}
		out <- *next
	}()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	select {
	case <-at:
	case <-time.After(60 * time.Second):
		t.Fatal("the continuation did not read the run")
	}
	return func() { close(gate) }, func() Run {
		next, ok := <-out
		if !ok {
			t.FailNow()
		}
		return next
	}
}

// An agent's withdrawal that reaches the run after its continuation read
// it is refused: the continuation, held once it read the decision open,
// moves it, and the run continued writes nothing more; the continuation
// holds the decision, open, in the ledger and in the engine, and its answer
// is applied there (#636, review round 3).
func TestAWithdrawalDuringAContinuationIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	stopRun(t, e, run.ID)
	resume, done := continueHeld(t, e, run.ID)
	if _, err := e.WithdrawDecision(t.Context(), b.Attempt.SessionID, d.ID); err == nil {
		t.Fatal("a withdrawal after the continuation's read was taken")
	}
	resume()
	next := done()
	rec := decisionWhen(t, ld, d.ID, "open in the continuation", func(rec ledger.DecisionRecord) bool {
		return rec.State == ledger.DecisionOpen && rec.CurrentRun == next.ID
	})
	if kindCount(rec, ledger.HistoryCancelled) != 0 {
		t.Fatalf("%s: %q", d.ID, historyKinds(rec))
	}
	if got := activeHolds(t, ld, next.ID); !equalStrings(got, []string{"build " + d.ID}) {
		t.Fatalf("the continuation's holds: %q", got)
	}
	if _, ok := engineHolds(t, e, next.ID, d.ID); !ok {
		t.Fatalf("the continuation's engine does not hold %s", d.ID)
	}
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, next.ID)
}

// A line whose end comes after its run's continuation read it typing
// writes nothing: the line's program took it slowly (ErrSubmitPending),
// the run is stopped and continued, and its end arrives once the
// continuation, held there, has read the typing. The continuation marks it
// uncertain, the decision's one end, so its task file's mark, which the
// continuation's attempt writes, has its room, and neither run is paused
// (#636, review round 3).
func TestALateLineEndDuringAContinuationWritesNothing(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	var mu sync.Mutex
	var finished func(session.SubmitResult, error)
	typing := make(chan struct{})
	e.submitAnswer = func(ctx context.Context, l *session.Local, sub session.Submission) (session.SubmitResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if finished != nil || sub.Finished == nil {
			return l.Submit(ctx, sub)
		}
		finished = sub.Finished
		close(typing)
		return session.SubmitResult{Typed: true}, session.ErrSubmitPending
	}
	ended := make(chan int64, 4)
	e.afterLineEnded = func(n int64) { ended <- n }
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	select {
	case <-typing:
	case <-time.After(60 * time.Second):
		t.Fatal("the answer's line was not typed")
	}
	decisionWhen(t, ld, d.ID, "typing", hasKind(ledger.HistoryDeliveryTyping))
	stopRun(t, e, run.ID)
	resume, done := continueHeld(t, e, run.ID)
	mu.Lock()
	end := finished
	mu.Unlock()
	end(session.SubmitResult{}, errors.New("the session ended"))
	select {
	case <-ended:
	case <-time.After(60 * time.Second):
		t.Fatal("the line's end was not decided")
	}
	resume()
	next := done()
	waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
	rec := decisionWhen(t, ld, d.ID, "carried by the continuation's task file", hasKind(ledger.HistoryDeliveryTaskFile))
	if kinds := historyKinds(rec); kindCount(rec, ledger.HistoryDeliveryUntyped) != 0 || kindCount(rec, ledger.HistoryDeliveryUncertain) != 1 {
		t.Fatalf("%s's deliveries: %q", d.ID, kinds)
	}
	if paused(t, e, next.ID) || paused(t, e, run.ID) {
		t.Fatalf("paused: the continuation %v, the run continued %v", paused(t, e, next.ID), paused(t, e, run.ID))
	}
	stopRun(t, e, next.ID)
}

// ghostChain is the real ledger, with run target continuing a chain of
// runs only it has: ghost-1 continues ghost-2, and so on to ghost-<ghosts>,
// which continues none. Their rows hold no decisions.
type ghostChain struct {
	sqlLedger
	target string
	ghosts atomic.Int64
}

func (l *ghostChain) Load(ctx context.Context, runID string) (Loaded, bool, error) {
	from, ok, err := l.sqlLedger.Load(ctx, runID)
	if ok && runID == l.target {
		from.Run.ContinuedFrom = "ghost-1"
	}
	return from, ok, err
}

func (l *ghostChain) RunRow(ctx context.Context, id string) (ledger.RunRow, bool, error) {
	if n, ok := strings.CutPrefix(id, "ghost-"); ok {
		i, err := strconv.Atoi(n)
		if err != nil || i < 1 || int64(i) > l.ghosts.Load() {
			return ledger.RunRow{}, false, nil
		}
		row := ledger.RunRow{ID: id, State: ledger.RunInterrupted}
		if int64(i) < l.ghosts.Load() {
			row.ContinuedFrom = fmt.Sprintf("ghost-%d", i+1)
		}
		return row, true, nil
	}
	return l.sqlLedger.RunRow(ctx, id)
}

func (l *ghostChain) Decisions(ctx context.Context, f ledger.DecisionFilter) ([]ledger.DecisionRow, error) {
	if strings.HasPrefix(f.CurrentRun, "ghost-") {
		return nil, nil
	}
	return l.sqlLedger.Decisions(ctx, f)
}

// A chain is read whole or the continuation is refused (#636, review round
// 4): the run and maxChain-1 runs before it are read to the chain's first;
// one run more is refused chain_too_long, with nothing made or moved: the
// decision stays open under the run, its hold active, the run not marked
// continued, and a continuation once the chain reads whole moves it.
func TestAChainLongerThanTheServerReadsIsNotContinued(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	gc := &ghostChain{sqlLedger: sqlLedger{l: ld2}, target: run.ID}
	e2.UseLedger(gc)

	gc.ghosts.Store(maxChain - 1)
	from, ok, err := gc.Load(t.Context(), run.ID)
	if err != nil || !ok {
		t.Fatalf("the run: %v %v", ok, err)
	}
	dh, err := e2.readDecisionHistory(t.Context(), from)
	if err != nil || len(dh.chain) != maxChain || dh.chain[maxChain-1] != fmt.Sprintf("ghost-%d", maxChain-1) || len(dh.moving) != 1 {
		t.Fatalf("the chain at the limit: %d runs, %d moving, %v", len(dh.chain), len(dh.moving), err)
	}

	gc.ghosts.Store(maxChain)
	if _, err := e2.ContinueRun(t.Context(), run.ID, ""); !errors.Is(err, ErrChainTooLong) || !strings.Contains(err.Error(), "10000 runs") {
		t.Fatalf("the continuation of a chain one longer: %v", err)
	}
	rec := decisionWhen(t, ld2, d.ID, "open", inState(ledger.DecisionOpen))
	if rec.CurrentRun != run.ID || len(historyKinds(rec)) != 2 {
		t.Fatalf("%s after the refusal: in %s, %q", d.ID, rec.CurrentRun, historyKinds(rec))
	}
	if got := activeHolds(t, ld2, run.ID); !slices.Equal(got, []string{"build " + d.ID}) {
		t.Fatalf("holds %q", got)
	}
	if row, ok, err := ld2.Run(t.Context(), run.ID); err != nil || !ok || row.ContinuedBy != "" {
		t.Fatalf("the run after the refusal: %+v %v", row, err)
	}

	e2.UseLedger(SQLLedger(ld2))
	next := continueRun(t, e2, run.ID)
	decisionWhen(t, ld2, d.ID, "moved", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == next.ID })
	stopRun(t, e2, next.ID)
}
