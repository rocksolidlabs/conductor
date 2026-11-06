// Package history is the index of Conductor's history and its search
// (docs/plans/history.md, Program item HS1): one SQLite database,
// history/index.sqlite in the data directory, of the entries its caller
// tells it, each under the source it was read from, with the links
// between their evidence references, searched with FTS5. It is a cache:
// the stores (the session records, the ledger, the run records, Dispatch,
// Brakeman's briefs) hold the truth, the index is rebuilt from them when
// it is missing, damaged or of another version, and nothing reads it to
// decide anything. An entry's class (said, decided, checked, record) is
// set here from its kind alone (kinds.go), never by a caller. The package
// imports the standard library and the database driver (modernc.org/
// sqlite, as the ledger and Dispatch do) and nothing else of the module:
// it is told entries and asked queries, and knows nothing of where
// entries come from.
//
// One process writes the index, the holder of history/owner.lock (taken
// by the journal, not here), through Open; every other server on the data
// directory reads it through Open with ReadOnly. The owner never deletes
// a file it can open: a reindex and an index of another schema version
// are cleared in place in one transaction, so a reader sees the database
// before it or after it; only a file SQLite cannot open as a database, or
// one that fails its check and cannot be cleared, is moved aside, and a
// reader opens the path again when the file under it is another.
//
// The owner writes through one connection and reads through a pool of
// its own beside it (readConns), so in WAL a search never holds a write
// back and a write never holds a search: a write waits only for another
// write. Open does not check the file: it opens at once, and an index the
// last owner did not close (a crash, a kill) answers its searches
// StateChecking until Check has read it whole (PRAGMA quick_check), which
// its caller runs in the background.
//
// # Who may call what while what else runs
//
// Open returns an Index no other goroutine holds yet; after it every
// method may be called from any goroutine at any time, and the index
// holds to this (life_test.go tests each pair through the seams on
// Index, never by a sleep):
//
//   - A write (Replace, Remove, Clear, and Check's own clear and mark) runs
//     on the owner's one writer, so two writes take turns, each waiting
//     for the one before as its context allows. A read (Search, Entry,
//     Links, Similar, Mark, Sources, Stats, Unchecked, Check's quick_check)
//     runs on one of the owner's readConns readers in a snapshot of its
//     own, waiting for a free reader as its context allows. Neither waits
//     for the other: no lock is held across a wait for a connection
//     (mu is held only to read or set the closed flags), so a write with
//     every reader busy and more searches queued still commits.
//   - Clear, the rebuild, is one transaction: every table dropped and made
//     again, the generation bumped, the index marked open by its owner and
//     owed no Check. Two Clears, or a Clear and a Check's clear, give the
//     same index in either order, and a read sees the index before it or
//     after it.
//   - Check runs one at a time: a second waits for the first as its
//     context allows. Its quick_check is a read and its clear a write, as
//     above; only when the clear meets damage too does it hold the index
//     whole (life) to move the file aside, waiting for every call running
//     to end, as Close does.
//   - Close refuses every call made once it has begun (ErrClosed), waits
//     for every call begun before it to end, a Check's move aside among
//     them, marks the index closed (not when a Check has found damage it
//     has not yet repaired: the next owner checks it again), and closes the
//     connections. Every Close, the first and any made while it runs or
//     after it, returns only once all that is done: the first with what
//     closing the connections answered, the others nil.
//   - A reader opens the file again when the path names another (refresh)
//     holding the index whole, as a move aside does.
//   - The handles (db, rdb, fi) change only while the index is held whole,
//     so a call, which holds it shared from its start to its end, reads
//     them without a lock. A write therefore waits for a read in one case
//     only: when a swap (Close, a move aside, a reader's reopen) is waiting
//     for the calls running to end, a call that starts meanwhile waits for
//     the swap, which waits for them; each read is bounded by its context
//     and a search by MaxScanned.
package history

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // the database driver, registered as "sqlite"
)

// Version is the schema version this package writes and reads.
const Version = 1

// File is the index's name in the history directory.
const File = "index.sqlite"

// damaged is the suffix of a file moved aside (moveAside), removed at the
// owner's next start.
const damaged = ".damaged"

// The states of an index a search can find in place of entries
// (Page.State): none ("") is an index of this version.
const (
	// StateOtherVersion is an index another Conductor's schema holds: its
	// owner rebuilds it to its own when it next holds the lock.
	StateOtherVersion = "other_version"
	// StateMissing is no index yet: a reader opened before its owner made
	// the file, or between a move aside and the new file.
	StateMissing = "missing"
	// StateChecking is an index its last owner did not close: its
	// searches answer no rows until Check has read it whole and found it
	// sound (or cleared it). Its writes, Mark, Sources and Stats go on.
	StateChecking = "checking"
)

// readConns is how many connections an index reads through: the owner's
// beside its one writer, a reader's all it has. Two, so the indexer's
// marks and a search, or a search and a Check, never wait on each other.
const readConns = 2

var (
	// ErrReadOnly is a write to an index opened read-only.
	ErrReadOnly = errors.New("history: the index is read-only")
	// ErrClosed is a call after Close.
	ErrClosed = errors.New("history: the index is closed")
	// ErrNotReady is a read of an index that holds no entries of this
	// version (StateOtherVersion, StateMissing); its words say which.
	ErrNotReady = errors.New("history: the index is not ready")
)

// Options are how an index is opened.
type Options struct {
	// ReadOnly opens the index as a reader: another process owns it.
	ReadOnly bool
	// MaxEntries and MaxBytes are the budget (0: the package's,
	// MaxEntries and MaxBytes); a test sets them lower.
	MaxEntries int
	MaxBytes   int64
	// Logger takes what the index logs (nil: slog.Default).
	Logger *slog.Logger
}

// Index is an open index. Its methods are safe for concurrent use, as the
// package's "Who may call what while what else runs" says. The owner
// writes through one connection (db) and reads through readConns others
// (rdb), so in WAL a search and a write never wait for each other; a
// reader reads through readConns (db).
type Index struct {
	path       string
	readOnly   bool
	maxEntries int
	maxBytes   int64
	log        *slog.Logger
	now        func() time.Time
	// maxScanned and maxRanked are a search's bounds (MaxScanned,
	// MaxRanked); a test sets them lower.
	maxScanned int
	maxRanked  int

	// life is held shared by every call for as long as it runs and whole
	// by whatever changes the handles: Close, Check's move aside and a
	// reader's reopen (refresh). Close returns only once nothing it let
	// begin can still read or write, a file is swapped only while nothing
	// uses it, and a call reads the handles without mu.
	life sync.RWMutex

	db  *sql.DB     // the owner's writer; a reader's pool
	rdb *sql.DB     // the owner's readers (nil for a reader)
	fi  os.FileInfo // the file db is open on

	// mu guards the flags below; it is held only to read or set them,
	// never across a wait.
	mu sync.Mutex
	// closed refuses every call: Close has begun, or Check could not
	// make the file again.
	closed bool
	// shutting says Close has begun; done is closed once it has ended.
	shutting bool
	done     chan struct{}
	// damage says a Check found the file damaged and has not repaired it:
	// Close then leaves it marked open, so its next owner checks it again.
	damage bool

	// checks holds the Check running (one at a time).
	checks chan struct{}

	// beforeCommit, when set, runs before a Replace commits; an error it
	// answers fails the commit (TestReplaceIsAllOrNothing).
	beforeCommit func() error
	// beforeClear, when set, runs in a clear before it commits; an error
	// it answers fails it (TestOverlappingClearsOweNoCheck).
	beforeClear func() error
	// inRead, when set, runs inside each read transaction once it holds
	// its snapshot (TestASearchNeverHoldsAWriteBack).
	inRead func()
	// inCheck, when set, runs in Check once its quick_check has read the
	// file, before it acts on what it found (TestCloseWaitsForACheck).
	inCheck func()
	// moving, when set, runs in Check's move aside once it holds the index
	// whole (TestCloseDuringAMoveAside).
	moving func()
	// closing, when set, runs in the first Close once it refuses new calls
	// and before it waits for those running; closeWaits in every later
	// Close before it waits for the first (TestCloseWaitsForAWrite).
	closing    func()
	closeWaits func()
}

// testBeforePrepare, when set, runs in the owner's Open with its context
// after the file is opened and before it is brought to this version
// (TestACancelledOpenKeepsTheIndex).
var testBeforePrepare func(ctx context.Context)

// enter takes the index for one call (life, shared): ErrClosed after
// Close. leave ends it. A call enters once, never again inside itself.
func (ix *Index) enter() error {
	// Refused before it waits for life: a Close waiting for the calls
	// running holds every new one back until it is done.
	if ix.isClosed() {
		return ErrClosed
	}
	ix.life.RLock()
	if ix.isClosed() {
		ix.life.RUnlock()
		return ErrClosed
	}
	return nil
}

func (ix *Index) leave() { ix.life.RUnlock() }

// enterRead is enter for a read: a reader first opens the file again
// when the path names another than the one it reads (refresh).
func (ix *Index) enterRead(ctx context.Context) error {
	if ix.readOnly {
		if err := ix.refresh(ctx); err != nil {
			return err
		}
	}
	return ix.enter()
}

func (ix *Index) isClosed() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.closed
}

func (ix *Index) setDamage(d bool) {
	ix.mu.Lock()
	ix.damage = d
	ix.mu.Unlock()
}

// Open opens the index at path, its directory made 0700 when missing: as
// its owner, the file made when missing (0600), moved aside when SQLite
// cannot read it as a database, cleared in place when it is of another
// version, and marked open, so the next owner knows whether this one
// closed it (an index not closed answers StateChecking until Check); or,
// with ReadOnly, as a reader, which opens the file when it is there and
// again whenever the path names another file (a search answers
// StateMissing while there is none). The owner's Open reads no more of the
// file than its schema's version: it does not check it (Check does). A
// cancelled context, or any failure that is not the file's damage, is
// answered as it is, the file left as it was.
func Open(ctx context.Context, path string, opts Options) (*Index, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	ix := &Index{path: abs, readOnly: opts.ReadOnly, maxEntries: opts.MaxEntries, maxBytes: opts.MaxBytes, log: opts.Logger, now: time.Now,
		maxScanned: MaxScanned, maxRanked: MaxRanked, done: make(chan struct{}), checks: make(chan struct{}, 1)}
	if ix.maxEntries <= 0 {
		ix.maxEntries = MaxEntries
	}
	if ix.maxBytes <= 0 {
		ix.maxBytes = MaxBytes
	}
	if ix.log == nil {
		ix.log = slog.Default()
	}
	if ix.readOnly {
		_ = ix.refresh(ctx) // a file not there yet is opened at the first read
		return ix, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	removeDamaged(abs)
	if err := ix.openOwner(ctx); err != nil {
		return nil, err
	}
	return ix, nil
}

// openOwner opens the file for writing and brings it to this version,
// moving it aside once when SQLite cannot read it, then opens the owner's
// readers. A failure that is not the file's damage (a cancelled context,
// a busy or full disk) is answered with the file left where it is.
func (ix *Index) openOwner(ctx context.Context) error {
	db, err := ix.openFile(ctx)
	if err == nil {
		if testBeforePrepare != nil {
			testBeforePrepare(ctx)
		}
		if err = ix.prepare(ctx, db); err == nil {
			return ix.openReaders(ctx, db)
		}
		db.Close()
	}
	if ctx.Err() != nil || !isDamaged(err) {
		return err
	}
	ix.log.Warn("history: the index is damaged; moved aside and made again (it is rebuilt from the stores)", "path", ix.path, "err", err)
	return ix.remake(ctx)
}

// openReaders opens the owner's readers beside its writer db, which then
// becomes the index's.
func (ix *Index) openReaders(ctx context.Context, db *sql.DB) error {
	rdb, err := open(ctx, ix.path, true, readConns)
	if err != nil {
		db.Close()
		return err
	}
	ix.db, ix.rdb = db, rdb
	return nil
}

// openFile makes the file when missing, 0600 before SQLite opens it (so
// its -wal and -shm, which SQLite makes with the file's mode, are 0600
// too), takes the group's and others' permissions off one already there,
// and opens it for writing.
func (ix *Index) openFile(ctx context.Context) (*sql.DB, error) {
	f, err := os.OpenFile(ix.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	f.Close()
	if fi, err := os.Stat(ix.path); err == nil {
		if err := os.Chmod(ix.path, fi.Mode().Perm()&0o600); err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
	}
	db, err := open(ctx, ix.path, false, 1)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(ix.path)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("history: %w", err)
	}
	ix.fi = fi
	return db, nil
}

// prepare brings the database to this version and marks it open: one
// that holds another version or none is cleared in place (clear, which
// marks it open itself and owes it no Check). It reads the schema's
// version and nothing more; an error that isDamaged says the file must be
// moved aside, any other (a cancelled context, a busy or full disk) is
// answered as it is.
func (ix *Index) prepare(ctx context.Context, db *sql.DB) error {
	v, _, err := readVersion(ctx, db)
	switch {
	case err == nil && v == Version:
		return markOpen(ctx, db)
	case err == nil:
		ix.log.Info("history: the index is of another version; cleared in place and rebuilt", "path", ix.path, "version", v, "this", Version)
	case ctx.Err() != nil || !isNoTable(err):
		return err
	}
	return ix.clear(ctx, db, Version)
}

// markOpen marks an index of this version open by its owner as Open finds
// it, and owes it a Check when the last owner did not close it (open still
// set): its searches answer StateChecking until Check finds it sound. Only
// Open calls it, on a file it has not cleared: a clear marks the index
// open in its own transaction.
func markOpen(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "UPDATE schema_version SET unchecked = (unchecked OR open), open = 1")
	return err
}

// isDamaged says the file must be moved aside: SQLite cannot read it as a
// database, or reads it as damaged. A cancelled context, a busy file or a
// full disk is not damage.
func isDamaged(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "not a database") || strings.Contains(s, "malformed") ||
		strings.Contains(s, "SQLITE_NOTADB") || strings.Contains(s, "SQLITE_CORRUPT")
}

// The outcomes of Check.
const (
	CheckSound      = "sound"       // the index read whole and found sound
	CheckCleared    = "cleared"     // it failed its check and was cleared in place
	CheckMovedAside = "moved_aside" // it failed its check, could not be cleared, and was moved aside and made again
)

// Check reads the owner's index whole (PRAGMA quick_check) through one of
// its readers, so writes and the indexer's marks go on beside it (an
// index of 238 MB takes some 8 s). Sound, it ends StateChecking; damaged,
// it clears the index in place (the generation bumped, so every cursor is
// stale), or, when the clear itself finds damage, moves the file aside and
// makes it again, as Open does with a file SQLite cannot read; either way
// the index is empty and the indexer's catch-up, which compares the
// stores with Sources, writes it again. A cancelled context, or a failure
// that is not the file's damage, is answered as it is with nothing
// changed: the index stays StateChecking if it was. The owner's caller
// runs it in the background, at once after an Open whose Unchecked is
// true, and may run it at any time; a Check called while another runs
// waits for it, as its context allows, and then reads the file itself.
func (ix *Index) Check(ctx context.Context) (string, error) {
	if ix.readOnly {
		return "", ErrReadOnly
	}
	select {
	case ix.checks <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-ix.checks }()
	if err := ix.enter(); err != nil {
		return "", err
	}
	var check string
	err := ix.rdb.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check)
	if ix.inCheck != nil {
		ix.inCheck()
	}
	if err == nil && check == "ok" {
		_, err = ix.db.ExecContext(ctx, "UPDATE schema_version SET unchecked = 0")
		ix.leave()
		if err != nil {
			return "", err
		}
		return CheckSound, nil
	}
	if ctx.Err() != nil || (err != nil && !isDamaged(err)) {
		ix.leave()
		return "", err
	}
	if err == nil {
		err = errors.New(check)
	}
	ix.log.Warn("history: the index fails its check; cleared in place (it is rebuilt from the stores)", "path", ix.path, "err", err)
	// Until it is repaired, Close leaves the index marked open, so its next
	// owner checks it again.
	ix.setDamage(true)
	err = ix.clear(ctx, ix.db, Version)
	ix.leave()
	if err == nil {
		ix.setDamage(false)
		return CheckCleared, nil
	}
	if ctx.Err() != nil || !isDamaged(err) {
		return "", err
	}
	// The clear met damage too: the file is moved aside once nothing reads
	// or writes it, and made again. A Close begun meanwhile has refused it.
	ix.life.Lock()
	defer ix.life.Unlock()
	if ix.isClosed() {
		return "", ErrClosed
	}
	if ix.moving != nil {
		ix.moving()
	}
	ix.rdb.Close()
	ix.db.Close()
	ix.db, ix.rdb = nil, nil
	ix.log.Warn("history: the index could not be cleared; moved aside and made again", "path", ix.path, "err", err)
	if err := ix.remake(ctx); err != nil {
		// No file to use: the index is closed, every call ErrClosed, and
		// the next Open tries again.
		ix.mu.Lock()
		ix.closed = true
		ix.mu.Unlock()
		ix.log.Error("history: the index could not be made again; closed", "path", ix.path, "err", err)
		return "", err
	}
	ix.setDamage(false)
	return CheckMovedAside, nil
}

// remake moves the file aside and makes it again, with the owner's
// writer and readers on the new one.
func (ix *Index) remake(ctx context.Context) error {
	if err := moveAside(ix.path); err != nil {
		return fmt.Errorf("history: moving the damaged index aside: %w", err)
	}
	db, err := ix.openFile(ctx)
	if err != nil {
		return err
	}
	if err := ix.prepare(ctx, db); err != nil {
		db.Close()
		return err
	}
	return ix.openReaders(ctx, db)
}

// Unchecked says the owner's index was not closed by its last owner and
// has not passed Check since: its searches answer StateChecking.
func (ix *Index) Unchecked(ctx context.Context) (bool, error) {
	if err := ix.enterRead(ctx); err != nil {
		return false, err
	}
	defer ix.leave()
	tx, _, state, err := ix.readTx(ctx)
	if tx != nil {
		tx.Rollback()
	}
	return state == StateChecking, err
}

// moveAside renames the file and its -wal and -shm to their damaged
// names, so a new file is never read with the old one's log.
func moveAside(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(path+suffix, path+damaged+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// removeDamaged removes what an earlier start moved aside.
func removeDamaged(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + damaged + suffix)
	}
}

// stale says a reader's database is not the file the path names: the
// path names another, or none while it is open, or one while it is not.
// The index is held, shared or whole.
func (ix *Index) stale() bool {
	fi, err := os.Stat(ix.path)
	if err != nil {
		return ix.db != nil
	}
	return ix.db == nil || !os.SameFile(fi, ix.fi)
}

// refresh opens a reader's database again when it is stale, holding the
// index whole (so no call reads the database it closes); the next stale
// file is opened by the next call. A reader whose path names no file has
// no database (its searches answer StateMissing).
func (ix *Index) refresh(ctx context.Context) error {
	if err := ix.enter(); err != nil {
		return err
	}
	stale := ix.stale()
	ix.leave()
	if !stale {
		return nil
	}
	ix.life.Lock()
	defer ix.life.Unlock()
	if ix.isClosed() {
		return ErrClosed
	}
	fi, err := os.Stat(ix.path)
	if err == nil && ix.db != nil && os.SameFile(fi, ix.fi) {
		return nil // another call opened it meanwhile
	}
	if ix.db != nil {
		ix.log.Info("history: the index file was made again or removed; reading the new one", "path", ix.path)
		ix.db.Close()
		ix.db, ix.fi = nil, nil
	}
	if err != nil {
		return nil
	}
	db, err := open(ctx, ix.path, true, readConns)
	if err != nil {
		return err
	}
	ix.db, ix.fi = db, fi
	return nil
}

// readTx begins a read transaction, one snapshot of the database for
// every statement of a read, and reads the version and generation in it,
// through the owner's readers or a reader's pool. state is StateMissing
// or StateOtherVersion, with no transaction, for an index that holds no
// entries of this version; StateChecking, with the transaction, for one
// owed a Check (a search answers no rows; the indexer's reads go on). The
// caller has entered the index (enterRead), so the handles stay as they
// are, and no lock is held while the transaction waits for a connection.
func (ix *Index) readTx(ctx context.Context) (tx *sql.Tx, gen int64, state string, err error) {
	db := ix.rdb
	if ix.readOnly {
		db = ix.db
	}
	if db == nil {
		return nil, 0, StateMissing, nil
	}
	tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, "", err
	}
	v, gen, err := readVersion(ctx, tx)
	switch {
	case err == nil && v == Version:
		var unchecked bool
		if err := tx.QueryRowContext(ctx, "SELECT unchecked FROM schema_version").Scan(&unchecked); err != nil {
			tx.Rollback()
			return nil, 0, "", err
		}
		if ix.inRead != nil {
			ix.inRead()
		}
		if unchecked {
			return tx, gen, StateChecking, nil
		}
		return tx, gen, "", nil
	case err == nil:
		tx.Rollback()
		return nil, 0, StateOtherVersion, nil
	case isNoTable(err):
		tx.Rollback()
		return nil, 0, StateMissing, nil
	}
	tx.Rollback()
	return nil, 0, "", err
}

// read enters the index and runs fn in a read transaction (readTx):
// ErrNotReady, with the state, for an index that holds no entries of this
// version, and for one owed a Check when the read is a search's (an
// entry, its links, Similar).
func (ix *Index) read(ctx context.Context, search bool, fn func(tx *sql.Tx, gen int64) error) error {
	if err := ix.enterRead(ctx); err != nil {
		return err
	}
	defer ix.leave()
	tx, gen, state, err := ix.readTx(ctx)
	if err != nil {
		return err
	}
	if tx != nil {
		defer tx.Rollback()
	}
	if state != "" && (state != StateChecking || search) {
		return fmt.Errorf("%w: %s", ErrNotReady, state)
	}
	return fn(tx, gen)
}

// writeTx begins a write transaction (BEGIN IMMEDIATE, dsn's _txlock) on
// the owner's one writer, waiting for the write before it as ctx allows.
// The caller has entered the index, so the writer stays as it is, and no
// lock is held while the transaction waits for it.
func (ix *Index) writeTx(ctx context.Context) (*sql.Tx, error) {
	if ix.readOnly {
		return nil, ErrReadOnly
	}
	return ix.db.BeginTx(ctx, nil)
}

// ReadOnly says the index takes no writes.
func (ix *Index) ReadOnly() bool { return ix.readOnly }

// Close closes the index: a call made once it has begun is ErrClosed, and
// it returns only once every call begun before it has ended, so nothing it
// let begin writes the file after it (a Replace held before its commit
// commits first). The owner marks the index closed, so its next owner owes
// it no Check, unless a Check has found it damaged and not yet repaired
// it. Every Close returns only once the index is closed: one called while
// another runs waits for it and answers nil, as does one called after.
func (ix *Index) Close() error {
	ix.mu.Lock()
	if ix.shutting {
		ix.mu.Unlock()
		if ix.closeWaits != nil {
			ix.closeWaits()
		}
		<-ix.done
		return nil
	}
	ix.shutting, ix.closed = true, true
	ix.mu.Unlock()
	defer close(ix.done)
	if ix.closing != nil {
		ix.closing()
	}
	ix.life.Lock()
	defer ix.life.Unlock()
	ix.mu.Lock()
	damage := ix.damage
	ix.mu.Unlock()
	var errs []error
	if !ix.readOnly && ix.db != nil && !damage {
		if _, err := ix.db.Exec("UPDATE schema_version SET open = 0"); err != nil {
			ix.log.Warn("history: the index could not be marked closed; its next owner checks it", "path", ix.path, "err", err)
		}
	}
	for _, db := range []*sql.DB{ix.rdb, ix.db} {
		if db != nil {
			errs = append(errs, db.Close())
		}
	}
	return errors.Join(errs...)
}

// Clear empties the index in place for a reindex: every table dropped and
// this version's schema made in one transaction, the generation bumped,
// and the index marked open by its owner and owed no Check, so a cursor
// taken before it is stale, a reader sees the index before the clear or
// after it, never between, and Clears that overlap give the same index in
// either order.
func (ix *Index) Clear(ctx context.Context) error {
	return ix.clearTo(ctx, Version)
}

// clearTo is Clear writing the version given (a test makes an index of
// another version this way).
func (ix *Index) clearTo(ctx context.Context, version int) error {
	if ix.readOnly {
		return ErrReadOnly
	}
	if err := ix.enter(); err != nil {
		return err
	}
	defer ix.leave()
	return ix.clear(ctx, ix.db, version)
}

// schema is the index's tables (docs/plans/history.md, "The database"),
// version 1. schema_version holds, beside the version and the generation,
// how many entries the budget dropped, whether an owner holds the index
// open (open: set by Open, cleared by Close, so an owner finds it set
// when the last did not close) and whether a Check is owed (unchecked).
// One change of shape: an entry's texts (its title,
// summary and body, up to 17 KiB) are a row of texts beside its entries
// row, by the same id, and the search table's content is texts. An
// entries row is then a few hundred bytes, so a filter's scan and a
// search's join and sort read a page for dozens of entries rather than
// one (BenchmarkSearch); entries.bytes is the texts' size, which the
// budget counts. entries_when (an entry's time alone) is the budget's
// order and the filters' newest first; entries_outcome counts the
// palette's questions (a class, an outcome, a project) from the index
// alone; entries_source and links_source are a source's rows, which every
// Replace deletes.
var schema = []string{
	`CREATE TABLE schema_version (version INTEGER NOT NULL, generation INTEGER NOT NULL, dropped INTEGER NOT NULL DEFAULT 0,
		open INTEGER NOT NULL DEFAULT 0, unchecked INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE sources (
		key TEXT PRIMARY KEY,
		mark TEXT NOT NULL,
		indexed_at TEXT NOT NULL,
		bytes INTEGER NOT NULL
	)`,
	`CREATE TABLE entries (
		id INTEGER PRIMARY KEY,
		key TEXT NOT NULL UNIQUE,
		source TEXT NOT NULL REFERENCES sources(key) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		class TEXT NOT NULL,
		ref TEXT NOT NULL,
		seq INTEGER NOT NULL DEFAULT 0,
		anchor TEXT NOT NULL,
		at TEXT NOT NULL,
		host TEXT NOT NULL DEFAULT '', host_user TEXT NOT NULL DEFAULT '', hosted INTEGER NOT NULL DEFAULT 0,
		where_ran TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		repo TEXT NOT NULL DEFAULT '',
		tree TEXT NOT NULL DEFAULT '',
		revision TEXT NOT NULL DEFAULT '',
		commit_id TEXT NOT NULL DEFAULT '',
		agent TEXT NOT NULL DEFAULT '', agent_version TEXT NOT NULL DEFAULT '', agent_session TEXT NOT NULL DEFAULT '',
		crew TEXT NOT NULL DEFAULT '', run TEXT NOT NULL DEFAULT '', ticket TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL DEFAULT '',
		actor TEXT NOT NULL DEFAULT '',
		subject_key TEXT NOT NULL DEFAULT '',
		digest TEXT NOT NULL,
		bytes INTEGER NOT NULL,
		origin TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE texts (
		id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
		title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE links (
		from_ref TEXT NOT NULL, rel TEXT NOT NULL, to_ref TEXT NOT NULL,
		source TEXT NOT NULL REFERENCES sources(key) ON DELETE CASCADE,
		PRIMARY KEY (from_ref, rel, to_ref)
	)`,
	`CREATE VIRTUAL TABLE entries_fts USING fts5(title, summary, body, content = 'texts', content_rowid = 'id',
		tokenize = 'unicode61 remove_diacritics 2', prefix = '2 3')`,
	`CREATE INDEX entries_at ON entries(class, at)`,
	`CREATE INDEX entries_when ON entries(at)`,
	`CREATE INDEX entries_outcome ON entries(class, outcome, project)`,
	`CREATE INDEX entries_anchor ON entries(anchor)`,
	`CREATE INDEX entries_ref ON entries(ref, key)`,
	`CREATE INDEX entries_source ON entries(source)`,
	`CREATE INDEX entries_subject ON entries(subject_key) WHERE subject_key != ''`,
	`CREATE INDEX entries_digest ON entries(digest)`,
	`CREATE INDEX links_to ON links(to_ref, rel)`,
	`CREATE INDEX links_source ON links(source)`,
}

// clear drops every table of the database and makes this package's
// schema, writing version and a generation past the one it held, in one
// BEGIN IMMEDIATE transaction. A database that holds no generation (a new
// file, one made again after a move aside, one of another shape) starts
// at a random one (newGeneration), so a cursor of an earlier file is
// never of this one's generation. The same transaction marks the index
// open by its owner (open, which Close clears) and owes it no Check (an
// empty index has nothing to check): a mark made after the commit, in a
// transaction of its own, would let another clear between them, and the
// mark would then read the owner's own open as an earlier owner's crash.
// Foreign keys are checked at its commit, by which time no table is left
// to check.
func (ix *Index) clear(ctx context.Context, db *sql.DB, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var gen int64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM schema_version").Scan(&gen); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		gen = newGeneration()
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return err
	}
	// The virtual tables first (each drops its shadow tables), then the
	// rest, the latest made first: a child before its parent.
	for _, virtual := range []bool{true, false} {
		rows, err := tx.QueryContext(ctx, `SELECT type, name FROM sqlite_master
			WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' AND (sql LIKE 'CREATE VIRTUAL TABLE%') = ? ORDER BY rowid DESC`, virtual)
		if err != nil {
			return err
		}
		var drops []string
		for rows.Next() {
			var typ, name string
			if err := rows.Scan(&typ, &name); err != nil {
				rows.Close()
				return err
			}
			drops = append(drops, fmt.Sprintf(`DROP %s IF EXISTS "%s"`, strings.ToUpper(typ), strings.ReplaceAll(name, `"`, `""`)))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range drops {
			if _, err := tx.ExecContext(ctx, d); err != nil {
				return err
			}
		}
	}
	for _, stmt := range schema {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version, generation, open, unchecked) VALUES (?, ?, 1, 0)", version, gen+1); err != nil {
		return err
	}
	if ix.beforeClear != nil {
		if err := ix.beforeClear(); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// newGeneration is a first generation: random, at most 2^62, so it never
// wraps when bumped and JSON carries it exactly.
func newGeneration() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]) >> 2)
}

// queryer is a database or a transaction of it.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readVersion reads the schema's version and generation.
func readVersion(ctx context.Context, q queryer) (version int, gen int64, err error) {
	err = q.QueryRowContext(ctx, "SELECT version, generation FROM schema_version").Scan(&version, &gen)
	return version, gen, err
}

func isNoTable(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "no such column") || errors.Is(err, sql.ErrNoRows))
}

// open opens the database at path with conns connections and reaches it
// once.
func open(ctx context.Context, path string, readOnly bool, conns int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, readOnly))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("history: %s: %w", path, err)
	}
	return db, nil
}

// dsn is the connection string, as the ledger's and Dispatch's are (the
// file as a URI with its path escaped, BEGIN IMMEDIATE for a write, a
// reader in read-only mode with no journal mode of its own) but for the
// sync: NORMAL, since the index is a cache a crash may cost a few
// commits of, which the stores still hold; and a page cache of 32 MiB, a
// search's working set over an index of a few hundred MiB.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	pragmas := []string{"busy_timeout(5000)", "foreign_keys(1)", "synchronous(NORMAL)", "cache_size(-32768)"}
	if !readOnly {
		pragmas = append(pragmas, "journal_mode(WAL)")
	}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	q.Set("_time_format", "sqlite")
	q.Set("_txlock", "immediate")
	if readOnly {
		q.Set("mode", "ro")
	}
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	return u.String()
}
