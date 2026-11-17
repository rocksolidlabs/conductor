package api

import (
	"errors"
	"net/http"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A candidate's preview over HTTP (Program item PV1, docs/plans/browser-
// evidence.md "A candidate's preview"; PR-8): Preview this candidate and
// its Close, the admin's alone, each a command with its receipt in the
// run's ledger (crew.Engine.PreviewCandidate, ClosePreviewCommand), for a
// run the engine holds and one it opens from the ledger.

// maxPreviewCommandBody bounds a preview command's body: {commandId}.
const maxPreviewCommandBody = 1 << 10

// routePreviewCommands adds the two command routes, each the admin's and
// a switchyard's 403.
func (s *Server) routePreviewCommands(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/runs/{run}/tasks/{task}/attempts/{n}/preview", s.requireAdmin(s.launchingAny(s.handlePreviewOpen)))
	mux.HandleFunc("POST /api/runs/{run}/tasks/{task}/attempts/{n}/preview/{seq}/close", s.requireAdmin(s.launchingAny(s.handlePreviewClose)))
}

// previewCommandBody is a preview command's body.
type previewCommandBody struct {
	CommandID string `json:"commandId"`
}

// previewCommand reads a preview command's path (a run id, a task id, an
// attempt number from 1, and for a Close a seq from 1), takes no
// parameter, and reads its body, {commandId} and nothing else, at most
// maxPreviewCommandBody: 400 invalid_request for anything else. When it
// reports false it has answered.
func previewCommand(w http.ResponseWriter, r *http.Request, close bool) (runID, taskID string, n, seq int, cmd crew.Command, ok bool) {
	runID, taskID = r.PathValue("run"), r.PathValue("task")
	n, ok = pathNumber(r.PathValue("n"))
	if close {
		var sok bool
		seq, sok = pathNumber(r.PathValue("seq"))
		ok = ok && sok
	}
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) || !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id, a task id, an attempt number and, to close, a preview's number")
		return "", "", 0, 0, cmd, false
	}
	if !noQuery(w, r) {
		return "", "", 0, 0, cmd, false
	}
	var body previewCommandBody
	if err := decodeJSONLimit(w, r, &body, maxPreviewCommandBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the body is {commandId}")
		return "", "", 0, 0, cmd, false
	}
	if !crew.CommandIDPattern.MatchString(body.CommandID) {
		writeError(w, http.StatusBadRequest, "invalid_request", crew.ErrBadCommandID.Error())
		return "", "", 0, 0, cmd, false
	}
	return runID, taskID, n, seq, crew.Command{ID: body.CommandID}, true
}

// handlePreviewOpen opens a preview of the candidate as the admin's
// command: 202 {receipt, seq, state} (resolved: its readiness to come;
// 200 the same for a repeat, the preview's state now); a refusal once the
// command is taken with its receipt, {error, receipt} (previewRefused);
// one before it with none (previewError).
func (s *Server) handlePreviewOpen(w http.ResponseWriter, r *http.Request) {
	runID, taskID, n, _, cmd, ok := previewCommand(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	rc, ps, err := s.runs.PreviewCandidate(ctx, runID, taskID, n, actorOf(r), cmd)
	if rc.CommandID != "" && rc.Status == ledger.CommandFailed {
		s.previewRefused(w, rc, err)
		return
	}
	if err != nil {
		s.previewError(w, runID, err)
		return
	}
	status := http.StatusAccepted
	if rc.Repeat {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"receipt": rc, "seq": ps.Seq, "state": ps.State})
}

// handlePreviewClose closes the preview seq of the candidate as the
// admin's command: 200 {receipt, state} (ending, or ended once it is let
// go; a repeat the same, the state now); 409 preview_opening while its
// open is under way, 404 no_preview for one not open, each with its
// receipt; a Close of a run the engine neither holds nor previews now is
// 404 no_preview with none.
func (s *Server) handlePreviewClose(w http.ResponseWriter, r *http.Request) {
	runID, taskID, n, seq, cmd, ok := previewCommand(w, r, true)
	if !ok {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	rc, ps, err := s.runs.ClosePreviewCommand(ctx, runID, taskID, n, seq, actorOf(r), cmd)
	if rc.CommandID != "" && rc.Status == ledger.CommandFailed {
		s.previewRefused(w, rc, err)
		return
	}
	if err != nil {
		s.previewError(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": rc, "state": ps.State})
}

// previewRefused answers a preview command whose receipt is failed, now or
// before (a repeat answers as the first did): {error, receipt}, the
// refusal its code: 404 for not_found and no_preview, 429 for
// previews_busy with the previews open named (open: [{run, task, attempt,
// seq}], those the refusal named, or the server's now for a repeat), 409
// for the rest (not_candidate, run_continued, run_stopped, preview_dirty,
// preview_open, preview_limit, no_manifest, preview_opening,
// preview_failed).
func (s *Server) previewRefused(w http.ResponseWriter, rc crew.Receipt, err error) {
	code := cmpOr(rc.Refusal, crew.RefusalPreviewFailed)
	body := map[string]any{"error": apiError{Code: code, Message: rc.Error}, "receipt": rc}
	status := http.StatusConflict
	switch code {
	case crew.RefusalNotFound, crew.RefusalNoPreview:
		status = http.StatusNotFound
	case crew.RefusalPreviewsBusy:
		status = http.StatusTooManyRequests
		open := s.runs.OpenPreviews()
		if busy := (*crew.PreviewsBusyError)(nil); errors.As(err, &busy) {
			open = busy.Open
		}
		body["open"] = open
	}
	writeJSON(w, status, body)
}

// previewError answers a preview command refused before it was taken, with
// no receipt: 404 no_preview for a Close of a preview the engine does not
// hold (and no repeat of one), 409 for a run continued (run_continued) or
// stopped (run_stopped) as the ledger has it, a command id used before
// with other content (command_conflict) or a run whose commands are
// spent (too_many_commands); 404 for a run the engine and the ledger have
// not; 503 ledger_unavailable.
func (s *Server) previewError(w http.ResponseWriter, runID string, err error) {
	switch {
	case errors.Is(err, crew.ErrPreviewNotFound):
		writeError(w, http.StatusNotFound, crew.RefusalNoPreview, err.Error())
	case errors.Is(err, crew.ErrRunStopped):
		writeError(w, http.StatusConflict, crew.RefusalRunStopped, err.Error())
	default:
		s.taskError(w, "preview command", runID, err)
	}
}
