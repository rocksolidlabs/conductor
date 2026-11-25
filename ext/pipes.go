package ext

import (
	"context"
	"io"
	"os"
	"time"
)

// PipeStarter is a Runtime that can start a long-lived child with its
// input and output as pipes: the way a session runs an agent through the
// agent's own documented non-interactive interface, beside its terminal
// (docs/plans/chat-or-terminal.md). It is optional beside Runtime, as
// Starter is: the native Runtime implements it, a registered Runtime may,
// and a Runtime that does not is never handed a pipes child. A registered
// one reaches the server behind its guard, which offers StartPipes only
// when the Runtime itself implements it.
//
// Ownership and panics are as Runtime's: until StartPipes has returned
// normally, whatever it began is the Runtime's, and a panic that leaves it
// leaves nothing running; once it has returned Pipes, they are the
// caller's. The environment it receives is whole, a session's agent token
// among it: a Runtime logs and keeps none of it.
type PipeStarter interface {
	// StartPipes starts s.Argv in DirFile's directory with exactly s.Env,
	// its standard input and output as pipes, its standard error read to
	// its end and dropped, in a process group of its own. Its context bounds
	// the start, never the child's life, with StartTerminal's commitment
	// rule: a context already ended when the call is made is answered with
	// its error and nothing is started; one that ends while the start is
	// under way is answered one of two ways, never both, decided by a
	// commitment point of the Runtime's own: before it, the start is
	// abandoned, whatever the Runtime began ended and reaped, and the
	// context's error returned; past it, Pipes are returned, the caller's.
	// s.DirFile is lent for the call: the Runtime neither closes it nor
	// keeps it once StartPipes returns.
	StartPipes(ctx context.Context, s PipeSpec) (Pipes, error)
}

// PipeSpec is a child to start with pipes (StartPipes): TerminalSpec's
// fields without a terminal's size.
type PipeSpec struct {
	// Argv is the program and its arguments, run as they are: never a
	// shell line.
	Argv []string
	// Dir is the working directory's path, and DirFile the directory
	// Conductor opened and checked: the child starts in DirFile's
	// directory (natively, on Linux by its descriptor; elsewhere by Dir,
	// checked to still name it). DirFile is lent for the call.
	Dir     string
	DirFile *os.File
	// Env is the whole environment, as Conductor built it.
	Env []string
	Ref ProcessRef
}

// Pipes is a child StartPipes started. Stdin and Stdout are its standard
// input and output; Done, Exit, Stop and Process are Terminal's.
type Pipes interface {
	// Stdin is the child's standard input: what is written reaches it, and
	// Close gives it the end of its input.
	Stdin() io.WriteCloser
	// Stdout is the child's standard output, never its standard error:
	// what it writes, as it writes it, until its end (io.EOF) once nothing
	// of the child holds it.
	Stdout() io.Reader
	// Done is closed once the child has exited and been reaped, what it
	// left in its process group having been sent SIGKILL first (natively);
	// it does not wait for those members to be gone.
	Done() <-chan struct{}
	// Exit is how the child ended, once Done is closed.
	Exit() ExitStatus
	// Stop ends the child as Terminal's Stop ends its process: nothing
	// signalled once it has exited; otherwise its group asked to end
	// (natively SIGTERM), the child waited for until grace passes or ctx
	// ends, then the group forced (SIGKILL) and the child waited for at most
	// StopKillWait: nil once it has been reaped, an error if not. It closes
	// the pipes in every case and returns within grace plus StopKillWait.
	// It may be called more than once, and from more than one goroutine.
	Stop(ctx context.Context, grace time.Duration) error
	// Process is the child as the Runtime reports it, recorded and logged,
	// never acted on.
	Process() ProcessInfo
}
