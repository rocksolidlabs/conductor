package ledger

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// versionDecisionSync is the version that added decision_statements and
// decision_output: a database before it keeps neither.
const versionDecisionSync = 16

// The tables version 16 adds for sync's seams (docs/plans/sync-seams.md,
// row OS-3a, the step before OS-3), and what OS-3 writes and reads of them.

// decisionStatementsTable keeps the statement a held answer carries
// (version 16): one row for each response of a decision whose authority
// names a statement, keyed by the decision record's n and the response's
// k, as decision_responses is, the statement whole as text. The authority
// holds only its SHA-256, so the statement need not fit DR1's bound of an
// authority. A row is written in the transaction of the response it
// belongs to and is deleted with its decision (ON DELETE CASCADE), as a
// pruned run's decisions are: a decision handed on to another run keeps
// its number, and so its rows.
const decisionStatementsTable = `CREATE TABLE decision_statements (
		decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
		k INTEGER NOT NULL,
		statement TEXT NOT NULL,
		PRIMARY KEY (decision, k)
	)`

// decisionOutputTable keeps a decision's copied-output verdicts (version
// 16): one row for each free-text part of a decision (its request's
// fields, each response's text and rationale, each receipt's and the
// application's words), keyed by the decision record's n and the part's
// name, with its verdict and where a part found to quote output is cut (-1
// for none), written in the transaction that writes the part, so a
// document built later, after the output it quoted is gone, still cuts
// it: a row outlives the output it was judged against, and is deleted
// with its decision (ON DELETE CASCADE), whose document is never built
// again; a decision handed on to another run keeps its number, and so its
// rows.
const decisionOutputTable = `CREATE TABLE decision_output (
		decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
		part TEXT NOT NULL,
		verdict TEXT NOT NULL,
		cut_at INTEGER NOT NULL DEFAULT -1,
		PRIMARY KEY (decision, part)
	)`

// OutputCheck is the copied-output check a decision's free-text parts are
// judged by as they are written (docs/plans/sync-seams.md, OS-3): the
// text, whether an agent wrote it, and its verdict, VerdictClean,
// VerdictOutput or VerdictPending.
type OutputCheck func(ctx context.Context, text string, agent bool) string

// The verdicts decision_output keeps.
const (
	VerdictClean   = "clean"
	VerdictOutput  = "output"
	VerdictPending = "pending"
)

// SetOutputCheck gives the ledger the check every transaction judges the
// decision parts it writes by from then on; nil for none (no row written).
func (l *Ledger) SetOutputCheck(f OutputCheck) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outputCheck = f
}

// OnDecisions gives the ledger fn, called with the numbers of the
// decisions a committed transaction changed (its decision opened, put,
// routed, answered, moved, responded to, given a history row or a policy)
// and those a prune deleted or handed back, under the lock, after
// OnCommit's and OnPrune's calls: fn must only queue.
func (l *Ledger) OnDecisions(fn func(ns []int64)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onDecisions = fn
}

// decisionsChanged tells OnDecisions's fn of ns, each once. The caller
// holds mu.
func (l *Ledger) decisionsChanged(ns []int64) {
	if l.onDecisions == nil || len(ns) == 0 {
		return
	}
	out := slices.Clone(ns)
	slices.Sort(out)
	l.onDecisions(slices.Compact(out))
}

// changed notes that the transaction changed decision n.
func (t *Tx) changed(n int64) {
	if n > 0 {
		t.decisions = append(t.decisions, n)
	}
}

// prunedDecisions is the decisions a prune of runID deletes or hands back.
func prunedDecisions(ctx context.Context, tx *sql.Tx, runID string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT n FROM decisions WHERE run_id = ? OR current_run = ?", runID, runID)
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	return out, cmp.Or(rows.Err(), rows.Close())
}

// The parts of a decision decision_output keeps a verdict of.
const (
	PartQuestion    = "question"
	PartContext     = "context"
	PartUncertainty = "uncertainty"
	PartAuthorizes  = "authorizes"
	PartExpertise   = "expertise"
	PartWhy         = "why"
	PartApplied     = "applied/words"
)

// PartLabel and PartTradeoffs name an option's parts; PartResponse a
// response's (text, rationale, words).
func PartLabel(i int) string                 { return fmt.Sprintf("option/%d/label", i) }
func PartTradeoffs(i int) string             { return fmt.Sprintf("option/%d/tradeoffs", i) }
func PartResponse(k int, what string) string { return fmt.Sprintf("response/%d/%s", k, what) }

// judge writes the verdict of each part (name to text) of decision n: a
// part with no text has none. Nothing without a check.
func (t *Tx) judge(n int64, parts map[string]string, agent bool) error {
	if t.check == nil {
		return nil
	}
	for name, text := range parts {
		if text == "" {
			if _, err := t.tx.ExecContext(t.ctx, "DELETE FROM decision_output WHERE decision = ? AND part = ?", n, name); err != nil {
				return err
			}
			continue
		}
		v := t.check(t.ctx, text, agent)
		cut := -1
		if v == VerdictOutput {
			cut = 0
		}
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO decision_output (decision, part, verdict, cut_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (decision, part) DO UPDATE SET verdict = excluded.verdict, cut_at = excluded.cut_at`, n, name, v, cut); err != nil {
			return err
		}
	}
	return nil
}

// judgeRequest judges a decision's request as it opens: an agent's
// request is an agent's words, a plan's and a comparison's the server's.
func (t *Tx) judgeRequest(d DecisionRow) error {
	var r requestShape
	if json.Unmarshal([]byte(d.Request), &r) != nil {
		return nil
	}
	parts := map[string]string{PartQuestion: r.Question, PartContext: r.Context, PartUncertainty: r.Uncertainty, PartAuthorizes: r.Authorizes,
		PartExpertise: strings.Join(r.Expertise, "\n")}
	for i, o := range r.Options {
		parts[PartLabel(i)], parts[PartTradeoffs(i)] = o.Label, o.Tradeoffs
	}
	return t.judge(d.N, parts, d.Origin == OriginAgent)
}

// judgeWords judges a decision's words as a put changes them: its why and
// its application's words, each when it changed.
func (t *Tx) judgeWords(n int64, cur, d DecisionRow) error {
	parts := map[string]string{}
	if d.Why != cur.Why {
		parts[PartWhy] = d.Why
	}
	if d.Applied != cur.Applied {
		var a struct {
			Words string `json:"words"`
		}
		_ = json.Unmarshal([]byte(d.Applied), &a)
		parts[PartApplied] = a.Words
	}
	return t.judge(n, parts, false)
}

// judgeResponse judges a response's text, rationale and words, as its
// receipt keeps them.
func (t *Tx) judgeResponse(n int64, r ResponseRow) error {
	var rc struct {
		Text      string `json:"text"`
		Rationale string `json:"rationale"`
		Words     string `json:"words"`
	}
	if json.Unmarshal([]byte(r.Receipt), &rc) != nil {
		return nil
	}
	return t.judge(n, map[string]string{PartResponse(r.K, "text"): rc.Text, PartResponse(r.K, "rationale"): rc.Rationale,
		PartResponse(r.K, "words"): rc.Words}, false)
}

// MaxStatement bounds a held answer's statement, in bytes.
const MaxStatement = 2 << 10

// keepStatement keeps a held answer's statement beside its response, in
// the response's transaction.
func (t *Tx) keepStatement(n int64, r ResponseRow) error {
	if r.Statement == "" {
		return nil
	}
	if len(r.Statement) > MaxStatement {
		return tooLarge("a held answer's statement", len(r.Statement))
	}
	_, err := t.tx.ExecContext(t.ctx, "INSERT INTO decision_statements (decision, k, statement) VALUES (?, ?, ?)", n, r.K, r.Statement)
	return err
}

// Statement reads the statement kept beside decision n's response k; ok
// false for none.
func (l *Ledger) Statement(ctx context.Context, n int64, k int) (statement string, ok bool, err error) {
	err = l.readAt(ctx, func(q querier, version int) error {
		if version < versionDecisionSync {
			return nil
		}
		e := q.QueryRowContext(ctx, "SELECT statement FROM decision_statements WHERE decision = ? AND k = ?", n, k).Scan(&statement)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		ok = e == nil
		return e
	})
	return statement, ok, err
}

// OutputVerdicts reads decision n's verdicts by part, and whether the
// ledger keeps verdicts at all (a database of an earlier version keeps
// none).
func (l *Ledger) OutputVerdicts(ctx context.Context, n int64) (map[string]string, bool, error) {
	out := map[string]string{}
	kept := false
	err := l.readAt(ctx, func(q querier, version int) error {
		if version < versionDecisionSync {
			return nil
		}
		kept = true
		rows, err := q.QueryContext(ctx, "SELECT part, verdict FROM decision_output WHERE decision = ?", n)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p, v string
			if err := rows.Scan(&p, &v); err != nil {
				rows.Close()
				return err
			}
			out[p] = v
		}
		return cmp.Or(rows.Err(), rows.Close())
	})
	return out, kept, err
}

// outputTx runs fn in a transaction of the ledger's own, outside every
// run's transition, and tells OnDecisions of the decisions fn says it
// changed.
func (l *Ledger) outputTx(ctx context.Context, fn func(tx *sql.Tx) ([]int64, error)) error {
	if l.readOnly {
		return fmt.Errorf("%w: %s", ErrReadOnly, l.why)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed, err := fn(tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	l.decisionsChanged(changed)
	return nil
}

// WithholdPendingOutput makes every pending verdict output, in one
// transaction: at a start, before the check answers anything, since the
// output a pending part may have quoted lived in the process that ended.
func (l *Ledger) WithholdPendingOutput(ctx context.Context) error {
	return l.outputTx(ctx, func(tx *sql.Tx) ([]int64, error) {
		ns, err := pendingDecisions(ctx, tx)
		if err != nil || len(ns) == 0 {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE decision_output SET verdict = ?, cut_at = 0 WHERE verdict = ?", VerdictOutput, VerdictPending)
		return ns, err
	})
}

// pendingDecisions is the decisions with a pending verdict.
func pendingDecisions(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT decision FROM decision_output WHERE verdict = ?", VerdictPending)
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	return out, cmp.Or(rows.Err(), rows.Close())
}

// PendingOutput is a decision's part whose verdict is pending.
type PendingOutput struct {
	N    int64
	Part string
}

// PendingOutputs is at most limit parts whose verdict is pending.
func (l *Ledger) PendingOutputs(ctx context.Context, limit int) ([]PendingOutput, error) {
	var out []PendingOutput
	err := l.readAt(ctx, func(q querier, version int) error {
		if version < versionDecisionSync {
			return nil
		}
		rows, err := q.QueryContext(ctx, "SELECT decision, part FROM decision_output WHERE verdict = ? ORDER BY decision, part LIMIT ?", VerdictPending, max(limit, 1))
		if err != nil {
			return err
		}
		for rows.Next() {
			var p PendingOutput
			if err := rows.Scan(&p.N, &p.Part); err != nil {
				rows.Close()
				return err
			}
			out = append(out, p)
		}
		return cmp.Or(rows.Err(), rows.Close())
	})
	return out, err
}

// SettleOutput writes a pending part's verdict as judged now; one already
// settled stays.
func (l *Ledger) SettleOutput(ctx context.Context, n int64, part, verdict string) error {
	return l.outputTx(ctx, func(tx *sql.Tx) ([]int64, error) {
		cut := -1
		if verdict == VerdictOutput {
			cut = 0
		}
		res, err := tx.ExecContext(ctx, "UPDATE decision_output SET verdict = ?, cut_at = ? WHERE decision = ? AND part = ? AND verdict = ?", verdict, cut, n, part, VerdictPending)
		if err != nil {
			return nil, err
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return nil, nil
		}
		return []int64{n}, nil
	})
}

// LastDecision is the highest decision number the ledger holds, 0 for
// none: read, never written (Host.LastDecision).
func (l *Ledger) LastDecision(ctx context.Context) (int64, error) {
	var n int64
	err := l.readAt(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		return q.QueryRowContext(ctx, "SELECT COALESCE(MAX(n), 0) FROM decisions").Scan(&n)
	})
	return n, err
}

// statementFits says a response's statement goes with its authority: a
// held answer's (via sync) is the statement whose SHA-256 its authority
// names, at most MaxStatement bytes; every other response has none.
func statementFits(a authorityShape, statement string) error {
	if a.Via != ViaSync {
		if statement != "" {
			return invalid("only a held answer keeps a statement")
		}
		return nil
	}
	if len(statement) > MaxStatement {
		return tooLarge("a held answer's statement", len(statement))
	}
	sum := sha256.Sum256([]byte(statement))
	if statement == "" || hex.EncodeToString(sum[:]) != a.StatementSHA256 {
		return invalid("a held answer's statement is not the one its authority names")
	}
	return nil
}
