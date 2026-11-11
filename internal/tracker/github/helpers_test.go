package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
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
	t     *testing.T
	fake  *githubtest.Server
	a     *github.Adapter
	cred  tracker.Credential
	log   *lockedBuffer
	hosts []string
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
	r := &rig{t: t, fake: fake, a: a, cred: mustToken(t, "github.com", false), log: log, hosts: hosts}
	r.repos()
	return r
}

// The fixtures' repositories: octo-example/widgets is 4000001, and
// octo-example/private-widgets 4000003; every request after a lookup names
// them by these ids.
const (
	widgets = "/repositories/4000001"
	private = "/repositories/4000003"
)

// repoAnswer is GET /repos/{owner}/{name}'s answer, the fields Conductor
// reads and a few beside them.
func repoAnswer(id int64, fullName string) map[string]any {
	return map[string]any{"id": id, "node_id": "R_kgDOFixture" + strconv.FormatInt(id, 10), "full_name": fullName, "private": false}
}

// repos serves the lookups of the fixtures' repositories, and
// octo-example/old-widgets as a renamed repository's: a 301 to the
// repository by its id, as GitHub answers a name a rename left.
func (r *rig) repos() {
	r.reply(http.MethodGet, "/repos/octo-example/widgets", 200, repoAnswer(4000001, "octo-example/widgets"))
	r.reply(http.MethodGet, widgets, 200, repoAnswer(4000001, "octo-example/widgets"))
	r.reply(http.MethodGet, "/repos/octo-example/private-widgets", 200, repoAnswer(4000003, "octo-example/private-widgets"))
	r.reply(http.MethodGet, "/repos/octo-example/old-widgets", http.StatusMovedPermanently, `{"message":"Moved Permanently"}`, "Location", "https://api.github.com"+widgets)
}

// credFor is a token's credential for host, and whether one could be made
// for it.
func credFor(h string) (tracker.Credential, bool) {
	c, err := tracker.Token(h, secret, false)
	return c, err == nil
}

// mustToken is a token's credential for a host Token admits.
func mustToken(t testing.TB, h string, loopback bool) tracker.Credential {
	t.Helper()
	c, err := tracker.Token(h, secret, loopback)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// dials records the address of every connection an adapter makes.
type dials struct {
	mu    sync.Mutex
	addrs []string
}

func (d *dials) list() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

// dialRecorded gives the rig an adapter over the same fake whose every
// dial is recorded: the address a request is sent to, as the transport
// dials it.
func (r *rig) dialRecorded() *dials {
	d := &dials{}
	tr := r.fake.Transport()
	inner := tr.DialContext
	tr.DialContext = func(c context.Context, network, addr string) (net.Conn, error) {
		d.mu.Lock()
		d.addrs = append(d.addrs, addr)
		d.mu.Unlock()
		return inner(c, network, addr)
	}
	hosts := r.hosts
	r.a = github.New(github.Options{
		Hosts:     func() []string { return hosts },
		Transport: tr,
		Logger:    quiet,
		UserAgent: "Conductor/test",
		Now:       func() time.Time { return now },
	})
	return d
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
