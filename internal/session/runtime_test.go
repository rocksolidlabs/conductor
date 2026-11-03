package session

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A Runtime's terminal drives a Local through RuntimeProcess: its output
// reaches a viewer, a controller's input and resize reach it, its exit
// code is the session's, and a stop ends it (docs/plans/runtime.md, PR-3).
func TestARuntimeTerminalDrivesALocal(t *testing.T) {
	p := sessiontest.NewFakeProc()
	s := NewLocal(Info{ID: "sess", Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24}, RuntimeProcess(sessiontest.FakeTerminal(p)), Options{ScrollbackBytes: 4096})
	t.Cleanup(func() { p.End(0) })
	sink := newChanSink(false)
	sub, err := s.Attach("", RoleControl, "", 100, 30, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Print("hello from the runtime\n"); err != nil {
		t.Fatal(err)
	}
	// The output reaches the viewer, live or as scrollback.
	for seen, deadline := 0, time.After(10*time.Second); ; {
		found := false
		for ; seen < sink.count(); seen++ {
			if f, _ := proto.Decode(sink.frame(seen)); (f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback) && bytes.Contains(f.Payload, []byte("hello from the runtime")) {
				found = true
			}
		}
		if found {
			break
		}
		select {
		case <-sink.writeCh:
		case <-deadline:
			t.Fatalf("the terminal's output did not reach the viewer (%d frames)", sink.count())
		}
	}
	if err := s.Input(sub, []byte("typed")); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-p.Input:
		if string(in) != "typed" {
			t.Fatalf("the terminal was written %q", in)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the input did not reach the terminal")
	}
	if err := s.ResizeWith(sub, 120, 40, true); err != nil {
		t.Fatal(err)
	}
	if c, r := p.Size(); c != 120 || r != 40 {
		t.Fatalf("the terminal is %dx%d, not 120x40", c, r)
	}
	p.End(5)
	select {
	case <-s.Ended():
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end with its terminal")
	}
	if code := s.Info().ExitCode; code == nil || *code != 5 {
		t.Fatalf("the exit code is %v, not 5", code)
	}

	// Stopped: the terminal's Stop ends it.
	q := sessiontest.NewFakeProc()
	stopped := NewLocal(Info{ID: "sess2", Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24}, RuntimeProcess(sessiontest.FakeTerminal(q)), Options{ScrollbackBytes: 4096})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := stopped.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !q.Stopped() {
		t.Fatal("the session's stop did not reach its terminal")
	}
}

// wordsTerminal is a terminal that has exited with words beyond its code.
type wordsTerminal struct{ ext.Terminal }

func (wordsTerminal) Exit() ext.ExitStatus {
	return ext.ExitStatus{Code: 137, Signal: "killed", Err: "wait: the process was lost"}
}

// The terminal's exit is a pty.ExitStatus again: its code, its signal, and
// its words as an error; none for none.
func TestARuntimeTerminalsExitIsAPtyExitStatus(t *testing.T) {
	st := RuntimeProcess(wordsTerminal{}).Exit()
	if st.Code != 137 || st.Signal != "killed" || st.Err == nil || st.Err.Error() != "wait: the process was lost" {
		t.Fatalf("the exit: %+v", st)
	}
	p := sessiontest.NewFakeProc()
	p.End(3)
	if st := RuntimeProcess(sessiontest.FakeTerminal(p)).Exit(); st.Code != 3 || st.Err != nil {
		t.Fatalf("a plain exit: %+v", st)
	}
}
