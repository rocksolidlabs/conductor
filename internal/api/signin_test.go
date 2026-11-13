package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// signInEnv is a server whose identity provider is p (none for nil),
// registered as "cloud", logging everything to the buffer it answers.
func signInEnv(t *testing.T, p ext.IdentityProvider) (*testEnv, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	var opts []Option
	if p != nil {
		opts = append(opts, withIdentity(p))
	}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), nil, opts...)
	return e, logs
}

// signInCall makes one call of the sign-in's routes with tok and body ("":
// none) and answers its status and its body as sent.
func signInCall(t *testing.T, e *testEnv, method, tok, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.http.URL+"/api/signin", rd)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// signInStatusOf is a 200's reply decoded as a reader would, refusing a
// field the reply does not have.
func signInStatusOf(t *testing.T, code int, body string) signInReply {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("%d %s, want 200", code, body)
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var r signInReply
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("the reply %q: %v", body, err)
	}
	return r
}

// signInErrorCode is an error reply's code.
func signInErrorCode(t *testing.T, body string) string {
	t.Helper()
	var r struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("the error %q: %v", body, err)
	}
	return r.Error.Code
}

// starterSignIn offers a sign-in and needs a Start, which no test here
// makes: its slot answers the free default.
type starterSignIn struct{ *exttest.FakeSignIn }

func (starterSignIn) Start(context.Context, ext.Host) error { return errors.New("not yet") }

// A plain build, the free default, a provider with no sign-in and one
// whose Start has not succeeded all answer the read {"supported": false}
// and the begin and the sign-out 404 not_supported, asking nothing.
func TestSignInPlainBuild(t *testing.T) {
	notStarted := starterSignIn{exttest.NewFakeSignIn("cloud")}
	for _, c := range []struct {
		name string
		p    ext.IdentityProvider
	}{
		{"no provider", nil},
		{"the free default", local.Identity{}},
		{"a provider with no sign-in", exttest.NewFakeIdentity("cloud")},
		{"a provider not yet started", notStarted},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := signInEnv(t, c.p)
			code, body := signInCall(t, e, "GET", adminToken, "")
			if code != http.StatusOK || strings.TrimSpace(body) != `{"supported":false}` {
				t.Fatalf("GET: %d %s, want 200 {\"supported\":false}", code, body)
			}
			for _, m := range []string{"POST", "DELETE"} {
				for _, b := range []string{"", "{}"} {
					if code, body := signInCall(t, e, m, adminToken, b); code != http.StatusNotFound || signInErrorCode(t, body) != "not_supported" {
						t.Fatalf("%s %q: %d %s, want 404 not_supported", m, b, code, body)
					}
				}
			}
		})
	}
	if notStarted.Begins() != 0 || notStarted.SignOuts() != 0 {
		t.Errorf("a provider not yet started was asked: %d begins, %d sign-outs", notStarted.Begins(), notStarted.SignOuts())
	}
}

// Through a fake provider: signed out, begun (starting), a code, signed
// in, signed out again; each reply the provider's status as it stands,
// with no body or {}; and nothing of a status in the server's log.
func TestSignInRoutes(t *testing.T) {
	f := exttest.NewFakeSignIn("cloud")
	e, logs := signInEnv(t, f)
	read := func() signInReply {
		t.Helper()
		code, body := signInCall(t, e, "GET", adminToken, "")
		return signInStatusOf(t, code, body)
	}
	if r := read(); !r.Supported || r.State != ext.SignedOut {
		t.Fatalf("at first: %+v, want supported and signed out", r)
	}
	code, body := signInCall(t, e, "POST", adminToken, "")
	if r := signInStatusOf(t, code, body); !r.Supported || r.State != ext.SignInStarting || f.Begins() != 1 {
		t.Fatalf("begun: %+v, %d begins", r, f.Begins())
	}
	expires := time.Date(2026, 11, 12, 9, 15, 0, 0, time.UTC)
	f.SetStatus(ext.SignInStatus{State: ext.SignInCode, Code: "WDJB-MJHT", URL: "https://id.example.com/device", Expires: expires})
	if r := read(); r.State != ext.SignInCode || r.Code != "WDJB-MJHT" || r.URL != "https://id.example.com/device" || !r.Expires.Equal(expires) {
		t.Fatalf("a code: %+v", r)
	}
	f.SetStatus(ext.SignInStatus{State: ext.SignedIn, Who: "Ana · workspace Acme"})
	if r := read(); r.State != ext.SignedIn || r.Who != "Ana · workspace Acme" || r.Code != "" || r.URL != "" {
		t.Fatalf("signed in: %+v", r)
	}
	f.OnSignOut(ext.SignInStatus{State: ext.SignedOut, Why: "signed out from Settings"})
	code, body = signInCall(t, e, "DELETE", adminToken, "{}")
	if r := signInStatusOf(t, code, body); r.State != ext.SignedOut || r.Why != "signed out from Settings" || f.SignOuts() != 1 {
		t.Fatalf("signed out: %+v, %d sign-outs", r, f.SignOuts())
	}
	code, body = signInCall(t, e, "POST", adminToken, "{}")
	if r := signInStatusOf(t, code, body); r.State != ext.SignInStarting || f.Begins() != 2 {
		t.Fatalf("begun with {}: %+v, %d begins", r, f.Begins())
	}

	// Reads, begins and sign-outs at once, the status changing under
	// them: each answers a status the provider held (fewer at once than
	// the guard's slots, so none is busy).
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			method := []string{"GET", "POST", "DELETE"}[i%3]
			if code, body := signInCall(t, e, method, adminToken, ""); code != http.StatusOK {
				t.Errorf("%s at once: %d %s", method, code, body)
			}
		}()
	}
	f.SetStatus(ext.SignInStatus{State: ext.SignedIn, Who: "Ana · workspace Acme"})
	wg.Wait()

	for _, secret := range []string{"WDJB-MJHT", "Ana", "id.example.com", "signed out from Settings"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log quotes a sign-in's %q:\n%s", secret, logs.String())
		}
	}
}

// signInGate is a provider whose sign-in calls do what the test says: hold
// until released (late), panic, or answer an error that quotes a secret.
type signInGate struct {
	*exttest.FakeSignIn
	mu      sync.Mutex
	hold    chan struct{} // non-nil: SignInStatus waits for it to close
	panics  bool
	failure error
}

func (g *signInGate) SignInStatus() ext.SignInStatus {
	g.mu.Lock()
	hold, panics := g.hold, g.panics
	g.mu.Unlock()
	if panics {
		panic("the status of " + leakWords)
	}
	if hold != nil {
		<-hold
	}
	return g.FakeSignIn.SignInStatus()
}

func (g *signInGate) BeginSignIn(ctx context.Context) error {
	g.mu.Lock()
	err := g.failure
	g.mu.Unlock()
	if err != nil {
		return err
	}
	return g.FakeSignIn.BeginSignIn(ctx)
}

// leakWords stands for what a provider's failure may quote.
const leakWords = "secret-from-the-provider"

// A body past 1 KiB, a member, a status out of its bounds, a late, a
// panicking and a failing provider: each refused, the bodies before the
// provider is asked, the provider's answers as 503 signin_unavailable in
// the server's words, none quoting the provider's; and a valid status at
// its longest fits the reply's bound.
func TestSignInLimits(t *testing.T) {
	g := &signInGate{FakeSignIn: exttest.NewFakeSignIn("cloud")}
	e, logs := signInEnv(t, g)

	for _, m := range []string{"POST", "DELETE"} {
		if code, body := signInCall(t, e, m, adminToken, `{"x":"`+strings.Repeat("a", maxSignInBody)+`"}`); code != http.StatusRequestEntityTooLarge || signInErrorCode(t, body) != "bound" {
			t.Errorf("%s past 1 KiB: %d %s, want 413 bound", m, code, body)
		}
		for _, b := range []string{`{"force":true}`, `[]`, `{}{}`, `"x"`} {
			if code, body := signInCall(t, e, m, adminToken, b); code != http.StatusBadRequest || signInErrorCode(t, body) != "invalid_request" {
				t.Errorf("%s %s: %d %s, want 400 invalid_request", m, b, code, body)
			}
		}
	}
	if g.Begins() != 0 || g.SignOuts() != 0 {
		t.Fatalf("a refused body reached the provider: %d begins, %d sign-outs", g.Begins(), g.SignOuts())
	}

	unavailable := func(what, method string) {
		t.Helper()
		code, body := signInCall(t, e, method, adminToken, "")
		if code != http.StatusServiceUnavailable || signInErrorCode(t, body) != "signin_unavailable" {
			t.Fatalf("%s: %d %s, want 503 signin_unavailable", what, code, body)
		}
		if strings.Contains(body, leakWords) || strings.Contains(body, "http://") || strings.Contains(body, strings.Repeat("w", 20)) {
			t.Fatalf("%s: the reply quotes the provider: %s", what, body)
		}
	}
	for _, c := range []struct {
		what string
		s    ext.SignInStatus
	}{
		{"who past its bound", ext.SignInStatus{State: ext.SignedIn, Who: strings.Repeat("w", ext.MaxSignInWho+1)}},
		{"a code while signed out", ext.SignInStatus{State: ext.SignedOut, Code: "WDJB-MJHT"}},
		{"a page over http", ext.SignInStatus{State: ext.SignInCode, Code: "WDJB-MJHT", URL: "http://id.example.com/device"}},
		{"no state", ext.SignInStatus{}},
	} {
		g.SetStatus(c.s)
		unavailable(c.what, "GET")
	}

	g.SetStatus(ext.SignInStatus{State: ext.SignedOut})
	g.mu.Lock()
	g.failure = errors.New("could not begin: " + leakWords)
	g.mu.Unlock()
	unavailable("a begin that fails", "POST")
	g.mu.Lock()
	g.failure, g.panics = nil, true
	g.mu.Unlock()
	unavailable("a status that panics", "GET")

	// Late: the status waits on a gate the test opens only once the
	// reply is in, so the guard's wait ends first whatever the load.
	hold := make(chan struct{})
	g.mu.Lock()
	g.panics, g.hold = false, hold
	g.mu.Unlock()
	unavailable("a late status", "GET")
	close(hold)
	g.mu.Lock()
	g.hold = nil
	g.mu.Unlock()

	if strings.Contains(logs.String(), leakWords) {
		t.Errorf("the log quotes the provider's words:\n%s", logs.String())
	}

	// A valid status at its longest in each state fits the reply's bound,
	// every byte that JSON escapes escaped.
	worst := func(n int) string { return strings.Repeat(`"`, n) }
	far := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -(11*3600+59*60)))
	for _, s := range []ext.SignInStatus{
		{State: ext.SignedIn, Who: worst(ext.MaxSignInWho), Why: worst(ext.MaxSignInWhy)},
		{State: ext.SignedIn, Who: strings.Repeat("\u2028", ext.MaxSignInWho/3), Why: strings.Repeat("\u2028", ext.MaxSignInWhy/3)},
		{State: ext.SignInCode, Code: strings.Repeat("-", ext.MaxSignInCode), URL: "https://e.example/" + strings.Repeat("&", ext.MaxSignInURL-len("https://e.example/")), Expires: far, Why: worst(ext.MaxSignInWhy)},
		{State: ext.SignedOut, Why: worst(ext.MaxSignInWhy)},
	} {
		if err := s.Valid(); err != nil {
			t.Fatalf("the longest status is not valid: %v", err)
		}
		g.SetStatus(s)
		code, body := signInCall(t, e, "GET", adminToken, "")
		if r := signInStatusOf(t, code, body); r.SignInStatus.Who != s.Who || len(body) > maxSignInReply {
			t.Errorf("a %s status at its longest: %d bytes, past %d", s.State, len(body), maxSignInReply)
		}
	}
}

// The routes are the admin's: no credential, a host token, a link's
// token, an unknown one and a provider's principal without the admin
// grant (a decide one, a host one) are 401, the provider asked nothing;
// a provider's principal with the admin grant is admitted.
func TestSignInAdminOnly(t *testing.T) {
	f := exttest.NewFakeSignIn("cloud")
	f.Add("tok-decide", ext.Principal{Subject: "eve", Name: "Eve", Grants: []ext.Grant{ext.GrantDecide}})
	f.Add("tok-host", ext.Principal{Subject: "hal", Name: "Hal", Grants: []ext.Grant{ext.GrantHost}})
	f.Add("tok-admin", cloudAdmin("ana"))
	e, _ := signInEnv(t, f)
	_, linkTok, err := e.srv.links.Create("s1", session.RoleControl, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", "test-host-token", linkTok, "not-a-token", "tok-decide", "tok-host"} {
		for _, m := range []string{"GET", "POST", "DELETE"} {
			if code, body := signInCall(t, e, m, tok, ""); code != http.StatusUnauthorized {
				t.Errorf("%s with %q: %d %s, want 401", m, tok, code, body)
			}
		}
	}
	if f.Begins() != 0 || f.SignOuts() != 0 {
		t.Fatalf("a refused caller reached the sign-in: %d begins, %d sign-outs", f.Begins(), f.SignOuts())
	}
	code, body := signInCall(t, e, "POST", "tok-admin", "")
	if r := signInStatusOf(t, code, body); r.State != ext.SignInStarting || f.Begins() != 1 {
		t.Fatalf("the provider's admin: %+v, %d begins", r, f.Begins())
	}
}
