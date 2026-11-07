package crew

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

// continuing is the runs the fake ledger holds that continue runID, by id.
func (l *fakeLedger) continuing(runID string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for id, row := range l.runs {
		if row.ContinuedFrom == runID {
			out = append(out, id)
		}
	}
	return out
}

// commandRuns is the runs the fake ledger holds a command of the actor and
// id in.
func (l *fakeLedger) commandRuns(actor, id string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, row := range l.commands {
		if row.Actor == actor && row.ID == id {
			out = append(out, row.RunID)
		}
	}
	return out
}

// resumedIn is the runs the engine holds that continue runID, by id.
func resumedIn(e *Engine, runID string) []string {
	var out []string
	for _, r := range e.List() {
		if r.ResumedFrom == runID {
			out = append(out, r.ID)
		}
	}
	return out
}

// sqlContinuing is the runs the real ledger holds that continue runID.
func sqlContinuing(t *testing.T, ld *ledger.Ledger, runID string) []string {
	t.Helper()
	rows, err := ld.Runs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, row := range rows {
		if row.ContinuedFrom == runID {
			out = append(out, row.ID)
		}
	}
	return out
}

// mintedBy collects the ids the engine mints for its continuations
// (afterAdd), in order.
func mintedBy(e *Engine) func() []string {
	var mu sync.Mutex
	var ids []string
	e.afterAdd = func(id string) {
		mu.Lock()
		defer mu.Unlock()
		ids = append(ids, id)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(ids)
	}
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
	minted := mintedBy(e)

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
	// Nothing was made: no run continuing it, in the engine or the ledger
	// (not even one stopped: a refused continuation is discarded whole),
	// none of the ids minted for the refused ones, no mark on the run, no
	// command.
	if ids := minted(); len(ids) == 0 {
		t.Fatal("no continuation was minted for the refusals in the transaction")
	}
	for _, id := range minted() {
		if _, ok := e.Get(id); ok {
			t.Fatalf("the refused continuation %s is held", id)
		}
		l.mu.Lock()
		row, ok := l.runs[id]
		l.mu.Unlock()
		if ok {
			t.Fatalf("the refused continuation %s is in the ledger: %+v", id, row)
		}
	}
	if ids := resumedIn(e, run.ID); len(ids) != 0 {
		t.Fatalf("the engine holds continuations %v", ids)
	}
	if ids := l.continuing(run.ID); len(ids) != 0 {
		t.Fatalf("the ledger holds continuations %v", ids)
	}
	if ids := l.commandRuns("admin", "rq1"); len(ids) != 0 {
		t.Fatalf("the command is in %v", ids)
	}
	if r := l.runRowWhen(t, run.ID, "as it was", func(ledger.RunRow) bool { return true }); r.ContinuedBy != "" {
		t.Fatalf("the run is marked continued by %s", r.ContinuedBy)
	}
	e.afterAdd = nil

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

			// A failed candidate that does not stand: attempt 2, not the
			// source's last (t.priorLast). Nothing is made.
			if _, err := continueWithRepair(e, run.ID, "rq0", "build", 2, a3.Result, "", personsRequest("rv-0023")); !errors.Is(err, ErrStaleCandidate) {
				t.Fatalf("a failed candidate that does not stand: %v", err)
			}
			if got := l.continuing(run.ID); len(got) != 0 {
				t.Fatalf("the ledger holds continuations %v", got)
			}
			if got := resumedIn(e, run.ID); len(got) != 0 {
				t.Fatalf("the engine holds continuations %v", got)
			}

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

// A continuation carrying a valid repair whose first commit the ledger
// refuses once is discarded whole: no run continuing the source, in the
// engine or the ledger, not even a stopped one, no command, the source
// not marked; the same command sent again then makes the continuation,
// its receipt in the continuation's first transaction, and sent once more
// is that receipt.
func TestContinuationWhoseFirstCommitFailsIsDiscarded(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	commitResult(t, e, waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning), "docs.txt", "docs\n", "docs")
	runFinished(t, e, l, run.ID, ledger.OutcomeSucceeded)
	cr := personsRequest("rv-0024")
	minted := mintedBy(e)
	l.mu.Lock()
	l.failOn, l.failOnce = "run.continued", true
	l.mu.Unlock()

	if _, err := continueWithRepair(e, run.ID, "rq1", "build", 1, c1, "", cr); err == nil || !strings.Contains(err.Error(), "disk full (injected)") {
		t.Fatalf("the continuation whose first commit failed: %v", err)
	}
	ids := minted()
	if len(ids) != 1 {
		t.Fatalf("minted %v", ids)
	}
	if _, ok := e.Get(ids[0]); ok {
		t.Fatalf("the discarded continuation %s is held", ids[0])
	}
	l.mu.Lock()
	row, ok := l.runs[ids[0]]
	l.mu.Unlock()
	if ok {
		t.Fatalf("the discarded continuation is in the ledger: %+v", row)
	}
	if got := l.continuing(run.ID); len(got) != 0 {
		t.Fatalf("the ledger holds continuations %v", got)
	}
	if got := l.commandRuns("admin", "rq1"); len(got) != 0 {
		t.Fatalf("the command is in %v", got)
	}
	if r := l.runRowWhen(t, run.ID, "as it was", func(ledger.RunRow) bool { return true }); r.ContinuedBy != "" {
		t.Fatalf("the run is marked continued by %s", r.ContinuedBy)
	}
	if got, _ := e.Get(run.ID); got.Outcome != ledger.OutcomeSucceeded {
		t.Fatalf("the source after the discard: %s/%q", got.State, got.Outcome)
	}
	e.afterAdd = nil

	next, err := continueWithRepair(e, run.ID, "rq1", "build", 1, c1, "", cr)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == ids[0] {
		t.Fatalf("the continuation has the discarded one's id %s", next.ID)
	}
	if got := l.continuing(run.ID); !slices.Equal(got, []string{next.ID}) {
		t.Fatalf("the ledger's continuations %v, not %s", got, next.ID)
	}
	if row, found, _ := l.Receipt(context.Background(), next.ID, "admin", "rq1"); !found || row.Status != ledger.CommandDone {
		t.Fatalf("the receipt in the continuation: %+v %v", row, found)
	}
	again, err := e.RequestChanges(context.Background(), next.ID, "admin", Command{ID: "rq1"}, RepairRequest{Task: "build", Attempt: 1, Commit: c1, Imported: run.ID + "/build/1", Request: cr})
	if err != nil || !again.Repeat || again.Status != ledger.CommandDone {
		t.Fatalf("the command again: %+v %v", again, err)
	}
	if b2 := attemptN(t, e, next.ID, "build", 2); b2.Attempt.Start != c1 {
		t.Fatalf("attempt 2 starts at %s, not the candidate %s", short(b2.Attempt.Start), short(c1))
	}
	stopRun(t, e, next.ID)
}

// A continuation exists for anyone else only once its first transaction
// has committed (#623, review round 2): a Stop of the minted run before
// that (afterAdd) is refused as of no such run and unmakes it, with a
// stale candidate (the refusal that came after a Stop's committed row) or
// a standing one, so the ledger never holds the minted id, stopped or
// not; and no first transaction but the continuation's own writes it (a
// stop's recorded directly is refused). The source is as it was: the
// same request then makes the continuation, and the minted ids are still
// absent once the ledger holds it.
func TestContinuationStoppedBeforeItsFirstCommitIsNeverMade(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	commitResult(t, e, waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning), "docs.txt", "docs\n", "docs")
	runFinished(t, e, l, run.ID, ledger.OutcomeSucceeded)
	cr := personsRequest("rv-0028")

	var (
		mu      sync.Mutex
		minted  []string
		stopErr []error
	)
	e.afterAdd = func(id string) {
		err := e.Stop(context.Background(), id)
		mu.Lock()
		minted, stopErr = append(minted, id), append(stopErr, err)
		mu.Unlock()
	}
	for _, tc := range []struct {
		name, commit string
	}{
		{"a stale candidate", strings.Repeat("a", 40)},
		{"the standing candidate", c1},
	} {
		if _, err := continueWithRepair(e, run.ID, "rq1", "build", 1, tc.commit, "", cr); !errors.Is(err, ErrRunStopped) {
			t.Fatalf("%s, stopped before its first commit: %v", tc.name, err)
		}
	}
	// No first transaction but its own writes a continuation not made: a
	// stop recorded for one is refused, nothing written.
	e.afterAdd = func(id string) {
		err := e.recordStop(runOf(t, e, id))
		mu.Lock()
		minted, stopErr = append(minted, id), append(stopErr, err)
		mu.Unlock()
		if !errors.Is(err, ErrRunNotFound) {
			t.Errorf("a stop recorded for the continuation %s not made: %v", id, err)
		}
	}
	if _, err := continueWithRepair(e, run.ID, "rq1", "build", 1, strings.Repeat("a", 40), "", cr); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("a stale candidate, a stop recorded before its first commit: %v", err)
	}
	e.afterAdd = nil
	mu.Lock()
	ids, errs := slices.Clone(minted), slices.Clone(stopErr)
	mu.Unlock()
	if len(ids) != 3 {
		t.Fatalf("minted %v", ids)
	}
	for i, err := range errs[:2] {
		if !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("the Stop of %s before its first commit: %v", ids[i], err)
		}
	}
	absent := func(when string) {
		t.Helper()
		for _, id := range ids {
			if _, ok := e.Get(id); ok {
				t.Fatalf("%s: the continuation never made %s is held", when, id)
			}
			l.mu.Lock()
			row, ok := l.runs[id]
			l.mu.Unlock()
			if ok {
				t.Fatalf("%s: the continuation never made is in the ledger: %+v", when, row)
			}
		}
	}
	absent("refused")
	if got := l.continuing(run.ID); len(got) != 0 {
		t.Fatalf("the ledger holds continuations %v", got)
	}
	if got := l.commandRuns("admin", "rq1"); len(got) != 0 {
		t.Fatalf("the command is in %v", got)
	}
	if r := l.runRowWhen(t, run.ID, "as it was", func(ledger.RunRow) bool { return true }); r.ContinuedBy != "" {
		t.Fatalf("the run is marked continued by %s", r.ContinuedBy)
	}

	next, err := continueWithRepair(e, run.ID, "rq1", "build", 1, c1, "", cr)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ids, next.ID) {
		t.Fatalf("the continuation has an unmade one's id %s", next.ID)
	}
	l.runRowWhen(t, run.ID, "continued", func(r ledger.RunRow) bool { return r.ContinuedBy == next.ID })
	if row, found, _ := l.Receipt(context.Background(), next.ID, "admin", "rq1"); !found || row.Status != ledger.CommandDone {
		t.Fatalf("the receipt in the continuation: %+v %v", row, found)
	}
	absent("once the continuation is made")
	// Made, it is a run as any other: a Stop stops it.
	stopRun(t, e, next.ID)
}

// A continuation whose first transaction has committed is a run as any
// other, and its Stop interrupts the work under way before it waits for
// any transaction of the run (#623, review round 3): with docs' line held
// in the ledger (its transaction holding the run's transaction lock), the
// Stop cancels the run's context and ends its sessions, and the record
// then refused under the stop does not pause the run.
func TestAStopOfAMadeContinuationDoesNotWaitForItsTransactions(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	l.runRowWhen(t, run.ID, "continued", func(r ledger.RunRow) bool { return r.ContinuedBy == next.ID })
	build := waitTaskWhere(t, e, next.ID, "build", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	// docs' line, once typed, is recorded in a transaction that waits here.
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "attempt.prompted", make(chan struct{}, 1), make(chan struct{})
	l.mu.Unlock()
	released := false
	release := func() {
		if !released {
			released = true
			close(l.release)
		}
	}
	defer release()
	commitResult(t, e, build, "built.txt", "yes\n", "built")
	waitTask(t, e, next.ID, "build", ledger.TaskSucceeded, 2)
	select {
	case <-l.blocked:
	case <-time.After(60 * time.Second):
		t.Fatal("docs' line was not recorded within 60 s")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop(context.Background(), next.ID) }()
	// The bound only bounds a hang: the old code never cancels while the
	// line's transaction is held.
	deadline := time.Now().Add(60 * time.Second)
	for {
		e.mu.Lock()
		r := e.runs[next.ID]
		cancelled := r.stopping && r.ctx.Err() != nil
		e.mu.Unlock()
		if cancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Stop of the made continuation did not cancel it while its transaction was held")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	got, _ := e.Get(next.ID)
	if got.StoppedAt == nil || got.State != RunStopped || got.Paused {
		t.Fatalf("the continuation after its Stop: %+v", got)
	}
	l.runRowWhen(t, next.ID, "stopped", func(r ledger.RunRow) bool { return r.State == ledger.RunStopped && !r.Paused })
}

// firstRefused is the real ledger, which refuses the first transition of
// the run named by id, before it writes anything.
type firstRefused struct {
	sqlLedger
	mu sync.Mutex
	id string
}

func (l *firstRefused) refuse(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.id = id
}

func (l *firstRefused) Transition(ctx context.Context, runID string, fn func(LedgerTx) error) (int64, error) {
	l.mu.Lock()
	refused := l.id != "" && l.id == runID
	if refused {
		l.id = ""
	}
	l.mu.Unlock()
	if refused {
		return 0, errors.New("the ledger refused the commit (test)")
	}
	return l.sqlLedger.Transition(ctx, runID, fn)
}

// A repair continuation whose first commit fails leaves the engine's
// authority confirmations as they were (#623, review round 3): deploy,
// which needs build, waits on an identity's accepted answer when the run
// is stopped; its recheck answers while the continuation carrying a
// repair of build is held after its read (afterAdd), and the repair
// withdraws deploy, which cancels the decision in the continuation. The
// first commit fails, so the source's decision is still answered in the
// ledger: its confirmation is still the engine's, and a plain
// continuation then applies it.
func TestAFailedRepairContinuationKeepsTheConfirmation(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	rl := &firstRefused{sqlLedger: sqlLedger{l: ld}}
	e.UseLedger(rl)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("deploy", "ops", "build")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	dp, _, _ := attemptOn(t, e, fl, run.ID, "deploy")
	d := ask(t, e, dp.Attempt.SessionID, approval("q1", "Ship it?"))
	rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the identity's response: %+v %v", rc, err)
	}
	stopRun(t, e, run.ID)
	decisionWhen(t, ld, d.ID, "answered", inState(ledger.DecisionAnswered))
	n, _ := ledger.ParseDecisionID(d.ID)
	confirmed := func() int {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.confirms[confirmKey{n, rc.K}]
	}

	var once sync.Once
	e.afterAdd = func(id string) {
		once.Do(func() {
			e.ConfirmAuthority(d.ID, rc.K, true)
			rl.refuse(id)
		})
	}
	if _, err := continueWithRepair(e, run.ID, "rq1", "build", 1, c1, "", personsRequest("rv-0031")); err == nil || !strings.Contains(err.Error(), "refused the commit (test)") {
		t.Fatalf("the repair continuation whose first commit failed: %v", err)
	}
	e.afterAdd = nil
	if got := confirmed(); got != 1 {
		t.Fatalf("the confirmation after the failed first commit: %d", got)
	}
	if rec := decisionWhen(t, ld, d.ID, "answered", inState(ledger.DecisionAnswered)); rec.CurrentRun != run.ID {
		t.Fatalf("the decision is %s's", rec.CurrentRun)
	}

	next := continueRun(t, e, run.ID)
	rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.Run != next.ID || rec.CurrentRun != next.ID || kindCount(rec, ledger.HistoryApplied) != 1 {
		t.Fatalf("the application: %+v in %s", app, rec.CurrentRun)
	}
	waitAttempt(t, e, next.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, next.ID)
}

// A dependent whose result was being verified when the run stopped is
// given its attempt back when a continuation's repair withdraws it, as
// the dependent verifying in a run the engine holds is when Request
// changes withdraws it there: one more attempt each, every attempt so far
// taken back. The continuation is of a stopped run.
func TestContinuationRepairGivesBackAVerifyingDependentsAttempt(t *testing.T) {
	setup := func(t *testing.T, e *Engine, l *fakeLedger) (Run, string) {
		dir := newRepo(t)
		docs := work("docs", "writer", "build")
		// A check that passes only once its mark exists, never made: the
		// result stays verifying until it is withdrawn or the run stops.
		mark := filepath.Join(t.TempDir(), "never")
		docs.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "until test -e " + mark + "; do sleep 0.05; done"}}}
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), docs))
		commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
		c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
		d1 := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
		writeFile(t, filepath.Join(d1.Attempt.Worktree, "docs.txt"), "docs\n")
		commitAll(t, d1.Attempt.Worktree, "docs")
		submit(t, e, d1.Attempt.SessionID, Result{Summary: "docs"})
		l.attemptWhen(t, run.ID, "docs", 1, func(a ledger.AttemptRow) bool {
			return a.State == ledger.AttemptVerifying && a.ResultCommit != "" && a.Result != ""
		})
		return run, c1
	}
	cr := personsRequest("rv-0025")
	givenBack := func(r ledger.TaskRow) bool {
		return r.Extra == 1 && r.Withdrawn == 1 && r.State == ledger.TaskBlocked
	}

	// Held: Request changes of build while docs verifies.
	e, _, l := managedEngine(t)
	held, c1 := setup(t, e, l)
	if _, err := requestChanges(e, held.ID, "rq1", "build", 1, c1, cr); err != nil {
		t.Fatal(err)
	}
	h := l.taskWhen(t, held.ID, "docs", "given back", givenBack)
	stopRun(t, e, held.ID)

	// A continuation of the run stopped while docs verified: docs' attempt
	// was carried as a result to verify again.
	e2, _, l2 := managedEngine(t)
	stopped, c1 := setup(t, e2, l2)
	stopRun(t, e2, stopped.ID)
	l2.attemptWhen(t, stopped.ID, "docs", 1, func(a ledger.AttemptRow) bool {
		return a.State == ledger.AttemptStopped && strings.HasPrefix(a.Stage, "verifying") && a.ResultCommit != ""
	})
	next, err := continueWithRepair(e2, stopped.ID, "rq1", "build", 1, c1, "", cr)
	if err != nil {
		t.Fatal(err)
	}
	c := l2.taskWhen(t, next.ID, "docs", "given back", givenBack)
	if c.Extra != h.Extra || c.Withdrawn != h.Withdrawn || c.Attempts != h.Attempts {
		t.Fatalf("docs in the continuation: extra %d, withdrawn %d, attempts %d; held: %d, %d, %d", c.Extra, c.Withdrawn, c.Attempts, h.Extra, h.Withdrawn, h.Attempts)
	}
	if p := l2.payloads(next.ID, "attempt.withdrawn"); len(p) != 1 || p[0]["task"] != "docs" || p[0]["attempt"] != 1 || p[0]["needs"] != "build" {
		t.Fatalf("attempt.withdrawn in the continuation: %+v", p)
	}
	if p := l2.payloads(next.ID, "run.continued"); len(p) != 1 || p[0]["reverified"] != 0 {
		t.Fatalf("run.continued: %+v", p)
	}
	if b2 := attemptN(t, e2, next.ID, "build", 2); b2.Attempt.Start != c1 {
		t.Fatalf("attempt 2 starts at %s, not the candidate %s", short(b2.Attempt.Start), short(c1))
	}
	stopRun(t, e2, next.ID)
}

// A continuation carrying a repair of a run from before a restart (the
// engine holds it no more) on the real ledger: the candidate, build's
// result, not imported, nor docs which needs it; the command's row and
// settled receipt in the continuation's first transaction, found by its
// id; the command sent again to the continuation is that receipt.
func TestContinuationRepairOfARunFromBeforeARestart(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	commitResult(t, e, waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning), "docs.txt", "docs\n", "docs")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	waitFake(t, "the run's row finished", func() bool {
		row, ok, err := ld.Run(context.Background(), run.ID)
		return err == nil && ok && row.State == ledger.RunFinished
	})
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	if _, ok := e2.Get(run.ID); ok {
		t.Fatal("the engine after the restart holds the run")
	}
	cr := personsRequest("rv-0026")

	next, err := continueWithRepair(e2, run.ID, "rv.rv-0026.1", "build", 1, c1, "", cr)
	if err != nil {
		t.Fatal(err)
	}
	if next.ResumedFrom != run.ID {
		t.Fatalf("the continuation continues %q", next.ResumedFrom)
	}
	if got := sqlContinuing(t, ld2, run.ID); !slices.Equal(got, []string{next.ID}) {
		t.Fatalf("the ledger's continuations %v, not %s", got, next.ID)
	}
	row, found, err := ld2.Receipt(context.Background(), next.ID, "admin", "rv.rv-0026.1")
	if err != nil || !found || row.Status != ledger.CommandDone || row.Intent != intentRequestChanges {
		t.Fatalf("the command's row in the continuation: %+v %v %v", row, found, err)
	}
	if kept, err := priorReceipt(row, intentRequestChanges, fingerprint(intentRequestChanges, "build", "1", c1, "rv-0026")); err != nil || kept.Revision != 1 || kept.Task == nil || kept.Task.State != ledger.TaskReady {
		t.Fatalf("the receipt kept: %+v %v", kept, err)
	}
	if d := ledgerTask(t, ld2, next.ID, "docs"); d.State != ledger.TaskBlocked || d.ResultCommit != "" || d.ImportedFrom != "" {
		t.Fatalf("docs in the continuation: %+v", d)
	}
	if b := ledgerTask(t, ld2, next.ID, "build"); b.ResultCommit != "" || b.ImportedFrom != "" || !strings.Contains(b.Repair, "rv-0026") {
		t.Fatalf("build in the continuation: %+v", b)
	}
	again, err := e2.RequestChanges(context.Background(), next.ID, "admin", Command{ID: "rv.rv-0026.1"}, RepairRequest{Task: "build", Attempt: 1, Commit: c1, Imported: run.ID + "/build/1", Request: cr})
	if err != nil || !again.Repeat || again.Revision != 1 || again.Status != ledger.CommandDone {
		t.Fatalf("the command again: %+v %v", again, err)
	}
	if b2 := attemptN(t, e2, next.ID, "build", 2); b2.Attempt.Start != c1 {
		t.Fatalf("attempt 2 starts at %s, not the candidate %s", short(b2.Attempt.Start), short(c1))
	}
	stopRun(t, e2, next.ID)
}

// A continuation's repair whose reopen would ask two gates again, in a
// continuation with room for one, is refused decision_limit, as the
// request is in a run the engine holds: nothing made, no run continuing
// the source in the ledger and no mark on it.
func TestContinuationRepairNeedsRoomForEveryGate(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "passes"))}
	smoke := PackageDecision{ID: "smoke", Question: "Smoke it?", Kind: ledger.DecisionApproval, Before: []string{"smoke"}}
	run := launchGated(t, e, dir, "api-sweep-000000d1", repoCrew(dir, build, work("deploy", "ops", "build"), work("smoke", "qa", "build")), shipGate("deploy"), smoke)
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	before, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID})
	if err != nil || len(before) != 2 {
		t.Fatalf("the gates: %+v %v", before, err)
	}
	for _, g := range before {
		decisionWhen(t, ld, g.ID(), "final", func(rec ledger.DecisionRecord) bool { return rec.Final() })
	}
	// The continuation made with room for one decision more.
	var minted []string
	e.afterAdd = func(id string) {
		minted = append(minted, id)
		r := runOf(t, e, id)
		e.mu.Lock()
		r.dec.opened = ledger.MaxDecisionsPerRun - 1
		e.mu.Unlock()
	}

	_, err = continueWithRepair(e, run.ID, "rq1", "build", 3, c3, "", personsRequest("rv-0027"))
	var de *DecisionError
	if !errors.As(err, &de) || de.Code != CodeDecisionLimit || !strings.Contains(de.Words, "ship, smoke") || !strings.Contains(de.Words, "the request for changes is refused") {
		t.Fatalf("the request: %v", err)
	}
	e.afterAdd = nil
	if len(minted) != 1 {
		t.Fatalf("minted %v", minted)
	}
	if _, ok, err := ld.Run(context.Background(), minted[0]); err != nil || ok {
		t.Fatalf("the refused continuation %s in the ledger: %v %v", minted[0], ok, err)
	}
	if got := sqlContinuing(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("the ledger holds continuations %v", got)
	}
	if row, ok, err := ld.Run(context.Background(), run.ID); err != nil || !ok || row.ContinuedBy != "" {
		t.Fatalf("the run after the refusal: %+v %v %v", row, ok, err)
	}
	if got, _ := e.Get(run.ID); got.Outcome != ledger.OutcomeFailed {
		t.Fatalf("the run after the refused request: %s/%q", got.State, got.Outcome)
	}
}
