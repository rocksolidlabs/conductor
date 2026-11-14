package api

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/session"
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

// holdSlots takes every call slot of the identity guard named name, and
// gives them back when t ends.
func holdSlots(t *testing.T, srv *Server, name string) func() {
	t.Helper()
	var held []extSlot
	for range extInFlight {
		sl, ok := srv.extTake(ext.KindIdentity, name)
		if !ok {
			t.Fatal("a slot was taken already")
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

// The workbench token, a host token, a link and a session's own agent
// token are answered as ever in every state of the provider, which is
// never asked about them.
func TestChainTokensNeverWaitOnTheProvider(t *testing.T) {
	e, f, sid := notCheckedEnv(t)
	t.Cleanup(func() { extWaiting.Store(nil) })
	agentTok := e.agentToken(sid)
	_, linkTok, err := e.srv.links.Create(sid, session.RoleView, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	type ask struct {
		pl   principalPlace
		tok  string
		want int
	}
	asks := []ask{
		{principalPlace{method: "GET", path: "/api/whoami"}, adminToken, http.StatusOK},
		{principalPlace{method: "GET", path: "/api/whoami"}, "test-host-token", http.StatusUnauthorized},
		{principalPlace{method: "GET", path: "/api/me"}, "test-host-token", http.StatusOK},
		{principalPlace{method: "GET", path: "/api/sessions/{sid}"}, linkTok, http.StatusOK},
		{principalPlace{method: "POST", path: "/api/sessions/{sid}/attention", body: `{"state":"working"}`}, agentTok, http.StatusOK},
	}
	run := func(state string) {
		t.Helper()
		for _, a := range asks {
			if rec := askPlace(e.srv, a.pl, sid, a.tok); rec.Code != a.want {
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
	o := ownTokens{e: b.testEnv, sid: b.createSession("cat")}
	o.tok = b.agentToken(o.sid)
	b.enable(t, "bk", nil)
	req := b.briefing(t)
	o.turnSid, o.turnTok, _ = b.turnSession(t, req)
	b.waitState(t, req, brakeman.StateRunning)
	return o
}

// A live session's agent token and a live Brakeman turn's token (named as
// a turn names its session, and not), at every place that makes a
// principal from a request and in every state of the provider, are
// answered as the same request is by a server with no provider registered,
// and the provider is never asked about them. A session that has ended
// holds no token of the server's own: its token is an unknown one.
func TestOwnTokensAnsweredAsWithoutProvider(t *testing.T) {
	f := &judgeIdentity{asked: map[string]int{}}
	with, without := ownTokenEnv(t, f), ownTokenEnv(t, nil)
	t.Cleanup(func() { extWaiting.Store(nil) })
	type answer struct {
		code      int
		auth, err string
	}
	answerOf := func(rec *httptest.ResponseRecorder) answer {
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return answer{rec.Code, rec.Header().Get("WWW-Authenticate"), codeOf(out) + ": " + apiMessage(out)}
	}
	presented := []struct {
		what      string
		tok, turn func(o ownTokens) string
	}{
		{"a session's agent token", func(o ownTokens) string { return o.tok }, func(ownTokens) string { return "" }},
		{"a turn's token", func(o ownTokens) string { return o.turnTok }, func(ownTokens) string { return "" }},
		{"a turn's token, its session named", func(o ownTokens) string { return o.turnTok }, func(o ownTokens) string { return o.turnSid }},
	}
	run := func(state string) {
		t.Helper()
		for _, pl := range principalPlaces {
			for _, p := range presented {
				want := answerOf(askAs(without.e.srv, pl, without.sid, p.tok(without), p.turn(without), nil))
				if state == "late" {
					setGuardWait(time.Nanosecond)
				}
				got := answerOf(askAs(with.e.srv, pl, with.sid, p.tok(with), p.turn(with), &judgeCase{state: state}))
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
	for _, tok := range []string{with.tok, with.turnTok} {
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

// The places that make a principal from a request are the table's, each
// one reached through tokenPrincipal, whose second result says whether it
// answered the request and is never dropped; the provider is asked about a
// request's token by the maker alone (asksProvider); and notChecked alone
// writes the code not_checked.
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
				if call, ok := n.(*ast.CallExpr); ok {
					if ask := asksProvider(call, guards); ask != "" && !askerOf(ask, fn) {
						t.Errorf("%s: %s asks the provider (%s) outside the maker", fset.Position(call.Pos()), fn.Name.Name, ask)
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
// is set; once it is not, a publication.
type withheldPublisher struct {
	status int
	refuse atomic.Bool
	tries  atomic.Int32
}

func (p *withheldPublisher) Publish(_ context.Context, local *session.Local) (PublishedSession, error) {
	p.tries.Add(1)
	if p.refuse.Load() {
		return nil, &hostagent.RefusedError{Status: p.status, Code: http.StatusText(p.status), Withheld: true}
	}
	return &gatedPublication{id: "there-" + local.Info().ID, done: make(chan struct{})}, nil
}

func (p *withheldPublisher) Server() string { return "https://rendezvous.example.net" }

// A publication whose registration presented no credential, its provider
// withholding one, is owed once it ends without a publication, whatever
// the refusal's status: refused 401 after its one try, refused 429 or 503
// once its tries again have ended. A Share once the credential is back
// publishes it.
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
// call makes ("providerPrincipal", the guard's "judge", or "Authenticate"
// of the server's identity extension or a guard), or "": guards are the
// names the function holds a guardIdentity by.
func asksProvider(call *ast.CallExpr, guards map[string]bool) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	switch name := sel.Sel.Name; {
	case name == "providerPrincipal":
		return name
	case name == "judge" && isGuard(sel.X, guards):
		return name
	case name == "Authenticate" && isGuard(sel.X, guards):
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
