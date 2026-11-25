// Package way holds the types of a session's protocol way: the child, the
// protocol driver and what the driver tells the session. They are the
// session's own (package session names each of them, as session.Protocol,
// session.ProtocolEvents and the rest); they live here, a package that
// imports nothing of the session, so that the session's test kit
// (internal/session/sessiontest), which the session's own tests import,
// can implement a driver without importing the session.
package way

import (
	"context"
	"errors"
	"time"

	"github.com/rocksolidlabs/conductor/internal/pty"
)

// MaxProtocolLine bounds one line of a child's output: above the largest
// single tool result Claude Code documents. A driver reads a longer line
// to its end without keeping it, and says so with a Notice.
const MaxProtocolLine = 16 << 20

// NoInterruptWords are ErrNoInterrupt's.
const NoInterruptWords = "session: the agent offers no way to stop a turn and keep its conversation"

// ErrNoInterrupt is a driver's answer to Interrupt when its agent offers no
// interrupt it confirms: the session then ends the child and starts it
// again on the same conversation.
var ErrNoInterrupt = errors.New(NoInterruptWords)

// Child is a protocol way's process: the agent's own non-interactive
// interface, started with its input and output as pipes.
type Child interface {
	// Done is closed once the process has exited and been reaped.
	Done() <-chan struct{}
	// Exit is how it exited, once Done is closed.
	Exit() pty.ExitStatus
	// Stop ends it: its input closed first, grace to go by itself, then
	// its process group ended, as a terminal's Stop does.
	Stop(ctx context.Context, grace time.Duration) error
}

// Protocol is a protocol way's driver: one agent's documented structured
// interface over the child's pipes. The session calls it; it calls the
// session back only through ProtocolEvents, in the order the agent's
// output gave them.
type Protocol interface {
	// Name is the protocol's name from the catalog's closed set
	// ("claude-stream-json", later "codex-app-server", "acp-v1").
	Name() string
	// Verbatim says whether the protocol has a documented mark that the
	// agent takes a message's text as written (no @path expanded, no
	// /command run). Constant for the driver.
	Verbatim() bool
	// Begin writes the protocol's initialize request and returns nil once
	// the agent has answered it and its answer passed the driver's shape
	// check, or the error that says why not. It returns by ctx's end at
	// the latest. It never waits for anything the agent writes only once
	// a first message has arrived.
	Begin(ctx context.Context) error
	// Send writes one message as a new user turn, with the protocol's
	// as-written mark where Verbatim is true, and returns once its bytes
	// are written to the agent's input, or the write failed. A written
	// message is not a taken one: the agent's acknowledgement arrives as
	// ProtocolEvents.Accepted with m.Ref.
	Send(ctx context.Context, m Message) error
	// Interrupt asks the agent to end the turn it runs and keep the
	// conversation. It returns once the ask is written; the turn's end
	// arrives as ProtocolEvents.TurnEnded with outcome "interrupted".
	// ErrNoInterrupt says the agent offers none: the session then ends
	// the child and resumes the same conversation.
	Interrupt(ctx context.Context) error
	// Read reads the agent's output until it ends or ctx ends, and hands
	// each event to e in order. It returns io.EOF at the output's end. A
	// line past MaxProtocolLine is skipped to its end with a Notice; a
	// line that is not the protocol's at all returns an error (the
	// session then ends the child). Read never waits on a person: a
	// request it meets is handed over and Read reads on.
	Read(ctx context.Context, e ProtocolEvents) error
	// Reply writes the session's verdict on an in-stream request (Codex,
	// the Agent Client Protocol) under the request's own id, or the
	// protocol's own cancel of it for a withdrawal. Called at most once
	// per request, on a goroutine of the session's.
	Reply(ctx context.Context, ref string, v Verdict) error
	// CloseInput ends the agent's input. Nothing is written after it.
	CloseInput() error
}

// ProtocolEvents is what a driver tells the session. Each call returns
// once the session has applied it (its state first, its viewers after),
// and none waits on a person.
type ProtocolEvents interface {
	// Conversation is the agent's own conversation id, as its stream names it.
	Conversation(id string)
	// Accepted says the agent took the message Send wrote under ref.
	Accepted(ref string)
	// TurnStarted says a turn began ("" when the agent names none).
	TurnStarted(agentTurn string)
	// Text is the agent's words, streamed; final closes ref.
	Text(ref string, delta string, final bool)
	// ToolStarted is a tool call announced, with its name and whole input.
	ToolStarted(t ToolStep)
	// ToolEnded is its result: status, exit code and output where the
	// agent gives them.
	ToolEnded(ref string, r ToolResult)
	// FileChanged is a file a tool changed: path, op, counts, diff where given.
	FileChanged(f FileChange)
	// Permission is an in-stream request (Codex, ACP); the verdict goes
	// back through Protocol.Reply.
	Permission(r PermissionRequest)
	// PermissionWithdrawn says the agent took back an in-stream request
	// it made.
	PermissionWithdrawn(ref string)
	// Background says work the agent started still runs after its turn
	// (or no longer does).
	Background(running bool)
	// TurnEnded is a turn's end: "done", "interrupted", "error", "limit".
	TurnEnded(outcome string, u Usage)
	// Notice is something the agent said that is none of the above.
	Notice(code, words string)
}

// Message is one message the session sends into the conversation.
type Message struct {
	// Ref is the session's id for it, which the agent's acknowledgement
	// names (ProtocolEvents.Accepted).
	Ref string
	// Text is the message as written.
	Text string
}

// ToolStep is a tool call the agent announced.
type ToolStep struct {
	// Ref is the agent's own id of the call; Parent the Ref of the step a
	// subagent's call belongs to, "" for none.
	Ref, Parent string
	// Name is the tool's name.
	Name string
	// Input is the call's whole input, as the agent sent it (JSON).
	Input []byte
	// Summary, when the driver gives one, is the command line of a
	// command or the path of a file tool; else the session makes one
	// from Input.
	Summary string
}

// Tool step statuses (ToolResult.Status).
const (
	ToolOK          = "ok"
	ToolError       = "error"
	ToolDenied      = "denied"
	ToolInterrupted = "interrupted"
)

// ToolResult is a tool call's result.
type ToolResult struct {
	// Status is one of the Tool* statuses.
	Status string
	// Exit is the command's exit code where the agent gives it.
	Exit *int
	// Output is the call's whole output as the agent gave it; Cut says the
	// driver could not keep all of it (a line past MaxProtocolLine).
	Output []byte
	Cut    bool
	// Duration is the call's time as the agent reports it; 0 when it
	// reports none, and the session measures it.
	Duration time.Duration
}

// File change ops (FileChange.Op).
const (
	FileCreate = "create"
	FileEdit   = "edit"
	FileDelete = "delete"
)

// FileChange is a file a tool changed, as the agent's stream says it.
type FileChange struct {
	// Tool is the Ref of the step that changed it.
	Tool string
	// Path is the file as the agent names it.
	Path string
	// Op is one of the File* ops.
	Op string
	// Added and Removed are the lines, as the tool's input gives them.
	Added, Removed int
	// Diff is the change where the agent gives one.
	Diff []byte
}

// PermissionRequest is an in-stream request for approval (Codex, the
// Agent Client Protocol).
type PermissionRequest struct {
	// Ref is the request's own id, which Reply answers under.
	Ref string
	// Call is the agent's id of the tool call it asks about.
	Call string
	// Tool is the tool's name and Input its whole input.
	Tool  string
	Input []byte
	// Cwd is the folder the request names, where it names one.
	Cwd string
}

// Verdict is the session's answer to an in-stream request.
type Verdict struct {
	// Allow answers allow once; otherwise deny.
	Allow bool
	// Cancel answers with the protocol's own cancel of the request (a
	// withdrawal), in place of a deny.
	Cancel bool
	// Message is what the agent is told with a deny.
	Message string
}

// Usage is what a turn cost, where the agent reports it.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	// CostUSD is the turn's cost in US dollars; 0 when not reported.
	CostUSD float64
}

// Start starts a protocol way's child and its driver, on the agent's
// conversation (its own id, "" for a new one).
type Start func(ctx context.Context, conversation string) (Child, Protocol, error)

// Enter, set by package session as it loads, puts the session local (a
// *session.Local) in the protocol way with start: the tests' way in, until
// a move between ways does it (sessiontest.EnterProtocol).
var Enter func(local any, start Start) error
