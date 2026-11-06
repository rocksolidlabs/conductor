package pty

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pastDrain holds a reader back for longer than the system lets the last
// close of a terminal's slave side wait for it (about 0.6 s on macOS,
// after which what is unread is discarded; Linux keeps it). It is a hold,
// not a wait for anything: what the tests check does not depend on its
// length, and a longer one only makes the old code's loss more certain.
const pastDrain = 3 * time.Second

// exiting says p's process has exited or is exiting: reaped, a zombie,
// gone, or (macOS's ps) in its exit, where it waits for its output to be
// read.
func exiting(p *Process) bool {
	select {
	case <-p.Done():
		return true
	default:
	}
	if heldExited(p.PID()) {
		return true
	}
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(p.PID())).Output()
	return strings.Contains(string(out), "E")
}

// A process that prints and exits at once keeps what it printed for a
// reader that comes only after it has exited: a command that fails at
// start shows its error, however late its session reads. The process is
// a program run directly, as an agent is (a bash leader's exit waits for
// its reader on macOS whatever the parent holds).
func TestLastOutputKeptForALateReader(t *testing.T) {
	p, err := Start(Spec{Argv: []string{"/usr/bin/env"}, Env: []string{"LAST=the last output"}})
	if err != nil {
		t.Fatal(err)
	}
	heldWait(t, "exit", func() bool { return exiting(p) })
	// Held back past the system's wait: the process is reaped inside it
	// where its exit does not wait for the reader, its output gone.
	select {
	case <-p.Done():
	case <-time.After(pastDrain):
	}
	if got := output(t, p); !strings.Contains(got, "the last output") {
		t.Fatalf("a late reader got %q, want the last output", got)
	}
	p.Close()
}

// The exit waits for a reader only while one may come: Stop and Abort,
// with nobody reading, end a process that printed and exited within their
// own bounds (an error once KillWait has passed), as they did before the
// parent kept its descriptor.
func TestStopAndAbortDoNotWaitForAReader(t *testing.T) {
	spec := Spec{Argv: []string{"/usr/bin/env"}, Env: []string{"UNREAD=1"}}
	p, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	heldWait(t, "exit", func() bool { return exiting(p) })
	if err := p.Stop(context.Background(), 0); err != nil {
		t.Fatalf("stop: %v", err)
	}
	held, err := StartHeld(spec)
	if err != nil {
		t.Fatal(err)
	}
	heldWait(t, "exit", func() bool { return exiting(held) })
	if err := held.Abort(); err != nil {
		t.Fatalf("abort: %v", err)
	}
}
