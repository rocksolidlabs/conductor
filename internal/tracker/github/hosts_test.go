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
// byte outside that alphabet makes no credential, and makes no request
// whether it comes as a saved Enterprise host or a reference's.
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
		if _, err := tracker.Token(h, secret, false); !errors.Is(err, tracker.ErrHost) {
			t.Errorf("a credential made for %q: %v", h, err)
		}
		r := tracker.Ref{System: tracker.GitHub, Host: h, Space: "team/tool", Kind: tracker.KindIssue, ID: "9"}
		if _, _, err := a.Read(ctx, mustToken(t, "github.com", false), r, tracker.Validators{}); err == nil {
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
		if _, err := tracker.Token(h, secret, true); !errors.Is(err, tracker.ErrHost) {
			t.Errorf("a loopback credential made for %q: %v", h, err)
		}
		r := tracker.Ref{System: tracker.GitHub, Host: h, Space: "team/tool", Kind: tracker.KindIssue, ID: "9"}
		if _, _, err := a.Read(ctx, mustToken(t, "github.com", true), r, tracker.Validators{}); err == nil {
			t.Errorf("loopback %q answered", h)
		}
		if got := rec.requests(); len(got) != 0 {
			t.Errorf("loopback %q: requests %q", h, got)
		}
	}
	for _, h := range []string{"localhost:8123", "127.0.0.1:8123", "[::1]:8123"} {
		rec := &recorder{}
		a := github.New(github.Options{Loopback: true, Transport: rec})
		_, _ = a.Whoami(ctx, mustToken(t, h, true))
		got := rec.requests()
		if len(got) != 1 || got[0] != "http://"+h+"/api/v3/user auth=Bearer "+secret {
			t.Errorf("loopback %q: requests %q", h, got)
		}
	}
	// A good Enterprise host: the address dialled is the one admitted.
	rec := &recorder{}
	a := github.New(github.Options{Hosts: func() []string { return []string{"ghe.example.test"} }, Transport: rec})
	_, _ = a.Whoami(ctx, mustToken(t, "ghe.example.test", false))
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

// Every entry point refuses a host that is not admitted before any
// request: as the credential's own host, and as the host of the item a
// good credential is asked about.
func TestBadHostAtEveryEntryPoint(t *testing.T) {
	bad := []string{
		"ghe.example.test@collector.example", "collector.example/ghe.example.test", "collector.example?ghe.example.test",
		"collector.example#x", "ghe.example.test:8443", "GHE.example.test", "ghe.example.test.", "gİthub.com", "",
		"127.0.0.1", "10.0.0.1", "169.254.169.254", "2130706433", "0x7f000001", "[::1]", "localhost",
	}
	job := tracker.Check{ID: "job:2000001", HasLog: true}
	for _, h := range bad {
		rec := &recorder{}
		a := github.New(github.Options{Hosts: func() []string { return []string{h} }, Transport: rec, Logger: quiet})
		item := tracker.Ref{System: tracker.GitHub, Host: h, Space: "team/tool", Kind: tracker.KindIssue, ID: "9"}
		tg := tracker.Target{NodeID: "I_kwDOFixture9", Kind: tracker.KindIssue, SpaceID: "4000001", Ref: item}
		m := tracker.Match{Marker: a.Marker("p1"), BodySha256: tracker.BodyDigest("x")}
		img := tracker.NewImageRef(mustURL(t, "https://camo.githubusercontent.com/abc"), "")
		entries := map[string]func(tracker.Credential) error{
			"Whoami":  func(c tracker.Credential) error { _, err := a.Whoami(ctx, c); return err },
			"Resolve": func(c tracker.Credential) error { _, err := a.Resolve(ctx, c, tg.NodeID); return err },
			"Image":   func(c tracker.Credential) error { _, _, err := a.Image(ctx, c, img, 1<<20); return err },
		}
		itemEntries := map[string]func(tracker.Credential) error{
			"Read":     func(c tracker.Credential) error { _, _, err := a.Read(ctx, c, item, tracker.Validators{}); return err },
			"Checks":   func(c tracker.Credential) error { _, err := a.Checks(ctx, c, item, head); return err },
			"CheckLog": func(c tracker.Credential) error { _, err := a.CheckLog(ctx, c, item, job, 100); return err },
			"Comment": func(c tracker.Credential) error {
				_, err := a.Comment(ctx, c.ForWrite(tracker.Write{Kind: tracker.WriteComment, Publication: "p1", Try: 1}), tg, "x")
				return err
			},
			"FindComment": func(c tracker.Credential) error { _, _, err := a.FindComment(ctx, c, tg, now, m); return err },
			"Close": func(c tracker.Credential) error {
				return a.Close(ctx, c.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: "p1", Try: 1}), tg, tracker.ReasonCompleted)
			},
		}
		if c, ok := credFor(h); ok {
			for name, call := range entries {
				if call(c) == nil {
					t.Errorf("%s with a credential for %q answered", name, h)
				}
			}
			for name, call := range itemEntries {
				if call(c) == nil {
					t.Errorf("%s with a credential for %q answered", name, h)
				}
			}
		}
		if got := rec.requests(); len(got) != 0 {
			t.Errorf("a credential for %q: requests %q", h, got)
		}
		good, _ := credFor("github.com")
		for name, call := range itemEntries {
			if call(good) == nil {
				t.Errorf("%s of an item on %q answered", name, h)
			}
		}
		if got := rec.requests(); len(got) != 0 {
			t.Errorf("an item on %q: requests %q", h, got)
		}
	}
}

// A redirect hop is judged as it is dialled too: after an admitted image
// or log host, a Location spelled in Unicode, percent-encoded, in
// fullwidth letters, upper case or with a trailing dot is never dialled.
func TestRedirectHopIsTheHostDialled(t *testing.T) {
	r := newRig(t)
	d := r.dialRecorded()
	hops := []string{
		"https://gİthub-production-user-asset-6210df.s3.amazonaws.com/k/x.png",
		"https://g%C4%B0thub-production-user-asset-6210df.s3.amazonaws.com/k/x.png",
		"//gİthub-production-user-asset-6210df.s3.amazonaws.com/k/x.png",
		"https://camo.gİthubusercontent.com/abc",
		"https://ｃamo.githubusercontent.com/abc",
		"https://CAMO.githubusercontent.com/abc",
		"https://camo.githubusercontent.com./abc",
	}
	for _, loc := range hops {
		r.fake.Reply(http.MethodGet, "github.com", "/user-attachments/assets/r1", http.StatusFound, "", "Location", loc)
		if rc, _, err := r.a.Image(ctx, r.cred, tracker.NewImageRef(mustURL(t, "https://github.com/user-attachments/assets/r1"), ""), 1<<20); err == nil {
			rc.Close()
			t.Errorf("an image's hop to %s answered", loc)
		}
	}
	for _, a := range d.list() {
		if a != "github.com:443" {
			t.Errorf("an image's hop dialled %s", a)
		}
	}

	r = newRig(t)
	d = r.dialRecorded()
	logs := "/repos/octo-example/widgets/actions/jobs/2000001/logs"
	r.reply(http.MethodGet, logs, http.StatusFound, "", "Location", "https://pipelines.actions.githubusercontent.com/log?sig=s")
	for _, loc := range []string{
		"https://productionresultssa1.blob.core.wİndows.net/x",
		"https://productionresultssa1.blob.core.w%C4%B0ndows.net/x",
		"https://productionresultssa１.blob.core.windows.net/x",
		"https://PRODUCTIONRESULTSSA1.blob.core.windows.net/x",
		"https://results-receiver.actİons.githubusercontent.com/x",
	} {
		r.fake.Reply(http.MethodGet, "pipelines.actions.githubusercontent.com", "/log", http.StatusFound, "", "Location", loc)
		if _, err := r.a.CheckLog(ctx, r.cred, pr43, tracker.Check{ID: "job:2000001", HasLog: true}, 100); err == nil {
			t.Errorf("a log's hop to %s answered", loc)
		}
	}
	for _, a := range d.list() {
		if a != "api.github.com:443" && a != "pipelines.actions.githubusercontent.com:443" {
			t.Errorf("a log's hop dialled %s", a)
		}
	}
}
