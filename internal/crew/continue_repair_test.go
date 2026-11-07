package crew

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The continuation that carries a person's repair (docs/plans/
// review-repair.md, RV1 PR-2c): Request changes of a run the engine no
// longer runs, made as the continuation of it.

// continueWithRepair is ContinueRunWith of runID carrying a person's
// request of the task's attempt n at commit, named by ref ("" for an
// attempt of runID), as admin with the command id.
func continueWithRepair(e *Engine, runID, id, taskID string, n int, commit, ref string, cr ChangeRequest) (*Run, error) {
	return e.ContinueRunWith(context.Background(), runID, "", ContinueOptions{
		Repair: &RepairRequest{Task: taskID, Attempt: n, Commit: commit, Imported: ref, Request: cr},
		Actor:  "admin", Command: Command{ID: id},
	})
}

// runFinished waits for the run to end with the outcome, in the engine and
// in its row.
func runFinished(t *testing.T, e *Engine, l *fakeLedger, runID, outcome string) {
	t.Helper()
	waitOutcome(t, e, runID, outcome)
	l.runRowWhen(t, runID, "finished", func(r ledger.RunRow) bool { return r.State == ledger.RunFinished })
}

// A finished run continued with a person's request of build's result:
// the candidate is not imported, nor is what needs it (the review that
// approved it, docs), while lint, which does not, is; build's repair
// attempt starts at the candidate with the request in its task file; and
// the command's row and its settled receipt are in the continuation's
// first transaction, so the command sent again to the continuation is its
// receipt, a repeat.
func TestContinuationCarriesAPersonsRepair(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("check", "critic", "build"), work("docs", "writer", "build"), work("lint", "linter")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	approves(t, e, waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning))
	commitResult(t, e, waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning), "docs.txt", "docs\n", "docs")
	commitResult(t, e, waitAttempt(t, e, run.ID, "lint", ledger.AttemptRunning), "lint.txt", "lint\n", "lint")
	runFinished(t, e, l, run.ID, ledger.OutcomeSucceeded)
	cr := personsRequest("rv-0021")

	next, err := continueWithRepair(e, run.ID, "rv.rv-0021.1", "build", 1, c1, "", cr)
	if err != nil {
		t.Fatal(err)
	}
	if next.ResumedFrom != run.ID {
		t.Fatalf("the continuation continues %q", next.ResumedFrom)
	}
	// The candidate's repair attempt, at the candidate, its request in the
	// task file.
	b2 := attemptN(t, e, next.ID, "build", 2)
	if b2.Attempt.Start != c1 {
		t.Fatalf("attempt 2 starts at %s, not the candidate %s", short(b2.Attempt.Start), short(c1))
	}
	md, f := taskFileOf(t, b2)
	if rp := f.Repair; rp == nil || rp.Request == nil || rp.Review != "rv-0021" || rp.Attempt != 1 || rp.Commit != c1 || rp.Start != "" || rp.Summary != cr.Summary {
		t.Fatalf("attempt 2's repair: %+v", f.Repair)
	}
	if f.StartFrom != "the commit Nate asked changes of, in review rv-0021" || !strings.Contains(md, "Nate asked for changes to the commit "+c1) {
		t.Fatalf("attempt 2's task.md (starts from %q):\n%s", f.StartFrom, md)
	}
	// What needs it is not imported; what does not is.
	for _, id := range []string{"check", "docs"} {
		if ts := taskOf(t, e, next.ID, id); ts.State != ledger.TaskBlocked || ts.Result != nil {
			t.Fatalf("%s in the continuation: %+v", id, ts)
		}
	}
	if ts := taskOf(t, e, next.ID, "lint"); ts.State != ledger.TaskSucceeded || ts.Result == nil || ts.Result.Attempt != 1 {
		t.Fatalf("lint in the continuation: %+v", ts)
	}
	if p := l.payloads(next.ID, "run.continued"); len(p) != 1 || p[0]["imported"] != 1 {
		t.Fatalf("run.continued: %+v", p)
	}
	// The command's row and its settled receipt, in the first transaction:
	// its events lead the continuation's, and the receipt is at its first
	// revision.
	kinds := l.kinds(next.ID)
	want := []string{"run.continued", "command.accepted", "task.reopened", "task.withdrawn", "task.withdrawn", "command.request_changes"}
	if len(kinds) < len(want) || !slices.Equal(kinds[:len(want)], want) {
		t.Fatalf("the continuation's events: %v", kinds)
	}
	if p := l.payloads(next.ID, "task.reopened"); len(p) != 1 || p[0]["review"] != "rv-0021" || p[0]["by"] != "Nate" || p[0]["imported"] != run.ID+"/build/1" {
		t.Fatalf("task.reopened: %+v", p)
	}
	row, found, err := l.Receipt(context.Background(), next.ID, "admin", "rv.rv-0021.1")
	if err != nil || !found || row.Status != ledger.CommandDone || row.Intent != intentRequestChanges {
		t.Fatalf("the command's row in the continuation: %+v %v %v", row, found, err)
	}
	kept, err := priorReceipt(row, intentRequestChanges, fingerprint(intentRequestChanges, "build", "1", c1, "rv-0021"))
	if err != nil || kept.Revision != 1 || kept.Task == nil || kept.Task.ID != "build" || kept.Task.State != ledger.TaskReady || kept.Task.Result != nil {
		t.Fatalf("the receipt kept: %+v %+v %v", kept, kept.Task, err)
	}
	if _, found, _ := l.Receipt(context.Background(), run.ID, "admin", "rv.rv-0021.1"); found {
		t.Fatal("the command is in the run continued too")
	}
	// The same command sent again, to the continuation: its receipt.
	again, err := e.RequestChanges(context.Background(), next.ID, "admin", Command{ID: "rv.rv-0021.1"}, RepairRequest{Task: "build", Attempt: 1, Commit: c1, Imported: run.ID + "/build/1", Request: cr})
	if err != nil || !again.Repeat || again.Revision != 1 || again.Status != ledger.CommandDone {
		t.Fatalf("the command again: %+v %v", again, err)
	}
	if n := l.count(next.ID, "task.reopened"); n != 1 {
		t.Fatalf("%d repairs after the command was sent again", n)
	}
	// And to the run continued: continued already.
	var resumed *RunResumedError
	if _, err := continueWithRepair(e, run.ID, "rv.rv-0021.1", "build", 1, c1, "", cr); !errors.As(err, &resumed) || resumed.By != next.ID {
		t.Fatalf("the run continued, continued again: %v", err)
	}
	// The repair's result: the review runs again on it, docs starts at it.
	commitResult(t, e, b2, "built.txt", "built: the parser\n", "built again")
	b := waitTask(t, e, next.ID, "build", ledger.TaskSucceeded, 2)
	if rv := attemptN(t, e, next.ID, "check", 2); rv.Attempt.Start != b.Result.Commit {
		t.Fatalf("the review runs again at %s, not the repair's result %s", short(rv.Attempt.Start), short(b.Result.Commit))
	}
	if d := attemptN(t, e, next.ID, "docs", 2); d.Attempt.Start != b.Result.Commit {
		t.Fatalf("docs' attempt 2 starts at %s, not the repair's result %s", short(d.Attempt.Start), short(b.Result.Commit))
	}
	stopRun(t, e, next.ID)
}

// A request whose candidate does not stand in the chain's history makes no
// continuation: a commit or an attempt that is not the result, a result
// named by the run that holds it by import rather than by the run that
// made it, a review task, a task the crew has not, a revision moved on,
// and a request the run could not keep; the run is then continued as if
// none had been asked, and the candidate, named by its reference, is
// repaired in a continuation of that continuation.
func TestContinuationRefusesAStaleCandidate(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("check", "critic", "build")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	approves(t, e, waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning))
	runFinished(t, e, l, run.ID, ledger.OutcomeSucceeded)
	cr := personsRequest("rv-0022")
	runs := len(e.List())

	other := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, task string
		n          int
		commit     string
		ref        string
		want       error
	}{
		{"another commit", "build", 1, other, "", ErrStaleCandidate},
		{"another attempt", "build", 2, c1, "", ErrStaleCandidate},
		{"another run's reference", "build", 1, c1, "crew-00000000/build/1", ErrStaleCandidate},
		{"a review task", "check", 1, c1, "", ErrTaskState},
		{"no such task", "deploy", 1, c1, "", ErrTaskNotFound},
	} {
		if _, err := continueWithRepair(e, run.ID, "rq1", tc.task, tc.n, tc.commit, tc.ref, cr); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, not %v", tc.name, err, tc.want)
		}
	}
	if _, err := e.ContinueRunWith(context.Background(), run.ID, "", ContinueOptions{
		Repair: &RepairRequest{Task: "build", Attempt: 1, Commit: c1, Request: cr}, Actor: "admin", Command: Command{ID: "rq1", ExpectedRevision: 1 << 40},
	}); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("a revision moved on: %v", err)
	}
	huge := personsRequest("rv-0022")
	huge.Summary = strings.Repeat("<", maxSummary)
	for i := range huge.Changes {
		huge.Changes[i].Message = strings.Repeat("<", maxFindingText)
	}
	for len(huge.Changes) < maxFindings {
		c := huge.Changes[0]
		c.ID = fmt.Sprintf("g%02d", len(huge.Changes))
		huge.Changes = append(huge.Changes, c)
	}
	if _, err := continueWithRepair(e, run.ID, "rq1", "build", 1, c1, "", huge); !errors.Is(err, ErrChangeRequestTooLarge) {
		t.Fatalf("a request too large: %v", err)
	}
	if _, err := e.ContinueRunWith(context.Background(), run.ID, "", ContinueOptions{Repair: &RepairRequest{Task: "build", Attempt: 1, Commit: c1, Request: cr}, Command: Command{ID: "rq1"}}); !errors.Is(err, ErrBadChangeRequest) {
		t.Fatalf("a request with no actor: %v", err)
	}
	if _, err := e.ContinueRunWith(context.Background(), run.ID, "", ContinueOptions{Repair: &RepairRequest{Task: "build", Attempt: 1, Commit: c1, Request: cr}, Actor: "admin"}); !errors.Is(err, ErrBadCommandID) {
		t.Fatalf("a request with no command id: %v", err)
	}
	// Nothing was made: no run, no mark on the run, no command.
	if n := len(e.List()); n != runs {
		t.Fatalf("%d runs after the refusals, were %d", n, runs)
	}
	if r := l.runRowWhen(t, run.ID, "as it was", func(ledger.RunRow) bool { return true }); r.ContinuedBy != "" {
		t.Fatalf("the run is marked continued by %s", r.ContinuedBy)
	}

	// Continued with no request, build's result is imported: named by the
	// continuation, it is not the candidate; named by its reference, it is.
	mid, err := e.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	runFinished(t, e, l, mid.ID, ledger.OutcomeSucceeded)
	if _, err := continueWithRepair(e, mid.ID, "rq2", "build", 1, c1, "", cr); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("the imported result named by the continuation: %v", err)
	}
	last, err := continueWithRepair(e, mid.ID, "rq2", "build", 1, c1, run.ID+"/build/1", cr)
	if err != nil {
		t.Fatalf("the imported result named by its reference: %v", err)
	}
	if b2 := attemptN(t, e, last.ID, "build", 2); b2.Attempt.Start != c1 {
		t.Fatalf("attempt 2 starts at %s, not the candidate %s", short(b2.Attempt.Start), short(c1))
	}
	if row, found, _ := l.Receipt(context.Background(), last.ID, "admin", "rq2"); !found || row.Status != ledger.CommandDone {
		t.Fatalf("the receipt in the continuation: %+v %v", row, found)
	}
	stopRun(t, e, last.ID)
}

// A finished run whose last attempt was refused before its checks (its
// setup, or its manifest, failed at its result) and whose attempts are
// spent, continued with a person's request of that candidate: the repair
// attempt is granted, starts where the refused attempt started (the
// source's last attempt as the continuation read it), its task file names
// the candidate's commit, branch and refusal beside the request, and its
// agent is launched.
func TestContinuationRepairOfARefusedCandidate(t *testing.T) {
	for _, tc := range []struct{ name, file, stage string }{
		{"setup", "broken.txt", "setup deps"},
		{"manifest", manifestFile, "manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, l := managedEngine(t)
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
			a3 := spend(t, e, run.ID, "build", tc.file).Attempt
			if a3.Stage != tc.stage {
				t.Fatalf("attempt 3 failed at %q", a3.Stage)
			}
			runFinished(t, e, l, run.ID, ledger.OutcomeFailed)
			sessions := len(fl.launched())

			next, err := continueWithRepair(e, run.ID, "rq1", "build", 3, a3.Result, "", personsRequest("rv-0023"))
			if err != nil {
				t.Fatal(err)
			}
			b4 := attemptN(t, e, next.ID, "build", 4)
			if b4.Attempt.Start != a3.Start || b4.Attempt.SessionID == "" || len(fl.launched()) != sessions+1 {
				t.Fatalf("attempt 4 starts at %s (attempt 3 started at %s), session %q", short(b4.Attempt.Start), short(a3.Start), b4.Attempt.SessionID)
			}
			md, f := taskFileOf(t, b4)
			if rp := f.Repair; rp == nil || rp.Request == nil || rp.Commit != a3.Result || rp.Start != a3.Start || rp.Attempt != 3 {
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
			l.taskWhen(t, next.ID, "build", "granted one more attempt", func(r ledger.TaskRow) bool { return r.Extra == 1 && strings.Contains(r.Repair, `"start"`) })
			if r, _ := e.Get(next.ID); r.State != RunRunning || r.Outcome != "" {
				t.Fatalf("the continuation: %s/%q", r.State, r.Outcome)
			}
			stopRun(t, e, next.ID)
		})
	}
}
