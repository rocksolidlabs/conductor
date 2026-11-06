package crew

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The operator's operations on a decision (docs/plans/decisions.md,
// Comments and more information, Routing, An operation repeats as a
// response does), and the task file's decisions (The task file).

// TestMoreInfoIsTypedAndBumpsRev: the operator's request for more is typed
// to the asking attempt's live session and bumps the rev, so a response
// against the card before it is stale; the agent answers by a new request
// that supersedes the decision.
func TestMoreInfoIsTypedAndBumpsRev(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	rc, err := e.CommentDecision(t.Context(), d.ID, "o1", MoreInfoKind, "Which table is it for?", "admin")
	if err != nil || rc.Rev != 2 || rc.Op != ledger.HistoryInfoRequested {
		t.Fatalf("the request for more: %+v %v", rc, err)
	}
	lineWith(t, p, "The operator asks about "+d.ID+": Which table is it for?")
	var de *DecisionError
	if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1}, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeStaleDecision {
		t.Fatalf("a response at the rev before: %v", err)
	}
	more := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Which index for the users table?", Supersedes: d.ID})
	decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"build " + more.ID}) {
		t.Fatalf("holds %q", got)
	}
	stopRun(t, e, run.ID)
}

// A request for more has one recipient, the agent that asked, live: one
// parked for a sibling task, a gate and an answered decision are refused
// no_recipient with no row and the rev unchanged, and nothing is typed; a
// comment is still taken.
func TestMoreInfoNeedsALiveAsker(t *testing.T) {
	refused := func(t *testing.T, e *Engine, ld *ledger.Ledger, id, want string) {
		t.Helper()
		before := decisionWhen(t, ld, id, "read", func(ledger.DecisionRecord) bool { return true })
		var de *DecisionError
		if _, err := e.CommentDecision(t.Context(), id, "o1", MoreInfoKind, "More?", "admin"); !errors.As(err, &de) || de.Code != CodeNoRecipient || !strings.Contains(de.Words, want) {
			t.Fatalf("a request for more of %s: %v", id, err)
		}
		after := decisionWhen(t, ld, id, "read", func(ledger.DecisionRecord) bool { return true })
		if after.Rev != before.Rev || len(after.History) != len(before.History) {
			t.Fatalf("a refused request for more changed %s: rev %d → %d, %q", id, before.Rev, after.Rev, historyKinds(after))
		}
		rc, err := e.CommentDecision(t.Context(), id, "o2", CommentKind, "Noted.", "admin")
		if err != nil || rc.Rev != before.Rev+1 {
			t.Fatalf("a comment on %s: %+v %v", id, rc, err)
		}
	}
	t.Run("parked", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "lead")))
		b, _, p := attemptOn(t, e, fl, run.ID, "build")
		d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
		reportDone(e, b.Attempt.SessionID)
		waitGivenBack(t, e, run.ID, "build", 1, d.ID)
		refused(t, e, ld, d.ID, "has ended its attempt")
		if got := typed(p); slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "The operator asks") }) {
			t.Fatalf("typed into the parked attempt: %q", got)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("a gate", func(t *testing.T) {
		dir := newRepo(t)
		e, _, ld := decisionEngine(t)
		run := launchGated(t, e, dir, "api-sweep-000000d1", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
		refused(t, e, ld, gateOf(t, e, ld, run.ID).ID, "is the plan's")
		stopRun(t, e, run.ID)
	})
	t.Run("answered", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
		b, _, _ := attemptOn(t, e, fl, run.ID, "build")
		d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
		if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1}, "user:nate", identity(e, "nate")); err != nil {
			t.Fatal(err)
		}
		refused(t, e, ld, d.ID, d.ID+" is answered")
		stopRun(t, e, run.ID)
	})
}

// A request for more taken while the agent's turn had ended clears the
// mark: a sibling task that becomes ready for the member meanwhile does
// not park the attempt before the agent's next done; the line is typed
// once, and the agent's superseding request holds the task.
func TestMoreInfoKeepsItsAttemptFromBeingParked(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("prep", "ops"), work("build", "lead"), work("docs", "lead", "prep")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	pr, _, _ := attemptOn(t, e, fl, run.ID, "prep")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	reportDone(e, b.Attempt.SessionID)
	if _, err := e.CommentDecision(t.Context(), d.ID, "o1", MoreInfoKind, "For which table?", "admin"); err != nil {
		t.Fatal(err)
	}
	lineWith(t, p, "The operator asks about "+d.ID)
	commitResult(t, e, pr, "prep.txt", "p\n", "prepared")
	waitTask(t, e, run.ID, "docs", ledger.TaskReady, 0)
	e.pumpLocked(runOf(t, e, run.ID))
	unparked(t, e, run.ID, "build")
	if ts := taskOf(t, e, run.ID, "docs"); ts.Attempts != 0 {
		t.Fatalf("docs took the member: %+v", ts)
	}
	more := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Which index for the users table?", Supersedes: d.ID})
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"build " + more.ID}) {
		t.Fatalf("holds %q", got)
	}
	reportDone(e, b.Attempt.SessionID)
	waitGivenBack(t, e, run.ID, "build", 1, more.ID)
	attemptOn(t, e, fl, run.ID, "docs")
	if got := typed(p); slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "The operator asks") }) {
		t.Fatalf("the request for more typed again: %q", got)
	}
	stopRun(t, e, run.ID)
}

// Each operation sent again after a lost reply gets its receipt again and
// writes nothing (the request for more typed once): also after the
// decision was answered, and after a continuation moved it; the same id
// with another body is op_conflict.
func TestAnOperationRepeatsItsReceipt(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	w, _, _ := attemptOn(t, e, fl, run.ID, "docs")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	same := func(t *testing.T, first, again OpReceipt, err error) {
		t.Helper()
		if err != nil || !again.Repeat || again.Seq != first.Seq || again.Rev != first.Rev || again.Op != first.Op {
			t.Fatalf("the repeat: %+v (first %+v) %v", again, first, err)
		}
	}
	comment := func() (OpReceipt, error) {
		return e.CommentDecision(t.Context(), d.ID, "o1", CommentKind, "Looks right.", "admin")
	}
	c1, err := comment()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := comment()
	same(t, c1, c2, err)
	var de *DecisionError
	if _, err := e.CommentDecision(t.Context(), d.ID, "o1", CommentKind, "Other words.", "admin"); !errors.As(err, &de) || de.Code != CodeOpConflict {
		t.Fatalf("another body under the same id: %v", err)
	}
	more := func() (OpReceipt, error) {
		return e.CommentDecision(t.Context(), d.ID, "o2", MoreInfoKind, "Which table?", "admin")
	}
	m1, err := more()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := more()
	same(t, m1, m2, err)
	lineWith(t, p, "The operator asks about "+d.ID)
	to := []Destination{{Kind: "operator"}}
	r1, err := e.RouteDecision(t.Context(), d.ID, "o3", to, "for the operator", "admin")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.RouteDecision(t.Context(), d.ID, "o3", to, "for the operator", "admin")
	same(t, r1, r2, err)
	rec := decisionWhen(t, ld, d.ID, "commented", hasKind(ledger.HistoryCommented))
	if kindCount(rec, ledger.HistoryCommented) != 1 || kindCount(rec, ledger.HistoryInfoRequested) != 1 || kindCount(rec, ledger.HistoryRouted) != 2 {
		t.Fatalf("history %q", historyKinds(rec))
	}
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: rec.Rev})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	c3, err := comment()
	same(t, c1, c3, err)

	// A cancel, a command with a receipt.
	wd := ask(t, e, w.Attempt.SessionID, clarification("q1", "Which tone?"))
	cancel := func() (OpReceipt, error) {
		return e.CancelDecision(t.Context(), wd.ID, "c1", false, "not needed", "admin")
	}
	k1, err := cancel()
	if err != nil {
		t.Fatal(err)
	}
	k2, err := cancel()
	same(t, k1, k2, err)
	if rec := decisionWhen(t, ld, wd.ID, "cancelled", inState(ledger.DecisionCancelled)); kindCount(rec, ledger.HistoryCancelled) != 1 {
		t.Fatalf("history %q", historyKinds(rec))
	}

	// After a continuation moved the decision.
	moved := ask(t, e, b.Attempt.SessionID, clarification("q3", "Which cache?"))
	o1, err := e.CommentDecision(t.Context(), moved.ID, "o4", CommentKind, "Keep it small.", "admin")
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec := decisionWhen(t, ld, moved.ID, "moved", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == next.ID }); rec.State != ledger.DecisionOpen {
		t.Fatalf("%s after the continuation: %+v", moved.ID, rec.DecisionRow)
	}
	o2, err := e.CommentDecision(t.Context(), moved.ID, "o4", CommentKind, "Keep it small.", "admin")
	same(t, o1, o2, err)
	stopRun(t, e, next.ID)
}

// An applied clarification gate's answer stays in each task it released:
// in their attempts, in a retried attempt of one of them, and in their
// attempts after a continuation.
func TestAReleasedGatesAnswerStaysInItsTasksFiles(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	colour := PackageDecision{ID: "colour", Question: "Which colour?", Kind: ledger.DecisionClarification, Before: []string{"deploy", "smoke"}}
	run := launchGated(t, e, dir, "api-sweep-000000d2", repoCrew(dir, work("deploy", "ops"), work("smoke", "qa")), colour)
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Text: "blue", ExpectedRev: 1})
	carries := func(t *testing.T, ts TaskState) {
		t.Helper()
		if _, f := taskFileOf(t, ts); decisionIn(t, f, g.ID).Answer != "blue" {
			t.Fatalf("%s's task file: %+v", ts.ID, f.Decisions)
		}
	}
	carries(t, attemptLine(t, e, fl, run.ID, "deploy", 1))
	carries(t, attemptLine(t, e, fl, run.ID, "smoke", 1))
	if err := e.CancelTask(t.Context(), run.ID, "smoke"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, e, run.ID, "smoke", ledger.TaskCancelled, 1)
	if err := e.RetryTask(t.Context(), run.ID, "smoke"); err != nil {
		t.Fatal(err)
	}
	carries(t, attemptLine(t, e, fl, run.ID, "smoke", 2))
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	carries(t, attemptLine(t, e, fl, next.ID, "deploy", 2))
	carries(t, attemptLine(t, e, fl, next.ID, "smoke", 3))
	if rec := decisionWhen(t, ld, g.ID, "carried", hasKind(ledger.HistoryDeliveryTaskFile)); kindCount(rec, ledger.HistoryDeliveryTaskFile) != 1 {
		t.Fatalf("history %q", historyKinds(rec))
	}
	stopRun(t, e, next.ID)
}

// Two tasks a gate released read its applied answer at once, both reads
// made before either's mark commits: each task file carries the answer,
// and the decision is marked carried once (delivery.task_file).
func TestAnAnswerIsMarkedCarriedOnce(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	var seen sync.Map
	arrived, release := make(chan string, 2), make(chan struct{})
	e.afterDecisionsRead = func(task string) {
		if _, again := seen.LoadOrStore(task, true); !again {
			arrived <- task
			<-release
		}
	}
	colour := PackageDecision{ID: "colour", Question: "Which colour?", Kind: ledger.DecisionClarification, Before: []string{"deploy", "smoke"}}
	run := launchGated(t, e, dir, "api-sweep-000000d3", repoCrew(dir, work("deploy", "ops"), work("smoke", "qa")), colour)
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Text: "blue", ExpectedRev: 1})
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(60 * time.Second):
			t.Fatal("the two task files were not read")
		}
	}
	close(release)
	for _, id := range []string{"deploy", "smoke"} {
		if _, f := taskFileOf(t, attemptLine(t, e, fl, run.ID, id, 1)); decisionIn(t, f, g.ID).Answer != "blue" {
			t.Fatalf("%s's task file: %+v", id, f.Decisions)
		}
	}
	if rec := decisionWhen(t, ld, g.ID, "carried", hasKind(ledger.HistoryDeliveryTaskFile)); kindCount(rec, ledger.HistoryDeliveryTaskFile) != 1 {
		t.Fatalf("history %q", historyKinds(rec))
	}
	stopRun(t, e, run.ID)
}

// A task file reads the decisions of its run chain by the runs that hold
// them, never a page of every run's in their place: another run's newer
// decisions about a task of the same id, more than a page of them, leave
// the run's own answer in its task's next task file.
func TestATaskFileReadsItsOwnChainsDecisions(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	p.End(0)
	waitGivenBack(t, e, run.ID, "build", 1, d.ID)
	other := launchManaged(t, e, repoCrew(newRepo(t), work("build", "lead")))
	stopRun(t, e, other.ID)
	fillDecisions(t, ld, other.ID, ledger.MaxDecisionsPage+1)
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	b2 := attemptLine(t, e, fl, run.ID, "build", 2)
	if _, f := taskFileOf(t, b2); decisionIn(t, f, d.ID).Answer != "btree" {
		t.Fatalf("the task file: %+v", f.Decisions)
	}
	stopRun(t, e, run.ID)
}

// failingReads is the real ledger whose reads of decisions fail while
// fail is set.
type failingReads struct {
	sqlLedger
	fail atomic.Bool
}

func (l *failingReads) Decisions(ctx context.Context, f ledger.DecisionFilter) ([]ledger.DecisionRow, error) {
	if l.fail.Load() {
		return nil, errors.New("the ledger could not read decisions (test)")
	}
	return l.sqlLedger.Decisions(ctx, f)
}

// An attempt whose task's decisions cannot be read is never started
// without them: it fails at "decisions" before any session, not counted,
// and the run pauses; unpaused once the ledger reads, the next attempt
// starts.
func TestAnAttemptWithoutItsDecisionsIsNotStarted(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	fr := &failingReads{sqlLedger: sqlLedger{l: ld}}
	fr.fail.Store(true)
	e.UseLedger(fr)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	ts := waitTaskWhere(t, e, run.ID, "build", "attempt 1 failed at "+decisionsStage, func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 1 && ts.Attempt.Stage == decisionsStage && ts.State == ledger.TaskReady
	})
	if !paused(t, e, run.ID) || extraOf(t, e, run.ID, "build") != 1 || !strings.Contains(ts.Attempt.Error, "could not read decisions") {
		t.Fatalf("after the failed read: paused %v, extra %d, %+v", paused(t, e, run.ID), extraOf(t, e, run.ID, "build"), ts.Attempt)
	}
	fl.mu.Lock()
	launched := slices.ContainsFunc(fl.calls, func(c launchCall) bool { return strings.HasSuffix(c.name, "build #1") })
	fl.mu.Unlock()
	if launched {
		t.Fatal("attempt 1's session was started")
	}
	fr.fail.Store(false)
	if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	attemptLine(t, e, fl, run.ID, "build", 2)
	stopRun(t, e, run.ID)
}

// The task file carries the task's decisions in the record's words: an
// applied answer with who gave it and why, one still open, one superseded
// with its words, a gate's (golden). A task file without decisions is the
// goldens' of TestTaskFileByteIdenticalWithoutContext, unchanged.
func TestTheTaskFileCarriesTheAnswer(t *testing.T) {
	f := goldenTaskFiles()["checkpoint"]
	f.Decisions = []TaskDecision{
		{ID: "dr-1", Type: ledger.DecisionApproval, Origin: ledger.OriginAgent, Question: "Merge the schema change?", State: ledger.DecisionApplied, Answer: "Approve", Rationale: "the migration is reversible", By: "Nate"},
		{ID: "dr-2", Type: ledger.DecisionClarification, Origin: ledger.OriginPlan, Gate: "colour", Question: "Which colour?", State: ledger.DecisionApplied, Answer: "blue", By: "Nate"},
		{ID: "dr-3", Type: ledger.DecisionApproval, Origin: ledger.OriginAgent, Question: "Ship build's first result?", State: ledger.DecisionSuperseded, Answer: "Approve", By: "Nate",
			Words: "dr-3 was superseded: the candidate is now 9f2c1ab, attempt 3; ask again if you still need it"},
		{ID: "dr-4", Type: ledger.DecisionClarification, Origin: ledger.OriginAgent, Question: "Which index?", State: ledger.DecisionOpen},
	}
	md, js, more, err := fitTaskFile(f)
	if err != nil || more != nil {
		t.Fatalf("the task files: more %q %v", more, err)
	}
	dir := filepath.Join("testdata", "taskfile-decisions")
	got := map[string][]byte{"decisions.md": md, "decisions.json": js}
	for name, b := range got {
		path := filepath.Join(dir, name)
		if *updateTaskFileGoldens {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, want) {
			t.Errorf("%s differs from its golden:\n got: %q\nwant: %q", name, b, want)
		}
	}
	for _, want := range []string{"## Decisions", "dr-1 (approval), applied: Merge the schema change? Nate answered: Approve. Why: the migration is reversible.",
		"dr-3 (approval), superseded:", "never act on it", "dr-4 (clarification), open: Which index? Conductor holds this task"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("task.md lacks %q", want)
		}
	}
	// Cut first, whole, into task-more.json when the files would not fit.
	for i := range maxTaskDecisions {
		f.Decisions = append(f.Decisions, TaskDecision{ID: "dr-" + strings.Repeat("9", i+1), Type: ledger.DecisionClarification, Origin: ledger.OriginAgent,
			Question: "Which way?", State: ledger.DecisionApplied, Answer: strings.Repeat("a", 4000), By: "Nate"})
	}
	_, js, more, err = fitTaskFile(f)
	if err != nil || !bytes.Contains(more, []byte(`"decisions"`)) || bytes.Contains(js, []byte(`"decisions"`)) {
		t.Fatalf("the decisions cut: %v\n%s", err, more)
	}
}
