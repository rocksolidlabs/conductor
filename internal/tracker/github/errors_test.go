package github_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// reset closes the connection without an answer.
func reset(w http.ResponseWriter, _ *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

// held runs call while the route is held at the fake's gate, ends its
// context once the request has reached the gate, and gives its error.
func held(t *testing.T, r *rig, host, path string, call func(context.Context) error) error {
	t.Helper()
	gate := r.fake.Hold(http.MethodGet, host, path)
	if path == "/graphql" {
		gate = r.fake.Hold(http.MethodPost, host, path)
	}
	c, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- call(c) }()
	<-gate.Reached()
	cancel()
	err := <-done
	gate.Open()
	return err
}

func TestErrorsMapped(t *testing.T) {
	r := newRig(t)
	path := "/repos/octo-example/widgets/issues/42"
	read := func() error {
		_, _, err := r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{})
		return err
	}
	cases := []struct {
		name   string
		status int
		header []string
		want   error
	}{
		{"401", 401, nil, tracker.ErrUnauthorized},
		{"403", 403, []string{"X-Accepted-GitHub-Permissions", "issues=read"}, tracker.ErrForbidden},
		{"404", 404, nil, tracker.ErrNotFound},
		{"410", 410, nil, tracker.ErrNotFound},
		{"422", 422, nil, tracker.ErrUnsupported},
		{"500", 500, nil, tracker.ErrUnavailable},
		{"502", 502, nil, tracker.ErrUnavailable},
		{"418", 418, nil, tracker.ErrUnsupported},
	}
	for _, c := range cases {
		r.reply(http.MethodGet, path, c.status, `{"message":"no"}`, c.header...)
		err := read()
		var re *tracker.RequestError
		if !errors.Is(err, c.want) || !errors.As(err, &re) || re.Status != c.status || re.Method != "GET" || re.Path != path {
			t.Errorf("%s: %v", c.name, err)
		}
		var rl *tracker.ErrRateLimited
		if errors.As(err, &rl) {
			t.Errorf("%s: rate limited", c.name)
		}
	}
	r.reply(http.MethodGet, path, 403, "", "X-Accepted-GitHub-Permissions", "issues=read")
	if err := read(); !strings.Contains(err.Error(), "it needs issues=read") {
		t.Errorf("403's permission not named: %v", err)
	}

	// Rate limits: a 403 or 429 with the remaining count at 0 or a
	// Retry-After.
	until := time.Unix(1762780000, 0)
	for _, c := range []struct {
		status int
		header []string
		want   time.Time
	}{
		{403, []string{"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(until.Unix(), 10)}, until},
		{429, []string{"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(until.Unix(), 10)}, until},
		{403, []string{"Retry-After", "60"}, now.Add(time.Minute)},
		{429, []string{"Retry-After", "30"}, now.Add(30 * time.Second)},
		{429, nil, time.Time{}},
		// A secondary rate limit may carry neither header: told by its
		// message, and GitHub says to wait at least a minute.
		{403, []string{"X-RateLimit-Remaining", "4000"}, now.Add(time.Minute)},
	} {
		body := ""
		if c.status == 403 && len(c.header) == 2 {
			body = `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`
		}
		r.reply(http.MethodGet, path, c.status, body, c.header...)
		var rl *tracker.ErrRateLimited
		if err := read(); !errors.As(err, &rl) || !rl.Reset.Equal(c.want) || errors.Is(err, tracker.ErrForbidden) {
			t.Errorf("%d %v: %v", c.status, c.header, err)
		}
	}

	// No answer: a connection reset, a call ended while the request was
	// held at the fake's gate.
	r.fake.Handle(http.MethodGet, api, path, reset)
	if err := read(); !errors.Is(err, tracker.ErrUnavailable) {
		t.Errorf("reset: %v", err)
	}
	r.reply(http.MethodGet, path, 200, githubtest.Fixture("issue.json"))
	err := held(t, r, api, path, func(c context.Context) error {
		_, _, err := r.a.Read(c, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{})
		return err
	})
	if !errors.Is(err, tracker.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Errorf("held: %v", err)
	}
	// The request's own deadline, held at the gate, which never opens,
	// until the adapter's clock says it passed: timed out, not the call.
	clock := &fakeClock{}
	github.SetClock(r.a, clock.after)
	gate := r.fake.Hold(http.MethodGet, api, path)
	done := make(chan error, 1)
	go func() { done <- read() }()
	<-gate.Reached()
	clock.pass()
	err = <-done
	gate.Open()
	if !errors.Is(err, tracker.ErrUnavailable) || !strings.Contains(err.Error(), "timed out") || errors.Is(err, context.Canceled) {
		t.Errorf("the request's deadline: %v", err)
	}
	// An answer GitHub would not give.
	r.reply(http.MethodGet, path, 200, "not json")
	if err := read(); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("unreadable: %v", err)
	}
}

// padded is a JSON object of exactly n bytes.
func padded(n int) []byte {
	b := []byte(`{"node_id":"x"}`)
	return append(b, []byte(strings.Repeat(" ", n-len(b)))...)
}

func TestBodiesBounded(t *testing.T) {
	r := newRig(t)
	iss := ref("octo-example/widgets", tracker.KindIssue, "42")
	path := "/repos/octo-example/widgets/issues/42"
	r.reply(http.MethodGet, path, 200, padded(github.MaxItemAnswer))
	if _, _, err := r.a.Read(ctx, r.cred, iss, tracker.Validators{}); err != nil {
		t.Fatalf("an item at its bound: %v", err)
	}
	r.reply(http.MethodGet, path, 200, padded(github.MaxItemAnswer+1))
	if _, _, err := r.a.Read(ctx, r.cred, iss, tracker.Validators{}); !errors.Is(err, tracker.ErrBound) {
		t.Fatalf("an item past its bound: %v", err)
	}

	status := "/repos/octo-example/widgets/commits/" + head + "/status"
	r.reply(http.MethodGet, status, 200, padded(github.MaxPageAnswer+1))
	if _, err := r.a.Checks(ctx, r.cred, iss, head); !errors.Is(err, tracker.ErrBound) {
		t.Fatalf("a page past its bound: %v", err)
	}
	r.reply(http.MethodGet, status, 200, padded(github.MaxPageAnswer))
	r.reply(http.MethodGet, "/repos/octo-example/widgets/actions/runs", 200, `{"total_count":0,"workflow_runs":[]}`)
	r.reply(http.MethodGet, "/repos/octo-example/widgets/commits/"+head+"/check-runs", 200, `{"total_count":0,"check_runs":[]}`)
	if _, err := r.a.Checks(ctx, r.cred, iss, head); err != nil {
		t.Fatalf("a page at its bound: %v", err)
	}

	r.reply(http.MethodPost, "/graphql", 200, padded(github.MaxGraphQLAnswer+1))
	if _, err := r.a.Resolve(ctx, r.cred, "I_kwDOFixture42"); !errors.Is(err, tracker.ErrBound) {
		t.Fatalf("a GraphQL answer past its bound: %v", err)
	}

	host := "user-images.githubusercontent.com"
	img := tracker.NewImageRef(mustURL(t, "https://"+host+"/1/a.png"), "")
	for _, n := range []int{100, 101} {
		r.fake.Reply(http.MethodGet, host, "/1/a.png", 200, strings.Repeat("x", n), "Content-Type", "image/png")
		rc, _, err := r.a.Image(ctx, r.cred, img, 100)
		if n == 101 {
			if !errors.Is(err, tracker.ErrBound) {
				t.Fatalf("an image past its bound, by its length: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || len(b) != 100 {
			t.Fatalf("an image at its bound: %d %v", len(b), err)
		}
	}
	// No length said: the read past the bound is refused.
	r.fake.Handle(http.MethodGet, host, "/1/a.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		for i := 0; i < 101; i++ {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
		}
	})
	rc, _, err := r.a.Image(ctx, r.cred, img, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if !errors.Is(err, tracker.ErrBound) || len(b) > 100 {
		t.Fatalf("a streamed image past its bound: %d %v", len(b), err)
	}

	logs := "/repos/octo-example/widgets/actions/jobs/2000001/logs"
	blob := "productionresultssa0.blob.core.windows.net"
	r.reply(http.MethodGet, logs, http.StatusFound, "", "Location", "https://"+blob+"/actions-results/x/job-logs.txt?sig=s")
	r.fake.Reply(http.MethodGet, blob, "/actions-results/x/job-logs.txt", 200, strings.Repeat("l", github.MaxLogRead+1))
	job := tracker.Check{ID: "job:2000001", HasLog: true}
	if _, err := r.a.CheckLog(ctx, r.cred, iss, job, 2048); !errors.Is(err, tracker.ErrBound) {
		t.Fatalf("a log past its bound: %v", err)
	}
}

func TestTokenOnlyToTheAPIHost(t *testing.T) {
	r := newRig(t)
	iss := ref("octo-example/widgets", tracker.KindIssue, "42")

	// An image redirected within GitHub's image hosts: followed, with no
	// credential on either request.
	img := tracker.NewImageRef(mustURL(t, "https://private-user-images.githubusercontent.com/1/a.png?jwt=j"), "")
	r.fake.Reply(http.MethodGet, "private-user-images.githubusercontent.com", "/1/a.png", http.StatusFound, "", "Location", "https://objects.githubusercontent.com/store/a.png?sig=s")
	r.fake.Reply(http.MethodGet, "objects.githubusercontent.com", "/store/a.png", 200, "png", "Content-Type", "image/png")
	rc, _, err := r.a.Image(ctx, r.cred, img, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	// A job's log: the API's 302 handed back, its Location fetched with no
	// credential.
	blob := "productionresultssa0.blob.core.windows.net"
	r.reply(http.MethodGet, "/repos/octo-example/widgets/actions/jobs/7/logs", http.StatusFound, "", "Location", "https://"+blob+"/logs/7.txt?sig=s")
	r.fake.Reply(http.MethodGet, blob, "/logs/7.txt", 200, "the log")
	if b, err := r.a.CheckLog(ctx, r.cred, iss, tracker.Check{ID: "job:7", HasLog: true}, 100); err != nil || string(b.Bytes) != "the log" {
		t.Fatalf("CheckLog %+v %v", b, err)
	}

	// A redirect from the API host to another host: handed back, never
	// followed through the credential's transport.
	r.reply(http.MethodGet, "/repos/octo-example/widgets/issues/42", http.StatusFound, "", "Location", "https://collector.evil.example/steal")
	if _, _, err := r.a.Read(ctx, r.cred, iss, tracker.Validators{}); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("an API redirect off its host: %v", err)
	}
	// An image redirected to a host no rule admits: refused, never asked.
	r.fake.Reply(http.MethodGet, "private-user-images.githubusercontent.com", "/1/a.png", http.StatusFound, "", "Location", "https://169.254.169.254/latest/meta-data")
	if _, _, err := r.a.Image(ctx, r.cred, img, 1<<20); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("an image redirect off its hosts: %v", err)
	}
	// A log redirected on to an image host: refused.
	r.fake.Reply(http.MethodGet, blob, "/logs/7.txt", http.StatusFound, "", "Location", "https://camo.githubusercontent.com/x")
	if _, err := r.a.CheckLog(ctx, r.cred, iss, tracker.Check{ID: "job:7", HasLog: true}, 100); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("a log redirect to an image host: %v", err)
	}
	// An image whose own address no rule admits is never fetched.
	if _, _, err := r.a.Image(ctx, r.cred, tracker.NewImageRef(mustURL(t, "https://intranet.example/x.png"), ""), 1<<20); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("an image off GitHub's hosts: %v", err)
	}
	for _, q := range r.fake.Requests() {
		if q.Host == "collector.evil.example" || q.Host == "169.254.169.254" || q.Host == "camo.githubusercontent.com" || q.Host == "intranet.example" {
			t.Fatalf("a refused host was asked: %s", q.Host)
		}
	}
	r.authOnlyToAPI()
}

func TestErrorsCarryNoQuery(t *testing.T) {
	r := newRig(t)
	iss := ref("octo-example/widgets", tracker.KindIssue, "42")
	var errs []error
	r.reply(http.MethodGet, "/repos/octo-example/widgets/commits/"+head+"/status", 200, `{"total_count":0,"statuses":[]}`)
	r.reply(http.MethodGet, "/repos/octo-example/widgets/actions/runs", 500, "")
	_, err := r.a.Checks(ctx, r.cred, iss, head)
	errs = append(errs, err)
	r.fake.Handle(http.MethodGet, api, "/repos/octo-example/widgets/actions/runs", reset)
	_, err = r.a.Checks(ctx, r.cred, iss, head)
	errs = append(errs, err)
	errs = append(errs, held(t, r, api, "/repos/octo-example/widgets/actions/runs", func(c context.Context) error {
		_, err := r.a.Checks(c, r.cred, iss, head)
		return err
	}))

	img := tracker.NewImageRef(mustURL(t, "https://private-user-images.githubusercontent.com/1/a.png?jwt=SIGNEDJWT"), "")
	r.fake.Reply(http.MethodGet, "private-user-images.githubusercontent.com", "/1/a.png", 404, "")
	_, _, err = r.a.Image(ctx, r.cred, img, 100)
	errs = append(errs, err)
	r.fake.Handle(http.MethodGet, "private-user-images.githubusercontent.com", "/1/a.png", reset)
	_, _, err = r.a.Image(ctx, r.cred, img, 100)
	errs = append(errs, err)

	r.graphql(func(githubtest.GraphQL) (int, any) {
		return 200, issueNode("I_kwDOFixture42", 42, 4000001, "octo-example/widgets", "OPEN")
	})
	r.reply(http.MethodGet, "/repos/octo-example/widgets/issues/42/comments", 503, "")
	tgt := tracker.Target{NodeID: "I_kwDOFixture42", Kind: tracker.KindIssue, SpaceID: "4000001", Ref: iss}
	m := tracker.Match{Marker: "<!-- conductor-publication:p1 -->", BodySha256: tracker.BodyDigest("x")}
	_, _, err = r.a.FindComment(ctx, r.cred, tgt, now, m)
	errs = append(errs, err)

	for i, err := range errs {
		if err == nil {
			t.Fatalf("%d: no error", i)
		}
		s := err.Error()
		for _, bad := range []string{"?", "head_sha", "per_page", "since", "jwt", "SIGNEDJWT", "sig=", secret, "https://"} {
			if strings.Contains(s, bad) {
				t.Errorf("%d: %q carries %q", i, s, bad)
			}
		}
	}
}

func TestNoRetry(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 503} {
		r := newRig(t)
		r.reply(http.MethodGet, "/repos/octo-example/widgets/issues/42", status, "", "Retry-After", "1")
		_, _, _ = r.a.Read(ctx, r.cred, ref("octo-example/widgets", tracker.KindIssue, "42"), tracker.Validators{})
		r.reply(http.MethodPost, "/graphql", status, "", "Retry-After", "1")
		_, _ = r.a.Comment(ctx, r.cred.ForWrite(tracker.Write{Kind: tracker.WriteComment, Publication: "p1", Try: 1}), tracker.Target{NodeID: "I_x", Kind: tracker.KindIssue, Ref: ref("octo-example/widgets", tracker.KindIssue, "42")}, "hello")
		if n := len(r.fake.Requests()); n != 2 {
			t.Fatalf("%d: %d requests", status, n)
		}
	}
}
