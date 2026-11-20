package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The receipts of a managed run's project manifest (EN1,
// docs/plans/project-manifests.md): an attempt's steps (its trees' steps
// and its checks, every row the ledger keeps of it) and the run's
// prerequisites, each with its log and the log's SHA-256. They are read
// from the ledger, so a run the engine has let go, or one a server before
// this one ran, still answers. Admin only, with a bearer header as every
// admin route: the app fetches a log, it never sends the browser to it.

// logHashHeader carries a log's SHA-256, over the bytes served.
const logHashHeader = "X-Conductor-Log-SHA256"

// pathNumber reads a path's attempt number or k: 1 or more.
func pathNumber(v string) (int, bool) {
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 1 && strconv.Itoa(n) == v
}

// receiptLedger is the ledger the receipt routes read, or, having answered
// 503 ledger_unavailable, nil.
func (s *Server) receiptLedger(w http.ResponseWriter) *ledger.Ledger {
	if s.ledger == nil {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened; see the server's log")
		return nil
	}
	return s.ledger
}

// receiptError answers a ledger read that failed: 503 when the ledger is
// unavailable or closed, 500 otherwise.
func (s *Server) receiptError(w http.ResponseWriter, what, runID string, err error) {
	if errors.Is(err, ledger.ErrUnavailable) || errors.Is(err, ledger.ErrClosed) {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", err.Error())
		return
	}
	s.log.Warn("receipts: "+what, "run", runID, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "the receipts could not be read")
}

// knownRun reports whether the ledger has the run, having answered 404 or
// the read's error when it has not.
func (s *Server) knownRun(w http.ResponseWriter, ctx context.Context, ld *ledger.Ledger, runID string) bool {
	_, ok, err := ld.Run(ctx, runID)
	if err != nil {
		s.receiptError(w, "run", runID, err)
		return false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return false
	}
	return true
}

// handleAttemptSteps answers an attempt's receipts: 200 {steps}, each as
// the task's read reports it (crew.StepState: the row whole but for its
// log), by when it started; 404 not_found for a run or an attempt the
// ledger has not; 503 ledger_unavailable.
func (s *Server) handleAttemptSteps(w http.ResponseWriter, r *http.Request) {
	runID, taskID := r.PathValue("run"), r.PathValue("task")
	n, ok := pathNumber(r.PathValue("n"))
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) || !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id, a task id and an attempt number")
		return
	}
	ld := s.receiptLedger(w)
	if ld == nil || !s.knownRun(w, r.Context(), ld, runID) {
		return
	}
	attempts, err := ld.Attempts(r.Context(), runID)
	if err != nil {
		s.receiptError(w, "attempts", runID, err)
		return
	}
	found := false
	for _, a := range attempts {
		found = found || a.TaskID == taskID && a.N == n
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such attempt")
		return
	}
	rows, err := ld.Checks(r.Context(), runID, taskID, n, false)
	if err != nil {
		s.receiptError(w, "steps", runID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": crew.StepStates(rows)})
}

// handleStepLog answers one receipt's log of an attempt: 200 text/plain,
// the log as the ledger keeps it (at most 1 MiB), its SHA-256 in
// X-Conductor-Log-SHA256; 404 not_found for a run, attempt or row the
// ledger has not, or a step that has no log (it runs, or it ended with
// none); 410 log_dropped for a log not kept past the run's bound (its hash
// and tail are on the row).
func (s *Server) handleStepLog(w http.ResponseWriter, r *http.Request) {
	runID, taskID, id := r.PathValue("run"), r.PathValue("task"), r.PathValue("id")
	n, okN := pathNumber(r.PathValue("n"))
	k, okK := pathNumber(r.PathValue("k"))
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) || !crew.ValidTaskID(id) || !okN || !okK {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id, a task id, an attempt number, a step id and its k")
		return
	}
	ld := s.receiptLedger(w)
	if ld == nil {
		return
	}
	row, ok, err := ld.Check(r.Context(), runID, taskID, n, id, k)
	if err != nil {
		s.receiptError(w, "step log", runID, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such step")
		return
	}
	writeLog(w, row.State, row.Log, row.LogDropped, row.LogSHA256)
}

// handlePrerequisites answers a run's prerequisites' receipts: 200
// {steps}, each of phase prerequisite, by when it started; 404 not_found
// for a run the ledger has not; 503 ledger_unavailable.
func (s *Server) handlePrerequisites(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run")
	if !crew.ValidRunID(runID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id")
		return
	}
	ld := s.receiptLedger(w)
	if ld == nil || !s.knownRun(w, r.Context(), ld, runID) {
		return
	}
	rows, err := ld.Prerequisites(r.Context(), runID, false)
	if err != nil {
		s.receiptError(w, "prerequisites", runID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": crew.PrerequisiteStates(rows)})
}

// handlePrerequisiteLog answers one prerequisite receipt's log, as
// handleStepLog does a step's.
func (s *Server) handlePrerequisiteLog(w http.ResponseWriter, r *http.Request) {
	runID, id := r.PathValue("run"), r.PathValue("id")
	k, okK := pathNumber(r.PathValue("k"))
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(id) || !okK {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id, a prerequisite id and its k")
		return
	}
	ld := s.receiptLedger(w)
	if ld == nil {
		return
	}
	row, ok, err := ld.Prerequisite(r.Context(), runID, id, k)
	if err != nil {
		s.receiptError(w, "prerequisite log", runID, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such prerequisite")
		return
	}
	writeLog(w, row.State, row.Log, row.LogDropped, row.LogSHA256)
}

// writeLog answers a receipt's log: 404 while the step has none yet, and
// for one that ended with none (no hash: a check the attempt closed under,
// a step the recovery concluded), 410 once dropped, else the bytes the
// ledger keeps (at most ledger.MaxLog: it takes no more) with their
// SHA-256. An empty log that was kept has its hash and is answered, empty.
func writeLog(w http.ResponseWriter, state string, log []byte, dropped bool, sha string) {
	switch {
	case state == ledger.CheckStarted || state == ledger.CheckRunning:
		writeError(w, http.StatusNotFound, "not_found", "the step has no log yet: it is running")
		return
	case dropped:
		writeError(w, http.StatusGone, "log_dropped", "the log was not kept: the run's logs were at their bound; its hash and tail are on the step")
		return
	case sha == "":
		writeError(w, http.StatusNotFound, "not_found", "the step has no log: it ended before one was kept")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set(logHashHeader, sha)
	h.Set("Content-Length", strconv.Itoa(len(log)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(log)
}

// taskFromLedger is a task of a run the engine does not hold, as the
// ledger has it (crew.LoadedTask), a result a continuation imported read
// from the run that holds its attempt (crew.LoadImports): ErrNoLedger
// without one, ErrRunNotFound and ErrTaskNotFound for what the ledger has
// not.
func (s *Server) taskFromLedger(ctx context.Context, runID, taskID string) (crew.TaskState, []crew.AttemptState, error) {
	if s.ledger == nil {
		return crew.TaskState{}, nil, crew.ErrNoLedger
	}
	ld := crew.SQLLedger(s.ledger)
	from, ok, err := ld.Load(ctx, runID)
	if err != nil {
		return crew.TaskState{}, nil, err
	}
	if !ok {
		return crew.TaskState{}, nil, crew.ErrRunNotFound
	}
	if err := crew.LoadImports(ctx, ld, &from); err != nil {
		return crew.TaskState{}, nil, err
	}
	st, attempts, ok := crew.LoadedTask(from, taskID)
	if !ok {
		return crew.TaskState{}, nil, crew.ErrTaskNotFound
	}
	if s.runs != nil {
		// A preview the engine holds of an attempt of the run, opened from
		// the ledger (crew.Engine.PreviewsOf), as a held run's reports it.
		if pv := s.runs.PreviewsOf(runID)[taskID]; pv != nil {
			for i := range attempts {
				attempts[i].Previewing = pv[attempts[i].N]
			}
			if st.Attempt != nil {
				st.Attempt.Previewing = pv[st.Attempt.N]
			}
		}
	}
	return st, attempts, nil
}

// withPreviewing gives the latest attempt of each task of run, a run the
// engine does not hold read from its record, the preview the engine holds
// of it now (crew.Engine.PreviewsOf), as a held run's reports it.
func (s *Server) withPreviewing(run *crew.Run) {
	if s.runs == nil {
		return
	}
	previews := s.runs.PreviewsOf(run.ID)
	for i := range run.Tasks {
		if a := run.Tasks[i].Attempt; a != nil {
			a.Previewing = previews[run.Tasks[i].ID][a.N]
		}
	}
}

// withSteps gives each attempt its receipts from the ledger (the task's
// read alone carries them, as it carries what was submitted).
func (s *Server) withSteps(ctx context.Context, runID, taskID string, attempts []crew.AttemptState) error {
	if s.ledger == nil {
		return nil
	}
	// The context each attempt was given, from the ledger: a run the
	// engine holds and one read from the ledger alike.
	given, err := s.ledger.AttemptContext(ctx, runID, taskID)
	if err != nil {
		return err
	}
	for i := range attempts {
		attempts[i].Context = crew.ContextEntries(given[attempts[i].N])
	}
	for i := range attempts {
		rows, err := s.ledger.Checks(ctx, runID, taskID, attempts[i].N, false)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			attempts[i].Steps = crew.StepStates(rows)
		}
	}
	return nil
}
