package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/nvim"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// wsClient is a minimal viewer used by the end-to-end tests.
type wsClient struct {
	t    *testing.T
	c    *websocket.Conn
	sent atomic.Int64 // the frames send has written, for a test that waits until the server has read them
}

func dialViewer(t *testing.T, e *testEnv, sessionID, token string) *wsClient {
	t.Helper()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/sessions/" + sessionID + "?token=" + token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(proto.MaxFrame)
	t.Cleanup(func() { c.CloseNow() })
	return &wsClient{t: t, c: c}
}

func (w *wsClient) send(frame []byte) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		w.t.Fatalf("write: %v", err)
	}
	w.sent.Add(1)
}

func (w *wsClient) hello(cols, rows uint16) {
	w.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: cols, Rows: rows, Client: "test"}))
}

func (w *wsClient) read() (proto.Frame, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := w.c.Read(ctx)
	if err != nil {
		return proto.Frame{}, err
	}
	return proto.Decode(data)
}

// expectControl reads frames until a control message with the given t arrives.
func (w *wsClient) expectControl(t string) map[string]any {
	w.t.Helper()
	for {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for %s: %v", t, err)
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		if m["t"] == t {
			return m
		}
	}
}

// expectOutput reads until the accumulated OUTPUT/SCROLLBACK contains needle.
func (w *wsClient) expectOutput(needle string) {
	w.t.Helper()
	var acc []byte
	for !bytes.Contains(acc, []byte(needle)) {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for output %q: %v (have %q)", needle, err, acc)
		}
		if f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback {
			acc = append(acc, f.Payload...)
		}
	}
}

func (w *wsClient) expectClose(code websocket.StatusCode) {
	w.t.Helper()
	for {
		_, err := w.read()
		if err == nil {
			continue
		}
		if got := websocket.CloseStatus(err); got != code {
			w.t.Fatalf("close status %d, want %d (%v)", got, code, err)
		}
		return
	}
}

func TestViewerWebSocketRoundTrip(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")

	ctl := dialViewer(t, e, id, adminToken)
	ctl.hello(100, 30)
	welcome := ctl.expectControl(proto.CtlWelcome)
	if welcome["role"] != "control" || welcome["transport"] != "ws" || welcome["cols"] != float64(100) {
		t.Fatalf("welcome %v", welcome)
	}
	ctl.expectControl(proto.CtlReady)
	ctl.send(proto.Encode(proto.TypeInput, []byte("ping\n")))
	ctl.expectOutput("ping")

	// A view link joins late and receives the scrollback.
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	viewToken := lo["token"].(string)
	linkID := lo["link"].(map[string]any)["id"].(string)
	viewer := dialViewer(t, e, id, viewToken)
	viewer.hello(80, 24)
	w2 := viewer.expectControl(proto.CtlWelcome)
	if w2["role"] != "view" || w2["cols"] != float64(100) {
		t.Fatalf("viewer welcome %v", w2)
	}
	viewer.expectOutput("ping")
	viewer.expectControl(proto.CtlReady)
	viewer.send(proto.Encode(proto.TypeInput, []byte("x")))
	if m := viewer.expectControl(proto.CtlError); m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("expected read_only, got %v", m)
	}
	// controller resize is broadcast to the viewer
	ctl.send(proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: 120, Rows: 40}))
	if m := viewer.expectControl(proto.CtlResize); m["cols"] != float64(120) {
		t.Fatalf("resize broadcast %v", m)
	}
	// ping/pong
	ctl.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 42}))
	if m := ctl.expectControl(proto.CtlPong); m["ts"] != float64(42) {
		t.Fatalf("pong %v", m)
	}
	// revoke disconnects the viewer with 4403
	resp, _ := e.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke %d", resp.StatusCode)
	}
	viewer.expectClose(proto.CloseForbidden)

	// stopping the session notifies the controller
	e.do("DELETE", "/api/sessions/"+id, adminToken, nil)
	if m := ctl.expectControl(proto.CtlStatus); m["status"] != "stopped" {
		t.Fatalf("status %v", m)
	}
}

// One viewer sizes the session (round 14): the workbench's window, which
// came with no link, holds the size; a control link opened on a laptop
// follows it, its window's resize refused without a word, and takes it only
// with Fit to my window (take), which every viewer then hears.
func TestAControlLinkTakesTheSizeOnlyByAsking(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	ctl := dialViewer(t, e, id, adminToken)
	ctl.hello(100, 30)
	w := ctl.expectControl(proto.CtlWelcome)
	if w["sizer"] != true || w["sizedBy"] != w["subscriberId"] || w["cols"] != float64(100) {
		t.Fatalf("the workbench's welcome %v", w)
	}
	ctl.expectControl(proto.CtlReady)
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	laptop := dialViewer(t, e, id, lo["token"].(string))
	laptop.hello(80, 24)
	lw := laptop.expectControl(proto.CtlWelcome)
	if lw["role"] != "control" || lw["sizedBy"] != w["subscriberId"] || lw["cols"] != float64(100) {
		t.Fatalf("the laptop's welcome %v", lw)
	}
	laptop.expectControl(proto.CtlReady)
	size := func() (float64, float64) {
		t.Helper()
		_, out := e.do("GET", "/api/sessions/"+id, adminToken, nil)
		info := out["session"].(map[string]any)
		return info["cols"].(float64), info["rows"].(float64)
	}
	// Its window's resize: no answer, no change.
	laptop.send(proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: 80, Rows: 24}))
	laptop.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 1}))
	laptop.expectControl(proto.CtlPong)
	if c, r := size(); c != 100 || r != 30 {
		t.Fatalf("a link's resize moved the size to %vx%v", c, r)
	}
	// Fit to my window: the size is the laptop's, and the workbench hears who sizes it.
	laptop.send(proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: 80, Rows: 24, Take: true}))
	if m := ctl.expectControl(proto.CtlResize); m["cols"] != float64(80) || m["by"] != lw["subscriberId"] {
		t.Fatalf("the take's resize %v", m)
	}
	if c, r := size(); c != 80 || r != 24 {
		t.Fatalf("after the take %vx%v", c, r)
	}
	// The workbench's window changing size no longer moves it; its own take does.
	ctl.send(proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: 110, Rows: 30}))
	ctl.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 2}))
	ctl.expectControl(proto.CtlPong)
	if c, _ := size(); c != 80 {
		t.Fatalf("the workbench's passive resize moved it to %v", c)
	}
	ctl.send(proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: 110, Rows: 30, Take: true}))
	if m := laptop.expectControl(proto.CtlResize); m["by"] != lw["subscriberId"] {
		t.Fatalf("the laptop's own take, heard first %v", m)
	}
	if m := laptop.expectControl(proto.CtlResize); m["cols"] != float64(110) || m["by"] != w["subscriberId"] {
		t.Fatalf("taken back %v", m)
	}
}

func TestViewerWebSocketAuthFailures(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	bad := dialViewer(t, e, id, "nope")
	bad.expectClose(proto.CloseUnauthorized)
	missing := dialViewer(t, e, "0000000000000000", adminToken)
	missing.expectClose(proto.CloseNotFound)
	// hello timeout / wrong first frame
	noHello := dialViewer(t, e, id, adminToken)
	noHello.send(proto.Encode(proto.TypeInput, []byte("x")))
	noHello.expectClose(proto.CloseProtocolError)
}

func TestViewerFileGet(t *testing.T) {
	e := newTestEnv(t, nil)
	os.WriteFile(filepath.Join(e.root, "a.go"), []byte("package a\n"), 0o600)
	big := bytes.Repeat([]byte("b"), 300<<10)
	os.WriteFile(filepath.Join(e.root, "big.txt"), big, 0o600)
	id := e.createSession("cat")
	c := dialViewer(t, e, id, adminToken)
	c.hello(80, 24)
	if w := c.expectControl(proto.CtlWelcome); w["fileView"] != true {
		t.Fatalf("fileView %v", w)
	}
	c.expectControl(proto.CtlReady)
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "r1", Path: "a.go"}))
	h, body := c.expectFile("r1")
	if h.Kind != "file" || string(body) != "package a\n" {
		t.Fatalf("file %+v %q", h, body)
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "r2", Path: "big.txt"}))
	h, body = c.expectFile("r2")
	if h.Kind != "file" || len(body) != len(big) {
		t.Fatalf("big %+v %d", h, len(body))
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "r3", Path: "../outside"}))
	if h, _ := c.expectFile("r3"); h.Kind != "error" || h.Error.Code != "denied" {
		t.Fatalf("escape %+v", h)
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "r4", Path: ".", Stat: true}))
	if h, _ := c.expectFile("r4"); h.Kind != "dir" || len(h.Entries) != 0 {
		t.Fatalf("stat dir %+v", h)
	}
	// Touched (round 13, G1): the session's own index of the files its agent
	// touched, over the same connection.
	if resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "file", "op": "edit", "path": "a.go", "tool": "Edit"}); resp.StatusCode >= 300 {
		t.Fatalf("event %d %v", resp.StatusCode, out)
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "r5", Op: proto.FileOpTouched}))
	if h, _ := c.expectFile("r5"); h.Kind != "touched" || len(h.Touched) != 1 || filepath.Base(h.Touched[0].Path) != "a.go" || h.Touched[0].Op != "edit" || h.Touched[0].Count != 1 {
		t.Fatalf("touched %+v", h)
	}
}

func (w *wsClient) expectFile(reqID string) (proto.FileHeader, []byte) {
	w.t.Helper()
	for {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for file %s: %v", reqID, err)
		}
		if f.Type != proto.TypeFile {
			continue
		}
		h, body, err := proto.DecodeFile(f.Payload)
		if err != nil {
			w.t.Fatal(err)
		}
		if h.ReqID == reqID {
			return h, body
		}
	}
}

func TestAttentionViaAgentTokenBellAndEvents(t *testing.T) {
	e := newTestEnv(t, nil)
	// A session that prints its own notify token, then behaves like cat.
	cat, _ := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{
		{ID: "env", Name: "env", Command: []string{"/bin/sh", "-c", "echo TOKEN=$CONDUCTOR_NOTIFY_TOKEN URL=$CONDUCTOR_NOTIFY_URL; exec /bin/cat"}},
	}})
	e.setCatalog(cat)
	id := e.createSession("env")

	// Events stream: subscribe before the changes.
	evReq, _ := http.NewRequest("GET", e.http.URL+"/api/events", nil)
	evReq.Header.Set("Authorization", "Bearer "+adminToken)
	evResp, err := e.client.Do(evReq)
	if err != nil {
		t.Fatal(err)
	}
	defer evResp.Body.Close()
	events := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(evResp.Body)
		var name string
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				events <- name + " " + strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	waitEvent := func(pred func(string) bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-events:
				if pred(ev) {
					return
				}
			case <-deadline:
				t.Fatal("event not observed")
			}
		}
	}
	waitEvent(func(ev string) bool { return strings.HasPrefix(ev, "snapshot ") && strings.Contains(ev, id) })

	c := dialViewer(t, e, id, adminToken)
	c.hello(80, 24)
	// The token line is usually already in the scrollback replay.
	var acc []byte
	for !bytes.Contains(acc, []byte("URL=")) || !bytes.Contains(acc, []byte("\n")) {
		f, err := c.read()
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback {
			acc = append(acc, f.Payload...)
		}
	}
	line := string(acc[bytes.Index(acc, []byte("TOKEN=")):])
	line = strings.TrimSpace(strings.SplitN(line, "\n", 2)[0])
	fields := strings.Fields(line)
	token := strings.TrimPrefix(fields[0], "TOKEN=")
	url := strings.TrimPrefix(fields[1], "URL=")
	if len(token) != 43 || !strings.HasSuffix(url, "/api/sessions/"+id+"/attention") {
		t.Fatalf("env line %q", line)
	}

	// Wrong token is rejected; the agent token works.
	resp, out := e.do("POST", "/api/sessions/"+id+"/attention", "nope", map[string]any{"state": "needs_input"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d %v", resp.StatusCode, out)
	}
	resp, out = e.do("POST", "/api/sessions/"+id+"/attention", token, map[string]any{"state": "needs_input", "message": "approve the plan"})
	if resp.StatusCode != 200 {
		t.Fatalf("agent token: %d %v", resp.StatusCode, out)
	}
	m := c.expectControl(proto.CtlAttention)
	if m["state"] != "needs_input" || m["message"] != "approve the plan" || m["source"] != "api" {
		t.Fatalf("attention frame %v", m)
	}
	qid, _ := m["id"].(string)
	waitEvent(func(ev string) bool {
		return strings.HasPrefix(ev, "session ") && strings.Contains(ev, `"state":"needs_input"`)
	})
	_, out = e.do("GET", "/api/sessions/"+id, adminToken, nil)
	if att := out["session"].(map[string]any)["attention"].(map[string]any); att["state"] != "needs_input" {
		t.Fatalf("listing attention %v", att)
	}
	resp, _ = e.do("POST", "/api/sessions/"+id+"/attention", token, map[string]any{"state": "bogus"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus state: %d", resp.StatusCode)
	}

	// A reply naming it clears it (keys alone answer no question the agent
	// reported): the clear frame, and the reply's word.
	c.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "k1", Question: qid, Text: "k"}))
	for cleared, taken := false, false; !cleared || !taken; {
		f, err := c.read()
		if err != nil {
			t.Fatalf("waiting for the reply's clear and word: %v", err)
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		switch m["t"] {
		case proto.CtlAttention:
			if m["state"] != "" || m["source"] != "input" {
				t.Fatalf("clear frame %v", m)
			}
			cleared = true
		case proto.CtlAnswerOK, proto.CtlError:
			if m["t"] != proto.CtlAnswerOK || m["requestId"] != "k1" {
				t.Fatalf("the reply's word %v", m)
			}
			taken = true
		}
	}
	// A bell in the output sets it again with source bell.
	c.send(proto.Encode(proto.TypeInput, []byte("\a\n")))
	if m := c.expectControl(proto.CtlAttention); m["state"] != "needs_input" || m["source"] != "bell" {
		t.Fatalf("bell frame %v", m)
	}
	// Admin can clear.
	resp, _ = e.do("POST", "/api/sessions/"+id+"/attention", adminToken, map[string]any{"state": "clear"})
	if resp.StatusCode != 200 {
		t.Fatalf("admin clear: %d", resp.StatusCode)
	}
	if m := c.expectControl(proto.CtlAttention); m["state"] != "" || m["source"] != "admin" {
		t.Fatalf("admin clear frame %v", m)
	}
	// Events without the admin token, or with it only in the query, are refused.
	resp, _ = e.do("GET", "/api/events", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events auth: %d", resp.StatusCode)
	}
	resp, _ = e.do("GET", "/api/events?token="+adminToken, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events query token must be refused: %d", resp.StatusCode)
	}
}

func (w *wsClient) helloNamed(cols, rows uint16, name string) {
	w.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: cols, Rows: rows, Client: "test", Name: name}))
}

func TestViewerRosterCarriesNamesAndLinkLabels(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")

	owner := dialViewer(t, e, id, adminToken)
	owner.helloNamed(80, 24, "  Jordan\x07 ")
	owner.expectControl(proto.CtlReady)

	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control", "label": "pairing"})
	guest := dialViewer(t, e, id, lo["token"].(string))
	guest.helloNamed(80, 24, "Priya Shah")
	guest.expectControl(proto.CtlReady)

	// The owner receives the roster broadcast when the guest joins.
	var roster map[string]any
	for i := 0; i < 5; i++ {
		roster = owner.expectControl(proto.CtlViewers)
		if roster["count"] == float64(2) {
			break
		}
	}
	list, _ := roster["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("roster %v", roster)
	}
	names := map[string]map[string]any{}
	for _, v := range list {
		m := v.(map[string]any)
		names[m["name"].(string)] = m
	}
	if names["Jordan"] == nil || names["Priya Shah"] == nil {
		t.Fatalf("names %v", names)
	}
	if names["Priya Shah"]["link"] != "pairing" || names["Priya Shah"]["role"] != "control" {
		t.Fatalf("guest entry %v", names["Priya Shah"])
	}
	if names["Jordan"]["link"] != nil {
		t.Fatalf("owner should carry no link label %v", names["Jordan"])
	}

	// An absurdly long name is a protocol error, not a crash.
	long := dialViewer(t, e, id, adminToken)
	long.helloNamed(80, 24, strings.Repeat("n", 1000))
	long.expectClose(proto.CloseProtocolError)
}

// fakeHost is a `conductor host` reduced to its control connection: it
// registers a session and then sends and reads the JSON messages by hand, so
// a test decides exactly what the server hears and checks exactly what it
// says back. A goroutine reads the connection for as long as the test runs, so
// a test can wait for a while for nothing more to come without giving up the
// connection (a read that times out closes it).
type fakeHost struct {
	t         *testing.T
	c         *websocket.Conn
	sessionID string
	msgs      chan map[string]any // the server's text messages, in order
	bin       chan []byte         // the server's binary messages (relay envelopes), in order
	done      chan struct{}       // closed when the connection has ended
	err       error               // why, once done is closed
	links     int                 // link requests made, for their ids
}

func dialFakeHost(t *testing.T, e *testEnv, agentToken string) *fakeHost {
	t.Helper()
	return dialFakeHostAuth(t, e, agentToken, "Bearer test-host-token")
}

// dialFakeHostAuth registers a fake host with the given Authorization value;
// "" sends none (an open host).
func dialFakeHostAuth(t *testing.T, e *testEnv, agentToken, auth string) *fakeHost {
	t.Helper()
	h, _ := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop"}, proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24, AgentToken: agentToken}, auth)
	return h
}

// dialFakeHostWith registers a fake host saying host and sess, with the
// given Authorization value ("" sends none), and returns it with the
// server's registered answer.
func dialFakeHostWith(t *testing.T, e *testEnv, host proto.HostInfo, sess proto.HostSession, auth string) (*fakeHost, map[string]any) {
	t.Helper()
	return dialFakeHostPinged(t, e, host, sess, auth, nil)
}

// dialFakeHostPinged is dialFakeHostWith whose connection calls onPing, when
// set, as each of the server's pings arrives (and answers it as ever).
func dialFakeHostPinged(t *testing.T, e *testEnv, host proto.HostInfo, sess proto.HostSession, auth string, onPing func()) (*fakeHost, map[string]any) {
	t.Helper()
	return dialFakeHostRegister(t, e, proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: host, Session: sess}, auth, onPing)
}

// dialFakeHostRegister registers a fake host with reg (a resume among
// them), as dialFakeHostPinged does.
func dialFakeHostRegister(t *testing.T, e *testEnv, reg proto.Register, auth string, onPing func()) (*fakeHost, map[string]any) {
	t.Helper()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	if auth != "" {
		header.Set("Authorization", auth)
	}
	opts := &websocket.DialOptions{HTTPHeader: header}
	if onPing != nil {
		opts.OnPingReceived = func(context.Context, []byte) bool {
			onPing()
			return true
		}
	}
	c, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	c.SetReadLimit(proto.MaxHostMessage + proto.MaxFrame)
	t.Cleanup(func() { c.CloseNow() })
	h := &fakeHost{t: t, c: c, msgs: make(chan map[string]any, 4096), bin: make(chan []byte, 4096), done: make(chan struct{})}
	go h.pump()
	h.send(reg)
	registered := h.expect(proto.HostRegistered)
	h.sessionID, _ = registered["sessionId"].(string)
	if h.sessionID == "" {
		t.Fatal("the server registered no session")
	}
	return h, registered
}

// pump moves the server's text messages to msgs and its binary ones to bin
// until the connection ends.
func (h *fakeHost) pump() {
	defer close(h.done)
	for {
		typ, data, err := h.c.Read(h.t.Context())
		if err != nil {
			h.err = err
			return
		}
		if typ != websocket.MessageText {
			select {
			case h.bin <- append([]byte(nil), data...):
			case <-h.t.Context().Done():
				return
			}
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			h.err = fmt.Errorf("unreadable message %q: %w", data, err)
			return
		}
		select {
		case h.msgs <- m:
		case <-h.t.Context().Done():
			return
		}
	}
}

func (h *fakeHost) send(v any) {
	h.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(h.t.Context(), 5*time.Second)
	defer cancel()
	if err := h.c.Write(ctx, websocket.MessageText, b); err != nil {
		h.t.Fatalf("host write: %v", err)
	}
}

// expect returns the next message from the server with the given t, skipping
// the others.
func (h *fakeHost) expect(t string) map[string]any {
	h.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m := <-h.msgs:
			if m["t"] == t {
				return m
			}
		case <-h.done:
			// Whatever arrived before the end is still queued.
			for {
				select {
				case m := <-h.msgs:
					if m["t"] == t {
						return m
					}
				default:
					h.t.Fatalf("host waiting for %s: the connection ended: %v", t, h.err)
				}
			}
		case <-timeout:
			h.t.Fatalf("host waiting for %s: nothing came", t)
		}
	}
}

// link has the server mint a share link to the host's session over the
// host's own connection (link, link_created), as a publishing machine has
// its links minted, and returns the link's token: on a switchyard, the only
// way to a link.
func (h *fakeHost) link(role, label string) string {
	h.t.Helper()
	h.links++
	h.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: fmt.Sprintf("test-%d", h.links), Role: role, Label: label})
	m := h.expect(proto.HostLinkCreated)
	u, _ := m["url"].(string)
	_, token, ok := strings.Cut(u, "/join/")
	if !ok || token == "" {
		h.t.Fatalf("link_created %v", m)
	}
	return token
}

// drain returns what the server sends until it has been quiet for idle.
func (h *fakeHost) drain(idle time.Duration) []map[string]any {
	var got []map[string]any
	for {
		select {
		case m := <-h.msgs:
			got = append(got, m)
		case <-h.done:
			return got
		case <-time.After(idle):
			return got
		}
	}
}

// ofType keeps the messages with the given t.
func ofType(msgs []map[string]any, t string) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		if m["t"] == t {
			out = append(out, m)
		}
	}
	return out
}

func hostActivity(typ string, mutate func(*proto.Activity)) proto.HostActivityMsg {
	a := proto.Activity{T: proto.CtlActivity, At: time.Now().UTC().Format(time.RFC3339Nano), Type: typ}
	if mutate != nil {
		mutate(&a)
	}
	return proto.HostActivityMsg{T: proto.HostActivity, Entry: a}
}

// quiet fails if an event whose text starts with prefix arrives within d.
func quiet(t *testing.T, events <-chan string, d time.Duration, prefix string) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev := <-events:
			if strings.HasPrefix(ev, prefix) {
				t.Fatalf("unexpected %.200s", ev)
			}
		case <-deadline:
			return
		}
	}
}

func TestHostActivityReachesTheEventStream(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	events := e.sse(t)

	// The entry is the host's; the session id it names is not believed.
	msg := hostActivity(session.ActivityToolUse, func(a *proto.Activity) {
		a.By, a.ByName, a.Message, a.Tool = "0123456789abcdef", "Ada", "ran\x00 it", "Bash"
	})
	msg.SessionID = "someoneelse00000"
	host.send(msg)
	got := activityPayload(t, e.waitEvent(t, events, isActivity("tool_use", host.sessionID)))
	if got["sessionId"] != host.sessionID || got["by"] != "0123456789abcdef" || got["byName"] != "Ada" || got["message"] != "ran it" || got["tool"] != "Bash" {
		t.Fatalf("entry %v", got)
	}

	// What the server cannot use is dropped without costing the host its
	// connection: an entry of a type nobody has, a message from a newer host,
	// and an entry far too big.
	host.send(hostActivity("bogus", func(a *proto.Activity) { a.Message = "bogus entry" }))
	host.send(map[string]any{"t": "from_the_future", "n": 1})
	host.send(hostActivity(session.ActivityError, func(a *proto.Activity) {
		a.Message, a.URL, a.Tool, a.ByName = strings.Repeat("m", 8000), "https://x/"+strings.Repeat("u", 8000), strings.Repeat("k", 500), strings.Repeat("n", 500)
	}))
	host.send(hostActivity(session.ActivityProgress, func(a *proto.Activity) { a.Message = "marker" }))
	sawBogus := false
	var big map[string]any
	e.waitEvent(t, events, func(ev string) bool {
		if strings.Contains(ev, "bogus") {
			sawBogus = true
		}
		if isActivity(session.ActivityError, host.sessionID)(ev) {
			big = activityPayload(t, ev)
		}
		return strings.Contains(ev, `"message":"marker"`)
	})
	if sawBogus {
		t.Fatal("an entry of an unknown type was streamed")
	}
	if big == nil {
		t.Fatal("the big entry was not streamed")
	}
	if len(big["message"].(string)) != session.MaxAttentionMessage || len(big["url"].(string)) > session.MaxEventURL || len(big["tool"].(string)) != session.MaxEventTool || len([]rune(big["byName"].(string))) != proto.MaxNameLen {
		t.Fatalf("the big entry was not cut: message %d, url %d, tool %d, byName %d", len(big["message"].(string)), len(big["url"].(string)), len(big["tool"].(string)), len(big["byName"].(string)))
	}
}

// A hosted session's attention entry carries the state its host sends with it.
// A host's question reaches the server's listings (what the sidebar and the
// Yard read) with the id the host gave it, so that an answer from them names
// the question the host holds; a report the server forwards to the host
// shows none there until the host tells its own.
func TestAHostedQuestionIsListedWithItsHostsID(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	host.send(proto.HostAttentionMsg{T: proto.HostAttention, SessionID: host.sessionID, State: "needs_input", Message: "Allow Bash?", Source: session.SourceQuestion, Kind: session.KindPermission, Options: []proto.AttentionOption{{Label: "Yes", Input: "y"}}, ID: "0123456789abcdef"})
	listed := func() map[string]any {
		_, out := e.do("GET", "/api/sessions/"+host.sessionID, adminToken, nil)
		s, _ := out["session"].(map[string]any)
		a, _ := s["attention"].(map[string]any)
		return a
	}
	deadline := time.Now().Add(3 * time.Second)
	for listed()["id"] != "0123456789abcdef" {
		if time.Now().After(deadline) {
			t.Fatalf("listed %v", listed())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp, out := e.do("POST", "/api/sessions/"+host.sessionID+"/attention", adminToken, map[string]any{"state": "needs_input", "message": "Which port?"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("report: %d %v", resp.StatusCode, out)
	}
	host.expect(proto.HostAttention)
	if a := listed(); a["id"] != nil || a["message"] != "Which port?" {
		t.Fatalf("a forwarded report listed %v before the host told its id", a)
	}
	host.send(proto.HostAttentionMsg{T: proto.HostAttention, SessionID: host.sessionID, State: "needs_input", Message: "Which port?", Source: session.SourceAdmin, ID: "fedcba9876543210"})
	deadline = time.Now().Add(3 * time.Second)
	for listed()["id"] != "fedcba9876543210" {
		if time.Now().After(deadline) {
			t.Fatalf("listed %v", listed())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHostedAttentionEntriesCarryTheirStateOnTheEventStream(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	events := e.sse(t)
	msg := hostActivity(session.ActivityAttention, func(a *proto.Activity) { a.Message = "waiting" })
	msg.State = "needs_input"
	host.send(msg)
	ev := e.waitEvent(t, events, isActivity("attention", host.sessionID))
	if m := activityPayload(t, ev); m["state"] != "needs_input" {
		t.Fatalf("activity %v", m)
	}
}

// A message that cannot be read is the host's mistake, as for every other
// host message, and ends the connection with a protocol error.
func TestHostActivityThatIsNotAnEntryClosesTheConnection(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	host.send(map[string]any{"t": proto.HostActivity, "entry": "not an entry"})
	select {
	case <-host.done:
		if websocket.CloseStatus(host.err) != proto.CloseProtocolError {
			t.Fatalf("closed with %v, want %d", host.err, proto.CloseProtocolError)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed open")
	}
}

func TestEventsRouteForwardsAHostedSessionsEventsToItsHost(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	events := e.sse(t)
	path := "/api/sessions/" + host.sessionID + "/events"

	resp, out := e.do("POST", path, "hosted-agent-token", map[string]any{
		"type": "artifact", "message": "PR opened\x00", "url": "https://github.com/x/y/pull/1", "to": "review",
	})
	if resp.StatusCode != http.StatusAccepted || out["accepted"] != true {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	m := host.expect(proto.HostActivity)
	entry, _ := m["entry"].(map[string]any)
	if _, named := m["sessionId"]; named {
		t.Fatalf("the server named the session: %v", m)
	}
	if entry["t"] != proto.CtlActivity || entry["type"] != "artifact" || entry["byName"] != "agent" || entry["message"] != "PR opened" ||
		entry["url"] != "https://github.com/x/y/pull/1" || entry["to"] != "review" || entry["at"] != "" {
		t.Fatalf("the host was sent %v", m)
	}

	// The server has no session of its own to record in: nothing streams
	// until the host has recorded the entry and reports it.
	quiet(t, events, 200*time.Millisecond, "activity ")
	entry["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	host.send(map[string]any{"t": proto.HostActivity, "sessionId": host.sessionID, "entry": entry})
	got := activityPayload(t, e.waitEvent(t, events, isActivity("artifact", host.sessionID)))
	if got["url"] != "https://github.com/x/y/pull/1" || got["byName"] != "agent" {
		t.Fatalf("streamed %v", got)
	}

	// What the host reports is not sent back to it, or the two would echo
	// each other for ever: the next thing the host hears is the next event.
	if resp, out := e.do("POST", path, "hosted-agent-token", map[string]any{"type": "progress", "message": "marker"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if next, _ := host.expect(proto.HostActivity)["entry"].(map[string]any); next["message"] != "marker" {
		t.Fatalf("the host was sent %v before the marker", next)
	}
}

// Attention types are applied to a hosted session as /attention applies
// them: recorded on the server and told to the host.
func TestEventsRouteAttentionTypesReachAHostLikeTheAttentionRoute(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	d, _ := e.srv.registry.Get(host.sessionID)
	options := []map[string]any{{"label": "Yes", "input": "1"}}

	resp, out := e.do("POST", "/api/sessions/"+host.sessionID+"/events", "hosted-agent-token", map[string]any{"type": "needs_input", "message": "Allow Bash?", "kind": "permission", "options": options})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	m := host.expect(proto.HostAttention)
	if m["state"] != "needs_input" || m["message"] != "Allow Bash?" || m["kind"] != "permission" || m["source"] != session.SourceAPI || len(m["options"].([]any)) != 1 {
		t.Fatalf("the host was told %v", m)
	}
	if att := d.Info().Attention; att.State != session.AttentionNeedsInput || att.Kind != "permission" {
		t.Fatalf("server attention %+v", att)
	}

	resp, out = e.do("POST", "/api/sessions/"+host.sessionID+"/attention", "hosted-agent-token", map[string]any{"state": "working"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if m := host.expect(proto.HostAttention); m["state"] != "working" || m["source"] != session.SourceAPI {
		t.Fatalf("the host was told %v", m)
	}
}

// A report about a hosted session whose host is away has nowhere to be
// recorded; the caller is told, not given a 202.
func TestEventsRouteTellsWhenTheHostIsAway(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	d, _ := e.srv.registry.Get(host.sessionID)
	host.c.CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for d.Info().Status != session.StatusHostDisconnected {
		if time.Now().After(deadline) {
			t.Fatalf("status %s", d.Info().Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	path := "/api/sessions/" + host.sessionID + "/events"
	resp, out := e.do("POST", path, "hosted-agent-token", map[string]any{"type": "progress", "message": "1/7"})
	if resp.StatusCode != http.StatusConflict || errorCode(out) != "host_disconnected" {
		t.Fatalf("event for a hostless session: %d %v", resp.StatusCode, out)
	}
	// Attention is held on the server meanwhile, as /attention holds it.
	if resp, out := e.do("POST", path, "hosted-agent-token", map[string]any{"type": "needs_input", "message": "?"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("attention for a hostless session: %d %v", resp.StatusCode, out)
	}
	if d.Info().Attention.State != session.AttentionNeedsInput {
		t.Fatalf("attention %+v", d.Info().Attention)
	}
	// And they are metered as for a connected host: past the burst, 429.
	limited := false
	for i := 0; i < 5*session.EventBurst && !limited; i++ {
		resp, out := e.do("POST", "/api/sessions/"+host.sessionID+"/attention", "hosted-agent-token", map[string]any{"state": "working", "message": fmt.Sprint("n", i)})
		switch {
		case resp.StatusCode == http.StatusTooManyRequests && errorCode(out) == "rate_limited":
			limited = true
		case resp.StatusCode != http.StatusOK:
			t.Fatalf("word %d: %d %v", i, resp.StatusCode, out)
		}
	}
	if !limited {
		t.Fatal("attention words for a session without a host were never limited")
	}
}

// Every event the route accepts for a hosted session is a message down the
// one connection that also carries the input of the session's viewers, and a
// full queue closes it. So the server keeps a bucket for the session, the size
// of a server session's own, and refuses the rest with a 429 instead of
// forwarding it.
func TestEventsRouteLimitsWhatItForwardsToAHost(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	path := "/api/sessions/" + host.sessionID + "/events"

	accepted := 0
	var refused map[string]any
	for i := 0; i < 5*session.EventBurst; i++ {
		resp, out := e.do("POST", path, "hosted-agent-token", map[string]any{"type": "tool_use", "tool": "Bash"})
		if resp.StatusCode == http.StatusTooManyRequests {
			refused = out
			break
		}
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("event %d: %d %v", i, resp.StatusCode, out)
		}
		accepted++
	}
	if errorCode(refused) != "rate_limited" {
		t.Fatalf("%d events in a row were never refused as rate_limited: %v", accepted, refused)
	}
	// A burst at once, plus what the bucket earned while the requests were made.
	if accepted < session.EventBurst || accepted > session.EventBurst+session.EventRatePerSecond {
		t.Fatalf("accepted %d events before the first 429, want about %d", accepted, session.EventBurst)
	}

	// The host got what was accepted and nothing else: a refused event is
	// not forwarded.
	got := ofType(host.drain(300*time.Millisecond), proto.HostActivity)
	if len(got) != accepted {
		t.Fatalf("the host was sent %d activity messages for %d accepted events", len(got), accepted)
	}
}

// The attention route, and the attention words of the events route, are more
// ways to the same connection and get the same treatment. A word the server
// refuses changes nothing on the server either.
func TestAttentionWordsAreLimitedOnBothRoutesForAHostedSession(t *testing.T) {
	routes := []struct {
		name, suffix, field string
		ok                  int
	}{
		{"attention route", "/attention", "state", http.StatusOK},
		{"events route", "/events", "type", http.StatusAccepted},
	}
	for _, r := range routes {
		t.Run(r.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			host := dialFakeHost(t, e, "hosted-agent-token")
			d, _ := e.srv.registry.Get(host.sessionID)
			path := "/api/sessions/" + host.sessionID + r.suffix
			states := []string{"working", "done"}

			accepted := 0
			var refused map[string]any
			for i := 0; i < 5*session.EventBurst; i++ {
				resp, out := e.do("POST", path, "hosted-agent-token", map[string]any{r.field: states[i%2], "message": fmt.Sprint("n", i)})
				if resp.StatusCode == http.StatusTooManyRequests {
					refused = out
					break
				}
				if resp.StatusCode != r.ok {
					t.Fatalf("word %d: %d %v", i, resp.StatusCode, out)
				}
				accepted++
			}
			if errorCode(refused) != "rate_limited" {
				t.Fatalf("%d attention words in a row were never refused as rate_limited: %v", accepted, refused)
			}
			if accepted < session.EventBurst || accepted > session.EventBurst+session.EventRatePerSecond {
				t.Fatalf("accepted %d attention words before the first 429, want about %d", accepted, session.EventBurst)
			}
			if att := d.Info().Attention; att.Message != fmt.Sprint("n", accepted-1) {
				t.Fatalf("attention %+v: the refused word must not be applied", att)
			}
			if got := ofType(host.drain(300*time.Millisecond), proto.HostAttention); len(got) != accepted {
				t.Fatalf("the host was told %d times for %d accepted words", len(got), accepted)
			}
		})
	}
}

// One budget for a hosted session, whichever route an agent uses: a hook that
// alternates between the two cannot double it.
func TestEventsAndAttentionRoutesShareAHostedSessionsBudget(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	events := "/api/sessions/" + host.sessionID + "/events"
	attention := "/api/sessions/" + host.sessionID + "/attention"

	for i := 0; i < session.EventBurst; i++ {
		if resp, out := e.do("POST", events, "hosted-agent-token", map[string]any{"type": "progress", "message": fmt.Sprint("n", i)}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("event %d: %d %v", i, resp.StatusCode, out)
		}
	}
	// The burst is spent. Separate budgets would allow another burst of words.
	words := 0
	var refused map[string]any
	for i := 0; i < 3*session.EventBurst; i++ {
		resp, out := e.do("POST", attention, "hosted-agent-token", map[string]any{"state": "working", "message": fmt.Sprint("w", i)})
		if resp.StatusCode == http.StatusTooManyRequests {
			refused = out
			break
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("word %d: %d %v", i, resp.StatusCode, out)
		}
		words++
	}
	if errorCode(refused) != "rate_limited" || words > session.EventBurst/2 {
		t.Fatalf("%d attention words followed a spent burst of events, refusal %v", words, refused)
	}
}

// A host that leaves sends its viewer one error frame, host_disconnected, then
// the close, and the frames it still owed come first. The host queues more
// output than the connection to a viewer that is not reading can take, so
// the viewer is still owed most of it when the host leaves; the viewer then
// reads everything, one frame at a time. Every frame the host sent arrives,
// its last one included, before the error frame. The quiet sub-case pins the
// pump's drain-then-close order alone: its reader sits in readFrame until the
// pump closes the sink and never learns on its own that the host is gone.
// The typing sub-case is the one where the reader learns it (the relay
// answers ErrHostGone) and must wait for the pump rather than close the
// connection under it.
func TestAHostThatLeavesSendsItsViewerOneError(t *testing.T) {
	// 1000 frames of 16 KiB, well past what the sockets between the server
	// and the viewer hold (a few MiB), and well within the viewer's queue.
	const frames = 1000
	filler := bytes.Repeat([]byte("o"), proto.MaxOutput)
	for _, typing := range []bool{false, true} {
		t.Run(fmt.Sprintf("typing=%v", typing), func(t *testing.T) {
			e := newTestEnv(t, nil)
			// The viewer the host's going closed is cut closeWait (5 s) on,
			// and the typing reader waits for the drain writeTimeout at most
			// and then closes under it, as for a viewer that reads slowly:
			// this one reads 16 MiB a frame at a time, which a loaded machine
			// takes longer than either to do. The test is of the order of
			// what it is written, not of those bounds.
			e.srv.viewerCloseWait = 2 * time.Minute
			e.srv.hostGoneWait = 2 * time.Minute
			host := dialFakeHost(t, e, "hosted-agent-token")
			events := e.sse(t)
			v := dialViewer(t, e, host.sessionID, adminToken)
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

			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			for i := 0; i <= frames; i++ {
				out := filler
				if i == frames {
					out = []byte("the last frame")
				}
				env, err := proto.EncodeRelay(viewerID, proto.Encode(proto.TypeOutput, out))
				if err != nil {
					t.Fatal(err)
				}
				if err := host.c.Write(ctx, websocket.MessageBinary, env); err != nil {
					t.Fatalf("relay frame %d: %v", i, err)
				}
			}
			// The server reads a host's messages in order: once it streams the
			// marker, every frame above is queued for the viewer.
			host.send(hostActivity(session.ActivityProgress, func(a *proto.Activity) { a.Message = "queued" }))
			e.waitEvent(t, events, func(ev string) bool {
				return isActivity(session.ActivityProgress, host.sessionID)(ev) && strings.Contains(ev, `"message":"queued"`)
			})
			host.c.CloseNow()

			outputs, errs := 0, 0
			last := false
			for {
				if typing {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					_ = v.c.Write(ctx, websocket.MessageBinary, proto.Encode(proto.TypeInput, []byte("x")))
					cancel()
				}
				f, err := v.read()
				if err != nil {
					if got := websocket.CloseStatus(err); got != proto.CloseSessionEnded {
						t.Fatalf("close status %d (%v), want %d", got, err, proto.CloseSessionEnded)
					}
					break
				}
				switch f.Type {
				case proto.TypeOutput:
					if errs > 0 {
						t.Fatal("an output frame came after the error frame")
					}
					outputs++
					last = string(f.Payload) == "the last frame"
				case proto.TypeControl:
					var m map[string]any
					json.Unmarshal(f.Payload, &m)
					if m["t"] == proto.CtlError {
						errs++
						if m["code"] != proto.ErrCodeHostDisconnected {
							t.Fatalf("error frame %v", m)
						}
					}
				}
			}
			if outputs != frames+1 || !last {
				t.Fatalf("%d of %d output frames before the close, the last one last: %v", outputs, frames+1, last)
			}
			if errs != 1 {
				t.Fatalf("%d error frames, want one", errs)
			}
		})
	}
}

// Over the WebSocket a hello of (0, 0) follows the session's size, as does a
// view link's hello of any size and an out-of-range pair; a controller's sets
// it, as before. The role comes from the token alone.
func TestAHelloOfZeroFollowsTheSessionsSize(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	ctl := dialViewer(t, e, id, adminToken)
	ctl.hello(148, 57)
	ctl.expectControl(proto.CtlWelcome)
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	for _, tc := range []struct {
		token      string
		cols, rows uint16
	}{
		{adminToken, 0, 0},
		{lo["token"].(string), 120, 40},
		{adminToken, 501, 40},
	} {
		c := dialViewer(t, e, id, tc.token)
		c.hello(tc.cols, tc.rows)
		if w := c.expectControl(proto.CtlWelcome); w["cols"] != float64(148) || w["rows"] != float64(57) {
			t.Fatalf("hello %dx%d: welcome %v", tc.cols, tc.rows, w)
		}
		if info := e.local(id).Info(); info.Cols != 148 || info.Rows != 57 {
			t.Fatalf("hello %dx%d resized the session to %dx%d", tc.cols, tc.rows, info.Cols, info.Rows)
		}
	}
}

// A submit message types its text and then Enter, apart, for a controller;
// a view link's is refused, and an over-long one is a bad frame.
func TestSubmitMessageTypesALine(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	ctl := dialViewer(t, e, id, adminToken)
	ctl.hello(0, 0)
	ctl.expectControl(proto.CtlReady)
	ctl.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "hello there"}))
	ctl.expectOutput("hello there")
	if e.local(id).Info().Attention.State != "" {
		t.Fatal("attention changed")
	}
	ctl.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: strings.Repeat("x", proto.MaxSubmit+1)}))
	if m := ctl.expectControl(proto.CtlError); m["code"] != proto.ErrCodeBadFrame {
		t.Fatalf("long: %v", m)
	}
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	v := dialViewer(t, e, id, lo["token"].(string))
	v.hello(0, 0)
	v.expectControl(proto.CtlReady)
	v.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "nope"}))
	if m := v.expectControl(proto.CtlError); m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("view: %v", m)
	}
}

// verdict waits for the owner's word on the answer reqID: answer_ok, or an
// error naming it.
func (w *wsClient) verdict(reqID string) map[string]any {
	w.t.Helper()
	for {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for the word on %s: %v", reqID, err)
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		if (m["t"] == proto.CtlAnswerOK || m["t"] == proto.CtlSubmitOK || m["t"] == proto.CtlError) && m["requestId"] == reqID {
			return m
		}
	}
}

// Two controllers answer one question together, each naming it: the
// server takes one (answer_ok naming it) and refuses the other
// (question_answered naming it), and the program reads the choice once.
// One that comes late is refused too; a view link may not answer; an
// answer past its bounds is a bad frame naming its request; a plain
// keystroke answers no question the agent reported: it is written and the
// question stands for a reply naming it.
func TestTwoAnswersToOneQuestionOverTheWire(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	l := e.local(id)
	a := dialViewer(t, e, id, adminToken)
	a.hello(0, 0)
	if w := a.expectControl(proto.CtlWelcome); w["answer"] != true {
		t.Fatalf("welcome %v", w)
	}
	a.expectControl(proto.CtlReady)
	b := dialViewer(t, e, id, adminToken)
	b.hello(0, 0)
	b.expectControl(proto.CtlReady)
	watch := dialViewer(t, e, id, adminToken)
	watch.hello(0, 0)
	watch.expectControl(proto.CtlReady)
	l.SetAttentionFull(session.AttentionNeedsInput, "Allow Bash?", session.SourceAPI, session.KindPermission, []session.Option{{Label: "Yes", Input: "1"}, {Label: "No", Input: "3"}})
	q := l.Info().Attention.ID
	if m := a.expectControl(proto.CtlAttention); m["id"] != q || q == "" {
		t.Fatalf("attention %v, want id %q", m, q)
	}
	answer := func(reqID, question, input string) []byte {
		return proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: reqID, Question: question, Input: input})
	}
	a.send(answer("a1", q, "1"))
	b.send(answer("b1", q, "1"))
	words := []map[string]any{a.verdict("a1"), b.verdict("b1")}
	var ok, refused int
	for _, m := range words {
		switch {
		case m["t"] == proto.CtlAnswerOK:
			ok++
		case m["code"] == proto.ErrCodeQuestionAnswered && (m["message"] == session.QuestionClaimedWords || strings.HasPrefix(m["message"].(string), "this question was already answered by ")):
			// Refused as the other's keys were being written (CLAIMED), or
			// once they had resolved it.
			refused++
		default:
			t.Fatalf("a word %v", m)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("%d taken, %d refused: %v", ok, refused, words)
	}
	if info := l.Info(); info.Attention.State != session.AttentionNone || info.LastAnswer == nil {
		t.Fatalf("not answered: %+v", info.Attention)
	}
	b.send(answer("b2", q, "1"))
	if m := b.verdict("b2"); m["code"] != proto.ErrCodeQuestionAnswered {
		t.Fatalf("a late answer: %v", m)
	}
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	v := dialViewer(t, e, id, lo["token"].(string))
	v.hello(0, 0)
	v.expectControl(proto.CtlReady)
	l.SetAttentionFull(session.AttentionNeedsInput, "Allow Edit?", session.SourceAPI, session.KindPermission, []session.Option{{Label: "Yes", Input: "1"}})
	next := l.Info().Attention.ID
	v.send(answer("v1", next, "1"))
	if m := v.verdict("v1"); m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("a view link's answer: %v", m)
	}
	a.send(answer("a2", strings.Repeat("q", proto.MaxQuestionID+1), "1"))
	if m := a.verdict("a2"); m["code"] != proto.ErrCodeBadFrame {
		t.Fatalf("an answer past its bounds: %v", m)
	}
	// An older page's click: the keys alone, written, answering nothing;
	// the question stands, and a reply naming it resolves it.
	a.send(proto.Encode(proto.TypeInput, []byte("2")))
	watch.outputUntil("12")
	if att := l.Info().Attention; att.State != session.AttentionNeedsInput || att.ID != next {
		t.Fatalf("a keystroke answered: %+v", att)
	}
	a.send(answer("a3", next, "1"))
	if m := a.verdict("a3"); m["t"] != proto.CtlAnswerOK {
		t.Fatalf("the reply naming it: %v", m)
	}
	if att := l.Info().Attention; att.State != session.AttentionNone {
		t.Fatalf("not answered: %+v", att)
	}
	// What cat read: one 1 of the two answers, then the 2, the 1 of the
	// reply, then the line.
	a.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "end"}))
	if out := watch.outputUntil("1end\r\n121end"); strings.Contains(out, "11") {
		t.Fatalf("the program read the choice twice: %q", out)
	}
}

// expectChat waits for the next chat message of the kind.
func (w *wsClient) expectChat(kind string) map[string]any {
	w.t.Helper()
	for {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for a chat %s: %v", kind, err)
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		if m["t"] == proto.CtlChat && m["kind"] == kind {
			return m
		}
	}
}

func TestWelcomeSaysChat(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	c := dialViewer(t, e, id, adminToken)
	c.hello(0, 0)
	if w := c.expectControl(proto.CtlWelcome); w["chat"] != true {
		t.Fatalf("welcome %v", w)
	}
}

// A post reaches every viewer of the session, the sender included, whatever
// the roles; a late viewer gets what was said as its history.
func TestChatReachesEveryViewer(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	owner := dialViewer(t, e, id, adminToken)
	owner.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Nate"}))
	owner.expectControl(proto.CtlReady)
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	guest := dialViewer(t, e, id, lo["token"].(string))
	guest.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Priya"}))
	guest.expectControl(proto.CtlReady)

	owner.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "hello from the owner", Nonce: "o1"}))
	m := guest.expectChat(proto.ChatKindMessage)
	by := m["by"].(map[string]any)
	if m["text"] != "hello from the owner" || m["nonce"] != "o1" || by["name"] != "Nate" || by["role"] != "control" || m["scope"] != "session" || m["id"] == "" {
		t.Fatalf("the guest got %v", m)
	}
	if own := owner.expectChat(proto.ChatKindMessage); own["id"] != m["id"] {
		t.Fatalf("the owner's own copy %v", own)
	}
	guest.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "hi from a link"}))
	if r := owner.expectChat(proto.ChatKindMessage); r["text"] != "hi from a link" || r["by"].(map[string]any)["role"] != "view" || r["by"].(map[string]any)["name"] != "Priya" {
		t.Fatalf("the owner got %v", r)
	}
	late := dialViewer(t, e, id, adminToken)
	late.hello(0, 0)
	h := late.expectControl(proto.CtlChatHistory)
	var texts []string
	for _, raw := range h["messages"].([]any) {
		if mm := raw.(map[string]any); mm["kind"] == proto.ChatKindMessage {
			texts = append(texts, mm["text"].(string))
		}
	}
	if strings.Join(texts, "|") != "hello from the owner|hi from a link" {
		t.Fatalf("history %v", h)
	}
}

// A controller's "to agent" post is typed into the session and marked in the
// chat; a view link can neither send to the agent nor type a kept message.
func TestChatToAgentTypesALineAndMarksIt(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	owner := dialViewer(t, e, id, adminToken)
	owner.hello(0, 0)
	owner.expectControl(proto.CtlReady)
	owner.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "typed into cat", To: proto.ChatToAgent}))
	msg := owner.expectChat(proto.ChatKindMessage)
	owner.expectOutput("typed into cat")
	if marker := owner.expectChat(proto.ChatKindSentToAgent); marker["ref"] != msg["id"] || marker["to"] != nil {
		t.Fatalf("marker %v for %v", marker, msg)
	}
	// Any kept message can be typed later, by a controller.
	owner.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "later", Nonce: "l"}))
	later := owner.expectChat(proto.ChatKindMessage)
	owner.send(proto.MustControl(proto.ChatSend{T: proto.CtlChatSend, Ref: later["id"].(string)}))
	owner.expectOutput("later")
	owner.expectChat(proto.ChatKindSentToAgent)
	owner.send(proto.MustControl(proto.ChatSend{T: proto.CtlChatSend, Ref: "nope"}))
	if m := owner.expectControl(proto.CtlError); m["code"] != proto.ErrCodeBadFrame || m["requestId"] != "nope" {
		t.Fatalf("unknown ref: %v", m)
	}

	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	v := dialViewer(t, e, id, lo["token"].(string))
	v.hello(0, 0)
	v.expectControl(proto.CtlReady)
	v.send(proto.MustControl(proto.ChatSend{T: proto.CtlChatSend, Ref: msg["id"].(string)}))
	if m := v.expectControl(proto.CtlError); m["code"] != proto.ErrCodeReadOnly || m["requestId"] != msg["id"] {
		t.Fatalf("view chat_send: %v", m)
	}
	v.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "me too", To: proto.ChatToAgent, Nonce: "v1"}))
	if m := v.expectControl(proto.CtlError); m["code"] != proto.ErrCodeReadOnly || m["requestId"] != "v1" {
		t.Fatalf("view to agent: %v", m)
	}
	// The connection is intact: a plain post still goes.
	v.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "still here"}))
	if m := v.expectChat(proto.ChatKindMessage); m["text"] != "still here" {
		t.Fatalf("after the refusals: %v", m)
	}
}

// Past the burst a connection's posts are refused with too_many_requests,
// and the connection stays open.
func TestChatRateLimitLeavesTheConnectionOpen(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	c := dialViewer(t, e, id, adminToken)
	c.hello(0, 0)
	c.expectControl(proto.CtlReady)
	// The connection's bucket holds ChatBurst posts as its first is taken
	// and earns ChatRatePerSecond more: the first refused is past the burst,
	// and past it by no more than the bucket earned while the posts were
	// read, which on a loaded machine is a post or more.
	start := time.Now()
	for i := range proto.ChatBurst + 40 {
		c.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: strconv.Itoa(i), Nonce: strconv.Itoa(i)}))
	}
	m := c.expectControl(proto.CtlError)
	earned := int(time.Since(start).Seconds() * proto.ChatRatePerSecond)
	if first, err := strconv.Atoi(fmt.Sprint(m["requestId"])); m["code"] != proto.ErrCodeTooManyRequests || err != nil || first < proto.ChatBurst || first > proto.ChatBurst+earned {
		t.Fatalf("limit (%d earned): %v", earned, m)
	}
	c.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 7}))
	if m := c.expectControl(proto.CtlPong); m["ts"] != float64(7) {
		t.Fatalf("pong after the limit: %v", m)
	}
	if h := e.local(id).ChatHistory(); len(h) < proto.ChatBurst {
		t.Fatalf("%d kept", len(h))
	}
}

// The admin stream carries a session's chat and a run's as `chat` events,
// for the counts the sidebar keeps while no page has the thread open.
func TestEventsStreamCarriesChat(t *testing.T) {
	e := newTestEnv(t, nil)
	stream := e.sse(t)
	id := e.createSession("cat")
	owner := dialViewer(t, e, id, adminToken)
	owner.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Nate"}))
	owner.expectControl(proto.CtlReady)
	owner.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "on the stream"}))
	owner.expectChat(proto.ChatKindMessage)
	next := func(what string) string {
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-stream:
				if strings.HasPrefix(ev, "chat ") && strings.Contains(ev, what) {
					return ev
				}
			case <-deadline:
				t.Fatalf("no chat event with %q", what)
			}
		}
	}
	if ev := next(`"text":"on the stream"`); !strings.Contains(ev, `"sessionId":"`+id+`"`) || strings.Contains(ev, `"runId"`) {
		t.Fatalf("session chat event %s", ev)
	}
	runID := e.launchCrew(t, "Streamed", catMember("core", "immediately"))
	coreID := e.waitRunning(t, runID, "core")
	member := dialViewer(t, e, coreID, adminToken)
	member.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Nate"}))
	member.expectControl(proto.CtlReady)
	member.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Scope: proto.ChatScopeRun, Text: "run on the stream"}))
	if ev := next(`"text":"run on the stream"`); !strings.Contains(ev, `"runId":"`+runID+`"`) || strings.Contains(ev, `"sessionId"`) {
		t.Fatalf("run chat event %s", ev)
	}
}

// A host's chat message reaches the admin stream as a `chat` event of the
// connection's session, cleaned; one of a kind the server does not know is
// dropped without costing the host its connection.
func TestHostChatReachesTheEventStream(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	events := e.sse(t)
	host.send(proto.HostChatMsg{T: proto.HostChat, SessionID: "someoneelse00000", Message: proto.ChatMessage{T: "chat", ID: "0123456789abcdef", At: time.Now().UTC().Format(time.RFC3339Nano), Scope: "run", Kind: "message", By: proto.ChatBy{ID: "fedcba9876543210", Name: "Ada\x00", Role: "view"}, Text: "hosted <hello>\x00"}})
	ev := e.waitEvent(t, events, func(ev string) bool { return strings.HasPrefix(ev, "chat ") && strings.Contains(ev, "hosted") })
	for _, want := range []string{`"sessionId":"` + host.sessionID + `"`, `"scope":"session"`, `"name":"Ada"`, `"text":"hosted \u003chello\u003e"`, `"role":"view"`} {
		if !strings.Contains(ev, want) {
			t.Fatalf("streamed %s, missing %s", ev, want)
		}
	}
	host.send(proto.HostChatMsg{T: proto.HostChat, Message: proto.ChatMessage{ID: "x", Kind: "bogus", By: proto.ChatBy{Role: "view"}}})
	host.send(hostActivity(session.ActivityProgress, func(a *proto.Activity) { a.Message = "still here" }))
	e.waitEvent(t, events, func(ev string) bool { return strings.Contains(ev, `"message":"still here"`) })
}

// A view link's viewer is closed as the session's end, not as a revoked
// link, when the session ends and takes its links with it.
func TestAViewLinkClosesAsSessionEndedWhenTheSessionEnds(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	guest := dialViewer(t, e, id, lo["token"].(string))
	guest.hello(0, 0)
	guest.expectControl(proto.CtlReady)
	if resp, _ := e.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	guest.expectClose(proto.CloseSessionEnded)
}

// expectNvim waits for the next nvim_event of the kind.
func (w *wsClient) expectNvim(kind string) map[string]any {
	w.t.Helper()
	for {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for an nvim_event %s: %v", kind, err)
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		if m["t"] == proto.CtlNvimEvent && m["kind"] == kind {
			return m
		}
	}
}

// The editor's Neovim over the viewer WebSocket (design round 12, F8): the
// welcome says the owner may edit and nvim is here; nvim_open answers with
// opened and the buffer; a key's change comes back as lines; :w writes the
// file; a view link is refused with nvim_unavailable.
func TestNvimOverTheViewerWebSocket(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	e := newTestEnv(t, nil)
	dir := filepath.Join(e.root, "notes")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alpha\nbeta\n"), 0o644)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": dir})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	owner := dialViewer(t, e, id, adminToken)
	owner.hello(0, 0)
	if w := owner.expectControl(proto.CtlWelcome); w["fileEdit"] != true || w["nvim"] != true {
		t.Fatalf("welcome %v", w)
	}
	owner.expectControl(proto.CtlReady)
	owner.send(proto.MustControl(proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "notes.txt"}))
	opened := owner.expectNvim(proto.NvimOpened)
	if opened["reqId"] != "r1" || opened["id"] == nil {
		t.Fatalf("opened %v", opened)
	}
	nid := opened["id"].(string)
	if lines := owner.expectNvim(proto.NvimLines); lines["last"] != float64(-1) || len(lines["lines"].([]any)) != 2 {
		t.Fatalf("the whole buffer %v", lines)
	}
	owner.send(proto.MustControl(proto.NvimInput{T: proto.CtlNvimInput, ID: nid, Keys: "dd"}))
	if lines := owner.expectNvim(proto.NvimLines); lines["first"] != nil || lines["last"] != float64(1) || lines["lines"] != nil { // first 0 is left out
		t.Fatalf("dd %v", lines)
	}
	owner.send(proto.MustControl(proto.NvimInput{T: proto.CtlNvimInput, ID: nid, Keys: ":w<CR>"}))
	owner.expectNvim(proto.NvimWritten)
	if b, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(b) != "beta\n" {
		t.Fatalf("after :w: %q", b)
	}
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	guest := dialViewer(t, e, id, lo["token"].(string))
	guest.hello(0, 0)
	if w := guest.expectControl(proto.CtlWelcome); w["fileEdit"] != nil {
		t.Fatalf("a view link's welcome offers editing: %v", w)
	}
	guest.expectControl(proto.CtlReady)
	guest.send(proto.MustControl(proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "g1", Path: "notes.txt"}))
	if ev := guest.expectNvim(proto.NvimError); ev["code"] != proto.ErrCodeNvimUnavailable || ev["reqId"] != "g1" {
		t.Fatalf("a view link: %v", ev)
	}
	owner.send(proto.MustControl(proto.NvimClose{T: proto.CtlNvimClose, ID: nid}))
	// The editor ends on a goroutine of its own, and Neovim writes its state
	// in the test's directories as it leaves: they go only once it has.
	d, _ := e.srv.registry.Get(id)
	d.(*session.Local).WaitEditors()
}

// A save over the viewer WebSocket (design round 12, F6): the read carries
// the file's hash, the save in two parts answers `written`, the file changes
// on disk; a view link's save is refused read_only.
func TestFileWriteOverTheViewerWebSocket(t *testing.T) {
	e := newTestEnv(t, nil)
	dir := filepath.Join(e.root, "save")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alpha\n"), 0o644)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": dir})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	owner := dialViewer(t, e, id, adminToken)
	owner.hello(0, 0)
	if w := owner.expectControl(proto.CtlWelcome); w["fileEdit"] != true {
		t.Fatalf("welcome %v", w)
	}
	owner.expectControl(proto.CtlReady)
	owner.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "r1", Path: "notes.txt"}))
	h, _ := owner.expectFile("r1")
	if h.Sha256 == "" {
		t.Fatalf("read %+v", h)
	}
	body := []byte("alpha\nbeta\n")
	for _, part := range [][2]int{{0, 6}, {6, len(body)}} {
		frame, err := proto.EncodeFileWrite(proto.FileWrite{ReqID: "w1", Path: "notes.txt", Offset: int64(part[0]), Total: int64(len(body)), BaseSha256: h.Sha256}, body[part[0]:part[1]])
		if err != nil {
			t.Fatal(err)
		}
		owner.send(frame)
	}
	if w, _ := owner.expectFile("w1"); w.Kind != "written" || w.Size != int64(len(body)) {
		t.Fatalf("written %+v", w)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(b) != string(body) {
		t.Fatalf("on disk %q", b)
	}
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	guest := dialViewer(t, e, id, lo["token"].(string))
	guest.hello(0, 0)
	guest.expectControl(proto.CtlReady)
	frame, _ := proto.EncodeFileWrite(proto.FileWrite{ReqID: "g1", Path: "notes.txt", Total: 2, Force: true}, []byte("x\n"))
	guest.send(frame)
	if g, _ := guest.expectFile("g1"); g.Kind != "error" || g.Error.Code != "read_only" {
		t.Fatalf("a view link's save %+v", g)
	}
}

// A hosted session's control viewer in relay mode saves a file: the server
// forwards the FILE_WRITE frame to the host instead of closing the
// connection as an unexpected frame. The editor saves this way on every
// connection that is not offered Neovim, so the relay must carry it; without
// it such a viewer could not save over the relay.
func TestHostedViewerRelaysFileWrite(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	v := dialViewer(t, e, host.sessionID, adminToken)
	v.hello(80, 24)
	v.expectControl(proto.CtlWelcome)
	viewerID, _ := host.expect(proto.HostViewerJoin)["viewerId"].(string)
	if viewerID == "" {
		t.Fatal("no viewer id from the host")
	}
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
	// The save frame the editor sends.
	frame, err := proto.EncodeFileWrite(proto.FileWrite{ReqID: "w1", Path: "notes.txt", Total: 2}, []byte("x\n"))
	if err != nil {
		t.Fatal(err)
	}
	v.send(frame)
	// The host sees it as a relayed envelope carrying the FILE_WRITE frame;
	// nothing closed the viewer in between.
	deadline := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case <-deadline:
			t.Fatal("the host did not receive the relayed save")
		case env := <-host.bin:
			id, inner, derr := proto.DecodeRelay(env[1:])
			if derr != nil {
				t.Fatalf("relay envelope: %v", derr)
			}
			if id == viewerID && inner.Type == proto.TypeFileWrite {
				got = true
			}
		}
	}
	// The viewer connection is still open: a relayed output reaches it.
	env, _ := proto.EncodeRelay(viewerID, proto.Encode(proto.TypeOutput, []byte("alive")))
	if err := host.c.Write(t.Context(), websocket.MessageBinary, env); err != nil {
		t.Fatalf("host relay write: %v", err)
	}
	v.expectOutput("alive")
}

// A save part that cannot be read (its header said to be longer than the
// part, or past 4 KiB) names no save to answer: it is a protocol error, and
// the connection is closed with bad_frame as an unexpected frame type closes
// it. No client of Conductor's sends one. (Before this the error came with
// no save id and the connection stayed.)
func TestAMalformedSavePartClosesTheConnection(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	owner := dialViewer(t, e, id, adminToken)
	owner.hello(0, 0)
	owner.expectControl(proto.CtlReady)
	// A header said to be 5 KiB long, past proto.MaxWriteHeader, in a part of 20 bytes.
	part := make([]byte, 20)
	binary.BigEndian.PutUint32(part[8:12], 5<<10)
	owner.send(proto.Encode(proto.TypeFileWrite, part))
	if m := owner.expectControl(proto.CtlError); m["code"] != proto.ErrCodeBadFrame || m["message"] != "malformed file write" {
		t.Fatalf("the viewer was told %v", m)
	}
	owner.expectClose(proto.CloseProtocolError)
}
