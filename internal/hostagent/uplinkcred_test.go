package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The uplink's credential (docs/plans/identity-uplink.md): what each
// registration presents through an identity provider, and the host's half
// of the renewal. The provider is a fake whose answers a test sets; the
// host's waits go through Options.after (stepClock) and each renewal step
// is waited for on renewed, never on the clock.

const ownedCode = "code-owned-1"

// uplinkFake is an identity provider that owns the codes of its form and
// gives what token answers, holding UplinkToken at gate while one is set;
// as a switchyard's provider it admits the tokens it is given as a host.
type uplinkFake struct {
	mu    sync.Mutex
	owns  func() bool
	token func() (string, bool)
	gate  chan struct{}
	calls atomic.Int32
}

func (*uplinkFake) Name() string { return "cloud" }
func (f *uplinkFake) Authenticate(_ context.Context, tok string) (ext.Principal, bool, error) {
	return ext.Principal{Subject: "u-ada", Name: "Ada", Grants: []ext.Grant{ext.GrantHost}}, strings.HasPrefix(tok, "cred-"), nil
}
func (f *uplinkFake) OwnsHostToken(c string) bool {
	f.mu.Lock()
	owns := f.owns
	f.mu.Unlock()
	if owns != nil {
		return owns()
	}
	return strings.HasPrefix(c, "code-")
}
func (f *uplinkFake) UplinkToken(string, string) (string, bool) {
	f.calls.Add(1)
	f.mu.Lock()
	token, gate := f.token, f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if token == nil {
		return "", false
	}
	return token()
}
func (f *uplinkFake) answer(tok string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = func() (string, bool) { return tok, ok }
}

// lateAfter is a clock whose IdentityWait fires at once: an ask held at a
// gate is late. Every other wait never fires.
func lateAfter(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	if d == ext.IdentityWait {
		ch <- time.Now()
	}
	return ch
}

// yardRecording is a switchyard (open hosts when open) whose identity
// provider is p, recording every registration's Authorization header.
func yardRecording(t *testing.T, open bool, p ext.IdentityProvider) (string, func() []string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.Switchyard.Enabled = true
	cfg.Switchyard.OpenHosts = open
	// Every case's registration from the test's one address.
	cfg.Switchyard.OpenHostRegistrationsPerMinute, cfg.Switchyard.OpenHostSessions = 600, 100
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), discardLog, nil, nil, api.WithExtensions(local.Fill(ext.Providers{Identity: p, All: []ext.Entry{{Kind: ext.KindIdentity, Name: "cloud", Value: p}}})))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var auths []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/host" {
			mu.Lock()
			auths = append(auths, r.Header.Get("Authorization"))
			mu.Unlock()
		}
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	return hs.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), auths...)
	}
}

// catLocal is a server session running /bin/cat.
func catLocal(t *testing.T) *session.Local {
	t.Helper()
	dir := t.TempDir()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: dir, Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	l := session.NewLocal(session.Info{ID: "local-1", Name: "cat", Kind: session.KindServer, AgentID: "cat", Command: []string{"cat"}, Cwd: dir, Status: session.StatusRunning, Cols: 80, Rows: 24, CreatedAt: time.Now().UTC()},
		proc, session.Options{Transport: proto.TransportWS, FileView: "view", Log: discardLog})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = l.Stop(ctx)
	})
	return l
}

// TestOwnedTokenNeverPresented: a configured token the provider owns, or
// may own, never leaves the machine, whatever the provider answers for the
// switchyard: the session registers open, or is refused where open hosts
// are off; no provider, and one that does not own the token, present it as
// before.
func TestOwnedTokenNeverPresented(t *testing.T) {
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	owns := map[string]func() bool{
		"owned":     func() bool { return true },
		"late":      func() bool { <-never; return false },
		"panicking": func() bool { panic("cannot tell " + ownedCode) },
	}
	gave := map[string]func(f *uplinkFake, c *UplinkCredential){
		"ok false":         func(f *uplinkFake, _ *UplinkCredential) { f.answer("", false) },
		"a refused answer": func(f *uplinkFake, _ *UplinkCredential) { f.answer("not a token", true) },
		"past the bound": func(f *uplinkFake, _ *UplinkCredential) {
			f.answer("cred-"+strings.Repeat("A", ext.MaxHostToken), true)
		},
		"the configured token": func(f *uplinkFake, _ *UplinkCredential) { f.answer(ownedCode, true) },
		"late":                 func(f *uplinkFake, _ *UplinkCredential) { f.gate = never },
		"panicking": func(f *uplinkFake, _ *UplinkCredential) {
			f.token = func() (string, bool) { panic("token " + ownedCode) }
		},
		"not started":                func(_ *uplinkFake, c *UplinkCredential) { c.started = func(context.Context) bool { return false } },
		"a Start that never returns": nil,
	}
	openURL, openAuths := yardRecording(t, true, &uplinkFake{})
	closedURL, closedAuths := yardRecording(t, false, &uplinkFake{})
	l := catLocal(t)
	cell := func(own func() bool, give func(*uplinkFake, *UplinkCredential), url string, late bool) *UplinkCredential {
		f := &uplinkFake{owns: own}
		cred := NewUplinkCredential(f, "cloud", url, ownedCode, nil, discardLog)
		if give != nil {
			give(f, cred)
		} else {
			// Its first try given up at once (StartIdentity's clock): never started.
			s := &hangStarter{uplinkFake: f}
			started, stop := StartIdentity(t.Context(), s, "cloud", HostIdentity{StateDir: t.TempDir(), after: func(d time.Duration) <-chan time.Time {
				if d == ext.StartWait {
					return lateAfter(ext.IdentityWait)
				}
				return make(chan time.Time)
			}})
			t.Cleanup(stop)
			cred.started = started
		}
		if late {
			cred.after = lateAfter
		}
		return cred
	}
	for ownName, own := range owns {
		for gaveName, give := range gave {
			what, late := ownName+", "+gaveName, ownName != "owned" || gaveName == "late"
			if tok, withheld := cell(own, give, openURL, late).Present(t.Context()); tok != "" || !withheld {
				t.Fatalf("%s: Present %q, withheld %v", what, tok, withheld)
			}
			for _, url := range []string{openURL, closedURL} {
				u := &Uplink{ServerURL: url, Token: ownedCode, HostName: "office", Log: discardLog, Credential: cell(own, give, url, late)}
				pub, err := u.Publish(t.Context(), l)
				var refused *RefusedError
				switch {
				case url == openURL && err != nil:
					t.Fatalf("%s: an open host is refused: %v", what, err)
				case url == closedURL && (!errors.As(err, &refused) || !refused.CredentialWithheld() || refused.Status != http.StatusUnauthorized):
					t.Fatalf("%s: where open hosts are off: %v", what, err)
				}
				if pub != nil {
					pub.Stop()
				}
			}
		}
	}
	for _, a := range append(openAuths(), closedAuths()...) {
		if a != "" {
			t.Fatalf("a registration presented %q", a)
		}
	}
	// As before: no provider with the methods, or one that owns nothing.
	if NewUplinkCredential(local.Identity{}, "", openURL, ownedCode, nil, nil) != nil {
		t.Fatal("the free default has a credential")
	}
	f := &uplinkFake{owns: func() bool { return false }}
	if tok, withheld := NewUplinkCredential(f, "cloud", openURL, "a-host-token", nil, nil).Present(t.Context()); tok != "a-host-token" || withheld {
		t.Fatalf("a token the provider does not own: %q, %v", tok, withheld)
	}
	f.answer("cred-1", true)
	if tok, _ := NewUplinkCredential(f, "cloud", openURL, "a-host-token", nil, nil).Present(t.Context()); tok != "cred-1" {
		t.Fatalf("the provider's token for one it does not own: %q", tok)
	}
}

// hangStarter is uplinkFake with a Start that returns only once its
// context ends.
type hangStarter struct{ *uplinkFake }

func (*hangStarter) Start(ctx context.Context, _ ext.Host) error { <-ctx.Done(); return ctx.Err() }

// TestCredentialAfterTheFirstAsk: conductor host's first registration,
// refused for a credential the provider withheld, is tried again (its
// backoff fired through the clock) and presents the provider's token once
// it has one, never the code.
func TestCredentialAfterTheFirstAsk(t *testing.T) {
	f := &uplinkFake{}
	f.mu.Lock()
	n := 0
	f.token = func() (string, bool) {
		n++
		return "cred-after", n > 1
	}
	f.mu.Unlock()
	url, auths := yardRecording(t, false, f)
	registered := make(chan string, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Options{ServerURL: url, Token: ownedCode, Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Log: discardLog,
			Credential: NewUplinkCredential(f, "cloud", url, ownedCode, nil, discardLog),
			after: func(d time.Duration) <-chan time.Time {
				if d == time.Second {
					return lateAfter(ext.IdentityWait)
				}
				return make(chan time.Time)
			},
			Registered: func(id, _ string) { registered <- id }})
		done <- err
	}()
	select {
	case <-registered:
	case err := <-done:
		t.Fatalf("Run: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("never registered")
	}
	if got := auths(); len(got) != 2 || got[0] != "" || got[1] != "Bearer cred-after" {
		t.Fatalf("the registrations presented %q", got)
	}
	cancel()
	<-done
}

// stepClock hands each wait to the test, which fires the one a live
// goroutine is waiting on (fire).
type stepClock struct {
	mu    sync.Mutex
	waits map[time.Duration][]chan time.Time
	armed map[time.Duration]int
}

func (c *stepClock) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time)
	c.mu.Lock()
	if c.waits == nil {
		c.waits, c.armed = map[time.Duration][]chan time.Time{}, map[time.Duration]int{}
	}
	c.waits[d] = append(c.waits[d], ch)
	c.armed[d]++
	c.mu.Unlock()
	return ch
}

// count is how many waits of d were ever asked of the clock.
func (c *stepClock) count(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.armed[d]
}

// fire fires a wait of d that a goroutine is waiting on: one whose
// goroutine has gone stays unfired.
func (c *stepClock) fire(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for i, ch := range c.waits[d] {
			select {
			case ch <- time.Now():
				c.waits[d] = append(c.waits[d][:i:i], c.waits[d][i+1:]...)
				c.mu.Unlock()
				return
			default:
			}
		}
		c.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("nothing waits %s", d)
}

// renewHarness is a cat published to a stub switchyard through a
// credential the provider f answers, its clock and its renewal steps.
type renewHarness struct {
	y     *stubYard
	f     *uplinkFake
	cred  *UplinkCredential
	u     *Uplink
	clock *stepClock
	steps chan renewStep
	// hold, once set, holds each connection writer at gate before it takes
	// a message (controlWriter.taking).
	hold atomic.Bool
	gate chan struct{}
}

func newRenewHarness(t *testing.T, renews bool, first string) *renewHarness {
	t.Helper()
	h := newRenewUplink(t, renews, first, "")
	pub, err := h.u.Publish(t.Context(), catLocal(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pub.Stop)
	h.registered(t, first)
	return h
}

// newRenewUplink is the harness's uplink, not yet publishing, to server
// ("" for the stub switchyard's own address), the credential's guard on
// the test's clock as the uplink's waits are.
func newRenewUplink(t *testing.T, renews bool, first, server string) *renewHarness {
	t.Helper()
	h := &renewHarness{y: newStubYard(t), f: &uplinkFake{}, clock: &stepClock{}, steps: make(chan renewStep, 64), gate: make(chan struct{})}
	h.y.renews.Store(renews)
	h.f.answer(first, first != "")
	if server == "" {
		server = h.y.srv.URL
	}
	h.cred = NewUplinkCredential(h.f, "cloud", server, ownedCode, nil, discardLog)
	h.cred.after = h.clock.after
	h.u = &Uplink{ServerURL: server, Token: ownedCode, HostName: "office", Log: discardLog, Credential: h.cred,
		after: h.clock.after, renewed: func(s renewStep) { h.steps <- s }, taking: func() {
			if h.hold.Load() {
				<-h.gate
			}
		}}
	return h
}

// registered waits for a registration presenting tok ("" for none).
func (h *renewHarness) registered(t *testing.T, tok string) {
	t.Helper()
	want := ""
	if tok != "" {
		want = "Bearer " + tok
	}
	select {
	case got := <-h.y.auths:
		if got != want {
			t.Fatalf("a registration presented %q, want %q", got, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no registration")
	}
	<-h.y.reg
	<-h.y.conn
}

// tick fires the renewal's step and waits for what it decided.
func (h *renewHarness) tick(t *testing.T, want renewStep) {
	t.Helper()
	h.clock.fire(t, uplinkTokenEvery)
	h.step(t, want)
}

func (h *renewHarness) step(t *testing.T, want renewStep) {
	t.Helper()
	select {
	case got := <-h.steps:
		if got != want {
			t.Fatalf("the renewal's step %s, want %s", got, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no renewal step (%s wanted)", want)
	}
}

func (h *renewHarness) sent() int { return len(h.y.received(proto.HostTokenType)) }

// TestHostTokenRenewalSameSubjectOnly is the host's half of host_token: a
// changed credential is one frame, only where registered said renews; a
// connection that presented nothing reconnects with it instead; a send
// withdrawn closes the connection, and nothing of a step outlives it.
func TestHostTokenRenewalSameSubjectOnly(t *testing.T) {
	t.Run("sent once, unchanged after, and presented at the reconnect", func(t *testing.T) {
		h := newRenewHarness(t, true, "cred-1")
		// A token at its bound is sent whole, within the message's bound.
		long := "cred-" + strings.Repeat("A", ext.MaxHostToken-len("cred-"))
		h.f.answer(long, true)
		h.tick(t, renewSent)
		h.tick(t, renewUnchanged)
		h.y.wait("the host_token", func(m yardMsg) bool { return m.t == proto.HostTokenType })
		got := h.y.received(proto.HostTokenType)
		if b, _ := json.Marshal(got[0].raw); len(got) != 1 || got[0].raw["token"] != long || len(b) > proto.MaxHostTokenMessage {
			t.Fatalf("host_token frames %d, the message %d bytes", len(got), len(b))
		}
		// The switchyard's verdict on another subject: closed, and the host
		// registers again with what its provider gives.
		h.y.mu.Lock()
		c := h.y.c
		h.y.mu.Unlock()
		c.Close(proto.CloseForbidden, "access revoked")
		h.registered(t, long)
	})
	t.Run("an open host's connection closes once a credential comes", func(t *testing.T) {
		h := newRenewHarness(t, false, "")
		h.f.answer("cred-2", true)
		h.tick(t, renewClosed)
		h.registered(t, "cred-2")
		if h.sent() != 0 {
			t.Fatal("an open host's connection was sent host_token")
		}
	})
	t.Run("none where registered did not say renews", func(t *testing.T) {
		h := newRenewHarness(t, false, "cred-1")
		h.f.answer("cred-2", true)
		h.tick(t, renewNone)
		if h.sent() != 0 {
			t.Fatal("host_token sent where registered did not say renews")
		}
	})
	t.Run("none for a desk where registered did not say renews, one where it did", func(t *testing.T) {
		for _, renews := range []bool{false, true} {
			h := newRenewUplink(t, renews, "cred-1", "")
			d, err := h.u.OpenDesk(t.Context(), proto.DeskRun{ID: "run-1", Name: "run"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = d.Stop(ctx)
			})
			h.registered(t, "cred-1")
			h.f.answer("cred-2", true)
			want, n := renewNone, 0
			if renews {
				want, n = renewSent, 1
			}
			h.tick(t, want)
			if renews {
				h.y.wait("the desk's host_token", func(m yardMsg) bool { return m.t == proto.HostTokenType })
			}
			if got := h.sent(); got != n {
				t.Fatalf("renews %v: %d host_token frames from a desk, want %d", renews, got, n)
			}
		}
	})
	// On a connection that renews: what the provider answers out of bounds,
	// or the configured token it owns answered back, is never sent.
	t.Run("never sent: out of bounds, or the owned token answered back", func(t *testing.T) {
		never := make(chan struct{})
		t.Cleanup(func() { close(never) })
		h := newRenewHarness(t, true, "cred-1")
		for _, bad := range []string{"cred-" + strings.Repeat("A", ext.MaxHostToken), "cred 2", ownedCode} {
			h.f.answer(bad, true)
			h.tick(t, renewNone)
		}
		// The owned token answered back by an owner that panics, or is late.
		h.f.answer(ownedCode, true)
		h.f.mu.Lock()
		h.f.owns = func() bool { panic("cannot tell " + ownedCode) }
		h.f.mu.Unlock()
		h.tick(t, renewNone)
		h.f.mu.Lock()
		h.f.owns = func() bool { <-never; return false }
		h.f.mu.Unlock()
		h.clock.fire(t, uplinkTokenEvery)
		h.clock.fire(t, ext.IdentityWait) // the owner's ask, late
		h.step(t, renewNone)
		if h.sent() != 0 {
			t.Fatal("host_token sent out of bounds or with the owned token")
		}
	})
	t.Run("a send withdrawn closes the connection", func(t *testing.T) {
		h := newRenewHarness(t, true, "cred-1")
		// What the connection sends as it starts is out before the writer is held.
		h.y.wait("the attention a connection starts with", func(m yardMsg) bool { return m.t == proto.HostAttention })
		h.hold.Store(true)
		h.f.answer("cred-2", true)
		h.clock.fire(t, uplinkTokenEvery)
		h.clock.fire(t, controlTurnWait)
		h.step(t, renewClosed)
		h.hold.Store(false)
		close(h.gate)
		h.registered(t, "cred-2")
		if h.sent() != 0 {
			t.Fatal("a withdrawn host_token was read")
		}
	})
	t.Run("a step held across a disconnect ends with its connection", func(t *testing.T) {
		h := newRenewHarness(t, true, "cred-1")
		gate := make(chan struct{})
		h.f.mu.Lock()
		h.f.gate = gate
		h.f.mu.Unlock()
		h.f.answer("cred-2", true)
		h.clock.fire(t, uplinkTokenEvery)
		waitFor(t, "the step's ask", func() bool { return h.f.calls.Load() >= 2 })
		h.y.mu.Lock()
		c := h.y.c
		h.y.mu.Unlock()
		c.Close(proto.CloseForbidden, "access revoked")
		h.step(t, renewEnded)
		f := h.f
		f.mu.Lock()
		f.gate = nil
		f.mu.Unlock()
		close(gate)
		// The reconnect, after its loop's wait, presents what the provider
		// answers then (a fresh ask, or the held one should it still be in
		// flight, never late on the test's clock: cred-2 either way).
		h.registered(t, "cred-2")
		if h.sent() != 0 {
			t.Fatal("a step of the connection that ended sent host_token")
		}
		// The new connection's own step sends the token once.
		h.f.answer("cred-3", true)
		h.tick(t, renewSent)
		h.y.wait("the new connection's host_token", func(m yardMsg) bool { return m.t == proto.HostTokenType })
		if got := h.y.received(proto.HostTokenType); len(got) != 1 || got[0].raw["token"] != "cred-3" {
			t.Fatalf("host_token frames %v", got)
		}
	})
}

// seenServer is a test server that records every request's path and
// Authorization before serve (nil: none) answers it.
type seenServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string
}

func newSeenServer(t *testing.T, tls bool, serve func(s *seenServer, w http.ResponseWriter, r *http.Request)) *seenServer {
	t.Helper()
	s := &seenServer{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
		s.mu.Unlock()
		serve(s, w, r)
	})
	if tls {
		s.srv = httptest.NewTLSServer(h)
	} else {
		s.srv = httptest.NewServer(h)
	}
	t.Cleanup(s.srv.Close)
	return s
}

func (s *seenServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// TestRegistrationFollowsNoRedirect: a registration through the identity
// provider's credential is made to the configured address alone. Answered
// with a redirect (to another host, the same host's other port, another
// scheme, a subdomain, or a chain back to the configured server) it fails
// in the host's own words: the server the redirect names sees no request,
// nothing is sent anywhere after, and nothing counts as presented.
func TestRegistrationFollowsNoRedirect(t *testing.T) {
	port := func(u string) string {
		_, p, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://"), ":")
		return p
	}
	for _, c := range []struct {
		name string
		tls  bool
		// to is where the configured server redirects, given the yard's
		// address (127.0.0.1:<port>), its own and another server's, which
		// redirects back to the configured one (the chain).
		to func(yard, self, other string) string
		// names maps a name the dial is given to the address it dials.
		names func(yard, self string) map[string]string
		url   func(self string) string
	}{
		{name: "another host", to: func(yard, _, _ string) string { return "http://localhost:" + port(yard) + "/ws/host" }},
		{name: "the same host's other port", to: func(yard, _, _ string) string { return "http://" + yard + "/ws/host" }},
		{name: "another scheme", tls: true, to: func(yard, _, _ string) string { return "http://" + yard + "/ws/host" }},
		{name: "a subdomain", to: func(yard, _, _ string) string { return "http://sub.yard.test:" + port(yard) + "/ws/host" },
			names: func(yard, self string) map[string]string {
				return map[string]string{"yard.test:" + port(self): self, "sub.yard.test:" + port(yard): yard}
			},
			url: func(self string) string { return "http://yard.test:" + port(self) }},
		{name: "a chain back to the configured server", to: func(_, _, other string) string { return "http://localhost:" + port(other) + "/ws/host" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			y := newStubYard(t)
			y.renews.Store(true)
			yard := newSeenServer(t, false, func(_ *seenServer, w http.ResponseWriter, r *http.Request) { y.srv.Config.Handler.ServeHTTP(w, r) })
			yardAddr := strings.TrimPrefix(yard.srv.URL, "http://")
			var otherAddr atomic.Pointer[string]
			configured := newSeenServer(t, c.tls, func(s *seenServer, w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("back") != "" {
					y.srv.Config.Handler.ServeHTTP(w, r) // the chain's end: the yard itself
					return
				}
				http.Redirect(w, r, c.to(yardAddr, s.srv.Listener.Addr().String(), *otherAddr.Load()), http.StatusTemporaryRedirect)
			})
			self := configured.srv.Listener.Addr().String()
			server := configured.srv.URL
			other := newSeenServer(t, false, func(_ *seenServer, w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, configured.srv.URL+"/ws/host?back=1", http.StatusTemporaryRedirect)
			})
			otherHost := other.srv.Listener.Addr().String()
			otherAddr.Store(&otherHost)
			var base *http.Transport
			if c.tls {
				base = configured.srv.Client().Transport.(*http.Transport)
			}
			var transport *http.Transport
			if c.names != nil {
				server = c.url(self)
				transport = (&namedDialer{to: c.names(yardAddr, self)}).transport(base)
			} else if base != nil {
				transport = base.Clone()
			}
			f := &uplinkFake{}
			f.answer("cred-1", true)
			cred := NewUplinkCredential(f, "cloud", server, ownedCode, nil, discardLog)
			wsURL, err := controlURL(server)
			if err != nil {
				t.Fatal(err)
			}
			a := &agent{opts: Options{ServerURL: server, Token: ownedCode, HostName: "office", Insecure: true, Switchyard: true, ReconnectMax: time.Second,
				Log: discardLog, Credential: cred, transport: transport}, wsURL: wsURL, peers: map[string]*peer{}, log: discardLog, instance: "inst-1", localID: "1"}
			conn, _, err := a.dialAndRegister(t.Context())
			if conn != nil {
				conn.CloseNow()
			}
			if err == nil {
				t.Fatal("a redirected registration through the credential registered")
			}
			if strings.Contains(err.Error(), port(yardAddr)) || strings.Contains(err.Error(), "localhost") || strings.Contains(err.Error(), "sub.yard") {
				t.Fatalf("the failure names where the redirect led: %v", err)
			}
			if got := append(yard.requests(), other.requests()...); len(got) != 0 {
				t.Fatalf("a server the redirect names saw %q", got)
			}
			if got := configured.requests(); len(got) != 1 || !strings.HasSuffix(got[0], "Bearer cred-1") {
				t.Fatalf("the configured server saw %q, one registration presenting the credential wanted", got)
			}
			if a.out != nil && a.out.presented != "" {
				t.Fatalf("%q counts as presented", a.out.presented)
			}
			if n := len(y.received(proto.HostTokenType)); n != 0 {
				t.Fatalf("%d host_token frames", n)
			}
		})
	}
	// With no provider, a redirect is followed as before.
	y := newStubYard(t)
	configured := newSeenServer(t, false, func(_ *seenServer, w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, y.srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	runUntilRegistered(t, Options{ServerURL: configured.srv.URL, Token: hostToken, Argv: []string{"/bin/cat"}, RelayOnly: true, Log: discardLog})
}

// TestLateOwnerAnswerCountsAsOwned: an owner check whose answer and
// deadline are both there when the guard looks is late, so a "not owned"
// given then counts as owned and the configured token is not presented.
// The guard is held until the answer is there (judging); the deadline
// has passed already (lateAfter).
func TestLateOwnerAnswerCountsAsOwned(t *testing.T) {
	for i := range 64 {
		f := &uplinkFake{owns: func() bool { return false }}
		cred := NewUplinkCredential(f, "cloud", "https://yard.invalid", ownedCode, nil, discardLog)
		cred.after = lateAfter
		cred.judging = func(answered func() bool) {
			for !answered() { // the fake answers at once
				time.Sleep(time.Millisecond)
			}
		}
		if tok, withheld := cred.Present(t.Context()); tok != "" || !withheld {
			t.Fatalf("try %d: a late answer that the token is not owned presented %q, withheld %v", i, tok, withheld)
		}
	}
}

// TestCredentialLogsNoWords: the guard's failures are logged by kind and
// why, never with what a provider said, a panic's value carrying the owned
// token among it.
func TestCredentialLogsNoWords(t *testing.T) {
	logs := &syncLog{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	for _, c := range []struct {
		owns  func() bool
		token func() (string, bool)
		late  bool
	}{
		{owns: func() bool { panic("cannot tell " + ownedCode) }},
		{owns: func() bool { return true }, token: func() (string, bool) { panic("token " + ownedCode) }},
		{owns: func() bool { return true }, token: func() (string, bool) { return ownedCode, true }},
		{owns: func() bool { <-never; return false }, late: true},
	} {
		f := &uplinkFake{owns: c.owns, token: c.token}
		cred := NewUplinkCredential(f, "cloud", "https://yard.invalid", ownedCode, nil, log)
		if c.late {
			cred.after = lateAfter
		}
		if tok, withheld := cred.Present(t.Context()); tok != "" || !withheld {
			t.Fatalf("Present %q, %v", tok, withheld)
		}
	}
	out := logs.String()
	for _, want := range []string{"failure=panic", "failure=late", `failure="the configured token answered back"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the log has no %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, ownedCode) {
		t.Fatalf("the log quotes the provider:\n%s", out)
	}
}

// TestFirstRegistrationBound: conductor host's retries of a withheld first
// registration end at firstCredentialWait, a try under way included: one
// whose ask the provider holds is cut short when the bound fires, and Run
// answers the withheld refusal.
func TestFirstRegistrationBound(t *testing.T) {
	f := &uplinkFake{}
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	f.mu.Lock()
	n := 0
	f.token = func() (string, bool) {
		if n++; n > 1 {
			<-gate
		}
		return "", false
	}
	f.mu.Unlock()
	url, _ := yardRecording(t, false, f)
	clock, credClock := &stepClock{}, &stepClock{}
	cred := NewUplinkCredential(f, "cloud", url, ownedCode, nil, discardLog)
	cred.after = credClock.after
	done := make(chan error, 1)
	go func() {
		_, err := Run(t.Context(), Options{ServerURL: url, Token: ownedCode, Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Log: discardLog,
			Credential: cred, after: clock.after})
		done <- err
	}()
	clock.fire(t, time.Second) // the first retry's backoff
	waitFor(t, "the retry's ask, held", func() bool { return f.calls.Load() >= 2 })
	clock.fire(t, firstCredentialWait)
	select {
	case err := <-done:
		var refused *RefusedError
		if !errors.As(err, &refused) || !refused.CredentialWithheld() || refused.Status != http.StatusUnauthorized {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the try under way outlived the bound")
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("the provider was asked %d times", n)
	}
}

// stopStarter is uplinkFake with a Start that, once its context ends,
// returns only when gate opens (never, unless it does).
type stopStarter struct {
	*uplinkFake
	began, ended chan struct{}
	gate         chan struct{}
	returned     atomic.Bool
}

func (s *stopStarter) Start(ctx context.Context, _ ext.Host) error {
	close(s.began)
	<-ctx.Done()
	close(s.ended)
	<-s.gate
	s.returned.Store(true)
	return ctx.Err()
}

// TestStopWaitsForTheTry: StartIdentity's stop waits for a Start under way
// to return, at most ext.StartWait (fired through its clock).
func TestStopWaitsForTheTry(t *testing.T) {
	for _, opens := range []bool{true, false} {
		s := &stopStarter{uplinkFake: &uplinkFake{}, began: make(chan struct{}), ended: make(chan struct{}), gate: make(chan struct{})}
		clock := &stepClock{}
		_, stop := StartIdentity(t.Context(), s, "cloud", HostIdentity{StateDir: t.TempDir(), after: clock.after})
		<-s.began
		stopped := make(chan bool, 1)
		go func() {
			stop()
			stopped <- s.returned.Load()
		}()
		<-s.ended
		waitFor(t, "stop waiting for the try under way", func() bool { return clock.count(ext.StartWait) >= 2 })
		if opens {
			close(s.gate)
			if !<-stopped {
				t.Fatal("stop returned before the try under way")
			}
			continue
		}
		clock.fire(t, ext.StartWait) // the stop's bound
		if <-stopped {
			t.Fatal("a Start that never returns returned")
		}
		close(s.gate)
	}
}

// quickStarter is uplinkFake with a Start that succeeds at once, keeping
// the Host it was handed.
type quickStarter struct {
	*uplinkFake
	host chan ext.Host
}

func (s *quickStarter) Start(_ context.Context, h ext.Host) error {
	s.host <- h
	return nil
}

// TestLateStartCountsAsLate: a Start whose success and whose ext.StartWait
// are both there when the try looks is late: not started, its Host
// refusing (no token). The try is held until the answer is there
// (judging); the wait has fired already.
func TestLateStartCountsAsLate(t *testing.T) {
	for i := range 64 {
		s := &quickStarter{uplinkFake: &uplinkFake{}, host: make(chan ext.Host, 1)}
		after := func(d time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			if d == ext.StartWait {
				ch <- time.Now()
			}
			return ch
		}
		started, stop := StartIdentity(t.Context(), s, "cloud", HostIdentity{Token: ownedCode, StateDir: t.TempDir(), after: after,
			judging: func(answered func() bool) {
				for !answered() {
					time.Sleep(time.Millisecond)
				}
			}})
		h := <-s.host
		if started(t.Context()) || h.HostToken() != "" {
			t.Fatalf("try %d: a Start answered past its wait started (its Host's token %q)", i, h.HostToken())
		}
		stop()
	}
}

// TestHostedSessionRefusedTheStateDir: a hosted session whose folder holds
// the host's state directory is refused a read and a save of what its
// provider keeps there.
func TestHostedSessionRefusedTheStateDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CONDUCTOR_DATA_DIR", "")
	t.Setenv("CONDUCTOR_CATALOG_PATH", "")
	deny, err := config.LocalFileDenyFunc("")
	if err != nil {
		t.Fatal(err)
	}
	opts := editOpts()
	opts.FileDenyFunc = deny
	a, proc, begun := newHostOf(t, opts, 0, 0)
	r := &signalRouter{to: map[string]chan any{}}
	a.sendHook = r.send
	heardFromServer(t, a)
	cwd := a.local.Info().Cwd
	t.Setenv("XDG_STATE_HOME", filepath.Join(cwd, "state"))
	state, err := config.HostStateDir()
	if err != nil {
		t.Fatal(err)
	}
	lease := filepath.Join(state, "ext", "cloud", "lease.json")
	if err := os.MkdirAll(filepath.Dir(lease), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lease, []byte(`{"lease":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fileIn(t, a, "notes.txt", "ordinary\n")
	owner := submitViewerOn(t, a, proc, begun, "fedcba9876543210", "", r.add("fedcba9876543210"))
	rel := "state/conductor/ext/cloud/lease.json"
	owner.send(proto.FileGet{T: proto.CtlFileGet, ReqID: "r1", Path: rel})
	if h := owner.fileAnswer("r1"); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" {
		t.Fatalf("the read: %+v %+v", h, h.Error)
	}
	owner.saveOver("w1", rel, "changed")
	if h := owner.fileAnswer("w1"); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" {
		t.Fatalf("the save: %+v %+v", h, h.Error)
	}
	hasBody(t, lease, `{"lease":"x"}`)
	owner.send(proto.FileGet{T: proto.CtlFileGet, ReqID: "r2", Path: "notes.txt"})
	if h := owner.fileAnswer("r2"); h.Kind != "file" {
		t.Fatalf("an ordinary read: %+v %+v", h, h.Error)
	}
}
