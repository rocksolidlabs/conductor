//go:build unix

package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// publish on unix (docs/plans/dispatch.md, PR-7): the shard directory and
// then the attachments' directory synced on every upload, through syncDir,
// so a new name is durable before a row names it, whoever made the shard
// and however that ended.

// recorder is a syncDir that records the directories it is given, in
// order, and syncs each for real; fail, when set, runs first and may fail
// the sync or hold it.
type recorder struct {
	mu    sync.Mutex
	paths []string
	fail  func(path string) error
}

func (r *recorder) sync(path string) error {
	r.mu.Lock()
	r.paths = append(r.paths, path)
	fail := r.fail
	r.mu.Unlock()
	if fail != nil {
		if err := fail(path); err != nil {
			return err
		}
	}
	return syncDirectory(path)
}

// take is what the recorder was given since it was last taken.
func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.paths
	r.paths = nil
	return out
}

// sameShard is content other than first whose hash begins as first's,
// so its file goes into first's shard.
func sameShard(first []byte) []byte {
	want := sumOf(first)[:2]
	for i := 0; ; i++ {
		c := []byte(fmt.Sprintf("%s, again %d", first, i))
		if sumOf(c)[:2] == want {
			return c
		}
	}
}

// newShard is content whose shard s has not made.
func newShard(t *testing.T, s *Store, seed string) []byte {
	t.Helper()
	for i := 0; ; i++ {
		c := []byte(fmt.Sprintf("%s %d", seed, i))
		if !exists(t, filepath.Dir(contentAt(s, sumOf(c)))) {
			return c
		}
	}
}

// shardOf is the shard directory of content in s.
func shardOf(s *Store, content []byte) string { return filepath.Dir(contentAt(s, sumOf(content))) }

// Every upload has syncDir sync its shard and then the attachments'
// directory, in that order: one that made the shard, one into a shard
// there already, and one into a shard the test made by hand and never
// synced, as an upload stopped between making a shard and syncing its
// parent leaves one. Every file is 0600 and every directory 0700.
func TestEveryUploadSyncsItsShardAndThenTheAttachments(t *testing.T) {
	s, key := attachmentTicket(t)
	rec := &recorder{}
	s.syncDir = rec.sync
	first := newShard(t, s, "makes its shard")
	byHand := newShard(t, s, "into a shard made by hand")
	if err := os.Mkdir(shardOf(s, byHand), 0o700); err != nil {
		t.Fatal(err)
	}
	for i, content := range [][]byte{first, sameShard(first), byHand} {
		mustPut(t, s, fmt.Sprintf("a%d", i), key, "f.txt", content)
		if got, want := rec.take(), []string{shardOf(s, content), rootOf(s)}; !slices.Equal(got, want) {
			t.Fatalf("upload %d synced %v, want %v", i, got, want)
		}
	}
	for _, p := range []string{rootOf(s), filepath.Join(rootOf(s), uploadsDir), shardOf(s, first), contentAt(s, sumOf(first))} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if mode := fi.Mode().Perm(); (fi.IsDir() && mode != 0o700) || (!fi.IsDir() && mode != 0o600) {
			t.Fatalf("%s is %v", p, mode)
		}
	}
}

// A sync made to fail fails the upload before its operation runs: the
// error, no attachment row, no receipt and no lease, and the file it
// published is an orphan the sweep deletes. The same content uploaded
// again under a new op id is taken.
func TestAFailedSyncFailsTheUploadAndLeavesAnOrphan(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	failure := errors.New("the disk is gone")
	rec := &recorder{fail: func(string) error { return failure }}
	s.syncDir = rec.sync
	content := []byte("published, never named")
	if _, _, err := put(s, "a1", key, "f.txt", content); !errors.Is(err, failure) {
		t.Fatalf("a failed sync: %v", err)
	}
	if _, found, _ := s.Receipt(ctx, "test", "a1"); found || count(t, s, "SELECT COUNT(*) FROM attachments") != 0 || count(t, s, "SELECT COUNT(*) FROM uploads") != 0 {
		t.Fatal("a failed sync kept a receipt, a row or a lease")
	}
	if !exists(t, contentAt(s, sumOf(content))) {
		t.Fatal("the rename never happened")
	}
	if err := s.sweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	noTrace(t, s, content)
	rec.fail = nil
	a := mustPut(t, s, "a2", key, "f.txt", content)
	if !bytes.Equal(readContent(t, s, a.ID), content) {
		t.Fatal("the upload again did not open whole")
	}
}

// The parent's sync made to fail alone, on the upload that makes the
// shard: the upload fails as above and the shard is left behind,
// unsynced into its parent. The same content uploaded again under a new
// op id, the syncs restored, is taken, and syncs the shard and then the
// parent though the shard was there before it.
func TestAFailedParentSyncLeavesAShardTheNextUploadSyncs(t *testing.T) {
	s, key := attachmentTicket(t)
	failure := errors.New("the parent's sync failed")
	rec := &recorder{}
	rec.fail = func(path string) error {
		if path == rootOf(s) {
			return failure
		}
		return nil
	}
	s.syncDir = rec.sync
	content := newShard(t, s, "makes a shard, its parent unsynced")
	if _, _, err := put(s, "a1", key, "f.txt", content); !errors.Is(err, failure) {
		t.Fatalf("a failed parent sync: %v", err)
	}
	if got := rec.take(); !slices.Equal(got, []string{shardOf(s, content), rootOf(s)}) {
		t.Fatalf("synced %v", got)
	}
	if !exists(t, shardOf(s, content)) || count(t, s, "SELECT COUNT(*) FROM attachments")+count(t, s, "SELECT COUNT(*) FROM uploads") != 0 {
		t.Fatal("the shard is gone, or a row or a lease is left")
	}
	rec.fail = nil
	mustPut(t, s, "a2", key, "f.txt", content)
	if got := rec.take(); !slices.Equal(got, []string{shardOf(s, content), rootOf(s)}) {
		t.Fatalf("the upload into the shard left behind synced %v", got)
	}
}

// The store's directory is synced at open, which makes the attachments'
// directory's name durable in it. A sync that fails there leaves the store
// serving, SweepError saying why, and every upload tries it again before
// it reads its file until one succeeds: an upload whose try fails is that
// error, its file unread and nothing kept (no receipt, row, lease or
// file); the first whose try succeeds syncs the store's directory, then
// its shard and the attachments' directory, and is taken; the uploads
// after it sync those two alone.
func TestAFailedSyncAtOpenIsTriedAgainBeforeAnUpload(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	failure := errors.New("the store's directory did not sync")
	var failing atomic.Bool
	failing.Store(true)
	rec := &recorder{fail: func(path string) error {
		if path == dir && failing.Load() {
			return failure
		}
		return nil
	}}
	s, err := openWith(dir, func(s *Store) { s.syncDir = rec.sync })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SweepError(); !errors.Is(err, failure) {
		t.Fatalf("the sync at open: %v", err)
	}
	if got := rec.take(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("the open synced %v", got)
	}
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Attached")
	content := []byte("after a failed sync at open")
	var read bytes.Buffer
	if _, _, err := s.AddAttachment(ctx, testOp("a1"), key, "f.txt", "text/plain", io.TeeReader(bytes.NewReader(content), &read)); !errors.Is(err, failure) || read.Len() != 0 {
		t.Fatalf("an upload, the store's directory failing still: %v, %d bytes read", err, read.Len())
	}
	if got := rec.take(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("the upload that failed synced %v", got)
	}
	if _, found, _ := s.Receipt(ctx, "test", "a1"); found || count(t, s, "SELECT COUNT(*) FROM attachments")+count(t, s, "SELECT COUNT(*) FROM uploads") != 0 ||
		len(contentFiles(t, s)) != 0 {
		t.Fatal("an upload whose sync failed kept a receipt, a row, a lease or a file")
	}
	failing.Store(false)
	a := mustPut(t, s, "a2", key, "f.txt", content)
	if got := rec.take(); !slices.Equal(got, []string{dir, shardOf(s, content), rootOf(s)}) {
		t.Fatalf("the upload after it synced %v", got)
	}
	other := []byte("its name durable now")
	mustPut(t, s, "a3", key, "g.txt", other)
	if got := rec.take(); !slices.Equal(got, []string{shardOf(s, other), rootOf(s)}) {
		t.Fatalf("the next upload synced %v", got)
	}
	if !bytes.Equal(readContent(t, s, a.ID), content) {
		t.Fatal("the attachment did not open whole")
	}
}

// An upload held inside its sync of the shard it made, the shard not yet
// synced into its parent, holds no other: a second upload into the same
// shard runs whole meanwhile, syncing the shard and then the parent itself,
// and commits its row; the first, released, syncs its parent and names its
// file.
func TestAnUploadHeldAtItsShardsSyncHoldsNoOther(t *testing.T) {
	s, key := attachmentTicket(t)
	first := newShard(t, s, "held at its shard's sync")
	second := sameShard(first)
	reached, release := make(chan struct{}), make(chan struct{})
	// The first sync alone is held; every other passes at once (a
	// sync.Once would hold them all behind the first).
	var holding atomic.Bool
	rec := &recorder{}
	rec.fail = func(string) error {
		if holding.CompareAndSwap(false, true) {
			close(reached)
			<-release
		}
		return nil
	}
	s.syncDir = rec.sync
	done := make(chan uploaded, 1)
	go func() {
		rc, a, err := put(s, "a1", key, "first.txt", first)
		done <- uploaded{rc, a, err}
	}()
	select {
	case <-reached:
	case u := <-done:
		t.Fatalf("the first upload ended before its sync: %+v", u)
	}
	if got := rec.take(); !slices.Equal(got, []string{shardOf(s, first)}) {
		t.Fatalf("the first upload synced %v before it was held", got)
	}
	b := mustPut(t, s, "a2", key, "second.txt", second)
	if got := rec.take(); !slices.Equal(got, []string{shardOf(s, second), rootOf(s)}) {
		t.Fatalf("the second upload synced %v", got)
	}
	close(release)
	u := <-done
	want(t, u.rc, u.err, StatusDone, "")
	if got := rec.take(); !slices.Equal(got, []string{rootOf(s)}) {
		t.Fatalf("the first upload, released, synced %v", got)
	}
	if !bytes.Equal(readContent(t, s, u.a.ID), first) || !bytes.Equal(readContent(t, s, b.ID), second) {
		t.Fatal("the attachments did not open whole")
	}
}

// publish replaces a file already there, the same content, and is
// errUploadGone when its temp file is gone.
func TestPublishOnUnix(t *testing.T) {
	dir := t.TempDir()
	tmp, dst := filepath.Join(dir, "tmp-1"), filepath.Join(dir, "ab", strings.Repeat("ab", 32))
	for range 2 {
		if err := os.WriteFile(tmp, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := publish(tmp, dst, syncDirectory); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(dst); err != nil || string(b) != "content" || exists(t, tmp) {
			t.Fatalf("published: %q %v, the temp file left %v", b, err, exists(t, tmp))
		}
	}
	if err := publish(tmp, dst, syncDirectory); !errors.Is(err, errUploadGone) {
		t.Fatalf("no temp file: %v", err)
	}
}

// A publication that fails before its file is renamed into place (the
// shard cannot be made; the rename fails) fails the upload, a capture's
// and an attachment's, and leaves no file of it anywhere under the
// store's folder and no lease, at once, before any sweep.
func TestAPublicationThatFailsLeavesNoFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a directory's mode does not bind root")
	}
	s, key := attachmentTicket(t)
	root := rootOf(s)
	t.Cleanup(func() { os.Chmod(root, 0o700) })
	unmakable := func(string) {
		if err := os.Chmod(root, 0o500); err != nil {
			t.Error(err)
		}
	}
	unrenamable := func(sum string) {
		if err := os.MkdirAll(filepath.Join(contentAt(s, sum), "in-the-way"), 0o700); err != nil {
			t.Error(err)
		}
	}
	n, f := testCapture(true)
	tries := 0
	for _, c := range []struct {
		name string
		in   func(sum string)
	}{{"the shard cannot be made", unmakable}, {"the rename fails", unrenamable}} {
		for _, capture := range []bool{true, false} {
			tries++
			content := pngOf(t, 8, 8, byte(tries))
			s.beforePublish = func() { c.in(sumOf(content)) }
			var err error
			if capture {
				_, err = create(t, s, opID("capture", tries), n, f, content)
			} else {
				_, _, err = put(s, opID("attachment", tries), key, "a.png", content)
			}
			s.beforePublish = nil
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err == nil {
				t.Fatalf("%s (capture %v): the upload was taken", c.name, capture)
			}
			noUploads(t, s)
			if err := filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || !d.Type().IsRegular() {
					return err
				}
				if b, err := os.ReadFile(p); err != nil || bytes.Equal(b, content) {
					t.Errorf("%s (capture %v): the upload's content is left at %s (%v)", c.name, capture, p, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Dir(contentAt(s, sumOf(content)))); err != nil {
				t.Fatal(err)
			}
		}
	}
}
