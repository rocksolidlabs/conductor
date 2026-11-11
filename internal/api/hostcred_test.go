package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A host connection admitted through an identity provider's principal
// (docs/plans/identity-uplink.md): rechecked every 30 s through the
// server's clock seam (fakeClock) and renewed in place by host_token. The
// provider's asks are held by gates in the fake and each step is waited
// for on hostCredSeam's hooks or the fake's asks, never on the clock.

const (
	credOld  = "cloud-host-old"
	credNew  = "cloud-host-new"
	credNew2 = "cloud-host-new-2"
)

var credHost = ext.Principal{Subject: "u-ada", Name: "Ada", Grants: []ext.Grant{ext.GrantHost}}

// credIdentity admits the tokens the test gives a principal, answers a
// token's ask from answer when set, holds a token's ask at its gate while
// one is set, and tells asks each token it is asked about.
type credIdentity struct {
	mu     sync.Mutex
	admits map[string]ext.Principal
	answer map[string]func(context.Context) (ext.Principal, bool, error)
	gates  map[string]chan struct{}
	asked  []string
	asks   chan string
}

func newCredIdentity() *credIdentity {
	return &credIdentity{admits: map[string]ext.Principal{credOld: credHost, credNew: credHost, credNew2: credHost},
		answer: map[string]func(context.Context) (ext.Principal, bool, error){}, gates: map[string]chan struct{}{}, asks: make(chan string, 1024)}
}

func (*credIdentity) Name() string { return "cloud" }

func (f *credIdentity) Authenticate(ctx context.Context, tok string) (ext.Principal, bool, error) {
	f.mu.Lock()
	f.asked = append(f.asked, tok)
	p, ok := f.admits[tok]
	ans, gate := f.answer[tok], f.gates[tok]
	f.mu.Unlock()
	f.asks <- tok
	if gate != nil {
		<-gate
	}
	if ans != nil {
		return ans(ctx)
	}
	return p, ok, nil
}

func (f *credIdentity) set(fn func(f *credIdentity)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *credIdentity) timesAsked(tok string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.asked {
		if a == tok {
			n++
		}
	}
	return n
}

// awaitAsk waits for the fake to be asked about tok.
func (f *credIdentity) awaitAsk(t *testing.T, tok string) {
	t.Helper()
	deadline := time.After(testWait)
	for {
		select {
		case a := <-f.asks:
			if a == tok {
				return
			}
		case <-deadline:
			t.Fatalf("the provider was never asked about %s", tok)
		}
	}
}

// credEnv is a switchyard admitting no open hosts whose identity provider
// is f, its clock the test's.
func credEnv(t *testing.T, f *credIdentity) (*testEnv, *fakeClock) {
	t.Helper()
	e := providerEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true }, f)
	return e, newFakeClock(e.srv)
}

// hostReg is the register of the session localID of one instance, resuming
// resume when set.
func hostReg(localID string, resume *proto.HostResume) proto.Register {
	return proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop", Instance: "inst-1"},
		Session: proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: "/tmp", Cols: 80, Rows: 24, LocalID: localID}, Resume: resume}
}

// dialCred registers first (a register or a register_desk) presenting tok
// ("" for none); a refusal answers nil and {"status": code}.
func dialCred(t *testing.T, e *testEnv, first any, tok string) (*fakeHost, map[string]any) {
	t.Helper()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	if tok != "" {
		header.Set("Authorization", "Bearer "+tok)
	}
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if resp != nil {
			return nil, map[string]any{"status": resp.StatusCode}
		}
		t.Fatalf("dial host: %v", err)
	}
	c.SetReadLimit(proto.MaxHostMessage + proto.MaxFrame)
	t.Cleanup(func() { c.CloseNow() })
	h := &fakeHost{t: t, c: c, msgs: make(chan map[string]any, 4096), bin: make(chan []byte, 4096), done: make(chan struct{})}
	go h.pump()
	h.send(first)
	reg := h.expect(proto.HostRegistered)
	h.sessionID, _ = reg["sessionId"].(string)
	return h, reg
}

// closedRevoked waits for h's connection to end, closed 4403 access revoked.
func closedRevoked(t *testing.T, h *fakeHost) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(testWait):
		t.Fatal("the host connection was not closed")
	}
	expectAccessRevoked(t, h.err)
}

// hostAway waits for the hosted session id to show its host gone, which the
// handler sets once its read loop has ended.
func hostAway(t *testing.T, e *testEnv, id string) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for {
		if d, ok := e.srv.registry.Get(id); ok && d.Info().Status == session.StatusHostDisconnected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s never showed its host gone", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// resumedAs resumes reg's session by its secret presenting tok, once its
// host is away: the same id, and link still opens it. The connection is
// closed after.
func resumedAs(t *testing.T, e *testEnv, localID string, reg map[string]any, tok, link string) {
	t.Helper()
	id, _ := reg["sessionId"].(string)
	secret, _ := reg["secret"].(string)
	hostAway(t, e, id)
	again, back := dialCred(t, e, hostReg(localID, &proto.HostResume{SessionID: id, Secret: secret}), tok)
	if again == nil || back["sessionId"] != id || back["resumed"] != true {
		t.Fatalf("the resume with a token the provider admits: %v", back)
	}
	if resp, _ := e.do("GET", "/api/join/"+link, link, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the link no longer opens the session: %d", resp.StatusCode)
	}
	again.c.CloseNow()
}

// recheckWith waits for the next recheck's wait, sets answer for tok (nil:
// what it admits) and fires it, then waits for the provider's ask of tok.
func recheckWith(t *testing.T, clock *fakeClock, f *credIdentity, tok string, answer func(context.Context) (ext.Principal, bool, error)) {
	t.Helper()
	tm := clock.next(t, identityRecheckEvery)
	f.set(func(f *credIdentity) { f.answer[tok] = answer })
	tm.fire <- time.Now()
	f.awaitAsk(t, tok)
}

func hostToken(tok string) proto.HostTokenMsg {
	return proto.HostTokenMsg{T: proto.HostTokenType, Token: tok}
}

// TestHostConnectionRechecked: a provider-admitted host connection, a
// session's and a desk's, says renews and is closed 4403 once the provider
// no longer admits it; a failing answer keeps it; the server's own tokens
// say no renews and are never asked about; after the close the session is
// as after any drop.
func TestHostConnectionRechecked(t *testing.T) {
	t.Run("a session", func(t *testing.T) {
		f := newCredIdentity()
		e, clock := credEnv(t, f)
		for i, tok := range []string{adminToken, "test-host-token"} {
			h, reg := dialCred(t, e, hostReg("own-"+string(rune('a'+i)), nil), tok)
			if reg["renews"] != nil {
				t.Fatalf("a connection of the server's own token says renews: %v", reg)
			}
			h.c.CloseNow()
		}
		h, reg := dialCred(t, e, hostReg("1", nil), credOld)
		if reg["renews"] != true {
			t.Fatalf("a provider-admitted connection does not say renews: %v", reg)
		}
		link := h.link("view", "")
		for _, name := range []string{"error", "panic", "late"} {
			recheckWith(t, clock, f, credOld, failures[name])
		}
		// Each failure kept it: the next recheck waits, and a link is minted.
		h.link("view", "")
		recheckWith(t, clock, f, credOld, func(context.Context) (ext.Principal, bool, error) { return ext.Principal{}, false, nil })
		closedRevoked(t, h)
		resumedAs(t, e, "1", reg, credNew, link)
		if _, refused := dialCred(t, e, hostReg("1", &proto.HostResume{SessionID: h.sessionID, Secret: reg["secret"].(string)}), ""); refused["status"] != http.StatusUnauthorized {
			t.Fatalf("a resume presenting nothing at a switchyard with no open hosts: %v", refused)
		}
		if f.timesAsked(adminToken)+f.timesAsked("test-host-token")+f.timesAsked(link) != 0 {
			t.Fatal("the provider was asked about a token of the server's own")
		}
	})
	t.Run("a desk", func(t *testing.T) {
		f := newCredIdentity()
		e, clock := credEnv(t, f)
		h, reg := dialCred(t, e, deskReg("inst-1", "run-1"), credOld)
		if reg["renews"] != true {
			t.Fatalf("a provider-admitted desk does not say renews: %v", reg)
		}
		recheckWith(t, clock, f, credOld, func(context.Context) (ext.Principal, bool, error) {
			return ext.Principal{Subject: "u-bob", Name: "Bob", Grants: []ext.Grant{ext.GrantHost}}, true, nil
		})
		closedRevoked(t, h)
	})
	// A verdict crossing the host's messages: nothing the reader reads after
	// it is handled, a link request and a host_token included.
	t.Run("a verdict before the host's messages", func(t *testing.T) {
		f := newCredIdentity()
		e, clock := credEnv(t, f)
		h, _ := dialCred(t, e, hostReg("1", nil), credOld)
		reading, closing, gate := make(chan struct{}, 8), make(chan struct{}, 1), make(chan struct{})
		e.srv.hostCred = &hostCredSeam{reading: func() {
			select {
			case reading <- struct{}{}:
			default:
			}
		}, closing: func() { closing <- struct{}{}; <-gate }}
		recheckWith(t, clock, f, credOld, func(context.Context) (ext.Principal, bool, error) { return ext.Principal{}, false, nil })
		<-closing
		h.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "late-1", Role: "view"})
		h.send(hostToken(credNew))
		<-reading
		hostAway(t, e, h.sessionID)
		close(gate)
		closedRevoked(t, h)
		if n := len(e.srv.links.ListBySession(h.sessionID)); n != 0 {
			t.Fatalf("%d links minted after the verdict", n)
		}
		for _, m := range h.drain(0) {
			if m["t"] == proto.HostLinkCreated {
				t.Fatalf("link_created after the verdict: %v", m)
			}
		}
		if f.timesAsked(credNew) != 0 {
			t.Fatal("the provider was asked about a host_token read after the verdict")
		}
	})
}

// TestHostTokenRenewal is the switchyard's half of host_token: the one
// success swaps the token, and every other outcome closes the connection
// 4403 with the session kept for its host's next registration.
func TestHostTokenRenewal(t *testing.T) {
	t.Run("the same subject, a session's and a desk's", func(t *testing.T) {
		f := newCredIdentity()
		e, clock := credEnv(t, f)
		h, _ := dialCred(t, e, hostReg("1", nil), credOld)
		h.send(hostToken(credNew))
		f.awaitAsk(t, credNew)
		h.link("view", "") // read after the renewal: it was taken
		recheckWith(t, clock, f, credNew, nil)
		h.c.CloseNow()
		hostAway(t, e, h.sessionID)
		clock.next(t, identityRecheckEvery) // the session's next, which its end leaves unfired
		d, _ := dialCred(t, e, deskReg("inst-1", "run-1"), credOld)
		d.send(hostToken(credNew))
		f.awaitAsk(t, credNew)
		recheckWith(t, clock, f, credNew, nil)
	})
	t.Run("two together, taken in order", func(t *testing.T) {
		f := newCredIdentity()
		e, _ := credEnv(t, f)
		h, _ := dialCred(t, e, hostReg("1", nil), credOld)
		read, release := make(chan struct{}), make(chan struct{})
		e.srv.hostCred = &hostCredSeam{reading: func() {
			select {
			case read <- struct{}{}:
				<-release
			default:
			}
		}}
		h.send(hostToken(credNew))
		<-read
		h.send(hostToken(credNew2))
		e.srv.hostCred.reading = nil
		close(release)
		f.awaitAsk(t, credNew)
		f.awaitAsk(t, credNew2)
		h.link("view", "")
	})
	type fallback struct {
		name, presented string
		msg             []byte
		answer          func(context.Context) (ext.Principal, bool, error)
	}
	tokenMsg := func(tok string) []byte { return []byte(`{"t":"host_token","token":"` + tok + `"}`) }
	falls := []fallback{
		{name: "past the bound", presented: credOld, msg: tokenMsg(strings.Repeat("a", proto.MaxHostToken+1))},
		{name: "not token68", presented: credOld, msg: tokenMsg("a b")},
		{name: "a connection of a host token", presented: "test-host-token", msg: tokenMsg(credNew)},
		{name: "another subject", presented: credOld, msg: tokenMsg(credNew), answer: func(context.Context) (ext.Principal, bool, error) {
			return ext.Principal{Subject: "u-bob", Name: "Bob", Grants: []ext.Grant{ext.GrantHost}}, true, nil
		}},
		{name: "no host grant", presented: credOld, msg: tokenMsg(credNew), answer: func(context.Context) (ext.Principal, bool, error) {
			return ext.Principal{Subject: "u-ada", Name: "Ada"}, true, nil
		}},
	}
	for _, name := range []string{"error", "panic", "late"} {
		falls = append(falls, fallback{name: name, presented: credOld, msg: tokenMsg(credNew), answer: failures[name]})
	}
	for _, fb := range falls {
		t.Run("the fallback: "+fb.name, func(t *testing.T) {
			f := newCredIdentity()
			e, _ := credEnv(t, f)
			f.set(func(f *credIdentity) { f.answer[credNew] = fb.answer })
			h, reg := dialCred(t, e, hostReg("1", nil), fb.presented)
			link := h.link("view", "")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			err := h.c.Write(ctx, websocket.MessageText, fb.msg)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			closedRevoked(t, h)
			f.set(func(f *credIdentity) { delete(f.answer, credNew) })
			resumedAs(t, e, "1", reg, credNew2, link)
		})
	}
	t.Run("the fallback: a token of the server's own chain, never asked", func(t *testing.T) {
		f := newCredIdentity()
		e, _ := credEnv(t, f)
		own, _ := dialCred(t, e, hostReg("own", nil), credOld)
		live, revoked := own.link("view", ""), own.link("view", "")
		expired := expiredLink(t, e, own.sessionID)
		if r, err := e.srv.links.Resolve(revoked); err == nil {
			e.srv.links.Revoke(own.sessionID, r.ID)
		}
		for i, tok := range []string{adminToken, "test-host-token", live, expired, revoked} {
			h, _ := dialCred(t, e, hostReg("chain-"+string(rune('a'+i)), nil), credOld)
			h.send(hostToken(tok))
			closedRevoked(t, h)
			if f.timesAsked(tok) != 0 {
				t.Fatalf("token %d of the chain was asked of the provider", i)
			}
		}
	})
	// The two timers against one deadline.
	t.Run("a recheck, the renewal, then a recheck after the old is revoked", func(t *testing.T) {
		f := newCredIdentity()
		e, clock := credEnv(t, f)
		h, _ := dialCred(t, e, hostReg("1", nil), credOld)
		recheckWith(t, clock, f, credOld, nil)
		h.send(hostToken(credNew))
		f.awaitAsk(t, credNew)
		h.link("view", "")
		f.set(func(f *credIdentity) { delete(f.admits, credOld) })
		recheckWith(t, clock, f, credNew, nil)
		h.link("view", "")
	})
	t.Run("a recheck waits for the renewal's ask", func(t *testing.T) {
		wait := time.Hour
		extWaiting.Store(&wait)
		t.Cleanup(func() { extWaiting.Store(nil) })
		f := newCredIdentity()
		e, clock := credEnv(t, f)
		waiting := make(chan struct{}, 1)
		h, _ := dialCred(t, e, hostReg("1", nil), credOld)
		e.srv.hostCred = &hostCredSeam{waiting: func() { waiting <- struct{}{} }}
		gate := make(chan struct{})
		f.set(func(f *credIdentity) { f.gates[credNew] = gate; delete(f.admits, credOld) })
		h.send(hostToken(credNew))
		f.awaitAsk(t, credNew)
		tm := clock.next(t, identityRecheckEvery)
		tm.fire <- time.Now()
		<-waiting
		f.set(func(f *credIdentity) { delete(f.gates, credNew) })
		close(gate)
		f.awaitAsk(t, credNew) // the recheck's, of the token current then
		h.link("view", "")
		if n := f.timesAsked(credOld); n != 1 {
			t.Fatalf("the old token was asked %d times, its registration's alone wanted", n)
		}
	})
	t.Run("a renewal not delivered in time", func(t *testing.T) {
		f := newCredIdentity()
		e, clock := credEnv(t, f)
		h, reg := dialCred(t, e, hostReg("1", nil), credOld)
		link := h.link("view", "")
		hold, held, closing := make(chan struct{}), make(chan struct{}, 4), make(chan struct{}, 1)
		e.srv.hostCred = &hostCredSeam{reading: func() { held <- struct{}{}; <-hold }, closing: func() { closing <- struct{}{} }}
		h.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: h.sessionID, Status: string(session.StatusRunning)})
		h.send(hostToken(credNew))
		<-held
		f.set(func(f *credIdentity) { delete(f.admits, credOld) })
		recheckWith(t, clock, f, credOld, nil)
		<-closing
		close(hold)
		closedRevoked(t, h)
		if f.timesAsked(credNew) != 0 {
			t.Fatal("the host_token queued behind the verdict was handled")
		}
		e.srv.hostCred = nil
		resumedAs(t, e, "1", reg, credNew, link)
	})
}

// detailIdentity is credIdentity saying who the server is signed in as.
type detailIdentity struct {
	*credIdentity
	detail func() string
	asked  chan struct{}
}

func (d *detailIdentity) Detail() string {
	d.asked <- struct{}{}
	return d.detail()
}

// startingIdentity is detailIdentity with a Start that fails: never ready.
type startingIdentity struct{ *detailIdentity }

func (startingIdentity) Start(context.Context, ext.Host) error { return errors.New("not yet") }

// TestCapabilitiesDetail: the identity provider's row carries its detail,
// within its bounds, once it has started; the guide asks nothing and shows
// none.
func TestCapabilitiesDetail(t *testing.T) {
	d := &detailIdentity{credIdentity: newCredIdentity(), asked: make(chan struct{}, 64)}
	e := providerEnv(t, nil, d)
	for _, c := range []struct {
		name, detail, want string
		late, panics       bool
	}{
		{name: "a detail", detail: "signed in as Ana · workspace Acme", want: "signed in as Ana · workspace Acme"},
		{name: "80 runes", detail: strings.Repeat("é", ext.MaxIdentityDetail), want: strings.Repeat("é", ext.MaxIdentityDetail)},
		{name: "81 runes", detail: strings.Repeat("é", ext.MaxIdentityDetail+1)},
		{name: "two lines", detail: "signed in\nas Ana"},
		{name: "panicking", panics: true},
		{name: "late", late: true},
	} {
		d.detail = func() string {
			if c.panics {
				panic("signed in as " + credOld)
			}
			if c.late {
				time.Sleep(ext.IdentityWait + 100*time.Millisecond)
			}
			return c.detail
		}
		_, out := e.do("GET", "/api/capabilities", adminToken, nil)
		rows, _ := out["providers"].([]any)
		row, _ := rows[0].(map[string]any)
		if got, _ := row["detail"].(string); got != c.want || (c.want == "" && row["detail"] != nil) {
			t.Errorf("%s: detail %q, want %q", c.name, got, c.want)
		}
	}
	// "late" was the last: its call may still run, so detail is not set again.
	for range len(d.asked) {
		<-d.asked
	}
	caps := e.srv.capabilities(t.Context(), false)
	if len(d.asked) != 0 || caps.Providers[0].Detail != "" {
		t.Fatal("the guide's rows asked the provider or hold its detail")
	}
	starting := providerEnv(t, nil, startingIdentity{&detailIdentity{credIdentity: newCredIdentity(), detail: func() string { return "signed in" }, asked: make(chan struct{}, 4)}})
	if _, out := starting.do("GET", "/api/capabilities", adminToken, nil); strings.Contains(mustJSON(out), "detail") {
		t.Fatalf("a starting provider's row has a detail: %v", out)
	}
	if _, out := newTestEnv(t, nil).do("GET", "/api/capabilities", adminToken, nil); mustJSON(out) != `{"providers":[]}` {
		t.Fatalf("a plain build's reply: %v", out)
	}
}

// modeStarter records the Host each try of its Start is given; its Start
// never returns when hang says so.
type modeStarter struct {
	name string
	hang bool
	host chan ext.Host
	err  func(tok string) error
}

func (m *modeStarter) Name() string { return m.name }
func (m *modeStarter) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}
func (m *modeStarter) Route(context.Context, ext.Decision) (ext.Route, error) {
	return ext.Route{}, nil
}
func (m *modeStarter) Resolved(context.Context, ext.Resolution) {}
func (m *modeStarter) Start(ctx context.Context, h ext.Host) error {
	m.host <- h
	if m.hang {
		<-ctx.Done()
	}
	if m.err != nil {
		return m.err(h.HostToken())
	}
	return nil
}

// TestProviderToldItsMode: a provider's Host says which binary runs it, and
// only the identity slot's provider is handed the host token, while the
// server publishes; ProviderStarted answers at once for a provider without
// Start and false for one whose first try was given up.
func TestProviderToldItsMode(t *testing.T) {
	for _, yard := range []bool{false, true} {
		id := &modeStarter{name: "cloud", host: make(chan ext.Host, 4)}
		router := &modeStarter{name: "routes", host: make(chan ext.Host, 4)}
		e := newTestEnvAgents(t, func(c *config.Config) {
			c.Switchyard.Enabled = yard
			c.Rendezvous.Server, c.Rendezvous.Token = "https://yard.invalid", "code-1"
		}, slog.New(slog.DiscardHandler), nil, WithExtensions(local.Fill(ext.Providers{Identity: id, Router: router,
			All: []ext.Entry{{Kind: ext.KindIdentity, Name: "cloud", Value: id}, {Kind: ext.KindDecisions, Name: "routes", Value: router}}})))
		e.srv.StartExtensions(t.Context())
		ih, rh := <-id.host, <-router.host
		wantMode, wantToken := ext.ModeServer, "code-1"
		if yard {
			wantMode, wantToken = ext.ModeSwitchyard, ""
		}
		if ih.Mode() != wantMode || rh.Mode() != wantMode || ih.HostToken() != wantToken || rh.HostToken() != "" {
			t.Errorf("switchyard %v: identity's Host %s %q, the router's %s %q", yard, ih.Mode(), ih.HostToken(), rh.Mode(), rh.HostToken())
		}
		if !e.srv.ProviderStarted(t.Context(), "cloud") || !e.srv.ProviderStarted(t.Context(), "nobody") {
			t.Errorf("switchyard %v: a provider that started, or none, is not started", yard)
		}
	}
	hung := &modeStarter{name: "cloud", hang: true, host: make(chan ext.Host, 4)}
	e := providerEnv(t, nil, hung)
	clock := newFakeClock(e.srv)
	e.srv.StartExtensions(t.Context())
	<-hung.host
	clock.next(t, ext.StartWait).fire <- time.Now()
	if e.srv.ProviderStarted(t.Context(), "cloud") {
		t.Fatal("a provider whose first try was given up started")
	}
}

// TestIdentityStartNotQuoted: a failed Start of the identity provider,
// which was handed the host token, is logged without its words.
func TestIdentityStartNotQuoted(t *testing.T) {
	for _, panics := range []bool{false, true} {
		id := &modeStarter{name: "cloud", host: make(chan ext.Host, 4), err: func(tok string) error {
			if panics {
				panic("cannot redeem " + tok)
			}
			return errors.New("cannot redeem " + tok)
		}}
		logs := &syncLog{}
		e := newTestEnvAgents(t, func(c *config.Config) {
			c.Rendezvous.Server, c.Rendezvous.Token = "https://yard.invalid", "code-secret-1"
		},
			slog.New(slog.NewTextHandler(logs, nil)), nil, withIdentity(id))
		e.srv.StartExtensions(t.Context())
		<-id.host
		e.srv.ProviderStarted(t.Context(), "cloud")
		e.srv.stopExtensions()
		if strings.Contains(logs.String(), "code-secret-1") || !strings.Contains(logs.String(), "kind=start") {
			t.Fatalf("panics %v: the log:\n%s", panics, logs.String())
		}
	}
}

// ownerUplink is an identity provider that owns the code it was configured
// with and gives the switchyard a credential once it has one.
type ownerUplink struct {
	*credIdentity
	mu  sync.Mutex
	tok string
}

func (*ownerUplink) OwnsHostToken(c string) bool { return strings.HasPrefix(c, "code-") }
func (o *ownerUplink) UplinkToken(string, string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.tok, o.tok != ""
}

// TestPublishOwedWithheld: a publication refused for a credential the
// provider withheld is owed, the Share dialog says republish, and a Share
// once the provider has a credential publishes the same running session.
func TestPublishOwedWithheld(t *testing.T) {
	yard, _ := credEnv(t, newCredIdentity())
	owner := &ownerUplink{credIdentity: newCredIdentity()}
	local := newTestEnv(t, nil)
	cred := hostagent.NewUplinkCredential(owner, "cloud", yard.http.URL, "code-123", nil, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: yard.http.URL, Token: "code-123", HostName: "office", RelayOnly: true, Credential: cred}})
	id := local.launch("sh", []string{"-c", "exec cat"})["id"].(string)
	deadline := time.Now().Add(testWait)
	for !local.srv.owedPublication(id) {
		if time.Now().After(deadline) {
			t.Fatal("the withheld publication is not owed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, out := local.do("GET", "/api/sessions/"+id+"/links", adminToken, nil); out["republish"] != true {
		t.Fatalf("the Share dialog's reply: %v", out)
	}
	owner.mu.Lock()
	owner.tok = credNew
	owner.mu.Unlock()
	if resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"}); resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("the Share once the provider has a credential: %d %v", resp.StatusCode, out)
	}
	if local.srv.publishedOf(id) == nil {
		t.Fatal("the session is not published")
	}
}
