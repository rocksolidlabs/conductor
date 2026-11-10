package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

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
//	refused before any request (an untagged credential, a target or a
//	body out of shape, a host not admitted)                    nothing written
//	no connection was ever had for the request (a dial, a TLS
//	handshake or a proxy failed; the token's transport refused
//	the address; the call ended first)                          nothing written
//	a connection was had and no answer came (a reset, the
//	request's own deadline, the call ended)                     not known
//	any 3xx (a write follows none)                              not known
//	401; 403 (a permission, or a rate limit); 429 (a rate limit) nothing written
//	any other 4xx; any 5xx                                      not known
//	a 2xx past MaxGraphQLAnswer, or one that does not parse     not known
//	a 200 whose data for the mutation is null beside an error
//	of type NOT_FOUND, FORBIDDEN, UNPROCESSABLE or RATE_LIMITED nothing written
//	a 200 whose data is null beside no error, or another type   not known
//	a 200 with data and errors beside it                        not known
//	a 200 whose data does not say what was written (Comment:
//	no comment, ids that are not ids; Close: not the issue
//	asked, or not closed)                                       not known
//	a 200 whose data says what was written                      made
//
// Nothing written is answered only where the request provably never
// reached GitHub or GitHub provably refused it; an outcome not known is
// said in the error's words and the adapter never sends the write again.
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
		for _, e := range ans.Errors {
			switch e.Type {
			case "NOT_FOUND", "FORBIDDEN", "RATE_LIMITED":
				return nil, tracker.Proven(fail(a.gqlRefusal(gqlAnswer{Errors: []gqlError{e}, header: ans.header}), "GitHub refused the write"))
			case "UNPROCESSABLE":
				return nil, tracker.Proven(fail(tracker.ErrUnsupported, "GitHub refused the write as invalid"))
			}
		}
		return nil, tracker.Unknown(fail(tracker.ErrUnavailable, "GitHub's answer does not say whether the write was made"))
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

// Comment posts a comment on the node t names (AddCommentDocument), with a
// credential tagged for a comment. Its error says, through
// tracker.NothingWritten, whether the answer proves nothing was posted.
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
	if !nodeIDShape(t.NodeID) || t.NodeID == "" || len(t.NodeID) > tracker.MaxNodeID || body == "" || len(body) > MaxCommentBody {
		return tracker.Comment{}, tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrBound, Words: "a comment is 1 to 65536 bytes on a node"})
	}
	ans, r, status, gerr := a.gql(ctx, c, ep, AddCommentDocument, map[string]any{"subjectId": t.NodeID, "body": body}, true)
	raw, err := a.sortWrite(ep, r, status, ans, gerr, "addComment")
	if err != nil {
		return tracker.Comment{}, err
	}
	var got struct {
		CommentEdge *struct {
			Node *struct {
				FullDatabaseID bigID     `json:"fullDatabaseId"`
				URL            string    `json:"url"`
				CreatedAt      time.Time `json:"createdAt"`
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
	out = tracker.Comment{ID: n.FullDatabaseID.remote(), URL: n.URL, CreatedAt: n.CreatedAt, AuthorID: remoteID(n.Author.DatabaseID), Author: cut(n.Author.Login, 39)}
	if out.Check() != nil {
		return tracker.Comment{}, unknown
	}
	return out, nil
}

type commentJSON struct {
	ID        json.Number `json:"id"`
	HTMLURL   string      `json:"html_url"`
	Body      string      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
	User      *struct {
		Login string      `json:"login"`
		ID    json.Number `json:"id"`
	} `json:"user"`
}

// FindComment looks for a comment matching m on the node t names: it
// Resolves the node for this listing (tracker.ErrTargetChanged when it is
// no longer of t's kind in t's repository), then lists GET
// …/issues/{n}/comments?since=<since> at the node's place now, at most
// MaxCommentPages pages of 100. A comment's author is its user.id in
// decimal. Found is yes for a match; no only from a listing that cannot
// have skipped a comment: one read whole in a single page (one answer is
// one moment), or, over several pages, read whole twice with the same
// comments in the same order (GitHub's pages count by position, so a
// comment deleted before a page's start moves the next one into the page
// already read; a second pass that differs says the list moved); unknown
// otherwise: a page past the bound, a failure, a list that moved, or a
// comment whose bytes match but whose ids cannot be read.
func (a *Adapter) FindComment(ctx context.Context, c tracker.Credential, t tracker.Target, since time.Time, m tracker.Match) (tracker.Comment, tracker.Found, error) {
	if err := m.Check(); err != nil {
		return tracker.Comment{}, tracker.FoundUnknown, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: "-", Err: tracker.ErrUnsupported, Words: err.Error()}
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
	if err := now.Ref.Check(); err != nil {
		return tracker.Comment{}, tracker.FoundUnknown, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: ep.errPath(request{method: http.MethodPost}), Status: http.StatusOK, Err: tracker.ErrUnsupported, Words: "an answer Conductor cannot read"}
	}
	path := "/repos/" + now.Ref.Space + "/issues/" + now.Ref.ID + "/comments"
	first, err := a.listWindow(ctx, c, ep, path, since, m)
	switch {
	case err != nil:
		return tracker.Comment{}, tracker.FoundUnknown, err
	case first.match != nil:
		return *first.match, tracker.FoundYes, nil
	case !first.whole || first.unreadable:
		return tracker.Comment{}, tracker.FoundUnknown, nil
	case first.pages == 1:
		return tracker.Comment{}, tracker.FoundNo, nil
	}
	again, err := a.listWindow(ctx, c, ep, path, since, m)
	switch {
	case err != nil:
		return tracker.Comment{}, tracker.FoundUnknown, err
	case again.match != nil:
		return *again.match, tracker.FoundYes, nil
	case !again.whole || again.unreadable || !slices.Equal(first.ids, again.ids):
		return tracker.Comment{}, tracker.FoundUnknown, nil
	}
	return tracker.Comment{}, tracker.FoundNo, nil
}

// window is one pass over a FindComment listing.
type window struct {
	match *tracker.Comment
	// ids are the listed comments' ids in order; pages the pages read.
	ids   []string
	pages int
	// whole: the last page was reached; unreadable: a comment's bytes
	// matched but its ids or author could not be read.
	whole, unreadable bool
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
			if !m.MatchesBody(cm.Body) {
				continue
			}
			if cm.User == nil || remoteID(cm.User.ID) == "" {
				w.unreadable = true
				continue
			}
			author := remoteID(cm.User.ID)
			if m.AuthorID != "" && author != m.AuthorID {
				continue
			}
			found := tracker.Comment{ID: remoteID(cm.ID), URL: cm.HTMLURL, CreatedAt: cm.CreatedAt, AuthorID: author, Author: cut(cm.User.Login, 39)}
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
// a credential tagged for a close; a Target of kind change is refused
// before any request. Its error says, through tracker.NothingWritten,
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
	if !nodeIDShape(t.NodeID) {
		return tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "a node id is 1 to 100 bytes"})
	}
	ans, r, status, gerr := a.gql(ctx, c, ep, CloseIssueDocument, map[string]any{"issueId": t.NodeID, "stateReason": gr}, true)
	raw, err := a.sortWrite(ep, r, status, ans, gerr, "closeIssue")
	if err != nil {
		return err
	}
	// Made only when the answer names the issue asked, closed.
	var got struct {
		Issue *struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"issue"`
	}
	if json.Unmarshal(raw, &got) != nil || got.Issue == nil || got.Issue.ID != t.NodeID || got.Issue.State != "CLOSED" {
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
