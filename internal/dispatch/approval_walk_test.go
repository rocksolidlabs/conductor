package dispatch

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// forged is a revision's field row as another machine (c1) would hand it:
// a's own row of the field as a template, its value, its date and a new
// identity.
func forged(t *testing.T, a *Store, id, field, value string, at time.Time, n int) ChangeRow {
	t.Helper()
	rows, _, err := a.ChangesFrom(context.Background(), 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rows, func(c ChangeRow) bool { return c.Entity == EntityPlan && c.EntityID == id })
	if i < 0 {
		t.Fatalf("no row of revision %s on a", id)
	}
	c := rows[i]
	c.Op, c.ViewDigest, c.Remote = OpSet, "", false
	c.Field, c.Value, c.Prior, c.At, c.Origin, c.Actor, c.OpID, c.N = field, value, "", at, "c1", "admin", fmt.Sprintf("forged-%d", n), 0
	return c
}

// apply hands rows to s as another machine's batch, failing the test on
// a refusal.
func apply(t *testing.T, s *Store, keys map[string]string, rows ...ChangeRow) {
	t.Helper()
	if _, err := s.ApplyRemote(context.Background(), rows, nil, keys); err != nil {
		t.Fatal(err)
	}
}

// One approval field from elsewhere dated far ahead, then a re-approval
// here: a later approval from elsewhere to a changed intent still reads as
// elsewhere, as does one sent without the far field.
func TestAFarDatedApprovalFieldNeverMakesAnotherApprovalLocal(t *testing.T) {
	ctx := context.Background()
	for _, withB := range []bool{true, false} {
		a, b, key, after := planPair(t)
		id := propose(t, a, "propose", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
		approve(t, a, "approve", id, "mine")
		pull(t, a, b, after)
		keys := registryKeys(t, a)
		apply(t, a, keys, forged(t, a, id, "approved_note", "far", time.Date(9000, 1, 1, 0, 0, 0, 0, time.UTC), 1))
		approve(t, a, "approve-again", id, "mine again")
		if withB {
			title := "Fix the login loop for good"
			mustEdit(t, b, "edit-on-b", key, TicketEdit{Set: TicketSet{Title: &title}})
			approve(t, b, "approve-on-b", id, "b's")
			var back int64
			pull(t, b, a, &back)
		} else {
			intent := IntentDigest(ticketOf(t, a, key))
			later := time.Now().Add(time.Hour)
			apply(t, a, keys, forged(t, a, id, "approved_intent", intent, later, 2), forged(t, a, id, "approved_rev", "1", later, 3),
				forged(t, a, id, "approved_at", stamp(later), later, 4))
		}
		rc, err := a.Launch(ctx, testOp("launch"), key, id, "")
		want(t, rc, err, StatusFailed, RefusalApprovedElsewhere)
	}
}

// A lone state row from elsewhere putting back a revision this machine
// withdrew launches nothing; this machine's own approval after it does.
func TestARemoteStateRowAloneDoesNotLaunchAWithdrawnRevision(t *testing.T) {
	ctx := context.Background()
	a, _, key, _ := planPair(t)
	id := propose(t, a, "propose", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	approve(t, a, "approve", id, "mine")
	if rc, err := a.WithdrawPlan(ctx, adminOp("withdraw"), id); err != nil || rc.Status != StatusDone {
		t.Fatalf("the withdrawal: %+v %v", rc, err)
	}
	apply(t, a, registryKeys(t, a), forged(t, a, id, "state", PlanApproved, time.Now().Add(time.Hour), 1))
	rc, err := a.Launch(ctx, testOp("launch"), key, id, "")
	want(t, rc, err, StatusFailed, RefusalApprovedElsewhere)
	approve(t, a, "approve-again", id, "mine again")
	mustLaunch(t, a, "launch-again", key, id, "")
}

// A re-approval made after a withdrawal on another machine is the newest
// state on both machines.
func TestAReapprovalAfterAWithdrawalElsewhereWins(t *testing.T) {
	ctx := context.Background()
	a, b, key, after := planPair(t)
	id := propose(t, a, "propose", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	approve(t, a, "approve", id, "mine")
	pull(t, a, b, after)
	if rc, err := a.WithdrawPlan(ctx, adminOp("withdraw"), id); err != nil || rc.Status != StatusDone {
		t.Fatalf("the withdrawal: %+v %v", rc, err)
	}
	later := time.Now().Add(time.Minute)
	b.clock = func() time.Time { return later }
	// b reads the approval as a's and approves it again.
	approve(t, b, "approve-on-b", id, "b's")
	pull(t, a, b, after)
	var back int64
	pull(t, b, a, &back)
	if sa, sb := planOf(t, a, id).State, planOf(t, b, id).State; sa != PlanApproved || sb != PlanApproved {
		t.Fatalf("after a's withdrawal and b's later approval: a %s, b %s", sa, sb)
	}
}

// Over two machines, in any order of approvals, withdrawals, re-approvals,
// edits, exchanges and rows from elsewhere forged for each approval field
// and for the state at an earlier, a later and a far date, a launch here
// succeeds only when this machine's operator approved the intent it
// launches, and withdrew nothing since. Every walk begins with a field
// from elsewhere at a far date, each walk's own: a re-approval here
// stamps that field past it, and an edit, an approval and an exchange from
// the other machine then leave the newest approval elsewhere, though the
// newest of all its rows is this machine's far-stamped field.
func TestALaunchRestsOnThisMachinesOwnApproval(t *testing.T) {
	fields := []string{"approved_at", "approved_by", "approved_note", "approved_rev", "approved_intent", "state"}
	far := time.Date(9000, 1, 1, 0, 0, 0, 0, time.UTC)
	launched, refused := 0, 0
	for seed := uint64(1); seed <= 12; seed++ {
		ctx := context.Background()
		r := rand.New(rand.NewPCG(seed, seed+1))
		a, b, key, after := planPair(t)
		id := propose(t, a, "propose", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
		pull(t, a, b, after)
		var back int64
		approvedHere := "" // the intent a's operator approved, "" after a withdrawal here
		n := 0
		forge := func(f string, at time.Time) {
			value := map[string]string{"approved_intent": IntentDigest(ticketOf(t, a, key)), "state": PlanApproved, "approved_rev": "1"}[f]
			if value == "" {
				value = "forged"
			}
			apply(t, a, registryKeys(t, a), forged(t, a, id, f, value, at, n))
		}
		check := func(step int) {
			rc, err := a.Launch(ctx, testOp(fmt.Sprint("launch-", n)), key, id, "")
			if err == nil && rc.Status == StatusDone && (approvedHere == "" || approvedHere != IntentDigest(ticketOf(t, a, key))) {
				t.Fatalf("seed %d step %d: a launched on %q, a's operator approved %q", seed, step, IntentDigest(ticketOf(t, a, key)), approvedHere)
			}
			switch {
			case err == nil && rc.Status == StatusDone:
				launched++
			case err == nil && rc.Refusal == RefusalApprovedElsewhere:
				refused++
			}
		}
		// The walk's first steps: a's approval, which b receives; a field
		// from elsewhere at a far date and a's approval again, which b does
		// not; b's edit and approval, and b's rows to a.
		n++
		if rc, err := a.ApprovePlan(ctx, adminOp("a-approve-first"), id, 0, "a"); err != nil || rc.Status != StatusDone {
			t.Fatalf("seed %d: a's first approval: %+v %v", seed, rc, err)
		}
		pull(t, a, b, after)
		forge(fields[r.IntN(len(fields)-1)], far.Add(time.Duration(seed)*time.Hour))
		if rc, err := a.ApprovePlan(ctx, adminOp("a-approve-again"), id, 0, "a"); err == nil && rc.Status == StatusDone {
			approvedHere = IntentDigest(ticketOf(t, a, key))
		}
		title := fmt.Sprint("The title of walk ", seed)
		b.EditTicket(ctx, testOp("b-edit-first"), key, TicketEdit{Set: TicketSet{Title: &title}})
		b.ApprovePlan(ctx, adminOp("b-approve-first"), id, 0, "b")
		pull(t, b, a, &back)
		check(-1)
		for step := range 40 {
			n++
			switch k := r.IntN(8); k {
			case 0:
				if rc, err := a.ApprovePlan(ctx, adminOp(fmt.Sprint("a-approve-", n)), id, 0, "a"); err == nil && rc.Status == StatusDone {
					approvedHere = IntentDigest(ticketOf(t, a, key))
				}
			case 1:
				if rc, err := a.WithdrawPlan(ctx, adminOp(fmt.Sprint("a-withdraw-", n)), id); err == nil && rc.Status == StatusDone {
					approvedHere = ""
				}
			case 2:
				b.ApprovePlan(ctx, adminOp(fmt.Sprint("b-approve-", n)), id, 0, "b")
			case 3:
				b.WithdrawPlan(ctx, adminOp(fmt.Sprint("b-withdraw-", n)), id)
			case 4:
				title := fmt.Sprint("A title of step ", step)
				b.EditTicket(ctx, testOp(fmt.Sprint("b-edit-", n)), key, TicketEdit{Set: TicketSet{Title: &title}})
			case 5:
				pull(t, a, b, after)
				pull(t, b, a, &back)
			default:
				at := []time.Time{time.Now().Add(-time.Hour), time.Now().Add(time.Hour), far.Add(time.Duration(n) * time.Second)}[r.IntN(3)]
				forge(fields[r.IntN(len(fields))], at)
			}
			check(step)
		}
	}
	// The walk met both: launches of this machine's own approval, and
	// launches refused as approved elsewhere.
	if launched == 0 || refused == 0 {
		t.Fatalf("%d launches, %d refused approved elsewhere", launched, refused)
	}
}
