package dispatch

import (
	"context"
	"strings"
	"time"
)

// A label is a project's: a name and one of the twelve colours, set on
// tickets as members of their labels set. A live label's name is unique
// in its project on a local write, a rule of the operation and not an
// index: two replicas may each create one name, and a merge keeps both
// (the field rules), a person's to tidy. A delete is a tombstone that
// leaves every ticket and frees its name; there is no undelete.

// NewLabel is what label.create takes: the name and the colour, one of
// LabelColors.
type NewLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// LabelEdit is what label.edit takes: each field given is set.
type LabelEdit struct {
	Name  *string `json:"name,omitempty"`
	Color *string `json:"color,omitempty"`
}

// labelCreated is a label's create row's value: what never changes. Its
// name and colour are set rows of their own after it.
type labelCreated struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	CreatedAt time.Time `json:"createdAt"`
}

// CreateLabel makes a label in the project with the key (label.create):
// a name of 1 to MaxLabelName characters with no control character that
// no live label of the project holds, whatever its case (invalid), a
// colour of LabelColors (invalid), and at most MaxLabels live labels in
// the project (bound). One create row records what never changes, then
// one set row each for the name and the colour. A key with no project is
// not_found; an archived project refuses it (archived). The receipt
// carries the label's id.
func (s *Store) CreateLabel(ctx context.Context, op Op, project string, l NewLabel) (Receipt, error) {
	args := struct {
		Project string `json:"project"`
		NewLabel
	}{project, l}
	return s.Apply(ctx, op, "label.create", args, func(t *tx) error {
		p, err := t.project(project)
		if err != nil {
			return err
		}
		if !p.ArchivedAt.IsZero() {
			return refuse(RefusalArchived, "%s is archived", p.Key)
		}
		if err := checkField("label.name", "the name", l.Name); err != nil {
			return err
		}
		if err := checkColor(l.Color); err != nil {
			return err
		}
		live, err := t.liveLabels(p.ID)
		if err != nil {
			return err
		}
		if err := nameFree(live, "", l.Name, p.Key); err != nil {
			return err
		}
		if err := checkCount("labels", len(live)+1, MaxLabels); err != nil {
			return err
		}
		id := newID()
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO labels (id, project_id, name, created_at) VALUES (?, ?, '', ?)", id, p.ID, stamp(t.now)); err != nil {
			return err
		}
		t.created(id, "")
		value, err := encodeValue(labelCreated{ID: id, ProjectID: p.ID, CreatedAt: t.now})
		if err != nil {
			return err
		}
		base := ChangeRow{Entity: EntityLabel, EntityID: id, ProjectID: p.ID}
		create := base
		create.Op, create.Value = OpCreate, value
		if err := t.record(create); err != nil {
			return err
		}
		return t.setScalars("labels", base, []fieldValue{{"name", ""}, {"color", ""}}, []fieldValue{{"name", l.Name}, {"color", l.Color}})
	})
}

// EditLabel sets the fields given of the live label with the id in the
// project with the key (label.edit), each checked as CreateLabel checks
// it, a new name against the project's other live labels: one set row
// per field whose value changes; an edit that changes nothing is refused
// invalid. The label missing, deleted or another project's is not_found;
// an expected revision is the label's.
func (s *Store) EditLabel(ctx context.Context, op Op, project, id string, e LabelEdit) (Receipt, error) {
	args := struct {
		Project string `json:"project"`
		ID      string `json:"id"`
		LabelEdit
	}{project, id, e}
	return s.Apply(ctx, op, "label.edit", args, func(t *tx) error {
		p, lb, err := t.label(project, id)
		if err != nil {
			return err
		}
		before, err := t.scalars("labels", lb.ID, []string{"name", "color"})
		if err != nil {
			return err
		}
		after := fieldValues(before)
		if e.Name != nil {
			if err := checkField("label.name", "the name", *e.Name); err != nil {
				return err
			}
			live, err := t.liveLabels(p.ID)
			if err != nil {
				return err
			}
			if err := nameFree(live, lb.ID, *e.Name, p.Key); err != nil {
				return err
			}
			setField(after, "name", *e.Name)
		}
		if e.Color != nil {
			if err := checkColor(*e.Color); err != nil {
				return err
			}
			setField(after, "color", *e.Color)
		}
		return t.setScalars("labels", ChangeRow{Entity: EntityLabel, EntityID: lb.ID, ProjectID: p.ID}, before, after)
	})
}

// DeleteLabel deletes the live label with the id in the project with the
// key (label.delete): a tombstone, its deleted_at set, its row and its
// history kept and its name free for a new label; and it leaves every
// ticket that has it, deleted ones included, each membership removed
// with one remove row on that ticket naming the tags of its adds, so no
// ticket keeps it. A reference to it from a ticket is checked inside the
// operation's transaction, under the write lock, so an add or a set
// committed before the delete is removed by it and one after it finds a
// tombstone (not_found). The label missing, deleted already or another
// project's is not_found; an expected revision is the label's.
func (s *Store) DeleteLabel(ctx context.Context, op Op, project, id string) (Receipt, error) {
	args := struct {
		Project string `json:"project"`
		ID      string `json:"id"`
	}{project, id}
	return s.Apply(ctx, op, "label.delete", args, func(t *tx) error {
		p, lb, err := t.label(project, id)
		if err != nil {
			return err
		}
		if err := t.setScalars("labels", ChangeRow{Entity: EntityLabel, EntityID: lb.ID, ProjectID: p.ID},
			[]fieldValue{{"deleted_at", ""}}, []fieldValue{{"deleted_at", stamp(t.now)}}); err != nil {
			return err
		}
		holders, err := readTexts(t.ctx, t.tx, `SELECT ticket_labels.ticket_id FROM ticket_labels JOIN tickets ON tickets.id = ticket_labels.ticket_id
			WHERE ticket_labels.label_id = ? ORDER BY tickets.number`, lb.ID)
		if err != nil {
			return err
		}
		for _, tid := range holders {
			base := ChangeRow{Entity: EntityTicket, EntityID: tid, TicketID: tid, ProjectID: p.ID}
			if _, err := t.members(base, labelSet, nil, []string{lb.ID}); err != nil {
				return err
			}
		}
		return nil
	})
}

// label reads the live label with the id in the project with the key
// inside the operation, in the order the store's rules ask: the project
// not_found; the label not_found when missing, another project's or
// deleted; then the revision expected, the label's; then archived.
func (t *tx) label(project, id string) (Project, Label, error) {
	p, err := t.project(project)
	if err != nil {
		return Project{}, Label{}, err
	}
	lb, err := scanLabel(t.tx.QueryRowContext(t.ctx, "SELECT "+labelColumns+" FROM labels WHERE id = ?", id))
	switch {
	case isNoRows(err):
		return Project{}, Label{}, refuse(RefusalNotFound, "no label %s", id)
	case err != nil:
		return Project{}, Label{}, err
	case lb.ProjectID != p.ID:
		return Project{}, Label{}, refuse(RefusalNotFound, "the label %s is not %s's", id, p.Key)
	case !lb.DeletedAt.IsZero():
		return Project{}, Label{}, refuse(RefusalNotFound, "the label %s is deleted", id)
	}
	if err := t.expect(lb.Rev); err != nil {
		return Project{}, Label{}, err
	}
	if !p.ArchivedAt.IsZero() {
		return Project{}, Label{}, refuse(RefusalArchived, "%s is archived", p.Key)
	}
	return p, lb, nil
}

// liveLabels reads the live labels of the project with the id inside the
// operation.
func (t *tx) liveLabels(projectID string) ([]Label, error) {
	return labelsOf(t.ctx, t.tx, projectID)
}

// nameFree refuses a label's name that a live label of the project other
// than the one with the id except holds, whatever its case: two labels
// "API" and "api" would be one label to a person picking one.
func nameFree(live []Label, except, name, project string) error {
	for _, lb := range live {
		if lb.ID != except && strings.EqualFold(lb.Name, name) {
			return refuse(RefusalInvalid, "the label %q of %s holds the name %q", lb.Name, project, name)
		}
	}
	return nil
}

// Labels reads the live labels of the project with the key, by name: two
// of one name, which a merge may leave, both. ErrNotFound when there is
// no such project.
func (s *Store) Labels(ctx context.Context, project string) ([]Label, error) {
	var out []Label
	err := s.read(ctx, func(q queryer) error {
		p, err := projectByKey(ctx, q, project)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = labelsOf(ctx, q, p.ID)
		return err
	})
	return answer(out, err)
}

// Label reads the label with the id of the project with the key, deleted
// or not. ErrNotFound when the project has no such label.
func (s *Store) Label(ctx context.Context, project, id string) (Label, error) {
	var out Label
	err := s.read(ctx, func(q queryer) error {
		p, err := projectByKey(ctx, q, project)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = scanLabel(q.QueryRowContext(ctx, "SELECT "+labelColumns+" FROM labels WHERE id = ? AND project_id = ?", id, p.ID))
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	})
	return answer(out, err)
}

// labelsOf reads the live labels of the project with the id, by name.
func labelsOf(ctx context.Context, q queryer, projectID string) ([]Label, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+labelColumns+" FROM labels WHERE project_id = ? AND deleted_at = '' ORDER BY name, created_at, id", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Label{}
	for rows.Next() {
		lb, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, lb)
	}
	return out, rows.Err()
}

// labelColumns is a label's row as scanLabel reads it.
const labelColumns = "id, project_id, name, color, created_at, deleted_at, rev"

// scanLabel reads a label's row.
func scanLabel(row interface{ Scan(...any) error }) (Label, error) {
	var lb Label
	var created, deleted string
	if err := row.Scan(&lb.ID, &lb.ProjectID, &lb.Name, &lb.Color, &created, &deleted, &lb.Rev); err != nil {
		return Label{}, err
	}
	lb.CreatedAt, lb.DeletedAt = unstamp(created), unstamp(deleted)
	return lb, nil
}
