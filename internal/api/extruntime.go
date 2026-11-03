package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The Runtime (docs/plans/runtime.md, Program item C10): every process of
// a server session starts through one. The server keeps two, both set in
// New and never changed after: the native one (local.Native), which every
// session that is not an attempt of a managed run starts in (a person's,
// a resume, a legacy crew's member, an attempt of a run the offered one
// declined), and the one its engine offers managed runs (crew's
// SetRuntime), the extensions' Runtime: the registered one behind the
// guard, or, with none registered, the free default local.Fill put in the
// slot. An attempt's session starts in its run's Runtime, which its
// launch names (crew.LaunchSpec.Where).

// installRuntimes sets the server's two Runtimes from p, the extensions'
// set, and native, the test's stand-in for the native one (nil outside
// the tests). A registered Runtime is wrapped in the guard (guardRuntime);
// the free default is held as it is, since its Takes answers at once from
// no I/O and its process methods are what the server ran before the seam,
// so the guard would only add a goroutine to every run made.
func (s *Server) installRuntimes(p ext.Providers, native ext.Runtime) {
	s.native = native
	if s.native == nil {
		s.native = local.Native()
	}
	s.offered = p.Runtime
	if s.offered == nil {
		s.offered = local.Native()
	}
	if !slices.ContainsFunc(p.All, func(e ext.Entry) bool { return e.Kind == ext.KindRuntime }) {
		return
	}
	name := extNames(p)(ext.KindRuntime, 0, p.Runtime)
	s.offered = guardRuntime{s: s, name: name, where: runtimeWhere(p.Runtime), p: p.Runtime, ready: s.readyOf(name, p.Runtime)}
}

// runtimeWhere is r's Where, read once, as the registry read it when r
// registered (ext.RegisterRuntime refused one not valid): a Where that
// panics is "", which the engine's SetRuntime refuses, failing New.
func runtimeWhere(r ext.Runtime) (w ext.Where) {
	defer func() {
		if recover() != nil {
			w = ""
		}
	}()
	return r.Where()
}

// runtimeAt is the Runtime a session whose launch names where starts in:
// the native one for none or ext.WhereNative, the offered one for its own
// Where (an attempt of a run it took); false for any other, which no
// Runtime of the server's is.
func (s *Server) runtimeAt(where ext.Where) (ext.Runtime, bool) {
	switch where {
	case "", ext.WhereNative:
		return s.native, true
	case s.offered.Where():
		return s.offered, true
	}
	return nil, false
}

// processRefOf is what a server session's process is for (ext.ProcessRef):
// its session, and, from its crew reference, its run, and for an attempt
// its task, number and tree, the attempt's worktree.
func processRefOf(id string, crewRef *session.CrewRef) ext.ProcessRef {
	ref := ext.ProcessRef{Session: id}
	if crewRef != nil {
		ref.Run = crewRef.RunID
		if crewRef.Task != "" {
			ref.Task, ref.Attempt, ref.Tree = crewRef.Task, crewRef.Attempt, ledger.TreeWorktree
		}
	}
	return ref
}

// guardRuntime is a registered Runtime behind the guard. Takes goes
// through extCall within ext.TakesWait, its fallback false: a late,
// failing or panicking answer, or one asked before the Runtime's Start has
// succeeded, is a run declined, which runs natively. A process method has
// no wait and no fallback, since a process that started is not answered
// for by a default: its panic is recovered once the method has unwound, by
// when the contract has had the Runtime end and reap what it began, logged
// at error naming the Runtime, and answered as a refused start, or as a
// command's result with Exit -1 and the words. The Terminal and the
// ServiceProcess it answers are behind the guard too (guardProcess), since
// the server's goroutines call them for as long as the process lives. The
// panic's value is not quoted: the spec the Runtime was handed holds a
// session's agent token.
type guardRuntime struct {
	s     *Server
	name  string
	where ext.Where
	p     ext.Runtime
	ready *atomic.Bool
}

var _ ext.Runtime = guardRuntime{}

func (g guardRuntime) Name() string     { return g.name }
func (g guardRuntime) Where() ext.Where { return g.where }

// Takes answers the Runtime's Takes for ref within ext.TakesWait, false
// on any failure, and false before its Start has succeeded.
func (g guardRuntime) Takes(ref ext.ProcessRef) bool {
	if !live(g.ready) {
		return false
	}
	return extCall(context.Background(), g.s, ext.KindRuntime, g.name, ext.TakesWait, false, nil, func(context.Context) (bool, error) {
		return g.p.Takes(ref), nil
	})
}

// panicked logs a process method's panic at error and answers its words.
func (g guardRuntime) panicked(method string) string {
	g.s.log.Error("extension call failed: the Runtime panicked, and what it started is refused", "kind", ext.KindRuntime, "provider", g.name, "call", method, "failure", "panic")
	return fmt.Sprintf("the Runtime %s panicked in %s", g.name, method)
}

func (g guardRuntime) StartTerminal(ctx context.Context, s ext.TerminalSpec) (t ext.Terminal, err error) {
	defer func() {
		if recover() != nil {
			t, err = nil, errors.New(g.panicked("StartTerminal"))
		}
	}()
	pt, err := g.p.StartTerminal(ctx, s)
	if err != nil {
		return nil, err
	}
	return g.terminal(pt), nil
}

func (g guardRuntime) Run(ctx context.Context, s ext.CommandSpec) (r ext.CommandResult) {
	defer func() {
		if recover() != nil {
			r = ext.CommandResult{Exit: -1, Err: g.panicked("Run")}
		}
	}()
	return g.p.Run(ctx, s)
}

func (g guardRuntime) Serve(ctx context.Context, s ext.CommandSpec) (svc ext.ServiceProcess, err error) {
	defer func() {
		if recover() != nil {
			svc, err = nil, errors.New(g.panicked("Serve"))
		}
	}()
	ps, err := g.p.Serve(ctx, s)
	if err != nil {
		return nil, err
	}
	return g.service(ps), nil
}

func (g guardRuntime) Probe(ctx context.Context, s ext.ProbeSpec) (status int, err error) {
	defer func() {
		if recover() != nil {
			status, err = 0, errors.New(g.panicked("Probe"))
		}
	}()
	return g.p.Probe(ctx, s)
}

func (g guardRuntime) Port(ctx context.Context, ref ext.ProcessRef) (n int, err error) {
	defer func() {
		if recover() != nil {
			n, err = 0, errors.New(g.panicked("Port"))
		}
	}()
	return g.p.Port(ctx, ref)
}

// guardProcess is what the guard keeps of a process a registered Runtime
// started, a session's terminal or a service: every method of the
// Runtime's Terminal or ServiceProcess the server calls is recovered, so a
// panic in one never ends the server. The first panic is logged at error,
// naming the Runtime and the method, and ends the process: the guard asks
// the Runtime's own Stop (with no grace, for a terminal), bounded by
// ext.StopKillWait, on a goroutine of its own, and from then on the
// process answers as one that has ended: a read, a write or a resize the
// words, its exit Code -1 (a service's result Exit -1) and the words, and
// its Done closed once that stop has returned. Done is otherwise the
// Runtime's, read on that goroutine too.
type guardProcess struct {
	g     guardRuntime
	what  string
	stop  func(ctx context.Context) error
	once  sync.Once
	words string
	// failed is closed once a method has panicked, words set before it;
	// ended once the guard's stop of the process has returned, stopErr
	// set before it; done once the Runtime's Done or ended is.
	failed  chan struct{}
	ended   chan struct{}
	stopErr error
	done    chan struct{}
}

// guarded is a guardProcess of the Runtime's process what ("Terminal" or
// "ServiceProcess"), ended by stop, whose Done is done; its watch of
// done begins at once.
func (g guardRuntime) guarded(what string, stop func(context.Context) error, done func() <-chan struct{}) *guardProcess {
	p := &guardProcess{g: g, what: what, stop: stop, failed: make(chan struct{}), ended: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		var ch <-chan struct{}
		func() {
			defer func() {
				if recover() != nil {
					p.fail("Done")
				}
			}()
			ch = done()
		}()
		select {
		case <-ch:
		case <-p.ended:
		}
	}()
	return p
}

// fail is the first panic of the process's method: logged, the process
// ended. Later ones change nothing.
func (p *guardProcess) fail(method string) {
	p.once.Do(func() {
		p.g.s.log.Error("extension call failed: the Runtime's process panicked, and it is ended", "kind", ext.KindRuntime, "provider", p.g.name, "call", p.what+"."+method, "failure", "panic")
		p.words = fmt.Sprintf("the Runtime %s panicked in %s.%s", p.g.name, p.what, method)
		close(p.failed)
		go func() {
			defer close(p.ended)
			ctx, cancel := context.WithTimeout(context.Background(), ext.StopKillWait)
			defer cancel()
			p.stopErr = p.safeStop(ctx)
		}()
	})
}

// safeStop is the Runtime's stop of the process, its panic the words.
func (p *guardProcess) safeStop(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New(p.words)
		}
	}()
	return p.stop(ctx)
}

// broken says a method of the process has panicked.
func (p *guardProcess) broken() bool {
	select {
	case <-p.failed:
		return true
	default:
		return false
	}
}

// err is the words as an error, once broken.
func (p *guardProcess) err() error { return errors.New(p.words) }

// stopped is a Stop of a process that has panicked: the guard's stop's
// answer once it has returned, ctx's error if ctx ends first.
func (p *guardProcess) stopped(ctx context.Context) error {
	select {
	case <-p.ended:
		return p.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// guard runs fn, a call of the Runtime's method of the process; a panic
// in it is the process's failure, and answers false.
func (p *guardProcess) guard(method string, fn func()) (ok bool) {
	defer func() {
		if recover() != nil {
			p.fail(method)
			ok = false
		}
	}()
	fn()
	return true
}

// terminal is t behind the guard.
func (g guardRuntime) terminal(t ext.Terminal) ext.Terminal {
	return guardTerminal{guardProcess: g.guarded("Terminal", func(ctx context.Context) error { return t.Stop(ctx, 0) }, t.Done), t: t}
}

// guardTerminal is a registered Runtime's Terminal behind the guard
// (guardProcess).
type guardTerminal struct {
	*guardProcess
	t ext.Terminal
}

func (t guardTerminal) Read(b []byte) (n int, err error) {
	if t.broken() || !t.guard("Read", func() { n, err = t.t.Read(b) }) {
		return 0, t.err()
	}
	return n, err
}

func (t guardTerminal) Write(b []byte) (n int, err error) {
	if t.broken() || !t.guard("Write", func() { n, err = t.t.Write(b) }) {
		return 0, t.err()
	}
	return n, err
}

func (t guardTerminal) Resize(cols, rows uint16) (err error) {
	if t.broken() || !t.guard("Resize", func() { err = t.t.Resize(cols, rows) }) {
		return t.err()
	}
	return err
}

func (t guardTerminal) Done() <-chan struct{} { return t.done }

func (t guardTerminal) Exit() (st ext.ExitStatus) {
	if t.broken() || !t.guard("Exit", func() { st = t.t.Exit() }) {
		return ext.ExitStatus{Code: -1, Err: t.words}
	}
	return st
}

func (t guardTerminal) Stop(ctx context.Context, grace time.Duration) (err error) {
	if !t.broken() && t.guard("Stop", func() { err = t.t.Stop(ctx, grace) }) {
		return err
	}
	return t.stopped(ctx)
}

func (t guardTerminal) Process() (info ext.ProcessInfo) {
	if t.broken() || !t.guard("Process", func() { info = t.t.Process() }) {
		return ext.ProcessInfo{}
	}
	return info
}

// service is sp behind the guard.
func (g guardRuntime) service(sp ext.ServiceProcess) ext.ServiceProcess {
	return guardService{guardProcess: g.guarded("ServiceProcess", sp.Stop, sp.Done), sp: sp}
}

// guardService is a registered Runtime's ServiceProcess behind the guard
// (guardProcess).
type guardService struct {
	*guardProcess
	sp ext.ServiceProcess
}

func (s guardService) Done() <-chan struct{} { return s.done }

func (s guardService) Result() (r ext.CommandResult) {
	if s.broken() || !s.guard("Result", func() { r = s.sp.Result() }) {
		return ext.CommandResult{Exit: -1, Err: s.words}
	}
	return r
}

func (s guardService) Stop(ctx context.Context) (err error) {
	if !s.broken() && s.guard("Stop", func() { err = s.sp.Stop(ctx) }) {
		return err
	}
	return s.stopped(ctx)
}
