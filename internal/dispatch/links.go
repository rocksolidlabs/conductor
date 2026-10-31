package dispatch

import (
	"context"
	"slices"
	"strings"
)

// A link is a ticket's pointer outward: a delivery (a merged pull
// request's or a release's URL: a merge is such a link in this item,
// its merged state never read), a run of the engine's, or an external
// reference. Its identity is (ticket, kind, ref), ref being a run's id, a
// pull request's number, else the URL itself, so every link has one. It
// is a member of its ticket's links set: one live row per identity on a
// local write, a rule and not an index, two replicas' adds of one member
// one member with two tags whose later add's row (its URL and note) stands
// for it, and a remove through that row's id removing both. A run belongs
// to one ticket at most: a run link names a run no other live ticket's
// live run link names, a tombstone's links holding no run. The store's
// one sight of the engine is the RunExists function the API gives it; it
// starts nothing.

// NewLink is what link.add takes: the kind (LinkKinds); the reference,
// which a run link needs (the run's id) and a delivery or external link
// takes from its URL when none is given; the URL, which a delivery or
// external link needs; and a note.
type NewLink struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
	URL  string `json:"url,omitempty"`
	Note string `json:"note,omitempty"`
}

// SetRunExists gives the store the engine's sight of its runs: fn reports
// whether a run with the id exists, one the engine holds or has a record
// of. A run link names only a run fn knows; with no fn every run link is
// refused not_found. fn runs inside an operation's transaction with the
// store's lock held, so it must not call the store.
func (s *Store) SetRunExists(fn func(id string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runExists = fn
}

// AddLink links the live ticket with the key outward (link.add): its
// fields checked by their rules (the URL http or https, the reference
// and the URL at most 2 KiB, the note at most MaxLinkNote characters); a
// delivery or external link names its URL and takes it as its reference
// when it names none, a run link names its reference (invalid
// otherwise). Its identity live on the ticket already is invalid. A run
// link's reference names a run that exists (not_found otherwise) and no
// other live ticket's live run link (run_linked, the receipt's Key naming
// that ticket), so a run belongs to one ticket at most. At most MaxLinks
// live links on the ticket (bound). One add row, its value the link
// whole ({id, kind, ref, url, note, createdAt, createdBy}). A deleted
// ticket is not_found; an expected revision is the ticket's; an archived
// project refuses it (archived). The receipt carries the link's id.
func (s *Store) AddLink(ctx context.Context, op Op, key string, l NewLink) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
		NewLink
	}{key, l}
	return s.Apply(ctx, op, "link.add", args, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if !slices.Contains(LinkKinds, l.Kind) {
			return refuse(RefusalInvalid, "the kind %q is none of %s", l.Kind, strings.Join(LinkKinds, ", "))
		}
		for _, f := range []struct{ key, label, value string }{
			{"link.ref", "the reference", l.Ref},
			{"link.url", "the URL", l.URL},
			{"link.note", "the note", l.Note},
		} {
			if err := checkField(f.key, f.label, f.value); err != nil {
				return err
			}
		}
		ref := l.Ref
		switch {
		case l.Kind == LinkRun && ref == "":
			return refuse(RefusalInvalid, "a run link names its run")
		case l.Kind != LinkRun && l.URL == "":
			return refuse(RefusalInvalid, "a %s link names its URL", l.Kind)
		case ref == "":
			ref = l.URL
		}
		var held int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM links WHERE ticket_id = ? AND kind = ? AND ref = ? AND deleted_at = ''",
			tk.ID, l.Kind, ref).Scan(&held); err != nil {
			return err
		}
		if held > 0 {
			return refuse(RefusalInvalid, "%s has that %s link already", tk.Key, l.Kind)
		}
		if l.Kind == LinkRun {
			if t.runExists == nil || !t.runExists(ref) {
				return refuse(RefusalNotFound, "no run %s", ref)
			}
			holder, err := t.runHolder(ref, tk.ID)
			if err != nil {
				return err
			}
			if holder != "" {
				return &Refusal{Code: RefusalRunLinked, Words: "the run " + ref + " is linked to " + holder, Key: holder}
			}
		}
		var n int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM (SELECT DISTINCT kind, ref FROM links WHERE ticket_id = ? AND deleted_at = '')", tk.ID).Scan(&n); err != nil {
			return err
		}
		if err := checkCount("links", n+1, MaxLinks); err != nil {
			return err
		}
		link := Link{ID: newID(), TicketID: tk.ID, Kind: l.Kind, Ref: ref, URL: l.URL, Note: l.Note, CreatedAt: t.now, CreatedBy: t.op.author()}
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO links (id, ticket_id, kind, ref, url, note, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			link.ID, link.TicketID, link.Kind, link.Ref, link.URL, link.Note, stamp(link.CreatedAt), link.CreatedBy); err != nil {
			return err
		}
		t.created(link.ID, "")
		value, err := encodeValue(link)
		if err != nil {
			return err
		}
		return t.record(ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID, Field: "links", Op: OpAdd, Value: value})
	})
}

// RemoveLink removes the live link with the id from the live ticket with
// the key (link.remove): every live row of its member tombstoned (two,
// after a merge brought a second add), one remove row (more when a merge
// brought more adds than one row's prior holds), its value the row named
// whole and its prior the tags of the adds it removes, so the member is
// gone on every replica. A link added again after it is a new row with a
// new id. The link missing, removed or another ticket's is not_found; a
// deleted ticket is not_found; an expected revision is the ticket's; an
// archived project refuses it (archived).
func (s *Store) RemoveLink(ctx context.Context, op Op, key, id string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}{key, id}
	return s.Apply(ctx, op, "link.remove", args, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		l, err := scanLink(t.tx.QueryRowContext(t.ctx, "SELECT "+linkColumns+" FROM links WHERE id = ?", id))
		switch {
		case isNoRows(err):
			return refuse(RefusalNotFound, "no link %s", id)
		case err != nil:
			return err
		case l.TicketID != tk.ID:
			return refuse(RefusalNotFound, "the link %s is not %s's", id, tk.Key)
		case !l.DeletedAt.IsZero():
			return refuse(RefusalNotFound, "the link %s is removed", id)
		}
		return t.removeLink(tk, l)
	})
}

// removeLink tombstones every live row of the member of the ticket's
// link l and records its remove: its value l whole, its prior the tags of
// the adds it removes (one row, more when they are more than one prior
// holds: recordRemove).
func (t *tx) removeLink(tk Ticket, l Link) error {
	ids, err := readTexts(t.ctx, t.tx, "SELECT id FROM links WHERE ticket_id = ? AND kind = ? AND ref = ? AND deleted_at = ''", tk.ID, l.Kind, l.Ref)
	if err != nil {
		return err
	}
	adds, err := addsOf(t.ctx, t.tx, tk.ID, "links", ids)
	if err != nil {
		return err
	}
	for _, lid := range ids {
		if _, err := t.tx.ExecContext(t.ctx, "UPDATE links SET deleted_at = ? WHERE id = ?", stamp(t.now), lid); err != nil {
			return err
		}
	}
	value, err := encodeValue(l)
	if err != nil {
		return err
	}
	return t.recordRemove(ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID, Field: "links", Value: value},
		rowTags(ids, adds))
}

// runHolder is the key of the live ticket other than the one with the id
// except whose live run link names the run, "" when there is none: a
// tombstone's links hold no run.
func (t *tx) runHolder(run, except string) (string, error) {
	keys, err := readTexts(t.ctx, t.tx, `SELECT tickets.key FROM links JOIN tickets ON tickets.id = links.ticket_id
		WHERE links.kind = ? AND links.ref = ? AND links.deleted_at = '' AND tickets.deleted_at = '' AND tickets.id != ?
		ORDER BY tickets.key LIMIT 1`, LinkRun, run, except)
	if err != nil || len(keys) == 0 {
		return "", err
	}
	return keys[0], nil
}

// releaseRuns removes the run links of the ticket, undeleted in this
// operation, whose run another live ticket linked while it was deleted:
// one remove row each, so a run belongs to one live ticket at most. Its
// other links come back as they were.
func (t *tx) releaseRuns(tk Ticket) error {
	links, err := linksOf(t.ctx, t.tx, tk.ID)
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.Kind != LinkRun {
			continue
		}
		holder, err := t.runHolder(l.Ref, tk.ID)
		if err != nil {
			return err
		}
		if holder == "" {
			continue
		}
		if err := t.removeLink(tk, l); err != nil {
			return err
		}
	}
	return nil
}

// Links reads the live links of the ticket with the key, deleted or not,
// in the order they were made: each member once, the row of its latest
// add standing for it, its URL and note the ones shown (the field rules:
// two replicas' adds of one member). ErrNotFound when there is no such
// ticket.
func (s *Store) Links(ctx context.Context, key string) ([]Link, error) {
	var out []Link
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = linksOf(ctx, q, tk.ID)
		return err
	})
	return out, err
}

// linksOf reads the live links of the ticket with the id, each member
// once (Links).
func linksOf(ctx context.Context, q queryer, ticketID string) ([]Link, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+linkColumns+" FROM links WHERE ticket_id = ? AND deleted_at = ''", ticketID)
	if err != nil {
		return nil, err
	}
	var all []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type member struct{ kind, ref string }
	groups := map[member][]Link{}
	var order []member
	for _, l := range all {
		m := member{l.Kind, l.Ref}
		if groups[m] == nil {
			order = append(order, m)
		}
		groups[m] = append(groups[m], l)
	}
	var many []string
	for _, m := range order {
		if len(groups[m]) > 1 {
			for _, l := range groups[m] {
				many = append(many, l.ID)
			}
		}
	}
	adds, err := addsOf(ctx, q, ticketID, "links", many)
	if err != nil {
		return nil, err
	}
	out := []Link{}
	for _, m := range order {
		g := groups[m]
		best := g[0]
		for _, l := range g[1:] {
			if keyOf(l.ID, stamp(l.CreatedAt), adds).after(keyOf(best.ID, stamp(best.CreatedAt), adds)) {
				best = l
			}
		}
		out = append(out, best)
	}
	slices.SortFunc(out, func(a, b Link) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// RunTicket reads the ticket a run is linked to, for the run page's chip:
// the live ticket whose live run link names the run, when there is one
// (one at most, by run_linked); else, when only deleted tickets' live run
// links name it, the one whose link's add is latest in the log's order,
// its DeletedAt set so the chip says so. A live ticket always wins over a
// tombstone, so a run linked again after its ticket's delete answers the
// new ticket. found is false when no live link names the run.
func (s *Store) RunTicket(ctx context.Context, run string) (tk Ticket, found bool, err error) {
	err = s.read(ctx, func(q queryer) error {
		tk, found, err = runTicket(ctx, q, run)
		return err
	})
	return tk, found, err
}

// runTicket is RunTicket read through q: a read transaction's, or an
// operation's (AddRunComment), which finds the run's ticket inside its
// own transaction.
func runTicket(ctx context.Context, q queryer, run string) (tk Ticket, found bool, err error) {
	rows, err := q.QueryContext(ctx, `SELECT links.id, links.ticket_id, links.created_at, tickets.deleted_at FROM links JOIN tickets ON tickets.id = links.ticket_id
		WHERE links.kind = ? AND links.ref = ? AND links.deleted_at = ''`, LinkRun, run)
	if err != nil {
		return Ticket{}, false, err
	}
	type holder struct {
		link, ticket, created string
		live                  bool
	}
	var holders []holder
	for rows.Next() {
		var h holder
		var deleted string
		if err := rows.Scan(&h.link, &h.ticket, &h.created, &deleted); err != nil {
			rows.Close()
			return Ticket{}, false, err
		}
		h.live = deleted == ""
		holders = append(holders, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Ticket{}, false, err
	}
	var best *holder
	var bestKey foldKey
	for i := range holders {
		h := &holders[i]
		adds, err := addsOf(ctx, q, h.ticket, "links", []string{h.link})
		if err != nil {
			return Ticket{}, false, err
		}
		k := keyOf(h.link, h.created, adds)
		if best == nil || (h.live && !best.live) || (h.live == best.live && k.after(bestKey)) {
			best, bestKey = h, k
		}
	}
	if best == nil {
		return Ticket{}, false, nil
	}
	tk, err = ticketByID(ctx, q, best.ticket)
	if err != nil {
		return Ticket{}, false, err
	}
	return tk, true, nil
}

// linkColumns is a link's row as scanLink reads it.
const linkColumns = "id, ticket_id, kind, ref, url, note, created_at, created_by, deleted_at"

// scanLink reads a link's row.
func scanLink(row interface{ Scan(...any) error }) (Link, error) {
	var l Link
	var created, deleted string
	if err := row.Scan(&l.ID, &l.TicketID, &l.Kind, &l.Ref, &l.URL, &l.Note, &created, &l.CreatedBy, &deleted); err != nil {
		return Link{}, err
	}
	l.CreatedAt, l.DeletedAt = unstamp(created), unstamp(deleted)
	return l, nil
}
