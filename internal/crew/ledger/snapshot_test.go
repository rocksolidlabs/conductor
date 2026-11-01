package ledger

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// snapshotState is what a snapshot's file holds: its check, and for the
// run with the id its revision and its events, which every transition of
// newRun and bump moves together.
func snapshotState(t *testing.T, path, id string) (check string, revision, events int64) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT revision FROM runs WHERE id = ?", id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE run_id = ?", id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	return check, revision, events
}

// bump is one transition of the run: its revision and one event.
func bump(l *Ledger, id string, k int) error {
	_, err := l.Transition(context.Background(), id, func(tx *Tx) error {
		return tx.Event("bumped", map[string]int{"k": k})
	})
	return err
}

// Fifty transitions during a snapshot: the snapshot is taken under the
// ledger's lock, so the copy is one committed state, whole (quick_check,
// the run's revision and its events agreeing), never one half written,
// and the transitions all commit around it.
func TestSnapshotWhileWriting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const id = "team-00000001"
	newRun(t, l, id)
	half := make(chan struct{})
	var wg sync.WaitGroup
	var werr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for k := range 50 {
			if k == 25 {
				close(half)
			}
			if err := bump(l, id, k); err != nil {
				werr = err
				return
			}
		}
	}()
	<-half
	dst := filepath.Join(t.TempDir(), "copy.sqlite")
	if err := l.Snapshot(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if werr != nil {
		t.Fatalf("a transition beside the snapshot: %v", werr)
	}
	check, rev, events := snapshotState(t, dst, id)
	if check != "ok" || rev != events || rev < 26 || rev > 51 {
		t.Fatalf("the copy: check %q, revision %d, events %d", check, rev, events)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("the copy's mode: %v %v", fi, err)
		}
	}
	// Once they are done, a second copy holds all of them; a file in its
	// place is refused, never written over.
	again := filepath.Join(t.TempDir(), "again.sqlite")
	if err := l.Snapshot(context.Background(), again); err != nil {
		t.Fatal(err)
	}
	if check, rev, events := snapshotState(t, again, id); check != "ok" || rev != 51 || events != 51 {
		t.Fatalf("the second copy: check %q, revision %d, events %d", check, rev, events)
	}
	if err := l.Snapshot(context.Background(), again); err == nil {
		t.Fatal("a snapshot over a file that exists")
	}
	l.Close()
	if err := l.Snapshot(context.Background(), filepath.Join(t.TempDir(), "closed.sqlite")); !errors.Is(err, ErrClosed) {
		t.Fatalf("a snapshot of a closed ledger: %v", err)
	}
}

// SnapshotFile copies a database no server has open through a read-only
// connection of its own: the copy holds every run, and the file it read
// is as it was, byte for byte, with nothing made beside it. It reads one a
// server holds too, and refuses a link.
func TestSnapshotFileReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const id = "team-00000001"
	newRun(t, l, id)
	for k := range 3 {
		if err := bump(l, id, k); err != nil {
			t.Fatal(err)
		}
	}
	// Held open by its owner: the read-only connection reads what the
	// owner committed, its log among it.
	path := filepath.Join(dir, File)
	held := filepath.Join(t.TempDir(), "held.sqlite")
	if err := SnapshotFile(context.Background(), path, held); err != nil {
		t.Fatal(err)
	}
	if check, rev, events := snapshotState(t, held, id); check != "ok" || rev != 4 || events != 4 {
		t.Fatalf("the copy of a held ledger: check %q, revision %d, events %d", check, rev, events)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	listing := func() map[string]string {
		out := map[string]string{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name()] = string(b)
		}
		return out
	}
	before := listing()
	if runtime.GOOS != "windows" {
		// Read-only on disk too: a write would fail, and none is tried.
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(path, 0o600) })
	}
	dst := filepath.Join(t.TempDir(), "copy.sqlite")
	if err := SnapshotFile(context.Background(), path, dst); err != nil {
		t.Fatal(err)
	}
	if check, rev, events := snapshotState(t, dst, id); check != "ok" || rev != 4 || events != 4 {
		t.Fatalf("the copy: check %q, revision %d, events %d", check, rev, events)
	}
	// SQLite makes the -shm (and an empty -wal) beside a WAL database it
	// reads, which the next owner's open takes as its own: nothing else
	// appears, and nothing that was there changes.
	after := listing()
	for name, b := range after {
		was, ok := before[name]
		switch {
		case ok && was != b:
			t.Fatalf("%s changed", name)
		case !ok && name == File+"-wal" && b != "":
			t.Fatalf("the read wrote %d bytes into the log", len(b))
		case !ok && name != File+"-wal" && name != File+"-shm":
			t.Fatalf("the read made %s", name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Fatalf("%s went", name)
		}
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "link.sqlite")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if err := SnapshotFile(context.Background(), link, filepath.Join(t.TempDir(), "x.sqlite")); err == nil {
			t.Fatal("a link was copied")
		}
	}
	if err := SnapshotFile(context.Background(), filepath.Join(t.TempDir(), File), filepath.Join(t.TempDir(), "y.sqlite")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file: %v", err)
	}
}

// Held says whether a server holds a ledger's owner lock, and never makes
// anything: no lock file is held by nobody (and none is made, nor the
// directory), a ledger open in this process holds it (flock is per open
// file), and its Close lets it go.
func TestHeld(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no owner lock on this platform")
	}
	dir := filepath.Join(t.TempDir(), "runs")
	if held, err := Held(dir); err != nil || held {
		t.Fatalf("no directory: %v %v", held, err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Held made the directory: %v", err)
	}
	l := openLedger(t, dir)
	if held, err := Held(dir); err != nil || !held {
		t.Fatalf("an open ledger: %v %v", held, err)
	}
	// Asking again leaves it held, and its owner writing.
	if held, err := Held(dir); err != nil || !held {
		t.Fatalf("asked again: %v %v", held, err)
	}
	newRun(t, l, "team-00000001")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if held, err := Held(dir); err != nil || held {
		t.Fatalf("closed: %v %v", held, err)
	}
	// A directory with no lock file: none is made.
	bare := t.TempDir()
	if held, err := Held(bare); err != nil || held {
		t.Fatalf("no lock file: %v %v", held, err)
	}
	if _, err := os.Lstat(filepath.Join(bare, Lock)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Held made the lock file: %v", err)
	}
	// What it says once the lock is free, a new ledger takes it.
	l = openLedger(t, dir)
	if ro, why := l.ReadOnly(); ro {
		t.Fatalf("after Held: read-only: %s", why)
	}
}
