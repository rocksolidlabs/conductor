package crew

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A command is a change a person asks of a managed run (pause, unpause,
// cancel, retry, accept) with a receipt (docs/plans/agent-harness-order.md,
// decision 10): the asker mints an id, the run keeps the outcome under
// (actor, id), and the same id sent again with the same content gets the
// same receipt, never a second change; other content is a conflict. An
// expected revision, when given, refuses the command once the run has
// moved on. The change a command makes is the engine's own (PauseRun,
// CancelTask, RetryTask: the same code), with the receipt written in the
// change's transaction.

// CommandIDPattern bounds a command id a client mints.
var CommandIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)

// Command errors.
var (
	ErrBadCommandID    = errors.New("a command id is 1 to 32 letters, digits, '.', '_', '~' or '-'")
	ErrStaleRevision   = errors.New("the run has moved on since the revision expected")
	ErrCommandConflict = errors.New("the command id was used before with other content")
	ErrTooManyCommands = errors.New("the run has taken as many commands as it keeps")
)

// Command is what the asker sends with a change.
type Command struct {
	ID               string `json:"commandId"`
	ExpectedRevision int64  `json:"expectedRevision,omitempty"`
}

// Receipt is what a command gets: its id and intent, the run's revision
// after it, whether this answer is the one given before (a repeat), its
// status (done, or failed with why: the error's words and the refusal's
// name, which a repeat answers with again), and the task or the run's
// pause as they stand.
type Receipt struct {
	CommandID string       `json:"commandId"`
	Intent    string       `json:"intent"`
	Revision  int64        `json:"revision"`
	Repeat    bool         `json:"repeat,omitempty"`
	Status    string       `json:"status"`
	Error     string       `json:"error,omitempty"`
	Refusal   string       `json:"refusal,omitempty"`
	Task      *ReceiptTask `json:"task,omitempty"`
	Paused    *bool        `json:"paused,omitempty"`
}

// ReceiptTask is the task a command changed, as its receipt keeps it: what
// the command did to it, not the whole task (its result's issues and
// findings, its checkpoint), which can pass every input's bound and still
// make a receipt the ledger cannot keep. The run has the rest.
type ReceiptTask struct {
	ID       string         `json:"id"`
	State    string         `json:"state"`
	Reason   string         `json:"reason,omitempty"`
	Attempts int            `json:"attempts"`
	Result   *ReceiptResult `json:"result,omitempty"`
}

// ReceiptResult is a task's result as a receipt keeps it.
type ReceiptResult struct {
	Attempt int    `json:"attempt"`
	Commit  string `json:"commit,omitempty"`
}

// maxReceiptReason bounds a receipt's task reason, in characters: a reason
// can carry a check's last output.
const maxReceiptReason = 2000

// receiptTask is st as a receipt keeps it.
func receiptTask(st TaskState) *ReceiptTask {
	rt := &ReceiptTask{ID: st.ID, State: st.State, Reason: st.Reason, Attempts: st.Attempts}
	if rs := []rune(rt.Reason); len(rs) > maxReceiptReason {
		rt.Reason = string(rs[:maxReceiptReason]) + "…"
	}
	if st.Result != nil {
		rt.Result = &ReceiptResult{Attempt: st.Result.Attempt, Commit: st.Result.Commit}
	}
	return rt
}

// Refusals a failed receipt names.
const (
	RefusalTaskState     = "task_state"
	RefusalRunStopped    = "run_stopped"
	RefusalNotFound      = "not_found"
	RefusalStaleRevision = "stale_revision"
	RefusalRunContinued  = "run_continued"
	// RefusalStaleCandidate refuses a command bound to a candidate that is
	// no longer the task's standing one (Recheck).
	RefusalStaleCandidate = "stale_candidate"
)

// ErrStaleCandidate refuses a command bound to a candidate the task has
// moved on from: another attempt is its standing one now.
var ErrStaleCandidate = errors.New("the candidate is no longer the task's standing one")

// refusalOf names the refusal err is.
func refusalOf(err error) string {
	switch {
	case errors.Is(err, ErrRunStopped):
		return RefusalRunStopped
	case errors.Is(err, ErrTaskNotFound), errors.Is(err, ErrRunNotFound):
		return RefusalNotFound
	case errors.Is(err, ErrStaleRevision):
		return RefusalStaleRevision
	case errors.Is(err, ErrRunContinued):
		return RefusalRunContinued
	case errors.Is(err, ErrStaleCandidate):
		return RefusalStaleCandidate
	}
	return RefusalTaskState
}

// fingerprint is a command's content, for the conflict check.
func fingerprint(intent string, args ...string) string {
	h := sha256.New()
	h.Write([]byte(intent))
	for _, a := range args {
		h.Write([]byte{0})
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// errCommandRepeated says the command's row was there as the transaction
// wrote: a repeat raced the first, whose commit is the answer.
var errCommandRepeated = errors.New("command repeated")

// A change is what a command does to the run, under e.mu in its
// transaction: the task it concerns (nil for the run), the events of the
// change, and an error that fails the command (its receipt says so) rather
// than the transaction; a change that errs changed nothing.
type change func(r *run) (t *task, events []event, err error)

// act runs a command of actor on the run in two transactions, the
// ledger's way (decision 10): the first takes the command (its row
// accepted; a repeat answers with the receipt given before, other content
// is a conflict, the run's cap refuses it: nothing changes), the second
// makes the change, the revision checked first, and settles the receipt
// beside it, so a change is never made for a command the ledger did not
// take, and a command taken without a settled receipt is one the server
// ended between (the recovery leaves it uncertain). undo, when given, puts
// the change back when its commit fails, as transact takes it.
func (e *Engine) act(ctx context.Context, runID, actor string, cmd Command, intent string, args []string, do change, undo func(r *run)) (Receipt, error) {
	if !CommandIDPattern.MatchString(cmd.ID) {
		return Receipt{}, ErrBadCommandID
	}
	if e.ledger == nil {
		return Receipt{}, ErrNoLedger
	}
	r, err := e.managedRun(runID)
	if err != nil {
		return Receipt{}, err
	}
	if e.afterLookup != nil {
		e.afterLookup(runID)
	}
	fp := fingerprint(intent, args...)
	if prior, found, err := e.ledger.Receipt(ctx, runID, actor, cmd.ID); err != nil {
		return Receipt{}, err
	} else if found {
		return priorReceipt(prior, intent, fp)
	}
	// The command taken: a row of its own, before any change rests on it.
	// The revision it was taken at is the one the asker's expectation is
	// held to, and nothing else may commit between the taking and the
	// change.
	var before int64
	err = e.transact(ctx, r, func() tx {
		before = r.revision
		w := tx{events: []event{{"command.accepted", map[string]any{"actor": actor, "command": cmd.ID, "intent": intent, "args": args}}}}
		w.more = func(ltx LedgerTx) error {
			if _, found, err := ltx.Command(actor, cmd.ID, intent, fp); err != nil {
				return err
			} else if found {
				return errCommandRepeated
			}
			return nil
		}
		return w
		// Refused (a repeat, the cap, the ledger), the command is not taken:
		// no acceptance is written for it later.
	}, giveUp)
	switch {
	case errors.Is(err, errCommandRepeated):
		prior, found, rerr := e.ledger.Receipt(ctx, runID, actor, cmd.ID)
		if rerr != nil || !found {
			return Receipt{}, errors.Join(err, rerr)
		}
		return priorReceipt(prior, intent, fp)
	case errors.Is(err, ledger.ErrCommandConflict):
		return Receipt{}, ErrCommandConflict
	case errors.Is(err, ledger.ErrTooManyCommands):
		return Receipt{}, ErrTooManyCommands
	case err != nil:
		if e.afterRefused != nil {
			e.afterRefused(runID, err)
		}
		e.afterTransact(r, err)
		return Receipt{}, err
	}
	e.mu.Lock()
	taken := e.afterTaken
	e.mu.Unlock()
	if taken != nil {
		taken(runID, cmd.ID)
	}
	// The change, with its receipt.
	var out Receipt
	var cmdErr error
	var back func()
	if undo != nil {
		back = func() { undo(r) }
	}
	err = e.transact(ctx, r, func() tx {
		var t *task
		var events []event
		if err := e.continued(r); err != nil {
			cmdErr = err
		} else if cmd.ExpectedRevision > 0 && (cmd.ExpectedRevision != before || r.revision != before+1) {
			cmdErr = fmt.Errorf("%w: expected %d, the run was at %d when the command was taken and is at %d", ErrStaleRevision, cmd.ExpectedRevision, before, r.revision)
		} else {
			t, events, cmdErr = do(r)
		}
		// A run's transactions commit one after another, each as the
		// revision after the run's: the receipt kept says which.
		out = Receipt{CommandID: cmd.ID, Intent: intent, Revision: r.revision + 1, Status: ledger.CommandDone}
		if cmdErr != nil {
			out.Status, out.Error, out.Refusal = ledger.CommandFailed, cmdErr.Error(), refusalOf(cmdErr)
		}
		if t != nil {
			out.Task = receiptTask(r.taskStateOf(t))
		}
		paused := r.paused
		out.Paused = &paused
		w := tx{events: append(events, event{"command." + intent, map[string]any{"actor": actor, "command": cmd.ID, "args": args, "status": out.Status, "error": out.Error}})}
		w.more = func(ltx LedgerTx) error { return ltx.Settle(actor, cmd.ID, out.Status, out) }
		return w
	}, back)
	if err != nil {
		e.afterTransact(r, err)
		return Receipt{}, err
	}
	e.afterTransact(r, nil)
	return out, cmdErr
}

// priorReceipt is the receipt given before, as a repeat; a conflict when
// the content differs.
func priorReceipt(prior ledger.CommandRow, intent, fp string) (Receipt, error) {
	if prior.Intent != intent || prior.Fingerprint != fp {
		return Receipt{}, ErrCommandConflict
	}
	var out Receipt
	if prior.Receipt != "" {
		if err := json.Unmarshal([]byte(prior.Receipt), &out); err != nil {
			return Receipt{}, fmt.Errorf("the receipt kept cannot be read: %w", err)
		}
	}
	out.CommandID, out.Intent, out.Repeat = prior.ID, prior.Intent, true
	if out.Status == "" {
		out.Status = prior.Status
	}
	return out, nil
}

// managedRun is the run, when the engine holds it and it is managed.
func (e *Engine) managedRun(runID string) (*run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[runID]
	if !ok {
		return nil, ErrRunNotFound
	}
	if !r.managed {
		return nil, fmt.Errorf("%w: a legacy run takes no command", ErrTaskState)
	}
	return r, nil
}

// taskIn is the run's task, with check run on it under e.mu.
func (e *Engine) taskIn(runID, taskID string, check func(*task) error) (*run, *task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[runID]
	if !ok {
		return nil, nil, ErrRunNotFound
	}
	t := r.task(taskID)
	if t == nil {
		return nil, nil, ErrTaskNotFound
	}
	if check != nil {
		if err := check(t); err != nil {
			return nil, nil, err
		}
	}
	return r, t, nil
}

// cancellable, retryable and acceptable say whether a task's state takes
// the command.
func cancellable(t *task) error {
	switch t.state {
	case ledger.TaskSucceeded, ledger.TaskFailed, ledger.TaskCancelled:
		return fmt.Errorf("%w: it is %s", ErrTaskState, t.state)
	}
	return nil
}

// retryStates are the task states a retry takes.
var retryStates = []string{ledger.TaskFailed, ledger.TaskCancelled}

// RetryableStates are the task states Retry takes, for the operating
// guide (internal/api, guide.go), which says them as retryable enforces
// them.
func RetryableStates() []string { return slices.Clone(retryStates) }

func retryable(t *task) error {
	if !slices.Contains(retryStates, t.state) {
		return fmt.Errorf("%w: it is %s", ErrTaskState, t.state)
	}
	return nil
}

func acceptable(t *task) error {
	if t.state != ledger.TaskFailed {
		return fmt.Errorf("%w: it is %s, and only a failed task is accepted", ErrTaskState, t.state)
	}
	return nil
}

// continued refuses a change of the run once a continuation is made of
// it or is being made (ErrRunContinued), or once the engine no longer
// holds it (ErrRunNotFound: evicted after the change looked it up, it
// could be continued from the ledger with no mark on it). The caller holds
// e.mu, in a transaction's change: ContinueRun marks the run under its
// txMu, so a change under way commits first and every later one sees the
// mark, and a run whose commit is under way is not evicted (dirty).
func (e *Engine) continued(r *run) error {
	if e.runs[r.id] != r {
		return ErrRunNotFound
	}
	if r.resumedBy != "" || r.resuming {
		return ErrRunContinued
	}
	return nil
}

// setPaused pauses or resumes the run's dispatch, noting it; nothing when
// it is so already. An unpause clears the pause's words and runs the
// project manifest's prerequisites again, once committed, unless a
// generation of them is in flight (steps.go): a host fixed in between is
// seen, and failing again they pause again. The caller holds e.mu, in a
// transaction.
func (r *run) setPaused(paused bool) []event {
	if r.paused == paused {
		return nil
	}
	r.paused = paused
	r.markRun()
	if paused {
		r.note(session.ActivityStatus, "scheduling paused")
	} else {
		r.pausedWhy = ""
		r.rearmPrerequisites()
		r.note(session.ActivityStatus, "scheduling resumed")
	}
	return []event{{"run.paused", map[string]any{"paused": paused}}}
}

// pauseMark is what a pause or an unpause changes, to put back when its
// commit is refused.
type pauseMark struct {
	paused      bool
	why, prereq string
	start       bool
}

// pauseMarkOf is r's pause as it stands. The caller holds e.mu.
func (r *run) pauseMarkOf() pauseMark {
	return pauseMark{paused: r.paused, why: r.pausedWhy, prereq: r.prereq, start: r.prereqStart}
}

// restorePause puts r's pause back as m had it. The caller holds e.mu.
func (r *run) restorePause(m pauseMark) {
	r.paused, r.pausedWhy, r.prereq, r.prereqStart = m.paused, m.why, m.prereq, m.start
}

// cancelTask cancels t: its attempt in flight is closed (its session
// stopped by the commit that makes the cancel durable), or the task is
// cancelled outright; what needs it is blocked for good, and the run may
// end. The caller holds e.mu, in a transaction; t is not terminal.
func (e *Engine) cancelTask(r *run, t *task) []event {
	// What its attempts asked ends with it.
	e.cancelAsked(r, t, t.def.ID+" was cancelled")
	if a := t.active(); a != nil {
		if a.sessionID != "" {
			r.pendingStops = append(r.pendingStops, a.sessionID)
		}
		e.closeAttempt(r, a, ledger.AttemptCancelled, a.state, "cancelled")
	} else {
		t.state, t.reason = ledger.TaskCancelled, "cancelled"
		r.markTask(t)
		r.ready()
		e.settleRun(r)
	}
	r.note(session.ActivityStatus, "task %s cancelled", t.def.ID)
	events := []event{{"task.cancelled", map[string]any{"task": t.def.ID}}}
	if r.outcome != "" {
		events = append(events, event{"run.finished", map[string]any{"outcome": r.outcome}})
	}
	return events
}

// reopenRun opens r again for a change that makes one of its tasks
// runnable or verified again (Retry, Accept anyway, Check again, and a
// repair through reopenFor: a review's needs_changes or Request changes):
// its outcome cleared and marked, the prerequisites its end interrupted
// armed again (reopenPrerequisites), and every plan decision's gate the
// change makes matter again asked again or kept holding (regate, with
// cause, what made the work runnable): a gate's requirement outlives its
// decision (I14), so no task a gate names is dispatched past it. Every
// such change reopens the run through it, so what reopening a run must
// also do is done for all of them in one place; each person's command
// checks the room the gates it would ask again need (retryNeedsRoom)
// before its change. The
// caller holds e.mu, in a transaction's change, with the change's task
// already in its new state.
func (e *Engine) reopenRun(r *run, cause string) {
	if r.outcome != "" {
		r.outcome = ""
		r.markRun()
	}
	r.reopenPrerequisites()
	e.regate(r, cause)
}

// retryTask gives failed or cancelled t one more attempt: it is blocked
// or ready again (a repair it carries stays), the run open, and the
// prerequisites its end interrupted armed again. The caller holds e.mu,
// in a transaction.
func (e *Engine) retryTask(r *run, t *task) []event {
	if len(t.attempts)+t.prior >= maxAttemptsPerTask+t.extra {
		t.extra++
	}
	t.state, t.reason = ledger.TaskBlocked, ""
	r.markTask(t)
	e.reopenRun(r, t.def.ID+" was retried")
	r.ready()
	r.note(session.ActivityStatus, "task %s: retried, one more attempt allowed", t.def.ID)
	return []event{{"task.retried", map[string]any{"task": t.def.ID}}}
}

// acceptTask takes failed t's last submitted result as its result all the
// same ("Accept anyway": a commit that failed its checks, decided by a
// person): the task succeeds with that commit, and what needs it goes on.
// A result a withdrawal took back rested on an input since changed: never
// accepted. The caller holds e.mu, in a transaction.
func (e *Engine) acceptTask(r *run, t *task, actor string) ([]event, error) {
	last, err := acceptFrom(t)
	if err != nil {
		return nil, err
	}
	res := last.submitted
	t.result = &taskResult{attempt: last.n, commit: last.result, summary: res.Summary, issues: slices.Clone(res.Issues), verdict: res.Verdict, findings: slices.Clone(res.Findings), at: time.Now().UTC()}
	t.repair, t.imported = nil, ""
	t.state, t.reason = ledger.TaskSucceeded, fmt.Sprintf("accepted by %s with attempt %d's result", actor, last.n)
	r.markTask(t)
	// Open again (what needs the task goes on), the prerequisites the
	// run's end interrupted armed again and its gates asked again, unless
	// the acceptance settles it.
	e.reopenRun(r, t.def.ID+" was accepted")
	r.ready()
	e.settleRun(r)
	r.note(session.ActivityStatus, "task %s: attempt %d's result accepted by %s", t.def.ID, last.n, actor)
	events := []event{{"task.accepted", map[string]any{"task": t.def.ID, "attempt": last.n, "actor": actor, "commit": last.result}}}
	if r.outcome != "" {
		events = append(events, event{"run.finished", map[string]any{"outcome": r.outcome}})
	}
	return events, nil
}

// acceptFrom is the attempt whose result an Accept anyway takes for t:
// the latest kept one (none a withdrawal took back) that submitted a
// commit; ErrTaskState when there is none. Accept and TaskCommands both
// ask it. The caller holds e.mu.
func acceptFrom(t *task) (*attempt, error) {
	for i := len(t.attempts) - 1; i >= 0 && t.kept(t.attempts[i].n); i-- {
		if a := t.attempts[i]; a.submitted != nil && a.result != "" {
			return a, nil
		}
	}
	if t.withdrawn > 0 {
		return nil, fmt.Errorf("%w: no attempt since the withdrawal submitted a commit to accept", ErrTaskState)
	}
	return nil, fmt.Errorf("%w: no attempt submitted a commit to accept", ErrTaskState)
}

// The task commands, as TaskCommands names them.
const (
	TaskRetry  = "retry"
	TaskAccept = "accept"
	TaskCancel = "cancel"
)

// TaskCommandOn reports whether the ledger's command row is one of the
// three task commands sent for the task taskID: its intent Retry, Accept
// or Cancel, and its fingerprint the one act keeps for that command on
// that task (TaskCommandFingerprint). A run's pause or another task's
// command is not, whatever its id.
func TaskCommandOn(row ledger.CommandRow, taskID string) bool {
	switch row.Intent {
	case TaskRetry, TaskAccept, TaskCancel:
		return row.Fingerprint == TaskCommandFingerprint(row.Intent, taskID)
	}
	return false
}

// TaskCommandFingerprint is the fingerprint act keeps in the ledger for
// the task command name (TaskRetry, TaskAccept, TaskCancel) on the task
// taskID.
func TaskCommandFingerprint(name, taskID string) string { return fingerprint(name, taskID) }

// TaskCommandSet is what a task's state allows of the three task
// commands, as the engine would judge them now: their names in the order
// retry, accept, cancel, the run's revision they were judged at (the
// expected revision a command sent for them carries), for an Accept
// anyway the attempt and commit it would take, for a Retry the commit it
// would start at, and the task they were judged on.
type TaskCommandSet struct {
	Commands      []string `json:"commands"`
	Revision      int64    `json:"revision"`
	AcceptAttempt int      `json:"acceptAttempt,omitempty"`
	AcceptCommit  string   `json:"acceptCommit,omitempty"`
	// RetryStart is the commit a Retry's next attempt would start at, as
	// the engine picks it (startCommit): a failed attempt's result, a
	// repair's, a checkpoint's; "" when the task's inputs give it.
	RetryStart string `json:"retryStart,omitempty"`
	// Task is the task as it stood when the commands were judged, read
	// under the same lock as them: what a page describes them by.
	Task TaskState `json:"task"`
}

// TaskCommands is what the run's task allows of Retry, Accept and Cancel,
// judged by the preconditions the commands themselves apply (the run held
// and managed, its launch recorded, not continued: continued; then
// retryable, acceptable with acceptFrom, cancellable), so that a page
// offering them and the engine taking them never disagree. A command it
// leaves out is one the engine would refuse now; one it names may still
// be refused when the run moves first (the revision). ErrRunNotFound and
// ErrTaskNotFound for what the engine does not hold.
func (e *Engine) TaskCommands(runID, taskID string) (TaskCommandSet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[runID]
	if !ok {
		return TaskCommandSet{}, ErrRunNotFound
	}
	t := r.task(taskID)
	if t == nil {
		return TaskCommandSet{}, ErrTaskNotFound
	}
	out := e.commandsOf(r, t)
	out.Task = r.taskStateOf(t)
	return out, nil
}

// commandsOf is TaskCommands for r's task t, without the task's state.
// The caller holds e.mu.
func (e *Engine) commandsOf(r *run, t *task) TaskCommandSet {
	out := TaskCommandSet{Commands: []string{}, Revision: r.revision}
	if e.ledger == nil || !r.managed || r.unrecorded() || r.foreign || e.continued(r) != nil {
		return out
	}
	for _, name := range []string{TaskRetry, TaskAccept, TaskCancel} {
		a, err := taskCommandCheck(r, name, t)
		if err != nil {
			continue
		}
		out.Commands = append(out.Commands, name)
		if a != nil {
			out.AcceptAttempt, out.AcceptCommit = a.n, a.result
		}
		if name == TaskRetry {
			out.RetryStart = r.startCommit(t)
		}
	}
	return out
}

// withCommands puts in the run's read out (Get, GetWithDiffs, List) the task commands each of r's
// tasks allows (TaskState.Commands), as TaskCommands judges them, so the
// run page offers what the engine takes. The caller holds e.mu.
func (e *Engine) withCommands(r *run, out *Run) {
	for i, t := range r.tasks {
		if i < len(out.Tasks) {
			if c := e.commandsOf(r, t).Commands; len(c) > 0 {
				out.Tasks[i].Commands = c
			}
		}
	}
}

// taskCommandCheck is the task command name's precondition on t of the
// run r, the one the command applies (Retry, Accept, Cancel) and
// TaskCommands asks: nil when t's state takes it, with, for an Accept
// anyway, the attempt whose result it takes. A precondition a command
// gains, of the task or of its run, goes here, so the two never disagree.
// The caller holds e.mu.
func taskCommandCheck(r *run, name string, t *task) (*attempt, error) {
	switch name {
	case TaskRetry:
		return nil, retryable(t)
	case TaskAccept:
		if err := acceptable(t); err != nil {
			return nil, err
		}
		return acceptFrom(t)
	case TaskCancel:
		return nil, cancellable(t)
	}
	return nil, fmt.Errorf("%w: no task command %q", ErrTaskState, name)
}

// SetAfterTakenForTest sets the seam a command runs between its two
// transactions, once the ledger took it and before its change (nil for
// none), for a test of what reads its receipt meanwhile (the decision
// inbox's track: accepted, the engine applying it).
func (e *Engine) SetAfterTakenForTest(fn func(runID, commandID string)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.afterTaken = fn
}

// Pause is PauseRun as a command with a receipt (paused false: unpause).
func (e *Engine) Pause(ctx context.Context, runID, actor string, cmd Command, paused bool) (Receipt, error) {
	intent := "pause"
	if !paused {
		intent = "unpause"
	}
	// was is the pause as the change found it, put back when the commit is
	// refused; nil when no change was made (a stale revision, a run stopped
	// or continued: act writes the refusal's receipt alone), which leaves
	// nothing to put back. A mark of nothing would clear the run's pause
	// and its prerequisites' state, and the pump, which dispatches only on
	// passed, would wait for good (Codex's review of #422).
	var was *pauseMark
	return e.act(ctx, runID, actor, cmd, intent, nil, func(r *run) (*task, []event, error) {
		if r.stoppedAt != nil {
			return nil, nil, ErrRunStopped
		}
		m := r.pauseMarkOf()
		was = &m
		return nil, r.setPaused(paused), nil
	}, func(r *run) {
		if was == nil {
			return
		}
		r.restorePause(*was)
		r.note(session.ActivityError, "the ledger could not record the %s", intent)
	})
}

// Cancel is CancelTask as a command with a receipt.
func (e *Engine) Cancel(ctx context.Context, runID, actor string, cmd Command, taskID string) (Receipt, error) {
	return e.act(ctx, runID, actor, cmd, "cancel", []string{taskID}, func(r *run) (*task, []event, error) {
		t := r.task(taskID)
		if t == nil {
			return nil, nil, ErrTaskNotFound
		}
		if _, err := taskCommandCheck(r, TaskCancel, t); err != nil {
			return t, nil, err
		}
		return t, e.cancelTask(r, t), nil
	}, nil)
}

// Retry is RetryTask as a command with a receipt.
func (e *Engine) Retry(ctx context.Context, runID, actor string, cmd Command, taskID string) (Receipt, error) {
	return e.act(ctx, runID, actor, cmd, "retry", []string{taskID}, func(r *run) (*task, []event, error) {
		t := r.task(taskID)
		if t == nil {
			return nil, nil, ErrTaskNotFound
		}
		if _, err := taskCommandCheck(r, TaskRetry, t); err != nil {
			return t, nil, err
		}
		if err := r.retryNeedsRoom(t, "retry"); err != nil {
			return t, nil, err
		}
		return t, e.retryTask(r, t), nil
	}, nil)
}

// Accept takes a failed task's last submitted result as its result, as a
// command with a receipt.
func (e *Engine) Accept(ctx context.Context, runID, actor string, cmd Command, taskID string) (Receipt, error) {
	return e.act(ctx, runID, actor, cmd, "accept", []string{taskID}, func(r *run) (*task, []event, error) {
		t := r.task(taskID)
		if t == nil {
			return nil, nil, ErrTaskNotFound
		}
		if _, err := taskCommandCheck(r, TaskAccept, t); err != nil {
			return t, nil, err
		}
		if err := r.retryNeedsRoom(t, "accept"); err != nil {
			return t, nil, err
		}
		events, err := e.acceptTask(r, t, actor)
		return t, events, err
	}, nil)
}

// RecheckRequest names the failed candidate a Check again verifies again:
// the task, the attempt that made it and its commit, and, in a
// continuation, Imported, the candidate's reference in the run that made
// it ("<run>/<task>/<n>", the continued run's attempt); "" names an attempt
// of the run itself.
type RecheckRequest struct {
	Task     string `json:"task"`
	Attempt  int    `json:"attempt"`
	Commit   string `json:"commit"`
	Imported string `json:"imported,omitempty"`
}

// Recheckable reports whether a failed attempt's verification is what
// failed it, so its result can be checked again as it is: it failed with
// a result commit at a check or a verify step (failedAtCheck), or before
// the checks, as its checkout was made ready (refusedBeforeChecks: the
// checkout, the manifest, a setup, service or ready step, the ready
// phase).
func Recheckable(a ledger.AttemptRow) bool {
	if a.State != ledger.AttemptFailed || a.ResultCommit == "" {
		return false
	}
	if _, _, ok := failedAtCheck(a); ok {
		return true
	}
	return refusedBeforeChecks(a.Stage)
}

// Recheck is Check again, a command with a receipt (docs/plans/
// review-repair.md, RV1): a failed task's standing failed candidate, the
// one rq names, is verified again as it is, the same commit, nothing
// changed. Refused, nothing is recorded: a task that is not failed, or
// whose verification did not fail it (task_state); a candidate that is no
// longer the task's standing failed one (stale_candidate); a run the
// engine does not run, stopped or let go (task_state: continue it, then
// check again in its continuation), or continued (run_continued); a run
// with maxActiveAttempts attempts in flight (task_state: check again once
// one ends), as the pump dispatches none past them.
//
// What is recorded before the check starts: in the command's own
// transaction, with its settled receipt and the result.reverified event,
// a new attempt (numbered next, not counted against the task) whose
// result is the candidate's commit and which is verifying, the task
// verifying, and a finished run open again (reopenRun). The check starts
// once that is committed and the run's prerequisites have passed, whatever
// the pause (startRechecks, from the pump): the prerequisites a finished
// run's end interrupted run again first, and a run they fail pauses
// waits with them. The candidate's own attempt and its receipts stay as
// they are: the new attempt's are its own.
//
// Duplicates and races, each a refusal or the same answer again: the same
// command (actor and id) sent again is its receipt again, never a second
// check, and the same id naming another candidate a conflict, as for
// every command (act); another command while the attempt it made is
// verifying finds the task verifying (task_state), and once that attempt
// failed, the candidate it named moved on (stale_candidate); two sent at
// once are taken one after the other under the run's transactions, so one
// makes the attempt and the other is one of these. A stop before the
// change refuses it; one after closes the new attempt, stopped, and its
// check never starts or ends interrupted with it. A continuation marked
// first refuses it; one asked after it finds the run running
// (ErrRunRunning). A server that ends after the commit, before the check
// started, leaves the attempt verifying in the ledger: the recovery
// interrupts it and the run's continuation verifies its result again
// (reverify), as it does any result a restart cut; one that ends before
// the commit changed nothing.
func (e *Engine) Recheck(ctx context.Context, runID, actor string, cmd Command, rq RecheckRequest) (Receipt, error) {
	if e.ledger != nil {
		if _, err := e.managedRun(runID); errors.Is(err, ErrRunNotFound) {
			if _, ok, lerr := e.ledger.Load(ctx, runID); lerr == nil && ok {
				return Receipt{}, fmt.Errorf("%w: run %s is not running on this server: continue it, then check again in its continuation", ErrTaskState, runID)
			}
		}
	}
	// made is the attempt the change recorded, verified once it is
	// committed; was the run as the change found it, put back with the
	// attempt taken back when the commit is refused.
	var made *attempt
	var was struct {
		state, reason, outcome string
		pause                  pauseMark
	}
	return e.act(ctx, runID, actor, cmd, "recheck", []string{rq.Task, strconv.Itoa(rq.Attempt), rq.Commit}, func(r *run) (*task, []event, error) {
		t := r.task(rq.Task)
		if t == nil {
			return nil, nil, ErrTaskNotFound
		}
		if r.stopping || r.stoppedAt != nil {
			return t, nil, fmt.Errorf("%w: the run is stopped: continue it, then check again in its continuation", ErrTaskState)
		}
		if t.def.Kind != TaskWork {
			return t, nil, fmt.Errorf("%w: a review task's verdict is not checked again", ErrTaskState)
		}
		if t.state != ledger.TaskFailed {
			return t, nil, fmt.Errorf("%w: it is %s, and only a failed task's candidate is checked again", ErrTaskState, t.state)
		}
		fc, ok := r.standingFailed(t)
		if !ok {
			return t, nil, fmt.Errorf("%w: the task has no failed candidate since its withdrawal", ErrStaleCandidate)
		}
		named := rq.Imported
		if named == "" {
			named = fmt.Sprintf("%s/%s/%d", r.id, rq.Task, rq.Attempt)
		}
		if standing := fmt.Sprintf("%s/%s/%d", fc.RunID, fc.TaskID, fc.N); named != standing || fc.ResultCommit != rq.Commit {
			return t, nil, staleCandidate(fc, rq)
		}
		if !Recheckable(fc) {
			return t, nil, fmt.Errorf("%w: attempt %d failed at %s, not in its verification: retry the task instead", ErrTaskState, fc.N, cmp.Or(fc.Stage, fc.State))
		}
		if r.active >= maxActiveAttempts {
			return t, nil, fmt.Errorf("%w: the run has %d attempts in flight, as many as it runs at once: check again once one ends", ErrTaskState, r.active)
		}
		m := r.member(t.def.Member)
		if m == nil {
			return t, nil, fmt.Errorf("%w: the task's member %s is not in the run", ErrTaskState, t.def.Member)
		}
		// The gates its reopen would ask again need their room, as a
		// Retry's do: refused before anything is recorded.
		if err := r.retryNeedsRoom(t, "check again"); err != nil {
			return t, nil, err
		}
		was.state, was.reason, was.outcome, was.pause = t.state, t.reason, r.outcome, r.pauseMarkOf()
		a, events := e.recheckTask(r, t, m, fc, actor)
		made = a
		return t, events, nil
	}, func(r *run) {
		if made == nil {
			return
		}
		a, t := made, made.t
		made = nil
		a.closedAt = time.Now().UTC()
		a.cancel()
		if i := slices.Index(t.attempts, a); i >= 0 {
			t.attempts = slices.Delete(t.attempts, i, i+1)
		}
		if i := slices.Index(r.rechecks, a); i >= 0 {
			r.rechecks = slices.Delete(r.rechecks, i, i+1)
		}
		delete(r.changedAttempts, a)
		r.active--
		t.extra--
		t.state, t.reason = was.state, was.reason
		if r.outcome != was.outcome {
			r.outcome = was.outcome
			r.markRun()
		}
		r.restorePause(was.pause)
		r.markTask(t)
		r.ready()
		r.note(session.ActivityError, "the ledger could not record the check again of task %s", t.def.ID)
	})
}

// staleCandidate is the refusal of a Check again whose candidate is not
// fc, the task's standing failed one, in words that name what stands.
func staleCandidate(fc ledger.AttemptRow, rq RecheckRequest) error {
	if fc.N > rq.Attempt {
		return fmt.Errorf("%w: attempt %d failed since, at %s: check that one again", ErrStaleCandidate, fc.N, short(fc.ResultCommit))
	}
	return fmt.Errorf("%w: the task's failed candidate is attempt %d of run %s at %s, not attempt %d at %s", ErrStaleCandidate, fc.N, fc.RunID, short(fc.ResultCommit), rq.Attempt, short(rq.Commit))
}

// recheckTask records attempt n+1 of failed t, the Check again of fc, its
// standing failed candidate: no session, fc's result commit its result and
// fc's start its start (where a next attempt goes back to when it is
// refused before its checks), what fc submitted its submission, verifying
// and not counted against the task; the task verifying, the run open again
// (reopenRun). The attempt waits in r.rechecks for its verification, which
// the pump starts once the change is committed and the prerequisites have
// passed (startRechecks). The caller holds e.mu, in the command's change.
func (e *Engine) recheckTask(r *run, t *task, m *member, fc ledger.AttemptRow, actor string) (*attempt, []event) {
	ctx, cancel := context.WithCancel(r.ctx)
	n := len(t.attempts) + t.prior + 1
	start := cmp.Or(fc.StartCommit, fc.ResultCommit)
	res := decodeResult(fc.Result)
	now := time.Now().UTC()
	a := &attempt{t: t, m: m, n: n, state: ledger.AttemptVerifying, ctx: ctx, cancel: cancel, reservedAt: now, launchedAt: now, inputs: r.inputs(t), start: start, result: fc.ResultCommit, submitted: &res,
		worktree: filepath.Join(r.cwd, worktreesDir, r.id, fmt.Sprintf("%s.%d", t.def.ID, n)), where: r.rt.Where()}
	t.attempts = append(t.attempts, a)
	t.extra++
	t.state, t.reason = ledger.TaskVerifying, fmt.Sprintf("attempt %d checks again attempt %d's result %s, as %s asked", n, fc.N, short(fc.ResultCommit), actor)
	r.active++
	r.rechecks = append(r.rechecks, a)
	r.markTask(t)
	r.markAttempt(a)
	e.reopenRun(r, t.def.ID+" was checked again")
	r.ready()
	r.note(session.ActivityStatus, "task %s: attempt %d checks again the result %s of attempt %d, as %s asked", t.def.ID, n, short(fc.ResultCommit), fc.N, actor)
	payload := map[string]any{"task": t.def.ID, "attempt": n, "commit": fc.ResultCommit, "of": fc.N, "actor": actor}
	if fc.RunID != r.id {
		payload["from"] = fc.RunID
	}
	return a, []event{{"result.reverified", payload}}
}

// startRechecks starts the verification of each Check again whose change
// is committed (r.rechecks) once the run's prerequisites have passed,
// whatever the pause: a Check again is a person's command, not the pump's
// scheduling. One closed meanwhile (a stop) is let go. The caller holds
// e.mu, in the pump, on the state as committed.
func (e *Engine) startRechecks(r *run) {
	if len(r.rechecks) == 0 || r.stopping || r.prereq != prereqPassed || r.ctx.Err() != nil {
		return
	}
	for _, a := range r.rechecks {
		if !a.closedAt.IsZero() {
			continue
		}
		r.starts.Add(1)
		go e.recheckRecorded(r, a)
	}
	r.rechecks = nil
}

// recheckRecorded verifies a Check again's attempt, its change committed,
// and lets go of the start startRechecks counted.
func (e *Engine) recheckRecorded(r *run, a *attempt) {
	defer r.starts.Done()
	if e.beforeRecheck != nil {
		e.beforeRecheck(r.id)
	}
	e.verifyRecorded(r, a)
}

// TaskContext is an attempt's task as written for its session: task.md
// and task.json as they are in its working directory. ErrNotAnAttempt for
// a session that is no attempt, ErrNotRunning for one without a worktree
// yet.
func (e *Engine) TaskContext(sessionID string) (md, js []byte, err error) {
	v, ok := e.bySession.Load(sessionID)
	if !ok {
		return nil, nil, ErrNotAnAttempt
	}
	sm := v.(sessionMember)
	if sm.a == nil {
		return nil, nil, ErrNotAnAttempt
	}
	e.mu.Lock()
	dir := sm.a.worktree
	prefix := sm.r.prefix
	e.mu.Unlock()
	if dir == "" {
		return nil, nil, fmt.Errorf("%w: the attempt has no worktree yet", ErrNotRunning)
	}
	root, err := os.OpenRoot(filepath.Join(dir, prefix))
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	md, err = root.ReadFile(filepath.Join(taskDir, "task.md"))
	if err != nil {
		return nil, nil, err
	}
	js, err = root.ReadFile(filepath.Join(taskDir, "task.json"))
	if err != nil {
		return nil, nil, err
	}
	return md, js, nil
}

// TaskOf is a run's task as reported, with every attempt's state and what
// it submitted.
func (e *Engine) TaskOf(runID, taskID string) (TaskState, []AttemptState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[runID]
	if !ok {
		return TaskState{}, nil, ErrRunNotFound
	}
	t := r.task(taskID)
	if t == nil {
		return TaskState{}, nil, ErrTaskNotFound
	}
	st := r.taskStateOf(t)
	if c := e.commandsOf(r, t).Commands; len(c) > 0 {
		st.Commands = c
	}
	attempts := make([]AttemptState, 0, len(t.attempts))
	for _, a := range t.attempts {
		as := r.attemptState(a)
		if a.submitted != nil {
			res := *a.submitted
			res.Issues, res.Findings = slices.Clone(res.Issues), slices.Clone(res.Findings)
			as.Submitted = &res
		}
		attempts = append(attempts, as)
	}
	return st, attempts, nil
}
