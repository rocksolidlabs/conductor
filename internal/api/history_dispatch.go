package api

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// The history's sources over Dispatch and Brakeman (docs/plans/history.md,
// "The sources", PR-4): a ticket, read through Dispatch's own reads, and a
// Brakeman brief, read through Brakeman's store. Each is a function from
// what its store holds to the index's entries and links; what neither
// store shows its reader is left out here as it is there:
//
//   - a deleted comment keeps its place and its number, never its words
//     (Dispatch's search drops a comment's body once it is deleted,
//     internal/dispatch/query.go);
//   - a ticket's history is read for the acts it records (a plan
//     revision's approval, withdrawal and supersession, an acceptance and
//     its withdrawal, a close by a person) and for nothing else: no
//     field's earlier value, and nothing of a review or a finding (RV1's
//     rows, Program item RV1, which PR-10 indexes by its own rules);
//   - a plan revision is its record (its crew, its base, its number), never
//     its body;
//   - a brief's request is never read for what the person asked of
//     Brakeman (its note): only the brief's own items, and of those only a
//     fact, an interpretation, a proposal or an outcome, each Brakeman's
//     words (said); a brief whose turn could see the terminals' screens
//     keeps its items' words out of the index (their kind and what they
//     cite stay), so no terminal's output reaches a search through it.

// The relations PR-4 adds.
const (
	relCites    = "cites"
	relDelivers = "delivers"
)

// The refs of Dispatch's records, as Brakeman's grammar spells them.
func hTicketRef(key string) string             { return "ticket:" + key }
func hCommentRef(key, id string) string        { return "comment:" + key + "/" + id }
func hLaunchRef(id string) string              { return "launch:" + id }
func hBriefRef(id string) string               { return "brief:" + id }
func hBriefItemRef(id string, item int) string { return "brief:" + id + "/" + strconv.Itoa(item) }

// ticketRead is what a ticket's pass read of Dispatch, each through the
// store's own read: the ticket as its page shows it, every comment, its
// plan revisions, its launches and its changes. Its project is its key's;
// its repository is left to the project (a project's folder can move with
// no change of the ticket's, and Similar scopes by the project first).
type ticketRead struct {
	v        dispatch.TicketView
	comments []dispatch.Comment
	plans    []dispatch.PlanSummary
	launches []dispatch.LaunchSummary
	changes  []dispatch.ChangeRow
}

// isPerson says an actor is a person's: the operator (the workbench token)
// or an identity provider's principal, never the server, a merge or an
// agent.
func isPerson(actor string) bool {
	return actor == dispatch.ActorAdmin || strings.HasPrefix(actor, "user:")
}

// ticketEntries is a ticket's entries: its record, its comments, its plan
// revisions with the acts on them, its launches, its acceptance and its
// close by a person, and its deliveries whose verification receipt is
// kept; and their links.
func ticketEntries(r ticketRead) ([]history.Entry, []history.Link) {
	tk := r.v.Ticket
	key := tk.Key
	ref := hTicketRef(key)
	project, _, _ := dispatch.ParseKey(key)
	base := history.Entry{Anchor: ref, Project: hField(project), Ticket: hField(key)}
	var out []history.Entry
	var links []history.Link

	e := base
	e.Key, e.Ref, e.Kind, e.At = ref, ref, "ticket", hAt(tk.CreatedAt, time.Unix(0, 0))
	e.Revision, e.Actor, e.Outcome = hField(fmt.Sprintf("rev %d", tk.Rev)), hField(tk.Owner), hField(tk.Status)
	if !tk.DeletedAt.IsZero() {
		e.Outcome = "deleted"
	}
	e.Title = hTitle(key + " " + tk.Title)
	e.Summary = hSummary(strings.Join(hNonEmpty(tk.Status, tk.Owner, project), " · "))
	e.Body = history.Clip(strings.Join(hNonEmpty(tk.Description, tk.Acceptance), "\n\n"), history.MaxLongBody)
	out = append(out, e)

	for _, c := range r.comments {
		x := base
		x.Key = hCommentRef(key, c.ID)
		x.Ref, x.Kind, x.At, x.Seq, x.Actor = x.Key, "comment", hAt(c.CreatedAt, e.At), int64(c.N), hField(cmp.Or(c.By, c.Author))
		if !c.DeletedAt.IsZero() {
			// A tombstone: its place and its number, never its words.
			x.Outcome, x.Title = "deleted", hTitle(fmt.Sprintf("comment #%d on %s, deleted", c.N, key))
		} else {
			x.Title = hTitle(c.Body)
			x.Summary = hSummary(fmt.Sprintf("comment #%d on %s by %s", c.N, key, x.Actor))
			x.Body = history.Clip(c.Body, history.MaxLongBody)
		}
		if x.Title == "" {
			x.Title = fmt.Sprintf("comment #%d on %s", c.N, key)
		}
		out = append(out, x)
	}

	planN := map[string]int{}
	for _, p := range r.plans {
		planN[p.ID] = p.N
		x := base
		x.Key = hPlanRef(key, p.N)
		x.Ref, x.Kind, x.At, x.Actor = x.Key, "plan", hAt(p.CreatedAt, e.At), hField(p.CreatedBy)
		x.Revision, x.Crew, x.Outcome = fmt.Sprintf("r%d", p.N), hField(p.CrewID), hField(p.State)
		if objectID(p.Base) {
			x.Commit = p.Base
		}
		x.Title = hTitle(fmt.Sprintf("r%d of %s: %s", p.N, key, cmp.Or(p.CrewName, p.CrewID)))
		x.Summary = hSummary(strings.Join(hNonEmpty(p.CrewID, "from "+p.Base, p.State), " · "))
		out = append(out, x)
	}

	// The acts the ticket's history records, each keyed by the op id of
	// the change that recorded it: an act made again is a decision of its
	// own. A note is the one in effect at the act: the same operation's
	// note row, else the last one before it (Dispatch writes a field only
	// when it changes, so an act made again with the same note writes no
	// note row; a note cleared is a row of its own, its value "").
	changes := slices.Clone(r.changes)
	slices.SortStableFunc(changes, func(a, b dispatch.ChangeRow) int { return cmp.Compare(a.Seq, b.Seq) })
	isNote := func(c dispatch.ChangeRow) bool { return c.Field == "approved_note" || c.Field == "accepted_note" }
	opNotes := map[string]string{}
	for _, c := range changes {
		if isNote(c) {
			opNotes[c.Entity+"\x00"+c.EntityID+"\x00"+c.Field+"\x00"+c.OpID] = c.Value
		}
	}
	current := map[string]string{} // the note in effect, by entity and field
	noteAt := func(c dispatch.ChangeRow, field string) string {
		if v, ok := opNotes[c.Entity+"\x00"+c.EntityID+"\x00"+field+"\x00"+c.OpID]; ok {
			return v
		}
		return current[c.Entity+"\x00"+c.EntityID+"\x00"+field]
	}
	seen := map[string]bool{}
	for _, c := range changes {
		if isNote(c) {
			current[c.Entity+"\x00"+c.EntityID+"\x00"+c.Field] = c.Value
			continue
		}
		x := base
		x.At, x.Actor = hAt(c.At, e.At), hField(c.Actor)
		by := cmp.Or(c.By, c.Actor)
		switch act := planAct(c); {
		case act != "":
			// An approval made again sets approved_at again (its state
			// stays approved), so each act is read by its own field.
			n, ok := planN[c.EntityID]
			if !ok {
				continue
			}
			x.Kind = "plan." + act
			x.Key = fmt.Sprintf("%s#%s/r%d/%s", ref, x.Kind, n, c.OpID)
			x.Ref, x.Revision, x.Outcome = hPlanRef(key, n), fmt.Sprintf("r%d", n), act
			x.Title = hTitle(fmt.Sprintf("r%d of %s %s by %s", n, key, act, by))
			if act == dispatch.PlanApproved {
				x.Body = hShortBody(noteAt(c, "approved_note"))
			}
		case c.Entity == dispatch.EntityTicket && c.Field == "accepted_at":
			x.Kind = "ticket.accepted"
			if c.Value == "" {
				x.Kind = "ticket.unaccepted"
			}
			x.Key = fmt.Sprintf("%s#%s/%s", ref, x.Kind, c.OpID)
			x.Ref, x.Outcome = ref, strings.TrimPrefix(x.Kind, "ticket.")
			x.Title = hTitle(fmt.Sprintf("%s %s by %s", key, x.Outcome, by))
			if x.Kind == "ticket.accepted" {
				x.Body = hShortBody(noteAt(c, "accepted_note"))
			}
		case c.Entity == dispatch.EntityTicket && c.Field == "status" && isPerson(c.Actor) &&
			(c.Value == dispatch.TicketDone || c.Value == dispatch.TicketCancelled):
			x.Kind = "ticket.closed"
			x.Key = fmt.Sprintf("%s#%s/%s", ref, x.Kind, c.OpID)
			x.Ref, x.Outcome = ref, c.Value
			x.Title = hTitle(fmt.Sprintf("%s closed as %s by %s", key, c.Value, by))
		default:
			continue
		}
		if x.Actor == "" || seen[x.Key] {
			continue
		}
		seen[x.Key] = true
		x.Summary = hSummary(strings.Join(hNonEmpty(x.Outcome, by), " · "))
		out = append(out, x)
	}

	for _, l := range r.launches {
		x := base
		x.Key = hLaunchRef(l.ID)
		x.Ref, x.Kind, x.At, x.Actor, x.Run = x.Key, "launch", hAt(l.CreatedAt, e.At), hField(l.CreatedBy), hField(l.RunID)
		x.Revision, x.Outcome = fmt.Sprintf("r%d", l.PlanN), hField(l.State)
		x.Title = hTitle(fmt.Sprintf("launch of r%d of %s as %s", l.PlanN, key, cmp.Or(l.Label, l.RunID, l.ID)))
		x.Summary = hSummary(strings.Join(hNonEmpty(l.State, l.RunID, l.Why), " · "))
		out = append(out, x)
		if l.PlanN > 0 {
			links = append(links, history.Link{From: x.Key, Rel: relLaunched, To: hPlanRef(key, l.PlanN)})
		}
	}

	for _, l := range r.v.Links {
		if l.Kind != dispatch.LinkDelivery || l.Candidate == "" || len(l.Verified) == 0 || !l.DeletedAt.IsZero() {
			continue
		}
		cand, err := dispatch.ParseCandidate(l.Candidate)
		if err != nil {
			continue
		}
		var v dispatch.Verified
		if json.Unmarshal(l.Verified, &v) != nil {
			continue
		}
		x := base
		x.Key = fmt.Sprintf("%s#delivery/%s", ref, l.ID)
		x.Ref, x.Kind, x.At, x.Actor = ref, "delivery.verified", hAt(cmp.Or(v.At, l.CreatedAt), e.At), hField(l.CreatedBy)
		x.Commit, x.Run, x.Outcome = cand.Commit, hField(cand.Run), "failed"
		if v.Passed {
			x.Outcome = "passed"
		}
		x.Title = hTitle(fmt.Sprintf("%s delivered by %s, %s", key, cand.Name(), x.Outcome))
		x.Summary = hSummary(fmt.Sprintf("%d receipts · %s", len(v.Receipts), cand.String()))
		out = append(out, x)
		links = append(links, history.Link{From: ref, Rel: relDelivers, To: attemptKey(cand.Run, cand.Task, cand.Attempt)})
	}
	return out, links
}

// planAct is the act on a plan revision a change records: approved (its
// approved_at set), withdrawn (its withdrawn_at set) or superseded (its
// state so set by another revision's approval); "" for any other change.
func planAct(c dispatch.ChangeRow) string {
	if c.Entity != dispatch.EntityPlan {
		return ""
	}
	switch {
	case c.Field == "approved_at" && c.Value != "":
		return dispatch.PlanApproved
	case c.Field == "withdrawn_at" && c.Value != "":
		return dispatch.PlanWithdrawn
	case c.Field == "state" && c.Value == dispatch.PlanSuperseded:
		return dispatch.PlanSuperseded
	}
	return ""
}

// objectID says s is a full git object id (SHA-1 or SHA-256).
func objectID(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0123456789abcdef") == ""
}

// The items of a brief the history keeps: Brakeman's account, in words.
var briefItemWords = map[string]string{
	brakeman.ItemFact:           "fact",
	brakeman.ItemInterpretation: "interpretation",
	brakeman.ItemProposal:       "proposal",
	brakeman.ItemOutcome:        "outcome",
}

// briefSawTerminals says the brief's turn could see the terminals' screens:
// its footprint says so, or, with none, its configuration as the turn
// began did.
func briefSawTerminals(req brakeman.Request) bool {
	if req.Footprint != nil {
		return slices.Contains(req.Footprint.Classes, brakeman.ClassTerminals)
	}
	return req.Config.Visibility.Has(brakeman.ClassTerminals)
}

// briefEntries is a kept brief's entries: one brief.item per fact,
// interpretation, proposal and outcome (said: Brakeman's words), each
// citing the refs it names. The request's own note, what a person asked,
// is never among them.
func briefEntries(req brakeman.Request, b brakeman.Brief) ([]history.Entry, []history.Link) {
	src := hBriefRef(req.ID)
	words := !briefSawTerminals(req)
	at := hAt(req.BriefAt, hAt(req.CreatedAt, time.Unix(0, 0)))
	var out []history.Entry
	var links []history.Link
	for i, it := range b.Items {
		what, ok := briefItemWords[it.Kind]
		if !ok {
			continue
		}
		x := history.Entry{Anchor: src, At: at, Actor: "brakeman", Agent: hField(req.Agent.AgentID), AgentVersion: hField(req.Agent.Version)}
		x.Key = hBriefItemRef(req.ID, i)
		x.Ref, x.Kind = x.Key, "brief.item"
		x.Title = hTitle("Brakeman's " + what)
		x.Summary = hSummary(strings.Join(hNonEmpty(b.Title, req.Kind, req.ID), " · "))
		if words {
			text := ""
			switch it.Kind {
			case brakeman.ItemFact:
				text = it.Fact.Text
			case brakeman.ItemInterpretation:
				text = it.Interpretation.Text
			case brakeman.ItemProposal:
				text = it.Proposal.Text
			case brakeman.ItemOutcome:
				text = it.Outcome.Text
			}
			x.Title = hTitle("Brakeman's " + what + ": " + text)
			x.Body = history.Clip(text, history.MaxLongBody)
		} else {
			x.Summary = hSummary("its turn saw the terminals: its words are not indexed · " + req.ID)
		}
		out = append(out, x)
		for _, r := range it.Refs() {
			links = append(links, history.Link{From: x.Key, Rel: relCites, To: r.String()})
		}
	}
	return out, dedupLinks(links)
}
