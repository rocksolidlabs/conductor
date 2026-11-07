package api

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// The prepared failed-check request and Check again (Program item RV1,
// docs/plans/review-repair.md, PR-3): a failed receipt read from the
// ledger as a draft note of kind check, with everything a repair needs
// (the tested commit, the check's identity and outcome, the last of its
// output, its log's hash, the attempt before, the class the server worked
// out, the criteria the run was launched with), and whether Check again
// would be taken now, by the engine's own rule, on the run of the
// receipt's chain it would go to. Check again itself is the engine's
// command (crew.Engine.Recheck), sent to the newest run of the chain the
// candidate is in, so a candidate of a run a continuation carries is
// checked again in the continuation. Both are the admin's.

// MaxRepairReply bounds the prepared request, whole and never cut: the
// sum of its parts at their escaped worst as writeJSON encodes them (six
// bytes for a byte it escapes). The finding is about 80 KiB (its argv at
// the 8 KiB a check's argv may hold, every byte a control character, 48
// KiB with its 32 entries' quotes; its tail and its quote at 2 KiB each,
// 12 KiB each; its error at dispatch.MaxCheckError and its words at
// dispatch.MaxFindingMessage characters; ids, commits and the attempt
// before under 4 KiB), the criteria at most about 390 KiB (a package's
// 16 KiB of acceptance, 96 KiB escaped; or a task's eight checks with
// their argv at the same worst), the rest under 4 KiB
// (TestPrepareRepairBound). Exported for the CLI.
const MaxRepairReply = 512 << 10

// preparedFinding is the draft note a failed receipt makes: of kind
// check, a change asked for, its words a first draft the person rewrites,
// its receipt (check) filled from the ledger with the server's class, the
// person's choice set to it and the quote to the receipt's tail, both
// theirs to change. A review sends back only the receipt's source,
// identity, choice and quote (dispatch.CheckRequest).
type preparedFinding struct {
	Kind    string            `json:"kind"`
	Intent  string            `json:"intent"`
	Message string            `json:"message"`
	Check   dispatch.CheckRef `json:"check"`
}

// repairCriteria is what the candidate was to meet: the acceptance
// criteria of the package a ticket launched the run with (source ticket),
// else the task's checks as the crew names them (source checks).
type repairCriteria struct {
	Source     string       `json:"source"`
	Acceptance string       `json:"acceptance,omitempty"`
	Checks     []crew.Check `json:"checks,omitempty"`
}

// repairPlan and repairTicket are the launch's references: the plan
// revision that launched the run and its ticket.
type repairPlan struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

type repairTicket struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// recheckOffer says whether Check again would be taken now, judged by the
// engine's own rule (crew.Engine.RecheckTaken) on the run it would go to:
// the newest of the receipt's chain (run, at its revision); attempt and
// commit name the candidate as the receipt does, in its own run; why says
// in words why not.
type recheckOffer struct {
	Allowed  bool   `json:"allowed"`
	Run      string `json:"run"`
	Revision int64  `json:"revision"`
	Attempt  int    `json:"attempt"`
	Commit   string `json:"commit"`
	Why      string `json:"why,omitempty"`
}

// preparedRepair is the prepared request's reply.
type preparedRepair struct {
	Finding  preparedFinding `json:"finding"`
	Criteria repairCriteria  `json:"criteria"`
	Plan     *repairPlan     `json:"plan,omitempty"`
	Ticket   *repairTicket   `json:"ticket,omitempty"`
	Recheck  recheckOffer    `json:"recheck"`
}

// repairPhases are the phases whose failure fails an attempt, so whose
// receipt a repair can name: a task's check, a verify step, and a step
// that makes the tree (dispatch's receipt identities).
var repairPhases = []string{ledger.PhaseCheck, ledger.PhaseVerify, ledger.PhaseSetup, ledger.PhaseService, ledger.PhaseReady}

// receiptPassed reports whether a finished receipt failed nothing, by the
// fields the ledger says it by, as a check finding's receipt is judged
// (dispatch's CheckRef.passed, whose table says why): words fail a row of
// every phase; a check or a verify step passed only with exit 0, stable,
// in time and by no signal besides; a step of the tree by its words alone.
func receiptPassed(r ledger.CheckRow) bool {
	if r.State != ledger.CheckFinished || r.Error != "" {
		return false
	}
	switch r.Phase {
	case "", ledger.PhaseCheck, ledger.PhaseVerify:
		return r.Exit == 0 && r.Stable && !r.TimedOut && r.Signal == ""
	}
	return true
}

// fitTail is a receipt's tail as a check finding keeps it
// (dispatch.MaxCheckExcerpt bytes of UTF-8, its last; dispatch's fitTail).
func fitTail(tail string) string {
	if len(tail) > dispatch.MaxCheckExcerpt {
		tail = tail[len(tail)-dispatch.MaxCheckExcerpt:]
	}
	for i := 0; i < utf8.UTFMax-1 && tail != "" && !utf8.RuneStart(tail[0]); i++ {
		tail = tail[1:]
	}
	tail = strings.ToValidUTF8(tail, string(utf8.RuneError))
	for len(tail) > dispatch.MaxCheckExcerpt {
		_, n := utf8.DecodeRuneInString(tail)
		tail = tail[n:]
	}
	return tail
}

// fitCheckError is a receipt's words as a check finding keeps them
// (dispatch.MaxCheckError bytes of UTF-8, its first; dispatch's fitError).
func fitCheckError(words string) string {
	words = strings.ToValidUTF8(words, string(utf8.RuneError))
	for len(words) > dispatch.MaxCheckError {
		_, n := utf8.DecodeLastRuneInString(words)
		words = words[:len(words)-n]
	}
	return words
}

// receiptIdentity is a receipt's identity as a check finding names it:
// <run>/<task>/<n>/<phase>/<id>/<k>.
func receiptIdentity(r ledger.CheckRow, phase string) string {
	return r.RunID + "/" + r.TaskID + "/" + strconv.Itoa(r.N) + "/" + phase + "/" + r.ID + "/" + strconv.Itoa(r.K)
}

// shortCommit is a commit's first seven digits.
func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// handlePrepareRepair answers GET /api/runs/{run}/tasks/{task}/attempts/
// {n}/steps/{id}/{k}/repair: 200 the prepared failed-check request
// (preparedRepair), under MaxRepairReply; 404 not_found for a run, an
// attempt or a receipt the ledger has not; 409 task_state for a receipt
// that did not fail (it runs, or it passed) or whose phase fails no
// attempt (a cleanup step); 400 invalid_request for an id that is not one
// or any query; 503 ledger_unavailable, and run_changing while a
// continuation of the chain is being made.
func (s *Server) handlePrepareRepair(w http.ResponseWriter, r *http.Request) {
	runID, taskID, id := r.PathValue("run"), r.PathValue("task"), r.PathValue("id")
	n, okN := pathNumber(r.PathValue("n"))
	k, okK := pathNumber(r.PathValue("k"))
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) || !crew.ValidTaskID(id) || !okN || !okK {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id, a task id, an attempt number, a step id and its k")
		return
	}
	if _, err := readQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ld := s.receiptLedger(w)
	if ld == nil {
		return
	}
	ctx := r.Context()
	fr, aerr, err := s.failedReceipt(ctx, ld, runID, taskID, n, id, k)
	switch {
	case err != nil && errors.Is(err, errRunContinuing):
		s.chainReadError(w, runID, err)
		return
	case err != nil:
		s.receiptError(w, "repair: the receipt", runID, err)
		return
	case aerr != nil:
		writeAPIError(w, aerr)
		return
	}
	row, a, nodes, ref, failure, phase, rc := fr.run, fr.attempt, fr.nodes, fr.ref, fr.failure, fr.ref.Phase, fr.row
	out := preparedRepair{
		Finding: preparedFinding{Kind: "check", Intent: "change", Check: ref,
			Message: cutRunes(fmt.Sprintf("The %s %s failed on %s (attempt %d): %s.", phase, id, shortCommit(rc.Subject), n, failure.Words), dispatch.MaxFindingMessage)},
		Recheck: s.recheckOfferOn(ctx, nodes, runID, a),
	}
	out.Criteria, out.Plan, out.Ticket = criteriaOf(row, taskID)
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(out); err != nil || b.Len() > MaxRepairReply {
		s.log.Error("a prepared failed-check request passed its bound", "run", runID, "bytes", b.Len(), "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the prepared request could not be made")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Bytes())
}

// failedReceiptOf is a failed receipt as the ledger keeps it, read for a
// check finding: the run's row, the attempt, the run's chain, the receipt's
// row, its class (crew.ClassifyFailure over the same check's receipts on
// the same commit across the chain) and the receipt filled as a check
// finding keeps it (dispatch.CheckRef: chosen set to the class, quote to
// the tail).
type failedReceiptOf struct {
	run     ledger.RunRow
	attempt ledger.AttemptRow
	nodes   []chainNode
	row     ledger.CheckRow
	failure crew.Failure
	ref     dispatch.CheckRef
}

// failedReceipt reads the receipt k of the step id of attempt n of the
// task in the run from the ledger ld, for the prepared request and a
// review's check finding alike: an apiError for what the reader is told
// (404 not_found for a run, an attempt or a receipt the ledger has not;
// 409 task_state for a receipt that did not fail, one still running, or a
// phase whose failure fails no attempt), an error for a read that failed
// (errRunContinuing among them, while a continuation of the chain is
// made).
func (s *Server) failedReceipt(ctx context.Context, ld *ledger.Ledger, runID, taskID string, n int, id string, k int) (failedReceiptOf, *apiError, error) {
	var out failedReceiptOf
	row, found, err := ld.Run(ctx, runID)
	if err != nil {
		return out, nil, err
	}
	if !found {
		return out, newAPIError(http.StatusNotFound, "not_found", "no such run"), nil
	}
	checks, err := ld.Checks(ctx, runID, taskID, n, false)
	if err != nil {
		return out, nil, err
	}
	i := slices.IndexFunc(checks, func(c ledger.CheckRow) bool { return c.ID == id && c.K == k })
	if i < 0 {
		return out, newAPIError(http.StatusNotFound, "not_found", "no such step"), nil
	}
	rc := checks[i]
	rc.RunID, rc.TaskID, rc.N = runID, taskID, n
	phase := rc.Phase
	if phase == "" {
		phase = ledger.PhaseCheck
	}
	switch {
	case !slices.Contains(repairPhases, phase):
		return out, newAPIError(http.StatusConflict, crew.RefusalTaskState, "a "+phase+" step's failure fails no attempt: there is nothing to repair"), nil
	case rc.State == ledger.CheckStarted || rc.State == ledger.CheckRunning:
		return out, newAPIError(http.StatusConflict, crew.RefusalTaskState, "the step is still running"), nil
	case receiptPassed(rc):
		return out, newAPIError(http.StatusConflict, crew.RefusalTaskState, "the step passed: nothing failed to repair"), nil
	}
	attempts, err := ld.Attempts(ctx, runID)
	if err != nil {
		return out, nil, err
	}
	j := slices.IndexFunc(attempts, func(a ledger.AttemptRow) bool { return a.TaskID == taskID && a.N == n })
	if j < 0 {
		return out, newAPIError(http.StatusNotFound, "not_found", "no such attempt"), nil
	}
	a := attempts[j]
	nodes, _, _, err := s.runChain(ctx, runID)
	if err != nil {
		return out, nil, err
	}
	same, err := ld.ChecksOn(ctx, chainRuns(nodes, runID), taskID, rc.Phase, id, rc.Subject)
	if err != nil {
		return out, nil, err
	}
	failure := crew.ClassifyFailure(a, rc, same)
	previous, err := s.attemptBefore(ctx, attempts, nodes, taskID, n)
	if err != nil {
		return out, nil, err
	}
	ref := dispatch.CheckRef{Source: "ledger", Receipt: receiptIdentity(rc, phase), Run: runID, Task: taskID, Attempt: n, Phase: phase, ID: id, K: k,
		Subject: rc.Subject, Argv: rc.Argv, Signal: rc.Signal, TimedOut: rc.TimedOut, Killed: rc.Killed, Stable: rc.Stable, State: rc.State,
		Error: fitCheckError(rc.Error), Where: rc.Where, LogSHA256: rc.LogSHA256, LogBytes: rc.LogBytes, Tail: fitTail(rc.Tail),
		Class: failure.Class, Chosen: failure.Class, Previous: previous}
	ref.Quote = ref.Tail
	if ref.Argv == nil {
		ref.Argv = []string{}
	}
	if rc.State == ledger.CheckFinished {
		exit := rc.Exit
		ref.Exit = &exit
	}
	return failedReceiptOf{run: row, attempt: a, nodes: nodes, row: rc, failure: failure, ref: ref}, nil, nil
}

// criteriaOf is what the run row's candidate of the task was to meet:
// the acceptance criteria of the package a ticket launched it with (and
// the launch's plan revision and ticket), else the task's checks as the
// run's crew names them.
func criteriaOf(row ledger.RunRow, taskID string) (repairCriteria, *repairPlan, *repairTicket) {
	var plan *repairPlan
	var ticket *repairTicket
	if row.Package != "" {
		var pkg dispatch.Package
		if json.Unmarshal([]byte(row.Package), &pkg) == nil {
			plan = &repairPlan{ID: pkg.Plan.ID, N: pkg.Plan.N}
			ticket = &repairTicket{ID: pkg.Ticket.ID, Key: pkg.Ticket.Key}
			if pkg.Ticket.Acceptance != "" {
				return repairCriteria{Source: "ticket", Acceptance: pkg.Ticket.Acceptance}, plan, ticket
			}
		}
	}
	out := repairCriteria{Source: "checks", Checks: []crew.Check{}}
	var c crew.Crew
	if json.Unmarshal([]byte(row.Crew), &c) == nil {
		if i := slices.IndexFunc(c.Tasks, func(t crew.Task) bool { return t.ID == taskID }); i >= 0 && c.Tasks[i].Checks != nil {
			out.Checks = c.Tasks[i].Checks
		}
	}
	return out, plan, ticket
}

// chainRuns is the runs of a chain a same-check read takes: every run of
// it, at most ledger.MaxChecksOnRuns, the newest kept and the anchor
// always among them.
func chainRuns(nodes []chainNode, anchor string) []string {
	ids := make([]string, 0, len(nodes))
	for _, nd := range nodes {
		ids = append(ids, nd.id)
	}
	if len(ids) > ledger.MaxChecksOnRuns {
		ids = ids[len(ids)-ledger.MaxChecksOnRuns:]
		if !slices.Contains(ids, anchor) {
			ids[0] = anchor
		}
	}
	if len(ids) == 0 {
		ids = []string{anchor}
	}
	return ids
}

// chainForward is the run anchor and the runs that continue it, in order,
// each the one its predecessor's continued_by names, as far as the chain
// read has them.
func chainForward(nodes []chainNode, anchor string) []string {
	byID := map[string]chainNode{}
	for _, nd := range nodes {
		byID[nd.id] = nd
	}
	out := []string{anchor}
	for id := anchor; ; {
		nd, ok := byID[id]
		if !ok || nd.by == "" || slices.Contains(out, nd.by) {
			return out
		}
		if _, ok := byID[nd.by]; !ok {
			return out
		}
		id = nd.by
		out = append(out, id)
	}
}

// attemptBefore is the task's attempt before n as the ledger keeps it:
// this run's, else one of its chain's (attempt numbers run on across
// continuations); nil for the first, or one the ledger has no more.
func (s *Server) attemptBefore(ctx context.Context, attempts []ledger.AttemptRow, nodes []chainNode, taskID string, n int) (*dispatch.CheckPrevious, error) {
	if n <= 1 {
		return nil, nil
	}
	find := func(rows []ledger.AttemptRow) *dispatch.CheckPrevious {
		for _, a := range rows {
			if a.TaskID == taskID && a.N == n-1 {
				return &dispatch.CheckPrevious{Attempt: a.N, State: a.State, Stage: a.Stage, Commit: a.ResultCommit}
			}
		}
		return nil
	}
	if p := find(attempts); p != nil {
		return p, nil
	}
	for _, nd := range slices.Backward(nodes) {
		rows, err := s.ledger.Attempts(ctx, nd.id)
		if err != nil {
			return nil, err
		}
		if p := find(rows); p != nil {
			return p, nil
		}
	}
	return nil, nil
}

// refusalWords is a refusal's words without its error's leading kind.
func refusalWords(err error) string {
	msg := err.Error()
	for _, kind := range []error{crew.ErrTaskState, crew.ErrStaleCandidate, crew.ErrRunContinued, crew.ErrTaskNotFound} {
		if rest, ok := strings.CutPrefix(msg, kind.Error()+": "); ok {
			return rest
		}
	}
	return msg
}

// recheckOfferOn is whether Check again of a, an attempt of the run
// anchor whose chain nodes are, would be taken now: on the newest run of
// the chain from anchor, by the engine's rule when the engine holds it,
// else not ("continue run X first"), the revision then the ledger's.
func (s *Server) recheckOfferOn(ctx context.Context, nodes []chainNode, anchor string, a ledger.AttemptRow) recheckOffer {
	forward := chainForward(nodes, anchor)
	newest := forward[len(forward)-1]
	out := recheckOffer{Run: newest, Attempt: a.N, Commit: a.ResultCommit}
	if p, ok, err := s.ledger.Pointer(ctx, newest); err == nil && ok {
		out.Revision = p.Revision
	}
	if a.ResultCommit == "" {
		out.Why = fmt.Sprintf("attempt %d submitted no commit: there is nothing to check again", a.N)
		return out
	}
	if _, held := s.runs.Get(newest); !held {
		out.Why = "continue run " + newest + " first"
		return out
	}
	rev, err := s.runs.RecheckTaken(newest, recheckRequestFor(anchor, newest, a.TaskID, a.N, a.ResultCommit))
	out.Revision = rev
	if err != nil {
		out.Why = refusalWords(err)
		return out
	}
	out.Allowed = true
	return out
}

// recheckRequestFor is Check again of the candidate attempt n of the task
// in the run anchor, sent to the run to: Imported its reference when to
// is a continuation of anchor.
func recheckRequestFor(anchor, to, taskID string, n int, commit string) crew.RecheckRequest {
	rq := crew.RecheckRequest{Task: taskID, Attempt: n, Commit: commit}
	if to != anchor {
		rq.Imported = anchor + "/" + taskID + "/" + strconv.Itoa(n)
	}
	return rq
}

// recheckBody is POST /api/runs/{run}/tasks/{task}/recheck's body: the
// command's id and expected revision (the prepared request's revision, of
// the run it goes to), and the candidate as its own run names it.
type recheckBody struct {
	crew.Command
	Attempt int    `json:"attempt"`
	Commit  string `json:"commit"`
}

// handleRecheckTask is Check again: POST /api/runs/{run}/tasks/{task}/
// recheck {commandId, attempt, commit, expectedRevision?} → 200 {receipt,
// run, attempt}, where the candidate (attempt n of the task in {run}, its
// commit) is checked again: the newest run of {run}'s chain, with the
// candidate's reference when that is a continuation, attempt the new
// attempt there (n+1, its result the same commit). A resend is looked for
// by its command id along the chain from {run} and answered from the run
// that took it (receipt.repeat), another content under the id 409
// command_conflict; one Check again at a time, from the chain's read to
// its answer (s.rechecking), so one id is taken in one run. 409 {error,
// receipt, run} for a refusal the engine recorded (stale_candidate when
// the candidate is no longer the task's standing failed one there,
// stale_revision for an expected revision not the run's, run_continued
// for a run continued since the chain's read, task_state otherwise); 409
// task_state "continue run X first" when the server holds no run of the
// chain to send it to, nothing checked; 404 not_found for a run the
// ledger has not; 400 invalid_request for a body that is not one; 503
// ledger_unavailable, and run_changing while a continuation is being made
// or when another Check again held s.rechecking past the request's time.
func (s *Server) handleRecheckTask(w http.ResponseWriter, r *http.Request) {
	runID, taskID := r.PathValue("run"), r.PathValue("task")
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id and a task id")
		return
	}
	var body recheckBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch {
	case !crew.CommandIDPattern.MatchString(body.ID):
		writeError(w, http.StatusBadRequest, "invalid_request", crew.ErrBadCommandID.Error())
		return
	case body.ExpectedRevision < 0:
		writeError(w, http.StatusBadRequest, "invalid_request", "expectedRevision is a revision of the run")
		return
	case body.Attempt < 1 || !fullCommit(body.Commit):
		writeError(w, http.StatusBadRequest, "invalid_request", "attempt is the candidate's attempt, 1 or more, and commit its commit in full")
		return
	}
	ld := s.receiptLedger(w)
	if ld == nil {
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	// One Check again at a time, from the chain's read to the command: a
	// resend that walked the chain while another request under its id
	// waited to send it to an older run (continued since, so refused
	// there) would else take it in the newest, one id then holding a
	// refusal in one run and a check in another.
	if f := recheckWaitSeam.Load(); f != nil && len(s.rechecking) == cap(s.rechecking) {
		(*f)(runID)
	}
	select {
	case s.rechecking <- struct{}{}:
		defer func() { <-s.rechecking }()
	case <-ctx.Done():
		// Nothing taken: another held the server for the whole of the
		// request's time.
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "run_changing", "another Check again was being sent; nothing was taken, ask again")
		return
	}
	if _, found, err := ld.Run(ctx, runID); err != nil {
		s.receiptError(w, "recheck: the run", runID, err)
		return
	} else if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	}
	nodes, _, _, err := s.runChain(ctx, runID)
	if err != nil {
		s.chainReadError(w, runID, err)
		return
	}
	forward := chainForward(nodes, runID)
	newest := forward[len(forward)-1]
	actor := actorOf(r)
	rq := recheckRequestFor(runID, newest, taskID, body.Attempt, body.Commit)
	// A resend: the run of the chain that took the command answers it.
	for _, run := range forward {
		c, ok, err := ld.Receipt(ctx, run, actor, body.ID)
		if err != nil {
			s.receiptError(w, "recheck: a receipt", run, err)
			return
		}
		if !ok {
			continue
		}
		rc, err := crew.RecheckRepeat(c, rq)
		s.writeRecheck(w, run, rc, err)
		return
	}
	if f := recheckSentSeam.Load(); f != nil {
		(*f)(newest)
	}
	if _, held := s.runs.Get(newest); !held {
		writeError(w, http.StatusConflict, crew.RefusalTaskState, "continue run "+newest+" first: the server runs no run of the chain to check it again in")
		return
	}
	rc, err := s.runs.Recheck(ctx, newest, actor, body.Command, rq)
	s.writeRecheck(w, newest, rc, err)
}

// recheckWaitSeam, when a test sets it, is called with its route's run by
// a Check again that finds another holding s.rechecking, before it waits;
// recheckSentSeam with the run a Check again is about to be sent to, once
// no receipt of its id was found along the chain.
var (
	recheckWaitSeam atomic.Pointer[func(run string)]
	recheckSentSeam atomic.Pointer[func(run string)]
)

// writeRecheck answers Check again as writeReceipt answers a command,
// with the run it went to and, taken, the attempt it made there (the
// receipt's task's attempts: the new one is the last).
func (s *Server) writeRecheck(w http.ResponseWriter, run string, rc crew.Receipt, err error) {
	switch {
	case rc.CommandID != "" && rc.Status == ledger.CommandFailed:
		writeJSON(w, http.StatusConflict, map[string]any{"error": apiError{Code: cmp.Or(rc.Refusal, crew.RefusalTaskState), Message: rc.Error}, "receipt": rc, "run": run})
	case err == nil:
		out := map[string]any{"receipt": rc, "run": run}
		if rc.Task != nil && rc.Status == ledger.CommandDone {
			out["attempt"] = rc.Task.Attempts
		}
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, crew.ErrStaleCandidate):
		writeError(w, http.StatusConflict, crew.RefusalStaleCandidate, err.Error())
	default:
		s.taskError(w, "recheck", run, err)
	}
}
