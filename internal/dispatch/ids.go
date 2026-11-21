package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// IDLen is an id's length: 16 lower-case hex characters from 8 random
// bytes, the shape of a session id. Every row but a person's has one; a
// person is its name.
const IDLen = 16

// newID returns a new opaque id.
func newID() string {
	var b [IDLen / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("dispatch: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// ValidID reports whether id is an id's shape (IDLen lower-case hex
// characters): what a query names a label, a milestone or a cursor's row
// by, and a route's path a comment, a label, a milestone, a relation, a
// link or a view.
func ValidID(id string) bool {
	if len(id) != IDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// KeyPattern is a project's key: one upper-case letter, then one to five
// upper-case letters or digits (CND). It is chosen at creation and never
// changed.
var KeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,5}$`)

// ValidKey reports whether key is a project's key.
func ValidKey(key string) bool { return KeyPattern.MatchString(key) }

// TicketKey is a ticket's key: its project's key and its number (CND-12),
// unique and never reassigned, kept by a soft delete.
func TicketKey(project string, n int64) string {
	return project + "-" + strconv.FormatInt(n, 10)
}

// ParseKey splits a ticket's key (CND-12) into its project's key and its
// number; ok is false for anything else: a number is digits without a
// leading zero, 1 or more, of at most 18 digits.
func ParseKey(key string) (project string, n int64, ok bool) {
	project, digits, found := strings.Cut(key, "-")
	if !found || !ValidKey(project) || digits == "" || len(digits) > 18 || digits[0] == '0' {
		return "", 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", 0, false
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 1 {
		return "", 0, false
	}
	return project, n, true
}

// errNoProject says the project has no row to allocate from.
var errNoProject = errors.New("dispatch: no such project")

// allocateNumber allocates the next number of the project inside t's
// transaction, under the write lock, so numbers are never allocated
// twice whichever process creates. A project this store has not shared
// takes next_number, in order, as before sync (docs/plans/dispatch.md,
// the allocation class of the field rules). A shared one
// (docs/plans/sync-seams.md, OS-1a) takes the next number of its current
// block when the block was handed to the active tag (block_tag), then the
// block asked ahead; with none left, the replica's overflow range's next;
// with none, the provisional window's next, a window drawn at random
// above ProvisionalFloor when the store retired (or now, when it has
// none), so a create never waits.
func allocateNumber(t *tx, projectID string) (int64, error) {
	var state, blockTag string
	var next, from, to, nextFrom, nextTo, overNext, overTo, provNext, provTo, reserved int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT share_state, block_tag, next_number, number_from, number_to, next_from, next_to,
		overflow_next, overflow_to, provisional_next, provisional_to, share_reserved_to FROM projects WHERE id = ?`, projectID).
		Scan(&state, &blockTag, &next, &from, &to, &nextFrom, &nextTo, &overNext, &overTo, &provNext, &provTo, &reserved)
	switch {
	case isNoRows(err):
		return 0, errNoProject
	case err != nil:
		return 0, err
	}
	take := func(stmt string, n int64) (int64, error) {
		_, err := t.tx.ExecContext(t.ctx, stmt, projectID)
		return n, err
	}
	switch state {
	case SharePending, ShareCompleting:
		// A share in progress: its reserved numbers first, as before sync.
		if next <= reserved {
			return take("UPDATE projects SET next_number = next_number + 1 WHERE id = ?", next)
		}
	case ShareShared, ShareDormant:
	default:
		return take("UPDATE projects SET next_number = next_number + 1 WHERE id = ?", next)
	}
	if t.origin != "" && blockTag == t.origin && (state == ShareShared || state == ShareDormant) {
		if to > 0 && next >= from && next <= to {
			// Past half of the block, the provider is told to ask the next
			// one (OnBlockLow), after the commit.
			if next-from >= (to-from+1)/2 && nextTo == 0 {
				t.blockLow = projectID
			}
			return take("UPDATE projects SET next_number = next_number + 1 WHERE id = ?", next)
		}
		if nextTo > 0 {
			return take("UPDATE projects SET number_from = next_from, number_to = next_to, next_number = next_from + 1, next_from = 0, next_to = 0 WHERE id = ?", nextFrom)
		}
	}
	if overTo > 0 && overNext <= overTo {
		return take("UPDATE projects SET overflow_next = overflow_next + 1 WHERE id = ?", overNext)
	}
	if provTo == 0 || provNext > provTo {
		start, err := provisionalStart()
		if err != nil {
			return 0, err
		}
		if _, err := t.tx.ExecContext(t.ctx, "UPDATE projects SET provisional_from = ?, provisional_to = ?, provisional_next = ? WHERE id = ?",
			start, start+ProvisionalSize-1, start, projectID); err != nil {
			return 0, err
		}
		provNext = start
	}
	return take("UPDATE projects SET provisional_next = provisional_next + 1 WHERE id = ?", provNext)
}
