package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// rowsOf is an operation's change rows as op:field=value<prior, by n.
func rowsOf(t *testing.T, s *Store, opID string) []string {
	t.Helper()
	var out []string
	for _, row := range changesOf(t, s, "test", opID) {
		out = append(out, row.Op+":"+row.Field+"="+row.Value+"<"+row.Prior)
	}
	return out
}

// chainOf makes n tickets of the project, each the parent of the next,
// and returns their keys, the root first.
func chainOf(t *testing.T, s *Store, project string, n int, title string) []string {
	t.Helper()
	var keys []string
	parent := ""
	for i := range n {
		_, key := makeTicket(t, s, NewTicket{Project: project, Title: fmt.Sprintf("%s %d", title, i+1), Parent: parent})
		keys = append(keys, key)
		parent = key
	}
	return keys
}

// edit sends one ticket.edit and answers its receipt.
func edit(s *Store, opID, key string, e TicketEdit) (Receipt, error) {
	return s.EditTicket(context.Background(), testOp(opID), key, e)
}

func TestCreateATicketWithEveryField(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	pid := newOpenProject(t, s, "CND")
	parentID, parentKey := newTicket(t, s, "CND", "Parent")
	l1, l2 := insertLabel(t, s, pid, "api", false), insertLabel(t, s, pid, "ui", false)
	m := insertMilestone(t, s, pid, "1.0", false)
	n := NewTicket{Project: "CND", Title: "Every field", Description: "## What\n\n- a\n\t- b", Acceptance: "It works.", Status: TicketReady, Priority: 3,
		Horizon: HorizonNext, Owner: "Alice", Assignees: []string{"person:Alice", "agent:claude"}, Labels: []string{l1, l2}, Milestone: m, Parent: parentKey}
	op := testOp("c1")
	op.By = "Alice B"
	rc, err := s.CreateTicket(ctx, op, n)
	want(t, rc, err, StatusDone, "")
	if rc.Key != "CND-2" || len(rc.ID) != IDLen || rc.Rev != 1 || rc.Intent != "ticket.create" {
		t.Fatalf("the receipt: %+v", rc)
	}
	first := rc
	tk := ticketOf(t, s, "CND-2")
	labels := []string{l1, l2}
	slices.Sort(labels)
	if tk.ID != rc.ID || tk.ProjectID != pid || tk.Number != 2 || tk.Key != "CND-2" || tk.Title != n.Title || tk.Description != n.Description ||
		tk.Acceptance != n.Acceptance || tk.Status != TicketReady || tk.Priority != 3 || tk.Horizon != HorizonNext || tk.Owner != "Alice" ||
		tk.ParentID != parentID || tk.MilestoneID != m || !slices.Equal(tk.Assignees, []string{"agent:claude", "person:Alice"}) || !slices.Equal(tk.Labels, labels) ||
		tk.CreatedBy != "Alice B" || tk.Source != "native" || tk.CreatedAt.IsZero() || !tk.ClosedAt.IsZero() || !tk.DeletedAt.IsZero() || tk.Rev != 1 ||
		!tk.AcceptedAt.IsZero() || tk.AcceptedBy != "" || tk.AcceptedFor != "" || tk.AcceptedCriteria != "" || tk.AcceptedNote != "" ||
		tk.ExternalID != "" || tk.ExternalURL != "" || tk.ExternalSnapshot != "" || tk.Authority != "" {
		t.Fatalf("the ticket: %+v", tk)
	}
	// One create row with what never changes, one set row per field the
	// schema's default does not hold, one add row per member, in that
	// order, each naming the ticket and its project; then the one person
	// it names that the directory lacked, Alice, owner and assignee both.
	rows := changesOf(t, s, "test", "c1")
	for i, row := range rows[:len(rows)-1] {
		if row.N != i || row.Entity != EntityTicket || row.EntityID != tk.ID || row.TicketID != tk.ID || row.ProjectID != pid || row.By != "Alice B" || row.Actor != "test" {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	if last := rows[len(rows)-1]; last.N != len(rows)-1 || last.Entity != EntityPerson || last.EntityID != "Alice" || last.Op != OpCreate || last.TicketID != "" || last.ProjectID != "" {
		t.Fatalf("the person's row: %+v", last)
	}
	got := rowsOf(t, s, "c1")
	if want := []string{"create:=" + rows[0].Value + "<", "set:title=Every field<", "set:description=" + n.Description + "<", "set:acceptance=It works.<",
		"set:status=ready<backlog", "set:priority=3<0", "set:horizon=next<", "set:owner=Alice<", "set:parent_id=" + parentID + "<", "set:milestone_id=" + m + "<",
		"add:assignees=person:Alice<", "add:assignees=agent:claude<", "add:labels=" + l1 + "<", "add:labels=" + l2 + "<",
		"create:=" + rows[len(rows)-1].Value + "<"}; !slices.Equal(got, want) {
		t.Fatalf("the rows:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var created ticketCreated
	if err := json.Unmarshal([]byte(rows[0].Value), &created); err != nil || created.ID != tk.ID || created.ProjectID != pid || created.Number != 2 ||
		created.Key != "CND-2" || created.CreatedBy != "Alice B" || !created.CreatedAt.Equal(tk.CreatedAt) {
		t.Fatalf("the create row's value: %s %v", rows[0].Value, err)
	}
	// Made done or cancelled, it is closed at once (the project's close
	// policy requires nothing, so done is allowed at once).
	for i, status := range []string{TicketDone, TicketCancelled} {
		rc, err := s.CreateTicket(ctx, testOp(opID("closed", i)), NewTicket{Project: "CND", Title: "Closed already", Status: status})
		want(t, rc, err, StatusDone, "")
		if tk := ticketOf(t, s, rc.Key); tk.Status != status || tk.ClosedAt.IsZero() {
			t.Fatalf("made %s: %+v", status, tk)
		}
		if got := rowsOf(t, s, opID("closed", i)); !strings.HasPrefix(got[len(got)-1], "set:closed_at=") {
			t.Fatalf("made %s: %v", status, got)
		}
	}
	// With no display name, the actor made it.
	rc, err = s.CreateTicket(ctx, Op{Actor: "admin", ID: "c3"}, NewTicket{Project: "CND", Title: "By the admin"})
	want(t, rc, err, StatusDone, "")
	if by := ticketOf(t, s, rc.Key).CreatedBy; by != "admin" {
		t.Fatalf("created by %q", by)
	}
	// The same operation sent again is answered by its receipt: no second
	// ticket, no second number.
	again, err := s.CreateTicket(ctx, op, n)
	want(t, again, err, StatusDone, "")
	if !again.Repeat || again.ID != first.ID || again.Key != first.Key {
		t.Fatalf("the repeat: %+v", again)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM tickets"); n != 5 {
		t.Fatalf("%d tickets after a repeat", n)
	}
}

func TestATicketsValuesAreChecked(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	newProject(t, s, "OTH")
	for i, c := range []struct {
		n       NewTicket
		refusal string
	}{
		{NewTicket{Project: "NOPE", Title: "x"}, RefusalNotFound},
		{NewTicket{Project: "CND", Title: ""}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "a\nb"}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: strings.Repeat("é", MaxTicketTitle+1)}, RefusalBound},
		{NewTicket{Project: "CND", Title: "x", Description: strings.Repeat("d", MaxTicketDescription+1)}, RefusalBound},
		{NewTicket{Project: "CND", Title: "x", Acceptance: strings.Repeat("a", MaxAcceptance+1)}, RefusalBound},
		{NewTicket{Project: "CND", Title: "x", Status: "doing"}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "x", Priority: MaxPriority + 1}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "x", Priority: -1}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "x", Horizon: "soon"}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "x", Owner: strings.Repeat("o", MaxPersonName+1)}, RefusalBound},
		{NewTicket{Project: "CND", Title: "x", Owner: "a\tb"}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "x", Assignees: []string{"bob"}}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "x", Assignees: []string{"agent:Claude"}}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "x", Assignees: []string{"person:Bob", "person:Bob"}}, RefusalInvalid},
		{NewTicket{Project: "CND", Title: "x", Parent: "CND-9"}, RefusalNotFound},
	} {
		rc, err := s.CreateTicket(ctx, testOp(opID("bad", i)), c.n)
		want(t, rc, err, StatusFailed, c.refusal)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM tickets"); n != 0 {
		t.Fatalf("%d tickets made by refused creates", n)
	}
	// A refused create allocates no number: the first ticket is CND-1.
	if _, key := newTicket(t, s, "CND", "First"); key != "CND-1" {
		t.Fatalf("the first key %q", key)
	}
	// At their bounds, every field is taken whole.
	n := NewTicket{Project: "CND", Title: strings.Repeat("é", MaxTicketTitle), Description: strings.Repeat("d", MaxTicketDescription),
		Acceptance: strings.Repeat("a", MaxAcceptance), Owner: strings.Repeat("é", MaxPersonName), Priority: MaxPriority}
	_, key := makeTicket(t, s, n)
	if tk := ticketOf(t, s, key); tk.Title != n.Title || tk.Description != n.Description || tk.Acceptance != n.Acceptance || tk.Owner != n.Owner || tk.Priority != MaxPriority {
		t.Fatal("a field at its bound was not kept whole")
	}
}

func TestEditSetsSeveralFieldsInOneOperation(t *testing.T) {
	s := openStore(t, storeDir(t))
	pid := newProject(t, s, "CND")
	l1, l2 := insertLabel(t, s, pid, "api", false), insertLabel(t, s, pid, "ui", false)
	m := insertMilestone(t, s, pid, "1.0", false)
	rc, err := s.CreateTicket(context.Background(), testOp("c1"), NewTicket{Project: "CND", Title: "First", Assignees: []string{"person:Alice"}, Labels: []string{l1}})
	want(t, rc, err, StatusDone, "")
	key := rc.Key
	tags := map[string]Tag{}
	for _, row := range changesOf(t, s, "test", "c1") {
		if row.Op == OpAdd {
			tags[row.Value] = TagOf(row)
		}
	}
	op := testOp("e1")
	op.ExpectedRev = 1
	rc, err = s.EditTicket(context.Background(), op, key, TicketEdit{
		Set: TicketSet{Title: ptr("Renamed"), Description: ptr("More"), Acceptance: ptr("Done when done."), Status: ptr(TicketInProgress),
			Priority: ptr(2), Horizon: ptr(HorizonNow), Owner: ptr("Bob"), Milestone: ptr(m)},
		Add:    TicketMembers{Assignees: []string{"agent:codex"}, Labels: []string{l2}},
		Remove: TicketMembers{Assignees: []string{"person:Alice"}, Labels: []string{l1}},
	})
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 2 {
		t.Fatalf("one operation, one bump: %+v", rc)
	}
	got := rowsOf(t, s, "e1")
	if want := []string{"set:title=Renamed<First", "set:description=More<", "set:acceptance=Done when done.<", "set:status=in_progress<backlog",
		"set:priority=2<0", "set:horizon=now<", "set:owner=Bob<", "set:milestone_id=" + m + "<",
		"remove:assignees=person:Alice<" + EncodeTags([]Tag{tags["person:Alice"]}), "add:assignees=agent:codex<",
		"remove:labels=" + l1 + "<" + EncodeTags([]Tag{tags[l1]}), "add:labels=" + l2 + "<",
		// Bob, the new owner, was not in the directory: added, after the
		// ticket's rows.
		"create:=" + changesOf(t, s, "test", "e1")[12].Value + "<"}; !slices.Equal(got, want) {
		t.Fatalf("the rows:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	tk := ticketOf(t, s, key)
	if tk.Title != "Renamed" || tk.Description != "More" || tk.Acceptance != "Done when done." || tk.Status != TicketInProgress || tk.Priority != 2 ||
		tk.Horizon != HorizonNow || tk.Owner != "Bob" || tk.MilestoneID != m || !slices.Equal(tk.Assignees, []string{"agent:codex"}) ||
		!slices.Equal(tk.Labels, []string{l2}) || tk.Rev != 2 || !tk.ClosedAt.IsZero() {
		t.Fatalf("the ticket: %+v", tk)
	}
	// An edit that changes nothing, a member there already added and one
	// absent removed, is refused, and writes nothing.
	rc, err = edit(s, "e2", key, TicketEdit{Set: TicketSet{Title: ptr("Renamed")}, Add: TicketMembers{Assignees: []string{"agent:codex"}},
		Remove: TicketMembers{Assignees: []string{"person:Alice"}}})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if rows := rowsOf(t, s, "e2"); len(rows) != 0 || ticketOf(t, s, key).Rev != 2 {
		t.Fatalf("an edit of nothing wrote %v", rows)
	}
	// One that expects a revision gone by is stale.
	op = testOp("e3")
	op.ExpectedRev = 1
	rc, err = s.EditTicket(context.Background(), op, key, TicketEdit{Set: TicketSet{Title: ptr("Stale")}})
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	// A milestone and an owner cleared.
	rc, err = edit(s, "e4", key, TicketEdit{Set: TicketSet{Milestone: ptr(""), Owner: ptr("")}})
	want(t, rc, err, StatusDone, "")
	if got := rowsOf(t, s, "e4"); !slices.Equal(got, []string{"set:owner=<Bob", "set:milestone_id=<" + m}) {
		t.Fatalf("the clearing rows: %v", got)
	}
	// Each value is checked as a create checks it, and a member named
	// twice, or both added and removed, is invalid.
	for i, c := range []struct {
		e       TicketEdit
		refusal string
	}{
		{TicketEdit{Set: TicketSet{Title: ptr("")}}, RefusalInvalid},
		{TicketEdit{Set: TicketSet{Title: ptr(strings.Repeat("t", MaxTicketTitle+1))}}, RefusalBound},
		{TicketEdit{Set: TicketSet{Description: ptr("a\x1bb")}}, RefusalInvalid},
		{TicketEdit{Set: TicketSet{Status: ptr("doing")}}, RefusalInvalid},
		{TicketEdit{Set: TicketSet{Priority: ptr(MaxPriority + 1)}}, RefusalInvalid},
		{TicketEdit{Set: TicketSet{Horizon: ptr("soon")}}, RefusalInvalid},
		{TicketEdit{Set: TicketSet{Owner: ptr("a\nb")}}, RefusalInvalid},
		{TicketEdit{Add: TicketMembers{Assignees: []string{"bob"}}}, RefusalInvalid},
		{TicketEdit{Add: TicketMembers{Assignees: []string{"person:X", "person:X"}}}, RefusalInvalid},
		{TicketEdit{Remove: TicketMembers{Labels: []string{l2, l2}}}, RefusalInvalid},
		{TicketEdit{Add: TicketMembers{Labels: []string{l1}}, Remove: TicketMembers{Labels: []string{l1}}}, RefusalInvalid},
		{TicketEdit{}, RefusalInvalid},
	} {
		rc, err := edit(s, opID("bad", i), key, c.e)
		want(t, rc, err, StatusFailed, c.refusal)
	}
	rc, err = edit(s, "none", "CND-99", TicketEdit{Set: TicketSet{Title: ptr("x")}})
	want(t, rc, err, StatusFailed, RefusalNotFound)
}

func TestClosedAtIsSetAndCleared(t *testing.T) {
	s := openStore(t, storeDir(t))
	newOpenProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "First")
	status := func(opID, status string) []string {
		t.Helper()
		rc, err := edit(s, opID, key, TicketEdit{Set: TicketSet{Status: ptr(status)}})
		want(t, rc, err, StatusDone, "")
		return rowsOf(t, s, opID)
	}
	rows := status("s1", TicketDone)
	closedAt := ticketOf(t, s, key).ClosedAt
	if closedAt.IsZero() || len(rows) != 2 || rows[0] != "set:status=done<backlog" || rows[1] != "set:closed_at="+stamp(closedAt)+"<" {
		t.Fatalf("moved to done: %v", rows)
	}
	// A move out of done clears it.
	if rows := status("s2", TicketInProgress); len(rows) != 2 || rows[1] != "set:closed_at=<"+stamp(closedAt) || !ticketOf(t, s, key).ClosedAt.IsZero() {
		t.Fatalf("moved out of done: %v", rows)
	}
	// An edit that leaves the status leaves closed_at.
	rc, err := edit(s, "t1", key, TicketEdit{Set: TicketSet{Title: ptr("Renamed")}})
	want(t, rc, err, StatusDone, "")
	if rows := rowsOf(t, s, "t1"); len(rows) != 1 {
		t.Fatalf("a title's edit: %v", rows)
	}
	// Cancelled closes it too, and a move between the two closed statuses
	// leaves it closed.
	status("s3", TicketCancelled)
	if ticketOf(t, s, key).ClosedAt.IsZero() {
		t.Fatal("cancelled is not closed")
	}
	status("s4", TicketDone)
	closedAt = ticketOf(t, s, key).ClosedAt
	if closedAt.IsZero() {
		t.Fatal("done after cancelled is not closed")
	}
	if rows := status("s5", TicketReady); !slices.Equal(rows, []string{"set:status=ready<done", "set:closed_at=<" + stamp(closedAt)}) {
		t.Fatalf("reopened: %v", rows)
	}
	if !ticketOf(t, s, key).ClosedAt.IsZero() {
		t.Fatal("reopened, still closed")
	}
}

func TestTheParentChainIsCheckedUpAndDown(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	eight := chainOf(t, s, "CND", 8, "Deep")
	// A ninth ticket under the eighth is a chain of 9: refused, by a new
	// ticket and by a new parent.
	rc, err := s.CreateTicket(ctx, testOp("c9"), NewTicket{Project: "CND", Title: "Ninth", Parent: eight[7]})
	want(t, rc, err, StatusFailed, RefusalCycle)
	if !strings.Contains(rc.Error, "9") {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	_, lone := newTicket(t, s, "CND", "Lone")
	rc, err = edit(s, "p9", lone, TicketEdit{Set: TicketSet{Parent: ptr(eight[7])}})
	want(t, rc, err, StatusFailed, RefusalCycle)
	// Under the seventh it is a chain of 8: allowed.
	rc, err = edit(s, "p8", lone, TicketEdit{Set: TicketSet{Parent: ptr(eight[6])}})
	want(t, rc, err, StatusDone, "")
	// A cycle: a ticket under its own child, a chain of 4 that the depth
	// alone would allow; the root under its own descendant; a ticket its
	// own parent.
	pair := chainOf(t, s, "CND", 2, "Pair")
	rc, err = edit(s, "pair", pair[0], TicketEdit{Set: TicketSet{Parent: ptr(pair[1])}})
	want(t, rc, err, StatusFailed, RefusalCycle)
	if !strings.Contains(rc.Error, "cycle") {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	rc, err = edit(s, "cycle", eight[0], TicketEdit{Set: TicketSet{Parent: ptr(eight[3])}})
	want(t, rc, err, StatusFailed, RefusalCycle)
	rc, err = edit(s, "self", eight[2], TicketEdit{Set: TicketSet{Parent: ptr(eight[2])}})
	want(t, rc, err, StatusFailed, RefusalCycle)
	// A subtree of height 4 moved under a chain of 5 makes a chain of 9:
	// refused; under a chain of 4, a chain of 8: allowed.
	sub := chainOf(t, s, "CND", 4, "Sub")
	five := chainOf(t, s, "CND", 5, "Five")
	rc, err = edit(s, "under5", sub[0], TicketEdit{Set: TicketSet{Parent: ptr(five[4])}})
	want(t, rc, err, StatusFailed, RefusalCycle)
	four := chainOf(t, s, "CND", 4, "Four")
	rc, err = edit(s, "under4", sub[0], TicketEdit{Set: TicketSet{Parent: ptr(four[3])}})
	want(t, rc, err, StatusDone, "")
	if got := ticketOf(t, s, sub[0]).ParentID; got != ticketOf(t, s, four[3]).ID {
		t.Fatalf("the moved subtree's parent %q", got)
	}
	// Moved, the subtree's own chain is the longer by it: a ticket under
	// its deepest is a chain of 9.
	rc, err = s.CreateTicket(ctx, testOp("deeper"), NewTicket{Project: "CND", Title: "Deeper", Parent: sub[3]})
	want(t, rc, err, StatusFailed, RefusalCycle)
	// A parent cleared.
	rc, err = edit(s, "clear", sub[0], TicketEdit{Set: TicketSet{Parent: ptr("")}})
	want(t, rc, err, StatusDone, "")
	if got := ticketOf(t, s, sub[0]).ParentID; got != "" {
		t.Fatalf("the parent after a clear %q", got)
	}
}

func TestAParentOfAnotherProjectIsInvalidAndADeletedOneNotFound(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	newProject(t, s, "OTH")
	_, other := newTicket(t, s, "OTH", "Other's")
	_, a := newTicket(t, s, "CND", "A")
	_, b := newTicket(t, s, "CND", "B")
	rc, err := s.CreateTicket(ctx, testOp("c1"), NewTicket{Project: "CND", Title: "x", Parent: other})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = edit(s, "e1", a, TicketEdit{Set: TicketSet{Parent: ptr(other)}})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.DeleteTicket(ctx, testOp("d1"), b)
	want(t, rc, err, StatusDone, "")
	rc, err = s.CreateTicket(ctx, testOp("c2"), NewTicket{Project: "CND", Title: "x", Parent: b})
	want(t, rc, err, StatusFailed, RefusalNotFound)
	rc, err = edit(s, "e2", a, TicketEdit{Set: TicketSet{Parent: ptr(b)}})
	want(t, rc, err, StatusFailed, RefusalNotFound)
	for i, key := range []string{"CND-99", "garbage", ""} {
		rc, err = s.CreateTicket(ctx, testOp(opID("missing", i)), NewTicket{Project: "CND", Title: "x", Parent: key})
		if key == "" {
			want(t, rc, err, StatusDone, "")
			continue
		}
		want(t, rc, err, StatusFailed, RefusalNotFound)
	}
}

func TestALabelOrMilestoneIsOneOfTheTicketsProject(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	pid := newProject(t, s, "CND")
	oid := newProject(t, s, "OTH")
	_, key := newTicket(t, s, "CND", "First")
	labels := map[string]string{
		insertLabel(t, s, oid, "api", false):  RefusalInvalid,
		insertLabel(t, s, oid, "old", true):   RefusalNotFound,
		insertLabel(t, s, pid, "gone", true):  RefusalNotFound,
		strings.Repeat("0", IDLen):            RefusalNotFound,
		insertLabel(t, s, pid, "live", false): "",
	}
	milestones := map[string]string{
		insertMilestone(t, s, oid, "1.0", false): RefusalInvalid,
		insertMilestone(t, s, pid, "0.9", true):  RefusalNotFound,
		strings.Repeat("0", IDLen):               RefusalNotFound,
		insertMilestone(t, s, pid, "1.0", false): "",
	}
	i := 0
	for id, refusal := range labels {
		i++
		status := StatusFailed
		if refusal == "" {
			status = StatusDone
		}
		rc, err := s.CreateTicket(ctx, testOp(opID("cl", i)), NewTicket{Project: "CND", Title: "x", Labels: []string{id}})
		want(t, rc, err, status, refusal)
		rc, err = edit(s, opID("el", i), key, TicketEdit{Add: TicketMembers{Labels: []string{id}}})
		want(t, rc, err, status, refusal)
	}
	for id, refusal := range milestones {
		i++
		status := StatusFailed
		if refusal == "" {
			status = StatusDone
		}
		rc, err := s.CreateTicket(ctx, testOp(opID("cm", i)), NewTicket{Project: "CND", Title: "x", Milestone: id})
		want(t, rc, err, status, refusal)
		rc, err = edit(s, opID("em", i), key, TicketEdit{Set: TicketSet{Milestone: ptr(id)}})
		want(t, rc, err, status, refusal)
	}
}

// A parent deleted with live children: one row, its own; the children
// keep their parent_id and read it as deleted, no other ticket's rev
// moves, every write on it but the undelete is not_found, and the
// undelete restores every reference by the reverse of that one row.
func TestDeletingAParentLeavesEveryReferenceInPlace(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	pID, p := newTicket(t, s, "CND", "Parent")
	_, c1 := makeTicket(t, s, NewTicket{Project: "CND", Title: "Child 1", Parent: p})
	_, c2 := makeTicket(t, s, NewTicket{Project: "CND", Title: "Child 2", Parent: p})
	_, x := newTicket(t, s, "CND", "Other")
	revs := func() map[string]int64 {
		out := map[string]int64{"project": projectOf(t, s, "CND").Rev}
		for _, k := range []string{c1, c2, x} {
			out[k] = ticketOf(t, s, k).Rev
		}
		return out
	}
	before := revs()
	rc, err := s.DeleteTicket(ctx, testOp("d1"), p)
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 2 {
		t.Fatalf("the receipt: %+v", rc)
	}
	rows := changesOf(t, s, "test", "d1")
	if len(rows) != 1 || rows[0].Entity != EntityTicket || rows[0].EntityID != pID || rows[0].Field != "deleted_at" || rows[0].Op != OpSet || rows[0].Value == "" || rows[0].Prior != "" {
		t.Fatalf("the delete's rows: %+v", rows)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE op_id = 'd1'"); n != 1 {
		t.Fatalf("%d rows written by a delete", n)
	}
	deleted := ticketOf(t, s, p)
	if deleted.DeletedAt.IsZero() || deleted.Key != "CND-1" || deleted.Title != "Parent" {
		t.Fatalf("the tombstone: %+v", deleted)
	}
	for _, c := range []string{c1, c2} {
		child := ticketOf(t, s, c)
		var parentKey string
		if err := s.db.QueryRow("SELECT key FROM tickets WHERE id = ?", child.ParentID).Scan(&parentKey); err != nil || child.ParentID != pID {
			t.Fatalf("%s's parent: %q %v", c, child.ParentID, err)
		}
		if ticketOf(t, s, parentKey).DeletedAt.IsZero() {
			t.Fatalf("%s's parent does not read as deleted", c)
		}
	}
	if after := revs(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("revs moved by the delete: %v, before %v", after, before)
	}
	// The list's parent= finds the children by the column.
	if n := count(t, s, "SELECT COUNT(*) FROM tickets WHERE parent_id = ?", pID); n != 2 {
		t.Fatalf("%d children by the column", n)
	}
	// Every write on it but the undelete: not_found.
	rc, err = edit(s, "w1", p, TicketEdit{Set: TicketSet{Title: ptr("Renamed")}})
	want(t, rc, err, StatusFailed, RefusalNotFound)
	rc, err = s.DeleteTicket(ctx, testOp("w2"), p)
	want(t, rc, err, StatusFailed, RefusalNotFound)
	rc, err = s.CreateTicket(ctx, testOp("w3"), NewTicket{Project: "CND", Title: "Child 3", Parent: p})
	want(t, rc, err, StatusFailed, RefusalNotFound)
	rc, err = edit(s, "w4", x, TicketEdit{Set: TicketSet{Parent: ptr(p)}})
	want(t, rc, err, StatusFailed, RefusalNotFound)
	// The undelete: the reverse of the one row.
	rc, err = s.UndeleteTicket(ctx, testOp("u1"), p)
	want(t, rc, err, StatusDone, "")
	if rows := changesOf(t, s, "test", "u1"); len(rows) != 1 || rows[0].Field != "deleted_at" || rows[0].Value != "" || rows[0].Prior != changesOf(t, s, "test", "d1")[0].Value {
		t.Fatalf("the undelete's rows: %+v", rows)
	}
	if tk := ticketOf(t, s, p); !tk.DeletedAt.IsZero() || tk.Rev != 3 {
		t.Fatalf("undeleted: %+v", tk)
	}
	for _, c := range []string{c1, c2} {
		if got := ticketOf(t, s, c).ParentID; got != pID {
			t.Fatalf("%s's parent after the undelete: %q", c, got)
		}
	}
	if after := revs(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("revs moved by the undelete: %v, before %v", after, before)
	}
	rc, err = s.UndeleteTicket(ctx, testOp("u2"), p)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = edit(s, "w5", p, TicketEdit{Set: TicketSet{Title: ptr("Renamed")}})
	want(t, rc, err, StatusDone, "")
	rc, err = s.CreateTicket(ctx, testOp("w6"), NewTicket{Project: "CND", Title: "Child 3", Parent: p})
	want(t, rc, err, StatusDone, "")
	for _, f := range []func(context.Context, Op, string) (Receipt, error){s.DeleteTicket, s.UndeleteTicket} {
		rc, err = f(ctx, testOp("missing-"+newID()), "CND-99")
		want(t, rc, err, StatusFailed, RefusalNotFound)
	}
}

// The walks take tombstones as edges: a cycle and a chain of 9 through a
// deleted ticket are refused, so an undelete never makes either.
func TestACycleAndAChainThroughATombstoneAreRefused(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	abc := chainOf(t, s, "CND", 3, "ABC")
	rc, err := s.DeleteTicket(ctx, testOp("d1"), abc[1])
	want(t, rc, err, StatusDone, "")
	rc, err = edit(s, "cycle", abc[0], TicketEdit{Set: TicketSet{Parent: ptr(abc[2])}})
	want(t, rc, err, StatusFailed, RefusalCycle)
	// A chain of 8 with a tombstone in it: a ninth under it is refused.
	eight := chainOf(t, s, "CND", 8, "Deep")
	rc, err = s.DeleteTicket(ctx, testOp("d2"), eight[3])
	want(t, rc, err, StatusDone, "")
	rc, err = s.CreateTicket(ctx, testOp("c9"), NewTicket{Project: "CND", Title: "Ninth", Parent: eight[7]})
	want(t, rc, err, StatusFailed, RefusalCycle)
	// And a subtree of height 4 through a tombstone under a chain of 5.
	sub := chainOf(t, s, "CND", 4, "Sub")
	rc, err = s.DeleteTicket(ctx, testOp("d3"), sub[1])
	want(t, rc, err, StatusDone, "")
	five := chainOf(t, s, "CND", 5, "Five")
	rc, err = edit(s, "under5", sub[0], TicketEdit{Set: TicketSet{Parent: ptr(five[4])}})
	want(t, rc, err, StatusFailed, RefusalCycle)
	// The undeletes find what they left: no cycle, no chain past 8.
	for i, key := range []string{abc[1], eight[3], sub[1]} {
		rc, err = s.UndeleteTicket(ctx, testOp(opID("u", i)), key)
		want(t, rc, err, StatusDone, "")
	}
}

func TestAssigneesAndLabelsPastTheirBoundsAreRefused(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	pid := newProject(t, s, "CND")
	people := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("person:P%d", i))
		}
		return out
	}
	var labels []string
	for i := range MaxTicketLabels + 1 {
		labels = append(labels, insertLabel(t, s, pid, fmt.Sprintf("l%d", i), false))
	}
	rc, err := s.CreateTicket(ctx, testOp("a8"), NewTicket{Project: "CND", Title: "Eight", Assignees: people(MaxAssignees)})
	want(t, rc, err, StatusDone, "")
	eight := rc.Key
	rc, err = s.CreateTicket(ctx, testOp("a9"), NewTicket{Project: "CND", Title: "Nine", Assignees: people(MaxAssignees + 1)})
	want(t, rc, err, StatusFailed, RefusalBound)
	rc, err = s.CreateTicket(ctx, testOp("l20"), NewTicket{Project: "CND", Title: "Twenty", Labels: labels[:MaxTicketLabels]})
	want(t, rc, err, StatusDone, "")
	twenty := rc.Key
	rc, err = s.CreateTicket(ctx, testOp("l21"), NewTicket{Project: "CND", Title: "Twenty-one", Labels: labels})
	want(t, rc, err, StatusFailed, RefusalBound)
	// An edit adding one past the bound: refused, and nothing changed.
	rc, err = edit(s, "e1", eight, TicketEdit{Add: TicketMembers{Assignees: []string{"person:Extra"}}})
	want(t, rc, err, StatusFailed, RefusalBound)
	if !strings.Contains(rc.Error, "8") {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	rc, err = edit(s, "e2", twenty, TicketEdit{Add: TicketMembers{Labels: labels[MaxTicketLabels:]}})
	want(t, rc, err, StatusFailed, RefusalBound)
	if a, l := ticketOf(t, s, eight).Assignees, ticketOf(t, s, twenty).Labels; len(a) != MaxAssignees || len(l) != MaxTicketLabels {
		t.Fatalf("after the refusals: %d assignees, %d labels", len(a), len(l))
	}
	// One added as one is removed leaves the set at its bound: allowed.
	rc, err = edit(s, "e3", eight, TicketEdit{Add: TicketMembers{Assignees: []string{"person:Extra"}}, Remove: TicketMembers{Assignees: []string{"person:P0"}}})
	want(t, rc, err, StatusDone, "")
	if a := ticketOf(t, s, eight).Assignees; len(a) != MaxAssignees || !slices.Contains(a, "person:Extra") || slices.Contains(a, "person:P0") {
		t.Fatalf("after a swap: %v", a)
	}
}

// An edit's add list longer than its set's bound is refused before any
// of its members is looked up or written, as a create's is: whatever the
// members are, the bound answers, and no per-member work runs under the
// write lock first.
func TestAnAddListPastItsBoundIsRefusedBeforeItsMembers(t *testing.T) {
	s := openStore(t, storeDir(t))
	pid := newProject(t, s, "CND")
	oid := newProject(t, s, "OTH")
	_, key := newTicket(t, s, "CND", "First")
	// One label of another project among 21: invalid, were the labels
	// looked up before the list was counted.
	labels := []string{insertLabel(t, s, oid, "other", false)}
	for i := range MaxTicketLabels {
		labels = append(labels, insertLabel(t, s, pid, fmt.Sprintf("l%d", i), false))
	}
	rc, err := edit(s, "l21", key, TicketEdit{Add: TicketMembers{Labels: labels}})
	want(t, rc, err, StatusFailed, RefusalBound)
	// Nine assignees while every write to the set's table fails: the
	// refusal comes before the first write, so the edit is refused bound
	// rather than failed by the database.
	if _, err := s.db.Exec(`CREATE TRIGGER no_assignee BEFORE INSERT ON ticket_assignees BEGIN SELECT RAISE(ABORT, 'an assignee was written'); END`); err != nil {
		t.Fatal(err)
	}
	var people []string
	for i := range MaxAssignees + 1 {
		people = append(people, fmt.Sprintf("person:P%d", i))
	}
	rc, err = edit(s, "a9", key, TicketEdit{Add: TicketMembers{Assignees: people}})
	want(t, rc, err, StatusFailed, RefusalBound)
	// At its bound the list does reach the writes, and the trigger.
	if _, err := edit(s, "a8", key, TicketEdit{Add: TicketMembers{Assignees: people[:MaxAssignees]}}); err == nil || !strings.Contains(err.Error(), "an assignee was written") {
		t.Fatalf("eight assignees, every write failing: %v", err)
	}
	if tk := ticketOf(t, s, key); len(tk.Assignees) != 0 || len(tk.Labels) != 0 || tk.Rev != 1 {
		t.Fatalf("after the refusals: %+v", tk)
	}
}

// Whatever actor added a member, a remove can take it away: the add
// carries its actor in the tag the remove names in its prior. An actor
// of 64 KiB, which made that prior too large to write, is refused before
// anything is written; one at its bound, at its worst as JSON ('<' is
// six bytes in a tag, U+2028 six for its three), leaves the remove far
// inside MaxChangeValue. With no display name the actor is the ticket's
// created_by, within that field's bound.
func TestAMemberAddedByAnyActorCanBeRemoved(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	pid := newProject(t, s, "CND")
	label := insertLabel(t, s, pid, "api", false)
	members := NewTicket{Project: "CND", Title: "Members", Assignees: []string{"person:Alice"}, Labels: []string{label}}
	if _, err := s.CreateTicket(ctx, Op{Actor: strings.Repeat("a", 64<<10), ID: "huge", By: "Tester"}, members); !errors.Is(err, ErrBadActor) {
		t.Fatalf("an actor of 64 KiB: %v", err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM tickets"); n != 0 {
		t.Fatalf("%d tickets made by a refused actor", n)
	}
	for i, actor := range []string{strings.Repeat("<", MaxActor), strings.Repeat("\u2028", MaxActor/3), "agent:" + strings.Repeat("m", 40)} {
		create, remove := opID("c", i), opID("r", i)
		rc, err := s.CreateTicket(ctx, Op{Actor: actor, ID: create}, members)
		want(t, rc, err, StatusDone, "")
		key := rc.Key
		if by := ticketOf(t, s, key).CreatedBy; by != actor || checkField("op.by", "by", by) != nil {
			t.Fatalf("created by %q", by)
		}
		tags := map[string]Tag{}
		for _, row := range changesOf(t, s, actor, create) {
			if row.Op == OpAdd {
				tags[row.Value] = TagOf(row)
			}
		}
		if len(tags) != 2 || tags["person:Alice"].Actor != actor || tags[label].Actor != actor {
			t.Fatalf("the adds' tags: %+v", tags)
		}
		rc, err = edit(s, remove, key, TicketEdit{Remove: TicketMembers{Assignees: []string{"person:Alice"}, Labels: []string{label}}})
		want(t, rc, err, StatusDone, "")
		if got, want := rowsOf(t, s, remove), []string{"remove:assignees=person:Alice<" + EncodeTags([]Tag{tags["person:Alice"]}),
			"remove:labels=" + label + "<" + EncodeTags([]Tag{tags[label]})}; !slices.Equal(got, want) {
			t.Fatalf("the removes:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if tk := ticketOf(t, s, key); len(tk.Assignees) != 0 || len(tk.Labels) != 0 {
			t.Fatalf("after the removes: %+v", tk)
		}
	}
}

// A set past its bound (a merge may leave one; here rows the test
// inserts) refuses an add and allows a remove, and refuses an add until
// it is under its bound again.
func TestASetPastItsBoundRefusesAnAddAndAllowsARemove(t *testing.T) {
	s := openStore(t, storeDir(t))
	pid := newProject(t, s, "CND")
	tid, key := newTicket(t, s, "CND", "Crowded")
	now := stamp(time.Now())
	for i := range MaxAssignees + 2 {
		if _, err := s.db.Exec("INSERT INTO ticket_assignees (ticket_id, actor, added_at) VALUES (?, ?, ?)", tid, fmt.Sprintf("person:P%d", i), now); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := edit(s, "a1", key, TicketEdit{Add: TicketMembers{Assignees: []string{"person:Z"}}})
	want(t, rc, err, StatusFailed, RefusalBound)
	rc, err = edit(s, "r1", key, TicketEdit{Remove: TicketMembers{Assignees: []string{"person:P0"}}})
	want(t, rc, err, StatusDone, "")
	// The member the test inserted had no add in the log: the remove names
	// no tag.
	if got := rowsOf(t, s, "r1"); !slices.Equal(got, []string{"remove:assignees=person:P0<[]"}) {
		t.Fatalf("the remove's rows: %v", got)
	}
	// Still past its bound (9): an add with a remove that leaves it there
	// is refused; one with two removes leaves it at 8 and is allowed.
	rc, err = edit(s, "a2", key, TicketEdit{Add: TicketMembers{Assignees: []string{"person:Z"}}, Remove: TicketMembers{Assignees: []string{"person:P1"}}})
	want(t, rc, err, StatusFailed, RefusalBound)
	rc, err = edit(s, "a3", key, TicketEdit{Add: TicketMembers{Assignees: []string{"person:Z"}}, Remove: TicketMembers{Assignees: []string{"person:P1", "person:P2"}}})
	want(t, rc, err, StatusDone, "")
	if a := ticketOf(t, s, key).Assignees; len(a) != MaxAssignees || !slices.Contains(a, "person:Z") {
		t.Fatalf("the assignees: %v", a)
	}
	// Labels the same, a label deleted counting for nothing: its
	// membership reads as absent.
	var labels []string
	for i := range MaxTicketLabels + 2 {
		id := insertLabel(t, s, pid, fmt.Sprintf("l%d", i), false)
		labels = append(labels, id)
		if i <= MaxTicketLabels {
			if _, err := s.db.Exec("INSERT INTO ticket_labels (ticket_id, label_id) VALUES (?, ?)", tid, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	extra := labels[MaxTicketLabels+1]
	rc, err = edit(s, "l1", key, TicketEdit{Add: TicketMembers{Labels: []string{extra}}})
	want(t, rc, err, StatusFailed, RefusalBound)
	rc, err = edit(s, "l2", key, TicketEdit{Remove: TicketMembers{Labels: []string{labels[0]}}})
	want(t, rc, err, StatusDone, "")
	rc, err = edit(s, "l3", key, TicketEdit{Add: TicketMembers{Labels: []string{extra}}})
	want(t, rc, err, StatusFailed, RefusalBound)
	if _, err := s.db.Exec("UPDATE labels SET deleted_at = ? WHERE id = ?", now, labels[1]); err != nil {
		t.Fatal(err)
	}
	if got := ticketOf(t, s, key).Labels; len(got) != MaxTicketLabels-1 || slices.Contains(got, labels[1]) {
		t.Fatalf("a deleted label's membership: %v", got)
	}
	rc, err = edit(s, "l4", key, TicketEdit{Add: TicketMembers{Labels: []string{extra}}})
	want(t, rc, err, StatusDone, "")
}

// A remove names in its prior the tags of the member's adds it removes,
// and only those no earlier remove named: an add after a remove is a new
// tag of its own.
func TestARemoveNamesTheTagsOfTheAddsItRemoves(t *testing.T) {
	s := openStore(t, storeDir(t))
	newProject(t, s, "CND")
	rc, err := s.CreateTicket(context.Background(), testOp("c1"), NewTicket{Project: "CND", Title: "First", Assignees: []string{"person:Alice"}})
	want(t, rc, err, StatusDone, "")
	key := rc.Key
	var first Tag
	for _, row := range changesOf(t, s, "test", "c1") {
		if row.Op == OpAdd {
			first = TagOf(row)
		}
	}
	if first.OpID != "c1" {
		t.Fatalf("no add row in the create: %+v", first)
	}
	rc, err = edit(s, "r1", key, TicketEdit{Remove: TicketMembers{Assignees: []string{"person:Alice"}}})
	want(t, rc, err, StatusDone, "")
	if got := rowsOf(t, s, "r1"); !slices.Equal(got, []string{"remove:assignees=person:Alice<" + EncodeTags([]Tag{first})}) {
		t.Fatalf("the first remove: %v", got)
	}
	rc, err = edit(s, "a2", key, TicketEdit{Add: TicketMembers{Assignees: []string{"person:Alice"}}})
	want(t, rc, err, StatusDone, "")
	rc, err = edit(s, "r2", key, TicketEdit{Remove: TicketMembers{Assignees: []string{"person:Alice"}}})
	want(t, rc, err, StatusDone, "")
	if got := rowsOf(t, s, "r2"); !slices.Equal(got, []string{"remove:assignees=person:Alice<" + EncodeTags([]Tag{{Actor: "test", OpID: "a2", N: 0}})}) {
		t.Fatalf("the second remove: %v", got)
	}
}

// A description at its bound made of quotes, backslashes and newlines is
// recorded whole: set, then replaced, its value and its prior the text as
// it is.
func TestADescriptionAtItsBoundIsRecordedWhole(t *testing.T) {
	s := openStore(t, storeDir(t))
	newProject(t, s, "CND")
	fill := func(unit, rest string) string {
		return strings.Repeat(unit, MaxTicketDescription/len(unit)) + strings.Repeat(rest, MaxTicketDescription%len(unit))
	}
	d1, d2 := fill("\"\\\n", "\""), fill("\\\n\"", "\\")
	if len(d1) != MaxTicketDescription || len(d2) != MaxTicketDescription || d1 == d2 {
		t.Fatalf("the descriptions are %d and %d bytes", len(d1), len(d2))
	}
	rc, err := s.CreateTicket(context.Background(), testOp("c1"), NewTicket{Project: "CND", Title: "Quoted", Description: d1})
	want(t, rc, err, StatusDone, "")
	key := rc.Key
	rows := changesOf(t, s, "test", "c1")
	if len(rows) != 3 || rows[2].Field != "description" || rows[2].Value != d1 || rows[2].Prior != "" {
		t.Fatal("the create's description row is not the description whole")
	}
	rc, err = edit(s, "e1", key, TicketEdit{Set: TicketSet{Description: &d2}})
	want(t, rc, err, StatusDone, "")
	rows = changesOf(t, s, "test", "e1")
	if len(rows) != 1 || rows[0].Value != d2 || rows[0].Prior != d1 {
		t.Fatal("the replacing row is not the two descriptions whole")
	}
	if ticketOf(t, s, key).Description != d2 {
		t.Fatal("the description read back is not the one set")
	}
	rc, err = edit(s, "e2", key, TicketEdit{Set: TicketSet{Description: ptr(d1 + "x")}})
	want(t, rc, err, StatusFailed, RefusalBound)
}

// A Markdown field allows \n and \t and refuses every other control
// character; a title refuses them all.
func TestAControlCharacterInAMarkdownFieldIsRefused(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "First")
	for i, c := range []string{"\x00", "\x07", "\r", "\v", "\f", "\x1b", "\x7f", string(rune(0x85)), string(rune(0x9b))} {
		for j, e := range []TicketEdit{
			{Set: TicketSet{Description: ptr("a" + c + "b")}},
			{Set: TicketSet{Acceptance: ptr("a" + c + "b")}},
			{Set: TicketSet{Title: ptr("a" + c + "b")}},
		} {
			rc, err := edit(s, fmt.Sprintf("c%d-%d", i, j), key, e)
			want(t, rc, err, StatusFailed, RefusalInvalid)
		}
		rc, err := s.CreateTicket(ctx, testOp(opID("n", i)), NewTicket{Project: "CND", Title: "x", Description: "a" + c + "b"})
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	rc, err := edit(s, "ok", key, TicketEdit{Set: TicketSet{Description: ptr("a\nb\tc"), Acceptance: ptr("- a\n\t- b")}})
	want(t, rc, err, StatusDone, "")
	for i, title := range []string{"a\nb", "a\tb"} {
		rc, err := edit(s, opID("title", i), key, TicketEdit{Set: TicketSet{Title: ptr(title)}})
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
}

// Every write on a deleted ticket but its undelete is not_found: a
// comment, a relation from it or to it, a link, an acceptance, removes
// included; once undeleted, each is allowed again.
func TestEveryWriteOnADeletedTicketIsNotFound(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	s.SetRunExists(func(string) bool { return true })
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Deleted", Owner: "Alice"})
	var live []string
	for i := range 4 {
		_, k := newTicket(t, s, "CND", fmt.Sprintf("Live %d", i))
		live = append(live, k)
	}
	rc, err := s.AddComment(ctx, testOp("c1"), key, "Before")
	want(t, rc, err, StatusDone, "")
	comment := rc.ID
	rc, err = relate(s, "r1", key, RelationBlocks, live[0])
	want(t, rc, err, StatusDone, "")
	relation := rc.ID
	rc, err = relate(s, "r2", live[1], RelationBlocks, key)
	want(t, rc, err, StatusDone, "")
	incoming := rc.ID
	rc, err = s.AddLink(ctx, testOp("l1"), key, NewLink{Kind: LinkDelivery, URL: "https://example.com/pull/1"}, nil)
	want(t, rc, err, StatusDone, "")
	link := rc.ID
	rc, err = s.AcceptTicket(ctx, testOp("a1"), key, "")
	want(t, rc, err, StatusDone, "")
	rc, err = s.DeleteTicket(ctx, testOp("d1"), key)
	want(t, rc, err, StatusDone, "")
	writes := []func(string) (Receipt, error){
		func(id string) (Receipt, error) { return s.AddComment(ctx, testOp(id), key, "After") },
		func(id string) (Receipt, error) { return s.EditComment(ctx, testOp(id), key, comment, "After") },
		func(id string) (Receipt, error) { return s.DeleteComment(ctx, testOp(id), key, comment) },
		func(id string) (Receipt, error) { return relate(s, id, key, RelationRelates, live[2]) },
		func(id string) (Receipt, error) { return relate(s, id, live[3], RelationRelates, key) },
		func(id string) (Receipt, error) { return s.RemoveRelation(ctx, testOp(id), key, relation) },
		// From the live end of a relation whose other end is deleted.
		func(id string) (Receipt, error) { return s.RemoveRelation(ctx, testOp(id), live[1], incoming) },
		func(id string) (Receipt, error) {
			return s.AddLink(ctx, testOp(id), key, NewLink{Kind: LinkExternal, URL: "https://example.com/issues/2"}, nil)
		},
		func(id string) (Receipt, error) { return s.RemoveLink(ctx, testOp(id), key, link) },
		func(id string) (Receipt, error) { return s.AcceptTicket(ctx, testOp(id), key, "") },
		func(id string) (Receipt, error) { return s.UnacceptTicket(ctx, testOp(id), key) },
	}
	for i, w := range writes {
		rc, err := w(opID("deleted", i))
		want(t, rc, err, StatusFailed, RefusalNotFound)
	}
	rc, err = s.UndeleteTicket(ctx, testOp("u1"), key)
	want(t, rc, err, StatusDone, "")
	for i, w := range writes {
		rc, err := w(opID("undeleted", i))
		want(t, rc, err, StatusDone, "")
	}
}

func TestTheTicketReadIsWhole(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	rc, err := s.CreateLabel(ctx, testOp("l1"), "CND", NewLabel{Name: "api", Color: "blue"})
	api := mustDone(t, "label", rc, err).ID
	rc, err = s.CreateMilestone(ctx, testOp("m1"), "CND", NewMilestone{Name: "Beta", Target: "2026-12-01"})
	beta := mustDone(t, "milestone", rc, err).ID
	_, parent := newTicket(t, s, "CND", "The parent")                                                                                   // CND-1
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "The ticket", Description: "What it is", Acceptance: "When it is done", // CND-2
		Priority: 3, Horizon: HorizonNow, Owner: "Alice", Assignees: []string{"person:Bob", "agent:claude"}, Labels: []string{api}, Milestone: beta, Parent: parent})
	// 55 children, the second deleted: 50 of the 54 live ones read, by
	// number, and the count of all.
	for i := range 55 {
		makeTicket(t, s, NewTicket{Project: "CND", Title: fmt.Sprintf("Child %d", i+1), Parent: key}) // CND-3 to CND-57
	}
	rc, err = s.DeleteTicket(ctx, testOp("d4"), "CND-4")
	mustDone(t, "delete a child", rc, err)
	// It blocks CND-58; CND-59 blocks it; it relates to CND-60, deleted.
	for _, title := range []string{"Blocked by it", "Its blocker", "Related"} {
		newTicket(t, s, "CND", title)
	}
	for i, r := range [][3]string{{key, RelationBlocks, "CND-58"}, {"CND-59", RelationBlocks, key}, {key, RelationRelates, "CND-60"}} {
		rc, err := s.AddRelation(ctx, testOp(opID("r", i)), r[0], r[1], r[2])
		mustDone(t, "relation", rc, err)
	}
	rc, err = s.DeleteTicket(ctx, testOp("d60"), "CND-60")
	mustDone(t, "delete CND-60", rc, err)
	rc, err = s.AddLink(ctx, testOp("k1"), key, NewLink{Kind: LinkDelivery, URL: "https://example.com/pull/1"}, nil)
	mustDone(t, "link", rc, err)
	// Two attachments, the second removed: the read carries the live one.
	for i := range 2 {
		rc, _, err := put(s, opID("f", i), key, fmt.Sprintf("file%d.txt", i), []byte(fmt.Sprintf("file %d", i)))
		if id := mustDone(t, "attachment", rc, err).ID; i == 1 {
			rc, err := s.RemoveAttachment(ctx, testOp("rf"), key, id)
			mustDone(t, "remove an attachment", rc, err)
		}
	}
	// 60 comments, the 59th deleted: the last 50 read, numbered 11 to 60,
	// the tombstone among them.
	var c59 string
	for i := range 60 {
		rc, err := s.AddComment(ctx, testOp(opID("c", i)), key, fmt.Sprintf("Comment %d", i+1))
		if id := mustDone(t, "comment", rc, err).ID; i == 58 {
			c59 = id
		}
	}
	rc, err = s.DeleteComment(ctx, testOp("dc"), key, c59)
	mustDone(t, "delete a comment", rc, err)

	v, err := s.Ticket(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if v.Key != key || v.Title != "The ticket" || v.Description != "What it is" || v.Acceptance != "When it is done" || v.Owner != "Alice" ||
		v.Priority != 3 || v.Horizon != HorizonNow || v.MilestoneID != beta || v.Rev != ticketOf(t, s, key).Rev {
		t.Fatalf("the row: %+v", v.Ticket)
	}
	if strings.Join(v.Assignees, " ") != "agent:claude person:Bob" || strings.Join(v.Labels, " ") != api {
		t.Fatalf("the sets: %v %v", v.Assignees, v.Labels)
	}
	if v.Parent == nil || v.Parent.Key != parent || v.Parent.Title != "The parent" || v.Parent.Status != TicketBacklog || !v.Parent.DeletedAt.IsZero() {
		t.Fatalf("the parent: %+v", v.Parent)
	}
	if v.Children.Total != 54 || len(v.Children.Tickets) != MaxTicketChildren || v.Children.Tickets[0].Key != "CND-3" || v.Children.Tickets[1].Key != "CND-5" ||
		v.Children.Tickets[MaxTicketChildren-1].Key != "CND-53" {
		t.Fatalf("the children: %d of %d, %+v", len(v.Children.Tickets), v.Children.Total, v.Children.Tickets[:2])
	}
	var rels []string
	for _, r := range v.Relations {
		rels = append(rels, fmt.Sprintf("%s:%s:deleted=%v", r.Kind, r.Other.Key, !r.Other.DeletedAt.IsZero()))
	}
	if strings.Join(rels, " ") != "blocks:CND-58:deleted=false blocks:CND-59:deleted=false relates:CND-60:deleted=true" || v.RelationsTotal != 3 {
		t.Fatalf("the relations: %v of %d", rels, v.RelationsTotal)
	}
	if !v.Blocked {
		t.Fatal("not blocked by CND-59")
	}
	if len(v.Links) != 1 || v.Links[0].Kind != LinkDelivery || len(v.Attachments) != 1 || v.Attachments[0].Name != "file0.txt" {
		t.Fatalf("the links %+v and attachments %+v", v.Links, v.Attachments)
	}
	if len(v.Comments) != MaxTicketComments {
		t.Fatalf("the comments: %d", len(v.Comments))
	}
	if v.Comments[48].ID != c59 || v.Comments[48].DeletedAt.IsZero() {
		t.Fatalf("the 59th comment: %+v", v.Comments[48])
	}
	// Each with its own number and its own body: the read numbers the
	// comments without their bodies and reads the carried ones whole.
	for i, c := range v.Comments {
		if c.N != i+11 || c.Body != fmt.Sprintf("Comment %d", i+11) || c.TicketID != v.ID {
			t.Fatalf("the comment numbered %d: %+v", i+11, c)
		}
	}
	if strings.Join(v.Close.Requires, " ") != RequireAcceptance || strings.Join(v.Close.Missing, " ") != RequireAcceptance || !v.Close.Met.Delivery {
		t.Fatalf("the close state: %+v", v.Close)
	}
	// A deleted parent reads as such; a deleted ticket reads whole but its
	// children, whose references to it are inactive (they keep it).
	rc, err = s.DeleteTicket(ctx, testOp("dp"), parent)
	mustDone(t, "delete the parent", rc, err)
	if v, err = s.Ticket(ctx, key); err != nil || v.Parent == nil || v.Parent.DeletedAt.IsZero() {
		t.Fatalf("a deleted parent: %+v %v", v.Parent, err)
	}
	rc, err = s.DeleteTicket(ctx, testOp("dk"), key)
	mustDone(t, "delete the ticket", rc, err)
	if v, err = s.Ticket(ctx, key); err != nil || v.DeletedAt.IsZero() || v.Children.Total != 0 || len(v.Children.Tickets) != 0 || len(v.Comments) != MaxTicketComments {
		t.Fatalf("a deleted ticket: %+v %v", v.Children, err)
	}
	if page, err := s.Children(ctx, key, 0, ""); err != nil || page.Total != 54 {
		t.Fatalf("a deleted ticket's children by the list: %d %v", page.Total, err)
	}
	if _, err := s.Ticket(ctx, "CND-999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no ticket: %v", err)
	}
}

func TestTheTicketReadCarriesItsRelationsWithinTheirBound(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	_, hub := newTicket(t, s, "CND", "The hub")
	n := 0
	relate := func(to string) {
		t.Helper()
		n++
		rc, err := s.AddRelation(ctx, testOp(opID("r", n)), hub, RelationRelates, to)
		mustDone(t, "relate", rc, err)
	}
	life := func(what string, keys []string, do func(context.Context, Op, string) (Receipt, error)) {
		t.Helper()
		for _, k := range keys {
			n++
			rc, err := do(ctx, testOp(opID(what, n)), k)
			mustDone(t, what, rc, err)
		}
	}
	// The hub related to MaxRelations tickets, its bound. A relation to a
	// deleted ticket counts for nothing toward the bound, so deleting ten
	// of them lets the hub relate to three more, and it holds more
	// relations than the bound with no merge, as many as such rounds make.
	var made []string
	for range MaxRelations {
		_, k := newTicket(t, s, "CND", "Related")
		relate(k)
		made = append(made, k)
	}
	gone := made[:10]
	life("delete", gone, s.DeleteTicket)
	for range 3 {
		_, k := newTicket(t, s, "CND", "Related after")
		relate(k)
		made = append(made, k)
	}
	read := func(want []string, inactive int) {
		t.Helper()
		v, err := s.Ticket(ctx, hub)
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Relations) != MaxTicketRelations || v.RelationsTotal != MaxRelations+3 {
			t.Fatalf("the read carries %d relations of %d, want %d of %d", len(v.Relations), v.RelationsTotal, MaxTicketRelations, MaxRelations+3)
		}
		got, dead := make([]string, len(v.Relations)), 0
		for i, r := range v.Relations {
			got[i] = r.Other.Key
			if !r.Other.DeletedAt.IsZero() {
				dead++
			}
		}
		if strings.Join(got, " ") != strings.Join(want, " ") || dead != inactive {
			t.Fatalf("the relations read: %v (%d inactive), want %v (%d)", got, dead, want, inactive)
		}
		all, err := s.Relations(ctx, hub)
		if err != nil || len(all) != MaxRelations+3 {
			t.Fatalf("Relations reads %d of them: %v", len(all), err)
		}
	}
	// The read carries the 93 active ones, in the order they were made,
	// then the inactive ones, made first, as many as fit: seven of the ten,
	// Relations reading the other three.
	read(append(slices.Clone(made[10:]), made[:7]...), 7)
	// The undelete makes the ten active again, the hub past its bound: the
	// read carries the first made, and the hub's next relation is refused.
	life("undelete", gone, s.UndeleteTicket)
	read(made[:MaxTicketRelations], 0)
	_, k := newTicket(t, s, "CND", "One more")
	rc, err := s.AddRelation(ctx, testOp("r-more"), hub, RelationRelates, k)
	want(t, rc, err, StatusFailed, RefusalBound)
}

// pausedRead starts a read of the ticket with the key that stops at

// betweenReads, once its row is read, until release is closed; answers a

// channel of what it read.

func pausedRead(t *testing.T, s *Store, key string) (reached, release chan struct{}, read chan TicketView) {
	t.Helper()
	reached, release, read = make(chan struct{}), make(chan struct{}), make(chan TicketView, 1)
	var once sync.Once
	s.betweenReads = func() {
		once.Do(func() {
			close(reached)
			<-release
		})
	}
	go func() {
		v, err := s.Ticket(context.Background(), key)
		if err != nil {
			t.Errorf("the paused read: %v", err)
		}
		read <- v
	}()
	return reached, release, read
}

// commentsOfView is the bodies of a ticket read's comments.

func commentsOfView(v TicketView) string {
	var bodies []string
	for _, c := range v.Comments {
		bodies = append(bodies, c.Body)
	}
	return strings.Join(bodies, ", ")
}

func TestACompositeReadAnswersOneState(t *testing.T) {
	cases := []struct {
		name string
		// write commits a comment and a status to the ticket while the read
		// is paused, and waits until it has, or until it is waiting on the
		// paused read.
		write func(t *testing.T, s *Store, key string) (wait func())
	}{
		{"a second store on the file", func(t *testing.T, s *Store, key string) func() {
			other := openStore(t, s.dir)
			ctx := context.Background()
			rc, err := other.AddComment(ctx, testOp("c2"), key, "Second")
			mustDone(t, "the other store's comment", rc, err)
			rc, err = edit(other, "e1", key, TicketEdit{Set: TicketSet{Status: ptr(TicketReady)}})
			mustDone(t, "the other store's status", rc, err)
			return func() {}
		}},
		{"the writer in this process", func(t *testing.T, s *Store, key string) func() {
			// The store's one connection is the paused read's: the writes
			// wait for it, seen on the pool's count of waits.
			waits := s.db.Stats().WaitCount
			done := make(chan error, 1)
			go func() {
				ctx := context.Background()
				rc, err := s.AddComment(ctx, testOp("c2"), key, "Second")
				if err == nil && rc.Status != StatusDone {
					err = fmt.Errorf("the comment: %+v", rc)
				}
				if err == nil {
					rc, err = edit(s, "e1", key, TicketEdit{Set: TicketSet{Status: ptr(TicketReady)}})
					if err == nil && rc.Status != StatusDone {
						err = fmt.Errorf("the status: %+v", rc)
					}
				}
				done <- err
			}()
			deadline := time.Now().Add(time.Minute)
			for s.db.Stats().WaitCount == waits {
				if time.Now().After(deadline) {
					t.Fatal("the write never waited on the read")
				}
				time.Sleep(time.Millisecond)
			}
			return func() {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := openStore(t, storeDir(t))
			ctx := context.Background()
			newOpenProject(t, s, "CND")
			_, key := newTicket(t, s, "CND", "Read whole")
			rc, err := s.AddComment(ctx, testOp("c1"), key, "First")
			mustDone(t, "comment", rc, err)
			before := ticketOf(t, s, key)
			reached, release, read := pausedRead(t, s, key)
			<-reached
			wait := c.write(t, s, key)
			close(release)
			v := <-read
			wait()
			s.betweenReads = nil
			// The state before the commits, whole: its rev and status, and the
			// comments without the new one.
			if v.Rev != before.Rev || v.Status != TicketBacklog || commentsOfView(v) != "First" || v.Close.Met.Acceptance {
				t.Fatalf("the paused read: rev %d (before %d), %s, %q", v.Rev, before.Rev, v.Status, commentsOfView(v))
			}
			// The next read, the new state.
			after, err := s.Ticket(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if after.Rev != before.Rev+1 || after.Status != TicketReady || commentsOfView(after) != "First, Second" {
				t.Fatalf("the next read: rev %d, %s, %q", after.Rev, after.Status, commentsOfView(after))
			}
		})
	}
}

func TestHistoryPagesTheTicketsChanges(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CND")
	tid, key := newTicket(t, s, "CND", "History")
	_, other := newTicket(t, s, "CND", "Another")
	for i := range 6 {
		rc, err := edit(s, opID("e", i), key, TicketEdit{Set: TicketSet{Priority: ptr(1 + i%4), Horizon: ptr(Horizons[1+i%3])}})
		mustDone(t, "edit", rc, err)
		rc, err = edit(s, opID("o", i), other, TicketEdit{Set: TicketSet{Priority: ptr(1 + i%4)}})
		mustDone(t, "edit another", rc, err)
	}
	rc, err := s.AddComment(ctx, testOp("c1"), key, "A comment's rows are its ticket's history")
	mustDone(t, "comment", rc, err)
	want := texts(t, s, "SELECT CAST(seq AS TEXT) FROM changes WHERE ticket_id = ? ORDER BY seq", tid)
	var got []string
	var after int64
	pages := 0
	for {
		page, err := s.History(ctx, key, after, 4)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Changes {
			if c.TicketID != tid || c.At.IsZero() || c.OpID == "" {
				t.Fatalf("a change: %+v", c)
			}
			got = append(got, fmt.Sprint(c.Seq))
		}
		pages++
		if page.Next == 0 {
			break
		}
		after = page.Next
	}
	if strings.Join(got, " ") != strings.Join(want, " ") || pages != (len(want)+3)/4 {
		t.Fatalf("%d pages of %v, want %v", pages, got, want)
	}
	// A page ends before the change that would take its text past its
	// bound, so descriptions at their bound page too.
	big := func(c byte) string { return strings.Repeat(string(c), MaxTicketDescription) }
	for i := range 12 {
		rc, err := edit(s, opID("d", i), key, TicketEdit{Set: TicketSet{Description: ptr(big(byte('a' + i)))}})
		mustDone(t, "a description at its bound", rc, err)
	}
	page, err := s.History(ctx, key, 0, MaxHistoryPage)
	if err != nil {
		t.Fatal(err)
	}
	text := 0
	for _, c := range page.Changes {
		text += len(c.Value) + len(c.Prior)
	}
	if page.Next == 0 || text > maxHistoryText || len(page.Changes) >= len(want)+12 {
		t.Fatalf("a page of %d changes, %d bytes of text, next %d", len(page.Changes), text, page.Next)
	}
	rest, err := s.History(ctx, key, page.Next, MaxHistoryPage)
	if err != nil || len(rest.Changes) == 0 || rest.Changes[0].Seq <= page.Next {
		t.Fatalf("the page after: %d changes %v", len(rest.Changes), err)
	}
	for _, c := range []struct {
		after int64
		limit int
	}{{0, MaxHistoryPage + 1}, {0, -1}, {-1, 10}} {
		if _, err := s.History(ctx, key, c.after, c.limit); !errors.Is(err, ErrBadQuery) {
			t.Errorf("after %d limit %d: %v", c.after, c.limit, err)
		}
	}
	if _, err := s.History(ctx, "CND-99", 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no ticket: %v", err)
	}
}
