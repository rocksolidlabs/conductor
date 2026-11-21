//go:build unix

package api

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
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

func TestAServerWithNoSyncStoreIsSeenByOneWithAStore(t *testing.T) {
	dir := syncDataDir(t)
	plain := newTestEnvAgents(t, func(c *config.Config) { c.DataDir = dir }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if plain.srv.copyCheck != nil || plain.srv.outputs != nil {
		t.Fatal("a server with no sync store has a check or an index")
	}
	if p := plain.srv.presence; p == nil || p.unknown || p.lock == nil {
		t.Fatal("a server with no sync store did not mark its presence")
	}
	ctx := context.Background()
	plain.newDispatchProject(t, "CND", nil)
	plain.newDispatchTicket(t, "CND", "The ticket", nil)
	b := syncEnv(t, dir, newFakeSync())
	if !b.srv.outputs.whole(ctx) {
		t.Fatal("the index not whole")
	}
	own := "a person's own words, long enough to be judged by the check"
	verdict := func(body string) string {
		t.Helper()
		if rc, err := b.srv.dispatch.AddComment(ctx, adminOp(""), "CND-1", body); err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("the comment: %+v %v", rc, err)
		}
		rows, _, err := b.srv.dispatch.ChangesFrom(ctx, 0, dispatch.MaxChangesPage)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(rows, func(c dispatch.ChangeRow) bool { return c.Value == body })
		if i < 0 {
			t.Fatalf("no row of %q", body)
		}
		return rows[i].Output
	}
	// The plain server's sessions' output is in no index: whatever is
	// written through the server with a store may quote it.
	if b.srv.copyCheck.alone() {
		t.Fatal("a server with a sync store judges alone beside one with none")
	}
	if v := verdict(own + " (beside a server with no sync store)"); v != verdictOutput {
		t.Fatalf("beside a server with no sync store: %q", v)
	}
	stopEnv(t, plain)
	if !b.srv.copyCheck.alone() {
		t.Fatal("alone again, and not judged so")
	}
	if v := verdict(own + " (alone again)"); v != verdictClean {
		t.Fatalf("alone again, a person's own words: %q", v)
	}
}
