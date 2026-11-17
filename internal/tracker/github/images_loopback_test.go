package github_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// In loopback mode (the tests' and the e2e harness's switch) an item on a
// loopback host may show images served by that same host over http, as the
// e2e's fake GitHub serves them: the item's own host byte for byte, with no
// user, and downloaded with no credential. Any other http address, another
// loopback port or spelling among them, is left out; without the switch the
// rule is tracker.ImageHost's alone, which admits no loopback address.
func TestLoopbackImagesOnlyFromTheItemsOwnHost(t *testing.T) {
	fake := githubtest.New(t)
	host := fake.PlainHost()
	other := "127.0.0.1:1"
	if host == other {
		other = "127.0.0.1:2"
	}
	body := `<p><img src="http://` + host + `/_img/42/a.png?jwt=signed.a">` +
		`<img src="http://` + other + `/_img/42/b.png">` +
		`<img src="http://localhost:` + host[len("127.0.0.1:"):] + `/_img/42/c.png">` +
		`<img src="http://user@` + host + `/_img/42/d.png">` +
		`<img src="https://` + host + `/_img/42/e.png">` +
		`<img src="http://ghe.example.test/_img/42/f.png"></p>`
	fake.Reply(http.MethodGet, host, "/api/v3/repos/team/tool", 200, repoAnswer(4000009, "team/tool"))
	fake.Reply(http.MethodGet, host, "/api/v3/repositories/4000009/issues/42", 200, map[string]any{"node_id": "I_kwDOFixture42", "html_url": "http://" + host + "/team/tool/issues/42", "body_html": body})
	fake.Reply(http.MethodGet, host, "/_img/42/a.png", 200, "\x89PNG\r\n\x1a\nloopback", "Content-Type", "image/png")
	lo := github.New(github.Options{Loopback: true, Transport: fake.Transport()})
	cred := mustToken(t, host, true)
	r42 := tracker.Ref{System: tracker.GitHub, Host: host, Space: "team/tool", Kind: tracker.KindIssue, ID: "42"}
	it, _, err := lo.Read(ctx, cred, r42, tracker.Validators{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(it.Images) != 1 || it.Images[0].ID != "http://"+host+"/_img/42/a.png" || it.ImagesLeftOut.Unlisted != 5 {
		t.Fatalf("images %v, left out %+v", it.Images, it.ImagesLeftOut)
	}
	rc, typ, err := lo.Image(ctx, cred, it.Images[0], 1<<20)
	if err != nil || typ != "image/png" {
		t.Fatalf("Image: %q %v", typ, err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "\x89PNG\r\n\x1a\nloopback" {
		t.Fatalf("bytes %q", b)
	}
	for _, q := range fake.Requests() {
		if q.Path == "/_img/42/a.png" && (q.Header.Get("Authorization") != "" || q.RawQuery != "jwt=signed.a") {
			t.Fatalf("the image was fetched with %q, query %q", q.Header.Get("Authorization"), q.RawQuery)
		}
		if q.Path != "/_img/42/a.png" && len(q.Path) > 5 && q.Path[:5] == "/_img" {
			t.Fatalf("an image left out was fetched: %s", q.Path)
		}
	}
	// Refused at the download as at the read: another loopback host's image, handed in by a caller.
	for _, raw := range []string{"http://" + other + "/_img/42/b.png", "http://user@" + host + "/_img/42/d.png", "http://ghe.example.test/_img/42/f.png"} {
		if _, _, err := lo.Image(ctx, cred, tracker.NewImageRef(mustURL(t, raw), ""), 1<<20); err == nil {
			t.Errorf("Image %s answered", raw)
		}
	}
	// Without the switch: no loopback image, whatever host the credential names.
	if tracker.ImageHost(tracker.GitHub, host, mustURL(t, "http://"+host+"/_img/42/a.png")) {
		t.Fatal("ImageHost admitted a loopback address")
	}
}
