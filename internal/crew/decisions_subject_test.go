package crew

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A decision about something that moved (docs/plans/decisions.md,
// Superseding and cancelling, S1 to S4): a candidate replaced by the
// task's next result, a file whose blob changed or cannot be read, a
// commit the work no longer rests on, the asking task reopened. Each test
// waits on the ledger's record, and holds a line at a seam where an order
// must be made.

// candidateRun is a run whose build fails its check once (no ok.txt) and
// whose docs, of another member, asks about build's first result: build's
// first attempt failed with its candidate, docs running, and that
// candidate's commit.
func candidateRun(t *testing.T, e *Engine, fl *fakeLauncher, dir string) (Run, TaskState, string) {
	t.Helper()
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test -f ok.txt"}}}
	run := launchManaged(t, e, repoCrew(dir, build, work("docs", "writer")))
	b1, _, _ := attemptOn(t, e, fl, run.ID, "build")
	writeFile(t, filepath.Join(b1.Attempt.Worktree, "x.txt"), "x\n")
	commitAll(t, b1.Attempt.Worktree, "first")
	c1 := head(t, b1.Attempt.Worktree)
	submit(t, e, b1.Attempt.SessionID, Result{Summary: "first"})
	waitPast(t, e, run.ID, "build", 1)
	w := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	return run, w, c1
}

// waitPast waits for the task's attempt n to have ended: the task has a
// later attempt, or n failed.
func waitPast(t *testing.T, e *Engine, runID, taskID string, n int) {
	t.Helper()
	waitTaskWhere(t, e, runID, taskID, fmt.Sprintf("past attempt %d", n), func(ts TaskState) bool {
		return ts.Attempt != nil && (ts.Attempt.N > n || ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptFailed)
	})
}

// approveCandidate is an approval request of build's attempt 1's result.
func approveCandidate(runID, commit string) DecisionRequest {
	req := approval("q1", "Ship build's first result?")
	req.Subject = &DecisionSubject{Kind: "candidate", Run: runID, Task: "build", N: 1, Commit: commit}
	return req
}

// secondCandidate commits ok.txt in build's attempt 2 and submits it: its
// next candidate, and its commit.
func secondCandidate(t *testing.T, e *Engine, fl *fakeLauncher, runID string) string {
	t.Helper()
	b2 := attemptLine(t, e, fl, runID, "build", 2)
	writeFile(t, filepath.Join(b2.Attempt.Worktree, "ok.txt"), "ok\n")
	commitAll(t, b2.Attempt.Worktree, "second")
	c2 := head(t, b2.Attempt.Worktree)
	submit(t, e, b2.Attempt.SessionID, Result{Summary: "second"})
	return c2
}

// A task's new result supersedes an open approval of its earlier
// candidate, with the words of what moved; the hold is released and the
// asking agent told.
func TestANewCandidateSupersedesItsApproval(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run, w, c1 := candidateRun(t, e, fl, dir)
	_, wp := fl.attemptSession(t, w)
	lineWith(t, wp, "docs")
	d := ask(t, e, w.Attempt.SessionID, approveCandidate(run.ID, c1))
	c2 := secondCandidate(t, e, fl, run.ID)
	rec := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	if want := fmt.Sprintf("the candidate is now %s, attempt 2", short(c2)); !strings.Contains(rec.Why, want) {
		t.Fatalf("%s's words %q, want %q", d.ID, rec.Why, want)
	}
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds %q", got)
	}
	lineWith(t, wp, d.ID+" was superseded")
	stopRun(t, e, run.ID)
}

// An applied approval of a candidate is superseded too by the task's next
// result: its response and application receipts and history kept, both
// rows in it.
func TestAnAppliedApprovalIsSupersededByANewCandidate(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run, w, c1 := candidateRun(t, e, fl, dir)
	l, wp := fl.attemptSession(t, w)
	lineWith(t, wp, "docs")
	d := ask(t, e, w.Attempt.SessionID, approveCandidate(run.ID, c1))
	mirrorOf(t, l, d.ID)
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	lineWith(t, wp, "Decision "+d.ID)
	decisionWhen(t, ld, d.ID, "entered", hasKind(ledger.HistoryDeliveryEntered))
	secondCandidate(t, e, fl, run.ID)
	rec := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	if len(rec.Responses) != 1 || rec.Applied == "" || !slices.Contains(historyKinds(rec), ledger.HistoryApplied) || kindCount(rec, ledger.HistorySuperseded) != 1 {
		t.Fatalf("%s: %+v %q", d.ID, rec.DecisionRow, historyKinds(rec))
	}
	stopRun(t, e, run.ID)
}

// readUntil reads p's writes until what was written holds want, for up to
// d, and returns them.
func readUntil(t *testing.T, p *sessiontest.FakeProc, want string, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	var got string
	for !strings.Contains(got, want) {
		select {
		case b := <-p.Input:
			got += string(b)
		case <-deadline:
			t.Fatalf("%q not written within %v (got %q)", want, d, got)
		}
	}
	return got
}

// An applied approval whose line has not gone in is never typed once a
// new candidate supersedes it: held before its text, nothing is typed;
// held after its text, in the session's pause before its Enter, the Enter
// is never written; either way delivery.untyped says why. Superseded after
// delivery.entered, the line stands entered.
func TestASupersededApprovalIsNeverTyped(t *testing.T) {
	t.Run("before its text", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		held, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		e.beforeTyping = func(int64) { once.Do(func() { close(held); <-release }) }
		run, w, c1 := candidateRun(t, e, fl, dir)
		_, wp := fl.attemptSession(t, w)
		lineWith(t, wp, "docs")
		d := ask(t, e, w.Attempt.SessionID, approveCandidate(run.ID, c1))
		answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
		<-held
		secondCandidate(t, e, fl, run.ID)
		decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
		close(release)
		rec := decisionWhen(t, ld, d.ID, "untyped", hasKind(ledger.HistoryDeliveryUntyped))
		if !strings.Contains(string(rec.History[len(rec.History)-1].Detail), d.ID+" was superseded before its answer went in") {
			t.Fatalf("the untyped row: %s", rec.History[len(rec.History)-1].Detail)
		}
		// The next line is the agent told, never the answer.
		if got := lineWith(t, wp, d.ID+" was superseded"); strings.Contains(got, "answered by") {
			t.Fatalf("the answer was typed: %q", got)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("after its text", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		// docs's answer line held where its session reports its text
		// typed, before the pause its Enter waits in, until the
		// supersession has committed: the order made at a seam, never
		// left to a pause's length (#636, review round 1). BeforeEnter
		// would not do: the session's writer calls it once its look for
		// a withdrawal has passed, so the Enter follows it whatever came.
		var line atomic.Pointer[string]
		typedText, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		fl.beforeActivity = func(_ string, entry session.ActivityEntry) {
			if want := line.Load(); want != nil && entry.Type == session.ActivityInput && strings.Contains(entry.Message, *want) {
				once.Do(func() { close(typedText); <-release })
			}
		}
		run, w, c1 := candidateRun(t, e, fl, dir)
		_, wp := fl.attemptSession(t, w)
		lineWith(t, wp, "docs")
		d := ask(t, e, w.Attempt.SessionID, approveCandidate(run.ID, c1))
		want := "Decision " + d.ID + " (approval)"
		line.Store(&want)
		answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
		<-typedText
		if got := readUntil(t, wp, want, 30*time.Second); strings.Contains(got, "\r") {
			t.Fatalf("an Enter before the supersession: %q", got)
		}
		secondCandidate(t, e, fl, run.ID)
		decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
		close(release)
		rec := decisionWhen(t, ld, d.ID, "untyped", hasKind(ledger.HistoryDeliveryUntyped))
		if rec.State != ledger.DecisionSuperseded || slices.Contains(historyKinds(rec), ledger.HistoryDeliveryEntered) {
			t.Fatalf("%s: %+v %q", d.ID, rec.DecisionRow, historyKinds(rec))
		}
		// Submit has returned: an Enter it wrote would be in the writes now.
		if got := typed(wp); slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "\r") }) {
			t.Fatalf("an Enter was written after the supersession: %q", got)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("after its Enter", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		run, w, c1 := candidateRun(t, e, fl, dir)
		_, wp := fl.attemptSession(t, w)
		lineWith(t, wp, "docs")
		d := ask(t, e, w.Attempt.SessionID, approveCandidate(run.ID, c1))
		answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
		lineWith(t, wp, "Decision "+d.ID)
		decisionWhen(t, ld, d.ID, "entered", hasKind(ledger.HistoryDeliveryEntered))
		secondCandidate(t, e, fl, run.ID)
		rec := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
		if slices.Contains(historyKinds(rec), ledger.HistoryDeliveryUntyped) {
			t.Fatalf("%s's history: %q", d.ID, historyKinds(rec))
		}
		stopRun(t, e, run.ID)
	})
}

// undelivered approves an attempt's HEAD (or schema.sql at it) and ends the
// attempt while a seam holds the answer's line: the next attempt starts at
// the checkpoint. checkpointAtHead saves it at the approved commit, else
// at the commit before; artifact asks about schema.sql, whose bytes differ
// between the two. It returns the decision's id, the next attempt's task
// file and the ledger.
func undelivered(t *testing.T, checkpointAtHead, artifact bool) (string, TaskFile, *ledger.Ledger) {
	t.Helper()
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.beforeTyping = func(int64) { once.Do(func() { close(held); <-release }) }
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	wt := b.Attempt.Worktree
	writeFile(t, filepath.Join(wt, "schema.sql"), "create table t (id int);\n")
	commitAll(t, wt, "c1")
	save := func() {
		if _, err := e.SaveCheckpoint(t.Context(), b.Attempt.SessionID, "the schema", "the index", ""); err != nil {
			t.Fatal(err)
		}
	}
	if !checkpointAtHead {
		save()
	}
	writeFile(t, filepath.Join(wt, "schema.sql"), "create table t (id bigint);\n")
	commitAll(t, wt, "c2")
	if checkpointAtHead {
		save()
	}
	req := approval("q1", "Merge the schema?")
	if artifact {
		req.Subject = &DecisionSubject{Kind: "artifact", Path: "schema.sql"}
	}
	d := ask(t, e, b.Attempt.SessionID, req)
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	<-held
	p.End(0)
	waitPast(t, e, run.ID, "build", 1)
	close(release)
	b2 := attemptLine(t, e, fl, run.ID, "build", 2)
	_, f := taskFileOf(t, b2)
	t.Cleanup(func() { stopRun(t, e, run.ID) })
	return d.ID, f, ld
}

// An approval applied to a live attempt whose line never went in (the
// agent exited first) is rechecked at the next attempt's start: the
// checkpoint before the approved commit supersedes it, and the task file
// says so, never that it was approved; a checkpoint at the approved commit
// carries it as applied (delivery.task_file); a file's blob other at the
// start supersedes it too.
func TestAnUndeliveredApprovalIsRecheckedAtTheNextStart(t *testing.T) {
	t.Run("the checkpoint before", func(t *testing.T) {
		id, f, ld := undelivered(t, false, false)
		rec := decisionWhen(t, ld, id, "superseded", inState(ledger.DecisionSuperseded))
		if !strings.Contains(rec.Why, "this attempt starts at "+short(f.Start)) || rec.Applied == "" {
			t.Fatalf("%s: %+v", id, rec.DecisionRow)
		}
		if td := decisionIn(t, f, id); td.State != ledger.DecisionSuperseded || !strings.Contains(td.Words, "ask again") {
			t.Fatalf("the task file's decision: %+v", td)
		}
	})
	t.Run("the checkpoint at it", func(t *testing.T) {
		id, f, ld := undelivered(t, true, false)
		decisionWhen(t, ld, id, "carried by the task file", hasKind(ledger.HistoryDeliveryTaskFile))
		if td := decisionIn(t, f, id); td.State != ledger.DecisionApplied || td.Answer != "Approve" {
			t.Fatalf("the task file's decision: %+v", td)
		}
	})
	t.Run("a file's blob", func(t *testing.T) {
		id, f, ld := undelivered(t, false, true)
		rec := decisionWhen(t, ld, id, "superseded", inState(ledger.DecisionSuperseded))
		if !strings.Contains(rec.Why, "schema.sql changed") {
			t.Fatalf("%s: %q", id, rec.Why)
		}
		if td := decisionIn(t, f, id); td.State != ledger.DecisionSuperseded {
			t.Fatalf("the task file's decision: %+v", td)
		}
	})
}

// A file committed again with other bytes between the response and the
// application supersedes its approval: an identity's response, applied
// only once confirmed, finds the blob changed.
func TestAnArtifactThatChangedSupersedes(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	wt := b.Attempt.Worktree
	writeFile(t, filepath.Join(wt, "README.md"), "one\n")
	commitAll(t, wt, "readme")
	req := approval("q1", "Publish the README?")
	req.Subject = &DecisionSubject{Kind: "artifact", Path: "README.md"}
	d := ask(t, e, b.Attempt.SessionID, req)
	rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the response: %+v %v", rc, err)
	}
	writeFile(t, filepath.Join(wt, "README.md"), "two\n")
	commitAll(t, wt, "readme again")
	e.ConfirmAuthority(d.ID, rc.K, true)
	rec := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	if !strings.Contains(rec.Why, "README.md changed: sha256") || rec.Applied != "" {
		t.Fatalf("%s: %+v", d.ID, rec.DecisionRow)
	}
	lineWith(t, p, "Decision "+d.ID+"'s answer was not applied")
	stopRun(t, e, run.ID)
}

// A subject that cannot be read is never validated: the asking attempt
// gone, its file's blob at the next attempt's checkpoint other, or the
// file missing there, supersedes the approval at the response, which is
// refused stale_decision.
func TestAnUnreadableArtifactIsNeverValidated(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(t *testing.T, wt string)
	}{
		{"other bytes", "schema.sql changed", func(t *testing.T, wt string) {
			writeFile(t, filepath.Join(wt, "schema.sql"), "create table t (id bigint);\n")
			commitAll(t, wt, "bigint")
		}},
		{"missing", "schema.sql cannot be read at", func(t *testing.T, wt string) {
			runGit(t, wt, "rm", "-q", "schema.sql")
			commitAll(t, wt, "no schema")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, ld := decisionEngine(t)
			run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
			b, _, p := attemptOn(t, e, fl, run.ID, "build")
			wt := b.Attempt.Worktree
			writeFile(t, filepath.Join(wt, "schema.sql"), "create table t (id int);\n")
			commitAll(t, wt, "schema")
			req := approval("q1", "Merge the schema?")
			req.Subject = &DecisionSubject{Kind: "artifact", Path: "schema.sql"}
			d := ask(t, e, b.Attempt.SessionID, req)
			tc.change(t, wt)
			if _, err := e.SaveCheckpoint(t.Context(), b.Attempt.SessionID, "the schema", "the rest", ""); err != nil {
				t.Fatal(err)
			}
			p.End(0)
			waitGivenBack(t, e, run.ID, "build", 1, d.ID)
			var de *DecisionError
			rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "admin", workbench(e))
			if !errors.As(err, &de) || de.Code != CodeStaleDecision || !strings.Contains(rc.Words, tc.want) {
				t.Fatalf("the response: %+v %v", rc, err)
			}
			rec := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
			if rec.Applied != "" || !strings.Contains(rec.Why, tc.want) {
				t.Fatalf("%s: %+v", d.ID, rec.DecisionRow)
			}
			b2 := attemptLine(t, e, fl, run.ID, "build", 2)
			if _, f := taskFileOf(t, b2); decisionIn(t, f, d.ID).State != ledger.DecisionSuperseded {
				t.Fatalf("the task file: %+v", f.Decisions)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// An approval of a commit is rechecked at the response: the attempt's HEAD
// moved on supersedes it ("HEAD is now …"), the response refused
// stale_decision, the agent told.
func TestAnApprovalOfAMovedCommitIsSuperseded(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	was := head(t, b.Attempt.Worktree)
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	writeFile(t, filepath.Join(b.Attempt.Worktree, "more.txt"), "m\n")
	commitAll(t, b.Attempt.Worktree, "more")
	now := head(t, b.Attempt.Worktree)
	var de *DecisionError
	rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "admin", workbench(e))
	want := fmt.Sprintf("HEAD is now %s, was %s", short(now), short(was))
	if !errors.As(err, &de) || de.Code != CodeStaleDecision || !strings.Contains(rc.Words, want) {
		t.Fatalf("the response: %+v %v", rc, err)
	}
	decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	lineWith(t, p, d.ID+" was superseded: "+want)
	stopRun(t, e, run.ID)
}

// A supersession of an agent's decision whose asking attempt has ended
// releases the hold: the task's next attempt dispatches with the words in
// its task file.
func TestASupersededDecisionLeavesNoHold(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	writeFile(t, filepath.Join(b.Attempt.Worktree, "a.txt"), "a\n")
	commitAll(t, b.Attempt.Worktree, "a")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	p.End(0)
	waitGivenBack(t, e, run.ID, "build", 1, d.ID)
	if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "admin", workbench(e)); err == nil {
		t.Fatal("an approval of a commit the next attempt does not start at was taken")
	}
	decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds %q", got)
	}
	b2 := attemptLine(t, e, fl, run.ID, "build", 2)
	if _, f := taskFileOf(t, b2); !strings.Contains(decisionIn(t, f, d.ID).Words, d.ID+" was superseded") {
		t.Fatalf("the task file: %+v", f.Decisions)
	}
	stopRun(t, e, run.ID)
}

// A gate whose answer's authority could not be rechecked is superseded by
// a fresh gate that takes its holds: the old one's row released, the
// fresh one's active, its tasks still held.
func TestASupersededGateMovesItsHolds(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000c1", repoCrew(dir, work("deploy", "ops"), work("smoke", "qa")), shipGate("deploy", "smoke"))
	g := gateOf(t, e, ld, run.ID)
	rc, err := e.RespondDecision(t.Context(), g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", identity(e, "nate"))
	if err != nil {
		t.Fatal(err)
	}
	e.ConfirmAuthority(g.ID, rc.K, false)
	old := decisionWhen(t, ld, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	rows, err := ld.Holds(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var released, active []string
	for _, h := range rows {
		if h.ReleasedAt.IsZero() {
			active = append(active, h.TaskID+" "+ledger.DecisionID(h.Decision))
		} else {
			released = append(released, h.TaskID+" "+ledger.DecisionID(h.Decision))
		}
	}
	slices.Sort(active)
	slices.Sort(released)
	if !slices.Equal(active, []string{"deploy " + old.SupersededBy, "smoke " + old.SupersededBy}) || !slices.Equal(released, []string{"deploy " + g.ID, "smoke " + g.ID}) {
		t.Fatalf("holds: active %q released %q", active, released)
	}
	stopRun(t, e, run.ID)
}

// A review that reopens a task withdraws what needed it: a decision a
// withdrawn attempt asked is cancelled with the words, its hold released.
func TestAReopenCancelsTheTasksDecisions(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("check", "critic", "build"), work("docs", "writer", "build")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	commitResult(t, e, b, "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	w, _, _ := attemptOn(t, e, fl, run.ID, "docs")
	d := ask(t, e, w.Attempt.SessionID, clarification("q1", "Which tone?"))
	c, _, _ := attemptOn(t, e, fl, run.ID, "check")
	submit(t, e, c.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Findings: []Finding{{Severity: "major", Message: "fix it"}}})
	rec := decisionWhen(t, ld, d.ID, "cancelled", inState(ledger.DecisionCancelled))
	if !strings.Contains(rec.Why, "reopened by review check") {
		t.Fatalf("%s's words: %q", d.ID, rec.Why)
	}
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds %q", got)
	}
	stopRun(t, e, run.ID)
}
