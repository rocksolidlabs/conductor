package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The watch of a provider's principal's long-lived connections (#560):
// the identity provider is asked again every identityRecheckEvery, the
// waits handed to the test through the server's clock seam (fakeClock),
// so no test waits on the clock.

// watchToken is the token the turning provider admits.
const watchToken = "cloud-session-token"

// turningIdentity is an identity provider that admits watchToken as an
// admin until the test turns its answer (turn), and notes every token it
// is asked about.
type turningIdentity struct {
	answer atomic.Pointer[func(ctx context.Context) (ext.Principal, bool, error)]

	mu    sync.Mutex
	asked []string
}

func newTurningIdentity() *turningIdentity {
	f := &turningIdentity{}
	f.turn(func(context.Context) (ext.Principal, bool, error) { return cloudAdmin("u-ada"), true, nil })
	return f
}

func (*turningIdentity) Name() string { return "cloud" }

func (f *turningIdentity) Authenticate(ctx context.Context, tok string) (ext.Principal, bool, error) {
	f.mu.Lock()
	f.asked = append(f.asked, tok)
	f.mu.Unlock()
	if tok != watchToken {
		return ext.Principal{}, false, nil
	}
	return (*f.answer.Load())(ctx)
}

// turn makes answer the provider's from its next ask.
func (f *turningIdentity) turn(answer func(ctx context.Context) (ext.Principal, bool, error)) {
	f.answer.Store(&answer)
}

// askedTokens is every token the provider was asked about, in order.
func (f *turningIdentity) askedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// timesAsked is how many times the provider was asked about tok.
func (f *turningIdentity) timesAsked(tok string) int {
	n := 0
	for _, t := range f.askedTokens() {
		if t == tok {
			n++
		}
	}
	return n
}

// watchStream is a GET /api/events stream: its events as "<event>
// <data>", and done closed once the server has ended it.
type watchStream struct {
	events chan string
	done   chan struct{}
}

// openStream opens GET /api/events with tok, which the server must let in.
func openStream(t *testing.T, e *testEnv, tok string) *watchStream {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "GET", e.http.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("events stream with %q: %d", tok, resp.StatusCode)
	}
	t.Cleanup(func() { resp.Body.Close() })
	s := &watchStream{events: make(chan string, 4096), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case len(line) > 7 && line[:7] == "event: ":
				name = line[7:]
			case len(line) > 6 && line[:6] == "data: ":
				select {
				case s.events <- name + " " + line[6:]:
				default:
				}
			}
		}
	}()
	return s
}

// ended reports whether the server has ended the stream.
func (s *watchStream) ended() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// waitEnded fails t unless the server ends the stream within testWait.
func (s *watchStream) waitEnded(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(testWait):
		t.Fatal("the events stream was not ended")
	}
}

// expectSession fails t unless a session event naming id arrives: the
// stream still carries what happens.
func (s *watchStream) expectSession(t *testing.T, id string) {
	t.Helper()
	deadline := time.After(testWait)
	for {
		select {
		case ev := <-s.events:
			var m map[string]any
			if name, data, ok := cutEvent(ev); ok && name == "session" && json.Unmarshal([]byte(data), &m) == nil && m["id"] == id {
				return
			}
		case <-s.done:
			t.Fatal("the events stream ended")
		case <-deadline:
			t.Fatalf("no session event for %s", id)
		}
	}
}

// cutEvent splits a watchStream event into its name and data.
func cutEvent(ev string) (name, data string, ok bool) {
	for i := range len(ev) {
		if ev[i] == ' ' {
			return ev[:i], ev[i+1:], true
		}
	}
	return "", "", false
}

// expectRevoked reads until the connection closes and fails t unless an
// error{revoked} came first and the close is 4403 with the words of a
// principal the provider no longer admits, which name no link.
func (w *wsClient) expectRevoked() {
	w.t.Helper()
	sawError := false
	for {
		f, err := w.read()
		if err != nil {
			expectAccessRevoked(w.t, err)
			if !sawError {
				w.t.Fatal("closed with no error{revoked} before it")
			}
			return
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		if json.Unmarshal(f.Payload, &m) == nil && m["t"] == "error" {
			if m["code"] != proto.ErrCodeRevoked {
				w.t.Fatalf("error %v, want %s", m, proto.ErrCodeRevoked)
			}
			sawError = true
		}
	}
}

// expectAccessRevoked fails t unless err is a close 4403 "access revoked".
func expectAccessRevoked(t *testing.T, err error) {
	t.Helper()
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != proto.CloseForbidden || ce.Reason != "access revoked" {
		t.Fatalf("closed with %v, want %d access revoked", err, proto.CloseForbidden)
	}
}

// recheckFired waits for the watch's wait, turns the provider's answer to
// answer and fires the wait: the provider is asked again.
func recheckFired(t *testing.T, clock *fakeClock, f *turningIdentity, answer func(ctx context.Context) (ext.Principal, bool, error)) {
	t.Helper()
	tm := clock.next(t, identityRecheckEvery)
	f.turn(answer)
	tm.fire <- time.Now()
}

// What a provider may answer when it is asked again: the revocations end
// the connection, the failures leave it.
var (
	revocations = map[string]func(ctx context.Context) (ext.Principal, bool, error){
		"not admitted": func(context.Context) (ext.Principal, bool, error) { return ext.Principal{}, false, nil },
		"host grant only": func(context.Context) (ext.Principal, bool, error) {
			return ext.Principal{Subject: "u-ada", Name: "Ada", Grants: []ext.Grant{ext.GrantHost}}, true, nil
		},
		"no grant": func(context.Context) (ext.Principal, bool, error) {
			return ext.Principal{Subject: "u-ada", Name: "Ada"}, true, nil
		},
		"another subject": func(context.Context) (ext.Principal, bool, error) { return cloudAdmin("u-bob"), true, nil },
	}
	failures = map[string]func(ctx context.Context) (ext.Principal, bool, error){
		"error": func(context.Context) (ext.Principal, bool, error) {
			return ext.Principal{}, false, errors.New("the service is down")
		},
		"panic": func(context.Context) (ext.Principal, bool, error) { panic("the provider broke") },
		"late": func(ctx context.Context) (ext.Principal, bool, error) {
			// Past the guard's IdentityWait: its context ends first.
			<-ctx.Done()
			return ext.Principal{}, false, nil
		},
		"out of bounds": func(context.Context) (ext.Principal, bool, error) {
			return ext.Principal{Subject: "has a space", Name: "Ada", Grants: []ext.Grant{ext.GrantAdmin}}, true, nil
		},
	}
)

// A principal the provider admitted and then revokes loses its events
// stream at the next ask; one the provider still admits keeps it.
func TestRevokedPrincipalLosesEventStream(t *testing.T) {
	for name, revoke := range revocations {
		t.Run(name, func(t *testing.T) {
			f := newTurningIdentity()
			e := providerEnv(t, nil, f)
			clock := newFakeClock(e.srv)
			s := openStream(t, e, watchToken)
			// Still admitted: the stream stays and carries what happens.
			recheckFired(t, clock, f, func(context.Context) (ext.Principal, bool, error) { return cloudAdmin("u-ada"), true, nil })
			// The next wait is asked for once the ask is answered.
			next := clock.next(t, identityRecheckEvery)
			s.expectSession(t, e.createSession("cat"))
			f.turn(revoke)
			next.fire <- time.Now()
			s.waitEnded(t)
			clock.none(t)
			if n := f.timesAsked(watchToken); n != 3 {
				t.Fatalf("the provider was asked %d times about the token, want 3 (the open and two asks)", n)
			}
		})
	}
}

// A principal the provider revokes loses its viewer's connection, on a
// server session and a hosted one, closed as a revoked link's is: an
// error{revoked} and 4403.
func TestRevokedPrincipalLosesViewer(t *testing.T) {
	for name, revoke := range revocations {
		t.Run(name+", server session", func(t *testing.T) {
			f := newTurningIdentity()
			e := providerEnv(t, nil, f)
			clock := newFakeClock(e.srv)
			id := e.createSession("cat")
			v := dialViewer(t, e, id, watchToken)
			v.hello(80, 24)
			if w := v.expectControl(proto.CtlWelcome); w["role"] != "control" {
				t.Fatalf("welcome %v", w)
			}
			v.expectControl(proto.CtlReady)
			recheckFired(t, clock, f, revoke)
			v.expectRevoked()
			clock.none(t)
		})
		t.Run(name+", hosted session", func(t *testing.T) {
			f := newTurningIdentity()
			e := providerEnv(t, nil, f)
			clock := newFakeClock(e.srv)
			// The viewer's close is held: its connection is not cut
			// closeWait on, and the viewer is not let go (RemoveViewer,
			// which sends a leave) until the host has been told.
			e.srv.viewerCloseWait = 2 * time.Minute
			told := make(chan struct{})
			release := sync.OnceFunc(func() { close(told) })
			defer release()
			e.srv.viewerSeam = &viewerSeam{left: func() { <-told }}
			host := dialFakeHost(t, e, "")
			v := dialViewer(t, e, host.sessionID, watchToken)
			if w := v.expectControl(proto.CtlWelcome); w["role"] != "control" {
				t.Fatalf("welcome %v", w)
			}
			// The host knows the viewer (a viewer whose join never reached
			// it is never named to it, and needs no leave).
			joined := host.expect(proto.HostViewerJoin)
			if joined["principal"] != true {
				t.Fatalf("viewer_join %v does not say the viewer is a principal", joined)
			}
			recheckFired(t, clock, f, revoke)
			// The viewer reads nothing yet, so its close handshake waits on
			// it: the host is told the viewer was revoked all the same, so
			// that it withdraws what the viewer has under way rather than
			// finish it (once: TestARevokedPrincipalsLeaveGoesAtOnce).
			if m := host.expect(proto.HostViewerLeave); m["viewerId"] != joined["viewerId"] || m["reason"] != proto.LeaveRevoked {
				t.Fatalf("viewer_leave %v, want viewer %v's with reason %s", m, joined["viewerId"], proto.LeaveRevoked)
			}
			release()
			v.expectRevoked()
		})
	}
}

// A hosted viewer's join says it came as an identity provider's principal
// (principal), so that the host withdraws what it has under way when the
// connection to the server is lost, a revoked leave perhaps not carried;
// the workbench token's viewer and a link's say nothing of it.
func TestOnlyAPrincipalsHostedJoinSaysPrincipal(t *testing.T) {
	f := newTurningIdentity()
	e := providerEnv(t, nil, f)
	host := dialFakeHost(t, e, "")
	for _, c := range []struct {
		tok       string
		principal bool
	}{{adminToken, false}, {watchToken, true}} {
		v := dialViewer(t, e, host.sessionID, c.tok)
		v.expectControl(proto.CtlWelcome)
		joined := host.expect(proto.HostViewerJoin)
		if got, ok := joined["principal"]; (got == true) != c.principal || (!c.principal && ok) {
			t.Fatalf("token %q: viewer_join %v", c.tok, joined)
		}
	}
}

// A principal the provider revokes while a submission of its waits for
// the session's turn: the submission is withdrawn, never typed, as a
// revoked link's is, though its connection's slot waits for it.
func TestRevokedPrincipalsWaitingSubmissionIsWithdrawn(t *testing.T) {
	f := newTurningIdentity()
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{drawer}, withIdentity(f))
	clock := newFakeClock(e.srv)
	id := e.createSession("drawer")
	local := e.local(id)
	owner := dialViewer(t, e, id, adminToken)
	owner.hello(0, 0)
	owner.expectControl(proto.CtlReady)
	out := watchOutput(t, owner)

	// A link's line is typed and its link revoked before its Enter: the
	// session's turn stays held until a person types into the terminal.
	resp, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("link: %d %v", resp.StatusCode, lo)
	}
	linkID := lo["link"].(map[string]any)["id"].(string)
	guest := dialViewer(t, e, id, lo["token"].(string))
	guest.hello(0, 0)
	guest.expectControl(proto.CtlReady)
	guest.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "first"}))
	out.waitFor(t, "first", 5*time.Second)
	if resp, _ := e.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	waitAttention(t, local, session.AttentionNeedsInput, session.HeldWords)

	// The provider's principal submits a line, which waits for the held
	// turn: a second of the connection's meanwhile is refused.
	cloud := dialViewer(t, e, id, watchToken)
	cloud.hello(0, 0)
	cloud.expectControl(proto.CtlReady)
	codes := make(chan string, 16)
	closed := make(chan error, 1)
	go func() {
		for {
			f, err := cloud.read()
			if err != nil {
				closed <- err
				return
			}
			var m map[string]any
			if f.Type == proto.TypeControl && json.Unmarshal(f.Payload, &m) == nil && m["t"] == proto.CtlError {
				code, _ := m["code"].(string)
				codes <- code
			}
		}
	}()
	cloud.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "second"}))
	cloud.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "third"}))
	select {
	case code := <-codes:
		if code != proto.ErrCodeTooManyRequests {
			t.Fatalf("error %s, want the third line's %s", code, proto.ErrCodeTooManyRequests)
		}
	case <-time.After(testWait):
		t.Fatal("the third line was not refused: the second does not wait")
	}

	// The provider revokes the principal: its viewer is closed as revoked.
	recheckFired(t, clock, f, revocations["not admitted"])
	select {
	case err := <-closed:
		expectAccessRevoked(t, err)
	case <-time.After(testWait):
		t.Fatal("the revoked principal's viewer was not closed")
	}
	clock.none(t)

	// The owner types into the terminal: the hold ends, and the turn goes
	// to the next that waits for it. The owner's own line is entered; the
	// revoked principal's, which waited before it, was never typed.
	owner.send(proto.Encode(proto.TypeInput, []byte{0x15}))
	waitAttention(t, local, session.AttentionNone, "")
	owner.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "fourth"}))
	out.waitFor(t, "got:fourth", testWait)
	if out.has("second") {
		t.Fatal("the revoked principal's waiting line was typed")
	}
}

// blockedWriter is the response of an events stream whose client reads
// nothing: every write waits until a write deadline is set, and then
// fails as a passed deadline does.
type blockedWriter struct {
	header http.Header
	cut    chan struct{}
	once   sync.Once
}

func (w *blockedWriter) Header() http.Header { return w.header }
func (w *blockedWriter) WriteHeader(int)     {}
func (w *blockedWriter) FlushError() error   { return nil }

func (w *blockedWriter) Write([]byte) (int, error) {
	<-w.cut
	return 0, os.ErrDeadlineExceeded
}

func (w *blockedWriter) SetWriteDeadline(at time.Time) error {
	if !at.IsZero() {
		w.once.Do(func() { close(w.cut) })
	}
	return nil
}

// A principal the provider revokes loses its events stream though the
// stream's write waits on a client that reads nothing, from its first,
// the snapshot: the revoke cuts the write.
func TestRevokeCutsABlockedEventStream(t *testing.T) {
	f := newTurningIdentity()
	e := providerEnv(t, nil, f)
	clock := newFakeClock(e.srv)
	w := &blockedWriter{header: http.Header{}, cut: make(chan struct{})}
	t.Cleanup(func() { w.once.Do(func() { close(w.cut) }) })
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/events", nil)
	r.Header.Set("Authorization", "Bearer "+watchToken)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.handleEvents(w, r)
	}()
	// The watch waits while the snapshot's write does.
	recheckFired(t, clock, f, revocations["not admitted"])
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("the revoked principal's blocked events stream was not ended")
	}
	clock.none(t)
}

// A provider that fails, panics, is late or answers out of bounds when
// asked again has said nothing: the connections it admitted stay, and it
// is asked again at the next wait.
func TestFailingProviderKeepsConnections(t *testing.T) {
	for name, fail := range failures {
		t.Run(name, func(t *testing.T) {
			f := newTurningIdentity()
			e := providerEnv(t, nil, f)
			clock := newFakeClock(e.srv)
			id := e.createSession("cat")
			s := openStream(t, e, watchToken)
			v := dialViewer(t, e, id, watchToken)
			v.hello(80, 24)
			v.expectControl(proto.CtlWelcome)
			v.expectControl(proto.CtlReady)
			// Two watches wait: the stream's and the viewer's, in either order.
			first, second := clock.next(t, identityRecheckEvery), clock.next(t, identityRecheckEvery)
			f.turn(fail)
			first.fire <- time.Now()
			second.fire <- time.Now()
			// Each watch asks for its next wait once its ask is answered:
			// the failure ended nothing.
			clock.next(t, identityRecheckEvery)
			clock.next(t, identityRecheckEvery)
			if n := f.timesAsked(watchToken); n != 4 {
				t.Fatalf("the provider was asked %d times about the token, want 4 (two opens, two asks)", n)
			}
			if s.ended() {
				t.Fatal("the events stream was ended by a failing provider")
			}
			v.send(proto.Encode(proto.TypeInput, []byte("still here\n")))
			v.expectOutput("still here")
			s.expectSession(t, e.createSession("cat"))
		})
	}
}

// The workbench token's connections, and a link's, are not watched: with
// a provider that revokes everything, they stay, the provider is never
// asked about them, and no watch waits for them. The free default watches
// nothing either.
func TestChainConnectionsUnwatched(t *testing.T) {
	f := newTurningIdentity()
	e := providerEnv(t, nil, f)
	clock := newFakeClock(e.srv)
	id := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	linkTok := lo["token"].(string)
	admin := openStream(t, e, adminToken)
	ctl := dialViewer(t, e, id, adminToken)
	ctl.hello(80, 24)
	ctl.expectControl(proto.CtlReady)
	viaLink := dialViewer(t, e, id, linkTok)
	viaLink.hello(80, 24)
	viaLink.expectControl(proto.CtlReady)
	// The provider's own stream is watched, and revoked.
	cloud := openStream(t, e, watchToken)
	recheckFired(t, clock, f, revocations["not admitted"])
	cloud.waitEnded(t)
	clock.none(t)
	if admin.ended() {
		t.Fatal("the workbench token's stream was ended")
	}
	admin.expectSession(t, e.createSession("cat"))
	ctl.send(proto.Encode(proto.TypeInput, []byte("admin\n")))
	ctl.expectOutput("admin")
	viaLink.send(proto.Encode(proto.TypeInput, []byte("link\n")))
	viaLink.expectOutput("link")
	for _, tok := range f.askedTokens() {
		if tok != watchToken {
			t.Fatalf("the provider was asked about %q", tok)
		}
	}

	// No watch for a principal without a subject, whatever its server has
	// in the identity slot.
	free := newTestEnvAgents(t, nil, e.srv.log, nil, WithExtensions(local.Fill(ext.Providers{})))
	for _, srv := range []*Server{e.srv, free.srv} {
		for _, p := range []principal{
			srv.tokenPrincipal(tokenRequest(adminToken, false), adminToken),
			srv.tokenPrincipal(tokenRequest("test-host-token", false), "test-host-token"),
			srv.tokenPrincipal(tokenRequest(linkTok, false), linkTok),
		} {
			if srv.principalWatch(p, "a-token") != nil {
				t.Fatalf("a watch for %+v", p)
			}
		}
	}
	if free.srv.principalWatch(free.srv.tokenPrincipal(tokenRequest(watchToken, false), watchToken), watchToken) != nil {
		t.Fatal("the free default's server watches an unknown token")
	}
}
