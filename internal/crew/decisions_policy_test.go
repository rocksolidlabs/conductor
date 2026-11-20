package crew

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// routedPolicyJSON is a router's policy naming ana in its one group.
const routedPolicyJSON = `{"revision":"r1","approver":"router","quorum":1,"authority":[{"kind":"operator"}],"router":"switchman","scope":"s1",` +
	`"pinned":{"rule":"r-1","groups":[{"role":"architecture","quorum":1}],"override":"operator","notify":"now"},` +
	`"groups":[{"role":"architecture","label":"architecture owner","quorum":1,"rule":"r-1","eligible":[{"subject":"ana","name":"Ana","via":"role","ref":"ref-1"}]}],` +
	`"override":"operator","routedAt":"2026-11-19T09:00:00Z","spare":true,"fallback":"local/1"}`

// Under a router's policy the entries are the operator's and one eligible
// entry per person it names, never the decide grant's, whatever the
// request's for says: ana, named, holding the decide grant (or the admin
// grant) through an identity, is admitted to the open record; bob,
// holding the decide grant, is not; ana through no identity, or holding
// only the host grant, is not. One judgement, AdmissionOf, for the list
// and the read alike.
func TestRouterEntryIsTheOneCheck(t *testing.T) {
	for _, given := range []bool{true, false} {
		rec := ledger.DecisionRecord{DecisionRow: ledger.DecisionRow{N: 7, State: ledger.DecisionOpen, Policy: routedPolicyJSON, Request: grantedRequest(given)}}
		entries := EntriesOf(rec)
		if len(entries) != 2 || entries[0].Kind != EntryOperator || entries[1] != (PolicyEntry{Kind: EntryEligible, Subject: "ana"}) {
			t.Fatalf("given %v: the entries %+v", given, entries)
		}
		for _, c := range []struct {
			name string
			who  Admittee
			want Admission
		}{
			{"ana deciding", Admittee{Grants: []string{"decide"}, Via: ViaIdentity, Subject: "ana"}, AdmitOpen},
			{"bob deciding", Admittee{Grants: []string{"decide"}, Via: ViaIdentity, Subject: "bob"}, AdmitNone},
			{"ana with the host grant alone", Admittee{Grants: []string{"host"}, Via: ViaIdentity, Subject: "ana"}, AdmitNone},
			{"ana through a router", Admittee{Grants: []string{"decide"}, Via: ViaRouter, Subject: "ana"}, AdmitNone},
			{"the operator", Admittee{Grants: []string{"admin"}, Via: ViaWorkbench}, AdmitWhole},
		} {
			if got := AdmissionOf(rec, c.who); got != c.want {
				t.Errorf("given %v, %s: %d, want %d", given, c.name, got, c.want)
			}
		}
		if !Admits(entries, Admittee{Grants: []string{"admin"}, Via: ViaIdentity, Subject: "ana"}) {
			t.Errorf("given %v: ana holding the admin grant is not admitted by her entry", given)
		}
	}
}

// gatedUnderARouter launches a run whose two gates (ship before deploy,
// smoke before smoke) open while the router routes policies, each with its
// spare, rejects smoke, and fills the run with n decisions of its own: the
// run and its gates' rows by gate.
func gatedUnderARouter(t *testing.T, id string, n int) (*Engine, *ledger.Ledger, Run, map[string]ledger.DecisionRow) {
	t.Helper()
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	e.SetRouterPolicies(true, nil)
	smoke := PackageDecision{ID: "smoke", Question: "Smoke it?", Kind: ledger.DecisionApproval, Before: []string{"smoke"}}
	run := launchGated(t, e, dir, id, repoCrew(dir, work("deploy", "ops"), work("smoke", "qa")), shipGate("deploy"), smoke)
	rows, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	gates := map[string]ledger.DecisionRow{}
	for _, row := range rows {
		if !ledger.PolicySpare(row.Policy) {
			t.Fatalf("gate %s opened with no spare under a router: %s", row.Gate, row.Policy)
		}
		gates[row.Gate] = row
	}
	answer(t, e, gates["smoke"].ID(), Response{ResponseID: "r1", Choice: choice(1), ExpectedRev: 1})
	waitTask(t, e, run.ID, "smoke", ledger.TaskCancelled, 0)
	fillDecisions(t, ld, run.ID, n)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	r.dec.opened = n + 2 // as the ledger counts them: the fill and both gates
	e.mu.Unlock()
	return e, ld, run, gates
}

// Under a router, a run of 999 decisions whose open gate holds its spare
// is full, as the ledger counts it: a retry that would ask the rejected
// gate again is refused decision_limit, and nothing is carried that the
// ledger refuses, so the run pauses, unpauses and stops.
func TestARunAtItsBoundUnderARouterIsNeverWedged(t *testing.T) {
	e, _, run, _ := gatedUnderARouter(t, "api-sweep-000000d1", ledger.MaxDecisionsPerRun-3)
	var de *DecisionError
	if err := e.RetryTask(t.Context(), run.ID, "smoke"); !errors.As(err, &de) || de.Code != CodeDecisionLimit {
		t.Fatalf("the retry at the bound: %v", err)
	}
	if err := e.PauseRun(t.Context(), run.ID, true); err != nil {
		t.Fatalf("the pause: %v", err)
	}
	if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
		t.Fatalf("the unpause: %v", err)
	}
	stopRun(t, e, run.ID)
}

// A chain of replacements holds one decision and one spare of the run's
// bound, however long, in the engine's count as in the ledger's: after
// three replacements of ship, a retry that asks smoke again (its two
// places) still has room.
func TestAChainOfReplacementsKeepsTheRunsRoom(t *testing.T) {
	e, ld, run, gates := gatedUnderARouter(t, "api-sweep-000000d2", ledger.MaxDecisionsPerRun-5)
	ship := gates["ship"].ID()
	for i := range 3 {
		rec := decisionWhen(t, ld, ship, "open", inState(ledger.DecisionOpen))
		rc, err := e.RespondDecision(t.Context(), ship, Response{ResponseID: fmt.Sprintf("n%d", i), Choice: choice(0), ExpectedRev: rec.Rev}, "user:nate", identity(e, "nate"))
		if err != nil || rc.Status != "accepted" {
			t.Fatalf("the identity's answer %d: %+v %v", i, rc, err)
		}
		e.ConfirmAuthority(ship, rc.K, false)
		ship = decisionWhen(t, ld, ship, "superseded", func(r ledger.DecisionRecord) bool {
			return r.State == ledger.DecisionSuperseded && r.SupersededBy != ""
		}).SupersededBy
	}
	if err := e.RetryTask(t.Context(), run.ID, "smoke"); err != nil {
		t.Fatalf("the retry after the chain: %v", err)
	}
	stopRun(t, e, run.ID)
}

// A router's names, labels, rules and gaps are kept without their format
// characters (a right-to-left override, a zero-width space), so none
// reorders what a card draws beside it; a name of nothing else is its
// subject. The router's own policy is not changed.
func TestARoutersWordsAreKeptWithoutFormatCharacters(t *testing.T) {
	p := ext.RoutePolicy{Revision: "r1", Scope: "s1", Override: ext.OverrideOperator,
		Pinned: ext.PinnedRequirement{Rule: "r\u200b-1"},
		Groups: []ext.Requirement{{Role: "architecture", Label: "architecture\u202e owner", Quorum: 1, Rule: "r-\u20661", Gap: "",
			Eligible: []ext.Eligible{{Subject: "ana", Name: "A\u202ena", Via: ext.ViaRole, Ref: "ref-1"}, {Subject: "ben", Name: "\u200b\u200d", Via: ext.ViaRole, Ref: "ref-2"}}}}}
	raw, err := storedPolicy(p, "switchman", true, time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got, _, ok := RouterPolicyOf(raw)
	if !ok {
		t.Fatalf("not a router's policy: %s", raw)
	}
	g := got.Groups[0]
	if g.Label != "architecture owner" || g.Rule != "r-1" || got.Pinned.Rule != "r-1" || g.Eligible[0].Name != "Ana" || g.Eligible[1].Name != "ben" {
		t.Fatalf("kept: %s", raw)
	}
	if p.Groups[0].Eligible[0].Name != "A\u202ena" {
		t.Fatal("the router's own policy was changed")
	}
}
