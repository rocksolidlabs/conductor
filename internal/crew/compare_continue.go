package crew

import (
	"context"
	"fmt"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A comparison's continuation (docs/plans/compare-crew.md, PR-3): a
// continuation carries the chain's crew unchanged, its compare block among
// it, so every run of a comparison's chain is the comparison. Two rules
// hold there, both before anything is written: the chain is bounded
// (compareChainRoom), and a lane no applied selection selects is neither
// repaired nor left without the result it had (compareContinued), so no
// candidate is made unlike the others before the person's choice, on any
// path that continues a run: a restart's Continue, a person's Continue,
// RV1's repair continuation (ContinueRunWith).

// compareRefusal is a comparison's refusal: its words alone, matching the
// sentinel it is.
type compareRefusal struct {
	is    error
	words string
}

func (e *compareRefusal) Error() string { return e.words }
func (e *compareRefusal) Unwrap() error { return e.is }

// compareChainRoom refuses a continuation of a comparison's chain that
// holds maxCompareChain runs already, from among them: the comparison
// reads its chain whole (Engine.Comparison stops at the same bound), so
// one continued further is launched anew. It matches ErrChainTooLong.
// The chain is the one readDecisionHistory read when the ledger keeps
// decisions, else walked here, at most maxCompareChain runs back.
func (e *Engine) compareChainRoom(ctx context.Context, from Loaded, h history) error {
	n := len(h.dec.chain)
	if _, ok := e.ledger.(decisionLedger); !ok {
		n = 1
		seen := map[string]bool{from.Run.ID: true}
		for prev := from.Run.ContinuedFrom; prev != "" && !seen[prev] && n < maxCompareChain; n++ {
			seen[prev] = true
			loaded, found, err := e.ledger.Load(ctx, prev)
			if err != nil {
				return fmt.Errorf("the ledger could not read run %s: %w", prev, err)
			}
			if !found {
				break
			}
			prev = loaded.Run.ContinuedFrom
		}
	}
	if n >= maxCompareChain {
		return &compareRefusal{is: ErrChainTooLong, words: fmt.Sprintf("this comparison has been continued %d times: launch a new comparison", maxCompareChain-1)}
	}
	return nil
}

// compareSelected is the lane (its candidate task) the chain's applied
// selection selects (read with the chain's decisions,
// readDecisionHistory), "" when none is applied or None of these was.
func (h history) compareSelected() string {
	if sel := h.dec.selection; sel != nil && !sel.None {
		return sel.Task
	}
	return ""
}

// compareContinued judges a comparison's continuation r once each task's
// import and repair are decided, inside its first change, before anything
// is readied: a task of a lane other than selected (the lane an applied
// selection selects, "" for none) that succeeded in the source (its row in
// rows) and is not imported, or that starts with a repair, refuses the
// continuation (ErrNotSelected, in words). A plain continuation imports
// every result it can find and carries no repair. Nothing for a run of no
// comparison. The caller holds e.mu or knows r is not shared yet.
func (r *run) compareContinued(rows map[string]ledger.TaskRow, selected string) error {
	if r.compare == nil {
		return nil
	}
	for _, t := range r.tasks {
		lane := r.lanes[t.def.ID]
		if lane == "" || lane == selected {
			continue
		}
		row, ok := rows[t.def.ID]
		succeeded := ok && row.State == ledger.TaskSucceeded && row.ResultAttempt > 0 && row.ResultAttempt > row.Withdrawn
		if (succeeded && t.imported == "") || t.repair != nil {
			return &compareRefusal{is: ErrNotSelected, words: "a comparison's candidate is repaired once it is selected"}
		}
	}
	return nil
}
