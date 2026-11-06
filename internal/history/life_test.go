package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// crash ends an owner as a killed process would: its connections gone,
// the index never marked closed.
func crash(ix *Index) {
	ix.mu.Lock()
	ix.closed = true
	ix.mu.Unlock()
	ix.rdb.Close()
	ix.db.Close()
}

// Close returns only once a Replace it let begin has ended: one held
// before its commit commits first, and is in the file for the next
// owner; a call after Close is refused.
func TestCloseWaitsForAWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ix := openIndex(t, dir, Options{})
	held, release := make(chan struct{}), make(chan struct{})
	ix.beforeCommit = func() error {
		close(held)
		<-release
		return nil
	}
	replaced := make(chan error, 1)
	go func() {
		replaced <- ix.Replace(ctx, Source{Key: "session:s1", Mark: "m1"}, []Entry{chat("session:s1#e1", "s1", "alpha", t0)}, nil)
	}()
	<-held
	closing := make(chan struct{})
	ix.closing = func() { close(closing) }
	closed := make(chan error, 1)
	go func() { closed <- ix.Close() }()
	<-closing
	// Close refuses new calls now and waits for the Replace, which waits
	// for release: it cannot have returned.
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while a Replace it let begin was still to commit", err)
	default:
	}
	close(release)
	if err := <-replaced; err != nil {
		t.Fatalf("the held Replace: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	wantErr(t, ix.Replace(ctx, Source{Key: "session:s2"}, nil, nil), ErrClosed)
	_, err := ix.Search(ctx, Query{})
	wantErr(t, err, ErrClosed)
	next := openIndex(t, dir, Options{})
	wantKeys(t, next, "alpha", "session:s1#e1")
}

// A search holds a reader of the owner's own, never its writer: a
// Replace made while a search is held inside its read transaction
// commits at once, and the held search answers the snapshot it began
// with.
func TestASearchNeverHoldsAWriteBack(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	inside, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	ix.inRead = func() {
		once.Do(func() {
			close(inside)
			<-release
		})
	}
	type answer struct {
		p   Page
		err error
	}
	searched := make(chan answer, 1)
	go func() {
		q, _ := ParseQuery("alpha")
		p, err := ix.Search(ctx, q)
		searched <- answer{p, err}
	}()
	<-inside
	// The bound only bounds a hang: a Replace that waited for the held
	// search would wait until release, which comes after it.
	wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := ix.Replace(wctx, Source{Key: "session:s2", Mark: "m1"}, []Entry{chat("session:s2#e1", "s2", "alpha", t0.Add(time.Hour))}, nil); err != nil {
		t.Fatalf("a Replace beside a held search: %v", err)
	}
	close(release)
	a := <-searched
	if a.err != nil || len(a.p.Entries) != 1 || a.p.Entries[0].Key != "session:s1#e1" {
		t.Fatalf("the held search: %v %v, want its own snapshot", keysOf(a.p.Entries), a.err)
	}
	wantKeys(t, ix, "alpha", "session:s2#e1", "session:s1#e1")
}

// An owner's Open whose context ends while it prepares the file answers
// the context's error and leaves the file as it was: not moved aside, not
// cleared; so does a Check whose context has ended.
func TestACancelledOpenKeepsTheIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	ix := openIndex(t, dir, Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	ix.Close()
	ctx, cancel := context.WithCancel(context.Background())
	testBeforePrepare = func(context.Context) { cancel() }
	_, err := Open(ctx, path, Options{})
	testBeforePrepare = nil
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled Open: %v", err)
	}
	if _, err := os.Stat(path + damaged); !os.IsNotExist(err) {
		t.Fatalf("a cancelled Open moved the index aside: %v", err)
	}
	ix = openIndex(t, dir, Options{})
	wantKeys(t, ix, "alpha", "session:s1#e1")
	ix.Close()

	// An index not closed, owed a Check, and the Check's context ended.
	ix = openIndex(t, dir, Options{})
	crash(ix)
	ix = openIndex(t, dir, Options{})
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ix.Check(done); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled Check: %v", err)
	}
	if owed, err := ix.Unchecked(context.Background()); err != nil || !owed {
		t.Fatalf("after a cancelled Check: owed %v, %v", owed, err)
	}
	if _, err := ix.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, ix, "alpha", "session:s1#e1")
}

// The owner's Open does not check the file. An index its last owner did
// not close is owed a Check: until Check finds it sound its searches (the
// owner's and a reader's) answer StateChecking with no rows and Entry
// ErrNotReady, while its writes, Mark, Sources and Stats go on; a clean
// Close owes the next owner none.
func TestAnIndexNotClosedIsCheckedBeforeItIsSearched(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	ix := openIndex(t, dir, Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	if owed, err := ix.Unchecked(ctx); err != nil || owed {
		t.Fatalf("a new index owes a check: %v %v", owed, err)
	}
	crash(ix)

	ix = openIndex(t, dir, Options{})
	reader, err := Open(ctx, path, Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if owed, err := ix.Unchecked(ctx); err != nil || !owed {
		t.Fatalf("an index not closed owes no check: %v %v", owed, err)
	}
	for _, x := range []*Index{ix, reader} {
		if p := search(t, x, "alpha", 0); p.State != StateChecking || len(p.Entries) != 0 {
			t.Fatalf("an unchecked index searched: state %q, %v", p.State, keysOf(p.Entries))
		}
		if _, err := x.Entry(ctx, "session:s1#e1"); !errors.Is(err, ErrNotReady) {
			t.Fatalf("an entry of an unchecked index: %v", err)
		}
		if st, err := x.Stats(ctx); err != nil || st.State != StateChecking || st.Entries != 1 {
			t.Fatalf("the stats of an unchecked index: %+v %v", st, err)
		}
		if mark, ok, err := x.Mark(ctx, "session:s1"); err != nil || !ok || mark != "m1" {
			t.Fatalf("a mark of an unchecked index: %q %v %v", mark, ok, err)
		}
	}
	put(t, ix, "session:s2", chat("session:s2#e1", "s2", "alpha", t0.Add(time.Hour)))
	if got, err := ix.Check(ctx); err != nil || got != CheckSound {
		t.Fatalf("Check: %q %v", got, err)
	}
	for _, x := range []*Index{ix, reader} {
		wantKeys(t, x, "alpha", "session:s2#e1", "session:s1#e1")
	}
	ix.Close()
	ix = openIndex(t, dir, Options{})
	if owed, err := ix.Unchecked(ctx); err != nil || owed {
		t.Fatalf("an index closed owes a check: %v %v", owed, err)
	}
	wantKeys(t, ix, "alpha", "session:s2#e1", "session:s1#e1")
}

// A file SQLite opens but finds damaged when Check reads it whole is
// cleared in place, or moved aside when the clear meets the damage too:
// either way the index is empty and sound, owed no check, and a cursor of
// it before is stale.
func TestCheckClearsADamagedIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	ix := openIndex(t, dir, Options{})
	var es []Entry
	for i := range 400 {
		es = append(es, chat("session:s1#e"+string(rune('a'+i%26))+string(rune('a'+i/26)), "s1", "alpha beta gamma delta epsilon", t0))
	}
	put(t, ix, "session:s1", es...)
	first := search(t, ix, "alpha", 1)
	if first.Next == "" {
		t.Fatal("no second page")
	}
	ix.Close()
	// Pages past the schema's first overwritten, as a failing disk would
	// leave them.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := f.Stat()
	junk := make([]byte, 4096*4)
	for i := range junk {
		junk[i] = byte(i * 7)
	}
	if _, err := f.WriteAt(junk, fi.Size()/2); err != nil {
		t.Fatal(err)
	}
	f.Close()
	ix = openIndex(t, dir, Options{})
	got, err := ix.Check(ctx)
	if err != nil || (got != CheckCleared && got != CheckMovedAside) {
		t.Fatalf("Check of a damaged index: %q %v", got, err)
	}
	if n := count(t, ix, "entries"); n != 0 {
		t.Fatalf("%d entries kept", n)
	}
	ftsOK(t, ix)
	if owed, err := ix.Unchecked(ctx); err != nil || owed {
		t.Fatalf("after Check: owed %v %v", owed, err)
	}
	put(t, ix, "session:s1", es...)
	q, _ := ParseQuery("alpha")
	q.After = first.Next
	_, err = ix.Search(ctx, q)
	wantErr(t, err, ErrStaleCursor)
	if got, err := ix.Check(ctx); err != nil || got != CheckSound {
		t.Fatalf("Check again: %q %v", got, err)
	}
}
