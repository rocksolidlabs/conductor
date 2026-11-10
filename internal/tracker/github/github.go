// Package github is the GitHub adapter of internal/tracker
// (docs/plans/tracker-linking.md, Program item IN1a): GitHub's REST API
// over net/http and encoding/json, no client library, and GraphQL, a JSON
// POST over the same client, for the three calls that address an item by
// its node id (Resolve, Comment, Close; graphql.go). github.com and GitHub
// Enterprise Server hosts are reached over https; an http base only on a
// loopback host with Options.Loopback (CONDUCTOR_TRACKER_LOOPBACK=1, the
// tests' and the e2e harness's alone).
//
// A call uses two clients of its own. The API client makes the requests to
// the API host, through the credential's Transport and no other way, and
// follows a redirect only within that host (a renamed repository's 301),
// handing any other back unfollowed, so a credential reaches the API host
// alone by construction. The signed-address client fetches what the API
// named on another host (an image's src in body_html, a job log's
// Location) through the base transport alone, which holds no credential,
// following a redirect only to a host the request's rule admits
// (tracker.ImageHost, tracker.LogHost). Every answer is read to its bound,
// past which it is tracker.ErrBound; nothing is retried and nothing polls.
// GitHub's answers are decoded leniently (they carry many fields this
// package does not read); what Conductor sends is built from its own types.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// The adapter's bounds.
const (
	// RequestTimeout bounds one request to the API host; ImageTimeout an
	// image's download.
	RequestTimeout = 15 * time.Second
	ImageTimeout   = 60 * time.Second
	// MaxRedirects is the most hops a request follows.
	MaxRedirects = 5
	// MaxItemAnswer bounds an issue's or a pull request's answer
	// (body_html included), MaxPageAnswer a page of comments or checks,
	// MaxGraphQLAnswer a GraphQL answer, MaxErrorAnswer what is read of a
	// refusal, MaxLogRead a job's log.
	MaxItemAnswer    = 1 << 20
	MaxPageAnswer    = 2 << 20
	MaxGraphQLAnswer = 64 << 10
	MaxErrorAnswer   = 64 << 10
	MaxLogRead       = 8 << 20
	// MaxRuns is the workflow runs read at a commit, MaxJobs a run's jobs,
	// MaxCheckRuns the check runs and MaxStatuses the statuses read.
	MaxRuns      = 20
	MaxJobs      = 100
	MaxCheckRuns = 100
	MaxStatuses  = 100
	// MaxCheckSetEncoded bounds a check set's JSON.
	MaxCheckSetEncoded = 32 << 10
	// MaxCommentPages is the pages of 100 comments FindComment reads.
	MaxCommentPages = 10
	// MaxCommentBody is GitHub's bound on a comment's body.
	MaxCommentBody = 65536
)

// Options configure an Adapter.
type Options struct {
	// Hosts gives the GitHub Enterprise Server hosts admitted beside
	// github.com, read at each call (the hosts a token is saved for).
	Hosts func() []string
	// Loopback admits an http base on a loopback host
	// (CONDUCTOR_TRACKER_LOOPBACK=1).
	Loopback bool
	// Transport is the base round tripper of both clients (nil: a clone
	// of http.DefaultTransport). It holds no credential.
	Transport http.RoundTripper
	// Logger receives the writes' lines (nil: slog.Default()).
	Logger *slog.Logger
	// UserAgent is sent with every request (default Conductor/<version>).
	UserAgent string
	// Now is the clock a rate limit's Retry-After is read against.
	Now func() time.Time
}

// Adapter is the GitHub adapter, safe for concurrent use.
type Adapter struct {
	o    Options
	base http.RoundTripper
	log  *slog.Logger
	ua   string
}

var _ tracker.Adapter = (*Adapter)(nil)

// New makes the adapter.
func New(o Options) *Adapter {
	a := &Adapter{o: o, base: o.Transport, log: o.Logger, ua: o.UserAgent}
	if a.base == nil {
		a.base = http.DefaultTransport.(*http.Transport).Clone()
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.ua == "" {
		a.ua = "Conductor/" + version.Version
	}
	if a.o.Now == nil {
		a.o.Now = time.Now
	}
	return a
}

// System is "github".
func (a *Adapter) System() string { return tracker.GitHub }

// Marker is <!-- conductor-publication:<id> -->, for an id of 1 to 64
// letters, digits, '-' and '_' (” for any other).
func (a *Adapter) Marker(id string) string {
	if id == "" || len(id) > 64 {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return "<!-- conductor-publication:" + id + " -->"
}

func (a *Adapter) parseOptions() tracker.ParseOptions {
	po := tracker.ParseOptions{Loopback: a.o.Loopback}
	if a.o.Hosts != nil {
		po.Hosts = a.o.Hosts()
	}
	return po
}

// Parse reads a reference (tracker.ParseGitHub).
func (a *Adapter) Parse(raw, defaultSpace string) (tracker.Ref, error) {
	return tracker.ParseGitHub(raw, defaultSpace, a.parseOptions())
}

// endpoint is a host's API: its scheme and host, REST's base and
// GraphQL's address.
type endpoint struct {
	host    string // the item's host (github.com, an Enterprise host)
	scheme  string
	apiHost string
	rest    string
	graphql string
}

// endpointFor is the API of a host the credential is for.
func (a *Adapter) endpointFor(c tracker.Credential, host string) (endpoint, error) {
	if c.IsZero() {
		return endpoint{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnauthorized, Words: "no token is saved for " + host}
	}
	if c.Host() != host {
		return endpoint{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "the token is for another host"}
	}
	switch {
	case host == tracker.GitHubHost:
		return endpoint{host: host, scheme: "https", apiHost: "api.github.com", rest: "https://api.github.com", graphql: "https://api.github.com/graphql"}, nil
	case a.o.Loopback && tracker.IsLoopback(host):
		return endpoint{host: host, scheme: "http", apiHost: host, rest: "http://" + host + "/api/v3", graphql: "http://" + host + "/api/graphql"}, nil
	}
	for _, h := range a.parseOptions().Hosts {
		if h == host && !strings.Contains(host, ":") && host != "" {
			return endpoint{host: host, scheme: "https", apiHost: host, rest: "https://" + host + "/api/v3", graphql: "https://" + host + "/api/graphql"}, nil
		}
	}
	return endpoint{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: host + " is not a host Conductor reads"}
}

// request is one request to the API host.
type request struct {
	method string
	path   string // REST's path under the base; "" for GraphQL
	query  url.Values
	accept string
	etag   string
	body   []byte
	max    int64
	// keep3xx hands a redirect off the API host back as the answer (a
	// job log's 302) instead of refusing it.
	keep3xx bool
}

// answer is the API host's answer.
type answer struct {
	status     int
	header     http.Header
	body       []byte
	redirected bool
}

func (e endpoint) url(r request) string {
	if r.path == "" {
		return e.graphql
	}
	u := e.rest + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	return u
}

// errPath is a request's path as an error says it: never its query.
func (e endpoint) errPath(r request) string {
	u, err := url.Parse(e.url(r))
	if err != nil {
		return "-"
	}
	return u.Path
}

// errRedirect is a redirect the client refused.
var errRedirect = errors.New("redirect refused")

// do sends a request to the API host with the credential's transport and
// reads its answer to the bound. A status of 400 or more is the error it
// maps to; a 3xx off the API host is refused unless keep3xx.
func (a *Adapter) do(ctx context.Context, c tracker.Credential, ep endpoint, r request) (answer, error) {
	path := ep.errPath(r)
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, ep.url(r), body)
	if err != nil {
		return answer{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: path, Err: tracker.ErrUnsupported}
	}
	accept := r.accept
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", a.ua)
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.etag != "" {
		req.Header.Set("If-None-Match", r.etag)
	}
	redirected := false
	client := &http.Client{
		Transport: c.Transport(a.base),
		Timeout:   RequestTimeout,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= MaxRedirects {
				return errRedirect
			}
			if next.URL.Scheme != ep.scheme || !strings.EqualFold(next.URL.Host, ep.apiHost) {
				return http.ErrUseLastResponse
			}
			redirected = true
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return answer{}, a.transportError(ctx, r.method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
		if r.keep3xx {
			return answer{status: resp.StatusCode, header: resp.Header, redirected: redirected}, nil
		}
		return answer{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: path, Status: resp.StatusCode, Err: tracker.ErrUnsupported, Words: "a redirect off the API host"}
	}
	if resp.StatusCode >= 400 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxErrorAnswer))
		return answer{status: resp.StatusCode, header: resp.Header}, a.statusError(r.method, path, resp)
	}
	data, err := readBounded(resp.Body, r.max)
	if err != nil {
		if errors.Is(err, tracker.ErrBound) {
			return answer{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: path, Status: resp.StatusCode, Err: tracker.ErrBound, Words: "past " + strconv.FormatInt(r.max, 10) + " bytes"}
		}
		return answer{}, a.transportError(ctx, r.method, path, err)
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: data, redirected: redirected}, nil
}

// readBounded reads r to its end, at most max bytes; one more is
// tracker.ErrBound.
func readBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, tracker.ErrBound
	}
	return data, nil
}

// transportError is a request that had no answer: its words never carry
// the URL (a url.Error's text has the query).
func (a *Adapter) transportError(ctx context.Context, method, path string, err error) error {
	e := &tracker.RequestError{System: tracker.GitHub, Method: method, Path: path, Err: tracker.ErrUnavailable, Words: "the connection failed"}
	if errors.Is(err, errRedirect) {
		e.Err, e.Words = tracker.ErrUnsupported, "a redirect Conductor does not follow"
		return e
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		e.Words = "timed out"
	}
	if ctx.Err() != nil {
		e.Cause = ctx.Err()
		e.Words = "the call ended"
	}
	return e
}

// statusError maps a refusal to its typed error.
func (a *Adapter) statusError(method, path string, resp *http.Response) error {
	e := &tracker.RequestError{System: tracker.GitHub, Method: method, Path: path, Status: resp.StatusCode}
	rl := a.rateLimited(resp.Header)
	switch s := resp.StatusCode; {
	case (s == http.StatusForbidden || s == http.StatusTooManyRequests) && rl != nil:
		e.Err = rl
	case s == http.StatusTooManyRequests:
		e.Err = &tracker.ErrRateLimited{}
	case s == http.StatusUnauthorized:
		e.Err = tracker.ErrUnauthorized
	case s == http.StatusForbidden:
		e.Err = tracker.ErrForbidden
		if p := resp.Header.Get("X-Accepted-GitHub-Permissions"); p != "" && len(p) <= 200 {
			e.Words = "it needs " + p
		}
	case s == http.StatusNotFound || s == http.StatusGone:
		e.Err = tracker.ErrNotFound
	case s >= 500:
		e.Err = tracker.ErrUnavailable
	default:
		e.Err = tracker.ErrUnsupported
		if s == http.StatusUnprocessableEntity {
			e.Words = "GitHub refused the request as invalid"
		}
	}
	return e
}

// rateLimited reads a rate limit's refusal from its headers, or nil.
func (a *Adapter) rateLimited(h http.Header) *tracker.ErrRateLimited {
	if ra := h.Get("Retry-After"); ra != "" {
		if s, err := strconv.Atoi(ra); err == nil && s >= 0 {
			return &tracker.ErrRateLimited{Reset: a.o.Now().Add(time.Duration(s) * time.Second)}
		}
		return &tracker.ErrRateLimited{}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return &tracker.ErrRateLimited{Reset: time.Unix(s, 0)}
		}
		return &tracker.ErrRateLimited{}
	}
	return nil
}

// decode reads a JSON answer leniently (GitHub's answers carry fields this
// package does not read).
func decode(ep endpoint, r request, data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Status: http.StatusOK, Err: tracker.ErrUnsupported, Words: "an answer Conductor cannot read"}
	}
	return nil
}

// remoteID makes a RemoteID of a GitHub number.
func remoteID(n json.Number) tracker.RemoteID {
	s := n.String()
	if _, err := strconv.ParseUint(s, 10, 64); err != nil {
		return ""
	}
	return tracker.RemoteID(s)
}

// Whoami reads the token's account: GET /user, its scopes from
// X-OAuth-Scopes (a classic token's; a fine-grained token has none).
func (a *Adapter) Whoami(ctx context.Context, c tracker.Credential) (tracker.Account, error) {
	ep, err := a.endpointFor(c, c.Host())
	if err != nil {
		return tracker.Account{}, err
	}
	r := request{method: http.MethodGet, path: "/user", max: MaxItemAnswer}
	ans, err := a.do(ctx, c, ep, r)
	if err != nil {
		return tracker.Account{}, err
	}
	var u struct {
		Login string      `json:"login"`
		ID    json.Number `json:"id"`
	}
	if err := decode(ep, r, ans.body, &u); err != nil {
		return tracker.Account{}, err
	}
	acct := tracker.Account{Login: u.Login, ID: remoteID(u.ID)}
	if err := acct.Check(); err != nil || u.Login == "" {
		return tracker.Account{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Status: ans.status, Err: tracker.ErrUnsupported, Words: "an account Conductor cannot read"}
	}
	for _, s := range strings.Split(ans.header.Get("X-OAuth-Scopes"), ",") {
		if s = strings.TrimSpace(s); s != "" && len(acct.Scopes) < 50 {
			acct.Scopes = append(acct.Scopes, s)
		}
	}
	return acct, nil
}
