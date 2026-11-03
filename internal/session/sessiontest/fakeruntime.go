package sessiontest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/rocksolidlabs/conductor/ext"
)

// FakeRuntime is an ext.Runtime for this repository's tests: it records
// every call with its spec and its ref, and answers each as the test sets
// it, or, with Base set, passes the call to Base (local.Native, so a run's
// real work can be done under a Where that is not native). A call nothing
// answers is refused: StartTerminal, Serve, Probe and Port with an error
// naming the method, Run with Exit -1 and those words. Set its fields
// before its first call; its methods are safe for concurrent use.
//
// Of this module it imports ext and internal/pty alone, never
// internal/session, so the session's own tests may use it.
type FakeRuntime struct {
	// Place is its Where; "fake" when empty.
	Place ext.Where
	// TakesRun answers Takes; nil takes every run.
	TakesRun func(ext.ProcessRef) bool
	// Base, when set, answers every call of the methods below the test
	// has not scripted.
	Base ext.Runtime
	// Terminal answers StartTerminal with a FakeProc's terminal
	// (FakeTerminal), or its error.
	Terminal func(ext.TerminalSpec) (*FakeProc, error)
	// Result answers Run.
	Result func(ext.CommandSpec) ext.CommandResult
	// Service answers Serve.
	Service func(ext.CommandSpec) (ext.ServiceProcess, error)
	// Probes answer Probe by URL, and Ports answer Port by ref.
	Probes map[string]int
	Ports  map[ext.ProcessRef]int
	// PanicAfterStart makes StartTerminal start through Base, then end and
	// reap what Base started (Stop with no grace), as the contract asks of
	// every Runtime a panic leaves, and panic with PanicAfterStartValue.
	PanicAfterStart bool

	mu    sync.Mutex
	calls []RuntimeCall
}

// PanicAfterStartValue is what a FakeRuntime with PanicAfterStart panics
// with.
const PanicAfterStartValue = "sessiontest: the FakeRuntime panics after its start"

var _ ext.Runtime = (*FakeRuntime)(nil)

// RuntimeCall is one call a FakeRuntime was made: its method, its ref and
// its spec (an ext.TerminalSpec, ext.CommandSpec or ext.ProbeSpec; nil for
// Takes and Port), and, for a StartTerminal Base answered, the process
// Base started, as its Terminal reported it.
type RuntimeCall struct {
	Method  string
	Ref     ext.ProcessRef
	Spec    any
	Process ext.ProcessInfo
}

// Calls is every call made so far, in order.
func (f *FakeRuntime) Calls() []RuntimeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Asked is the calls of method made so far, in order.
func (f *FakeRuntime) Asked(method string) []RuntimeCall {
	var out []RuntimeCall
	for _, c := range f.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *FakeRuntime) record(c RuntimeCall) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return len(f.calls) - 1
}

// Name answers "fake".
func (f *FakeRuntime) Name() string { return "fake" }

// Where answers Place, "fake" when it is empty.
func (f *FakeRuntime) Where() ext.Where {
	if f.Place == "" {
		return "fake"
	}
	return f.Place
}

// Takes answers TakesRun, true when it is nil, recording the call.
func (f *FakeRuntime) Takes(ref ext.ProcessRef) bool {
	f.record(RuntimeCall{Method: "Takes", Ref: ref})
	return f.TakesRun == nil || f.TakesRun(ref)
}

func refused(method string) error {
	return fmt.Errorf("sessiontest: the FakeRuntime has no answer for %s", method)
}

// StartTerminal answers Terminal, else Base, else refuses.
func (f *FakeRuntime) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	i := f.record(RuntimeCall{Method: "StartTerminal", Ref: s.Ref, Spec: s})
	switch {
	case f.PanicAfterStart:
		if f.Base == nil {
			panic("sessiontest: PanicAfterStart without a Base")
		}
		t, err := f.Base.StartTerminal(ctx, s)
		if err != nil {
			return nil, err
		}
		f.started(i, t)
		_ = t.Stop(context.Background(), 0)
		panic(PanicAfterStartValue)
	case f.Terminal != nil:
		p, err := f.Terminal(s)
		if err != nil {
			return nil, err
		}
		return FakeTerminal(p), nil
	case f.Base != nil:
		t, err := f.Base.StartTerminal(ctx, s)
		if err == nil {
			f.started(i, t)
		}
		return t, err
	}
	return nil, refused("StartTerminal")
}

// started notes the process t reports on call i.
func (f *FakeRuntime) started(i int, t ext.Terminal) {
	p := t.Process()
	f.mu.Lock()
	f.calls[i].Process = p
	f.mu.Unlock()
}

// Run answers Result, else Base, else refuses.
func (f *FakeRuntime) Run(ctx context.Context, s ext.CommandSpec) ext.CommandResult {
	f.record(RuntimeCall{Method: "Run", Ref: s.Ref, Spec: s})
	switch {
	case f.Result != nil:
		return f.Result(s)
	case f.Base != nil:
		return f.Base.Run(ctx, s)
	}
	return ext.CommandResult{Exit: -1, Err: refused("Run").Error()}
}

// Serve answers Service, else Base, else refuses.
func (f *FakeRuntime) Serve(ctx context.Context, s ext.CommandSpec) (ext.ServiceProcess, error) {
	f.record(RuntimeCall{Method: "Serve", Ref: s.Ref, Spec: s})
	switch {
	case f.Service != nil:
		return f.Service(s)
	case f.Base != nil:
		return f.Base.Serve(ctx, s)
	}
	return nil, refused("Serve")
}

// Probe answers Probes by URL, else Base, else refuses.
func (f *FakeRuntime) Probe(ctx context.Context, s ext.ProbeSpec) (int, error) {
	f.record(RuntimeCall{Method: "Probe", Ref: s.Ref, Spec: s})
	if status, ok := f.Probes[s.URL]; ok {
		return status, nil
	}
	if f.Base != nil {
		return f.Base.Probe(ctx, s)
	}
	return 0, refused("Probe")
}

// Port answers Ports by ref, else Base, else refuses.
func (f *FakeRuntime) Port(ctx context.Context, ref ext.ProcessRef) (int, error) {
	f.record(RuntimeCall{Method: "Port", Ref: ref})
	if n, ok := f.Ports[ref]; ok {
		return n, nil
	}
	if f.Base != nil {
		return f.Base.Port(ctx, ref)
	}
	return 0, refused("Port")
}

// FakeTerminal is p as an ext.Terminal: its output, input, size, end and
// stop are p's; its exit code p's (-1 after Stop); its process a pid of 0,
// none.
func FakeTerminal(p *FakeProc) ext.Terminal { return fakeTerminal{p} }

type fakeTerminal struct{ *FakeProc }

func (t fakeTerminal) Exit() ext.ExitStatus {
	st := t.FakeProc.Exit()
	return ext.ExitStatus{Code: st.Code, Signal: st.Signal}
}

func (t fakeTerminal) Process() ext.ProcessInfo { return ext.ProcessInfo{} }
