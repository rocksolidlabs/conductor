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
// the API host, through the credential's Transport and no other way, whose
// every hop is held to the API host byte for byte, and follows a redirect
// only within that host (a renamed repository's 301), handing any other
// back unfollowed, so a credential reaches the API host alone by
// construction: the credential carries its API address and its transport
// sends to no other. A job log's redirect is never followed by it, whatever
// host it names. A repository is named by its owner and name in one
// request alone, its lookup (lookupRepo); every request after it names the
// repository by the identity the lookup read (repo.path), so no other
// repository's answer, one that took the name meanwhile or a fork with the
// same commits, can stand for it. A write is sent once by every layer: a body that
// cannot be sent again, with or without bytes, no redirect followed, and
// its every ending sorted into made, nothing written or not known
// (sortWrite's table), nothing written only on a fact that shows it. No
// hop of any request carries a Referer. The signed-address client
// fetches what the API named on another host (an image's src in body_html,
// a job log's Location) through the base transport alone, which holds no
// credential, sending only to a host the request's rule admits, at every
// hop (tracker.ImageHost, tracker.LogHost). Every host goes through
// tracker.CheckHost, and an address is built from its parts. Every answer
// is read to its bound, past which it is tracker.ErrBound, and every
// request has its own deadline (RequestTimeout, ImageTimeout); nothing is
// retried and nothing polls. GitHub's answers are decoded leniently (they
// carry many fields this package does not read); what Conductor sends is
// built from its own types.
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
	"sync/atomic"
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
	// ChecksTimeout bounds a whole Checks call (at most MaxRuns+4
	// requests): when it passes, the listings read are the answer and the
	// rest is said to be missing.
	ChecksTimeout = 45 * time.Second
	// MaxRateLimitWait is the furthest reset a rate limit's answer is
	// taken to give (GitHub's limits reset within the hour): a Retry-After
	// or a reset time past it, or before now, is a reset not known.
	MaxRateLimitWait = time.Hour
	// MaxCount bounds a listing's total_count: a count outside 0 to it is
	// not known.
	MaxCount = 1_000_000
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
	// of http.DefaultTransport). It holds no credential, and it sends a
	// request at most once (net/http's own sends a write once: the
	// adapter gives it no body to send again); a layer that retries by
	// itself would send a write twice.
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
	// after runs f once d has passed and gives the stop of that timer:
	// the clock of every request's own deadline (RequestTimeout,
	// ImageTimeout), time.AfterFunc's but in tests.
	after func(d time.Duration, f func()) (stop func() bool)
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
	a.after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	return a
}

// errTimedOut is the cause of a request's context its own deadline ended.
var errTimedOut = errors.New("the request's deadline passed")

// deadline is ctx with the request's own deadline d on the adapter's clock:
// the context ends, with errTimedOut as its cause, once d has passed; done
// stops the clock and ends the context.
func (a *Adapter) deadline(ctx context.Context, d time.Duration) (context.Context, func()) {
	rc, cancel := context.WithCancelCause(ctx)
	stop := a.after(d, func() { cancel(errTimedOut) })
	return rc, func() {
		stop()
		cancel(context.Canceled)
	}
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

// endpoint is a host's API: the item's host, the API's scheme and host,
// and the paths REST and GraphQL are under. An address is built from these
// parts, never by joining strings, and every request's URL is held to
// scheme and apiHost byte for byte before it is sent (do) and at every hop
// (the credential's transport).
type endpoint struct {
	host        string // the item's host (github.com, an Enterprise host)
	scheme      string
	apiHost     string
	restPrefix  string // "" for api.github.com, /api/v3 for an Enterprise host
	graphqlPath string
}

// endpointFor is the API of a host the credential is for, at the
// credential's own API address (tracker.APIAddress, fixed when it was
// made). The host passes tracker.CheckHost (a name, never a number; no
// user, path, query, fragment, port, percent sign or byte past lower-case
// ASCII; a loopback form only with Options.Loopback) and is github.com, a
// loopback form, or a saved Enterprise Server host equal byte for byte;
// anything else, or a credential whose address is not that host's API, is
// refused before any request.
func (a *Adapter) endpointFor(c tracker.Credential, host string) (endpoint, error) {
	if c.IsZero() {
		return endpoint{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnauthorized, Words: "no token is saved for this host"}
	}
	if c.Host() != host {
		return endpoint{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "the token is for another host"}
	}
	refused := &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "not a host Conductor reads"}
	scheme, api, err := tracker.APIAddress(host, a.o.Loopback)
	if err != nil {
		return endpoint{}, refused
	}
	if cs, ch := c.API(); cs != scheme || ch != api {
		return endpoint{}, refused
	}
	ep := endpoint{host: host, scheme: scheme, apiHost: api, restPrefix: "/api/v3", graphqlPath: "/api/graphql"}
	switch {
	case host == tracker.GitHubHost:
		ep.restPrefix, ep.graphqlPath = "", "/graphql"
		return ep, nil
	case a.o.Loopback && tracker.IsLoopback(host):
		return ep, nil
	}
	for _, h := range a.parseOptions().Hosts {
		if h == host {
			return ep, nil
		}
	}
	return endpoint{}, refused
}

// repo is a repository as every request after its lookup names it: by the
// identity the lookup read (GitHub's numeric id, which a rename keeps and
// a new repository of a reused name, a fork among them, does not have),
// never by its owner and name.
type repo struct {
	id tracker.RemoteID
	// moved: the lookup was answered from a redirect (a renamed
	// repository).
	moved bool
}

// path is the one place a repository's part of a request path is written
// after its lookup: /repositories/{id} and rest. Nothing else writes a
// repository's path but lookupRepo, which alone names it by owner and
// name. An id that is not a positive number in decimal digits makes no
// path ("", which do refuses: no route is ever "").
func (p repo) path(rest string) string {
	if decimal(string(p.id)) == "" || p.id == "0" {
		return ""
	}
	return "/repositories/" + string(p.id) + rest
}

// lookupRepo reads a repository's identity from its owner and name (GET
// /repos/{owner}/{name}, following a renamed repository's redirect within
// the API host): the only request that names a repository by its name.
// Whatever the call asks after it is asked by the identity it read
// (repo.path), so a name another repository takes meanwhile cannot answer
// for it. An answer whose id cannot be read is one Conductor cannot read.
// space is held to tracker.Ref.Check by the caller.
func (a *Adapter) lookupRepo(ctx context.Context, c tracker.Credential, ep endpoint, space string) (repo, error) {
	r := request{method: http.MethodGet, path: "/repos/" + space, max: MaxItemAnswer}
	ans, err := a.do(ctx, c, ep, r)
	if err != nil {
		return repo{}, err
	}
	var got struct {
		ID json.Number `json:"id"`
	}
	if err := decode(ep, r, ans.body, &got); err != nil {
		return repo{}, err
	}
	p := repo{id: remoteID(got.ID), moved: ans.redirected}
	if p.path("") == "" {
		return repo{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: ep.errPath(r), Status: ans.status, Err: tracker.ErrUnsupported, Words: "an answer Conductor cannot read: no repository id"}
	}
	return p, nil
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
	// keep3xx marks a request whose answer is its redirect (a job log's
	// 302): no redirect is followed for it, whatever host it names, the
	// API host's own included, and the 3xx is handed back unread for the
	// signed-address client to fetch; any other success is an answer
	// Conductor does not take for it (the log is fetched one way).
	keep3xx bool
	// write marks a mutation: sent once by every layer under the adapter
	// (a body that cannot be sent again, with or without bytes, and no
	// redirect followed), and a request that provably never left is said
	// as nothing written.
	write bool
}

// answer is the API host's answer.
type answer struct {
	status     int
	header     http.Header
	body       []byte
	redirected bool
}

func (e endpoint) path(r request) string {
	if r.path == "" {
		return e.graphqlPath
	}
	return e.restPrefix + r.path
}

func (e endpoint) url(r request) *url.URL {
	u := &url.URL{Scheme: e.scheme, Host: e.apiHost, Path: e.path(r)}
	if len(r.query) > 0 {
		u.RawQuery = r.query.Encode()
	}
	return u
}

// errPath is a request's path as an error says it: never its query.
func (e endpoint) errPath(r request) string { return e.path(r) }

// errRedirect is a redirect the client refused.
var errRedirect = errors.New("redirect refused")

// do sends a request to the API host with the credential's transport and
// reads its answer to the bound, within the request's own deadline. A
// status of 400 or more is the error it maps to; a 3xx off the API host is
// refused, and a request with keep3xx follows no redirect at all and takes
// only its 3xx. A path that is "" is refused unless the request is
// GraphQL's POST. A write (r.write) is sent once by every layer:
// its request always has a body that cannot be sent again (net/http sends
// a request again over HTTP/2 when it has no body, or through GetBody,
// which a write's request does not have), no redirect is followed for it,
// and any answer but a 200 or a refusal is not known. Nothing written is
// said only on a fact: the address was refused or the call had ended
// before the request was made, or the base transport was never handed it
// (the token's transport refused it). A base transport that was handed it
// and failed tells nothing of whether it was sent: not known.
func (a *Adapter) do(ctx context.Context, c tracker.Credential, ep endpoint, r request) (answer, error) {
	path := ep.errPath(r)
	rctx, done := a.deadline(ctx, RequestTimeout)
	defer done()
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	u := ep.url(r)
	req, err := http.NewRequestWithContext(rctx, r.method, u.String(), body)
	if err != nil || (r.path == "" && r.method != http.MethodPost) || req.URL.Scheme != ep.scheme || req.URL.Host != ep.apiHost || req.URL.User != nil || req.URL.Path != u.Path {
		e := &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: path, Err: tracker.ErrUnsupported, Words: "an address Conductor does not send to"}
		if r.write {
			return answer{}, tracker.Proven(e)
		}
		return answer{}, e
	}
	if r.write {
		// Never nil nor http.NoBody (a request net/http's HTTP/2 client
		// sends again), never rewound.
		req.Body, req.GetBody = io.NopCloser(bytes.NewReader(r.body)), nil
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
	var handed atomic.Bool
	client := &http.Client{
		Transport: c.Transport(handedTo{a.base, &handed}),
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			next.Header.Del("Referer")
			if r.write || r.keep3xx {
				return http.ErrUseLastResponse
			}
			if len(via) >= MaxRedirects {
				return errRedirect
			}
			if next.URL.Scheme != ep.scheme || next.URL.Host != ep.apiHost || next.URL.User != nil {
				return http.ErrUseLastResponse
			}
			redirected = true
			return nil
		},
	}
	if rctx.Err() != nil {
		e := a.transportError(ctx, rctx, r.method, path, rctx.Err())
		if r.write {
			return answer{}, tracker.Proven(e)
		}
		return answer{}, e
	}
	resp, err := client.Do(req)
	if err != nil {
		e := a.transportError(ctx, rctx, r.method, path, err)
		if r.write && !handed.Load() {
			return answer{}, tracker.Proven(e)
		}
		return answer{}, e
	}
	defer resp.Body.Close()
	if r.write && resp.StatusCode != http.StatusOK && resp.StatusCode < 400 {
		words := "an answer that does not say whether the write was made"
		if resp.StatusCode >= 300 {
			words = "a redirect, which a write does not follow"
		}
		return answer{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: path, Status: resp.StatusCode, Err: tracker.ErrUnsupported, Words: words}
	}
	if r.keep3xx && resp.StatusCode < 400 {
		if resp.StatusCode < 300 || resp.StatusCode == http.StatusNotModified {
			return answer{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: path, Status: resp.StatusCode, Err: tracker.ErrUnavailable, Words: "an answer that is not the redirect asked for"}
		}
		return answer{status: resp.StatusCode, header: resp.Header}, nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
		return answer{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: path, Status: resp.StatusCode, Err: tracker.ErrUnsupported, Words: "a redirect off the API host"}
	}
	if resp.StatusCode >= 400 {
		return answer{status: resp.StatusCode, header: resp.Header}, a.statusError(r.method, path, resp)
	}
	data, err := readBounded(resp.Body, r.max)
	if err != nil {
		if errors.Is(err, tracker.ErrBound) {
			return answer{}, &tracker.RequestError{System: tracker.GitHub, Method: r.method, Path: path, Status: resp.StatusCode, Err: tracker.ErrBound, Words: "past " + strconv.FormatInt(r.max, 10) + " bytes"}
		}
		return answer{}, a.transportError(ctx, rctx, r.method, path, err)
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: data, redirected: redirected}, nil
}

// handedTo is the base transport under the credential's, noting that a
// request was handed to it: the line past which a write may have been sent.
type handedTo struct {
	base   http.RoundTripper
	handed *atomic.Bool
}

func (h handedTo) RoundTrip(r *http.Request) (*http.Response, error) {
	h.handed.Store(true)
	return h.base.RoundTrip(r)
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
// the URL (a url.Error's text has the query). ctx is the caller's, rctx
// the request's own (its deadline's cause errTimedOut).
func (a *Adapter) transportError(ctx, rctx context.Context, method, path string, err error) error {
	e := &tracker.RequestError{System: tracker.GitHub, Method: method, Path: path, Err: tracker.ErrUnavailable, Words: "the connection failed"}
	switch {
	case errors.Is(err, tracker.ErrOffHost):
		e.Err, e.Words = tracker.ErrUnsupported, "an address Conductor does not send the token to"
		return e
	case errors.Is(err, errRedirect):
		e.Err, e.Words = tracker.ErrUnsupported, "a redirect Conductor does not follow"
		return e
	case ctx.Err() != nil:
		e.Cause = ctx.Err()
		e.Words = "the call ended"
		return e
	case errors.Is(context.Cause(rctx), errTimedOut):
		e.Words = "timed out"
		return e
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		e.Words = "timed out"
	}
	return e
}

// statusError maps a refusal to its typed error. A rate limit is told
// from a refusal for a permission by GitHub's own signals, in every form
// it gives them (rateLimited): its headers, or its message.
func (a *Adapter) statusError(method, path string, resp *http.Response) error {
	msg := refusalMessage(resp.Body)
	e := &tracker.RequestError{System: tracker.GitHub, Method: method, Path: path, Status: resp.StatusCode}
	rl := a.rateLimited(resp.Header, resp.StatusCode, msg)
	switch s := resp.StatusCode; {
	case rl != nil:
		e.Err = rl
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

// refusalMessage is a refusal's message ("message" of its JSON), read to
// MaxErrorAnswer; never shown, only matched.
func refusalMessage(body io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(body, MaxErrorAnswer))
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	return m.Message
}

// rateLimited reads a rate limit's refusal, or nil, from GitHub's signals
// (docs.github.com, "Rate limits for the REST API" and for GraphQL):
// Retry-After, X-RateLimit-Remaining at 0 (with X-RateLimit-Reset), or, on
// a 403 or a 429, a message naming the rate limit (a secondary limit may
// carry neither header, and GitHub says to wait at least a minute then).
// A 429 is always a rate limit. Each number is held to its range before
// any arithmetic: a Retry-After of 0 to MaxRateLimitWait seconds, a reset
// from now to MaxRateLimitWait after it; outside, or not a number, the
// reset is not known (zero), never a time in the past or far ahead.
func (a *Adapter) rateLimited(h http.Header, status int, msg string) *tracker.ErrRateLimited {
	now := a.o.Now()
	if ra := h.Get("Retry-After"); ra != "" {
		if s, err := strconv.ParseInt(ra, 10, 64); err == nil && s >= 0 && s <= int64(MaxRateLimitWait/time.Second) {
			return &tracker.ErrRateLimited{Reset: now.Add(time.Duration(s) * time.Second)}
		}
		return &tracker.ErrRateLimited{}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && s >= now.Unix() && s <= now.Add(MaxRateLimitWait).Unix() {
			return &tracker.ErrRateLimited{Reset: time.Unix(s, 0)}
		}
		return &tracker.ErrRateLimited{}
	}
	if (status == http.StatusForbidden || status == http.StatusTooManyRequests) && strings.Contains(strings.ToLower(msg), "rate limit") {
		return &tracker.ErrRateLimited{Reset: now.Add(time.Minute)}
	}
	if status == http.StatusTooManyRequests {
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

// remoteID makes a RemoteID of a GitHub id as GitHub gives it, a JSON
// number or a BigInt's JSON string: its decimal digits as written (1 to
// MaxRemoteID, no leading zero), never through a fixed-width type, or ”.
func remoteID(n json.Number) tracker.RemoteID {
	return decimal(n.String())
}

func decimal(s string) tracker.RemoteID {
	if s == "" || len(s) > tracker.MaxRemoteID || (s[0] == '0' && len(s) > 1) {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return ""
		}
	}
	return tracker.RemoteID(s)
}

// bigID is a GitHub BigInt (fullDatabaseId): a JSON string of digits, or
// a number as some answers give it.
type bigID string

func (b *bigID) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*b = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*b = bigID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*b = bigID(n.String())
	return nil
}

func (b bigID) remote() tracker.RemoteID { return decimal(string(b)) }

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
