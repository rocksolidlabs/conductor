package exttest

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// SetWhere is a script changed: it tells Changed, so an Await that read
// the old Where before it ends once the new one holds.
func TestFakeRuntimeSetWhereTellsChanged(t *testing.T) {
	f := NewFakeRuntime("vm", "vm")
	changed := f.Changed()
	f.SetWhere("other")
	select {
	case <-changed:
	default:
		t.Fatal("SetWhere did not tell Changed")
	}
	// The bound turns a lost tell into a failure, not a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	checked := make(chan struct{})
	got := make(chan error, 1)
	var once bool
	go func() {
		got <- Await(ctx, f, func() bool {
			if !once {
				once = true
				defer close(checked)
			}
			return f.Where() == "third"
		})
	}()
	<-checked // the Await has read "other" and waits on Changed
	f.SetWhere("third")
	if err := <-got; err != nil {
		t.Fatalf("Await after SetWhere: %v", err)
	}
}

// FakeRuntime records every call of the Runtime's methods with its ref and
// its spec (no DirFile, Log or Started kept), answers what a test scripts,
// and, unscripted, its refusal: StartTerminal, Serve, Probe and Port an
// error naming the method, Run Exit -1 with those words, Takes true unless
// set. It passes the suite's checks that start nothing (Runtime): RunAll
// would also ask for a RuntimeCase, its Where not native.
func TestFakeRuntimeRecordsAndRefuses(t *testing.T) {
	ctx := context.Background()
	f := NewFakeRuntime("vm", "vm")
	changed := f.Changed()
	ref := ext.ProcessRef{Run: "build-00000001", Task: "build", Attempt: 2, Tree: "checkout"}
	if !f.Takes(ref) {
		t.Fatal("Takes declined before SetTakes")
	}
	select {
	case <-changed:
	default:
		t.Fatal("a call did not tell Changed")
	}
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	argv := []string{"agent", "--flag"}
	if term, err := f.StartTerminal(ctx, ext.TerminalSpec{Argv: argv, DirFile: dir, Env: []string{"A=1"}, Ref: ref}); term != nil || err == nil || !strings.Contains(err.Error(), "StartTerminal") {
		t.Fatalf("an unscripted StartTerminal: %v %v", term, err)
	}
	argv[0] = "changed" // the record is a copy
	if res := f.Run(ctx, ext.CommandSpec{Argv: []string{"make", "test"}, Started: func(ext.ProcessInfo) {}, Ref: ref}); res.Exit != -1 || !strings.Contains(res.Err, "Run") {
		t.Fatalf("an unscripted Run: %+v", res)
	}
	if svc, err := f.Serve(ctx, ext.CommandSpec{Argv: []string{"serve"}, Ref: ref}); svc != nil || err == nil || !strings.Contains(err.Error(), "Serve") {
		t.Fatalf("an unscripted Serve: %v %v", svc, err)
	}
	if n, err := f.Probe(ctx, ext.ProbeSpec{URL: "http://127.0.0.1:1/", Ref: ref}); n != 0 || err == nil || !strings.Contains(err.Error(), "Probe") {
		t.Fatalf("an unscripted Probe: %d %v", n, err)
	}
	if n, err := f.Port(ctx, ref); n != 0 || err == nil || !strings.Contains(err.Error(), "Port") {
		t.Fatalf("an unscripted Port: %d %v", n, err)
	}
	calls := f.RuntimeCalls()
	var methods []string
	for _, c := range calls {
		methods = append(methods, c.Method)
		if c.Ref != ref {
			t.Errorf("%s recorded the ref %+v", c.Method, c.Ref)
		}
	}
	if got := strings.Join(methods, " "); got != "Takes StartTerminal Run Serve Probe Port" {
		t.Fatalf("the calls recorded: %s", got)
	}
	if c := calls[1]; c.Terminal.DirFile != nil || c.Terminal.Argv[0] != "agent" || c.Terminal.Env[0] != "A=1" {
		t.Errorf("the StartTerminal record: %+v", c.Terminal)
	}
	if c := calls[2]; c.Command.Started != nil || c.Command.Argv[1] != "test" {
		t.Errorf("the Run record: %+v", c.Command)
	}
	if calls[4].Probe.URL != "http://127.0.0.1:1/" {
		t.Errorf("the Probe record: %+v", calls[4].Probe)
	}
	// An answer is a copy too: changing its argv and env changes no later one.
	calls[1].Terminal.Argv[0], calls[1].Terminal.Env[0], calls[2].Command.Argv[0] = "changed", "B=2", "changed"
	if again := f.RuntimeCalls(); again[1].Terminal.Argv[0] != "agent" || again[1].Terminal.Env[0] != "A=1" || again[2].Command.Argv[0] != "make" {
		t.Errorf("a changed answer changed the record: %+v %+v", again[1].Terminal, again[2].Command)
	}

	// Scripted, each method answers as its script does.
	f.SetTakes(false)
	f.SetRun(func(context.Context, ext.CommandSpec) ext.CommandResult {
		return ext.CommandResult{Exit: 3, Head: []byte("out")}
	})
	f.SetPort(func(context.Context, ext.ProcessRef) (int, error) { return 41234, nil })
	f.SetProbe(func(context.Context, ext.ProbeSpec) (int, error) { return 200, nil })
	errStart := errors.New("no guest")
	f.SetStartTerminal(func(context.Context, ext.TerminalSpec) (ext.Terminal, error) { return nil, errStart })
	f.SetServe(func(context.Context, ext.CommandSpec) (ext.ServiceProcess, error) { return nil, errStart })
	if f.Takes(ref) {
		t.Error("Takes after SetTakes(false)")
	}
	if res := f.Run(ctx, ext.CommandSpec{}); res.Exit != 3 || string(res.Head) != "out" {
		t.Errorf("a scripted Run: %+v", res)
	}
	if n, err := f.Port(ctx, ref); n != 41234 || err != nil {
		t.Errorf("a scripted Port: %d %v", n, err)
	}
	if n, err := f.Probe(ctx, ext.ProbeSpec{}); n != 200 || err != nil {
		t.Errorf("a scripted Probe: %d %v", n, err)
	}
	if _, err := f.StartTerminal(ctx, ext.TerminalSpec{}); !errors.Is(err, errStart) {
		t.Errorf("a scripted StartTerminal: %v", err)
	}
	if _, err := f.Serve(ctx, ext.CommandSpec{}); !errors.Is(err, errStart) {
		t.Errorf("a scripted Serve: %v", err)
	}
	if n := len(f.RuntimeCalls()); n != 12 {
		t.Errorf("%d calls recorded, want 12", n)
	}

	Runtime(t, nil, f)
}

// With a base, every method a test has not scripted passes to it, each
// call recorded first; a scripted one answers its script; Takes and Where
// stay the fake's.
func TestFakeRuntimeBasePassesUnscriptedCalls(t *testing.T) {
	ctx := context.Background()
	base := NewFakeRuntime("base", "base")
	base.SetRun(func(context.Context, ext.CommandSpec) ext.CommandResult { return ext.CommandResult{Exit: 5} })
	base.SetPort(func(context.Context, ext.ProcessRef) (int, error) { return 40001, nil })
	base.SetProbe(func(context.Context, ext.ProbeSpec) (int, error) { return 204, nil })
	term := NewFakeTerminal(ext.ProcessInfo{PID: 7})
	base.SetStartTerminal(func(context.Context, ext.TerminalSpec) (ext.Terminal, error) { return term, nil })
	errServe := errors.New("base serve")
	base.SetServe(func(context.Context, ext.CommandSpec) (ext.ServiceProcess, error) { return nil, errServe })
	base.SetTakes(false)

	f := NewFakeRuntime("vm", "vm")
	f.SetBase(base)
	ref := ext.ProcessRef{Run: "build-00000001"}
	if !f.Takes(ref) {
		t.Error("Takes passed to the base")
	}
	if got, err := f.StartTerminal(ctx, ext.TerminalSpec{Argv: []string{"agent"}, Ref: ref}); got != term || err != nil {
		t.Errorf("StartTerminal: %v %v", got, err)
	}
	if res := f.Run(ctx, ext.CommandSpec{Argv: []string{"make"}, Ref: ref}); res.Exit != 5 {
		t.Errorf("Run: %+v", res)
	}
	if _, err := f.Serve(ctx, ext.CommandSpec{Ref: ref}); !errors.Is(err, errServe) {
		t.Errorf("Serve: %v", err)
	}
	if n, err := f.Probe(ctx, ext.ProbeSpec{Ref: ref}); n != 204 || err != nil {
		t.Errorf("Probe: %d %v", n, err)
	}
	if n, err := f.Port(ctx, ref); n != 40001 || err != nil {
		t.Errorf("Port: %d %v", n, err)
	}
	f.SetRun(func(context.Context, ext.CommandSpec) ext.CommandResult { return ext.CommandResult{Exit: 9} })
	if res := f.Run(ctx, ext.CommandSpec{}); res.Exit != 9 {
		t.Errorf("a scripted Run with a base: %+v", res)
	}
	var methods []string
	for _, c := range f.RuntimeCalls() {
		methods = append(methods, c.Method)
	}
	if got := strings.Join(methods, " "); got != "Takes StartTerminal Run Serve Probe Port Run" {
		t.Errorf("the fake recorded %s", got)
	}
	if n := len(base.RuntimeCalls()); n != 5 {
		t.Errorf("the base was given %d calls, want 5 (Takes and the scripted Run are the fake's)", n)
	}
	f.SetBase(nil)
	if res := f.Run(ctx, ext.CommandSpec{}); res.Exit != 9 {
		t.Errorf("a scripted Run with the base gone: %+v", res)
	}
	f.SetRun(nil)
	if res := f.Run(ctx, ext.CommandSpec{}); res.Exit != -1 || !strings.Contains(res.Err, "not scripted") {
		t.Errorf("an unscripted Run with no base: %+v", res)
	}
	if f.Where() != "vm" {
		t.Errorf("Where %q", f.Where())
	}
}

// A FakeTerminal is a pipe: what the test prints the caller reads, what
// the caller writes the test reads back, resizes and stops recorded; End
// ends its output and closes Done; Stop ends it as the ask does natively
// and closes it.
func TestFakeTerminalIsAPipe(t *testing.T) {
	term := NewFakeTerminal(ext.ProcessInfo{PID: 41, Group: 41})
	changed := term.Changed()
	go term.Print([]byte("hello"))
	buf := make([]byte, 16)
	if n, err := term.Read(buf); err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("Read: %q %v", buf[:n], err)
	}
	if _, err := term.Write([]byte("typed\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Error("a write did not tell Changed")
	}
	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if string(term.Typed()) != "typed\n" || len(term.Sizes()) != 1 || term.Sizes()[0] != (TerminalSize{Cols: 120, Rows: 40}) {
		t.Errorf("Typed %q, Sizes %v", term.Typed(), term.Sizes())
	}
	if term.Process().PID != 41 {
		t.Errorf("Process %+v", term.Process())
	}
	select {
	case <-term.Done():
		t.Fatal("Done before the end")
	default:
	}
	term.End(ext.ExitStatus{Code: 2})
	term.End(ext.ExitStatus{Code: 3}) // the first end holds
	<-term.Done()
	if _, err := term.Read(buf); !errors.Is(err, io.EOF) {
		t.Errorf("Read after the end: %v", err)
	}
	if st := term.Exit(); st.Code != 2 {
		t.Errorf("Exit %+v", st)
	}
	if err := term.Stop(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if st := term.Exit(); st.Code != 2 {
		t.Errorf("Stop after the end changed Exit: %+v", st)
	}
	if _, err := term.Write([]byte("x")); err == nil {
		t.Error("Write after Stop")
	}
	if err := term.Resize(1, 1); err == nil {
		t.Error("Resize after Stop")
	}

	running := NewFakeTerminal(ext.ProcessInfo{})
	if err := running.Stop(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	<-running.Done()
	if st := running.Exit(); st.Code != 143 || st.Signal != "terminated" {
		t.Errorf("a running terminal stopped: %+v", st)
	}
	if _, err := running.Print([]byte("late")); err == nil {
		t.Error("Print after Stop")
	}
	if _, err := running.Read(buf); !errors.Is(err, io.EOF) {
		t.Errorf("Read after Stop: %v", err)
	}
	if got := running.Stops(); len(got) != 1 || got[0] != 0 {
		t.Errorf("Stops %v", got)
	}
}

// A Stop made while an End is under way returns once that End has closed
// Done and ended the output, never before: a Stop that saw the end begun
// and returned at once would leave its caller a Done still open and a Read
// still blocked.
func TestFakeTerminalStopWaitsForAConcurrentEnd(t *testing.T) {
	buf := make([]byte, 1)
	for range 500 {
		term := NewFakeTerminal(ext.ProcessInfo{})
		start := make(chan struct{})
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			<-start
			term.End(ext.ExitStatus{Code: 1})
		}()
		close(start)
		if err := term.Stop(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
		select {
		case <-term.Done():
		default:
			t.Fatal("Stop returned before Done was closed")
		}
		if _, err := term.Read(buf); !errors.Is(err, io.EOF) {
			t.Fatalf("Read once Stop returned: %v, want io.EOF", err)
		}
		<-ended
	}
}
