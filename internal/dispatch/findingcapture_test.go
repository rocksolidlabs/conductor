package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A finding's capture (schema version 7, issue #798): the store reads and
// writes a finding of kind capture, its capture the server's evidence.

// fixtureV6 is a copy of testdata/dispatch-v6.sqlite in a store directory
// of the test's: a version 6 database this item's version 6 code wrote
// (the version 5 fixture brought to 6, a sent send's cancel closed, and a
// review of a launched run's candidate with a finding of each of the four
// kinds version 6 takes, its pages then compacted at 1 KiB).
func fixtureV6(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dispatch-v6.sqlite"))
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

// captureNote is a capture finding asking for a change on the send's
// annotation k, and captureOn the evidence that binds each finding's
// capture as its request names it.
func captureNote(capture, send string, k int) NewFinding {
	return NewFinding{Kind: FindingKindCapture, Intent: FindingIntentChange, Severity: "major", Capture: &CaptureRef{Capture: capture, Send: send, K: k}}
}

func captureOn(r NewReview, refs map[int]*CaptureRef) ReviewEvidence {
	ev := evidenceOf(r)
	for k, ref := range refs {
		ev.Findings[k-1].Capture = ref
	}
	return ev
}

// boundOf is the evidence the server binds to r: each capture finding's
// capture as its request names it.
func boundOf(r NewReview) ReviewEvidence {
	refs := map[int]*CaptureRef{}
	for i, f := range r.Findings {
		if f.Capture != nil {
			ref := *f.Capture
			refs[i+1] = &ref
		}
	}
	return captureOn(r, refs)
}

// A version 6 database this code's version 6 wrote is brought to version 7
// at open: the column added, empty on every finding, every row of every
// table as it was (its open sends and their close among them), no migrate
// row, the schema a fresh store's; its findings read as they did, none
// naming a capture; a binary that writes version 6 opens it read-only and
// reads it.
func TestMigrationToFindingCapturesKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV6(t)
	raw := openRaw(t, filepath.Join(dir, File))
	var version int
	if err := raw.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != findingCaptureSchema-1 {
		t.Fatalf("the fixture is version %d %v", version, err)
	}
	before, cols := map[string][]string{}, map[string][]string{}
	for _, table := range tables {
		if table == "tickets_fts" || table == "schema_version" {
			continue
		}
		cols[table] = columnsOf(t, raw, table)
		before[table] = tableRows(t, raw, table, cols[table])
	}
	if len(before["review_findings"]) != 5 || len(before["capture_sends_open"]) != 3 || slices.Contains(cols["review_findings"], "capture_ref") {
		t.Fatalf("the fixture: %d findings, %d open sends, the columns %v", len(before["review_findings"]), len(before["capture_sends_open"]), cols["review_findings"])
	}
	var key string
	if err := raw.QueryRow("SELECT t.key FROM tickets t JOIN reviews r ON r.ticket_id = t.id WHERE t.title = 'Reviewed at six'").Scan(&key); err != nil {
		t.Fatal(err)
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
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != Version {
		t.Fatalf("version %d %v", version, err)
	}
	for table, c := range cols {
		if got := tableRows(t, s.db, table, c); !slices.Equal(got, before[table]) {
			t.Errorf("%s's rows changed", table)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM review_findings WHERE capture_ref != ''"); n != 0 {
		t.Fatalf("%d findings given a capture by the migration", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE op = ?", OpMigrate); n != 0 {
		t.Fatalf("%d migrate rows", n)
	}
	fresh := openStore(t, storeDir(t))
	if got, want := schemaShape(t, s.db), schemaShape(t, fresh.db); !slices.Equal(got, want) {
		t.Fatalf("the migrated schema is not a fresh store's:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	page, err := s.Reviews(ctx, key, "")
	if err != nil || len(page.Reviews) != 1 {
		t.Fatalf("the reviews: %+v %v", page, err)
	}
	v := reviewOf(t, s, page.Reviews[0].ID)
	var kinds []string
	for _, f := range v.Findings {
		if f.Capture != nil {
			t.Fatalf("a finding read with a capture: %+v", f)
		}
		kinds = append(kinds, f.Kind)
	}
	if !slices.Equal(kinds, []string{FindingKindLine, FindingKindFile, FindingKindGeneral, FindingKindCheck}) || v.Findings[3].Check == nil {
		t.Fatalf("the findings: %v", kinds)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	older, err := openWith(dir, func(s *Store) { s.writes = findingCaptureSchema - 1 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { older.Close() })
	if ro, why := older.ReadOnly(); !ro || !strings.Contains(why, fmt.Sprintf("version %d, written by a newer Conductor", findingCaptureSchema)) {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	rc, _, err := older.MarkFinding(ctx, testOp("older-mark"), v.Findings[0].ID, NewMark{Candidate: v.Candidate, Verdict: FindingWithdrawn, Via: ViaWeb}, nil)
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if got, err := older.Review(ctx, v.ID); err != nil || len(got.Findings) != 4 {
		t.Fatalf("a read of the newer database: %+v %v", got, err)
	}
}

// A version 6 database the store cannot migrate (another writer holds it
// past the busy timeout) is read as it is: its reviews' findings, every
// read of them, without the column version 7 adds.
func TestAVersion6DatabaseItCannotMigrateReadsItsFindings(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV6(t)
	path := filepath.Join(dir, File)
	conn := rollbackConnAt(t, path)
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.ExecContext(context.Background(), "ROLLBACK") })
	old := busyTimeout
	busyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { busyTimeout = old })
	s := openStore(t, dir)
	if ro, why := s.ReadOnly(); !ro || !strings.Contains(why, "another writer holds the database") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	var key string
	for _, k := range []string{"CND-1", "CND-2", "CND-3", "CND-4", "CND-5", "CND-6"} {
		if tk, err := s.Ticket(ctx, k); err == nil && tk.Title == "Reviewed at six" {
			key = k
		}
	}
	if key == "" {
		t.Fatal("no ticket of the fixture's review")
	}
	page, err := s.Reviews(ctx, key, "")
	if err != nil || len(page.Reviews) != 1 {
		t.Fatalf("the reviews: %+v %v", page, err)
	}
	v, err := s.Review(ctx, page.Reviews[0].ID)
	if err != nil || len(v.Findings) != 4 {
		t.Fatalf("the review: %+v %v", v, err)
	}
	f, _, err := s.Finding(ctx, v.Findings[2].ID)
	if err != nil || f.Kind != FindingKindGeneral || f.Capture != nil {
		t.Fatalf("a finding: %+v %v", f, err)
	}
	if fs, err := s.Findings(ctx, key, FindingFilter{}, ""); err != nil || len(fs.Findings) != 4 {
		t.Fatalf("the ticket's findings: %+v %v", fs, err)
	}
}

// The store keeps a finding of kind capture with its capture as the
// server bound it: the capture, its send and the annotation's place, in
// the finding's row and its create row, read back by every read of it;
// the evidence is not the fingerprint, so a resend with other evidence is
// the same review. Evidence that does not answer the request (a capture
// finding without one, a capture on another kind, ids not of the store's
// form, an annotation's place out of bounds, a capture other than the one
// the request names) is the server's fault, an error and no receipt.
func TestACaptureFindingIsKeptWithItsCapture(t *testing.T) {
	ctx := context.Background()
	s, key, run := reviewTicket(t)
	capture, sent, candidate := reviewedCapture(t, s, run)
	other, _, _ := reviewedCapture(t, s, run)
	r := request(candidate, captureNote(capture, sent, 1), lineChange("Return the users."))
	ref := &CaptureRef{Capture: capture, Send: sent, K: 1}
	rc, v, err := s.SubmitReview(ctx, testOp("rv1"), key, r, boundOf(r))
	want(t, rc, err, StatusDone, "")
	if got := v.Findings[0]; got.Kind != FindingKindCapture || got.Capture == nil || *got.Capture != *ref || got.AnchorCommit != "" ||
		!strings.HasPrefix(got.Message, "Save is covered by the overlay.") {
		t.Fatalf("the capture finding: %+v", got)
	}
	if v.Findings[1].Capture != nil {
		t.Fatalf("a line finding read with a capture: %+v", v.Findings[1])
	}
	if got := texts(t, s, "SELECT capture_ref FROM review_findings WHERE id = ?", v.Findings[0].ID); len(got) != 1 ||
		got[0] != fmt.Sprintf(`{"capture":%q,"send":%q,"k":1}`, capture, sent) {
		t.Fatalf("the row's capture_ref: %v", got)
	}
	if got := texts(t, s, "SELECT value FROM changes WHERE entity = ? AND entity_id = ? AND op = ?", EntityFinding, v.Findings[0].ID, OpCreate); len(got) != 1 ||
		!strings.Contains(got[0], fmt.Sprintf(`"capture":{"capture":%q,"send":%q,"k":1}`, capture, sent)) {
		t.Fatalf("the create row: %v", got)
	}
	if got, _, err := s.Finding(ctx, v.Findings[0].ID); err != nil || got.Capture == nil || *got.Capture != *ref {
		t.Fatalf("the finding's read: %+v %v", got, err)
	}
	if page, err := s.Findings(ctx, key, FindingFilter{}, ""); err != nil || len(page.Findings) != 2 || page.Findings[0].Capture == nil {
		t.Fatalf("the ticket's findings: %+v %v", page, err)
	}
	// The evidence is not the fingerprint: the same request is the same
	// review, whatever capture the server binds.
	again, _, err := s.SubmitReview(ctx, testOp("rv1"), key, r, captureOn(r, map[int]*CaptureRef{1: {Capture: other, Send: sent, K: 2}}))
	if err != nil || !again.Repeat || again.ID != v.ID {
		t.Fatalf("a resend: %+v %v", again, err)
	}
	reviews := count(t, s, "SELECT COUNT(*) FROM reviews")
	// The server's evidence that does not answer the request.
	for i, c := range []struct {
		name string
		r    NewReview
		refs map[int]*CaptureRef
	}{
		{"none", request(candidate, captureNote(capture, sent, 1)), nil},
		{"on another kind", request(candidate, lineChange("x")), map[int]*CaptureRef{1: ref}},
		{"an id of no form", request(candidate, captureNote(capture, sent, 1)), map[int]*CaptureRef{1: {Capture: "c1", Send: sent, K: 1}}},
		{"annotation 0", request(candidate, captureNote(capture, sent, 1)), map[int]*CaptureRef{1: {Capture: capture, Send: sent, K: 0}}},
		{"past the annotations", request(candidate, captureNote(capture, sent, 1)), map[int]*CaptureRef{1: {Capture: capture, Send: sent, K: MaxCaptureAnnotations + 1}}},
		{"another than asked", request(candidate, captureNote(capture, sent, 1)), map[int]*CaptureRef{1: {Capture: other, Send: sent, K: 1}}},
	} {
		op := opID("evidence-", i)
		rc, _, err := s.SubmitReview(ctx, testOp(op), key, c.r, captureOn(c.r, c.refs))
		if err == nil || !strings.Contains(err.Error(), "the review's evidence") {
			t.Errorf("%s: %+v %v", c.name, rc, err)
		}
		var ref *Refusal
		if errors.As(err, &ref) {
			t.Errorf("%s: a refusal: %v", c.name, err)
		}
		if _, found, _ := s.Receipt(ctx, "test", op); found {
			t.Errorf("%s: a receipt kept", c.name)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM reviews"); n != reviews {
		t.Fatalf("%d reviews kept of the refused", n-reviews)
	}
}
