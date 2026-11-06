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
	"maps"
	"net/http"
	"slices"
	"strconv"
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

// actReceiptRef is the ref a brief cites an act's receipt by: the
// operation's receipt, kept by Dispatch under the actor brakeman and the
// act's derived key op (brakeman.RefForms names the form).
func actReceiptRef(op string) string {
	return brakeman.Ref{Kind: brakeman.RefOp, Actor: brakemanActor, Op: op}.String()
}

// actSeam, when a test sets it, runs at each stage of an act ("waiting"
// once its body is read, before it waits for the request's make mutex;
// "pending" once it is written pending, or found committed, before its
// operation or the answer of its kept refusal; "committed"
// once the operation answered, before its outcome is copied); an error it
// answers stops the act there, as a server that died would.
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

// actBody is an act's body as its operation's route decodes it, the
// fields its checks and its operation read: the one decoding, so what
// the act is checked by is what its operation runs.
type actBody struct {
	OpID        string
	ExpectedRev int64
	// Plan and Label are a launch's.
	Plan, Label string
	// Propose is a follow-up's request (its crew and basedOn among it).
	Propose dispatch.PlanRequest
}

// turnAct answers a Brakeman turn's act on the operation ar
// (docs/plans/brakeman.md, "BK2 (PR-12)"). The grant (the snapshot the
// turn began under and the configuration now; 403 not_granted) is checked
// first of all, before the body is read. An opId that names an act of the
// request already answers 409 op_conflict for other content and 409
// act_not_run for one the server stopped before it ran; one whose
// operation committed (Dispatch's Committed, its receipt kept or pruned)
// is run again under its derived key, so the operation's own store
// answers it whatever the target's state now, but for a refusal the store
// kept, answered from the receipt as Committed read it, nothing run. Any other act, new or
// retried, is admitted in admission's order (pendAct): refused in this
// order, the grant again, the target's visibility (403 not_visible) and
// scope, the ticket's and what the operation makes (403 outside_scope),
// the target's state as the operation requires (404 not_found, 409
// stale_revision, 409 stale_target), at most brakeman.MaxActs a request
// (409 acts_spent); then written pending. The operation runs, its receipt
// is copied into the request, and the answer is the operation's route's
// own. The whole act holds the request's make mutex, so no two acts of a
// request interleave, and is admitted as a make is (admitMake), so
// release waits for it before the owner lock goes; its operation begins
// only while the desk is open (beginAct, 503 shutting_down otherwise), and
// the shutdown waits for it before the server's runs are stopped.
func (s *Server) turnAct(w http.ResponseWriter, r *http.Request, tp *turnPrincipal, ar actRoute) {
	d := s.brakeman
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	// A turn not granted the operation is refused first, whatever it
	// sends: its reads give it no way past this.
	if aerr := actGrant(tp, ar.op); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	// Admitted while the desk is open, and waited for by release before
	// the owner lock goes: an act never commits its operation beside the
	// next owner's recovery of its entry.
	if !d.admitMake() {
		s.brakemanError(w, errDeskClosing)
		return
	}
	defer d.madeDone()
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
	if err := atActSeam("waiting", tp.req.ID, body.OpID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", err.Error())
		return
	}
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
	admit := true
	// kept is a refusal Dispatch kept for the act's operation, answered as
	// it was read here.
	var kept *dispatch.Receipt
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
		// An act whose operation committed is answered by its store,
		// whatever the target's state now. One that did not (refused
		// before the store, or a failure of the server) runs anew if it
		// runs at all: it is held to the checks again and written pending
		// again before it runs. A refusal the store kept is answered from
		// the receipt read here, never sent to the operation again: a
		// refusal writes no change row, so once the receipt is pruned the
		// operation would run anew, unchecked and not pending.
		switch rc, found, err := ds.Committed(ctx, brakemanActor, op); {
		case err != nil:
			s.dispatchError(w, "act", err)
			return
		case found && rc.Status != dispatch.StatusDone:
			rc.Repeat = true
			kept, admit = &rc, false
		case found:
			admit = false
		}
	}
	if admit {
		act := brakeman.Act{OpID: body.OpID, Operation: ar.op, Target: target, Op: op, Args: args, State: brakeman.ActPending, At: d.now().UTC()}
		var ae *apiError
		switch err := s.pendAct(ctx, ds, tp, ar, key, body, act); {
		case err == nil:
		case errors.As(err, &ae):
			writeAPIError(w, ae)
			return
		default:
			s.brakemanError(w, err)
			return
		}
	}
	if err := atActSeam("pending", req.ID, body.OpID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", err.Error())
		return
	}
	rec := newReplyRecorder()
	if kept != nil {
		// The route's own answer to its kept refusal, nothing run.
		s.writeOp(rec, "act", *kept, nil, false, "", func(dispatch.Receipt) (any, error) { return nil, nil })
	} else {
		// The operation begins only while the desk is open, and the
		// shutdown waits for it before the server's runs are stopped: a
		// run an act launches is one those stops find. Refused once the
		// desk closes, the entry stays pending, and the next start marks
		// it not_run.
		if !d.beginAct() {
			s.brakemanError(w, errDeskClosing)
			return
		}
		defer d.actDone()
		s.runAct(ctx, rec, ds, tp, ar, key, op, body)
	}
	if err := atActSeam("committed", req.ID, body.OpID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", err.Error())
		return
	}
	s.copyActOutcome(d, req.ID, body.OpID, rec)
	s.answerAct(ctx, w, tp, key, op, rec)
}

// answerAct answers the turn tp's act on the ticket key with what its
// operation's route answered (rec), as the turn may read it as it is
// answered: the receipt and the error always, the act's own answer, so a
// retry is answered by its receipt whatever the grant now; launch and
// plan, Dispatch's records of the ticket, only while the configuration
// now and the turn's snapshot show dispatch, the ticket is in the scope
// and the turn has reads left, each noted in its read log at its state
// and rev and the answer counted as one read; never the run, which the
// turn reads through its grant (GET /api/runs/{run}). dispatch is noted
// as a class the turn's answers used (the receipt and the words are
// Dispatch's) for its brief's footprint. Beside a receipt, receiptRef is
// the ref a brief cites it by (op:brakeman/<op>, op the act's derived
// key, under which Dispatch keeps the receipt): the receipt's own id is
// what the operation made (a launch's, a revision's), which no ref of an
// act's receipt names. All of it under the reads' mutex, before the
// answer is written, so a brief kept after the answer carries what it
// delivered.
func (s *Server) answerAct(ctx context.Context, w http.ResponseWriter, tp *turnPrincipal, key, op string, rec *replyRecorder) {
	d := s.brakeman
	d.readMu.Lock()
	defer d.readMu.Unlock()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.body.Bytes(), &m); err != nil {
		// Not an object (no route answers so): nothing of it is given.
		d.usedClass(tp.req.ID, brakeman.ClassDispatch)
		writeError(w, cmp.Or(rec.status, http.StatusInternalServerError), "store_failed", "the operation's answer could not be read")
		return
	}
	out := map[string]json.RawMessage{}
	for _, k := range []string{"receipt", "error"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	if v, ok := out["receipt"]; ok && !isJSONNull(v) {
		ref, _ := json.Marshal(actReceiptRef(op))
		out["receiptRef"] = ref
	}
	if o := s.actReader(ctx, tp, key); o != nil {
		given := map[string]json.RawMessage{}
		if v, ok := m["launch"]; ok && !isJSONNull(v) {
			var l struct {
				ID    string `json:"id"`
				State string `json:"state"`
				Rev   int64  `json:"rev"`
			}
			if json.Unmarshal(v, &l) == nil && l.ID != "" {
				o.noteRef("launch:"+l.ID, brakeman.ClassDispatch, l.State, strconv.FormatInt(l.Rev, 10))
				given["launch"] = v
			}
		}
		if v, ok := m["plan"]; ok && !isJSONNull(v) {
			var p struct {
				N     int             `json:"n"`
				State string          `json:"state"`
				Stale json.RawMessage `json:"stale"`
				Rev   int64           `json:"rev"`
			}
			if json.Unmarshal(v, &p) == nil && p.N > 0 {
				stale := len(p.Stale) > 0 && !isJSONNull(p.Stale)
				o.noteRef(fmt.Sprintf("plan:%s/%d", key, p.N), brakeman.ClassDispatch, planRefState(p.State, stale), strconv.FormatInt(p.Rev, 10))
				given["plan"] = v
			}
		}
		if len(given) > 0 {
			if _, err := d.st.NoteAnswer(tp.req.ID, o.reads...); err != nil {
				// Not noted, not given: the receipt answers alone.
				s.log.Warn("a Brakeman act's answer could not be noted as read: its records are left out", "request", tp.req.ID, "err", err)
			} else {
				maps.Copy(out, given)
			}
		}
	}
	d.usedClass(tp.req.ID, brakeman.ClassDispatch)
	b, err := marshalNoEscape(out)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_failed", "the operation's answer could not be written")
		return
	}
	h := w.Header()
	for k, v := range rec.header {
		h[k] = v
	}
	h.Del("Content-Length")
	w.WriteHeader(cmp.Or(rec.status, http.StatusOK))
	_, _ = w.Write(append(b, '\n'))
}

// actReader is the observer an act's answer is read by: the turn tp's
// request and the configuration as they are now, while the turn may read
// Dispatch's records of the ticket key (its request a turn's, reads left,
// dispatch shown, the ticket in the scope); nil otherwise.
func (s *Server) actReader(ctx context.Context, tp *turnPrincipal, key string) *observer {
	d := s.brakeman
	req, err := d.st.Get(tp.req.ID)
	if err != nil || (req.State != brakeman.StateStarting && req.State != brakeman.StateRunning) || req.Reads >= req.Config.Budget.ReadsPerTurn {
		return nil
	}
	cfg, err := d.st.Config()
	if err != nil {
		return nil
	}
	now := &turnPrincipal{session: tp.session, req: req, cfg: cfg}
	o := s.newObserver(now)
	if !o.grant.sees(brakeman.ClassDispatch) {
		return nil
	}
	if ok, err := o.ticketAdmitted(ctx, key); err != nil || !ok {
		return nil
	}
	return o
}

// isJSONNull reports whether raw is JSON's null.
func isJSONNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// errActsSpent refuses an act past brakeman.MaxActs a request.
var errActsSpent = newAPIError(http.StatusConflict, "acts_spent", fmt.Sprintf("a request takes at most %d acts", brakeman.MaxActs))

// pendAct admits the act of the turn tp and writes it pending, new or
// over the entry of its opId, in admission's order (the desk's admit,
// which a configuration's save and quiesce take): the configuration read
// again, so a grant taken away or a visibility narrowed while the act
// waited holds; the checks of admitAct under it; then, in one Update, the
// request still the turn's (starting or running; brakeman.ErrNotTurn
// otherwise) and at most brakeman.MaxActs acts. An act admitted here is
// before any save after it. An *apiError is the act's refusal; another
// error is the desk's.
func (s *Server) pendAct(ctx context.Context, ds *dispatch.Store, tp *turnPrincipal, ar actRoute, key string, body actBody, act brakeman.Act) error {
	d := s.brakeman
	d.admit.Lock()
	defer d.admit.Unlock()
	if d.closing.Load() {
		return errDeskClosing
	}
	cfg, err := d.st.Config()
	if err != nil {
		return err
	}
	tp.cfg = cfg
	if aerr := s.admitAct(ctx, ds, tp, ar, key, body); aerr != nil {
		return aerr
	}
	_, err = d.update(tp.req.ID, func(q *brakeman.Request) error {
		if q.State != brakeman.StateStarting && q.State != brakeman.StateRunning {
			return brakeman.ErrNotTurn
		}
		if i := slices.IndexFunc(q.Acts, func(a brakeman.Act) bool { return a.OpID == act.OpID }); i >= 0 {
			q.Acts[i] = act
			return nil
		}
		if len(q.Acts) >= brakeman.MaxActs {
			return errActsSpent
		}
		q.Acts = append(q.Acts, act)
		return nil
	})
	return err
}

// actArgs reads an act's body as its route decodes it (unknown fields
// refused), once: what the checks read and what the operation is given
// are the same decoded fields, whatever the spelling or the order of the
// keys sent. args is the hex SHA-256 of the arguments as decoded, the
// ticket's key and every field but the op id and the name, encoded in a
// set order.
func actArgs(ar actRoute, key string, raw []byte) (actBody, string, *apiError) {
	var body actBody
	var canon any
	switch ar.op {
	case brakeman.ActPrepareFollowup:
		var b planProposeBody
		if err := store.DecodeStrict(raw, &b); err != nil {
			return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
		}
		body = actBody{OpID: b.OpID, ExpectedRev: b.ExpectedRev, Propose: b.PlanRequest}
		b.OpID, b.ByName = "", ""
		canon = b
	default:
		var b ticketLaunchBody
		if err := store.DecodeStrict(raw, &b); err != nil {
			return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
		}
		body = actBody{OpID: b.OpID, ExpectedRev: b.ExpectedRev, Plan: b.Plan, Label: b.Label}
		b.OpID, b.ByName = "", ""
		canon = b
	}
	if !dispatch.OpIDPattern.MatchString(body.OpID) {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", "opId is 1 to 32 letters, digits, '.', '_', '~' or '-'")
	}
	if body.ExpectedRev < 0 {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", "expectedRev is the ticket's revision, or 0 for none")
	}
	b, err := json.Marshal(struct {
		Key  string `json:"key"`
		Args any    `json:"args"`
	}{key, canon})
	if err != nil {
		return actBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	sum := sha256.Sum256(b)
	return body, hex.EncodeToString(sum[:]), nil
}

// actGrant refuses an operation the turn tp is not granted: the grant is
// the snapshot the turn began under and the configuration now, so a grant
// taken away holds at once and one given waits for the next turn. nil
// when it is granted.
func actGrant(tp *turnPrincipal, op string) *apiError {
	if !slices.Contains(tp.req.Config.Permissions.Act, op) || !slices.Contains(tp.cfg.Permissions.Act, op) {
		return newAPIError(http.StatusForbidden, "not_granted", "Brakeman is not granted "+op+": only the person grants it, in Settings")
	}
	if err := actServed(op); err != nil {
		return newAPIError(http.StatusForbidden, "not_granted", err.Error())
	}
	return nil
}

// admitAct checks an act of the turn tp on the ticket key, in order: the
// grant, the target's visibility and scope, what the operation makes (a
// run's or a revision's crew and repository) held to the scope too, and
// the target's state as the operation requires. nil admits it. A refusal
// past the visibility check is in Dispatch's words (a revision's state, a
// ticket's rev, a revision's crew), so dispatch is noted as a class the
// turn's answers used, under the reads' mutex, as answerAct notes it.
func (s *Server) admitAct(ctx context.Context, ds *dispatch.Store, tp *turnPrincipal, ar actRoute, key string, body actBody) *apiError {
	if aerr := actGrant(tp, ar.op); aerr != nil {
		return aerr
	}
	// What the turn may see, checked apart from the grant: a grant gives
	// no read.
	o := s.newObserver(tp)
	if !seesAny(actClasses, o.grant.sees) {
		return newAPIError(http.StatusForbidden, "not_visible", "Brakeman may not see "+brakeman.ClassDispatch)
	}
	aerr := s.admitVisibleAct(ctx, ds, o, ar, key, body)
	if aerr != nil {
		d := s.brakeman
		d.readMu.Lock()
		d.usedClass(tp.req.ID, brakeman.ClassDispatch)
		d.readMu.Unlock()
	}
	return aerr
}

// admitVisibleAct is admitAct's checks once Dispatch is visible: the
// ticket in the scope, what the operation makes, the target's state.
func (s *Server) admitVisibleAct(ctx context.Context, ds *dispatch.Store, o *observer, ar actRoute, key string, body actBody) *apiError {
	switch ok, err := o.ticketAdmitted(ctx, key); {
	case err != nil:
		return newAPIError(http.StatusInternalServerError, "store_failed", "the ticket's runs could not be read")
	case !ok:
		return newAPIError(http.StatusForbidden, "outside_scope", "outside the request's scope")
	}
	if ar.op == brakeman.ActPrepareFollowup {
		// The follow-up's crew and the folder it would plan in, as the
		// proposal resolves them; the proposal holds them to the scope
		// again once its folder is resolved (planGuard), before git runs.
		if aerr := s.followupAdmitted(ctx, ds, o, key, body.Propose.Crew); aerr != nil {
			return aerr
		}
	}
	return s.actTargetState(ctx, ds, o, ar, key, body)
}

// followupAdmitted holds a follow-up's crew, and the folder a proposal of
// it on the ticket key plans in, to the turn's scope. A crew or a project
// the proposal cannot read is left to the proposal, which refuses it in
// its own words.
func (s *Server) followupAdmitted(ctx context.Context, ds *dispatch.Store, o *observer, key, crewID string) *apiError {
	if !o.grant.crewAdmitted(crewID) {
		return newAPIError(http.StatusForbidden, "outside_scope", "the crew "+cut(crewID, 64)+" is outside the request's scope")
	}
	if s.crews == nil || !o.grant.byRepo() {
		return nil
	}
	c, err := s.crews.Get(crewID)
	if err != nil {
		return nil
	}
	project, _, _ := dispatch.ParseKey(key)
	pv, err := ds.Project(ctx, project)
	if err != nil {
		return nil
	}
	cwd, err := s.resolveCwd(s.planCwd(pv, c))
	if err != nil {
		return nil
	}
	return actMakesAdmitted(ctx, o, crewID, cwd)
}

// actMakesAdmitted holds what an act makes, of the crew crewID in the
// folder cwd, to the turn's scope: the crew to each scope's Crews and the
// folder's repository root to its Repos, when they name any (its project
// is the ticket's, held by ticketAdmitted). nil admits it.
func actMakesAdmitted(ctx context.Context, o *observer, crewID, cwd string) *apiError {
	if !o.grant.crewAdmitted(crewID) {
		return newAPIError(http.StatusForbidden, "outside_scope", "the crew "+cut(crewID, 64)+" is outside the request's scope")
	}
	if !o.grant.byRepo() {
		return nil
	}
	top, err := o.rr.top(ctx, cwd)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			return ae
		}
		return newAPIError(http.StatusInternalServerError, "git_failed", "the repository of the crew's working directory could not be read")
	}
	if !o.grant.repoAdmitted(top) {
		return newAPIError(http.StatusForbidden, "outside_scope", "the crew's repository is outside the request's scope")
	}
	return nil
}

// crewAdmitted reports whether every scope of the grant that names crews
// names crewID.
func (g refGrant) crewAdmitted(crewID string) bool {
	for _, s := range g.scopes {
		if len(s.Crews) > 0 && !slices.Contains(s.Crews, crewID) {
			return false
		}
	}
	return true
}

// repoAdmitted reports whether every scope of the grant that names
// repositories names the root top ("" for none, in none).
func (g refGrant) repoAdmitted(top string) bool {
	for _, s := range g.scopes {
		if len(s.Repos) > 0 && !slices.Contains(s.Repos, top) {
			return false
		}
	}
	return true
}

// actTargetState checks the act's target is in the state its operation
// requires: the ticket at the revision the act read (expectedRev, when
// given), and the plan revision it names the ticket's own, approved, and
// for a launch an approval that still holds and a crew and repository in
// the scope. nil when it is.
func (s *Server) actTargetState(ctx context.Context, ds *dispatch.Store, o *observer, ar actRoute, key string, body actBody) *apiError {
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
		id, what = body.Propose.BasedOn, "the approved revision a follow-up is based on (basedOn)"
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
	case ar.op == brakeman.ActLaunchApprovedPlan:
		// The run the launch makes: the revision's crew, in the folder
		// the revision fixed on it.
		v, err := ds.Plan(ctx, id)
		if err != nil {
			return newAPIError(http.StatusInternalServerError, "store_failed", "the plan revision could not be read")
		}
		var pb struct {
			Crew struct {
				Cwd string `json:"cwd"`
			} `json:"crew"`
		}
		if err := json.Unmarshal(v.Body, &pb); err != nil {
			return newAPIError(http.StatusInternalServerError, "store_failed", "the plan revision's crew could not be read")
		}
		return actMakesAdmitted(ctx, o, p.CrewID, pb.Crew.Cwd)
	}
	return nil
}

// runAct runs the act's operation through its one function under the
// actor brakeman, its op id op, answering to rec as the operation's route
// does: a launch through the desk's ext.Host (LaunchApproved, DS2's
// launch), a follow-up proposal through DS2's (ticketPropose), held to
// the turn's scope once its folder is resolved and to an approved base in
// the store's transaction.
func (s *Server) runAct(ctx context.Context, rec *replyRecorder, ds *dispatch.Store, tp *turnPrincipal, ar actRoute, key, op string, body actBody) {
	switch ar.op {
	case brakeman.ActLaunchApprovedPlan:
		al := ext.ApprovedLaunch{Ticket: key, Plan: body.Plan, ExpectedRev: body.ExpectedRev, OpID: op, Label: body.Label,
			By: ext.Principal{Subject: brakemanActor, Name: brakemanBy}}
		ref, err := s.brakeman.host().LaunchApproved(withReplySink(ctx, rec), al)
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
		o := s.newObserver(tp)
		guard := &planGuard{admit: func(ctx context.Context, crewID, cwd string) *apiError {
			return actMakesAdmitted(ctx, o, crewID, cwd)
		}}
		s.ticketPropose(ctx, rec, ds, dispatch.Op{Actor: brakemanActor, ID: op, ExpectedRev: body.ExpectedRev, By: brakemanBy}, key, body.Propose, guard)
	}
}

// copyActOutcome copies what the act's operation answered into its
// entry: done with the receipt's id, or refused with its words, for an
// answer that carries the operation's receipt; refused with the route's
// words for a refusal before the store kept one (4xx); nothing for a
// failure of the server (5xx without a receipt), which leaves the entry
// pending for a retry under the same opId or the recovery. The outcome
// copied, the request's brakeman event is sent: the copy changes no state
// of the request's, so update sends none, and a page holding the request
// (its brief kept, or its slot given up, while the act ran) would show the
// act pending for as long as it stays open.
func (s *Server) copyActOutcome(d *brakemanDesk, request, opID string, rec *replyRecorder) {
	state, receipt, why := actOutcome(rec)
	if state == "" {
		return
	}
	if _, err := d.update(request, func(q *brakeman.Request) error {
		for i := range q.Acts {
			if a := &q.Acts[i]; a.OpID == opID && a.State != brakeman.ActNotRun {
				a.State, a.Receipt, a.Why, a.At = state, receipt, why, d.now().UTC()
				*a = a.Fit()
			}
		}
		return nil
	}); err != nil {
		s.log.Warn("a Brakeman act's outcome could not be copied: it stays pending for the recovery", "request", request, "opId", opID, "err", err)
		return
	}
	d.publish(request)
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

// maxActWhy bounds an act's words as they are read; Act.Fit then cuts
// them to what its entry has room for within brakeman.MaxActBytes, as
// encoded.
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
// (brakeman, op) copied, done or refused, with its words, or, the receipt
// pruned, the changes log's record that it committed (Committed); none,
// the operation never committed, and the entry is marked not_run, never
// run by the recovery (its turn has ended, and the grant was that turn's).
func (s *Server) completeAct(ctx context.Context, d *brakemanDesk, request string, a brakeman.Act) error {
	ds := s.dispatch
	if ds == nil {
		return &errRefStore{what: "Dispatch's store"}
	}
	rc, found, err := ds.Committed(ctx, brakemanActor, a.Op)
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
				*p = p.Fit()
			}
		}
		return nil
	})
	return err
}
