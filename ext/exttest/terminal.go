package exttest

import (
	"context"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

var _ ext.Terminal = (*FakeTerminal)(nil)

// TerminalSize is a terminal's size, as Resize was given it.
type TerminalSize struct{ Cols, Rows uint16 }

// FakeTerminal is an ext.Terminal over pipes, for a FakeRuntime's
// StartTerminal to answer: no process runs. What the test prints (Print)
// is what the caller reads (Read); what the caller writes (Write) the test
// reads back (Typed); every Resize and every Stop's grace is recorded; End
// is the process exiting. Stop of a terminal not ended ends it as the ask
// ends a process natively (ExitStatus 143, "terminated"), and closes it, as
// every Stop does: Read then answers io.EOF, Write and Resize an error. It
// is safe for concurrent use, and Changed is told at every record (input
// written, a resize, a stop, the end), so a test waits on what it was
// given (Await).
type FakeTerminal struct {
	changes
	info ext.ProcessInfo
	out  *io.PipeReader
	outW *io.PipeWriter
	done chan struct{}

	// endOnce makes the first End the end: a concurrent End or Stop waits
	// for it, so neither returns before Done is closed and the output ended.
	endOnce sync.Once

	mu     sync.Mutex
	typed  []byte
	sizes  []TerminalSize
	stops  []time.Duration
	exit   ext.ExitStatus
	closed bool
}

// NewFakeTerminal is a running FakeTerminal that reports info as its
// process.
func NewFakeTerminal(info ext.ProcessInfo) *FakeTerminal {
	r, w := io.Pipe()
	return &FakeTerminal{info: info, out: r, outW: w, done: make(chan struct{})}
}

// Print is the process printing b: it returns once the caller has read it
// all, or with io.ErrClosedPipe once the terminal is closed.
func (t *FakeTerminal) Print(b []byte) (int, error) { return t.outW.Write(b) }

// Typed answers every byte the caller has written, in order.
func (t *FakeTerminal) Typed() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.typed)
}

// Sizes answers every size Resize was given, in order.
func (t *FakeTerminal) Sizes() []TerminalSize {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.sizes)
}

// Stops answers the grace of every Stop, in order.
func (t *FakeTerminal) Stops() []time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.stops)
}

// End is the process exiting with st: Done is closed and Exit answers st.
// The terminal stays open, as a terminal whose process exited is until it
// is stopped, its output ending (Read io.EOF). Ending an ended terminal
// changes nothing; an End or Stop made while another End is under way
// returns once that one has closed Done and ended the output.
func (t *FakeTerminal) End(st ext.ExitStatus) {
	t.endOnce.Do(func() {
		t.mu.Lock()
		t.exit = st
		t.mu.Unlock()
		t.outW.Close()
		close(t.done)
		t.tell()
	})
}

// Read reads what the test printed; io.EOF once the process has ended or
// the terminal is closed.
func (t *FakeTerminal) Read(b []byte) (int, error) { return t.out.Read(b) }

// Write is input typed at the terminal, recorded; an error once it is
// closed.
func (t *FakeTerminal) Write(b []byte) (int, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, os.ErrClosed
	}
	t.typed = append(t.typed, b...)
	t.mu.Unlock()
	t.tell()
	return len(b), nil
}

// Resize records the size; an error once the terminal is closed.
func (t *FakeTerminal) Resize(cols, rows uint16) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return os.ErrClosed
	}
	t.sizes = append(t.sizes, TerminalSize{Cols: cols, Rows: rows})
	t.mu.Unlock()
	t.tell()
	return nil
}

// Done is closed once the process has ended (End, or Stop).
func (t *FakeTerminal) Done() <-chan struct{} { return t.done }

// Exit is how the process ended, once Done is closed.
func (t *FakeTerminal) Exit() ext.ExitStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exit
}

// Stop records grace, ends a process still running as the ask ends one
// natively (128 plus SIGTERM's 15, "terminated"), closes the terminal and
// returns nil: a fake's stop is never refused.
func (t *FakeTerminal) Stop(_ context.Context, grace time.Duration) error {
	t.mu.Lock()
	t.stops = append(t.stops, grace)
	t.closed = true
	t.mu.Unlock()
	t.End(ext.ExitStatus{Code: 143, Signal: "terminated"})
	t.tell()
	return nil
}

// Process answers the info the terminal was made with.
func (t *FakeTerminal) Process() ext.ProcessInfo { return t.info }
