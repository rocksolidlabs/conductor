package dispatch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// The projection of rows from another replica: each field the row names
// made what the whole log makes it by the field's class (rules.go), so
// local and remote rows give one state wherever they arrived.

// project writes what the row makes of its entity.
func (a *applier) project(c ChangeRow) error {
	switch c.Op {
	case OpCreate:
		return a.create(c)
	case OpAdd, OpRemove:
		a.sets[[2]string{c.EntityID, c.Field}] = true
		return nil
	}
	// A scalar, gated or counterpart field: set, observe or adopt.
	if Class(c.Entity, c.Field) == ClassCounterpart || (c.Entity == EntityTicket && (c.Op == OpObserve || c.Op == OpAdopt)) {
		a.group[c.EntityID] = true
		if Class(c.Entity, c.Field) == ClassCounterpart {
			return nil
		}
	}
	return a.projectScalar(c.Entity, c.EntityID, c.Field)
}

// tableOf is the table an entity's row is kept in.
func tableOf(entity string) string {
	if rt, ok := revTables[entity]; ok {
		return rt.table
	}
	return ""
}

// projectScalar writes the field the last of its rows in the one order
// sets (a set, an observe or an adopt), DS1's scalar rule.
func (a *applier) projectScalar(entity, id, field string) error {
	table := tableOf(entity)
	if table == "" || !fieldPattern.MatchString(field) {
		return fmt.Errorf("dispatch: no column %s of %s", field, entity)
	}
	rows, err := readChanges(a.ctx, a.tx, "WHERE entity = ? AND entity_id = ? AND field = ? AND op IN (?, ?, ?)", entity, id, field, OpSet, OpObserve, OpAdopt)
	if err != nil || len(rows) == 0 {
		return err
	}
	Order(rows)
	last := rows[len(rows)-1]
	_, err = a.tx.ExecContext(a.ctx, "UPDATE "+table+" SET "+field+" = ? WHERE id = ?", last.Value, id)
	return err
}

// create makes the entity a create row names, as its writer made it
// before its set rows: the immutable fields from the row, every other
// field at the schema's default until its rows set it.
func (a *applier) create(c ChangeRow) error {
	at := stamp(a.now)
	switch c.Entity {
	case EntityProject:
		var v projectCreated
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return err
		}
		key, err := a.registryKey(c)
		if err != nil {
			return err
		}
		var of, ot, pf, pt int64
		if err := a.tx.QueryRowContext(a.ctx, `SELECT COALESCE(MAX(overflow_from), 0), COALESCE(MAX(overflow_to), 0), COALESCE(MAX(provisional_from), 0),
			COALESCE(MAX(provisional_to), 0) FROM projects`).Scan(&of, &ot, &pf, &pt); err != nil {
			return err
		}
		_, err = a.tx.ExecContext(a.ctx, `INSERT INTO projects (id, key, name, created_at, updated_at, shared, share_state, registry_key,
			overflow_from, overflow_to, overflow_next, provisional_from, provisional_to, provisional_next) VALUES (?, ?, '', ?, ?, ?, 'shared', ?, ?, ?, ?, ?, ?, ?)`,
			v.ID, key, stamp(v.CreatedAt), at, a.replica.workspace, key, of, ot, of, pf, pt, pf)
		return err
	case EntityTicket:
		var v ticketCreated
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return err
		}
		var projectKey string
		if err := a.tx.QueryRowContext(a.ctx, "SELECT CASE WHEN registry_key != '' THEN registry_key ELSE key END FROM projects WHERE id = ?", v.ProjectID).Scan(&projectKey); err != nil {
			return err
		}
		_, err := a.tx.ExecContext(a.ctx, "INSERT INTO tickets (id, project_id, number, key, title, created_at, created_by, updated_at) VALUES (?, ?, ?, ?, '', ?, ?, ?)",
			v.ID, v.ProjectID, v.Number, TicketKey(projectKey, v.Number), stamp(v.CreatedAt), v.CreatedBy, at)
		return err
	case EntityComment:
		var v commentCreated
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return err
		}
		_, err := a.tx.ExecContext(a.ctx, "INSERT INTO comments (id, ticket_id, author, by, body, created_at) VALUES (?, ?, ?, ?, '', ?)",
			v.ID, v.TicketID, Canon(v.Author, c.Origin), v.By, stamp(v.CreatedAt))
		return err
	case EntityLabel:
		var v labelCreated
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return err
		}
		_, err := a.tx.ExecContext(a.ctx, "INSERT INTO labels (id, project_id, name, created_at) VALUES (?, ?, '', ?)", v.ID, v.ProjectID, stamp(v.CreatedAt))
		return err
	case EntityMilestone:
		var v milestoneCreated
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return err
		}
		_, err := a.tx.ExecContext(a.ctx, "INSERT INTO milestones (id, project_id, name, created_at, updated_at) VALUES (?, ?, '', ?, ?)",
			v.ID, v.ProjectID, stamp(v.CreatedAt), at)
		return err
	case EntityView:
		var v viewCreated
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return err
		}
		_, err := a.tx.ExecContext(a.ctx, "INSERT INTO views (id, project_id, name, kind, created_at, updated_at) VALUES (?, ?, '', '', ?, ?)",
			v.ID, v.ProjectID, stamp(v.CreatedAt), at)
		return err
	case EntityPerson:
		var v Person
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return err
		}
		_, err := a.tx.ExecContext(a.ctx, "INSERT OR IGNORE INTO people (name, created_at) VALUES (?, ?)", c.EntityID, stamp(v.CreatedAt))
		return err
	case EntityPlan:
		var v planCreated
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return err
		}
		view, ok := a.views[c.ViewDigest]
		if !ok {
			if err := a.tx.QueryRowContext(a.ctx, "SELECT body FROM held_bodies WHERE digest = ?", c.ViewDigest).Scan(&view); err != nil {
				return err
			}
		}
		var doc struct {
			V      int    `json:"v"`
			Digest string `json:"digest"`
		}
		if json.Unmarshal(view, &doc) != nil || doc.V != planViewVersion || doc.Digest != v.Digest || sha256Hex(string(view)) != c.ViewDigest {
			return fmt.Errorf("%w: revision %s", ErrBadView, v.ID)
		}
		if _, err := a.tx.ExecContext(a.ctx, "INSERT OR IGNORE INTO plan_views (view_digest, digest, view) VALUES (?, ?, ?)", c.ViewDigest, v.Digest, view); err != nil {
			return err
		}
		if _, err := a.tx.ExecContext(a.ctx, "DELETE FROM held_bodies WHERE digest = ?", c.ViewDigest); err != nil {
			return err
		}
		_, err := a.tx.ExecContext(a.ctx, `INSERT INTO plans (id, ticket_id, based_on, crew_id, crew_name, crew_updated_at, base, body, digest, bytes,
			state, created_at, created_by, fingerprint, view_digest, body_here) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, 0)`,
			v.ID, v.TicketID, v.BasedOn, v.CrewID, v.CrewName, stamp(v.CrewUpdatedAt), v.Base, v.Digest, v.Bytes, PlanProposed,
			stamp(v.CreatedAt), v.CreatedBy, v.Fingerprint, c.ViewDigest)
		return err
	}
	return fmt.Errorf("dispatch: no create of %s", c.Entity)
}

// projectSet makes the ticket's set field what its adds and removes make
// it, the observed-remove rule: a member is present while an add of it is
// in the log that no remove names, whatever the rows' places in the one
// order.
func (a *applier) projectSet(ticket, field string) error {
	rows, err := readChanges(a.ctx, a.tx, "WHERE entity = ? AND entity_id = ? AND field = ? AND op IN (?, ?)", EntityTicket, ticket, field, OpAdd, OpRemove)
	if err != nil {
		return err
	}
	Order(rows)
	removed := map[Tag]bool{}
	for _, r := range rows {
		if r.Op == OpRemove {
			for _, t := range priorTags(r.Prior, r.Origin) {
				removed[t] = true
			}
		}
	}
	switch field {
	case "assignees", "labels":
		table, column := "ticket_assignees", "actor"
		if field == "labels" {
			table, column = "ticket_labels", "label_id"
		}
		live := map[string]string{} // member → its first live add's at
		for _, r := range rows {
			if r.Op == OpAdd && !removed[TagOf(r)] {
				if _, ok := live[r.Value]; !ok {
					live[r.Value] = stamp(r.At)
				}
			}
		}
		have, err := readTexts(a.ctx, a.tx, "SELECT "+column+" FROM "+table+" WHERE ticket_id = ?", ticket)
		if err != nil {
			return err
		}
		for _, m := range have {
			if _, ok := live[m]; !ok {
				if _, err := a.tx.ExecContext(a.ctx, "DELETE FROM "+table+" WHERE ticket_id = ? AND "+column+" = ?", ticket, m); err != nil {
					return err
				}
			}
		}
		for m, at := range live {
			if slices.Contains(have, m) {
				continue
			}
			stmt, args := "INSERT INTO ticket_labels (ticket_id, label_id) VALUES (?, ?)", []any{ticket, m}
			if field == "assignees" {
				stmt, args = "INSERT INTO ticket_assignees (ticket_id, actor, added_at) VALUES (?, ?, ?)", []any{ticket, m, at}
			}
			if _, err := a.tx.ExecContext(a.ctx, stmt, args...); err != nil {
				return err
			}
		}
		return nil
	case "relations":
		for _, r := range rows {
			if r.Op != OpAdd {
				continue
			}
			var rel Relation
			if err := json.Unmarshal([]byte(r.Value), &rel); err != nil {
				return err
			}
			deleted := ""
			if removed[TagOf(r)] {
				deleted = stamp(removedAt(rows, TagOf(r)))
			}
			if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO relations (id, from_id, to_id, kind, created_at, created_by, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET deleted_at = CASE WHEN excluded.deleted_at = '' THEN '' WHEN relations.deleted_at != '' THEN relations.deleted_at ELSE excluded.deleted_at END`,
				rel.ID, rel.FromID, rel.ToID, rel.Kind, stamp(rel.CreatedAt), rel.CreatedBy, deleted); err != nil {
				return err
			}
		}
		return nil
	case "links":
		for _, r := range rows {
			if r.Op != OpAdd {
				continue
			}
			var l linkAdded
			if err := json.Unmarshal([]byte(r.Value), &l); err != nil {
				return err
			}
			deleted := ""
			if removed[TagOf(r)] {
				deleted = stamp(removedAt(rows, TagOf(r)))
			}
			verified := ""
			if len(l.Verified) > 0 {
				verified = string(l.Verified)
			}
			if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO links (id, ticket_id, kind, ref, url, note, candidate, verified, created_at, created_by, fingerprint, deleted_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET deleted_at = CASE WHEN excluded.deleted_at = '' THEN '' WHEN links.deleted_at != '' THEN links.deleted_at ELSE excluded.deleted_at END`,
				l.ID, ticket, l.Kind, l.Ref, l.URL, l.Note, l.Candidate, verified, stamp(l.CreatedAt), l.CreatedBy, l.Fingerprint, deleted); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("dispatch: no set %s of a ticket", field)
}

// removedAt is when the first remove that names the tag was made.
func removedAt(rows []ChangeRow, tag Tag) time.Time {
	for _, r := range rows {
		if r.Op == OpRemove && slices.Contains(priorTags(r.Prior, r.Origin), tag) {
			return r.At
		}
	}
	return time.Time{}
}

// fieldPattern is a column's name, which a row's field names.
var fieldPattern = regexp.MustCompile(`^[a-z_]{1,32}$`)

// mergeCounterpart merges the ticket's counterpart group over the rows
// this replica holds (ReconcileCounterpart: every replica's rows but their
// snapshots, which never leave a machine, and this replica's own
// snapshot rows) and drops its read of the linked item, so the page's
// next open reads the tracker with this machine's own credential.
func (a *applier) mergeCounterpart(ticket string) error {
	rows, err := readChanges(a.ctx, a.tx, "WHERE entity = ? AND entity_id = ?", EntityTicket, ticket)
	if err != nil {
		return err
	}
	st := ReconcileCounterpart(rows)
	if st.ExternalSnapshot == "" {
		// No read of the item on this machine: the address is the decided
		// item's last address row, and a field the tracker owns its last
		// row (an import's, an observe's), in DS1's order, until this
		// machine reads the item itself.
		Order(rows)
		for _, r := range rows {
			switch {
			case r.Field == "external_url" && st.ExternalID != "":
				st.ExternalURL = r.Value
			case r.Field == FieldTitle && (r.Op == OpSet || r.Op == OpObserve || r.Op == OpAdopt):
				st.Title = r.Value
			case r.Field == FieldDescription && (r.Op == OpSet || r.Op == OpObserve || r.Op == OpAdopt):
				st.Description = r.Value
			}
		}
	}
	if _, err := a.tx.ExecContext(a.ctx, `UPDATE tickets SET external_id = ?, external_url = ?, external_snapshot = ?, authority = ?, title = ?, description = ?
		WHERE id = ?`, st.ExternalID, st.ExternalURL, st.ExternalSnapshot, st.Authority, st.Title, st.Description, ticket); err != nil {
		return err
	}
	_, err = a.tx.ExecContext(a.ctx, "DELETE FROM ticket_reads WHERE ticket_id = ?", ticket)
	return err
}
