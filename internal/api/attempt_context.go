package api

import (
	"net/http"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// The text of a block of context an attempt was given
// (docs/plans/context-seams.md, CT-3): GET
// /api/runs/{run}/tasks/{task}/attempts/{n}/context/{seq}, the admin's.
// The row is the ledger's; the text the data directory's context file
// keeps, shown only when its SHA-256 is the row's. A turn of Brakeman's is
// refused it (it has no entry in the observe list): it sees a block's
// shape alone, through the task's read (filterTask).

// contextSHAHeader carries the SHA-256 of the block's text.
const contextSHAHeader = "X-Conductor-Context-SHA256"

// handleAttemptContextBody answers 200 text/plain the block's text, at
// most ext.MaxContextBody bytes, its SHA-256 in contextSHAHeader; 404
// not_found for a row the ledger has not, and for a text the file lacks or
// whose digest is not its row's (logged, never shown); 400
// invalid_request for an id or a number not shaped as one; 503
// ledger_unavailable without a ledger.
func (s *Server) handleAttemptContextBody(w http.ResponseWriter, r *http.Request) {
	runID, taskID := r.PathValue("run"), r.PathValue("task")
	n, okN := pathNumber(r.PathValue("n"))
	seq, okSeq := pathNumber(r.PathValue("seq"))
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) || !okN || !okSeq {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id, a task id, an attempt number and a block's number")
		return
	}
	ld := s.receiptLedger(w)
	if ld == nil {
		return
	}
	given, err := ld.AttemptContext(r.Context(), runID, taskID)
	if err != nil {
		s.receiptError(w, "context: the rows", runID, err)
		return
	}
	var sum string
	for _, row := range given[n] {
		if row.Seq == seq {
			sum = row.SHA256
		}
	}
	if sum == "" || s.contextFiles == nil {
		writeError(w, http.StatusNotFound, "not_found", "no such block")
		return
	}
	body, ok, err := s.contextFiles.Body(runID, taskID, n, seq, sum)
	if err != nil || !ok {
		// The row names a text the file lacks, or one whose digest is not
		// its own: never shown, said in the log by where it is alone.
		s.log.Warn("context: a recorded block's text is missing or changed", "run", runID, "task", taskID, "attempt", n, "seq", seq, "err", err)
		writeError(w, http.StatusNotFound, "not_found", "no such block")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set(contextSHAHeader, sum)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}
