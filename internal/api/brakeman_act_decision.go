package api

import (
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

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Brakeman's route_decision (docs/plans/brakeman.md, "PR-12 and PR-13"
// and PR-18; Program item BK2): DR1's routing of a decision record, sent
// to DR1's own route (POST /api/decisions/{did}/route), to the operator
// or to a person of the decision's run's crew, by name ({kind: person})
// or by the responsibility they hold ({kind: role}, HM1's labels). It is
// an act as the others are (turnAct's order: the grant, then the act's
// entry, the checks, pending, the operation, its receipt copied), with
// DR1's record in Dispatch's place: what the turn must see is the runs
// (the decision is its run's, and the turn reads it, its rev and its ref
// decision:<dr-n>, in its run's read), its scope the run's, the state the
// decision's (open, at the rev read, a label the run's crew holds, a
// person routed to only while it is for no one), the operation
// crew.Engine.RouteDecision under the actor brakeman with the derived op
// id, held to the run admitted (crew.WithAdmittedRun), which DR1's
// history keeps, so a repeat is answered by DR1's own receipt and a
// recovery reads the entry's outcome back by the decision and its op
// (ledger.HistoryByOp). The act's target is the decision's ref. The turn's
// routes count in their own bound in DR1's history, never in the
// operator's operations (ledger.MaxDecisionTurnOps). It never answers the
// decision: DR1 takes an answer from the operator alone, and no grant
// reaches its respond route.

// decisionRoutePattern is DR1's routing route, route_decision's own.
const decisionRoutePattern = "POST /api/decisions/{did}/route"

// routeActClasses are what a route_decision act needs its turn to see: a
// decision is its run's.
var routeActClasses = []string{brakeman.ClassRuns}

// routeActBody is a route_decision act's body as DR1's route decodes it.
type routeActBody struct {
	OpID        string
	ExpectedRev int
	To          crew.Destination
	Note        string
}

// routeActArgs reads a route's body as DR1's route decodes it (unknown
// fields refused), once: args is the hex SHA-256 of the decision's id and
// every field but the op id, encoded in a set order.
func routeActArgs(did string, raw []byte) (routeActBody, string, *apiError) {
	var b decisionRouteBody
	if err := store.DecodeStrict(raw, &b); err != nil {
		return routeActBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	body := routeActBody{OpID: b.OpID, ExpectedRev: b.ExpectedRev, To: b.To, Note: b.Note}
	if !crew.CommandIDPattern.MatchString(body.OpID) {
		return routeActBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", "opId is 1 to 32 letters, digits, '.', '_', '~' or '-'")
	}
	if body.ExpectedRev < 1 {
		return routeActBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", "expectedRev is the decision's rev you read, from 1")
	}
	b.OpID = ""
	enc, err := json.Marshal(struct {
		Decision string            `json:"decision"`
		Args     decisionRouteBody `json:"args"`
	}{did, b})
	if err != nil {
		return routeActBody{}, "", newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	sum := sha256.Sum256(enc)
	return body, hex.EncodeToString(sum[:]), nil
}

// turnRoute answers a Brakeman turn's route_decision act on the decision
// the path names, in turnAct's order: the grant first of all, before the
// body is read; an opId that names an act of the request already answers
// 409 op_conflict for other content and 409 act_not_run for one the
// server stopped before it ran; one whose route DR1's history holds
// (HistoryByOp) runs again under its derived key, which DR1 answers with
// that route's receipt whatever the decision's state now. Any other act
// is admitted in admission's order (pendRoute) and written pending; then
// DR1's route runs, its receipt is copied into the request, and the
// answer is the receipt and the error DR1's route gave.
func (s *Server) turnRoute(w http.ResponseWriter, r *http.Request, tp *turnPrincipal, ar actRoute) {
	d := s.brakeman
	if aerr := actGrant(tp, ar.op); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if s.ledger == nil {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "this server keeps no decision records")
		return
	}
	if !d.admitMake() {
		s.brakemanError(w, errDeskClosing)
		return
	}
	defer d.madeDone()
	did := r.PathValue("did")
	n, ok := ledger.ParseDecisionID(did)
	if !ok {
		writeError(w, http.StatusNotFound, crew.CodeNotFound, "no decision "+cut(did, 32))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ar.bound))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", ar.bound>>10))
		return
	}
	body, args, aerr := routeActArgs(did, raw)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	ctx, cancel := runContext(r)
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
	op := actOp(req.ID, body.OpID)
	target := brakeman.Ref{Kind: brakeman.RefDecision, Decision: ledger.DecisionID(n)}
	admit := true
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
		// A route DR1 kept is answered by DR1, whatever the decision's
		// state now; DR1 keeps no refusal, so one refused runs anew, held
		// to the checks again and written pending again.
		switch _, found, err := s.ledger.HistoryByOp(ctx, n, brakemanActor, op); {
		case err != nil:
			s.decisionError(w, "route", did, err, nil)
			return
		case found:
			admit = false
		}
	}
	if admit {
		var ae *apiError
		run, err := s.pendRoute(ctx, tp, n, did, target, op, args, body)
		switch {
		case err == nil:
			// The route is held to the run its scope was judged by: a
			// continuation since moves the decision, and the route is
			// refused, never made on the run it moved to.
			ctx = crew.WithAdmittedRun(ctx, run)
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
	if !d.beginAct() {
		s.brakemanError(w, errDeskClosing)
		return
	}
	rec := newReplyRecorder()
	rc, err := s.runs.RouteDecision(ctx, did, op, body.ExpectedRev, []crew.Destination{body.To}, body.Note, brakemanActor)
	// The receipt as DR1's route answers it, without the decision record,
	// which the turn's answer never carries: nothing read after the
	// commit can lose a route that committed.
	if err != nil {
		s.decisionError(rec, "route", did, err, nil)
	} else {
		writeJSON(rec, http.StatusOK, map[string]any{"receipt": rc})
	}
	d.actDone()
	if err := atActSeam("committed", req.ID, body.OpID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", err.Error())
		return
	}
	s.copyRouteOutcome(d, req.ID, body.OpID, rec)
	s.answerRoute(w, tp, rec)
}

// pendRoute admits the route act of the turn tp on decision n (did, its
// ref target) and writes it pending, as pendAct does a ticket's act:
// under the desk's admit, with the configuration read again, the checks
// of admitRoute under it, then in one Update the request still the turn's
// and at most brakeman.MaxActs acts. It answers the run the decision was
// admitted on, the entry's run.
func (s *Server) pendRoute(ctx context.Context, tp *turnPrincipal, n int64, did string, target brakeman.Ref, op, args string, body routeActBody) (string, error) {
	d := s.brakeman
	d.admit.Lock()
	defer d.admit.Unlock()
	if d.closing.Load() {
		return "", errDeskClosing
	}
	cfg, err := d.st.Config()
	if err != nil {
		return "", err
	}
	tp.cfg = cfg
	run, aerr := s.admitRoute(ctx, tp, n, did, body)
	if aerr != nil {
		return "", aerr
	}
	act := brakeman.Act{OpID: body.OpID, Operation: brakeman.ActRouteDecision, Target: target, Run: run,
		Op: op, Args: args, State: brakeman.ActPending, At: d.now().UTC()}
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
	return run, err
}

// admitRoute checks a route act of the turn tp on decision n, in order:
// the grant; what the turn sees (the runs: a grant gives no read); the
// decision there, with its run in the scope (one outside it as one not
// there); its state as DR1's routing requires of Brakeman (open, at the
// rev the act read) and, for a route to a person, a decision for no one
// yet (the plan's or a person's for is not Brakeman's to change) and the
// label held in the run's crew as launched (crew.Holder). It answers the
// decision's run, which the act's entry names as its run, or the
// refusal. A refusal past the visibility check is in the runs' words, so
// runs is noted as a class the turn's answers used.
func (s *Server) admitRoute(ctx context.Context, tp *turnPrincipal, n int64, did string, body routeActBody) (string, *apiError) {
	if aerr := actGrant(tp, brakeman.ActRouteDecision); aerr != nil {
		return "", aerr
	}
	o := s.newObserver(tp)
	if !seesAny(routeActClasses, o.grant.sees) {
		return "", newAPIError(http.StatusForbidden, "not_visible", "Brakeman may not see "+brakeman.ClassRuns)
	}
	run, aerr := s.admitVisibleRoute(ctx, o, n, did, body)
	if aerr != nil {
		d := s.brakeman
		d.readMu.Lock()
		d.usedClass(tp.req.ID, brakeman.ClassRuns)
		d.readMu.Unlock()
	}
	return run, aerr
}

// admitVisibleRoute is admitRoute's checks once the runs are visible.
func (s *Server) admitVisibleRoute(ctx context.Context, o *observer, n int64, did string, body routeActBody) (string, *apiError) {
	missing := newAPIError(http.StatusNotFound, crew.CodeNotFound, "no decision "+did)
	rec, found, err := s.ledger.Decision(ctx, n)
	switch {
	case err != nil:
		return "", newAPIError(http.StatusInternalServerError, "store_failed", "the decision could not be read")
	case !found:
		return "", missing
	}
	rr, ok, err := o.runAdmitted(ctx, rec.CurrentRun)
	switch {
	case err != nil:
		return "", newAPIError(http.StatusInternalServerError, "store_failed", "the decision's run could not be read")
	case !ok:
		// Not in the scope: as a decision that is not there, so the
		// answer says nothing of one the turn may not see.
		return "", missing
	}
	switch {
	case body.To.Kind == crew.DestGrant:
		return "", newAPIError(http.StatusBadRequest, crew.CodeInvalid, "Brakeman routes a decision to the operator or a person of its run's crew, never to a grant")
	case crew.GivenToGrant(rec.Request):
		// Neither given nor taken back by a turn: the operator's alone.
		return "", newAPIError(http.StatusConflict, "stale_target", fmt.Sprintf("%s is given to the decide grant: the operator may route it", did))
	case rec.State != ledger.DecisionOpen:
		return "", newAPIError(http.StatusConflict, "stale_target", fmt.Sprintf("%s is %s: Brakeman routes only a decision still open", did, rec.State))
	case rec.Rev != body.ExpectedRev:
		return "", newAPIError(http.StatusConflict, crew.CodeStaleDecision, fmt.Sprintf("the rev expected is %d; %s's is %d: read it again", body.ExpectedRev, did, rec.Rev))
	}
	label := ""
	switch body.To.Kind {
	case "person":
		label = "person:" + body.To.Name
	case "role":
		label = "role:" + body.To.Name
	}
	if who := decisionFor(rec.Request); label != "" && who != "" {
		// Whom a decision is for (HM1) is its plan's or a person's to
		// change: Brakeman routes to a person only one for no one yet.
		return "", newAPIError(http.StatusConflict, "stale_target", fmt.Sprintf("%s is for %s already: Brakeman routes to a person only a decision for no one; the operator may route it", did, who))
	}
	if label != "" && body.To.Name != "" {
		people := rr.run.People
		if _, _, err := crew.Holder(crew.Crew{ID: cmp.Or(rr.crewID, rr.run.CrewID), Name: rr.run.Name, People: people}, label); err != nil {
			return "", newAPIError(http.StatusBadRequest, crew.CodeInvalid, err.Error()+": a decision is routed to a person of its run's crew")
		}
	}
	return rec.CurrentRun, nil
}

// decisionFor is the person a decision's request, as the ledger keeps
// it, is for (HM1's request for: {for: {label, person, responsibility}}),
// "" for none.
func decisionFor(request string) string {
	var body struct {
		For *struct {
			Person string `json:"person"`
		} `json:"for"`
	}
	if json.Unmarshal([]byte(request), &body) != nil || body.For == nil {
		return ""
	}
	return body.For.Person
}

// copyRouteOutcome copies what DR1's route answered into the act's entry:
// done with the decision routed as its receipt, refused with DR1's words
// (or the route's) for a 4xx, nothing for a failure of the server, which
// leaves the entry pending for a retry under the same opId or the
// recovery; then the request's brakeman event is sent, as copyActOutcome
// sends it.
func (s *Server) copyRouteOutcome(d *brakemanDesk, request, opID string, rec *replyRecorder) {
	state, receipt, why := routeOutcome(rec)
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

// routeReply is what an act reads of DR1's route's answer.
type routeReply struct {
	Receipt *crew.OpReceipt `json:"receipt"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// routeOutcome is a route act's state, receipt and words from DR1's
// route's recorded answer; "" for one that says nothing of whether it
// ran. The receipt is the decision routed and the history row DR1 wrote
// (dr-7#3), what its page shows.
func routeOutcome(rec *replyRecorder) (state, receipt, why string) {
	var reply routeReply
	_ = json.Unmarshal(rec.body.Bytes(), &reply)
	switch {
	case rec.status >= 200 && rec.status < 300 && reply.Receipt != nil:
		return brakeman.ActDone, routeReceipt(reply.Receipt.Decision, reply.Receipt.Seq), ""
	case rec.status >= 400 && rec.status < 500:
		why := http.StatusText(rec.status)
		if reply.Error != nil {
			why = cmpOr(reply.Error.Message, reply.Error.Code, why)
		}
		return brakeman.ActRefused, "", cutWhy(why)
	}
	return "", "", ""
}

// routeReceipt is a route's receipt as an act's entry keeps it: the
// decision and the seq of DR1's history row.
func routeReceipt(decision string, seq int) string {
	return fmt.Sprintf("%s#%d", decision, seq)
}

// answerRoute answers the turn tp's route act with DR1's route's receipt
// and error, never the decision record, which the turn's reads do not
// give it; runs is noted as a class the answer used.
func (s *Server) answerRoute(w http.ResponseWriter, tp *turnPrincipal, rec *replyRecorder) {
	d := s.brakeman
	d.readMu.Lock()
	defer d.readMu.Unlock()
	d.usedClass(tp.req.ID, brakeman.ClassRuns)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.body.Bytes(), &m); err != nil {
		writeError(w, cmp.Or(rec.status, http.StatusInternalServerError), "store_failed", "the operation's answer could not be read")
		return
	}
	out := map[string]json.RawMessage{}
	for _, k := range []string{"receipt", "error"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	writeJSON(w, cmp.Or(rec.status, http.StatusOK), out)
}

// completeRoute completes a route act the last owner left pending, from
// DR1's history by the entry's decision (its target) and op: the row
// brakeman wrote under it, the route done with its receipt; none, the
// route never committed, and the entry is marked not_run, never run by
// the recovery.
func (s *Server) completeRoute(ctx context.Context, d *brakemanDesk, request string, a brakeman.Act) error {
	if s.ledger == nil {
		return &errRefStore{what: "the ledger"}
	}
	n, _ := ledger.ParseDecisionID(a.Target.Decision)
	h, found, err := s.ledger.HistoryByOp(ctx, n, brakemanActor, a.Op)
	if err != nil {
		return err
	}
	state, receipt, why := brakeman.ActNotRun, "", "the server stopped before it ran"
	if found {
		state, receipt, why = brakeman.ActDone, routeReceipt(ledger.DecisionID(h.Decision), h.Seq), ""
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
