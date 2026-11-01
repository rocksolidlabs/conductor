package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Snapshot writes a copy of the database to dst, a file that must not
// exist yet, for an export (docs/plans/ext.md, C6): SQLite's VACUUM INTO on
// the store's own connection, under the store's lock, so an operation
// waits for it and the copy is one committed state, whole. The copy holds
// rows, not the attachments' files they name: SnapshotHashes reads from it
// which those are. A store opened read-only is copied as it reads; one
// with nothing to read is ErrUnavailable. dst is made 0600.
func (s *Store) Snapshot(ctx context.Context, dst string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrClosed
	case s.db == nil:
		return fmt.Errorf("%w: %s", ErrUnavailable, s.why)
	}
	return vacuumInto(ctx, s.db, dst)
}

// SnapshotFile writes a copy of the database at path to dst, as Snapshot
// does, for an export with no server running: a read-only connection of
// its own on the file, closed before it returns. It refuses a path that
// is a symbolic link or not there.
func SnapshotFile(ctx context.Context, path, dst string) error {
	db, err := openSnapshot(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	return vacuumInto(ctx, db, dst)
}

// SnapshotHashes reads, from the database file at path (a snapshot, or a
// restore's staged copy: nothing writes it), the content every live row
// requires: the distinct SHA-256 of every attachment row not removed, and
// of every file a launch's package pins (launch_files, which the sweep
// keeps after the attachment's removal and OpenLaunchFile opens), sorted.
// A removed attachment's content that nothing else names is not among
// them: the next sweep deletes it.
func SnapshotHashes(ctx context.Context, path string) ([]string, error) {
	db, err := openSnapshot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	query := "SELECT DISTINCT sha256 FROM attachments WHERE deleted_at = ''"
	var pins bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'launch_files')").Scan(&pins); err != nil {
		return nil, fmt.Errorf("dispatch: %s: %w", path, err)
	}
	if pins {
		query += " UNION SELECT sha256 FROM launch_files"
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("dispatch: %s: %w", path, err)
	}
	defer rows.Close()
	var sums []string
	for rows.Next() {
		var sum string
		if err := rows.Scan(&sum); err != nil {
			return nil, err
		}
		if !hashPattern.MatchString(sum) {
			return nil, fmt.Errorf("dispatch: %s names a content %q that is no SHA-256", path, sum)
		}
		sums = append(sums, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(sums)
	return slices.Compact(sums), nil
}

// AttachmentPath is where the content with the hash sum lives under a
// store's directory, as a slash path relative to it
// ("attachments/ab/ab…"): what an export copies and a restore checks.
func AttachmentPath(sum string) string {
	return attachmentsDir + "/" + sum[:2] + "/" + sum
}

// openSnapshot opens the database file at path on a read-only connection
// of its own, refusing a link or a file that is not there.
func openSnapshot(ctx context.Context, path string) (*sql.DB, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("dispatch: %s is not a regular file", path)
	}
	return open(ctx, path, true)
}

// vacuumInto copies db's database into dst, a new file, made private.
func vacuumInto(ctx context.Context, db *sql.DB, dst string) error {
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("dispatch: the snapshot's file %s exists already", dst)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("dispatch: snapshot: %w", cerr)
		}
		return fmt.Errorf("dispatch: snapshot: %w", err)
	}
	return os.Chmod(dst, 0o600)
}
