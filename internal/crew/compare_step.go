package crew

import (
	"context"
	"slices"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The lanes settled (docs/plans/compare-crew.md, PR-2, Settling): a lane
// has ended when its candidate task is terminal and each review of it is
// terminal or doomed (it waits behind a candidate that failed or was
// cancelled, and nothing will change it); a review's needs_changes
// reopening its candidate is no end. When every lane has ended, the shown
// candidates of the lanes with a standing result are the settled set, and
// compare.settled records it once for each settling, in the change that
// ends the last lane, or in the first change that finds the lanes ended
// with no settling of them recorded.

// compareState is what a compare run keeps of its lanes' settling.
type compareState struct {
	// settled is the set last written in compare.settled while every
	// lane has stayed ended since (empty, not nil, for a settling with no
	// standing result), nil while a lane has not ended.
	settled []SettledCandidate
	// events are the compare step's events of the change being made,
	// written after the change's own (commitHeld).
	events []event
}

// compareStep settles a compare run's lanes inside each change of it,
// whatever made the change, a stop's and a continuation's first
// transaction included. The caller holds e.mu, inside a change, after its
// mutate and before its ended work: while a lane has not ended, settled
// is none; once every lane has, and the settled set is not the one last
// written, compare.settled is written with the set, in this change.
func (e *Engine) compareStep(r *run) {
	// The selection kept current first: a lane changed since supersedes it
	// (compare_select.go).
	e.selectStep(r)
	// The lanes settle until a selection of the chain is applied, after
	// which nothing of the comparison is settled again.
	if !r.selectionApplied() {
		e.settleLanes(r)
	}
	// Then the selection opened once every lane has ended, and the run
	// settled (compare_select.go).
	e.selectEnd(r)
}

// settleLanes is the settling of compareStep: compare.settled written
// once for each settled set. The caller holds e.mu, inside a change.
func (e *Engine) settleLanes(r *run) {
	for _, cd := range r.compare.Candidates {
		if !r.laneEnded(cd.Task) {
			r.cmp.settled = nil
			return
		}
	}
	set := r.settledSet()
	if r.cmp.settled != nil && slices.Equal(r.cmp.settled, set) {
		return
	}
	r.cmp.settled = set
	// A new set is looked at again by the selection (compare_select.go):
	// an Accept anyway after a settling with no candidate opens one.
	r.sel.tried = false
	r.cmp.events = append(r.cmp.events, event{eventCompareSettled, map[string]any{"candidates": slices.Clone(set)}})
}

// laneEnded reports whether the lane of the candidate task cand has ended.
// The caller holds e.mu.
func (r *run) laneEnded(cand string) bool {
	t := r.task(cand)
	if t == nil || !terminalTask(t.state) {
		return false
	}
	doomed := r.doomed()
	for _, rv := range r.tasks {
		if rv == t || r.lanes[rv.def.ID] != cand {
			continue
		}
		if !terminalTask(rv.state) && !doomed[rv] {
			return false
		}
	}
	return true
}

// shownCandidate is the lane's standing result: its task, the run that
// made it (an imported one's source), its attempt and its commit; ok false
// for a lane with none. The caller holds e.mu.
func (r *run) shownCandidate(cand string) (SettledCandidate, bool) {
	t := r.task(cand)
	if t == nil || t.state != ledger.TaskSucceeded || t.result == nil {
		return SettledCandidate{}, false
	}
	run := r.id
	if src, tid, n, ok := parseImport(t.imported); ok && tid == cand && n == t.result.attempt {
		run = src
	}
	return SettledCandidate{Task: cand, Run: run, Attempt: t.result.attempt, Commit: t.result.commit}, true
}

// settledSet is the shown candidates of the lanes that have one, in the
// block's order. The caller holds e.mu.
func (r *run) settledSet() []SettledCandidate {
	set := []SettledCandidate{}
	for _, cd := range r.compare.Candidates {
		if sc, ok := r.shownCandidate(cd.Task); ok {
			set = append(set, sc)
		}
	}
	return set
}

// readCompareHistory is the chain's newest compare.settled, read before a
// continuation's first transaction with the chain's other reads: the set
// its compare step starts from (nil for none). chain is the run continued
// and the runs it continues, newest first; a ledger that cannot read
// events gives none, so the continuation settles its lanes again.
func (e *Engine) readCompareHistory(ctx context.Context, from Loaded) ([]SettledCandidate, error) {
	cl, ok := e.ledger.(compareLedger)
	if !ok {
		return nil, nil
	}
	seen := map[string]bool{}
	for id, n := from.Run.ID, 0; id != "" && !seen[id] && n < maxCompareChain; n++ {
		seen[id] = true
		ev, ok, err := cl.NewestEvent(ctx, id, eventCompareSettled)
		if err != nil {
			return nil, err
		}
		if ok {
			return settledOf(ev.Payload), nil
		}
		p, ok, err := cl.Pointer(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		id = p.ContinuedFrom
	}
	return nil, nil
}
