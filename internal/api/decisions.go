package api

import (
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
	// bounds) and its refs, each at most maxDecisionRef as encoded, its
	// escapes counted (fitRef).
	maxDecisionReply = 1 << 20
	maxDecisionRef   = 4 << 10
	// maxDecisionListed bounds a record as the operator's lists give it
	// (listedOf: its request, baseline, policy, application, why and
	// answer at their bounds), and maxDecisionListPage a page of them
	// (GET /api/decisions, a ticket's decisions; TestTheOperatorsDecisionListFitsItsBound).
	maxDecisionListed   = 48 << 10
	maxDecisionListPage = maxDecisionsListed*maxDecisionListed + 1<<10
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
	// A Brakeman turn granted route_decision is taken here too (actable):
	// its act runs this route's one operation under the actor brakeman.
	mux.HandleFunc(decisionRoutePattern, s.actable(decisionRoutePattern, s.launching(s.handleDecisionRoute)))
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
// 201 {decision} at once, 200 the same for a repeat of its requestId,
// answered before anything else is looked at. A new request's evidence
// refs are resolved, under the asker's own grant, into the baseline the
// record keeps (decisionBaseline).
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
	// A request sent before answers its decision as it is now, before its
	// evidence is resolved again: what it cites may have changed since.
	if d, sent, err := s.runs.DecisionSent(ctx, sid, req); sent {
		if err != nil {
			s.decisionError(w, "request", sid, err, nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"decision": d})
		return
	}
	baseline, err := s.decisionBaseline(ctx, sid, req.Evidence)
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

// handleSessionDecision reads one of an attempt's task's decisions whole:
// the engine judges it the task's in the read that finds it
// (crew.Engine.TaskDecision), and another task's is answered as an id no
// decision has.
func (s *Server) handleSessionDecision(w http.ResponseWriter, r *http.Request) {
	sid, ok := s.decisionSession(w, r)
	if !ok {
		return
	}
	if _, _, ok := s.attemptTask(w, sid); !ok || !noQuery(w, r) {
		return
	}
	if _, ok := s.decisionLedger(w); !ok {
		return
	}
	did := r.PathValue("did")
	d, err := s.runs.TaskDecision(r.Context(), sid, did)
	if err != nil {
		s.decisionError(w, "read", did, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": d})
}

// handleSessionDecisionWithdraw withdraws an open decision the attempt
// asked: {decision}; 409 not_held once its run is being continued.
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
	// Judged by its row in the transaction that would cancel it: another
	// attempt's is answered as an id no decision has, one of its own that
	// is final already decision_closed, in its words.
	d, err := s.runs.WithdrawDecision(ctx, sid, did)
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
	rows, next, err := decisionRows(ctx, ld, f)
	if err != nil {
		s.taskError(w, "decisions", f.CurrentRun, err)
		return
	}
	reply := map[string]any{}
	if next != "" {
		reply["next"] = next
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

// decisionRows is the page f names, newest first, and next, the last
// id, when more follow it ("" for none).
func decisionRows(ctx context.Context, ld *ledger.Ledger, f ledger.DecisionFilter) ([]ledger.DecisionRow, string, error) {
	rows, err := ld.Decisions(ctx, f)
	if err != nil || len(rows) < f.Limit || len(rows) == 0 {
		return rows, "", err
	}
	// A full page: next only when one follows it.
	more := f
	more.Before, more.Limit = rows[len(rows)-1].N, 1
	after, err := ld.Decisions(ctx, more)
	if err != nil || len(after) == 0 {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID(), nil
}

// decisionRecords is rows' records as the operator's lists give them
// (listedOf).
func (s *Server) decisionRecords(ctx context.Context, rows []ledger.DecisionRow) ([]crew.Decision, error) {
	out := make([]crew.Decision, 0, len(rows))
	for _, row := range rows {
		d, err := s.runs.Decision(ctx, row.ID())
		if err != nil {
			return nil, err
		}
		out = append(out, listedOf(d))
	}
	return out, nil
}

// listedOf is d as the operator's lists give it: without its history and
// its responses' receipts (its answer, in words, stays), which its whole
// read (GET /api/decisions/{did}) gives, so a record listed is at most
// maxDecisionListed bytes as encoded.
func listedOf(d crew.Decision) crew.Decision {
	d.History, d.Responses = nil, nil
	return d
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

// opRev checks that an operation names the rev the operator read, which
// the engine judges in the operation's transaction (crew.Engine.operate):
// one sent before under its actor and id answers its receipt, any other
// at a rev the decision moved past stale_decision. It answers false once
// it has answered.
func opRev(w http.ResponseWriter, rev int) bool {
	if rev < 1 {
		writeError(w, http.StatusBadRequest, crew.CodeInvalid, "an operation names the rev it read, expectedRev")
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
	if !opRev(w, body.ExpectedRev) {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	rc, err := s.runs.CancelDecision(ctx, did, body.CommandID, body.ExpectedRev, body.Release, body.Why, actorOf(r))
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
	if !opRev(w, body.ExpectedRev) {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	rc, err := s.runs.CommentDecision(ctx, did, body.OpID, body.ExpectedRev, body.Kind, body.Text, actorOf(r))
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
	if !opRev(w, body.ExpectedRev) {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	rc, err := s.runs.RouteDecision(ctx, did, body.OpID, body.ExpectedRev, []crew.Destination{body.To}, body.Note, actorOf(r))
	s.writeDecisionOp(w, r, "route", did, rc, err)
}

// addRunDecisions adds to the reply of a run's read (task "") or a
// task's the summaries of the decisions the run or the task holds, newest
// first, at most maxDecisionsListed, for the admin alone (a Brakeman
// turn's shared read of the run carries none: its decisions are BK2's):
// decisionsNext, the last id, when more follow (GET /api/decisions?run=
// &after= reads on), and decisionsUnread, with no list, when the ledger
// could not be read, which is logged. Nothing without a ledger or with no
// decision.
func (s *Server) addRunDecisions(r *http.Request, reply map[string]any, run, task string) {
	if p, _ := r.Context().Value(principalKey{}).(principal); s.ledger == nil || !p.admin {
		return
	}
	ctx := r.Context()
	rows, next, err := decisionRows(ctx, s.ledger, ledger.DecisionFilter{CurrentRun: run, Task: task, Limit: maxDecisionsListed})
	var out []decisionSummary
	if err == nil {
		out, err = s.decisionSummaries(ctx, rows)
	}
	switch {
	case err != nil:
		s.log.Warn("the decisions of a run's read", "run", run, "task", task, "err", err)
		reply["decisionsUnread"] = true
	case len(out) > 0:
		reply["decisions"] = out
		if next != "" {
			reply["decisionsNext"] = next
		}
	}
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
// ledger changed, each as the transition left it (crew.SQLLedgerTold):
// for each, the decision event, then one notification for the webhooks
// and the providers' sinks, queued without waiting, and the decision
// router told the decision changed (its workers read the record: routed
// as it opens, resolved at its final state, router.go). It runs under the
// ledger's lock once the commit is made, in the order of the commits, and
// reads nothing.
func (s *Server) decisionTold(changes []crew.DecisionChange) {
	for _, c := range changes {
		row := c.Row
		s.events.decision(decisionEvent{ID: row.ID(), Run: row.CurrentRun, Task: row.TaskID, State: row.State, Rev: row.Rev})
		if s.notifier != nil {
			s.notifier.decision(decisionNotification(row, c.By))
		}
		s.router.record(row.ID())
	}
}

// decisionNotification is the notification of a decision's change: the
// asking session (none for a gate) with its run and task, the state, the
// question, the run page's task with the decision, and the responder's
// actor (by) once one was accepted.
func decisionNotification(row ledger.DecisionRow, by string) ext.Notification {
	var body struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal([]byte(row.Request), &body)
	u := "/runs/" + url.PathEscape(row.CurrentRun) + "?view=tasks"
	if row.TaskID != "" {
		u += "&task=" + url.QueryEscape(row.TaskID)
	}
	u += "&decision=" + row.ID()
	return ext.Notification{Event: "decision", Type: "decision", At: time.Now().UTC(), State: row.State, Message: body.Question, URL: u, By: by,
		Session: ext.SessionRef{ID: row.SessionID, Run: row.CurrentRun, Task: row.TaskID, Attempt: row.Attempt}}
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
