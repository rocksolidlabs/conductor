package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// The three GraphQL documents, sent byte for byte as written, every value
// a variable and never text built into a document, so each is named by
// its SHA-256 (graphql_test.go's golden holds them) and a transport that
// admits only known documents can recognise it.
const (
	// ResolveDocument reads where a node is now: its type (Issue is kind
	// issue, PullRequest kind change), number, URL, state and repository.
	ResolveDocument = `query Resolve($id: ID!) {
  node(id: $id) {
    __typename
    ... on Issue { id number url state repository { databaseId nameWithOwner } }
    ... on PullRequest { id number url state repository { databaseId nameWithOwner } }
  }
}`
	// AddCommentDocument posts a comment on a node, answering its
	// database id, URL, time and author.
	AddCommentDocument = `mutation AddComment($subjectId: ID!, $body: String!) {
  addComment(input: {subjectId: $subjectId, body: $body}) {
    commentEdge { node { databaseId url createdAt author { login ... on User { databaseId } ... on Bot { databaseId } } } }
  }
}`
	// CloseIssueDocument closes an issue with a reason.
	CloseIssueDocument = `mutation CloseIssue($issueId: ID!, $stateReason: IssueClosedStateReason) {
  closeIssue(input: {issueId: $issueId, stateReason: $stateReason}) {
    issue { id state stateReason }
  }
}`
)

// gqlError is one of a GraphQL answer's errors.
type gqlError struct {
	Type string `json:"type"`
}

// gqlAnswer is a GraphQL answer: its data, raw, and its errors.
type gqlAnswer struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []gqlError                 `json:"errors"`
}

// gql posts a document with its variables. The answer's HTTP refusal is
// the error do maps; a 200 is decoded, its errors left to the caller.
func (a *Adapter) gql(ctx context.Context, c tracker.Credential, ep endpoint, doc string, vars map[string]any) (gqlAnswer, request, int, error) {
	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{doc, vars})
	r := request{method: http.MethodPost, body: body, max: MaxGraphQLAnswer}
	if err != nil {
		return gqlAnswer{}, r, 0, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Err: tracker.ErrUnsupported}
	}
	ans, err := a.do(ctx, c, ep, r)
	if err != nil {
		return gqlAnswer{}, r, ans.status, err
	}
	var out gqlAnswer
	if err := decode(ep, r, ans.body, &out); err != nil {
		return gqlAnswer{}, r, ans.status, err
	}
	return out, r, ans.status, nil
}

// gqlRefusal maps a GraphQL answer's errors to a typed error.
func gqlRefusal(errs []gqlError) error {
	for _, e := range errs {
		switch e.Type {
		case "NOT_FOUND":
			return tracker.ErrNotFound
		case "FORBIDDEN":
			return tracker.ErrForbidden
		case "RATE_LIMITED":
			return &tracker.ErrRateLimited{}
		}
	}
	return tracker.ErrUnsupported
}

type repoJSON struct {
	DatabaseID    json.Number `json:"databaseId"`
	NameWithOwner string      `json:"nameWithOwner"`
}

type nodeJSON struct {
	Typename   string   `json:"__typename"`
	ID         string   `json:"id"`
	Number     int64    `json:"number"`
	URL        string   `json:"url"`
	State      string   `json:"state"`
	Repository repoJSON `json:"repository"`
}

// Resolve reads where a node is now (ResolveDocument): an Issue is a
// Target of kind issue, a PullRequest of kind change, any other node, or
// none, tracker.ErrNotFound. The kind is GitHub's type, never a URL's.
func (a *Adapter) Resolve(ctx context.Context, c tracker.Credential, nodeID string) (tracker.Target, error) {
	ep, err := a.endpointFor(c, c.Host())
	if err != nil {
		return tracker.Target{}, err
	}
	if nodeID == "" || len(nodeID) > tracker.MaxNodeID {
		return tracker.Target{}, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "-", Err: tracker.ErrUnsupported, Words: "a node id is 1 to 100 bytes"}
	}
	ans, r, status, err := a.gql(ctx, c, ep, ResolveDocument, map[string]any{"id": nodeID})
	if err != nil {
		return tracker.Target{}, err
	}
	fail := func(e error, words string) (tracker.Target, error) {
		return tracker.Target{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Status: status, Err: e, Words: words}
	}
	raw := ans.Data["node"]
	if len(raw) == 0 || string(raw) == "null" {
		if len(ans.Errors) > 0 && gqlRefusal(ans.Errors) != tracker.ErrNotFound {
			return fail(gqlRefusal(ans.Errors), "")
		}
		return fail(tracker.ErrNotFound, "")
	}
	var n nodeJSON
	if err := json.Unmarshal(raw, &n); err != nil {
		return fail(tracker.ErrUnsupported, "an answer Conductor cannot read")
	}
	t := tracker.Target{NodeID: n.ID, SpaceID: remoteID(n.Repository.DatabaseID), State: strings.ToLower(n.State)}
	switch n.Typename {
	case "Issue":
		t.Kind = tracker.KindIssue
	case "PullRequest":
		t.Kind = tracker.KindChange
	default:
		return fail(tracker.ErrNotFound, "the node is not an issue or a pull request")
	}
	owner, name, ok := strings.Cut(n.Repository.NameWithOwner, "/")
	if !ok || n.ID != nodeID || t.SpaceID == "" {
		return fail(tracker.ErrUnsupported, "an answer Conductor cannot read")
	}
	ref, err := tracker.ParseGitHub(owner+"/"+name+"#"+strconv.FormatInt(n.Number, 10), "", tracker.ParseOptions{})
	if err != nil {
		return fail(tracker.ErrUnsupported, "an answer Conductor cannot read")
	}
	ref.Host, ref.Kind = ep.host, t.Kind
	t.Ref = ref
	t.URL = itemURL(ref, n.URL)
	switch t.State {
	case tracker.StateOpen, tracker.StateClosed, tracker.StateMerged:
	default:
		t.State = tracker.StateOpen
	}
	return t, nil
}
