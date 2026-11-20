package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// The answer to a request whose token the identity provider did not judge
// (#890): 503 not_checked, written by notChecked alone, at every place that
// makes a principal from a request.

// principalMaker is the one function that makes a request's principal and
// the one that wraps it: authenticate calls tokenPrincipal.
const principalMaker = "auth.go:authenticate"

// principalPlace is a place that makes a principal from a request, with a
// request that reaches it: by the provider's token tok ("admin": a
// principal with the admin grant; "decide": one with the decide grant
// alone), grace asks of that token answered before the provider's state
// applies (the place is a route's second), and the status the request is
// answered once the provider answers (0: as the workbench token's).
type principalPlace struct {
	at                 string
	method, path, body string
	tok                string
	grace, admitted    int
}

// principalPlaces is every place in internal/api that makes a principal
// from a request (TestPrincipalPlacesAreTheTable holds the source to it).
var principalPlaces = []principalPlace{
	{at: "auth.go:requireAdmin", method: "GET", path: "/api/whoami", tok: "admin"},
	{at: "attention.go:attentionPrincipal", method: "POST", path: "/api/sessions/{sid}/attention", body: `{"state":"working"}`, tok: "admin"},
	{at: "brakeman_observe.go:reader", method: "GET", path: "/api/dispatch/tickets", tok: "admin"},
	{at: "brakeman_observe.go:observeRoute", method: "GET", path: "/api/brakeman/status", tok: "admin"},
	// Reached once attentionPrincipal (taskSession's) has admitted the token.
	{at: "brakeman_session.go:turnSession", method: "GET", path: "/api/sessions/{sid}/brakeman/request", tok: "admin", grace: 1},
	{at: "decide.go:deciding", method: "GET", path: "/api/me/decisions", tok: "decide", admitted: http.StatusOK},
	// Reached once deciding has admitted the token.
	{at: "decide.go:decideAnswer", method: "POST", path: "/api/decisions/dr-1/respond", body: `{}`, tok: "decide", grace: 1, admitted: http.StatusNotFound},
	{at: "events.go:handleEvents", method: "GET", path: "/api/events", tok: "admin"},
	{at: "files.go:handleGetFile", method: "GET", path: "/api/sessions/{sid}/files?path=a.txt", tok: "admin"},
	{at: "lane.go:handleRunDesk", method: "GET", path: "/ws/runs/r1/desk", tok: "admin"},
	{at: "me.go:handleMe", method: "GET", path: "/api/me", tok: "decide", admitted: http.StatusOK},
	{at: "sessions.go:handleGetSession", method: "GET", path: "/api/sessions/{sid}", tok: "admin"},
	{at: "tasks.go:taskSession", method: "POST", path: "/api/sessions/{sid}/task/result", body: `{}`, tok: "admin"},
	{at: "extlookup.go:lookupCaller", method: "POST", path: "/api/sessions/{sid}/context/lookup", body: `{}`, tok: "admin"},
	{at: "ws_host.go:handleHostWS", method: "GET", path: "/ws/host", tok: "admin"},
	{at: "ws_viewer.go:handleViewerWS", method: "GET", path: "/ws/sessions/{sid}", tok: "admin"},
}

// The tokens the provider knows, invented here.
const (
	judgedAdmin  = "cloud-judged-admin"
	judgedDecide = "cloud-judged-decide"
)

// judgeIdentity is an identity provider, and a Starter whose Start
// succeeds at once, that admits judgedAdmin and judgedDecide, or answers
// as its state says: "late" (it never answers before the guard's wait has
// ended), "fails", "panics", "invalid" (a subject past ext.MaxSubject),
// "no grant", "not mine" (ok false). A call whose request carries a
// judgeCase answers by that case instead.
type judgeIdentity struct {
	mu    sync.Mutex
	state string
	asked map[string]int
}

// judgeCase is one case's state of the provider, and its grace asks
// answered before that state applies, carried by the case's own requests
// (askAs): a call the guard's wait gave up on, still running as the next
// case begins, answers by its own case and leaves the next one's alone. A
// grace ask used in the late state moves the guard's wait to a nanosecond
// for the next ask (extWaiting), so the ask after it is the late one.
type judgeCase struct {
	mu    sync.Mutex
	state string
	grace int
}

type judgeCaseKey struct{}

// graceLeft is how many of c's grace asks were not made.
func (c *judgeCase) graceLeft() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.grace
}

func (*judgeIdentity) Name() string                          { return "cloud" }
func (*judgeIdentity) Start(context.Context, ext.Host) error { return nil }

func (f *judgeIdentity) Authenticate(ctx context.Context, tok string) (ext.Principal, bool, error) {
	f.mu.Lock()
	f.asked[tok]++
	state := f.state
	f.mu.Unlock()
	if c, ok := ctx.Value(judgeCaseKey{}).(*judgeCase); ok {
		c.mu.Lock()
		state = c.state
		if c.grace > 0 {
			c.grace--
			if state == "late" {
				setGuardWait(time.Nanosecond)
			}
			state = ""
		}
		c.mu.Unlock()
	}
	known := map[string]ext.Principal{
		judgedAdmin:  cloudAdmin("u1"),
		judgedDecide: {Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}},
	}
	switch state {
	case "late":
		<-ctx.Done()
		return known[tok], true, nil
	case "fails":
		return ext.Principal{}, false, errors.New("its keys are out of reach")
	case "panics":
		panic("its keys are out of reach")
	case "invalid":
		return cloudAdmin(strings.Repeat("u", ext.MaxSubject+1)), true, nil
	case "no grant":
		return ext.Principal{Subject: "u1", Name: "U"}, true, nil
	case "not mine":
		return ext.Principal{}, false, nil
	}
	p, ok := known[tok]
	return p, ok, nil
}

// set puts the provider in state for every call that carries no judgeCase.
func (f *judgeIdentity) set(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
}

func (f *judgeIdentity) timesAsked(tok string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[tok]
}

// setGuardWait is every guard call's wait from its next call on (nil: the
// product's own).
func setGuardWait(d time.Duration) { extWaiting.Store(&d) }

// cutRecorder is a ResponseRecorder that ends its request's context once
// anything is written, so a stream's handler returns after its first
// write.
type cutRecorder struct {
	*httptest.ResponseRecorder
	cut func()
}

func (c *cutRecorder) WriteHeader(code int) { c.ResponseRecorder.WriteHeader(code); c.cut() }
func (c *cutRecorder) Write(b []byte) (int, error) {
	n, err := c.ResponseRecorder.Write(b)
	c.cut()
	return n, err
}
func (c *cutRecorder) Flush() { c.ResponseRecorder.Flush(); c.cut() }

// placeAnswer is what a request is answered: its status, its
// WWW-Authenticate and its error's code and words.
type placeAnswer struct {
	code      int
	auth, err string
}

func placeAnswerOf(rec *httptest.ResponseRecorder) placeAnswer {
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return placeAnswer{rec.Code, rec.Header().Get("WWW-Authenticate"), codeOf(out) + ": " + apiMessage(out)}
}

// askPlace is what the server answers place's request presenting tok, on a
// server whose session is sid.
func askPlace(srv *Server, pl principalPlace, sid, tok string) *httptest.ResponseRecorder {
	return askAs(srv, pl, sid, tok, "", nil)
}

// askAs is askPlace's request naming the session turn as a Brakeman turn's
// request does (none when empty), its provider's calls answered by c (by
// the provider's own state when nil).
func askAs(srv *Server, pl principalPlace, sid, tok, turn string, c *judgeCase) *httptest.ResponseRecorder {
	path := strings.ReplaceAll(pl.path, "{sid}", sid)
	var body io.Reader
	if pl.body != "" {
		body = strings.NewReader(pl.body)
	}
	r := httptest.NewRequest(pl.method, path, body)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if turn != "" {
		r.Header.Set(brakemanSessionHeader, turn)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if c != nil {
		ctx = context.WithValue(ctx, judgeCaseKey{}, c)
	}
	rec := &cutRecorder{ResponseRecorder: httptest.NewRecorder(), cut: cancel}
	srv.Handler().ServeHTTP(rec, r.WithContext(ctx))
	return rec.ResponseRecorder
}

// notCheckedEnv is a server whose identity provider is a judgeIdentity, not
// yet started, with a session, every refusal the token's (the limiter
// admits all).
func notCheckedEnv(t *testing.T) (*testEnv, *judgeIdentity, string) {
	t.Helper()
	f := &judgeIdentity{asked: map[string]int{}}
	e := providerEnv(t, nil, f)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	return e, f, e.createSession("cat")
}

// isNotChecked fails t unless rec is the one answer to a token the
// provider did not judge.
func isNotChecked(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusServiceUnavailable || codeOf(out) != "not_checked" ||
		apiMessage(out) != "the credential could not be checked for now; try again" || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("%s: %d %v %q, want 503 not_checked", what, rec.Code, rec.Header(), rec.Body.String())
	}
}

// holdSlots takes every call slot of the identity guard named name, each
// as it is free (a call the guard's wait gave up on gives its slot back
// once it returns), and gives them back when t ends.
func holdSlots(t *testing.T, srv *Server, name string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testWait)
	defer cancel()
	var held []extSlot
	for range extInFlight {
		sl, ok := srv.extAwait(ctx, ext.KindIdentity, name)
		if !ok {
			t.Fatalf("a slot was still taken after %s", testWait)
		}
		held = append(held, sl)
	}
	var once sync.Once
	free := func() {
		once.Do(func() {
			for _, sl := range held {
				sl.free()
			}
		})
	}
	t.Cleanup(free)
	return free
}

// At every place that makes a principal from a request, a token the
// provider would admit, while the provider has not started, is late,
// fails, panics, answers a principal out of bounds or has every call slot
// held, is answered 503 not_checked and nothing else, never 401, 403 or
// 404; past the rate limit, 429 first. Once the provider answers, the same
// request is admitted.
func TestNotCheckedAtEveryPlace(t *testing.T) {
	e, f, sid := notCheckedEnv(t)
	t.Cleanup(func() { extWaiting.Store(nil) })
	g := e.srv.exts.identity.(guardIdentity)
	check := func(t *testing.T, state string, limited bool) {
		t.Helper()
		for _, pl := range principalPlaces {
			tok := placeToken(pl)
			grace := pl.grace
			if state == "not started" || state == "busy" {
				grace = 0 // the provider is not asked: the first place answers
			}
			// The case's own: its grace asks, answered within the test's
			// bound, reach the route's second place whatever the load.
			c := &judgeCase{state: state, grace: grace}
			switch {
			case grace > 0:
				setGuardWait(testWait)
			case state == "late":
				setGuardWait(time.Nanosecond)
			}
			rec := askAs(e.srv, pl, sid, tok, "", c)
			extWaiting.Store(nil)
			if n := c.graceLeft(); n != 0 {
				t.Fatalf("%s, %s: %d of the first place's asks not made: %d %s", pl.at, state, n, rec.Code, rec.Body)
			}
			if limited {
				var out map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &out)
				if rec.Code != http.StatusTooManyRequests || codeOf(out) != "rate_limited" {
					t.Fatalf("%s, %s, past the rate limit: %d %s", pl.at, state, rec.Code, rec.Body)
				}
				continue
			}
			isNotChecked(t, pl.at+", "+state, rec)
		}
	}
	unlimited := e.srv.limiter
	for _, limited := range []bool{false, true} {
		if limited {
			e.srv.limiter = newRateLimiter(0, 0)
		}
		if live(g.ready) {
			t.Fatal("the provider has started")
		}
		check(t, "not started", limited)
	}
	e.srv.limiter = unlimited
	e.srv.StartExtensions(t.Context())
	if !e.srv.ProviderStarted(t.Context(), "cloud") {
		t.Fatal("the provider did not start")
	}
	for _, limited := range []bool{false, true} {
		if limited {
			e.srv.limiter = newRateLimiter(0, 0)
		}
		for _, state := range []string{"late", "fails", "panics", "invalid"} {
			check(t, state, limited)
		}
		free := holdSlots(t, e.srv, "cloud")
		check(t, "busy", limited)
		free()
		e.srv.limiter = unlimited
	}
	// The provider answers: admitted, as the workbench token is, or as the
	// table says.
	for _, pl := range principalPlaces {
		f.set("")
		got := askPlace(e.srv, pl, sid, placeToken(pl))
		want := pl.admitted
		if want == 0 {
			want = askPlace(e.srv, pl, sid, adminToken).Code
		}
		if got.Code != want || want == http.StatusUnauthorized || want == http.StatusServiceUnavailable {
			t.Fatalf("%s admitted: %d %s, want %d", pl.at, got.Code, got.Body, want)
		}
	}
}

// A token the provider judges, as not its own or as a principal with no
// grant, is answered at every place as an unknown token is by a server
// with no provider registered, byte for byte, past the rate limit too; and
// a server with no provider answers every unknown token so with every
// call slot of its free default held.
func TestJudgedTokenAnsweredAsWithoutProvider(t *testing.T) {
	e, f, sid := notCheckedEnv(t)
	e.srv.StartExtensions(t.Context())
	if !e.srv.ProviderStarted(t.Context(), "cloud") {
		t.Fatal("the provider did not start")
	}
	free := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	free.srv.limiter = newRateLimiter(1e6, 1e6)
	freeSid := free.createSession("cat")
	freeName := free.srv.exts.identity.(guardIdentity).name
	type reply struct {
		code   int
		auth   string
		body   string
		header http.Header
	}
	of := func(rec *httptest.ResponseRecorder, sid string) reply {
		h := rec.Header().Clone()
		h.Del("Date")
		return reply{rec.Code, rec.Header().Get("WWW-Authenticate"), strings.ReplaceAll(rec.Body.String(), sid, "{sid}"), h}
	}
	same := func(a, b reply) bool {
		return a.code == b.code && a.auth == b.auth && a.body == b.body && maps.EqualFunc(a.header, b.header, slices.Equal)
	}
	for _, limited := range []bool{false, true} {
		if limited {
			e.srv.limiter, free.srv.limiter = newRateLimiter(0, 0), newRateLimiter(0, 0)
		}
		for _, pl := range principalPlaces {
			tok := placeToken(pl)
			want := of(askPlace(free.srv, pl, freeSid, tok), freeSid)
			if want.code == http.StatusServiceUnavailable {
				t.Fatalf("%s without a provider: %d", pl.at, want.code)
			}
			release := holdSlots(t, free.srv, freeName)
			if got := of(askPlace(free.srv, pl, freeSid, tok), freeSid); !same(got, want) {
				t.Fatalf("%s without a provider, its slots held: %+v, want %+v", pl.at, got, want)
			}
			release()
			for _, state := range []string{"not mine", "no grant"} {
				f.set(state)
				if got := of(askPlace(e.srv, pl, sid, tok), sid); !same(got, want) {
					t.Fatalf("%s, %s: %+v, want %+v", pl.at, state, got, want)
				}
			}
		}
	}
}

// placeToken is the provider's token for pl's principal.
func placeToken(pl principalPlace) string {
	if pl.tok == "decide" {
		return judgedDecide
	}
	return judgedAdmin
}

// The workbench token, a host token, a link, a session's own agent token
// and a hosted session's are answered as ever in every state of the
// provider, which is never asked about them.
func TestChainTokensNeverWaitOnTheProvider(t *testing.T) {
	e, f, sid := notCheckedEnv(t)
	t.Cleanup(func() { extWaiting.Store(nil) })
	agentTok := e.agentToken(sid)
	hostedTok := unminted("h")
	hosted := dialFakeHost(t, e, hostedTok).sessionID
	_, linkTok, err := e.srv.links.Create(sid, session.RoleView, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	type ask struct {
		pl   principalPlace
		tok  string
		want int
		sid  string
	}
	asks := []ask{
		{principalPlace{method: "GET", path: "/api/whoami"}, adminToken, http.StatusOK, ""},
		{principalPlace{method: "GET", path: "/api/whoami"}, "test-host-token", http.StatusUnauthorized, ""},
		{principalPlace{method: "GET", path: "/api/me"}, "test-host-token", http.StatusOK, ""},
		{principalPlace{method: "GET", path: "/api/sessions/{sid}"}, linkTok, http.StatusOK, ""},
		{principalPlace{method: "POST", path: "/api/sessions/{sid}/attention", body: `{"state":"working"}`}, agentTok, http.StatusOK, ""},
		{principalPlace{method: "GET", path: "/api/whoami"}, hostedTok, http.StatusUnauthorized, ""},
		{principalPlace{method: "POST", path: "/api/sessions/{sid}/attention", body: `{"state":"working"}`}, hostedTok, http.StatusOK, hosted},
	}
	run := func(state string) {
		t.Helper()
		for _, a := range asks {
			at := sid
			if a.sid != "" {
				at = a.sid
			}
			if rec := askPlace(e.srv, a.pl, at, a.tok); rec.Code != a.want {
				t.Fatalf("%s %s, %s: %d %s, want %d", a.pl.method, a.pl.path, state, rec.Code, rec.Body, a.want)
			}
		}
	}
	run("not started")
	e.srv.StartExtensions(t.Context())
	if !e.srv.ProviderStarted(t.Context(), "cloud") {
		t.Fatal("the provider did not start")
	}
	setGuardWait(time.Nanosecond)
	for _, state := range []string{"late", "fails", "panics", "invalid", "no grant", "not mine"} {
		f.set(state)
		run(state)
	}
	extWaiting.Store(nil)
	free := holdSlots(t, e.srv, "cloud")
	run("busy")
	free()
	for _, a := range asks {
		if n := f.timesAsked(a.tok); n != 0 {
			t.Fatalf("the provider was asked %d times about a token of the chain's", n)
		}
	}
}

// ownTokens is a server with a session and a running Brakeman turn: the
// session's id and agent token, and the turn's.
type ownTokens struct {
	e                          *testEnv
	sid, tok, turnSid, turnTok string
	// hostedSid and hostedTok are a hosted session's id and agent token.
	hostedSid, hostedTok string
}

// ownTokenEnv is ownTokens on a server whose identity provider is p (none
// registered when nil), every refusal the token's.
func ownTokenEnv(t *testing.T, p ext.IdentityProvider) ownTokens {
	t.Helper()
	var opts []Option
	if p != nil {
		opts = append(opts, withIdentity(p))
	}
	b := bkEnvOf(t, newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), bkAgents(), opts...))
	o := ownTokens{e: b.testEnv, sid: b.createSession("cat"), hostedTok: unminted("h")}
	o.tok = b.agentToken(o.sid)
	o.hostedSid = dialFakeHost(t, b.testEnv, o.hostedTok).sessionID
	b.enable(t, "bk", nil)
	req := b.briefing(t)
	o.turnSid, o.turnTok, _ = b.turnSession(t, req)
	b.waitState(t, req, brakeman.StateRunning)
	return o
}

// A live session's agent token, a live hosted session's (its session named,
// and another), and a live Brakeman turn's token (named as a turn names its
// session, and not), at every place that makes a
// principal from a request and in every state of the provider, are
// answered as the same request is by a server with no provider registered,
// and the provider is never asked about them. A session that has ended
// holds no token of the server's own: its token is an unknown one.
func TestOwnTokensAnsweredAsWithoutProvider(t *testing.T) {
	f := &judgeIdentity{asked: map[string]int{}}
	with, without := ownTokenEnv(t, f), ownTokenEnv(t, nil)
	t.Cleanup(func() { extWaiting.Store(nil) })
	none := func(ownTokens) string { return "" }
	presented := []struct {
		what           string
		tok, turn, sid func(o ownTokens) string
	}{
		{"a session's agent token", func(o ownTokens) string { return o.tok }, none, none},
		{"a turn's token", func(o ownTokens) string { return o.turnTok }, none, none},
		{"a turn's token, its session named", func(o ownTokens) string { return o.turnTok }, func(o ownTokens) string { return o.turnSid }, none},
		{"a hosted session's agent token", func(o ownTokens) string { return o.hostedTok }, none, none},
		{"a hosted session's agent token, its session at the path", func(o ownTokens) string { return o.hostedTok }, none, func(o ownTokens) string { return o.hostedSid }},
	}
	// at is the session a place names for p: the server session's, unless
	// p names its own.
	at := func(o ownTokens, sid func(ownTokens) string) string {
		if id := sid(o); id != "" {
			return id
		}
		return o.sid
	}
	run := func(state string) {
		t.Helper()
		for _, pl := range principalPlaces {
			for _, p := range presented {
				want := placeAnswerOf(askAs(without.e.srv, pl, at(without, p.sid), p.tok(without), p.turn(without), nil))
				if state == "late" {
					setGuardWait(time.Nanosecond)
				}
				got := placeAnswerOf(askAs(with.e.srv, pl, at(with, p.sid), p.tok(with), p.turn(with), &judgeCase{state: state}))
				extWaiting.Store(nil)
				if got != want {
					t.Fatalf("%s, %s, %s: %+v, want %+v", pl.at, state, p.what, got, want)
				}
			}
		}
	}
	run("not started")
	with.e.srv.StartExtensions(t.Context())
	if !with.e.srv.ProviderStarted(t.Context(), "cloud") {
		t.Fatal("the provider did not start")
	}
	for _, state := range []string{"late", "fails", "panics", "invalid", "no grant", "not mine"} {
		run(state)
	}
	free := holdSlots(t, with.e.srv, "cloud")
	run("busy")
	free()
	for _, tok := range []string{with.tok, with.turnTok, with.hostedTok} {
		if n := f.timesAsked(tok); n != 0 {
			t.Fatalf("the provider was asked %d times about a token of the server's own", n)
		}
	}
	d, _ := with.e.srv.registry.Get(with.sid)
	if err := d.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !waitFor(testWait, func() bool { return d.Info().Status.Ended() }) {
		t.Fatal("the session did not end")
	}
	isNotChecked(t, "an ended session's token", askAs(with.e.srv, principalPlaces[0], with.sid, with.tok, "", &judgeCase{state: "fails"}))
	if n := f.timesAsked(with.tok); n != 1 {
		t.Fatalf("the provider was asked %d times about an ended session's token", n)
	}
}

// liveTokenEnv is a server whose identity provider, a judgeIdentity, has
// started, every refusal the token's.
func liveTokenEnv(t *testing.T, mutate func(*config.Config)) (*testEnv, *judgeIdentity) {
	t.Helper()
	f := &judgeIdentity{asked: map[string]int{}}
	e := providerEnv(t, mutate, f)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	e.srv.StartExtensions(t.Context())
	if !e.srv.ProviderStarted(t.Context(), "cloud") {
		t.Fatal("the provider did not start")
	}
	return e, f
}

// unminted is a token of the length the server mints that nobody minted.
func unminted(c string) string { return strings.Repeat(c, share.TokenLen) }

// whoami is an admin route, which takes no agent token.
var whoami = principalPlace{method: "GET", path: "/api/whoami"}

// asAnUnknownToken fails t unless tok, presented at pl (sid and turn as
// askAs takes them) while the provider fails, is answered as a token
// nobody minted is once the provider judges it not its own: as a server
// with no provider answers it (TestJudgedTokenAnsweredAsWithoutProvider).
func asAnUnknownToken(t *testing.T, srv *Server, what string, pl principalPlace, sid, tok, turn string) {
	t.Helper()
	want := placeAnswerOf(askAs(srv, pl, sid, unminted("u"), turn, &judgeCase{state: "not mine"}))
	if got := placeAnswerOf(askAs(srv, pl, sid, tok, turn, &judgeCase{state: "fails"})); got != want {
		t.Fatalf("%s, %s %s: %+v, want %+v", what, pl.method, pl.path, got, want)
	}
}

// Whether a token is a live one of the server's own is one hash of it and
// one lookup, the same with no session and with a thousand, half of them
// ended; a token of another length than the server mints is not hashed for
// it, whatever its size; and a server with no provider registered does not
// ask it.
func TestALiveTokenIsOneHashAndOneLookup(t *testing.T) {
	if tok, _ := share.NewToken(); len(tok) != share.TokenLen {
		t.Fatalf("a minted token is %d long, not %d", len(tok), share.TokenLen)
	}
	e, _ := liveTokenEnv(t, func(c *config.Config) { c.MaxSessions = 2000 })
	hashes := func(srv *Server, tok string) int64 {
		before := srv.agentTokens.hashed.Load()
		askPlace(srv, whoami, "", tok)
		return srv.agentTokens.hashed.Load() - before
	}
	check := func(what string) {
		t.Helper()
		if n := hashes(e.srv, unminted("u")); n != 1 {
			t.Fatalf("%s: a token nobody minted hashed %d times, not once", what, n)
		}
		for _, size := range []int{1, share.TokenLen - 1, share.TokenLen + 1, 64 << 10} {
			if n := hashes(e.srv, strings.Repeat("u", size)); n != 0 {
				t.Fatalf("%s: a token %d long hashed %d times", what, size, n)
			}
		}
	}
	check("no session")
	for i := range 1000 {
		proc := sessiontest.NewFakeProc()
		l := session.NewLocal(session.Info{ID: session.NewID(), Kind: session.KindServer, Status: session.StatusRunning, Cols: 80, Rows: 24}, proc, session.Options{Tokens: &e.srv.agentTokens})
		l.SetAgentToken(fmt.Sprintf("%0*d", share.TokenLen, i))
		if err := e.srv.registry.Add(l); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { proc.End(0) })
		if i%2 == 0 {
			if err := l.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
	}
	check("a thousand sessions, half of them ended")
	free := newTestEnv(t, nil)
	if n := hashes(free.srv, unminted("u")); n != 0 {
		t.Fatalf("no provider registered: a token hashed %d times", n)
	}
}

// heldLaunch launches agent on e and holds the launch at its "started"
// seam, its process running and its session not yet in the registry: the
// session's id, its process's token (read from tokFile, which the process
// writes), the release of the launch and its answer's status.
func heldLaunch(t *testing.T, e *testEnv, agent, tokFile string) (id, tok string, release func(), created <-chan int) {
	t.Helper()
	id, tok, release, created = heldLaunchAt(t, e, &e.srv.launchHook, "started", agent, tokFile)
	if _, ok := e.srv.registry.Get(id); ok {
		t.Fatal("the registry holds the session while its launch is held")
	}
	return id, tok, release, created
}

// heldLaunchAt is heldLaunch held at stage of the seam whose hook is hooks
// (launchHook's, recordHook's).
func heldLaunchAt(t *testing.T, e *testEnv, hooks *atomic.Pointer[func(stage, id string)], at, agent, tokFile string) (id, tok string, release func(), created <-chan int) {
	t.Helper()
	started := make(chan string, 1)
	gate := make(chan struct{})
	var held, let sync.Once
	release = func() { let.Do(func() { close(gate) }) }
	t.Cleanup(release)
	hook := func(stage, sid string) {
		if stage == at {
			held.Do(func() {
				started <- sid
				<-gate
			})
		}
	}
	hooks.Store(&hook)
	t.Cleanup(func() { hooks.Store(nil) })
	answer := make(chan int, 1)
	go func() {
		resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": agent})
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
			t.Errorf("the launch of %s: %d %v", agent, resp.StatusCode, out)
		}
		answer <- resp.StatusCode
	}()
	select {
	case id = <-started:
	case code := <-answer:
		t.Fatalf("the launch of %s was answered %d before its %s seam", agent, code, at)
	}
	if !waitFor(testWait, func() bool {
		b, err := os.ReadFile(tokFile)
		tok = string(b)
		return err == nil
	}) {
		t.Fatal("the process never wrote its token")
	}
	return id, tok, release, answer
}

// A session's token is the server's own from its minting, before its
// process starts: while the launch is held after the process started and
// before the registry holds its session, the provider failing, the
// process's token at an admin route, at /api/me, at the session's read and
// at a Brakeman turn's shared read naming the session is answered as a
// server with no provider answers it, and the provider is never asked
// about it. Once the session ends, its token is unknown at once: the
// provider is asked, and while it cannot judge, 503. A launch that fails
// once its token was minted (the registry full) leaves its token unknown.
func TestALaunchingSessionsTokenIsTheServersOwn(t *testing.T) {
	e, f := liveTokenEnv(t, func(c *config.Config) { c.MaxSessions = 1 })
	dir := t.TempDir()
	write := `printf %s "$CONDUCTOR_NOTIFY_TOKEN" > "$1.tmp" && mv "$1.tmp" "$1"; exec /bin/cat`
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{
		{ID: "first", Name: "first", Command: []string{"/bin/sh", "-c", write, "sh", filepath.Join(dir, "first")}},
		{ID: "second", Name: "second", Command: []string{"/bin/sh", "-c", write, "sh", filepath.Join(dir, "second")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(cat)

	id, tok, release, created := heldLaunch(t, e, "first", filepath.Join(dir, "first"))
	for _, pl := range []principalPlace{whoami, {method: "GET", path: "/api/me"}, {method: "GET", path: "/api/sessions/{sid}"}} {
		asAnUnknownToken(t, e.srv, "a launching session's token", pl, id, tok, "")
	}
	asAnUnknownToken(t, e.srv, "a launching session's token, its session named", principalPlace{method: "GET", path: "/api/dispatch/tickets"}, id, tok, id)
	if n := f.timesAsked(tok); n != 0 {
		t.Fatalf("the provider was asked %d times about a launching session's token", n)
	}
	release()
	if code := <-created; code != http.StatusCreated {
		t.Fatalf("the launch: %d", code)
	}
	d, _ := e.srv.registry.Get(id)
	if err := d.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	isNotChecked(t, "an ended session's token", askAs(e.srv, whoami, "", tok, "", &judgeCase{state: "fails"}))
	if n := f.timesAsked(tok); n != 1 {
		t.Fatalf("the provider was asked %d times about an ended session's token", n)
	}

	id, tok, release, created = heldLaunch(t, e, "second", filepath.Join(dir, "second"))
	asAnUnknownToken(t, e.srv, "a launching session's token", whoami, id, tok, "")
	res, err := e.srv.Reserve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(res.Release)
	release()
	if code := <-created; code != http.StatusConflict {
		t.Fatalf("a launch past the registry's cap: %d", code)
	}
	isNotChecked(t, "a failed launch's token", askAs(e.srv, whoami, "", tok, "", &judgeCase{state: "fails"}))
	if n := f.timesAsked(tok); n != 1 {
		t.Fatalf("the provider was asked %d times about a failed launch's token", n)
	}
}

// size is how many tokens l holds.
func (l *liveTokens) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held)
}

// The set of live tokens counts each owner's entry: a hash two owners
// hold is held until both have removed theirs, and removing a hash that
// is not held changes nothing.
func TestALiveTokenSetCountsEachOwner(t *testing.T) {
	var l liveTokens
	tok, other := unminted("c"), unminted("d")
	h := share.HashToken(tok)
	l.Add(h)
	l.Add(h)
	l.Remove(h)
	if !l.holds(tok) || l.size() != 1 {
		t.Fatalf("one owner's removal of a hash two hold: held %v, size %d", l.holds(tok), l.size())
	}
	l.Remove(share.HashToken(other))
	if !l.holds(tok) || l.holds(other) || l.size() != 1 {
		t.Fatalf("a removal of a hash not held: held %v and %v, size %d", l.holds(tok), l.holds(other), l.size())
	}
	l.Remove(h)
	if l.holds(tok) || l.size() != 0 {
		t.Fatalf("both owners removed theirs: held %v, size %d", l.holds(tok), l.size())
	}
	l.Remove(h)
	l.Add(h)
	if !l.holds(tok) || l.size() != 1 {
		t.Fatalf("a removal past none left a count behind: held %v, size %d", l.holds(tok), l.size())
	}
}

// A hosted session's agent token is answered by the server itself from the
// session's registration until it ends: at a route that takes it first (the
// session's report) as a server with no provider serves it, and at a route
// that does not take it as a token nobody minted is once the provider judges
// it not its own, the provider never asked. A resume with another token
// swaps them: the new one is known, the old one is a token the chain does
// not know, asked of the provider (503 while it cannot judge). The
// session's end makes its token unknown again, and its leaving the registry
// leaves the set empty.
func TestAHostedSessionsTokenIsAnsweredByTheServer(t *testing.T) {
	e, f := liveTokenEnv(t, nil)
	free := newTestEnv(t, nil)
	tok := unminted("h")
	sess := func(agentToken string) proto.HostSession {
		return proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24, AgentToken: agentToken}
	}
	host, registered := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop"}, sess(tok), "Bearer test-host-token")
	secret, _ := registered["secret"].(string)
	if secret == "" {
		t.Fatal("the server sent no secret to resume with")
	}
	setHolds := func(n int, when string) {
		t.Helper()
		if got := e.srv.agentTokens.size(); got != n {
			t.Fatalf("the set of live tokens holds %d tokens once %s, not %d", got, when, n)
		}
	}
	setHolds(1, "a hosted session registered")
	report := principalPlaces[1]
	want := placeAnswerOf(askPlace(free.srv, report, dialFakeHost(t, free, tok).sessionID, tok))
	if got := placeAnswerOf(askAs(e.srv, report, host.sessionID, tok, "", &judgeCase{state: "fails"})); got != want || got.code != http.StatusOK {
		t.Fatalf("a hosted session's report: %+v, want %+v (200)", got, want)
	}
	asAnUnknownToken(t, e.srv, "a hosted session's token at a route that does not take it", whoami, "", tok, "")
	if n := f.timesAsked(tok); n != 0 {
		t.Fatalf("the provider was asked %d times about a live hosted session's token", n)
	}

	// The host goes and resumes the session with another token, which the
	// session takes (before the server says it registered).
	id := host.sessionID
	host.c.CloseNow()
	until(t, "the host gone", func() bool { return hostedSession(t, e, id).Info().Status == session.StatusHostDisconnected })
	setHolds(1, "the host went")
	tok2 := unminted("r")
	again, resumed := dialFakeHostRegister(t, e, proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"}, Session: sess(tok2),
		Resume: &proto.HostResume{SessionID: id, Secret: secret}}, "Bearer test-host-token", nil)
	if resumed["resumed"] != true || again.sessionID != id {
		t.Fatalf("resume: %v", resumed)
	}
	if !hostedSession(t, e, id).AgentTokenOK(tok2) {
		t.Fatal("the resumed session does not hold the resume's token")
	}
	setHolds(1, "the hosted session resumed with another token")
	asAnUnknownToken(t, e.srv, "the resume's token", whoami, "", tok2, "")
	isNotChecked(t, "the token the resume replaced", askAs(e.srv, whoami, "", tok, "", &judgeCase{state: "fails"}))
	if o, n := f.timesAsked(tok), f.timesAsked(tok2); o != 1 || n != 0 {
		t.Fatalf("the provider was asked %d times about the replaced token and %d about the resume's, not 1 and 0", o, n)
	}

	// A status, the session's end: the read loop has handled it once it
	// answers a revoke sent after it.
	again.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: id, Status: string(session.StatusExited)})
	again.send(proto.HostLinkRevokeMsg{T: proto.HostLinkRevoke, RequestID: "after-status", LinkID: "none"})
	linkAnswer(t, again, "after-status")
	if st := hostedSession(t, e, id).Info().Status; st != session.StatusExited {
		t.Fatalf("the hosted session's status after its host said exited: %s", st)
	}
	setHolds(0, "the hosted session's host said it exited")
	isNotChecked(t, "an ended hosted session's token", askAs(e.srv, whoami, "", tok2, "", &judgeCase{state: "fails"}))
	if n := f.timesAsked(tok2); n != 1 {
		t.Fatalf("the provider was asked %d times about an ended hosted session's token, not once", n)
	}

	// The ended session leaves the registry (Retire) before the answer.
	if resp, out := e.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("removing the ended hosted session: %d %v", resp.StatusCode, out)
	}
	setHolds(0, "the hosted session left the registry")
}

// A hosted session registered with a live launched session's token takes
// nothing from it and gains nothing of it, whichever ends first: the token
// stays known, never asked of the provider, while either session lives,
// and is unknown once both have ended; the launched session is never noted
// as called by its agent.
func TestAHostedTokenEqualToALaunchedOneTakesNothingFromIt(t *testing.T) {
	for _, hostedFirst := range []bool{true, false} {
		name := "the launched session ends first"
		if hostedFirst {
			name = "the hosted session ends first"
		}
		t.Run(name, func(t *testing.T) {
			e, f := liveTokenEnv(t, nil)
			sid := e.createSession("cat")
			tok := e.agentToken(sid)
			host := dialFakeHost(t, e, tok)
			if n := e.srv.agentTokens.size(); n != 1 {
				t.Fatalf("the set holds %d tokens for one token two sessions hold, not 1", n)
			}
			if rec := askPlace(e.srv, principalPlaces[1], host.sessionID, tok); rec.Code != http.StatusOK {
				t.Fatalf("the hosted session's report: %d %s", rec.Code, rec.Body)
			}
			d, _ := e.srv.registry.Get(sid)
			local := d.(*session.Local)
			endHosted := func() {
				t.Helper()
				host.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: host.sessionID, Status: string(session.StatusExited)})
				host.send(proto.HostLinkRevokeMsg{T: proto.HostLinkRevoke, RequestID: "after-status", LinkID: "none"})
				linkAnswer(t, host, "after-status")
				if !hostedSession(t, e, host.sessionID).Info().Status.Ended() {
					t.Fatal("the hosted session did not end")
				}
			}
			endLaunched := func() {
				t.Helper()
				if err := local.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			first, second := endLaunched, endHosted
			if hostedFirst {
				first, second = endHosted, endLaunched
			}
			first()
			asAnUnknownToken(t, e.srv, "the token, one of its two sessions ended", whoami, sid, tok, "")
			if n := f.timesAsked(tok); n != 0 {
				t.Fatalf("the provider was asked %d times about a token a live session holds", n)
			}
			second()
			if n := e.srv.agentTokens.size(); n != 0 {
				t.Fatalf("the set holds %d tokens once both sessions ended, not 0", n)
			}
			isNotChecked(t, "the token, both its sessions ended", askAs(e.srv, whoami, "", tok, "", &judgeCase{state: "fails"}))
			if n := f.timesAsked(tok); n != 1 {
				t.Fatalf("the provider was asked %d times about a token no live session holds, not once", n)
			}
			if local.AgentCalled() {
				t.Fatal("the launched session was noted as called by its agent")
			}
		})
	}
}

// afterSet is the server's set calling after once each change is made.
type afterSet struct {
	*liveTokens
	after func()
}

func (a *afterSet) Add(h [32]byte)    { a.liveTokens.Add(h); a.after() }
func (a *afterSet) Remove(h [32]byte) { a.liveTokens.Remove(h); a.after() }

// A host that comes back with the token its session holds keeps it known
// throughout: GET /api/me made after each step of the refresh, the new
// entry added and the old one removed, is answered as a token nobody
// minted is once the provider judges it, never 503, the provider never
// asked.
func TestAHostedReconnectKeepsItsTokenKnown(t *testing.T) {
	e, f := liveTokenEnv(t, nil)
	tok := unminted("h")
	me := principalPlace{method: "GET", path: "/api/me"}
	steps := 0
	e.srv.hosts.Tokens = &afterSet{liveTokens: &e.srv.agentTokens, after: func() {
		steps++
		asAnUnknownToken(t, e.srv, fmt.Sprintf("the token after step %d", steps), me, "", tok, "")
	}}
	s := proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24, LocalID: "1", AgentToken: tok}
	conn := signal.NewHostConn()
	hs := registerSession(t, e, conn, s)
	hs.HostDisconnected(conn)
	steps = 0
	again := registerSession(t, e, signal.NewHostConn(), s)
	if again != hs {
		t.Fatal("the same instance did not take its session back")
	}
	if steps != 2 {
		t.Fatalf("the refresh made %d changes to the set, not an add and a remove", steps)
	}
	if n := f.timesAsked(tok); n != 0 {
		t.Fatalf("the provider was asked %d times about a reconnecting session's token", n)
	}
}

// A hosted session removed from the registry while its registration is
// still finishing (held at its record's begin, after the registry and the
// hub hold it) leaves no entry, and none once the registration returns.
func TestARemovalDuringARegistrationLeavesNoToken(t *testing.T) {
	e, f := liveTokenEnv(t, nil)
	tok := unminted("h")
	s := proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24, LocalID: "1", AgentToken: tok}
	id := signal.HostedID("instance-r", "1")
	begun, gate := make(chan struct{}), make(chan struct{})
	hook := func(stage, sid string) {
		if sid == id && stage == "hostedBegun" {
			close(begun)
			<-gate
		}
	}
	e.srv.recordHook.Store(&hook)
	t.Cleanup(func() { e.srv.recordHook.Store(nil) })
	registered := make(chan error, 1)
	go func() {
		_, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "mac", User: "ada", Instance: "instance-r"}, Session: s}, signal.NewHostConn())
		registered <- err
	}()
	select {
	case <-begun:
	case <-time.After(testWait):
		t.Fatal("the registration never began its record")
	}
	if n := e.srv.agentTokens.size(); n != 1 {
		t.Fatalf("the set holds %d tokens while the registration is held, not 1", n)
	}
	e.srv.registry.Remove(id)
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once the held registration's session was removed, not 0", n)
	}
	close(gate)
	if err := <-registered; err != nil {
		t.Fatal(err)
	}
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once the registration returned, not 0", n)
	}
	isNotChecked(t, "a removed hosted session's token", askAs(e.srv, whoami, "", tok, "", &judgeCase{state: "fails"}))
	if n := f.timesAsked(tok); n != 1 {
		t.Fatalf("the provider was asked %d times about a removed hosted session's token, not once", n)
	}
}

// tokenWriters is a catalog of agents named names, each writing its
// CONDUCTOR_NOTIFY_TOKEN to the file of its name in dir.
func tokenWriters(t *testing.T, e *testEnv, dir string, names ...string) {
	t.Helper()
	write := `printf %s "$CONDUCTOR_NOTIFY_TOKEN" > "$1.tmp" && mv "$1.tmp" "$1"; exec /bin/cat`
	var agents []catalog.Agent
	for _, n := range names {
		agents = append(agents, catalog.Agent{ID: n, Name: n, Command: []string{"/bin/sh", "-c", write, "sh", filepath.Join(dir, n)}})
	}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(cat)
}

// A session's token has one owner at a time: its launch until the session
// takes it, then the session. A session stopped while its launch is still
// finishing (held after the registry holds it) is no longer the server's own
// from that moment: the provider failing, its token is 503 not_checked at
// once and the provider asked once, while the launch is held; the launch
// let go, the set is empty. A session whose launch returned holds its
// token: the server's own, never asked of the provider, until it ends. A
// launch whose process could not start, and one refused after its process
// started, leave the set empty.
func TestASessionTakesItsTokenFromItsLaunch(t *testing.T) {
	e, f := liveTokenEnv(t, func(c *config.Config) { c.MaxSessions = 1 })
	dir := t.TempDir()
	tokenWriters(t, e, dir, "first", "second", "third")
	id, tok, release, created := heldLaunchAt(t, e, &e.srv.recordHook, "afterRegistryAdd", "first", filepath.Join(dir, "first"))
	d, ok := e.srv.registry.Get(id)
	if !ok {
		t.Fatal("the registry does not hold the session at its afterRegistryAdd seam")
	}
	if err := d.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	isNotChecked(t, "a stopped session's token, its launch held", askAs(e.srv, whoami, "", tok, "", &judgeCase{state: "fails"}))
	if n := f.timesAsked(tok); n != 1 {
		t.Fatalf("the provider was asked %d times about a stopped session's token, its launch held", n)
	}
	release()
	if code := <-created; code != http.StatusCreated {
		t.Fatalf("the launch: %d", code)
	}
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once the launch returned, not 0", n)
	}

	id, tok, release, created = heldLaunch(t, e, "second", filepath.Join(dir, "second"))
	release()
	if code := <-created; code != http.StatusCreated {
		t.Fatalf("the launch: %d", code)
	}
	asAnUnknownToken(t, e.srv, "a session's token, its launch returned", whoami, id, tok, "")
	if n, size := f.timesAsked(tok), e.srv.agentTokens.size(); n != 0 || size != 1 {
		t.Fatalf("the provider was asked %d times about a live session's token, the set holds %d; want 0 and 1", n, size)
	}
	d, _ = e.srv.registry.Get(id)
	if err := d.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once the session ended, not 0", n)
	}

	_, _, release, created = heldLaunch(t, e, "third", filepath.Join(dir, "third"))
	res, err := e.srv.Reserve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(res.Release)
	release()
	if code := <-created; code != http.StatusConflict {
		t.Fatalf("a launch past the registry's cap: %d", code)
	}
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once a launch was refused, not 0", n)
	}
	res.Release()

	e.save(map[string]any{"id": "ghost", "name": "Ghost", "command": []string{"definitely-not-a-real-binary-xyz"}})
	if resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "ghost"}); resp.StatusCode != http.StatusBadGateway || errorCode(out) != "start_failed" {
		t.Fatalf("a launch whose process cannot start: %d %v", resp.StatusCode, out)
	}
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once a launch whose process could not start returned, not 0", n)
	}
}

// Twenty-four launches at once against a registry of six places, sessions
// stopped at their launch's seam, others stopped and removed beside the
// launches: the set never holds more tokens than the live sessions and the
// launches under way, at every seam and every removal, and none once all
// have ended.
func TestTheLiveTokensAreTheLiveSessionsAndTheLaunches(t *testing.T) {
	e, _ := liveTokenEnv(t, func(c *config.Config) { c.MaxSessions = 6 })
	var (
		mu             sync.Mutex
		known          []session.Driver
		sent, answered atomic.Int64
	)
	// The bound is read around the set's size so that it can only be
	// larger than the owners at the size's moment: what ends or is
	// answered before it, what starts or registers after it.
	check := func(where string) {
		mu.Lock()
		gone := 0
		for _, d := range known {
			if d.Info().Status.Ended() {
				gone++
			}
		}
		mu.Unlock()
		done := answered.Load()
		size := e.srv.agentTokens.size()
		mu.Lock()
		live := len(known) - gone
		mu.Unlock()
		if under := int(sent.Load() - done); size > live+under {
			t.Errorf("%s: the set holds %d tokens, more than %d live sessions and %d launches under way", where, size, live, under)
		}
	}
	beside := make(chan string, 24)
	registered := func(stage, id string) {
		if stage != "afterRegistryAdd" {
			return
		}
		d, ok := e.srv.registry.Get(id)
		if !ok {
			t.Errorf("the registry does not hold %s at its afterRegistryAdd seam", id)
			return
		}
		mu.Lock()
		known = append(known, d)
		n := len(known)
		mu.Unlock()
		check("a registration")
		switch n % 3 {
		case 0:
			if err := d.Stop(context.Background()); err != nil {
				t.Errorf("a stop at the seam: %v", err)
			}
			check("a stop at the seam")
		case 1:
			beside <- id
		}
	}
	started := func(stage, _ string) {
		if stage == "started" {
			check("a process started")
		}
	}
	e.srv.recordHook.Store(&registered)
	e.srv.launchHook.Store(&started)
	t.Cleanup(func() { e.srv.recordHook.Store(nil); e.srv.launchHook.Store(nil) })
	removed := make(chan struct{})
	go func() {
		defer close(removed)
		for id := range beside {
			e.do("DELETE", "/api/sessions/"+id, adminToken, nil)
			check("a stop and removal beside the launches")
		}
	}()
	var launches sync.WaitGroup
	for range 24 {
		sent.Add(1)
		launches.Go(func() {
			e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "ready-cat"})
			answered.Add(1)
		})
	}
	launches.Wait()
	close(beside)
	<-removed
	check("all launches answered")
	mu.Lock()
	defer mu.Unlock()
	if len(known) < 6 {
		t.Fatalf("%d sessions registered, not the registry's six at least", len(known))
	}
	for _, d := range known {
		if err := d.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once every session ended, not 0", n)
	}
}

// replacingSet is the server's set whose Remove calls between first: the
// moment between a replacement's two steps.
type replacingSet struct {
	*liveTokens
	between func()
}

func (r *replacingSet) Remove(h [32]byte) {
	if r.between != nil {
		r.between()
	}
	r.liveTokens.Remove(h)
}

// A token that replaces another on a live session is added before the old
// one is removed: held between the two steps, both are the server's own;
// then the new one is, and the old one is unknown. The session ended, the
// set is empty, and a token handed to it then is removed at once.
func TestAReplacedTokenIsNeverNeithersOwn(t *testing.T) {
	e, f := liveTokenEnv(t, nil)
	old, repl := unminted("o"), unminted("n")
	set := &replacingSet{liveTokens: &e.srv.agentTokens}
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	l := session.NewLocal(session.Info{ID: session.NewID(), Kind: session.KindServer, Status: session.StatusRunning, Cols: 80, Rows: 24}, proc, session.Options{Tokens: set})
	l.SetAgentToken(old)
	held := false
	set.between = func() {
		set.between, held = nil, true
		asAnUnknownToken(t, e.srv, "between the steps, the old token", whoami, "", old, "")
		asAnUnknownToken(t, e.srv, "between the steps, the new token", whoami, "", repl, "")
	}
	l.SetAgentToken(repl)
	if !held {
		t.Fatal("the replacement removed no old token")
	}
	asAnUnknownToken(t, e.srv, "the new token", whoami, "", repl, "")
	isNotChecked(t, "the replaced token", askAs(e.srv, whoami, "", old, "", &judgeCase{state: "fails"}))
	if o, n := f.timesAsked(old), f.timesAsked(repl); o != 1 || n != 0 {
		t.Fatalf("the provider was asked %d times about the replaced token and %d about the new one, not 1 and 0", o, n)
	}
	if err := l.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once the session ended, not 0", n)
	}
	l.TakeAgentToken(unminted("a"), session.AddToken(set, share.HashToken(unminted("a"))))
	if n := e.srv.agentTokens.size(); n != 0 {
		t.Fatalf("the set holds %d tokens once an ended session was handed one, not 0", n)
	}
}

// The question notes nothing about a session: its token answered at a
// route that does not take it, the session does not count as called by
// its agent (AgentCalled).
func TestTheQuestionNotesNoSession(t *testing.T) {
	e, _ := liveTokenEnv(t, nil)
	sid := e.createSession("cat")
	tok := e.agentToken(sid)
	asAnUnknownToken(t, e.srv, "a session's token", whoami, sid, tok, "")
	d, _ := e.srv.registry.Get(sid)
	if d.(*session.Local).AgentCalled() {
		t.Fatal("the question noted the session as called by its agent")
	}
}

// holdSlots waits for a slot a call still holds (a late call's, which
// gives it back once it returns) instead of failing on it: one slot is
// held here and given back only once holdSlots holds every other.
func TestHoldSlotsWaitsForASlotStillTaken(t *testing.T) {
	e, _ := liveTokenEnv(t, nil)
	late, ok := e.srv.extTake(ext.KindIdentity, "cloud")
	if !ok {
		t.Fatal("no slot was free")
	}
	g := e.srv.guard(ext.KindIdentity, "cloud")
	go func() {
		waitFor(testWait, func() bool { return len(g.slots) == cap(g.slots) })
		late.free()
	}()
	holdSlots(t, e.srv, "cloud")()
}

// The places that make a principal from a request are the table's, each
// one reached through tokenPrincipal, whose second result says whether it
// answered the request and is never dropped; the provider's ask about a
// request's token is named by the maker alone, called or not
// (asksProvider); and notChecked alone writes the code not_checked. It sees
// a call, a method value and a method expression of the provider's ask
// inside function bodies; it does not follow an alias of the identity
// extension or of the guard, a direct call of the guard's inner ask, or a
// method value declared at package level.
func TestPrincipalPlacesAreTheTable(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{principalMaker}
	for _, pl := range principalPlaces {
		want = append(want, pl.at)
	}
	var got []string
	codes := 0
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		codes += strings.Count(string(src), `"not_checked"`)
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			guards := guardNames(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if ask := asksProvider(sel, guards); ask != "" && !askerOf(ask, fn) {
						t.Errorf("%s: %s asks the provider (%s) outside the maker", fset.Position(sel.Pos()), fn.Name.Name, ask)
					}
				}
				as, isAssign := n.(*ast.AssignStmt)
				if isAssign && len(as.Rhs) == 1 && len(as.Lhs) == 2 && makesPrincipal(as.Rhs[0]) {
					if id, ok := as.Lhs[1].(*ast.Ident); ok && id.Name == "_" {
						t.Errorf("%s: %s drops whether the request was answered", fset.Position(as.Pos()), fn.Name.Name)
					}
				}
				if call, ok := n.(*ast.CallExpr); ok && makesPrincipal(call) {
					got = append(got, name+":"+fn.Name.Name)
				}
				return true
			})
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the places that make a principal from a request:\n%s\nwant (principalPlaces):\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if codes != 1 {
		t.Fatalf("not_checked is written in %d places, notChecked's alone wanted", codes)
	}
}

// A publication the switchyard answers 503 is tried again, as a 429 is; a
// 401 is not.
func TestPublishTriesNotCheckedAgain(t *testing.T) {
	for _, c := range []struct {
		status int
		again  bool
	}{{http.StatusServiceUnavailable, true}, {http.StatusTooManyRequests, true}, {http.StatusUnauthorized, false}} {
		wait, again := publishRetry(&hostagent.RefusedError{Status: c.status, Code: "not_checked"}, 0)
		if again != c.again || (again && wait != publishBackoff[0]) {
			t.Fatalf("%d: tried again %v after %s", c.status, again, wait)
		}
	}
}

// withheldPublisher is a switchyard's registration that presents no
// credential, its provider withholding one, refused status while refuse
// is set; once it is not, a publication, counted in made.
type withheldPublisher struct {
	status      int
	refuse      atomic.Bool
	tries, made atomic.Int32
}

func (p *withheldPublisher) Publish(_ context.Context, local *session.Local) (PublishedSession, error) {
	p.tries.Add(1)
	if p.refuse.Load() {
		return nil, &hostagent.RefusedError{Status: p.status, Code: http.StatusText(p.status), Withheld: true}
	}
	p.made.Add(1)
	return &gatedPublication{id: "there-" + local.Info().ID, done: make(chan struct{})}, nil
}

func (p *withheldPublisher) Server() string { return "https://rendezvous.example.net" }

// A publication whose registration presented no credential, its provider
// withholding one, is owed once it ends without a publication, whatever
// the refusal's status: refused 401 after its one try, refused 429 or 503
// once its tries again have ended, judged on its last. A Share once the
// credential is back publishes it, once: a Share after it publishes
// nothing more.
func TestAWithheldPublicationIsOwedWhenItsTriesEnd(t *testing.T) {
	was := publishBackoff
	publishBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { publishBackoff = was })
	for _, c := range []struct {
		status int
		tries  int32
	}{{http.StatusUnauthorized, 1}, {http.StatusTooManyRequests, 4}, {http.StatusServiceUnavailable, 4}} {
		t.Run(http.StatusText(c.status), func(t *testing.T) {
			e := newTestEnv(t, nil)
			p := &withheldPublisher{status: c.status}
			p.refuse.Store(true)
			e.srv.SetPublisher(p)
			id := e.createSession("cat")
			pending := func() bool {
				e.srv.pubMu.Lock()
				defer e.srv.pubMu.Unlock()
				return e.srv.pubPending[id] != nil
			}
			if !waitFor(testWait, func() bool { return p.tries.Load() > 0 && !pending() }) {
				t.Fatal("the publication did not end")
			}
			if n, owed := p.tries.Load(), e.srv.owedPublication(id); n != c.tries || !owed {
				t.Fatalf("%d tries, owed %v; want %d tries, owed", n, owed, c.tries)
			}
			p.refuse.Store(false)
			e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
			if !waitFor(testWait, func() bool { return e.srv.publishedOf(id) != nil }) {
				t.Fatal("the Share once the credential is back did not publish the session")
			}
			e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
			if !waitFor(testWait, func() bool { return !pending() }) {
				t.Fatal("a publication is still under way")
			}
			if n := p.made.Load(); n != 1 {
				t.Fatalf("the Shares published the session %d times, not once", n)
			}
		})
	}
}

// triesPublisher is a switchyard's registration answered refused for its
// tries in order, then, once refuse is clear, a publication counted in
// made.
type triesPublisher struct {
	refusals    []*hostagent.RefusedError
	refuse      atomic.Bool
	tries, made atomic.Int32
}

func (p *triesPublisher) Publish(_ context.Context, local *session.Local) (PublishedSession, error) {
	n := p.tries.Add(1)
	if p.refuse.Load() {
		return nil, p.refusals[min(int(n), len(p.refusals))-1]
	}
	p.made.Add(1)
	return &gatedPublication{id: "there-" + local.Info().ID, done: make(chan struct{})}, nil
}

func (p *triesPublisher) Server() string { return "https://rendezvous.example.net" }

// A publication is owed by the withheld credential's rule as its last try
// was answered, whatever its earlier tries were: withheld, then refused for
// another reason at the last (a 503, a 401 that ends them at once), it is
// not owed: a refusal like any other, its words the last one's, and a Share
// publishes nothing; refused otherwise, then withheld at the last, it is
// owed, and the next Share publishes it, once.
func TestTheOwedRuleJudgesTheLastTry(t *testing.T) {
	was := publishBackoff
	publishBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { publishBackoff = was })
	withheld := func(status int) *hostagent.RefusedError {
		return &hostagent.RefusedError{Status: status, Code: http.StatusText(status), Withheld: true}
	}
	refused := func(status int) *hostagent.RefusedError {
		return &hostagent.RefusedError{Status: status, Code: http.StatusText(status), Message: "refused for another reason"}
	}
	for _, c := range []struct {
		name     string
		refusals []*hostagent.RefusedError
		owed     bool
	}{
		{"withheld, then 503 at the last", []*hostagent.RefusedError{withheld(503), withheld(429), withheld(503), refused(503)}, false},
		{"withheld, then 401", []*hostagent.RefusedError{withheld(503), refused(401)}, false},
		{"refused otherwise, then withheld at the last", []*hostagent.RefusedError{refused(503), refused(429), refused(503), withheld(503)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			p := &triesPublisher{refusals: c.refusals}
			p.refuse.Store(true)
			e.srv.SetPublisher(p)
			id := e.createSession("cat")
			pending := func() bool {
				e.srv.pubMu.Lock()
				defer e.srv.pubMu.Unlock()
				return e.srv.pubPending[id] != nil
			}
			if !waitFor(testWait, func() bool { return p.tries.Load() > 0 && !pending() }) {
				t.Fatal("the publication did not end")
			}
			if n, owed := p.tries.Load(), e.srv.owedPublication(id); int(n) != len(c.refusals) || owed != c.owed {
				t.Fatalf("%d tries, owed %v; want %d tries, owed %v", n, owed, len(c.refusals), c.owed)
			}
			p.refuse.Store(false)
			for range 2 {
				e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
				if !waitFor(testWait, func() bool { return !pending() }) {
					t.Fatal("a publication is still under way")
				}
			}
			want := int32(0)
			if c.owed {
				want = 1
			}
			if n := p.made.Load(); n != want {
				t.Fatalf("the Shares published the session %d times, not %d", n, want)
			}
			if !c.owed {
				e.srv.pubMu.Lock()
				why := e.srv.pubErr[id]
				e.srv.pubMu.Unlock()
				if last := c.refusals[len(c.refusals)-1].Error(); why != last {
					t.Fatalf("the publication's words: %q, want the last refusal's %q", why, last)
				}
			}
		})
	}
}

// makesPrincipal says e calls authenticate or tokenPrincipal.
func makesPrincipal(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "authenticate" || sel.Sel.Name == "tokenPrincipal")
}

// asksProvider is the ask of the provider about a request's token that
// sel names ("providerPrincipal", the guard's "judge", or "Authenticate"
// of the server's identity extension or a guard), called or not (a method
// value, a method expression, an alias), or "": guards are the names the
// function holds a guardIdentity by.
func asksProvider(sel *ast.SelectorExpr, guards map[string]bool) string {
	guard := isGuard(sel.X, guards) || isGuardType(sel.X)
	switch name := sel.Sel.Name; {
	case name == "providerPrincipal":
		return name
	case name == "judge" && guard:
		return name
	case name == "Authenticate" && guard:
		return name
	case name == "Authenticate":
		if x, ok := sel.X.(*ast.SelectorExpr); ok && x.Sel.Name == "identity" {
			return name
		}
	}
	return ""
}

// askerOf says fn may make ask: providerPrincipal is the maker's
// (tokenPrincipal), and the guard's judge providerPrincipal's and the
// guard's own Authenticate's.
func askerOf(ask string, fn *ast.FuncDecl) bool {
	switch ask {
	case "providerPrincipal":
		return fn.Name.Name == "tokenPrincipal"
	case "judge":
		return fn.Name.Name == "providerPrincipal" || fn.Name.Name == "Authenticate" && fn.Recv != nil && isGuardType(fn.Recv.List[0].Type)
	}
	return false
}

// guardNames are the names fn holds a guardIdentity by: its receiver,
// its parameters, and what it declares or assigns as one.
func guardNames(fn *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}
	for _, fl := range []*ast.FieldList{fn.Recv, fn.Type.Params} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			if isGuardType(f.Type) {
				for _, n := range f.Names {
					names[n.Name] = true
				}
			}
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Rhs) == 1 && isGuard(n.Rhs[0], nil) {
				if id, ok := n.Lhs[0].(*ast.Ident); ok {
					names[id.Name] = true
				}
			}
		case *ast.ValueSpec:
			if n.Type != nil && isGuardType(n.Type) {
				for _, id := range n.Names {
					names[id.Name] = true
				}
			}
		}
		return true
	})
	return names
}

// isGuard says e is a guardIdentity: one of names, an assertion to one,
// or one made there.
func isGuard(e ast.Expr, names map[string]bool) bool {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return isGuard(e.X, names)
	case *ast.Ident:
		return names[e.Name]
	case *ast.TypeAssertExpr:
		return e.Type != nil && isGuardType(e.Type)
	case *ast.CompositeLit:
		return isGuardType(e.Type)
	case *ast.StarExpr:
		return isGuardType(e)
	}
	return false
}

// isGuardType says e names guardIdentity or a pointer to it.
func isGuardType(e ast.Expr) bool {
	if st, ok := e.(*ast.StarExpr); ok {
		e = st.X
	}
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "guardIdentity"
}

// hostedWithToken registers a hosted session on e, with a host token,
// whose agent token is tok: tok is then a live agent token.
func hostedWithToken(t *testing.T, e *testEnv, localID, tok string) {
	t.Helper()
	dialFakeHostWith(t, e, proto.HostInfo{Name: "box", Instance: "inst-live"},
		proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: "/tmp", Cols: 80, Rows: 24, LocalID: localID, AgentToken: tok}, "Bearer test-host-token")
	if !e.srv.agentTokens.holds(tok) {
		t.Fatal("the hosted session's token is not live")
	}
}

// A session's live agent token is never a host credential: a host
// connection admitted through the provider that renews to one is closed
// 4403 as for a token the provider refuses, and the provider is never
// asked about it, though it would admit it.
func TestARenewalToALiveAgentTokenIsRefusedUnasked(t *testing.T) {
	f := newCredIdentity()
	e, _ := credEnv(t, f)
	live := unminted("l")
	f.set(func(f *credIdentity) { f.admits[live] = credHost })
	hostedWithToken(t, e, "live", live)
	h, _ := dialCred(t, e, hostReg("1", nil), credOld)
	h.send(hostToken(live))
	closedRevoked(t, h)
	if n := f.timesAsked(live); n != 0 {
		t.Fatalf("the provider was asked %d times about a live agent token offered as a host credential", n)
	}
}

// A renewal kept pending (every renewal slot taken) that has since become
// a session's live agent token is refused at the connection's recheck, the
// provider never asked about it, though it would admit it.
func TestAPendingRenewalThatBecameALiveAgentTokenIsRefusedUnasked(t *testing.T) {
	f := newCredIdentity()
	e, clock := credEnv(t, f)
	live := unminted("p")
	f.set(func(f *credIdentity) { f.admits[live] = credHost })
	h, _ := dialCred(t, e, hostReg("1", nil), credOld)
	first := clock.next(t, identityRecheckEvery)
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	fillRenewals(t, e, clock, f, gate)
	h.send(hostToken(live))
	h.link("view", "") // read after the renewal: kept pending, the connection stayed
	if n := f.timesAsked(live); n != 0 {
		t.Fatal("a renewal past every slot was asked")
	}
	hostedWithToken(t, e, "pending", live)
	first.fire <- time.Now()
	closedRevoked(t, h)
	if n := f.timesAsked(live); n != 0 {
		t.Fatalf("the recheck asked the provider %d times about a pending renewal that is a live agent token", n)
	}
}
