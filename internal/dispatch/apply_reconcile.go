package dispatch

import (
	"encoding/json"
)

// The invariants after a batch (docs/plans/dispatch.md, Reconcile): the
// parent chains, the blocks graph and the run links replayed from the
// whole log in the one order; an edge refused is undone in the row tables
// with a repair row of the actor merge, and an edge a later batch admits
// again (an earlier change arrived) is restored with one. Repairs are
// derived, each replica's own, and never handed out (Replicates).

// edgeRow is an edge change with the row it came from and the table row
// its member is (a relation's, a link's id; "" for a parent or a
// tombstone).
type edgeRow struct {
	e      EdgeChange
	row    ChangeRow
	member string
}

// reconcile replays the invariants and writes what their standing became.
func (a *applier) reconcile() error {
	rows, err := readChanges(a.ctx, a.tx, `WHERE entity = ? AND ((field = 'parent_id' AND op = ?) OR (field = 'deleted_at' AND op = ?)
		OR (field IN ('relations', 'links') AND op IN (?, ?)))`, EntityTicket, OpSet, OpSet, OpAdd, OpRemove)
	if err != nil {
		return err
	}
	var edges []EdgeChange
	byTag := map[Tag]edgeRow{}
	for _, r := range rows {
		er, ok := edgeOf(r)
		if !ok {
			continue
		}
		edges = append(edges, er.e)
		if r.Op != OpRemove {
			byTag[er.e.Tag()] = er
		}
	}
	refused := map[Tag]bool{}
	for _, e := range Reconcile(edges) {
		refused[e.Tag()] = true
	}
	standing, repairs, err := a.standings()
	if err != nil {
		return err
	}
	for tag, er := range byTag {
		wasRefused := standing[tag] == standingRefused
		switch {
		case refused[tag] && !wasRefused:
			if err := a.repair(er, false, repairs[tag]); err != nil {
				return err
			}
		case !refused[tag] && wasRefused:
			if err := a.repair(er, true, repairs[tag]); err != nil {
				return err
			}
		}
	}
	return nil
}

// edgeOf is the edge change a row is, and whether it is one: a ticket's
// parent set, its tombstone set or cleared, a blocks relation's add or
// remove, a run link's add or remove.
func edgeOf(r ChangeRow) (edgeRow, bool) {
	e := EdgeChange{At: r.At, Actor: canon(r), OpID: r.OpID, N: r.N, Op: r.Op, From: r.EntityID}
	switch r.Field {
	case "parent_id":
		e.Kind, e.To = EdgeParent, r.Value
		return edgeRow{e, r, ""}, true
	case "deleted_at":
		e.Kind, e.Deleted = EdgeTombstone, r.Value != ""
		return edgeRow{e, r, ""}, true
	case "relations":
		var rel Relation
		if json.Unmarshal([]byte(r.Value), &rel) != nil || rel.Kind != RelationBlocks {
			return edgeRow{}, false
		}
		e.Kind, e.From, e.To = EdgeBlocks, rel.FromID, rel.ToID
		if r.Op == OpRemove {
			e.Tags = priorTags(r.Prior, r.Origin)
		}
		return edgeRow{e, r, rel.ID}, true
	case "links":
		var l linkAdded
		if json.Unmarshal([]byte(r.Value), &l) != nil || l.Kind != LinkRun {
			return edgeRow{}, false
		}
		e.Kind, e.To = EdgeRun, l.Ref
		if r.Op == OpRemove {
			e.Tags = priorTags(r.Prior, r.Origin)
		}
		return edgeRow{e, r, l.ID}, true
	}
	return edgeRow{}, false
}

// The standing an edge's last repair gave it.
const (
	standingNone = iota
	standingRefused
	standingAdmitted
)

// standings is each edge's standing by its last repair, and how many
// repairs it has.
func (a *applier) standings() (map[Tag]int, map[Tag]int, error) {
	rows, err := readChanges(a.ctx, a.tx, "WHERE actor = ? AND op = ? ORDER BY n", ActorMerge, OpReconcile)
	if err != nil {
		return nil, nil, err
	}
	standing, count := map[Tag]int{}, map[Tag]int{}
	for _, r := range rows {
		var v struct {
			Tag      Tag  `json:"tag"`
			Admitted bool `json:"admitted"`
		}
		if json.Unmarshal([]byte(r.Value), &v) != nil {
			continue
		}
		count[v.Tag]++
		if v.Admitted {
			standing[v.Tag] = standingAdmitted
		} else {
			standing[v.Tag] = standingRefused
		}
	}
	return standing, count, nil
}

// repair records the edge's new standing and undoes or restores it in
// the row tables: a refused parent cleared, a refused relation or link
// tombstoned; an admitted one put back while its own rows still make it.
func (a *applier) repair(er edgeRow, admitted bool, n int) error {
	r := Repair(er.e, admitted, a.now, n)
	r.ProjectID = er.row.ProjectID
	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO changes (entity, entity_id, ticket_id, project_id, field, op, value, prior, at, actor, by, op_id, n, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, '', ?, ?, ?)`, r.Entity, r.EntityID, r.TicketID, r.ProjectID, r.Field, r.Op, r.Value, stamp(r.At), r.Actor, r.OpID, r.N,
		a.replica.tag); err != nil {
		return err
	}
	switch er.e.Kind {
	case EdgeParent:
		if !admitted {
			_, err := a.tx.ExecContext(a.ctx, "UPDATE tickets SET parent_id = '' WHERE id = ? AND parent_id = ?", er.e.From, er.e.To)
			return err
		}
		return a.projectScalar(EntityTicket, er.e.From, "parent_id")
	case EdgeBlocks, EdgeRun:
		table := "relations"
		if er.e.Kind == EdgeRun {
			table = "links"
		}
		if !admitted {
			_, err := a.tx.ExecContext(a.ctx, "UPDATE "+table+" SET deleted_at = ? WHERE id = ? AND deleted_at = ''", stamp(a.now), er.member)
			return err
		}
		a.sets[[2]string{er.row.EntityID, table}] = true
		return a.projectSet(er.row.EntityID, table)
	}
	return nil
}
