package api

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A run's candidates (Program item DS2, docs/plans/dispatch-evidence.md,
// PR-8, "A run's candidates"): each task's attempts that submitted
// something, with the commit each submitted and the receipts of the
// checks and verify steps that ran against that commit, read from the
// ledger, so a run the engine let go, or one a server before this one
// ran, still answers. A receipt is the attempt's row whose subject is the
// candidate's commit, so none is ever shown against a commit it did not
// check. A receipt says what ran, where, against which commit and how it
// ended, never how its process was made: no pid and no process group.

const (
	// MaxCandidatesReply bounds a page of a run's candidates: eight tasks,
	// six candidates each, sixteen receipts each (a task's eight checks
	// and its manifest's eight verify steps), every text at its bound and
	// escaped worst, encoded whole, is under it (dispatch_evidence_test.go).
	// Exported for the CLI.
	MaxCandidatesReply = 1 << 20
	// candidateTasksPage is a page's tasks, candidatesPerTask a task's
	// candidates on it (more counts the rest, which the task's own route
	// reads), maxCandidateReceipts a candidate's receipts.
	candidateTasksPage   = 8
	candidatesPerTask    = 6
	maxCandidateReceipts = 16
	// maxReceiptError bounds a receipt's error here, in characters (the
	// task's route has it whole).
	maxReceiptError = 120
	// candidateReadTries bounds the times a page's reads are made again
	// because the run's revision moved while they were made.
	candidateReadTries = 8
	// whereNative is where every check runs until a runtime other than
	// the native one exists (the Program, section 2.5, constraint 7): the
	// ledger's checks rows carry no where yet, and this is their value.
	whereNative = "native"
)

// candidateReceipt is one receipt of a candidate: a check's or a verify
// step's run against the candidate's commit. Exit is set once it
// finished; its log is the step log route's.
type candidateReceipt struct {
	Phase      string     `json:"phase"`
	ID         string     `json:"id"`
	K          int        `json:"k"`
	Where      string     `json:"where"`
	State      string     `json:"state"`
	Exit       *int       `json:"exit,omitempty"`
	Signal     string     `json:"signal,omitempty"`
	TimedOut   bool       `json:"timedOut"`
	Killed     bool       `json:"killed"`
	Stable     bool       `json:"stable"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	LogSHA256  string     `json:"logSha256,omitempty"`
	LogBytes   int64      `json:"logBytes"`
	LogDropped bool       `json:"logDropped"`
}

// candidateAccepted is the Accept anyway that took a candidate as its
// task's result: who, when, and the command's id.
type candidateAccepted struct {
	Actor   string    `json:"actor"`
	At      time.Time `json:"at"`
	Command string    `json:"command"`
}

// runCandidate is an attempt that submitted something: a work attempt's
// result commit (commit), or a review's verdict on the commit it reviewed
// (start), with its receipts. Standing says it is its task's result now,
// withdrawn that a withdrawal took it back; findings are a review's,
// counted by severity (their text is the task route's).
type runCandidate struct {
	Task           string             `json:"task"`
	Kind           string             `json:"kind"`
	Attempt        int                `json:"attempt"`
	Commit         string             `json:"commit,omitempty"`
	Start          string             `json:"start,omitempty"`
	State          string             `json:"state"`
	Stage          string             `json:"stage,omitempty"`
	Standing       bool               `json:"standing"`
	Withdrawn      bool               `json:"withdrawn"`
	AcceptedAnyway *candidateAccepted `json:"acceptedAnyway,omitempty"`
	Verdict        string             `json:"verdict,omitempty"`
	Findings       map[string]int     `json:"findings,omitempty"`
	Receipts       []candidateReceipt `json:"receipts"`
}

// taskCandidates is a task's candidates on a page: at most
// candidatesPerTask, newest first, more the rest.
type taskCandidates struct {
	Task       string         `json:"task"`
	Kind       string         `json:"kind"`
	Candidates []runCandidate `json:"candidates"`
	More       int            `json:"more"`
}

// candidateReceiptOf is a check row as a candidate's receipt.
func candidateReceiptOf(row ledger.CheckRow) candidateReceipt {
	rc := candidateReceipt{Phase: row.Phase, ID: row.ID, K: row.K, Where: whereNative, State: row.State, Signal: row.Signal, TimedOut: row.TimedOut,
		Killed: row.Killed, Stable: row.Stable, Error: cutRunes(row.Error, maxReceiptError), StartedAt: row.StartedAt,
		LogSHA256: row.LogSHA256, LogBytes: row.LogBytes, LogDropped: row.LogDropped}
	if rc.Phase == "" {
		rc.Phase = ledger.PhaseCheck
	}
	if row.State == ledger.CheckFinished {
		exit := row.Exit
		rc.Exit = &exit
	}
	if !row.FinishedAt.IsZero() {
		at := row.FinishedAt
		rc.FinishedAt = &at
	}
	return rc
}

// acceptances reads the run's done Accept anyway commands: the attempt
// each took, by task and attempt, the latest standing for one taken twice.
func acceptances(rows []ledger.CommandRow) map[string]map[int]candidateAccepted {
	out := map[string]map[int]candidateAccepted{}
	for _, c := range rows {
		if c.Intent != "accept" || c.Status != ledger.CommandDone {
			continue
		}
		var rc crew.Receipt
		if json.Unmarshal([]byte(c.Receipt), &rc) != nil || rc.Task == nil || rc.Task.Result == nil {
			continue
		}
		if out[rc.Task.ID] == nil {
			out[rc.Task.ID] = map[int]candidateAccepted{}
		}
		if prior, ok := out[rc.Task.ID][rc.Task.Result.Attempt]; !ok || !c.CreatedAt.Before(prior.At) {
			out[rc.Task.ID][rc.Task.Result.Attempt] = candidateAccepted{Actor: c.Actor, At: c.CreatedAt, Command: c.ID}
		}
	}
	return out
}

// candidatesSeam, when a test sets it, is called between a candidates
// page's reads of the ledger (its tasks', then its attempts'), so a test
// can commit a transition there.
var candidatesSeam atomic.Pointer[func(run string)]

// handleRunCandidates answers a page of a run's candidates: 200 {run,
// managed, base, candidates, next?}, candidates at most candidateTasksPage
// tasks in the crew's order from after the task ?after names, each with
// its candidates, next the page's last task when more follow; under
// MaxCandidatesReply. The run's tasks, attempts, receipts and commands are
// four reads, made again while the run's revision moved between the first
// and the last (at most candidateReadTries times), so a page pairs a task
// row with the attempts of its own revision; 503 run_changing (Retry-After
// 1) when the run moved through every try, never rows of two revisions. A
// run the ledger has not, or any run while the ledger cannot
// be read, is looked for as GET /api/runs/{run} looks: found, a legacy run answers
// {managed: false, candidates: []} (true for a managed run's record whose
// rows are gone); 404 not_found only for a run found nowhere with a ledger
// that could be read, or none; 503 ledger_unavailable for a managed run,
// or a run found nowhere, with no ledger to read; 400 invalid_request for
// an id that is not one, an after that is no task of the run, or any
// other parameter.
func (s *Server) handleRunCandidates(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run")
	if !crew.ValidRunID(runID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id")
		return
	}
	q, err := readQuery(r, "after")
	var after string
	if err == nil {
		after, err = q.one("after")
	}
	if err == nil && after != "" && !crew.ValidTaskID(after) {
		err = errors.New("after is a task id")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx := r.Context()
	var row ledger.RunRow
	found := false
	// unread is the ledger's error when it cannot be read (unavailable or
	// closed): a legacy run still answers, from the engine or its record.
	var unread error
	if s.ledger != nil {
		row, found, err = s.ledger.Run(ctx, runID)
		switch {
		case errors.Is(err, ledger.ErrUnavailable), errors.Is(err, ledger.ErrClosed):
			unread, found = err, false
		case err != nil:
			s.receiptError(w, "candidates", runID, err)
			return
		}
	}
	if !found {
		run := s.launchedRun(runID)
		switch {
		case unread != nil && (run == nil || run.Managed != nil):
			s.receiptError(w, "candidates", runID, unread)
		case run == nil:
			writeError(w, http.StatusNotFound, "not_found", "no such run")
		case s.ledger == nil && run.Managed != nil:
			writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened; see the server's log")
		default:
			writeDispatch(w, http.StatusOK, map[string]any{"run": runID, "managed": run.Managed != nil, "base": "", "candidates": []taskCandidates{}})
		}
		return
	}
	var c crew.Crew
	if err := json.Unmarshal([]byte(row.Crew), &c); err != nil {
		s.receiptError(w, "candidates: the run's crew", runID, err)
		return
	}
	from := 0
	if after != "" {
		i := slices.IndexFunc(c.Tasks, func(t crew.Task) bool { return t.ID == after })
		if i < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "after names no task of the run")
			return
		}
		from = i + 1
	}
	tasks := c.Tasks[from:min(from+candidateTasksPage, len(c.Tasks))]
	var (
		taskRows []ledger.TaskRow
		attempts []ledger.AttemptRow
		checks   []ledger.CheckRow
		commands []ledger.CommandRow
	)
	// The rows of one revision: read, then the revision looked at again,
	// and read again while it moved.
	for rev, try := row.Revision, 1; ; try++ {
		if taskRows, err = s.ledger.Tasks(ctx, runID); err != nil {
			s.receiptError(w, "candidates: tasks", runID, err)
			return
		}
		if f := candidatesSeam.Load(); f != nil {
			(*f)(runID)
		}
		if attempts, err = s.ledger.Attempts(ctx, runID); err != nil {
			s.receiptError(w, "candidates: attempts", runID, err)
			return
		}
		if checks, err = s.ledger.Checks(ctx, runID, "", 0, false); err != nil {
			s.receiptError(w, "candidates: receipts", runID, err)
			return
		}
		if commands, err = s.ledger.Commands(ctx, runID); err != nil {
			s.receiptError(w, "candidates: commands", runID, err)
			return
		}
		p, ok, perr := s.ledger.Pointer(ctx, runID)
		if perr != nil {
			s.receiptError(w, "candidates: the run's revision", runID, perr)
			return
		}
		// Pruned meanwhile, the rows read are the last it had.
		if !ok || p.Revision == rev {
			break
		}
		// Moved through every try: rows of two revisions are never
		// answered, the page is asked for again.
		if try == candidateReadTries {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "run_changing", "the run changed through every read of its candidates; ask again")
			return
		}
		rev = p.Revision
	}
	accepted := acceptances(commands)
	out := make([]taskCandidates, 0, len(tasks))
	for _, t := range tasks {
		tc := taskCandidates{Task: t.ID, Kind: t.Kind, Candidates: []runCandidate{}}
		var tr ledger.TaskRow
		if i := slices.IndexFunc(taskRows, func(r ledger.TaskRow) bool { return r.ID == t.ID }); i >= 0 {
			tr = taskRows[i]
			if tr.Kind != "" {
				tc.Kind = tr.Kind
			}
		}
		var submitted []ledger.AttemptRow
		for _, a := range attempts {
			if a.TaskID == t.ID && a.Result != "" {
				submitted = append(submitted, a)
			}
		}
		slices.SortFunc(submitted, func(a, b ledger.AttemptRow) int { return b.N - a.N })
		if len(submitted) > candidatesPerTask {
			tc.More = len(submitted) - candidatesPerTask
			submitted = submitted[:candidatesPerTask]
		}
		for _, a := range submitted {
			tc.Candidates = append(tc.Candidates, candidateOf(tc.Kind, tr, a, checks, accepted[t.ID]))
		}
		out = append(out, tc)
	}
	reply := map[string]any{"run": runID, "managed": true, "base": row.Base, "candidates": out}
	if from+len(tasks) < len(c.Tasks) {
		reply["next"] = tasks[len(tasks)-1].ID
	}
	writeDispatch(w, http.StatusOK, reply)
}

// candidateOf is the attempt a as a candidate of its task (its row tr):
// its commit (a work attempt's result; a review's is the start it
// reviewed), whether it stands, whether a withdrawal took it back, the
// Accept anyway that took it, a review's verdict and findings counted,
// and the receipts of its rows whose subject is that commit.
func candidateOf(kind string, tr ledger.TaskRow, a ledger.AttemptRow, checks []ledger.CheckRow, accepted map[int]candidateAccepted) runCandidate {
	var res crew.Result
	_ = json.Unmarshal([]byte(a.Result), &res)
	c := runCandidate{Task: a.TaskID, Kind: kind, Attempt: a.N, Start: a.StartCommit, State: a.State, Stage: a.Stage,
		Standing: tr.ResultAttempt == a.N && tr.ImportedFrom == "", Withdrawn: a.N <= tr.Withdrawn, Receipts: []candidateReceipt{}}
	subject := a.StartCommit
	if kind != crew.TaskReview {
		c.Commit = cmp.Or(a.ResultCommit, res.Commit)
		subject = c.Commit
	} else {
		c.Verdict = res.Verdict
		c.Findings = map[string]int{}
		for _, f := range res.Findings {
			c.Findings[f.Severity]++
		}
	}
	if acc, ok := accepted[a.N]; ok {
		c.AcceptedAnyway = &acc
	}
	for _, row := range checks {
		if row.TaskID != a.TaskID || row.N != a.N || subject == "" || row.Subject != subject {
			continue
		}
		if row.Phase != "" && row.Phase != ledger.PhaseCheck && row.Phase != ledger.PhaseVerify {
			continue
		}
		if len(c.Receipts) == maxCandidateReceipts {
			break
		}
		c.Receipts = append(c.Receipts, candidateReceiptOf(row))
	}
	return c
}
