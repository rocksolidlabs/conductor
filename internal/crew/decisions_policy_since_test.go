package crew

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A router that confirms policies and answers no query with a Since
// (ext.SinceConfirmer, docs/plans/decision-policy.md): the server sets no
// PolicySinceCheck for it, so the engine confirms the deciding answer of a
// policy of one place by the plain query at its application, and takes no
// policy that counts approvals. Every test waits on the ledger's record.

// plainRouter answers the engine's plain query as answer says, recording
// the subjects asked about.
type plainRouter struct {
	mu     sync.Mutex
	answer func(subject string) ext.Confirmation
	asked  []string
}

func (p *plainRouter) check(_ context.Context, _ ledger.DecisionRecord, _, subject string) ext.Confirmation {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, subject)
	return p.answer(subject)
}

// askedAbout counts the plain queries about subject.
func (p *plainRouter) askedAbout(subject string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, s := range p.asked {
		if s == subject {
			n++
		}
	}
	return n
}

// anaEligible is the router's answer for a policy at rev-1 that names ana
// in architecture.
func anaEligible(subject string) ext.Confirmation {
	c := ext.Confirmation{InForce: true, Revision: "rev-1"}
	if subject == "ana" {
		c.Groups = []string{"architecture"}
	}
	return c
}

// noSinceGate launches a run whose ship gate opens while a router that
// answers no Since routes policies: the engine asks p alone.
func noSinceGate(t *testing.T, id string, p *plainRouter) (*Engine, *ledger.Ledger, Run, string) {
	t.Helper()
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	e.SetRouterPolicies(true, p.check)
	run := launchGated(t, e, dir, id, repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	return e, ld, run, gateOf(t, e, ld, run.ID).ID
}

// anaApproves is ana's approval of id with the confirmation the respond
// route asked for her (eligible), her sign-in rechecked: accepted.
func anaApproves(t *testing.T, e *Engine, ld *ledger.Ledger, id string) {
	t.Helper()
	rec := decisionWhen(t, ld, id, "open", inState(ledger.DecisionOpen))
	conf := anaEligible("ana")
	choice := 0
	rc, err := e.RespondDecisionWith(t.Context(), id, Response{ResponseID: "a1", Choice: &choice, ExpectedRev: rec.Rev, ByName: "Ana"},
		"user:ana", decideAs(e, "ana"), &conf)
	if err != nil || rc.Status != ledger.ResponseAccepted {
		t.Fatalf("ana's approval of %s: %+v %v", id, rc, err)
	}
	e.ConfirmAuthority(id, rc.K, true)
}

// A policy of one place from a router that answers no Since: ana's
// eligible approval is applied once, confirmed at its application by the
// plain query (her subject asked about), and stays applied: nothing
// supersedes it and no fresh decision is opened.
func TestAOnePlaceAnswerIsConfirmedByThePlainQueryWithoutASince(t *testing.T) {
	p := &plainRouter{answer: anaEligible}
	e, ld, run, id := noSinceGate(t, "api-sweep-00001094", p)
	if _, err := e.RoutePolicy(t.Context(), id, "switchman", recordOf(t, ld, id).Rev, policyOf("rev-1", false, eligibleGroup("architecture", 1, "ana")), nil, ""); err != nil {
		t.Fatal(err)
	}
	anaApproves(t, e, ld, id)
	appliedOnceIn(t, ld, id)
	if n := p.askedAbout("ana"); n != 1 {
		t.Fatalf("the router was asked about ana %d times at the application, want once", n)
	}
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	rows, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.State == ledger.DecisionOpen || r.State == ledger.DecisionSuperseded {
			t.Fatalf("after the application the run holds %s %s", ledger.DecisionID(r.N), r.State)
		}
	}
	stopRun(t, e, run.ID)
}

// The deciding answer of a policy of one place, from a router that
// answers no Since, is still refused at its application by each of the
// plain query's refusals: its person no longer eligible, the policy no
// longer in force, the router behind, of another revision, or silent.
// The decision is superseded with words saying so, nothing applied.
func TestTheDecidingAnswerWithoutASinceIsRefusedAtItsApplication(t *testing.T) {
	for i, c := range []struct {
		name string
		now  ext.Confirmation
	}{
		{"no longer eligible", ext.Confirmation{InForce: true, Revision: "rev-1"}},
		{"not in force", ext.Confirmation{Why: "the policy was withdrawn"}},
		{"behind", ext.Confirmation{InForce: true, Behind: true, Revision: "rev-1", Groups: []string{"architecture"}}},
		{"another revision", ext.Confirmation{InForce: true, Revision: "rev-2", Groups: []string{"architecture"}}},
		{"silent", ext.Confirmation{Why: "the router did not answer"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &plainRouter{answer: func(string) ext.Confirmation { return c.now }}
			e, ld, run, id := noSinceGate(t, "api-sweep-0000109"+string(rune('a'+i)), p)
			if _, err := e.RoutePolicy(t.Context(), id, "switchman", recordOf(t, ld, id).Rev, policyOf("rev-1", false, eligibleGroup("architecture", 1, "ana")), nil, ""); err != nil {
				t.Fatal(err)
			}
			anaApproves(t, e, ld, id)
			old := decisionWhen(t, ld, id, "superseded", inState(ledger.DecisionSuperseded))
			if !strings.Contains(old.Why, "Ana's answer could not be confirmed") || old.SupersededBy == "" || old.Applied != "" {
				t.Fatalf("the superseded record: why %q, by %q, applied %q", old.Why, old.SupersededBy, old.Applied)
			}
			if p.askedAbout("ana") == 0 {
				t.Fatal("the router was not asked about ana at the application")
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A policy that counts approvals (a quorum of two; two groups) from a
// router that answers no Since is not taken: the record stays under the
// server's own policy at its rev, the router's one routed row saying why;
// told again, nothing more is written; the operator answers it and it is
// applied.
func TestACountingPolicyWithoutASinceIsNotTaken(t *testing.T) {
	for i, c := range []struct {
		name string
		pol  ext.RoutePolicy
	}{
		{"a quorum of two", policyOf("rev-1", true, eligibleGroup("architecture", 2, "ana", "ben"))},
		{"two groups", policyOf("rev-1", false, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &plainRouter{answer: anaEligible}
			e, ld, run, id := noSinceGate(t, "api-sweep-000010a"+string(rune('a'+i)), p)
			before := recordOf(t, ld, id)
			rc, err := e.RoutePolicy(t.Context(), id, "switchman", before.Rev, c.pol, toAna, "the architecture owner")
			if err != nil {
				t.Fatalf("the route: %v", err)
			}
			rec := recordOf(t, ld, id)
			if ledger.RouterPolicy(rec.Policy) || rec.Rev != before.Rev || rc.Rev != before.Rev {
				t.Fatalf("the record after a counting policy: rev %d (receipt %d, was %d) %s", rec.Rev, rc.Rev, before.Rev, rec.Policy)
			}
			want := "asks for 2 approvals, and its router does not confirm an approval since its time: the operator answers it under the server's own policy"
			if !strings.Contains(rc.Words, want) {
				t.Fatalf("the route's words: %q", rc.Words)
			}
			kept := 0
			for _, h := range rec.History {
				if h.Kind == ledger.HistoryRouted && h.By == "router:switchman" && strings.Contains(h.Detail, want) {
					kept++
				}
			}
			if kept != 1 {
				t.Fatalf("the history holds %d router's rows saying why: %+v", kept, rec.History)
			}
			again, err := e.RoutePolicy(t.Context(), id, "switchman", before.Rev, c.pol, toAna, "the architecture owner")
			if err != nil || !again.Repeat || len(recordOf(t, ld, id).History) != len(rec.History) {
				t.Fatalf("told again: %+v %v", again, err)
			}
			answer(t, e, id, Response{ResponseID: "o1", Choice: choice(0), ExpectedRev: rec.Rev})
			appliedOnceIn(t, ld, id)
			if n := p.askedAbout("ana"); n != 0 {
				t.Fatalf("the router was asked about ana %d times", n)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A re-route to a policy that counts approvals, from a router that
// answers no Since, is refused with the words why, the record keeping the
// server's own policy it holds; a re-route to a policy of one place is
// taken.
func TestARerouteToACountingPolicyWithoutASinceIsRefused(t *testing.T) {
	p := &plainRouter{answer: anaEligible}
	e, ld, run, id := noSinceGate(t, "api-sweep-000010b1", p)
	rec := recordOf(t, ld, id)
	_, err := e.Reroute(t.Context(), id, "switchman", policyOf("rev-1", false, eligibleGroup("architecture", 2, "ana", "ben")), ext.Rerouted, "op-1", rec.Rev)
	if !errors.Is(err, ext.ErrRefused) || !strings.Contains(err.Error(), "asks for 2 approvals, and its router does not confirm an approval since its time") {
		t.Fatalf("the re-route: %v", err)
	}
	if now := recordOf(t, ld, id); ledger.RouterPolicy(now.Policy) || now.Rev != rec.Rev {
		t.Fatalf("after the refused re-route: %s rev %d", now.Policy, now.Rev)
	}
	if _, err := e.Reroute(t.Context(), id, "switchman", policyOf("rev-2", false, eligibleGroup("architecture", 1, "ana")), ext.Rerouted, "op-2", rec.Rev); err != nil {
		t.Fatalf("the re-route to one place: %v", err)
	}
	if now := recordOf(t, ld, id); policyRevision(t, now) != "rev-2" {
		t.Fatalf("after the re-route to one place: %s", now.Policy)
	}
	stopRun(t, e, run.ID)
}

// A record that holds a policy counting approvals, written while the
// engine confirmed approvals since their time, is judged not in force once
// it does not (a server whose router answers no Since): ana's approval is
// refused, the words saying why, and the operator answers it under the
// server's own policy, applied.
func TestAKeptCountingPolicyIsNotInForceWithoutASince(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	q := &quorumRouter{pol: policyOf("rev-1", true, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000010c1", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	e.SetPolicySince(nil)
	rc, err := approveAs(t, e, ld, q, g.ID, "ana", "a1", 0)
	answerRefused(t, "ana", rc, err, CodeNotApprover)
	if !strings.Contains(rc.Words, "not in force now (it asks for 2 approvals, and its router does not confirm an approval since its time)") {
		t.Fatalf("ana's refusal: %q", rc.Words)
	}
	conf := q.answer("")
	op, err := e.RespondDecisionWith(t.Context(), g.ID, Response{ResponseID: "o1", Choice: choice(0), ExpectedRev: recordOf(t, ld, g.ID).Rev, ByName: "Nate"}, "admin", workbench(e), &conf)
	if err != nil || op.Status != ledger.ResponseAccepted || op.Policy != policyLocal {
		t.Fatalf("the operator's answer: %+v %v", op, err)
	}
	appliedOnceIn(t, ld, g.ID)
	stopRun(t, e, run.ID)
}
