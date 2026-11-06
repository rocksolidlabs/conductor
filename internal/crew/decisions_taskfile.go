package crew

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The task file's decisions (docs/plans/decisions.md, The task file): an
// attempt's files carry each decision of its task's run chain that holds
// or held the task, or that the task asked, with its state and, once
// applied, the answer, so an answer that was not typed into the asking
// attempt reaches the task's next one (I4), and an applied gate's answer
// stays in every task it released.

// TaskDecision is a decision as a task file carries it, in the record's
// words.
type TaskDecision struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Origin    string `json:"origin"`
	Gate      string `json:"gate,omitempty"`
	Question  string `json:"question"`
	State     string `json:"state"`
	Answer    string `json:"answer,omitempty"`
	Rationale string `json:"rationale,omitempty"`
	By        string `json:"by,omitempty"`
	Words     string `json:"words,omitempty"`
}

// maxTaskDecisions bounds the decisions a task file carries: the newest.
const maxTaskDecisions = 20

// answerOf is a record's accepted answer in words: the option chosen and
// the text, the rationale, and who gave it.
func answerOf(rec ledger.DecisionRecord) (answer, rationale, by string) {
	ans := acceptedOf(rec)
	if ans == nil {
		return "", "", ""
	}
	options := optionsOf(rec.Type, rec.Request)
	if ans.choice != nil && *ans.choice >= 0 && *ans.choice < len(options) {
		answer = options[*ans.choice]
	}
	if ans.text != "" {
		if answer != "" {
			answer += ": "
		}
		answer += ans.text
	}
	return answer, ans.rationale, nameOf(ans.byName, ans.actor)
}

// taskDecisionOf is a record as a task file carries it.
func taskDecisionOf(rec ledger.DecisionRecord) TaskDecision {
	var body requestBody
	_ = json.Unmarshal([]byte(rec.Request), &body)
	td := TaskDecision{ID: rec.ID(), Type: rec.Type, Origin: rec.Origin, Gate: rec.Gate, Question: body.Question, State: rec.State, Words: rec.Why}
	if rec.State == ledger.DecisionApplied || rec.State == ledger.DecisionAnswered || rec.State == ledger.DecisionSuperseded && rec.Applied != "" {
		td.Answer, td.Rationale, td.By = answerOf(rec)
	}
	return td
}

// taskDecisions are the decisions a's task files carry, read from the
// ledger before they are written. An applied approval of a commit or a
// file whose line is not known to have gone into its asking attempt is
// rechecked first against a's start, read from the object store (S4): the
// same commit or blob carries it as applied, another supersedes it, and
// the files say so; each applied answer the files carry first is marked
// so (delivery.task_file), once. The transaction checks a's start is the
// one the recheck read. The caller holds no lock.
func (e *Engine) taskDecisions(ctx context.Context, r *run, a *attempt) ([]TaskDecision, error) {
	dl, ok := e.ledger.(decisionLedger)
	if !ok {
		return nil, nil
	}
	e.mu.Lock()
	chain := slices.Clone(r.dec.chain)
	if len(chain) == 0 {
		chain = []string{r.id}
	}
	taskID, start := a.t.def.ID, a.start
	e.mu.Unlock()
	// Each run of the chain's own, by the run that holds it now: a
	// decision of the chain is held by one of its runs, and another run's
	// decisions about a task of the same id are never read in their place
	// (#636, review round 1). The newest maxTaskDecisions of each run hold
	// the chain's newest.
	var rows []ledger.DecisionRow
	for _, runID := range chain {
		got, err := dl.Decisions(ctx, ledger.DecisionFilter{CurrentRun: runID, Task: taskID, Limit: maxTaskDecisions})
		if err != nil {
			return nil, err
		}
		rows = append(rows, got...)
	}
	slices.SortFunc(rows, func(x, y ledger.DecisionRow) int { return cmp.Compare(y.N, x.N) })
	rows = slices.CompactFunc(rows, func(x, y ledger.DecisionRow) bool { return x.N == y.N })
	var recs []ledger.DecisionRecord
	for _, row := range rows {
		rec, ok, err := dl.Decision(ctx, row.N)
		if err != nil {
			return nil, err
		}
		if ok {
			recs = append(recs, rec)
		}
		if len(recs) == maxTaskDecisions {
			break
		}
	}
	if len(recs) == 0 {
		return nil, nil
	}
	slices.Reverse(recs) // oldest first
	moved := map[int64]string{}
	var carried []int64
	for _, rec := range recs {
		if rec.State != ledger.DecisionApplied {
			continue
		}
		last, filed := deliveryOf(rec)
		if filed || last == ledgerEntered {
			continue
		}
		var body requestBody
		_ = json.Unmarshal([]byte(rec.Request), &body)
		if s := body.Subject; rec.Origin == ledger.OriginAgent && rec.Type == ledger.DecisionApproval && rec.TaskID == taskID && s != nil && (s.Kind == "commit" || s.Kind == "artifact") {
			if words := subjectMoved(ctx, *s, subjectAt{dir: r.cwd, commit: start}); words != "" {
				if s.Kind == "commit" {
					words = fmt.Sprintf("%s approved %s; this attempt starts at %s", rec.ID(), short(s.Commit), short(start))
				}
				moved[rec.N] = words
				continue
			}
		}
		carried = append(carried, rec.N)
	}
	if e.afterDecisionsRead != nil {
		e.afterDecisionsRead(taskID)
	}
	if len(moved) > 0 || len(carried) > 0 {
		stale := false
		// A change kept ahead (no undo): under no attempt's context, which
		// a cancel or a stop ends, so that only the ledger refuses it.
		err := e.transact(context.WithoutCancel(ctx), r, func() tx {
			if !a.closedAt.IsZero() || a.start != start {
				stale = true
				return tx{}
			}
			for _, rec := range recs {
				words, ok := moved[rec.N]
				if !ok {
					continue
				}
				if d := settledN(r, rec.N); d != nil {
					e.supersedeAgent(r, d, words)
					continue
				}
				d := decisionOfRecord(r, rec)
				e.closeDecision(r, d, ledger.DecisionSuperseded, fmt.Sprintf("%s was superseded: %s; ask again if you still need it", rec.ID(), words), "yardmaster", decisionOp{}, map[string]any{"moved": words}, "")
			}
			// Each applied answer marked carried once, whatever the
			// attempts that read it before the mark committed: the mark
			// is taken here, in the run's order of changes (#636, review
			// round 1).
			var ids []int64
			for _, n := range carried {
				if r.dec.filed[n] {
					continue
				}
				if r.dec.filed == nil {
					r.dec.filed = map[int64]bool{}
				}
				r.dec.filed[n] = true
				ids = append(ids, n)
				if d := settledN(r, n); d != nil {
					d.taskFiled = true
				}
			}
			an := a.n
			return tx{more: func(ltx LedgerTx) error {
				dtx, err := decisionsOf(ltx)
				if err != nil {
					return err
				}
				for _, n := range ids {
					if _, _, err := dtx.History(n, ledger.HistoryRow{Kind: ledger.HistoryDeliveryTaskFile, By: "yardmaster", Detail: historyDetail(map[string]any{"task": taskID, "attempt": an})}); err != nil {
						return err
					}
				}
				return nil
			}}
		}, nil)
		if err != nil {
			e.afterTransact(r, err)
			return nil, err
		}
		if !stale {
			for i, rec := range recs {
				if words, ok := moved[rec.N]; ok {
					recs[i].State = ledger.DecisionSuperseded
					recs[i].Why = fmt.Sprintf("%s was superseded: %s; ask again if you still need it", rec.ID(), words)
				}
			}
		}
	}
	out := make([]TaskDecision, 0, len(recs))
	for _, rec := range recs {
		out = append(out, taskDecisionOf(rec))
	}
	return out, nil
}

// decisionsStage is the stage an attempt fails at, uncounted, when its
// task's decisions cannot be read for its task files: the only way an
// answer applied for its task file reaches it, so it is never started
// without them, and the run pauses until a person looks.
const decisionsStage = "decisions"

// noteTaskDecisions reads a's task file decisions for dispatch: a read
// that fails is its error, and dispatch fails the attempt at
// decisionsStage (#636, review round 1).
func (e *Engine) noteTaskDecisions(ctx context.Context, r *run, a *attempt) error {
	ds, err := e.taskDecisions(ctx, r, a)
	if err != nil {
		return fmt.Errorf("its task's decisions could not be read: %w", err)
	}
	e.mu.Lock()
	a.decisions = ds
	e.mu.Unlock()
	return nil
}

// writeDecisions writes task.md's Decisions section: each decision with
// its state and, once applied, its answer; nothing for none.
func writeDecisions(b *strings.Builder, ds []TaskDecision) {
	if len(ds) == 0 {
		return
	}
	b.WriteString("## Decisions\n\nThe decisions of this task, asked by its attempts or holding it:\n\n")
	for _, d := range ds {
		fmt.Fprintf(b, "- %s (%s", d.ID, d.Type)
		if d.Gate != "" {
			fmt.Fprintf(b, ", the plan's %s", d.Gate)
		}
		fmt.Fprintf(b, "), %s: %s", d.State, flatLine(d.Question))
		switch d.State {
		case ledger.DecisionApplied:
			fmt.Fprintf(b, " %s answered: %s.", d.By, strings.TrimSuffix(flatLine(d.Answer), "."))
			if d.Rationale != "" {
				fmt.Fprintf(b, " Why: %s.", strings.TrimSuffix(flatLine(d.Rationale), "."))
			}
		case ledger.DecisionOpen, ledger.DecisionAnswered:
			b.WriteString(" Conductor holds this task until the operator's answer is applied.")
		default:
			if d.Words != "" {
				fmt.Fprintf(b, " %s.", strings.TrimSuffix(flatLine(d.Words), "."))
			}
			if d.State == ledger.DecisionSuperseded && d.Answer != "" {
				b.WriteString(" Its answer is not current: never act on it.")
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}
