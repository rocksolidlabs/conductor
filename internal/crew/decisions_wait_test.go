package crew

import (
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
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("a working attempt was given up: %+v", ts.Attempt)
	}
	if ts := taskOf(t, e, run.ID, "docs"); ts.Attempts != 0 {
		t.Fatalf("docs dispatched while its member works: %+v", ts)
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
	// Attempt 1 failed in the ledger: the task's attempt 2 starts at once,
	// so the task's own attempt only passes through it.
	row := ledgerAttempt(t, ld, run.ID, "build", 1, ledger.AttemptFailed)
	if row.Stage == stageDecision || extraOf(t, e, run.ID, "build") != 0 {
		t.Fatalf("an attempt that submitted was given back: %+v", row)
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

// An answer never reaches another task's attempt: the asking attempt gone
// (given back), the answer goes to its task's next attempt through its task
// file, and nothing is typed into the live attempt of another task.
func TestAnAnswerNeverReachesAnotherAttempt(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	b, _, bp := attemptOn(t, e, fl, run.ID, "build")
	_, _, wp := attemptOn(t, e, fl, run.ID, "docs")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	bp.End(0)
	waitGivenBack(t, e, run.ID, "build", 1, d.ID)
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowTaskFile {
		t.Fatalf("the application: %+v", app)
	}
	b2 := attemptLine(t, e, fl, run.ID, "build", 2)
	if _, f := taskFileOf(t, b2); decisionIn(t, f, d.ID).Answer != "btree" {
		t.Fatalf("the next attempt's task file: %+v", f.Decisions)
	}
	if _, err := e.SendMessage(t.Context(), b2.Attempt.SessionID, "writer", "marker"); err != nil {
		t.Fatal(err)
	}
	got := lineWith(t, wp, "marker")
	if strings.Contains(got, d.ID) {
		t.Fatalf("the answer reached docs: %q", got)
	}
	stopRun(t, e, run.ID)
}
