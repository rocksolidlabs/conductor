package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A comment's number is derived at read: 1 to k with no gap under 50
// concurrent adds, in the order (created_at, author, id), and among its
// own ticket's comments alone, another ticket's made among them numbered
// apart; a comment the test inserts with an earlier created_at (a
// replica's, say) takes its place and the later ones renumber, no row
// rewritten.
func TestCommentsAreNumberedAtReadWithNoGap(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	tid, key := newTicket(t, s, "CND", "Talked about")
	otherID, other := newTicket(t, s, "CND", "Talked about too")
	comment := func(ticket, op, body string) error {
		rc, err := s.AddComment(ctx, testOp(op), ticket, body)
		if err == nil && rc.Status != StatusDone {
			err = fmt.Errorf("%s: %+v", body, rc)
		}
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, 75)
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Every other one comments on the other ticket first, so the two
			// tickets' comments are made among each other's.
			if i%2 == 0 {
				errs <- comment(other, opID("o", i), fmt.Sprintf("other %d", i))
			}
			errs <- comment(key, opID("c", i), fmt.Sprintf("comment %d", i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cs, err := s.Comments(ctx, key, 0, MaxCommentPage)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 50 {
		t.Fatalf("%d comments", len(cs))
	}
	for i, c := range cs {
		if c.N != i+1 || c.TicketID != tid {
			t.Fatalf("comment %d: %+v", i, c)
		}
		if i > 0 {
			p := cs[i-1]
			if c.CreatedAt.Before(p.CreatedAt) || (c.CreatedAt.Equal(p.CreatedAt) && (c.Author < p.Author || (c.Author == p.Author && c.ID < p.ID))) {
				t.Fatalf("comments %d and %d out of order: %+v %+v", i, i+1, p, c)
			}
		}
	}
	// The other ticket's 25, made among these, are its own 1 to 25.
	ocs, err := s.Comments(ctx, other, 0, MaxCommentPage)
	if err != nil || len(ocs) != 25 {
		t.Fatalf("the other ticket's comments: %d %v", len(ocs), err)
	}
	for i, c := range ocs {
		if c.N != i+1 || c.TicketID != otherID {
			t.Fatalf("the other ticket's comment %d: %+v", i, c)
		}
	}
	// A comment written earlier than all, as a replica's add would bring
	// it, takes the first place; one at the same moment as the fifth, by
	// an author before test, takes the fifth's place and moves it on: its
	// id the greatest there is, so the author alone puts it first.
	early, tie := newID(), "ffffffffffffffff"
	for _, r := range []struct {
		id, author, at string
	}{{early, "replica", stamp(cs[0].CreatedAt.Add(-time.Second))}, {tie, "agent:a", stamp(cs[4].CreatedAt)}} {
		if _, err := s.db.Exec("INSERT INTO comments (id, ticket_id, author, by, body, created_at, rev) VALUES (?, ?, ?, 'Remote', 'from afar', ?, 1)",
			r.id, tid, r.author, r.at); err != nil {
			t.Fatal(err)
		}
	}
	again, err := s.Comments(ctx, key, 0, MaxCommentPage)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, c := range again {
		if c.N != i+1 {
			t.Fatalf("after the inserts, comment %d is numbered %d", i, c.N)
		}
		ids = append(ids, c.ID)
	}
	// The order is (created_at, author, id), whatever the clock gave the
	// 50: sorted here the same way, the early one first and the tie
	// before the fifth.
	all := append([]Comment{{ID: early, Author: "replica", CreatedAt: cs[0].CreatedAt.Add(-time.Second)}, {ID: tie, Author: "agent:a", CreatedAt: cs[4].CreatedAt}}, cs...)
	slices.SortFunc(all, func(a, b Comment) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		if c := strings.Compare(a.Author, b.Author); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	var want []string
	for _, c := range all {
		want = append(want, c.ID)
	}
	if !slices.Equal(ids, want) || ids[0] != early || slices.Index(ids, tie) > slices.Index(ids, cs[4].ID) {
		t.Fatalf("the order after the inserts:\n%v\nwant\n%v", ids, want)
	}
	// Pages: after a number, at most a limit.
	page, err := s.Comments(ctx, key, 50, 10)
	if err != nil || len(page) != 2 || page[0].N != 51 || page[1].N != 52 {
		t.Fatalf("the last page: %+v %v", page, err)
	}
	page, err = s.Comments(ctx, key, 0, 10)
	if err != nil || len(page) != 10 || page[0].ID != early || page[9].N != 10 {
		t.Fatalf("the first page: %+v %v", page, err)
	}
	for _, bad := range [][2]int{{0, 0}, {0, MaxCommentPage + 1}, {-1, 10}} {
		if _, err := s.Comments(ctx, key, bad[0], bad[1]); err == nil {
			t.Fatalf("a page of %d after %d was read", bad[1], bad[0])
		}
	}
	// One comment, read with its number.
	c, err := s.Comment(ctx, key, cs[4].ID)
	if err != nil || c.N != slices.Index(ids, cs[4].ID)+1 || c.N < 7 || c.Body != cs[4].Body {
		t.Fatalf("the fifth comment, now seventh or later: %+v %v", c, err)
	}
	if _, err := s.Comments(ctx, "CND-99", 0, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the comments of no ticket: %v", err)
	}
	if _, err := s.Comment(ctx, key, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no comment: %v", err)
	}
}

func TestAddEditAndDeleteAComment(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	pid := newProject(t, s, "CND")
	tid, key := newTicket(t, s, "CND", "Talked about")
	_, other := newTicket(t, s, "CND", "Elsewhere")
	calls := listen(s)
	op := Op{Actor: "admin", ID: "c1", By: "Alice"}
	rc, err := s.AddComment(ctx, op, key, "First, <b> & \"quoted\"")
	want(t, rc, err, StatusDone, "")
	id := rc.ID
	if len(id) != IDLen || rc.Rev != 1 || rc.Intent != "comment.add" {
		t.Fatalf("the receipt: %+v", rc)
	}
	if got := calls.one(t); len(got) != 1 || got[0] != (Change{Seq: got[0].Seq, Entity: EntityComment, ID: id, Ticket: tid, Project: "CND"}) {
		t.Fatalf("OnChange: %+v", got)
	}
	// A create row with what never changes, then the body: the author is
	// the actor and by its display name.
	rows := changesOf(t, s, "admin", "c1")
	if len(rows) != 2 || rows[0].Op != OpCreate || rows[1].Op != OpSet || rows[1].Field != "body" || rows[1].Value != "First, <b> & \"quoted\"" || rows[1].Prior != "" {
		t.Fatalf("the rows: %+v", rows)
	}
	for _, r := range rows {
		if r.Entity != EntityComment || r.EntityID != id || r.TicketID != tid || r.ProjectID != pid {
			t.Fatalf("a row: %+v", r)
		}
	}
	var created commentCreated
	if err := json.Unmarshal([]byte(rows[0].Value), &created); err != nil || created.ID != id || created.TicketID != tid || created.Author != "admin" || created.By != "Alice" || created.CreatedAt.IsZero() {
		t.Fatalf("the create row's value: %s %v", rows[0].Value, err)
	}
	c, err := s.Comment(ctx, key, id)
	if err != nil || c.N != 1 || c.Author != "admin" || c.By != "Alice" || c.Body != "First, <b> & \"quoted\"" || c.Rev != 1 || !c.EditedAt.IsZero() || !c.DeletedAt.IsZero() {
		t.Fatalf("the comment: %+v %v", c, err)
	}
	// Adding a comment moves nothing of the ticket's: its rev stays.
	if rev := ticketOf(t, s, key).Rev; rev != 1 {
		t.Fatalf("the ticket's rev after a comment: %d", rev)
	}
	// The ticket edited, its rev is 2 and the comment's still 1.
	rc, err = edit(s, "t1", key, TicketEdit{Set: TicketSet{Title: ptr("Talked about at length")}})
	want(t, rc, err, StatusDone, "")
	// Edit: the body set, its prior kept, and edited_at; the comment's rev
	// is what an expected revision compares with, not the ticket's.
	stale := testOp("e0")
	stale.ExpectedRev = 2
	rc, err = s.EditComment(ctx, stale, key, id, "Second")
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	edit := testOp("e1")
	edit.ExpectedRev = 1
	rc, err = s.EditComment(ctx, edit, key, id, "Second")
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 2 {
		t.Fatalf("the edit's receipt: %+v", rc)
	}
	edited := rowsOf(t, s, "e1")
	if len(edited) != 2 || edited[0] != "set:body=Second<First, <b> & \"quoted\"" || !strings.HasPrefix(edited[1], "set:edited_at=") || !strings.HasSuffix(edited[1], "<") {
		t.Fatalf("the edit's rows: %v", edited)
	}
	if c, err := s.Comment(ctx, key, id); err != nil || c.Body != "Second" || c.EditedAt.IsZero() || c.Rev != 2 {
		t.Fatalf("edited: %+v %v", c, err)
	}
	// The same body again changes nothing.
	rc, err = s.EditComment(ctx, testOp("e2"), key, id, "Second")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	// A second edit's prior is the first's.
	rc, err = s.EditComment(ctx, testOp("e3"), key, id, "Third")
	want(t, rc, err, StatusDone, "")
	if r := rowsOf(t, s, "e3"); len(r) != 2 || r[0] != "set:body=Third<Second" || !strings.Contains(r[1], "<20") {
		t.Fatalf("the second edit's rows: %v", r)
	}
	// Delete: a tombstone, keeping its place; then every write on it is
	// not_found.
	rc, err = s.AddComment(ctx, testOp("c2"), key, "Later")
	want(t, rc, err, StatusDone, "")
	later := rc.ID
	rc, err = s.DeleteComment(ctx, testOp("d1"), key, id)
	want(t, rc, err, StatusDone, "")
	if r := rowsOf(t, s, "d1"); len(r) != 1 || !strings.HasPrefix(r[0], "set:deleted_at=20") || !strings.HasSuffix(r[0], "<") {
		t.Fatalf("the delete's rows: %v", r)
	}
	cs, err := s.Comments(ctx, key, 0, 10)
	if err != nil || len(cs) != 2 || cs[0].ID != id || cs[0].N != 1 || cs[0].DeletedAt.IsZero() || cs[0].Body != "Third" || cs[1].ID != later || cs[1].N != 2 {
		t.Fatalf("after the delete: %+v %v", cs, err)
	}
	for i, f := range []func() (Receipt, error){
		func() (Receipt, error) { return s.EditComment(ctx, testOp("x1"), key, id, "Again") },
		func() (Receipt, error) { return s.DeleteComment(ctx, testOp("x2"), key, id) },
		func() (Receipt, error) { return s.EditComment(ctx, testOp("x3"), key, "0000000000000000", "Again") },
		// Another ticket's comment through this ticket's key.
		func() (Receipt, error) { return s.DeleteComment(ctx, testOp("x4"), other, later) },
		func() (Receipt, error) { return s.EditComment(ctx, testOp("x5"), "CND-99", later, "Again") },
		func() (Receipt, error) { return s.AddComment(ctx, testOp("x6"), "CND-99", "Again") },
	} {
		rc, err := f()
		if err != nil || rc.Refusal != RefusalNotFound {
			t.Fatalf("write %d: %+v %v", i, rc, err)
		}
	}
	// The body's rule.
	for i, c := range []struct {
		body, refusal string
	}{
		{"", RefusalInvalid},
		{"a\rb", RefusalInvalid},
		{strings.Repeat("b", MaxCommentBody+1), RefusalBound},
		{strings.Repeat("b", MaxCommentBody), ""},
		{"- a\n\t- b", ""},
	} {
		status := StatusFailed
		if c.refusal == "" {
			status = StatusDone
		}
		rc, err := s.AddComment(ctx, testOp(opID("body", i)), key, c.body)
		want(t, rc, err, status, c.refusal)
		rc, err = s.EditComment(ctx, testOp(opID("ebody", i)), key, later, c.body+"!")
		if c.refusal == RefusalBound || c.body == strings.Repeat("b", MaxCommentBody) {
			want(t, rc, err, StatusFailed, RefusalBound)
		}
	}
	// An archived project refuses all three.
	if rc, err := s.ArchiveProject(ctx, testOp("archive"), "CND"); err != nil || rc.Status != StatusDone {
		t.Fatalf("archive: %+v %v", rc, err)
	}
	rc, err = s.AddComment(ctx, testOp("a1"), key, "Archived")
	want(t, rc, err, StatusFailed, RefusalArchived)
	rc, err = s.EditComment(ctx, testOp("a2"), key, later, "Archived")
	want(t, rc, err, StatusFailed, RefusalArchived)
	rc, err = s.DeleteComment(ctx, testOp("a3"), key, later)
	want(t, rc, err, StatusFailed, RefusalArchived)
}

// A ticket holds at most MaxComments, its tombstones among them, since a
// deleted comment keeps its place.
func TestATicketHoldsAtMostItsBoundOfComments(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	tid, key := newTicket(t, s, "CND", "Talked about")
	now := time.Now()
	inserts, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxComments - 1 {
		deleted := ""
		if i == 0 {
			deleted = stamp(now)
		}
		if _, err := inserts.Exec("INSERT INTO comments (id, ticket_id, author, by, body, created_at, deleted_at, rev) VALUES (?, ?, 'test', '', 'x', ?, ?, 1)",
			newID(), tid, stamp(now.Add(time.Duration(i))), deleted); err != nil {
			t.Fatal(err)
		}
	}
	if err := inserts.Commit(); err != nil {
		t.Fatal(err)
	}
	rc, err := s.AddComment(ctx, testOp("last"), key, "The last one")
	want(t, rc, err, StatusDone, "")
	rc, err = s.AddComment(ctx, testOp("past"), key, "One too many")
	want(t, rc, err, StatusFailed, RefusalBound)
	if !strings.Contains(rc.Error, fmt.Sprint(MaxComments)) {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	cs, err := s.Comments(ctx, key, MaxComments-MaxCommentPage, MaxCommentPage)
	if err != nil || len(cs) != MaxCommentPage || cs[len(cs)-1].N != MaxComments || cs[len(cs)-1].Body != "The last one" {
		t.Fatalf("the last page: %d comments %v", len(cs), err)
	}
}

// A run's comment goes to the ticket that holds the run when it commits,
// found in its own transaction, and its id keeps that ticket: before any
// link it is ErrNoRunTicket with nothing written, so the same id comments
// once a ticket holds the run; sent again after the link moved, after its
// receipt was pruned and the link removed, and after the ticket's delete,
// it answers its receipt, a repeat naming the ticket it went to, and
// other content op_conflict. A refused comment's id answers op_conflict
// for other content, before and after the link moved, and its refusal
// again for its own.
func TestARunCommentKeepsTheTicketItWentTo(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "RCM")
	_, a := newTicket(t, s, "RCM", "A")
	_, b := newTicket(t, s, "RCM", "B")
	s.SetRunExists(runs("run1"))
	// comment is the run's comment and, for a done one, the ticket its
	// log names (OpTicket), as the API reads it back.
	comment := func(id, body string) (Receipt, string, error) {
		t.Helper()
		rc, err := s.AddRunComment(ctx, testOp(id), "run1", body)
		if err != nil || rc.Status != StatusDone {
			return rc, "", err
		}
		key, found, err := s.OpTicket(ctx, "test", id)
		if err != nil || !found {
			t.Fatalf("%s is done and its log names no ticket: %v", id, err)
		}
		return rc, key, nil
	}
	count := func(key string) int {
		t.Helper()
		cs, err := s.Comments(ctx, key, 0, MaxCommentPage)
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}
	sent := func(when, id, body, wantKey string, repeat bool) {
		t.Helper()
		rc, key, err := comment(id, body)
		want(t, rc, err, StatusDone, "")
		if key != wantKey || rc.Repeat != repeat {
			t.Fatalf("%s: %s went to %q (repeat %v), want %q (repeat %v)", when, id, key, rc.Repeat, wantKey, repeat)
		}
	}
	refused := func(when, id, body, refusal string, repeat bool) {
		t.Helper()
		rc, key, err := comment(id, body)
		want(t, rc, err, StatusFailed, refusal)
		if key != "" || rc.Repeat != repeat {
			t.Fatalf("%s: %s refused %s names %q (repeat %v)", when, id, refusal, key, rc.Repeat)
		}
	}

	if _, _, err := comment("c1", "on A"); !errors.Is(err, ErrNoRunTicket) {
		t.Fatalf("before any link: %v", err)
	}
	if _, found, err := s.Receipt(ctx, "test", "c1"); err != nil || found {
		t.Fatalf("no ticket wrote a receipt: %v %v", found, err)
	}
	link, err := addLink(s, "l1", a, NewLink{Kind: LinkRun, Ref: "run1"})
	want(t, link, err, StatusDone, "")
	sent("linked to A", "c1", "on A", a, false)
	refused("linked to A", "c2", "", RefusalInvalid, false)
	refused("linked to A", "c2", "now with words", RefusalOpConflict, false)

	rc, err := s.RemoveLink(ctx, testOp("r1"), a, link.ID)
	want(t, rc, err, StatusDone, "")
	link, err = addLink(s, "l2", b, NewLink{Kind: LinkRun, Ref: "run1"})
	want(t, link, err, StatusDone, "")
	sent("moved to B", "c1", "on A", a, true)
	refused("moved to B", "c1", "other", RefusalOpConflict, false)
	refused("moved to B", "c2", "", RefusalInvalid, true)
	refused("moved to B", "c2", "now with words", RefusalOpConflict, false)
	sent("moved to B", "c3", "on B", b, false)
	if na, nb := count(a), count(b); na != 1 || nb != 1 {
		t.Fatalf("moved to B: %d comments on A and %d on B", na, nb)
	}

	if _, err := s.db.Exec("DELETE FROM ops WHERE actor = 'test' AND id = 'c1'"); err != nil {
		t.Fatal(err)
	}
	rc, err = s.RemoveLink(ctx, testOp("r2"), b, link.ID)
	want(t, rc, err, StatusDone, "")
	sent("pruned and unlinked", "c1", "on A", a, true)
	rc, err = s.DeleteTicket(ctx, testOp("d1"), a)
	want(t, rc, err, StatusDone, "")
	sent("A deleted", "c1", "on A", a, true)
	if _, _, err := comment("c4", "nowhere"); !errors.Is(err, ErrNoRunTicket) {
		t.Fatalf("unlinked: %v", err)
	}
	if na, nb := count(a), count(b); na != 1 || nb != 1 {
		t.Fatalf("at the end: %d comments on A and %d on B", na, nb)
	}
}

// A run's comment is answered by its receipt once it commits, whatever
// becomes of its context after: cancelled from OnChange, after the commit
// and before AddRunComment returns, the receipt is done, the comment on
// the ticket, and the ticket it went to is OpTicket's to read with a
// live context; the same id sent again is a repeat.
func TestARunCommentKeepsItsReceiptWhenItsContextEndsAfterTheCommit(t *testing.T) {
	s := openStore(t, storeDir(t))
	newProject(t, s, "RCX")
	_, key := newTicket(t, s, "RCX", "A")
	s.SetRunExists(runs("run1"))
	link, err := addLink(s, "l1", key, NewLink{Kind: LinkRun, Ref: "run1"})
	want(t, link, err, StatusDone, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var commits atomic.Int32
	s.OnChange(func([]Change) {
		commits.Add(1)
		cancel()
	})
	rc, err := s.AddRunComment(ctx, testOp("c1"), "run1", "seen")
	want(t, rc, err, StatusDone, "")
	if commits.Load() != 1 || ctx.Err() == nil || rc.ID == "" {
		t.Fatalf("the comment's commit: %d commits, context %v, receipt %+v", commits.Load(), ctx.Err(), rc)
	}
	if _, _, err := s.OpTicket(ctx, "test", "c1"); err == nil {
		t.Fatal("OpTicket read with the cancelled context")
	}
	live := context.Background()
	if got, found, err := s.OpTicket(live, "test", "c1"); err != nil || !found || got != key {
		t.Fatalf("OpTicket after the commit: %q %v %v", got, found, err)
	}
	if c, err := s.Comment(live, key, rc.ID); err != nil || c.Body != "seen" {
		t.Fatalf("the comment: %+v %v", c, err)
	}
	again, err := s.AddRunComment(live, testOp("c1"), "run1", "seen")
	want(t, again, err, StatusDone, "")
	if !again.Repeat || again.ID != rc.ID {
		t.Fatalf("the resend: %+v", again)
	}
}
