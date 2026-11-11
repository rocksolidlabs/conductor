package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
		"ok false":             func(f *uplinkFake, _ *UplinkCredential) { f.answer("", false) },
		"a refused answer":     func(f *uplinkFake, _ *UplinkCredential) { f.answer("not a token", true) },
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
}

func (c *stepClock) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time)
	c.mu.Lock()
	if c.waits == nil {
		c.waits = map[time.Duration][]chan time.Time{}
	}
	c.waits[d] = append(c.waits[d], ch)
	c.mu.Unlock()
	return ch
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
	clock *stepClock
	steps chan renewStep
	// hold, once set, holds each connection writer at gate before it takes
	// a message (controlWriter.taking).
	hold atomic.Bool
	gate chan struct{}
}

func newRenewHarness(t *testing.T, renews bool, first string) *renewHarness {
	t.Helper()
	h := &renewHarness{y: newStubYard(t), f: &uplinkFake{}, clock: &stepClock{}, steps: make(chan renewStep, 64), gate: make(chan struct{})}
	h.y.renews.Store(renews)
	h.f.answer(first, first != "")
	u := &Uplink{ServerURL: h.y.srv.URL, Token: ownedCode, HostName: "office", Log: discardLog,
		Credential: NewUplinkCredential(h.f, "cloud", h.y.srv.URL, ownedCode, nil, discardLog),
		after:      h.clock.after, renewed: func(s renewStep) { h.steps <- s }, taking: func() {
			if h.hold.Load() {
				<-h.gate
			}
		}}
	pub, err := u.Publish(t.Context(), catLocal(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pub.Stop)
	h.registered(t, first)
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
		// Out of bounds: nothing to send, ever.
		for _, bad := range []string{"cred-" + strings.Repeat("A", ext.MaxHostToken), "cred 2"} {
			h.f.answer(bad, true)
			h.tick(t, renewNone)
		}
		if h.sent() != 0 {
			t.Fatal("host_token sent where registered did not say renews")
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
		close(gate)
		// The reconnect's ask shared the one held: it presents its answer.
		h.registered(t, "cred-2")
		h.tick(t, renewUnchanged)
		if h.sent() != 0 {
			t.Fatal("a step of the connection that ended sent host_token")
		}
	})
}
