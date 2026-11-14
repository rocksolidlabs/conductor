package crew

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A person's repair (docs/plans/review-repair.md, RV1 PR-2b): Request
// changes of a task's standing candidate, applied as the repair transition
// a review task's needs_changes makes.

// personsRequest is a person's batch on review id: a change on the lines
// of built.txt, a question and a note.
func personsRequest(id string) ChangeRequest {
	return ChangeRequest{Review: id, By: "Nate", Via: "web", Base: strings.Repeat("9", 40), Summary: "Two things before it ships.", Changes: []RequestedChange{
		{ID: "f1", Kind: "line", Intent: "change", Severity: "major", Path: "built.txt", Side: "candidate", From: 1, To: 1, Lines: []string{"built"}, Message: "Say what was built.", Resolution: "Name the parser."},
		{ID: "f2", Kind: "general", Intent: "question", Message: "Is the lexer in?"},
		{ID: "f3", Kind: "file", Intent: "note", Path: "built.txt", Side: "candidate", Message: "Keep it short."},
	}}
}

// requestChanges is RequestChanges of the task's attempt n at commit, as
// admin.
func requestChanges(e *Engine, runID, id, taskID string, n int, commit string, cr ChangeRequest) (Receipt, error) {
	return e.RequestChanges(context.Background(), runID, "admin", Command{ID: id}, RepairRequest{Task: taskID, Attempt: n, Commit: commit, Request: cr})
}

// A succeeded task's result is its standing candidate: Request changes of
// it drops the result, sets the person's repair (in the task's row, the
// summary the repair's), and its next attempt starts at the candidate with
// the request in its task file; task.reopened names the review and who
// asked.
func TestRequestChangesReopensTheStandingCandidate(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning) // the run stays open
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	cr := personsRequest("rv-0001")

	rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, cr)
	if err != nil || rc.Status != ledger.CommandDone || rc.Intent != "request_changes" || rc.Task == nil || rc.Task.Result != nil || rc.Task.Reason != "changes requested by Nate: 1 change, 1 question, 1 note" {
		t.Fatalf("Request changes: %+v %+v %v", rc, rc.Task, err)
	}
	b2 := attemptN(t, e, run.ID, "build", 2)
	if b2.Attempt.Start != c1 {
		t.Fatalf("attempt 2 starts at %s, not the candidate %s", short(b2.Attempt.Start), short(c1))
	}
	md, f := taskFileOf(t, b2)
	want := cr.clone()
	want.Summary = ""
	if rp := f.Repair; rp == nil || rp.Review != "rv-0001" || rp.Attempt != 1 || rp.Commit != c1 || rp.Start != "" || rp.Summary != cr.Summary || len(rp.Findings) != 0 || !reflect.DeepEqual(rp.Request, want) {
		t.Fatalf("task.json's repair: %+v", f.Repair)
	}
	if f.StartFrom != "the commit Nate asked changes of, in review rv-0001" ||
		!strings.Contains(md, "Nate asked for changes to the commit "+c1+" (review rv-0001, against the base "+cr.Base+"), where this attempt starts. Their summary: "+cr.Summary) ||
		!strings.Contains(md, "- `f1` (major), `built.txt:1`, the candidate's side: Say what was built.") || !strings.Contains(md, "Asked: Name the parser.") ||
		!strings.Contains(md, "### Questions to answer in your summary\n\n- `f2`, the candidate as a whole: Is the lexer in?") || !strings.Contains(md, "Say which you addressed with `addressed`") {
		t.Fatalf("task.md:\n%s", md)
	}
	row := l.taskWhen(t, run.ID, "build", "with the person's repair", func(r ledger.TaskRow) bool { return strings.Contains(r.Repair, `"request"`) })
	var kept TaskRepair
	if err := json.Unmarshal([]byte(row.Repair), &kept); err != nil || kept.Review != "rv-0001" || kept.Commit != c1 || kept.Summary != cr.Summary || !reflect.DeepEqual(kept.Request, want) {
		t.Fatalf("the task's row keeps %s (%v)", row.Repair, err)
	}
	if p := l.payloads(run.ID, "task.reopened"); len(p) != 1 || p[0]["review"] != "rv-0001" || p[0]["by"] != "Nate" || p[0]["attempt"] != 1 || p[0]["findings"] != 3 || p[0]["task"] != "build" {
		t.Fatalf("task.reopened: %+v", p)
	}
	// Its result, once more, clears the repair.
	commitResult(t, e, b2, "built.txt", "built: the parser\n", "built again")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2)
	l.taskWhen(t, run.ID, "build", "with no repair", func(r ledger.TaskRow) bool { return r.Repair == "" && r.ResultAttempt == 2 })
	stopRun(t, e, run.ID)
}

// What rested on the result is withdrawn, as a review task's
// needs_changes withdraws it: a review that approved it runs again on the
// repair's result, and an attempt in flight on it is cancelled at stage
// withdrawn, not counted.
func TestRequestChangesWithdrawsWhatRestedOnIt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("check", "critic", "build"), work("docs", "writer", "build")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	approves(t, e, waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning))
	waitTask(t, e, run.ID, "check", ledger.TaskSucceeded, 1)
	d1 := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)

	if rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, personsRequest("rv-0001")); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	docs := waitTaskWhere(t, e, run.ID, "docs", "its attempt withdrawn", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 1 && ts.Attempt.State == ledger.AttemptCancelled
	})
	if docs.Attempt.Stage != "withdrawn" || docs.State != ledger.TaskBlocked {
		t.Fatalf("docs after the request: %+v %+v", docs, docs.Attempt)
	}
	if check := taskOf(t, e, run.ID, "check"); check.State != ledger.TaskBlocked || check.Result != nil {
		t.Fatalf("the review after the request: %+v", check)
	}
	if l.count(run.ID, "task.withdrawn") != 1 || l.count(run.ID, "attempt.withdrawn") != 1 {
		t.Fatalf("withdrawals: %d of results, %d of attempts", l.count(run.ID, "task.withdrawn"), l.count(run.ID, "attempt.withdrawn"))
	}
	l.taskWhen(t, run.ID, "docs", "withdrawn, not counted", func(r ledger.TaskRow) bool { return r.Withdrawn == 1 && r.Extra == 1 })

	// The repair's result: the review runs again on it, docs starts at it.
	commitResult(t, e, attemptN(t, e, run.ID, "build", 2), "built.txt", "built: the parser\n", "built again")
	b := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2)
	rv := attemptN(t, e, run.ID, "check", 2)
	if rv.Attempt.Start != b.Result.Commit {
		t.Fatalf("the review runs again at %s, not the repair's result %s", short(rv.Attempt.Start), short(b.Result.Commit))
	}
	d2 := attemptN(t, e, run.ID, "docs", 2)
	startsAtTheNewInput(t, d2, b, d1)
	stopRun(t, e, run.ID)
}

// A failed task whose attempts are spent is repaired at its standing
// failed candidate with one more attempt, granted by the request, which
// starts at the candidate with the failed check's receipt beside the
// person's changes.
func TestRequestChangesOnAFailedTaskGrantsAnAttempt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "passes"))}
	run := launchManaged(t, e, repoCrew(dir, build))
	failed := spend(t, e, run.ID, "build", "built.txt")
	c3 := failed.Attempt.Result

	rc, err := requestChanges(e, run.ID, "rq1", "build", 3, c3, personsRequest("rv-0002"))
	if err != nil || rc.Status != ledger.CommandDone || rc.Task == nil || rc.Task.State != ledger.TaskReady {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	b4 := attemptN(t, e, run.ID, "build", 4)
	if b4.Attempt.Start != c3 {
		t.Fatalf("attempt 4 starts at %s, not the candidate %s", short(b4.Attempt.Start), short(c3))
	}
	_, f := taskFileOf(t, b4)
	if f.Repair == nil || f.Repair.Request == nil || f.Repair.Commit != c3 || f.Repair.Attempt != 3 || f.Previous == nil || f.Previous.Attempt != 3 || f.Previous.Check == nil || f.Previous.Check.ID != "test" {
		t.Fatalf("attempt 4's task file: repair %+v, the attempt before %+v", f.Repair, f.Previous)
	}
	if f.MaxAttempts != maxAttemptsPerTask+1 {
		t.Fatalf("attempt 4 of %d", f.MaxAttempts)
	}
	l.taskWhen(t, run.ID, "build", "granted one more attempt", func(r ledger.TaskRow) bool { return r.Extra == 1 && strings.Contains(r.Repair, `"rv-0002"`) })
	stopRun(t, e, run.ID)
}

// A continuation's failed task, with no attempt of its own, has the
// continued run's last attempt as its standing failed candidate: Request
// changes names it by its reference there, and the repair attempt starts
// at it in the continuation. Named as the continuation's own attempt it
// does not stand; the continued run refuses it.
func TestRequestChangesOnAContinuationsFailedTask(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "passes"))}
	run := launchManaged(t, e, repoCrew(dir, build))
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	l.runRowWhen(t, run.ID, "finished", func(r ledger.RunRow) bool { return r.State == ledger.RunFinished })
	ctx := context.Background()
	next, err := e.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, e, next.ID, "build", ledger.TaskFailed, maxAttemptsPerTask)
	waitOutcome(t, e, next.ID, ledger.OutcomeFailed)
	cr := personsRequest("rv-0003")
	if _, err := requestChanges(e, run.ID, "rq0", "build", 3, c3, cr); !errors.Is(err, ErrRunContinued) {
		t.Fatalf("Request changes of the continued run: %v", err)
	}
	if rc, err := requestChanges(e, next.ID, "rq1", "build", 3, c3, cr); !errors.Is(err, ErrStaleCandidate) || rc.Refusal != RefusalStaleCandidate {
		t.Fatalf("the candidate not named by its reference: %+v %v", rc, err)
	}
	rc, err := e.RequestChanges(ctx, next.ID, "admin", Command{ID: "rq2"}, RepairRequest{Task: "build", Attempt: 3, Commit: c3, Imported: run.ID + "/build/3", Request: cr})
	if err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes in the continuation: %+v %v", rc, err)
	}
	b4 := attemptN(t, e, next.ID, "build", 4)
	if b4.Attempt.Start != c3 {
		t.Fatalf("attempt 4 starts at %s, not the candidate %s", short(b4.Attempt.Start), short(c3))
	}
	if _, f := taskFileOf(t, b4); f.Repair == nil || f.Repair.Request == nil || f.Repair.Commit != c3 || f.Repair.Review != "rv-0003" {
		t.Fatalf("attempt 4's repair: %+v", f.Repair)
	}
	if p := l.payloads(next.ID, "task.reopened"); len(p) != 1 || p[0]["imported"] != run.ID+"/build/3" {
		t.Fatalf("task.reopened: %+v", p)
	}
	if r, _ := e.Get(next.ID); r.State != RunRunning || r.Outcome != "" {
		t.Fatalf("the continuation after the request: %s/%q", r.State, r.Outcome)
	}
	stopRun(t, e, next.ID)
}

// A continuation's imported candidate stands only as the attempt that made
// it: named by its reference with another attempt number, the candidate
// is refused stale_candidate, a succeeded task's result as a failed task's
// standing failed candidate, and nothing is recorded (#622, Codex round
// 2); named with its own attempt, each is repaired, the attempt recorded
// the candidate's.
func TestRequestChangesChecksAnImportedCandidatesAttempt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	docs := work("docs", "writer")
	docs.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "passes"))}
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), docs))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	d3 := spend(t, e, run.ID, "docs", "docs.txt").Attempt.Result
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	l.runRowWhen(t, run.ID, "finished", func(r ledger.RunRow) bool { return r.State == ledger.RunFinished })
	ctx := context.Background()
	next, err := e.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, e, next.ID, "docs", ledger.TaskFailed, maxAttemptsPerTask)
	waitOutcome(t, e, next.ID, ledger.OutcomeFailed)
	if ts := taskOf(t, e, next.ID, "build"); ts.State != ledger.TaskSucceeded {
		t.Fatalf("build in the continuation: %+v", ts)
	}
	ask := func(id, task string, n int, commit string) (Receipt, error) {
		return e.RequestChanges(ctx, next.ID, "admin", Command{ID: id}, RepairRequest{Task: task, Attempt: n, Commit: commit, Imported: fmt.Sprintf("%s/%s/%d", run.ID, task, map[string]int{"build": 1, "docs": 3}[task]), Request: personsRequest("rv-0014")})
	}
	if rc, err := ask("rq1", "build", 2, c1); !errors.Is(err, ErrStaleCandidate) || rc.Refusal != RefusalStaleCandidate {
		t.Fatalf("the imported result named with attempt 2: %+v %v", rc, err)
	}
	if rc, err := ask("rq2", "docs", 2, d3); !errors.Is(err, ErrStaleCandidate) || rc.Refusal != RefusalStaleCandidate {
		t.Fatalf("the imported failed candidate named with attempt 2: %+v %v", rc, err)
	}
	if n := l.count(next.ID, "task.reopened"); n != 0 {
		t.Fatalf("%d repairs recorded for candidates that do not stand", n)
	}
	if rc, err := ask("rq3", "build", 1, c1); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("the imported result named with its attempt: %+v %v", rc, err)
	}
	if rc, err := ask("rq4", "docs", 3, d3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("the imported failed candidate named with its attempt: %+v %v", rc, err)
	}
	p := l.payloads(next.ID, "task.reopened")
	if len(p) != 2 || fmt.Sprint(p[0]["attempt"]) != "1" || fmt.Sprint(p[1]["attempt"]) != "3" {
		t.Fatalf("task.reopened: %+v", p)
	}
	stopRun(t, e, next.ID)
}

// A request bound to a candidate that no longer stands is refused with a
// receipt that says what stands now, and changes nothing: an attempt
// running since, a newer result, and a review task's verdict, which is
// not repaired.
func TestRequestChangesRefusesACandidateThatNoLongerStands(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("check", "critic", "build")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	cr := personsRequest("rv-0004")

	rc, err := requestChanges(e, run.ID, "rq0", "check", 1, c1, cr)
	if !errors.Is(err, ErrTaskState) || rc.Refusal != RefusalTaskState || !strings.Contains(err.Error(), "request changes of the candidate it reviewed") {
		t.Fatalf("a review task: %+v %v", rc, err)
	}
	if rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, cr); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	b2 := attemptN(t, e, run.ID, "build", 2)
	rc, err = requestChanges(e, run.ID, "rq2", "build", 1, c1, cr)
	if !errors.Is(err, ErrStaleCandidate) || rc.Refusal != RefusalStaleCandidate || !strings.Contains(err.Error(), "attempt 2 is running: request changes of its result when it ends, or cancel it") {
		t.Fatalf("an attempt running since: %+v %v", rc, err)
	}
	commitResult(t, e, b2, "built.txt", "built again\n", "built again")
	c2 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2).Result.Commit
	rc, err = requestChanges(e, run.ID, "rq3", "build", 1, c1, cr)
	if !errors.Is(err, ErrStaleCandidate) || !strings.Contains(err.Error(), "the result is now attempt 2's "+short(c2)) {
		t.Fatalf("a newer result: %+v %v", rc, err)
	}
	// The newer result named with the old commit does not stand either.
	if _, err := requestChanges(e, run.ID, "rq4", "build", 2, c1, cr); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("attempt 2 with attempt 1's commit: %v", err)
	}
	if got := taskOf(t, e, run.ID, "build"); got.State != ledger.TaskSucceeded || got.Result == nil || got.Result.Attempt != 2 {
		t.Fatalf("the task after the refusals: %+v", got)
	}
	if n := l.count(run.ID, "task.reopened"); n != 1 {
		t.Fatalf("%d repairs recorded", n)
	}
	stopRun(t, e, run.ID)
}

// The same command sent again answers the receipt kept, never a second
// repair; the same id naming another review is a conflict.
func TestRequestChangesRepeatIsTheSameReceipt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	cr := personsRequest("rv-0005")
	first, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, cr)
	if err != nil || first.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", first, err)
	}
	attemptN(t, e, run.ID, "build", 2)
	again, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, cr)
	if err != nil || !again.Repeat || again.Revision != first.Revision || again.Status != ledger.CommandDone {
		t.Fatalf("sent again: %+v %v (first %+v)", again, err, first)
	}
	other := personsRequest("rv-0006")
	if _, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, other); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("the same id for another review: %v", err)
	}
	if n := l.count(run.ID, "task.reopened"); n != 1 {
		t.Fatalf("%d repairs recorded", n)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != 2 {
		t.Fatalf("the task after the repeat: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// A run its one task's success finished is open again once the request is
// recorded, in the engine and in its row, and finishes as the repair
// decides.
func TestRequestChangesReopensAFinishedRun(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	l.runRowWhen(t, run.ID, "finished", func(r ledger.RunRow) bool { return r.State == ledger.RunFinished })

	if rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, personsRequest("rv-0007")); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	if r, _ := e.Get(run.ID); r.State != RunRunning || r.Outcome != "" {
		t.Fatalf("the run after the request: %s/%q", r.State, r.Outcome)
	}
	l.runRowWhen(t, run.ID, "running again", func(r ledger.RunRow) bool { return r.State == ledger.RunRunning && r.Outcome == "" })
	commitResult(t, e, attemptN(t, e, run.ID, "build", 2), "built.txt", "built again\n", "built again")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2)
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	stopRun(t, e, run.ID)
}

// A failed candidate refused before its checks (its manifest, or a setup
// step, failed in its checkout) is no tree to build on: the repair
// attempt starts where the refused attempt started, its task file names
// the candidate's commit, its branch and the refusal beside the person's
// changes, and its agent is launched.
func TestRequestChangesOnACandidateRefusedBeforeItsChecks(t *testing.T) {
	for _, tc := range []struct{ name, file, stage string }{
		{"setup", "broken.txt", "setup deps"},
		{"manifest", manifestFile, "manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, _ := managedEngine(t)
			build := work("build", "lead")
			build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
			var c Crew
			if tc.name == "setup" {
				c = repoCrew(dir, build)
				c.Setup = []Check{{ID: "deps", Argv: []string{"sh", "-c", "test ! -e broken.txt"}}}
			} else {
				commitManifestFile(t, dir, `{"schema": 1, "setup": [{"id": "deps", "argv": ["true"]}]}`)
				c = fileCrew(dir, build)
			}
			run := launchManaged(t, e, c)
			failed := spend(t, e, run.ID, "build", tc.file)
			a3 := failed.Attempt
			if a3.Stage != tc.stage {
				t.Fatalf("attempt 3 failed at %q", a3.Stage)
			}
			sessions := len(fl.launched())
			if rc, err := requestChanges(e, run.ID, "rq1", "build", 3, a3.Result, personsRequest("rv-0008")); err != nil || rc.Status != ledger.CommandDone {
				t.Fatalf("Request changes: %+v %v", rc, err)
			}
			b4 := attemptN(t, e, run.ID, "build", 4)
			if b4.Attempt.Start != a3.Start || b4.Attempt.SessionID == "" || len(fl.launched()) != sessions+1 {
				t.Fatalf("attempt 4 starts at %s (attempt 3 started at %s), session %q", short(b4.Attempt.Start), short(a3.Start), b4.Attempt.SessionID)
			}
			md, f := taskFileOf(t, b4)
			if rp := f.Repair; rp == nil || rp.Commit != a3.Result || rp.Start != a3.Start || rp.Attempt != 3 {
				t.Fatalf("the repair: %+v", f.Repair)
			}
			if p := f.Previous; p == nil || p.Attempt != 3 || p.Stage != tc.stage || p.Result != a3.Result || p.Branch != a3.Branch {
				t.Fatalf("the attempt before: %+v", f.Previous)
			}
			if f.StartFrom != "where attempt 3 started: its result, which Nate asked changes of, was refused before its checks" ||
				!strings.Contains(md, "That commit was refused before its checks, so this attempt starts where its attempt 3 started") ||
				!strings.Contains(md, "Its result, the commit "+a3.Result+" on the branch "+a3.Branch+", is not where you start") {
				t.Fatalf("attempt 4's task.md (starts from %q):\n%s", f.StartFrom, md)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// No text reaches a session but the task line every attempt gets: the
// repair attempt's session is typed its task line and nothing else, and
// another member's live session nothing at all.
func TestRequestChangesTypesNothingIntoALiveSession(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	d1 := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	_, docs := fl.attemptSession(t, d1)
	if got := waitTyped(t, docs, 10*time.Second); !strings.HasPrefix(got, taskLine(work("docs", "writer"), 1)) {
		t.Fatalf("docs' line: %q", got)
	}
	b1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	_, p1 := fl.attemptSession(t, b1)
	waitTyped(t, p1, 10*time.Second)
	commitResult(t, e, b1, "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit

	if rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, personsRequest("rv-0009")); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	b2 := attemptN(t, e, run.ID, "build", 2)
	_, p2 := fl.attemptSession(t, b2)
	if got, want := waitTyped(t, p2, 10*time.Second), taskLine(work("build", "lead"), 2); !strings.HasPrefix(got, want) || strings.Contains(got, "Nate") || strings.Contains(got, "f1") {
		t.Fatalf("attempt 2's session was typed %q, not its task line %q", got, want)
	}
	commitResult(t, e, b2, "built.txt", "built again\n", "built again")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2)
	if got := typed(docs); len(got) != 0 {
		t.Fatalf("docs' live session was typed %q", got)
	}
	if got := typed(p2); len(got) != 0 {
		t.Fatalf("attempt 2's session was typed more: %q", got)
	}
	if got := typed(p1); len(got) != 0 {
		t.Fatalf("attempt 1's session was typed %q", got)
	}
	stopRun(t, e, run.ID)
}

// A task a decision holds stays held: build's repair withdraws deploy,
// which a plan's gate holds, and once the repair's result stands deploy
// is ready and held again with its words, never dispatched; the gate is
// neither answered nor opened again by the request.
func TestRequestChangesLeavesAGatesHoldInPlace(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000c2", repoCrew(dir, work("build", "lead"), work("deploy", "ops", "build")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	held := func(ts TaskState) bool {
		return ts.State == ledger.TaskReady && ts.Reason == "waits for decision "+g.ID
	}
	waitTaskWhere(t, e, run.ID, "deploy", "ready and held", held)

	if rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, personsRequest("rv-0010")); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	commitResult(t, e, attemptN(t, e, run.ID, "build", 2), "built.txt", "built again\n", "built again")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2)
	if dep := waitTaskWhere(t, e, run.ID, "deploy", "ready and held again", held); dep.Attempts != 0 {
		t.Fatalf("deploy dispatched past its gate: %+v", dep)
	}
	if rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen)); rec.Rev != 1 {
		t.Fatalf("the gate after the request: %+v", rec.DecisionRow)
	}
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"deploy " + g.ID}) {
		t.Fatalf("holds after the request: %q", got)
	}
	stopRun(t, e, run.ID)
}

// A person's repair reopens the run through the path every reopen takes,
// and so asks the plan's gates again: build's spent attempts finish the
// run failed and cancel the gate that held deploy, which needs it; Request
// changes of build's failed candidate reopens the run (reopenRun's regate)
// and opens a fresh gate in its own change, so deploy, ready once build's
// repair succeeds, stays held with its words, no attempt of it dispatched,
// the run running; the operator's Approve of the fresh gate then releases
// it and it starts (docs/plans/decisions.md, Holding; I14). Modelled on
// TestRecheckLeavesAGatesHoldInPlace; it fails with the regate taken out
// of reopenRun or with reopenFor not reopening the run through it.
func TestRequestChangesAsksAGateAgain(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", mark)}
	run := launchGated(t, e, dir, "api-sweep-000000c3", repoCrew(dir, build, work("deploy", "ops", "build")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))

	if rc, err := requestChanges(e, run.ID, "rq1", "build", 3, c3, personsRequest("rv-0012")); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	fresh := gateOf(t, e, ld, run.ID)
	if fresh.ID == g.ID || fresh.Gate != "ship" || !strings.Contains(string(fresh.History[0].Detail), "build was reopened by Nate after "+g.ID+" was cancelled") {
		t.Fatalf("the fresh gate: %+v %s", fresh, fresh.History[0].Detail)
	}
	mustMark(t, mark)
	commitResult(t, e, attemptN(t, e, run.ID, "build", 4), "built.txt", "built again\n", "built again")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 4)
	dep := waitTaskWhere(t, e, run.ID, "deploy", "ready and held", func(ts TaskState) bool {
		return ts.State == ledger.TaskReady && ts.Reason == "waits for decision "+fresh.ID
	})
	if dep.Attempts != 0 {
		t.Fatalf("deploy dispatched past its gate: %+v", dep)
	}
	if r, _ := e.Get(run.ID); r.State != RunRunning || r.Outcome != "" {
		t.Fatalf("the run reopened with a held task left: %s/%q", r.State, r.Outcome)
	}
	if rec := decisionWhen(t, ld, fresh.ID, "open", inState(ledger.DecisionOpen)); rec.Rev != 1 {
		t.Fatalf("the fresh gate after the request: %+v", rec.DecisionRow)
	}
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"deploy " + fresh.ID}) {
		t.Fatalf("holds after the request: %q", got)
	}
	if ts := taskOf(t, e, run.ID, "deploy"); ts.Attempts != 0 {
		t.Fatalf("deploy dispatched past its gate: %+v", ts)
	}

	answer(t, e, fresh.ID, Response{ResponseID: "a1", Choice: choice(0), ExpectedRev: 1})
	if app := applied(t, decisionWhen(t, ld, fresh.ID, "applied", inState(ledger.DecisionApplied))); app.How != HowReleased || !slices.Equal(app.Tasks, []string{"deploy"}) {
		t.Fatalf("the application: %+v", app)
	}
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// A person's repair whose reopen would ask two gates again, in a run with
// room for one, is refused decision_limit with a receipt before anything
// is recorded, as a Retry and a Check again are: build stays failed with
// no new attempt and no repair, no gate opens, and the run stays failed.
func TestRequestChangesNeedsRoomForEveryGate(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "passes"))}
	smoke := PackageDecision{ID: "smoke", Question: "Smoke it?", Kind: ledger.DecisionApproval, Before: []string{"smoke"}}
	run := launchGated(t, e, dir, "api-sweep-000000c4", repoCrew(dir, build, work("deploy", "ops", "build"), work("smoke", "qa", "build")), shipGate("deploy"), smoke)
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	// The outcome is the engine's view; the gates' cancellations commit
	// after it, outside its lock: wait for the ledger to hold both final.
	before, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID})
	if err != nil || len(before) != 2 {
		t.Fatalf("the gates: %+v %v", before, err)
	}
	for _, g := range before {
		decisionWhen(t, ld, g.ID(), "final", func(rec ledger.DecisionRecord) bool { return rec.Final() })
	}
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	r.dec.opened = ledger.MaxDecisionsPerRun - 1
	e.mu.Unlock()

	rc, err := requestChanges(e, run.ID, "rq1", "build", 3, c3, personsRequest("rv-0013"))
	var de *DecisionError
	if !errors.As(err, &de) || de.Code != CodeDecisionLimit || !strings.Contains(de.Words, "ship, smoke") || !strings.Contains(de.Words, "the request for changes is refused") || rc.Status != ledger.CommandFailed {
		t.Fatalf("the request: %+v %v", rc, err)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskFailed || ts.Attempts != maxAttemptsPerTask {
		t.Fatalf("build after the refused request: %+v", ts)
	}
	if row := ledgerTask(t, ld, run.ID, "build"); row.Repair != "" {
		t.Fatalf("build's row after the refused request has a repair: %s", row.Repair)
	}
	if after, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID}); err != nil || len(after) != 2 {
		t.Fatalf("decisions after the refused request: %+v %v", after, err)
	}
	if got, _ := e.Get(run.ID); got.Outcome != ledger.OutcomeFailed {
		t.Fatalf("the run after the refused request: %s/%q", got.State, got.Outcome)
	}
	stopRun(t, e, run.ID)
}

// A repair attempt's result may say which of the person's changes it
// addressed, each by an id the request names; a review's result says
// none, and neither does a result with no person's request to answer.
func TestAddressedNamesOnlyTheRequestsChanges(t *testing.T) {
	for _, tc := range []struct {
		kind string
		a    []Addressed
		bad  string
	}{
		{TaskReview, []Addressed{{ID: "f1"}}, "a review's result addresses nothing"},
		{TaskWork, slices.Repeat([]Addressed{{ID: "f1"}}, 2), "each change once"},
		{TaskWork, []Addressed{{ID: ""}}, "each change once"},
		{TaskWork, []Addressed{{ID: "f1", Note: strings.Repeat("n", maxAddressNote+1)}}, "a note of at most 1000"},
		{TaskWork, make([]Addressed, maxAddressed+1), "more than 50"},
	} {
		res := Result{Addressed: tc.a}
		if tc.kind == TaskReview {
			res.Verdict = VerdictApproved
		}
		if err := res.validate(tc.kind); !errors.Is(err, ErrBadResult) || !strings.Contains(err.Error(), tc.bad) {
			t.Errorf("%s %d addressed: %v", tc.kind, len(tc.a), err)
		}
	}
	if err := (Result{Addressed: []Addressed{{ID: "f1", Note: strings.Repeat("n", maxAddressNote)}}}).validate(TaskWork); err != nil {
		t.Fatalf("a note at its bound: %v", err)
	}

	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	b1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(b1.Attempt.Worktree, "built.txt"), "built\n")
	commitAll(t, b1.Attempt.Worktree, "built")
	ctx := context.Background()
	if _, err := e.SubmitResult(ctx, b1.Attempt.SessionID, Result{Summary: "built", Addressed: []Addressed{{ID: "f1"}}}); !errors.Is(err, ErrBadResult) || !strings.Contains(err.Error(), "addressed names a change the request does not have") {
		t.Fatalf("addressed with no request: %v", err)
	}
	submit(t, e, b1.Attempt.SessionID, Result{Summary: "built"})
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	if rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, personsRequest("rv-0011")); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	b2 := attemptN(t, e, run.ID, "build", 2)
	writeFile(t, filepath.Join(b2.Attempt.Worktree, "built.txt"), "built: the parser\n")
	commitAll(t, b2.Attempt.Worktree, "named")
	if _, err := e.SubmitResult(ctx, b2.Attempt.SessionID, Result{Summary: "named", Addressed: []Addressed{{ID: "f1"}, {ID: "nope"}}}); !errors.Is(err, ErrBadResult) || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("addressed naming a change the request does not have: %v", err)
	}
	addressed := []Addressed{{ID: "f1", Note: "named the parser"}, {ID: "f2", Note: "yes, in the summary"}}
	submit(t, e, b2.Attempt.SessionID, Result{Summary: "named; the lexer is in", Addressed: addressed})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2)
	row := l.attemptWhen(t, run.ID, "build", 2, succeededRow)
	if got := decodeResult(row.Result); !reflect.DeepEqual(got.Addressed, addressed) {
		t.Fatalf("the attempt's row keeps %s", row.Result)
	}
	stopRun(t, e, run.ID)
}

// A task row written before a person's repair existed (a review task's
// repair) reads as it did and is written back byte for byte.
func TestOldRepairRowsReadAsBefore(t *testing.T) {
	old := `{"review":"check","attempt":2,"commit":"` + strings.Repeat("b", 40) + `","summary":"Two gaps.","findings":[{"severity":"major","path":"parse.go","line":40,"message":"An empty input panics."},{"severity":"nit","message":"Name the error."}]}`
	rp := decodeRepair(old)
	if rp == nil || rp.request != nil || rp.start != "" || rp.review != "check" || rp.attempt != 2 || len(rp.findings) != 2 {
		t.Fatalf("decoded: %+v", rp)
	}
	b, err := json.Marshal(rp.taskRepair())
	if err != nil || string(b) != old {
		t.Fatalf("written back:\n%s\nwas:\n%s", b, old)
	}
	f := goldenTaskFiles()["repair"]
	f.Repair = rp.taskRepair()
	md, _, _, err := fitTaskFile(f)
	if err != nil || !strings.Contains(string(md), "Review \"check\" (attempt 2) asked for changes to the commit") || strings.Contains(string(md), "addressed") {
		t.Fatalf("its task.md: %v\n%s", err, md)
	}
}

// The ledger's version 10 is a barrier: 9 to 10 changes no table and
// keeps every row, a person's repair among them; and a ledger of a version
// later than the binary's (as version 10 is to a binary before it),
// holding a person's repair, opens read-only, where Continue is refused by
// the barrier itself, and Retry and Request changes find no run to act on,
// with nothing written.
func TestLedgerVersionIsABarrier(t *testing.T) {
	dir := newRepo(t)
	ldir := t.TempDir()
	e, ld := sqlEngineIn(t, ldir)
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "passes"))}
	run := launchManaged(t, e, repoCrew(dir, build))
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	if rc, err := requestChanges(e, run.ID, "rq1", "build", 3, c3, personsRequest("rv-0012")); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	attemptN(t, e, run.ID, "build", 4)
	stopRun(t, e, run.ID)
	ctx := context.Background()
	rows := func(l *ledger.Ledger) string {
		t.Helper()
		row, _, err := l.Run(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		tasks, err := l.Tasks(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		attempts, err := l.Attempts(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		evs, err := l.Events(ctx, run.ID, 0, 10000)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal([]any{row, tasks, attempts, evs})
		return string(b)
	}
	ld.Close()
	closed, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	before := rows(closed)
	closed.Close()
	if !strings.Contains(before, `\"request\"`) {
		t.Fatal("the ledger holds no person's repair")
	}
	setVersion := func(v int) {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(ldir, ledger.File))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		// Version 9 had no previews table (version 11 added it).
		if v == 9 {
			if _, err := db.Exec("DROP TABLE previews"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec("UPDATE schema_version SET version = ?", v); err != nil {
			t.Fatal(err)
		}
	}
	version := func() int {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(ldir, ledger.File)+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var v int
		if err := db.QueryRow("SELECT version FROM schema_version").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	// 9 to 10, and on to Version: every row as it was.
	setVersion(9)
	migrated, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	if ro, why := migrated.ReadOnly(); ro {
		t.Fatalf("read-only after 9 to 10: %s", why)
	}
	if v := version(); v != ledger.Version || ledger.Version != 11 {
		t.Fatalf("version %d after the migration, Version %d", v, ledger.Version)
	}
	if got := rows(migrated); got != before {
		t.Fatalf("the rows after 9 to 10:\n%s\nwere:\n%s", got, before)
	}
	migrated.Close()

	// A later version's ledger: read-only, nothing taken.
	setVersion(ledger.Version + 1)
	later, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { later.Close() })
	if ro, why := later.ReadOnly(); !ro || !strings.Contains(why, "newer Conductor") {
		t.Fatalf("a later version's ledger: read-only %v: %s", ro, why)
	}
	e2, _ := readyEngine(t)
	e2.UseLedger(SQLLedger(later))
	// The barrier's own refusal: the ledger is read-only, written by a
	// newer Conductor, so no continuation is made.
	if _, err := e2.ContinueRun(ctx, run.ID, ""); !errors.Is(err, ErrNoLedger) || !strings.Contains(err.Error(), "written by a newer Conductor") {
		t.Fatalf("a run continued from a later version's ledger: %v", err)
	}
	// A server on a newer ledger holds no run to act on: Retry and Request
	// changes act only on a run the engine holds, which only a
	// continuation, refused above, could make.
	if err := e2.RetryTask(ctx, run.ID, "build"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("a task retried in a later version's ledger: %v", err)
	}
	if _, err := requestChanges(e2, run.ID, "rq2", "build", 3, c3, personsRequest("rv-0013")); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("changes requested in a later version's ledger: %v", err)
	}
	if got := rows(later); got != before {
		t.Fatalf("the rows after the refusals:\n%s\nwere:\n%s", got, before)
	}
	if v := version(); v != ledger.Version+1 {
		t.Fatalf("the later version's ledger is version %d now", v)
	}
}

// A request's shape is a review's batch: every rule of validate refuses
// with ErrBadChangeRequest, and each bound is accepted at its bound.
func TestChangeRequestValidate(t *testing.T) {
	exit := 1
	check := func() *RequestedCheck {
		return &RequestedCheck{Receipt: "r-1/build/2/check/test/1", Run: "r-1", Task: "build", Attempt: 2, Phase: ledger.PhaseCheck, ID: "test", K: 1, Subject: strings.Repeat("b", 40), Exit: &exit, Class: "code", Chosen: "code"}
	}
	withCheck := func(cr *ChangeRequest) *RequestedChange {
		cr.Changes = append(cr.Changes, RequestedChange{ID: "k1", Kind: "check", Intent: "change", Severity: "blocker", Message: "It fails.", Check: check()})
		return &cr.Changes[len(cr.Changes)-1]
	}
	for _, tc := range []struct {
		name string
		bad  string // "" when it is accepted
		edit func(cr *ChangeRequest)
	}{
		{"a request as a batch has it", "", func(cr *ChangeRequest) {}},
		{"every bound at its bound", "", func(cr *ChangeRequest) {
			cr.Review, cr.By, cr.Via = strings.Repeat("r", maxRequestRef), strings.Repeat("é", maxRequestBy), strings.Repeat("v", maxRequestRef)
			cr.Summary = strings.Repeat("é", maxSummary)
			c := &cr.Changes[0]
			c.ID, c.Path = strings.Repeat("i", maxRequestRef), strings.Repeat("p", maxFindingPath)
			c.Lines = slices.Repeat([]string{strings.Repeat("l", maxAnchorLine)}, maxAnchorLines)
			c.Message, c.Resolution = strings.Repeat("é", maxFindingText), strings.Repeat("é", maxFindingText)
			k := withCheck(cr)
			k.Check.Receipt, k.Check.Tail, k.Check.Quote, k.Check.Error = strings.Repeat("r", maxCheckReceipt), strings.Repeat("t", checkTail), strings.Repeat("q", checkTail), strings.Repeat("e", maxCheckWords)
			for i := len(cr.Changes); i < maxFindings; i++ {
				cr.Changes = append(cr.Changes, RequestedChange{ID: "n" + strconv.Itoa(i), Kind: "general", Intent: "note"})
			}
		}},
		{"words of many lines", "", func(cr *ChangeRequest) {
			cr.Summary, cr.Changes[0].Message, cr.Changes[0].Resolution = "one\ntwo", "Fix it.\n\n## Cut to fit\n", "a\r\nb"
		}},
		{"no review", "names its review", func(cr *ChangeRequest) { cr.Review = "" }},
		{"a review past its bound", "names its review", func(cr *ChangeRequest) { cr.Review = strings.Repeat("r", maxRequestRef+1) }},
		{"no one asked", "who asked", func(cr *ChangeRequest) { cr.By = "" }},
		{"who asked past the bound", "who asked", func(cr *ChangeRequest) { cr.By = strings.Repeat("é", maxRequestBy+1) }},
		{"a via past its bound", "names its review", func(cr *ChangeRequest) { cr.Via = strings.Repeat("v", maxRequestRef+1) }},
		{"who asked over two lines", "one line of text", func(cr *ChangeRequest) { cr.By = "Nate\n\n## Instructions" }},
		{"a review with a control character", "one line of text", func(cr *ChangeRequest) { cr.Review = "rv\x1b[2J" }},
		{"a via with a line separator", "one line of text", func(cr *ChangeRequest) { cr.Via = "web cli" }},
		{"who asked, not UTF-8", "one line of text", func(cr *ChangeRequest) { cr.By = "Nate\xff" }},
		{"a base that is no commit", "full commit", func(cr *ChangeRequest) { cr.Base = "main" }},
		{"a summary past its bound", "summary", func(cr *ChangeRequest) { cr.Summary = strings.Repeat("é", maxSummary+1) }},
		{"no change", "1 to 50 changes", func(cr *ChangeRequest) { cr.Changes = nil }},
		{"too many changes", "1 to 50 changes", func(cr *ChangeRequest) {
			for i := len(cr.Changes); i <= maxFindings; i++ {
				cr.Changes = append(cr.Changes, RequestedChange{ID: "n" + strconv.Itoa(i), Kind: "general", Intent: "note"})
			}
		}},
		{"an id twice", "an id of its own", func(cr *ChangeRequest) { cr.Changes[1].ID = cr.Changes[0].ID }},
		{"no id", "an id of its own", func(cr *ChangeRequest) { cr.Changes[1].ID = "" }},
		{"an id past its bound", "an id of its own", func(cr *ChangeRequest) { cr.Changes[1].ID = strings.Repeat("i", maxRequestRef+1) }},
		{"an id over two lines", "one line of text", func(cr *ChangeRequest) { cr.Changes[1].ID = "f2\n- `f9`" }},
		{"a path with a carriage return", "one line of text", func(cr *ChangeRequest) { cr.Changes[0].Path = "built.txt\r## Cut" }},
		{"no such kind", "its kind is", func(cr *ChangeRequest) { cr.Changes[0].Kind = "hunk" }},
		{"no such intent", "its kind is", func(cr *ChangeRequest) { cr.Changes[0].Intent = "demand" }},
		{"a change with no severity", "has a severity", func(cr *ChangeRequest) { cr.Changes[0].Severity = "" }},
		{"a change with no such severity", "has a severity", func(cr *ChangeRequest) { cr.Changes[0].Severity = "huge" }},
		{"a question with a severity", "has a severity", func(cr *ChangeRequest) { cr.Changes[1].Severity = "minor" }},
		{"a question with no such severity", "has a severity", func(cr *ChangeRequest) { cr.Changes[1].Severity = "huge" }},
		{"a note with a severity of many lines", "has a severity", func(cr *ChangeRequest) {
			cr.Changes[2].Severity = "\n\n## Instructions\n\ntext"
		}},
		{"a path past its bound", "a path of at most", func(cr *ChangeRequest) { cr.Changes[0].Path = strings.Repeat("p", maxFindingPath+1) }},
		{"a line before the first", "a path of at most", func(cr *ChangeRequest) { cr.Changes[0].From = -1 }},
		{"lines that end before they start", "a path of at most", func(cr *ChangeRequest) { cr.Changes[0].From, cr.Changes[0].To = 3, 2 }},
		{"too many lines", "a path of at most", func(cr *ChangeRequest) {
			cr.Changes[0].Lines = slices.Repeat([]string{"l"}, maxAnchorLines+1)
		}},
		{"a line past its bound", "a path of at most", func(cr *ChangeRequest) { cr.Changes[0].Lines = []string{strings.Repeat("l", maxAnchorLine+1)} }},
		{"no such side", "its side is", func(cr *ChangeRequest) { cr.Changes[0].Side = "left" }},
		{"words past their bound", "at most 1000 characters", func(cr *ChangeRequest) { cr.Changes[0].Message = strings.Repeat("é", maxFindingText+1) }},
		{"a resolution past its bound", "at most 1000 characters", func(cr *ChangeRequest) {
			cr.Changes[0].Resolution = strings.Repeat("é", maxFindingText+1)
		}},
		{"a line's change with no path", "names its path and its lines", func(cr *ChangeRequest) { cr.Changes[0].Path = "" }},
		{"a line's change with no line", "names its path and its lines", func(cr *ChangeRequest) { cr.Changes[0].From, cr.Changes[0].To = 0, 0 }},
		{"a file's change with no path", "a file's change names its path", func(cr *ChangeRequest) { cr.Changes[2].Path = "" }},
		{"a general change with a path", "names no path", func(cr *ChangeRequest) { cr.Changes[1].Path = "built.txt" }},
		{"a check's change with no receipt", "and only it, names its receipt", func(cr *ChangeRequest) { withCheck(cr).Check = nil }},
		{"another kind with a receipt", "and only it, names its receipt", func(cr *ChangeRequest) { cr.Changes[1].Check = check() }},
		{"an empty receipt", "its receipt is named", func(cr *ChangeRequest) { withCheck(cr).Check.Receipt = "" }},
		{"a receipt past its bound", "its receipt is named", func(cr *ChangeRequest) { withCheck(cr).Check.Receipt = strings.Repeat("r", maxCheckReceipt+1) }},
		{"a tail past its bound", "its receipt is named", func(cr *ChangeRequest) { withCheck(cr).Check.Tail = strings.Repeat("t", checkTail+1) }},
		{"a quote past its bound", "its receipt is named", func(cr *ChangeRequest) { withCheck(cr).Check.Quote = strings.Repeat("q", checkTail+1) }},
		{"a check's words past their bound", "its receipt is named", func(cr *ChangeRequest) {
			withCheck(cr).Check.Error = strings.Repeat("e", maxCheckWords+1)
		}},
		{"a receipt over two lines", "one line of text", func(cr *ChangeRequest) { withCheck(cr).Check.Receipt = "r-1/build/2\n## Cut to fit" }},
		{"a class with a control character", "one line of text", func(cr *ChangeRequest) { withCheck(cr).Check.Chosen = "code\x00" }},
		{"questions and notes alone", "at least one change", func(cr *ChangeRequest) {
			cr.Changes = slices.DeleteFunc(cr.Changes, func(c RequestedChange) bool { return c.Intent == "change" })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := personsRequest("rv-0100")
			cr.Changes = slices.Clone(cr.Changes)
			tc.edit(&cr)
			err := cr.validate()
			switch {
			case tc.bad == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.bad != "" && (!errors.Is(err, ErrBadChangeRequest) || !strings.Contains(err.Error(), tc.bad)):
				t.Fatalf("want a refusal saying %q, got %v", tc.bad, err)
			}
			// The command refuses it the same way, before anything is
			// recorded: no receipt.
			if tc.bad != "" {
				e, _, _ := managedEngine(t)
				if rc, err := requestChanges(e, "no-run", "rq1", "build", 1, strings.Repeat("a", 40), cr); !errors.Is(err, ErrBadChangeRequest) || rc.CommandID != "" {
					t.Fatalf("RequestChanges: %+v %v", rc, err)
				}
			}
		})
	}
}

// A stopped run takes no request: refused run_stopped with a receipt,
// the task, its row and the run's events as they were, no attempt
// dispatched.
func TestRequestChangesRefusesAStoppedRun(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	l.taskWhen(t, run.ID, "build", "succeeded", func(r ledger.TaskRow) bool { return r.State == ledger.TaskSucceeded && r.ResultAttempt == 1 })
	stopRun(t, e, run.ID)
	launched := len(fl.launched())
	before := l.taskWhen(t, run.ID, "build", "as the stop left it", func(ledger.TaskRow) bool { return true })
	events := len(l.kinds(run.ID))

	rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, personsRequest("rv-0101"))
	if !errors.Is(err, ErrRunStopped) || rc.Status != ledger.CommandFailed || rc.Refusal != RefusalRunStopped || !strings.Contains(err.Error(), "continue it, then request changes in its continuation") {
		t.Fatalf("a stopped run: %+v %v", rc, err)
	}
	if got, ok, err := l.Receipt(context.Background(), run.ID, "admin", "rq1"); err != nil || !ok || got.Status != ledger.CommandFailed {
		t.Fatalf("the receipt kept: %+v %v %v", got, ok, err)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskSucceeded || ts.Result == nil || ts.Result.Commit != c1 || ts.Attempts != 1 {
		t.Fatalf("the task after the refusal: %+v", ts)
	}
	l.mu.Lock()
	after := l.tasks[run.ID+"/build"]
	l.mu.Unlock()
	if !reflect.DeepEqual(after, before) || after.Repair != "" {
		t.Fatalf("the task's row after the refusal:\n%+v\nwas:\n%+v", after, before)
	}
	kinds := l.kinds(run.ID)
	for _, k := range kinds[events:] {
		if k != "command.accepted" && k != "command.request_changes" {
			t.Fatalf("an event besides the command's: %s (%q)", k, kinds[events:])
		}
	}
	if n := len(fl.launched()); n != launched {
		t.Fatalf("%d sessions launched after the refusal, %d before", n, launched)
	}
}

// A person's repair is durable before its attempt starts: the request is
// recorded for a candidate refused before its checks while the run is
// paused, the server ends, and the continuation starts exactly one repair
// attempt, where the refused attempt started (repair.start), with the
// request in its task.json and the attempt the request granted; the
// command's receipt is found again by its id.
func TestPersonsRepairSurvivesARestart(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
	c := repoCrew(dir, build)
	c.Setup = []Check{{ID: "deps", Argv: []string{"sh", "-c", "test ! -e broken.txt"}}}
	run := launchManaged(t, e, c)
	a3 := spend(t, e, run.ID, "build", "broken.txt").Attempt
	if a3.Stage != "setup deps" || a3.Start == a3.Result {
		t.Fatalf("attempt 3 failed at %q, from %s", a3.Stage, short(a3.Start))
	}
	ctx := context.Background()
	if err := e.PauseRun(ctx, run.ID, true); err != nil {
		t.Fatal(err)
	}
	cr := personsRequest("rv-0102")
	if rc, err := requestChanges(e, run.ID, "rq1", "build", 3, a3.Result, cr); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	l.taskWhen(t, run.ID, "build", "with the person's repair and its attempt", func(r ledger.TaskRow) bool {
		return strings.Contains(r.Repair, `"request"`) && strings.Contains(r.Repair, `"start":"`+a3.Start+`"`) && r.Extra == 1
	})
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != maxAttemptsPerTask || ts.State != ledger.TaskReady {
		t.Fatalf("the repair attempt dispatched in a paused run: %+v", ts)
	}
	// The server ends; another opens the ledger and continues the run.
	interrupt(l, run.ID, nil)
	e2, fl2 := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.PauseRun(ctx, next.ID, false); err != nil {
		t.Fatal(err)
	}
	b4 := attemptN(t, e2, next.ID, "build", maxAttemptsPerTask+1)
	if b4.Attempt.Start != a3.Start || b4.Attempt.SessionID == "" {
		t.Fatalf("the repair attempt starts at %s, the refused attempt started at %s", short(b4.Attempt.Start), short(a3.Start))
	}
	_, f := taskFileOf(t, b4)
	want := cr.clone()
	want.Summary = ""
	if rp := f.Repair; rp == nil || rp.Commit != a3.Result || rp.Start != a3.Start || rp.Attempt != 3 || rp.Review != "rv-0102" || rp.Summary != cr.Summary || !reflect.DeepEqual(rp.Request, want) {
		t.Fatalf("the repair attempt's task.json: %+v", f.Repair)
	}
	if f.MaxAttempts != maxAttemptsPerTask+1 {
		t.Fatalf("attempt %d of %d", f.Attempt, f.MaxAttempts)
	}
	commitResult(t, e2, b4, "fixed.txt", "fixed\n", "fixed")
	done := waitTask(t, e2, next.ID, "build", ledger.TaskSucceeded, maxAttemptsPerTask+1)
	if done.Attempts != maxAttemptsPerTask+1 || len(fl2.launched()) != 1 {
		t.Fatalf("after the repair: %d attempts, %d sessions launched", done.Attempts, len(fl2.launched()))
	}
	if got, ok, err := l.Receipt(ctx, run.ID, "admin", "rq1"); err != nil || !ok || got.Status != ledger.CommandDone || got.Intent != "request_changes" {
		t.Fatalf("the command's receipt after the restart: %+v %v %v", got, ok, err)
	}
	stopRun(t, e2, next.ID)
}

// A person's request on a task a review task's needs_changes left failed,
// its attempts spent, keeps the review's findings: the repair carries
// both, task.md has the person's changes first and the review's findings
// after them, and the row keeps the review's name. A request too large
// to keep beside those findings is refused with a receipt, nothing
// changed.
func TestRequestChangesKeepsAReviewsFindings(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "passed", Argv: []string{"sh", "-c", "test -e passed.txt"}}}
	run := launchManaged(t, e, repoCrew(dir, build, review("check", "critic", "build")))
	for n := 1; n < maxAttemptsPerTask; n++ {
		commitResult(t, e, attemptN(t, e, run.ID, "build", n), "built.txt", strings.Repeat("yes\n", n), "built")
	}
	commitResult(t, e, attemptN(t, e, run.ID, "build", maxAttemptsPerTask), "passed.txt", "yes\n", "passed")
	c3 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, maxAttemptsPerTask).Result.Commit
	// The review's findings: twenty of 1,000 characters, and one short.
	findings := []Finding{{Severity: "major", Path: "built.txt", Line: 1, Message: "Say why."}}
	for i := range 20 {
		findings = append(findings, Finding{Severity: "nit", Message: strconv.Itoa(i) + strings.Repeat("f", maxFindingText-2)})
	}
	rv := waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	submit(t, e, rv.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Summary: "Not yet.", Findings: findings})
	failed := waitTask(t, e, run.ID, "build", ledger.TaskFailed, maxAttemptsPerTask)
	if !strings.Contains(failed.Reason, "no attempt is left") {
		t.Fatalf("build after the review: %+v", failed)
	}
	l.taskWhen(t, run.ID, "build", "with the review's repair", func(r ledger.TaskRow) bool { return strings.Contains(r.Repair, `"review":"check"`) })

	// Too large beside the review's findings: refused, with a receipt.
	big := edgeRequest(t, ledger.MaxPayload-16<<10)
	if err := FitChangeRequest(repoCrew(dir, build, review("check", "critic", "build")), nil, "build", big); err != nil {
		t.Fatalf("the large request alone: %v", err)
	}
	rc, err := requestChanges(e, run.ID, "rq0", "build", maxAttemptsPerTask, c3, big)
	if !errors.Is(err, ErrChangeRequestTooLarge) || rc.Status != ledger.CommandFailed || !strings.Contains(err.Error(), "with the 21 findings of review check it keeps") {
		t.Fatalf("too large beside the findings: %+v %v", rc, err)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskFailed || ts.Attempts != maxAttemptsPerTask {
		t.Fatalf("build after the refusal: %+v", ts)
	}

	cr := personsRequest("rv-0103")
	if rc, err := requestChanges(e, run.ID, "rq1", "build", maxAttemptsPerTask, c3, cr); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes: %+v %v", rc, err)
	}
	b4 := attemptN(t, e, run.ID, "build", maxAttemptsPerTask+1)
	md, f := taskFileOf(t, b4)
	want := FindingsFrom{Review: "check", Attempt: 1, Commit: c3, Summary: "Not yet."}
	if rp := f.Repair; rp == nil || rp.Request == nil || rp.Review != "rv-0103" || rp.FindingsFrom == nil || *rp.FindingsFrom != want || !reflect.DeepEqual(rp.Findings, findings) {
		t.Fatalf("task.json's repair: %+v", f.Repair)
	}
	person := strings.Index(md, "- `f1` (major), `built.txt:1`, the candidate's side: Say what was built.")
	kept := strings.Index(md, "### The findings of review \"check\" (attempt 1), which this request keeps\n\nReview \"check\" asked for changes to the commit "+c3+" before this request, and they stand: address each of these too, or say in your summary why not; the review runs again on your result. Its summary: Not yet.\n\n")
	if person < 0 || kept < person || !strings.Contains(md[kept:], "- major built.txt:1: Say why.\n") || !strings.Contains(md[kept:], "- nit: 19f") {
		t.Fatalf("task.md:\n%s", md)
	}
	row := l.taskWhen(t, run.ID, "build", "with both", func(r ledger.TaskRow) bool { return strings.Contains(r.Repair, `"rv-0103"`) })
	if rp := decodeRepair(row.Repair); rp == nil || rp.request == nil || rp.from == nil || *rp.from != want || len(rp.findings) != len(findings) {
		t.Fatalf("the task's row keeps %s", row.Repair)
	}
	stopRun(t, e, run.ID)
}
