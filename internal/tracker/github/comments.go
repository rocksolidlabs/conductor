package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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
// saying whether the answer proves nothing was written. Only HTTP 401 or
// 403, or a 200 whose data for the mutation is null beside an error of
// type NOT_FOUND, FORBIDDEN or UNPROCESSABLE, proves it; every other
// answer, a 200 with partial data included, is not known.
func sortWrite(ep endpoint, r request, status int, ans gqlAnswer, err error, field string) (json.RawMessage, error) {
	if err != nil {
		var re *tracker.RequestError
		if errors.As(err, &re) && (re.Status == http.StatusUnauthorized || re.Status == http.StatusForbidden) {
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
			case "NOT_FOUND", "FORBIDDEN":
				return nil, tracker.Proven(fail(gqlRefusal([]gqlError{e}), "GitHub refused the write"))
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
	if t.NodeID == "" || len(t.NodeID) > tracker.MaxNodeID || body == "" || len(body) > MaxCommentBody {
		return tracker.Comment{}, tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrBound, Words: "a comment is 1 to 65536 bytes on a node"})
	}
	ans, r, status, gerr := a.gql(ctx, c, ep, AddCommentDocument, map[string]any{"subjectId": t.NodeID, "body": body})
	raw, err := sortWrite(ep, r, status, ans, gerr, "addComment")
	if err != nil {
		return tracker.Comment{}, err
	}
	var got struct {
		CommentEdge *struct {
			Node *struct {
				DatabaseID json.Number `json:"databaseId"`
				URL        string      `json:"url"`
				CreatedAt  time.Time   `json:"createdAt"`
				Author     *struct {
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
	out = tracker.Comment{ID: remoteID(n.DatabaseID), URL: n.URL, CreatedAt: n.CreatedAt, AuthorID: remoteID(n.Author.DatabaseID), Author: cut(n.Author.Login, 39)}
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
// decimal. Found is yes for a match, no when every comment of the window
// was read, unknown when more pages are there than it reads.
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
	path := "/repos/" + now.Ref.Space + "/issues/" + now.Ref.ID + "/comments"
	for p := 1; p <= MaxCommentPages; p++ {
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(p)}}
		if !since.IsZero() {
			q.Set("since", since.UTC().Format(time.RFC3339))
		}
		var list []commentJSON
		ans, err := a.page(ctx, c, ep, path, q, &list)
		if err != nil {
			return tracker.Comment{}, tracker.FoundUnknown, err
		}
		for _, cm := range list {
			if cm.User == nil {
				continue
			}
			author := remoteID(cm.User.ID)
			if !m.Matches(cm.Body, author) {
				continue
			}
			found := tracker.Comment{ID: remoteID(cm.ID), URL: cm.HTMLURL, CreatedAt: cm.CreatedAt, AuthorID: author, Author: cut(cm.User.Login, 39)}
			if found.Check() != nil {
				continue
			}
			return found, tracker.FoundYes, nil
		}
		// The last page: no next named, and either a short page or a
		// Link that names the others (a full page with no Link at all
		// may have a next; it is read).
		if !hasNext(ans) && (len(list) < 100 || ans.header.Get("Link") != "") {
			return tracker.Comment{}, tracker.FoundNo, nil
		}
	}
	return tracker.Comment{}, tracker.FoundUnknown, nil
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
	if t.NodeID == "" || len(t.NodeID) > tracker.MaxNodeID {
		return tracker.Proven(&tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "a node id is 1 to 100 bytes"})
	}
	ans, r, status, gerr := a.gql(ctx, c, ep, CloseIssueDocument, map[string]any{"issueId": t.NodeID, "stateReason": gr})
	_, err = sortWrite(ep, r, status, ans, gerr, "closeIssue")
	return err
}
