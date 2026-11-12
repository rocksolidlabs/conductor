package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// A capture's sends (Program item PV1, docs/plans/browser-evidence.md,
// "Operations"): a send takes a capture, at the revision the person sees,
// to a ticket or to Brakeman.
//
// What a send carries: the capture's document at that revision
// (CaptureDocument, conductor.capture/1), frozen as a content file named by
// its SHA-256 (the send's row keeps the digest, never the document), and
// the screenshot's content file when there is one; the send holds both
// (capture_files, holder its id) while something can still use them: a
// send of a live capture, and a sent one for as long as the store keeps
// the send (nothing removes a send; what a sent send of a deleted capture
// keeps is the settlement's to say, #794); a send of a deleted capture
// that ended refused or cancelled lets them go (releaseEnded). A
// send to a ticket writes, in its one transaction, the ticket when it is
// new (its description ticketText), an attachment row naming each of the
// two files (no file copied), a comment in the store's own words
// (sendComment), and its sent outcome. A send to Brakeman carries the
// request the API made of it (SendFacts.Request, a JSON object of at most
// MaxSendRequest bytes), kept whole so the desk can be asked again, and is
// pending until it is settled. A send never carries an edit made after its
// revision, the screenshot's bytes inside the document, or a request
// beside a ticket; the tree's secret never reaches a capture (the API
// scrubs it first).
//
// Who may make one: any operation's actor, as for every operation of the
// store, which knows no roles (the API's route decides who sends).
//
// A send's state is one projection of its outcomes, sendState, the same in
// every replica and every order of arrival; every read that picks sends by
// their state does it by the same projection in SQL (decision). Locally
// the terminal rule holds: an outcome is appended to a pending send alone.
// A person's cancel (AskCancel) is an intent of its own, written before
// anything acts on it and never an outcome: a pending send is cancelled
// (CancelSend) only once a cancel was asked, and one the desk admitted
// first is settled sent with its cancel still asked (AskedCancels), for the
// settlement to close its request at the desk. A capture's delete leaves
// its sends whole, each readable as a row: what was sent stays. Locally a
// send that ended is ended: a sent outcome a replica brings later for one
// whose files went makes it sent, and this store holds no file of it.

// A send's kind and its state. SendReview and SendDecision are reserved:
// the store refuses them until the items that own reviews and decisions
// lift it (the plan's PR-10 and PR-11).
const (
	SendTicket    = "ticket"
	SendBrakeman  = "brakeman"
	SendReview    = "review"
	SendDecision  = "decision"
	SendPending   = "pending"
	SendSent      = "sent"
	SendRefused   = "refused"
	SendCancelled = "cancelled"
)

// The intents of a send's outcomes and of a cancel, beside
// IntentCaptureSend.
const (
	IntentSendSent      = "capture_send.sent"
	IntentSendRefused   = "capture_send.refused"
	IntentSendCancelled = "capture_send.cancelled"
	IntentSendCancel    = "capture_send.cancel"
)

// brakemanTarget is what a send to Brakeman is sent as: its request's id
// at the desk, br-<n> of at most 18 digits. It is a copy of Brakeman's own
// form (requestIDPattern, internal/brakeman/refs.go), since this package
// imports none of the module's; TestBrakemansRequestIDIsTheStores holds
// the copy to that source.
var brakemanTarget = regexp.MustCompile(`^br-[1-9][0-9]{0,17}$`)

// Destination is where a send goes as the person named it, kept as its
// asked: a new ticket (Project, a project's key, and Title), an existing
// one (Key), or Brakeman (Kind alone).
type Destination struct {
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	Title   string `json:"title,omitempty"`
	Key     string `json:"key,omitempty"`
}

// SendFacts is what the API made of a send: for Brakeman the request as
// the desk is asked it (a JSON object of at most MaxSendRequest bytes),
// which a settlement asks again whole.
type SendFacts struct {
	Request json.RawMessage `json:"request,omitempty"`
}

// SendInput is a send's input, written once: the capture and the revision
// it sent, where to as asked, the document's digest, the request (among
// the reads of sends, read whole by CaptureSend alone; the send's create
// row in the change log holds it too), and who made it when.
type SendInput struct {
	ID        string          `json:"id"`
	Capture   string          `json:"capture"`
	Rev       int64           `json:"rev"`
	Kind      string          `json:"kind"`
	Asked     json.RawMessage `json:"asked"`
	Digest    string          `json:"digest"`
	Request   json.RawMessage `json:"request,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	CreatedBy string          `json:"createdBy,omitempty"`
	Actor     string          `json:"actor"`
	OpID      string          `json:"opId"`
}

// SendOutcome is what became of a send: sent (its target and the
// attachments it added), refused (why) or cancelled.
type SendOutcome struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Target string    `json:"target,omitempty"`
	Files  []string  `json:"files,omitempty"`
	Why    string    `json:"why,omitempty"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	OpID   string    `json:"opId"`
}

// SendCancel is a person's cancel of a send, kept before anything acts on
// it.
type SendCancel struct {
	OpID  string    `json:"opId"`
	At    time.Time `json:"at"`
	Actor string    `json:"actor"`
}

// CaptureSend is a send as a read gives it: its input, its state by
// sendState with the deciding outcome's target, files and why, every
// outcome and every cancel.
type CaptureSend struct {
	SendInput
	State    string        `json:"state"`
	Target   string        `json:"target,omitempty"`
	Files    []string      `json:"files,omitempty"`
	Why      string        `json:"why,omitempty"`
	Outcomes []SendOutcome `json:"outcomes"`
	Cancels  []SendCancel  `json:"cancels"`
}

// SendsPage is a page of sends without their requests; Next is the cursor
// of the next page, "" at the end.
type SendsPage struct {
	Sends []CaptureSend `json:"sends"`
	Next  string        `json:"next,omitempty"`
}

// sendState is a send's state, the one projection every read makes of its
// outcomes: none is pending; any sent makes it sent; else it is the
// earliest refusal or cancellation. The outcome that decides is a sent one
// before any other, then the earliest by (at, actor, op_id, id), so the
// same outcomes give the same state and the same target in any order of
// arrival. It returns the deciding outcome, nil for pending.
func sendState(outcomes []SendOutcome) (string, *SendOutcome) {
	var decided *SendOutcome
	for i := range outcomes {
		o := &outcomes[i]
		if o.Kind != SendSent && o.Kind != SendRefused && o.Kind != SendCancelled {
			continue
		}
		if decided == nil || decides(o, decided) {
			decided = o
		}
	}
	if decided == nil {
		return SendPending, nil
	}
	return decided.Kind, decided
}

// decides is whether a decides a send's state before b (sendState).
func decides(a, b *SendOutcome) bool {
	switch {
	case (a.Kind == SendSent) != (b.Kind == SendSent):
		return a.Kind == SendSent
	case !a.At.Equal(b.At):
		return a.At.Before(b.At)
	case a.Actor != b.Actor:
		return a.Actor < b.Actor
	case a.OpID != b.OpID:
		return a.OpID < b.OpID
	}
	return a.ID < b.ID
}

// decision is sendState in SQL for the send whose id the column send
// names: cols of the outcome that decides its state, in sendState's order
// (stamps order as text as times do), no row for a pending send.
func decision(send, cols string) string {
	return `(SELECT ` + cols + ` FROM capture_send_outcomes e WHERE e.send_id = ` + send + ` AND e.kind IN ('sent', 'refused', 'cancelled')
		ORDER BY e.kind = 'sent' DESC, e.at, e.actor, e.op_id, e.id LIMIT 1)`
}

// sendStateSQL is the SQL of the send's state (sendState).
func sendStateSQL(send string) string { return `COALESCE(` + decision(send, "e.kind") + `, 'pending')` }

// sentTo is the SQL condition that the send is sent to target (a column or
// a parameter), by sendState: its deciding outcome a sent one naming it.
func sentTo(send, target string) string {
	return `('sent', ` + target + `) = ` + decision(send, "e.kind, e.target")
}

// SendCapture sends the live capture with the id at rev, its revision now,
// to the destination (capture.send), received the digest of the client's
// request: a person sends what they see, so another revision is refused
// stale_revision, and a capture deleted or never made not_found. The
// document at rev is built from the rows and published as a content file
// through the upload path; one transaction then reads the capture again
// (stale_revision when it moved meanwhile), takes the upload's lease and
// writes the send's input, the rows holding the document and the
// screenshot, and for a ticket the ticket (new, its description
// ticketText; or the live one with the key), the attachment rows naming
// the screenshot's and the document's content (the document alone with no
// screenshot), the comment, and the sent outcome, the ticket's rev bumped
// once; for Brakeman it leaves the send pending with its request. At most
// MaxCaptureSends a capture and MaxAttachments a ticket (bound). review
// and decision are refused invalid until their items lift it. The
// receipt's id is the send's, its key the ticket's; the operation's
// expected revision is none (the capture's is rev).
func (s *Store) SendCapture(ctx context.Context, op Op, digest, id string, rev int64, to Destination, f SendFacts) (Receipt, error) {
	doc := bytes.NewReader(nil)
	var request []byte
	pre := func() error {
		c := &checker{}
		c.must(op.ExpectedRev == 0, RefusalInvalid, "a send names the capture's revision it sends, and the operation none")
		switch to.Kind {
		case SendTicket:
			_, _, isKey := ParseKey(to.Key)
			c.must((to.Key == "") != (to.Project == "" && to.Title == ""), RefusalInvalid, "a send to a ticket names a ticket's key, or a project and a title")
			if to.Key != "" {
				c.must(isKey, RefusalInvalid, "the ticket %q is no ticket's key", to.Key)
			} else {
				c.must(ValidKey(to.Project), RefusalInvalid, "the project %q is no project's key", to.Project)
				c.field("ticket.title", "the new ticket's title", to.Title)
			}
			c.must(len(f.Request) == 0, RefusalInvalid, "a send to a ticket carries no request")
		case SendBrakeman:
			c.must(to.Key == "" && to.Project == "" && to.Title == "", RefusalInvalid, "a send to Brakeman names no ticket")
			c.field("send.request", "the request", string(f.Request))
			c.must(bytes.HasPrefix(bytes.TrimLeft(f.Request, " \t\r\n"), []byte("{")), RefusalInvalid, "the request is not a JSON object")
		case SendReview, SendDecision:
			c.must(false, RefusalInvalid, "a capture is sent to a ticket or to Brakeman; a send to a %s comes with its item", to.Kind)
		default:
			c.must(false, RefusalInvalid, "a capture is sent to a ticket or to Brakeman, not to %q", to.Kind)
		}
		if c.err != nil {
			return c.err
		}
		if len(f.Request) > 0 {
			var b bytes.Buffer
			if err := json.Compact(&b, f.Request); err != nil {
				return refuse(RefusalInvalid, "the request is not JSON")
			}
			request = b.Bytes()
		}
		body, err := s.Document(ctx, id, rev)
		switch {
		case errors.Is(err, ErrNotFound):
			return refuse(RefusalNotFound, "no live capture %s", id)
		case errors.Is(err, ErrStaleCapture):
			return refuse(RefusalStaleRevision, "the capture is not at r%d: send what you see", rev)
		case err != nil:
			return err
		}
		doc.Reset(body)
		return nil
	}
	return s.withUpload(ctx, op, IntentCaptureSend, captureArgs{ID: id, Received: digest}, pre, doc, MaxCaptureDocument, nil, func(t *tx, u *received) error {
		c, err := t.liveCapture(id)
		if err != nil {
			return err
		}
		body, err := encodeValue(documentOf(c))
		if err != nil {
			return err
		}
		if c.Rev != rev || sha256Hex(body) != u.sum {
			return refuse(RefusalStaleRevision, "the capture changed while its document was made, to r%d: send what you see", c.Rev)
		}
		var sends int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM capture_sends WHERE capture_id = ?", id).Scan(&sends); err != nil {
			return err
		}
		if err := checkCount("sends of a capture", sends+1, MaxCaptureSends); err != nil {
			return err
		}
		asked, err := encodeValue(to)
		if err != nil {
			return err
		}
		in := SendInput{ID: newID(), Capture: id, Rev: rev, Kind: to.Kind, Asked: json.RawMessage(asked), Digest: u.sum, Request: request,
			CreatedAt: t.now, CreatedBy: t.op.author(), Actor: t.op.Actor, OpID: t.op.ID}
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO capture_sends (id, capture_id, rev, kind, asked, digest, request, created_at, created_by,
			actor, op_id, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, in.ID, id, rev, in.Kind, asked, in.Digest, string(request),
			stamp(t.now), in.CreatedBy, in.Actor, in.OpID, t.fp); err != nil {
			return err
		}
		// The send's create row is the operation's first: its input with the
		// request (at most MaxSendRequest) and the fingerprint (prunedAnswer).
		value, err := encodeValue(struct {
			SendInput
			Fingerprint string `json:"fingerprint"`
		}{in, t.fp})
		if err != nil {
			return err
		}
		if err := t.record(ChangeRow{Entity: EntityCaptureSend, EntityID: in.ID, Op: OpCreate, Value: value}); err != nil {
			return err
		}
		held := []string{u.sum}
		if c.Image.SHA256 != "" {
			held = append(held, c.Image.SHA256)
		}
		for _, sum := range held {
			if err := t.holdFile(EntityCaptureSend, in.ID, id, sum, in.ID); err != nil {
				return err
			}
		}
		key := ""
		if to.Kind == SendTicket {
			var files []string
			if key, files, err = t.sendToTicket(c, to, u); err != nil {
				return err
			}
			if err := t.outcome(in.ID, SendSent, key, files, ""); err != nil {
				return err
			}
		}
		t.created(in.ID, key)
		return nil
	})
}

// sendToTicket makes a ticket send's rows on its ticket: the ticket (new,
// or the live one with the key), an attachment row naming the screenshot's
// content and one the document's (no file copied), and the comment; it
// gives the ticket's key and the attachments' ids.
func (t *tx) sendToTicket(c Capture, to Destination, doc *received) (string, []string, error) {
	key := to.Key
	if key == "" {
		id, err := t.createTicket(NewTicket{Project: to.Project, Title: to.Title, Description: ticketText(c)}, nil, nil, "")
		if err != nil {
			return "", nil, err
		}
		made, err := ticketByID(t.ctx, t.tx, id)
		if err != nil {
			return "", nil, err
		}
		key = made.Key
	}
	tk, _, err := t.ticket(key, false)
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
	ids, names := []string{}, []string{}
	for _, f := range files {
		a := Attachment{ID: newID(), TicketID: tk.ID, Name: f.name, Size: f.size, SHA256: f.sum, ContentType: f.contentType, CreatedAt: t.now,
			CreatedBy: t.op.author()}
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO attachments (id, ticket_id, name, size, sha256, content_type, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			a.ID, a.TicketID, a.Name, a.Size, a.SHA256, a.ContentType, stamp(a.CreatedAt), a.CreatedBy); err != nil {
			return "", nil, err
		}
		value, err := encodeValue(a)
		if err != nil {
			return "", nil, err
		}
		if err := t.record(ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID, Field: "attachments", Op: OpAdd,
			Value: value}); err != nil {
			return "", nil, err
		}
		ids, names = append(ids, a.ID), append(names, a.Name)
	}
	if err := t.addComment(tk.Key, sendComment(c, names)); err != nil {
		return "", nil, err
	}
	return tk.Key, ids, nil
}

// sendComment is the comment a send to a ticket writes, in the store's own
// words alone: the capture's id and revision, the attachments' names
// (fileName: the id and the revision) and whether there is a screenshot.
// What the page gave and why there is no screenshot are the description's
// (in its blocks) and the attached document's.
func sendComment(c Capture, names []string) string {
	for i, n := range names {
		names[i] = "`" + n + "`"
	}
	body := fmt.Sprintf("Capture %s r%d, attached as %s.", c.ID, c.Rev, strings.Join(names, " and "))
	if c.Image.How == ImageNone {
		body += " It has no screenshot."
	}
	return body
}

// outcome appends an outcome to a send: its row and one add row of the
// send's outcomes, which keeps the operation's fingerprint.
func (t *tx) outcome(sendID, kind, target string, files []string, why string) error {
	if files == nil {
		files = []string{}
	}
	o := SendOutcome{ID: newID(), Kind: kind, Target: target, Files: files, Why: why, At: t.now, Actor: t.op.Actor, OpID: t.op.ID}
	encoded, err := encodeValue(files)
	if err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO capture_send_outcomes (send_id, id, kind, target, files, why, at, actor, op_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		sendID, o.ID, o.Kind, o.Target, encoded, o.Why, stamp(o.At), o.Actor, o.OpID); err != nil {
		return err
	}
	value, err := encodeValue(struct {
		SendOutcome
		Fingerprint string `json:"fingerprint"`
	}{o, t.fp})
	if err != nil {
		return err
	}
	return t.record(ChangeRow{Entity: EntityCaptureSend, EntityID: sendID, Field: "outcomes", Op: OpAdd, Value: value})
}

// MarkSent records that the pending send to Brakeman with the id was taken
// by the desk as the request target, br-<n> (capture_send.sent).
func (s *Store) MarkSent(ctx context.Context, op Op, id, target string) (Receipt, error) {
	return s.settleSend(ctx, op, IntentSendSent, SendSent, captureArgs{ID: id, Target: target})
}

// MarkRefused records that the pending send with the id was refused by
// its target, with the target's words (capture_send.refused): at most
// MaxSendWhy characters as given, then folded (addressFree) and kept
// within MaxSendWhy, cut at a character's end (the cut carries no mark),
// so words within their bound are never refused for what the fold makes
// of them.
func (s *Store) MarkRefused(ctx context.Context, op Op, id, why string) (Receipt, error) {
	return s.settleSend(ctx, op, IntentSendRefused, SendRefused, captureArgs{ID: id, Why: why})
}

// CancelSend records that the pending send with the id was cancelled
// before its target took it (capture_send.cancelled): only once a person's
// cancel of it was asked (AskCancel), invalid before.
func (s *Store) CancelSend(ctx context.Context, op Op, id string) (Receipt, error) {
	return s.settleSend(ctx, op, IntentSendCancelled, SendCancelled, captureArgs{ID: id})
}

// settleSend appends an outcome of the kind to a send under the terminal
// rule: a pending send alone takes one, any other is refused
// stale_revision with its state named; not_found for no such send. A send
// of a deleted capture that ends refused or cancelled lets its files go in
// the same transaction (releaseEnded). The receipt's id is the send's.
func (s *Store) settleSend(ctx context.Context, op Op, intent, kind string, args captureArgs) (Receipt, error) {
	var released []string
	rc, err := s.Apply(ctx, op, intent, args, func(t *tx) error {
		released = nil
		sv, err := readSend(t.ctx, t.tx, args.ID, false)
		switch {
		case errors.Is(err, ErrNotFound):
			return refuse(RefusalNotFound, "no send %s", args.ID)
		case err != nil:
			return err
		case sv.State != SendPending:
			return refuse(RefusalStaleRevision, "the send is %s already: it takes no other outcome", sv.State)
		}
		why := ""
		switch kind {
		case SendSent:
			if err := checkField("send.target", "the target", args.Target); err != nil {
				return err
			}
			if sv.Kind != SendBrakeman || !brakemanTarget.MatchString(args.Target) {
				return refuse(RefusalInvalid, "a send to Brakeman is sent as its request's id, br-<n>")
			}
		case SendRefused:
			if err := checkField("send.why", "why", args.Why); err != nil {
				return err
			}
			why = cutChars(addressFree(args.Why), MaxSendWhy)
		case SendCancelled:
			if len(sv.Cancels) == 0 {
				return refuse(RefusalInvalid, "no cancel was asked of the send %s", args.ID)
			}
		}
		t.created(args.ID, "")
		if err := t.outcome(args.ID, kind, args.Target, nil, why); err != nil || kind == SendSent {
			return err
		}
		var gone bool
		if err := t.tx.QueryRowContext(t.ctx, "SELECT deleted_at != '' FROM captures WHERE id = ?", sv.Capture).Scan(&gone); err != nil || !gone {
			return err
		}
		released, err = t.releaseEnded(sv.Capture, args.ID)
		return err
	})
	s.collectAll(ctx, rc, err, released)
	return rc, err
}

// collectAll collects the files an operation done released, once it is
// committed (collectOne keeps any something else still names).
func (s *Store) collectAll(ctx context.Context, rc Receipt, err error, released []string) {
	if err == nil && rc.Status == StatusDone && !rc.Repeat {
		for _, sum := range released {
			_ = s.collectOne(ctx, sum)
		}
	}
}

// releaseEnded lets go the files held by the sends of the capture with the
// id that ended without being sent (refused or cancelled), the send with
// the id send alone when it is not "": each file row deleted and its add
// row removed in the change log, in the caller's transaction (a delete's,
// or the outcome's that ended the send once its capture was deleted). A
// sent send, to a ticket or one Brakeman took, keeps what it holds for as
// long as the store keeps it. It gives the hashes let go, for the caller
// to collect once committed.
func (t *tx) releaseEnded(captureID, send string) ([]string, error) {
	where, args := "s.capture_id = ? AND "+sendStateSQL("s.id")+" IN ('refused', 'cancelled')", []any{captureID}
	if send != "" {
		where, args = where+" AND s.id = ?", append(args, send)
	}
	held := [][2]string{}
	err := func() error {
		rows, err := t.tx.QueryContext(t.ctx, "SELECT f.holder, f.sha256 FROM capture_sends s JOIN capture_files f ON f.capture_id = s.capture_id AND f.holder = s.id WHERE "+where+
			" ORDER BY f.holder, f.sha256", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h [2]string
			if err := rows.Scan(&h[0], &h[1]); err != nil {
				return err
			}
			held = append(held, h)
		}
		return rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	sums := []string{}
	for _, h := range held {
		value := fmt.Sprintf(`{"sha256":%q,"holder":%q}`, h[1], h[0])
		var tag Tag
		if err := t.tx.QueryRowContext(t.ctx, "SELECT actor, op_id, n FROM changes WHERE entity = ? AND entity_id = ? AND field = 'files' AND op = ? AND value = ?",
			EntityCaptureSend, h[0], OpAdd, value).Scan(&tag.Actor, &tag.OpID, &tag.N); err != nil && !isNoRows(err) {
			return nil, err
		}
		if _, err := t.tx.ExecContext(t.ctx, "DELETE FROM capture_files WHERE capture_id = ? AND sha256 = ? AND holder = ?", captureID, h[1], h[0]); err != nil {
			return nil, err
		}
		if err := t.recordRemove(ChangeRow{Entity: EntityCaptureSend, EntityID: h[0], Field: "files", Value: value}, []Tag{tag}); err != nil {
			return nil, err
		}
		sums = append(sums, h[1])
	}
	return sums, nil
}

// AskCancel records a person's cancel of the send with the id
// (capture_send.cancel), its row and its receipt in one transaction,
// before anything acts on it: a pending and a sent send (its request is
// closed at the desk) take it; a send to a ticket (made in its one step),
// a refused one and a cancelled one are refused stale_revision, nothing of
// them under way, writing nothing, so a resend is refused the same by the
// send's state while that state is the same, whatever was pruned. At most
// MaxSendCancels a send (bound), counted after the state and before the
// id: at MaxSendCancels cancels every further cancel is refused bound, one
// under an id another cancel of the send took among them. Below the bound
// a cancel is known on its send by its operation's id: an id another
// cancel of the send took is refused op_conflict. The receipt's id is the
// send's.
func (s *Store) AskCancel(ctx context.Context, op Op, id string) (Receipt, error) {
	return s.Apply(ctx, op, IntentSendCancel, captureArgs{ID: id}, func(t *tx) error {
		sv, err := readSend(t.ctx, t.tx, id, false)
		switch {
		case errors.Is(err, ErrNotFound):
			return refuse(RefusalNotFound, "no send %s", id)
		case err != nil:
			return err
		case sv.Kind == SendTicket:
			return refuse(RefusalStaleRevision, "a send to a ticket is made in its one step: nothing of it is under way")
		case sv.State == SendRefused, sv.State == SendCancelled:
			return refuse(RefusalStaleRevision, "the send was %s: nothing of it is under way", sv.State)
		}
		if err := checkCount("cancels of a send", len(sv.Cancels)+1, MaxSendCancels); err != nil {
			return err
		}
		res, err := t.tx.ExecContext(t.ctx, "INSERT INTO capture_send_cancels (send_id, op_id, at, actor) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING",
			id, t.op.ID, stamp(t.now), t.op.Actor)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return refuse(RefusalOpConflict, "the operation id %s is taken on this send by another cancel: ask again under another id", t.op.ID)
		}
		t.created(id, "")
		value, err := encodeValue(struct {
			SendCancel
			Fingerprint string `json:"fingerprint"`
		}{SendCancel{OpID: t.op.ID, At: t.now, Actor: t.op.Actor}, t.fp})
		if err != nil {
			return err
		}
		return t.record(ChangeRow{Entity: EntityCaptureSend, EntityID: id, Field: "cancels", Op: OpAdd, Value: value})
	})
}

// sendColumns is a send's row as readSends reads it, its request left out.
const sendColumns = "s.id, s.capture_id, s.rev, s.kind, s.asked, s.digest, s.created_at, s.created_by, s.actor, s.op_id"

// readSends reads the sends the clause picks (the rest of a query on
// capture_sends s) with their outcomes, cancels and state, without their
// requests: none on a database older than captureSchema.
func readSends(ctx context.Context, q queryer, clause string, args ...any) ([]CaptureSend, error) {
	if schemaOf(q) < captureSchema {
		return []CaptureSend{}, nil
	}
	out, err := func() ([]CaptureSend, error) {
		rows, err := q.QueryContext(ctx, "SELECT "+sendColumns+" FROM capture_sends s "+clause, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []CaptureSend{}
		for rows.Next() {
			var sv CaptureSend
			var asked, created string
			if err := rows.Scan(&sv.ID, &sv.Capture, &sv.Rev, &sv.Kind, &asked, &sv.Digest, &created, &sv.CreatedBy, &sv.Actor, &sv.OpID); err != nil {
				return nil, err
			}
			sv.Asked, sv.CreatedAt = json.RawMessage(asked), unstamp(created)
			out = append(out, sv)
		}
		return out, rows.Err()
	}()
	if err != nil {
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
	defer rows.Close()
	for rows.Next() {
		var o SendOutcome
		var files, at string
		if err := rows.Scan(&o.ID, &o.Kind, &o.Target, &files, &o.Why, &at, &o.Actor, &o.OpID); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(files), &o.Files); err != nil {
			return fmt.Errorf("dispatch: send %s's outcome %s: %w", sv.ID, o.ID, err)
		}
		o.At = unstamp(at)
		sv.Outcomes = append(sv.Outcomes, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cancels, err := q.QueryContext(ctx, "SELECT op_id, at, actor FROM capture_send_cancels WHERE send_id = ? ORDER BY at, actor, op_id", sv.ID)
	if err != nil {
		return err
	}
	defer cancels.Close()
	for cancels.Next() {
		var c SendCancel
		var at string
		if err := cancels.Scan(&c.OpID, &at, &c.Actor); err != nil {
			return err
		}
		c.At = unstamp(at)
		sv.Cancels = append(sv.Cancels, c)
	}
	var decided *SendOutcome
	if sv.State, decided = sendState(sv.Outcomes); decided != nil {
		sv.Target, sv.Files, sv.Why = decided.Target, decided.Files, decided.Why
	}
	return cancels.Err()
}

// readSend reads the send with the id whole, its request with it when
// request is set: ErrNotFound when there is none.
func readSend(ctx context.Context, q queryer, id string, request bool) (CaptureSend, error) {
	sends, err := readSends(ctx, q, "WHERE s.id = ?", id)
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

// sendKey is the key of the ticket the send to a ticket with the id went
// to, by sendState; "" for another send.
func sendKey(ctx context.Context, q queryer, id string) (string, error) {
	var key string
	err := q.QueryRowContext(ctx, "SELECT COALESCE("+decision("s.id", "e.target")+", '') FROM capture_sends s WHERE s.id = ? AND s.kind = 'ticket'", id).Scan(&key)
	if isNoRows(err) {
		return "", nil
	}
	return key, err
}

// CaptureSend reads the send with the id whole, its request among it, of a
// capture deleted or not: what was sent stays. ErrNotFound when there is
// none.
func (s *Store) CaptureSend(ctx context.Context, id string) (CaptureSend, error) {
	var sv CaptureSend
	err := s.read(ctx, func(q queryer) (err error) {
		sv, err = readSend(ctx, q, id, true)
		return err
	})
	return answer(sv, err)
}

// sendsPage is a page of the sends where picks (a condition on
// capture_sends s), oldest first or newest, after the send with the id
// after (a page's Next), at most limit, 1 to MaxCapturePage: ErrBadQuery
// for a limit out of range or an after that names no send.
func (s *Store) sendsPage(ctx context.Context, where string, args []any, newest bool, after string, limit int) (SendsPage, error) {
	if limit < 1 || limit > MaxCapturePage {
		return SendsPage{}, badQuery("a page of sends is 1 to %d, not %d", MaxCapturePage, limit)
	}
	order, cmp := "ASC", ">"
	if newest {
		order, cmp = "DESC", "<"
	}
	page := SendsPage{Sends: []CaptureSend{}}
	err := s.read(ctx, func(q queryer) error {
		clause := "WHERE " + where
		all := append([]any{}, args...)
		if after != "" {
			var there bool
			if schemaOf(q) >= captureSchema {
				if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM capture_sends WHERE id = ?)", after).Scan(&there); err != nil {
					return err
				}
			}
			if !there {
				return badQuery("the page's cursor names no send")
			}
			clause += " AND (s.created_at, s.id) " + cmp + " (SELECT created_at, id FROM capture_sends WHERE id = ?)"
			all = append(all, after)
		}
		sends, err := readSends(ctx, q, clause+" ORDER BY s.created_at "+order+", s.id "+order+" LIMIT ?", append(all, limit+1)...)
		if err != nil {
			return err
		}
		if len(sends) > limit {
			sends, page.Next = sends[:limit], sends[limit-1].ID
		}
		page.Sends = sends
		return nil
	})
	return answer(page, err)
}

// CaptureSends lists the sends of the kind sent to the target (a ticket's
// key, br-<n>) by sendState, newest first, by pages (sendsPage): a
// ticket's captures as they were sent, a deleted capture's among them.
// Its answer is a page; its work is the target's sends (#794). ErrBadQuery for another kind, a target of no such form, or a bad
// page.
func (s *Store) CaptureSends(ctx context.Context, kind, target, after string, limit int) (SendsPage, error) {
	_, _, isKey := ParseKey(target)
	if !(kind == SendTicket && isKey) && !(kind == SendBrakeman && brakemanTarget.MatchString(target)) {
		return SendsPage{}, badQuery("the sends named are a ticket's, by its key, or Brakeman's, by br-<n>")
	}
	return s.sendsPage(ctx, `s.kind = ? AND s.id IN (SELECT send_id FROM capture_send_outcomes WHERE kind = 'sent' AND target = ?) AND `+sentTo("s.id", "?"),
		[]any{kind, target, target}, true, after, limit)
}

// PendingSends lists every pending send by sendState, oldest first, by
// pages (sendsPage), without their requests (CaptureSend reads one whole):
// what a settlement asks its target for again. Its answer is a page; the
// work of a page is not bounded by it, since version 5 keeps nothing a read
// finds a send by its state with: it reads the sends the store keeps. The
// row that first calls it (issue #794) brings what bounds that.
func (s *Store) PendingSends(ctx context.Context, after string, limit int) (SendsPage, error) {
	return s.sendsPage(ctx, sendStateSQL("s.id")+" = 'pending'", nil, false, after, limit)
}

// AskedCancels lists every send with a cancel asked whose state is not
// cancelled, oldest first, by pages (sendsPage): what a settlement
// finishes (a pending one cancelled, a sent one's request closed at the
// desk), whether a cancel is done at the desk being the desk's to say. A
// send sent and a send refused with its cancel asked stay listed, since
// nothing records its cancel closed; its answer is a page and its work, as
// PendingSends', is not bounded by it. Issue #794 brings both.
func (s *Store) AskedCancels(ctx context.Context, after string, limit int) (SendsPage, error) {
	return s.sendsPage(ctx, "s.id IN (SELECT send_id FROM capture_send_cancels) AND "+sendStateSQL("s.id")+" != 'cancelled'", nil, false, after, limit)
}

// OpenSendFile opens the content with the hash that the send with the id
// holds (the document it froze, the screenshot it carried), and nothing
// else: ErrNotFound unless that send's file row names it, whatever the
// attachments' directory holds, of a capture deleted or not. The caller
// closes the file.
func (s *Store) OpenSendFile(ctx context.Context, id, sum string) (*os.File, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	var capture string
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < captureSchema {
			return ErrNotFound
		}
		err := q.QueryRowContext(ctx, "SELECT capture_id FROM capture_sends WHERE id = ?", id).Scan(&capture)
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return answer(s.openHeld(ctx, capture, sum, id))
}

// fileName is the name a send's file takes on a ticket.
func fileName(c Capture, ext string) string {
	return fmt.Sprintf("capture-%s-r%d.%s", c.ID, c.Rev, ext)
}

// pageAddress is a page's address as a ticket's words name it, written
// from its parts and nothing else: its scheme, its host with its port and
// its path (in the escaped form the URL parser gives: a path whose given
// form is not a valid encoding is written re-encoded), never its user,
// query or fragment, an address on any host shown as what it is (the
// document attached keeps it whole).
func pageAddress(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return "the page"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// ticketFence opens and closes a block of a capture's values in a ticket's
// description. Every line between two of them begins with the store's
// label (ticketWords.block), so no line of a block is a fence, and nothing
// a capture holds can end the block it stands in.
const ticketFence = "```"

// ticketWords is a ticket's description made from a capture, line by
// line: the store's own words, blocks of the capture's values, and the
// person's notes, each quoted (ticketWords.quote). Only the store's own
// fence lines are a fence alone.
type ticketWords struct{ strings.Builder }

// say adds a line of the store's own words.
func (w *ticketWords) say(line string) { w.WriteString(line + "\n") }

// block adds a block of values, each on one line after its label (a word
// of the store's, which value is given): every string of the capture but
// the person's notes is written this way and no other. A value is written
// as the capture holds it, folded onto one line (line), with addressFree
// over it as a courtesy: it takes the user, the query and the fragment off
// what it recognises as an address with a scheme, and changes nothing
// else.
func (w *ticketWords) block(values func(value func(label, s string))) {
	w.say(ticketFence)
	values(func(label, s string) { w.line(label, addressFree(fitText(s, false))) })
	w.say(ticketFence)
}

// line adds a line of a block: the label, then the value folded onto one
// line (fitText, strings.Fields); an empty one is its label alone.
func (w *ticketWords) line(label, s string) {
	if v := strings.Join(strings.Fields(fitText(s, false)), " "); v != "" {
		label += ": " + v
	} else {
		label += ":"
	}
	w.say(label)
}

// quote adds the person's note, their Markdown, each of its lines in a
// block quote, so whatever the note opens (a fence, a list) ends with the
// quote, at the blank line after it.
func (w *ticketWords) quote(note string) {
	for _, l := range strings.Split(fitText(note, true), "\n") {
		w.say("> " + l)
	}
}

// text is the description within n bytes: whole when it fits, else cut at
// a line's end with "…" after it, a block open there closed by its fence
// first, the fence counted in the bound; empty for a bound too small for
// the fence and the "…".
func (w *ticketWords) text(n int) string {
	const more = "\n…"
	text := w.String()
	if len(text) <= n {
		return text
	}
	if n < len(ticketFence)+1+len(more) {
		return ""
	}
	open := func(s string) bool { return strings.Count("\n"+s, "\n"+ticketFence+"\n")%2 == 1 }
	keep, _ := cutLines(text, n-len(more), "")
	if open(keep) {
		if keep, _ = cutLines(text, n-len(more)-len(ticketFence)-1, ""); open(keep) {
			keep += ticketFence + "\n"
		}
	}
	return keep + more
}

// ticketText is a new ticket's description made from a capture
// (describe), within MaxTicketDescription bytes.
func ticketText(c Capture) string { return describe(c).text(MaxTicketDescription) }

// describe is a new ticket's description made from a capture: its
// warnings first, in fixed words (the tree changed between the selection
// and the save, the page served another revision or one not confirmed, no
// screenshot); the capture's id and revision; one block of what the page
// and the tree gave (the page's address by pageAddress, its title, the
// viewport, the run and task, the commits of the selection's read and the
// save's, what the page declares, and why there is no screenshot: a text
// of the client's, of no fixed kind); then for each annotation a block of
// its kind, its element's tag, name and selector, what a click at its
// centre reaches, whether it is disabled and its first source, and the
// person's note, their Markdown, quoted. Every string of the capture but
// the notes stands in a block (ticketWords.block), whatever it holds; the
// capture's id and the numbers are the store's.
func describe(c Capture) *ticketWords {
	w := &ticketWords{}
	o := c.Origin
	if o.Drift != nil {
		w.say("> **The tree changed between the selection and the save.**")
	}
	switch {
	case o.Serving.State == ServingStale:
		w.say("> **The page served another revision than the tree's.**")
	case o.Serving.State == ServingUnconfirmed && o.Kind != OriginURL:
		w.say("> What the page served is not confirmed against the tree.")
	}
	if c.Image.How == ImageNone {
		w.say("> **No screenshot.**")
	}
	if w.Len() > 0 {
		w.say("")
	}
	revision := func(commit, content string) string {
		if content != "" {
			return short(commit) + "+" + short(content)
		}
		return short(commit)
	}
	w.say(fmt.Sprintf("Capture %s, revision %d, as the page and the tree gave it:", c.ID, c.Rev))
	w.say("")
	w.block(func(value func(label, s string)) {
		w.line("page", pageAddress(c.URL)) // from its parts, not folded again
		value("title", c.Title)
		value("viewport", fmt.Sprintf("%s %d by %d", c.Viewport.Mode, c.Viewport.Width, c.Viewport.Height))
		if o.Kind != OriginURL {
			value("tree", o.Tree)
			value("run", o.Run)
			value("task", o.Task)
			value("attempt", fmt.Sprint(o.Attempt))
			value("commit", short(o.Commit))
			value("changed paths", fmt.Sprint(o.Changed))
		}
		if d := o.Drift; d != nil {
			value("the selection read", revision(d.Commit, d.Content))
			value("the save read", revision(o.Commit, o.Content))
		}
		if o.Serving.State == ServingStale {
			value("the page declares", o.Serving.Page)
		}
		if c.Image.How == ImageNone {
			value("no screenshot", c.Image.Why)
		}
	})
	for _, a := range c.Annotations {
		w.say("")
		w.say(fmt.Sprintf("### Annotation %d", a.K))
		w.say("")
		w.block(func(value func(label, s string)) {
			value("kind", a.Kind)
			if e := a.Element; e != nil {
				value("element", e.Tag)
				value("name", e.Name)
				value("selector", e.Selector)
				if !e.Hit.Self {
					value("a click at its centre reaches", e.Hit.Selector)
				}
				if e.State.Disabled {
					value("disabled", "yes")
				}
			}
			if len(a.Sources) > 0 {
				value("source", a.Sources[0].Path)
				value("source class", a.Sources[0].Class)
			}
		})
		if a.Note != "" {
			w.say("")
			w.quote(a.Note)
		}
	}
	return w
}
