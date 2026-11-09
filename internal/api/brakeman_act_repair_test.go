package api

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// Brakeman's request_repair (docs/plans/brakeman.md, PR-12; BK2; #658):
// RV1's review route taking the turn. The fixture is the act fixture with
// a run of CND-1 launched by the operator whose build attempt 1 succeeded,
// the candidate the turn requests changes of.

// repairFixture is the act fixture and the candidate: its run and commit.
type repairFixture struct {
	*actFixture
	run, commit string
}

// newRepairFixture makes the act fixture, its turn granted grant with mod
// applied, and the candidate.
func newRepairFixture(t *testing.T, grant []string, mod func(c map[string]any)) *repairFixture {
	t.Helper()
	f := newActFixture(t, grant, mod)
	run, c := succeededCandidate(t, f.launchTest, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	return &repairFixture{actFixture: f, run: run, commit: c}
}

// repair sends the turn's request for changes of the candidate under
// opID, with set over the body, and answers it.
func (f *repairFixture) repair(t *testing.T, opID string, set map[string]any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"opId": opID, "candidate": candName(f.run, "build", 1, f.commit), "base": f.head, "verdict": dispatch.VerdictRequestChanges,
		"summary": "Handle the error.", "findings": []any{changeOf("Handle the error.")}}
	maps.Copy(body, set)
	return f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/reviews", body)
}

// TestARepairIsHeldToTheGrantAndWhatTheTurnSees: request_repair ungranted
// is refused first, though the turn reads the candidate (a read gives no
// grant); granted, with the runs or Dispatch not visible, refused as not
// visible and the candidate not read (a grant gives no read); with the
// candidate's run outside the scope, refused as outside it. Nothing is
// recorded.
func TestARepairIsHeldToTheGrantAndWhatTheTurnSees(t *testing.T) {
	f := newRepairFixture(t, []string{brakeman.ActLaunchApprovedPlan}, nil)
	candidates := "/api/runs/" + f.run + "/candidates"
	if status, out := f.asTurn(t, http.MethodGet, candidates, nil); status != http.StatusOK {
		t.Fatalf("the read: %d %v", status, out)
	}
	if status, out := f.repair(t, "q-1", nil); status != http.StatusForbidden || codeOf(out) != "not_granted" {
		t.Fatalf("ungranted: %d %v", status, out)
	}
	for _, hidden := range []string{brakeman.ClassRuns, brakeman.ClassDispatch} {
		f.endTurn(t)
		f.turn(t, []string{brakeman.ActRequestRepair}, func(c map[string]any) { seeAllBut(c, hidden) })
		if status, out := f.repair(t, "q-2", nil); status != http.StatusForbidden || codeOf(out) != "not_visible" {
			t.Fatalf("%s hidden: %d %v", hidden, status, out)
		}
		if hidden == brakeman.ClassRuns {
			if status, out := f.asTurn(t, http.MethodGet, candidates, nil); status != http.StatusForbidden || codeOf(out) != "not_visible" {
				t.Fatalf("the read with the runs hidden: %d %v", status, out)
			}
		}
	}
	f.endTurn(t)
	f.turn(t, []string{brakeman.ActRequestRepair}, func(c map[string]any) {
		seeAllBut(c, "")
		c["scope"] = map[string]any{"repos": []any{"/elsewhere"}}
	})
	if status, out := f.repair(t, "q-3", nil); status != http.StatusForbidden || codeOf(out) != "outside_scope" {
		t.Fatalf("the run outside the scope: %d %v", status, out)
	}
	if acts, n := f.acts(t), reviewsOn(t, f.srv, "CND-1"); len(acts) != 0 || n != 0 {
		t.Fatalf("acts %+v, %d reviews", acts, n)
	}
}

// seeAllBut sets the configuration c's visibility of the runs and Dispatch
// on, but hidden's.
func seeAllBut(c map[string]any, hidden string) {
	v := c["visibility"].(map[string]any)
	for _, class := range []string{brakeman.ClassRuns, brakeman.ClassDispatch} {
		v[class] = class != hidden
	}
}

// TestARepairRequestsChangesAsBrakeman: granted, the turn's request is
// RV1's review, recorded as Brakeman's (actor and via brakeman, by
// Brakeman), applied by Yardmaster under the actor brakeman, its receipt
// Dispatch's under the derived op id and copied into the request's act
// with the candidate's run; the answer the receipt and its ref, never the
// review; the same opId again is the same review.
func TestARepairRequestsChangesAsBrakeman(t *testing.T) {
	f := newRepairFixture(t, []string{brakeman.ActRequestRepair}, nil)
	status, out := f.repair(t, "q-1", nil)
	rc, _ := out["receipt"].(map[string]any)
	op := actOp(f.req, "q-1")
	if status != http.StatusCreated || rc["status"] != dispatch.StatusDone || out["receiptRef"] != "op:brakeman/"+op || out["review"] != nil {
		t.Fatalf("the act: %d %v", status, out)
	}
	id := rc["id"].(string)
	v := reviewNow(t, f.srv, id)
	if v.Actor != brakemanActor || v.Via != dispatch.ViaBrakeman || v.CreatedBy != brakemanBy || v.Verdict != dispatch.VerdictRequestChanges {
		t.Fatalf("the review: %+v", v.ReviewSummary)
	}
	if app := waitApplication(t, f.srv, id, dispatch.ApplicationApplied); app.Run != f.run || app.Attempt != 2 {
		t.Fatalf("the application: %+v", app)
	}
	if row, found, err := f.srv.ledger.Receipt(t.Context(), f.run, brakemanActor, reviewCommandID(id, 1)); err != nil || !found || row.Status != ledger.CommandDone {
		t.Fatalf("the ledger's command: %+v %v %v", row, found, err)
	}
	acts := f.acts(t)
	if len(acts) != 1 || acts[0].Operation != brakeman.ActRequestRepair || acts[0].State != brakeman.ActDone || acts[0].Receipt != id ||
		acts[0].Run != f.run || acts[0].Target.String() != "ticket:CND-1" || acts[0].Op != op {
		t.Fatalf("acts %+v", acts)
	}
	status, again := f.repair(t, "q-1", nil)
	if rc2, _ := again["receipt"].(map[string]any); status >= 300 || rc2["id"] != id || rc2["repeat"] != true {
		t.Fatalf("the same opId again: %d %v", status, again)
	}
	if n := reviewsOn(t, f.srv, "CND-1"); n != 1 {
		t.Fatalf("%d reviews", n)
	}
}

// TestARepairRetryAfterALostReplyIsOneOperation: an act whose review
// committed and whose answer was lost (the server stopped before the
// copy) is answered from Dispatch's receipt when the turn sends it again
// under its opId: one review, one command, the entry done.
func TestARepairRetryAfterALostReplyIsOneOperation(t *testing.T) {
	f := newRepairFixture(t, []string{brakeman.ActRequestRepair}, nil)
	holdActAt(t, "committed")
	if status, out := f.repair(t, "q-1", nil); status != http.StatusServiceUnavailable {
		t.Fatalf("the act: %d %v", status, out)
	}
	atAct(t, func(string, string, string) error { return nil })
	status, out := f.repair(t, "q-1", nil)
	rc, _ := out["receipt"].(map[string]any)
	if status >= 300 || rc["status"] != dispatch.StatusDone || rc["repeat"] != true {
		t.Fatalf("sent again: %d %v", status, out)
	}
	id := rc["id"].(string)
	if n := reviewsOn(t, f.srv, "CND-1"); n != 1 {
		t.Fatalf("%d reviews", n)
	}
	waitApplication(t, f.srv, id, dispatch.ApplicationApplied)
	if _, found, _ := f.srv.ledger.Receipt(t.Context(), f.run, brakemanActor, reviewCommandID(id, 2)); found {
		t.Fatal("a second command was sent")
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActDone || acts[0].Receipt != id {
		t.Fatalf("acts %+v", acts)
	}
}

// TestARepairOfATargetInTheWrongStateIsRefused: what is not a request for
// changes Brakeman may send is refused (a comment, an approval, via, the
// word to continue); a ticket that moved since the turn read it, a commit
// that is not the attempt's result, a candidate whose task began a later
// attempt and a run the server no longer runs are refused before anything
// is recorded: one review, the first.
func TestARepairOfATargetInTheWrongStateIsRefused(t *testing.T) {
	f := newRepairFixture(t, []string{brakeman.ActRequestRepair}, nil)
	for i, c := range []struct {
		set          map[string]any
		status       int
		code, reason string
	}{
		{map[string]any{"verdict": dispatch.VerdictComment}, http.StatusForbidden, "not_granted", "a comment"},
		{map[string]any{"verdict": dispatch.VerdictApprove, "findings": []any{}}, http.StatusForbidden, "not_granted", "an approval"},
		{map[string]any{"via": dispatch.ViaBrakeman}, http.StatusBadRequest, "invalid_request", "via"},
		{map[string]any{"continueRun": true}, http.StatusForbidden, "not_granted", "the word to continue"},
		{map[string]any{"expectedRev": f.ticketRev(t) + 1}, http.StatusConflict, dispatch.RefusalStaleRevision, "a ticket's rev it left"},
		{map[string]any{"candidate": candName(f.run, "build", 1, f.head)}, http.StatusConflict, dispatch.RefusalInvalid, "a commit not the result"},
	} {
		if status, out := f.repair(t, "w-"+string(rune('a'+i)), c.set); status != c.status || codeOf(out) != c.code {
			t.Fatalf("%s: %d %v", c.reason, status, out)
		}
	}
	if status, out := f.repair(t, "q-1", nil); status != http.StatusCreated {
		t.Fatalf("the first: %d %v", status, out)
	}
	// Attempt 2 began: the candidate moved.
	if status, out := f.repair(t, "q-2", nil); status != http.StatusConflict || codeOf(out) != "stale_target" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "moved") {
		t.Fatalf("a moved candidate: %d %v", status, out)
	}
	f.expect(t, "POST", "/api/runs/"+f.run+"/stop", nil, http.StatusOK, "")
	if status, out := f.repair(t, "q-3", nil); status != http.StatusConflict || codeOf(out) != "stale_target" {
		t.Fatalf("a stopped run: %d %v", status, out)
	}
	if acts, n := f.acts(t), reviewsOn(t, f.srv, "CND-1"); len(acts) != 1 || acts[0].OpID != "q-1" || n != 1 {
		t.Fatalf("acts %+v, %d reviews", acts, n)
	}
}

// TestARepairIsHeldToTheRunItWasAdmittedOn: a run continued between the
// act's admission and its application is never sent the request: the
// review is recorded and its application refused in words naming the
// continuation, and no command reaches either run.
func TestARepairIsHeldToTheRunItWasAdmittedOn(t *testing.T) {
	f := newRepairFixture(t, []string{brakeman.ActRequestRepair}, nil)
	var next string
	atAct(t, func(stage, _, _ string) error {
		if stage == "pending" && next == "" {
			f.expect(t, "POST", "/api/runs/"+f.run+"/stop", nil, http.StatusOK, "")
			out := f.expect(t, "POST", "/api/runs/"+f.run+"/resume", nil, http.StatusCreated, "")
			next = out["run"].(map[string]any)["id"].(string)
			t.Cleanup(func() { f.do("POST", "/api/runs/"+next+"/stop", adminToken, nil) })
		}
		return nil
	})
	status, out := f.repair(t, "q-1", nil)
	rc, _ := out["receipt"].(map[string]any)
	if status != http.StatusCreated || rc["status"] != dispatch.StatusDone || next == "" {
		t.Fatalf("the act: %d %v (continued as %q)", status, out, next)
	}
	id := rc["id"].(string)
	app := waitApplication(t, f.srv, id, dispatch.ApplicationRefused)
	if !strings.Contains(app.Why, "continued as "+next) {
		t.Fatalf("the application: %+v", app)
	}
	for _, run := range []string{f.run, next} {
		if _, found, _ := f.srv.ledger.Receipt(t.Context(), run, brakemanActor, reviewCommandID(id, 1)); found {
			t.Fatalf("a command reached %s", run)
		}
	}
}
