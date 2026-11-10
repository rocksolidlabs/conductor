package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The operations on captures and their sends (Program item PV1,
// docs/plans/browser-evidence.md "Operations"): each a typed operation
// (Apply) whose content is the request's digest as the API received it
// (RequestDigest), so a resend is answered by its receipt, or once that is
// pruned by the fingerprint the capture's or the send's row keeps
// (prunedAnswer). A file an operation names (a create's screenshot, a
// send's document) takes the upload path first (upload), and the
// operation's one transaction takes its lease with the rows; a refused
// operation leaves no row, and the file it published is collected at once
// when nothing else names it (collectOne).

// The intents of the operations on captures beside IntentCaptureCreate and
// IntentCaptureSend.
const (
	IntentCaptureEdit   = "capture.edit"
	IntentCaptureDelete = "capture.delete"
	IntentSendSent      = "capture_send.sent"
	IntentSendRefused   = "capture_send.refused"
	IntentSendCancelled = "capture_send.cancelled"
	IntentSendCancel    = "capture_send.cancel"
)

// captureArgs is a capture operation's content, its fingerprint: the
// capture or the send it names, the request's digest, and what a send's
// outcome records.
type captureArgs struct {
	ID       string `json:"id,omitempty"`
	Received string `json:"received,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Target   string `json:"target,omitempty"`
	Why      string `json:"why,omitempty"`
}

// withUpload runs a capture operation that names a file: a resend is
// answered first (Replay) and uploads nothing; pre's refusal is kept with
// nothing uploaded; else r (nil for no file) takes the upload path, at
// most limit bytes, check refusing what it would not have published, and
// the operation's change takes the upload's lease first in its
// transaction. A lease the change never took is dropped, and a file a
// refused or failed operation published is collected when nothing names
// it.
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
		if err != nil || rc.Status != StatusDone {
			_ = s.collectOne(ctx, u.sum)
		}
	}
	return rc, err
}

// CreateCapture records a capture (capture.create), received the digest
// of the client's request, n the client's words, f what the API read, and
// image the screenshot's PNG (nil for none: n says why). Everything is
// checked first (checkCapture: every bound, the one moment, the origin);
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
		var live int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM captures WHERE deleted_at = ''").Scan(&live); err != nil {
			return err
		}
		if err := checkCount("live captures", live+1, MaxCaptures); err != nil {
			return err
		}
		c.ID, c.CreatedAt, c.SavedAt, c.CreatedBy, c.Actor = newID(), t.now, t.now, t.op.author(), t.op.Actor
		if u != nil {
			c.Image.SHA256, c.Image.Bytes = u.sum, u.size
		}
		if c.RetakeOf != "" {
			var err error
			if c.RetakeVia, err = t.retakeVia(c); err != nil {
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

// retakeVia checks the capture c retakes and says how the two are related:
// run (the same run and task), ticket (c's run was launched from a ticket
// a sent send of the first targets), else repo (the same repository); a
// link's capture retakes a link's of the same page origin, related so by
// nothing more. A deleted capture, another repository's or another
// origin's is refused invalid.
func (t *tx) retakeVia(c Capture) (string, error) {
	first, _, err := readCapture(t.ctx, t.tx, c.RetakeOf)
	if err != nil || !first.DeletedAt.IsZero() {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", err
		}
		return "", refuse(RefusalInvalid, "the capture retaken, %s, is not a live capture", c.RetakeOf)
	}
	link, firstLink := c.Origin.Kind == OriginURL, first.Origin.Kind == OriginURL
	switch {
	case link || firstLink:
		if link != firstLink || pageOrigin(c.URL) != pageOrigin(first.URL) {
			return "", refuse(RefusalInvalid, "a link's capture retakes only a link's of the same page origin")
		}
		return "", nil
	case c.Scope.Repo == "" || first.Scope.Repo != c.Scope.Repo:
		return "", refuse(RefusalInvalid, "the capture retaken is another repository's")
	case first.Origin.Run == c.Origin.Run && first.Origin.Task == c.Origin.Task:
		return RetakeRun, nil
	}
	var ticket bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS (SELECT 1 FROM launches l JOIN tickets k ON k.id = l.ticket_id
		JOIN capture_sends s ON s.capture_id = ? AND s.kind = 'ticket' JOIN capture_send_outcomes o ON o.send_id = s.id AND `+earliestSent+`
		WHERE l.run_id = ? AND o.target = k.key)`, first.ID, c.Origin.Run).Scan(&ticket); err != nil {
		return "", err
	}
	if ticket {
		return RetakeTicket, nil
	}
	return RetakeRepo, nil
}

// earliestSent restricts an outcome o to its send's earliest sent one, the
// one sendState decides by.
const earliestSent = `o.id = (SELECT e.id FROM capture_send_outcomes e WHERE e.send_id = o.send_id AND e.kind = 'sent'
	ORDER BY e.at, e.actor, e.op_id, e.id LIMIT 1)`

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
	row := base
	row.Field, row.Op, row.Value = "sets", OpAdd, fmt.Sprintf(`{"id":%q,"rev":%d,"digest":%q,"members":%s}`, ptr.Set, rev, sha256Hex(encoded), encoded)
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
// new element's report is refused invalid ("Capture again"). One
// transaction writes a version of each annotation that changed, a new set
// and the pointer, and bumps the capture's rev. Refused stale_revision for
// another revision, not_found for a deleted capture, bound past
// MaxAnnotationSets.
func (s *Store) EditCapture(ctx context.Context, op Op, digest, id string, expectedRev int64, edits []AnnotationEdit) (Receipt, error) {
	return s.withUpload(ctx, op, IntentCaptureEdit, captureArgs{ID: id, Received: digest}, func() error { return nil }, nil, 0, nil, func(t *tx, _ *received) error {
		c, err := t.liveCapture(id)
		if err != nil {
			return err
		}
		if c.Rev != expectedRev {
			return refuse(RefusalStaleRevision, "the capture is at r%d, not r%d", c.Rev, expectedRev)
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
				ch.must(e.K >= 1 && e.K <= len(c.Annotations) && !seen[e.K], RefusalInvalid, "%s names annotation %d, which is not one of the capture's once", label, e.K)
				if ch.err != nil {
					return ch.err
				}
				seen[e.K], a = true, c.Annotations[e.K-1]
			}
			ch.must(e.Kind == a.Kind, RefusalInvalid, "%s is %s, not %s", label, a.Kind, e.Kind)
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
// files) and its annotations' versions and sets, so a capture deleted
// unsent leaves nothing the sweep must keep. Its sends, the files they
// hold and the ticket attachments they made stay. not_found when there is
// none or it is deleted.
func (s *Store) DeleteCapture(ctx context.Context, op Op, id string) (Receipt, error) {
	return s.withUpload(ctx, op, IntentCaptureDelete, captureArgs{ID: id}, func() error { return nil }, nil, 0, nil, func(t *tx, _ *received) error {
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
		return nil
	})
}

// SendCapture sends the live capture with the id at rev, its revision now
// (stale_revision otherwise: a person sends what they see), to a ticket
// or to Brakeman (capture.send). Its document is built from the rows at
// rev and published as a content file through the upload path; one
// transaction then writes the send's input (for Brakeman with the request
// the API made, which leaves it pending), the rows holding the document
// and the screenshot, and for a ticket the ticket (new, its description
// ticketText; or the live one with the key), the attachments naming the
// screenshot's and the document's hashes (the document alone without a
// screenshot), a comment naming the capture and its revision, and the
// send's sent outcome; the ticket's rev bumped once. At most
// MaxCaptureSends a capture and MaxAttachments a ticket (bound). review
// and decision are refused invalid until their items lift it. The
// receipt's id is the send's, its key the ticket's.
func (s *Store) SendCapture(ctx context.Context, op Op, digest, id string, rev int64, to Destination, f SendFacts) (Receipt, error) {
	pre := func() error {
		c := &checker{}
		switch to.Kind {
		case SendTicket:
			c.must((to.Key == "") != (to.Project == "" && to.Title == ""), RefusalInvalid, "a ticket's send names a ticket's key, or a project and a title")
			if to.Key == "" {
				c.field("ticket.title", "the title", to.Title)
			}
			c.must(len(f.Request) == 0, RefusalInvalid, "a ticket's send carries no request")
		case SendBrakeman:
			c.must(to.Key == "" && to.Project == "" && to.Title == "", RefusalInvalid, "a send to Brakeman names no ticket")
			c.must(len(f.Request) <= MaxSendRequest, RefusalBound, "the request is more than %d bytes", MaxSendRequest)
			c.must(len(f.Request) > 0 && json.Valid(f.Request), RefusalInvalid, "a send to Brakeman carries its request as JSON")
		default:
			c.must(false, RefusalInvalid, "a capture is sent to a ticket or to Brakeman, not to %q", to.Kind)
		}
		return c.err
	}
	var r io.Reader
	doc, err := s.Document(ctx, id, rev)
	switch {
	case err == nil:
		r = bytes.NewReader(doc)
	case !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrStaleCapture):
		return Receipt{}, err
	}
	return s.withUpload(ctx, op, IntentCaptureSend, captureArgs{ID: id, Received: digest}, pre, r, MaxCaptureDocument, nil, func(t *tx, u *received) error {
		c, err := t.liveCapture(id)
		if err != nil {
			return err
		}
		if c.Rev != rev {
			return refuse(RefusalStaleRevision, "the capture is at r%d, not r%d: send what you see", c.Rev, rev)
		}
		body, err := encodeValue(documentOf(c))
		if err != nil {
			return err
		}
		if u == nil || sha256Hex(body) != u.sum {
			return refuse(RefusalStaleRevision, "the capture changed while its document was made: send it again")
		}
		var sends int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM capture_sends WHERE capture_id = ?", id).Scan(&sends); err != nil {
			return err
		}
		if err := checkCount("sends", sends+1, MaxCaptureSends); err != nil {
			return err
		}
		asked, err := encodeValue(to)
		if err != nil {
			return err
		}
		sv := CaptureSend{ID: newID(), Capture: id, Rev: rev, Kind: to.Kind, Asked: json.RawMessage(asked), Digest: u.sum, Request: f.Request,
			CreatedAt: t.now, CreatedBy: t.op.author(), Actor: t.op.Actor, OpID: t.op.ID}
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO capture_sends (id, capture_id, rev, kind, asked, digest, request, created_at, created_by,
			actor, op_id, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, sv.ID, id, rev, sv.Kind, asked, sv.Digest, string(f.Request),
			stamp(t.now), sv.CreatedBy, sv.Actor, sv.OpID, t.fp); err != nil {
			return err
		}
		value, err := encodeValue(struct {
			CaptureSend
			State       *struct{} `json:"state,omitempty"`
			Outcomes    *struct{} `json:"outcomes,omitempty"`
			Cancels     *struct{} `json:"cancels,omitempty"`
			Fingerprint string    `json:"fingerprint"`
		}{CaptureSend: sv, Fingerprint: t.fp})
		if err != nil {
			return err
		}
		if err := t.record(ChangeRow{Entity: EntityCaptureSend, EntityID: sv.ID, Op: OpCreate, Value: value}); err != nil {
			return err
		}
		held := []string{u.sum}
		if c.Image.SHA256 != "" {
			held = append(held, c.Image.SHA256)
		}
		for _, sum := range held {
			if err := t.holdFile(EntityCaptureSend, sv.ID, id, sum, sv.ID); err != nil {
				return err
			}
		}
		if to.Kind != SendTicket {
			t.created(sv.ID, "")
			return nil
		}
		key, files, err := t.sendToTicket(c, to, u)
		if err != nil {
			return err
		}
		if err := t.outcome(sv.ID, SendSent, key, files, ""); err != nil {
			return err
		}
		t.created(sv.ID, key)
		return nil
	})
}

// sendToTicket makes a ticket send's rows on its ticket: the ticket (new,
// or the live one with the key), its attachments naming the screenshot's
// and the document's content, and the comment; it gives the ticket's key
// and the attachments' ids.
func (t *tx) sendToTicket(c Capture, to Destination, doc *received) (string, []string, error) {
	var tk Ticket
	var err error
	if to.Key != "" {
		tk, _, err = t.ticket(to.Key, false)
	} else {
		var id string
		if id, err = t.createTicket(NewTicket{Project: to.Project, Title: to.Title, Description: ticketText(c)}, nil, nil, ""); err == nil {
			tk, err = ticketByID(t.ctx, t.tx, id)
		}
	}
	if err != nil {
		return "", nil, err
	}
	type file struct {
		name, sum, contentType string
		size                   int64
	}
	files := []file{{fileName(c, "json"), doc.sum, "application/json", doc.size}}
	if c.Image.SHA256 != "" {
		files = append([]file{{fileName(c, "png"), c.Image.SHA256, "image/png", c.Image.Bytes}}, files...)
	}
	var n int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM attachments WHERE ticket_id = ? AND deleted_at = ''", tk.ID).Scan(&n); err != nil {
		return "", nil, err
	}
	if err := checkCount("attachments", n+len(files), MaxAttachments); err != nil {
		return "", nil, err
	}
	var ids []string
	for _, f := range files {
		a := Attachment{ID: newID(), TicketID: tk.ID, Name: f.name, Size: f.size, SHA256: f.sum, ContentType: f.contentType, CreatedAt: t.now, CreatedBy: t.op.author()}
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO attachments (id, ticket_id, name, size, sha256, content_type, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			a.ID, a.TicketID, a.Name, a.Size, a.SHA256, a.ContentType, stamp(a.CreatedAt), a.CreatedBy); err != nil {
			return "", nil, err
		}
		value, err := encodeValue(a)
		if err != nil {
			return "", nil, err
		}
		if err := t.record(ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID, Field: "attachments", Op: OpAdd, Value: value}); err != nil {
			return "", nil, err
		}
		ids = append(ids, a.ID)
	}
	body := fmt.Sprintf("Capture %s r%d of %s, attached as %s.", c.ID, c.Rev, c.URL, files[0].name)
	if c.Image.SHA256 != "" {
		body = fmt.Sprintf("Capture %s r%d of %s, attached as %s and %s.", c.ID, c.Rev, c.URL, files[0].name, files[1].name)
	} else {
		body += " No screenshot: " + c.Image.Why
	}
	if err := t.addComment(tk.Key, body); err != nil {
		return "", nil, err
	}
	return tk.Key, ids, nil
}

// outcome appends an outcome to a send: its row and one add row of the
// send's outcomes.
func (t *tx) outcome(sendID, kind, target string, files []string, why string) error {
	o := SendOutcome{ID: newID(), Kind: kind, Target: target, Files: files, Why: why, At: t.now, Actor: t.op.Actor, OpID: t.op.ID}
	encoded, err := encodeValue(o.Files)
	if err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO capture_send_outcomes (send_id, id, kind, target, files, why, at, actor, op_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		sendID, o.ID, o.Kind, o.Target, encoded, o.Why, stamp(o.At), o.Actor, o.OpID); err != nil {
		return err
	}
	value, err := encodeValue(o)
	if err != nil {
		return err
	}
	return t.record(ChangeRow{Entity: EntityCaptureSend, EntityID: sendID, Field: "outcomes", Op: OpAdd, Value: value})
}

// MarkSent records that the pending send with the id was taken by its
// target (capture_send.sent): br-<n> for Brakeman.
func (s *Store) MarkSent(ctx context.Context, op Op, id, target string) (Receipt, error) {
	return s.settleSend(ctx, op, IntentSendSent, captureArgs{ID: id, Kind: SendSent, Target: target})
}

// MarkRefused records that the pending send with the id was refused by
// its target, with the target's words (capture_send.refused).
func (s *Store) MarkRefused(ctx context.Context, op Op, id, why string) (Receipt, error) {
	return s.settleSend(ctx, op, IntentSendRefused, captureArgs{ID: id, Kind: SendRefused, Why: why})
}

// CancelSend records that the pending send with the id was cancelled
// before its target saw it (capture_send.cancelled).
func (s *Store) CancelSend(ctx context.Context, op Op, id string) (Receipt, error) {
	return s.settleSend(ctx, op, IntentSendCancelled, captureArgs{ID: id, Kind: SendCancelled})
}

// settleSend appends an outcome to a send under the terminal rule: only a
// pending send takes one, any other is refused stale_revision with its
// state named. not_found when there is no such send.
func (s *Store) settleSend(ctx context.Context, op Op, intent string, args captureArgs) (Receipt, error) {
	return s.Apply(ctx, op, intent, args, func(t *tx) error {
		sv, err := readSend(t.ctx, t.tx, args.ID, false)
		if errors.Is(err, ErrNotFound) {
			return refuse(RefusalNotFound, "no send %s", args.ID)
		} else if err != nil {
			return err
		}
		if sv.State != SendPending {
			return refuse(RefusalStaleRevision, "the send is %s already", sv.State)
		}
		switch args.Kind {
		case SendSent:
			err = checkField("send.target", "the target", args.Target)
		case SendRefused:
			err = checkField("send.why", "why", args.Why)
		}
		if err != nil {
			return err
		}
		t.created(args.ID, "")
		return t.outcome(args.ID, args.Kind, args.Target, nil, args.Why)
	})
}

// AskCancel records a person's cancel of a send (capture_send.cancel), its
// row and its receipt in one transaction, before anything acts on it: a
// pending or a sent send takes it (its request is cancelled at the
// target), as one already cancelled does; a ticket's send (made in one
// step, never pending) and a refused one are refused stale_revision. At
// most MaxSendCancels a send (bound).
func (s *Store) AskCancel(ctx context.Context, op Op, id string) (Receipt, error) {
	return s.Apply(ctx, op, IntentSendCancel, captureArgs{ID: id}, func(t *tx) error {
		sv, err := readSend(t.ctx, t.tx, id, false)
		switch {
		case errors.Is(err, ErrNotFound):
			return refuse(RefusalNotFound, "no send %s", id)
		case err != nil:
			return err
		case sv.Kind == SendTicket:
			return refuse(RefusalStaleRevision, "a send to a ticket is made in one step: nothing is pending")
		case sv.State == SendRefused:
			return refuse(RefusalStaleRevision, "the send was refused: %s", sv.Why)
		}
		if err := checkCount("cancels", len(sv.Cancels)+1, MaxSendCancels); err != nil {
			return err
		}
		res, err := t.tx.ExecContext(t.ctx, "INSERT INTO capture_send_cancels (send_id, op_id, at, actor) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING",
			id, t.op.ID, stamp(t.now), t.op.Actor)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return refuse(RefusalInvalid, "the operation id %s cancelled this send already", t.op.ID)
		}
		t.created(id, "")
		value, err := encodeValue(SendCancel{OpID: t.op.ID, At: t.now, Actor: t.op.Actor})
		if err != nil {
			return err
		}
		return t.record(ChangeRow{Entity: EntityCaptureSend, EntityID: id, Field: "cancels", Op: OpAdd, Value: value})
	})
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
	return out, err
}

// Capture reads the capture with the id, its annotations and its sends
// (without their requests), as a read gives it: a deleted capture's words
// are not among it (Capture). ErrNotFound when there is none.
func (s *Store) Capture(ctx context.Context, id string) (Capture, error) {
	var c Capture
	err := s.read(ctx, func(q queryer) error {
		var anns []Annotation
		var err error
		if c, anns, err = readCapture(ctx, q, id); err != nil {
			return err
		}
		if !c.DeletedAt.IsZero() {
			c = Capture{CaptureDocument: CaptureDocument{ID: c.ID, Rev: c.Rev}, CreatedAt: c.CreatedAt, DeletedAt: c.DeletedAt}
		}
		c.Annotations = anns
		c.Sends, err = readSends(ctx, q, "WHERE capture_id = ? ORDER BY created_at, id", id)
		return err
	})
	return c, err
}

// CaptureFilter picks a list's captures: an attempt's (Run and Task, and
// Attempt when given), or those a sent send of took to the ticket with the
// key; none, the most recent.
type CaptureFilter struct {
	Run     string
	Task    string
	Attempt int
	Ticket  string
}

// CapturesPage is a page of live captures, newest first, each with its
// sends and its first annotation alone, that annotation's note cut to 200
// characters; Next is the cursor of the next page, "" at the end.
type CapturesPage struct {
	Captures []Capture `json:"captures"`
	Next     string    `json:"next,omitempty"`
}

// Captures lists the live captures the filter picks, newest first, after
// the capture with the id after (a page's Next), at most limit, 1 to
// MaxCapturePage. A ticket's are its sends': a capture appears there only
// once a send of it is sent to it.
func (s *Store) Captures(ctx context.Context, f CaptureFilter, after string, limit int) (CapturesPage, error) {
	page := CapturesPage{Captures: []Capture{}}
	if limit < 1 || limit > MaxCapturePage {
		return page, fmt.Errorf("dispatch: a page of captures is 1 to %d", MaxCapturePage)
	}
	where, args := []string{"deleted_at = ''"}, []any{}
	switch {
	case f.Ticket != "":
		where = append(where, `id IN (SELECT s.capture_id FROM capture_sends s JOIN capture_send_outcomes o ON o.send_id = s.id AND `+earliestSent+`
			WHERE s.kind = 'ticket' AND o.target = ?)`)
		args = append(args, f.Ticket)
	case f.Run != "":
		where, args = append(where, "run_id = ?", "task_id = ?"), append(args, f.Run, f.Task)
		if f.Attempt > 0 {
			where, args = append(where, "attempt_n = ?"), append(args, f.Attempt)
		}
	}
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < captureSchema {
			return nil
		}
		if after != "" {
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
		for i := range page.Captures {
			c := &page.Captures[i]
			_, anns, err := readCapture(ctx, q, c.ID)
			if err != nil {
				return err
			}
			if len(anns) > 0 {
				c.Annotations = []Annotation{{K: 1, Kind: anns[0].Kind, Note: cutChars(anns[0].Note, 200)}}
			}
			if c.Sends, err = readSends(ctx, q, "WHERE capture_id = ? ORDER BY created_at, id", c.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return page, err
}

// cutChars is s cut to at most n characters.
func cutChars(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n])
	}
	return s
}

// sendColumns is a send's row as readSends reads it.
const sendColumns = "id, capture_id, rev, kind, asked, digest, request, created_at, created_by, actor, op_id"

// readSends reads the sends a clause picks (the rest of a query on
// capture_sends) with their outcomes, cancels and state, without their
// requests.
func readSends(ctx context.Context, q queryer, clause string, args ...any) ([]CaptureSend, error) {
	if schemaOf(q) < captureSchema {
		return []CaptureSend{}, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT "+sendColumns+" FROM capture_sends "+clause, args...)
	if err != nil {
		return nil, err
	}
	out := []CaptureSend{}
	for rows.Next() {
		var sv CaptureSend
		var asked, request, created string
		if err := rows.Scan(&sv.ID, &sv.Capture, &sv.Rev, &sv.Kind, &asked, &sv.Digest, &request, &created, &sv.CreatedBy, &sv.Actor, &sv.OpID); err != nil {
			rows.Close()
			return nil, err
		}
		sv.Asked, sv.CreatedAt = json.RawMessage(asked), unstamp(created)
		out = append(out, sv)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := sendLogs(ctx, q, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sendLogs reads a send's outcomes and cancels, and its state by them.
func sendLogs(ctx context.Context, q queryer, sv *CaptureSend) error {
	sv.Outcomes, sv.Cancels = []SendOutcome{}, []SendCancel{}
	rows, err := q.QueryContext(ctx, "SELECT id, kind, target, files, why, at, actor, op_id FROM capture_send_outcomes WHERE send_id = ? ORDER BY at, actor, op_id, id", sv.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var o SendOutcome
		var files, at string
		if err := rows.Scan(&o.ID, &o.Kind, &o.Target, &files, &o.Why, &at, &o.Actor, &o.OpID); err != nil {
			rows.Close()
			return err
		}
		o.At = unstamp(at)
		if err := json.Unmarshal([]byte(files), &o.Files); err != nil {
			rows.Close()
			return err
		}
		sv.Outcomes = append(sv.Outcomes, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = q.QueryContext(ctx, "SELECT op_id, at, actor FROM capture_send_cancels WHERE send_id = ? ORDER BY at, actor, op_id", sv.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c SendCancel
		var at string
		if err := rows.Scan(&c.OpID, &at, &c.Actor); err != nil {
			return err
		}
		c.At = unstamp(at)
		sv.Cancels = append(sv.Cancels, c)
	}
	var decided *SendOutcome
	if sv.State, decided = sendState(sv.Outcomes); decided != nil {
		sv.Target, sv.Files, sv.Why = decided.Target, decided.Files, decided.Why
	}
	return rows.Err()
}

// readSend reads the send with the id whole, its request with it when
// request is set: ErrNotFound when there is none.
func readSend(ctx context.Context, q queryer, id string, request bool) (CaptureSend, error) {
	sends, err := readSends(ctx, q, "WHERE id = ?", id)
	if err != nil {
		return CaptureSend{}, err
	}
	if len(sends) == 0 {
		return CaptureSend{}, ErrNotFound
	}
	sv := sends[0]
	if request {
		var r string
		if err := q.QueryRowContext(ctx, "SELECT request FROM capture_sends WHERE id = ?", id).Scan(&r); err != nil {
			return CaptureSend{}, err
		}
		if r != "" {
			sv.Request = json.RawMessage(r)
		}
	}
	return sv, nil
}

// CaptureSend reads the send with the id whole, its request among it, of
// a capture deleted or not: what was sent stays. ErrNotFound when there is
// none.
func (s *Store) CaptureSend(ctx context.Context, id string) (CaptureSend, error) {
	var sv CaptureSend
	err := s.read(ctx, func(q queryer) (err error) {
		sv, err = readSend(ctx, q, id, true)
		return err
	})
	return sv, err
}

// CaptureSends lists the sends of the kind whose state is sent to the
// target (a ticket's key, br-<n>), newest first, at most MaxCapturePage:
// a ticket's captures as they were sent, a deleted capture's among them.
func (s *Store) CaptureSends(ctx context.Context, kind, target string) ([]CaptureSend, error) {
	var out []CaptureSend
	err := s.read(ctx, func(q queryer) (err error) {
		out, err = readSends(ctx, q, `WHERE kind = ? AND id IN (SELECT o.send_id FROM capture_send_outcomes o WHERE o.kind = 'sent' AND o.target = ? AND `+
			earliestSent+`) ORDER BY created_at DESC, id DESC LIMIT ?`, kind, target, MaxCapturePage)
		return err
	})
	return out, err
}

// PendingSends lists every send with no outcome, oldest first, without
// their requests (CaptureSend reads one whole): what a settlement asks its
// target for again.
func (s *Store) PendingSends(ctx context.Context) ([]CaptureSend, error) {
	var out []CaptureSend
	err := s.read(ctx, func(q queryer) (err error) {
		out, err = readSends(ctx, q, "WHERE NOT EXISTS (SELECT 1 FROM capture_send_outcomes o WHERE o.send_id = capture_sends.id) ORDER BY created_at, id")
		return err
	})
	return out, err
}

// AskedCancels lists every send with a cancel that is not cancelled,
// oldest first: what a settlement finishes (a pending one cancelled, a
// sent one's request cancelled at its target), whether a cancel is done
// at the target being the target's to say.
func (s *Store) AskedCancels(ctx context.Context) ([]CaptureSend, error) {
	var out []CaptureSend
	err := s.read(ctx, func(q queryer) error {
		sends, err := readSends(ctx, q, "WHERE id IN (SELECT send_id FROM capture_send_cancels) ORDER BY created_at, id")
		for _, sv := range sends {
			if sv.State != SendCancelled {
				out = append(out, sv)
			}
		}
		return err
	})
	return out, err
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
	return c, f, err
}

// OpenSendFile opens the content with the hash that the send with the id
// holds (its frozen document, the screenshot it carried), and nothing
// else: ErrNotFound unless that send's file row names it, whatever the
// attachments' directory holds. The caller closes the file.
func (s *Store) OpenSendFile(ctx context.Context, id, sum string) (*os.File, error) {
	return s.openHeld(ctx, "", sum, id)
}

// openHeld opens a content file a capture_files row of the holder names.
func (s *Store) openHeld(ctx context.Context, captureID, sum, holder string) (*os.File, error) {
	if !hashPattern.MatchString(sum) || (!ValidID(holder) && holder != holderCapture) {
		return nil, ErrNotFound
	}
	var held bool
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < captureSchema {
			return nil
		}
		return q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM capture_files WHERE sha256 = ? AND holder = ? AND (? = '' OR capture_id = ?))",
			sum, holder, captureID, captureID).Scan(&held)
	})
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, ErrNotFound
	}
	f, err := os.Open(contentPath(filepath.Join(s.dir, attachmentsDir), sum))
	if err != nil {
		return nil, fmt.Errorf("dispatch: the content %s held by %s: %w", sum, holder, err)
	}
	return f, nil
}
