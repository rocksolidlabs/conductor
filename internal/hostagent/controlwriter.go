package hostagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// What the host writes on its control connection goes out one message at a
// time, from one goroutine per connection (controlWriter): the host's own
// messages first (its status, its attention and activity, the answers and
// candidates of its WebRTC viewers, its link requests), then the frames
// relayed to its viewers, each viewer's in the order its frames come and the
// viewers in turn. A viewer the server says is behind (viewer_behind) takes
// no turn: its frames wait on the host for it to catch up (relaySink), and
// the connection carries the frames of the viewers that take them. A
// message is handed to the connection once what it holds
// unsent is under a mark; until then it waits its turn, for a bounded time
// of its own, and one whose time runs out is withdrawn, never written late.
//
// Under the WebSocket library, the connection's socket is a pacedConn: a
// write to it never waits on the socket, which a goroutine of its own feeds,
// and the connection is given up only when the socket takes nothing at all
// for writeStall. A server may read the connection slowly on purpose (a
// switchyard's bound on what a host relays, which a host shares with the
// other open hosts of its address): what the host sends then waits, here,
// in its turn. A write that must be done within a time limit, as each of the
// host's was and as the library's answer to a ping is, closed the
// connection, and with it every viewer of the session, WebRTC ones included,
// once the server had paused for longer than that; now a relayed viewer
// whose frame waits too long is let go alone, as a slow consumer, and the
// connection stays.
const (
	// controlTurnWait bounds how long one of the host's own messages waits
	// for its turn; one that waits longer is dropped, as a write that timed
	// out was. The few the server must have while the connection lasts (a
	// viewer's close or error, which let the viewer go there too) wait as
	// long as the connection does (sendControlWhole).
	controlTurnWait = 15 * time.Second
	// relayTurnWait bounds how long a frame relayed to a viewer waits for
	// its turn, as long as a frame for a WebRTC viewer waits for room in its
	// data channel (dcSendTimeout): past it the viewer is let go as a slow
	// consumer.
	relayTurnWait = dcSendTimeout
	// writeStall is how long the socket may take nothing at all before the
	// connection is taken for dead and closed. It is longer than a live
	// server pauses its reading: a switchyard's bound on what open hosts
	// relay pauses it until the address has paid for what its hosts sent,
	// about 35 s at the default bound with the most sessions an address may
	// publish each sending a frame of the largest size.
	writeStall = 2 * time.Minute
	// sendMark is what a connection may hold unsent before the next message
	// waits: little, so that a message of the host's own waits for no more
	// than one message ahead of it and what the socket holds.
	sendMark = 64 << 10
	// sendChunk is the most the socket is handed at once, each a sign that
	// it still takes what it is given.
	sendChunk = 64 << 10
	// unsentLimit bounds what a connection holds unsent. The writer adds a
	// message only under sendMark, so past the mark there is at most one
	// message (a relayed frame of the largest size, proto.MaxFrame, in its
	// envelope) and the library's own small frames, its answers to pings
	// among them: more is a server that pings and does not read, and the
	// connection is closed.
	unsentLimit = 4 << 20
)

var (
	// errConnDown is a message's error when the connection is down or ends
	// before the message is written.
	errConnDown = errors.New("host: control connection down")
	// errViewerGone is a relayed frame's error when its viewer goes while
	// the frame waits for its turn.
	errViewerGone = errors.New("host: viewer gone")
	// errTooMuchUnsent is a connection's failure when it would hold more
	// than unsentLimit unsent.
	errTooMuchUnsent = errors.New("host: the server takes too little of the control connection")
)

// pacedConn is a control connection's socket (see above). Writes are kept
// in order and never wait; pump hands them to the socket, sendChunk at a
// time, until the socket takes nothing at all for the stall, and the first
// failure is every later write's error and closes the socket. Close drops what is still kept: a host that
// lets the connection go on purpose waits for it to empty first (settle).
type pacedConn struct {
	net.Conn
	stall time.Duration
	// limit is unsentLimit (a test's own).
	limit int

	mu sync.Mutex
	// kept is what waits for the socket; sending, what pump is handing it
	// now (both count as unsent).
	kept    bytes.Buffer
	sending int
	err     error
	// more wakes pump; less is closed (and replaced) when what is unsent
	// shrinks or the connection fails.
	more chan struct{}
	less chan struct{}
}

func newPacedConn(c net.Conn, stall time.Duration) *pacedConn {
	if stall <= 0 {
		stall = writeStall
	}
	p := &pacedConn{Conn: c, stall: stall, limit: unsentLimit, more: make(chan struct{}, 1), less: make(chan struct{})}
	go p.pump()
	return p
}

// Write keeps b to be sent, after what was written before, and returns at
// once; the socket's failure, once it has failed. Past unsentLimit the
// connection fails and closes.
func (p *pacedConn) Write(b []byte) (int, error) {
	p.mu.Lock()
	if p.err != nil {
		err := p.err
		p.mu.Unlock()
		return 0, err
	}
	if p.kept.Len()+p.sending+len(b) > p.limit {
		p.mu.Unlock()
		p.fail(errTooMuchUnsent)
		_ = p.Conn.Close()
		return 0, errTooMuchUnsent
	}
	p.kept.Write(b)
	p.mu.Unlock()
	select {
	case p.more <- struct{}{}:
	default:
	}
	return len(b), nil
}

// SetWriteDeadline is pump's alone: writes to a pacedConn never wait.
func (p *pacedConn) SetWriteDeadline(time.Time) error { return nil }

// SetDeadline sets the read deadline (see SetWriteDeadline).
func (p *pacedConn) SetDeadline(t time.Time) error { return p.Conn.SetReadDeadline(t) }

// Close drops what is unsent and closes the socket.
func (p *pacedConn) Close() error {
	p.fail(net.ErrClosed)
	return p.Conn.Close()
}

// unsent is what was written and the socket has not taken, and why the
// connection failed (nil while it has not), with a channel closed once
// unsent shrinks or the connection fails. A nil pacedConn (a connection
// dialed without one) holds nothing.
func (p *pacedConn) unsent() (int, <-chan struct{}, error) {
	if p == nil {
		return 0, nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kept.Len() + p.sending, p.less, p.err
}

// fail records the connection's failure, the first one: what is kept is
// dropped, and whoever waits on it is woken.
func (p *pacedConn) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return
	}
	p.err = err
	// Released, not only emptied: a connection that failed holds nothing.
	p.kept = bytes.Buffer{}
	p.sending = 0
	close(p.less)
	p.less = make(chan struct{})
	select {
	case p.more <- struct{}{}:
	default:
	}
}

// pump hands what is kept to the socket until the connection fails.
func (p *pacedConn) pump() {
	buf := make([]byte, sendChunk)
	for {
		p.mu.Lock()
		for p.err == nil && p.kept.Len() == 0 {
			p.mu.Unlock()
			<-p.more
			p.mu.Lock()
		}
		if p.err != nil {
			p.mu.Unlock()
			return
		}
		n, _ := p.kept.Read(buf)
		p.sending = n
		p.mu.Unlock()
		if err := p.send(buf[:n]); err != nil {
			p.fail(err)
			_ = p.Conn.Close()
			return
		}
	}
}

// send hands b to the socket, giving up only when the socket takes nothing
// at all for the stall: a write whose deadline passes after it took some of
// b goes on with the rest under a deadline of its own.
func (p *pacedConn) send(b []byte) error {
	for len(b) > 0 {
		_ = p.Conn.SetWriteDeadline(time.Now().Add(p.stall))
		n, err := p.Conn.Write(b)
		b = b[n:]
		if n > 0 {
			p.mu.Lock()
			p.sending = max(0, p.sending-n)
			close(p.less)
			p.less = make(chan struct{})
			p.mu.Unlock()
		}
		if err != nil && (n == 0 || !errors.Is(err, os.ErrDeadlineExceeded)) {
			return err
		}
	}
	return nil
}

// errNotPaced is a dial's error when the connection it upgraded is not a
// pacedConn: base's transport is not an *http.Transport.
var errNotPaced = errors.New("host: the control connection was not dialed through a paced socket")

// pacedDial dials url as websocket.Dial does, with base's client
// (http.DefaultClient's transport when nil) made to dial each connection as
// a pacedConn of the given stall, and returns the pacedConn under the
// connection it upgraded: the one the upgrade request went out on, as the
// request's trace saw it, which after a redirect need not be the last one
// dialed. The transport is the dial's own and keeps no idle connection.
// heard, when set, is called each time a read of the upgraded connection
// takes something (hearingTransport).
func pacedDial(ctx context.Context, url string, header http.Header, base *http.Client, stall time.Duration, heard func()) (*websocket.Conn, *pacedConn, *http.Response, error) {
	if base == nil {
		base = &http.Client{}
	}
	t, ok := base.Transport.(*http.Transport)
	if base.Transport == nil {
		t, ok = http.DefaultTransport.(*http.Transport)
	}
	if !ok {
		return nil, nil, nil, errNotPaced
	}
	t = t.Clone()
	t.OnProxyConnectResponse = proxyStatus(t.OnProxyConnectResponse)
	dial := t.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return newPacedConn(c, stall), nil
	}
	// The transport layers TLS over what it dials.
	t.DialTLSContext = nil
	client := *base
	client.Transport = t
	if heard != nil {
		client.Transport = hearingTransport{base: t, heard: heard}
	}
	defer t.CloseIdleConnections()
	var mu sync.Mutex
	var used net.Conn
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		mu.Lock()
		used = info.Conn
		mu.Unlock()
	}})
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header, HTTPClient: &client})
	if err != nil {
		return nil, nil, resp, err
	}
	mu.Lock()
	conn := used
	mu.Unlock()
	// TLS to the server, over TLS to a proxy when it is an https one.
	for {
		tc, ok := conn.(*tls.Conn)
		if !ok {
			break
		}
		conn = tc.NetConn()
	}
	pc, ok := conn.(*pacedConn)
	if !ok {
		c.CloseNow()
		return nil, nil, resp, errNotPaced
	}
	return c, pc, resp, nil
}

// hearingTransport is base, whose upgraded connections (the body of a 101
// answer, which the WebSocket library reads and writes) call heard each
// time a read takes something: what the connection's TLS gives, which it
// gives only once it has authenticated a whole record, or what the socket
// gives on a connection with none. So a large frame that comes in slowly
// counts as it comes, record by record, and bytes that are not yet a whole
// record (anyone's, on the way) do not.
type hearingTransport struct {
	base  http.RoundTripper
	heard func()
}

func (h hearingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := h.base.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusSwitchingProtocols {
		if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
			resp.Body = hearingBody{ReadWriteCloser: rwc, heard: h.heard}
		}
	}
	return resp, err
}

// hearingBody is an upgraded connection whose reads call heard when they
// take something.
type hearingBody struct {
	io.ReadWriteCloser
	heard func()
}

func (b hearingBody) Read(p []byte) (int, error) {
	n, err := b.ReadWriteCloser.Read(p)
	if n > 0 {
		b.heard()
	}
	return n, err
}

// outMessage is one message waiting for, or taking, its turn.
type outMessage struct {
	typ  websocket.MessageType
	data []byte
	// taken says the writer has it; under the writer's mu.
	taken bool
	// done carries the write's outcome, once: from the writer, or from the
	// connection's end while the message still waits.
	done chan error
}

// controlWriter writes the messages of one control connection, c over pc
// (see above).
type controlWriter struct {
	c  *websocket.Conn
	pc *pacedConn
	// live is what the host hears on c (connLive).
	live *connLive
	// controlTurn replaces controlTurnWait in tests.
	controlTurn time.Duration
	// after is the clock of put's turn waits (a test's own; nil is a
	// timer), and taking is told, in tests, each time run is about to
	// take a message from a lane.
	after  func(time.Duration) <-chan time.Time
	taking func()
	// presented is what the connection's registration presented and
	// renews whether its registered said renews (renewToken's): made with
	// the connection, gone with it. presented changes only on renewToken's
	// goroutine.
	presented string
	renews    bool

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}

	mu      sync.Mutex
	control []*outMessage
	relay   []*outMessage
	stopped bool
}

// newControlWriter starts the writer of c, whose socket is pc.
func newControlWriter(c *websocket.Conn, pc *pacedConn) *controlWriter {
	return newControlWriterWith(c, pc, nil, nil)
}

// newControlWriterWith is newControlWriter with its clock and its taking
// hook (tests; nil for none).
func newControlWriterWith(c *websocket.Conn, pc *pacedConn, after func(time.Duration) <-chan time.Time, taking func()) *controlWriter {
	ctx, cancel := context.WithCancel(context.Background())
	w := &controlWriter{c: c, pc: pc, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), after: after, taking: taking}
	go w.run()
	return w
}

// sendControl writes one of the host's own messages, ahead of the relayed
// frames, once its turn comes within controlTurnWait (controlTurn).
func (w *controlWriter) sendControl(typ websocket.MessageType, data []byte) error {
	turn := controlTurnWait
	if w.controlTurn > 0 {
		turn = w.controlTurn
	}
	return w.put(true, typ, data, turn, nil)
}

// sendControlWhole is sendControl for a message the server must have while
// the connection lasts: it waits for its turn as long as the connection
// does, which writeStall bounds, or until gone closes (errViewerGone).
func (w *controlWriter) sendControlWhole(typ websocket.MessageType, data []byte, gone <-chan struct{}) error {
	return w.put(true, typ, data, 0, gone)
}

// sendRelay writes a frame relayed to a viewer once its turn comes within
// turn: session.ErrSlowConsumer when it does not, errViewerGone when gone
// closes first. Either way the frame is withdrawn, and the connection is
// left as it is.
func (w *controlWriter) sendRelay(data []byte, turn time.Duration, gone <-chan struct{}) error {
	return w.put(false, websocket.MessageBinary, data, turn, gone)
}

// unsent is what the connection holds unsent.
func (w *controlWriter) unsent() int {
	n, _, _ := w.pc.unsent()
	return n
}

// put queues a message in its lane and waits until it is written or its
// wait for its turn ends (turn, none when 0; gone): then it is withdrawn.
func (w *controlWriter) put(control bool, typ websocket.MessageType, data []byte, turn time.Duration, gone <-chan struct{}) error {
	m := &outMessage{typ: typ, data: data, done: make(chan error, 1)}
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return errConnDown
	}
	if control {
		w.control = append(w.control, m)
	} else {
		w.relay = append(w.relay, m)
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	var expired <-chan time.Time
	if turn > 0 && w.after != nil {
		expired = w.after(turn)
	} else if turn > 0 {
		t := time.NewTimer(turn)
		defer t.Stop()
		expired = t.C
	}
	why := session.ErrSlowConsumer
	select {
	case err := <-m.done:
		return err
	case <-expired:
	case <-gone:
		why = errViewerGone
	}
	w.mu.Lock()
	if m.taken {
		// Handed to the connection already, which takes no time.
		w.mu.Unlock()
		return <-m.done
	}
	w.control = slices.DeleteFunc(w.control, func(o *outMessage) bool { return o == m })
	w.relay = slices.DeleteFunc(w.relay, func(o *outMessage) bool { return o == m })
	w.mu.Unlock()
	// Withdrawn, unless the connection's end answered it meanwhile.
	select {
	case err := <-m.done:
		return err
	default:
		return why
	}
}

// run hands the messages to the connection in their turn, each once the
// connection holds less than sendMark unsent, until the connection fails or
// the writer is stopped.
func (w *controlWriter) run() {
	defer close(w.done)
	defer w.stop()
	for {
		n, less, err := w.pc.unsent()
		if err != nil {
			return
		}
		if n > sendMark {
			select {
			case <-less:
			case <-w.ctx.Done():
				return
			}
			continue
		}
		if w.taking != nil {
			w.taking()
		}
		w.mu.Lock()
		if w.stopped {
			w.mu.Unlock()
			return
		}
		var m *outMessage
		switch {
		case len(w.control) > 0:
			m, w.control = dequeue(w.control)
		case len(w.relay) > 0:
			m, w.relay = dequeue(w.relay)
		}
		if m != nil {
			m.taken = true
		}
		w.mu.Unlock()
		if m == nil {
			select {
			case <-w.wake:
			case <-w.ctx.Done():
				return
			}
			continue
		}
		// A write to a pacedConn does not wait on the socket.
		err = w.c.Write(w.ctx, m.typ, m.data)
		m.done <- err
		if err != nil {
			// A message cut short leaves the connection unusable.
			w.c.CloseNow()
			return
		}
	}
}

// dequeue takes a lane's first message, and leaves no reference to it in
// the lane: a message written is not kept, with its data, by the array
// under the lane. An empty lane lets go of its array.
func dequeue(lane []*outMessage) (*outMessage, []*outMessage) {
	m := lane[0]
	lane[0] = nil
	if len(lane) == 1 {
		return m, nil
	}
	return m, lane[1:]
}

// stop ends the writer: what waits is answered errConnDown, as is every
// message put after. It does not wait for the writer's goroutine, which
// ends at once.
func (w *controlWriter) stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	waiting := append(w.control, w.relay...)
	w.control, w.relay = nil, nil
	w.mu.Unlock()
	w.cancel()
	for _, m := range waiting {
		m.done <- errConnDown
	}
}
