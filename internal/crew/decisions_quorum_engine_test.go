package crew

import (
	"context"
	"errors"
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

// noApprovalStuck holds the invariants the judgement keeps, on decision id
// as the ledger holds it now: while it is open, when every eligible person
// approving together would fill every place, some person's approval
// answers or is counted; and an eligible person's
// approval answers it exactly when, added to the approvals that stand, it
// would fill every place, by a brute-force count that shares nothing with
// fill; so no approval is refused, or merely counted, while it could
// complete the decision.
func noApprovalStuck(t *testing.T, e *Engine, ld *ledger.Ledger, id string) {
	t.Helper()
	rec := decisionWhen(t, ld, id, "readable", func(ledger.DecisionRecord) bool { return true })
	if rec.State != ledger.DecisionOpen {
		return
	}
	p, ok := routedOf(rec.Policy)
	if !ok {
		return
	}
	standing, _ := countedRows(rec, e.Boot(), func(k int) bool { return e.CountedPending(rec.N, k) })
	if can, moves := completable(p, standing); can && !moves {
		t.Fatalf("%s: every eligible person together completes it, and no one's approval answers or is counted", id)
	}
	for _, g := range p.Groups {
		for _, el := range g.Eligible {
			groups := p.confirmedFor(el.Subject)
			completes := bruteFill(p, append(slices.Clone(standing), countedRow{subject: el.Subject, groups: groups})) >= placesOf(p)
			v, _ := judgeCount(p, standing, el.Subject, groups)
			if completes != (v == countAnswers) {
				t.Fatalf("%s: %s's approval would complete it: %v; its verdict %d", id, el.Subject, completes, v)
			}
		}
	}
}

// rerouteTo has q hold pol and re-routes id to it at its rev now, its
// result not a supersession.
func rerouteTo(t *testing.T, e *Engine, ld *ledger.Ledger, q *quorumRouter, id string, pol ext.RoutePolicy, opID string) {
	t.Helper()
	q.mu.Lock()
	q.pol = pol
	q.mu.Unlock()
	rec := decisionWhen(t, ld, id, "open", inState(ledger.DecisionOpen))
	res, err := e.Reroute(t.Context(), id, "switchman", pol, ext.Rerouted, opID, rec.Rev)
	if err != nil || res.Superseded != "" || res.Unchanged {
		t.Fatalf("the re-route to %s: %+v %v", pol.Revision, res, err)
	}
}

// countedHere is mustCount with its recheck admitted at once, as the
// respond route rechecks a response after its commit.
func countedHere(t *testing.T, e *Engine, ld *ledger.Ledger, q *quorumRouter, id, subject, rid, status string) {
	t.Helper()
	rc := mustCount(t, e, ld, q, id, subject, rid, status)
	e.ConfirmAuthority(id, rc.K, true)
	noApprovalStuck(t, e, ld, id)
}

// appliedOnce waits for id applied and says it was applied once.
func appliedOnceIn(t *testing.T, ld *ledger.Ledger, id string) ledger.DecisionRecord {
	t.Helper()
	rec := decisionWhen(t, ld, id, "applied", inState(ledger.DecisionApplied))
	n := 0
	for _, h := range rec.History {
		if h.Kind == ledger.HistoryApplied {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s applied %d times", id, n)
	}
	return rec
}

// Ana's approval stays counted while a re-route takes her away; cam is
// counted meanwhile; a re-route names ana again: the counted approvals
// alone fill both places, and the decision waits for an answer. The next
// eligible approval answers it, ben's (not distinct) or ana's own, already
// counted (distinct persons), and it is applied once. The invariant holds
// after every step.
func TestApprovalsThatFillEveryPlaceAfterAReRouteAreCompleted(t *testing.T) {
	for _, c := range []struct {
		distinct bool
		last     string
	}{{false, "ben"}, {true, "ana"}} {
		t.Run(fmt.Sprintf("distinct %v, %s answers", c.distinct, c.last), func(t *testing.T) {
			dir := newRepo(t)
			e, _, ld := decisionEngine(t)
			sec := eligibleGroup("security", 1, "cam")
			q := &quorumRouter{pol: policyOf("rev-1", c.distinct, eligibleGroup("architecture", 1, "ana"), sec)}
			q.routes(e)
			run := launchGated(t, e, dir, fmt.Sprintf("api-sweep-0000%04x", map[bool]int{false: 0xf6, true: 0xf7}[c.distinct]), repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
			g := gateOf(t, e, ld, run.ID)
			routeUnder(t, e, ld, q, g.ID)
			countedHere(t, e, ld, q, g.ID, "ana", "a1", ledger.ResponseCounted)
			rerouteTo(t, e, ld, q, g.ID, policyOf("rev-2", c.distinct, eligibleGroup("architecture", 1, "ben"), sec), "op-1")
			noApprovalStuck(t, e, ld, g.ID)
			countedHere(t, e, ld, q, g.ID, "cam", "c1", ledger.ResponseCounted)
			rerouteTo(t, e, ld, q, g.ID, policyOf("rev-3", c.distinct, eligibleGroup("architecture", 1, "ana", "ben"), sec), "op-2")
			noApprovalStuck(t, e, ld, g.ID)
			rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen))
			if qu := QuorumOf(rec, e.Boot(), e.CountedPending); qu.Filled != 2 || qu.Places != 2 {
				t.Fatalf("the count after ana's return: %+v", qu)
			}
			rc := mustCount(t, e, ld, q, g.ID, c.last, "z1", ledger.ResponseAccepted)
			e.ConfirmAuthority(g.ID, rc.K, true)
			appliedOnceIn(t, ld, g.ID)
			waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
			stopRun(t, e, run.ID)
		})
	}
}

// A re-route moves ana, counted for architecture, to security, the only
// person it names there: her standing approval fills nothing, and her
// fresh approval is counted for security, not refused already counted;
// her third, while architecture still waits, is already counted and
// takes no row; ben's answers it, applied once. The invariant holds after
// every step.
func TestAnApproverMovedByAReRouteIsJudgedAnew(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	q := &quorumRouter{pol: policyOf("rev-1", true, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000f8", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	countedHere(t, e, ld, q, g.ID, "ana", "a1", ledger.ResponseCounted)
	rerouteTo(t, e, ld, q, g.ID, policyOf("rev-2", true, eligibleGroup("architecture", 1, "ben"), eligibleGroup("security", 1, "ana")), "op-1")
	noApprovalStuck(t, e, ld, g.ID)
	countedHere(t, e, ld, q, g.ID, "ana", "a2", ledger.ResponseCounted)
	rows := len(decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen)).Responses)
	if _, err := approveAs(t, e, ld, q, g.ID, "ana", "a3", 0); decisionFailure(err) == nil || decisionFailure(err).Code != CodeAlreadyCounted {
		t.Fatalf("ana's third approval: %v", err)
	}
	if got := len(decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen)).Responses); got != rows {
		t.Fatalf("already counted took a row: %d, was %d", got, rows)
	}
	noApprovalStuck(t, e, ld, g.ID)
	rc := mustCount(t, e, ld, q, g.ID, "ben", "b1", ledger.ResponseAccepted)
	e.ConfirmAuthority(g.ID, rc.K, true)
	appliedOnceIn(t, ld, g.ID)
	stopRun(t, e, run.ID)
}

// A restart between the last approval and the application: cam's answer,
// its recheck not made before the server's end, is never applied; the
// continuation supersedes the answered gate by a fresh one with its hold.
func TestARestartBeforeTheApplicationSupersedesTheCombinedAnswer(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	q := &quorumRouter{pol: policyOf("rev-1", true, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000f9", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	countedHere(t, e, ld, q, g.ID, "ana", "a1", ledger.ResponseCounted)
	mustCount(t, e, ld, q, g.ID, "cam", "c1", ledger.ResponseAccepted)
	decisionWhen(t, ld, g.ID, "answered", inState(ledger.DecisionAnswered))

	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	q.routes(e2)
	next := continueRun(t, e2, run.ID)
	old := decisionWhen(t, ld2, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	if old.Applied != "" || old.SupersededBy == "" {
		t.Fatalf("the answered gate after the restart: %+v", old.DecisionRow)
	}
	if got := activeHolds(t, ld2, next.ID); !equalStrings(got, []string{"deploy " + old.SupersededBy}) {
		t.Fatalf("holds %q", got)
	}
	stopRun(t, e2, next.ID)
}

// Rows run short with no re-route: 24 kept refusals and four approvals
// counted for architecture, never rechecked before the server's end. After
// the restart they count no more: eight places to fill, four rows left.
// The next response supersedes the gate first, its replacement holding the
// task, and is refused stale_decision, saying so.
func TestTheResponseAfterApprovalsLapsedSupersedesWhenRowsRunShort(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	arch := eligibleGroup("architecture", 4, "a0", "a1", "a2", "a3")
	sec := eligibleGroup("security", 4, "b0", "b1", "b2", "b3")
	q := &quorumRouter{pol: policyOf("rev-1", false, arch, sec)}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000fa", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	operator := q.answer("")
	for i := range 24 {
		rec := decisionWhen(t, ld, g.ID, "readable", func(ledger.DecisionRecord) bool { return true })
		if _, err := e.RespondDecisionWith(t.Context(), g.ID, Response{ResponseID: fmt.Sprintf("o%d", i), Choice: choice(0), ExpectedRev: rec.Rev}, "admin", workbench(e), &operator); decisionFailure(err) == nil {
			t.Fatalf("the operator's answer %d was taken", i)
		}
	}
	for i := range 4 {
		mustCount(t, e, ld, q, g.ID, fmt.Sprintf("a%d", i), fmt.Sprintf("a%d", i), ledger.ResponseCounted)
	}
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	q.routes(e2)
	next := continueRun(t, e2, run.ID)
	_, err := approveAs(t, e2, ld2, q, g.ID, "b0", "b0", 0)
	if de := decisionFailure(err); de == nil || de.Code != CodeStaleDecision || !strings.Contains(de.Words, "responses filled its record") {
		t.Fatalf("the response after the lapse: %v", err)
	}
	old := decisionWhen(t, ld2, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	if got := activeHolds(t, ld2, next.ID); !equalStrings(got, []string{"deploy " + old.SupersededBy}) {
		t.Fatalf("holds %q", got)
	}
	stopRun(t, e2, next.ID)
}

// A void recheck that leaves the rows short supersedes the gate at once:
// 24 kept refusals, three architecture approvals admitted and a fourth
// whose recheck refuses it: five places to fill, four rows left.
func TestAVoidedRecheckThatLeavesRowsShortSupersedes(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	q := &quorumRouter{pol: policyOf("rev-1", false, eligibleGroup("architecture", 4, "a0", "a1", "a2", "a3"), eligibleGroup("security", 4, "b0", "b1", "b2", "b3"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000fb", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	operator := q.answer("")
	for i := range 24 {
		rec := decisionWhen(t, ld, g.ID, "readable", func(ledger.DecisionRecord) bool { return true })
		if _, err := e.RespondDecisionWith(t.Context(), g.ID, Response{ResponseID: fmt.Sprintf("o%d", i), Choice: choice(0), ExpectedRev: rec.Rev}, "admin", workbench(e), &operator); decisionFailure(err) == nil {
			t.Fatalf("the operator's answer %d was taken", i)
		}
	}
	for i := range 3 {
		countedHere(t, e, ld, q, g.ID, fmt.Sprintf("a%d", i), fmt.Sprintf("a%d", i), ledger.ResponseCounted)
	}
	rc := mustCount(t, e, ld, q, g.ID, "a3", "a3", ledger.ResponseCounted)
	e.ConfirmAuthority(g.ID, rc.K, false)
	old := decisionWhen(t, ld, g.ID, "superseded", inState(ledger.DecisionSuperseded))
	if !strings.Contains(old.Why, "responses filled its record") || !hasRechecked(old, rc.K, false) {
		t.Fatalf("the gate after the void recheck: %+v", old.DecisionRow)
	}
	stopRun(t, e, run.ID)
}

// An open decision with an identity's counted approval holds a place for
// its replacement in the engine's count as in the ledger's: a gate opened
// before the router routed policies (no spare), in a run of 999 decisions,
// takes the run's last place with ana's counted approval; then the engine
// has no room for another decision, and the ledger refuses one.
func TestACountedApprovalHoldsAPlaceInTheEnginesCount(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000fc", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	q := &quorumRouter{pol: policyOf("rev-1", false, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "cam"))}
	q.routes(e)
	routeUnder(t, e, ld, q, g.ID)
	if rec := decisionWhen(t, ld, g.ID, "routed", inState(ledger.DecisionOpen)); ledger.PolicySpare(rec.Policy) {
		t.Fatalf("the gate holds a spare: %s", rec.Policy)
	}
	fillDecisions(t, ld, run.ID, ledger.MaxDecisionsPerRun-2)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	r.dec.opened = ledger.MaxDecisionsPerRun - 1 // the fill and the gate
	held, room := r.decisionsHeld(), r.decisionRoom(1, false)
	e.mu.Unlock()
	if held != ledger.MaxDecisionsPerRun-1 || !room {
		t.Fatalf("before the approval: the run holds %d, room %v", held, room)
	}
	countedHere(t, e, ld, q, g.ID, "ana", "a1", ledger.ResponseCounted)
	e.mu.Lock()
	held, room = r.decisionsHeld(), r.decisionRoom(1, false)
	e.mu.Unlock()
	if held != ledger.MaxDecisionsPerRun || room {
		t.Fatalf("after ana's counted approval: the run holds %d, room %v", held, room)
	}
	_, err := ld.Transition(t.Context(), run.ID, func(tx *ledger.Tx) error {
		_, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginAgent, Type: ledger.DecisionClarification, TaskID: "deploy", Attempt: 1,
			Member: "ops", Fingerprint: "fp-past", Request: `{"question":"One more?","subject":{"kind":"commit","commit":"` + strings.Repeat("a", 40) + `"}}`, Policy: policyJSON})
		return err
	})
	if !errors.Is(err, ledger.ErrDecisionLimit) {
		t.Fatalf("the ledger's opening past the place: %v", err)
	}
	stopRun(t, e, run.ID)
}

// Under distinct persons, ana counted for security; a re-route names her
// in architecture too, and ben in security. Ana's approval fills no place
// more by itself, yet widens where she may be placed: it is counted, not
// refused; ben's approval then answers (ana in architecture, ben in
// security), applied once. The invariants hold after every step.
func TestAnApprovalThatWidensItsPersonsGroupsIsCounted(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	q := &quorumRouter{pol: policyOf("rev-1", true, eligibleGroup("architecture", 1, "cam"), eligibleGroup("security", 1, "ana"))}
	q.routes(e)
	run := launchGated(t, e, dir, "api-sweep-000000fd", repoCrew(dir, work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	routeUnder(t, e, ld, q, g.ID)
	countedHere(t, e, ld, q, g.ID, "ana", "a1", ledger.ResponseCounted)
	rerouteTo(t, e, ld, q, g.ID, policyOf("rev-2", true, eligibleGroup("architecture", 1, "ana"), eligibleGroup("security", 1, "ana", "ben")), "op-1")
	noApprovalStuck(t, e, ld, g.ID)
	countedHere(t, e, ld, q, g.ID, "ana", "a2", ledger.ResponseCounted)
	rc := mustCount(t, e, ld, q, g.ID, "ben", "b1", ledger.ResponseAccepted)
	e.ConfirmAuthority(g.ID, rc.K, true)
	appliedOnceIn(t, ld, g.ID)
	stopRun(t, e, run.ID)
}
