package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// The four steps of one action (the decision inbox's track): computed in
// order from the record of that action's own outcome, never from what is
// true of its subject now. A receipt is settled once and never rewritten,
// so two reads give the same steps or later ones; a source that forgets
// (a session's 64 kept results, a session gone) gives unknown, never a
// guess.

// step is a step with its words cut to their bound.
func step(name, state string, at *time.Time, words, receipt string) deliveryStep {
	if at != nil {
		t := at.UTC()
		at = &t
	}
	return deliveryStep{Step: name, State: state, At: at, Words: fitText(words, 500), Receipt: receipt}
}

// timeOf is t's address, nil for the zero time.
func timeOf(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// The words of the steps a hosted question's server does not know.
const (
	hostHoldsWords  = "the host holds this answer; this server keeps no record of it"
	notKnownResumed = "it is not known here whether the work went on"
)

// The words of a question the session keeps without showing it
// (Info.Kept): the agent's own set aside while it works on, or the one
// the hold of the turn holds over.
const keptWords = "set aside: the session keeps it to show again when the agent's turn is done or the held turn ends"

// leftWords are the words of a question that went for good without an
// answer through its claim, by how it went (session.Left*), but for keys
// typed in the terminal (terminalSteps).
var leftWords = map[string]string{
	session.LeftEnded:      "the session ended before an answer was taken",
	session.LeftSuperseded: "another question was shown in its place before an answer was taken",
	session.LeftWithdrawn:  "withdrawn by the agent before an answer was taken",
}

// questionSteps are the four steps of an answer to the question id of the
// session d, attempt the request id the browser sent it with ("" when it
// is not known); known is false when the session keeps nothing of the
// question (never asked as far as it knows, or forgotten) and it is not
// shown. How a question can leave, and what the steps then read:
//
//   - shown, keys typed in the terminal reached it: recorded refused
//     (terminalSteps), the rest not applicable;
//   - shown, or kept to show again (set aside, held over): waiting;
//   - answered through its claim (a server session's kept result): this
//     attempt's or another's, then resumed from the kept resumption
//     (working, a next question: done; the end: not applicable; none
//     yet: waiting);
//   - gone for good without an answer (session.QuestionLeft): keys typed
//     in the terminal (terminalSteps), the session's end, another
//     question in its place, or withdrawn: recorded refused at the time
//     it went, the rest not applicable;
//   - a hosted session's, answered or gone: recorded and validated
//     unknown (the host holds the answer), resumed from what the server
//     saw as the host synced it (hostedResumes): done or not applicable
//     at the change that said how the work went on, waiting until then,
//     unknown when the server did not see it leave;
//   - forgotten (past the session's last 64), or the session no longer
//     listed: unknown (handleInboxTrack), never a guess.
func (s *Server) questionSteps(d session.Driver, id, attempt string) ([]deliveryStep, bool) {
	info := d.Info()
	hs, hosted := d.(*signal.HostedSession)
	if hosted {
		// What the host said, never a change the server forwarded and the
		// host has not applied (HostView).
		info = hs.HostView()
	}
	receipt := questionRef(info.ID, id)
	shown := info.Attention.State == session.AttentionNeedsInput && info.Attention.ID == id
	kept := !shown && slices.Contains(info.Kept, id)
	avail := step(stepAvailable, stepDone, info.Attention.Since, "the session asked it", receipt)
	if !shown {
		avail.At = nil
	}
	// Keys typed in the terminal reached it: it shows until the screen
	// moves on, and takes no answer (the session refuses every reply
	// naming it), so nothing a page sent was recorded.
	if shown && info.Attention.Answered == session.AnsweredTerminal {
		return terminalSteps(avail, nil, receipt), true
	}
	waits := "not answered yet"
	if kept {
		waits = keptWords
	}
	if hosted {
		res, seen, pending := s.hostedSeen.of(info.ID, id)
		out := []deliveryStep{avail, step(stepRecorded, stepUnknown, nil, hostHoldsWords, ""), step(stepValidated, stepUnknown, nil, hostHoldsWords, "")}
		switch {
		case shown || kept:
			if kept {
				out[1], out[2] = step(stepRecorded, stepWaiting, nil, keptWords, ""), step(stepValidated, stepWaiting, nil, keptWords, "")
			}
			return append(out, step(stepResumed, stepWaiting, nil, waits, "")), true
		case res != nil:
			return append(out, resumedStep(*res)), true
		case pending:
			return append(out, step(stepResumed, stepWaiting, nil, "the agent has not gone on yet", "")), true
		case seen:
			return append(out, step(stepResumed, stepUnknown, nil, notKnownResumed, "")), true
		}
		return nil, false
	}
	local, ok := d.(*session.Local)
	if !ok {
		return nil, false
	}
	r, answered := local.QuestionResult(id)
	if !answered {
		if shown || kept {
			return []deliveryStep{avail, step(stepRecorded, stepWaiting, nil, waits, ""), step(stepValidated, stepWaiting, nil, waits, ""),
				step(stepResumed, stepWaiting, nil, waits, "")}, true
		}
		left, ok := local.QuestionLeft(id)
		if !ok {
			return nil, false
		}
		if left.How == session.LeftTerminal {
			return terminalSteps(avail, &left.At, receipt), true
		}
		words := cmpOr(leftWords[left.How], "it left before an answer was taken")
		return []deliveryStep{avail, step(stepRecorded, stepRefused, &left.At, words, receipt), step(stepValidated, stepNotApplicable, nil, words, ""),
			step(stepResumed, stepNotApplicable, nil, words, "")}, true
	}
	by := r.ByName
	if by == "" {
		by = "Conductor"
	}
	if attempt != "" && r.ReqID != attempt {
		words := "another answer first, by " + by
		return []deliveryStep{avail, step(stepRecorded, stepRefused, &r.At, words, receipt), step(stepValidated, stepRefused, &r.At, words, receipt),
			step(stepResumed, stepNotApplicable, nil, words, "")}, true
	}
	recorded := step(stepRecorded, stepDone, &r.At, "answered by "+by+"; kept by the session while it is listed", receipt)
	validated := step(stepValidated, stepDone, &r.At, "the session's claim wrote the answer: the session is the owner, one step with the record", receipt)
	if r.Resumed == nil {
		return []deliveryStep{avail, recorded, validated, step(stepResumed, stepWaiting, nil, "the agent has not gone on yet", "")}, true
	}
	return []deliveryStep{avail, recorded, validated, resumedStep(*r.Resumed)}, true
}

// terminalWords say a question was answered by keys typed in its terminal.
const terminalWords = "keys typed in the terminal answered it first"

// terminalSteps are the steps of an answer to a question keys typed in
// the terminal answered first: recorded refused (nothing a page sent was
// taken), the rest not applicable; at, when known, the time it left.
func terminalSteps(avail deliveryStep, at *time.Time, receipt string) []deliveryStep {
	return []deliveryStep{avail, step(stepRecorded, stepRefused, at, terminalWords, receipt), step(stepValidated, stepNotApplicable, nil, terminalWords, ""),
		step(stepResumed, stepNotApplicable, nil, terminalWords, "")}
}

// resumedStep is the resumed step of a question's kept resumption.
func resumedStep(r session.Resumption) deliveryStep {
	switch r.How {
	case session.ResumedWorking:
		return step(stepResumed, stepDone, &r.At, "the agent is working again", "")
	case session.ResumedAsked:
		return step(stepResumed, stepDone, &r.At, "the agent went on and asked a next question", "")
	}
	return step(stepResumed, stepNotApplicable, &r.At, "the session ended before the work went on", "")
}

// commandSteps are the four steps of the command id of actor on the task
// of the run: the ledger's receipt, whatever its status (accepted, done,
// failed, or uncertain after a stop), then the task as it stands. A
// Check again goes to the newest run of the chain (the recheck route), so
// its receipt is looked for in the runs that continue this one too
// (laterReceipt): the run continued after the page read it, its reply
// lost or not, the receipt is where the route took it.
func (s *Server) commandSteps(ctx context.Context, runID, taskID, actor, id string) ([]deliveryStep, error) {
	avail := step(stepAvailable, stepDone, nil, "asked of task "+taskID+" of run "+runID, "")
	if s.ledger == nil {
		return nil, crew.ErrNoLedger
	}
	row, found, err := s.ledger.Receipt(ctx, runID, actor, id)
	if err == nil && !found {
		row, found, err = s.laterReceipt(ctx, runID, actor, id)
	}
	if err != nil {
		return nil, err
	}
	if !found {
		none := "no receipt of this command is kept here"
		return []deliveryStep{avail, step(stepRecorded, stepUnknown, nil, none, ""), step(stepValidated, stepUnknown, nil, none, ""), step(stepResumed, stepUnknown, nil, none, "")}, nil
	}
	runID = row.RunID
	receipt := "command:" + runID + "/" + id
	var args []string
	if row.Intent == crew.TaskRecheck {
		// Its fingerprint holds its candidate: the task is read from what
		// it was taken with, whatever its receipt says (none, refused
		// before its change or not settled).
		if args, err = crew.CommandArgs(ctx, s.ledger, row); err != nil {
			return nil, err
		}
	}
	if !crew.TaskCommandOn(row, args, taskID) {
		// The id names another command (a run's pause, another task's):
		// none with it was taken for this task, whatever that one became.
		return otherReceiptSteps(avail, "the command id names another command of the run, not one for task "+taskID+": none with it was taken for this task"), nil
	}
	recorded := step(stepRecorded, stepDone, timeOf(row.CreatedAt), "the engine's ledger keeps the command's receipt", receipt)
	var rc crew.Receipt
	if row.Receipt != "" {
		_ = json.Unmarshal([]byte(row.Receipt), &rc)
	}
	task, attempts := s.taskNow(ctx, runID, taskID)
	switch row.Status {
	case ledger.CommandAccepted:
		w := "the engine took it and is applying it"
		return []deliveryStep{avail, recorded, step(stepValidated, stepWaiting, nil, w, receipt), step(stepResumed, stepWaiting, nil, w, "")}, nil
	case ledger.CommandFailed:
		w := fmt.Sprintf("refused (%s): %s", cmpOr(rc.Refusal, crew.RefusalTaskState), rc.Error)
		if rc.Refusal == crew.RefusalStaleRevision {
			w = "refused (stale_revision): the run moved on; nothing was done"
		}
		return []deliveryStep{avail, recorded, step(stepValidated, stepRefused, nil, w, receipt), step(stepResumed, stepNotApplicable, nil, w, "")}, nil
	case ledger.CommandUncertain:
		w := "the server stopped after the engine took this and before it applied it"
		return []deliveryStep{avail, recorded, step(stepValidated, stepUnknown, nil, w, receipt), uncertainResumed()}, nil
	}
	validated := step(stepValidated, stepDone, nil, fmt.Sprintf("the engine took it at the run's revision; revision %d after it", rc.Revision), receipt)
	if row.Intent == crew.TaskRecheck {
		resumed, err := s.recheckResumed(ctx, runID, taskID, rc, attempts)
		if err != nil {
			return nil, err
		}
		return []deliveryStep{avail, recorded, validated, resumed}, nil
	}
	var deps []dependent
	depsKnown := false
	if row.Intent == crew.TaskAccept {
		var tasks []crew.TaskState
		tasks, depsKnown = s.runDependents(runID, taskID)
		for _, d := range tasks {
			_, as := s.taskNow(ctx, runID, d.ID)
			if as == nil && d.Attempt != nil {
				as = []crew.AttemptState{*d.Attempt}
			}
			deps = append(deps, dependent{id: d.ID, attempts: as})
		}
	}
	return []deliveryStep{avail, recorded, validated, doneResumed(row, rc, task, attempts, deps, depsKnown)}, nil
}

// laterReceipt is actor's receipt of the Check again id in a run that
// continues runID, directly or not, walking forward as the recheck route
// does (runNode's marks, each run once); found false for none, and for an
// id a later run keeps as another command, which is not this one.
func (s *Server) laterReceipt(ctx context.Context, runID, actor, id string) (ledger.CommandRow, bool, error) {
	if s.runs == nil {
		return ledger.CommandRow{}, false, nil
	}
	seen := map[string]bool{runID: true}
	for at := runID; ; {
		nd, ok, err := s.runNode(ctx, at)
		if err != nil || !ok || nd.by == "" || seen[nd.by] {
			return ledger.CommandRow{}, false, err
		}
		at = nd.by
		seen[at] = true
		row, found, err := s.ledger.Receipt(ctx, at, actor, id)
		if err != nil || found {
			return row, found && row.Intent == crew.TaskRecheck, err
		}
	}
}

// recheckResumed is the resumed step of a Check again the engine took:
// its attempt (the receipt's task's last, the one its change recorded)
// checking the candidate again, from the first receipt of its
// verification the ledger keeps (its checkout's steps, its checks), or
// its end when it decided without one. The change records the attempt
// verifying and its verification starts only once the run's
// prerequisites have passed (Engine.startRechecks), so until then it is
// waiting, never done; closed before (a stop, a cancel) it is not
// applicable; unknown when the attempt is not held here.
func (s *Server) recheckResumed(ctx context.Context, runID, taskID string, rc crew.Receipt, attempts []crew.AttemptState) (deliveryStep, error) {
	var a *crew.AttemptState
	for i := range attempts {
		if rc.Task != nil && attempts[i].N == rc.Task.Attempts {
			a = &attempts[i]
		}
	}
	if a == nil {
		return step(stepResumed, stepUnknown, nil, "the attempt that checks it again is not held here", ""), nil
	}
	// The attempt as read first, its receipts after: a verification that
	// started between the two reads as started.
	rows, err := s.ledger.Checks(ctx, runID, taskID, a.N, false)
	if err != nil {
		return deliveryStep{}, err
	}
	var first *time.Time
	for _, r := range rows {
		if at := r.StartedAt; !at.IsZero() && (first == nil || at.Before(*first)) {
			first = &at
		}
	}
	switch {
	case first != nil:
		return step(stepResumed, stepDone, first, fmt.Sprintf("attempt %d checks the candidate again", a.N), ""), nil
	case a.State == ledger.AttemptSucceeded || a.State == ledger.AttemptFailed:
		return step(stepResumed, stepDone, a.EndedAt, fmt.Sprintf("attempt %d checked the candidate again", a.N), ""), nil
	case a.EndedAt == nil:
		return step(stepResumed, stepWaiting, nil, fmt.Sprintf("attempt %d waits to check the candidate again: its verification starts once the run's prerequisites have passed", a.N), ""), nil
	}
	return step(stepResumed, stepNotApplicable, a.EndedAt, fmt.Sprintf("attempt %d was %s before it checked the candidate again", a.N, a.State), ""), nil
}

// otherReceiptSteps are the steps of an action whose id names a receipt
// of another operation: recorded refused with the words, the rest not
// applicable.
func otherReceiptSteps(avail deliveryStep, words string) []deliveryStep {
	return []deliveryStep{avail, step(stepRecorded, stepRefused, nil, words, ""), step(stepValidated, stepNotApplicable, nil, words, ""),
		step(stepResumed, stepNotApplicable, nil, words, "")}
}

// dependent is a task that needs the one an Accept took, with its
// attempts, every one kept.
type dependent struct {
	id       string
	attempts []crew.AttemptState
}

// taskNow is the task as the engine holds it, or as the ledger has it for
// a run the engine let go.
func (s *Server) taskNow(ctx context.Context, runID, taskID string) (*crew.TaskState, []crew.AttemptState) {
	if s.runs == nil {
		return nil, nil
	}
	st, attempts, err := s.runs.TaskOf(runID, taskID)
	if errors.Is(err, crew.ErrRunNotFound) {
		st, attempts, err = s.taskFromLedger(ctx, runID, taskID)
	}
	if err != nil {
		return nil, nil
	}
	return &st, attempts
}

// runDependents is the tasks that need taskID directly, from the run as
// the engine holds it or, for a run it let go, as its record keeps it;
// known false when neither holds the run: what needs the task is not
// known here, never taken for nothing.
func (s *Server) runDependents(runID, taskID string) (deps []crew.TaskState, known bool) {
	run, ok := s.runOrRecord(runID)
	if !ok {
		return nil, false
	}
	for _, t := range run.Tasks {
		if slices.Contains(t.Needs, taskID) {
			deps = append(deps, t)
		}
	}
	return deps, true
}

// startedSince is the attempt of attempts that started first at or
// after since, nil for none.
func startedSince(attempts []crew.AttemptState, since time.Time) *crew.AttemptState {
	var first *crew.AttemptState
	for i := range attempts {
		if a := attempts[i]; a.StartedAt != nil && !a.StartedAt.Before(since) && (first == nil || a.StartedAt.Before(*first.StartedAt)) {
			first = &attempts[i]
		}
	}
	return first
}

// doneResumed is the resumed step of a command the engine took: a retry's
// next attempt running, an accept's dependents dispatched (the first
// attempt of any of them that started since, read from every attempt it
// keeps, so that a later attempt reserved and not started yet, or one
// that failed, does not hide the start that resumed the work), a
// cancel's nothing (a Check again's is recheckResumed).
func doneResumed(row ledger.CommandRow, rc crew.Receipt, task *crew.TaskState, attempts []crew.AttemptState, deps []dependent, depsKnown bool) deliveryStep {
	switch row.Intent {
	case crew.TaskRetry:
		if task == nil {
			return step(stepResumed, stepUnknown, nil, "the task is not held here any more", "")
		}
		if a := startedSince(attempts, row.CreatedAt); a != nil {
			return step(stepResumed, stepDone, a.StartedAt, fmt.Sprintf("attempt %d started", a.N), "")
		}
		return step(stepResumed, stepWaiting, nil, "the next attempt has not started yet", "")
	case crew.TaskAccept:
		if !depsKnown {
			return step(stepResumed, stepUnknown, nil, "the run is not held here; what followed is not known", "")
		}
		if len(deps) == 0 {
			return step(stepResumed, stepDone, timeOf(row.CreatedAt), "its result is the task's; nothing else needs it", "")
		}
		var first *crew.AttemptState
		var by string
		for _, d := range deps {
			if a := startedSince(d.attempts, row.CreatedAt); a != nil && (first == nil || a.StartedAt.Before(*first.StartedAt)) {
				first, by = a, d.id
			}
		}
		if first != nil {
			return step(stepResumed, stepDone, first.StartedAt, by+" started from its result", "")
		}
		return step(stepResumed, stepWaiting, nil, "the tasks that need it have not started yet", "")
	}
	return step(stepResumed, stepNotApplicable, nil, "a cancel resumes nothing: what needs the task is blocked", "")
}

// uncertainResumed is the resumed step of a command the ledger left
// uncertain at a stop. A command's change commits in one transaction with
// its receipt's settlement (Engine.act's second; a change whose commit was
// refused carries its settlement into the commit that makes it), and the
// recovery leaves uncertain only a receipt that was never settled: so
// nothing of the command was made, whatever the task shows now (another
// command's doing, before it or since).
func uncertainResumed() deliveryStep {
	return step(stepResumed, stepNotApplicable, nil, "nothing of it was made: its change commits with its receipt, which was never settled; the task's commands are offered again", "")
}

// planSteps are the four steps of the Dispatch operation id of actor on
// the plan revision ref names: its receipt, its own outcome, and nothing
// resumed (a launch from the ticket page starts the work). The receipt is
// read with how it binds to the revision (dispatch.PlanReceipt): one of
// another operation (another intent, or an approval or withdrawal of
// another revision) is none of this revision's; one whose revision the
// store does not keep (a refused approval) is unknown, never this
// revision's refusal nor another's.
func (s *Server) planSteps(ctx context.Context, ref brakeman.Ref, actor, id string) ([]deliveryStep, error) {
	if s.dispatch == nil {
		return nil, errNoItem
	}
	receipt := "op:" + actor + "/" + id
	avail := step(stepAvailable, stepDone, nil, "the revision was open for the operator", "")
	planID, err := s.planIDOf(ctx, ref)
	if err != nil {
		return nil, err
	}
	rc, bound, found, err := s.dispatch.PlanReceipt(ctx, actor, id, planID)
	if err != nil {
		return nil, err
	}
	switch {
	case !found:
		none := "no receipt of this operation is kept here"
		return []deliveryStep{avail, step(stepRecorded, stepUnknown, nil, none, ""), step(stepValidated, stepUnknown, nil, none, ""), step(stepResumed, stepUnknown, nil, none, "")}, nil
	case bound == dispatch.BoundElsewhere:
		return otherReceiptSteps(avail, "the operation id names another operation, not an approval or withdrawal of "+ref.String()+": none with it was taken for this revision"), nil
	case bound != dispatch.BoundHere:
		w := "Dispatch keeps a receipt with this id but not the revision it named (a refused approval): whether it was this revision's is not known here"
		return []deliveryStep{avail, step(stepRecorded, stepUnknown, nil, w, ""), step(stepValidated, stepUnknown, nil, w, ""), step(stepResumed, stepUnknown, nil, w, "")}, nil
	}
	recorded := step(stepRecorded, stepDone, nil, "Dispatch keeps the operation's receipt", receipt)
	if rc.Status != dispatch.StatusDone {
		w := fmt.Sprintf("refused (%s): %s", rc.Refusal, rc.Error)
		return []deliveryStep{avail, recorded, step(stepValidated, stepRefused, nil, w, receipt), step(stepResumed, stepNotApplicable, nil, w, "")}, nil
	}
	validated := step(stepValidated, stepDone, nil, "Dispatch held it against the revision and the ticket as they were", receipt)
	resumed := "a launch from the ticket page starts it"
	if rc.Intent == dispatch.IntentPlanWithdraw {
		resumed = "a withdrawal resumes nothing"
	}
	return []deliveryStep{avail, recorded, validated, step(stepResumed, stepNotApplicable, nil, resumed, "")}, nil
}

// planIDOf is the id of the plan revision ref names; "" for none (its
// ticket gone, or no such revision), which no operation names.
func (s *Server) planIDOf(ctx context.Context, ref brakeman.Ref) (string, error) {
	list, err := s.dispatch.Plans(ctx, ref.Ticket)
	if errors.Is(err, dispatch.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, p := range list {
		if p.N == ref.N {
			return p.ID, nil
		}
	}
	return "", nil
}

// handleInboxTrack answers the four steps of one action, from its durable
// receipt: {ref, receipt, steps}. ref names the item; receipt is the
// command id (task), the Dispatch op id (plan) or the question's id
// (question), and attempt, for a question, the request id the answer was
// sent with.
func (s *Server) handleInboxTrack(w http.ResponseWriter, r *http.Request) {
	q, ok := inboxQuery(w, r, "ref", "receipt", "attempt")
	if !ok {
		return
	}
	if n, is, err := decisionRefN(q["ref"]); is {
		if err != nil || q["attempt"] != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "ref is a decision's ref and receipt response:<responseId> or comment:<opId>, with no attempt")
			return
		}
		s.handleDecisionTrack(w, r, n, q["receipt"])
		return
	}
	ref, err := brakeman.ParseRef(q["ref"])
	receipt, attempt := q["receipt"], q["attempt"]
	if err != nil || len(receipt) > 64 || len(attempt) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_request", "ref is an item's ref, receipt its action's id, attempt an answer's request id")
		return
	}
	var steps []deliveryStep
	switch ref.Kind {
	case brakeman.RefQuestion:
		if receipt != "" && receipt != ref.Question {
			// An action on a question's item that is no answer (a
			// blocker's task command) is tracked under its own ref
			// (the action's track), never read as the question's.
			writeError(w, http.StatusBadRequest, "invalid_request", "a question's receipt is its own id; a task command carried by its item is tracked under the action's track ref")
			return
		}
		d, ok := s.registry.Get(ref.Session)
		if !ok {
			gone := "the session is no longer listed: what it kept is gone"
			steps = []deliveryStep{step(stepAvailable, stepDone, nil, "the session asked it", ""), step(stepRecorded, stepUnknown, nil, gone, ""),
				step(stepValidated, stepUnknown, nil, gone, ""), step(stepResumed, stepUnknown, nil, gone, "")}
			break
		}
		var known bool
		if steps, known = s.questionSteps(d, ref.Question, attempt); !known {
			gone := "the session keeps no result of this question (it keeps its last 64)"
			steps = []deliveryStep{step(stepAvailable, stepDone, nil, "the session asked it", ""), step(stepRecorded, stepUnknown, nil, gone, ""),
				step(stepValidated, stepUnknown, nil, gone, ""), step(stepResumed, stepUnknown, nil, gone, "")}
		}
	case brakeman.RefTask:
		if !crew.CommandIDPattern.MatchString(receipt) {
			writeError(w, http.StatusBadRequest, "invalid_request", "receipt is the command's id")
			return
		}
		if steps, err = s.commandSteps(r.Context(), ref.Run, ref.Task, actorOf(r), receipt); err != nil {
			s.taskError(w, "track", ref.Run, err)
			return
		}
	case brakeman.RefPlan:
		if !dispatch.OpIDPattern.MatchString(receipt) {
			writeError(w, http.StatusBadRequest, "invalid_request", "receipt is the operation's id")
			return
		}
		if steps, err = s.planSteps(r.Context(), ref, actorOf(r), receipt); err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable", "the operation's receipt could not be read")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "ref is a question:, task: or plan: ref")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref.String(), "receipt": receipt, "steps": steps})
}
