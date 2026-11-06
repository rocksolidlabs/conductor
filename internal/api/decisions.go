package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Durable decisions' routes (Program item DR1, docs/plans/decisions.md,
// The changes, internal/api; docs/protocol.md, Decisions). An attempt's
// agent asks a decision with its own token and is answered at once; the
// operator (the admin: the workbench token, or an identity provider's
// principal with the admin grant) reads, answers, cancels, comments and
// routes it; Yardmaster (internal/crew) owns the wait and the
// application. A share link's controller, an agent's token and a Brakeman
// turn's token are refused every admin route (requireAdmin), so the free
// build's one approver is the operator. After each committed change of a
// decision the server sends the `decision` event and hands the notifier
// one notification (decisionTold).

// The decisions' bounds (docs/protocol.md, Decisions).
const (
	// maxDecisionRequestBody bounds a request's body, maxDecisionAnswerBody
	// a response's and an operation's.
	maxDecisionRequestBody = 24 << 10
	maxDecisionAnswerBody  = 8 << 10
	// maxDecisionSummaryText bounds a summary's question, answer and why,
	// each as encoded, maxDecisionSummary a summary, and
	// maxDecisionSummaryPage a page of them (maxDecisionsListed), under
	// the session client's 4 MiB (internal/cli, maxSessionReply).
	maxDecisionSummaryText = 1 << 10
	maxDecisionSummary     = 4 << 10
	maxDecisionSummaryPage = maxDecisionsListed * maxDecisionSummary
	maxDecisionsListed     = ledger.MaxDecisionsPage
	// maxDecisionReply bounds GET /api/decisions/{did}: the record whole
	// (its request, baseline, receipts, history and application at their
	// bounds) and its refs, each at most maxDecisionRef.
	maxDecisionReply = 1 << 20
	maxDecisionRef   = 2 << 10
	// maxDecisionEvent bounds a decision event's data.
	maxDecisionEvent = 256
)

// routeDecisions adds the decisions' routes.
func (s *Server) routeDecisions(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sessions/{id}/decisions", s.launchingAny(s.handleSessionDecisionRequest))
	mux.HandleFunc("GET /api/sessions/{id}/decisions", s.launchingAny(s.handleSessionDecisions))
	mux.HandleFunc("GET /api/sessions/{id}/decisions/{did}", s.launchingAny(s.handleSessionDecision))
	mux.HandleFunc("POST /api/sessions/{id}/decisions/{did}/withdraw", s.launchingAny(s.handleSessionDecisionWithdraw))
	mux.HandleFunc("GET /api/decisions", s.launching(s.handleDecisions))
	mux.HandleFunc("GET /api/decisions/{did}", s.launching(s.handleDecision))
	mux.HandleFunc("POST /api/decisions/{did}/respond", s.launching(s.handleDecisionRespond))
	mux.HandleFunc("POST /api/decisions/{did}/cancel", s.launching(s.handleDecisionCancel))
	mux.HandleFunc("POST /api/decisions/{did}/comment", s.launching(s.handleDecisionComment))
	mux.HandleFunc("POST /api/decisions/{did}/route", s.launching(s.handleDecisionRoute))
	mux.HandleFunc("GET /api/dispatch/tickets/{key}/decisions", s.launching(s.handleTicketDecisions))
}

// authorityOf is the authority a response of r's principal is judged by
// (crew.Authority), beside actorOf, the audit's words: the admin grant
// the principal holds, how it authenticated (the workbench token, or an
// identity provider's principal with its subject) and this server
// process's boot. It is the principal's, never the body's.
func (s *Server) authorityOf(r *http.Request) crew.Authority {
	p, _ := r.Context().Value(principalKey{}).(principal)
	a := crew.Authority{Via: crew.ViaWorkbench, Boot: s.runs.Boot()}
	if p.admin {
		a.Grant = string(ext.GrantAdmin)
	}
	if p.subject != "" {
		a.Via, a.Subject = crew.ViaIdentity, p.subject
	}
	return a
}

// decisionStatus is the HTTP status of a decision's refusal code.
func decisionStatus(code string) int {
	switch code {
	case crew.CodeInvalid:
		return http.StatusBadRequest
	case crew.CodeNotFound:
		return http.StatusNotFound
	case crew.CodeDecisionLimit:
		return http.StatusTooManyRequests
	case crew.CodeNotApprover:
		return http.StatusForbidden
	case crew.CodeDecisionMoved:
		return http.StatusServiceUnavailable
	}
	return http.StatusConflict
}

// decisionError answers err, a decision route's: a decision's refusal
// with its code and words, and a kept receipt beside it when there is
// one; the task routes' errors as they answer them.
func (s *Server) decisionError(w http.ResponseWriter, what, id string, err error, receipt any) {
	var de *crew.DecisionError
	if !errors.As(err, &de) {
		s.taskError(w, "decision "+what, id, err)
		return
	}
	body := map[string]any{"error": apiError{Code: de.Code, Message: de.Words}}
	if receipt != nil {
		body["receipt"] = receipt
	}
	writeJSON(w, decisionStatus(de.Code), body)
}

// decisionSession is the session of a decision's session route, for its
// own agent token alone: a decision is asked and read by the attempt's
// own session.
func (s *Server) decisionSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	d, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return "", false
	}
	if agentTokenOK(d, presentedToken(r, false)) {
		return d.Info().ID, true
	}
	if !s.limiter.allow(clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return "", false
	}
	writeError(w, http.StatusUnauthorized, "unauthorized", "the attempt's own agent token required")
	return "", false
}

// handleSessionDecisionRequest takes an attempt's decision request:
// 201 {decision} at once, 200 the same for a repeat of its requestId.
// Its evidence refs are resolved first into the baseline the record
// keeps (decisionBaseline).
func (s *Server) handleSessionDecisionRequest(w http.ResponseWriter, r *http.Request) {
	sid, ok := s.decisionSession(w, r)
	if !ok {
		return
	}
	var req crew.DecisionRequest
	if err := decodeJSONLimit(w, r, &req, maxDecisionRequestBody); err != nil {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, err.Error())
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	baseline, err := s.decisionBaseline(ctx, req.Evidence)
	if err != nil {
		s.decisionError(w, "request", sid, err, nil)
		return
	}
	req.Baseline = baseline
	d, created, err := s.runs.RequestDecision(ctx, sid, req)
	if err != nil {
		s.decisionError(w, "request", sid, err, nil)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"decision": d})
}

// attemptTask is the run and task of an attempt's session.
func (s *Server) attemptTask(w http.ResponseWriter, sid string) (run, task string, ok bool) {
	run, task, _, ok = s.runs.AttemptOf(sid)
	if !ok {
		writeError(w, http.StatusNotFound, "not_an_attempt", crew.ErrNotAnAttempt.Error())
	}
	return run, task, ok
}

// decisionLedger is the ledger the decisions' reads are made from, or
// false once it has answered: 503 without one.
func (s *Server) decisionLedger(w http.ResponseWriter) (*ledger.Ledger, bool) {
	if s.ledger == nil {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "this server keeps no ledger, so no decisions")
		return nil, false
	}
	return s.ledger, true
}

// handleSessionDecisions lists an attempt's task's decisions in the run
// that holds them, newest first: {decisions, next?}, each a summary.
func (s *Server) handleSessionDecisions(w http.ResponseWriter, r *http.Request) {
	sid, ok := s.decisionSession(w, r)
	if !ok {
		return
	}
	run, task, ok := s.attemptTask(w, sid)
	if !ok {
		return
	}
	f, ok := decisionPage(w, r, "after", "limit")
	if !ok {
		return
	}
	f.CurrentRun, f.Task = run, task
	s.writeDecisionPage(w, r, f, true)
}

// handleSessionDecision reads one of an attempt's task's decisions whole.
func (s *Server) handleSessionDecision(w http.ResponseWriter, r *http.Request) {
	sid, ok := s.decisionSession(w, r)
	if !ok {
		return
	}
	run, task, ok := s.attemptTask(w, sid)
	if !ok || !noQuery(w, r) {
		return
	}
	did := r.PathValue("did")
	if !s.taskHolds(w, r.Context(), run, task, did) {
		return
	}
	d, err := s.runs.Decision(r.Context(), did)
	if err != nil {
		s.decisionError(w, "read", did, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": d})
}

// taskHolds reports whether decision did is one of the task's in the run
// that holds it (asked by it, or holding it), and answers 404 otherwise.
func (s *Server) taskHolds(w http.ResponseWriter, ctx context.Context, run, task, did string) bool {
	ld, ok := s.decisionLedger(w)
	if !ok {
		return false
	}
	n, ok := ledger.ParseDecisionID(did)
	if !ok {
		writeError(w, http.StatusNotFound, crew.CodeNotFound, "not a decision's id")
		return false
	}
	rows, err := ld.Decisions(ctx, ledger.DecisionFilter{CurrentRun: run, Task: task, Before: n + 1, Limit: 1})
	if err != nil {
		s.taskError(w, "decision read", did, err)
		return false
	}
	if len(rows) == 0 || rows[0].N != n {
		writeError(w, http.StatusNotFound, crew.CodeNotFound, "no decision "+did+" of task "+task)
		return false
	}
	return true
}

// handleSessionDecisionWithdraw withdraws an open decision the attempt
// asked: {decision}.
func (s *Server) handleSessionDecisionWithdraw(w http.ResponseWriter, r *http.Request) {
	sid, ok := s.decisionSession(w, r)
	if !ok {
		return
	}
	var body struct{}
	if err := decodeJSONLimit(w, r, &body, maxDecisionAnswerBody); err != nil {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, err.Error())
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	did := r.PathValue("did")
	d, err := s.runs.WithdrawDecision(ctx, sid, did)
	var de *crew.DecisionError
	if errors.As(err, &de) && de.Code == crew.CodeNotFound {
		// One of its own that is final already: closed, in its words.
		_, task, n, _ := s.runs.AttemptOf(sid)
		if rec, rerr := s.runs.Decision(ctx, did); rerr == nil && rec.Asker != nil && rec.Asker.Task == task && rec.Asker.Attempt == n && rec.ClosedAt != nil {
			err = &crew.DecisionError{Code: crew.CodeDecisionClosed, Words: did + " is " + rec.State + ": " + cmp.Or(rec.Why, "it is closed")}
		}
	}
	if err != nil {
		s.decisionError(w, "withdraw", sid, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": d})
}

// decisionPage reads a list's query: the parameters named (of after,
// limit, state, run, ticket) and nothing else; after a decision's id,
// limit 1 to maxDecisionsListed. It answers 400 and false for a query it
// cannot take.
func decisionPage(w http.ResponseWriter, r *http.Request, names ...string) (ledger.DecisionFilter, bool) {
	var f ledger.DecisionFilter
	q, err := readQuery(r, names...)
	bad := func(why string) (ledger.DecisionFilter, bool) {
		writeError(w, http.StatusBadRequest, "invalid_request", why)
		return ledger.DecisionFilter{}, false
	}
	if err != nil {
		return bad(err.Error())
	}
	after, err := q.one("after")
	if err != nil {
		return bad(err.Error())
	}
	if after != "" {
		n, ok := ledger.ParseDecisionID(after)
		if !ok {
			return bad("after is a decision's id, dr-N")
		}
		f.Before = n
	}
	limit, err := q.one("limit")
	if err != nil {
		return bad(err.Error())
	}
	f.Limit = maxDecisionsListed
	if limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil || n < 1 || n > maxDecisionsListed {
			return bad("limit is 1 to " + strconv.Itoa(maxDecisionsListed))
		}
		f.Limit = n
	}
	state, err := q.one("state")
	if err != nil {
		return bad(err.Error())
	}
	if state != "" {
		switch state {
		case ledger.DecisionOpen, ledger.DecisionAnswered, ledger.DecisionApplied, ledger.DecisionSuperseded, ledger.DecisionCancelled, ledger.DecisionExpired:
		default:
			return bad("state is open, answered, applied, superseded, cancelled or expired")
		}
		f.States = []string{state}
	}
	if f.CurrentRun, err = q.one("run"); err != nil {
		return bad(err.Error())
	}
	if f.CurrentRun != "" && !crew.ValidRunID(f.CurrentRun) {
		return bad("run is a run's id")
	}
	if f.Ticket, err = q.one("ticket"); err != nil {
		return bad(err.Error())
	}
	return f, true
}

// writeDecisionPage answers the page f names, newest first: summaries
// (an attempt's) or the records without their history (the admin's),
// with next, the last id, when more follow.
func (s *Server) writeDecisionPage(w http.ResponseWriter, r *http.Request, f ledger.DecisionFilter, summaries bool) {
	ld, ok := s.decisionLedger(w)
	if !ok {
		return
	}
	ctx := r.Context()
	rows, err := ld.Decisions(ctx, f)
	next := false
	if err == nil && len(rows) == f.Limit {
		// A full page: next only when one follows it.
		var after []ledger.DecisionRow
		more := f
		more.Before, more.Limit = rows[len(rows)-1].N, 1
		after, err = ld.Decisions(ctx, more)
		next = len(after) > 0
	}
	if err != nil {
		s.taskError(w, "decisions", f.CurrentRun, err)
		return
	}
	reply := map[string]any{}
	if next {
		reply["next"] = rows[len(rows)-1].ID()
	}
	if summaries {
		out, err := s.decisionSummaries(ctx, rows)
		if err != nil {
			s.taskError(w, "decisions", f.CurrentRun, err)
			return
		}
		reply["decisions"] = out
	} else {
		out, err := s.decisionRecords(ctx, rows)
		if err != nil {
			s.taskError(w, "decisions", f.CurrentRun, err)
			return
		}
		reply["decisions"] = out
	}
	writeJSON(w, http.StatusOK, reply)
}

// decisionRecords is rows' records, each without its history.
func (s *Server) decisionRecords(ctx context.Context, rows []ledger.DecisionRow) ([]crew.Decision, error) {
	out := make([]crew.Decision, 0, len(rows))
	for _, row := range rows {
		d, err := s.runs.Decision(ctx, row.ID())
		if err != nil {
			return nil, err
		}
		d.History = nil
		out = append(out, d)
	}
	return out, nil
}

// decisionSummary is a decision as a list of an attempt's gives it: never
// the record (decision_read reads one whole). Its question, answer and
// why are each cut at a character's end to maxDecisionSummaryText bytes
// as encoded, cut saying one was.
type decisionSummary struct {
	ID           string     `json:"id"`
	RequestID    string     `json:"requestId,omitempty"`
	Type         string     `json:"type"`
	Origin       string     `json:"origin"`
	Task         string     `json:"task,omitempty"`
	Gate         string     `json:"gate,omitempty"`
	State        string     `json:"state"`
	Rev          int        `json:"rev"`
	Question     string     `json:"question"`
	Answer       string     `json:"answer,omitempty"`
	Why          string     `json:"why,omitempty"`
	SupersededBy string     `json:"supersededBy,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	AnsweredAt   *time.Time `json:"answeredAt,omitempty"`
	ClosedAt     *time.Time `json:"closedAt,omitempty"`
	Cut          bool       `json:"cut,omitempty"`
}

// decisionSummaries is rows as summaries: a record is read for the
// answer of a decision that has one.
func (s *Server) decisionSummaries(ctx context.Context, rows []ledger.DecisionRow) ([]decisionSummary, error) {
	out := make([]decisionSummary, 0, len(rows))
	for _, row := range rows {
		answer := ""
		if !row.AnsweredAt.IsZero() {
			d, err := s.runs.Decision(ctx, row.ID())
			if err != nil {
				return nil, err
			}
			answer = d.Answer
		}
		out = append(out, summaryOf(row, answer))
	}
	return out, nil
}

// summaryOf is row's summary with its answer in words.
func summaryOf(row ledger.DecisionRow, answer string) decisionSummary {
	var body struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal([]byte(row.Request), &body)
	sum := decisionSummary{ID: row.ID(), RequestID: row.RequestID, Type: row.Type, Origin: row.Origin, Task: row.TaskID, Gate: row.Gate,
		State: row.State, Rev: row.Rev, Question: body.Question, Answer: answer, Why: row.Why, SupersededBy: row.SupersededBy, CreatedAt: row.CreatedAt}
	if !row.AnsweredAt.IsZero() {
		at := row.AnsweredAt
		sum.AnsweredAt = &at
	}
	if !row.ClosedAt.IsZero() {
		at := row.ClosedAt
		sum.ClosedAt = &at
	}
	for _, f := range []*string{&sum.Question, &sum.Answer, &sum.Why} {
		if c := cutEncoded(*f, maxDecisionSummaryText); c != *f {
			*f, sum.Cut = c, true
		}
	}
	return sum
}

// cutEncoded is s cut where a character ends to at most n bytes as
// writeJSON encodes it: a character JSON escapes counts as its escape
// (a control character, <, > and & six bytes, " and \ two, U+2028 and
// U+2029 six, a byte that is not UTF-8 six, as �).
func cutEncoded(s string, n int) string {
	size := 0
	for i := 0; i < len(s); {
		c, w := utf8.DecodeRuneInString(s[i:])
		enc := w
		switch {
		case c == utf8.RuneError && w == 1:
			enc = 6
		case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
			enc = 2
		case c < 0x20 || c == '<' || c == '>' || c == '&' || c == '\u2028' || c == '\u2029':
			enc = 6
		}
		if size+enc > n {
			return s[:i]
		}
		size += enc
		i += w
	}
	return s
}

// handleDecisions lists the decisions the query names, newest first,
// each without its history: {decisions, next?}.
func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	f, ok := decisionPage(w, r, "after", "limit", "state", "run", "ticket")
	if !ok {
		return
	}
	if f.Ticket != "" {
		id, ok := s.ticketID(w, r, f.Ticket)
		if !ok {
			return
		}
		f.Ticket = id
	}
	s.writeDecisionPage(w, r, f, false)
}

// ticketID is the id of the ticket with the key, or false once it has
// answered.
func (s *Server) ticketID(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return "", false
	}
	v, err := ds.Ticket(r.Context(), key)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return "", false
	}
	return v.ID, true
}

// handleTicketDecisions lists the decisions of the ticket's runs (those
// whose ticket is the ticket), newest first, at most maxDecisionsListed,
// each without its history: {decisions}.
func (s *Server) handleTicketDecisions(w http.ResponseWriter, r *http.Request) {
	key, ok := ticketKey(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	id, ok := s.ticketID(w, r, key)
	if !ok {
		return
	}
	ld, ok := s.decisionLedger(w)
	if !ok {
		return
	}
	rows, err := ld.Decisions(r.Context(), ledger.DecisionFilter{Ticket: id, Limit: maxDecisionsListed})
	if err != nil {
		s.taskError(w, "ticket decisions", key, err)
		return
	}
	out, err := s.decisionRecords(r.Context(), rows)
	if err != nil {
		s.taskError(w, "ticket decisions", key, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": out})
}

// handleDecision reads a decision whole with its evidence's refs:
// {decision, refs}.
func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	did := r.PathValue("did")
	d, err := s.runs.Decision(r.Context(), did)
	if err != nil {
		s.decisionError(w, "read", did, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": d, "refs": s.decisionRefs(r.Context(), d.Evidence)})
}

// handleDecisionRespond takes the operator's response: 201 {receipt,
// decision} once accepted, 200 the same for a repeat; a refusal its code
// with the receipt kept. An identity provider's principal's accepted
// response is applied only once the provider admits its token again
// (recheckResponse).
func (s *Server) handleDecisionRespond(w http.ResponseWriter, r *http.Request) {
	did := r.PathValue("did")
	var resp crew.Response
	if err := decodeJSONLimit(w, r, &resp, maxDecisionAnswerBody); err != nil {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, err.Error())
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	auth := s.authorityOf(r)
	rc, err := s.runs.RespondDecision(ctx, did, resp, actorOf(r), auth)
	if err != nil {
		var kept any
		if rc.Decision != "" {
			kept = rc
		}
		s.decisionError(w, "respond", did, err, kept)
		return
	}
	if auth.Via == crew.ViaIdentity && !rc.Repeat {
		s.recheckResponse(ctx, r, did, rc.K)
	}
	status := http.StatusCreated
	if rc.Repeat {
		status = http.StatusOK
	}
	d, err := s.runs.Decision(ctx, did)
	if err != nil {
		s.decisionError(w, "respond", did, err, rc)
		return
	}
	writeJSON(w, status, map[string]any{"receipt": rc, "decision": d})
}

// recheckResponse asks the identity provider again, once the response k
// to decision did has committed, about the token r presented, and tells
// the engine whether it admitted it as the same subject with the admin
// grant (crew.Engine.ConfirmAuthority): a provider that fails, is late or
// is busy has not said yes. The token is the request's alone: never kept
// or logged.
func (s *Server) recheckResponse(ctx context.Context, r *http.Request, did string, k int) {
	p, _ := r.Context().Value(principalKey{}).(principal)
	admitted := false
	if g, ok := s.exts.identity.(guardIdentity); ok && p.subject != "" {
		pr, yes, known := g.recheck(ctx, presentedToken(r, false))
		admitted = known && yes && pr.Subject == p.subject && s.grantPrincipal(pr).admin
	}
	s.runs.ConfirmAuthority(did, k, admitted)
}

// decisionOp is an operation's body's common part: the rev the operator
// read.
type decisionOp struct {
	ExpectedRev int `json:"expectedRev"`
}

// The operator's operations' bodies (the guide's inputs are their
// schemas): a cancel's, a comment's and a route's. A response's is
// crew.Response.
type (
	decisionCancelBody struct {
		decisionOp
		CommandID string `json:"commandId"`
		Why       string `json:"why"`
		Release   bool   `json:"release,omitempty"`
	}
	decisionCommentBody struct {
		decisionOp
		OpID string `json:"opId"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	decisionRouteBody struct {
		decisionOp
		OpID string           `json:"opId"`
		To   crew.Destination `json:"to"`
		Note string           `json:"note,omitempty"`
	}
)

// opReady checks an operation against the rev the operator read: one
// sent before under its actor and id goes on (the engine answers its
// receipt, or op_conflict), any other is refused stale_decision when the
// decision moved past the rev read, with the words of what changed. It
// answers false once it has answered.
func (s *Server) opReady(w http.ResponseWriter, r *http.Request, did, opID string, rev int) bool {
	n, ok := ledger.ParseDecisionID(did)
	if !ok || s.ledger == nil {
		return true // the engine refuses it in words
	}
	rec, found, err := s.ledger.Decision(r.Context(), n)
	if err != nil || !found {
		return true
	}
	actor := actorOf(r)
	for _, h := range rec.History {
		if h.OpID == opID && h.By == actor {
			return true
		}
	}
	switch {
	case rev < 1:
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, "an operation names the rev it read, expectedRev")
		return false
	case rev != rec.Rev:
		writeError(w, http.StatusConflict, crew.CodeStaleDecision, did+" is at rev "+strconv.Itoa(rec.Rev)+", not the rev "+strconv.Itoa(rev)+" read: read it again")
		return false
	}
	return true
}

// writeDecisionOp answers an operation's receipt with the decision as it is now.
func (s *Server) writeDecisionOp(w http.ResponseWriter, r *http.Request, what, did string, rc crew.OpReceipt, err error) {
	if err != nil {
		s.decisionError(w, what, did, err, nil)
		return
	}
	d, err := s.runs.Decision(r.Context(), did)
	if err != nil {
		s.decisionError(w, what, did, err, rc)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": rc, "decision": d})
}

// handleDecisionCancel cancels an open or answered decision, its tasks
// released when release says so, else cancelled with it:
// {receipt, decision}.
func (s *Server) handleDecisionCancel(w http.ResponseWriter, r *http.Request) {
	did := r.PathValue("did")
	var body decisionCancelBody
	if err := decodeJSONLimit(w, r, &body, maxDecisionAnswerBody); err != nil {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, err.Error())
		return
	}
	if !s.opReady(w, r, did, body.CommandID, body.ExpectedRev) {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	rc, err := s.runs.CancelDecision(ctx, did, body.CommandID, body.Release, body.Why, actorOf(r))
	s.writeDecisionOp(w, r, "cancel", did, rc, err)
}

// handleDecisionComment keeps the operator's comment, or asks the asking
// attempt for more information (more_info): {receipt, decision}.
func (s *Server) handleDecisionComment(w http.ResponseWriter, r *http.Request) {
	did := r.PathValue("did")
	var body decisionCommentBody
	if err := decodeJSONLimit(w, r, &body, maxDecisionAnswerBody); err != nil {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, err.Error())
		return
	}
	if !s.opReady(w, r, did, body.OpID, body.ExpectedRev) {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	rc, err := s.runs.CommentDecision(ctx, did, body.OpID, body.Kind, body.Text, actorOf(r))
	s.writeDecisionOp(w, r, "comment", did, rc, err)
}

// handleDecisionRoute routes a decision to a destination (the operator,
// local/1's one): {receipt, decision}.
func (s *Server) handleDecisionRoute(w http.ResponseWriter, r *http.Request) {
	did := r.PathValue("did")
	var body decisionRouteBody
	if err := decodeJSONLimit(w, r, &body, maxDecisionAnswerBody); err != nil {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, err.Error())
		return
	}
	if !s.opReady(w, r, did, body.OpID, body.ExpectedRev) {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	rc, err := s.runs.RouteDecision(ctx, did, body.OpID, []crew.Destination{body.To}, body.Note, actorOf(r))
	s.writeDecisionOp(w, r, "route", did, rc, err)
}

// runDecisions is the summaries of the decisions a run holds (task "")
// or a task of it holds, newest first, at most maxDecisionsListed; none
// without a ledger.
func (s *Server) runDecisions(ctx context.Context, run, task string) []decisionSummary {
	if s.ledger == nil {
		return nil
	}
	rows, err := s.ledger.Decisions(ctx, ledger.DecisionFilter{CurrentRun: run, Task: task, Limit: maxDecisionsListed})
	if err != nil || len(rows) == 0 {
		return nil
	}
	out, err := s.decisionSummaries(ctx, rows)
	if err != nil {
		return nil
	}
	return out
}

// decisionEvent is the data of a `decision` event: a decision changed,
// for a page to read again.
type decisionEvent struct {
	ID    string `json:"id"`
	Run   string `json:"run"`
	Task  string `json:"task,omitempty"`
	State string `json:"state"`
	Rev   int    `json:"rev"`
}

// decision sends a decision event, at most maxDecisionEvent bytes; it is
// state a page reads again, dropped with a client that cannot keep up,
// as a run event is.
func (h *eventHub) decision(ev decisionEvent) {
	b, err := json.Marshal(ev)
	if err != nil || len(b) > maxDecisionEvent {
		return
	}
	h.broadcast([]byte("event: decision\ndata: " + string(b) + "\n\n"))
}

// decisionTold is told the decisions a committed transition of the
// ledger changed (crew.SQLLedgerTold): for each, the decision event, then
// one notification for the webhooks and the providers' sinks, queued
// without waiting, and the decision router told of the record (routed as
// it opens, resolved at its final state: router.go). It runs on the
// committing goroutine, after the commit.
func (s *Server) decisionTold(ids []string) {
	for _, id := range ids {
		n, ok := ledger.ParseDecisionID(id)
		if !ok || s.ledger == nil {
			continue
		}
		rec, found, err := s.ledger.Decision(context.Background(), n)
		if err != nil || !found {
			continue
		}
		s.events.decision(decisionEvent{ID: id, Run: rec.CurrentRun, Task: rec.TaskID, State: rec.State, Rev: rec.Rev})
		if s.notifier != nil {
			s.notifier.decision(decisionNotification(rec))
		}
		s.router.record(id)
	}
}

// decisionNotification is the notification of a decision's change: the
// asking session (none for a gate) with its run and task, the state, the
// question, the run page's task with the decision, and the responder's
// actor once one was accepted.
func decisionNotification(rec ledger.DecisionRecord) ext.Notification {
	var body struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal([]byte(rec.Request), &body)
	u := "/runs/" + url.PathEscape(rec.CurrentRun) + "?view=tasks"
	if rec.TaskID != "" {
		u += "&task=" + url.QueryEscape(rec.TaskID)
	}
	u += "&decision=" + rec.ID()
	n := ext.Notification{Event: "decision", Type: "decision", At: time.Now().UTC(), State: rec.State, Message: body.Question, URL: u,
		Session: ext.SessionRef{ID: rec.SessionID, Run: rec.CurrentRun, Task: rec.TaskID, Attempt: rec.Attempt}}
	for _, r := range rec.Responses {
		if r.Status == ledger.ResponseAccepted {
			n.By = r.Actor
		}
	}
	return n
}

// decision queues a decision's notification for every webhook that lists
// the decision event and every provider sink that takes it, without
// waiting. Once the webhooks are closed it queues nothing.
func (nt *notifier) decision(n ext.Notification) {
	if nt.hooks.ctx.Err() != nil {
		return
	}
	var body []byte
	for _, w := range nt.hooks.hooks {
		event := w.eventFor(n.Event, n.Type)
		if event == "" {
			continue
		}
		if body == nil {
			b, err := webhookBody(n)
			if err != nil {
				break
			}
			body = b
		}
		w.queue.push(webhookDelivery{event: event, body: body})
	}
	for _, g := range nt.sinks {
		if g.mayTake(n.Event, n.Type) {
			g.queue.push(n)
		}
	}
}
