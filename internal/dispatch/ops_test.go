package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestARepeatAnswersTheReceiptAndOtherContentConflicts(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	first, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, first, err, StatusDone, "")
	if first.ID != "Alice" || first.Repeat || first.Rev != 0 || first.Intent != "person.add" || first.OpID != "a1" {
		t.Fatalf("the first receipt: %+v", first)
	}
	again, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, again, err, StatusDone, "")
	if !again.Repeat || again.ID != "Alice" {
		t.Fatalf("the repeat: %+v", again)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM people"); n != 1 {
		t.Fatalf("%d people after a repeat", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes"); n != 1 {
		t.Fatalf("%d change rows after a repeat", n)
	}
	// Other content under the id: a conflict, answered without a row.
	conflict, err := s.AddPerson(ctx, testOp("a1"), "Bob")
	want(t, conflict, err, StatusFailed, RefusalOpConflict)
	if conflict.Repeat || conflict.Error == "" {
		t.Fatalf("the conflict: %+v", conflict)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops"); n != 1 {
		t.Fatalf("%d receipts after a conflict", n)
	}
	if rc, found, err := s.Receipt(ctx, "test", "a1"); !found || err != nil || rc.Status != StatusDone || rc.ID != "Alice" {
		t.Fatalf("the receipt kept: %+v %v %v", rc, found, err)
	}
	// Another actor's same id is another operation.
	other, err := s.AddPerson(ctx, Op{Actor: "agent:x", ID: "a1"}, "Bob")
	want(t, other, err, StatusDone, "")
	// A receipt never given is not found.
	if _, found, err := s.Receipt(ctx, "test", "never"); found || err != nil {
		t.Fatalf("a receipt never given: %v %v", found, err)
	}
}

func TestAStaleRevisionIsRefusedAndKept(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	pid := newProject(t, s, "CND")
	// stale sends op, whose expected revision is not the project's, twice:
	// each answer is a stale_revision refusal naming both revisions and
	// kept as a failed receipt, the second a repeat that does not try
	// again, and the project's name and rev and the changes log stay as
	// they were.
	stale := func(op Op, name string, rev int64) {
		t.Helper()
		logged := count(t, s, "SELECT COUNT(*) FROM changes")
		for _, repeat := range []bool{false, true} {
			rc, err := setFields(s, op, EntityProject, pid, field{"name", "Stale"})
			want(t, rc, err, StatusFailed, RefusalStaleRevision)
			if rc.Repeat != repeat {
				t.Fatalf("the refusal of %s, a repeat %v: %+v", op.ID, repeat, rc)
			}
			if !strings.Contains(rc.Error, strconv.FormatInt(op.ExpectedRev, 10)) || !strings.Contains(rc.Error, strconv.FormatInt(rev, 10)) {
				t.Fatalf("the refusal's words: %q", rc.Error)
			}
			if kept, found, err := s.Receipt(ctx, "test", op.ID); !found || err != nil || kept.Status != StatusFailed || kept.Refusal != RefusalStaleRevision {
				t.Fatalf("the failed receipt: %+v %v %v", kept, found, err)
			}
			var gotName string
			var gotRev int64
			if err := s.db.QueryRow("SELECT name, rev FROM projects WHERE id = ?", pid).Scan(&gotName, &gotRev); err != nil || gotName != name || gotRev != rev {
				t.Fatalf("after the refusal of %s: %q rev %d %v", op.ID, gotName, gotRev, err)
			}
			if rows := changesOf(t, s, "test", op.ID); len(rows) != 0 {
				t.Fatalf("change rows of a refused operation: %+v", rows)
			}
			if n := count(t, s, "SELECT COUNT(*) FROM changes"); n != logged {
				t.Fatalf("%d change rows after the refusal of %s, %d before", n, op.ID, logged)
			}
		}
	}
	// A revision ahead of the project's.
	op := testOp("e1")
	op.ExpectedRev = 5
	stale(op, "CND", 1)
	// The revision as it is: allowed, and the receipt carries the new one.
	op = testOp("e2")
	op.ExpectedRev = 1
	rc, err := setFields(s, op, EntityProject, pid, field{"name", "Conductor"})
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 2 {
		t.Fatalf("the receipt's rev: %+v", rc)
	}
	// A revision behind the project's: a writer that read revision 1
	// before e2 made it 2 is refused, and e2's name stands.
	op = testOp("e3")
	op.ExpectedRev = 1
	stale(op, "Conductor", 2)
	// No revision expected: allowed whatever it is.
	rc, err = setFields(s, testOp("e4"), EntityProject, pid, field{"name", "Conductor 2"})
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 3 {
		t.Fatalf("the receipt's rev: %+v", rc)
	}
}

func TestARefusedChangeLeavesAFailedReceiptAndNoChange(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	rc, err := s.AddPerson(ctx, testOp("a1"), strings.Repeat("a", MaxPersonName+1))
	want(t, rc, err, StatusFailed, RefusalBound)
	if kept, found, err := s.Receipt(ctx, "test", "a1"); !found || err != nil || kept.Status != StatusFailed || kept.Refusal != RefusalBound || kept.Error != rc.Error {
		t.Fatalf("the failed receipt: %+v %v %v", kept, found, err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM people"); n != 0 {
		t.Fatalf("%d people after a refusal", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes"); n != 0 {
		t.Fatalf("%d change rows after a refusal", n)
	}
	// A refusal found after the change wrote rows keeps none of them.
	pid := newProject(t, s, "CND")
	_, err = s.Apply(ctx, testOp("late"), "test.late", nil, func(x *tx) error {
		if err := x.record(ChangeRow{Entity: EntityProject, EntityID: pid, ProjectID: pid, Field: "name", Op: OpSet, Value: "x"}); err != nil {
			return err
		}
		return refuse(RefusalInvalid, "found late")
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows := changesOf(t, s, "test", "late"); len(rows) != 0 {
		t.Fatalf("rows kept of a change refused late: %+v", rows)
	}
	var rev int64
	if err := s.db.QueryRow("SELECT rev FROM projects WHERE id = ?", pid).Scan(&rev); err != nil || rev != 1 {
		t.Fatalf("rev after a late refusal: %d %v", rev, err)
	}
}

func TestACommitMadeToFailLeavesNeither(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	s.beforeReceipt = func() error { return errors.New("the disk is full") }
	_, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	if err == nil || !strings.Contains(err.Error(), "disk is full") {
		t.Fatalf("a commit made to fail: %v", err)
	}
	// A refusal's receipt is written in the same transaction: a commit
	// made to fail keeps it neither.
	_, err = s.AddPerson(ctx, testOp("r1"), strings.Repeat("a", MaxPersonName+1))
	if err == nil || !strings.Contains(err.Error(), "disk is full") {
		t.Fatalf("a refusal's commit made to fail: %v", err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM people"); n != 0 {
		t.Fatalf("%d people", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes"); n != 0 {
		t.Fatalf("%d change rows", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops"); n != 0 {
		t.Fatalf("%d receipts", n)
	}
	// Nothing was written, so the same id runs once the store can
	// commit.
	s.beforeReceipt = nil
	rc, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
	if rc.Repeat {
		t.Fatalf("a repeat of an operation never written: %+v", rc)
	}
}

// A refusal's receipt is committed in the transaction that ran the change,
// the change undone under a savepoint, so the write lock is held from the
// change to the receipt: a second store on the same file that sends the
// same operation in between waits and is refused busy, never running the
// operation itself and racing the first's receipt (a UNIQUE error for one
// of them); once the first has committed, it finds the receipt.
func TestARefusalKeepsTheWriteLockUntilItsReceiptIsCommitted(t *testing.T) {
	dir := storeDir(t)
	ctx := context.Background()
	first := openStore(t, dir)
	old := busyTimeout
	busyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { busyTimeout = old })
	second := openStore(t, dir)
	long := strings.Repeat("a", MaxPersonName+1)
	var during Receipt
	var duringErr error
	first.beforeReceipt = func() error {
		first.beforeReceipt = nil
		during, duringErr = second.AddPerson(ctx, testOp("a1"), long)
		return nil
	}
	rc, err := first.AddPerson(ctx, testOp("a1"), long)
	want(t, rc, err, StatusFailed, RefusalBound)
	if !isBusy(duringErr) {
		t.Fatalf("the second store, between the refusal and its receipt: %+v %v", during, duringErr)
	}
	again, err := second.AddPerson(ctx, testOp("a1"), long)
	want(t, again, err, StatusFailed, RefusalBound)
	if !again.Repeat || again.Error != rc.Error {
		t.Fatalf("the second store's answer after the commit: %+v", again)
	}
	if conflict, err := second.AddPerson(ctx, testOp("a1"), "Alice"); err != nil || conflict.Refusal != RefusalOpConflict {
		t.Fatalf("other content after the commit: %+v %v", conflict, err)
	}
	if n := count(t, first, "SELECT COUNT(*) FROM ops"); n != 1 {
		t.Fatalf("%d receipts", n)
	}
}

// A change that records no change row is refused invalid, and what it
// wrote is undone: every done receipt has its row (actor, op_id, 0) in the
// log, so no success is taken for a refusal and run again once its receipt
// is pruned.
func TestAChangeThatRecordsNoRowIsRefused(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	calls := 0
	silent := func(x *tx) error {
		calls++
		_, err := x.tx.ExecContext(x.ctx, "INSERT INTO people (name, created_at) VALUES ('Ghost', ?)", stamp(x.now))
		return err
	}
	rc, err := s.Apply(ctx, testOp("n1"), "test.silent", nil, silent)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if !strings.Contains(rc.Error, "changes nothing") {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM people"); n != 0 {
		t.Fatalf("%d people written without a change row", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes"); n != 0 {
		t.Fatalf("%d change rows", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops WHERE status = 'done'"); n != 0 {
		t.Fatalf("%d done receipts", n)
	}
	again, err := s.Apply(ctx, testOp("n1"), "test.silent", nil, silent)
	want(t, again, err, StatusFailed, RefusalInvalid)
	if !again.Repeat || calls != 1 {
		t.Fatalf("the repeat: %+v after %d calls", again, calls)
	}
	// Its receipt pruned, the resend is a refusal's: it runs anew and is
	// refused again on its merits, never answered as done.
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	again, err = s.Apply(ctx, testOp("n1"), "test.silent", nil, func(x *tx) error { calls++; return nil })
	want(t, again, err, StatusFailed, RefusalInvalid)
	if again.Repeat || calls != 2 {
		t.Fatalf("the resend after pruning: %+v after %d calls", again, calls)
	}
}

func TestADatabaseErrorInTheChangeWritesNothing(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	_, err := s.Apply(ctx, testOp("bad"), "test.bad", nil, func(x *tx) error {
		_, err := x.tx.ExecContext(x.ctx, "INSERT INTO no_such_table (x) VALUES (1)")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("a database error: %v", err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops"); n != 0 {
		t.Fatalf("%d receipts after a database error", n)
	}
	// A cancelled context is told as the context's own error.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.AddPerson(cancelled, testOp("c1"), "Alice"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled operation: %v", err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops"); n != 0 {
		t.Fatalf("%d receipts after a cancelled operation", n)
	}
}

func TestRevIsBumpedOncePerOperationWhateverItSet(t *testing.T) {
	s := openStore(t, storeDir(t))
	pid := newProject(t, s, "CND")
	var rev int64
	var updated string
	if err := s.db.QueryRow("SELECT rev, updated_at FROM projects WHERE id = ?", pid).Scan(&rev, &updated); err != nil || rev != 1 {
		t.Fatalf("rev after the create: %d %v", rev, err)
	}
	rc, err := setFields(s, testOp("e1"), EntityProject, pid, field{"name", "Conductor"}, field{"description", "The app"}, field{"cwd", "/srv/conductor"})
	want(t, rc, err, StatusDone, "")
	var after string
	if err := s.db.QueryRow("SELECT rev, updated_at FROM projects WHERE id = ?", pid).Scan(&rev, &after); err != nil || rev != 2 || rc.Rev != 2 {
		t.Fatalf("rev after three fields set: row %d receipt %d %v", rev, rc.Rev, err)
	}
	if after <= updated {
		t.Fatalf("updated_at did not move: %q then %q", updated, after)
	}
	// A ticket's rev is apart from its project's, and the receipt
	// carries the subject's: the first entity the change named.
	tid, _ := newTicket(t, s, "CND", "First")
	rc, err = setFields(s, testOp("e2"), EntityTicket, tid, field{"title", "Second"})
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 2 {
		t.Fatalf("the ticket's rev: %+v", rc)
	}
	if err := s.db.QueryRow("SELECT rev FROM projects WHERE id = ?", pid).Scan(&rev); err != nil || rev != 2 {
		t.Fatalf("the project's rev moved with the ticket's edit: %d %v", rev, err)
	}
	// A set member's row is its ticket's field: the ticket's rev moves.
	rc, err = s.Apply(context.Background(), testOp("e3"), "ticket.edit", nil, func(x *tx) error {
		if _, err := x.tx.ExecContext(x.ctx, "INSERT INTO ticket_assignees (ticket_id, actor, added_at) VALUES (?, ?, ?)", tid, "person:Alice", stamp(x.now)); err != nil {
			return err
		}
		return x.record(ChangeRow{Entity: EntityTicket, EntityID: tid, TicketID: tid, ProjectID: pid, Field: "assignees", Op: OpAdd, Value: "person:Alice"})
	})
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 3 {
		t.Fatalf("the ticket's rev after a member added: %+v", rc)
	}
	// An operation that names no row with a rev carries none.
	rc, err = s.AddPerson(context.Background(), testOp("p1"), "Alice")
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 0 {
		t.Fatalf("a person's receipt carries a rev: %+v", rc)
	}
}

func TestChangesRowsOnePerFieldWithPriorAndN(t *testing.T) {
	s := openStore(t, storeDir(t))
	pid := newProject(t, s, "CND")
	before := time.Now().UTC().Add(-time.Second)
	rc, err := setFields(s, testOp("e1"), EntityProject, pid, field{"name", "Conductor"}, field{"description", "The app"})
	want(t, rc, err, StatusDone, "")
	rows := changesOf(t, s, "test", "e1")
	if len(rows) != 2 {
		t.Fatalf("%d rows: %+v", len(rows), rows)
	}
	for i, row := range rows {
		if row.N != i || row.Entity != EntityProject || row.EntityID != pid || row.ProjectID != pid || row.TicketID != "" ||
			row.Op != OpSet || row.Actor != "test" || row.By != "Tester" || row.OpID != "e1" || row.At.Before(before) || row.Seq == 0 {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	if rows[0].Field != "name" || rows[0].Value != "Conductor" || rows[0].Prior != "CND" {
		t.Fatalf("the name's row: %+v", rows[0])
	}
	if rows[1].Field != "description" || rows[1].Value != "The app" || rows[1].Prior != "" {
		t.Fatalf("the description's row: %+v", rows[1])
	}
	if !rows[0].At.Equal(rows[1].At) || rows[1].Seq != rows[0].Seq+1 {
		t.Fatalf("one time and consecutive seqs: %+v", rows)
	}
	// The stamp as written orders as text: nine fractional digits always.
	var at string
	if err := s.db.QueryRow("SELECT at FROM changes WHERE seq = ?", rows[0].Seq).Scan(&at); err != nil || len(at) != len(stampLayout) || !strings.HasSuffix(at, "Z") {
		t.Fatalf("the stamp as written: %q %v", at, err)
	}
	// A second set of the same field has the first's value as its prior.
	rc, err = setFields(s, testOp("e2"), EntityProject, pid, field{"name", "Conductor 2"})
	want(t, rc, err, StatusDone, "")
	if rows := changesOf(t, s, "test", "e2"); len(rows) != 1 || rows[0].Prior != "Conductor" || rows[0].Value != "Conductor 2" {
		t.Fatalf("the second set: %+v", rows)
	}
	// A row whose identity the log holds is refused, and nothing of the
	// operation is kept.
	if _, err := s.db.Exec("INSERT INTO changes (entity, entity_id, project_id, field, op, value, at, actor, by, op_id, n) VALUES ('project', ?, ?, 'name', 'set', 'held', ?, 'test', 'Tester', 'held', 1)", pid, pid, stamp(time.Now())); err != nil {
		t.Fatal(err)
	}
	_, err = setFields(s, testOp("held"), EntityProject, pid, field{"name", "A"}, field{"description", "B"})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("a held identity: %v", err)
	}
	if rows := changesOf(t, s, "test", "held"); len(rows) != 1 || rows[0].Value != "held" {
		t.Fatalf("rows of the held id: %+v", rows)
	}
	if _, found, _ := s.Receipt(context.Background(), "test", "held"); found {
		t.Fatal("a receipt was kept for a refused row")
	}
	var name string
	if err := s.db.QueryRow("SELECT name FROM projects WHERE id = ?", pid).Scan(&name); err != nil || name != "Conductor 2" {
		t.Fatalf("the name after the refused row: %q", name)
	}
	// A value past the row's bound is an error, not a row.
	_, err = s.Apply(context.Background(), testOp("big"), "test.big", nil, func(x *tx) error {
		return x.record(ChangeRow{Entity: EntityProject, EntityID: pid, ProjectID: pid, Field: "description", Op: OpSet, Value: strings.Repeat("x", MaxChangeValue+1)})
	})
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("a value past the bound: %v", err)
	}
	// A row naming a row that is not there is an error.
	_, err = s.Apply(context.Background(), testOp("ghost"), "test.ghost", nil, func(x *tx) error {
		return x.record(ChangeRow{Entity: EntityTicket, EntityID: "no-such-ticket", Field: "title", Op: OpSet, Value: "x"})
	})
	if err == nil || !strings.Contains(err.Error(), "no row") {
		t.Fatalf("a row of no entity: %v", err)
	}
}

// A remove's tags go into the fewest priors within MaxChangeValue, in
// their order: none is one empty list; tags whose list is MaxChangeValue
// bytes exactly are one prior, and a byte more two; every prior but the
// last is too full to take the next tag.
func TestTagPriorsCutAtTheRowBound(t *testing.T) {
	if got := tagPriors(nil); len(got) != 1 || got[0] != "[]" {
		t.Fatalf("no tags: %q", got)
	}
	// A tag whose op id is as long as op ids get and whose actor is size
	// bytes.
	tag := func(i, size int) Tag {
		actor := fmt.Sprintf("replica-%04d-", i)
		return Tag{Actor: actor + strings.Repeat("r", size-len(actor)), OpID: fmt.Sprintf("%032d", i)}
	}
	// Tags added until their list is past the bound, then actors cut by a
	// byte, one at a time, until it is the bound exactly; size counts the
	// list's bytes as it goes, and the list as encoded is checked after.
	var tags []Tag
	size := len("[]")
	for i := 0; size <= MaxChangeValue; i++ {
		tg := tag(i, MaxActor)
		if len(tags) > 0 {
			size += len(",")
		}
		size += len(EncodeTags([]Tag{tg})) - len("[]")
		tags = append(tags, tg)
	}
	for i := 0; size > MaxChangeValue; i++ {
		tags[i] = tag(i, MaxActor-1)
		size--
	}
	if n := len(EncodeTags(tags)); n != MaxChangeValue {
		t.Fatalf("the list is %d bytes", n)
	}
	if got := tagPriors(tags); len(got) != 1 || got[0] != EncodeTags(tags) {
		t.Fatalf("at the bound: %d priors", len(got))
	}
	tags[0] = tag(0, MaxActor)
	last := len(tags) - 1
	if got := tagPriors(tags); len(got) != 2 || got[0] != EncodeTags(tags[:last]) || got[1] != EncodeTags(tags[last:]) {
		t.Fatalf("a byte past the bound: %d priors", len(got))
	}
	// Many, of every actor's length: each prior within the bound and every
	// one but the last too full for the next tag, the tags named once each,
	// in order.
	var many []Tag
	for i := range 2000 {
		many = append(many, tag(i, 20+i%(MaxActor-19)))
	}
	priors := tagPriors(many)
	var named []Tag
	for i, p := range priors {
		part := Tags(p)
		if len(p) > MaxChangeValue || len(part) == 0 {
			t.Fatalf("prior %d: %d bytes, %d tags", i, len(p), len(part))
		}
		if i < len(priors)-1 {
			next := len(EncodeTags([]Tag{many[len(named)+len(part)]})) - len("[]")
			if len(p)+len(",")+next <= MaxChangeValue {
				t.Fatalf("prior %d, %d bytes, could take the next tag of %d", i, len(p), next)
			}
		}
		named = append(named, part...)
	}
	if len(priors) < 2 || !slices.Equal(named, many) {
		t.Fatalf("%d priors name %d of the %d tags", len(priors), len(named), len(many))
	}
}

func TestPruneKeepsTheNewestReceipts(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	// 20,001 receipts, one statement: r00000 the oldest, stamped a
	// nanosecond apart.
	if _, err := s.db.ExecContext(ctx, `WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
		INSERT INTO ops (actor, id, intent, fingerprint, status, receipt, created_at)
		SELECT 'test', printf('r%05d', i), 'x', 'f', 'done', '{}', printf('2026-10-28T12:00:00.%09dZ', i) FROM seq`, maxReceipts); err != nil {
		t.Fatal(err)
	}
	n, err := s.prune(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops"); n != maxReceipts {
		t.Fatalf("%d receipts kept", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops WHERE id = 'r00000'"); n != 0 {
		t.Fatal("the oldest receipt was kept")
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops WHERE id = 'r00001' OR id = ?", fmt.Sprintf("r%05d", maxReceipts)); n != 2 {
		t.Fatalf("the newest receipts: %d of 2 kept", n)
	}
	if got := s.pruned.Load(); got != 1 {
		t.Fatalf("the pruned counter: %d", got)
	}
	// Open prunes too: the counter of a store reopened on the directory.
	if _, err := s.db.Exec("INSERT INTO ops (actor, id, intent, fingerprint, status, receipt, created_at) VALUES ('test', 'r-old', 'x', 'f', 'done', '{}', '2026-10-28T11:00:00.000000000Z')"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again := openStore(t, s.dir)
	if got := again.pruned.Load(); got != 1 {
		t.Fatalf("the pruning at open dropped %d", got)
	}
	if n := count(t, again, "SELECT COUNT(*) FROM ops WHERE id = 'r-old'"); n != 0 {
		t.Fatal("the oldest receipt survived the open")
	}
}

func TestAResendAfterPruningIsAnsweredByTheLog(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	rc, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
	if _, err := s.db.Exec("DELETE FROM ops WHERE actor = 'test' AND id = 'a1'"); err != nil {
		t.Fatal(err)
	}
	// The same content: done and a repeat, with no rev, id or key.
	again, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, again, err, StatusDone, "")
	if !again.Repeat || again.Rev != 0 || again.ID != "" || again.Key != "" || again.Intent != "person.add" {
		t.Fatalf("the resend: %+v", again)
	}
	// Other content: the same answer, and nothing changed.
	other, err := s.AddPerson(ctx, testOp("a1"), "Bob")
	want(t, other, err, StatusDone, "")
	if !other.Repeat || other.ID != "" {
		t.Fatalf("the resend with other content: %+v", other)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM people"); n != 1 {
		t.Fatalf("%d people after the resends", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes"); n != 1 {
		t.Fatalf("%d change rows after the resends", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops"); n != 0 {
		t.Fatalf("%d receipts written by the resends", n)
	}
	// A refused operation wrote no change: its resend runs anew, refused
	// again on its own merits, or allowed now.
	rc, err = s.AddPerson(ctx, testOp("a2"), "Alice")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if _, err := s.db.Exec("DELETE FROM ops WHERE actor = 'test' AND id = 'a2'"); err != nil {
		t.Fatal(err)
	}
	rc, err = s.AddPerson(ctx, testOp("a2"), "Alice")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if rc.Repeat {
		t.Fatalf("a refusal resent after pruning was a repeat: %+v", rc)
	}
	if _, found, _ := s.Receipt(ctx, "test", "a2"); !found {
		t.Fatal("the refusal's receipt was not written again")
	}
	if _, err := s.db.Exec("DELETE FROM ops WHERE actor = 'test' AND id = 'a2'"); err != nil {
		t.Fatal(err)
	}
	rc, err = s.AddPerson(ctx, testOp("a2"), "Carol")
	want(t, rc, err, StatusDone, "")
	if rc.Repeat || rc.ID != "Carol" {
		t.Fatalf("the refused id reused for an allowed operation: %+v", rc)
	}
}

// OpTicket names the ticket an operation changed, from the log: after
// its receipt is pruned and after the ticket's delete; an operation that
// changed no ticket, a refusal, and one never given name none.
func TestOpTicketNamesTheTicketAnOperationChanged(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "OPS")
	_, key := newTicket(t, s, "OPS", "Commented")
	rc, err := s.AddComment(ctx, testOp("c1"), key, "seen")
	want(t, rc, err, StatusDone, "")
	rc, err = s.AddComment(ctx, testOp("c2"), key, "")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if _, err := s.db.Exec("DELETE FROM ops WHERE actor = 'test' AND id = 'c1'"); err != nil {
		t.Fatal(err)
	}
	rc, err = s.DeleteTicket(ctx, testOp("d1"), key)
	want(t, rc, err, StatusDone, "")
	for _, c := range []struct {
		actor, id string
		found     bool
	}{
		{"test", "c1", true},
		{"test", "d1", true},
		{"test", "project-OPS", false},
		{"test", "c2", false},
		{"test", "never", false},
		{"agent:x", "c1", false},
	} {
		got, found, err := s.OpTicket(ctx, c.actor, c.id)
		if err != nil || found != c.found || (found && got != key) || (!found && got != "") {
			t.Errorf("OpTicket(%s, %s): %q %v %v", c.actor, c.id, got, found, err)
		}
	}
}

// OnChange's listeners are called once per commit, after it, with what
// it changed: one change per entity, with the commit's last seq, in the
// order the rows named them.
func TestOnChangeIsCalledOncePerCommitAfterIt(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	var calls [][]Change
	var current string
	s.OnChange(func(cs []Change) {
		if _, found, err := s.Receipt(ctx, "test", current); !found || err != nil {
			t.Errorf("called before the commit of %s: %v %v", current, found, err)
		}
		calls = append(calls, cs)
	})
	// call is the changes of the nth call, failing the test unless there
	// have been n.
	call := func(n int) []Change {
		t.Helper()
		if len(calls) != n {
			t.Fatalf("%d calls, want %d: %+v", len(calls), n, calls)
		}
		return calls[n-1]
	}
	current = "a1"
	rc, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
	if got := call(1); !slices.Equal(got, []Change{{Seq: 1, Entity: EntityPerson, ID: "Alice"}}) {
		t.Fatalf("after a person: %+v", got)
	}
	// A project's create writes three rows (its create, its name, its
	// close policy): one change, with the commit's last seq.
	current = "project-CND"
	pid := newProject(t, s, "CND")
	if got := call(2); !slices.Equal(got, []Change{{Seq: 4, Entity: EntityProject, ID: pid, Project: "CND"}}) {
		t.Fatalf("after a project: %+v", got)
	}
	current = "t1"
	rc, err = s.CreateTicket(ctx, testOp("t1"), NewTicket{Project: "CND", Title: "First"})
	want(t, rc, err, StatusDone, "")
	tid := rc.ID
	if got := call(3); !slices.Equal(got, []Change{{Seq: 6, Entity: EntityTicket, ID: tid, Ticket: tid, Project: "CND"}}) {
		t.Fatalf("after a ticket: %+v", got)
	}
	// Two rows of one entity: one change, with the commit's last seq.
	current = "e1"
	rc, err = setFields(s, testOp("e1"), EntityTicket, tid, field{"title", "Second"}, field{"description", "More"})
	want(t, rc, err, StatusDone, "")
	if got := call(4); !slices.Equal(got, []Change{{Seq: 8, Entity: EntityTicket, ID: tid, Ticket: tid, Project: "CND"}}) {
		t.Fatalf("after two fields: %+v", got)
	}
	// Two entities in one operation: one call of two changes, both with
	// the last seq, in the order the rows named them.
	current = "e2"
	rc, err = s.Apply(ctx, testOp("e2"), "test.two", nil, func(x *tx) error {
		if err := x.record(ChangeRow{Entity: EntityProject, EntityID: pid, ProjectID: pid, Field: "name", Op: OpSet, Value: "N", Prior: "CND"}); err != nil {
			return err
		}
		if _, err := x.tx.ExecContext(x.ctx, "UPDATE projects SET name = 'N' WHERE id = ?", pid); err != nil {
			return err
		}
		return x.record(ChangeRow{Entity: EntityTicket, EntityID: tid, TicketID: tid, ProjectID: pid, Field: "title", Op: OpSet, Value: "Third", Prior: "Second"})
	})
	want(t, rc, err, StatusDone, "")
	if got := call(5); !slices.Equal(got, []Change{{Seq: 10, Entity: EntityProject, ID: pid, Project: "CND"}, {Seq: 10, Entity: EntityTicket, ID: tid, Ticket: tid, Project: "CND"}}) {
		t.Fatalf("after two entities: %+v", got)
	}
	// A refusal, a repeat and a conflict change nothing: no call.
	current = "a2"
	rc, err = s.AddPerson(ctx, testOp("a2"), "Alice")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	current = "a1"
	rc, err = s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
	rc, err = s.AddPerson(ctx, testOp("a1"), "Bob")
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if len(calls) != 5 {
		t.Fatalf("a refusal, a repeat or a conflict was announced: %+v", calls[5:])
	}
	// A second listener gets the call too, each a copy of its own: a
	// third, called last, that overwrites its copy changes neither of the
	// copies the first two kept.
	var second [][]Change
	s.OnChange(func(cs []Change) { second = append(second, cs) })
	s.OnChange(func(cs []Change) { clear(cs) })
	current = "a3"
	rc, err = s.AddPerson(ctx, testOp("a3"), "Bob")
	want(t, rc, err, StatusDone, "")
	got := call(6)
	if len(got) != 1 || got[0] != (Change{Seq: got[0].Seq, Entity: EntityPerson, ID: "Bob"}) || got[0].Seq <= 10 ||
		len(second) != 1 || !slices.Equal(second[0], got) {
		t.Fatalf("three listeners: %+v %+v", got, second)
	}
}

func TestABadOperationIsAnError(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	for _, op := range []Op{{Actor: "test", ID: ""}, {Actor: "test", ID: "a b"}, {Actor: "test", ID: strings.Repeat("a", 33)}, {Actor: "test", ID: "a/b"}} {
		if _, err := s.AddPerson(ctx, op, "Alice"); !errors.Is(err, ErrBadOpID) {
			t.Fatalf("op id %q: %v", op.ID, err)
		}
	}
	// The actor is at most MaxActor bytes, with no control character:
	// every change row records it, and every member's tag.
	for _, actor := range []string{"", strings.Repeat("a", MaxActor+1), strings.Repeat("é", MaxActor/2+1), "a\nb", "a\x00b", "\xff"} {
		if _, err := s.AddPerson(ctx, Op{Actor: actor, ID: "a1"}, "Alice"); !errors.Is(err, ErrBadActor) {
			t.Fatalf("actor %q: %v", actor, err)
		}
	}
	// The display name an operation records is a name: at most MaxByName
	// characters, no control character; none at all is the actor alone.
	for _, by := range []string{strings.Repeat("é", MaxByName+1), "a\nb", "a\x00b", "\xff"} {
		if _, err := s.AddPerson(ctx, Op{Actor: "test", ID: "b1", By: by}, "Alice"); !errors.Is(err, ErrBadBy) {
			t.Fatalf("by %q: %v", by, err)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops"); n != 0 {
		t.Fatalf("%d receipts of refused operations", n)
	}
	for i, by := range []string{"", strings.Repeat("é", MaxByName)} {
		rc, err := s.AddPerson(ctx, Op{Actor: "test", ID: opID("by", i), By: by}, opID("Person ", i))
		want(t, rc, err, StatusDone, "")
	}
	for i, actor := range []string{strings.Repeat("a", MaxActor), strings.Repeat("é", MaxActor/2)} {
		rc, err := s.AddPerson(ctx, Op{Actor: actor, ID: opID("actor", i)}, opID("Actor ", i))
		want(t, rc, err, StatusDone, "")
	}
	for _, id := range []string{"a", strings.Repeat("a", 32), "A.b_c~d-e", "01"} {
		rc, err := s.AddPerson(ctx, Op{Actor: "test", ID: id}, "Person "+id)
		want(t, rc, err, StatusDone, "")
	}
	if n := count(t, s, "SELECT COUNT(*) FROM ops"); n != 8 {
		t.Fatalf("%d receipts", n)
	}
}

func TestAReceiptStaysUnderItsBound(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	long := strings.Repeat("ü", 5000)
	rc, err := s.Apply(ctx, testOp("r1"), "test.refuse", nil, func(x *tx) error {
		return &Refusal{Code: RefusalClosePolicy, Words: long, Missing: []string{"acceptance", "delivery"}}
	})
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if len([]rune(rc.Error)) != maxReceiptWords+1 || !strings.HasPrefix(rc.Error, "üüü") || !strings.HasSuffix(rc.Error, "…") {
		t.Fatalf("the words were not cut: %d runes", len([]rune(rc.Error)))
	}
	if len(rc.Missing) != 2 {
		t.Fatalf("missing: %v", rc.Missing)
	}
	var body string
	if err := s.db.QueryRow("SELECT receipt FROM ops WHERE actor = 'test' AND id = 'r1'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) > MaxReceipt {
		t.Fatalf("the receipt is %d bytes", len(body))
	}
	var kept Receipt
	if err := json.Unmarshal([]byte(body), &kept); err != nil || kept.Missing[1] != "delivery" || kept.Repeat {
		t.Fatalf("the receipt as kept: %+v %v", kept, err)
	}
	// The receipt's JSON names its fields as the API will send them.
	if !strings.Contains(body, `"opId":"r1"`) || !strings.Contains(body, `"refusal":"close_policy"`) || !strings.Contains(body, `"missing":["acceptance","delivery"]`) {
		t.Fatalf("the receipt's JSON: %s", body)
	}
	// The words are cut by their size as JSON too, at the longest op id:
	// characters JSON writes as six bytes (<, >, &, U+2028, U+2029, a
	// control character) or that take four in UTF-8 still leave the
	// receipt under its bound, kept and answered again, and cut no shorter
	// than it must be.
	for i, w := range []string{
		strings.Repeat("<", maxReceiptWords),
		strings.Repeat(string(rune(0x2028)), maxReceiptWords),
		strings.Repeat(string(rune(0x1F600)), maxReceiptWords),
		strings.Repeat("&\x01\"\\", maxReceiptWords),
	} {
		op := testOp(strings.Repeat("o", 31) + strconv.Itoa(i))
		rc, err := s.Apply(ctx, op, "test.refuse", nil, func(x *tx) error {
			return &Refusal{Code: RefusalClosePolicy, Words: w, Missing: []string{"acceptance", "delivery"}}
		})
		want(t, rc, err, StatusFailed, RefusalClosePolicy)
		cut := strings.TrimSuffix(rc.Error, "…")
		if cut == rc.Error || !strings.HasPrefix(w, cut) || !utf8.ValidString(rc.Error) {
			t.Fatalf("words %d were not cut at a character: %q", i, rc.Error)
		}
		var body string
		if err := s.db.QueryRow("SELECT receipt FROM ops WHERE actor = 'test' AND id = ?", op.ID).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) > MaxReceipt {
			t.Fatalf("words %d: the receipt is %d bytes", i, len(body))
		}
		again, err := s.Apply(ctx, op, "test.refuse", nil, func(x *tx) error { return errors.New("not run again") })
		want(t, again, err, StatusFailed, RefusalClosePolicy)
		if !again.Repeat || again.Error != rc.Error {
			t.Fatalf("words %d: the repeat %+v", i, again)
		}
		// The repeat's answer, its flag set, is the larger of the two, and
		// the words are fitted to it, so it stays under the bound too.
		if b, _ := json.Marshal(again); len(b) > MaxReceipt {
			t.Fatalf("words %d: the repeat's receipt is %d bytes", i, len(b))
		}
		longer := again
		longer.Error = w[:len(cut)+utf8.RuneLen([]rune(w[len(cut):])[0])] + "…"
		if b, _ := json.Marshal(longer); len(b) <= MaxReceipt {
			t.Fatalf("words %d were cut shorter than they must be: %d bytes with one more character", i, len(b))
		}
	}
	// A read-only store's refusal quotes why it is read-only, which may
	// hold the database's path: its receipt is fitted the same way.
	ro := &Store{readOnly: true, why: strings.Repeat("<", maxReceiptWords)}
	rc, err = ro.Apply(ctx, testOp(strings.Repeat("o", 32)), "test.refuse", nil, func(x *tx) error { return errors.New("not run") })
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if b, _ := json.Marshal(rc); len(b) > MaxReceipt || !strings.HasSuffix(rc.Error, "…") {
		t.Fatalf("the read-only refusal's receipt is %d bytes: %q", len(b), rc.Error)
	}
}
