package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// Where each row ran (version 7, docs/plans/runtime.md, PR-4): an
// attempt's, a receipt's (a task's check, a manifest's step) and a
// prerequisite's row say it, the puts refuse a row that does not, and a
// database of an earlier version, migrated or only read, says native.

// seedWhere puts an attempt of run team-00000001, a check and a step of
// it and a prerequisite of the run, each where they ran.
func seedWhere(t *testing.T, l *Ledger, where string) {
	t.Helper()
	newRun(t, l, "team-00000001")
	putWhere(t, l, where)
}

// putWhere puts seedWhere's rows of the run, new or again, each where they
// ran.
func putWhere(t *testing.T, l *Ledger, where string) {
	t.Helper()
	if _, err := l.Transition(context.Background(), "team-00000001", func(tx *Tx) error {
		if err := tx.PutTask(TaskRow{RunID: "team-00000001", ID: "build", Member: "lead", Kind: "work", State: TaskRunning}); err != nil {
			return err
		}
		if err := tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 1, Member: "lead", State: AttemptVerifying, ReservedAt: tx.Now(), Where: where}); err != nil {
			return err
		}
		if err := tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "unit", K: 1, Argv: []string{"go", "test"}, State: CheckFinished, Exit: 0, StartedAt: tx.Now(), FinishedAt: tx.Now(), Log: []byte("ok"), Where: where}); err != nil {
			return err
		}
		if err := tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "deps", K: 2, Phase: PhaseSetup, Manifest: "crew:1", Argv: []string{"npm", "ci"}, State: CheckFinished, StartedAt: tx.Now(), FinishedAt: tx.Now(), Where: where}); err != nil {
			return err
		}
		if err := tx.PutPrerequisite(PrerequisiteRow{RunID: "team-00000001", ID: "node", K: 1, Argv: []string{"node", "--version"}, Manifest: "crew:1", State: CheckFinished, StartedAt: tx.Now(), FinishedAt: tx.Now(), Where: where}); err != nil {
			return err
		}
		return tx.Event("check.finished", map[string]any{"task": "build", "attempt": 1, "check": "unit", "k": 1})
	}); err != nil {
		t.Fatal(err)
	}
}

// readWhere reads every row seedWhere put back, through each reader, and
// answers the Where each says, by reader.
func readWhere(t *testing.T, l *Ledger) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	attempts, err := l.Attempts(ctx, "team-00000001")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts: %+v %v", attempts, err)
	}
	out["attempt"] = attempts[0].Where
	checks, err := l.Checks(ctx, "team-00000001", "", 0, false)
	if err != nil || len(checks) != 2 {
		t.Fatalf("checks: %+v %v", checks, err)
	}
	for _, c := range checks {
		out["checks/"+c.ID] = c.Where
	}
	c, ok, err := l.Check(ctx, "team-00000001", "build", 1, "unit", 1)
	if err != nil || !ok {
		t.Fatalf("check: %v %v", ok, err)
	}
	out["check"] = c.Where
	prereqs, err := l.Prerequisites(ctx, "team-00000001", true)
	if err != nil || len(prereqs) != 1 {
		t.Fatalf("prerequisites: %+v %v", prereqs, err)
	}
	out["prerequisites"] = prereqs[0].Where
	p, ok, err := l.Prerequisite(ctx, "team-00000001", "node", 1)
	if err != nil || !ok {
		t.Fatalf("prerequisite: %v %v", ok, err)
	}
	out["prerequisite"] = p.Where
	return out
}

// allWhere fails unless every reader of got said want.
func allWhere(t *testing.T, got map[string]string, want, when string) {
	t.Helper()
	if len(got) != 6 {
		t.Fatalf("%s: %d readers answered: %v", when, len(got), got)
	}
	for k, v := range got {
		if v != want {
			t.Fatalf("%s: %s says %q, want %q (all: %v)", when, k, v, want, got)
		}
	}
}

// asVersion6 makes the database one a version 6 Conductor wrote: no
// where_ran on any of the three tables.
func asVersion6(t *testing.T, l *Ledger) {
	t.Helper()
	for _, stmt := range append(slices.Clone(version7Added), "UPDATE schema_version SET version = 6") {
		if _, err := l.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// whereColumns counts the where_ran columns of each of the three tables.
func whereColumns(t *testing.T, l *Ledger) map[string]int {
	t.Helper()
	db, err := l.conn()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, table := range []string{"attempts", "checks", "prerequisites"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'where_ran'", table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func dbVersion(t *testing.T, l *Ledger) int {
	t.Helper()
	db, err := l.conn()
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow("SELECT version FROM schema_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A version 6 database (no where_ran) is brought to version 7, and on to
// the version this Conductor writes, as it opens: each attempt, check, step and prerequisite row says native, which
// is what ran every row a version 6 database holds, and its events are as
// they were.
func TestAVersion6DatabaseIsBroughtToVersion7(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	ctx := context.Background()
	seedWhere(t, l, WhereNative)
	before, _ := l.Events(ctx, "team-00000001", 0, 100)
	asVersion6(t, l)
	l.Close()

	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, again); v != Version {
		t.Fatalf("version %d after the migration", v)
	}
	if cols := whereColumns(t, again); cols["attempts"] != 1 || cols["checks"] != 1 || cols["prerequisites"] != 1 {
		t.Fatalf("where_ran columns after the migration: %v", cols)
	}
	allWhere(t, readWhere(t, again), WhereNative, "after the migration")
	after, _ := again.Events(ctx, "team-00000001", 0, 100)
	if len(after) != len(before) {
		t.Fatalf("events after the migration: %+v, before %+v", after, before)
	}
	for i := range after {
		if after[i].Kind != before[i].Kind || after[i].Payload != before[i].Payload || after[i].Seq != before[i].Seq {
			t.Fatalf("event %d after the migration: %+v, before %+v", i, after[i], before[i])
		}
	}
}

// A version 4 database reaches version 7 through migration 4's frozen
// prerequisites table (prerequisitesTableV5): one where_ran column on each
// table, none duplicated (a step to 7 that met it already would fail and
// undo the upgrade), every row native.
func TestAVersion4DatabaseGainsWhereOnce(t *testing.T) {
	if strings.Contains(prerequisitesTableV5, "where_ran") || !strings.Contains(prerequisitesTable, "where_ran") {
		t.Fatal("migration 4's prerequisites table is not version 5's, or the schema's has no where_ran")
	}
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	newRun(t, l, "team-00000001")
	attemptOf(t, l, "team-00000001", "build", 1, AttemptVerifying)
	if _, err := l.Transition(context.Background(), "team-00000001", func(tx *Tx) error {
		return tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "unit", K: 1, Argv: []string{"go", "test"}, State: CheckFinished, StartedAt: tx.Now(), Where: WhereNative})
	}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range append(slices.Clone(version5Added), "UPDATE schema_version SET version = 4") {
		if _, err := l.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	l.Close()

	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, again); v != Version {
		t.Fatalf("version %d after the migration, Version %d", v, Version)
	}
	if cols := whereColumns(t, again); cols["attempts"] != 1 || cols["checks"] != 1 || cols["prerequisites"] != 1 {
		t.Fatalf("where_ran columns after the migration: %v", cols)
	}
	ctx := context.Background()
	if a, err := again.Attempts(ctx, "team-00000001"); err != nil || len(a) != 1 || a[0].Where != WhereNative {
		t.Fatalf("the attempt after the migration: %+v %v", a, err)
	}
	if c, err := again.Checks(ctx, "team-00000001", "build", 1, false); err != nil || len(c) != 1 || c[0].Where != WhereNative {
		t.Fatalf("the check after the migration: %+v %v", c, err)
	}
	// A prerequisite put after it, by the column the migration added.
	if _, err := again.Transition(ctx, "team-00000001", func(tx *Tx) error {
		return tx.PutPrerequisite(PrerequisiteRow{RunID: "team-00000001", ID: "node", K: 1, State: CheckFinished, StartedAt: tx.Now(), Where: "fake"})
	}); err != nil {
		t.Fatal(err)
	}
	if p, err := again.Prerequisites(ctx, "team-00000001", false); err != nil || len(p) != 1 || p[0].Where != "fake" {
		t.Fatalf("a prerequisite after the migration: %+v %v", p, err)
	}
}

// A version 6 database whose step to 7 fails (a statement of the
// migration made to fail, the tests' seam as takeLock is one) stays a
// version 6 one, opened read-only with why: its attempts, checks and
// prerequisites are read all the same, every row native (its columns are
// version 6's, where_ran read as 'native'), and nothing is written.
func TestAVersion6LedgerThatCannotMigrateIsRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	seedWhere(t, l, WhereNative)
	asVersion6(t, l)
	l.Close()

	old := migrations[6]
	migrations[6] = append(slices.Clone(old), "ALTER TABLE no_such_table ADD COLUMN x TEXT")
	t.Cleanup(func() { migrations[6] = old })
	again := openLedger(t, dir)
	ro, why := again.ReadOnly()
	if !ro || !strings.Contains(why, "migrating from version 6") {
		t.Fatalf("a migration that fails: read-only %v: %s", ro, why)
	}
	if v := dbVersion(t, again); v != 6 {
		t.Fatalf("version %d after a failed migration", v)
	}
	if cols := whereColumns(t, again); cols["attempts"] != 0 || cols["checks"] != 0 || cols["prerequisites"] != 0 {
		t.Fatalf("where_ran columns after a failed migration: %v", cols)
	}
	allWhere(t, readWhere(t, again), WhereNative, "read unmigrated")
	if _, err := again.Transition(context.Background(), "team-00000001", func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a write to the unmigrated database: %v", err)
	}
}

// While a new server waits for the owner lock the server it replaces
// still holds, it reads the version 6 database read-only and unmigrated,
// every row native; once the lock is let go the database is migrated to
// 7 and the reads read the column.
func TestAnUpgradeBesideThePreviousOwnerReads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	seedWhere(t, l, WhereNative)
	asVersion6(t, l)
	l.Close()

	// The lock is another's (Open's try and the wait's) until the test
	// lets it go. A try is made under the ledger's lock, so it answers at
	// once rather than wait at the gate, which would hold the reads.
	old, oldWait := takeLock, LockWait
	LockWait = time.Hour // the gate bounds the wait, not the clock
	gate := make(chan struct{})
	takeLock = func(path string) (*ownerLock, string, bool, error) {
		select {
		case <-gate:
			return old(path)
		default:
			return nil, "another server holds the owner lock (test)", true, nil
		}
	}
	var opened *Ledger
	t.Cleanup(func() {
		if opened != nil {
			opened.Close()
		}
		takeLock, LockWait = old, oldWait
	})
	waiting, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	opened = waiting
	if ro, why := waiting.ReadOnly(); !ro || !strings.HasPrefix(why, "waiting for the owner lock") {
		t.Fatalf("beside the previous owner: read-only %v: %s", ro, why)
	}
	if v := dbVersion(t, waiting); v != 6 {
		t.Fatalf("version %d while waiting", v)
	}
	allWhere(t, readWhere(t, waiting), WhereNative, "while waiting")

	owned := make(chan struct{})
	waiting.OnOwned(func() { close(owned) })
	close(gate)
	<-owned
	if ro, why := waiting.ReadOnly(); ro {
		t.Fatalf("read-only once owned: %s", why)
	}
	if v := dbVersion(t, waiting); v != Version {
		t.Fatalf("version %d once owned", v)
	}
	allWhere(t, readWhere(t, waiting), WhereNative, "once owned")
	// The reads read the column now: a row put where another Runtime ran
	// says so.
	if _, err := waiting.Transition(context.Background(), "team-00000001", func(tx *Tx) error {
		return tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 2, Member: "lead", State: AttemptReserved, ReservedAt: tx.Now(), Where: "fake"})
	}); err != nil {
		t.Fatal(err)
	}
	if a, err := waiting.Attempts(context.Background(), "team-00000001"); err != nil || len(a) != 2 || a[1].Where != "fake" {
		t.Fatalf("the attempts once owned: %+v %v", a, err)
	}
}

// A ledger reading a version 6 database read-only, another server its
// owner, reads the rows that owner puts once it has migrated the database
// to 7 by their column: the version is read again with the rows, in one
// snapshot (readAt), never kept from the attach while the database moved
// on, which would read every row as native.
func TestAReaderSeesAnotherOwnersMigration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	seedWhere(t, l, WhereNative)
	asVersion6(t, l)
	l.Close()

	// The reader: the lock another's and not waited for, so it stays a
	// reader, never migrating, while the owner opened next migrates.
	old := takeLock
	t.Cleanup(func() { takeLock = old })
	takeLock = func(string) (*ownerLock, string, bool, error) {
		return nil, "another server owns the ledger (test)", false, nil
	}
	reader := openLedger(t, dir)
	takeLock = old
	if ro, _ := reader.ReadOnly(); !ro {
		t.Fatal("the reader opened as the owner")
	}
	if v := dbVersion(t, reader); v != 6 {
		t.Fatalf("version %d before the owner's migration", v)
	}
	allWhere(t, readWhere(t, reader), WhereNative, "before the owner's migration")

	owner := openLedger(t, dir)
	if ro, why := owner.ReadOnly(); ro {
		t.Fatalf("the owner opened read-only: %s", why)
	}
	if v := dbVersion(t, owner); v != Version {
		t.Fatalf("version %d after the owner's migration", v)
	}
	putWhere(t, owner, "fake")
	allWhere(t, readWhere(t, reader), "fake", "the reader, after the owner's migration")
	if v := dbVersion(t, reader); v != Version {
		t.Fatalf("the reader reads version %d", v)
	}
}

// Each of the three puts refuses a row that does not say where it ran
// (ErrNoWhere) and one whose Where is past MaxWhere bytes (ErrTooLarge),
// and the transaction is undone with it.
func TestARowWithNoWhereIsRefused(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	seedWhere(t, l, WhereNative)
	puts := map[string]func(tx *Tx, where string) error{
		"attempt": func(tx *Tx, where string) error {
			return tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 2, Member: "lead", State: AttemptReserved, ReservedAt: tx.Now(), Where: where})
		},
		"check": func(tx *Tx, where string) error {
			return tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "lint", K: 3, State: CheckStarted, StartedAt: tx.Now(), Where: where})
		},
		"prerequisite": func(tx *Tx, where string) error {
			return tx.PutPrerequisite(PrerequisiteRow{RunID: "team-00000001", ID: "node", K: 2, State: CheckStarted, StartedAt: tx.Now(), Where: where})
		},
	}
	for name, put := range puts {
		for where, want := range map[string]error{"": ErrNoWhere, strings.Repeat("a", MaxWhere+1): ErrTooLarge} {
			rev, _ := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("mark", nil) })
			_, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
				if err := tx.Event("before", nil); err != nil {
					return err
				}
				return put(tx, where)
			})
			if !errors.Is(err, want) {
				t.Fatalf("%s with where %q: %v, want %v", name, where, err, want)
			}
			if r, _, _ := l.Run(ctx, "team-00000001"); r.Revision != rev {
				t.Fatalf("%s refused: the revision moved from %d to %d", name, rev, r.Revision)
			}
		}
		// MaxWhere bytes are taken.
		if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return put(tx, strings.Repeat("a", MaxWhere)) }); err != nil {
			t.Fatalf("%s with where of %d bytes: %v", name, MaxWhere, err)
		}
	}
	if evs, _ := l.Events(ctx, "team-00000001", 0, 1000); slices.ContainsFunc(evs, func(e EventRow) bool { return e.Kind == "before" }) {
		t.Fatal("a refused row's transaction left its event")
	}
	if a, _ := l.Attempts(ctx, "team-00000001"); len(a) != 2 || a[1].Where != strings.Repeat("a", MaxWhere) {
		t.Fatalf("the attempts: %+v", a)
	}
}

// A row keeps its Where: put, read back by every reader, and put again
// (a receipt's running and finished rows) unchanged.
func TestARowKeepsItsWhere(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	seedWhere(t, l, "fake")
	allWhere(t, readWhere(t, l), "fake", "as put")
	if _, err := l.Transition(context.Background(), "team-00000001", func(tx *Tx) error {
		if err := tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 1, Member: "lead", State: AttemptSucceeded, ReservedAt: tx.Now(), ClosedAt: tx.Now(), Where: "fake"}); err != nil {
			return err
		}
		return tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "unit", K: 1, Argv: []string{"go", "test"}, State: CheckFinished, Exit: 1, StartedAt: tx.Now(), FinishedAt: tx.Now(), Where: "fake"})
	}); err != nil {
		t.Fatal(err)
	}
	allWhere(t, readWhere(t, l), "fake", "put again")
	// Recover concludes states and leaves where they ran.
	if _, err := l.Transition(context.Background(), "team-00000001", func(tx *Tx) error {
		return tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "unit", K: 1, Argv: []string{"go", "test"}, State: CheckRunning, StartedAt: tx.Now(), Where: "fake"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c, ok, err := l.Check(context.Background(), "team-00000001", "build", 1, "unit", 1); err != nil || !ok || c.State != CheckInterrupted || c.Where != "fake" {
		t.Fatalf("a recovered check: %+v %v %v", c, ok, err)
	}
}

// The ledger's native is ext's: a row the native Runtime ran says its
// Where's text, and a valid Where fits MaxWhere.
func TestWhereNativeIsExtNative(t *testing.T) {
	if WhereNative != string(ext.WhereNative) {
		t.Fatalf("ledger.WhereNative %q, ext.WhereNative %q", WhereNative, ext.WhereNative)
	}
	if err := ext.Where(strings.Repeat("a", MaxWhere)).Valid(); err != nil {
		t.Fatalf("a Where of MaxWhere bytes: %v", err)
	}
	if err := ext.Where(strings.Repeat("a", MaxWhere+1)).Valid(); err == nil {
		t.Fatal("a Where past MaxWhere bytes is valid: the ledger's bound is not ext's")
	}
}

// A reader of an unmigrated database (readAt's read transaction, the
// ledger's one connection held) and a Snapshot (mu held, waiting for that
// connection in its VACUUM INTO) overlap and both finish: the read gives
// the connection back before it takes mu to keep a version, never the
// other way round, which would leave each waiting on the other (Codex
// round 2 on #588). The overlap is the seam's: the read holds, inside its
// transaction, until the Snapshot waits for the connection (the pool's
// wait count), then goes on.
func TestAnUnmigratedReadAndASnapshotOverlap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	seedWhere(t, l, WhereNative)
	asVersion6(t, l)
	l.Close()

	old := takeLock
	t.Cleanup(func() { takeLock = old })
	takeLock = func(string) (*ownerLock, string, bool, error) {
		return nil, "another server owns the ledger (test)", false, nil
	}
	reader := openLedger(t, dir)
	takeLock = old
	if ro, _ := reader.ReadOnly(); !ro {
		t.Fatal("the reader opened as the owner")
	}
	db, err := reader.conn()
	if err != nil {
		t.Fatal(err)
	}

	// The deadline only ends the deadlock this test catches, so the test
	// fails rather than hang; the overlap that passes never meets it.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dst := filepath.Join(t.TempDir(), "copy.db")
	snapped := make(chan error, 1)
	reader.inRead = func() {
		waits := db.Stats().WaitCount
		go func() { snapped <- reader.Snapshot(ctx, dst) }()
		for db.Stats().WaitCount == waits {
			select {
			case err := <-snapped:
				snapped <- err
				return // it finished without waiting: nothing overlaps
			case <-ctx.Done():
				return
			default:
				runtime.Gosched()
			}
		}
	}
	attempts, err := reader.Attempts(ctx, "team-00000001")
	reader.inRead = nil
	if err != nil || len(attempts) != 1 || attempts[0].Where != WhereNative {
		t.Fatalf("the read beside the snapshot: %+v %v", attempts, err)
	}
	if err := <-snapped; err != nil {
		t.Fatalf("the snapshot beside the read: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the read and the snapshot waited on each other until the deadline")
	}
}
