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
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
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
// "no grant", "not mine" (ok false). A token's grace asks are answered
// first; one used in the late state moves the guard's wait to a nanosecond
// for the next ask (extWaiting), so the ask after it is the late one.
type judgeIdentity struct {
	mu    sync.Mutex
	state string
	grace map[string]int
	asked map[string]int
}

func (*judgeIdentity) Name() string                          { return "cloud" }
func (*judgeIdentity) Start(context.Context, ext.Host) error { return nil }

func (f *judgeIdentity) Authenticate(ctx context.Context, tok string) (ext.Principal, bool, error) {
	f.mu.Lock()
	f.asked[tok]++
	state := f.state
	if f.grace[tok] > 0 {
		f.grace[tok]--
		if state == "late" {
			setGuardWait(time.Nanosecond)
		}
		state = ""
	}
	f.mu.Unlock()
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

// set puts the provider in state, with grace asks of tok answered first.
func (f *judgeIdentity) set(state, tok string, grace int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state, f.grace = state, map[string]int{tok: grace}
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
	path := strings.ReplaceAll(pl.path, "{sid}", sid)
	var body io.Reader
	if pl.body != "" {
		body = strings.NewReader(pl.body)
	}
	r := httptest.NewRequest(pl.method, path, body)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
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
			f.set(state, tok, grace)
			if state == "late" {
				if grace > 0 {
					setGuardWait(time.Hour)
				} else {
					setGuardWait(time.Nanosecond)
				}
			}
			rec := askPlace(e.srv, pl, sid, tok)
			extWaiting.Store(nil)
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
		f.set("", "", 0)
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
				f.set(state, tok, 0)
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
		f.set(state, "", 0)
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

// The places that make a principal from a request are the table's, each
// one reached through tokenPrincipal, whose second result says whether it
// answered the request and is never dropped; and notChecked alone writes
// the code not_checked.
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
			ast.Inspect(fn.Body, func(n ast.Node) bool {
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

// makesPrincipal says e calls authenticate or tokenPrincipal.
func makesPrincipal(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "authenticate" || sel.Sel.Name == "tokenPrincipal")
}
