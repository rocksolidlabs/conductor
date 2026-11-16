package hostagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

const (
	adminToken = "admin-token"
	hostToken  = "host-token"
)

func startServer(t *testing.T) (*api.Server, *httptest.Server) {
	t.Helper()
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.HostTokens = []string{hostToken}
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs
}

type viewer struct {
	t *testing.T
	c *websocket.Conn
}

func dialViewer(t *testing.T, base, sessionID, token string) *viewer {
	t.Helper()
	url := strings.Replace(base, "http://", "ws://", 1) + "/ws/sessions/" + sessionID + "?token=" + token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial viewer: %v", err)
	}
	c.SetReadLimit(proto.MaxFrame)
	t.Cleanup(func() { c.CloseNow() })
	return &viewer{t: t, c: c}
}

func (v *viewer) send(frame []byte) {
	v.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := v.c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		v.t.Fatalf("write: %v", err)
	}
}

func (v *viewer) read() (proto.Frame, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := v.c.Read(ctx)
	if err != nil {
		return proto.Frame{}, err
	}
	return proto.Decode(data)
}

func (v *viewer) expectJSON(typ byte, t string) map[string]any {
	v.t.Helper()
	for {
		f, err := v.read()
		if err != nil {
			v.t.Fatalf("waiting for %s: %v", t, err)
		}
		if f.Type != typ {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		if m["t"] == t {
			return m
		}
	}
}

func (v *viewer) expectOutput(needle string) {
	v.t.Helper()
	var acc []byte
	for !bytes.Contains(acc, []byte(needle)) {
		f, err := v.read()
		if err != nil {
			v.t.Fatalf("waiting for %q: %v (have %q)", needle, err, acc)
		}
		if f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback {
			acc = append(acc, f.Payload...)
		}
	}
}

func TestHostRelayEndToEnd(t *testing.T) {
	srv, hs := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registered := make(chan string, 1)
	done := make(chan Result, 1)
	go func() {
		res, err := Run(ctx, Options{
			ServerURL: hs.URL,
			Token:     hostToken,
			Name:      "relay test",
			HostName:  "laptop",
			Argv:      []string{"/bin/cat"},
			RelayOnly: true,
			Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			Registered: func(id, base string) {
				registered <- id
			},
		})
		if err != nil {
			t.Errorf("run: %v", err)
		}
		done <- res
	}()
	var sessionID string
	select {
	case sessionID = <-registered:
	case <-time.After(10 * time.Second):
		t.Fatal("host did not register")
	}
	d, ok := srv.Registry().Get(sessionID)
	if !ok || d.Info().Kind != "hosted" || d.Info().HostName != "laptop" {
		t.Fatalf("hosted session not listed: %v %+v", ok, d)
	}

	v := dialViewer(t, hs.URL, sessionID, adminToken)
	welcome := v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	if welcome["transport"] != "webrtc" || welcome["viewerId"] == "" {
		t.Fatalf("welcome %v", welcome)
	}
	// Terminal frames before relay mode are rejected.
	v.send(proto.Encode(proto.TypeInput, []byte("x")))
	if m := v.expectJSON(proto.TypeControl, proto.CtlError); m["code"] != proto.ErrCodeBadFrame {
		t.Fatalf("pre-relay input: %v", m)
	}
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	v.send(relay)
	v.expectJSON(proto.TypeSignal, proto.SigRelayOK)
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 90, Rows: 25}))
	w2 := v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	if w2["transport"] != "relay" || w2["role"] != "control" {
		t.Fatalf("relay welcome %v", w2)
	}
	v.expectJSON(proto.TypeControl, proto.CtlReady)
	v.send(proto.Encode(proto.TypeInput, []byte("through the relay\n")))
	v.expectOutput("through the relay")

	// A bell echoed by cat marks the hosted session as needing input on the
	// server; a line entered withdraws it (keys answer no question). An
	// API-set state reaches the relay viewer.
	v.send(proto.Encode(proto.TypeInput, []byte("\a\n")))
	waitAttention := func(state string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if string(d.Info().Attention.State) == state {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("server attention %+v, want %q", d.Info().Attention, state)
	}
	waitAttention("needs_input")
	v.send(proto.Encode(proto.TypeInput, []byte("z\r")))
	waitAttention("")
	req0, _ := http.NewRequest("POST", hs.URL+"/api/sessions/"+sessionID+"/attention", strings.NewReader(`{"state":"needs_input","message":"from api"}`))
	req0.Header.Set("Authorization", "Bearer "+adminToken)
	req0.Header.Set("Content-Type", "application/json")
	if resp, err := hs.Client().Do(req0); err != nil || resp.StatusCode != 200 {
		t.Fatalf("api attention: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	for {
		m := v.expectJSON(proto.TypeControl, proto.CtlAttention)
		if m["message"] == "from api" && m["source"] == "admin" {
			break
		}
	}

	// A view-role link cannot type through the relay.
	link, tok, err := srv.Links().Create(sessionID, "view", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	ro := dialViewer(t, hs.URL, sessionID, tok)
	ro.expectJSON(proto.TypeControl, proto.CtlWelcome)
	ro.send(relay)
	ro.expectJSON(proto.TypeSignal, proto.SigRelayOK)
	ro.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24}))
	ro.expectOutput("through the relay") // scrollback replay precedes ready
	ro.expectJSON(proto.TypeControl, proto.CtlReady)
	ro.send(proto.Encode(proto.TypeInput, []byte("nope")))
	if m := ro.expectJSON(proto.TypeControl, proto.CtlError); m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("read-only relay: %v", m)
	}
	srv.Links().Revoke(sessionID, link.ID)
	for {
		if _, err := ro.read(); err != nil {
			if websocket.CloseStatus(err) != proto.CloseForbidden {
				t.Fatalf("revoked viewer close: %v", err)
			}
			break
		}
	}

	// Stopping through the API reaches the host.
	req, _ := http.NewRequest("DELETE", hs.URL+"/api/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := hs.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case res := <-done:
		if res.SessionID != sessionID {
			t.Fatalf("result %+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("host did not exit after stop")
	}
	if m := v.expectJSON(proto.TypeControl, proto.CtlStatus); m["status"] != "stopped" {
		t.Fatalf("status %v", m)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info := d.Info(); info.Status == "stopped" || info.Status == "exited" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server did not observe the exit: %+v", d.Info())
}

func TestHostDisconnectClosesViewers(t *testing.T) {
	srv, hs := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	registered := make(chan string, 1)
	go Run(ctx, Options{
		ServerURL: hs.URL, Token: hostToken, Argv: []string{"/bin/cat"}, RelayOnly: true,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registered: func(id, _ string) { registered <- id },
	})
	sessionID := <-registered
	v := dialViewer(t, hs.URL, sessionID, adminToken)
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	cancel() // host goes away
	for {
		f, err := v.read()
		if err != nil {
			if websocket.CloseStatus(err) != proto.CloseSessionEnded {
				t.Fatalf("expected 4410, got %v", err)
			}
			break
		}
		_ = f
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d, ok := srv.Registry().Get(sessionID); ok && (d.Info().Status == "host_disconnected" || d.Info().Status == "stopped") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session status not updated after host disconnect")
}

// adminFeed opens GET /api/events as the admin and returns what it streams as
// "<event> <data>" strings.
func adminFeed(t *testing.T, base string) <-chan string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "GET", base+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("events stream: %v %v", err, resp)
	}
	t.Cleanup(func() { resp.Body.Close() })
	feed := make(chan string, 1024)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				select {
				case feed <- name + " " + strings.TrimPrefix(line, "data: "):
				case <-t.Context().Done():
					return
				}
			}
		}
	}()
	return feed
}

func waitFeed(t *testing.T, feed <-chan string, what string, match func(string) bool) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-feed:
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("never saw %s", what)
		}
	}
}

// An event reported for a hosted session travels server, host, server before
// the admin stream shows it, and the host reports its own last entry, the
// final status, before it lets go of its connection.
func TestHostActivityTravelsBothWaysThroughTheServer(t *testing.T) {
	_, hs := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registered := make(chan string, 1)
	done := make(chan Result, 1)
	go func() {
		res, err := Run(ctx, Options{
			ServerURL: hs.URL, Token: hostToken, Name: "activity test", Argv: []string{"/bin/cat"}, RelayOnly: true,
			Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			Registered: func(id, _ string) { registered <- id },
		})
		if err != nil {
			t.Errorf("run: %v", err)
		}
		done <- res
	}()
	var sessionID string
	select {
	case sessionID = <-registered:
	case <-time.After(10 * time.Second):
		t.Fatal("host did not register")
	}
	feed := adminFeed(t, hs.URL)

	post := func(body string) {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/api/sessions/"+sessionID+"/events", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("post %s: %d", body, resp.StatusCode)
		}
	}
	post(`{"type":"artifact","message":"PR opened","url":"https://github.com/x/y/pull/1"}`)
	ev := waitFeed(t, feed, "the artifact entry", func(ev string) bool {
		return strings.HasPrefix(ev, "activity ") && strings.Contains(ev, `"type":"artifact"`) && strings.Contains(ev, sessionID)
	})
	for _, want := range []string{`"sessionId":"` + sessionID + `"`, `"byName":"agent"`, `"message":"PR opened"`, `"url":"https://github.com/x/y/pull/1"`} {
		if !strings.Contains(ev, want) {
			t.Fatalf("streamed %s, missing %s", ev, want)
		}
	}

	// Stopping the session ends the process; the final status entry, made
	// on the host, reaches the stream before the host exits.
	req, _ := http.NewRequest("DELETE", hs.URL+"/api/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFeed(t, feed, "the final status entry", func(ev string) bool {
		return strings.HasPrefix(ev, "activity ") && strings.Contains(ev, `"type":"status"`) && strings.Contains(ev, `"message":"stopped`) && strings.Contains(ev, sessionID)
	})
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("host did not exit after stop")
	}
}

// SIGINT and SIGTERM cancel Run's context. The host stops the process and
// leaves: it has no connection left to say anything on, so it waits for
// nothing.
func TestHostExitsPromptlyWhenCancelled(t *testing.T) {
	_, hs := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registered := make(chan string, 1)
	done := make(chan Result, 1)
	go func() {
		res, err := Run(ctx, Options{
			ServerURL: hs.URL, Token: hostToken, Name: "cancel test", Argv: []string{"/bin/cat"}, RelayOnly: true,
			Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			Registered: func(id, _ string) { registered <- id },
		})
		if err != nil {
			t.Errorf("run: %v", err)
		}
		done <- res
	}()
	select {
	case <-registered:
	case <-time.After(10 * time.Second):
		t.Fatal("host did not register")
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("host did not exit after cancel")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("Run took %v to return after its context was cancelled", took)
	}
}

// A server that forgot the session (a restarted switchyard) answers a
// resume with 4404; the host then registers afresh under a new id instead of
// retrying the resume forever, and Registered is called again with it.
func TestHostRegistersAfreshWhenTheServerForgotTheSession(t *testing.T) {
	var attempts atomic.Int32
	resumes := make(chan string, 4)
	idents := make(chan string, 4)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/host" {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		n := attempts.Add(1)
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var reg proto.Register
		if json.Unmarshal(data, &reg) != nil {
			c.Close(websocket.StatusProtocolError, "bad register")
			return
		}
		if reg.Resume != nil {
			resumes <- reg.Resume.SessionID
		} else {
			resumes <- ""
		}
		idents <- reg.Host.Instance + "|" + reg.Session.LocalID
		switch n {
		case 1:
			_ = c.Write(r.Context(), websocket.MessageText, mustJSON(proto.Registered{T: proto.HostRegistered, SessionID: "s1", Secret: "sec", ShareBaseURL: "http://sy.test"}))
			time.Sleep(150 * time.Millisecond)
			c.Close(websocket.StatusGoingAway, "restarting")
		case 2:
			c.Close(websocket.StatusCode(proto.CloseNotFound), "cannot resume session")
		default:
			_ = c.Write(r.Context(), websocket.MessageText, mustJSON(proto.Registered{T: proto.HostRegistered, SessionID: "s2", Secret: "sec2", ShareBaseURL: "http://sy.test"}))
			for {
				if _, _, err := c.Read(r.Context()); err != nil {
					return
				}
			}
		}
	}))
	defer fake.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := make(chan string, 4)
	go Run(ctx, Options{
		ServerURL: fake.URL, Token: "host-token", Name: "x", HostName: "box", Argv: []string{"sleep", "30"},
		ReconnectMax: 50 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registered: func(id, base string) { ids <- id },
	})
	want := func(ch chan string, v, what string) {
		t.Helper()
		select {
		case got := <-ch:
			if got != v {
				t.Fatalf("%s: got %q, want %q", what, got, v)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: nothing within 10s", what)
		}
	}
	want(ids, "s1", "first registration")
	want(resumes, "", "first attempt resumes nothing")
	want(resumes, "s1", "second attempt resumes s1")
	want(resumes, "", "third attempt registers afresh")
	want(ids, "s2", "registered again under the new id")
	// Every attempt names the same instance and local id: a server that keeps
	// instances gives the session its id back.
	first := <-idents
	if !strings.HasSuffix(first, "|1") || len(first) < 20 {
		t.Fatalf("instance and local id %q", first)
	}
	want(idents, first, "the second attempt's instance")
	want(idents, first, "the third attempt's instance")
}

// What is said in a hosted session's chat reaches the server's admin stream
// as a `chat` event of the hosted session: the host's own OnChat hook queues
// it for the control connection.
func TestHostChatTravelsToTheServerStream(t *testing.T) {
	_, hs := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registered := make(chan string, 1)
	done := make(chan Result, 1)
	go func() {
		res, err := Run(ctx, Options{
			ServerURL: hs.URL, Token: hostToken, Name: "chat test", Argv: []string{"/bin/cat"}, RelayOnly: true,
			Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			Registered: func(id, _ string) { registered <- id },
		})
		if err != nil {
			t.Errorf("run: %v", err)
		}
		done <- res
	}()
	var sessionID string
	select {
	case sessionID = <-registered:
	case <-time.After(10 * time.Second):
		t.Fatal("host did not register")
	}
	feed := adminFeed(t, hs.URL)
	v := dialViewer(t, hs.URL, sessionID, adminToken)
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	v.send(relay)
	v.expectJSON(proto.TypeSignal, proto.SigRelayOK)
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 90, Rows: 25, Name: "Nate"}))
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	v.expectJSON(proto.TypeControl, proto.CtlReady)
	v.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "hosted hello", Nonce: "h1"}))
	ev := waitFeed(t, feed, "the chat message", func(ev string) bool {
		return strings.HasPrefix(ev, "chat ") && strings.Contains(ev, `"text":"hosted hello"`)
	})
	for _, want := range []string{`"sessionId":"` + sessionID + `"`, `"kind":"message"`, `"name":"Nate"`, `"role":"control"`} {
		if !strings.Contains(ev, want) {
			t.Fatalf("streamed %s, missing %s", ev, want)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("host did not stop")
	}
}

// A host back from away tells the server its session's attention as the
// connection starts, the empty state included: the server keeps the
// attention it had when the host went, so a question answered at the host's
// own terminal meanwhile would stay in its listings (the sidebar, the Yard,
// a switchyard's view) until the session's next change.
func TestAHostBackFromAwayTellsTheServerItsAttentionTheEmptyStateToo(t *testing.T) {
	srv, hs := startServer(t)
	a := &agent{opts: Options{ServerURL: hs.URL, Token: hostToken, Name: "away", Argv: []string{"stub"}, AgentID: "stub", HostName: "h", Log: discardLog}, peers: map[string]*peer{}, log: discardLog}
	a.dir = t.TempDir()
	wsURL, err := controlURL(hs.URL)
	if err != nil {
		t.Fatal(err)
	}
	a.wsURL = wsURL
	ctx := t.Context()
	conn, registered, err := a.dialAndRegister(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The session, named as Run names it, its changes forwarded as there.
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	local := session.NewLocal(session.Info{ID: registered.SessionID, Cwd: a.dir, Cols: 80, Rows: 24}, proc, session.Options{OnChange: a.noteChange, Log: discardLog})
	a.mu.Lock()
	a.local = local
	a.mu.Unlock()
	serve := func(c *websocket.Conn) <-chan error {
		served := make(chan error, 1)
		go func() { served <- a.serveConn(ctx, c) }()
		return served
	}
	served := serve(conn)
	d, ok := srv.Registry().Get(registered.SessionID)
	if !ok {
		t.Fatal("the hosted session is not listed")
	}
	listed := func(what string, want func(session.Info) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !want(d.Info()) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the server lists %+v", what, d.Info())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	local.SetAttentionFull(session.AttentionNeedsInput, "Allow?", session.SourceAPI, session.KindPermission, nil)
	listed("the question told", func(i session.Info) bool { return i.Attention.State == session.AttentionNeedsInput })

	// The host goes away, and the question is answered at its terminal
	// meanwhile: the server, told nothing, lists the question still.
	conn.CloseNow()
	<-served
	listed("the host gone", func(i session.Info) bool { return i.Status == session.StatusHostDisconnected })
	local.SetAttention(session.AttentionNone, "", session.SourceInput)
	if att := local.Info().Attention; att.State != session.AttentionNone {
		t.Fatalf("the session's attention %+v", att)
	}
	if att := d.Info().Attention; att.State != session.AttentionNeedsInput {
		t.Fatalf("the server lists %+v with the host away", att)
	}

	// Back: the server's listing matches the session again.
	conn2, again, err := a.dialAndRegister(ctx)
	if err != nil || !again.Resumed {
		t.Fatalf("resume: %v %+v", err, again)
	}
	t.Cleanup(func() { conn2.CloseNow() })
	serve(conn2)
	listed("the host back", func(i session.Info) bool {
		return i.Status == session.StatusRunning && i.Attention.State == session.AttentionNone
	})
}

// untilListed waits for the server's listing of a hosted session to show
// what want asks for: polled, the host's frames being the server's to read
// in their own time.
func untilListed(t *testing.T, d session.Driver, what string, want func(session.Info) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !want(d.Info()) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the server lists %+v", what, d.Info())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// hostedBy registers a host built as TestAHostBackFromAwayTellsTheServerItsAttentionTheEmptyStateToo
// builds one, its session named as Run names it and its changes forwarded
// as there. It returns the agent, the session, the first connection and
// its registration, and serve, which serves a connection.
func hostedBy(t *testing.T, base string) (*agent, *session.Local, *websocket.Conn, proto.Registered, func(*websocket.Conn) <-chan error) {
	t.Helper()
	a, local, conn, registered, serve, _ := hostedOver(t, base, nil)
	return a, local, conn, registered, serve
}

// hostedOver is hostedBy with the session's window title read by title
// (Options.WorkingTitle, none when nil), returning its process too.
func hostedOver(t *testing.T, base string, title *regexp.Regexp) (*agent, *session.Local, *websocket.Conn, proto.Registered, func(*websocket.Conn) <-chan error, *sessiontest.FakeProc) {
	t.Helper()
	a := &agent{opts: Options{ServerURL: base, Token: hostToken, Name: "turns", Argv: []string{"stub"}, AgentID: "stub", HostName: "h", Log: discardLog}, peers: map[string]*peer{}, log: discardLog}
	a.dir = t.TempDir()
	wsURL, err := controlURL(base)
	if err != nil {
		t.Fatal(err)
	}
	a.wsURL = wsURL
	ctx := t.Context()
	conn, registered, err := a.dialAndRegister(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	local := session.NewLocal(session.Info{ID: registered.SessionID, Cwd: a.dir, Cols: 80, Rows: 24}, proc, session.Options{OnChange: a.noteChange, Log: discardLog, WorkingTitle: title})
	a.mu.Lock()
	a.local = local
	a.mu.Unlock()
	serve := func(c *websocket.Conn) <-chan error {
		served := make(chan error, 1)
		go func() { served <- a.serveConn(ctx, c) }()
		return served
	}
	return a, local, conn, registered, serve, proc
}

// enter types an Enter into the host's session as a controlling viewer
// does; the step that ends a done shown is taken before the write returns.
func enter(t *testing.T, local *session.Local) {
	t.Helper()
	sub, err := local.Attach("", session.RoleControl, "", 80, 24, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Detach(sub)
	if err := local.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
}

// An Enter that reaches a host's session through the server, after its
// done, is told the server: its copy shows the empty state with source
// input, as the host's session does. The relayed viewer is held by the
// host's peer first (it has seen the session's output).
func TestAnEnterAfterADoneIsToldTheServer(t *testing.T) {
	srv, base, id := relayHostOn(t, []string{"/bin/sh", "-c", "echo ready; exec cat"})
	d, ok := srv.Registry().Get(id)
	if !ok {
		t.Fatal("the hosted session is not listed")
	}
	hs, ok := d.(*signal.HostedSession)
	if !ok {
		t.Fatalf("listed as %T", d)
	}
	v := relayControllerSeeing(t, base, id, "ready")
	if err := hs.SetAttentionFull(session.AttentionDone, "All tests pass.", session.SourceAPI, session.KindDone, nil, true); err != nil {
		t.Fatal(err)
	}
	// The host's own report of it, with the id its session gave it.
	untilListed(t, d, "the host's done", func(i session.Info) bool {
		return i.Attention.State == session.AttentionDone && i.Attention.ID != "" && i.Turns
	})
	v.send(proto.Encode(proto.TypeInput, []byte("\r")))
	untilListed(t, d, "the Enter told", func(i session.Info) bool {
		return i.Attention.State == session.AttentionNone && i.Attention.Source == session.SourceInput && i.Turns
	})
}

// A server that forgot the session (it restarted) has the host register
// afresh: the new copy saw no report of the agent's, and learns from the
// host's attention frame that its agent reports its turns, with the empty
// state an Enter left after a done.
func TestARegistrationAfreshAfterAnEnterKeepsTurns(t *testing.T) {
	srv, hs := startServer(t)
	restarted, hs2 := startServer(t)
	a, local, conn, registered, serve := hostedBy(t, hs.URL)
	served := serve(conn)
	d, ok := srv.Registry().Get(registered.SessionID)
	if !ok {
		t.Fatal("the hosted session is not listed")
	}
	local.SetAttentionFull(session.AttentionDone, "All tests pass.", session.SourceAPI, session.KindDone, nil)
	untilListed(t, d, "the done told", func(i session.Info) bool { return i.Attention.State == session.AttentionDone && i.Turns })
	enter(t, local)
	untilListed(t, d, "the Enter told", func(i session.Info) bool {
		return i.Attention.State == session.AttentionNone && i.Attention.Source == session.SourceInput
	})

	// The next connection reaches a server that never knew the session: it
	// refuses the resume, and the host registers afresh.
	conn.CloseNow()
	<-served
	wsURL, err := controlURL(hs2.URL)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.wsURL = wsURL
	a.mu.Unlock()
	if _, _, err := a.dialAndRegister(t.Context()); err == nil || !strings.Contains(err.Error(), "registering afresh") {
		t.Fatalf("the resume: %v", err)
	}
	conn2, again, err := a.dialAndRegister(t.Context())
	if err != nil || again.Resumed {
		t.Fatalf("registered afresh: %v %+v", err, again)
	}
	t.Cleanup(func() { conn2.CloseNow() })
	serve(conn2)
	d2, ok := restarted.Registry().Get(again.SessionID)
	if !ok {
		t.Fatal("the new copy is not listed")
	}
	untilListed(t, d2, "the new copy", func(i session.Info) bool {
		return i.Attention.State == session.AttentionNone && i.Attention.Source == session.SourceInput && i.Turns
	})
}

// A host that finished another turn while it was away (an Enter, a
// working, a done with other words) tells it as its connection comes
// back: the copy shows that done, its id and words, in place of the one it
// kept.
func TestADoneOfAnotherTurnWhileAwayReplacesTheShownDone(t *testing.T) {
	srv, hs := startServer(t)
	a, local, conn, registered, serve := hostedBy(t, hs.URL)
	served := serve(conn)
	d, ok := srv.Registry().Get(registered.SessionID)
	if !ok {
		t.Fatal("the hosted session is not listed")
	}
	local.SetAttentionFull(session.AttentionDone, "Refactored the parser.", session.SourceAPI, session.KindDone, nil)
	doneA := local.Info().Attention
	untilListed(t, d, "done A", func(i session.Info) bool { return i.Attention.ID == doneA.ID })

	conn.CloseNow()
	<-served
	untilListed(t, d, "the host gone", func(i session.Info) bool { return i.Status == session.StatusHostDisconnected })
	enter(t, local)
	local.SetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil)
	local.SetAttentionFull(session.AttentionDone, "Docs updated.", session.SourceAPI, session.KindDone, nil)
	doneB := local.Info().Attention
	if doneB.ID == doneA.ID || doneB.Message != "Docs updated." {
		t.Fatalf("the host's session shows %+v", doneB)
	}
	if att := d.Info().Attention; att.ID != doneA.ID {
		t.Fatalf("the server lists %+v with the host away", att)
	}

	conn2, again, err := a.dialAndRegister(t.Context())
	if err != nil || !again.Resumed {
		t.Fatalf("resume: %v %+v", err, again)
	}
	t.Cleanup(func() { conn2.CloseNow() })
	serve(conn2)
	untilListed(t, d, "done B", func(i session.Info) bool {
		return i.Status == session.StatusRunning && i.Attention.ID == doneB.ID && i.Attention.Message == "Docs updated."
	})
}
