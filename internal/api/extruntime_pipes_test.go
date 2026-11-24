package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// ctxKey marks the context a test hands a start, so the Runtime can say it
// was handed that context itself.
type ctxKey struct{}

// heardPipes is a FakePipeRuntime that keeps the context each StartPipes
// was handed.
type heardPipes struct {
	sessiontest.FakePipeRuntime
	mu   *sync.Mutex
	ctxs *[]context.Context
}

func (h heardPipes) StartPipes(ctx context.Context, s ext.PipeSpec) (ext.Pipes, error) {
	h.mu.Lock()
	*h.ctxs = append(*h.ctxs, ctx)
	h.mu.Unlock()
	return h.FakePipeRuntime.StartPipes(ctx, s)
}

// A registered Runtime is a PipeStarter through the guard exactly when it
// is one itself: one without pipes is offered none, and one with them is
// offered StartPipes behind the guard. The start is handed the caller's
// context itself, never one bounded by extCall's wait, and its Pipes are
// the guard's, which reach the Runtime's child (its input, its output,
// its stop). A StartPipes that panics is recovered: a refused start whose
// words, like the log's, hold nothing of the panic's value.
func TestRegisteredPipeStarterIsOfferedPipes(t *testing.T) {
	t.Run("a Runtime without pipes", func(t *testing.T) {
		rt := &sessiontest.FakeRuntime{}
		e := runtimeEnv(t, nativeSeam(), nil, withRuntime(rt))
		if _, ok := e.srv.offered.(ext.PipeStarter); ok {
			t.Fatalf("a Runtime without StartPipes is a PipeStarter through the guard (%T)", e.srv.offered)
		}
		if _, ok := e.srv.offered.(guardRuntime); !ok {
			t.Fatalf("the registered Runtime is held as %T, not behind the guard", e.srv.offered)
		}
	})
	t.Run("a Runtime with pipes", func(t *testing.T) {
		fp := sessiontest.NewFakeProc()
		t.Cleanup(func() { fp.End(0) })
		var mu sync.Mutex
		var ctxs []context.Context
		rt := heardPipes{FakePipeRuntime: sessiontest.FakePipeRuntime{FakeRuntime: &sessiontest.FakeRuntime{
			Pipes: func(ext.PipeSpec) (*sessiontest.FakeProc, error) { return fp, nil },
		}}, mu: &mu, ctxs: &ctxs}
		e := runtimeEnv(t, nativeSeam(), nil, withRuntime(rt))
		ps, ok := e.srv.offered.(ext.PipeStarter)
		if !ok {
			t.Fatalf("a Runtime with StartPipes is no PipeStarter through the guard (%T)", e.srv.offered)
		}
		if _, ok := e.srv.offered.(guardPipes); !ok {
			t.Fatalf("the registered Runtime is held as %T, not behind the guard", e.srv.offered)
		}
		ctx := context.WithValue(context.Background(), ctxKey{}, "the caller's")
		p, err := ps.StartPipes(ctx, ext.PipeSpec{Argv: []string{"agent", "-p"}, Ref: ext.ProcessRef{Session: "s-1"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(guardedPipes); !ok {
			t.Fatalf("the Pipes are the Runtime's own (%T), not the guard's", p)
		}
		mu.Lock()
		heard := append([]context.Context(nil), ctxs...)
		mu.Unlock()
		if len(heard) != 1 || heard[0] != ctx {
			t.Fatalf("the Runtime was handed %v, want the caller's context itself: never extCall's, bounded by its wait", heard)
		}
		if c := rt.Asked("StartPipes"); len(c) != 1 || c[0].Ref.Session != "s-1" {
			t.Fatalf("the start recorded: %+v", c)
		}
		go func() { _ = fp.Print("ready\n") }()
		buf := make([]byte, 16)
		if n, err := p.Stdout().Read(buf); err != nil || string(buf[:n]) != "ready\n" {
			t.Fatalf("read %q, %v", buf[:n], err)
		}
		if _, err := io.WriteString(p.Stdin(), "go\n"); err != nil {
			t.Fatal(err)
		}
		select {
		case b := <-fp.Input:
			if string(b) != "go\n" {
				t.Fatalf("the child was written %q", b)
			}
		case <-time.After(testWait):
			t.Fatal("the input did not reach the child")
		}
		if err := p.Stop(t.Context(), time.Second); err != nil || !fp.Stopped() {
			t.Fatalf("the stop: %v, stopped %v", err, fp.Stopped())
		}
		select {
		case <-p.Done():
		default:
			t.Fatal("the Pipes not done once their stop returned")
		}
		if st := p.Exit(); st.Code != -1 {
			t.Fatalf("the exit: %+v", st)
		}
	})
	t.Run("a StartPipes that panics", func(t *testing.T) {
		const secret = "agent-token-in-the-panic"
		rt := sessiontest.FakePipeRuntime{FakeRuntime: &sessiontest.FakeRuntime{
			Pipes: func(ext.PipeSpec) (*sessiontest.FakeProc, error) { panic("cannot start with " + secret) },
		}}
		logs := &logBuffer{}
		e := runtimeEnv(t, nativeSeam(), slog.New(slog.NewTextHandler(logs, nil)), withRuntime(rt))
		ps, ok := e.srv.offered.(ext.PipeStarter)
		if !ok {
			t.Fatalf("a Runtime with StartPipes is no PipeStarter through the guard (%T)", e.srv.offered)
		}
		p, err := ps.StartPipes(t.Context(), ext.PipeSpec{Argv: []string{"agent"}})
		if p != nil || err == nil || err.Error() != "the Runtime fake panicked in StartPipes" {
			t.Fatalf("a start that panicked answered %v, %v", p, err)
		}
		l := logs.String()
		if !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "provider=fake call=StartPipes failure=panic") {
			t.Fatalf("the guard's log:\n%s", l)
		}
		if strings.Contains(l, secret) {
			t.Fatalf("the guard's log quotes the panic:\n%s", l)
		}
	})
}

// overreader is a Pipes whose output and input answer more bytes than the
// buffer they are given holds, and whose Stop is counted and ends it.
type overreader struct {
	ext.Pipes
	stops *atomic.Int32
	done  chan struct{}
}

func (o overreader) Stdout() io.Reader           { return o }
func (o overreader) Stdin() io.WriteCloser       { return o }
func (o overreader) Write(b []byte) (int, error) { return len(b) + 1, nil }
func (o overreader) Close() error                { return nil }
func (o overreader) Read(b []byte) (int, error)  { return len(b) + 1, nil }
func (o overreader) Done() <-chan struct{}       { return o.done }
func (o overreader) Stop(context.Context, time.Duration) error {
	if o.stops.Add(1) == 1 {
		close(o.done)
	}
	return nil
}

// misanswering is a FakePipeRuntime whose StartPipes answers what answer
// gives in place of its own.
type misanswering struct {
	sessiontest.FakePipeRuntime
	answer func() (ext.Pipes, error)
}

func (m misanswering) StartPipes(context.Context, ext.PipeSpec) (ext.Pipes, error) { return m.answer() }

// The guard hands on nothing the Runtime misanswered: a start that answers
// no Pipes and no error is refused, and a Write of the input or a Read of
// the output that answers a count outside its buffer is the child's
// failure, as a panic is: the call answers nothing and the words, and the
// child is stopped through the Runtime's Stop.
func TestAPipesGuardRefusesWhatTheRuntimeMisanswers(t *testing.T) {
	t.Run("no Pipes and no error", func(t *testing.T) {
		rt := misanswering{FakePipeRuntime: sessiontest.FakePipeRuntime{FakeRuntime: &sessiontest.FakeRuntime{}}, answer: func() (ext.Pipes, error) { return nil, nil }}
		e := runtimeEnv(t, nativeSeam(), nil, withRuntime(rt))
		ps, ok := e.srv.offered.(ext.PipeStarter)
		if !ok {
			t.Fatalf("no PipeStarter through the guard (%T)", e.srv.offered)
		}
		if p, err := ps.StartPipes(t.Context(), ext.PipeSpec{}); p != nil || err == nil || err.Error() != "the Runtime fake answered no Pipes" {
			t.Fatalf("a start that answered nothing: %v, %v", p, err)
		}
	})
	t.Run("a written count outside the buffer", func(t *testing.T) {
		over := overreader{stops: &atomic.Int32{}, done: make(chan struct{})}
		rt := misanswering{FakePipeRuntime: sessiontest.FakePipeRuntime{FakeRuntime: &sessiontest.FakeRuntime{}}, answer: func() (ext.Pipes, error) { return over, nil }}
		e := runtimeEnv(t, nativeSeam(), nil, withRuntime(rt))
		p, err := e.srv.offered.(ext.PipeStarter).StartPipes(t.Context(), ext.PipeSpec{})
		if err != nil {
			t.Fatal(err)
		}
		n, err := p.Stdin().Write([]byte("go\n"))
		if n != 0 || err == nil || err.Error() != "the Runtime fake answered a count outside its buffer in Pipes.Stdin.Write" {
			t.Fatalf("a write that answered 4 of 3 bytes: %d, %v", n, err)
		}
		select {
		case <-p.Done():
		case <-time.After(testWait):
			t.Fatal("the child was not stopped")
		}
	})
	t.Run("a count outside the buffer", func(t *testing.T) {
		stops := &atomic.Int32{}
		over := overreader{stops: stops, done: make(chan struct{})}
		rt := misanswering{FakePipeRuntime: sessiontest.FakePipeRuntime{FakeRuntime: &sessiontest.FakeRuntime{}}, answer: func() (ext.Pipes, error) { return over, nil }}
		e := runtimeEnv(t, nativeSeam(), nil, withRuntime(rt))
		p, err := e.srv.offered.(ext.PipeStarter).StartPipes(t.Context(), ext.PipeSpec{})
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 8)
		n, err := p.Stdout().Read(buf)
		if n != 0 || err == nil || err.Error() != "the Runtime fake answered a count outside its buffer in Pipes.Stdout.Read" {
			t.Fatalf("a read that answered %d of %d bytes: %d, %v", len(buf)+1, len(buf), n, err)
		}
		select {
		case <-p.Done():
		case <-time.After(testWait):
			t.Fatal("the child was not stopped")
		}
		if n := stops.Load(); n != 1 {
			t.Fatalf("the Runtime's Stop was called %d times, want once", n)
		}
		if _, err := p.Stdin().Write([]byte("x")); err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("a write once the child failed: %v", err)
		}
	})
}
