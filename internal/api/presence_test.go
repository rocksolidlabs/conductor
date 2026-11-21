//go:build unix

package api

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// leftLock makes a presence lock nobody holds, as a process that ended
// without letting its own go leaves it, last written age ago.
func leftLock(t *testing.T, dataDir, name string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dataDir, "presence", name+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("pid 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAServerTakesItsPresenceThoughALookHeldItForAMoment(t *testing.T) {
	dir := t.TempDir()
	var look *store.OwnerLock
	tries := 0
	p, why := takePresenceWith(dir, func(path string, try int) {
		tries++
		switch try {
		case 0:
			// Another server's look at the folder holds the lock as this
			// server tries for it.
			l, _, _, err := store.TakeOwnerLock(path)
			if err != nil || l == nil {
				t.Fatalf("the look's lock: %v", err)
			}
			look = l
		case 1:
			look.Release()
		}
	})
	t.Cleanup(p.release)
	if why != "" || p.unknown || p.lock == nil {
		t.Fatalf("the presence was not taken after %d tries: %q", tries, why)
	}
	if tries != 2 {
		t.Fatalf("tries: %d", tries)
	}
	other, why := takePresence(dir)
	t.Cleanup(other.release)
	if why != "" {
		t.Fatal(why)
	}
	if !other.shared() || !p.shared() {
		t.Fatal("two servers over one folder, and one of them judges alone")
	}
	other.release()
	if p.shared() {
		t.Fatal("alone again, and still shared")
	}
}

func TestAServerThatCannotTakeItsPresenceJudgesAsIfNotAlone(t *testing.T) {
	dir := t.TempDir()
	var look *store.OwnerLock
	p, why := takePresenceWith(dir, func(path string, try int) {
		if try == 0 {
			l, _, _, err := store.TakeOwnerLock(path)
			if err != nil || l == nil {
				t.Fatalf("the look's lock: %v", err)
			}
			look = l
		}
	})
	t.Cleanup(look.Release)
	if why == "" || !p.unknown || !p.shared() {
		t.Fatalf("a lock held at every try: why %q, unknown %v", why, p.unknown)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "presence")); err != nil || len(entries) != 0 {
		t.Fatalf("the lock's file was left: %v %v", entries, err)
	}
	// A data folder whose presence cannot be made.
	file := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p, why = takePresence(file)
	if why == "" || !p.unknown || !p.shared() {
		t.Fatalf("a folder that cannot be made: why %q, unknown %v", why, p.unknown)
	}
	p.release()
}

func TestALoneServersLooksNeverFindEachOther(t *testing.T) {
	dir := t.TempDir()
	// Dated ahead, so that no load makes it old while the looks run.
	left := leftLock(t, dir, "left", -time.Hour)
	p, why := takePresence(dir)
	t.Cleanup(p.release)
	if why != "" {
		t.Fatal(why)
	}
	// Many checks at once, each a look at the lock a process left: a look
	// that found another look's hold would answer shared.
	var shared atomic.Int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 400 {
				if p.shared() {
					shared.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if n := shared.Load(); n != 0 {
		t.Fatalf("a server alone answered shared %d times", n)
	}
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("a lock just made was removed: %v", err)
	}
}

func TestALockLeftByAnEndedProcessIsRemovedOnceOld(t *testing.T) {
	dir := t.TempDir()
	left := leftLock(t, dir, "left", 2*presenceStale)
	heldPath := leftLock(t, dir, "held", 2*presenceStale)
	held, _, _, err := store.TakeOwnerLock(heldPath)
	if err != nil || held == nil {
		t.Fatalf("the other server's lock: %v", err)
	}
	when := time.Now().Add(-2 * presenceStale)
	if err := os.Chtimes(heldPath, when, when); err != nil {
		t.Fatal(err)
	}
	p, why := takePresence(dir)
	t.Cleanup(p.release)
	if why != "" {
		t.Fatal(why)
	}
	if !p.shared() {
		t.Fatal("a held lock, however old, is another live server's")
	}
	if _, err := os.Stat(heldPath); err != nil {
		t.Fatalf("a held lock was removed: %v", err)
	}
	held.Release()
	if p.shared() {
		t.Fatal("a lock nobody holds read as another server's")
	}
	for _, path := range []string{left, heldPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("an old lock nobody holds was kept: %s %v", filepath.Base(path), err)
		}
	}
}
