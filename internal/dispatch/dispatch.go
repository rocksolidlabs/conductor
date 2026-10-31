// Package dispatch is the store of Dispatch, Conductor's own work tracking
// (docs/plans/dispatch.md, Program item DS1): projects, tickets with
// stable keys, their discussion and their history, in one SQLite database
// of its own in the data directory (<dataDir>/dispatch/dispatch.sqlite),
// local first, with no account and no network. Every write is a typed
// operation with an id the client mints and a receipt the store keeps
// (Apply): one transaction, one changes row per field it set, added or
// removed, the entity's rev bumped once, so a reply lost to the network is
// asked for again without a second change, and the same log is what
// replication (Program item CS1) will ship between replicas by the field
// rules of rules.go. The package imports the standard library and the
// database driver (modernc.org/sqlite, SQLite translated to Go, so the
// binaries stay CGO_ENABLED=0; the managed runs' ledger is its other
// importer), and on Windows golang.org/x/sys/windows for an attachment's
// move written through (a module internal/proc imports already), and
// nothing else: no HTTP, no session, no process. A ticket's attachments
// are files beside the database (attachments.go), which only the sweep
// deletes (sweep.go). From schema version 2 it also keeps a ticket's
// plan revisions, the record that authorizes a run (plans.go; Program
// item DS2, docs/plans/dispatch-evidence.md), its launches, each recorded
// pending under a run id it mints before the API starts the run, with the
// package the run is handed, frozen and hashed, whose attachments the
// sweep keeps (launches.go, package.go), and answers a resend before its
// caller does any work of its own (Replay). It starts nothing and imports
// nothing of the engine's: its sight of the runs is the functions the API
// gives it (SetRunExists, SetRunTicket, SetRunChain). The API, the CLI
// and the tests are its callers.
package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // the database driver, registered as "sqlite"
)

// File is the database's name under the store's directory.
const File = "dispatch.sqlite"

// busyTimeout is how long a connection waits for the database's write
// lock before it is refused busy: an opener that waits on another
// opener's schema transaction, which is a few dozen statements, is well
// inside it, so a lock held longer is not an opener's and the store opens
// read-only. A variable, since Open reads it before the store exists; a
// test sets it.
var busyTimeout = 5 * time.Second

// beforePrepareTx, when set, runs between the database's check and the
// transaction that reads the schema's version: a test holds the write
// lock until Open reaches it.
var beforePrepareTx func()

// defaultPruneEvery is how often an open store prunes its receipts: a
// day (docs/plans/dispatch.md, Owner decisions, 15: pruned at open and
// daily).
const defaultPruneEvery = 24 * time.Hour

// Store is the open database. One connection serves it, so every
// operation is one BEGIN IMMEDIATE … COMMIT in turn and a read never sees
// an operation half done; there is no owner lock (docs/plans/dispatch.md,
// Owner decisions, 2): SQLite's own locking orders writers across
// processes, every revision is read and bumped inside the transaction
// that uses it, and the schema is made inside the transaction that read
// its version.
type Store struct {
	dir  string
	path string
	// db is nil when there is nothing to read: a database that cannot be
	// opened at all.
	db *sql.DB
	// mu orders the writers and guards closed, db, readOnly, why,
	// runExists and sight.
	mu       sync.Mutex
	closed   bool
	readOnly bool
	why      string
	// runExists is the engine's sight of its runs, and sight its sight of
	// a run's package and of its chain, which the API gives the store
	// (SetRunExists, SetRunTicket, SetRunChain): the store's only sight of
	// the engine.
	runExists func(id string) bool
	sight     runSight
	// runSuffix, when set, mints a launch's run id's last part in place of
	// crypto/rand (mintRunID): a test forces a run id the store holds
	// already, set through openWith.
	runSuffix func() string

	// lmu guards listeners, what OnChange registered.
	lmu       sync.Mutex
	listeners []func([]Change)

	// pruned counts the receipts prune dropped, at open and by the
	// pruning goroutine: what a test of the goroutine watches.
	pruned atomic.Int64
	// pruneEvery is how often the pruning goroutine calls prune: a day
	// (defaultPruneEvery). Open starts the goroutine on a writable store
	// and Close stops it (stopPruning cancels it, and pruneDone is closed
	// once it has ended); a test sets the field through openWith, before
	// the goroutine reads it.
	pruneEvery  time.Duration
	stopPruning context.CancelFunc
	pruneDone   chan struct{}
	// betweenReads, when set, runs inside Ticket's read transaction once
	// the ticket's row is read and before the rest of the read (its
	// relations, children, links, attachments, comments and close state):
	// a test commits a change there and sees the read answer the state
	// before it whole.
	betweenReads func()
	// beforeReceipt, when set, runs inside an operation's transaction,
	// the write lock held, once the change is made (or refused and
	// undone) and before the receipt is written and the transaction
	// committed: a test makes the commit fail with an error, or acts
	// while the lock is held.
	beforeReceipt func() error

	// The attachments' files (attachments.go, sweep.go). syncDir makes a
	// directory's entries durable (syncDirectory; on Windows the move is
	// written through instead), unlink deletes a file the sweep collects
	// (os.Remove), and sweepGrace is how old a lease or an upload's temp
	// file is when the sweep takes it for a crashed upload's (an hour):
	// fields a test replaces, read under mu. beforePublish, when set, runs
	// in an upload between its lease's commit and the file's publication,
	// and beforeCommit between the publication and the operation that
	// writes the attachment's row: a test pauses an upload there.
	// beforeOpen, when set, runs in OpenAttachment between the row's read
	// and the file's open: a test removes the attachment and sweeps its
	// file there. sweepErr is what the sweep at open ended with
	// (SweepError). dirSynced says a sync of the store's directory has
	// succeeded since the attachments' directory was there
	// (attachmentDirs): Open's, else an upload's, so one that failed at
	// open is tried again before an upload is taken; read and written
	// under mu.
	syncDir       func(path string) error
	unlink        func(path string) error
	sweepGrace    time.Duration
	beforePublish func()
	beforeCommit  func()
	beforeOpen    func()
	sweepErr      error
	dirSynced     bool
}

// Errors of a store that cannot serve.
var (
	// ErrReadOnly is returned by a maintenance write (Checkpoint) on a
	// store that opened read-only; an operation answers a read_only
	// refusal instead. ReadOnly says why.
	ErrReadOnly = errors.New("dispatch: read-only")
	// ErrUnavailable is returned by a read of a store with no database to
	// read; ReadOnly says why.
	ErrUnavailable = errors.New("dispatch: unavailable")
	// ErrClosed is returned once Close has begun.
	ErrClosed = errors.New("dispatch: closed")
	// ErrNotFound is returned by a read of what the store does not hold:
	// a ticket, project, comment, label or milestone by a key or an id
	// that names none (the API answers 404 not_found).
	ErrNotFound = errors.New("dispatch: not found")
)

// Change is one entity a commit changed: the commit's last seq (the
// changes log's), the entity, its id (a person's is its name), and the
// ticket (its id) and the project (its key) the entity belongs to, ""
// where there is none. A commit has one per entity it changed, and
// OnChange hands them over together: a relation counts for both its
// tickets, a label's delete for the label and every ticket it left. The
// API turns them into the dispatch events; CS1's continuity store hooks
// them too.
type Change struct {
	Seq     int64
	Entity  string
	ID      string
	Ticket  string
	Project string
}

// Open opens, or creates, the database under dir (the data directory's
// dispatch directory, made 0700 when missing): the ledger's pragmas, one
// connection, the database's check, and the schema brought to Version
// inside one BEGIN IMMEDIATE transaction that reads the version first, so
// two openers on one directory are ordered by the write lock: the second
// waits for the first's commit, then reads the version it wrote and finds
// nothing to do. It never fails for want of a usable database: a database
// a newer Conductor wrote, one that fails its check, one another writer
// holds past busyTimeout, or one the store may not write (proved by a
// write the schema's transaction tries and rolls back, writable) opens
// read-only, read when it can be and never written, and ReadOnly says
// why. The file is made private (0600, or 0400 when the owner may not
// write it) before the schema's transaction.
// It fails only when the directory cannot be made or the database file is
// a symbolic link. A relative dir is taken under the working directory,
// and the store keeps it absolute.
// A writable store then makes its attachments' directories when they are
// missing and sweeps their files (sweep), before any caller can upload:
// what the sweep could not delete is left for the next, a sync of the
// store's directory that failed is tried again by the next upload, and
// SweepError says why, the store serving regardless. The receipts are
// pruned at open (prune) and then every day by a goroutine of the
// store's, which Close stops.
func Open(dir string) (*Store, error) { return openWith(dir, nil) }

// openWith is Open, configure (when given) setting the store's fields
// before anything reads them: a test shortens pruneEvery so.
func openWith(dir string, configure func(*Store)) (*Store, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, File)
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("dispatch: %s is a symbolic link", path)
	}
	s := &Store{dir: dir, path: path, pruneEvery: defaultPruneEvery, syncDir: syncDirectory, unlink: os.Remove, sweepGrace: defaultSweepGrace}
	if configure != nil {
		configure(s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := openWriter(ctx, path)
	if err != nil {
		// The database will not open (a damaged header, or a writer
		// holding it past the timeout): kept as it is, read when it can
		// be, never written.
		s.readOnly, s.why = true, err.Error()
		s.readBack(ctx)
		return s, nil
	}
	// Private before the first transaction, which makes the -wal and -shm
	// with the file's mode as it is then: a file the open has just made
	// has the umask's.
	if err := private(path); err != nil {
		db.Close()
		s.readOnly, s.why = true, "the database's mode could not be set: "+err.Error()
		s.readBack(ctx)
		return s, nil
	}
	if err := s.prepare(ctx, db); err != nil {
		// Damaged, written by a newer Conductor, held by another writer,
		// or a file the store may not write (#444): kept as it is, read
		// when it can be, never written.
		db.Close()
		s.readOnly, s.why = true, err.Error()
		s.readBack(ctx)
		return s, nil
	}
	s.db = db
	now := time.Now()
	// The sweep, before any caller can upload: what it leaves is the next
	// one's, and nothing is lost but space, so Open does not fail for it.
	s.sweepErr = s.openAttachments(ctx, now)
	// A pruning that fails drops nothing, and the next one (the
	// goroutine's, or the next open's) takes what it left: nothing is lost
	// but space, so Open does not fail for it.
	s.prune(ctx, now)
	s.startPruning()
	return s, nil
}

// SweepError is what the sweep at open ended with (sweep): the first file
// it could not delete, left for the next sweep, the database's error, or
// the attachments' directories' (their making, or the sync of the store's
// directory, which the next upload tries again); nil when it found nothing
// it could not clear, and for a store opened read-only, which sweeps
// nothing. The server logs it.
func (s *Store) SweepError() error { return s.sweepErr }

// startPruning starts the goroutine that prunes the receipts every
// pruneEvery, until Close stops it.
func (s *Store) startPruning() {
	if s.pruneEvery <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.stopPruning, s.pruneDone = cancel, make(chan struct{})
	go s.pruning(ctx, s.pruneEvery, s.pruneDone)
}

// pruning calls prune every every until ctx ends, then closes done. A
// pruning that fails drops nothing and the next one takes what it left,
// so its error is not kept; ctx ends one in flight, so Close never waits
// on a long one.
func (s *Store) pruning(ctx context.Context, every time.Duration, done chan struct{}) {
	defer close(done)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			s.prune(ctx, now)
		}
	}
}

// readBack opens the database read-only for a store that will not take
// writes, or leaves nothing to read, and says so in why.
func (s *Store) readBack(ctx context.Context) {
	db, err := open(ctx, s.path, true)
	if err != nil {
		s.why += "; nor read: " + err.Error()
		return
	}
	s.db = db
}

// openWriter opens the database for writing, trying again while the
// open is refused busy, for up to busyTimeout: the first openers of one
// empty directory each switch the new file to WAL mode, and the switch
// upgrades the shared lock the pragma holds to an exclusive one, which
// SQLite refuses at once rather than wait for while another connection
// reads (its rule against a deadlock between two upgrades), so the
// refusal comes without the busy handler. Past the timeout the lock is
// not an opener's, and the store opens read-only as it does for a writer
// holding the schema transaction's lock.
func openWriter(ctx context.Context, path string) (*sql.DB, error) {
	deadline := time.Now().Add(busyTimeout)
	wait := 5 * time.Millisecond
	for {
		db, err := open(ctx, path, false)
		if err == nil || !isBusy(err) {
			return db, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("dispatch: another writer holds the database: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dispatch: %w", ctx.Err())
		case <-time.After(wait):
		}
		if wait < 100*time.Millisecond {
			wait *= 2
		}
	}
}

// open opens the database at path and reaches it once.
func open(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, readOnly))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("dispatch: %s: %w", path, err)
	}
	return db, nil
}

// dsn is the connection string: the file, whose path is absolute (Open
// makes it so: a relative one's first element would be read as the URI's
// authority, which SQLite refuses), as a URI with an empty authority, its
// path with forward slashes and escaped, so a directory with '#', '?' or
// '%' in its name is the directory it is, and a Windows drive after a
// '/' (file:///C:/…); the pragmas every connection needs (the busy
// timeout, foreign keys on, a full sync; on darwin a full fsync, since a
// plain fsync there does not reach the disk), WAL for a writable one,
// BEGIN IMMEDIATE for every transaction, and read-only mode when the
// store is. A read-only connection sets no journal mode: it reads the file
// in the mode the file is in (a WAL file's header says WAL), since a
// switch from a rollback journal's mode writes the header, which
// read-only mode refuses, and a refused open would leave the store
// nothing to read.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	pragmas := []string{fmt.Sprintf("busy_timeout(%d)", busyTimeout/time.Millisecond), "foreign_keys(1)", "synchronous(FULL)"}
	if !readOnly {
		pragmas = append(pragmas, "journal_mode(WAL)")
	}
	if runtime.GOOS == "darwin" {
		pragmas = append(pragmas, "fullfsync(1)", "checkpoint_fullfsync(1)")
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
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	return u.String()
}

// prepare checks the database (quick_check) and brings it to Version in
// one BEGIN IMMEDIATE transaction that reads the version first, or
// refuses it: a later version, one that fails the check, or one whose
// write lock another writer holds past busyTimeout.
func (s *Store) prepare(ctx context.Context, db *sql.DB) error {
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return fmt.Errorf("dispatch: quick_check: %w", err)
	}
	if check != "ok" {
		return fmt.Errorf("dispatch: the database fails its check: %s", check)
	}
	if beforePrepareTx != nil {
		beforePrepareTx()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		if isBusy(err) {
			return fmt.Errorf("dispatch: another writer holds the database: %w", err)
		}
		return fmt.Errorf("dispatch: %w", err)
	}
	defer tx.Rollback()
	var version int
	err = tx.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version)
	switch {
	case err == nil && version > Version:
		return fmt.Errorf("dispatch: the database is version %d, written by a newer Conductor; this one writes version %d", version, Version)
	case err == nil && version == Version:
		// Nothing to write: a write rolled back proves the connection
		// writes at all (writable).
		if err := writable(ctx, tx); err != nil {
			return fmt.Errorf("dispatch: %s cannot be written: %w", s.path, err)
		}
		return nil
	case err == nil:
		if err := migrate(ctx, tx, version); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows) || isNoTable(err):
		// New, or empty: made whole.
		for _, stmt := range schema {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("dispatch: schema: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version) VALUES (?)", Version); err != nil {
			return err
		}
	default:
		return fmt.Errorf("dispatch: schema_version: %w", err)
	}
	return tx.Commit()
}

// migrate brings the database from version from to Version inside the
// transaction that read from, one version at a time, or refuses one it
// has no way up from; the version is bumped in the same transaction, so a
// second opener finds the bump and never runs the steps again.
func migrate(ctx context.Context, tx *sql.Tx, from int) error {
	for v := from; v < Version; v++ {
		stmts, ok := migrations[v]
		if !ok {
			return fmt.Errorf("dispatch: the database is version %d, which this Conductor cannot bring to %d", from, Version)
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("dispatch: migrating from version %d: %w", v, err)
			}
		}
		if f := migrationsGo[v]; f != nil {
			if err := f(ctx, tx); err != nil {
				return fmt.Errorf("dispatch: migrating from version %d: %w", v, err)
			}
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE schema_version SET version = ?", Version)
	return err
}

func isNoTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// private takes the group's and others' permissions off the database
// file and grants the owner none it lacks: 0644 becomes 0600, and a file
// made 0400 stays 0400, so a file the store may not write stays so and
// opens read-only (#444).
func private(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.Chmod(path, fi.Mode().Perm()&0o600)
}

// writable proves that the connection writes, with a write the
// schema's transaction, tx, rolls back. SQLite opens a file the process
// may not write (or whose WAL it may not write) read-only, without an
// error, and nothing short of a write shows it: a file in a rollback
// journal's mode fails the open, whose switch to WAL writes the header,
// but one already in WAL mode, the mode the store leaves it in, has
// nothing to write at open, and in WAL mode even BEGIN IMMEDIATE takes its
// lock (#444). The write sets the header's user version to what it is,
// so the file is as it was even were it committed.
func writable(ctx context.Context, tx *sql.Tx) error {
	var v int64
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v))
	return err
}

// isBusy says err is SQLite's refusal of a lock another connection holds.
func isBusy(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked"))
}

// ReadOnly reports whether the store takes no writes, and why: a newer
// Conductor wrote the database, it failed its check, another writer held
// it past the busy timeout at open, the store may not write the database
// file (in a rollback journal's mode the open fails, in WAL mode the write
// the open tries does), or it could not be opened at all.
func (s *Store) ReadOnly() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readOnly, s.why
}

// Dir is the directory the store lives in.
func (s *Store) Dir() string { return s.dir }

// conn gives the database for a read, or why there is none.
func (s *Store) conn() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return nil, ErrClosed
	case s.db == nil:
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, s.why)
	}
	return s.db, nil
}

// read runs fn in one read transaction, so that a read of more than one
// statement describes one state, its rev and its rows agreeing, whatever
// commits between its statements from this process or from a second
// server on the file: under WAL the transaction reads one snapshot from
// its first statement to its end. ReadOnly makes the driver begin it as
// a plain BEGIN whatever _txlock says, so it takes no write lock. A
// read-only store reads the version in the same transaction, and fn is
// given an olderSchema for a database of a version before this binary's,
// one the store could not migrate (another writer held it, or the store
// may not write it): its reads take what the later versions added as
// empty (schemaOf), so a ticket reads as it did before them.
func (s *Store) read(ctx context.Context, fn func(q queryer) error) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	s.mu.Lock()
	readOnly := s.readOnly
	s.mu.Unlock()
	rtx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	var q queryer = rtx
	if readOnly {
		var version int
		if err := rtx.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err == nil && version < Version {
			q = olderSchema{queryer: rtx, version: version}
		}
	}
	if err := fn(q); err != nil {
		return err
	}
	return rtx.Commit()
}

// olderSchema is a read's queryer on a database of a version before
// Version, which a read-only store reads as it found it (read).
type olderSchema struct {
	queryer
	version int
}

// schemaOf is the schema version of the database q reads: an
// olderSchema's version, else Version (a writable store's database is
// brought to Version at open, and a newer one holds what Version has).
func schemaOf(q queryer) int {
	if o, ok := q.(olderSchema); ok {
		return o.version
	}
	return Version
}

// Checkpoint moves the write-ahead log into the database and truncates
// it, for an export of a running server (Program item C6), and returns
// nil only when it did: a checkpoint another connection keeps from
// finishing past the busy timeout (a reader on a snapshot, a writer) is
// an error, the log still holding what it held.
func (s *Store) Checkpoint(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrClosed
	case s.db == nil || s.readOnly:
		return fmt.Errorf("%w: %s", ErrReadOnly, s.why)
	}
	return checkpoint(ctx, s.db)
}

// checkpoint runs wal_checkpoint(TRUNCATE) and reads its result, which is
// where SQLite reports a checkpoint kept from finishing (its first
// column; the log's frames and those moved after it), never as an error.
func checkpoint(ctx context.Context, db *sql.DB) error {
	var busy, frames, moved int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &moved); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("dispatch: another connection kept the checkpoint from finishing: %d of the log's %d frames moved", moved, frames)
	}
	return nil
}

// Close checkpoints the write-ahead log into the database and closes it,
// returning the checkpoint's error, one another connection kept from
// finishing included, beside the close's. A read or a write after it
// begins gets ErrClosed; one in flight gets the database's own error,
// never a panic.
func (s *Store) Close() error {
	// The pruning goroutine first, outside the lock, which a pruning in
	// flight holds: stopped and ended before the database closes.
	if s.stopPruning != nil {
		s.stopPruning()
		<-s.pruneDone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	if s.db != nil {
		if !s.readOnly {
			if err := checkpoint(context.Background(), s.db); err != nil {
				errs = append(errs, err)
			}
		}
		errs = append(errs, s.db.Close())
	}
	return errors.Join(errs...)
}

// OnChange registers fn, called once after every commit with what the
// commit changed: one Change per entity, in the order the operation
// named them, its subject first, however many there are (a label's
// delete names every ticket it left). It is called on the committing
// goroutine and outside the store's lock: it must not block on the
// store's writers. Each listener is handed a copy of its own, which it
// may keep or change.
func (s *Store) OnChange(fn func([]Change)) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// notify tells the listeners what a commit changed: each, in the order
// they registered, all of it in one call and in a copy of its own.
func (s *Store) notify(changes []Change) {
	s.lmu.Lock()
	fns := slices.Clone(s.listeners)
	s.lmu.Unlock()
	for _, fn := range fns {
		fn(slices.Clone(changes))
	}
}
