package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The task routes of a managed run (docs/plans/agent-harness-order.md, row
// 6 of #334): an attempt's session reads its task and submits its result
// with its own token, and an admin reads a task and commands the run with
// receipts (crew.Command).

// taskPage is how much of a task file one read answers.
const taskPage = 32 << 10

// taskSession is the session of the task routes, for its own agent token
// or the admin; a hosted or unknown session is refused.
func (s *Server) taskSession(w http.ResponseWriter, r *http.Request, agentOnly bool) (session.Driver, bool) {
	d, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return nil, false
	}
	if agentOnly {
		// A result is always the agent's own: the workbench token submits
		// none, so a person cannot finish a task for an agent by mistake.
		if agentTokenOK(d, presentedToken(r, false)) {
			return d, true
		}
		if p := s.authenticate(r, false); p.admin {
			writeError(w, http.StatusForbidden, "agent_only", "a result is submitted by the attempt's own session, with its token")
			return nil, false
		}
		if !s.limiter.allow(clientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return nil, false
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "agent token required")
		return nil, false
	}
	if _, ok := s.attentionPrincipal(w, r, d); !ok {
		return nil, false
	}
	return d, true
}

// handleSessionTask answers an attempt's task: 200 {task, markdown, page,
// pages, bytes}, the task file in pages of 32 KiB (page 1 unless asked);
// 404 not_an_attempt for a session that is no attempt, 409 for an attempt
// without a worktree yet.
func (s *Server) handleSessionTask(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	q, ok := queryValues(w, r)
	if !ok {
		return
	}
	page := 1
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "page is a number from 1")
			return
		}
		page = n
	}
	md, js, err := s.runs.TaskContext(d.Info().ID)
	switch {
	case errors.Is(err, crew.ErrNotAnAttempt):
		writeError(w, http.StatusNotFound, "not_an_attempt", err.Error())
		return
	case errors.Is(err, crew.ErrNotRunning):
		writeError(w, http.StatusConflict, "not_running", err.Error())
		return
	case err != nil:
		s.log.Warn("task context", "session", d.Info().ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the task could not be read")
		return
	}
	pages := (len(md) + taskPage - 1) / taskPage
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		writeError(w, http.StatusBadRequest, "invalid_request", "the task has "+strconv.Itoa(pages)+" pages")
		return
	}
	from := (page - 1) * taskPage
	to := min(from+taskPage, len(md))
	var task json.RawMessage
	if json.Valid(js) {
		task = json.RawMessage(js)
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task, "markdown": string(md[from:to]), "page": page, "pages": pages, "bytes": len(md)})
}

// handleSessionTaskResult takes an attempt's result from its own session:
// 201 {task} once it is recorded (the task verifying, or succeeded when it
// has no checks), 200 {task} for the same commit submitted again while the
// first is verified; 409 with why when the worktree or the commit is not
// as a result must be, 404 not_an_attempt, 400 bad_result.
func (s *Server) handleSessionTaskResult(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, true)
	if !ok {
		return
	}
	var res crew.Result
	if err := decodeJSON(w, r, &res); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	st, repeat, err := s.runs.Submit(ctx, d.Info().ID, res)
	if err != nil {
		s.taskError(w, "result", d.Info().ID, err)
		return
	}
	status := http.StatusCreated
	if repeat {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"task": st})
}

// taskError answers an error of the task routes with its code.
func (s *Server) taskError(w http.ResponseWriter, what, id string, err error) {
	var dirty *crew.DirtyWorktreeError
	switch {
	case errors.Is(err, crew.ErrNotAnAttempt):
		writeError(w, http.StatusNotFound, "not_an_attempt", err.Error())
	case errors.Is(err, crew.ErrRunNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such run")
	case errors.Is(err, crew.ErrTaskNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, crew.ErrBadResult):
		writeError(w, http.StatusBadRequest, "bad_result", err.Error())
	case errors.Is(err, crew.ErrBadCommandID):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.As(err, &dirty):
		writeError(w, http.StatusConflict, "dirty_worktree", err.Error())
	case errors.Is(err, crew.ErrStaleAttempt):
		writeError(w, http.StatusConflict, "stale_attempt", err.Error())
	case errors.Is(err, crew.ErrNotRunning):
		writeError(w, http.StatusConflict, "not_running", err.Error())
	case errors.Is(err, crew.ErrOffBranch):
		writeError(w, http.StatusConflict, "off_branch", err.Error())
	case errors.Is(err, crew.ErrCommitMissing):
		writeError(w, http.StatusConflict, "commit_mismatch", err.Error())
	case errors.Is(err, crew.ErrNotFromInput):
		writeError(w, http.StatusConflict, "not_from_input", err.Error())
	case errors.Is(err, crew.ErrReviewChanged):
		writeError(w, http.StatusConflict, "review_changed", err.Error())
	case errors.Is(err, crew.ErrReservedPath):
		writeError(w, http.StatusConflict, "reserved_path", err.Error())
	case errors.Is(err, crew.ErrSubmodules):
		writeError(w, http.StatusConflict, "submodules", err.Error())
	case errors.Is(err, crew.ErrRunStopped):
		writeError(w, http.StatusConflict, "run_stopped", err.Error())
	case errors.Is(err, crew.ErrRunContinued):
		writeError(w, http.StatusConflict, crew.RefusalRunContinued, err.Error())
	case errors.Is(err, crew.ErrTaskState):
		writeError(w, http.StatusConflict, "task_state", err.Error())
	case errors.Is(err, crew.ErrCommandConflict):
		writeError(w, http.StatusConflict, "command_conflict", err.Error())
	case errors.Is(err, crew.ErrStaleRevision):
		writeError(w, http.StatusConflict, "stale_revision", err.Error())
	case errors.Is(err, crew.ErrTooManyCommands):
		writeError(w, http.StatusConflict, "too_many_commands", err.Error())
	case errors.Is(err, crew.ErrInboxFull):
		writeError(w, http.StatusTooManyRequests, "inbox_full", err.Error())
	case errors.Is(err, crew.ErrBadMessage), errors.Is(err, crew.ErrBadBlocker), errors.Is(err, crew.ErrBadCheckpoint), errors.Is(err, crew.ErrReviewNoCheck):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, crew.ErrMemberNotFound):
		writeError(w, http.StatusNotFound, "no_member", err.Error())
	case errors.Is(err, crew.ErrNoCommit):
		writeError(w, http.StatusConflict, "no_commit", err.Error())
	case errors.Is(err, crew.ErrNoLedger), errors.Is(err, ledger.ErrUnavailable), errors.Is(err, ledger.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", err.Error())
	default:
		s.log.Warn("task "+what+" failed", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// handleGetTask answers a managed run's task with every attempt: 200
// {task, attempts}, each attempt with what it submitted and its receipts
// (steps, from the ledger); for a run the engine does not hold (let go,
// or run by a server before this one) the ledger's (manifest.go:
// taskFromLedger), with no preview; 404 for an unknown run or task; 503
// ledger_unavailable when the run is not held and there is no ledger to
// read it from.
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	runID, taskID := r.PathValue("run"), r.PathValue("task")
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id and a task id")
		return
	}
	st, attempts, err := s.runs.TaskOf(runID, taskID)
	if errors.Is(err, crew.ErrRunNotFound) {
		st, attempts, err = s.taskFromLedger(r.Context(), runID, taskID)
	}
	if err == nil {
		err = s.withSteps(r.Context(), runID, taskID, attempts)
	}
	if err != nil {
		s.taskError(w, "get", runID, err)
		return
	}
	if attempts == nil {
		attempts = []crew.AttemptState{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": st, "attempts": attempts})
}

// taskCommand reads a command's body: {commandId, expectedRevision?}.
func taskCommand(w http.ResponseWriter, r *http.Request) (crew.Command, bool) {
	var cmd crew.Command
	if err := decodeJSON(w, r, &cmd); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return cmd, false
	}
	if !crew.CommandIDPattern.MatchString(cmd.ID) {
		writeError(w, http.StatusBadRequest, "invalid_request", crew.ErrBadCommandID.Error())
		return cmd, false
	}
	if cmd.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "expectedRevision is a revision of the run")
		return cmd, false
	}
	return cmd, true
}

// writeReceipt answers a command: 200 {receipt} when it was taken (now or
// before: receipt.repeat), 409 {error, receipt} when it failed and the
// failure is its receipt (the same id asks again and gets the same answer,
// its refusal the code), and the error's code when it was not taken at all.
func (s *Server) writeReceipt(w http.ResponseWriter, runID string, receipt crew.Receipt, err error) {
	if receipt.CommandID != "" && receipt.Status == ledger.CommandFailed {
		code := receipt.Refusal
		if code == "" {
			code = crew.RefusalTaskState
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": apiError{Code: code, Message: receipt.Error}, "receipt": receipt})
		return
	}
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"receipt": receipt})
		return
	}
	s.taskError(w, "command", runID, err)
}

// runCommand reads the command of a run route and checks the run is one
// the engine holds.
func (s *Server) runCommand(w http.ResponseWriter, r *http.Request, withTask bool) (runID, taskID string, cmd crew.Command, ok bool) {
	runID = r.PathValue("run")
	if !crew.ValidRunID(runID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id")
		return "", "", cmd, false
	}
	if withTask {
		taskID = r.PathValue("task")
		if !crew.ValidTaskID(taskID) {
			writeError(w, http.StatusBadRequest, "invalid_request", "a task id")
			return "", "", cmd, false
		}
		if _, _, err := s.runs.TaskOf(runID, taskID); err != nil {
			s.taskError(w, "command", runID, err)
			return "", "", cmd, false
		}
	} else if _, ok := s.runs.Get(runID); !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return "", "", cmd, false
	}
	cmd, ok = taskCommand(w, r)
	return runID, taskID, cmd, ok
}

// handlePauseRun pauses a managed run's dispatch (attempts in flight go
// on): POST {commandId, expectedRevision?} → 200 {receipt}.
func (s *Server) handlePauseRun(w http.ResponseWriter, r *http.Request) {
	s.handlePause(w, r, true)
}

// handleUnpauseRun resumes a paused run's dispatch.
func (s *Server) handleUnpauseRun(w http.ResponseWriter, r *http.Request) {
	s.handlePause(w, r, false)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request, paused bool) {
	runID, _, cmd, ok := s.runCommand(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	receipt, err := s.runs.Pause(ctx, runID, actorOf(r), cmd, paused)
	s.writeReceipt(w, runID, receipt, err)
}

// handleCancelTask cancels a task: its attempt in flight is stopped, and
// the task is cancelled with what needs it.
func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	runID, taskID, cmd, ok := s.runCommand(w, r, true)
	if !ok {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	receipt, err := s.runs.Cancel(ctx, runID, actorOf(r), cmd, taskID)
	s.writeReceipt(w, runID, receipt, err)
}

// handleRetryTask allows a failed or cancelled task one more attempt.
func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	runID, taskID, cmd, ok := s.runCommand(w, r, true)
	if !ok {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	receipt, err := s.runs.Retry(ctx, runID, actorOf(r), cmd, taskID)
	s.writeReceipt(w, runID, receipt, err)
}

// handleAcceptTask takes a failed task's last submitted result as its
// result ("Accept anyway").
func (s *Server) handleAcceptTask(w http.ResponseWriter, r *http.Request) {
	runID, taskID, cmd, ok := s.runCommand(w, r, true)
	if !ok {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	receipt, err := s.runs.Accept(ctx, runID, actorOf(r), cmd, taskID)
	s.writeReceipt(w, runID, receipt, err)
}

// Messages, blockers and checkpoints (#334, rows 9 and 10): a member's
// session sends another member a message, reads its inbox and
// acknowledges it; an attempt's session reports what blocks it and saves
// where it got to. Each with the session's own token, or the admin's.

// handleSessionMessages sends a message from the session's member: body
// {to, text}; 201 {message}; 404 no_member; 429 inbox_full once the
// recipient has 50 not acknowledged; 400 for an empty text or one over
// 4,000 characters.
func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	var body struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	m, err := s.runs.SendMessage(ctx, d.Info().ID, body.To, body.Text)
	if err != nil {
		s.taskError(w, "message", d.Info().ID, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"message": m})
}

// handleSessionInbox answers the session's member's messages after
// `?after=` (a sequence number, 0 unless given), at most 50: {messages}.
func (s *Server) handleSessionInbox(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	q, ok := queryValues(w, r)
	if !ok {
		return
	}
	var after int64
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "after is a sequence number from 0")
			return
		}
		after = n
	}
	msgs, err := s.runs.Inbox(d.Info().ID, after)
	if err != nil {
		s.taskError(w, "inbox", d.Info().ID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// handleSessionInboxAck acknowledges the session's member's messages up
// to a sequence number: body {upTo}; 200 {acked}, how many now.
func (s *Server) handleSessionInboxAck(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	var body struct {
		UpTo int64 `json:"upTo"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if body.UpTo < 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "upTo is a sequence number from 1")
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	n, err := s.runs.Ack(ctx, d.Info().ID, body.UpTo)
	if err != nil {
		s.taskError(w, "inbox ack", d.Info().ID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"acked": n})
}

// handleRunMessages answers a managed run's messages: {messages}.
func (s *Server) handleRunMessages(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run")
	if !crew.ValidRunID(runID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id")
		return
	}
	msgs, err := s.runs.MessagesOf(runID)
	if err != nil {
		s.taskError(w, "messages", runID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// handleSessionTaskBlocked reports what blocks the session's attempt:
// body {reason, message?}; 200 {task}; the session then asks the message
// (else the reason) as its own question.
func (s *Server) handleSessionTaskBlocked(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	var body struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	st, err := s.runs.Blocked(ctx, d.Info().ID, body.Reason, body.Message)
	if err != nil {
		s.taskError(w, "blocked", d.Info().ID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": st})
}

// handleSessionTaskCheckpoint saves where the session's attempt got to,
// tied to its worktree's commit: body {done, remaining, notes?}; 200
// {attempt}; 409 no_commit when the worktree has none.
func (s *Server) handleSessionTaskCheckpoint(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	var body struct {
		Done      string `json:"done"`
		Remaining string `json:"remaining"`
		Notes     string `json:"notes"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	a, err := s.runs.SaveCheckpoint(ctx, d.Info().ID, body.Done, body.Remaining, body.Notes)
	if err != nil {
		s.taskError(w, "checkpoint", d.Info().ID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempt": a})
}
