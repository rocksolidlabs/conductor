package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fixtureV3 is a copy of testdata/dispatch-v3.sqlite in a store directory
// of the test's: a version 3 database the version 3 code wrote (main at
// 175e0be5's store, its pages then compacted at 1 KiB), holding two
// projects (one archived), three tickets (one deleted, one a child that
// blocks its parent), a label, a milestone, an owner, an assignee and an
// acceptance, a comment, an attachment's row, a delivery link with its
// candidate and receipt, an external link, an approved plan revision, a
// launch started, and a review with a finding and its mark.
func fixtureV3(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dispatch-v3.sqlite"))
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

// version4Tables is the tables version 4 makes.
var version4Tables = []string{"link_reads", "ticket_reads", "store_instance", "publications"}

// instancePattern is a store's instance: 32 hex digits.
var instancePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// A version 3 database is brought to version 4 at open: four tables, four
// indexes and a ticket's closed_on made, the instance's one row written,
// every row of every table as it was (closed_on empty), no migrate row, the
// version 4 shape read back from sqlite_master and the schema a fresh
// store's; it writes on (an import); and a binary that writes version 3
// opens it read-only and reads it.
func TestMigrationThreeToFour(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV3(t)
	raw := openRaw(t, filepath.Join(dir, File))
	var version int
	if err := raw.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("the fixture is version %d %v", version, err)
	}
	before := map[string][]string{}
	cols := map[string][]string{}
	for _, table := range tables {
		if slices.Contains(version4Tables, table) || slices.Contains(version5Tables, table) || table == "tickets_fts" || table == "schema_version" {
			continue
		}
		cols[table] = columnsOf(t, raw, table)
		before[table] = tableRows(t, raw, table, cols[table])
	}
	if len(before["tickets"]) != 3 || len(before["reviews"]) != 1 || len(before["finding_marks"]) != 1 || len(before["launches"]) != 1 ||
		len(before["links"]) != 2 || len(before["attachments"]) != 1 || len(before["changes"]) == 0 {
		t.Fatalf("the fixture: %d tickets, %d reviews, %d marks, %d launches, %d links", len(before["tickets"]), len(before["reviews"]),
			len(before["finding_marks"]), len(before["launches"]), len(before["links"]))
	}
	if slices.Contains(cols["tickets"], "closed_on") {
		t.Fatal("the fixture has closed_on")
	}
	raw.Close()
	s, err := OpenOptions(dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	// Brought on to this binary's version, each step in the one open.
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != Version {
		t.Fatalf("version %d %v", version, err)
	}
	for table, c := range cols {
		if got := tableRows(t, s.db, table, c); !slices.Equal(got, before[table]) {
			t.Errorf("%s's rows changed:\n got %v\nwant %v", table, got, before[table])
		}
	}
	if n := countOf(t, s, "SELECT COUNT(*) FROM tickets WHERE closed_on != ''"); n != 0 {
		t.Fatalf("%d tickets with closed_on", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE op = ?", OpMigrate); n != 0 {
		t.Fatalf("%d migrate rows", n)
	}
	for _, table := range []string{"link_reads", "ticket_reads", "publications"} {
		if n := countOf(t, s, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("%d rows in %s", n, table)
		}
	}
	if ids := texts(t, s, "SELECT id FROM store_instance"); len(ids) != 1 || !instancePattern.MatchString(ids[0]) {
		t.Fatalf("the instance: %v", ids)
	}
	// The version 4 shape, read back from sqlite_master.
	got := texts(t, s, "SELECT type || ' ' || name FROM sqlite_master WHERE name IN ('link_reads', 'ticket_reads', 'store_instance', 'publications', "+
		"'tickets_external', 'link_reads_ticket', 'publications_ticket_created', 'publications_state') ORDER BY name")
	if want := []string{"table link_reads", "index link_reads_ticket", "table publications", "index publications_state", "index publications_ticket_created",
		"table store_instance", "table ticket_reads", "index tickets_external"}; !slices.Equal(got, want) {
		t.Fatalf("the version 4 objects: %v", got)
	}
	if c := columnsOf(t, s.db, "tickets"); c[len(c)-1] != "closed_on" {
		t.Fatalf("the tickets' columns: %v", c)
	}
	fresh := openStore(t, storeDir(t))
	if got, want := schemaShape(t, s.db), schemaShape(t, fresh.db); !slices.Equal(got, want) {
		t.Fatalf("the migrated schema is not a fresh store's:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	if got := uniqueIndexes(t, s.db); !slices.Equal(got, theUniqueIndexes) {
		t.Fatalf("the unique indexes: %v", got)
	}
	// It reads what it kept and writes on.
	v, err := s.Ticket(ctx, "CND-1")
	if err != nil || len(v.Links) != 2 || len(v.Comments) != 1 || len(v.Attachments) != 1 || v.Owners != nil || v.ExternalRead != nil || len(v.LinkReads) != 0 {
		t.Fatalf("a ticket after the migration: %+v %v", v, err)
	}
	if page, err := s.Reviews(ctx, "CND-1", ""); err != nil || len(page.Reviews) != 1 {
		t.Fatalf("the reviews: %+v %v", page, err)
	}
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "Imported", "Body"))
	if key != "CND-4" {
		t.Fatalf("the import's key: %s", key)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A version 4 database under a binary that writes version 3: read-only,
	// read as it is, and left version 4.
	older, err := openWith(dir, func(s *Store) { s.writes = 3 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { older.Close() })
	if ro, why := older.ReadOnly(); !ro || !strings.Contains(why, fmt.Sprintf("version %d, written by a newer Conductor", Version)) {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if v, err := older.Ticket(ctx, key); err != nil || v.Title != "Imported" {
		t.Fatalf("a read: %+v %v", v, err)
	}
	rc, err := older.AddComment(ctx, testOp("c1"), key, "x")
	want(t, rc, err, StatusFailed, RefusalReadOnly)
}

// The tables and indexes version 4 adds, none of the indexes unique: one
// ticket per linked item is a rule of the local write.
func TestSchemaIndexes(t *testing.T) {
	s := openStore(t, storeDir(t))
	for table, want := range map[string][]string{
		"link_reads": {"link_id", "ticket_id", "system", "host", "kind", "node_id", "state", "merged", "merged_at", "merge_sha", "head_sha", "checks",
			"checks_incomplete", "updated_at", "read_at", "issue_etag", "pull_etag", "error", "error_at"},
		"ticket_reads":   {"ticket_id", "external_id", "updated_at", "read_at", "read_by", "issue_etag", "pull_etag", "error", "error_at"},
		"store_instance": {"id"},
		"publications": {"id", "ticket_id", "target", "target_kind", "target_url", "target_ref", "target_node_id", "target_space_id", "instance", "marker",
			"body", "post_sha256", "parts", "close_issue", "state", "tries", "attempt", "find_since", "sent_as_id", "sent_as_login", "remote_id",
			"remote_url", "close_state", "close_tries", "close_error", "error", "sending_at", "sent_at", "sent_by", "created_at", "created_by",
			"fingerprint", "rev"},
	} {
		if got := columnsOf(t, s.db, table); !slices.Equal(got, want) {
			t.Errorf("%s's columns: %v, want %v", table, got, want)
		}
	}
	for index, want := range map[string]string{
		"tickets_external":            "tickets(external_id)",
		"link_reads_ticket":           "link_reads(ticket_id)",
		"publications_ticket_created": "publications(ticket_id, created_at)",
		"publications_state":          "publications(state)",
		// The earlier versions' indexes, kept.
		"plans_ticket_created":   "plans(ticket_id, created_at)",
		"reviews_ticket_created": "reviews(ticket_id, created_at)",
		"tickets_list":           "tickets(project_id, deleted_at, status, priority, horizon, owner, milestone_id, parent_id, updated_at, created_at, number, id)",
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
	if got := texts(t, s, "SELECT dflt_value FROM pragma_table_info('publications') WHERE name = 'state'"); !slices.Equal(got, []string{"'draft'"}) {
		t.Errorf("a publication's default state: %v", got)
	}
}

// Reissue writes a new instance in a copy no server has open, nothing else
// of it touched; a database before version 4 is left as it is; a path
// that is no regular file is refused.
func TestReissue(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	first, err := s.Instance(ctx)
	if err != nil || !instancePattern.MatchString(first) {
		t.Fatalf("the instance: %q %v", first, err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.sqlite")
	if err := s.Snapshot(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	raw := openRaw(t, copyPath)
	before := map[string][]string{}
	for _, table := range []string{"projects", "changes", "ops"} {
		before[table] = tableRows(t, raw, table, columnsOf(t, raw, table))
	}
	raw.Close()
	if err := Reissue(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	raw = openRaw(t, copyPath)
	var id, mode string
	if err := raw.QueryRow("SELECT id FROM store_instance").Scan(&id); err != nil || id == first || !instancePattern.MatchString(id) {
		t.Fatalf("the reissued instance %q (was %q): %v", id, first, err)
	}
	for table, rows := range before {
		if got := tableRows(t, raw, table, columnsOf(t, raw, table)); !slices.Equal(got, rows) {
			t.Fatalf("%s changed", table)
		}
	}
	if err := raw.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode == "wal" {
		t.Fatalf("the copy's journal mode: %s %v", mode, err)
	}
	raw.Close()
	if again, _ := s.Instance(ctx); again != first {
		t.Fatalf("the store's own instance changed: %s", again)
	}
	v3 := filepath.Join(fixtureV3(t), File)
	if err := Reissue(ctx, v3); err != nil {
		t.Fatal(err)
	}
	if n := len(texts(t, &Store{db: openRaw(t, v3)}, "SELECT name FROM sqlite_master WHERE name = 'store_instance'")); n != 0 {
		t.Fatalf("a version 3 database got an instance")
	}
	link := filepath.Join(t.TempDir(), "link.sqlite")
	if err := os.Symlink(copyPath, link); err == nil {
		if err := Reissue(ctx, link); err == nil {
			t.Fatal("a symbolic link was reissued")
		}
	}
	if err := Reissue(ctx, filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("a missing file was reissued")
	}
}
