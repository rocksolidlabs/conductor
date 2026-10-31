package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// A ticket's launches (Program item DS2, docs/plans/dispatch-evidence.md,
// PR-2): a launch is made only from an approved, fresh revision, pending,
// under a run id the store mints, with its package built from the rows
// and hashed, its attachments pinned, and one create row that never bumps
// the ticket's rev; its outcome is recorded once, by whichever recorder
// commits first.

// runIDShape is a run id minted for the test crew: its id, '-', 8 hex.
var runIDShape = regexp.MustCompile(`^crew-[0-9a-f]{8}$`)

// approvedPlan proposes a revision of the ticket with the key from the
// request and body and approves it as the operator with the note, and
// returns its id.
func approvedPlan(t *testing.T, s *Store, key string, req PlanRequest, body PlanBody, note string) string {
	t.Helper()
	id := propose(t, s, "p-"+newID(), key, req, body)
	approve(t, s, "a-"+newID(), id, note)
	return id
}

// simplePlan is an approved revision of the test crew for the ticket with
// the key, handing nothing over.
func simplePlan(t *testing.T, s *Store, key string) string {
	t.Helper()
	return approvedPlan(t, s, key, PlanRequest{Crew: "crew"}, testPlanBody("crew"), "go")
}

// mustLaunch launches from the revision, failing the test unless it was
// done, and reads the launch whole.
func mustLaunch(t *testing.T, s *Store, opID, key, plan, label string) LaunchView {
	t.Helper()
	rc, err := s.Launch(context.Background(), testOp(opID), key, plan, label)
	want(t, rc, err, StatusDone, "")
	if rc.ID == "" {
		t.Fatalf("%s: no launch id: %+v", opID, rc)
	}
	return launchOf(t, s, rc.ID)
}

// launchOf reads the launch with the id whole.
func launchOf(t *testing.T, s *Store, id string) LaunchView {
	t.Helper()
	v, err := s.LaunchByID(context.Background(), id)
	if err != nil {
		t.Fatalf("launch %s: %v", id, err)
	}
	return v
}

// packageOf decodes a launch's package.
func packageOf(t *testing.T, v LaunchView) Package {
	t.Helper()
	var p Package
	dec := json.NewDecoder(strings.NewReader(string(v.Package)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("the package of %s: %v", v.ID, err)
	}
	return p
}

// A launch from an approved revision is pending, under a run id of the
// engine's shape minted from the revision's crew, with its label the
// ticket's key and title when none is given; its package holds the
// ticket as it stood (its labels by name, its milestone, parent and
// blockers), the revision with its approval and agents, the references,
// the attachments under derived names, the comments with their bodies,
// the constraints, the decisions and the launch, hashed as stored; each
// attachment's content is pinned; one create row, without the package,
// bumps the launch's rev and not the ticket's, and one commit's change
// names the launch with its ticket and project.
func TestLaunchMakesAPendingLaunch(t *testing.T) {
	s, tid, key := planTicket(t)
	ctx := context.Background()
	rc, err := s.CreateLabel(ctx, testOp("lb"), "CND", NewLabel{Name: "auth", Color: "red"})
	want(t, rc, err, StatusDone, "")
	label := rc.ID
	rc, err = s.CreateMilestone(ctx, testOp("ms"), "CND", NewMilestone{Name: "R1", Target: "2026-12-01"})
	want(t, rc, err, StatusDone, "")
	milestone := rc.ID
	_, parent := makeTicket(t, s, NewTicket{Project: "CND", Title: "The parent"})
	_, blocker := makeTicket(t, s, NewTicket{Project: "CND", Title: "The blocker"})
	rc, err = relate(s, "rel", blocker, RelationBlocks, key)
	want(t, rc, err, StatusDone, "")
	mustEdit(t, s, "e1", key, TicketEdit{
		Set: TicketSet{Owner: ptr("Alice"), Parent: ptr(parent), Milestone: ptr(milestone), Priority: ptr(3)},
		Add: TicketMembers{Assignees: []string{"person:Bob"}, Labels: []string{label}},
	})
	upper := mustPut(t, s, "up1", key, "Spec.png", []byte("the upper spec"))
	lower := mustPut(t, s, "up2", key, "spec.png", []byte("the lower spec"))
	cid := comment(t, s, "c1", key, "Use the **new** flow")
	req := PlanRequest{Crew: "crew", Attachments: []string{upper.ID, lower.ID}, Comments: []string{cid}, Constraints: "No new dependencies", Note: "first try"}
	body := testPlanBody("crew")
	body.Attachments = req.Attachments
	body.Comments = []PlanComment{{ID: cid}}
	body.Constraints = req.Constraints
	body.Decisions = []PlanDecision{{ID: "ship", Question: "Ship it?", Kind: DecisionApproval, Before: []string{"review"}}}
	body.Note = req.Note
	plan := approvedPlan(t, s, key, req, body, "looks right")
	before := ticketOf(t, s, key)
	log := listen(s)

	rc, err = s.Launch(ctx, testOp("l1"), key, plan, "")
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 1 {
		t.Fatalf("the receipt's rev is the launch's: %+v", rc)
	}
	changes := log.one(t)
	if len(changes) != 1 || changes[0].Entity != EntityLaunch || changes[0].ID != rc.ID || changes[0].Ticket != tid || changes[0].Project != "CND" {
		t.Fatalf("the launch's changes: %+v", changes)
	}
	if after := ticketOf(t, s, key); after.Rev != before.Rev || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("the launch moved the ticket: rev %d, then %d", before.Rev, after.Rev)
	}
	v := launchOf(t, s, rc.ID)
	l := v.LaunchSummary
	switch {
	case l.State != LaunchPending, l.TicketID != tid, l.TicketKey != key, l.PlanID != plan, l.PlanN != 1, l.Rev != 1:
		t.Fatalf("the launch: %+v", l)
	case !runIDShape.MatchString(l.RunID):
		t.Fatalf("the run id %q is not the crew's id and 8 hex", l.RunID)
	case l.Label != key+" Fix the login loop":
		t.Fatalf("the default label %q", l.Label)
	case l.TicketRev != before.Rev, l.Intent != IntentDigest(before), l.Criteria != criteriaHash(before.Acceptance):
		t.Fatalf("the launch's ticket: rev %d intent %s criteria %s", l.TicketRev, l.Intent, l.Criteria)
	case l.PackageSHA256 != sha256Hex(string(v.Package)), len(v.Package) > MaxPackage:
		t.Fatalf("the package's hash %s of %d bytes", l.PackageSHA256, len(v.Package))
	case l.Fingerprint == "", l.CreatedBy != "Tester", l.Why != "", !l.StartedAt.IsZero(), l.Agents == nil || len(l.Agents) != 0:
		t.Fatalf("the launch's other fields: %+v", l)
	}
	p := packageOf(t, v)
	pv := planOf(t, s, plan)
	switch tk := p.Ticket; {
	case p.Version != PackageVersion:
		t.Fatalf("the package's version %d", p.Version)
	case tk.ID != tid, tk.Key != key, tk.Rev != before.Rev, tk.Title != "Fix the login loop", tk.Description != "It loops.", tk.Acceptance != "- no loop":
		t.Fatalf("the package's ticket: %+v", tk)
	case tk.Status != TicketBacklog, tk.Priority != 3, tk.Owner != "Alice", !slices.Equal(tk.Assignees, []string{"person:Bob"}), !slices.Equal(tk.Labels, []string{"auth"}):
		t.Fatalf("the package's ticket's fields: %+v", tk)
	case tk.Milestone == nil || *tk.Milestone != (PackageMilestone{"R1", "2026-12-01"}):
		t.Fatalf("the package's milestone: %+v", tk.Milestone)
	case tk.Parent == nil || tk.Parent.Key != parent || tk.Parent.Title != "The parent":
		t.Fatalf("the package's parent: %+v", tk.Parent)
	case len(tk.Blockers) != 1 || tk.Blockers[0] != (PackageRelated{blocker, "The blocker", TicketBacklog}) || tk.BlockersTotal != 1:
		t.Fatalf("the package's blockers: %+v %d", tk.Blockers, tk.BlockersTotal)
	case tk.Intent != l.Intent, tk.Criteria != l.Criteria:
		t.Fatalf("the package's digests: %+v", tk)
	}
	switch pp := p.Plan; {
	case pp.ID != plan, pp.N != 1, pp.Digest != pv.Digest, pp.BasedOn != "", pp.ApprovedBy != "Operator", pp.Note != "looks right", !pp.ApprovedAt.Equal(pv.ApprovedAt):
		t.Fatalf("the package's revision: %+v", pp)
	case pp.Crew != (PackageCrew{ID: "crew", Name: "Crew crew", UpdatedAt: pp.Crew.UpdatedAt}) || pp.Crew.UpdatedAt.IsZero(), pp.Base != testBase, pp.Yolo:
		t.Fatalf("the package's crew: %+v", pp)
	case !slices.Equal(pp.Agents, []PackageAgent{{"claude", testAgent("claude").SHA256}, {"codex", testAgent("codex").SHA256}}):
		t.Fatalf("the package's agents: %+v", pp.Agents)
	}
	if len(p.References) != 1 || p.References[0] != body.References[0] {
		t.Fatalf("the package's references: %+v", p.References)
	}
	wantFiles := []PackageAttachment{
		{ID: upper.ID, Name: "Spec.png", File: "01-Spec.png", Size: upper.Size, SHA256: upper.SHA256, ContentType: "text/plain"},
		{ID: lower.ID, Name: "spec.png", File: "02-spec.png", Size: lower.Size, SHA256: lower.SHA256, ContentType: "text/plain"},
	}
	if !slices.Equal(p.Attachments, wantFiles) {
		t.Fatalf("the package's attachments: %+v", p.Attachments)
	}
	if len(p.Comments) != 1 || p.Comments[0].ID != cid || p.Comments[0].N != 1 || p.Comments[0].Body != "Use the **new** flow" ||
		p.Comments[0].SHA256 != sha256Hex("Use the **new** flow") || p.Comments[0].Author != "test" || p.Comments[0].By != "Tester" || p.Comments[0].At.IsZero() {
		t.Fatalf("the package's comments: %+v", p.Comments)
	}
	if p.Constraints != "No new dependencies" || len(p.Decisions) != 1 || p.Decisions[0].ID != "ship" {
		t.Fatalf("the package's constraints and decisions: %q %+v", p.Constraints, p.Decisions)
	}
	if p.Launch.ID != l.ID || p.Launch.Run != l.RunID || p.Launch.Label != l.Label || !p.Launch.At.Equal(l.CreatedAt) || p.Launch.By != "Tester" {
		t.Fatalf("the package's launch: %+v", p.Launch)
	}
	pins := texts(t, s, "SELECT sha256 FROM launch_files WHERE launch_id = ? ORDER BY sha256", l.ID)
	wantPins := []string{upper.SHA256, lower.SHA256}
	slices.Sort(wantPins)
	if !slices.Equal(pins, wantPins) {
		t.Fatalf("the pins: %v", pins)
	}
	rows := changesOf(t, s, "test", "l1")
	if len(rows) != 1 || rows[0].Entity != EntityLaunch || rows[0].EntityID != l.ID || rows[0].Op != OpCreate || rows[0].TicketID != tid || rows[0].ProjectID == "" {
		t.Fatalf("the launch's rows: %+v", rows)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(rows[0].Value), &created); err != nil || created["package"] != nil || created["packageSha256"] != l.PackageSHA256 ||
		created["runId"] != l.RunID || created["fingerprint"] != l.Fingerprint || created["planId"] != plan {
		t.Fatalf("the create row's value: %s %v", rows[0].Value, err)
	}
	tv, err := s.Ticket(ctx, key)
	if err != nil || tv.LaunchCount != 1 {
		t.Fatalf("the ticket's launches: %d %v", tv.LaunchCount, err)
	}
	all, err := s.Launches(ctx, key, 0)
	if err != nil || len(all) != 1 || fmt.Sprint(all[0]) != fmt.Sprint(l) {
		t.Fatalf("the ticket's launches: %+v %v", all, err)
	}
	// A label given is the run's; a second launch is a second run.
	v2 := mustLaunch(t, s, "l2", key, plan, "Second try")
	if v2.Label != "Second try" || v2.RunID == l.RunID || !runIDShape.MatchString(v2.RunID) {
		t.Fatalf("the second launch: %+v", v2.LaunchSummary)
	}
	if all, err := s.Launches(ctx, key, 1); err != nil || len(all) != 1 || all[0].ID != v2.ID {
		t.Fatalf("the newest launch first: %+v %v", all, err)
	}
	if _, err := s.LaunchByID(ctx, "0123456789abcdef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such launch: %v", err)
	}
	if _, err := s.Launches(ctx, "CND-99", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such ticket: %v", err)
	}
}

// Each refusal of a launch keeps nothing but its failed receipt: a closed
// ticket, an archived project, a deleted ticket, a revision missing or
// another ticket's, one proposed, superseded or withdrawn, a stale
// approval, an attachment or a comment handed over and gone or edited,
// the launch past the ticket's bound, a label past its own, and a stale
// expected revision.
func TestLaunchRefusals(t *testing.T) {
	s, _, _ := planTicket(t)
	ctx := context.Background()
	newOpenProject(t, s, "OPN")
	newProject(t, s, "ARC")
	fresh := func(project string) (string, string) {
		t.Helper()
		_, key := makeTicket(t, s, NewTicket{Project: project, Title: "Launch me", Description: "Do it.", Acceptance: "- done"})
		return key, simplePlan(t, s, key)
	}
	_, other := makeTicket(t, s, NewTicket{Project: "CND", Title: "Another"})
	otherPlan := simplePlan(t, s, other)
	n := 0
	for _, c := range []struct {
		name    string
		setup   func() (key, plan, label string, rev int64)
		refusal string
		words   string
	}{
		{"a closed ticket", func() (string, string, string, int64) {
			key, plan := fresh("OPN")
			mustEdit(t, s, "done-"+key, key, TicketEdit{Set: TicketSet{Status: ptr(TicketDone)}})
			return key, plan, "", 0
		}, RefusalInvalid, "is done: reopen it to launch"},
		{"an archived project", func() (string, string, string, int64) {
			key, plan := fresh("ARC")
			rc, err := s.ArchiveProject(ctx, testOp("arc"), "ARC")
			want(t, rc, err, StatusDone, "")
			return key, plan, "", 0
		}, RefusalArchived, "ARC is archived"},
		{"a deleted ticket", func() (string, string, string, int64) {
			key, plan := fresh("CND")
			rc, err := s.DeleteTicket(ctx, testOp("del-"+key), key)
			want(t, rc, err, StatusDone, "")
			return key, plan, "", 0
		}, RefusalNotFound, "is deleted"},
		{"no such revision", func() (string, string, string, int64) {
			key, _ := fresh("CND")
			return key, "0123456789abcdef", "", 0
		}, RefusalNotFound, "no plan revision"},
		{"another ticket's revision", func() (string, string, string, int64) {
			key, _ := fresh("CND")
			return key, otherPlan, "", 0
		}, RefusalNotFound, "is not"},
		{"a proposed revision", func() (string, string, string, int64) {
			key, _ := fresh("CND")
			return key, propose(t, s, "pp-"+key, key, PlanRequest{Crew: "crew"}, testPlanBody("crew")), "", 0
		}, RefusalInvalid, "revision 2 is proposed: approve it first"},
		{"a superseded revision", func() (string, string, string, int64) {
			key, first := fresh("CND")
			simplePlan(t, s, key)
			return key, first, "", 0
		}, RefusalInvalid, "revision 1 is superseded by revision 2"},
		{"a withdrawn revision", func() (string, string, string, int64) {
			key, plan := fresh("CND")
			rc, err := s.WithdrawPlan(ctx, testOp("wd-"+key), plan)
			want(t, rc, err, StatusDone, "")
			return key, plan, "", 0
		}, RefusalInvalid, "revision 1 is withdrawn"},
		{"a stale approval", func() (string, string, string, int64) {
			key, plan := fresh("CND")
			mustEdit(t, s, "desc-"+key, key, TicketEdit{Set: TicketSet{Description: ptr("It loops twice.")}})
			return key, plan, "", 0
		}, RefusalStalePlan, "the description changed since revision 1 was approved: approve it again or revise it"},
		{"an attachment removed", func() (string, string, string, int64) {
			_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Attached"})
			a := mustPut(t, s, "put-"+key, key, "notes.txt", []byte("notes of "+key))
			body := testPlanBody("crew")
			body.Attachments = []string{a.ID}
			plan := approvedPlan(t, s, key, PlanRequest{Crew: "crew", Attachments: body.Attachments}, body, "")
			rc, err := s.RemoveAttachment(ctx, testOp("rm-"+key), key, a.ID)
			want(t, rc, err, StatusDone, "")
			return key, plan, "", 0
		}, RefusalStalePlan, "the attachment notes.txt was removed since revision 1 was made: revise it"},
		{"a comment deleted", func() (string, string, string, int64) {
			_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Discussed"})
			cid := comment(t, s, "c-"+key, key, "context")
			body := testPlanBody("crew")
			body.Comments = []PlanComment{{ID: cid}}
			plan := approvedPlan(t, s, key, PlanRequest{Crew: "crew", Comments: []string{cid}}, body, "")
			rc, err := s.DeleteComment(ctx, testOp("dc-"+key), key, cid)
			want(t, rc, err, StatusDone, "")
			return key, plan, "", 0
		}, RefusalStalePlan, "comment 1 was deleted since revision 1 was made: revise it"},
		{"a comment edited", func() (string, string, string, int64) {
			_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Discussed again"})
			cid := comment(t, s, "c-"+key, key, "context")
			body := testPlanBody("crew")
			body.Comments = []PlanComment{{ID: cid}}
			plan := approvedPlan(t, s, key, PlanRequest{Crew: "crew", Comments: []string{cid}}, body, "")
			rc, err := s.EditComment(ctx, testOp("ec-"+key), key, cid, "other context")
			want(t, rc, err, StatusDone, "")
			return key, plan, "", 0
		}, RefusalStalePlan, "comment 1 was edited since revision 1 was made: revise it"},
		{"the launch past the bound", func() (string, string, string, int64) {
			key, plan := fresh("CND")
			fillLaunches(t, s, key, plan, MaxLaunches)
			return key, plan, "", 0
		}, RefusalBound, "at most 200 launches of a ticket"},
		{"a label past its bound", func() (string, string, string, int64) {
			key, plan := fresh("CND")
			return key, plan, strings.Repeat("é", MaxLaunchLabel+1), 0
		}, RefusalBound, "the label is more than 60 characters"},
		{"a stale expected revision", func() (string, string, string, int64) {
			key, plan := fresh("CND")
			return key, plan, "", ticketOf(t, s, key).Rev + 1
		}, RefusalStaleRevision, "the revision expected is"},
	} {
		key, plan, label, rev := c.setup()
		n++
		op := testOp(fmt.Sprintf("refused%d", n))
		op.ExpectedRev = rev
		launches := count(t, s, "SELECT COUNT(*) FROM launches")
		pins := count(t, s, "SELECT COUNT(*) FROM launch_files")
		rows := count(t, s, "SELECT COUNT(*) FROM changes")
		rc, err := s.Launch(ctx, op, key, plan, label)
		if err != nil || rc.Status != StatusFailed || rc.Refusal != c.refusal || !strings.Contains(rc.Error, c.words) {
			t.Fatalf("%s: %+v %v, want %s %q", c.name, rc, err, c.refusal, c.words)
		}
		if got := [3]int{count(t, s, "SELECT COUNT(*) FROM launches"), count(t, s, "SELECT COUNT(*) FROM launch_files"), count(t, s, "SELECT COUNT(*) FROM changes")}; got != [3]int{launches, pins, rows} {
			t.Fatalf("%s kept %v, before %v", c.name, got, [3]int{launches, pins, rows})
		}
		if got := texts(t, s, "SELECT status FROM ops WHERE actor = 'test' AND id = ?", op.ID); !slices.Equal(got, []string{StatusFailed}) {
			t.Fatalf("%s: its receipt %v", c.name, got)
		}
	}
	// The 200th launch is taken; the bound counts every launch, failed or
	// not.
	key, plan := fresh("CND")
	fillLaunches(t, s, key, plan, MaxLaunches-1)
	mustLaunch(t, s, "the200th", key, plan, "")
}

// fillLaunches writes n launches of the revision of the ticket with the key as
// rows, failed, past the operation's rules: what a ticket's bound counts.
func fillLaunches(t *testing.T, s *Store, key, plan string, n int) {
	t.Helper()
	tk := ticketOf(t, s, key)
	for i := range n {
		if _, err := s.db.Exec(`INSERT INTO launches (id, ticket_id, plan_id, run_id, ticket_rev, intent, criteria, package, package_sha256, state, created_at)
			VALUES (?, ?, ?, ?, 1, '', '', '{}', '', 'failed', ?)`, newID(), tk.ID, plan, fmt.Sprintf("crew-%08x", i), stamp(tk.CreatedAt)); err != nil {
			t.Fatal(err)
		}
	}
}

// A launch's outcome is recorded once, under the actor server and the op
// id <launch>.started or .failed whoever records it: a second record of
// the same outcome is the first's receipt (its agents or its words as the
// first gave them), the other outcome after it is refused invalid and
// changes nothing, a launch not pending refuses both. The agents are
// checked and their versions cut, why is made text and cut, and neither
// bumps the ticket's rev.
func TestLaunchStartedOrFailedOnce(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	plan := simplePlan(t, s, key)
	first := mustLaunch(t, s, "l1", key, plan, "")
	ticketRev := ticketOf(t, s, key).Rev
	agents := []LaunchAgent{{Member: "dev", Agent: "claude", Version: "2.1.304 (Claude Code)"}, {Member: "rev", Agent: "codex", Version: strings.Repeat("é", 80)}}
	rc, err := s.StartedLaunch(ctx, Op{Actor: "anyone", ID: "ignored", By: "Server"}, first.ID, agents)
	want(t, rc, err, StatusDone, "")
	if rc.OpID != first.ID+".started" || rc.Intent != IntentLaunchStarted || rc.Rev != 2 {
		t.Fatalf("the started receipt: %+v", rc)
	}
	got := launchOf(t, s, first.ID)
	if got.State != LaunchStarted || got.StartedAt.IsZero() || len(got.Agents) != 2 || got.Agents[0] != agents[0] ||
		len(got.Agents[1].Version) != 100 || !utf8.ValidString(got.Agents[1].Version) || got.Why != "" {
		t.Fatalf("the started launch: %+v", got.LaunchSummary)
	}
	rows := changesOf(t, s, ActorServer, first.ID+".started")
	if len(rows) != 3 || rows[0].Field != "state" || rows[0].Value != LaunchStarted || rows[0].Prior != LaunchPending || rows[0].By != "Server" {
		t.Fatalf("the started rows: %+v", rows)
	}
	// The reconcile records it again, with no agents: the first stands.
	again, err := s.StartedLaunch(ctx, Op{}, first.ID, nil)
	want(t, again, err, StatusDone, "")
	if !again.Repeat || launchOf(t, s, first.ID).Agents[0] != agents[0] {
		t.Fatalf("the second record of the start: %+v", again)
	}
	// A failure recorded after it is refused and changes nothing.
	rc, err = s.FailedLaunch(ctx, Op{}, first.ID, "the server stopped before the run started")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if !strings.Contains(rc.Error, "is started already") || launchOf(t, s, first.ID).State != LaunchStarted {
		t.Fatalf("a failure after the start: %+v", rc)
	}

	// The other order: failed first, with why made text and cut.
	second := mustLaunch(t, s, "l2", key, plan, "")
	why := "agent \x00claude is not installed\n" + strings.Repeat("x", 3000)
	rc, err = s.FailedLaunch(ctx, Op{}, second.ID, why)
	want(t, rc, err, StatusDone, "")
	got = launchOf(t, s, second.ID)
	if got.State != LaunchFailed || !strings.HasPrefix(got.Why, "agent  claude is not installed\n") || utf8.RuneCountInString(got.Why) != MaxLaunchWhy ||
		!strings.HasSuffix(got.Why, "…") || !got.StartedAt.IsZero() {
		t.Fatalf("the failed launch: %q", got.Why)
	}
	rc, err = s.FailedLaunch(ctx, Op{}, second.ID, "another reason")
	want(t, rc, err, StatusDone, "")
	if !rc.Repeat || launchOf(t, s, second.ID).Why != got.Why {
		t.Fatalf("the second record of the failure: %+v", rc)
	}
	rc, err = s.StartedLaunch(ctx, Op{}, second.ID, nil)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if launchOf(t, s, second.ID).State != LaunchFailed {
		t.Fatal("a start after the failure changed the launch")
	}
	// An empty why says so.
	third := mustLaunch(t, s, "l3", key, plan, "")
	rc, err = s.FailedLaunch(ctx, Op{}, third.ID, " \n ")
	want(t, rc, err, StatusDone, "")
	if w := launchOf(t, s, third.ID).Why; w != "the run could not be started" {
		t.Fatalf("an empty why: %q", w)
	}

	// The agents' shapes and bound, checked before anything is written,
	// their refusal kept by no receipt, so each record after it is looked
	// at afresh.
	fourth := mustLaunch(t, s, "l4", key, plan, "")
	for _, bad := range []struct {
		agents  []LaunchAgent
		refusal string
	}{
		{[]LaunchAgent{{Member: "Dev", Agent: "claude"}}, RefusalInvalid},
		{[]LaunchAgent{{Member: "dev", Agent: "claude code"}}, RefusalInvalid},
		{slices.Repeat([]LaunchAgent{{Member: "dev", Agent: "claude"}}, MaxPlanAgents+1), RefusalBound},
	} {
		rc, err := s.StartedLaunch(ctx, Op{}, fourth.ID, bad.agents)
		want(t, rc, err, StatusFailed, bad.refusal)
		if rc.Repeat || rc.OpID != fourth.ID+".started" || launchOf(t, s, fourth.ID).State != LaunchPending {
			t.Fatalf("bad agents %+v: %+v", bad.agents, rc)
		}
		if _, found, err := s.Receipt(ctx, ActorServer, fourth.ID+".started"); err != nil || found {
			t.Fatalf("bad agents %+v left a receipt: %v %v", bad.agents, found, err)
		}
	}
	// Twelve members at their bounds with versions at their escaped worst
	// fit the agents' bound.
	worst := make([]LaunchAgent, MaxPlanAgents)
	for i := range worst {
		worst[i] = LaunchAgent{Member: fmt.Sprintf("%02d", i) + strings.Repeat("m", 38), Agent: strings.Repeat("a", 32), Version: strings.Repeat(" ", MaxLaunchAgentVersion/3) + `"`}
	}
	rc, err = s.StartedLaunch(ctx, Op{}, fourth.ID, worst)
	want(t, rc, err, StatusDone, "")
	if v := texts(t, s, "SELECT agents FROM launches WHERE id = ?", fourth.ID); len(v[0]) > MaxLaunchAgents {
		t.Fatalf("the agents at their worst: %d bytes", len(v[0]))
	}
	if n := ticketOf(t, s, key).Rev; n != ticketRev {
		t.Fatalf("the outcomes moved the ticket: rev %d, then %d", ticketRev, n)
	}
	// No such launch: refused not_found, or not looked for when the id is
	// not an id's shape.
	rc, err = s.StartedLaunch(ctx, Op{}, "0123456789abcdef", nil)
	want(t, rc, err, StatusFailed, RefusalNotFound)
	if _, err := s.FailedLaunch(ctx, Op{}, "not an id", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an id of no shape: %v", err)
	}
}

// A refused record of a launch's outcome never answers the next: a
// revision expected is none of the outcome's (the operation keeps only
// the display name), and agents refused are kept by no receipt, so the
// valid record after either is done, the launch started, and a failure
// recorded after it is refused, so a launch whose run exists is never
// failed (which would free the run from the chain rule).
func TestALaunchOutcomeRefusedFirstIsRecordedNext(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	plan := simplePlan(t, s, key)
	agents := []LaunchAgent{{Member: "dev", Agent: "claude", Version: "2.1.304"}}

	// A stale revision expected is not the outcome's to check.
	stale := mustLaunch(t, s, "l1", key, plan, "")
	rc, err := s.StartedLaunch(ctx, Op{Actor: "x", ID: "y", ExpectedRev: 7}, stale.ID, agents)
	want(t, rc, err, StatusDone, "")
	if rc.Repeat || launchOf(t, s, stale.ID).State != LaunchStarted {
		t.Fatalf("a start with a revision expected: %+v", rc)
	}
	rc, err = s.StartedLaunch(ctx, Op{}, stale.ID, agents)
	want(t, rc, err, StatusDone, "")
	if !rc.Repeat {
		t.Fatalf("the reconcile's record of the start: %+v", rc)
	}
	rc, err = s.FailedLaunch(ctx, Op{ExpectedRev: 7}, stale.ID, "reconcile")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if launchOf(t, s, stale.ID).State != LaunchStarted {
		t.Fatal("a failure after the start failed the launch")
	}
	failed := mustLaunch(t, s, "l2", key, plan, "")
	rc, err = s.FailedLaunch(ctx, Op{ExpectedRev: 7}, failed.ID, "no agent")
	want(t, rc, err, StatusDone, "")
	if launchOf(t, s, failed.ID).State != LaunchFailed {
		t.Fatal("a failure with a revision expected was not recorded")
	}

	// Bad agents first, then the good ones: done, and the start stands.
	bad := mustLaunch(t, s, "l3", key, plan, "")
	rc, err = s.StartedLaunch(ctx, Op{}, bad.ID, []LaunchAgent{{Member: "Dev", Agent: "claude"}})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.StartedLaunch(ctx, Op{}, bad.ID, agents)
	want(t, rc, err, StatusDone, "")
	if rc.Repeat {
		t.Fatalf("the start after bad agents: %+v", rc)
	}
	got := launchOf(t, s, bad.ID)
	if got.State != LaunchStarted || len(got.Agents) != 1 || got.Agents[0] != agents[0] {
		t.Fatalf("the launch started after bad agents: %+v", got.LaunchSummary)
	}
	rc, err = s.FailedLaunch(ctx, Op{}, bad.ID, "reconcile")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if launchOf(t, s, bad.ID).State != LaunchStarted {
		t.Fatal("a failure after the start failed the launch")
	}
}

// A launch's outcome is recorded whatever became of its ticket and its
// project since the launch: one launch is recorded started and the other
// failed after the ticket is deleted and the project archived, as the
// reconcile at start must.
func TestALaunchOutcomeOutlivesItsTicket(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	plan := simplePlan(t, s, key)
	started := mustLaunch(t, s, "l1", key, plan, "")
	failed := mustLaunch(t, s, "l2", key, plan, "")
	rc, err := s.DeleteTicket(ctx, testOp("del"), key)
	want(t, rc, err, StatusDone, "")
	rc, err = s.ArchiveProject(ctx, testOp("arch"), "CND")
	want(t, rc, err, StatusDone, "")

	agents := []LaunchAgent{{Member: "dev", Agent: "claude", Version: "2.1.304"}}
	rc, err = s.StartedLaunch(ctx, Op{}, started.ID, agents)
	want(t, rc, err, StatusDone, "")
	rc, err = s.FailedLaunch(ctx, Op{}, failed.ID, "the server stopped")
	want(t, rc, err, StatusDone, "")
	if got := launchOf(t, s, started.ID); got.State != LaunchStarted || len(got.Agents) != 1 || got.Agents[0] != agents[0] {
		t.Fatalf("the launch started after its ticket went: %+v", got.LaunchSummary)
	}
	if got := launchOf(t, s, failed.ID); got.State != LaunchFailed || got.Why != "the server stopped" {
		t.Fatalf("the launch failed after its ticket went: %+v", got.LaunchSummary)
	}
}

// What changed since a launch: the intent fields set after its create row
// whose value is not the package's (one edited and edited back is not
// named, and one set before the launch never is), the live comments added
// after it, and the ticket's rev now; nothing for a launch just made.
func TestChangedSince(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	comment(t, s, "before", key, "said before the launch")
	plan := simplePlan(t, s, key)
	mustEdit(t, s, "prio", key, TicketEdit{Set: TicketSet{Priority: ptr(2)}})
	l := mustLaunch(t, s, "l1", key, plan, "")
	got, err := s.ChangedSince(ctx, l.ID)
	if err != nil || len(got.Fields) != 0 || got.Comments != 0 || got.Rev != ticketOf(t, s, key).Rev {
		t.Fatalf("just launched: %+v %v", got, err)
	}
	mustEdit(t, s, "e1", key, TicketEdit{Set: TicketSet{Description: ptr("It loops twice."), Title: ptr("Fix the login loops")}})
	mustEdit(t, s, "e2", key, TicketEdit{Set: TicketSet{Acceptance: ptr("- no loops")}})
	mustEdit(t, s, "e3", key, TicketEdit{Set: TicketSet{Acceptance: ptr("- no loop"), Status: ptr(TicketInProgress), Owner: ptr("Alice")}})
	keep := comment(t, s, "c1", key, "one")
	gone := comment(t, s, "c2", key, "two")
	rc, err := s.DeleteComment(ctx, testOp("dc"), key, gone)
	want(t, rc, err, StatusDone, "")
	_ = keep
	got, err = s.ChangedSince(ctx, l.ID)
	if err != nil || !slices.Equal(got.Fields, []string{"title", "description"}) || got.Comments != 1 || got.Rev != ticketOf(t, s, key).Rev {
		t.Fatalf("after the edits: %+v %v", got, err)
	}
	if _, err := s.ChangedSince(ctx, "0123456789abcdef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such launch: %v", err)
	}
}

// The pending launches are every launch neither started nor failed,
// oldest first, of every ticket.
func TestPendingLaunches(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	plan := simplePlan(t, s, key)
	_, other := makeTicket(t, s, NewTicket{Project: "CND", Title: "Another"})
	otherPlan := simplePlan(t, s, other)
	if got, err := s.PendingLaunches(ctx); err != nil || len(got) != 0 {
		t.Fatalf("none yet: %+v %v", got, err)
	}
	a := mustLaunch(t, s, "a", key, plan, "")
	b := mustLaunch(t, s, "b", other, otherPlan, "")
	c := mustLaunch(t, s, "c", key, plan, "")
	d := mustLaunch(t, s, "d", other, otherPlan, "")
	rc, err := s.StartedLaunch(ctx, Op{}, b.ID, nil)
	want(t, rc, err, StatusDone, "")
	rc, err = s.FailedLaunch(ctx, Op{}, c.ID, "no")
	want(t, rc, err, StatusDone, "")
	got, err := s.PendingLaunches(ctx)
	if err != nil || len(got) != 2 || got[0].ID != a.ID || got[1].ID != d.ID || got[0].RunID != a.RunID || got[1].TicketKey != other {
		t.Fatalf("the pending launches: %+v %v", got, err)
	}
}

// A run id another launch names, or one the engine knows, is minted
// again; a mint that never finds a free one is the store's error, nothing
// written.
func TestARunIDIsMintedAgainWhenTaken(t *testing.T) {
	var suffixes []string
	s, err := openWith(storeDir(t), func(s *Store) {
		s.runSuffix = func() string {
			next := suffixes[0]
			if len(suffixes) > 1 {
				suffixes = suffixes[1:]
			}
			return next
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	newProject(t, s, "CND")
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Again"})
	plan := simplePlan(t, s, key)
	s.SetRunExists(runs("crew-22222222"))
	suffixes = []string{"11111111"}
	if l := mustLaunch(t, s, "l1", key, plan, ""); l.RunID != "crew-11111111" {
		t.Fatalf("the first run id %s", l.RunID)
	}
	suffixes = []string{"11111111", "22222222", "33333333"}
	if l := mustLaunch(t, s, "l2", key, plan, ""); l.RunID != "crew-33333333" {
		t.Fatalf("the run id minted again %s", l.RunID)
	}
	suffixes = []string{"33333333"}
	launches := count(t, s, "SELECT COUNT(*) FROM launches")
	if _, err := s.Launch(context.Background(), testOp("l3"), key, plan, ""); err == nil || !strings.Contains(err.Error(), "no free run id") {
		t.Fatalf("no free run id: %v", err)
	}
	if count(t, s, "SELECT COUNT(*) FROM launches") != launches || count(t, s, "SELECT COUNT(*) FROM ops WHERE id = 'l3'") != 0 {
		t.Fatal("a launch with no run id kept something")
	}
}
