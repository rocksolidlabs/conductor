package github_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// In loopback mode (the tests' and the e2e harness's switch) a job of an
// item on a loopback host may have its log fetched from that same host
// over http, as the e2e's fake GitHub serves it behind the logs route's
// redirect: the item's own host byte for byte, with no user, fetched with
// no credential. A redirect to another loopback port or spelling, to a
// user, to https on that host or to another host is not fetched, nor is a
// redirect the log's host answers that leaves it; with loopback mode off
// the adapter fetches no loopback log at all, and the rule is
// tracker.LogHost's alone, which admits no loopback address.
func TestLoopbackLogsOnlyFromTheItemsOwnHost(t *testing.T) {
	fake := githubtest.New(t)
	host := fake.PlainHost()
	other := "127.0.0.1:1"
	if host == other {
		other = "127.0.0.1:2"
	}
	fake.Reply(http.MethodGet, host, "/api/v3/repos/team/tool", 200, repoAnswer(4000009, "team/tool"))
	logs := "/api/v3/repositories/4000009/actions/jobs/7/logs"
	fake.Reply(http.MethodGet, host, "/_logs/7", 200, "line one\n--- FAIL: TestLoopback\n", "Content-Type", "text/plain")
	lo := github.New(github.Options{Loopback: true, Transport: fake.Transport()})
	cred := mustToken(t, host, true)
	pr := tracker.Ref{System: tracker.GitHub, Host: host, Space: "team/tool", Kind: tracker.KindChange, ID: "42"}
	job := tracker.Check{ID: "job:7", HasLog: true}

	fake.Reply(http.MethodGet, host, logs, http.StatusFound, "", "Location", "http://"+host+"/_logs/7?sig=signed")
	b, err := lo.CheckLog(ctx, cred, pr, job, 23)
	if err != nil || string(b.Bytes) != "--- FAIL: TestLoopback\n" || !b.Cut {
		t.Fatalf("the log's tail: %+v %v", b, err)
	}
	for _, q := range fake.Requests() {
		if q.Path == "/_logs/7" && (q.Header.Get("Authorization") != "" || q.RawQuery != "sig=signed") {
			t.Fatalf("the log was fetched with %q, query %q", q.Header.Get("Authorization"), q.RawQuery)
		}
	}
	// Refused: another loopback port or spelling, a user, https on the host, another host.
	for _, loc := range []string{"http://" + other + "/_logs/7", "http://localhost:" + host[len("127.0.0.1:"):] + "/_logs/7", "http://user@" + host + "/_logs/7",
		"https://" + host + "/_logs/7", "http://ghe.example.test/_logs/7"} {
		before := len(fake.Requests())
		fake.Reply(http.MethodGet, host, logs, http.StatusFound, "", "Location", loc)
		if _, err := lo.CheckLog(ctx, cred, pr, job, 23); !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("a log at %s: %v", loc, err)
		}
		for _, q := range fake.Requests()[before:] {
			if q.Path == "/_logs/7" {
				t.Errorf("a log at %s was fetched", loc)
			}
		}
	}
	// A redirect from the log's host that leaves it, to another loopback
	// host, is not followed.
	fake.Reply(http.MethodGet, host, logs, http.StatusFound, "", "Location", "http://"+host+"/_logs/8")
	fake.Reply(http.MethodGet, host, "/_logs/8", http.StatusFound, "", "Location", "http://"+other+"/_logs/7")
	before := len(fake.Requests())
	if _, err := lo.CheckLog(ctx, cred, pr, job, 23); err == nil {
		t.Error("a log whose host redirected to another host answered")
	}
	for _, q := range fake.Requests()[before:] {
		if q.Host == other {
			t.Errorf("the redirect to %s was followed", other)
		}
	}
	// Loopback mode off: the adapter fetches no log of a loopback host,
	// its own redirect to that host included.
	fake.Reply(http.MethodGet, host, logs, http.StatusFound, "", "Location", "http://"+host+"/_logs/7?sig=signed")
	off := github.New(github.Options{Transport: fake.Transport()})
	before = len(fake.Requests())
	if _, err := off.CheckLog(ctx, cred, pr, job, 23); err == nil {
		t.Error("loopback mode off: a loopback log answered")
	}
	for _, q := range fake.Requests()[before:] {
		if q.Path == "/_logs/7" {
			t.Error("loopback mode off: the loopback log was fetched")
		}
	}
	// Without the switch: no loopback log, whatever host the credential names.
	if tracker.LogHost(tracker.GitHub, host, mustURL(t, "http://"+host+"/_logs/7")) {
		t.Fatal("LogHost admitted a loopback address")
	}
}
