package github_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

const (
	api    = "api.github.com"
	secret = "ghp_FixtureTokenNeverSent0123456789abcd"
	head   = "1111111111111111111111111111111111111111"
)

// quiet is a logger for an adapter whose write lines no test reads.
var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// now is the fake clock a Retry-After is read against.
var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// lockedBuffer is a log the adapter writes and the test reads.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type rig struct {
	t    *testing.T
	fake *githubtest.Server
	a    *github.Adapter
	cred tracker.Credential
	log  *lockedBuffer
}

func newRig(t *testing.T, hosts ...string) *rig {
	t.Helper()
	fake := githubtest.New(t)
	log := &lockedBuffer{}
	a := github.New(github.Options{
		Hosts:     func() []string { return hosts },
		Transport: fake.Transport(),
		Logger:    slog.New(slog.NewTextHandler(log, nil)),
		UserAgent: "Conductor/test",
		Now:       func() time.Time { return now },
	})
	return &rig{t: t, fake: fake, a: a, cred: tracker.Token("github.com", secret), log: log}
}

// reply serves a fixed answer on the API host.
func (r *rig) reply(method, path string, status int, body any, header ...string) {
	r.fake.Reply(method, api, path, status, body, header...)
}

// fixture is a fixture decoded, to change before serving it.
func fixture(t *testing.T, name string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(githubtest.Fixture(name), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// etagged serves body with an ETag, answering 304 to a request that
// sends it back.
func (r *rig) etagged(path, etag string, body any) {
	b := githubtest.Body(body)
	r.fake.Handle(http.MethodGet, api, path, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("ETag", etag)
		if req.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
}

// graphql serves the GraphQL endpoint with answer.
func (r *rig) graphql(answer func(g githubtest.GraphQL) (int, any)) {
	r.fake.Handle(http.MethodPost, api, "/graphql", func(w http.ResponseWriter, req *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(req.Body)
		g, err := githubtest.ParseGraphQL(body.Bytes())
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		status, out := answer(g)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(githubtest.Body(out))
	})
}

// issueNode is ResolveDocument's answer for an issue.
func issueNode(id string, number int, repoID int64, nameWithOwner, state string) map[string]any {
	return map[string]any{"data": map[string]any{"node": map[string]any{
		"__typename": "Issue", "id": id, "number": number, "state": state,
		"url":        "https://github.com/" + nameWithOwner + "/issues/" + itoa(number),
		"repository": map[string]any{"fullDatabaseId": strconv.FormatInt(repoID, 10), "nameWithOwner": nameWithOwner},
	}}}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func ref(space, kind, id string) tracker.Ref {
	return tracker.Ref{System: tracker.GitHub, Host: "github.com", Space: space, Kind: kind, ID: id}
}

// noSecretAnywhere fails when any request but the API host's carries
// Authorization, or the API host's carries another than the token's.
func (r *rig) authOnlyToAPI() {
	r.t.Helper()
	for _, q := range r.fake.Requests() {
		auth := q.Header.Get("Authorization")
		if q.Host == api {
			if auth != "Bearer "+secret {
				r.t.Errorf("%s %s%s: Authorization %q", q.Method, q.Host, q.Path, auth)
			}
			continue
		}
		if auth != "" || strings.Contains(string(q.Body), secret) {
			r.t.Errorf("%s %s%s carried the token", q.Method, q.Host, q.Path)
		}
	}
}
