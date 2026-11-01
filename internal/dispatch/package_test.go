package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// A launch's package (docs/plans/dispatch-evidence.md, "The package"):
// built from the rows in the launch's transaction, so it is one state of
// the ticket; at most MaxPackage with every part at its bound and every
// text at its escaped worst; each attachment under a derived, portable
// name.

// The escaped worst of a text, as the store encodes values (HTML escaping
// off): a character-bounded field of U+2028, six bytes each in JSON, and a
// byte-bounded one of '"', two bytes each (as are '\', a newline and a
// tab, and U+2028 for its three).
func worstChars(n int) string { return strings.Repeat(" ", n) }
func worstBytes(n int) string { return strings.Repeat(`"`, n) }

// padded is a distinct value of n characters: the worst text of n-2 and
// two digits.
func padded(worst func(int) string, n, i int) string { return worst(n-2) + fmt.Sprintf("%02d", i) }

// Every part of a package at its bound, every text at its escaped worst,
// built through the store's own operations: the ticket's title, owner,
// assignees, labels, milestone, parent and twenty blockers' titles, its
// description and criteria; the revision's crew, twelve agents, the
// approval's note and approver; twenty references; eight attachments;
// twenty comments of 64 KiB together, by an author and a display name at
// their worst; the constraints; ten decisions; the launch's label and
// author. The launch is taken, its package under MaxPackage.
func TestPackageAtItsWorst(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	newProject(t, s, "CNDXYZ")
	worstOp := func(id string) Op { return Op{Actor: worstBytes(MaxActor), ID: id, By: worstChars(MaxByName)} }
	labels := make([]string, MaxTicketLabels)
	for i := range labels {
		rc, err := s.CreateLabel(ctx, testOp(opID("lb", i)), "CNDXYZ", NewLabel{Name: padded(worstChars, MaxLabelName, i), Color: "red"})
		want(t, rc, err, StatusDone, "")
		labels[i] = rc.ID
	}
	rc, err := s.CreateMilestone(ctx, testOp("ms"), "CNDXYZ", NewMilestone{Name: worstChars(MaxMilestoneName), Target: "2026-12-01"})
	want(t, rc, err, StatusDone, "")
	milestone := rc.ID
	_, parent := makeTicket(t, s, NewTicket{Project: "CNDXYZ", Title: worstChars(MaxTicketTitle)})
	assignees := make([]string, MaxAssignees)
	for i := range assignees {
		assignees[i] = "person:" + padded(worstChars, MaxPersonName, i)
	}
	_, key := makeTicket(t, s, NewTicket{
		Project: "CNDXYZ", Title: worstChars(MaxTicketTitle), Description: worstBytes(MaxTicketDescription), Acceptance: worstBytes(MaxAcceptance),
		Owner: worstChars(MaxPersonName), Assignees: assignees, Labels: labels, Milestone: milestone, Parent: parent, Priority: MaxPriority,
	})
	for i := range MaxPackageBlockers {
		_, b := makeTicket(t, s, NewTicket{Project: "CNDXYZ", Title: worstChars(MaxTicketTitle)})
		rc, err := relate(s, opID("rel", i), b, RelationBlocks, key)
		want(t, rc, err, StatusDone, "")
	}
	var attachments []string
	for i := range MaxPlanAttachments {
		rc, a, err := s.AddAttachment(ctx, worstOp(opID("up", i)), key, padded(worstBytes, MaxAttachmentName, i), worstBytes(MaxContentType), bytes.NewReader([]byte{byte(i)}))
		want(t, rc, err, StatusDone, "")
		attachments = append(attachments, a.ID)
	}
	var comments []string
	var planComments []PlanComment
	for i := range MaxPlanComments {
		rc, err := s.AddComment(ctx, worstOp(opID("c", i)), key, padded(worstBytes, MaxPlanCommentBytes/MaxPlanComments, i))
		want(t, rc, err, StatusDone, "")
		comments = append(comments, rc.ID)
		planComments = append(planComments, PlanComment{ID: rc.ID})
	}
	tasks := make([]map[string]any, MaxDecisionBefore)
	before := make([]string, MaxDecisionBefore)
	for i := range tasks {
		before[i] = fmt.Sprintf("%02d", i) + strings.Repeat("t", 38)
		tasks[i] = map[string]any{"id": before[i], "member": "dev", "kind": "work"}
	}
	crewID := strings.Repeat("c", 64)
	crew := testCrew(crewID, map[string]any{"name": worstChars(MaxCrewName), "tasks": tasks})
	agents := make([]PlanAgent, MaxPlanAgents)
	for i := range agents {
		agents[i] = testAgent(fmt.Sprintf("%02d", i) + strings.Repeat("a", 30))
	}
	refs := make([]PlanReference, MaxPlanReferences)
	for i := range refs {
		refs[i] = PlanReference{Path: worstBytes(MaxReferencePath), Start: 1 << 30, End: 1<<31 - 1, Note: worstChars(MaxReferenceNote), Object: strings.Repeat("f", 64), Kind: ObjectBlob}
	}
	decisions := make([]PlanDecision, MaxPlanDecisions)
	for i := range decisions {
		decisions[i] = PlanDecision{ID: fmt.Sprintf("%02d", i) + strings.Repeat("d", 38), Question: worstChars(MaxDecisionQuestion), Kind: DecisionClarification, Before: before}
	}
	body := PlanBody{
		Base: strings.Repeat("e", 64), Crew: crew, Agents: agents, References: refs, Attachments: attachments, Comments: planComments,
		Constraints: worstBytes(MaxPlanConstraints), Decisions: decisions, Note: worstBytes(MaxPlanNote),
	}
	plan := propose(t, s, "p1", key, PlanRequest{Crew: crewID, Attachments: attachments, Comments: comments}, body)
	rc, err = s.ApprovePlan(ctx, Op{Actor: ActorAdmin, ID: "a1", By: worstChars(MaxByName)}, plan, 0, worstChars(MaxApprovedNote))
	want(t, rc, err, StatusDone, "")

	rc, err = s.Launch(ctx, worstOp("l1"), key, plan, worstChars(MaxLaunchLabel))
	want(t, rc, err, StatusDone, "")
	v := launchOf(t, s, rc.ID)
	p := packageOf(t, v)
	switch {
	case len(p.Ticket.Labels) != MaxTicketLabels, len(p.Ticket.Assignees) != MaxAssignees, len(p.Ticket.Blockers) != MaxPackageBlockers,
		p.Ticket.Milestone == nil, p.Ticket.Parent == nil, len(p.Plan.Agents) != MaxPlanAgents, len(p.References) != MaxPlanReferences,
		len(p.Attachments) != MaxPlanAttachments, len(p.Comments) != MaxPlanComments, len(p.Decisions) != MaxPlanDecisions:
		t.Fatalf("a part is short of its bound: %+v", p.Ticket)
	}
	if len(v.Package) > MaxPackage {
		t.Fatalf("the package at its worst is %d bytes, more than %d", len(v.Package), MaxPackage)
	}
	t.Logf("the package at its worst: %d bytes of %d", len(v.Package), MaxPackage)
	// The summary's read beside it is far under the launch read's bound
	// the API will give (1 MiB).
	whole, err := json.Marshal(v)
	if err != nil || len(whole) > 1<<20 {
		t.Fatalf("the launch's read at its worst: %d bytes %v", len(whole), err)
	}
}

// A package past MaxPackage, which no part at its bound makes but a merge
// could (a set past its bound), is refused bound naming its largest part.
func TestAPackagePastItsBoundNamesItsLargestPart(t *testing.T) {
	pkg := Package{Comments: []PackageComment{{Body: strings.Repeat("x", MaxPackage)}}, Constraints: "small"}
	err := packageBound(pkg, MaxPackage+100)
	var ref *Refusal
	if !errors.As(err, &ref) || ref.Code != RefusalBound || !strings.HasPrefix(ref.Words, "the comments are 512.") || !strings.Contains(ref.Words, "of the package's 512.1 KiB") {
		t.Fatalf("the refusal: %v", err)
	}
}

// The package holds the ticket as it stood when the launch committed: an
// edit before the launch is in it with its rev, one after it is not, and
// the package's bytes and digest never change after the launch.
func TestThePackageIsTheTicketsRev(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	plan := simplePlan(t, s, key)
	mustEdit(t, s, "before", key, TicketEdit{Set: TicketSet{Priority: ptr(4), Owner: ptr("Alice")}})
	rev := ticketOf(t, s, key).Rev
	v := mustLaunch(t, s, "l1", key, plan, "")
	p := packageOf(t, v)
	if p.Ticket.Rev != rev || v.TicketRev != rev || p.Ticket.Priority != 4 || p.Ticket.Owner != "Alice" {
		t.Fatalf("the package's ticket: rev %d (the ticket's %d), priority %d, owner %q", p.Ticket.Rev, rev, p.Ticket.Priority, p.Ticket.Owner)
	}
	mustEdit(t, s, "after", key, TicketEdit{Set: TicketSet{Title: ptr("Fix the login loops"), Priority: ptr(1), Owner: ptr("")}})
	comment(t, s, "c1", key, "after the launch")
	again := launchOf(t, s, v.ID)
	if !bytes.Equal(again.Package, v.Package) || again.PackageSHA256 != v.PackageSHA256 || sha256Hex(string(again.Package)) != v.PackageSHA256 {
		t.Fatal("the package changed after its launch")
	}
	if got := packageOf(t, again); got.Ticket.Title != "Fix the login loop" || got.Ticket.Priority != 4 || len(got.Comments) != 0 {
		t.Fatalf("the package after the edits: %+v", got.Ticket)
	}
	if _, err := s.ChangedSince(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
}

// fileShape is the shape the engine checks an attachment's file name
// against before it opens anything.
var fileShape = regexp.MustCompile(`^[0-9]{2}-[A-Za-z0-9._-]{1,64}$`)

// An attachment's file name is derived, never its display name: NN-, its
// place, then the name with every byte outside [A-Za-z0-9._-] made '_',
// cut to 64 bytes and its dots trimmed at both ends, '_' for nothing
// left. Names a file system would fold together (Spec.png and spec.png,
// an NFC and an NFD spelling), names Windows reserves (CON, aux.txt), a
// name ending in a dot or a space, one of 200 bytes and one of
// punctuation only: each a distinct, portable name of the engine's shape,
// none ending in a dot.
func TestPackageFileNames(t *testing.T) {
	for _, c := range []struct{ names, want []string }{
		{[]string{"Spec.png", "spec.png"}, []string{"01-Spec.png", "02-spec.png"}},
		{[]string{"café.txt", "café.txt"}, []string{"01-caf__.txt", "02-cafe__.txt"}},
		{[]string{"CON", "aux.txt", "nul"}, []string{"01-CON", "02-aux.txt", "03-nul"}},
		{[]string{"notes.", "notes ", ".hidden", "..."}, []string{"01-notes", "02-notes_", "03-hidden", "04-_"}},
		{[]string{strings.Repeat("a", 63) + ".png" + strings.Repeat("b", 133)}, []string{"01-" + strings.Repeat("a", 63)}},
		{[]string{"!!!", "%?*", "a b/c\\d"}, []string{"01-___", "02-___", "03-a_b_c_d"}},
	} {
		seen := map[string]bool{}
		for i, name := range c.names {
			got := attachmentFile(i, name)
			if got != c.want[i] {
				t.Errorf("%q at %d: %q, want %q", name, i, got, c.want[i])
			}
			folded := strings.ToLower(got)
			if !fileShape.MatchString(got) || strings.HasSuffix(got, ".") || seen[folded] {
				t.Errorf("%q at %d: %q is not a distinct name of the engine's shape", name, i, got)
			}
			seen[folded] = true
		}
	}
	// The eighth place, and a name cut so it ends in a dot, trimmed.
	if got := attachmentFile(7, strings.Repeat("x", 63)+"..png"); got != "08-"+strings.Repeat("x", 63) {
		t.Fatalf("the eighth place: %q", got)
	}
}
