package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
)

// A revision's view (docs/plans/sync-seams.md, "Plan views"): what of a
// revision's body is handed to another replica. The body holds the saved
// crew whole (its working directory, its setup and every check's argv) and
// each agent's command, start and yolo arguments, which stay on this
// machine; the view holds what a person reads of the revision: its base,
// its crew's tasks (each with its member and that member's agent, never a
// check), the agents by their catalog id, the references, the attachments
// by id and name, the comments, the constraints, the decisions and the
// note. Never the crew's working directory, setup or manifest, a check's
// argv, an agent's command, start or yolo arguments or environment, a
// manifest file, a member's arguments or prompt, or what the revision
// hands over of the ticket's source. It is canonical JSON (the store's
// encoding, every list present), version 1, carrying the whole body's
// digest, which stays the revision's identity; it is kept and handed out
// under its own SHA-256, the view digest.

// planViewVersion is the version of the view's shape.
const planViewVersion = 1

// maxPlanView bounds a view as encoded: a revision's body's bound. A test
// lowers it to make a view past its bound.
var maxPlanView = MaxPlanBody

// revisionView is a revision's view, version 1. Every type it holds is a
// view type of this file, each field named here: an allow-list all the
// way down, so a field a body's types gain later stays on the machine
// until a view type names it. makePlanView reads the body's JSON straight
// into these types (viewBody), never through the body's own.
type revisionView struct {
	V           int              `json:"v"`
	Digest      string           `json:"digest"`
	Base        string           `json:"base"`
	Crew        viewCrew         `json:"crew"`
	Agents      []string         `json:"agents"`
	References  []viewReference  `json:"references"`
	Attachments []viewAttachment `json:"attachments"`
	Comments    []viewComment    `json:"comments"`
	Constraints string           `json:"constraints"`
	Decisions   []viewDecision   `json:"decisions"`
	Note        string           `json:"note"`
}

// viewCrew is a view's crew: its id, its name and its tasks.
type viewCrew struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Tasks []viewTask `json:"tasks"`
}

// viewTask is a task as a view gives it: its id, its kind, the member who
// does it and that member's agent, its instructions (the prompt the agent
// is given) and the tasks it needs.
type viewTask struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Member       string   `json:"member"`
	Agent        string   `json:"agent"`
	Instructions string   `json:"instructions"`
	Needs        []string `json:"needs"`
}

// viewAttachment is an attachment a revision hands over, by its id and its
// name.
type viewAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// viewReference is a source reference as a view gives it: its relative
// path, its line range, its note, and the object it names at the base.
type viewReference struct {
	Path   string `json:"path"`
	Start  int    `json:"start,omitempty"`
	End    int    `json:"end,omitempty"`
	Note   string `json:"note,omitempty"`
	Object string `json:"object"`
	Kind   string `json:"kind"`
}

// viewComment is a comment a revision hands over: its id and its body's
// SHA-256 when the revision was made.
type viewComment struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// viewDecision is a decision a person will make, as a view gives it: its
// id, its question, its kind, the tasks that wait on it and whom it is
// for.
type viewDecision struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Kind     string   `json:"kind"`
	Before   []string `json:"before"`
	For      string   `json:"for,omitempty"`
}

// viewBody is what a view reads of a body, in the view's own types: the
// body's other parts (the agents' commands and arguments, a manifest
// file, the source) are never decoded.
type viewBody struct {
	Base   string          `json:"base"`
	Crew   json.RawMessage `json:"crew"`
	Agents []struct {
		ID string `json:"id"`
	} `json:"agents"`
	References  []viewReference `json:"references"`
	Attachments []string        `json:"attachments"`
	Comments    []viewComment   `json:"comments"`
	Constraints string          `json:"constraints"`
	Decisions   []viewDecision  `json:"decisions"`
	Note        string          `json:"note"`
}

// viewBodyCrew is what a view reads of a body's crew.
type viewBodyCrew struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Members []struct {
		Name    string `json:"name"`
		AgentID string `json:"agentId"`
	} `json:"members"`
	Tasks []struct {
		ID           string   `json:"id"`
		Kind         string   `json:"kind"`
		Member       string   `json:"member"`
		Instructions string   `json:"instructions"`
		Needs        []string `json:"needs"`
	} `json:"tasks"`
}

// makePlanView makes the view of the body, as stored, whose digest is
// digest, naming each attachment it hands over by name(id) (empty for
// one the store has no row of). It gives the view as encoded, or false when
// the body is not one the view can be read from or the view would be past
// maxPlanView.
func makePlanView(body, digest string, name func(id string) (string, error)) (string, bool, error) {
	var b viewBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		return "", false, nil
	}
	var c viewBodyCrew
	if len(b.Crew) == 0 || json.Unmarshal(b.Crew, &c) != nil {
		return "", false, nil
	}
	agentOf := map[string]string{}
	for _, m := range c.Members {
		agentOf[m.Name] = m.AgentID
	}
	v := revisionView{
		V: planViewVersion, Digest: digest, Base: b.Base,
		Crew:        viewCrew{ID: c.ID, Name: c.Name, Tasks: []viewTask{}},
		Agents:      []string{},
		References:  nonNil(b.References),
		Attachments: []viewAttachment{},
		Comments:    nonNil(b.Comments),
		Constraints: b.Constraints,
		Decisions:   []viewDecision{},
		Note:        b.Note,
	}
	for _, task := range c.Tasks {
		v.Crew.Tasks = append(v.Crew.Tasks, viewTask{ID: task.ID, Kind: task.Kind, Member: task.Member, Agent: agentOf[task.Member],
			Instructions: task.Instructions, Needs: nonNil(task.Needs)})
	}
	for _, a := range b.Agents {
		v.Agents = append(v.Agents, a.ID)
	}
	for _, id := range b.Attachments {
		n, err := name(id)
		if err != nil {
			return "", false, err
		}
		v.Attachments = append(v.Attachments, viewAttachment{ID: id, Name: n})
	}
	for _, d := range b.Decisions {
		d.Before = nonNil(d.Before)
		v.Decisions = append(v.Decisions, d)
	}
	encoded, err := encodeValue(v)
	if err != nil {
		return "", false, err
	}
	if len(encoded) > maxPlanView {
		return "", false, nil
	}
	return encoded, true, nil
}

// nonNil is s, or an empty slice for none, so a view's lists are always
// present.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// attachmentName is the name of the attachment with the id as the
// transaction reads it, empty for one it holds no row of.
func attachmentName(ctx context.Context, tx *sql.Tx) func(id string) (string, error) {
	return func(id string) (string, error) {
		var n string
		switch err := tx.QueryRowContext(ctx, "SELECT name FROM attachments WHERE id = ?", id).Scan(&n); {
		case isNoRows(err):
			return "", nil
		case err != nil:
			return "", err
		}
		return n, nil
	}
}

// keepPlanView keeps view, the view of the revision id of the body whose
// digest is digest, in plan_views under its SHA-256 and names it as the
// revision's view_digest, in tx.
func keepPlanView(ctx context.Context, tx *sql.Tx, id, digest, view string) error {
	viewDigest := sha256Hex(view)
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO plan_views (view_digest, digest, view) VALUES (?, ?, ?)", viewDigest, digest, []byte(view)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE plans SET view_digest = ? WHERE id = ?", viewDigest, id)
	return err
}

// writePlanViews is version 8's Go step: the view of every revision the
// store holds, made from its body in the migration's transaction, kept in
// plan_views under its digest and named by the revision's view_digest. It
// reads the revisions' ids first and then each body by its id, so it holds
// one body at a time. A revision whose view cannot be made (makePlanView)
// keeps an empty view_digest; a revision made on version 8 has its view
// from its proposal (ProposePlan).
func writePlanViews(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM plans ORDER BY rowid")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	name := attachmentName(ctx, tx)
	for _, id := range ids {
		var body, digest string
		if err := tx.QueryRowContext(ctx, "SELECT body, digest FROM plans WHERE id = ?", id).Scan(&body, &digest); err != nil {
			return err
		}
		view, ok, err := makePlanView(body, digest, name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := keepPlanView(ctx, tx, id, digest, view); err != nil {
			return err
		}
	}
	return nil
}

// planOrigin is where a revision stands between machines: whether its
// body is here, the tag of the replica that made it, and whether the
// last row of its approval came from another machine.
type planOrigin struct {
	bodyHere       bool
	made           string
	approvedBy     string
	approvedRemote bool
}

// planOrigins reads where the revision with the id stands; a database
// before version 8 holds every body and every approval here.
func planOrigins(ctx context.Context, q queryer, id string) (planOrigin, error) {
	o := planOrigin{bodyHere: true}
	if schemaOf(q) < syncSchema {
		return o, nil
	}
	var here int
	if err := q.QueryRowContext(ctx, "SELECT body_here FROM plans WHERE id = ?", id).Scan(&here); err != nil {
		return o, err
	}
	o.bodyHere = here == 1
	made, err := readChanges(ctx, q, "WHERE entity = ? AND entity_id = ? AND op = ?", EntityPlan, id, OpCreate)
	if err != nil {
		return o, err
	}
	if len(made) > 0 && made[0].Remote {
		o.made = made[0].Origin
	}
	// The approval is this machine's own only when the newest row of every
	// one of its five fields and of the revision's state is: one of them
	// from elsewhere makes it approved elsewhere, by the newest such row's
	// origin.
	approvals, err := readChanges(ctx, q, "WHERE entity = ? AND entity_id = ? AND field IN ('approved_at', 'approved_by', 'approved_note', 'approved_rev', 'approved_intent', 'state') AND op = ?", EntityPlan, id, OpSet)
	if err != nil {
		return o, err
	}
	Order(approvals)
	newest := map[string]ChangeRow{}
	for _, r := range approvals {
		newest[r.Field] = r
	}
	var from *ChangeRow
	for _, r := range newest {
		if r.Remote && (from == nil || Less(*from, r)) {
			from = &r
		}
	}
	if from != nil {
		o.approvedBy, o.approvedRemote = from.Origin, true
	}
	return o, nil
}

// deviceName is the name of the machine of the tag, as its provider gave
// it (replica_names), "" for none or for a tag with no name known.
func deviceName(ctx context.Context, q queryer, tag string) (string, error) {
	if tag == "" {
		return "", nil
	}
	var name string
	err := q.QueryRowContext(ctx, "SELECT name FROM replica_names WHERE tag = ?", tag).Scan(&name)
	if isNoRows(err) {
		return "", nil
	}
	return name, err
}

// planViewOf reads the revision's view.
func planViewOf(ctx context.Context, q queryer, id string) (revisionView, error) {
	var view []byte
	if err := q.QueryRowContext(ctx, "SELECT v.view FROM plans p JOIN plan_views v ON v.view_digest = p.view_digest WHERE p.id = ?", id).Scan(&view); err != nil {
		return revisionView{}, err
	}
	var v revisionView
	return v, json.Unmarshal(view, &v)
}
