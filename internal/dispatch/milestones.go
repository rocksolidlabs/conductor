package dispatch

import (
	"context"
	"time"
)

// A milestone is a project's: a name, a description, a target date and
// open or closed, named by a ticket's milestone_id. Names are not unique:
// a milestone of a repeated name is a person's to tidy. A delete is a
// tombstone that clears milestone_id on every ticket naming it; there is
// no undelete.

// NewMilestone is what milestone.create takes: the name, and optionally
// the description (Markdown), the target (YYYY-MM-DD) and the state
// (open when empty).
type NewMilestone struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Target      string `json:"target,omitempty"`
	State       string `json:"state,omitempty"`
}

// MilestoneEdit is what milestone.edit takes: each field given is set,
// the target "" for none.
type MilestoneEdit struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Target      *string `json:"target,omitempty"`
	State       *string `json:"state,omitempty"`
}

// milestoneCreated is a milestone's create row's value: what never
// changes. Every other field is a set row of its own after it.
type milestoneCreated struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	CreatedAt time.Time `json:"createdAt"`
}

// milestoneDefaults is a milestone's scalar fields, in the order an
// operation records them, with the values the schema gives a new row;
// milestoneScalars their columns.
var (
	milestoneDefaults = []fieldValue{{"name", ""}, {"description", ""}, {"target", ""}, {"state", MilestoneOpen}}
	milestoneScalars  = columns(milestoneDefaults)
)

// CreateMilestone makes a milestone in the project with the key
// (milestone.create): a name of 1 to MaxMilestoneName characters, a
// description of at most MaxMilestoneDescription bytes, a target date or
// none, open or closed (invalid otherwise), and at most MaxMilestones
// live milestones in the project (bound). One create row records what
// never changes, then one set row per field the schema's default does
// not already hold. A key with no project is not_found; an archived
// project refuses it (archived). The receipt carries the milestone's id.
func (s *Store) CreateMilestone(ctx context.Context, op Op, project string, m NewMilestone) (Receipt, error) {
	args := struct {
		Project string `json:"project"`
		NewMilestone
	}{project, m}
	return s.Apply(ctx, op, "milestone.create", args, func(t *tx) error {
		p, err := t.project(project)
		if err != nil {
			return err
		}
		if !p.ArchivedAt.IsZero() {
			return refuse(RefusalArchived, "%s is archived", p.Key)
		}
		state := m.State
		if state == "" {
			state = MilestoneOpen
		}
		after := fieldValues(milestoneDefaults)
		if err := setMilestone(after, &m.Name, &m.Description, &m.Target, &state); err != nil {
			return err
		}
		var live int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM milestones WHERE project_id = ? AND deleted_at = ''", p.ID).Scan(&live); err != nil {
			return err
		}
		if err := checkCount("milestones", live+1, MaxMilestones); err != nil {
			return err
		}
		id := newID()
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO milestones (id, project_id, name, created_at, updated_at) VALUES (?, ?, '', ?, ?)",
			id, p.ID, stamp(t.now), stamp(t.now)); err != nil {
			return err
		}
		t.created(id, "")
		value, err := encodeValue(milestoneCreated{ID: id, ProjectID: p.ID, CreatedAt: t.now})
		if err != nil {
			return err
		}
		base := ChangeRow{Entity: EntityMilestone, EntityID: id, ProjectID: p.ID}
		create := base
		create.Op, create.Value = OpCreate, value
		if err := t.record(create); err != nil {
			return err
		}
		return t.setScalars("milestones", base, milestoneDefaults, after)
	})
}

// EditMilestone sets the fields given of the live milestone with the id
// in the project with the key (milestone.edit), each checked as
// CreateMilestone checks it: one set row per field whose value changes;
// an edit that changes nothing is refused invalid. The milestone missing,
// deleted or another project's is not_found; an expected revision is the
// milestone's.
func (s *Store) EditMilestone(ctx context.Context, op Op, project, id string, e MilestoneEdit) (Receipt, error) {
	args := struct {
		Project string `json:"project"`
		ID      string `json:"id"`
		MilestoneEdit
	}{project, id, e}
	return s.Apply(ctx, op, "milestone.edit", args, func(t *tx) error {
		p, m, err := t.milestone(project, id)
		if err != nil {
			return err
		}
		before, err := t.scalars("milestones", m.ID, milestoneScalars)
		if err != nil {
			return err
		}
		after := fieldValues(before)
		if err := setMilestone(after, e.Name, e.Description, e.Target, e.State); err != nil {
			return err
		}
		return t.setScalars("milestones", ChangeRow{Entity: EntityMilestone, EntityID: m.ID, ProjectID: p.ID}, before, after)
	})
}

// DeleteMilestone deletes the live milestone with the id in the project
// with the key (milestone.delete): a tombstone, its deleted_at set, its
// row and its history kept; and it clears milestone_id on every ticket
// naming it, deleted ones included, one set row on each, its prior the
// milestone's id. A ticket's set of it is checked inside the operation's
// transaction, under the write lock, so a set committed before the
// delete is cleared by it and one after it finds a tombstone
// (not_found). The milestone missing, deleted already or another
// project's is not_found; an expected revision is the milestone's.
func (s *Store) DeleteMilestone(ctx context.Context, op Op, project, id string) (Receipt, error) {
	args := struct {
		Project string `json:"project"`
		ID      string `json:"id"`
	}{project, id}
	return s.Apply(ctx, op, "milestone.delete", args, func(t *tx) error {
		p, m, err := t.milestone(project, id)
		if err != nil {
			return err
		}
		if err := t.setScalars("milestones", ChangeRow{Entity: EntityMilestone, EntityID: m.ID, ProjectID: p.ID},
			[]fieldValue{{"deleted_at", ""}}, []fieldValue{{"deleted_at", stamp(t.now)}}); err != nil {
			return err
		}
		holders, err := readTexts(t.ctx, t.tx, "SELECT id FROM tickets WHERE milestone_id = ? ORDER BY number", m.ID)
		if err != nil {
			return err
		}
		for _, tid := range holders {
			if err := t.setScalars("tickets", ChangeRow{Entity: EntityTicket, EntityID: tid, TicketID: tid, ProjectID: p.ID},
				[]fieldValue{{"milestone_id", m.ID}}, []fieldValue{{"milestone_id", ""}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// setMilestone checks each field given of a milestone and sets it among
// the values an operation will write.
func setMilestone(after []fieldValue, name, description, target, state *string) error {
	if name != nil {
		if err := checkField("milestone.name", "the name", *name); err != nil {
			return err
		}
		setField(after, "name", *name)
	}
	if description != nil {
		if err := checkField("milestone.description", "the description", *description); err != nil {
			return err
		}
		setField(after, "description", *description)
	}
	if target != nil {
		if err := checkTarget(*target); err != nil {
			return err
		}
		setField(after, "target", *target)
	}
	if state != nil {
		if err := checkMilestoneState(*state); err != nil {
			return err
		}
		setField(after, "state", *state)
	}
	return nil
}

// milestone reads the live milestone with the id in the project with the
// key inside the operation, in the order the store's rules ask: the
// project not_found; the milestone not_found when missing, another
// project's or deleted; then the revision expected, the milestone's;
// then archived.
func (t *tx) milestone(project, id string) (Project, Milestone, error) {
	p, err := t.project(project)
	if err != nil {
		return Project{}, Milestone{}, err
	}
	m, err := scanMilestone(t.tx.QueryRowContext(t.ctx, "SELECT "+milestoneColumns+" FROM milestones WHERE id = ?", id))
	switch {
	case isNoRows(err):
		return Project{}, Milestone{}, refuse(RefusalNotFound, "no milestone %s", id)
	case err != nil:
		return Project{}, Milestone{}, err
	case m.ProjectID != p.ID:
		return Project{}, Milestone{}, refuse(RefusalNotFound, "the milestone %s is not %s's", id, p.Key)
	case !m.DeletedAt.IsZero():
		return Project{}, Milestone{}, refuse(RefusalNotFound, "the milestone %s is deleted", id)
	}
	if err := t.expect(m.Rev); err != nil {
		return Project{}, Milestone{}, err
	}
	if !p.ArchivedAt.IsZero() {
		return Project{}, Milestone{}, refuse(RefusalArchived, "%s is archived", p.Key)
	}
	return p, m, nil
}

// Milestones reads the live milestones of the project with the key, by
// target, those with none last, then by name. ErrNotFound when there is
// no such project.
func (s *Store) Milestones(ctx context.Context, project string) ([]Milestone, error) {
	var out []Milestone
	err := s.read(ctx, func(q queryer) error {
		p, err := projectByKey(ctx, q, project)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = milestonesOf(ctx, q, p.ID)
		return err
	})
	return answer(out, err)
}

// milestonesOf reads the live milestones of the project with the id, by
// target, those with none last, then by name (Milestones).
func milestonesOf(ctx context.Context, q queryer, projectID string) ([]Milestone, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+milestoneColumns+" FROM milestones WHERE project_id = ? AND deleted_at = '' ORDER BY target = '', target, name, created_at, id", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Milestone{}
	for rows.Next() {
		m, err := scanMilestone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Milestone reads the milestone with the id of the project with the key,
// deleted or not. ErrNotFound when the project has no such milestone.
func (s *Store) Milestone(ctx context.Context, project, id string) (Milestone, error) {
	var out Milestone
	err := s.read(ctx, func(q queryer) error {
		p, err := projectByKey(ctx, q, project)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = scanMilestone(q.QueryRowContext(ctx, "SELECT "+milestoneColumns+" FROM milestones WHERE id = ? AND project_id = ?", id, p.ID))
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	})
	return answer(out, err)
}

// milestoneColumns is a milestone's row as scanMilestone reads it.
const milestoneColumns = "id, project_id, name, description, target, state, created_at, updated_at, deleted_at, rev"

// scanMilestone reads a milestone's row.
func scanMilestone(row interface{ Scan(...any) error }) (Milestone, error) {
	var m Milestone
	var created, updated, deleted string
	if err := row.Scan(&m.ID, &m.ProjectID, &m.Name, &m.Description, &m.Target, &m.State, &created, &updated, &deleted, &m.Rev); err != nil {
		return Milestone{}, err
	}
	m.CreatedAt, m.UpdatedAt, m.DeletedAt = unstamp(created), unstamp(updated), unstamp(deleted)
	return m, nil
}
