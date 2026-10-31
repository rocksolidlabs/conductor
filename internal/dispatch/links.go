package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
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
// for it, and a remove through that row's id removing both.
//
// A run belongs to one ticket at most, and so does its whole chain: the
// run, every run it continues and every run that continues it (Program
// item DS2, docs/plans/dispatch-evidence.md, "Links"). A ticket holds a
// chain when it launched a run of it (a launch, or the run's package) or
// has a live run link to a run of it, a tombstone's links holding no run.
// A launched run is bound by its package and is never linked by hand, nor
// is a run of its chain; a run link names a run of a chain no other
// ticket holds; and a chain a version 1 store left on two tickets' links
// (its rule compared one run id) is kept as it was, contested
// (ChainHolders naming both), until a person removes one of the links.
// The store's sight of the engine is the functions the API gives it
// (SetRunExists, SetRunTicket, SetRunChain); it starts nothing.

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

// LinkAddArgs is link.add's content, its fingerprint: the ticket's key
// and the link as the client sent it, before an empty reference takes the
// URL, so a request with the reference omitted and one with the reference
// the URL make one link and differ as requests. Replay takes it.
type LinkAddArgs struct {
	Key string `json:"key"`
	NewLink
}

// linkAdded is a link's add row's value: the link whole and the
// fingerprint of the operation that added it, which the link's row keeps
// too, so a replica can compare a resend once its receipt is pruned.
type linkAdded struct {
	Link
	Fingerprint string `json:"fingerprint"`
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

// runSight is the store's sight of a run's package and of its chain, the
// functions the API gives it: ticket the id of the ticket a run was
// launched from (its package's, which its continuations carry), chain the
// runs of a run's chain. Either may be nil, for no sight: a run's chain is
// then the run alone, and its package known only by the store's launches.
type runSight struct {
	ticket func(run string) (ticketID string, ok bool)
	chain  func(run string) []string
}

// SetRunTicket gives the store the engine's sight of a run's package: fn
// reports the id of the ticket the run was launched from (the run's
// Ticket, from the engine or its record), ok false for a run no ticket
// launched. A run it names is held by that ticket whether or not the store
// has the launch (one a replica made). fn runs inside an operation's
// transaction with the store's lock held, so it must not call the store.
func (s *Store) SetRunTicket(fn func(run string) (ticketID string, ok bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sight.ticket = fn
}

// SetRunChain gives the store the engine's sight of a run's chain: fn
// reports the runs of the run's chain, the run, every run it continues
// and every run that continues it (the ledger's continued_by rows and the
// run records' resumedFrom and resumedBy), the run itself among them or
// not. A chain only grows, by a continuation that is a new run, so a check
// made at a link's write stays true until the link is removed. fn runs
// inside an operation's transaction with the store's lock held, so it
// must not call the store.
func (s *Store) SetRunChain(fn func(run string) []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sight.chain = fn
}

// chainOf is the runs of the run's chain, the run first, each once.
func (rs runSight) chainOf(run string) []string {
	out := []string{run}
	if rs.chain != nil {
		for _, r := range rs.chain(run) {
			if r != "" && !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// ChainHolder is a ticket that holds a run's chain: the ticket, the run of
// the chain it holds it by, and how: Launch, the launch that made that run
// (or "" when only the run's package names the ticket), or Link, its live
// run link to it.
type ChainHolder struct {
	TicketID string `json:"ticketId"`
	Key      string `json:"key"`
	Run      string `json:"run"`
	Launch   string `json:"launch,omitempty"`
	Link     string `json:"link,omitempty"`
	// deleted says the ticket is a tombstone: a launch binds its run
	// whatever becomes of the ticket, and a tombstone's links hold none.
	deleted bool
}

// chainHolders reads who holds the chain of runs through q: launched, the
// tickets that launched a run of it, live or deleted (a launch pending or
// started, else the run's package by sight), and linked, the live
// tickets whose live run link names a run of it, each in the order found.
// A version 1 database, read as it was found, has no launches.
func chainHolders(ctx context.Context, q queryer, sight runSight, runs []string) (launched, linked []ChainHolder, err error) {
	for _, run := range runs {
		var byLaunch []ChainHolder
		if schemaOf(q) >= 2 {
			rows, err := q.QueryContext(ctx, `SELECT launches.id, tickets.id, tickets.key, tickets.deleted_at FROM launches JOIN tickets ON tickets.id = launches.ticket_id
				WHERE launches.run_id = ? AND launches.state != ? ORDER BY launches.created_at, launches.id`, run, LaunchFailed)
			if err != nil {
				return nil, nil, err
			}
			for rows.Next() {
				h := ChainHolder{Run: run}
				var deleted string
				if err := rows.Scan(&h.Launch, &h.TicketID, &h.Key, &deleted); err != nil {
					rows.Close()
					return nil, nil, err
				}
				h.deleted = deleted != ""
				byLaunch = append(byLaunch, h)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, nil, err
			}
		}
		if sight.ticket != nil {
			if id, ok := sight.ticket(run); ok && !slices.ContainsFunc(byLaunch, func(h ChainHolder) bool { return h.TicketID == id }) {
				h := ChainHolder{TicketID: id, Key: id, Run: run}
				tk, err := ticketByID(ctx, q, id)
				switch {
				case err == nil:
					h.Key, h.deleted = tk.Key, !tk.DeletedAt.IsZero()
				case !isNoRows(err):
					return nil, nil, err
				}
				byLaunch = append(byLaunch, h)
			}
		}
		launched = append(launched, byLaunch...)
		rows, err := q.QueryContext(ctx, `SELECT links.id, tickets.id, tickets.key FROM links JOIN tickets ON tickets.id = links.ticket_id
			WHERE links.kind = ? AND links.ref = ? AND links.deleted_at = '' AND tickets.deleted_at = '' ORDER BY tickets.key, links.created_at, links.id`, LinkRun, run)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			h := ChainHolder{Run: run}
			if err := rows.Scan(&h.Link, &h.TicketID, &h.Key); err != nil {
				rows.Close()
				return nil, nil, err
			}
			linked = append(linked, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return launched, linked, nil
}

// heldBy is the live tickets among holders, one entry each (the first
// found), by key.
func heldBy(holders ...[]ChainHolder) []ChainHolder {
	var out []ChainHolder
	for _, hs := range holders {
		for _, h := range hs {
			if !h.deleted && !slices.ContainsFunc(out, func(o ChainHolder) bool { return o.TicketID == h.TicketID }) {
				out = append(out, h)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b ChainHolder) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// ChainHolders reads the live tickets that hold the chain of the run (by a
// launch of a run of it, or by a live run link to one), over the runs the
// store's sight of the chain gives (SetRunChain), one entry each, by key:
// none for a chain no live ticket holds, one for a held chain, and more
// for a contested one, which a version 1 store or a merge left on two
// tickets' links and which no ticket owns until a person removes one of
// the links.
func (s *Store) ChainHolders(ctx context.Context, run string) ([]ChainHolder, error) {
	s.mu.Lock()
	sight := s.sight
	s.mu.Unlock()
	var out []ChainHolder
	err := s.read(ctx, func(q queryer) error {
		launched, linked, err := chainHolders(ctx, q, sight, sight.chainOf(run))
		if err != nil {
			return err
		}
		out = heldBy(launched, linked)
		if out == nil {
			out = []ChainHolder{}
		}
		return nil
	})
	return out, err
}

// chainRefusal is why a run link to the run, on the ticket tk, is refused
// by who holds the run's chain (chainHolders), nil when no one does: a
// launched run, or a run of a launched run's chain, is its launching
// ticket's (run_linked naming it, live or deleted, since a package binds
// its run whatever becomes of its ticket); a run of a chain one live
// ticket links is that ticket's, the link's own ticket among them
// (run_linked naming it); and a contested chain is refused naming every
// holder, the receipt's key the first.
func chainRefusal(tk Ticket, run string, launched, linked []ChainHolder) error {
	if len(launched) > 0 {
		h := launched[0]
		words := fmt.Sprintf("the run %s was launched from %s: it is among %s's runs already", run, h.Key, h.Key)
		if h.Run != run {
			words = fmt.Sprintf("the run %s is of one chain with run %s, launched from %s: it is among %s's runs already", run, h.Run, h.Key, h.Key)
		}
		return &Refusal{Code: RefusalRunLinked, Words: words, Key: h.Key}
	}
	holders := heldBy(linked)
	switch len(holders) {
	case 0:
		return nil
	case 1:
		h := holders[0]
		switch {
		case h.Run == run:
			return &Refusal{Code: RefusalRunLinked, Words: "the run " + run + " is linked to " + h.Key, Key: h.Key}
		case h.TicketID == tk.ID:
			return &Refusal{Code: RefusalRunLinked, Key: h.Key,
				Words: fmt.Sprintf("%s links run %s already, of one chain with run %s: a chain belongs to one ticket, and it is among %s's runs", h.Key, h.Run, run, h.Key)}
		}
		return &Refusal{Code: RefusalRunLinked, Key: h.Key,
			Words: fmt.Sprintf("%s links run %s, of one chain with run %s: a chain belongs to one ticket", h.Key, h.Run, run)}
	}
	keys := make([]string, len(holders))
	for i, h := range holders {
		keys[i] = h.Key
	}
	return &Refusal{Code: RefusalRunLinked, Key: keys[0],
		Words: fmt.Sprintf("the chain of run %s is contested: %s each link a run of it; remove one of the links first", run, strings.Join(keys, " and "))}
}

// AddLink links the live ticket with the key outward (link.add): its
// fields checked by their rules (the URL http or https, the reference
// and the URL at most 2 KiB, the note at most MaxLinkNote characters); a
// delivery or external link names its URL and takes it as its reference
// when it names none, a run link names its reference (invalid
// otherwise); a decision link is reserved for Program item DR1 (invalid).
// Its identity live on the ticket already is invalid. A run link's
// reference names a run that exists (not_found otherwise) of a chain no
// ticket holds (run_linked, the receipt's Key naming the holder:
// chainRefusal), so a run, and its chain, belongs to one ticket at most. At most MaxLinks
// live links on the ticket (bound). One add row, its value the link
// whole ({id, kind, ref, url, note, createdAt, createdBy}) and the
// operation's fingerprint, which the link's row keeps as well (Replay
// compares a resend by it once the receipt is pruned). A deleted
// ticket is not_found; an expected revision is the ticket's; an archived
// project refuses it (archived). The receipt carries the link's id.
func (s *Store) AddLink(ctx context.Context, op Op, key string, l NewLink) (Receipt, error) {
	return s.Apply(ctx, op, IntentLinkAdd, LinkAddArgs{Key: key, NewLink: l}, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if l.Kind == LinkDecision {
			return refuse(RefusalInvalid, "a decision link is reserved for durable decision records, which Program item DR1 adds")
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
			launched, linked, err := chainHolders(t.ctx, t.tx, t.sight, t.sight.chainOf(ref))
			if err != nil {
				return err
			}
			if err := chainRefusal(tk, ref, launched, linked); err != nil {
				return err
			}
		}
		var n int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM (SELECT DISTINCT kind, ref FROM links WHERE ticket_id = ? AND deleted_at = '')", tk.ID).Scan(&n); err != nil {
			return err
		}
		if err := checkCount("links", n+1, MaxLinks); err != nil {
			return err
		}
		link := Link{ID: newID(), TicketID: tk.ID, Kind: l.Kind, Ref: ref, URL: l.URL, Note: l.Note, CreatedAt: t.now, CreatedBy: t.op.author(), Fingerprint: t.fp}
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO links (id, ticket_id, kind, ref, url, note, created_at, created_by, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			link.ID, link.TicketID, link.Kind, link.Ref, link.URL, link.Note, stamp(link.CreatedAt), link.CreatedBy, link.Fingerprint); err != nil {
			return err
		}
		t.created(link.ID, "")
		value, err := encodeValue(linkAdded{Link: link, Fingerprint: link.Fingerprint})
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

// releaseRuns removes the run links of the ticket, undeleted in this
// operation, whose run's chain another ticket holds now (one linked a run
// of it while this one was deleted, or launched one): one remove row
// each, so a chain belongs to one live ticket at most. Its other links
// come back as they were.
func (t *tx) releaseRuns(tk Ticket) error {
	links, err := linksOf(t.ctx, t.tx, tk.ID)
	if err != nil {
		return err
	}
	other := func(h ChainHolder) bool { return h.TicketID != tk.ID }
	for _, l := range links {
		if l.Kind != LinkRun {
			continue
		}
		launched, linked, err := chainHolders(t.ctx, t.tx, t.sight, t.sight.chainOf(l.Ref))
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(launched, other) && !slices.ContainsFunc(linked, other) {
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
	rows, err := q.QueryContext(ctx, "SELECT "+linkColumnsOf(q)+" FROM links WHERE ticket_id = ? AND deleted_at = ''", ticketID)
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

// linkColumns is a link's row as scanLink reads it; linkColumnsV1 is the
// same of a version 1 database, which has no candidate, verified or
// fingerprint and reads them empty, as the migration leaves them.
const (
	linkColumns   = "id, ticket_id, kind, ref, url, note, candidate, verified, created_at, created_by, deleted_at, fingerprint"
	linkColumnsV1 = "id, ticket_id, kind, ref, url, note, '', '', created_at, created_by, deleted_at, ''"
)

// linkColumnsOf is the link columns of the schema q reads (schemaOf).
func linkColumnsOf(q queryer) string {
	if schemaOf(q) < 2 {
		return linkColumnsV1
	}
	return linkColumns
}

// scanLink reads a link's row.
func scanLink(row interface{ Scan(...any) error }) (Link, error) {
	var l Link
	var verified, created, deleted string
	if err := row.Scan(&l.ID, &l.TicketID, &l.Kind, &l.Ref, &l.URL, &l.Note, &l.Candidate, &verified, &created, &l.CreatedBy, &deleted, &l.Fingerprint); err != nil {
		return Link{}, err
	}
	if verified != "" {
		l.Verified = json.RawMessage(verified)
	}
	l.CreatedAt, l.DeletedAt = unstamp(created), unstamp(deleted)
	return l, nil
}
