package crew

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The policy's transaction (RoutePolicy) after the router's other writes
// to the record (docs/plans/decision-policy.md, the policy's transaction
// and a for change telling the router again): its repeat is the router's
// routing row of the same revision, and nothing else.

// anaRoute is a router's policy at revision rev naming ana alone in its
// one group, the architecture owner, the operator's override allowed.
func anaRoute(rev string) ext.RoutePolicy {
	return ext.RoutePolicy{Revision: rev, Scope: "s1", Override: ext.OverrideOperator,
		Pinned: ext.PinnedRequirement{Rule: "r-1", Groups: []ext.PinnedGroup{{Role: "architecture", Quorum: 1}}, Override: ext.OverrideOperator, Notify: ext.NotifyNow},
		Groups: []ext.Requirement{{Role: "architecture", Label: "architecture owner", Quorum: 1, Rule: "r-1",
			Eligible: []ext.Eligible{{Subject: "ana", Name: "Ana", Via: ext.ViaRole, Ref: "ref-1"}}}}}
}

// toAna is the router's destinations for the record.
var toAna = []Destination{{Kind: "person", Name: "ana"}}

// routedGate launches a run whose ship gate opens while the router routes
// policies: the engine, its ledger, the run and the gate's id.
func routedGate(t *testing.T, id string) (*Engine, *ledger.Ledger, Run, string) {
	t.Helper()
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	e.SetRouterPolicies(true, nil)
	run := launchGated(t, e, dir, id, repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	return e, ld, run, gateOf(t, e, ld, run.ID).ID
}

// recordOf is decision id's record as the ledger keeps it.
func recordOf(t *testing.T, ld *ledger.Ledger, id string) ledger.DecisionRecord {
	t.Helper()
	return decisionWhen(t, ld, id, "read", func(ledger.DecisionRecord) bool { return true })
}

// policyRevision is the revision of the router's policy rec carries, ""
// for none.
func policyRevision(t *testing.T, rec ledger.DecisionRecord) string {
	t.Helper()
	if !ledger.RouterPolicy(rec.Policy) {
		return ""
	}
	var p struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(rec.Policy), &p); err != nil {
		t.Fatal(err)
	}
	return p.Revision
}

// decideAs is an identity's authority with the decide grant.
func decideAs(e *Engine, subject string) Authority {
	return Authority{Grant: string(ext.GrantDecide), Via: ViaIdentity, Subject: subject, Boot: e.Boot()}
}

// answerRefused checks a response refused with code, kept, nothing accepted.
func answerRefused(t *testing.T, who string, rc ResponseReceipt, err error, code string) {
	t.Helper()
	var de *DecisionError
	if !errors.As(err, &de) || de.Code != code || rc.Status != ledger.ResponseRefused {
		t.Fatalf("%s's answer: %+v %v, want refused %s", who, rc, err, code)
	}
}

// The router routed the gate first with destinations and no policy (its
// one routed row), the operator then gave it to the decide grant, and the
// router, told again, answers its policy: the policy is written at the
// record's next rev, its row a router's change of that revision, and from
// then on only the persons it names are admitted: bob, holding the decide
// grant the record was given to, is refused not_eligible, and the
// operator's plain answer too. The same policy told again is a repeat:
// its first receipt, nothing written; a route with destinations and no
// policy writes nothing; a policy of another revision is refused
// routed_by_policy, a change of policy being a re-route's.
func TestAPolicyAfterTheRoutersEarlierRouteIsWritten(t *testing.T) {
	e, ld, run, id := routedGate(t, "api-sweep-0000a7e1")
	ctx := t.Context()
	if _, err := e.RecordRoute(ctx, id, "switchman", toAna, "the architecture owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RouteDecision(ctx, id, "give-1", recordOf(t, ld, id).Rev, []Destination{{Kind: DestGrant, Grant: string(ext.GrantDecide)}}, "", "admin"); err != nil {
		t.Fatal(err)
	}
	before := recordOf(t, ld, id)
	rc, err := e.RoutePolicy(ctx, id, "switchman", before.Rev, anaRoute("r1"), toAna, "the architecture owner")
	if err != nil || rc.Repeat || rc.Rev != before.Rev+1 {
		t.Fatalf("the policy after the router's route: %+v %v", rc, err)
	}
	rec := recordOf(t, ld, id)
	last := rec.History[len(rec.History)-1]
	if policyRevision(t, rec) != "r1" || rec.Rev != before.Rev+1 || len(rec.History) != len(before.History)+1 ||
		last.Kind != ledger.HistoryRerouted || last.By != "router:switchman" || last.OpID != "" || !json.Valid([]byte(last.Detail)) {
		t.Fatalf("the record under its policy: rev %d (was %d), policy %s, last row %+v", rec.Rev, before.Rev, rec.Policy, last)
	}
	if got := AdmissionOf(rec, Admittee{Grants: []string{string(ext.GrantDecide)}, Via: ViaIdentity, Subject: "ana"}); got != AdmitOpen {
		t.Fatalf("ana under the policy: %d", got)
	}
	if got := AdmissionOf(rec, Admittee{Grants: []string{string(ext.GrantDecide)}, Via: ViaIdentity, Subject: "bob"}); got != AdmitNone {
		t.Fatalf("bob under the policy: %d", got)
	}
	inForce := &ext.Confirmation{InForce: true}
	brc, err := e.RespondDecisionWith(ctx, id, Response{ResponseID: "bob-1", Choice: choice(0), ExpectedRev: rec.Rev}, ext.SubjectActor+"bob", decideAs(e, "bob"), inForce)
	answerRefused(t, "bob", brc, err, CodeNotEligible)
	orc, err := e.RespondDecisionWith(ctx, id, Response{ResponseID: "op-1", Choice: choice(0), ExpectedRev: recordOf(t, ld, id).Rev}, "admin", workbench(e), inForce)
	answerRefused(t, "the operator", orc, err, CodeNotEligible)

	written := recordOf(t, ld, id)
	again, err := e.RoutePolicy(ctx, id, "switchman", before.Rev, anaRoute("r1"), toAna, "the architecture owner")
	if err != nil || !again.Repeat || again.Rev != rc.Rev {
		t.Fatalf("the same policy told again: %+v %v (first %+v)", again, err, rc)
	}
	if _, err := e.RecordRoute(ctx, id, "switchman", []Destination{{Kind: "person", Name: "ben"}}, "the backup"); err != nil {
		t.Fatalf("a route with no policy after the policy: %v", err)
	}
	var de *DecisionError
	if _, err := e.RoutePolicy(ctx, id, "switchman", written.Rev, anaRoute("r2"), toAna, "the architecture owner"); !errors.As(err, &de) || de.Code != CodeRoutedByPolicy {
		t.Fatalf("another policy: %v", err)
	}
	if now := recordOf(t, ld, id); now.Rev != written.Rev || len(now.History) != len(written.History) || policyRevision(t, now) != "r1" {
		t.Fatalf("a repeat or a refusal wrote: rev %d (was %d), %d rows (were %d), policy %s", now.Rev, written.Rev, len(now.History), len(written.History), now.Policy)
	}
	stopRun(t, e, run.ID)
}

// The router's first answer carries its policy (its one routed row, of
// revision r1): told again with r1 it is a repeat, its first receipt and
// nothing written; with r2 it is no repeat, and is refused
// routed_by_policy, a change of policy being a re-route's; a route with
// destinations and no policy after it writes nothing.
func TestAPolicyOfAnotherRevisionIsNoRepeat(t *testing.T) {
	e, ld, run, id := routedGate(t, "api-sweep-0000a7e2")
	ctx := t.Context()
	before := recordOf(t, ld, id)
	rc, err := e.RoutePolicy(ctx, id, "switchman", before.Rev, anaRoute("r1"), toAna, "the architecture owner")
	if err != nil || rc.Repeat {
		t.Fatalf("the first route: %+v %v", rc, err)
	}
	written := recordOf(t, ld, id)
	if last := written.History[len(written.History)-1]; policyRevision(t, written) != "r1" || last.Kind != ledger.HistoryRouted || last.By != "router:switchman" {
		t.Fatalf("the first route wrote %s, last row %+v", written.Policy, last)
	}
	again, err := e.RoutePolicy(ctx, id, "switchman", before.Rev, anaRoute("r1"), toAna, "the architecture owner")
	if err != nil || !again.Repeat || again.Rev != rc.Rev {
		t.Fatalf("the same policy told again: %+v %v", again, err)
	}
	var de *DecisionError
	if _, err := e.RoutePolicy(ctx, id, "switchman", written.Rev, anaRoute("r2"), toAna, "the architecture owner"); !errors.As(err, &de) || de.Code != CodeRoutedByPolicy {
		t.Fatalf("another policy told after the first: %v", err)
	}
	if _, err := e.RecordRoute(ctx, id, "switchman", []Destination{{Kind: "person", Name: "ben"}}, "the backup"); err != nil {
		t.Fatalf("a route with no policy after the policy: %v", err)
	}
	if now := recordOf(t, ld, id); now.Rev != written.Rev || len(now.History) != len(written.History) || policyRevision(t, now) != "r1" {
		t.Fatalf("a repeat or a refusal wrote: rev %d (was %d), %d rows (were %d), policy %s", now.Rev, written.Rev, len(now.History), len(written.History), now.Policy)
	}
	stopRun(t, e, run.ID)
}

// A record whose policy came by a re-route (Host.Reroute on a record under
// the server's own policy: a router's change, and no routed row of the
// router's) takes no route row from the router told it again: a route
// with destinations and no policy is answered as a repeat by the row that
// put the policy in place, its rev and history as they were.
func TestARouteAfterAReroutedPolicyWritesNothing(t *testing.T) {
	e, ld, run, id := routedGate(t, "api-sweep-0000a7e3")
	ctx := t.Context()
	before := recordOf(t, ld, id)
	if res, err := e.Reroute(ctx, id, "switchman", anaRoute("r1"), ledger.HistoryRerouted, "rr-1", before.Rev); err != nil || res.Unchanged {
		t.Fatalf("the re-route: %+v %v", res, err)
	}
	placed := recordOf(t, ld, id)
	row := placed.History[len(placed.History)-1]
	if policyRevision(t, placed) != "r1" || row.Kind != ledger.HistoryRerouted || row.By != "router:switchman" {
		t.Fatalf("the re-routed record: policy %s, last row %+v", placed.Policy, row)
	}
	rc, err := e.RecordRoute(ctx, id, "switchman", toAna, "the architecture owner")
	if err != nil || !rc.Repeat || rc.Op != ledger.HistoryRerouted || rc.Seq != row.Seq {
		t.Fatalf("a route with no policy after the re-route: %+v %v (the policy's row %+v)", rc, err, row)
	}
	if now := recordOf(t, ld, id); now.Rev != placed.Rev || len(now.History) != len(placed.History) || now.Policy != placed.Policy {
		t.Fatalf("the route wrote: rev %d (was %d), %d rows (were %d)", now.Rev, placed.Rev, len(now.History), len(placed.History))
	}
	stopRun(t, e, run.ID)
}
