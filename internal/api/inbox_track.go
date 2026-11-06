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

// questionSteps are the four steps of an answer to the question id of the
// session d, attempt the request id the browser sent it with ("" when it
// is not known); known is false when the session keeps nothing of the
// question (never asked as far as it knows, or forgotten) and it is not
// shown.
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
	if hosted {
		res, seen := s.hostedSeen.of(info.ID, id)
		if !seen && !shown {
			return nil, false
		}
		out := []deliveryStep{avail, step(stepRecorded, stepUnknown, nil, hostHoldsWords, ""), step(stepValidated, stepUnknown, nil, hostHoldsWords, "")}
		switch {
		case res != nil:
			out = append(out, resumedStep(*res))
		case shown:
			out = append(out, step(stepResumed, stepWaiting, nil, "the question still waits", ""))
		default:
			out = append(out, step(stepResumed, stepUnknown, nil, notKnownResumed, ""))
		}
		return out, true
	}
	local, ok := d.(*session.Local)
	if !ok {
		return nil, false
	}
	r, answered := local.QuestionResult(id)
	if !answered {
		if left, ok := local.QuestionLeft(id); ok {
			if left.How == session.LeftTerminal {
				return terminalSteps(avail, &left.At, receipt), true
			}
			words := "the question was withdrawn before an answer was taken: nothing was typed"
			return []deliveryStep{avail, step(stepRecorded, stepRefused, &left.At, words, receipt), step(stepValidated, stepNotApplicable, nil, words, ""),
				step(stepResumed, stepNotApplicable, nil, words, "")}, true
		}
		if shown {
			return []deliveryStep{avail, step(stepRecorded, stepWaiting, nil, "not answered yet", ""), step(stepValidated, stepWaiting, nil, "not answered yet", ""),
				step(stepResumed, stepWaiting, nil, "not answered yet", "")}, true
		}
		return nil, false
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
// failed, or uncertain after a stop), then the task as it stands.
func (s *Server) commandSteps(ctx context.Context, runID, taskID, actor, id string) ([]deliveryStep, error) {
	receipt := "command:" + runID + "/" + id
	avail := step(stepAvailable, stepDone, nil, "asked of task "+taskID+" of run "+runID, "")
	if s.ledger == nil {
		return nil, crew.ErrNoLedger
	}
	row, found, err := s.ledger.Receipt(ctx, runID, actor, id)
	if err != nil {
		return nil, err
	}
	if !found {
		none := "no receipt of this command is kept here"
		return []deliveryStep{avail, step(stepRecorded, stepUnknown, nil, none, ""), step(stepValidated, stepUnknown, nil, none, ""), step(stepResumed, stepUnknown, nil, none, "")}, nil
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
	deps, depsKnown := s.runDependents(runID, taskID)
	return []deliveryStep{avail, recorded, validated, doneResumed(row, rc, task, attempts, deps, depsKnown)}, nil
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

// startedSince is the first of attempts started at or after since, nil
// for none.
func startedSince(attempts []crew.AttemptState, since time.Time) *crew.AttemptState {
	for i := range attempts {
		if a := attempts[i]; a.StartedAt != nil && !a.StartedAt.Before(since) {
			return &attempts[i]
		}
	}
	return nil
}

// doneResumed is the resumed step of a command the engine took: a retry's
// next attempt running, an accept's dependents dispatched, a cancel's
// nothing.
func doneResumed(row ledger.CommandRow, rc crew.Receipt, task *crew.TaskState, attempts []crew.AttemptState, deps []crew.TaskState, depsKnown bool) deliveryStep {
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
		for _, d := range deps {
			if a := d.Attempt; a != nil && a.StartedAt != nil && !a.StartedAt.Before(row.CreatedAt) {
				return step(stepResumed, stepDone, a.StartedAt, d.ID+" started from its result", "")
			}
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

// planSteps are the four steps of the Dispatch operation id of actor on a
// plan revision: its receipt, its own outcome, and nothing resumed (a
// launch from the ticket page starts the work).
func (s *Server) planSteps(ctx context.Context, actor, id string) ([]deliveryStep, error) {
	if s.dispatch == nil {
		return nil, errNoItem
	}
	receipt := "op:" + actor + "/" + id
	avail := step(stepAvailable, stepDone, nil, "the revision was open for the operator", "")
	rc, found, err := s.dispatch.Receipt(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if !found {
		none := "no receipt of this operation is kept here"
		return []deliveryStep{avail, step(stepRecorded, stepUnknown, nil, none, ""), step(stepValidated, stepUnknown, nil, none, ""), step(stepResumed, stepUnknown, nil, none, "")}, nil
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
		if steps, err = s.planSteps(r.Context(), actorOf(r), receipt); err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable", "the operation's receipt could not be read")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "ref is a question:, task: or plan: ref")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref.String(), "receipt": receipt, "steps": steps})
}
