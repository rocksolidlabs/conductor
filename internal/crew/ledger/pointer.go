package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The sync seam's hooks (docs/plans/ext.md, C6): an observer told, in
// commit order, of every run a committed transition changed and of every
// run a committed prune removed, and the readers it reads a run's pointer
// with when it delivers it. Nothing here changes the schema.

// RunPointer is what a run's row says of where the run stands: its ids,
// its state and outcome, whether it is paused, its revision, its links to
// the runs it continues and that continue it, and when it was made and
// closed. Never the crew as launched, the task, attempt or check rows,
// nor the logs.
type RunPointer struct {
	ID            string
	CrewID        string
	State         string
	Outcome       string
	Paused        bool
	Revision      int64
	ContinuedFrom string
	ContinuedBy   string
	CreatedAt     time.Time
	ClosedAt      time.Time // zero while the run is open
}

// pointerCols are the runs columns a RunPointer is read from, in its scan
// order.
const pointerCols = `id, crew_id, state, outcome, paused, revision, continued_from, continued_by, created_at, closed_at`

// scanPointer reads a RunPointer from sc, a row of pointerCols.
func scanPointer(sc scanner) (RunPointer, error) {
	var p RunPointer
	var paused int
	var created, closed string
	if err := sc.Scan(&p.ID, &p.CrewID, &p.State, &p.Outcome, &paused, &p.Revision, &p.ContinuedFrom, &p.ContinuedBy, &created, &closed); err != nil {
		return RunPointer{}, err
	}
	p.Paused, p.CreatedAt, p.ClosedAt = paused != 0, unstamp(created), unstamp(closed)
	return p, nil
}

// OnCommit gives the ledger fn, called once per run a transition changed,
// after the transition has committed and under the lock that orders the
// transitions, so the calls come in commit order and never for a
// transition that failed: the transition's own run, with its new
// revision, then the run its Continued marked, when there is one (its
// continued_by set, its revision bumped too). Recover's transitions are
// transitions too. Each pointer is read in the transaction. fn must only
// queue what it is told: it holds every writer of the ledger while it
// runs, and it must not call the ledger. Set it before the first
// transition it is to be told of; a later call replaces it.
func (l *Ledger) OnCommit(fn func(RunPointer)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onCommit = fn
}

// OnPrune gives the ledger fn, called with a run's id after a prune that
// removed its rows has committed, under the lock as OnCommit's fn is,
// never for a prune that failed or found no row. fn must only queue.
func (l *Ledger) OnPrune(fn func(runID string)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onPrune = fn
}

// pointers reads, inside the transaction, the pointer of its run and of
// the run Continued marked, in that order.
func (t *Tx) pointers() ([]RunPointer, error) {
	ids := []string{t.runID}
	if t.marked != "" && t.marked != t.runID {
		ids = append(ids, t.marked)
	}
	out := make([]RunPointer, 0, len(ids))
	for _, id := range ids {
		p, err := scanPointer(t.tx.QueryRowContext(t.ctx, `SELECT `+pointerCols+` FROM runs WHERE id = ?`, id))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// committed tells the observer of the pointers a committed transition
// changed. The caller holds mu.
func (l *Ledger) committed(ps []RunPointer) {
	if l.afterCommit != nil {
		l.afterCommit()
	}
	if l.onCommit == nil {
		return
	}
	for _, p := range ps {
		l.onCommit(p)
	}
}

// pruned tells the observer of a run a committed prune removed. The caller
// holds mu.
func (l *Ledger) pruned(runID string) {
	if l.afterCommit != nil {
		l.afterCommit()
	}
	if l.onPrune != nil {
		l.onPrune(runID)
	}
}

// NoDatabase reports that the ledger has nothing to read because no
// database file is where it keeps one: it holds no run. A read's
// ErrUnavailable says less, only that there is nothing to read: a file
// that is there but will not open, or a lock file that failed, is not
// an empty ledger.
func (l *Ledger) NoDatabase() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil {
		return false
	}
	_, err := os.Lstat(filepath.Join(l.dir, File))
	return errors.Is(err, fs.ErrNotExist)
}

// MaxPointers bounds a page of Pointers.
const MaxPointers = 1000

// Pointer reads a run's pointer; false, with no error, when the ledger
// has no row of it (never made, or pruned).
func (l *Ledger) Pointer(ctx context.Context, id string) (RunPointer, bool, error) {
	db, err := l.conn()
	if err != nil {
		return RunPointer{}, false, err
	}
	p, err := scanPointer(db.QueryRowContext(ctx, `SELECT `+pointerCols+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RunPointer{}, false, nil
	}
	if err != nil {
		return RunPointer{}, false, err
	}
	return p, true, nil
}

// Pointers reads a page of the runs' pointers in id order: at most n
// (1 to MaxPointers) of the runs whose id sorts after after ("" for the
// first page). A page shorter than n is the last.
func (l *Ledger) Pointers(ctx context.Context, after string, n int) ([]RunPointer, error) {
	if n < 1 || n > MaxPointers {
		return nil, fmt.Errorf("ledger: a page of %d pointers, not 1 to %d", n, MaxPointers)
	}
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+pointerCols+` FROM runs WHERE id > ? ORDER BY id LIMIT ?`, after, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunPointer
	for rows.Next() {
		p, err := scanPointer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
