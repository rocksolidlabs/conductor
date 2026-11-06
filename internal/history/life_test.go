package history

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// hang bounds a wait that only a hang would reach: what the test waits
// for needs no more than the steps before it, and a wait past it is the
// defect the test is for, not a slow machine.
const hang = 2 * time.Minute

// within waits for ch's value, failing the test (after release, so
// nothing it holds is left held) when it does not come before hang.
func within[T any](t *testing.T, what string, ch <-chan T, release func()) T {
	t.Helper()
	timer := time.NewTimer(hang)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
		release()
		t.Fatalf("%s: still waiting after %v", what, hang)
		panic("unreachable")
	}
}

// notYet fails the test if ch has a value: what sends it must still be
// waiting for a step the test has not taken.
func notYet[T any](t *testing.T, what string, ch <-chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("%s returned (%v) before the step it waits for", what, v)
	default:
	}
}

// waitQueued waits until n calls wait for a connection of db: the pool's
// own count, the mechanism, never a sleep's guess. It fails the test
// (after release) when they do not before hang.
func waitQueued(t *testing.T, db *sql.DB, n int64, release func()) {
	t.Helper()
	deadline := time.Now().Add(hang)
	for db.Stats().WaitCount < n {
		if time.Now().After(deadline) {
			release()
			t.Fatalf("%d calls never queued for a connection", n)
		}
		time.Sleep(time.Millisecond)
	}
}

// gate holds the first n calls of a hook until release, each signalled
// on held as it arrives; later calls pass.
type gate struct {
	n, seen int
	mu      sync.Mutex
	held    chan struct{}
	open    chan struct{}
	once    sync.Once
}

func newGate(n int) *gate {
	return &gate{n: n, held: make(chan struct{}, n), open: make(chan struct{})}
}

func (g *gate) hook() {
	g.mu.Lock()
	g.seen++
	hold := g.seen <= g.n
	g.mu.Unlock()
	if hold {
		g.held <- struct{}{}
		<-g.open
	}
}

func (g *gate) release() { g.once.Do(func() { close(g.open) }) }

// arrived waits for the gate's first n calls to arrive.
func (g *gate) arrived(t *testing.T) {
	t.Helper()
	for range g.n {
		within(t, "a call at the gate", (<-chan struct{})(g.held), g.release)
	}
}

type answer struct {
	p   Page
	err error
}

func searchAsync(ix *Index, ctx context.Context, line string) <-chan answer {
	ch := make(chan answer, 1)
	go func() {
		q, _ := ParseQuery(line)
		p, err := ix.Search(ctx, q)
		ch <- answer{p, err}
	}()
	return ch
}

func async(fn func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	return ch
}

// Close returns only once a Replace it let begin has ended: one held
// before its commit commits first, and is in the file for the next
// owner. A second Close, made while the first waits, returns only once
// the first has closed the index, and a third, after, at once; a call
// after Close is refused.
func TestCloseWaitsForAWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ix := openIndex(t, dir, Options{})
	g := newGate(1)
	ix.beforeCommit = func() error { g.hook(); return nil }
	replaced := async(func() error {
		return ix.Replace(ctx, Source{Key: "session:s1", Mark: "m1"}, []Entry{chat("session:s1#e1", "s1", "alpha", t0)}, nil)
	})
	g.arrived(t)
	closing, waits := make(chan struct{}), make(chan struct{})
	ix.closing = func() { close(closing) }
	var once sync.Once
	ix.closeWaits = func() { once.Do(func() { close(waits) }) }
	first := async(ix.Close)
	within(t, "the first Close", (<-chan struct{})(closing), g.release)
	second := async(ix.Close)
	within(t, "the second Close", (<-chan struct{})(waits), g.release)
	// Both refuse new calls now and wait for the Replace, which waits for
	// the gate: neither can have returned.
	notYet(t, "the first Close", first)
	notYet(t, "the second Close", second)
	wantErr(t, ix.Replace(ctx, Source{Key: "session:s2"}, nil, nil), ErrClosed)
	g.release()
	if err := within(t, "the held Replace", replaced, g.release); err != nil {
		t.Fatalf("the held Replace: %v", err)
	}
	if err := within(t, "the second Close", second, g.release); err != nil {
		t.Fatal(err)
	}
	// The second Close returned: the index is closed, its connections
	// with it, whichever Close the scheduler ran first.
	if err := ix.db.Ping(); err == nil {
		t.Fatal("the second Close returned before the index was closed")
	}
	if err := within(t, "the first Close", first, g.release); err != nil {
		t.Fatal(err)
	}
	if err := ix.Close(); err != nil {
		t.Fatalf("a Close after: %v", err)
	}
	_, err := ix.Search(ctx, Query{})
	wantErr(t, err, ErrClosed)
	next := openIndex(t, dir, Options{})
	if owed, err := next.Unchecked(ctx); err != nil || owed {
		t.Fatalf("closed, it owes a check: %v %v", owed, err)
	}
	wantKeys(t, next, "alpha", "session:s1#e1")
}

// Close waits for a search it let begin, the owner's and a reader's: the
// search answers its rows, and Close returns after it.
func TestCloseWaitsForASearch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	owner := openIndex(t, dir, Options{})
	put(t, owner, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	reader, err := Open(ctx, filepath.Join(dir, File), Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, ix := range []*Index{reader, owner} {
		g := newGate(1)
		ix.inRead = g.hook
		searched := searchAsync(ix, ctx, "alpha")
		g.arrived(t)
		closing := make(chan struct{})
		ix.closing = func() { close(closing) }
		closed := async(ix.Close)
		within(t, "Close", (<-chan struct{})(closing), g.release)
		notYet(t, "Close", closed)
		g.release()
		a := within(t, "the held search", searched, g.release)
		if a.err != nil || len(a.p.Entries) != 1 {
			t.Fatalf("the held search: %v %v", keysOf(a.p.Entries), a.err)
		}
		if err := within(t, "Close", closed, g.release); err != nil {
			t.Fatal(err)
		}
		_, err := ix.Search(ctx, Query{})
		wantErr(t, err, ErrClosed)
	}
}

// Close waits for a Clear it let begin: the Clear commits, and the next
// owner finds the index cleared and owes it no Check.
func TestCloseWaitsForAClear(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ix := openIndex(t, dir, Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	before, err := ix.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := newGate(1)
	ix.beforeClear = func() error { g.hook(); return nil }
	cleared := async(func() error { return ix.Clear(ctx) })
	g.arrived(t)
	closing := make(chan struct{})
	ix.closing = func() { close(closing) }
	closed := async(ix.Close)
	within(t, "Close", (<-chan struct{})(closing), g.release)
	notYet(t, "Close", closed)
	g.release()
	if err := within(t, "the held Clear", cleared, g.release); err != nil {
		t.Fatal(err)
	}
	if err := within(t, "Close", closed, g.release); err != nil {
		t.Fatal(err)
	}
	next := openIndex(t, dir, Options{})
	st, err := next.Stats(ctx)
	if err != nil || st.State != "" || st.Entries != 0 || st.Generation != before.Generation+1 {
		t.Fatalf("after a Clear and Close: %+v %v (generation before %d)", st, err, before.Generation)
	}
}

// Close waits for a Check it let begin, and the Check, a write beside it
// and a second Check each do what they say: a Replace commits while the
// Check reads, a second Check waits for the first as its context allows,
// and the Check ends the owed state before Close marks the index closed.
func TestCloseWaitsForACheck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ix := openIndex(t, dir, Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	crash(ix)
	ix = openIndex(t, dir, Options{})
	g := newGate(1)
	ix.inCheck = g.hook
	type result struct {
		got string
		err error
	}
	checked := make(chan result, 1)
	go func() {
		got, err := ix.Check(ctx)
		checked <- result{got, err}
	}()
	g.arrived(t)
	wctx, cancel := context.WithTimeout(ctx, hang)
	defer cancel()
	if err := ix.Replace(wctx, Source{Key: "session:s2", Mark: "m1"}, []Entry{chat("session:s2#e1", "s2", "alpha", t0)}, nil); err != nil {
		t.Fatalf("a Replace beside a Check: %v", err)
	}
	done, stop := context.WithCancel(ctx)
	stop()
	if _, err := ix.Check(done); !errors.Is(err, context.Canceled) {
		t.Fatalf("a second Check while one runs, its context ended: %v", err)
	}
	closing := make(chan struct{})
	ix.closing = func() { close(closing) }
	closed := async(ix.Close)
	within(t, "Close", (<-chan struct{})(closing), g.release)
	notYet(t, "Close", closed)
	g.release()
	if r := within(t, "the held Check", (<-chan result)(checked), g.release); r.err != nil || r.got != CheckSound {
		t.Fatalf("the held Check: %q %v", r.got, r.err)
	}
	if err := within(t, "Close", closed, g.release); err != nil {
		t.Fatal(err)
	}
	next := openIndex(t, dir, Options{})
	if owed, err := next.Unchecked(ctx); err != nil || owed {
		t.Fatalf("checked and closed, it owes a check: %v %v", owed, err)
	}
	wantSameKeys(t, next, "alpha", "session:s1#e1", "session:s2#e1")
}

// damage overwrites pages of the index at dir past its schema's, as a
// failing disk would leave them; the index holds 400 entries first.
func damage(t *testing.T, dir string) {
	t.Helper()
	ix := openIndex(t, dir, Options{})
	var es []Entry
	for i := range 400 {
		es = append(es, chat("session:s1#e"+string(rune('a'+i%26))+string(rune('a'+i/26)), "s1", "alpha beta gamma delta epsilon", t0))
	}
	put(t, ix, "session:s1", es...)
	ix.Close()
	f, err := os.OpenFile(filepath.Join(dir, File), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	junk := make([]byte, 4096*4)
	for i := range junk {
		junk[i] = byte(i * 7)
	}
	if _, err := f.WriteAt(junk, fi.Size()/2); err != nil {
		t.Fatal(err)
	}
}

// failClear makes every clear of the damaged file meet damage, as a
// clear of a file damaged past its schema can (and as the real one may
// before it reaches the seam): Check then moves the file aside. A clear
// once moved is set (Check's move aside has begun) is of the new file and
// goes through.
func failClear(ix *Index, moved *atomic.Bool) {
	ix.beforeClear = func() error {
		if moved.Load() {
			return nil
		}
		return errors.New("database disk image is malformed")
	}
}

// A Check that must move the file aside and a Close, in either order.
// Close first: the Check, refused, leaves the file where it is, and Close
// leaves it marked open, so its next owner checks it again. The Check
// first: Close waits for the move aside and marks the new file closed.
func TestCloseDuringAMoveAside(t *testing.T) {
	ctx := context.Background()
	t.Run("close first", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, File)
		damage(t, dir)
		ix := openIndex(t, dir, Options{})
		failClear(ix, new(atomic.Bool))
		g := newGate(1)
		ix.inCheck = g.hook
		checked := async(func() error { _, err := ix.Check(ctx); return err })
		g.arrived(t)
		closing := make(chan struct{})
		ix.closing = func() { close(closing) }
		closed := async(ix.Close)
		within(t, "Close", (<-chan struct{})(closing), g.release)
		notYet(t, "Close", closed)
		g.release()
		wantErr(t, within(t, "the Check", checked, g.release), ErrClosed)
		if err := within(t, "Close", closed, g.release); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path + damaged); !os.IsNotExist(err) {
			t.Fatalf("a Check refused by Close moved the file aside: %v", err)
		}
		next := openIndex(t, dir, Options{})
		if owed, err := next.Unchecked(ctx); err != nil || !owed {
			t.Fatalf("damaged and not repaired, it owes no check: %v %v", owed, err)
		}
	})
	t.Run("the check first", func(t *testing.T) {
		dir := t.TempDir()
		damage(t, dir)
		ix := openIndex(t, dir, Options{})
		var moved atomic.Bool
		failClear(ix, &moved)
		g := newGate(1)
		ix.moving = func() {
			moved.Store(true)
			g.hook()
		}
		type result struct {
			got string
			err error
		}
		checked := make(chan result, 1)
		go func() {
			got, err := ix.Check(ctx)
			checked <- result{got, err}
		}()
		g.arrived(t)
		closing := make(chan struct{})
		ix.closing = func() { close(closing) }
		closed := async(ix.Close)
		within(t, "Close", (<-chan struct{})(closing), g.release)
		notYet(t, "Close", closed)
		g.release()
		if r := within(t, "the Check", (<-chan result)(checked), g.release); r.err != nil || r.got != CheckMovedAside {
			t.Fatalf("the Check: %q %v", r.got, r.err)
		}
		if err := within(t, "Close", closed, g.release); err != nil {
			t.Fatal(err)
		}
		next := openIndex(t, dir, Options{})
		if owed, err := next.Unchecked(ctx); err != nil || owed {
			t.Fatalf("made again and closed, it owes a check: %v %v", owed, err)
		}
		if n := count(t, next, "entries"); n != 0 {
			t.Fatalf("%d entries in the file made again", n)
		}
	})
}

// A write waits for the write before it, never for a read: with both of
// the owner's readers held inside their searches, a third search queued
// for a reader and a fourth given up while queued, a Replace and a
// Remove commit at once; a search beside a write held before its commit
// answers at once, from the snapshot before it.
func TestAWriteNeverWaitsBehindSearches(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	put(t, ix, "session:s3", chat("session:s3#e1", "s3", "alpha", t0))
	g := newGate(readConns)
	ix.inRead = g.hook
	var held []<-chan answer
	for range readConns {
		held = append(held, searchAsync(ix, ctx, "alpha"))
	}
	g.arrived(t)
	queued := searchAsync(ix, ctx, "alpha")
	waitQueued(t, ix.rdb, 1, g.release)
	given, giveUp := context.WithCancel(ctx)
	gaveUp := searchAsync(ix, given, "alpha")
	waitQueued(t, ix.rdb, 2, g.release)
	giveUp()
	if a := within(t, "a search given up while queued", gaveUp, g.release); !errors.Is(a.err, context.Canceled) {
		t.Fatalf("a search given up while queued: %v", a.err)
	}
	replaced := async(func() error {
		return ix.Replace(ctx, Source{Key: "session:s2", Mark: "m1"}, []Entry{chat("session:s2#e1", "s2", "alpha", t0.Add(time.Hour))}, nil)
	})
	if err := within(t, "a Replace beside held and queued searches", replaced, g.release); err != nil {
		t.Fatal(err)
	}
	removed := async(func() error { return ix.Remove(ctx, "session:s3") })
	if err := within(t, "a Remove beside held and queued searches", removed, g.release); err != nil {
		t.Fatal(err)
	}
	g.release()
	for _, ch := range held {
		if a := within(t, "a held search", ch, g.release); a.err != nil || len(a.p.Entries) != 2 {
			t.Fatalf("a held search: %v %v, want its own snapshot", keysOf(a.p.Entries), a.err)
		}
	}
	if a := within(t, "the queued search", queued, g.release); a.err != nil {
		t.Fatalf("the queued search: %v", a.err)
	}
	wantKeys(t, ix, "alpha", "session:s2#e1", "session:s1#e1")

	// A write held before its commit: a second write waits for it, a
	// search does not.
	w := newGate(1)
	ix.inRead = nil
	ix.beforeCommit = func() error { w.hook(); return nil }
	first := async(func() error {
		return ix.Replace(ctx, Source{Key: "session:s4", Mark: "m1"}, []Entry{chat("session:s4#e1", "s4", "alpha", t0.Add(2*time.Hour))}, nil)
	})
	w.arrived(t)
	waits := ix.db.Stats().WaitCount
	second := async(func() error { return ix.Remove(ctx, "session:s1") })
	waitQueued(t, ix.db, waits+1, w.release)
	searched := searchAsync(ix, ctx, "alpha")
	if a := within(t, "a search beside a held write", searched, w.release); a.err != nil || len(a.p.Entries) != 2 {
		t.Fatalf("a search beside a held write: %v %v", keysOf(a.p.Entries), a.err)
	}
	notYet(t, "a write behind a held write", second)
	w.release()
	for _, ch := range []<-chan error{first, second} {
		if err := within(t, "a write", ch, w.release); err != nil {
			t.Fatal(err)
		}
	}
	wantKeys(t, ix, "alpha", "session:s4#e1", "session:s2#e1")
}

// Clears that overlap, the second waiting for the first's commit, leave
// the index owed no Check and its searches answering (a mark of the
// owner's open made after a clear's commit, in a transaction of its own,
// read the owner's own open as a crash); a search held across both
// answers its snapshot from before them.
func TestOverlappingClearsOweNoCheck(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	before, err := ix.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := newGate(1)
	ix.inRead = r.hook
	searched := searchAsync(ix, ctx, "alpha")
	r.arrived(t)
	g := newGate(1)
	ix.beforeClear = func() error { g.hook(); return nil }
	a := async(func() error { return ix.Clear(ctx) })
	g.arrived(t)
	waits := ix.db.Stats().WaitCount
	b := async(func() error { return ix.Clear(ctx) })
	waitQueued(t, ix.db, waits+1, func() { g.release(); r.release() })
	g.release()
	for _, ch := range []<-chan error{a, b} {
		if err := within(t, "a Clear", ch, g.release); err != nil {
			t.Fatal(err)
		}
	}
	r.release()
	if got := within(t, "the held search", searched, r.release); got.err != nil || len(got.p.Entries) != 1 {
		t.Fatalf("the held search: %v %v, want its snapshot", keysOf(got.p.Entries), got.err)
	}
	if owed, err := ix.Unchecked(ctx); err != nil || owed {
		t.Fatalf("after two Clears: owed %v %v", owed, err)
	}
	put(t, ix, "session:s2", chat("session:s2#e1", "s2", "alpha", t0))
	if p := search(t, ix, "alpha", 0); p.State != "" || len(p.Entries) != 1 {
		t.Fatalf("after two Clears: state %q, %v", p.State, keysOf(p.Entries))
	}
	if st, err := ix.Stats(ctx); err != nil || st.Generation != before.Generation+2 {
		t.Fatalf("after two Clears: %+v %v (generation before %d)", st, err, before.Generation)
	}
}

// A reader whose file is moved aside and made again while one of its
// searches runs: the search answers the old file's rows, and the next
// opens the new file once the first has ended.
func TestAReaderReopensBesideASearch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	a := openIndex(t, dir, Options{})
	put(t, a, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	reader, err := Open(ctx, path, Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	g := newGate(1)
	reader.inRead = g.hook
	held := searchAsync(reader, ctx, "alpha")
	g.arrived(t)
	a.Close()
	if err := moveAside(path); err != nil {
		t.Fatal(err)
	}
	b := openIndex(t, dir, Options{})
	put(t, b, "session:s2", chat("session:s2#e1", "s2", "alpha", t0))
	next := searchAsync(reader, ctx, "alpha")
	g.release()
	if got := within(t, "the held search", held, g.release); got.err != nil || len(got.p.Entries) != 1 || got.p.Entries[0].Key != "session:s1#e1" {
		t.Fatalf("the held search: %v %v", keysOf(got.p.Entries), got.err)
	}
	if got := within(t, "the next search", next, g.release); got.err != nil || len(got.p.Entries) != 1 || got.p.Entries[0].Key != "session:s2#e1" {
		t.Fatalf("the next search: %v %v", keysOf(got.p.Entries), got.err)
	}
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
