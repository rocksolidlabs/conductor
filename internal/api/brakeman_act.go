package api

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync/atomic"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Brakeman's authorized actions (docs/plans/brakeman.md, PR-12; Program
// item BK2). The person grants operations from a closed list
// (brakeman.Grantable, Permissions.Act, written only by the admin's
// configuration route); Brakeman has no act route of its own. Each granted
// operation is sent to that operation's own route, which takes the
// principal brakeman (a turn's session token with X-Conductor-Session
// naming its session, as its reads do) when the turn is granted it, checks
// the act in a set order, writes it pending into the request, runs the
// operation's one function under the actor brakeman with the derived
// idempotency key, answers the route's own answer and copies its receipt
// into the request. A read gives no grant (s.reader takes no write) and a
// grant gives no read (the target's visibility and scope are checked
// apart). No operation of the list approves a plan revision, answers a
// decision, changes a gate, accepts a task, edits the configuration or
// closes a ticket: DS2 refuses an approval by any actor but the operator.

// brakemanActor is the actor of the operations Brakeman runs: what their
// stores keep their receipts under, apart from the operator's (admin) and
// a provider's principal's (user:<subject>).
const brakemanActor = "brakeman"

// brakemanBy is the display name an act's operation records as by.
const brakemanBy = "Brakeman"

// actRoute is a granted operation's own route: its operation and the
// bound of its body (the route's own).
type actRoute struct {
	op    string
	bound int64
}

// actRoutes is the routes each granted operation is sent to, by pattern.
// request_repair (RV1's request changes on a candidate) and
// route_decision (DR1's routing of a decision record) have no route in
// this build: neither can be granted (actServed).
var actRoutes = map[string]actRoute{
	"POST /api/dispatch/tickets/{key}/launch": {op: brakeman.ActLaunchApprovedPlan, bound: maxTicketLaunchBody},
	"POST /api/dispatch/tickets/{key}/plans":  {op: brakeman.ActPrepareFollowup, bound: maxTicketBody},
}

// actServed answers nil for a granted operation whose route this build
// serves, else why not (brakeman.Facts.ServesAct).
func actServed(op string) error {
	switch op {
	case brakeman.ActLaunchApprovedPlan, brakeman.ActPrepareFollowup:
		return nil
	case brakeman.ActRequestRepair:
		return errors.New("RV1's request for changes on a candidate is not served by this build")
	case brakeman.ActRouteDecision:
		return errors.New("DR1's decision records, whose routing it is, are not served by this build")
	}
	return fmt.Errorf("no operation %s", op)
}

// actOp is an act's derived idempotency key: its request and the turn's
// op id, 32 characters, never another request's.
func actOp(request, opID string) string {
	return deriveOp("act", request, opID)
}

// actSeam, when a test sets it, runs at each stage of an act ("pending"
// once it is written pending, before its operation; "committed" once the
// operation answered, before its outcome is copied); an error it answers
// stops the act there, as a server that died would.
var actSeam atomic.Pointer[func(stage, request, opID string) error]

func atActSeam(stage, request, opID string) error {
	if f := actSeam.Load(); f != nil {
		return (*f)(stage, request, opID)
	}
	return nil
}

// actable is next for every caller but a Brakeman turn on a granted
// operation's own route (actRoutes), which turnAct answers: any other
// pattern is next as it is.
func (s *Server) actable(pattern string, next http.HandlerFunc) http.HandlerFunc {
	ar, ok := actRoutes[pattern]
	if !ok {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// A turn's token is looked at first, as a read's is, so it is
		// never asked of the token chain as a token of its own.
		tp, aerr := s.turnPrincipalOf(r)
		switch {
		case aerr != nil:
			writeAPIError(w, aerr)
		case tp != nil:
			s.turnAct(w, r, tp, ar)
		default:
			next(w, r)
		}
	}
}

// actBody is the fields of a granted operation's body an act reads
// before the operation's route decodes it whole.
type actBody struct {
	OpID        string `json:"opId"`
	ExpectedRev int64  `json:"expectedRev"`
	Plan        string `json:"plan"`
	BasedOn     string `json:"basedOn"`
	Label       string `json:"label"`
}

// turnAct answers a Brakeman turn's act on the operation ar: refused in
// this order (docs/plans/brakeman.md, "BK2 (PR-12)") unless its opId
// names an act of the request already, which is run again under its
// derived key so the operation's own store answers its receipt whatever
// the target's state now (409 op_conflict for other content under it,
// 409 act_not_run for one the server stopped before it ran): the turn's
// session the running request's (turnPrincipalOf); the grant, the
// snapshot the turn began under and the configuration now (403
// not_granted); the target's visibility (403 not_visible) and scope (403
// outside_scope); the target's state as the operation requires (404
// not_found, 409 stale_revision, 409 stale_target); at most
// brakeman.MaxActs a request (409 acts_spent). Then it is written pending,
// the operation run, and its receipt copied into the request; the answer
// is the operation's route's own. The whole act holds the request's make
// mutex, so no two acts of a request interleave.
func (s *Server) turnAct(w http.ResponseWriter, r *http.Request, tp *turnPrincipal, ar actRoute) {
	d := s.brakeman
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ar.bound))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", ar.bound>>10))
		return
	}
	body, args, aerr := actArgs(ar, key, raw)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	mu := d.makeLock(tp.req.ID)
	mu.Lock()
	defer mu.Unlock()
	req, err := d.st.Get(tp.req.ID)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	tp.req = req
	target := brakeman.Ref{Kind: brakeman.RefTicket, Ticket: key}
	op := actOp(req.ID, body.OpID)
	if i := slices.IndexFunc(req.Acts, func(a brakeman.Act) bool { return a.OpID == body.OpID }); i >= 0 {
		a := req.Acts[i]
		switch {
		case a.Operation != ar.op || a.Target != target || a.Args != args:
			writeError(w, http.StatusConflict, "op_conflict", "the opId names another act of the request: mint a new one")
			return
		case a.State == brakeman.ActNotRun:
			writeError(w, http.StatusConflict, "act_not_run", "the server stopped before the act ran; it is not run again")
			return
		}
	} else {
		if aerr := s.admitAct(ctx, ds, tp, ar, key, body); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		if len(req.Acts) >= brakeman.MaxActs {
			writeError(w, http.StatusConflict, "acts_spent", fmt.Sprintf("a request takes at most %d acts", brakeman.MaxActs))
			return
		}
		act := brakeman.Act{OpID: body.OpID, Operation: ar.op, Target: target, Op: op, Args: args, State: brakeman.ActPending, At: d.now().UTC()}
		if _, err := d.update(req.ID, func(q *brakeman.Request) error {
			if q.State != brakeman.StateStarting && q.State != brakeman.StateRunning {
				return brakeman.ErrNotTurn
			}
			q.Acts = append(q.Acts, act)
			return nil
		}); err != nil {
			s.brakemanError(w, err)
			return
		}
	}
	if err := atActSeam("pending", req.ID, body.OpID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", err.Error())
		return
	}
	rec := newReplyRecorder()
	s.runAct(ctx, rec, r, d, ar, key, op, body, raw)
	if err := atActSeam("committed", req.ID, body.OpID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", err.Error())
		return
	}
	s.copyActOutcome(d, req.ID, body.OpID, rec)
	writeRecorded(w, rec)
}

// writeRecorded writes a recorded answer to w.
func writeRecorded(w http.ResponseWriter, rec *replyRecorder) {
	h := w.Header()
	for k, v := range rec.header {
		h[k] = v
	}
	w.WriteHeader(cmp.Or(rec.status, http.StatusOK))
	_, _ = w.Write(rec.body.Bytes())
}

// actArgs reads an act's body: its op id and the fields its checks need,
// the body decoded as its route decodes it (unknown fields refused), and
// the hex SHA-256 of its arguments, the ticket's key and every field but
// the op id and the name, in a set order.
func actArgs(ar actRoute, key string, raw []byte) (actBody, string, *apiError) {
	var body actBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	var strict any = &ticketLaunchBody{}
	if ar.op == brakeman.ActPrepareFollowup {
		strict = &planProposeBody{}
	}
	if err := store.DecodeStrict(raw, strict); err != nil {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if !dispatch.OpIDPattern.MatchString(body.OpID) {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", "opId is 1 to 32 letters, digits, '.', '_', '~' or '-'")
	}
	if body.ExpectedRev < 0 {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", "expectedRev is the ticket's revision, or 0 for none")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	delete(fields, "opId")
	delete(fields, "byName")
	fields["key"], _ = json.Marshal(key)
	// A map's keys are encoded in order: the same arguments, the same sum.
	b, err := json.Marshal(fields)
	if err != nil {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	sum := sha256.Sum256(b)
	return body, hex.EncodeToString(sum[:]), nil
}

// admitAct checks a new act of the turn tp on the ticket key, in order:
// the grant, the target's visibility and scope, and its state as the
// operation requires. nil admits it.
func (s *Server) admitAct(ctx context.Context, ds *dispatch.Store, tp *turnPrincipal, ar actRoute, key string, body actBody) *apiError {
	// The grant: the snapshot the turn began under and the configuration
	// now, so a grant taken away holds at once and one given waits for the
	// next turn.
	if !slices.Contains(tp.req.Config.Permissions.Act, ar.op) || !slices.Contains(tp.cfg.Permissions.Act, ar.op) {
		return newAPIError(http.StatusForbidden, "not_granted", "Brakeman is not granted "+ar.op+": only the person grants it, in Settings")
	}
	if err := actServed(ar.op); err != nil {
		return newAPIError(http.StatusForbidden, "not_granted", err.Error())
	}
	// What the turn may see, checked apart from the grant: a grant gives
	// no read.
	o := s.newObserver(tp)
	if !o.grant.sees(brakeman.ClassDispatch) {
		return newAPIError(http.StatusForbidden, "not_visible", "Brakeman may not see "+brakeman.ClassDispatch)
	}
	switch ok, err := o.ticketAdmitted(ctx, key); {
	case err != nil:
		return newAPIError(http.StatusInternalServerError, "store_failed", "the ticket's runs could not be read")
	case !ok:
		return newAPIError(http.StatusForbidden, "outside_scope", "outside the request's scope")
	}
	return s.actTargetState(ctx, ds, ar, key, body)
}

// actTargetState checks the act's target is in the state its operation
// requires: the ticket at the revision the act read (expectedRev, when
// given), and the plan revision it names the ticket's own, approved, and
// for a launch an approval that still holds. nil when it is.
func (s *Server) actTargetState(ctx context.Context, ds *dispatch.Store, ar actRoute, key string, body actBody) *apiError {
	tk, err := ds.Ticket(ctx, key)
	switch {
	case errors.Is(err, dispatch.ErrNotFound):
		return newAPIError(http.StatusNotFound, "not_found", "no ticket "+key)
	case err != nil:
		return newAPIError(http.StatusInternalServerError, "store_failed", "the ticket could not be read")
	}
	if body.ExpectedRev > 0 && tk.Rev != body.ExpectedRev {
		return newAPIError(http.StatusConflict, dispatch.RefusalStaleRevision, fmt.Sprintf("the revision expected is %d; %s's is %d: read it again", body.ExpectedRev, key, tk.Rev))
	}
	id, what := body.Plan, "the revision to launch"
	if ar.op == brakeman.ActPrepareFollowup {
		id, what = body.BasedOn, "the approved revision a follow-up is based on (basedOn)"
	}
	if !dispatch.ValidID(id) {
		return newAPIError(http.StatusBadRequest, "invalid_request", what+" is a plan revision's id, 16 lower-case hex characters")
	}
	plans, err := ds.Plans(ctx, key)
	if err != nil {
		return newAPIError(http.StatusInternalServerError, "store_failed", "the ticket's plan revisions could not be read")
	}
	i := slices.IndexFunc(plans, func(p dispatch.PlanSummary) bool { return p.ID == id })
	if i < 0 {
		return newAPIError(http.StatusNotFound, "not_found", fmt.Sprintf("no plan revision %s of %s", id, key))
	}
	p := plans[i]
	switch {
	case p.State != dispatch.PlanApproved:
		return newAPIError(http.StatusConflict, "stale_target", fmt.Sprintf("revision %d of %s is %s: Brakeman acts only on the revision the operator approved", p.N, key, p.State))
	case ar.op == brakeman.ActLaunchApprovedPlan && p.Stale != nil:
		return newAPIError(http.StatusConflict, "stale_target", fmt.Sprintf("the approval of revision %d of %s no longer holds: the operator approves it again or revises it", p.N, key))
	}
	return nil
}

// runAct runs the act's operation through its one function under the
// actor brakeman, its op id op, answering to rec as the operation's route
// does: a launch through the desk's ext.Host (LaunchApproved, DS2's
// launch), a follow-up proposal through DS2's propose route.
func (s *Server) runAct(ctx context.Context, rec *replyRecorder, r *http.Request, d *brakemanDesk, ar actRoute, key, op string, body actBody, raw []byte) {
	switch ar.op {
	case brakeman.ActLaunchApprovedPlan:
		al := ext.ApprovedLaunch{Ticket: key, Plan: body.Plan, ExpectedRev: body.ExpectedRev, OpID: op, Label: body.Label,
			By: ext.Principal{Subject: brakemanActor, Name: brakemanBy}}
		ref, err := d.host().LaunchApproved(withReplySink(ctx, rec), al)
		if rec.status != 0 {
			return
		}
		// A Host that answered without the route's own words (one of a
		// test's): its answer, in the route's form.
		if err != nil {
			writeError(rec, http.StatusConflict, "launch_failed", err.Error())
			return
		}
		writeDispatch(rec, http.StatusCreated, map[string]any{"run": map[string]any{"id": ref.ID, "crewId": ref.Crew}})
	case brakeman.ActPrepareFollowup:
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		fields["opId"], _ = json.Marshal(op)
		fields["byName"], _ = json.Marshal(brakemanBy)
		b, _ := json.Marshal(fields)
		in, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL.Path, bytes.NewReader(b))
		if err != nil {
			writeError(rec, http.StatusInternalServerError, "store_failed", err.Error())
			return
		}
		in.Header.Set("Content-Type", "application/json")
		in.SetPathValue("key", key)
		in = withPrincipal(in, principal{admin: true, as: brakemanActor})
		s.handleDispatchProposePlan(rec, in)
	}
}

// copyActOutcome copies what the act's operation answered into its
// entry: done with the receipt's id, or refused with its words, for an
// answer that carries the operation's receipt; refused with the route's
// words for a refusal before the store kept one (4xx); nothing for a
// failure of the server (5xx without a receipt), which leaves the entry
// pending for a retry under the same opId or the recovery.
func (s *Server) copyActOutcome(d *brakemanDesk, request, opID string, rec *replyRecorder) {
	state, receipt, why := actOutcome(rec)
	if state == "" {
		return
	}
	if _, err := d.update(request, func(q *brakeman.Request) error {
		for i := range q.Acts {
			if a := &q.Acts[i]; a.OpID == opID && a.State != brakeman.ActNotRun {
				a.State, a.Receipt, a.Why, a.At = state, receipt, why, d.now().UTC()
			}
		}
		return nil
	}); err != nil {
		s.log.Warn("a Brakeman act's outcome could not be copied: it stays pending for the recovery", "request", request, "opId", opID, "err", err)
	}
}

// actOutcome is an act's state, receipt and words from its operation's
// recorded answer; "" for one that says nothing of whether it ran.
func actOutcome(rec *replyRecorder) (state, receipt, why string) {
	var reply launchReply
	_ = json.Unmarshal(rec.body.Bytes(), &reply)
	switch {
	case reply.Receipt != nil:
		return receiptOutcome(*reply.Receipt)
	case rec.status >= 200 && rec.status < 300:
		if reply.Run != nil {
			return brakeman.ActDone, reply.Run.ID, ""
		}
		return brakeman.ActDone, "", ""
	case rec.status >= 400 && rec.status < 500:
		why := http.StatusText(rec.status)
		if reply.Error != nil {
			why = cmpOr(reply.Error.Message, reply.Error.Code, why)
		}
		return brakeman.ActRefused, "", cutWhy(why)
	}
	return "", "", ""
}

// receiptOutcome is an act's state, receipt and words from its
// operation's receipt.
func receiptOutcome(rc dispatch.Receipt) (state, receipt, why string) {
	if rc.Status == dispatch.StatusDone {
		return brakeman.ActDone, rc.ID, ""
	}
	return brakeman.ActRefused, rc.ID, cutWhy(cmpOr(rc.Error, rc.Refusal))
}

// maxActWhy bounds an act's words, so its entry stays within
// brakeman.MaxActBytes.
const maxActWhy = 200

// cutWhy is why cut to maxActWhy bytes on a character's boundary.
func cutWhy(why string) string {
	for len(why) > maxActWhy {
		_, n := utf8.DecodeLastRuneInString(why)
		why = why[:len(why)-n]
	}
	return why
}

// completeAct completes an act the last owner left pending, from the
// operation's own store by what the entry holds: Dispatch's receipt for
// (brakeman, op) copied, done or refused, with its words; none, the
// operation never committed, and the entry is marked not_run, never run
// by the recovery (its turn has ended, and the grant was that turn's).
func (s *Server) completeAct(ctx context.Context, d *brakemanDesk, request string, a brakeman.Act) error {
	ds := s.dispatch
	if ds == nil {
		return &errRefStore{what: "Dispatch's store"}
	}
	rc, found, err := ds.Receipt(ctx, brakemanActor, a.Op)
	if err != nil {
		return err
	}
	state, receipt, why := brakeman.ActNotRun, "", "the server stopped before it ran"
	if found {
		state, receipt, why = receiptOutcome(rc)
	}
	_, err = d.st.Update(request, func(q *brakeman.Request) error {
		for i := range q.Acts {
			if p := &q.Acts[i]; p.OpID == a.OpID && p.State == brakeman.ActPending {
				p.State, p.Receipt, p.Why, p.At = state, receipt, why, d.now().UTC()
			}
		}
		return nil
	})
	return err
}
