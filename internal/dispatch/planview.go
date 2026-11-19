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

// revisionView is a revision's view, version 1.
type revisionView struct {
	V           int              `json:"v"`
	Digest      string           `json:"digest"`
	Base        string           `json:"base"`
	Crew        viewCrew         `json:"crew"`
	Agents      []string         `json:"agents"`
	References  []PlanReference  `json:"references"`
	Attachments []viewAttachment `json:"attachments"`
	Comments    []PlanComment    `json:"comments"`
	Constraints string           `json:"constraints"`
	Decisions   []PlanDecision   `json:"decisions"`
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
// maxPlanView: such a revision has no view and is never handed out.
func makePlanView(body, digest string, name func(id string) (string, error)) (string, bool, error) {
	var b PlanBody
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
		Decisions:   []PlanDecision{},
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

// writePlanViews is version 8's Go step: the view of every revision the
// store holds, made from its body in the migration's transaction, kept in
// plan_views under its digest and named by the revision's view_digest. A
// revision whose view cannot be made (makePlanView) keeps an empty view_digest.
func writePlanViews(ctx context.Context, tx *sql.Tx) error {
	type revision struct{ id, body, digest string }
	rows, err := tx.QueryContext(ctx, "SELECT id, body, digest FROM plans ORDER BY rowid")
	if err != nil {
		return err
	}
	var all []revision
	for rows.Next() {
		var r revision
		if err := rows.Scan(&r.id, &r.body, &r.digest); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	name := func(id string) (string, error) {
		var n string
		switch err := tx.QueryRowContext(ctx, "SELECT name FROM attachments WHERE id = ?", id).Scan(&n); {
		case isNoRows(err):
			return "", nil
		case err != nil:
			return "", err
		}
		return n, nil
	}
	for _, r := range all {
		view, ok, err := makePlanView(r.body, r.digest, name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		viewDigest := sha256Hex(view)
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO plan_views (view_digest, digest, view) VALUES (?, ?, ?)", viewDigest, r.digest, []byte(view)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE plans SET view_digest = ? WHERE id = ?", viewDigest, r.id); err != nil {
			return err
		}
	}
	return nil
}
