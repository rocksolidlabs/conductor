package crew

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Continuations and recovery of decisions (docs/plans/decisions.md,
// Continuations and recovery; O6, I3, I6, I16): a continuation's first
// transaction moves the open and answered decisions of the run it
// continues, with their holds, gives back an interrupted attempt that
// waited on one (once per attempt, whatever decision), marks a typing a
// restart cut uncertain, and asks again for a gate's requirement that
// work made runnable again; the answered ones are applied after it.

// decisionLedger is the ledger's reads of decisions a continuation and a
// task file make: ledger.Ledger's.
type decisionLedger interface {
	Decision(ctx context.Context, n int64) (ledger.DecisionRecord, bool, error)
	Decisions(ctx context.Context, f ledger.DecisionFilter) ([]ledger.DecisionRow, error)
	Holds(ctx context.Context, run string) ([]ledger.HoldRow, error)
	RunRow(ctx context.Context, id string) (ledger.RunRow, bool, error)
}

func (s sqlLedger) Decisions(ctx context.Context, f ledger.DecisionFilter) ([]ledger.DecisionRow, error) {
	return s.l.Decisions(ctx, f)
}

func (s sqlLedger) Holds(ctx context.Context, run string) ([]ledger.HoldRow, error) {
	return s.l.Holds(ctx, run)
}

func (s sqlLedger) RunRow(ctx context.Context, id string) (ledger.RunRow, bool, error) {
	return s.l.Run(ctx, id)
}

// decisionHistory is what a continuation takes of the decisions of the
// runs it continues, read before its first transaction (the ledger's one
// connection serves no reader inside one).
type decisionHistory struct {
	// chain is the run continued and the runs it continues, newest first.
	chain []string
	// moving are the run continued's open and answered decisions, by n,
	// with their responses and history; holds its hold rows.
	moving []ledger.DecisionRecord
	holds  []ledger.HoldRow
	// settled are the chain's applied approvals of an agent (S1, S4).
	settled []ledger.DecisionRecord
	// uncertain are the applied decisions whose typing has no end: the
	// server ended while their line was typed (I6).
	uncertain []int64
	// gates says the chain opened gates; released the plan decisions it
	// released, lastGate each gate's decision last closed without it.
	gates    bool
	released map[string]bool
	lastGate map[string]gateEnd
}

// allDecisions reads every decision run holds, newest first.
func allDecisions(ctx context.Context, dl decisionLedger, f ledger.DecisionFilter) ([]ledger.DecisionRow, error) {
	var out []ledger.DecisionRow
	for {
		rows, err := dl.Decisions(ctx, f)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < ledger.MaxDecisionsPage {
			return out, nil
		}
		f.Before = rows[len(rows)-1].N
	}
}

// deliveryOf is the last delivery row of a record's history ("" for none),
// and whether its task file carried it.
func deliveryOf(rec ledger.DecisionRecord) (last string, taskFiled bool) {
	for _, h := range rec.History {
		switch h.Kind {
		case ledgerTyping, ledgerEntered, ledgerUntyped, ledgerUncertain:
			last = h.Kind
		case ledger.HistoryDeliveryTaskFile:
			taskFiled = true
		}
	}
	return last, taskFiled
}

// applicationOf is a row's application receipt, zero for none.
func applicationOf(raw string) Application {
	var app Application
	_ = json.Unmarshal([]byte(raw), &app)
	return app
}

// waived reports whether a gate's cancel released its tasks (the
// operator's cancel with release: the one explicit waiver).
func waived(rec ledger.DecisionRecord) bool {
	for _, h := range rec.History {
		if h.Kind == ledger.HistoryCancelled {
			var dt opDetail
			_ = json.Unmarshal([]byte(h.Detail), &dt)
			return dt.Release
		}
	}
	return false
}

// readDecisionHistory reads what a continuation of from takes of the
// decisions of its chain. A ledger that keeps none has none.
func (e *Engine) readDecisionHistory(ctx context.Context, from Loaded) (decisionHistory, error) {
	dh := decisionHistory{chain: []string{from.Run.ID}, released: map[string]bool{}, lastGate: map[string]gateEnd{}}
	dl, ok := e.ledger.(decisionLedger)
	if !ok {
		return dh, nil
	}
	seen := map[string]bool{from.Run.ID: true}
	for prev := from.Run.ContinuedFrom; prev != "" && !seen[prev] && len(dh.chain) < maxChain; {
		seen[prev] = true
		row, ok, err := dl.RunRow(ctx, prev)
		if err != nil {
			return decisionHistory{}, fmt.Errorf("the ledger could not read run %s: %w", prev, err)
		}
		if !ok {
			break
		}
		dh.chain = append(dh.chain, prev)
		prev = row.ContinuedFrom
	}
	record := func(n int64) (ledger.DecisionRecord, error) {
		rec, ok, err := dl.Decision(ctx, n)
		if err == nil && !ok {
			err = fmt.Errorf("%w: %s", ledger.ErrNoDecision, ledger.DecisionID(n))
		}
		return rec, err
	}
	for _, runID := range dh.chain {
		rows, err := allDecisions(ctx, dl, ledger.DecisionFilter{CurrentRun: runID})
		if err != nil {
			return decisionHistory{}, err
		}
		// Newest first: a gate's last decision is the first seen.
		for _, row := range rows {
			if row.Origin == ledger.OriginPlan {
				dh.gates = true
			}
			switch {
			case runID == from.Run.ID && (row.State == ledger.DecisionOpen || row.State == ledger.DecisionAnswered):
				rec, err := record(row.N)
				if err != nil {
					return decisionHistory{}, err
				}
				dh.moving = append(dh.moving, rec)
			case row.Origin == ledger.OriginPlan && row.State == ledger.DecisionApplied:
				switch app := applicationOf(row.Applied); app.How {
				case HowReleased, HowNoted:
					dh.released[row.Gate] = true
				case HowCancelled:
					if _, ok := dh.lastGate[row.Gate]; !ok {
						dh.lastGate[row.Gate] = gateEnd{id: row.ID(), rejected: true}
					}
				}
			case row.Origin == ledger.OriginPlan && row.State == ledger.DecisionCancelled:
				rec, err := record(row.N)
				if err != nil {
					return decisionHistory{}, err
				}
				if waived(rec) {
					dh.released[row.Gate] = true
				} else if _, ok := dh.lastGate[row.Gate]; !ok {
					dh.lastGate[row.Gate] = gateEnd{id: row.ID()}
				}
			case row.Origin == ledger.OriginAgent && row.State == ledger.DecisionApplied:
				app := applicationOf(row.Applied)
				if row.Type != ledger.DecisionApproval && app.How != HowTyped {
					continue
				}
				rec, err := record(row.N)
				if err != nil {
					return decisionHistory{}, err
				}
				if last, _ := deliveryOf(rec); last == ledgerTyping {
					dh.uncertain = append(dh.uncertain, row.N)
				}
				if row.Type == ledger.DecisionApproval {
					dh.settled = append(dh.settled, rec)
				}
			}
		}
	}
	slices.SortFunc(dh.moving, func(a, b ledger.DecisionRecord) int { return cmp.Compare(a.N, b.N) })
	holds, err := dl.Holds(ctx, from.Run.ID)
	if err != nil {
		return decisionHistory{}, err
	}
	dh.holds = holds
	return dh, nil
}

// acceptedOf is a record's accepted response, nil for none.
func acceptedOf(rec ledger.DecisionRecord) *accepted {
	for _, rr := range rec.Responses {
		if rr.Status != ledger.ResponseAccepted {
			continue
		}
		var rc ResponseReceipt
		_ = json.Unmarshal([]byte(rr.Receipt), &rc)
		var auth Authority
		_ = json.Unmarshal([]byte(rr.Authority), &auth)
		return &accepted{k: rr.K, choice: rc.Choice, text: rc.Text, rationale: rc.Rationale, byName: rc.ByName, actor: rr.Actor, auth: auth}
	}
	return nil
}

// decisionOfRecord is the engine's copy of a decision the run takes from
// the ledger: its asking task the run's own (no attempt of it is live),
// its question, options and subject from its request, its answer, how its
// line went. The caller holds e.mu.
func decisionOfRecord(r *run, rec ledger.DecisionRecord) *decision {
	var body requestBody
	_ = json.Unmarshal([]byte(rec.Request), &body)
	d := &decision{n: rec.N, origin: rec.Origin, typ: rec.Type, gate: rec.Gate, question: body.Question, state: rec.State, rev: rec.Rev, subject: body.Subject, options: optionsOf(rec.Type, rec.Request), openedAt: rec.CreatedAt}
	if rec.Origin == ledger.OriginAgent {
		d.t = r.task(rec.TaskID)
	}
	if rec.State == ledger.DecisionAnswered {
		d.answer = acceptedOf(rec)
	}
	d.delivery, d.taskFiled = deliveryOf(rec)
	return d
}

// refundPlan is an interrupted attempt a continuation gives back when no
// refund names it yet.
type refundPlan struct {
	t   *task
	run string
	n   int
	d   int64
}

// continueDecisions takes the decisions of the run continued into r, in
// the continuation's first change: the chain, the gates' requirements, the
// moving decisions with their holds and the settled ones in the engine;
// its transaction's part (moving them and their holds, the give-backs, the
// uncertain typings); regate at the end. The caller holds e.mu, in the
// continuation's first change.
func (e *Engine) continueDecisions(r *run, from Loaded, h history) func(LedgerTx) error {
	dh := h.dec
	r.dec.chain = append([]string{r.id}, dh.chain...)
	r.dec.lastGate = dh.lastGate
	if r.pkg != nil && len(r.pkg.p.Decisions) > 0 && (dh.gates || e.gatesAtLaunch) {
		r.dec.gates = map[string]bool{}
		for _, pd := range r.pkg.p.Decisions {
			r.dec.gates[pd.ID] = dh.released[pd.ID]
		}
	}
	for _, rec := range dh.moving {
		d := decisionOfRecord(r, rec)
		r.decisions = append(r.decisions, d)
		for _, hr := range dh.holds {
			if hr.Decision == rec.N && hr.ReleasedAt.IsZero() {
				if t := r.task(hr.TaskID); t != nil {
					r.hold(t, d)
				}
			}
		}
	}
	for _, rec := range dh.settled {
		d := decisionOfRecord(r, rec)
		if slices.Contains(dh.uncertain, rec.N) {
			d.delivery = ledgerUncertain
		}
		r.dec.settled = append(r.dec.settled, d)
	}
	// An attempt the server's end cut while it waited on a decision is
	// given back once (I3), whatever decision it waited on then.
	var refunds []refundPlan
	for _, t := range r.tasks {
		la, ok := h.last[t.def.ID]
		if !ok || la.State != ledger.AttemptInterrupted || la.ResultCommit != "" {
			continue
		}
		for _, d := range r.decisions {
			rec := recordN(dh.moving, d.n)
			if d.origin == ledger.OriginAgent && d.t == t && rec.Attempt == la.N && d.waits() {
				refunds = append(refunds, refundPlan{t: t, run: la.RunID, n: la.N, d: d.n})
				break
			}
		}
	}
	e.regate(r, "the run was continued")
	fromID, to := from.Run.ID, r.id
	uncertain := slices.Clone(dh.uncertain)
	moving := make([]int64, 0, len(dh.moving))
	for _, rec := range dh.moving {
		moving = append(moving, rec.N)
	}
	return func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			if len(dh.moving) == 0 && len(dh.uncertain) == 0 {
				return nil // a ledger that keeps no decisions has none to move
			}
			return err
		}
		mv, ok := dtx.(interface {
			MoveDecisions(from, to string) ([]ledger.DecisionRow, error)
			MoveHolds(from, to string, at time.Time) (int, error)
		})
		if !ok {
			return errNoDecisions
		}
		rows, err := mv.MoveDecisions(fromID, to)
		if err != nil {
			return err
		}
		// The rows moved are the ones read, or nothing is made: a decision
		// closed or opened on the run continued since the read would leave
		// the engine holding what the ledger does not (#636, review round
		// 3). Every change of a run the engine holds refuses once the run
		// is marked (continued), before the read, so this refuses only
		// what nothing should have written.
		if !sameNs(rows, moving) {
			return fmt.Errorf("%w: its decisions changed as it was continued", ErrRunUnsettled)
		}
		if _, err := mv.MoveHolds(fromID, to, dtx.Now()); err != nil {
			return err
		}
		// The engine's copy follows the rows that write answers: one
		// answered on the old run since it was read is moved as answered,
		// with its answer read here, in the move's transaction, so it is
		// the engine's once the move commits and never lost to a read
		// after it (#636, review round 2).
		for _, row := range rows {
			if row.State != ledger.DecisionAnswered {
				continue
			}
			e.mu.Lock()
			d := r.decisionN(row.N)
			stale := d != nil && d.state == ledger.DecisionOpen
			e.mu.Unlock()
			if !stale {
				continue
			}
			rec, ok, err := dtx.Record(row.N)
			if err == nil && !ok {
				err = fmt.Errorf("%w: %s", ledger.ErrNoDecision, row.ID())
			}
			if err != nil {
				return err
			}
			ans := acceptedOf(rec)
			e.mu.Lock()
			d.state, d.rev, d.answer = ledger.DecisionAnswered, row.Rev, ans
			e.mu.Unlock()
		}
		for _, p := range refunds {
			wrote, err := refund(dtx, p.run, p.t.def.ID, p.n, p.d)
			if err != nil {
				return err
			}
			if !wrote {
				continue // given back already, by an earlier continuation
			}
			e.mu.Lock()
			p.t.extra++
			row := r.taskRow(p.t)
			r.note(session.ActivityStatus, "task %s: attempt %d was interrupted while it waited for %s: given back, not counted", p.t.def.ID, p.n, ledger.DecisionID(p.d))
			e.mu.Unlock()
			if err := ltx.PutTask(row); err != nil {
				return err
			}
			if err := ltx.Event("attempt.waited", map[string]any{"task": p.t.def.ID, "attempt": p.n, "decision": ledger.DecisionID(p.d), "run": p.run}); err != nil {
				return err
			}
		}
		for _, n := range uncertain {
			// Its typing was under way when the server ended: never typed
			// again (I6); the task's next attempt reads it in its files.
			// Read again here, in the move's transaction: only a typing
			// the ledger still holds without its end is marked, so a line's
			// end written since the read is never followed by a second end
			// (#636, review round 3).
			rec, ok, err := dtx.Record(n)
			if err == nil && !ok {
				err = fmt.Errorf("%w: %s", ledger.ErrNoDecision, ledger.DecisionID(n))
			}
			if err != nil {
				return err
			}
			last, _ := deliveryOf(rec)
			if last != ledgerTyping {
				e.mu.Lock()
				if d := settledN(r, n); d != nil {
					d.delivery = last
				}
				e.mu.Unlock()
				continue
			}
			if _, _, err := dtx.History(n, ledger.HistoryRow{Kind: ledgerUncertain, By: "yardmaster", Detail: historyDetail(map[string]any{"why": "the server ended while its line was typed: it is never typed again"})}); err != nil {
				return err
			}
		}
		return nil
	}
}

// sameNs says rows are the decisions numbered ns, in their order.
func sameNs(rows []ledger.DecisionRow, ns []int64) bool {
	return slices.EqualFunc(rows, ns, func(row ledger.DecisionRow, n int64) bool { return row.N == n })
}

// recordN is the record of decision n among recs, zero for none.
func recordN(recs []ledger.DecisionRecord, n int64) ledger.DecisionRecord {
	for _, rec := range recs {
		if rec.N == n {
			return rec
		}
	}
	return ledger.DecisionRecord{}
}

// recoveredHolds are the holds a recovered record's tasks show: the run's
// active hold rows, with their decisions.
func recoveredHolds(from Loaded, taskID string) []TaskHold {
	var out []TaskHold
	for _, h := range from.Holds {
		if h.TaskID != taskID || !h.ReleasedAt.IsZero() {
			continue
		}
		row := from.HeldDecisions[h.Decision]
		var body requestBody
		_ = json.Unmarshal([]byte(row.Request), &body)
		out = append(out, TaskHold{ID: ledger.DecisionID(h.Decision), Type: row.Type, State: row.State, Question: body.Question})
	}
	return out
}
