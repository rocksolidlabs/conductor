package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// The sweep is the one thing that deletes an attachment's file
// (docs/plans/dispatch.md, "Attachments"). A file is an orphan when
// neither a live attachment row nor an upload's lease names its hash: a
// crash between an upload's publication and its commit (its lease then
// expires), a publication whose sync failed, or a removed attachment's
// content. It runs as two steps, each a write transaction of its own.
// expireLeases drops the leases older than sweepGrace, a crashed upload's,
// and commits before any file goes, so a dropped lease stays dropped
// however the collection ends: a deletion the database undoes cannot
// bring a file back, and an upload paused across the sweep that found
// its lease restored by a rollback would commit a row naming content that
// is not there. collectOrphans then lists the directory and, in a second
// transaction it holds until its last unlink has returned, whatever
// becomes of its context, deletes every listed content file that no live
// row and no lease names, and every upload's temp file older than the
// grace. The write lock orders it against an upload in this server or in
// another on the same directory: an upload publishes its file only after
// its lease is committed, and no commit falls inside the collection's
// transaction, so a collection that began before the lease finishes its
// deletes before the file is published (a reused hash it deleted is
// published afresh), and one that begins after reads the lease and leaves
// the file, whatever its age. An upload whose lease the sweep dropped
// before its row was written is refused expired, never trusted. A
// launch's package pins the content of the attachments it names
// (launch_files, Program item DS2): a pinned file is named, so a package's
// attachment outlives its removal from the ticket, for every attempt of
// the run and of its continuations, which copy it through OpenLaunchFile.

// shardPattern is the name of a shard of the attachments' directory: the
// first two characters of its contents' hashes.
var shardPattern = regexp.MustCompile(`^[0-9a-f]{2}$`)

// sweep expires the leases older than sweepGrace, then collects the
// files nothing names (collectOrphans), now being the clock both read.
// Open runs it before any caller can upload, and a test calls it, or
// either step alone. A lease the expiry failed to drop is still a lease,
// its file left too, so the collection runs whatever the expiry did. It
// returns the first error either step met: a file it could not delete is
// left for the next sweep, the sweep going on with the rest, and ctx
// ended stops it between files, the rest left for the next sweep too.
func (s *Store) sweep(ctx context.Context, now time.Time) error {
	err := s.expireLeases(ctx, now)
	if cerr := s.collectOrphans(ctx, now); err == nil {
		err = cerr
	}
	return err
}

// expireLeases drops the uploads' leases older than sweepGrace, a crashed
// upload's, in one write transaction (BEGIN IMMEDIATE) that it commits,
// on disk (synchronous FULL), before it returns: an upload that resumes
// after it finds no lease and is refused expired.
func (s *Store) expireLeases(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	grace := s.sweepGrace
	s.mu.Unlock()
	if err := s.exec(ctx, "DELETE FROM uploads WHERE created_at <= ?", stamp(now.Add(-grace))); err != nil {
		return fmt.Errorf("dispatch: expiring the uploads' leases: %w", err)
	}
	return nil
}

// collectOrphans lists the attachments' directory, then, in a write
// transaction (BEGIN IMMEDIATE) it holds while it works and writes nothing
// in, deletes every listed content file whose hash no live attachment
// row, no lease and no launch's pin names, and every upload's temp file at
// least
// sweepGrace old by now, each through unlink. The transaction is the lock
// that orders the collection against an upload's lease, so it lasts until
// the last unlink has returned, whatever becomes of ctx: it is begun, and
// read in, on a context no cancellation ends, since database/sql rolls a
// transaction back the moment its context ends, which would release the
// lock with an unlink decided under it still to come, and an upload in a
// second store could then lease the hash, publish its file and commit its
// row before that unlink deleted the file. ctx is checked before the
// transaction and before each file instead, the collection stopping at the
// first check that finds it ended, its error returned and the files after
// it left for the next sweep. A file it cannot delete is left for the next
// sweep too, and the first error is returned at the end; the database
// failing stops it.
func (s *Store) collectOrphans(ctx context.Context, now time.Time) error {
	contents, temps, err := listAttachments(filepath.Join(s.dir, attachmentsDir))
	if err != nil {
		return fmt.Errorf("dispatch: listing the attachments: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrClosed
	case s.readOnly || s.db == nil:
		return fmt.Errorf("%w: %s", ErrReadOnly, s.why)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	held := context.WithoutCancel(ctx)
	dbtx, err := s.db.BeginTx(held, nil)
	if err != nil {
		return err
	}
	// It writes nothing: its end undoes nothing, and nothing it deleted was
	// named while it held the lock.
	defer dbtx.Rollback()
	var first error
	keep := func(err error) {
		if first == nil {
			first = err
		}
	}
	// ended says ctx has ended, keeping its error unless a file's came first.
	ended := func() bool {
		if err := ctx.Err(); err != nil {
			keep(fmt.Errorf("dispatch: %w", err))
			return true
		}
		return false
	}
	unlink := func(path string) {
		if err := s.unlink(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			keep(fmt.Errorf("dispatch: sweeping %s: %w", path, err))
		}
	}
	for _, c := range contents {
		if ended() {
			return first
		}
		var named bool
		if err := dbtx.QueryRowContext(held, `SELECT EXISTS (SELECT 1 FROM attachments WHERE sha256 = ? AND deleted_at = '')
			OR EXISTS (SELECT 1 FROM uploads WHERE sha256 = ?) OR EXISTS (SELECT 1 FROM launch_files WHERE sha256 = ?)`, c.sum, c.sum, c.sum).Scan(&named); err != nil {
			return err
		}
		if !named {
			unlink(c.path)
		}
	}
	cutoff := now.Add(-s.sweepGrace)
	for _, u := range temps {
		if ended() {
			return first
		}
		if !u.made.After(cutoff) {
			unlink(u.path)
		}
	}
	return first
}

// listed is a file the sweep may delete: a content file with its hash, or
// an upload's temp file with when it was made.
type listed struct {
	path, sum string
	made      time.Time
}

// listAttachments lists the attachments' directory at root: the content
// files of its shards, each a regular file named by a hash beginning with
// its shard's name, and the temp files of its uploads' directory, each a
// regular file named as receive names one. Anything else is no file of
// the store's, and the sweep leaves it. A directory that does not exist
// holds nothing.
func listAttachments(root string) (contents, temps []listed, err error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || (name != uploadsDir && !shardPattern.MatchString(name)) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, name))
		if err != nil {
			return nil, nil, err
		}
		for _, f := range files {
			path := filepath.Join(root, name, f.Name())
			switch {
			case !f.Type().IsRegular():
			case name == uploadsDir:
				if made, ok := uploadMade(f.Name()); ok {
					temps = append(temps, listed{path: path, made: made})
				}
			case hashPattern.MatchString(f.Name()) && f.Name()[:2] == name:
				contents = append(contents, listed{path: path, sum: f.Name()})
			}
		}
	}
	return contents, temps, nil
}

// openAttachments is what Open does for the attachments of a writable
// store before any caller can upload: their directories made, the first's
// name made durable (attachmentDirs; a sync that fails here is tried again
// by the next upload), and the sweep. It returns the first error, the
// sweep running whatever the directories did.
func (s *Store) openAttachments(ctx context.Context, now time.Time) error {
	_, err := s.attachmentDirs(s.syncDir)
	if serr := s.sweep(ctx, now); err == nil {
		err = serr
	}
	return err
}

// OpenLaunchFile opens the content the package of the launch with the id
// names by its hash, sum, and nothing else: content no launch_files row of
// that launch pins is ErrNotFound, whatever the attachments' directory
// holds, so a caller can open only what a package hands over. A pin is
// never dropped and the sweep keeps what a pin names, so the content of a
// launch's attachment opens after the attachment's removal from its
// ticket; a pinned file missing, or not a regular file, is an error. The
// engine copies a package's attachments into each attempt through it,
// checking each one's size and hash as it copies. The caller closes the
// file.
func (s *Store) OpenLaunchFile(ctx context.Context, launchID, sum string) (*os.File, error) {
	if !ValidID(launchID) || !hashPattern.MatchString(sum) {
		return nil, ErrNotFound
	}
	var pinned bool
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 2 {
			return nil
		}
		return q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM launch_files WHERE launch_id = ? AND sha256 = ?)", launchID, sum).Scan(&pinned)
	})
	if err != nil {
		return nil, err
	}
	if !pinned {
		return nil, ErrNotFound
	}
	f, err := os.Open(contentPath(filepath.Join(s.dir, attachmentsDir), sum))
	if err != nil {
		return nil, fmt.Errorf("dispatch: the content %s of launch %s: %w", sum, launchID, err)
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, fmt.Errorf("dispatch: the content %s of launch %s: %w", sum, launchID, err)
	}
	return f, nil
}
