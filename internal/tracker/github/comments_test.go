package github_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

var (
	issue42 = ref("octo-example/widgets", tracker.KindIssue, "42")
	target  = tracker.Target{NodeID: "I_kwDOFixture42", Kind: tracker.KindIssue, SpaceID: "4000001", Ref: holding(issue42, "4000001"), URL: "https://github.com/octo-example/widgets/issues/42", State: tracker.StateOpen}
)

// holding is a Ref holding the repository's identity, as a read returns it.
func holding(r tracker.Ref, id tracker.RemoteID) tracker.Ref {
	r.SpaceID = id
	return r
}

// posted is text as a publication posts it: the text, a blank line and the
// publication's marker (text that already holds the marker as it is).
func posted(pub, text string) string {
	marker := "<!-- conductor-publication:" + pub + " -->"
	if strings.Contains(text, marker) {
		return text
	}
	return text + "\n\n" + marker
}

// tagFor is a comment's write tag for body: its PostSha256 the SHA-256 of
// the exact bytes posted.
func tagFor(pub string, try int, body string) tracker.Write {
	sum := sha256.Sum256([]byte(body))
	return tracker.Write{Kind: tracker.WriteComment, Publication: pub, Try: try, PostSha256: hex.EncodeToString(sum[:])}
}

// post posts text as publication pub's try: its bytes posted(pub, text),
// under a credential tagged with their digest.
func (r *rig) post(c context.Context, pub string, try int, t tracker.Target, text string) (tracker.Comment, error) {
	body := posted(pub, text)
	return r.a.Comment(c, r.cred.ForWrite(tagFor(pub, try, body)), t, body)
}

func (r *rig) closeCred(pub string, try int) tracker.Credential {
	return r.cred.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: pub, Try: try, Comment: "7700001"})
}

// commentAnswer is AddCommentDocument's answer, its id a BigInt's JSON
// string as GitHub gives fullDatabaseId (captured 2026-11-10).
// It names the target's node (the subject) and repository (4000001).
func commentAnswer(id int64, author int64) map[string]any {
	return commentOn(target.NodeID, "4000001", id, author)
}

// commentOn is AddCommentDocument's answer for a comment on subject in the
// repository repoID.
func commentOn(subject, repoID string, id int64, author int64) map[string]any {
	return map[string]any{"data": map[string]any{"addComment": map[string]any{"subject": map[string]any{"id": subject}, "commentEdge": map[string]any{"node": map[string]any{
		"fullDatabaseId": strconv.FormatInt(id, 10), "url": "https://github.com/octo-example/widgets/issues/42#issuecomment-" + strconv.FormatInt(id, 10), "createdAt": "2026-11-10T12:00:01Z",
		"repository": map[string]any{"fullDatabaseId": repoID},
		"author":     map[string]any{"login": "mona-example", "databaseId": author},
	}}}}}
}

// closeAnswer is CloseIssueDocument's answer: the issue id, CLOSED, in the
// repository repoID.
func closeAnswer(id, reason any, repoID string) map[string]any {
	return map[string]any{"data": map[string]any{"closeIssue": map[string]any{"issue": map[string]any{"id": id, "state": "CLOSED", "stateReason": reason, "repository": map[string]any{"fullDatabaseId": repoID}}}}}
}

// answerAll answers each of the three documents.
func (r *rig) answerAll() {
	r.graphql(func(g githubtest.GraphQL) (int, any) {
		switch g.Query {
		case github.ResolveDocument:
			return 200, issueNode(g.Variables["id"].(string), 42, 4000001, "octo-example/widgets", "OPEN")
		case github.AddCommentDocument:
			return 200, commentOn(g.Variables["subjectId"].(string), "4000001", 6700000001, 9000001)
		case github.CloseIssueDocument:
			return 200, closeAnswer(g.Variables["issueId"], g.Variables["stateReason"], "4000001")
		}
		return 200, map[string]any{"data": nil, "errors": []any{map[string]any{"type": "UNKNOWN_DOCUMENT"}}}
	})
}

func TestDocumentsAreConstants(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	var bodies []string
	for _, b := range []string{"plain", `say "hi"`, `back\slash`, "<b>&amp;</b> > <", "line one\nline two", "ends with the marker\n\n<!-- conductor-publication:p1 -->", `"}}, "query": "mutation { x }`} {
		bodies = append(bodies, posted("p1", b))
	}
	nodes := []string{"I_kwDOFixture42", `I_"quoted"`, `I_\back`, "I_<&>"}
	for _, n := range nodes {
		tg := target
		tg.NodeID = n
		if _, err := r.a.Resolve(ctx, r.cred, n); err != nil && !errors.Is(err, tracker.ErrUnsupported) {
			t.Fatal(err)
		}
		for _, b := range bodies {
			if _, err := r.post(ctx, "p1", 1, tg, b); err != nil {
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
	body := posted(pub, "the outcome")
	tag := tagFor(pub, 4, body)
	cc := r.cred.ForWrite(tag)
	if _, err := r.a.Comment(ctx, cc, target, body); err != nil {
		t.Fatal(err)
	}
	cl := r.cred.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: pub, Try: 5, Comment: "7712345"})
	if err := r.a.Close(ctx, cl, target, tracker.ReasonCompleted); err != nil {
		t.Fatal(err)
	}
	for _, q := range r.fake.Requests() {
		// The posted bytes hold the publication's marker, which names it
		// by design; nothing else of the tag is sent.
		all := string(q.Body) + q.RawQuery + q.Path
		if g, err := githubtest.ParseGraphQL(q.Body); err == nil && g.Variables["body"] == body {
			delete(g.Variables, "body")
			all = g.Query + fmt.Sprint(g.Variables) + q.RawQuery + q.Path
		}
		for k, vs := range q.Header {
			all += k + strings.Join(vs, "")
		}
		for _, tag := range []string{pub, tag.PostSha256, "7712345"} {
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

	answer = map[string]any{"data": map[string]any{"node": map[string]any{"__typename": "PullRequest", "id": "PR_kwDOFixture43", "number": 43, "url": "https://github.com/octo-example/widgets/pull/43", "state": "MERGED", "repository": map[string]any{"fullDatabaseId": "4000001", "nameWithOwner": "octo-example/widgets"}}}}
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
	// A repository's id past 2^31, given as a number or as GitHub's BigInt
	// string: read whole either way.
	for _, id := range []any{"9876543210123", 9876543210123} {
		answer = issueNode("I_kwDOFixture42", 42, 0, "octo-example/widgets", "OPEN")
		answer["data"].(map[string]any)["node"].(map[string]any)["repository"].(map[string]any)["fullDatabaseId"] = id
		if tg, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); err != nil || tg.SpaceID != "9876543210123" {
			t.Errorf("repository id %v: %+v %v", id, tg, err)
		}
	}
	// A repository without its id is not taken.
	answer = issueNode("I_kwDOFixture42", 42, 0, "octo-example/widgets", "OPEN")
	answer["data"].(map[string]any)["node"].(map[string]any)["repository"].(map[string]any)["fullDatabaseId"] = nil
	if _, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); err == nil {
		t.Error("a repository without its id taken")
	}
	// A GraphQL rate limit: GitHub's 200 with RATE_LIMITED and its reset.
	r.fake.Handle(http.MethodPost, api, "/graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10))
		_, _ = w.Write([]byte(`{"data":{"node":null},"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`))
	})
	var rl *tracker.ErrRateLimited
	if _, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); !errors.As(err, &rl) || !rl.Reset.Equal(now.Add(10*time.Minute)) {
		t.Errorf("a GraphQL rate limit: %v", err)
	}
	r.graphql(func(githubtest.GraphQL) (int, any) { return 200, answer })
	// An answer for another node than asked is not taken.
	answer = issueNode("I_kwDOOther", 42, 4000001, "octo-example/widgets", "OPEN")
	if _, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); err == nil {
		t.Error("another node's answer taken")
	}
}

// comments serves the comments of issue 42 on pages of 100: page p's
// comments from fill, and a Link to a next page while p < pages.
func (r *rig) comments(pages int, fill func(page, i int) map[string]any) {
	r.fake.Handle(http.MethodGet, api, "/repositories/4000001/issues/42/comments", func(w http.ResponseWriter, req *http.Request) {
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
	c, err := r.post(ctx, "p1", 1, target, posted)
	// A comment's id is past a GraphQL Int's 2^31 (captured: ten digits),
	// read whole from fullDatabaseId.
	if err != nil || c.ID != "6700000001" || c.AuthorID != "9000001" || c.Author != "mona-example" || c.Check() != nil {
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
	r.fake.Handle(http.MethodGet, api, "/repositories/4000001/issues/42/comments", func(w http.ResponseWriter, req *http.Request) {
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

// outcome is one of the write table's three answers.
type outcome string

const (
	made    outcome = "made"
	nothing outcome = "nothing written"
	unknown outcome = "not known"
)

// sortedAs is the outcome a write's error says, checked in its words too.
func sortedAs(t *testing.T, name string, err error) outcome {
	t.Helper()
	switch {
	case err == nil:
		return made
	case tracker.NothingWritten(err):
		if errors.Is(err, tracker.ErrWriteUnknown) || !strings.HasSuffix(err.Error(), ": nothing was written") {
			t.Errorf("%s: words %q", name, err.Error())
		}
		return nothing
	case errors.Is(err, tracker.ErrWriteUnknown):
		if !strings.HasSuffix(err.Error(), ": not known whether the write was made") {
			t.Errorf("%s: words %q", name, err.Error())
		}
		return unknown
	}
	t.Errorf("%s: an error that is not sorted: %v", name, err)
	return ""
}

// fakeClock is the clock of the requests' own deadlines: a deadline
// passes when the test says.
type fakeClock struct {
	mu sync.Mutex
	fs []func()
}

func (c *fakeClock) after(_ time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fs = append(c.fs, f)
	return func() bool { return true }
}

// pass makes every deadline set so far pass.
func (c *fakeClock) pass() {
	c.mu.Lock()
	fs := c.fs
	c.fs = nil
	c.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

// TestWriteTable walks the write table (sortWrite's comment): every way a
// write can end, for a comment and for a close, sorted into made, nothing
// written or not known, said in the error's words, and never sent twice.
func TestWriteTable(t *testing.T) {
	null := func(field, typ string) string {
		return `{"data":{"` + field + `":null},"errors":[{"type":"` + typ + `","message":"no"}]}`
	}
	closed := func(id, state string) string {
		return `{"data":{"closeIssue":{"issue":{"id":"` + id + `","state":"` + state + `","stateReason":"COMPLETED","repository":{"fullDatabaseId":"4000001"}}}}}`
	}
	comment := `{"data":{"addComment":{"subject":{"id":"I_kwDOFixture42"},"commentEdge":{"node":{"fullDatabaseId":"6700000001","url":"https://github.com/octo-example/widgets/issues/42#issuecomment-6700000001","createdAt":"2026-11-10T12:00:01Z","repository":{"fullDatabaseId":"4000001"},"author":{"login":"mona-example","databaseId":9000001}}}}}}`
	// An answer per document: the comment's and the close's.
	type ans struct {
		status         int
		comment, close string
		header         []string
	}
	both := func(status int, body string, header ...string) ans { return ans{status, body, body, header} }
	rows := []struct {
		name string
		ans  ans
		// serve, when set, serves the route instead of ans.
		serve          http.HandlerFunc
		comment, cls   outcome
		rateLimited    bool
		requestsAtMost int
	}{
		{name: "made", ans: ans{200, comment, closed(target.NodeID, "CLOSED"), nil}, comment: made, cls: made},
		{name: "a reset after the request was read", serve: reset, comment: unknown, cls: unknown},
		{name: "301", ans: both(301, "", "Location", "https://api.github.com/elsewhere"), comment: unknown, cls: unknown},
		{name: "302", ans: both(302, "", "Location", "https://api.github.com/elsewhere"), comment: unknown, cls: unknown},
		{name: "303", ans: both(303, "", "Location", "https://api.github.com/elsewhere"), comment: unknown, cls: unknown},
		{name: "307", ans: both(307, "", "Location", "https://api.github.com/graphql"), comment: unknown, cls: unknown},
		{name: "308", ans: both(308, "", "Location", "https://api.github.com/graphql"), comment: unknown, cls: unknown},
		{name: "300", ans: both(300, ""), comment: unknown, cls: unknown},
		{name: "304", ans: both(304, ""), comment: unknown, cls: unknown},
		{name: "307 with no Location", ans: both(307, ""), comment: unknown, cls: unknown},
		{name: "201 with the data", ans: ans{201, comment, closed(target.NodeID, "CLOSED"), nil}, comment: unknown, cls: unknown},
		{name: "204", ans: both(204, ""), comment: unknown, cls: unknown},
		{name: "401", ans: both(401, `{"message":"Bad credentials"}`), comment: nothing, cls: nothing},
		{name: "403", ans: both(403, `{"message":"Resource not accessible by personal access token"}`), comment: nothing, cls: nothing},
		{name: "403 a secondary rate limit", ans: both(403, `{"message":"You have exceeded a secondary rate limit."}`, "X-RateLimit-Remaining", "4000"), comment: nothing, cls: nothing, rateLimited: true},
		{name: "403 the primary rate limit", ans: both(403, `{"message":"API rate limit exceeded"}`, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1762780000"), comment: nothing, cls: nothing, rateLimited: true},
		{name: "429", ans: both(429, "", "Retry-After", "30"), comment: nothing, cls: nothing, rateLimited: true},
		{name: "429 with nothing said", ans: both(429, ""), comment: nothing, cls: nothing, rateLimited: true},
		{name: "400", ans: both(400, `{"message":"Problems parsing JSON"}`), comment: unknown, cls: unknown},
		{name: "404", ans: both(404, ""), comment: unknown, cls: unknown},
		{name: "422", ans: both(422, ""), comment: unknown, cls: unknown},
		{name: "500", ans: both(500, ""), comment: unknown, cls: unknown},
		{name: "502", ans: both(502, ""), comment: unknown, cls: unknown},
		{name: "a 200 past its bound", ans: both(200, string(padded(github.MaxGraphQLAnswer+1))), comment: unknown, cls: unknown},
		{name: "a 200 that does not parse", ans: both(200, "{not json"), comment: unknown, cls: unknown},
		{name: "null beside NOT_FOUND", ans: ans{200, null("addComment", "NOT_FOUND"), null("closeIssue", "NOT_FOUND"), nil}, comment: nothing, cls: nothing},
		{name: "null beside FORBIDDEN", ans: ans{200, null("addComment", "FORBIDDEN"), null("closeIssue", "FORBIDDEN"), nil}, comment: nothing, cls: nothing},
		{name: "null beside UNPROCESSABLE", ans: ans{200, null("addComment", "UNPROCESSABLE"), null("closeIssue", "UNPROCESSABLE"), nil}, comment: nothing, cls: nothing},
		{name: "null beside RATE_LIMITED", ans: ans{200, null("addComment", "RATE_LIMITED"), null("closeIssue", "RATE_LIMITED"), []string{"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1762780000"}}, comment: nothing, cls: nothing, rateLimited: true},
		{name: "null beside another type", ans: ans{200, null("addComment", "INTERNAL"), null("closeIssue", "INTERNAL"), nil}, comment: unknown, cls: unknown},
		{name: "no data, no error", ans: both(200, `{"data":null}`), comment: unknown, cls: unknown},
		{name: "data with errors beside", ans: ans{200, strings.TrimSuffix(comment, "}") + `,"errors":[{"type":"NOT_FOUND"}]}`, strings.TrimSuffix(closed(target.NodeID, "CLOSED"), "}") + `,"errors":[{"type":"NOT_FOUND"}]}`, nil}, comment: unknown, cls: unknown},
		{name: "data that does not say what was written", ans: ans{200, `{"data":{"addComment":{"commentEdge":null}}}`, `{"data":{"closeIssue":{"issue":null}}}`, nil}, comment: unknown, cls: unknown},
		{name: "ids that are not ids; not the issue asked", ans: ans{200, strings.Replace(comment, `"6700000001"`, `"67e8"`, 1), closed("I_kwDOAnotherOne", "CLOSED"), nil}, comment: unknown, cls: unknown},
		{name: "no id; an issue not closed", ans: ans{200, strings.Replace(comment, `"fullDatabaseId":"6700000001",`, ``, 1), closed(target.NodeID, "OPEN"), nil}, comment: unknown, cls: unknown},
		{name: "another subject; another repository", ans: ans{200, strings.Replace(comment, `"I_kwDOFixture42"`, `"I_kwDOAnotherOne"`, 1), strings.Replace(closed(target.NodeID, "CLOSED"), `"4000001"`, `"4999999"`, 1), nil}, comment: unknown, cls: unknown},
		{name: "the comment in another repository; no repository", ans: ans{200, strings.Replace(comment, `{"fullDatabaseId":"4000001"}`, `{"fullDatabaseId":"4999999"}`, 1), strings.Replace(closed(target.NodeID, "CLOSED"), `,"repository":{"fullDatabaseId":"4000001"}`, ``, 1), nil}, comment: unknown, cls: unknown},
		{name: "no subject; a null repository", ans: ans{200, strings.Replace(comment, `"subject":{"id":"I_kwDOFixture42"},`, ``, 1), strings.Replace(closed(target.NodeID, "CLOSED"), `{"fullDatabaseId":"4000001"}`, `null`, 1), nil}, comment: unknown, cls: unknown},
	}
	for _, row := range rows {
		r := newRig(t)
		if row.serve != nil {
			r.fake.Handle(http.MethodPost, api, "/graphql", row.serve)
		} else {
			r.fake.Handle(http.MethodPost, api, "/graphql", func(w http.ResponseWriter, req *http.Request) {
				g, _ := githubtest.ParseGraphQL(r.fake.Requests()[len(r.fake.Requests())-1].Body)
				body := row.ans.comment
				if g.Query == github.CloseIssueDocument {
					body = row.ans.close
				}
				for i := 0; i+1 < len(row.ans.header); i += 2 {
					w.Header().Set(row.ans.header[i], row.ans.header[i+1])
				}
				w.WriteHeader(row.ans.status)
				_, _ = w.Write([]byte(body))
			})
		}
		_, cerr := r.post(ctx, "p1", 1, target, "x")
		kerr := r.a.Close(ctx, r.closeCred("p1", 1), target, tracker.ReasonCompleted)
		if got := sortedAs(t, row.name+" (comment)", cerr); got != row.comment {
			t.Errorf("%s: a comment %s: %v", row.name, got, cerr)
		}
		if got := sortedAs(t, row.name+" (close)", kerr); got != row.cls {
			t.Errorf("%s: a close %s: %v", row.name, got, kerr)
		}
		var rl *tracker.ErrRateLimited
		if row.rateLimited && (!errors.As(cerr, &rl) || !errors.As(kerr, &rl)) {
			t.Errorf("%s: not said as a rate limit: %v / %v", row.name, cerr, kerr)
		}
		if n := len(r.fake.Requests()); n != 2 {
			t.Errorf("%s: %d requests for two writes", row.name, n)
		}
	}

	// Refused before any request: nothing written, nothing asked.
	r := newRig(t)
	r.answerAll()
	bad := target
	bad.NodeID = ""
	if _, err := r.post(ctx, "p1", 1, bad, "x"); sortedAs(t, "a node id out of shape", err) != nothing {
		t.Errorf("a node id out of shape: %v", err)
	}
	if err := r.a.Close(ctx, r.closeCred("p1", 1), bad, tracker.ReasonCompleted); sortedAs(t, "a node id out of shape", err) != nothing {
		t.Errorf("a node id out of shape: %v", err)
	}
	offHost := target
	offHost.Ref.Host = "ghe.example.test@collector.example"
	if _, err := r.post(ctx, "p1", 1, offHost, "x"); sortedAs(t, "a host not admitted", err) != nothing {
		t.Errorf("a host not admitted: %v", err)
	}
	if n := len(r.fake.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}

	// The call had ended before the request was made: nothing written.
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.post(ended, "p1", 1, target, "x"); sortedAs(t, "a call ended first", err) != nothing {
		t.Errorf("a call ended before the request: %v", err)
	}
	// The base transport was handed the request and failed (here its
	// dial): what it sent is not known (TestWriteUnknownWhenTheBaseFails).
	dead := github.New(github.Options{Logger: quiet, Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no route")
	}}})
	if _, err := dead.Comment(ctx, r.cred.ForWrite(tagFor("p1", 1, posted("p1", "x"))), target, posted("p1", "x")); sortedAs(t, "a dial that failed", err) != unknown || !errors.Is(err, tracker.ErrUnavailable) {
		t.Errorf("a dial that failed: %v", err)
	}

	// A connection was had and no answer came: the call ended while the
	// write was held at GitHub, or the request's own deadline passed.
	r = newRig(t)
	r.answerAll()
	err := held(t, r, api, "/graphql", func(c context.Context) error {
		_, err := r.post(c, "p1", 1, target, "x")
		return err
	})
	if sortedAs(t, "the call ended while held", err) != unknown || !errors.Is(err, context.Canceled) {
		t.Errorf("the call ended while held: %v", err)
	}
	clock := &fakeClock{}
	github.SetClock(r.a, clock.after)
	for _, write := range []func() error{
		func() error { _, err := r.post(ctx, "p1", 1, target, "x"); return err },
		func() error { return r.a.Close(ctx, r.closeCred("p1", 1), target, tracker.ReasonCompleted) },
	} {
		gate := r.fake.Hold(http.MethodPost, api, "/graphql")
		done := make(chan error, 1)
		go func() { done <- write() }()
		<-gate.Reached()
		clock.pass()
		err := <-done
		gate.Open()
		if sortedAs(t, "the request's deadline", err) != unknown || !strings.Contains(err.Error(), "timed out") || errors.Is(err, context.Canceled) {
			t.Errorf("the request's own deadline: %v", err)
		}
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

// FindComment answers no only from a listing that cannot have skipped a
// comment: when the list moved between two of its pages (a comment before
// the boundary deleted, so the next page starts one later), a match past
// the boundary is either found or the answer is not known, never no.
func TestFindNoOnlyFromAWholeListing(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	marker := r.a.Marker("p1")
	posted := "Fixed in the delivery.\n\n" + marker
	m := tracker.Match{Marker: marker, BodySha256: tracker.BodyDigest(posted), AuthorID: "9000001"}
	var mu sync.Mutex
	list := make([]map[string]any, 0, 150)
	for i := 0; i < 150; i++ {
		if i == 100 {
			list = append(list, comment(7700001, 9000001, posted))
			continue
		}
		list = append(list, comment(7600000+i, 9000002, "an unrelated comment"))
	}
	deleted := false
	r.fake.Handle(http.MethodGet, api, "/repositories/4000001/issues/42/comments", func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p, _ := strconv.Atoi(req.URL.Query().Get("page"))
		if p == 2 && !deleted {
			// The first comment is deleted between the two pages' reads.
			list, deleted = list[1:], true
		}
		lo, hi := (p-1)*100, p*100
		lo, hi = min(lo, len(list)), min(hi, len(list))
		var links []string
		if hi < len(list) {
			links = append(links, fmt.Sprintf(`<https://api.github.com/repositories/4000001/issues/42/comments?page=%d>; rel="next"`, p+1))
		}
		if p > 1 {
			links = append(links, fmt.Sprintf(`<https://api.github.com/repositories/4000001/issues/42/comments?page=%d>; rel="prev"`, p-1))
		}
		if len(links) > 0 {
			w.Header().Set("Link", strings.Join(links, ", "))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(githubtest.Body(list[lo:hi]))
	})
	got, found, err := r.a.FindComment(ctx, r.cred, target, now, m)
	if found == tracker.FoundNo {
		t.Fatalf("no, while the matching comment is on the issue: %+v %v", got, err)
	}
	if found == tracker.FoundYes && got.ID != "7700001" {
		t.Fatalf("found %+v", got)
	}

	// A comment that matches but whose ids cannot be read makes the answer
	// not known, never no.
	r.comments(1, func(p, i int) map[string]any {
		if i == 0 {
			c := comment(7700001, 9000001, posted)
			c["id"] = -1
			return c
		}
		return nil
	})
	if _, found, _ := r.a.FindComment(ctx, r.cred, target, now, tracker.Match{Marker: marker, BodySha256: tracker.BodyDigest(posted)}); found != tracker.FoundUnknown {
		t.Fatalf("an unreadable match: %v", found)
	}
	// So does one whose author is not given.
	r.comments(1, func(p, i int) map[string]any {
		if i == 0 {
			c := comment(7700001, 9000001, posted)
			c["user"] = nil
			return c
		}
		return nil
	})
	if _, found, _ := r.a.FindComment(ctx, r.cred, target, now, m); found != tracker.FoundUnknown {
		t.Fatalf("a match with no author: %v", found)
	}
}

// Resolve answers only what it read: a node answered with errors beside it
// (a field that failed is null and its error listed) or with a state GitHub
// gives no such item is an answer Conductor cannot read, never one whose
// state is taken to be open.
func TestResolveAnswersOnlyWhatItRead(t *testing.T) {
	r := newRig(t)
	var answer map[string]any
	r.graphql(func(githubtest.GraphQL) (int, any) { return 200, answer })
	partial := issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", "OPEN")
	partial["data"].(map[string]any)["node"].(map[string]any)["state"] = nil
	partial["errors"] = []any{map[string]any{"type": "INTERNAL", "path": []any{"node", "state"}}}
	beside := issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", "CLOSED")
	beside["errors"] = []any{map[string]any{"type": "INTERNAL", "path": []any{"node", "repository"}}}
	pull := func(state string) map[string]any {
		return map[string]any{"data": map[string]any{"node": map[string]any{"__typename": "PullRequest", "id": "I_kwDOFixture42", "number": 43, "url": "https://github.com/octo-example/widgets/pull/43", "state": state, "repository": map[string]any{"fullDatabaseId": "4000001", "nameWithOwner": "octo-example/widgets"}}}}
	}
	for name, a := range map[string]map[string]any{
		"a state null beside its error": partial,
		"errors beside a whole node":    beside,
		"a state GitHub does not give":  issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", "SOMETHING_NEW"),
		"no state":                      issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", ""),
		"an issue merged":               issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", "MERGED"),
		"a lower-case state":            issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", "open"),
		"a pull request drafted":        pull("DRAFT"),
	} {
		answer = a
		if tg, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); err == nil {
			t.Errorf("%s: answered %+v", name, tg)
		}
	}
	for state, want := range map[string]string{"OPEN": tracker.StateOpen, "CLOSED": tracker.StateClosed} {
		answer = issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", state)
		if tg, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); err != nil || tg.State != want {
			t.Errorf("an issue %s: %+v %v", state, tg, err)
		}
	}
	for state, want := range map[string]string{"OPEN": tracker.StateOpen, "CLOSED": tracker.StateClosed, "MERGED": tracker.StateMerged} {
		answer = pull(state)
		if tg, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); err != nil || tg.State != want {
			t.Errorf("a pull request %s: %+v %v", state, tg, err)
		}
	}
}

// What is posted is byte for byte what was given, or nothing: a body that
// is not valid UTF-8 (which the encoding to GitHub would change) is
// refused before any request, so the bytes a match digests are the bytes
// GitHub holds.
func TestCommentPostsTheBytesGiven(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	for _, body := range []string{"Fixed \xff in the delivery.", "\xc3", "a\xed\xa0\x80b"} {
		_, err := r.post(ctx, "p1", 1, target, body+"\n\n"+r.a.Marker("p1"))
		if !tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("%q: %v", body, err)
		}
	}
	if n := len(r.fake.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
	// Valid UTF-8 the encoding escapes (<, >, &, U+2028) is the same text.
	body := "<b>&</b> é\n\n" + r.a.Marker("p1")
	if _, err := r.post(ctx, "p1", 1, target, body); err != nil {
		t.Fatal(err)
	}
	g, _ := githubtest.ParseGraphQL(r.fake.Requests()[0].Body)
	if g.Variables["body"] != body {
		t.Fatalf("sent %q", g.Variables["body"])
	}
}

// The finder lists the target's own repository by its identity: the
// listing is addressed by the repository's id the Resolve read, so a name
// another repository has taken since cannot answer for the target.
func TestFindListsTheRepositoryByItsID(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	marker := r.a.Marker("p1")
	posted := "Fixed in the delivery.\n\n" + marker
	// The old name now names a new repository, whose #42 has other comments.
	r.fake.Reply(http.MethodGet, api, "/repos/octo-example/widgets/issues/42/comments", 200, []map[string]any{comment(8800001, 9000003, "a comment on the new repository's #42")})
	r.fake.Reply(http.MethodGet, api, "/repositories/4000001/issues/42/comments", 200, []map[string]any{comment(7700001, 9000001, posted)})
	got, found, err := r.a.FindComment(ctx, r.cred, target, now, tracker.Match{Marker: marker, BodySha256: tracker.BodyDigest(posted), AuthorID: "9000001"})
	if err != nil || found != tracker.FoundYes || got.ID != "7700001" {
		t.Fatalf("%+v %v %v", got, found, err)
	}
	for _, q := range r.fake.Requests() {
		if strings.HasPrefix(q.Path, "/repos/") {
			t.Fatalf("listed by name: %s", q.Path)
		}
	}
}
