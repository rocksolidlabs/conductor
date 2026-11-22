package ledger

import (
	"fmt"
	"sync"
	"sync/atomic"

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

// probeOwnerLock tries once for the lock at path without making the file
// (store.ProbeOwnerLock), reading at most ProbeLineMax bytes of its
// holder's line when it is held.
func probeOwnerLock(path string) (held bool, line string, noLocks bool, err error) {
	held, line, noLocks, err = store.ProbeOwnerLock(path, ProbeLineMax)
	if err != nil {
		err = fmt.Errorf("ledger: %w", err)
	}
	return held, line, noLocks, err
}

// lockTries orders this process's tries for an owner lock: a ledger's
// (Open's and awaitLock's) and a probe's (Probe) are made one at a time.
// A probe that finds the lock free holds it for the moment of its try,
// and a ledger's try made in that moment would find it held: a wait at its
// last try would then give up on a lock nobody keeps. The lock is per
// open file, so this is as true of two tries in one process as of two
// processes; across processes nothing orders them.
var lockTries tryOrder

// tryOrder is a mutex that counts who waits for it: a test reads waiting
// to see a try held behind another, rather than guessing it from a clock.
type tryOrder struct {
	mu      sync.Mutex
	waiting atomic.Int32
}

func (o *tryOrder) lock() {
	o.waiting.Add(1)
	o.mu.Lock()
	o.waiting.Add(-1)
}

func (o *tryOrder) unlock() { o.mu.Unlock() }

// takeOrdered is takeLock made in its turn (lockTries).
func takeOrdered(path string) (*ownerLock, string, bool, error) {
	lockTries.lock()
	defer lockTries.unlock()
	return takeLock(path)
}
