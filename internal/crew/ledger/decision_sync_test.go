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

// The tables version 16 adds for sync's seams (docs/plans/sync-seams.md,
// row OS-3a): a held answer's statement and a decision's copied-output
// verdicts, their migration, the rules the database holds, and the
// version's barrier.

// isSyncEntry says whether name is a schema entry version 16 adds: the two
// tables and their keys' automatic indexes.
func isSyncEntry(name string) bool {
	switch name {
	case "decision_statements", "sqlite_autoindex_decision_statements_1", "decision_output", "sqlite_autoindex_decision_output_1":
		return true
	}
	return false
}

// putStatement and putOutput write a row of each table as SQL alone
// would: no writer of the ledger's exists for either yet.
func putStatement(l *Ledger, decision int64, k int) error {
	_, err := l.db.Exec("INSERT INTO decision_statements (decision, k, statement) VALUES (?, ?, ?)", decision, k, `{"statement":"yes"}`)
	return err
}

func putOutput(l *Ledger, decision int64, part, verdict string) error {
	_, err := l.db.Exec("INSERT INTO decision_output (decision, part, verdict) VALUES (?, ?, ?)", decision, part, verdict)
	return err
}

// filled is a ledger at this binary's version with a row in every table
// version 15 has: a run, its tasks, attempts, events, a command settled,
// a message, a check, a prerequisite, a tree, a decision with a response,
// its history, a hold and a refund, a preview, a run's origin, an outbox
// row and a recorded block of context.
func filled(t *testing.T, dir string) *Ledger {
	t.Helper()
	l := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build", "docs")
	n := open(t, l, agentDecision(run, "build", 1, "q1"))
	in(t, l, run, func(tx *Tx) error {
		now := tx.Now()
		if _, _, err := tx.Command("admin", "c1", "pause", "fp-c1"); err != nil {
			return err
		}
		for _, err := range []error{
			tx.Settle("admin", "c1", "done", map[string]string{"task": "build"}),
			tx.PutMessage(MessageRow{RunID: run, Seq: 1, From: "lead", To: "docs", Text: "hello", CreatedAt: now}),
			tx.PutCheck(CheckRow{RunID: run, TaskID: "build", N: 1, ID: "test", K: 1, Phase: PhaseCheck, Argv: []string{"go", "test"}, Subject: "c1", State: CheckFinished, StartedAt: now, Where: WhereNative, Log: []byte("ok\n")}),
			tx.PutPrerequisite(PrerequisiteRow{RunID: run, ID: "node", K: 1, Argv: []string{"node", "--version"}, Manifest: "crew:1", State: CheckFinished, StartedAt: now, Where: WhereNative}),
			tx.PutTree(TreeRow{RunID: run, TaskID: "build", N: 1, Kind: TreeWorktree, Dir: "/w/build.1", Commit: "abc", Manifest: "crew:1", State: TreeReady}),
			tx.Hold(run, "docs", n),
		} {
			if err != nil {
				return err
			}
		}
		if _, _, err := tx.History(n, HistoryRow{Kind: HistoryOpened, Detail: `{"by":"plan"}`}); err != nil {
			return err
		}
		if _, err := tx.Respond(n, ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: "r1", Fingerprint: "fp-r1", Status: ResponseAccepted, Receipt: `{"words":"yes"}`}); err != nil {
			return err
		}
		if _, err := tx.Refund(run, "build", 1, n); err != nil {
			return err
		}
		return tx.OpenPreview(openRow(1))
	})
	if err := putOrigin(l, run, "example", "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := putOutbox(l, n); err != nil {
		t.Fatal(err)
	}
	if err := putContext(l, run, "build", 1, 1, "task"); err != nil {
		t.Fatal(err)
	}
	return l
}

// A new database has both tables with the plan's columns and keys, and no
// foreign key, as the plan gives them: a row is refused a second time for
// its key, and taken for a decision the ledger has not (a row outlives its
// decision: nothing deletes it with the run). Opened again, the database
// is as it was. A migration that fails partway (decision_statements made,
// a table named decision_output there already) is one transaction: the
// version 15 database is exactly as it was, read-only, the why naming the
// table; the next open, the cause gone, migrates to a schema equal to a new
// database's.
func TestSchema16FreshAndAFailedMigration(t *testing.T) {
	freshDir := filepath.Join(t.TempDir(), "runs")
	fresh := openLedger(t, freshDir)
	if v := dbVersion(t, fresh); v != Version || Version != 16 {
		t.Fatalf("a new database is version %d, Version %d", v, Version)
	}
	wantStatements := []string{
		`column decision INTEGER notnull=1 default="" pk=1`,
		`column k INTEGER notnull=1 default="" pk=2`,
		`column statement TEXT notnull=1 default="" pk=0`,
	}
	wantOutput := []string{
		`column decision INTEGER notnull=1 default="" pk=1`,
		`column part TEXT notnull=1 default="" pk=2`,
		`column verdict TEXT notnull=1 default="" pk=0`,
		`column cut_at INTEGER notnull=1 default="-1" pk=0`,
	}
	freshStatements, freshOutput := columnsOf(t, fresh, "decision_statements"), columnsOf(t, fresh, "decision_output")
	// Each is its columns and its text: no foreign key line between them.
	if !slices.Equal(freshStatements[:len(freshStatements)-1], wantStatements) || !slices.Equal(freshOutput[:len(freshOutput)-1], wantOutput) {
		t.Fatalf("a new database's tables:\n%s\n\n%s", strings.Join(freshStatements, "\n"), strings.Join(freshOutput, "\n"))
	}

	const run = "team-00000001"
	newRun(t, fresh, run)
	tasksOf(t, fresh, run, "build")
	n := open(t, fresh, agentDecision(run, "build", 1, "q1"))
	for _, err := range []error{putStatement(fresh, n, 1), putOutput(fresh, n, "question", "clean"), putOutput(fresh, n, "responses/1/text", "pending")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for what, err := range map[string]error{
		"a statement given twice": putStatement(fresh, n, 1),
		"a part judged twice":     putOutput(fresh, n, "question", "output"),
	} {
		if err == nil {
			t.Errorf("%s: taken", what)
		}
	}
	if err := putStatement(fresh, 999, 1); err != nil {
		t.Fatalf("a statement of a decision the ledger has not: %v", err)
	}
	if err := putOutput(fresh, 999, "question", "clean"); err != nil {
		t.Fatalf("a verdict of a decision the ledger has not: %v", err)
	}
	// A pruned run's decisions go; the rows keyed by them stay.
	if _, err := fresh.db.Exec("DELETE FROM runs WHERE id = ?", run); err != nil {
		t.Fatal(err)
	}
	if d := countRows(t, fresh, "SELECT COUNT(*) FROM decisions"); d != 0 {
		t.Fatalf("%d decisions after the run's prune", d)
	}
	if s, o := countRows(t, fresh, "SELECT COUNT(*) FROM decision_statements"), countRows(t, fresh, "SELECT COUNT(*) FROM decision_output"); s != 2 || o != 3 {
		t.Fatalf("after the run's prune: %d statements, %d verdicts", s, o)
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

	dir := filepath.Join(t.TempDir(), "runs")
	filled(t, dir).Close()
	rawExec(t, dir, append(slices.Clone(version16Added), "UPDATE schema_version SET version = 15", "CREATE TABLE decision_output (x)")...)
	v15 := openReadOnly(t, dir)
	entries, rows := allEntries(t, v15), everyRow(t, v15)
	v15.Close()
	failed := openLedger(t, dir)
	if ro, why := failed.ReadOnly(); !ro || !strings.Contains(why, "decision_output") {
		t.Fatalf("a failed migration: read-only %v: %s", ro, why)
	}
	if v := dbVersion(t, failed); v != 15 {
		t.Fatalf("version %d after a failed migration", v)
	}
	if text := originSQL(t, failed, "decision_statements"); text != "" {
		t.Fatalf("the failed migration kept its first table: %q", text)
	}
	if got := allEntries(t, failed); !slices.Equal(got, entries) {
		t.Fatalf("the schema after a failed migration:\n%v\nwas:\n%v", got, entries)
	}
	if got := everyRow(t, failed); !maps.EqualFunc(got, rows, slices.Equal) {
		t.Fatalf("the rows after a failed migration:\n%v\nwere:\n%v", got, rows)
	}
	failed.Close()
	rawExec(t, dir, "DROP TABLE decision_output")
	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only once the cause is gone: %s", why)
	}
	if v := dbVersion(t, again); v != Version {
		t.Fatalf("version %d after the migration", v)
	}
	if got := columnsOf(t, again, "decision_statements"); !slices.Equal(got, freshStatements) {
		t.Fatalf("decision_statements after the migration:\n%s\na new database's:\n%s", strings.Join(got, "\n"), strings.Join(freshStatements, "\n"))
	}
	if got := columnsOf(t, again, "decision_output"); !slices.Equal(got, freshOutput) {
		t.Fatalf("decision_output after the migration:\n%s\na new database's:\n%s", strings.Join(got, "\n"), strings.Join(freshOutput, "\n"))
	}
}

// A version 15 database with a row in every table it has is brought to
// version 16 as it opens: both tables are added with a new database's
// definitions and hold no row; every other entry and every row is as it
// was, compared whole; the migrated schema is a new database's; and the
// open after it changes nothing.
func TestSchema16MigratesAndKeepsEveryRow(t *testing.T) {
	fresh := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	freshEntries := allEntries(t, fresh)
	fresh.Close()

	dir := filepath.Join(t.TempDir(), "runs")
	filled(t, dir).Close()
	rawExec(t, dir, append(slices.Clone(version16Added), "UPDATE schema_version SET version = 15")...)

	v15 := openReadOnly(t, dir)
	if v := dbVersion(t, v15); v != 15 {
		t.Fatalf("the fixture is version %d", v)
	}
	before, entries := everyRow(t, v15), allEntries(t, v15)
	for table, rows := range before {
		if len(rows) == 0 {
			t.Errorf("the fixture's %s holds no rows", table)
		}
	}
	for _, table := range []string{"decision_statements", "decision_output"} {
		if _, ok := before[table]; ok {
			t.Fatalf("the version 15 fixture has %s", table)
		}
	}
	v15.Close()

	migrated := openLedger(t, dir)
	if ro, why := migrated.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, migrated); v != Version || Version != 16 {
		t.Fatalf("version %d after the migration, Version %d", v, Version)
	}
	if got := allEntries(t, migrated); !slices.Equal(got, freshEntries) {
		t.Fatalf("the migrated schema:\n%v\na new database's:\n%v", got, freshEntries)
	}
	rest := slices.DeleteFunc(allEntries(t, migrated), func(e string) bool {
		_, name, _ := strings.Cut(e, " ")
		name, _, _ = strings.Cut(name, " ")
		return isSyncEntry(name)
	})
	if !slices.Equal(rest, entries) {
		t.Fatalf("the schema after the migration:\n%v\nbefore:\n%v", rest, entries)
	}
	after := everyRow(t, migrated)
	if len(after["decision_statements"]) != 0 || len(after["decision_output"]) != 0 {
		t.Fatalf("rows after the migration: %v %v", after["decision_statements"], after["decision_output"])
	}
	others := maps.Clone(after)
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

// Version 16 is a barrier: a binary that writes 15 opens a version 16
// ledger read-only. That binary's rule for a later version is this one's
// (schemaAt), so a ledger one version past this binary's stands for it:
// read-only in the words of a newer Conductor, its writes and its recovery
// refused, read as it is, every row (a statement's and a verdict's among
// them) and every entry as it was. Opened again by a binary of its own
// version, it is the owner's, as it was.
func TestVersion16OpensReadOnlyInVersion15(t *testing.T) {
	if Version != 16 || len(migrations[15]) != 2 {
		t.Fatalf("Version %d, version 15's migration %v: a held answer's statements and a decision's verdicts are version 16, a barrier of its own", Version, migrations[15])
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "runs")
	l := filled(t, dir)
	const run = "team-00000001"
	n := int64(1)
	if err := putStatement(l, n, 1); err != nil {
		t.Fatal(err)
	}
	if err := putOutput(l, n, "question", "clean"); err != nil {
		t.Fatal(err)
	}
	before, entries := everyRow(t, l), allEntries(t, l)
	if len(before["decision_statements"]) != 1 || len(before["decision_output"]) != 1 {
		t.Fatalf("the ledger holds %d statements and %d verdicts", len(before["decision_statements"]), len(before["decision_output"]))
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
