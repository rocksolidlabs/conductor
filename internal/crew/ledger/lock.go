package ledger

import (
	"fmt"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// ownerLock is the owner lock, store.OwnerLock: runs/owner.lock, held for
// the process's life.
type ownerLock = store.OwnerLock

// takeOwnerLock tries once for the lock at path (store.TakeOwnerLock). It
// returns no lock and the reason when another process holds it (held) or
// when the filesystem does not support locks (a network or 9p mount):
// managed runs are then off for this server, until a held lock is taken
// (Ledger.awaitLock). An error is for trouble with the file itself.
func takeOwnerLock(path string) (*ownerLock, string, bool, error) {
	lock, why, held, err := store.TakeOwnerLock(path)
	if err != nil {
		err = fmt.Errorf("ledger: %w", err)
	}
	return lock, why, held, err
}
