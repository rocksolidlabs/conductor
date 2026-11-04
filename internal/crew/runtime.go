package crew

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proc"
)

// A managed run's trees run through a Runtime (docs/plans/runtime.md,
// Program item C10): its prerequisites, its attempts' trees' steps,
// services, probes and ports, and their checks, every one through the
// executor of the run's Runtime (execFor). The engine offers its managed
// runs one Runtime (SetRuntime; the native one, local.Native, until it is
// set), and keeps the native one beside it for what the offered one does
// not take: a run's Runtime is chosen once, when the run is made or
// continued (addRun), the offered one when its Takes answers true for the
// run's ref ({Run}), the native one otherwise, and it is the run's for its
// life in this engine. A legacy crew's run is offered nothing. Nothing of a
// run's trees reaches the machine but through its Runtime.

// SetRuntime gives the engine the Runtime it offers its managed runs (the
// server's, from its extensions; local.Native until it is set), beside
// SetCheckConcurrency and SetEnvPassthrough and set like them before the
// first launch. It refuses nil, a Runtime whose Where is not valid
// (ext.Where.Valid), and any call once the engine has made a run, a
// launched, planned or continued one, so every managed run of an engine is
// offered the same Runtime.
func (e *Engine) SetRuntime(rt ext.Runtime) error {
	if rt == nil {
		return errors.New("crew: no Runtime")
	}
	if err := rt.Where().Valid(); err != nil {
		return fmt.Errorf("crew: the Runtime %q: %w", rt.Name(), err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.madeRun {
		return errors.New("crew: the Runtime is set before the engine's first run, and it has made one")
	}
	e.offered, e.exec = rt, execFor(rt)
	return nil
}

// runtimeFor chooses the Runtime of a run being made with the id given:
// for a managed run, the offered Runtime when its Takes answers true for
// {Run: id}, the native one otherwise; for a legacy run, the native one,
// with the offered asked nothing. taken says the offered one took it. It
// marks the engine as having made a run (SetRuntime refuses after), and
// asks Takes holding no lock of the engine's.
func (e *Engine) runtimeFor(managed bool, id string) (rt ext.Runtime, taken bool) {
	e.mu.Lock()
	offered := e.offered
	e.madeRun = true
	e.mu.Unlock()
	if managed && offered.Takes(ext.ProcessRef{Run: id}) {
		return offered, true
	}
	return e.native, false
}

// execOf is the executor of r's Runtime: the engine's (e.exec, the
// offered Runtime's, whose functions a test may replace) for a run the
// offered Runtime took, the native one's for any other.
func (e *Engine) execOf(r *run) treeExec {
	if r.taken {
		return e.exec
	}
	return execFor(e.native)
}

// procRef is what a process of r's is for (ext.ProcessRef): the run, and
// for one of attempt a's trees the task, the attempt's number and the
// tree's kind (ledger.TreeWorktree, ledger.TreeCheckout); a prerequisite's
// (a nil) names the run alone.
func procRef(r *run, a *attempt, kind string) ext.ProcessRef {
	ref := ext.ProcessRef{Run: r.id}
	if a != nil {
		ref.Task, ref.Attempt, ref.Tree = a.t.def.ID, a.n, kind
	}
	return ref
}

// execFor is rt as a tree's executor. Its run and serve build the
// command's environment with proc.Prepare, so the filter that keeps
// Conductor's tokens and the blocked loaders out is Conductor's whatever
// runs the command, and hand it to rt whole; a spec Prepare refuses is
// answered as proc.Run and proc.Serve answer it (Exit -1 and the same
// words; the same error), with rt asked nothing. A result comes back field
// for field (procResult).
func execFor(rt ext.Runtime) treeExec {
	return treeExec{
		run: func(ctx context.Context, ref ext.ProcessRef, s proc.Spec) proc.Result {
			env, err := proc.Prepare(s, false)
			if err != nil {
				return proc.Result{Exit: -1, Err: err.Error()}
			}
			return procResult(rt.Run(ctx, commandSpec(s, env, ref)))
		},
		serve: func(ctx context.Context, ref ext.ProcessRef, s proc.Spec) (service, error) {
			env, err := proc.Prepare(s, true)
			if err != nil {
				return nil, err
			}
			sp, err := rt.Serve(ctx, commandSpec(s, env, ref))
			if err != nil {
				return nil, err
			}
			return &runtimeService{sp: sp}, nil
		},
		probe: func(ctx context.Context, ref ext.ProcessRef, url string, interval time.Duration) (int, error) {
			return rt.Probe(ctx, ext.ProbeSpec{URL: url, Interval: interval, Ref: ref})
		},
		port: func(ctx context.Context, ref ext.ProcessRef) (int, error) {
			return rt.Port(ctx, ref)
		},
	}
}

// commandSpec is s as a Runtime runs it: its environment env, as
// proc.Prepare built it, and the ref.
func commandSpec(s proc.Spec, env []string, ref ext.ProcessRef) ext.CommandSpec {
	cs := ext.CommandSpec{Argv: s.Argv, Dir: s.Dir, Env: env, Timeout: s.Timeout, Head: s.Head, Tail: s.Tail, Log: s.Log, LogLimit: s.LogLimit, Ref: ref}
	if started := s.Started; started != nil {
		cs.Started = func(p ext.ProcessInfo) { started(proc.Process{PID: p.PID, Group: p.Group, StartedAt: p.StartedAt}) }
	}
	return cs
}

// procResult is r as the engine reads a command's end: proc.Result, field
// for field.
func procResult(r ext.CommandResult) proc.Result {
	return proc.Result{
		Process: proc.Process{PID: r.Process.PID, Group: r.Process.Group, StartedAt: r.Process.StartedAt},
		Exit:    r.Exit, Signal: r.Signal,
		Head: r.Head, Tail: r.Tail, Total: r.Total, Truncated: r.Truncated,
		Started: r.Started, Duration: r.Duration,
		TimedOut: r.TimedOut, Canceled: r.Canceled, Killed: r.Killed,
		Err: r.Err, LogErr: r.LogErr,
	}
}

// runtimeService is a Runtime's service as the engine holds one.
type runtimeService struct{ sp ext.ServiceProcess }

func (s *runtimeService) Done() <-chan struct{}          { return s.sp.Done() }
func (s *runtimeService) Result() proc.Result            { return procResult(s.sp.Result()) }
func (s *runtimeService) Stop(ctx context.Context) error { return s.sp.Stop(ctx) }
