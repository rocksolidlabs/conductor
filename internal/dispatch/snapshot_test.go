package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
)

// snapshotTickets reads the copy at path on a read-only connection: its
// check, and the titles of its tickets in order.
func snapshotTickets(t *testing.T, path string) (check string, titles []string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT title FROM tickets ORDER BY title")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, s)
	}
	return check, titles
}

// A snapshot of an open store while operations commit: the copy is one
// committed state (its check passes, its tickets a prefix of those made),
// and a store opened over it in a directory of its own reads them as the
// original does. A file in its place is refused; a closed store refuses.
func TestSnapshot(t *testing.T) {
	s := openStore(t, storeDir(t))
	newProject(t, s, "CND")
	half := make(chan struct{})
	var wg sync.WaitGroup
	var werr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 20 {
			if i == 10 {
				close(half)
			}
			rc, err := s.CreateTicket(context.Background(), testOp(fmt.Sprintf("ticket-%02d", i)), NewTicket{Project: "CND", Title: fmt.Sprintf("t%02d", i)})
			if err == nil && rc.Status != StatusDone {
				err = fmt.Errorf("ticket %d: %+v", i, rc)
			}
			if err != nil {
				werr = err
				return
			}
		}
	}()
	<-half
	dst := filepath.Join(t.TempDir(), "copy.sqlite")
	if err := s.Snapshot(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if werr != nil {
		t.Fatalf("an operation beside the snapshot: %v", werr)
	}
	check, titles := snapshotTickets(t, dst)
	if check != "ok" || len(titles) < 10 || len(titles) > 20 {
		t.Fatalf("the copy: check %q, %d tickets", check, len(titles))
	}
	for i, title := range titles {
		if title != fmt.Sprintf("t%02d", i) {
			t.Fatalf("the copy's tickets are not a prefix of those made: %v", titles)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("the copy's mode: %v %v", fi, err)
		}
	}
	// A store over the copy reads every ticket the copy holds.
	dir := storeDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dst, filepath.Join(dir, File)); err != nil {
		t.Fatal(err)
	}
	restored := openStore(t, dir)
	if ro, why := restored.ReadOnly(); ro {
		t.Fatalf("the copy opens read-only: %s", why)
	}
	if n := count(t, restored, "SELECT COUNT(*) FROM tickets"); n != len(titles) {
		t.Fatalf("the restored store holds %d tickets, the copy %d", n, len(titles))
	}
	again := filepath.Join(t.TempDir(), "again.sqlite")
	if err := os.WriteFile(again, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(context.Background(), again); err == nil {
		t.Fatal("a snapshot over a file that exists")
	}
	s.Close()
	if err := s.Snapshot(context.Background(), filepath.Join(t.TempDir(), "closed.sqlite")); !errors.Is(err, ErrClosed) {
		t.Fatalf("a snapshot of a closed store: %v", err)
	}
}

// SnapshotFile copies a database no server has open through a read-only
// connection of its own (the file made read-only on disk too), and one a
// store holds open; it refuses a link and a missing file.
func TestSnapshotFile(t *testing.T) {
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	newTicket(t, s, "CND", "one")
	newTicket(t, s, "CND", "two")
	path := filepath.Join(dir, File)
	held := filepath.Join(t.TempDir(), "held.sqlite")
	if err := SnapshotFile(context.Background(), path, held); err != nil {
		t.Fatal(err)
	}
	if check, titles := snapshotTickets(t, held); check != "ok" || !slices.Equal(titles, []string{"one", "two"}) {
		t.Fatalf("the copy of an open store: %q %v", check, titles)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(path, 0o600) })
	}
	dst := filepath.Join(t.TempDir(), "copy.sqlite")
	if err := SnapshotFile(context.Background(), path, dst); err != nil {
		t.Fatal(err)
	}
	if check, titles := snapshotTickets(t, dst); check != "ok" || !slices.Equal(titles, []string{"one", "two"}) {
		t.Fatalf("the copy: %q %v", check, titles)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
		t.Fatalf("the file read changed: %v", err)
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

// SnapshotHashes names each content a live row requires once: two live
// attachments sharing one content give one hash, a removed attachment's
// content is left out unless a live row or a launch's pin still names it,
// and a pinned content is named though its attachment was removed.
func TestSnapshotHashes(t *testing.T) {
	s, key := attachmentTicket(t)
	ctx := context.Background()
	shared := mustPut(t, s, "a1", key, "a.txt", []byte("shared"))
	mustPut(t, s, "a2", key, "b.txt", []byte("shared"))
	gone := mustPut(t, s, "a3", key, "gone.txt", []byte("removed"))
	pinned := mustPut(t, s, "a4", key, "pinned.txt", []byte("pinned"))
	kept := mustPut(t, s, "a5", key, "kept.txt", []byte("kept"))
	for i, a := range []Attachment{gone, pinned} {
		rc, err := s.RemoveAttachment(ctx, testOp(fmt.Sprintf("r%d", i)), key, a.ID)
		want(t, rc, err, StatusDone, "")
	}
	dst := filepath.Join(t.TempDir(), "copy.sqlite")
	if err := s.Snapshot(ctx, dst); err != nil {
		t.Fatal(err)
	}
	// A launch's pin on the removed content, written into the copy as a
	// launch would have written it into the store.
	raw, err := sql.Open("sqlite", dsn(dst, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("INSERT INTO launch_files (launch_id, sha256) VALUES ('l1', ?)", pinned.SHA256); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	sums, err := SnapshotHashes(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	wanted := []string{shared.SHA256, pinned.SHA256, kept.SHA256}
	slices.Sort(wanted)
	if !slices.Equal(sums, wanted) {
		t.Fatalf("the hashes: %v, not %v", sums, wanted)
	}
	if slices.Contains(sums, gone.SHA256) {
		t.Fatal("a removed attachment's content is named")
	}
	if got := AttachmentPath(kept.SHA256); got != "attachments/"+kept.SHA256[:2]+"/"+kept.SHA256 {
		t.Fatalf("AttachmentPath: %s", got)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), filepath.FromSlash(AttachmentPath(kept.SHA256)))); err != nil {
		t.Fatalf("AttachmentPath does not name the file: %v", err)
	}
}
