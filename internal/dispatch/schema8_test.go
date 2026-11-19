package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Schema version 8 (docs/plans/sync-seams.md, row OS-1a0): the tables and
// columns rows from other replicas and a project's sharing keep, and the
// view of every revision made before it. Nothing on this version reads or
// writes them but the migration's views.

// fixtureV7 is a copy of testdata/dispatch-v7.sqlite in a store directory
// of the test's: a version 7 database main's version 7 code wrote (the
// version 6 fixture brought to 7, then given on CND-2 an attachment, a
// comment and two more revisions: one of a crew with a working directory,
// setup, a manifest and checks, agents with commands, start and yolo
// arguments and an environment, a manifest file, two references, the
// attachment and the comment handed over, constraints, a decision and a
// note, approved; one of the plain test crew, withdrawn; its pages then
// compacted at 1 KiB).
func fixtureV7(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dispatch-v7.sqlite"))
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

// v7Rows is every row of every table the version 7 fixture holds (but the
// FTS index and the version), by table, read with the columns it has.
func v7Rows(t *testing.T, dir string) (rows, cols map[string][]string) {
	t.Helper()
	raw := openRaw(t, filepath.Join(dir, File))
	defer raw.Close()
	var version int
	if err := raw.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != syncSchema-1 {
		t.Fatalf("the fixture is version %d %v", version, err)
	}
	rows, cols = map[string][]string{}, map[string][]string{}
	for _, table := range tables {
		if slices.Contains(version8Tables, table) || table == "tickets_fts" || table == "schema_version" {
			continue
		}
		cols[table] = columnsOf(t, raw, table)
		rows[table] = tableRows(t, raw, table, cols[table])
	}
	if len(rows["plans"]) != 4 || len(rows["projects"]) == 0 || len(rows["changes"]) == 0 || slices.Contains(cols["plans"], "view_digest") {
		t.Fatalf("the fixture: %d revisions, %d projects, %d change rows, the revisions' columns %v",
			len(rows["plans"]), len(rows["projects"]), len(rows["changes"]), cols["plans"])
	}
	return rows, cols
}

// A version 7 database main's version 7 code wrote is brought to version 8
// at open: every row of every table as it was, the new columns at their
// defaults on every row (no origin, no verdict, nothing recovered, nothing
// remote; no project shared, no block, no range, no window, no registry
// key; every revision's body here), the new tables empty but the views,
// no migrate row, and the schema a fresh store's.
func TestSchema8MigratesAndKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV7(t)
	before, cols := v7Rows(t, dir)
	s, err := OpenOptions(dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != syncSchema || Version != syncSchema {
		t.Fatalf("version %d (this build's %d) %v", version, Version, err)
	}
	for table, c := range cols {
		if got := tableRows(t, s.db, table, c); !slices.Equal(got, before[table]) {
			t.Errorf("%s's rows changed:\n got %v\nwant %v", table, got, before[table])
		}
	}
	for _, check := range []struct {
		table, where string
	}{
		{"changes", "origin != '' OR output != '' OR prior_output != '' OR recovered_by != '' OR remote != 0"},
		{"projects", "shared != '' OR share_state != '' OR share_upto != 0 OR share_reserved_to != 0 OR block_tag != '' OR next_from != 0 OR " +
			"next_to != 0 OR overflow_from != 0 OR overflow_to != 0 OR overflow_next != 0 OR provisional_from != 0 OR provisional_to != 0 OR " +
			"provisional_next != 0 OR registry_key != ''"},
		{"plans", "body_here != 1 OR body = ''"},
	} {
		if n := count(t, s, "SELECT COUNT(*) FROM "+check.table+" WHERE "+check.where); n != 0 {
			t.Errorf("%d rows of %s off their defaults: %s", n, check.table, check.where)
		}
	}
	for _, table := range []string{"replica_tags", "replica_names", "held_rows", "held_bodies"} {
		if n := count(t, s, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Errorf("%d rows in %s", n, table)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE op = ?", OpMigrate); n != 0 {
		t.Fatalf("%d migrate rows", n)
	}
	fresh := openStore(t, storeDir(t))
	if got, want := schemaShape(t, s.db), schemaShape(t, fresh.db); !slices.Equal(got, want) {
		t.Fatalf("the migrated schema is not a fresh store's:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	// The store reads and writes as before: a revision read whole, a
	// ticket made.
	plans, err := s.Plans(ctx, "CND-2")
	if err != nil || len(plans) != 2 {
		t.Fatalf("CND-2's revisions: %+v %v", plans, err)
	}
	if v, err := s.Plan(ctx, plans[0].ID); err != nil || len(v.Body) == 0 {
		t.Fatalf("a revision: %+v %v", v, err)
	}
	newTicket(t, s, "CND", "Made at eight")
}

// fixtureView is the view the migration makes of the fixture's revision
// of crew-two: its base, its tasks with their members' agents, the agents
// by id, the references, the attachment by its name, the comment, the
// constraints, the decision and the note; nothing else of its body.
func fixtureView(digest, attachment, comment string) revisionView {
	return revisionView{
		V: 1, Digest: digest, Base: strings.Repeat("ef", 20),
		Crew: viewCrew{ID: "crew-two", Name: "Crew crew-two", Tasks: []viewTask{
			{ID: "build", Kind: "work", Member: "dev", Agent: "builder", Instructions: "Build the fix", Needs: []string{}},
			{ID: "review", Kind: "review", Member: "rev", Agent: "checker", Instructions: "Review the fix", Needs: []string{"build"}},
		}},
		Agents: []string{"builder", "checker"},
		References: []PlanReference{
			{Path: "docs/login.md", Start: 2, End: 9, Note: "the flow", Object: strings.Repeat("34", 20), Kind: ObjectBlob},
			{Path: "web", Object: strings.Repeat("56", 20), Kind: ObjectTree},
		},
		Attachments: []viewAttachment{{ID: attachment, Name: "trace notes.txt"}},
		Comments:    []PlanComment{{ID: comment, SHA256: sha256Hex("Use the second approach.")}},
		Constraints: "Keep the old route working.",
		Decisions:   []PlanDecision{{ID: "ship", Question: "Ship it on Friday?", Kind: DecisionApproval, Before: []string{"review"}, For: "person:Operator"}},
		Note:        "Second try, with a & b <c>.",
	}
}

// What a revision's body keeps on this machine, every one of which the
// fixture's crew-two revision holds: the crew's working directory, its
// setup's and its manifest's argv, a check's argv, an agent's command,
// start and yolo arguments, environment's name and SHA-256 and
// selection's SHA-256, the manifest file, a member's arguments and prompt.
var bodyOnly = []string{"/work/secret-place", "TOKEN=abc", "install-deps", "make", "./secret/...", "/opt/tools/builder-bin", "checker-bin",
	"--serve", "--session", "{id}", "--allow-everything", "BUILDER_KEY", "conductor.json", "/srv/secret", "--fast", "Member prompt",
	sha256Hex("BUILDER_KEY=1"), sha256Hex("selection of builder"), sha256Hex("selection of checker")}

// The migration makes the view of every revision the store holds from its
// body: each revision named by its view's SHA-256, its view kept under it
// with the body's digest, the view version 1 naming that digest and
// holding what a person reads of the revision and none of what stays on
// the machine; the view of the fixture's full revision is exactly what
// the body gives. A revision whose body cannot be read, or whose view
// would be past its bound, is left without one, and the migration goes on.
func TestMigrationMakesTheViewOfEveryRevision(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV7(t)
	raw := openRaw(t, filepath.Join(dir, File))
	var full, fullDigest, attachment, comment string
	if err := raw.QueryRow("SELECT id, digest FROM plans WHERE crew_id = 'crew-two'").Scan(&full, &fullDigest); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow("SELECT id FROM attachments WHERE name = 'trace notes.txt'").Scan(&attachment); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow("SELECT id FROM comments WHERE body = 'Use the second approach.'").Scan(&comment); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := raw.QueryRow("SELECT body FROM plans WHERE id = ?", full).Scan(&body); err != nil {
		t.Fatal(err)
	}
	for _, w := range bodyOnly {
		if !strings.Contains(body, w) {
			t.Fatalf("the fixture's body lacks %q", w)
		}
	}
	raw.Close()
	s := openStore(t, dir)
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT p.id, p.digest, p.view_digest, v.digest, v.view FROM plans p LEFT JOIN plan_views v ON v.view_digest = p.view_digest ORDER BY p.rowid")
	if err != nil {
		t.Fatal(err)
	}
	type viewed struct {
		id, digest, viewDigest string
		keptDigest             *string
		view                   []byte
	}
	var all []viewed
	for rows.Next() {
		var v viewed
		if err := rows.Scan(&v.id, &v.digest, &v.viewDigest, &v.keptDigest, &v.view); err != nil {
			t.Fatal(err)
		}
		all = append(all, v)
	}
	rows.Close()
	if len(all) != 4 {
		t.Fatalf("%d revisions", len(all))
	}
	for _, v := range all {
		if v.viewDigest == "" || v.keptDigest == nil {
			t.Fatalf("the revision %s has no view: %q", v.id, v.viewDigest)
		}
		if got := sha256Hex(string(v.view)); got != v.viewDigest || *v.keptDigest != v.digest {
			t.Fatalf("the revision %s's view is named %s, its SHA-256 %s, kept with %s for the body %s", v.id, v.viewDigest, got, *v.keptDigest, v.digest)
		}
		dec := json.NewDecoder(strings.NewReader(string(v.view)))
		dec.DisallowUnknownFields()
		var got revisionView
		if err := dec.Decode(&got); err != nil || got.V != 1 || got.Digest != v.digest || len(got.Crew.Tasks) == 0 || len(got.Agents) == 0 {
			t.Fatalf("the revision %s's view %s: %v", v.id, v.view, err)
		}
		for _, w := range bodyOnly {
			if strings.Contains(string(v.view), w) {
				t.Errorf("the revision %s's view holds %q, which stays on this machine: %s", v.id, w, v.view)
			}
		}
		for _, w := range []string{"/work/conductor", "--model", "go\",\"test", "\"command\"", "\"cwd\"", "\"setup\"", "\"argv\"", "\"manifest", "\"startArgs\"", "\"yoloArgs\"", "\"env"} {
			if strings.Contains(string(v.view), w) {
				t.Errorf("the revision %s's view holds %s: %s", v.id, w, v.view)
			}
		}
		if v.id != full {
			continue
		}
		want, err := encodeValue(fixtureView(v.digest, attachment, comment))
		if err != nil {
			t.Fatal(err)
		}
		if string(v.view) != want {
			t.Fatalf("the full revision's view:\n got %s\nwant %s", v.view, want)
		}
	}
	// Revisions of one body share one view: the fixture's four are of two.
	if n, bodies := count(t, s, "SELECT COUNT(*) FROM plan_views"), count(t, s, "SELECT COUNT(DISTINCT digest) FROM plans"); n != bodies || n != 2 {
		t.Fatalf("%d views of the fixture's %d bodies", n, bodies)
	}

	// A revision whose body the view cannot be read from (not JSON, or its
	// crew not an object), and one whose view is past its bound: each left
	// without a view, the others made.
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, path string)
	}{
		{"a body that is not JSON", func(t *testing.T, path string) {
			raw := openRaw(t, path)
			if _, err := raw.Exec("UPDATE plans SET body = 'not a body' WHERE id = ?", full); err != nil {
				t.Fatal(err)
			}
			raw.Close()
		}},
		{"a body whose crew is not one", func(t *testing.T, path string) {
			raw := openRaw(t, path)
			if _, err := raw.Exec("UPDATE plans SET body = '{\"crew\":7}' WHERE id = ?", full); err != nil {
				t.Fatal(err)
			}
			raw.Close()
		}},
		{"a view past its bound", func(t *testing.T, path string) {
			old := maxPlanView
			maxPlanView = len(body) / 2
			t.Cleanup(func() { maxPlanView = old })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixtureV7(t)
			tc.break_(t, filepath.Join(dir, File))
			s := openStore(t, dir)
			if ro, why := s.ReadOnly(); ro {
				t.Fatalf("read-only: %s", why)
			}
			var version int
			if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != syncSchema {
				t.Fatalf("version %d %v", version, err)
			}
			if got := texts(t, s, "SELECT view_digest FROM plans WHERE id = ?", full); len(got) != 1 || got[0] != "" {
				t.Fatalf("the revision's view: %v", got)
			}
			if n := count(t, s, "SELECT COUNT(*) FROM plans WHERE id != ? AND view_digest IN (SELECT view_digest FROM plan_views)", full); n != 3 {
				t.Fatalf("%d of the other three revisions with their view", n)
			}
		})
	}
}

// A version 8 database opens read-only under a binary that writes version
// 7, as every later version does: it says so, refuses a write, and reads
// what it holds (a ticket, its revisions, a revision whole).
func TestVersion8OpensReadOnlyInVersion7(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV7(t)
	s := openStore(t, dir)
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	newTicket(t, s, "CND", "Made at eight")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	older, err := openWith(dir, func(s *Store) { s.writes = syncSchema - 1 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { older.Close() })
	if ro, why := older.ReadOnly(); !ro || !strings.Contains(why, fmt.Sprintf("the database is version %d, written by a newer Conductor; this one writes version %d", syncSchema, syncSchema-1)) {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	rc, err := older.CreateTicket(ctx, testOp("older-create"), NewTicket{Project: "CND", Title: "Made at seven"})
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if tk, err := older.Ticket(ctx, "CND-2"); err != nil || tk.Title != "Second <one> & \"quoted\"" {
		t.Fatalf("a ticket: %+v %v", tk.Ticket, err)
	}
	plans, err := older.Plans(ctx, "CND-2")
	if err != nil || len(plans) != 2 {
		t.Fatalf("the revisions: %+v %v", plans, err)
	}
	for _, p := range plans {
		if v, err := older.Plan(ctx, p.ID); err != nil || len(v.Body) == 0 || v.Digest != p.Digest {
			t.Fatalf("a revision: %+v %v", v, err)
		}
	}
	page, err := older.List(ctx, Query{Project: "CND"})
	if err != nil || !slices.ContainsFunc(page.Tickets, func(tk TicketSummary) bool { return tk.Title == "Made at eight" }) {
		t.Fatalf("the list: %+v %v", page, err)
	}
}
