package ledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
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

// fullOrigin is an origin row of run with every column given.
func fullOrigin(run string) OriginRow {
	return OriginRow{RunID: run, Provider: "example", OpID: "op-1", Ref: "example:42", Label: "Review of change 42",
		Project: "CND", Subject: "change:git.example.com/acme/app#42@" + strings.Repeat("a", 40), SubjectURL: "https://git.example.com/acme/app/pull/42",
		Prior: "team-0000000a", StartCommit: strings.Repeat("b", 40), Deadline: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), LimitSeconds: 3600,
		ResolvedDigest: strings.Repeat("c", 64), Agents: `[{"agent":"claude","selection":"` + strings.Repeat("d", 64) + `"}]`}
}

// putOriginTx writes row in a transition of run.
func putOriginTx(l *Ledger, run string, row OriginRow) error {
	_, err := l.Transition(context.Background(), run, func(tx *Tx) error { return tx.PutOrigin(row) })
	return err
}

// Each column of an origin row is held to its bound by its writer: at the
// bound it is kept, read back as written; one past it is refused
// (ErrBadOrigin) with nothing written, the run's revision as it was. A
// row of another run than the transaction's is refused, and so is a
// second row of the run (ErrOriginExists) and an operation id the
// provider's rows hold (ErrOpExists).
func TestRunOriginRowBounds(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	hex := func(n int) string { return strings.Repeat("e", n) }
	runID := func(n int) string { return strings.Repeat("a", n-9) + "-0000000f" }
	cases := []struct {
		name     string
		at, past func(*OriginRow)
	}{
		{name: "provider", at: func(r *OriginRow) { r.Provider = strings.Repeat("p", MaxOriginProvider) }, past: func(r *OriginRow) { r.Provider = strings.Repeat("p", MaxOriginProvider+1) }},
		{name: "provider's form", at: func(r *OriginRow) { r.Provider = "a-1" }, past: func(r *OriginRow) { r.Provider = "A" }},
		{name: "no provider", at: func(r *OriginRow) {}, past: func(r *OriginRow) { r.Provider = "" }},
		{name: "op id", at: func(r *OriginRow) { r.OpID = strings.Repeat("o", MaxOriginOpID) }, past: func(r *OriginRow) { r.OpID = strings.Repeat("o", MaxOriginOpID+1) }},
		{name: "op id's form", at: func(r *OriginRow) { r.OpID = "A.b_c~d-1" }, past: func(r *OriginRow) { r.OpID = "a b" }},
		{name: "no op id", at: func(r *OriginRow) { r.OpID = "" }, past: func(r *OriginRow) { r.OpID = "é" }},
		{name: "ref", at: func(r *OriginRow) { r.Ref = strings.Repeat("r", MaxOriginRef) }, past: func(r *OriginRow) { r.Ref = strings.Repeat("r", MaxOriginRef+1) }},
		{name: "ref's form", at: func(r *OriginRow) { r.Ref = "a:b~!" }, past: func(r *OriginRow) { r.Ref = "a b" }},
		{name: "no ref", at: func(r *OriginRow) {}, past: func(r *OriginRow) { r.Ref = "" }},
		{name: "label", at: func(r *OriginRow) { r.Label = strings.Repeat("é", MaxOriginLabel) }, past: func(r *OriginRow) { r.Label = strings.Repeat("é", MaxOriginLabel+1) }},
		{name: "label's line", at: func(r *OriginRow) { r.Label = "a # b" }, past: func(r *OriginRow) { r.Label = "a\nb" }},
		{name: "no label", at: func(r *OriginRow) {}, past: func(r *OriginRow) { r.Label = "" }},
		{name: "project", at: func(r *OriginRow) { r.Project = "A12345" }, past: func(r *OriginRow) { r.Project = "A123456" }},
		{name: "project's form", at: func(r *OriginRow) { r.Project = "" }, past: func(r *OriginRow) { r.Project = "cnd" }},
		{name: "subject", at: func(r *OriginRow) { r.Subject = strings.Repeat("s", MaxOriginSubject) }, past: func(r *OriginRow) { r.Subject = strings.Repeat("s", MaxOriginSubject+1) }},
		{name: "subject's line", at: func(r *OriginRow) { r.Subject = "" }, past: func(r *OriginRow) { r.Subject = "a\rb" }},
		{name: "link", at: func(r *OriginRow) { r.SubjectURL = "https://x/" + strings.Repeat("u", MaxOriginURL-10) }, past: func(r *OriginRow) { r.SubjectURL = "https://x/" + strings.Repeat("u", MaxOriginURL-9) }},
		{name: "link's scheme", at: func(r *OriginRow) { r.SubjectURL = "" }, past: func(r *OriginRow) { r.SubjectURL = "http://x/" }},
		{name: "link's query", at: func(r *OriginRow) {}, past: func(r *OriginRow) { r.SubjectURL = "https://x/?token=1" }},
		{name: "link's fragment", at: func(r *OriginRow) {}, past: func(r *OriginRow) { r.SubjectURL = "https://x/#a" }},
		{name: "earlier run", at: func(r *OriginRow) { r.Prior = runID(MaxOriginRunID) }, past: func(r *OriginRow) { r.Prior = runID(MaxOriginRunID + 1) }},
		{name: "earlier run's form", at: func(r *OriginRow) { r.Prior = "" }, past: func(r *OriginRow) { r.Prior = "team-0000000G" }},
		{name: "start commit", at: func(r *OriginRow) { r.StartCommit = hex(64) }, past: func(r *OriginRow) { r.StartCommit = hex(41) }},
		{name: "digest", at: func(r *OriginRow) { r.ResolvedDigest = hex(64) }, past: func(r *OriginRow) { r.ResolvedDigest = hex(63) }},
		{name: "limit", at: func(r *OriginRow) { r.LimitSeconds = MaxOriginLimit }, past: func(r *OriginRow) { r.LimitSeconds = MaxOriginLimit + 1 }},
		{name: "least limit", at: func(r *OriginRow) { r.LimitSeconds = MinOriginLimit }, past: func(r *OriginRow) { r.LimitSeconds = MinOriginLimit - 1 }},
		{name: "deadline with no limit", at: func(r *OriginRow) { r.Deadline, r.LimitSeconds = time.Time{}, 0 }, past: func(r *OriginRow) { r.LimitSeconds = 0 }},
		{name: "limit with no deadline", at: func(r *OriginRow) {}, past: func(r *OriginRow) { r.Deadline = time.Time{} }},
		{name: "agents", at: func(r *OriginRow) { r.Agents = `"` + strings.Repeat("x", MaxOriginAgents-2) + `"` }, past: func(r *OriginRow) { r.Agents = `"` + strings.Repeat("x", MaxOriginAgents-1) + `"` }},
		{name: "agents' JSON", at: func(r *OriginRow) { r.Agents = "" }, past: func(r *OriginRow) { r.Agents = "[" }},
	}
	for i, c := range cases {
		run := fmt.Sprintf("team-%08x", 2*i+1)
		newRun(t, l, run)
		before, _, _ := l.Run(ctx, run)
		past := fullOrigin(run)
		past.OpID = fmt.Sprintf("past-%d", i)
		c.past(&past)
		if err := putOriginTx(l, run, past); !errors.Is(err, ErrBadOrigin) {
			t.Fatalf("%s one past its bound: %v", c.name, err)
		}
		if after, _, _ := l.Run(ctx, run); after.Revision != before.Revision {
			t.Fatalf("%s: the refusal moved the run's revision", c.name)
		}
		if _, ok, err := l.Origin(ctx, run); ok || err != nil {
			t.Fatalf("%s: a refused row was written: %v %v", c.name, ok, err)
		}
		at := fullOrigin(run)
		at.OpID = fmt.Sprintf("at-%d", i)
		c.at(&at)
		if err := putOriginTx(l, run, at); err != nil {
			t.Fatalf("%s at its bound: %v", c.name, err)
		}
		if got, ok, err := l.Origin(ctx, run); err != nil || !ok || !reflect.DeepEqual(got, at) {
			t.Fatalf("%s at its bound, read back: %+v %v %v\nwritten %+v", c.name, got, ok, err, at)
		}
	}

	const run, other = "team-00001001", "team-00001002"
	newRun(t, l, run)
	newRun(t, l, other)
	if err := putOriginTx(l, run, fullOrigin(other)); !errors.Is(err, ErrBadOrigin) {
		t.Fatalf("a row of another run: %v", err)
	}
	if _, ok, _ := l.Origin(ctx, other); ok {
		t.Fatal("another run's row was written")
	}
	first := fullOrigin(run)
	first.OpID = "op-x"
	if err := putOriginTx(l, run, first); err != nil {
		t.Fatal(err)
	}
	again := fullOrigin(run)
	again.OpID = "op-y"
	if err := putOriginTx(l, run, again); !errors.Is(err, ErrOriginExists) {
		t.Fatalf("a second row of the run: %v", err)
	}
	taken := fullOrigin(other)
	taken.OpID = "op-x"
	if err := putOriginTx(l, other, taken); !errors.Is(err, ErrOpExists) {
		t.Fatalf("an operation id the provider's rows hold: %v", err)
	}
	if got, ok, _ := l.Origin(ctx, run); !ok || got.OpID != "op-x" {
		t.Fatalf("the first row after the refusals: %+v %v", got, ok)
	}
}

// RunByOp finds the run of a provider's operation id and no other: none
// for another provider's id, for an empty id (a continuation's row) and
// for an id no row holds. It is answered by a read-only ledger, and by
// one whose database is older than the table, with no row.
func TestRunByOpReadsTheProvidersOwn(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const run, cont = "team-00000001", "team-00000002"
	newRun(t, l, run)
	newRun(t, l, cont)
	if err := putOriginTx(l, run, fullOrigin(run)); err != nil {
		t.Fatal(err)
	}
	c := fullOrigin(cont)
	c.OpID = ""
	if err := putOriginTx(l, cont, c); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := l.RunByOp(ctx, "example", "op-1"); err != nil || !ok || got.RunID != run {
		t.Fatalf("the provider's op-1: %+v %v %v", got, ok, err)
	}
	for _, q := range [][2]string{{"other", "op-1"}, {"example", ""}, {"example", "op-2"}} {
		if got, ok, err := l.RunByOp(ctx, q[0], q[1]); ok || err != nil {
			t.Fatalf("%q's %q: %+v %v %v", q[0], q[1], got, ok, err)
		}
	}
	// The look-up searches the index; it never scans the table.
	plan, err := l.db.Query("EXPLAIN QUERY PLAN SELECT "+originCols+" FROM run_origin WHERE "+originByOp, "example", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for plan.Next() {
		var id, parent, unused int
		var detail string
		if err := plan.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	plan.Close()
	if len(details) != 1 || !strings.Contains(details[0], "USING INDEX run_origin_op") {
		t.Fatalf("RunByOp's plan: %q", details)
	}
	ro := openReadOnly(t, dir)
	if got, ok, err := ro.RunByOp(ctx, "example", "op-1"); err != nil || !ok || got.RunID != run {
		t.Fatalf("a read-only ledger's op-1: %+v %v %v", got, ok, err)
	}
	ro.Close()
	l.Close()
	rawExec(t, dir, append(slices.Clone(version13Added), "UPDATE schema_version SET version = 12")...)
	old := openReadOnly(t, dir)
	if got, ok, err := old.RunByOp(ctx, "example", "op-1"); ok || err != nil {
		t.Fatalf("a version 12 database's op-1: %+v %v %v", got, ok, err)
	}
	if got, ok, err := old.Origin(ctx, run); ok || err != nil {
		t.Fatalf("a version 12 database's origin: %+v %v %v", got, ok, err)
	}
}
