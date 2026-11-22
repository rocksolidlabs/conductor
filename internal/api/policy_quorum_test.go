package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Groups, quorum and distinct persons on a decision record (SW1 PR-9,
// docs/plans/decision-policy.md), at the routes: the router is exttest's
// FakeRouter, every person a FakeIdentity's principal holding the decide
// grant, each answering through the decide routes. Every test waits on the
// record in the ledger, never on the clock.

// quorumFixture is a policy fixture whose router confirms each subject in
// the groups the policy it holds now names it in (set), and whose identity
// provider admits a pass for each person.
type quorumFixture struct {
	*policyFixture
	mu  sync.Mutex
	now map[string]ext.RoutePolicy // the policy the router holds, by record
	// refuse, when set, answers whether the router refuses q (not in force
	// for its subject): what a router that cannot say a subject stayed
	// eligible since a time answers.
	refuse func(q ext.ConfirmQuery) bool
}

func newQuorumFixture(t *testing.T) *quorumFixture {
	t.Helper()
	return &quorumFixture{policyFixture: newPolicyFixture(t), now: map[string]ext.RoutePolicy{}}
}

// pass is subject's pass, admitted with the decide grant, made once.
func (f *quorumFixture) pass(subject string) string {
	p := "pass.decide.q-" + subject
	f.id.FakeIdentity.Add(p, ext.Principal{Subject: subject, Name: strings.ToUpper(subject[:1]) + subject[1:], Grants: []ext.Grant{ext.GrantDecide}})
	return p
}

// group is a requirement of role, quorum q, its eligible subjects by role.
func group(role string, q int, subjects ...string) ext.Requirement {
	g := ext.Requirement{Role: role, Label: role + " owner", Quorum: q, Rule: "r-" + role}
	for _, s := range subjects {
		g.Eligible = append(g.Eligible, ext.Eligible{Subject: s, Name: strings.ToUpper(s[:1]) + s[1:], Via: ext.ViaRole, Ref: "ref-" + s})
	}
	if len(subjects) == 0 {
		g.Gap = "none is assigned"
	}
	return g
}

// groupsPolicy is a policy of groups, distinct as given, revision rev.
func groupsPolicy(rev string, distinct bool, groups ...ext.Requirement) ext.RoutePolicy {
	p := exttest.SamplePolicy()
	p.Revision, p.Distinct, p.Pinned.Distinct = rev, distinct, distinct
	p.Groups, p.Pinned.Groups = groups, nil
	for _, g := range groups {
		p.Pinned.Groups = append(p.Pinned.Groups, ext.PinnedGroup{Role: g.Role, Quorum: g.Quorum})
	}
	return p
}

// open routes an approval under pol, the router confirming it in force,
// and answers its id.
func (f *quorumFixture) openUnder(t *testing.T, requestID, did string, pol ext.RoutePolicy) string {
	t.Helper()
	if err := pol.Valid(*exttest.SampleRecord()); err != nil {
		t.Fatalf("the policy is not valid: %v", err)
	}
	f.hold(did, pol)
	f.routed(t, requestID, did, pol)
	return did
}

// hold has the router hold pol for did and confirm each subject in the
// groups pol names it in.
func (f *quorumFixture) hold(did string, pol ext.RoutePolicy) {
	f.mu.Lock()
	f.now[did] = pol
	f.mu.Unlock()
	f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
		f.mu.Lock()
		p, refuse := f.now[did], f.refuse
		f.mu.Unlock()
		if refuse != nil && refuse(q) {
			return ext.Confirmation{InForce: true, Revision: p.Revision}
		}
		c := ext.Confirmation{InForce: true, Revision: p.Revision}
		for _, g := range p.Groups {
			if slices.ContainsFunc(g.Eligible, func(e ext.Eligible) bool { return e.Subject == q.Subject }) {
				c.Groups = append(c.Groups, g.Role)
			}
		}
		return c
	})
}

// approve sends subject's approval (choice) of did at its rev now through
// the decide route: its status and reply.
func (f *quorumFixture) answerAs(t *testing.T, subject, did, rid string, choice int) (int, map[string]any) {
	t.Helper()
	status, _, out := f.respondAs(t, f.pass(subject), did, rid, choice, f.rev(t, did))
	return status, out
}

// approved sends subject's approval and expects status and, for a
// receipt, its status.
func (f *quorumFixture) approved(t *testing.T, subject, did, rid string, status int, receipt string) map[string]any {
	t.Helper()
	got, out := f.answerAs(t, subject, did, rid, 0)
	if got != status {
		t.Fatalf("%s's approval of %s: %d %v", subject, did, got, out)
	}
	if receipt != "" {
		rc, _ := out["receipt"].(map[string]any)
		if rc["status"] != receipt {
			t.Fatalf("%s's receipt: %v", subject, out)
		}
	}
	return out
}

// quorumOf is the operator's read's quorum of did.
func (f *quorumFixture) quorumOf(t *testing.T, did string) *proto.DecisionQuorum {
	t.Helper()
	status, data, _ := f.call(t, http.MethodGet, "/api/decisions/"+did, nil)
	if status != http.StatusOK {
		t.Fatalf("the operator's read: %d %s", status, data)
	}
	var reply struct {
		Quorum *proto.DecisionQuorum `json:"quorum"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Quorum
}

// countedOf is the subjects of did's counted rows, in order.
func countedSubjects(rec ledger.DecisionRecord) []string {
	var out []string
	for _, r := range rec.Responses {
		if r.Status == ledger.ResponseCounted {
			var a crew.Authority
			_ = json.Unmarshal([]byte(r.Authority), &a)
			out = append(out, a.Subject)
		}
	}
	return out
}

// appliedOnce waits for did applied and says its applied rows are one.
func (f *quorumFixture) appliedOnce(t *testing.T, did string) ledger.DecisionRecord {
	t.Helper()
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	rec := f.record(t, did)
	applied := 0
	for _, h := range rec.History {
		if h.Kind == ledger.HistoryApplied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("%s applied %d times", did, applied)
	}
	return rec
}

// sinceAsked are the times the router was asked whether subject stayed
// eligible since, for did.
func (f *quorumFixture) sinceAsked(did, subject string) []time.Time {
	var out []time.Time
	for _, q := range f.fr.Confirms() {
		if q.Decision.Request == did && q.Subject == subject && !q.Since.IsZero() {
			out = append(out, q.Since)
		}
	}
	return out
}

// A policy of two groups (architecture: ana; security: cam), distinct
// persons: ana's approval is counted, the decision open at rev + 1 and "1
// of 2" on the operator's read; cam's completes it, accepted, the
// combined answer applied once.
func TestCombinedGroupsAnswerOnceAllMet(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
	rev := f.rev(t, did)
	out := f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	if rc := out["receipt"].(map[string]any); rc["rev"] != float64(rev+1) {
		t.Fatalf("ana's counted receipt: %v", rc)
	}
	rec := f.record(t, did)
	if rec.State != ledger.DecisionOpen || rec.Rev != rev+1 || len(rec.Responses) != 1 || rec.Responses[0].Status != ledger.ResponseCounted {
		t.Fatalf("after ana's approval: %s rev %d %+v", rec.State, rec.Rev, rec.Responses)
	}
	if rc := f.receiptOf(t, did, 1); !strings.Contains(rc.Words, "1 of 2") || !strings.Contains(rc.Words, "it waits for the security owner") || len(rc.Groups) != 1 {
		t.Fatalf("ana's receipt as kept: %+v", rc)
	}
	q := f.quorumOf(t, did)
	if q == nil || q.Places != 2 || q.Filled != 1 || len(q.Groups) != 2 || !q.Groups[0].Met || q.Groups[1].Met || q.Groups[1].Counted != 0 {
		t.Fatalf("the operator's count after ana: %+v", q)
	}
	if v := f.view(t, f.pass("ana"), did); len(v.Own) != 1 || v.Own[0].Status != proto.ReceiptCounted || v.Outcome != proto.OutcomeWaiting {
		t.Fatalf("ana's view: %+v", v)
	}
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptAccepted)
	rec = f.appliedOnce(t, did)
	if got := countedSubjects(rec); len(got) != 1 || got[0] != "ana" {
		t.Fatalf("the counted rows: %v", got)
	}
	if v := f.view(t, f.pass("ana"), did); v.Outcome != proto.OutcomeApplied {
		t.Fatalf("ana's outcome once applied: %+v", v)
	}
}

// One group of quorum two counts distinct subjects: ana's approval counts,
// her second (a new response id, or her other pass) is already_counted and
// takes no row; ben's completes it.
func TestQuorumCountsDistinctSubjects(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", false, group("architecture", 2, "ana", "ben", "cam")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	if status, out := f.answerAs(t, "ana", did, "a2", 0); status != http.StatusConflict || errorCode(out) != crew.CodeAlreadyCounted {
		t.Fatalf("ana's second approval: %d %v", status, out)
	}
	status, _, out := f.respondAs(t, passHostAna, did, "a3", 0, f.rev(t, did))
	if status != http.StatusConflict || errorCode(out) != crew.CodeAlreadyCounted || !strings.Contains(mustJSON(out), "is counted already") {
		t.Fatalf("ana's approval through her other pass: %d %v", status, out)
	}
	if rec := f.record(t, did); len(rec.Responses) != 1 {
		t.Fatalf("the repeats took rows: %+v", rec.Responses)
	}
	f.approved(t, "ben", did, "b1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)
}

// Under distinct persons one person fills one place, however many groups
// name them: ana, in both groups, fills one, and her second approval is
// already_counted; ben, the architecture owner by the role and the
// security owner's delegate for ana, fills one place too, as himself, and
// a second approval of his is already_counted; the decision stays open
// until cam fills the other.
func TestDistinctAcrossGroups(t *testing.T) {
	f := newQuorumFixture(t)
	arch := group("architecture", 1, "ana", "ben")
	sec := group("security", 1, "ana", "cam")
	sec.Eligible = append(sec.Eligible, ext.Eligible{Subject: "ben", Name: "Ben", Via: ext.ViaDelegate, For: "ana", Ref: "ref-ben-d"})
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, arch, sec))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	if status, out := f.answerAs(t, "ana", did, "a2", 0); status != http.StatusConflict || errorCode(out) != crew.CodeAlreadyCounted {
		t.Fatalf("ana again: %d %v", status, out)
	}
	if q := f.quorumOf(t, did); q.Filled != 1 {
		t.Fatalf("ana, in both groups, fills %d places", q.Filled)
	}
	// Ben with ana fills both: he takes the place ana leaves.
	f.approved(t, "ben", did, "b1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)

	// Ben alone, by the role and as ana's delegate, fills one place.
	did2 := f.openUnder(t, "r2", "dr-2", groupsPolicy("rev-2", true, arch, sec))
	f.approved(t, "ben", did2, "b1", http.StatusCreated, proto.ReceiptCounted)
	if status, out := f.answerAs(t, "ben", did2, "b2", 0); status != http.StatusConflict || errorCode(out) != crew.CodeAlreadyCounted {
		t.Fatalf("ben again: %d %v", status, out)
	}
	if rec := f.record(t, did2); rec.State != ledger.DecisionOpen || len(rec.Responses) != 1 {
		t.Fatalf("ben filled two places: %s %+v", rec.State, rec.Responses)
	}
	f.approved(t, "cam", did2, "c1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did2)
}

// The groups in the order security, architecture; ana is named in both,
// cam in security alone. Ana approves first: a greedy assignment would
// give her security, the one place cam can fill. Cam's approval completes
// it all the same: the largest assignment puts ana on architecture.
func TestDistinctAssignmentFindsTheMatching(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("security", 1, "ana", "cam"), group("architecture", 1, "ana")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	if q := f.quorumOf(t, did); q.Filled != 1 || q.Places != 2 {
		t.Fatalf("after ana: %+v", q)
	}
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)
}

// Without distinct persons one person fills a place in every group that
// names them: ana's one approval answers a policy of two groups at once.
func TestOnePersonInTwoGroupsWithoutDistinct(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", false, group("architecture", 1, "ana"), group("security", 1, "ana", "cam")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptAccepted)
	rec := f.appliedOnce(t, did)
	if rc := f.receiptOf(t, did, 1); len(rc.Groups) != 2 || len(rec.Responses) != 1 {
		t.Fatalf("ana's receipt: %+v", rc)
	}
}

// One eligible Reject answers the record at once, rejected, whatever the
// places counted: ana's approval stays kept as counted, and her outcome is
// not used.
func TestAnEligibleRejectAnswersAtOnce(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	status, out := f.answerAs(t, "cam", did, "c1", 1)
	if status != http.StatusCreated || out["receipt"].(map[string]any)["status"] != proto.ReceiptAccepted {
		t.Fatalf("cam's Reject: %d %v", status, out)
	}
	rec := f.appliedOnce(t, did)
	if got := countedSubjects(rec); len(got) != 1 {
		t.Fatalf("the counted rows after the Reject: %v", got)
	}
	if v := f.view(t, f.pass("ana"), did); v.Outcome != proto.OutcomeNotUsed {
		t.Fatalf("ana's outcome after the Reject: %+v", v)
	}
}

// An accepted override answers the record whole, every group at once; the
// counted approval before it stays counted in the record.
func TestAnOverrideAnswersEveryGroup(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 2, "ana", "ben"), group("security", 1, "cam")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), true, "the release cannot wait for the owners"), http.StatusCreated, "")
	rec := f.appliedOnce(t, did)
	if got := countedSubjects(rec); len(got) != 1 || got[0] != "ana" {
		t.Fatalf("the counted rows after the override: %v", got)
	}
	if rc := f.receiptOf(t, did, 2); !rc.Override {
		t.Fatalf("the override's receipt: %+v", rc)
	}
}

// A counted approval keeps its place across a re-route that keeps its
// subject eligible: after the router's change of revision, cam's approval
// still completes the record with ana's.
func TestCountedResponsesKeepTheirPlaces(t *testing.T) {
	f := newQuorumFixture(t)
	first := groupsPolicy("rev-1", true, group("architecture", 1, "ana", "ben"), group("security", 1, "cam"))
	did := f.openUnder(t, "r1", "dr-1", first)
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	next := groupsPolicy("rev-2", true, group("architecture", 1, "ana"), group("security", 1, "cam", "dan"))
	f.hold(did, next)
	res, err := f.routerHost(t).Reroute(t.Context(), ext.RerouteRequest{Decision: did, Route: ext.Route{Policy: &next}, Why: ext.Rerouted, OpID: "op-1", ExpectedRev: f.rev(t, did)})
	if err != nil || res.Superseded != "" {
		t.Fatalf("the re-route: %+v %v", res, err)
	}
	if q := f.quorumOf(t, did); q.Filled != 1 {
		t.Fatalf("ana's place after the re-route: %+v", q)
	}
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)
}

// Ana's counted approval, its sign-in's recheck not admitted (the pass no
// longer admitted after her response): it no longer counts, a rechecked
// row says so, her view reads it refused not_confirmed, and cam's approval
// after it is counted, the decision still open; ana may approve again.
func TestCountedApprovalVoidedByASignInRevocation(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
	ana := f.pass("ana")
	// Her response's authentication and its judgement are the first two
	// asks; the recheck after its commit, the third, admits no one.
	f.id.recheckAs(ana, ext.Principal{}, false)
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	rec := f.record(t, did)
	var rechecked []string
	for _, h := range rec.History {
		if h.Kind == ledger.HistoryRechecked {
			rechecked = append(rechecked, h.Detail)
		}
	}
	if len(rechecked) != 1 || !strings.Contains(rechecked[0], `"admitted":false`) {
		t.Fatalf("the rechecked rows: %v", rechecked)
	}
	if v := f.view(t, ana, did); len(v.Own) != 1 || v.Own[0].Status != proto.ReceiptRefused || v.Own[0].Code != crew.CodeNotConfirmed || v.Outcome != proto.OutcomeRefused || v.OutcomeCode != crew.CodeNotConfirmed {
		t.Fatalf("ana's view: %+v", v)
	}
	if q := f.quorumOf(t, did); q.Filled != 0 {
		t.Fatalf("the voided approval fills %d", q.Filled)
	}
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptCounted)
	if rec := f.record(t, did); rec.State != ledger.DecisionOpen {
		t.Fatalf("cam's approval answered beside a voided one: %s", rec.State)
	}
	f.approved(t, "ana", did, "a2", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)
}

// At the application the router is asked whether ana, counted, stayed
// eligible since her approval's time, and cannot say: her approval is
// void, and the decision is superseded by a fresh one with words saying
// whose approval could not be confirmed; nothing is applied.
func TestCountedApprovalVoidedWhenItsAuthorityCannotBeEstablished(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	f.mu.Lock()
	f.refuse = func(q ext.ConfirmQuery) bool { return q.Subject == "ana" && !q.Since.IsZero() }
	f.mu.Unlock()
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptAccepted)
	old := f.decisionWhen(t, did, "superseded", inState(ledger.DecisionSuperseded))
	if !strings.Contains(old["why"].(string), "Ana's approval could not be confirmed") || old["supersededBy"] == "" {
		t.Fatalf("the superseded record: %v", old)
	}
	if rec := f.record(t, did); rec.Applied != "" {
		t.Fatalf("applied: %s", rec.Applied)
	}
	if len(f.sinceAsked(did, "ana")) == 0 {
		t.Fatal("the router was never asked about ana since her approval")
	}
}

// Every counted approval and the deciding one are confirmed at the
// application, each since its own response's time.
func TestCountedResponsesRecheckedAtApplication(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", false, group("architecture", 2, "ana", "ben"), group("security", 1, "cam")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	f.approved(t, "ben", did, "b1", http.StatusCreated, proto.ReceiptCounted)
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptAccepted)
	rec := f.appliedOnce(t, did)
	for _, r := range rec.Responses {
		var a crew.Authority
		_ = json.Unmarshal([]byte(r.Authority), &a)
		got := f.sinceAsked(did, a.Subject)
		if len(got) == 0 {
			t.Errorf("%s was never asked since its approval", a.Subject)
		}
		for _, at := range got {
			if !at.Equal(r.At) {
				t.Errorf("%s was asked since %v, want since %v", a.Subject, at, r.At)
			}
		}
	}
}

// Three groups of quorum four, eight eligible in each: all sixteen of the
// first two groups approve first: eight are counted, eight are not_needed
// and take no row; the third group's four complete it, the last accepted.
func TestSurplusApprovalsTakeNoRow(t *testing.T) {
	f := newQuorumFixture(t)
	var groups []ext.Requirement
	var people [3][]string
	for g, role := range []string{"architecture", "security", "release"} {
		for i := range 8 {
			people[g] = append(people[g], fmt.Sprintf("%c%d", 'a'+g, i))
		}
		groups = append(groups, group(role, 4, people[g]...))
	}
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", false, groups...))
	counted, notNeeded := 0, 0
	for g := range 2 {
		for _, s := range people[g] {
			status, out := f.answerAs(t, s, did, "a-"+s, 0)
			switch {
			case status == http.StatusCreated && out["receipt"].(map[string]any)["status"] == proto.ReceiptCounted:
				counted++
			case status == http.StatusConflict && errorCode(out) == crew.CodeNotNeeded:
				notNeeded++
			default:
				t.Fatalf("%s's approval: %d %v", s, status, out)
			}
		}
	}
	if counted != 8 || notNeeded != 8 || len(f.record(t, did).Responses) != 8 {
		t.Fatalf("counted %d, not needed %d, rows %d", counted, notNeeded, len(f.record(t, did).Responses))
	}
	for i, s := range people[2][:4] {
		want := proto.ReceiptCounted
		if i == 3 {
			want = proto.ReceiptAccepted
		}
		f.approved(t, s, did, "a-"+s, http.StatusCreated, want)
	}
	if rec := f.appliedOnce(t, did); len(rec.Responses) != 12 {
		t.Fatalf("%d rows", len(rec.Responses))
	}
}

// Under a policy of twelve places a refusal is kept only while the rows
// left after it are at least the places unfilled: twenty refusals are
// kept, the twenty-first is not, and the twelve approvals still find their
// rows.
func TestRefusalsLeaveRoomForUnmetPlaces(t *testing.T) {
	f := newQuorumFixture(t)
	var groups []ext.Requirement
	var people []string
	for g, role := range []string{"architecture", "security", "release"} {
		var mine []string
		for i := range 4 {
			mine = append(mine, fmt.Sprintf("%c%d", 'a'+g, i))
		}
		people = append(people, mine...)
		groups = append(groups, group(role, 4, mine...))
	}
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", false, groups...))
	for i := range 21 {
		out := f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody(fmt.Sprintf("o%d", i), 0, f.rev(t, did), false, ""), http.StatusForbidden, crew.CodeNotEligible)
		rc, _ := out["receipt"].(map[string]any)
		kept := rc["kept"] != false
		if kept != (i < 20) {
			t.Fatalf("refusal %d kept %v: %v", i+1, kept, out)
		}
	}
	if n := len(f.record(t, did).Responses); n != 20 {
		t.Fatalf("%d rows after the refusals", n)
	}
	for i, s := range people {
		want := proto.ReceiptCounted
		if i == len(people)-1 {
			want = proto.ReceiptAccepted
		}
		f.approved(t, s, did, "a-"+s, http.StatusCreated, want)
	}
	if rec := f.appliedOnce(t, did); len(rec.Responses) != ledger.MaxDecisionResponses {
		t.Fatalf("%d rows", len(rec.Responses))
	}
}

// The server takes every shape a router's policy holds: four groups of
// quorum four, distinct persons, written into the record as sent.
func TestTheFullPolicyShapeIsTaken(t *testing.T) {
	f := newQuorumFixture(t)
	var groups []ext.Requirement
	for g := range 4 {
		var mine []string
		for i := range 4 {
			mine = append(mine, fmt.Sprintf("%c%d", 'a'+g, i))
		}
		groups = append(groups, group(fmt.Sprintf("role-%d", g), 4, mine...))
	}
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, groups...))
	p, _, ok := crew.RouterPolicyOf(f.record(t, did).Policy)
	if !ok || len(p.Groups) != 4 || !p.Distinct || p.Groups[3].Quorum != 4 || len(p.Pinned.Groups) != 4 {
		t.Fatalf("the policy kept: %+v", p)
	}
	if q := f.quorumOf(t, did); q.Places != 16 || q.Filled != 0 || len(q.Groups) != 4 {
		t.Fatalf("the count: %+v", q)
	}
}
