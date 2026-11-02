package dispatch

import (
	"context"
	"errors"
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
		if c.Ticket != oldKey || c.Project != "OLD" || c.At.IsZero() || c.Actor != "test" {
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
