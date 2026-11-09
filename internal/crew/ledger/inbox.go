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
// MaxInboxDecisions, 0 for that), each with what it holds and its counts;
// next says more follow the page; total counts every decision in those
// states, not the page's, and answered those of them answered (the
// inbox's tracking items). The page, what each holds and the counts are
// of one read. A database older than the decision tables has none.
func (l *Ledger) InboxDecisions(ctx context.Context, states []string, after InboxCursor, limit int) (page []InboxDecision, next bool, total, answered int, err error) {
	if limit <= 0 || limit > MaxInboxDecisions {
		limit = MaxInboxDecisions
	}
	if len(states) == 0 {
		return []InboxDecision{}, false, 0, 0, nil
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
	query := "SELECT " + inboxCols + " FROM decisions d WHERE " + where + " ORDER BY " + orderSQL("d.created_at") + ", " + decisionRefSQL + " LIMIT ?"
	pageArgs = append(pageArgs, limit+1)
	page = []InboxDecision{}
	err = l.readSnapshot(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(d.state = ?), 0) FROM decisions d WHERE d.state IN ("+marks+")", append([]any{DecisionAnswered}, args...)...).Scan(&total, &answered); err != nil {
			return err
		}
		page, err = readInbox(ctx, q, query, pageArgs...)
		return err
	})
	if err != nil {
		return nil, false, 0, 0, err
	}
	if len(page) > limit {
		return page[:limit], true, total, answered, nil
	}
	return page, false, total, answered, nil
}

// InboxDecision is a decision's row with what the inbox says of it beside
// the row, all of one read: the tasks it holds now (its active holds under
// its current run, by task), and how many responses and history rows it
// keeps, which mark the record whole beside its rev (a refused response
// and a delivery's rows do not move the rev).
type InboxDecision struct {
	DecisionRow
	Held               []string
	Responses, History int
}

// inboxCols are a decision's columns, then its two counts.
const inboxCols = decisionCols + ", (SELECT COUNT(*) FROM decision_responses r WHERE r.decision = d.n), (SELECT COUNT(*) FROM decision_history h WHERE h.decision = d.n)"

// readInbox runs query, a select of inboxCols, through q, then reads what
// each decision found holds through the same q: called within one read
// transaction, the rows and their holds are of one commit.
func readInbox(ctx context.Context, q querier, query string, args ...any) ([]InboxDecision, error) {
	out := []InboxDecision{}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id InboxDecision
		d, err := scanDecision(countsScanner{rows, &id.Responses, &id.History})
		if err != nil {
			rows.Close()
			return nil, err
		}
		id.DecisionRow = d
		out = append(out, id)
	}
	if err := cmp.Or(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	ns := make([]int64, 0, len(out))
	for _, d := range out {
		ns = append(ns, d.N)
	}
	held, err := readHeld(ctx, q, ns)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Held = held[out[i].N]
	}
	return out, nil
}

// readHeld reads the tasks each of the decisions ns holds now, through q:
// its active holds under its current run (the run that holds it), by
// task. A decision that holds nothing has no entry.
func readHeld(ctx context.Context, q querier, ns []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(ns) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ns))
	for _, n := range ns {
		args = append(args, n)
	}
	rows, err := q.QueryContext(ctx, "SELECT h.decision, h.task_id FROM decision_holds h JOIN decisions d ON d.n = h.decision WHERE h.run_id = d.current_run AND h.released_at = '' AND h.decision IN ("+
		strings.TrimSuffix(strings.Repeat("?, ", len(ns)), ", ")+") ORDER BY h.decision, h.task_id", args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n int64
		var task string
		if err := rows.Scan(&n, &task); err != nil {
			rows.Close()
			return nil, err
		}
		out[n] = append(out[n], task)
	}
	return out, cmp.Or(rows.Err(), rows.Close())
}

// ScopeDecisions reads every decision run holds, whatever its state (with
// task, those of task: asked by it or holding it, as DecisionFilter's
// Task reads), oldest first, each with what it holds and its counts, every
// row of one read transaction, so a change committed meanwhile is seen
// whole or not at all: a decision link's scope, its open items and the
// final records a link still reads. At most MaxDecisionsPerRun and a page
// are read, past what a run may hold. A database older than the decision
// tables has none.
func (l *Ledger) ScopeDecisions(ctx context.Context, run, task string) ([]InboxDecision, error) {
	out := []InboxDecision{}
	if run == "" {
		return out, nil
	}
	args := []any{run}
	where := "d.current_run = ?"
	if task != "" {
		where += " AND (d.task_id = ? OR EXISTS (SELECT 1 FROM decision_holds h WHERE h.decision = d.n AND h.task_id = ?))"
		args = append(args, task, task)
	}
	query := "SELECT " + inboxCols + " FROM decisions d WHERE " + where + " ORDER BY d.n LIMIT ?"
	args = append(args, MaxDecisionsPerRun+MaxDecisionsPage)
	err := l.readSnapshot(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		var err error
		out, err = readInbox(ctx, q, query, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// countsScanner scans a decision's columns, then its two counts.
type countsScanner struct {
	sc                 scanner
	responses, history *int
}

func (c countsScanner) Scan(dest ...any) error {
	return c.sc.Scan(append(dest, c.responses, c.history)...)
}

// ClosedDecisions reads the decisions closed (applied, superseded,
// cancelled or expired) at since or later, the latest closed first, at
// most limit (at most MaxInboxDecisions, 0 for that), each with what it
// holds and its counts, of one read: the inbox's Recent.
func (l *Ledger) ClosedDecisions(ctx context.Context, since time.Time, limit int) ([]InboxDecision, error) {
	if limit <= 0 || limit > MaxInboxDecisions {
		limit = MaxInboxDecisions
	}
	out := []InboxDecision{}
	err := l.readSnapshot(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		var err error
		out, err = readInbox(ctx, q, "SELECT "+inboxCols+" FROM decisions d WHERE d.state IN (?, ?, ?, ?) AND d.closed_at != '' AND "+orderSQL("d.closed_at")+" >= ? ORDER BY "+orderSQL("d.closed_at")+" DESC, d.n DESC LIMIT ?",
			DecisionApplied, DecisionSuperseded, DecisionCancelled, DecisionExpired, orderKey(since), limit)
		return err
	})
	return out, err
}
