package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A ticket's plan revisions (Program item DS2,
// docs/plans/dispatch-evidence.md): the record between what a ticket asks
// and the run that does it. A revision is taken from a saved crew with
// tasks, pinned to a commit, and immutable once made but for its state,
// its approval and its withdrawal: a change is the next revision (based
// on the one before). The operator approves one against the ticket's
// intent (IntentDigest), which supersedes the ticket's other approved
// revision in the same transaction; an approval holds while the intent is
// the one it recorded and every comment the revision hands over still
// has the body it had, and the reads say when it no longer does and what
// cures it (PlanStale). A revision's number is its place among its
// ticket's revisions, derived at read as a comment's is, so a replicated
// revision takes its place without renumbering a row; it is addressed by
// its id. Its rows are entity plan, naming its ticket and project, and
// bump the revision's rev, never the ticket's, so proposing or approving
// never makes a ticket's edit stale.

// IntentDigest is a ticket's intent as an approval records it: the
// SHA-256, in hex, of its title, its description and its acceptance
// criteria as a JSON array, encoded as the store encodes values (HTML
// escaping off). An edit of any of the three changes it; nothing else
// does.
func IntentDigest(tk Ticket) string {
	value, _ := encodeValue([]string{tk.Title, tk.Description, tk.Acceptance})
	return sha256Hex(value)
}

// sha256Hex is the SHA-256 of s, in hex.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// The intents of a revision's approval and withdrawal (its proposal's is
// IntentPlanPropose).
const (
	IntentPlanApprove  = "plan.approve"
	IntentPlanWithdraw = "plan.withdraw"
)

// PlanProposeArgs is plan.propose's content, its fingerprint: the
// ticket's key and the request as the client sent it, never the body the
// API built from it. Replay takes it.
type PlanProposeArgs struct {
	Key string `json:"key"`
	PlanRequest
}

// planCreated is a revision's create row's value, its summary as made:
// what never changes (the field rules' immutable class), never the body,
// which is content named by its digest and past a change row's bound.
type planCreated struct {
	ID            string    `json:"id"`
	TicketID      string    `json:"ticketId"`
	BasedOn       string    `json:"basedOn"`
	CrewID        string    `json:"crewId"`
	CrewName      string    `json:"crewName"`
	CrewUpdatedAt time.Time `json:"crewUpdatedAt,omitzero"`
	Base          string    `json:"base"`
	Digest        string    `json:"digest"`
	Bytes         int64     `json:"bytes"`
	Fingerprint   string    `json:"fingerprint"`
	CreatedAt     time.Time `json:"createdAt"`
	CreatedBy     string    `json:"createdBy"`
}

// The shapes a body's parts take: a commit or object id (SHA-1 or
// SHA-256, in lower-case hex), a SHA-256, a saved crew's id and a task's
// (internal/crew's idPattern and taskIDPattern; the store imports nothing
// of the engine's).
var (
	objectIDPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	crewIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	taskIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
)

// planCrew is what the store reads of a body's crew: its id, its name,
// when it was saved, and its tasks' ids.
type planCrew struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
	Tasks     []struct {
		ID string `json:"id"`
	} `json:"tasks"`
}

// ProposePlan makes a plan revision of the live ticket with the key
// (plan.propose) from req, the request as the client sent it, and body,
// what the API built from it (the saved crew as it is, the agents as they
// resolve, the base made a full commit, the references resolved there).
// The operation's content is req alone (PlanProposeArgs), so a resend is
// answered by its receipt whatever the crew or HEAD became since. In the
// transaction: the ticket live (not_found) and its project not archived
// (archived); at most MaxPlans revisions on it (bound); the revision it
// is based on one of the ticket's (not_found); the body's crew a JSON
// object of the request's crew with tasks (invalid), every part of the
// body of its shape (invalid) and within its bound (bound); the
// attachments and comments it hands over the ticket's and live
// (not_found), at most MaxPlanAttachments of MaxPlanAttachmentBytes and
// MaxPlanComments of MaxPlanCommentBytes (bound), each comment's SHA-256
// set from its row; every decision waiting on tasks of the crew
// (invalid); the body as encoded at most MaxPlanBody (bound, naming its
// largest part). One plans row, proposed, its fingerprint the
// operation's, and one create row whose value is the revision's summary
// (planCreated), never the body. Any actor may propose. The receipt's id
// is the revision's.
func (s *Store) ProposePlan(ctx context.Context, op Op, key string, req PlanRequest, body PlanBody) (Receipt, error) {
	return s.Apply(ctx, op, IntentPlanPropose, PlanProposeArgs{Key: key, PlanRequest: req}, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		var n int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM plans WHERE ticket_id = ?", tk.ID).Scan(&n); err != nil {
			return err
		}
		if err := checkCount("plan revisions on a ticket", n+1, MaxPlans); err != nil {
			return err
		}
		if req.BasedOn != "" {
			var of string
			switch err := t.tx.QueryRowContext(t.ctx, "SELECT ticket_id FROM plans WHERE id = ?", req.BasedOn).Scan(&of); {
			case isNoRows(err):
				return refuse(RefusalNotFound, "no plan revision %s", req.BasedOn)
			case err != nil:
				return err
			case of != tk.ID:
				return refuse(RefusalNotFound, "the plan revision %s is not %s's", req.BasedOn, tk.Key)
			}
		}
		crew, err := readPlanCrew(body.Crew, req.Crew)
		if err != nil {
			return err
		}
		if !objectIDPattern.MatchString(body.Base) {
			return refuse(RefusalInvalid, "the base %q is not a full commit id", body.Base)
		}
		if err := checkPlanAgents(body.Agents); err != nil {
			return err
		}
		if err := checkManifestFile(body.ManifestFile); err != nil {
			return err
		}
		if err := checkReferences(body.References); err != nil {
			return err
		}
		if err := t.planAttachments(tk, body.Attachments); err != nil {
			return err
		}
		if body.Comments, err = t.planComments(tk, body.Comments); err != nil {
			return err
		}
		if err := checkField("plan.constraints", "the constraints", body.Constraints); err != nil {
			return err
		}
		if err := checkDecisions(body.Decisions, crew); err != nil {
			return err
		}
		if err := checkField("plan.note", "the note", body.Note); err != nil {
			return err
		}
		body = body.normalized()
		encoded, err := encodeValue(body)
		if err != nil {
			return err
		}
		if len(encoded) > MaxPlanBody {
			return bodyBound(body, len(encoded))
		}
		p := planCreated{
			ID: newID(), TicketID: tk.ID, BasedOn: req.BasedOn, CrewID: crew.ID, CrewName: crew.Name, CrewUpdatedAt: crew.UpdatedAt.UTC(),
			Base: body.Base, Digest: sha256Hex(encoded), Bytes: int64(len(encoded)), Fingerprint: t.fp, CreatedAt: t.now, CreatedBy: t.op.author(),
		}
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO plans (id, ticket_id, based_on, crew_id, crew_name, crew_updated_at, base, body, digest, bytes,
			state, created_at, created_by, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.TicketID, p.BasedOn, p.CrewID, p.CrewName, stamp(p.CrewUpdatedAt), p.Base, encoded, p.Digest, p.Bytes,
			PlanProposed, stamp(p.CreatedAt), p.CreatedBy, p.Fingerprint); err != nil {
			return err
		}
		t.created(p.ID, "")
		value, err := encodeValue(p)
		if err != nil {
			return err
		}
		return t.record(ChangeRow{Entity: EntityPlan, EntityID: p.ID, TicketID: tk.ID, ProjectID: tk.ProjectID, Op: OpCreate, Value: value})
	})
}

// readPlanCrew reads what the store needs of a body's crew: a JSON object
// whose id is the request's crew, with at least one task (a crew without
// tasks is not planned: the approval covers exactly what runs).
func readPlanCrew(raw json.RawMessage, want string) (planCrew, error) {
	var c planCrew
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &c) != nil {
		return planCrew{}, refuse(RefusalInvalid, "the revision's crew is not a crew")
	}
	switch {
	case !crewIDPattern.MatchString(c.ID):
		return planCrew{}, refuse(RefusalInvalid, "the revision's crew has no crew's id")
	case c.ID != want:
		return planCrew{}, refuse(RefusalInvalid, "the revision's crew is %s, not the crew %s the request names", c.ID, want)
	case len(c.Tasks) == 0:
		return planCrew{}, refuse(RefusalInvalid, "the crew %s has no tasks: a revision plans a crew with tasks", c.ID)
	}
	if err := checkField("plan.crew_name", "the crew's name", c.Name); err != nil {
		return planCrew{}, err
	}
	return c, nil
}

// hasTask reports whether the crew has a task with the id.
func (c planCrew) hasTask(id string) bool {
	for _, task := range c.Tasks {
		if task.ID == id {
			return true
		}
	}
	return false
}

// checkPlanAgents checks a body's agents: at most MaxPlanAgents, each a
// catalog agent's id once, with a command, its selection's SHA-256 and
// its environment's (empty for none).
func checkPlanAgents(agents []PlanAgent) error {
	if err := checkCount("agents in a revision", len(agents), MaxPlanAgents); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range agents {
		switch {
		case !agentIDPattern.MatchString(a.ID):
			return refuse(RefusalInvalid, "the agent %q is not a catalog agent's id", a.ID)
		case seen[a.ID]:
			return refuse(RefusalInvalid, "the agent %s is named twice", a.ID)
		case len(a.Command) == 0:
			return refuse(RefusalInvalid, "the agent %s has no command", a.ID)
		case !sha256Pattern.MatchString(a.SHA256):
			return refuse(RefusalInvalid, "the agent %s's selection has no SHA-256", a.ID)
		case a.EnvSHA256 != "" && !sha256Pattern.MatchString(a.EnvSHA256):
			return refuse(RefusalInvalid, "the agent %s's environment's SHA-256 is not one", a.ID)
		}
		seen[a.ID] = true
	}
	return nil
}

// checkManifestFile checks a body's file-form project manifest, when
// there is one: its path a reference's, its blob an object id, its
// manifest a JSON object, all of it at most MaxManifestFile as encoded.
func checkManifestFile(m *PlanManifestFile) error {
	if m == nil {
		return nil
	}
	if err := checkPath("the manifest file's path", m.Path); err != nil {
		return err
	}
	if !objectIDPattern.MatchString(m.Blob) {
		return refuse(RefusalInvalid, "the manifest file's blob %q is not an object id", m.Blob)
	}
	if len(m.Manifest) == 0 || m.Manifest[0] != '{' || !json.Valid(m.Manifest) {
		return refuse(RefusalInvalid, "the manifest file's manifest is not a JSON object")
	}
	encoded, err := encodeValue(m)
	if err != nil {
		return err
	}
	if len(encoded) > MaxManifestFile {
		return refuse(RefusalBound, "the manifest file is %d bytes as encoded, more than %d", len(encoded), MaxManifestFile)
	}
	return nil
}

// checkPath checks a path relative to the crew's working directory: at
// most MaxReferencePath bytes of text, slash-separated, cleaned, never
// absolute and never leaving the directory ("." is the directory).
func checkPath(label, p string) error {
	if err := checkField("plan.reference_path", label, p); err != nil {
		return err
	}
	switch {
	case strings.Contains(p, `\`):
		return refuse(RefusalInvalid, "%s %q is not slash-separated", label, p)
	case path.IsAbs(p), path.Clean(p) != p:
		return refuse(RefusalInvalid, "%s %q is not a clean relative path", label, p)
	case p == "..", strings.HasPrefix(p, "../"):
		return refuse(RefusalInvalid, "%s %q leaves the working directory", label, p)
	}
	return nil
}

// checkReferences checks a body's references: at most MaxPlanReferences,
// each a path (checkPath), an optional line range from 1 with its end at
// least its start, a note of at most MaxReferenceNote characters, and
// the object the path names at the base with its kind.
func checkReferences(refs []PlanReference) error {
	if err := checkCount("references in a revision", len(refs), MaxPlanReferences); err != nil {
		return err
	}
	for _, r := range refs {
		if err := checkPath("a reference's path", r.Path); err != nil {
			return err
		}
		switch {
		case r.Start < 0 || r.End < 0:
			return refuse(RefusalInvalid, "the reference %s's lines are numbered from 1", r.Path)
		case r.End > 0 && r.Start == 0:
			return refuse(RefusalInvalid, "the reference %s's line range has an end and no start", r.Path)
		case r.End > 0 && r.End < r.Start:
			return refuse(RefusalInvalid, "the reference %s's line range ends before it starts", r.Path)
		case !objectIDPattern.MatchString(r.Object):
			return refuse(RefusalInvalid, "the reference %s names no object", r.Path)
		case r.Kind != ObjectBlob && r.Kind != ObjectTree:
			return refuse(RefusalInvalid, "the reference %s's kind %q is neither blob nor tree", r.Path, r.Kind)
		}
		if err := checkField("plan.reference_note", "the reference "+r.Path+"'s note", r.Note); err != nil {
			return err
		}
	}
	return nil
}

// planAttachments checks the attachments a revision hands over: at most
// MaxPlanAttachments (bound, before any is looked up), each named once
// (invalid), each a live attachment of the ticket (not_found), their
// content at most MaxPlanAttachmentBytes together (bound).
func (t *tx) planAttachments(tk Ticket, ids []string) error {
	if err := checkCount("attachments handed over", len(ids), MaxPlanAttachments); err != nil {
		return err
	}
	var total int64
	for i, id := range ids {
		if slices.Contains(ids[:i], id) {
			return refuse(RefusalInvalid, "the attachment %s is handed over twice", id)
		}
		var of, deleted string
		var size int64
		switch err := t.tx.QueryRowContext(t.ctx, "SELECT ticket_id, size, deleted_at FROM attachments WHERE id = ?", id).Scan(&of, &size, &deleted); {
		case isNoRows(err):
			return refuse(RefusalNotFound, "no attachment %s", id)
		case err != nil:
			return err
		case of != tk.ID:
			return refuse(RefusalNotFound, "the attachment %s is not %s's", id, tk.Key)
		case deleted != "":
			return refuse(RefusalNotFound, "the attachment %s is removed", id)
		}
		total += size
	}
	if total > MaxPlanAttachmentBytes {
		return refuse(RefusalBound, "the attachments handed over are %d bytes, more than %d", total, MaxPlanAttachmentBytes)
	}
	return nil
}

// planComments checks the comments a revision hands over and gives them
// with the SHA-256 of each one's body as its row holds it now, whatever
// the body built: at most MaxPlanComments (bound, before any is looked
// up), each named once (invalid), each a live comment of the ticket
// (not_found), their bodies at most MaxPlanCommentBytes together (bound).
func (t *tx) planComments(tk Ticket, in []PlanComment) ([]PlanComment, error) {
	if err := checkCount("comments handed over", len(in), MaxPlanComments); err != nil {
		return nil, err
	}
	out := make([]PlanComment, 0, len(in))
	total := 0
	for i, c := range in {
		for _, before := range in[:i] {
			if before.ID == c.ID {
				return nil, refuse(RefusalInvalid, "the comment %s is handed over twice", c.ID)
			}
		}
		var of, body, deleted string
		switch err := t.tx.QueryRowContext(t.ctx, "SELECT ticket_id, body, deleted_at FROM comments WHERE id = ?", c.ID).Scan(&of, &body, &deleted); {
		case isNoRows(err):
			return nil, refuse(RefusalNotFound, "no comment %s", c.ID)
		case err != nil:
			return nil, err
		case of != tk.ID:
			return nil, refuse(RefusalNotFound, "the comment %s is not %s's", c.ID, tk.Key)
		case deleted != "":
			return nil, refuse(RefusalNotFound, "the comment %s is deleted", c.ID)
		}
		total += len(body)
		out = append(out, PlanComment{ID: c.ID, SHA256: sha256Hex(body)})
	}
	if total > MaxPlanCommentBytes {
		return nil, refuse(RefusalBound, "the comments handed over are %d bytes, more than %d", total, MaxPlanCommentBytes)
	}
	return out, nil
}

// checkDecisions checks the decisions a revision names: at most
// MaxPlanDecisions, each an id of a task id's shape named once, a
// question, its kind, and at most MaxDecisionBefore tasks of the crew
// that wait on it, each once.
func checkDecisions(ds []PlanDecision, crew planCrew) error {
	if err := checkCount("decisions in a revision", len(ds), MaxPlanDecisions); err != nil {
		return err
	}
	for i, d := range ds {
		switch {
		case !taskIDPattern.MatchString(d.ID):
			return refuse(RefusalInvalid, "the decision %q's id is not of a task id's shape", d.ID)
		case slices.ContainsFunc(ds[:i], func(o PlanDecision) bool { return o.ID == d.ID }):
			return refuse(RefusalInvalid, "the decision %s is named twice", d.ID)
		case d.Kind != DecisionApproval && d.Kind != DecisionClarification:
			return refuse(RefusalInvalid, "the decision %s's kind %q is neither approval nor clarification", d.ID, d.Kind)
		}
		if err := checkField("plan.decision_question", "the decision "+d.ID+"'s question", d.Question); err != nil {
			return err
		}
		if err := checkCount("tasks waiting on a decision", len(d.Before), MaxDecisionBefore); err != nil {
			return err
		}
		for j, task := range d.Before {
			switch {
			case slices.Contains(d.Before[:j], task):
				return refuse(RefusalInvalid, "the decision %s names the task %s twice", d.ID, task)
			case !crew.hasTask(task):
				return refuse(RefusalInvalid, "the decision %s waits on the task %q, which the crew %s has not", d.ID, task, crew.ID)
			}
		}
	}
	return nil
}

// normalized is the body with every list a list, none null, so a body
// reads back the same whichever way it was built. The lists whose
// elements it sets are copies, never the caller's: a body built with the
// request's decisions (body.Decisions = req.Decisions) leaves the request
// as it was sent, so a resend of it has the same fingerprint.
func (b PlanBody) normalized() PlanBody {
	b.Agents = slices.Clone(b.Agents)
	b.Decisions = slices.Clone(b.Decisions)
	if b.Agents == nil {
		b.Agents = []PlanAgent{}
	}
	for i, a := range b.Agents {
		for _, l := range []*[]string{&a.Command, &a.StartArgs, &a.YoloArgs, &a.EnvNames} {
			if *l == nil {
				*l = []string{}
			}
		}
		b.Agents[i] = a
	}
	if b.References == nil {
		b.References = []PlanReference{}
	}
	if b.Attachments == nil {
		b.Attachments = []string{}
	}
	if b.Comments == nil {
		b.Comments = []PlanComment{}
	}
	if b.Decisions == nil {
		b.Decisions = []PlanDecision{}
	}
	for i, d := range b.Decisions {
		if d.Before == nil {
			d.Before = []string{}
		}
		b.Decisions[i] = d
	}
	return b
}

// bodyBound is the bound refusal of a body of total bytes as encoded,
// past MaxPlanBody: its words name the body's largest part and its size,
// so the person sees what made it too large ("the agents' commands are
// 2.9 MiB of the revision's 3.1 MiB").
func bodyBound(b PlanBody, total int) error {
	parts := []struct {
		name string
		v    any
	}{
		{"the crew is", b.Crew}, {"the agents' commands are", b.Agents}, {"the manifest file is", b.ManifestFile},
		{"the references are", b.References}, {"the attachments are", b.Attachments}, {"the comments are", b.Comments},
		{"the constraints are", b.Constraints}, {"the decisions are", b.Decisions}, {"the note is", b.Note},
	}
	largest, size := "", -1
	for _, p := range parts {
		v, err := encodeValue(p.v)
		if err != nil {
			return err
		}
		if len(v) > size {
			largest, size = p.name, len(v)
		}
	}
	return refuse(RefusalBound, "%s %s of the revision's %s, more than the %s a revision holds", largest, sizeWords(size), sizeWords(total), sizeWords(MaxPlanBody))
}

// sizeWords is n bytes in words: in MiB or KiB to a tenth, else in bytes.
func sizeWords(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// planApproveArgs is plan.approve's content.
type planApproveArgs struct {
	ID        string `json:"id"`
	TicketRev int64  `json:"ticketRev,omitempty"`
	Note      string `json:"note,omitempty"`
}

// ApprovePlan approves the plan revision with the id (plan.approve), the
// operator's alone (an actor other than ActorAdmin is invalid: an
// approval is the operator's), with note, its rationale, at most
// MaxApprovedNote characters of Markdown. The expected revision is the
// revision's, and ticketRev, when given, the ticket's (stale_revision for
// either). The revision missing, or its ticket deleted, is not_found; an
// archived project refuses it (archived); a withdrawn revision is
// invalid; one whose handed-over comment is edited or gone since it was
// made is stale_plan, its remedy a new revision, since the digest it
// recorded is the immutable body's; one approved whose approval holds is
// invalid. It sets approved_at, approved_by (the operator's display
// name), approved_note, approved_rev (the ticket's rev now),
// approved_intent (IntentDigest now) and state approved, one set row each
// that changes, so an approval made again records the new one beside the
// old in the history, and sets every other approved revision of the
// ticket superseded in the same transaction, one row each: on a local
// write a ticket has one approved revision at most. The ticket's rev is
// not bumped.
func (s *Store) ApprovePlan(ctx context.Context, op Op, id string, ticketRev int64, note string) (Receipt, error) {
	args := planApproveArgs{ID: id, TicketRev: ticketRev, Note: note}
	return s.Apply(ctx, op, IntentPlanApprove, args, func(t *tx) error {
		if t.op.Actor != ActorAdmin {
			return refuse(RefusalInvalid, "an approval is the operator's: %s cannot approve a plan revision", t.op.Actor)
		}
		p, tk, err := t.plan(id)
		if err != nil {
			return err
		}
		if ticketRev > 0 && tk.Rev != ticketRev {
			return refuse(RefusalStaleRevision, "the ticket's revision expected is %d; it is %d", ticketRev, tk.Rev)
		}
		if p.State == PlanWithdrawn {
			return refuse(RefusalInvalid, "revision %d is withdrawn", p.N)
		}
		comments, err := planBodyComments(t.ctx, t.tx, p.ID)
		if err != nil {
			return err
		}
		if words, stale, err := commentsStale(t.ctx, t.tx, tk, p, comments); err != nil {
			return err
		} else if stale {
			return refuse(RefusalStalePlan, "%s: revise it", words)
		}
		intent := IntentDigest(tk)
		if p.State == PlanApproved && p.ApprovedIntent == intent {
			return refuse(RefusalInvalid, "revision %d is approved already", p.N)
		}
		if err := checkField("plan.approved_note", "the note", note); err != nil {
			return err
		}
		base := ChangeRow{Entity: EntityPlan, EntityID: p.ID, TicketID: tk.ID, ProjectID: tk.ProjectID}
		cols := []string{"approved_at", "approved_by", "approved_note", "approved_rev", "approved_intent", "state"}
		before, err := t.scalars("plans", p.ID, cols)
		if err != nil {
			return err
		}
		after := []fieldValue{{"approved_at", stamp(t.now)}, {"approved_by", t.op.author()}, {"approved_note", note},
			{"approved_rev", fmt.Sprint(tk.Rev)}, {"approved_intent", intent}, {"state", PlanApproved}}
		if err := t.setScalars("plans", base, before, after); err != nil {
			return err
		}
		others, err := readTexts(t.ctx, t.tx, "SELECT id FROM plans WHERE ticket_id = ? AND state = ? AND id != ? ORDER BY created_at, id", tk.ID, PlanApproved, p.ID)
		if err != nil {
			return err
		}
		for _, other := range others {
			row := base
			row.EntityID = other
			if err := t.setScalars("plans", row, []fieldValue{{"state", PlanApproved}}, []fieldValue{{"state", PlanSuperseded}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// WithdrawPlan withdraws the plan revision with the id (plan.withdraw):
// any revision not withdrawn already (invalid), withdrawn_at,
// withdrawn_by (the display name) and state withdrawn, one set row each.
// An approved one withdrawn leaves its ticket with none approved. The
// expected revision is the revision's; the revision missing, or its
// ticket deleted, is not_found; an archived project refuses it
// (archived).
func (s *Store) WithdrawPlan(ctx context.Context, op Op, id string) (Receipt, error) {
	args := struct {
		ID string `json:"id"`
	}{id}
	return s.Apply(ctx, op, IntentPlanWithdraw, args, func(t *tx) error {
		p, tk, err := t.plan(id)
		if err != nil {
			return err
		}
		if p.State == PlanWithdrawn {
			return refuse(RefusalInvalid, "revision %d is withdrawn already", p.N)
		}
		base := ChangeRow{Entity: EntityPlan, EntityID: p.ID, TicketID: tk.ID, ProjectID: tk.ProjectID}
		cols := []string{"withdrawn_at", "withdrawn_by", "state"}
		before, err := t.scalars("plans", p.ID, cols)
		if err != nil {
			return err
		}
		return t.setScalars("plans", base, before, []fieldValue{{"withdrawn_at", stamp(t.now)}, {"withdrawn_by", t.op.author()}, {"state", PlanWithdrawn}})
	})
}

// plan reads the plan revision with the id inside the operation, with its
// number, and its ticket, in the order the store's rules ask: not_found
// when there is none or its ticket is deleted; then the revision
// expected, the revision's; then archived when the ticket's project is.
func (t *tx) plan(id string) (PlanSummary, Ticket, error) {
	p, err := scanPlan(t.tx.QueryRowContext(t.ctx, "SELECT "+planColumns+" FROM plans WHERE id = ?", id))
	if isNoRows(err) {
		return PlanSummary{}, Ticket{}, refuse(RefusalNotFound, "no plan revision %s", id)
	}
	if err != nil {
		return PlanSummary{}, Ticket{}, err
	}
	tk, err := readTicketByID(t.ctx, t.tx, p.TicketID)
	if err != nil {
		return PlanSummary{}, Ticket{}, err
	}
	if !tk.DeletedAt.IsZero() {
		return PlanSummary{}, Ticket{}, refuse(RefusalNotFound, "%s is deleted", tk.Key)
	}
	if err := t.expect(p.Rev); err != nil {
		return PlanSummary{}, Ticket{}, err
	}
	if _, err := t.open(tk); err != nil {
		return PlanSummary{}, Ticket{}, err
	}
	if p.N, err = planNumber(t.ctx, t.tx, p); err != nil {
		return PlanSummary{}, Ticket{}, err
	}
	return p, tk, nil
}

// readTicketByID is readTicket by the ticket's id.
func readTicketByID(ctx context.Context, q queryer, id string) (Ticket, error) {
	var key string
	if err := q.QueryRowContext(ctx, "SELECT key FROM tickets WHERE id = ?", id).Scan(&key); err != nil {
		return Ticket{}, err
	}
	return readTicket(ctx, q, key)
}

// planNumber is the revision's number: its 1-based place among its
// ticket's revisions by (created_at, id).
func planNumber(ctx context.Context, q queryer, p PlanSummary) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM plans WHERE ticket_id = ? AND (created_at < ? OR (created_at = ? AND id <= ?))",
		p.TicketID, stamp(p.CreatedAt), stamp(p.CreatedAt), p.ID).Scan(&n)
	return n, err
}

// planBodyComments reads the comments the body of the revision with the
// id hands over.
func planBodyComments(ctx context.Context, q queryer, id string) ([]PlanComment, error) {
	var body string
	if err := q.QueryRowContext(ctx, "SELECT body FROM plans WHERE id = ?", id).Scan(&body); err != nil {
		return nil, err
	}
	var b struct {
		Comments []PlanComment `json:"comments"`
	}
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		return nil, fmt.Errorf("dispatch: the body of plan revision %s: %w", id, err)
	}
	return b.Comments, nil
}

// commentsStale says whether a comment the revision hands over is gone
// (deleted, or with no row) or edited since the revision was made, its
// body's SHA-256 no longer the one recorded, and names the first in
// words ("comment 4 was edited since revision 3 was made").
func commentsStale(ctx context.Context, q queryer, tk Ticket, p PlanSummary, comments []PlanComment) (string, bool, error) {
	for _, c := range comments {
		var body, deleted string
		var n int
		err := q.QueryRowContext(ctx, "SELECT c.body, c.deleted_at, numbered.n FROM comments c JOIN ("+numberedComments+") numbered ON numbered.id = c.id WHERE c.id = ?",
			tk.ID, c.ID).Scan(&body, &deleted, &n)
		switch {
		case isNoRows(err):
			return fmt.Sprintf("the comment %s handed over is gone since revision %d was made", c.ID, p.N), true, nil
		case err != nil:
			return "", false, err
		case deleted != "":
			return fmt.Sprintf("comment %d was deleted since revision %d was made", n, p.N), true, nil
		case sha256Hex(body) != c.SHA256:
			return fmt.Sprintf("comment %d was edited since revision %d was made", n, p.N), true, nil
		}
	}
	return "", false, nil
}

// intentFields names a ticket's intent fields in words, in their order.
var intentFields = []struct{ field, words string }{{"title", "the title"}, {"description", "the description"}, {"acceptance", "the acceptance criteria"}}

// intentWords says which of the ticket's intent fields differ now from
// what they were at the revision's approval: a field the ticket's set
// rows after the approval's latest (by the local seq) touched, whose
// first such row's prior (its value at the approval) is not its value
// now, so a field edited and edited back is not named: "the description
// changed since revision 3 was approved", or, when the log does not say
// (an approval a replica made), "the ticket's intent changed".
func intentWords(ctx context.Context, q queryer, tk Ticket, p PlanSummary) (string, error) {
	var since int64
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM changes WHERE entity = ? AND entity_id = ? AND field = 'approved_at'",
		EntityPlan, p.ID).Scan(&since); err != nil {
		return "", err
	}
	// Each field's first set row after the approval, its prior the
	// field's value at the approval.
	rows, err := q.QueryContext(ctx, `SELECT field, prior FROM changes WHERE seq IN (SELECT MIN(seq) FROM changes
		WHERE entity = ? AND entity_id = ? AND op = ? AND seq > ? AND field IN ('title', 'description', 'acceptance') GROUP BY field)`,
		EntityTicket, tk.ID, OpSet, since)
	if err != nil {
		return "", err
	}
	approvedValue := map[string]string{}
	for rows.Next() {
		var field, prior string
		if err := rows.Scan(&field, &prior); err != nil {
			rows.Close()
			return "", err
		}
		approvedValue[field] = prior
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	now := map[string]string{"title": tk.Title, "description": tk.Description, "acceptance": tk.Acceptance}
	var named []string
	for _, f := range intentFields {
		if prior, ok := approvedValue[f.field]; ok && prior != now[f.field] {
			named = append(named, f.words)
		}
	}
	what := "the ticket's intent"
	switch len(named) {
	case 0:
	case 1:
		what = named[0]
	default:
		what = strings.Join(named[:len(named)-1], ", ") + " and " + named[len(named)-1]
	}
	return fmt.Sprintf("%s changed since revision %d was approved", what, p.N), nil
}

// planStale is why the approved revision's approval no longer holds, nil
// while it does or for a revision not approved: a handed-over comment
// edited or gone (RemedyRevise, the intent's change said beside it when
// there is one), else the ticket's intent changed (RemedyApprove).
func planStale(ctx context.Context, q queryer, tk Ticket, p PlanSummary) (*PlanStale, error) {
	if p.State != PlanApproved {
		return nil, nil
	}
	comments, err := planBodyComments(ctx, q, p.ID)
	if err != nil {
		return nil, err
	}
	words, stale, err := commentsStale(ctx, q, tk, p, comments)
	if err != nil {
		return nil, err
	}
	intentChanged := p.ApprovedIntent != IntentDigest(tk)
	var iw string
	if intentChanged {
		if iw, err = intentWords(ctx, q, tk, p); err != nil {
			return nil, err
		}
	}
	switch {
	case stale && intentChanged:
		return &PlanStale{Words: words + "; " + iw, Remedy: RemedyRevise}, nil
	case stale:
		return &PlanStale{Words: words, Remedy: RemedyRevise}, nil
	case intentChanged:
		return &PlanStale{Words: iw, Remedy: RemedyApprove}, nil
	}
	return nil, nil
}

// Plans reads the plan revisions of the ticket with the key, deleted or
// not, newest first: each one's summary with its number, and for an
// approved one whose approval no longer holds, why and its remedy
// (Stale). Never a body. ErrNotFound when there is no such ticket.
func (s *Store) Plans(ctx context.Context, key string) ([]PlanSummary, error) {
	var out []PlanSummary
	err := s.read(ctx, func(q queryer) error {
		tk, err := readTicket(ctx, q, key)
		if err != nil {
			return err
		}
		out, err = plansOf(ctx, q, tk, false)
		return err
	})
	return out, err
}

// plansOf reads the revisions of the ticket, newest first, each with its
// number and its staleness; only the approved ones when approved is set,
// the newest MaxTicketApproved of them (the ticket's read: Ticket). A
// version 1 database, read as it was found, has none.
func plansOf(ctx context.Context, q queryer, tk Ticket, approved bool) ([]PlanSummary, error) {
	if schemaOf(q) < 2 {
		return []PlanSummary{}, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT "+planColumns+" FROM plans WHERE ticket_id = ? ORDER BY created_at, id", tk.ID)
	if err != nil {
		return nil, err
	}
	var all []PlanSummary
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		p.N = len(all) + 1
		all = append(all, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []PlanSummary{}
	for i := len(all) - 1; i >= 0; i-- {
		p := all[i]
		if approved && (p.State != PlanApproved || len(out) == MaxTicketApproved) {
			continue
		}
		if p.Stale, err = planStale(ctx, q, tk, p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Plan reads the plan revision with the id, whole: its summary, with its
// number and its staleness, and its body as the bytes it is kept as.
// ErrNotFound when there is none.
func (s *Store) Plan(ctx context.Context, id string) (PlanView, error) {
	var out PlanView
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 2 {
			// A version 1 database, read as it was found: no revisions.
			return ErrNotFound
		}
		p, err := scanPlan(q.QueryRowContext(ctx, "SELECT "+planColumns+" FROM plans WHERE id = ?", id))
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		tk, err := readTicketByID(ctx, q, p.TicketID)
		if err != nil {
			return err
		}
		if p.N, err = planNumber(ctx, q, p); err != nil {
			return err
		}
		if p.Stale, err = planStale(ctx, q, tk, p); err != nil {
			return err
		}
		var body string
		if err := q.QueryRowContext(ctx, "SELECT body FROM plans WHERE id = ?", id).Scan(&body); err != nil {
			return err
		}
		out = PlanView{PlanSummary: p, Body: json.RawMessage(body)}
		return nil
	})
	return out, err
}

// planColumns is a revision's row as scanPlan reads it, its body not
// among them.
const planColumns = `id, ticket_id, based_on, crew_id, crew_name, crew_updated_at, base, digest, bytes, state, approved_at, approved_by,
	approved_note, approved_rev, approved_intent, withdrawn_at, withdrawn_by, created_at, created_by, fingerprint, rev`

// scanPlan reads a revision's row, its number not among it.
func scanPlan(row interface{ Scan(...any) error }) (PlanSummary, error) {
	var p PlanSummary
	var crewUpdated, approved, withdrawn, created string
	if err := row.Scan(&p.ID, &p.TicketID, &p.BasedOn, &p.CrewID, &p.CrewName, &crewUpdated, &p.Base, &p.Digest, &p.Bytes, &p.State,
		&approved, &p.ApprovedBy, &p.ApprovedNote, &p.ApprovedRev, &p.ApprovedIntent, &withdrawn, &p.WithdrawnBy, &created, &p.CreatedBy,
		&p.Fingerprint, &p.Rev); err != nil {
		return PlanSummary{}, err
	}
	p.CrewUpdatedAt, p.ApprovedAt, p.WithdrawnAt, p.CreatedAt = unstamp(crewUpdated), unstamp(approved), unstamp(withdrawn), unstamp(created)
	return p, nil
}
