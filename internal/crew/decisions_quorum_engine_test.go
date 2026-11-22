package crew

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Counted approvals in the engine (SW1 PR-9): what a restart keeps of
// them, a re-route that leaves their rows short, and a held answer, which
// never counts. The router is a fake of the server's guarded calls
// (quorumRouter); every test waits on the ledger's record.

// quorumRouter answers the engine's asks of a router that holds pol: in
// force at its revision, a subject eligible in the groups pol names it
// in. refuseSince, when set, says a subject the router cannot say stayed
// eligible since a time.
type quorumRouter struct {
	mu          sync.Mutex
	pol         ext.RoutePolicy
	refuseSince func(subject string) bool
	since       []string // the subjects asked about since a time
}

func (q *quorumRouter) answer(subject string) ext.Confirmation {
	q.mu.Lock()
	defer q.mu.Unlock()
	c := ext.Confirmation{InForce: true, Revision: q.pol.Revision}
	for _, g := range q.pol.Groups {
		if slices.ContainsFunc(g.Eligible, func(e ext.Eligible) bool { return e.Subject == subject }) {
			c.Groups = append(c.Groups, g.Role)
		}
	}
	return c
}

func (q *quorumRouter) check(_ context.Context, _ ledger.DecisionRecord, _, subject string) ext.Confirmation {
	return q.answer(subject)
}

func (q *quorumRouter) checkSince(_ context.Context, _ ledger.DecisionRecord, _, subject string, since time.Time) ext.Confirmation {
	q.mu.Lock()
	q.since = append(q.since, subject)
	refuse := q.refuseSince != nil && q.refuseSince(subject)
	q.mu.Unlock()
	if since.IsZero() || refuse {
		return ext.Confirmation{InForce: true, Revision: q.pol.Revision}
	}
	return q.answer(subject)
}

// routes has e ask q, as the server's composition does.
func (q *quorumRouter) routes(e *Engine) {
	e.SetRouterPolicies(true, q.check)
	e.SetPolicySince(q.checkSince)
}

// eligibleGroup is a requirement of role, quorum n, its subjects eligible
// by the role.
func eligibleGroup(role string, n int, subjects ...string) ext.Requirement {
	g := ext.Requirement{Role: role, Label: role + " owner", Quorum: n, Rule: "r-" + role}
	for _, s := range subjects {
		g.Eligible = append(g.Eligible, ext.Eligible{Subject: s, Name: strings.ToUpper(s[:1]) + s[1:], Via: ext.ViaRole, Ref: "ref-" + s})
	}
	return g
}

// policyOf is a policy of groups at revision rev.
func policyOf(rev string, distinct bool, groups ...ext.Requirement) ext.RoutePolicy {
	p := ext.RoutePolicy{Revision: rev, Scope: "s1", Distinct: distinct, Override: ext.OverrideOperator, Groups: groups,
		Pinned: ext.PinnedRequirement{Rule: "r-1", Distinct: distinct, Override: ext.OverrideOperator, Notify: ext.NotifyNow}}
	for _, g := range groups {
		p.Pinned.Groups = append(p.Pinned.Groups, ext.PinnedGroup{Role: g.Role, Quorum: g.Quorum})
	}
	return p
}

// routeUnder writes q's policy into the open gate id at its rev, through
// the policy's transaction.
func routeUnder(t *testing.T, e *Engine, ld *ledger.Ledger, q *quorumRouter, id string) {
	t.Helper()
	rec := decisionWhen(t, ld, id, "open", inState(ledger.DecisionOpen))
	q.mu.Lock()
	pol := q.pol
	q.mu.Unlock()
	if _, err := e.RoutePolicy(t.Context(), id, "switchman", rec.Rev, pol, nil, ""); err != nil {
		t.Fatalf("the policy of %s: %v", id, err)
	}
}

// approveAs is subject's approval of id at its rev now, the router's
// confirmation asked as the respond route asks it, choice as given.
func approveAs(t *testing.T, e *Engine, ld *ledger.Ledger, q *quorumRouter, id, subject, rid string, choice int) (ResponseReceipt, error) {
	t.Helper()
	rec := decisionWhen(t, ld, id, "readable", func(ledger.DecisionRecord) bool { return true })
	conf := q.answer(subject)
	return e.RespondDecisionWith(t.Context(), id, Response{ResponseID: rid, Choice: &choice, ExpectedRev: rec.Rev, ByName: subject},
		"user:"+subject, Authority{Grant: "decide", Via: ViaIdentity, Subject: subject, Boot: e.Boot()}, &conf)
}

// mustCount is approveAs expecting the receipt's status.
func mustCount(t *testing.T, e *Engine, ld *ledger.Ledger, q *quorumRouter, id, subject, rid, status string) ResponseReceipt {
	t.Helper()
	rc, err := approveAs(t, e, ld, q, id, subject, rid, 0)
	if err != nil || rc.Status != status {
		t.Fatalf("%s's approval of %s: %+v %v, want %s", subject, id, rc, err, status)
	}
	return rc
}

// A confirmed counted approval keeps its place across a restart: ana's,
// rechecked and admitted before the server's end, still counts after it,
// and cam's approval in the continuation completes the gate, applied.
func TestPartialApprovalSurvivesARestart(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	q := &quorumRouter{pol: policyOf("rev-1", true, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000f1", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	rc := mustCount(t, e, ld, q, g.ID, "ana", "a1", ledger.ResponseCounted)
	e.ConfirmAuthority(g.ID, rc.K, true)
	decisionWhen(t, ld, g.ID, "ana's approval rechecked", func(r ledger.DecisionRecord) bool { return hasRechecked(r, rc.K, true) })

	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	q.routes(e2)
	next := continueRun(t, e2, run.ID)
	rec := decisionWhen(t, ld2, g.ID, "open in the continuation", inState(ledger.DecisionOpen))
	if got := CountedOf(rec, e2.Boot(), e2.CountedPending); len(got) != 1 || got[0].Subject != "ana" || !got[0].Confirmed {
		t.Fatalf("ana's approval after the restart: %+v", got)
	}
	cam := mustCount(t, e2, ld2, q, g.ID, "cam", "c1", ledger.ResponseAccepted)
	e2.ConfirmAuthority(g.ID, cam.K, true)
	decisionWhen(t, ld2, g.ID, "applied", inState(ledger.DecisionApplied))
	waitAttempt(t, e2, next.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e2, next.ID)
}

// hasRechecked says r holds a rechecked row for k, admitted as given.
func hasRechecked(r ledger.DecisionRecord, k int, admitted bool) bool {
	return slices.ContainsFunc(r.History, func(h ledger.HistoryRow) bool {
		return h.Kind == ledger.HistoryRechecked && strings.Contains(h.Detail, fmt.Sprintf(`"k":%d`, k)) && strings.Contains(h.Detail, fmt.Sprintf(`"admitted":%v`, admitted))
	})
}

// A counted approval whose sign-in was never rechecked before the
// server's end counts no more after it: cam's approval in the continuation
// is counted, not the answer, and ana's new approval is no repeat of her
// old one: it completes the gate.
func TestUnconfirmedCountedResponseAfterARestartIsRefused(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	q := &quorumRouter{pol: policyOf("rev-1", true, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000f2", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	mustCount(t, e, ld, q, g.ID, "ana", "a1", ledger.ResponseCounted)

	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	q.routes(e2)
	next := continueRun(t, e2, run.ID)
	rec := decisionWhen(t, ld2, g.ID, "open in the continuation", inState(ledger.DecisionOpen))
	if got := CountedOf(rec, e2.Boot(), e2.CountedPending); len(got) != 0 {
		t.Fatalf("an unrechecked approval counts after the restart: %+v", got)
	}
	cam := mustCount(t, e2, ld2, q, g.ID, "cam", "c1", ledger.ResponseCounted)
	e2.ConfirmAuthority(g.ID, cam.K, true)
	ana := mustCount(t, e2, ld2, q, g.ID, "ana", "a2", ledger.ResponseAccepted)
	e2.ConfirmAuthority(g.ID, ana.K, true)
	decisionWhen(t, ld2, g.ID, "applied", inState(ledger.DecisionApplied))
	stopRun(t, e2, next.ID)
}

// The answer of a policy that counts waits for every counted approval's
// recheck: cam's accepted answer is not applied while ana's counted one is
// still to be rechecked, and is once it is.
func TestTheAnswerWaitsForTheCountedRechecks(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	q := &quorumRouter{pol: policyOf("rev-1", true, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000f3", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	ana := mustCount(t, e, ld, q, g.ID, "ana", "a1", ledger.ResponseCounted)
	cam := mustCount(t, e, ld, q, g.ID, "cam", "c1", ledger.ResponseAccepted)
	e.ConfirmAuthority(g.ID, cam.K, true)
	// Held: ana's recheck has not answered. Its answer applies it.
	e.mu.Lock()
	waits := e.countedWaits(numberOfID(g.ID))
	e.mu.Unlock()
	if !waits {
		t.Fatal("ana's counted approval is not waited for")
	}
	if rec := decisionWhen(t, ld, g.ID, "answered", inState(ledger.DecisionAnswered)); rec.Applied != "" {
		t.Fatalf("applied before ana's recheck: %s", rec.Applied)
	}
	e.ConfirmAuthority(g.ID, ana.K, true)
	decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

func numberOfID(id string) int64 {
	n, _ := ledger.ParseDecisionID(id)
	return n
}

// At the run's 1,000 decisions, a gate of two groups of quorum four holds
// 24 kept refusals and four counted architecture approvals: four rows left
// for its four places unfilled. The router's re-route names other
// architecture owners: the four approvals fill nothing, eight places
// unfilled and four rows left; the re-route supersedes the gate in its
// transaction, its result naming the fresh decision, which takes the
// place of its spare (the run at its bound) and holds its task.
func TestShrinkingEligibilityPastTheRowsSupersedesWithItsPlace(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	arch := eligibleGroup("architecture", 4, "a0", "a1", "a2", "a3")
	sec := eligibleGroup("security", 4, "b0", "b1", "b2", "b3")
	q := &quorumRouter{pol: policyOf("rev-1", false, arch, sec)}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000f4", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	operator := q.answer("")
	for i := range 24 {
		rec := decisionWhen(t, ld, g.ID, "readable", func(ledger.DecisionRecord) bool { return true })
		rc, err := e.RespondDecisionWith(t.Context(), g.ID, Response{ResponseID: fmt.Sprintf("o%d", i), Choice: choice(0), ExpectedRev: rec.Rev}, "admin", workbench(e), &operator)
		if de := decisionFailure(err); de == nil || de.Code != CodeNotEligible || rc.K == 0 {
			t.Fatalf("the operator's refusal %d: %+v %v", i, rc, err)
		}
	}
	for i := range 4 {
		rc := mustCount(t, e, ld, q, g.ID, fmt.Sprintf("a%d", i), fmt.Sprintf("a%d", i), ledger.ResponseCounted)
		e.ConfirmAuthority(g.ID, rc.K, true)
	}
	if rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen)); len(rec.Responses) != 28 {
		t.Fatalf("%d rows", len(rec.Responses))
	}
	fillDecisions(t, ld, run.ID, ledger.MaxDecisionsPerRun-2)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	r.dec.opened = ledger.MaxDecisionsPerRun - 1 // the fill and the gate, its spare the last place
	e.mu.Unlock()

	next := policyOf("rev-2", false, eligibleGroup("architecture", 4, "x0", "x1", "x2", "x3"), sec)
	q.mu.Lock()
	q.pol = next
	q.mu.Unlock()
	rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen))
	res, err := e.Reroute(t.Context(), g.ID, "switchman", next, ext.Rerouted, "op-1", rec.Rev)
	if err != nil || res.Superseded == "" {
		t.Fatalf("the re-route: %+v %v", res, err)
	}
	old := decisionWhen(t, ld, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	if old.SupersededBy != res.Superseded || !strings.Contains(old.Why, "responses filled its record") {
		t.Fatalf("the superseded gate: %+v", old.DecisionRow)
	}
	fresh := decisionWhen(t, ld, res.Superseded, "open", inState(ledger.DecisionOpen))
	if !ledger.PolicySpare(fresh.Policy) {
		t.Fatalf("the fresh decision holds no spare: %s", fresh.Policy)
	}
	if got := activeHolds(t, ld, run.ID); !equalStrings(got, []string{"deploy " + res.Superseded}) {
		t.Fatalf("holds %q", got)
	}
	stopRun(t, e, run.ID)
}

// A held answer never counts: to a gate under a policy that counts
// approvals it is refused answered_at_server, kept refused, and nothing is
// counted.
func TestAHeldAnswerNeverCounts(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	q := &quorumRouter{pol: policyOf("rev-1", true, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000f5", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	conf := q.answer("ana")
	rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen))
	rc, err := e.RespondHeldWith(t.Context(), g.ID, Response{ResponseID: "h1", Choice: choice(0), ExpectedRev: rec.Rev}, "user:ana", held("ana", "decide", "f-1"), heldStatement, &conf)
	if de := decisionFailure(err); de == nil || de.Code != CodeAnsweredAtServer || rc.Status != ledger.ResponseRefused {
		t.Fatalf("the held answer: %+v %v", rc, err)
	}
	if rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen)); len(CountedOf(rec, e.Boot(), e.CountedPending)) != 0 || rec.Responses[0].Status != ledger.ResponseRefused {
		t.Fatalf("the record after the held answer: %+v", rec.Responses)
	}
	stopRun(t, e, run.ID)
}
