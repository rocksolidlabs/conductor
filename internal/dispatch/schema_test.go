package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// schemaShape is the schema of a database as SQLite reads it back, one
// line per table and index by name: a table's columns (name, type, not
// null, default, place in the primary key) and foreign keys, an index's
// table, uniqueness, origin, partiality and columns. Two databases are
// compared by it, since a column a migration adds is the same column as
// one a CREATE TABLE makes though the statements' texts differ.
func schemaShape(t *testing.T, db *sql.DB) []string {
	t.Helper()
	objects := map[string][]string{}
	rows, err := db.Query("SELECT type, name, tbl_name FROM sqlite_master WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	type object struct{ kind, name, table string }
	var all []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.kind, &o.name, &o.table); err != nil {
			t.Fatal(err)
		}
		all = append(all, o)
	}
	rows.Close()
	lines := func(query string, args ...any) []string {
		r, err := db.Query(query, args...)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer r.Close()
		cols, _ := r.Columns()
		var out []string
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprint(vals...))
		}
		return out
	}
	var shape []string
	for _, o := range all {
		switch o.kind {
		case "table":
			objects[o.name] = append(lines("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid", o.name),
				lines("SELECT \"table\", \"from\", \"to\" FROM pragma_foreign_key_list(?) ORDER BY id, seq", o.name)...)
		case "index":
			objects[o.name] = append(lines("SELECT \"unique\", origin, partial FROM pragma_index_list(?) WHERE name = ?", o.table, o.name),
				lines("SELECT name FROM pragma_index_info(?) ORDER BY seqno", o.name)...)
		}
		shape = append(shape, o.kind+" "+o.name+" on "+o.table+": "+strings.Join(objects[o.name], "; "))
	}
	// The primary keys' own indexes, which sqlite_master names
	// sqlite_autoindex_*: their tables and columns.
	for _, o := range all {
		if o.kind != "table" {
			continue
		}
		for _, ix := range lines("SELECT name FROM pragma_index_list(?) WHERE origin = 'pk' ORDER BY name", o.name) {
			shape = append(shape, "primary key of "+o.name+": "+strings.Join(lines("SELECT name FROM pragma_index_info(?) ORDER BY seqno", ix), ", "))
		}
	}
	return shape
}

// openRaw opens the database file at path with the driver alone, none of
// the store's checks.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// versionOneStore is the directory of a version 1 store holding what fill
// wrote: the rows are written by this binary's operations, and the
// database then taken back to version 1 as DS1 left it (the version 2
// tables dropped, a link's three columns dropped, a link's add rows
// without the fingerprint version 1 never wrote, the version 1), its
// schema then checked to be version 1's (schemaVersion1) whole, so what
// a test migrates is a version 1 database and nothing else.
func versionOneStore(t *testing.T, fill func(s *Store)) string {
	t.Helper()
	dir := storeDir(t)
	s := openStore(t, dir)
	if fill != nil {
		fill(s)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := openRaw(t, filepath.Join(dir, File))
	for _, stmt := range []string{
		"DROP TABLE launch_files",
		"DROP TABLE launches",
		"DROP TABLE plans",
		"ALTER TABLE links DROP COLUMN verified",
		"ALTER TABLE links DROP COLUMN candidate",
		"ALTER TABLE links DROP COLUMN fingerprint",
		"UPDATE changes SET value = json_remove(value, '$.fingerprint') WHERE entity = 'ticket' AND field = 'links' AND op = 'add'",
		"UPDATE schema_version SET version = 1",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	v1 := openRaw(t, filepath.Join(t.TempDir(), "v1.sqlite"))
	for _, stmt := range schemaVersion1 {
		if _, err := v1.Exec(stmt); err != nil {
			t.Fatalf("the version 1 schema: %v", err)
		}
	}
	if got, want := schemaShape(t, db), schemaShape(t, v1); !slices.Equal(got, want) {
		t.Fatalf("the fixture is not version 1's schema:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// tableRows reads every row of the table, the given columns of each in the
// order of its rowid, as text.
func tableRows(t *testing.T, db *sql.DB, table string, cols []string) []string {
	t.Helper()
	r, err := db.Query("SELECT " + strings.Join(cols, ", ") + " FROM " + table + " ORDER BY rowid")
	if err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	defer r.Close()
	var out []string
	for r.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := r.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%#v", vals))
	}
	return out
}

// columnsOf is the table's columns, in their order.
func columnsOf(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	r, err := db.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []string
	for r.Next() {
		var c string
		r.Scan(&c)
		out = append(out, c)
	}
	return out
}

func TestSchemaVersion2(t *testing.T) {
	if Version != 2 {
		t.Fatalf("Version is %d", Version)
	}
	s := openStore(t, storeDir(t))
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("version %d %v", version, err)
	}
	for table, want := range map[string][]string{
		"plans": {"id", "ticket_id", "based_on", "crew_id", "crew_name", "crew_updated_at", "base", "body", "digest", "bytes", "state",
			"approved_at", "approved_by", "approved_note", "approved_rev", "approved_intent", "withdrawn_at", "withdrawn_by", "created_at",
			"created_by", "fingerprint", "rev"},
		"launches": {"id", "ticket_id", "plan_id", "run_id", "label", "ticket_rev", "intent", "criteria", "package", "package_sha256", "state",
			"why", "agents", "created_at", "created_by", "fingerprint", "started_at", "rev"},
		"launch_files": {"launch_id", "sha256"},
		"links":        {"id", "ticket_id", "kind", "ref", "url", "note", "created_at", "created_by", "deleted_at", "fingerprint", "candidate", "verified"},
	} {
		if got := columnsOf(t, s.db, table); !slices.Equal(got, want) {
			t.Errorf("%s's columns: %v, want %v", table, got, want)
		}
	}
	// The indexes version 2 adds, none unique: a launch's run id is
	// unique by a rule, never by an index that could refuse a
	// replicated row.
	for index, want := range map[string]string{
		"plans_ticket_created":    "plans(ticket_id, created_at)",
		"launches_ticket_created": "launches(ticket_id, created_at)",
		"launches_plan":           "launches(plan_id)",
		"launches_state":          "launches(state)",
		"launches_run":            "launches(run_id)",
		"launch_files_sha256":     "launch_files(sha256)",
	} {
		var table string
		var unique int
		if err := s.db.QueryRow("SELECT m.tbl_name, i.\"unique\" FROM sqlite_master m JOIN pragma_index_list(m.tbl_name) i ON i.name = m.name WHERE m.name = ?", index).Scan(&table, &unique); err != nil {
			t.Errorf("the index %s: %v", index, err)
			continue
		}
		got := table + "(" + strings.Join(texts(t, s, "SELECT name FROM pragma_index_info(?) ORDER BY seqno", index), ", ") + ")"
		if got != want || unique != 0 {
			t.Errorf("the index %s is %s, unique %d; want %s, not unique", index, got, unique, want)
		}
	}
	// The new tables' defaults are their first states.
	if got := texts(t, s, "SELECT dflt_value FROM pragma_table_info('plans') WHERE name = 'state'"); !slices.Equal(got, []string{"'proposed'"}) {
		t.Errorf("a revision's default state: %v", got)
	}
	if got := texts(t, s, "SELECT dflt_value FROM pragma_table_info('launches') WHERE name = 'state'"); !slices.Equal(got, []string{"'pending'"}) {
		t.Errorf("a launch's default state: %v", got)
	}
}

// A version 1 store with projects, tickets, comments, labels, links and
// attachments, brought to version 2 at open: every row kept, the new
// columns empty, the schema read back the one a fresh store makes, the
// unique indexes still the keys never minted twice, and the store
// writing on: a link added now keeps its fingerprint, a revision is
// proposed.
func TestMigratedVersion2EqualsAFreshOne(t *testing.T) {
	ctx := context.Background()
	var key string
	dir := versionOneStore(t, func(s *Store) {
		s.SetRunExists(runs("crew-0123abcd"))
		newProject(t, s, "CND")
		label := newLabel(t, s, "CND", "bug")
		_, key = makeTicket(t, s, NewTicket{Project: "CND", Title: "Fix the <login> loop", Description: "It loops.", Acceptance: "No loop.", Labels: []string{label}})
		newTicket(t, s, "CND", "Another")
		rc, err := s.AddComment(ctx, testOp("c1"), key, "First")
		want(t, rc, err, StatusDone, "")
		mustPut(t, s, "a1", key, "spec.txt", []byte("the spec"))
		rc, err = addLink(s, "l1", key, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/1"})
		want(t, rc, err, StatusDone, "")
		rc, err = addLink(s, "l2", key, NewLink{Kind: LinkRun, Ref: "crew-0123abcd"})
		want(t, rc, err, StatusDone, "")
	})
	raw := openRaw(t, filepath.Join(dir, File))
	before := map[string][]string{}
	v1Columns := map[string][]string{}
	for _, table := range tables {
		// The version 2 tables, the FTS index (whose entries a search
		// below reads) and the version, which the migration bumps.
		if slices.Contains([]string{"plans", "launches", "launch_files", "tickets_fts", "schema_version"}, table) {
			continue
		}
		v1Columns[table] = columnsOf(t, raw, table)
		before[table] = tableRows(t, raw, table, v1Columns[table])
	}
	if len(before["links"]) != 2 || len(before["tickets"]) != 2 || len(before["changes"]) == 0 {
		t.Fatalf("the fixture holds %d links, %d tickets, %d change rows", len(before["links"]), len(before["tickets"]), len(before["changes"]))
	}
	raw.Close()

	s := openStore(t, dir)
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != Version {
		t.Fatalf("version %d %v", version, err)
	}
	for table, cols := range v1Columns {
		if got := tableRows(t, s.db, table, cols); !slices.Equal(got, before[table]) {
			t.Errorf("%s's rows changed in the migration:\n got %v\nwant %v", table, got, before[table])
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM links WHERE fingerprint != '' OR candidate != '' OR verified != ''"); n != 0 {
		t.Fatalf("%d links with a new column set by the migration", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE op = ?", OpMigrate); n != 0 {
		t.Fatalf("%d migrate rows: nothing is derived", n)
	}
	fresh := openStore(t, storeDir(t))
	if got, want := schemaShape(t, s.db), schemaShape(t, fresh.db); !slices.Equal(got, want) {
		t.Fatalf("the migrated schema is not a fresh store's:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	if got := uniqueIndexes(t, s.db); !slices.Equal(got, theUniqueIndexes) {
		t.Fatalf("the migrated store's unique indexes: %v", got)
	}
	// It reads what it kept and writes on.
	v, err := s.Ticket(ctx, key)
	if err != nil || len(v.Links) != 2 || len(v.Comments) != 1 || len(v.Attachments) != 1 || len(v.Approved) != 0 || v.LaunchCount != 0 {
		t.Fatalf("the ticket after the migration: %+v %v", v, err)
	}
	if page, err := s.List(ctx, Query{Project: "CND", Filter: Filter{Q: "login"}}); err != nil || page.Total != 1 {
		t.Fatalf("a search after the migration: %+v %v", page, err)
	}
	rc, err := addLink(s, "l3", key, NewLink{Kind: LinkExternal, URL: "https://example.com/x"})
	want(t, rc, err, StatusDone, "")
	if fp := texts(t, s, "SELECT fingerprint FROM links WHERE id = ?", rc.ID); len(fp) != 1 || !sha256Pattern.MatchString(fp[0]) {
		t.Fatalf("a link added after the migration keeps no fingerprint: %v", fp)
	}
	rc, err = s.ProposePlan(ctx, testOp("p1"), key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	want(t, rc, err, StatusDone, "")
}

// A version 1 database the store cannot migrate, which it opens
// read-only, is read as it was found: a ticket with its links (their
// candidate, receipt and fingerprint empty), no approved revision and no
// launch; the links' own read; a ticket's revisions none and a revision's
// read not_found; a pruned link add's resend too old to compare, and a
// new operation not found. Nothing reads what version 2 adds, which the
// file has not, and the file stays version 1. The migration fails two
// ways: a writer holding the database past the busy timeout, and a file
// the store may not write.
func TestAVersionOneDatabaseItCannotMigrateIsRead(t *testing.T) {
	cases := []struct {
		name string
		// fail makes the store's migration of the file at path fail and
		// gives the words ReadOnly says why with.
		fail func(t *testing.T, path string) string
	}{
		{"a writer holding it past the busy timeout", func(t *testing.T, path string) string {
			conn := rollbackConnAt(t, path)
			if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.ExecContext(context.Background(), "ROLLBACK") })
			old := busyTimeout
			busyTimeout = 50 * time.Millisecond
			t.Cleanup(func() { busyTimeout = old })
			return "another writer holds the database"
		}},
		{"a file it may not write", func(t *testing.T, path string) string {
			if os.Geteuid() == 0 {
				t.Skip("root writes a file whatever its mode")
			}
			if err := os.Chmod(path, 0o400); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(path, 0o600) })
			return "readonly database"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			url := "https://github.com/rocksolidlabs/conductor/pull/1"
			var key string
			dir := versionOneStore(t, func(s *Store) {
				newProject(t, s, "CND")
				_, key = newTicket(t, s, "CND", "Delivered")
				rc, err := addLink(s, "l1", key, NewLink{Kind: LinkDelivery, URL: url})
				want(t, rc, err, StatusDone, "")
				rc, err = addLink(s, "l2", key, NewLink{Kind: LinkExternal, URL: "https://example.com/x"})
				want(t, rc, err, StatusDone, "")
			})
			path := filepath.Join(dir, File)
			// The first add's receipt pruned, as prune drops it.
			raw := openRaw(t, path)
			if _, err := raw.Exec("DELETE FROM ops WHERE id = 'l1'"); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			words := tc.fail(t, path)
			s := openStore(t, dir)
			if ro, why := s.ReadOnly(); !ro || !strings.Contains(why, words) || strings.Contains(why, "nor read") {
				t.Fatalf("read-only %v: %s", ro, why)
			}
			v, err := s.Ticket(ctx, key)
			if err != nil {
				t.Fatalf("the ticket's read: %v", err)
			}
			if len(v.Links) != 2 || v.Approved == nil || len(v.Approved) != 0 || v.LaunchCount != 0 {
				t.Fatalf("the ticket's read: %d links, approved %v, %d launches", len(v.Links), v.Approved, v.LaunchCount)
			}
			for _, l := range v.Links {
				if l.Candidate != "" || l.Verified != nil || l.Fingerprint != "" {
					t.Fatalf("a version 1 link reads %+v", l)
				}
			}
			if links, err := s.Links(ctx, key); err != nil || len(links) != 2 {
				t.Fatalf("the links' read: %+v %v", links, err)
			}
			if plans, err := s.Plans(ctx, key); err != nil || plans == nil || len(plans) != 0 {
				t.Fatalf("the revisions' read: %+v %v", plans, err)
			}
			if _, err := s.Plan(ctx, "0123456789abcdef"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a revision's read: %v", err)
			}
			rc, found := replayed(t, s, testOp("l1"), IntentLinkAdd, LinkAddArgs{Key: key, NewLink: NewLink{Kind: LinkDelivery, URL: url}})
			if !found || rc.Refusal != RefusalOpConflict || rc.Error != wordsTooOld {
				t.Fatalf("a pruned add's resend: %+v %v", rc, found)
			}
			if rc, found := replayed(t, s, testOp("p1"), IntentPlanPropose, PlanProposeArgs{Key: key, PlanRequest: PlanRequest{Crew: "crew"}}); found {
				t.Fatalf("a new proposal: %+v", rc)
			}
			rc, err = s.ProposePlan(ctx, testOp("p1"), key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
			want(t, rc, err, StatusFailed, RefusalReadOnly)
			var version int
			if err := s.db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil || version != 1 {
				t.Fatalf("the file's version: %d %v", version, err)
			}
		})
	}
}
