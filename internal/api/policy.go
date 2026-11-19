package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
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

// confirm asks the provider to confirm q, in one of its call slots, within
// ext.ConfirmWait, its answer checked (ext.Confirmation.Valid); any
// failure is notConfirmed.
func (g guardRouter) confirm(ctx context.Context, q ext.ConfirmQuery) ext.Confirmation {
	pc, ok := g.p.(ext.PolicyConfirmer)
	if !ok || !live(g.ready) {
		return notConfirmed
	}
	own := q
	own.Decision = copyDecision(q.Decision)
	return extCall(ctx, g.s, ext.KindDecisions, g.name, ext.ConfirmWait, notConfirmed, func(c ext.Confirmation) error {
		return c.Valid()
	}, func(ctx context.Context) (ext.Confirmation, error) {
		mine := own
		mine.Decision = copyDecision(own.Decision)
		c, err := pc.Confirm(ctx, mine)
		c.Groups = copyOrNil(c.Groups)
		return c, err
	})
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
	g, ok := s.exts.router.(guardRouter)
	if !ok {
		return notConfirmed
	}
	d := s.recordDecision(ctx, rec)
	c := g.confirm(ctx, ext.ConfirmQuery{Decision: d, Revision: revision, Subject: subject})
	retry := c.Behind || c.Routes || c.InForce && c.Revision != "" && c.Revision != revision
	if !s.policyClock.held(rec.ID(), retry) {
		return ext.Confirmation{Why: "its router did not settle its policy within " + ext.MaxBehind.String()}
	}
	return c
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
	return &c, nil
}

// policyNow is the operator's read's {inForce, why} of a record under a
// router's policy, from one Confirm with no subject; nil for any other
// record, and with no router that confirms policies.
func (s *Server) policyNow(ctx context.Context, did string) *policyNowReply {
	if !s.runs.RoutesPolicies() || s.ledger == nil {
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
	c := s.policyCheck(ctx, rec, p.Revision, "")
	inForce := c.InForce && !c.Behind && (c.Revision == "" || c.Revision == p.Revision)
	return &policyNowReply{InForce: inForce, Why: c.Why}
}

// policyNowReply is GET /api/decisions/{did}'s policyNow.
type policyNowReply struct {
	InForce bool   `json:"inForce"`
	Why     string `json:"why,omitempty"`
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
