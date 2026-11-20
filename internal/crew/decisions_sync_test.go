package crew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Held answers in the engine (decisions_sync.go): recorded through DR1's
// path as an identity's, refused where they are never taken, applied only
// once the recheck says yes, superseded on a no, never on a restart.

const heldStatement = "a signed statement"

// held is a held answer's authority of subject under grant.
func held(subject, grant, fence string) Authority {
	sum := sha256.Sum256([]byte(heldStatement))
	return Authority{Grant: grant, Via: ViaSync, Subject: subject, Membership: "m-1", SignIn: "s-1", At: "2026-11-20T09:00:00Z",
		StatementSHA256: hex.EncodeToString(sum[:]), RequestSHA256: strings.Repeat("a", 64), Held: "h-" + subject, Fence: fence}
}

// rechecks records what the engine hands its recheck.
type rechecks struct {
	mu  sync.Mutex
	got []string
	ch  chan struct{}
}

func newRechecks(e *Engine) *rechecks {
	rk := &rechecks{ch: make(chan struct{}, 16)}
	e.SetRecheck(func(id string, k int) {
		rk.mu.Lock()
		rk.got = append(rk.got, id)
		rk.mu.Unlock()
		rk.ch <- struct{}{}
	})
	return rk
}

func (rk *rechecks) wait(t *testing.T, id string) {
	t.Helper()
	end := time.After(20 * time.Second)
	for {
		rk.mu.Lock()
		asked := slices.Contains(rk.got, id)
		rk.mu.Unlock()
		if asked {
			return
		}
		select {
		case <-rk.ch:
		case <-end:
			t.Fatalf("the recheck of %s was never asked", id)
		}
	}
}

// A held answer is recorded under user:<subject> with its authority and
// its statement in the response's transaction; it is not applied until the
// recheck says yes, the engine asking for the recheck once; a repeat of it
// is the same receipt and writes nothing; applied once.
func TestViaSyncAppliedOnlyAfterAdmits(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	rk := newRechecks(e)
	run := launchGated(t, e, dir, "api-sweep-000000b1", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	resp := Response{ResponseID: "h1", Choice: choice(0), ExpectedRev: 1}
	rc, err := e.RespondHeld(t.Context(), g.ID, resp, "user:ana", held("ana", grantAdmin, "f-1"), heldStatement)
	if err != nil || rc.Status != "accepted" || rc.Actor != "user:ana" {
		t.Fatalf("the held answer: %+v %v", rc, err)
	}
	rk.wait(t, g.ID)
	rec := decisionWhen(t, ld, g.ID, "answered", inState(ledger.DecisionAnswered))
	if s, ok, err := ld.Statement(t.Context(), rec.N, rc.K); err != nil || !ok || s != heldStatement {
		t.Fatalf("the statement: %q %v %v", s, ok, err)
	}
	if a := rec.Responses[0].Authority; !strings.Contains(a, `"via":"sync"`) || !strings.Contains(a, `"fence":"f-1"`) || strings.Contains(a, heldStatement) {
		t.Fatalf("the authority: %s", a)
	}
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
	again, err := e.RespondHeld(t.Context(), g.ID, resp, "user:ana", held("ana", grantAdmin, "f-2"), heldStatement)
	if err != nil || !again.Repeat || again.K != rc.K || again.Status != "accepted" {
		t.Fatalf("the repeat: %+v %v", again, err)
	}
	e.ConfirmAuthority(g.ID, rc.K, true)
	rec = decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	e.applyDecisions(r)
	e.ConfirmAuthority(g.ID, rc.K, true)
	rec = decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	n := 0
	for _, k := range historyKinds(rec) {
		if k == ledger.HistoryApplied {
			n++
		}
	}
	if n != 1 || len(rec.Responses) != 1 {
		t.Fatalf("%d applied rows, %d responses", n, len(rec.Responses))
	}
	rk.mu.Lock()
	asked := len(rk.got)
	rk.mu.Unlock()
	if asked != 1 {
		t.Fatalf("the recheck was asked %d times", asked)
	}
	stopRun(t, e, run.ID)
}

// A held answer the recheck does not admit is superseded by a fresh
// decision with its holds, as an identity's is.
func TestViaSyncSupersededWhenAdmitsSaysNo(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	newRechecks(e)
	run := launchGated(t, e, dir, "api-sweep-000000b2", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	rc, err := e.RespondHeld(t.Context(), g.ID, Response{ResponseID: "h1", Choice: choice(0), ExpectedRev: 1}, "user:ana", held("ana", grantAdmin, "f-1"), heldStatement)
	if err != nil {
		t.Fatal(err)
	}
	e.ConfirmAuthority(g.ID, rc.K, false)
	old := decisionWhen(t, ld, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	if old.Applied != "" || !strings.Contains(old.Why, "user:ana's answer could not be rechecked") {
		t.Fatalf("the refused answer's decision: %+v", old.DecisionRow)
	}
	fresh := decisionWhen(t, ld, old.SupersededBy, "open", inState(ledger.DecisionOpen))
	if got := activeHolds(t, ld, run.ID); !slices.Equal(got, []string{"deploy " + fresh.ID()}) {
		t.Fatalf("holds %q", got)
	}
	stopRun(t, e, run.ID)
}

// A held answer accepted by another process (a restart) is not superseded
// for that: it waits for its recheck, which the new process asks for, and
// is applied on a yes.
func TestSyncAnswerRecheckedAfterRestart(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	rk := newRechecks(e)
	run := launchGated(t, e, dir, "api-sweep-000000b3", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	auth := held("ana", grantAdmin, "f-1")
	auth.Boot = "a-process-before-this-one"
	rc, err := e.RespondHeld(t.Context(), g.ID, Response{ResponseID: "h1", Choice: choice(0), ExpectedRev: 1}, "user:ana", auth, heldStatement)
	if err != nil {
		t.Fatal(err)
	}
	// The answer's boot as the ledger keeps it is another process's, as
	// after a restart.
	e.mu.Lock()
	r := e.runs[run.ID]
	d := r.decisionN(mustN(t, g.ID))
	d.answer.auth.Boot = "a-process-before-this-one"
	e.mu.Unlock()
	rk.wait(t, g.ID)
	e.applyDecisions(r)
	if rec := decisionWhen(t, ld, g.ID, "answered", inState(ledger.DecisionAnswered)); rec.Applied != "" || rec.State != ledger.DecisionAnswered {
		t.Fatalf("a restart's held answer was taken up without its recheck: %+v", rec.DecisionRow)
	}
	e.ConfirmAuthority(g.ID, rc.K, true)
	decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	stopRun(t, e, run.ID)
}

func mustN(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		t.Fatalf("%q", id)
	}
	return n
}

// While a router that confirms policies is registered the engine refuses
// a held answer that reaches it, kept, answered_at_server, its statement
// kept beside the refusal; the router is not asked.
func TestEngineRefusesAHeldAnswerUnderAPolicy(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000b4", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	asked := 0
	e.SetRouterPolicies(true, func(context.Context, ledger.DecisionRecord, string, string) ext.Confirmation {
		asked++
		return ext.Confirmation{}
	})
	rc, err := e.RespondHeld(t.Context(), g.ID, Response{ResponseID: "h1", Choice: choice(0), ExpectedRev: 1}, "user:ana", held("ana", grantAdmin, "f-1"), heldStatement)
	if de := decisionFailure(err); de == nil || de.Code != CodeAnsweredAtServer || rc.Status != "refused" || !strings.Contains(rc.Words, "an answer given elsewhere is not taken here") {
		t.Fatalf("the held answer: %+v %v", rc, err)
	}
	rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen))
	if len(rec.Responses) != 1 || rec.Responses[0].Status != ledger.ResponseRefused {
		t.Fatalf("the refusal was not kept: %+v", rec.Responses)
	}
	if s, ok, _ := ld.Statement(t.Context(), rec.N, rc.K); !ok || s != heldStatement {
		t.Fatalf("the kept refusal's statement: %q %v", s, ok)
	}
	if asked != 0 {
		t.Fatalf("the router was asked %d times", asked)
	}
	stopRun(t, e, run.ID)
}

// A held answer to a run the engine does not hold is refused not_held,
// nothing written: the provider keeps it.
func TestHeldAnswerNotHeldRunStaysHeld(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000b5", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	g := gateOf(t, e, ld, run.ID)
	stopRun(t, e, run.ID)
	_, err := e.RespondHeld(t.Context(), g.ID, Response{ResponseID: "h1", Choice: choice(0), ExpectedRev: 1}, "user:ana", held("ana", grantAdmin, "f-1"), heldStatement)
	if de := decisionFailure(err); de == nil || de.Code != CodeNotHeld {
		t.Fatalf("the held answer to a stopped run: %v", err)
	}
	if rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen)); len(rec.Responses) != 0 {
		t.Fatalf("a row was written: %+v", rec.Responses)
	}
}

// The entries take a held answer as an identity's: the operator's with the
// admin grant, the decide grant's with the decide grant; never an eligible
// person's; Sent takes it as its subject's own.
func TestHeldAnswerAdmittedByTheEntries(t *testing.T) {
	admin := Admittee{Grants: []string{string(ext.GrantAdmin)}, Via: ViaSync, Subject: "ana"}
	decide := Admittee{Grants: []string{string(ext.GrantDecide)}, Via: ViaSync, Subject: "dan"}
	switch {
	case !admin.operator():
		t.Error("an admin's held answer is not the operator's")
	case decide.operator():
		t.Error("a decide principal's held answer is the operator's")
	case !Admits([]PolicyEntry{{Kind: EntryGrant, Grant: string(ext.GrantDecide)}}, decide):
		t.Error("the decide grant's entry refuses a decide principal's held answer")
	case Admits([]PolicyEntry{{Kind: EntryOperator}}, decide):
		t.Error("the operator's entry admits a decide principal's held answer")
	case Admits([]PolicyEntry{{Kind: EntryEligible, Subject: "dan"}}, decide):
		t.Error("an eligible entry admits a held answer")
	}
	row := ledger.ResponseRow{Actor: "user:dan", Authority: `{"grant":"decide","via":"sync","subject":"dan","boot":"b"}`}
	reader := Admittee{Grants: []string{string(ext.GrantDecide)}, Via: ViaIdentity, Subject: "dan"}
	if !reader.Sent(row) {
		t.Error("Sent does not take dan's held answer as dan's")
	}
	if (Admittee{Grants: reader.Grants, Via: ViaIdentity, Subject: "eve"}).Sent(row) {
		t.Error("Sent takes dan's held answer as eve's")
	}
}

// A comparison's selection is never answered by a held answer: refused
// not_approver, kept, whatever grant its subject holds.
func TestHeldAnswerToASelectionIsRefused(t *testing.T) {
	se := launchSelect(t, 2)
	defer stopRun(t, se.e, se.run.ID)
	se.pass(t, se.run.ID, "approach-a", 1)
	se.pass(t, se.run.ID, "approach-b", 1)
	row := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
	rc, err := se.e.RespondHeld(t.Context(), row.ID(), Response{ResponseID: "h1", Choice: choice(0), Rationale: "a is smaller", ExpectedRev: row.Rev}, "user:ana", held("ana", grantAdmin, "f-1"), heldStatement)
	if de := decisionFailure(err); de == nil || de.Code != CodeNotApprover || !strings.Contains(rc.Words, "is a comparison's selection: it is answered only at this server") {
		t.Fatalf("the held answer to the selection: %+v %v", rc, err)
	}
	rec := decisionWhen(t, se.ld, row.ID(), "open", inState(ledger.DecisionOpen))
	if len(rec.Responses) != 1 || rec.Responses[0].Status != ledger.ResponseRefused {
		t.Fatalf("the refusal was not kept: %+v", rec.Responses)
	}
}
