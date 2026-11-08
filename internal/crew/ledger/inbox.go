package ledger

import (
	"cmp"
	"context"
	"strings"
	"time"
)

// The decision inbox's reads of the decisions (docs/plans/decision-inbox.md,
// PR-3): the inbox merges its sources by (since, ref), a page and one of
// each after its cursor, with a complete count, and a decision's since is
// its created_at, its ref "decision:" and its id. Decisions pages newest
// first by n with no count, which that merge cannot use, so these are
// queries of their own over the same table: read-only, no schema change.
//
// A time is kept as RFC 3339 with its nanoseconds and no trailing zeros
// (stamp), whose text does not sort as the times do ("…:00Z" after
// "…:00.5Z"), so these reads order by orderKey, the stamp with its
// fraction written out to nine digits, which does.

// DecisionRefPrefix is what a decision's ref begins with: "decision:" and
// its id is the decision inbox's name for it (Brakeman's form).
const DecisionRefPrefix = "decision:"

// DecisionRef is decision n's ref, "decision:dr-<n>".
func DecisionRef(n int64) string { return DecisionRefPrefix + DecisionID(n) }

// MaxInboxDecisions bounds a page of InboxDecisions and of ClosedDecisions.
const MaxInboxDecisions = 500

// InboxCursor is where a page of InboxDecisions begins: after the item
// asked at Since whose ref is Ref, in the order (since, ref), the ref of
// any kind of the inbox's (a question's, a plan revision's) compared as
// text with a decision's. The zero cursor begins at the first.
type InboxCursor struct {
	Since time.Time
	Ref   string
}

// orderKey is t as orderSQL writes a stamp: sortable as text.
func orderKey(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000") }

// orderSQL is the column col's stamp as orderKey writes it: its first 19
// characters, then a point and its fraction padded to nine digits (none
// for a stamp of whole seconds).
func orderSQL(col string) string {
	return "(substr(" + col + ", 1, 19) || '.' || substr(CASE WHEN substr(" + col + ", 20, 1) = '.' THEN substr(" + col + ", 21, length(" + col + ") - 21) ELSE '' END || '000000000', 1, 9))"
}

// decisionRefSQL is a decision row's ref, as DecisionRef writes it.
const decisionRefSQL = "('" + DecisionRefPrefix + "dr-' || d.n)"

// InboxDecisions reads the decisions in the given states, oldest first by
// (created_at, ref), at most limit of them after the cursor (limit at most
// MaxInboxDecisions, 0 for that); next says more follow the page; total
// counts every decision in those states, not the page's. The page and the
// count are of one read. A database older than the decision tables has
// none.
func (l *Ledger) InboxDecisions(ctx context.Context, states []string, after InboxCursor, limit int) (page []DecisionRow, next bool, total int, err error) {
	if limit <= 0 || limit > MaxInboxDecisions {
		limit = MaxInboxDecisions
	}
	if len(states) == 0 {
		return []DecisionRow{}, false, 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(states)), ", ")
	var args []any
	for _, s := range states {
		args = append(args, s)
	}
	where := "d.state IN (" + marks + ")"
	pageArgs := append([]any{}, args...)
	if !after.Since.IsZero() {
		key := orderKey(after.Since)
		where += " AND (" + orderSQL("d.created_at") + " > ? OR (" + orderSQL("d.created_at") + " = ? AND " + decisionRefSQL + " > ?))"
		pageArgs = append(pageArgs, key, key, after.Ref)
	}
	query := "SELECT " + decisionCols + " FROM decisions d WHERE " + where + " ORDER BY " + orderSQL("d.created_at") + ", " + decisionRefSQL + " LIMIT ?"
	pageArgs = append(pageArgs, limit+1)
	page = []DecisionRow{}
	err = l.readSnapshot(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM decisions d WHERE d.state IN ("+marks+")", args...).Scan(&total); err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, query, pageArgs...)
		if err != nil {
			return err
		}
		for rows.Next() {
			d, err := scanDecision(rows)
			if err != nil {
				rows.Close()
				return err
			}
			page = append(page, d)
		}
		return cmp.Or(rows.Err(), rows.Close())
	})
	if err != nil {
		return nil, false, 0, err
	}
	if len(page) > limit {
		return page[:limit], true, total, nil
	}
	return page, false, total, nil
}

// ClosedDecisions reads the decisions closed (applied, superseded,
// cancelled or expired) at since or later, the latest closed first, at
// most limit (at most MaxInboxDecisions, 0 for that): the inbox's Recent.
func (l *Ledger) ClosedDecisions(ctx context.Context, since time.Time, limit int) ([]DecisionRow, error) {
	if limit <= 0 || limit > MaxInboxDecisions {
		limit = MaxInboxDecisions
	}
	out := []DecisionRow{}
	err := l.readAt(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		rows, err := q.QueryContext(ctx, "SELECT "+decisionCols+" FROM decisions d WHERE d.state IN (?, ?, ?, ?) AND d.closed_at != '' AND "+orderSQL("d.closed_at")+" >= ? ORDER BY "+orderSQL("d.closed_at")+" DESC, d.n DESC LIMIT ?",
			DecisionApplied, DecisionSuperseded, DecisionCancelled, DecisionExpired, orderKey(since), limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			d, err := scanDecision(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, d)
		}
		return cmp.Or(rows.Err(), rows.Close())
	})
	return out, err
}
