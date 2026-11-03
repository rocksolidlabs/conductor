package sessiontest

import (
	"context"
	"errors"
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
