package dispatch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// closeOfKey reads the close policy's state of the ticket with the key.
func closeOfKey(t *testing.T, s *Store, key string) CloseState {
	t.Helper()
	st, err := s.CloseState(context.Background(), key)
	if err != nil {
		t.Fatalf("the close state of %s: %v", key, err)
	}
	return st
}

// setStatus moves the ticket with the key to the status through
// ticket.edit and answers its receipt.
func setStatus(s *Store, opID, key, status string) (Receipt, error) {
	return edit(s, opID, key, TicketEdit{Set: TicketSet{Status: &status}})
}

func TestAcceptAndUnaccept(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newOpenProject(t, s, "CND")
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Accepted", Owner: "Alice", Acceptance: "It works."})
	_, ownerless := newTicket(t, s, "CND", "Nobody's")
	op := Op{Actor: "admin", ID: "a1", By: "Operator", ExpectedRev: 1}
	rc, err := s.AcceptTicket(ctx, op, key, "Looks right")
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 2 || rc.Intent != "ticket.accept" {
		t.Fatalf("the receipt: %+v", rc)
	}
	tk := ticketOf(t, s, key)
	if tk.AcceptedAt.IsZero() || tk.AcceptedBy != "Operator" || tk.AcceptedFor != "Alice" || tk.AcceptedCriteria != criteriaHash("It works.") ||
		tk.AcceptedNote != "Looks right" || tk.Status != TicketBacklog {
		t.Fatalf("the acceptance: %+v", tk)
	}
	if len(tk.AcceptedCriteria) != 64 {
		t.Fatalf("the criteria's hash: %q", tk.AcceptedCriteria)
	}
	rows := changesOf(t, s, "admin", "a1")
	var fields []string
	for _, r := range rows {
		if r.Op != OpSet || r.Prior != "" || r.Entity != EntityTicket {
			t.Fatalf("an acceptance row: %+v", r)
		}
		fields = append(fields, r.Field)
	}
	if !slices.Equal(fields, acceptanceColumns) {
		t.Fatalf("the acceptance's rows: %v", fields)
	}
	// No status change touches it: open, done, cancelled, reopened.
	for i, status := range []string{TicketInProgress, TicketDone, TicketCancelled, TicketBacklog} {
		rc, err := setStatus(s, opID("s", i), key, status)
		want(t, rc, err, StatusDone, "")
		if got := ticketOf(t, s, key); got.AcceptedAt != tk.AcceptedAt || got.AcceptedFor != "Alice" || got.AcceptedCriteria != tk.AcceptedCriteria {
			t.Fatalf("moved to %s, the acceptance changed: %+v", status, got)
		}
		for _, r := range rowsOf(t, s, opID("s", i)) {
			if strings.Contains(r, "accepted_") {
				t.Fatalf("moved to %s: %v", status, r)
			}
		}
	}
	// Accepted again while closed: allowed, each field that changes a row,
	// the old values the priors.
	rc, err = setStatus(s, "close", key, TicketDone)
	want(t, rc, err, StatusDone, "")
	rc, err = s.AcceptTicket(ctx, testOp("a2"), key, "")
	want(t, rc, err, StatusDone, "")
	if got := rowsOf(t, s, "a2"); len(got) != 3 || !strings.HasPrefix(got[0], "set:accepted_at=") || got[1] != "set:accepted_by=Tester<Operator" || got[2] != "set:accepted_note=<Looks right" {
		t.Fatalf("the second acceptance's rows: %v", got)
	}
	if ticketOf(t, s, key).Status != TicketDone {
		t.Fatal("an acceptance moved the status")
	}
	// With no display name, the actor accepted it, with a note again.
	rc, err = s.AcceptTicket(ctx, Op{Actor: "admin", ID: "a3"}, key, "Still right")
	want(t, rc, err, StatusDone, "")
	if tk := ticketOf(t, s, key); tk.AcceptedBy != "admin" || tk.AcceptedNote != "Still right" {
		t.Fatalf("accepted by %q, the note %q", tk.AcceptedBy, tk.AcceptedNote)
	}
	// Unaccept: every field cleared, the note among them, one row each
	// with its prior; the status stays.
	rc, err = s.UnacceptTicket(ctx, testOp("u1"), key)
	want(t, rc, err, StatusDone, "")
	if got := rowsOf(t, s, "u1"); len(got) != 5 || !strings.HasPrefix(got[0], "set:accepted_at=<") || got[1] != "set:accepted_by=<admin" ||
		got[2] != "set:accepted_for=<Alice" || got[3] != "set:accepted_criteria=<"+criteriaHash("It works.") || got[4] != "set:accepted_note=<Still right" {
		t.Fatalf("the unaccept's rows: %v", got)
	}
	tk = ticketOf(t, s, key)
	if !tk.AcceptedAt.IsZero() || tk.AcceptedBy != "" || tk.AcceptedFor != "" || tk.AcceptedCriteria != "" || tk.AcceptedNote != "" || tk.Status != TicketDone {
		t.Fatalf("unaccepted: %+v", tk)
	}
	rc, err = s.UnacceptTicket(ctx, testOp("u2"), key)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if !strings.Contains(rc.Error, "not accepted") {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	// An acceptance is the owner's: none, none to accept for.
	rc, err = s.AcceptTicket(ctx, testOp("n1"), ownerless, "")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if !strings.Contains(rc.Error, "owner") {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	// The note's rule, a revision gone by, no ticket.
	for i, c := range []struct{ note, refusal string }{
		{strings.Repeat("é", MaxAcceptedNote+1), RefusalBound},
		{"a\x00b", RefusalInvalid},
	} {
		rc, err := s.AcceptTicket(ctx, testOp(opID("note", i)), key, c.note)
		want(t, rc, err, StatusFailed, c.refusal)
	}
	rc, err = s.AcceptTicket(ctx, testOp("note-max"), key, strings.Repeat("é", MaxAcceptedNote-2)+"\n\t")
	want(t, rc, err, StatusDone, "")
	stale := testOp("stale")
	stale.ExpectedRev = 1
	rc, err = s.AcceptTicket(ctx, stale, key, "")
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	rc, err = s.AcceptTicket(ctx, testOp("none"), "CND-99", "")
	want(t, rc, err, StatusFailed, RefusalNotFound)
	rc, err = s.UnacceptTicket(ctx, testOp("none2"), "CND-99")
	want(t, rc, err, StatusFailed, RefusalNotFound)
	// An archived project refuses both.
	if rc, err := s.ArchiveProject(ctx, testOp("archive"), "CND"); err != nil || rc.Status != StatusDone {
		t.Fatalf("archive: %+v %v", rc, err)
	}
	rc, err = s.UnacceptTicket(ctx, testOp("x1"), key)
	want(t, rc, err, StatusFailed, RefusalArchived)
	rc, err = s.AcceptTicket(ctx, testOp("x2"), key, "")
	want(t, rc, err, StatusFailed, RefusalArchived)
}

// done waits for the owner's acceptance (a native project's default
// policy): refused, then allowed once accepted; refused again after the
// owner changes and after the criteria are edited, the page's reason in
// close, allowed after a new acceptance; the gate reads the ticket as the
// edit leaves it.
func TestDoneWaitsForTheOwnersAcceptance(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Gated", Owner: "Alice", Acceptance: "It works."})
	refused := func(opID string, wantWhy string) {
		t.Helper()
		before := ticketOf(t, s, key)
		rc, err := setStatus(s, opID, key, TicketDone)
		want(t, rc, err, StatusFailed, RefusalClosePolicy)
		if !slices.Equal(rc.Missing, []string{RequireAcceptance}) || !strings.Contains(rc.Error, "the owner's acceptance") || !strings.Contains(rc.Error, wantWhy) {
			t.Fatalf("the refusal: %+v", rc)
		}
		if after := ticketOf(t, s, key); after.Status != before.Status || after.Rev != before.Rev || !after.ClosedAt.IsZero() {
			t.Fatalf("a refused close changed the ticket: %+v", after)
		}
	}
	allowed := func(opID string) {
		t.Helper()
		rc, err := setStatus(s, opID, key, TicketDone)
		want(t, rc, err, StatusDone, "")
		if tk := ticketOf(t, s, key); tk.Status != TicketDone || tk.ClosedAt.IsZero() {
			t.Fatalf("closed: %+v", tk)
		}
		rc, err = setStatus(s, opID+"-reopen", key, TicketInReview)
		want(t, rc, err, StatusDone, "")
	}
	accept := func(opID string) {
		t.Helper()
		rc, err := s.AcceptTicket(ctx, testOp(opID), key, "")
		want(t, rc, err, StatusDone, "")
	}
	st := closeOfKey(t, s, key)
	if !slices.Equal(st.Requires, []string{RequireAcceptance}) || st.Met.Acceptance || st.Met.Delivery || !slices.Equal(st.Missing, []string{RequireAcceptance}) || st.Why != "" {
		t.Fatalf("not accepted: %+v", st)
	}
	refused("d1", "")
	accept("a1")
	if st := closeOfKey(t, s, key); !st.Met.Acceptance || len(st.Missing) != 0 || st.Why != "" {
		t.Fatalf("accepted: %+v", st)
	}
	allowed("d2")
	// The owner changed: the acceptance stays recorded, unmet, and says
	// why.
	rc, err := edit(s, "owner", key, TicketEdit{Set: TicketSet{Owner: ptr("Bob")}})
	want(t, rc, err, StatusDone, "")
	if tk := ticketOf(t, s, key); tk.AcceptedFor != "Alice" || tk.AcceptedAt.IsZero() {
		t.Fatalf("the acceptance after the owner's change: %+v", tk)
	}
	st = closeOfKey(t, s, key)
	if st.Met.Acceptance || st.Why != "accepted for Alice; the owner is now Bob" || !slices.Equal(st.Missing, []string{RequireAcceptance}) {
		t.Fatalf("after the owner's change: %+v", st)
	}
	refused("d3", "accepted for Alice; the owner is now Bob")
	accept("a2")
	allowed("d4")
	// The criteria edited: unmet, and says why.
	rc, err = edit(s, "criteria", key, TicketEdit{Set: TicketSet{Acceptance: ptr("It works, and fast.")}})
	want(t, rc, err, StatusDone, "")
	if st := closeOfKey(t, s, key); st.Met.Acceptance || st.Why != "accepted before the criteria changed" {
		t.Fatalf("after the criteria's edit: %+v", st)
	}
	refused("d5", "accepted before the criteria changed")
	accept("a3")
	allowed("d6")
	// The gate reads the ticket as the edit leaves it: a new owner or new
	// criteria set with done in one edit are refused, and nothing of the
	// edit is written.
	for i, e := range []TicketEdit{
		{Set: TicketSet{Owner: ptr("Carol"), Status: ptr(TicketDone)}},
		{Set: TicketSet{Acceptance: ptr("Something else."), Status: ptr(TicketDone)}},
	} {
		rc, err := edit(s, opID("together", i), key, e)
		want(t, rc, err, StatusFailed, RefusalClosePolicy)
	}
	if tk := ticketOf(t, s, key); tk.Owner != "Bob" || tk.Acceptance != "It works, and fast." {
		t.Fatalf("a refused edit wrote: %+v", tk)
	}
	// Both changed: both reasons; the owner cleared: said so.
	rc, err = edit(s, "both", key, TicketEdit{Set: TicketSet{Owner: ptr("Dan"), Acceptance: ptr("New.")}})
	want(t, rc, err, StatusDone, "")
	if st := closeOfKey(t, s, key); st.Why != "accepted for Bob; the owner is now Dan; accepted before the criteria changed" {
		t.Fatalf("both changed: %q", st.Why)
	}
	rc, err = edit(s, "noowner", key, TicketEdit{Set: TicketSet{Owner: ptr(""), Acceptance: ptr("It works, and fast.")}})
	want(t, rc, err, StatusDone, "")
	if st := closeOfKey(t, s, key); st.Why != "accepted for Bob; the ticket has no owner now" {
		t.Fatalf("no owner: %q", st.Why)
	}
	// A done ticket stays done whatever changes after: the gate is on
	// entering done; any other move is free, cancelled included.
	rc, err = edit(s, "dan", key, TicketEdit{Set: TicketSet{Owner: ptr("Dan")}})
	want(t, rc, err, StatusDone, "")
	accept("a4")
	rc, err = setStatus(s, "d7", key, TicketDone)
	want(t, rc, err, StatusDone, "")
	rc, err = edit(s, "after", key, TicketEdit{Set: TicketSet{Owner: ptr("Eve"), Title: ptr("Renamed")}})
	want(t, rc, err, StatusDone, "")
	if tk := ticketOf(t, s, key); tk.Status != TicketDone {
		t.Fatalf("a done ticket reopened by an owner's change: %+v", tk)
	}
	rc, err = s.UnacceptTicket(ctx, testOp("u1"), key)
	want(t, rc, err, StatusDone, "")
	if tk := ticketOf(t, s, key); tk.Status != TicketDone {
		t.Fatal("an unaccept moved the status")
	}
	_, other := makeTicket(t, s, NewTicket{Project: "CND", Title: "Dropped", Owner: "Alice"})
	for i, status := range []string{TicketReady, TicketInProgress, TicketInReview, TicketCancelled, TicketBacklog} {
		rc, err := setStatus(s, opID("free", i), other, status)
		want(t, rc, err, StatusDone, "")
	}
}

// done waits for a delivery link where the policy requires one: refused,
// then allowed once a live delivery link is there (another kind counts
// for nothing, and so does another ticket's delivery link); a policy of
// both names both missing, in order.
func TestDoneWaitsForADeliveryLink(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	s.SetRunExists(runs("run1"))
	if rc, err := s.CreateProject(ctx, testOp("p1"), NewProject{Key: "DEL", Name: "Delivered", CloseRequires: &[]string{RequireDelivery}}); err != nil || rc.Status != StatusDone {
		t.Fatalf("the project: %+v %v", rc, err)
	}
	_, key := newTicket(t, s, "DEL", "Shipped")
	rc, err := setStatus(s, "d1", key, TicketDone)
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if !slices.Equal(rc.Missing, []string{RequireDelivery}) || !strings.Contains(rc.Error, "a delivery link") {
		t.Fatalf("the refusal: %+v", rc)
	}
	// An external link and a run link are not a delivery.
	rc, err = addLink(s, "l1", key, NewLink{Kind: LinkExternal, URL: "https://example.com/issues/1"})
	want(t, rc, err, StatusDone, "")
	rc, err = addLink(s, "l2", key, NewLink{Kind: LinkRun, Ref: "run1"})
	want(t, rc, err, StatusDone, "")
	// Another ticket's delivery link is that ticket's: it meets that
	// ticket's policy and leaves this one's unmet.
	_, elsewhere := newTicket(t, s, "DEL", "Shipped elsewhere")
	rc, err = addLink(s, "l-elsewhere", elsewhere, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/8"})
	want(t, rc, err, StatusDone, "")
	if st := closeOfKey(t, s, elsewhere); !st.Met.Delivery || len(st.Missing) != 0 {
		t.Fatalf("the other ticket's own link: %+v", st)
	}
	if st := closeOfKey(t, s, key); st.Met.Delivery || !slices.Equal(st.Missing, []string{RequireDelivery}) {
		t.Fatalf("another ticket's link met this one's policy: %+v", st)
	}
	rc, err = setStatus(s, "d2", key, TicketDone)
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if !slices.Equal(rc.Missing, []string{RequireDelivery}) {
		t.Fatalf("the refusal with another ticket's link: %+v", rc)
	}
	// A merge is a delivery link a person adds.
	rc, err = addLink(s, "l3", key, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9"})
	want(t, rc, err, StatusDone, "")
	delivery := rc.ID
	if st := closeOfKey(t, s, key); !st.Met.Delivery || st.Met.Acceptance || len(st.Missing) != 0 {
		t.Fatalf("delivered: %+v", st)
	}
	rc, err = setStatus(s, "d3", key, TicketDone)
	want(t, rc, err, StatusDone, "")
	// The link removed: unmet on read, the ticket left done.
	rc, err = s.RemoveLink(ctx, testOp("x1"), key, delivery)
	want(t, rc, err, StatusDone, "")
	if st := closeOfKey(t, s, key); st.Met.Delivery || !slices.Equal(st.Missing, []string{RequireDelivery}) {
		t.Fatalf("the delivery removed: %+v", st)
	}
	if ticketOf(t, s, key).Status != TicketDone {
		t.Fatal("removing a link moved the status")
	}
	// Both required: both missing, acceptance first.
	if rc, err := s.CreateProject(ctx, testOp("p2"), NewProject{Key: "BTH", Name: "Both", CloseRequires: &[]string{RequireDelivery, RequireAcceptance}}); err != nil || rc.Status != StatusDone {
		t.Fatalf("the project: %+v %v", rc, err)
	}
	_, both := makeTicket(t, s, NewTicket{Project: "BTH", Title: "Both", Owner: "Alice"})
	rc, err = setStatus(s, "b1", both, TicketDone)
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if !slices.Equal(rc.Missing, []string{RequireAcceptance, RequireDelivery}) || !strings.Contains(rc.Error, "the owner's acceptance and a delivery link") {
		t.Fatalf("both missing: %+v", rc)
	}
	rc, err = s.AcceptTicket(ctx, testOp("a1"), both, "")
	want(t, rc, err, StatusDone, "")
	rc, err = setStatus(s, "b2", both, TicketDone)
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if !slices.Equal(rc.Missing, []string{RequireDelivery}) {
		t.Fatalf("delivery missing: %+v", rc)
	}
	rc, err = addLink(s, "l4", both, NewLink{Kind: LinkDelivery, URL: "https://example.com/release/1"})
	want(t, rc, err, StatusDone, "")
	rc, err = setStatus(s, "b3", both, TicketDone)
	want(t, rc, err, StatusDone, "")
}

// A policy that requires nothing lets done at once, a create made done
// among it; one that requires something refuses a create made done, and
// allocates no number for it.
func TestAPolicyOfNothingClosesAtOnceAndACreateIsGated(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newOpenProject(t, s, "OPN")
	newProject(t, s, "CND")
	if rc, err := s.CreateProject(ctx, testOp("ext"), NewProject{Key: "EXT", Name: "External", Tracker: TrackerExternal, TrackerURL: "https://github.com/x/y/issues"}); err != nil || rc.Status != StatusDone {
		t.Fatalf("the external project: %+v %v", rc, err)
	}
	for _, project := range []string{"OPN", "EXT"} {
		_, key := newTicket(t, s, project, "Open")
		rc, err := setStatus(s, "done-"+project, key, TicketDone)
		want(t, rc, err, StatusDone, "")
		rc, err = s.CreateTicket(ctx, testOp("made-"+project), NewTicket{Project: project, Title: "Made done", Status: TicketDone})
		want(t, rc, err, StatusDone, "")
		if st := closeOfKey(t, s, rc.Key); len(st.Requires) != 0 || len(st.Missing) != 0 {
			t.Fatalf("%s's close state: %+v", project, st)
		}
	}
	rc, err := s.CreateTicket(ctx, testOp("made-CND"), NewTicket{Project: "CND", Title: "Made done", Owner: "Alice", Status: TicketDone})
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if !slices.Equal(rc.Missing, []string{RequireAcceptance}) || !strings.Contains(rc.Error, "a new ticket") {
		t.Fatalf("the refusal: %+v", rc)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM tickets WHERE project_id = (SELECT id FROM projects WHERE key = 'CND')"); n != 0 {
		t.Fatalf("%d tickets made", n)
	}
	if _, key := newTicket(t, s, "CND", "First"); key != "CND-1" {
		t.Fatalf("a refused create allocated a number: %s", key)
	}
	// Cancelled needs nothing.
	rc, err = s.CreateTicket(ctx, testOp("cancelled"), NewTicket{Project: "CND", Title: "Dropped", Status: TicketCancelled})
	want(t, rc, err, StatusDone, "")
}

// The close policy's state is computed on every read and stored nowhere:
// the project's policy edited later reads true at once, on every ticket,
// a deleted one's included.
func TestTheCloseStateIsComputedOnRead(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Read", Owner: "Alice"})
	if st := closeOfKey(t, s, key); !slices.Equal(st.Missing, []string{RequireAcceptance}) {
		t.Fatalf("before: %+v", st)
	}
	rc, err := s.EditProject(ctx, testOp("e1"), "CND", ProjectEdit{CloseRequires: &[]string{RequireDelivery}})
	want(t, rc, err, StatusDone, "")
	if st := closeOfKey(t, s, key); !slices.Equal(st.Requires, []string{RequireDelivery}) || !slices.Equal(st.Missing, []string{RequireDelivery}) {
		t.Fatalf("after the policy's edit: %+v", st)
	}
	rc, err = s.EditProject(ctx, testOp("e2"), "CND", ProjectEdit{CloseRequires: &[]string{}})
	want(t, rc, err, StatusDone, "")
	if st := closeOfKey(t, s, key); len(st.Requires) != 0 || len(st.Missing) != 0 || st.Requires == nil || st.Missing == nil {
		t.Fatalf("after the policy's clearing: %#v", st)
	}
	rc, err = s.DeleteTicket(ctx, testOp("d1"), key)
	want(t, rc, err, StatusDone, "")
	if st := closeOfKey(t, s, key); len(st.Missing) != 0 {
		t.Fatalf("a deleted ticket's: %+v", st)
	}
	// Nothing of the state is a column: the ticket's columns are the
	// schema's, the acceptance's facts among them and no close.
	cols := texts(t, s, "SELECT name FROM pragma_table_info('tickets')")
	for _, c := range cols {
		if (strings.Contains(c, "close") && c != "closed_at") || strings.Contains(c, "blocked") || strings.Contains(c, "met") {
			t.Fatalf("a stored state: %s", c)
		}
	}
	if _, err := s.CloseState(ctx, "CND-99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no ticket: %v", err)
	}
}

// verified is met by a live delivery whose recorded receipt passed, and
// unmet by one whose receipt failed, by a delivery that names no
// candidate and by none, each with its words, which the close_policy
// refusal carries; a passed delivery removed leaves it unmet on read, the
// ticket left done. Required with delivery, a passed candidate meets both.
func TestClosePolicyVerified(t *testing.T) {
	s, _, _ := planTicket(t)
	ctx := context.Background()
	if rc, err := s.CreateProject(ctx, testOp("p1"), NewProject{Key: "VER", Name: "Verified", CloseRequires: &[]string{RequireVerified, RequireDelivery}}); err != nil || rc.Status != StatusDone {
		t.Fatalf("the project: %+v %v", rc, err)
	}
	if p, err := s.Project(ctx, "VER"); err != nil || !slices.Equal(p.Project.CloseRequires, []string{RequireDelivery, RequireVerified}) {
		t.Fatalf("the policy: %+v %v", p, err)
	}
	_, key := makeTicket(t, s, NewTicket{Project: "VER", Title: "Verified", Description: "d", Acceptance: "- it works"})
	l := mustLaunch(t, s, "l1", key, simplePlan(t, s, key), "")
	s.SetRunExists(runs(l.RunID))
	st := closeOfKey(t, s, key)
	if st.Met.Verified || st.Met.Delivery || !slices.Equal(st.Missing, []string{RequireDelivery, RequireVerified}) || st.VerifiedWhy != "no delivery names a candidate" || st.Why != "" {
		t.Fatalf("nothing delivered: %+v", st)
	}
	// A delivery naming no candidate: delivered, not verified.
	rc, err := addLink(s, "plain", key, NewLink{Kind: LinkDelivery, URL: "https://example.com/pull/1"})
	want(t, rc, err, StatusDone, "")
	st = closeOfKey(t, s, key)
	if st.Met.Verified || !st.Met.Delivery || !slices.Equal(st.Missing, []string{RequireVerified}) || st.VerifiedWhy != "no delivery names a candidate" {
		t.Fatalf("a delivery with no candidate: %+v", st)
	}
	rc, err = setStatus(s, "d1", key, TicketDone)
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if !slices.Equal(rc.Missing, []string{RequireVerified}) || !strings.Contains(rc.Error, "it lacks a verified delivery (no delivery names a candidate)") {
		t.Fatalf("the refusal: %+v", rc)
	}
	// A candidate that failed its check: the words name it.
	failed := passedReceipt(1)
	failed.Passed, failed.Receipts[0].Exit = false, exit(1)
	rc, err = addDelivery(s, "failed", key, "https://example.com/pull/2", l.RunID+"/build/1@"+commitA, failed)
	want(t, rc, err, StatusDone, "")
	st = closeOfKey(t, s, key)
	if st.Met.Verified || st.VerifiedWhy != "the delivered candidate build#1 failed the check test (exit 1)" {
		t.Fatalf("a failed candidate: %+v", st)
	}
	rc, err = setStatus(s, "d2", key, TicketDone)
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if !strings.Contains(rc.Error, "the delivered candidate build#1 failed the check test") {
		t.Fatalf("the refusal: %+v", rc)
	}
	// The other ways a candidate falls short, in words.
	for _, c := range []struct {
		v    Verified
		want string
	}{
		{Verified{Attempt: 1}, "ran no check"},
		{Verified{Attempt: 1, Receipts: []VerifiedReceipt{{Phase: PhaseVerify, ID: "lint", Where: "native"}}}, "did not finish the verify step lint"},
		{Verified{Attempt: 1, Receipts: []VerifiedReceipt{{Phase: PhaseCheck, ID: "test", Where: "native", Exit: exit(0)}}}, "passed the check test but not stably"},
		{Verified{Attempt: 1, Receipts: []VerifiedReceipt{{Phase: PhaseCheck, ID: "test", Where: "native", Exit: exit(0), Stable: true}}}, "passed its checks, but its attempt did not succeed"},
	} {
		if got := shortfall(c.v); got != c.want {
			t.Errorf("%+v: %q, want %q", c.v, got, c.want)
		}
	}
	if met, why := verifiedState([]delivery{{candidate: l.RunID + "/build/1@" + commitA, verified: "{"}}); met || !strings.Contains(why, "cannot be read") {
		t.Fatalf("a receipt that cannot be read: %v %q", met, why)
	}
	// A candidate that passed: met, and done is taken.
	rc, err = addDelivery(s, "passed", key, "https://example.com/pull/3", l.RunID+"/build/2@"+commitA, passedReceipt(2))
	want(t, rc, err, StatusDone, "")
	passed := rc.ID
	st = closeOfKey(t, s, key)
	if !st.Met.Verified || !st.Met.Delivery || len(st.Missing) != 0 || st.VerifiedWhy != "" {
		t.Fatalf("a passed candidate: %+v", st)
	}
	rc, err = setStatus(s, "d3", key, TicketDone)
	want(t, rc, err, StatusDone, "")
	// Removed: unmet on read, the words the failed candidate's again, the
	// ticket left done.
	rc, err = s.RemoveLink(ctx, testOp("x1"), key, passed)
	want(t, rc, err, StatusDone, "")
	st = closeOfKey(t, s, key)
	if st.Met.Verified || !slices.Equal(st.Missing, []string{RequireVerified}) || !strings.Contains(st.VerifiedWhy, "build#1 failed") {
		t.Fatalf("the passed delivery removed: %+v", st)
	}
	if ticketOf(t, s, key).Status != TicketDone {
		t.Fatal("removing a link moved the status")
	}
	// The ticket view carries it.
	v, err := s.Ticket(ctx, key)
	if err != nil || v.Close.Met.Verified || v.Close.VerifiedWhy == "" {
		t.Fatalf("the ticket's close: %+v %v", v.Close, err)
	}
	// Verified alone required: a create made done is refused for it.
	if rc, err := s.CreateProject(ctx, testOp("p2"), NewProject{Key: "VO", Name: "Verified only", CloseRequires: &[]string{RequireVerified}}); err != nil || rc.Status != StatusDone {
		t.Fatalf("the project: %+v %v", rc, err)
	}
	rc, err = s.CreateTicket(ctx, testOp("c1"), NewTicket{Project: "VO", Title: "Made done", Status: TicketDone})
	want(t, rc, err, StatusFailed, RefusalClosePolicy)
	if !slices.Equal(rc.Missing, []string{RequireVerified}) {
		t.Fatalf("a create made done: %+v", rc)
	}
}
