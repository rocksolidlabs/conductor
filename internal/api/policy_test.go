package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A decision router's policy on a decision record (SW1,
// docs/plans/decision-policy.md), at the routes: the router is exttest's
// FakeRouter (it confirms policies), the identity provider exttest's
// FakeIdentity behind the decide fixture's passes (ana and bob hold the
// decide grant, nate the admin grant). Every test waits on the record in
// the ledger or holds a step at a seam (the fake's gate, the server's
// clock of the router's bound), never on the clock.

// policyFixture is a decide fixture whose router is a FakeRouter.
type policyFixture struct {
	*decideFixture
	fr *exttest.FakeRouter
}

func newPolicyFixture(t *testing.T) *policyFixture {
	t.Helper()
	fr := exttest.NewFakeRouter("switchman")
	return &policyFixture{newDecideFixtureRouted(t, nil, fr), fr}
}

// anaPolicy is the policy for the architecture owner, ana eligible by the
// role, the operator's override as given.
func anaPolicy(override string) ext.RoutePolicy {
	p := exttest.SamplePolicy()
	p.Groups[0].Eligible[0] = ext.Eligible{Subject: "ana", Name: "Ana", Via: ext.ViaRole, Ref: "ref-ana"}
	p.Override, p.Pinned.Override = override, override
	return p
}

// confirmFor has the router confirm did's policy as in force, ana
// eligible for the architecture group, everyone else in force and not
// eligible.
func (f *policyFixture) inForce(did string) {
	f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
		if q.Subject == "ana" {
			return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
		}
		return ext.Confirmation{InForce: true}
	})
}

// routed opens an approval with the router answering pol for it, and
// waits for the policy in its record: its id.
func (f *policyFixture) routed(t *testing.T, requestID, did string, pol ext.RoutePolicy) string {
	t.Helper()
	f.fr.SetPolicy(did, pol)
	f.fr.SetRoute(did, ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ana"}}, Rule: "architecture decisions"})
	got := f.open(t, requestID)
	if got != did {
		t.Fatalf("opened %s, want %s", got, did)
	}
	until(t, did+"'s policy written", func() bool { return ledger.RouterPolicy(f.record(t, did).Policy) })
	return did
}

// receiptOf is the response k's receipt of did as kept.
func (f *decideFixture) receiptOf(t *testing.T, did string, k int) crew.ResponseReceipt {
	t.Helper()
	for _, r := range f.record(t, did).Responses {
		if r.K == k {
			var rc crew.ResponseReceipt
			if err := json.Unmarshal([]byte(r.Receipt), &rc); err != nil {
				t.Fatal(err)
			}
			return rc
		}
	}
	t.Fatalf("%s has no response %d", did, k)
	return crew.ResponseReceipt{}
}

// operatorBody is the operator's response body.
func operatorBody(rid string, choice, rev int, override bool, rationale string) map[string]any {
	b := map[string]any{"responseId": rid, "choice": choice, "expectedRev": rev}
	if override {
		b["override"] = true
	}
	if rationale != "" {
		b["rationale"] = rationale
	}
	return b
}

// A route with destinations and a policy, through routeRecord, on an
// agent's approval: the policy written as the record's (the router's
// approver, its name, its revision, the spare kept, its fallback local/1),
// rev + 1, and exactly two routed rows, the opening's and the router's
// one, with its destinations, rule and revision and no op id.
func TestAPolicyIsRecordedAsTheDecisionsPolicy(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	rec := f.record(t, did)
	if rec.Rev != 2 || rec.State != ledger.DecisionOpen {
		t.Fatalf("the record after its policy: rev %d %s", rec.Rev, rec.State)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(rec.Policy), &p); err != nil {
		t.Fatal(err)
	}
	if p["approver"] != "router" || p["router"] != "switchman" || p["revision"] != "exttest-r1" || p["spare"] != true || p["fallback"] != "local/1" || p["scope"] != "exttest-scope" {
		t.Fatalf("the policy kept: %s", rec.Policy)
	}
	var routed []ledger.HistoryRow
	for _, h := range rec.History {
		if h.Kind == ledger.HistoryRouted {
			routed = append(routed, h)
		}
	}
	if len(routed) != 2 || routed[0].By != "local/1" || routed[1].By != "router:switchman" || routed[1].OpID != "" || routed[0].OpID != "" {
		t.Fatalf("the routed rows: %+v", routed)
	}
	var dt map[string]any
	_ = json.Unmarshal([]byte(routed[1].Detail), &dt)
	if dt["revision"] != "exttest-r1" || dt["rule"] != "architecture decisions" || !strings.Contains(mustJSON(dt["to"]), `"Ana"`) {
		t.Fatalf("the router's routed row: %s", routed[1].Detail)
	}
	// The router was told the record with what it says of itself.
	var told ext.Decision
	for _, d := range f.fr.Decisions() {
		if d.Request == did {
			told = d
		}
	}
	if told.Record == nil || told.Record.Type != "approval" || told.Record.Origin != "agent" || told.Record.Task != "build" {
		t.Fatalf("the router was told %+v", told)
	}
}

// Ana, eligible, answers through the decide route: the router is asked
// with her subject before the response's transaction, the receipt names
// the group she answered for, and the router is asked again before the
// answer is applied; it is applied once.
func TestEligibleSubjectAnswers(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	f.inForce(did)
	status, data, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did))
	if status != http.StatusCreated {
		t.Fatalf("ana's answer: %d %s", status, data)
	}
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	rc := f.receiptOf(t, did, 1)
	if rc.Status != "accepted" || len(rc.Groups) != 1 || rc.Groups[0] != "architecture" || rc.Via != ext.ViaRole || rc.Ref != "ref-ana" || rc.Override || rc.Policy != "" {
		t.Fatalf("ana's receipt: %+v", rc)
	}
	asked := 0
	for _, q := range f.fr.Confirms() {
		if q.Decision.Request == did && q.Subject == "ana" && q.Revision == "exttest-r1" {
			asked++
		}
	}
	if asked != 3 {
		t.Fatalf("the router was asked about ana %d times, want at the decide route's admission, the response and the application", asked)
	}
}

// Bob, a decide principal the policy does not name, does not read the
// decision (404) nor answer it (404); ana, named by the policy and no
// longer eligible by the router's confirmation, is refused not_eligible,
// her refusal kept.
func TestIneligibleDecidePrincipalIsRefused(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	f.inForce(did)
	if status, data := f.readAs(t, passBob, did); status != http.StatusNotFound {
		t.Fatalf("bob's read: %d %s", status, data)
	}
	if status, data, _ := f.respondAs(t, passBob, did, "b1", 0, f.rev(t, did)); status != http.StatusNotFound {
		t.Fatalf("bob's answer: %d %s", status, data)
	}
	if status, data := f.readAs(t, passAna, did); status != http.StatusOK {
		t.Fatalf("ana's read: %d %s", status, data)
	}
	// Her eligibility ends between the route's admission (the router's
	// first answer about her) and the response's own ask (its second).
	var mu sync.Mutex
	asked := 0
	f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
		mu.Lock()
		defer mu.Unlock()
		if q.Subject != "ana" {
			return ext.Confirmation{InForce: true}
		}
		asked++
		if asked == 1 {
			return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
		}
		return ext.Confirmation{InForce: true}
	})
	status, data, out := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did))
	if status != http.StatusForbidden || errorCode(out) != crew.CodeNotEligible || !strings.Contains(string(data), "You are not among those who may answer "+did) {
		t.Fatalf("ana's answer once not eligible: %d %s", status, data)
	}
	rec := f.record(t, did)
	if len(rec.Responses) != 1 || rec.Responses[0].Status != ledger.ResponseRefused || rec.State != ledger.DecisionOpen {
		t.Fatalf("the record after the refusal: %+v", rec.Responses)
	}
}

// The operator, not eligible, is refused not_eligible without an
// override, and override_refused with one whose reason is short; an
// override with its reason is accepted, recorded as one, and applied.
func TestOperatorNeedsAnExplicitOverride(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	f.inForce(did)
	rev := f.rev(t, did)
	out := f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, rev, false, ""), http.StatusForbidden, crew.CodeNotEligible)
	if !strings.Contains(mustJSON(out), "Ana answers it") {
		t.Fatalf("the plain answer's refusal: %v", out)
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o2", 0, rev, true, "too short"), http.StatusForbidden, crew.CodeOverrideRefused)
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o3", 0, rev, true, "Ana is away this week"), http.StatusCreated, "")
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	if rc := f.receiptOf(t, did, 3); !rc.Override || rc.Status != "accepted" || !strings.Contains(rc.Words, "overriding its router's policy") {
		t.Fatalf("the override's receipt: %+v", rc)
	}
	if rc := f.receiptOf(t, did, 1); rc.Code != crew.CodeNotEligible {
		t.Fatalf("the plain answer's kept receipt: %+v", rc)
	}
}

// A policy whose override is none refuses the operator's override,
// kept; the decision stays open for the persons it names.
func TestOverrideNoneRefusesTheOperator(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	f.inForce(did)
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), true, "Ana is away this week"), http.StatusForbidden, crew.CodeOverrideRefused)
	if rec := f.record(t, did); rec.State != ledger.DecisionOpen || len(rec.Responses) != 1 {
		t.Fatalf("after the refused override: %s %d responses", rec.State, len(rec.Responses))
	}
	// The plain answer's refusal never sends the operator to an override
	// the policy refuses.
	out := f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o2", 0, f.rev(t, did), false, ""), http.StatusForbidden, crew.CodeNotEligible)
	if words := mustJSON(out); strings.Contains(words, "by an override") || !strings.Contains(words, "allows no override") {
		t.Fatalf("the plain answer's refusal under override none: %s", words)
	}
}

// An override DR1's own checks refuse (a card read at an older rev, a
// decision Ana has answered) is kept refused with nothing of the policy's
// judgement: no override, no group, no local policy.
func TestARefusedOverrideCarriesNoOverride(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	f.inForce(did)
	rev := f.rev(t, did)
	status, data, out := f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, rev-1, true, "Ana is away this week"))
	if errorCode(out) != crew.CodeStaleDecision {
		t.Fatalf("an override at rev %d: %d %s", rev-1, status, data)
	}
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, rev); status != http.StatusCreated {
		t.Fatalf("ana's answer: %d %s", status, data)
	}
	status, data, out = f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o2", 0, rev, true, "Ana is away this week"))
	if errorCode(out) != crew.CodeDecisionAnswered {
		t.Fatalf("an override after ana's answer: %d %s", status, data)
	}
	for _, k := range []int{1, 3} {
		rc := f.receiptOf(t, did, k)
		if rc.Status != "refused" || rc.Override || rc.Groups != nil || rc.Via != "" || rc.Ref != "" || rc.Policy != "" || rc.PolicyWhy != "" {
			t.Fatalf("the refused override %d's receipt: %+v", k, rc)
		}
		raw := f.record(t, did).Responses[k-1].Receipt
		if strings.Contains(raw, `"override"`) || strings.Contains(raw, `"groups"`) {
			t.Fatalf("the refused override %d kept %s", k, raw)
		}
	}
}

// Not in force (the router says so, with its why): the operator answers
// under local/1, no override needed, the receipt keeping local/1 and the
// why; ana, named by the policy, is refused not_approver.
func TestPolicyNotInForceIsLocal1(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	f.fr.SetConfirm(did, func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{Why: "the roster is being moved"} })
	// Not in force, the policy admits no decide principal: the decide
	// route does not show it to ana at all.
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did)); status != http.StatusNotFound {
		t.Fatalf("ana's answer not in force: %d %s", status, data)
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusCreated, "")
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	if rc := f.receiptOf(t, did, 1); rc.Policy != "local/1" || rc.PolicyWhy != "the roster is being moved" || rc.Override {
		t.Fatalf("the operator's receipt: %+v", rc)
	}
}

// Behind, or another revision: 409 stale_policy, unkept (no row), and the
// same response sent again once the router stands behind its policy is
// judged again.
func TestStalePolicyRefused(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	for _, c := range []ext.Confirmation{{InForce: true, Behind: true}, {InForce: true, Revision: "exttest-r2"}} {
		f.fr.SetConfirm(did, func(ext.ConfirmQuery) ext.Confirmation { return c })
		status, data, out := f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), true, "Ana is away this week"))
		if status != http.StatusConflict || errorCode(out) != crew.CodeStalePolicy {
			t.Fatalf("ana's answer with %+v: %d %s", c, status, data)
		}
		if n := len(f.record(t, did).Responses); n != 0 {
			t.Fatalf("a stale_policy refusal kept %d rows", n)
		}
	}
}

// A response refused stale_policy is judged again when sent again: the
// same response id, once the router confirms ana, is accepted.
func TestStalePolicyRefusalIsNotKept(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	f.fr.SetConfirm(did, func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{InForce: true, Behind: true} })
	body := operatorBody("o1", 0, f.rev(t, did), true, "Ana is away this week")
	if status, data, _ := f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+did+"/respond", body); status != http.StatusConflict {
		t.Fatalf("the first send: %d %s", status, data)
	}
	f.inForce(did)
	if status, data, _ := f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+did+"/respond", body); status != http.StatusCreated {
		t.Fatalf("the same response sent again: %d %s", status, data)
	}
}

// policyFakeClock is a clock a test moves.
type policyFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *policyFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *policyFakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// pastTheBound answers the router's retry for ever (c), and checks that
// the operator is refused stale_policy within ext.MaxBehind of the first
// such answer and answers under local/1 past it.
func pastTheBound(t *testing.T, c ext.Confirmation) {
	f := newPolicyFixture(t)
	clock := &policyFakeClock{now: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)}
	f.srv.policyClock.mu.Lock()
	f.srv.policyClock.now = clock.Now
	f.srv.policyClock.mu.Unlock()
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	f.fr.SetConfirm(did, func(ext.ConfirmQuery) ext.Confirmation { return c })
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusConflict, crew.CodeStalePolicy)
	clock.add(ext.MaxBehind)
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusConflict, crew.CodeStalePolicy)
	clock.add(time.Second)
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusCreated, "")
	if rc := f.receiptOf(t, did, 1); rc.Policy != "local/1" || !strings.Contains(rc.PolicyWhy, "did not settle") {
		t.Fatalf("the operator's receipt past the bound: %+v", rc)
	}
}

// A router answering Behind for ever is held to ext.MaxBehind: past it,
// the operator answers under local/1.
func TestBehindPastItsBoundIsNotInForce(t *testing.T) {
	pastTheBound(t, ext.Confirmation{InForce: true, Behind: true})
}

// A router answering in force at another revision for ever is held to
// ext.MaxBehind as well.
func TestRevisionMismatchPastItsBoundIsNotInForce(t *testing.T) {
	pastTheBound(t, ext.Confirmation{InForce: true, Revision: "exttest-r2"})
}

// Not in force of another revision is a try again too, held to the same
// bound: past it the operator answers under local/1.
func TestNotInForceOfAnotherRevisionPastItsBoundIsLocal(t *testing.T) {
	pastTheBound(t, ext.Confirmation{Why: "moving", Revision: "exttest-r2"})
}

// An answer in force clears the clock: Behind, then in force for longer
// than ext.MaxBehind, then Behind once is stale_policy again, not local/1.
func TestBehindBoundClearedByAnInForceAnswer(t *testing.T) {
	f := newPolicyFixture(t)
	clock := &policyFakeClock{now: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)}
	f.srv.policyClock.mu.Lock()
	f.srv.policyClock.now = clock.Now
	f.srv.policyClock.mu.Unlock()
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	behind := true
	var mu sync.Mutex
	f.fr.SetConfirm(did, func(ext.ConfirmQuery) ext.Confirmation {
		mu.Lock()
		defer mu.Unlock()
		if behind {
			return ext.Confirmation{InForce: true, Behind: true}
		}
		return ext.Confirmation{InForce: true}
	})
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusConflict, crew.CodeStalePolicy)
	mu.Lock()
	behind = false
	mu.Unlock()
	// Read in force (the operator's read asks it), then far past the bound.
	if now := f.expect(t, http.MethodGet, "/api/decisions/"+did, nil, http.StatusOK, "")["policyNow"].(map[string]any); now["inForce"] != true {
		t.Fatalf("policyNow: %v", now)
	}
	clock.add(3 * ext.MaxBehind)
	mu.Lock()
	behind = true
	mu.Unlock()
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusConflict, crew.CodeStalePolicy)
}

// The opening window: while the router's Route is held, a record opened
// under a router that routes policies refuses the operator's response
// stale_policy, nothing kept; released, the record is routed.
// holdWindows holds every opening window of s open for the test's
// length: the windows' clock stopped, and the router's held route given a
// wait no test outlasts, so neither of the product's two bounds (twice
// ext.RouteWait, the route's own wait) closes a window before the test's
// own release of the route does.
func holdWindows(t *testing.T, s *Server) {
	t.Helper()
	at := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	s.router.windows.mu.Lock()
	s.router.windows.now = func() time.Time { return at }
	s.router.windows.mu.Unlock()
	long := time.Hour
	extWaiting.Store(&long)
	t.Cleanup(func() { extWaiting.Store(nil) })
}

func TestOpeningWindowRefusesTheOperator(t *testing.T) {
	f := newPolicyFixture(t)
	holdWindows(t, f.srv)
	release := f.fr.HoldRoutes()
	defer release()
	f.fr.SetPolicy("dr-1", anaPolicy(ext.OverrideOperator))
	did := f.open(t, "r1")
	until(t, "the router told of "+did, func() bool {
		for _, d := range f.fr.Decisions() {
			if d.Request == did {
				return true
			}
		}
		return false
	})
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, 1, false, ""), http.StatusConflict, crew.CodeStalePolicy)
	if n := len(f.record(t, did).Responses); n != 0 {
		t.Fatalf("the window's refusal kept %d rows", n)
	}
	release()
	until(t, did+"'s policy written", func() bool { return ledger.RouterPolicy(f.record(t, did).Policy) })
}

// Ana's answer is accepted, then the router no longer confirms her at the
// application: the decision is superseded by a fresh one, its words saying
// so, and nothing of her answer is applied.
func TestConfirmFailingAtApplicationSupersedes(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	var mu sync.Mutex
	asked := 0
	f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
		mu.Lock()
		defer mu.Unlock()
		if q.Subject != "ana" {
			return ext.Confirmation{InForce: true}
		}
		asked++
		if asked <= 2 {
			// The decide route's admission and the response's own ask.
			return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
		}
		return ext.Confirmation{InForce: true}
	})
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did)); status != http.StatusCreated {
		t.Fatalf("ana's answer: %d %s", status, data)
	}
	old := f.decisionWhen(t, did, "superseded", inState(ledger.DecisionSuperseded))
	if !strings.Contains(old["why"].(string), "could not be confirmed") || old["supersededBy"] != "dr-2" {
		t.Fatalf("the superseded record: %v", old)
	}
	if fresh := f.record(t, "dr-2"); fresh.State != ledger.DecisionOpen || ledger.RouterPolicy(fresh.Policy) && !ledger.PolicySpare(fresh.Policy) {
		t.Fatalf("the fresh decision: %s %s", fresh.State, fresh.Policy)
	}
}

// A router's own answer is refused under its policy, as under local/1,
// and kept: never applied.
func TestRoutersAnswerRefusedUnderItsPolicy(t *testing.T) {
	f := newPolicyFixture(t)
	f.fr.SetPolicy("dr-1", anaPolicy(ext.OverrideOperator))
	f.fr.SetRoute("dr-1", ext.Route{Answer: &ext.Answer{Input: "1", By: ext.Principal{Subject: "ana", Name: "Ana"}}})
	did := f.open(t, "r1")
	until(t, "the router's answer kept", func() bool { return len(f.record(t, did).Responses) == 1 })
	rec := f.record(t, did)
	if !ledger.RouterPolicy(rec.Policy) || rec.State != ledger.DecisionOpen || rec.Responses[0].Status != ledger.ResponseRefused {
		t.Fatalf("after the router's answer: %s %+v", rec.State, rec.Responses)
	}
	if rc := f.receiptOf(t, did, 1); rc.Code != crew.CodeNotApprover {
		t.Fatalf("the router's receipt: %+v", rc)
	}
}

// A group with nobody eligible keeps its gap: the operator's plain answer
// is refused naming the gap, the decision stays open, and only an
// override answers it.
func TestGapKeepsTheRequirement(t *testing.T) {
	f := newPolicyFixture(t)
	p := anaPolicy(ext.OverrideOperator)
	p.Groups[0].Eligible, p.Groups[0].Gap = nil, "Waiting for an architecture owner: none is assigned in CND"
	did := f.routed(t, "r1", "dr-1", p)
	f.inForce(did)
	out := f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusForbidden, crew.CodeNotEligible)
	if !strings.Contains(mustJSON(out), "none is assigned in CND") {
		t.Fatalf("the refusal: %v", out)
	}
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did)); status != http.StatusNotFound {
		t.Fatalf("ana, named by no group: %d %s", status, data)
	}
	if rec := f.record(t, did); rec.State != ledger.DecisionOpen {
		t.Fatalf("the gap's decision: %s", rec.State)
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o2", 0, f.rev(t, did), true, "no owner is assigned yet"), http.StatusCreated, "")
}

// The guard refuses a policy on a screen's question, one past one group of
// quorum 1, one carrying the server's counts, and one from a router that
// does not confirm policies: the whole route is refused (the free
// default's empty route).
func TestScreenQuestionPolicyRefusedByTheGuard(t *testing.T) {
	e := newTestEnv(t, nil)
	p := exttest.SamplePolicy()
	fr := exttest.NewFakeRouter("switchman")
	g := guardRouter{s: e.srv, name: "switchman", p: fr, ready: readyNow()}
	screen := exttest.SampleDecision()
	fr.SetRoute(screen.ID, ext.Route{Policy: &p})
	slot, ok := g.take()
	if !ok {
		t.Fatal("no slot")
	}
	if rt, ok := g.routeIn(context.Background(), slot, screen); ok || rt.Policy != nil {
		t.Fatalf("a policy on a screen's question: %+v %v", rt, ok)
	}
}

// readyNow is a provider marked started.
func readyNow() *atomic.Bool {
	b := &atomic.Bool{}
	b.Store(true)
	return b
}

// A policy of two groups (PR-9's) is refused by the guard: the record
// keeps the server's own policy.
func TestMoreThanOneGroupRefusedUntilGroups(t *testing.T) {
	f := newPolicyFixture(t)
	p := anaPolicy(ext.OverrideOperator)
	p.Pinned.Groups = append(p.Pinned.Groups, ext.PinnedGroup{Role: "security", Quorum: 1})
	p.Groups = append(p.Groups, ext.Requirement{Role: "security", Label: "security owner", Quorum: 1, Rule: "r-2", Eligible: []ext.Eligible{{Subject: "bob", Name: "Bob", Via: ext.ViaRole, Ref: "ref-bob"}}})
	if err := p.Valid(*exttest.SampleRecord()); err != nil {
		t.Fatalf("the two groups' policy is not valid for ext: %v", err)
	}
	records := watchRecords(t)
	f.fr.SetPolicy("dr-1", p)
	did := f.open(t, "r1")
	records.wait(t, "routed", did)
	if rec := f.record(t, did); ledger.RouterPolicy(rec.Policy) {
		t.Fatalf("a two groups' policy written: %s", rec.Policy)
	}
}

// A router that does not confirm policies (the api's fake) answering a
// policy: refused by the guard, the record under local/1 as on a server
// with no router, its column byte for byte the free build's.
func TestPolicyFromARouterWithoutConfirmRefused(t *testing.T) {
	records := watchRecords(t)
	pol := exttest.SamplePolicy()
	fr := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		if d.Request == "" {
			return ext.Route{}, nil
		}
		return ext.Route{Policy: pol.Clone()}, nil
	}}
	f := routerDecisionFixture(t, fr, nil)
	if f.srv.runs.RoutesPolicies() {
		t.Fatal("a router that does not confirm policies routes them")
	}
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	step := records.wait(t, "routed", "dr-1")
	if !errors.Is(step.err, errUnrouted) {
		t.Fatalf("the route with a policy: %v", step.err)
	}
	n, _ := ledger.ParseDecisionID("dr-1")
	rec, _, err := f.srv.ledger.Decision(t.Context(), n)
	if err != nil || rec.Policy != `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}` {
		t.Fatalf("the record's policy: %s %v", rec.Policy, err)
	}
}

// With no router registered nothing of a router's policy appears: the
// policy column is the free build's byte for byte, the operator's read
// has no policyNow, a response is judged as on main, and no outbox row is
// written at the decision's end.
func TestNoRouterNothingChanges(t *testing.T) {
	f := newDecideFixture(t, nil)
	if f.srv.runs.RoutesPolicies() {
		t.Fatal("a server with no router routes policies")
	}
	did := f.open(t, "r1")
	rec := f.record(t, did)
	if rec.Policy != `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}` {
		t.Fatalf("the policy column: %s", rec.Policy)
	}
	if _, ok := f.expect(t, http.MethodGet, "/api/decisions/"+did, nil, http.StatusOK, "")["policyNow"]; ok {
		t.Fatal("policyNow on a server with no router")
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, 1, false, ""), http.StatusCreated, "")
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	if rc := f.receiptOf(t, did, 1); rc.Policy != "" || len(rc.Groups) != 0 || rc.Override {
		t.Fatalf("the receipt: %+v", rc)
	}
	if rows, err := f.srv.ledger.Outcomes(t.Context(), 0, 10); err != nil || len(rows) != 0 {
		t.Fatalf("outbox rows %v: %v", rows, err)
	}
}

// A record under a router's policy handed to the engine with no
// confirmation (a door that did not ask the router) is refused
// stale_policy, nothing written, never judged as not in force; a record
// still under the server's own policy is answered by the operator as on
// main, on the same server.
func TestRespondWithoutConfirmationIsStalePolicy(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	choice := 0
	_, err := f.srv.runs.RespondDecision(t.Context(), did, crew.Response{ResponseID: "o1", Choice: &choice, ExpectedRev: f.rev(t, did)}, "admin", crew.Authority{Via: crew.ViaWorkbench, Grant: string(ext.GrantAdmin)})
	var de *crew.DecisionError
	if !errors.As(err, &de) || de.Code != crew.CodeStalePolicy {
		t.Fatalf("an answer with no confirmation: %v", err)
	}
	if n := len(f.record(t, did).Responses); n != 0 {
		t.Fatalf("%d rows kept", n)
	}
	// The router answers no policy for the next: under the server's own
	// policy it is the operator's, as on main.
	plain := f.open(t, "r2")
	until(t, plain+" told", func() bool {
		for _, d := range f.fr.Decisions() {
			if d.Request == plain {
				return true
			}
		}
		return false
	})
	until(t, plain+"'s window closed", func() bool { return !f.srv.router.windows.in(plain) })
	f.expect(t, http.MethodPost, "/api/decisions/"+plain+"/respond", operatorBody("o2", 0, f.rev(t, plain), false, ""), http.StatusCreated, "")
}

// recipientSink is a sink that says whether it delivers to a notice's
// recipient alone.
type recipientSink struct {
	*recSink
	alone bool
}

func (s recipientSink) DeliversToRecipients() bool { return s.alone }

// A notice meant for one person (its Recipient set) reaches only a sink
// that delivers to that person alone: never a sink that is no
// RecipientSink, nor one answering false. A notice with no recipient
// reaches every sink that takes notices, as on main.
func TestRecipientNoticeReachesOnlyRecipientSinks(t *testing.T) {
	plain := &recSink{name: "plain", events: []string{ext.TypeProvider}}
	no := recipientSink{&recSink{name: "no", events: []string{ext.TypeProvider}}, false}
	yes := recipientSink{&recSink{name: "yes", events: []string{ext.TypeProvider}}, true}
	e := newNotifyEnv(t, nil, nil, plain, no, yes)
	for _, n := range []ext.Notification{
		{Type: ext.TypeProvider, Origin: "switchman", Message: "for ana", Recipient: "ana"},
		{Type: ext.TypeProvider, Origin: "switchman", Message: "for everyone"},
	} {
		for _, name := range []string{"plain", "no", "yes"} {
			if err := sinkOf(t, e.srv, name).deliver(t.Context(), n); err != nil {
				t.Fatal(err)
			}
		}
	}
	for s, want := range map[*recSink][]string{plain: {"for everyone"}, no.recSink: {"for everyone"}, yes.recSink: {"for ana", "for everyone"}} {
		s.mu.Lock()
		var got []string
		for _, n := range s.got {
			got = append(got, n.Message)
		}
		s.mu.Unlock()
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s was handed %q, want %q", s.name, got, want)
		}
	}
}

// A subject's key: two subjects of each kind, two keys; one subject twice,
// one key; every key of ext.DecisionSubject's shape, which refuses a key
// in capitals and a short one.
func TestDecisionRecordSubjectKey(t *testing.T) {
	pairs := [][2]crew.DecisionSubject{
		{{Kind: "commit", Commit: "abc"}, {Kind: "commit", Commit: "abd"}},
		{{Kind: "candidate", Run: "r1", Task: "build", N: 1, Commit: "abc"}, {Kind: "candidate", Run: "r1", Task: "build", N: 2, Commit: "abc"}},
		{{Kind: "artifact", Commit: "abc", Path: "a.md", SHA256: "d1"}, {Kind: "artifact", Commit: "abc", Path: "b.md", SHA256: "d1"}},
		{{Kind: "plan", Plan: "pl-1", Digest: "d1"}, {Kind: "plan", Plan: "pl-2", Digest: "d1"}},
	}
	for _, p := range pairs {
		a, b := SubjectKey(p[0]), SubjectKey(p[1])
		if a == b || a != SubjectKey(p[0]) {
			t.Errorf("%s: keys %s and %s", p[0].Kind, a, b)
		}
		for _, k := range []string{a, b} {
			if err := (ext.DecisionSubject{Kind: p[0].Kind, Key: k}).Valid(); err != nil {
				t.Errorf("%s: key %s: %v", p[0].Kind, k, err)
			}
		}
	}
	key := SubjectKey(pairs[0][0])
	for _, bad := range []string{strings.ToUpper(key), key[:31]} {
		if (ext.DecisionSubject{Kind: "commit", Key: bad}).Valid() == nil {
			t.Errorf("the key %q is valid", bad)
		}
	}
}

// A record that carries a router's policy, told again (a pass after a
// restart), writes nothing when the router answers another policy: a
// change of policy is a re-route's. The engine's policy transaction
// refuses it routed_by_policy, its rev and policy as they were.
func TestRetoldRecordWithAPolicyWritesNothing(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	before := f.record(t, did)
	other := anaPolicy(ext.OverrideNone)
	other.Revision = "exttest-r2"
	// The same policy is its repeat: its first receipt.
	if rc, err := f.srv.runs.RoutePolicy(t.Context(), did, "switchman", before.Rev, anaPolicy(ext.OverrideOperator), nil, ""); err != nil || !rc.Repeat {
		t.Fatalf("the same policy again: %+v %v", rc, err)
	}
	// Another revision is no repeat of it, by the same router or by
	// another router's name (the module registered again under a new
	// one): refused, a change of policy being a re-route's.
	for _, by := range []string{"switchman", "relay"} {
		_, err := f.srv.runs.RoutePolicy(t.Context(), did, by, before.Rev, other, nil, "")
		var de *crew.DecisionError
		if !errors.As(err, &de) || de.Code != crew.CodeRoutedByPolicy {
			t.Fatalf("a second policy by %s: %v", by, err)
		}
	}
	// Told again by the router's own path: nothing written.
	records := watchRecords(t)
	f.fr.SetPolicy(did, other)
	f.srv.router.retell(did)
	records.wait(t, "routed", did)
	after := f.record(t, did)
	if after.Rev != before.Rev || after.Policy != before.Policy || len(after.History) != len(before.History) {
		t.Fatalf("told again: rev %d→%d, policy %s", before.Rev, after.Rev, after.Policy)
	}
}

// A comparison's selection never takes a policy: told to the router with
// its origin, the router's policy for it writes nothing (not even a routed
// row), and the engine's policy transaction refuses it.
func TestCompareSelectionNeverTakesAPolicy(t *testing.T) {
	f := newPolicyFixture(t)
	var n int64
	if _, err := f.srv.ledger.Transition(t.Context(), f.run, func(tx *ledger.Tx) error {
		got, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginCompare, Type: ledger.DecisionApproval, Fingerprint: "fp-cmp",
			Request: `{"question":"Which approach does CND-12 continue with?",` +
				`"options":[{"label":"approach-a at 4b825dc","subject":{"kind":"candidate","run":"` + f.run + `","task":"approach-a","n":1,"commit":"4b825dc"}},` +
				`{"label":"None of these"}],"recommendation":-1,"authorizes":"Continuing the selected candidate.",` +
				`"subject":{"kind":"comparison","run":"` + f.run + `","base":"abc123","digest":"d1"}}`,
			Policy: `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`})
		n = got.N
		return err
	}); err != nil {
		t.Fatal(err)
	}
	did := ledger.DecisionID(n)
	records := watchRecords(t)
	f.fr.SetPolicy(did, anaPolicy(ext.OverrideNone))
	f.srv.router.record(did)
	records.wait(t, "routed", did)
	rec := f.record(t, did)
	if ledger.RouterPolicy(rec.Policy) || rec.Rev != 1 {
		t.Fatalf("the selection after its route: rev %d %s", rec.Rev, rec.Policy)
	}
	for _, h := range rec.History {
		if h.By == "router:switchman" {
			t.Fatalf("a router's row on the selection: %+v", h)
		}
	}
	_, err := f.srv.runs.RoutePolicy(t.Context(), did, "switchman", 1, anaPolicy(ext.OverrideNone), nil, "")
	var de *crew.DecisionError
	if !errors.As(err, &de) || de.Code != crew.CodeInvalid {
		t.Fatalf("a policy for the selection: %v", err)
	}
	if f.srv.router.windows.in(did) {
		t.Fatal("a window opened for the selection")
	}
}

// An override by an identity's admin principal keeps DR1's identity
// recheck: the provider no longer admitting its token after the response
// committed, the decision is superseded by a fresh one, never applied.
func TestOverrideByAnAdminIdentityIsRechecked(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	f.inForce(did)
	f.id.recheckAs(passAdmin, ext.Principal{}, false)
	// The gatedIdentity answers the third ask after recheckAs: this read is
	// the first, the response's own authentication the second, and its
	// recheck once it committed the third.
	if status, data := f.readAs(t, passAdmin, did); status != http.StatusOK {
		t.Fatalf("the admin's read: %d %s", status, data)
	}
	status, data, _ := f.callAs(t, passAdmin, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), true, "Ana is away this week"))
	if status != http.StatusCreated {
		t.Fatalf("the admin's override: %d %s", status, data)
	}
	if rc := f.receiptOf(t, did, 1); !rc.Override {
		t.Fatalf("the override's receipt: %+v", rc)
	}
	if old := f.decisionWhen(t, did, "superseded", inState(ledger.DecisionSuperseded)); !strings.Contains(old["why"].(string), "could not be rechecked") {
		t.Fatalf("the superseded record: %v", old)
	}
}

// policyNowOf is pass's read of did's policyNow, nil when the read carries
// none.
func (f *decideFixture) policyNowOf(t *testing.T, pass, did string) *policyNowReply {
	t.Helper()
	status, data := f.readAs(t, pass, did)
	if status != http.StatusOK {
		t.Fatalf("%s's read of %s: %d %.300s", pass, did, status, data)
	}
	var out struct {
		PolicyNow *policyNowReply `json:"policyNow"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out.PolicyNow
}

// An identity's operator the policy names and the router confirms is
// eligible on its read, so its card does not hold it back, and its plain
// answer is accepted as eligible, no override; the workbench token's read
// of the same record is in force and not eligible.
func TestAnEligibleAdminIdentityReadsAsEligible(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", withEligible(anaPolicy(ext.OverrideNone), "exttest-r1", "nate"))
	f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
		if q.Subject == "nate" {
			return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
		}
		return ext.Confirmation{InForce: true}
	})
	if now := f.policyNowOf(t, passAdmin, did); now == nil || !now.InForce || !now.Eligible {
		t.Fatalf("nate's read: %+v", now)
	}
	if now := f.policyNowOf(t, adminToken, did); now == nil || !now.InForce || now.Eligible {
		t.Fatalf("the workbench token's read: %+v", now)
	}
	status, data, _ := f.callAs(t, passAdmin, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("n1", 0, f.rev(t, did), false, ""))
	if status != http.StatusCreated {
		t.Fatalf("nate's plain answer: %d %s", status, data)
	}
	if rc := f.receiptOf(t, did, 1); rc.Override || len(rc.Groups) != 1 {
		t.Fatalf("nate's receipt: %+v", rc)
	}
}

// A record that carries a router's policy, read on a server with no router
// that confirms policies, reads as not in force, in the respond route's
// own words: the operator answers it.
func TestARoutedRecordWithNoRouterReadsAsNotInForce(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	n, _ := ledger.ParseDecisionID(did)
	rev := f.rev(t, did)
	if _, err := f.srv.ledger.Transition(t.Context(), f.run, func(tx *ledger.Tx) error {
		ok, err := tx.PutPolicy(n, routedPolicyText, rev)
		if err == nil && !ok {
			err = errors.New("the policy was not written")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if now := f.policyNowOf(t, adminToken, did); now == nil || now.InForce || now.Why != "no router confirms its policy now" {
		t.Fatalf("the read with no router: %+v", now)
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusCreated, "")
}

// routedPolicyText is a router's policy naming ana, as a record keeps it.
const routedPolicyText = `{"revision":"r1","approver":"router","quorum":1,"authority":[{"kind":"operator"}],"router":"switchman","scope":"s1",` +
	`"pinned":{"rule":"r-1","groups":[{"role":"architecture","quorum":1}],"override":"operator","notify":"now"},` +
	`"groups":[{"role":"architecture","label":"architecture owner","quorum":1,"rule":"r-1","eligible":[{"subject":"ana","name":"Ana","via":"role","ref":"ref-1"}]}],` +
	`"override":"operator","routedAt":"2026-11-19T09:00:00Z","spare":true,"fallback":"local/1"}`

// A router that confirms leaving its revision out confirms the revision
// it was asked about, and a confirmation not in force is about one
// revision too: re-routed between the respond route's ask and the
// response's transaction, an answer is refused stale_policy, nothing
// kept, whether it was sent at the rev read before the re-route or at the
// rev after it: the operator's override with the policy in force, and the
// operator's plain answer with the policy not in force.
func TestARerouteBetweenTheAskAndTheAnswerIsStale(t *testing.T) {
	f := newPolicyFixture(t)
	for i, c := range []struct {
		name     string
		conf     ext.Confirmation
		override bool
		after    bool
	}{
		{"an override at the rev read before", ext.Confirmation{InForce: true}, true, false},
		{"an override at the rev after", ext.Confirmation{InForce: true}, true, true},
		{"a plain answer not in force at the rev after", ext.Confirmation{Why: "the roster moves"}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			did := f.routed(t, fmt.Sprintf("r%d", i+1), fmt.Sprintf("dr-%d", i+1), anaPolicy(ext.OverrideOperator))
			var once sync.Once
			f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
				if q.Subject == "" {
					once.Do(func() {
						rec := f.record(t, did)
						if _, err := f.srv.runs.Reroute(context.Background(), did, "switchman", withEligible(anaPolicy(ext.OverrideOperator), "exttest-r2", "ana"), ledger.HistoryRerouted, "op-m", rec.Rev); err != nil {
							t.Errorf("%s: the re-route: %v", c.name, err)
						}
					})
				}
				return c.conf
			})
			rev := f.rev(t, did)
			if c.after {
				rev++ // the rev the re-route commits
			}
			rationale := ""
			if c.override {
				rationale = "Ana is away this week"
			}
			status, data, out := f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, rev, c.override, rationale))
			if status != http.StatusConflict || errorCode(out) != crew.CodeStalePolicy {
				t.Fatalf("%s: %d %s", c.name, status, data)
			}
			if n := len(f.record(t, did).Responses); n != 0 {
				t.Fatalf("%s: %d responses kept", c.name, n)
			}
		})
	}
}

// The walk's sequence (#1063): a routed decision answered by Ana, eligible,
// and applied; its router then holds it no more and answers "in force,
// behind" for it. A second answer says the decision is answered, by Ana,
// as on a server with no router, whether the operator answers plainly or
// by override; nothing more is recorded. An open decision under a policy
// that is behind is still refused stale_policy, unkept.
func TestASecondAnswerToARoutedDecisionSaysWhoAnswered(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
	f.inForce(did)
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did)); status != http.StatusCreated {
		t.Fatalf("ana's answer: %d %s", status, data)
	}
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	behind := func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{InForce: true, Behind: true} }
	f.fr.SetConfirm(did, behind)
	rec := f.record(t, did)
	for i, override := range []bool{false, true} {
		rationale := ""
		if override {
			rationale = "Ana is away this week"
		}
		status, data, out := f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody(fmt.Sprintf("o%d", i), 1, rec.Rev, override, rationale))
		if status != http.StatusConflict || errorCode(out) != crew.CodeDecisionAnswered || !strings.Contains(string(data), "answered by Ana") {
			t.Fatalf("a second answer (override %v): %d %s", override, status, data)
		}
	}
	if after := f.record(t, did); after.State != ledger.DecisionApplied || after.Rev != rec.Rev {
		t.Fatalf("the decision after the second answers: %s rev %d", after.State, after.Rev)
	}
	// An open decision under a policy that is behind: stale_policy, unkept.
	open := f.routed(t, "r2", "dr-2", anaPolicy(ext.OverrideOperator))
	f.fr.SetConfirm(open, behind)
	f.expect(t, http.MethodPost, "/api/decisions/"+open+"/respond", operatorBody("p1", 0, f.rev(t, open), true, "Ana is away this week"), http.StatusConflict, crew.CodeStalePolicy)
	if n := len(f.record(t, open).Responses); n != 0 {
		t.Fatalf("a stale_policy refusal kept %d rows", n)
	}
}
