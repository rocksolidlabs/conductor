package crew

import (
	"context"
	"encoding/json"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The engine's half of counted approvals (decisions_quorum.go): the waits
// for their sign-in's rechecks, what a recheck writes, and the supersession
// of a decision whose counted approvals' response rows ran short.

// CountedPending says whether decision n's counted response k waits for
// its sign-in's recheck in this process (ConfirmAuthority).
func (e *Engine) CountedPending(n int64, k int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.countedPending[confirmKey{n, k}]
}

// countedWaits says whether a counted response of decision n waits for its
// recheck. The caller holds e.mu.
func (e *Engine) countedWaits(n int64) bool {
	for key := range e.countedPending {
		if key.n == n {
			return true
		}
	}
	return false
}

// keepsNoRoom says a refusal kept on rec, open under its router's policy,
// would leave fewer response rows than the places the policy still needs
// of the counted approvals that stand: such a refusal is not kept, so the
// rows always hold one for each place still to fill, the deciding one
// included.
func keepsNoRoom(rec ledger.DecisionRecord, pol policyIn) bool {
	p, ok := routedOf(rec.Policy)
	if !ok {
		return false
	}
	standing, _ := countedRows(rec, pol.boot, pol.pendingOf(rec.N))
	return ledger.MaxDecisionResponses-(len(rec.Responses)+1) < unmet(p, standing)
}

// replaceTold is replaceDecisionFrom with what follows its commit: the
// question the asking session shows withdrawn, and the replacement's asked
// in its place. The caller holds e.mu, in the change.
func (e *Engine) replaceTold(r *run, d *decision, why, from string) (*decision, tx) {
	replace, t := e.replaceDecisionFrom(r, d, why, from)
	opened := t.done
	t.done = func() {
		opened()
		e.withdrawMirror(d.a, d)
		if replace.a != nil {
			go e.askMirror(r, replace.a, replace)
		}
	}
	return replace, t
}

// shortWords are the words a decision whose response rows ran short is
// superseded with.
func shortWords(id string) string { return id + "'s responses filled its record" }

// supersedeShort supersedes the run's open decision n, whose counted
// approvals' response rows ran short (rowsShort), by a fresh decision
// that takes the place of its spare, in a change of its own with no undo,
// as supersede does a moved subject's: the words it was superseded with,
// "" when, read again under the run's txMu, it is no longer open, no
// longer short, or holds a row of the response actor sent as responseID
// already (a repeat changes nothing). The caller holds no lock.
func (e *Engine) supersedeShort(ctx context.Context, r *run, n int64, actor, responseID, fp string) (string, error) {
	r.txMu.Lock()
	rec, ok, err := e.readDecision(ctx, n)
	if err != nil {
		r.txMu.Unlock()
		return "", err
	}
	if _, found, _ := replayed(rec, actor, responseID, fp); !ok || found || rec.State != ledger.DecisionOpen {
		r.txMu.Unlock()
		return "", nil
	}
	p, routed := routedOf(rec.Policy)
	standing, _ := countedRows(rec, e.boot, func(k int) bool { return e.CountedPending(n, k) })
	if !routed || !rowsShort(rec, p, standing) {
		r.txMu.Unlock()
		return "", nil
	}
	words := shortWords(rec.ID())
	done := ""
	err = e.commitHeld(context.Background(), r, notFirst, nil, func() tx {
		d := r.decisionN(n)
		if r.stopping || r.ctx.Err() != nil || d == nil || d.state != ledger.DecisionOpen {
			return tx{}
		}
		_, t := e.replaceTold(r, d, words, ledger.DecisionOpen)
		done = words
		return t
	}, nil)
	r.txMu.Unlock()
	e.afterTransact(r, err)
	return done, nil
}

// recheckedCounted keeps what the identity provider answered of counted
// response k's sign-in (ConfirmAuthority): a rechecked row, admitted or
// not, in a transaction of the run that holds decision n (through the
// ledger when none does), once. Only then is its wait ended: an approval
// whose row could not be written stands not, as one never rechecked. Not
// admitted, the decision is superseded when its rows ran short; an answered
// decision that waited for the recheck is applied (or superseded) then.
// The caller holds no lock.
func (e *Engine) recheckedCounted(n int64, k int, admitted bool) {
	key := confirmKey{n, k}
	e.mu.Lock()
	var holder *run
	for _, r := range e.runs {
		if r.decisionN(n) != nil && e.continued(r) == nil && !r.unrecorded() {
			holder = r
			break
		}
	}
	e.mu.Unlock()
	write := func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		rec, ok, err := dtx.Record(n)
		if err != nil || !ok {
			return err
		}
		counted := false
		for _, r := range rec.Responses {
			counted = counted || r.K == k && r.Status == ledger.ResponseCounted
		}
		for _, h := range rec.History {
			var dt struct {
				K int `json:"k"`
			}
			if h.Kind == ledger.HistoryRechecked && json.Unmarshal([]byte(h.Detail), &dt) == nil && dt.K == k {
				counted = false // rechecked once already
			}
		}
		if !counted {
			return nil
		}
		_, _, err = dtx.History(n, ledger.HistoryRow{Kind: ledger.HistoryRechecked, By: "yardmaster", Detail: historyDetail(map[string]any{"k": k, "admitted": admitted})})
		return err
	}
	ctx := context.Background()
	if holder != nil {
		if err := e.transact(ctx, holder, func() tx { return tx{more: write} }, func() {}); err == nil {
			e.pumpLocked(holder)
		}
	} else if rec, ok, err := e.readDecision(ctx, n); err == nil && ok {
		_, _ = e.ledger.Transition(ctx, rec.CurrentRun, write)
	}
	e.mu.Lock()
	delete(e.countedPending, key)
	e.mu.Unlock()
	if holder == nil {
		return
	}
	if !admitted {
		_, _ = e.supersedeShort(ctx, holder, n, "", "", "")
	}
	e.applyDecisions(holder)
}
