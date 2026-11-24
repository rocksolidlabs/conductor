package sessiontest

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session/internal/way"
)

// FakeProtocol is a session's protocol driver (session.Protocol) in
// memory, with its child (Child): a test hands the session the agent's
// events (Hand, and the methods named for each event), each applied before
// the call returns, and reads what the session sent the agent (Sent,
// Replies) and in what order it called the driver and the child (Calls).
// Set its fields before the session enters the protocol way; its methods
// are safe for concurrent use.
type FakeProtocol struct {
	// ProtocolName is what Name answers ("fake-protocol" when empty), and
	// IsVerbatim what Verbatim does.
	ProtocolName string
	IsVerbatim   bool
	// BeginErr is what Begin answers, InterruptErr what Interrupt does
	// (session.ErrNoInterrupt: an agent that offers none), SendErr what
	// Send does (the message recorded all the same).
	BeginErr     error
	InterruptErr error
	SendErr      error
	// BeforeSend, when set, runs in Send with the message before it is
	// recorded: a test's gate on a write.
	BeforeSend func(way.Message)

	mu        sync.Mutex
	calls     []string
	sent      []way.Message
	replies   []FakeReply
	startedOn string
	changed   chan struct{}

	events  chan fakeEvent
	ended   chan struct{}
	endOnce sync.Once
	readErr error
	reading chan struct{}
	readOne sync.Once
	child   *FakeChild
}

// FakeReply is a verdict the session sent on an in-stream request.
type FakeReply struct {
	Ref     string
	Verdict way.Verdict
}

type fakeEvent struct {
	fn   func(way.ProtocolEvents)
	done chan struct{}
}

// NewFakeProtocol returns a driver whose child runs.
func NewFakeProtocol() *FakeProtocol {
	f := &FakeProtocol{events: make(chan fakeEvent), ended: make(chan struct{}), reading: make(chan struct{}), changed: make(chan struct{})}
	f.child = &FakeChild{f: f, done: make(chan struct{})}
	return f
}

// Child is the driver's child: its Stop ends the driver's output.
func (f *FakeProtocol) Child() *FakeChild { return f.child }

// Start is a session.ProtocolStart that starts f, then each of next in
// turn for each later start (a child started again), recording the
// conversation each was started on (StartedOn); past them it fails.
func (f *FakeProtocol) Start(next ...*FakeProtocol) way.Start {
	var mu sync.Mutex
	queue := append([]*FakeProtocol{f}, next...)
	return func(_ context.Context, conversation string) (way.Child, way.Protocol, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(queue) == 0 {
			return nil, nil, errors.New("sessiontest: no fake protocol left to start")
		}
		p := queue[0]
		queue = queue[1:]
		p.mu.Lock()
		p.startedOn = conversation
		p.mu.Unlock()
		return p.child, p, nil
	}
}

// StartedOn is the conversation the driver was started on.
func (f *FakeProtocol) StartedOn() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startedOn
}

// EnterProtocol puts local, a *session.Local running in its terminal, in
// the protocol way with start's child and driver: the tests' way in until
// a move between ways does it.
func EnterProtocol(local any, start way.Start) error {
	if way.Enter == nil {
		return errors.New("sessiontest: package session is not loaded")
	}
	return way.Enter(local, start)
}

// note records a call and wakes whoever waits for one.
func (f *FakeProtocol) note(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
}

// Calls is every call the session made of the driver and its child, in
// order: "begin", "send", "interrupt", "reply", "close_input", "stop".
func (f *FakeProtocol) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Sent is every message Send was handed, in order.
func (f *FakeProtocol) Sent() []way.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// Replies is every verdict Reply was handed, in order.
func (f *FakeProtocol) Replies() []FakeReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies)
}

// Await waits until ok holds of the calls made so far, or ctx ends
// (false then): it is looked at again at each call.
func (f *FakeProtocol) Await(ctx context.Context, ok func(calls []string) bool) bool {
	for {
		f.mu.Lock()
		calls, changed := slices.Clone(f.calls), f.changed
		f.mu.Unlock()
		if ok(calls) {
			return true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

// Count is how many times calls holds call.
func Count(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}

// Name is ProtocolName.
func (f *FakeProtocol) Name() string {
	if f.ProtocolName == "" {
		return "fake-protocol"
	}
	return f.ProtocolName
}

// Verbatim is IsVerbatim.
func (f *FakeProtocol) Verbatim() bool { return f.IsVerbatim }

// Begin answers BeginErr.
func (f *FakeProtocol) Begin(context.Context) error {
	f.note("begin")
	return f.BeginErr
}

// Send records m and answers SendErr.
func (f *FakeProtocol) Send(_ context.Context, m way.Message) error {
	if f.BeforeSend != nil {
		f.BeforeSend(m)
	}
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	f.note("send")
	return f.SendErr
}

// Interrupt answers InterruptErr.
func (f *FakeProtocol) Interrupt(context.Context) error {
	f.note("interrupt")
	return f.InterruptErr
}

// Reply records the verdict.
func (f *FakeProtocol) Reply(_ context.Context, ref string, v way.Verdict) error {
	f.mu.Lock()
	f.replies = append(f.replies, FakeReply{Ref: ref, Verdict: v})
	f.mu.Unlock()
	f.note("reply")
	return nil
}

// CloseInput records the call.
func (f *FakeProtocol) CloseInput() error {
	f.note("close_input")
	return nil
}

// Read hands the session each event a test hands over, in order, until
// the output ends (End, Fail, the child's end): io.EOF, or Fail's error.
func (f *FakeProtocol) Read(ctx context.Context, e way.ProtocolEvents) error {
	f.readOne.Do(func() { close(f.reading) })
	for {
		select {
		case ev := <-f.events:
			ev.fn(e)
			close(ev.done)
		case <-f.ended:
			f.mu.Lock()
			err := f.readErr
			f.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Reading is closed once the session reads the driver's output.
func (f *FakeProtocol) Reading() <-chan struct{} { return f.reading }

// End ends the driver's output (io.EOF), as the agent's exit does.
func (f *FakeProtocol) End() { f.fail(nil) }

// Fail ends the driver's output with err: a line it could not read.
func (f *FakeProtocol) Fail(err error) { f.fail(err) }

func (f *FakeProtocol) fail(err error) {
	f.endOnce.Do(func() {
		f.mu.Lock()
		f.readErr = err
		f.mu.Unlock()
		close(f.ended)
	})
}

// Hand hands fn the session's events, on the session's read, and returns
// once the session has applied what fn told it: true then, false when the
// output had ended before the session took it.
func (f *FakeProtocol) Hand(fn func(e way.ProtocolEvents)) bool {
	ev := fakeEvent{fn: fn, done: make(chan struct{})}
	select {
	case f.events <- ev:
	case <-f.ended:
		return false
	}
	<-ev.done
	return true
}

// The events, each handed over as Hand does.

func (f *FakeProtocol) Conversation(id string) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.Conversation(id) })
}
func (f *FakeProtocol) Accepted(ref string) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.Accepted(ref) })
}
func (f *FakeProtocol) TurnStarted(turn string) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.TurnStarted(turn) })
}
func (f *FakeProtocol) Text(ref, delta string, final bool) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.Text(ref, delta, final) })
}
func (f *FakeProtocol) ToolStarted(t way.ToolStep) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.ToolStarted(t) })
}
func (f *FakeProtocol) ToolEnded(ref string, r way.ToolResult) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.ToolEnded(ref, r) })
}
func (f *FakeProtocol) FileChanged(c way.FileChange) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.FileChanged(c) })
}
func (f *FakeProtocol) Permission(r way.PermissionRequest) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.Permission(r) })
}
func (f *FakeProtocol) PermissionWithdrawn(ref string) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.PermissionWithdrawn(ref) })
}
func (f *FakeProtocol) Background(running bool) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.Background(running) })
}
func (f *FakeProtocol) TurnEnded(outcome string, u way.Usage) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.TurnEnded(outcome, u) })
}
func (f *FakeProtocol) Notice(code, words string) bool {
	return f.Hand(func(e way.ProtocolEvents) { e.Notice(code, words) })
}

// FakeChild is a FakeProtocol's child (session.Child): its Stop, or End,
// ends it and the driver's output.
type FakeChild struct {
	f    *FakeProtocol
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	code int
}

// Done is closed once the child has ended.
func (c *FakeChild) Done() <-chan struct{} { return c.done }

// Exit is the code End was given, -1 after Stop.
func (c *FakeChild) Exit() pty.ExitStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return pty.ExitStatus{Code: c.code}
}

// Stop records the call and ends the child, code -1.
func (c *FakeChild) Stop(context.Context, time.Duration) error {
	c.f.note("stop")
	c.end(-1)
	return nil
}

// End makes the child exit by itself with code.
func (c *FakeChild) End(code int) { c.end(code) }

func (c *FakeChild) end(code int) {
	c.once.Do(func() {
		c.mu.Lock()
		c.code = code
		c.mu.Unlock()
		close(c.done)
		c.f.End()
	})
}
