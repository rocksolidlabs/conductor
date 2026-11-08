package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// Whom a decision is for (docs/plans/decisions.md, HM1, PR-12), through
// the routes: the proposal's check of a label against the crew the
// revision takes, the operator's answer for a label, and the decisions on
// a person's page and in GET /api/decisions?for=.

// peopledTeam shapes the launch fixture's crew with two people: nate the
// architecture owner, ana the acceptance owner.
func peopledTeam(c *crew.Crew) {
	c.People = []crew.Person{{Name: "nate", Responsibilities: []string{crew.RespArchitecture}}, {Name: "ana", Responsibilities: []string{crew.RespAcceptance}}}
}

// forGate is a decision of a revision, before build, for the label.
func forGate(id, kind, label string) map[string]any {
	return map[string]any{"id": id, "question": "Is the schema right?", "kind": kind, "before": []string{"build"}, "for": label}
}

// A proposal whose decision is for a label the crew does not resolve is
// refused invalid, in the words of the crew's check: a responsibility
// nobody holds, one not among the four, a person not in the crew; a label
// of no shape is the store's to refuse. Nothing is kept.
func TestProposalRefusesALabelTheCrewLacks(t *testing.T) {
	l := newLaunchTest(t, nil, peopledTeam)
	before := len(l.expect(t, "GET", "/api/dispatch/tickets/CND-1/plans", nil, http.StatusOK, "")["plans"].([]any))
	for label, words := range map[string]string{
		"role:security": "no person of crew " + l.crew.ID + " holds security",
		"role:legal":    `"role:legal" is not one of architecture, product, security, acceptance`,
		"person:zed":    `no person of crew ` + l.crew.ID + ` is named "zed"`,
		"team:api":      "must be role:<responsibility> or person:<name>",
	} {
		out := l.propose(t, "CND-1", map[string]any{"crew": l.crew.ID, "decisions": []any{forGate("schema", "approval", label)}}, http.StatusBadRequest, dispatch.RefusalInvalid)
		if !strings.Contains(apiMessage(out), words) {
			t.Errorf("for %s: %q, want %q", label, apiMessage(out), words)
		}
	}
	if after := len(l.expect(t, "GET", "/api/dispatch/tickets/CND-1/plans", nil, http.StatusOK, "")["plans"].([]any)); after != before {
		t.Fatalf("%d revisions kept, %d before", after, before)
	}
	// A label the crew resolves is taken, kept in the revision's body.
	id := l.proposed(t, l.crew.ID, map[string]any{"decisions": []any{forGate("schema", "approval", "role:architecture"), forGate("migrate", "assignment", "person:ana")}})
	body := entityOf(t, l.expect(t, "GET", "/api/dispatch/plans/"+id, nil, http.StatusOK, ""), "plan")["body"].(map[string]any)
	ds := body["decisions"].([]any)
	if ds[0].(map[string]any)["for"] != "role:architecture" || ds[1].(map[string]any)["for"] != "person:ana" || ds[1].(map[string]any)["kind"] != "assignment" {
		t.Fatalf("the revision's decisions: %v", ds)
	}
}

// The operator answers a gate for its label: the decision's request names
// nate, the architecture owner; the receipt's actor is admin (who
// answered) and its for nate, architecture (for whom), side by side; the
// gate's task then starts.
func TestTheOperatorAnswersForALabel(t *testing.T) {
	l := newLaunchTest(t, nil, peopledTeam)
	plan := l.approved(t, map[string]any{"decisions": []any{forGate("schema", "approval", "role:architecture")}})
	run := entityOf(t, l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, ""), "run")["id"].(string)
	ds := l.expect(t, "GET", "/api/decisions?run="+run, nil, http.StatusOK, "")["decisions"].([]any)
	if len(ds) != 1 {
		t.Fatalf("the run's decisions: %v", ds)
	}
	d := ds[0].(map[string]any)
	did := d["id"].(string)
	f, _ := d["request"].(map[string]any)["for"].(map[string]any)
	if f["label"] != "role:architecture" || f["person"] != "nate" || f["responsibility"] != crew.RespArchitecture {
		t.Fatalf("the gate's for: %v", d["request"])
	}
	out := l.respond(t, adminToken, did, "a1", 0, 1, http.StatusCreated, "")
	rc := out["receipt"].(map[string]any)
	rf, _ := rc["for"].(map[string]any)
	if rc["actor"] != "admin" || rf["person"] != "nate" || rf["responsibility"] != crew.RespArchitecture || !strings.HasSuffix(rc["words"].(string), " for nate, the architecture owner") {
		t.Fatalf("the receipt: %v", rc)
	}
	l.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	runningAttempt(t, l.testEnv, run, "build", 1)
	// The receipt as the record keeps it says the same.
	rec := l.expect(t, "GET", "/api/decisions/"+did, nil, http.StatusOK, "")["decision"].(map[string]any)
	kept := rec["responses"].([]any)[0].(map[string]any)
	if kept["actor"] != "admin" || kept["for"].(map[string]any)["person"] != "nate" {
		t.Fatalf("the kept receipt: %v", kept)
	}
}

// A person's decisions: GET /api/decisions?for= lists those whose for
// named them, and their page lists the open and answered ones with the
// record, the newest first; once applied it lists none, and a name a
// decision named still has a page; a name of no shape is refused.
func TestAPersonsDecisions(t *testing.T) {
	l := newLaunchTest(t, nil, peopledTeam)
	plan := l.approved(t, map[string]any{"decisions": []any{forGate("schema", "approval", "role:architecture"), forGate("migrate", "assignment", "person:ana")}})
	run := entityOf(t, l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, ""), "run")["id"].(string)
	forOf := func(name string) []any {
		t.Helper()
		ds, _ := l.expect(t, "GET", "/api/decisions?for="+name, nil, http.StatusOK, "")["decisions"].([]any)
		return ds
	}
	nate, ana := forOf("nate"), forOf("ana")
	if len(nate) != 1 || nate[0].(map[string]any)["gate"] != "schema" || len(ana) != 1 || ana[0].(map[string]any)["gate"] != "migrate" {
		t.Fatalf("nate's %v, ana's %v", nate, ana)
	}
	if ds := forOf("bo"); len(ds) != 0 {
		t.Fatalf("bo's: %v", ds)
	}
	l.expect(t, "GET", "/api/decisions?for=", nil, http.StatusOK, "")
	l.expect(t, "GET", "/api/decisions?for="+strings.Repeat("a", crew.MaxPersonName+1), nil, http.StatusBadRequest, "invalid_request")
	work := func(name string) map[string]any {
		t.Helper()
		return l.expect(t, "GET", "/api/people/"+personSegment(name), nil, http.StatusOK, "")
	}
	w := work("ana")
	if ds, _ := w["decisions"].([]any); len(ds) != 1 || ds[0].(map[string]any)["gate"] != "migrate" || ds[0].(map[string]any)["type"] != ledger.DecisionAssignment || ds[0].(map[string]any)["history"] != nil {
		t.Fatalf("ana's page's decisions: %v", w["decisions"])
	}
	did := ana[0].(map[string]any)["id"].(string)
	l.respond(t, adminToken, did, "a1", 0, 1, http.StatusCreated, "")
	l.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	if ds, _ := work("ana")["decisions"].([]any); len(ds) != 0 {
		t.Fatalf("ana's page after Done: %v", ds)
	}
	_ = run
	// The crew saved without ana: her page stands on the run's and the
	// decision's names.
	c := l.crew
	c.People = c.People[:1]
	if _, err := l.srv.crews.Update(c.ID, c); err != nil {
		t.Fatal(err)
	}
	if w := work("ana"); w["name"] != "ana" {
		t.Fatalf("ana's page: %v", w)
	}
}
