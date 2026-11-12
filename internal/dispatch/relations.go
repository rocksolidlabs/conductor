package dispatch

import (
	"context"
	"slices"
	"strings"
	"time"
)

// A relation is between two tickets of one project: From blocks To, or
// the two relate; "depends on" is blocks read from the other end. It is a
// member of its from ticket's relations set (the field rules' set class):
// its add a row of that ticket's, tagged by its identity, its remove one
// naming the tags it removes; and it counts for both its tickets, whose
// pages both show it, so an operation on it changes both. One live row
// per (from, to, kind) on a local write, a rule and not an index: two
// replicas' adds of one member are one member with two tags, the row of
// the later add standing for it, and a remove through that row's id
// removes both. blocks edges never form a cycle; a ticket is blocked
// while a live ticket blocks it whose status is neither done nor
// cancelled. A relation removed and added again is a new row with a new
// id.

// TicketRelation is a relation as a ticket's read shows it: the row that
// stands for its member, and the ticket at its other end.
type TicketRelation struct {
	Relation
	Other RelatedTicket `json:"other"`
}

// RelatedTicket is the ticket at a relation's other end: its key, title
// and status, and DeletedAt when it is a tombstone, the relation then
// inactive: kept, shown as such, counting for nothing, and active again
// on the ticket's undelete.
type RelatedTicket struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	DeletedAt time.Time `json:"deletedAt,omitzero"`
}

// AddRelation relates the live ticket with the key to the live ticket to
// (relation.add): kind blocks (the first blocks to) or relates (invalid
// otherwise). Both tickets live (not_found) and of one project (invalid);
// a ticket blocking itself is a cycle and relating to itself invalid;
// the member live already is invalid, and for relates the reverse too,
// since a relation of that kind reads the same from either end; a blocks
// edge that would close a cycle is refused cycle, the walk taking
// tombstoned tickets' relations as edges, so an undelete never makes one;
// at most MaxRelations active relations on either ticket (bound), one to
// a deleted ticket counting for nothing (relationCount). One add row on
// the first ticket, its value the relation whole; both tickets' revs are
// bumped. An expected revision is the first ticket's; an archived
// project refuses it (archived). The receipt carries the relation's id.
func (s *Store) AddRelation(ctx context.Context, op Op, key, kind, to string) (Receipt, error) {
	args := struct {
		Key  string `json:"key"`
		Kind string `json:"kind"`
		To   string `json:"to"`
	}{key, kind, to}
	return s.Apply(ctx, op, "relation.add", args, func(t *tx) error {
		from, p, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if !slices.Contains(RelationKinds, kind) {
			return refuse(RefusalInvalid, "the kind %q is none of %s", kind, strings.Join(RelationKinds, ", "))
		}
		other, err := ticketByKey(t.ctx, t.tx, to)
		switch {
		case isNoRows(err):
			return refuse(RefusalNotFound, "no ticket %s", to)
		case err != nil:
			return err
		case !other.DeletedAt.IsZero():
			return refuse(RefusalNotFound, "%s is deleted", other.Key)
		case other.ProjectID != from.ProjectID:
			return refuse(RefusalInvalid, "%s is not of %s", other.Key, p.Key)
		case other.ID == from.ID && kind == RelationBlocks:
			return refuse(RefusalCycle, "%s cannot block itself", from.Key)
		case other.ID == from.ID:
			return refuse(RefusalInvalid, "%s cannot relate to itself", from.Key)
		}
		held, err := t.relationHeld(from.ID, other.ID, kind)
		if err != nil {
			return err
		}
		if !held && kind == RelationRelates {
			if held, err = t.relationHeld(other.ID, from.ID, kind); err != nil {
				return err
			}
		}
		if held {
			return refuse(RefusalInvalid, "%s and %s are related so already", from.Key, other.Key)
		}
		if kind == RelationBlocks {
			cycle, err := t.blocksAlready(other.ID, from.ID)
			if err != nil {
				return err
			}
			if cycle {
				return refuse(RefusalCycle, "%s blocks %s already, directly or through others: %s blocking %s would make a cycle", other.Key, from.Key, from.Key, other.Key)
			}
		}
		for _, tk := range []Ticket{from, other} {
			n, err := t.relationCount(tk.ID)
			if err != nil {
				return err
			}
			if err := checkCount("relations on "+tk.Key, n+1, MaxRelations); err != nil {
				return err
			}
		}
		r := Relation{ID: newID(), FromID: from.ID, ToID: other.ID, Kind: kind, CreatedAt: t.now, CreatedBy: t.op.author()}
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO relations (id, from_id, to_id, kind, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)",
			r.ID, r.FromID, r.ToID, r.Kind, stamp(r.CreatedAt), r.CreatedBy); err != nil {
			return err
		}
		t.created(r.ID, "")
		value, err := encodeValue(r)
		if err != nil {
			return err
		}
		if err := t.record(ChangeRow{Entity: EntityTicket, EntityID: from.ID, TicketID: from.ID, ProjectID: from.ProjectID,
			Field: "relations", Op: OpAdd, Value: value}); err != nil {
			return err
		}
		return t.touch(EntityTicket, other.ID, other.ID, other.ProjectID)
	})
}

// RemoveRelation removes the live relation with the id from the live
// ticket with the key, at either of its ends (relation.remove): every
// live row of its member tombstoned (two, after a merge brought a second
// add), one remove row on its from ticket (more when a merge brought more
// adds than one row's prior holds), its value the row named whole and its
// prior the tags of the adds it removes, so the member is gone on every
// replica; both tickets' revs are bumped. The relation missing, removed
// or not the ticket's is not_found, and so is one whose other end is
// deleted (a write on a deleted ticket's relations; its undelete brings
// the relation back as it was). An expected revision is the ticket's
// with the key; an archived project refuses it (archived).
func (s *Store) RemoveRelation(ctx context.Context, op Op, key, id string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}{key, id}
	return s.Apply(ctx, op, "relation.remove", args, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		r, err := scanRelation(t.tx.QueryRowContext(t.ctx, "SELECT "+relationColumns+" FROM relations WHERE id = ?", id))
		switch {
		case isNoRows(err):
			return refuse(RefusalNotFound, "no relation %s", id)
		case err != nil:
			return err
		case r.FromID != tk.ID && r.ToID != tk.ID:
			return refuse(RefusalNotFound, "the relation %s is not %s's", id, tk.Key)
		case !r.DeletedAt.IsZero():
			return refuse(RefusalNotFound, "the relation %s is removed", id)
		}
		otherID := r.ToID
		if otherID == tk.ID {
			otherID = r.FromID
		}
		other, err := ticketByID(t.ctx, t.tx, otherID)
		if err != nil {
			return err
		}
		if !other.DeletedAt.IsZero() {
			return refuse(RefusalNotFound, "%s is deleted", other.Key)
		}
		ids, err := readTexts(t.ctx, t.tx, "SELECT id FROM relations WHERE from_id = ? AND to_id = ? AND kind = ? AND deleted_at = ''", r.FromID, r.ToID, r.Kind)
		if err != nil {
			return err
		}
		adds, err := addsOf(t.ctx, t.tx, r.FromID, "relations", ids)
		if err != nil {
			return err
		}
		for _, rid := range ids {
			if _, err := t.tx.ExecContext(t.ctx, "UPDATE relations SET deleted_at = ? WHERE id = ?", stamp(t.now), rid); err != nil {
				return err
			}
		}
		value, err := encodeValue(r)
		if err != nil {
			return err
		}
		// The ticket with the key first: the receipt's subject, whose rev
		// the page that asked shows.
		if err := t.touch(EntityTicket, tk.ID, tk.ID, tk.ProjectID); err != nil {
			return err
		}
		if err := t.recordRemove(ChangeRow{Entity: EntityTicket, EntityID: r.FromID, TicketID: r.FromID, ProjectID: tk.ProjectID,
			Field: "relations", Value: value}, rowTags(ids, adds)); err != nil {
			return err
		}
		return t.touch(EntityTicket, other.ID, other.ID, other.ProjectID)
	})
}

// relationHeld reports whether a live relation of the kind goes from the
// ticket with the id from to the one with the id to.
func (t *tx) relationHeld(from, to, kind string) (bool, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM relations WHERE from_id = ? AND to_id = ? AND kind = ? AND deleted_at = ''", from, to, kind).Scan(&n)
	return n > 0, err
}

// blocksAlready reports whether the ticket with the id blocker blocks the
// one with the id blocked, directly or through others, along the live
// blocks relations, tombstoned tickets' among them (a tombstone keeps its
// relations, so the invariant holds over the whole table and an undelete
// never closes a cycle). A new edge blocked → blocker would close a cycle
// exactly then. The walk goes up from blocked over its blockers, which
// the index on relations(to_id) serves, rather than down from blocker:
// the same reachability. UNION visits each ticket once, so it ends even
// on a cycle a merge left, bounded by the tickets of the project.
func (t *tx) blocksAlready(blocker, blocked string) (bool, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, `WITH RECURSIVE up(id) AS (
			SELECT ?
			UNION
			SELECT relations.from_id FROM relations JOIN up ON relations.to_id = up.id
			WHERE relations.kind = ? AND relations.deleted_at = ''
		)
		SELECT COUNT(*) FROM up WHERE id = ?`, blocked, RelationBlocks, blocker).Scan(&n)
	return n > 0, err
}

// relationCount counts the active relations of the live ticket with the
// id, at either end: a member counted once however many rows a merge left
// it, and a relation whose other end is deleted not at all, since it is
// inactive and counts for nothing while that ticket is deleted. The
// cycle walk still takes it as an edge (blocksAlready). That ticket's
// undelete brings the relation back whole and may leave this one past its
// bound, as a merge may, which refuses its next add until it is under it
// again and never a remove.
func (t *tx) relationCount(ticketID string) (int, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM (SELECT DISTINCT r.from_id, r.to_id, r.kind FROM relations r
		JOIN tickets src ON src.id = r.from_id JOIN tickets dst ON dst.id = r.to_id
		WHERE (r.from_id = ? OR r.to_id = ?) AND r.deleted_at = '' AND src.deleted_at = '' AND dst.deleted_at = '')`, ticketID, ticketID).Scan(&n)
	return n, err
}

// Relations reads the live relations of the ticket with the key, deleted
// or not, both ways, in the order they were made: each member once, the
// row of its latest add standing for it (the field rules: two replicas'
// adds of one member), with the ticket at its other end, whose DeletedAt
// says the relation is inactive. ErrNotFound when there is no such
// ticket.
func (s *Store) Relations(ctx context.Context, key string) ([]TicketRelation, error) {
	var out []TicketRelation
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = relationsOf(ctx, q, tk.ID)
		return err
	})
	return answer(out, err)
}

// relationsOf reads the live relations of the ticket with the id, both
// ways, each member once (Relations).
func relationsOf(ctx context.Context, q queryer, ticketID string) ([]TicketRelation, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.id, r.from_id, r.to_id, r.kind, r.created_at, r.created_by, r.deleted_at,
			o.id, o.key, o.title, o.status, o.deleted_at
		FROM relations r JOIN tickets o ON o.id = CASE WHEN r.from_id = ? THEN r.to_id ELSE r.from_id END
		WHERE (r.from_id = ? OR r.to_id = ?) AND r.deleted_at = ''`, ticketID, ticketID, ticketID)
	if err != nil {
		return nil, err
	}
	var all []TicketRelation
	for rows.Next() {
		var tr TicketRelation
		var created, deleted, otherDeleted string
		if err := rows.Scan(&tr.ID, &tr.FromID, &tr.ToID, &tr.Kind, &created, &tr.CreatedBy, &deleted,
			&tr.Other.ID, &tr.Other.Key, &tr.Other.Title, &tr.Other.Status, &otherDeleted); err != nil {
			rows.Close()
			return nil, err
		}
		tr.CreatedAt, tr.DeletedAt, tr.Other.DeletedAt = unstamp(created), unstamp(deleted), unstamp(otherDeleted)
		all = append(all, tr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type member struct{ from, to, kind string }
	groups := map[member][]TicketRelation{}
	var order []member
	for _, tr := range all {
		m := member{tr.FromID, tr.ToID, tr.Kind}
		if groups[m] == nil {
			order = append(order, m)
		}
		groups[m] = append(groups[m], tr)
	}
	out := []TicketRelation{}
	for _, m := range order {
		g := groups[m]
		if len(g) == 1 {
			out = append(out, g[0])
			continue
		}
		ids := make([]string, len(g))
		for i, tr := range g {
			ids[i] = tr.ID
		}
		adds, err := addsOf(ctx, q, m.from, "relations", ids)
		if err != nil {
			return nil, err
		}
		best := g[0]
		for _, tr := range g[1:] {
			if keyOf(tr.ID, stamp(tr.CreatedAt), adds).after(keyOf(best.ID, stamp(best.CreatedAt), adds)) {
				best = tr
			}
		}
		out = append(out, best)
	}
	slices.SortFunc(out, func(a, b TicketRelation) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// Blocked reports whether the ticket with the key is blocked: a live
// ticket blocks it whose status is neither done nor cancelled. A deleted
// blocker blocks nothing while it is deleted, and blocks again on its
// undelete. Nothing stops a blocked ticket's status from changing (a
// person decides); a read only says so. ErrNotFound when there is no
// such ticket.
func (s *Store) Blocked(ctx context.Context, key string) (bool, error) {
	var out bool
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = blockedOf(ctx, q, tk.ID)
		return err
	})
	return answer(out, err)
}

// blockedOf reports whether the ticket with the id is blocked (Blocked).
func blockedOf(ctx context.Context, q queryer, ticketID string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM relations JOIN tickets b ON b.id = relations.from_id
		WHERE relations.to_id = ? AND relations.kind = ? AND relations.deleted_at = '' AND b.deleted_at = '' AND b.status NOT IN (?, ?)`,
		ticketID, RelationBlocks, TicketDone, TicketCancelled).Scan(&n)
	return n > 0, err
}

// relationColumns is a relation's row as scanRelation reads it.
const relationColumns = "id, from_id, to_id, kind, created_at, created_by, deleted_at"

// scanRelation reads a relation's row.
func scanRelation(row interface{ Scan(...any) error }) (Relation, error) {
	var r Relation
	var created, deleted string
	if err := row.Scan(&r.ID, &r.FromID, &r.ToID, &r.Kind, &created, &r.CreatedBy, &deleted); err != nil {
		return Relation{}, err
	}
	r.CreatedAt, r.DeletedAt = unstamp(created), unstamp(deleted)
	return r, nil
}
