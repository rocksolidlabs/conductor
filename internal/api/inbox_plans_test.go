package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// planRef is the inbox's name of CND-1's revision n.
func planRef(n int) string { return fmt.Sprintf("plan:CND-1/%d", n) }

// editTicket sets fields of CND-1 through its route.
func (p *planTest) editTicket(t *testing.T, set map[string]any) {
	t.Helper()
	p.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": set}, http.StatusOK, "")
}

// A proposed revision is an item with Approve and Withdraw; approved, it
// is not, until the ticket's description is edited (an item again, Approve
// again offered, its Now saying why), and not once the edit is undone. An
// approval whose remedy is a new revision has no Approve.
func TestInboxPlanItems(t *testing.T) {
	p := newPlanTest(t, nil)
	id := p.proposed(t, p.saveCrew(t, teamCrew("Team", "cat")).ID, nil)
	page, _ := p.inboxList(t, "")
	if page.Total != 1 || !listed(page, planRef(1)) || page.Items[0].Kind != inboxPlan {
		t.Fatalf("a proposed revision: %+v", page)
	}
	it := p.itemOf(t, planRef(1))
	if !slices.Equal(actionNames(it), []string{"approve", "withdraw"}) || it.Subject != planRef(1) || !actionOf(t, it, "withdraw").Confirm {
		t.Fatalf("its item: %+v", it)
	}
	if !strings.Contains(it.Blocking.Words, "Nothing runs for CND-1 until a revision is approved and launched") || it.Evidence[0].Ref != "ticket:CND-1" {
		t.Fatalf("blocking and evidence: %+v %+v", it.Blocking, it.Evidence)
	}
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/approve", map[string]any{"opId": opID()}, http.StatusOK, "")
	if page, _ := p.inboxList(t, ""); page.Total != 0 {
		t.Fatalf("an approval that holds: %+v", page)
	}
	p.editTicket(t, map[string]any{"description": "It loops twice."})
	page, _ = p.inboxList(t, "")
	if page.Total != 1 || !listed(page, planRef(1)) {
		t.Fatalf("the approval no longer holding: %+v", page)
	}
	it = p.itemOf(t, planRef(1))
	if !slices.Equal(actionNames(it), []string{"approve", "withdraw"}) || !strings.Contains(it.Now, "the description changed since revision 1 was approved") {
		t.Fatalf("approve again: %+v", it)
	}
	p.editTicket(t, map[string]any{"description": "It loops."})
	if page, _ := p.inboxList(t, ""); page.Total != 0 {
		t.Fatalf("the edit undone: %+v", page)
	}
	if it := p.itemOf(t, planRef(1)); len(it.Actions) != 0 || it.Now == "" {
		t.Fatalf("not open, read: %+v", it)
	}
	// A remedy of revise: Withdraw alone.
	rd := p.srv.newInboxRead(context.Background())
	revise := rd.planItem(dispatch.OpenPlan{PlanSummary: dispatch.PlanSummary{ID: id, N: 1, State: dispatch.PlanApproved, Rev: 3,
		Stale: &dispatch.PlanStale{Words: "a handed-over comment changed", Remedy: dispatch.RemedyRevise}}, Key: "CND-1"}, true)
	if !slices.Equal(actionNames(*revise), []string{"withdraw"}) || !strings.Contains(revise.Now, "a new revision is needed") {
		t.Fatalf("revise: %+v", revise)
	}
}

// Approve carries both revisions: the ticket edited alone, an Approve at
// its action's revisions is refused stale_revision; the revision approved
// by another operation alone (its rev bumped, the ticket's not), a
// Withdraw at its action's Expect is refused; both at fresh revisions
// succeed.
func TestInboxPlanActionsCarryBothRevisions(t *testing.T) {
	p := newPlanTest(t, nil)
	id := p.proposed(t, p.saveCrew(t, teamCrew("Team", "cat")).ID, nil)
	it := p.itemOf(t, planRef(1))
	approve := actionOf(t, it, "approve")
	if approve.Expect != it.Revision || approve.TicketRev == 0 || approve.Route != "POST /api/dispatch/plans/"+id+"/approve" {
		t.Fatalf("approve: %+v", approve)
	}
	p.editTicket(t, map[string]any{"title": "Fix the login loop, now"})
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/approve", map[string]any{"opId": opID(), "expectedRev": atoi(t, approve.Expect), "ticketRev": approve.TicketRev}, http.StatusConflict, dispatch.RefusalStaleRevision)
	fresh := actionOf(t, p.itemOf(t, planRef(1)), "approve")
	if fresh.TicketRev == approve.TicketRev {
		t.Fatalf("the ticket's revision read again: %d", fresh.TicketRev)
	}
	withdraw := actionOf(t, p.itemOf(t, planRef(1)), "withdraw")
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/approve", map[string]any{"opId": opID(), "expectedRev": atoi(t, fresh.Expect), "ticketRev": fresh.TicketRev}, http.StatusOK, "")
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/withdraw", map[string]any{"opId": opID(), "expectedRev": atoi(t, withdraw.Expect)}, http.StatusConflict, dispatch.RefusalStaleRevision)
	now := p.itemOf(t, planRef(1))
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/withdraw", map[string]any{"opId": opID(), "expectedRev": atoi(t, now.Revision)}, http.StatusOK, "")
}

// atoi is s as a number, failing t otherwise.
func atoi(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return n
}

// A yolo-on revision's Approve scope says so and names the approval it
// supersedes; a revision whose body cannot be read has no Approve and
// says why.
func TestInboxPlanScopeSaysYolo(t *testing.T) {
	p := newPlanTest(t, nil)
	on := true
	c := teamCrew("Bold", "cat")
	c.Yolo = &on
	saved := p.saveCrew(t, c)
	first := p.proposed(t, saved.ID, nil)
	p.expect(t, "POST", "/api/dispatch/plans/"+first+"/approve", map[string]any{"opId": opID()}, http.StatusOK, "")
	p.proposed(t, saved.ID, nil)
	scope := actionOf(t, p.itemOf(t, planRef(2)), "approve").Scope
	for _, want := range []string{"yolo on", "sets revision 1's approval superseded", "build", "cat", "launching is a separate step"} {
		if !strings.Contains(scope, want) {
			t.Fatalf("the scope %q lacks %q", scope, want)
		}
	}
	rd := p.srv.newInboxRead(context.Background())
	unread := rd.planItem(dispatch.OpenPlan{PlanSummary: dispatch.PlanSummary{ID: "0123456789abcdef", N: 3, State: dispatch.PlanProposed, Rev: 1}, Key: "CND-1"}, true)
	if slices.Contains(actionNames(*unread), "approve") || !strings.Contains(unread.Now, "revision 3 could not be read, so its yolo choice is not known") {
		t.Fatalf("a body not read: %+v", unread)
	}
}

// While Brakeman's resolver refuses the plan kind, a plan item's subject
// is its ticket's ref; once it takes it, the plan's own.
func TestInboxPlanSubjectBeforeBrakemanPlanRefs(t *testing.T) {
	p := newPlanTest(t, nil)
	p.proposed(t, p.saveCrew(t, teamCrew("Team", "cat")).ID, nil)
	planRefsParsed = func() bool { return false }
	t.Cleanup(func() { planRefsParsed = nil })
	if it := p.itemOf(t, planRef(1)); it.Subject != "ticket:CND-1" {
		t.Fatalf("before the plan kind: %q", it.Subject)
	}
	planRefsParsed = nil
	if it := p.itemOf(t, planRef(1)); it.Subject != planRef(1) {
		t.Fatalf("with the plan kind: %q", it.Subject)
	}
}

// An approval's track: its validated step stays done after the ticket is
// edited, while the item's Now says the approval no longer holds, and
// after the edit is undone; two reads give the same steps.
func TestInboxTrackPlanApproval(t *testing.T) {
	p := newPlanTest(t, nil)
	id := p.proposed(t, p.saveCrew(t, teamCrew("Team", "cat")).ID, nil)
	a := actionOf(t, p.itemOf(t, planRef(1)), "approve")
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/approve", map[string]any{"opId": "ap-1", "expectedRev": atoi(t, a.Expect), "ticketRev": a.TicketRev}, http.StatusOK, "")
	check := func(what string) {
		t.Helper()
		for range 2 {
			s := p.trackOf(t, planRef(1), "ap-1", "")
			if s[stepRecorded].State != stepDone || s[stepRecorded].Receipt != "op:"+adminActor+"/ap-1" || s[stepValidated].State != stepDone || s[stepResumed].State != stepNotApplicable {
				t.Fatalf("%s: %+v", what, s)
			}
		}
	}
	check("approved")
	p.editTicket(t, map[string]any{"description": "Changed."})
	check("the ticket edited")
	if it := p.itemOf(t, planRef(1)); !strings.Contains(it.Now, "The approval no longer holds") {
		t.Fatalf("Now: %q", it.Now)
	}
	p.editTicket(t, map[string]any{"description": "It loops."})
	check("the edit undone")
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/withdraw", map[string]any{"opId": "wd-1", "expectedRev": 1}, http.StatusConflict, dispatch.RefusalStaleRevision)
	if s := p.trackOf(t, planRef(1), "wd-1", ""); s[stepValidated].State != stepRefused || !strings.Contains(s[stepValidated].Words, "stale_revision") {
		t.Fatalf("a refused withdrawal: %+v", s)
	}
	// A receipt is the revision's only when it is an approval or a
	// withdrawal of that revision: a ticket's edit, or revision 1's
	// approval and refused withdrawal read under revision 2, are none of
	// its own, recorded refused with the words, whatever they became.
	p.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": "ed-1", "set": map[string]any{"description": "Edited."}}, http.StatusOK, "")
	p.proposed(t, p.saveCrew(t, teamCrew("Team two", "cat")).ID, nil)
	for _, c := range []struct{ ref, op string }{{planRef(1), "ed-1"}, {planRef(2), "ap-1"}, {planRef(2), "wd-1"}} {
		s := p.trackOf(t, c.ref, c.op, "")
		if s[stepRecorded].State != stepRefused || !strings.Contains(s[stepRecorded].Words, "another operation") || s[stepValidated].State != stepNotApplicable || s[stepResumed].State != stepNotApplicable {
			t.Fatalf("%s under %s: %+v", c.op, c.ref, s)
		}
	}
	if s := p.trackOf(t, planRef(1), "ap-1", ""); s[stepValidated].State != stepDone {
		t.Fatalf("its own approval after: %+v", s)
	}
}

// 230 open revisions over five tickets, proposed ones and approvals that
// no longer hold among them, with questions and a failed task's worth of
// items beside them: read page by page from the cursor, every item once,
// in (since, ref) order, total right on every page.
func TestInboxPagesPlans(t *testing.T) {
	p := newPlanTest(t, nil)
	saved := p.saveCrew(t, teamCrew("Team", "cat"))
	keys := []string{"CND-1"}
	for i := 2; i <= 5; i++ {
		p.newDispatchTicket(t, "CND", fmt.Sprintf("Ticket %d", i), map[string]any{"description": "d"})
		keys = append(keys, fmt.Sprintf("CND-%d", i))
	}
	first := p.proposed(t, saved.ID, nil)
	_, body, _ := p.planBodyOf(t, first)
	ctx := context.Background()
	want := map[string]bool{planRef(1): true}
	sessions := 0
	for i := 1; i < 230; i++ {
		key := keys[i%5]
		rc, err := p.srv.dispatch.ProposePlan(ctx, dispatch.Op{Actor: adminActor, ID: fmt.Sprintf("bulk-%d", i), By: "Operator"}, key, dispatch.PlanRequest{Crew: saved.ID}, body)
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("propose %d: %+v %v", i, rc, err)
		}
		v, err := p.srv.dispatch.Plan(ctx, rc.ID)
		if err != nil {
			t.Fatal(err)
		}
		want[dispatch.PlanRef(key, v.N)] = true
		if i < 5 {
			// Each other ticket's first revision approved, then stale.
			if _, err := p.srv.dispatch.ApprovePlan(ctx, dispatch.Op{Actor: adminActor, ID: fmt.Sprintf("ap-%d", i), By: "Operator"}, rc.ID, 0, ""); err != nil {
				t.Fatal(err)
			}
			p.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"description": "changed"}}, http.StatusOK, "")
		}
		if i%40 == 0 {
			fs := newFakeSession(t, p.testEnv, sessionHex(fmt.Sprintf("pages-%d", i)), session.Options{})
			want[questionRef(fs.l.Info().ID, fs.ask(t, "Which?", dbChoices))] = true
			sessions++
		}
	}
	total := 230 + sessions
	seen := map[string]bool{}
	var last *inboxSummary
	after := ""
	for {
		page, n := p.inboxList(t, after)
		if page.Total != total || n > maxInboxReply {
			t.Fatalf("total %d of %d, %d bytes", page.Total, total, n)
		}
		for i := range page.Items {
			s := page.Items[i]
			if seen[s.Ref] || !want[s.Ref] {
				t.Fatalf("%s repeated or unknown", s.Ref)
			}
			if last != nil && (s.Since.Before(last.Since) || (s.Since.Equal(last.Since) && s.Ref <= last.Ref)) {
				t.Fatalf("%s after %s", s.Ref, last.Ref)
			}
			seen[s.Ref], last = true, &page.Items[i]
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(seen) != total {
		t.Fatalf("read %d of %d", len(seen), total)
	}
}
