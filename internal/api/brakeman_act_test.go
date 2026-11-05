package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// Brakeman's authorized actions (docs/plans/brakeman.md, PR-12; BK2). The
// fixture is a launchTest (CND-1 with revision 1 of a saved managed crew
// approved) whose catalog also holds Brakeman's scripted agents, and a
// turn granted what the test grants. An act is the turn's session token
// with X-Conductor-Session naming its session, on the granted operation's
// own route. Each test runs git under a HOME of its own (planRepo) and
// some hold the act's seam, so none is parallel.

// actFixture is the launchTest, the Brakeman desk over its server, and the
// turn.
type actFixture struct {
	*launchTest
	bk            *bkEnv
	req, sid, tok string
}

// newActFixture makes the fixture and its turn, granted the operations
// grant, with mod applied to the configuration.
func newActFixture(t *testing.T, grant []string, mod func(c map[string]any)) *actFixture {
	t.Helper()
	l := newLaunchTest(t, nil, nil, bkAgents()...)
	f := &actFixture{launchTest: l, bk: bkEnvOf(t, l.testEnv)}
	f.turn(t, grant, mod)
	return f
}

// turn turns Brakeman on with the grants and mod and waits for a turn's
// session to be running.
func (f *actFixture) turn(t *testing.T, grant []string, mod func(c map[string]any)) {
	t.Helper()
	f.bk.enable(t, "bk", func(c map[string]any) {
		act := make([]any, 0, len(grant))
		for _, g := range grant {
			act = append(act, g)
		}
		c["permissions"].(map[string]any)["act"] = act
		if mod != nil {
			mod(c)
		}
	})
	f.req = f.bk.briefing(t)
	f.sid, f.tok, _ = f.bk.turnSession(t, f.req)
	f.bk.waitState(t, f.req, brakeman.StateRunning)
}

// asTurn sends body to path as the turn (its token, its session named)
// and answers the status and the reply.
func (f *actFixture) asTurn(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(encodeNoHTML(t, body))
	}
	req, err := http.NewRequest(method, f.http.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.tok)
	req.Header.Set(brakemanSessionHeader, f.sid)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

// launchAct sends a launch act of CND-1 with body (the fixture's revision
// unless it names one) and answers it.
func (f *actFixture) launchAct(t *testing.T, body map[string]any) (int, map[string]any) {
	t.Helper()
	if _, ok := body["plan"]; !ok {
		body["plan"] = f.plan
	}
	return f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/launch", body)
}

// acts is the request's acts as the desk keeps them.
func (f *actFixture) acts(t *testing.T) []brakeman.Act {
	t.Helper()
	r, err := f.bk.desk.st.Get(f.req)
	if err != nil {
		t.Fatal(err)
	}
	return r.Acts
}

// codeOf is a reply's error code.
func codeOf(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// ticketRev is CND-1's revision now.
func (f *actFixture) ticketRev(t *testing.T) int64 {
	t.Helper()
	tk, err := f.srv.dispatch.Ticket(t.Context(), "CND-1")
	if err != nil {
		t.Fatal(err)
	}
	return tk.Rev
}

// edit edits CND-1 as the operator with set.
func (l *launchTest) edit(t *testing.T, set map[string]any) {
	t.Helper()
	l.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": set}, http.StatusOK, "")
}

var everyGrant = []string{brakeman.ActLaunchApprovedPlan, brakeman.ActPrepareFollowup}

// TestAnUngrantedActIsRefused: an operation the turn is not granted is
// refused before anything is kept, and one granted after the turn began
// waits for the next turn (the snapshot intersected with now).
func TestAnUngrantedActIsRefused(t *testing.T) {
	f := newActFixture(t, []string{brakeman.ActPrepareFollowup}, nil)
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusForbidden || codeOf(out) != "not_granted" {
		t.Fatalf("an ungranted launch: %d %v", status, out)
	}
	// Granted now, but not in the snapshot the turn began under.
	f.bk.configure(t, func(c map[string]any) {
		c["permissions"].(map[string]any)["act"] = []any{brakeman.ActLaunchApprovedPlan, brakeman.ActPrepareFollowup}
	})
	if status, out := f.launchAct(t, map[string]any{"opId": "a-2"}); status != http.StatusForbidden || codeOf(out) != "not_granted" {
		t.Fatalf("a grant made after the turn began: %d %v", status, out)
	}
	if acts, launches := f.acts(t), f.launches(t); len(acts) != 0 || len(launches) != 0 {
		t.Fatalf("acts %v, launches %v", acts, launches)
	}
	// A grant taken away holds at once.
	f.endTurn(t)
	f.turn(t, []string{brakeman.ActLaunchApprovedPlan}, nil)
	f.bk.configure(t, func(c map[string]any) { c["permissions"].(map[string]any)["act"] = []any{} })
	if status, out := f.launchAct(t, map[string]any{"opId": "a-3"}); status != http.StatusForbidden || codeOf(out) != "not_granted" {
		t.Fatalf("a grant taken away: %d %v", status, out)
	}
	// The grants this build cannot serve are refused at the save.
	for _, op := range []string{brakeman.ActRequestRepair, brakeman.ActRouteDecision} {
		c := f.bk.config(t)
		c["permissions"].(map[string]any)["act"] = []any{op}
		f.expect(t, "PUT", "/api/brakeman/config", map[string]any{"rev": c["rev"], "config": c}, http.StatusBadRequest, brakeman.CodeInvalidConfig)
	}
}

// endTurn stops the turn's run and waits for the slot.
func (f *actFixture) endTurn(t *testing.T) {
	t.Helper()
	f.expect(t, "POST", "/api/brakeman/requests/"+f.req+"/cancel", map[string]any{"opId": opID()}, http.StatusOK, "")
	f.bk.waitState(t, f.req, brakeman.StateCancelled)
	f.bk.slotFree(t)
}

// TestAGrantGivesNoRead: granted to launch, with Dispatch not visible, the
// turn reads no ticket and its act on one is refused as not visible; with
// the scope narrowed to another project, outside its scope.
func TestAGrantGivesNoRead(t *testing.T) {
	f := newActFixture(t, everyGrant, func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = false })
	if status, out := f.asTurn(t, http.MethodGet, "/api/dispatch/tickets/CND-1", nil); status != http.StatusForbidden || codeOf(out) != "not_visible" {
		t.Fatalf("the read: %d %v", status, out)
	}
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusForbidden || codeOf(out) != "not_visible" {
		t.Fatalf("the act: %d %v", status, out)
	}
	f.endTurn(t)
	f.turn(t, everyGrant, func(c map[string]any) {
		c["visibility"].(map[string]any)["dispatch"] = true
		c["scope"] = map[string]any{"projects": []any{"OTH"}}
	})
	if status, out := f.launchAct(t, map[string]any{"opId": "a-2"}); status != http.StatusForbidden || codeOf(out) != "outside_scope" {
		t.Fatalf("an act outside the scope: %d %v", status, out)
	}
	if len(f.launches(t)) != 0 {
		t.Fatal("a launch was made")
	}
}

// TestAReadGivesNoGrant: with every read granted and no grant to act,
// every granted operation's route refuses the turn, and the turn's token
// without its session named is no principal of Brakeman's there at all.
func TestAReadGivesNoGrant(t *testing.T) {
	f := newActFixture(t, nil, func(c map[string]any) { c["visibility"].(map[string]any)["terminals"] = true })
	if status, out := f.asTurn(t, http.MethodGet, "/api/dispatch/tickets/CND-1/plans", nil); status != http.StatusOK {
		t.Fatalf("the read: %d %v", status, out)
	}
	for pattern := range actRoutes {
		path := strings.Replace(strings.TrimPrefix(pattern, "POST "), "{key}", "CND-1", 1)
		body := map[string]any{"opId": "a-1", "plan": f.plan, "basedOn": f.plan, "crew": f.crew.ID}
		if strings.HasSuffix(path, "/launch") {
			delete(body, "basedOn")
			delete(body, "crew")
		} else {
			delete(body, "plan")
		}
		if status, out := f.asTurn(t, http.MethodPost, path, body); status != http.StatusForbidden || codeOf(out) != "not_granted" {
			t.Fatalf("%s: %d %v", pattern, status, out)
		}
		if status, _, _ := f.callAs(t, f.tok, http.MethodPost, path, body); status != http.StatusUnauthorized {
			t.Fatalf("%s with the token alone: %d", pattern, status)
		}
	}
	if len(f.acts(t)) != 0 || len(f.launches(t)) != 0 {
		t.Fatal("an act was kept")
	}
}

// TestAnActRunsTheOperationsOwnFunction: a granted launch is DS2's launch,
// answered as the operator's route answers it, its receipt kept under the
// actor brakeman and the derived op id and copied into the request; a
// granted follow-up is DS2's proposal, which the operator still approves.
func TestAnActRunsTheOperationsOwnFunction(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	status, out := f.launchAct(t, map[string]any{"opId": "a-1", "expectedRev": f.ticketRev(t)})
	if status != http.StatusCreated {
		t.Fatalf("the act: %d %v", status, out)
	}
	admin := f.launch(t, f.testEnv, map[string]any{"byName": "Nate"}, http.StatusCreated, "")
	if !sameKeys(out, admin) || !sameKeys(receiptOf(t, out), receiptOf(t, admin)) || !sameKeys(entityOf(t, out, "launch"), entityOf(t, admin, "launch")) {
		t.Fatalf("the act's answer\n%v\nis not the route's\n%v", out, admin)
	}
	rc, launch := receiptOf(t, out), entityOf(t, out, "launch")
	if rc["status"] != dispatch.StatusDone || rc["intent"] != dispatch.IntentTicketLaunch || rc["id"] != launch["id"] || launch["state"] != dispatch.LaunchStarted || entityOf(t, out, "run")["id"] != launch["runId"] {
		t.Fatalf("the act's launch: %v", out)
	}
	op := actOp(f.req, "a-1")
	kept, found, err := f.srv.dispatch.Receipt(t.Context(), brakemanActor, op)
	if err != nil || !found || kept.ID != launch["id"] || kept.OpID != op {
		t.Fatalf("Dispatch's receipt under brakeman: %+v %v %v", kept, found, err)
	}
	if _, found, _ := f.srv.dispatch.Receipt(t.Context(), dispatch.ActorAdmin, op); found {
		t.Fatal("the act's receipt is the operator's")
	}
	if v := f.launchOf(t, launch["id"].(string)); v.CreatedBy != brakemanBy {
		t.Fatalf("the launch was made by %q", v.CreatedBy)
	}
	acts := f.acts(t)
	if len(acts) != 1 || acts[0].State != brakeman.ActDone || acts[0].Receipt != launch["id"] || acts[0].Op != op || acts[0].Operation != brakeman.ActLaunchApprovedPlan ||
		acts[0].Target.String() != "ticket:CND-1" || len(acts[0].Args) != 64 {
		t.Fatalf("the act kept: %+v", acts)
	}

	// The follow-up: a proposal based on the approved revision, kept
	// proposed for the operator to approve.
	status, out = f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "f-1", "crew": f.crew.ID, "basedOn": f.plan, "note": "the next round"})
	if status != http.StatusCreated {
		t.Fatalf("the follow-up: %d %v", status, out)
	}
	plan := entityOf(t, out, "plan")
	if plan["state"] != dispatch.PlanProposed || plan["basedOn"] != f.plan || plan["createdBy"] != brakemanBy || receiptOf(t, out)["id"] != plan["id"] {
		t.Fatalf("the follow-up's revision: %v", plan)
	}
	if acts := f.acts(t); len(acts) != 2 || acts[1].State != brakeman.ActDone || acts[1].Receipt != plan["id"] || acts[1].Operation != brakeman.ActPrepareFollowup {
		t.Fatalf("the acts kept: %+v", acts)
	}
}

// sameKeys reports whether two JSON objects have the same keys.
func sameKeys(a, b map[string]any) bool {
	ka, kb := make([]string, 0, len(a)), make([]string, 0, len(b))
	for k := range a {
		ka = append(ka, k)
	}
	for k := range b {
		kb = append(kb, k)
	}
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}

// TestAnActIsKeptOnce: the same act again answers the launch it made,
// with the same receipt: one launch, one act.
func TestAnActIsKeptOnce(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	status, first := f.launchAct(t, map[string]any{"opId": "a-1"})
	if status != http.StatusCreated {
		t.Fatalf("the act: %d %v", status, first)
	}
	status, again := f.launchAct(t, map[string]any{"opId": "a-1"})
	if status != http.StatusOK || receiptOf(t, again)["id"] != receiptOf(t, first)["id"] || receiptOf(t, again)["repeat"] != true {
		t.Fatalf("the act again: %d %v", status, again)
	}
	if acts, launches := f.acts(t), f.launches(t); len(acts) != 1 || len(launches) != 1 {
		t.Fatalf("acts %+v, launches %+v", acts, launches)
	}
}

// TestAStaleTargetIsRefused: a ticket that moved since the turn read it,
// a revision only proposed, and one whose approval no longer holds are
// refused before anything is kept.
func TestAStaleTargetIsRefused(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	rev := f.ticketRev(t)
	f.edit(t, map[string]any{"priority": 2})
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1", "expectedRev": rev}); status != http.StatusConflict || codeOf(out) != dispatch.RefusalStaleRevision {
		t.Fatalf("a ticket that moved: %d %v", status, out)
	}
	proposed := f.proposed(t, f.crew.ID, nil)
	if status, out := f.launchAct(t, map[string]any{"opId": "a-2", "plan": proposed}); status != http.StatusConflict || codeOf(out) != "stale_target" {
		t.Fatalf("a proposed revision: %d %v", status, out)
	}
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "a-3", "crew": f.crew.ID, "basedOn": proposed}); status != http.StatusConflict || codeOf(out) != "stale_target" {
		t.Fatalf("a follow-up of a proposed revision: %d %v", status, out)
	}
	f.edit(t, map[string]any{"description": "It loops twice."})
	if status, out := f.launchAct(t, map[string]any{"opId": "a-4"}); status != http.StatusConflict || codeOf(out) != "stale_target" {
		t.Fatalf("an approval that no longer holds: %d %v", status, out)
	}
	if status, out := f.launchAct(t, map[string]any{"opId": "a-5", "plan": "0123456789abcdef"}); status != http.StatusNotFound {
		t.Fatalf("a revision that is not there: %d %v", status, out)
	}
	if acts, launches := f.acts(t), f.launches(t); len(acts) != 0 || len(launches) != 0 {
		t.Fatalf("acts %+v, launches %+v", acts, launches)
	}
}

// TestAnActRetryAfterTheTargetMovedAnswersItsReceipt: once the act ran,
// its retry is answered from the operation's receipt though the ticket
// moved since (by the act's own launch or the operator).
func TestAnActRetryAfterTheTargetMovedAnswersItsReceipt(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	body := map[string]any{"opId": "a-1", "expectedRev": f.ticketRev(t)}
	status, first := f.launchAct(t, body)
	if status != http.StatusCreated {
		t.Fatalf("the act: %d %v", status, first)
	}
	f.edit(t, map[string]any{"description": "It loops twice."})
	status, again := f.launchAct(t, map[string]any{"opId": "a-1", "expectedRev": body["expectedRev"]})
	if status != http.StatusOK || receiptOf(t, again)["id"] != receiptOf(t, first)["id"] {
		t.Fatalf("the retry: %d %v", status, again)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActDone {
		t.Fatalf("acts %+v", acts)
	}
}

// holdActAt has the act stop at stage, as a server that died there.
func holdActAt(t *testing.T, stage string) {
	t.Helper()
	f := func(at, _, _ string) error {
		if at == stage {
			return errors.New("the server stopped")
		}
		return nil
	}
	actSeam.Store(&f)
	t.Cleanup(func() { actSeam.Store(nil) })
}

// TestAnActCommittedBeforeItsCopyIsCompletedAfterARestart: an act whose
// operation committed and whose outcome was never copied is completed by
// the desk's recovery from Dispatch's receipt for (brakeman, op).
func TestAnActCommittedBeforeItsCopyIsCompletedAfterARestart(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	holdActAt(t, "committed")
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusServiceUnavailable {
		t.Fatalf("the act: %d %v", status, out)
	}
	actSeam.Store(nil)
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActPending {
		t.Fatalf("acts before the recovery %+v", acts)
	}
	launches := f.launches(t)
	if len(launches) != 1 {
		t.Fatalf("launches %+v", launches)
	}
	f.endTurn(t)
	f.bk.desk.recoverMakes(t.Context())
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActDone || acts[0].Receipt != launches[0].ID {
		t.Fatalf("acts after the recovery %+v", acts)
	}
}

// TestAnActThatNeverRanIsMarkedNotRun: an act written pending whose
// operation never ran is marked not_run by the recovery, never run by
// it, and its op id is refused after.
func TestAnActThatNeverRanIsMarkedNotRun(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	holdActAt(t, "pending")
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusServiceUnavailable {
		t.Fatalf("the act: %d %v", status, out)
	}
	actSeam.Store(nil)
	f.bk.desk.recoverMakes(t.Context())
	acts := f.acts(t)
	if len(acts) != 1 || acts[0].State != brakeman.ActNotRun || acts[0].Why != "the server stopped before it ran" {
		t.Fatalf("acts after the recovery %+v", acts)
	}
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusConflict || codeOf(out) != "act_not_run" {
		t.Fatalf("the act again: %d %v", status, out)
	}
	if n := len(f.launches(t)); n != 0 {
		t.Fatalf("%d launches", n)
	}
}

// TestARetryWithNoReceiptIsCheckedAgain: an act left pending with no
// receipt in its operation's store (the server failed before the
// operation committed) is held to the checks again when it is sent again:
// with its grant taken away meanwhile, refused, nothing launched; granted
// again, it runs.
func TestARetryWithNoReceiptIsCheckedAgain(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	holdActAt(t, "pending")
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusServiceUnavailable {
		t.Fatalf("the act: %d %v", status, out)
	}
	actSeam.Store(nil)
	f.bk.configure(t, func(c map[string]any) { c["permissions"].(map[string]any)["act"] = []any{brakeman.ActPrepareFollowup} })
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusForbidden || codeOf(out) != "not_granted" {
		t.Fatalf("the retry with its grant taken away: %d %v", status, out)
	}
	if n := len(f.launches(t)); n != 0 {
		t.Fatalf("%d launches", n)
	}
	f.bk.configure(t, func(c map[string]any) {
		c["permissions"].(map[string]any)["act"] = []any{brakeman.ActLaunchApprovedPlan}
	})
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusCreated {
		t.Fatalf("the retry granted again: %d %v", status, out)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActDone {
		t.Fatalf("acts %+v", acts)
	}
}

// TestAnActsOpIDNamesOneAct: the same op id with other arguments is
// refused, and with another operation too.
func TestAnActsOpIDNamesOneAct(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1", "label": "first"}); status != http.StatusCreated {
		t.Fatalf("the act: %d %v", status, out)
	}
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1", "label": "second"}); status != http.StatusConflict || codeOf(out) != "op_conflict" {
		t.Fatalf("other arguments: %d %v", status, out)
	}
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "a-1", "crew": f.crew.ID, "basedOn": f.plan}); status != http.StatusConflict || codeOf(out) != "op_conflict" {
		t.Fatalf("another operation: %d %v", status, out)
	}
	if acts, launches := f.acts(t), f.launches(t); len(acts) != 1 || len(launches) != 1 {
		t.Fatalf("acts %+v, launches %+v", acts, launches)
	}
}

// TestNoGrantApprovesAnswersOrAccepts: granted every operation it can be,
// the turn approves no revision, withdraws none, accepts, closes or
// deletes no ticket and changes no configuration; and the follow-up it
// proposes waits for the operator's approval.
func TestNoGrantApprovesAnswersOrAccepts(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	proposed := f.proposed(t, f.crew.ID, nil)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/dispatch/plans/" + proposed + "/approve"},
		{"POST", "/api/dispatch/plans/" + f.plan + "/withdraw"},
		{"POST", "/api/dispatch/tickets/CND-1/accept"},
		{"POST", "/api/dispatch/tickets/CND-1/delete"},
		{"POST", "/api/dispatch/tickets/CND-1/edit"},
		{"PUT", "/api/brakeman/config"},
	} {
		if status, out := f.asTurn(t, c.method, c.path, map[string]any{"opId": opID(), "note": "Go.", "set": map[string]any{"status": "done"}}); status != http.StatusUnauthorized {
			t.Errorf("%s %s: %d %v", c.method, c.path, status, out)
		}
	}
	v, err := f.srv.dispatch.Plan(t.Context(), proposed)
	if err != nil || v.State != dispatch.PlanProposed {
		t.Fatalf("the revision: %+v %v", v.PlanSummary, err)
	}
	if tk, err := f.srv.dispatch.Ticket(t.Context(), "CND-1"); err != nil || tk.Status == "done" || !tk.DeletedAt.IsZero() {
		t.Fatalf("the ticket: %+v %v", tk.Ticket, err)
	}
}

// TestBrakemanCannotGrantItself: every route of the mux, sent as the turn
// with a body granting it every operation, leaves the grants as the
// person saved them.
func TestBrakemanCannotGrantItself(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	before := b.config(t)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	b.waitState(t, id, brakeman.StateRunning)
	grant := map[string]any{"permissions": map[string]any{"act": brakeman.Grantable}}
	body := map[string]any{"opId": "self", "rev": before["rev"], "config": grant, "permissions": grant["permissions"]}
	wild := strings.NewReplacer("{id}", sid, "{run}", "x", "{name}", "x", "{task}", "x", "{key}", "CND-1", "{token}", "x", "{linkId}", "x", "{command}", "x", "{n}", "1", "{k}", "1", "{i}", "0", "{path...}", "x")
	for _, pattern := range muxPatterns(t, "/api/") {
		method, path, _ := strings.Cut(pattern, " ")
		real := strings.NewReplacer("{", "", "}", "").Replace(wild.Replace(path))
		var send any
		if method != http.MethodGet {
			send = body
		}
		req, err := http.NewRequest(method, b.http.URL+real, bytes.NewReader(encodeNoHTML(t, send)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set(brakemanSessionHeader, sid)
		req.Header.Set("Content-Type", "application/json")
		resp, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	after := b.config(t)
	if act, _ := after["permissions"].(map[string]any)["act"].([]any); after["rev"] != before["rev"] || len(act) != 0 {
		t.Fatalf("the configuration after the walk: %v", after)
	}
	if !slices.Equal(b.desk.mustConfig(t).Permissions.Act, nil) {
		t.Fatalf("the grants kept: %v", b.desk.mustConfig(t).Permissions.Act)
	}
}

// The launch through ext.Host (LaunchApproved): DS2's launch, under the
// provider's principal's actor, or brakeman's on the desk's Host.

// TestLaunchApprovedRefusesAnUnapprovedRevision: a revision only proposed
// is refused with the store's words, one that is not there is not found,
// and nothing launches.
func TestLaunchApprovedRefusesAnUnapprovedRevision(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	proposed := l.proposed(t, l.crew.ID, nil)
	h := testHost(l.srv, t.Context(), "cloud")
	_, err := h.LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: proposed, OpID: "p-1", By: ada})
	var ae *apiError
	if !errors.Is(err, ext.ErrRefused) || !errors.As(err, &ae) || ae.Code != dispatch.RefusalInvalid || !strings.Contains(err.Error(), "approve it first") {
		t.Fatalf("an unapproved revision: %v", err)
	}
	if _, err := h.LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: "0123456789abcdef", OpID: "p-2", By: ada}); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("a revision that is not there: %v", err)
	}
	if _, err := h.LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: "nope", OpID: "p-3", By: ada}); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a request past its bounds: %v", err)
	}
	if n := len(l.launches(t)); n != 0 {
		t.Fatalf("%d launches", n)
	}
}

// TestLaunchApprovedRetryAfterALostReplyIsOneRun: the same OpID through
// the Host twice is one launch and one run, the same answered twice; the
// operation kept under the provider's principal's actor, and on the
// desk's Host under brakeman's.
func TestLaunchApprovedRetryAfterALostReplyIsOneRun(t *testing.T) {
	l := newLaunchTest(t, nil, nil, bkAgents()...)
	r := ext.ApprovedLaunch{Ticket: "CND-1", Plan: l.plan, OpID: "p-1", Label: "nightly", By: ada}
	h := testHost(l.srv, t.Context(), "cloud")
	first, err := h.LaunchApproved(t.Context(), r)
	if err != nil || first.ID == "" || !strings.HasSuffix(first.URL, "/runs/"+first.ID) {
		t.Fatalf("the launch: %+v %v", first, err)
	}
	again, err := h.LaunchApproved(t.Context(), r)
	if err != nil || again != first {
		t.Fatalf("the launch again: %+v %v, want %+v", again, err, first)
	}
	launches := l.launches(t)
	if len(launches) != 1 || launches[0].RunID != first.ID {
		t.Fatalf("launches %+v", launches)
	}
	if rc, found, err := l.srv.dispatch.Receipt(t.Context(), ext.SubjectActor+ada.Subject, "p-1"); err != nil || !found || rc.ID != launches[0].ID {
		t.Fatalf("the receipt under the provider's actor: %+v %v %v", rc, found, err)
	}
	// The desk's Host keeps it under brakeman.
	desk := l.srv.BrakemanHost()
	r.OpID = "p-2"
	if _, err := desk.LaunchApproved(t.Context(), r); err != nil {
		t.Fatalf("the desk's launch: %v", err)
	}
	if _, found, err := l.srv.dispatch.Receipt(t.Context(), brakemanActor, "p-2"); err != nil || !found {
		t.Fatalf("the desk's receipt: %v %v", found, err)
	}
}

// TestLaunchApprovedCarriesItsExpectedRev: a ticket that moved since the
// caller read it is refused (stale_revision), nothing launched; read
// again, it launches.
func TestLaunchApprovedCarriesItsExpectedRev(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	tk, err := l.srv.dispatch.Ticket(t.Context(), "CND-1")
	if err != nil {
		t.Fatal(err)
	}
	l.edit(t, map[string]any{"priority": 2})
	h := testHost(l.srv, t.Context(), "cloud")
	_, err = h.LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: l.plan, ExpectedRev: tk.Rev, OpID: "p-1", By: ada})
	var ae *apiError
	if !errors.Is(err, ext.ErrRefused) || !errors.As(err, &ae) || ae.Code != dispatch.RefusalStaleRevision {
		t.Fatalf("a moved ticket: %v", err)
	}
	if n := len(l.launches(t)); n != 0 {
		t.Fatalf("%d launches", n)
	}
	now, _ := l.srv.dispatch.Ticket(t.Context(), "CND-1")
	if _, err := h.LaunchApproved(context.Background(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: l.plan, ExpectedRev: now.Rev, OpID: "p-2", By: ada}); err != nil {
		t.Fatalf("read again: %v", err)
	}
}
