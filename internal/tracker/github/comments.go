package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// tagged is the write a credential is tagged for, refused (proving
// nothing was written: no request is made) when it is not of kind.
func tagged(c tracker.Credential, kind string) (tracker.Write, error) {
	w, ok := c.Write()
	if !ok || w.Kind != kind || w.Publication == "" {
		return tracker.Write{}, tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "a write's credential is tagged for it"})
	}
	return w, nil
}

// sortWrite reads a mutation's answer: its data for field, or the error
// saying which of the write table's three outcomes the answer proves. The
// table, every way a write can end (a test walks it, TestWriteTable):
//
//	refused before any request (an untagged credential, a target, or
//	a body out of shape or not valid UTF-8, a host not admitted,
//	the call ended before the request was made)                 nothing written
//	the base transport was never handed the request (the token's
//	transport refused the address)                              nothing written
//	the base transport was handed it and no answer came (a dial,
//	a reset, the request's own deadline, the call ended: what it
//	sent is not known)                                          not known
//	any 1xx, or any 2xx but 200                                 not known
//	any 3xx, 304 included (a write follows none)                not known
//	401; 403 (a permission, or a rate limit); 429 (a rate limit) nothing written
//	any other 4xx; any 5xx                                      not known
//	a 200 past MaxGraphQLAnswer, or one that does not parse     not known
//	a 200 whose data for the mutation is null and whose every
//	error is of type NOT_FOUND, FORBIDDEN, UNPROCESSABLE or
//	RATE_LIMITED at no path or at the mutation's own field      nothing written
//	a 200 whose data is null beside no error, or beside any
//	other error (another type, or a refusal deeper in the
//	payload), in any order                                      not known
//	a 200 with data and errors beside it                        not known
//	a 200 whose data does not say what was written, or says
//	it of another target (Comment: no comment, ids that are
//	not ids, a subject or repository not the target's; Close:
//	not the issue asked or its repository, or not closed)       not known
//	a 200 whose data says what was written on the target        made
//
// Nothing written is answered only on a fact: the request provably never
// left (refused before it, or never handed to the base transport) or
// GitHub's own answer refused it; an outcome not known is said in the
// error's words and the adapter never sends the write again.
func (a *Adapter) sortWrite(ep endpoint, r request, status int, ans gqlAnswer, err error, field string) (json.RawMessage, error) {
	if err != nil {
		if tracker.NothingWritten(err) {
			return nil, err
		}
		var re *tracker.RequestError
		if errors.As(err, &re) && (re.Status == http.StatusUnauthorized || re.Status == http.StatusForbidden || re.Status == http.StatusTooManyRequests) {
			return nil, tracker.Proven(err)
		}
		return nil, tracker.Unknown(err)
	}
	raw := ans.Data[field]
	fail := func(e error, words string) *tracker.RequestError {
		return &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Status: status, Err: e, Words: words}
	}
	if len(raw) == 0 || string(raw) == "null" {
		proven := len(ans.Errors) > 0
		for _, e := range ans.Errors {
			proven = proven && e.refusing(field)
		}
		if !proven {
			return nil, tracker.Unknown(fail(tracker.ErrUnavailable, "GitHub's answer does not say whether the write was made"))
		}
		if ans.Errors[0].Type == "UNPROCESSABLE" {
			return nil, tracker.Proven(fail(tracker.ErrUnsupported, "GitHub refused the write as invalid"))
		}
		return nil, tracker.Proven(fail(a.gqlRefusal(gqlAnswer{Errors: ans.Errors[:1], header: ans.header}), "GitHub refused the write"))
	}
	if len(ans.Errors) > 0 {
		return nil, tracker.Unknown(fail(tracker.ErrUnavailable, "GitHub answered with errors beside the write's data"))
	}
	return raw, nil
}

// logWrite is a write's line: the publication and the try, never the body.
func (a *Adapter) logWrite(w tracker.Write, err error) {
	outcome := "made"
	switch {
	case tracker.NothingWritten(err):
		outcome = "nothing written"
	case err != nil:
		outcome = "not known"
	}
	attrs := []any{"system", tracker.GitHub, "write", w.Kind, "publication", w.Publication, "try", w.Try, "outcome", outcome}
	if err != nil {
		attrs = append(attrs, "err", err.Error())
	}
	a.log.Info("tracker write", attrs...)
}

// targetShape refuses, proving nothing written, a Target of a shape no
// Resolve returns: its kind issue or change, its node id a node id, and
// its repository's identity the adapter's own form of one (a positive
// decimal number).
func targetShape(t tracker.Target) error {
	if (t.Kind != tracker.KindIssue && t.Kind != tracker.KindChange) || !nodeIDShape(t.NodeID) || !spaceIDShape(t.SpaceID) {
		return tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "not a target a Resolve gives"})
	}
	return nil
}

// spaceIDShape reports a repository's identity in the adapter's own form:
// a positive decimal number with no leading zero.
func spaceIDShape(id tracker.RemoteID) bool { return repo{id: id}.path("") != "" }

// Comment posts a comment on the node t names (AddCommentDocument), with a
// credential tagged for a comment. Before any request, proving nothing
// written, it refuses what its own finder could never confirm: a Target of
// a shape no Resolve returns, and a body that is not the bytes the tag
// names (its SHA-256 the tag's PostSha256), not in the form a listing's
// match reads (tracker.BodyDigest's normalisation leaves it as it is), or
// without the publication's marker. Its answer is made only when it names
// the target's own node (the subject) and repository; any other is not
// known. Its error says, through tracker.NothingWritten, whether the
// answer proves nothing was posted.
func (a *Adapter) Comment(ctx context.Context, c tracker.Credential, t tracker.Target, body string) (out tracker.Comment, err error) {
	w, err := tagged(c, tracker.WriteComment)
	if err != nil {
		return tracker.Comment{}, err
	}
	defer func() { a.logWrite(w, err) }()
	ep, err := a.endpointFor(c, t.Ref.Host)
	if err != nil {
		return tracker.Comment{}, tracker.Proven(err)
	}
	if err := targetShape(t); err != nil {
		return tracker.Comment{}, err
	}
	if !nodeIDShape(t.NodeID) || t.NodeID == "" || len(t.NodeID) > tracker.MaxNodeID || body == "" || len(body) > MaxCommentBody {
		return tracker.Comment{}, tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrBound, Words: "a comment is 1 to 65536 bytes on a node"})
	}
	// What is posted is byte for byte what was given, or nothing: the
	// encoding to GitHub would change bytes that are not UTF-8.
	if !utf8.ValidString(body) {
		return tracker.Comment{}, tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "a comment's body is UTF-8"})
	}
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	if marker := a.Marker(w.Publication); digest != w.PostSha256 || tracker.BodyDigest(body) != digest || marker == "" || !strings.Contains(body, marker) {
		return tracker.Comment{}, tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "a comment's body is the bytes its tag names, normalised, with its marker"})
	}
	ans, r, status, gerr := a.gql(ctx, c, ep, AddCommentDocument, map[string]any{"subjectId": t.NodeID, "body": body}, true)
	raw, err := a.sortWrite(ep, r, status, ans, gerr, "addComment")
	if err != nil {
		return tracker.Comment{}, err
	}
	var got struct {
		Subject *struct {
			ID string `json:"id"`
		} `json:"subject"`
		CommentEdge *struct {
			Node *struct {
				FullDatabaseID bigID     `json:"fullDatabaseId"`
				CreatedAt      time.Time `json:"createdAt"`
				Repository     *repoJSON `json:"repository"`
				Author         *struct {
					Login      string      `json:"login"`
					DatabaseID json.Number `json:"databaseId"`
				} `json:"author"`
			} `json:"node"`
		} `json:"commentEdge"`
	}
	unknown := tracker.Unknown(&tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Status: status, Err: tracker.ErrUnsupported, Words: "a comment's answer Conductor cannot read"})
	if json.Unmarshal(raw, &got) != nil || got.CommentEdge == nil || got.CommentEdge.Node == nil || got.CommentEdge.Node.Author == nil {
		return tracker.Comment{}, unknown
	}
	n := got.CommentEdge.Node
	if got.Subject == nil || got.Subject.ID != t.NodeID || n.Repository == nil || n.Repository.FullDatabaseID.remote() != t.SpaceID {
		return tracker.Comment{}, tracker.Unknown(&tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Status: status, Err: tracker.ErrTargetChanged, Words: "the comment's answer does not name the target's node and repository"})
	}
	out = tracker.Comment{ID: n.FullDatabaseID.remote(), CreatedAt: n.CreatedAt, AuthorID: remoteID(n.Author.DatabaseID), Author: cut(n.Author.Login, 39)}
	if out.Check() != nil {
		return tracker.Comment{}, unknown
	}
	return onPage(out, t.URL), nil
}

// onPage is cm with its URL the item's page as a resolve gave it and the
// comment's anchor (tracker.Comment).
func onPage(cm tracker.Comment, page string) tracker.Comment {
	cm.URL = page + "#issuecomment-" + string(cm.ID)
	return cm
}

type commentJSON struct {
	ID        json.Number `json:"id"`
	Body      string      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
	User      *struct {
		Login string      `json:"login"`
		ID    json.Number `json:"id"`
	} `json:"user"`
}

// FindComment looks for a comment matching m, made at or after since, on
// the node t names; a Target of a shape no Resolve returns is refused
// before any request. It Resolves the node for this listing (tracker.ErrTargetChanged when it is
// no longer of t's kind in t's repository), then lists GET
// /repositories/{id}/issues/{n}/comments?since=<since>, the repository by
// the id the Resolve read (a rename keeps it, a name another repository
// takes has another, so no other repository's issue can answer for the
// target), at most MaxCommentPages pages of 100, none answered from a
// redirect (a page that is is a failure: unknown). GitHub's since is the
// time of a comment's last change, so a listed comment created before
// since is never a match, and one whose creation time cannot be read is
// not known. A comment's author is its user.id in decimal. Found is yes for a match; no only from a listing that cannot
// have skipped a comment: one read whole in a single page (one answer is
// one moment), or, over several pages, read whole twice with the same
// comments in the same order (GitHub's pages count by position, so a
// comment deleted before a page's start moves the next one into the page
// already read; a second pass that differs says the list moved); unknown
// otherwise: a page past the bound, a failure, a list that moved, or a
// comment whose bytes match (whose marker, for a Match that is Marked) but
// whose ids cannot be read. Of a Match that is Marked, a window read so
// with no match is other, not no, when a comment in it holds the marker.
func (a *Adapter) FindComment(ctx context.Context, c tracker.Credential, t tracker.Target, since time.Time, m tracker.Match) (tracker.Comment, tracker.Found, error) {
	if err := m.Check(); err != nil {
		return tracker.Comment{}, tracker.FoundUnknown, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: "-", Err: tracker.ErrUnsupported, Words: err.Error()}
	}
	if targetShape(t) != nil {
		return tracker.Comment{}, tracker.FoundUnknown, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: "-", Err: tracker.ErrUnsupported, Words: "not a target a Resolve gives"}
	}
	ep, err := a.endpointFor(c, t.Ref.Host)
	if err != nil {
		return tracker.Comment{}, tracker.FoundUnknown, err
	}
	now, err := a.Resolve(ctx, c, t.NodeID)
	if err != nil {
		return tracker.Comment{}, tracker.FoundUnknown, err
	}
	if now.Kind != t.Kind || now.SpaceID != t.SpaceID {
		return tracker.Comment{}, tracker.FoundUnknown, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: ep.errPath(request{method: http.MethodPost}), Status: http.StatusOK, Err: tracker.ErrTargetChanged}
	}
	if err := now.Ref.Check(); err != nil || decimal(string(now.SpaceID)) == "" {
		return tracker.Comment{}, tracker.FoundUnknown, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: ep.errPath(request{method: http.MethodPost}), Status: http.StatusOK, Err: tracker.ErrUnsupported, Words: "an answer Conductor cannot read"}
	}
	path := repo{id: now.SpaceID}.path("/issues/" + now.Ref.ID + "/comments")
	first, err := a.listWindow(ctx, c, ep, path, since, m)
	switch {
	case err != nil:
		return tracker.Comment{}, tracker.FoundUnknown, err
	case first.match != nil:
		return onPage(*first.match, now.URL), tracker.FoundYes, nil
	case !first.whole || first.unreadable:
		return tracker.Comment{}, tracker.FoundUnknown, nil
	case first.pages == 1:
		return tracker.Comment{}, first.none(), nil
	}
	again, err := a.listWindow(ctx, c, ep, path, since, m)
	switch {
	case err != nil:
		return tracker.Comment{}, tracker.FoundUnknown, err
	case again.match != nil:
		return onPage(*again.match, now.URL), tracker.FoundYes, nil
	case !again.whole || again.unreadable || !slices.Equal(first.ids, again.ids):
		return tracker.Comment{}, tracker.FoundUnknown, nil
	}
	again.other = again.other || first.other
	return tracker.Comment{}, again.none(), nil
}

// window is one pass over a FindComment listing.
type window struct {
	match *tracker.Comment
	// ids are the listed comments' ids in order; pages the pages read.
	ids   []string
	pages int
	// whole: the last page was reached; unreadable: a comment's bytes
	// matched but its ids or author could not be read; other: a comment
	// that does not match holds the marker of a Match that is Marked.
	whole, unreadable, other bool
}

// none is a window read whole with no match: other when a comment in it
// holds a Marked match's marker, no otherwise.
func (w window) none() tracker.Found {
	if w.other {
		return tracker.FoundOther
	}
	return tracker.FoundNo
}

func (a *Adapter) listWindow(ctx context.Context, c tracker.Credential, ep endpoint, path string, since time.Time, m tracker.Match) (window, error) {
	var w window
	for p := 1; p <= MaxCommentPages; p++ {
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(p)}}
		if !since.IsZero() {
			q.Set("since", since.UTC().Format(time.RFC3339))
		}
		var list []commentJSON
		ans, err := a.page(ctx, c, ep, path, q, &list)
		if err != nil {
			return w, err
		}
		w.pages = p
		for _, cm := range list {
			w.ids = append(w.ids, cm.ID.String())
			if !strings.Contains(cm.Body, m.Marker) || !m.Marked && !m.MatchesBody(cm.Body) {
				continue
			}
			// GitHub's since is the time of a comment's last change: one
			// made before since is never a match, and one whose time
			// cannot be read is not known.
			if cm.CreatedAt.IsZero() {
				w.unreadable = true
				continue
			}
			if !since.IsZero() && cm.CreatedAt.Before(since) {
				continue
			}
			if cm.User == nil || remoteID(cm.User.ID) == "" {
				w.unreadable = true
				continue
			}
			author := remoteID(cm.User.ID)
			if !m.Matches(cm.Body, author) {
				w.other = w.other || m.Marked
				continue
			}
			found := tracker.Comment{ID: remoteID(cm.ID), CreatedAt: cm.CreatedAt, AuthorID: author, Author: cut(cm.User.Login, 39)}
			if found.Check() != nil {
				w.unreadable = true
				continue
			}
			w.match = &found
			return w, nil
		}
		// The last page: no next named, and either a short page or a
		// Link that names the others (a full page with no Link at all
		// may have a next; it is read).
		if !hasNext(ans) && (len(list) < 100 || ans.header.Get("Link") != "") {
			w.whole = true
			return w, nil
		}
	}
	return w, nil
}

// Close closes the issue t names (CloseIssueDocument) with a reason, with
// a credential tagged for a close; a Target of kind change, or of a shape
// no Resolve returns, is refused before any request. Its answer is made
// only when it names the issue asked, in the target's repository, closed. Its error says, through tracker.NothingWritten,
// whether the answer proves the issue was not closed.
func (a *Adapter) Close(ctx context.Context, c tracker.Credential, t tracker.Target, reason string) (err error) {
	w, err := tagged(c, tracker.WriteClose)
	if err != nil {
		return err
	}
	defer func() { a.logWrite(w, err) }()
	if t.Kind != tracker.KindIssue {
		return tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "only an issue is closed"})
	}
	var gr string
	switch reason {
	case tracker.ReasonCompleted:
		gr = "COMPLETED"
	case tracker.ReasonNotPlanned:
		gr = "NOT_PLANNED"
	default:
		return tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "a close's reason is completed or not_planned"})
	}
	ep, err := a.endpointFor(c, t.Ref.Host)
	if err != nil {
		return tracker.Proven(err)
	}
	if err := targetShape(t); err != nil {
		return err
	}
	ans, r, status, gerr := a.gql(ctx, c, ep, CloseIssueDocument, map[string]any{"issueId": t.NodeID, "stateReason": gr}, true)
	raw, err := a.sortWrite(ep, r, status, ans, gerr, "closeIssue")
	if err != nil {
		return err
	}
	// Made only when the answer names the issue asked, in the target's
	// repository, closed.
	var got struct {
		Issue *struct {
			ID         string    `json:"id"`
			State      string    `json:"state"`
			Repository *repoJSON `json:"repository"`
		} `json:"issue"`
	}
	if json.Unmarshal(raw, &got) != nil || got.Issue == nil || got.Issue.ID != t.NodeID || got.Issue.State != "CLOSED" || got.Issue.Repository == nil || got.Issue.Repository.FullDatabaseID.remote() != t.SpaceID {
		return tracker.Unknown(&tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Status: status, Err: tracker.ErrUnsupported, Words: "a close's answer that does not name the issue closed"})
	}
	return nil
}

// nodeIDShape reports a node id of 1 to MaxNodeID bytes of visible ASCII
// (it goes into a GraphQL variable, never into a path).
func nodeIDShape(id string) bool {
	return id != "" && len(id) <= tracker.MaxNodeID && visible(id)
}

func visible(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
