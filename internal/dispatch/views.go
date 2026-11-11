package dispatch

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// A saved view is a list, a board or a roadmap a person named: its query
// a list's filter, sort and grouping (ViewQuery) as JSON, of one project
// or of every project (project_id ""). Its name, kind and query are
// scalars of the field rules and its project immutable; a delete is a
// tombstone, kept with its history, and there is no undelete. At most
// MaxViews live, every project's together.

// ViewQuery is a saved view's query: a list's filter and sort, and what
// its list is grouped by (Groupings). Its project is the view's own: a
// view of a project lists that project's tickets by it, a view of every
// project every project's.
type ViewQuery struct {
	Filter
	Group string `json:"group,omitempty"`
}

// NewView is what view.create takes: its project by its key, "" for
// every project; its name; its kind (ViewKinds); its query, the JSON of
// a ViewQuery.
type NewView struct {
	Project string `json:"project,omitempty"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Query   string `json:"query"`
}

// ViewEdit is what view.edit takes: each field given is set. A view's
// project never changes.
type ViewEdit struct {
	Name  *string `json:"name,omitempty"`
	Kind  *string `json:"kind,omitempty"`
	Query *string `json:"query,omitempty"`
}

// viewCreated is a view's create row's value: what never changes. Its
// name, kind and query are set rows of their own after it.
type viewCreated struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	CreatedAt time.Time `json:"createdAt"`
}

// viewDefaults is a view's scalar fields, in the order an operation
// records them, with the values the schema gives a new row; viewScalars
// their columns.
var (
	viewDefaults = []fieldValue{{"name", ""}, {"kind", ""}, {"query", "{}"}}
	viewScalars  = columns(viewDefaults)
)

// CreateView saves a view (view.create): of the project with the key, or
// of every project when it is "" (not_found for a key with no project,
// archived for an archived one); a name of 1 to MaxViewName characters
// with no control character; a kind of ViewKinds; a query of at most
// MaxViewQuery bytes that is a ViewQuery's JSON, its values each in its
// vocabulary (bound past its size, invalid otherwise); and at most
// MaxViews live views (bound). One create row records what never
// changes, then one set row per field the schema's default does not
// already hold. The receipt carries the view's id.
func (s *Store) CreateView(ctx context.Context, op Op, v NewView) (Receipt, error) {
	return s.Apply(ctx, op, "view.create", v, func(t *tx) error {
		projectID := ""
		if v.Project != "" {
			p, err := t.project(v.Project)
			if err != nil {
				return err
			}
			if !p.ArchivedAt.IsZero() {
				return refuse(RefusalArchived, "%s is archived", p.Key)
			}
			projectID = p.ID
		}
		after := fieldValues(viewDefaults)
		if err := setView(after, &v.Name, &v.Kind, &v.Query); err != nil {
			return err
		}
		var live int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM views WHERE deleted_at = ''").Scan(&live); err != nil {
			return err
		}
		if err := checkCount("views", live+1, MaxViews); err != nil {
			return err
		}
		id := newID()
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO views (id, project_id, name, kind, created_at, updated_at) VALUES (?, ?, '', '', ?, ?)",
			id, projectID, stamp(t.now), stamp(t.now)); err != nil {
			return err
		}
		t.created(id, "")
		value, err := encodeValue(viewCreated{ID: id, ProjectID: projectID, CreatedAt: t.now})
		if err != nil {
			return err
		}
		base := ChangeRow{Entity: EntityView, EntityID: id, ProjectID: projectID}
		create := base
		create.Op, create.Value = OpCreate, value
		if err := t.record(create); err != nil {
			return err
		}
		return t.setScalars("views", base, viewDefaults, after)
	})
}

// EditView sets the fields given of the live view with the id
// (view.edit), each checked as CreateView checks it: one set row per
// field whose value changes, its prior the value it replaces; an edit
// that changes nothing is refused invalid. The view missing or deleted
// is not_found; an expected revision is the view's; a view of an
// archived project is refused archived.
func (s *Store) EditView(ctx context.Context, op Op, id string, e ViewEdit) (Receipt, error) {
	args := struct {
		ID string `json:"id"`
		ViewEdit
	}{id, e}
	return s.Apply(ctx, op, "view.edit", args, func(t *tx) error {
		v, err := t.view(id)
		if err != nil {
			return err
		}
		before, err := t.scalars("views", v.ID, viewScalars)
		if err != nil {
			return err
		}
		after := fieldValues(before)
		if err := setView(after, e.Name, e.Kind, e.Query); err != nil {
			return err
		}
		return t.setScalars("views", ChangeRow{Entity: EntityView, EntityID: v.ID, ProjectID: v.ProjectID}, before, after)
	})
}

// DeleteView deletes the live view with the id (view.delete): a
// tombstone, its deleted_at set, its row and its history kept. The view
// missing or deleted already is not_found; an expected revision is the
// view's; a view of an archived project is refused archived.
func (s *Store) DeleteView(ctx context.Context, op Op, id string) (Receipt, error) {
	args := struct {
		ID string `json:"id"`
	}{id}
	return s.Apply(ctx, op, "view.delete", args, func(t *tx) error {
		v, err := t.view(id)
		if err != nil {
			return err
		}
		return t.setScalars("views", ChangeRow{Entity: EntityView, EntityID: v.ID, ProjectID: v.ProjectID},
			[]fieldValue{{"deleted_at", ""}}, []fieldValue{{"deleted_at", stamp(t.now)}})
	})
}

// view reads the live view with the id inside the operation, in the
// order the store's rules ask: not_found when missing or deleted; then
// the revision expected, the view's; then archived when its project is.
func (t *tx) view(id string) (View, error) {
	v, err := scanView(t.tx.QueryRowContext(t.ctx, "SELECT "+viewColumns+" FROM views WHERE id = ?", id))
	switch {
	case isNoRows(err):
		return View{}, refuse(RefusalNotFound, "no view %s", id)
	case err != nil:
		return View{}, err
	case !v.DeletedAt.IsZero():
		return View{}, refuse(RefusalNotFound, "the view %s is deleted", id)
	}
	if err := t.expect(v.Rev); err != nil {
		return View{}, err
	}
	if v.ProjectID != "" {
		p, err := projectByID(t.ctx, t.tx, v.ProjectID)
		if err != nil {
			return View{}, err
		}
		if !p.ArchivedAt.IsZero() {
			return View{}, refuse(RefusalArchived, "%s is archived", p.Key)
		}
	}
	return v, nil
}

// setView checks each field given of a view and sets it among the values
// an operation will write.
func setView(after []fieldValue, name, kind, query *string) error {
	if name != nil {
		if err := checkField("view.name", "the name", *name); err != nil {
			return err
		}
		setField(after, "name", *name)
	}
	if kind != nil {
		if !slices.Contains(ViewKinds, *kind) {
			return refuse(RefusalInvalid, "the kind %q is none of %s", *kind, strings.Join(ViewKinds, ", "))
		}
		setField(after, "kind", *kind)
	}
	if query != nil {
		if err := checkField("view.query", "the query", *query); err != nil {
			return err
		}
		if _, err := ParseViewQuery(*query); err != nil {
			return refuse(RefusalInvalid, "the query: %s", strings.TrimPrefix(err.Error(), ErrBadQuery.Error()+": "))
		}
		setField(after, "query", *query)
	}
	return nil
}

// ParseViewQuery reads a saved view's query: a JSON object of a
// ViewQuery's fields and no other, each value in its vocabulary and
// within its bound as a list's filter is (ErrBadQuery otherwise), so a
// view saved is a list the store will run.
func ParseViewQuery(query string) (ViewQuery, error) {
	var vq ViewQuery
	if !strings.HasPrefix(strings.TrimLeft(query, " \t\r\n"), "{") {
		return ViewQuery{}, badQuery("the query is not a JSON object")
	}
	if err := decodeStrict([]byte(query), &vq, false); err != nil {
		return ViewQuery{}, badQuery("the query is not a list's: %v", err)
	}
	if err := vq.Filter.check(); err != nil {
		return ViewQuery{}, err
	}
	if !slices.Contains(Groupings, vq.Group) {
		return ViewQuery{}, badQuery("the grouping %q is none of %s", vq.Group, strings.Join(Groupings[1:], ", "))
	}
	return vq, nil
}

// Views reads the live saved views, by name: with a project's key, that
// project's and every project's, the views its pages offer; with "",
// every live view. ErrNotFound for a key with no project.
func (s *Store) Views(ctx context.Context, project string) ([]View, error) {
	var out []View
	err := s.read(ctx, func(q queryer) error {
		if project == "" {
			var err error
			out, err = viewsOf(ctx, q, "SELECT "+viewColumns+" FROM views WHERE deleted_at = ''"+viewOrder)
			return err
		}
		p, err := projectByKey(ctx, q, project)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = projectViews(ctx, q, p.ID)
		return err
	})
	return answer(out, err)
}

// View reads the view with the id, deleted or not. ErrNotFound when there
// is none.
func (s *Store) View(ctx context.Context, id string) (View, error) {
	var out View
	err := s.read(ctx, func(q queryer) error {
		var err error
		out, err = scanView(q.QueryRowContext(ctx, "SELECT "+viewColumns+" FROM views WHERE id = ?", id))
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	})
	return answer(out, err)
}

// viewOrder is the order the views are read in: by name, without regard
// to ASCII case, then as they were made.
const viewOrder = " ORDER BY name COLLATE NOCASE, created_at, id"

// projectViews reads the live views a project's pages offer: the
// project's own and every project's.
func projectViews(ctx context.Context, q queryer, projectID string) ([]View, error) {
	return viewsOf(ctx, q, "SELECT "+viewColumns+" FROM views WHERE (project_id = ? OR project_id = '') AND deleted_at = ''"+viewOrder, projectID)
}

// viewsOf reads the views a query gives.
func viewsOf(ctx context.Context, q queryer, query string, args ...any) ([]View, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []View{}
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// viewColumns is a view's row as scanView reads it.
const viewColumns = "id, project_id, name, kind, query, created_at, updated_at, deleted_at, rev"

// scanView reads a view's row: its query the JSON it is.
func scanView(row interface{ Scan(...any) error }) (View, error) {
	var v View
	var query, created, updated, deleted string
	if err := row.Scan(&v.ID, &v.ProjectID, &v.Name, &v.Kind, &query, &created, &updated, &deleted, &v.Rev); err != nil {
		return View{}, err
	}
	v.Query = json.RawMessage(query)
	v.CreatedAt, v.UpdatedAt, v.DeletedAt = unstamp(created), unstamp(updated), unstamp(deleted)
	return v, nil
}
