package dispatch

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// The copied-output check's verdicts of a change row (docs/plans/sync-seams.md,
// OS-1c): changes.output of the row's value and changes.prior_output of
// its prior, written with the row. A row written before the check existed
// keeps none ("").
const (
	VerdictClean   = "clean"
	VerdictOutput  = "output"
	VerdictPending = "pending"
)

// OutputCheck is the copied-output check a write judges a value by: the
// value's text and whether an agent wrote it, its verdict (clean, output
// or pending). It runs inside the writing transaction.
type OutputCheck func(ctx context.Context, text string, agent bool) string

// SetOutputCheck gives the store the check every write judges its rows
// by from then on; nil for none.
func (s *Store) SetOutputCheck(f OutputCheck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outputCheck = f
}

// AgentActor reports whether a row's actor is not a person's: the
// workbench's operator (ActorAdmin) and the store's own (ActorServer,
// ActorMerge) are not, and every other actor (an agent's, Brakeman's, one
// this build does not know) is taken as an agent's.
func AgentActor(actor string) bool {
	switch {
	case actor == ActorAdmin, actor == ActorServer, actor == ActorMerge:
		return false
	case strings.HasPrefix(actor, "@"):
		_, raw, _ := strings.Cut(actor[1:], "/")
		return AgentActor(raw)
	}
	return true
}

// WithholdPending makes every pending verdict output, in one transaction:
// at a start, before the check answers anything, since the output a
// pending row may have quoted (a session's ring and screen) lived in the
// memory of the process that ended. It answers how many rows it changed.
func (s *Store) WithholdPending(ctx context.Context) (int64, error) {
	var n int64
	err := s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		for _, col := range []string{"output", "prior_output"} {
			res, err := dbtx.ExecContext(ctx, "UPDATE changes SET "+col+" = ? WHERE "+col+" = ?", VerdictOutput, VerdictPending)
			if err != nil {
				return err
			}
			m, _ := res.RowsAffected()
			n += m
		}
		return nil
	})
	return n, err
}

// PendingRow is a row a verdict of which is pending: its position, its
// value and prior, when and by whom it was written, its verdicts, and
// whether this process wrote it (Mine): the only rows a process settles by
// its check, since the output a row may have quoted lived in the memory of
// the process that wrote it. Another process's pending row, one running
// beside this one over the same folder or one that ended, is output.
type PendingRow struct {
	Seq          int64
	Value, Prior string
	At           time.Time
	Actor        string
	Output       string
	PriorOutput  string
	Mine         bool
}

// MaxPendingPage bounds PendingRows.
const MaxPendingPage = 500

// PendingRows is at most limit rows with a pending verdict, oldest first.
func (s *Store) PendingRows(ctx context.Context, limit int) ([]PendingRow, error) {
	limit = min(max(limit, 1), MaxPendingPage)
	var out []PendingRow
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < syncSchema {
			return nil
		}
		rows, err := q.QueryContext(ctx, `SELECT seq, value, prior, at, actor, output, prior_output FROM changes
			WHERE output = ? OR prior_output = ? ORDER BY seq LIMIT ?`, VerdictPending, VerdictPending, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PendingRow
			var at string
			if err := rows.Scan(&r.Seq, &r.Value, &r.Prior, &at, &r.Actor, &r.Output, &r.PriorOutput); err != nil {
				return err
			}
			r.At = unstamp(at)
			out = append(out, r)
		}
		return rows.Err()
	})
	s.mu.Lock()
	for i := range out {
		out[i].Mine = s.pendingMine[out[i].Seq]
	}
	s.mu.Unlock()
	return out, err
}

// SettleVerdicts writes a pending row's verdicts as judged now: each
// verdict that is pending takes the one given; one already settled stays.
func (s *Store) SettleVerdicts(ctx context.Context, seq int64, output, priorOutput string) error {
	err := s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		if _, err := dbtx.ExecContext(ctx, `UPDATE changes SET output = CASE WHEN output = ? THEN ? ELSE output END,
			prior_output = CASE WHEN prior_output = ? THEN ? ELSE prior_output END WHERE seq = ?`,
			VerdictPending, output, VerdictPending, priorOutput, seq); err != nil {
			return err
		}
		return nil
	})
	if err == nil && output != VerdictPending && priorOutput != VerdictPending {
		s.mu.Lock()
		delete(s.pendingMine, seq)
		s.mu.Unlock()
	}
	return err
}
