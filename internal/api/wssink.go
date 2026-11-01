package api

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

const (
	writeTimeout = 15 * time.Second
	helloTimeout = 5 * time.Second
	readLimit    = 64 << 10
)

// defaultPingEvery is how often a connection is pinged.
const defaultPingEvery = 30 * time.Second

// pingEvery is defaultPingEvery, which a server takes when it is made
// (Server.pingEvery), so a test may shorten it for the servers it makes.
var pingEvery = defaultPingEvery

// defaultHostPingEvery is how often a host's control connection is pinged
// while a link a viewer joined its session through still stands
// (hostKeepalive): the host types such a viewer's lines only while it has
// heard from the server within 15 s, three of these.
const defaultHostPingEvery = 5 * time.Second

// hostPingEvery is defaultHostPingEvery, which a server takes when it is
// made (Server.hostPingEvery), so a test may shorten it.
var hostPingEvery = defaultHostPingEvery

// wsSink writes session frames to a viewer WebSocket and translates the close
// reason into a close code.
type wsSink struct {
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once

	// raw is the connection under c (hijackKeeper), nil when it is not
	// known: the cut closes it, which ends whatever c is doing, the close
	// handshake under way included, which c's own CloseNow only waits for.
	raw net.Conn

	// cutMu guards cut, the timer Closing starts, and closed, which Close
	// sets once it has closed the connection.
	cutMu  sync.Mutex
	cut    *time.Timer
	closed bool

	// closeWait replaces the package's closeWait when set (a test's
	// Server.viewerCloseWait).
	closeWait time.Duration
}

// hijackKeeper is the ResponseWriter a viewer's WebSocket is accepted on: it
// keeps the connection the accept takes over (Hijack), under its TLS if it
// has one, for the viewer's sink to cut (wsSink.raw).
type hijackKeeper struct {
	http.ResponseWriter
	conn net.Conn
}

func (h *hijackKeeper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(h.ResponseWriter).Hijack()
	if err == nil {
		h.conn = c
		if tc, ok := c.(*tls.Conn); ok {
			h.conn = tc.NetConn()
		}
	}
	return c, rw, err
}

// closeWait bounds how long the connection of a viewer the session closed
// is kept while it is written what it is still owed (at the session's end,
// or its host's going, the frames queued for it: its last output) and then
// its error and its close: past it the connection is cut (Closing). A
// viewer that reads that little is let go, whatever it was owed; a write
// that waits on it is never left to its own time (writeTimeout). A hosted
// viewer's queue holds at most 16 MiB (a server session's 1 MiB), which a
// viewer on a fast connection takes in a second or so.
const closeWait = 5 * time.Second

// Closing is told that the client was closed (its subscription ended, as
// session.Sink's closingSink, or a hosted viewer was closed): the
// connection has closeWait from now to be written what the client is owed,
// its error and its close, and is cut then, whatever is being written to
// it or waits on its close, its close handshake included (raw). It returns
// at once, as it may be called under a session's locks.
func (s *wsSink) Closing() {
	s.cutMu.Lock()
	defer s.cutMu.Unlock()
	if s.cut == nil && !s.closed {
		wait := closeWait
		if s.closeWait > 0 {
			wait = s.closeWait
		}
		s.cut = time.AfterFunc(wait, s.cutNow)
	}
}

// cutNow ends the connection at once: its socket first, which stops a
// close handshake under way, then the WebSocket.
func (s *wsSink) cutNow() {
	if s.raw != nil {
		_ = s.raw.Close()
	}
	_ = s.c.CloseNow()
}

func newWSSink(c *websocket.Conn) *wsSink { return newWSSinkOn(c, nil) }

// viewerSink is newWSSinkOn with the server's viewerCloseWait.
func (s *Server) viewerSink(c *websocket.Conn, raw net.Conn) *wsSink {
	sink := newWSSinkOn(c, raw)
	sink.closeWait = s.viewerCloseWait
	return sink
}

// newWSSinkOn is the sink of c, whose socket is raw (nil when not known).
func newWSSinkOn(c *websocket.Conn, raw net.Conn) *wsSink {
	ctx, cancel := context.WithCancel(context.Background())
	return &wsSink{c: c, raw: raw, ctx: ctx, cancel: cancel}
}

func (s *wsSink) WriteFrame(frame []byte) error {
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	return s.c.Write(ctx, websocket.MessageBinary, frame)
}

func (s *wsSink) Close(reason error) {
	s.once.Do(func() {
		code, msg := closeCodeFor(reason)
		if reason != nil {
			// Best-effort error frame before the close so the UI can show why.
			if errCode := errorCodeFor(reason); errCode != "" {
				_ = s.WriteFrame(proto.NewError(errCode, msg))
			}
		}
		_ = s.c.Close(code, msg)
		s.cancel()
		s.cutMu.Lock()
		s.closed = true
		if s.cut != nil {
			s.cut.Stop()
		}
		s.cutMu.Unlock()
	})
}

func closeCodeFor(reason error) (websocket.StatusCode, string) {
	switch {
	case reason == nil:
		return websocket.StatusNormalClosure, "bye"
	case errors.Is(reason, session.ErrExpired):
		return proto.CloseForbidden, "link expired"
	case errors.Is(reason, session.ErrRevoked):
		return proto.CloseForbidden, "link revoked"
	case errors.Is(reason, session.ErrSlowConsumer), errors.Is(reason, signal.ErrSlowViewer):
		return websocket.StatusPolicyViolation, "slow consumer"
	case errors.Is(reason, errShuttingDown):
		return websocket.StatusGoingAway, "server shutting down"
	case errors.Is(reason, signal.ErrHostGone):
		return proto.CloseSessionEnded, "host disconnected"
	case errors.Is(reason, session.ErrSessionEnded):
		return proto.CloseSessionEnded, "session ended"
	}
	return websocket.StatusInternalError, "connection closed"
}

func errorCodeFor(reason error) string {
	switch {
	case errors.Is(reason, session.ErrExpired):
		return proto.ErrCodeExpired
	case errors.Is(reason, session.ErrRevoked):
		return proto.ErrCodeRevoked
	case errors.Is(reason, session.ErrSlowConsumer), errors.Is(reason, signal.ErrSlowViewer):
		return proto.ErrCodeSlowConsumer
	case errors.Is(reason, signal.ErrHostGone):
		return proto.ErrCodeHostDisconnected
	case errors.Is(reason, session.ErrSessionEnded):
		return proto.ErrCodeSessionEnded
	}
	return ""
}

// viewerSeam lets a test order what a hosted viewer's connection does as a
// network may (serveHostedViewer). read, when set, is called before each read
// of the connection, with a channel closed once the viewer's writer has
// ended; write, when set, is called before each frame the writer writes, and
// an error it returns fails that write; ended, when set, is called on the
// writer's goroutine once Pump has returned, before the read loop's waits
// end; waited, when set, is the host connection's OnWait (signal.HostConn):
// called as a viewer's frame takes its place in line for room in the host's
// queue, so a test knows a frame waits without guessing it from the clock
// (#281); left, when set, is called as the read loop returns, its waits
// over and a frame it held dropped, before the connection is closed;
// taking, when set, is called by the host connection's writer before each
// ask for its next message to write (signal.HostConn.Next, which drops a
// revoked viewer's messages as it takes them), holding no lock: a test that
// holds it there keeps what is queued for the host in the queue, where a
// revoke finds it, while the connection's pings and pongs still pass (#281).
type viewerSeam struct {
	read   func(written <-chan struct{})
	write  func() error
	ended  func()
	waited func()
	left   func()
	taking func(ctx context.Context)
}

// seamSink is a sink whose writes fail when the seam says so.
type seamSink struct {
	*wsSink
	fail func() error
}

func (s seamSink) WriteFrame(frame []byte) error {
	if err := s.fail(); err != nil {
		return err
	}
	return s.wsSink.WriteFrame(frame)
}

// readFrame reads one binary frame with a deadline.
func readFrame(ctx context.Context, c *websocket.Conn, timeout time.Duration) (proto.Frame, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	typ, data, err := c.Read(ctx)
	if err != nil {
		return proto.Frame{}, err
	}
	if typ != websocket.MessageBinary {
		return proto.Frame{}, errors.New("text frames are not accepted")
	}
	return proto.Decode(data)
}

// keepalive pings the peer every `every` until ctx ends or a ping fails.
func keepalive(ctx context.Context, c *websocket.Conn, every time.Duration) {
	if every <= 0 {
		every = defaultPingEvery // a Server made without New
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// keepaliveUntilGone pings the peer every `every` until ctx ends, and calls
// gone, once, when a ping cannot be written: the connection has failed. A
// pong that does not come is no such sign, for pongs are read by the
// connection's reader, which may be held meanwhile (a hosted viewer slowed
// by its address's budget, or waiting for room in its host's queue); the
// pings go on. A write that times out closes the connection, and the next
// ping says so.
func keepaliveUntilGone(ctx context.Context, c *websocket.Conn, every time.Duration, gone func()) {
	if every <= 0 {
		every = defaultPingEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, min(writeTimeout, every))
			err := c.Ping(pctx)
			cancel()
			if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				gone()
				return
			}
		}
	}
}

// hostGone ends a hosted viewer whose host has left. ErrHostGone comes back
// only once HostDisconnected has detached the host, and it closes every viewer
// right after, so Pump soon writes what the host still owed and then closes
// the sink with one error frame. The reader waits for that rather than closing
// the sink under Pump. Should Pump's drain and close take longer than wait
// (Server.hostGoneWait, writeTimeout: a viewer that reads slowly), the
// reader closes the sink itself, under Pump: that is safe, for Close runs
// once and the connection takes concurrent writes.
func hostGone(sink *wsSink, pumped <-chan struct{}, wait time.Duration) {
	select {
	case <-pumped:
	case <-time.After(wait):
		sink.Close(signal.ErrHostGone)
	}
}
