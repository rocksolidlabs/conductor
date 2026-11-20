package ledger

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The record of the context each attempt was given (version 15): the
// table, its migration, the rules the database itself holds, and the
// version's barrier.

// putContext writes a row of attempt_context as SQL alone would: no
// writer of the ledger's exists for it yet.
func putContext(l *Ledger, run, task string, n, seq int, phase string) error {
	_, err := l.db.Exec(`INSERT INTO attempt_context (run_id, task_id, n, seq, phase, source, title, ref, pack, sha256, bytes, at)
		VALUES (?, ?, ?, ?, ?, 'graph', 'Callers of Build', 'build.go', 'p-1', ?, 42, '2026-11-19T00:00:00Z')`,
		run, task, n, seq, phase, strings.Repeat("ab", 32))
	return err
}

// columnsOf is the table's columns as SQLite reads them (name, type, not
// null, default, key), its foreign keys, and its text, with each check's.
func columnsOf(t *testing.T, l *Ledger, table string) []string {
	t.Helper()
	var out []string
	rows, err := l.db.Query("SELECT name, type, \"notnull\", COALESCE(dflt_value, ''), pk FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, typ, def string
		var notNull, pk int
		if err := rows.Scan(&name, &typ, &notNull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("column %s %s notnull=%d default=%q pk=%d", name, typ, notNull, def, pk))
	}
	rows.Close()
	fks, err := l.db.Query(`SELECT "table", "from", "to", on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		t.Fatal(err)
	}
	for fks.Next() {
		var to, from, col, onDelete string
		if err := fks.Scan(&to, &from, &col, &onDelete); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("foreign %s -> %s.%s on delete %s", from, to, col, onDelete))
	}
	fks.Close()
	return append(out, "sql "+originSQL(t, l, table))
}

// isContextEntry says whether name is a schema entry version 15 adds: the
// table, and its key's automatic index.
func isContextEntry(name string) bool {
	return name == "attempt_context" || name == "sqlite_autoindex_attempt_context_1"
}

// A new database has the record of the context each attempt was given,
// with the plan's columns, keyed by the attempt and the block's seq; the
// database holds its rules: a phase other than task or lookup is
// refused, a second row of one seq of an attempt is refused, a row of an
// attempt the ledger has not is refused, and a pruned run's rows go with
// its attempts. Opened again, the database is as it was; and a migration
// that fails (a table of the version's name there already) leaves the
// version 14 database exactly as it was, read-only, the why naming the
// table, and the next open, the cause gone, migrates.
func TestAttemptContextMigrationFreshAndAgain(t *testing.T) {
	freshDir := filepath.Join(t.TempDir(), "runs")
	fresh := openLedger(t, freshDir)
	if v := dbVersion(t, fresh); v != Version || Version != 16 {
		t.Fatalf("a new database is version %d, Version %d", v, Version)
	}
	want := []string{
		`column run_id TEXT notnull=1 default="" pk=1`,
		`column task_id TEXT notnull=1 default="" pk=2`,
		`column n INTEGER notnull=1 default="" pk=3`,
		`column seq INTEGER notnull=1 default="" pk=4`,
		`column phase TEXT notnull=1 default="" pk=0`,
		`column source TEXT notnull=1 default="" pk=0`,
		`column title TEXT notnull=1 default="" pk=0`,
		`column ref TEXT notnull=1 default="''" pk=0`,
		`column pack TEXT notnull=1 default="''" pk=0`,
		`column sha256 TEXT notnull=1 default="" pk=0`,
		`column bytes INTEGER notnull=1 default="" pk=0`,
		`column at TEXT notnull=1 default="" pk=0`,
		`foreign run_id -> attempts.run_id on delete CASCADE`,
		`foreign task_id -> attempts.task_id on delete CASCADE`,
		`foreign n -> attempts.n on delete CASCADE`,
	}
	freshCols := columnsOf(t, fresh, "attempt_context")
	if !slices.Equal(freshCols[:len(want)], want) || !strings.Contains(freshCols[len(want)], "CHECK (phase IN ('task', 'lookup'))") {
		t.Fatalf("a new database's attempt_context:\n%s", strings.Join(freshCols, "\n"))
	}

	const run = "team-00000001"
	newRun(t, fresh, run)
	tasksOf(t, fresh, run, "build")
	if err := putContext(fresh, run, "build", 1, 1, "task"); err != nil {
		t.Fatal(err)
	}
	if err := putContext(fresh, run, "build", 1, 2, "lookup"); err != nil {
		t.Fatal(err)
	}
	for what, err := range map[string]error{
		"a phase that is none":          putContext(fresh, run, "build", 1, 3, "start"),
		"a seq given twice":             putContext(fresh, run, "build", 1, 1, "task"),
		"an attempt the ledger has not": putContext(fresh, run, "build", 2, 1, "task"),
		"a task the ledger has not":     putContext(fresh, run, "docs", 1, 1, "task"),
	} {
		if err == nil {
			t.Errorf("%s: taken", what)
		}
	}
	if n := countRows(t, fresh, "SELECT COUNT(*) FROM attempt_context"); n != 2 {
		t.Fatalf("%d rows", n)
	}
	// A pruned run's rows go with its attempts.
	newRun(t, fresh, "team-00000002")
	tasksOf(t, fresh, "team-00000002", "build")
	if err := putContext(fresh, "team-00000002", "build", 1, 1, "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.db.Exec("DELETE FROM runs WHERE id = 'team-00000002'"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, fresh, "SELECT COUNT(*) FROM attempt_context WHERE run_id = 'team-00000002'"); n != 0 {
		t.Fatalf("a pruned run keeps %d rows", n)
	}
	freshEntries, freshRows := allEntries(t, fresh), everyRow(t, fresh)
	fresh.Close()
	reopened := openLedger(t, freshDir)
	if ro, why := reopened.ReadOnly(); ro {
		t.Fatalf("read-only when opened again: %s", why)
	}
	if got := allEntries(t, reopened); !slices.Equal(got, freshEntries) {
		t.Fatalf("the schema when opened again:\n%v\nwas:\n%v", got, freshEntries)
	}
	if got := everyRow(t, reopened); !maps.EqualFunc(got, freshRows, slices.Equal) {
		t.Fatalf("the rows when opened again:\n%v\nwere:\n%v", got, freshRows)
	}
	reopened.Close()

	// A version 14 database, then a table of the version's name there
	// already: the migration fails in its one transaction and nothing of
	// it is kept, every entry and row as they were.
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	newRun(t, l, run)
	tasksOf(t, l, run, "build")
	l.Close()
	rawExec(t, dir, append(slices.Clone(version15Added), "UPDATE schema_version SET version = 14", "CREATE TABLE attempt_context (x)")...)
	v14 := openReadOnly(t, dir)
	entries, rows := allEntries(t, v14), everyRow(t, v14)
	v14.Close()
	failed := openLedger(t, dir)
	if ro, why := failed.ReadOnly(); !ro || !strings.Contains(why, "attempt_context") {
		t.Fatalf("a failed migration: read-only %v: %s", ro, why)
	}
	if v := dbVersion(t, failed); v != 14 {
		t.Fatalf("version %d after a failed migration", v)
	}
	if got := allEntries(t, failed); !slices.Equal(got, entries) {
		t.Fatalf("the schema after a failed migration:\n%v\nwas:\n%v", got, entries)
	}
	if got := everyRow(t, failed); !maps.EqualFunc(got, rows, slices.Equal) {
		t.Fatalf("the rows after a failed migration:\n%v\nwere:\n%v", got, rows)
	}
	if _, ok, err := failed.Run(context.Background(), run); err != nil || !ok {
		t.Fatalf("a version 14 ledger's run, read: %v %v", ok, err)
	}
	failed.Close()
	rawExec(t, dir, "DROP TABLE attempt_context")
	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only once the cause is gone: %s", why)
	}
	if v := dbVersion(t, again); v != Version {
		t.Fatalf("version %d after the migration", v)
	}
	if got := columnsOf(t, again, "attempt_context"); !slices.Equal(got, freshCols) {
		t.Fatalf("attempt_context after the migration:\n%s\na new database's:\n%s", strings.Join(got, "\n"), strings.Join(freshCols, "\n"))
	}
}

// A version 14 database holding rows in every older kind of table (a run,
// its tasks and attempts, a decision, a preview, a run's origin, an outbox
// row) is brought to version 15 as it opens: the record is added with a
// new database's definition, column by column with its check's text, and
// holds no row; every other entry and every row is as it was, compared
// whole. The open after it changes nothing.
func TestAttemptContextMigrationKeepsEveryRow(t *testing.T) {
	fresh := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	freshCols := columnsOf(t, fresh, "attempt_context")
	fresh.Close()

	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build", "docs")
	n := open(t, l, agentDecision(run, "build", 1, "q1"))
	if err := putPreview(l, run, func(tx *Tx) error { return tx.OpenPreview(openRow(1)) }); err != nil {
		t.Fatal(err)
	}
	if err := putOrigin(l, run, "example", "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := putOutbox(l, n); err != nil {
		t.Fatal(err)
	}
	l.Close()
	rawExec(t, dir, append(slices.Clone(version15Added), "UPDATE schema_version SET version = 14")...)

	v14 := openReadOnly(t, dir)
	if v := dbVersion(t, v14); v != 14 {
		t.Fatalf("the fixture is version %d", v)
	}
	if text := originSQL(t, v14, "attempt_context"); text != "" {
		t.Fatalf("the version 14 fixture has version 15's table: %q", text)
	}
	before, entries := everyRow(t, v14), allEntries(t, v14)
	for _, table := range []string{"runs", "tasks", "attempts", "events", "decisions", "previews", "run_origin", "decision_outbox"} {
		if len(before[table]) == 0 {
			t.Fatalf("the fixture's %s holds no rows", table)
		}
	}
	v14.Close()

	migrated := openLedger(t, dir)
	if ro, why := migrated.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, migrated); v != Version || Version != 16 {
		t.Fatalf("version %d after the migration, Version %d", v, Version)
	}
	if got := columnsOf(t, migrated, "attempt_context"); !slices.Equal(got, freshCols) {
		t.Fatalf("attempt_context after the migration:\n%s\na new database's:\n%s", strings.Join(got, "\n"), strings.Join(freshCols, "\n"))
	}
	if n := countRows(t, migrated, "SELECT COUNT(*) FROM attempt_context"); n != 0 {
		t.Fatalf("%d rows after the migration", n)
	}
	rest := slices.DeleteFunc(allEntries(t, migrated), func(e string) bool {
		_, name, _ := strings.Cut(e, " ")
		name, _, _ = strings.Cut(name, " ")
		// Version 16's tables come with the migration from 14 too.
		return isContextEntry(name) || isSyncEntry(name)
	})
	if !slices.Equal(rest, entries) {
		t.Fatalf("the schema after the migration:\n%v\nbefore:\n%v", rest, entries)
	}
	after := everyRow(t, migrated)
	others := maps.Clone(after)
	delete(others, "attempt_context")
	delete(others, "decision_statements")
	delete(others, "decision_output")
	if !maps.EqualFunc(others, before, slices.Equal) {
		t.Fatalf("the rows after the migration:\n%v\nbefore:\n%v", after, before)
	}
	afterEntries := allEntries(t, migrated)
	migrated.Close()

	third := openLedger(t, dir)
	if ro, why := third.ReadOnly(); ro {
		t.Fatalf("read-only when opened after the migration: %s", why)
	}
	if got := allEntries(t, third); !slices.Equal(got, afterEntries) {
		t.Fatalf("the schema when opened after the migration:\n%v\nwas:\n%v", got, afterEntries)
	}
	if got := everyRow(t, third); !maps.EqualFunc(got, after, slices.Equal) {
		t.Fatalf("the rows when opened after the migration:\n%v\nwere:\n%v", got, after)
	}
}

// Version 15 is a barrier: a binary that writes 14 opens a version 15
// ledger read-only. That binary's rule for a later version is this one's
// (schemaAt), so a ledger one version past this binary's stands for it:
// read-only in the words of a newer Conductor, its writes and its recovery
// refused, read as it is, and every row (a recorded block's among them)
// and every entry as it was. Opened again by a binary of its own version,
// it is the owner's, as it was.
func TestOlderBinaryOpensAnAttemptContextLedgerReadOnly(t *testing.T) {
	if Version != 16 || len(migrations[14]) == 0 {
		t.Fatalf("Version %d, version 14's migration %v: the record of an attempt's context is version 15, a barrier of its own, and 16 the version after it", Version, migrations[14])
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build")
	if err := putContext(l, run, "build", 1, 1, "task"); err != nil {
		t.Fatal(err)
	}
	before, entries := everyRow(t, l), allEntries(t, l)
	if len(before["attempt_context"]) != 1 {
		t.Fatalf("the ledger holds %d recorded blocks", len(before["attempt_context"]))
	}
	l.Close()

	rawExec(t, dir, fmt.Sprintf("UPDATE schema_version SET version = %d", Version+1))
	later := openLedger(t, dir)
	ro, why := later.ReadOnly()
	if !ro || !strings.Contains(why, fmt.Sprintf("the database is version %d, written by a newer Conductor; this one writes version %d", Version+1, Version)) {
		t.Fatalf("a later version's ledger: read-only %v: %s", ro, why)
	}
	if _, err := later.Transition(ctx, run, func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a write to a later version's ledger: %v", err)
	}
	if _, err := later.Recover(ctx); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a recovery of a later version's ledger: %v", err)
	}
	if _, ok, err := later.Run(ctx, run); err != nil || !ok {
		t.Fatalf("a later version's run, read: %v %v", ok, err)
	}
	if got := everyRow(t, later); !maps.EqualFunc(got, before, slices.Equal) {
		t.Fatalf("a later version's ledger after the refusals:\n%v\nwere:\n%v", got, before)
	}
	if got := allEntries(t, later); !slices.Equal(got, entries) {
		t.Fatalf("a later version's schema after the refusals:\n%v\nwas:\n%v", got, entries)
	}
	later.Close()

	rawExec(t, dir, fmt.Sprintf("UPDATE schema_version SET version = %d", Version))
	own := openLedger(t, dir)
	if ro, why := own.ReadOnly(); ro {
		t.Fatalf("read-only at its own version: %s", why)
	}
	if got := everyRow(t, own); !maps.EqualFunc(got, before, slices.Equal) {
		t.Fatalf("the ledger at its own version:\n%v\nwas:\n%v", got, before)
	}
}
