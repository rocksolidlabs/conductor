package dispatch

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The replica's identity (docs/plans/sync-seams.md, OS-1a): a store that a
// sync provider joins to a shared workspace writes its rows under a tag,
// 1 to MaxTag lower-case letters and digits, the rows' origin. A local row
// keeps its raw actor and names its origin; a row applied from another
// replica is stored with its actor qualified by its origin (Canon), so
// every replica orders the same rows the same way and two replicas'
// identities never meet. Nothing allocated to a tag outlives it: a store
// restored from an export retires its allocations at its open, and a
// provider retires them when its identity is no longer the tag's.

// MaxTag bounds a replica's tag, in bytes; MaxRemoteActor a canonical
// actor, @<origin>/<actor>.
const (
	MaxTag         = 8
	MaxRemoteActor = 72
)

// replicaTagPattern is a replica's tag.
var replicaTagPattern = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// ValidTag reports whether tag is a replica's tag.
func ValidTag(tag string) bool { return replicaTagPattern.MatchString(tag) }

// Errors of the replica's doors.
var (
	ErrBadTag     = errors.New("dispatch: a replica's tag is 1 to 8 lower-case letters and digits")
	ErrNotJoined  = errors.New("dispatch: this store has joined no workspace")
	ErrBadReplica = errors.New("dispatch: a replica's workspace and device are 1 to 64 bytes of letters, digits, '.', '_', '~', ':' or '-'")
)

// replicaIDPattern is a provider's opaque id of a workspace or a device.
var replicaIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~:-]{1,64}$`)

// Canon is a row's canonical actor: @<origin>/<actor>, the actor of a row
// of the replica origin as every replica orders it. An actor already
// qualified, and any actor of a row with no origin (written before this
// store joined anything), is as it is.
func Canon(actor, origin string) string {
	if origin == "" || strings.HasPrefix(actor, "@") {
		return actor
	}
	return "@" + origin + "/" + actor
}

// canon is the row's canonical actor.
func canon(c ChangeRow) string { return Canon(c.Actor, c.Origin) }

// rawActor is the actor a canonical actor qualifies, and its origin: the
// actor as it is for one not qualified.
func rawActor(actor string) (raw, origin string) {
	if !strings.HasPrefix(actor, "@") {
		return actor, ""
	}
	o, a, ok := strings.Cut(actor[1:], "/")
	if !ok {
		return actor, ""
	}
	return a, o
}

// The provisional window: a store that has retired its allocations takes
// a shared project's numbers from a window of ProvisionalSize drawn at
// random above ProvisionalFloor, past every overflow range, until its
// provider joins it again.
const (
	ProvisionalFloor = 2_000_000_000
	ProvisionalSize  = 1_000_000
	provisionalSpan  = 1_000_000_000 // the windows' starts are drawn from this many above the floor
)

// replica is the store's active tag, the workspace and device it was
// joined for and the store's instance when it joined; tag "" for none.
type replicaRow struct {
	tag, workspace, device, instance string
}

// activeReplica reads the active tag, the zero replica for none (and for
// a database before version 8, read as it was found).
func activeReplica(ctx context.Context, q queryer) (replicaRow, error) {
	if schemaOf(q) < syncSchema {
		return replicaRow{}, nil
	}
	var r replicaRow
	err := q.QueryRowContext(ctx, "SELECT tag, workspace, device, instance FROM replica_tags WHERE active = 1").Scan(&r.tag, &r.workspace, &r.device, &r.instance)
	if isNoRows(err) {
		return replicaRow{}, nil
	}
	return r, err
}

// Join makes tag the store's active replica tag, for the workspace and
// the device, with the overflow range [overflowFrom, overflowTo] a shared
// project's create takes numbers from when it has no block left. In one
// transaction: every allocation of the tag before it retired (Retire),
// the earlier active tag moved to history, the tag's row written with the
// store's instance, every local row with no origin given the tag as its
// origin (from then on each local row is written with it), and the
// overflow range set on every project. An origin once set never changes,
// so joining again under a new tag qualifies only the rows written since.
func (s *Store) Join(ctx context.Context, tag, workspace, device string, overflowFrom, overflowTo int64) error {
	switch {
	case !ValidTag(tag):
		return ErrBadTag
	case !replicaIDPattern.MatchString(workspace) || !replicaIDPattern.MatchString(device):
		return ErrBadReplica
	case overflowFrom < 0 || overflowTo < overflowFrom || (overflowFrom == 0) != (overflowTo == 0):
		return fmt.Errorf("dispatch: an overflow range of %d to %d is not one", overflowFrom, overflowTo)
	}
	return s.writeTx(ctx, func(dbtx *sql.Tx, now time.Time) error {
		if err := retire(ctx, dbtx, s.interrupted); err != nil {
			return err
		}
		var instance string
		if err := dbtx.QueryRowContext(ctx, "SELECT id FROM store_instance").Scan(&instance); err != nil {
			return err
		}
		if _, err := dbtx.ExecContext(ctx, "UPDATE replica_tags SET active = 0"); err != nil {
			return err
		}
		if _, err := dbtx.ExecContext(ctx, `INSERT INTO replica_tags (tag, workspace, device, instance, joined_at, active) VALUES (?, ?, ?, ?, ?, 1)
			ON CONFLICT (tag) DO UPDATE SET workspace = excluded.workspace, device = excluded.device, instance = excluded.instance,
			joined_at = excluded.joined_at, active = 1`, tag, workspace, device, instance, stamp(now)); err != nil {
			return err
		}
		if _, err := dbtx.ExecContext(ctx, "UPDATE changes SET origin = ? WHERE origin = '' AND remote = 0", tag); err != nil {
			return err
		}
		return keepOverflow(ctx, dbtx, overflowFrom, overflowTo)
	})
}

// ranges is the replica's overflow range and its provisional window,
// which a project made or received on this store takes.
type ranges struct {
	overflowFrom, overflowTo, provisionalFrom, provisionalTo int64
}

// keepOverflow keeps the replica's overflow range a join hands it: on
// every project, each project's next in it at its start. (The one place
// the range is written: a column of the replica's own is a store step of
// its own; until then a store that joins before it has a project keeps
// no range, and its shared projects' creates take the provisional
// window.)
func keepOverflow(ctx context.Context, dbtx *sql.Tx, from, to int64) error {
	_, err := dbtx.ExecContext(ctx, "UPDATE projects SET overflow_from = ?, overflow_to = ?, overflow_next = ?", from, to, from)
	return err
}

// replicaRanges reads the replica's overflow range and provisional
// window, as keepOverflow and retire left them on the projects. (The one
// place the range is read.)
func replicaRanges(ctx context.Context, q queryer) (ranges, error) {
	var r ranges
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(overflow_from), 0), COALESCE(MAX(overflow_to), 0), COALESCE(MAX(provisional_from), 0),
		COALESCE(MAX(provisional_to), 0) FROM projects`).Scan(&r.overflowFrom, &r.overflowTo, &r.provisionalFrom, &r.provisionalTo)
	return r, err
}

// Retire drops every allocation of the active tag: each project's current
// and next blocks, the tag they were handed to and the overflow range; a
// share in progress (pending or completing) back to none, with words; and
// a provisional window drawn for every project. The provider calls it when
// the identity its tag was joined under is no longer its own.
func (s *Store) Retire(ctx context.Context) error {
	return s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error { return retire(ctx, dbtx, s.interrupted) })
}

// interrupted keeps the words of a share a retire took back.
func (s *Store) interrupted(project string) { s.keepShareWords(project, "", interruptedShare) }

// interruptedShare is the words a share in progress keeps when its store
// retires it.
const interruptedShare = "sharing was interrupted when this machine's data was copied: share it again"

// retire is Retire in dbtx; interrupted is called with each project whose
// share in progress it took back.
func retire(ctx context.Context, dbtx *sql.Tx, interrupted func(project string)) error {
	from, err := provisionalStart()
	if err != nil {
		return err
	}
	ids, err := readTexts(ctx, dbtx, "SELECT id FROM projects WHERE share_state IN ('pending', 'completing')")
	if err != nil {
		return err
	}
	for _, id := range ids {
		interrupted(id)
	}
	_, err = dbtx.ExecContext(ctx, `UPDATE projects SET number_from = 0, number_to = 0, next_from = 0, next_to = 0, block_tag = '',
		overflow_from = 0, overflow_to = 0, overflow_next = 0,
		provisional_from = ?, provisional_to = ?, provisional_next = ?,
		share_state = CASE WHEN share_state IN ('pending', 'completing') THEN '' ELSE share_state END,
		share_reserved_to = CASE WHEN share_state IN ('pending', 'completing') THEN 0 ELSE share_reserved_to END`,
		from, from+ProvisionalSize-1, from)
	return err
}

// provisionalStart draws a provisional window's first number at random
// above ProvisionalFloor.
func provisionalStart() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return ProvisionalFloor + int64(binary.BigEndian.Uint64(b[:])%provisionalSpan), nil
}

// retireIfRestored retires every allocation, in the open's first
// transaction, when the active tag was joined by another instance of the
// store (a store restored from an export, which Reissue made anew): so no
// create of a restored store runs before its inherited blocks are
// retired.
func retireIfRestored(ctx context.Context, dbtx *sql.Tx, interrupted func(string)) error {
	r, err := activeReplica(ctx, dbtx)
	if err != nil || r.tag == "" {
		return err
	}
	var instance string
	if err := dbtx.QueryRowContext(ctx, "SELECT id FROM store_instance").Scan(&instance); err != nil {
		return err
	}
	if instance == r.instance {
		return nil
	}
	return retire(ctx, dbtx, interrupted)
}

// writeTx runs fn in one write transaction under the store's lock, at the
// store's time, and commits it; a read-only store refuses it.
func (s *Store) writeTx(ctx context.Context, fn func(*sql.Tx, time.Time) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrClosed
	case s.readOnly || s.db == nil:
		return fmt.Errorf("%w: %s", ErrUnavailable, s.why)
	}
	dbtx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cancelledAs(ctx, err)
	}
	defer dbtx.Rollback()
	now := time.Now()
	if s.clock != nil {
		now = s.clock()
	}
	if err := fn(dbtx, now.UTC()); err != nil {
		return cancelledAs(ctx, err)
	}
	return cancelledAs(ctx, dbtx.Commit())
}

// Replica is the store's active tag and what it was joined for, "" for
// none.
func (s *Store) Replica(ctx context.Context) (tag, workspace, device string, err error) {
	err = s.read(ctx, func(q queryer) error {
		r, err := activeReplica(ctx, q)
		tag, workspace, device = r.tag, r.workspace, r.device
		return err
	})
	return tag, workspace, device, err
}

// ShareForTest marks the project with the key shared in the workspace,
// as a share's cut-over leaves it (OS-1b), for a test of the store's
// callers: the store's own tests write the columns.
func (s *Store) ShareForTest(ctx context.Context, key, workspace string) error {
	return s.writeTx(ctx, func(dbtx *sql.Tx, _ time.Time) error {
		_, err := dbtx.ExecContext(ctx, "UPDATE projects SET shared = ?, share_state = 'shared', registry_key = key WHERE key = ?", workspace, key)
		return err
	})
}
