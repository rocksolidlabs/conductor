package crew

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A decision router's policy on a decision record (SW1,
// docs/plans/decision-policy.md). With no router registered that confirms
// policies (ext.PolicyConfirmer), nothing here runs: every record opens and
// stays under the server's own policy (local/1) and is answered as DR1
// ships it. With one, a record opens under local/1 with a spare place for
// its replacement (openingPolicy); the router's answer writes its policy
// into the record (RoutePolicy); only the persons it names answer while
// the router confirms it (respondTx's judgement, the confirmation handed
// in by the route that asked it); the operator answers in its place only by
// an explicit override where the policy allows one; an eligible answer is
// confirmed again before it is applied (applyOne); and whenever the router
// does not confirm the policy, the operator answers under local/1, the
// receipt saying why.

// The refusal codes of a router's policy.
const (
	CodeNotEligible     = "not_eligible"
	CodeOverrideRefused = "override_refused"
	CodeRoutedByPolicy  = "routed_by_policy"
	CodeNoRouter        = "no_router"
)

// EntryEligible is the kind of a policy entry that admits one identity, by
// its subject: a person a router's policy names among a group's eligible.
const EntryEligible = "eligible"

// minOverrideReason is the shortest reason an override is given with, in
// characters, once trimmed.
const minOverrideReason = 10

// policySpareJSON is the server's own policy (policyJSON) with a spare: a
// record opened while a router routes policies holds a place for the
// replacement its routed answer may need from its opening.
const policySpareJSON = `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}],"spare":true}`

// PolicyCheck asks the registered router to confirm a record's policy at
// revision, for subject ("" for whether it is in force at all): the
// server's guarded call (bounded by ext.ConfirmWait and held to
// ext.MaxBehind), never failing: a router that does not answer is not in
// force. It is called outside every transaction.
type PolicyCheck func(ctx context.Context, rec ledger.DecisionRecord, revision, subject string) ext.Confirmation

// SetRouterPolicies tells the engine whether the registered decision
// router confirms policies, and hands it the one function that asks it.
// Called once, before the engine runs anything.
func (e *Engine) SetRouterPolicies(on bool, check PolicyCheck) {
	e.routesPolicies.Store(on)
	if on && check != nil {
		e.policyCheck.Store(&check)
	} else {
		e.policyCheck.Store(nil)
	}
}

// RoutesPolicies says the registered router confirms policies.
func (e *Engine) RoutesPolicies() bool { return e.routesPolicies.Load() }

// openingPolicy is the policy a record of origin opens under: the server's
// own, with a spare while the router routes policies, never for a
// comparison's selection (the operator's alone).
func (e *Engine) openingPolicy(origin string) string {
	if e.RoutesPolicies() && origin != ledger.OriginCompare {
		return policySpareJSON
	}
	return policyJSON
}

// routedPolicy is a router's policy as a record keeps it.
type routedPolicy struct {
	Revision    string                `json:"revision"`
	Approver    string                `json:"approver"`
	Quorum      int                   `json:"quorum"`
	Authority   []PolicyEntry         `json:"authority"`
	Router      string                `json:"router"`
	Scope       string                `json:"scope"`
	Pinned      ext.PinnedRequirement `json:"pinned"`
	Groups      []ext.Requirement     `json:"groups"`
	Distinct    bool                  `json:"distinct,omitempty"`
	Override    string                `json:"override"`
	Unsupported bool                  `json:"unsupported,omitempty"`
	Reroutes    int                   `json:"reroutes,omitempty"`
	Recent      []ext.RouteChange     `json:"recent,omitempty"`
	RoutedAt    time.Time             `json:"routedAt"`
	Spare       bool                  `json:"spare,omitempty"`
	Fallback    string                `json:"fallback"`
}

// RouterPolicyOf is the router's policy a record's policy column holds, ok
// false under the server's own.
func RouterPolicyOf(raw string) (ext.RoutePolicy, string, bool) {
	p, ok := routedOf(raw)
	if !ok {
		return ext.RoutePolicy{}, "", false
	}
	return ext.RoutePolicy{Revision: p.Revision, Scope: p.Scope, Pinned: p.Pinned, Groups: p.Groups, Distinct: p.Distinct,
		Override: p.Override, Unsupported: p.Unsupported, Reroutes: p.Reroutes, Recent: p.Recent}, p.Router, true
}

// routedOf decodes a record's router policy, ok false for the server's own
// (or a column that does not decode, which admits only the operator).
func routedOf(raw string) (routedPolicy, bool) {
	var p routedPolicy
	if !ledger.RouterPolicy(raw) || json.Unmarshal([]byte(raw), &p) != nil {
		return routedPolicy{}, false
	}
	return p, true
}

// eligibleOf is subject's place in p: the groups that name it among their
// eligible, in order, and the first such entry; nil when none does.
func (p routedPolicy) eligibleOf(subject string) (groups []string, first *ext.Eligible) {
	if subject == "" {
		return nil, nil
	}
	for _, g := range p.Groups {
		for i := range g.Eligible {
			if g.Eligible[i].Subject == subject {
				groups = append(groups, g.Role)
				if first == nil {
					e := g.Eligible[i]
					first = &e
				}
				break
			}
		}
	}
	return groups, first
}

// whoAnswers is the words of who answers p's record, for a refusal: the
// first group's label and its eligible names, or its gap.
func (p routedPolicy) whoAnswers(id string) string {
	if len(p.Groups) == 0 {
		return id + " is answered by the people its router's policy names"
	}
	g := p.Groups[0]
	if len(g.Eligible) == 0 {
		return fmt.Sprintf("%s is for the %s: %s", id, g.Label, g.Gap)
	}
	names := make([]string, 0, len(g.Eligible))
	for _, e := range g.Eligible {
		names = append(names, e.Name)
	}
	return fmt.Sprintf("%s is for the %s: %s answers it", id, g.Label, strings.Join(names, " or "))
}

// policyEntries is a router policy's entries: the operator's, and one
// eligible entry per distinct subject among its groups' eligible; never
// the decide grant's, whatever the request's for says (only the policy
// chooses).
func (p routedPolicy) entries() []PolicyEntry {
	out := []PolicyEntry{{Kind: EntryOperator}}
	for _, g := range p.Groups {
		for _, el := range g.Eligible {
			if !slices.ContainsFunc(out, func(e PolicyEntry) bool { return e.Kind == EntryEligible && e.Subject == el.Subject }) {
				out = append(out, PolicyEntry{Kind: EntryEligible, Subject: el.Subject})
			}
		}
	}
	return out
}

// policyIn is what a respond route hands the engine of the router beside a
// response: whether the registered router confirms policies, and its
// confirmation of the record's policy for the responder, asked before the
// transaction (nil when the route asked none).
type policyIn struct {
	routes bool
	conf   *ext.Confirmation
}

// RespondDecisionWith is RespondDecision with the router's confirmation
// the route asked for the record's policy and the responder's subject
// (nil for none asked), which the response is judged with in its
// transaction.
func (e *Engine) RespondDecisionWith(ctx context.Context, id string, resp Response, actor string, auth Authority, conf *ext.Confirmation) (ResponseReceipt, error) {
	if conf != nil {
		c := *conf
		c.Groups = slices.Clone(conf.Groups)
		conf = &c
	}
	return e.respond(ctx, id, resp, actor, auth, policyIn{routes: e.RoutesPolicies(), conf: conf})
}

// judgePolicy is the router's policy's judgement of a response to the
// record rec, in its transaction, before DR1's checks (The respond route's
// judgement): done says it answered (a refusal in rc, or an error for one
// that is not kept); else rc carries what an accepted response's receipt
// says of the policy, and DR1's checks follow.
func judgePolicy(rec ledger.DecisionRecord, resp Response, who Admittee, pol policyIn, rc *ResponseReceipt) (done bool, err error) {
	id := rec.ID()
	p, routed := routedOf(rec.Policy)
	if !routed {
		if pol.routes && pol.conf != nil && pol.conf.Routes && rec.Origin != ledger.OriginCompare {
			// The router will route it now: told again by the route.
			return true, refuse(CodeStalePolicy, "%s's policy is being written: try again", id)
		}
		return false, nil
	}
	if who.Via == ViaRouter {
		rc.Code, rc.Words = CodeNotApprover, fmt.Sprintf("%s is answered by the people its policy names: a router's own answer is refused", id)
		return true, nil
	}
	if resp.Override && !who.operator() {
		return true, refuse(CodeInvalid, "only the operator overrides %s's policy", id)
	}
	if pol.routes && pol.conf == nil {
		// A door that did not ask the router: never taken as not in force.
		return true, refuse(CodeStalePolicy, "%s's policy was not confirmed for this answer: try again", id)
	}
	inForce := pol.routes && pol.conf.InForce
	if inForce && (pol.conf.Behind || pol.conf.Revision != "" && pol.conf.Revision != p.Revision) {
		return true, refuse(CodeStalePolicy, "%s's policy is changing: try again", id)
	}
	if !inForce {
		why := "no router confirms its policy now"
		if pol.conf != nil && pol.conf.Why != "" {
			why = pol.conf.Why
		}
		if !who.operator() {
			rc.Code, rc.Words = CodeNotApprover, fmt.Sprintf("%s's policy is not in force now (%s): the operator answers it", id, why)
			return true, nil
		}
		rc.Policy, rc.PolicyWhy = policyLocal, why
		return false, nil
	}
	groups, el := p.eligibleOf(who.Subject)
	confirmed := slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(pol.conf.Groups, g) })
	if who.Via == ViaIdentity && confirmed && Admits(p.entries(), who) {
		// Eligible: the groups the router confirms it in now, of those the
		// policy names it in. An operator's identity that is eligible answers
		// as eligible, never as an override.
		for _, g := range groups {
			if slices.Contains(pol.conf.Groups, g) {
				rc.Groups = append(rc.Groups, g)
			}
		}
		rc.Via, rc.Ref = el.Via, el.Ref
		return false, nil
	}
	if !who.operator() {
		rc.Code, rc.Words = CodeNotEligible, p.whoAnswers(id)
		return true, nil
	}
	if !resp.Override {
		rc.Code, rc.Words = CodeNotEligible, p.whoAnswers(id)+": the operator answers only by an override, with its reason"
		return true, nil
	}
	if p.Override != ext.OverrideOperator {
		rc.Code, rc.Words = CodeOverrideRefused, fmt.Sprintf("%s's policy allows no override", id)
		return true, nil
	}
	if len([]rune(strings.TrimSpace(resp.Rationale))) < minOverrideReason {
		rc.Code, rc.Words = CodeOverrideRefused, fmt.Sprintf("an override of %s's policy gives its reason, at least %d characters", id, minOverrideReason)
		return true, nil
	}
	rc.Override = true
	return false, nil
}

// RoutePolicy writes the router by's policy into decision id's record, in
// one transaction (the policy's transaction): when the record is open at
// rev (the rev the router was shown) under the server's own policy, the
// policy (Tx.PutPolicy, its spare kept), rev + 1 and the router's one
// routed row with its destinations, rule and revision; when it is answered
// already, the routed row alone, saying the policy came after the answer.
// A record at another rev is refused stale_decision (the caller tells it
// again); one that carries a router's policy already routed_by_policy; a
// comparison's selection, which takes none, invalid. Nothing is written
// for a refusal.
func (e *Engine) RoutePolicy(ctx context.Context, id, by string, rev int, policy ext.RoutePolicy, to []Destination, rule string) (OpReceipt, error) {
	base := routeDetail(id, by, to, rule)
	base.Revision = policy.Revision
	detail := func(rec ledger.DecisionRecord) (opDetail, error) {
		dt := base
		if rec.State != ledger.DecisionOpen {
			dt.Words += ": its router's policy came after its answer, and changes nothing"
			return dt, nil
		}
		stored, err := storedPolicy(policy, by, ledger.PolicySpare(rec.Policy), time.Now().UTC())
		if err != nil {
			return opDetail{}, err
		}
		dt.edit = func(dtx decisionTx, row ledger.DecisionRow) error {
			ok, err := dtx.PutPolicy(row.N, stored, row.Rev)
			if err != nil {
				return err
			}
			if !ok {
				return refuse(CodeStaleDecision, "%s moved while its policy was written", id)
			}
			return nil
		}
		return dt, nil
	}
	op := operation{kind: ledger.HistoryRouted, once: true, actor: "router:" + by}
	op.choose = func(rec ledger.DecisionRecord) (operation, error) {
		o := op
		switch {
		case rec.Origin == ledger.OriginCompare:
			return o, refuse(CodeInvalid, "%s is a comparison's selection: it takes no router's policy", id)
		case ledger.RouterPolicy(rec.Policy):
			return o, refuse(CodeRoutedByPolicy, "%s carries its router's policy already: a change of it is a re-route", id)
		case rec.Final():
			return o, refuse(CodeDecisionClosed, "%s is %s", id, rec.State)
		case rec.State == ledger.DecisionOpen:
			if rec.Rev != rev {
				return o, refuse(CodeStaleDecision, "%s is at rev %d, not %d", id, rec.Rev, rev)
			}
			o.rev, o.expectedRev = true, rev
		}
		o.held = func(_ *run, _ *decision, rec ledger.DecisionRecord) (opDetail, func(), error) {
			dt, err := detail(rec)
			return dt, nil, err
		}
		o.notHeld = func(rec ledger.DecisionRecord) (opDetail, error) { return detail(rec) }
		return o, nil
	}
	return e.operate(ctx, id, op)
}

// storedPolicy is policy as a record keeps it: the server's fields beside
// the router's (its approver the router, the router's name, when it was
// first written, the spare kept, the policy it falls back to).
func storedPolicy(policy ext.RoutePolicy, router string, spare bool, at time.Time) (string, error) {
	b, err := json.Marshal(routedPolicy{
		Revision: policy.Revision, Approver: ledger.ApproverRouter, Quorum: 1, Authority: []PolicyEntry{{Kind: EntryOperator}},
		Router: router, Scope: policy.Scope, Pinned: policy.Pinned, Groups: policy.Groups, Distinct: policy.Distinct,
		Override: policy.Override, Unsupported: policy.Unsupported, Reroutes: policy.Reroutes, Recent: policy.Recent,
		RoutedAt: at, Spare: spare, Fallback: policyLocal,
	})
	return string(b), err
}

// confirmAtApply asks the router again, before d's application, whether
// the subject of its accepted response is still eligible under the
// record's policy (an answer that counted as eligible); true for any other
// answer (an override, the server's own policy), whose rechecks are DR1's.
// The caller holds no lock.
func (e *Engine) confirmAtApply(d *decision) bool {
	e.mu.Lock()
	ans := d.answer
	n := d.n
	e.mu.Unlock()
	if ans == nil || len(ans.groups) == 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*ext.ConfirmWait)
	defer cancel()
	rec, ok, err := e.readDecision(ctx, n)
	if err != nil || !ok {
		return false
	}
	p, routed := routedOf(rec.Policy)
	check := e.policyCheck.Load()
	if !routed || check == nil {
		return false
	}
	c := (*check)(ctx, rec, p.Revision, ans.auth.Subject)
	return c.InForce && !c.Behind && (c.Revision == "" || c.Revision == p.Revision) &&
		slices.ContainsFunc(ans.groups, func(g string) bool { return slices.Contains(c.Groups, g) })
}
