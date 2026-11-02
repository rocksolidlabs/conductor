package session

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubbornProc is a fakeProc whose first fails stops fail and leave the
// process running (Done open), as a process that ignores its signals
// would; each stop is counted.
type stubbornProc struct {
	*fakeProc
	fails atomic.Int32
	stops atomic.Int32
}

func (p *stubbornProc) Stop(ctx context.Context, grace time.Duration) error {
	p.stops.Add(1)
	if p.fails.Add(-1) >= 0 {
		return errors.New("the process did not exit")
	}
	return p.fakeProc.Stop(ctx, grace)
}

// A stop that failed leaves the session stopped and its process running:
// the next stop signals the process again and answers its error, the
// status staying stopped; once a stop reaps it, ProcessDone is closed.
func TestAStopRetriedAfterAFailureReachesTheProcess(t *testing.T) {
	p := &stubbornProc{fakeProc: newFakeProc()}
	p.fails.Store(1)
	s := NewLocal(Info{ID: "sess", Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24}, p, Options{})
	t.Cleanup(func() { p.exit() })
	if err := s.Stop(t.Context()); err == nil {
		t.Fatal("the failed stop answered nil")
	}
	if s.Info().Status != StatusStopped {
		t.Fatalf("status %s after a failed stop", s.Info().Status)
	}
	select {
	case <-s.ProcessDone():
		t.Fatal("ProcessDone closed with the process running")
	default:
	}
	if err := s.Stop(t.Context()); err != nil {
		t.Fatalf("the retried stop: %v", err)
	}
	if n := p.stops.Load(); n != 2 {
		t.Fatalf("%d stops reached the process, want 2", n)
	}
	select {
	case <-s.ProcessDone():
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessDone not closed once the stop reaped the process")
	}
	if s.Info().Status != StatusStopped {
		t.Fatalf("status %s", s.Info().Status)
	}
}

// Once the process is reaped a stop signals nothing: it answers nil at
// once, however often it is called.
func TestAStopAfterTheProcessIsReapedSignalsNothing(t *testing.T) {
	p := &stubbornProc{fakeProc: newFakeProc()}
	s := NewLocal(Info{ID: "sess", Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24}, p, Options{})
	t.Cleanup(func() { p.exit() })
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-s.ProcessDone()
	for range 3 {
		if err := s.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := p.stops.Load(); n != 1 {
		t.Fatalf("%d stops reached the process, want 1", n)
	}
}

// LastLine is the last line of output that is not blank, its escapes
// stripped.
func TestLastLineIsTheLastOutputLine(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	p.outW.Write([]byte("starting\r\n\x1b[31mPlease run /login\x1b[0m\r\n\r\n"))
	deadline := time.Now().Add(5 * time.Second)
	for s.LastLine() != "Please run /login" {
		if time.Now().After(deadline) {
			t.Fatalf("last line %q", s.LastLine())
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.outW.Write([]byte(strings.Repeat("é", 300) + "\n"))
	for len(s.LastLine()) < maxLastLine-1 {
		if time.Now().After(deadline) {
			t.Fatalf("last line %q", s.LastLine())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if l := s.LastLine(); len(l) > maxLastLine || !strings.HasPrefix(l, "éé") {
		t.Fatalf("a long line %d bytes %q", len(l), l)
	}
}
