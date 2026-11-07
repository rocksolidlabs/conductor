package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// Durable decisions (docs/plans/decisions.md, DR1 PR-2): an attempt's
// decision and a plan's gates, the holds, the response and the
// application. The engine runs on the real ledger (decisionEngine), whose
// decision tables and bounds are what a server has; every test waits on the
// ledger's record for the state it needs (decisionWhen), never on a clock,
// and holds a step at a seam where an order must be made.

// decisionEngine is an engine with fake sessions over the real ledger, in a
// directory of the test's.
func decisionEngine(t *testing.T) (*Engine, *fakeLauncher, *ledger.Ledger) {
	t.Helper()
	e, fl := readyEngine(t)
	ld, err := ledger.Open(t.TempDir())
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

// workbench is the operator's authority: the workbench token's.
func workbench(e *Engine) Authority {
	return Authority{Grant: grantAdmin, Via: ViaWorkbench, Boot: e.Boot()}
}

// identity is an identity provider's admin principal's authority.
func identity(e *Engine, subject string) Authority {
	return Authority{Grant: grantAdmin, Via: ViaIdentity, Subject: subject, Boot: e.Boot()}
}

func choice(i int) *int { return &i }

// approval is an approval request of the attempt's HEAD.
func approval(requestID, question string) DecisionRequest {
	return DecisionRequest{RequestID: requestID, Type: ledger.DecisionApproval, Question: question,
		Options: []DecisionOption{{Label: "Approve"}, {Label: "Hold off", Tradeoffs: "slower"}}, Authorizes: "merging the schema change"}
}

// ask opens a decision of the session's attempt.
func ask(t *testing.T, e *Engine, sessionID string, req DecisionRequest) Decision {
	t.Helper()
	d, created, err := e.RequestDecision(t.Context(), sessionID, req)
	if err != nil || !created || d.State != ledger.DecisionOpen || d.Rev != 1 {
		t.Fatalf("the request: %+v created %v %v", d, created, err)
	}
	return d
}

// answer is the operator's response with the workbench token.
func answer(t *testing.T, e *Engine, id string, resp Response) ResponseReceipt {
	t.Helper()
	if resp.ByName == "" {
		resp.ByName = "Nate"
	}
	rc, err := e.RespondDecision(t.Context(), id, resp, "admin", workbench(e))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the response to %s: %+v %v", id, rc, err)
	}
	return rc
}

// decisionWhen waits for the ledger's record of decision id to satisfy ok,
// and returns it.
func decisionWhen(t *testing.T, ld *ledger.Ledger, id, what string, ok func(ledger.DecisionRecord) bool) ledger.DecisionRecord {
	t.Helper()
	n, valid := ledger.ParseDecisionID(id)
	if !valid {
		t.Fatalf("%q is no decision's id", id)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		rec, found, err := ld.Decision(context.Background(), n)
		if err != nil {
			t.Fatal(err)
		}
		if found && ok(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is not %s after 20 s: %+v", id, what, rec.DecisionRow)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func inState(state string) func(ledger.DecisionRecord) bool {
	return func(rec ledger.DecisionRecord) bool { return rec.State == state }
}

// historyKinds is the kinds of a record's history, in order.
func historyKinds(rec ledger.DecisionRecord) []string {
	var out []string
	for _, h := range rec.History {
		out = append(out, h.Kind)
	}
	return out
}

// applied is a record's application receipt.
func applied(t *testing.T, rec ledger.DecisionRecord) Application {
	t.Helper()
	var app Application
	if err := json.Unmarshal([]byte(rec.Applied), &app); err != nil {
		t.Fatalf("%s's application receipt %q: %v", rec.ID(), rec.Applied, err)
	}
	return app
}

// activeHolds is the run's holds not released, as "task dr-n".
func activeHolds(t *testing.T, ld *ledger.Ledger, runID string) []string {
	t.Helper()
	rows, err := ld.Holds(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range rows {
		if h.ReleasedAt.IsZero() {
			out = append(out, h.TaskID+" "+ledger.DecisionID(h.Decision))
		}
	}
	slices.Sort(out)
	return out
}

// mirrorOf waits until the session shows the question that mirrors
// decision id, and returns it.
func mirrorOf(t *testing.T, l *session.Local, id string) session.Attention {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		a := l.Info().Attention
		if a.State == session.AttentionNeedsInput && a.Decision == id {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session does not show %s's question: %+v", id, a)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lineWith waits for the next submission to p and fails unless it holds
// want.
func lineWith(t *testing.T, p *sessiontest.FakeProc, want string) string {
	t.Helper()
	got := waitTyped(t, p, 10*time.Second)
	if !strings.Contains(got, want) {
		t.Fatalf("typed %q, want a line with %q", got, want)
	}
	return got
}

// infoWhen waits until the session's info satisfies ok, and returns it:
// the session resolves a question after its Enter is written, which
// lineWith sees first.
func infoWhen(t *testing.T, l *session.Local, what string, ok func(session.Info) bool) session.Info {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		info := l.Info()
		if ok(info) {
			return info
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session is not %s: %+v %+v kept %q", what, info.Attention, info.LastAnswer, info.Kept)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// attemptOn is the task's running attempt, its session and process, the
// task line read.
func attemptOn(t *testing.T, e *Engine, fl *fakeLauncher, runID, taskID string) (TaskState, *session.Local, *sessiontest.FakeProc) {
	t.Helper()
	ts := waitAttempt(t, e, runID, taskID, ledger.AttemptRunning)
	l, p := fl.attemptSession(t, ts)
	lineWith(t, p, taskID)
	return ts, l, p
}

// eventOrder is the index of each kind's first event of the run.
func eventOrder(t *testing.T, ld *ledger.Ledger, runID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for i, k := range sqlKinds(t, ld, runID) {
		if _, ok := out[k]; !ok {
			out[k] = i
		}
	}
	return out
}

// gatedPlan is a planned launch of run runID at base whose package names
// the plan decisions ds.
func gatedPlan(t *testing.T, runID, base string, ds ...PackageDecision) Planned {
	t.Helper()
	var p Package
	if err := json.Unmarshal(testPackage(t, runID, base), &p); err != nil {
		t.Fatal(err)
	}
	p.Decisions = ds
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return Planned{RunID: runID, Base: base, Digest: packageDigest(raw), Package: raw}
}

// launchGated launches c as a planned run whose package names ds, its
// gates opened at the launch.
func launchGated(t *testing.T, e *Engine, dir, runID string, c Crew, ds ...PackageDecision) Run {
	t.Helper()
	return launchPlanned(t, e, c, gatedPlan(t, runID, head(t, dir), ds...))
}

// shipGate is an approval gate on the tasks before names.
func shipGate(before ...string) PackageDecision {
	return PackageDecision{ID: "ship", Question: "Ship to production?", Kind: ledger.DecisionApproval, Before: before}
}

// ledgerTask is the task's row as the ledger keeps it.
func ledgerTask(t *testing.T, ld *ledger.Ledger, runID, taskID string) ledger.TaskRow {
	t.Helper()
	rows, err := ld.Tasks(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == taskID {
			return row
		}
	}
	t.Fatalf("run %s has no task %s in the ledger", runID, taskID)
	return ledger.TaskRow{}
}

// gateOf is the run's gate decision, read through the ledger.
func gateOf(t *testing.T, e *Engine, ld *ledger.Ledger, runID string) Decision {
	t.Helper()
	rows, err := ld.Decisions(context.Background(), ledger.DecisionFilter{CurrentRun: runID})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Origin == ledger.OriginPlan && row.Final() == false {
			d, err := e.Decision(t.Context(), row.ID())
			if err != nil {
				t.Fatal(err)
			}
			return d
		}
	}
	t.Fatalf("run %s has no open gate: %+v", runID, rows)
	return Decision{}
}

// An attempt's decision: one transaction opens its row, its opened and
// routed history, the hold of its task and its event; the session's
// question mirrors it; the workbench token's answer is recorded, then
// applied once, typed into the attempt's live session answering the
// question, and the hold released.
func TestAnAgentsDecisionIsTypedIntoItsLiveAttempt(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Ship the schema change?"))
	if d.ID != "dr-1" || d.Origin != ledger.OriginAgent || d.Asker == nil || d.Asker.Task != "build" || d.Asker.Attempt != 1 || !d.Asker.Live {
		t.Fatalf("the decision: %+v %+v", d, d.Asker)
	}
	var req requestBody
	if err := json.Unmarshal(d.Request, &req); err != nil || req.Subject == nil || req.Subject.Kind != "commit" || req.Subject.Commit != head(t, b.Attempt.Worktree) {
		t.Fatalf("the request's subject: %s %v", d.Request, err)
	}
	rec := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen))
	if got := historyKinds(rec); !slices.Equal(got, []string{ledger.HistoryOpened, ledger.HistoryRouted}) {
		t.Fatalf("history %q", got)
	}
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"build dr-1"}) {
		t.Fatalf("holds %q", got)
	}
	q := mirrorOf(t, l, d.ID)

	rc := answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), Rationale: "the migration is reversible", ExpectedRev: 1})
	if rc.K != 1 || rc.Rev != 2 || rc.Decision != d.ID {
		t.Fatalf("the receipt: %+v", rc)
	}
	rec = decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowTyped || app.Task != "build" || app.Attempt != 1 || app.Response != 1 || !app.Validated {
		t.Fatalf("the application: %+v", app)
	}
	line := lineWith(t, p, "Decision dr-1 (approval) answered by Nate: Approve.")
	if !strings.Contains(line, "Why: the migration is reversible.") || !strings.Contains(line, "(receipt dr-1/1)") {
		t.Fatalf("the line: %q", line)
	}
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds after the application: %q", got)
	}
	infoWhen(t, l, "answered by Nate", func(info session.Info) bool {
		return info.Attention.ID != q.ID && info.LastAnswer != nil && info.LastAnswer.ByName == "Nate"
	})
	if got := sqlKinds(t, ld, run.ID); !slices.Contains(got, "decision.opened") || !slices.Contains(got, "decision.answered") || !slices.Contains(got, "decision.applied") {
		t.Fatalf("events %q", got)
	}
	stopRun(t, e, run.ID)
}

// A request id sent again answers the same decision, with nothing new; the
// same id with another body is decision_conflict. A file asked about is
// bound to its blob as committed at HEAD, and refused artifact_uncommitted
// while the worktree's file differs from it.
func TestARepeatedRequestIsTheSameDecisionAndAnArtifactIsItsBlob(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	sid := b.Attempt.SessionID
	req := DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"}
	first := ask(t, e, sid, req)
	again, created, err := e.RequestDecision(t.Context(), sid, req)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("the repeat: %+v created %v %v", again, created, err)
	}
	other := req
	other.Question = "Which index, really?"
	var de *DecisionError
	if _, _, err := e.RequestDecision(t.Context(), sid, other); !errors.As(err, &de) || de.Code != CodeDecisionConflict {
		t.Fatalf("another body under the same id: %v", err)
	}
	rows, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("decisions %+v %v", rows, err)
	}

	writeFile(t, filepath.Join(b.Attempt.Worktree, "schema.sql"), "create table t (id int);\n")
	commitAll(t, b.Attempt.Worktree, "the schema")
	art := DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Is the schema right?", Subject: &DecisionSubject{Kind: "artifact", Path: "schema.sql"}}
	writeFile(t, filepath.Join(b.Attempt.Worktree, "schema.sql"), "create table t (id bigint);\n")
	if _, _, err := e.RequestDecision(t.Context(), sid, art); !errors.As(err, &de) || de.Code != CodeArtifactUncommitted {
		t.Fatalf("a file not committed: %v", err)
	}
	commitAll(t, b.Attempt.Worktree, "bigint")
	d := ask(t, e, sid, art)
	var body requestBody
	if err := json.Unmarshal(d.Request, &body); err != nil {
		t.Fatal(err)
	}
	blob := runGit(t, b.Attempt.Worktree, "rev-parse", "HEAD:schema.sql")
	digest, err := blobDigest(t.Context(), b.Attempt.Worktree, "HEAD", "schema.sql")
	if err != nil || body.Subject.Kind != "artifact" || body.Subject.Path != "schema.sql" || body.Subject.Commit != head(t, b.Attempt.Worktree) || body.Subject.SHA256 != digest || strings.TrimSpace(blob) == "" {
		t.Fatalf("the artifact subject: %+v (%s) %v", body.Subject, digest, err)
	}

	// Sent again once the file changed (its reply lost): the same decision,
	// not a refusal of a subject checked again.
	writeFile(t, filepath.Join(b.Attempt.Worktree, "schema.sql"), "create table t (id text);\n")
	if again, created, err := e.RequestDecision(t.Context(), sid, art); err != nil || created || again.ID != d.ID {
		t.Fatalf("the artifact's request again, its file changed: %+v created %v %v", again, created, err)
	}
	// A change an index flag hides from git status is a change all the
	// same.
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		runGit(t, b.Attempt.Worktree, "update-index", flag, "schema.sql")
		if st := runGit(t, b.Attempt.Worktree, "status", "--porcelain"); strings.TrimSpace(st) != "" {
			t.Fatalf("%s: git status still shows the change: %q", flag, st)
		}
		hidden := DecisionRequest{RequestID: "q3" + flag, Type: ledger.DecisionClarification, Question: "Is the schema right now?", Subject: &DecisionSubject{Kind: "artifact", Path: "schema.sql"}}
		if _, _, err := e.RequestDecision(t.Context(), sid, hidden); !errors.As(err, &de) || de.Code != CodeArtifactUncommitted {
			t.Fatalf("a change hidden by %s: %v", flag, err)
		}
		runGit(t, b.Attempt.Worktree, "update-index", strings.Replace(flag, "--", "--no-", 1), "schema.sql")
	}
	stopRun(t, e, run.ID)
}

// A gate holds the tasks its plan decision names: build runs, deploy is
// ready and held with its words, no attempt of it, until the operator's
// Approve is applied (released); then it starts.
func TestAGateHoldsItsBeforeTasks(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000a1", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	if g.Gate != "ship" || g.Origin != ledger.OriginPlan || g.Ticket != testTicketID || g.Plan != testPlanID {
		t.Fatalf("the gate: %+v", g)
	}
	dep := waitTaskWhere(t, e, run.ID, "deploy", "held", func(ts TaskState) bool {
		return ts.State == ledger.TaskReady && ts.Reason == "waits for decision "+g.ID
	})
	if dep.Attempts != 0 {
		t.Fatalf("deploy dispatched while held: %+v", dep)
	}
	if row := ledgerTask(t, ld, run.ID, "deploy"); row.State != ledger.TaskReady || row.Reason != "waits for decision "+g.ID {
		t.Fatalf("deploy's row in the ledger: %+v", row)
	}
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"deploy " + g.ID}) {
		t.Fatalf("holds %q", got)
	}
	if order := eventOrder(t, ld, run.ID); order["run.created"] > order["decision.opened"] {
		t.Fatalf("decision.opened before run.created: %v", order)
	}
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowReleased || !slices.Equal(app.Tasks, []string{"deploy"}) {
		t.Fatalf("the application: %+v", app)
	}
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// While its only unfinished task is held, the run is not finished: build
// succeeds, deploy waits, the run has no outcome.
func TestTheRunWaitsOnAHeldTask(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000a2", repoCrew(dir, work("build", "lead"), work("deploy", "ops", "build")), shipGate("deploy"))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	commitResult(t, e, b, "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	g := gateOf(t, e, ld, run.ID)
	waitTaskWhere(t, e, run.ID, "deploy", "ready and held", func(ts TaskState) bool {
		return ts.State == ledger.TaskReady && ts.Reason == "waits for decision "+g.ID
	})
	if r, _ := e.Get(run.ID); r.Outcome != "" || r.State != RunRunning {
		t.Fatalf("the run with a held task left: %s/%q", r.State, r.Outcome)
	}
	if ts := taskOf(t, e, run.ID, "deploy"); ts.Attempts != 0 {
		t.Fatalf("deploy: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// Pause holds the dispatch: a gate answered while the run is paused is
// applied, and its released task waits for the unpause.
func TestPauseHoldsReleasedTasks(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000a3", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	// Its only task held, the run commits nothing more: the launch's own
	// transaction wrote the task's row naming its gate.
	if row := ledgerTask(t, ld, run.ID, "deploy"); row.State != ledger.TaskReady || row.Reason != "waits for decision "+g.ID {
		t.Fatalf("deploy's row in the ledger: %+v", row)
	}
	if err := e.PauseRun(t.Context(), run.ID, true); err != nil {
		t.Fatal(err)
	}
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	// The application's pump has run (RespondDecision applies before it
	// returns): paused, nothing is dispatched.
	if ts := taskOf(t, e, run.ID, "deploy"); ts.State != ledger.TaskReady || ts.Attempts != 0 || ts.Reason != "" {
		t.Fatalf("deploy while paused: %+v", ts)
	}
	if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// A gate's Reject cancels the tasks it held, with its words, once the
// decision is final; the run's other work goes on.
func TestARejectedGateCancelsItsTasks(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000a4", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(1), Rationale: "not this week", ExpectedRev: 1})
	rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowCancelled || !slices.Equal(app.Tasks, []string{"deploy"}) {
		t.Fatalf("the application: %+v", app)
	}
	dep := waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 0)
	if want := "rejected in " + g.ID + " by Nate: not this week"; dep.Reason != want || dep.Attempts != 0 {
		t.Fatalf("deploy: %+v, want the reason %q", dep, want)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskRunning {
		t.Fatalf("build after the rejection: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// A gate holding the run's last unfinished task, which has no attempt,
// rejected: the decision makes one transition (answered → applied) with
// one applied row and no cancelled one, its receipt naming the task; the
// task is cancelled and the run given its outcome in the same transaction,
// after the decision: decision.applied, task.cancelled, run.finished once.
func TestRejectingTheLastHeldTaskEndsTheRunOnce(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000a5", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1})
	rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	if got := historyKinds(rec); !slices.Equal(got, []string{ledger.HistoryOpened, ledger.HistoryRouted, ledger.HistoryResponded, ledger.HistoryApplied}) {
		t.Fatalf("history %q", got)
	}
	if app := applied(t, rec); app.How != HowCancelled || !slices.Equal(app.Tasks, []string{"deploy"}) {
		t.Fatalf("the application: %+v", app)
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeCancelled)
	kinds := sqlKinds(t, ld, run.ID)
	finished := 0
	for _, k := range kinds {
		if k == "run.finished" {
			finished++
		}
	}
	order := eventOrder(t, ld, run.ID)
	if finished != 1 || !(order["decision.applied"] < order["task.cancelled"] && order["task.cancelled"] < order["run.finished"]) {
		t.Fatalf("events %q", kinds)
	}
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds %q", got)
	}
}

// A result while the attempt's decision waits is refused decision_open,
// the attempt running on: open, it is not answered yet; answered (an
// identity's answer, recorded while its recheck has not said yes), its
// answer is not applied yet, and nothing tells the agent to go on with it
// (#637, review round 2); once the decision is applied the result is
// taken.
func TestAResultWhileADecisionWaitsIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	// Committed before the ask: an approval of HEAD is superseded by a
	// commit after it (S4).
	writeFile(t, filepath.Join(b.Attempt.Worktree, "built.txt"), "b\n")
	commitAll(t, b.Attempt.Worktree, "built")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	var de *DecisionError
	if _, err := e.SubmitResult(t.Context(), b.Attempt.SessionID, Result{Summary: "built"}); !errors.As(err, &de) || de.Code != CodeDecisionOpen || !strings.Contains(de.Words, d.ID+" is not answered yet") {
		t.Fatalf("a result while %s is open: %v", d.ID, err)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("the attempt after the refusal: %+v", ts.Attempt)
	}
	rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" || !strings.HasPrefix(rc.Words, "Recorded: ") {
		t.Fatalf("the identity's answer: %+v %v", rc, err)
	}
	decisionWhen(t, ld, d.ID, "answered", inState(ledger.DecisionAnswered))
	_, err = e.SubmitResult(t.Context(), b.Attempt.SessionID, Result{Summary: "built"})
	if !errors.As(err, &de) || de.Code != CodeDecisionOpen || !strings.Contains(de.Words, d.ID+" is answered and its answer is not applied yet") || strings.Contains(de.Words, "not answered") {
		t.Fatalf("a result while %s is answered: %v", d.ID, err)
	}
	e.ConfirmAuthority(d.ID, rc.K, true)
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	lineWith(t, p, "Decision "+d.ID)
	submit(t, e, b.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
}

// A done without a result while the decision waits types nothing and
// leaves the reminder unspent; one after the application is reminded.
func TestNoReminderWhileADecisionWaits(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	mirrorOf(t, l, d.ID)
	done := func() {
		e.OnActivity(b.Attempt.SessionID, session.ActivityEntry{Type: session.ActivityAttention, Message: "done", At: time.Now().UTC()}, session.AttentionDone)
	}
	done()
	e.mu.Lock()
	r := e.runs[run.ID]
	a := r.task("build").active()
	reminded := a.reminded
	e.mu.Unlock()
	if reminded {
		t.Fatal("the reminder was spent while the decision waits")
	}
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	// The next line typed is the answer: no reminder before it.
	lineWith(t, p, "Decision "+d.ID)
	done()
	lineWith(t, p, "does not finish task")
	stopRun(t, e, run.ID)
}

// The reply comes before the mirror: a seam holds the session's ask, and
// the request has its reply, the decision open in the ledger, with no
// question shown; let go, the question shows.
func TestTheReplyComesBeforeTheMirror(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := decisionEngine(t)
	at, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.beforeMirror = func(int64) { once.Do(func() { close(at) }); <-gate }
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	<-at
	if a := l.Info().Attention; a.Decision != "" {
		t.Fatalf("a question shows before the ask was let go: %+v", a)
	}
	close(gate)
	mirrorOf(t, l, d.ID)
	stopRun(t, e, run.ID)
}

// A mirror asked late never outlives its decision: the ask held at the
// seam (the decision read as open) while the decision is answered and
// applied: let go, the question asked is withdrawn at once, the answer
// typed once as a line naming its decision, and no question shows. The
// same while a superseding request takes the decision's place: its
// question never stays, the new decision's does; and while the operator
// cancels it: no question stays.
func TestALateMirrorNeverOutlivesItsAnswer(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		at, gate := make(chan struct{}), make(chan struct{})
		var once sync.Once
		e.beforeMirror = func(int64) { once.Do(func() { close(at); <-gate }) }
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
		b, l, _ := attemptOn(t, e, fl, run.ID, "build")
		d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
		<-at
		if _, err := e.CancelDecision(t.Context(), d.ID, "c1", 0, false, "not needed", "admin"); err != nil {
			t.Fatal(err)
		}
		decisionWhen(t, ld, d.ID, "cancelled", inState(ledger.DecisionCancelled))
		r := runOf(t, e, run.ID)
		e.mu.Lock()
		a := r.task("build").active()
		e.mu.Unlock()
		close(gate)
		// The ask holds the attempt's askMu to its end, the withdrawal of
		// a question asked too late among it.
		a.askMu.Lock()
		info := l.Info()
		a.askMu.Unlock()
		if info.Attention.Decision != "" {
			t.Fatalf("a cancelled decision's question stays: %+v", info.Attention)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("answered", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		at, gate := make(chan struct{}), make(chan struct{})
		var once sync.Once
		e.beforeMirror = func(int64) { once.Do(func() { close(at); <-gate }) }
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
		b, l, p := attemptOn(t, e, fl, run.ID, "build")
		d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
		<-at
		answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
		decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
		close(gate)
		lineWith(t, p, "Decision "+d.ID)
		deadline := time.Now().Add(10 * time.Second)
		for {
			e.mu.Lock()
			asked := e.runs[run.ID].task("build").active()
			e.mu.Unlock()
			info := l.Info()
			if info.Attention.Decision == "" && len(info.Kept) == 0 && asked != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("a question outlives its answer: %+v kept %q", info.Attention, info.Kept)
			}
			time.Sleep(5 * time.Millisecond)
		}
		// The answer was typed once: the next line is a known one.
		if _, err := l.Submit(t.Context(), session.Submission{Text: "marker", ByName: "test"}); err != nil {
			t.Fatal(err)
		}
		lineWith(t, p, "marker")
		stopRun(t, e, run.ID)
	})
	t.Run("superseded", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		at, gate := make(chan struct{}), make(chan struct{})
		e.beforeMirror = func(n int64) {
			if n == 1 {
				close(at)
				<-gate
			}
		}
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
		b, l, _ := attemptOn(t, e, fl, run.ID, "build")
		d1 := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"})
		<-at
		d2 := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Which index, given the load?", Supersedes: d1.ID})
		decisionWhen(t, ld, d1.ID, "superseded", inState(ledger.DecisionSuperseded))
		close(gate)
		mirrorOf(t, l, d2.ID)
		info := l.Info()
		e.mu.Lock()
		kept := slices.Clone(info.Kept)
		e.mu.Unlock()
		if len(kept) != 0 {
			t.Fatalf("a superseded decision's question is kept: %q", kept)
		}
		stopRun(t, e, run.ID)
	})
}

// A request that supersedes the open decision of its task takes its place:
// dr-1 superseded by dr-2 with its words and history, its hold released
// and dr-2's active, the session's question dr-2's; a response to dr-1 is
// refused with why.
func TestASupersedingRequestMovesTheHold(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d1 := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"})
	mirrorOf(t, l, d1.ID)
	d2 := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Which index, given the load?", Supersedes: d1.ID})
	rec := decisionWhen(t, ld, d1.ID, "superseded", inState(ledger.DecisionSuperseded))
	if rec.SupersededBy != d2.ID || !strings.Contains(rec.Why, d2.ID) || !slices.Contains(historyKinds(rec), ledger.HistorySuperseded) || rec.Rev != 2 {
		t.Fatalf("%s: %+v %q", d1.ID, rec.DecisionRow, historyKinds(rec))
	}
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"build " + d2.ID}) {
		t.Fatalf("holds %q", got)
	}
	mirrorOf(t, l, d2.ID)
	// The superseding request sent again (its reply lost): dr-2, though
	// dr-1 is no longer open.
	if again, created, err := e.RequestDecision(t.Context(), b.Attempt.SessionID, DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Which index, given the load?", Supersedes: d1.ID}); err != nil || created || again.ID != d2.ID {
		t.Fatalf("the superseding request again: %+v created %v %v", again, created, err)
	}
	var de *DecisionError
	rc, err := e.RespondDecision(t.Context(), d1.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 2}, "admin", workbench(e))
	if !errors.As(err, &de) || de.Code != CodeDecisionClosed || rc.Status != "refused" || !strings.Contains(rc.Words, d2.ID) {
		t.Fatalf("a response to the superseded one: %+v %v", rc, err)
	}
	stopRun(t, e, run.ID)
}

// A handoff queued while the mirror shows waits; the decision's answer is
// typed first, answering the question, and the handoff after it.
func TestADecisionAnswerPassesAQueuedHandoff(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	w, _, _ := attemptOn(t, e, fl, run.ID, "docs")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	mirrorOf(t, l, d.ID)
	if _, err := e.SendMessage(t.Context(), w.Attempt.SessionID, "lead", "the docs are in"); err != nil {
		t.Fatal(err)
	}
	waitLogged(t, e, run.ID, "handoff queued from writer to lead", 1, 10*time.Second)
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	lineWith(t, p, "Decision "+d.ID)
	lineWith(t, p, "the docs are in")
	infoWhen(t, l, "answered by the decision's line", func(info session.Info) bool {
		return info.LastAnswer != nil && info.LastAnswer.ByName == "Nate"
	})
	stopRun(t, e, run.ID)
}

// Two answers of one attempt queued before its deliverer runs (dr-2's
// question shown, dr-1's taken by it), in each order, with a handoff
// queued between them: dr-2's answers its question, dr-1's is typed as a
// line naming its decision, each once, and the handoff after both.
func TestTwoAnswersQueuedBeforeDeliveryAreBothTyped(t *testing.T) {
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			dir := newRepo(t)
			e, fl, ld := decisionEngine(t)
			hold, held := make(chan struct{}), make(chan struct{})
			var once sync.Once
			e.beforeAnswer = func(member string) {
				if member == "lead" {
					once.Do(func() { close(held); <-hold })
				}
			}
			run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
			b, l, p := attemptOn(t, e, fl, run.ID, "build")
			w, _, _ := attemptOn(t, e, fl, run.ID, "docs")
			ds := []Decision{
				ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"}),
			}
			mirrorOf(t, l, ds[0].ID)
			ds = append(ds, ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Which cache?"}))
			mirrorOf(t, l, ds[1].ID)
			first := ds[order[0]]
			answer(t, e, first.ID, Response{ResponseID: "r1", Text: "the first answer", ExpectedRev: 1})
			<-held
			if _, err := e.SendMessage(t.Context(), w.Attempt.SessionID, "lead", "the docs are in"); err != nil {
				t.Fatal(err)
			}
			second := ds[order[1]]
			answer(t, e, second.ID, Response{ResponseID: "r1", Text: "the second answer", ExpectedRev: 1})
			for _, d := range ds {
				decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
			}
			close(hold)
			got := []string{waitTyped(t, p, 10*time.Second), waitTyped(t, p, 10*time.Second), waitTyped(t, p, 10*time.Second)}
			if !strings.Contains(got[0], "Decision "+ds[1].ID) || !strings.Contains(got[1], "Decision "+ds[0].ID) || !strings.Contains(got[2], "the docs are in") {
				t.Fatalf("typed %q", got)
			}
			infoWhen(t, l, "dr-2's question answered", func(info session.Info) bool {
				return info.LastAnswer != nil && info.LastAnswer.Message == "Which cache?" && info.Attention.Decision == ""
			})
			stopRun(t, e, run.ID)
		})
	}
}

// A response is applied once: application passes after it apply nothing,
// the response sent again gets its receipt (repeat) and changes nothing,
// and one line was typed.
func TestAResponseIsAppliedOnce(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	resp := Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"}
	first := answer(t, e, d.ID, resp)
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	lineWith(t, p, "Decision "+d.ID)
	e.mu.Lock()
	r := e.runs[run.ID]
	e.mu.Unlock()
	e.applyDecisions(r)
	e.applyDecisions(r)
	again, err := e.RespondDecision(t.Context(), d.ID, resp, "admin", workbench(e))
	if err != nil || !again.Repeat || again.K != first.K || again.Status != "accepted" {
		t.Fatalf("the response again: %+v %v", again, err)
	}
	rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	n := 0
	for _, k := range historyKinds(rec) {
		if k == ledger.HistoryApplied {
			n++
		}
	}
	if n != 1 || len(rec.Responses) != 1 || rec.Rev != 3 {
		t.Fatalf("%d applied rows, %d responses, rev %d", n, len(rec.Responses), rec.Rev)
	}
	if _, err := l.Submit(t.Context(), session.Submission{Text: "marker", ByName: "test"}); err != nil {
		t.Fatal(err)
	}
	lineWith(t, p, "marker")
	stopRun(t, e, run.ID)
}

// An identity's response is applied only once the provider admitted its
// token again after it committed (ConfirmAuthority): until then nothing is
// applied, the run going on; not admitted, the decision is superseded by a
// fresh one with its holds, which the workbench token's answer releases;
// admitted, it is applied.
func TestARevokedIdentityIsNotApplied(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000a6", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	rc, err := e.RespondDecision(t.Context(), g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the identity's response: %+v %v", rc, err)
	}
	// Not confirmed while the run goes on: nothing applied.
	e.mu.Lock()
	r := e.runs[run.ID]
	e.mu.Unlock()
	e.applyDecisions(r)
	if rec := decisionWhen(t, ld, g.ID, "answered", inState(ledger.DecisionAnswered)); rec.Applied != "" {
		t.Fatalf("applied before its recheck: %+v", rec.DecisionRow)
	}
	if ts := taskOf(t, e, run.ID, "deploy"); ts.Attempts != 0 {
		t.Fatalf("deploy released before the recheck: %+v", ts)
	}
	e.ConfirmAuthority(g.ID, rc.K, false)
	old := decisionWhen(t, ld, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	if old.Applied != "" || old.SupersededBy == "" || !strings.Contains(old.Why, "user:nate's answer could not be rechecked") {
		t.Fatalf("the revoked answer's decision: %+v", old.DecisionRow)
	}
	fresh := decisionWhen(t, ld, old.SupersededBy, "open", inState(ledger.DecisionOpen))
	if fresh.Gate != "ship" || fresh.Request != old.Request {
		t.Fatalf("the fresh decision: %+v", fresh.DecisionRow)
	}
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"deploy " + fresh.ID()}) {
		t.Fatalf("holds %q", got)
	}
	if ts := taskOf(t, e, run.ID, "deploy"); ts.Attempts != 0 || ts.Reason != "waits for decision "+fresh.ID() {
		t.Fatalf("deploy: %+v", ts)
	}
	if row := ledgerTask(t, ld, run.ID, "deploy"); row.Reason != "waits for decision "+fresh.ID() {
		t.Fatalf("deploy's row in the ledger: %+v", row)
	}
	answer(t, e, fresh.ID(), Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, fresh.ID(), "applied", inState(ledger.DecisionApplied))
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)

	// Admitted again: applied. The process that accepted it is the
	// engine's, whatever the caller said (no Boot here).
	run2 := launchGated(t, e, dir, "api-sweep-000000a7", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g2 := gateOf(t, e, ld, run2.ID)
	rc, err = e.RespondDecision(t.Context(), g2.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", Authority{Grant: grantAdmin, Via: ViaIdentity, Subject: "nate"})
	if err != nil {
		t.Fatal(err)
	}
	e.ConfirmAuthority(g2.ID, rc.K, true)
	decisionWhen(t, ld, g2.ID, "applied", inState(ledger.DecisionApplied))
	waitAttempt(t, e, run2.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run2.ID)
}

// fillDecisions writes n final decisions of the run straight into the
// ledger, as many earlier requests would have left them.
func fillDecisions(t *testing.T, ld *ledger.Ledger, runID string, n int) {
	t.Helper()
	body := `{"question":"Earlier?","subject":{"kind":"commit","commit":"` + strings.Repeat("a", 40) + `"}}`
	if _, err := ld.Transition(context.Background(), runID, func(tx *ledger.Tx) error {
		for i := range n {
			row, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginAgent, Type: ledger.DecisionClarification, TaskID: "build", Attempt: 1,
				Member: "lead", Fingerprint: fmt.Sprintf("fp-%d", i), Request: body, Policy: policyJSON})
			if err != nil {
				return err
			}
			row.State, row.Rev, row.Why = ledger.DecisionCancelled, 2, "earlier"
			if err := tx.PutDecision(row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A replacement always has its place: in a run of 999 decisions with an
// open gate among them, an identity's response is accepted and holds the
// place of the decision its failed recheck would need, so a new request is
// refused decision_limit; not admitted, the replacement opens as the
// 1,000th. The run full, an identity's response to the replacement is
// refused decision_limit with no row (its own place would pass the bound),
// and the workbench token's, which needs none, is accepted and releases
// the gate's task.
func TestAReplacementAlwaysHasItsPlace(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000a8", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	g := gateOf(t, e, ld, run.ID)
	fillDecisions(t, ld, run.ID, ledger.MaxDecisionsPerRun-2)
	rc, err := e.RespondDecision(t.Context(), g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the identity's response: %+v %v", rc, err)
	}
	var de *DecisionError
	if _, _, err := e.RequestDecision(t.Context(), b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "One more?"}); !errors.As(err, &de) || de.Code != CodeDecisionLimit {
		t.Fatalf("a request past the place: %v", err)
	}
	e.ConfirmAuthority(g.ID, rc.K, false)
	old := decisionWhen(t, ld, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	fresh := decisionWhen(t, ld, old.SupersededBy, "open", inState(ledger.DecisionOpen))
	if all, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID, Limit: 1}); err != nil || len(all) != 1 || all[0].N != fresh.N {
		t.Fatalf("the newest decision: %+v %v", all, err)
	}
	if _, err := e.RespondDecision(t.Context(), fresh.ID(), Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", identity(e, "nate")); !errors.As(err, &de) || de.Code != CodeDecisionLimit || !strings.Contains(de.Words, "answer with the workbench token") {
		t.Fatalf("the identity's response past the place: %v", err)
	}
	if rec := decisionWhen(t, ld, fresh.ID(), "open", inState(ledger.DecisionOpen)); len(rec.Responses) != 0 {
		t.Fatalf("the refused identity's response kept a row: %+v", rec.Responses)
	}
	answer(t, e, fresh.ID(), Response{ResponseID: "r2", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, fresh.ID(), "applied", inState(ledger.DecisionApplied))
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// The rechecks of a response, each a kept refusal with its words: a
// router's answer is no operator's (not_approver), a past rev is stale, a
// rev nobody read is invalid with no row, an answer that does not fit the
// type is invalid; a second response names the first; a run not held
// takes the workbench token's response, never an identity's (not_held).
func TestAResponsesRechecks(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	var de *DecisionError
	router := Authority{Via: ViaRouter, Subject: "nate", Boot: e.Boot()}
	if rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "x1", Choice: choice(0), ExpectedRev: 1}, "user:nate", router); !errors.As(err, &de) || de.Code != CodeNotApprover || rc.K != 1 {
		t.Fatalf("a router's answer: %+v %v", rc, err)
	}
	if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "x2", Choice: choice(0), ExpectedRev: 5}, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeInvalid {
		t.Fatalf("a rev nobody read: %v", err)
	}
	if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "x3", Text: "sure", ExpectedRev: 1}, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeInvalid {
		t.Fatalf("an approval answered with a text: %v", err)
	}
	rec := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen))
	if len(rec.Responses) != 1 || rec.Responses[0].Status != ledger.ResponseRefused {
		t.Fatalf("responses %+v", rec.Responses)
	}
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	lineWith(t, p, "Decision "+d.ID)
	if rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r2", Choice: choice(1), ExpectedRev: 1, ByName: "Ana"}, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeDecisionAnswered || !strings.Contains(rc.Words, "answered by Nate") {
		t.Fatalf("a second response: %+v %v", rc, err)
	}
	stopRun(t, e, run.ID)

	// The run not held (stopped): the workbench token answers through the
	// ledger, an identity is refused.
	run2 := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b2, _, _ := attemptOn(t, e, fl, run2.ID, "build")
	d2 := ask(t, e, b2.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"})
	stopRun(t, e, run2.ID)
	if _, err := e.RespondDecision(t.Context(), d2.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1}, "user:nate", identity(e, "nate")); !errors.As(err, &de) || de.Code != CodeNotHeld {
		t.Fatalf("an identity's response to a run not held: %v", err)
	}
	answer(t, e, d2.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	decisionWhen(t, ld, d2.ID, "answered", inState(ledger.DecisionAnswered))
}

// refusingLedger is the real ledger, which refuses the next refuse
// transitions, before it writes anything: commits the ledger refused.
type refusingLedger struct {
	sqlLedger
	refuse atomic.Int32
}

func (l *refusingLedger) Transition(ctx context.Context, runID string, fn func(LedgerTx) error) (int64, error) {
	for {
		n := l.refuse.Load()
		if n <= 0 {
			break
		}
		if l.refuse.CompareAndSwap(n, n-1) {
			return 0, errors.New("the ledger refused the commit (test)")
		}
	}
	return l.sqlLedger.Transition(ctx, runID, fn)
}

// An application whose commit the ledger refused is carried by the
// pause's commit, whole: the answer applied once and typed once, its line
// queued only once the application is written; and a replacement whose
// commit was refused has its number, its holds' words, and is answered
// and applied.
func TestARefusedApplicationIsCarriedWhole(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	rl := &refusingLedger{sqlLedger: sqlLedger{l: ld}}
	e.UseLedger(rl)

	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	mirrorOf(t, l, d.ID)
	rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the identity's response: %+v %v", rc, err)
	}
	rl.refuse.Store(1)
	e.ConfirmAuthority(d.ID, rc.K, true)
	if rl.refuse.Load() != 0 {
		t.Fatal("no commit was refused")
	}
	rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowTyped {
		t.Fatalf("the application: %+v", app)
	}
	n := 0
	for _, k := range historyKinds(rec) {
		if k == ledger.HistoryApplied {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d applied rows: %q", n, historyKinds(rec))
	}
	lineWith(t, p, "Decision "+d.ID+" (approval) answered by Nate")
	if _, err := l.Submit(t.Context(), session.Submission{Text: "marker", ByName: "test"}); err != nil {
		t.Fatal(err)
	}
	lineWith(t, p, "marker")
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds %q", got)
	}
	stopRun(t, e, run.ID)

	// The replacement's commit refused.
	run2 := launchGated(t, e, dir, "api-sweep-000000b1", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run2.ID)
	rc, err = e.RespondDecision(t.Context(), g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the identity's response: %+v %v", rc, err)
	}
	rl.refuse.Store(1)
	e.ConfirmAuthority(g.ID, rc.K, false)
	if rl.refuse.Load() != 0 {
		t.Fatal("no commit was refused")
	}
	old := decisionWhen(t, ld, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	fresh := decisionWhen(t, ld, old.SupersededBy, "open", inState(ledger.DecisionOpen))
	if ts := taskOf(t, e, run2.ID, "deploy"); ts.Reason != "waits for decision "+fresh.ID() {
		t.Fatalf("deploy: %+v", ts)
	}
	answer(t, e, fresh.ID(), Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	if app := applied(t, decisionWhen(t, ld, fresh.ID(), "applied", inState(ledger.DecisionApplied))); app.How != HowReleased {
		t.Fatalf("the replacement's application: %+v", app)
	}
	// The refusal paused the run's dispatch: unpaused, deploy starts.
	if err := e.PauseRun(t.Context(), run2.ID, false); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run2.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run2.ID)
}

// A mirror set aside between the deliverer's look and its line (the agent
// at work) is answered by a line naming its decision, and then withdrawn:
// no question shows at the agent's next done.
func TestAMirrorSetAsideBeforeItsLineIsWithdrawn(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	q := mirrorOf(t, l, d.ID)
	var once sync.Once
	e.afterLook = func(member, mirror string) {
		if member == "lead" && mirror == q.ID {
			once.Do(func() {
				if err := l.TrySetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil); err != nil {
					t.Error(err)
				}
			})
		}
	}
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	lineWith(t, p, "Decision "+d.ID)
	infoWhen(t, l, "without the mirror", func(info session.Info) bool {
		return info.Attention.ID != q.ID && !slices.Contains(info.Kept, q.ID)
	})
	if err := l.TrySetAttentionFull(session.AttentionDone, "done", session.SourceAPI, session.KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if a := l.Info().Attention; a.Decision != "" || a.ID == q.ID {
		t.Fatalf("the applied decision's question at the next done: %+v", a)
	}
	stopRun(t, e, run.ID)
}

// An older decision's ask that comes after a newer one's (askMu taken out
// of their order) leaves the newer decision's question showing; so it does
// after a request superseding the older one was refused by the ledger,
// which put the older one back last among the run's decisions (#635,
// review round 2).
func TestAnOlderMirrorNeverReplacesANewer(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	rl := &refusingLedger{sqlLedger: sqlLedger{l: ld}}
	e.UseLedger(rl)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d1 := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"})
	mirrorOf(t, l, d1.ID)
	d2 := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Which cache?"})
	mirrorOf(t, l, d2.ID)
	e.mu.Lock()
	r := e.runs[run.ID]
	a := r.task("build").active()
	n1, _ := ledger.ParseDecisionID(d1.ID)
	older := r.decisionN(n1)
	e.mu.Unlock()
	// dr-1's ask, late.
	e.askMirror(r, a, older)
	if got := l.Info().Attention; got.Decision != d2.ID {
		t.Fatalf("the question after dr-1's late ask: %+v", got)
	}
	// A request superseding dr-1, its commit refused: dr-1 is open again.
	rl.refuse.Store(1)
	if _, _, err := e.RequestDecision(t.Context(), b.Attempt.SessionID, DecisionRequest{RequestID: "q3", Type: ledger.DecisionClarification, Question: "Which index now?", Supersedes: d1.ID}); err == nil {
		t.Fatal("the superseding request's refused commit was not told")
	}
	if rl.refuse.Load() != 0 {
		t.Fatal("no commit was refused")
	}
	decisionWhen(t, ld, d1.ID, "open", inState(ledger.DecisionOpen))
	e.askMirror(r, a, older)
	if got := l.Info().Attention; got.Decision != d2.ID {
		t.Fatalf("the question after dr-1's late ask, its supersession refused: %+v", got)
	}
	stopRun(t, e, run.ID)
}

// A run stopped between a response's choice of it and its transaction is
// no longer held: an identity's response is refused not_held, nothing
// recorded, and the workbench token's goes through the ledger.
func TestAResponseToARunStoppedMeanwhileIsNotHeld(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"})
	var once sync.Once
	e.beforeRespond = func(int64) { once.Do(func() { stopRun(t, e, run.ID) }) }
	var de *DecisionError
	if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1}, "user:nate", identity(e, "nate")); !errors.As(err, &de) || de.Code != CodeNotHeld {
		t.Fatalf("an identity's response to a run stopped meanwhile: %v", err)
	}
	if rec := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen)); len(rec.Responses) != 0 {
		t.Fatalf("the refused response kept a row: %+v", rec.Responses)
	}
	answer(t, e, d.ID, Response{ResponseID: "r2", Text: "btree", ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "answered", inState(ledger.DecisionAnswered))
}

// The asking attempt gone (its session ended) and another task's attempt
// live: the answer is applied for the task's next attempt (how:
// task_file), its hold released, and nothing is typed into the other
// attempt, nor into the next one.
func TestAnAnswerNeverReachesAnotherAttempt(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	_, wl, wp := attemptOn(t, e, fl, run.ID, "docs")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	mirrorOf(t, l, d.ID)
	p.End(1)
	waitTaskWhere(t, e, run.ID, "build", "held after its attempt ended", func(ts TaskState) bool {
		return ts.State == ledger.TaskReady && ts.Attempts == 1 && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptFailed
	})
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowTaskFile || app.Task != "build" || app.Attempt != 1 {
		t.Fatalf("the application: %+v", app)
	}
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds %q", got)
	}
	// Released, the task's next attempt starts.
	b2 := waitTaskWhere(t, e, run.ID, "build", "at its next attempt", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	l2, p2 := fl.attemptSession(t, b2)
	lineWith(t, p2, "build")
	for _, s := range []struct {
		l *session.Local
		p *sessiontest.FakeProc
	}{{wl, wp}, {l2, p2}} {
		if _, err := s.l.Submit(t.Context(), session.Submission{Text: "marker", ByName: "test"}); err != nil {
			t.Fatal(err)
		}
		lineWith(t, s.p, "marker")
	}
	stopRun(t, e, run.ID)
}

// A gate whose plan decision names no task before it, answered while the
// run goes on: noted, holding nothing, the run's work going on.
func TestAGateWithNoTaskIsNoted(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000b2", repoCrew(dir, work("build", "lead")), shipGate())
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("holds %q", got)
	}
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowNoted || len(app.Tasks) != 0 {
		t.Fatalf("the application: %+v", app)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskRunning {
		t.Fatalf("build after the gate: %+v", ts)
	}
	if r, _ := e.Get(run.ID); r.Outcome != "" {
		t.Fatalf("the run after the gate: %q", r.Outcome)
	}
	stopRun(t, e, run.ID)
}

// A gate holding a task and one that needs it, rejected: both cancelled,
// and the run's end recorded once, after both.
func TestARejectedGateEndsTheRunOnceAfterAllItsTasks(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000b3", repoCrew(dir, work("build", "lead"), work("deploy", "ops", "build")), shipGate("build", "deploy"))
	g := gateOf(t, e, ld, run.ID)
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1})
	rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowCancelled || !slices.Equal(app.Tasks, []string{"build", "deploy"}) {
		t.Fatalf("the application: %+v", app)
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeCancelled)
	kinds := sqlKinds(t, ld, run.ID)
	finished, lastCancel, finish := 0, -1, -1
	for i, k := range kinds {
		switch k {
		case "run.finished":
			finished++
			finish = i
		case "task.cancelled":
			lastCancel = i
		}
	}
	if finished != 1 || finish < lastCancel {
		t.Fatalf("events %q", kinds)
	}
}

// A replacement whose commit was refused, and the pause's after it, is
// carried by the next commit, a task's cancellation: the replacement opens
// with its number, and the task it holds stays cancelled in the ledger as
// in the engine, its row never put back as the replacement's change saw
// it (#635, review round 2).
func TestACarriedReplacementNeverUndoesACancellation(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	rl := &refusingLedger{sqlLedger: sqlLedger{l: ld}}
	e.UseLedger(rl)
	run := launchGated(t, e, dir, "api-sweep-000000b5", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	rc, err := e.RespondDecision(t.Context(), g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the identity's response: %+v %v", rc, err)
	}
	rl.refuse.Store(2)
	e.ConfirmAuthority(g.ID, rc.K, false)
	if n := rl.refuse.Load(); n != 0 {
		t.Fatalf("%d commits were not refused", n)
	}
	if rec := decisionWhen(t, ld, g.ID, "answered", inState(ledger.DecisionAnswered)); rec.SupersededBy != "" {
		t.Fatalf("the refused replacement was written: %+v", rec.DecisionRow)
	}
	if err := e.CancelTask(t.Context(), run.ID, "deploy"); err != nil {
		t.Fatal(err)
	}
	old := decisionWhen(t, ld, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	decisionWhen(t, ld, old.SupersededBy, "open", inState(ledger.DecisionOpen))
	if row := ledgerTask(t, ld, run.ID, "deploy"); row.State != ledger.TaskCancelled {
		t.Fatalf("deploy's row in the ledger after its cancellation: %+v", row)
	}
	if ts := taskOf(t, e, run.ID, "deploy"); ts.State != ledger.TaskCancelled {
		t.Fatalf("deploy in the engine: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// A dialog or a startup question the session finds as a decision's line
// is taken, nothing typed (the writer's guard: the mirror looked at
// first, then the screen), keeps the answer queued: once the agent is at
// work again, the answer is typed, once (#635, review round 2).
func TestADialogAsTheAnswerIsTakenKeepsItQueued(t *testing.T) {
	for name, refusal := range map[string]error{"question": session.ErrQuestion, "trust": session.ErrTrustQuestion} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, ld := decisionEngine(t)
			run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
			b, l, p := attemptOn(t, e, fl, run.ID, "build")
			d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
			mirrorOf(t, l, d.ID)
			var refused atomic.Bool
			e.submitAnswer = func(ctx context.Context, l *session.Local, sub session.Submission) (session.SubmitResult, error) {
				if refused.CompareAndSwap(false, true) {
					// The dialog found as the text was taken: it shows, and
					// nothing was typed.
					l.SetAttention(session.AttentionNeedsInput, "Allow the command?", session.SourceAPI)
					return session.SubmitResult{}, refusal
				}
				return l.Submit(ctx, sub)
			}
			answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
			decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
			infoWhen(t, l, "showing the dialog", func(info session.Info) bool { return info.Attention.Message == "Allow the command?" })
			// The dialog answered: the agent works on.
			l.SetAttention(session.AttentionWorking, "", session.SourceAPI)
			lineWith(t, p, "Decision "+d.ID+" (approval) answered by Nate")
			if !refused.Load() {
				t.Fatal("the line was never refused")
			}
			if _, err := l.Submit(t.Context(), session.Submission{Text: "marker", ByName: "test"}); err != nil {
				t.Fatal(err)
			}
			lineWith(t, p, "marker")
			stopRun(t, e, run.ID)
		})
	}
}

// holdingLedger is the real ledger, which holds run's first transition
// once it has committed, closing at and waiting for gate (a launch's
// first commit in the ledger, its engine not yet told), and tells read of
// each decision read.
type holdingLedger struct {
	sqlLedger
	run      string
	at, gate chan struct{}
	once     sync.Once
	read     chan int64
}

func (l *holdingLedger) Transition(ctx context.Context, runID string, fn func(LedgerTx) error) (int64, error) {
	rev, err := l.sqlLedger.Transition(ctx, runID, fn)
	if runID == l.run && err == nil {
		l.once.Do(func() {
			close(l.at)
			<-l.gate
		})
	}
	return rev, err
}

func (l *holdingLedger) Decision(ctx context.Context, n int64) (ledger.DecisionRecord, bool, error) {
	rec, ok, err := l.sqlLedger.Decision(ctx, n)
	select {
	case l.read <- n:
	default:
	}
	return rec, ok, err
}

// A response read while the launch's first commit, which opened the run's
// gate, is in the ledger and not yet the engine's is answered through the
// run once that commit is through, never past the engine: the gate is
// applied and its task starts (#635, review round 2).
func TestAResponseDuringTheLaunchsCommitIsTheEngines(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	const runID = "api-sweep-000000b6"
	hl := &holdingLedger{sqlLedger: sqlLedger{l: ld}, run: runID, at: make(chan struct{}), gate: make(chan struct{}), read: make(chan int64, 1)}
	e.UseLedger(hl)
	t.Cleanup(func() {
		select {
		case <-hl.gate:
		default:
			close(hl.gate)
		}
	})
	plan := gatedPlan(t, runID, head(t, dir), shipGate("deploy"))
	type launched struct {
		run *Run
		err error
	}
	done := make(chan launched, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		run, release, err := e.LaunchPlanned(ctx, repoCrew(dir, work("build", "lead"), work("deploy", "ops")), "CND-12 Fix the login loop", plan)
		release()
		done <- launched{run, err}
	}()
	<-hl.at
	rows, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: runID})
	if err != nil || len(rows) != 1 || rows[0].Origin != ledger.OriginPlan {
		t.Fatalf("the launch's first commit holds no gate: %+v %v", rows, err)
	}
	g := rows[0].ID()
	type responded struct {
		rc  ResponseReceipt
		err error
	}
	res := make(chan responded, 1)
	go func() {
		rc, err := e.RespondDecision(context.Background(), g, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"}, "admin", workbench(e))
		res <- responded{rc, err}
	}()
	<-hl.read // the response's look, the launch's commit held
	close(hl.gate)
	l := <-done
	if l.err != nil {
		t.Fatal(l.err)
	}
	if got := <-res; got.err != nil || got.rc.Status != "accepted" {
		t.Fatalf("the response: %+v %v", got.rc, got.err)
	}
	if app := applied(t, decisionWhen(t, ld, g, "applied", inState(ledger.DecisionApplied))); app.How != HowReleased || !slices.Equal(app.Tasks, []string{"deploy"}) {
		t.Fatalf("the application: %+v", app)
	}
	waitAttempt(t, e, runID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, runID)
}

// A decision's line the program takes slowly (its text still being taken
// when its time ran out: accepted, pending) keeps the decision's question
// until its Enter is in: the Enter answers it, by the responder, and the
// mirror is not withdrawn first (#635, review round 2).
func TestAnAnswerTakenSlowlyAnswersItsMirror(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	fl.inputWait = 200 * time.Millisecond
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	q := mirrorOf(t, l, d.ID)
	// The program takes nothing for now: the answer's text waits.
	for len(p.Input) < cap(p.Input) {
		p.Input <- nil
	}
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	waitLogged(t, e, run.ID, "decision "+d.ID+"'s answer waits for lead to take its input", 1, 10*time.Second)
	if a := l.Info().Attention; a.ID != q.ID {
		t.Fatalf("the mirror went while its line was pending: %+v", a)
	}
	// The program reads again: the text, then its Enter.
	lineWith(t, p, "Decision "+d.ID+" (approval) answered by Nate")
	infoWhen(t, l, "its question answered by Nate", func(info session.Info) bool {
		return info.Attention.ID != q.ID && info.LastAnswer != nil && info.LastAnswer.ByName == "Nate" && info.LastAnswer.Message == "Merge it?"
	})
	waitLogged(t, e, run.ID, "decision "+d.ID+"'s answer typed into lead", 1, 10*time.Second)
	stopRun(t, e, run.ID)
}

// fillOpen writes n open decisions of the run straight into the ledger,
// of no attempt: the run's open bound counts them, no attempt's does.
func fillOpen(t *testing.T, ld *ledger.Ledger, runID string, n int) {
	t.Helper()
	body := `{"question":"Elsewhere?","subject":{"kind":"commit","commit":"` + strings.Repeat("b", 40) + `"}}`
	if _, err := ld.Transition(context.Background(), runID, func(tx *ledger.Tx) error {
		for i := range n {
			if _, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginAgent, Type: ledger.DecisionClarification, TaskID: "docs",
				Member: "writer", Fingerprint: fmt.Sprintf("open-%d", i), Request: body, Policy: policyJSON}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A request that supersedes an open decision takes its place among the
// bounds: at the attempt's four open, and at the run's 64, a fifth request
// is refused decision_limit and a superseding one opens (#635, review
// round 2).
func TestASupersedingRequestOpensAtTheBound(t *testing.T) {
	for _, bound := range []string{"attempt", "run"} {
		t.Run(bound, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, ld := decisionEngine(t)
			run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
			b, _, _ := attemptOn(t, e, fl, run.ID, "build")
			clar := func(id, q, sup string) DecisionRequest {
				return DecisionRequest{RequestID: id, Type: ledger.DecisionClarification, Question: q, Supersedes: sup}
			}
			first := ask(t, e, b.Attempt.SessionID, clar("q1", "Which index?", ""))
			if bound == "attempt" {
				for i := 2; i <= ledger.MaxOpenPerAttempt; i++ {
					ask(t, e, b.Attempt.SessionID, clar(fmt.Sprintf("q%d", i), fmt.Sprintf("Question %d?", i), ""))
				}
			} else {
				fillOpen(t, ld, run.ID, ledger.MaxOpenPerRun-1)
			}
			var de *DecisionError
			if _, _, err := e.RequestDecision(t.Context(), b.Attempt.SessionID, clar("over", "One more?", "")); !errors.As(err, &de) || de.Code != CodeDecisionLimit {
				t.Fatalf("a request past the bound: %v", err)
			}
			d := ask(t, e, b.Attempt.SessionID, clar("again", "Which index, given the load?", first.ID))
			if rec := decisionWhen(t, ld, first.ID, "superseded", inState(ledger.DecisionSuperseded)); rec.SupersededBy != d.ID || !strings.Contains(rec.Why, d.ID) {
				t.Fatalf("%s: %+v", first.ID, rec.DecisionRow)
			}
			if got := activeHolds(t, ld, run.ID); slices.Contains(got, "build "+first.ID) || !slices.Contains(got, "build "+d.ID) {
				t.Fatalf("holds %q", got)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// Two responses that read the open decision before either committed: the
// one that loses is refused decision_answered, and its words, the
// receipt's as kept too, name who answered first (#635, review round 2).
func TestALateResponseNamesWhoAnsweredFirst(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	at, gate := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	e.beforeRespond = func(int64) {
		if first.CompareAndSwap(false, true) {
			close(at)
			<-gate
		}
	}
	type responded struct {
		rc  ResponseReceipt
		err error
	}
	lost := make(chan responded, 1)
	go func() {
		rc, err := e.RespondDecision(context.Background(), d.ID, Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1, ByName: "Ana"}, "admin", workbench(e))
		lost <- responded{rc, err}
	}()
	<-at // Ana's response has read the decision open
	answer(t, e, d.ID, Response{ResponseID: "r2", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"})
	close(gate)
	got := <-lost
	var de *DecisionError
	if !errors.As(got.err, &de) || de.Code != CodeDecisionAnswered || !strings.Contains(got.rc.Words, "answered by Nate") {
		t.Fatalf("the late response: %+v %v", got.rc, got.err)
	}
	rec := decisionWhen(t, ld, d.ID, "with the late response kept", func(rec ledger.DecisionRecord) bool { return len(rec.Responses) == 2 })
	var kept ResponseReceipt
	for _, r := range rec.Responses {
		if r.Status == ledger.ResponseRefused {
			if err := json.Unmarshal([]byte(r.Receipt), &kept); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.Contains(kept.Words, "answered by Nate") {
		t.Fatalf("the kept refusal: %+v", kept)
	}
	stopRun(t, e, run.ID)
}

// respondRefusing is the real ledger, which refuses the commit of the next
// transaction that kept a response to decision n once refuse is set: the
// response's row written, then the commit refused, so nothing of it is kept.
type respondRefusing struct {
	sqlLedger
	n      atomic.Int64
	refuse atomic.Bool
}

func (l *respondRefusing) Transition(ctx context.Context, runID string, fn func(LedgerTx) error) (int64, error) {
	if !l.refuse.Load() {
		return l.sqlLedger.Transition(ctx, runID, fn)
	}
	return l.sqlLedger.Transition(ctx, runID, func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		before, _, err := dtx.Record(l.n.Load())
		if err != nil {
			return err
		}
		if err := fn(ltx); err != nil {
			return err
		}
		after, _, err := dtx.Record(l.n.Load())
		if err != nil {
			return err
		}
		if len(after.Responses) > len(before.Responses) && l.refuse.CompareAndSwap(true, false) {
			return errors.New("the ledger refused the response's commit (test)")
		}
		return nil
	})
}

// savedAs fails unless rc is the receipt the ledger saved for the response
// k of rec, byte for byte, given again: repeat, with its k.
func savedAs(t *testing.T, rec ledger.DecisionRecord, k int, rc ResponseReceipt) {
	t.Helper()
	if !rc.Repeat || rc.K != k {
		t.Fatalf("not the repeat of response %d: %+v", k, rc)
	}
	rc.Repeat, rc.K = false, 0
	for _, r := range rec.Responses {
		if r.K == k {
			if got := receiptJSON(rc); got != r.Receipt {
				t.Fatalf("the receipt given again:\n%s\nthe receipt saved:\n%s", got, r.Receipt)
			}
			return
		}
	}
	t.Fatalf("no response %d in %+v", k, rec.Responses)
}

// A response sent again is answered from the row the ledger saved, byte for
// byte, before anything of the new send is built (#635, review round 3).
// The answer's accepted receipt fills the ledger's receipt bound, so a
// receipt built again for it (decision_answered's code and words are
// longer) would pass the bound and the repeat be refused too large. Its
// first send's commit is refused (nothing kept: the next send is a new
// response); a copy sent while that one commits finds its row in its own
// transaction; sent again once applied, it is the saved receipt; another
// body under its id is response_conflict; and after a restart (an engine
// over the same ledger, the run not held) it is the saved receipt again,
// for the workbench token and for an identity, whose repeat is not
// refused not_held. One response row, one application.
func TestARepeatIsItsSavedReceipt(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	rl := &respondRefusing{sqlLedger: sqlLedger{l: ld}}
	e.UseLedger(rl)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"})
	mirrorOf(t, l, d.ID)
	n, _ := ledger.ParseDecisionID(d.ID)
	rl.n.Store(n)

	// The text whose accepted receipt is the bound at its widest time.
	size := func(text string) int {
		return len(receiptJSON(ResponseReceipt{Decision: d.ID, Status: "accepted", Words: fmt.Sprintf("Recorded: %s answered by Nate", d.ID),
			Actor: "admin", ByName: "Nate", Text: text, At: time.Date(2026, 11, 6, 12, 0, 0, 123456789, time.UTC), Rev: 2}))
	}
	text := strings.Repeat("<", 1300)
	for size(text+"x") <= ledger.MaxDecisionReceipt {
		text += "x"
	}
	if size(text) < ledger.MaxDecisionReceipt-1 || len(text) > maxAnswerText {
		t.Fatalf("the text's receipt is %d bytes, the text %d", size(text), len(text))
	}
	resp := Response{ResponseID: "r1", Text: text, ExpectedRev: 1, ByName: "Nate"}

	rl.refuse.Store(true)
	if _, err := e.RespondDecision(t.Context(), d.ID, resp, "admin", workbench(e)); err == nil || rl.refuse.Load() {
		t.Fatalf("the refused commit: %v", err)
	}
	if rec := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen)); len(rec.Responses) != 0 {
		t.Fatalf("a refused commit kept %+v", rec.Responses)
	}

	// Sent again: a copy commits after this send read the decision and
	// before its transaction, which finds the copy's row.
	var copied atomic.Bool
	var inner ResponseReceipt
	var innerErr error
	e.beforeRespond = func(int64) {
		if copied.CompareAndSwap(false, true) { // the copy's own send passes here too
			inner, innerErr = e.RespondDecision(t.Context(), d.ID, resp, "admin", workbench(e))
		}
	}
	outer, err := e.RespondDecision(t.Context(), d.ID, resp, "admin", workbench(e))
	e.beforeRespond = nil
	if innerErr != nil || inner.Status != "accepted" || inner.Repeat || inner.K != 1 {
		t.Fatalf("the copy: %+v %v", inner, innerErr)
	}
	if err != nil {
		t.Fatalf("the send its copy passed: %v", err)
	}
	rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	savedAs(t, rec, 1, outer)
	lineWith(t, p, "Decision "+d.ID)

	again, err := e.RespondDecision(t.Context(), d.ID, resp, "admin", workbench(e))
	if err != nil {
		t.Fatalf("the response again: %v", err)
	}
	savedAs(t, rec, 1, again)
	other := resp
	other.Text += "y"
	var de *DecisionError
	if _, err := e.RespondDecision(t.Context(), d.ID, other, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeResponseConflict {
		t.Fatalf("another body under r1: %v", err)
	}

	// An identity's answer to a second question, accepted.
	d2 := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q2", Type: ledger.DecisionClarification, Question: "Which cache?"})
	resp2 := Response{ResponseID: "r1", Text: "lru", ExpectedRev: 1, ByName: "Ana"}
	if rc, err := e.RespondDecision(t.Context(), d2.ID, resp2, "user:ana", identity(e, "ana")); err != nil || rc.Status != "accepted" || rc.Repeat {
		t.Fatalf("the identity's response: %+v %v", rc, err)
	}

	// After a restart: an engine over the same ledger, the run not held.
	e2, _ := readyEngine(t)
	e2.UseLedger(SQLLedger(ld))
	restarted, err := e2.RespondDecision(t.Context(), d.ID, resp, "admin", workbench(e2))
	if err != nil {
		t.Fatalf("the response after a restart: %v", err)
	}
	savedAs(t, rec, 1, restarted)
	byIdentity, err := e2.RespondDecision(t.Context(), d2.ID, resp2, "user:ana", identity(e2, "ana"))
	if err != nil {
		t.Fatalf("the identity's response after a restart: %v", err)
	}
	savedAs(t, decisionWhen(t, ld, d2.ID, "answered", inState(ledger.DecisionAnswered)), 1, byIdentity)

	rec = decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	applies := 0
	for _, k := range historyKinds(rec) {
		if k == ledger.HistoryApplied {
			applies++
		}
	}
	if applies != 1 || len(rec.Responses) != 1 {
		t.Fatalf("%d applied rows, %d responses", applies, len(rec.Responses))
	}
	stopRun(t, e, run.ID)
}

// A stale response is told the changes after the rev it read, with their
// words, never one it saw (#635, review round 3): comments at revs 2 and 3
// (written as an operation writes them, its rev in its detail); a response
// that read rev 2 is told rev 3's words and not rev 2's, one that read
// rev 1 both, in order; past staleChanges the newest are told, with the
// count of the earlier, each one's words cut to staleText.
func TestAStaleResponseIsToldTheChangesSinceItsRev(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"})
	stopRun(t, e, run.ID)
	n, _ := ledger.ParseDecisionID(d.ID)
	comment := func(rev int, text string) {
		t.Helper()
		detail, _ := json.Marshal(map[string]any{"text": text, "by": "admin", "rev": rev, "words": "Comment kept on " + d.ID})
		if _, err := ld.Transition(context.Background(), run.ID, func(tx *ledger.Tx) error {
			row, _, err := tx.Decision(n)
			if err != nil {
				return err
			}
			row.Rev = rev
			if err := tx.PutDecision(row); err != nil {
				return err
			}
			_, _, err = tx.History(n, ledger.HistoryRow{Kind: ledger.HistoryCommented, By: "admin", Detail: string(detail), OpID: fmt.Sprintf("c%d", rev), Fingerprint: fmt.Sprintf("fp%d", rev)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	comment(2, "the old index is fine")
	comment(3, "use the btree index\nfor the sweep")
	stale := func(responseID string, read int) string {
		t.Helper()
		rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: responseID, Text: "btree", ExpectedRev: read}, "admin", workbench(e))
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeStaleDecision || rc.Words != de.Words {
			t.Fatalf("the response that read rev %d: %+v %v", read, rc, err)
		}
		return rc.Words
	}
	if w := stale("r1", 2); !strings.Contains(w, "rev 3, commented by admin: use the btree index for the sweep") || strings.Contains(w, "old index") || strings.Contains(w, "rev 2,") {
		t.Fatalf("read at rev 2: %q", w)
	}
	w := stale("r2", 1)
	if i, j := strings.Index(w, "rev 2, commented by admin: the old index is fine"), strings.Index(w, "rev 3, commented"); i < 0 || j < i {
		t.Fatalf("read at rev 1: %q", w)
	}
	for rev := 4; rev <= 8; rev++ {
		comment(rev, fmt.Sprintf("comment %d ", rev)+strings.Repeat("é", 200))
	}
	w = stale("r3", 1)
	if !strings.Contains(w, "2 earlier changes; rev 4, commented") || strings.Contains(w, "rev 3,") || !strings.Contains(w, "rev 8, commented by admin: comment 8 ") || !utf8.ValidString(w) {
		t.Fatalf("read at rev 1, seven changes: %q", w)
	}
	if strings.Contains(w, strings.Repeat("é", staleText/2)) || !strings.Contains(w, strings.Repeat("é", (staleText-len("comment 8 "))/2)+"…") {
		t.Fatalf("a change's words not cut to %d bytes: %q", staleText, w)
	}
}

// A response's fingerprint is of its body as sent (#635, review round 3):
// the same id with a text or a name that cleans to the answer's is another
// body (response_conflict), the body sent again its repeat, and a body
// whose text is not UTF-8 is refused with no row.
func TestAResponseIsFingerprintedAsSent(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, DecisionRequest{RequestID: "q1", Type: ledger.DecisionClarification, Question: "Which index?"})
	resp := Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1, ByName: "Nate"}
	answer(t, e, d.ID, resp)
	lineWith(t, p, "Decision "+d.ID)
	var de *DecisionError
	for _, other := range []Response{
		{ResponseID: "r1", Text: "btree ", ExpectedRev: 1, ByName: "Nate"},
		{ResponseID: "r1", Text: "btree\x01", ExpectedRev: 1, ByName: "Nate"},
		{ResponseID: "r1", Text: "btree", ExpectedRev: 1, ByName: " Nate"},
	} {
		if rc, err := e.RespondDecision(t.Context(), d.ID, other, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeResponseConflict {
			t.Fatalf("%+v under r1: %+v %v", other, rc, err)
		}
	}
	if rc, err := e.RespondDecision(t.Context(), d.ID, resp, "admin", workbench(e)); err != nil || !rc.Repeat || rc.Status != "accepted" {
		t.Fatalf("the body again: %+v %v", rc, err)
	}
	if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r2", Text: "b\xfftree", ExpectedRev: 1}, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeInvalid {
		t.Fatalf("a text not UTF-8: %v", err)
	}
	if rec := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied)); len(rec.Responses) != 1 {
		t.Fatalf("responses %+v", rec.Responses)
	}
	stopRun(t, e, run.ID)
}

// A repeat is looked for where it would act (#636, review round 4): a
// copy of a response held after its first look, while the other copy
// records a stale refusal (the approval commented on since its rev) and
// the asking worktree's HEAD then moves, finds the saved refusal under
// the supersession's lock when released: it answers the saved receipt and
// supersedes nothing. The approval stays open, its hold active, in the
// ledger as in the engine.
func TestARepeatHeldAcrossASubjectMoveSupersedesNothing(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	if _, err := e.CommentDecision(t.Context(), d.ID, "o1", 0, CommentKind, "Look at the index first.", "admin"); err != nil {
		t.Fatal(err)
	}
	decisionWhen(t, ld, d.ID, "commented", hasKind(ledger.HistoryCommented))
	at, gate := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	e.beforeRespond = func(int64) {
		if first.CompareAndSwap(false, true) {
			close(at)
			<-gate
		}
	}
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	resp := Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1, ByName: "Nate"}
	type responded struct {
		rc  ResponseReceipt
		err error
	}
	held := make(chan responded, 1)
	go func() {
		rc, err := e.RespondDecision(context.Background(), d.ID, resp, "admin", workbench(e))
		held <- responded{rc, err}
	}()
	<-at // the held copy looked, and found no row of r1
	var de *DecisionError
	saved, err := e.RespondDecision(t.Context(), d.ID, resp, "admin", workbench(e))
	if !errors.As(err, &de) || de.Code != CodeStaleDecision || saved.Repeat {
		t.Fatalf("the copy that records: %+v %v", saved, err)
	}
	writeFile(t, filepath.Join(b.Attempt.Worktree, "more.txt"), "m\n")
	commitAll(t, b.Attempt.Worktree, "more")
	close(gate)
	got := <-held
	if !errors.As(got.err, &de) || de.Code != CodeStaleDecision {
		t.Fatalf("the held copy: %+v %v", got.rc, got.err)
	}
	rec := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen))
	savedAs(t, rec, saved.K, got.rc)
	if rec.Rev != 2 || kindCount(rec, ledger.HistorySuperseded) != 0 || len(rec.Responses) != 1 {
		t.Fatalf("%s after the repeat: rev %d %q, %d responses", d.ID, rec.Rev, historyKinds(rec), len(rec.Responses))
	}
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"build " + d.ID}) {
		t.Fatalf("holds %q", got)
	}
	if rev, ok := engineHolds(t, e, run.ID, d.ID); !ok || rev != 2 {
		t.Fatalf("the engine's %s: rev %d, held %v", d.ID, rev, ok)
	}
	stopRun(t, e, run.ID)
}

// A replacement is counted as it is carried (#636, review round 4): in a
// run at its bound, an identity's answer holding the last place, its
// recheck failed and the replacement's commit refused, and the pause's
// after it, the run still holds every decision it may, so a retry that
// would ask a rejected gate again is refused decision_limit; nothing
// carried must then fail for ever, and the next commit writes the
// replacement as the run's 1,000th decision.
func TestACarriedReplacementHoldsItsPlace(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	rl := refusing(e, ld)
	smoke := PackageDecision{ID: "smoke", Question: "Smoke it?", Kind: ledger.DecisionApproval, Before: []string{"smoke"}}
	run := launchGated(t, e, dir, "api-sweep-000000cb", repoCrew(dir, work("deploy", "ops"), work("smoke", "qa")), shipGate("deploy"), smoke)
	gates := map[string]ledger.DecisionRow{}
	rows, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		gates[row.Gate] = row
	}
	answer(t, e, gates["smoke"].ID(), Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1})
	waitTask(t, e, run.ID, "smoke", ledger.TaskCancelled, 0)
	fillDecisions(t, ld, run.ID, ledger.MaxDecisionsPerRun-3)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	r.dec.opened = ledger.MaxDecisionsPerRun - 1 // as the ledger counts them
	e.mu.Unlock()
	ship := gates["ship"].ID()
	rc, err := e.RespondDecision(t.Context(), ship, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "user:nate", identity(e, "nate"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the identity's response: %+v %v", rc, err)
	}

	g := rl.armHeld("decision.superseded")
	released := sync.OnceFunc(func() { close(g.release) })
	t.Cleanup(released) // a check that fails while the commit is held ends the test, never hangs it
	confirmed := make(chan struct{})
	go func() {
		defer close(confirmed)
		e.ConfirmAuthority(ship, rc.K, false)
	}()
	<-g.reached // the replacement made, its commit not answered
	room := func(when string) {
		t.Helper()
		e.mu.Lock()
		held, ok := r.decisionsHeld(), r.decisionRoom(1)
		e.mu.Unlock()
		if held != ledger.MaxDecisionsPerRun || ok {
			t.Fatalf("%s: the run holds %d, room for one more %v", when, held, ok)
		}
	}
	room("the replacement's commit under way")
	rl.arm("decision.superseded") // and the pause's, which carries it
	released()
	rl.wasRefused(t)
	rl.wasRefused(t)
	<-confirmed
	room("both commits refused")
	var de *DecisionError
	if err := e.RetryTask(t.Context(), run.ID, "smoke"); !errors.As(err, &de) || de.Code != CodeDecisionLimit {
		t.Fatalf("the retry: %v", err)
	}
	if ts := taskOf(t, e, run.ID, "smoke"); ts.State != ledger.TaskCancelled {
		t.Fatalf("smoke after the refused retry: %+v", ts)
	}

	if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
		t.Fatalf("the unpause, which writes what was carried: %v", err)
	}
	old := decisionWhen(t, ld, ship, "superseded", inState(ledger.DecisionSuperseded))
	fresh := decisionWhen(t, ld, old.SupersededBy, "open", inState(ledger.DecisionOpen))
	all, err := allDecisions(t.Context(), sqlLedger{l: ld}, ledger.DecisionFilter{CurrentRun: run.ID})
	if err != nil || len(all) != ledger.MaxDecisionsPerRun || all[0].N != fresh.N {
		t.Fatalf("the run's decisions: %d, %v (the replacement %s)", len(all), err, fresh.ID())
	}
	if paused(t, e, run.ID) {
		t.Fatal("the run is still paused")
	}
	stopRun(t, e, run.ID)
}
