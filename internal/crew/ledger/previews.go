package ledger

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"
)

// A candidate's preview checkout (docs/plans/browser-evidence.md, PV1): a
// checkout of an attempt's result that a person opened to look at, made
// usable through the run's project manifest as a tree is, opened many
// times, each a row of its own by seq. Its states: resolved (its manifest
// read and its ports leased, before the checkout is added or a step runs),
// ready (its preview offered) and ended (with why, and interrupted when
// the recovery ended it). Its steps are receipts of the attempt (checks).
const (
	PreviewResolved = "resolved"
	PreviewReady    = "ready"
	PreviewEnded    = "ended"
)

// MaxPreviews is how many previews a candidate is opened in the ledger.
const MaxPreviews = 20

// Errors of the previews' writers.
var (
	// ErrPreviewOpen says the candidate has a preview not ended yet: one
	// at a time.
	ErrPreviewOpen = errors.New("ledger: the candidate's preview is open")
	// ErrTooManyPreviews says the candidate was opened MaxPreviews times.
	ErrTooManyPreviews = errors.New("ledger: too_many_previews")
	// ErrNoPreview says the preview has no row in the state the writer
	// moves it from.
	ErrNoPreview = errors.New("ledger: no such preview open")
)

// PreviewRow is a preview checkout of the attempt (TaskID, N) by Seq,
// from 1: its directory, the commit, the manifest and the ports of its
// tree, its URL once ready, its state, why it ended, who opened it and
// when, and where it ran (its run's Runtime's Where).
type PreviewRow struct {
	RunID       string
	TaskID      string
	N           int
	Seq         int
	Dir         string
	Commit      string
	Manifest    string
	Ports       map[string]int
	Preview     string
	State       string
	Why         string
	Interrupted bool
	OpenedAt    time.Time
	OpenedBy    string
	EndedAt     time.Time
	Where       string
}

// previewsTable is the previews table (version 11): no row of an older
// table is rewritten to make it.
const previewsTable = `CREATE TABLE previews (
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		seq INTEGER NOT NULL,
		dir TEXT NOT NULL DEFAULT '',
		commit_id TEXT NOT NULL DEFAULT '',
		manifest TEXT NOT NULL DEFAULT '',
		ports TEXT NOT NULL DEFAULT '',
		preview TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		why TEXT NOT NULL DEFAULT '',
		interrupted INTEGER NOT NULL DEFAULT 0,
		opened_at TEXT NOT NULL,
		opened_by TEXT NOT NULL DEFAULT '',
		ended_at TEXT NOT NULL DEFAULT '',
		where_ran TEXT NOT NULL DEFAULT 'native',
		PRIMARY KEY (run_id, task_id, n, seq),
		FOREIGN KEY (run_id, task_id, n) REFERENCES attempts(run_id, task_id, n) ON DELETE CASCADE
	)`

// OpenPreview puts a preview's first row, resolved: refused while another
// of the candidate is not ended (ErrPreviewOpen), past MaxPreviews of it
// (ErrTooManyPreviews), and for a seq that is not after every one the
// candidate had, so a seq only grows.
func (t *Tx) OpenPreview(r PreviewRow) error {
	if err := checkWhere(r.Where); err != nil {
		return err
	}
	if err := rowText("", r.Manifest, r.Dir, r.OpenedBy); err != nil {
		return err
	}
	ports, err := encodePorts(r.Ports)
	if err != nil {
		return err
	}
	var n, open, last int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*), COALESCE(SUM(state != ?), 0), COALESCE(MAX(seq), 0) FROM previews WHERE run_id = ? AND task_id = ? AND n = ?",
		PreviewEnded, t.runID, r.TaskID, r.N).Scan(&n, &open, &last); err != nil {
		return err
	}
	switch {
	case open > 0:
		return ErrPreviewOpen
	case n >= MaxPreviews:
		return ErrTooManyPreviews
	case r.Seq <= last:
		return fmt.Errorf("ledger: preview %d of %s/%d is not after %d", r.Seq, r.TaskID, r.N, last)
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO previews (run_id, task_id, n, seq, dir, commit_id, manifest, ports, state, opened_at, opened_by, where_ran)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.runID, r.TaskID, r.N, r.Seq, r.Dir, r.Commit, r.Manifest, ports, PreviewResolved, stamp(cmp.Or(r.OpenedAt, t.now)), r.OpenedBy, r.Where)
	return err
}

// PreviewReady records a resolved preview ready, with its URL.
func (t *Tx) PreviewReady(r PreviewRow) error {
	if err := rowText("", r.Preview); err != nil {
		return err
	}
	return t.movePreview(r, "UPDATE previews SET state = ?, preview = ? WHERE run_id = ? AND task_id = ? AND n = ? AND seq = ? AND state = ?",
		PreviewReady, r.Preview, t.runID, r.TaskID, r.N, r.Seq, PreviewResolved)
}

// PreviewEnding records the first half of a preview's end, before its
// services are signalled: its URL cleared and why it ends.
func (t *Tx) PreviewEnding(r PreviewRow) error {
	if err := rowText("", r.Why); err != nil {
		return err
	}
	return t.movePreview(r, "UPDATE previews SET preview = '', why = ? WHERE run_id = ? AND task_id = ? AND n = ? AND seq = ? AND state != ?",
		r.Why, t.runID, r.TaskID, r.N, r.Seq, PreviewEnded)
}

// EndPreview records a preview ended, its services stopped and its
// cleanup run: why, whether the shutdown's cut-off came first
// (Interrupted) and when.
func (t *Tx) EndPreview(r PreviewRow) error {
	if err := rowText("", r.Why); err != nil {
		return err
	}
	return t.movePreview(r, "UPDATE previews SET state = ?, preview = '', why = ?, interrupted = ?, ended_at = ? WHERE run_id = ? AND task_id = ? AND n = ? AND seq = ? AND state != ?",
		PreviewEnded, r.Why, b2i(r.Interrupted), stamp(cmp.Or(r.EndedAt, t.now)), t.runID, r.TaskID, r.N, r.Seq, PreviewEnded)
}

// movePreview runs one writer's update, ErrNoPreview when it moved no row.
func (t *Tx) movePreview(r PreviewRow, query string, args ...any) error {
	res, err := t.tx.ExecContext(t.ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s/%d preview %d", ErrNoPreview, r.TaskID, r.N, r.Seq)
	}
	return nil
}

// Previews reads a run's previews, by task, attempt and seq.
func (l *Ledger) Previews(ctx context.Context, runID string) ([]PreviewRow, error) {
	db, version, err := l.connAt()
	if err != nil {
		return nil, err
	}
	if version < 11 {
		return nil, nil // no table before version 11, and so no preview
	}
	rows, err := db.QueryContext(ctx, `SELECT task_id, n, seq, dir, commit_id, manifest, ports, preview, state, why, interrupted, opened_at, opened_by, ended_at, where_ran
		FROM previews WHERE run_id = ? ORDER BY task_id, n, seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PreviewRow
	for rows.Next() {
		r := PreviewRow{RunID: runID}
		var ports, opened, ended string
		var interrupted int
		if err := rows.Scan(&r.TaskID, &r.N, &r.Seq, &r.Dir, &r.Commit, &r.Manifest, &ports, &r.Preview, &r.State, &r.Why, &interrupted, &opened, &r.OpenedBy, &ended, &r.Where); err != nil {
			return nil, err
		}
		if r.Ports, err = decodePorts(ports); err != nil {
			return nil, err
		}
		r.Interrupted, r.OpenedAt, r.EndedAt = interrupted != 0, unstamp(opened), unstamp(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

// previewLeftOpen is why a preview the recovery ends was not ended by the
// owner.
const previewLeftOpen = "the owner stopped before the preview was ended"

// endPreviews ends the previews of the transaction's run not ended yet, as
// the recovery finds them (resolved or ready): ended and interrupted, with
// why, each with a `preview.ended` event of a fixed shape; nothing is run
// for them, and a service of theirs is reported with the others
// (openServices), never signalled. It returns how many.
func (t *Tx) endPreviews(now string) (int, error) {
	type key struct {
		task   string
		n, seq int
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT task_id, n, seq FROM previews WHERE run_id = ? AND state != ? ORDER BY task_id, n, seq", t.runID, PreviewEnded)
	if err != nil {
		return 0, err
	}
	var open []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.task, &k.n, &k.seq); err != nil {
			rows.Close()
			return 0, err
		}
		open = append(open, k)
	}
	if err := cmp.Or(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	if len(open) == 0 {
		return 0, nil
	}
	if _, err := t.tx.ExecContext(t.ctx, "UPDATE previews SET state = ?, preview = '', interrupted = 1, why = ?, ended_at = ? WHERE run_id = ? AND state != ?",
		PreviewEnded, previewLeftOpen, now, t.runID, PreviewEnded); err != nil {
		return 0, err
	}
	for _, k := range open {
		if err := t.Event("preview.ended", map[string]any{"task": k.task, "attempt": k.n, "seq": k.seq, "preview": "", "why": previewLeftOpen, "interrupted": true, "recovered": true}); err != nil {
			return 0, err
		}
	}
	return len(open), nil
}
