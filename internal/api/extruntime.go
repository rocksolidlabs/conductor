package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

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
// command's result with Exit -1 and the words. The panic's value is not
// quoted: the spec the Runtime was handed holds a session's agent token.
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
	return g.p.StartTerminal(ctx, s)
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
	return g.p.Serve(ctx, s)
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
