package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A decision router's policy on a decision record (SW1,
// docs/plans/decision-policy.md), the server's half: the registered
// router is asked to confirm a record's policy (confirm, through the
// guard, bounded by ext.ConfirmWait and held to ext.MaxBehind), a
// record's opening window keeps every response out until the router has
// had its say (policyWindows), and the respond routes hand the router's
// confirmation to the engine with the response (confirmFor). With no
// router registered that confirms policies, none of it runs.

// routerConfirms says the registered decision router confirms policies:
// a provider that registered (never the free default) and implements
// ext.PolicyConfirmer.
func (s *Server) routerConfirms() bool {
	g, ok := s.exts.router.(guardRouter)
	if !ok {
		return false
	}
	_, confirms := g.p.(ext.PolicyConfirmer)
	return confirms && s.routerRegistered()
}

// routerRegistered says a decision router was registered: the free build
// has none.
func (s *Server) routerRegistered() bool {
	for _, e := range s.exts.all {
		if e.Kind == ext.KindDecisions {
			return true
		}
	}
	return false
}

// notConfirmed is the confirmation of a router that did not answer: a
// late, failing, panicking or invalid answer, or none for want of a slot.
var notConfirmed = ext.Confirmation{Why: "the router did not answer"}

// confirmTold asks the provider to confirm q, in one of its call slots,
// within ext.ConfirmWait, its answer checked (ext.Confirmation.Valid); any
// failure is notConfirmed. The slot is waited for inside that one wait
// (extAwait): a guard busy for a moment is never read as "not in force",
// only a router that is late or down. answered says the router answered
// at all: a slot found and a valid answer, both in time.
func (g guardRouter) confirmTold(ctx context.Context, q ext.ConfirmQuery) (ext.Confirmation, bool) {
	pc, ok := g.p.(ext.PolicyConfirmer)
	if !ok || !live(g.ready) {
		return notConfirmed, false
	}
	wait := ext.ConfirmWait
	if f := extWaiting.Load(); f != nil {
		wait = *f
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if wctx.Err() != nil {
		// Its wait has ended already (a list's): nothing goes in.
		return notConfirmed, false
	}
	slot, ok := g.s.extAwait(wctx, ext.KindDecisions, g.name)
	if !ok {
		if ctx.Err() == nil {
			g.s.extFailed(ext.KindDecisions, g.name, "busy", fmt.Sprintf("no call slot free within %s", wait))
		}
		return notConfirmed, false
	}
	own := q
	own.Decision = copyDecision(q.Decision)
	c, answered := extCallIn(wctx, g.s, slot, ext.KindDecisions, g.name, wait, notConfirmed, func(c ext.Confirmation) error {
		return c.Valid()
	}, func(ctx context.Context) (ext.Confirmation, error) {
		mine := own
		mine.Decision = copyDecision(own.Decision)
		c, err := pc.Confirm(ctx, mine)
		c.Groups = copyOrNil(c.Groups)
		return c, err
	})
	return c, answered
}

// policyClock is what the server holds a router to (ext.MaxBehind): per
// record, when the run of answers that ask for a retry (Behind, Routes, a
// Revision other than the record's) began, cleared by any other answer.
// A restart starts it again.
type policyClock struct {
	mu    sync.Mutex
	since map[string]time.Time
	// now is the clock; a test sets a fake one.
	now func() time.Time
}

func (c *policyClock) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// held says whether an answer asking for a retry, for the record id, is
// still within ext.MaxBehind of the first in its run; retry false clears
// the run.
func (c *policyClock) held(id string, retry bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !retry {
		delete(c.since, id)
		return true
	}
	now := c.clock()
	if c.since == nil {
		c.since = map[string]time.Time{}
	}
	first, ok := c.since[id]
	if !ok {
		c.since[id] = now
		return true
	}
	return now.Sub(first) <= ext.MaxBehind
}

// forget drops the record id's clock (its end).
func (c *policyClock) forget(id string) {
	c.mu.Lock()
	delete(c.since, id)
	c.mu.Unlock()
}

// policyCheck asks the registered router to confirm rec's policy at
// revision for subject ("" for whether it is in force): the guard's call,
// then the server's bound: an answer asking for a retry past ext.MaxBehind
// of the first in its run is taken as not in force. It is
// crew.PolicyCheck.
func (s *Server) policyCheck(ctx context.Context, rec ledger.DecisionRecord, revision, subject string) ext.Confirmation {
	c, _ := s.policyCheckTold(ctx, rec, revision, subject, time.Time{})
	return c
}

// policyCheckSince is policyCheck asking whether subject stayed eligible,
// without a break, since since (a counted response's time, at the
// application): a router that cannot say answers not eligible. It is
// crew.PolicySinceCheck. A zero since is refused as not confirmed: the
// question would be another.
func (s *Server) policyCheckSince(ctx context.Context, rec ledger.DecisionRecord, revision, subject string, since time.Time) ext.Confirmation {
	if since.IsZero() || subject == "" {
		return notConfirmed
	}
	c, _ := s.policyCheckTold(ctx, rec, revision, subject, since)
	return c
}

// policyCheckTold is policyCheck, since since when it is not zero, and
// says whether the router answered.
func (s *Server) policyCheckTold(ctx context.Context, rec ledger.DecisionRecord, revision, subject string, since time.Time) (ext.Confirmation, bool) {
	g, ok := s.exts.router.(guardRouter)
	if !ok {
		return notConfirmed, false
	}
	d := s.recordDecision(ctx, rec)
	c, answered := g.confirmTold(ctx, ext.ConfirmQuery{Decision: d, Revision: revision, Subject: subject, Since: since})
	// A revision other than the one asked about, in force or not, asks the
	// caller to try again (the response's judgement), so it too is held to
	// the bound.
	retry := c.Behind || c.Routes || c.Revision != "" && c.Revision != revision
	if !s.policyClock.held(rec.ID(), retry) {
		return ext.Confirmation{Why: "its router did not settle its policy within " + ext.MaxBehind.String()}, answered
	}
	return c, answered
}

// eligibleNow says whether a decide principal of subject may see and
// answer rec now (visibility following eligibility): a record under the
// server's own policy as its entries say; one under a router's policy only
// while a router that confirms policies is registered and confirms the
// policy in force at its revision with subject eligible in one of the
// groups that name it. answered says the router answered (false: no slot,
// late, failing, or no router). It fails closed: anything else is no.
func (s *Server) eligibleNow(ctx context.Context, rec ledger.DecisionRecord, subject string) (yes, answered bool) {
	if !ledger.RouterPolicy(rec.Policy) {
		return true, true
	}
	p, _, ok := crew.RouterPolicyOf(rec.Policy)
	if !ok || !s.runs.RoutesPolicies() || subject == "" {
		return false, true
	}
	c, answered := s.policyCheckTold(ctx, rec, p.Revision, subject, time.Time{})
	if !c.InForce || c.Behind || c.Revision != "" && c.Revision != p.Revision {
		return false, answered
	}
	for _, g := range p.Groups {
		for _, e := range g.Eligible {
			if e.Subject == subject && slices.Contains(c.Groups, g.Role) {
				return true, answered
			}
		}
	}
	return false, answered
}

// policyWindows are the records' opening windows, in memory: a record
// opened while the router routes policies is answered by no one until the
// router has had its say (its policy's transaction, an answer with none, a
// tell not made or failed), or twice ext.RouteWait from its commit. A
// record's entry stays, closed, until it is final, so that a later change
// opens none again; a restart forgets them all (a pass tells every open
// record again, and Confirm's Routes covers them).
type policyWindows struct {
	mu   sync.Mutex
	ends map[string]time.Time // zero once closed
	now  func() time.Time
}

func (w *policyWindows) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// open opens id's window, once.
func (w *policyWindows) open(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ends == nil {
		w.ends = map[string]time.Time{}
	}
	if _, seen := w.ends[id]; !seen {
		w.ends[id] = w.clock().Add(2 * ext.RouteWait)
	}
}

// close closes id's window, keeping its entry.
func (w *policyWindows) close(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, seen := w.ends[id]; seen {
		w.ends[id] = time.Time{}
	}
}

// forget drops id's entry (its end).
func (w *policyWindows) forget(id string) {
	w.mu.Lock()
	delete(w.ends, id)
	w.mu.Unlock()
}

// in says id's window is open now.
func (w *policyWindows) in(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	end, ok := w.ends[id]
	return ok && !end.IsZero() && w.clock().Before(end)
}

// opensWindow says the change row, just committed, opens a record's
// window: open at its first rev under the server's own policy with a
// spare (a record opened while the router routes policies), never a
// comparison's selection.
func opensWindow(row ledger.DecisionRow) bool {
	return row.State == ledger.DecisionOpen && row.Rev == 1 && row.Origin != ledger.OriginCompare &&
		ledger.PolicySpare(row.Policy) && !ledger.RouterPolicy(row.Policy)
}

// errInWindow is a response in a record's opening window: unkept, to try
// again.
func errInWindow(id string) error {
	return &crew.DecisionError{Code: crew.CodeStalePolicy, Words: id + "'s policy is being written: try again"}
}

// confirmFor is what a respond route hands the engine beside a response
// to decision did by the authority auth: nil with no router that confirms
// policies; else the router's confirmation of the record's policy for the
// responder (its subject, "" for the workbench token), asked before the
// response's transaction. A record in its opening window is refused
// stale_policy, nothing kept; a record still under the server's own
// policy that the router will route now (Routes) is told again.
func (s *Server) confirmFor(ctx context.Context, did string, auth crew.Authority) (*ext.Confirmation, error) {
	if !s.runs.RoutesPolicies() || s.ledger == nil {
		return nil, nil
	}
	n, ok := ledger.ParseDecisionID(did)
	if !ok {
		return nil, nil
	}
	if s.router.windows.in(did) {
		return nil, errInWindow(did)
	}
	rec, found, err := s.ledger.Decision(ctx, n)
	if err != nil || !found || rec.Origin == ledger.OriginCompare {
		return nil, err
	}
	revision := "local/1"
	subject := ""
	if auth.Via == crew.ViaIdentity {
		subject = auth.Subject
	}
	if p, _, routed := crew.RouterPolicyOf(rec.Policy); routed {
		revision = p.Revision
	} else {
		subject = ""
	}
	c := s.policyCheck(ctx, rec, revision, subject)
	if c.Routes && !ledger.RouterPolicy(rec.Policy) {
		s.router.retell(did)
	}
	if c.Revision == "" {
		// An answer that leaves its revision out is the revision asked
		// about: the response's transaction compares it with the record's,
		// so a re-route between the ask and the commit is stale_policy.
		c.Revision = revision
	}
	return &c, nil
}

// policyNowFor is the operator's read's word on a record under a router's
// policy now, {inForce, why}, from one Confirm asked with subject ("" for
// none); nil for any other record. A record that carries a router's policy
// on a server whose router confirms no policies now (none registered, or
// one that does not confirm) is not in force, in the words the respond
// route's judgement gives. The reply says too whether subject is eligible
// now (Eligible), as the respond route would judge its plain answer, so an
// operator's identity the policy names answers as eligible on its card.
func (s *Server) policyNowFor(ctx context.Context, did, subject string) *policyNowReply {
	if s.ledger == nil {
		return nil
	}
	n, ok := ledger.ParseDecisionID(did)
	if !ok {
		return nil
	}
	rec, found, err := s.ledger.Decision(ctx, n)
	if err != nil || !found {
		return nil
	}
	p, _, routed := crew.RouterPolicyOf(rec.Policy)
	if !routed {
		return nil
	}
	if !s.runs.RoutesPolicies() {
		return &policyNowReply{Why: "no router confirms its policy now"}
	}
	c := s.policyCheck(ctx, rec, p.Revision, subject)
	inForce := c.InForce && !c.Behind && (c.Revision == "" || c.Revision == p.Revision)
	reply := &policyNowReply{InForce: inForce, Why: c.Why}
	if inForce && subject != "" {
		for _, g := range p.Groups {
			for _, e := range g.Eligible {
				if e.Subject == subject && slices.Contains(c.Groups, g.Role) {
					reply.Eligible = true
				}
			}
		}
	}
	return reply
}

// routeInForce is whether a route of decision did is refused because its
// router's policy is in force: one Confirm with no subject, in force being
// the confirmation's InForce alone. A router refreshing its own state
// (Behind), or answering of another revision, still stands behind its
// policy, held to ext.MaxBehind as every such answer is (policyCheck: past
// it, not in force). revision is the policy's revision it asked about, ""
// for a record under the server's own policy (never in force).
func (s *Server) routeInForce(ctx context.Context, did string) (revision string, inForce bool) {
	if s.ledger == nil {
		return "", false
	}
	n, ok := ledger.ParseDecisionID(did)
	if !ok {
		return "", false
	}
	rec, found, err := s.ledger.Decision(ctx, n)
	if err != nil || !found {
		return "", false
	}
	p, _, routed := crew.RouterPolicyOf(rec.Policy)
	if !routed {
		return "", false
	}
	if !s.runs.RoutesPolicies() {
		return p.Revision, false
	}
	return p.Revision, s.policyCheck(ctx, rec, p.Revision, "").InForce
}

// quorumFor is the operator's read's count of a record under a router's
// policy (proto.DecisionQuorum): its places in all, those the counted
// approvals that stand fill now, and each group's count; nil for any other
// record, and for one the ledger cannot read now.
func (s *Server) quorumFor(ctx context.Context, did string) *proto.DecisionQuorum {
	if s.ledger == nil {
		return nil
	}
	n, ok := ledger.ParseDecisionID(did)
	if !ok {
		return nil
	}
	rec, found, err := s.ledger.Decision(ctx, n)
	if err != nil || !found {
		return nil
	}
	return crew.QuorumOf(rec, s.runs.Boot(), s.runs.CountedPending)
}

// policyNowReply is GET /api/decisions/{did}'s policyNow: in force now and
// the router's why; eligible when the reader, an identity's operator, is
// among those the policy names and the router confirms now.
type policyNowReply struct {
	InForce  bool   `json:"inForce"`
	Why      string `json:"why,omitempty"`
	Eligible bool   `json:"eligible,omitempty"`
}

// recordDecision is rec as a router is told it, with what the record says
// of itself (recordOf).
func (s *Server) recordDecision(ctx context.Context, rec ledger.DecisionRecord) ext.Decision {
	d := recordDecision(rec)
	d.Record = s.recordOf(ctx, rec)
	return d
}

// recordOf is what rec says of itself to a router: its type and origin,
// its gate and asking task, the run's crew, whom its request names, its
// expertise, its subject by kind and key, and the routes written before
// the router's own (at most ext.MaxRecommended).
func (s *Server) recordOf(ctx context.Context, rec ledger.DecisionRecord) *ext.DecisionRecord {
	out := &ext.DecisionRecord{Type: rec.Type, Origin: rec.Origin, Gate: cutBytes(rec.Gate, ext.MaxRecordKey), Task: cutBytes(rec.TaskID, ext.MaxRecordKey)}
	if s.ledger != nil {
		if run, ok, err := s.ledger.Run(ctx, rec.CurrentRun); err == nil && ok && validCrewID(run.CrewID) {
			out.Crew = run.CrewID
		}
	}
	var body struct {
		For       *crew.DecisionFor     `json:"for"`
		Expertise []string              `json:"expertise"`
		Subject   *crew.DecisionSubject `json:"subject"`
	}
	_ = json.Unmarshal([]byte(rec.Request), &body)
	if f := body.For; f != nil && f.Label != "" && f.Label != crew.GrantDecideLabel {
		out.For = &ext.RecordFor{Label: f.Label, Person: f.Person, Responsibility: f.Responsibility}
	}
	if len(body.Expertise) <= ext.MaxRecordExpertise {
		out.Expertise = body.Expertise
	}
	if body.Subject != nil {
		out.Subject = &ext.DecisionSubject{Kind: body.Subject.Kind, Key: SubjectKey(*body.Subject)}
	}
	out.Recommended = recommended(rec)
	if out.Valid() != nil {
		// A field past a router's bound is left out rather than the
		// record's say: the router still routes by the rest.
		out.For, out.Expertise, out.Recommended = nil, nil, nil
		if out.Valid() != nil {
			return &ext.DecisionRecord{Type: rec.Type, Origin: rec.Origin}
		}
	}
	return out
}

// validCrewID says id is a crew id as ext.DecisionRecord takes one.
func validCrewID(id string) bool {
	return (ext.DecisionRecord{Type: "approval", Origin: "plan", Crew: id}).Valid() == nil
}

// SubjectKey is a decision subject's key: the first ext.SubjectKeyLen hex
// digits of the SHA-256 of its canonical JSON (its encoding as the record
// keeps it), so a router tells two subjects apart, and finds one subject
// asked about twice, without holding a path.
func SubjectKey(sub crew.DecisionSubject) string {
	b, _ := json.Marshal(sub)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:ext.SubjectKeyLen]
}

// recommended are the destinations of rec's routed rows written by the
// operator or by Brakeman (its routes before the router's), at most
// ext.MaxRecommended: recommendations, never who may answer.
func recommended(rec ledger.DecisionRecord) []ext.Destination {
	var out []ext.Destination
	for _, h := range rec.History {
		if h.Kind != ledger.HistoryRouted || h.By == "local/1" || len(h.By) > 7 && h.By[:7] == "router:" {
			continue
		}
		var dt struct {
			To  []ext.Destination `json:"to"`
			For *crew.DecisionFor `json:"for"`
		}
		if json.Unmarshal([]byte(h.Detail), &dt) != nil {
			continue
		}
		if dt.For != nil && dt.For.Person != "" {
			dt.To = append(dt.To, ext.Destination{Kind: "person", Name: dt.For.Person})
		}
		for _, d := range dt.To {
			if len(out) < ext.MaxRecommended && (ext.Route{To: []ext.Destination{d}}).Valid(ext.Decision{}) == nil {
				out = append(out, d)
			}
		}
	}
	return out
}

// routePolicyOf writes the router's policy rt.Policy into the record d at
// rev (the policy's transaction, crew.Engine.RoutePolicy), with the
// route's destinations and rule as its one routed row: unrouted (told
// again) when the record moved meanwhile, nothing when it carries a
// router's policy already or is closed.
func (r *decisionRouter) routePolicyOf(d ext.Decision, rev int, rt ext.Route, unrouted func()) error {
	to := make([]crew.Destination, 0, len(rt.To))
	for _, t := range rt.To {
		to = append(to, crew.Destination{Kind: t.Kind, Name: t.Name})
	}
	_, err := r.s.runs.RoutePolicy(r.s.extCtx, d.Request, r.s.exts.router.Name(), rev, *rt.Policy, to, rt.Rule)
	var de *crew.DecisionError
	switch {
	case err == nil:
	case errors.As(err, &de) && de.Code == crew.CodeStaleDecision:
		unrouted()
	case errors.As(err, &de):
		r.s.log.Info("routed decision's policy not written", "decision", d.Request, "words", de.Words)
	default:
		r.s.log.Warn("routed decision's policy not written", "decision", d.Request, "err", err)
		unrouted()
	}
	return err
}

// retell tells the router of the open record id again: it leaves the set
// of records told, and is read again.
func (r *decisionRouter) retell(id string) {
	r.mu.Lock()
	delete(r.opened, id)
	r.mu.Unlock()
	r.mark(id)
}

// The registered decision router's reads and writes of the records'
// policies (ext.Host.OpenDecisions, Outcomes, AckOutcomes, Reroute): the
// router's Host alone serves them, and only where the server keeps a
// ledger; every other Host answers ext.ErrUnsupported.

// openDecisionsPage and outcomesPage are the pages' sizes.
const (
	openDecisionsPage = 64
	outcomesPage      = 32
)

// routerLedger is the ledger the router's Host reads, or why it reads none:
// ext.ErrUnsupported for any Host but the registered router's, on a
// switchyard and with no ledger.
func (h *extHost) routerLedger() (*ledger.Ledger, error) {
	if err := h.gone(); err != nil {
		return nil, err
	}
	if h.desk || !h.s.routerRegistered() || h.name != h.s.exts.router.Name() {
		return nil, fmt.Errorf("%w: only the registered decision router reads the decisions' policies", ext.ErrUnsupported)
	}
	if h.s.cfg.Switchyard.Enabled || h.s.ledger == nil {
		return nil, fmt.Errorf("%w: this server keeps no ledger of decisions", ext.ErrUnsupported)
	}
	return h.s.ledger, nil
}

// OpenDecisions pages every open record after the decision after ("" for
// the first), in number order, each with its policy (nil under the
// server's own), when the router's policy was first written, the last
// route's why, its rev and its counted approvals that stand. No count cuts it: a router reads every page.
func (h *extHost) OpenDecisions(ctx context.Context, after string) (ext.OpenDecisionsPage, error) {
	ld, err := h.routerLedger()
	if err != nil {
		return ext.OpenDecisionsPage{}, err
	}
	var from int64
	if after != "" {
		n, ok := ledger.ParseDecisionID(after)
		if !ok || len(after) > ext.MaxDecisionRef {
			return ext.OpenDecisionsPage{}, refused("%q is not a decision's id", cutBytes(after, ext.MaxDecisionRef))
		}
		from = n
	}
	bctx, end := h.bound(ctx)
	defer end()
	recs, more, err := ld.OpenRecordsAfter(bctx, from, openDecisionsPage)
	if err != nil {
		return ext.OpenDecisionsPage{}, err
	}
	page := ext.OpenDecisionsPage{Decisions: []ext.OpenDecision{}}
	for _, rec := range recs {
		od := ext.OpenDecision{Decision: h.s.recordDecision(bctx, rec), Rev: rec.Rev, Why: lastRouteWhy(rec)}
		if p, _, ok := crew.RouterPolicyOf(rec.Policy); ok {
			od.Policy = &p
			od.RoutedAt = routedAtOf(rec.Policy)
			od.Counted = crew.CountedOf(rec, h.s.runs.Boot(), h.s.runs.CountedPending)
		}
		page.Decisions = append(page.Decisions, od)
	}
	if more && len(recs) > 0 {
		page.Next = recs[len(recs)-1].ID()
	}
	return page, nil
}

// lastRouteWhy is the why of rec's last change of route: the kind of a
// re-route (Host.Reroute's row, with its op id: assigned, delegated,
// escalated or rerouted), or routed for any other route, a router's first
// policy included (written as a router's change, with no op id, when the
// record was routed before it); "" for none.
func lastRouteWhy(rec ledger.DecisionRecord) string {
	for i := len(rec.History) - 1; i >= 0; i-- {
		switch h := rec.History[i]; h.Kind {
		case ledger.HistoryAssigned, ledger.HistoryDelegated, ledger.HistoryEscalated, ledger.HistoryRerouted:
			if h.OpID != "" {
				return h.Kind
			}
			return ledger.HistoryRouted
		case ledger.HistoryRouted:
			return ledger.HistoryRouted
		}
	}
	return ""
}

// routedAtOf is when a router's policy was first written into raw.
func routedAtOf(raw string) time.Time {
	var p struct {
		RoutedAt time.Time `json:"routedAt"`
	}
	_ = json.Unmarshal([]byte(raw), &p)
	return p.RoutedAt
}

// Outcomes reads the outcome outbox after after, 32 at a time, in seq
// order: each row as the router reads it, its record, policy and outcome
// as the row keeps them.
func (h *extHost) Outcomes(ctx context.Context, after uint64) (ext.OutcomesPage, error) {
	ld, err := h.routerLedger()
	if err != nil {
		return ext.OutcomesPage{}, err
	}
	bctx, end := h.bound(ctx)
	defer end()
	rows, err := ld.Outcomes(bctx, int64(min(after, 1<<62)), outcomesPage)
	if err != nil {
		return ext.OutcomesPage{}, err
	}
	page := ext.OutcomesPage{Outcomes: []ext.OutboxEntry{}}
	for _, r := range rows {
		var o ledger.Outcome
		if err := json.Unmarshal([]byte(r.Outcome), &o); err != nil {
			return ext.OutcomesPage{}, err
		}
		page.Outcomes = append(page.Outcomes, outboxEntry(uint64(r.Seq), o))
	}
	if len(rows) == outcomesPage {
		page.Next = uint64(rows[len(rows)-1].Seq)
	}
	return page, nil
}

// outboxEntry is an outbox row as a router reads it.
func outboxEntry(seq uint64, o ledger.Outcome) ext.OutboxEntry {
	d := ext.Decision{ID: o.Decision, Request: o.Decision, Kind: decisionKind, Message: cutBytes(o.Record.Question, maxRoutedMessage), Since: o.Record.CreatedAt,
		Session: ext.SessionRef{ID: o.Record.Session, Run: o.Record.Run, Task: o.Record.Task, Attempt: o.Record.Attempt},
		Record:  &ext.DecisionRecord{Type: o.Record.Type, Origin: o.Record.Origin, Gate: cutBytes(o.Record.Gate, ext.MaxRecordKey), Task: cutBytes(o.Record.Task, ext.MaxRecordKey)}}
	for i, label := range o.Record.Options {
		d.Options = append(d.Options, ext.DecisionOption{Label: label, Input: fmt.Sprint(i + 1)})
	}
	e := ext.OutboxEntry{Seq: seq, Decision: d, Outcome: outcomeOf(o), ClosedAt: o.ClosedAt}
	if p, _, ok := crew.RouterPolicyOf(string(o.Policy)); ok {
		e.Policy = &p
		e.RoutedAt = routedAtOf(string(o.Policy))
	}
	return e
}

// outcomeOf is a kept outcome as ext says it.
func outcomeOf(o ledger.Outcome) ext.DecisionOutcome {
	return ext.DecisionOutcome{State: o.State, By: o.By, Via: o.Via, SupersededBy: o.SupersededBy, Why: oneLineWords(o.Why, ext.MaxDecisionWhyLine)}
}

// oneLineWords is s on one line, cut where a character ends to n bytes.
func oneLineWords(s string, n int) string {
	return cutBytes(strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " "), n)
}

// AckOutcomes deletes the outbox's rows up to through: the router's
// acknowledgement, the only way a row leaves.
func (h *extHost) AckOutcomes(ctx context.Context, through uint64) error {
	ld, err := h.routerLedger()
	if err != nil {
		return err
	}
	bctx, end := h.bound(ctx)
	defer end()
	_, err = ld.AckOutcomes(bctx, int64(min(through, 1<<62)))
	return err
}

// Reroute changes a record's policy (crew.Engine.Reroute): only for the
// registered router that confirms policies, a request valid
// (ext.RerouteRequest.Valid), and a policy valid for the record and of the
// shape this server takes (guardRouter.policyShape).
func (h *extHost) Reroute(ctx context.Context, r ext.RerouteRequest) (ext.RerouteResult, error) {
	ld, err := h.routerLedger()
	if err != nil {
		return ext.RerouteResult{}, err
	}
	if !h.s.runs.RoutesPolicies() {
		return ext.RerouteResult{}, fmt.Errorf("%w: the registered router confirms no policies", ext.ErrUnsupported)
	}
	if err := r.Valid(); err != nil {
		return ext.RerouteResult{}, refused("%v", err)
	}
	g, _ := h.s.exts.router.(guardRouter)
	if err := g.policyShape(r.Route.Policy); err != nil {
		return ext.RerouteResult{}, refused("%v", err)
	}
	n, _ := ledger.ParseDecisionID(r.Decision)
	bctx, end := h.bound(ctx)
	defer end()
	rec, found, err := ld.Decision(bctx, n)
	switch {
	case err != nil:
		return ext.RerouteResult{}, err
	case !found:
		return ext.RerouteResult{}, fmt.Errorf("%w: no decision %s", ext.ErrNotFound, r.Decision)
	}
	if err := r.Route.Policy.Valid(*h.s.recordOf(bctx, rec)); err != nil {
		return ext.RerouteResult{}, refused("%v", err)
	}
	res, err := h.s.runs.Reroute(bctx, r.Decision, h.s.exts.router.Name(), *r.Route.Policy, r.Why, r.OpID, r.ExpectedRev)
	if err == nil && !res.Unchanged {
		h.s.router.windows.close(r.Decision)
	}
	return res, err
}

// listAsks is how many of a list's asks of the router run at once: a
// quarter of the guard's slots (extInFlight), so a list never holds them
// all and the respond routes' own asks find one.
const listAsks = extInFlight / 4

// eligibleRows is rows, in order, without those under a router's policy the
// router does not confirm subject eligible in now: asked after the
// ledger's read, listAsks at a time, all within one ext.ConfirmWait, each
// ask waiting for a slot inside it. A row the router did not answer in
// that wait (late, down, or no slot free in time) is left out too, and
// unasked says one was.
func (s *Server) eligibleRows(ctx context.Context, rows []ledger.DecisionRow, subject string) (kept []ledger.DecisionRow, unasked bool) {
	cctx, cancel := context.WithTimeout(ctx, ext.ConfirmWait)
	defer cancel()
	yes := make([]bool, len(rows))
	answered := make([]bool, len(rows))
	asks := make(chan int)
	var wg sync.WaitGroup
	for range listAsks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range asks {
				yes[i], answered[i] = s.eligibleNow(cctx, ledger.DecisionRecord{DecisionRow: rows[i]}, subject)
			}
		}()
	}
	for i, row := range rows {
		if !ledger.RouterPolicy(row.Policy) {
			yes[i], answered[i] = true, true
			continue
		}
		asks <- i
	}
	close(asks)
	wg.Wait()
	for i, row := range rows {
		if yes[i] {
			kept = append(kept, row)
		}
		unasked = unasked || !answered[i]
	}
	return kept, unasked
}

// policyRoutes are a router's policy's destinations as the inbox shows
// them: each eligible person (person), each gap's role (role), at most
// ext.MaxDestinations, and the first group's label and rule, at most 200
// bytes.
func policyRoutes(p ext.RoutePolicy) ([]inboxDestination, string) {
	var to []inboxDestination
	for _, g := range p.Groups {
		if len(g.Eligible) == 0 && len(to) < ext.MaxDestinations {
			to = append(to, inboxDestination{Kind: "role", Name: cutRunes(g.Label, ext.MaxDestinationName-1)})
		}
		for _, e := range g.Eligible {
			if len(to) < ext.MaxDestinations {
				to = append(to, inboxDestination{Kind: "person", Name: cutRunes(e.Name, ext.MaxDestinationName-1)})
			}
		}
	}
	rule := ""
	if len(p.Groups) > 0 {
		rule = fitText("the "+p.Groups[0].Label+", rule "+p.Groups[0].Rule, 200)
	}
	return to, rule
}
