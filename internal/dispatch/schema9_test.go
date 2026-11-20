package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Schema version 9 (docs/plans/sync-seams.md, the step after OS-1b): a
// replica tag's overflow range and a project's share's words. Nothing on
// this version reads or writes them.

// fixtureV8 is a copy of testdata/dispatch-v8.sqlite in a store directory
// of the test's: a version 8 database main's version 8 code wrote (the
// version 7 fixture brought to 8, then given a ticket, a replica's tag
// active and one retired, another replica's name, every change row's
// origin, CND shared with its number blocks' overflow range and its
// registry key, and a held row; its pages then compacted at 1 KiB).
func fixtureV8(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dispatch-v8.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	dir := storeDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, File), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// v8Rows is every row of every table the version 8 fixture holds (but the
// FTS index and the version), by table, read with the columns it has.
func v8Rows(t *testing.T, dir string) (rows, cols map[string][]string) {
	t.Helper()
	raw := openRaw(t, filepath.Join(dir, File))
	defer raw.Close()
	var version int
	if err := raw.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != syncKeptSchema-1 {
		t.Fatalf("the fixture is version %d %v", version, err)
	}
	rows, cols = map[string][]string{}, map[string][]string{}
	for _, table := range tables {
		if table == "tickets_fts" || table == "schema_version" {
			continue
		}
		cols[table] = columnsOf(t, raw, table)
		rows[table] = tableRows(t, raw, table, cols[table])
	}
	if len(rows["replica_tags"]) != 2 || len(rows["replica_names"]) != 1 || len(rows["held_rows"]) != 1 || len(rows["plan_views"]) == 0 ||
		len(rows["projects"]) < 2 || slices.Contains(cols["projects"], "share_words") || slices.Contains(cols["replica_tags"], "overflow_from") {
		t.Fatalf("the fixture: %d tags, %d names, %d held rows, %d views, %d projects; the projects' columns %v, the tags' %v",
			len(rows["replica_tags"]), len(rows["replica_names"]), len(rows["held_rows"]), len(rows["plan_views"]), len(rows["projects"]),
			cols["projects"], cols["replica_tags"])
	}
	return rows, cols
}

// version9Columns is the columns version 9 adds, by table, as a fresh
// store and a migrated one both hold them: name, type, not null, default.
var version9Columns = map[string][]string{
	"replica_tags": {"overflow_from INTEGER 1 0", "overflow_to INTEGER 1 0"},
	"projects":     {"share_words TEXT 1 ''"},
}

// columnDecls is the table's columns among names as name, type, not null
// and default.
func columnDecls(t *testing.T, q queryer, table string, names []string) []string {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), "SELECT name, type, \"notnull\", COALESCE(dflt_value, 'NULL') FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, typ, dflt string
		var notNull int
		if err := rows.Scan(&name, &typ, &notNull, &dflt); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(names, name) {
			out = append(out, fmt.Sprintf("%s %s %d %s", name, typ, notNull, dflt))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// columnNames is the names of version9Columns' columns of the table.
func columnNames(table string) []string {
	var out []string
	for _, c := range version9Columns[table] {
		out = append(out, strings.Fields(c)[0])
	}
	return out
}

// A fresh store makes version 9's columns in its creates, each its
// table's last, at version 9.
func TestAFreshStoreHasVersion9sColumns(t *testing.T) {
	s := openStore(t, storeDir(t))
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != syncKeptSchema || Version != syncKeptSchema {
		t.Fatalf("version %d (this build's %d) %v", version, Version, err)
	}
	for table, want := range version9Columns {
		if got := columnDecls(t, s.db, table, columnNames(table)); !slices.Equal(got, want) {
			t.Errorf("%s's version 9 columns: got %v, want %v", table, got, want)
		}
		cols := columnsOf(t, s.db, table)
		if last := cols[len(cols)-len(want):]; !slices.Equal(last, columnNames(table)) {
			t.Errorf("%s's last columns: %v", table, last)
		}
	}
}

// A version 8 database main's version 8 code wrote is brought to version 9
// at open: every row of every table as it was, read whole with the columns
// it had, the new columns at their defaults on every row (no range on a
// tag, no words on a share), no migrate row, and the schema a fresh
// store's.
func TestSchema9MigratesAndKeepsEveryRow(t *testing.T) {
	dir := fixtureV8(t)
	before, cols := v8Rows(t, dir)
	s, err := OpenOptions(dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != syncKeptSchema {
		t.Fatalf("version %d %v", version, err)
	}
	for table, c := range cols {
		if got := tableRows(t, s.db, table, c); !slices.Equal(got, before[table]) {
			t.Errorf("%s's rows changed:\n got %v\nwant %v", table, got, before[table])
		}
	}
	for table, want := range version9Columns {
		if got := columnDecls(t, s.db, table, columnNames(table)); !slices.Equal(got, want) {
			t.Errorf("%s's version 9 columns: got %v, want %v", table, got, want)
		}
	}
	for _, check := range []struct{ table, where string }{
		{"replica_tags", "overflow_from != 0 OR overflow_to != 0"},
		{"projects", "share_words != ''"},
	} {
		if n := count(t, s, "SELECT COUNT(*) FROM "+check.table+" WHERE "+check.where); n != 0 {
			t.Errorf("%d rows of %s off their defaults: %s", n, check.table, check.where)
		}
	}
	// The change rows compared whole above are every row: no migrate row
	// was written.
	fresh := openStore(t, storeDir(t))
	if got, want := schemaShape(t, s.db), schemaShape(t, fresh.db); !slices.Equal(got, want) {
		t.Fatalf("the migrated schema is not a fresh store's:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// A migration from version 8 that fails part way (its projects already
// holding a share_words of another kind, so the second statement is
// refused after the first added a tag's range) leaves the database as it
// was: version 8, no column of version 9's, every row as it was; the store
// opens it read-only and says why.
func TestAFailedMigrationLeavesVersion8AsItWas(t *testing.T) {
	dir := fixtureV8(t)
	path := filepath.Join(dir, File)
	raw := openRaw(t, path)
	if _, err := raw.Exec("ALTER TABLE projects ADD COLUMN share_words BLOB"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	before, cols := v8RowsAnyColumns(t, dir)
	s, err := OpenOptions(dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	ro, why := s.ReadOnly()
	s.Close()
	if !ro || !strings.Contains(why, fmt.Sprintf("migrating from version %d", syncKeptSchema-1)) {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	after := openRaw(t, path)
	defer after.Close()
	var version int
	if err := after.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != syncKeptSchema-1 {
		t.Fatalf("version %d %v", version, err)
	}
	if got := columnDecls(t, after, "replica_tags", columnNames("replica_tags")); len(got) != 0 {
		t.Fatalf("the tags gained %v", got)
	}
	for table, c := range cols {
		if got := tableRows(t, after, table, c); !slices.Equal(got, before[table]) {
			t.Errorf("%s's rows changed:\n got %v\nwant %v", table, got, before[table])
		}
	}
	if got, want := columnsOf(t, after, "replica_tags"), cols["replica_tags"]; !slices.Equal(got, want) {
		t.Fatalf("the tags' columns: %v, were %v", got, want)
	}
}

// v8RowsAnyColumns is v8Rows without its check of the columns: every row
// of every table, read with the columns it has.
func v8RowsAnyColumns(t *testing.T, dir string) (rows, cols map[string][]string) {
	t.Helper()
	raw := openRaw(t, filepath.Join(dir, File))
	defer raw.Close()
	rows, cols = map[string][]string{}, map[string][]string{}
	for _, table := range tables {
		if table == "tickets_fts" || table == "schema_version" {
			continue
		}
		cols[table] = columnsOf(t, raw, table)
		rows[table] = tableRows(t, raw, table, cols[table])
	}
	return rows, cols
}

// A version 9 database opens read-only under a binary that writes version
// 8, as every later version does: it says so, refuses a write, and reads
// what it holds (a ticket, a replica's tag).
func TestVersion9OpensReadOnlyInVersion8(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV8(t)
	s := openStore(t, dir)
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	_, key := newTicket(t, s, "CND", "Made at nine")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	older, err := openWith(dir, func(s *Store) { s.writes = syncKeptSchema - 1 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { older.Close() })
	if ro, why := older.ReadOnly(); !ro || !strings.Contains(why, fmt.Sprintf("the database is version %d, written by a newer Conductor; this one writes version %d", syncKeptSchema, syncKeptSchema-1)) {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	rc, err := older.CreateTicket(ctx, testOp("older-create"), NewTicket{Project: "CND", Title: "Made at eight"})
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if tk, err := older.Ticket(ctx, key); err != nil || tk.Title != "Made at nine" {
		t.Fatalf("the ticket: %+v %v", tk.Ticket, err)
	}
	if n := count(t, older, "SELECT COUNT(*) FROM replica_tags WHERE tag = 'a1' AND active = 1"); n != 1 {
		t.Fatalf("%d active tags a1", n)
	}
}
