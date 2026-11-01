package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// tokenIdentity is an identity provider that knows the tokens of known,
// each its principal, and notes every token it is asked about.
type tokenIdentity struct {
	known map[string]ext.Principal

	mu    sync.Mutex
	asked []string
}

func (*tokenIdentity) Name() string { return "cloud" }

func (f *tokenIdentity) Authenticate(_ context.Context, tok string) (ext.Principal, bool, error) {
	f.mu.Lock()
	f.asked = append(f.asked, tok)
	f.mu.Unlock()
	p, ok := f.known[tok]
	return p, ok, nil
}

// askedTokens is every token the provider was asked about, in order.
func (f *tokenIdentity) askedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// withIdentity is the option of a server whose identity provider is p,
// registered as "cloud", every other slot its free default.
func withIdentity(p ext.IdentityProvider) Option {
	return WithExtensions(local.Fill(ext.Providers{
		Identity: p,
		All:      []ext.Entry{{Kind: ext.KindIdentity, Name: "cloud", Value: p}},
	}))
}

// providerEnv is a server over a data directory with the ready cat of the
// managed-run tests, its identity provider p (none for nil), and mutate
// applied to its configuration.
func providerEnv(t *testing.T, mutate func(*config.Config), p ext.IdentityProvider) *testEnv {
	t.Helper()
	var opts []Option
	if p != nil {
		opts = append(opts, withIdentity(p))
	}
	return newTestEnvAgents(t, mutate, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}}, opts...)
}

// cloudAdmin is a provider's principal with the admin grant.
func cloudAdmin(subject string) ext.Principal {
	return ext.Principal{Subject: subject, Name: "Cloud " + subject[:min(len(subject), 8)], Grants: []ext.Grant{ext.GrantAdmin}}
}

// tokenRequest is a request presenting tok in its Authorization header or,
// byQuery, in its token query parameter.
func tokenRequest(tok string, byQuery bool) *http.Request {
	r := httptest.NewRequest("GET", "/api/sessions/s1", nil)
	switch {
	case byQuery:
		r.URL.RawQuery = "token=" + url.QueryEscape(tok)
	case tok != "":
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

// expiredLink is the token of a view link on the session that expired an
// hour ago, put in the server's store through Restore, as a link kept on
// disk comes back: expired by its own time, with no clock to wait on.
func expiredLink(t *testing.T, e *testEnv, sid string) string {
	t.Helper()
	tok, hash := share.NewToken()
	past := time.Now().UTC().Add(-time.Hour)
	e.srv.links.Restore(share.Link{ID: session.NewID(), SessionID: sid, Role: session.RoleView, CreatedAt: past.Add(-time.Hour), ExpiresAt: &past}, hash)
	if _, err := e.srv.links.Resolve(tok); !errors.Is(err, share.ErrExpired) {
		t.Fatalf("the link is not expired: %v", err)
	}
	return tok
}

// samePrincipal fails t unless got is the principal want is, field by
// field: what a route reads of it.
func samePrincipal(t *testing.T, what string, got, want principal) {
	t.Helper()
	if got.admin != want.admin || got.yard != want.yard || got.host != want.host ||
		got.subject != want.subject || got.name != want.name || got.linkID() != want.linkID() ||
		(got.runOf != nil) != (want.link != nil) {
		t.Fatalf("%s: principal %+v, want %+v", what, got, want)
	}
}

// With no identity provider, and with the free default, every kind of
// token gives the principal main gives, by header and by query, on a
// server and on a switchyard.
func TestTokenChainUnchanged(t *testing.T) {
	for _, yard := range []bool{false, true} {
		for _, with := range []string{"no provider", "the default"} {
			t.Run(fmt.Sprintf("switchyard %v, %s", yard, with), func(t *testing.T) {
				var opts []Option
				if with == "the default" {
					opts = append(opts, WithExtensions(local.Fill(ext.Providers{})))
				}
				e := newTestEnvAgents(t, func(c *config.Config) { c.Switchyard.Enabled = yard }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, opts...)
				if g, ok := e.srv.exts.identity.(guardIdentity); !ok || g.p != (local.Identity{}) {
					t.Fatalf("the identity slot: %#v", e.srv.exts.identity)
				}
				link, linkTok, err := e.srv.links.Create("s1", session.RoleView, "", 0)
				if err != nil {
					t.Fatal(err)
				}
				revoked, revokedTok, err := e.srv.links.Create("s1", session.RoleControl, "", 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := e.srv.links.Revoke("s1", revoked.ID); !ok {
					t.Fatal("the link was not revoked")
				}
				expiredTok := expiredLink(t, e, "s1")
				want := map[string]principal{
					adminToken:        {admin: true, yard: yard, host: true},
					"test-host-token": {host: true},
					linkTok:           {link: link},
					revokedTok:        {},
					expiredTok:        {},
					"not-a-token":     {},
					"":                {},
				}
				for tok, w := range want {
					what := tok
					if tok == linkTok {
						what = "a link"
					} else if tok == revokedTok {
						what = "a revoked link"
					} else if tok == expiredTok {
						what = "an expired link"
					}
					samePrincipal(t, what+" by header", e.srv.authenticate(tokenRequest(tok, false), false), w)
					samePrincipal(t, what+" by query", e.srv.authenticate(tokenRequest(tok, true), true), w)
					// A route that takes no query token reads none.
					samePrincipal(t, what+" by query where none is read", e.srv.authenticate(tokenRequest(tok, true), false), principal{})
					if got := e.srv.authenticate(tokenRequest(tok, false), false).actor(); got != adminActor {
						t.Fatalf("%s: actor %q", what, got)
					}
					if got := e.srv.hostAllowed(tokenRequest(tok, false), tok); got != w.host {
						t.Fatalf("%s: a host registration allowed %v", what, got)
					}
				}
				// The workbench's receipts are the admin's, as on main.
				if got := actorOf(withPrincipal(tokenRequest(adminToken, false), principal{admin: true})); got != "admin" {
					t.Fatalf("the workbench's actor: %q", got)
				}
				if got := actorOf(tokenRequest(adminToken, false)); got != "admin" {
					t.Fatalf("a request without a principal: %q", got)
				}
			})
		}
	}
}

// The provider is asked last: a chain token (the workbench's, a host's, a
// link's), a revoked link, an expired link and a session's agent token
// never reach it; a token none of them knows does.
func TestProviderAskedLast(t *testing.T) {
	id := &tokenIdentity{}
	e := providerEnv(t, nil, id)
	sid := e.createSession("cat")
	agentTok := e.agentToken(sid)
	_, linkTok, err := e.srv.links.Create(sid, session.RoleView, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	revoked, revokedTok, err := e.srv.links.Create(sid, session.RoleControl, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.links.Revoke(sid, revoked.ID)
	expiredTok := expiredLink(t, e, sid)

	if resp, _ := e.do("GET", "/api/sessions", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the workbench token: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/api/sessions/"+sid, linkTok, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("a link: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/api/sessions/"+sid, revokedTok, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked link: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/api/sessions", revokedTok, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked link at an admin route: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/api/sessions/"+sid, expiredTok, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an expired link: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/api/sessions", expiredTok, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an expired link at an admin route: %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/sessions/"+sid+"/attention", agentTok, map[string]any{"state": "working"}); resp.StatusCode >= 300 {
		t.Fatalf("the agent token: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/api/sessions/"+sid+"/task", agentTok, nil); resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("the agent token at the task route: %d", resp.StatusCode)
	}
	dialFakeHost(t, e, "hosted-agent-token")
	if code := dialHostRaw(t, e, "Bearer "+linkTok); code != http.StatusUnauthorized {
		t.Fatalf("a link at the host route: %d", code)
	}
	if code := dialHostRaw(t, e, "Bearer "+expiredTok); code != http.StatusUnauthorized {
		t.Fatalf("an expired link at the host route: %d", code)
	}
	if got := id.askedTokens(); len(got) != 0 {
		t.Fatalf("the provider was asked about %d chain tokens", len(got))
	}
	// A token the chain does not know is the provider's to answer.
	if resp, _ := e.do("GET", "/api/sessions", "cloud-unknown", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unknown token: %d", resp.StatusCode)
	}
	if got := id.askedTokens(); !slices.Equal(got, []string{"cloud-unknown"}) {
		t.Fatalf("the provider was asked about %v", got)
	}
}

// A provider's principal is what its grants make it: the admin grant what
// the workbench token makes a caller (a switchyard's yard on one), the host
// grant a host token's.
func TestProviderPrincipal(t *testing.T) {
	id := &tokenIdentity{known: map[string]ext.Principal{
		"cloud-admin": cloudAdmin("u1"),
		"cloud-host":  {Subject: "u2", Name: "Host", Grants: []ext.Grant{ext.GrantHost}},
		"cloud-both":  {Subject: "u3", Name: "Both", Grants: []ext.Grant{ext.GrantHost, ext.GrantAdmin}},
	}}
	for _, yard := range []bool{false, true} {
		t.Run(fmt.Sprintf("switchyard %v", yard), func(t *testing.T) {
			e := providerEnv(t, func(c *config.Config) { c.Switchyard.Enabled = yard }, id)
			samePrincipal(t, "admin", e.srv.authenticate(tokenRequest("cloud-admin", false), false), principal{admin: true, yard: yard, host: true, subject: "u1", name: "Cloud u1"})
			samePrincipal(t, "host", e.srv.authenticate(tokenRequest("cloud-host", true), true), principal{host: true, subject: "u2", name: "Host"})
			samePrincipal(t, "both", e.srv.authenticate(tokenRequest("cloud-both", false), false), principal{admin: true, yard: yard, host: true, subject: "u3", name: "Both"})
			wantRole := session.RoleControl
			if yard {
				wantRole = ""
			}
			if got := e.srv.authenticate(tokenRequest("cloud-admin", false), false).role("any"); got != wantRole {
				t.Fatalf("the admin's role: %q", got)
			}
			if resp, _ := e.do("GET", "/api/sessions", "cloud-admin", nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("the admin grant at an admin route: %d", resp.StatusCode)
			}
			if resp, _ := e.do("GET", "/api/sessions", "cloud-host", nil); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("the host grant at an admin route: %d", resp.StatusCode)
			}
			if resp, out := e.do("GET", "/api/whoami", "cloud-admin", nil); resp.StatusCode != http.StatusOK || out["switchyard"] != yard {
				t.Fatalf("whoami: %d %v", resp.StatusCode, out)
			}
		})
	}
}

// A provider's principal with either grant registers a hosted session, as a
// host token does; one with no grant, and a token nobody knows, do not.
func TestProviderHostRegistration(t *testing.T) {
	id := &tokenIdentity{known: map[string]ext.Principal{
		"cloud-admin":   cloudAdmin("u1"),
		"cloud-host":    {Subject: "u2", Name: "Host", Grants: []ext.Grant{ext.GrantHost}},
		"cloud-nogrant": {Subject: "u3", Name: "None"},
	}}
	e := providerEnv(t, nil, id)
	for _, tok := range []string{"cloud-host", "cloud-admin"} {
		h := dialFakeHostAuth(t, e, "agent-"+tok, "Bearer "+tok)
		if _, ok := e.srv.registry.Get(h.sessionID); !ok {
			t.Fatalf("%s: the hosted session is not listed", tok)
		}
	}
	for _, tok := range []string{"cloud-nogrant", "cloud-unknown"} {
		if code := dialHostRaw(t, e, "Bearer "+tok); code != http.StatusUnauthorized {
			t.Fatalf("%s at the host route: %d", tok, code)
		}
	}
}

// A provider that is late, fails, panics, or answers a subject past 48
// bytes, a grant it may not give or no grant is no principal: 401 within
// IdentityWait, the token refused at the host route too. The warn line
// names the failure's kind and count and never the provider's words, which
// quote the token.
func TestProviderRefused(t *testing.T) {
	type lateCall struct {
		left time.Duration
		ok   bool
		err  error
	}
	late := make(chan lateCall, 1)
	const secret = "s3cr3t-tok"
	id := fakeIdentity{answer: func(ctx context.Context, tok string) (ext.Principal, bool, error) {
		switch {
		case strings.HasPrefix(tok, "late-"):
			dl, ok := ctx.Deadline()
			left := time.Until(dl)
			<-ctx.Done()
			late <- lateCall{left: left, ok: ok, err: ctx.Err()}
			return cloudAdmin("u1"), true, nil
		case strings.HasPrefix(tok, "fail-"):
			return ext.Principal{}, false, fmt.Errorf("the token %s is not ours", tok)
		case strings.HasPrefix(tok, "panic-"):
			panic("the token " + tok + " broke us")
		case strings.HasPrefix(tok, "long-"):
			return cloudAdmin(strings.Repeat("u", ext.MaxSubject+1)), true, nil
		case strings.HasPrefix(tok, "badgrant-"):
			return ext.Principal{Subject: "u1", Name: "U", Grants: []ext.Grant{ext.Grant("root-" + tok)}}, true, nil
		case strings.HasPrefix(tok, "nogrant-"):
			return ext.Principal{Subject: "u1", Name: tok[:min(len(tok), 40)]}, true, nil
		}
		return ext.Principal{}, false, nil
	}}
	var logs syncLog
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := newExtServer(t, log, withIdentity(id))
	// Every failure a minute after the last, so each is logged.
	var minutes atomic.Int64
	srv.extClock = func() time.Time { return time.Unix(0, 0).Add(time.Duration(minutes.Add(1)) * time.Minute) }
	h := srv.Handler()
	for _, c := range []struct{ prefix, failure string }{
		{"late-", "late"},
		{"fail-", "error"},
		{"panic-", "panic"},
		{"long-", "invalid"},
		{"badgrant-", "invalid"},
		{"nogrant-", "invalid"},
	} {
		tok := c.prefix + secret
		before := strings.Count(logs.String(), "failure="+c.failure)
		req := tokenRequest(tok, false)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", c.prefix, rec.Code)
		}
		if c.prefix == "late-" {
			// The guard gave the call IdentityWait at most and ended it.
			lc := <-late
			if !lc.ok || lc.left > ext.IdentityWait || !errors.Is(lc.err, context.DeadlineExceeded) {
				t.Fatalf("the late call: %+v", lc)
			}
		}
		if got := strings.Count(logs.String(), "failure="+c.failure); got != before+1 {
			t.Fatalf("%s: %d warn lines of failure=%s, want %d:\n%s", c.prefix, got, c.failure, before+1, logs.String())
		}
		if c.prefix != "late-" && srv.hostAllowed(tokenRequest(tok, false), tok) {
			t.Fatalf("%s: a host registration allowed", c.prefix)
		}
	}
	out := logs.String()
	if strings.Contains(out, secret) {
		t.Fatalf("the log quotes a token:\n%s", out)
	}
	if strings.Contains(out, "not ours") || strings.Contains(out, "broke us") || strings.Contains(out, "root-") {
		t.Fatalf("the log quotes the provider's words:\n%s", out)
	}
	for _, want := range []string{"kind=identity", "provider=cloud", "failures=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log has no %s:\n%s", want, out)
		}
	}
}

// providerToken is the token the receipts tests present for a provider's
// principal with the admin grant.
const providerToken = "cloud-admin-token"

// A pause, a cancel, a retry and Dispatch's creates made with a provider's
// token are kept under user:<subject>: the ledger's command rows, Dispatch's
// receipts and changes, and the reads of each, which find the caller's
// alone; the workbench's are still the admin's.
func TestProviderPrincipalInReceipts(t *testing.T) {
	id := &tokenIdentity{known: map[string]ext.Principal{providerToken: cloudAdmin("u1")}}
	e := providerEnv(t, nil, id)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, run.ID, "build", 1)
	base := "/api/runs/" + run.ID
	for _, c := range []struct{ path, cmd string }{
		{"/pause", "p1"},
		{"/tasks/docs/cancel", "p2"},
		{"/tasks/docs/retry", "p3"},
	} {
		resp, out := e.do("POST", base+c.path, providerToken, map[string]any{"commandId": c.cmd})
		if rc := receiptOf(t, out); resp.StatusCode != http.StatusOK || rc["status"] != "done" {
			t.Fatalf("%s: %d %v", c.path, resp.StatusCode, out)
		}
	}
	if resp, out := e.do("POST", base+"/unpause", adminToken, map[string]any{"commandId": "w1"}); resp.StatusCode != http.StatusOK || receiptOf(t, out)["status"] != "done" {
		t.Fatalf("the workbench's unpause: %d %v", resp.StatusCode, out)
	}
	ctx := t.Context()
	for _, c := range []struct {
		actor, cmd string
		found      bool
	}{
		{"user:u1", "p1", true}, {"user:u1", "p2", true}, {"user:u1", "p3", true}, {"user:u1", "w1", false},
		{"admin", "p1", false}, {"admin", "p3", false}, {"admin", "w1", true},
	} {
		if _, ok, err := e.srv.ledger.Receipt(ctx, run.ID, c.actor, c.cmd); err != nil || ok != c.found {
			t.Fatalf("the ledger's %s command %s: found %v, %v", c.actor, c.cmd, ok, err)
		}
	}
	if resp, out := e.do("GET", base+"/commands/p3", providerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the provider reads its receipt: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.do("GET", base+"/commands/p3", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the workbench reads the provider's receipt: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", base+"/commands/w1", providerToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the provider reads the workbench's receipt: %d", resp.StatusCode)
	}

	// Dispatch: a project and a ticket made by the provider's principal, a
	// ticket by the workbench.
	for _, c := range []struct {
		tok, path string
		body      map[string]any
	}{
		{providerToken, "/api/dispatch/projects", map[string]any{"opId": "d1", "key": "CLD", "name": "Cloud"}},
		{providerToken, "/api/dispatch/tickets", map[string]any{"opId": "d2", "project": "CLD", "title": "by the cloud"}},
		{adminToken, "/api/dispatch/tickets", map[string]any{"opId": "w2", "project": "CLD", "title": "by the workbench"}},
	} {
		if resp, out := e.do("POST", c.path, c.tok, c.body); resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s %v: %d %v", c.path, c.body["opId"], resp.StatusCode, out)
		}
	}
	for key, actor := range map[string]string{"CLD-1": "user:u1", "CLD-2": "admin"} {
		resp, out := e.do("GET", "/api/dispatch/tickets/"+key+"/history", adminToken, nil)
		changes, _ := out["changes"].([]any)
		if resp.StatusCode != http.StatusOK || len(changes) == 0 {
			t.Fatalf("%s's history: %d %v", key, resp.StatusCode, out)
		}
		for _, ch := range changes {
			if got := ch.(map[string]any)["actor"]; got != actor {
				t.Fatalf("%s's change by %v, want %s", key, got, actor)
			}
		}
	}
	for _, c := range []struct {
		actor, op string
		found     bool
	}{
		{"user:u1", "d1", true}, {"user:u1", "d2", true}, {"user:u1", "w2", false},
		{"admin", "d1", false}, {"admin", "w2", true},
	} {
		if _, ok, err := e.srv.dispatch.Receipt(ctx, c.actor, c.op); err != nil || ok != c.found {
			t.Fatalf("Dispatch's %s operation %s: found %v, %v", c.actor, c.op, ok, err)
		}
	}
	if resp, out := e.do("GET", "/api/dispatch/ops/d2", providerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the provider reads its operation: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.do("GET", "/api/dispatch/ops/d2", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the workbench reads the provider's operation: %d", resp.StatusCode)
	}
}

// Subjects that are this repository's own actors are kept under user:,
// apart from the workbench's, the merge's and a member's receipts: the
// same command and operation ids under each actor are each its own. A
// subject of 48 bytes, the most, creates, edits and reads back a Dispatch
// record and a ledger command.
func TestProviderSubjectsNeverCollide(t *testing.T) {
	long := strings.Repeat("s", ext.MaxSubject)
	subjects := map[string]string{"cloud-admin": "admin", "cloud-merge": "merge", "cloud-agent": "agent:x", "cloud-long": long}
	known := map[string]ext.Principal{}
	for tok, sub := range subjects {
		known[tok] = cloudAdmin(sub)
	}
	e := providerEnv(t, nil, &tokenIdentity{known: known})
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/runs/" + run.ID
	ctx := t.Context()
	tokens := []string{adminToken, "cloud-admin", "cloud-merge", "cloud-agent", "cloud-long"}
	keys := map[string]string{adminToken: "WB", "cloud-admin": "PA", "cloud-merge": "PM", "cloud-agent": "PG", "cloud-long": "PL"}
	for _, tok := range tokens {
		resp, out := e.do("POST", base+"/pause", tok, map[string]any{"commandId": "c1"})
		if rc := receiptOf(t, out); resp.StatusCode != http.StatusOK || rc["status"] != "done" || rc["repeat"] != nil {
			t.Fatalf("%s's pause: %d %v", tok, resp.StatusCode, out)
		}
		resp, out = e.do("POST", "/api/dispatch/projects", tok, map[string]any{"opId": "o1", "key": keys[tok], "name": keys[tok]})
		if rc, _ := out["receipt"].(map[string]any); resp.StatusCode != http.StatusCreated || rc["repeat"] != nil || rc["key"] != keys[tok] {
			t.Fatalf("%s's project: %d %v", tok, resp.StatusCode, out)
		}
	}
	for _, c := range []struct{ actor, key string }{
		{"admin", "WB"}, {"user:admin", "PA"}, {"user:merge", "PM"}, {"user:agent:x", "PG"}, {"user:" + long, "PL"},
	} {
		if _, ok, err := e.srv.ledger.Receipt(ctx, run.ID, c.actor, "c1"); err != nil || !ok {
			t.Fatalf("the ledger's %s command: %v %v", c.actor, ok, err)
		}
		rc, ok, err := e.srv.dispatch.Receipt(ctx, c.actor, "o1")
		if err != nil || !ok || rc.Key != c.key {
			t.Fatalf("Dispatch's %s operation: %+v %v %v", c.actor, rc, ok, err)
		}
	}
	for _, actor := range []string{"merge", "agent:x"} {
		if _, ok, _ := e.srv.dispatch.Receipt(ctx, actor, "o1"); ok {
			t.Fatalf("an operation is kept under %s", actor)
		}
		if _, ok, _ := e.srv.ledger.Receipt(ctx, run.ID, actor, "c1"); ok {
			t.Fatalf("a command is kept under %s", actor)
		}
	}
	if got := len("user:" + long); got != 53 {
		t.Fatalf("the longest actor is %d bytes", got)
	}
	// The longest subject edits its project and reads both receipts back.
	if resp, out := e.do("POST", "/api/dispatch/projects/PL/edit", "cloud-long", map[string]any{"opId": "o2", "name": "Longest"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the edit: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("GET", "/api/dispatch/ops/o2", "cloud-long", nil); resp.StatusCode != http.StatusOK || out["receipt"].(map[string]any)["status"] != "done" {
		t.Fatalf("the edit's receipt: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("GET", "/api/dispatch/projects/PL", "cloud-long", nil); resp.StatusCode != http.StatusOK || out["project"].(map[string]any)["name"] != "Longest" {
		t.Fatalf("the project read back: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("GET", base+"/commands/c1", "cloud-long", nil); resp.StatusCode != http.StatusOK || out["command"].(map[string]any)["status"] != "done" {
		t.Fatalf("the command read back: %d %v", resp.StatusCode, out)
	}
}

// A provider's principal with the admin grant proposes a plan revision but
// does not approve one: an approval is the operator's (dispatch.ApprovePlan),
// so the provider's is refused 409 invalid with a failed receipt kept under
// user:<subject>, the revision stays proposed, and the workbench token
// approves it.
func TestProviderPlanApprovalIsTheOperators(t *testing.T) {
	id := &tokenIdentity{known: map[string]ext.Principal{providerToken: cloudAdmin("u1")}}
	e := providerEnv(t, func(cfg *config.Config) { cfg.DataDir = filepath.Join(t.TempDir(), "data") }, id)
	repo := filepath.Join(e.root, "repo")
	planRepo(t, repo)
	e.newDispatchProject(t, "CND", map[string]any{"cwd": repo})
	e.newDispatchTicket(t, "CND", "Fix the login loop", map[string]any{"description": "It loops.", "acceptance": "It does not."})
	p := &planTest{testEnv: e, repo: repo}
	saved := p.saveCrew(t, teamCrew("Team", "ready-cat"))

	resp, out := e.do("POST", "/api/dispatch/tickets/CND-1/plans", providerToken, map[string]any{"opId": "propose-1", "crew": saved.ID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the provider's proposal: %d %v", resp.StatusCode, out)
	}
	rev := entityOf(t, out, "plan")["id"].(string)

	resp, out = e.do("POST", "/api/dispatch/plans/"+rev+"/approve", providerToken, map[string]any{"opId": "approve-1"})
	if resp.StatusCode != http.StatusConflict || errorCode(out) != "invalid" {
		t.Fatalf("the provider's approval: %d %v", resp.StatusCode, out)
	}
	if rc := receiptOf(t, out); rc["status"] != "failed" || rc["refusal"] != "invalid" {
		t.Fatalf("the provider's approval's receipt: %v", rc)
	}
	ctx := t.Context()
	if rc, ok, err := e.srv.dispatch.Receipt(ctx, "user:u1", "approve-1"); err != nil || !ok || rc.Status != "failed" {
		t.Fatalf("the refused approval's receipt under user:u1: %+v %v %v", rc, ok, err)
	}
	if _, ok, _ := e.srv.dispatch.Receipt(ctx, "admin", "approve-1"); ok {
		t.Fatal("the refused approval is kept under admin")
	}
	if v := p.plansOf(t, "CND-1")[rev]; v["state"] != dispatch.PlanProposed {
		t.Fatalf("after the provider's approval: %v", v)
	}

	resp, out = e.do("POST", "/api/dispatch/plans/"+rev+"/approve", adminToken, map[string]any{"opId": "approve-2"})
	if resp.StatusCode != http.StatusOK || entityOf(t, out, "plan")["state"] != dispatch.PlanApproved {
		t.Fatalf("the workbench's approval: %d %v", resp.StatusCode, out)
	}
}
