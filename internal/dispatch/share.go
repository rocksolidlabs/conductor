package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// A project's share and numbers (docs/plans/sync-seams.md, OS-1b): a
// person shares a project of this machine's (project.share), the provider
// hands its history out and completes the share, and from then on its
// creates take numbers from blocks a provider hands this replica, then
// from the replica's overflow range, so two machines never number one
// ticket alike. A project not shared has its numbers as before sync, and
// a person may give it another key (project.rekey) only while it is not
// shared, since the workspace's registry holds a shared project's key.

// The intents of a project's share and rekey.
const (
	IntentProjectShare = "project.share"
	IntentProjectRekey = "project.rekey"
)

// The share states of a project (projects.share_state): none, the share
// asked and its numbers reserved, its history handed out and the share
// completing, shared, dormant (the workspace's copy gone, its blocks
// kept), and refused (the workspace refused it, its words kept, numbered
// as a project not shared). A share's words (projects.share_words) are
// what its state was last given: a refusal's, a dormant or unshared
// workspace's, a retire's (interruptedShare); a share asked again, or a
// dormant project shared again, forgets them.
const (
	SharePending    = "pending"
	ShareCompleting = "completing"
	ShareShared     = "shared"
	ShareDormant    = "dormant"
	ShareRefused    = "refused"
)

// The refusals of a share and a rekey.
const (
	RefusalNumbersOutOfRange = "numbers_out_of_range"
	RefusalShareInProgress   = "share_in_progress"
	RefusalShared            = "shared"
)

// The numbers' ranges (docs/plans/sync-seams.md, Events, Numbers): a
// project's own numbers before its share and every block below
// ShareNumbersBelow; overflow ranges above it; provisional windows above
// ProvisionalFloor. A share reserves ShareReserve numbers past its
// project's next for the creates made while it completes.
const (
	ShareNumbersBelow = 1_000_000
	ShareReserve      = 999
)

// ErrShareMoved is CompleteShare's refusal when the log holds a row of
// the project past the position its history was handed out to: Newest is
// that row's position.
type ErrShareMoved struct {
	Newest int64
}

func (e ErrShareMoved) Error() string {
	return fmt.Sprintf("dispatch: the project has a row past the share's position, at %d", e.Newest)
}

// ErrNoSuchProject is a share's door naming a project this store has not.
var ErrNoSuchProject = errors.New("dispatch: no such project")

// ShareProject asks the project with the key to be shared
// (project.share): refused for an archived project (archived) and for one
// whose highest number is ShareNumbersBelow or more (numbers_out_of_range,
// since blocks and the reservation live below it); otherwise its share is
// pending and ShareReserve numbers past its next are reserved for the
// creates made while it completes, in one transaction. A dormant
// project's share reserves nothing (its numbers are blocks already) and
// is pending again. A project shared, completing or pending already is
// invalid.
func (s *Store) ShareProject(ctx context.Context, op Op, key string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
	}{key}
	return s.Apply(ctx, op, IntentProjectShare, args, func(t *tx) error {
		p, err := t.project(key)
		if err != nil {
			return err
		}
		var state string
		var highest int64
		if err := t.tx.QueryRowContext(t.ctx, "SELECT share_state, (SELECT COALESCE(MAX(number), 0) FROM tickets WHERE project_id = ?) FROM projects WHERE id = ?",
			p.ID, p.ID).Scan(&state, &highest); err != nil {
			return err
		}
		switch {
		case !p.ArchivedAt.IsZero():
			return refuse(RefusalArchived, "%s is archived: unarchive it to share it", p.Key)
		case state == SharePending || state == ShareCompleting || state == ShareShared:
			return refuse(RefusalInvalid, "%s is %s already", p.Key, state)
		case state != ShareDormant && highest >= ShareNumbersBelow:
			return refuse(RefusalNumbersOutOfRange, "%s has a ticket numbered %d: a shared project's own numbers are below %d", p.Key, highest, ShareNumbersBelow)
		}
		stmt := "UPDATE projects SET share_state = ?, share_words = '', share_reserved_to = next_number + ? WHERE id = ?"
		args := []any{SharePending, ShareReserve, p.ID}
		if state == ShareDormant {
			stmt, args = "UPDATE projects SET share_state = ?, share_words = '' WHERE id = ?", []any{SharePending, p.ID}
		}
		if _, err := t.tx.ExecContext(t.ctx, stmt, args...); err != nil {
			return err
		}
		t.created(p.ID, p.Key)
		return t.record(ChangeRow{Entity: EntityProject, EntityID: p.ID, ProjectID: p.ID, Field: "share_state", Op: OpSet, Value: SharePending, Prior: state})
	})
}

// RekeyProject gives the project with the key another key
// (project.rekey): allowed only while the project is not shared and no
// share of it is in progress (its share none or refused;
// share_in_progress while pending or
// completing, since the provider may have registered the key already;
// shared once shared, since the workspace's registry holds it); the new
// key of KeyPattern (invalid) and no other project's (key_taken). The
// project's key and its tickets' keys follow, in one set row of the key,
// whose history keeps the key the project was made under. Once it
// commits, the rows held for the old key's holder (a workspace's project
// of that key, held while this one had it) are applied in the next
// transaction, in their order.
func (s *Store) RekeyProject(ctx context.Context, op Op, key, newKey string) (Receipt, error) {
	args := struct {
		Key    string `json:"key"`
		NewKey string `json:"newKey"`
	}{key, newKey}
	rc, err := s.Apply(ctx, op, IntentProjectRekey, args, func(t *tx) error {
		p, err := t.project(key)
		if err != nil {
			return err
		}
		var shared, state string
		if err := t.tx.QueryRowContext(t.ctx, "SELECT shared, share_state FROM projects WHERE id = ?", p.ID).Scan(&shared, &state); err != nil {
			return err
		}
		switch {
		case state == SharePending || state == ShareCompleting:
			return refuse(RefusalShareInProgress, "%s is being shared: its key is the workspace's now", p.Key)
		case shared != "" || state == ShareShared || state == ShareDormant:
			return refuse(RefusalShared, "%s is shared: the workspace keeps its key", p.Key)
		case !KeyPattern.MatchString(newKey):
			return refuse(RefusalInvalid, "the key %q is not a project's key: a capital letter, then one to five capitals or digits", newKey)
		case newKey == p.Key:
			return refuse(RefusalInvalid, "%s is %s's key already", newKey, p.Key)
		}
		var held int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM projects WHERE key = ?", newKey).Scan(&held); err != nil {
			return err
		}
		if held > 0 {
			return refuse(RefusalKeyTaken, "the key %s is another project's", newKey)
		}
		if _, err := t.tx.ExecContext(t.ctx, "UPDATE projects SET key = ? WHERE id = ?", newKey, p.ID); err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(t.ctx, "UPDATE tickets SET key = ? || '-' || number WHERE project_id = ?", newKey, p.ID); err != nil {
			return err
		}
		t.created(p.ID, newKey)
		delete(t.keys, p.ID)
		return t.record(ChangeRow{Entity: EntityProject, EntityID: p.ID, ProjectID: p.ID, Field: "key", Op: OpSet, Value: newKey, Prior: p.Key})
	})
	if err == nil && rc.Status == StatusDone && !rc.Repeat {
		// The rows held for a workspace's project of the old key, applied
		// now that the key is free (a store that joined nothing holds none).
		s.ApplyRemote(ctx, nil, nil, nil)
	}
	return rc, err
}

// SetBlock writes the shared project's current block of numbers (next
// false) or the block asked ahead (next true), handed to the active tag.
// ErrNotJoined before the store joined a workspace; ErrNoSuchProject for
// a project it has not.
func (s *Store) SetBlock(ctx context.Context, project string, from, to int64, next bool) error {
	if from < 1 || to < from || to >= ShareNumbersBelow {
		return fmt.Errorf("dispatch: a block of %d to %d is not one below %d", from, to, ShareNumbersBelow)
	}
	return s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		r, err := activeReplica(ctx, dbtx)
		if err != nil {
			return err
		}
		if r.tag == "" {
			return ErrNotJoined
		}
		stmt := "UPDATE projects SET number_from = ?, number_to = ?, next_number = ?, block_tag = ? WHERE id = ?"
		args := []any{from, to, from, r.tag, project}
		if next {
			stmt, args = "UPDATE projects SET next_from = ?, next_to = ?, block_tag = ? WHERE id = ?", []any{from, to, r.tag, project}
		}
		res, err := dbtx.ExecContext(ctx, stmt, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNoSuchProject
		}
		return nil
	})
}

// CompleteShare moves the pending share of the project to completing in
// the workspace, its history handed out up to the position upto: refused
// with ErrShareMoved when the log holds a row of the project past upto
// (the provider hands those out and asks again), and for a project whose
// share is not pending.
func (s *Store) CompleteShare(ctx context.Context, project, workspace string, upto int64) error {
	if !replicaIDPattern.MatchString(workspace) {
		return ErrBadReplica
	}
	return s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		state, err := shareState(ctx, dbtx, project)
		if err != nil {
			return err
		}
		if state != SharePending {
			return fmt.Errorf("dispatch: the share of %s is %q, not pending", project, state)
		}
		var newest int64
		if err := dbtx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM changes WHERE project_id = ? AND seq > ?", project, upto).Scan(&newest); err != nil {
			return err
		}
		if newest > 0 {
			return ErrShareMoved{Newest: newest}
		}
		_, err = dbtx.ExecContext(ctx, "UPDATE projects SET shared = ?, share_state = ?, share_upto = ? WHERE id = ?", workspace, ShareCompleting, upto, project)
		return err
	})
}

// ConfirmShare makes the completing share of the project shared, once
// the provider says the workspace holds it: from then on its creates take
// block numbers, and the key its registry holds is the one it has now.
func (s *Store) ConfirmShare(ctx context.Context, project string) error {
	return s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		state, err := shareState(ctx, dbtx, project)
		if err != nil {
			return err
		}
		if state != ShareCompleting {
			return fmt.Errorf("dispatch: the share of %s is %q, not completing", project, state)
		}
		_, err = dbtx.ExecContext(ctx, "UPDATE projects SET share_state = ?, registry_key = key, share_reserved_to = 0 WHERE id = ?", ShareShared, project)
		return err
	})
}

// RefuseShare makes the project's pending share refused and takes its
// reserved numbers back, keeping the words (at most MaxShareWords bytes
// of one line) for the project's page, so the person can give it another
// key. A share refused already keeps its state and takes the words; a
// share in any other state (completing, shared, dormant, none, a
// received project's) is an error and changes nothing; ErrNoSuchProject
// for a project the store has not.
func (s *Store) RefuseShare(ctx context.Context, project, words string) error {
	if err := checkShareWords(words); err != nil {
		return err
	}
	return s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		state, err := shareState(ctx, dbtx, project)
		if err != nil {
			return err
		}
		set := "shared = '', share_state = 'refused', share_reserved_to = 0, share_upto = 0, share_words = ?"
		switch state {
		case SharePending:
		case ShareRefused:
			set = "share_words = ?"
		default:
			return fmt.Errorf("dispatch: the share of %s is %q, not pending", project, state)
		}
		_, err = dbtx.ExecContext(ctx, "UPDATE projects SET "+set+" WHERE id = ?", addressFree(words), project)
		return err
	})
}

// Dormant makes every shared or completing project of the workspace
// dormant (still the workspace's, its blocks kept, nothing of it handed
// out), when the provider says the workspace's shared copy is gone; a
// batch of a dormant project's rows from another machine makes it shared
// again.
func (s *Store) Dormant(ctx context.Context, workspace, words string) error {
	return s.shareWords(ctx, "share_state = 'dormant'", "shared = ? AND share_state IN ('shared', 'completing')", workspace, words, false)
}

// Unshared marks every project of the workspace no longer shared with
// this machine: its share none (shared kept for history), nothing of it
// handed out or applied.
func (s *Store) Unshared(ctx context.Context, workspace, words string) error {
	return s.shareWords(ctx, "share_state = ''", "shared = ? AND share_state != ''", workspace, words, false)
}

// MaxShareWords bounds the words a share's state keeps, in bytes.
const MaxShareWords = 200

// shareWords sets set and the words, kept address-free, on the projects
// where names (with arg), in one statement; one is true for a call that
// names one project, ErrNoSuchProject when it names none.
func (s *Store) shareWords(ctx context.Context, set, where, arg, words string, one bool) error {
	if err := checkShareWords(words); err != nil {
		return err
	}
	return s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		res, err := dbtx.ExecContext(ctx, "UPDATE projects SET "+set+", share_words = ? WHERE "+where, addressFree(words), arg)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 && one {
			return ErrNoSuchProject
		}
		return nil
	})
}

// checkShareWords refuses words a share's state cannot keep.
func checkShareWords(words string) error {
	if len(words) > MaxShareWords || !utf8.ValidString(words) || strings.ContainsAny(words, "\r\n") || hasControl(words) {
		return fmt.Errorf("dispatch: a share's words are one line of at most %d bytes", MaxShareWords)
	}
	return nil
}

// shareWordsColumn is the share's words as a read selects them, from the
// table named by prefix: none on a version 8 database (read-only), which
// has no column for them.
func shareWordsColumn(q queryer, prefix string) string {
	if schemaOf(q) < syncKeptSchema {
		return "''"
	}
	return prefix + "share_words"
}

// shareState is the project's share state; ErrNoSuchProject for none.
func shareState(ctx context.Context, q queryer, project string) (string, error) {
	var state string
	err := q.QueryRowContext(ctx, "SELECT share_state FROM projects WHERE id = ?", project).Scan(&state)
	if isNoRows(err) {
		return "", ErrNoSuchProject
	}
	return state, err
}

// BlockState is a shared project's blocks: its current block and the
// next number in it, the block asked ahead, and how many numbers of the
// current one are left.
type BlockState struct {
	Project  string
	From     int64
	To       int64
	Next     int64
	NextFrom int64
	NextTo   int64
	Left     int64
}

// SharedProject is a project with a share, as the provider reads it.
type SharedProject struct {
	ID, Key, Name, State, Words string
	ReservedTo, Highest         int64
}

// MaxSharedProjects bounds Blocks and Shared.
const MaxSharedProjects = 1000

// Blocks is each shared project's blocks.
func (s *Store) Blocks(ctx context.Context) ([]BlockState, error) {
	var out []BlockState
	err := s.read(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT id, number_from, number_to, next_number, next_from, next_to FROM projects
			WHERE share_state IN ('shared', 'dormant') ORDER BY id LIMIT ?`, MaxSharedProjects)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b BlockState
			if err := rows.Scan(&b.Project, &b.From, &b.To, &b.Next, &b.NextFrom, &b.NextTo); err != nil {
				return err
			}
			if b.To > 0 && b.Next <= b.To {
				b.Left = b.To - b.Next + 1
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// Shared is each project with a share (or refused one), with its state
// and words, the numbers its share reserved and its highest number; none
// on a database older than sync's.
func (s *Store) Shared(ctx context.Context) ([]SharedProject, error) {
	var out []SharedProject
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < syncSchema {
			return nil
		}
		rows, err := q.QueryContext(ctx, `SELECT p.id, p.key, p.name, p.share_state, `+shareWordsColumn(q, "p.")+`, p.share_reserved_to,
			COALESCE((SELECT MAX(number) FROM tickets t WHERE t.project_id = p.id), 0)
			FROM projects p WHERE p.share_state != '' OR p.shared != '' ORDER BY p.key LIMIT ?`, MaxSharedProjects)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p SharedProject
			if err := rows.Scan(&p.ID, &p.Key, &p.Name, &p.State, &p.Words, &p.ReservedTo, &p.Highest); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// SetNames keeps other replicas' device names, by tag: at most
// MaxReplicaNames, each a valid tag to at most MaxDeviceName bytes of one
// line of words, kept address-free.
func (s *Store) SetNames(ctx context.Context, names map[string]string) error {
	if len(names) > MaxReplicaNames {
		return fmt.Errorf("dispatch: %d names, more than %d", len(names), MaxReplicaNames)
	}
	for tag, name := range names {
		if !ValidTag(tag) || name == "" || len(name) > MaxDeviceName || !utf8.ValidString(name) || hasControl(name) {
			return fmt.Errorf("dispatch: the name of %q is not a device's", tag)
		}
	}
	return s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		for tag, name := range names {
			if _, err := dbtx.ExecContext(ctx, "INSERT INTO replica_names (tag, name) VALUES (?, ?) ON CONFLICT (tag) DO UPDATE SET name = excluded.name",
				tag, addressFree(name)); err != nil {
				return err
			}
		}
		return nil
	})
}

// The bounds of the replica names.
const (
	MaxReplicaNames = 1000
	MaxDeviceName   = 64
)

// HandedChanges is ChangesFrom's rows that leave this machine: a row of
// no project (a person's), or of a project whose share is pending,
// completing or shared; a dormant, refused or unshared project's rows and
// a local project's stay. The position to read from next is past every
// row read.
func (s *Store) HandedChanges(ctx context.Context, after int64, limit int) ([]ChangeRow, int64, error) {
	rows, next, err := s.ChangesFrom(ctx, after, limit)
	if err != nil {
		return nil, 0, err
	}
	states := map[string]string{}
	err = s.read(ctx, func(q queryer) error {
		for _, c := range rows {
			p := c.ProjectID
			if c.Entity == EntityProject {
				p = c.EntityID
			}
			if _, ok := states[p]; ok || p == "" {
				continue
			}
			st, err := shareState(ctx, q, p)
			if err != nil && !errors.Is(err, ErrNoSuchProject) {
				return err
			}
			states[p] = st
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	out := rows[:0]
	for _, c := range rows {
		p := c.ProjectID
		if c.Entity == EntityProject {
			p = c.EntityID
		}
		switch states[p] {
		case SharePending, ShareCompleting, ShareShared:
			out = append(out, c)
		case "":
			if p == "" {
				out = append(out, c)
			}
		}
	}
	return out, next, nil
}

// OnBlockLow registers fn, called once a create took a shared project's
// current block past its half and no block is asked ahead, after the
// commit, on a goroutine of its own, never blocking a write: a call that
// has not returned drops the next until it does. nil for none.
func (s *Store) OnBlockLow(fn func(project string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockLow = fn
}

// tellBlockLow calls OnBlockLow's fn for the project, unless a call has
// not returned.
func (s *Store) tellBlockLow(project string) {
	s.mu.Lock()
	fn := s.blockLow
	s.mu.Unlock()
	if fn == nil || !s.blockLowBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.blockLowBusy.Store(false)
		fn(project)
	}()
}

// shareOf is the project's share and what it holds back, for its read.
func (s *Store) shareOf(ctx context.Context, q queryer, p Project) (*ProjectShare, *HeldBy, error) {
	if schemaOf(q) < syncSchema {
		return nil, nil, nil
	}
	var sh ProjectShare
	if err := q.QueryRowContext(ctx, "SELECT share_state, "+shareWordsColumn(q, "")+", share_reserved_to, shared FROM projects WHERE id = ?", p.ID).
		Scan(&sh.State, &sh.Words, &sh.ReservedTo, &sh.Shared); err != nil {
		return nil, nil, err
	}
	if sh.State != SharePending && sh.State != ShareCompleting {
		sh.ReservedTo = 0
	}
	var share *ProjectShare
	if sh.State != "" || sh.Words != "" {
		share = &sh
	}
	// The incoming project's create waits for the key, and its other rows
	// for the project.
	var held *HeldBy
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM held_rows WHERE waits_for = ? OR waits_for IN
		(SELECT 'project:' || json_extract(row, '$.entityId') FROM held_rows WHERE waits_for = ?)`, "key:"+p.Key, "key:"+p.Key).Scan(&n); err != nil {
		return nil, nil, err
	}
	if n > 0 {
		held = &HeldBy{Key: p.Key, Rows: n}
	}
	return share, held, nil
}

// TicketShared says the ticket with the id is in a project shared here
// now (its share confirmed): a run's pointer names its launch ticket only
// then. False for a ticket the store does not hold.
func (s *Store) TicketShared(ctx context.Context, ticketID string) (bool, error) {
	shared := false
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < syncSchema {
			return nil
		}
		var state string
		err := q.QueryRowContext(ctx, "SELECT p.share_state FROM tickets t JOIN projects p ON p.id = t.project_id WHERE t.id = ?", ticketID).Scan(&state)
		if isNoRows(err) {
			return nil
		}
		shared = err == nil && state == ShareShared
		return err
	})
	return shared, err
}
