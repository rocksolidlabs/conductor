package crew

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A continuation (docs/plans/agent-harness-order.md, decision 9; row 8 of
// #334): a managed run the server no longer runs (interrupted by the
// server's end, stopped, or finished) goes on as a new run of the same
// crew as launched, from the same base. The results that stood are
// imported by reference, the attempts spent are counted against their
// tasks, a result that was being verified (when the server ended, or the
// run was stopped) is verified again without a session, and the rest is
// dispatched as new. An attempt whose launch was
// never known (uncertain) waits for a person: its task fails with why, and
// a retry allows one more attempt. Nothing is retried on its own.

// Loaded is a run as the ledger has it, for a continuation, for the record
// written after a recovery and for a task read of a run the engine does
// not hold. Prerequisites are its prerequisites' receipts, without their
// logs. Imported are the attempt rows its tasks' results were imported
// from by reference, by the reference (run/task/n), read from the runs
// that hold them (LoadImports): empty unless read.
type Loaded struct {
	Run           ledger.RunRow
	Tasks         []ledger.TaskRow
	Attempts      []ledger.AttemptRow
	Messages      []ledger.MessageRow
	Prerequisites []ledger.PrerequisiteRow
	Imported      map[string]ledger.AttemptRow
}

// LoadImports reads into from.Imported the attempt rows of the results its
// tasks imported by reference (a continuation's: the row stays in the run
// that made it, which the task row's ImportedFrom names), so a task read
// from the ledger says what such a result's attempt submitted, as the run
// reports it while the engine holds it. A run the ledger has not is passed
// over (the result reads with its commit alone); a read that fails is the
// error.
func LoadImports(ctx context.Context, l Ledger, from *Loaded) error {
	runs := map[string][]ledger.AttemptRow{}
	for _, row := range from.Tasks {
		runID, taskID, n, ok := parseImport(row.ImportedFrom)
		if !ok || taskID != row.ID || n != row.ResultAttempt || runID == from.Run.ID {
			continue
		}
		attempts, read := runs[runID]
		if !read {
			src, found, err := l.Load(ctx, runID)
			if err != nil {
				return fmt.Errorf("the ledger could not read run %s: %w", runID, err)
			}
			if found {
				attempts = src.Attempts
			}
			runs[runID] = attempts
		}
		if ra := byN(attempts, taskID, n); ra.N == n {
			if from.Imported == nil {
				from.Imported = map[string]ledger.AttemptRow{}
			}
			from.Imported[row.ImportedFrom] = ra
		}
	}
	return nil
}

// candidate is a result a continuation verifies again: the interrupted
// attempt's number, commit and start, and what it submitted.
type candidate struct {
	n      int
	commit string
	start  string
	result Result
}

// ContinueRun makes the run that continues runID, a managed run the ledger
// has: interrupted, stopped or finished, and not continued yet. The new run
// names the old in ResumedFrom (the ledger's ContinuedFrom) and the old
// names it in ResumedBy (ContinuedBy). A run still going is refused
// (ErrRunRunning), one continued already with RunResumedError. The label
// is the old run's when none is given and the engine still holds it.
func (e *Engine) ContinueRun(ctx context.Context, runID, label string) (*Run, error) {
	if e.ledger == nil {
		return nil, ErrNoLedger
	}
	if err := ValidateLabel(label); err != nil {
		return nil, err
	}
	e.flushPending(ctx, runID)
	old, label, err := e.markContinuing(runID, label)
	if err != nil {
		return nil, err
	}
	if e.beforeContinue != nil {
		e.beforeContinue(runID)
	}
	undo := func() {
		if old != nil {
			e.mu.Lock()
			old.resuming = false
			e.mu.Unlock()
		}
	}
	from, ok, err := e.ledger.Load(ctx, runID)
	if err != nil {
		undo()
		return nil, err
	}
	if !ok {
		undo()
		return nil, ErrRunNotFound
	}
	if from.Run.ContinuedBy != "" {
		undo()
		return nil, &RunResumedError{By: from.Run.ContinuedBy}
	}
	if from.Run.State == ledger.RunRunning {
		undo()
		return nil, ErrRunRunning
	}
	var c Crew
	if err := json.Unmarshal([]byte(from.Run.Crew), &c); err != nil {
		undo()
		return nil, fmt.Errorf("the run's crew as launched cannot be read: %w", err)
	}
	if !c.Managed() {
		undo()
		return nil, fmt.Errorf("%w: the run's crew as launched has no tasks", ErrTaskState)
	}
	h, err := e.readHistory(ctx, from)
	if err != nil {
		undo()
		return nil, err
	}
	// A run launched from a ticket goes on with its package: the same
	// bytes, the same ticket, never a new launch.
	pkg, err := packageOfRow(from.Run)
	if err != nil {
		undo()
		return nil, err
	}
	// Prepared at its own base, never at the directory's HEAD: the
	// commit its manifest and its prerequisites are read at.
	p, err := e.prepare(ctx, c, from.Run.Base)
	if err != nil {
		undo()
		return nil, err
	}
	// Its id is minted against the runs the engine holds; one the ledger
	// has (a planned run's) is refused by the continuation's first
	// transaction before anything is made, and another is minted.
	var r *run
	for try := 1; ; try++ {
		r, _ = e.addRun(c, p, label, "", pkg)
		if e.afterAdd != nil {
			e.afterAdd(r.id)
		}
		err := e.startContinued(ctx, r, from, h, old)
		if err == nil {
			break
		}
		e.abort(r)
		e.launched(r)
		if minted(err, try) {
			continue
		}
		undo()
		return nil, err
	}
	defer e.launched(r)
	out, ok := e.Get(r.id)
	if !ok {
		return nil, ErrRunNotFound
	}
	return &out, nil
}

// flushPending writes what a run the engine holds has kept for the ledger (the
// rows and events of changes whose commits failed), so a continuation
// reads it whole; markContinuing refuses one that still has some. A run
// stopped before its launch was recorded, its stop refused by the ledger,
// holds that stop: the run's first transaction (transactFirst), written
// now as one, its row first through CreateRun, so an id the ledger has
// is refused still and the run, another's, let go (dropForeign). Nothing
// else of a run whose launch is not recorded is written (errLaunching):
// it has nothing pending, its launch's refused commit dropped with it.
func (e *Engine) flushPending(ctx context.Context, runID string) {
	e.mu.Lock()
	r := e.runs[runID]
	first := r != nil && r.unrecorded() && r.stoppedAt != nil
	e.mu.Unlock()
	if r == nil {
		return
	}
	if first {
		if err := e.transactFirst(ctx, r, func() tx { return tx{} }, nil); errors.Is(err, ErrRunExists) {
			e.dropForeign(r)
		}
		return
	}
	_ = e.transact(ctx, r, func() tx { return tx{} }, nil)
}

// markContinuing marks the run the engine holds as being continued
// (resuming), or refuses it: legacy, still going, continued already or
// being so, or holding changes the ledger has not taken (ErrRunUnsettled). The mark is made under the run's txMu: a change of it under
// way commits first, and every later one sees the mark and refuses
// (ErrRunContinued), so the rows a continuation reads are the run's last.
// A run the engine does not hold (old nil) is the ledger's to judge. The
// label is the run's when none is given.
func (e *Engine) markContinuing(runID, label string) (*run, string, error) {
	e.mu.Lock()
	old := e.runs[runID]
	e.mu.Unlock()
	if old == nil {
		return nil, label, nil
	}
	old.txMu.Lock()
	defer old.txMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runs[runID] != old {
		return nil, "", ErrRunNotFound
	}
	switch {
	case old.dirty():
		return nil, "", ErrRunUnsettled
	case !old.managed:
		return nil, "", fmt.Errorf("%w: a legacy run is resumed, not continued", ErrTaskState)
	case old.stopping && !old.stopDone:
		return nil, "", ErrRunStopped
	case old.stoppedAt == nil && old.outcome == "":
		return nil, "", ErrRunRunning
	case old.resumedBy != "":
		return nil, "", &RunResumedError{By: old.resumedBy}
	case old.resuming:
		return nil, "", &RunResumedError{}
	}
	old.resuming = true
	if label == "" {
		label = old.label
	}
	return old, label, nil
}

// history is what a continuation takes from the runs it continues: the
// source's task rows, each task's last attempt kept and counted (along
// ContinuedFrom when the source made none), the attempt after it whose
// worktree could not be made usable when the task's last is one
// (unusable), whether such an attempt refused the last one's checkpoint
// (refusedCP), and the imported results with where each was found.
type history struct {
	rows      map[string]ledger.TaskRow
	last      map[string]ledger.AttemptRow
	unusable  map[string]ledger.AttemptRow
	refusedCP map[string]bool
	refs      map[string]ledger.AttemptRow
	refOf     map[string]string
}

// uncountedRow reports whether an attempt's row is one not counted: its
// worktree could not be made usable (steps.go), failed with no result at a
// stage of the project manifest, or, for a run launched from a ticket, it
// could not start as its plan revision approved, at "package" or "agent"
// (launchStage). A continuation passes over it as the run that made it
// did.
func uncountedRow(a ledger.AttemptRow) bool {
	return a.State == ledger.AttemptFailed && a.ResultCommit == "" && (manifestStage(a.Stage) || launchStage(a.Stage))
}

// manifestStage reports whether stage is a project manifest's: one of its
// phases' steps, "ready" for the tree's readiness not recorded (treeReady:
// its tree.ready refused), or "manifest" for one that could not be
// resolved. A checkout's "ready" (its ready phase changed tracked files)
// is a result's, which its row carries, so uncountedRow never takes it.
func manifestStage(stage string) bool {
	return stage == "manifest" || stage == "ready" || strings.HasPrefix(stage, "setup ") || strings.HasPrefix(stage, "service ") || strings.HasPrefix(stage, "ready ")
}

// readHistory reads the history a continuation of from takes, before the
// continuation is made: a run the ledger cannot read refuses it with
// nothing made.
func (e *Engine) readHistory(ctx context.Context, from Loaded) (history, error) {
	rows := map[string]ledger.TaskRow{}
	for _, row := range from.Tasks {
		rows[row.ID] = row
	}
	// An attempt is a task's last only when a withdrawal has not taken it
	// back, and when it is within the attempts the task's row counts: the
	// history the continued run had (a ledger whose ancestor was changed
	// after it was continued has attempts past it).
	usable := func(a ledger.AttemptRow) bool {
		row := rows[a.TaskID]
		return a.N > row.Withdrawn && a.N <= row.Attempts
	}
	// last is each task's highest counted attempt, top its highest, and
	// uncounted its attempts whose worktree could not be made usable.
	last, top := map[string]ledger.AttemptRow{}, map[string]ledger.AttemptRow{}
	uncounted := map[string][]ledger.AttemptRow{}
	consider := func(a ledger.AttemptRow) {
		if !usable(a) {
			return
		}
		if a.N > top[a.TaskID].N {
			top[a.TaskID] = a
		}
		if uncountedRow(a) {
			uncounted[a.TaskID] = append(uncounted[a.TaskID], a)
			return
		}
		if a.N > last[a.TaskID].N {
			last[a.TaskID] = a
		}
	}
	for _, a := range from.Attempts {
		consider(a)
	}
	// The results imported by reference: from this run's rows, or from the
	// run an earlier continuation imported them from (the reference is
	// kept as it was). Read before the transaction: the ledger's one
	// connection serves no reader inside one. A run that cannot be read,
	// here or in the walk back below, ends the continuation before it is
	// made (readErr): read without it,
	// a task could start at the wrong commit, and the link made would
	// leave no second try once the ledger reads again.
	var readErr error
	sources := map[string]Loaded{from.Run.ID: from}
	source := func(ref string) (ledger.AttemptRow, bool) {
		runID, taskID, n, ok := parseImport(ref)
		if !ok {
			return ledger.AttemptRow{}, false
		}
		src, ok := sources[runID]
		if !ok {
			loaded, found, err := e.ledger.Load(ctx, runID)
			if err != nil {
				readErr = cmp.Or(readErr, fmt.Errorf("the ledger could not read run %s: %w", runID, err))
			}
			if err != nil || !found {
				return ledger.AttemptRow{}, false
			}
			sources[runID] = loaded
			src = loaded
		}
		row := byN(src.Attempts, taskID, n)
		return row, row.N == n
	}
	// A task the run made no attempt at (continued while paused, and
	// stopped again) takes its last attempt from the run it continued,
	// and so back: attempt numbers run on across continuations, so the
	// highest is the latest, and with it its checkpoint, its refusal or
	// its candidate.
	seen := map[string]bool{from.Run.ID: true}
	for prevID := from.Run.ContinuedFrom; prevID != "" && !seen[prevID]; {
		lacking := false
		for _, row := range from.Tasks {
			if _, ok := last[row.ID]; !ok && row.Attempts > row.Withdrawn {
				lacking = true
				break
			}
		}
		if !lacking {
			break
		}
		seen[prevID] = true
		prev, found, err := e.ledger.Load(ctx, prevID)
		if err != nil {
			return history{}, fmt.Errorf("the ledger could not read run %s: %w", prevID, err)
		}
		if !found {
			break
		}
		sources[prevID] = prev
		for _, a := range prev.Attempts {
			consider(a)
		}
		prevID = prev.Run.ContinuedFrom
	}
	// A result's attempt, found where the row says, or else in this run
	// or back along ContinuedFrom: the reference recorded is where it was
	// found. One found nowhere is not imported (the task fails, for a
	// person to retry), never made a success with nothing behind it.
	find := func(row ledger.TaskRow) (string, ledger.AttemptRow, bool) {
		if _, taskID, n, ok := parseImport(row.ImportedFrom); ok && taskID == row.ID && n == row.ResultAttempt {
			if ra, ok := source(row.ImportedFrom); ok {
				return row.ImportedFrom, ra, true
			}
		}
		seen := map[string]bool{}
		for runID := from.Run.ID; runID != "" && !seen[runID]; {
			seen[runID] = true
			ref := runID + "/" + row.ID + "/" + strconv.Itoa(row.ResultAttempt)
			if ra, ok := source(ref); ok {
				return ref, ra, true
			}
			src, ok := sources[runID]
			if !ok {
				break
			}
			runID = src.Run.ContinuedFrom
		}
		return "", ledger.AttemptRow{}, false
	}
	refs := map[string]ledger.AttemptRow{}
	refOf := map[string]string{}
	for _, row := range from.Tasks {
		if row.State != ledger.TaskSucceeded || row.ResultAttempt == 0 || row.ResultAttempt <= row.Withdrawn {
			continue
		}
		if ref, ra, ok := find(row); ok {
			refs[row.ID], refOf[row.ID] = ra, ref
		}
	}
	if readErr != nil {
		return history{}, readErr
	}
	// A task whose last attempt could not be made usable: the next starts
	// where it did, and the last counted one's checkpoint is refused when
	// an attempt after it, made there, could not be made usable.
	unusable, refusedCP := map[string]ledger.AttemptRow{}, map[string]bool{}
	for id, t := range top {
		la, hasLast := last[id]
		if uncountedRow(t) && (!hasLast || t.N > la.N) {
			unusable[id] = t
		}
		if !hasLast || la.Checkpoint == "" {
			continue
		}
		if cp := decodeCheckpoint(la.Checkpoint); cp != nil && cp.commit != la.StartCommit {
			for _, u := range uncounted[id] {
				// Only the manifest's failure refuses the checkpoint's
				// tree; a package or an agent is no fault of it.
				if u.N > la.N && u.StartCommit == cp.commit && manifestStage(u.Stage) {
					refusedCP[id] = true
				}
			}
		}
	}
	return history{rows: rows, last: last, unusable: unusable, refusedCP: refusedCP, refs: refs, refOf: refOf}, nil
}

// startContinued sets r up from the run it continues and records it: the
// base and the pause carried, every task as its row and its last attempt
// say (imported, failed, cancelled, verified again, waiting for a person,
// or new), the old run's row naming the new; then the ready tasks are
// dispatched.
func (e *Engine) startContinued(ctx context.Context, r *run, from Loaded, h history, old *run) error {
	rows, last, refs, refOf := h.rows, h.last, h.refs, h.refOf
	var imported, again, uncertain, held int
	err := e.transactFirst(ctx, r, func() tx {
		r.base, r.paused, r.pausedWhy, r.resumedFrom = from.Run.Base, from.Run.Paused, from.Run.PausedWhy, from.Run.ID
		r.markRun()
		// The prerequisites run again in the continuation, at its base,
		// once this is committed: a host fixed since is seen.
		r.armPrerequisites()
		for _, row := range from.Messages {
			m := &message{seq: row.Seq, from: row.From, to: row.To, text: row.Text, createdAt: row.CreatedAt, typingAt: row.TypingAt, deliveredAt: row.DeliveredAt, ackedAt: row.AckedAt}
			if !row.TypingAt.IsZero() && row.DeliveredAt.IsZero() {
				// Set out to be typed and not entered before the server
				// ended: whether it reached the terminal cannot be told.
				m.uncertain = true
				held++
				r.note(session.ActivityStatus, "message %d from %s to %s may have been typed before the server ended: not typed again, in %s's inbox", m.seq, m.from, m.to, m.to)
			}
			r.messages = append(r.messages, m)
		}
		for _, t := range r.tasks {
			row, ok := rows[t.def.ID]
			if !ok {
				r.markTask(t)
				continue
			}
			t.prior, t.extra, t.withdrawn = row.Attempts, row.Extra, row.Withdrawn
			if rp := decodeRepair(row.Repair); rp != nil {
				t.repair = rp
			}
			la, hasLast := last[t.def.ID]
			if hasLast && la.Checkpoint != "" && t.def.Kind == TaskWork && !h.refusedCP[t.def.ID] {
				if t.checkpoint = decodeCheckpoint(la.Checkpoint); t.checkpoint != nil {
					t.checkpoint.n, t.checkpoint.start, t.checkpoint.branch = la.N, la.StartCommit, la.Branch
				}
			}
			if ua, ok := h.unusable[t.def.ID]; ok {
				// The last attempt could not be made usable: the next starts
				// where it started, as if it had not been made, or where the
				// checkpointed attempt did when it refused the checkpoint.
				u := &unusable{n: ua.N, stage: ua.Stage, err: ua.Error, next: ua.StartCommit}
				if hasLast {
					u.prev = &TaskPrevious{Attempt: la.N, State: la.State, Stage: la.Stage, Error: la.Error}
					u.prevResult = la.ResultCommit
					if la.ResultCommit != "" && la.ResultCommit != ua.StartCommit {
						u.prev.Result, u.prev.Branch = la.ResultCommit, la.Branch
					}
					if h.refusedCP[t.def.ID] {
						if cp := decodeCheckpoint(la.Checkpoint); cp != nil {
							u.checkpoint, u.branch, u.checkpointN, u.next = cp.commit, la.Branch, la.N, la.StartCommit
						}
					}
				}
				t.unusable = u
			}
			if hasLast && t.def.Kind == TaskWork && la.State == ledger.AttemptFailed && la.ResultCommit != "" && la.StartCommit != "" && refusedBeforeChecks(la.Stage) {
				t.refused = &refusal{n: la.N, start: la.StartCommit, stage: la.Stage, err: la.Error, result: la.ResultCommit, branch: la.Branch}
			}
			ra, found := refs[t.def.ID]
			switch {
			case row.State == ledger.TaskSucceeded && row.ResultAttempt > 0 && row.ResultAttempt <= row.Withdrawn:
				// A result a withdrawal took back (a version 3 ledger,
				// migrated): as new.
				t.state, t.reason = ledger.TaskBlocked, ""
				if len(t.def.Needs) == 0 {
					t.state = ledger.TaskReady
				}
			case row.State == ledger.TaskSucceeded && !found:
				t.state, t.reason = ledger.TaskFailed, fmt.Sprintf("its result's attempt %d is not in the ledger: retry it", row.ResultAttempt)
			case row.State == ledger.TaskSucceeded:
				res := decodeResult(ra.Result)
				at := ra.ClosedAt
				if at.IsZero() {
					at = time.Now().UTC()
				}
				t.result = &taskResult{attempt: row.ResultAttempt, commit: row.ResultCommit, summary: res.Summary, issues: res.Issues, verdict: res.Verdict, findings: res.Findings, at: at, branch: ra.Branch}
				if t.def.Kind == TaskReview {
					// The review's candidate, the source attempt's start: a
					// dependent starts there in the continuation too (#351).
					t.result.reviewed = ra.StartCommit
				}
				if t.result.verdict == "" {
					t.result.verdict = row.Verdict
				}
				t.imported = refOf[t.def.ID]
				t.state, t.reason = ledger.TaskSucceeded, ""
				imported++
			case row.State == ledger.TaskFailed:
				t.state, t.reason = ledger.TaskFailed, row.Reason
			case row.State == ledger.TaskCancelled:
				t.state, t.reason = ledger.TaskCancelled, "cancelled"
			default:
				switch {
				case hasLast && la.State == ledger.AttemptUncertain:
					t.state, t.reason = ledger.TaskFailed, fmt.Sprintf("attempt %d's launch is uncertain (the server stopped before it was known): retry it to go on", la.N)
					uncertain++
				case hasLast && (la.State == ledger.AttemptInterrupted || la.State == ledger.AttemptStopped) && t.def.Kind == TaskWork && la.ResultCommit != "" && la.Result != "" && strings.HasPrefix(la.Stage, "verifying"):
					// Its result was being verified when the server ended or
					// the run was stopped: verified again, the work kept.
					t.candidate = &candidate{n: la.N, commit: la.ResultCommit, start: la.StartCommit, result: decodeResult(la.Result)}
					t.state, t.reason = ledger.TaskReady, fmt.Sprintf("attempt %d's result %s is verified again", la.N, short(la.ResultCommit))
					again++
				default:
					t.state, t.reason = ledger.TaskBlocked, ""
					if len(t.def.Needs) == 0 {
						t.state = ledger.TaskReady
					}
				}
			}
			r.markTask(t)
		}
		r.ready()
		e.settleRun(r)
		r.note(session.ActivityStatus, "continues %s: %d results kept, %d verified again, %d waiting for a decision, %d messages carried", from.Run.ID, imported, again, uncertain, len(r.messages))
		w := ev("run.continued", map[string]any{"from": from.Run.ID, "base": r.base, "imported": imported, "reverified": again, "uncertain": uncertain, "messages": len(r.messages), "held": held})
		w.more = func(ltx LedgerTx) error {
			if err := ltx.Continued(from.Run.ID); err != nil {
				return err
			}
			// The messages carried, under this run: a later continuation
			// reads them here.
			for _, m := range r.messages {
				if err := ltx.PutMessage(r.messageRow(m)); err != nil {
					return err
				}
			}
			return nil
		}
		return w
	}, giveUp)
	if errors.Is(err, ledger.ErrContinued) {
		// Continued meanwhile, by another server or request: the one that
		// was is the continuation.
		by := ""
		if again, ok, lerr := e.ledger.Load(ctx, from.Run.ID); lerr == nil && ok {
			by = again.Run.ContinuedBy
		}
		return &RunResumedError{By: by}
	}
	if err != nil {
		return err
	}
	if old != nil {
		e.mu.Lock()
		old.resumedBy = r.id
		old.resuming = false
		old.note(session.ActivityStatus, "continued as %s", r.id)
		old.touch()
		e.mu.Unlock()
	}
	e.pumpLocked(r)
	return nil
}

// parseImport reads an import reference, "<run>/<task>/<n>".
func parseImport(ref string) (runID, taskID string, n int, ok bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 1 || !ValidRunID(parts[0]) || !ValidTaskID(parts[1]) {
		return "", "", 0, false
	}
	return parts[0], parts[1], n, true
}

// decodeRepair reads a task row's repair; nil for an empty or unreadable
// one.
func decodeRepair(raw string) *repair {
	var rp TaskRepair
	if raw == "" || json.Unmarshal([]byte(raw), &rp) != nil {
		return nil
	}
	return &repair{review: rp.Review, attempt: rp.Attempt, commit: rp.Commit, summary: rp.Summary, findings: rp.Findings}
}

// byN is the attempt row of the task numbered n, or a zero row.
func byN(attempts []ledger.AttemptRow, taskID string, n int) ledger.AttemptRow {
	for _, a := range attempts {
		if a.TaskID == taskID && a.N == n {
			return a
		}
	}
	return ledger.AttemptRow{}
}

// decodeResult reads an attempt row's result; an empty or unreadable one
// is empty.
func decodeResult(raw string) Result {
	var res Result
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &res)
	}
	return res
}

// reverify makes the attempt that verifies t's candidate again: no
// session, the candidate's commit its result and the start of the work
// its start (where a next attempt goes back to when the candidate is
// refused before its checks, refusedBeforeChecks), not counted against
// the task; the verification follows its record
// (dispatchCandidate). The caller holds e.mu, in the pump.
func (e *Engine) reverify(r *run, t *task, m *member) {
	c := *t.candidate
	t.candidate = nil
	ctx, cancel := context.WithCancel(r.ctx)
	n := len(t.attempts) + t.prior + 1
	start := c.start
	if start == "" {
		start = c.commit
	}
	a := &attempt{t: t, m: m, n: n, state: ledger.AttemptReserved, ctx: ctx, cancel: cancel, reservedAt: time.Now().UTC(), inputs: r.inputs(t), start: start, provisional: true,
		worktree: filepath.Join(r.cwd, worktreesDir, r.id, fmt.Sprintf("%s.%d", t.def.ID, n))}
	t.attempts = append(t.attempts, a)
	t.extra++
	t.state, t.reason = ledger.TaskRunning, ""
	r.active++
	r.markTask(t)
	r.markAttempt(a)
	r.starts.Add(1)
	r.note(session.ActivityStatus, "task %s: attempt %d verifies again the result %s of attempt %d", t.def.ID, n, short(c.commit), c.n)
	go e.dispatchCandidate(ctx, r, a, c)
}

// dispatchCandidate records a as verifying the candidate's result, then
// verifies it (verify), as a submitted result is after its record.
func (e *Engine) dispatchCandidate(ctx context.Context, r *run, a *attempt, c candidate) {
	defer r.starts.Done()
	t := a.t
	res := c.result
	err := e.transact(ctx, r, func() tx {
		if !a.closedAt.IsZero() {
			return tx{}
		}
		a.provisional = false
		a.launchedAt = time.Now().UTC()
		a.result, a.submitted, a.state = c.commit, &res, ledger.AttemptVerifying
		t.state = ledger.TaskVerifying
		r.markAttempt(a)
		r.markTask(t)
		return ev("result.reverified", map[string]any{"task": t.def.ID, "attempt": a.n, "commit": c.commit, "of": c.n})
	}, func() {
		a.provisional, a.result, a.submitted, a.state = true, "", nil, ledger.AttemptReserved
		t.state = ledger.TaskRunning
	})
	if err != nil {
		_ = e.transact(context.Background(), r, func() tx {
			r.unreserve(a, "the ledger could not record the attempt: "+session.CleanMessage(err.Error()))
			return pausedTx(e.pauseDispatch(r, err), err)
		}, nil)
		return
	}
	e.mu.Lock()
	closed := !a.closedAt.IsZero() || r.stopping
	if !closed {
		r.checks.Add(1)
	}
	e.mu.Unlock()
	if closed {
		return // cancelled, or the stop closes it
	}
	e.verify(r, a)
}

// Recovered is rec, a managed run's record, as the ledger has the run:
// the run's state (interrupted when the recovery found it running), each
// task as its row and its last attempt say (an attempt interrupted or
// uncertain leaves its task ready or failed with why), the members'
// sessions ended with the server. The server writes it in place of the
// record the run last saved, which may lag the ledger (a snapshot cut
// short by the server's end) or be missing (rec empty): then the run is
// built from the crew as launched and the manifest its row keeps, its log
// empty. Its prerequisites are read from their receipts
// (recoveredPrerequisites), whatever the record said of them, and nothing
// is cleaning: a record that said otherwise at the ledger's revision is
// written again too (UnderWay).
func Recovered(rec Run, from Loaded) Run {
	if rec.ID == "" {
		rec = Run{ID: from.Run.ID, CrewID: from.Run.CrewID, StartedAt: from.Run.CreatedAt, State: RunRunning}
		var c Crew
		if err := json.Unmarshal([]byte(from.Run.Crew), &c); err == nil {
			rec.Name, rec.Goal, rec.Cwd, rec.Isolation, rec.Yolo = c.Name, c.Goal, c.Cwd, c.Isolation, c.Yolo != nil && *c.Yolo
			for _, m := range c.Members {
				rec.Members = append(rec.Members, MemberState{Name: m.Name, AgentID: m.AgentID, Start: m.Start, Status: MemberPending})
			}
			for _, t := range c.Tasks {
				rec.Tasks = append(rec.Tasks, TaskState{ID: t.ID, Member: t.Member, Kind: t.Kind, Needs: slices.Clone(t.Needs), State: ledger.TaskBlocked})
			}
			rec.Manifest = launchedManifest(c, from.Run.Manifest)
		}
		rec.Ticket = ticketOfRow(from.Run)
		if !from.Run.ClosedAt.IsZero() && from.Run.State == ledger.RunStopped {
			at := from.Run.ClosedAt
			rec.StoppedAt = &at
		}
	}
	if rec.Ticket == nil {
		// Its input, which the row keeps: a record written before the
		// run's binding was read says it too.
		rec.Ticket = ticketOfRow(from.Run)
	}
	rec.ResumedBy = from.Run.ContinuedBy
	if from.Run.ContinuedFrom != "" {
		rec.ResumedFrom = from.Run.ContinuedFrom
	}
	switch from.Run.State {
	case ledger.RunInterrupted:
		rec.State = RunInterrupted
	case ledger.RunStopped:
		rec.State = RunStopped
	case ledger.RunFinished:
		rec.State = RunFinished
	}
	rec.Managed = &Managed{Revision: from.Run.Revision}
	rec.Paused, rec.PausedWhy, rec.Outcome, rec.NeedsInput = from.Run.Paused, from.Run.PausedWhy, from.Run.Outcome, 0
	// Nothing runs after the server's end: a tree it left ending is the
	// recovery's (ledger.Recover), not the run's, and so is a prerequisite
	// it left running, which the recovery interrupted. The prerequisites
	// are as their receipts say, not as a record that lags them.
	rec.Cleaning = 0
	if m := rec.Manifest; m != nil {
		read := *m
		read.Prerequisites = recoveredPrerequisites(m.Prerequisites, from.Prerequisites, from.Run.State == ledger.RunRunning)
		rec.Manifest = &read
	}
	rows := map[string]ledger.TaskRow{}
	for _, row := range from.Tasks {
		rows[row.ID] = row
	}
	for i := range rec.Tasks {
		t := &rec.Tasks[i]
		row, ok := rows[t.ID]
		taskStateOfRow(t, row, ok, from.Attempts, from.Imported)
	}
	now := time.Now().UTC()
	for i := range rec.Members {
		m := &rec.Members[i]
		if m.Status == MemberRunning || m.Status == MemberStarting {
			m.Status, m.NeedsInput = MemberEnded, false
			if m.Ended == nil {
				m.Ended = &now
			}
		}
	}
	return rec
}

// UnderWay reports whether rec, a managed run's record, says something of
// the run is under way that only the server running it can be doing: a
// tree being ended, or a prerequisite running. A record at the ledger's
// revision can say so: the journal can write it between a commit and the
// end of what that commit finished (a tree's tree.ended and the drop of
// its count, a generation's last row and its end), and the server can end
// before the next. Nothing runs after the server's end, so the server's
// start writes such a record again as the ledger has it (Recovered), as it
// does one that lags (Codex's review of #422, round 2).
func UnderWay(rec Run) bool {
	if rec.Cleaning != 0 {
		return true
	}
	if m := rec.Manifest; m != nil {
		for _, p := range m.Prerequisites {
			if p.State == "running" {
				return true
			}
		}
	}
	return false
}

// recoveredPrerequisites is a run's prerequisites as their receipts say
// once nothing runs (after the server's end), in the order listed (the
// record's, which is the manifest's): each with its latest receipt's k,
// and the state of that receipt when the generation that ran last reached
// it (passed or failed with its exit, words and time; interrupted), else
// pending. Generations go one at a time and each begins with the first
// prerequisite, so the last began with the first's latest receipt, and a
// receipt that started before it is an earlier generation's. A receipt
// still open in a run the ledger has running is running.
func recoveredPrerequisites(listed []PrerequisiteState, rows []ledger.PrerequisiteRow, runRunning bool) []PrerequisiteState {
	latest := map[string]ledger.PrerequisiteRow{}
	for _, row := range rows {
		if row.K > latest[row.ID].K {
			latest[row.ID] = row
		}
	}
	var since time.Time
	if len(listed) > 0 {
		since = latest[listed[0].ID].StartedAt
	}
	var out []PrerequisiteState
	for _, p := range listed {
		ps := PrerequisiteState{ID: p.ID, State: "pending"}
		row, ok := latest[p.ID]
		if ok {
			ps.K = row.K
		}
		switch {
		case !ok || row.StartedAt.Before(since):
		case row.State == ledger.CheckFinished:
			exit := row.Exit
			ps.Exit, ps.Error, ps.Duration = &exit, row.Error, row.FinishedAt.Sub(row.StartedAt).Milliseconds()
			ps.State = "passed"
			if row.Error != "" {
				ps.State = "failed"
			}
		case row.State == ledger.CheckInterrupted || !runRunning:
			ps.State, ps.Error = "interrupted", row.Error
		default:
			ps.State = "running"
		}
		out = append(out, ps)
	}
	return out
}

// launchedManifest is the project manifest of a run whose record is
// missing, as the run's ledger row keeps the manifest at its base (row,
// manifestRow): where it comes from, the crew's revision or the file's
// blob, and every prerequisite in the manifest's order, those no
// generation reached among them (the file at the base is not read again).
// A row with none is a run without a manifest, or a version 4 run's, whose
// manifest can only be the crew's as launched (setup steps at most); nil
// for a crew with none. The states are recoveredPrerequisites'.
func launchedManifest(c Crew, row string) *RunManifest {
	var mr manifestRow
	if row == "" || json.Unmarshal([]byte(row), &mr) != nil || mr.Source == "" {
		m := c.effectiveManifest()
		if m.empty() {
			return nil
		}
		mr = manifestRow{Source: sourceCrew, Revision: c.ManifestRevision}
		if m.File {
			mr = manifestRow{Source: sourceFile}
		}
		for _, ch := range m.Prerequisites {
			mr.Prerequisites = append(mr.Prerequisites, ch.ID)
		}
	}
	rm := &RunManifest{Source: mr.Source, Revision: mr.Revision, Digest: mr.Digest}
	for _, id := range mr.Prerequisites {
		rm.Prerequisites = append(rm.Prerequisites, PrerequisiteState{ID: id, State: "pending"})
	}
	return rm
}

// taskStateOfRow is st, a task as a record or the crew has it, as the
// ledger has it: its row's state, reason, attempts and import (when it has
// a row), its result's attempt decoded (a snapshot's own kept when it is
// the same attempt; one a continuation imported read from imported, the
// rows LoadImports reads by reference), its last attempt
// (attemptStateOfRow), and the state the recovery concluded for the task
// of an attempt it found interrupted or uncertain. Recovered and
// LoadedTask build a task this one way.
func taskStateOfRow(st *TaskState, row ledger.TaskRow, hasRow bool, attempts []ledger.AttemptRow, imported map[string]ledger.AttemptRow) {
	if hasRow {
		st.State, st.Reason, st.Attempts, st.ImportedFrom = row.State, row.Reason, row.Attempts, row.ImportedFrom
		switch {
		case row.ResultAttempt == 0:
			// Withdrawn, or never made: the snapshot's result goes.
			st.Result = nil
		case st.Result == nil || st.Result.Attempt != row.ResultAttempt:
			ra := byN(attempts, st.ID, row.ResultAttempt)
			if imp, ok := imported[row.ImportedFrom]; ok && row.ImportedFrom != "" {
				ra = imp
			}
			res := decodeResult(ra.Result)
			st.Result = &ResultState{Attempt: row.ResultAttempt, Commit: row.ResultCommit, Summary: res.Summary, Issues: res.Issues, Verdict: row.Verdict, Findings: res.Findings, At: ra.ClosedAt}
		}
	}
	var la ledger.AttemptRow
	for _, a := range attempts {
		if a.TaskID == st.ID && a.N > la.N {
			la = a
		}
	}
	if la.N == 0 {
		return
	}
	as := attemptStateOfRow(la)
	st.Attempt = &as
	switch {
	case la.State == ledger.AttemptUncertain && (st.State == ledger.TaskRunning || st.State == ledger.TaskVerifying):
		st.State, st.Reason = ledger.TaskFailed, fmt.Sprintf("attempt %d's launch is uncertain (the server stopped before it was known): retry it once the run is continued", la.N)
	case la.State == ledger.AttemptInterrupted && (st.State == ledger.TaskRunning || st.State == ledger.TaskVerifying):
		st.State, st.Reason = ledger.TaskReady, fmt.Sprintf("attempt %d was interrupted by the server's end: the run continues it", la.N)
	}
}

// attemptStateOfRow is an attempt's row as the run reports it: its
// number, session, worktree, branch, commits, state, stage, error, times
// and checkpoint. What it submitted and its receipts are a task's own
// read's (LoadedTask, the API's).
func attemptStateOfRow(la ledger.AttemptRow) AttemptState {
	as := AttemptState{N: la.N, SessionID: la.SessionID, Worktree: la.Worktree, Branch: la.Branch, Start: la.StartCommit, Result: la.ResultCommit, State: la.State, Stage: la.Stage, Error: la.Error}
	if !la.LaunchedAt.IsZero() {
		at := la.LaunchedAt
		as.StartedAt = &at
	}
	if !la.ClosedAt.IsZero() {
		at := la.ClosedAt
		as.EndedAt = &at
	}
	if c := decodeCheckpoint(la.Checkpoint); c != nil {
		as.Checkpoint = c.api()
	}
	return as
}

// LoadedTask is a task of a run the engine does not hold (let go, or the
// server started since), as the ledger has it: the task as the run would
// report it, from the crew as launched and its rows (taskStateOfRow; a
// result a continuation imported read from from.Imported, which
// LoadImports fills), and every attempt of it in order with what it
// submitted (attemptStateOfRow). It has no preview: nothing answers behind
// a tree the engine let go. ok is false when the run has no such task.
func LoadedTask(from Loaded, taskID string) (TaskState, []AttemptState, bool) {
	st := TaskState{ID: taskID}
	known := false
	var c Crew
	if err := json.Unmarshal([]byte(from.Run.Crew), &c); err == nil {
		for _, t := range c.Tasks {
			if t.ID == taskID {
				st.Member, st.Kind, st.Needs, known = t.Member, t.Kind, slices.Clone(t.Needs), true
			}
		}
	}
	row, hasRow := ledger.TaskRow{}, false
	for _, r := range from.Tasks {
		if r.ID == taskID {
			row, hasRow = r, true
		}
	}
	if !known && !hasRow {
		return TaskState{}, nil, false
	}
	if hasRow {
		if st.Member == "" {
			st.Member, st.Kind = row.Member, row.Kind
		}
	} else {
		st.State = ledger.TaskBlocked
	}
	taskStateOfRow(&st, row, hasRow, from.Attempts, from.Imported)
	var attempts []AttemptState
	for _, a := range from.Attempts {
		if a.TaskID != taskID {
			continue
		}
		as := attemptStateOfRow(a)
		if a.Result != "" {
			res := decodeResult(a.Result)
			as.Submitted = &res
		}
		attempts = append(attempts, as)
	}
	slices.SortFunc(attempts, func(x, y AttemptState) int { return x.N - y.N })
	return st, attempts, true
}
