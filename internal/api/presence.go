package api

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// presence is this server's mark in the data folder (presence/<id>.lock),
// a lock held for the process's life: a server over the same folder sees
// it held while this one runs, and nothing of it outlasts the process.
// While another live server shares the folder, the copied-output check
// judges every text output (outputGuard.shared): that server's sessions'
// output is in its index, never in this one's (docs/plans/sync-seams.md,
// OS-1c).
type presence struct {
	dir, path string
	lock      *store.OwnerLock
}

// takePresence takes this server's presence lock in dir's presence/;
// nil, and a server that judges alone, where the folder's filesystem has
// no locks.
func takePresence(dataDir string) (*presence, error) {
	dir := filepath.Join(dataDir, "presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, hex.EncodeToString(id[:])+".lock")
	lock, _, _, err := store.TakeOwnerLock(path)
	if err != nil || lock == nil {
		os.Remove(path)
		return nil, err
	}
	return &presence{dir: dir, path: path, lock: lock}, nil
}

// shared says another live server holds a presence lock in the folder;
// true when the folder cannot be read, failing closed.
func (p *presence) shared() bool {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		path := filepath.Join(p.dir, e.Name())
		if path == p.path || !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		held, err := store.LockHeld(path)
		if err != nil || held {
			return true
		}
	}
	return false
}

// release lets this server's presence go.
func (p *presence) release() {
	if p == nil || p.lock == nil {
		return
	}
	p.lock.Release()
	p.lock = nil
	os.Remove(p.path)
}
