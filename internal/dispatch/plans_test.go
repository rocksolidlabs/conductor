package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// testBase is a revision's base in the tests: a full commit id.
var testBase = strings.Repeat("ab", 20)

// testCrew is a saved crew with tasks as the crew store keeps it, its id
// the one given, its tasks build and review, and extra fields laid in.
func testCrew(id string, extra map[string]any) json.RawMessage {
	c := map[string]any{
		"id": id, "name": "Crew " + id, "cwd": "/work/conductor", "where": "server", "isolation": "worktree", "yolo": false,
		"members": []map[string]any{{"name": "dev", "agentId": "claude", "args": []string{"--model", "sonnet"}}, {"name": "rev", "agentId": "codex"}},
		"tasks": []map[string]any{
			{"id": "build", "member": "dev", "kind": "work", "instructions": "Build it", "checks": []map[string]any{{"id": "test", "argv": []string{"go", "test", "./..."}}}},
			{"id": "review", "member": "rev", "kind": "review", "instructions": "Review it", "needs": []string{"build"}},
		},
		"createdAt": "2026-10-30T11:00:00Z", "updatedAt": "2026-10-30T12:00:00Z",
	}
	for k, v := range extra {
		c[k] = v
	}
	raw, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return raw
}

// testAgent is a catalog agent as a revision records it.
func testAgent(id string) PlanAgent {
	return PlanAgent{ID: id, Command: []string{id}, StartArgs: []string{"--session-id", "{id}"}, EnvNames: []string{"A"},
		EnvSHA256: sha256Hex("A=1"), SHA256: sha256Hex("selection of " + id)}
}

// testPlanBody is a revision's body of the crew with the id, as the API
// would build it: the crew, its two agents, one reference.
func testPlanBody(crew string) PlanBody {
	return PlanBody{
		Base: testBase, Crew: testCrew(crew, nil), Agents: []PlanAgent{testAgent("claude"), testAgent("codex")},
		References: []PlanReference{{Path: "README.md", Start: 1, End: 3, Note: "the intro", Object: strings.Repeat("cd", 20), Kind: ObjectBlob}},
	}
}

// adminOp is an operation of the operator's, by Operator.
func adminOp(id string) Op { return Op{Actor: ActorAdmin, ID: id, By: "Operator"} }

// planTicket is a store with a project CND and a ticket of it with
// criteria, and the ticket's id and key.
func planTicket(t *testing.T) (s *Store, id, key string) {
	t.Helper()
	s = openStore(t, storeDir(t))
	newProject(t, s, "CND")
	id, key = makeTicket(t, s, NewTicket{Project: "CND", Title: "Fix the login loop", Description: "It loops.", Acceptance: "- no loop"})
	return s, id, key
}

// propose proposes a revision of the ticket with the key from the crew's
// body, failing the test unless it was done, and returns its id.
func propose(t *testing.T, s *Store, opID, key string, req PlanRequest, body PlanBody) string {
	t.Helper()
	rc, err := s.ProposePlan(context.Background(), testOp(opID), key, req, body)
	want(t, rc, err, StatusDone, "")
	if rc.ID == "" {
		t.Fatalf("%s: no revision id: %+v", opID, rc)
	}
	return rc.ID
}

// approve approves the revision as the operator, failing the test unless
// it was done.
func approve(t *testing.T, s *Store, opID, id, note string) Receipt {
	t.Helper()
	rc, err := s.ApprovePlan(context.Background(), adminOp(opID), id, 0, note)
	want(t, rc, err, StatusDone, "")
	return rc
}

// planOf reads the revision with the id whole.
func planOf(t *testing.T, s *Store, id string) PlanView {
	t.Helper()
	v, err := s.Plan(context.Background(), id)
	if err != nil {
		t.Fatalf("plan %s: %v", id, err)
	}
	return v
}

// approvedOf is the ids of the ticket's approved revisions as its read
// carries them, and their staleness.
func approvedOf(t *testing.T, s *Store, key string) ([]string, []*PlanStale) {
	t.Helper()
	v, err := s.Ticket(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	var stale []*PlanStale
	for _, p := range v.Approved {
		ids = append(ids, p.ID)
		stale = append(stale, p.Stale)
	}
	return ids, stale
}

// mustEdit edits the ticket with the key, failing the test unless done.
func mustEdit(t *testing.T, s *Store, opID, key string, e TicketEdit) {
	t.Helper()
	rc, err := s.EditTicket(context.Background(), testOp(opID), key, e)
	want(t, rc, err, StatusDone, "")
}

// comment comments on the ticket with the key and returns the comment's
// id.
func comment(t *testing.T, s *Store, opID, key, body string) string {
	t.Helper()
	rc, err := s.AddComment(context.Background(), testOp(opID), key, body)
	want(t, rc, err, StatusDone, "")
	return rc.ID
}

// insertAttachment writes an attachment's row of the ticket as a test
// needs it (a size without its content): the proposal reads the rows
// alone.
func insertAttachment(t *testing.T, s *Store, ticketID string, size int64) string {
	t.Helper()
	id := newID()
	if _, err := s.db.Exec("INSERT INTO attachments (id, ticket_id, name, size, sha256, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		id, ticketID, "a-"+id, size, sha256Hex(id), stamp(time.Now())); err != nil {
		t.Fatal(err)
	}
	return id
}

// The proposal keeps the body as the store encodes it, named by its
// digest: each handed-over comment's SHA-256 set from its row whatever
// the API gave, the summary in its one create row and never the body,
// the ticket's rev untouched.
func TestProposeKeepsTheBodyAndItsDigest(t *testing.T) {
	s, tid, key := planTicket(t)
	ctx := context.Background()
	c := comment(t, s, "c1", key, "Use the <new> session store & keep it")
	a := mustPut(t, s, "a1", key, "spec.txt", []byte("the spec"))
	before := ticketOf(t, s, key).Rev
	log := listen(s)
	req := PlanRequest{Crew: "crew", Base: "HEAD", References: []PlanReferenceRequest{{Path: "./README.md", Start: 1, End: 3, Note: "the intro"}},
		Attachments: []string{a.ID}, Comments: []string{c}, Constraints: "Do not touch the <API>.", Note: "First try."}
	body := testPlanBody("crew")
	body.Attachments, body.Comments = []string{a.ID}, []PlanComment{{ID: c, SHA256: "not the body's"}}
	body.Constraints, body.Note = req.Constraints, req.Note
	body.Decisions = []PlanDecision{{ID: "ship", Question: "Ship it on Friday?", Kind: DecisionApproval, Before: []string{"review"}}}
	op := testOp("p1")
	rc, err := s.ProposePlan(ctx, op, key, req, body)
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 1 || rc.ID == "" {
		t.Fatalf("the receipt: %+v", rc)
	}
	v := planOf(t, s, rc.ID)
	wantBody := body
	wantBody.Comments = []PlanComment{{ID: c, SHA256: sha256Hex("Use the <new> session store & keep it")}}
	encoded, err := encodeValue(wantBody.normalized())
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Body) != encoded {
		t.Fatalf("the body kept:\n%s\nwant\n%s", v.Body, encoded)
	}
	if strings.Contains(string(v.Body), `\u003c`) {
		t.Fatalf("the body is kept with HTML escaping: %s", v.Body)
	}
	fp, _ := fingerprint(IntentPlanPropose, PlanProposeArgs{Key: key, PlanRequest: req})
	updated := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	p := v.PlanSummary
	if p.ID != rc.ID || p.TicketID != tid || p.N != 1 || p.BasedOn != "" || p.CrewID != "crew" || p.CrewName != "Crew crew" || !p.CrewUpdatedAt.Equal(updated) ||
		p.Base != testBase || p.Digest != sha256Hex(encoded) || p.Bytes != int64(len(encoded)) || p.State != PlanProposed || p.Fingerprint != fp ||
		p.CreatedBy != "Tester" || p.CreatedAt.IsZero() || p.Rev != 1 || p.Stale != nil || !p.ApprovedAt.IsZero() {
		t.Fatalf("the summary: %+v", p)
	}
	// One create row, its value the summary as made, never the body.
	rows := changesOf(t, s, "test", "p1")
	if len(rows) != 1 || rows[0].Entity != EntityPlan || rows[0].EntityID != rc.ID || rows[0].TicketID != tid || rows[0].Op != OpCreate || rows[0].Field != "" {
		t.Fatalf("the change rows: %+v", rows)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(rows[0].Value), &created); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range created {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if strings.Join(keys, ",") != "base,basedOn,bytes,createdAt,createdBy,crewId,crewName,crewUpdatedAt,digest,fingerprint,id,ticketId" {
		t.Fatalf("the create row carries %v", keys)
	}
	if created["digest"] != p.Digest || created["fingerprint"] != fp {
		t.Fatalf("the create row: %s", rows[0].Value)
	}
	// The ticket's rev is not the revision's to bump; the commit names
	// the revision with its ticket and project.
	if after := ticketOf(t, s, key).Rev; after != before {
		t.Fatalf("the ticket's rev moved from %d to %d", before, after)
	}
	if got := log.one(t); len(got) != 1 || got[0].Entity != EntityPlan || got[0].ID != rc.ID || got[0].Ticket != tid || got[0].Project != "CND" {
		t.Fatalf("OnChange: %+v", got)
	}
	// The list carries the summary, never the body.
	list, err := s.Plans(ctx, key)
	if err != nil || len(list) != 1 || list[0].ID != rc.ID || list[0].Digest != p.Digest {
		t.Fatalf("the list: %+v %v", list, err)
	}
	if _, err := s.Plans(ctx, "CND-99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the list of no ticket: %v", err)
	}
	if _, err := s.Plan(ctx, newID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no revision: %v", err)
	}
}

// The proposal's content is the request as sent: the same request with
// another body (the crew saved again, an agent resolving otherwise) is
// answered by the first receipt, another request under the id is
// op_conflict, and the revision's row keeps the request's fingerprint.
func TestProposeFingerprintsTheRequest(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	req := PlanRequest{Crew: "crew", Base: "HEAD"}
	id := propose(t, s, "p1", key, req, testPlanBody("crew"))
	other := testPlanBody("crew")
	other.Crew = testCrew("crew", map[string]any{"updatedAt": "2026-10-31T09:00:00Z", "goal": "changed"})
	other.Agents = other.Agents[:1]
	other.Base = strings.Repeat("ef", 20)
	rc, err := s.ProposePlan(ctx, testOp("p1"), key, req, other)
	want(t, rc, err, StatusDone, "")
	if !rc.Repeat || rc.ID != id {
		t.Fatalf("the same request with another body: %+v", rc)
	}
	rc, err = s.ProposePlan(ctx, testOp("p1"), key, PlanRequest{Crew: "crew", Base: "main"}, testPlanBody("crew"))
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if n := count(t, s, "SELECT COUNT(*) FROM plans"); n != 1 {
		t.Fatalf("%d revisions", n)
	}
	if v := planOf(t, s, id); v.Base != testBase {
		t.Fatalf("the revision's base became %s", v.Base)
	}
	fp, _ := fingerprint(IntentPlanPropose, PlanProposeArgs{Key: key, PlanRequest: req})
	if got := texts(t, s, "SELECT fingerprint FROM plans WHERE id = ?", id); len(got) != 1 || got[0] != fp {
		t.Fatalf("the row's fingerprint %v, want %s", got, fp)
	}
}

// A body built with the request's own decisions, one with no tasks
// before it (Before nil), and an agent with no yolo arguments, leaves the
// request and the body as they were sent: the body is
// kept with an empty list, and the same request sent again, applied or
// replayed, is answered by the first receipt, never op_conflict.
func TestProposeLeavesTheRequestAsSent(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	req := PlanRequest{Crew: "crew", Decisions: []PlanDecision{{ID: "ship", Question: "Ship it?", Kind: DecisionApproval}}}
	body := testPlanBody("crew")
	body.Decisions = req.Decisions
	body.Agents[0].YoloArgs = nil
	id := propose(t, s, "p1", key, req, body)
	if req.Decisions[0].Before != nil || body.Agents[0].YoloArgs != nil {
		t.Fatalf("the proposal changed what it was given: %+v %+v", req.Decisions, body.Agents)
	}
	if !strings.Contains(string(planOf(t, s, id).Body), `"before":[]`) {
		t.Fatal("the kept body's decision has no empty list")
	}
	rc, err := s.ProposePlan(ctx, testOp("p1"), key, req, body)
	want(t, rc, err, StatusDone, "")
	if !rc.Repeat || rc.ID != id {
		t.Fatalf("the same request again: %+v", rc)
	}
	rc, found := replayed(t, s, testOp("p1"), IntentPlanPropose, PlanProposeArgs{Key: key, PlanRequest: req})
	if !found || rc.Status != StatusDone || !rc.Repeat || rc.ID != id {
		t.Fatalf("the same request replayed: %+v %v", rc, found)
	}
}

// Every bound of a revision, at it and one past it: past it the proposal
// is refused bound and nothing is kept.
func TestProposeBounds(t *testing.T) {
	s, tid, key := planTicket(t)
	ctx := context.Background()
	repeat := func(n int, f func(i int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	attachments := repeat(MaxPlanAttachments+1, func(int) string { return insertAttachment(t, s, tid, 1) })
	big := []string{insertAttachment(t, s, tid, MaxPlanAttachmentBytes), insertAttachment(t, s, tid, 1)}
	comments := repeat(MaxPlanComments+1, func(i int) string { return comment(t, s, opID("c", i), key, "a comment") })
	full := repeat(4, func(i int) string { return comment(t, s, opID("full", i), key, strings.Repeat("x", MaxCommentBody)) })
	planComments := func(ids []string) []PlanComment {
		out := []PlanComment{}
		for _, id := range ids {
			out = append(out, PlanComment{ID: id})
		}
		return out
	}
	reference := func(p string) PlanReference {
		return PlanReference{Path: p, Object: strings.Repeat("cd", 20), Kind: ObjectTree}
	}
	tasks := repeat(MaxDecisionBefore+1, func(i int) string { return fmt.Sprintf("t%d", i) })
	manyTasks := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		manyTasks = append(manyTasks, map[string]any{"id": task, "member": "dev", "kind": "work"})
	}
	decision := func(id string) PlanDecision {
		return PlanDecision{ID: id, Question: "Go?", Kind: DecisionClarification, Before: []string{"build"}}
	}
	// manifestFile is a manifest file whose encoding is n bytes.
	manifestFile := func(n int) *PlanManifestFile {
		m := &PlanManifestFile{Path: ".conductor/manifest.json", Blob: strings.Repeat("ab", 20), Manifest: json.RawMessage(`{"pad":""}`)}
		v, _ := encodeValue(m)
		m.Manifest = json.RawMessage(`{"pad":"` + strings.Repeat("a", n-len(v)) + `"}`)
		return m
	}
	// paddedBody is a body whose encoding is n bytes, its crew padded.
	paddedBody := func(n int) PlanBody {
		b := testPlanBody("crew")
		b.Crew = testCrew("crew", map[string]any{"goal": ""})
		v, _ := encodeValue(b.normalized())
		b.Crew = testCrew("crew", map[string]any{"goal": strings.Repeat("g", n-len(v))})
		return b
	}
	cases := []struct {
		name      string
		at, past  func(b *PlanBody)
		pastWords string
	}{
		{"references",
			func(b *PlanBody) { b.References = slices.Repeat([]PlanReference{reference("docs")}, MaxPlanReferences) },
			func(b *PlanBody) {
				b.References = slices.Repeat([]PlanReference{reference("docs")}, MaxPlanReferences+1)
			}, "references"},
		{"a reference's path",
			func(b *PlanBody) { b.References = []PlanReference{reference(strings.Repeat("p", MaxReferencePath))} },
			func(b *PlanBody) { b.References = []PlanReference{reference(strings.Repeat("p", MaxReferencePath+1))} }, "path"},
		{"a reference's note",
			func(b *PlanBody) { b.References[0].Note = strings.Repeat("é", MaxReferenceNote) },
			func(b *PlanBody) { b.References[0].Note = strings.Repeat("é", MaxReferenceNote+1) }, "note"},
		{"agents",
			func(b *PlanBody) {
				b.Agents = nil
				for i := range MaxPlanAgents {
					b.Agents = append(b.Agents, testAgent(fmt.Sprintf("agent-%d", i)))
				}
			},
			func(b *PlanBody) {
				b.Agents = nil
				for i := range MaxPlanAgents + 1 {
					b.Agents = append(b.Agents, testAgent(fmt.Sprintf("agent-%d", i)))
				}
			}, "agents"},
		{"attachments",
			func(b *PlanBody) { b.Attachments = attachments[:MaxPlanAttachments] },
			func(b *PlanBody) { b.Attachments = attachments }, "attachments"},
		{"the attachments' bytes",
			func(b *PlanBody) { b.Attachments = big[:1] },
			func(b *PlanBody) { b.Attachments = big }, "bytes"},
		{"comments",
			func(b *PlanBody) { b.Comments = planComments(comments[:MaxPlanComments]) },
			func(b *PlanBody) { b.Comments = planComments(comments) }, "comments"},
		{"the comments' bytes",
			func(b *PlanBody) { b.Comments = planComments(full) },
			func(b *PlanBody) { b.Comments = planComments(append(slices.Clone(full), comments[0])) }, "bytes"},
		{"the constraints",
			func(b *PlanBody) { b.Constraints = strings.Repeat("c", MaxPlanConstraints) },
			func(b *PlanBody) { b.Constraints = strings.Repeat("c", MaxPlanConstraints+1) }, "constraints"},
		{"decisions",
			func(b *PlanBody) {
				b.Decisions = nil
				for i := range MaxPlanDecisions {
					b.Decisions = append(b.Decisions, decision(fmt.Sprintf("d%d", i)))
				}
			},
			func(b *PlanBody) {
				b.Decisions = nil
				for i := range MaxPlanDecisions + 1 {
					b.Decisions = append(b.Decisions, decision(fmt.Sprintf("d%d", i)))
				}
			}, "decisions"},
		{"a decision's question",
			func(b *PlanBody) {
				d := decision("d")
				d.Question = strings.Repeat("é", MaxDecisionQuestion)
				b.Decisions = []PlanDecision{d}
			},
			func(b *PlanBody) {
				d := decision("d")
				d.Question = strings.Repeat("é", MaxDecisionQuestion+1)
				b.Decisions = []PlanDecision{d}
			},
			"question"},
		{"the tasks waiting on a decision",
			func(b *PlanBody) {
				b.Crew = testCrew("crew", map[string]any{"tasks": manyTasks})
				d := decision("d")
				d.Before = tasks[:MaxDecisionBefore]
				b.Decisions = []PlanDecision{d}
			},
			func(b *PlanBody) {
				b.Crew = testCrew("crew", map[string]any{"tasks": manyTasks})
				d := decision("d")
				d.Before = tasks
				b.Decisions = []PlanDecision{d}
			}, "tasks"},
		{"the note",
			func(b *PlanBody) { b.Note = strings.Repeat("n", MaxPlanNote) },
			func(b *PlanBody) { b.Note = strings.Repeat("n", MaxPlanNote+1) }, "note"},
		{"a manifest file",
			func(b *PlanBody) { b.ManifestFile = manifestFile(MaxManifestFile) },
			func(b *PlanBody) { b.ManifestFile = manifestFile(MaxManifestFile + 1) }, "manifest"},
		{"the body",
			func(b *PlanBody) { *b = paddedBody(MaxPlanBody) },
			func(b *PlanBody) { *b = paddedBody(MaxPlanBody + 1) }, "the crew is 2.0 MiB of the revision's 2.0 MiB"},
	}
	for i, c := range cases {
		at := testPlanBody("crew")
		c.at(&at)
		rc, err := s.ProposePlan(ctx, testOp(opID("at", i)), key, PlanRequest{Crew: "crew", Note: c.name}, at)
		if err != nil || rc.Status != StatusDone {
			t.Errorf("%s at its bound: %+v %v", c.name, rc, err)
			continue
		}
		if c.name == "the body" {
			if v := planOf(t, s, rc.ID); v.Bytes != MaxPlanBody || len(v.Body) != MaxPlanBody {
				t.Errorf("the body at its bound is %d bytes (%d read)", v.Bytes, len(v.Body))
			}
		}
		made := count(t, s, "SELECT COUNT(*) FROM plans")
		past := testPlanBody("crew")
		c.past(&past)
		rc, err = s.ProposePlan(ctx, testOp(opID("past", i)), key, PlanRequest{Crew: "crew", Note: c.name}, past)
		if err != nil || rc.Status != StatusFailed || rc.Refusal != RefusalBound || !strings.Contains(rc.Error, c.pastWords) {
			t.Errorf("%s one past its bound: %+v %v", c.name, rc, err)
		}
		if n := count(t, s, "SELECT COUNT(*) FROM plans"); n != made {
			t.Errorf("%s one past its bound kept a revision", c.name)
		}
	}
	// A body past its bound names its largest part: twelve agents whose
	// commands are 32 arguments of 4 KiB of backslashes each, about 3 MiB.
	huge := testPlanBody("crew")
	huge.Agents = nil
	for i := range MaxPlanAgents {
		a := testAgent(fmt.Sprintf("agent-%d", i))
		a.Command = slices.Repeat([]string{strings.Repeat(`\`, 4096)}, 32)
		huge.Agents = append(huge.Agents, a)
	}
	rc, err := s.ProposePlan(ctx, testOp("huge"), key, PlanRequest{Crew: "crew"}, huge)
	want(t, rc, err, StatusFailed, RefusalBound)
	if !strings.Contains(rc.Error, "the agents' commands are 3.0 MiB of the revision's 3.0 MiB") {
		t.Fatalf("the oversized body's words: %q", rc.Error)
	}
	// The revisions of one ticket: 100 of them, whatever their state, and
	// not one more.
	_, other := newTicket(t, s, "CND", "Many plans")
	for i := range MaxPlans {
		propose(t, s, opID("many", i), other, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	}
	rc, err = s.ProposePlan(ctx, testOp("one-more"), other, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	want(t, rc, err, StatusFailed, RefusalBound)
	// An approval's note at its bound and one past it.
	id := propose(t, s, "noted", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	rc, err = s.ApprovePlan(ctx, adminOp("note-past"), id, 0, strings.Repeat("é", MaxApprovedNote+1))
	want(t, rc, err, StatusFailed, RefusalBound)
	rc, err = s.ApprovePlan(ctx, adminOp("note-at"), id, 0, strings.Repeat("é", MaxApprovedNote))
	want(t, rc, err, StatusDone, "")
}

// What a proposal refuses besides its bounds, each with nothing kept.
func TestProposeRefusals(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	_, otherKey := newTicket(t, s, "CND", "Another")
	otherPlan := propose(t, s, "other-plan", otherKey, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	otherComment := comment(t, s, "other-comment", otherKey, "elsewhere")
	gone := comment(t, s, "gone", key, "soon deleted")
	rc, err := s.DeleteComment(ctx, testOp("delete-gone"), key, gone)
	want(t, rc, err, StatusDone, "")
	cases := []struct {
		name    string
		req     PlanRequest
		body    func(b *PlanBody)
		refusal string
	}{
		{"based on another ticket's revision", PlanRequest{Crew: "crew", BasedOn: otherPlan}, nil, RefusalNotFound},
		{"based on no revision", PlanRequest{Crew: "crew", BasedOn: newID()}, nil, RefusalNotFound},
		{"a crew that is not one", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Crew = json.RawMessage(`[1]`) }, RefusalInvalid},
		{"another crew than the request's", PlanRequest{Crew: "other"}, nil, RefusalInvalid},
		{"a crew without tasks", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Crew = testCrew("crew", map[string]any{"tasks": []any{}}) }, RefusalInvalid},
		{"a base that is no full commit", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Base = "HEAD" }, RefusalInvalid},
		{"an agent named twice", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Agents = append(b.Agents, testAgent("claude")) }, RefusalInvalid},
		{"an agent with no selection", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Agents[0].SHA256 = "" }, RefusalInvalid},
		{"an agent with no command", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Agents[0].Command = nil }, RefusalInvalid},
		{"a reference leaving the directory", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.References[0].Path = "../secret" }, RefusalInvalid},
		{"an absolute reference", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.References[0].Path = "/etc/passwd" }, RefusalInvalid},
		{"a reference not cleaned", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.References[0].Path = "docs/../README.md" }, RefusalInvalid},
		{"a reference ending before it starts", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.References[0].Start, b.References[0].End = 9, 3 }, RefusalInvalid},
		{"a reference with an end and no start", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.References[0].Start = 0 }, RefusalInvalid},
		{"a reference with no object", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.References[0].Object = "" }, RefusalInvalid},
		{"a reference of another kind", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.References[0].Kind = "commit" }, RefusalInvalid},
		{"another ticket's comment", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Comments = []PlanComment{{ID: otherComment}} }, RefusalNotFound},
		{"a deleted comment", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Comments = []PlanComment{{ID: gone}} }, RefusalNotFound},
		{"an attachment that is not there", PlanRequest{Crew: "crew"}, func(b *PlanBody) { b.Attachments = []string{newID()} }, RefusalNotFound},
		{"a decision waiting on no task of the crew", PlanRequest{Crew: "crew"}, func(b *PlanBody) {
			b.Decisions = []PlanDecision{{ID: "ship", Question: "Ship?", Kind: DecisionApproval, Before: []string{"deploy"}}}
		}, RefusalInvalid},
		{"a decision of another kind", PlanRequest{Crew: "crew"}, func(b *PlanBody) {
			b.Decisions = []PlanDecision{{ID: "ship", Question: "Ship?", Kind: "vote"}}
		}, RefusalInvalid},
		{"a manifest file that is no manifest", PlanRequest{Crew: "crew"}, func(b *PlanBody) {
			b.ManifestFile = &PlanManifestFile{Path: "manifest.json", Blob: strings.Repeat("ab", 20), Manifest: json.RawMessage(`"no"`)}
		}, RefusalInvalid},
	}
	for i, c := range cases {
		body := testPlanBody("crew")
		if c.body != nil {
			c.body(&body)
		}
		rc, err := s.ProposePlan(ctx, testOp(opID("r", i)), key, c.req, body)
		if err != nil || rc.Status != StatusFailed || rc.Refusal != c.refusal {
			t.Errorf("%s: %+v %v, want %s", c.name, rc, err, c.refusal)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM plans"); n != 1 {
		t.Fatalf("%d revisions kept by refusals", n)
	}
	// A deleted ticket is not_found; an archived project refuses it.
	rc, err = s.ProposePlan(ctx, testOp("none"), "CND-99", PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	want(t, rc, err, StatusFailed, RefusalNotFound)
	rc, err = s.ArchiveProject(ctx, testOp("archive"), "CND")
	want(t, rc, err, StatusDone, "")
	rc, err = s.ProposePlan(ctx, testOp("archived"), key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	want(t, rc, err, StatusFailed, RefusalArchived)
}

// A revision's number is its place among its ticket's revisions,
// derived at read: newest first in the list, and one a replica made
// earlier takes its place without a row renumbered.
func TestRevisionNumbersArePlaces(t *testing.T) {
	s, tid, key := planTicket(t)
	ctx := context.Background()
	var ids []string
	for i := range 3 {
		ids = append(ids, propose(t, s, opID("p", i), key, PlanRequest{Crew: "crew"}, testPlanBody("crew")))
	}
	list, err := s.Plans(ctx, key)
	if err != nil || len(list) != 3 {
		t.Fatalf("the list: %+v %v", list, err)
	}
	for i, p := range list {
		if p.ID != ids[2-i] || p.N != 3-i {
			t.Fatalf("the list's %d: %s n %d, want %s n %d", i, p.ID, p.N, ids[2-i], 3-i)
		}
	}
	// A revision a replica made before the first, its row as replication
	// would write it.
	early := newID()
	first := planOf(t, s, ids[0])
	if _, err := s.db.Exec(`INSERT INTO plans (id, ticket_id, crew_id, crew_name, base, body, digest, bytes, created_at, created_by, rev)
		VALUES (?, ?, 'crew', 'Crew crew', ?, '{}', ?, 2, ?, 'elsewhere', 1)`, early, tid, testBase, sha256Hex("{}"), stamp(first.CreatedAt.Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	list, err = s.Plans(ctx, key)
	if err != nil || len(list) != 4 || list[3].ID != early || list[3].N != 1 || list[2].ID != ids[0] || list[2].N != 2 || list[0].N != 4 {
		t.Fatalf("the list with a replica's revision: %+v %v", list, err)
	}
	if v := planOf(t, s, ids[2]); v.N != 4 {
		t.Fatalf("the third revision's read is n %d", v.N)
	}
	if got := texts(t, s, "SELECT name FROM pragma_table_info('plans') WHERE name = 'n'"); len(got) != 0 {
		t.Fatal("a revision's number is stored")
	}
}

// An approval is the operator's, against the ticket's intent; it
// supersedes the ticket's other approved revision in its transaction,
// one row each, and never bumps the ticket's rev.
func TestApproveSupersedes(t *testing.T) {
	s, tid, key := planTicket(t)
	ctx := context.Background()
	r1 := propose(t, s, "p1", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	r2 := propose(t, s, "p2", key, PlanRequest{Crew: "crew", Note: "second"}, testPlanBody("crew"))
	tk := ticketOf(t, s, key)
	log := listen(s)
	rc, err := s.ApprovePlan(ctx, Op{Actor: ActorAdmin, ID: "a1", By: "Operator", ExpectedRev: 1}, r1, tk.Rev, "Looks right.")
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 2 {
		t.Fatalf("the receipt's rev: %+v", rc)
	}
	v := planOf(t, s, r1)
	if v.State != PlanApproved || v.ApprovedBy != "Operator" || v.ApprovedNote != "Looks right." || v.ApprovedRev != tk.Rev ||
		v.ApprovedIntent != IntentDigest(tk) || v.ApprovedAt.IsZero() || v.Stale != nil {
		t.Fatalf("approved: %+v", v.PlanSummary)
	}
	var fields []string
	for _, r := range changesOf(t, s, ActorAdmin, "a1") {
		if r.Entity != EntityPlan || r.EntityID != r1 || r.TicketID != tid || r.Op != OpSet {
			t.Fatalf("an approval's row: %+v", r)
		}
		fields = append(fields, r.Field)
	}
	if strings.Join(fields, ",") != "approved_at,approved_by,approved_note,approved_rev,approved_intent,state" {
		t.Fatalf("the approval's rows: %v", fields)
	}
	if got := log.one(t); len(got) != 1 || got[0].Entity != EntityPlan || got[0].ID != r1 || got[0].Ticket != tid {
		t.Fatalf("OnChange: %+v", got)
	}
	if ids, _ := approvedOf(t, s, key); !slices.Equal(ids, []string{r1}) {
		t.Fatalf("the ticket's approved revisions: %v", ids)
	}
	// The second's approval supersedes the first, in its transaction.
	rc, err = s.ApprovePlan(ctx, adminOp("a2"), r2, 0, "")
	want(t, rc, err, StatusDone, "")
	if v := planOf(t, s, r1); v.State != PlanSuperseded || v.Rev != 3 || v.ApprovedNote != "Looks right." {
		t.Fatalf("the first after the second's approval: %+v", v.PlanSummary)
	}
	rows := changesOf(t, s, ActorAdmin, "a2")
	last := rows[len(rows)-1]
	if last.EntityID != r1 || last.Field != "state" || last.Value != PlanSuperseded || last.Prior != PlanApproved {
		t.Fatalf("the supersession's row: %+v", last)
	}
	if ids, _ := approvedOf(t, s, key); !slices.Equal(ids, []string{r2}) {
		t.Fatalf("the ticket's approved revisions: %v", ids)
	}
	if after := ticketOf(t, s, key).Rev; after != tk.Rev {
		t.Fatalf("the ticket's rev moved from %d to %d", tk.Rev, after)
	}
	// Stale revisions: the revision's own expected, and the ticket's.
	rc, err = s.ApprovePlan(ctx, Op{Actor: ActorAdmin, ID: "a3", ExpectedRev: 1}, r1, 0, "")
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	rc, err = s.ApprovePlan(ctx, adminOp("a4"), r1, tk.Rev+1, "")
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	rc, err = s.ApprovePlan(ctx, adminOp("a5"), newID(), 0, "")
	want(t, rc, err, StatusFailed, RefusalNotFound)
}

// An approval is the operator's alone: an agent's actor, or any other,
// is refused invalid, with nothing written.
func TestApprovalIsTheOperators(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	id := propose(t, s, "p1", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	for _, actor := range []string{"agent:dev", "test", "merge"} {
		rc, err := s.ApprovePlan(ctx, Op{Actor: actor, ID: "a1"}, id, 0, "")
		want(t, rc, err, StatusFailed, RefusalInvalid)
		if !strings.Contains(rc.Error, "operator") {
			t.Fatalf("%s: %q", actor, rc.Error)
		}
	}
	if v := planOf(t, s, id); v.State != PlanProposed || v.Rev != 1 {
		t.Fatalf("the revision after the refusals: %+v", v.PlanSummary)
	}
	approve(t, s, "a1", id, "")
}

// An approval holds while the ticket's intent (its title, description
// and acceptance criteria) is the one it recorded: each makes it stale,
// with its words and the remedy approve; status, priority, labels, the
// owner, a comment added and a comment not handed over edited do not.
// The words name a field only when it differs from its value at the
// approval: one edited and edited back is not named beside one that
// changed.
func TestApprovalGoesStaleWithTheIntent(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	handed := comment(t, s, "c1", key, "handed over")
	other := comment(t, s, "c2", key, "not handed over")
	label := newLabel(t, s, "CND", "bug")
	body := testPlanBody("crew")
	body.Comments = []PlanComment{{ID: handed}}
	id := propose(t, s, "p1", key, PlanRequest{Crew: "crew", Comments: []string{handed}}, body)
	approve(t, s, "a1", id, "")
	holding := func(what string) {
		t.Helper()
		if v := planOf(t, s, id); v.Stale != nil {
			t.Fatalf("stale after %s: %+v", what, v.Stale)
		}
		if _, stale := approvedOf(t, s, key); len(stale) != 1 || stale[0] != nil {
			t.Fatalf("the ticket's read after %s: %+v", what, stale)
		}
	}
	mustEdit(t, s, "status", key, TicketEdit{Set: TicketSet{Status: ptr(TicketInProgress)}})
	mustEdit(t, s, "priority", key, TicketEdit{Set: TicketSet{Priority: ptr(3)}})
	mustEdit(t, s, "label", key, TicketEdit{Add: TicketMembers{Labels: []string{label}}})
	mustEdit(t, s, "owner", key, TicketEdit{Set: TicketSet{Owner: ptr("Alice")}})
	comment(t, s, "c3", key, "a comment added")
	rc, err := s.EditComment(ctx, testOp("e2"), key, other, "edited, not handed over")
	want(t, rc, err, StatusDone, "")
	holding("the edits that are no intent")
	// A title edited and edited back is the intent it was.
	mustEdit(t, s, "t1", key, TicketEdit{Set: TicketSet{Title: ptr("Another title")}})
	mustEdit(t, s, "t2", key, TicketEdit{Set: TicketSet{Title: ptr("Fix the login loop")}})
	holding("a title edited back")
	for i, c := range []struct {
		edit  TicketSet
		words string
	}{
		{TicketSet{Title: ptr("Fix the login loop, now")}, "the title changed since revision 1 was approved"},
		{TicketSet{Description: ptr("It loops twice.")}, "the description changed since revision 1 was approved"},
		{TicketSet{Acceptance: ptr("- no loop\n- a test")}, "the acceptance criteria changed since revision 1 was approved"},
		{TicketSet{Title: ptr("Both"), Description: ptr("Both changed.")}, "the title and the description changed since revision 1 was approved"},
	} {
		mustEdit(t, s, opID("intent", i), key, TicketEdit{Set: c.edit})
		v := planOf(t, s, id)
		if v.Stale == nil || v.Stale.Words != c.words || v.Stale.Remedy != RemedyApprove || v.State != PlanApproved {
			t.Fatalf("after %+v: %+v", c.edit, v.Stale)
		}
		list, err := s.Plans(ctx, key)
		if err != nil || list[0].Stale == nil || list[0].Stale.Words != c.words {
			t.Fatalf("the list after %+v: %+v %v", c.edit, list, err)
		}
		if _, stale := approvedOf(t, s, key); len(stale) != 1 || stale[0] == nil || stale[0].Remedy != RemedyApprove {
			t.Fatalf("the ticket's read after %+v: %+v", c.edit, stale)
		}
		// Approving again cures it.
		approve(t, s, opID("again", i), id, "")
		holding("approving again")
	}
	// A field edited and edited back beside one that changed: the words
	// name the one that changed alone.
	mustEdit(t, s, "d1", key, TicketEdit{Set: TicketSet{Description: ptr("Other.")}})
	mustEdit(t, s, "d2", key, TicketEdit{Set: TicketSet{Description: ptr("Both changed.")}})
	holding("the description edited back")
	mustEdit(t, s, "t3", key, TicketEdit{Set: TicketSet{Title: ptr("Only the title")}})
	if v := planOf(t, s, id); v.Stale == nil || v.Stale.Words != "the title changed since revision 1 was approved" || v.Stale.Remedy != RemedyApprove {
		t.Fatalf("a description edited back beside the title changed: %+v", v.Stale)
	}
}

// An approval covers the words handed over: a handed-over comment edited
// or deleted after it makes it stale for good, its remedy revise; an
// approval again is refused stale_plan with nothing changed; a revision
// made from it by Revise, with the comment's body now, is approved.
func TestApprovalGoesStaleWithAHandedOverComment(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	c := comment(t, s, "c1", key, "Use the new store.")
	body := testPlanBody("crew")
	body.Comments = []PlanComment{{ID: c}}
	req := PlanRequest{Crew: "crew", Comments: []string{c}}
	r1 := propose(t, s, "p1", key, req, body)
	approve(t, s, "a1", r1, "")
	rc, err := s.EditComment(ctx, testOp("e1"), key, c, "Use the old store.")
	want(t, rc, err, StatusDone, "")
	v := planOf(t, s, r1)
	if v.Stale == nil || v.Stale.Remedy != RemedyRevise || v.Stale.Words != "comment 1 was edited since revision 1 was made" {
		t.Fatalf("stale: %+v", v.Stale)
	}
	if _, stale := approvedOf(t, s, key); len(stale) != 1 || stale[0] == nil || stale[0].Remedy != RemedyRevise {
		t.Fatalf("the ticket's read: %+v", stale)
	}
	changes := count(t, s, "SELECT COUNT(*) FROM changes")
	rc, err = s.ApprovePlan(ctx, adminOp("a2"), r1, 0, "")
	want(t, rc, err, StatusFailed, RefusalStalePlan)
	if rc.Error != "comment 1 was edited since revision 1 was made: revise it" {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes"); n != changes || planOf(t, s, r1).Rev != v.Rev {
		t.Fatal("a refused approval changed something")
	}
	// An intent edit too: still revise, both said.
	mustEdit(t, s, "t1", key, TicketEdit{Set: TicketSet{Description: ptr("Loops.")}})
	if v := planOf(t, s, r1); v.Stale == nil || v.Stale.Remedy != RemedyRevise ||
		v.Stale.Words != "comment 1 was edited since revision 1 was made; the description changed since revision 1 was approved" {
		t.Fatalf("stale with both: %+v", v.Stale)
	}
	// Revise: the next revision, based on the first, with the body now.
	req.BasedOn = r1
	r2 := propose(t, s, "p2", key, req, body)
	v2 := planOf(t, s, r2)
	if v2.BasedOn != r1 || v2.N != 2 || !strings.Contains(string(v2.Body), sha256Hex("Use the old store.")) {
		t.Fatalf("the revision made by Revise: %+v %s", v2.PlanSummary, v2.Body)
	}
	approve(t, s, "a3", r2, "With the new words.")
	if v := planOf(t, s, r2); v.Stale != nil || v.State != PlanApproved {
		t.Fatalf("the revised one: %+v", v.PlanSummary)
	}
	if v := planOf(t, s, r1); v.State != PlanSuperseded || v.Stale != nil {
		t.Fatalf("the first after the revised one's approval: %+v", v.PlanSummary)
	}
	// A handed-over comment deleted is stale the same way.
	rc, err = s.DeleteComment(ctx, testOp("d1"), key, c)
	want(t, rc, err, StatusDone, "")
	if v := planOf(t, s, r2); v.Stale == nil || v.Stale.Remedy != RemedyRevise || v.Stale.Words != "comment 1 was deleted since revision 2 was made" {
		t.Fatalf("stale after a delete: %+v", v.Stale)
	}
	rc, err = s.ApprovePlan(ctx, adminOp("a4"), r2, 0, "")
	want(t, rc, err, StatusFailed, RefusalStalePlan)
}

// An approval made again: refused while it holds, taken once it is stale
// (its intent's change) or superseded, the new one recorded beside the
// old in the history.
func TestApproveAgain(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	r1 := propose(t, s, "p1", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	approve(t, s, "a1", r1, "first")
	rc, err := s.ApprovePlan(ctx, adminOp("a2"), r1, 0, "again")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if rc.Error != "revision 1 is approved already" {
		t.Fatalf("the words: %q", rc.Error)
	}
	mustEdit(t, s, "t1", key, TicketEdit{Set: TicketSet{Title: ptr("A new title")}})
	tk := ticketOf(t, s, key)
	approve(t, s, "a3", r1, "again")
	v := planOf(t, s, r1)
	if v.Stale != nil || v.ApprovedNote != "again" || v.ApprovedRev != tk.Rev || v.ApprovedIntent != IntentDigest(tk) {
		t.Fatalf("approved again: %+v", v.PlanSummary)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE entity = ? AND entity_id = ? AND field = 'approved_at'", EntityPlan, r1); n != 2 {
		t.Fatalf("%d approvals in the history", n)
	}
	if got := texts(t, s, "SELECT prior FROM changes WHERE entity = ? AND entity_id = ? AND field = 'approved_note' ORDER BY seq", EntityPlan, r1); !slices.Equal(got, []string{"", "first"}) {
		t.Fatalf("the notes' history: %v", got)
	}
	// Superseded, then approved again: the other superseded in turn.
	r2 := propose(t, s, "p2", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	approve(t, s, "a4", r2, "")
	approve(t, s, "a5", r1, "back to the first")
	if planOf(t, s, r1).State != PlanApproved || planOf(t, s, r2).State != PlanSuperseded {
		t.Fatal("the first approved again did not supersede the second")
	}
	if ids, _ := approvedOf(t, s, key); !slices.Equal(ids, []string{r1}) {
		t.Fatalf("the ticket's approved revisions: %v", ids)
	}
}

// Any revision not withdrawn may be withdrawn: a proposed one, an
// approved one (its ticket left with none approved); a withdrawn one is
// approved or withdrawn no more.
func TestWithdraw(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	r1 := propose(t, s, "p1", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	r2 := propose(t, s, "p2", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	rc, err := s.WithdrawPlan(ctx, testOp("w1"), r1)
	want(t, rc, err, StatusDone, "")
	v := planOf(t, s, r1)
	if v.State != PlanWithdrawn || v.WithdrawnBy != "Tester" || v.WithdrawnAt.IsZero() || v.Rev != 2 {
		t.Fatalf("withdrawn: %+v", v.PlanSummary)
	}
	var fields []string
	for _, r := range changesOf(t, s, "test", "w1") {
		fields = append(fields, r.Field)
	}
	if strings.Join(fields, ",") != "withdrawn_at,withdrawn_by,state" {
		t.Fatalf("the withdrawal's rows: %v", fields)
	}
	rc, err = s.WithdrawPlan(ctx, testOp("w2"), r1)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.ApprovePlan(ctx, adminOp("a1"), r1, 0, "")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	approve(t, s, "a2", r2, "")
	rc, err = s.WithdrawPlan(ctx, Op{Actor: "test", ID: "w3", ExpectedRev: 1}, r2)
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	rc, err = s.WithdrawPlan(ctx, testOp("w4"), r2)
	want(t, rc, err, StatusDone, "")
	if ids, _ := approvedOf(t, s, key); len(ids) != 0 {
		t.Fatalf("approved after the withdrawal: %v", ids)
	}
	list, err := s.Plans(ctx, key)
	if err != nil || len(list) != 2 || list[0].State != PlanWithdrawn || list[1].State != PlanWithdrawn {
		t.Fatalf("the list: %+v %v", list, err)
	}
	// An archived project refuses it; a deleted ticket's revision is
	// not_found.
	r3 := propose(t, s, "p3", key, PlanRequest{Crew: "crew"}, testPlanBody("crew"))
	rc, err = s.ArchiveProject(ctx, testOp("archive"), "CND")
	want(t, rc, err, StatusDone, "")
	rc, err = s.WithdrawPlan(ctx, testOp("w5"), r3)
	want(t, rc, err, StatusFailed, RefusalArchived)
	rc, err = s.UnarchiveProject(ctx, testOp("unarchive"), "CND")
	want(t, rc, err, StatusDone, "")
	rc, err = s.DeleteTicket(ctx, testOp("delete"), key)
	want(t, rc, err, StatusDone, "")
	rc, err = s.WithdrawPlan(ctx, testOp("w6"), r3)
	want(t, rc, err, StatusFailed, RefusalNotFound)
	rc, err = s.ApprovePlan(ctx, adminOp("a3"), r3, 0, "")
	want(t, rc, err, StatusFailed, RefusalNotFound)
}

// The intent digest is the SHA-256 of the title, the description and the
// acceptance criteria as a JSON array, HTML escaping off, and of nothing
// else.
func TestIntentDigest(t *testing.T) {
	tk := Ticket{Title: "a <b>", Description: "c & d", Acceptance: "e"}
	if got, want := IntentDigest(tk), sha256Hex(`["a <b>","c & d","e"]`); got != want {
		t.Fatalf("IntentDigest: %s, want %s", got, want)
	}
	other := tk
	other.Status, other.Priority, other.Owner, other.Labels = TicketDone, 4, "Alice", []string{"x"}
	if IntentDigest(other) != IntentDigest(tk) {
		t.Fatal("the digest moved with a field that is no intent")
	}
	for _, f := range []func(*Ticket){func(t *Ticket) { t.Title += "!" }, func(t *Ticket) { t.Description += "!" }, func(t *Ticket) { t.Acceptance += "!" }} {
		changed := tk
		f(&changed)
		if IntentDigest(changed) == IntentDigest(tk) {
			t.Fatalf("the digest did not move with %+v", changed)
		}
	}
}
