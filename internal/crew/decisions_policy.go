package crew

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

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
	// CodeNotNeeded is an eligible approval that fills no place the record
	// still needs (its groups' quorums met already): unkept, no row.
	CodeNotNeeded = "not_needed"
	// CodeAlreadyCounted is a second approval of a subject whose approval
	// counts already: unkept, no row.
	CodeAlreadyCounted = "already_counted"
	// CodeNotConfirmed is the code a counted approval reads with once it
	// no longer counts: its sign-in could not be rechecked (after its
	// commit, or not before a restart). Never a response's refusal.
	CodeNotConfirmed = "not_confirmed"
)

// EntryEligible is the kind of a policy entry that admits one identity, by
// its subject: a person a router's policy names among a group's eligible.
const EntryEligible = "eligible"

// maxPolicyReceipt is the room a receipt's words leave the fields of a
// router's policy (its why, groups, via, ref, override), as encoded: their
// worst case together, so a receipt under a router's policy keeps
// ledger.MaxDecisionReceipt.
const maxPolicyReceipt = 2 << 10

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

// PolicySinceCheck is PolicyCheck asking whether subject stayed eligible,
// without a break, since since (ext.ConfirmQuery.Since): a router that
// cannot say answers not eligible. It is called outside every
// transaction.
type PolicySinceCheck func(ctx context.Context, rec ledger.DecisionRecord, revision, subject string, since time.Time) ext.Confirmation

// SetPolicySince hands the engine the function that asks the registered
// router whether a subject stayed eligible since a time: every counted
// approval and the deciding one are confirmed so before the application
// (confirmAtApply). Called once, beside SetRouterPolicies.
func (e *Engine) SetPolicySince(check PolicySinceCheck) {
	if check != nil {
		e.policySince.Store(&check)
	} else {
		e.policySince.Store(nil)
	}
}

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
	// statement is a held answer's statement (RespondHeld), kept beside its
	// row.
	statement string
	// boot is this process's id and pending says whether a counted
	// response of decision n, k, waits for its sign-in's recheck here: what
	// a counted approval stands by (countedRows). Set by respond.
	boot    string
	pending func(n int64, k int) bool
}

// pendingOf is pol's pending for decision n's responses.
func (pol policyIn) pendingOf(n int64) func(k int) bool {
	return func(k int) bool { return pol.pending != nil && pol.pending(n, k) }
}

// approvesRecord says resp approves a record of type typ: an approval's or an
// assignment's first option (Approve, Done). Their second rejects; a
// clarification is answered whole by its first eligible answer.
func approvesRecord(typ string, resp Response) bool {
	return (typ == ledger.DecisionApproval || typ == ledger.DecisionAssignment) && resp.Choice != nil && *resp.Choice == 0
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
// says of the policy, and DR1's checks follow. counted says the response
// is an eligible approval that fills a place of a policy that counts
// approvals and leaves one unfilled (decisions_quorum.go): it is kept
// counted, the decision left open. An eligible approval that fills no
// place still needed is refused not_needed, and its subject's second one
// already_counted, each unkept.
func judgePolicy(rec ledger.DecisionRecord, resp Response, who Admittee, pol policyIn, rc *ResponseReceipt) (done, counted bool, err error) {
	id := rec.ID()
	p, routed := routedOf(rec.Policy)
	if !routed {
		if pol.routes && pol.conf != nil && pol.conf.Routes && rec.Origin != ledger.OriginCompare {
			// The router will route it now: told again by the route.
			return true, false, refuse(CodeStalePolicy, "%s's policy is being written: try again", id)
		}
		return false, false, nil
	}
	if who.Via == ViaRouter {
		rc.Code, rc.Words = CodeNotApprover, fmt.Sprintf("%s is answered by the people its policy names: a router's own answer is refused", id)
		return true, false, nil
	}
	if resp.Override && !who.operator() {
		return true, false, refuse(CodeInvalid, "only the operator overrides %s's policy", id)
	}
	if pol.routes && pol.conf == nil {
		// A door that did not ask the router: never taken as not in force.
		return true, false, refuse(CodeStalePolicy, "%s's policy was not confirmed for this answer: try again", id)
	}
	inForce := pol.routes && pol.conf.InForce
	if inForce && (pol.conf.Behind || pol.conf.Revision != "" && pol.conf.Revision != p.Revision) {
		return true, false, refuse(CodeStalePolicy, "%s's policy is changing: try again", id)
	}
	if !inForce && pol.routes && pol.conf.Revision != "" && pol.conf.Revision != p.Revision {
		// Not in force, of another revision than the one kept now: the
		// router's word was about another policy.
		return true, false, refuse(CodeStalePolicy, "%s's policy is changing: try again", id)
	}
	if !inForce {
		why := "no router confirms its policy now"
		if pol.conf != nil && pol.conf.Why != "" {
			why = pol.conf.Why
		}
		if !who.operator() {
			rc.Code, rc.Words = CodeNotApprover, fmt.Sprintf("%s's policy is not in force now (%s): the operator answers it", id, why)
			return true, false, nil
		}
		rc.Policy, rc.PolicyWhy = policyLocal, why
		return false, false, nil
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
		if !p.counts() || !approvesRecord(rec.Type, resp) {
			// One place, a clarification, or a Reject (Not done): the first
			// eligible answer answers the record whole.
			return false, false, nil
		}
		standing, _ := countedRows(rec, pol.boot, pol.pendingOf(rec.N))
		verdict, per := judgeCount(p, standing, who.Subject, rc.Groups)
		switch verdict {
		case countAlready:
			return true, false, refuse(CodeAlreadyCounted, "%s's approval of %s is counted already", who.Subject, id)
		case countNotNeeded:
			return true, false, refuse(CodeNotNeeded, "%s", p.notNeededWords(id, p.namedIn(who.Subject, rc.Groups), per))
		case countCounted:
			rc.Words = countedWords(id, nameOf(resp.ByName, who.Subject), p, rc.Groups, per)
			return false, true, nil
		}
		// Every place is filled with it: accepted, the combined answer.
		return false, false, nil
	}
	if !who.operator() {
		rc.Code, rc.Words = CodeNotEligible, p.whoAnswers(id)
		return true, false, nil
	}
	if !resp.Override {
		rc.Code, rc.Words = CodeNotEligible, p.whoAnswers(id)+": the operator answers only by an override, with its reason"
		if p.Override != ext.OverrideOperator {
			// Never send the operator to an override the policy refuses.
			rc.Words = p.whoAnswers(id) + ": its policy allows no override"
		}
		return true, false, nil
	}
	if p.Override != ext.OverrideOperator {
		rc.Code, rc.Words = CodeOverrideRefused, fmt.Sprintf("%s's policy allows no override", id)
		return true, false, nil
	}
	if len([]rune(strings.TrimSpace(resp.Rationale))) < minOverrideReason {
		rc.Code, rc.Words = CodeOverrideRefused, fmt.Sprintf("an override of %s's policy gives its reason, at least %d characters", id, minOverrideReason)
		return true, false, nil
	}
	rc.Override = true
	return false, false, nil
}

// RoutePolicy writes the router by's policy into decision id's record, in
// one transaction (the policy's transaction): when the record is open at
// rev (the rev the router was shown) under the server's own policy, the
// policy (Tx.PutPolicy, its spare kept), rev + 1 and the router's one
// routed row with its destinations, rule and revision; when it is answered
// already, the routed row alone, saying the policy came after the answer.
// A record a router routed before with no policy (RecordRoute), this one
// or one under another name, holds the router's one routed row already: its policy is written the same
// way, its row a router's change (rerouted, the router's group), as a
// re-route of a record under the server's own policy is. Its repeat is
// the router's routing row of the same revision, answered by that row's
// receipt; a policy of another revision is no repeat. A record at another
// rev is refused stale_decision (the caller tells it again); one that
// carries a router's policy already routed_by_policy; a comparison's
// selection, which takes none, and a policy with no revision, invalid.
// Nothing is written for a refusal.
func (e *Engine) RoutePolicy(ctx context.Context, id, by string, rev int, policy ext.RoutePolicy, to []Destination, rule string) (OpReceipt, error) {
	if policy.Revision == "" {
		// Its revision is its identity: what its repeat is known by. The
		// router's route is checked before (ext.RoutePolicy.Valid); a
		// caller that did not check is refused here, never answered as a
		// repeat of another write.
		return OpReceipt{}, refuse(CodeInvalid, "a router's policy for %s names its revision", id)
	}
	base := routeDetail(id, by, to, stripFormat(rule))
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
	op := operation{kind: ledger.HistoryRouted, once: true, actor: "router:" + by, revision: policy.Revision}
	op.choose = func(rec ledger.DecisionRecord) (operation, error) {
		o := op
		if slices.ContainsFunc(rec.History, func(h ledger.HistoryRow) bool {
			return h.Kind == ledger.HistoryRouted && h.OpID == "" && strings.HasPrefix(h.By, "router:")
		}) {
			// A router's one routed row is an earlier route's, this
			// router's or one registered under another name: the routed
			// rows' room is full.
			o.kind = ledger.HistoryRerouted
		}
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
	policy = shownPolicy(policy)
	b, err := json.Marshal(routedPolicy{
		Revision: policy.Revision, Approver: ledger.ApproverRouter, Quorum: 1, Authority: []PolicyEntry{{Kind: EntryOperator}},
		Router: router, Scope: policy.Scope, Pinned: policy.Pinned, Groups: policy.Groups, Distinct: policy.Distinct,
		Override: policy.Override, Unsupported: policy.Unsupported, Reroutes: policy.Reroutes, Recent: policy.Recent,
		RoutedAt: at, Spare: spare, Fallback: policyLocal,
	})
	return string(b), err
}

// shownPolicy is policy with its words as a card shows them: the format
// characters (Unicode's Cf: bidirectional controls, zero-width ones) taken
// out of every name, label, rule and gap a router gave, so none reorders
// or hides what is drawn beside it. A name left empty is its subject, a
// label its role. Nothing of the caller's is changed.
func shownPolicy(policy ext.RoutePolicy) ext.RoutePolicy {
	policy.Pinned.Rule = stripFormat(policy.Pinned.Rule)
	groups := make([]ext.Requirement, len(policy.Groups))
	for i, g := range policy.Groups {
		g.Label, g.Rule, g.Gap = cmp.Or(stripFormat(g.Label), g.Role), stripFormat(g.Rule), stripFormat(g.Gap)
		g.Eligible = slices.Clone(g.Eligible)
		for j := range g.Eligible {
			g.Eligible[j].Name = cmp.Or(stripFormat(g.Eligible[j].Name), g.Eligible[j].Subject)
		}
		groups[i] = g
	}
	if policy.Groups != nil {
		policy.Groups = groups
	}
	return policy
}

// stripFormat is s without its format characters (Unicode's Cf).
func stripFormat(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// confirmAtApply asks the router again, before d's application, whether
// the subject of its accepted response is still eligible under the
// record's policy (an answer that counted as eligible), since its
// response's time; and, where the policy counts approvals and the answer
// approves, the same of every counted approval that stands, since each
// one's time, then whether the approvals the router confirms still fill
// every place (fill, from the rows as they stand now). ok true for any
// other answer (an override, the server's own policy), whose rechecks are
// DR1's. Anything not confirmed (no longer eligible, not in force, no
// answer, a counted approval not rechecked) is ok false, with the words
// the decision is superseded with. The caller holds no lock.
func (e *Engine) confirmAtApply(d *decision) (ok bool, why string) {
	e.mu.Lock()
	ans := d.answer
	n, typ := d.n, d.typ
	e.mu.Unlock()
	if ans == nil || len(ans.groups) == 0 {
		return true, ""
	}
	who := nameOf(ans.byName, ans.actor)
	notConfirmed := who + "'s answer could not be confirmed"
	ctx, cancel := context.WithTimeout(context.Background(), (ledger.MaxDecisionResponses+1)*2*ext.ConfirmWait)
	defer cancel()
	rec, found, err := e.readDecision(ctx, n)
	if err != nil || !found {
		return false, notConfirmed
	}
	p, routed := routedOf(rec.Policy)
	check, since := e.policyCheck.Load(), e.policySince.Load()
	if !routed || check == nil {
		return false, notConfirmed
	}
	// confirm is the groups of groups the router confirms subject eligible
	// in now, and since at when it can be asked so, and ok when the router
	// confirms the subject eligible at all (in force at the record's
	// revision, in a group): an approval whose person it does not is void.
	confirm := func(subject string, at time.Time, groups []string) (out []string, ok bool) {
		cctx, cancel := context.WithTimeout(ctx, 2*ext.ConfirmWait)
		defer cancel()
		var c ext.Confirmation
		if since != nil {
			c = (*since)(cctx, rec, p.Revision, subject, at)
		} else {
			c = (*check)(cctx, rec, p.Revision, subject)
		}
		if !c.InForce || c.Behind || c.Revision != "" && c.Revision != p.Revision || len(c.Groups) == 0 {
			return nil, false
		}
		for _, g := range groups {
			if slices.Contains(c.Groups, g) {
				out = append(out, g)
			}
		}
		return out, true
	}
	at := time.Time{}
	for _, r := range rec.Responses {
		if r.K == ans.k {
			at = r.At
		}
	}
	mine, _ := confirm(ans.auth.Subject, at, ans.groups)
	if len(mine) == 0 {
		return false, notConfirmed
	}
	if !p.counts() || !approvesRecord(typ, Response{Choice: ans.choice}) {
		return true, ""
	}
	if since == nil {
		// Counted approvals are confirmed only since their time.
		return false, notConfirmed
	}
	standing, _ := countedRows(rec, e.boot, func(k int) bool { return e.CountedPending(n, k) })
	rows := []countedRow{{subject: ans.auth.Subject, groups: mine}}
	for _, c := range standing {
		if !c.confirmed {
			// Its sign-in's recheck has not answered: not established.
			return false, c.name + "'s approval could not be rechecked"
		}
		g, ok := confirm(c.subject, c.at, c.groups)
		if !ok {
			return false, c.name + "'s approval could not be confirmed"
		}
		// It fills the places of the groups it was counted in that the
		// router confirms its person in since then: none when a re-route
		// moved them (their later approval fills the new group's).
		c.groups = g
		rows = append(rows, c)
	}
	if filled, _ := fill(p, rows); filled < placesOf(p) {
		return false, rec.ID() + "'s approvals no longer fill its requirement"
	}
	return true, ""
}

// Reroute is a decision router's change of decision id's policy
// (ext.Host.Reroute, by the router named router): one transaction,
// conditional on the decision open and at expectedRev. Its answers, in
// order: ext.ErrQuestionAnswered for a decision no longer open; Unchanged,
// writing nothing, when policy's revision is the record's already (a
// repeat, however late); ext.ErrStale, with the record's rev and revision,
// when the record moved past expectedRev; ext.ErrRefused, with the words
// why, for a comparison's selection and for a policy whose Pinned or Scope
// is not the record's, or whose form goes from the requirement's groups
// back to the unsupported form. A record under the server's own policy is
// routed as a first route. Each of a record's first
// ledger.MaxDecisionRouterChanges changes writes a history row of kind why
// with opID; every later one is recorded in the policy itself (Reroutes
// and Recent), so a re-route never runs out. The policy is valid for the
// record already (the caller's check, ext.RoutePolicy.Valid).
func (e *Engine) Reroute(ctx context.Context, id, router string, policy ext.RoutePolicy, why, opID string, expectedRev int) (ext.RerouteResult, error) {
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return ext.RerouteResult{}, fmt.Errorf("%w: %q is not a decision's id", ext.ErrRefused, shortText(id))
	}
	rec, found, err := e.readDecision(ctx, n)
	switch {
	case err != nil:
		return ext.RerouteResult{}, err
	case !found:
		return ext.RerouteResult{}, fmt.Errorf("%w: no decision %s", ext.ErrNotFound, id)
	}
	if res, done, err := rerouteJudged(rec, policy, expectedRev); done {
		return res, err
	}
	fp := fingerprintOf([]any{"reroute", policy, why})
	actor := "router:" + router
	var stored string
	// fresh is the decision that supersedes the record in the re-route's
	// transaction, when the new policy leaves its counted approvals fewer
	// response rows than places to fill (Superseded).
	var fresh *decision
	op := operation{kind: why, opID: opID, fp: fp, actor: actor, rev: true, expectedRev: expectedRev}
	op.choose = func(rec ledger.DecisionRecord) (operation, error) {
		o := op
		if _, done, err := rerouteJudged(rec, policy, expectedRev); done {
			if err == nil {
				err = refuse(CodeDecisionConflict, "%s's revision is %s already", id, policy.Revision)
			}
			return o, err
		}
		changes := 0
		for _, h := range rec.History {
			if h.Kind == ledger.HistoryAssigned || h.Kind == ledger.HistoryDelegated || h.Kind == ledger.HistoryEscalated || h.Kind == ledger.HistoryRerouted {
				changes++
			}
		}
		next := policy
		next.Reroutes, next.Recent = 0, nil
		at := time.Now().UTC()
		routedAt := at
		if p, ok := routedOf(rec.Policy); ok {
			routedAt = p.RoutedAt
			next.Reroutes = p.Reroutes + 1
			next.Recent = slices.Clone(p.Recent)
		}
		o.rowless = changes >= ledger.MaxDecisionRouterChanges
		if o.rowless {
			// Past the history's room: the change at the head of Recent,
			// the oldest of its bound dropped.
			next.Recent = append([]ext.RouteChange{{Why: why, Revision: policy.Revision, At: at}}, next.Recent...)
			if len(next.Recent) > ext.MaxPolicyRecent {
				next.Recent = next.Recent[:ext.MaxPolicyRecent]
			}
		}
		var err error
		stored, err = storedPolicy(next, router, ledger.PolicySpare(rec.Policy), routedAt)
		if err != nil {
			return o, err
		}
		detail := func(ledger.DecisionRecord) (opDetail, error) {
			dt := opDetail{By: router, Revision: policy.Revision, Words: fmt.Sprintf("%s is %s by %s: revision %s", id, why, router, policy.Revision)}
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
		short := false
		if next, ok := routedOf(stored); ok && next.counts() {
			standing, _ := countedRows(rec, e.boot, func(k int) bool { return e.CountedPending(n, k) })
			short = rowsShort(rec, next, standing)
		}
		if short {
			// Its counted approvals' rows ran short under the new policy:
			// superseded in this transaction, its replacement taking the
			// place of its spare (decisions_quorum.go). No undo: the change
			// is the engine's at once.
			o.ahead = true
		}
		o.held = func(r *run, d *decision, rec ledger.DecisionRecord) (opDetail, func(), error) {
			dt, err := detail(rec)
			if err != nil || !short || d == nil || d.state != ledger.DecisionOpen {
				return dt, nil, err
			}
			replace, t := e.replaceTold(r, d, shortWords(id), ledger.DecisionOpen)
			fresh = replace
			dt.also, dt.alsoDone = t.more, t.done
			return dt, nil, nil
		}
		o.notHeld = detail
		return o, nil
	}
	rc, err := e.operate(ctx, id, op)
	var de *DecisionError
	switch {
	case err == nil:
		res := ext.RerouteResult{Rev: rc.Rev}
		e.mu.Lock()
		if fresh != nil && fresh.n != 0 {
			res.Superseded = fresh.id()
		}
		e.mu.Unlock()
		return res, nil
	case errors.As(err, &de) && de.Code == CodeStaleDecision:
		now, _, _ := e.readDecision(ctx, n)
		p, _, _ := RouterPolicyOf(now.Policy)
		return ext.RerouteResult{}, fmt.Errorf("%w: %s is at rev %d, revision %q", ext.ErrStale, id, now.Rev, p.Revision)
	case errors.As(err, &de) && (de.Code == CodeDecisionClosed || de.Code == CodeDecisionAnswered):
		return ext.RerouteResult{}, fmt.Errorf("%w: %s", ext.ErrQuestionAnswered, de.Words)
	case errors.As(err, &de) && de.Code == CodeDecisionConflict:
		// Its revision came first, from another try of the same change.
		now, _, _ := e.readDecision(ctx, n)
		return ext.RerouteResult{Rev: now.Rev, Unchanged: true}, nil
	case errors.As(err, &de):
		return ext.RerouteResult{}, fmt.Errorf("%w: %s", ext.ErrRefused, de.Words)
	}
	return ext.RerouteResult{}, err
}

// rerouteJudged is a re-route's judgement of rec before any write: done
// with the answer (Unchanged, or a refusal) when it is not to be written.
func rerouteJudged(rec ledger.DecisionRecord, policy ext.RoutePolicy, expectedRev int) (ext.RerouteResult, bool, error) {
	id := rec.ID()
	if rec.State != ledger.DecisionOpen {
		return ext.RerouteResult{}, true, fmt.Errorf("%w: %s is %s", ext.ErrQuestionAnswered, id, rec.State)
	}
	if rec.Origin == ledger.OriginCompare {
		return ext.RerouteResult{}, true, fmt.Errorf("%w: %s is a comparison's selection: it takes no router's policy", ext.ErrRefused, id)
	}
	p, routed := routedOf(rec.Policy)
	if routed && p.Revision == policy.Revision {
		return ext.RerouteResult{Rev: rec.Rev, Unchanged: true}, true, nil
	}
	if rec.Rev != expectedRev {
		return ext.RerouteResult{}, true, fmt.Errorf("%w: %s is at rev %d, revision %q", ext.ErrStale, id, rec.Rev, p.Revision)
	}
	if routed {
		switch {
		case !reflect.DeepEqual(p.Pinned, policy.Pinned):
			return ext.RerouteResult{}, true, fmt.Errorf("%w: %s's requirement is pinned for its life: a re-route changes who is eligible, never what is required", ext.ErrRefused, id)
		case p.Scope != policy.Scope:
			return ext.RerouteResult{}, true, fmt.Errorf("%w: %s's scope is its first route's", ext.ErrRefused, id)
		case !p.Unsupported && policy.Unsupported:
			return ext.RerouteResult{}, true, fmt.Errorf("%w: %s routes the requirement's own groups: the one change of form is to them, never back", ext.ErrRefused, id)
		}
	}
	return ext.RerouteResult{}, false, nil
}

// AskRouter is the operator's route of decision id to its decision router
// ({kind: router}): one of the operator's operations, a routed row saying
// the router was asked again, no rev. The caller tells the router again
// once it has committed.
func (e *Engine) AskRouter(ctx context.Context, id, opID string, expectedRev int, note, actor string) (OpReceipt, error) {
	if err := e.refuseSelection(ctx, id, "%s is a comparison's selection, the operator's to answer: it is not routed"); err != nil {
		return OpReceipt{}, err
	}
	fp := fingerprintOf([]any{"route", []Destination{{Kind: DestRouter}}, note})
	note, err := cleanOpText(note, false)
	if err != nil {
		return OpReceipt{}, err
	}
	dt := opDetail{Note: note, To: []any{map[string]string{"kind": DestRouter}}, By: actor, Words: fmt.Sprintf("%s is routed to its decision router again", id)}
	return e.operate(ctx, id, operation{kind: ledger.HistoryRouted, opID: opID, fp: fp, actor: actor, expectedRev: expectedRev,
		held:    func(*run, *decision, ledger.DecisionRecord) (opDetail, func(), error) { return dt, nil, nil },
		notHeld: func(ledger.DecisionRecord) (opDetail, error) { return dt, nil }})
}

// DestRouter is a route to the registered decision router: ask it again.
const DestRouter = "router"
