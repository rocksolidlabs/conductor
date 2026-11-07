package crew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
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
	rc, err := e.CommentDecision(t.Context(), d.ID, "o1", 0, MoreInfoKind, "Which table is it for?", "admin")
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
		if _, err := e.CommentDecision(t.Context(), id, "o1", 0, MoreInfoKind, "More?", "admin"); !errors.As(err, &de) || de.Code != CodeNoRecipient || !strings.Contains(de.Words, want) {
			t.Fatalf("a request for more of %s: %v", id, err)
		}
		after := decisionWhen(t, ld, id, "read", func(ledger.DecisionRecord) bool { return true })
		if after.Rev != before.Rev || len(after.History) != len(before.History) {
			t.Fatalf("a refused request for more changed %s: rev %d → %d, %q", id, before.Rev, after.Rev, historyKinds(after))
		}
		rc, err := e.CommentDecision(t.Context(), id, "o2", 0, CommentKind, "Noted.", "admin")
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
	if _, err := e.CommentDecision(t.Context(), d.ID, "o1", 0, MoreInfoKind, "For which table?", "admin"); err != nil {
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
// with another body is op_conflict, also one that differs only in what
// cleaning its text removes (the fingerprint is of the body as sent), and a
// text that is not UTF-8 is invalid.
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
		return e.CommentDecision(t.Context(), d.ID, "o1", 0, CommentKind, "Looks right.", "admin")
	}
	c1, err := comment()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := comment()
	same(t, c1, c2, err)
	var de *DecisionError
	if _, err := e.CommentDecision(t.Context(), d.ID, "o1", 0, CommentKind, "Other words.", "admin"); !errors.As(err, &de) || de.Code != CodeOpConflict {
		t.Fatalf("another body under the same id: %v", err)
	}
	if _, err := e.CommentDecision(t.Context(), d.ID, "o1", 0, CommentKind, "Looks right. ", "admin"); !errors.As(err, &de) || de.Code != CodeOpConflict {
		t.Fatalf("a body that differs as sent, under the same id: %v", err)
	}
	if _, err := e.CommentDecision(t.Context(), d.ID, "o9", 0, CommentKind, "Looks\xffright.", "admin"); !errors.As(err, &de) || de.Code != CodeInvalid {
		t.Fatalf("a text that is not UTF-8: %v", err)
	}
	if _, err := e.CancelDecision(t.Context(), d.ID, "o9", 0, false, "not\xffneeded", "admin"); !errors.As(err, &de) || de.Code != CodeInvalid {
		t.Fatalf("a cancel's words that are not UTF-8: %v", err)
	}
	more := func() (OpReceipt, error) {
		return e.CommentDecision(t.Context(), d.ID, "o2", 0, MoreInfoKind, "Which table?", "admin")
	}
	m1, err := more()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := more()
	same(t, m1, m2, err)
	lineWith(t, p, "The operator asks about "+d.ID)
	to := []Destination{{Kind: "operator"}}
	r1, err := e.RouteDecision(t.Context(), d.ID, "o3", 0, to, "for the operator", "admin")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.RouteDecision(t.Context(), d.ID, "o3", 0, to, "for the operator", "admin")
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
		return e.CancelDecision(t.Context(), wd.ID, "c1", 0, false, "not needed", "admin")
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
	o1, err := e.CommentDecision(t.Context(), moved.ID, "o4", 0, CommentKind, "Keep it small.", "admin")
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
	o2, err := e.CommentDecision(t.Context(), moved.ID, "o4", 0, CommentKind, "Keep it small.", "admin")
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

// An operation is judged at the rev the operator read in its own
// transaction: an answer committed after the operation's look (made at
// the seam between the look and the transaction, where the routes once
// checked the rev; an identity's, kept answered until its recheck is
// confirmed) makes a cancel and a comment sent at the rev before it
// stale_decision, and the answer stands (#637, review round 1).
func TestAnOperationAtARevAnAnswerMovedIsStale(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	w, wl, _ := attemptOn(t, e, fl, run.ID, "docs")
	for i, c := range []struct {
		name string
		sid  string
		op   func(id string) (OpReceipt, error)
	}{
		{"a cancel", b.Attempt.SessionID, func(id string) (OpReceipt, error) {
			return e.CancelDecision(t.Context(), id, "c1", 1, false, "not needed", "admin")
		}},
		{"a comment", w.Attempt.SessionID, func(id string) (OpReceipt, error) {
			return e.CommentDecision(t.Context(), id, "o1", 1, CommentKind, "Looks right.", "admin")
		}},
	} {
		d := ask(t, e, c.sid, clarification("q1", "Which index?"))
		mirrorOf(t, []*session.Local{l, wl}[i], d.ID)
		var rc ResponseReceipt
		e.beforeOperate = func(int64) {
			e.beforeOperate = nil
			var err error
			rc, err = e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1}, "user:nate", identity(e, "nate"))
			if err != nil || rc.Status != "accepted" {
				t.Errorf("the answer: %+v %v", rc, err)
			}
		}
		_, err := c.op(d.ID)
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeStaleDecision || !strings.Contains(de.Words, "rev 1") {
			t.Fatalf("%s sent at the rev before the answer: %v", c.name, err)
		}
		e.ConfirmAuthority(d.ID, rc.K, true)
		rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
		if kindCount(rec, ledger.HistoryCancelled) != 0 || kindCount(rec, ledger.HistoryCommented) != 0 {
			t.Fatalf("%s was written: %q", c.name, historyKinds(rec))
		}
	}
	stopRun(t, e, run.ID)
}

// keptAside writes a comment on decision id straight into the ledger, on
// the run that holds it, in a transaction of its own: the row's rev moves
// and the engine's copy does not, as a write outside the run's
// transactions leaves it (a comment kept on the old run while a
// continuation read the decisions).
func keptAside(t *testing.T, ld *ledger.Ledger, runID, id string) {
	t.Helper()
	n, _ := ledger.ParseDecisionID(id)
	if _, err := ld.Transition(context.Background(), runID, func(tx *ledger.Tx) error {
		row, ok, err := tx.Decision(n)
		if err != nil || !ok {
			return errors.Join(err, errors.New("no row"))
		}
		row.Rev++
		if err := tx.PutDecision(row); err != nil {
			return err
		}
		_, _, err = tx.History(n, ledger.HistoryRow{Kind: ledger.HistoryCommented, By: "admin", Detail: historyDetail(map[string]any{"text": "Seen elsewhere.", "rev": row.Rev}),
			OpID: "aside", Fingerprint: fingerprintOf([]string{CommentKind, "Seen elsewhere."})})
		return err
	}); err != nil {
		t.Fatalf("the write aside: %v", err)
	}
}

// opIn says decision rec's history holds operation opID.
func opIn(rec ledger.DecisionRecord, opID string) bool {
	for _, h := range rec.History {
		if h.OpID == opID {
			return true
		}
	}
	return false
}

// competingLedger is the real ledger whose next transaction of run, once
// armed, first runs compete: a writer between the engine's change, made
// under its lock, and that change's transaction.
type competingLedger struct {
	sqlLedger
	run     string
	mu      sync.Mutex
	compete func()
}

func (l *competingLedger) arm(run string, f func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.run, l.compete = run, f
}

func (l *competingLedger) Transition(ctx context.Context, runID string, fn func(LedgerTx) error) (int64, error) {
	l.mu.Lock()
	f := l.compete
	if f == nil || runID != l.run {
		f = nil
	} else {
		l.compete = nil
	}
	l.mu.Unlock()
	if f != nil {
		f()
	}
	return l.sqlLedger.Transition(ctx, runID, fn)
}

// operatorOps are the operator's operations that take the rev read, each
// with the history kind it writes.
var operatorOps = []struct {
	name, kind string
	do         func(ctx context.Context, e *Engine, id, opID string, rev int) (OpReceipt, error)
}{
	{"a cancel", ledger.HistoryCancelled, func(ctx context.Context, e *Engine, id, opID string, rev int) (OpReceipt, error) {
		return e.CancelDecision(ctx, id, opID, rev, false, "not needed", "admin")
	}},
	{"a comment", ledger.HistoryCommented, func(ctx context.Context, e *Engine, id, opID string, rev int) (OpReceipt, error) {
		return e.CommentDecision(ctx, id, opID, rev, CommentKind, "Looks right.", "admin")
	}},
	{"a request for more", ledger.HistoryInfoRequested, func(ctx context.Context, e *Engine, id, opID string, rev int) (OpReceipt, error) {
		return e.CommentDecision(ctx, id, opID, rev, MoreInfoKind, "Which table?", "admin")
	}},
	{"a route", ledger.HistoryRouted, func(ctx context.Context, e *Engine, id, opID string, rev int) (OpReceipt, error) {
		return e.RouteDecision(ctx, id, opID, rev, []Destination{{Kind: "operator"}}, "for the operator", "admin")
	}},
}

// stale says err is the stale_decision refusal of a rev read before rev now.
func stale(err error, read, now int) bool {
	var de *DecisionError
	return errors.As(err, &de) && de.Code == CodeStaleDecision && strings.Contains(de.Words, fmt.Sprintf("at rev %d, not the rev %d read", now, read))
}

// Every operation that takes the rev read is judged by the decision's row,
// in the transaction that applies it, whether or not the engine holds a
// copy of the decision: a write the copy never saw, made between the
// engine's change and its transaction (where the copy once judged), makes
// the operation sent at the rev before it stale_decision, with nothing
// written and the run not paused, and the operation sent at the row's rev
// is taken. A decision whose run is not held is judged the same way, by
// the same judgement (#637, review round 2).
func TestEveryOperationIsJudgedByItsRow(t *testing.T) {
	for _, o := range operatorOps {
		t.Run(o.name+", the engine holding a copy", func(t *testing.T) {
			dir := newRepo(t)
			e, fl, ld := decisionEngine(t)
			cl := &competingLedger{sqlLedger: sqlLedger{l: ld}}
			e.UseLedger(cl)
			run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
			b, l, _ := attemptOn(t, e, fl, run.ID, "build")
			d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
			mirrorOf(t, l, d.ID)
			e.beforeOperate = func(int64) {
				e.beforeOperate = nil
				cl.arm(run.ID, func() { keptAside(t, ld, run.ID, d.ID) })
			}
			if _, err := o.do(t.Context(), e, d.ID, "o1", 1); !stale(err, 1, 2) {
				t.Fatalf("%s at the rev before the write aside: %v", o.name, err)
			}
			rec := decisionWhen(t, ld, d.ID, "the write aside", func(rec ledger.DecisionRecord) bool { return opIn(rec, "aside") })
			if opIn(rec, "o1") || rec.State != ledger.DecisionOpen || rec.Rev != 2 {
				t.Fatalf("%s refused wrote: %s rev %d %q", o.name, rec.State, rec.Rev, historyKinds(rec))
			}
			if paused(t, e, run.ID) {
				t.Fatalf("%s refused paused the run", o.name)
			}
			rc, err := o.do(t.Context(), e, d.ID, "o2", 2)
			if err != nil || rc.Op != o.kind {
				t.Fatalf("%s at the row's rev: %+v %v", o.name, rc, err)
			}
			if rec := decisionWhen(t, ld, d.ID, o.name, func(rec ledger.DecisionRecord) bool { return opIn(rec, "o2") }); rec.Rev != rc.Rev {
				t.Fatalf("%s's receipt says rev %d, its row %d", o.name, rc.Rev, rec.Rev)
			}
			stopRun(t, e, run.ID)
		})
	}
	for _, o := range operatorOps {
		t.Run(o.name+", its run not held", func(t *testing.T) {
			dir := newRepo(t)
			e, fl, ld := decisionEngine(t)
			run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
			b, _, _ := attemptOn(t, e, fl, run.ID, "build")
			d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
			stopRun(t, e, run.ID)
			e.beforeOperate = func(int64) {
				e.beforeOperate = nil
				keptAside(t, ld, run.ID, d.ID)
			}
			_, err := o.do(t.Context(), e, d.ID, "o1", 1)
			var de *DecisionError
			switch o.kind {
			case ledger.HistoryCancelled, ledger.HistoryInfoRequested:
				// Refused for the run's state before any rev is judged.
				if !errors.As(err, &de) || de.Code != CodeNotHeld && de.Code != CodeNoRecipient {
					t.Fatalf("%s of a run not held: %v", o.name, err)
				}
				return
			}
			if !stale(err, 1, 2) {
				t.Fatalf("%s at the rev before the write aside: %v", o.name, err)
			}
			if rc, err := o.do(t.Context(), e, d.ID, "o2", 2); err != nil || rc.Op != o.kind {
				t.Fatalf("%s at the row's rev: %+v %v", o.name, rc, err)
			}
			rec := decisionWhen(t, ld, d.ID, o.name, func(rec ledger.DecisionRecord) bool { return opIn(rec, "o2") })
			if opIn(rec, "o1") {
				t.Fatalf("%s refused wrote: %q", o.name, historyKinds(rec))
			}
		})
	}
}

// A comment kept on the old run while a continuation is held between its
// read of the decisions and its first transaction moves the decision's
// rev where the continuation's copy never saw it: a cancel sent at the rev
// before the comment is stale_decision, and one sent at the comment's rev
// cancels it (#637, review round 2).
func TestACancelAfterAContinuationIsJudgedByItsRow(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
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
	rc, err := e2.CommentDecision(t.Context(), d.ID, "o1", 1, CommentKind, "Keep it small.", "admin")
	if err != nil || rc.Rev != 2 {
		close(gate)
		t.Fatalf("the comment during the continuation: %+v %v", rc, err)
	}
	close(gate)
	next, ok := <-out
	if !ok {
		t.FailNow()
	}
	if _, err := e2.CancelDecision(t.Context(), d.ID, "c1", 1, false, "", "admin"); !stale(err, 1, 2) {
		t.Fatalf("a cancel at the rev before the comment: %v", err)
	}
	if paused(t, e2, next.ID) {
		t.Fatal("the refused cancel paused the run")
	}
	if rc, err := e2.CancelDecision(t.Context(), d.ID, "c2", 2, false, "", "admin"); err != nil || rc.Op != ledger.HistoryCancelled {
		t.Fatalf("a cancel at the comment's rev: %+v %v", rc, err)
	}
	rec := decisionWhen(t, ld2, d.ID, "cancelled", inState(ledger.DecisionCancelled))
	if opIn(rec, "c1") || !opIn(rec, "c2") {
		t.Fatalf("the cancels written: %q", historyKinds(rec))
	}
	stopRun(t, e2, next.ID)
}
