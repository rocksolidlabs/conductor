// Package sessiontest supports the tests of packages that run a
// session.Local over a process of their own: FakeProc stands in for the PTY
// process. Production code never imports it.
package sessiontest

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/pty"
)

// FakeProc is a session.Process in memory. What a test prints with Print is
// the process's output, what the session writes to the process arrives on
// Input, and End makes it exit.
type FakeProc struct {
	// Input receives a copy of every write to the process, in order. It holds
	// 64 writes; a write waits for room until the process ends.
	Input chan []byte

	out  *io.PipeReader
	outW *io.PipeWriter
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	code    int
	stopped bool
	cols    uint16
	rows    uint16
}

// NewFakeProc returns a running FakeProc.
func NewFakeProc() *FakeProc {
	r, w := io.Pipe()
	return &FakeProc{Input: make(chan []byte, 64), out: r, outW: w, done: make(chan struct{})}
}

// Print writes s as output of the process. It returns once the session has
// read it, or io.ErrClosedPipe once the process has ended.
func (f *FakeProc) Print(s string) error {
	_, err := f.outW.Write([]byte(s))
	return err
}

// End makes the process exit with code: its output ends and Done closes.
// Only the first End or Stop counts.
func (f *FakeProc) End(code int) { f.end(code, false) }

// Stopped reports whether Stop ended the process.
func (f *FakeProc) Stopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// Size is the terminal size of the last Resize, 0 by 0 before one.
func (f *FakeProc) Size() (cols, rows uint16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cols, f.rows
}

func (f *FakeProc) end(code int, stopped bool) {
	f.once.Do(func() {
		f.mu.Lock()
		f.code, f.stopped = code, stopped
		f.mu.Unlock()
		close(f.done)
		f.outW.Close()
	})
}

// Read is the process's output, for the session's pump.
func (f *FakeProc) Read(b []byte) (int, error) { return f.out.Read(b) }

// Write sends a copy of b to Input.
func (f *FakeProc) Write(b []byte) (int, error) {
	select {
	case <-f.done:
		return 0, io.ErrClosedPipe
	default:
	}
	select {
	case f.Input <- bytes.Clone(b):
		return len(b), nil
	case <-f.done:
		return 0, io.ErrClosedPipe
	}
}

// Resize records the terminal size.
func (f *FakeProc) Resize(cols, rows uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cols, f.rows = cols, rows
	return nil
}

// Done is closed once the process has ended.
func (f *FakeProc) Done() <-chan struct{} { return f.done }

// Exit is how the process ended: the code End was given, -1 after Stop.
func (f *FakeProc) Exit() pty.ExitStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return pty.ExitStatus{Code: f.code}
}

// Stop ends the process as a signal would, with code -1.
func (f *FakeProc) Stop(context.Context, time.Duration) error {
	f.end(-1, true)
	return nil
}
