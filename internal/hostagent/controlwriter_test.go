package hostagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// heldServer is a WebSocket server that reads what a host sends only once
// it is let (read), and hands over what it reads, in order.
type heldServer struct {
	read chan struct{}
	got  chan []byte
	conn chan *websocket.Conn
}

// dialHeld starts a heldServer and dials it as a host does, its socket a
// pacedConn of the given stall with a small send buffer, so that it backs up
// soon after the server stops reading.
func dialHeld(t *testing.T, stall time.Duration) (*heldServer, *websocket.Conn, *pacedConn) {
	t.Helper()
	s := &heldServer{read: make(chan struct{}), got: make(chan []byte, 64), conn: make(chan *websocket.Conn, 1)}
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(8 << 20)
		s.conn <- c
		select {
		case <-s.read:
		case <-done:
			return
		}
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			s.got <- data
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(done) })
	dialer := &net.Dialer{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		_ = rc.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 4<<10) })
		return serr
	}}
	c, pc, _, err := pacedDial(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil, &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}, stall, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return s, c, pc
}

// backedUp waits until pc holds more than it may before the next message
// goes: the socket under it takes nothing more.
func backedUp(t *testing.T, pc *pacedConn) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, _, err := pc.unsent()
		if err != nil {
			t.Fatalf("the connection failed: %v", err)
		}
		if n > sendMark {
			// Held, not merely on its way: still held a moment later.
			time.Sleep(100 * time.Millisecond)
			if m, _, _ := pc.unsent(); m > sendMark {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the connection never backed up (%d unsent)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waiting is how many messages wait in w's lanes.
func waiting(w *controlWriter) (control, relay int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.control), len(w.relay)
}

func waitLanes(t *testing.T, w *controlWriter, control, relay int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, r := waiting(w)
		if c == control && r == relay {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lanes hold %d and %d, want %d and %d", c, r, control, relay)
		}
		time.Sleep(time.Millisecond)
	}
}

// expectGot reads what the server got next and compares it with want.
func (s *heldServer) expectGot(t *testing.T, want []byte) {
	t.Helper()
	select {
	case got := <-s.got:
		if !bytes.Equal(got, want) {
			t.Fatalf("the server got %d bytes %.12q, want %d bytes %.12q", len(got), got, len(want), want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the server got nothing; want %.12q", want)
	}
}

// While the server does not read, a write to the connection still returns
// at once; what follows waits its turn, the host's own messages ahead of the
// frames relayed to its viewers. Once the server reads, it gets them all in
// that order.
func TestTheHostsOwnMessagesGoAheadOfRelayedFrames(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	big := bytes.Repeat([]byte("b"), 1<<20)
	// The server reads nothing until the test lets it, and the socket takes
	// a fraction of the megabyte: a write that waited on the socket would
	// not return at all. The library hands a megabyte over a few kilobytes
	// at a time, which takes a second on a loaded machine.
	start := time.Now()
	wrote := make(chan error, 1)
	go func() { wrote <- w.sendRelay(big, time.Second, nil) }()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("a write waited %v on a connection the server does not read", time.Since(start))
	}
	backedUp(t, pc)
	relayed := make(chan error, 1)
	go func() { relayed <- w.sendRelay([]byte("relayed"), time.Minute, nil) }()
	waitLanes(t, w, 0, 1)
	own := make(chan error, 1)
	go func() { own <- w.sendControl(websocket.MessageText, []byte(`{"t":"status"}`)) }()
	waitLanes(t, w, 1, 1)
	close(s.read)
	s.expectGot(t, big)
	s.expectGot(t, []byte(`{"t":"status"}`))
	s.expectGot(t, []byte("relayed"))
	for _, ch := range []chan error{own, relayed} {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
}

// A frame the server would not relay (past its type's bound) is its viewer's
// error alone: never written, so the server has no cause to close the
// host's connection, and the frames after it go.
func TestAFrameTheServerWouldNotRelayIsNeverWritten(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	a := &agent{out: w}
	const viewer = "0123456789abcdef"
	over := proto.Encode(proto.TypeControl, bytes.Repeat([]byte("x"), proto.MaxControl+1))
	if err := a.sendRelay(viewer, over, nil); !errors.Is(err, proto.ErrTooLarge) {
		t.Fatalf("a control frame past its bound: %v", err)
	}
	fits := proto.MustControl(map[string]string{"t": "status"})
	if err := a.sendRelay(viewer, fits, nil); err != nil {
		t.Fatal(err)
	}
	close(s.read)
	want, _ := proto.EncodeRelay(viewer, fits)
	s.expectGot(t, want)
}

// A relayed frame whose turn does not come within its wait is withdrawn as
// a slow consumer's, and one whose viewer goes is withdrawn at once: neither
// is ever written, and the connection stays, carrying what comes after.
func TestARelayedFrameThatWaitsTooLongIsWithdrawnAndTheConnectionStays(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	big := bytes.Repeat([]byte("b"), 1<<20)
	if err := w.sendRelay(big, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	start := time.Now()
	if err := w.sendRelay([]byte("late"), 200*time.Millisecond, nil); !errors.Is(err, session.ErrSlowConsumer) {
		t.Fatalf("a frame that waited past its turn: %v", err)
	}
	if took := time.Since(start); took < 200*time.Millisecond || took > 2*time.Second {
		t.Fatalf("it was withdrawn after %v", took)
	}
	gone := make(chan struct{})
	left := make(chan error, 1)
	go func() { left <- w.sendRelay([]byte("gone"), time.Minute, gone) }()
	waitLanes(t, w, 0, 1)
	close(gone)
	select {
	case err := <-left:
		if !errors.Is(err, errViewerGone) {
			t.Fatalf("a frame whose viewer went: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a frame whose viewer went went on waiting")
	}
	if _, _, err := pc.unsent(); err != nil {
		t.Fatalf("the connection failed: %v", err)
	}
	close(s.read)
	after := make(chan error, 1)
	go func() { after <- w.sendRelay([]byte("after"), time.Minute, nil) }()
	s.expectGot(t, big)
	s.expectGot(t, []byte("after"))
	if err := <-after; err != nil {
		t.Fatal(err)
	}
}

// The library answers a ping whatever the connection holds unsent: an answer
// it cannot write within its own time limit (5 s) would end the reading of
// the connection, and with it the host's every viewer.
func TestAPingIsAnsweredWhileTheConnectionIsBackedUp(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	if err := w.sendRelay(bytes.Repeat([]byte("b"), 1<<20), time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	readErr := make(chan error, 1)
	go func() {
		_, _, err := c.Read(t.Context())
		readErr <- err
	}()
	server := <-s.conn
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		_ = server.Ping(ctx) // its answer waits behind what is unsent
	}()
	select {
	case err := <-readErr:
		t.Fatalf("the host's reading ended while the connection was backed up: %v", err)
	case <-time.After(7 * time.Second):
	}
	if _, _, err := pc.unsent(); err != nil {
		t.Fatalf("the connection failed: %v", err)
	}
}

// A socket that takes nothing at all for the stall is taken for dead: the
// connection is closed, what waited is answered as the connection's end,
// and so is what is sent after.
func TestASocketThatTakesNothingForTheStallIsGivenUp(t *testing.T) {
	_, c, pc := dialHeld(t, 300*time.Millisecond)
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	// A megabyte backs the socket up. It goes to the paced socket in one
	// write, all of it kept before the socket is fed any, so the stall can
	// only begin once it is there: through the library a megabyte goes a
	// few kilobytes at a time, which on a loaded machine takes as long as
	// the stall, and the socket was given up under it. The server reads
	// nothing, so the bytes need not make a message.
	if _, err := pc.Write(bytes.Repeat([]byte("b"), 1<<20)); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- w.sendRelay([]byte("next"), time.Minute, nil) }()
	select {
	case err := <-waited:
		if !errors.Is(err, errConnDown) {
			t.Fatalf("a frame waiting as the connection was given up: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a socket that took nothing was never given up")
	}
	if _, _, err := pc.unsent(); err == nil {
		t.Fatal("the connection did not record its failure")
	}
	if err := w.sendControl(websocket.MessageText, []byte(`{}`)); !errors.Is(err, errConnDown) {
		t.Fatalf("a message after the connection was given up: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("the connection was not closed: %v", err)
	}
}

// A relayed viewer whose frames do not get their turn on the control
// connection in time is let go alone, as a slow consumer: the session goes
// on for its other viewers, the connection stays, and the server is told
// the viewer is closed once the connection takes it, however long after the
// time one of the host's own messages waits for its turn.
func TestARelayedViewerWhoseFramesWaitTooLongIsLetGoAlone(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	dir := t.TempDir()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: dir, Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(context.Background(), time.Second)
	a := &agent{opts: Options{}, proc: proc, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1", relayWait: 300 * time.Millisecond, controlWait: 200 * time.Millisecond}
	w := a.useConn(c, pc)
	t.Cleanup(w.stop)
	a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: dir, Cols: 80, Rows: 24}, proc, session.Options{})
	big := bytes.Repeat([]byte("b"), 1<<20)
	if err := w.sendRelay(big, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)

	healthy := &outputCount{}
	hsub, err := a.local.AttachWith(session.AttachOptions{ID: "local-1", Role: session.RoleControl, Name: "healthy"}, healthy)
	if err != nil {
		t.Fatal(err)
	}
	const relayed = "relay-0123456789"
	rsub, err := a.local.AttachWith(session.AttachOptions{ID: relayed, Role: session.RoleView, Name: "relayed"}, newRelaySink(a, relayed))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-rsub.Done():
		if !errors.Is(rsub.Reason(), session.ErrSlowConsumer) {
			t.Fatalf("the relayed viewer ended: %v", rsub.Reason())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the relayed viewer was never let go")
	}
	if err := a.local.Input(hsub, []byte("still here\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for healthy.bytes() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the other viewer was sent no output")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r := hsub.Reason(); r != nil {
		t.Fatalf("the other viewer was closed: %v", r)
	}
	// The server goes on not reading past the frame's wait, the wait of the
	// error that tells the viewer why, and the wait of a message of the
	// host's own.
	time.Sleep(time.Second)
	if _, _, err := pc.unsent(); err != nil {
		t.Fatalf("the connection failed: %v", err)
	}
	close(s.read)
	s.expectGot(t, big)
	// Why, if its turn came in time, and then the close.
	for {
		select {
		case got := <-s.got:
			if bytes.Contains(got, []byte(`"slow_consumer"`)) && bytes.Contains(got, []byte(relayed)) {
				continue
			}
			if !bytes.Contains(got, []byte(`"viewer_closed"`)) || !bytes.Contains(got, []byte(relayed)) {
				t.Fatalf("the server got %.80q, want the viewer's close", got)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the server was never told the viewer is closed")
		}
		break
	}
}

// A change of source alone is a change the server is told: to a switchyard,
// which hears no words, a bell's wait turning into a held turn (source
// submit) differs by nothing else.
func TestAnAttentionChangeOfSourceAloneIsTold(t *testing.T) {
	a := &agent{opts: Options{Switchyard: true}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
	s, c, pc := dialHeld(t, 0)
	close(s.read)
	t.Cleanup(a.useConn(c, pc).stop)
	for _, src := range []string{session.SourceBell, session.SourceSubmit} {
		a.onLocalChange(session.Info{ID: "sess-1", Attention: session.Attention{State: session.AttentionNeedsInput, Message: "words", Source: src}})
		select {
		case got := <-s.got:
			if !bytes.Contains(got, []byte(`"source":"`+src+`"`)) || bytes.Contains(got, []byte("words")) {
				t.Fatalf("the switchyard got %q", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the change to %s was never told", src)
		}
	}
}

// An attention change the server was not told (no connection, or no turn on
// it in time) is told with the session's next change, though the attention
// itself did not change again.
func TestAnAttentionChangeNotSentIsSentWithTheNextChange(t *testing.T) {
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
	info := session.Info{ID: "sess-1", Attention: session.Attention{State: session.AttentionNeedsInput, Message: "Allow?", Source: "hook"}}
	a.onLocalChange(info) // no connection: not told
	s, c, pc := dialHeld(t, 0)
	close(s.read)
	t.Cleanup(a.useConn(c, pc).stop)
	a.onLocalChange(info)
	select {
	case got := <-s.got:
		if !bytes.Contains(got, []byte(`"needs_input"`)) {
			t.Fatalf("the server got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the attention was never told")
	}
	// Told now: the same again is not sent again.
	a.onLocalChange(info)
	select {
	case got := <-s.got:
		t.Fatalf("the server got %q again", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// A host dials a server over TLS, one that offers HTTP/2 besides, through
// the paced transport made from the default one, as conductor host dials a
// switchyard: the upgrade is made, and what the host writes arrives.
func TestAPacedDialWorksOverTLS(t *testing.T) {
	got := make(chan []byte, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, data, err := c.Read(r.Context()); err == nil {
			got <- data
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	c, pc, _, err := pacedDial(t.Context(), "wss"+strings.TrimPrefix(srv.URL, "https"), nil, &http.Client{Transport: tr}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	if err := w.sendControl(websocket.MessageText, []byte(`{"t":"status"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-got:
		if string(data) != `{"t":"status"}` {
			t.Fatalf("the server got %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server got nothing")
	}
}

// What a connection holds unsent is bounded: the writer adds a message only
// under its mark, and the library's own frames (its answers to pings) that
// would take it past the limit close the connection instead.
func TestAConnectionHoldsBoundedUnsent(t *testing.T) {
	_, c, pc := dialHeld(t, 0)
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	if err := w.sendRelay(bytes.Repeat([]byte("b"), 1<<20), time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	pc.mu.Lock()
	pc.limit = pc.kept.Len() + pc.sending + 1024
	pc.mu.Unlock()
	if _, err := pc.Write(make([]byte, 512)); err != nil {
		t.Fatalf("a write within the limit: %v", err)
	}
	if _, err := pc.Write(make([]byte, 1024)); !errors.Is(err, errTooMuchUnsent) {
		t.Fatalf("a write past the limit: %v", err)
	}
	if _, _, err := pc.unsent(); !errors.Is(err, errTooMuchUnsent) {
		t.Fatalf("the connection's failure: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("the connection was not closed: %v", err)
	}
}

// The stall is time in which the socket takes nothing at all: one that takes
// a little at a time, never a whole part within the stall, keeps the
// connection, and one that then stops taking anything loses it.
func TestTheStallIsTimeWithNothingTaken(t *testing.T) {
	near, far := net.Pipe()
	t.Cleanup(func() { near.Close(); far.Close() })
	pc := newPacedConn(near, 200*time.Millisecond)
	want := bytes.Repeat([]byte("0123456789abcdef"), sendChunk/16)
	if _, err := pc.Write(want); err != nil {
		t.Fatal(err)
	}
	// 1 KiB every 50 ms: a part of sendChunk takes over three seconds.
	var got []byte
	buf := make([]byte, 1024)
	for len(got) < len(want) {
		time.Sleep(50 * time.Millisecond)
		n, err := far.Read(buf)
		if err != nil {
			t.Fatalf("after %d bytes: %v", len(got), err)
		}
		got = append(got, buf[:n]...)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the socket was handed other bytes")
	}
	if _, _, err := pc.unsent(); err != nil {
		t.Fatalf("a socket that took a little at a time was given up: %v", err)
	}
	if _, err := pc.Write(want); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, err := pc.unsent(); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a socket that took nothing was never given up")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A viewer's error goes to the server however long the connection keeps it
// waiting, the first of the viewer's alone.
func TestAViewersErrorWaitsAsLongAsTheConnection(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1", controlWait: 100 * time.Millisecond}
	w := a.useConn(c, pc)
	t.Cleanup(w.stop)
	big := bytes.Repeat([]byte("b"), 1<<20)
	if err := w.sendRelay(big, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	const staying, leaving = "0123456789abcdef", "fedcba9876543210"
	a.peers[staying] = newPeer(a, staying, session.RoleView, "", "", false)
	a.peers[leaving] = newPeer(a, leaving, session.RoleView, "", "", false)
	a.sendViewerError(staying, "attach_failed", "first")
	a.sendViewerError(staying, "attach_failed", "second")
	// A viewer that leaves while its error waits takes the error with it:
	// the server let it go itself.
	a.sendViewerError(leaving, "attach_failed", "gone")
	a.removePeer(leaving)
	a.sendViewerError(leaving, "attach_failed", "after")
	time.Sleep(500 * time.Millisecond)
	close(s.read)
	s.expectGot(t, big)
	select {
	case got := <-s.got:
		if !bytes.Contains(got, []byte(`"viewer_error"`)) || !bytes.Contains(got, []byte(staying)) || !bytes.Contains(got, []byte("first")) {
			t.Fatalf("the server got %.80q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the viewer's error never went")
	}
	select {
	case got := <-s.got:
		t.Fatalf("the server got %.80q besides", got)
	case <-time.After(300 * time.Millisecond):
	}
}

// The watcher that reports the session's end on a connection ends with the
// connection: one per connection the host ever had would otherwise wait
// for the session's end, each holding its connection's writer.
func TestAStatusWatcherEndsWithItsConnection(t *testing.T) {
	dir := t.TempDir()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: dir, Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(context.Background(), time.Second)
	a := &agent{opts: Options{}, proc: proc, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
	a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: dir, Cols: 80, Rows: 24}, proc, session.Options{})
	for range 3 {
		_, c, pc := dialHeld(t, 0)
		w := a.useConn(c, pc)
		ended := make(chan struct{})
		go func() {
			a.watchStatus(t.Context(), w)
			close(ended)
		}()
		a.dropConn(c)
		select {
		case <-ended:
		case <-time.After(2 * time.Second):
			t.Fatal("the watcher outlived its connection")
		}
	}
}

// The socket a dial writes on is the one its upgrade went out on: after a
// redirect to another server and back, the first connection to the first
// server carries the upgrade, though another was dialed after it.
func TestAPacedDialFindsTheConnectionItUpgraded(t *testing.T) {
	got := make(chan []byte, 1)
	var first *httptest.Server
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, first.URL+"/final", http.StatusFound)
	}))
	t.Cleanup(other.Close)
	var conns atomic.Int32
	first = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, other.URL+"/redirect", http.StatusFound)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, data, err := c.Read(r.Context()); err == nil {
			got <- data
		}
	}))
	first.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	first.Start()
	t.Cleanup(first.Close)
	c, pc, _, err := pacedDial(t.Context(), "ws"+strings.TrimPrefix(first.URL, "http")+"/start", nil, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if n := conns.Load(); n != 1 {
		t.Skipf("the first server's connection was not kept for the upgrade (%d connections)", n)
	}
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	if err := w.sendControl(websocket.MessageText, []byte(`{"t":"status"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-got:
		if string(data) != `{"t":"status"}` {
			t.Fatalf("the server got %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server got nothing")
	}
}

// An attention change that had no turn in time on a connection the server
// reads slowly is told once the connection takes it, with no other change
// of the session's to carry it; and changes meanwhile are told in order,
// the latest last.
func TestAnAttentionChangeWithNoTurnIsToldOnceTheConnectionTakesIt(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1", controlWait: 100 * time.Millisecond}
	w := a.useConn(c, pc)
	t.Cleanup(w.stop)
	big := bytes.Repeat([]byte("b"), 1<<20)
	if err := w.sendRelay(big, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	a.onLocalChange(session.Info{ID: "sess-1", Attention: session.Attention{State: session.AttentionWorking, Source: "hook"}})
	a.onLocalChange(session.Info{ID: "sess-1", Attention: session.Attention{State: session.AttentionNeedsInput, Message: "Allow?", Source: "hook"}})
	time.Sleep(300 * time.Millisecond)
	close(s.read)
	s.expectGot(t, big)
	var last []byte
	for done := false; !done; {
		select {
		case got := <-s.got:
			if !bytes.Contains(got, []byte(`"attention"`)) {
				t.Fatalf("the server got %.80q", got)
			}
			last = got
		case <-time.After(time.Second):
			done = true
		}
	}
	if !bytes.Contains(last, []byte(`"needs_input"`)) {
		t.Fatalf("the server was told last %q", last)
	}
}

// A host dials a server through an https proxy as it does without one: the
// server's TLS goes over the proxy's, and the paced socket is found under
// both.
func TestAPacedDialWorksThroughAnHTTPSProxy(t *testing.T) {
	got := make(chan []byte, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, data, err := c.Read(r.Context()); err == nil {
			got <- data
		}
	}))
	t.Cleanup(srv.Close)
	var tunnels atomic.Int32
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "connect only", http.StatusMethodNotAllowed)
			return
		}
		to, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer to.Close()
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		from, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer from.Close()
		tunnels.Add(1)
		_, _ = from.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() { _, _ = io.Copy(to, from) }()
		_, _ = io.Copy(from, to)
	}))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// The test servers share one certificate.
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	tr.Proxy = http.ProxyURL(proxyURL)
	c, pc, _, err := pacedDial(t.Context(), "wss"+strings.TrimPrefix(srv.URL, "https"), nil, &http.Client{Transport: tr}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if tunnels.Load() != 1 {
		t.Fatalf("%d tunnels through the proxy", tunnels.Load())
	}
	w := newControlWriter(c, pc)
	t.Cleanup(w.stop)
	if err := w.sendControl(websocket.MessageText, []byte(`{"t":"status"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-got:
		if string(data) != `{"t":"status"}` {
			t.Fatalf("the server got %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server got nothing")
	}
}

// The server is told the session ended only after the relayed viewers' last
// frames, though the host's own messages otherwise go first: a server ends
// an ended session's links, and with them their viewers, dropping what was
// not relayed to them yet.
func TestTheSessionsEndGoesAfterTheViewersLastFrames(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	dir := t.TempDir()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/sh", "-c", "sleep 0.5; echo LAST"}, Dir: dir, Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(context.Background(), time.Second)
	a := &agent{opts: Options{Switchyard: true}, proc: proc, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
	w := a.useConn(c, pc)
	t.Cleanup(w.stop)
	a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: dir, Cols: 80, Rows: 24}, proc, session.Options{OnActivity: a.onLocalActivity})
	big := bytes.Repeat([]byte("b"), 1<<20)
	if err := w.sendRelay(big, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	const relayed = "relay-0123456789"
	if _, err := a.local.AttachWith(session.AttachOptions{ID: relayed, Role: session.RoleView, Name: "relayed"}, newRelaySink(a, relayed)); err != nil {
		t.Fatal(err)
	}
	go a.watchStatus(t.Context(), w)
	select {
	case <-a.local.Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("the session never ended")
	}
	time.Sleep(500 * time.Millisecond)
	close(s.read)
	s.expectGot(t, big)
	frames, ended := 0, false
	for !ended {
		select {
		case got := <-s.got:
			switch {
			case bytes.Contains(got, []byte(`"t":"status"`)) && bytes.Contains(got, []byte(`"exited"`)) && !bytes.Contains(got, []byte(relayed)):
				ended = true
			case bytes.Contains(got, []byte(relayed)):
				frames++
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the server was never told the session ended")
		}
	}
	if frames == 0 {
		t.Fatal("no frame of the viewer's went before the session's end")
	}
	select {
	case got := <-s.got:
		if bytes.Contains(got, []byte(relayed)) {
			t.Fatalf("a frame of the viewer's went after the session's end: %.80q", got)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

// A link's revocation goes at once while the session runs, ahead of the
// frames relayed to its viewers; once the session has ended (the publishing
// server revokes its links then), after the viewers' last frames, which the
// revocation would drop.
func TestARevocationGoesAtOnceUnlessTheSessionEnded(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "ended"}[ended], func(t *testing.T) {
			s, c, pc := dialHeld(t, 0)
			dir := t.TempDir()
			argv := []string{"/bin/cat"}
			if ended {
				argv = []string{"/bin/sh", "-c", "sleep 0.5; echo LAST"}
			}
			proc, err := pty.Start(pty.Spec{Argv: argv, Dir: dir, Env: hostEnv(nil)})
			if err != nil {
				t.Fatal(err)
			}
			defer proc.Stop(context.Background(), time.Second)
			a := &agent{opts: Options{Switchyard: true}, proc: proc, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
			w := a.useConn(c, pc)
			t.Cleanup(w.stop)
			a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: dir, Cols: 80, Rows: 24}, proc, session.Options{OnActivity: a.onLocalActivity})
			big := bytes.Repeat([]byte("b"), 1<<20)
			if err := w.sendRelay(big, time.Second, nil); err != nil {
				t.Fatal(err)
			}
			backedUp(t, pc)
			const relayed = "relay-0123456789"
			if _, err := a.local.AttachWith(session.AttachOptions{ID: relayed, Role: session.RoleView, Name: "relayed"}, newRelaySink(a, relayed)); err != nil {
				t.Fatal(err)
			}
			if ended {
				select {
				case <-a.local.Ended():
				case <-time.After(5 * time.Second):
					t.Fatal("the session never ended")
				}
			}
			waitLanes(t, w, 0, 1) // the viewer's first frame waits its turn
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			go func() { _ = a.requestRevoke(ctx, "link-1") }()
			time.Sleep(500 * time.Millisecond)
			close(s.read)
			s.expectGot(t, big)
			frames := 0
			for {
				select {
				case got := <-s.got:
					if bytes.Contains(got, []byte(relayed)) {
						frames++
						continue
					}
					if !bytes.Contains(got, []byte(`"link_revoke"`)) {
						continue
					}
				case <-time.After(10 * time.Second):
					t.Fatal("the revocation never went")
				}
				break
			}
			if ended && frames == 0 {
				t.Fatal("the revocation of an ended session's link went before the viewer's last frames")
			}
			if !ended && frames != 0 {
				t.Fatalf("the revocation of a running session's link went after %d of the viewer's frames", frames)
			}
		})
	}
}

// A message taken from its lane is no longer kept, with its data, by the
// array under the lane, and an emptied lane lets go of its array.
func TestAMessageTakenIsNotKeptByItsLane(t *testing.T) {
	under := []*outMessage{{data: []byte("one")}, {data: []byte("two")}}
	m, rest := dequeue(under)
	if string(m.data) != "one" || len(rest) != 1 || string(rest[0].data) != "two" {
		t.Fatalf("took %q, left %d", m.data, len(rest))
	}
	if under[0] != nil {
		t.Fatal("the lane's array keeps the message taken")
	}
	m, rest = dequeue(rest)
	if string(m.data) != "two" || rest != nil {
		t.Fatalf("took %q, left %v", m.data, rest)
	}
	if under[1] != nil {
		t.Fatal("the lane's array keeps the last message taken")
	}
}

// Stopping the writer, as the connection ends, answers what waits at once.
func TestStoppingTheWriterAnswersWhatWaits(t *testing.T) {
	_, c, pc := dialHeld(t, 0)
	w := newControlWriter(c, pc)
	if err := w.sendRelay(bytes.Repeat([]byte("b"), 1<<20), time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	waited := make(chan error, 2)
	go func() { waited <- w.sendRelay([]byte("r"), time.Minute, nil) }()
	go func() { waited <- w.sendControl(websocket.MessageText, []byte(`{}`)) }()
	waitLanes(t, w, 1, 1)
	w.stop()
	for range 2 {
		select {
		case err := <-waited:
			if !errors.Is(err, errConnDown) {
				t.Fatalf("a message waiting as the writer stopped: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("a message went on waiting after the writer stopped")
		}
	}
}

// A retry of an attention change that had no turn, caught by its
// connection's end, carries on to the connection that has taken its place:
// a host back from away tells its attention as the connection starts and
// leaves that to a retry under way (retellAttention), which must not give
// the attention up with the old connection then. One that gave up before
// the new connection came leaves the attention untold, for the retell.
func TestAnAttentionRetryCaughtByTheConnectionsEndTellsTheNextConnection(t *testing.T) {
	// retrying brings a host to a retry under way on a connection the server
	// does not read: its session's question had no turn in time.
	retrying := func(t *testing.T) (*agent, *websocket.Conn) {
		t.Helper()
		_, c1, pc1 := dialHeld(t, 0)
		a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1", controlWait: 100 * time.Millisecond}
		proc := sessiontest.NewFakeProc()
		t.Cleanup(func() { proc.End(0) })
		a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{OnChange: a.noteChange, Log: discardLog})
		w1 := a.useConn(c1, pc1)
		t.Cleanup(w1.stop)
		if err := w1.sendRelay(bytes.Repeat([]byte("b"), 1<<20), time.Second, nil); err != nil {
			t.Fatal(err)
		}
		backedUp(t, pc1)
		a.local.SetAttentionFull(session.AttentionNeedsInput, "Allow?", "hook", "", nil)
		deadline := time.Now().Add(5 * time.Second)
		for {
			a.mu.Lock()
			under := a.attentionRetry
			a.mu.Unlock()
			if under {
				return a, c1
			}
			if time.Now().After(deadline) {
				t.Fatal("no retry is under way")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	told := func(t *testing.T, s *heldServer) {
		t.Helper()
		select {
		case got := <-s.got:
			if !bytes.Contains(got, []byte(`"needs_input"`)) {
				t.Fatalf("the new connection got %.80q", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the attention was never told on the new connection")
		}
	}
	t.Run("the retry wakes to the new connection", func(t *testing.T) {
		a, _ := retrying(t)
		s2, c2, pc2 := dialHeld(t, 0)
		close(s2.read)
		// The new writer takes the old one's place and stops it: the
		// retry's send on the old fails with the new in place.
		t.Cleanup(a.useConn(c2, pc2).stop)
		told(t, s2)
	})
	t.Run("the retry gave up before the new connection", func(t *testing.T) {
		a, c1 := retrying(t)
		a.dropConn(c1)
		deadline := time.Now().Add(5 * time.Second)
		for {
			a.mu.Lock()
			under, untold := a.attentionRetry, a.attentionUntold
			a.mu.Unlock()
			if !under {
				if !untold {
					t.Fatal("the attention counts as told")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the retry did not end with the connection")
			}
			time.Sleep(5 * time.Millisecond)
		}
		s2, c2, pc2 := dialHeld(t, 0)
		close(s2.read)
		t.Cleanup(a.useConn(c2, pc2).stop)
		a.retellAttention()
		told(t, s2)
	})
}

// The attention a connection's start has the server told is the session's
// as the forwarder gets to it, not a snapshot taken as the connection
// started: a question that comes while the forwarder is busy is what the
// server is told, never an older empty state after it.
func TestAConnectionStartTellsTheSessionAsItIsWhenForwarded(t *testing.T) {
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	// The session's changes are not noted here: the question below comes
	// as one noted before the connection's start would have, in the gap.
	a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{Log: discardLog})
	out := make(chan any, 8)
	release := make(chan struct{})
	a.sendHook = func(v any) {
		out <- v
		<-release
	}
	// The forwarder is busy with a change as the connection starts.
	a.local.SetAttentionFull(session.AttentionWorking, "", "hook", "", nil)
	a.noteChange(a.local.Info())
	if m := (<-out).(proto.HostAttentionMsg); m.State != string(session.AttentionWorking) {
		t.Fatalf("the server got %+v first", m)
	}
	a.retellAttention()
	a.local.SetAttentionFull(session.AttentionNeedsInput, "Allow?", "hook", "", nil)
	close(release)
	select {
	case v := <-out:
		if m := v.(proto.HostAttentionMsg); m.State != string(session.AttentionNeedsInput) {
			t.Fatalf("the connection's start told %+v, want the question", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection's start told nothing")
	}
	select {
	case v := <-out:
		t.Fatalf("the server got %+v besides", v)
	case <-time.After(200 * time.Millisecond):
	}
}

// A retry whose send went through on a connection that was replaced before
// the retry saw the result tells the replacement too: a paced write proves
// no delivery, and the replacement's own start left the attention to the
// retry under way (onLocalChange).
func TestARetryWhoseConnectionWasReplacedAsItSentTellsTheReplacement(t *testing.T) {
	_, c1, pc1 := dialHeld(t, 0)
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{OnChange: a.noteChange, Log: discardLog})
	t.Cleanup(a.useConn(c1, pc1).stop)
	out := make(chan proto.HostAttentionMsg, 8)
	release := make(chan struct{})
	var sent atomic.Int32
	a.sendHook = func(v any) {
		m, ok := v.(proto.HostAttentionMsg)
		if !ok {
			return
		}
		// Counted before it is published: the test starts the retry once
		// it has the first, which is then counted for sure.
		n := sent.Add(1)
		out <- m
		if n == 2 {
			<-release // the retry's: its result waits
		}
	}
	a.local.SetAttentionFull(session.AttentionNeedsInput, "Allow?", "hook", "", nil)
	if m := <-out; m.State != string(session.AttentionNeedsInput) {
		t.Fatalf("the server got %+v first", m)
	}
	// A retry, as a send that had no turn in time starts one: its send
	// goes through, and the result is not seen yet.
	a.retryAttention()
	<-out
	// Meanwhile the connection is replaced, and the new one starts: its
	// retell is left to the retry under way.
	_, c2, pc2 := dialHeld(t, 0)
	t.Cleanup(a.useConn(c2, pc2).stop)
	a.retellAttention()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		forwarding := a.forwarding
		a.mu.Unlock()
		if !forwarding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the retell was never forwarded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	select {
	case m := <-out:
		if m.State != string(session.AttentionNeedsInput) {
			t.Fatalf("the replacement was told %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement was never told the attention")
	}
	for deadline = time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		a.mu.Lock()
		under, untold := a.attentionRetry, a.attentionUntold
		a.mu.Unlock()
		if !under {
			if untold {
				t.Fatal("the attention counts as untold")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the retry never ended")
		}
	}
}
