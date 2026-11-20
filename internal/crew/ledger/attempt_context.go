package ledger

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// attemptContextTable is the record of the context each attempt was given
// (version 15, docs/plans/context-seams.md, CT-3a): one row for each block
// a context provider gave an attempt, at its start (phase task) or by a
// lookup while it worked (phase lookup), never its text, which the data
// directory's context file keeps. seq numbers an attempt's blocks from 1
// across both phases; source is the provider's name; title, ref and pack
// are the block's title, what it is about and the provider's mark of the
// record it came from ("" for none); sha256 is the body's digest in
// lower-case hex and bytes its length; at is when it was recorded (RFC
// 3339, UTC). The key is the attempt and seq, so an attempt's rows are
// read in order by the key's prefix (its run and task for a task's
// attempts, the run for a run's candidates, the attempt for the next seq)
// and one block by the whole key, with no index of its own. A row goes
// with its attempt when its run is pruned, as a preview does (ON DELETE
// CASCADE). The bounds of a row's fields are its writer's, as ext's
// bounds give them; only the phase is held by a CHECK, since a row's
// meaning rests on it. No attempt before this version was given a
// recorded block. AddAttemptContext writes it and AttemptContext reads it
// (CT-3).
const attemptContextTable = `CREATE TABLE attempt_context (
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		seq INTEGER NOT NULL,
		phase TEXT NOT NULL CHECK (phase IN ('task', 'lookup')),
		source TEXT NOT NULL,
		title TEXT NOT NULL,
		ref TEXT NOT NULL DEFAULT '',
		pack TEXT NOT NULL DEFAULT '',
		sha256 TEXT NOT NULL,
		bytes INTEGER NOT NULL,
		at TEXT NOT NULL,
		PRIMARY KEY (run_id, task_id, n, seq),
		FOREIGN KEY (run_id, task_id, n) REFERENCES attempts(run_id, task_id, n) ON DELETE CASCADE
	)`

// The phases of a recorded block.
const (
	PhaseTask   = "task"
	PhaseLookup = "lookup"
)

// The bounds of an attempt's recorded context: its task rows (8 blocks of
// each of at most 16 context providers) and its lookup rows (at most 8 of
// one answer).
const (
	MaxContextTaskRows   = 128
	MaxContextLookupRows = 64
	MaxContextLookupOne  = 8
)

// AttemptContextRow is one block of context an attempt was given, as the
// ledger keeps it: its attempt, its seq, its phase, the provider that gave
// it, its title, what it is about, the provider's mark, its body's SHA-256
// and size, and when it was recorded; never its text.
type AttemptContextRow struct {
	RunID  string
	TaskID string
	N      int
	Seq    int
	Phase  string
	Source string
	Title  string
	Ref    string
	Pack   string
	SHA256 string
	Bytes  int
	At     time.Time
}

// ErrContextFull is a block past an attempt's bound of recorded context.
var ErrContextFull = errors.New("ledger: the attempt's recorded context is full")

// AddAttemptContext records blocks an attempt of the transaction's run
// was given, each after every seq the attempt has: refused for another
// run's attempt, a phase that is none, a seq not after the attempt's
// last, a digest that is not 64 lower-case hex, a size below 1, and past
// the attempt's bounds (ErrContextFull: MaxContextTaskRows task rows,
// MaxContextLookupRows lookup rows, MaxContextLookupOne of one call's).
func (t *Tx) AddAttemptContext(rows []AttemptContextRow) error {
	type key struct {
		task string
		n    int
	}
	counted := map[key]bool{}
	for _, r := range rows {
		switch {
		case r.RunID != t.runID:
			return fmt.Errorf("ledger: context of run %s put in %s's transition", r.RunID, t.runID)
		case r.Phase != PhaseTask && r.Phase != PhaseLookup:
			return fmt.Errorf("ledger: context phase %q is none", r.Phase)
		case len(r.SHA256) != 64 || strings.Trim(r.SHA256, "0123456789abcdef") != "":
			return fmt.Errorf("ledger: context digest %q is not a SHA-256", r.SHA256)
		case r.Bytes < 1 || r.Seq < 1:
			return fmt.Errorf("ledger: context seq %d of %d bytes", r.Seq, r.Bytes)
		}
		if err := rowText("", r.Source, r.Title, r.Ref, r.Pack); err != nil {
			return err
		}
		k := key{r.TaskID, r.N}
		if !counted[k] {
			counted[k] = true
			var task, lookup, last int
			if err := t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(SUM(phase = 'task'), 0), COALESCE(SUM(phase = 'lookup'), 0), COALESCE(MAX(seq), 0)
				FROM attempt_context WHERE run_id = ? AND task_id = ? AND n = ?`, t.runID, r.TaskID, r.N).Scan(&task, &lookup, &last); err != nil {
				return err
			}
			var addTask, addLookup int
			next := last
			for _, o := range rows {
				if o.TaskID != r.TaskID || o.N != r.N {
					continue
				}
				if next++; o.Seq != next {
					return fmt.Errorf("ledger: context seq %d of %s/%d is not %d", o.Seq, r.TaskID, r.N, next)
				}
				if o.Phase == PhaseTask {
					addTask++
				} else {
					addLookup++
				}
			}
			if task+addTask > MaxContextTaskRows || lookup+addLookup > MaxContextLookupRows || addLookup > MaxContextLookupOne {
				return ErrContextFull
			}
		}
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO attempt_context (run_id, task_id, n, seq, phase, source, title, ref, pack, sha256, bytes, at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.runID, r.TaskID, r.N, r.Seq, r.Phase, r.Source, r.Title, r.Ref, r.Pack, r.SHA256, r.Bytes, stamp(cmp.Or(r.At, t.now))); err != nil {
			return err
		}
	}
	return nil
}

// AttemptContext reads the context recorded for the run's task's
// attempts, by attempt, each attempt's in its seq's order; none before
// version 15.
func (l *Ledger) AttemptContext(ctx context.Context, runID, taskID string) (map[int][]AttemptContextRow, error) {
	db, version, err := l.connAt()
	if err != nil {
		return nil, err
	}
	if version < 15 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT n, seq, phase, source, title, ref, pack, sha256, bytes, at
		FROM attempt_context WHERE run_id = ? AND task_id = ? ORDER BY n, seq`, runID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int][]AttemptContextRow{}
	for rows.Next() {
		r := AttemptContextRow{RunID: runID, TaskID: taskID}
		var at string
		if err := rows.Scan(&r.N, &r.Seq, &r.Phase, &r.Source, &r.Title, &r.Ref, &r.Pack, &r.SHA256, &r.Bytes, &at); err != nil {
			return nil, err
		}
		r.At = unstamp(at)
		out[r.N] = append(out[r.N], r)
	}
	return out, rows.Err()
}

// AttemptContextCounts reads how many blocks each attempt of the run was
// given, by task and attempt; none before version 15.
func (l *Ledger) AttemptContextCounts(ctx context.Context, runID string) (map[string]map[int]int, error) {
	db, version, err := l.connAt()
	if err != nil {
		return nil, err
	}
	if version < 15 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT task_id, n, COUNT(*) FROM attempt_context WHERE run_id = ? GROUP BY task_id, n`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int]int{}
	for rows.Next() {
		var task string
		var n, count int
		if err := rows.Scan(&task, &n, &count); err != nil {
			return nil, err
		}
		if out[task] == nil {
			out[task] = map[int]int{}
		}
		out[task][n] = count
	}
	return out, rows.Err()
}
