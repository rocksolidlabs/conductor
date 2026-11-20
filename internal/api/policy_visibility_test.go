package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
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
	// Who answers, as a brief says it (Brakeman's too): by the policy,
	// never "you, the operator" alone; this policy allows no override.
	if it.Answerer != "Ana, for the architecture owner, by its router's policy" {
		t.Fatalf("%s's answerer: %q", did, it.Answerer)
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

// A Brakeman turn's route_decision on a decision under its router's
// policy in force is refused 409 routed_by_policy, to a person or a role,
// nothing written (its rev, its request and the turn's acts as they
// were); its route to the router is refused 403, the operator's to ask.
// Not in force, the turn's route is taken and the router is told the
// record again.
func TestATurnsRouteIsRefusedUnderAPolicyInForce(t *testing.T) {
	fr := exttest.NewFakeRouter("switchman")
	fr.SetPolicy("dr-1", anaPolicy(ext.OverrideNone))
	fr.SetRoute("dr-1", ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ana"}}, Rule: "architecture decisions"})
	f := newRouteFixtureWith(t, []string{brakeman.ActRouteDecision}, nil, WithExtensions(local.Fill(ext.Providers{Router: fr, All: []ext.Entry{
		{Kind: ext.KindDecisions, Name: "switchman", Value: fr},
	}})))
	if f.did != "dr-1" {
		t.Fatalf("the fixture's decision is %s", f.did)
	}
	n, _ := ledger.ParseDecisionID(f.did)
	record := func() ledger.DecisionRecord {
		rec, _, err := f.srv.ledger.Decision(t.Context(), n)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	until(t, f.did+"'s policy written", func() bool { return ledger.RouterPolicy(record().Policy) })
	fr.SetConfirm(f.did, func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{InForce: true} })
	before := record()
	for i, to := range []map[string]any{{"kind": "person", "name": "nate"}, {"kind": "role", "name": "security"}} {
		status, out := f.route(t, fmt.Sprintf("p-%d", i), to)
		if status != http.StatusConflict || codeOf(out) != crew.CodeRoutedByPolicy {
			t.Fatalf("the turn's route to %v in force: %d %v", to, status, out)
		}
	}
	status, out := f.route(t, "p-router", map[string]any{"kind": "router"})
	if status != http.StatusForbidden || codeOf(out) != crew.CodeNotApprover {
		t.Fatalf("the turn's route to the router: %d %v", status, out)
	}
	if after := record(); after.Rev != before.Rev || after.Request != before.Request || len(f.routedBy(t, brakemanActor)) != 0 || len(f.acts(t)) != 0 {
		t.Fatalf("a refused route wrote: rev %d to %d, %d acts", before.Rev, after.Rev, len(f.acts(t)))
	}
	fr.SetConfirm(f.did, func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{Why: "the roster moves"} })
	told := len(fr.Decisions())
	if status, out := f.route(t, "p-9", map[string]any{"kind": "person", "name": "nate"}); status != http.StatusOK {
		t.Fatalf("the turn's route not in force: %d %v", status, out)
	}
	until(t, "the router told "+f.did+" again", func() bool { return len(fr.Decisions()) > told })
}

// A router answering its policy in force while it refreshes its own state
// (Behind) still stands behind it: a turn's route and the operator's route
// to a person are each refused 409 routed_by_policy, nothing kept, the rev
// unmoved.
func TestARouteIsRefusedWhileTheRouterIsBehind(t *testing.T) {
	fr := exttest.NewFakeRouter("switchman")
	fr.SetPolicy("dr-1", anaPolicy(ext.OverrideNone))
	fr.SetRoute("dr-1", ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ana"}}, Rule: "architecture decisions"})
	f := newRouteFixtureWith(t, []string{brakeman.ActRouteDecision}, nil, WithExtensions(local.Fill(ext.Providers{Router: fr, All: []ext.Entry{
		{Kind: ext.KindDecisions, Name: "switchman", Value: fr},
	}})))
	n, _ := ledger.ParseDecisionID(f.did)
	record := func() ledger.DecisionRecord {
		rec, _, err := f.srv.ledger.Decision(t.Context(), n)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	until(t, f.did+"'s policy written", func() bool { return ledger.RouterPolicy(record().Policy) })
	fr.SetConfirm(f.did, func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{InForce: true, Behind: true} })
	before := record()
	if status, out := f.route(t, "b-1", map[string]any{"kind": "person", "name": "nate"}); status != http.StatusConflict || codeOf(out) != crew.CodeRoutedByPolicy {
		t.Fatalf("the turn's route while the router is behind: %d %v", status, out)
	}
	out := f.expect(t, http.MethodPost, "/api/decisions/"+f.did+"/route", map[string]any{"opId": "o-1", "to": map[string]any{"kind": "person", "name": "nate"}, "expectedRev": before.Rev}, http.StatusConflict, crew.CodeRoutedByPolicy)
	if after := record(); after.Rev != before.Rev || after.Request != before.Request || len(after.History) != len(before.History) || len(f.acts(t)) != 0 {
		t.Fatalf("a refused route wrote: rev %d to %d, %v", before.Rev, after.Rev, out)
	}
}

// A turn's route asks the router before the desk's lock: with the
// router's answer held at a gate, an ask of Brakeman made meanwhile is
// answered; once the gate opens, the route is refused routed_by_policy as
// before.
func TestATurnsRouteAsksTheRouterOutsideTheDesksLock(t *testing.T) {
	fr := exttest.NewFakeRouter("switchman")
	fr.SetPolicy("dr-1", anaPolicy(ext.OverrideNone))
	fr.SetRoute("dr-1", ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ana"}}, Rule: "architecture decisions"})
	f := newRouteFixtureWith(t, []string{brakeman.ActRouteDecision}, nil, WithExtensions(local.Fill(ext.Providers{Router: fr, All: []ext.Entry{
		{Kind: ext.KindDecisions, Name: "switchman", Value: fr},
	}})))
	n, _ := ledger.ParseDecisionID(f.did)
	until(t, f.did+"'s policy written", func() bool {
		rec, _, err := f.srv.ledger.Decision(t.Context(), n)
		return err == nil && ledger.RouterPolicy(rec.Policy)
	})
	// The router's answer waits for the gate, and nothing ends its wait
	// before: the ask is held for as long as the test holds it.
	long := time.Hour
	extWaiting.Store(&long)
	t.Cleanup(func() { extWaiting.Store(nil) })
	inside, gate := make(chan struct{}), make(chan struct{})
	var once, open sync.Once
	release := func() { open.Do(func() { close(gate) }) }
	t.Cleanup(release)
	fr.SetConfirm(f.did, func(ext.ConfirmQuery) ext.Confirmation {
		once.Do(func() { close(inside) })
		<-gate
		return ext.Confirmation{InForce: true}
	})
	type reply struct {
		status int
		out    map[string]any
	}
	routed := make(chan reply, 1)
	rev := f.rev(t)
	go func() {
		status, out := f.routeAt(t, "g-1", map[string]any{"kind": "person", "name": "nate"}, rev)
		routed <- reply{status, out}
	}()
	<-inside
	asked := make(chan int, 1)
	go func() {
		status, _, _ := f.callAs(t, adminToken, http.MethodPost, "/api/brakeman/requests", map[string]any{"opId": "ask-1", "kind": "briefing"})
		asked <- status
	}()
	select {
	case <-asked:
	case <-time.After(time.Minute): // bounds a hang: the old order holds the ask until the gate opens
		t.Fatal("an ask of Brakeman waited behind a turn's route held at the router")
	}
	release()
	if got := <-routed; got.status != http.StatusConflict || codeOf(got.out) != crew.CodeRoutedByPolicy {
		t.Fatalf("the turn's route once the router answered: %d %v", got.status, got.out)
	}
}

// A list of more routed rows than the guard has call slots, with a router
// that answers at once, while the guard is busy for a moment: every slot
// held by other calls, each given back as an ask waits for one. Every row
// the router confirms is listed and the list is not cut. Twenty-four rows
// (one record's, many times over) are asked as the decide list asks them.
func TestAListOfMoreRowsThanSlotsIsWhole(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	f.inForce(did)
	row := f.record(t, did).DecisionRow
	rows := make([]ledger.DecisionRow, extInFlight+8)
	for i := range rows {
		rows[i] = row
	}
	var mu sync.Mutex
	var held []extSlot
	for range extInFlight {
		sl, ok := f.srv.extTake(ext.KindDecisions, "switchman")
		if !ok {
			t.Fatal("a call slot of the router was taken before the test held them")
		}
		held = append(held, sl)
	}
	giveOne := func() {
		mu.Lock()
		defer mu.Unlock()
		if len(held) > 0 {
			held[0].free()
			held = held[1:]
		}
	}
	t.Cleanup(func() {
		for len(held) > 0 {
			giveOne()
		}
	})
	hook := func(kind, _ string) {
		if kind == ext.KindDecisions {
			giveOne()
		}
	}
	f.srv.extSlotWait.Store(&hook)
	kept, unasked := f.srv.eligibleRows(t.Context(), rows, "ana")
	if len(kept) != len(rows) || unasked {
		t.Fatalf("%d of %d rows listed, cut %v", len(kept), len(rows), unasked)
	}
}
