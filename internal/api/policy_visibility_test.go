package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Who sees a routed decision and who changes its route (SW1 PR-7b,
// docs/plans/decision-policy.md): the decide routes follow the router's
// confirmation, a route to a person, a role or the grant is refused under a
// policy in force, and the router may be asked again.

// the route is taken as before and the router is told the record again.
func TestRouteDecisionUnderAPolicyIsRefused(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	f.inForce(did)
	rev := f.rev(t, did)
	for i, to := range []map[string]any{{"kind": "person", "name": "zelda"}, {"kind": "role", "name": "architecture"}} {
		f.routeTo(t, did, fmt.Sprintf("o%d", i), to, http.StatusConflict, crew.CodeRoutedByPolicy)
	}
	if got := f.record(t, did); got.Rev != rev {
		t.Fatalf("a refused route moved the rev to %d", got.Rev)
	}
	f.fr.SetConfirm(did, func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{Why: "the roster moves"} })
	told := len(f.fr.Decisions())
	f.routeTo(t, did, "o9", map[string]any{"kind": "person", "name": "zelda"}, http.StatusOK, "")
	until(t, "the router told "+did+" again", func() bool { return len(f.fr.Decisions()) > told })
}

// The decide grant's route is refused under a policy in force too.
func TestGrantRouteRefusedUnderAPolicy(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	f.inForce(did)
	f.routeTo(t, did, "g1", toGrant, http.StatusConflict, crew.CodeRoutedByPolicy)
	if crew.GivenToGrant(f.record(t, did).Request) {
		t.Fatal("given to the grant under a policy in force")
	}
}

// with its new for.
func TestForChangedWhileNotInForceAsksTheRouter(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	told := len(f.fr.Decisions())
	f.routeTo(t, did, "o1", map[string]any{"kind": "person", "name": "zelda"}, http.StatusOK, "")
	until(t, "told again with its for", func() bool {
		ds := f.fr.Decisions()
		return len(ds) > told && ds[len(ds)-1].Record != nil && ds[len(ds)-1].Record.For != nil && ds[len(ds)-1].Record.For.Person == "zelda"
	})
}

// and the record told again; with no router, 409 no_router.
func TestRouteToTheRouter(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	rev, told := f.rev(t, did), len(f.fr.Decisions())
	f.routeTo(t, did, "ask1", map[string]any{"kind": "router"}, http.StatusOK, "")
	until(t, "told again", func() bool { return len(f.fr.Decisions()) > told })
	rec := f.record(t, did)
	if rec.Rev != rev || !strings.Contains(rec.History[len(rec.History)-1].Detail, `"kind":"router"`) {
		t.Fatalf("after the route to the router: rev %d, %+v", rec.Rev, rec.History[len(rec.History)-1])
	}
	plain := newDecideFixture(t, nil)
	other := plain.open(t, "r1")
	plain.routeTo(t, other, "ask1", map[string]any{"kind": "router"}, http.StatusConflict, crew.CodeNoRouter)
}

// refuse her at once.
func TestRemovedEligibilityHidesTheDecisionBeforeTheReroute(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	f.inForce(did)
	if _, l := f.list(t, passAna); !slices.Contains(ids(l), did) {
		t.Fatalf("ana's list before: %v", ids(l))
	}
	f.fr.SetConfirm(did, func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{InForce: true} })
	if _, l := f.list(t, passAna); slices.Contains(ids(l), did) || l.Cut {
		t.Fatalf("ana's list after: %v cut %v", ids(l), l.Cut)
	}
	if status, _ := f.readAs(t, passAna, did); status != http.StatusNotFound {
		t.Fatalf("ana's read: %d", status)
	}
	if status, _, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did)); status != http.StatusNotFound {
		t.Fatalf("ana's answer: %d", status)
	}
}

// now, beside those of the server's own policy it is admitted to.
func TestDecidePrincipalSeesOnlyItsDecisions(t *testing.T) {
	f := newPolicyFixture(t)
	forAna := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	forBob := f.routed(t, "r2", "dr-2", withEligible(anaPolicy(ext.OverrideNone), "rb", "bob"))
	for _, did := range []string{forAna, forBob} {
		f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
			if q.Subject == "ana" && did == forAna || q.Subject == "bob" && did == forBob {
				return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
			}
			return ext.Confirmation{InForce: true}
		})
	}
	given := f.open(t, "r3")
	until(t, given+"'s window closed", func() bool { return !f.srv.router.windows.in(given) })
	f.give(t, given)
	if _, l := f.list(t, passAna); !slices.Equal(ids(l), []string{given, forAna}) {
		t.Fatalf("ana's list: %v", ids(l))
	}
	if _, l := f.list(t, passBob); !slices.Equal(ids(l), []string{given, forBob}) {
		t.Fatalf("bob's list: %v", ids(l))
	}
	if status, _ := f.readAs(t, passBob, forAna); status != http.StatusNotFound {
		t.Fatalf("bob reading ana's: %d", status)
	}
}

// ext.ConfirmWait) is left out of the list, which says it was cut.
func TestDecideListLeavesOutUnconfirmedRows(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	gate := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(gate) })
	f.fr.SetConfirm(did, func(ext.ConfirmQuery) ext.Confirmation {
		<-gate
		return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
	})
	_, l := f.list(t, passAna)
	if slices.Contains(ids(l), did) || !l.Cut {
		t.Fatalf("the list with its row unconfirmed: %v cut %v", ids(l), l.Cut)
	}
	once.Do(func() { close(gate) })
}

// its policy.
func TestInboxShowsThePolicy(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	it := f.itemOf(t, ledger.DecisionRef(1))
	if len(it.RoutedTo) != 1 || it.RoutedTo[0].Kind != "person" || it.RoutedTo[0].Name != "Ana" || !strings.Contains(it.RoutedRule, "architecture owner") || !strings.Contains(it.RoutedRule, "r-1") {
		t.Fatalf("%s's item: %+v %q", did, it.RoutedTo, it.RoutedRule)
	}
}

// refused, nothing written.
func TestDecisionLinkCannotAnswerARoutedDecision(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	f.inForce(did)
	choice := 0
	out := refuseRecordDecide(proto.Decide{ReqID: "q1", Ref: ledger.DecisionRef(1), Action: "approve", Revision: fmt.Sprint(f.rev(t, did)), Choice: &choice, OpID: "op-1"})
	if !strings.Contains(string(out), linkCannotAnswer) {
		t.Fatalf("a decision link's answer: %s", out)
	}
	if n := len(f.record(t, did).Responses); n != 0 {
		t.Fatalf("%d responses", n)
	}
}
