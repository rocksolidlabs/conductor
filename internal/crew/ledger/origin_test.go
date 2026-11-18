package ledger

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Where a run was launched from (version 13): the table and its index,
// their migration, and the rules the database itself holds.

// isOriginEntry says whether name is a schema entry version 13 adds: the
// table, its primary key's own index and the operation ids' index.
func isOriginEntry(name string) bool {
	return name == "run_origin" || name == "run_origin_op" || name == "sqlite_autoindex_run_origin_1"
}

// masterEntries is every schema entry of l but version 13's and the
// version's own row, as type, name and text.
func masterEntries(t *testing.T, l *Ledger) []string {
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
		if !isOriginEntry(name) {
			out = append(out, typ+" "+name+" "+text)
		}
	}
	return out
}

// originSQL is the text sqlite_master keeps for name, "" for none.
func originSQL(t *testing.T, l *Ledger, name string) string {
	t.Helper()
	var text string
	if err := l.db.QueryRow("SELECT COALESCE((SELECT sql FROM sqlite_master WHERE name = ?), '')", name).Scan(&text); err != nil {
		t.Fatal(err)
	}
	return text
}

// putOrigin writes a row of run_origin as SQL alone would: no writer of
// the ledger's exists for it.
func putOrigin(l *Ledger, run, provider, opID string) error {
	_, err := l.db.Exec("INSERT INTO run_origin (run_id, provider, op_id, ref) VALUES (?, ?, ?, ?)", run, provider, opID, "example:42")
	return err
}

// A version 12 database holding a run, its tasks, an attempt, a decision
// and a preview is brought to version 13 as it opens: run_origin and its
// index are added with a new database's definitions and hold no row, and
// every other table and index is as it was, each table's rows too. A
// migration that fails (a table of that name there already) leaves it at
// version 12, read-only, the why naming the table, and the next open, the
// cause gone, migrates.
func TestRunOriginMigration(t *testing.T) {
	fresh := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	freshTable, freshIndex := originSQL(t, fresh, "run_origin"), originSQL(t, fresh, "run_origin_op")
	if !strings.Contains(freshTable, "CREATE TABLE run_origin") || !strings.Contains(freshIndex, "WHERE op_id != ''") {
		t.Fatalf("a new database's run_origin %q and run_origin_op %q", freshTable, freshIndex)
	}

	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build", "docs")
	open(t, l, agentDecision(run, "build", 1, "q1"))
	if err := putPreview(l, run, func(tx *Tx) error { return tx.OpenPreview(openRow(1)) }); err != nil {
		t.Fatal(err)
	}
	l.Close()
	rawExec(t, dir, append(slices.Clone(version13Added), "UPDATE schema_version SET version = 12")...)

	v12 := openReadOnly(t, dir)
	if v := dbVersion(t, v12); v != 12 {
		t.Fatalf("the fixture is version %d", v)
	}
	if text := originSQL(t, v12, "run_origin") + originSQL(t, v12, "run_origin_op"); text != "" {
		t.Fatalf("the version 12 fixture has version 13's schema: %q", text)
	}
	tables := slices.DeleteFunc(schemaNames(t, v12, "table"), func(s string) bool { return s == "schema_version" })
	before := map[string][]string{}
	for _, table := range tables {
		before[table] = tableRows(t, v12, table)
	}
	for _, table := range []string{"runs", "tasks", "attempts", "decisions", "previews"} {
		if len(before[table]) == 0 {
			t.Fatalf("the fixture's %s holds no rows", table)
		}
	}
	entries := masterEntries(t, v12)
	v12.Close()

	// A table of the version's name already there: the migration fails,
	// and nothing of it is kept.
	rawExec(t, dir, "CREATE TABLE run_origin (x)")
	failed := openLedger(t, dir)
	if ro, why := failed.ReadOnly(); !ro || !strings.Contains(why, "run_origin") {
		t.Fatalf("a failed migration: read-only %v: %s", ro, why)
	}
	if v := dbVersion(t, failed); v != 12 {
		t.Fatalf("version %d after a failed migration", v)
	}
	if text := originSQL(t, failed, "run_origin_op"); text != "" {
		t.Fatalf("the index after a failed migration: %q", text)
	}
	if _, ok, err := failed.Run(context.Background(), run); err != nil || !ok {
		t.Fatalf("a version 12 ledger's run, read: %v %v", ok, err)
	}
	failed.Close()
	rawExec(t, dir, "DROP TABLE run_origin")

	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, again); v != Version || Version != 13 {
		t.Fatalf("version %d after the migration, Version %d", v, Version)
	}
	if got := originSQL(t, again, "run_origin"); got != freshTable {
		t.Fatalf("run_origin after the migration: %q, a new database's %q", got, freshTable)
	}
	if got := originSQL(t, again, "run_origin_op"); got != freshIndex {
		t.Fatalf("run_origin_op after the migration: %q, a new database's %q", got, freshIndex)
	}
	if n := countRows(t, again, "SELECT COUNT(*) FROM run_origin"); n != 0 {
		t.Fatalf("%d origins after the migration", n)
	}
	if got := masterEntries(t, again); !slices.Equal(got, entries) {
		t.Fatalf("the schema after the migration:\n%v\nbefore:\n%v", got, entries)
	}
	for _, table := range tables {
		if got := tableRows(t, again, table); !slices.Equal(got, before[table]) {
			t.Fatalf("%s after the migration:\n%v\nbefore:\n%v", table, got, before[table])
		}
	}
}

// One provider's operation id names one run: a second row with it is
// refused by the index, the same id under another provider is kept, and
// rows of one provider with an empty operation id are not keyed by it.
func TestRunOriginOpIDsAreOnePerProvider(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	runs := []string{"team-00000001", "team-00000002", "team-00000003", "team-00000004", "team-00000005"}
	for _, run := range runs {
		newRun(t, l, run)
	}
	if err := putOrigin(l, runs[0], "example", "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := putOrigin(l, runs[1], "example", "op-1"); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("a second run of one provider's operation id: %v", err)
	}
	if err := putOrigin(l, runs[1], "other", "op-1"); err != nil {
		t.Fatalf("another provider's run of the same operation id: %v", err)
	}
	if err := putOrigin(l, runs[2], "example", "op-2"); err != nil {
		t.Fatalf("another operation id of the provider: %v", err)
	}
	for _, run := range runs[3:] {
		if err := putOrigin(l, run, "example", ""); err != nil {
			t.Fatalf("a run of the provider with no operation id: %v", err)
		}
	}
	if n := countRows(t, l, "SELECT COUNT(*) FROM run_origin"); n != 5 {
		t.Fatalf("%d origins kept, want 5", n)
	}
	if n := countRows(t, l, "SELECT COUNT(*) FROM run_origin WHERE provider = 'example' AND op_id = 'op-1'"); n != 1 {
		t.Fatalf("%d runs of the provider's op-1", n)
	}
}

// A run's origin is its run's: one row a run, a row for a run the ledger
// lacks refused (the foreign key), and the row removed when its run is
// pruned.
func TestRunOriginGoesWithItsRun(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run, kept = "team-00000001", "team-00000002"
	if err := putOrigin(l, run, "example", "op-1"); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("an origin of a run the ledger lacks: %v", err)
	}
	newRun(t, l, run)
	newRun(t, l, kept)
	if err := putOrigin(l, run, "example", "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := putOrigin(l, run, "example", "op-2"); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("a second origin of one run: %v", err)
	}
	if err := putOrigin(l, kept, "example", "op-2"); err != nil {
		t.Fatal(err)
	}
	finish(t, l, run)
	if err := l.Prune(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, l, "SELECT COUNT(*) FROM run_origin WHERE run_id = ?", run); n != 0 {
		t.Fatalf("the pruned run's origin is still there (%d rows)", n)
	}
	if n := countRows(t, l, "SELECT COUNT(*) FROM run_origin WHERE run_id = ?", kept); n != 1 {
		t.Fatalf("the other run's origin: %d rows", n)
	}
}
