package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Snapshot writes a copy of the database to dst, a file that must not
// exist yet, for an export (docs/plans/ext.md, C6): SQLite's VACUUM INTO on
// the ledger's own connection, under the ledger's lock, so a transition
// waits for it and the copy is one committed state, whole, whatever the
// server writes meanwhile. A ledger that opened read-only (another server
// holds the owner lock) is copied as it reads; one with no database is
// ErrUnavailable. dst is made 0600.
func (l *Ledger) Snapshot(ctx context.Context, dst string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.closed:
		return ErrClosed
	case l.db == nil:
		return fmt.Errorf("%w: %s", ErrUnavailable, l.why)
	}
	return vacuumInto(ctx, l.db, dst)
}

// SnapshotFile writes a copy of the database at path to dst, as Snapshot
// does, for an export with no server running: a read-only connection of
// its own on the file, closed before it returns. It refuses a path that
// is a symbolic link or not there.
func SnapshotFile(ctx context.Context, path, dst string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("ledger: %s is not a regular file", path)
	}
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return vacuumInto(ctx, db, dst)
}

// vacuumInto copies db's database into dst, a new file, made private.
func vacuumInto(ctx context.Context, db *sql.DB, dst string) error {
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ledger: the snapshot's file %s exists already", dst)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("ledger: snapshot: %w", cancelledAs(ctx, err))
	}
	return os.Chmod(dst, 0o600)
}

// Held reports whether a server holds the owner lock of the ledger under
// dir (the runs directory): it tries the lock and lets it go at once,
// never making the file or the directory, so a directory with no lock
// file is held by nobody. On a platform or a filesystem with no locks it
// answers false: nothing can be told there.
func Held(dir string) (bool, error) {
	return lockHeld(filepath.Join(dir, Lock))
}
