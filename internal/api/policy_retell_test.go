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
