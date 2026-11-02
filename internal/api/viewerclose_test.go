package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// pipeProc is a process whose output the test writes; it counts what it is
// typed.
type pipeProc struct {
	r     *io.PipeReader
	w     *io.PipeWriter
	done  chan struct{}
	once  sync.Once
	typed atomic.Int64
}

func newPipeProc() *pipeProc {
	r, w := io.Pipe()
	return &pipeProc{r: r, w: w, done: make(chan struct{})}
}
func (p *pipeProc) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeProc) Write(b []byte) (int, error) { p.typed.Add(int64(len(b))); return len(b), nil }
func (p *pipeProc) Resize(uint16, uint16) error { return nil }
func (p *pipeProc) Done() <-chan struct{}       { return p.done }
func (p *pipeProc) Exit() pty.ExitStatus        { return pty.ExitStatus{} }
func (p *pipeProc) Stop(context.Context, time.Duration) error {
	p.once.Do(func() { close(p.done); p.w.Close() })
	return nil
}

// gapSink is a viewer that reads at once; it keeps the longest gap between
// two outputs since it was last reset.
type gapSink struct {
	mu     sync.Mutex
	bytes  int
	last   time.Time
	maxGap time.Duration
}

func (s *gapSink) WriteFrame(f []byte) error {
	fr, err := proto.Decode(f)
	if err != nil || fr.Type != proto.TypeOutput {
		return nil
	}
	now := time.Now()
	s.mu.Lock()
	if !s.last.IsZero() && now.Sub(s.last) > s.maxGap {
		s.maxGap = now.Sub(s.last)
	}
	s.last = now
	s.bytes += len(fr.Payload)
	s.mu.Unlock()
	return nil
}
func (s *gapSink) Close(error) {}

func (s *gapSink) reset() {
	s.mu.Lock()
	s.maxGap = 0
	s.mu.Unlock()
}

func (s *gapSink) stats() (time.Duration, int, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxGap, s.bytes, s.last
}

// smallBufListener gives each accepted connection a small send buffer, so
// that a client that does not read fills it soon.
type smallBufListener struct{ net.Listener }

func (l smallBufListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(16 << 10)
	}
	return c, err
}

// A browser that keeps its WebSocket open but stops reading while the agent
// prints fast is dropped as a slow consumer. Its connection's write and close
// then wait on that browser (up to the write timeout) on its own goroutine:
// the other viewers keep receiving the output, and the session's description
// and its input do not wait.
func TestAWebSocketThatStopsReadingDoesNotStallTheSession(t *testing.T) {
	t.Setenv("CONDUCTOR_RENDEZVOUS", "0")
	proc := newPipeProc()
	local := session.NewLocal(session.Info{ID: "slow-reader", Cols: 80, Rows: 24}, proc, session.Options{ScrollbackBytes: 4096})
	defer proc.Stop(context.Background(), 0)
	healthy := &gapSink{}
	hsub, err := local.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "healthy", Cols: 80, Rows: 24}, healthy)
	if err != nil {
		t.Fatal(err)
	}

	subs := make(chan *session.Subscription, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(readLimit)
		sink := newWSSink(c)
		sub, err := local.AttachWith(session.AttachOptions{Role: session.RoleView, LinkID: "link", Name: "stopped"}, sink)
		if err != nil {
			sink.Close(err)
			return
		}
		defer local.Detach(sub)
		subs <- sub
		// The read side, as serveLocalViewer holds it.
		for {
			if _, err := readFrame(sink.ctx, c, 0); err != nil {
				return
			}
		}
	}))
	srv.Listener = smallBufListener{srv.Listener}
	srv.Start()
	defer srv.Close()

	// The browser: a small receive buffer, and it never reads.
	dialer := &net.Dialer{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		_ = rc.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096) })
		return serr
	}}
	hc := &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}
	cc, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	defer cc.CloseNow()
	var sub *session.Subscription
	select {
	case sub = <-subs:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser never attached")
	}

	// The agent writes 16 KiB every millisecond.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		chunk := bytes.Repeat([]byte("y"), proto.MaxOutput)
		tk := time.NewTicker(time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
			}
			if _, err := proc.w.Write(chunk); err != nil {
				return
			}
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !errors.Is(sub.Reason(), session.ErrSlowConsumer) {
		if time.Now().After(deadline) {
			t.Fatal("the browser that stopped reading was never dropped")
		}
		time.Sleep(time.Millisecond)
	}
	healthy.reset()
	_, before, _ := healthy.stats()

	// For three seconds after, while its connection waits on the browser
	// (writeTimeout, 15 s): a session that waited with it would send the
	// healthy viewer nothing all that while, where a loaded machine pauses
	// the healthy viewer's output for half a second now and then.
	var slowest time.Duration
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		for name, fn := range map[string]func(){
			"Info":  func() { _ = local.Info() },
			"Input": func() { _ = local.Input(hsub, []byte("k")) },
		} {
			t0 := time.Now()
			done := make(chan struct{})
			go func() { fn(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatalf("%s waited on the dropped browser's connection", name)
			}
			slowest = max(slowest, time.Since(t0))
		}
	}
	gap, after, last := healthy.stats()
	if since := time.Since(last); since > gap {
		gap = since
	}
	t.Logf("after the drop: the healthy viewer's longest gap %v, %d bytes more; Info and Input at most %v", gap.Round(time.Millisecond), after-before, slowest.Round(time.Millisecond))
	if gap > 1500*time.Millisecond {
		t.Errorf("the healthy viewer's output stalled for %v", gap)
	}
	if after == before {
		t.Error("the healthy viewer received nothing after the drop")
	}
}

// A client the session has ended (here dropped as a slow consumer) is not
// acted on while its connection closes: what it sends after is not typed
// into the agent.
func TestAClientTheSessionEndedIsNotActedOn(t *testing.T) {
	t.Setenv("CONDUCTOR_RENDEZVOUS", "0")
	proc := newPipeProc()
	local := session.NewLocal(session.Info{ID: "ended-client", Cols: 80, Rows: 24}, proc, session.Options{ScrollbackBytes: 4096})
	defer proc.Stop(context.Background(), 0)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(readLimit)
		(&Server{}).serveLocalViewer(r.Context(), c, nil, local, session.RoleControl, "", true, nil, nil)
	}))
	srv.Listener = smallBufListener{srv.Listener}
	srv.Start()
	defer srv.Close()
	dialer := &net.Dialer{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		_ = rc.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096) })
		return serr
	}}
	hc := &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}
	cc, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	defer cc.CloseNow()
	send := func(frame []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cc.Write(ctx, websocket.MessageBinary, frame); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test"}))
	send(proto.Encode(proto.TypeInput, []byte("before")))
	deadline := time.Now().Add(5 * time.Second)
	for proc.typed.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the controller's input never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	// The client stops reading while the agent prints: it is dropped.
	go func() {
		chunk := bytes.Repeat([]byte("y"), proto.MaxOutput)
		for range 4 * session.HighWaterMark / len(chunk) {
			if _, err := proc.w.Write(chunk); err != nil {
				return
			}
		}
	}()
	deadline = time.Now().Add(10 * time.Second)
	for local.Viewers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the client that stopped reading was never dropped")
		}
		time.Sleep(time.Millisecond)
	}
	typed := proc.typed.Load()
	send(proto.Encode(proto.TypeInput, []byte("after")))
	time.Sleep(500 * time.Millisecond)
	if n := proc.typed.Load() - typed; n != 0 {
		t.Fatalf("%d bytes the dropped client sent were typed into the agent", n)
	}
}
