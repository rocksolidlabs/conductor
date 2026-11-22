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

// The outcome outbox (version 14): the table and its index, their
// migration, the rule the database itself holds, and the version's
// barrier.

// isOutboxEntry says whether name is a schema entry version 14 adds: the
// table and its index on the decision.
func isOutboxEntry(name string) bool {
	return name == "decision_outbox" || name == "decision_outbox_decision"
}

// putOutbox writes a row of decision_outbox as SQL alone would: no writer
// of the ledger's exists for it yet.
func putOutbox(l *Ledger, decision int64) error {
	_, err := l.db.Exec("INSERT INTO decision_outbox (decision, outcome, at) VALUES (?, ?, ?)", decision, `{"outcome":"applied"}`, "2026-11-19T00:00:00Z")
	return err
}

// allEntries is every schema entry of l, as type, name and text.
func allEntries(t *testing.T, l *Ledger) []string {
	t.Helper()
	rows, err := l.db.Query("SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ, name, text string
		if err := rows.Scan(&typ, &name, &text); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ+" "+name+" "+text)
	}
	return out
}

// everyRow is every row of every table of l but the version's own, by
// table.
func everyRow(t *testing.T, l *Ledger) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range schemaNames(t, l, "table") {
		if table != "schema_version" {
			out[table] = tableRows(t, l, table)
		}
	}
	return out
}

// A new database has the outbox and its index; the index keeps one row a
// decision, a decision with no record of its own is taken (no foreign key:
// a row outlives a pruned run), and a removed row's seq is never given
// again. Opened again, the database is as it was. A version 13 database
// holding a run, its tasks, a decision, a preview and a run's origin is
// brought to version 14 as it opens: the outbox and its index are added
// with a new database's definitions and hold no row, and every other
// entry and row is as it was. A migration that fails (a table of that
// name there already) leaves it at version 13, read-only, the why naming
// the table; the next open, the cause gone, migrates, and the one after
// it changes nothing.
func TestRoutedLedgerMigrationFreshAndAgain(t *testing.T) {
	freshDir := filepath.Join(t.TempDir(), "runs")
	fresh := openLedger(t, freshDir)
	freshTable, freshIndex := originSQL(t, fresh, "decision_outbox"), originSQL(t, fresh, "decision_outbox_decision")
	if !strings.Contains(freshTable, "CREATE TABLE decision_outbox") || !strings.Contains(freshTable, "AUTOINCREMENT") || strings.Contains(freshTable, "REFERENCES") {
		t.Fatalf("a new database's decision_outbox: %q", freshTable)
	}
	if !strings.Contains(freshIndex, "CREATE UNIQUE INDEX decision_outbox_decision ON decision_outbox(decision)") {
		t.Fatalf("a new database's decision_outbox_decision: %q", freshIndex)
	}
	if v := dbVersion(t, fresh); v != Version || Version != 17 {
		t.Fatalf("a new database is version %d, Version %d", v, Version)
	}
	if n := countRows(t, fresh, "SELECT COUNT(*) FROM decision_outbox"); n != 0 {
		t.Fatalf("a new database's outbox holds %d rows", n)
	}
	// One row a decision; a decision no record holds is taken.
	if err := putOutbox(fresh, 1); err != nil {
		t.Fatal(err)
	}
	if err := putOutbox(fresh, 1); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("a second row for one decision: %v", err)
	}
	if err := putOutbox(fresh, 999); err != nil {
		t.Fatalf("a row for a decision no record holds: %v", err)
	}
	// A seq is never given again, even once its row is gone.
	if _, err := fresh.db.Exec("DELETE FROM decision_outbox WHERE decision = 999"); err != nil {
		t.Fatal(err)
	}
	if err := putOutbox(fresh, 2); err != nil {
		t.Fatal(err)
	}
	if got := tableRows(t, fresh, "decision_outbox"); len(got) != 2 || !strings.HasPrefix(got[0], "[1 1 ") || !strings.HasPrefix(got[1], "[3 2 ") {
		t.Fatalf("the outbox's rows: %v", got)
	}
	freshEntries, freshRows := allEntries(t, fresh), everyRow(t, fresh)
	fresh.Close()
	reopened := openLedger(t, freshDir)
	if ro, why := reopened.ReadOnly(); ro {
		t.Fatalf("read-only when opened again: %s", why)
	}
	if v := dbVersion(t, reopened); v != Version {
		t.Fatalf("version %d when opened again", v)
	}
	if got := allEntries(t, reopened); !slices.Equal(got, freshEntries) {
		t.Fatalf("the schema when opened again:\n%v\nwas:\n%v", got, freshEntries)
	}
	if got := everyRow(t, reopened); !maps.EqualFunc(got, freshRows, slices.Equal) {
		t.Fatalf("the rows when opened again:\n%v\nwere:\n%v", got, freshRows)
	}
	reopened.Close()

	// A version 13 database with rows in every older kind of table.
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build", "docs")
	open(t, l, agentDecision(run, "build", 1, "q1"))
	if err := putPreview(l, run, func(tx *Tx) error { return tx.OpenPreview(openRow(1)) }); err != nil {
		t.Fatal(err)
	}
	if err := putOrigin(l, run, "example", "op-1"); err != nil {
		t.Fatal(err)
	}
	l.Close()
	rawExec(t, dir, append(slices.Clone(version14Added), "UPDATE schema_version SET version = 13")...)

	v13 := openReadOnly(t, dir)
	if v := dbVersion(t, v13); v != 13 {
		t.Fatalf("the fixture is version %d", v)
	}
	if text := originSQL(t, v13, "decision_outbox") + originSQL(t, v13, "decision_outbox_decision"); text != "" {
		t.Fatalf("the version 13 fixture has version 14's schema: %q", text)
	}
	before := everyRow(t, v13)
	for _, table := range []string{"runs", "tasks", "decisions", "previews", "run_origin"} {
		if len(before[table]) == 0 {
			t.Fatalf("the fixture's %s holds no rows", table)
		}
	}
	entries := allEntries(t, v13)
	v13.Close()

	// A table of the version's name already there: the migration fails,
	// and nothing of it is kept.
	rawExec(t, dir, "CREATE TABLE decision_outbox (x)")
	failed := openLedger(t, dir)
	if ro, why := failed.ReadOnly(); !ro || !strings.Contains(why, "decision_outbox") {
		t.Fatalf("a failed migration: read-only %v: %s", ro, why)
	}
	if v := dbVersion(t, failed); v != 13 {
		t.Fatalf("version %d after a failed migration", v)
	}
	if text := originSQL(t, failed, "decision_outbox_decision"); text != "" {
		t.Fatalf("the index after a failed migration: %q", text)
	}
	if _, ok, err := failed.Run(context.Background(), run); err != nil || !ok {
		t.Fatalf("a version 13 ledger's run, read: %v %v", ok, err)
	}
	failed.Close()
	rawExec(t, dir, "DROP TABLE decision_outbox")

	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, again); v != Version {
		t.Fatalf("version %d after the migration, Version %d", v, Version)
	}
	if got := originSQL(t, again, "decision_outbox"); got != freshTable {
		t.Fatalf("decision_outbox after the migration: %q, a new database's %q", got, freshTable)
	}
	if got := originSQL(t, again, "decision_outbox_decision"); got != freshIndex {
		t.Fatalf("decision_outbox_decision after the migration: %q, a new database's %q", got, freshIndex)
	}
	if n := countRows(t, again, "SELECT COUNT(*) FROM decision_outbox"); n != 0 {
		t.Fatalf("%d outbox rows after the migration", n)
	}
	migrated := slices.DeleteFunc(allEntries(t, again), func(e string) bool {
		_, name, _ := strings.Cut(e, " ")
		name, _, _ = strings.Cut(name, " ")
		// Version 15's and 16's tables come with the migration from 13 too.
		return isOutboxEntry(name) || isContextEntry(name) || isSyncEntry(name)
	})
	if !slices.Equal(migrated, entries) {
		t.Fatalf("the schema after the migration:\n%v\nbefore:\n%v", migrated, entries)
	}
	after := everyRow(t, again)
	rest := maps.Clone(after)
	delete(rest, "decision_outbox")
	delete(rest, "attempt_context")
	delete(rest, "decision_statements")
	delete(rest, "decision_output")
	if !maps.EqualFunc(rest, before, slices.Equal) {
		t.Fatalf("the rows after the migration:\n%v\nbefore:\n%v", after, before)
	}
	afterEntries := allEntries(t, again)
	again.Close()

	// A run again: nothing to do, nothing changed.
	third := openLedger(t, dir)
	if ro, why := third.ReadOnly(); ro {
		t.Fatalf("read-only when opened after the migration: %s", why)
	}
	if v := dbVersion(t, third); v != Version {
		t.Fatalf("version %d when opened after the migration", v)
	}
	if got := allEntries(t, third); !slices.Equal(got, afterEntries) {
		t.Fatalf("the schema when opened after the migration:\n%v\nwas:\n%v", got, afterEntries)
	}
	if got := everyRow(t, third); !maps.EqualFunc(got, after, slices.Equal) {
		t.Fatalf("the rows when opened after the migration:\n%v\nwere:\n%v", got, after)
	}
}

// Version 14 is a barrier: a binary that writes 13 opens a version 14
// ledger read-only. That binary's rule for a later version is this one's
// (schemaAt), so a ledger one version past this binary's stands for it:
// read-only in the words of a newer Conductor, its writes and its recovery
// refused, read as it is, and every row (an outbox row among them) and
// every entry as it was. Opened again by a binary of its own version, it
// is the owner's, as it was.
func TestOlderBinaryOpensARoutedLedgerReadOnly(t *testing.T) {
	if Version != 17 || len(migrations[13]) == 0 {
		t.Fatalf("Version %d, version 13's migration %v: the outbox's barrier is 14, a version of its own, and 15 to 17 the versions after it", Version, migrations[13])
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build")
	n := open(t, l, agentDecision(run, "build", 1, "q1"))
	if err := putOutbox(l, n); err != nil {
		t.Fatal(err)
	}
	before, entries := everyRow(t, l), allEntries(t, l)
	if len(before["decision_outbox"]) != 1 || len(before["decisions"]) != 1 {
		t.Fatalf("the ledger holds %d outbox rows and %d decisions", len(before["decision_outbox"]), len(before["decisions"]))
	}
	l.Close()

	// As version 14 is to a binary that writes 13.
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
	if rec, ok, err := later.Decision(ctx, n); err != nil || !ok || rec.N != n {
		t.Fatalf("a later version's decision, read: %+v %v %v", rec, ok, err)
	}
	if got := everyRow(t, later); !maps.EqualFunc(got, before, slices.Equal) {
		t.Fatalf("a later version's ledger after the refusals:\n%v\nwere:\n%v", got, before)
	}
	if got := allEntries(t, later); !slices.Equal(got, entries) {
		t.Fatalf("a later version's schema after the refusals:\n%v\nwas:\n%v", got, entries)
	}
	if v := dbVersion(t, later); v != Version+1 {
		t.Fatalf("the later version's ledger is version %d now", v)
	}
	later.Close()

	// Back at its own version, opened by its own binary: the owner's, as it
	// was.
	rawExec(t, dir, fmt.Sprintf("UPDATE schema_version SET version = %d", Version))
	own := openLedger(t, dir)
	if ro, why := own.ReadOnly(); ro {
		t.Fatalf("read-only at its own version: %s", why)
	}
	if got := everyRow(t, own); !maps.EqualFunc(got, before, slices.Equal) {
		t.Fatalf("the ledger at its own version:\n%v\nwas:\n%v", got, before)
	}
}
