package sessiontest

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// A FakeRuntime records every call with its spec and its ref; unscripted,
// it refuses each process method naming it; scripted, it answers as the
// test sets it (a FakeProc's terminal, a result, a service, the probe's
// and the port's maps); with Base, it passes what is not scripted on.
func TestFakeRuntimeRecordsWhatItIsAsked(t *testing.T) {
	ctx := t.Context()
	ref := ext.ProcessRef{Run: "r-1", Task: "build", Attempt: 2, Tree: "checkout"}
	t.Run("unscripted", func(t *testing.T) {
		f := &FakeRuntime{}
		if f.Name() != "fake" || f.Where() != "fake" || f.Where().Valid() != nil || !f.Takes(ext.ProcessRef{Run: "r-1"}) {
			t.Fatalf("the fake's names: %q %q, takes it", f.Name(), f.Where())
		}
		if _, err := f.StartTerminal(ctx, ext.TerminalSpec{Argv: []string{"agent"}, Ref: ref}); err == nil || !strings.Contains(err.Error(), "StartTerminal") {
			t.Fatalf("StartTerminal: %v", err)
		}
		if res := f.Run(ctx, ext.CommandSpec{Argv: []string{"make"}, Ref: ref}); res.Exit != -1 || !strings.Contains(res.Err, "Run") {
			t.Fatalf("Run: %+v", res)
		}
		if _, err := f.Serve(ctx, ext.CommandSpec{Argv: []string{"web"}, Ref: ref}); err == nil || !strings.Contains(err.Error(), "Serve") {
			t.Fatalf("Serve: %v", err)
		}
		if _, err := f.Probe(ctx, ext.ProbeSpec{URL: "http://127.0.0.1:1/", Ref: ref}); err == nil || !strings.Contains(err.Error(), "Probe") {
			t.Fatalf("Probe: %v", err)
		}
		if _, err := f.Port(ctx, ref); err == nil || !strings.Contains(err.Error(), "Port") {
			t.Fatalf("Port: %v", err)
		}
		var methods []string
		for _, c := range f.Calls() {
			methods = append(methods, c.Method)
			if c.Method != "Takes" && c.Ref != ref {
				t.Fatalf("%s's ref: %+v", c.Method, c.Ref)
			}
		}
		if want := []string{"Takes", "StartTerminal", "Run", "Serve", "Probe", "Port"}; !reflect.DeepEqual(methods, want) {
			t.Fatalf("the calls: %v", methods)
		}
		if s, ok := f.Asked("Run")[0].Spec.(ext.CommandSpec); !ok || s.Argv[0] != "make" {
			t.Fatalf("Run's spec: %+v", f.Asked("Run")[0].Spec)
		}
		if c := f.Asked("Takes"); len(c) != 1 || c[0].Ref != (ext.ProcessRef{Run: "r-1"}) || c[0].Spec != nil {
			t.Fatalf("Takes's calls: %+v", c)
		}
	})
	t.Run("scripted", func(t *testing.T) {
		proc := NewFakeProc()
		want := ext.CommandResult{Exit: 3, Head: []byte("no\n"), Total: 3, Err: "", Duration: time.Second}
		f := &FakeRuntime{
			Place:    "guest",
			TakesRun: func(r ext.ProcessRef) bool { return r.Run == "mine" },
			Terminal: func(ext.TerminalSpec) (*FakeProc, error) { return proc, nil },
			Result:   func(ext.CommandSpec) ext.CommandResult { return want },
			Service:  func(ext.CommandSpec) (ext.ServiceProcess, error) { return nil, errors.New("no service here") },
			Probes:   map[string]int{"http://127.0.0.1:1/": 204},
			Ports:    map[ext.ProcessRef]int{ref: 41001},
		}
		if f.Where() != "guest" || f.Takes(ext.ProcessRef{Run: "other"}) || !f.Takes(ext.ProcessRef{Run: "mine"}) {
			t.Fatal("Place or TakesRun not answered")
		}
		term, err := f.StartTerminal(ctx, ext.TerminalSpec{Argv: []string{"agent"}, Ref: ref})
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = proc.Print("ready> ") }()
		buf := make([]byte, 16)
		if n, err := term.Read(buf); err != nil || string(buf[:n]) != "ready> " {
			t.Fatalf("read %q, %v", buf[:n], err)
		}
		if _, err := term.Write([]byte("go")); err != nil || string(<-proc.Input) != "go" {
			t.Fatalf("write: %v", err)
		}
		if err := term.Resize(100, 30); err != nil {
			t.Fatal(err)
		}
		if cols, rows := proc.Size(); cols != 100 || rows != 30 {
			t.Fatalf("the size: %dx%d", cols, rows)
		}
		proc.End(7)
		<-term.Done()
		if st := term.Exit(); st.Code != 7 || term.Process().PID != 0 {
			t.Fatalf("the exit: %+v, process %+v", st, term.Process())
		}
		if res := f.Run(ctx, ext.CommandSpec{Ref: ref}); !reflect.DeepEqual(res, want) {
			t.Fatalf("Run: %+v", res)
		}
		if _, err := f.Serve(ctx, ext.CommandSpec{Ref: ref}); err == nil || err.Error() != "no service here" {
			t.Fatalf("Serve: %v", err)
		}
		if status, err := f.Probe(ctx, ext.ProbeSpec{URL: "http://127.0.0.1:1/", Ref: ref}); status != 204 || err != nil {
			t.Fatalf("Probe: %d %v", status, err)
		}
		if n, err := f.Port(ctx, ref); n != 41001 || err != nil {
			t.Fatalf("Port: %d %v", n, err)
		}
		if n := len(f.Calls()); n != 7 {
			t.Fatalf("%d calls recorded: %+v", n, f.Calls())
		}
	})
	t.Run("through its Base", func(t *testing.T) {
		f := &FakeRuntime{Base: local.Native()}
		dir := t.TempDir()
		res := f.Run(ctx, ext.CommandSpec{Argv: []string{"sh", "-c", "echo $GREETING"}, Dir: dir, Env: []string{"GREETING=hello"}, Timeout: time.Minute, Ref: ref})
		if res.Exit != 0 || string(res.Head) != "hello\n" || res.Err != "" {
			t.Fatalf("Run through the Base: %+v", res)
		}
		n, err := f.Port(ctx, ref)
		if err != nil || n <= 0 {
			t.Fatalf("Port through the Base: %d %v", n, err)
		}
		c := f.Asked("Run")
		if len(c) != 1 || c[0].Ref != ref || c[0].Spec.(ext.CommandSpec).Dir != dir {
			t.Fatalf("the Run recorded: %+v", c)
		}
		svc, err := f.Serve(context.Background(), ext.CommandSpec{Argv: []string{"sleep", "60"}, Dir: dir, Env: []string{}, Ref: ref})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		<-svc.Done()
	})
}

// A FakeRuntime is no ext.PipeStarter; a FakePipeRuntime is: unscripted
// its StartPipes refuses naming the method; scripted it answers a
// FakeProc's pipes (the test's prints its output, its input arriving on
// Input, a closed input refusing writes, its end and its stop the
// FakeProc's); through its Base it starts the native child, the call
// recorded with its spec, its ref and the process.
func TestFakePipeRuntimeScriptsPipes(t *testing.T) {
	ctx := t.Context()
	ref := ext.ProcessRef{Session: "s-1"}
	var plain ext.Runtime = &FakeRuntime{}
	if _, ok := plain.(ext.PipeStarter); ok {
		t.Fatal("a FakeRuntime is a PipeStarter")
	}
	var piped ext.Runtime = FakePipeRuntime{&FakeRuntime{}}
	ps, ok := piped.(ext.PipeStarter)
	if !ok {
		t.Fatal("a FakePipeRuntime is no PipeStarter")
	}
	if _, err := ps.StartPipes(ctx, ext.PipeSpec{Argv: []string{"agent"}, Ref: ref}); err == nil || !strings.Contains(err.Error(), "StartPipes") {
		t.Fatalf("unscripted: %v", err)
	}
	proc := NewFakeProc()
	f := FakePipeRuntime{&FakeRuntime{Pipes: func(ext.PipeSpec) (*FakeProc, error) { return proc, nil }}}
	p, err := f.StartPipes(ctx, ext.PipeSpec{Argv: []string{"agent", "-p"}, Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = proc.Print("{\"type\":\"ready\"}\n") }()
	buf := make([]byte, 64)
	if n, err := p.Stdout().Read(buf); err != nil || string(buf[:n]) != "{\"type\":\"ready\"}\n" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
	if _, err := p.Stdin().Write([]byte("go\n")); err != nil || string(<-proc.Input) != "go\n" {
		t.Fatalf("write: %v", err)
	}
	if err := p.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stdin().Write([]byte("late\n")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("a write once the input is closed: %v", err)
	}
	if err := p.Stop(ctx, time.Second); err != nil || !proc.Stopped() {
		t.Fatalf("stop: %v, stopped %v", err, proc.Stopped())
	}
	<-p.Done()
	if st := p.Exit(); st.Code != -1 || p.Process().PID != 0 {
		t.Fatalf("the exit: %+v, process %+v", st, p.Process())
	}
	if c := f.Asked("StartPipes"); len(c) != 1 || c[0].Ref != ref || !reflect.DeepEqual(c[0].Spec.(ext.PipeSpec).Argv, []string{"agent", "-p"}) {
		t.Fatalf("the call recorded: %+v", c)
	}
	t.Run("a script that panics", func(t *testing.T) {
		f := FakePipeRuntime{&FakeRuntime{Pipes: func(ext.PipeSpec) (*FakeProc, error) { panic("the script panics") }}}
		defer func() {
			if r := recover(); r != "the script panics" {
				t.Fatalf("recovered %v", r)
			}
		}()
		f.StartPipes(ctx, ext.PipeSpec{Ref: ref})
	})
	t.Run("through its Base", func(t *testing.T) {
		f := FakePipeRuntime{&FakeRuntime{Base: local.Native()}}
		p, err := f.StartPipes(ctx, ext.PipeSpec{Argv: []string{"cat"}, Dir: t.TempDir(), Env: []string{}, Ref: ref})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Stdin().Write([]byte("echoed\n")); err != nil {
			t.Fatal(err)
		}
		if n, err := io.ReadAtLeast(p.Stdout(), buf, len("echoed\n")); err != nil || string(buf[:n]) != "echoed\n" {
			t.Fatalf("read %q, %v", buf[:n], err)
		}
		if err := p.Stop(ctx, 0); err != nil {
			t.Fatal(err)
		}
		if c := f.Asked("StartPipes"); len(c) != 1 || c[0].Process.PID <= 0 || c[0].Process != p.Process() {
			t.Fatalf("the call recorded: %+v", c)
		}
	})
}
