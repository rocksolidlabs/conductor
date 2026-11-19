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

// The sends still open (schema version 6, issue #794): a table of the
// database's own, derived from the sends' rows, which PendingSends and
// AskedCancels read through.

// fixtureV5 is a copy of testdata/dispatch-v5.sqlite in a store directory
// of the test's: a version 5 database main's version 5 code wrote (the
// version 4 fixture brought to 5 and given three captures and ten sends,
// one in each state a send takes and two of a capture since deleted, its
// pages then compacted at 1 KiB). Its sends' op ids name their states.
func fixtureV5(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dispatch-v5.sqlite"))
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

// fixtureV5Open is the open row each of the fixture's sends gets, by the
// op id that made it: pending, and a cancel open; a send that is neither
// has no row.
var fixtureV5Open = map[string][2]bool{
	"fx-pending":        {true, false},
	"fx-pending-cancel": {true, true},
	"fx-sent-cancel":    {false, true},
	"fx-gone-pending":   {true, false},
}

// openRows is the open sends' table, each send's row by its id: pending,
// and a cancel open.
func openRows(t *testing.T, s *Store) map[string][2]bool {
	t.Helper()
	rows, err := s.db.Query("SELECT o.send_id, o.pending, o.cancel, o.created_at = s.created_at FROM capture_sends_open o JOIN capture_sends s ON s.id = o.send_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][2]bool{}
	for rows.Next() {
		var id string
		var pending, cancel, ordered bool
		if err := rows.Scan(&id, &pending, &cancel, &ordered); err != nil {
			t.Fatal(err)
		}
		if !ordered || !(pending || cancel) {
			t.Errorf("the open row of %s: pending %v, cancel %v, its send's time %v", id, pending, cancel, ordered)
		}
		out[id] = [2]bool{pending, cancel}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM capture_sends_open"); n != len(out) {
		t.Fatalf("%d open rows, %d of them of a send", n, len(out))
	}
	return out
}

// openAgrees fails the test unless the open sends' table is what every
// send's rows make of it (sendState and its cancels), but for the sends
// whose cancel was closed (closed), which hold no cancel.
func openAgrees(t *testing.T, s *Store, closed ...string) {
	t.Helper()
	want := map[string][2]bool{}
	for _, id := range texts(t, s, "SELECT id FROM capture_sends") {
		sv := sendOf(t, s, id)
		pending := sv.State == SendPending
		cancel := len(sv.Cancels) > 0 && (pending || sv.State == SendSent) && !slices.Contains(closed, id)
		if pending || cancel {
			want[id] = [2]bool{pending, cancel}
		}
	}
	got := openRows(t, s)
	for id, w := range want {
		if g, ok := got[id]; !ok || g != w {
			t.Errorf("the send %s: open %v %v, its rows make %v", id, g, ok, w)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("the send %s has an open row its rows do not make: %v", id, got[id])
		}
	}
}

// fixtureSends is the ids of the fixture's sends by the op ids that made
// them.
func fixtureSends(t *testing.T, s *Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range texts(t, s, "SELECT op_id || ' ' || id FROM capture_sends") {
		op, id, _ := strings.Cut(row, " ")
		out[op] = id
	}
	return out
}

// projection is the ids of the sends a read on the database picks by
// sendState alone, the way version 5 picked them, oldest first.
func projection(t *testing.T, s *Store, where string) []string {
	t.Helper()
	return texts(t, s, "SELECT s.id FROM capture_sends s WHERE "+where+" ORDER BY s.created_at, s.id")
}

const (
	pendingByState = "COALESCE((SELECT e.kind FROM capture_send_outcomes e WHERE e.send_id = s.id AND e.kind IN ('sent', 'refused', 'cancelled') ORDER BY e.kind = 'sent' DESC, e.at, e.actor, e.op_id, e.id LIMIT 1), 'pending') = 'pending'"
	sentByState    = "COALESCE((SELECT e.kind FROM capture_send_outcomes e WHERE e.send_id = s.id AND e.kind IN ('sent', 'refused', 'cancelled') ORDER BY e.kind = 'sent' DESC, e.at, e.actor, e.op_id, e.id LIMIT 1), 'pending') = 'sent'"
	cancelAsked    = "s.id IN (SELECT send_id FROM capture_send_cancels)"
)

// A version 5 database main's code wrote, with a send in every state, is
// brought to version 6 at open: the table and its two indexes made, its
// rows the fixture's open sends and only those (a pending send, with a
// cancel asked or not, of a live capture or a deleted one; a sent send
// with a cancel asked, which version 5 recorded no close of), every other
// row of every table as it was, no migrate row, the schema a fresh
// store's; PendingSends and AskedCancels then list what version 5's
// projection picks from the same rows, and writes go on. A binary that
// writes version 5 opens it read-only, refusing every write, and reads it.
func TestMigrationToOpenSendsKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV5(t)
	raw := openRaw(t, filepath.Join(dir, File))
	var version int
	if err := raw.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != openSendsSchema-1 {
		t.Fatalf("the fixture is version %d %v", version, err)
	}
	before, cols := map[string][]string{}, map[string][]string{}
	for _, table := range tables {
		if slices.Contains(version6Tables, table) || slices.Contains(version8Tables, table) || table == "tickets_fts" || table == "schema_version" {
			continue
		}
		cols[table] = columnsOf(t, raw, table)
		before[table] = tableRows(t, raw, table, cols[table])
	}
	if len(before["capture_sends"]) != 10 || len(before["capture_send_cancels"]) != 4 || len(before["captures"]) != 3 {
		t.Fatalf("the fixture: %d sends, %d cancels, %d captures", len(before["capture_sends"]), len(before["capture_send_cancels"]), len(before["captures"]))
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
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE op = ?", OpMigrate); n != 0 {
		t.Fatalf("%d migrate rows", n)
	}
	if got := texts(t, s, "SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'capture_sends_open' AND name NOT LIKE 'sqlite_%' ORDER BY name"); !slices.Equal(got,
		[]string{"capture_sends_open_cancel", "capture_sends_open_pending"}) {
		t.Fatalf("the version 6 indexes: %v", got)
	}
	fresh := openStore(t, storeDir(t))
	if got, want := schemaShape(t, s.db), schemaShape(t, fresh.db); !slices.Equal(got, want) {
		t.Fatalf("the migrated schema is not a fresh store's:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	sends := fixtureSends(t, s)
	wantOpen := map[string][2]bool{}
	for op, row := range fixtureV5Open {
		wantOpen[sends[op]] = row
	}
	if got := openRows(t, s); fmt.Sprint(got) != fmt.Sprint(wantOpen) {
		t.Fatalf("the open sends: %v, want %v", got, wantOpen)
	}
	openAgrees(t, s)
	// The reads list what version 5's projection picks from the rows.
	if got, w := pending(t, s), projection(t, s, pendingByState); !slices.Equal(got, w) || len(got) != 3 {
		t.Fatalf("pending: %v, the projection %v", got, w)
	}
	if got, w := asked(t, s), projection(t, s, cancelAsked+" AND ("+pendingByState+" OR "+sentByState+")"); !slices.Equal(got, w) || len(got) != 2 {
		t.Fatalf("asked: %v, the projection %v", got, w)
	}
	// Writes go on: a pending send taken, a sent send's cancel closed.
	rc, err := s.MarkSent(ctx, testOp("after-mark"), sends["fx-pending"], "br-21")
	want2(t, rc, err)
	if err := s.CloseCancel(ctx, sends["fx-sent-cancel"]); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s); slices.Contains(got, sends["fx-pending"]) || len(got) != 2 {
		t.Fatalf("pending after a send taken: %v", got)
	}
	if got := asked(t, s); !slices.Equal(got, []string{sends["fx-pending-cancel"]}) {
		t.Fatalf("asked after a close: %v", got)
	}
	openAgrees(t, s, sends["fx-sent-cancel"])
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	older, err := openWith(dir, func(s *Store) { s.writes = openSendsSchema - 1 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { older.Close() })
	if ro, why := older.ReadOnly(); !ro || !strings.Contains(why, fmt.Sprintf("version %d, written by a newer Conductor", Version)) {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	rc, err = older.AskCancel(ctx, testOp("older-ask"), sends["fx-pending"])
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if err := older.CloseCancel(ctx, sends["fx-pending-cancel"]); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a close on the older binary: %v", err)
	}
	if p, err := older.PendingSends(ctx, "", MaxCapturePage); err != nil || len(p.Sends) != 2 {
		t.Fatalf("a read of the newer database: %+v %v", p, err)
	}
}

// want2 fails the test unless the receipt is done.
func want2(t *testing.T, rc Receipt, err error) {
	t.Helper()
	want(t, rc, err, StatusDone, "")
}

// A version 5 database the store cannot migrate (another writer holds it
// past the busy timeout) is read as it is: PendingSends and AskedCancels
// pick the sends by sendState from their rows, as version 5 did, the
// table they read through not being there, every page by its cursor.
func TestAVersion5DatabaseItCannotMigrateListsItsOpenSends(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV5(t)
	path := filepath.Join(dir, File)
	reference := openRaw(t, path)
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
	ids := func(where string) []string {
		rows, err := reference.Query("SELECT s.id FROM capture_sends s WHERE " + where + " ORDER BY s.created_at, s.id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var id string
			rows.Scan(&id)
			out = append(out, id)
		}
		return out
	}
	for _, c := range []struct {
		name  string
		read  func(after string, limit int) (SendsPage, error)
		where string
		n     int
	}{
		{"pending", func(after string, limit int) (SendsPage, error) { return s.PendingSends(ctx, after, limit) }, pendingByState, 3},
		{"asked", func(after string, limit int) (SendsPage, error) { return s.AskedCancels(ctx, after, limit) },
			cancelAsked + " AND (" + pendingByState + " OR " + sentByState + ")", 2},
	} {
		w := ids(c.where)
		var got []string
		after := ""
		for range 10 {
			p, err := c.read(after, 1)
			got = append(got, pageIDs(t, p, err)...)
			if after = p.Next; after == "" {
				break
			}
		}
		if !slices.Equal(got, w) || len(got) != c.n {
			t.Errorf("%s on version 5: %v, want %v", c.name, got, w)
		}
	}
}

// Every write that opens or closes a send writes its open row again in
// the same transaction: a send to Brakeman pending, a ticket's never; a
// cancel asked of a pending or a sent send open; taken, refused or
// cancelled each as its state says, the cancel of a refused or cancelled
// send closed with it; a write refused changes nothing. The table is what
// the sends' rows make of it after every step of every state.
func TestTheOpenSendsFollowEveryWrite(t *testing.T) {
	w := newSendWalk(t)
	for _, state := range sendWalkStates {
		for _, op := range sendWalkOps {
			w.step(t, state, op)
			openAgrees(t, w.s)
		}
	}
	if len(openRows(t, w.s)) == 0 {
		t.Fatal("the walk left no send open")
	}
	// An operation refused, or failing at its receipt, leaves the table as
	// its rows were.
	ctx := context.Background()
	sid := w.sendIn(t, "pending")
	before := openRows(t, w.s)
	failed := errors.New("the receipt's write failed")
	w.s.beforeReceipt = func() error { return failed }
	if _, err := w.s.MarkSent(ctx, testOp("lost-mark"), sid, "br-3"); !errors.Is(err, failed) {
		t.Fatalf("a settlement whose receipt fails: %v", err)
	}
	if _, err := w.s.AskCancel(ctx, testOp("lost-ask"), sid); !errors.Is(err, failed) {
		t.Fatalf("a cancel whose receipt fails: %v", err)
	}
	w.s.beforeReceipt = nil
	rc, err := w.s.MarkSent(ctx, testOp("bad-mark"), sid, "not a request")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if got := openRows(t, w.s); fmt.Sprint(got) != fmt.Sprint(before) || !got[sid][0] {
		t.Fatalf("the open sends after writes that failed: %v, before %v", got, before)
	}
	openAgrees(t, w.s)
}

// CloseCancel records a sent send's cancel closed at the desk: AskedCancels
// lists it no more, with no changes row and no receipt written; it is
// idempotent, and closes a refused or cancelled send's cancel as nothing;
// it refuses a pending send (its cancel ends with its outcome), a send
// with no cancel asked and a send that is not there, changing nothing; a
// cancel asked after the close is open again, and closed again; a closed
// store answers ErrClosed.
func TestACancelClosedAtTheDeskIsNoLongerListed(t *testing.T) {
	ctx := context.Background()
	w := newSendWalk(t)
	s := w.s
	taken := w.sendIn(t, "sent by Brakeman, a cancel asked")
	held := w.sendIn(t, "pending, a cancel asked")
	plain := w.sendIn(t, "sent by Brakeman")
	refused := w.sendIn(t, "pending, a cancel asked")
	rc, err := s.MarkRefused(ctx, testOp("refuse-held"), refused, "the queue is full")
	want2(t, rc, err)
	cancelled := w.sendIn(t, "cancelled")
	if got := asked(t, s); !slices.Equal(got, []string{taken, held}) {
		t.Fatalf("asked: %v", got)
	}
	changes, ops := count(t, s, "SELECT COUNT(*) FROM changes"), count(t, s, "SELECT COUNT(*) FROM ops")
	for range 2 {
		if err := s.CloseCancel(ctx, taken); err != nil {
			t.Fatalf("a close: %v", err)
		}
	}
	for _, sid := range []string{refused, cancelled} {
		if err := s.CloseCancel(ctx, sid); err != nil {
			t.Fatalf("a close of a send with nothing under way: %v", err)
		}
	}
	refusal := func(err error) string {
		var ref *Refusal
		if !errors.As(err, &ref) {
			return fmt.Sprint(err)
		}
		return ref.Code
	}
	for sid, code := range map[string]string{held: RefusalStaleRevision, plain: RefusalInvalid, newID(): RefusalNotFound} {
		if got := refusal(s.CloseCancel(ctx, sid)); got != code {
			t.Errorf("a close of %s: %s, want %s", sid, got, code)
		}
	}
	if got := asked(t, s); !slices.Equal(got, []string{held}) {
		t.Fatalf("asked after the close: %v", got)
	}
	if c, o := count(t, s, "SELECT COUNT(*) FROM changes"), count(t, s, "SELECT COUNT(*) FROM ops"); c != changes || o != ops {
		t.Fatalf("a close wrote %d changes rows and %d receipts", c-changes, o-ops)
	}
	if sv := sendOf(t, s, taken); sv.State != SendSent || len(sv.Cancels) != 1 {
		t.Fatalf("the send closed: %+v", sv)
	}
	openAgrees(t, s, taken)
	// A cancel asked again after the close is open again.
	rc, err = s.AskCancel(ctx, testOp("ask-again"), taken)
	want2(t, rc, err)
	if got := asked(t, s); !slices.Equal(got, []string{taken, held}) {
		t.Fatalf("asked after a second cancel: %v", got)
	}
	openAgrees(t, s)
	if err := s.CloseCancel(ctx, taken); err != nil {
		t.Fatal(err)
	}
	openAgrees(t, s, taken)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseCancel(ctx, held); !errors.Is(err, ErrClosed) {
		t.Fatalf("a close on a closed store: %v", err)
	}
}

// recording is a read's queryer (the store's readQueryer) that keeps every
// statement it is given, with its arguments.
type recording struct {
	queryer
	stmts *[]recorded
}

type recorded struct {
	query string
	args  []any
}

func (q recording) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	*q.stmts = append(*q.stmts, recorded{query, args})
	return q.queryer.QueryContext(ctx, query, args...)
}

func (q recording) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	*q.stmts = append(*q.stmts, recorded{query, args})
	return q.queryer.QueryRowContext(ctx, query, args...)
}

// queryPlan is SQLite's plan of the statement, one detail a line.
func queryPlan(t *testing.T, s *Store, st recorded) []string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+st.query, st.args...)
	if err != nil {
		t.Fatalf("%s: %v", st.query, err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan
}

// pageWork is the work of one read of a page: the statements it made and
// SQLite's plan of each. It fails the test unless no statement scans a
// table (a constant row, a subquery's frame, is no table), and the page's own (its first that reads sends) walks the open
// sends' index on the column, in its order (no temporary b-tree), so the
// LIMIT ends the walk: it reads its limit's open rows and one more.
func pageWork(t *testing.T, s *Store, column string, read func() (SendsPage, error)) (SendsPage, []recorded) {
	t.Helper()
	var stmts []recorded
	s.readQueryer = func(q queryer) queryer { return recording{q, &stmts} }
	p, err := read()
	s.readQueryer = nil
	if err != nil {
		t.Fatal(err)
	}
	pageStmt := -1
	for i, st := range stmts {
		plan := queryPlan(t, s, st)
		for _, line := range plan {
			if strings.HasPrefix(line, "SCAN") && line != "SCAN CONSTANT ROW" {
				t.Errorf("a statement of the page scans a table (%s):\n%s\n%s", line, st.query, strings.Join(plan, "\n"))
			}
		}
		if pageStmt < 0 && strings.Contains(st.query, "FROM capture_sends") && strings.Contains(st.query, "LIMIT") {
			pageStmt = i
			if len(plan) == 0 || !strings.HasPrefix(plan[0], "SEARCH o USING COVERING INDEX capture_sends_open_"+column+" ") ||
				strings.Contains(strings.Join(plan, "\n"), "TEMP B-TREE") {
				t.Errorf("the page's statement does not walk the open sends' index on %s in its order:\n%s\n%s", column, st.query, strings.Join(plan, "\n"))
			}
		}
	}
	if pageStmt < 0 {
		t.Fatalf("no page's statement among %d", len(stmts))
	}
	return p, stmts
}

// A page of one of PendingSends and of AskedCancels does work bounded by
// the open sends, not by the settled ones: with five sends open (two
// pending, one pending with a cancel asked, two sent with a cancel asked)
// and fifty settled (ten each sent to a ticket, sent to Brakeman, refused,
// cancelled, and sent with their cancel closed), the page's statement
// walks the open sends' index and reads its one row and the next, every
// other statement reads by a key, no statement scans a table, and the
// read makes as many statements as it did before the fifty were settled;
// the pages walk the open sends whole, oldest first.
func TestAPageOfOneReadsTheOpenSendsAlone(t *testing.T) {
	ctx := context.Background()
	w := newSendWalk(t)
	s := w.s
	var open []string
	for _, state := range []string{"pending", "pending", "pending, a cancel asked", "sent by Brakeman, a cancel asked", "sent by Brakeman, a cancel asked"} {
		open = append(open, w.sendIn(t, state))
	}
	pendingPage := func() (SendsPage, error) { return s.PendingSends(ctx, "", 1) }
	askedPage := func() (SendsPage, error) { return s.AskedCancels(ctx, "", 1) }
	_, pendingBefore := pageWork(t, s, "pending", pendingPage)
	_, askedBefore := pageWork(t, s, "cancel", askedPage)
	// Fifty settled sends, on a second capture past the first's bound.
	n, f := testCapture(false)
	n.Title = "Second"
	moment(&n)
	second := mustCreate(t, s, "second", n, f, nil)
	settled := 0
	for _, state := range []string{"sent to a ticket", "sent by Brakeman", "refused", "cancelled", "sent by Brakeman, a cancel asked"} {
		for i := range 10 {
			if settled == 40 {
				w.capture = second
			}
			sid := w.sendIn(t, state)
			if state == "sent by Brakeman, a cancel asked" {
				if err := s.CloseCancel(ctx, sid); err != nil {
					t.Fatalf("close %d: %v", i, err)
				}
			}
			settled++
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM capture_sends"); n != len(open)+50 || len(openRows(t, s)) != len(open) {
		t.Fatalf("%d sends, %d open", n, len(openRows(t, s)))
	}
	for _, c := range []struct {
		column string
		read   func() (SendsPage, error)
		before []recorded
		first  string
	}{
		{"pending", pendingPage, pendingBefore, open[0]},
		{"cancel", askedPage, askedBefore, open[2]},
	} {
		p, stmts := pageWork(t, s, c.column, c.read)
		if len(p.Sends) != 1 || p.Sends[0].ID != c.first || p.Next != c.first {
			t.Errorf("%s's page of one: %+v", c.column, p)
		}
		if len(stmts) != len(c.before) {
			t.Errorf("%s's page of one made %d statements, %d before the sends were settled", c.column, len(stmts), len(c.before))
		}
	}
	walk := func(read func(after string) (SendsPage, error)) []string {
		var got []string
		after := ""
		for range len(open) + 1 {
			p, err := read(after)
			got = append(got, pageIDs(t, p, err)...)
			if after = p.Next; after == "" {
				break
			}
		}
		return got
	}
	if got := walk(func(after string) (SendsPage, error) { return s.PendingSends(ctx, after, 1) }); !slices.Equal(got, open[:3]) {
		t.Fatalf("pending, page by page: %v, want %v", got, open[:3])
	}
	if got := walk(func(after string) (SendsPage, error) { return s.AskedCancels(ctx, after, 1) }); !slices.Equal(got, open[2:]) {
		t.Fatalf("asked, page by page: %v, want %v", got, open[2:])
	}
	// A page after a cursor walks the index from it.
	pageWork(t, s, "pending", func() (SendsPage, error) { return s.PendingSends(ctx, open[0], 1) })
	pageWork(t, s, "cancel", func() (SendsPage, error) { return s.AskedCancels(ctx, open[2], 1) })
}
