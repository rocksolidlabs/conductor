package github_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

var (
	issue42 = ref("octo-example/widgets", tracker.KindIssue, "42")
	target  = tracker.Target{NodeID: "I_kwDOFixture42", Kind: tracker.KindIssue, SpaceID: "4000001", Ref: issue42, URL: "https://github.com/octo-example/widgets/issues/42", State: tracker.StateOpen}
)

func (r *rig) commentCred(pub string, try int) tracker.Credential {
	return r.cred.ForWrite(tracker.Write{Kind: tracker.WriteComment, Publication: pub, Try: try, PostSha256: strings.Repeat("ab", 32)})
}

func (r *rig) closeCred(pub string, try int) tracker.Credential {
	return r.cred.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: pub, Try: try, Comment: "7700001"})
}

// commentAnswer is AddCommentDocument's answer.
func commentAnswer(id int64, author int64) map[string]any {
	return map[string]any{"data": map[string]any{"addComment": map[string]any{"commentEdge": map[string]any{"node": map[string]any{
		"databaseId": id, "url": "https://github.com/octo-example/widgets/issues/42#issuecomment-" + strconv.FormatInt(id, 10), "createdAt": "2026-11-10T12:00:01Z",
		"author": map[string]any{"login": "mona-example", "databaseId": author},
	}}}}}
}

// answerAll answers each of the three documents.
func (r *rig) answerAll() {
	r.graphql(func(g githubtest.GraphQL) (int, any) {
		switch g.Query {
		case github.ResolveDocument:
			return 200, issueNode(g.Variables["id"].(string), 42, 4000001, "octo-example/widgets", "OPEN")
		case github.AddCommentDocument:
			return 200, commentAnswer(7700001, 9000001)
		case github.CloseIssueDocument:
			return 200, map[string]any{"data": map[string]any{"closeIssue": map[string]any{"issue": map[string]any{"id": g.Variables["issueId"], "state": "CLOSED", "stateReason": g.Variables["stateReason"]}}}}
		}
		return 200, map[string]any{"data": nil, "errors": []any{map[string]any{"type": "UNKNOWN_DOCUMENT"}}}
	})
}

func TestDocumentsAreConstants(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	bodies := []string{"plain", `say "hi"`, `back\slash`, "<b>&amp;</b> > <", "line one\nline two\r\n", "ends with the marker\n\n<!-- conductor-publication:p1 -->", `"}}, "query": "mutation { x }`}
	nodes := []string{"I_kwDOFixture42", `I_"quoted"`, `I_\back`, "I_<&>"}
	for _, n := range nodes {
		tg := target
		tg.NodeID = n
		if _, err := r.a.Resolve(ctx, r.cred, n); err != nil && !errors.Is(err, tracker.ErrUnsupported) {
			t.Fatal(err)
		}
		for _, b := range bodies {
			if _, err := r.a.Comment(ctx, r.commentCred("p1", 1), tg, b); err != nil {
				t.Fatal(err)
			}
		}
		for _, reason := range []string{tracker.ReasonCompleted, tracker.ReasonNotPlanned} {
			if err := r.a.Close(ctx, r.closeCred("p1", 1), tg, reason); err != nil {
				t.Fatal(err)
			}
		}
	}
	seen := map[string]int{}
	for _, q := range r.fake.Requests() {
		g, err := githubtest.ParseGraphQL(q.Body)
		if err != nil {
			t.Fatal(err)
		}
		switch g.Query {
		case github.ResolveDocument:
			if len(g.Variables) != 1 || !contains(nodes, g.Variables["id"]) {
				t.Fatalf("Resolve's variables %v", g.Variables)
			}
		case github.AddCommentDocument:
			if len(g.Variables) != 2 || !contains(nodes, g.Variables["subjectId"]) || !contains(bodies, g.Variables["body"]) {
				t.Fatalf("AddComment's variables %v", g.Variables)
			}
		case github.CloseIssueDocument:
			if len(g.Variables) != 2 || !contains(nodes, g.Variables["issueId"]) || (g.Variables["stateReason"] != "COMPLETED" && g.Variables["stateReason"] != "NOT_PLANNED") {
				t.Fatalf("CloseIssue's variables %v", g.Variables)
			}
		default:
			t.Fatalf("a document that is not a constant: %q", g.Query)
		}
		seen[g.Query]++
	}
	if seen[github.ResolveDocument] != len(nodes) || seen[github.AddCommentDocument] != len(nodes)*len(bodies) || seen[github.CloseIssueDocument] != 2*len(nodes) {
		t.Fatalf("documents sent %v", seen)
	}

	// The three documents' SHA-256, held in a golden: a changed document
	// is a reviewed change.
	golden, err := os.ReadFile("testdata/graphql.sha256")
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for _, d := range []struct{ name, doc string }{{"ResolveDocument", github.ResolveDocument}, {"AddCommentDocument", github.AddCommentDocument}, {"CloseIssueDocument", github.CloseIssueDocument}} {
		sum := sha256.Sum256([]byte(d.doc))
		fmt.Fprintf(&got, "%s  %s\n", hex.EncodeToString(sum[:]), d.name)
	}
	if got.String() != string(golden) {
		t.Fatalf("the documents changed; their digests are now:\n%s", got.String())
	}
}

func contains(list []string, v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestMarker(t *testing.T) {
	a := github.New(github.Options{})
	if got := a.Marker("pub_01-AbC"); got != "<!-- conductor-publication:pub_01-AbC -->" {
		t.Fatalf("Marker %q", got)
	}
	if len(a.Marker(strings.Repeat("a", 64))) > tracker.MaxMarker {
		t.Fatal("past MaxMarker")
	}
	for _, bad := range []string{"", "a b", "x-->y", "é", strings.Repeat("a", 65), "a\n"} {
		if a.Marker(bad) != "" {
			t.Errorf("Marker(%q) marked", bad)
		}
	}
	if a.System() != "github" {
		t.Fatal("System")
	}
}

func TestWriteTagNeverSentWithAToken(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	const pub = "pubTAG123"
	cc := r.cred.ForWrite(tracker.Write{Kind: tracker.WriteComment, Publication: pub, Try: 4, PostSha256: strings.Repeat("9f", 32)})
	if _, err := r.a.Comment(ctx, cc, target, "the outcome"); err != nil {
		t.Fatal(err)
	}
	cl := r.cred.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: pub, Try: 5, Comment: "7712345"})
	if err := r.a.Close(ctx, cl, target, tracker.ReasonCompleted); err != nil {
		t.Fatal(err)
	}
	for _, q := range r.fake.Requests() {
		all := string(q.Body) + q.RawQuery + q.Path
		for k, vs := range q.Header {
			all += k + strings.Join(vs, "")
		}
		for _, tag := range []string{pub, strings.Repeat("9f", 32), "7712345"} {
			if strings.Contains(all, tag) {
				t.Fatalf("the tag %q reached GitHub: %s", tag, all)
			}
		}
	}
	log := r.log.String()
	if !strings.Contains(log, "publication="+pub+" try=4") || !strings.Contains(log, "write=close publication="+pub+" try=5") || strings.Contains(log, "the outcome") {
		t.Fatalf("the writes' lines: %s", log)
	}

	// An untagged credential, or one tagged for the other write, is
	// refused before any request.
	before := len(r.fake.Requests())
	for _, c := range []tracker.Credential{r.cred, cl, r.cred.ForWrite(tracker.Write{Kind: tracker.WriteComment})} {
		if _, err := r.a.Comment(ctx, c, target, "x"); !errors.Is(err, tracker.ErrUnsupported) || !tracker.NothingWritten(err) {
			t.Fatalf("Comment: %v", err)
		}
	}
	for _, c := range []tracker.Credential{r.cred, cc} {
		if err := r.a.Close(ctx, c, target, tracker.ReasonCompleted); !errors.Is(err, tracker.ErrUnsupported) || !tracker.NothingWritten(err) {
			t.Fatalf("Close: %v", err)
		}
	}
	if len(r.fake.Requests()) != before {
		t.Fatal("a refused write made a request")
	}
}

func TestResolve(t *testing.T) {
	r := newRig(t)
	var answer map[string]any
	r.graphql(func(githubtest.GraphQL) (int, any) { return 200, answer })

	answer = issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", "OPEN")
	tg, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42")
	if err != nil || tg != target {
		t.Fatalf("issue %+v %v", tg, err)
	}

	answer = map[string]any{"data": map[string]any{"node": map[string]any{"__typename": "PullRequest", "id": "PR_kwDOFixture43", "number": 43, "url": "https://github.com/octo-example/widgets/pull/43", "state": "MERGED", "repository": map[string]any{"databaseId": 4000001, "nameWithOwner": "octo-example/widgets"}}}}
	tg, err = r.a.Resolve(ctx, r.cred, "PR_kwDOFixture43")
	if err != nil || tg.Kind != tracker.KindChange || tg.State != tracker.StateMerged || tg.Ref.Kind != tracker.KindChange || tg.URL != "https://github.com/octo-example/widgets/pull/43" {
		t.Fatalf("pull request %+v %v", tg, err)
	}

	// Renamed: the new name, the repository's id unchanged.
	answer = issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets-renamed", "CLOSED")
	tg, err = r.a.Resolve(ctx, r.cred, "I_kwDOFixture42")
	if err != nil || tg.SpaceID != "4000001" || tg.Ref.Space != "octo-example/widgets-renamed" || tg.State != tracker.StateClosed {
		t.Fatalf("renamed %+v %v", tg, err)
	}

	for name, a := range map[string]map[string]any{
		"another type": {"data": map[string]any{"node": map[string]any{"__typename": "Commit", "id": "C_x"}}},
		"null":         {"data": map[string]any{"node": nil}, "errors": []any{map[string]any{"type": "NOT_FOUND"}}},
		"no data":      {"data": nil},
	} {
		answer = a
		if _, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); !errors.Is(err, tracker.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	answer = map[string]any{"data": map[string]any{"node": nil}, "errors": []any{map[string]any{"type": "FORBIDDEN"}}}
	if _, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); !errors.Is(err, tracker.ErrForbidden) {
		t.Errorf("forbidden: %v", err)
	}
	// An answer for another node than asked is not taken.
	answer = issueNode("I_kwDOOther", 42, 4000001, "octo-example/widgets", "OPEN")
	if _, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); err == nil {
		t.Error("another node's answer taken")
	}
}

// comments serves the comments of issue 42 on pages of 100: page p's
// comments from fill, and a Link to a next page while p < pages.
func (r *rig) comments(pages int, fill func(page, i int) map[string]any) {
	r.fake.Handle(http.MethodGet, api, "/repos/octo-example/widgets/issues/42/comments", func(w http.ResponseWriter, req *http.Request) {
		p, _ := strconv.Atoi(req.URL.Query().Get("page"))
		var list []map[string]any
		if p <= pages {
			for i := 0; i < 100; i++ {
				if c := fill(p, i); c != nil {
					list = append(list, c)
				}
			}
		}
		// GitHub's Link: a next while there is one, a prev past page 1.
		var links []string
		if p < pages {
			links = append(links, fmt.Sprintf(`<https://api.github.com/repositories/4000001/issues/42/comments?page=%d>; rel="next"`, p+1))
		}
		if p > 1 {
			links = append(links, fmt.Sprintf(`<https://api.github.com/repositories/4000001/issues/42/comments?page=%d>; rel="prev"`, p-1))
		}
		if len(links) > 0 {
			w.Header().Set("Link", strings.Join(links, ", "))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(githubtest.Body(list))
	})
}

func comment(id int, author int, body string) map[string]any {
	return map[string]any{"id": id, "html_url": fmt.Sprintf("https://github.com/octo-example/widgets/issues/42#issuecomment-%d", id), "body": body, "created_at": "2026-11-10T12:00:01Z",
		"user": map[string]any{"login": fmt.Sprintf("user%d", author), "id": author}}
}

func TestCommentAndFind(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	marker := r.a.Marker("p1")
	posted := "Fixed in the delivery.\n\n" + marker
	c, err := r.a.Comment(ctx, r.commentCred("p1", 1), target, posted)
	if err != nil || c.ID != "7700001" || c.AuthorID != "9000001" || c.Author != "mona-example" || c.Check() != nil {
		t.Fatalf("Comment %+v %v", c, err)
	}
	// Sent to the node id, never to a path.
	for _, q := range r.fake.Requests() {
		if q.Method != http.MethodPost || q.Path != "/graphql" {
			t.Fatalf("a write by path: %s %s", q.Method, q.Path)
		}
	}
	if r.fake.Writes("graphql AddComment") != 1 {
		t.Fatalf("writes %d", r.fake.Writes("graphql AddComment"))
	}

	m := tracker.Match{Marker: marker, BodySha256: tracker.BodyDigest(posted), AuthorID: "9000001"}
	filler := func(p, i int) map[string]any { return comment(p*1000+i, 9000002, "an unrelated comment") }
	find := func() (tracker.Comment, tracker.Found, error) {
		return r.a.FindComment(ctx, r.cred, target, now, m)
	}

	// On page 1, the body as GitHub returns it (CRLF, trailing spaces).
	r.comments(1, func(p, i int) map[string]any {
		if i == 3 {
			return comment(7700001, 9000001, strings.ReplaceAll(posted, "\n", "\r\n")+"  \r\n")
		}
		if i > 10 {
			return nil
		}
		return filler(p, i)
	})
	got, found, err := find()
	if err != nil || found != tracker.FoundYes || got.ID != "7700001" || got.AuthorID != "9000001" {
		t.Fatalf("page 1: %+v %v %v", got, found, err)
	}
	q := r.fake.Requests()[len(r.fake.Requests())-1]
	if !strings.Contains(q.RawQuery, "since=2026-11-10T12%3A00%3A00Z") || !strings.Contains(q.RawQuery, "per_page=100") {
		t.Fatalf("listed as %s", q.RawQuery)
	}

	// On page 7.
	r.comments(7, func(p, i int) map[string]any {
		if p == 7 && i == 50 {
			return comment(7700001, 9000001, posted)
		}
		return filler(p, i)
	})
	if got, found, err := find(); err != nil || found != tracker.FoundYes || got.ID != "7700001" {
		t.Fatalf("page 7: %+v %v %v", got, found, err)
	}

	// By another account: ignored when AuthorID is set, found and named
	// when it is ''.
	r.comments(1, func(p, i int) map[string]any {
		if i == 0 {
			return comment(7700009, 9000077, posted)
		}
		return nil
	})
	if _, found, err := find(); err != nil || found != tracker.FoundNo {
		t.Fatalf("another author: %v %v", found, err)
	}
	m.AuthorID = ""
	if got, found, err := find(); err != nil || found != tracker.FoundYes || got.AuthorID != "9000077" || got.Author != "user9000077" {
		t.Fatalf("any author: %+v %v %v", got, found, err)
	}

	// The marker beside other bytes is not a match.
	r.comments(1, func(p, i int) map[string]any {
		if i == 0 {
			return comment(7700010, 9000001, "Edited on GitHub.\n\n"+marker)
		}
		return nil
	})
	if _, found, err := find(); err != nil || found != tracker.FoundNo {
		t.Fatalf("other bytes: %v %v", found, err)
	}

	// A full page with no Link at all may have a next: it is read.
	r.fake.Handle(http.MethodGet, api, "/repos/octo-example/widgets/issues/42/comments", func(w http.ResponseWriter, req *http.Request) {
		var list []map[string]any
		switch req.URL.Query().Get("page") {
		case "1":
			for i := 0; i < 100; i++ {
				list = append(list, filler(1, i))
			}
		case "2":
			list = append(list, comment(7700001, 9000077, posted))
		}
		_, _ = w.Write(githubtest.Body(list))
	})
	if got, found, err := find(); err != nil || found != tracker.FoundYes || got.ID != "7700001" {
		t.Fatalf("a full page with no Link: %+v %v %v", got, found, err)
	}

	// Ten full pages and an eleventh: not known.
	r.comments(11, filler)
	if _, found, err := find(); err != nil || found != tracker.FoundUnknown {
		t.Fatalf("eleven pages: %v %v", found, err)
	}
	// Exactly ten full pages and no next: the whole window, none.
	r.comments(10, filler)
	if _, found, err := find(); err != nil || found != tracker.FoundNo {
		t.Fatalf("ten pages: %v %v", found, err)
	}

	// The node moved to another repository, or became another kind: the
	// listing is refused.
	moved := target
	moved.SpaceID = "4000999"
	if _, found, err := r.a.FindComment(ctx, r.cred, moved, now, m); !errors.Is(err, tracker.ErrTargetChanged) || found != tracker.FoundUnknown {
		t.Fatalf("moved: %v %v", found, err)
	}
	if r.fake.Writes("graphql AddComment") != 1 {
		t.Fatal("a find wrote")
	}
}

func TestWriteAnswers(t *testing.T) {
	type outcome int
	const (
		proven outcome = iota
		unknown
	)
	null := func(typ string) map[string]any {
		return map[string]any{"data": map[string]any{"addComment": nil}, "errors": []any{map[string]any{"type": typ, "message": "no"}}}
	}
	cases := []struct {
		name   string
		status int
		body   any
		want   outcome
	}{
		{"401", 401, `{"message":"Bad credentials"}`, proven},
		{"403", 403, `{"message":"Forbidden"}`, proven},
		{"NOT_FOUND", 200, null("NOT_FOUND"), proven},
		{"FORBIDDEN", 200, null("FORBIDDEN"), proven},
		{"UNPROCESSABLE", 200, null("UNPROCESSABLE"), proven},
		{"another error type", 200, null("INTERNAL"), unknown},
		{"no data, no error", 200, map[string]any{"data": nil}, unknown},
		{"partial data", 200, map[string]any{"data": commentAnswer(1, 2)["data"], "errors": []any{map[string]any{"type": "NOT_FOUND"}}}, unknown},
		{"an edge without its node", 200, map[string]any{"data": map[string]any{"addComment": map[string]any{"commentEdge": nil}}}, unknown},
		{"unreadable", 200, "{not json", unknown},
		{"404", 404, "", unknown},
		{"429", 429, "", unknown},
		{"500", 500, "", unknown},
		{"502", 502, "", unknown},
	}
	for _, c := range cases {
		r := newRig(t)
		r.reply(http.MethodPost, "/graphql", c.status, c.body)
		_, err := r.a.Comment(ctx, r.commentCred("p1", 1), target, "x")
		cerr := r.a.Close(ctx, r.closeCred("p1", 1), target, tracker.ReasonCompleted)
		for _, e := range []error{err, cerr} {
			if c.name == "partial data" && e == cerr {
				continue // the close's answer has no addComment
			}
			if c.name == "an edge without its node" && e == cerr {
				continue
			}
			got := unknown
			if tracker.NothingWritten(e) {
				got = proven
			}
			if e == nil || got != c.want || (got == unknown && !errors.Is(e, tracker.ErrWriteUnknown)) {
				t.Errorf("%s: %v", c.name, e)
			}
		}
	}
	// A call ended while the write was held: not known.
	r := newRig(t)
	r.answerAll()
	err := held(t, r, api, "/graphql", func(c context.Context) error {
		_, err := r.a.Comment(c, r.commentCred("p1", 1), target, "x")
		return err
	})
	if tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("held: %v", err)
	}
}

func TestCloseReason(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	for reason, want := range map[string]string{tracker.ReasonCompleted: "COMPLETED", tracker.ReasonNotPlanned: "NOT_PLANNED"} {
		if err := r.a.Close(ctx, r.closeCred("p1", 1), target, reason); err != nil {
			t.Fatal(err)
		}
		q := r.fake.Requests()[len(r.fake.Requests())-1]
		g, _ := githubtest.ParseGraphQL(q.Body)
		if q.Path != "/graphql" || g.Query != github.CloseIssueDocument || g.Variables["issueId"] != target.NodeID || g.Variables["stateReason"] != want {
			t.Fatalf("closed as %s %v", q.Path, g.Variables)
		}
	}
	before := len(r.fake.Requests())
	change := target
	change.Kind = tracker.KindChange
	if err := r.a.Close(ctx, r.closeCred("p1", 1), change, tracker.ReasonCompleted); !errors.Is(err, tracker.ErrUnsupported) || !tracker.NothingWritten(err) {
		t.Fatalf("a change closed: %v", err)
	}
	if err := r.a.Close(ctx, r.closeCred("p1", 1), target, "duplicate"); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("another reason: %v", err)
	}
	if len(r.fake.Requests()) != before || r.fake.Writes("graphql CloseIssue") != 2 {
		t.Fatal("a refused close made a request")
	}
}
