package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// An attachment is a file of a ticket's (docs/plans/dispatch.md,
// "Attachments"; Owner decisions, 8): its row a member of the ticket's
// attachments set, its content a file beside the database named by its
// SHA-256, attachments/<the hash's first two characters>/<the hash> under
// the store's directory, 0600 in directories of 0700 that the file routes
// refuse, so the database stays small, the export plain, and one content
// uploaded twice is one file. An upload (AddAttachment) writes its file
// and hashes it before anything names it, holds the hash with a lease (an
// uploads row) from before the file is published under it until the row
// that names it is committed, and makes the file's new name durable
// before that commit, so a committed row never names a file a power loss
// can take back. Only the sweep (sweep.go) deletes a file: one that
// neither a live attachment row nor a lease names. A remove tombstones
// the row and deletes nothing, since an upload of the same content may be
// between its publication and its commit.

const (
	// attachmentsDir is the attachments' directory under the store's: a
	// shard per first two characters of a hash, and uploadsDir.
	attachmentsDir = "attachments"
	// uploadsDir is where an upload writes its file before it is
	// published, under attachmentsDir; the file's name begins with when it
	// was made, in Unix nanoseconds, which the sweep reads its age from (no
	// file's modification time is consulted by anything).
	uploadsDir = "tmp"
	// defaultSweepGrace is how old a lease or an upload's temp file is when
	// the sweep takes it for a crashed upload's: an hour. A stopped process
	// or a machine asleep between an upload's two transactions looks the
	// same, and is refused expired when it resumes.
	defaultSweepGrace = time.Hour
)

// hashPattern is a content's name: its SHA-256 in lower-case hex.
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// errUploadGone is publish finding no temp file to publish: the sweep
// took it, older than its grace, while the upload was suspended. The
// operation is refused expired, as it is when the lease is gone.
var errUploadGone = errors.New("dispatch: the upload's file is gone")

// contentPath is where the content with the hash sum lives under root,
// the attachments' directory: its shard, the hash's first two characters,
// then the hash.
func contentPath(root, sum string) string { return filepath.Join(root, sum[:2], sum) }

// received is an upload's content as receive wrote it: its temp file (""
// once it is removed), its hash and its size; the lease that holds the
// hash once it is taken, and whether publish found the temp file gone.
type received struct {
	tmp, sum string
	size     int64
	lease    string
	gone     bool
}

// AddAttachment attaches a file to the live ticket with the key
// (attachment.add): its content read from r, at most MaxAttachmentSize
// bytes; its name 1 to MaxAttachmentName bytes naming no directory (no
// '/', '\' or control character, neither "." nor ".."); its content type
// as given, at most MaxContentType bytes. The content is written to a temp
// file under attachments/tmp while it is hashed, and synced; a lease (an
// uploads row naming the hash) is committed in a transaction of its own;
// the file is published under its hash (publish: renamed into its shard,
// a file already there being the same content, and its new name made
// durable); then the operation writes the attachment's row. Its
// transaction deletes the lease first and is refused expired when the
// lease is gone, a sweep having dropped it as older than sweepGrace (the
// upload suspended between its two transactions, and its file perhaps
// gone with the lease), and is refused expired too when publish found no
// temp file (the sweep took that); a refusal's transaction drops the
// lease with it. Content past MaxAttachmentSize is refused bound, its temp
// file removed and nothing published; a ticket holds at most
// MaxAttachments live attachments (bound). One add row of the ticket's
// attachments field, its value the attachment whole. A deleted ticket is
// not_found; an expected revision is the ticket's; an archived project
// refuses it (archived). The receipt carries the attachment's id and the
// ticket's rev, and the attachment returned is the row the operation
// wrote, zero when it wrote none (a refusal, a repeat).
//
// An error, and no receipt kept, when the operation cannot be looked at
// (a bad op id, actor or display name, which is checked before r is read;
// the store closed), when the store's directory cannot be synced
// (attachmentDirs, until a sync of it has succeeded; before r is read),
// when r fails (its error, wrapped), when the file cannot be written or
// published (a sync among it), or when the database fails: the lease is
// dropped in a transaction of its own when the database answers, else left
// to expire, and a file published is the sweep's.
func (s *Store) AddAttachment(ctx context.Context, op Op, key, name, contentType string, r io.Reader) (Receipt, Attachment, error) {
	args := func(size int64, sum string) any {
		return struct {
			Key         string `json:"key"`
			Name        string `json:"name"`
			ContentType string `json:"contentType"`
			Size        int64  `json:"size"`
			SHA256      string `json:"sha256"`
		}{key, name, contentType, size, sum}
	}
	return s.addAttachment(ctx, op, "attachment.add", key, name, contentType, r, args, nil)
}

// addAttachment is AddAttachment under the intent, its content the
// operation's args of the content's size and hash, and also, when given,
// in the operation's transaction once the attachment's row and its add
// row are written: what an import's image commits with it (its manifest's
// entry), refused with the attachment.
func (s *Store) addAttachment(ctx context.Context, op Op, intent, key, name, contentType string, r io.Reader,
	argsOf func(size int64, sum string) any, also func(t *tx, tk Ticket, a Attachment) error) (Receipt, Attachment, error) {
	if err := op.check(); err != nil {
		return Receipt{}, Attachment{}, err
	}
	s.mu.Lock()
	closed, readOnly, why := s.closed, s.readOnly || s.db == nil, s.why
	syncDir, beforePublish, beforeCommit := s.syncDir, s.beforePublish, s.beforeCommit
	s.mu.Unlock()
	switch {
	case closed:
		return Receipt{}, Attachment{}, ErrClosed
	case readOnly:
		return readOnlyReceipt(op, intent, why), Attachment{}, nil
	}
	root, err := s.attachmentDirs(syncDir)
	if err != nil {
		return Receipt{}, Attachment{}, err
	}
	u, err := receive(filepath.Join(root, uploadsDir), r)
	if err != nil {
		return Receipt{}, Attachment{}, err
	}
	if u.tmp != "" {
		if u.lease, err = s.lease(ctx, u.sum); err != nil {
			os.Remove(u.tmp)
			return Receipt{}, Attachment{}, err
		}
		if beforePublish != nil {
			beforePublish()
		}
		switch err := publish(u.tmp, contentPath(root, u.sum), syncDir); {
		case errors.Is(err, errUploadGone):
			u.gone = true
		case err != nil:
			s.dropLease(ctx, u.lease)
			return Receipt{}, Attachment{}, err
		}
		if beforeCommit != nil {
			beforeCommit()
		}
	}
	args := argsOf(u.size, u.sum)
	// ran says the change ran, so its transaction dropped the lease
	// whichever way it ended, once it committed.
	ran := false
	var made Attachment
	rc, err := s.Apply(ctx, op, intent, args, func(t *tx) error {
		ran = true
		if u.lease != "" {
			drop := func() (bool, error) {
				res, err := t.tx.ExecContext(t.ctx, "DELETE FROM uploads WHERE id = ?", u.lease)
				if err != nil {
					return false, err
				}
				n, err := res.RowsAffected()
				return n == 1, err
			}
			t.onRefusal = func() error {
				_, err := drop()
				return err
			}
			held, err := drop()
			if err != nil {
				return err
			}
			if !held {
				return refuse(RefusalExpired, "the upload outlived its lease before its row was written: upload the file again")
			}
		}
		if u.gone {
			return refuse(RefusalExpired, "the upload's file was swept before it was published: upload the file again")
		}
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if err := checkField("attachment.name", "the name", name); err != nil {
			return err
		}
		if err := checkField("attachment.content_type", "the content type", contentType); err != nil {
			return err
		}
		if u.size > MaxAttachmentSize {
			return refuse(RefusalBound, "the file is more than %d bytes", int64(MaxAttachmentSize))
		}
		var n int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM attachments WHERE ticket_id = ? AND deleted_at = ''", tk.ID).Scan(&n); err != nil {
			return err
		}
		if err := checkCount("attachments", n+1, MaxAttachments); err != nil {
			return err
		}
		a := Attachment{ID: newID(), TicketID: tk.ID, Name: name, Size: u.size, SHA256: u.sum, ContentType: contentType, CreatedAt: t.now, CreatedBy: t.op.author()}
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO attachments (id, ticket_id, name, size, sha256, content_type, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			a.ID, a.TicketID, a.Name, a.Size, a.SHA256, a.ContentType, stamp(a.CreatedAt), a.CreatedBy); err != nil {
			return err
		}
		t.created(a.ID, "")
		value, err := encodeValue(a)
		if err != nil {
			return err
		}
		if err := t.record(ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID, Field: "attachments", Op: OpAdd, Value: value}); err != nil {
			return err
		}
		if also != nil {
			if err := also(t, tk, a); err != nil {
				return err
			}
		}
		made = a
		return nil
	})
	if u.lease != "" && (!ran || err != nil) {
		// The operation's transaction never dropped the lease: a repeat, an
		// op_conflict, or the database failing.
		s.dropLease(ctx, u.lease)
	}
	if err != nil || rc.Status != StatusDone || rc.Repeat {
		made = Attachment{}
	}
	return rc, made, err
}

// receive writes r's content, at most MaxAttachmentSize and one byte
// more, to a new temp file in dir (0600) while it hashes it, and syncs
// it: its name begins with when it was made, which the sweep reads. A
// content past the bound is hashed as far as it was read and its temp
// file removed (tmp ""); so is the file when r or the disk fails, which is
// the error.
func receive(dir string, r io.Reader) (received, error) {
	f, err := os.CreateTemp(dir, strconv.FormatInt(time.Now().UnixNano(), 10)+"-*")
	if err != nil {
		return received{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, MaxAttachmentSize+1))
	if err == nil && n <= MaxAttachmentSize {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	u := received{tmp: f.Name(), sum: hex.EncodeToString(h.Sum(nil)), size: n}
	if err != nil || n > MaxAttachmentSize {
		os.Remove(u.tmp)
		u.tmp = ""
	}
	if err != nil {
		return received{}, fmt.Errorf("dispatch: the attachment's content: %w", err)
	}
	return u, nil
}

// uploadMade reads when an upload's temp file was made from its name
// (receive), and whether the name is one of an upload's.
func uploadMade(name string) (time.Time, bool) {
	nanos, _, ok := strings.Cut(name, "-")
	if !ok {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, n), true
}

// attachmentDirs makes the attachments' directory and its uploads'
// directory when they are missing (0700) and returns the first. The first's
// name is made durable in the store's directory (syncDir) before anything
// is put in it: when it made it, and until a sync of the store's directory
// has succeeded in this store (dirSynced), which Open's is the first to
// try, so the name is durable whoever made it (another server on the same
// directory, or an open that stopped before its sync) before this store
// takes an upload, and a sync that failed at open is tried again by every
// upload until one succeeds, an upload whose sync fails being that error.
func (s *Store) attachmentDirs(syncDir func(string) error) (string, error) {
	root := filepath.Join(s.dir, attachmentsDir)
	made := false
	switch err := os.Mkdir(root, 0o700); {
	case err == nil:
		made = true
	case !errors.Is(err, fs.ErrExist):
		return "", err
	}
	s.mu.Lock()
	synced := s.dirSynced && !made
	s.mu.Unlock()
	if !synced {
		err := syncDir(s.dir)
		s.mu.Lock()
		s.dirSynced = err == nil
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
	}
	if err := os.Mkdir(filepath.Join(root, uploadsDir), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	return root, nil
}

// lease holds the hash sum for an upload: an uploads row naming it,
// committed in a transaction of its own before the file is published
// under it, so every sweep from then on leaves the file until the row
// that names it is committed and the lease dropped in its transaction. It
// returns the lease's id.
func (s *Store) lease(ctx context.Context, sum string) (string, error) {
	id := newID()
	return id, s.exec(ctx, "INSERT INTO uploads (id, sha256, created_at) VALUES (?, ?, ?)", id, sum, stamp(time.Now()))
}

// dropLease deletes an upload's lease in a transaction of its own, for an
// upload whose operation did not drop it (a publish that failed, a repeat,
// the database failing); one the database will not drop is left to expire
// (the sweep), which only keeps its file the longer.
func (s *Store) dropLease(ctx context.Context, id string) {
	_ = s.exec(context.WithoutCancel(ctx), "DELETE FROM uploads WHERE id = ?", id)
}

// exec runs one statement in a write transaction of its own (BEGIN
// IMMEDIATE), under the store's lock.
func (s *Store) exec(ctx context.Context, query string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrClosed
	case s.readOnly || s.db == nil:
		return fmt.Errorf("%w: %s", ErrReadOnly, s.why)
	}
	dbtx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cancelledAs(ctx, err)
	}
	defer dbtx.Rollback()
	if _, err := dbtx.ExecContext(ctx, query, args...); err != nil {
		return cancelledAs(ctx, err)
	}
	if err := dbtx.Commit(); err != nil {
		return cancelledAs(ctx, err)
	}
	return nil
}

// RemoveAttachment removes the live attachment with the id from the live
// ticket with the key (attachment.remove): its row tombstoned, one remove
// row of the ticket's attachments field, its value the attachment as it
// was and its prior the tag of the add that made it. No file is deleted,
// since an upload of the same content may be between its publication and
// its commit: the sweep deletes the file once no live row and no lease
// names it. An attachment an imported image was made into also puts the
// image's entry in the ticket's import manifest back to pending (one set
// row of external_snapshot), so the image is fetched again at the next
// read and no entry names a removed attachment. The attachment missing,
// removed or another ticket's is not_found; a deleted ticket is
// not_found; an expected revision is the ticket's; an archived project
// refuses it (archived).
func (s *Store) RemoveAttachment(ctx context.Context, op Op, key, id string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}{key, id}
	return s.Apply(ctx, op, "attachment.remove", args, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		a, err := scanAttachment(t.tx.QueryRowContext(t.ctx, "SELECT "+attachmentColumns+" FROM attachments WHERE id = ?", id))
		switch {
		case isNoRows(err):
			return refuse(RefusalNotFound, "no attachment %s", id)
		case err != nil:
			return err
		case a.TicketID != tk.ID:
			return refuse(RefusalNotFound, "the attachment %s is not %s's", id, tk.Key)
		case !a.DeletedAt.IsZero():
			return refuse(RefusalNotFound, "the attachment %s is removed", id)
		}
		adds, err := addsOf(t.ctx, t.tx, tk.ID, "attachments", []string{id})
		if err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(t.ctx, "UPDATE attachments SET deleted_at = ? WHERE id = ?", stamp(t.now), id); err != nil {
			return err
		}
		value, err := encodeValue(a)
		if err != nil {
			return err
		}
		if err := t.recordRemove(ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID, Field: "attachments", Value: value},
			rowTags([]string{id}, adds)); err != nil {
			return err
		}
		// An imported image's attachment: its manifest's entry goes back to
		// pending, so no entry names a removed attachment, here as in a
		// merge (ReconcileCounterpart), and the image is fetched again at
		// the next read.
		sn, ok := ParseSnapshot(string(tk.ExternalSnapshot))
		i := -1
		if ok {
			i = slices.IndexFunc(sn.Images, func(im SnapshotImage) bool { return im.AttachmentID == id })
		}
		if i < 0 {
			return nil
		}
		sn.Images[i] = SnapshotImage{ID: sn.Images[i].ID}
		snapshot, err := encodeSnapshot(sn)
		if err != nil {
			return err
		}
		return t.setScalars("tickets", ticketBase(tk), []fieldValue{{"external_snapshot", string(tk.ExternalSnapshot)}}, []fieldValue{{"external_snapshot", snapshot}})
	})
}

// Attachment reads the attachment with the id, removed or not.
// ErrNotFound when there is none.
func (s *Store) Attachment(ctx context.Context, id string) (Attachment, error) {
	var a Attachment
	err := s.read(ctx, func(q queryer) error {
		var err error
		a, err = scanAttachment(q.QueryRowContext(ctx, "SELECT "+attachmentColumns+" FROM attachments WHERE id = ?", id))
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	})
	return a, err
}

// OpenAttachment opens the content of the live attachment with the id, of
// a ticket deleted or not, whose page still lists it: the attachment's row
// and its file, opened by the hash the row names and never by a path a
// client gave, its size the row's. ErrNotFound when there is no such
// attachment or it is removed, a remove committed between the row's read
// and the file's open among them: the row's read transaction has ended by
// then, and the file the remove left to the sweep may be gone (a second
// store's sweep at its open), so a file missing is the row read again,
// and not found when that row is removed. An error when the content of a
// live row is missing or not what the row says. The caller closes the
// file.
func (s *Store) OpenAttachment(ctx context.Context, id string) (Attachment, *os.File, error) {
	a, err := s.Attachment(ctx, id)
	if err != nil {
		return Attachment{}, nil, err
	}
	if !a.DeletedAt.IsZero() {
		return Attachment{}, nil, ErrNotFound
	}
	if !hashPattern.MatchString(a.SHA256) {
		return Attachment{}, nil, fmt.Errorf("dispatch: attachment %s names no content: %q", id, a.SHA256)
	}
	s.mu.Lock()
	beforeOpen := s.beforeOpen
	s.mu.Unlock()
	if beforeOpen != nil {
		beforeOpen()
	}
	f, err := os.Open(contentPath(filepath.Join(s.dir, attachmentsDir), a.SHA256))
	if errors.Is(err, fs.ErrNotExist) && s.removedSince(ctx, id) {
		return Attachment{}, nil, ErrNotFound
	}
	if err != nil {
		return Attachment{}, nil, fmt.Errorf("dispatch: the content of attachment %s: %w", id, err)
	}
	fi, err := f.Stat()
	if err == nil && (!fi.Mode().IsRegular() || fi.Size() != a.Size) {
		err = fmt.Errorf("%d bytes where its row says %d", fi.Size(), a.Size)
	}
	if err != nil {
		f.Close()
		return Attachment{}, nil, fmt.Errorf("dispatch: the content of attachment %s: %w", id, err)
	}
	return a, f, nil
}

// removedSince reports whether the attachment with the id, live when
// OpenAttachment read it, is removed now, its row read again: a remove
// committed since, whose file the sweep may then have deleted. A row the
// database cannot read is not taken for removed, so a live row's missing
// file stays the store's error.
func (s *Store) removedSince(ctx context.Context, id string) bool {
	a, err := s.Attachment(ctx, id)
	return errors.Is(err, ErrNotFound) || (err == nil && !a.DeletedAt.IsZero())
}

// SetBeforeCommitForTest sets the seam an upload runs between its file's
// publication and the operation that writes its row (nil for none), for
// a test of the store's callers (the API's 409 expired): the store's own
// tests set the field.
func (s *Store) SetBeforeCommitForTest(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforeCommit = fn
}

// attachmentsOf reads the live attachments of the ticket with the id, in
// the order they were added.
func attachmentsOf(ctx context.Context, q queryer, ticketID string) ([]Attachment, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+attachmentColumns+" FROM attachments WHERE ticket_id = ? AND deleted_at = '' ORDER BY created_at, id", ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// attachmentColumns is an attachment's row as scanAttachment reads it.
const attachmentColumns = "id, ticket_id, name, size, sha256, content_type, created_at, created_by, deleted_at"

// scanAttachment reads an attachment's row.
func scanAttachment(row interface{ Scan(...any) error }) (Attachment, error) {
	var a Attachment
	var created, deleted string
	if err := row.Scan(&a.ID, &a.TicketID, &a.Name, &a.Size, &a.SHA256, &a.ContentType, &created, &a.CreatedBy, &deleted); err != nil {
		return Attachment{}, err
	}
	a.CreatedAt, a.DeletedAt = unstamp(created), unstamp(deleted)
	return a, nil
}
