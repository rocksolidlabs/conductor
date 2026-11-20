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

// busyGuard holds every call slot of f's router, so each ask of it must
// wait for one, and answers a channel told each time an ask waits and the
// function that gives the slots back.
func busyGuard(t *testing.T, f *policyFixture) (waiting <-chan struct{}, free func()) {
	t.Helper()
	w := make(chan struct{}, 64)
	hook := func(kind, _ string) {
		if kind == ext.KindDecisions {
			select {
			case w <- struct{}{}:
			default:
			}
		}
	}
	f.srv.extSlotWait.Store(&hook)
	var held []extSlot
	for range extInFlight {
		sl, ok := f.srv.extTake(ext.KindDecisions, "switchman")
		if !ok {
			t.Fatal("a call slot of the router was taken before the test held them")
		}
		held = append(held, sl)
	}
	var once sync.Once
	free = func() {
		once.Do(func() {
			for _, sl := range held {
				sl.free()
			}
		})
	}
	t.Cleanup(free)
	return w, free
}

// whileBusy runs call while every slot of f's router is held, and gives
// the slots back once an ask is waiting for one (or call has returned
// without waiting, which the caller's checks then judge).
func whileBusy[T any](t *testing.T, f *policyFixture, call func() T) T {
	t.Helper()
	waiting, free := busyGuard(t, f)
	done := make(chan T, 1)
	go func() { done <- call() }()
	select {
	case <-waiting:
		free()
		return <-done
	case got := <-done:
		free()
		return got
	}
}

// A guard whose slots are all taken for a moment is waited for, inside the
// ask's one wait: the decide list keeps every row the router confirms and
// is not cut; an eligible person's answer is accepted; the operator's plain
// answer under a policy allowing no override is refused, never taken as
// not in force.
func TestABusyGuardIsWaitedFor(t *testing.T) {
	f := newPolicyFixture(t)
	first := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	second := f.routed(t, "r2", "dr-2", anaPolicy(ext.OverrideNone))
	f.inForce(first)
	f.inForce(second)
	l := whileBusy(t, f, func() proto.DecideList { _, l := f.list(t, passAna); return l })
	if !slices.Contains(ids(l), first) || !slices.Contains(ids(l), second) || l.Cut {
		t.Fatalf("ana's list with the guard busy: %v cut %v", ids(l), l.Cut)
	}
	type answer struct {
		status int
		data   []byte
		out    map[string]any
	}
	got := whileBusy(t, f, func() answer {
		status, data, out := f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+first+"/respond", operatorBody("o1", 0, f.rev(t, first), false, ""))
		return answer{status, data, out}
	})
	if got.status != http.StatusForbidden || errorCode(got.out) != crew.CodeNotEligible {
		t.Fatalf("the operator's plain answer with the guard busy: %d %s", got.status, got.data)
	}
	got = whileBusy(t, f, func() answer {
		status, data, out := f.respondAs(t, passAna, first, "a1", 0, f.rev(t, first))
		return answer{status, data, out}
	})
	if got.status != http.StatusCreated {
		t.Fatalf("ana's answer with the guard busy: %d %s", got.status, got.data)
	}
}

// The decide list asks the router listAsks rows at a time, never holding
// every slot of the guard: eight rows (one record's, eight times) are
// asked, each ask held in the router until one more than listAsks are in
// it at once, which the list never lets happen: it ends at its one wait,
// its rows unasked, with no more than listAsks asks made at once.
func TestTheDecideListAsksAFewAtATime(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	var mu sync.Mutex
	now, most, open := 0, 0, false
	gate := make(chan struct{})
	release := func() {
		mu.Lock()
		defer mu.Unlock()
		if !open {
			open = true
			close(gate)
		}
	}
	t.Cleanup(release)
	f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
		mu.Lock()
		now++
		most = max(most, now)
		full := now > listAsks
		mu.Unlock()
		if full {
			release()
		}
		<-gate
		mu.Lock()
		now--
		mu.Unlock()
		return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
	})
	row := f.record(t, did).DecisionRow
	rows := []ledger.DecisionRow{row, row, row, row, row, row, row, row}
	f.srv.eligibleRows(t.Context(), rows, "ana")
	mu.Lock()
	defer mu.Unlock()
	if most > listAsks {
		t.Fatalf("%d asks in the router at once, at most %d", most, listAsks)
	}
}
