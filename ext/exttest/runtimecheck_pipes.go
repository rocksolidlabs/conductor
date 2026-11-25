package exttest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// The checks of a Runtime that is an ext.PipeStarter too (RuntimeExec):
//
//   - StartPipes starts in the directory given with exactly Env and its
//     argv as it is; what is written to Stdin reaches the child, Stdout is
//     what it prints and never its standard error, and its exit reaches the
//     caller; DirFile is the caller's to close once it returns.
//   - A context ended before the call is answered with its error and no
//     Pipes; one that ends while the start is under way (once StartPipes
//     has looked at it once, twice or three times) with one of the two
//     answers the contract allows, Pipes returned being a started child.
//   - Pipes' Stop asks (a helper that exits on the ask: nil, exited 0),
//     forces past the grace (a helper that ignores the ask, no grace: nil),
//     and signals nothing after an exit (nil); a nil answered once the
//     child has been reaped, Done closed the moment Stop returns.
//
// Each call is bounded and recovered as every check's is, Pipes handed
// back after their bound are stopped, and a DirFile is closed only once the
// call has unwound.

// pipesChecks runs the pipes' checks on ps.
func (x *execCheck) pipesChecks(ps ext.PipeStarter) {
	x.pipes(ps)
	x.pipesUnderEndedContexts(ps)
	x.pipeStops(ps)
}

type startedPipes struct {
	p      ext.Pipes
	err    error
	closed error // the DirFile's Close
}

// startPipes calls StartPipes with s, its DirFile the directory s.Dir
// opened and closed by the check once the call has unwound: a Runtime that
// closed it is told so. ok false when the call did not return; Pipes it
// returns later are stopped.
func (x *execCheck) startPipes(ps ext.PipeStarter, ctx context.Context, what string, s ext.PipeSpec) (ext.Pipes, error, bool) {
	x.c.t.Helper()
	var f *os.File
	if s.Dir != "" {
		var err error
		if f, err = os.Open(s.Dir); err != nil {
			x.errorf("opening the directory for %s: %v", what, err)
			return nil, nil, false
		}
		s.DirFile = f
	}
	a, ok := owned(x, what, x.wait, func() (a startedPipes) {
		if f != nil {
			// The Runtime may use what it was lent until its call unwinds.
			defer func() { a.closed = f.Close() }()
		}
		a.p, a.err = ps.StartPipes(ctx, s)
		return a
	}, func(a startedPipes) { stopLatePipes(a.p) })
	if ok && errors.Is(a.closed, os.ErrClosed) {
		x.errorf("%s closed the DirFile it was lent: the caller closes it once StartPipes returns", what)
	}
	return a.p, a.err, ok
}

// stopLatePipes stops Pipes handed back after their call's bound.
func stopLatePipes(p ext.Pipes) {
	if p != nil {
		p.Stop(context.Background(), 0)
	}
}

// cleanupPipes stops Pipes a check is done with, saying nothing of how.
func (x *execCheck) cleanupPipes(p ext.Pipes) {
	if p == nil {
		return
	}
	bounded(x, "the cleanup's Pipes.Stop", ext.StopKillWait+x.wait, func() error { return p.Stop(context.Background(), 0) })
}

// stdout asks the child's Stdout once and reads it to its end: ok false,
// said, when the call did not return or answered none.
func (x *execCheck) stdout(p ext.Pipes, what string) (*screen, bool) {
	x.c.t.Helper()
	r, ok := bounded(x, "Pipes.Stdout of "+what, x.wait, p.Stdout)
	if ok && r == nil {
		x.errorf("Pipes.Stdout of %s answered nil: it is the child's standard output", what)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	return readOut(r), true
}

// pipes checks a child's start, its directory, its environment, its
// input, its output apart from its standard error, and its exit.
func (x *execCheck) pipes(ps ext.PipeStarter) {
	dir, mark := x.dir()
	env := x.env("pipes")
	args := []string{"a;b", "$HOME"}
	p, err, ok := x.startPipes(ps, context.Background(), "StartPipes", ext.PipeSpec{
		Argv: x.argv("pipes", args...), Dir: dir, Env: env, Ref: ext.ProcessRef{Session: "exttest-pipes"},
	})
	if !ok {
		return
	}
	if err != nil || p == nil {
		x.errorf("StartPipes of the helper's pipes mode answered %v, %v: want Pipes", p, err)
		x.cleanupPipes(p)
		return
	}
	defer x.cleanupPipes(p)
	s, ok := x.stdout(p, "the pipes' helper")
	if !ok || !x.await(s, "exttest-ready", "the pipes' helper") {
		return
	}
	x.report(s.text(), "StartPipes", mark, env, args)
	if _, ok := bounded(x, "Pipes.Stdin's Write", x.wait, func() error {
		in := p.Stdin()
		if in == nil {
			x.c.found("Pipes.Stdin answered nil: it is the child's standard input")
			return nil
		}
		if _, err := io.WriteString(in, "hello\n"); err != nil {
			x.c.found("Pipes.Stdin's Write answered %v", err)
		}
		return nil
	}); !ok {
		return
	}
	if !x.await(s, "exttest-got:hello", "the input reaching the child") {
		return
	}
	if strings.Contains(s.text(), "exttest-stderr") {
		x.errorf("Pipes.Stdout holds what the child wrote to its standard error: it is the child's standard output alone")
	}
	done, ok := x.doneOf("Pipes.Done of the pipes' helper", p.Done)
	if !ok || !x.awaitDone(done, "the pipes' helper, which exits 3 once it has read a line") {
		return
	}
	if st, ok := bounded(x, "Pipes.Exit of the pipes' helper", x.wait, p.Exit); ok && (st.Code != 3 || st.Signal != "") {
		x.errorf("the pipes' helper exited 3, and Exit answered %+v", st)
	}
}

// pipesUnderEndedContexts checks StartPipes under a context ended before
// the call, and under one that ends while the start is under way.
func (x *execCheck) pipesUnderEndedContexts(ps ext.PipeStarter) {
	dir, _ := x.dir()
	spec := func(check string) ext.PipeSpec {
		return ext.PipeSpec{Argv: x.argv("hold"), Dir: dir, Env: x.env(check), Ref: ext.ProcessRef{Session: "exttest-pipes-" + check}}
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	p, err, ok := x.startPipes(ps, ended, "StartPipes under an ended context", spec("ended"))
	if ok {
		if p != nil {
			x.errorf("StartPipes under a context ended before the call returned Pipes: it is answered with the context's error and nothing is started")
			x.cleanupPipes(p)
		}
		if !errors.Is(err, context.Canceled) {
			x.errorf("StartPipes under a context ended before the call answered %v, want its error (context.Canceled)", err)
		}
	}
	for looks := 1; looks <= 3; looks++ {
		ctx := newLookCancel(looks)
		what := fmt.Sprintf("StartPipes under a context that ends after %d look(s) at it", looks)
		p, err, ok := x.startPipes(ps, ctx, what, spec("ending"))
		ctx.cancel()
		if !ok {
			continue
		}
		switch {
		case p != nil && err != nil:
			x.errorf("%s answered Pipes and an error (%v): one of the two, never both", what, err)
			x.cleanupPipes(p)
		case p != nil:
			// Past its commitment point: a started child, the caller's.
			if s, ok := x.stdout(p, what+": the Pipes it returned"); ok {
				x.await(s, "exttest-ready", what+": the Pipes it returned, a started child,")
			}
			if err, ok := bounded(x, what+": the Pipes' Stop", ext.StopKillWait+x.wait, func() error { return p.Stop(context.Background(), 0) }); ok && err != nil {
				x.errorf("%s: the Pipes it returned did not stop: %v", what, err)
			}
		case errors.Is(err, context.Canceled):
			// Before its commitment point: abandoned.
		default:
			x.errorf("%s answered %v: want the context's error, or Pipes", what, err)
		}
	}
}

// pipeStops checks Pipes' Stop: asked, forced past the grace, and after an
// exit.
func (x *execCheck) pipeStops(ps ext.PipeStarter) {
	dir, _ := x.dir()
	begin := func(mode, check string) (ext.Pipes, <-chan struct{}) {
		argv := x.argv(mode)
		if mode == "exit" {
			argv = x.argv(mode, "0")
		}
		p, err, ok := x.startPipes(ps, context.Background(), "StartPipes for "+check, ext.PipeSpec{
			Argv: argv, Dir: dir, Env: x.env(check), Ref: ext.ProcessRef{Session: "exttest-pipes-" + check},
		})
		if !ok {
			return nil, nil
		}
		if err != nil || p == nil {
			x.errorf("StartPipes for %s answered %v, %v: want Pipes", check, p, err)
			x.cleanupPipes(p)
			return nil, nil
		}
		done, ok := x.doneOf("Pipes.Done of the helper for "+check, p.Done)
		if !ok {
			x.cleanupPipes(p)
			return nil, nil
		}
		if mode != "exit" {
			if s, ok := x.stdout(p, "the helper for "+check); !ok || !x.await(s, "exttest-ready", "the helper for "+check) {
				x.cleanupPipes(p)
				return nil, nil
			}
		}
		return p, done
	}
	// stop calls p's Stop and looks at done, its Done, as soon as it
	// returns, as stopNow does a Terminal's.
	stop := func(p ext.Pipes, done <-chan struct{}, grace time.Duration) func() stopAnswer {
		return func() stopAnswer {
			err := p.Stop(context.Background(), grace)
			select {
			case <-done:
				return stopAnswer{err, true}
			default:
				return stopAnswer{err, false}
			}
		}
	}

	// Asked: the helper exits 0 on the ask, within a grace it never reaches.
	if p, done := begin("asked", "pipes-stop-asked"); p != nil {
		const what = "Pipes.Stop of a child that exits on the ask"
		a, ok := bounded(x, what, x.wait+ext.StopKillWait+x.wait, stop(p, done, x.wait))
		if ok {
			x.stopped(a, what)
		}
		if ok && a.err == nil && a.done {
			if st, ok := bounded(x, "Pipes.Exit of the helper for pipes-stop-asked", x.wait, p.Exit); ok && (st.Code != 0 || st.Signal != "") {
				x.errorf("Pipes.Stop did not ask the child to end before forcing it: the helper exits 0 on the ask, and ended %+v", st)
			}
		}
		x.cleanupPipes(p)
	}

	// Forced past the grace: none given.
	if p, done := begin("ignore", "pipes-stop-grace"); p != nil {
		const what = "Pipes.Stop with no grace of a child that ignores the ask"
		if a, ok := bounded(x, what, ext.StopKillWait+x.wait, stop(p, done, 0)); ok {
			x.stopped(a, what)
		}
		x.cleanupPipes(p)
	}

	// After an exit: nothing to signal.
	if p, done := begin("exit", "pipes-stop-exited"); p != nil {
		if x.awaitDone(done, "the pipes' helper that exits at once") {
			err, ok := bounded(x, "Pipes.Stop after the child exited", x.wait, func() error { return p.Stop(context.Background(), x.wait) })
			if ok && err != nil {
				x.errorf("Pipes.Stop after the child exited answered %v, want nil", err)
			}
		}
		x.cleanupPipes(p)
	}
}
