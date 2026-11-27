package dispatch

import (
	"context"
	"testing"
)

// I11 (Program item CP1): the launch of a comparison's revision repeats
// the count of candidates it starts. The count is the operation's content
// with the key, the revision and the label: a resend with the same count
// answers the same launch, one with another is op_conflict; a launch of a
// comparison's revision without it, or with another, is refused
// confirm_candidates in words that say the count, and a count with any
// other revision is invalid, each with its receipt kept and no launch made.
// A launch's fingerprint made before the count existed is unchanged.
func TestACompareLaunchSaysHowMany(t *testing.T) {
	// A fingerprint made before: the arguments' JSON without the count.
	before, err := fingerprint(IntentTicketLaunch, struct {
		Key   string `json:"key"`
		Plan  string `json:"plan"`
		Label string `json:"label,omitempty"`
	}{"CND-1", "0123456789abcdef", "a label"})
	if err != nil {
		t.Fatal(err)
	}
	if now, _ := fingerprint(IntentTicketLaunch, LaunchArgs{Key: "CND-1", Plan: "0123456789abcdef", Label: "a label"}); now != before {
		t.Fatalf("a fingerprint without a count changed: %s, was %s", now, before)
	}
	if counted, _ := fingerprint(IntentTicketLaunch, LaunchArgs{Key: "CND-1", Plan: "0123456789abcdef", Label: "a label", Candidates: 3}); counted == before {
		t.Fatal("the count is not in the fingerprint")
	}

	s, _, key := planTicket(t)
	ctx := context.Background()
	compare := map[string]any{"criteria": []map[string]any{{"id": "fast", "text": "It is fast."}},
		"candidates": []map[string]any{{"task": "approach-a"}, {"task": "approach-b"}, {"task": "approach-c"}}}
	body := testPlanBody("cmp")
	body.Crew = testCrew("cmp", map[string]any{"compare": compare})
	cmpPlan := approvedPlan(t, s, key, PlanRequest{Crew: "cmp"}, body, "go")
	launches := func() int { return count(t, s, "SELECT COUNT(*) FROM launches") }

	for i, n := range []int{0, 2, 4} {
		rc, err := s.LaunchAdmitted(ctx, testOp(opID("c", i)), key, cmpPlan, "", n, nil)
		want(t, rc, err, StatusFailed, RefusalConfirmCandidates)
		if rc.Error != "this revision starts 3 candidates: send candidates 3 to launch them" {
			t.Fatalf("%d candidates: %q", n, rc.Error)
		}
		// Its receipt kept: the same op id answers it again.
		again, err := s.LaunchAdmitted(ctx, testOp(opID("c", i)), key, cmpPlan, "", n, nil)
		want(t, again, err, StatusFailed, RefusalConfirmCandidates)
	}
	// Launch, which sends no count, is refused the same way.
	rc, err := s.Launch(ctx, testOp("plain-launch"), key, cmpPlan, "")
	want(t, rc, err, StatusFailed, RefusalConfirmCandidates)
	if n := launches(); n != 0 {
		t.Fatalf("%d launches after the refusals", n)
	}

	rc, err = s.LaunchAdmitted(ctx, testOp("l3"), key, cmpPlan, "", 3, nil)
	want(t, rc, err, StatusDone, "")
	first := rc.ID
	rc, err = s.LaunchAdmitted(ctx, testOp("l3"), key, cmpPlan, "", 3, nil)
	want(t, rc, err, StatusDone, "")
	if !rc.Repeat || rc.ID != first {
		t.Fatalf("the same launch resent: %+v", rc)
	}
	rc, err = s.LaunchAdmitted(ctx, testOp("l3"), key, cmpPlan, "", 2, nil)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if n := launches(); n != 1 {
		t.Fatalf("%d launches, want 1", n)
	}

	// A revision of no comparison takes no count.
	plain := simplePlan(t, s, key)
	rc, err = s.LaunchAdmitted(ctx, testOp("p1"), key, plain, "", 1, nil)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if rc.Error != "candidates is sent with the launch of a trial: revision 2 starts none" {
		t.Fatalf("a count on an ordinary revision: %q", rc.Error)
	}
	rc, err = s.Launch(ctx, testOp("p2"), key, plain, "")
	want(t, rc, err, StatusDone, "")
}
