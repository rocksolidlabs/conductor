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
package history

import (
	"context"
	"database/sql"
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
)

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

// Index is an open index. Its methods are safe for concurrent use; it has
// one connection, so its reads and writes take turns.
type Index struct {
	path       string
	readOnly   bool
	maxEntries int
	maxBytes   int64
	log        *slog.Logger
	now        func() time.Time

	mu     sync.Mutex
	db     *sql.DB
	fi     os.FileInfo // the file db is open on
	closed bool

	// beforeCommit, when set, runs before a Replace commits; an error it
	// answers fails the commit (TestReplaceIsAllOrNothing).
	beforeCommit func() error
}

// Open opens the index at path, its directory made 0700 when missing: as
// its owner, the file made when missing (0600), moved aside when SQLite
// cannot open it as a database or it fails its check and cannot be
// cleared, cleared in place when it is of another version; or, with
// ReadOnly, as a reader, which opens the file when it is there and again
// whenever the path names another file (a search answers StateMissing
// while there is none).
func Open(ctx context.Context, path string, opts Options) (*Index, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	ix := &Index{path: abs, readOnly: opts.ReadOnly, maxEntries: opts.MaxEntries, maxBytes: opts.MaxBytes, log: opts.Logger, now: time.Now}
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
		ix.mu.Lock()
		_, _ = ix.reader(ctx) // a file not there yet is opened at the first read
		ix.mu.Unlock()
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
// moving it aside once when it cannot be opened or prepared.
func (ix *Index) openOwner(ctx context.Context) error {
	db, err := ix.openFile(ctx)
	if err == nil {
		if err = ix.prepare(ctx, db); err == nil {
			ix.db = db
			return nil
		}
		db.Close()
	}
	if !isDamaged(err) {
		return err
	}
	ix.log.Warn("history: the index is damaged; moved aside and made again (it is rebuilt from the stores)", "path", ix.path, "err", err)
	if err := moveAside(ix.path); err != nil {
		return fmt.Errorf("history: moving the damaged index aside: %w", err)
	}
	db, err = ix.openFile(ctx)
	if err != nil {
		return err
	}
	if err := ix.prepare(ctx, db); err != nil {
		db.Close()
		return err
	}
	ix.db = db
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
	db, err := open(ctx, ix.path, false)
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

// prepare checks the database and brings it to this version: one that
// fails its check, holds another version or none is cleared in place
// (clear). An error that isDamaged says the file must be moved aside.
func (ix *Index) prepare(ctx context.Context, db *sql.DB) error {
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		if err == nil {
			err = errors.New(check)
		}
		ix.log.Warn("history: the index fails its check; cleared in place", "path", ix.path, "err", err)
		if err := clear(ctx, db, Version); err != nil {
			return damagedErr{err}
		}
		return nil
	}
	v, _, err := readVersion(ctx, db)
	switch {
	case err == nil && v == Version:
		return nil
	case err == nil:
		ix.log.Info("history: the index is of another version; cleared in place and rebuilt", "path", ix.path, "version", v, "this", Version)
	case !isNoTable(err):
		return damagedErr{err}
	}
	if err := clear(ctx, db, Version); err != nil {
		return damagedErr{err}
	}
	return nil
}

// damagedErr is a database that could not be prepared: moved aside.
type damagedErr struct{ err error }

func (e damagedErr) Error() string { return "history: " + e.err.Error() }
func (e damagedErr) Unwrap() error { return e.err }

// isDamaged says the file must be moved aside: SQLite cannot read it as a
// database, or it could not be prepared.
func isDamaged(err error) bool {
	if err == nil {
		return false
	}
	var d damagedErr
	if errors.As(err, &d) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "not a database") || strings.Contains(s, "malformed") ||
		strings.Contains(s, "SQLITE_NOTADB") || strings.Contains(s, "SQLITE_CORRUPT")
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

// reader is a reader's database, opened again when the path names
// another file than the one it is open on, closed while the path names
// none (ErrNotReady). ix.mu is held.
func (ix *Index) reader(ctx context.Context) (*sql.DB, error) {
	fi, err := os.Stat(ix.path)
	if err != nil {
		if ix.db != nil {
			ix.db.Close()
			ix.db, ix.fi = nil, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrNotReady, StateMissing)
	}
	if ix.db != nil && os.SameFile(fi, ix.fi) {
		return ix.db, nil
	}
	if ix.db != nil {
		ix.log.Info("history: the index file was made again; reading the new one", "path", ix.path)
		ix.db.Close()
		ix.db, ix.fi = nil, nil
	}
	db, err := open(ctx, ix.path, true)
	if err != nil {
		return nil, err
	}
	ix.db, ix.fi = db, fi
	return db, nil
}

// readTx begins a read transaction, one snapshot of the database for
// every statement of a read, and reads the version and generation in it.
// A reader's file is checked first (reader). state is StateMissing or
// StateOtherVersion, with no transaction, for an index that holds no
// entries of this version.
func (ix *Index) readTx(ctx context.Context) (tx *sql.Tx, gen int64, state string, err error) {
	ix.mu.Lock()
	db := ix.db
	switch {
	case ix.closed:
		err = ErrClosed
	case ix.readOnly:
		db, err = ix.reader(ctx)
	}
	if err == nil {
		// Begun under mu, so no reopen closes db before the transaction
		// holds its connection.
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	ix.mu.Unlock()
	if errors.Is(err, ErrNotReady) {
		return nil, 0, StateMissing, nil
	}
	if err != nil {
		return nil, 0, "", err
	}
	v, gen, err := readVersion(ctx, tx)
	switch {
	case err == nil && v == Version:
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

// read runs fn in a read transaction (readTx): ErrNotReady, with the
// state, for an index that holds no entries of this version.
func (ix *Index) read(ctx context.Context, fn func(tx *sql.Tx, gen int64) error) error {
	tx, gen, state, err := ix.readTx(ctx)
	if err != nil {
		return err
	}
	if state != "" {
		return fmt.Errorf("%w: %s", ErrNotReady, state)
	}
	defer tx.Rollback()
	return fn(tx, gen)
}

// writeTx begins a write transaction (BEGIN IMMEDIATE, dsn's _txlock).
func (ix *Index) writeTx(ctx context.Context) (*sql.Tx, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	switch {
	case ix.closed:
		return nil, ErrClosed
	case ix.readOnly:
		return nil, ErrReadOnly
	}
	return ix.db.BeginTx(ctx, nil)
}

// ReadOnly says the index takes no writes.
func (ix *Index) ReadOnly() bool { return ix.readOnly }

// Close closes the index; a read or write after it is ErrClosed.
func (ix *Index) Close() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.closed {
		return nil
	}
	ix.closed = true
	if ix.db == nil {
		return nil
	}
	return ix.db.Close()
}

// Clear empties the index in place for a reindex: every table dropped and
// this version's schema made in one transaction, the generation bumped,
// so a cursor taken before it is stale and a reader sees the index before
// the clear or after it, never between.
func (ix *Index) Clear(ctx context.Context) error {
	return ix.clearTo(ctx, Version)
}

// clearTo is Clear writing the version given (a test makes an index of
// another version this way).
func (ix *Index) clearTo(ctx context.Context, version int) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	switch {
	case ix.closed:
		return ErrClosed
	case ix.readOnly:
		return ErrReadOnly
	}
	return clear(ctx, ix.db, version)
}

// schema is the index's tables (docs/plans/history.md, "The database"),
// version 1, with one change of shape: an entry's texts (its title,
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
	`CREATE TABLE schema_version (version INTEGER NOT NULL, generation INTEGER NOT NULL, dropped INTEGER NOT NULL DEFAULT 0)`,
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
	`CREATE INDEX entries_ref ON entries(ref)`,
	`CREATE INDEX entries_source ON entries(source)`,
	`CREATE INDEX entries_subject ON entries(subject_key) WHERE subject_key != ''`,
	`CREATE INDEX entries_digest ON entries(digest)`,
	`CREATE INDEX links_to ON links(to_ref, rel)`,
	`CREATE INDEX links_source ON links(source)`,
}

// clear drops every table of the database and makes this package's
// schema, writing version and a generation past the one it held, in one
// BEGIN IMMEDIATE transaction. Foreign keys are checked at its commit, by
// which time no table is left to check.
func clear(ctx context.Context, db *sql.DB, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var gen int64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM schema_version").Scan(&gen); err != nil {
		gen = 0
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
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version, generation) VALUES (?, ?)", version, gen+1); err != nil {
		return err
	}
	return tx.Commit()
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

// open opens the database at path with one connection and reaches it
// once.
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
