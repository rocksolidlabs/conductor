package api

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// presence is this server's mark in the data folder (presence/<id>.lock),
// a lock held for the process's life: a server over the same folder sees
// it held while this one runs, and nothing of it outlasts the process.
// Every server that runs sessions over a data folder takes one, with a
// sync store or with none: a server with none runs no check, and marks
// itself for one that does. While another live server shares the folder,
// the copied-output check judges every text output (outputGuard.shared):
// that server's sessions' output is in its index if it has one, never in
// this one's (docs/plans/sync-seams.md, OS-1c). A server that could not
// take its lock cannot tell whether it is alone, and judges as if it were
// not. A server of a version before the presence locks takes none and is
// not seen.
type presence struct {
	dir, path string
	lock      *store.OwnerLock
	// unknown says the lock could not be taken (trouble with the folder,
	// a filesystem with no locks): shared answers true.
	unknown bool
	// mu lets one look at the folder at a time. A look takes each other
	// lock for a moment to see whether it is held (store.LockHeld), and
	// two looks of one process at one file would each find the other's.
	mu sync.Mutex
}

const (
	// presenceTries is how often a server tries for its own lock. Its
	// file's name is its own, so only another server's look, which holds
	// it for a moment, can have it.
	presenceTries = 50
	// presenceStale is how old a lock nobody holds is before it is
	// removed. A server locks its file as it makes it, so one this old
	// was left by a process that ended without letting it go.
	presenceStale = time.Minute
)

// takePresence takes this server's presence lock in dir's presence/. It
// always answers a presence: one that could not take its lock answers
// shared for good, with why.
func takePresence(dataDir string) (*presence, string) { return takePresenceWith(dataDir, nil) }

// takePresenceWith is takePresence with a test's gate: onTry runs before
// each try for the lock, with the lock's path.
func takePresenceWith(dataDir string, onTry func(path string, try int)) (*presence, string) {
	dir := filepath.Join(dataDir, "presence")
	unknown := func(why string) (*presence, string) { return &presence{dir: dir, unknown: true}, why }
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return unknown(err.Error())
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return unknown(err.Error())
	}
	p := &presence{dir: dir, path: filepath.Join(dir, hex.EncodeToString(id[:])+".lock")}
	for try := range presenceTries {
		if onTry != nil {
			onTry(p.path, try)
		}
		lock, why, held, err := store.TakeOwnerLock(p.path)
		switch {
		case err != nil:
			os.Remove(p.path)
			return unknown(err.Error())
		case lock != nil:
			p.lock = lock
			return p, ""
		case !held:
			// The filesystem has no locks: nothing here can tell.
			os.Remove(p.path)
			return unknown(why)
		}
		// Another server's look holds it for a moment: try again.
		time.Sleep(time.Millisecond)
	}
	os.Remove(p.path)
	return unknown("the server's own presence lock stayed held")
}

// shared says another live server holds a presence lock in the folder;
// true when this server could not take its own, and when the folder or a
// lock cannot be read, failing closed. A lock nobody holds that is old
// enough not to be a starting server's is removed.
func (p *presence) shared() bool {
	if p == nil {
		return false
	}
	if p.unknown {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return true
	}
	now := time.Now()
	for _, e := range entries {
		path := filepath.Join(p.dir, e.Name())
		if path == p.path || !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		held, err := store.LockHeld(path)
		if err != nil || held {
			return true
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > presenceStale {
			os.Remove(path)
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
