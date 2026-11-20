package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	mine     map[string]bool   // this store's tags, every one it has joined under
	views    map[string][]byte // the batch's views, by view digest
	keys     map[string]string // the projects' registry keys, by project id
	applied  int
	heldNow  []string
	touched  []touched
	searched []searchEntry
	sets     map[[2]string]bool // (ticket, field) whose set members to project again
	group    map[string]bool    // tickets whose counterpart group to merge again
	seq      int64
}

// ApplyRemote applies another replica's rows (as ChangesFrom gives them:
// each with its raw actor, its origin and, for a plan's create, its view
// digest), with the views their plans' creates name and each project's
// registry key, in one transaction. The batch is refused whole, nothing
// applied, for a row that does not replicate (ErrNotReplicated), one with
// no origin or a canonical actor past MaxRemoteActor (ErrBadActor), a
// store that has joined no workspace (ErrNotJoined), a row of a project
// held here that this store has not shared (ErrNotShared), a view that is
// not the one its row names (ErrBadView), and past the held rows' bounds
// (ErrHeldFull). Then OnCommit and OnChange, as for a local commit.
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
		a := &applier{ctx: ctx, tx: dbtx, now: now.UTC(), views: views, keys: keys, sets: map[[2]string]bool{}, group: map[string]bool{}}
		if a.replica, err = activeReplica(ctx, dbtx); err != nil {
			return err
		}
		if a.replica.tag == "" {
			return ErrNotJoined
		}
		if a.mine, err = a.tags(); err != nil {
			return err
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
	}
	return nil
}

// hasControl reports whether s holds a control character.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// tags is every tag this store has joined under.
func (a *applier) tags() (map[string]bool, error) {
	tags, err := readTexts(a.ctx, a.tx, "SELECT tag FROM replica_tags")
	out := map[string]bool{}
	for _, t := range tags {
		out[t] = true
	}
	return out, err
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
// own share makes shared (none, pending, completing or refused).
func (a *applier) checkShared(rows []ChangeRow) error {
	seen := map[string]bool{}
	for _, c := range rows {
		p := projectOfRow(c)
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

// local reports whether the row is this store's own (its origin one of
// this store's tags): such a row is held with its raw actor.
func (a *applier) local(c ChangeRow) bool { return a.mine[c.Origin] }

// stored is the actor a row is kept under here: its raw actor for a row
// of this store's own, else qualified by its origin.
func (a *applier) stored(c ChangeRow) string {
	if a.local(c) {
		return c.Actor
	}
	return Canon(c.Actor, c.Origin)
}

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

// offer applies the row or holds it.
func (a *applier) offer(c ChangeRow) error {
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
	var rows, bytes int64
	if err := a.tx.QueryRowContext(a.ctx, "SELECT COUNT(*), COALESCE(SUM(LENGTH(row)), 0) FROM held_rows").Scan(&rows, &bytes); err != nil {
		return err
	}
	var already int
	if err := a.tx.QueryRowContext(a.ctx, "SELECT COUNT(*) FROM held_rows WHERE row = ?", string(encoded)).Scan(&already); err != nil {
		return err
	}
	if already == 0 {
		var bodies int64
		if err := a.tx.QueryRowContext(a.ctx, "SELECT COALESCE(SUM(LENGTH(body)), 0) FROM held_bodies").Scan(&bodies); err != nil {
			return err
		}
		if rows >= MaxHeldRows || bytes+bodies+int64(len(encoded)) > MaxHeldBytes {
			return fmt.Errorf("%w: %d held", ErrHeldFull, rows)
		}
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
			if _, err := a.tx.ExecContext(a.ctx, "INSERT OR IGNORE INTO held_bodies (digest, body) VALUES (?, ?)", c.ViewDigest, view); err != nil {
				return err
			}
		}
	}
	if len(a.heldNow) < MaxHeldWords && !slices.Contains(a.heldNow, h.why) {
		a.heldNow = append(a.heldNow, cutBytes(h.why, MaxHeldWord))
	}
	return nil
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
	remote := 0
	if !a.local(c) {
		remote = 1
	}
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
	if e, ok := searchedBy(c); ok && !slices.Contains(a.searched, e) {
		a.searched = append(a.searched, e)
	}
	if err := a.touch(c); err != nil {
		return err
	}
	if c.Op != OpCreate {
		return nil
	}
	waiting := []string{c.Entity + ":" + c.EntityID}
	if c.Entity == EntityPlan {
		waiting = append(waiting, "view:"+c.ViewDigest)
	}
	return a.release(waiting...)
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
	var rows []ChangeRow
	for _, w := range waiting {
		got, err := a.takeHeld("WHERE waits_for = ?", w)
		if err != nil {
			return err
		}
		rows = append(rows, got...)
	}
	Order(rows)
	for _, c := range rows {
		if err := a.offer(c); err != nil {
			return err
		}
	}
	return nil
}

// takeHeld takes the held rows the condition picks out of held_rows.
func (a *applier) takeHeld(where string, args ...any) ([]ChangeRow, error) {
	rs, err := a.tx.QueryContext(a.ctx, "SELECT seq, row FROM held_rows "+where+" ORDER BY seq", args...)
	if err != nil {
		return nil, err
	}
	var seqs []int64
	var out []ChangeRow
	for rs.Next() {
		var seq int64
		var row string
		if err := rs.Scan(&seq, &row); err != nil {
			rs.Close()
			return nil, err
		}
		var c ChangeRow
		if err := json.Unmarshal([]byte(row), &c); err != nil {
			rs.Close()
			return nil, err
		}
		seqs = append(seqs, seq)
		out = append(out, c)
	}
	rs.Close()
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

// replayHeld offers every held row again, in the one order, until a pass
// applies nothing: a row whose wait the batch ended without naming it
// (a reference arrived by another route, a key given up) is applied.
func (a *applier) replayHeld() error {
	for {
		before := a.applied
		rows, err := a.takeHeld("")
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		Order(rows)
		words := a.heldNow
		for _, c := range rows {
			if err := a.offer(c); err != nil {
				return err
			}
		}
		a.heldNow = words
		if a.applied == before {
			return nil
		}
	}
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
	if a.applied > 0 {
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
