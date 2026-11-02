package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// NewProject is what project.create takes: the key, chosen now and never
// changed; the name; and optionally the description (Markdown), the
// folder, the tracker (native when empty) with its URL, and the close
// policy, the default (Owner decisions, 6) when CloseRequires is nil.
type NewProject struct {
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Cwd           string    `json:"cwd,omitempty"`
	Tracker       string    `json:"tracker,omitempty"`
	TrackerURL    string    `json:"trackerUrl,omitempty"`
	CloseRequires *[]string `json:"closeRequires,omitempty"`
}

// ProjectEdit is what project.edit takes: each field given is set, the
// rest left as they are. The key never changes.
type ProjectEdit struct {
	Name          *string   `json:"name,omitempty"`
	Description   *string   `json:"description,omitempty"`
	Cwd           *string   `json:"cwd,omitempty"`
	Tracker       *string   `json:"tracker,omitempty"`
	TrackerURL    *string   `json:"trackerUrl,omitempty"`
	CloseRequires *[]string `json:"closeRequires,omitempty"`
}

// projectCreated is a project's create row's value: what never changes
// (the field rules' immutable and allocation classes). Every other field
// is a set row of its own after it, so each merges alone and each row
// meets its bound by its field's.
type projectCreated struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"createdAt"`
}

// projectDefaults is a project's scalar fields, in the order an
// operation records them, with the values the schema gives a new row
// before its create's set rows; projectScalars their columns.
var (
	projectDefaults = []fieldValue{{"name", ""}, {"description", ""}, {"cwd", ""}, {"tracker", TrackerNative}, {"tracker_url", ""}, {"close_requires", "[]"}}
	projectScalars  = columns(projectDefaults)
)

// CreateProject makes a project (project.create): its key, one
// upper-case letter then one to five upper-case letters or digits
// (invalid otherwise; key_taken when another project, archived or not,
// holds it); its name, description and folder within their bounds; its
// tracker, native with no URL or external with an http or https one; its
// close policy, acceptance, delivery and verified at most once each, the
// default when none is given. One create row records what
// never changes, then one set row per field the schema's default does
// not already hold. The receipt carries its id and key.
func (s *Store) CreateProject(ctx context.Context, op Op, p NewProject) (Receipt, error) {
	return s.Apply(ctx, op, "project.create", p, func(t *tx) error {
		if !ValidKey(p.Key) {
			return refuse(RefusalInvalid, "the key %q is not one upper-case letter then one to five upper-case letters or digits", p.Key)
		}
		tracker := p.Tracker
		if tracker == "" {
			tracker = TrackerNative
		}
		requires := defaultClosePolicy(tracker)
		if p.CloseRequires != nil {
			requires = *p.CloseRequires
		}
		for _, f := range []struct{ key, label, value string }{
			{"project.name", "the name", p.Name},
			{"project.description", "the description", p.Description},
			{"project.cwd", "the folder", p.Cwd},
		} {
			if err := checkField(f.key, f.label, f.value); err != nil {
				return err
			}
		}
		if err := checkTracker(tracker, p.TrackerURL); err != nil {
			return err
		}
		policy, err := closePolicyValue(requires)
		if err != nil {
			return err
		}
		after := fieldValues(projectDefaults)
		setField(after, "name", p.Name)
		setField(after, "description", p.Description)
		setField(after, "cwd", p.Cwd)
		setField(after, "tracker", tracker)
		setField(after, "tracker_url", p.TrackerURL)
		setField(after, "close_requires", policy)
		var held int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM projects WHERE key = ?", p.Key).Scan(&held); err != nil {
			return err
		}
		if held > 0 {
			return refuse(RefusalKeyTaken, "the key %s is another project's", p.Key)
		}
		id := newID()
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO projects (id, key, name, created_at, updated_at) VALUES (?, ?, '', ?, ?)",
			id, p.Key, stamp(t.now), stamp(t.now)); err != nil {
			return err
		}
		t.created(id, p.Key)
		value, err := encodeValue(projectCreated{ID: id, Key: p.Key, CreatedAt: t.now})
		if err != nil {
			return err
		}
		base := ChangeRow{Entity: EntityProject, EntityID: id, ProjectID: id}
		create := base
		create.Op, create.Value = OpCreate, value
		if err := t.record(create); err != nil {
			return err
		}
		return t.setScalars("projects", base, projectDefaults, after)
	})
}

// EditProject sets the fields given of the project with the key
// (project.edit), each checked as CreateProject checks it, the tracker
// and its URL together as the edit leaves them: one set row per field
// whose value changes, its prior the value it replaces; an edit that
// changes nothing is refused invalid. An archived project refuses it
// (archived); a key with no project is not_found.
func (s *Store) EditProject(ctx context.Context, op Op, key string, e ProjectEdit) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
		ProjectEdit
	}{key, e}
	return s.Apply(ctx, op, "project.edit", args, func(t *tx) error {
		p, err := t.project(key)
		if err != nil {
			return err
		}
		if err := t.expect(p.Rev); err != nil {
			return err
		}
		if !p.ArchivedAt.IsZero() {
			return refuse(RefusalArchived, "%s is archived", p.Key)
		}
		before, err := t.scalars("projects", p.ID, projectScalars)
		if err != nil {
			return err
		}
		after := fieldValues(before)
		for _, f := range []struct {
			key, label, column string
			value              *string
		}{
			{"project.name", "the name", "name", e.Name},
			{"project.description", "the description", "description", e.Description},
			{"project.cwd", "the folder", "cwd", e.Cwd},
		} {
			if f.value == nil {
				continue
			}
			if err := checkField(f.key, f.label, *f.value); err != nil {
				return err
			}
			setField(after, f.column, *f.value)
		}
		if e.Tracker != nil || e.TrackerURL != nil {
			tracker, trackerURL := p.Tracker, p.TrackerURL
			if e.Tracker != nil {
				tracker = *e.Tracker
			}
			if e.TrackerURL != nil {
				trackerURL = *e.TrackerURL
			}
			if err := checkTracker(tracker, trackerURL); err != nil {
				return err
			}
			setField(after, "tracker", tracker)
			setField(after, "tracker_url", trackerURL)
		}
		if e.CloseRequires != nil {
			policy, err := closePolicyValue(*e.CloseRequires)
			if err != nil {
				return err
			}
			setField(after, "close_requires", policy)
		}
		return t.setScalars("projects", ChangeRow{Entity: EntityProject, EntityID: p.ID, ProjectID: p.ID}, before, after)
	})
}

// ArchiveProject archives the project with the key (project.archive):
// archived_at set, one row; every write under it is refused archived
// until it is unarchived. Projects are archived, never deleted, so its
// key stays its. One archived already is invalid.
func (s *Store) ArchiveProject(ctx context.Context, op Op, key string) (Receipt, error) {
	return s.archive(ctx, op, "project.archive", key, true)
}

// UnarchiveProject unarchives the project with the key
// (project.unarchive): archived_at cleared, one row. One not archived is
// invalid.
func (s *Store) UnarchiveProject(ctx context.Context, op Op, key string) (Receipt, error) {
	return s.archive(ctx, op, "project.unarchive", key, false)
}

func (s *Store) archive(ctx context.Context, op Op, intent, key string, archive bool) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
	}{key}
	return s.Apply(ctx, op, intent, args, func(t *tx) error {
		p, err := t.project(key)
		if err != nil {
			return err
		}
		if err := t.expect(p.Rev); err != nil {
			return err
		}
		before, err := t.scalars("projects", p.ID, []string{"archived_at"})
		if err != nil {
			return err
		}
		switch {
		case archive && before[0].value != "":
			return refuse(RefusalInvalid, "%s is archived already", p.Key)
		case !archive && before[0].value == "":
			return refuse(RefusalInvalid, "%s is not archived", p.Key)
		}
		value := ""
		if archive {
			value = stamp(t.now)
		}
		return t.setScalars("projects", ChangeRow{Entity: EntityProject, EntityID: p.ID, ProjectID: p.ID}, before, []fieldValue{{"archived_at", value}})
	})
}

// projectColumns is a project's row as scanProject reads it.
const projectColumns = "id, key, name, description, cwd, tracker, tracker_url, close_requires, next_number, number_from, number_to, archived_at, created_at, updated_at, rev"

// scanProject reads a project's row.
func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var requires, archived, created, updated string
	if err := row.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.Cwd, &p.Tracker, &p.TrackerURL, &requires,
		&p.NextNumber, &p.NumberFrom, &p.NumberTo, &archived, &created, &updated, &p.Rev); err != nil {
		return Project{}, err
	}
	if err := json.Unmarshal([]byte(requires), &p.CloseRequires); err != nil {
		return Project{}, fmt.Errorf("dispatch: the close policy of %s: %w", p.Key, err)
	}
	if p.CloseRequires == nil {
		p.CloseRequires = []string{}
	}
	p.ArchivedAt, p.CreatedAt, p.UpdatedAt = unstamp(archived), unstamp(created), unstamp(updated)
	return p, nil
}

// queryer is what a read goes through: the store's database, or an
// operation's transaction.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// projectByKey reads the project with the key; sql.ErrNoRows when there
// is none.
func projectByKey(ctx context.Context, q queryer, key string) (Project, error) {
	return scanProject(q.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE key = ?", key))
}

// projectByID reads the project with the id; sql.ErrNoRows when there is
// none.
func projectByID(ctx context.Context, q queryer, id string) (Project, error) {
	return scanProject(q.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = ?", id))
}

// project reads the project with the key inside the operation: not_found
// when there is none.
func (t *tx) project(key string) (Project, error) {
	p, err := projectByKey(t.ctx, t.tx, key)
	if isNoRows(err) {
		return Project{}, refuse(RefusalNotFound, "no project %s", key)
	}
	return p, err
}

// ProjectView is a project as its page reads it, from one read
// transaction (Project): its row, its live labels by name, its live
// milestones by target, and the live saved views its pages offer (its
// own and every project's), each view's query the JSON it is.
type ProjectView struct {
	Project    Project     `json:"project"`
	Labels     []Label     `json:"labels"`
	Milestones []Milestone `json:"milestones"`
	Views      []View      `json:"views"`
}

// Project reads the project with the key, archived or not, with its
// labels, milestones and views (ProjectView), in one read transaction.
// ErrNotFound when there is no such project.
func (s *Store) Project(ctx context.Context, key string) (ProjectView, error) {
	var v ProjectView
	err := s.read(ctx, func(q queryer) error {
		p, err := projectByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		v.Project = p
		if v.Labels, err = labelsOf(ctx, q, p.ID); err != nil {
			return err
		}
		if v.Milestones, err = milestonesOf(ctx, q, p.ID); err != nil {
			return err
		}
		v.Views, err = projectViews(ctx, q, p.ID)
		return err
	})
	if err != nil {
		return ProjectView{}, err
	}
	return v, nil
}

// ProjectSummary is a project as the projects page lists it: without its
// description, folder, tracker URL and close policy, which its read
// carries, and with how many of its live tickets are open (neither done
// nor cancelled) and how many done; a cancelled ticket counts in neither.
type ProjectSummary struct {
	ID         string    `json:"id"`
	Key        string    `json:"key"`
	Name       string    `json:"name"`
	Tracker    string    `json:"tracker"`
	ArchivedAt time.Time `json:"archivedAt,omitzero"`
	Open       int       `json:"open"`
	Done       int       `json:"done"`
	Rev        int64     `json:"rev"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ProjectPage is a page of the projects, and Next, the cursor of the
// page after it, "" on the last.
type ProjectPage struct {
	Projects []ProjectSummary `json:"projects"`
	Next     string           `json:"next,omitempty"`
}

// projectsSort is the sort a cursor of the projects names.
const projectsSort = "projects"

// Projects reads a page of the projects by key, with their counts
// (ProjectSummary), in one statement: the archived ones among them only
// when archived is set; at most limit (MaxProjectPage when 0, at most
// MaxProjectPage), after the page whose Next is next ("" for the first
// page). ErrBadQuery for a limit or a cursor out of range.
func (s *Store) Projects(ctx context.Context, archived bool, limit int, next string) (ProjectPage, error) {
	if limit == 0 {
		limit = MaxProjectPage
	}
	if limit < 1 || limit > MaxProjectPage {
		return ProjectPage{}, badQuery("a page is 1 to %d projects, not %d", MaxProjectPage, limit)
	}
	after := ""
	if next != "" {
		c, err := decodeCursor(next)
		if err != nil {
			return ProjectPage{}, err
		}
		key, ok := "", len(c.Keys) == 1 && c.ID == "" && !c.Desc
		if ok {
			key, ok = c.Keys[0].(string)
		}
		if c.Sort != projectsSort || !ok || !ValidKey(key) {
			return ProjectPage{}, badQuery("the cursor is not a page of the projects")
		}
		after = key
	}
	page := ProjectPage{Projects: []ProjectSummary{}}
	err := s.read(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT p.id, p.key, p.name, p.tracker, p.archived_at, p.rev, p.updated_at,
				(SELECT COUNT(*) FROM tickets t WHERE t.project_id = p.id AND t.deleted_at = '' AND t.status NOT IN (?, ?)),
				(SELECT COUNT(*) FROM tickets t WHERE t.project_id = p.id AND t.deleted_at = '' AND t.status = ?)
			FROM projects p WHERE (? OR p.archived_at = '') AND p.key > ? ORDER BY p.key LIMIT ?`,
			TicketDone, TicketCancelled, TicketDone, archived, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ps ProjectSummary
			var archivedAt, updated string
			if err := rows.Scan(&ps.ID, &ps.Key, &ps.Name, &ps.Tracker, &archivedAt, &ps.Rev, &updated, &ps.Open, &ps.Done); err != nil {
				return err
			}
			if len(page.Projects) == limit {
				page.Next = encodeCursor(cursor{Sort: projectsSort, Keys: []any{page.Projects[limit-1].Key}})
				break
			}
			ps.ArchivedAt, ps.UpdatedAt = unstamp(archivedAt), unstamp(updated)
			page.Projects = append(page.Projects, ps)
		}
		return rows.Err()
	})
	if err != nil {
		return ProjectPage{}, err
	}
	return page, nil
}
