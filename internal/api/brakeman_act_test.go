package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
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
	// The route's answer, but its run, which the turn reads through its
	// own grant (GET /api/runs/{run}).
	admin := f.launch(t, f.testEnv, map[string]any{"byName": "Nate"}, http.StatusCreated, "")
	delete(admin, "run")
	// Beside it, the ref a brief cites the act's receipt by
	// (TestAnActsReceiptRefIsCited).
	if out["receiptRef"] != "op:brakeman/"+actOp(f.req, "a-1") {
		t.Fatalf("the act's receiptRef: %v", out)
	}
	delete(out, "receiptRef")
	if !sameKeys(out, admin) || !sameKeys(receiptOf(t, out), receiptOf(t, admin)) || !sameKeys(entityOf(t, out, "launch"), entityOf(t, admin, "launch")) {
		t.Fatalf("the act's answer\n%v\nis not the route's\n%v", out, admin)
	}
	rc, launch := receiptOf(t, out), entityOf(t, out, "launch")
	if rc["status"] != dispatch.StatusDone || rc["intent"] != dispatch.IntentTicketLaunch || rc["id"] != launch["id"] || launch["state"] != dispatch.LaunchStarted || launch["runId"] == "" {
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

// TestLaunchApprovedHonoursAGivenUpTry: a provider's try given up while
// its launch waits before the store keeps it launches nothing and keeps
// no receipt (context.Canceled); a later try's launch under the same OpID
// launches once.
func TestLaunchApprovedHonoursAGivenUpTry(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	held, letGo := make(chan struct{}), make(chan struct{})
	var once sync.Once
	l.srv.launches.setSeams(func(g *launchGate) {
		g.beforeStore = func(string) {
			once.Do(func() {
				close(held)
				<-letGo
			})
		}
	})
	try, giveUp := context.WithCancel(context.Background())
	h := testHost(l.srv, try, "cloud")
	r := ext.ApprovedLaunch{Ticket: "CND-1", Plan: l.plan, OpID: "p-1", By: ada}
	answered := make(chan error, 1)
	go func() {
		_, err := h.LaunchApproved(t.Context(), r)
		answered <- err
	}()
	<-held
	giveUp()
	close(letGo)
	if err := <-answered; !errors.Is(err, context.Canceled) {
		t.Fatalf("a launch whose try was given up as it waited: %v", err)
	}
	if n := len(l.launches(t)); n != 0 {
		t.Fatalf("%d launches", n)
	}
	if _, found, err := l.srv.dispatch.Receipt(t.Context(), ext.SubjectActor+ada.Subject, "p-1"); err != nil || found {
		t.Fatalf("a receipt kept: %v %v", found, err)
	}
	next := testHost(l.srv, t.Context(), "cloud")
	if ref, err := next.LaunchApproved(t.Context(), r); err != nil || ref.ID == "" {
		t.Fatalf("the next try's launch: %+v %v", ref, err)
	}
	if n := len(l.launches(t)); n != 1 {
		t.Fatalf("%d launches", n)
	}
}

// atAct runs fn at each stage of an act (actSeam) for the rest of the
// test.
func atAct(t *testing.T, fn func(stage, request, opID string) error) {
	t.Helper()
	actSeam.Store(&fn)
	t.Cleanup(func() { actSeam.Store(nil) })
}

// pruneActReceipt deletes the act's receipt from Dispatch's database, as
// its prune would.
func (f *actFixture) pruneActReceipt(t *testing.T, opID string) {
	t.Helper()
	if err := pruneReceipt(f.srv.dispatch, actOp(f.req, opID)); err != nil {
		t.Fatal(err)
	}
}

// pruneReceipt deletes Brakeman's receipt of the operation op from the
// database of the store ds, as its prune would: an error, never a
// failure of the test, so a seam may call it.
func pruneReceipt(ds *dispatch.Store, op string) error {
	db, err := sql.Open("sqlite", filepath.Join(ds.Dir(), dispatch.File))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA busy_timeout = 10000"); err != nil {
		return err
	}
	res, err := db.Exec("DELETE FROM ops WHERE actor = ? AND id = ?", brakemanActor, op)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%d receipts pruned", n)
	}
	return nil
}

// TestARefusedActRetriedIsPendingBeforeItRuns: an act refused before
// Dispatch kept a receipt (a base that names no commit yet), sent again
// once it would pass, is written pending before its operation runs: a
// server that stops after the operation committed and before the copy
// leaves it pending, and the recovery completes it from the receipt.
func TestARefusedActRetriedIsPendingBeforeItRuns(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	body := map[string]any{"opId": "f-1", "crew": f.crew.ID, "basedOn": f.plan, "base": "later"}
	status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", body)
	if status != http.StatusConflict || codeOf(out) != dispatch.RefusalInvalid {
		t.Fatalf("a base that names nothing yet: %d %v", status, out)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActRefused || acts[0].Receipt != "" {
		t.Fatalf("the refused act %+v", acts)
	}
	gitIn(t, f.repo, "branch", "later")
	var seen []brakeman.Act
	atAct(t, func(stage, _, _ string) error {
		if stage == "committed" {
			seen = f.acts(t)
			return errors.New("the server stopped")
		}
		return nil
	})
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", body); status != http.StatusServiceUnavailable {
		t.Fatalf("the retry: %d %v", status, out)
	}
	actSeam.Store(nil)
	if len(seen) != 1 || seen[0].State != brakeman.ActPending {
		t.Fatalf("the act as its operation committed %+v", seen)
	}
	rc, found, err := f.srv.dispatch.Receipt(t.Context(), brakemanActor, actOp(f.req, "f-1"))
	if err != nil || !found || rc.Status != dispatch.StatusDone {
		t.Fatalf("the retry's receipt: %+v %v %v", rc, found, err)
	}
	f.endTurn(t)
	f.bk.desk.recoverMakes(t.Context())
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActDone || acts[0].Receipt != rc.ID {
		t.Fatalf("acts after the recovery %+v", acts)
	}
}

// TestAnActsOutcomeCopiedAfterItsTurnIsSent: an act whose operation
// commits after its turn's brief is kept and its slot given up sends the
// request's brakeman event once its outcome is copied, though the
// request's state does not change: a page holding the brief reads the act
// done, never pending for as long as it stays open.
func TestAnActsOutcomeCopiedAfterItsTurnIsSent(t *testing.T) {
	l := newLaunchTest(t, nil, nil, bkAgents()...)
	f := &actFixture{launchTest: l, bk: bkEnvOf(t, l.testEnv)}
	c := f.bk.fakeClock()
	f.turn(t, everyGrant, nil)
	run := f.bk.runOf(t, f.sid)
	events := f.bk.sse(t)
	reached, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	atAct(t, func(stage, _, opID string) error {
		if stage == "committed" && opID == "a-1" {
			close(reached)
			<-gate
		}
		return nil
	})
	type answer struct {
		status int
		out    map[string]any
	}
	acted := make(chan answer, 1)
	go func() {
		status, out := f.launchAct(t, map[string]any{"opId": "a-1"})
		acted <- answer{status, out}
	}()
	select {
	case <-reached:
	case <-time.After(60 * time.Second):
		t.Fatal("the act's operation never committed")
	}
	// The launch has committed and its outcome is not yet copied: the
	// brief is kept, then the grace passes and the slot is given up, each
	// sending the request's event.
	if status, out := f.bk.submit(t, f.sid, f.tok, "b1", factBrief("session:"+f.sid)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
	ofRequest := func(ev string) bool {
		data, ok := strings.CutPrefix(ev, "brakeman ")
		var e brakemanEvent
		return ok && json.Unmarshal([]byte(data), &e) == nil && e.ID == f.req
	}
	f.bk.waitEvent(t, events, ofRequest)
	c.add(brakemanGrace + time.Second)
	f.bk.settle(t)
	f.bk.runStopped(t, run)
	f.bk.slotFree(t)
	f.bk.waitEvent(t, events, ofRequest)
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActPending {
		t.Fatalf("the act before its outcome is copied %+v", acts)
	}
	// The outcome copied, before the act is answered: its event.
	open()
	if a := <-acted; a.status != http.StatusCreated {
		t.Fatalf("the act: %d %v", a.status, a.out)
	}
	f.bk.waitEvent(t, events, ofRequest)
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActDone || acts[0].Receipt == "" {
		t.Fatalf("the act once its event is sent %+v", acts)
	}
}

// TestAnActIsCheckedAgainAfterItWaits: an act that waited for the
// request's make mutex is checked against the configuration and the
// request as they are once it holds it: a grant taken away meanwhile
// refuses a new act, and a request cancelled meanwhile refuses a retry
// the store holds no receipt for; nothing runs either way.
func TestAnActIsCheckedAgainAfterItWaits(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	// While it waits, the grant to launch is taken away.
	atAct(t, func(stage, _, opID string) error {
		if stage == "waiting" && opID == "a-1" {
			f.bk.configure(t, func(c map[string]any) { c["permissions"].(map[string]any)["act"] = []any{brakeman.ActPrepareFollowup} })
		}
		return nil
	})
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusForbidden || codeOf(out) != "not_granted" {
		t.Fatalf("a grant taken away as the act waited: %d %v", status, out)
	}
	if acts, launches := f.acts(t), f.launches(t); len(acts) != 0 || len(launches) != 0 {
		t.Fatalf("acts %+v, launches %+v", acts, launches)
	}

	// A follow-up left pending with no receipt, sent again while the
	// request is cancelled.
	atAct(t, func(stage, _, _ string) error {
		if stage == "pending" {
			return errors.New("the server stopped")
		}
		return nil
	})
	body := map[string]any{"opId": "f-1", "crew": f.crew.ID, "basedOn": f.plan}
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", body); status != http.StatusServiceUnavailable {
		t.Fatalf("the follow-up: %d %v", status, out)
	}
	plans := func() int {
		ps, err := f.srv.dispatch.Plans(t.Context(), "CND-1")
		if err != nil {
			t.Fatal(err)
		}
		return len(ps)
	}
	before := plans()
	atAct(t, func(stage, _, _ string) error {
		if stage == "waiting" {
			f.expect(t, "POST", "/api/brakeman/requests/"+f.req+"/cancel", map[string]any{"opId": opID()}, http.StatusOK, "")
		}
		return nil
	})
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", body); status != http.StatusConflict || codeOf(out) != "turn_over" {
		t.Fatalf("a retry of a cancelled request: %d %v", status, out)
	}
	if n := plans(); n != before {
		t.Fatalf("%d revisions proposed", n-before)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActPending {
		t.Fatalf("acts %+v", acts)
	}
}

// TestReleaseWaitsForAnAct: an act held before its operation keeps
// Brakeman's owner lock through a release that has begun (a second server
// opening the records is read-only), a new act is refused 503
// shutting_down meanwhile, and the release returns once the act has.
func TestReleaseWaitsForAnAct(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	held, letGo := make(chan struct{}), make(chan struct{})
	var once, freed sync.Once
	free := func() { freed.Do(func() { close(letGo) }) }
	t.Cleanup(free)
	atAct(t, func(stage, _, opID string) error {
		if stage == "pending" && opID == "a-1" {
			once.Do(func() {
				close(held)
				<-letGo
			})
		}
		return nil
	})
	acted := make(chan int, 1)
	go func() {
		status, _ := f.launchAct(t, map[string]any{"opId": "a-1"})
		acted <- status
	}()
	<-held
	released := make(chan struct{})
	go func() {
		f.bk.desk.release(context.Background())
		close(released)
	}()
	if !waitFor(30*time.Second, f.bk.desk.closing.Load) {
		t.Fatal("the desk never began to close")
	}
	if status, out := f.launchAct(t, map[string]any{"opId": "a-2"}); status != http.StatusServiceUnavailable || codeOf(out) != "shutting_down" {
		t.Fatalf("an act sent as the server stops: %d %v", status, out)
	}
	st, err := store.Open(f.srv.store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := brakeman.Open(st, brakeman.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if ro, _ := other.ReadOnly(); !ro {
		t.Fatal("the owner lock went while an act was under way")
	}
	select {
	case <-released:
		t.Fatal("the release returned while an act was under way")
	default:
	}
	free()
	<-acted
	<-released
}

// TestAnActForwardsWhatItWasCheckedBy: a body whose keys differ only in
// case is decoded once, so the follow-up proposed is based on the
// revision admission checked, and an opId spelled twice cannot replace
// the derived key its receipt is kept under.
func TestAnActForwardsWhatItWasCheckedBy(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	proposed := f.proposed(t, f.crew.ID, nil)
	raw := json.RawMessage(`{"opId":"c-1","crew":"` + f.crew.ID + `","basedOn":"` + proposed + `","BASEDON":"` + f.plan + `"}`)
	status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", raw)
	if status != http.StatusCreated {
		t.Fatalf("the follow-up: %d %v", status, out)
	}
	if plan := entityOf(t, out, "plan"); plan["basedOn"] != f.plan {
		t.Fatalf("checked against %s, proposed on %v", f.plan, plan["basedOn"])
	}
	raw = json.RawMessage(`{"opId":"c-2","crew":"` + f.crew.ID + `","basedOn":"` + f.plan + `","opid":"c-3","note":"again"}`)
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", raw); status != http.StatusCreated {
		t.Fatalf("the second follow-up: %d %v", status, out)
	}
	acts := f.acts(t)
	if len(acts) != 2 {
		t.Fatalf("acts %+v", acts)
	}
	if _, found, err := f.srv.dispatch.Receipt(t.Context(), brakemanActor, acts[1].Op); err != nil || !found {
		t.Fatalf("no receipt under the act's derived key %s: %v %v", acts[1].Op, found, err)
	}
	for _, id := range []string{"c-2", "c-3"} {
		if _, found, _ := f.srv.dispatch.Receipt(t.Context(), brakemanActor, id); found {
			t.Fatalf("a receipt under the turn's own op id %s", id)
		}
	}
}

// TestAFollowupsBaseIsApprovedAsItCommits: a follow-up admitted while its
// base was approved, the base withdrawn before the proposal commits, is
// refused by Dispatch's own transaction (stale_plan), nothing proposed.
func TestAFollowupsBaseIsApprovedAsItCommits(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	atAct(t, func(stage, _, _ string) error {
		if stage == "pending" {
			rc, err := f.srv.dispatch.WithdrawPlan(context.Background(), dispatch.Op{Actor: dispatch.ActorAdmin, ID: opID(), By: "Nate"}, f.plan)
			if err != nil || rc.Status != dispatch.StatusDone {
				t.Errorf("the withdrawal: %+v %v", rc, err)
			}
		}
		return nil
	})
	before, err := f.srv.dispatch.Plans(t.Context(), "CND-1")
	if err != nil {
		t.Fatal(err)
	}
	status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "f-1", "crew": f.crew.ID, "basedOn": f.plan})
	if status != http.StatusConflict || codeOf(out) != dispatch.RefusalStalePlan {
		t.Fatalf("a base withdrawn before the commit: %d %v", status, out)
	}
	if after, err := f.srv.dispatch.Plans(t.Context(), "CND-1"); err != nil || len(after) != len(before) {
		t.Fatalf("%d revisions, %d before: %v", len(after), len(before), err)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActRefused || !strings.Contains(acts[0].Why, "is withdrawn") {
		t.Fatalf("acts %+v", acts)
	}
	if rc, found, err := f.srv.dispatch.Receipt(t.Context(), brakemanActor, actOp(f.req, "f-1")); err != nil || !found || rc.Refusal != dispatch.RefusalStalePlan {
		t.Fatalf("Dispatch's receipt: %+v %v %v", rc, found, err)
	}
}

// TestAnActsMakingIsHeldToTheScope: the run a launch makes and the crew a
// follow-up plans are held to the scope's crews and repositories as the
// ticket is. With the ticket in the scope through a run of its crew, an
// act of that crew runs and an act of another crew is refused
// outside_scope; with repositories named, an act whose crew's repository
// is not among them is. Nothing is made by a refusal.
func TestAnActsMakingIsHeldToTheScope(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	other := f.saveCrew(t, teamCrew("Other", "ready-cat"))
	// CND-1 is linked to a stopped run of the fixture's crew.
	run := f.crew.ID + "-0123abcd"
	crewJSON, _ := json.Marshal(map[string]string{"id": f.crew.ID, "cwd": f.repo})
	if _, err := f.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
		return tx.PutRun(ledger.RunRow{ID: run, CrewID: f.crew.ID, Crew: string(crewJSON), State: ledger.RunStopped, CreatedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.records.Save(crew.Run{ID: run, CrewID: f.crew.ID, Name: f.crew.ID, Cwd: f.repo, State: crew.RunStopped, Managed: &crew.Managed{Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	doneOf(t)(f.srv.dispatch.AddLink(t.Context(), adminOp("Ann"), "CND-1", dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run}, nil))
	f.endTurn(t)
	f.turn(t, everyGrant, func(c map[string]any) { c["scope"] = map[string]any{"crews": []any{f.crew.ID}} })
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusCreated {
		t.Fatalf("a launch of the crew in the scope: %d %v", status, out)
	}
	plans, err := f.srv.dispatch.Plans(t.Context(), "CND-1")
	if err != nil {
		t.Fatal(err)
	}
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "f-1", "crew": other.ID, "basedOn": f.plan}); status != http.StatusForbidden || codeOf(out) != "outside_scope" {
		t.Fatalf("a follow-up of a crew outside the scope: %d %v", status, out)
	}
	// A revision of the other crew, approved by the operator.
	theirs := f.proposed(t, other.ID, nil)
	f.expect(t, "POST", "/api/dispatch/plans/"+theirs+"/approve", map[string]any{"opId": opID(), "byName": "Nate", "note": "Go."}, http.StatusOK, "")
	launches := len(f.launches(t))
	if status, out := f.launchAct(t, map[string]any{"opId": "a-2", "plan": theirs}); status != http.StatusForbidden || codeOf(out) != "outside_scope" {
		t.Fatalf("a launch of a crew outside the scope: %d %v", status, out)
	}

	// The repositories: the crew's is not among them.
	f.endTurn(t)
	f.turn(t, everyGrant, func(c map[string]any) {
		c["scope"] = map[string]any{"repos": []any{filepath.Join(t.TempDir(), "elsewhere")}}
	})
	if status, out := f.launchAct(t, map[string]any{"opId": "a-3", "plan": theirs}); status != http.StatusForbidden || codeOf(out) != "outside_scope" {
		t.Fatalf("a launch outside the scope's repositories: %d %v", status, out)
	}
	if status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "f-2", "crew": f.crew.ID, "basedOn": theirs}); status != http.StatusForbidden || codeOf(out) != "outside_scope" {
		t.Fatalf("a follow-up outside the scope's repositories: %d %v", status, out)
	}
	if n := len(f.launches(t)); n != launches {
		t.Fatalf("%d launches made by refusals", n-launches)
	}
	now, err := f.srv.dispatch.Plans(t.Context(), "CND-1")
	if err != nil || len(now) != len(plans)+1 {
		t.Fatalf("%d revisions, %d before the refusals and one the operator's: %v", len(now), len(plans), err)
	}
	if acts := f.acts(t); len(acts) != 0 {
		t.Fatalf("acts kept by refusals %+v", acts)
	}
}

// TestACommittedActOutlivesItsPrunedReceipt: an act whose receipt Dispatch
// pruned is answered as committed, never checked again as new: its retry
// answers the launch it made though the ticket moved since, one launch;
// and one stopped before its copy is completed done by the recovery from
// the change its launch recorded.
func TestACommittedActOutlivesItsPrunedReceipt(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	body := map[string]any{"opId": "a-1", "expectedRev": f.ticketRev(t)}
	status, first := f.launchAct(t, body)
	if status != http.StatusCreated {
		t.Fatalf("the act: %d %v", status, first)
	}
	f.pruneActReceipt(t, "a-1")
	f.edit(t, map[string]any{"description": "It loops twice."})
	status, again := f.launchAct(t, map[string]any{"opId": "a-1", "expectedRev": body["expectedRev"]})
	if status != http.StatusOK || receiptOf(t, again)["id"] != receiptOf(t, first)["id"] {
		t.Fatalf("the retry after the prune: %d %v", status, again)
	}
	if acts, launches := f.acts(t), f.launches(t); len(acts) != 1 || acts[0].State != brakeman.ActDone || len(launches) != 1 {
		t.Fatalf("acts %+v, launches %+v", acts, launches)
	}

	// Stopped before the copy, its receipt pruned before the recovery.
	plan := f.approved(t, nil)
	holdActAt(t, "committed")
	if status, out := f.launchAct(t, map[string]any{"opId": "a-2", "plan": plan}); status != http.StatusServiceUnavailable {
		t.Fatalf("the act: %d %v", status, out)
	}
	actSeam.Store(nil)
	launches := f.launches(t)
	f.pruneActReceipt(t, "a-2")
	f.endTurn(t)
	f.bk.desk.recoverMakes(t.Context())
	acts := f.acts(t)
	if len(acts) != 2 {
		t.Fatalf("acts after the recovery %+v", acts)
	}
	made := slices.IndexFunc(launches, func(l dispatch.LaunchSummary) bool { return l.ID == acts[1].Receipt })
	if len(launches) != 2 || acts[1].State != brakeman.ActDone || acts[1].Receipt == acts[0].Receipt || made < 0 {
		t.Fatalf("acts after the recovery %+v, launches %+v", acts, launches)
	}
}

// TestAnActsWordsFitItsEntry: a refusal whose words JSON escapes past the
// room the entry has (a base of quotes, quoted again in the route's
// words) is copied refused with its words cut to fit, never left pending.
func TestAnActsWordsFitItsEntry(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "f-1", "crew": f.crew.ID, "basedOn": f.plan, "base": strings.Repeat(`"`, 200)})
	if status != http.StatusConflict || codeOf(out) != dispatch.RefusalInvalid {
		t.Fatalf("the follow-up: %d %v", status, out)
	}
	acts := f.acts(t)
	if len(acts) != 1 || acts[0].State != brakeman.ActRefused || acts[0].Why == "" || !strings.HasPrefix(acts[0].Why, "the base ") {
		t.Fatalf("acts %+v", acts)
	}
	if b, _ := json.Marshal(acts[0]); len(b) > brakeman.MaxActBytes {
		t.Fatalf("the entry is %d bytes", len(b))
	}
}

// TestAShutdownLetsNoActLaunchPastItsStops: an act held after it was
// written pending, before its operation, as the server begins to shut
// down runs nothing once let go (503 shutting_down; the next start marks
// it not_run), and one held inside its operation is waited for before
// the runs are stopped, so the run it launched is stopped with the rest.
func TestAShutdownLetsNoActLaunchPastItsStops(t *testing.T) {
	type answer struct {
		status int
		out    map[string]any
	}
	shutdown := func(f *actFixture) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			f.srv.Shutdown(ctx)
		}()
		if !waitFor(30*time.Second, f.bk.desk.closing.Load) {
			t.Fatal("the shutdown never quiesced the desk")
		}
		return done
	}
	stopped := func(f *actFixture) {
		for _, run := range f.srv.runs.List() {
			if run.StoppedAt == nil {
				t.Errorf("the run %s runs after the shutdown", run.ID)
			}
		}
	}

	t.Run("before its operation", func(t *testing.T) {
		f := newActFixture(t, everyGrant, nil)
		held, letGo := make(chan struct{}), make(chan struct{})
		var once, freed sync.Once
		free := func() { freed.Do(func() { close(letGo) }) }
		t.Cleanup(free)
		atAct(t, func(stage, _, opID string) error {
			if stage == "pending" && opID == "a-1" {
				once.Do(func() {
					close(held)
					<-letGo
				})
			}
			return nil
		})
		acted := make(chan answer, 1)
		go func() {
			status, out := f.launchAct(t, map[string]any{"opId": "a-1"})
			acted <- answer{status, out}
		}()
		<-held
		done := shutdown(f)
		free()
		a := <-acted
		<-done
		if a.status != http.StatusServiceUnavailable || codeOf(a.out) != "shutting_down" {
			t.Fatalf("an act let go as the server stops: %d %v", a.status, a.out)
		}
		stopped(f)
		again := f.restart()
		if list, err := again.srv.dispatch.Launches(t.Context(), "CND-1", 0); err != nil || len(list) != 0 {
			t.Fatalf("launches after the shutdown: %+v %v", list, err)
		}
		if !waitFor(30*time.Second, func() bool {
			r, err := again.srv.brakeman.st.Get(f.req)
			return err == nil && len(r.Acts) == 1 && r.Acts[0].State == brakeman.ActNotRun
		}) {
			r, _ := again.srv.brakeman.st.Get(f.req)
			t.Fatalf("the act after a restart: %+v", r.Acts)
		}
	})

	t.Run("inside its operation", func(t *testing.T) {
		f := newActFixture(t, everyGrant, nil)
		held, letGo := make(chan struct{}), make(chan struct{})
		var once, freed sync.Once
		free := func() { freed.Do(func() { close(letGo) }) }
		t.Cleanup(free)
		f.srv.launches.setSeams(func(g *launchGate) {
			g.beforePlanned = func(string) {
				once.Do(func() {
					close(held)
					<-letGo
				})
			}
		})
		acted := make(chan answer, 1)
		go func() {
			status, out := f.launchAct(t, map[string]any{"opId": "a-1"})
			acted <- answer{status, out}
		}()
		<-held
		done := shutdown(f)
		// The shutdown waits in the desk's quiesce: the webhooks, closed
		// next, are not.
		if f.srv.webhooks.ctx.Err() != nil {
			t.Fatal("the shutdown went past the desk while an act's operation was under way")
		}
		free()
		a := <-acted
		<-done
		if a.status != http.StatusCreated {
			t.Fatalf("the act under way as the server stops: %d %v", a.status, a.out)
		}
		runID, _ := entityOf(t, a.out, "launch")["runId"].(string)
		if !slices.ContainsFunc(f.srv.runs.List(), func(r crew.Run) bool { return r.ID == runID }) {
			t.Fatalf("the act's run %s is not the engine's", runID)
		}
		stopped(f)
	})

	// The shutdown's wait runs out while the act is held inside its
	// operation, before the engine is given its run; the act is let go
	// once the shutdown has stopped the runs it lists, and answered
	// before the sessions close and the ledger goes: it starts no run.
	t.Run("past the shutdown's wait", func(t *testing.T) {
		f := newActFixture(t, everyGrant, nil)
		held, letGo := make(chan struct{}), make(chan struct{})
		var once, freed sync.Once
		free := func() { freed.Do(func() { close(letGo) }) }
		t.Cleanup(free)
		f.srv.launches.setSeams(func(g *launchGate) {
			g.beforePlanned = func(string) {
				once.Do(func() {
					close(held)
					<-letGo
				})
			}
		})
		acted := make(chan answer, 1)
		go func() {
			status, out := f.launchAct(t, map[string]any{"opId": "a-1"})
			acted <- answer{status, out}
		}()
		<-held
		var late answer
		f.srv.launches.setSeams(func(g *launchGate) {
			g.afterStops = func() {
				free()
				late = <-acted
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			f.srv.Shutdown(ctx)
		}()
		if !waitFor(30*time.Second, f.bk.desk.closing.Load) {
			t.Fatal("the shutdown never quiesced the desk")
		}
		cancel()
		<-done
		if late.status != http.StatusConflict || codeOf(late.out) != "launch_failed" {
			t.Fatalf("an act let go past the shutdown's stops: %d %v", late.status, late.out)
		}
		if l := entityOf(t, late.out, "launch"); l["state"] != dispatch.LaunchFailed || !strings.Contains(l["why"].(string), "shutting down") {
			t.Fatalf("the act's launch: %v", l)
		}
		stopped(f)
		if runs := f.srv.runs.List(); slices.ContainsFunc(runs, func(r crew.Run) bool { return r.Ticket != nil }) {
			t.Fatalf("a run from the ticket after the shutdown: %+v", runs)
		}
	})
}

// TestAnActsAnswerIsReadThroughTheGrant: an act's answer gives the turn
// what its grant reads as it is answered: with the runs hidden, no run,
// the launch Dispatch keeps noted in its read log and counted as a read;
// sent again once Dispatch is hidden too, its receipt alone, nothing
// noted.
func TestAnActsAnswerIsReadThroughTheGrant(t *testing.T) {
	f := newActFixture(t, everyGrant, func(c map[string]any) { c["visibility"].(map[string]any)["runs"] = false })
	reads := func() (int, []brakeman.Read) {
		r, err := f.bk.desk.st.Get(f.req)
		if err != nil {
			t.Fatal(err)
		}
		log, err := f.bk.desk.st.Reads(f.req)
		if err != nil {
			t.Fatal(err)
		}
		return r.Reads, log.Reads
	}
	n0, log0 := reads()
	status, out := f.launchAct(t, map[string]any{"opId": "a-1"})
	if status != http.StatusCreated {
		t.Fatalf("the act: %d %v", status, out)
	}
	if _, ok := out["run"]; ok {
		t.Fatalf("a turn that may not see runs is given its run: %v", out)
	}
	launch := entityOf(t, out, "launch")
	n1, log1 := reads()
	if n1 != n0+1 || len(log1) != len(log0)+1 {
		t.Fatalf("the answer counted %d reads and noted %d", n1-n0, len(log1)-len(log0))
	}
	if rd := log1[len(log1)-1]; rd.Ref.String() != "launch:"+launch["id"].(string) || rd.Class != brakeman.ClassDispatch || rd.State != dispatch.LaunchStarted {
		t.Fatalf("the launch noted as %+v", rd)
	}
	f.bk.configure(t, func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = false })
	status, again := f.launchAct(t, map[string]any{"opId": "a-1"})
	if status != http.StatusOK || receiptOf(t, again)["id"] != launch["id"] || again["launch"] != nil || again["run"] != nil {
		t.Fatalf("the act again with Dispatch hidden: %d %v", status, again)
	}
	if n2, log2 := reads(); n2 != n1 || len(log2) != len(log1) {
		t.Fatalf("the receipt alone counted %d reads and noted %d", n2-n1, len(log2)-len(log1))
	}
}

// TestABriefFromAnActIsWithheldOnceItsSourceIsHidden: a turn that read
// nothing of Dispatch but an act's answer keeps dispatch in its brief's
// footprint, so the brief is withheld from a turn with Dispatch off.
func TestABriefFromAnActIsWithheldOnceItsSourceIsHidden(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	status, out := f.launchAct(t, map[string]any{"opId": "a-1"})
	if status != http.StatusCreated {
		t.Fatalf("the act: %d %v", status, out)
	}
	runID := entityOf(t, out, "launch")["runId"].(string)
	if status, out := f.bk.submit(t, f.sid, f.tok, "b-1", factBrief("run:"+runID)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
	early := f.req
	if r, _ := f.bk.desk.st.Get(early); r.Footprint == nil || !slices.Contains(r.Footprint.Classes, brakeman.ClassDispatch) {
		t.Fatalf("the brief's footprint: %+v", r.Footprint)
	}
	f.expect(t, "POST", "/api/runs/"+f.bk.runOf(t, f.sid)+"/stop", nil, http.StatusOK, "")
	f.bk.slotFree(t)
	f.turn(t, everyGrant, func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = false })
	status, briefs := f.asTurn(t, http.MethodGet, "/api/brakeman/briefs", nil)
	if status != http.StatusOK {
		t.Fatalf("the briefs: %d %v", status, briefs)
	}
	found := false
	for _, b := range briefs["briefs"].([]any) {
		if m := b.(map[string]any); m["id"] == early {
			found = true
			if whole(m) || m["withheld"] == nil {
				t.Fatalf("the brief from an act, with Dispatch off: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("the brief %s is not listed: %v", early, briefs)
	}
}

// TestAnActsRefusalInDispatchsWordsIsInTheFootprint: an act refused past
// the visibility check, in Dispatch's words (a revision only proposed),
// notes dispatch among the classes the turn's answers used; one refused
// as not visible does not.
func TestAnActsRefusalInDispatchsWordsIsInTheFootprint(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	proposed := f.proposed(t, f.crew.ID, nil)
	if status, out := f.launchAct(t, map[string]any{"opId": "a-1", "plan": proposed}); status != http.StatusConflict || codeOf(out) != "stale_target" {
		t.Fatalf("a revision only proposed: %d %v", status, out)
	}
	if used := f.bk.desk.classesUsed(f.req); !slices.Contains(used, brakeman.ClassDispatch) {
		t.Fatalf("the classes used: %v", used)
	}

	h := newActFixture(t, everyGrant, func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = false })
	if status, out := h.launchAct(t, map[string]any{"opId": "a-1"}); status != http.StatusForbidden || codeOf(out) != "not_visible" {
		t.Fatalf("Dispatch hidden: %d %v", status, out)
	}
	if used := h.bk.desk.classesUsed(h.req); slices.Contains(used, brakeman.ClassDispatch) {
		t.Fatalf("the classes used by a refusal that read nothing: %v", used)
	}
}

// TestALateActKeepsTheNextTurnsFootprint: an act held after its
// operation, its turn cancelled and the next turn begun meanwhile, is
// answered once that turn has read Dispatch's projects (a read that notes
// no ref): the next turn keeps dispatch among the classes its answers
// used, so its brief's footprint carries it.
func TestALateActKeepsTheNextTurnsFootprint(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	held, letGo := make(chan struct{}), make(chan struct{})
	var once, freed sync.Once
	free := func() { freed.Do(func() { close(letGo) }) }
	t.Cleanup(free)
	atAct(t, func(stage, _, opID string) error {
		if stage == "committed" && opID == "a-1" {
			once.Do(func() {
				close(held)
				<-letGo
			})
		}
		return nil
	})
	// The first turn as it is now: f is the next turn's once it begins.
	first := *f
	acted := make(chan int, 1)
	go func() {
		status, _ := first.launchAct(t, map[string]any{"opId": "a-1"})
		acted <- status
	}()
	<-held
	f.endTurn(t)
	f.turn(t, everyGrant, nil)
	if status, out := f.asTurn(t, http.MethodGet, "/api/dispatch/projects", nil); status != http.StatusOK {
		t.Fatalf("the next turn's read: %d %v", status, out)
	}
	if used := f.bk.desk.classesUsed(f.req); !slices.Contains(used, brakeman.ClassDispatch) {
		t.Fatalf("the next turn's classes before the late answer: %v", used)
	}
	free()
	<-acted
	if used := f.bk.desk.classesUsed(f.req); !slices.Contains(used, brakeman.ClassDispatch) {
		t.Fatalf("the next turn's classes after the late answer: %v", used)
	}
	if status, out := f.bk.submit(t, f.sid, f.tok, "b-1", factBrief("run:"+f.bk.runOf(t, f.sid))); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
	if r, _ := f.bk.desk.st.Get(f.req); r.Footprint == nil || !slices.Contains(r.Footprint.Classes, brakeman.ClassDispatch) {
		t.Fatalf("the next turn's footprint: %+v", r.Footprint)
	}
}

// TestAKeptRefusalIsAnsweredFromItsReceipt: an act Dispatch refused with
// its receipt kept (a follow-up whose base was superseded as it
// committed), sent again once its base is approved again, its receipt
// pruned after the act found it and before the operation would run, is
// answered by that refusal as found, nothing proposed: never run anew
// past its checks and its pending entry.
func TestAKeptRefusalIsAnsweredFromItsReceipt(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	other := f.proposed(t, f.crew.ID, nil)
	approve := func(id string) error {
		rc, err := f.srv.dispatch.ApprovePlan(context.Background(), dispatch.Op{Actor: dispatch.ActorAdmin, ID: opID(), By: "Nate"}, id, 0, "Go.")
		if err == nil && rc.Status != dispatch.StatusDone {
			err = fmt.Errorf("refused: %s", rc.Error)
		}
		return err
	}
	var sent atomic.Int32
	op := actOp(f.req, "f-1")
	atAct(t, func(stage, _, opID string) error {
		if stage != "pending" || opID != "f-1" {
			return nil
		}
		var err error
		if sent.Add(1) == 1 {
			// The base superseded as the follow-up commits.
			err = approve(other)
		} else {
			err = pruneReceipt(f.srv.dispatch, op)
		}
		if err != nil {
			t.Errorf("at the act's %d send: %v", sent.Load(), err)
		}
		return nil
	})
	body := map[string]any{"opId": "f-1", "crew": f.crew.ID, "basedOn": f.plan}
	status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", body)
	if status != http.StatusConflict || codeOf(out) != dispatch.RefusalStalePlan {
		t.Fatalf("a base superseded as it commits: %d %v", status, out)
	}
	refused := receiptOf(t, out)
	if err := approve(f.plan); err != nil {
		t.Fatalf("the base approved again: %v", err)
	}
	before, err := f.srv.dispatch.Plans(t.Context(), "CND-1")
	if err != nil {
		t.Fatal(err)
	}
	status, again := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", body)
	if status != http.StatusConflict || codeOf(again) != dispatch.RefusalStalePlan || receiptOf(t, again)["id"] != refused["id"] || receiptOf(t, again)["repeat"] != true {
		t.Fatalf("the refusal sent again, its receipt pruned: %d %v", status, again)
	}
	if sent.Load() != 2 {
		t.Fatalf("the act was sent %d times past its admission", sent.Load())
	}
	if after, err := f.srv.dispatch.Plans(t.Context(), "CND-1"); err != nil || len(after) != len(before) {
		t.Fatalf("%d revisions, %d before: %v", len(after), len(before), err)
	}
	if rc, found, err := f.srv.dispatch.Receipt(t.Context(), brakemanActor, op); err != nil || found {
		t.Fatalf("a receipt after the prune: %+v %v %v", rc, found, err)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActRefused || !strings.Contains(acts[0].Why, "superseded") {
		t.Fatalf("acts %+v", acts)
	}
}

// TestAnActsReceiptRefIsCited: an act's answer gives receiptRef, the ref
// a brief cites the act's receipt by (op:brakeman/<the receipt's opId>,
// the act's derived key), never the id of what it made (the launch's,
// which the receipt's id is); a brief whose outcome cites it is kept and
// the ref resolves. A retry answered from the receipt kept gives the same
// ref. The three spellings two real models tried (the actor's case, the
// turn's opId for the receipt's, the launch's id as an op) are refused
// (422 bad_refs), as before: the answer and the guide's ref forms now say
// which one cites it.
func TestAnActsReceiptRefIsCited(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	status, out := f.launchAct(t, map[string]any{"opId": "a-1"})
	if status != http.StatusCreated {
		t.Fatalf("the act: %d %v", status, out)
	}
	op := actOp(f.req, "a-1")
	ref, _ := out["receiptRef"].(string)
	launch := entityOf(t, out, "launch")["id"].(string)
	if ref != "op:brakeman/"+op || receiptOf(t, out)["opId"] != op || strings.Contains(ref, launch) {
		t.Fatalf("the act's receiptRef %q: %v", ref, out)
	}
	if _, again := f.launchAct(t, map[string]any{"opId": "a-1"}); again["receiptRef"] != ref {
		t.Fatalf("the act retried: %v", again)
	}
	outcome := func(receipt string) map[string]any {
		return map[string]any{"schema": brakeman.BriefSchema, "title": "Launched", "summary": "The approved revision was launched.",
			"items": []any{map[string]any{"kind": brakeman.ItemFact, "text": "CND-1's approved revision was launched.", "evidence": []any{"ticket:CND-1"}},
				map[string]any{"kind": brakeman.ItemOutcome, "text": "Brakeman launched CND-1's approved revision.", "receipt": receipt}}}
	}
	for i, wrong := range []string{"op:Brakeman/" + op, "op:brakeman/a-1", "op:brakeman/" + launch} {
		if status, out := f.bk.submit(t, f.sid, f.tok, fmt.Sprintf("w-%d", i), outcome(wrong)); status != http.StatusUnprocessableEntity || codeOf(out) != "bad_refs" {
			t.Fatalf("a brief citing %s: %d %v", wrong, status, out)
		}
	}
	if status, out := f.bk.submit(t, f.sid, f.tok, "b-1", outcome(ref)); status != http.StatusCreated {
		t.Fatalf("a brief citing %s: %d %v", ref, status, out)
	}
	refs := f.expect(t, "GET", "/api/brakeman/requests/"+f.req+"/refs", nil, http.StatusOK, "")["refs"].([]any)
	if len(refs) != 2 || refs[1].(map[string]any)["ref"] != ref {
		t.Fatalf("the brief's refs: %v", refs)
	}
}
