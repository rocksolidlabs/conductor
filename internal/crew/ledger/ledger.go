// Package ledger keeps the managed runs' truth: one SQLite database per
// data directory, every transition of a run one transaction committed
// before the side effect it precedes, the events of a run in sequence, the
// receipts of the commands that caused them, and the evidence of its checks.
// It is the one package that imports the database driver (modernc.org/
// sqlite, SQLite translated to Go, so the binaries stay CGO_ENABLED=0).
// The run records (runs/<id>.json) stay the API's answer: a cache of this
// database with a revision. See docs/plans/agent-harness-order.md,
// decisions 7 and 8.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // the database driver, registered as "sqlite"
)

// File is the database's name under the runs directory; Lock is the owner
// lock's.
const (
	File = "managed.sqlite"
	Lock = "owner.lock"
)

// Version is the schema this binary writes. A database of a later version
// opens read-only; an earlier one is brought to it (migrations).
const Version = 6

// migrations bring a database from the version keyed to the next, in one
// transaction with the version's change: 1 to 2 adds the attempts'
// result column.
var migrations = map[int][]string{
	1: {"ALTER TABLE attempts ADD COLUMN result TEXT NOT NULL DEFAULT ''"},
	// Version 3 (#334, rows 9 and 10): an attempt's checkpoint, and the
	// messages the members of a run send one another.
	2: {"ALTER TABLE attempts ADD COLUMN checkpoint TEXT NOT NULL DEFAULT ''", "ALTER TABLE tasks ADD COLUMN repair TEXT NOT NULL DEFAULT ''", "ALTER TABLE tasks ADD COLUMN extra INTEGER NOT NULL DEFAULT 0", messagesTable},
	// Version 4 (#334, the withdrawal): the last attempt of a task a
	// withdrawal took back, filled in by backfillWithdrawn.
	3: {"ALTER TABLE tasks ADD COLUMN withdrawn INTEGER NOT NULL DEFAULT 0"},
	// Version 5 (EN1, project manifests, #394): every step of a project
	// manifest is a receipt. A checks row gains the phase it ran in (a
	// task's check, or a manifest phase), the manifest that ran it, the
	// tree's ports, its tries, the words of its failure and a probe's URL;
	// a run its pause's words and the project manifest at its base (where
	// it comes from and its prerequisites, in order); the prerequisites,
	// which no attempt owns, get a receipt table of the same shape, and the
	// trees a table of their lifecycle. Every row a version 4 database
	// holds is a task's check, so the defaults are true of it, and a
	// version 4 run's manifest can only be the crew's as launched (setup
	// steps at most: none read the file or ran a prerequisite), which its
	// row holds already: no backfill.
	4: {
		"ALTER TABLE checks ADD COLUMN phase TEXT NOT NULL DEFAULT 'check'",
		"ALTER TABLE checks ADD COLUMN manifest TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE checks ADD COLUMN ports TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE checks ADD COLUMN tries INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE checks ADD COLUMN error TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE checks ADD COLUMN url TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE runs ADD COLUMN paused_why TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE runs ADD COLUMN manifest TEXT NOT NULL DEFAULT ''",
		prerequisitesTable,
		treesTable,
	},
	// Version 6 (DS2, a run launched from a plan revision, #460): a run
	// names the Dispatch ticket it was launched from and keeps the launch
	// package as Dispatch built it, byte for byte, which its continuations
	// carry. Every row a version 5 database holds is a run no ticket
	// launched, which the empty defaults say: nothing is derived.
	5: {
		"ALTER TABLE runs ADD COLUMN ticket TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE runs ADD COLUMN package TEXT NOT NULL DEFAULT ''",
	},
}

// migrationsGo run after a version's statements, in the same transaction:
// what SQL alone cannot work out.
var migrationsGo = map[int]func(context.Context, *sql.Tx) error{
	3: backfillWithdrawn,
}

// messagesTable is the messages table (#334, row 9): one row per message
// a member sent another, by sequence within the run, with when it was
// typed into the recipient's session and when the recipient acknowledged
// it.
const messagesTable = `CREATE TABLE messages (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		from_member TEXT NOT NULL,
		to_member TEXT NOT NULL,
		text TEXT NOT NULL,
		created_at TEXT NOT NULL,
		typing_at TEXT NOT NULL DEFAULT '',
		delivered_at TEXT NOT NULL DEFAULT '',
		acked_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, seq)
	)`

// prerequisitesTable holds a run's prerequisites' receipts (version 5):
// the checks' columns but for the attempt's (task_id, n), the phase, the
// ports and the tries, keyed by the run, the prerequisite's id and k, its
// run's number in the run. A prerequisite runs before any attempt, so no
// checks row can hold it (that table's key is an attempt's).
const prerequisitesTable = `CREATE TABLE prerequisites (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		id TEXT NOT NULL,
		k INTEGER NOT NULL,
		argv TEXT NOT NULL,
		dir TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		pid INTEGER NOT NULL DEFAULT 0,
		pgroup INTEGER NOT NULL DEFAULT 0,
		process_started_at TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL DEFAULT '',
		exit INTEGER NOT NULL DEFAULT -1,
		signal TEXT NOT NULL DEFAULT '',
		timed_out INTEGER NOT NULL DEFAULT 0,
		killed INTEGER NOT NULL DEFAULT 0,
		stable INTEGER NOT NULL DEFAULT 0,
		truncated INTEGER NOT NULL DEFAULT 0,
		total INTEGER NOT NULL DEFAULT 0,
		log BLOB,
		log_dropped INTEGER NOT NULL DEFAULT 0,
		log_sha256 TEXT NOT NULL DEFAULT '',
		tail TEXT NOT NULL DEFAULT '',
		manifest TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, id, k)
	)`

// treesTable holds the lifecycle of each tree a run made usable (version
// 5): an attempt's worktree or the checkout of its result, from the
// transaction that resolved its manifest (resolved) to the one that ended
// it (ended), so a tree the owner left between the two is found at the
// next start whatever its run's state.
const treesTable = `CREATE TABLE trees (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		task_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		kind TEXT NOT NULL,
		dir TEXT NOT NULL DEFAULT '',
		commit_id TEXT NOT NULL DEFAULT '',
		manifest TEXT NOT NULL DEFAULT '',
		ports TEXT NOT NULL DEFAULT '',
		preview TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		why TEXT NOT NULL DEFAULT '',
		interrupted INTEGER NOT NULL DEFAULT 0,
		ended_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, task_id, n, kind)
	)`

// LockWait is how long a ledger keeps trying for the owner lock held by
// another process after Open (awaitLock): a desktop restart overlaps the
// old server's shutdown. The server serves meanwhile.
var LockWait = 10 * time.Second

// Ledger is the open database. One connection serves it, so every
// transaction is one BEGIN IMMEDIATE … COMMIT in turn, and a read never
// sees a transition half done.
type Ledger struct {
	dir string
	// db is nil when there is nothing to read: no database where the
	// ledger could not make one, or one that cannot be opened at all.
	db *sql.DB
	// mu orders the writers (the one connection would too, but a
	// transaction that waited on it would time out as busy instead) and
	// guards closed.
	mu     sync.Mutex
	closed bool

	readOnly bool
	why      string
	lock     *ownerLock
	// done ends awaitLock when the ledger is closed; ownedLate says the
	// lock was taken after Open, onOwned is what runs then (OnOwned), and
	// notified that it ran.
	done      chan struct{}
	ownedLate bool
	onOwned   func()
	notified  bool
	// beforeCommit, when set, runs before a transition commits: a test
	// makes the commit fail.
	beforeCommit func() error
}

// Errors of a ledger that cannot serve.
var (
	// ErrReadOnly is returned by a write to a ledger that opened
	// read-only; ReadOnly says why.
	ErrReadOnly = errors.New("ledger: read-only")
	// ErrUnavailable is returned by a read of a ledger with no database
	// to read; ReadOnly says why.
	ErrUnavailable = errors.New("ledger: unavailable")
	// ErrClosed is returned once Close has begun.
	ErrClosed = errors.New("ledger: closed")
)

// takeLock is takeOwnerLock; the tests make it fail.
var takeLock = takeOwnerLock

// Open opens, or creates, the database under dir (the runs directory,
// 0700) and takes the owner lock. It never fails for want of the lock or
// of a usable database: the ledger then opens read-only, or with nothing
// to read, and ReadOnly says why, so that the server runs its legacy
// crews while managed launches answer 503. It fails only when the
// directory cannot be made or the database file is a symbolic link.
func Open(dir string) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &Ledger{dir: dir, done: make(chan struct{})}
	path := filepath.Join(dir, File)
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("ledger: %s is a symbolic link", path)
	}
	lockPath := filepath.Join(dir, Lock)
	lock, why, held, err := takeLock(lockPath)
	if err != nil {
		// The lock file itself: the ledger is unavailable, the server not.
		l.readOnly, l.why = true, err.Error()
		return l, nil
	}
	l.lock = lock
	if lock == nil {
		l.readOnly, l.why = true, why
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.attach(ctx, path); err != nil {
		return nil, err
	}
	if lock == nil && held {
		// Another server holds the lock, most likely the one this server
		// replaces, still shutting down: this one serves at once, reads
		// what there is, and takes the lock as it is let go (awaitLock),
		// for up to LockWait.
		l.why = "waiting for the owner lock: " + why
		go l.awaitLock(lockPath, path)
	}
	return l, nil
}

// attach opens the database at path as the ledger is: the owner's,
// prepared (the schema, the check) and written, or read-only. A database
// that will not open as the owner's is kept as it is, read when it can be,
// never written; one that will not open at all leaves nothing to read.
// An error is the chmod's alone, the lock let go. The caller holds mu, or
// is Open.
func (l *Ledger) attach(ctx context.Context, path string) error {
	_, statErr := os.Lstat(path)
	if l.readOnly && statErr != nil {
		// Nothing to read, and not ours to make.
		l.why += "; no database to read"
		return nil
	}
	db, err := l.open(ctx, path, l.readOnly)
	if err != nil {
		if l.readOnly {
			l.why += "; " + err.Error()
			return nil
		}
		// The owner, but the database will not open for writing (a
		// damaged header, or a file in a rollback journal's mode that the
		// server may not write): kept as it is, read when it can be, never
		// written.
		l.readOnly, l.why = true, err.Error()
		if db, err = l.open(ctx, path, true); err != nil {
			l.why += "; nor read: " + err.Error()
			return nil
		}
		l.db = db
		return nil
	}
	l.db = db
	if !l.readOnly {
		// Private before the first transaction, which makes the -wal and
		// -shm with the file's mode as it is then: a file the open has
		// just made has the umask's. Its error waits for the write's
		// proof, so a file the server may not write is read-only, not a
		// failed Open.
		modeErr := private(path)
		if err := writable(ctx, db); err != nil {
			// The owner, but the connection does not write: a file in WAL
			// mode, or the WAL beside it, that the server may not write,
			// which SQLite opens read-only without an error (#444). Kept
			// as it is, read when it can be, never written.
			db.Close()
			l.readOnly, l.why = true, fmt.Sprintf("ledger: %s cannot be written: %v", path, err)
			if db, err = l.open(ctx, path, true); err != nil {
				l.why += "; nor read: " + err.Error()
				l.db = nil
				return nil
			}
			l.db = db
			return nil
		}
		if modeErr != nil {
			db.Close()
			l.db = nil
			l.release()
			return modeErr
		}
		if err := l.prepare(ctx); err != nil {
			// A damaged database, or one a later binary wrote: kept as it
			// is, read when it can be, never written.
			l.readOnly, l.why = true, err.Error()
			db.Close()
			if db, err = l.open(ctx, path, true); err != nil {
				l.why += "; nor read: " + err.Error()
				l.db = nil
				return nil
			}
			l.db = db
		}
	}
	return nil
}

// awaitLock tries for the owner lock every 250 ms for up to LockWait
// after Open and, once it is taken, opens the database as the owner's in
// place of the read-only one, under mu, so a reader sees one or the other;
// then OnOwned's function runs. Past LockWait the ledger stays read-only
// and says who holds the lock. Close ends the wait: each try is made under
// mu and only while the ledger is open, so a Close waits for a try under
// way and none is made after it. A try opens the lock file, making it when
// it is missing, and a lock taken opens the database: once Close has
// returned, the wait makes nothing in the directory, which may be going by
// then (a test's cleanup removing it, #281).
func (l *Ledger) awaitLock(lockPath, path string) {
	deadline := time.Now().Add(LockWait)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-tick.C:
		}
		l.mu.Lock()
		if l.closed {
			// Closed between the tick and the try: no try.
			l.mu.Unlock()
			return
		}
		lock, why, held, err := takeLock(lockPath)
		switch {
		case err != nil:
			l.why = err.Error()
		case lock != nil:
			l.lock = lock
			if l.db != nil {
				l.db.Close()
				l.db = nil
			}
			l.readOnly, l.why = false, ""
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := l.attach(ctx, path)
			cancel()
			if err != nil {
				l.readOnly, l.why = true, err.Error()
			}
			l.ownedLate = true
			cb := l.onOwned
			run := cb != nil && !l.readOnly && !l.notified
			if run {
				l.notified = true
			}
			l.mu.Unlock()
			if run {
				cb()
			}
			return
		case held && time.Now().Before(deadline):
			l.mu.Unlock()
			continue
		default:
			// Held past the wait, or locks no longer supported: as it is.
			l.why = why
		}
		l.mu.Unlock()
		return
	}
}

// OnOwned sets what runs once the ledger has taken the owner lock after
// Open (awaitLock): the server concludes what the last owner left open
// then (Recover). Set after Open; a lock taken before the call runs fn at
// once. It runs at most once.
func (l *Ledger) OnOwned(fn func()) {
	l.mu.Lock()
	l.onOwned = fn
	run := fn != nil && l.ownedLate && !l.readOnly && !l.notified
	if run {
		l.notified = true
	}
	l.mu.Unlock()
	if run {
		fn()
	}
}

// open opens the database at path and reaches it once.
func (l *Ledger) open(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, readOnly))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: %s: %w", path, err)
	}
	return db, nil
}

// private takes the group's and others' permissions off the database
// file and grants the owner none it lacks: 0644 becomes 0600, and a file
// made 0400 stays 0400, so a file the server may not write stays so and
// opens read-only (#444).
func private(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.Chmod(path, fi.Mode().Perm()&0o600)
}

// writable proves that the connection writes, with a write it rolls
// back. SQLite opens a file the process may not write read-only, without
// an error, and nothing short of a write shows it: a file in a rollback
// journal's mode fails the open, whose switch to WAL writes the header
// (#417), but one already in WAL mode, the mode the ledger leaves it in,
// has nothing to write at open (#444). The write sets the header's user
// version to what it is, inside a transaction that is rolled back, so the
// file is as it was.
func writable(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int64
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v))
	return err
}

// conn gives the database for a read, or why there is none.
func (l *Ledger) conn() (*sql.DB, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.closed:
		return nil, ErrClosed
	case l.db == nil:
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, l.why)
	}
	return l.db, nil
}

// dsn is the connection string: the file as a URI (its path escaped, so a
// directory with '#', '?' or '%' in its name is the directory it is), the
// pragmas every connection needs (a busy timeout, foreign keys on, a full
// sync; on darwin a full fsync, since a plain fsync there does not reach
// the disk), WAL for a writable one, and read-only mode when the ledger
// is. A read-only connection sets no journal mode: it reads the file in
// the mode the file is in (a WAL file's header says WAL), since a switch
// from a rollback journal's mode writes the header, which read-only mode
// refuses, and a refused open would leave the ledger nothing to read
// (#417).
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	pragmas := []string{"busy_timeout(5000)", "foreign_keys(1)", "synchronous(FULL)"}
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
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}

// prepare checks the database (quick_check) and brings it to Version, or
// refuses it: a later version, or one that fails the check.
func (l *Ledger) prepare(ctx context.Context) error {
	var check string
	if err := l.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return fmt.Errorf("ledger: quick_check: %w", err)
	}
	if check != "ok" {
		return fmt.Errorf("ledger: the database fails its check: %s", check)
	}
	var version int
	err := l.db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version)
	switch {
	case err == nil && version > Version:
		return fmt.Errorf("ledger: the database is version %d, written by a newer Conductor; this one writes version %d", version, Version)
	case err == nil && version == Version:
		return nil
	case err == nil:
		return l.migrate(ctx, version)
	case errors.Is(err, sql.ErrNoRows) || isNoTable(err):
		// New, or empty: made whole in one transaction.
	default:
		return fmt.Errorf("ledger: schema_version: %w", err)
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range schema {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ledger: schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version) VALUES (?)", Version); err != nil {
		return err
	}
	return tx.Commit()
}

// migrate brings the database from version from to Version, one version
// at a time in one transaction, or refuses one it has no way up from.
func (l *Ledger) migrate(ctx context.Context, from int) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for v := from; v < Version; v++ {
		stmts, ok := migrations[v]
		if !ok {
			return fmt.Errorf("ledger: the database is version %d, which this Conductor cannot bring to %d", from, Version)
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("ledger: migrating from version %d: %w", v, err)
			}
		}
		if f := migrationsGo[v]; f != nil {
			if err := f(ctx, tx); err != nil {
				return fmt.Errorf("ledger: migrating from version %d: %w", v, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_version SET version = ?", Version); err != nil {
		return err
	}
	return tx.Commit()
}

func isNoTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// ReadOnly reports whether the ledger takes no writes, and why: another
// server holds the owner lock (waited for, for a while after Open), locks
// are not supported where the data directory is, the server may not write
// the database file (in a rollback journal's mode the open fails, in WAL
// mode the write the open tries does), the database failed its check, or
// a newer Conductor wrote it.
func (l *Ledger) ReadOnly() (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readOnly, l.why
}

// Dir is the runs directory the ledger lives in.
func (l *Ledger) Dir() string { return l.dir }

// Close checkpoints the write-ahead log into the database, closes it and
// releases the owner lock. A read or a write after it begins gets
// ErrClosed; one in flight gets the database's own error, never a panic.
// It ends the wait for the owner lock too (awaitLock): a try under way
// ends first, and none follows, so once Close has returned the ledger
// makes nothing in its directory.
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.done != nil {
		close(l.done)
	}
	var errs []error
	if l.db != nil {
		if !l.readOnly {
			if _, err := l.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				errs = append(errs, err)
			}
		}
		errs = append(errs, l.db.Close())
	}
	l.release()
	return errors.Join(errs...)
}

func (l *Ledger) release() {
	if l.lock != nil {
		l.lock.release()
		l.lock = nil
	}
}

// schema is the database as Version has it, made whole for a new one (an
// older one is brought to it by migrations). Times are RFC 3339 text in
// UTC; JSON is text; a run's rows go with it (ON DELETE CASCADE) when the
// owner prunes it.
var schema = []string{
	`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
	`CREATE TABLE runs (
		id TEXT PRIMARY KEY,
		crew_id TEXT NOT NULL,
		crew TEXT NOT NULL,
		base TEXT NOT NULL,
		state TEXT NOT NULL,
		outcome TEXT NOT NULL DEFAULT '',
		paused INTEGER NOT NULL DEFAULT 0,
		revision INTEGER NOT NULL DEFAULT 0,
		continued_from TEXT NOT NULL DEFAULT '',
		continued_by TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		closed_at TEXT NOT NULL DEFAULT '',
		paused_why TEXT NOT NULL DEFAULT '',
		manifest TEXT NOT NULL DEFAULT '',
		ticket TEXT NOT NULL DEFAULT '',
		package TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE tasks (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		id TEXT NOT NULL,
		member TEXT NOT NULL,
		kind TEXT NOT NULL,
		state TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,
		result_attempt INTEGER NOT NULL DEFAULT 0,
		result_commit TEXT NOT NULL DEFAULT '',
		verdict TEXT NOT NULL DEFAULT '',
		imported_from TEXT NOT NULL DEFAULT '',
		repair TEXT NOT NULL DEFAULT '',
		extra INTEGER NOT NULL DEFAULT 0,
		withdrawn INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (run_id, id)
	)`,
	`CREATE TABLE attempts (
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		member TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		worktree TEXT NOT NULL DEFAULT '',
		branch TEXT NOT NULL DEFAULT '',
		start_commit TEXT NOT NULL DEFAULT '',
		result_commit TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		stage TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		reserved_at TEXT NOT NULL,
		launched_at TEXT NOT NULL DEFAULT '',
		prompted_at TEXT NOT NULL DEFAULT '',
		closed_at TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '',
		checkpoint TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, task_id, n),
		FOREIGN KEY (run_id, task_id) REFERENCES tasks(run_id, id) ON DELETE CASCADE
	)`,
	`CREATE TABLE checks (
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		id TEXT NOT NULL,
		k INTEGER NOT NULL,
		argv TEXT NOT NULL,
		dir TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		pid INTEGER NOT NULL DEFAULT 0,
		pgroup INTEGER NOT NULL DEFAULT 0,
		process_started_at TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL DEFAULT '',
		exit INTEGER NOT NULL DEFAULT -1,
		signal TEXT NOT NULL DEFAULT '',
		timed_out INTEGER NOT NULL DEFAULT 0,
		killed INTEGER NOT NULL DEFAULT 0,
		stable INTEGER NOT NULL DEFAULT 0,
		truncated INTEGER NOT NULL DEFAULT 0,
		total INTEGER NOT NULL DEFAULT 0,
		log BLOB,
		log_dropped INTEGER NOT NULL DEFAULT 0,
		log_sha256 TEXT NOT NULL DEFAULT '',
		tail TEXT NOT NULL DEFAULT '',
		phase TEXT NOT NULL DEFAULT 'check',
		manifest TEXT NOT NULL DEFAULT '',
		ports TEXT NOT NULL DEFAULT '',
		tries INTEGER NOT NULL DEFAULT 1,
		error TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, task_id, n, id, k),
		FOREIGN KEY (run_id, task_id, n) REFERENCES attempts(run_id, task_id, n) ON DELETE CASCADE
	)`,
	`CREATE TABLE commands (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		actor TEXT NOT NULL,
		id TEXT NOT NULL,
		intent TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		status TEXT NOT NULL,
		receipt TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		PRIMARY KEY (run_id, actor, id)
	)`,
	`CREATE TABLE events (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		at TEXT NOT NULL,
		kind TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, seq)
	)`,
	messagesTable,
	prerequisitesTable,
	treesTable,
}
