package pty

import (
	"bytes"
	"context"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

// signals records what a Process's kill was asked to send, sending it on:
// the seam a test observes Stop and Abort through.
type signals struct {
	mu   sync.Mutex
	sent []syscall.Signal
}

func (s *signals) observe(p *Process) {
	p.kill = func(pid int, sig syscall.Signal) error {
		s.mu.Lock()
		s.sent = append(s.sent, sig)
		s.mu.Unlock()
		return syscall.Kill(pid, sig)
	}
}

func (s *signals) got() []syscall.Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// A process that has exited is not signalled: once Done is closed, Stop
// sends nothing to a group whose id may be another's by then, and Abort
// once released is Stop's. A running one is asked with SIGTERM, which the
// recorder sees, so the seam is the one Stop signals through.
func TestStopAfterExitSendsNoSignal(t *testing.T) {
	p, err := Start(Spec{Argv: []string{"/bin/sh", "-c", "exit 4"}, Env: BuildEnv(os.Environ(), nil, nil, nil)})
	if err != nil {
		t.Fatal(err)
	}
	var sig signals
	sig.observe(p)
	select {
	case <-p.Done():
	case <-time.After(30 * time.Second): // no cap of the code's: a hang fails
		t.Fatal("the process did not exit")
	}
	if err := p.Stop(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := p.Abort(); err != nil {
		t.Fatal(err)
	}
	if got := sig.got(); len(got) != 0 {
		t.Fatalf("signals sent after the exit: %v", got)
	}
	if st := p.Exit(); st.Code != 4 || st.Signal != "" {
		t.Fatalf("exit %+v", st)
	}

	running, err := Start(Spec{Argv: []string{"/bin/sh", "-c", "while :; do sleep 1; done"}, Env: BuildEnv(os.Environ(), nil, nil, nil)})
	if err != nil {
		t.Fatal(err)
	}
	var asked signals
	asked.observe(running)
	if err := running.Stop(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := asked.got(); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("signals sent to a running process: %v", got)
	}
}

func TestStartEchoResizeStop(t *testing.T) {
	if _, err := os.Stat("/bin/cat"); err != nil {
		t.Skip("/bin/cat not available")
	}
	p, err := Start(Spec{Argv: []string{"/bin/cat"}, Env: BuildEnv(os.Environ(), nil, nil, nil), Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	var got []byte
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(got, []byte("hello")) && time.Now().Before(deadline) {
		n, err := p.Read(buf)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got)
		}
		got = append(got, buf[:n]...)
	}
	if !bytes.Contains(got, []byte("hello")) {
		t.Fatalf("echo not observed: %q", got)
	}
	if err := p.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if err := p.Resize(0, 1); err == nil {
		t.Fatal("expected resize error")
	}
	if err := p.Stop(context.Background(), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	if st := p.Exit(); st.Signal == "" && st.Code == 0 {
		t.Fatalf("expected signalled exit, got %+v", st)
	}
}

func TestStartRejectsEmpty(t *testing.T) {
	if _, err := Start(Spec{}); err != ErrNoCommand {
		t.Fatalf("expected ErrNoCommand, got %v", err)
	}
	if _, err := Start(Spec{Argv: []string{"/bin/cat", "a\x00b"}}); err == nil {
		t.Fatal("expected NUL rejection")
	}
}

func TestNormalExit(t *testing.T) {
	p, err := Start(Spec{Argv: []string{"/bin/sh", "-c", "exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
	if p.Exit().Code != 3 {
		t.Fatalf("exit code %+v", p.Exit())
	}
	p.Close()
}
