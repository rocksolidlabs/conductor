package local

import (
	"context"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proc"
	"github.com/rocksolidlabs/conductor/internal/pty"
)

// Native is the free Runtime: native processes on this machine, as the
// user, which is what Conductor does without a Runtime. Its terminal is
// pty.Start's, its commands and services are internal/proc's Run and
// Serve, its probe is proc.Probe and its port proc.FreePort, each called
// as Conductor calls them without the seam. Its Name is Name, its Where
// ext.WhereNative, and it takes every run.
func Native() ext.Runtime { return native{} }

var _ ext.Runtime = native{}

// native is the free Runtime. seam is nil outside ext/local's tests.
type native struct{ seam *startSeam }

// startSeam is the tests' way into StartTerminal: gate runs before the
// second check of the context, start in place of pty.Start (counting its
// calls, or ending the context while it runs), and after once it has
// returned. Each is nil outside the tests but start, which is pty.Start
// when nil.
type startSeam struct {
	gate  func()
	start func(pty.Spec) (*pty.Process, error)
	after func()
}

// Name answers Name.
func (native) Name() string { return Name }

// Where answers ext.WhereNative.
func (native) Where() ext.Where { return ext.WhereNative }

// Takes answers true for every ref: the native Runtime runs every run.
func (native) Takes(ext.ProcessRef) bool { return true }

// StartTerminal is pty.Start behind two checks of the context, the
// second its commitment point: an ended context at either is answered
// with its error and no pty.Start made. pty.Start takes no context and
// cannot be interrupted (a fork and an exec, bounded by the kernel), so a
// context that ends while it runs, or after, changes nothing: its
// Terminal is returned, the caller's. pty.Start is done with s.DirFile
// when it returns, and its errors are returned as they are.
func (r native) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := pty.Start
	if r.seam != nil {
		if r.seam.gate != nil {
			r.seam.gate()
		}
		if r.seam.start != nil {
			start = r.seam.start
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := start(pty.Spec{Argv: s.Argv, Dir: s.Dir, DirFile: s.DirFile, Env: s.Env, Cols: s.Cols, Rows: s.Rows})
	if err != nil {
		return nil, err
	}
	// From here the process is this call's until it returns: a panic on the
	// way (the seam's hook, in the tests) stops it with no grace, which
	// kills its group and reaps it, and goes on unchanged.
	returned := false
	defer func() {
		if !returned {
			p.Stop(context.Background(), 0)
		}
	}()
	t := &nativeTerminal{p: p, started: time.Now()}
	if r.seam != nil && r.seam.after != nil {
		r.seam.after()
	}
	returned = true
	return t, nil
}

// Run is proc.Run with the environment whole (proc.Spec.Environ).
func (native) Run(ctx context.Context, s ext.CommandSpec) ext.CommandResult {
	return commandResult(proc.Run(ctx, procSpec(s)))
}

// Serve is proc.Serve with the environment whole (proc.Spec.Environ).
func (native) Serve(ctx context.Context, s ext.CommandSpec) (ext.ServiceProcess, error) {
	svc, err := proc.Serve(ctx, procSpec(s))
	if err != nil {
		return nil, err
	}
	return nativeService{svc}, nil
}

// Probe is proc.Probe.
func (native) Probe(ctx context.Context, s ext.ProbeSpec) (int, error) {
	return proc.Probe(ctx, s.URL, s.Interval)
}

// Port is proc.FreePort.
func (native) Port(ctx context.Context, _ ext.ProcessRef) (int, error) {
	return proc.FreePort(ctx)
}

// procSpec is s as proc runs it: the environment whole, exactly s.Env (an
// empty one when s has none, never the server's own).
func procSpec(s ext.CommandSpec) proc.Spec {
	env := s.Env
	if env == nil {
		env = []string{}
	}
	ps := proc.Spec{Argv: s.Argv, Dir: s.Dir, Environ: env, Timeout: s.Timeout,
		Head: s.Head, Tail: s.Tail, Log: s.Log, LogLimit: s.LogLimit}
	if s.Started != nil {
		ps.Started = func(p proc.Process) { s.Started(processInfo(p)) }
	}
	return ps
}

func processInfo(p proc.Process) ext.ProcessInfo {
	return ext.ProcessInfo{PID: p.PID, Group: p.Group, StartedAt: p.StartedAt}
}

// commandResult is r field for field.
func commandResult(r proc.Result) ext.CommandResult {
	return ext.CommandResult{
		Process: processInfo(r.Process),
		Exit:    r.Exit, Signal: r.Signal,
		Head: r.Head, Tail: r.Tail, Total: r.Total, Truncated: r.Truncated,
		Started: r.Started, Duration: r.Duration,
		TimedOut: r.TimedOut, Canceled: r.Canceled, Killed: r.Killed,
		Err: r.Err, LogErr: r.LogErr,
	}
}

// nativeTerminal is a pty.Process as an ext.Terminal: its group is its own
// pid (creack/pty's setsid), and it started when pty.Start returned.
type nativeTerminal struct {
	p       *pty.Process
	started time.Time
}

func (t *nativeTerminal) Read(b []byte) (int, error)     { return t.p.Read(b) }
func (t *nativeTerminal) Write(b []byte) (int, error)    { return t.p.Write(b) }
func (t *nativeTerminal) Resize(cols, rows uint16) error { return t.p.Resize(cols, rows) }
func (t *nativeTerminal) Done() <-chan struct{}          { return t.p.Done() }

// Exit is the process's exit status, its error as words.
func (t *nativeTerminal) Exit() ext.ExitStatus {
	st := t.p.Exit()
	out := ext.ExitStatus{Code: st.Code, Signal: st.Signal}
	if st.Err != nil {
		out.Err = st.Err.Error()
	}
	return out
}

// Stop is pty.Process.Stop: SIGTERM to the group, SIGKILL after grace or
// once ctx ends, the leader waited for at most pty.KillWait after it.
func (t *nativeTerminal) Stop(ctx context.Context, grace time.Duration) error {
	return t.p.Stop(ctx, grace)
}

// Process is the pid, the group (the same number) and when it started.
func (t *nativeTerminal) Process() ext.ProcessInfo {
	pid := t.p.PID()
	return ext.ProcessInfo{PID: pid, Group: pid, StartedAt: t.started}
}

// nativeService is a proc.Service as an ext.ServiceProcess.
type nativeService struct{ svc *proc.Service }

func (s nativeService) Done() <-chan struct{}          { return s.svc.Done() }
func (s nativeService) Result() ext.CommandResult      { return commandResult(s.svc.Result()) }
func (s nativeService) Stop(ctx context.Context) error { return s.svc.Stop(ctx) }
