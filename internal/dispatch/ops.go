package dispatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Every write is a typed operation (Apply): the client mints an id, the
// store keeps the outcome under (actor, id), and the same id sent again
// with the same content gets the same receipt, never a second change;
// other content is a conflict. One transaction: the receipt is looked up,
// the entity's rev is compared with the one expected, the change is
// made, one changes row per field it set, added or removed, rev is
// bumped, the receipt is written, the commit, then OnChange. A refusal
// (the store's rules) is a failed receipt, the change undone under a
// savepoint and the receipt the transaction's only write (but an upload's
// lease, which its refusal drops: tx.onRefusal), so the same id answers it
// again and the write lock is held from the change to the receipt; a
// failure of the database itself, or a read-only store, writes nothing.

// OpIDPattern bounds an operation id a client mints: the shape of the
// ledger's command ids.
var OpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)

// Errors of an operation the store would not even look at: the API
// answers 400 invalid_request, as it answers a bad command id.
var (
	ErrBadOpID  = errors.New("dispatch: an operation id is 1 to 32 letters, digits, '.', '_', '~' or '-'")
	ErrBadActor = fmt.Errorf("dispatch: an operation names its actor in at most %d bytes, with no control character", MaxActor)
	ErrBadBy    = fmt.Errorf("dispatch: an operation's display name is at most %d characters, with no control character", MaxByName)
)

// Op is what a client sends with a change: who asks (Actor: the admin's
// operations are kept under admin, as the task commands are, and an
// attempt's under agent:<member>), the id it minted, the entity's
// revision it expects (0 for none), and the display name recorded as by
// on the change rows, which travels in the body as byName does for a
// broadcast. The actor is at most MaxActor bytes and the display name at
// most MaxByName characters, neither with a control character.
type Op struct {
	Actor       string
	ID          string
	ExpectedRev int64
	By          string
}

// ActorAdmin is the operator's actor: the admin's operations are kept
// under it, and an approval of a plan revision is its alone.
const ActorAdmin = "admin"

// author is the name a row the operation makes records as its creator
// (created_by): the display name, else the actor.
func (op Op) author() string {
	if op.By != "" {
		return op.By
	}
	return op.Actor
}

// check is what every operation must be before the store looks at it: an
// op id of OpIDPattern (ErrBadOpID), an actor within its rule
// (ErrBadActor), and an author, the display name or else the actor, within
// the display name's (ErrBadBy), so a row's created_by meets its bound: an
// actor within MaxActor always does.
func (op Op) check() error {
	switch {
	case !OpIDPattern.MatchString(op.ID):
		return ErrBadOpID
	case checkField("op.actor", "the actor", op.Actor) != nil:
		return ErrBadActor
	case checkField("op.by", "by", op.author()) != nil:
		return ErrBadBy
	}
	return nil
}

// The statuses of a receipt.
const (
	StatusDone   = "done"
	StatusFailed = "failed"
)

// The refusals a failed receipt names: the store's rules. read_only and
// op_conflict are answered without a row, the rest kept. key_taken is a
// project's create naming a key another project holds (the API's 409
// key_taken).
const (
	RefusalNotFound      = "not_found"
	RefusalInvalid       = "invalid"
	RefusalStaleRevision = "stale_revision"
	RefusalOpConflict    = "op_conflict"
	RefusalClosePolicy   = "close_policy"
	RefusalCycle         = "cycle"
	RefusalBound         = "bound"
	RefusalReadOnly      = "read_only"
	RefusalArchived      = "archived"
	RefusalRunLinked     = "run_linked"
	RefusalExpired       = "expired"
	RefusalKeyTaken      = "key_taken"
	// RefusalStalePlan is a plan revision whose approval no longer holds
	// for what is asked of it (Program item DS2): an approval of a
	// revision whose handed-over comment is edited or gone, a launch of
	// one whose ticket's intent changed since it was approved.
	RefusalStalePlan = "stale_plan"
)

// The words of an op_conflict refusal: the id was used with other
// content, or its operation's receipt is pruned and what it made keeps no
// fingerprint to compare a resend with (a link a version 1 store added).
const (
	wordsOpConflict = "the operation id was used before with other content"
	wordsTooOld     = "this operation is too old to compare: send it as a new one"
)

// The intents whose resend, its receipt pruned, is compared by the
// fingerprint kept with what the operation made (Replay): a plan
// revision's proposal, a launch, a link's add.
const (
	IntentPlanPropose  = "plan.propose"
	IntentTicketLaunch = "ticket.launch"
	IntentLinkAdd      = "link.add"
)

// Receipt is what an operation gets and what the store keeps under
// (actor, op id): the id and intent, the status (done, or failed with
// the refusal's name and its words), the subject's rev after the
// operation, the id and key of what a create made (an add's id is the
// row it made; a run_linked refusal's key is the ticket that holds the
// run), what a close_policy refusal lacks (Missing), and whether this
// answer is the one given before (Repeat). It carries no entity, which
// the caller reads after the commit, so no receipt grows with a
// description; it is at most MaxReceipt bytes as JSON.
type Receipt struct {
	OpID    string   `json:"opId"`
	Intent  string   `json:"intent"`
	Status  string   `json:"status"`
	Rev     int64    `json:"rev,omitempty"`
	ID      string   `json:"id,omitempty"`
	Key     string   `json:"key,omitempty"`
	Repeat  bool     `json:"repeat,omitempty"`
	Error   string   `json:"error,omitempty"`
	Refusal string   `json:"refusal,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

// Refusal is an operation refused by the store's rules: the code a
// failed receipt names, the words, for close_policy what is missing, and
// for run_linked the key of the ticket that holds the run (the receipt's
// Key). A change returns one; Apply keeps it as the failed receipt. Any
// other error a change returns is the database's, and nothing is
// written.
type Refusal struct {
	Code    string
	Words   string
	Missing []string
	Key     string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Words }

// refuse is a Refusal with the given code and words.
func refuse(code, format string, args ...any) error {
	return &Refusal{Code: code, Words: fmt.Sprintf(format, args...)}
}

// The entities a change row names. A set member (an assignee, label,
// relation, link or attachment) is a row of its ticket's field, not an
// entity of its own.
const (
	EntityProject   = "project"
	EntityTicket    = "ticket"
	EntityComment   = "comment"
	EntityLabel     = "label"
	EntityMilestone = "milestone"
	EntityView      = "view"
	EntityPerson    = "person"
	// EntityPlan is a ticket's plan revision and EntityLaunch a ticket's
	// launch (Program item DS2): their rows name their ticket and its
	// project, as a comment's do, and bump their own rev, never the
	// ticket's.
	EntityPlan   = "plan"
	EntityLaunch = "launch"
)

// The ops of a change row: a scalar field set (value and prior), a set
// member added or removed (prior naming the tags of the adds a remove
// removes), an entity created (value the row's fields) or deleted, a
// merge's repair (Repair; derived, never shipped) and a migration's
// derived value.
const (
	OpSet       = "set"
	OpAdd       = "add"
	OpRemove    = "remove"
	OpCreate    = "create"
	OpDelete    = "delete"
	OpReconcile = "reconcile"
	OpMigrate   = "migrate"
)

// ChangeRow is one row of the changes log: an operation on one field of
// one entity. Seq is local to this database (the event's and the
// history's cursor, never replicated); (Actor, OpID, N) is the row's
// identity across replicas, N the row's place in its operation from 0;
// the one order of the log is (At, Actor, OpID, N). Value is a string
// field's text as it is and a structured field's JSON; Prior the value
// before a set, or for a remove the tags of the adds it removes.
type ChangeRow struct {
	Seq       int64     `json:"seq"`
	Entity    string    `json:"entity"`
	EntityID  string    `json:"entityId"`
	TicketID  string    `json:"ticketId,omitempty"`
	ProjectID string    `json:"projectId,omitempty"`
	Field     string    `json:"field,omitempty"`
	Op        string    `json:"op"`
	Value     string    `json:"value"`
	Prior     string    `json:"prior,omitempty"`
	At        time.Time `json:"at"`
	Actor     string    `json:"actor"`
	By        string    `json:"by,omitempty"`
	OpID      string    `json:"opId"`
	N         int       `json:"n"`
}

// revTables names the table of each entity with a rev column, and
// whether the row has an updated_at to move with it.
var revTables = map[string]struct {
	table   string
	updated bool
}{
	EntityProject:   {"projects", true},
	EntityTicket:    {"tickets", true},
	EntityComment:   {"comments", false},
	EntityLabel:     {"labels", false},
	EntityMilestone: {"milestones", true},
	EntityView:      {"views", true},
	EntityPlan:      {"plans", false},
	EntityLaunch:    {"launches", false},
}

// tx is one operation's transaction: what a change writes through. It
// holds the operation, one time for all it writes, the next change row's
// n, the entities the rows named (for the rev bumps and OnChange), and
// what a create made.
type tx struct {
	ctx    context.Context
	tx     *sql.Tx
	now    time.Time
	op     Op
	intent string
	// fp is the operation's fingerprint, which an operation that keeps it
	// with what it makes writes there (a plan revision, a launch, a link).
	fp  string
	n   int
	seq int64
	// touched is every (entity, id) the change rows named, in order; the
	// first is the operation's subject, whose rev the receipt carries.
	touched []touched
	keys    map[string]string // project id → key, resolved once per operation
	id, key string
	// searched is every search entry the operation must rewrite, in the
	// order the rows named them (searchedBy): a ticket's own, whose create,
	// title, description or acceptance a row records, or a comment's, of
	// which a row records anything.
	searched []searchEntry
	// runExists is the store's sight of the engine's runs (SetRunExists),
	// nil when none was given; sight is its sight of a run's package and
	// chain (SetRunTicket, SetRunChain).
	runExists func(id string) bool
	sight     runSight
	// runSuffix mints a run id's last part for a launch (mintRunID), nil
	// for crypto/rand's: a test's seam.
	runSuffix func() string
	// onRefusal, when a change sets it, runs once a refusal has undone the
	// change, in its transaction before its receipt: what a refusal keeps
	// beside the receipt (an upload's lease, dropped with it).
	onRefusal func() error
}

// touched is an entity a change row named, with the ticket and the
// project it belongs to (the project by its key).
type touched struct {
	entity, id, ticket, project string
}

// Now is the transaction's time, one for all it writes.
func (t *tx) Now() time.Time { return t.now }

// expect compares the entity's rev, read inside the transaction, with
// the one the operation expects: stale_revision when they differ and one
// was given. A change calls it before it writes anything.
func (t *tx) expect(rev int64) error {
	if t.op.ExpectedRev > 0 && rev != t.op.ExpectedRev {
		return refuse(RefusalStaleRevision, "the revision expected is %d; it is %d", t.op.ExpectedRev, rev)
	}
	return nil
}

// created names what the operation made, for the receipt.
func (t *tx) created(id, key string) { t.id, t.key = id, key }

// record writes one changes row for a field of an entity, next in the
// operation (n), at the transaction's time and under its actor, by and
// op id, and marks the entity for the rev bump and OnChange. A row whose
// identity the log holds is refused by the index, and nothing of the
// operation is kept.
func (t *tx) record(c ChangeRow) error {
	if c.Entity == "" || c.EntityID == "" || c.Op == "" {
		return errors.New("dispatch: a change row names its entity, its id and its op")
	}
	if len(c.Value) > MaxChangeValue || len(c.Prior) > MaxChangeValue {
		return fmt.Errorf("dispatch: a change row of %s is more than %d bytes", c.Field, MaxChangeValue)
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO changes (entity, entity_id, ticket_id, project_id, field, op, value, prior, at, actor, by, op_id, n)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Entity, c.EntityID, c.TicketID, c.ProjectID, c.Field, c.Op, c.Value, c.Prior, stamp(t.now), t.op.Actor, t.op.By, t.op.ID, t.n)
	if err != nil {
		return err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return err
	}
	t.seq, t.n = seq, t.n+1
	if e, ok := searchedBy(c); ok && !slices.Contains(t.searched, e) {
		t.searched = append(t.searched, e)
	}
	return t.touch(c.Entity, c.EntityID, c.TicketID, c.ProjectID)
}

// searchedFields is a ticket's fields its own search entry holds beside
// its key, which never changes.
var searchedFields = map[string]bool{"title": true, "description": true, "acceptance": true}

// searchedBy is the search entry a change row makes stale, and whether
// there is one: a ticket's own on its create and on a set of a field the
// entry holds; a comment's on every row of the comment (its create, its
// body, its delete), since the entry holds the body while the comment is
// live. No other entry: a comment's rows leave its ticket's own entry and
// its other comments' as they are (docs/plans/dispatch.md, Owner
// decisions, 20), and a ticket's delete or undelete changes no entry,
// since a list leaves a deleted ticket out by its row.
func searchedBy(c ChangeRow) (searchEntry, bool) {
	switch {
	case c.Entity == EntityTicket && (c.Op == OpCreate || (c.Op == OpSet && searchedFields[c.Field])):
		return searchEntry{id: c.EntityID}, true
	case c.Entity == EntityComment:
		return searchEntry{comment: true, id: c.EntityID}, true
	}
	return searchEntry{}, false
}

// recordRemove records the remove of a member of one of a ticket's sets:
// row as given, its op remove and its prior the tags of the adds it
// removes, in their order. The tags go into as many rows as they need,
// each prior at most MaxChangeValue bytes (tagPriors): nothing bounds how
// many replicas' adds one member gathers, each a tag of its own and every
// one needed gone, and a remove too large to write would leave the member
// impossible to remove. Each row names its share and removes those tags
// alone, so the member is gone, on every replica, once every tag is
// named. One row whenever the tags fit, which they do but after such a
// merge.
func (t *tx) recordRemove(row ChangeRow, tags []Tag) error {
	for _, prior := range tagPriors(tags) {
		r := row
		r.Op, r.Prior = OpRemove, prior
		if err := t.record(r); err != nil {
			return err
		}
	}
	return nil
}

// tagPriors cuts tags, in their order, into the fewest priors of at most
// MaxChangeValue bytes each, every one but the last too full to take the
// next tag; none is one empty list. A tag alone is far under the bound,
// its actor at most MaxActor bytes and its op id at most 32.
func tagPriors(tags []Tag) []string {
	var out []string
	var part []Tag
	size := len("[]")
	for _, tag := range tags {
		n := len(EncodeTags([]Tag{tag})) - len("[]")
		if len(part) > 0 {
			n++ // the comma before it
		}
		if len(part) > 0 && size+n > MaxChangeValue {
			out = append(out, EncodeTags(part))
			part, size, n = nil, len("[]"), n-1
		}
		part = append(part, tag)
		size += n
	}
	return append(out, EncodeTags(part))
}

// touch marks an entity the operation changed, for the rev bump and
// OnChange, once however many rows name it; record calls it for every
// row, and an operation calls it alone for an entity it changed through
// another's row: a relation's add is a row of its from ticket's and
// changes its to ticket's page too, so it counts for both. The first
// entity touched is the operation's subject, whose rev the receipt
// carries.
func (t *tx) touch(entity, id, ticket, projectID string) error {
	key := ""
	if projectID != "" {
		var err error
		if key, err = t.projectKey(projectID); err != nil {
			return err
		}
	}
	for _, e := range t.touched {
		if e.entity == entity && e.id == id {
			return nil
		}
	}
	t.touched = append(t.touched, touched{entity, id, ticket, key})
	return nil
}

// projectKey is the key of the project with the given id, read once per
// operation.
func (t *tx) projectKey(id string) (string, error) {
	if key, ok := t.keys[id]; ok {
		return key, nil
	}
	var key string
	if err := t.tx.QueryRowContext(t.ctx, "SELECT key FROM projects WHERE id = ?", id).Scan(&key); err != nil {
		if isNoRows(err) {
			return "", fmt.Errorf("dispatch: a change row names project %s, which has no row", id)
		}
		return "", err
	}
	if t.keys == nil {
		t.keys = map[string]string{}
	}
	t.keys[id] = key
	return key, nil
}

// bump bumps the rev of every entity the change rows named, once per
// operation whatever it set, moving its updated_at with it where the row
// has one, and reads back the subject's rev for the receipt.
func (t *tx) bump() (int64, error) {
	var rev int64
	for i, e := range t.touched {
		rt, ok := revTables[e.entity]
		if !ok {
			continue
		}
		stmt := "UPDATE " + rt.table + " SET rev = rev + 1 WHERE id = ?"
		args := []any{e.id}
		if rt.updated {
			stmt = "UPDATE " + rt.table + " SET rev = rev + 1, updated_at = ? WHERE id = ?"
			args = []any{stamp(t.now), e.id}
		}
		res, err := t.tx.ExecContext(t.ctx, stmt, args...)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return 0, fmt.Errorf("dispatch: a change row names %s %s, which has no row", e.entity, e.id)
		}
		if i == 0 {
			if err := t.tx.QueryRowContext(t.ctx, "SELECT rev FROM "+rt.table+" WHERE id = ?", e.id).Scan(&rev); err != nil {
				return 0, err
			}
		}
	}
	return rev, nil
}

// changes is what the operation changed, for OnChange's one call: one
// per entity, with the commit's last seq.
func (t *tx) changes() []Change {
	out := make([]Change, 0, len(t.touched))
	for _, e := range t.touched {
		out = append(out, Change{Seq: t.seq, Entity: e.entity, ID: e.id, Ticket: e.ticket, Project: e.project})
	}
	return out
}

// fingerprint is an operation's content, for the conflict check: the
// SHA-256 of the intent and the arguments as JSON.
func fingerprint(intent string, args any) (string, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(intent))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Apply runs one operation: op names its actor and id, intent and args
// are its content (the fingerprint), and change makes it inside the
// transaction, through the tx's record, expect and created. The receipt
// comes back as the store keeps it: a repeat when the same id and content
// were answered before (Repeat set), op_conflict for the same id with
// other content (no row), a refusal change returned (a failed receipt,
// kept), else done with the subject's rev after the operation and what a
// create made. A change that records no changes row is refused invalid,
// so every done receipt has its row (actor, op id, 0): a resend whose
// receipt is pruned is answered by the changes log (prunedAnswer), a row
// (actor, op id, 0) meaning the operation ran, and the resend is answered
// done and Repeat with no rev, id or key, or, for an operation that keeps
// its fingerprint with what it made, done with that record's id when the
// fingerprints agree and op_conflict when they do not; none means the
// receipt was a refusal's, and the resend runs as a new operation. An error is the
// database's own, a bad op id, actor or display name, or the store
// closed: nothing was written, and nothing is answered; a read-only store
// answers a read_only refusal, written nowhere. OnChange's listeners are
// called after the commit, on this goroutine, each once with all the
// operation changed.
func (s *Store) Apply(ctx context.Context, op Op, intent string, args any, change func(*tx) error) (Receipt, error) {
	if err := op.check(); err != nil {
		return Receipt{}, err
	}
	fp, err := fingerprint(intent, args)
	if err != nil {
		return Receipt{}, err
	}
	s.mu.Lock()
	rc, changed, err := s.apply(ctx, op, intent, fp, change)
	s.mu.Unlock()
	if err != nil {
		return Receipt{}, err
	}
	if len(changed) > 0 {
		s.notify(changed)
	}
	return rc, nil
}

// apply is Apply under the store's lock.
func (s *Store) apply(ctx context.Context, op Op, intent, fp string, change func(*tx) error) (Receipt, []Change, error) {
	if s.closed {
		return Receipt{}, nil, ErrClosed
	}
	if s.readOnly || s.db == nil {
		return readOnlyReceipt(op, intent, s.why), nil, nil
	}
	dbtx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, nil, cancelledAs(ctx, err)
	}
	defer dbtx.Rollback()
	// The receipt, when the id was answered before; else the log, when
	// the receipt was pruned: the operation ran.
	rc, found, err := answered(ctx, dbtx, op, intent, fp)
	if err != nil {
		return Receipt{}, nil, cancelledAs(ctx, err)
	}
	if found {
		return rc, nil, nil
	}
	t := &tx{ctx: ctx, tx: dbtx, now: time.Now().UTC(), op: op, intent: intent, fp: fp, runExists: s.runExists, sight: s.sight, runSuffix: s.runSuffix}
	// The change runs under a savepoint: a refusal undoes it and keeps the
	// transaction, and with it the write lock, for its receipt, so no other
	// writer (a second store on the file) runs the same operation between
	// the refusal and the receipt.
	if _, err := dbtx.ExecContext(ctx, "SAVEPOINT change"); err != nil {
		return Receipt{}, nil, cancelledAs(ctx, err)
	}
	err = change(t)
	if err == nil && t.n == 0 {
		// Done with no row, a success would leave the log nothing to answer
		// a resend by once its receipt is pruned, and run again.
		err = refuse(RefusalInvalid, "the operation changes nothing")
	}
	if err != nil {
		var ref *Refusal
		if !errors.As(err, &ref) {
			return Receipt{}, nil, cancelledAs(ctx, err)
		}
		// Refused: nothing of the change is kept, and the failed receipt is
		// the transaction's only write but what the change asked a refusal
		// to keep (onRefusal: an upload's lease dropped).
		if _, err := dbtx.ExecContext(ctx, "ROLLBACK TO change"); err != nil {
			return Receipt{}, nil, cancelledAs(ctx, err)
		}
		if t.onRefusal != nil {
			if err := t.onRefusal(); err != nil {
				return Receipt{}, nil, cancelledAs(ctx, err)
			}
		}
		rc = fitWords(Receipt{OpID: op.ID, Intent: intent, Status: StatusFailed, Refusal: ref.Code, Error: words(ref.Words), Missing: ref.Missing, Key: ref.Key})
		if err := s.commitReceipt(ctx, dbtx, op, intent, fp, t.now, rc); err != nil {
			return Receipt{}, nil, err
		}
		return rc, nil, nil
	}
	// The search entries the change made stale, rewritten in its
	// transaction, so a read never finds a ticket the search does not,
	// nor the reverse.
	for _, e := range t.searched {
		if err := e.index(ctx, dbtx); err != nil {
			return Receipt{}, nil, cancelledAs(ctx, err)
		}
	}
	rev, err := t.bump()
	if err != nil {
		return Receipt{}, nil, cancelledAs(ctx, err)
	}
	rc = Receipt{OpID: op.ID, Intent: intent, Status: StatusDone, Rev: rev, ID: t.id, Key: t.key}
	if err := s.commitReceipt(ctx, dbtx, op, intent, fp, t.now, rc); err != nil {
		return Receipt{}, nil, err
	}
	return rc, t.changes(), nil
}

// answered is the answer an operation id sent again gets before anything
// is done, read through q (the operation's transaction, or Replay's
// read): its receipt when the store keeps one (a repeat of it for the same
// intent and fingerprint, op_conflict for other content), else what the
// changes log says of it (prunedAnswer); found is false for an id never
// answered, or whose receipt was a refusal's and is pruned.
func answered(ctx context.Context, q queryer, op Op, intent, fp string) (rc Receipt, found bool, err error) {
	var priorIntent, priorFp, priorBody string
	switch err := q.QueryRowContext(ctx, "SELECT intent, fingerprint, receipt FROM ops WHERE actor = ? AND id = ?", op.Actor, op.ID).Scan(&priorIntent, &priorFp, &priorBody); {
	case err == nil:
		if priorIntent != intent || priorFp != fp {
			return conflictReceipt(op, intent, wordsOpConflict), true, nil
		}
		if err := json.Unmarshal([]byte(priorBody), &rc); err != nil {
			return Receipt{}, false, fmt.Errorf("dispatch: the receipt of %s: %w", op.ID, err)
		}
		rc.Repeat = true
		return rc, true, nil
	case isNoRows(err):
	default:
		return Receipt{}, false, err
	}
	return prunedAnswer(ctx, q, op, intent, fp)
}

// conflictReceipt is an op_conflict refusal with the words, written
// nowhere.
func conflictReceipt(op Op, intent, words string) Receipt {
	return Receipt{OpID: op.ID, Intent: intent, Status: StatusFailed, Refusal: RefusalOpConflict, Error: words}
}

// prunedAnswer is the answer to an operation whose receipt the store
// keeps no more (prune), from the changes log, which keeps the
// operation's first row (actor, op id, 0) for good: none, and found is
// false (never run, or refused, which writes no row). Found, it is done
// and Repeat, with no rev, id or key, for an operation that keeps no
// fingerprint of its own; for one that does, its first row names what it
// made, and that record's fingerprint, written in the operation's
// transaction and never changed, is compared with the resend's: the same,
// done and Repeat with the record's id, which the caller reads it by;
// another, op_conflict, as the receipt would have answered. A plan
// revision's proposal finds its create row (entity plan), a launch its
// create row (entity launch), a link's add its add row (a ticket's links),
// whose link names the fingerprint; a first row of another kind is
// another operation's, op_conflict. A record with no fingerprint (a link
// a version 1 store added) cannot be compared: op_conflict with the words
// that say so, which refuses the resend rather than take content it
// cannot check, and a new operation for the same link is refused as a
// duplicate, so nothing is added twice.
func prunedAnswer(ctx context.Context, q queryer, op Op, intent, fp string) (rc Receipt, found bool, err error) {
	var entity, entityID, field, kind, value string
	switch err := q.QueryRowContext(ctx, "SELECT entity, entity_id, field, op, value FROM changes WHERE actor = ? AND op_id = ? AND n = 0",
		op.Actor, op.ID).Scan(&entity, &entityID, &field, &kind, &value); {
	case isNoRows(err):
		return Receipt{}, false, nil
	case err != nil:
		return Receipt{}, false, err
	}
	done := Receipt{OpID: op.ID, Intent: intent, Status: StatusDone, Repeat: true}
	var table, id string
	switch intent {
	case IntentPlanPropose:
		table, id = "plans", entityID
		if entity != EntityPlan || kind != OpCreate {
			return conflictReceipt(op, intent, wordsOpConflict), true, nil
		}
	case IntentTicketLaunch:
		table, id = "launches", entityID
		if entity != EntityLaunch || kind != OpCreate {
			return conflictReceipt(op, intent, wordsOpConflict), true, nil
		}
	case IntentLinkAdd:
		var v struct {
			ID string `json:"id"`
		}
		if entity != EntityTicket || field != "links" || kind != OpAdd || json.Unmarshal([]byte(value), &v) != nil || v.ID == "" {
			return conflictReceipt(op, intent, wordsOpConflict), true, nil
		}
		table, id = "links", v.ID
	default:
		return done, true, nil
	}
	// A version 1 database, read as it was found (read), kept no
	// fingerprint: recorded stays empty, too old to compare.
	var recorded string
	if schemaOf(q) >= 2 {
		if err := q.QueryRowContext(ctx, "SELECT fingerprint FROM "+table+" WHERE id = ?", id).Scan(&recorded); err != nil {
			if isNoRows(err) {
				return Receipt{}, false, fmt.Errorf("dispatch: the operation %s made %s %s, which has no row", op.ID, table, id)
			}
			return Receipt{}, false, err
		}
	}
	switch recorded {
	case "":
		return conflictReceipt(op, intent, wordsTooOld), true, nil
	case fp:
		done.ID = id
		return done, true, nil
	}
	return conflictReceipt(op, intent, wordsOpConflict), true, nil
}

// Replay answers an operation sent again before its caller does any work
// of its own, as Apply would answer it and no weaker (answered): found,
// the answer is the operation's (a repeat of its receipt, done with the
// record's id once the receipt is pruned, op_conflict for other content,
// or a refusal kept); not found, the operation is new and the caller goes
// on to make it. intent and args are the operation's content, as Apply is
// given them (PlanProposeArgs for plan.propose, LinkAddArgs for link.add):
// the API replays a proposal before it reads the crew or resolves the
// base, a launch before it starts anything, and a delivery's add before
// it reads the ledger, so a resend after a lost reply gets the first
// answer whatever those have become. It writes nothing; it reads a
// read-only store as any read does. An error is the database's, a bad op
// id, actor or display name, or the store closed or unavailable.
func (s *Store) Replay(ctx context.Context, op Op, intent string, args any) (rc Receipt, found bool, err error) {
	if err := op.check(); err != nil {
		return Receipt{}, false, err
	}
	fp, err := fingerprint(intent, args)
	if err != nil {
		return Receipt{}, false, err
	}
	err = s.read(ctx, func(q queryer) error {
		rc, found, err = answered(ctx, q, op, intent, fp)
		return err
	})
	if err != nil {
		return Receipt{}, false, err
	}
	return rc, found, nil
}

// commitReceipt writes an operation's receipt in its transaction and
// commits: a done operation's writes with it, or a refusal's receipt
// alone.
func (s *Store) commitReceipt(ctx context.Context, dbtx *sql.Tx, op Op, intent, fp string, now time.Time, rc Receipt) error {
	if s.beforeReceipt != nil {
		if err := s.beforeReceipt(); err != nil {
			return cancelledAs(ctx, err)
		}
	}
	if err := insertReceipt(ctx, dbtx, op, intent, fp, now, rc); err != nil {
		return cancelledAs(ctx, err)
	}
	if err := dbtx.Commit(); err != nil {
		return cancelledAs(ctx, err)
	}
	return nil
}

// readOnlyReceipt is the answer of a store that takes no writes: a
// read_only refusal with why, written nowhere.
func readOnlyReceipt(op Op, intent, why string) Receipt {
	return fitWords(Receipt{OpID: op.ID, Intent: intent, Status: StatusFailed, Refusal: RefusalReadOnly, Error: words(why)})
}

// insertReceipt writes the ops row of an operation.
func insertReceipt(ctx context.Context, dbtx *sql.Tx, op Op, intent, fp string, now time.Time, rc Receipt) error {
	body, err := json.Marshal(rc)
	if err != nil {
		return err
	}
	if len(body) > MaxReceipt {
		return fmt.Errorf("dispatch: the receipt of %s is %d bytes, more than %d", op.ID, len(body), MaxReceipt)
	}
	_, err = dbtx.ExecContext(ctx, `INSERT INTO ops (actor, id, intent, fingerprint, status, receipt, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		op.Actor, op.ID, intent, fp, rc.Status, string(body), stamp(now))
	return err
}

// words cuts a refusal's words to maxReceiptWords characters.
func words(s string) string {
	rs := []rune(s)
	if len(rs) <= maxReceiptWords {
		return s
	}
	return string(rs[:maxReceiptWords]) + "…"
}

// fitWords is rc with its words cut further, at a character and marked
// with "…", to the longest that leaves the receipt within MaxReceipt as
// JSON with Repeat set, the larger of its two answers (the first, and a
// repeat's), so both fit: a character's size there is not its count
// (JSON writes <, >, &, U+2028, U+2029 and a control character as six
// bytes, and UTF-8 takes up to four), so 1,000 characters can take 6 KiB.
func fitWords(rc Receipt) Receipt {
	size := func(rc Receipt) int {
		rc.Repeat = true
		b, _ := json.Marshal(rc)
		return len(b)
	}
	if rc.Error == "" || size(rc) <= MaxReceipt {
		return rc
	}
	rs := []rune(rc.Error)
	cut := func(k int) Receipt {
		c := rc
		c.Error = string(rs[:k]) + "…"
		return c
	}
	// The longest prefix that fits, by halves: a prefix one character
	// longer is never smaller.
	lo, hi := 0, len(rs)
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if size(cut(mid)) <= MaxReceipt {
			lo = mid
		} else {
			hi = mid
		}
	}
	return cut(lo)
}

// cancelledAs is err as the operation's caller should see it: the
// context's own error when the context ended under the transaction,
// whatever database/sql said of it (its rollback of a cancelled
// transaction races the commit, which then says sql.ErrTxDone), so that
// a stop is told from a store that failed; err itself otherwise.
func cancelledAs(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil && (errors.Is(err, sql.ErrTxDone) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("dispatch: %w", cerr)
	}
	return err
}

// Receipt reads an operation's receipt without acting: found is false
// when the store holds none (never given, or pruned).
func (s *Store) Receipt(ctx context.Context, actor, id string) (rc Receipt, found bool, err error) {
	db, err := s.conn()
	if err != nil {
		return Receipt{}, false, err
	}
	var body string
	switch err := db.QueryRowContext(ctx, "SELECT receipt FROM ops WHERE actor = ? AND id = ?", actor, id).Scan(&body); {
	case isNoRows(err):
		return Receipt{}, false, nil
	case err != nil:
		return Receipt{}, false, err
	}
	if err := json.Unmarshal([]byte(body), &rc); err != nil {
		return Receipt{}, false, fmt.Errorf("dispatch: the receipt of %s: %w", id, err)
	}
	return rc, true, nil
}

// OpTicket is the key of the ticket the actor's operation with the id
// changed, read from the changes log, the ticket its first row (n 0)
// names: the log keeps it after the receipt is pruned and after the
// ticket is deleted, so a resend can name where the operation went
// (AddRunComment). found is false for an operation that changed no
// ticket: never given, a refusal (which writes no row), or one of a
// project's or the store's.
func (s *Store) OpTicket(ctx context.Context, actor, id string) (key string, found bool, err error) {
	db, err := s.conn()
	if err != nil {
		return "", false, err
	}
	switch err := db.QueryRowContext(ctx, "SELECT t.key FROM changes c JOIN tickets t ON t.id = c.ticket_id WHERE c.actor = ? AND c.op_id = ? AND c.n = 0", actor, id).Scan(&key); {
	case isNoRows(err):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return key, true, nil
}

// prune drops every receipt but the newest maxReceipts, by their time,
// and reports how many went, counted on the pruned counter; it runs at
// open and every pruneEvery on the store's goroutine (pruning). now is
// the maintenance clock those callers share with the sweep (sweep.go); the
// pruning is by count and reads nothing of it.
func (s *Store) prune(ctx context.Context, now time.Time) (int, error) {
	_ = now
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return 0, ErrClosed
	case s.db == nil || s.readOnly:
		return 0, fmt.Errorf("%w: %s", ErrReadOnly, s.why)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM ops WHERE rowid IN (SELECT rowid FROM ops ORDER BY created_at DESC, actor DESC, id DESC LIMIT -1 OFFSET ?)`, maxReceipts)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	s.pruned.Add(n)
	return int(n), nil
}

// fieldValue is a scalar field's column and its value as a change row
// records it: a text field's text as it is, a number in decimal, a
// structured field's JSON. Its column takes that value as it is (SQLite
// stores a decimal in an INTEGER column as the integer), so the row is
// what its change rows make.
type fieldValue struct{ column, value string }

// fieldValues is a copy of fields, to change without touching them.
func fieldValues(fields []fieldValue) []fieldValue { return append([]fieldValue(nil), fields...) }

// setField sets the column's value among fields, in place.
func setField(fields []fieldValue, column, value string) {
	for i := range fields {
		if fields[i].column == column {
			fields[i].value = value
		}
	}
}

// valueOf is the value of the column among fields, "" when it is not
// among them.
func valueOf(fields []fieldValue, column string) string {
	for _, f := range fields {
		if f.column == column {
			return f.value
		}
	}
	return ""
}

// columns is the columns of fields, in their order.
func columns(fields []fieldValue) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.column
	}
	return out
}

// scalars reads the columns of the row with the id, as text: a number
// in decimal. table and the columns are the code's, never a client's.
func (t *tx) scalars(table, id string, cols []string) ([]fieldValue, error) {
	values := make([]string, len(cols))
	dest := make([]any, len(cols))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := t.tx.QueryRowContext(t.ctx, "SELECT "+strings.Join(cols, ", ")+" FROM "+table+" WHERE id = ?", id).Scan(dest...); err != nil {
		return nil, err
	}
	out := make([]fieldValue, len(cols))
	for i, c := range cols {
		out[i] = fieldValue{c, values[i]}
	}
	return out, nil
}

// setScalars writes each field of the row base names whose value in
// after differs from before's, in after's order: its column set, and one
// set row naming the field, its value and its prior. A field unchanged
// writes nothing. table and the columns are the code's, never a client's.
func (t *tx) setScalars(table string, base ChangeRow, before, after []fieldValue) error {
	prior := map[string]string{}
	for _, f := range before {
		prior[f.column] = f.value
	}
	for _, f := range after {
		if prior[f.column] == f.value {
			continue
		}
		if _, err := t.tx.ExecContext(t.ctx, "UPDATE "+table+" SET "+f.column+" = ? WHERE id = ?", f.value, base.EntityID); err != nil {
			return err
		}
		row := base
		row.Field, row.Op, row.Value, row.Prior = f.column, OpSet, f.value, prior[f.column]
		if err := t.record(row); err != nil {
			return err
		}
	}
	return nil
}
