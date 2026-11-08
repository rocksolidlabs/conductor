package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

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
	return &routeFixture{actFixture: f, run: run.ID, did: did}
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
	if rc["decision"] != f.did || !strings.Contains(rc["words"].(string), "ana, the security reviewer") {
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
	if len(acts) != 1 || acts[0].Operation != brakeman.ActRouteDecision || acts[0].State != brakeman.ActDone || acts[0].Target.String() != "run:"+f.run ||
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
// visible; with the scope naming another crew, outside it; at a rev the
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
	if status, out := f.route(t, "r-3", to); status != http.StatusForbidden || codeOf(out) != "outside_scope" {
		t.Fatalf("outside the scope: %d %v", status, out)
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
// recovery from DR1's history by its op; one written pending that never
// ran is marked not_run, never run by the recovery, and its op id is
// refused after.
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
	f.endTurn(t)
	f.bk.desk.recoverMakes(t.Context())
	acts := f.acts(t)
	if len(acts) != 2 || acts[0].State != brakeman.ActDone || !strings.HasPrefix(acts[0].Receipt, f.did+"#") ||
		acts[1].State != brakeman.ActNotRun || acts[1].Why != "the server stopped before it ran" {
		t.Fatalf("after the recovery %+v", acts)
	}
	if n := len(f.routedBy(t, brakemanActor)); n != 1 {
		t.Fatalf("%d routed rows by brakeman", n)
	}
}
