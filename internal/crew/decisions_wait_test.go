package crew

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The rest of Yardmaster's wait (docs/plans/decisions.md, DR1 PR-3): an
// attempt that ends while it waits is given back, a waiting attempt whose
// agent ended its turn gives up its member and its slot, the work ending
// under a decision ends it, and a gate's requirement outlives its decision.
// Every test waits on the ledger's record or the engine's state, never on
// a clock.

// clarification is a clarification request of the attempt's HEAD.
func clarification(requestID, question string) DecisionRequest {
	return DecisionRequest{RequestID: requestID, Type: ledger.DecisionClarification, Question: question}
}

// reportDone and reportWorking are the attempt's agent's own reports, as
// its hooks hand them to the engine.
func reportDone(e *Engine, sessionID string) {
	e.OnActivity(sessionID, session.ActivityEntry{Type: session.ActivityAttention, Message: "done", At: time.Now().UTC()}, session.AttentionDone)
}

func reportWorking(e *Engine, sessionID string) {
	e.OnActivity(sessionID, session.ActivityEntry{Type: session.ActivityAttention, Message: "working", At: time.Now().UTC()}, session.AttentionWorking)
}

// extraOf is the attempts a task was given past its bound.
func extraOf(t *testing.T, e *Engine, runID, taskID string) int {
	t.Helper()
	r := runOf(t, e, runID)
	e.mu.Lock()
	defer e.mu.Unlock()
	return r.task(taskID).extra
}

// attemptLine waits for the task's attempt n to be running and returns the
// task, its session and process, the task line read.
func attemptLine(t *testing.T, e *Engine, fl *fakeLauncher, runID, taskID string, n int) TaskState {
	t.Helper()
	ts := waitTaskWhere(t, e, runID, taskID, fmt.Sprintf("attempt %d running", n), func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
	})
	_, p := fl.attemptSession(t, ts)
	lineWith(t, p, taskID)
	return ts
}

// waitGivenBack waits for the task's attempt n to have closed while it
// waited on decision id: given back, the task ready and held.
func waitGivenBack(t *testing.T, e *Engine, runID, taskID string, n int, id string) TaskState {
	t.Helper()
	return waitTaskWhere(t, e, runID, taskID, fmt.Sprintf("attempt %d given back for %s", n, id), func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.Stage == stageDecision && ts.State == ledger.TaskReady && ts.Reason == "waits for decision "+id
	})
}

// hasKind says the record's history has a row of kind.
func hasKind(kind string) func(ledger.DecisionRecord) bool {
	return func(rec ledger.DecisionRecord) bool { return slices.Contains(historyKinds(rec), kind) }
}

// kindCount counts the record's history rows of kind.
func kindCount(rec ledger.DecisionRecord, kind string) int {
	n := 0
	for _, k := range historyKinds(rec) {
		if k == kind {
			n++
		}
	}
	return n
}

// decisionIn is the task file's decision id, failing without one.
func decisionIn(t *testing.T, f TaskFile, id string) TaskDecision {
	t.Helper()
	for _, d := range f.Decisions {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("the task file has no %s: %+v", id, f.Decisions)
	return TaskDecision{}
}

// unparked fails unless the task's attempt still runs with no park of it
// on its way: what a pump decides as it runs, read under e.mu once the
// pump returned. A park is a goroutine whose close commits later, so the
// attempt's state alone, read a moment after, says nothing of it.
func unparked(t *testing.T, e *Engine, runID, taskID string) {
	t.Helper()
	r := runOf(t, e, runID)
	e.mu.Lock()
	defer e.mu.Unlock()
	a := r.task(taskID).active()
	if a == nil || !a.closedAt.IsZero() || a.state != ledger.AttemptRunning || a.parking {
		t.Fatalf("%s's attempt was given up: %+v", taskID, a)
	}
}

// An agent's decision holds only its own task: the waiting attempt, its
// turn ended, gives its member up to a sibling task, which dispatches and
// finishes meanwhile; the attempt is given back, and the task's next
// attempt starts only after the answer, with it in its task file.
func TestAnAgentsDecisionHoldsOnlyItsTask(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	if ts := taskOf(t, e, run.ID, "docs"); ts.Attempts != 0 {
		t.Fatalf("docs dispatched while its member works: %+v", ts)
	}
	reportDone(e, b.Attempt.SessionID)
	waitGivenBack(t, e, run.ID, "build", 1, d.ID)
	w, _, _ := attemptOn(t, e, fl, run.ID, "docs")
	commitResult(t, e, w, "docs.txt", "d\n", "the docs")
	waitTask(t, e, run.ID, "docs", ledger.TaskSucceeded, 1)
	e.pumpLocked(runOf(t, e, run.ID))
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != 1 || len(ts.Holds) != 1 || ts.Holds[0].ID != d.ID {
		t.Fatalf("build while %s is open: %+v", d.ID, ts)
	}
	decisionWhen(t, ld, d.ID, "refunded", hasKind(ledger.HistoryRefunded))
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowTaskFile || app.Task != "build" {
		t.Fatalf("the application: %+v", app)
	}
	b2 := attemptLine(t, e, fl, run.ID, "build", 2)
	_, f := taskFileOf(t, b2)
	if td := decisionIn(t, f, d.ID); td.State != ledger.DecisionApplied || td.Answer != "btree" || td.By != "Nate" {
		t.Fatalf("the task file's decision: %+v", td)
	}
	if f.MaxAttempts != maxAttemptsPerTask+1 {
		t.Fatalf("the attempt given back still counts: max %d", f.MaxAttempts)
	}
	decisionWhen(t, ld, d.ID, "carried by a task file", hasKind(ledger.HistoryDeliveryTaskFile))
	stopRun(t, e, run.ID)
}

// An attempt whose agent still works is never parked, whether or not its
// question shows: a pump finds nothing to give up; the agent's done parks
// it, and the sibling dispatches then, woken by the done.
func TestAWorkingAttemptIsNeverParked(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	reportWorking(e, b.Attempt.SessionID)
	e.pumpLocked(runOf(t, e, run.ID))
	unparked(t, e, run.ID, "build")
	if ts := taskOf(t, e, run.ID, "docs"); ts.Attempts != 0 {
		t.Fatalf("docs dispatched while its member works: %+v", ts)
	}
	reportDone(e, b.Attempt.SessionID)
	waitGivenBack(t, e, run.ID, "build", 1, d.ID)
	attemptOn(t, e, fl, run.ID, "docs")
	stopRun(t, e, run.ID)
}

// A working report after a done clears the turn's end (I12): a sibling
// that becomes ready for the member then does not park the attempt; the
// agent's next done does, and the sibling starts.
func TestAWorkingReportClearsTheTurnsEnd(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("prep", "ops"), work("build", "lead"), work("docs", "lead", "prep")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	pr, _, _ := attemptOn(t, e, fl, run.ID, "prep")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	reportDone(e, b.Attempt.SessionID)
	reportWorking(e, b.Attempt.SessionID)
	commitResult(t, e, pr, "prep.txt", "p\n", "prepared")
	waitTask(t, e, run.ID, "docs", ledger.TaskReady, 0)
	e.pumpLocked(runOf(t, e, run.ID))
	unparked(t, e, run.ID, "build")
	if ts := taskOf(t, e, run.ID, "docs"); ts.Attempts != 0 {
		t.Fatalf("docs took the member of a working attempt: %+v", ts)
	}
	reportDone(e, b.Attempt.SessionID)
	waitGivenBack(t, e, run.ID, "build", 1, d.ID)
	attemptOn(t, e, fl, run.ID, "docs")
	stopRun(t, e, run.ID)
}

// A question the session's rate limit refused never shows; the agent's
// done still parks its attempt, and the sibling dispatches.
func TestARefusedMirrorStillParks(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := decisionEngine(t)
	asked := make(chan struct{}, 1)
	e.tryAsk = func(*session.Local, string, string) (string, error) {
		asked <- struct{}{}
		return "", session.ErrRateLimited
	}
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	<-asked
	if a := l.Info().Attention; a.Decision != "" {
		t.Fatalf("a refused question shows: %+v", a)
	}
	reportDone(e, b.Attempt.SessionID)
	waitGivenBack(t, e, run.ID, "build", 1, d.ID)
	attemptOn(t, e, fl, run.ID, "docs")
	stopRun(t, e, run.ID)
}

// Four attempts of four members wait on their decisions, a fifth task is
// ready for a slot: one waiting attempt whose turn ended is parked, given
// back, and the fifth dispatches; the others keep their sessions.
func TestWaitingAttemptsFreeTheRunsSlots(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := decisionEngine(t)
	ids := []string{"a", "b", "c", "d"}
	var tasks []Task
	for i, id := range ids {
		tasks = append(tasks, work(id, fmt.Sprintf("m%d", i+1)))
	}
	tasks = append(tasks, work("e", "m5"))
	run := launchManaged(t, e, repoCrew(dir, tasks...))
	sids := map[string]string{}
	for _, id := range ids {
		ts, _, _ := attemptOn(t, e, fl, run.ID, id)
		ask(t, e, ts.Attempt.SessionID, clarification("q-"+id, "Which way for "+id+"?"))
		sids[id] = ts.Attempt.SessionID
	}
	if ts := taskOf(t, e, run.ID, "e"); ts.Attempts != 0 {
		t.Fatalf("a fifth attempt in flight: %+v", ts)
	}
	for _, id := range ids {
		reportDone(e, sids[id])
	}
	attemptOn(t, e, fl, run.ID, "e")
	e.pumpLocked(runOf(t, e, run.ID))
	given := 0
	for _, id := range ids {
		ts := taskOf(t, e, run.ID, id)
		switch {
		case ts.Attempt.Stage == stageDecision:
			given++
		case ts.Attempt.State != ledger.AttemptRunning:
			t.Fatalf("%s: %+v", id, ts.Attempt)
		}
	}
	if given != 1 {
		t.Fatalf("%d waiting attempts given up for one slot", given)
	}
	stopRun(t, e, run.ID)
}

// The run's four slots are full and the ready task's member is one whose
// own attempt waits with its turn ended: that attempt is parked, which
// frees the member and a slot at once, and the task starts; the other
// attempts keep their sessions (I12).
func TestAMembersOwnWaitingAttemptGivesUpItsSlot(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("a", "m1"), work("b", "m2"), work("c", "m3"), work("d", "m4"), work("a2", "m1")))
	a, l, _ := attemptOn(t, e, fl, run.ID, "a")
	for _, id := range []string{"b", "c", "d"} {
		attemptOn(t, e, fl, run.ID, id)
	}
	d := ask(t, e, a.Attempt.SessionID, clarification("q1", "Which way?"))
	mirrorOf(t, l, d.ID)
	reportDone(e, a.Attempt.SessionID)
	waitGivenBack(t, e, run.ID, "a", 1, d.ID)
	attemptOn(t, e, fl, run.ID, "a2")
	for _, id := range []string{"b", "c", "d"} {
		if ts := taskOf(t, e, run.ID, id); ts.Attempt.State != ledger.AttemptRunning {
			t.Fatalf("%s: %+v", id, ts.Attempt)
		}
	}
	stopRun(t, e, run.ID)
}

// An attempt that exits while its decision is open is given back, and no
// attempt is reserved while the decision is open; once it is answered the
// next attempt starts at the checkpoint with the answer in its task file.
// Three times over: the task never fails for its attempts, and each
// refund has its refunded row.
func TestAnAttemptEndedWhileWaitingIsNotCounted(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	var prev, checkpoint string
	for n := 1; n <= maxAttemptsPerTask; n++ {
		ts := attemptLine(t, e, fl, run.ID, "build", n)
		if n > 1 {
			_, f := taskFileOf(t, ts)
			if ts.Attempt.Start != checkpoint {
				t.Fatalf("attempt %d starts at %s, not the checkpoint %s", n, short(ts.Attempt.Start), short(checkpoint))
			}
			if td := decisionIn(t, f, prev); td.State != ledger.DecisionApplied || td.Answer != fmt.Sprintf("answer %d", n-1) {
				t.Fatalf("attempt %d's task file: %+v", n, td)
			}
		}
		writeFile(t, filepath.Join(ts.Attempt.Worktree, fmt.Sprintf("step%d.txt", n)), "s\n")
		commitAll(t, ts.Attempt.Worktree, fmt.Sprintf("step %d", n))
		if _, err := e.SaveCheckpoint(t.Context(), ts.Attempt.SessionID, fmt.Sprintf("step %d", n), "the rest", ""); err != nil {
			t.Fatal(err)
		}
		checkpoint = head(t, ts.Attempt.Worktree)
		d := ask(t, e, ts.Attempt.SessionID, clarification(fmt.Sprintf("q%d", n), "Which way now?"))
		_, p := fl.attemptSession(t, ts)
		p.End(0)
		waitGivenBack(t, e, run.ID, "build", n, d.ID)
		e.pumpLocked(runOf(t, e, run.ID))
		if got := taskOf(t, e, run.ID, "build"); got.Attempts != n {
			t.Fatalf("an attempt reserved while %s is open: %+v", d.ID, got)
		}
		rec := decisionWhen(t, ld, d.ID, "refunded", hasKind(ledger.HistoryRefunded))
		if kindCount(rec, ledger.HistoryRefunded) != 1 {
			t.Fatalf("%s's history: %q", d.ID, historyKinds(rec))
		}
		answer(t, e, d.ID, Response{ResponseID: "r1", Text: fmt.Sprintf("answer %d", n), ExpectedRev: 1})
		decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
		prev = d.ID
	}
	attemptLine(t, e, fl, run.ID, "build", maxAttemptsPerTask+1)
	if x := extraOf(t, e, run.ID, "build"); x != maxAttemptsPerTask {
		t.Fatalf("%d attempts given back, want %d", x, maxAttemptsPerTask)
	}
	stopRun(t, e, run.ID)
}

// An attempt that submitted after its decision was applied, and failed its
// checks, is counted as any is: no refund.
func TestAnAttemptThatSubmittedIsCounted(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test -f ok.txt"}}}
	run := launchManaged(t, e, repoCrew(dir, build))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	mirrorOf(t, l, d.ID)
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	lineWith(t, p, "Decision "+d.ID)
	commitResult(t, e, b, "x.txt", "x\n", "no ok.txt")
	// Attempt 2 dispatches as soon as attempt 1 fails: waited for, then
	// attempt 1 read where it lasts, in the task's attempts and the
	// ledger's row, never as the task's latest.
	attemptLine(t, e, fl, run.ID, "build", 2)
	_, as, err := e.TaskOf(run.ID, "build")
	if err != nil || len(as) < 2 || as[0].State != ledger.AttemptFailed || as[0].Stage == stageDecision || extraOf(t, e, run.ID, "build") != 0 {
		t.Fatalf("an attempt that submitted was given back: %+v %v", as, err)
	}
	rows, err := ld.Attempts(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(rows, func(a ledger.AttemptRow) bool { return a.TaskID == "build" && a.N == 1 }); i < 0 || rows[i].State != ledger.AttemptFailed || rows[i].Stage == stageDecision {
		t.Fatalf("attempt 1's row: %+v", rows)
	}
	if rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied)); kindCount(rec, ledger.HistoryRefunded) != 0 {
		t.Fatalf("%s's history: %q", d.ID, historyKinds(rec))
	}
	stopRun(t, e, run.ID)
}

// failBuild ends build's attempts' sessions until it fails for good.
func failBuild(t *testing.T, e *Engine, fl *fakeLauncher, runID string) {
	t.Helper()
	for n := 1; n <= maxAttemptsPerTask; n++ {
		ts := attemptLine(t, e, fl, runID, "build", n)
		_, p := fl.attemptSession(t, ts)
		p.End(1)
	}
	waitTask(t, e, runID, "build", ledger.TaskFailed, maxAttemptsPerTask)
}

// gatedBuild is a run whose deploy needs build and is held by the ship
// gate, and the gate.
func gatedBuild(t *testing.T, runID string) (*Engine, *fakeLauncher, *ledger.Ledger, Run, Decision) {
	t.Helper()
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchGated(t, e, dir, runID, repoCrew(dir, work("build", "lead"), work("deploy", "ops", "build")), shipGate("deploy"))
	return e, fl, ld, run, gateOf(t, e, ld, run.ID)
}

// A gate whose work ended (build failed for good, so deploy, held, will
// not start) is cancelled with the words, its holds released; the run
// ends, and Prune takes it.
func TestADoomedGateIsCancelled(t *testing.T) {
	e, fl, ld, run, g := gatedBuild(t, "api-sweep-000000b1")
	failBuild(t, e, fl, run.ID)
	rec := decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
	if !strings.Contains(rec.Why, "build failed, so deploy will not start") || kindCount(rec, ledger.HistoryCancelled) != 1 {
		t.Fatalf("%s: %q %q", g.ID, rec.Why, historyKinds(rec))
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds %q", got)
	}
	e.ForgetForTest(run.ID)
	if err := ld.Prune(t.Context(), run.ID); err != nil {
		t.Fatalf("the finished run is not pruned: %v", err)
	}
}

// A retry of build after its gate was cancelled asks again: a fresh gate
// holds deploy in the retry's transaction, build succeeds, and deploy
// waits for the fresh gate's answer, then starts.
func TestARetriedTaskAsksItsGateAgain(t *testing.T) {
	e, fl, ld, run, g := gatedBuild(t, "api-sweep-000000b2")
	failBuild(t, e, fl, run.ID)
	decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
	if err := e.RetryTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	fresh := gateOf(t, e, ld, run.ID)
	if fresh.ID == g.ID || fresh.Gate != "ship" {
		t.Fatalf("the fresh gate: %+v", fresh)
	}
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"deploy " + fresh.ID}) {
		t.Fatalf("holds after the retry %q", got)
	}
	if opened := fresh.History[0]; !strings.Contains(string(opened.Detail), "build was retried after "+g.ID+" was cancelled") {
		t.Fatalf("the fresh gate's opening: %s", opened.Detail)
	}
	b := attemptLine(t, e, fl, run.ID, "build", maxAttemptsPerTask+1)
	commitResult(t, e, b, "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 0)
	waitTaskWhere(t, e, run.ID, "deploy", "held by "+fresh.ID, func(ts TaskState) bool {
		return ts.State == ledger.TaskReady && ts.Reason == "waits for decision "+fresh.ID && ts.Attempts == 0
	})
	answer(t, e, fresh.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// A retry in a continuation of the run whose gate ended asks again: the
// continuation derives the gate's requirement and its last decision from
// the chain's rows (readDecisionHistory), and the fresh gate holds the
// work until it is answered. Twice: the gate cancelled as build failed
// for good, and the gate rejected.
func TestARetryAfterAContinuationAsksAgain(t *testing.T) {
	t.Run("its gate cancelled as the work ended", func(t *testing.T) {
		dir, ldir := newRepo(t), t.TempDir()
		e, fl, ld := decisionEngineAt(t, ldir)
		run := launchGated(t, e, dir, "api-sweep-000000c7", repoCrew(dir, work("build", "lead"), work("deploy", "ops", "build")), shipGate("deploy"))
		g := gateOf(t, e, ld, run.ID)
		failBuild(t, e, fl, run.ID)
		decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
		waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
		e2, fl2, ld2 := restart(t, e, ld, ldir, run.ID)
		next := continueRun(t, e2, run.ID)
		if err := e2.RetryTask(t.Context(), next.ID, "build"); err != nil {
			t.Fatal(err)
		}
		fresh := gateOf(t, e2, ld2, next.ID)
		if fresh.ID == g.ID || !strings.Contains(string(fresh.History[0].Detail), "build was retried after "+g.ID+" was cancelled") {
			t.Fatalf("the fresh gate: %+v %s", fresh, fresh.History[0].Detail)
		}
		if got := activeHolds(t, ld2, next.ID); !slices.Equal(got, []string{"deploy " + fresh.ID}) {
			t.Fatalf("holds after the retry %q", got)
		}
		b := attemptLine(t, e2, fl2, next.ID, "build", maxAttemptsPerTask+1)
		commitResult(t, e2, b, "built.txt", "b\n", "built")
		waitTask(t, e2, next.ID, "build", ledger.TaskSucceeded, 0)
		waitTaskWhere(t, e2, next.ID, "deploy", "held by "+fresh.ID, func(ts TaskState) bool {
			return ts.State == ledger.TaskReady && ts.Reason == "waits for decision "+fresh.ID && ts.Attempts == 0
		})
		answer(t, e2, fresh.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
		waitAttempt(t, e2, next.ID, "deploy", ledger.AttemptRunning)
		stopRun(t, e2, next.ID)
	})
	t.Run("its gate rejected", func(t *testing.T) {
		dir, ldir := newRepo(t), t.TempDir()
		e, _, ld := decisionEngineAt(t, ldir)
		run := launchGated(t, e, dir, "api-sweep-000000c8", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
		g := gateOf(t, e, ld, run.ID)
		answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1})
		waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 0)
		waitOutcome(t, e, run.ID, ledger.OutcomeCancelled)
		e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
		next := continueRun(t, e2, run.ID)
		if err := e2.RetryTask(t.Context(), next.ID, "deploy"); err != nil {
			t.Fatal(err)
		}
		fresh := gateOf(t, e2, ld2, next.ID)
		if !strings.Contains(string(fresh.History[0].Detail), "deploy was retried after "+g.ID+" rejected it") {
			t.Fatalf("the fresh gate's opening: %s", fresh.History[0].Detail)
		}
		if ts := taskOf(t, e2, next.ID, "deploy"); ts.Attempts != 0 || ts.Reason != "waits for decision "+fresh.ID {
			t.Fatalf("deploy after the retry: %+v", ts)
		}
		answer(t, e2, fresh.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
		waitAttempt(t, e2, next.ID, "deploy", ledger.AttemptRunning)
		stopRun(t, e2, next.ID)
	})
}

// A change that makes a gate's work runnable again in a run with no room
// for the gate (a reopen's, a continuation's, which no retry's check
// guards) notes it once, and pump's backstop does not ask again at every
// pump: the task is never dispatched, and no regate is on its way.
func TestARegateWithNoRoomAsksOnce(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000c9", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1})
	waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 0)
	r := runOf(t, e, run.ID)
	err := e.transact(t.Context(), r, func() tx {
		// The run full, as a thousand requests would leave it.
		r.dec.opened = ledger.MaxDecisionsPerRun
		return tx{events: e.retryTask(r, r.task("deploy"))}
	}, nil)
	e.afterTransact(r, err)
	for range 3 {
		e.pumpLocked(r)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.mu.Lock()
		regating := r.dec.regating
		e.mu.Unlock()
		if !regating {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a regate is still on its way")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := logCount(t, e, run.ID, "cannot be asked again"); n != 1 {
		t.Fatalf("noted %d times", n)
	}
	if ts := taskOf(t, e, run.ID, "deploy"); ts.Attempts != 0 || ts.State != ledger.TaskReady {
		t.Fatalf("deploy without its gate: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// A retry that would ask two gates again, in a run with room for one, is
// refused decision_limit: the task stays failed, and no gate opens.
func TestARetryNeedsRoomForEveryGate(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	smoke := PackageDecision{ID: "smoke", Question: "Smoke it?", Kind: ledger.DecisionApproval, Before: []string{"smoke"}}
	run := launchGated(t, e, dir, "api-sweep-000000ca", repoCrew(dir, work("build", "lead"), work("deploy", "ops", "build"), work("smoke", "qa", "build")), shipGate("deploy"), smoke)
	failBuild(t, e, fl, run.ID)
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	before, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID})
	if err != nil || len(before) != 2 || !before[0].Final() || !before[1].Final() {
		t.Fatalf("the gates: %+v %v", before, err)
	}
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	r.dec.opened = ledger.MaxDecisionsPerRun - 1
	e.mu.Unlock()
	var de *DecisionError
	if err := e.RetryTask(t.Context(), run.ID, "build"); !errors.As(err, &de) || de.Code != CodeDecisionLimit || !strings.Contains(de.Words, "ship, smoke") {
		t.Fatalf("the retry: %v", err)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskFailed {
		t.Fatalf("build after the refused retry: %+v", ts)
	}
	if after, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID}); err != nil || len(after) != 2 {
		t.Fatalf("decisions after the refused retry: %+v %v", after, err)
	}
	stopRun(t, e, run.ID)
}

// A Reject cancels deploy; its retry asks again ("after dr-1 rejected
// it"), and the fresh gate's Approve starts it.
func TestARetriedRejectedTaskAsksAgain(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000b3", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1})
	waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 0)
	if err := e.RetryTask(t.Context(), run.ID, "deploy"); err != nil {
		t.Fatal(err)
	}
	fresh := gateOf(t, e, ld, run.ID)
	if !strings.Contains(string(fresh.History[0].Detail), "deploy was retried after "+g.ID+" rejected it") {
		t.Fatalf("the fresh gate's opening: %s", fresh.History[0].Detail)
	}
	if ts := taskOf(t, e, run.ID, "deploy"); ts.Attempts != 0 || ts.Reason != "waits for decision "+fresh.ID {
		t.Fatalf("deploy after the retry: %+v", ts)
	}
	answer(t, e, fresh.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// A build that failed its checks and is accepted anyway re-arms deploy,
// which a fresh gate holds: its gate was cancelled when build failed.
func TestAnAcceptedTaskAsksItsGateAgain(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"false"}}}
	run := launchGated(t, e, dir, "api-sweep-000000b4", repoCrew(dir, build, work("deploy", "ops", "build")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	for n := 1; n <= maxAttemptsPerTask; n++ {
		b := attemptLine(t, e, fl, run.ID, "build", n)
		commitResult(t, e, b, fmt.Sprintf("b%d.txt", n), "b\n", "built")
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, maxAttemptsPerTask)
	decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
	if _, err := e.Accept(t.Context(), run.ID, "admin", Command{ID: "c1"}, "build"); err != nil {
		t.Fatal(err)
	}
	fresh := gateOf(t, e, ld, run.ID)
	if fresh.ID == g.ID || !strings.Contains(string(fresh.History[0].Detail), "build was accepted") {
		t.Fatalf("the fresh gate: %+v %s", fresh, fresh.History[0].Detail)
	}
	waitTaskWhere(t, e, run.ID, "deploy", "held by "+fresh.ID, func(ts TaskState) bool {
		return ts.State == ledger.TaskReady && ts.Reason == "waits for decision "+fresh.ID && ts.Attempts == 0
	})
	stopRun(t, e, run.ID)
}

// An approved gate stays released for the run chain: deploy cancelled and
// retried starts with no fresh gate.
func TestAnApprovedGateStaysReleased(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000b5", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	if err := e.CancelTask(t.Context(), run.ID, "deploy"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 1)
	if err := e.RetryTask(t.Context(), run.ID, "deploy"); err != nil {
		t.Fatal(err)
	}
	waitTaskWhere(t, e, run.ID, "deploy", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if rows, _ := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID}); len(rows) != 1 {
		t.Fatalf("decisions after the retry: %+v", rows)
	}
	stopRun(t, e, run.ID)
}

// The operator's cancel with release is the one waiver: deploy starts,
// and a cancel and a retry of it later ask nothing again.
func TestAReleasingCancelIsAWaiver(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000b6", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	rc, err := e.CancelDecision(t.Context(), g.ID, "c1", true, "ship it anyway", "admin")
	if err != nil || rc.Op != ledger.HistoryCancelled {
		t.Fatalf("the cancel: %+v %v", rc, err)
	}
	rec := decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
	if !strings.Contains(rec.Why, "cancelled by the operator, not approved") || kindCount(rec, ledger.HistoryCancelled) != 1 {
		t.Fatalf("%s: %q %q", g.ID, rec.Why, historyKinds(rec))
	}
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	if err := e.CancelTask(t.Context(), run.ID, "deploy"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 1)
	if err := e.RetryTask(t.Context(), run.ID, "deploy"); err != nil {
		t.Fatal(err)
	}
	waitTaskWhere(t, e, run.ID, "deploy", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if rows, _ := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID}); len(rows) != 1 {
		t.Fatalf("decisions after the retry: %+v", rows)
	}
	stopRun(t, e, run.ID)
}

// The operator's cancel without release of a gate that holds the run's
// last task, which has no attempt: the decision makes one transition, one
// cancelled row, before the task is cancelled; the run's outcome follows
// in the same transaction: decision.cancelled, task.cancelled,
// run.finished once.
func TestCancellingTheLastHeldTasksGateEndsTheRunOnce(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000b7", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	if _, err := e.CancelDecision(t.Context(), g.ID, "c1", false, "", "admin"); err != nil {
		t.Fatal(err)
	}
	rec := decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
	if got := historyKinds(rec); !slices.Equal(got, []string{ledger.HistoryOpened, ledger.HistoryRouted, ledger.HistoryCancelled}) {
		t.Fatalf("history %q", got)
	}
	dep := waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 0)
	if dep.Reason != "cancelled with "+g.ID+" by the operator" {
		t.Fatalf("deploy: %+v", dep)
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeCancelled)
	finished := 0
	for _, k := range sqlKinds(t, ld, run.ID) {
		if k == "run.finished" {
			finished++
		}
	}
	order := eventOrder(t, ld, run.ID)
	if finished != 1 || !(order["decision.cancelled"] < order["task.cancelled"] && order["task.cancelled"] < order["run.finished"]) {
		t.Fatalf("events %q", sqlKinds(t, ld, run.ID))
	}
}

// The operator's cancel without release of a gate that holds build and
// deploy, deploy needing build: build's cancel dooms deploy and settles
// the run before deploy's own; the run finishes once, after both tasks'
// cancels.
func TestCancellingAGateOfDependentTasksEndsTheRunOnce(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000cb", repoCrew(dir, work("build", "lead"), work("deploy", "ops", "build")), shipGate("build", "deploy"))
	g := gateOf(t, e, ld, run.ID)
	if _, err := e.CancelDecision(t.Context(), g.ID, "c1", false, "", "admin"); err != nil {
		t.Fatal(err)
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeCancelled)
	kinds := sqlKinds(t, ld, run.ID)
	finished, lastCancel, at := 0, -1, -1
	for i, k := range kinds {
		switch k {
		case "run.finished":
			finished, at = finished+1, i
		case "task.cancelled":
			lastCancel = i
		}
	}
	if finished != 1 || at < lastCancel {
		t.Fatalf("events %q", kinds)
	}
}

// The run's end cancels every decision not final, in the transaction that
// gives it its outcome: a gate that holds no task keeps no run open, and is
// cancelled with the words; the run is pruned after.
func TestTheRunsEndCancelsItsDecisions(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	note := PackageDecision{ID: "note", Question: "Note the release?", Kind: ledger.DecisionClarification}
	run := launchGated(t, e, dir, "api-sweep-000000b8", repoCrew(dir, work("build", "lead")), note)
	g := gateOf(t, e, ld, run.ID)
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	commitResult(t, e, b, "built.txt", "b\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	rec := decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
	if want := "the run ended, succeeded, before " + g.ID + " was answered"; rec.Why != want {
		t.Fatalf("%s's words: %q, want %q", g.ID, rec.Why, want)
	}
	if order := eventOrder(t, ld, run.ID); order["decision.cancelled"] > order["run.finished"] {
		t.Fatalf("events %q", sqlKinds(t, ld, run.ID))
	}
	e.ForgetForTest(run.ID)
	if err := ld.Prune(t.Context(), run.ID); err != nil {
		t.Fatalf("the finished run is not pruned: %v", err)
	}
}

// A gate that holds no task may be answered while the run lasts (noted),
// and keeps no run open.
func TestAGateWithNoTaskKeepsNoRunOpen(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	note := PackageDecision{ID: "note", Question: "Note the release?", Kind: ledger.DecisionClarification}
	run := launchGated(t, e, dir, "api-sweep-000000b9", repoCrew(dir, work("build", "lead")), note)
	g := gateOf(t, e, ld, run.ID)
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	answer(t, e, g.ID, Response{ResponseID: "r1", Text: "noted", ExpectedRev: 1})
	rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowNoted || len(app.Tasks) != 0 {
		t.Fatalf("the application: %+v", app)
	}
	commitResult(t, e, b, "built.txt", "b\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
}

// A retry that would ask a gate again in a run with 64 decisions open,
// the ledger's other bound, is refused decision_limit before anything
// changes, though the run's total has room: no gate opens, deploy stays
// cancelled, and nothing is carried into the run's next commit, so a
// cancel of one of the 64 commits and frees the place the retry then
// takes (#636, review round 2).
func TestARetryNeedsRoomAmongTheOpenDecisions(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000cb", repoCrew(dir, work("deploy", "ops"), work("idle", "qa")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1})
	waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 0)
	waitAttempt(t, e, run.ID, "idle", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	// 64 decisions open, moved into the continuation's engine with its
	// first commit, as a crew's questions would leave them.
	fillOpen(t, ld, run.ID, ledger.MaxOpenPerRun)
	next := continueRun(t, e, run.ID)
	var de *DecisionError
	if err := e.RetryTask(t.Context(), next.ID, "deploy"); !errors.As(err, &de) || de.Code != CodeDecisionLimit || !strings.Contains(de.Words, fmt.Sprintf("%d of its %d open", ledger.MaxOpenPerRun, ledger.MaxOpenPerRun)) {
		t.Fatalf("the retry: %v", err)
	}
	if ts := taskOf(t, e, next.ID, "deploy"); ts.State != ledger.TaskCancelled {
		t.Fatalf("deploy after the refused retry: %+v", ts)
	}
	rows, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: next.ID})
	if err != nil {
		t.Fatal(err)
	}
	var standing ledger.DecisionRow
	for _, row := range rows {
		if row.Origin == ledger.OriginPlan && !row.Final() {
			t.Fatalf("a gate opened by the refused retry: %+v", row)
		}
		if row.State == ledger.DecisionOpen {
			standing = row
		}
	}
	if _, err := e.CancelDecision(t.Context(), standing.ID(), "c1", false, "", "admin"); err != nil {
		t.Fatalf("the run's next change: %v", err)
	}
	decisionWhen(t, ld, standing.ID(), "cancelled", inState(ledger.DecisionCancelled))
	if paused(t, e, next.ID) {
		t.Fatal("the run paused: a change was carried")
	}
	if err := e.RetryTask(t.Context(), next.ID, "deploy"); err != nil {
		t.Fatalf("the retry with a place free: %v", err)
	}
	fresh := gateOf(t, e, ld, next.ID)
	if got := activeHolds(t, ld, next.ID); !slices.Equal(got, []string{"deploy " + fresh.ID}) {
		t.Fatalf("holds after the retry %q", got)
	}
	stopRun(t, e, next.ID)
}
