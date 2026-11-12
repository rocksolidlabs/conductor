package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The decide grant's surface (docs/plans/decide-grant.md): a principal an
// identity provider gives ext.GrantDecide and not ext.GrantAdmin, a decide
// principal, is admitted to four routes, GET /api/me (me.go), GET
// /api/me/decisions, and the read and the respond route of a decision it
// is admitted to, and is answered by every other route as it is without
// the grant. What it may read and answer of a decision is one judgement,
// crew.AdmissionOf, over the one record its route read (decideAdmission);
// what it reads of one is proto.DecideView, built from that record alone
// (decideView), and of the decisions that admit it proto.DecideList. Every
// refusal it is answered carries the server's fixed words of its code
// (decideWords), never the engine's, which name other people. No live
// connection is given by the grant: GET /api/events stays the admin's.

// decideSeams lets a test reach the decide routes' bounds and hold their
// reads; nil outside tests. scanLimit stands in for ledger.MaxOpenScan;
// afterList runs between the list's ledger read and its reply, afterRead
// between a decision's read and its judgement (the read's and the
// respond route's).
type decideSeams struct {
	scanLimit            int
	afterList, afterRead func()
}

// decideHook runs the seam pick names, when a test set one.
func (s *Server) decideHook(pick func(*decideSeams) func()) {
	if h := s.decideSeam.Load(); h != nil {
		runHook(pick(h))
	}
}

// decider says p is a decide principal: the decide grant, not the admin
// grant, whatever else it holds.
func (p principal) decider() bool { return p.decide && !p.admin }

// principalOf is the principal requireAdmin or deciding put in r's
// context.
func principalOf(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

// admittee is p as crew.AdmissionOf judges it.
func admittee(p principal) crew.Admittee {
	via := crew.ViaWorkbench
	if p.subject != "" {
		via = crew.ViaIdentity
	}
	return crew.Admittee{Grants: grantsOf(p), Via: via, Subject: p.subject}
}

// decideAdmission is what p may read and answer of rec: the one judgement
// the read, the list and the respond route make.
func decideAdmission(rec ledger.DecisionRecord, p principal) crew.Admission {
	return crew.AdmissionOf(rec, admittee(p))
}

// deciding wraps the decide grant's decision routes: it authenticates
// first, as launching does, answering a principal that is neither the
// admin nor a decide principal (a host-only one, no credential) 401 under
// the rate limit, as requireAdmin does; then, on a switchyard, 403
// switchyard to the admin and a decide principal alike; else the admin as
// launching passes it and a decide principal to the handler.
func (s *Server) deciding(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.authenticate(r, false)
		switch {
		case !p.admin && !p.decide:
			s.unauthorized(w, r)
		case s.cfg.Switchyard.Enabled:
			writeError(w, http.StatusForbidden, "switchyard", "this server coordinates hosted sessions and launches none")
		default:
			next(w, withPrincipal(r, p))
		}
	}
}

// decideNoQuery answers 400 for a decide route asked with a query, in the
// server's own words.
func decideNoQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "this route takes no query")
		return false
	}
	return true
}

// decideUnread answers a ledger read that failed for a decide principal:
// 503 in the server's words, naming no decision; the error is logged.
func (s *Server) decideUnread(w http.ResponseWriter, err error) {
	s.log.Warn("a decide read failed", "err", err)
	writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "the decisions could not be read")
}

// decideNotFound is the answer to a decide principal for a decision it is
// not admitted to, and for one that does not exist: one body for both.
func decideNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, crew.CodeNotFound, "no such decision")
}

// handleDecideList answers GET /api/me/decisions: for a decide principal,
// the open decisions that admit it among the server's newest open ones,
// newest first, {id, rev} each (proto.DecideList), each row judged by
// decideAdmission as the ledger's one bounded read hands it; at most
// proto.MaxDecideListEntries, cut when one more admitted was met or the
// read reached its own bound first. The admin is 403 not_decide, nothing
// read.
func (s *Server) handleDecideList(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if !p.decider() {
		writeError(w, http.StatusForbidden, "not_decide", "this list is the decide grant's: the operator reads GET /api/decisions")
		return
	}
	if !decideNoQuery(w, r) {
		return
	}
	ld, ok := s.decisionLedger(w)
	if !ok {
		return
	}
	limit := 0
	if h := s.decideSeam.Load(); h != nil {
		limit = h.scanLimit
	}
	list := proto.DecideList{Decisions: []proto.DecideEntry{}}
	met := false
	more, err := ld.OpenDecisionsNewest(r.Context(), limit, func(row ledger.DecisionRow) bool {
		if decideAdmission(ledger.DecisionRecord{DecisionRow: row}, p) != crew.AdmitOpen {
			return true
		}
		if len(list.Decisions) == proto.MaxDecideListEntries {
			met = true
			return false
		}
		list.Decisions = append(list.Decisions, proto.DecideEntry{ID: row.ID(), Rev: row.Rev})
		return true
	})
	if err != nil {
		s.decideUnread(w, err)
		return
	}
	s.decideHook(func(h *decideSeams) func() { return h.afterList })
	list.Cut = met || more
	s.writeBounded(w, http.StatusOK, list, proto.MaxDecideList)
}

// decideRead answers a decide principal's GET /api/decisions/{did}: one
// read of the record whole, judged, and the decide view of that same
// record; 404 for one it is not admitted to.
func (s *Server) decideRead(w http.ResponseWriter, r *http.Request, p principal) {
	if !decideNoQuery(w, r) {
		return
	}
	rec, a, ok := s.admittedRecord(w, r, p)
	if !ok {
		return
	}
	s.writeBounded(w, http.StatusOK, decideView(rec, p.actor(), a), proto.MaxDecideView)
}

// admittedRecord is the decision the path names, read whole once, and what
// p may do with it; false once it has answered (404 for AdmitNone, as for
// a decision that does not exist).
func (s *Server) admittedRecord(w http.ResponseWriter, r *http.Request, p principal) (ledger.DecisionRecord, crew.Admission, bool) {
	ld, ok := s.decisionLedger(w)
	if !ok {
		return ledger.DecisionRecord{}, crew.AdmitNone, false
	}
	n, valid := ledger.ParseDecisionID(r.PathValue("did"))
	var rec ledger.DecisionRecord
	found := false
	if valid {
		var err error
		if rec, found, err = ld.Decision(r.Context(), n); err != nil {
			s.decideUnread(w, err)
			return ledger.DecisionRecord{}, crew.AdmitNone, false
		}
	}
	s.decideHook(func(h *decideSeams) func() { return h.afterRead })
	a := crew.AdmitNone
	if found {
		a = decideAdmission(rec, p)
	}
	if a == crew.AdmitNone {
		decideNotFound(w)
		return ledger.DecisionRecord{}, crew.AdmitNone, false
	}
	return rec, a, true
}

// decideRespond answers a decide principal's POST
// /api/decisions/{did}/respond: one read and its judgement first (404,
// nothing written, for one it is not admitted to), then DR1's response,
// whose transaction judges the entries it reads (crew.Admits). Its reply
// is its own receipt and the decide view of the record read again after
// the commit; a refusal its code with the server's words of it and its own
// receipt, reduced.
func (s *Server) decideRespond(w http.ResponseWriter, r *http.Request, p principal) {
	var resp crew.Response
	if err := decodeJSONLimit(w, r, &resp, maxDecisionRespondBody); err != nil {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, "the body is not a response")
		return
	}
	rec, _, ok := s.admittedRecord(w, r, p)
	if !ok {
		return
	}
	did := rec.ID()
	ctx, cancel := runContext(r)
	defer cancel()
	auth := s.authorityOf(r)
	rc, err := s.runs.RespondDecision(ctx, did, resp, actorOf(r), auth)
	if err != nil {
		var de *crew.DecisionError
		if !errors.As(err, &de) {
			s.log.Warn("a decide response failed", "decision", did, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "the answer could not be recorded")
			return
		}
		refusal := proto.DecideRefusal{Error: proto.DecideError{Code: de.Code, Message: decideWords(de.Code, did)}}
		if rc.Decision != "" {
			kept := decideReceipt(rc, resp.ResponseID)
			refusal.Receipt = &kept
		}
		s.writeBounded(w, decisionStatus(de.Code), refusal, proto.MaxDecideReply)
		return
	}
	if !rc.Repeat {
		s.recheckResponse(ctx, r, did, rc.K)
	}
	status := http.StatusCreated
	if rc.Repeat {
		status = http.StatusOK
	}
	reply := proto.DecideReply{Receipt: decideReceipt(rc, resp.ResponseID)}
	if after, found, err := s.ledger.Decision(ctx, rec.N); err == nil && found {
		if a := decideAdmission(after, p); a != crew.AdmitNone {
			v := decideView(after, p.actor(), a)
			reply.Decision = &v
		}
	}
	s.writeBounded(w, status, reply, proto.MaxDecideReply)
}

// decideReceipt is rc reduced to what a decide principal reads of its own
// receipt: never its words, its actor or what it sent beside the choice.
func decideReceipt(rc crew.ResponseReceipt, responseID string) proto.DecideReceipt {
	return proto.DecideReceipt{K: rc.K, ResponseID: responseID, Status: rc.Status, Code: rc.Code, Rev: rc.Rev, At: rc.At, Choice: rc.Choice}
}

// decideView is what a decide principal reads of rec, its admission a:
// open (AdmitOpen), the request's own words, the engine's options, and its
// own receipts; answered or final (AdmitOutcome), its id, rev, own
// receipts and outcome alone. Nothing is resolved through another record.
func decideView(rec ledger.DecisionRecord, actor string, a crew.Admission) proto.DecideView {
	v := proto.DecideView{ID: rec.ID(), Rev: rec.Rev, Own: []proto.DecideReceipt{}}
	for _, row := range rec.Responses {
		if row.Actor != actor {
			continue
		}
		var rc crew.ResponseReceipt
		_ = json.Unmarshal([]byte(row.Receipt), &rc)
		rc.K, rc.At = row.K, row.At
		v.Own = append(v.Own, decideReceipt(rc, row.ResponseID))
	}
	v.Own = v.Own[max(0, len(v.Own)-proto.MaxDecideOwn):]
	v.Outcome, v.OutcomeCode = crew.OutcomeOf(rec, actor)
	if a != crew.AdmitOpen {
		return v
	}
	var req struct {
		Question       string                `json:"question"`
		Context        string                `json:"context"`
		Options        []crew.DecisionOption `json:"options"`
		Recommendation *int                  `json:"recommendation"`
		Uncertainty    string                `json:"uncertainty"`
		Authorizes     string                `json:"authorizes"`
	}
	_ = json.Unmarshal([]byte(rec.Request), &req)
	for i, label := range crew.DecisionOptions(rec.Type, rec.Request) {
		o := proto.DecideOption{Label: label}
		if rec.Type != ledger.DecisionAssignment && i < len(req.Options) {
			o.Tradeoffs = req.Options[i].Tradeoffs
		}
		v.Options = append(v.Options, o)
	}
	created := rec.CreatedAt
	v.State, v.Type, v.Question, v.Context, v.CreatedAt = rec.State, rec.Type, req.Question, req.Context, &created
	v.Recommendation, v.Uncertainty, v.Authorizes = req.Recommendation, req.Uncertainty, req.Authorizes
	return v
}

// decideWords are the server's fixed words of a refusal's code to a decide
// principal, naming the decision and no one, at most
// proto.MaxDecideRefusalWords bytes.
func decideWords(code, did string) string {
	switch code {
	case crew.CodeDecisionAnswered:
		return did + " was answered before your answer reached it."
	case crew.CodeDecisionClosed:
		return did + " was closed before your answer."
	case crew.CodeStaleDecision:
		return did + " changed since you read it: read it again and answer."
	case crew.CodeNotApprover, crew.CodeNotFound:
		return did + " is no longer open to you."
	case crew.CodeResponseConflict:
		return "This answer to " + did + " was sent before with other words: send it as a new answer."
	case crew.CodeNotHeld:
		return did + " cannot be answered while its run is stopped."
	case crew.CodeStalePolicy:
		return did + " was opened under another policy and cannot be answered here."
	case crew.CodeInvalid:
		return "This answer does not fit " + did + ": check the choice and the text."
	case crew.CodeDecisionLimit:
		return did + " takes no more answers."
	case crew.CodeDecisionMoved:
		return "Not known whether your answer to " + did + " was recorded: send it again."
	}
	return did + " could not take this answer."
}

// writeBounded answers v, at most max bytes as encoded; one past it is a
// 500 in the server's words, never a reply cut short.
func (s *Server) writeBounded(w http.ResponseWriter, status int, v any, max int) {
	b, err := json.Marshal(v)
	if err != nil || len(b) > max {
		s.log.Warn("a decide reply passed its bound", "bytes", len(b), "bound", max)
		writeError(w, http.StatusInternalServerError, "internal", "the reply would pass its bound")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// hasIdentity says an identity provider is registered: the one check the
// route to the decide grant makes (409 no_identity without one).
func (s *Server) hasIdentity() bool {
	return slices.ContainsFunc(s.Registered(), func(e ext.Registration) bool { return e.Kind == ext.KindIdentity })
}
