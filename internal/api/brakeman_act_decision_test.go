package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Brakeman's route_decision (docs/plans/brakeman.md, PR-18; BK2): DR1's
// routing of a decision record, to the operator or to a person of the
// decision's run's crew. The fixture is a managed run of managedCrew with
// two people (nate the architecture owner, ana the security reviewer),
// its build attempt running and an open decision it asked, and a Brakeman
// turn granted what the test grants. None is parallel: some hold the act's
// seam.

// routeFixture is the actFixture's turn over a decision's run.
type routeFixture struct {
	*actFixture
	df       *decisionFixture
	run, did string
}

func newRouteFixture(t *testing.T, grant []string, mod func(c map[string]any)) *routeFixture {
	t.Helper()
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), append([]catalog.Agent{readyCat}, bkAgents()...))
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	e.gitRepo(t)
	c := managedCrew(e.root)
	c.People = []crew.Person{{Name: "nate", Responsibilities: []string{crew.RespArchitecture}}, {Name: "ana", Responsibilities: []string{crew.RespSecurity}}}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	srv := e.srv
	t.Cleanup(func() { stopEveryRun(srv) })
	df := &decisionFixture{testEnv: e, run: run.ID}
	df.sid = runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	df.tok = e.agentToken(df.sid)
	did := entityOf(t, df.ask(t, approvalBody("q-1"), http.StatusCreated, ""), "decision")["id"].(string)
	f := &actFixture{launchTest: &launchTest{planTest: &planTest{testEnv: e}}, bk: bkEnvOf(t, e)}
	f.turn(t, grant, mod)
	return &routeFixture{actFixture: f, df: df, run: run.ID, did: did}
}

// route sends the turn's route of the fixture's decision to `to` under
// the op id, at the rev it reads now.
func (f *routeFixture) route(t *testing.T, opID string, to map[string]any) (int, map[string]any) {
	t.Helper()
	return f.asTurn(t, http.MethodPost, "/api/decisions/"+f.did+"/route", map[string]any{"opId": opID, "expectedRev": f.rev(t), "to": to, "note": "Brakeman read the attempt."})
}

// rev is the decision's rev now.
func (f *routeFixture) rev(t *testing.T) int {
	t.Helper()
	n, _ := ledger.ParseDecisionID(f.did)
	rec, ok, err := f.srv.ledger.Decision(t.Context(), n)
	if err != nil || !ok {
		t.Fatalf("%s: %v %v", f.did, ok, err)
	}
	return rec.Rev
}

// routedBy is the decision's routed rows by actor, each its detail.
func (f *routeFixture) routedBy(t *testing.T, actor string) []map[string]any {
	t.Helper()
	n, _ := ledger.ParseDecisionID(f.did)
	rec, _, err := f.srv.ledger.Decision(t.Context(), n)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, h := range rec.History {
		if h.Kind == ledger.HistoryRouted && h.By == actor {
			var d map[string]any
			_ = json.Unmarshal([]byte(h.Detail), &d)
			out = append(out, d)
		}
	}
	return out
}

// TestRouteToALabel: granted route_decision, the turn routes an open
// decision through DR1's own route to the holder of a responsibility of
// the run's crew: DR1's receipt, under the actor brakeman and the derived
// op id, the routed row naming the person beside the operator, the act
// done in the request with the receipt copied; the same opId again is
// that route's receipt, one row; to a person by name and to the operator
// too. A label no person of the crew holds is refused before anything is
// kept, and the decision stays open, unanswered.
func TestRouteToALabel(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActRouteDecision}, nil)
	status, out := f.route(t, "r-1", map[string]any{"kind": "role", "name": "security"})
	if status != http.StatusOK {
		t.Fatalf("the route: %d %v", status, out)
	}
	rc, _ := out["receipt"].(map[string]any)
	if rc["decision"] != f.did || rc["words"] != f.did+" is now for ana, the security reviewer: routed by brakeman" {
		t.Fatalf("the receipt %v", out)
	}
	if _, ok := out["decision"]; ok {
		t.Fatalf("the act answered the decision record, which the turn's reads do not give: %v", out)
	}
	rows := f.routedBy(t, brakemanActor)
	if len(rows) != 1 {
		t.Fatalf("routed rows by brakeman %v", rows)
	}
	to, _ := json.Marshal(rows[0]["to"])
	if string(to) != `[{"kind":"operator"},{"kind":"person","name":"ana"}]` || rows[0]["rule"] != "local/1: the operator answers, for the security reviewer (role:security)" {
		t.Fatalf("the routed row %v", rows[0])
	}
	acts := f.acts(t)
	if len(acts) != 1 || acts[0].Operation != brakeman.ActRouteDecision || acts[0].State != brakeman.ActDone || acts[0].Target.String() != "decision:"+f.did || acts[0].Run != f.run ||
		acts[0].Op != actOp(f.req, "r-1") || acts[0].Receipt != routeReceipt(f.did, int(rc["seq"].(float64))) {
		t.Fatalf("the act %+v", acts)
	}
	// The same act again: DR1's receipt of it, nothing routed again.
	status, again := f.route(t, "r-1", map[string]any{"kind": "role", "name": "security"})
	if status != http.StatusOK || again["receipt"].(map[string]any)["repeat"] != true || len(f.routedBy(t, brakemanActor)) != 1 {
		t.Fatalf("the act again: %d %v", status, again)
	}
	// To a person by name, and to the operator.
	if status, out := f.route(t, "r-2", map[string]any{"kind": "person", "name": "nate"}); status != http.StatusOK || !strings.Contains(out["receipt"].(map[string]any)["words"].(string), "nate") {
		t.Fatalf("to nate: %d %v", status, out)
	}
	if status, out := f.route(t, "r-3", map[string]any{"kind": "operator"}); status != http.StatusOK {
		t.Fatalf("to the operator: %d %v", status, out)
	}
	// A responsibility no person of the crew holds, and a name none has.
	for i, to := range []map[string]any{{"kind": "role", "name": "product"}, {"kind": "person", "name": "zed"}} {
		status, out := f.route(t, "r-x"+string(rune('a'+i)), to)
		if status != http.StatusBadRequest || codeOf(out) != crew.CodeInvalid || !strings.Contains(out["error"].(map[string]any)["message"].(string), "no person of crew routes") {
			t.Fatalf("to %v: %d %v", to, status, out)
		}
	}
	if acts := f.acts(t); len(acts) != 3 || len(f.routedBy(t, brakemanActor)) != 3 {
		t.Fatalf("acts %+v", acts)
	}
	// Routed, never answered: the operator still answers it.
	d := f.decisionWhen(t, f.did, "open", inState(ledger.DecisionOpen))
	if rs, _ := d["responses"].([]any); len(rs) != 0 {
		t.Fatalf("the decision was answered: %v", d)
	}
}

// TestARouteIsHeldToTheGrantAndTheRuns: route_decision ungranted is
// refused first; granted, with the runs not visible, refused as not
// visible; with the scope naming another crew, as a decision that is not
// there, in the same words as an id no decision has; at a rev the
// decision has left, stale; none kept, nothing routed. And no grant
// answers it: the respond route takes no turn.
func TestARouteIsHeldToTheGrantAndTheRuns(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActLaunchApprovedPlan}, nil)
	to := map[string]any{"kind": "role", "name": "security"}
	if status, out := f.route(t, "r-1", to); status != http.StatusForbidden || codeOf(out) != "not_granted" {
		t.Fatalf("ungranted: %d %v", status, out)
	}
	f.endTurn(t)
	f.turn(t, []string{brakeman.ActRouteDecision}, func(c map[string]any) { c["visibility"].(map[string]any)["runs"] = false })
	if status, out := f.route(t, "r-2", to); status != http.StatusForbidden || codeOf(out) != "not_visible" {
		t.Fatalf("the runs hidden: %d %v", status, out)
	}
	f.endTurn(t)
	f.turn(t, []string{brakeman.ActRouteDecision}, func(c map[string]any) {
		c["visibility"].(map[string]any)["runs"] = true
		c["scope"] = map[string]any{"crews": []any{"another"}}
	})
	status, outside := f.route(t, "r-3", to)
	if status != http.StatusNotFound || codeOf(outside) != crew.CodeNotFound || outside["error"].(map[string]any)["message"] != "no decision "+f.did {
		t.Fatalf("outside the scope: %d %v", status, outside)
	}
	n, _ := ledger.ParseDecisionID(f.did)
	none := ledger.DecisionID(n + 100)
	status, missing := f.asTurn(t, http.MethodPost, "/api/decisions/"+none+"/route", map[string]any{"opId": "r-3b", "expectedRev": 1, "to": to})
	if status != http.StatusNotFound || codeOf(missing) != crew.CodeNotFound || missing["error"].(map[string]any)["message"] != "no decision "+none {
		t.Fatalf("no such decision: %d %v", status, missing)
	}
	f.endTurn(t)
	f.turn(t, []string{brakeman.ActRouteDecision}, func(c map[string]any) { c["scope"] = map[string]any{} })
	status, out := f.asTurn(t, http.MethodPost, "/api/decisions/"+f.did+"/route", map[string]any{"opId": "r-4", "expectedRev": f.rev(t) + 1, "to": to})
	if status != http.StatusConflict || codeOf(out) != crew.CodeStaleDecision {
		t.Fatalf("a rev it left: %d %v", status, out)
	}
	if acts := f.acts(t); len(acts) != 0 || len(f.routedBy(t, brakemanActor)) != 0 {
		t.Fatalf("acts %+v", acts)
	}
	if status, out := f.asTurn(t, http.MethodPost, "/api/decisions/"+f.did+"/respond", map[string]any{"responseId": "x-1", "choice": 0, "expectedRev": f.rev(t)}); status != http.StatusUnauthorized {
		t.Fatalf("the respond route took the turn: %d %v", status, out)
	}
}

// TestARouteCommittedBeforeItsCopyIsCompletedAfterARestart: a route whose
// row DR1 wrote and whose outcome was never copied is completed by the
// recovery from DR1's history by its decision and op; one written pending
// that never ran is marked not_run, never run by the recovery, and its op
// id is refused after (act_not_run).
func TestARouteCommittedBeforeItsCopyIsCompletedAfterARestart(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActRouteDecision}, nil)
	to := map[string]any{"kind": "role", "name": "security"}
	holdActAt(t, "committed")
	if status, out := f.route(t, "r-1", to); status != http.StatusServiceUnavailable {
		t.Fatalf("the route: %d %v", status, out)
	}
	holdActAt(t, "pending")
	if status, out := f.route(t, "r-2", to); status != http.StatusServiceUnavailable {
		t.Fatalf("the route: %d %v", status, out)
	}
	actSeam.Store(nil)
	rows := f.routedBy(t, brakemanActor)
	if acts := f.acts(t); len(acts) != 2 || acts[0].State != brakeman.ActPending || acts[1].State != brakeman.ActPending || len(rows) != 1 {
		t.Fatalf("before the recovery: acts %+v, rows %v", acts, rows)
	}
	f.bk.desk.recoverMakes(t.Context())
	acts := f.acts(t)
	if len(acts) != 2 || acts[0].State != brakeman.ActDone || !strings.HasPrefix(acts[0].Receipt, f.did+"#") ||
		acts[1].State != brakeman.ActNotRun || acts[1].Why != "the server stopped before it ran" {
		t.Fatalf("after the recovery %+v", acts)
	}
	if status, out := f.route(t, "r-2", to); status != http.StatusConflict || codeOf(out) != "act_not_run" {
		t.Fatalf("the route never run, again: %d %v", status, out)
	}
	if n := len(f.routedBy(t, brakemanActor)); n != 1 {
		t.Fatalf("%d routed rows by brakeman", n)
	}
}

// TestARouteRetriedAfterALostReplyIsOneRoute: a route whose reply was lost
// after DR1 committed it (the committed seam), sent again under its opId
// while the server runs, is answered by DR1's receipt of it (repeat): one
// routed row by brakeman, the act done with that receipt.
func TestARouteRetriedAfterALostReplyIsOneRoute(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActRouteDecision}, nil)
	to := map[string]any{"kind": "role", "name": "security"}
	holdActAt(t, "committed")
	if status, out := f.route(t, "r-1", to); status != http.StatusServiceUnavailable {
		t.Fatalf("the route: %d %v", status, out)
	}
	actSeam.Store(nil)
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActPending {
		t.Fatalf("before the retry %+v", acts)
	}
	status, out := f.route(t, "r-1", to)
	rc, _ := out["receipt"].(map[string]any)
	if status != http.StatusOK || rc["repeat"] != true || rc["decision"] != f.did {
		t.Fatalf("the retry: %d %v", status, out)
	}
	if n := len(f.routedBy(t, brakemanActor)); n != 1 {
		t.Fatalf("%d routed rows by brakeman", n)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActDone || acts[0].Receipt != routeReceipt(f.did, int(rc["seq"].(float64))) {
		t.Fatalf("after the retry %+v", acts)
	}
}

// TestARouteIsRefusedOnADecisionNotRoutable: a decision no longer open
// (cancelled) is refused at the admission (stale_target), nothing kept;
// one whose run is no longer running is refused by DR1 (not_held), the
// act refused with its words and nothing routed.
func TestARouteIsRefusedOnADecisionNotRoutable(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActRouteDecision}, nil)
	to := map[string]any{"kind": "role", "name": "security"}
	other := entityOf(t, f.df.ask(t, approvalBody("q-2"), http.StatusCreated, ""), "decision")["id"].(string)
	f.expect(t, http.MethodPost, "/api/decisions/"+other+"/cancel", map[string]any{"commandId": "c-1", "why": "Not needed.", "release": true, "expectedRev": 1}, http.StatusOK, "")
	status, out := f.asTurn(t, http.MethodPost, "/api/decisions/"+other+"/route", map[string]any{"opId": "r-1", "expectedRev": 1, "to": to})
	if status != http.StatusConflict || codeOf(out) != "stale_target" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "cancelled") {
		t.Fatalf("cancelled: %d %v", status, out)
	}
	if acts := f.acts(t); len(acts) != 0 {
		t.Fatalf("acts %+v", acts)
	}
	if err := f.srv.runs.Stop(t.Context(), f.run); err != nil {
		t.Fatal(err)
	}
	status, out = f.route(t, "r-2", to)
	if status != http.StatusConflict || codeOf(out) != crew.CodeNotHeld {
		t.Fatalf("its run stopped: %d %v", status, out)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActRefused || !strings.Contains(acts[0].Why, "continue it") {
		t.Fatalf("the act refused by DR1 %+v", acts)
	}
	if n := len(f.routedBy(t, brakemanActor)); n != 0 {
		t.Fatalf("%d routed rows by brakeman", n)
	}
}

// TestARouteIsHeldToTheRunItWasAdmittedOn: a route admitted while its
// decision's run is in the scope, held at the pending seam while the run
// is stopped and continued (its decision moving to the continuation,
// outside the scope), is refused in the routing transaction: nothing is
// routed on the continuation, the act refused.
func TestARouteIsHeldToTheRunItWasAdmittedOn(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActLaunchApprovedPlan}, nil)
	f.endTurn(t)
	f.turn(t, []string{brakeman.ActRouteDecision}, func(c map[string]any) { c["scope"] = map[string]any{"runs": []any{f.run}} })
	at, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hold := func(stage, _, _ string) error {
		if stage == "pending" {
			once.Do(func() { close(at); <-gate })
		}
		return nil
	}
	actSeam.Store(&hold)
	t.Cleanup(func() { actSeam.Store(nil) })
	body := map[string]any{"opId": "r-1", "expectedRev": f.rev(t), "to": map[string]any{"kind": "operator"}}
	type reply struct {
		status int
		out    map[string]any
	}
	done := make(chan reply, 1)
	go func() {
		status, out := f.asTurn(t, http.MethodPost, "/api/decisions/"+f.did+"/route", body)
		done <- reply{status, out}
	}()
	select {
	case <-at:
	case <-time.After(60 * time.Second):
		close(gate)
		t.Fatal("the route never reached its pending seam")
	}
	if err := f.srv.runs.Stop(t.Context(), f.run); err != nil {
		close(gate)
		t.Fatal(err)
	}
	next, err := f.srv.runs.ContinueRun(t.Context(), f.run, "")
	if err != nil {
		close(gate)
		t.Fatal(err)
	}
	n, _ := ledger.ParseDecisionID(f.did)
	if !waitFor(30*time.Second, func() bool {
		rec, ok, err := f.srv.ledger.Decision(t.Context(), n)
		return err == nil && ok && rec.CurrentRun == next.ID
	}) {
		close(gate)
		t.Fatalf("%s never moved to %s", f.did, next.ID)
	}
	close(gate)
	got := <-done
	if got.status != http.StatusConflict || codeOf(got.out) != crew.CodeStaleDecision || !strings.Contains(got.out["error"].(map[string]any)["message"].(string), "moved to another run since the route was admitted") {
		t.Fatalf("the route after the continuation: %d %v", got.status, got.out)
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActRefused || acts[0].Run != f.run {
		t.Fatalf("acts %+v", acts)
	}
	if rows := f.routedBy(t, brakemanActor); len(rows) != 0 {
		t.Fatalf("routed on the continuation: %v", rows)
	}
}

// TestATurnsRoutesKeepTheirOwnBound: a turn's routes of one decision count
// in their own bound, apart from the operator's operations: past it the
// turn's next route is refused (decision_limit, the act refused), and the
// operator's comment on the decision is still taken.
func TestATurnsRoutesKeepTheirOwnBound(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActRouteDecision}, nil)
	op := map[string]any{"kind": "operator"}
	for i := range ledger.MaxDecisionTurnOps {
		if status, out := f.route(t, fmt.Sprintf("r-%d", i), op); status != http.StatusOK {
			t.Fatalf("route %d: %d %v", i, status, out)
		}
	}
	status, out := f.route(t, "r-last", op)
	if status != http.StatusTooManyRequests || codeOf(out) != crew.CodeDecisionLimit {
		t.Fatalf("the route past the turn's bound: %d %v", status, out)
	}
	acts := f.acts(t)
	if last := acts[len(acts)-1]; last.OpID != "r-last" || last.State != brakeman.ActRefused {
		t.Fatalf("the act past the bound %+v", last)
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+f.did+"/comment", map[string]any{"opId": "o-1", "kind": "comment", "text": "Still mine to answer.", "expectedRev": f.rev(t)}, http.StatusOK, "")
}

// TestATurnRoutesTheDecisionItRead: the turn reads its decision in its
// run's read (and its task's), with its ref and rev, with its own token;
// routes it at that rev; and a brief proposing that route on the
// decision's ref at the rev read is taken, one at another rev refused.
func TestATurnRoutesTheDecisionItRead(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActRouteDecision}, nil)
	read := func(path string) map[string]any {
		t.Helper()
		status, out := f.asTurn(t, http.MethodGet, path, nil)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %v", path, status, out)
		}
		ds, _ := out["decisions"].([]any)
		for _, d := range ds {
			if m := d.(map[string]any); m["id"] == f.did {
				return m
			}
		}
		t.Fatalf("%s lists no %s: %v", path, f.did, out)
		return nil
	}
	read("/api/runs/" + f.run + "/tasks/build")
	d := read("/api/runs/" + f.run)
	rev := int(d["rev"].(float64))
	if d["ref"] != "decision:"+f.did || d["state"] != ledger.DecisionOpen || rev != f.rev(t) {
		t.Fatalf("the decision as the turn read it: %v", d)
	}
	status, out := f.asTurn(t, http.MethodPost, "/api/decisions/"+f.did+"/route", map[string]any{"opId": "r-1", "expectedRev": rev, "to": map[string]any{"kind": "role", "name": "security"}})
	if status != http.StatusOK {
		t.Fatalf("the route at the rev read: %d %v", status, out)
	}
	brief := func(rev string) map[string]any {
		br := factBrief("session:" + f.sid)
		br["items"] = append(br["items"].([]any), map[string]any{"kind": brakeman.ItemProposal, "text": "Route it to the security reviewer.", "op": brakeman.ActRouteDecision,
			"target": "decision:" + f.did, "expectedRevision": rev, "args": map[string]any{"opId": "r-1"}, "facts": []any{0}})
		return br
	}
	if status, out := f.bk.submit(t, f.sid, f.tok, "b-0", brief(strconv.Itoa(rev+1))); status != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(out), "decision:"+f.did) {
		t.Fatalf("a proposal at a rev not read: %d %v", status, out)
	}
	if status, out := f.bk.submit(t, f.sid, f.tok, "b-1", brief(strconv.Itoa(rev))); status != http.StatusCreated {
		t.Fatalf("the proposal at the rev read: %d %v", status, out)
	}
}

// TestACommittedRouteIsCopiedWhenItsCallerIsGone: a route whose caller
// has gone (its request's context cancelled) while it waited at the
// pending seam commits, and its receipt is copied: the act done, never
// left pending by a read after the commit.
func TestACommittedRouteIsCopiedWhenItsCallerIsGone(t *testing.T) {
	f := newRouteFixture(t, []string{brakeman.ActRouteDecision}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	hold := func(stage, _, _ string) error {
		if stage == "pending" {
			cancel()
		}
		return nil
	}
	actSeam.Store(&hold)
	t.Cleanup(func() { actSeam.Store(nil) })
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/decisions/"+f.did+"/route",
		strings.NewReader(fmt.Sprintf(`{"opId":"r-1","expectedRev":%d,"to":{"kind":"role","name":"security"}}`, f.rev(t))))
	r.Header.Set("Authorization", "Bearer "+f.tok)
	r.Header.Set(brakemanSessionHeader, f.sid)
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.1:4000"
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || ctx.Err() == nil {
		t.Fatalf("the route: %d %s (caller gone: %v)", w.Code, w.Body, ctx.Err())
	}
	if acts := f.acts(t); len(acts) != 1 || acts[0].State != brakeman.ActDone || !strings.HasPrefix(acts[0].Receipt, f.did+"#") {
		t.Fatalf("acts %+v", acts)
	}
}

// decisionFor reads whom a decision's request is for (HM1's for, as the
// ledger keeps it once HM1 writes one), which a Brakeman route to a person
// is refused on: none for a request without one or with no person.
func TestDecisionForReadsWhomItIsFor(t *testing.T) {
	for _, c := range []struct{ request, want string }{
		{`{"question":"May build start?"}`, ""},
		{`{"question":"q","for":{"label":"role:security","person":"ana","responsibility":"security"}}`, "ana"},
		{`{"question":"q","for":{"label":"role:product","person":""}}`, ""},
		{`{"question":"q","for":null}`, ""},
		{`not json`, ""},
	} {
		if got := decisionFor(c.request); got != c.want {
			t.Fatalf("%s: %q, want %q", c.request, got, c.want)
		}
	}
}
