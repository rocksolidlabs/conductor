package github_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

var ctx = context.Background()

func TestReadIssue(t *testing.T) {
	r := newRig(t)
	r.reply(http.MethodGet, widgets+"/issues/42", 200, githubtest.Fixture("issue.json"), "ETag", `W/"issue-1"`)
	it, notMod, err := r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{})
	if err != nil || notMod {
		t.Fatalf("Read: %v %v", notMod, err)
	}
	want := tracker.Ref{System: "github", Host: "github.com", Space: "octo-example/widgets", Kind: tracker.KindIssue, ID: "42"}
	if it.Ref != want || it.NodeID != "I_kwDOFixture42" || it.DatabaseID != "5300000042" || it.URL != "https://github.com/octo-example/widgets/issues/42" {
		t.Fatalf("identity %+v", it)
	}
	if it.Title != "The widget list crashes when empty" || it.State != tracker.StateOpen || it.StateReason != "" || it.BodyTruncated {
		t.Fatalf("fields %+v", it)
	}
	if strings.Join(it.Labels, ",") != "bug" || strings.Join(it.Assignees, ",") != "hubot-example" || it.Milestone != "v1.0" || it.Author != "mona-example" || it.AuthorID != "9000001" {
		t.Fatalf("people %+v", it)
	}
	if it.CreatedAt.IsZero() || it.UpdatedAt.Format("2006-01-02T15:04:05Z") != "2026-10-02T10:30:00Z" || !it.ClosedAt.IsZero() {
		t.Fatalf("times %+v", it)
	}
	if it.Validators.Issue != `W/"issue-1"` || it.Validators.NodeID != "I_kwDOFixture42" || it.Validators.Pull != "" {
		t.Fatalf("validators %+v", it.Validators)
	}
	if steps, ok := tracker.Repro(it.Body); !ok || steps != "1. Open the widget list\n2. Remove every widget\n3. See the crash\n\n![crash](https://github.com/user-attachments/assets/6f1c2e9a-4b7d-4a52-9c3e-0f1d2a3b4c5d)" {
		t.Fatalf("repro %q %v", steps, ok)
	}
	// body_html's images, in the shapes captured from a public issue on
	// 2026-11-10 (an attachment rendered as private-user-images with a
	// signed ?jwt=, an image from another site through camo, an older
	// upload on user-images): GitHub's own hosts kept, every other left
	// out and counted.
	var ids []string
	for _, img := range it.Images {
		ids = append(ids, img.ID)
	}
	wantIDs := []string{
		"https://private-user-images.githubusercontent.com/9000001/400000042-6f1c2e9a-4b7d-4a52-9c3e-0f1d2a3b4c5d.png",
		"https://camo.githubusercontent.com/3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a/68747470733a2f2f6578616d706c652e636f6d2f6c6f676f2e706e67",
		"https://user-images.githubusercontent.com/9000001/120000001-0a1b2c3d-1111-4a6b-8c7d-9e0f1a2b3c4d.png",
	}
	if strings.Join(ids, " ") != strings.Join(wantIDs, " ") || it.Images[0].Alt != "crash" || !strings.HasSuffix(it.Images[0].FetchURL(), "?jwt=REDACTED.fixture.jwt") {
		t.Fatalf("images %v", ids)
	}
	if it.ImagesLeftOut != (tracker.ImagesLeftOut{Unlisted: 3}) {
		t.Fatalf("left out %+v", it.ImagesLeftOut)
	}
	// The repository looked up by its name, then the issue by its id.
	if q := r.fake.Requests()[0]; q.Path != "/repos/octo-example/widgets" {
		t.Fatalf("first asked %s", q.Path)
	}
	q := r.fake.Requests()[1]
	if q.Path != widgets+"/issues/42" || q.Header.Get("Accept") != "application/vnd.github.full+json" || q.Header.Get("X-GitHub-Api-Version") != "2022-11-28" || q.Header.Get("User-Agent") != "Conductor/test" || q.Header.Get("If-None-Match") != "" {
		t.Fatalf("headers %v", q.Header)
	}
	r.authOnlyToAPI()
}

func TestReadPrivateImages(t *testing.T) {
	r := newRig(t)
	r.reply(http.MethodGet, private+"/issues/7", 200, githubtest.Fixture("issue_private.json"))
	it, _, err := r.a.Read(ctx, r.cred, ref("octo-example/private-widgets", tracker.KindIssue, "7"), tracker.Validators{})
	if err != nil || len(it.Images) != 1 {
		t.Fatalf("Read: %+v %v", it.Images, err)
	}
	img := it.Images[0]
	if img.ID != "https://private-user-images.githubusercontent.com/9000001/400000001-0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d.png" || !strings.Contains(img.FetchURL(), "?jwt=REDACTED.fixture.jwt") {
		t.Fatalf("image %s %s", img.ID, img.FetchURL())
	}
	if steps, ok := tracker.Repro(it.Body); !ok || steps != "1. Open Settings" {
		t.Fatalf("repro %q", steps)
	}
	// Downloaded from its signed address, with no credential.
	host := "private-user-images.githubusercontent.com"
	r.fake.Reply(http.MethodGet, host, "/9000001/400000001-0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d.png", 200, "\x89PNG\r\n\x1a\nfixture", "Content-Type", "image/png")
	rc, typ, err := r.a.Image(ctx, r.cred, img, 1<<20)
	if err != nil || typ != "image/png" {
		t.Fatalf("Image: %q %v", typ, err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(b) != "\x89PNG\r\n\x1a\nfixture" {
		t.Fatalf("bytes %q %v", b, err)
	}
	last := r.fake.Requests()[len(r.fake.Requests())-1]
	if last.Host != host || last.RawQuery != "jwt=REDACTED.fixture.jwt" {
		t.Fatalf("fetched %s?%s", last.Host, last.RawQuery)
	}
	r.authOnlyToAPI()
}

func TestReadPull(t *testing.T) {
	r := newRig(t)
	r.reply(http.MethodGet, widgets+"/issues/43", 200, githubtest.Fixture("pull_issue.json"), "ETag", `"i43"`)
	r.reply(http.MethodGet, widgets+"/pulls/43", 200, githubtest.Fixture("pull.json"), "ETag", `"p43"`)
	// A short form reads as an issue; GitHub's answer says it is a change.
	in, err := r.a.Parse("octo-example/widgets#43", "")
	if err != nil || in.Kind != tracker.KindIssue {
		t.Fatalf("Parse %+v %v", in, err)
	}
	it, _, err := r.a.Read(ctx, r.cred, in, tracker.Validators{})
	if err != nil {
		t.Fatal(err)
	}
	if it.Ref.Kind != tracker.KindChange || it.URL != "https://github.com/octo-example/widgets/pull/43" || it.State != tracker.StateOpen || it.Merged || it.MergeSHA != "" {
		t.Fatalf("pull %+v", it)
	}
	if it.HeadSHA != head || it.BaseRef != "main" || it.Validators.Issue != `"i43"` || it.Validators.Pull != `"p43"` {
		t.Fatalf("pull fields %+v", it)
	}

	merged := fixture(t, "pull.json")
	merged["merged"], merged["merged_at"], merged["state"] = true, "2026-10-05T08:00:00Z", "closed"
	r.reply(http.MethodGet, widgets+"/pulls/43", 200, merged)
	it, _, err = r.a.Read(ctx, r.cred, it.Ref, tracker.Validators{})
	if err != nil || it.State != tracker.StateMerged || !it.Merged || it.MergeSHA != "2222222222222222222222222222222222222222" || it.MergedAt.IsZero() {
		t.Fatalf("merged %+v %v", it, err)
	}
	r.authOnlyToAPI()
}

func TestReadNotModified(t *testing.T) {
	r := newRig(t)
	r.etagged(widgets+"/issues/42", `"i1"`, githubtest.Fixture("issue.json"))
	issue := ref("octo-example/widgets", tracker.KindIssue, "42")
	it, notMod, err := r.a.Read(ctx, r.cred, issue, tracker.Validators{Issue: `"i1"`})
	if err != nil || !notMod || it.NodeID != "" {
		t.Fatalf("an unchanged issue: %v %v", notMod, err)
	}

	// A pull request: both 304 is not modified.
	r.etagged(widgets+"/issues/43", `"i43"`, githubtest.Fixture("pull_issue.json"))
	r.etagged(widgets+"/pulls/43", `"p43"`, githubtest.Fixture("pull.json"))
	change := ref("octo-example/widgets", tracker.KindChange, "43")
	if _, notMod, err := r.a.Read(ctx, r.cred, change, tracker.Validators{Issue: `"i43"`, Pull: `"p43"`}); err != nil || !notMod {
		t.Fatalf("both 304: %v %v", notMod, err)
	}

	// The issue's 304 beside a pull request whose head moved: read whole.
	moved := fixture(t, "pull.json")
	moved["head"].(map[string]any)["sha"] = "4444444444444444444444444444444444444444"
	r.etagged(widgets+"/pulls/43", `"p44"`, moved)
	before := len(r.fake.Requests())
	it, notMod, err = r.a.Read(ctx, r.cred, change, tracker.Validators{Issue: `"i43"`, Pull: `"p43"`})
	if err != nil || notMod {
		t.Fatalf("head moved: %v %v", notMod, err)
	}
	if it.HeadSHA != "4444444444444444444444444444444444444444" || it.Title != "Show an empty list" || it.NodeID != "PR_kwDOFixture43" || it.Validators.Issue != `"i43"` || it.Validators.Pull != `"p44"` {
		t.Fatalf("not whole: %+v", it)
	}
	reqs := r.fake.Requests()[before:]
	if len(reqs) != 4 || reqs[3].Path != widgets+"/issues/43" || reqs[3].Header.Get("If-None-Match") != "" {
		t.Fatalf("the issue was not read again without its validator: %+v", reqs)
	}

	// The pull's 304 beside an issue that changed: the pull read again.
	r.etagged(widgets+"/pulls/43", `"p43"`, githubtest.Fixture("pull.json"))
	before = len(r.fake.Requests())
	it, notMod, err = r.a.Read(ctx, r.cred, change, tracker.Validators{Issue: `"old"`, Pull: `"p43"`})
	if err != nil || notMod || it.HeadSHA != head {
		t.Fatalf("issue changed: %+v %v %v", it, notMod, err)
	}
	if reqs := r.fake.Requests()[before:]; len(reqs) != 4 || reqs[3].Path != widgets+"/pulls/43" || reqs[3].Header.Get("If-None-Match") != "" {
		t.Fatalf("the pull was not read again: %+v", reqs)
	}
}

func TestReadFollowsRename(t *testing.T) {
	r := newRig(t)
	r.reply(http.MethodGet, "/repositories/4000001/issues/42", 200, githubtest.Fixture("issue.json"))
	it, _, err := r.a.Read(ctx, r.cred, ref("octo-example/old-widgets", tracker.KindIssue, "42"), tracker.Validators{NodeID: "I_kwDOFixture42"})
	if err != nil {
		t.Fatal(err)
	}
	if it.Ref.Space != "octo-example/widgets" || it.URL != "https://github.com/octo-example/widgets/issues/42" || it.NodeID != "I_kwDOFixture42" {
		t.Fatalf("not at its new place: %+v", it.Ref)
	}
	r.authOnlyToAPI()
}

// An issue transferred to another repository has another number there: it
// is answered at its place now, repository and number, and a node id that
// differs names that place.
func TestReadFollowsTransfer(t *testing.T) {
	r := newRig(t)
	moved := fixture(t, "issue.json")
	moved["html_url"] = "https://github.com/octo-example/other-repo/issues/7"
	r.reply(http.MethodGet, widgets+"/issues/42", http.StatusMovedPermanently, `{"message":"Moved Permanently"}`, "Location", "https://api.github.com/repositories/4000002/issues/7")
	r.reply(http.MethodGet, "/repositories/4000002/issues/7", 200, moved)
	it, _, err := r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{NodeID: "I_kwDOFixture42"})
	want := tracker.Ref{System: tracker.GitHub, Host: "github.com", Space: "octo-example/other-repo", Kind: tracker.KindIssue, ID: "7"}
	if err != nil || it.Ref != want || it.URL != "https://github.com/octo-example/other-repo/issues/7" {
		t.Fatalf("not at its place now: %+v %s %v", it.Ref, it.URL, err)
	}
	_, _, err = r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{NodeID: "I_kwDOAnotherOne"})
	var mv *tracker.ErrMoved
	if !errors.As(err, &mv) || mv.To != want {
		t.Fatalf("another node: %v", err)
	}
	// A redirected answer whose place cannot be read is refused.
	moved["html_url"] = "https://collector.example/octo-example/other-repo/issues/7"
	r.reply(http.MethodGet, "/repositories/4000002/issues/7", 200, moved)
	if _, _, err := r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{NodeID: "I_kwDOFixture42"}); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("an unreadable new place: %v", err)
	}
}

// A body's images past the bound, or on a host the rule does not admit,
// are counted, so the caller can say what was not imported.
func TestReadImagesLeftOut(t *testing.T) {
	r := newRig(t)
	var b strings.Builder
	for i := 0; i < tracker.MaxImages+2; i++ {
		fmt.Fprintf(&b, `<img src="https://user-images.githubusercontent.com/9000001/%d-x.png">`, i)
	}
	b.WriteString(`<img src="https://user-images.githubusercontent.com/9000001/0-x.png"><img src="https://intranet.example/x.png"><img src="/relative.png">`)
	r.reply(http.MethodGet, widgets+"/issues/42", 200, map[string]any{"node_id": "I_kwDOFixture42", "html_url": "https://github.com/octo-example/widgets/issues/42", "body_html": b.String()})
	it, _, err := r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{})
	if err != nil || len(it.Images) != tracker.MaxImages || it.ImagesLeftOut != (tracker.ImagesLeftOut{Unlisted: 2, PastBound: 2}) {
		t.Fatalf("%d images, left out %+v, %v", len(it.Images), it.ImagesLeftOut, err)
	}
}

// A Ref that is not ParseGitHub's shape asks nothing.
func TestReadRefHeldToItsShape(t *testing.T) {
	r := newRig(t)
	for _, bad := range []tracker.Ref{
		{System: tracker.GitHub, Host: "github.com", Space: "octo-example/widgets/../../user", Kind: tracker.KindIssue, ID: "42"},
		{System: tracker.GitHub, Host: "github.com", Space: "octo-example/widgets", Kind: tracker.KindIssue, ID: "42?per_page=1"},
		{System: tracker.GitHub, Host: "github.com", Space: "octo-example/widgets", Kind: tracker.KindIssue, ID: "42#x"},
		{System: tracker.GitHub, Host: "github.com", Space: "octo-example", Kind: tracker.KindIssue, ID: "42"},
	} {
		if _, _, err := r.a.Read(ctx, r.cred, bad, tracker.Validators{}); !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if n := len(r.fake.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestReadMovedRefused(t *testing.T) {
	r := newRig(t)
	r.reply(http.MethodGet, "/repositories/4000001/issues/42", 200, githubtest.Fixture("issue.json"))
	_, _, err := r.a.Read(ctx, r.cred, ref("octo-example/old-widgets", tracker.KindIssue, "42"), tracker.Validators{NodeID: "I_kwDOAnotherOne"})
	var mv *tracker.ErrMoved
	if !errors.As(err, &mv) || mv.To.Space != "octo-example/widgets" || mv.To.ID != "42" {
		t.Fatalf("redirected to another node: %v", err)
	}
	// The old name made a new repository: no redirect, another node id.
	r.reply(http.MethodGet, widgets+"/issues/42", 200, githubtest.Fixture("issue.json"))
	if _, _, err := r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{NodeID: "I_kwDOAnotherOne"}); !errors.As(err, &mv) {
		t.Fatalf("another node at the same place: %v", err)
	}
}

func TestWhoami(t *testing.T) {
	r := newRig(t)
	r.reply(http.MethodGet, "/user", 200, githubtest.Fixture("user.json"), "X-OAuth-Scopes", "repo, read:org")
	acct, err := r.a.Whoami(ctx, r.cred)
	if err != nil || acct.Login != "mona-example" || acct.ID != "9000001" || strings.Join(acct.Scopes, ",") != "repo,read:org" {
		t.Fatalf("Whoami %+v %v", acct, err)
	}
	r.reply(http.MethodGet, "/user", 200, githubtest.Fixture("user.json"))
	if acct, err := r.a.Whoami(ctx, r.cred); err != nil || acct.Scopes != nil {
		t.Fatalf("a fine-grained token %+v %v", acct, err)
	}
	r.reply(http.MethodGet, "/user", 401, `{"message":"Bad credentials"}`)
	if _, err := r.a.Whoami(ctx, r.cred); !errors.Is(err, tracker.ErrUnauthorized) {
		t.Fatalf("refused token: %v", err)
	}
	if _, err := r.a.Whoami(ctx, tracker.Credential{}); !errors.Is(err, tracker.ErrUnauthorized) {
		t.Fatalf("no token: %v", err)
	}
}

func TestHosts(t *testing.T) {
	fake := githubtest.New(t)
	host := fake.PlainHost()
	fake.Reply(http.MethodGet, host, "/api/v3/repos/team/tool", 200, repoAnswer(4000009, "team/tool"))
	fake.Reply(http.MethodGet, host, "/api/v3/repositories/4000009/issues/9", 200, githubtest.Fixture("issue.json"))
	lo := github.New(github.Options{Loopback: true, Transport: fake.Transport()})
	r9 := tracker.Ref{System: "github", Host: host, Space: "team/tool", Kind: tracker.KindIssue, ID: "9"}
	cred := mustToken(t, host, true)
	if _, _, err := lo.Read(ctx, cred, r9, tracker.Validators{}); err != nil {
		t.Fatalf("loopback with the switch: %v", err)
	}
	if got, err := lo.Parse("http://"+host+"/team/tool/issues/9", ""); err != nil || got != r9 {
		t.Fatalf("Parse %+v %v", got, err)
	}
	off := github.New(github.Options{Transport: fake.Transport()})
	if _, _, err := off.Read(ctx, cred, r9, tracker.Validators{}); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("loopback without the switch: %v", err)
	}
	// An Enterprise Server host over https, at /api/v3.
	ghe := github.New(github.Options{Hosts: func() []string { return []string{"ghe.example.test"} }, Transport: fake.Transport()})
	fake.Reply(http.MethodGet, "ghe.example.test", "/api/v3/repos/team/tool", 200, repoAnswer(4000009, "team/tool"))
	fake.Reply(http.MethodGet, "ghe.example.test", "/api/v3/repositories/4000009/issues/9", 200, githubtest.Fixture("issue.json"))
	r9.Host = "ghe.example.test"
	it, _, err := ghe.Read(ctx, mustToken(t, "ghe.example.test", false), r9, tracker.Validators{})
	if err != nil || it.Ref.Host != "ghe.example.test" || !strings.HasPrefix(it.URL, "https://ghe.example.test/") {
		t.Fatalf("Enterprise Server: %+v %v", it.Ref, err)
	}
	// A token is never sent to another host than its own.
	before := len(fake.Requests())
	if _, _, err := ghe.Read(ctx, mustToken(t, "github.com", false), r9, tracker.Validators{}); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("a github.com token to another host: %v", err)
	}
	if len(fake.Requests()) != before {
		t.Fatal("a request was made")
	}
}
