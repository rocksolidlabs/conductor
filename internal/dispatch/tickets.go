package dispatch

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"
)

// NewTicket is what ticket.create takes: its project by its key, its
// title, and every other field optionally: the status (backlog when
// empty), the priority (0, none, when not given), the horizon, the
// owner, the assignees (person:<name> or agent:<catalog id>), the labels
// and the milestone by their ids, the parent by its key.
type NewTicket struct {
	Project     string   `json:"project"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Acceptance  string   `json:"acceptance,omitempty"`
	Status      string   `json:"status,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	Horizon     string   `json:"horizon,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Assignees   []string `json:"assignees,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Milestone   string   `json:"milestone,omitempty"`
	Parent      string   `json:"parent,omitempty"`
}

// TicketSet is the scalar fields a ticket.edit sets, each when given:
// the parent by its key and the milestone by its id, "" for none.
type TicketSet struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Acceptance  *string `json:"acceptance,omitempty"`
	Status      *string `json:"status,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
	Horizon     *string `json:"horizon,omitempty"`
	Owner       *string `json:"owner,omitempty"`
	Parent      *string `json:"parent,omitempty"`
	Milestone   *string `json:"milestone,omitempty"`
}

// TicketMembers names members of a ticket's two sets: assignees as
// actors, labels by their ids.
type TicketMembers struct {
	Assignees []string `json:"assignees,omitempty"`
	Labels    []string `json:"labels,omitempty"`
}

// TicketEdit is what ticket.edit takes: the fields it sets and the
// members it adds and removes, in one operation.
type TicketEdit struct {
	Set    TicketSet     `json:"set,omitzero"`
	Add    TicketMembers `json:"add,omitzero"`
	Remove TicketMembers `json:"remove,omitzero"`
}

// ticketCreated is a ticket's create row's value: what never changes
// (the field rules' immutable and allocation classes). Every other field
// is a set row of its own after it, and every member an add row, so each
// merges by its own class and each row meets its bound by its field's (a
// description at its bound would not fit one row beside the rest).
type ticketCreated struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Number    int64     `json:"number"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy"`
}

// ticketDefaults is a ticket's scalar fields that its create and its
// edit set, in the order an operation records them, with the values the
// schema gives a new row before its create's set rows; ticketScalars
// their columns. closed_at follows the status. The acceptance columns
// are AcceptTicket's and UnacceptTicket's (acceptance.go), and source and
// the four columns IN1a reserves are written by nothing here.
var (
	ticketDefaults = []fieldValue{{"title", ""}, {"description", ""}, {"acceptance", ""}, {"status", TicketBacklog}, {"priority", "0"},
		{"horizon", ""}, {"owner", ""}, {"parent_id", ""}, {"milestone_id", ""}, {"closed_at", ""}}
	ticketScalars = columns(ticketDefaults)
)

// CreateTicket makes a ticket (ticket.create) in the project with the
// key: every field checked by its rule (model.go), the references
// checked inside the transaction (a parent, milestone or label missing or
// deleted is not_found, one of another project invalid, a parent that
// would make a chain past MaxParentDepth a cycle), at most MaxAssignees
// assignees and MaxTicketLabels labels (bound), a member named twice
// invalid; its number allocated from the project's next_number in this
// transaction, so numbers go in order and never twice, its key
// <project>-<number>. A status of done or cancelled sets closed_at; done
// is refused close_policy unless the project's close policy requires
// nothing, since a new ticket has no acceptance and no link. One create
// row records what never changes, then one set row per field the
// schema's default does not already hold, then one add row per member,
// then a person's create row for each name it gives (its owner, a
// person: assignee) that the directory lacks (knowPeople). An archived
// project refuses it (archived). The receipt carries its id and key.
func (s *Store) CreateTicket(ctx context.Context, op Op, n NewTicket) (Receipt, error) {
	return s.Apply(ctx, op, "ticket.create", n, func(t *tx) error {
		p, err := t.project(n.Project)
		if err != nil {
			return err
		}
		if !p.ArchivedAt.IsZero() {
			return refuse(RefusalArchived, "%s is archived", p.Key)
		}
		status := n.Status
		if status == "" {
			status = TicketBacklog
		}
		after := fieldValues(ticketDefaults)
		for _, f := range []struct{ key, label, column, value string }{
			{"ticket.title", "the title", "title", n.Title},
			{"ticket.description", "the description", "description", n.Description},
			{"ticket.acceptance", "the acceptance criteria", "acceptance", n.Acceptance},
		} {
			if err := setText(after, f.key, f.label, f.column, f.value); err != nil {
				return err
			}
		}
		if err := setVocabulary(after, status, n.Priority, n.Horizon, n.Owner); err != nil {
			return err
		}
		if err := checkMembers(TicketMembers{Assignees: n.Assignees, Labels: n.Labels}, TicketMembers{}); err != nil {
			return err
		}
		if err := checkCount("assignees", len(n.Assignees), MaxAssignees); err != nil {
			return err
		}
		if err := checkCount("labels", len(n.Labels), MaxTicketLabels); err != nil {
			return err
		}
		// The references, inside the transaction.
		for _, id := range n.Labels {
			if err := t.reference("labels", "label", p, id); err != nil {
				return err
			}
		}
		if n.Milestone != "" {
			if err := t.reference("milestones", "milestone", p, n.Milestone); err != nil {
				return err
			}
			setField(after, "milestone_id", n.Milestone)
		}
		if n.Parent != "" {
			parent, err := t.parent(p, n.Parent, Ticket{})
			if err != nil {
				return err
			}
			setField(after, "parent_id", parent)
		}
		if closed(status) {
			setField(after, "closed_at", stamp(t.now))
		}
		// Made done, it meets its project's close policy as made: no
		// acceptance and no link yet, so only a policy that requires
		// nothing lets it.
		if status == TicketDone {
			if st := closeState(p.CloseRequires, Ticket{Owner: n.Owner, Acceptance: n.Acceptance}, false); len(st.Missing) > 0 {
				return policyRefusal("a new ticket", st)
			}
		}
		number, err := allocateNumber(t, p.ID)
		if err != nil {
			return err
		}
		id, key := newID(), TicketKey(p.Key, number)
		by := t.op.author()
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO tickets (id, project_id, number, key, title, created_at, created_by, updated_at) VALUES (?, ?, ?, ?, '', ?, ?, ?)",
			id, p.ID, number, key, stamp(t.now), by, stamp(t.now)); err != nil {
			return err
		}
		t.created(id, key)
		value, err := encodeValue(ticketCreated{ID: id, ProjectID: p.ID, Number: number, Key: key, CreatedAt: t.now, CreatedBy: by})
		if err != nil {
			return err
		}
		base := ChangeRow{Entity: EntityTicket, EntityID: id, TicketID: id, ProjectID: p.ID}
		create := base
		create.Op, create.Value = OpCreate, value
		if err := t.record(create); err != nil {
			return err
		}
		if err := t.setScalars("tickets", base, ticketDefaults, after); err != nil {
			return err
		}
		assignees, err := t.members(base, assigneeSet, n.Assignees, nil)
		if err != nil {
			return err
		}
		if _, err := t.members(base, labelSet, n.Labels, nil); err != nil {
			return err
		}
		return t.knowPeople(n.Owner, assignees)
	})
}

// EditTicket edits the live ticket with the key (ticket.edit): it sets
// any of the title, description, acceptance criteria, status, priority,
// horizon, owner, parent and milestone, and adds and removes assignees
// and labels, in one operation, each field checked as CreateTicket checks
// it and the references after the revision check. One row per field that
// changes, its prior the value it replaces; a status moving to done or
// cancelled sets closed_at and one moving out of them clears it. A move
// to done is entered only when the project's close policy is met by the
// ticket as the edit leaves it (an owner or criteria the same edit sets
// included): close_policy otherwise, the receipt's Missing naming what
// is lacking; any other move is allowed, and no move touches the
// acceptance. Members: removes, then adds, one row each; a member there
// already is not added again nor one absent removed; a remove's prior
// names the tags of the member's adds it removes (the observed-remove
// rule); an add that leaves its set past its bound is refused, a remove
// never, so a set past its bound (a merge may leave one) refuses an add
// and allows a remove, and an add list longer than the bound is refused
// before any of its members is looked up or written. A member both added
// and removed, or named twice, is invalid; an edit that changes nothing
// is refused invalid. A name the edit gives (a new owner, a person:
// assignee added) that the directory lacks is added to it, after the
// ticket's rows (knowPeople). A deleted ticket is not_found; an archived
// project refuses it (archived).
func (s *Store) EditTicket(ctx context.Context, op Op, key string, e TicketEdit) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
		TicketEdit
	}{key, e}
	return s.Apply(ctx, op, "ticket.edit", args, func(t *tx) error {
		tk, p, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		before, err := t.scalars("tickets", tk.ID, ticketScalars)
		if err != nil {
			return err
		}
		after := fieldValues(before)
		for _, f := range []struct {
			key, label, column string
			value              *string
		}{
			{"ticket.title", "the title", "title", e.Set.Title},
			{"ticket.description", "the description", "description", e.Set.Description},
			{"ticket.acceptance", "the acceptance criteria", "acceptance", e.Set.Acceptance},
		} {
			if f.value == nil {
				continue
			}
			if err := setText(after, f.key, f.label, f.column, *f.value); err != nil {
				return err
			}
		}
		status, priority, horizon, owner := tk.Status, tk.Priority, tk.Horizon, tk.Owner
		if e.Set.Status != nil {
			status = *e.Set.Status
		}
		if e.Set.Priority != nil {
			priority = *e.Set.Priority
		}
		if e.Set.Horizon != nil {
			horizon = *e.Set.Horizon
		}
		if e.Set.Owner != nil {
			owner = *e.Set.Owner
		}
		if e.Set.Status != nil || e.Set.Priority != nil || e.Set.Horizon != nil || e.Set.Owner != nil {
			if err := setVocabulary(after, status, priority, horizon, owner); err != nil {
				return err
			}
		}
		if err := checkMembers(e.Add, e.Remove); err != nil {
			return err
		}
		// An add list longer than its set's bound leaves the set past it,
		// whatever the set holds (the members it adds are distinct, and
		// each is a live member after the operation), so it is refused
		// before any member is looked up or written, as a create's is.
		if err := checkCount("assignees", len(e.Add.Assignees), MaxAssignees); err != nil {
			return err
		}
		if err := checkCount("labels", len(e.Add.Labels), MaxTicketLabels); err != nil {
			return err
		}
		// The references, after the revision check (ticket).
		for _, id := range e.Add.Labels {
			if err := t.reference("labels", "label", p, id); err != nil {
				return err
			}
		}
		if e.Set.Milestone != nil {
			if *e.Set.Milestone != "" {
				if err := t.reference("milestones", "milestone", p, *e.Set.Milestone); err != nil {
					return err
				}
			}
			setField(after, "milestone_id", *e.Set.Milestone)
		}
		if e.Set.Parent != nil {
			parent := ""
			if *e.Set.Parent != "" {
				if parent, err = t.parent(p, *e.Set.Parent, tk); err != nil {
					return err
				}
			}
			setField(after, "parent_id", parent)
		}
		if status != tk.Status {
			if closed(status) {
				setField(after, "closed_at", stamp(t.now))
			} else {
				setField(after, "closed_at", "")
			}
		}
		// The gate: done is entered only when the policy is met by the
		// ticket as this edit leaves it.
		if status != tk.Status && status == TicketDone {
			draft := tk
			draft.Owner, draft.Acceptance = valueOf(after, "owner"), valueOf(after, "acceptance")
			st, err := closeOf(t.ctx, t.tx, p, draft)
			if err != nil {
				return err
			}
			if len(st.Missing) > 0 {
				return policyRefusal(tk.Key, st)
			}
		}
		base := ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID}
		if err := t.setScalars("tickets", base, before, after); err != nil {
			return err
		}
		assignees, err := t.members(base, assigneeSet, e.Add.Assignees, e.Remove.Assignees)
		if err != nil {
			return err
		}
		if _, err := t.members(base, labelSet, e.Add.Labels, e.Remove.Labels); err != nil {
			return err
		}
		// The names this edit gives: the owner when it changes it, the
		// assignees it adds. An edit that changes nothing gives none, and
		// is refused as such.
		newOwner := ""
		if o := valueOf(after, "owner"); o != tk.Owner {
			newOwner = o
		}
		return t.knowPeople(newOwner, assignees)
	})
}

// DeleteTicket deletes the live ticket with the key (ticket.delete): a
// tombstone, its own row's deleted_at set and nothing else written, one
// change row, its own rev bumped. Its children keep their parent_id and
// its relations and run links stay, inactive while it is deleted, and
// its key is kept and never reassigned; so its undelete restores it whole
// by the reverse of that one row. A deleted ticket is not_found; an
// archived project refuses it (archived).
func (s *Store) DeleteTicket(ctx context.Context, op Op, key string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
	}{key}
	return s.Apply(ctx, op, "ticket.delete", args, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		return t.setScalars("tickets", ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID},
			[]fieldValue{{"deleted_at", ""}}, []fieldValue{{"deleted_at", stamp(t.now)}})
	})
}

// UndeleteTicket restores the deleted ticket with the key
// (ticket.undelete) by the reverse of its delete's one row: deleted_at
// cleared, its prior the stamp it clears, so every reference to it is
// active again. The parent and blocks walks take tombstones as edges, so
// an undelete never makes a cycle or a chain past MaxParentDepth. One
// reference may not come back: a run link of its naming a run another
// live ticket has linked meanwhile (a tombstone holds no run) is removed
// in the same operation, one remove row naming its tags, so a run
// belongs to one live ticket at most. A live ticket is invalid; an
// archived project refuses it (archived).
func (s *Store) UndeleteTicket(ctx context.Context, op Op, key string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
	}{key}
	return s.Apply(ctx, op, "ticket.undelete", args, func(t *tx) error {
		tk, _, err := t.ticket(key, true)
		if err != nil {
			return err
		}
		before, err := t.scalars("tickets", tk.ID, []string{"deleted_at"})
		if err != nil {
			return err
		}
		if before[0].value == "" {
			return refuse(RefusalInvalid, "%s is not deleted", tk.Key)
		}
		if err := t.setScalars("tickets", ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID},
			before, []fieldValue{{"deleted_at", ""}}); err != nil {
			return err
		}
		return t.releaseRuns(tk)
	})
}

// ticket reads the ticket with the key inside the operation and its
// project, in the order the store's rules ask: not_found when there is
// none, or when it is deleted and deleted is false (every write on a
// deleted ticket but its undelete); then the revision expected, the
// ticket's; then archived when its project is.
func (t *tx) ticket(key string, deleted bool) (Ticket, Project, error) {
	tk, err := t.ticketRow(key, deleted)
	if err != nil {
		return Ticket{}, Project{}, err
	}
	if err := t.expect(tk.Rev); err != nil {
		return Ticket{}, Project{}, err
	}
	p, err := t.open(tk)
	if err != nil {
		return Ticket{}, Project{}, err
	}
	return tk, p, nil
}

// ticketRow reads the ticket with the key inside the operation: not_found
// when there is none, or when it is deleted and deleted is false. An
// operation on a row of the ticket's own (a comment's edit) reads the
// ticket so, then its row, whose revision it expects, then the project
// (open).
func (t *tx) ticketRow(key string, deleted bool) (Ticket, error) {
	tk, err := ticketByKey(t.ctx, t.tx, key)
	if isNoRows(err) {
		return Ticket{}, refuse(RefusalNotFound, "no ticket %s", key)
	}
	if err != nil {
		return Ticket{}, err
	}
	if !deleted && !tk.DeletedAt.IsZero() {
		return Ticket{}, refuse(RefusalNotFound, "%s is deleted", tk.Key)
	}
	return tk, nil
}

// open reads the ticket's project inside the operation: archived when it
// is, since an archived project refuses every write under it.
func (t *tx) open(tk Ticket) (Project, error) {
	p, err := projectByID(t.ctx, t.tx, tk.ProjectID)
	if err != nil {
		return Project{}, err
	}
	if !p.ArchivedAt.IsZero() {
		return Project{}, refuse(RefusalArchived, "%s is archived", p.Key)
	}
	return p, nil
}

// setText checks a ticket's text field by its rule and sets it among the
// values an operation will write.
func setText(after []fieldValue, key, label, column, value string) error {
	if err := checkField(key, label, value); err != nil {
		return err
	}
	setField(after, column, value)
	return nil
}

// setVocabulary checks a ticket's status, priority, horizon and owner
// and sets them among the values an operation will write.
func setVocabulary(after []fieldValue, status string, priority int, horizon, owner string) error {
	if err := checkStatus(status); err != nil {
		return err
	}
	if err := checkPriority(priority); err != nil {
		return err
	}
	if err := checkHorizon(horizon); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	setField(after, "status", status)
	setField(after, "priority", strconv.Itoa(priority))
	setField(after, "horizon", horizon)
	setField(after, "owner", owner)
	return nil
}

// checkMembers checks the members an operation adds and removes: each
// assignee's shape, no member named twice in one list, none both added
// and removed.
func checkMembers(add, remove TicketMembers) error {
	for _, set := range []struct {
		what        string
		add, remove []string
	}{
		{"assignee", add.Assignees, remove.Assignees},
		{"label", add.Labels, remove.Labels},
	} {
		seen := map[string]bool{}
		for _, m := range set.add {
			if seen[m] {
				return refuse(RefusalInvalid, "the %s %q is added twice", set.what, m)
			}
			seen[m] = true
		}
		removed := map[string]bool{}
		for _, m := range set.remove {
			if removed[m] {
				return refuse(RefusalInvalid, "the %s %q is removed twice", set.what, m)
			}
			if seen[m] {
				return refuse(RefusalInvalid, "the %s %q is both added and removed", set.what, m)
			}
			removed[m] = true
		}
	}
	for _, a := range add.Assignees {
		if err := checkAssignee(a); err != nil {
			return err
		}
	}
	return nil
}

// reference checks that id names a live row of the table (labels or
// milestones) in the ticket's project: a row missing or deleted is
// not_found, a live row of another project invalid. table is the code's.
func (t *tx) reference(table, what string, p Project, id string) error {
	var project, deleted string
	err := t.tx.QueryRowContext(t.ctx, "SELECT project_id, deleted_at FROM "+table+" WHERE id = ?", id).Scan(&project, &deleted)
	switch {
	case isNoRows(err):
		return refuse(RefusalNotFound, "no %s %s", what, id)
	case err != nil:
		return err
	case deleted != "":
		return refuse(RefusalNotFound, "the %s %s is deleted", what, id)
	case project != p.ID:
		return refuse(RefusalInvalid, "the %s %s is not of %s", what, id, p.Key)
	}
	return nil
}

// parent checks the ticket with the key as the parent of moved (the zero
// Ticket for one being created) in the project p, and gives its id: live
// (not_found when missing or deleted), of p (invalid), and no cycle and
// no chain past MaxParentDepth (cycle), checked by walking up the
// parent's chain and down moved's subtree under the transaction, the
// tombstones taken as edges (a deleted ticket keeps its parent_id and its
// children theirs), so an undelete never makes either.
func (t *tx) parent(p Project, key string, moved Ticket) (string, error) {
	par, err := ticketByKey(t.ctx, t.tx, key)
	switch {
	case isNoRows(err):
		return "", refuse(RefusalNotFound, "no ticket %s", key)
	case err != nil:
		return "", err
	case !par.DeletedAt.IsZero():
		return "", refuse(RefusalNotFound, "the parent %s is deleted", par.Key)
	case par.ProjectID != p.ID:
		return "", refuse(RefusalInvalid, "the parent %s is not of %s", par.Key, p.Key)
	case par.ID == moved.ID:
		return "", refuse(RefusalCycle, "%s cannot be its own parent", par.Key)
	}
	chain, err := t.chainUp(par.ID)
	if err != nil {
		return "", err
	}
	if moved.ID != "" && slices.Contains(chain, moved.ID) {
		return "", refuse(RefusalCycle, "%s is under %s: the parent would make a cycle", par.Key, moved.Key)
	}
	height := 1
	if moved.ID != "" {
		if height, err = t.subtreeHeight(moved.ID); err != nil {
			return "", err
		}
	}
	if depth := len(chain) + height; depth > MaxParentDepth {
		return "", refuse(RefusalCycle, "the parent %s would make a chain of %d tickets, more than %d", par.Key, depth, MaxParentDepth)
	}
	return par.ID, nil
}

// chainUp is the chain from the ticket with the id up to its root, the
// ticket first, tombstones included: a recursive walk under the
// transaction, cut after MaxParentDepth+1 tickets, which is past every
// bound a chain is checked against (a chain cut there is refused anyway,
// and a cycle a merge left among them cannot loop it).
func (t *tx) chainUp(id string) ([]string, error) {
	rows, err := t.tx.QueryContext(t.ctx, `WITH RECURSIVE up(id, parent_id, n) AS (
			SELECT id, parent_id, 1 FROM tickets WHERE id = ?
			UNION ALL
			SELECT tickets.id, tickets.parent_id, up.n + 1 FROM tickets JOIN up ON tickets.id = up.parent_id WHERE up.n <= ?
		)
		SELECT id FROM up ORDER BY n`, id, MaxParentDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chain []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		chain = append(chain, c)
	}
	return chain, rows.Err()
}

// subtreeHeight is how many tickets the longest chain down from the
// ticket with the id holds, it included, tombstones included: a
// recursive walk under the transaction, cut after MaxParentDepth+1
// levels, as chainUp is.
func (t *tx) subtreeHeight(id string) (int, error) {
	var height int
	err := t.tx.QueryRowContext(t.ctx, `WITH RECURSIVE down(id, h) AS (
			SELECT ?, 1
			UNION ALL
			SELECT tickets.id, down.h + 1 FROM tickets JOIN down ON tickets.parent_id = down.id WHERE down.h <= ?
		)
		SELECT MAX(h) FROM down`, id, MaxParentDepth).Scan(&height)
	return height, err
}

// memberSet is one of a ticket's two sets: its field in the change rows,
// its table and member column, its bound, how its live members are
// counted (a label's membership counts while its label is live: one a
// merge left naming a tombstone reads as absent) and how one is added.
type memberSet struct {
	field, table, column string
	max                  int
	count                string
	add                  func(t *tx, ticketID, member string) error
}

var (
	assigneeSet = memberSet{
		field: "assignees", table: "ticket_assignees", column: "actor", max: MaxAssignees,
		count: "SELECT COUNT(*) FROM ticket_assignees WHERE ticket_id = ?",
		add: func(t *tx, ticketID, member string) error {
			_, err := t.tx.ExecContext(t.ctx, "INSERT INTO ticket_assignees (ticket_id, actor, added_at) VALUES (?, ?, ?)", ticketID, member, stamp(t.now))
			return err
		},
	}
	labelSet = memberSet{
		field: "labels", table: "ticket_labels", column: "label_id", max: MaxTicketLabels,
		count: "SELECT COUNT(*) FROM ticket_labels JOIN labels ON labels.id = ticket_labels.label_id WHERE ticket_labels.ticket_id = ? AND labels.deleted_at = ''",
		add: func(t *tx, ticketID, member string) error {
			_, err := t.tx.ExecContext(t.ctx, "INSERT INTO ticket_labels (ticket_id, label_id) VALUES (?, ?)", ticketID, member)
			return err
		},
	}
)

// members removes and then adds members of one set of the ticket base
// names, one row each (a remove's prior the tags of the member's adds it
// removes, in more rows when they are more than one prior holds, its
// value the member; an add's value the member, its row's identity its
// tag), and gives the members it added; a member there already is not
// added again, nor one absent removed. An operation that adds leaves the
// set at its bound at most (bound), however far past it the set stood
// before; one that only removes is never refused.
func (t *tx) members(base ChangeRow, set memberSet, add, remove []string) ([]string, error) {
	ticketID := base.EntityID
	for _, m := range remove {
		there, err := t.isMember(set, ticketID, m)
		if err != nil {
			return nil, err
		}
		if !there {
			continue
		}
		tags, err := t.liveTags(ticketID, set.field, m)
		if err != nil {
			return nil, err
		}
		if _, err := t.tx.ExecContext(t.ctx, "DELETE FROM "+set.table+" WHERE ticket_id = ? AND "+set.column+" = ?", ticketID, m); err != nil {
			return nil, err
		}
		row := base
		row.Field, row.Value = set.field, m
		if err := t.recordRemove(row, tags); err != nil {
			return nil, err
		}
	}
	var added []string
	for _, m := range add {
		there, err := t.isMember(set, ticketID, m)
		if err != nil {
			return nil, err
		}
		if there {
			continue
		}
		if err := set.add(t, ticketID, m); err != nil {
			return nil, err
		}
		row := base
		row.Field, row.Op, row.Value = set.field, OpAdd, m
		if err := t.record(row); err != nil {
			return nil, err
		}
		added = append(added, m)
	}
	if len(added) == 0 {
		return nil, nil
	}
	var n int
	if err := t.tx.QueryRowContext(t.ctx, set.count, ticketID).Scan(&n); err != nil {
		return nil, err
	}
	return added, checkCount(set.field, n, set.max)
}

// isMember reports whether the ticket's set holds the member.
func (t *tx) isMember(set memberSet, ticketID, member string) (bool, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM "+set.table+" WHERE ticket_id = ? AND "+set.column+" = ?", ticketID, member).Scan(&n)
	return n > 0, err
}

// liveTags is the tags of the adds of a member of the ticket's set that
// no remove has named yet, in the log's order: what a remove of the
// member names in its prior.
func (t *tx) liveTags(ticketID, field, member string) ([]Tag, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT op, prior, actor, op_id, n FROM changes
		WHERE entity = ? AND entity_id = ? AND field = ? AND value = ? AND op IN (?, ?) ORDER BY at, actor, op_id, n`,
		EntityTicket, ticketID, field, member, OpAdd, OpRemove)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var adds []Tag
	removed := map[Tag]bool{}
	for rows.Next() {
		var op, prior string
		var tag Tag
		if err := rows.Scan(&op, &prior, &tag.Actor, &tag.OpID, &tag.N); err != nil {
			return nil, err
		}
		if op == OpAdd {
			adds = append(adds, tag)
			continue
		}
		for _, r := range Tags(prior) {
			removed[r] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	live := []Tag{}
	for _, a := range adds {
		if !removed[a] {
			live = append(live, a)
		}
	}
	return live, nil
}

// ticketColumns is a ticket's row as scanTicket reads it.
const ticketColumns = `id, project_id, number, key, title, description, acceptance, status, priority, horizon, owner, parent_id, milestone_id,
	accepted_at, accepted_by, accepted_for, accepted_criteria, accepted_note, source, external_id, external_url, external_snapshot, authority,
	created_at, created_by, updated_at, closed_at, deleted_at, rev`

// scanTicket reads a ticket's row, its sets not among it.
func scanTicket(row interface{ Scan(...any) error }) (Ticket, error) {
	var tk Ticket
	var accepted, created, updated, closedAt, deleted string
	if err := row.Scan(&tk.ID, &tk.ProjectID, &tk.Number, &tk.Key, &tk.Title, &tk.Description, &tk.Acceptance, &tk.Status, &tk.Priority,
		&tk.Horizon, &tk.Owner, &tk.ParentID, &tk.MilestoneID, &accepted, &tk.AcceptedBy, &tk.AcceptedFor, &tk.AcceptedCriteria,
		&tk.AcceptedNote, &tk.Source, &tk.ExternalID, &tk.ExternalURL, &tk.ExternalSnapshot, &tk.Authority, &created, &tk.CreatedBy,
		&updated, &closedAt, &deleted, &tk.Rev); err != nil {
		return Ticket{}, err
	}
	tk.AcceptedAt, tk.CreatedAt, tk.UpdatedAt, tk.ClosedAt, tk.DeletedAt = unstamp(accepted), unstamp(created), unstamp(updated), unstamp(closedAt), unstamp(deleted)
	return tk, nil
}

// ticketByKey reads the ticket with the key, deleted or not; sql.ErrNoRows
// when there is none.
func ticketByKey(ctx context.Context, q queryer, key string) (Ticket, error) {
	return scanTicket(q.QueryRowContext(ctx, "SELECT "+ticketColumns+" FROM tickets WHERE key = ?", key))
}

// ticketByID reads the ticket with the id, deleted or not; sql.ErrNoRows
// when there is none.
func ticketByID(ctx context.Context, q queryer, id string) (Ticket, error) {
	return scanTicket(q.QueryRowContext(ctx, "SELECT "+ticketColumns+" FROM tickets WHERE id = ?", id))
}

// addOf is where the add that made a row of a set stands in the log: its
// at as stamped, and its tag, which a remove of the row's member names.
type addOf struct {
	at  string
	tag Tag
}

// addsOf reads the adds that made the given rows of a ticket's set field
// (relations, links: a set whose rows have ids of their own), by the
// row's id, which the add's value carries. A row with no add in the log
// (one written by hand, never by an operation) is not among them.
func addsOf(ctx context.Context, q queryer, ticketID, field string, ids []string) (map[string]addOf, error) {
	out := map[string]addOf{}
	if len(ids) == 0 {
		return out, nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	rows, err := q.QueryContext(ctx, "SELECT value, at, actor, op_id, n FROM changes WHERE entity = ? AND entity_id = ? AND field = ? AND op = ?",
		EntityTicket, ticketID, field, OpAdd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		var a addOf
		if err := rows.Scan(&value, &a.at, &a.tag.Actor, &a.tag.OpID, &a.tag.N); err != nil {
			return nil, err
		}
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(value), &v) == nil && want[v.ID] {
			out[v.ID] = a
		}
	}
	return out, rows.Err()
}

// foldKey is where a set row stands among its member's rows: the log's
// one order of the adds that made them (at, actor, op_id, n), a row with
// no add standing at its created_at before any add of the same moment,
// and the row's id breaking the last tie.
type foldKey struct {
	at, actor, opID string
	n               int
	id              string
}

// keyOf is the fold key of the row with the id, made at createdAt.
func keyOf(id, createdAt string, adds map[string]addOf) foldKey {
	if a, ok := adds[id]; ok {
		return foldKey{a.at, a.tag.Actor, a.tag.OpID, a.tag.N, id}
	}
	return foldKey{at: createdAt, id: id}
}

// after reports whether k stands after o.
func (k foldKey) after(o foldKey) bool {
	switch {
	case k.at != o.at:
		return k.at > o.at
	case k.actor != o.actor:
		return k.actor > o.actor
	case k.opID != o.opID:
		return k.opID > o.opID
	case k.n != o.n:
		return k.n > o.n
	}
	return k.id > o.id
}

// rowTags is the tags of the adds that made the rows with the ids, in the
// log's order: what a remove of their member names in its prior. A row
// with no add names none.
func rowTags(ids []string, adds map[string]addOf) []Tag {
	var keys []foldKey
	for _, id := range ids {
		if a, ok := adds[id]; ok {
			keys = append(keys, foldKey{a.at, a.tag.Actor, a.tag.OpID, a.tag.N, id})
		}
	}
	slices.SortFunc(keys, func(a, b foldKey) int {
		if a.after(b) {
			return 1
		}
		if b.after(a) {
			return -1
		}
		return 0
	})
	tags := []Tag{}
	for _, k := range keys {
		tags = append(tags, Tag{k.actor, k.opID, k.n})
	}
	return tags
}

// readTicket reads the ticket with the key as a read shows it, deleted or
// not: its row; its assignees, by actor; its live labels, by name, a
// membership whose label is a tombstone reading as absent; and its
// milestone as none when the milestone is a tombstone. A merge may leave
// either (the field rules: the label's delete or the milestone's did not
// see the add or the set from another replica), and the rows are kept and
// read, never undone. ErrNotFound when there is none. q is a read
// transaction or an operation's, so the parts describe one state.
func readTicket(ctx context.Context, q queryer, key string) (Ticket, error) {
	tk, err := ticketByKey(ctx, q, key)
	if isNoRows(err) {
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, err
	}
	if tk.Assignees, err = readTexts(ctx, q, "SELECT actor FROM ticket_assignees WHERE ticket_id = ? ORDER BY actor", tk.ID); err != nil {
		return Ticket{}, err
	}
	if tk.Labels, err = readTexts(ctx, q, `SELECT labels.id FROM ticket_labels JOIN labels ON labels.id = ticket_labels.label_id
		WHERE ticket_labels.ticket_id = ? AND labels.deleted_at = '' ORDER BY labels.name, labels.id`, tk.ID); err != nil {
		return Ticket{}, err
	}
	if tk.MilestoneID != "" {
		var live int
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM milestones WHERE id = ? AND deleted_at = ''", tk.MilestoneID).Scan(&live); err != nil {
			return Ticket{}, err
		}
		if live == 0 {
			tk.MilestoneID = ""
		}
	}
	return tk, nil
}

// readTexts reads the one text column of the rows a query gives; none is
// an empty list.
func readTexts(ctx context.Context, q queryer, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// TicketView is a ticket as its page reads it, from one read transaction
// (Ticket): its row and its two sets; its parent's key, title and status,
// and DeletedAt when the parent is a tombstone (the reference then
// inactive); its first live children by number and how many it has; its
// first relations both ways, the active ones first, each with its other
// end, and how many it has (RelationsTotal); whether a live, open ticket
// blocks it; its live links and attachments; its last comments, each
// with its number; its close policy's state, computed now; its approved
// plan revisions' summaries, newest first (none, one on a local write,
// more when a merge left more: the field rules), at most
// MaxTicketApproved, each with its staleness; and how many launches it
// has. The revisions and the launches are reads of their own (Plans), so
// the read keeps its bound.
type TicketView struct {
	Ticket
	Parent         *RelatedTicket   `json:"parent,omitempty"`
	Children       TicketChildren   `json:"children"`
	Relations      []TicketRelation `json:"relations"`
	RelationsTotal int              `json:"relationsTotal"`
	Blocked        bool             `json:"blocked"`
	Links          []Link           `json:"links"`
	Attachments    []Attachment     `json:"attachments"`
	Comments       []Comment        `json:"comments"`
	Close          CloseState       `json:"close"`
	Approved       []PlanSummary    `json:"approved"`
	LaunchCount    int              `json:"launchCount"`
}

// TicketChildren is the first MaxTicketChildren live children of a
// ticket by number, and how many live children it has (Total): the rest
// are listed by Children.
type TicketChildren struct {
	Tickets []RelatedTicket `json:"tickets"`
	Total   int             `json:"total"`
}

// Ticket reads the ticket with the key, deleted or not, as its page shows
// it (TicketView), every part in one read transaction, so the reply
// describes one state, its rev and its rows agreeing, whatever commits
// between its statements from this process or from a second server on
// the file. A deleted ticket's own children are not read from it: a
// reference to a tombstone is inactive, and its children keep their
// parent (Children finds them). The last MaxTicketComments comments come
// with it, tombstones among them so the numbers have no gap; the earlier
// ones are paged by Comments. The first MaxTicketRelations relations come
// with it, the active ones first, and how many it has: relations to
// deleted tickets count for nothing toward MaxRelations, so a ticket may
// hold any number, and Relations reads them all. ErrNotFound when there
// is no such ticket.
func (s *Store) Ticket(ctx context.Context, key string) (TicketView, error) {
	var v TicketView
	err := s.read(ctx, func(q queryer) error {
		tk, err := readTicket(ctx, q, key)
		if err != nil {
			return err
		}
		if s.betweenReads != nil {
			s.betweenReads()
		}
		v = TicketView{Ticket: tk}
		if tk.ParentID != "" {
			par, err := ticketByID(ctx, q, tk.ParentID)
			switch {
			case err == nil:
				v.Parent = &RelatedTicket{ID: par.ID, Key: par.Key, Title: par.Title, Status: par.Status, DeletedAt: par.DeletedAt}
			case !isNoRows(err):
				return err
			}
		}
		if v.Children, err = childrenOf(ctx, q, tk); err != nil {
			return err
		}
		rels, err := relationsOf(ctx, q, tk.ID)
		if err != nil {
			return err
		}
		v.Relations, v.RelationsTotal = firstRelations(rels, MaxTicketRelations), len(rels)
		if v.Blocked, err = blockedOf(ctx, q, tk.ID); err != nil {
			return err
		}
		if v.Links, err = linksOf(ctx, q, tk.ID); err != nil {
			return err
		}
		if v.Attachments, err = attachmentsOf(ctx, q, tk.ID); err != nil {
			return err
		}
		if v.Comments, err = lastComments(ctx, q, tk.ID, MaxTicketComments); err != nil {
			return err
		}
		p, err := projectByID(ctx, q, tk.ProjectID)
		if err != nil {
			return err
		}
		if v.Close, err = closeOf(ctx, q, p, tk); err != nil {
			return err
		}
		if v.Approved, err = plansOf(ctx, q, tk, true); err != nil {
			return err
		}
		if schemaOf(q) < 2 {
			// A version 1 database, read as it was found: no launches.
			return nil
		}
		return q.QueryRowContext(ctx, "SELECT COUNT(*) FROM launches WHERE ticket_id = ?", tk.ID).Scan(&v.LaunchCount)
	})
	if err != nil {
		return TicketView{}, err
	}
	return v, nil
}

// childrenOf reads the first MaxTicketChildren live children of the
// ticket by number and how many it has; none for a tombstone, whose
// children's references are inactive while it is deleted.
func childrenOf(ctx context.Context, q queryer, tk Ticket) (TicketChildren, error) {
	out := TicketChildren{Tickets: []RelatedTicket{}}
	if !tk.DeletedAt.IsZero() {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT id, key, title, status FROM tickets WHERE parent_id = ? AND deleted_at = '' ORDER BY number LIMIT ?",
		tk.ID, MaxTicketChildren)
	if err != nil {
		return TicketChildren{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c RelatedTicket
		if err := rows.Scan(&c.ID, &c.Key, &c.Title, &c.Status); err != nil {
			return TicketChildren{}, err
		}
		out.Tickets = append(out.Tickets, c)
	}
	if err := rows.Err(); err != nil {
		return TicketChildren{}, err
	}
	rows.Close()
	err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM tickets WHERE parent_id = ? AND deleted_at = ''", tk.ID).Scan(&out.Total)
	return out, err
}

// firstRelations is the first n of a ticket's relations as its read
// carries them: the active ones, whose other end is live, before those
// whose other end is deleted, each in the order they come (Relations).
func firstRelations(all []TicketRelation, n int) []TicketRelation {
	out := make([]TicketRelation, 0, min(len(all), n))
	for _, active := range []bool{true, false} {
		for _, tr := range all {
			if len(out) == n {
				return out
			}
			if tr.Other.DeletedAt.IsZero() == active {
				out = append(out, tr)
			}
		}
	}
	return out
}

// lastComments reads the last n comments of the ticket with the id, in
// their order, each with its number, tombstones among them: the ticket's
// comments numbered without their bodies, and only the last n read whole
// (commentsNumbered; BenchmarkTicketRead).
func lastComments(ctx context.Context, q queryer, ticketID string, n int) ([]Comment, error) {
	rows, err := q.QueryContext(ctx, commentsNumbered("SELECT id, n FROM numbered ORDER BY n DESC LIMIT ?"), ticketID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HistoryPage is a page of a ticket's history: its changes in the order
// they were made (by seq), and Next, the seq to read after for the next
// page, 0 on the last.
type HistoryPage struct {
	Changes []ChangeRow `json:"changes"`
	Next    int64       `json:"next,omitempty"`
}

// History reads a page of the history of the ticket with the key, deleted
// or not, as the store recorded it: the change rows of the ticket and of
// its comments after the seq after (0 for the first page), at most limit
// (DefaultHistoryPage when 0, at most MaxHistoryPage) and no more text
// than maxHistoryText, so a page of descriptions at their bound stays a
// page. ErrBadQuery for a limit or an after out of range; ErrNotFound
// when there is no such ticket.
func (s *Store) History(ctx context.Context, key string, after int64, limit int) (HistoryPage, error) {
	if limit == 0 {
		limit = DefaultHistoryPage
	}
	if limit < 1 || limit > MaxHistoryPage || after < 0 {
		return HistoryPage{}, badQuery("a page of history is 1 to %d changes after a seq of 0 or more, not %d after %d", MaxHistoryPage, limit, after)
	}
	var out HistoryPage
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT seq, entity, entity_id, ticket_id, project_id, field, op, value, prior, at, actor, by, op_id, n
			FROM changes WHERE ticket_id = ? AND seq > ? ORDER BY seq LIMIT ?`, tk.ID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = HistoryPage{Changes: []ChangeRow{}}
		text := 0
		for rows.Next() {
			var c ChangeRow
			var at string
			if err := rows.Scan(&c.Seq, &c.Entity, &c.EntityID, &c.TicketID, &c.ProjectID, &c.Field, &c.Op, &c.Value, &c.Prior,
				&at, &c.Actor, &c.By, &c.OpID, &c.N); err != nil {
				return err
			}
			c.At = unstamp(at)
			size := len(c.Value) + len(c.Prior)
			if len(out.Changes) == limit || (len(out.Changes) > 0 && text+size > maxHistoryText) {
				out.Next = out.Changes[len(out.Changes)-1].Seq
				break
			}
			out.Changes = append(out.Changes, c)
			text += size
		}
		return rows.Err()
	})
	if err != nil {
		return HistoryPage{}, err
	}
	return out, nil
}
