package dispatch

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestChangesAfter(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "OLD")
	newProject(t, s, "NEW")
	_, oldKey := newTicket(t, s, "OLD", "an old ticket")
	_, newKey := newTicket(t, s, "NEW", "a new ticket")
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A comment on the old ticket bumps nothing of the ticket's own: the
	// feed sees it all the same, keyed by the ticket and its project.
	if rc, err := s.AddComment(ctx, testOp("c1"), oldKey, "a word on the old one"); err != nil || rc.Status != StatusDone {
		t.Fatalf("comment: %+v %v", rc, err)
	}
	got, err := s.ChangesAfter(ctx, head, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("the comment's change is not in the feed")
	}
	for _, c := range got {
		if c.Seq <= head {
			t.Fatalf("a change at or before %d: %+v", head, c)
		}
		if c.Ticket != oldKey || c.Project != "OLD" || c.At.IsZero() || c.Actor != "test" || c.OpID != "c1" {
			t.Fatalf("the comment's change: %+v", c)
		}
	}
	if got[0].Entity != EntityComment {
		t.Fatalf("the first change after the head is the comment's: %+v", got[0])
	}
	// Every project's changes, in seq order, from the first.
	all, err := s.ChangesAfter(ctx, 0, MaxFeedPage)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, c := range all {
		if i > 0 && c.Seq <= all[i-1].Seq {
			t.Fatalf("out of order at %d: %d after %d", i, c.Seq, all[i-1].Seq)
		}
		seen[c.Ticket] = true
	}
	if !seen[oldKey] || !seen[newKey] {
		t.Fatalf("both projects' tickets: %v", seen)
	}
	// A page is limited.
	if page, err := s.ChangesAfter(ctx, 0, 2); err != nil || len(page) != 2 || page[0].Seq != all[0].Seq {
		t.Fatalf("a page of two: %v %v", page, err)
	}
	for _, bad := range []struct {
		after int64
		limit int
	}{{0, 0}, {0, MaxFeedPage + 1}, {-1, 10}} {
		if _, err := s.ChangesAfter(ctx, bad.after, bad.limit); !errors.Is(err, ErrBadQuery) {
			t.Fatalf("after %d limit %d: %v", bad.after, bad.limit, err)
		}
	}
}

// TestChangeValueIsTheChangesOwn: two status changes in a row each read
// back the status that change set, not the ticket's status now; a seq of
// no change is ErrNotFound.
func TestChangeValueIsTheChangesOwn(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "a ticket")
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{TicketReady, TicketInProgress} {
		if rc, err := s.EditTicket(ctx, testOp(fmt.Sprintf("s%d", i)), key, TicketEdit{Set: TicketSet{Status: ptr(status)}}); err != nil || rc.Status != StatusDone {
			t.Fatalf("%s: %+v %v", status, rc, err)
		}
	}
	page, err := s.ChangesAfter(ctx, head, 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range page {
		if c.Field != "status" {
			continue
		}
		v, err := s.ChangeValue(ctx, c.Seq)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if len(got) != 2 || got[0] != TicketReady || got[1] != TicketInProgress {
		t.Fatalf("the status changes' values: %v", got)
	}
	if _, err := s.ChangeValue(ctx, page[len(page)-1].Seq+100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such change: %v", err)
	}
}
