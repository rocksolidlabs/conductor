package dispatch

import (
	"context"
	"testing"
)

// openRefs is the refs of a page of OpenPlans.
func openRefs(page []OpenPlan) []string {
	out := []string{}
	for _, p := range page {
		out = append(out, p.Ref())
	}
	return out
}

// The revisions that need the operator: a proposed one; an approved one
// once its approval no longer holds (the ticket's description edited),
// with its staleness, and not once the edit is undone; a handed-over
// comment edited, its remedy revise; a deleted ticket's left out; oldest
// first, a page at most limit with next set and the total of them all.
func TestOpenPlans(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	open := func(after PlanCursor, limit int) ([]OpenPlan, bool, int) {
		t.Helper()
		page, next, total, err := s.OpenPlans(ctx, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		return page, next, total
	}
	first := propose(t, s, "p1", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	page, next, total := open(PlanCursor{}, 10)
	if len(page) != 1 || next || total != 1 || page[0].ID != first || page[0].Ref() != "plan:"+key+"/1" || page[0].Key != key || page[0].Title != "Fix the login loop" || page[0].TicketRev == 0 || page[0].Stale != nil {
		t.Fatalf("a proposed revision: %+v %v %d", page, next, total)
	}
	approve(t, s, "a1", first, "")
	if page, _, total := open(PlanCursor{}, 10); len(page) != 0 || total != 0 {
		t.Fatalf("an approval that holds: %+v %d", page, total)
	}
	mustEdit(t, s, "d1", key, TicketEdit{Set: TicketSet{Description: ptr("It loops twice.")}})
	page, _, total = open(PlanCursor{}, 10)
	if len(page) != 1 || total != 1 || page[0].ID != first || page[0].Stale == nil || page[0].Stale.Remedy != RemedyApprove || page[0].Stale.Words != "the description changed since revision 1 was approved" {
		t.Fatalf("an approval no longer holding: %+v %+v %d", page, page[0].Stale, total)
	}
	mustEdit(t, s, "d2", key, TicketEdit{Set: TicketSet{Description: ptr("It loops.")}})
	if page, _, total := open(PlanCursor{}, 10); len(page) != 0 || total != 0 {
		t.Fatalf("the edit undone: %+v %d", page, total)
	}

	// A handed-over comment edited: stale for good, its remedy revise.
	handed := comment(t, s, "c1", key, "handed over")
	body := testPlanBody("crew")
	body.Comments = []PlanComment{{ID: handed}}
	second := propose(t, s, "p2", key, PlanRequest{Crew: "crew", Comments: []string{handed}}, body)
	approve(t, s, "a2", second, "")
	if rc, err := s.EditComment(ctx, testOp("e1"), key, handed, "edited"); err != nil || rc.Status != StatusDone {
		t.Fatalf("edit: %+v %v", rc, err)
	}
	page, _, _ = open(PlanCursor{}, 10)
	if len(page) != 1 || page[0].ID != second || page[0].Stale == nil || page[0].Stale.Remedy != RemedyRevise {
		t.Fatalf("a revise: %+v", page)
	}

	// Oldest first, across tickets; the limit and the cursor; a deleted
	// ticket's left out.
	_, other := makeTicket(t, s, NewTicket{Project: "CND", Title: "Another"})
	third := propose(t, s, "p3", other, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	fourth := propose(t, s, "p4", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	page, next, total = open(PlanCursor{}, 2)
	if len(page) != 2 || !next || total != 3 || page[0].ID != second || page[1].ID != third {
		t.Fatalf("the first page: %v %v %d", openRefs(page), next, total)
	}
	page, next, total = open(PlanCursor{Since: page[1].CreatedAt, Ref: page[1].Ref()}, 2)
	if len(page) != 1 || next || total != 3 || page[0].ID != fourth {
		t.Fatalf("the second page: %v %v %d", openRefs(page), next, total)
	}
	if rc, err := s.DeleteTicket(ctx, testOp("del"), other); err != nil || rc.Status != StatusDone {
		t.Fatalf("delete: %+v %v", rc, err)
	}
	if page, _, total := open(PlanCursor{}, 10); total != 2 || len(page) != 2 || page[0].ID != second || page[1].ID != fourth {
		t.Fatalf("after the delete: %v %d", openRefs(page), total)
	}
}

// 230 open revisions over five tickets, proposed ones and approvals that
// no longer hold among them, read page by page from the cursor: every
// one once, in (since, ref) order, total 230 on every page.
func TestOpenPlansPages(t *testing.T) {
	s := openStore(t, storeDir(t))
	newProject(t, s, "CND")
	ctx := context.Background()
	var keys []string
	for i := range 5 {
		_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: opID("ticket ", i), Description: "d"})
		keys = append(keys, key)
	}
	want := map[string]bool{}
	for i := range 230 {
		key := keys[i%5]
		id := propose(t, s, opID("p", i), key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
		if i < 5 {
			// Each ticket's first revision approved, then made stale by
			// an edit of its description (one approval a ticket: another
			// would set it superseded).
			approve(t, s, opID("a", i), id, "")
			mustEdit(t, s, opID("e", i), key, TicketEdit{Set: TicketSet{Description: ptr(opID("d", i))}})
		}
		want[id] = true
	}
	var after PlanCursor
	seen := map[string]bool{}
	var last *OpenPlan
	for pages := 0; ; pages++ {
		page, next, total, err := s.OpenPlans(ctx, after, 50)
		if err != nil {
			t.Fatal(err)
		}
		if total != 230 {
			t.Fatalf("page %d: total %d", pages, total)
		}
		for i := range page {
			p := page[i]
			if seen[p.ID] {
				t.Fatalf("%s twice", p.Ref())
			}
			if last != nil && planBefore(p.CreatedAt, p.Ref(), PlanCursor{last.CreatedAt, last.Ref()}) <= 0 {
				t.Fatalf("%s after %s", p.Ref(), last.Ref())
			}
			seen[p.ID], last = true, &page[i]
		}
		if !next {
			break
		}
		after = PlanCursor{Since: last.CreatedAt, Ref: last.Ref()}
	}
	if len(seen) != len(want) {
		t.Fatalf("read %d of %d", len(seen), len(want))
	}
}
