package exttest

import (
	"context"
	"errors"
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
// set. It passes the suite's Runtime check.
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

	RunAll(t, nil, ext.Providers{Runtime: f})
}
