package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// A ticket as a run launched from it reads it (AtLaunch): the statement
// as launched; the links and attachments it had at the launch and still
// has, by what their adds recorded; the comments that were there at the
// launch and have not changed since; every person's comment as it is now;
// nothing else, each list chosen by that rule before its limit cuts it.

// atLaunchLimits are the limits the API reads a lane's ticket with.
var atLaunchLimits = LaunchLimits{Comments: MaxTicketComments, Links: MaxLinks, Attachments: MaxAttachments}

// commentAs comments on the ticket as the actor and returns the comment's
// id.
func commentAs(t *testing.T, s *Store, actor, key, body string) string {
	t.Helper()
	rc, err := s.AddComment(context.Background(), Op{Actor: actor, ID: "c-" + newID()}, key, body)
	want(t, rc, err, StatusDone, "")
	return rc.ID
}

// editAs edits the comment as the actor.
func editAs(t *testing.T, s *Store, actor, key, id, body string) {
	t.Helper()
	rc, err := s.EditComment(context.Background(), Op{Actor: actor, ID: "e-" + newID()}, key, id, body)
	want(t, rc, err, StatusDone, "")
}

// attach adds an attachment of the name to the ticket and returns its id.
func attach(t *testing.T, s *Store, key, name string) string {
	t.Helper()
	rc, a, err := s.AddAttachment(context.Background(), testOp("a-"+newID()), key, name, "text/plain", strings.NewReader(name))
	want(t, rc, err, StatusDone, "")
	return a.ID
}

// linkByID is the ticket's live link with the id, as the ticket's links
// read it.
func linkByID(t *testing.T, s *Store, key, id string) Link {
	t.Helper()
	links, err := s.Links(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("no link %s", id)
	return Link{}
}

// atLaunch reads the launch's ticket with the API's limits.
func atLaunch(t *testing.T, s *Store, launch string) AtLaunch {
	t.Helper()
	v, err := s.AtLaunch(context.Background(), launch, atLaunchLimits)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// bodies is each comment's body, in order.
func bodies(cs []LaunchComment) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Body)
	}
	return out
}

// The launch's read holds the statement as launched, the earlier things
// unchanged since, the people's comments as they are now, and nothing
// else: not an agent's earlier comment edited since or deleted since, not
// a link removed since, not an attachment deleted since, nothing an
// agent, a link or an attachment added since, no field a link was given
// after its add, no read of a linked item; sixty agents' comments since
// push none of it out.
func TestAtLaunchReadsByTheLaunchsMark(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	keptAgent := commentAs(t, s, "agent:cand-a", key, "an agent's, unchanged")
	editedAgent := commentAs(t, s, "agent:cand-a", key, "an agent's, as written")
	deletedAgent := commentAs(t, s, "agent:cand-a", key, "an agent's, deleted since")
	person := commentAs(t, s, ActorAdmin, key, "a person's, as written")
	rc, err := addLink(s, "l-pr", key, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9", Note: "the first try"})
	want(t, rc, err, StatusDone, "")
	pr := rc.ID
	added := linkByID(t, s, key, pr)
	if err := s.RecordRead(ctx, pr, prRead("PR_9", 1, "open")); err != nil {
		t.Fatal(err)
	}
	rc, err = addLink(s, "l-gone", key, NewLink{Kind: LinkExternal, URL: "https://example.test/removed"})
	want(t, rc, err, StatusDone, "")
	removed := rc.ID
	keptFile := attach(t, s, key, "kept.txt")
	goneFile := attach(t, s, key, "gone.txt")
	plan := simplePlan(t, s, key)
	l := mustLaunch(t, s, "l1", key, plan, "")

	editAs(t, s, "agent:cand-a", key, editedAgent, "an agent's, edited since")
	if rc, err := s.DeleteComment(ctx, Op{Actor: "agent:cand-a", ID: "d1"}, key, deletedAgent); err != nil || rc.Status != StatusDone {
		t.Fatalf("delete: %+v %v", rc, err)
	}
	editAs(t, s, ActorAdmin, key, person, "a person's, edited since")
	if err := s.RecordRead(ctx, pr, prRead("PR_9", 2, "merged")); err != nil {
		t.Fatal(err)
	}
	// What a merge could set on the link's row after its add: a candidate
	// and its verification.
	if _, err := s.db.Exec("UPDATE links SET candidate = ?, verified = ?, note = ? WHERE id = ?", "crew-0000000a/approach-a/1", `{"run":"crew-0000000a"}`, "rewritten", pr); err != nil {
		t.Fatal(err)
	}
	if rc, err := s.RemoveLink(ctx, testOp("rl"), key, removed); err != nil || rc.Status != StatusDone {
		t.Fatalf("remove: %+v %v", rc, err)
	}
	if rc, err := s.RemoveAttachment(ctx, testOp("ra"), key, goneFile); err != nil || rc.Status != StatusDone {
		t.Fatalf("remove: %+v %v", rc, err)
	}
	for i := range 60 {
		commentAs(t, s, "agent:lead", key, fmt.Sprintf("an agent's, since %d", i))
	}
	for i, actor := range []string{ActorAdmin, "user:alice", ActorAdmin} {
		commentAs(t, s, actor, key, fmt.Sprintf("a person's, since %d", i))
	}
	rc, err = addLink(s, "l-since", key, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/12"})
	want(t, rc, err, StatusDone, "")
	attach(t, s, key, "since.txt")
	mustEdit(t, s, "e-title", key, TicketEdit{Set: TicketSet{Title: ptr("A title since"), Description: ptr("A description since")}})

	v := atLaunch(t, s, l.ID)
	tk := ticketOf(t, s, key)
	if v.ID != tk.ID || v.Key != key || v.Number != tk.Number || v.Title != "Fix the login loop" || v.Description != "It loops." || v.Acceptance != "- no loop" {
		t.Fatalf("the statement: %+v", v)
	}
	if got := bodies(v.Comments); !slices.Equal(got, []string{"an agent's, unchanged", "a person's, edited since", "a person's, since 0", "a person's, since 1", "a person's, since 2"}) {
		t.Fatalf("the comments: %q", got)
	}
	if c := v.Comments[0]; c.ID != keptAgent || c.Author != "agent:cand-a" || !c.EditedAt.IsZero() {
		t.Fatalf("the earlier comment: %+v", c)
	}
	if c := v.Comments[1]; c.ID != person || c.EditedAt.IsZero() {
		t.Fatalf("the person's comment: %+v", c)
	}
	if len(v.Links) != 1 || v.Links[0] != (LaunchLink{ID: pr, Kind: LinkDelivery, Ref: added.Ref, URL: added.URL, Note: "the first try", CreatedAt: added.CreatedAt, CreatedBy: "Tester"}) {
		t.Fatalf("the links: %+v, added %+v", v.Links, added)
	}
	if len(v.Attachments) != 1 || v.Attachments[0].ID != keptFile || v.Attachments[0].Name != "kept.txt" || !v.Attachments[0].DeletedAt.IsZero() {
		t.Fatalf("the attachments: %+v", v.Attachments)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if keys := slices.Sorted(maps.Keys(top)); !slices.Equal(keys, []string{"acceptance", "attachments", "comments", "description", "id", "key", "links", "number", "title"}) {
		t.Fatalf("the answer's fields: %v", keys)
	}
	for _, gone := range []string{"an agent's, edited since", "an agent's, as written", "a person's, as written", "deleted since", "an agent's, since", "pull/12",
		"since.txt", "gone.txt", "example.test/removed", "rewritten", "candidate", "verified", "PR_9", "merged", "headSha", "A title since"} {
		if strings.Contains(string(raw), gone) {
			t.Fatalf("the answer holds %q: %s", gone, raw)
		}
	}

	// Each list is chosen by the rule, then cut: two comments are the two
	// newest people's, not two of the sixty since.
	small, err := s.AtLaunch(ctx, l.ID, LaunchLimits{Comments: 2, Links: 1, Attachments: 1})
	if err != nil || !slices.Equal(bodies(small.Comments), []string{"a person's, since 1", "a person's, since 2"}) || len(small.Links) != 1 || len(small.Attachments) != 1 {
		t.Fatalf("cut to two: %+v %v", small, err)
	}
}

// Two launches of one ticket each read by their own mark: a comment, a
// link and an attachment made between them are the second's and not the
// first's.
func TestAtLaunchOfEachLaunchIsItsOwn(t *testing.T) {
	s, _, key := planTicket(t)
	plan := simplePlan(t, s, key)
	first := mustLaunch(t, s, "l1", key, plan, "")
	commentAs(t, s, "agent:lead", key, "between the launches")
	rc, err := addLink(s, "l-between", key, NewLink{Kind: LinkExternal, URL: "https://example.test/between"})
	want(t, rc, err, StatusDone, "")
	attach(t, s, key, "between.txt")
	second := mustLaunch(t, s, "l2", key, plan, "")
	commentAs(t, s, "agent:lead", key, "after both")
	a, b := atLaunch(t, s, first.ID), atLaunch(t, s, second.ID)
	if len(a.Comments) != 0 || len(a.Links) != 0 || len(a.Attachments) != 0 {
		t.Fatalf("the first launch's read: %+v", a)
	}
	if !slices.Equal(bodies(b.Comments), []string{"between the launches"}) || len(b.Links) != 1 || b.Links[0].ID != rc.ID || len(b.Attachments) != 1 {
		t.Fatalf("the second launch's read: %+v", b)
	}
}

// No such launch is ErrNotFound; a deleted ticket's launch is
// ErrLaunchTicketGone; a limit below 1 is refused before any read.
func TestAtLaunchRefusals(t *testing.T) {
	s, _, key := planTicket(t)
	ctx := context.Background()
	if _, err := s.AtLaunch(ctx, "0123456789abcdef", atLaunchLimits); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such launch: %v", err)
	}
	l := mustLaunch(t, s, "l1", key, simplePlan(t, s, key), "")
	if _, err := s.AtLaunch(ctx, l.ID, LaunchLimits{Comments: 1, Links: 0, Attachments: 1}); err == nil {
		t.Fatal("a limit of 0 was taken")
	}
	rc, err := s.DeleteTicket(ctx, testOp("del"), key)
	want(t, rc, err, StatusDone, "")
	if _, err := s.AtLaunch(ctx, l.ID, atLaunchLimits); !errors.Is(err, ErrLaunchTicketGone) {
		t.Fatalf("a deleted ticket: %v", err)
	}
}
