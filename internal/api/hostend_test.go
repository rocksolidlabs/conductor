package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// lastWords is a shell script that waits for the file its first argument
// names, then prints as many numbered lines as its second says and a final
// one, and exits: a session whose last output is printed just before it
// ends.
const lastWords = `while [ ! -e "$1" ]; do sleep 0.02; done; seq 1 "$2"; printf 'the %s line\n' final`

// holdWrites is a viewerSeam whose writer, once hold is set, waits before
// each frame until release is closed: what the server queues for its hosted
// viewers meanwhile stays queued, as it does for a viewer whose network is
// slow.
type holdWrites struct {
	hold    atomic.Bool
	release chan struct{}
	once    sync.Once
	// held counts the frames that came to wait.
	held atomic.Int32
}

func newHoldWrites(t *testing.T) *holdWrites {
	h := &holdWrites{release: make(chan struct{})}
	t.Cleanup(h.let)
	return h
}

func (h *holdWrites) seam() *viewerSeam {
	return &viewerSeam{write: func() error {
		if h.hold.Load() {
			h.held.Add(1)
			<-h.release
		}
		return nil
	}}
}

func (h *holdWrites) let() { h.once.Do(func() { close(h.release) }) }

// theEnd is what a viewer read until its connection closed.
type theEnd struct {
	output []byte
	// outputs counts the output frames.
	outputs int
	// status is the last status control message's, errCode the error
	// frame's before the close.
	status, errCode string
	// outputAfterError says output came after the error frame.
	outputAfterError bool
	closeCode        websocket.StatusCode
}

// readTheEnd reads v until its connection closes, at most for d.
func readTheEnd(t *testing.T, v *wsClient, d time.Duration) theEnd {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	defer cancel()
	var end theEnd
	for {
		_, data, err := v.c.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("the viewer's connection never closed (read %q)", tail(end.output))
			}
			end.closeCode = websocket.CloseStatus(err)
			return end
		}
		f, err := proto.Decode(data)
		if err != nil {
			continue
		}
		switch f.Type {
		case proto.TypeOutput:
			end.output = append(end.output, f.Payload...)
			end.outputs++
			end.outputAfterError = end.outputAfterError || end.errCode != ""
		case proto.TypeControl:
			var m map[string]any
			_ = json.Unmarshal(f.Payload, &m)
			switch m["t"] {
			case proto.CtlStatus:
				end.status, _ = m["status"].(string)
			case proto.CtlError:
				end.errCode, _ = m["code"].(string)
			}
		}
	}
}

func tail(b []byte) []byte {
	if len(b) > 80 {
		return b[len(b)-80:]
	}
	return b
}

// expectTheEnd fails unless end holds the session's output through its
// last numbered line and its final line, its exit, and then its end as
// such (4410 session_ended), never a revoke.
func expectTheEnd(t *testing.T, end theEnd, lines int) {
	t.Helper()
	if !bytes.Contains(end.output, []byte("the final line")) {
		t.Errorf("the viewer never got the session's last output: it ends %q", tail(end.output))
	}
	if !printedLine(end.output, strconv.Itoa(lines)) {
		t.Errorf("the viewer missed output before the last line: it ends %q", tail(end.output))
	}
	if end.status != string(session.StatusExited) {
		t.Errorf("the viewer was told the status %q, want exited", end.status)
	}
	if end.errCode != proto.ErrCodeSessionEnded || end.closeCode != proto.CloseSessionEnded {
		t.Errorf("the viewer was closed with %q and %d, want %q and %d", end.errCode, end.closeCode, proto.ErrCodeSessionEnded, proto.CloseSessionEnded)
	}
	if end.outputAfterError {
		t.Error("output came after the error frame")
	}
}

// printedLine reports whether out, a terminal's bytes, holds line as a
// line of its own as the program printed it: with the carriage returns
// taken out. The terminal's newline translation is the kernel's, and
// macOS's writes a newline's carriage return again when its output queue
// fills between the two, so a reader slower than the writer gets
// "30000\r\r\n" for the "30000\n" that seq printed.
func printedLine(out []byte, line string) bool {
	return bytes.Contains(bytes.ReplaceAll(out, []byte("\r"), nil), []byte("\n"+line+"\n"))
}

// The last-output check reads the numbered line past the carriage return
// macOS's terminal doubles for a slow reader: fed the tail a viewer got on
// the Mac (#281), the exact bytes the check once looked for are not there
// and the line as printed is; a line that is missing, or only part of a
// longer one, is still missed.
func TestPrintedLineReadsPastADoubledCarriageReturn(t *testing.T) {
	mac := []byte("29997\r\n29998\r\n29999\r\n30000\r\r\nthe final line\r\n")
	if bytes.Contains(mac, []byte("\n30000\r\n")) {
		t.Fatal("the Mac's tail holds the exact bytes after all: this test no longer proves the fix")
	}
	for _, c := range []struct {
		name string
		out  string
		want bool
	}{
		{"the Mac's doubled carriage return", string(mac), true},
		{"one carriage return", "29999\r\n30000\r\nthe final line\r\n", true},
		{"three", "29999\r\n30000\r\r\r\nthe final line\r\n", true},
		{"a doubled one before it", "29999\r\r\n30000\r\nthe final line\r\n", true},
		{"missing", "29998\r\n29999\r\nthe final line\r\n", false},
		{"cut short", "29999\r\n3000", false},
		{"part of a longer line", "29999\r\n130000\r\n300001\r\nthe final line\r\n", false},
	} {
		if got := printedLine([]byte(c.out), "30000"); got != c.want {
			t.Errorf("%s: printedLine(%q, 30000) = %v, want %v", c.name, c.out, got, c.want)
		}
	}
}

// linkLetGo waits until the session id of e has ended and no viewer
// attached through linkID is live on it: the session's end has closed them.
// It returns when it saw that.
func linkLetGo(t *testing.T, e *testEnv, id, linkID string) time.Time {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		d, ok := e.srv.registry.Get(id)
		var live int
		switch drv := d.(type) {
		case *signal.HostedSession:
			live = drv.LinkViewers()[linkID]
		case *session.Local:
			live = drv.LinkViewers()[linkID]
		}
		if ok && d.Info().Status.Ended() && live == 0 {
			return time.Now()
		}
		if time.Now().After(deadline) {
			t.Fatal("the session's end never closed the link's viewer")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startHost runs `conductor host` with argv against e, relay only, until the
// test ends, and returns its session's id.
func startHost(t *testing.T, e *testEnv, argv []string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	registered := make(chan string, 1)
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_, _ = hostagent.Run(ctx, hostagent.Options{
			ServerURL: e.http.URL, Token: "test-host-token", Name: "last words", HostName: "laptop", RelayOnly: true,
			Argv: argv, Dir: e.root, Log: slog.New(slog.DiscardHandler),
			Registered: func(id, _ string) { registered <- id },
		})
	}()
	t.Cleanup(func() { cancel(); <-ran })
	select {
	case id := <-registered:
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("the host never registered")
	}
	return ""
}

// viewLink mints a view link to session id of e and returns its id and token.
func viewLink(t *testing.T, e *testEnv, id string) (string, string) {
	t.Helper()
	resp, out := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view", "label": "guest"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("link: %d %v", resp.StatusCode, out)
	}
	return out["link"].(map[string]any)["id"].(string), out["token"].(string)
}

// slowDoor serves e on a second listener whose connections have small send
// buffers, and returns its URL: what a viewer through it does not read soon
// waits on the server.
func slowDoor(t *testing.T, e *testEnv) string {
	t.Helper()
	hs := httptest.NewUnstartedServer(e.srv.Handler())
	hs.Listener = smallBufListener{hs.Listener}
	hs.Start()
	t.Cleanup(hs.Close)
	return hs.URL
}

// dialSlowViewer is dialViewer through base with a small receive buffer, a
// viewer on a slow network: what it does not read soon waits on the server.
func dialSlowViewer(t *testing.T, base, sessionID, token string) *wsClient {
	t.Helper()
	dialer := &net.Dialer{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		_ = rc.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096) })
		return serr
	}}
	hc := &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/ws/sessions/"+sessionID+"?token="+token, &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(proto.MaxFrame)
	t.Cleanup(func() { c.CloseNow() })
	return &wsClient{t: t, c: c}
}

// A hosted session's viewer attached through a share link is given what
// the session printed before it ended, then the session's end as such:
// a view link's guest whose frames are still queued on the server when the
// host reports the end.
func TestAHostedSessionsLinkViewerGetsItsLastOutputThenItsEnd(t *testing.T) {
	e := newTestEnv(t, nil)
	writes := newHoldWrites(t)
	e.srv.viewerSeam = writes.seam()
	start := filepath.Join(t.TempDir(), "go")
	id := startHost(t, e, []string{"/bin/sh", "-c", lastWords, "sh", start, "2000"})
	linkID, token := viewLink(t, e, id)
	guest := dialViewer(t, e, id, token)
	joinRelay(t, guest)

	// From now on the guest's frames stay queued on the server until the
	// session's end has closed it there.
	writes.hold.Store(true)
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linkLetGo(t, e, id, linkID)
	writes.let()
	expectTheEnd(t, readTheEnd(t, guest, 10*time.Second), 2000)
}

// The same through a rendezvous: a server publishes its session there, and
// a guest attached at the rendezvous through a link minted there is given
// the session's last output, then its end: the publishing server's end of
// its links does not reach the rendezvous ahead of the session's end.
func TestAPublishedSessionsLinkViewerGetsItsLastOutputThenItsEnd(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	writes := newHoldWrites(t)
	rendezvous.srv.viewerSeam = writes.seam()
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	start := filepath.Join(t.TempDir(), "go")
	id := local.launch("sh", []string{"-c", lastWords, "sh", start, "2000"})["id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	for local.srv.publishedOf(id) == nil {
		if time.Now().After(deadline) {
			t.Fatal("not published")
		}
		time.Sleep(20 * time.Millisecond)
	}
	hostedID := local.srv.publishedOf(id).ID()
	resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view", "label": "guest"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("link: %d %v", resp.StatusCode, out)
	}
	linkID := out["link"].(map[string]any)["id"].(string)
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	guest := dialViewer(t, rendezvous, hostedID, token)
	joinRelay(t, guest)

	writes.hold.Store(true)
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linkLetGo(t, rendezvous, hostedID, linkID)
	writes.let()
	expectTheEnd(t, readTheEnd(t, guest, 10*time.Second), 2000)
	// The rendezvous no longer opens the link.
	if resp, _ := rendezvous.do("GET", "/api/join/"+token, token, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the join of the ended session's link at the rendezvous: %d", resp.StatusCode)
	}
}

// The publishing server's revoke of its ended session's link, once the
// publication has handed the end on, still stops what the rendezvous writes
// the link's viewer: the publication outlasts the session's end for as long
// as the rendezvous may be writing, and carries the revoke there.
func TestAPublishersRevokeAfterTheEndStopsWhatTheRendezvousStillWrites(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	writes := newHoldWrites(t)
	rendezvous.srv.viewerSeam = writes.seam()
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	start := filepath.Join(t.TempDir(), "go")
	id := local.launch("sh", []string{"-c", lastWords, "sh", start, "2000"})["id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	for local.srv.publishedOf(id) == nil {
		if time.Now().After(deadline) {
			t.Fatal("not published")
		}
		time.Sleep(20 * time.Millisecond)
	}
	hostedID := local.srv.publishedOf(id).ID()
	resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view", "label": "guest"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("link: %d %v", resp.StatusCode, out)
	}
	linkID := out["link"].(map[string]any)["id"].(string)
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	guest := dialViewer(t, rendezvous, hostedID, token)
	joinRelay(t, guest)

	writes.hold.Store(true)
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linkLetGo(t, rendezvous, hostedID, linkID)
	// The publication has handed on what it had by now (its settle takes
	// milliseconds here).
	time.Sleep(time.Second)
	if resp, out := local.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d %v", resp.StatusCode, out)
	}
	writes.let()
	if end := readTheEnd(t, guest, 10*time.Second); end.outputs > 1 {
		t.Fatalf("%d output frames were written after the revoke, the one being written at most", end.outputs)
	}
}

// A server session's link viewer on a slow network is given what the
// session printed before it ended, then its end, as a hosted one is: the
// output still queued for it as its link ends with the session is written
// first.
func TestAServerSessionsLinkViewerGetsItsLastOutputThenItsEnd(t *testing.T) {
	e := newTestEnv(t, nil)
	door := slowDoor(t, e)
	start := filepath.Join(t.TempDir(), "go")
	// About 170 KB: more than the sockets between the server and the guest
	// hold, less than the guest's queue on the server.
	id := e.launch("sh", []string{"-c", lastWords, "sh", start, "30000"})["id"].(string)
	linkID, token := viewLink(t, e, id)
	guest := dialSlowViewer(t, door, id, token)
	guest.hello(80, 24)
	guest.expectControl(proto.CtlReady)

	// The guest reads nothing until the session's end has closed it.
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linkLetGo(t, e, id, linkID)
	expectTheEnd(t, readTheEnd(t, guest, 10*time.Second), 30000)
}

// A link revoked while its hosted session runs still closes its viewer at
// once: nothing queued for it is written after the frame being written as
// the revoke lands, and it is told its link was revoked.
func TestARevokedHostedLinkViewerIsWrittenNothingMore(t *testing.T) {
	e := newTestEnv(t, nil)
	writes := newHoldWrites(t)
	e.srv.viewerSeam = writes.seam()
	start := filepath.Join(t.TempDir(), "go")
	id := startHost(t, e, []string{"/bin/sh", "-c", lastWords + "; sleep 60", "sh", start, "30000"})
	linkID, token := viewLink(t, e, id)
	guest := dialViewer(t, e, id, token)
	joinRelay(t, guest)

	writes.hold.Store(true)
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Some of the output reaches the server and waits there for the guest:
	// a frame of it is held as it is written.
	for deadline := time.Now().Add(10 * time.Second); writes.held.Load() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no output came to be written to the guest")
		}
	}
	if resp, _ := e.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	writes.let()
	end := readTheEnd(t, guest, 10*time.Second)
	if end.errCode != proto.ErrCodeRevoked || end.closeCode != proto.CloseForbidden {
		t.Fatalf("the revoked viewer was closed with %q and %d", end.errCode, end.closeCode)
	}
	if end.outputs > 1 {
		t.Fatalf("%d output frames were written to the revoked viewer, the one being written as it was revoked at most", end.outputs)
	}
	if d, _ := e.srv.registry.Get(id); d.Info().Status.Ended() {
		t.Fatal("the session ended: the revoke was not one of a running session")
	}
}

// A hosted session's link viewer that reads nothing at all is let go soon
// after the session's end, whatever it was still owed: the server does not
// wait on it for as long as a write may take.
func TestAHostedLinkViewerThatReadsNothingIsLetGoSoonAfterTheEnd(t *testing.T) {
	e := newTestEnv(t, nil)
	pumped := make(chan time.Time, 1)
	e.srv.viewerSeam = &viewerSeam{ended: func() { pumped <- time.Now() }}
	door := slowDoor(t, e)
	start := filepath.Join(t.TempDir(), "go")
	id := startHost(t, e, []string{"/bin/sh", "-c", lastWords, "sh", start, "30000"})
	linkID, token := viewLink(t, e, id)
	guest := dialSlowViewer(t, door, id, token)
	joinRelay(t, guest)

	// The guest reads nothing from now on.
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	closed := linkLetGo(t, e, id, linkID)
	select {
	case at := <-pumped:
		if took := at.Sub(closed); took > closeWait+time.Second {
			t.Fatalf("the viewer was let go %v after the end closed it, past %v", took, closeWait)
		}
	case <-time.After(closeWait + 5*time.Second):
		t.Fatalf("the viewer was not let go within %v of the end", closeWait+5*time.Second)
	}
}

// A server session's link viewer that reads nothing at all holds up the
// session's end no longer than closeWait: the end's close of its links
// returns once each link viewer's connection is closed, or cut.
func TestAServerLinkViewerThatReadsNothingHoldsTheEndBriefly(t *testing.T) {
	e := newTestEnv(t, nil)
	door := slowDoor(t, e)
	start := filepath.Join(t.TempDir(), "go")
	id := e.launch("sh", []string{"-c", lastWords, "sh", start, "30000"})["id"].(string)
	linkID, token := viewLink(t, e, id)
	guest := dialSlowViewer(t, door, id, token)
	guest.hello(80, 24)
	guest.expectControl(proto.CtlReady)

	// The guest reads nothing from now on.
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	closed := linkLetGo(t, e, id, linkID)
	for len(e.srv.links.ListBySession(id)) != 0 {
		if took := time.Since(closed); took > closeWait+3*time.Second {
			t.Fatalf("the session's end still waits on the viewer %v after closing it", took)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if took := time.Since(closed); took > closeWait+time.Second {
		t.Fatalf("the session's end waited %v on the viewer, past %v", took, closeWait)
	}
}

// A run's link keeps its viewers on a member whose session ended; a revoke
// of the link then closes them as revoked (4403), not as the session's end,
// which would still write them what was queued for them.
func TestARunLinkRevokedAfterItsMemberEndedClosesAsRevoked(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	runID := e.launchCrew(t, "Squad", catMember("core", "immediately"))
	coreID := e.waitRunning(t, runID, "core")
	_, out := e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"})
	linkID := out["link"].(map[string]any)["id"].(string)
	v := dialViewer(t, e, coreID, out["token"].(string))
	v.hello(80, 24)
	v.expectControl(proto.CtlReady)
	if resp, _ := e.do("DELETE", "/api/sessions/"+coreID, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	e.waitEnded(coreID)
	v.expectControl(proto.CtlStatus)
	if resp, _ := e.do("DELETE", "/api/runs/"+runID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if end := readTheEnd(t, v, 10*time.Second); end.errCode != proto.ErrCodeRevoked || end.closeCode != proto.CloseForbidden {
		t.Fatalf("the member's viewer was closed with %q and %d, want revoked and 4403", end.errCode, end.closeCode)
	}
}

// The connection of a client the session closed is cut closeWait after the
// close, whoever waits on the close: here the session's end closes a link's
// client from within the client's own attach (the change the attach
// reports, as one that lands while the end closes its links), before the
// goroutine serving the client could do anything, and the client reads
// nothing of the scrollback it is owed.
func TestAClientTheEndClosesAsItAttachesIsCutInTime(t *testing.T) {
	proc := newPipeProc()
	var local atomic.Pointer[session.Local]
	l := session.NewLocal(session.Info{ID: "ends-as-it-attaches", Cols: 80, Rows: 24}, proc, session.Options{ScrollbackBytes: 256 << 10, OnChange: func(info session.Info) {
		if s := local.Load(); s != nil && info.Status.Ended() {
			s.EndLink("L")
		}
	}})
	local.Store(l)
	// More scrollback than the sockets between the server and the client hold.
	if _, err := proc.w.Write(bytes.Repeat([]byte("s"), 200<<10)); err != nil {
		t.Fatal(err)
	}
	_ = proc.Stop(context.Background(), 0)
	select {
	case <-l.Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("the session never ended")
	}
	attached := make(chan time.Duration, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		start := time.Now()
		_, err = l.AttachWith(session.AttachOptions{Role: session.RoleView, LinkID: "L", Name: "late"}, newWSSink(c))
		attached <- time.Since(start)
		if err != nil {
			c.CloseNow()
		}
	}))
	srv.Listener = smallBufListener{srv.Listener}
	srv.Start()
	defer srv.Close()
	dialSlowViewer(t, srv.URL, "x", "x") // reads nothing
	select {
	case took := <-attached:
		if took > closeWait+2*time.Second {
			t.Fatalf("the attach waited %v on its own client's close, past %v", took, closeWait)
		}
	case <-time.After(closeWait + 10*time.Second):
		t.Fatal("the attach still waits on its own client's close")
	}
}

// hostedGuest registers a fake host on e and has a guest join its session
// through a view link and move to the relay; it returns the host, the
// guest, the link's id and the guest's viewer id at the host.
func hostedGuest(t *testing.T, e *testEnv) (*fakeHost, *wsClient, string, string) {
	t.Helper()
	host := dialFakeHost(t, e, "hosted-agent-token")
	linkID, token := viewLink(t, e, host.sessionID)
	guest, viewerID := guestThrough(t, e, host, token)
	return host, guest, linkID, viewerID
}

// guestThrough has a guest join host's session through the link token and
// move to the relay; it returns the guest and its viewer id at the host.
func guestThrough(t *testing.T, e *testEnv, host *fakeHost, token string) (*wsClient, string) {
	t.Helper()
	guest := dialViewer(t, e, host.sessionID, token)
	guest.expectControl(proto.CtlWelcome)
	viewerID, _ := host.expect(proto.HostViewerJoin)["viewerId"].(string)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.Simple{T: proto.SigRelay})
	guest.send(relay)
	for {
		f, err := guest.read()
		if err != nil {
			t.Fatalf("waiting for relay_ok: %v", err)
		}
		if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeSignal && tt == proto.SigRelayOK {
			return guest, viewerID
		}
	}
}

// A switchyard that could not delete its file of a link as the session's end
// took the link still has the host's revoke delete it later; that revoke
// also stops what the end still writes the link's viewers.
func TestAHostsRevokeOfALinkWhoseFileStayedStopsTheEndsDrain(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a directory whose mode keeps its owner from deleting in it")
	}
	dir := filepath.Join(t.TempDir(), "data")
	e := keptSwitchyard(t, dir, nil)
	writes := newHoldWrites(t)
	e.srv.viewerSeam = writes.seam()
	hi, hs := instanceHost(e, "instance-secret-0123456789", "1")
	host, _ := dialFakeHostWith(t, e, hi, hs, "")
	linkID, token := mintHostLink(t, host, "r1")
	guest, viewerID := guestThrough(t, e, host, token)
	writes.hold.Store(true)
	for i := range 20 {
		relayOutput(t, host, viewerID, fmt.Sprintf("line %d\r\n", i))
	}
	links := filepath.Join(dir, "links")
	if err := os.Chmod(links, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(links, 0o700) })
	host.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: host.sessionID, Status: "exited"})
	linkLetGo(t, e, host.sessionID, linkID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := e.srv.links.Get(linkID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the link never left with its session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Chmod(links, 0o700); err != nil {
		t.Fatal(err)
	}
	host.send(proto.HostLinkRevokeMsg{T: proto.HostLinkRevoke, RequestID: "rv", LinkID: linkID})
	if m := linkAnswer(t, host, "rv"); m["t"] != proto.HostLinkRevoked {
		t.Fatalf("the revoke once its file can go: %v", m)
	}
	writes.let()
	if end := readTheEnd(t, guest, 10*time.Second); end.outputs > 1 {
		t.Fatalf("%d output frames were written after the revoke, the one being written at most", end.outputs)
	}
}

// A host that reports the end and then leaves, as every host does: a revoke
// of the link after that still stops what the end writes its viewer, though
// the host's leaving let the viewer go from the session.
func TestARevokeAfterTheHostLeftStopsWhatTheEndStillWrites(t *testing.T) {
	e := newTestEnv(t, nil)
	writes := newHoldWrites(t)
	e.srv.viewerSeam = writes.seam()
	host, guest, linkID, viewerID := hostedGuest(t, e)
	writes.hold.Store(true)
	for i := range 20 {
		relayOutput(t, host, viewerID, fmt.Sprintf("line %d\r\n", i))
	}
	host.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: host.sessionID, Status: "exited"})
	linkLetGo(t, e, host.sessionID, linkID)
	host.c.CloseNow()
	deadline := time.Now().Add(10 * time.Second)
	for {
		d, _ := e.srv.registry.Get(host.sessionID)
		if hs, ok := d.(*signal.HostedSession); ok && !hs.Connected() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server never saw the host leave")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp, _ := e.do("DELETE", "/api/sessions/"+host.sessionID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	writes.let()
	if end := readTheEnd(t, guest, 10*time.Second); end.outputs > 1 {
		t.Fatalf("%d output frames were written after the revoke, the one being written at most", end.outputs)
	}
}

// A link revoked after its session ended, while the end still writes its
// viewers what was queued for them (the link forgotten already), stops that
// at the frame being written: through the owner's revoke, and through the
// host's (a publishing server's, whose revoke reaches the switchyard so).
func TestARevokeAfterTheEndStopsWhatTheEndStillWrites(t *testing.T) {
	for _, by := range []string{"owner", "host"} {
		t.Run(by, func(t *testing.T) {
			e := newTestEnv(t, nil)
			writes := newHoldWrites(t)
			e.srv.viewerSeam = writes.seam()
			host := dialFakeHost(t, e, "hosted-agent-token")
			var linkID, token string
			if by == "owner" {
				linkID, token = viewLink(t, e, host.sessionID)
			} else {
				linkID, token = mintHostLink(t, host, "r1")
			}
			guest, viewerID := guestThrough(t, e, host, token)
			writes.hold.Store(true)
			for i := range 20 {
				relayOutput(t, host, viewerID, fmt.Sprintf("line %d\r\n", i))
			}
			host.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: host.sessionID, Status: "exited"})
			linkLetGo(t, e, host.sessionID, linkID)
			deadline := time.Now().Add(10 * time.Second)
			for len(e.srv.links.ListBySession(host.sessionID)) != 0 {
				if time.Now().After(deadline) {
					t.Fatal("the session's end never took its links")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if by == "owner" {
				if resp, _ := e.do("DELETE", "/api/sessions/"+host.sessionID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
					t.Fatalf("revoke: %d", resp.StatusCode)
				}
			} else {
				host.send(proto.HostLinkRevokeMsg{T: proto.HostLinkRevoke, RequestID: "rv", LinkID: linkID})
				host.expect(proto.HostError) // not_found: the end took it
			}
			writes.let()
			end := readTheEnd(t, guest, 10*time.Second)
			if end.outputs > 1 {
				t.Fatalf("%d output frames were written after the revoke, the one being written at most", end.outputs)
			}
			if end.errCode != proto.ErrCodeSessionEnded || end.closeCode != proto.CloseSessionEnded {
				t.Fatalf("the guest was closed with %q and %d, want the end's", end.errCode, end.closeCode)
			}
		})
	}
}

// relayOutput has host relay output to its viewer viewerID.
func relayOutput(t *testing.T, host *fakeHost, viewerID, out string) {
	t.Helper()
	env, err := proto.EncodeRelay(viewerID, proto.Encode(proto.TypeOutput, []byte(out)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := host.c.Write(ctx, websocket.MessageBinary, env); err != nil {
		t.Fatalf("relay: %v", err)
	}
}

// A revoke that has marked its link as the session ends, the link's hook
// that closes its viewers still on its way, closes them as revoked all the
// same, written nothing more: the session's end does not take a link
// revoked before it.
func TestARevokeThatRacesTheEndClosesAsRevoked(t *testing.T) {
	e := newTestEnv(t, nil)
	writes := newHoldWrites(t)
	e.srv.viewerSeam = writes.seam()
	host, guest, linkID, viewerID := hostedGuest(t, e)
	writes.hold.Store(true)
	for i := range 20 {
		relayOutput(t, host, viewerID, fmt.Sprintf("line %d\r\n", i))
	}
	gate, entered := make(chan struct{}), make(chan struct{})
	onRevoke := e.srv.links.OnRevoke
	e.srv.links.OnRevoke = func(sessionID, linkID string) {
		close(entered)
		<-gate
		onRevoke(sessionID, linkID)
	}
	revoked := make(chan struct{})
	go func() {
		e.srv.links.Revoke(host.sessionID, linkID)
		close(revoked)
	}()
	<-entered
	host.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: host.sessionID, Status: "exited"})
	// The session's end has taken its links.
	deadline := time.Now().Add(10 * time.Second)
	for len(e.srv.links.ListBySession(host.sessionID)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the session's end never took its links")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(gate)
	<-revoked
	writes.let()
	end := readTheEnd(t, guest, 10*time.Second)
	if end.errCode != proto.ErrCodeRevoked || end.closeCode != proto.CloseForbidden {
		t.Fatalf("the guest was closed with %q and %d, want revoked and 4403", end.errCode, end.closeCode)
	}
	if end.outputs > 1 {
		t.Fatalf("%d output frames were written to the revoked guest, the one being written at most", end.outputs)
	}
}

// A viewer the session's end closes that takes most of closeWait to be
// written what it is owed, and then does not answer the WebSocket's close,
// is let go at closeWait all the same: the cut ends the close handshake
// under way too.
func TestAViewerThatDoesNotAnswerTheCloseIsCutAtCloseWait(t *testing.T) {
	e := newTestEnv(t, nil)
	var slow atomic.Bool
	pumped := make(chan time.Time, 1)
	e.srv.viewerSeam = &viewerSeam{
		write: func() error {
			if slow.CompareAndSwap(true, false) {
				time.Sleep(closeWait - 1500*time.Millisecond)
			}
			return nil
		},
		ended: func() { pumped <- time.Now() },
	}
	host, guest, linkID, viewerID := hostedGuest(t, e)
	slow.Store(true)
	for i := range 5 {
		relayOutput(t, host, viewerID, fmt.Sprintf("line %d\r\n", i))
	}
	host.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: host.sessionID, Status: "exited"})
	closed := linkLetGo(t, e, host.sessionID, linkID)
	// The guest reads what it is owed and its error, and then nothing: the
	// close that follows is never answered.
	for {
		f, err := guest.read()
		if err != nil {
			t.Fatalf("the guest's connection ended before its error: %v", err)
		}
		if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeControl && tt == proto.CtlError {
			break
		}
	}
	select {
	case at := <-pumped:
		if took := at.Sub(closed); took > closeWait+time.Second {
			t.Fatalf("the guest was let go %v after the end closed it, past %v", took, closeWait)
		}
	case <-time.After(closeWait + 10*time.Second):
		t.Fatal("the guest was never let go")
	}
}

// slowStop is a publication whose Stop waits for its gate, as a
// publication's end does for what it still has to send.
type slowStop struct {
	*gatedPublication
	stopping chan struct{}
	stopOnce sync.Once
}

func (p *slowStop) Stop() {
	p.stopOnce.Do(func() { close(p.stopping) })
	<-p.gate
}

type slowStopPublisher struct{ p *slowStop }

func (sp slowStopPublisher) Publish(context.Context, *session.Local) (PublishedSession, error) {
	return sp.p, nil
}
func (slowStopPublisher) Server() string { return "https://rendezvous.example.net" }

// A session leaving the server closes its viewers first, before its
// publication's end, which can take a while: a run link's revoke looks for
// them in the registry the session has left, so none may still be written
// anything meanwhile.
func TestALeavingSessionsViewersGoBeforeItsPublication(t *testing.T) {
	e := newTestEnv(t, nil)
	var mu sync.Mutex
	pub := &slowStop{gatedPublication: &gatedPublication{id: "there", gate: make(chan struct{}), applied: &[]string{}, mu: &mu, done: make(chan struct{})}, stopping: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-pub.gate:
		default:
			close(pub.gate)
		}
	})
	e.srv.SetPublisher(slowStopPublisher{pub})
	id := e.createSession("cat")
	deadline := time.Now().Add(10 * time.Second)
	for e.srv.publishedOf(id) == nil {
		if time.Now().After(deadline) {
			t.Fatal("not published")
		}
		time.Sleep(10 * time.Millisecond)
	}
	v := dialViewer(t, e, id, adminToken)
	v.hello(80, 24)
	v.expectControl(proto.CtlReady)
	if resp, _ := e.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	e.waitEnded(id)
	removed := make(chan struct{})
	go func() {
		e.do("DELETE", "/api/sessions/"+id, adminToken, nil)
		close(removed)
	}()
	select {
	case <-pub.stopping:
	case <-time.After(10 * time.Second):
		t.Fatal("the publication was never stopped")
	}
	// The publication's end still waits; the viewer was let go already.
	v.expectClose(proto.CloseSessionEnded)
	close(pub.gate)
	<-removed
}
