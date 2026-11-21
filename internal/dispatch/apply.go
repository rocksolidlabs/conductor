package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Applying another replica's rows (docs/plans/sync-seams.md, OS-1a,
// "Applying another replica's rows"): a batch is applied in one
// transaction, in the one order, each row kept with its actor qualified
// by its origin and each field it names projected from the whole log by
// the field's class, so two stores holding the same rows hold the same
// row tables whatever batches they arrived in. A row this store cannot
// apply yet (its entity, a reference or its project not here, a number
// another ticket holds, a key a local project holds) is held, untouched,
// and applied once what it waits for arrives: no batch of a correct
// provider fails for good.

// RemoteApplied is what ApplyRemote did: how many rows it applied, the held
// rows' words (at most MaxHeldWords, each at most MaxHeldWord bytes) and
// one line for the page.
type RemoteApplied struct {
	Applied int      `json:"applied"`
	Held    []string `json:"held,omitempty"`
	Words   string   `json:"words,omitempty"`
}

// The bounds of what is held, and of what RemoteApplied names of it.
const (
	MaxHeldRows  = 10_000
	MaxHeldBytes = 64 << 20
	MaxHeldWords = 50
	MaxHeldWord  = 200
)

// Errors of ApplyRemote: a batch refused whole, nothing of it applied.
var (
	ErrNotReplicated = errors.New("dispatch: a row that never leaves its machine")
	ErrNotShared     = errors.New("dispatch: a row of a project this machine has not shared")
	ErrHeldFull      = errors.New("dispatch: too many changes from other machines wait for what has not arrived")
	ErrBadView       = errors.New("dispatch: a revision's view is not the view its row names")
)

// heldWhy is a held row's reason: why in words, and the entity it waits
// for (kind:id), "" for none (a row only a person's change releases).
type heldWhy struct {
	why, waitsFor string
}

// applier is one ApplyRemote's transaction and what it has done.
type applier struct {
	ctx      context.Context
	tx       *sql.Tx
	now      time.Time
	replica  replicaRow
	mine     map[string]bool   // the tags this store's instance joined under
	inherit  map[string]bool   // the tags of the instance a restore copied it from
	views    map[string][]byte // the batch's views, by view digest
	keys     map[string]string // the projects' registry keys, by project id
	applied  int
	heldNow  []string
	touched  []touched
	searched []searchEntry
	sets     map[[2]string]bool // (ticket, field) whose set members to project again
	group    map[string]bool    // tickets whose counterpart group to merge again
	seq      int64
	// waiting is what the batch made that held rows may wait for (kind:id,
	// view:<digest>, key:<key>), offered again at its end (replayHeld);
	// replayKeys says the call may have freed a key (a start's, a rekey's:
	// a call with no rows), its held projects' keys looked at again.
	waiting    map[string]bool
	replayKeys bool
	// held is held_rows' count, bytes and rows' identities, read once by
	// the first hold of the call and kept as rows are held and taken.
	held *heldTally
	// taken counts the held rows the call took (the store's onHeldTaken).
	taken int
	// edges says the call applied a row of an invariant's edge (a parent,
	// a tombstone, a relation, a link): only then are they reconciled.
	// onReconcile is the store's test seam on that replay.
	edges       bool
	onReconcile func()
}

// heldTally is held_rows as one call's holds count it.
type heldTally struct {
	rows, bytes, bodies int64
	ids                 map[string]bool
}

// ApplyRemote applies another replica's rows (as ChangesFrom gives them:
// each with its raw actor, its origin and, for a plan's create, its view
// digest), with the views their plans' creates name and each project's
// registry key, in one transaction. The batch is refused whole, nothing
// applied, for a row that does not replicate (ErrNotReplicated), one with
// no origin or a canonical actor past MaxRemoteActor (ErrBadActor), a
// store that has joined no workspace (ErrNotJoined), a row of a project
// held here that this store has not shared (ErrNotShared), a view that is
// not the one its row names (ErrBadView), a row naming one of this
// store's own tags as its origin that is not one of this store's own rows
// (ErrBadActor), and past the held rows' bounds (ErrHeldFull). Every row
// applied is kept as another machine's; this store's own rows handed back
// are dropped. Then OnCommit and OnChange, as for a local commit.
func (s *Store) ApplyRemote(ctx context.Context, rows []ChangeRow, views map[string][]byte, keys map[string]string) (RemoteApplied, error) {
	if len(rows) > MaxChangesPage {
		return RemoteApplied{}, fmt.Errorf("dispatch: a batch of %d rows, more than %d", len(rows), MaxChangesPage)
	}
	for _, c := range rows {
		if err := checkRemote(c); err != nil {
			return RemoteApplied{}, err
		}
	}
	for digest, view := range views {
		if len(view) > MaxPlanBody || sha256Hex(string(view)) != digest {
			return RemoteApplied{}, fmt.Errorf("%w: %s", ErrBadView, digest)
		}
	}
	var out RemoteApplied
	var changed []Change
	s.mu.Lock()
	err := func() error {
		switch {
		case s.closed:
			return ErrClosed
		case s.readOnly || s.db == nil:
			return fmt.Errorf("%w: %s", ErrUnavailable, s.why)
		}
		dbtx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer dbtx.Rollback()
		now := time.Now()
		if s.clock != nil {
			now = s.clock()
		}
		a := &applier{ctx: ctx, tx: dbtx, now: now.UTC(), views: views, keys: keys, sets: map[[2]string]bool{}, group: map[string]bool{},
			waiting: map[string]bool{}, replayKeys: len(rows) == 0, onReconcile: s.onReconcile}
		if a.replica, err = activeReplica(ctx, dbtx); err != nil {
			return err
		}
		if a.replica.tag == "" {
			return ErrNotJoined
		}
		if a.mine, a.inherit, err = a.tags(); err != nil {
			return err
		}
		// Whatever comes in through the apply is from elsewhere: a row
		// naming a tag this store's instance joined under is this store's
		// own row handed back (another machine hands on what it received),
		// dropped, or else refused; never stored as this machine's. A tag
		// inherited from the instance a restore copied (offer) is another
		// machine's.
		for _, c := range rows {
			if !a.mine[c.Origin] {
				continue
			}
			if own, err := a.ownRow(c); err != nil {
				return err
			} else if !own {
				return fmt.Errorf("%w: a row from elsewhere names this machine's own origin %s", ErrBadActor, c.Origin)
			}
		}
		if err := a.checkShared(rows); err != nil {
			return err
		}
		batch := slices.Clone(rows)
		Order(batch)
		for _, c := range batch {
			if err := a.offer(c); err != nil {
				return err
			}
		}
		if err := a.replayHeld(); err != nil {
			return err
		}
		if s.onHeldTaken != nil {
			s.onHeldTaken(a.taken)
		}
		if err := a.finish(); err != nil {
			return err
		}
		if err := dbtx.Commit(); err != nil {
			return err
		}
		out = a.result()
		changed = a.changes()
		return nil
	}()
	if err == nil && len(changed) > 0 {
		s.committed(changed)
	}
	s.mu.Unlock()
	if err != nil {
		return RemoteApplied{}, cancelledAs(ctx, err)
	}
	if len(changed) > 0 {
		s.notify(changed)
	}
	return out, nil
}

// checkRemote refuses a row no replica could have handed out.
func checkRemote(c ChangeRow) error {
	switch {
	case !Replicates(c):
		return fmt.Errorf("%w: %s %s %s", ErrNotReplicated, c.Entity, c.Op, c.Field)
	case !ValidTag(c.Origin), c.RecoveredBy != "" && !ValidTag(c.RecoveredBy):
		return fmt.Errorf("%w: a row from another machine names its origin", ErrBadActor)
	case c.Actor == "" || c.Actor[0] == '@' || len(Canon(c.Actor, c.Origin)) > MaxRemoteActor || !utf8.ValidString(c.Actor) || hasControl(c.Actor):
		return fmt.Errorf("%w: %q", ErrBadActor, c.Actor)
	case !OpIDPattern.MatchString(c.OpID) || c.N < 0 || c.EntityID == "":
		return fmt.Errorf("dispatch: a row from another machine without its identity")
	case len(c.Value) > MaxChangeValue || len(c.Prior) > MaxChangeValue:
		return fmt.Errorf("dispatch: a row from another machine past %d bytes", MaxChangeValue)
	case c.At.IsZero() || !c.At.Before(MaxRemoteAt):
		return fmt.Errorf("dispatch: a row from another machine dated %s, not before %s", c.At.Format(time.RFC3339), MaxRemoteAt.Format(time.DateOnly))
	}
	return nil
}

// MaxRemoteAt bounds a row from elsewhere's time: a row from a clock set at
// the end of time would order past every edit made here, and an edit made
// after it would be stamped past what a stamp holds.
var MaxRemoteAt = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// hasControl reports whether s holds a control character.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// tags is the tags this store's instance joined under (mine), and the
// tags of the instance a restore copied it from (inherit): an export
// carries the tags it was joined under, and rows that instance wrote after
// the export reach the restored store from elsewhere.
func (a *applier) tags() (mine, inherit map[string]bool, err error) {
	mine, inherit = map[string]bool{}, map[string]bool{}
	var instance string
	if err := a.tx.QueryRowContext(a.ctx, "SELECT COALESCE((SELECT id FROM store_instance), '')").Scan(&instance); err != nil {
		return nil, nil, err
	}
	rs, err := a.tx.QueryContext(a.ctx, "SELECT tag, instance FROM replica_tags")
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var tag, of string
		if err := rs.Scan(&tag, &of); err != nil {
			return nil, nil, err
		}
		if of == instance {
			mine[tag] = true
		} else {
			inherit[tag] = true
		}
	}
	return mine, inherit, rs.Err()
}

// projectOfRow is the project a row belongs to: a project's own id for a
// project's rows.
func projectOfRow(c ChangeRow) string {
	if c.Entity == EntityProject {
		return c.EntityID
	}
	return c.ProjectID
}

// checkShared refuses a batch naming a project held here that only its
// own share makes shared (none, pending, completing or refused), or a row
// of a kind that belongs to a project naming none.
func (a *applier) checkShared(rows []ChangeRow) error {
	seen := map[string]bool{}
	for _, c := range rows {
		p := projectOfRow(c)
		if p == "" && c.Entity != EntityPerson {
			return fmt.Errorf("%w: a row of %s %s names no project", ErrNotShared, c.Entity, c.EntityID)
		}
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		var state string
		switch err := a.tx.QueryRowContext(a.ctx, "SELECT share_state FROM projects WHERE id = ?", p).Scan(&state); {
		case isNoRows(err):
		case err != nil:
			return err
		case state != "shared" && state != "dormant":
			return fmt.Errorf("%w: %s", ErrNotShared, p)
		}
	}
	return nil
}

// ownRow reports whether the log holds the row as this store's own: its
// raw actor, op id and number on a row written here.
func (a *applier) ownRow(c ChangeRow) (bool, error) {
	var n int
	err := a.tx.QueryRowContext(a.ctx, "SELECT COUNT(*) FROM changes WHERE actor = ? AND op_id = ? AND n = ? AND remote = 0", c.Actor, c.OpID, c.N).Scan(&n)
	return n > 0, err
}

// stored is the actor a row is kept under here: qualified by its origin,
// every row applied being another machine's.
func (a *applier) stored(c ChangeRow) string { return Canon(c.Actor, c.Origin) }

// holds reports whether the log holds the row's identity.
func (a *applier) holds(c ChangeRow) (bool, error) {
	var n int
	err := a.tx.QueryRowContext(a.ctx, "SELECT COUNT(*) FROM changes WHERE actor = ? AND op_id = ? AND n = ?", a.stored(c), c.OpID, c.N).Scan(&n)
	return n > 0, err
}

// exists reports whether the store holds the entity kind:id names.
func (a *applier) exists(kind, id string) (bool, error) {
	var query string
	switch kind {
	case EntityProject:
		query = "SELECT COUNT(*) FROM projects WHERE id = ?"
	case EntityTicket:
		query = "SELECT COUNT(*) FROM tickets WHERE id = ?"
	case EntityComment:
		query = "SELECT COUNT(*) FROM comments WHERE id = ?"
	case EntityLabel:
		query = "SELECT COUNT(*) FROM labels WHERE id = ?"
	case EntityMilestone:
		query = "SELECT COUNT(*) FROM milestones WHERE id = ?"
	case EntityView:
		query = "SELECT COUNT(*) FROM views WHERE id = ?"
	case EntityPerson:
		query = "SELECT COUNT(*) FROM people WHERE name = ?"
	case EntityPlan:
		query = "SELECT COUNT(*) FROM plans WHERE id = ?"
	default:
		return false, nil
	}
	var n int
	err := a.tx.QueryRowContext(a.ctx, query, id).Scan(&n)
	return n > 0, err
}

// offer applies the row or holds it; a row naming one of this store's own
// tags (its own row handed back, or one held before this store joined
// under that tag) is dropped, and so is a row of an inherited tag the log
// holds as written here; any other row of an inherited tag is another
// machine's. A row bound to a project it does not name,
// or to one this store has not shared (bound), refuses the batch.
func (a *applier) offer(c ChangeRow) error {
	if a.mine[c.Origin] {
		return nil
	}
	if a.inherit[c.Origin] {
		if own, err := a.ownRow(c); err != nil || own {
			return err
		}
	}
	if err := a.bound(c); err != nil {
		return err
	}
	if dup, err := a.holds(c); err != nil || dup {
		return err
	}
	if h, err := a.waits(c); err != nil {
		return err
	} else if h != nil {
		return a.hold(c, *h)
	}
	return a.apply(c)
}

// bound refuses (ErrNotShared) a row whose entity, as this store holds it,
// belongs to a project other than the one the row names, or to a project
// held here that is not shared: a create by the project its value names
// (a comment's or a revision's through the ticket it names, which must be
// the row's ticket), any other row by the stored entity's project; a
// ticket's relation from another ticket than the row's, or to a ticket of
// a project not shared here; a label added from another project. What is
// not held here yet is judged when the row is offered again.
func (a *applier) bound(c ChangeRow) error {
	if c.Entity == EntityPerson {
		return nil
	}
	named := projectOfRow(c)
	project, known, err := a.boundProject(c)
	if err != nil {
		return err
	}
	if known {
		if project != named {
			return fmt.Errorf("%w: %s %s belongs to project %s, not %s", ErrNotShared, c.Entity, c.EntityID, project, named)
		}
		if err := a.shareable(project); err != nil {
			return err
		}
	}
	relation := c.Entity == EntityTicket && c.Op == OpAdd && c.Field == "relations"
	if relation {
		var rel Relation
		if err := json.Unmarshal([]byte(c.Value), &rel); err != nil {
			return err
		}
		if rel.FromID != c.EntityID {
			return fmt.Errorf("%w: a relation of %s from %s", ErrNotShared, c.EntityID, rel.FromID)
		}
	}
	// Every reference the row carries (Refs, the one list of them): a
	// referenced entity held here is of the row's project, but for a
	// relation's far end, which may be of another project that is shared.
	for _, ref := range Refs(c) {
		kind, id := splitRef(ref)
		if kind == c.Entity && id == c.EntityID {
			continue
		}
		of, held, err := a.storedProject(kind, id)
		if err != nil {
			return err
		}
		if !held || of == named {
			continue
		}
		if relation && kind == EntityTicket {
			if err := a.shareable(of); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("%w: %s's %s %s refers to %s %s of project %s", ErrNotShared, c.Entity, c.EntityID, c.Field, kind, id, of)
	}
	return nil
}

// shareable refuses (ErrNotShared) a project held here that is neither
// shared nor dormant; one not held here is not refused (its rows wait).
func (a *applier) shareable(project string) error {
	var state string
	switch err := a.tx.QueryRowContext(a.ctx, "SELECT share_state FROM projects WHERE id = ?", project).Scan(&state); {
	case isNoRows(err):
		return nil
	case err != nil:
		return err
	case state != "shared" && state != "dormant":
		return fmt.Errorf("%w: %s", ErrNotShared, project)
	}
	return nil
}

// boundProject is the project the row touches as this store holds it,
// and whether that is known now: a project's own id; a create's from its
// value (its id the row's entity; a comment's or a revision's ticket the
// row's ticket, its project that ticket's as held here); any other row's
// the stored entity's.
func (a *applier) boundProject(c ChangeRow) (string, bool, error) {
	if c.Entity == EntityProject {
		if c.ProjectID != "" && c.ProjectID != c.EntityID {
			return c.ProjectID, true, nil
		}
		return c.EntityID, true, nil
	}
	if c.Op != OpCreate {
		return a.storedProject(c.Entity, c.EntityID)
	}
	var v struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
		TicketID  string `json:"ticketId"`
	}
	if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
		return "", false, err
	}
	if v.ID != c.EntityID {
		return "", false, fmt.Errorf("%w: a create of %s %s whose value makes %s", ErrNotShared, c.Entity, c.EntityID, v.ID)
	}
	switch c.Entity {
	case EntityComment, EntityPlan:
		if v.TicketID != c.TicketID {
			return "", false, fmt.Errorf("%w: a create of %s %s on ticket %s, its row's %s", ErrNotShared, c.Entity, c.EntityID, v.TicketID, c.TicketID)
		}
		return a.storedProject(EntityTicket, v.TicketID)
	}
	return v.ProjectID, true, nil
}

// storedProject is the project of the entity as this store holds it, and
// whether it is held.
func (a *applier) storedProject(kind, id string) (string, bool, error) {
	var query string
	switch kind {
	case EntityProject:
		query = "SELECT id FROM projects WHERE id = ?"
	case EntityTicket:
		query = "SELECT project_id FROM tickets WHERE id = ?"
	case EntityComment:
		query = "SELECT t.project_id FROM comments c JOIN tickets t ON t.id = c.ticket_id WHERE c.id = ?"
	case EntityPlan:
		query = "SELECT t.project_id FROM plans p JOIN tickets t ON t.id = p.ticket_id WHERE p.id = ?"
	case EntityLabel:
		query = "SELECT project_id FROM labels WHERE id = ?"
	case EntityMilestone:
		query = "SELECT project_id FROM milestones WHERE id = ?"
	case EntityView:
		query = "SELECT project_id FROM views WHERE id = ?"
	default:
		return "", false, nil
	}
	var p string
	switch err := a.tx.QueryRowContext(a.ctx, query, id).Scan(&p); {
	case isNoRows(err):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return p, true, nil
}

// waits is why the row cannot be applied yet, nil when it can.
func (a *applier) waits(c ChangeRow) (*heldWhy, error) {
	project := projectOfRow(c)
	if project != "" {
		there, err := a.exists(EntityProject, project)
		if err != nil {
			return nil, err
		}
		if !there {
			if c.Entity == EntityProject && c.Op == OpCreate {
				return a.receivable(c)
			}
			return &heldWhy{fmt.Sprintf("held: a change of project %s, whose first change has not arrived", project), EntityProject + ":" + project}, nil
		}
	}
	if c.Op != OpCreate {
		there, err := a.exists(c.Entity, c.EntityID)
		if err != nil {
			return nil, err
		}
		if !there {
			return &heldWhy{fmt.Sprintf("held: %s %s's first change has not arrived", c.Entity, c.EntityID), c.Entity + ":" + c.EntityID}, nil
		}
	}
	for _, ref := range Refs(c) {
		kind, id := splitRef(ref)
		there, err := a.exists(kind, id)
		if err != nil {
			return nil, err
		}
		if !there {
			return &heldWhy{fmt.Sprintf("held: %s %s's change waits for %s %s", c.Entity, c.EntityID, kind, id), ref}, nil
		}
	}
	switch {
	case c.Op == OpCreate && c.Entity == EntityTicket:
		var v ticketCreated
		if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
			return nil, err
		}
		var other string
		switch err := a.tx.QueryRowContext(a.ctx, "SELECT key FROM tickets WHERE project_id = ? AND number = ? AND id != ?", v.ProjectID, v.Number, v.ID).Scan(&other); {
		case err == nil:
			return &heldWhy{fmt.Sprintf("%s was numbered twice: kept on the machine that numbered it first", other), ""}, nil
		case !isNoRows(err):
			return nil, err
		}
	case c.Op == OpCreate && c.Entity == EntityPlan:
		if _, ok := a.views[c.ViewDigest]; !ok {
			there, err := a.heldBody(c.ViewDigest)
			if err != nil {
				return nil, err
			}
			if !there {
				return &heldWhy{fmt.Sprintf("held: revision %s's view has not arrived", c.EntityID), "view:" + c.ViewDigest}, nil
			}
		}
	}
	return nil, nil
}

// splitRef is a reference's kind and id.
func splitRef(ref string) (kind, id string) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == ':' {
			return ref[:i], ref[i+1:]
		}
	}
	return ref, ""
}

// receivable is why a project's create cannot be received, nil when it
// can: its registry key held by a project of this machine's own.
func (a *applier) receivable(c ChangeRow) (*heldWhy, error) {
	key, err := a.registryKey(c)
	if err != nil {
		return nil, err
	}
	var n int
	if err := a.tx.QueryRowContext(a.ctx, "SELECT COUNT(*) FROM projects WHERE key = ? AND id != ?", key, c.EntityID).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return &heldWhy{fmt.Sprintf("%s is this machine's own project: give it another key to receive the workspace's", key), "key:" + key}, nil
	}
	return nil, nil
}

// registryKey is the key the workspace's registry holds for the project
// a create row makes: the batch's, else the key the row was made under.
func (a *applier) registryKey(c ChangeRow) (string, error) {
	if k := a.keys[c.EntityID]; k != "" {
		return k, nil
	}
	var v projectCreated
	if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
		return "", err
	}
	return v.Key, nil
}

// heldBody reports whether held_bodies holds the view.
func (a *applier) heldBody(digest string) (bool, error) {
	var n int
	err := a.tx.QueryRowContext(a.ctx, "SELECT COUNT(*) FROM held_bodies WHERE digest = ?", digest).Scan(&n)
	return n > 0, err
}

// hold keeps the row in held_rows, untouched, with why and what it waits
// for, and the view it came with in held_bodies; once: a row held already
// is not held twice. Past MaxHeldRows or MaxHeldBytes the batch is
// refused (ErrHeldFull).
func (a *applier) hold(c ChangeRow, h heldWhy) error {
	encoded, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tally, err := a.tally()
	if err != nil {
		return err
	}
	if !tally.ids[string(encoded)] {
		if tally.rows >= MaxHeldRows || tally.bytes+tally.bodies+int64(len(encoded)) > MaxHeldBytes {
			return fmt.Errorf("%w: %d held", ErrHeldFull, tally.rows)
		}
		tally.rows, tally.bytes = tally.rows+1, tally.bytes+int64(len(encoded))
		tally.ids[string(encoded)] = true
		project := projectOfRow(c)
		if key, err := a.projectWord(project); err == nil && key != "" {
			project = key
		}
		if _, err := a.tx.ExecContext(a.ctx, "INSERT INTO held_rows (project_key, why, waits_for, row, held_at) VALUES (?, ?, ?, ?, ?)",
			project, h.why, h.waitsFor, string(encoded), stamp(a.now)); err != nil {
			return err
		}
	}
	if c.Entity == EntityPlan && c.Op == OpCreate {
		if view, ok := a.views[c.ViewDigest]; ok {
			res, err := a.tx.ExecContext(a.ctx, "INSERT OR IGNORE INTO held_bodies (digest, body) VALUES (?, ?)", c.ViewDigest, view)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				tally.bodies += int64(len(view))
			}
		}
	}
	if len(a.heldNow) < MaxHeldWords && !slices.Contains(a.heldNow, h.why) {
		a.heldNow = append(a.heldNow, cutBytes(h.why, MaxHeldWord))
	}
	return nil
}

// tally is held_rows' count, bytes and identities and held_bodies' bytes,
// read once by the call's first hold.
func (a *applier) tally() (*heldTally, error) {
	if a.held != nil {
		return a.held, nil
	}
	t := &heldTally{ids: map[string]bool{}}
	rs, err := a.tx.QueryContext(a.ctx, "SELECT row FROM held_rows")
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var row string
		if err := rs.Scan(&row); err != nil {
			rs.Close()
			return nil, err
		}
		t.rows, t.bytes = t.rows+1, t.bytes+int64(len(row))
		t.ids[row] = true
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := a.tx.QueryRowContext(a.ctx, "SELECT COALESCE(SUM(LENGTH(body)), 0) FROM held_bodies").Scan(&t.bodies); err != nil {
		return nil, err
	}
	a.held = t
	return t, nil
}

// projectWord is the project's key when this store holds it, "" else.
func (a *applier) projectWord(id string) (string, error) {
	var key string
	err := a.tx.QueryRowContext(a.ctx, "SELECT key FROM projects WHERE id = ?", id).Scan(&key)
	if isNoRows(err) {
		return "", nil
	}
	return key, err
}

// apply keeps the row, projects what it names, and releases what waited
// for an entity it made.
func (a *applier) apply(c ChangeRow) error {
	remote := 1
	res, err := a.tx.ExecContext(a.ctx, `INSERT INTO changes (entity, entity_id, ticket_id, project_id, field, op, value, prior, at, actor, by, op_id, n,
		origin, recovered_by, remote, output, prior_output) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Entity, c.EntityID, c.TicketID, c.ProjectID, c.Field, c.Op, c.Value, c.Prior, stamp(c.At), a.stored(c), c.By, c.OpID, c.N,
		c.Origin, c.RecoveredBy, remote, c.Output, c.PriorOutput)
	if err != nil {
		return err
	}
	if a.seq, err = res.LastInsertId(); err != nil {
		return err
	}
	a.applied++
	// A dormant project's row from another machine: the workspace holds it
	// again, and it is shared.
	if p := projectOfRow(c); p != "" {
		if _, err := a.tx.ExecContext(a.ctx, "UPDATE projects SET share_state = 'shared', share_words = '' WHERE id = ? AND share_state = 'dormant'", p); err != nil {
			return err
		}
	}
	if err := a.project(c); err != nil {
		return err
	}
	if c.Entity == EntityTicket {
		switch c.Field {
		case "parent_id", "deleted_at", "relations", "links":
			a.edges = true
		}
	}
	if e, ok := searchedBy(c); ok && !slices.Contains(a.searched, e) {
		a.searched = append(a.searched, e)
	}
	if err := a.touch(c); err != nil {
		return err
	}
	if c.Op != OpCreate {
		return nil
	}
	// What waited for the entity it made is offered again at the batch's
	// end (replayHeld), once for all it made.
	a.waiting[c.Entity+":"+c.EntityID] = true
	if c.Entity == EntityPlan {
		a.waiting["view:"+c.ViewDigest] = true
	}
	return nil
}

// touch marks the entity a row names for its rev, OnChange and, for a
// relation, its far end.
func (a *applier) touch(c ChangeRow) error {
	add := func(entity, id, ticket, project string) error {
		key, err := a.projectWord(project)
		if err != nil {
			return err
		}
		for _, e := range a.touched {
			if e.entity == entity && e.id == id {
				return nil
			}
		}
		a.touched = append(a.touched, touched{entity, id, ticket, key})
		return nil
	}
	if err := add(c.Entity, c.EntityID, c.TicketID, projectOfRow(c)); err != nil {
		return err
	}
	if c.Entity == EntityTicket && c.Field == "relations" {
		var r Relation
		if json.Unmarshal([]byte(c.Value), &r) == nil && r.ToID != "" {
			return add(EntityTicket, r.ToID, r.ToID, c.ProjectID)
		}
	}
	return nil
}

// release offers again, in the one order, every held row that waits for
// one of the entities named.
func (a *applier) release(waiting ...string) error {
	if len(waiting) == 0 {
		return nil
	}
	args := make([]any, len(waiting))
	for i, w := range waiting {
		args[i] = w
	}
	rows, err := a.takeHeld("WHERE waits_for IN (?"+strings.Repeat(", ?", len(waiting)-1)+")", args...)
	if err != nil {
		return err
	}
	Order(rows)
	for _, c := range rows {
		if err := a.offerHeld(c); err != nil {
			return err
		}
	}
	return nil
}

// offerHeld offers a row held before: one that what it waited for now
// shows bound to a project it does not name, or to one not shared here,
// is dropped, its batch long applied.
func (a *applier) offerHeld(c ChangeRow) error {
	err := a.offer(c)
	if errors.Is(err, ErrNotShared) {
		return nil
	}
	return err
}

// takeHeld takes the held rows the condition picks out of held_rows. A
// held row that is not a row from another machine (not one a batch's
// check would take, checkRemote: no build writes one, but a database is
// a file) is left held as it is, never offered nor written over, so it
// stops no batch.
func (a *applier) takeHeld(where string, args ...any) ([]ChangeRow, error) {
	rs, err := a.tx.QueryContext(a.ctx, "SELECT seq, row, waits_for FROM held_rows "+where+" ORDER BY seq", args...)
	if err != nil {
		return nil, err
	}
	var seqs []int64
	var out []ChangeRow
	for rs.Next() {
		var seq int64
		var row, waits string
		if err := rs.Scan(&seq, &row, &waits); err != nil {
			rs.Close()
			return nil, err
		}
		var c ChangeRow
		if json.Unmarshal([]byte(row), &c) != nil || checkRemote(c) != nil {
			continue
		}
		// A project's create held for its key keeps the registry's key it
		// waits for, taken at its replay when the batch names none.
		if key, ok := strings.CutPrefix(waits, "key:"); ok && c.Entity == EntityProject && c.Op == OpCreate && a.keys[c.EntityID] == "" {
			if a.keys == nil {
				a.keys = map[string]string{}
			}
			a.keys[c.EntityID] = key
		}
		seqs = append(seqs, seq)
		out = append(out, c)
		if a.held != nil {
			a.held.rows, a.held.bytes = a.held.rows-1, a.held.bytes-int64(len(row))
			delete(a.held.ids, row)
		}
	}
	rs.Close()
	a.taken += len(out)
	if err := rs.Err(); err != nil {
		return nil, err
	}
	for _, seq := range seqs {
		if _, err := a.tx.ExecContext(a.ctx, "DELETE FROM held_rows WHERE seq = ?", seq); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// replayHeld offers again, in the one order, the held rows the batch can
// have released and no other: those waiting for what it made, and for a
// view it brought; on a call that may have freed a key (no rows: a
// start's, a rekey's), those waiting for a key no project of this
// machine's holds now. What they release in turn is offered again, pass
// after pass, until a pass releases nothing; nothing is read per row.
func (a *applier) replayHeld() error {
	for digest := range a.views {
		a.waiting["view:"+digest] = true
	}
	if a.replayKeys {
		keys, err := readTexts(a.ctx, a.tx, "SELECT DISTINCT waits_for FROM held_rows WHERE waits_for LIKE 'key:%'")
		if err != nil {
			return err
		}
		for _, w := range keys {
			var n int
			if err := a.tx.QueryRowContext(a.ctx, "SELECT COUNT(*) FROM projects WHERE key = ?", strings.TrimPrefix(w, "key:")).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				a.waiting[w] = true
			}
		}
	}
	words := a.heldNow
	defer func() { a.heldNow = words }()
	for len(a.waiting) > 0 {
		waiting := make([]string, 0, len(a.waiting))
		for w := range a.waiting {
			waiting = append(waiting, w)
		}
		slices.Sort(waiting)
		clear(a.waiting)
		if err := a.release(waiting...); err != nil {
			return err
		}
	}
	return nil
}

// finish projects the sets and the counterpart groups the batch touched,
// reconciles the invariants, rewrites the search entries and bumps the
// revs.
func (a *applier) finish() error {
	for k := range a.sets {
		if err := a.projectSet(k[0], k[1]); err != nil {
			return err
		}
	}
	for ticket := range a.group {
		if err := a.mergeCounterpart(ticket); err != nil {
			return err
		}
	}
	// The invariants are replayed only after a row of their edges: a batch
	// of titles, comments and other fields cannot change their standing.
	// Their replay reads every edge row of the store, since an edge's
	// standing turns on every other edge in the one order (a cycle through
	// any tickets, a run held by any earlier link), and a bound to the
	// tickets the batch touched would need that walk to find them.
	if a.edges {
		if a.onReconcile != nil {
			a.onReconcile()
		}
		if err := a.reconcile(); err != nil {
			return err
		}
	}
	for _, e := range a.searched {
		if err := e.index(a.ctx, a.tx); err != nil {
			return err
		}
	}
	for _, e := range a.touched {
		rt, ok := revTables[e.entity]
		if !ok {
			continue
		}
		stmt := "UPDATE " + rt.table + " SET rev = rev + 1 WHERE id = ?"
		args := []any{e.id}
		if rt.updated {
			stmt = "UPDATE " + rt.table + " SET rev = rev + 1, updated_at = ? WHERE id = ?"
			args = []any{stamp(a.now), e.id}
		}
		if _, err := a.tx.ExecContext(a.ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

// result is what the batch did, for the provider.
func (a *applier) result() RemoteApplied {
	out := RemoteApplied{Applied: a.applied, Held: a.heldNow}
	switch {
	case len(a.heldNow) > 0:
		out.Words = fmt.Sprintf("%d changes applied; some wait for what has not arrived", a.applied)
	case a.applied > 0:
		out.Words = fmt.Sprintf("%d changes applied", a.applied)
	}
	return out
}

// changes is what the batch changed, for OnCommit and OnChange.
func (a *applier) changes() []Change {
	out := make([]Change, 0, len(a.touched))
	for _, e := range a.touched {
		out = append(out, Change{Seq: a.seq, Entity: e.entity, ID: e.id, Ticket: e.ticket, Project: e.project})
	}
	return out
}
