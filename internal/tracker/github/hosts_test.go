package github_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
)

// recorder is a base round tripper that records every request it is
// handed and answers none: nothing reaches a network.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (rt *recorder) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.seen = append(rt.seen, r.URL.Scheme+"://"+r.URL.Host+r.URL.Path+" auth="+r.Header.Get("Authorization"))
	rt.mu.Unlock()
	if r.Body != nil {
		r.Body.Close()
	}
	return nil, errors.New("recorder: no network")
}

func (rt *recorder) requests() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.seen...)
}

// A host is admitted only as an exact lower-case ASCII DNS name, or in
// loopback mode as one of the loopback forms with a numeric port: a host
// with a user, a path, a query, a fragment, a port, a percent sign or a
// byte outside that alphabet makes no request at all, whether it comes as
// a credential's host, a saved Enterprise host or a reference's.
func TestHostAdmittedExactly(t *testing.T) {
	bad := []string{
		"ghe.example.test@collector.example",
		"collector.example/ghe.example.test",
		"collector.example?ghe.example.test",
		"collector.example#x",
		"ghe.example.test:8443",
		"GHE.example.test",
		"ghe.example.test.",
		"ghe..example.test",
		"ghe%2eexample.test",
		"gİthub.com",
		"ghe.exam ple.test",
		"",
	}
	for _, h := range bad {
		rec := &recorder{}
		a := github.New(github.Options{Hosts: func() []string { return []string{h} }, Transport: rec})
		cred := tracker.Token(h, secret)
		if _, err := a.Whoami(ctx, cred); err == nil {
			t.Errorf("Whoami for %q answered", h)
		}
		if _, err := a.Resolve(ctx, cred, "I_kwDOFixture42"); err == nil {
			t.Errorf("Resolve for %q answered", h)
		}
		r := tracker.Ref{System: tracker.GitHub, Host: h, Space: "team/tool", Kind: tracker.KindIssue, ID: "9"}
		if _, _, err := a.Read(ctx, cred, r, tracker.Validators{}); err == nil {
			t.Errorf("Read for %q answered", h)
		}
		if got := rec.requests(); len(got) != 0 {
			t.Errorf("host %q: requests %q", h, got)
		}
	}

	// Loopback mode admits localhost, 127.0.0.1 and [::1] with a port of
	// digits, and nothing spelled around them.
	for _, h := range []string{"localhost:1@collector.example", "localhost:x", "localhost", "127.0.0.1", "localhost:", "127.0.0.1:123456", "127.0.0.2:80", "[::1]:8x", "localhost:80/x"} {
		rec := &recorder{}
		a := github.New(github.Options{Loopback: true, Transport: rec})
		if _, err := a.Whoami(ctx, tracker.Token(h, secret)); err == nil {
			t.Errorf("loopback %q answered", h)
		}
		if got := rec.requests(); len(got) != 0 {
			t.Errorf("loopback %q: requests %q", h, got)
		}
	}
	for _, h := range []string{"localhost:8123", "127.0.0.1:8123", "[::1]:8123"} {
		rec := &recorder{}
		a := github.New(github.Options{Loopback: true, Transport: rec})
		_, _ = a.Whoami(ctx, tracker.Token(h, secret))
		got := rec.requests()
		if len(got) != 1 || got[0] != "http://"+h+"/api/v3/user auth=Bearer "+secret {
			t.Errorf("loopback %q: requests %q", h, got)
		}
	}
	// A good Enterprise host: the address dialled is the one admitted.
	rec := &recorder{}
	a := github.New(github.Options{Hosts: func() []string { return []string{"ghe.example.test"} }, Transport: rec})
	_, _ = a.Whoami(ctx, tracker.Token("ghe.example.test", secret))
	if got := rec.requests(); len(got) != 1 || got[0] != "https://ghe.example.test/api/v3/user auth=Bearer "+secret {
		t.Errorf("Enterprise: requests %q", got)
	}
}

// An image's host is judged as the transport dials it: a host that is not
// already lower-case ASCII (a Unicode letter that folds onto a listed
// name, written raw or percent-encoded) is never admitted, never asked.
func TestImageHostIsTheHostDialled(t *testing.T) {
	r := newRig(t)
	for _, s := range []string{
		"https://gİthub.com/user-attachments/assets/6f1c2e9a",
		"https://g%C4%B0thub.com/user-attachments/assets/6f1c2e9a",
		"https://prİvate-user-images.githubusercontent.com/1/x.png",
		"https://camo.gİthubusercontent.com/abc",
		"https://CAMO.githubusercontent.com/abc",
	} {
		u := mustURL(t, s)
		if tracker.ImageHost(tracker.GitHub, "github.com", u) {
			t.Errorf("ImageHost admitted %s", s)
		}
		if _, _, err := r.a.Image(ctx, r.cred, tracker.NewImageRef(u, ""), 1<<20); !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("Image %s: %v", s, err)
		}
	}
	// In a read's body: left out, and counted.
	body := `<p><img src="https://gİthub.com/user-attachments/assets/6f1c2e9a"><img src="https://g%C4%B0thub.com/user-attachments/assets/6f1c2e9b"></p>`
	r.reply(http.MethodGet, "/repos/octo-example/widgets/issues/42", 200, map[string]any{"node_id": "I_kwDOFixture42", "html_url": "https://github.com/octo-example/widgets/issues/42", "body_html": body})
	it, _, err := r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{})
	if err != nil || len(it.Images) != 0 {
		t.Fatalf("Read: %v %v", it.Images, err)
	}
	for _, q := range r.fake.Requests() {
		if q.Host != api {
			t.Fatalf("asked %s", q.Host)
		}
	}
}
