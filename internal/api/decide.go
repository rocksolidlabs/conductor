package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"unicode"

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
// respond route's); working once a request's place is taken
// (decideWorked), before its work.
type decideSeams struct {
	scanLimit                     int
	afterList, afterRead, working func()
}

// maxDecideWorked is how many of one subject's decide requests are worked
// at once: the decide page has at most its list's read and one read of
// each of its four cards out at once, an answer and GET /api/me beside
// them, so two tabs of it fit.
const maxDecideWorked = 16

// decideWorking counts the decide requests being worked (decideWorked) by
// subject; a subject has an entry only while one of its requests is.
type decideWorking struct {
	mu sync.Mutex
	n  map[string]int
}

// decideWorked runs work, a decide principal's request's own work, in one
// of its subject's maxDecideWorked places (by subject, whatever name the
// pass carries), or answers 429 when all are taken, work not run. A
// request is counted from the moment the server starts its own work on it
// (for a response: once its body has been read whole; for the list and the
// read: once the route's gate and query check have passed; for GET /api/me:
// once its gate has passed) until its reply has been made, before the
// reply's first byte is written:
// the place is let go once work has made its reply, on every way out of
// it, and the reply is written after. A request that carries a body the
// route does not read is not waited for.
func (s *Server) decideWorked(w http.ResponseWriter, p principal, work func(http.ResponseWriter)) {
	c := &s.decideWork
	c.mu.Lock()
	taken := c.n[p.subject] < maxDecideWorked
	if taken {
		if c.n == nil {
			c.n = map[string]int{}
		}
		c.n[p.subject]++
	}
	c.mu.Unlock()
	if !taken {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many of your requests are being worked on: send this one again once one is answered")
		return
	}
	reply := &madeReply{header: w.Header()}
	func() {
		defer func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.n[p.subject]--; c.n[p.subject] == 0 {
				delete(c.n, p.subject)
			}
		}()
		s.decideHook(func(h *decideSeams) func() { return h.working })
		work(reply)
	}()
	if reply.status != 0 {
		w.WriteHeader(reply.status)
		_, _ = w.Write(reply.body.Bytes())
	}
}

// madeReply is a reply made whole before any of it is written: its status
// and its bytes. Its header is the reply's own, which nothing sends before
// its status is written.
type madeReply struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (m *madeReply) Header() http.Header { return m.header }

func (m *madeReply) WriteHeader(status int) {
	if m.status == 0 {
		m.status = status
	}
}

func (m *madeReply) Write(b []byte) (int, error) {
	m.WriteHeader(http.StatusOK)
	return m.body.Write(b)
}

// decideListing is the subjects whose list read is in flight, one each at
// most, so what one principal has the ledger read for its list is one
// bounded read at a time (ledger.MaxOpenScan rows, ledger.MaxOpenScanBytes
// of their text). Its done ends an entry on every way out of its list.
type decideListing struct {
	mu sync.Mutex
	in map[string]bool
}

// begin marks subject's list read in flight, false when one already is;
// done ends it.
func (l *decideListing) begin(subject string) (done func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.in[subject] {
		return nil, false
	}
	if l.in == nil {
		l.in = map[string]bool{}
	}
	l.in[subject] = true
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.in, subject)
	}, true
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

// viaOf is how p authenticated, as the engine judges it: an identity
// provider's principal, with its subject, or the server's own chain. A
// subject is set only from a provider's answer (grantPrincipal), never
// from anything a request carries, so a response's authority (authorityOf)
// and the decide routes' admission (admittee) are one way in.
func viaOf(p principal) (via, subject string) {
	if p.subject != "" {
		return crew.ViaIdentity, p.subject
	}
	return crew.ViaWorkbench, ""
}

// admittee is p as crew.AdmissionOf judges it.
func admittee(p principal) crew.Admittee {
	via, subject := viaOf(p)
	return crew.Admittee{Grants: grantsOf(p), Via: via, Subject: subject}
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
		p, ok := s.authenticate(w, r, false)
		switch {
		case !ok:
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
// read. One subject has one list read in flight at most (decideLists):
// another while it is is 429, nothing read. The list it answers, or its
// 503, is made inside that guard and written after it.
func (s *Server) handleDecideList(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if !p.decider() {
		writeError(w, http.StatusForbidden, "not_decide", "this list is the decide grant's: the operator reads GET /api/decisions")
		return
	}
	if !decideNoQuery(w, r) {
		return
	}
	s.decideWorked(w, p, func(w http.ResponseWriter) { s.decideList(w, r, p) })
}

// decideList is the list's work, in its subject's place (decideWorked).
func (s *Server) decideList(w http.ResponseWriter, r *http.Request, p principal) {
	ld, ok := s.decisionLedger(w)
	if !ok {
		return
	}
	done, ok := s.decideLists.begin(p.subject)
	if !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "a list of yours is being read: read it again once it is answered")
		return
	}
	done = sync.OnceFunc(done)
	defer done()
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
	b, _ := json.Marshal(list)
	done()
	s.writeBounded(w, http.StatusOK, json.RawMessage(b), proto.MaxDecideList)
}

// decideRead answers a decide principal's GET /api/decisions/{did}: one
// read of the record whole, judged, and the decide view of that same
// record; 404 for one it is not admitted to.
func (s *Server) decideRead(w http.ResponseWriter, r *http.Request, p principal) {
	if !decideNoQuery(w, r) {
		return
	}
	s.decideWorked(w, p, func(w http.ResponseWriter) {
		if rec, a, ok := s.admittedRecord(w, r, p); ok {
			s.writeBounded(w, http.StatusOK, decideView(rec, admittee(p), a), proto.MaxDecideView)
		}
	})
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
// receipt, reduced, when the refusal was kept. Of a decision it reads the
// outcome of (crew.AdmitOutcome: answered or final, which no change makes
// open again), only the send of one of its own responses goes on, to be
// answered from its saved receipt; any other is refused here, nothing
// written. A repeat's receipt is the past act it answers: its status and
// its rev are the send's, the view beside it the decision now.
func (s *Server) decideRespond(w http.ResponseWriter, r *http.Request, p principal) {
	if !decideNoQuery(w, r) {
		return
	}
	var body decideBody
	if err := decodeJSONLimit(w, r, &body, maxDecisionRespondBody); err != nil {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, "the body is not a response")
		return
	}
	resp := decideResponse(body, p)
	s.decideWorked(w, p, func(w http.ResponseWriter) { s.decideAnswer(w, r, p, resp) })
}

// decideAnswer is the respond route's work once its body is read whole,
// in its subject's place (decideWorked). The pass is asked about first, as
// the gate asks (authenticate): one the provider no longer admits as the
// same subject with the decide grant is answered as the gate answers it,
// the decision not read and nothing recorded.
func (s *Server) decideAnswer(w http.ResponseWriter, r *http.Request, p principal, resp crew.Response) {
	again, ok := s.authenticate(w, r, false)
	if !ok {
		return
	}
	if !again.decide || again.subject != p.subject {
		s.unauthorized(w, r)
		return
	}
	rec, a, ok := s.admittedRecord(w, r, p)
	if !ok {
		return
	}
	did := rec.ID()
	if a == crew.AdmitOutcome && !slices.ContainsFunc(rec.Responses, func(row ledger.ResponseRow) bool {
		return row.ResponseID == resp.ResponseID && admittee(p).Sent(row)
	}) {
		code := crew.CodeDecisionClosed
		if rec.State == ledger.DecisionAnswered || rec.State == ledger.DecisionApplied {
			code = crew.CodeDecisionAnswered
		}
		s.writeBounded(w, decisionStatus(code), proto.DecideRefusal{Error: proto.DecideError{Code: code, Message: decideWords(code, did)}}, proto.MaxDecideReply)
		return
	}
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
		if rc.Decision != "" && (rc.Kept == nil || *rc.Kept) {
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
			v := decideView(after, admittee(p), a)
			reply.Decision = &v
		}
	}
	s.writeBounded(w, status, reply, proto.MaxDecideReply)
}

// decideBody is what a decide principal's response carries, and all it
// may: decoded strictly, a body with any other member is 400 invalid
// before any read, whatever the decision.
type decideBody struct {
	ResponseID  string `json:"responseId"`
	Choice      *int   `json:"choice,omitempty"`
	Text        string `json:"text,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
	ExpectedRev int    `json:"expectedRev"`
}

// decideResponse is the engine's response of p's body, each field set by
// name: the name the answer is shown under is the provider's for p (as
// GET /api/me says it) as shownText shows it, the actor when nothing is
// left; nothing the request carries names it.
func decideResponse(b decideBody, p principal) crew.Response {
	return crew.Response{ResponseID: b.ResponseID, Choice: b.Choice, Text: b.Text, Rationale: b.Rationale, ExpectedRev: b.ExpectedRev, ByName: shownText(p.name)}
}

// shownText is a provider's text as it is shown: without its characters
// of Unicode's Cf, Zl and Zp, and trimmed of white space at both ends.
func shownText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return -1
		}
		return r
	}, s))
}

// decideReceipt is rc reduced to what a decide principal reads of its own
// receipt: never its words, its actor or what it sent beside the choice.
func decideReceipt(rc crew.ResponseReceipt, responseID string) proto.DecideReceipt {
	return proto.DecideReceipt{K: rc.K, ResponseID: responseID, Status: rc.Status, Code: rc.Code, Rev: rc.Rev, At: rc.At, Choice: rc.Choice}
}

// decideView is what a decide principal reads of rec, its admission a:
// open (AdmitOpen), the request's own words, the engine's options, and its
// own receipts; answered or final (AdmitOutcome), its id, rev, own
// receipts and outcome alone. Its own receipts are the rows who sent
// (crew.Admittee.Sent), as its outcome is (crew.OutcomeOf): a router's row
// kept in its name is not its own. Nothing is resolved through another
// record.
func decideView(rec ledger.DecisionRecord, who crew.Admittee, a crew.Admission) proto.DecideView {
	v := proto.DecideView{ID: rec.ID(), Rev: rec.Rev, Own: []proto.DecideReceipt{}}
	for _, row := range rec.Responses {
		if !who.Sent(row) {
			continue
		}
		var rc crew.ResponseReceipt
		_ = json.Unmarshal([]byte(row.Receipt), &rc)
		rc.K, rc.At = row.K, row.At
		v.Own = append(v.Own, decideReceipt(rc, row.ResponseID))
	}
	v.Own = v.Own[max(0, len(v.Own)-proto.MaxDecideOwn):]
	v.Outcome, v.OutcomeCode = crew.OutcomeOf(rec, who)
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
		return "This send to " + did + " differs from the one kept under its response id: read the decision again."
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

// writeBounded answers v, at most max bytes as written, its newline among
// them; one past it is a 500 in the server's words, never a reply cut
// short.
func (s *Server) writeBounded(w http.ResponseWriter, status int, v any, max int) {
	b, err := json.Marshal(v)
	b = append(b, '\n')
	if err != nil || len(b) > max {
		s.log.Warn("a decide reply passed its bound", "bytes", len(b), "bound", max)
		writeError(w, http.StatusInternalServerError, "internal", "the reply would pass its bound")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// hasIdentity says an identity provider is registered: the one check the
// route to the decide grant makes (409 no_identity without one).
func (s *Server) hasIdentity() bool {
	return slices.ContainsFunc(s.Registered(), func(e ext.Registration) bool { return e.Kind == ext.KindIdentity })
}
