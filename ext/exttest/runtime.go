package exttest

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"
)

// RuntimeCall is one call a FakeRuntime was given: its method, its ref
// and its spec. The record keeps no DirFile (lent for the call), no Log
// writer and no Started callback: the caller's, never the fake's to hold.
type RuntimeCall struct {
	Method   string // "Takes", "StartTerminal", "Run", "Serve", "Probe" or "Port"
	Ref      ext.ProcessRef
	Terminal ext.TerminalSpec // StartTerminal's
	Command  ext.CommandSpec  // Run's and Serve's
	Probe    ext.ProbeSpec    // Probe's
}

// runtimeScript is what a test set a FakeRuntime's methods to answer, and
// the calls it was given; under the fake's mu.
type runtimeScript struct {
	declines bool
	start    func(context.Context, ext.TerminalSpec) (ext.Terminal, error)
	run      func(context.Context, ext.CommandSpec) ext.CommandResult
	serve    func(context.Context, ext.CommandSpec) (ext.ServiceProcess, error)
	probe    func(context.Context, ext.ProbeSpec) (int, error)
	port     func(context.Context, ext.ProcessRef) (int, error)
	calls    []RuntimeCall
}

// SetTakes has Takes answer takes; it answers true until set.
func (f *FakeRuntime) SetTakes(takes bool) {
	f.mu.Lock()
	f.script.declines = !takes
	f.mu.Unlock()
	f.tell()
}

// SetStartTerminal has StartTerminal answer as fn does (nil: unscripted).
func (f *FakeRuntime) SetStartTerminal(fn func(context.Context, ext.TerminalSpec) (ext.Terminal, error)) {
	f.mu.Lock()
	f.script.start = fn
	f.mu.Unlock()
	f.tell()
}

// SetRun has Run answer as fn does (nil: unscripted).
func (f *FakeRuntime) SetRun(fn func(context.Context, ext.CommandSpec) ext.CommandResult) {
	f.mu.Lock()
	f.script.run = fn
	f.mu.Unlock()
	f.tell()
}

// SetServe has Serve answer as fn does (nil: unscripted).
func (f *FakeRuntime) SetServe(fn func(context.Context, ext.CommandSpec) (ext.ServiceProcess, error)) {
	f.mu.Lock()
	f.script.serve = fn
	f.mu.Unlock()
	f.tell()
}

// SetProbe has Probe answer as fn does (nil: unscripted).
func (f *FakeRuntime) SetProbe(fn func(context.Context, ext.ProbeSpec) (int, error)) {
	f.mu.Lock()
	f.script.probe = fn
	f.mu.Unlock()
	f.tell()
}

// SetPort has Port answer as fn does (nil: unscripted).
func (f *FakeRuntime) SetPort(fn func(context.Context, ext.ProcessRef) (int, error)) {
	f.mu.Lock()
	f.script.port = fn
	f.mu.Unlock()
	f.tell()
}

// RuntimeCalls answers the calls the fake was given, in order.
func (f *FakeRuntime) RuntimeCalls() []RuntimeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.script.calls)
}

// record notes c and answers the script as it is now.
func (f *FakeRuntime) record(c RuntimeCall) runtimeScript {
	c.Terminal.Argv, c.Terminal.Env, c.Terminal.DirFile = slices.Clone(c.Terminal.Argv), slices.Clone(c.Terminal.Env), nil
	c.Command.Argv, c.Command.Env = slices.Clone(c.Command.Argv), slices.Clone(c.Command.Env)
	c.Command.Log, c.Command.Started = nil, nil
	f.mu.Lock()
	f.script.calls = append(f.script.calls, c)
	s := f.script
	f.mu.Unlock()
	f.tell()
	return s
}

// unscripted is the refusal of a method no test scripted.
func (f *FakeRuntime) unscripted(method string) string {
	return fmt.Sprintf("exttest: FakeRuntime %q: %s is not scripted", f.name, method)
}

// Takes answers true unless SetTakes(false).
func (f *FakeRuntime) Takes(ref ext.ProcessRef) bool {
	return !f.record(RuntimeCall{Method: "Takes", Ref: ref}).declines
}

// StartTerminal answers as scripted; unscripted, an error naming it, with
// nothing started.
func (f *FakeRuntime) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	sc := f.record(RuntimeCall{Method: "StartTerminal", Ref: s.Ref, Terminal: s})
	if sc.start == nil {
		return nil, errors.New(f.unscripted("StartTerminal"))
	}
	return sc.start(ctx, s)
}

// Run answers as scripted; unscripted, Exit -1 with words naming it.
func (f *FakeRuntime) Run(ctx context.Context, s ext.CommandSpec) ext.CommandResult {
	sc := f.record(RuntimeCall{Method: "Run", Ref: s.Ref, Command: s})
	if sc.run == nil {
		return ext.CommandResult{Exit: -1, Err: f.unscripted("Run")}
	}
	return sc.run(ctx, s)
}

// Serve answers as scripted; unscripted, an error naming it, with nothing
// started.
func (f *FakeRuntime) Serve(ctx context.Context, s ext.CommandSpec) (ext.ServiceProcess, error) {
	sc := f.record(RuntimeCall{Method: "Serve", Ref: s.Ref, Command: s})
	if sc.serve == nil {
		return nil, errors.New(f.unscripted("Serve"))
	}
	return sc.serve(ctx, s)
}

// Probe answers as scripted; unscripted, an error naming it.
func (f *FakeRuntime) Probe(ctx context.Context, s ext.ProbeSpec) (int, error) {
	sc := f.record(RuntimeCall{Method: "Probe", Ref: s.Ref, Probe: s})
	if sc.probe == nil {
		return 0, errors.New(f.unscripted("Probe"))
	}
	return sc.probe(ctx, s)
}

// Port answers as scripted; unscripted, an error naming it.
func (f *FakeRuntime) Port(ctx context.Context, ref ext.ProcessRef) (int, error) {
	sc := f.record(RuntimeCall{Method: "Port", Ref: ref})
	if sc.port == nil {
		return 0, errors.New(f.unscripted("Port"))
	}
	return sc.port(ctx, ref)
}
