package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A ticket's publications (Program item IN1a, docs/plans/tracker-linking.md,
// "Publications: the state machine"): each one comment Conductor posts to a
// GitHub item for a person, composed and previewed, sent once. The row is
// the record of the send. BeginSend commits sending, with the try it
// starts (attempt), before any request to GitHub, and only the call whose
// transaction made that change owns the post; the state leaves sending
// only through a settle bound to that attempt. Nothing here posts again
// from sending or unconfirmed: a post is made again only from failed, which
// only an answer proving nothing was posted reaches, and only in the
// store_instance the publication was prepared in. Every decision of a
// change is made by one function of the row (beginSend, settled,
// beginClose, closeSettled, discarded), the same in the read that decides
// whether to apply it and in the operation's transaction.

// The states of a publication.
const (
	PublicationDraft       = "draft"
	PublicationSending     = "sending"
	PublicationSent        = "sent"
	PublicationFailed      = "failed"
	PublicationUnconfirmed = "unconfirmed"
	PublicationDiscarded   = "discarded"
)

// The states of a sent publication's close of its issue.
const (
	ClosePending = "pending"
	CloseClosing = "closing"
	CloseClosed  = "closed"
	CloseRefused = "refused"
)

// TargetLinked is the target that names the ticket's linked issue; a link
// is named "link:<id>".
const TargetLinked = "linked"

// PublicationParts is the parts a composer may offer, by name.
var PublicationParts = []string{"deliveries", "checks", "key"}

// The bounds of a publication.
const (
	// MaxPublicationBody bounds the body a person approves, in bytes.
	MaxPublicationBody = 16 << 10
	// MaxPublications bounds a ticket's publications.
	MaxPublications = 100
	// MaxPublicationTries bounds a publication's posts, and
	// MaxPublicationCloseTries its closes begun.
	MaxPublicationTries      = 5
	MaxPublicationCloseTries = 5
	// MaxTicketPublications is the newest publications a ticket's read
	// carries; MaxPublicationPage a page of them.
	MaxTicketPublications = 10
	MaxPublicationPage    = 50
	// MaxPublicationMarker bounds the adapter's marker, in bytes.
	MaxPublicationMarker = 128
	// MaxPublicationRef bounds a target's owner/name#n, in bytes.
	MaxPublicationRef = MaxSnapshotSpace + 1 + MaxSnapshotItemID
	// PublicationFindWindow is how far before its first send a settle's
	// listing starts (find_since): a server's clock hours off GitHub's
	// still lists the comment.
	PublicationFindWindow = 24 * time.Hour
)

// The intents of a publication's operations.
const (
	IntentPublicationPrepare = "publication.prepare"
	IntentPublicationSend    = "publication.send"
	IntentPublicationSettle  = "publication.settle"
	IntentPublicationClose   = "publication.close"
	IntentPublicationDiscard = "publication.discard"
)

// EntityPublication is a ticket's publication: its rows name their ticket
// and project, as a comment's do, and bump its own rev, never the
// ticket's.
const EntityPublication = "publication"

// Publication is a publication's row as a read shows it. Body is the
// publication's own read's alone; Restored says it was prepared in another
// store_instance (a restore's copy), so it is never sent from here.
type Publication struct {
	PublicationSummary
	Body string `json:"body"`
}

// PublicationSummary is a publication as a list shows it: all but its
// body, which its own read carries.
type PublicationSummary struct {
	ID            string    `json:"id"`
	Ticket        string    `json:"ticket"`
	TicketID      string    `json:"-"`
	ProjectID     string    `json:"-"`
	Target        string    `json:"target"`
	TargetKind    string    `json:"targetKind"`
	TargetURL     string    `json:"targetUrl"`
	TargetRef     string    `json:"targetRef"`
	TargetNodeID  string    `json:"targetNodeId"`
	TargetSpaceID string    `json:"targetSpaceId"`
	Instance      string    `json:"-"`
	Restored      bool      `json:"restored,omitempty"`
	Marker        string    `json:"marker"`
	PostSha256    string    `json:"postSha256"`
	Parts         []string  `json:"parts"`
	CloseIssue    string    `json:"closeIssue,omitempty"`
	State         string    `json:"state"`
	Tries         int       `json:"tries"`
	Attempt       int       `json:"attempt"`
	FindSince     time.Time `json:"findSince,omitzero"`
	SentAsID      string    `json:"sentAsId,omitempty"`
	SentAsLogin   string    `json:"sentAsLogin,omitempty"`
	RemoteID      string    `json:"remoteId,omitempty"`
	RemoteURL     string    `json:"remoteUrl,omitempty"`
	CloseState    string    `json:"closeState,omitempty"`
	CloseTries    int       `json:"closeTries"`
	CloseError    string    `json:"closeError,omitempty"`
	Error         string    `json:"error,omitempty"`
	SendingAt     time.Time `json:"sendingAt,omitzero"`
	SentAt        time.Time `json:"sentAt,omitzero"`
	SentBy        string    `json:"sentBy,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	CreatedBy     string    `json:"createdBy"`
	Rev           int64     `json:"rev"`
}

// Posted is the exact bytes a send posts: the body, a blank line and the
// marker. post_sha256 is their SHA-256.
func (p Publication) Posted() string { return p.Body + "\n\n" + p.Marker }

// Since is where a settle's listing starts: find_since, or for a
// publication never sent here its creation less the window.
func (p PublicationSummary) Since() time.Time {
	if !p.FindSince.IsZero() {
		return p.FindSince
	}
	return p.CreatedAt.Add(-PublicationFindWindow)
}

// NeedsYou reports a publication a person can act on: one being sent, not
// confirmed or failed, a restored draft, or a sent one whose close did not
// finish.
func (p PublicationSummary) NeedsYou() bool {
	switch p.State {
	case PublicationSending, PublicationUnconfirmed, PublicationFailed:
		return true
	case PublicationDraft:
		return p.Restored
	case PublicationSent:
		return p.CloseState == ClosePending || p.CloseState == CloseClosing || p.CloseState == CloseRefused
	}
	return false
}

// PublicationArgs is publication.prepare's content: the client's request.
type PublicationArgs struct {
	Key        string   `json:"key"`
	Target     string   `json:"target"`
	Body       string   `json:"body"`
	Parts      []string `json:"parts"`
	CloseIssue string   `json:"closeIssue"`
}

// PublicationTarget is where a publication goes, read from the tracker a
// moment before the prepare (its Resolve), handed apart from the request
// as every read is: the item's node id, its kind as the tracker types it,
// its repository's id, its page and its owner/name#n.
type PublicationTarget struct {
	NodeID, SpaceID, Kind, URL, Ref string
}

// Sender is the account a send's credential belongs to.
type Sender struct{ ID, Login string }

// Outcome is what a settle found: State sent (Comment the comment found or
// answered), failed (an answer proving nothing was posted), unconfirmed
// (the whole window listed, no comment, past the quiet time), or "" for
// words alone (the listing not whole, the target changed); Words say why.
type Outcome struct {
	State   string       `json:"state"`
	Comment *SentComment `json:"comment,omitempty"`
	Words   string       `json:"words,omitempty"`
}

// SentComment is a comment on the tracker: its id, its page and its
// author's id and login.
type SentComment struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	AuthorID string `json:"authorId"`
	Author   string `json:"author"`
}

// CloseOutcome is what a close found: closed or refused, with words.
type CloseOutcome struct {
	State string `json:"state"`
	Words string `json:"words,omitempty"`
}

const publicationColumns = `p.id, t.key, p.ticket_id, t.project_id, p.target, p.target_kind, p.target_url, p.target_ref, p.target_node_id, p.target_space_id,
	p.instance, p.marker, p.body, p.post_sha256, p.parts, p.close_issue, p.state, p.tries, p.attempt, p.find_since, p.sent_as_id, p.sent_as_login,
	p.remote_id, p.remote_url, p.close_state, p.close_tries, p.close_error, p.error, p.sending_at, p.sent_at, p.sent_by, p.created_at, p.created_by, p.rev`

const publicationFrom = ` FROM publications p JOIN tickets t ON t.id = p.ticket_id`

func scanPublication(row interface{ Scan(...any) error }, instance string) (Publication, error) {
	var p Publication
	var parts, since, sending, sent, created string
	err := row.Scan(&p.ID, &p.Ticket, &p.TicketID, &p.ProjectID, &p.Target, &p.TargetKind, &p.TargetURL, &p.TargetRef, &p.TargetNodeID, &p.TargetSpaceID,
		&p.Instance, &p.Marker, &p.Body, &p.PostSha256, &parts, &p.CloseIssue, &p.State, &p.Tries, &p.Attempt, &since, &p.SentAsID, &p.SentAsLogin,
		&p.RemoteID, &p.RemoteURL, &p.CloseState, &p.CloseTries, &p.CloseError, &p.Error, &sending, &sent, &p.SentBy, &created, &p.CreatedBy, &p.Rev)
	if err != nil {
		return Publication{}, err
	}
	_ = json.Unmarshal([]byte(parts), &p.Parts)
	if p.Parts == nil {
		p.Parts = []string{}
	}
	p.FindSince, p.SendingAt, p.SentAt, p.CreatedAt = unstamp(since), unstamp(sending), unstamp(sent), unstamp(created)
	p.Restored = p.Instance != instance
	return p, nil
}

// instanceOf reads the store's instance through q.
func instanceOf(ctx context.Context, q queryer) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, "SELECT id FROM store_instance").Scan(&id)
	if isNoRows(err) {
		return "", nil
	}
	return id, err
}

// publicationOf reads the publication with the id through q; a *Refusal
// not_found when there is none.
func publicationOf(ctx context.Context, q queryer, id string) (Publication, error) {
	instance, err := instanceOf(ctx, q)
	if err != nil {
		return Publication{}, err
	}
	p, err := scanPublication(q.QueryRowContext(ctx, "SELECT "+publicationColumns+publicationFrom+" WHERE p.id = ?", id), instance)
	if isNoRows(err) {
		return Publication{}, refuse(RefusalNotFound, "no publication %s", id)
	}
	return p, err
}

// scalars is the row's columns a publication's operations write, as text.
func (p Publication) scalars() []fieldValue {
	return []fieldValue{{"state", p.State}, {"tries", strconv.Itoa(p.Tries)}, {"attempt", strconv.Itoa(p.Attempt)}, {"find_since", stamp(p.FindSince)},
		{"sent_as_id", p.SentAsID}, {"sent_as_login", p.SentAsLogin}, {"remote_id", p.RemoteID}, {"remote_url", p.RemoteURL},
		{"close_state", p.CloseState}, {"close_tries", strconv.Itoa(p.CloseTries)}, {"close_error", p.CloseError}, {"error", p.Error},
		{"sending_at", stamp(p.SendingAt)}, {"sent_at", stamp(p.SentAt)}, {"sent_by", p.SentBy}}
}

// loginOf is an account's name as a publication keeps it: one line,
// address-free, at most MaxSnapshotLogin bytes.
func loginOf(s string) string {
	return cutBytes(strings.TrimSpace(addressFree(cleanText(s, false))), MaxSnapshotLogin)
}

// pubWords is a publication's words as its row keeps them (readWords).
func pubWords(s, otherwise string) string {
	if s = readWords(s); s == "" {
		s = otherwise
	}
	return s
}

// beginSend is the decision of a send (publication.send): the row made
// sending with a new attempt, or why not. Only a draft or a failed
// publication of this store's instance is sent, with the digest of the
// bytes its preview showed, at most MaxPublicationTries times.
func (p Publication) beginSend(digest string, by Sender, instance, author string, now time.Time) (Publication, error) {
	switch {
	case digest != p.PostSha256:
		return p, refuse(RefusalStaleRevision, "the digest sent is not the preview's: what is sent is what was previewed; read the publication again")
	case p.Instance != instance:
		return p, refuse(RefusalInvalid, "restored from an export: it may have been sent after the export was made; Check looks for it, or publish it as new")
	case p.State == PublicationSent:
		return p, refuse(RefusalInvalid, "already sent: %s's comment %s", p.TargetRef, p.RemoteID)
	case p.State == PublicationSending:
		return p, refuse(RefusalInvalid, "being sent: Check looks for it on GitHub")
	case p.State == PublicationUnconfirmed:
		return p, refuse(RefusalInvalid, "not confirmed: check it, or publish it as a new publication")
	case p.State != PublicationDraft && p.State != PublicationFailed:
		return p, refuse(RefusalInvalid, "the publication is %s", p.State)
	case p.Tries >= MaxPublicationTries:
		return p, refuse(RefusalBound, "a publication is sent at most %d times", MaxPublicationTries)
	}
	login := loginOf(by.Login)
	if err := checkRemoteID("the sender's id", by.ID); err != nil {
		return p, err
	}
	if by.ID == "" || login == "" {
		return p, refuse(RefusalInvalid, "a send names the account it posts as")
	}
	p.State, p.Tries, p.Attempt, p.SendingAt, p.SentBy, p.SentAsID, p.SentAsLogin, p.Error = PublicationSending, p.Tries+1, p.Tries+1, now, author, by.ID, login, ""
	if p.FindSince.IsZero() {
		p.FindSince = now.Add(-PublicationFindWindow)
	}
	return p, nil
}

// settled is the decision of a settle (publication.settle): what the
// outcome makes of the row, the row as it was when it changes nothing, or
// why it may not settle it. It is bound to the send's attempt, so a late
// answer of an old send never settles a later one. sending becomes sent,
// failed, unconfirmed or keeps its words; unconfirmed becomes sent or
// keeps its words; a restored draft or failed one becomes sent by a
// finding alone, its sender the comment's author, and nothing else changes
// it. A comment found for a send of this store's is its sender's.
func (p Publication) settled(attempt int, o Outcome, instance string, now time.Time) (Publication, error) {
	restored := p.Instance != instance
	if attempt != p.Attempt {
		return p, refuse(RefusalInvalid, "a settle of attempt %d; the publication's is %d", attempt, p.Attempt)
	}
	if p.State == PublicationUnconfirmed && o.State == PublicationUnconfirmed {
		o.State = ""
	}
	switch {
	case p.State == PublicationSending:
	case p.State == PublicationUnconfirmed && (o.State == PublicationSent || o.State == ""):
	case restored && (p.State == PublicationDraft || p.State == PublicationFailed):
		if o.State != PublicationSent {
			return p, nil
		}
	default:
		return p, refuse(RefusalInvalid, "a %s publication is not settled by %q", p.State, o.State)
	}
	switch o.State {
	case PublicationSent:
		c := o.Comment
		if c == nil || c.ID == "" || c.AuthorID == "" {
			return p, refuse(RefusalInvalid, "a comment found names its id and its author")
		}
		if err := checkRemoteID("the comment's id", c.ID); err != nil {
			return p, err
		}
		if err := checkRemoteID("the comment's author", c.AuthorID); err != nil {
			return p, err
		}
		if !restored && p.SentAsID != "" && c.AuthorID != p.SentAsID {
			return p, refuse(RefusalInvalid, "the comment is not by the account that sent it")
		}
		if restored || p.SentAsID == "" {
			p.SentAsID, p.SentAsLogin = c.AuthorID, loginOf(c.Author)
		}
		p.RemoteID, p.RemoteURL = c.ID, ""
		if a, err := admitAddress("the comment's page", c.URL); err == nil {
			p.RemoteURL = a.kept
		}
		p.State, p.SentAt, p.Error = PublicationSent, now, ""
		if p.CloseIssue != "" {
			p.CloseState = ClosePending
		}
	case PublicationFailed:
		if p.State != PublicationSending {
			return p, refuse(RefusalInvalid, "only a publication being sent fails")
		}
		p.State, p.Error = PublicationFailed, pubWords(o.Words, "GitHub refused the comment")
	case PublicationUnconfirmed:
		if p.State != PublicationSending {
			return p, refuse(RefusalInvalid, "only a publication being sent is not confirmed")
		}
		p.State, p.Error = PublicationUnconfirmed, pubWords(o.Words, "GitHub has not shown this comment; it may still appear")
	case "":
		p.Error = pubWords(o.Words, p.Error)
	default:
		return p, refuse(RefusalInvalid, "a settle's state is sent, failed, unconfirmed or none")
	}
	return p, nil
}

// beginClose is the decision of a close begun (publication.close): a sent
// publication whose close was asked for and is pending, closing or
// refused becomes closing, one more try, at most
// MaxPublicationCloseTries; every begin changes a field.
func (p Publication) beginClose() (Publication, error) {
	switch {
	case p.State != PublicationSent || p.CloseIssue == "":
		return p, refuse(RefusalInvalid, "only a sent publication whose close was asked for is closed")
	case p.CloseState != ClosePending && p.CloseState != CloseClosing && p.CloseState != CloseRefused:
		return p, refuse(RefusalInvalid, "the issue's close is %s", p.CloseState)
	case p.CloseTries >= MaxPublicationCloseTries:
		return p, refuse(RefusalBound, "an issue is closed at most %d times", MaxPublicationCloseTries)
	}
	p.CloseState, p.CloseTries, p.CloseError = CloseClosing, p.CloseTries+1, ""
	return p, nil
}

// closeSettled is the decision of a close's answer: a closing one becomes
// closed or refused, with its words.
func (p Publication) closeSettled(o CloseOutcome) (Publication, error) {
	if p.CloseState != CloseClosing || (o.State != CloseClosed && o.State != CloseRefused) {
		return p, refuse(RefusalInvalid, "a close's answer settles a closing close as closed or refused")
	}
	p.CloseState, p.CloseError = o.State, pubWords(o.Words, "")
	return p, nil
}

// discarded is the decision of a discard: a draft, a failed or an
// unconfirmed publication becomes discarded.
func (p Publication) discarded() (Publication, error) {
	if p.State != PublicationDraft && p.State != PublicationFailed && p.State != PublicationUnconfirmed {
		return p, refuse(RefusalInvalid, "a %s publication is not discarded", p.State)
	}
	p.State = PublicationDiscarded
	return p, nil
}

// changePublication runs decide on the publication with the id inside an
// operation, its revision the one expected, and writes what it changed:
// one set row per field, the publication's rev bumped.
func (t *tx) changePublication(id string, decide func(p Publication, instance string) (Publication, error)) error {
	p, err := publicationOf(t.ctx, t.tx, id)
	if err != nil {
		return err
	}
	if err := t.expect(p.Rev); err != nil {
		return err
	}
	instance, err := instanceOf(t.ctx, t.tx)
	if err != nil {
		return err
	}
	after, err := decide(p, instance)
	if err != nil {
		return err
	}
	base := ChangeRow{Entity: EntityPublication, EntityID: p.ID, TicketID: p.TicketID, ProjectID: p.ProjectID}
	return t.setScalars("publications", base, p.scalars(), after.scalars())
}

// normalBody is a body as a publication keeps it: \r\n and a lone \r made
// \n, trailing white space trimmed (the form a listing's match reads, so
// the bytes posted are the bytes matched); refused empty, past its bound,
// not UTF-8, or with a control character but \n and \t.
func normalBody(s string) (string, error) {
	s = strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), " \t\n")
	switch {
	case s == "":
		return "", refuse(RefusalInvalid, "a publication has a body")
	case len(s) > MaxPublicationBody:
		return "", refuse(RefusalBound, "a publication's body is at most %d bytes", MaxPublicationBody)
	case !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0:
		return "", refuse(RefusalInvalid, "a publication's body is UTF-8 with no control character but line breaks and tabs")
	}
	return s, nil
}

// PreparePublication makes a draft publication of the live ticket
// (publication.prepare): its target the ticket's linked issue (linked) or
// one of its live delivery or external links (link:<id>), at the item the
// API resolved a moment before (target: its node id, its kind as the
// tracker types it, its repository's id), which must be the linked issue's
// node, or the node the link's last read found; the body the person
// composed (normalBody), the parts ticked, the close asked for (completed
// or not_planned, for an issue alone), the marker mark gives of the id the
// transaction draws (holding the id, at most MaxPublicationMarker bytes),
// post_sha256 the SHA-256 of the bytes a send posts (Posted), and the
// store's instance. These are never written again: a publication goes to
// the item it was prepared for or nowhere. At most MaxPublications a
// ticket. The receipt's id is the publication's.
func (s *Store) PreparePublication(ctx context.Context, op Op, args PublicationArgs, target PublicationTarget, mark func(id string) string) (Receipt, error) {
	return s.Apply(ctx, op, IntentPublicationPrepare, args, func(t *tx) error {
		tk, _, err := t.ticket(args.Key, false)
		if err != nil {
			return err
		}
		body, err := normalBody(args.Body)
		if err != nil {
			return err
		}
		switch {
		case args.CloseIssue != "" && args.CloseIssue != "completed" && args.CloseIssue != "not_planned":
			return refuse(RefusalInvalid, "a close is completed or not_planned")
		case args.CloseIssue != "" && target.Kind != ExternalIssue:
			return refuse(RefusalInvalid, "only an issue is closed: closing a pull request closes it unmerged")
		case target.Kind != ExternalIssue && target.Kind != ExternalChange, !nodeIDPattern.MatchString(target.NodeID):
			return refuse(RefusalInvalid, "the target is an issue or a pull request by its node id")
		case target.SpaceID == "" || checkRemoteID("the target's repository", target.SpaceID) != nil:
			return refuse(RefusalInvalid, "the target names its repository's id")
		case target.Ref == "" || len(target.Ref) > MaxPublicationRef || holdsAddress(target.Ref) || strings.ContainsFunc(target.Ref, func(r rune) bool { return r <= ' ' || r > '~' }):
			return refuse(RefusalInvalid, "the target's place is owner/name#n")
		}
		page, err := itemPage(target.URL)
		if err != nil {
			return err
		}
		parts := []string{}
		for _, part := range args.Parts {
			if !slices.Contains(PublicationParts, part) || slices.Contains(parts, part) {
				return refuse(RefusalInvalid, "a part is one of %s, each once", strings.Join(PublicationParts, ", "))
			}
			parts = append(parts, part)
		}
		// The target is the ticket's linked issue, or a live link of it whose
		// last read found the node.
		if args.Target == TargetLinked {
			if i := strings.LastIndexByte(tk.ExternalID, ':'); tk.ExternalID == "" || tk.ExternalID[i+1:] != target.NodeID {
				return refuse(RefusalInvalid, "the target read is not %s's linked issue", tk.Key)
			}
		} else {
			linkID, ok := strings.CutPrefix(args.Target, "link:")
			var node string
			err := t.tx.QueryRowContext(t.ctx, `SELECT r.node_id FROM links l JOIN link_reads r ON r.link_id = l.id
				WHERE l.id = ? AND l.ticket_id = ? AND l.deleted_at = '' AND l.kind IN (?, ?)`, linkID, tk.ID, LinkDelivery, LinkExternal).Scan(&node)
			switch {
			case err != nil && !isNoRows(err):
				return err
			case !ok || node == "" || node != target.NodeID:
				return refuse(RefusalInvalid, "the target is %s's linked issue or a live delivery or external link read from GitHub", tk.Key)
			}
		}
		var n int
		if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM publications WHERE ticket_id = ?", tk.ID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxPublications {
			return refuse(RefusalBound, "a ticket has at most %d publications", MaxPublications)
		}
		instance, err := instanceOf(t.ctx, t.tx)
		if err != nil {
			return err
		}
		id := newID()
		marker := mark(id)
		if marker == "" || len(marker) > MaxPublicationMarker || !strings.Contains(marker, id) || !utf8.ValidString(marker) || strings.ContainsFunc(marker, unicode.IsControl) {
			return refuse(RefusalInvalid, "the tracker's marker holds the publication's id in at most %d bytes", MaxPublicationMarker)
		}
		p := Publication{PublicationSummary{ID: id, TicketID: tk.ID, Target: args.Target, TargetKind: target.Kind, TargetURL: page, TargetRef: target.Ref,
			TargetNodeID: target.NodeID, TargetSpaceID: target.SpaceID, Instance: instance, Marker: marker, Parts: parts, CloseIssue: args.CloseIssue,
			State: PublicationDraft, CreatedAt: t.now, CreatedBy: t.op.author()}, body}
		p.PostSha256 = sha256Hex(p.Posted())
		partsJSON, _ := json.Marshal(parts)
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO publications (id, ticket_id, target, target_kind, target_url, target_ref, target_node_id, target_space_id,
			instance, marker, body, post_sha256, parts, close_issue, state, created_at, created_by, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.TicketID, p.Target, p.TargetKind, p.TargetURL, p.TargetRef, p.TargetNodeID, p.TargetSpaceID, p.Instance, p.Marker, p.Body, p.PostSha256,
			string(partsJSON), p.CloseIssue, p.State, stamp(p.CreatedAt), p.CreatedBy, t.fp); err != nil {
			return err
		}
		value, err := encodeValue(p)
		if err != nil {
			return err
		}
		t.created(id, tk.Key)
		return t.record(ChangeRow{Entity: EntityPublication, EntityID: id, TicketID: tk.ID, ProjectID: tk.ProjectID, Op: OpCreate, Value: value})
	})
}

// sendArgs is publication.send's content: the client's request.
type sendArgs struct {
	ID         string `json:"id"`
	PostSha256 string `json:"postSha256"`
}

// BeginSend makes the publication sending for one more try (beginSend), as
// the account by (the account the send's credential belongs to), committed
// before any request to the tracker. owned is true only for the call whose
// transaction made that change: a resend of the same operation id is
// answered from its receipt (Apply looks it up before anything) and owns
// nothing, and its caller posts nothing. An expected revision is the
// publication's.
func (s *Store) BeginSend(ctx context.Context, op Op, id, postSha256 string, by Sender) (rc Receipt, owned bool, err error) {
	rc, err = s.Apply(ctx, op, IntentPublicationSend, sendArgs{id, postSha256}, func(t *tx) error {
		return t.changePublication(id, func(p Publication, instance string) (Publication, error) {
			return p.beginSend(postSha256, by, instance, t.op.author(), t.now)
		})
	})
	return rc, err == nil && rc.Status == StatusDone && !rc.Repeat, err
}

// DiscardPublication discards a draft, a failed or an unconfirmed
// publication (publication.discard). An expected revision is the
// publication's.
func (s *Store) DiscardPublication(ctx context.Context, op Op, id string) (Receipt, error) {
	return s.Apply(ctx, op, IntentPublicationDiscard, struct {
		ID string `json:"id"`
	}{id}, func(t *tx) error {
		return t.changePublication(id, func(p Publication, _ string) (Publication, error) { return p.discarded() })
	})
}

// serverOp is an operation the server makes for itself on the publication
// with the id (a settle, a close begun, a close's answer): decide is read
// against the row first, and an outcome that changes nothing is not
// applied at all (applied false: no operation, no receipt, no refusal
// kept), nor is one decide refuses (its *Refusal returned, kept nowhere).
// Otherwise it runs under DerivedOpID(id, phase, rev, the digest of what
// it carries) at that revision, so the same input at the same revision is
// a replay of one operation, and a different one a different id, which a
// moved revision refuses (stale_revision), never op_conflict.
func (s *Store) serverOp(ctx context.Context, actor, id, phase, intent string, input any, decide func(p Publication, instance string, now time.Time) (Publication, error)) (rc Receipt, applied bool, err error) {
	var p Publication
	var change bool
	err = s.read(ctx, func(q queryer) error {
		var err error
		if p, err = publicationOf(ctx, q, id); err != nil {
			return err
		}
		instance, err := instanceOf(ctx, q)
		if err != nil {
			return err
		}
		after, err := decide(p, instance, time.Now().UTC())
		change = err == nil && !slices.Equal(after.scalars(), p.scalars())
		return err
	})
	if err != nil || !change {
		return Receipt{}, false, err
	}
	digest, err := fingerprint(intent, input)
	if err != nil {
		return Receipt{}, false, err
	}
	op := Op{Actor: actor, ID: DerivedOpID(id, phase, strconv.FormatInt(p.Rev, 10), digest), ExpectedRev: p.Rev}
	rc, err = s.Apply(ctx, op, intent, input, func(t *tx) error {
		return t.changePublication(id, func(p Publication, instance string) (Publication, error) { return decide(p, instance, t.now) })
	})
	return rc, err == nil && rc.Status == StatusDone, err
}

// SettlePublication settles the publication's send of attempt with what
// was found (settled), as the server's operation under actor (serverOp).
func (s *Store) SettlePublication(ctx context.Context, actor, id string, attempt int, o Outcome) (Receipt, bool, error) {
	input := struct {
		ID      string  `json:"id"`
		Attempt int     `json:"attempt"`
		Outcome Outcome `json:"outcome"`
	}{id, attempt, o}
	return s.serverOp(ctx, actor, id, "settle", IntentPublicationSettle, input, func(p Publication, instance string, now time.Time) (Publication, error) {
		return p.settled(attempt, o, instance, now)
	})
}

// BeginClose begins the close of a sent publication's issue (beginClose),
// committed before the close is sent.
func (s *Store) BeginClose(ctx context.Context, actor, id string) (Receipt, bool, error) {
	input := struct {
		ID    string `json:"id"`
		Begin bool   `json:"begin"`
	}{id, true}
	return s.serverOp(ctx, actor, id, "close", IntentPublicationClose, input, func(p Publication, _ string, _ time.Time) (Publication, error) { return p.beginClose() })
}

// SettleClose settles a close begun with its answer (closeSettled).
func (s *Store) SettleClose(ctx context.Context, actor, id string, o CloseOutcome) (Receipt, bool, error) {
	input := struct {
		ID      string       `json:"id"`
		Outcome CloseOutcome `json:"outcome"`
	}{id, o}
	return s.serverOp(ctx, actor, id, "close", IntentPublicationClose, input, func(p Publication, _ string, _ time.Time) (Publication, error) {
		return p.closeSettled(o)
	})
}

// Publication reads the publication with the id, its body included;
// ErrNotFound when there is none.
func (s *Store) Publication(ctx context.Context, id string) (Publication, error) {
	var p Publication
	err := s.read(ctx, func(q queryer) error {
		var err error
		p, err = publicationOf(ctx, q, id)
		var ref *Refusal
		if errors.As(err, &ref) {
			return ErrNotFound
		}
		return err
	})
	return answer(p, err)
}

// publicationsOf reads the ticket's publications without their bodies,
// newest first, at most n after the cursor (a publication's id, "" for
// the start), those a person can act on alone when needYou.
func publicationsOf(ctx context.Context, q queryer, ticketID, cursor string, needYou bool, n int) ([]PublicationSummary, error) {
	out := []PublicationSummary{}
	if schemaOf(q) < 4 {
		return out, nil
	}
	instance, err := instanceOf(ctx, q)
	if err != nil {
		return nil, err
	}
	stmt := "SELECT " + publicationColumns + publicationFrom + " WHERE p.ticket_id = ?"
	args := []any{ticketID}
	if cursor != "" {
		stmt += " AND (p.created_at, p.id) < (SELECT created_at, id FROM publications WHERE id = ? AND ticket_id = ?)"
		args = append(args, cursor, ticketID)
	}
	rows, err := q.QueryContext(ctx, stmt+" ORDER BY p.created_at DESC, p.id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() && len(out) < n {
		p, err := scanPublication(rows, instance)
		if err != nil {
			return nil, err
		}
		if needYou && !p.NeedsYou() {
			continue
		}
		out = append(out, p.PublicationSummary)
	}
	return out, rows.Err()
}

// Publications pages the ticket's publications without their bodies,
// newest first, at most MaxPublicationPage after cursor (the last id of
// the page before), those a person can act on alone when needYou; next is
// the cursor of the next page, "" at the end. ErrNotFound when there is
// no such ticket; a cursor that is not one of its publications is an
// empty page.
func (s *Store) Publications(ctx context.Context, key, cursor string, needYou bool) (page []PublicationSummary, next string, err error) {
	err = s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if page, err = publicationsOf(ctx, q, tk.ID, cursor, needYou, MaxPublicationPage+1); err != nil {
			return err
		}
		if len(page) > MaxPublicationPage {
			page, next = page[:MaxPublicationPage], page[MaxPublicationPage-1].ID
		}
		return nil
	})
	return page, next, err
}

// ticketPublications is a ticket's read's part: its newest
// MaxTicketPublications publications and how many older ones a person can
// act on.
func ticketPublications(ctx context.Context, q queryer, ticketID string) ([]PublicationSummary, int, error) {
	newest, err := publicationsOf(ctx, q, ticketID, "", false, MaxTicketPublications)
	if err != nil || len(newest) < MaxTicketPublications {
		return newest, 0, err
	}
	older, err := publicationsOf(ctx, q, ticketID, newest[len(newest)-1].ID, true, MaxPublications)
	return newest, len(older), err
}

// SendingPublications lists the ids of at most n publications being sent,
// the oldest send first: what a server settles after it starts.
func (s *Store) SendingPublications(ctx context.Context, n int) ([]string, error) {
	var ids []string
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 4 {
			return nil
		}
		rows, err := q.QueryContext(ctx, "SELECT id FROM publications WHERE state = ? ORDER BY sending_at, id LIMIT ?", PublicationSending, n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}
