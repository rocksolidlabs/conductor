package ext

import (
	"context"
	"io"
	"os"
	"time"
)

// Runtime is where the processes of Conductor's sessions and of a managed
// run's trees run: native processes on this machine, as the user, unless a
// registered Runtime takes the managed run (docs/plans/runtime.md).
//
// The free default is ext/local's Native: pty.Start for a terminal,
// internal/proc for a bounded command and a service, a probe and a port of
// this machine's loopback, every one of them what Conductor does without a
// Runtime. A registered Runtime is offered managed runs alone, and takes
// one or declines it (Takes); a session a person starts or resumes, a
// legacy crew's members, a hosted session and every managed run it
// declines start natively.
//
// A Runtime that needs the server (to reach a Host, or the directory the
// server gives it) implements Starter beside Runtime, which is why the
// terminal's launch is StartTerminal and not Start: until its Start has
// succeeded, its slot answers the free default, so Takes is not asked and
// a run made meanwhile runs natively.
//
// Ownership: until StartTerminal, Run or Serve has returned normally,
// whatever it began (a process, a guest) is the Runtime's; once it has
// returned a Terminal or a ServiceProcess, that is the caller's. A panic
// that leaves one of those methods, the Runtime's own or one raised by a
// callback it calls on the caller's goroutine (CommandSpec.Started), leaves
// nothing running: before the panic leaves, the Runtime ends what it began
// and reaps it, then lets the panic go on unchanged, swallowing no caller's
// panic. A CommandSpec.Log writer is called on a goroutine of the
// Runtime's, where no caller could recover a panic: it must not panic.
//
// The environment a Runtime receives is whole, as Conductor built it (the
// filter that keeps its tokens and blocked loaders out stays in
// Conductor), the session's agent token among it, which the agent uses to
// reach the server over the network: a Runtime logs and keeps none of it.
//
// The types of the Runtime's methods are in-process Go types (a writer, a
// callback, an open directory), not wire records: no hosted service
// implements a Runtime.
type Runtime interface {
	Provider
	// Where names the Runtime, as the ledger's evidence rows record it:
	// constant for the Runtime's life, valid (Where.Valid), and WhereNative
	// for the free default alone.
	Where() Where
	// Takes answers, from the ref alone, with no I/O and no wait, whether
	// this Runtime runs the managed run the ref names ({Run}). It is asked
	// once for a run, when the run is made or continued, within TakesWait;
	// a Runtime that cannot run one now (its machine's KVM gone) declines,
	// and the run runs natively.
	Takes(ref ProcessRef) bool
	// StartTerminal starts s.Argv in DirFile's directory with exactly s.Env
	// on a terminal of s.Cols by s.Rows. Its context bounds the start, never
	// the process's life: a context already ended when the call is made is
	// answered with its error and nothing is started; one that ends while
	// the start is under way is answered one of two ways, never both,
	// decided by a commitment point of the Runtime's own: before it, the
	// start is abandoned, whatever the Runtime began ended and reaped
	// before the call returns, and the context's error returned; past it,
	// the start completes and its Terminal is returned, the caller's from
	// then on whether or not the context has ended since. s.DirFile is lent
	// for the call: the Runtime neither closes it nor keeps it once
	// StartTerminal returns.
	StartTerminal(ctx context.Context, s TerminalSpec) (Terminal, error)
	// Run runs one bounded command with internal/proc's semantics: an argv,
	// never a shell; a timeout it requires, at most MaxCommandTimeout; the
	// output's head and tail; the log within LogLimit; Started called once
	// the process exists and before its output is read; what the command
	// left in its process group ended with it. A command that does not
	// start is a CommandResult with Exit -1 and Err saying why.
	Run(ctx context.Context, s CommandSpec) CommandResult
	// Serve starts a command that runs until it is stopped (a tree's
	// service), as Run starts one, and returns once its process exists:
	// s.Timeout, when given, bounds the start alone, and ctx's end ends the
	// service. A command that could not start is an error, with nothing
	// left running.
	Serve(ctx context.Context, s CommandSpec) (ServiceProcess, error)
	// Probe makes one try of a readiness probe over HTTP from where the
	// tree's services listen: one GET of s.URL within s.Interval, no
	// redirect followed (a 3xx is the try's answer), the body read nothing
	// of. status is the answer's; err a try that got none.
	Probe(ctx context.Context, s ProbeSpec) (status int, err error)
	// Port answers a port number free where the tree the ref names runs,
	// for one of its services to bind.
	Port(ctx context.Context, ref ProcessRef) (int, error)
}

// TakesWait is the most the server waits for a Runtime's Takes (the
// guard): an answer past it, an error or a panic is a run declined, which
// runs natively.
const TakesWait = 100 * time.Millisecond

// StopKillWait is the most a Terminal's Stop waits for its process after
// forcing it: Stop returns within its grace plus StopKillWait.
const StopKillWait = 5 * time.Second

// MaxCommandTimeout bounds CommandSpec.Timeout: internal/proc's MaxTimeout.
const MaxCommandTimeout = 2 * time.Hour

// ProcessRef is what a process is for. A session's terminal names its
// session, and a managed attempt's also its run, task, attempt number and
// tree ("worktree"); a legacy crew member's its run alone beside the
// session; a check names its attempt and the tree "checkout"; a project
// manifest's step its attempt and its tree's kind; a prerequisite its run
// alone; a probe and a port their attempt and tree. The native Runtime
// ignores it; a Runtime may group an attempt's processes by it.
type ProcessRef struct {
	Session string
	Run     string
	Task    string
	Attempt int
	Tree    string
}

// TerminalSpec is a terminal process to start (StartTerminal).
type TerminalSpec struct {
	// Argv is the program and its arguments, run as they are: never a
	// shell line.
	Argv []string
	// Dir is the working directory's path, and DirFile the directory
	// Conductor opened and checked: the process starts in DirFile's
	// directory (natively, on Linux by its descriptor; elsewhere by Dir,
	// checked to still name it). DirFile is lent for the call.
	Dir     string
	DirFile *os.File
	// Env is the whole environment, as Conductor built it.
	Env []string
	// Cols and Rows are the terminal's size; 80 by 24 when zero.
	Cols, Rows uint16
	Ref        ProcessRef
}

// Terminal is a process StartTerminal started, with its terminal: what a
// session drives. Read and Write are the terminal's output and input.
type Terminal interface {
	io.Reader
	io.Writer
	// Resize changes the terminal's size.
	Resize(cols, rows uint16) error
	// Done is closed once the process has exited.
	Done() <-chan struct{}
	// Exit is how the process ended, once Done is closed.
	Exit() ExitStatus
	// Stop ends the process StartTerminal started, its leader. One that has
	// exited is not signalled: Stop closes the terminal and returns nil.
	// Otherwise it asks the process's group to end (natively SIGTERM to the
	// group), waits for the leader's exit until grace passes or ctx ends,
	// then forces the group (SIGKILL to the group) and waits at most
	// StopKillWait for the leader to be reaped: nil once it has been, an
	// error if not. It closes the terminal in every case and returns within
	// grace plus StopKillWait. It promises nothing of a descendant that
	// outlives the leader (one that ignores the ask and the hangup when the
	// leader exits on the ask, or one left by a leader that exited on its
	// own); the force reaches every member still in the group when it is
	// sent.
	Stop(ctx context.Context, grace time.Duration) error
	// Process is the process as the Runtime reports it, recorded and
	// logged, never acted on.
	Process() ProcessInfo
}

// ExitStatus is how a process ended: its exit code (128 plus the signal's
// number for one a signal ended, natively), the signal's name, and Err,
// what went wrong beyond the exit status ("" for nothing).
type ExitStatus struct {
	Code   int
	Signal string
	Err    string
}

// ProcessInfo is a process as a Runtime reports it: its pid, its process
// group and when it started. It is recorded and logged, never acted on.
type ProcessInfo struct {
	PID       int
	Group     int
	StartedAt time.Time
}

// CommandSpec is a bounded command (Run) or a service (Serve): internal/
// proc's bounds, with the environment whole.
type CommandSpec struct {
	// Argv is the program and its arguments, run as they are: never a
	// shell line.
	Argv []string
	// Dir is the working directory, absolute.
	Dir string
	// Env is the whole environment, as Conductor built it (internal/proc's
	// Prepare): the command gets exactly it.
	Env []string
	// Timeout is required by Run, at most MaxCommandTimeout: the process
	// group is ended past it. A service's, when given, bounds its start.
	Timeout time.Duration
	// Head and Tail are the bytes of output kept in memory, the first and
	// the last; the Runtime's defaults when zero.
	Head, Tail int
	// Log, when set, gets the output as it comes, at most LogLimit bytes in
	// all (the Runtime's default when zero), on a goroutine of the
	// Runtime's: it must not panic.
	Log      io.Writer
	LogLimit int64
	// Started, when set, is called once the process exists and before any
	// of its output is read, on the caller's goroutine.
	Started func(ProcessInfo)
	Ref     ProcessRef
}

// CommandResult is what became of a command: internal/proc's Result, field
// for field.
type CommandResult struct {
	// Process is set once the process started.
	Process ProcessInfo
	// Exit is the exit status; -1 when the process was ended by a signal
	// (Signal says which) or never started (Err says why).
	Exit   int
	Signal string
	// Head and Tail are the output's first and last bytes, stdout and
	// stderr together; Total is how many bytes there were, and Truncated
	// says Head and Tail do not hold them all.
	Head, Tail []byte
	Total      int64
	Truncated  bool
	// Started and Duration are when the process started and how long the
	// command ran from there.
	Started  time.Time
	Duration time.Duration
	// TimedOut says Timeout passed; Canceled, that the caller's context
	// ended first; Killed, that something the process left behind still
	// held its output when the process exited, and went with the group.
	TimedOut, Canceled, Killed bool
	// Err says why the command did not start, or what went wrong beyond its
	// exit status; LogErr why the log is short. "" for nothing.
	Err, LogErr string
}

// ServiceProcess is a service Serve started.
type ServiceProcess interface {
	// Done is closed once the service has exited and its end is read.
	Done() <-chan struct{}
	// Result is what became of the service once Done is closed; before, its
	// process alone, with Exit -1.
	Result() CommandResult
	// Stop ends the service: its group asked to end, then forced after the
	// Runtime's grace or once ctx ends; it returns once the service is
	// done. It may be called more than once.
	Stop(ctx context.Context) error
}

// ProbeSpec is one try of a readiness probe over HTTP (Probe): the URL and
// the try's bound.
type ProbeSpec struct {
	URL      string
	Interval time.Duration
	Ref      ProcessRef
}

// Where names a runtime, as the ledger's evidence rows record it.
type Where string

// WhereNative is the native runtime: a PTY for the attempt and
// internal/proc for its checks, on the machine that runs the session. Only
// the free default reports it (RegisterRuntime refuses it), so a row that
// says native was run by the native code.
const WhereNative Where = "native"

// MaxWhere bounds a Where, in bytes.
const MaxWhere = 32

// Valid checks w: 1 to MaxWhere of a-z, 0-9 and '-', beginning with a
// letter.
func (w Where) Valid() error {
	if !slug(string(w), MaxWhere, true) {
		return invalid("where", "%q is not 1 to %d of a-z, 0-9 and '-' beginning with a letter", string(w), MaxWhere)
	}
	return nil
}
