package ledger

import "context"

// MaxOpenRuns bounds what OpenRuns gives, whatever n it is asked for.
const MaxOpenRuns = 500

// OpenRun is an open run as its row was committed, with what a page that
// lists runs and who holds them shows of it, and nothing more: its id, its
// crew's id and name (the crew as launched; "" when that cannot be read),
// its state and whether it is paused. A run's attention is in the memory
// of the process that holds it, never in its row.
type OpenRun struct {
	ID       string
	CrewID   string
	CrewName string
	State    string
	Paused   bool
}

// openRunWhere is a run whose row is open: running, or interrupted (a
// recovery interrupted it) and not continued, its continuation not made.
const openRunWhere = `(state = '` + RunRunning + `' OR (state = '` + RunInterrupted + `' AND continued_by = ''))`

// OpenRuns reads the open runs (openRunWhere), newest first: at most n,
// and never more than MaxOpenRuns, with the count of the open runs left
// out. The rows and the count are of one commit. It reads the database
// the ledger opened, writable or read-only, and gives ErrUnavailable when
// there is none. What it gives is the rows as they were committed when it
// read them: a run can close, or be continued, the moment after.
func (l *Ledger) OpenRuns(ctx context.Context, n int) ([]OpenRun, int, error) {
	n = max(0, min(n, MaxOpenRuns))
	var (
		out   []OpenRun
		total int
	)
	err := l.readSnapshot(ctx, func(q querier, _ int) error {
		out, total = nil, 0
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE `+openRunWhere).Scan(&total); err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT id, crew_id,
			CASE WHEN json_valid(crew) AND json_type(crew, '$.name') = 'text' THEN json_extract(crew, '$.name') ELSE '' END,
			state, paused
			FROM runs WHERE `+openRunWhere+` ORDER BY created_at DESC, id DESC LIMIT ?`, n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r OpenRun
			var paused int
			if err := rows.Scan(&r.ID, &r.CrewID, &r.CrewName, &r.State, &paused); err != nil {
				return err
			}
			r.Paused = paused != 0
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total - len(out), nil
}
