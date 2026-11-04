package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/rocksolidlabs/conductor/internal/version"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

func switchyardEnv(t *testing.T, relay bool) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *config.Config) {
		c.Switchyard.Enabled = true
		c.Switchyard.Relay = &relay
	})
}

// A switchyard launches nothing: every route that starts, edits or lists
// what the server launches answers 403 switchyard, the health and whoami
// routes say what it is, and a hosted session's link resolves as anywhere.
func TestSwitchyardLaunchesNothing(t *testing.T) {
	e := switchyardEnv(t, true)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/sessions"}, {"GET", "/api/catalog"}, {"POST", "/api/catalog/check"}, {"GET", "/api/crews"}, {"POST", "/api/crews"},
		{"GET", "/api/runs"}, {"POST", "/api/runs/x-1a2b3c4d/stop"}, {"GET", "/api/integrations"}, {"GET", "/api/paths"}, {"GET", "/api/git/check"},
		{"POST", "/api/sessions/abc/resume"}, {"POST", "/api/sessions/abc/clone"}, {"POST", "/api/sessions/abc/crew"}, {"GET", "/api/sessions/abc/run"},
	} {
		resp, out := e.do(tc.method, tc.path, adminToken, map[string]any{})
		if resp.StatusCode != http.StatusForbidden || errorCode(out) != "switchyard" {
			t.Errorf("%s %s: %d %v", tc.method, tc.path, resp.StatusCode, out)
		}
	}
	resp, out := e.do("GET", "/api/health", "", nil)
	if resp.StatusCode != http.StatusOK || out["switchyard"] != true || out["relay"] != true {
		t.Fatalf("health: %d %v", resp.StatusCode, out)
	}
	resp, out = e.do("GET", "/api/whoami", adminToken, nil)
	if resp.StatusCode != http.StatusOK || out["switchyard"] != true || out["host"] == nil || out["version"] != version.String() {
		t.Fatalf("whoami: %d %v", resp.StatusCode, out)
	}
	// Sessions are listed (the hosted ones), and a plain server says it is no switchyard.
	if resp, _ := e.do("GET", "/api/sessions", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("sessions: %d", resp.StatusCode)
	}
	plain := newTestEnv(t, nil)
	if _, out := plain.do("GET", "/api/health", "", nil); out["switchyard"] != false || out["relay"] != true {
		t.Fatalf("a plain server's health: %v", out)
	}
}

// A host registers on the switchyard and has a link minted over its
// connection, a viewer joins by it and asks for the relay: relay_ok, and the
// host is told.
func TestSwitchyardCoordinatesAHostAndAViewer(t *testing.T) {
	e := switchyardEnv(t, true)
	host := dialFakeHost(t, e, "hosted-agent-token")
	token := host.link("view", "a friend")
	if resp, out := e.do("GET", "/api/join/"+token, "", nil); resp.StatusCode != http.StatusOK || out["session"] == nil {
		t.Fatalf("join: %d %v", resp.StatusCode, out)
	}
	v := dialViewer(t, e, host.sessionID, token)
	v.hello(80, 24)
	welcome := v.expectControl(proto.CtlWelcome)
	if welcome["transport"] != "webrtc" {
		t.Fatalf("welcome %v", welcome)
	}
	host.expect(proto.HostViewerJoin)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.Simple{T: proto.SigRelay})
	v.send(relay)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := v.read()
		if err != nil {
			t.Fatalf("waiting for relay_ok: %v", err)
		}
		if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeSignal && tt == proto.SigRelayOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no relay_ok")
		}
	}
	host.expect(proto.HostRelayStart)
}

// Without a relay the switchyard tells a viewer who asks (relay_off) and
// keeps the connection for its WebRTC attempt; a host that would use the
// relay alone is refused at registration.
func TestSwitchyardRelayOff(t *testing.T) {
	e := switchyardEnv(t, false)
	if _, out := e.do("GET", "/api/health", "", nil); out["relay"] != false {
		t.Fatalf("health: %v", out)
	}
	host := dialFakeHost(t, e, "hosted-agent-token")
	v := dialViewer(t, e, host.sessionID, host.link("control", ""))
	v.hello(80, 24)
	v.expectControl(proto.CtlWelcome)
	host.expect(proto.HostViewerJoin)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.Simple{T: proto.SigRelay})
	v.send(relay)
	for {
		f, err := v.read()
		if err != nil {
			t.Fatalf("waiting for the refusal: %v", err)
		}
		if f.Type == proto.TypeControl && bytes.Contains(f.Payload, []byte(`"relay_off"`)) {
			break
		}
	}
	if msgs := host.drain(200 * time.Millisecond); len(msgs) != 0 {
		t.Fatalf("the host heard %v", msgs)
	}
	// Still connected: an offer still reaches the host.
	offer, _ := proto.EncodeJSON(proto.TypeSignal, map[string]any{"t": proto.SigOffer, "sdp": "v=0 fake"})
	v.send(offer)
	host.expect(proto.HostOffer)

	// A relay-only host is refused.
	ctx := t.Context()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-host-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	b, _ := json.Marshal(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"},
		Session: proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24, RelayOnly: true}})
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("a relay-only host was registered on a switchyard without a relay")
	} else if ce := websocket.CloseStatus(err); ce != websocket.StatusCode(proto.CloseProtocolError) {
		t.Fatalf("close %v: %v", ce, err)
	}
}

// The join route answers the desktop app's own workbench across origins
// (loopback by default), and no other origin; a plain server answers none.
func TestSwitchyardAnswersLoopbackOriginsOnTheJoinRoute(t *testing.T) {
	// Outside dev mode: dev allows every localhost origin on every route.
	e := newTestEnv(t, func(c *config.Config) {
		c.Dev = false
		c.Switchyard.Enabled = true
	})
	host := dialFakeHost(t, e, "hosted-agent-token")
	token := host.link("view", "")
	get := func(env *testEnv, path, origin, method string) *http.Response {
		req, _ := http.NewRequest(method, env.http.URL+path, nil)
		req.Header.Set("Origin", origin)
		resp, err := env.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := get(e, "/api/join/"+token, "http://127.0.0.1:43123", "GET"); resp.Header.Get("Access-Control-Allow-Origin") != "http://127.0.0.1:43123" || resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback origin: %d %v", resp.StatusCode, resp.Header)
	}
	if resp := get(e, "/api/join/"+token, "http://localhost:5173", "OPTIONS"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight: %d", resp.StatusCode)
	}
	if resp := get(e, "/api/join/"+token, "https://evil.example", "GET"); resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("a stranger's origin was allowed: %v", resp.Header)
	}
	if resp := get(e, "/api/health", "http://127.0.0.1:43123", "GET"); resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("another route was allowed: %v", resp.Header)
	}
	plain := newTestEnv(t, func(c *config.Config) { c.Dev = false })
	if resp := get(plain, "/api/health", "http://127.0.0.1:43123", "GET"); resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("a plain server allowed an origin: %v", resp.Header)
	}
	// Configured origins replace the loopback ones.
	custom := newTestEnv(t, func(c *config.Config) {
		c.Dev = false
		c.Switchyard.Enabled = true
		c.Switchyard.AllowedOrigins = []string{"app.example:*"}
	})
	h2 := dialFakeHost(t, custom, "hosted-agent-token")
	tok2 := h2.link("view", "")
	if resp := get(custom, "/api/join/"+tok2, "https://app.example:8443", "GET"); resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example:8443" {
		t.Fatalf("configured origin: %v", resp.Header)
	}
	if resp := get(custom, "/api/join/"+tok2, "http://127.0.0.1:43123", "GET"); resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("loopback allowed beside configured origins: %v", resp.Header)
	}
}

// originMatches takes host patterns, as the WebSocket accept does.
func TestOriginMatches(t *testing.T) {
	patterns := []string{"127.0.0.1:*", "localhost:*", "app.example"}
	for origin, want := range map[string]bool{
		"http://127.0.0.1:1234": true, "http://localhost:3000": true, "https://app.example": true, "https://app.example:443": false,
		"http://127.0.0.1": false, "https://evil.example": false, "": false, "not a url": false,
	} {
		if got := originMatches(origin, patterns); got != want {
			t.Errorf("%q: %v", origin, got)
		}
	}
}

// A switchyard with a relay bound slows a host that streams more than it:
// every frame still arrives, in order, but no faster than the bound.
func TestSwitchyardRelayIsThrottledPerHost(t *testing.T) {
	noBurstFloor(t)
	e := newTestEnv(t, func(c *config.Config) {
		c.Switchyard.Enabled = true
		c.Switchyard.RelayKBps = 256
	})
	host := dialFakeHost(t, e, "hosted-agent-token")
	if took := relayOneMiB(t, e, host); took < 1500*time.Millisecond {
		t.Fatalf("1 MiB went through in %v with a bound of 256 KiB/s", took)
	}
}

// A host's messages count in its relay bound as its relayed frames do: the
// answers it sends a viewer go no faster than the bound either.
func TestSwitchyardRelayBoundCountsAHostsMessages(t *testing.T) {
	noBurstFloor(t)
	e := newTestEnv(t, func(c *config.Config) {
		c.Switchyard.Enabled = true
		c.Switchyard.RelayKBps = 256
	})
	host := dialFakeHost(t, e, "hosted-agent-token")
	v := dialViewer(t, e, host.sessionID, host.link("view", ""))
	v.hello(80, 24)
	v.expectControl(proto.CtlWelcome)
	viewerID, _ := host.expect(proto.HostViewerJoin)["viewerId"].(string)
	// 1 MiB of answers in 32 messages: past the burst of 512 KiB, two
	// seconds or more.
	const messages = 32
	sdp := strings.Repeat("a", 32<<10)
	start := time.Now()
	go func() {
		for i := 0; i < messages; i++ {
			b, _ := json.Marshal(proto.ViewerSDP{T: proto.HostAnswer, ViewerID: viewerID, SDP: sdp})
			if host.c.Write(t.Context(), websocket.MessageText, b) != nil {
				return
			}
		}
	}()
	for got := 0; got < messages; {
		f, err := v.read()
		if err != nil {
			t.Fatalf("after %d answers: %v", got, err)
		}
		if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeSignal && tt == proto.SigAnswer {
			got++
		}
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("1 MiB of answers went through in %v with a bound of 256 KiB/s", took)
	}
}

// A host's message is charged what the server forwards for it, not only what
// the host sent, and before the viewer is sent it: an answer the server
// encodes anew, six bytes for each '<' the host sent as one. With a bound of
// 8 KiB/s (a burst of 16 KiB, which the bucket never holds more than), an
// answer of 10.6 KiB of '<' is read within the burst, and its frame of
// 63.6 KiB (within a SIGNAL frame's 64 KiB), charged before the viewer is
// sent it, owes the bound 47 KiB at the least: the viewer gets the answer
// no sooner than five seconds after the host sent it, whatever the machine
// does meanwhile (a stall only delays it), and within twenty. Charged as
// read alone, it would arrive at once.
func TestSwitchyardRelayBoundCountsWhatAViewerIsSent(t *testing.T) {
	noBurstFloor(t)
	e := newTestEnv(t, func(c *config.Config) {
		c.Switchyard.Enabled = true
		c.Switchyard.RelayKBps = 8
	})
	host := dialFakeHost(t, e, "hosted-agent-token")
	v := dialViewer(t, e, host.sessionID, host.link("view", ""))
	v.expectControl(proto.CtlWelcome)
	viewerID, _ := host.expect(proto.HostViewerJoin)["viewerId"].(string)
	// By hand: json.Marshal would escape the '<'.
	answer := []byte(`{"t":"` + proto.HostAnswer + `","viewerId":"` + viewerID + `","sdp":"` + strings.Repeat("<", 10854) + `"}`)
	if n := len(proto.Encode(proto.TypeSignal, nil)) + len(`{"t":"answer","sdp":""}`) + 6*10854; n > 1+proto.MaxSignal {
		t.Fatalf("the answer's frame, %d bytes, is over what a SIGNAL frame holds", n)
	}
	sent := time.Now()
	if err := host.c.Write(t.Context(), websocket.MessageText, answer); err != nil {
		t.Fatal(err)
	}
	// The reader's own clock says when the answer came: a stall anywhere
	// makes it later, never earlier.
	arrived := make(chan time.Time, 1)
	failed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		for {
			_, data, err := v.c.Read(ctx)
			if err != nil {
				failed <- err
				return
			}
			if len(data) > 1 && data[0] == proto.TypeSignal {
				if tt, _ := proto.ParseHeader(data[1:]); tt == proto.SigAnswer {
					arrived <- time.Now()
					return
				}
			}
		}
	}()
	select {
	case at := <-arrived:
		if took := at.Sub(sent); took < 3*time.Second {
			t.Fatalf("an answer of 63.6 KiB reached the viewer %v after the host sent it, past a bound of 8 KiB/s with a burst of 16 KiB", took)
		}
	case err := <-failed:
		t.Fatalf("the answer never reached the viewer: %v", err)
	}
}

// No SIGNAL frame over its 64 KiB leaves the server: an answer that encodes
// anew past it (eleven KiB of '<', six bytes each as the server writes
// them) is never sent, and the viewer is told why in a CONTROL error
// (bad_frame). On a switchyard that relays, the viewer keeps its
// connection (its timer takes it to the relay) and the host keeps its: the
// next answer reaches the viewer. On one that does not relay, the viewer's
// WebRTC attempt has nowhere to go: it is closed with the error, the host
// told it left, and the host's connection stays. (Before the fix the frame
// of 66 KiB arrived, which the viewer's reader refuses as any reader does.)
func TestSwitchyardNeverSendsAViewerASignalFrameOverItsLimit(t *testing.T) {
	for _, relay := range []bool{true, false} {
		t.Run(fmt.Sprintf("relay %v", relay), func(t *testing.T) {
			e := newTestEnv(t, func(c *config.Config) {
				c.Switchyard.Enabled = true
				c.Switchyard.Relay = &relay
			})
			host := dialFakeHost(t, e, "hosted-agent-token")
			v := dialViewer(t, e, host.sessionID, host.link("view", ""))
			v.expectControl(proto.CtlWelcome)
			viewerID, _ := host.expect(proto.HostViewerJoin)["viewerId"].(string)
			answer := func(sdp string) []byte {
				return []byte(`{"t":"` + proto.HostAnswer + `","viewerId":"` + viewerID + `","sdp":"` + sdp + `"}`)
			}
			if err := host.c.Write(t.Context(), websocket.MessageText, answer(strings.Repeat("<", 11<<10))); err != nil {
				t.Fatal(err)
			}
			m := v.expectControl(proto.CtlError)
			if msg, _ := m["message"].(string); m["code"] != proto.ErrCodeBadFrame || !strings.Contains(msg, "answer") || !strings.Contains(msg, "not sent") {
				t.Fatalf("the viewer was told %v", m)
			}
			if !relay {
				v.expectClose(websocket.StatusInternalError)
				if m := host.expect(proto.HostViewerLeave); m["viewerId"] != viewerID {
					t.Fatalf("the host was told %v", m)
				}
				return
			}
			if err := host.c.Write(t.Context(), websocket.MessageText, answer("v=0 plain")); err != nil {
				t.Fatal(err)
			}
			for {
				f, err := v.read()
				if err != nil {
					t.Fatalf("after the refused answer: %v", err)
				}
				if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeSignal && tt == proto.SigAnswer {
					if !strings.Contains(string(f.Payload), "v=0 plain") {
						t.Fatalf("the answer after the refused one: %s", f.Payload)
					}
					return
				}
			}
		})
	}
}

// relayOneMiB joins host's session as a relayed viewer, streams 1 MiB from
// the host in 64 whole frames and says how long the viewer took to get it all.
func relayOneMiB(t *testing.T, e *testEnv, host *fakeHost) time.Duration {
	t.Helper()
	v := dialViewer(t, e, host.sessionID, host.link("view", ""))
	v.hello(80, 24)
	v.expectControl(proto.CtlWelcome)
	viewerID, _ := host.expect(proto.HostViewerJoin)["viewerId"].(string)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.Simple{T: proto.SigRelay})
	v.send(relay)
	for {
		f, err := v.read()
		if err != nil {
			t.Fatalf("waiting for relay_ok: %v", err)
		}
		if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeSignal && tt == proto.SigRelayOK {
			break
		}
	}
	host.expect(proto.HostRelayStart)
	// 1 MiB in 64 frames of 16 KiB: past the burst of 512 KiB, the rest
	// drains at 256 KiB/s, so the whole takes two seconds or more.
	const frames = 64
	chunk := bytes.Repeat([]byte("q"), 16<<10)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	start := time.Now()
	go func() {
		for i := 0; i < frames; i++ {
			env, _ := proto.EncodeRelay(viewerID, proto.Encode(proto.TypeOutput, chunk))
			if err := host.c.Write(ctx, websocket.MessageBinary, env); err != nil {
				return
			}
		}
	}()
	got := 0
	for got < frames {
		f, err := v.read()
		if err != nil {
			t.Fatalf("after %d frames: %v", got, err)
		}
		if f.Type != proto.TypeOutput {
			continue
		}
		if !bytes.Equal(f.Payload, chunk) {
			t.Fatalf("frame %d is not whole: %d bytes", got, len(f.Payload))
		}
		got++
	}
	return time.Since(start)
}

// A take larger than the burst is charged whole: what it brings past the
// burst is owed, and the next take waits for it.
func TestByteBucketChargesATakePastTheBurstWhole(t *testing.T) {
	b := newByteBucket(100_000, 200_000)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := b.take(ctx, 500_000); err == nil {
		t.Fatal("500 000 bytes went at once from a burst of 200 000")
	}
	ctx2, cancel2 := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel2()
	if err := b.take(ctx2, 1); err == nil {
		t.Fatal("a byte went while 300 000 were owed")
	}
}

// Takes are served in their order: a large one is not held back for ever by
// small ones that keep coming, each fitting what has refilled meanwhile.
func TestByteBucketServesTakesInOrder(t *testing.T) {
	b := newByteBucket(64<<10, 128<<10)
	if err := b.take(t.Context(), 128<<10); err != nil { // the burst, spent
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for b.take(ctx, 8<<10) == nil {
		}
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	lctx, lcancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer lcancel()
	if err := b.take(lctx, 32<<10); err != nil {
		t.Fatalf("a take of 32 KiB waited %v behind takes of 8 KiB at 64 KiB/s: %v", time.Since(start), err)
	}
}

// An address that leaves keeps its bucket until it has refilled: one that
// comes back at once finds what it spent still spent; one that is full again
// goes, and the next holder gets a fresh one.
func TestAddrBucketsKeepASpentBucketUntilItRefills(t *testing.T) {
	var c addrBuckets
	b := c.acquire("a", 100_000, 200_000)
	if err := b.take(t.Context(), 200_000); err != nil {
		t.Fatal(err)
	}
	c.release("a")
	if again := c.acquire("a", 100_000, 200_000); again != b {
		t.Fatal("an address that came back at once got a fresh burst")
	}
	c.release("a")
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		n := len(c.held)
		c.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a refilled bucket nobody holds was kept")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c.acquire("a", 100_000, 200_000) == b {
		t.Fatal("a refilled bucket was handed out again rather than a fresh one")
	}
	c.release("a")
}

// A byte bucket refills at its rate and starts with its burst; a take past
// what it holds is charged whole and waits for what it owes.
func TestByteBucket(t *testing.T) {
	b := newByteBucket(1000, 2000)
	if b.burst != 2000 || b.tokens != 2000 {
		t.Fatalf("%+v", b)
	}
	ctx := t.Context()
	start := time.Now()
	if err := b.take(ctx, 1500); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("a take within the burst waited")
	}
	// 500 left: 1000 more need half a second.
	if err := b.take(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("waited %v, want about 500 ms", d)
	}
	// Cancelled while waiting.
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := b.take(cctx, 2000); err == nil {
		t.Fatal("a cancelled wait returned no error")
	}
}

// dialHostRaw dials the host route with the given Authorization value ("" for
// none) and returns the HTTP status when the upgrade is refused, 101 when it
// went through (the connection is then closed).
func dialHostRaw(t *testing.T, e *testEnv, auth string) int {
	t.Helper()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	if auth != "" {
		header.Set("Authorization", auth)
	}
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if resp == nil {
			t.Fatalf("dial: %v", err)
		}
		return resp.StatusCode
	}
	c.CloseNow()
	return http.StatusSwitchingProtocols
}

func openSwitchyardEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *config.Config) {
		c.Switchyard.Enabled = true
		c.Switchyard.OpenHosts = true
		if mutate != nil {
			mutate(c)
		}
	})
}

// A switchyard that admits open hosts registers one that presents no token
// and shares it as any other; without openHosts, on a plain server, or
// with a wrong token, the route still answers 401.
func TestSwitchyardAdmitsOpenHosts(t *testing.T) {
	e := openSwitchyardEnv(t, nil)
	host := dialFakeHostAuth(t, e, "hosted-agent-token", "")
	if resp, out := e.do("GET", "/api/join/"+host.link("view", "open"), "", nil); resp.StatusCode != http.StatusOK || out["session"] == nil {
		t.Fatalf("join: %d %v", resp.StatusCode, out)
	}
	if got := dialHostRaw(t, e, "Bearer nope"); got != http.StatusUnauthorized {
		t.Fatalf("wrong token on an open switchyard: %d", got)
	}
	if got := dialHostRaw(t, switchyardEnv(t, true), ""); got != http.StatusUnauthorized {
		t.Fatalf("no token without openHosts: %d", got)
	}
	if got := dialHostRaw(t, newTestEnv(t, nil), ""); got != http.StatusUnauthorized {
		t.Fatalf("no token on a plain server: %d", got)
	}
}

// One address holds at most openHostSessions live open sessions; a place
// frees when its connection ends; a host with a token is never counted.
func TestOpenHostSessionsPerAddressAreCapped(t *testing.T) {
	e := openSwitchyardEnv(t, func(c *config.Config) {
		c.Switchyard.OpenHostSessions = 2
		c.Switchyard.OpenHostRegistrationsPerMinute = 600
	})
	first := dialFakeHostAuth(t, e, "a1", "")
	dialFakeHostAuth(t, e, "a2", "")
	if got := dialHostRaw(t, e, ""); got != http.StatusTooManyRequests {
		t.Fatalf("third open host: %d, want 429", got)
	}
	for i := 0; i < 3; i++ {
		dialFakeHost(t, e, "tokened"+string(rune('a'+i)))
	}
	first.c.CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for dialHostRaw(t, e, "") != http.StatusSwitchingProtocols {
		if time.Now().After(deadline) {
			t.Fatal("the place of a closed open host was not freed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Open registrations from one address are rate limited; tokened ones are not.
func TestOpenHostRegistrationsAreRateLimitedPerAddress(t *testing.T) {
	e := openSwitchyardEnv(t, func(c *config.Config) {
		c.Switchyard.OpenHostRegistrationsPerMinute = 2
		c.Switchyard.OpenHostSessions = 100
	})
	for i := 0; i < 2; i++ {
		if got := dialHostRaw(t, e, ""); got != http.StatusSwitchingProtocols {
			t.Fatalf("open registration %d: %d", i+1, got)
		}
	}
	if got := dialHostRaw(t, e, ""); got != http.StatusTooManyRequests {
		t.Fatalf("third open registration in a minute: %d, want 429", got)
	}
	if got := dialHostRaw(t, e, "Bearer test-host-token"); got != http.StatusSwitchingProtocols {
		t.Fatalf("a tokened host after the open limit: %d", got)
	}
}

// An open host relays under openHostRelayKBps, its own bound.
func TestOpenHostsRelayUnderTheirOwnBound(t *testing.T) {
	noBurstFloor(t)
	e := openSwitchyardEnv(t, func(c *config.Config) { c.Switchyard.OpenHostRelayKBps = 256; c.Switchyard.RelayKBps = 0 })
	host := dialFakeHostAuth(t, e, "hosted-agent-token", "")
	if took := relayOneMiB(t, e, host); took < 1500*time.Millisecond {
		t.Fatalf("an open host sent 1 MiB in %v under a bound of 256 KiB/s", took)
	}
}

// A hosted session takes its viewer cap from the config, as a server session does.
func TestHostedSessionsTakeMaxViewersFromTheConfig(t *testing.T) {
	e := switchyardEnv(t, true)
	e2 := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true; c.MaxViewersPerSession = 1 })
	_ = e
	host := dialFakeHost(t, e2, "hosted-agent-token")
	token := host.link("view", "")
	v1 := dialViewer(t, e2, host.sessionID, token)
	v1.hello(80, 24)
	v1.expectControl(proto.CtlWelcome)
	v2 := dialViewer(t, e2, host.sessionID, token)
	v2.hello(80, 24)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := v2.read()
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusCode(proto.CloseTooManyViewers) {
				t.Fatalf("second viewer: %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the second viewer was let in past a cap of one")
		}
	}
}

// One bucket shared by many takers never hands out more than its burst and
// what it refills meanwhile.
func TestByteBucketIsSafeToShare(t *testing.T) {
	b := newByteBucket(64<<10, 128<<10)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	var mu sync.Mutex
	taken := 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b.take(ctx, 4<<10) == nil {
				mu.Lock()
				taken += 4 << 10
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if limit := 2*(64<<10) + int(time.Since(start).Seconds()*float64(64<<10)) + 4<<10; taken > limit {
		t.Fatalf("took %d bytes, at most %d", taken, limit)
	}
}

func TestAddrBucketsShareOneBucketPerAddress(t *testing.T) {
	var c addrBuckets
	a1, a2, b := c.acquire("a", 1024, 2048), c.acquire("a", 1024, 2048), c.acquire("b", 1024, 2048)
	if a1 != a2 || a1 == b {
		t.Fatal("an address gets one bucket, another address its own")
	}
	c.release("a")
	if c.acquire("a", 1024, 2048) != a1 {
		t.Fatal("the bucket goes only with its last holder")
	}
	c.release("a")
	c.release("a")
	if c.acquire("a", 1024, 2048) == a1 {
		t.Fatal("a fresh bucket after the address left")
	}
	c.release("a")
	c.release("b")
	if len(c.held) != 0 {
		t.Fatalf("held %v", c.held)
	}
}

// The open hosts of one address relay under one bound: a second host's
// relay finds the bucket the first drained, where a bound per connection
// would give it a fresh burst.
func TestOpenHostsFromOneAddressShareTheRelayBound(t *testing.T) {
	noBurstFloor(t)
	e := openSwitchyardEnv(t, func(c *config.Config) { c.Switchyard.OpenHostRelayKBps = 256; c.Switchyard.RelayKBps = 0 })
	first := dialFakeHostAuth(t, e, "hosted-agent-token", "")
	second := dialFakeHostAuth(t, e, "hosted-agent-token", "")
	relayOneMiB(t, e, first)
	if took := relayOneMiB(t, e, second); took < 3*time.Second {
		t.Fatalf("the second open host of the address relayed 1 MiB in %v: its bound is its own", took)
	}
}

// On a switchyard the workbench token lists the sessions published there and
// has no role on any of them: it neither views nor controls one, mints no
// link to one, reads none of its files and reports nothing for it. The link
// the publishing machine had minted over its own connection is what grants a
// role. On a server of the owner's own, the token is control on a hosted
// session as ever.
func TestOnASwitchyardTheWorkbenchTokenHasNoRole(t *testing.T) {
	e := switchyardEnv(t, true)
	host := dialFakeHost(t, e, "hosted-agent-token")
	id := host.sessionID

	_, list := e.do("GET", "/api/sessions", adminToken, nil)
	if sessions, _ := list["sessions"].([]any); len(sessions) != 1 {
		t.Fatalf("listing %v", list)
	}
	if resp, out := e.do("GET", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusOK || out["role"] != "" || out["session"] == nil {
		t.Fatalf("get: %d %v", resp.StatusCode, out)
	}
	admin := dialViewer(t, e, id, adminToken)
	admin.expectClose(proto.CloseUnauthorized)
	if resp, out := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"}); resp.StatusCode != http.StatusForbidden || errorCode(out) != "switchyard" {
		t.Fatalf("link: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.do("GET", "/api/sessions/"+id+"/files?path=x", adminToken, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("files: %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/sessions/"+id+"/attention", adminToken, map[string]any{"state": "needs_input", "message": "1", "options": []map[string]any{{"label": "Yes", "input": "y\r"}}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("attention: %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "progress", "message": "x"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events: %d", resp.StatusCode)
	}
	if msgs := host.drain(200 * time.Millisecond); len(ofType(msgs, proto.HostViewerJoin)) != 0 || len(ofType(msgs, proto.HostAttention)) != 0 || len(ofType(msgs, proto.HostActivity)) != 0 {
		t.Fatalf("the host was sent %v", msgs)
	}
	// The session's own agent still reports.
	if resp, _ := e.do("POST", "/api/sessions/"+id+"/attention", "hosted-agent-token", map[string]any{"state": "working"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the agent's report: %d", resp.StatusCode)
	}
	// The publishing machine's link grants its role.
	guest := dialViewer(t, e, id, host.link("control", "for Priya"))
	guest.hello(80, 24)
	if w := guest.expectControl(proto.CtlWelcome); w["role"] != "control" {
		t.Fatalf("guest welcome %v", w)
	}
	if join := host.expect(proto.HostViewerJoin); join["role"] != "control" || join["linkLabel"] != "for Priya" {
		t.Fatalf("viewer_join %v", join)
	}

	// The owner's own server: the token is control on a hosted session.
	own := newTestEnv(t, nil)
	h2 := dialFakeHost(t, own, "hosted-agent-token")
	v := dialViewer(t, own, h2.sessionID, adminToken)
	v.hello(80, 24)
	if w := v.expectControl(proto.CtlWelcome); w["role"] != "control" {
		t.Fatalf("on the owner's server %v", w)
	}
}

// A switchyard keeps no chat and no activity of the sessions published
// there: a host that sends them (an older publisher, a `conductor host`)
// reaches no stream and no webhook. Its attention state still shows.
func TestASwitchyardKeepsNoChatOrActivity(t *testing.T) {
	e := switchyardEnv(t, true)
	host := dialFakeHost(t, e, "hosted-agent-token")
	var mu sync.Mutex
	var entries []session.ActivityEntry
	e.srv.events.addSink(func(_ string, en session.ActivityEntry, _ session.AttentionState) {
		mu.Lock()
		entries = append(entries, en)
		mu.Unlock()
	})
	ch := e.srv.events.subscribe()
	defer e.srv.events.unsubscribe(ch)
	host.send(proto.HostChatMsg{T: proto.HostChat, Message: proto.ChatMessage{T: proto.CtlChat, ID: "m1", At: time.Now().UTC().Format(time.RFC3339Nano), Scope: proto.ChatScopeSession, Kind: proto.ChatKindMessage, By: proto.ChatBy{ID: "b1", Name: "Nate", Role: "control"}, Text: "between us"}})
	host.send(proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{T: proto.CtlActivity, Type: session.ActivityProgress, At: time.Now().UTC().Format(time.RFC3339Nano), Message: "rotated the key"}})
	host.send(proto.HostAttentionMsg{T: proto.HostAttention, State: string(session.AttentionNeedsInput), Source: session.SourceAPI})
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-ch:
			s := string(msg)
			if strings.HasPrefix(s, "event: chat") || strings.HasPrefix(s, "event: activity") {
				t.Fatalf("the switchyard's stream carried %s", s)
			}
			if strings.HasPrefix(s, "event: session") && strings.Contains(s, `"needs_input"`) {
				mu.Lock()
				defer mu.Unlock()
				if len(entries) != 0 {
					t.Fatalf("the switchyard's sinks got %+v", entries)
				}
				return
			}
		case <-deadline:
			t.Fatal("the attention state never showed")
		}
	}
}
