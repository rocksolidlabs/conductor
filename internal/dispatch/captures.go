package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The operations on captures (Program item PV1,
// docs/plans/browser-evidence.md "Operations"): each a typed operation
// (Apply) whose content is the request's digest as the API received it
// (RequestDigest), so a resend is answered by its receipt, or once that is
// pruned by what its first row keeps (prunedAnswer: a create's
// fingerprint on the capture's row, an edit's on its set's change row, a
// delete's capture on its tombstone's row), each answered only by its own
// kind. A file an operation names (a create's screenshot, a send's
// document: sends.go) takes the upload
// path first (upload), and the operation's one transaction takes its
// lease with the rows; every way out of it after the file is published
// that commits no row naming the file (a refusal, a repeat, an error, a
// failed sync) ends in one collection (collectOne), which removes the
// file when nothing else names it.
//
// What a delete does: it REMOVES what only the live capture held (its
// screenshot's hold, the file collected as the delete commits when
// nothing else names it; its
// annotations' versions and sets, every note and element report with
// them), and it HIDES the rest: the page part stays in the capture's row
// and in the change row that made it, for the sends that froze it and for
// a later replication, and every read the store offers gives the deleted
// capture as its tombstone alone (its id, its revision, its times:
// Capture, ChangeValue), or not found (Document, OpenCaptureImage), or
// leaves it out (Captures). No change row is rewritten; an export holds
// what the store holds.

// The intents of the operations on captures beside IntentCaptureCreate.
const (
	IntentCaptureEdit   = "capture.edit"
	IntentCaptureDelete = "capture.delete"
)

// captureArgs is a capture operation's content, its fingerprint: the
// capture or the send it names, the request's digest, and what a send's
// outcome records (its target, why it was refused).
type captureArgs struct {
	ID       string `json:"id,omitempty"`
	Received string `json:"received,omitempty"`
	Target   string `json:"target,omitempty"`
	Why      string `json:"why,omitempty"`
}

// withUpload runs a capture operation that names a file: a resend is
// answered first (Replay) and uploads nothing; pre's refusal is kept with
// nothing uploaded; else r (nil for no file) takes the upload path, at
// most limit bytes, check refusing what it would not have published, and
// the operation's change takes the upload's lease first in its
// transaction. A lease the change never took is dropped, and a file
// published that no committed row names (the upload failed after its
// publication, the operation was refused, failed, or answered by another's
// receipt) is collected when nothing names it.
func (s *Store) withUpload(ctx context.Context, op Op, intent string, args captureArgs, pre func() error, r io.Reader, limit int64,
	check func(path string) error, change func(t *tx, u *received) error) (Receipt, error) {
	if err := op.check(); err != nil {
		return Receipt{}, err
	}
	if (args.Received != "" || intent != IntentCaptureDelete) && !hashPattern.MatchString(args.Received) {
		return Receipt{}, ErrBadReceived
	}
	if rc, found, err := s.Replay(ctx, op, intent, args); err != nil || found {
		return rc, err
	}
	if rc, ok, err := s.takes(op, intent); !ok || err != nil {
		return rc, err
	}
	if err := pre(); err != nil {
		return s.Apply(ctx, op, intent, args, func(*tx) error { return err })
	}
	var u *received
	if r != nil {
		got, err := s.upload(ctx, r, limit, check)
		if err != nil {
			if got.sum != "" {
				_ = s.collectOne(ctx, got.sum)
			}
			return Receipt{}, err
		}
		u = &got
	}
	ran := false
	rc, err := s.Apply(ctx, op, intent, args, func(t *tx) error {
		ran = true
		if u != nil {
			if err := t.takeUpload(*u); err != nil {
				return err
			}
			if u.size > limit {
				return refuse(RefusalBound, "the file is more than %d bytes", limit)
			}
		}
		return change(t, u)
	})
	if u != nil && u.lease != "" {
		if !ran || err != nil {
			s.dropLease(ctx, u.lease)
		}
		// Only a change that ran and committed done wrote a row naming it.
		if !ran || err != nil || rc.Status != StatusDone {
			_ = s.collectOne(ctx, u.sum)
		}
	}
	return rc, err
}

// CreateCapture records a capture (capture.create), received the digest
// of the client's request, n the client's words, f what the API read, and
// image the screenshot's PNG (nil for none: n says why). Everything is
// checked first (checkCapture: CheckNewCapture's rules first, then the
// origin and the facts);
// the screenshot is a PNG of at most MaxCaptureImage bytes and
// MaxCaptureImageSide pixels a side, its size read from its header before
// it is published, its regions within it; then one transaction writes the
// capture's row, its annotations' versions and their first set, the file
// it holds and the receipt, the lease released in it. At most MaxCaptures
// live (bound); a retake names a live capture of the same repository, or a
// link's of the same page origin, and records how the two are related
// (retake_via). The receipt's id is the capture's, its rev 1.
func (s *Store) CreateCapture(ctx context.Context, op Op, digest string, n NewCapture, f CaptureFacts, image io.Reader) (Receipt, error) {
	var c Capture
	var anns []Annotation
	pre := func() (err error) {
		c, anns, err = checkCapture(n, f, image != nil)
		return err
	}
	check := func(path string) error {
		w, h, err := pngSize(path)
		if err != nil {
			return err
		}
		c.Image.Width, c.Image.Height = w, h
		for _, a := range anns {
			if a.Kind == AnnotationRegion {
				if err := inImage(fmt.Sprintf("annotation %d", a.K), a.Rect, c.Image); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return s.withUpload(ctx, op, IntentCaptureCreate, captureArgs{Received: digest}, pre, image, MaxCaptureImage, check, func(t *tx, u *received) error {
		first, err := captureRoom(t.ctx, t.tx, n, f.Origin == OriginURL)
		if err != nil {
			return err
		}
		c.ID, c.CreatedAt, c.SavedAt, c.CreatedBy, c.Actor = newID(), t.now, t.now, t.op.author(), t.op.Actor
		if u != nil {
			c.Image.SHA256, c.Image.Bytes = u.sum, u.size
		}
		if c.RetakeOf != "" {
			if c.RetakeVia, err = t.retakeVia(c, first); err != nil {
				return err
			}
		}
		if err := t.insertCapture(c); err != nil {
			return err
		}
		t.created(c.ID, "")
		value, err := encodeValue(captureCreated{Capture: c, Fingerprint: t.fp})
		if err != nil {
			return err
		}
		if err := t.record(ChangeRow{Entity: EntityCapture, EntityID: c.ID, Op: OpCreate, Value: value}); err != nil {
			return err
		}
		if err := t.writeSet(c, 1, anns); err != nil {
			return err
		}
		if u == nil {
			return nil
		}
		return t.holdFile(EntityCapture, c.ID, c.ID, u.sum, holderCapture)
	})
}

// captureCreated is a capture's create row's value: its page part, what
// never changes, within 32 KiB by the bounds; its annotations are rows of
// their own (versions and sets), named by their digests.
type captureCreated struct {
	Capture
	Rev         *struct{} `json:"rev,omitempty"`
	Annotations *struct{} `json:"annotations,omitempty"`
	Set         *struct{} `json:"set,omitempty"`
	Fingerprint string    `json:"fingerprint"`
}

// CheckCaptureCreate judges a create by every rule of the store's that
// needs no tree's facts, before the API reads the tree: the client's words
// (CheckNewCapture; img nil for no screenshot, else its size as its header
// gives it), then, in one read of the store, room for the capture and the
// capture it retakes (captureRoom; link says the capture is a link's). A
// refusal is kept as the create's own, with its receipt, so a resend is
// answered by it (refused true); otherwise nothing is written.
func (s *Store) CheckCaptureCreate(ctx context.Context, op Op, digest string, n NewCapture, img *CaptureImage, link bool) (Receipt, bool, error) {
	err := CheckNewCapture(n, img)
	if err == nil {
		err = s.read(ctx, func(q queryer) error {
			_, err := captureRoom(ctx, q, n, link)
			return err
		})
	}
	var ref *Refusal
	if !errors.As(err, &ref) {
		return Receipt{}, false, err
	}
	rc, err := s.Apply(ctx, op, IntentCaptureCreate, captureArgs{Received: digest}, func(*tx) error { return ref })
	return rc, err == nil, err
}

// captureRoom judges what a create asks of the store that no tree's facts
// decide: room for one more live capture (MaxCaptures, bound), and, when n
// retakes one, the capture retaken there, live and of the same kind (link
// is whether n is a link's): a link's retakes a link's of the same page
// origin (pageOrigin), an attempt's an attempt's. It gives the capture
// retaken. The look before a tree is read (CheckCaptureCreate) and the
// create's transaction both judge by it.
func captureRoom(ctx context.Context, q queryer, n NewCapture, link bool) (Capture, error) {
	var live int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM captures WHERE deleted_at = ''").Scan(&live); err != nil {
		return Capture{}, err
	}
	if err := checkCount("live captures", live+1, MaxCaptures); err != nil || n.RetakeOf == "" {
		return Capture{}, err
	}
	first, _, err := readCapture(ctx, q, n.RetakeOf)
	switch {
	case errors.Is(err, ErrNotFound) || (err == nil && !first.DeletedAt.IsZero()):
		return Capture{}, refuse(RefusalInvalid, "the capture retaken is not a live capture")
	case err != nil:
		return Capture{}, err
	case link != (first.Origin.Kind == OriginURL) || (link && (pageOrigin(n.URL) == "" || pageOrigin(n.URL) != pageOrigin(first.URL))):
		return Capture{}, refuse(RefusalInvalid, "a link's capture retakes only a link's of the same page origin, and an attempt's an attempt's")
	}
	return first, nil
}

// retakeVia says how c is related to first, the capture it retakes as
// captureRoom gave it: run (the same run and task), else ticket (c's run
// was launched from a ticket that a send of the first was sent to, by
// sendState: sentTo), else repo (the same repository); a link's retake is
// related so by nothing more. Another repository's is refused invalid.
// One query decides the ticket, whatever the sends and the launches
// number; a retake written before the sends were the store's stays as it
// was recorded.
func (t *tx) retakeVia(c, first Capture) (string, error) {
	switch {
	case c.Origin.Kind == OriginURL:
		return "", nil
	case c.Scope.Repo == "" || first.Scope.Repo != c.Scope.Repo:
		return "", refuse(RefusalInvalid, "the capture retaken is another repository's")
	case first.Origin.Run == c.Origin.Run && first.Origin.Task == c.Origin.Task:
		return RetakeRun, nil
	}
	var ticket bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS (SELECT 1 FROM capture_sends s JOIN launches l ON l.run_id = ? JOIN tickets k ON k.id = l.ticket_id
		WHERE s.capture_id = ? AND s.kind = 'ticket' AND `+sentTo("s.id", "k.key")+`)`, c.Origin.Run, first.ID).Scan(&ticket); err != nil {
		return "", err
	}
	if ticket {
		return RetakeTicket, nil
	}
	return RetakeRepo, nil
}

// insertCapture writes a capture's row.
func (t *tx) insertCapture(c Capture) error {
	o := c.Origin
	var j [6]string
	for i, v := range []any{c.Viewport, o.ChangedPaths, c.Scope, o.Drift, o.Serving, SetPointer{}} {
		s, err := encodeValue(v)
		if err != nil {
			return err
		}
		j[i] = s
	}
	if o.Drift == nil {
		j[3] = ""
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO captures (id, created_at, created_by, actor, via, fingerprint, url, title, viewport,
		selected_at, saved_at, origin, run_id, task_id, attempt_n, tree, commit_id, changed, changed_paths, changed_cut, content, mutable,
		candidate, where_ran, scope, drift, serving, image_sha256, image_width, image_height, image_bytes, image_how, image_why, image_scale,
		epoch, retake_of, retake_via, annotations) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, stamp(c.CreatedAt), c.CreatedBy, c.Actor, c.Via, t.fp, c.URL, c.Title, j[0], stamp(c.SelectedAt), stamp(c.SavedAt), o.Kind,
		o.Run, o.Task, o.Attempt, o.Tree, o.Commit, o.Changed, j[1], o.ChangedCut, o.Content, o.Mutable, o.Candidate, o.Where, j[2], j[3], j[4],
		c.Image.SHA256, c.Image.Width, c.Image.Height, c.Image.Bytes, c.Image.How, c.Image.Why, c.Image.Scale, c.Epoch, c.RetakeOf, c.RetakeVia, j[5])
	return err
}

// writeSet writes a revision of c's annotations at rev: each version not
// yet held (content alone, by its SHA-256: one brought back names the
// earlier row), a set of its own naming them in their order, and the
// capture's pointer moved to it, one change row each, a version's holding
// its hash and size and never its body. At most MaxAnnotationSets sets
// (bound) and a document within MaxCaptureDocument (bound).
func (t *tx) writeSet(c Capture, rev int64, anns []Annotation) error {
	var sets int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM capture_annotation_sets WHERE capture_id = ?", c.ID).Scan(&sets); err != nil {
		return err
	}
	if err := checkCount("annotation sets: Capture again", sets+1, MaxAnnotationSets); err != nil {
		return err
	}
	doc := c.CaptureDocument
	doc.Rev, doc.Annotations = rev, anns
	if n := measure(doc); n > MaxCaptureDocument {
		return refuse(RefusalBound, "the capture's document would be %d bytes, more than %d", n, MaxCaptureDocument)
	}
	base := ChangeRow{Entity: EntityCapture, EntityID: c.ID}
	members := []string{}
	for _, a := range anns {
		a.K = 0
		body, err := encodeValue(a)
		if err != nil {
			return err
		}
		sum := sha256Hex(body)
		members = append(members, sum)
		var cols [4]string
		for i, v := range []any{a.Rect, a.Element, a.Observed, a.Sources} {
			if cols[i], err = encodeValue(v); err != nil {
				return err
			}
		}
		res, err := t.tx.ExecContext(t.ctx, `INSERT INTO capture_annotations (capture_id, sha256, bytes, kind, note, rect, element, observed, sources)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, c.ID, sum, len(body), a.Kind, a.Note, cols[0], cols[1], cols[2], cols[3])
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			row := base
			row.Field, row.Op, row.Value = "versions", OpAdd, fmt.Sprintf(`{"sha256":%q,"bytes":%d}`, sum, len(body))
			if err := t.record(row); err != nil {
				return err
			}
		}
	}
	encoded, err := encodeValue(members)
	if err != nil {
		return err
	}
	ptr := SetPointer{Rev: rev, Set: newID()}
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO capture_annotation_sets (capture_id, id, rev, digest, members, at, actor, op_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, ptr.Set, rev, sha256Hex(encoded), encoded, stamp(t.now), t.op.Actor, t.op.ID); err != nil {
		return err
	}
	// The set's row keeps the operation's fingerprint: an edit resent once
	// its receipt is pruned is answered by it (prunedAnswer).
	row := base
	row.Field, row.Op, row.Value = "sets", OpAdd, fmt.Sprintf(`{"id":%q,"rev":%d,"digest":%q,"members":%s,"fingerprint":%q}`, ptr.Set, rev,
		sha256Hex(encoded), encoded, t.fp)
	if err := t.record(row); err != nil {
		return err
	}
	value, err := encodeValue(ptr)
	if err != nil {
		return err
	}
	prior, err := encodeValue(c.Set)
	if err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(t.ctx, "UPDATE captures SET annotations = ? WHERE id = ?", value, c.ID); err != nil {
		return err
	}
	row = base
	row.Field, row.Op, row.Value, row.Prior = "annotations", OpSet, value, prior
	return t.record(row)
}

// holdFile writes the row that holds a content file for a live capture
// (holder capture) or a send (holder its id), one add row of the entity's
// files.
func (t *tx) holdFile(entity, id, captureID, sum, holder string) error {
	if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO capture_files (capture_id, sha256, holder) VALUES (?, ?, ?)", captureID, sum, holder); err != nil {
		return err
	}
	return t.record(ChangeRow{Entity: entity, EntityID: id, Field: "files", Op: OpAdd, Value: fmt.Sprintf(`{"sha256":%q,"holder":%q}`, sum, holder)})
}

// liveCapture reads the capture with the id inside the operation, with
// its live annotations: not_found when there is none or it is deleted.
func (t *tx) liveCapture(id string) (Capture, error) {
	c, anns, err := readCapture(t.ctx, t.tx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return Capture{}, refuse(RefusalNotFound, "no capture %s", id)
	case err != nil:
		return Capture{}, err
	case !c.DeletedAt.IsZero():
		return Capture{}, refuse(RefusalNotFound, "the capture %s is deleted", id)
	}
	c.Annotations = anns
	return c, nil
}

// EditCapture edits the live capture with the id at its revision
// expectedRev (capture.edit): edits is its annotations in their new order,
// each one it holds named by its place (K) with its note, and a region's
// rectangle within the screenshot, or a new region (K 0); one left out is
// removed. An element keeps its report, moment and sources as saved, and a
// new element's report is refused invalid ("Capture again"). The edit is
// checked first as it is handed in (CheckCaptureEdit). One transaction
// writes a version of each annotation that changed, a new set and the
// pointer, and bumps the capture's rev. Refused stale_revision for another
// revision, not_found for a deleted capture, bound past MaxAnnotationSets.
func (s *Store) EditCapture(ctx context.Context, op Op, digest, id string, expectedRev int64, edits []AnnotationEdit) (Receipt, error) {
	return s.withUpload(ctx, op, IntentCaptureEdit, captureArgs{ID: id, Received: digest}, func() error { return nil }, nil, 0, nil, func(t *tx, _ *received) error {
		if err := CheckCaptureEdit(edits); err != nil {
			return err
		}
		c, err := t.liveCapture(id)
		if err != nil {
			return err
		}
		if c.Rev != expectedRev {
			return refuse(RefusalStaleRevision, "the capture is at r%d, not the revision the edit names", c.Rev)
		}
		ch := &checker{}
		ch.count("annotations", len(edits), MaxCaptureAnnotations)
		next := make([]Annotation, 0, len(edits))
		seen := map[int]bool{}
		for i, e := range edits {
			label := fmt.Sprintf("annotation %d", i+1)
			a := Annotation{Kind: AnnotationRegion}
			ch.must(e.Element == nil, RefusalInvalid, "%s brings a new element: a new element is a new capture: Capture again", label)
			if e.K != 0 {
				ch.must(e.K >= 1 && e.K <= len(c.Annotations) && !seen[e.K], RefusalInvalid, "%s names an annotation that is not one of the capture's once", label)
				if ch.err != nil {
					return ch.err
				}
				seen[e.K], a = true, c.Annotations[e.K-1]
			}
			ch.must(e.Kind == a.Kind, RefusalInvalid, "%s is not of the kind the capture holds there", label)
			ch.field("annotation.note", label+"'s note", e.Note)
			a.K, a.Note = len(next)+1, e.Note
			if a.Kind == AnnotationRegion {
				ch.rect(label+"'s rectangle", e.Rect)
				ch.must(c.Image.How != ImageNone, RefusalInvalid, "%s is a region, drawn on a screenshot, and this capture has none", label)
				if ch.err == nil {
					ch.err = inImage(label, e.Rect, c.Image)
				}
				a.Rect = e.Rect
			} else {
				ch.must(e.Rect == Rect{} || e.Rect == a.Rect, RefusalInvalid, "%s is an element, whose box is as it was saved", label)
			}
			next = append(next, a)
		}
		if ch.err != nil {
			return ch.err
		}
		return t.writeSet(c, c.Rev+1, next)
	})
}

// DeleteCapture deletes the live capture with the id (capture.delete): a
// tombstone, and in the same transaction what only the live capture held
// removed, its screenshot's row (holder capture, one remove row of its
// files) and its annotations' versions and sets, so a capture with no
// send leaves nothing the sweep must keep; the rest is hidden by every
// read (the package's comment says what a delete removes and what it
// hides). Its sends stay, each readable as a row, and so do the ticket
// attachments they made; a sent send and a pending one keep the files they
// hold, and one that ended refused or cancelled, having delivered nothing,
// lets its files go in the same transaction (releaseEnded; a pending one
// does when it ends so), so its files answer not_found. Once the delete
// has committed, the files it released go through the one collection
// (collectOne): at once when nothing else names them, kept when anything
// does. not_found when there is none or it is deleted.
func (s *Store) DeleteCapture(ctx context.Context, op Op, id string) (Receipt, error) {
	var released []string
	rc, err := s.withUpload(ctx, op, IntentCaptureDelete, captureArgs{ID: id}, func() error { return nil }, nil, 0, nil, func(t *tx, _ *received) error {
		c, err := t.liveCapture(id)
		if err != nil {
			return err
		}
		base := ChangeRow{Entity: EntityCapture, EntityID: id}
		if err := t.setScalars("captures", base, []fieldValue{{"deleted_at", ""}}, []fieldValue{{"deleted_at", stamp(t.now)}}); err != nil {
			return err
		}
		if c.Image.SHA256 != "" {
			var tag Tag
			if err := t.tx.QueryRowContext(t.ctx, "SELECT actor, op_id, n FROM changes WHERE entity = ? AND entity_id = ? AND field = 'files' AND op = ?",
				EntityCapture, id, OpAdd).Scan(&tag.Actor, &tag.OpID, &tag.N); err != nil && !isNoRows(err) {
				return err
			}
			row := base
			row.Field, row.Value = "files", fmt.Sprintf(`{"sha256":%q,"holder":%q}`, c.Image.SHA256, holderCapture)
			if err := t.recordRemove(row, []Tag{tag}); err != nil {
				return err
			}
		}
		for _, q := range []string{"DELETE FROM capture_files WHERE capture_id = ? AND holder = 'capture'", "DELETE FROM capture_annotations WHERE capture_id = ?",
			"DELETE FROM capture_annotation_sets WHERE capture_id = ?"} {
			if _, err := t.tx.ExecContext(t.ctx, q, id); err != nil {
				return err
			}
		}
		// After the tombstone's row, the delete's first.
		if released, err = t.releaseEnded(id, ""); err == nil && c.Image.SHA256 != "" {
			released = append(released, c.Image.SHA256)
		}
		return err
	})
	s.collectAll(ctx, rc, err, released)
	return rc, err
}

// captureColumns is a capture's row as scanCapture reads it.
const captureColumns = `id, rev, created_at, created_by, actor, via, url, title, viewport, selected_at, saved_at, origin, run_id, task_id,
	attempt_n, tree, commit_id, changed, changed_paths, changed_cut, content, mutable, candidate, where_ran, scope, drift, serving, image_sha256,
	image_width, image_height, image_bytes, image_how, image_why, image_scale, epoch, retake_of, retake_via, deleted_at, annotations`

// scanCapture reads a capture's row.
func scanCapture(row interface{ Scan(...any) error }) (Capture, error) {
	var c Capture
	o := &c.Origin
	var created, selected, saved, deleted, viewport, paths, scope, driftJSON, servingJSON, set string
	if err := row.Scan(&c.ID, &c.Rev, &created, &c.CreatedBy, &c.Actor, &c.Via, &c.URL, &c.Title, &viewport, &selected, &saved, &o.Kind, &o.Run,
		&o.Task, &o.Attempt, &o.Tree, &o.Commit, &o.Changed, &paths, &o.ChangedCut, &o.Content, &o.Mutable, &o.Candidate, &o.Where, &scope,
		&driftJSON, &servingJSON, &c.Image.SHA256, &c.Image.Width, &c.Image.Height, &c.Image.Bytes, &c.Image.How, &c.Image.Why, &c.Image.Scale,
		&c.Epoch, &c.RetakeOf, &c.RetakeVia, &deleted, &set); err != nil {
		return Capture{}, err
	}
	c.Schema, c.CreatedAt, c.SelectedAt, c.SavedAt, c.DeletedAt = CaptureSchemaName, unstamp(created), unstamp(selected), unstamp(saved), unstamp(deleted)
	for _, j := range []struct {
		s string
		v any
	}{{viewport, &c.Viewport}, {paths, &o.ChangedPaths}, {scope, &c.Scope}, {driftJSON, &o.Drift}, {servingJSON, &o.Serving}, {set, &c.Set}} {
		if j.s != "" {
			if err := json.Unmarshal([]byte(j.s), j.v); err != nil {
				return Capture{}, fmt.Errorf("dispatch: capture %s: %w", c.ID, err)
			}
		}
	}
	return c, nil
}

// readCapture reads the capture with the id and the annotations of its
// live set in their order: ErrNotFound when there is none, or the
// database is older than captureSchema.
func readCapture(ctx context.Context, q queryer, id string) (Capture, []Annotation, error) {
	if schemaOf(q) < captureSchema {
		return Capture{}, nil, ErrNotFound
	}
	c, err := scanCapture(q.QueryRowContext(ctx, "SELECT "+captureColumns+" FROM captures WHERE id = ?", id))
	if isNoRows(err) {
		return Capture{}, nil, ErrNotFound
	}
	if err != nil || !c.DeletedAt.IsZero() {
		return c, nil, err
	}
	var members string
	if err := q.QueryRowContext(ctx, "SELECT members FROM capture_annotation_sets WHERE capture_id = ? AND id = ?", id, c.Set.Set).Scan(&members); err != nil {
		return Capture{}, nil, fmt.Errorf("dispatch: capture %s's set %s: %w", id, c.Set.Set, err)
	}
	var sums []string
	if err := json.Unmarshal([]byte(members), &sums); err != nil {
		return Capture{}, nil, err
	}
	anns := make([]Annotation, 0, len(sums))
	for i, sum := range sums {
		a := Annotation{K: i + 1}
		var rect, element, observed, sources string
		if err := q.QueryRowContext(ctx, "SELECT kind, note, rect, element, observed, sources FROM capture_annotations WHERE capture_id = ? AND sha256 = ?",
			id, sum).Scan(&a.Kind, &a.Note, &rect, &element, &observed, &sources); err != nil {
			return Capture{}, nil, fmt.Errorf("dispatch: capture %s's annotation %s: %w", id, sum, err)
		}
		for _, j := range []struct {
			s string
			v any
		}{{rect, &a.Rect}, {element, &a.Element}, {observed, &a.Observed}, {sources, &a.Sources}} {
			if err := json.Unmarshal([]byte(j.s), j.v); err != nil {
				return Capture{}, nil, fmt.Errorf("dispatch: capture %s's annotation %s: %w", id, sum, err)
			}
		}
		anns = append(anns, a)
	}
	return c, anns, nil
}

// documentOf is a live capture's document at its revision now.
func documentOf(c Capture) CaptureDocument {
	d := c.CaptureDocument
	if d.Annotations == nil {
		d.Annotations = []Annotation{}
	}
	return d
}

// Document is the live capture with the id's document at rev, its
// revision now, as a send freezes it: ErrNotFound for none or a deleted
// one, ErrStaleCapture for another revision (an earlier one is read from
// a send that froze it).
func (s *Store) Document(ctx context.Context, id string, rev int64) ([]byte, error) {
	var out []byte
	err := s.read(ctx, func(q queryer) error {
		c, anns, err := readCapture(ctx, q, id)
		switch {
		case err != nil:
			return err
		case !c.DeletedAt.IsZero():
			return ErrNotFound
		case c.Rev != rev:
			return ErrStaleCapture
		}
		c.Annotations = anns
		s, err := encodeValue(documentOf(c))
		out = []byte(s)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Capture reads the capture with the id, its annotations and its sends
// (without their requests: CaptureSend), as a read gives it: a deleted
// capture is its tombstone alone (Capture), its sends read by their own
// reads. ErrNotFound when there is none.
func (s *Store) Capture(ctx context.Context, id string) (Capture, error) {
	var c Capture
	err := s.read(ctx, func(q queryer) error {
		var anns []Annotation
		var err error
		if c, anns, err = readCapture(ctx, q, id); err != nil {
			return err
		}
		if !c.DeletedAt.IsZero() {
			d := tombstoneOf(c)
			c = Capture{CaptureDocument: CaptureDocument{ID: d.ID, Rev: d.Rev}, CreatedAt: d.CreatedAt, DeletedAt: d.DeletedAt}
			return nil
		}
		c.Annotations = anns
		c.Sends, err = readSends(ctx, q, "WHERE s.capture_id = ? ORDER BY s.created_at, s.id", id)
		return err
	})
	if err != nil {
		return Capture{}, err
	}
	return c, nil
}

// Answer is c as a read answers it: a deleted capture's tombstone alone
// (its id, its revision and its times), else c.
func (c Capture) Answer() any {
	if !c.DeletedAt.IsZero() {
		return tombstoneOf(c)
	}
	return c
}

// tombstoneOf is a deleted capture as every read gives it.
func tombstoneOf(c Capture) captureTombstone {
	return captureTombstone{ID: c.ID, Rev: c.Rev, CreatedAt: c.CreatedAt, DeletedAt: c.DeletedAt}
}

// deletedCapture is the tombstone of the capture with the id, as a read
// of a change row of it gives it: found false when there is none, it is
// live, or the database is older than captureSchema.
func deletedCapture(ctx context.Context, q queryer, id string) (string, bool, error) {
	c, _, err := readCapture(ctx, q, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return "", false, nil
	case err != nil:
		return "", false, err
	case c.DeletedAt.IsZero():
		return "", false, nil
	}
	v, err := encodeValue(tombstoneOf(c))
	return v, err == nil, err
}

// CaptureFilter picks a list's captures: a run's (Run), a task's of it
// (and Task), an attempt's (and Attempt); a ticket's (Ticket, its key:
// the captures a send was sent to, by sendState); none, the most recent.
type CaptureFilter struct {
	Run     string
	Task    string
	Attempt int
	Ticket  string
}

// CapturesPage is a page of live captures, newest first, each with its
// first annotation alone, that annotation's note cut to 200 characters;
// Next is the cursor of the next page, "" at the end.
type CapturesPage struct {
	Captures []Capture `json:"captures"`
	Next     string    `json:"next,omitempty"`
}

// Captures lists the live captures the filter picks, newest first, after
// the capture with the id after (a page's Next, a capture deleted since
// among them), at most limit, 1 to MaxCapturePage. A ticket's are its
// sends': a capture is listed there once a send of it was sent to the
// ticket. ErrBadQuery for a limit out of range, a task with no run or an
// attempt with no task, a ticket beside a run or no ticket's key, or an
// after that names no capture.
func (s *Store) Captures(ctx context.Context, f CaptureFilter, after string, limit int) (CapturesPage, error) {
	_, _, isKey := ParseKey(f.Ticket)
	switch {
	case limit < 1 || limit > MaxCapturePage:
		return CapturesPage{}, badQuery("a page of captures is 1 to %d, not %d", MaxCapturePage, limit)
	case (f.Task != "" && f.Run == "") || (f.Attempt != 0 && f.Task == "") || f.Attempt < 0:
		return CapturesPage{}, badQuery("a page of captures names a task of a run, and an attempt of a task")
	case f.Ticket != "" && (!isKey || f.Run != ""):
		return CapturesPage{}, badQuery("a page of a ticket's captures names a ticket's key, and no run")
	}
	page := CapturesPage{Captures: []Capture{}}
	where, args := []string{"deleted_at = ''"}, []any{}
	if f.Ticket != "" {
		where = append(where, `id IN (SELECT s.capture_id FROM capture_sends s WHERE s.kind = 'ticket' AND s.id IN
			(SELECT send_id FROM capture_send_outcomes WHERE kind = 'sent' AND target = ?) AND `+sentTo("s.id", "?")+`)`)
		args = append(args, f.Ticket, f.Ticket)
	}
	for _, w := range []struct {
		col string
		v   any
		set bool
	}{{"run_id", f.Run, f.Run != ""}, {"task_id", f.Task, f.Task != ""}, {"attempt_n", f.Attempt, f.Attempt > 0}} {
		if w.set {
			where, args = append(where, w.col+" = ?"), append(args, w.v)
		}
	}
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < captureSchema {
			return nil
		}
		if after != "" {
			var there bool
			if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM captures WHERE id = ?)", after).Scan(&there); err != nil {
				return err
			}
			if !there {
				return badQuery("the page's cursor names no capture")
			}
			where = append(where, "(created_at, id) < (SELECT created_at, id FROM captures WHERE id = ?)")
			args = append(args, after)
		}
		rows, err := q.QueryContext(ctx, "SELECT "+captureColumns+" FROM captures WHERE "+strings.Join(where, " AND ")+" ORDER BY created_at DESC, id DESC LIMIT ?",
			append(args, limit+1)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCapture(rows)
			if err != nil {
				return err
			}
			page.Captures = append(page.Captures, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Captures) > limit {
			page.Captures, page.Next = page.Captures[:limit], page.Captures[limit-1].ID
		}
		// A listing shows the first annotation's kind and note alone: one
		// row of two columns, by the live set's first member.
		for i := range page.Captures {
			c := &page.Captures[i]
			var kind, note string
			switch err := q.QueryRowContext(ctx, `SELECT a.kind, a.note FROM capture_annotation_sets s JOIN capture_annotations a
				ON a.capture_id = s.capture_id AND a.sha256 = json_extract(s.members, '$[0]') WHERE s.capture_id = ? AND s.id = ?`,
				c.ID, c.Set.Set).Scan(&kind, &note); {
			case isNoRows(err):
			case err != nil:
				return err
			default:
				c.Annotations = []Annotation{{K: 1, Kind: kind, Note: cutChars(note, 200)}}
			}
		}
		return nil
	})
	if err != nil {
		return CapturesPage{}, err
	}
	return page, nil
}

// cutChars is s cut to at most n characters.
func cutChars(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n])
	}
	return s
}

// OpenCaptureImage opens the live capture with the id's screenshot, by
// the hash its own file row names: ErrNotFound for none, a deleted
// capture, or one with no screenshot. The caller closes the file.
func (s *Store) OpenCaptureImage(ctx context.Context, id string) (Capture, *os.File, error) {
	c, err := s.Capture(ctx, id)
	if err != nil {
		return Capture{}, nil, err
	}
	if !c.DeletedAt.IsZero() || c.Image.SHA256 == "" {
		return Capture{}, nil, ErrNotFound
	}
	f, err := s.openHeld(ctx, id, c.Image.SHA256, holderCapture)
	if err != nil {
		return Capture{}, nil, err
	}
	return c, f, nil
}

// openHeld opens a content file a capture_files row of the holder names:
// ErrNotFound when no row names it, a release committed between the row's
// read and the file's open among them (the file missing, the row read
// again, as OpenAttachment does).
func (s *Store) openHeld(ctx context.Context, captureID, sum, holder string) (*os.File, error) {
	if !hashPattern.MatchString(sum) {
		return nil, ErrNotFound
	}
	isHeld := func() (bool, error) {
		var held bool
		err := s.read(ctx, func(q queryer) error {
			return q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM capture_files WHERE sha256 = ? AND holder = ? AND capture_id = ?)",
				sum, holder, captureID).Scan(&held)
		})
		return held, err
	}
	held, err := isHeld()
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	beforeOpen := s.beforeOpen
	s.mu.Unlock()
	if beforeOpen != nil {
		beforeOpen()
	}
	f, err := os.Open(contentPath(filepath.Join(s.dir, attachmentsDir), sum))
	if errors.Is(err, fs.ErrNotExist) {
		if still, rerr := isHeld(); rerr == nil && !still {
			return nil, ErrNotFound
		}
	}
	if err != nil {
		return nil, fmt.Errorf("dispatch: the content %s held by %s: %w", sum, holder, err)
	}
	return f, nil
}
