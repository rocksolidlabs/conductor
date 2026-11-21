package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The index of what the server holds captured (docs/plans/sync-seams.md,
// OS-1c): fingerprints of every source's text, never the text, in a file
// of its own (output-index.sqlite in the data directory, never exported
// and never on the sync allowlist), read by the copied-output check
// (outputGuard) to tell whether a value quotes captured output.
//
// A source's text, as Conductor captured it, is made plain (escape
// sequences removed, control characters dropped, a carriage return read as
// a line's end) and its whitespace folded (each run of it one space).
// Every window of indexWindow bytes of that text is hashed, and of each
// run of indexRun consecutive windows the smallest hash is kept (the
// rightmost on a tie): winnowing, which keeps at least one fingerprint of
// any run of indexWindow+indexRun-1 = 64 bytes the text holds, so a value
// sharing such a run with a source shares a fingerprint with it. Every
// line of at least indexLine bytes is hashed whole too. Each fingerprint
// names its source and its offset in the source's text, and a hit is
// confirmed against that text (memory for a live source, its reader for
// a kept one) before it counts: one that cannot be confirmed counts.
//
// It fails closed: while it is past its bound, being rebuilt, or behind
// the output that arrived (fed and not yet indexed past a wait), the
// check answers pending, never clean.

// The winnowing's parameters: a run of indexMatch bytes common to a value
// and a source always shares a fingerprint (indexWindow + indexRun - 1).
const (
	indexWindow = 32
	indexRun    = 33
	indexMatch  = indexWindow + indexRun - 1
	indexLine   = 16
)

// The kinds of fingerprint.
const (
	fpWindow = 0
	fpLine   = 1
)

// indexFile is the index's file in the data directory.
const indexFile = "output-index.sqlite"

// defaultIndexBound is the index's bound on disk (1 GiB, past the sum of
// the sources' own caps in an ordinary data directory).
const defaultIndexBound = 1 << 30

// indexCacheMax bounds the cache of hashes read from the file.
const indexCacheMax = 1 << 16

// The verdicts of the copied-output check, as Dispatch's rows keep them.
const (
	verdictClean   = "clean"
	verdictOutput  = "output"
	verdictPending = "pending"
)

// fpRef is a fingerprint's place: its source, its offset in the source's
// folded text and its kind; n is a line's length.
type fpRef struct {
	source string
	off    int64
	kind   int
	n      int
}

// liveSource is a source whose text the server holds in memory (a
// session's ring as rendered, a screen's rows): its folded text from base
// on, at most cap bytes, and the winnowing's state across the feeds.
type liveSource struct {
	text    []byte
	base    int64
	cap     int
	plain   plainState
	space   bool   // the last byte of text is a folded space
	line    []byte // the line being read, plain and folded
	lineOff int64  // where it begins in the folded text
	wins    []winHash
	picked  int64
	pickedH uint64
	refs    []fpHash // the fingerprints it holds, for its prune
}

type winHash struct {
	pos int64
	h   uint64
}

type fpHash struct {
	h   uint64
	ref fpRef
}

// outputIndex is the index. Feeds are queued (feed never waits on the
// file) and indexed by its own goroutine; a check waits for the feeds
// queued before it, up to indexCatchUp, and is pending past that.
type outputIndex struct {
	path  string
	bound int64

	mu sync.Mutex
	db *sql.DB
	// live is every live source by name; mem their fingerprints by hash.
	live map[string]*liveSource
	mem  map[uint64][]fpRef
	// cache holds what the file answered for a hash (nil for none).
	cache map[uint64][]fpRef
	// readers give a kept source's text by its kind (the part of its name
	// before the first colon), for a hit's confirmation.
	readers map[string]func(id string) ([]byte, bool)
	// pending is the feeds taken and not yet indexed, in order; taken
	// counts every feed taken and done every one indexed, progress is
	// closed (and made again) at each one indexed, and wake tells the
	// index's goroutine there is work. A feed never waits. A live source
	// has at most one feed queued (rings): a chunk arriving while it is
	// queued is joined to it, held to what the source keeps, and the bytes
	// queued across sources (queued) are held to maxQueued: past either,
	// the bytes let go are a prune of the source, as a ring's own cut is.
	// maxQueued is the bound across sources (defaultMaxQueued; a test's
	// smaller one, set as the index opens).
	pending   []func()
	rings     map[string]*ringFeed
	queued    int
	maxQueued int
	// fed is the bytes fed to each live source; holes the live sources
	// whose queued bytes were let go below what their rings still hold
	// (cut by the bound across sources), each until fed reaches its mark,
	// when the ring has turned over past the cut. While any source is
	// holed no text is judged clean: the index is not whole.
	fed     map[string]int64
	holes   map[string]*hole
	holeGen int
	taken   uint64
	// waited counts the checks that waited for the feeds (whole): a test's
	// seam, to show a check made under the ledger's lock never does.
	waited atomic.Int64
	// readsCut counts the checks that met maxSourceReads (quotesIf).
	readsCut atomic.Int64
	done     uint64
	progress chan struct{}
	wake     chan struct{}
	// screens gives every live screen's rows by source (screen:<session>),
	// read whole at a check (watchScreens); shots the screens whose
	// snapshot is queued and not yet taken, and lastShot each screen's
	// last snapshot's rows.
	screens  func() map[string][]string
	shots    map[string]bool
	lastShot map[string]string
	// rebuilding says the index is being made again from the sources;
	// saturated that its file is past its bound; broken why the file
	// cannot be used ("" for none).
	rebuilding bool
	// fresh says the file was not there (or could not be read) at the open:
	// the index is rebuilt from the kept sources (rebuild).
	fresh     bool
	saturated bool
	broken    string
	closed    bool
	wg        sync.WaitGroup
	// now is the clock of a prune's time.
	now func() time.Time
}

// indexCatchUp bounds a check's wait for the feeds queued before it.
const indexCatchUp = 2 * time.Second

const indexSchema = `
CREATE TABLE IF NOT EXISTS fp (h INTEGER NOT NULL, source TEXT NOT NULL, off INTEGER NOT NULL, kind INTEGER NOT NULL, n INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS fp_h ON fp (h);
CREATE INDEX IF NOT EXISTS fp_source ON fp (source, off);
CREATE TABLE IF NOT EXISTS prunes (source TEXT NOT NULL, upto INTEGER NOT NULL, at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS prunes_at ON prunes (at);
`

// openOutputIndex opens the index in dir (its file made when absent),
// bounded at bound bytes (defaultIndexBound for 0). A live source of a
// process before this one (a ring, a screen) went with that process, so
// its fingerprints are let go at the open, each source's prune recorded
// with the open's time.
func openOutputIndex(dir string, bound int64) (*outputIndex, error) {
	if bound <= 0 {
		bound = defaultIndexBound
	}
	x := &outputIndex{path: filepath.Join(dir, indexFile), bound: bound, live: map[string]*liveSource{}, mem: map[uint64][]fpRef{},
		cache: map[uint64][]fpRef{}, readers: map[string]func(string) ([]byte, bool){}, progress: make(chan struct{}),
		shots: map[string]bool{}, lastShot: map[string]string{}, rings: map[string]*ringFeed{}, maxQueued: defaultMaxQueued,
		fed: map[string]int64{}, holes: map[string]*hole{},
		wake: make(chan struct{}, 1), now: time.Now}
	if _, err := os.Stat(x.path); errors.Is(err, os.ErrNotExist) {
		x.fresh = true
	}
	if err := x.open(); err != nil {
		// A file that cannot be read is made again from the sources.
		for _, p := range []string{x.path, x.path + "-wal", x.path + "-shm"} {
			os.Remove(p)
		}
		x.fresh = true
		if err := x.open(); err != nil {
			x.broken = err.Error()
		}
	}
	// A fresh index holds none of the kept sources until it is rebuilt:
	// until then it answers pending.
	x.rebuilding = x.fresh
	x.wg.Add(1)
	go x.run()
	return x, nil
}

func (x *outputIndex) open() error {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: x.path, RawQuery: q.Encode()}).String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(indexSchema); err != nil {
		db.Close()
		return fmt.Errorf("the output index: %w", err)
	}
	if err := os.Chmod(x.path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		db.Close()
		return err
	}
	at := stampIndex(x.now())
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO prunes (source, upto, at) SELECT DISTINCT source, -1, ? FROM fp WHERE source LIKE 'ring:%' OR source LIKE 'screen:%'`, at); err != nil {
		db.Close()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM fp WHERE source LIKE 'ring:%' OR source LIKE 'screen:%'`); err != nil {
		db.Close()
		return err
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return err
	}
	x.db = db
	x.saturated = x.sizeLocked() > x.bound
	return nil
}

// stampIndex is a time as the index keeps it.
func stampIndex(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// run indexes the queued feeds, in order, until close has been asked
// and none is left.
func (x *outputIndex) run() {
	defer x.wg.Done()
	for {
		x.mu.Lock()
		if len(x.pending) == 0 {
			closed := x.closed
			x.mu.Unlock()
			if closed {
				return
			}
			<-x.wake
			continue
		}
		f := x.pending[0]
		x.pending[0] = nil
		x.pending = x.pending[1:]
		x.mu.Unlock()
		f()
		x.mu.Lock()
		x.done++
		close(x.progress)
		x.progress = make(chan struct{})
		x.mu.Unlock()
	}
}

// enqueue takes f to be run in order by the index's goroutine, never
// waiting; false once closed.
func (x *outputIndex) enqueue(f func()) bool {
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return false
	}
	x.pending = append(x.pending, f)
	x.taken++
	x.mu.Unlock()
	select {
	case x.wake <- struct{}{}:
	default:
	}
	return true
}

// close ends the index's goroutine and its file.
func (x *outputIndex) close() error {
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return nil
	}
	x.closed = true
	x.mu.Unlock()
	select {
	case x.wake <- struct{}{}:
	default:
	}
	x.wg.Wait()
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.db != nil {
		return x.db.Close()
	}
	return nil
}

// reader gives the kept sources of kind (a check's log, a history
// record) their text, for a hit's confirmation.
func (x *outputIndex) reader(kind string, f func(id string) ([]byte, bool)) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.readers[kind] = f
}

// ringFeed is a live source's bytes queued and not yet indexed, and
// whether some were let go before the index took them.
type ringFeed struct {
	buf  []byte
	lost bool
}

// defaultMaxQueued bounds the bytes of live sources queued across them.
const defaultMaxQueued = 64 << 20

// feed takes captured bytes of the live source name (a session's ring,
// `ring:<id>`), at most keep bytes of whose folded text it holds; never
// waits on the file. A chunk arriving while the source's feed is queued
// joins it; the queued bytes are held to keep and a quarter (what the
// source itself holds before it cuts), and to maxQueued across sources:
// what is let go is a prune of the source when the index takes the feed,
// so a text written meanwhile that may have quoted it is output.
func (x *outputIndex) feed(name string, p []byte, keep int) {
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return
	}
	limit := keep + keep/4
	if keep <= 0 {
		limit = x.maxQueued
	}
	x.fed[name] += int64(len(p))
	closing := 0
	if h := x.holes[name]; h != nil && !h.closing && x.fed[name] >= h.mark {
		// The ring has turned over past the cut: what it held then is gone
		// from it, and every text written while it was holed is output. The
		// hole stays until that prune's row is committed (closeHole).
		h.closing, closing = true, h.gen
	}
	holeAt := func(cut bool) {
		if !cut {
			return
		}
		mark := int64(math.MaxInt64)
		if keep > 0 {
			mark = x.fed[name] + int64(keep)
		}
		x.holeGen++
		x.holes[name] = &hole{mark: mark, gen: x.holeGen}
	}
	if q := x.rings[name]; q != nil {
		x.queued -= len(q.buf)
		q.buf = append(q.buf, p...)
		lost, cut := x.holdQueued(q, limit)
		q.lost = lost || q.lost
		holeAt(cut)
		x.queued += len(q.buf)
		x.mu.Unlock()
		if closing != 0 {
			x.enqueue(func() { x.closeHole(name, closing) })
		}
		return
	}
	q := &ringFeed{buf: bytes.Clone(p)}
	lost, cut := x.holdQueued(q, limit)
	q.lost = lost
	holeAt(cut)
	x.queued += len(q.buf)
	x.rings[name] = q
	x.mu.Unlock()
	if closing != 0 {
		x.enqueue(func() { x.closeHole(name, closing) })
	}
	x.enqueue(func() {
		x.mu.Lock()
		if x.rings[name] == q {
			delete(x.rings, name)
		}
		x.queued -= len(q.buf)
		buf, lost := q.buf, q.lost
		x.mu.Unlock()
		if lost {
			x.dropSource(name, -1)
		}
		x.addLive(name, buf, keep)
	})
}

// holdQueued cuts q's bytes to their last limit, and further so the bytes
// queued across sources stay within maxQueued; it reports whether any
// were let go (lost), and whether the cut went below limit, so below what
// the source's ring still holds (cut). The caller holds mu, with q's bytes
// not counted in queued.
func (x *outputIndex) holdQueued(q *ringFeed, limit int) (lost, cut bool) {
	room := max(min(limit, x.maxQueued-x.queued), 0)
	if len(q.buf) <= room {
		return false, false
	}
	q.buf = append(q.buf[:0:0], q.buf[len(q.buf)-room:]...)
	return true, room < limit
}

// recordPruneAt records a prune of the source up to upto (-1 whole) now,
// letting nothing go: a text written before it that may have quoted the
// source is output.
func (x *outputIndex) recordPruneAt(name string, upto int64) {
	x.mu.Lock()
	db := x.db
	x.mu.Unlock()
	if db == nil {
		return
	}
	if _, err := db.Exec("INSERT INTO prunes (source, upto, at) VALUES (?, ?, ?)", name, upto, stampIndex(x.now())); err != nil {
		x.fail(err)
	}
}

// hole is a live source's hole: the bytes fed at which its ring has turned
// over past the cut (mark), its generation, and whether its close is
// queued.
type hole struct {
	mark    int64
	gen     int
	closing bool
}

// closeHole records the source's prune now, then lets the hole of
// generation gen go: a check made before the row is committed finds the
// index not whole, and one made after it counts the prune.
func (x *outputIndex) closeHole(name string, gen int) {
	x.recordPruneAt(name, -1)
	x.mu.Lock()
	defer x.mu.Unlock()
	if h := x.holes[name]; h != nil && h.gen == gen {
		delete(x.holes, name)
	}
}

// holed says a live source is holed (holes): the caller holds mu.
func (x *outputIndex) holedLocked() bool { return len(x.holes) > 0 }

// keepText takes the whole text of a kept source (a check's log,
// `check:<run>/<task>/<n>/<id>/<k>`; a history record, `history:<id>`),
// which its reader gives again for a confirmation.
func (x *outputIndex) keepText(name string, text []byte) {
	b := bytes.Clone(text)
	x.enqueue(func() { x.addKept(name, b) })
}

// prune lets the source's fingerprints go, recording the prune's time:
// its session gone, a hole in it closes with its ring once that row is
// committed.
func (x *outputIndex) prune(name string) {
	x.enqueue(func() {
		// The prune's row is committed (dropSource) before the hole goes.
		x.dropSource(name, -1)
		x.mu.Lock()
		delete(x.holes, name)
		delete(x.fed, name)
		x.mu.Unlock()
	})
}

// rebuild makes the index again from the kept sources (a fresh one's):
// sources hands each source's whole text to keep; the index answers
// pending until it is done, and a check waits for it as for any feed.
func (x *outputIndex) rebuild(sources func(keep func(name string, text []byte))) {
	x.enqueue(func() {
		if sources != nil {
			sources(func(name string, text []byte) { x.addKept(name, text) })
		}
		x.mu.Lock()
		x.rebuilding = false
		x.mu.Unlock()
	})
}

// watchScreens gives the index the live screens, each read whole at a
// check: what is on a screen at a write is compared then, as a person
// copies it from there.
func (x *outputIndex) watchScreens(fn func() map[string][]string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.screens = fn
}

// shotDue says the screen name changed (output arrived for its session):
// a snapshot of its rows, read by rows when the index takes it, is queued
// unless one is queued already, and kept as the live source name, at most
// keep bytes, while it is due the index is behind. A snapshot equal to the
// last one is not kept again.
func (x *outputIndex) shotDue(name string, rows func() []string, keep int) {
	x.mu.Lock()
	if x.shots[name] || x.closed {
		x.mu.Unlock()
		return
	}
	x.shots[name] = true
	x.mu.Unlock()
	x.enqueue(func() {
		x.mu.Lock()
		delete(x.shots, name)
		x.mu.Unlock()
		text := strings.Join(rows(), "\n")
		x.mu.Lock()
		same := x.lastShot[name] == text
		x.lastShot[name] = text
		x.mu.Unlock()
		if same || strings.TrimSpace(text) == "" {
			return
		}
		x.addLive(name, []byte(text+"\n\n"), keep)
	})
}

// prunePrefix lets every source whose name begins with prefix go (a
// pruned run's logs), recording each prune.
func (x *outputIndex) prunePrefix(prefix string) {
	x.enqueue(func() {
		x.mu.Lock()
		db := x.db
		x.mu.Unlock()
		if db == nil {
			return
		}
		var names []string
		rows, err := db.Query("SELECT DISTINCT source FROM fp WHERE substr(source, 1, ?) = ?", len(prefix), prefix)
		if err != nil {
			x.fail(err)
			return
		}
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		rows.Close()
		for _, n := range names {
			x.dropSource(n, -1)
		}
	})
}

// addLive indexes p as the next bytes of the live source.
func (x *outputIndex) addLive(name string, p []byte, keep int) {
	x.mu.Lock()
	s := x.live[name]
	if s == nil {
		s = &liveSource{cap: keep, picked: -1}
		x.live[name] = s
	}
	var out []fpHash
	for _, c := range s.plain.next(p) {
		out = s.add(c, out)
	}
	for _, f := range out {
		f.ref.source = name
		x.mem[f.h] = append(x.mem[f.h], f.ref)
	}
	for i := range out {
		out[i].ref.source = name
	}
	s.refs = append(s.refs, out...)
	// What the ring no longer holds is pruned with it: the prune's row is
	// written before the fingerprints leave memory, so a check made during
	// the cut counts it.
	var cut int64 = -1
	if s.cap > 0 && len(s.text) > s.cap+s.cap/4 {
		drop := len(s.text) - s.cap
		cut = s.base + int64(drop)
		x.mu.Unlock()
		x.recordPruneAt(name, cut)
		x.mu.Lock()
		s.text = append(s.text[:0:0], s.text[drop:]...)
		s.base += int64(drop)
		kept := s.refs[:0]
		for _, f := range s.refs {
			if f.ref.off < cut {
				x.memDrop(f.h, name, f.ref.off)
				continue
			}
			kept = append(kept, f)
		}
		s.refs = kept
	}
	x.mu.Unlock()
	x.write(name, out, cut)
}

// add folds one plain byte into the source's text, and the fingerprints
// it completes to out.
func (s *liveSource) add(c byte, out []fpHash) []fpHash {
	if c == '\n' {
		out = s.endLine(out)
		c = ' '
	}
	if isSpace(c) {
		if s.space || len(s.text) == 0 && s.base == 0 {
			return out
		}
		c, s.space = ' ', true
	} else {
		s.space = false
	}
	if len(s.line) == 0 {
		if c == ' ' {
			return s.push(c, out)
		}
		s.lineOff = s.base + int64(len(s.text))
	}
	s.line = append(s.line, c)
	return s.push(c, out)
}

// push appends c to the folded text and winnows the window it ends.
func (s *liveSource) push(c byte, out []fpHash) []fpHash {
	s.text = append(s.text, c)
	end := s.base + int64(len(s.text))
	if end < indexWindow {
		return out
	}
	start := end - indexWindow
	if start < s.base {
		return out
	}
	s.wins = append(s.wins, winHash{pos: start, h: hashBytes(s.text[start-s.base : end-s.base])})
	if len(s.wins) > indexRun {
		s.wins = s.wins[1:]
	}
	if len(s.wins) < indexRun {
		return out
	}
	if m, ok := robustPick(s.wins, s.picked, s.pickedH); ok {
		s.picked, s.pickedH = m.pos, m.h
		out = append(out, fpHash{h: m.h, ref: fpRef{off: m.pos, kind: fpWindow}})
	}
	return out
}

// robustPick is winnowing's pick for the run of windows wins, the last of
// which is new, given the window picked last (its position and hash): a
// window of the run's smallest hash, the one picked last while it is in
// the run and still a smallest, else the rightmost smallest. ok says the
// pick is a new one. A run of one hash repeated (a log of one character)
// keeps one pick per run, not one per byte, and every run's pick is a
// window of its smallest hash, whatever came before.
func robustPick(wins []winHash, picked int64, pickedH uint64) (winHash, bool) {
	last := wins[len(wins)-1]
	if picked >= wins[0].pos {
		if last.h < pickedH {
			return last, true
		}
		return winHash{}, false
	}
	m := wins[0]
	for _, w := range wins[1:] {
		if w.h <= m.h {
			m = w
		}
	}
	return m, true
}

// endLine hashes the line read so far whole, when long enough.
func (s *liveSource) endLine(out []fpHash) []fpHash {
	line := bytes.TrimRight(s.line, " ")
	if len(line) >= indexLine {
		out = append(out, fpHash{h: hashBytes(line) ^ lineSalt, ref: fpRef{off: s.lineOff, kind: fpLine, n: len(line)}})
	}
	s.line = s.line[:0]
	return out
}

// lineSalt keeps a line's hash apart from a window's of the same bytes.
const lineSalt = 0x9e3779b97f4a7c15

func hashBytes(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f' }

// addKept indexes a kept source's whole text, its fingerprints in the
// file alone.
func (x *outputIndex) addKept(name string, text []byte) {
	s := &liveSource{picked: -1}
	var out []fpHash
	for _, c := range s.plain.next(text) {
		out = s.add(c, out)
	}
	out = s.endLine(out)
	for i := range out {
		out[i].ref.source = name
	}
	x.mu.Lock()
	for _, f := range out {
		delete(x.cache, f.h)
	}
	x.mu.Unlock()
	x.write(name, out, -2)
}

// memDrop removes one live fingerprint from memory. The caller holds mu.
func (x *outputIndex) memDrop(h uint64, source string, off int64) {
	refs := x.mem[h]
	for i, r := range refs {
		if r.source == source && r.off == off {
			refs = append(refs[:i], refs[i+1:]...)
			break
		}
	}
	if len(refs) == 0 {
		delete(x.mem, h)
	} else {
		x.mem[h] = refs
	}
}

// dropSource lets every fingerprint of the source go (upto -1), memory
// and file, recording the prune.
func (x *outputIndex) dropSource(name string, upto int64) {
	// The prune's row is committed before the fingerprints leave memory, so
	// a check made during the drop counts it.
	x.mu.Lock()
	db := x.db
	x.mu.Unlock()
	if db != nil {
		if err := x.dropRows(db, name, upto); err != nil {
			x.fail(err)
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if s := x.live[name]; s != nil {
		for _, f := range s.refs {
			x.memDrop(f.h, name, f.ref.off)
		}
		delete(x.live, name)
	}
	// A screen's last snapshot goes with its source.
	delete(x.lastShot, name)
	x.cache = map[uint64][]fpRef{}
}

// dropRows lets the source's fingerprints go from the file with its
// prune's row, in one transaction.
func (x *outputIndex) dropRows(db *sql.DB, name string, upto int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM fp WHERE source = ?", name); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO prunes (source, upto, at) VALUES (?, ?, ?)", name, upto, stampIndex(x.now())); err != nil {
		return err
	}
	return tx.Commit()
}

// write keeps the fingerprints in the file and, for a live source cut at
// cut (-1 none, its prune recorded by addLive), lets those before it go;
// -2 for a kept source, whose earlier fingerprints its new text replaces.
func (x *outputIndex) write(name string, out []fpHash, cut int64) {
	x.mu.Lock()
	db, saturated := x.db, x.saturated
	x.mu.Unlock()
	if db == nil || saturated && cut == -1 && len(out) > 0 {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		x.fail(err)
		return
	}
	defer tx.Rollback()
	if cut == -2 {
		if _, err := tx.Exec("DELETE FROM fp WHERE source = ?", name); err != nil {
			x.fail(err)
			return
		}
	} else if cut >= 0 {
		// The cut's prune row is written already (addLive).
		if _, err := tx.Exec("DELETE FROM fp WHERE source = ? AND off < ?", name, cut); err != nil {
			x.fail(err)
			return
		}
	}
	stmt, err := tx.Prepare("INSERT INTO fp (h, source, off, kind, n) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		x.fail(err)
		return
	}
	defer stmt.Close()
	for _, f := range out {
		if _, err := stmt.Exec(int64(f.h), f.ref.source, f.ref.off, f.ref.kind, f.ref.n); err != nil {
			x.fail(err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		x.fail(err)
		return
	}
	x.mu.Lock()
	x.saturated = x.sizeLocked() > x.bound
	x.mu.Unlock()
}

// sizeLocked is the file's size in bytes, its write-ahead log with it.
// The caller holds mu, or owns the index.
func (x *outputIndex) sizeLocked() int64 {
	var n int64
	for _, p := range []string{x.path, x.path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// fail marks the index unusable: every check is pending from then on.
func (x *outputIndex) fail(err error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.broken == "" {
		x.broken = err.Error()
	}
}

// whole waits, up to indexCatchUp or ctx, for the feeds queued before the
// call, and reports whether the index can answer: not past its bound,
// not being rebuilt, its file usable, nothing it was fed left behind.
func (x *outputIndex) whole(ctx context.Context) bool {
	x.mu.Lock()
	want := x.taken
	x.mu.Unlock()
	t := time.NewTimer(indexCatchUp)
	defer t.Stop()
	for {
		x.mu.Lock()
		if x.done >= want {
			break
		}
		ch := x.progress
		x.mu.Unlock()
		x.waited.Add(1)
		select {
		case <-ch:
		case <-t.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
	defer x.mu.Unlock()
	return x.db != nil && x.broken == "" && !x.saturated && !x.rebuilding && !x.holedLocked()
}

// wholeNow is whole without waiting: false while a feed taken is not yet
// indexed.
func (x *outputIndex) wholeNow() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.done >= x.taken && x.db != nil && x.broken == "" && !x.saturated && !x.rebuilding && !x.holedLocked()
}

// behind says a feed taken is not yet indexed by an index that can be
// read: what whole waits for.
func (x *outputIndex) behind() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.done < x.taken && x.db != nil && x.broken == "" && !x.saturated && !x.rebuilding
}

// state is the index's state in words, for the sync status: "" when whole.
func (x *outputIndex) state() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	switch {
	case x.broken != "" || x.db == nil:
		return "the output check cannot read its index: text written here stays on this machine until it is reindexed"
	case x.saturated:
		return "the output check is over its bound: text written here stays on this machine until it is reindexed"
	case x.rebuilding:
		return "the output check is reindexing: text written meanwhile waits"
	case x.holedLocked():
		return "a session printed past what the output check could take: text written meanwhile stays on this machine"
	}
	return ""
}

// lookup is the places of the hashes, from memory and the file (through
// its cache); an error when the file cannot be read.
func (x *outputIndex) lookup(hs []uint64) (map[uint64][]fpRef, error) {
	out := map[uint64][]fpRef{}
	var miss []uint64
	x.mu.Lock()
	for _, h := range hs {
		if refs, ok := x.mem[h]; ok {
			out[h] = append(out[h], refs...)
		}
		if refs, ok := x.cache[h]; ok {
			out[h] = append(out[h], refs...)
		} else {
			miss = append(miss, h)
		}
	}
	db := x.db
	x.mu.Unlock()
	for len(miss) > 0 {
		n := min(len(miss), 400)
		batch := miss[:n]
		miss = miss[n:]
		args := make([]any, len(batch))
		for i, h := range batch {
			args[i] = int64(h)
		}
		rows, err := db.Query("SELECT h, source, off, kind, n FROM fp WHERE h IN (?"+strings.Repeat(",?", len(batch)-1)+")", args...)
		if err != nil {
			return nil, err
		}
		found := map[uint64][]fpRef{}
		for rows.Next() {
			var h int64
			var r fpRef
			if err := rows.Scan(&h, &r.source, &r.off, &r.kind, &r.n); err != nil {
				rows.Close()
				return nil, err
			}
			found[uint64(h)] = append(found[uint64(h)], r)
		}
		err = rows.Close()
		if err == nil {
			err = rows.Err()
		}
		if err != nil {
			return nil, err
		}
		x.mu.Lock()
		if len(x.cache)+len(batch) > indexCacheMax {
			x.cache = map[uint64][]fpRef{}
		}
		for _, h := range batch {
			x.cache[h] = found[h]
			for _, r := range found[h] {
				if !strings.HasPrefix(r.source, "ring:") && !strings.HasPrefix(r.source, "screen:") {
					out[h] = append(out[h], r)
				}
			}
		}
		x.mu.Unlock()
	}
	return out, nil
}

// sourceTextIf is the source's folded text and where it begins, for a
// confirmation; false when it cannot be read (the hit then counts).
//
// A kept source is read only when read says so: deferred is true for one
// that needs its reader and was not read, and kept for one its reader was
// asked for.
func (x *outputIndex) sourceTextIf(name string, read bool) (text []byte, base int64, ok, deferred, kept bool) {
	x.mu.Lock()
	if s := x.live[name]; s != nil {
		t, base := bytes.Clone(s.text), s.base
		x.mu.Unlock()
		return t, base, true, false, false
	}
	kind, id, _ := strings.Cut(name, ":")
	rd := x.readers[kind]
	x.mu.Unlock()
	if rd == nil {
		return nil, 0, false, false, false
	}
	if !read {
		return nil, 0, false, true, false
	}
	raw, ok := rd(id)
	if !ok {
		return nil, 0, false, false, true
	}
	return foldText(raw), 0, true, false, true
}

// maxSourceReads bounds the kept sources one check reads to confirm a
// hit: a phrase common to many of them (a package's path in every check's
// log) costs at most this many reads, past which the text is kept back as
// output, counted in readsCut.
const maxSourceReads = 32

// foldText is text made plain and its whitespace folded, as a source's.
func foldText(raw []byte) []byte {
	s := &liveSource{picked: -1}
	for _, c := range s.plain.next(raw) {
		if c == '\n' {
			c = ' '
		}
		if isSpace(c) {
			if s.space || len(s.text) == 0 {
				continue
			}
			c, s.space = ' ', true
		} else {
			s.space = false
		}
		s.text = append(s.text, c)
	}
	return s.text
}

// quotes says whether the value (folded) quotes captured output by the
// rule: a run of at least indexMatch bytes, or two whole lines of at
// least indexLine bytes each, found in a source; each hit confirmed
// against its source's text, one that cannot be confirmed counting.
func (x *outputIndex) quotes(value string) (bool, error) {
	hit, _, err := x.quotesIf(value, true)
	return hit, err
}

// quotesIf is quotes, a hit on a kept source confirmed through its reader
// only when read says so: deferred, with no hit, when such a hit was left
// unconfirmed (the caller holds a lock the reader takes: the ledger's).
func (x *outputIndex) quotesIf(value string, read bool) (hit, deferred bool, err error) {
	folded := foldText([]byte(value))
	var hs []uint64
	type at struct {
		pos  int
		kind int
		n    int
	}
	places := map[uint64][]at{}
	// The value winnowed as a source is: a run of indexMatch bytes it
	// shares with a source holds a whole run of indexRun windows, whose
	// smallest hash both picked, so only the hashes the value picks are
	// looked up, each at every position of the value it is at (a source's
	// pick of a hash repeated in the run may be another occurrence of it).
	all := map[uint64][]int{}
	var wins []winHash
	var picked int64 = -1
	var pickedH uint64
	want := map[uint64]bool{}
	for i := 0; i+indexWindow <= len(folded); i++ {
		w := winHash{pos: int64(i), h: hashBytes(folded[i : i+indexWindow])}
		all[w.h] = append(all[w.h], i)
		wins = append(wins, w)
		if len(wins) > indexRun {
			wins = wins[1:]
		}
		if len(wins) < indexRun {
			continue
		}
		if m, ok := robustPick(wins, picked, pickedH); ok {
			picked, pickedH = m.pos, m.h
			want[m.h] = true
		}
	}
	for h := range want {
		hs = append(hs, h)
		for _, i := range all[h] {
			places[h] = append(places[h], at{pos: i, kind: fpWindow})
		}
	}
	var lines [][]byte
	for _, l := range strings.Split(strings.ReplaceAll(value, "\r", "\n"), "\n") {
		f := foldText([]byte(l))
		f = bytes.TrimRight(f, " ")
		if len(f) >= indexLine {
			lines = append(lines, f)
			h := hashBytes(f) ^ lineSalt
			if _, ok := places[h]; !ok {
				hs = append(hs, h)
			}
			places[h] = append(places[h], at{kind: fpLine, n: len(lines) - 1})
		}
	}
	if len(hs) == 0 {
		return false, false, nil
	}
	if x.onScreen(folded, lines) {
		return true, false, nil
	}
	found, err := x.lookup(hs)
	if err != nil {
		return false, false, err
	}
	texts := map[string][]byte{}
	bases := map[string]int64{}
	readable := map[string]bool{}
	unread := map[string]bool{}
	reads := 0
	text := func(src string) ([]byte, int64, bool, bool) {
		if t, ok := texts[src]; ok {
			return t, bases[src], readable[src], unread[src]
		}
		t, b, ok, later, kept := x.sourceTextIf(src, read)
		if kept {
			reads++
		}
		texts[src], bases[src], readable[src], unread[src] = t, b, ok, later
		return t, b, ok, later
	}
	linesHit := map[int]bool{}
	for h, refs := range found {
		for _, r := range refs {
			for _, p := range places[h] {
				if p.kind != r.kind {
					continue
				}
				if reads >= maxSourceReads {
					// A phrase many kept sources share: past the reads one
					// check may make, the text is kept back as output.
					x.readsCut.Add(1)
					return true, false, nil
				}
				src, base, ok, later := text(r.source)
				if later {
					// A kept source not read: its hit is left unconfirmed.
					deferred = true
					continue
				}
				if p.kind == fpLine {
					line := lines[p.n]
					o := r.off - base
					if !ok || o >= 0 && o+int64(len(line)) <= int64(len(src)) && bytes.Equal(src[o:o+int64(len(line))], line) {
						linesHit[p.n] = true
					}
					continue
				}
				if !ok {
					return true, false, nil
				}
				if commonRun(folded, p.pos, src, int(r.off-base)) >= indexMatch {
					return true, false, nil
				}
			}
		}
	}
	if len(linesHit) >= 2 {
		return true, false, nil
	}
	return false, deferred, nil
}

// onScreen says whether the value (folded, and its long lines) quotes
// what a live screen shows now, by the rule: the screens are read whole,
// not through the index.
func (x *outputIndex) onScreen(folded []byte, lines [][]byte) bool {
	x.mu.Lock()
	fn := x.screens
	x.mu.Unlock()
	if fn == nil {
		return false
	}
	for _, rows := range fn() {
		text := foldText([]byte(strings.Join(rows, "\n")))
		if len(text) >= indexMatch && len(folded) >= indexMatch {
			wins := map[uint64][]int{}
			for j := 0; j+indexWindow <= len(text); j++ {
				h := hashBytes(text[j : j+indexWindow])
				wins[h] = append(wins[h], j)
			}
			for i := 0; i+indexWindow <= len(folded); i++ {
				for _, j := range wins[hashBytes(folded[i:i+indexWindow])] {
					if commonRun(folded, i, text, j) >= indexMatch {
						return true
					}
				}
			}
		}
		hit := 0
		for _, l := range lines {
			for _, row := range rows {
				if r := bytes.TrimRight(foldText([]byte(row)), " "); bytes.Equal(r, l) {
					hit++
					break
				}
			}
		}
		if hit >= 2 {
			return true
		}
	}
	return false
}

// commonRun is the length of the run common to a and b through a[i] and
// b[j] aligned: 0 when they differ there or j is outside b.
func commonRun(a []byte, i int, b []byte, j int) int {
	if j < 0 || j >= len(b) || a[i] != b[j] {
		return 0
	}
	l := 0
	for i-l-1 >= 0 && j-l-1 >= 0 && a[i-l-1] == b[j-l-1] {
		l++
	}
	r := 0
	for i+r < len(a) && j+r < len(b) && a[i+r] == b[j+r] {
		r++
	}
	return l + r
}

// prunedSince reports whether a source was pruned after t: a value judged
// later may have quoted what is no longer there.
func (x *outputIndex) prunedSince(t time.Time) (bool, error) {
	x.mu.Lock()
	db := x.db
	x.mu.Unlock()
	if db == nil {
		return true, nil
	}
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM prunes WHERE at > ?", stampIndex(t)).Scan(&n)
	return n > 0, err
}

// plainState makes captured bytes plain text: escape sequences removed,
// other control characters dropped but the tab and the line's end, a
// carriage return read as a line's end.
type plainState struct {
	esc int // 0 text, 1 after ESC, 2 in CSI, 3 in OSC or a string, 4 OSC after ESC
}

func (p *plainState) next(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		switch p.esc {
		case 1:
			switch c {
			case '[':
				p.esc = 2
			case ']', 'P', 'X', '^', '_':
				p.esc = 3
			default:
				p.esc = 0
			}
			continue
		case 2:
			if c >= 0x40 && c <= 0x7e {
				p.esc = 0
			}
			continue
		case 3:
			switch c {
			case 0x07:
				p.esc = 0
			case 0x1b:
				p.esc = 4
			}
			continue
		case 4:
			p.esc = 0
			if c != '\\' {
				p.esc = 3
			}
			continue
		}
		switch {
		case c == 0x1b:
			p.esc = 1
		case c == '\r' || c == '\n':
			out = append(out, '\n')
		case c == '\t':
			out = append(out, ' ')
		case c < 0x20 || c == 0x7f:
		default:
			out = append(out, c)
		}
	}
	return out
}
