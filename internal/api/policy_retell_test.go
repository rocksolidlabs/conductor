package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The router routes a decision first with destinations and no policy (its
// one routed row); the operator then gives it to the decide grant, which
// tells the router again (a for change), and the router answers its
// policy: the policy is written at the record's next rev, its row a
// router's change of its revision, and only ana answers it. Bob, holding
// the decide grant, neither sees it nor answers it, nothing kept; the
// operator's plain answer is refused not_eligible.
func TestAPolicyAfterAnEarlierRouteIsWrittenWhenToldAgain(t *testing.T) {
	f := newPolicyFixture(t)
	f.fr.SetRoute("dr-1", ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ana"}}, Rule: "architecture decisions"})
	did := f.open(t, "r1")
	routedBy := func(kind string) func() bool {
		return func() bool {
			return slices.ContainsFunc(f.record(t, did).History, func(h ledger.HistoryRow) bool { return h.Kind == kind && h.By == "router:switchman" })
		}
	}
	until(t, did+" routed by the router", routedBy(ledger.HistoryRouted))
	if ledger.RouterPolicy(f.record(t, did).Policy) {
		t.Fatalf("%s took a policy the router did not answer", did)
	}
	f.fr.SetPolicy(did, anaPolicy(ext.OverrideOperator))
	f.inForce(did)
	before := f.record(t, did)
	f.give(t, did)
	until(t, did+"'s policy written", func() bool { return ledger.RouterPolicy(f.record(t, did).Policy) })
	rec := f.record(t, did)
	last := rec.History[len(rec.History)-1]
	if rec.Rev != before.Rev+2 || rec.State != ledger.DecisionOpen || last.Kind != ledger.HistoryRerouted || last.By != "router:switchman" ||
		!strings.Contains(last.Detail, `"revision":"exttest-r1"`) {
		t.Fatalf("under its policy: rev %d (was %d before the give), %s, last row %+v", rec.Rev, before.Rev, rec.State, last)
	}
	f.view(t, passAna, did)
	if status, data := f.readAs(t, passBob, did); status != http.StatusNotFound {
		t.Fatalf("bob's read under the policy: %d %s", status, data)
	}
	if status, data, _ := f.respondAs(t, passBob, did, "b1", 0, rec.Rev); status != http.StatusNotFound {
		t.Fatalf("bob's answer under the policy: %d %s", status, data)
	}
	if n := len(f.record(t, did).Responses); n != len(rec.Responses) {
		t.Fatalf("bob's refused answer was kept: %d rows (were %d)", n, len(rec.Responses))
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, rec.Rev, false, ""), http.StatusForbidden, crew.CodeNotEligible)
	if now := f.record(t, did); now.State != ledger.DecisionOpen {
		t.Fatalf("after the refused answers: %s", now.State)
	}
}

// The why OpenDecisions tells a router follows real re-routes: a first
// policy that followed the router's earlier route (its row a router's
// change with no op id) is routed; a re-route by Host.Reroute (its row
// with the router's op id) is its kind; a route after it is routed again.
func TestTheOpenDecisionsWhyFollowsReroutes(t *testing.T) {
	row := func(kind, by, op string) ledger.HistoryRow { return ledger.HistoryRow{Kind: kind, By: by, OpID: op} }
	opened := []ledger.HistoryRow{row(ledger.HistoryOpened, "plan", ""), row(ledger.HistoryRouted, "local/1", "")}
	first := append(slices.Clone(opened), row(ledger.HistoryRouted, "router:switchman", ""), row(ledger.HistoryRerouted, "router:switchman", ""))
	for _, c := range []struct {
		name    string
		history []ledger.HistoryRow
		want    string
	}{
		{"never routed", []ledger.HistoryRow{row(ledger.HistoryOpened, "plan", "")}, ""},
		{"a first policy after an earlier route", first, ledger.HistoryRouted},
		{"a re-route after it", append(slices.Clone(first), row(ledger.HistoryRerouted, "router:switchman", "rr-1")), ledger.HistoryRerouted},
		{"an escalation", append(slices.Clone(first), row(ledger.HistoryEscalated, "router:switchman", "rr-2")), ledger.HistoryEscalated},
		{"the operator's route after a re-route", append(slices.Clone(first), row(ledger.HistoryRerouted, "router:switchman", "rr-1"), row(ledger.HistoryRouted, "admin", "give-1")), ledger.HistoryRouted},
	} {
		if got := lastRouteWhy(ledger.DecisionRecord{History: c.history}); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
