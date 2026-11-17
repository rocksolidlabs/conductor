package crew

import (
	"context"
	"errors"
	"fmt"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Who may answer a comparison's selection, and what its lanes take once
// it is applied (docs/plans/compare-crew.md, I7 and I9): only the
// operator answers it, with a rationale (RespondDecision); it is never
// routed, never given to the decide grant (RouteDecision, EntriesOf,
// AdmissionOf, and the ledger's RouteFor) and never cancelled
// (CancelDecision); after its application a task command on a lane not
// selected is refused before anything is written (act).

// The refusals' words.
const (
	selectRationaleSay = "say why: the rationale is the record of this selection"
	selectNoCancel     = "a selection is not cancelled: answer None of these, with a rationale, to end the comparison with no candidate"
)

// NotSelectedError is the refusal of a lane not selected, in words that
// say what a person does next: an ErrNotSelected (compare.go), which the
// API answers 409 not_selected. Nothing is written before it.
type NotSelectedError struct{ Words string }

func (e *NotSelectedError) Error() string        { return e.Words }
func (e *NotSelectedError) Is(target error) bool { return target == ErrNotSelected }

// laneSelected refuses a task command on a task of a comparison's lane
// that the chain's applied selection did not select (I9): after None of
// these, every lane. Before an applied selection every lane takes its
// commands, which supersede an open or answered selection. The caller
// holds e.mu.
func (r *run) laneSelected(intent, taskID string) error {
	if r.compare == nil {
		return nil
	}
	sel := r.sel.applied
	if sel == nil {
		if intent == intentRequestChanges {
			// Only a selected candidate is repaired (decision 11): a repair
			// before the selection would make the candidates unlike.
			return &NotSelectedError{Words: "select a candidate first: a repair before the selection would make the candidates unlike"}
		}
		return nil
	}
	lane, ok := r.lanes[taskID]
	if !ok {
		lane = taskID
	}
	if sel.None {
		return &NotSelectedError{Words: fmt.Sprintf("no candidate was selected in %s (None of these): launch a new comparison to try %s again", sel.Decision, lane)}
	}
	if lane != sel.Task {
		return &NotSelectedError{Words: fmt.Sprintf("%s was not selected in %s: launch a new comparison to try it again", lane, sel.Decision)}
	}
	return nil
}

// refuseSelection refuses an operation of decision id that a comparison's
// selection never takes (the operator's cancel, a route), in words with
// the id: read from the ledger before anything is written, since a
// record's origin never changes. An id of no decision is left to the
// operation's own answer; a read that fails refuses the operation with its
// error, nothing written.
func (e *Engine) refuseSelection(ctx context.Context, id, words string) error {
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return nil
	}
	rec, found, err := e.readDecision(ctx, n)
	switch {
	case errors.Is(err, errNoDecisions):
		return nil // a ledger that keeps no decisions has no selection
	case err != nil:
		return err
	case !found || rec.Origin != ledger.OriginCompare:
		return nil
	}
	return refuse(CodeInvalid, words, id)
}
