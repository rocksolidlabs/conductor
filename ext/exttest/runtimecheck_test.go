package exttest

import (
	"context"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// TestMain runs the test binary as the execution checks' helper when it
// is given HelperArg first (Go's helper-process pattern), and the tests
// otherwise.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		os.Exit(RunHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// helperCase is the case that runs this test binary as the helper: its
// race detector told not to wait a second at each exit, as it does unless
// GORACE says otherwise.
func helperCase(t *testing.T) RuntimeCase {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return RuntimeCase{Argv: []string{exe, HelperArg}, Env: []string{"GORACE=atexit_sleep_ms=0"}}
}

// The native Runtime passes every check of the contract: those that start
// nothing and those that start the helper.
func TestTheContractHoldsForTheNativeRuntime(t *testing.T) {
	Runtime(t, nil, local.Native())
	RuntimeExec(t, local.Native(), helperCase(t))
}

// The fake passes the checks that start nothing as it is, and every check
// with the native Runtime as its base, each call it was given recorded,
// its Where its own; without a base, the execution checks name its
// refusals.
func TestTheContractHoldsForTheFake(t *testing.T) {
	t.Run("without a base", func(t *testing.T) {
		f := NewFakeRuntime("fake-runtime", "vm")
		Runtime(t, nil, f)
		rc := helperCase(t)
		errs := record(t, func(t T) { RuntimeExec(t, f, rc) })
		for _, method := range []string{"StartTerminal", "Run", "Serve", "Port"} {
			if !said(errs, "is not scripted", method) {
				t.Errorf("the execution checks did not name the unscripted %s: %q", method, errs)
			}
		}
	})
	t.Run("with the native base", func(t *testing.T) {
		f := NewFakeRuntime("fake-runtime", "vm")
		f.SetBase(local.Native())
		Runtime(t, nil, f)
		RuntimeExec(t, f, helperCase(t))
		seen := map[string]bool{}
		for _, c := range f.RuntimeCalls() {
			seen[c.Method] = true
		}
		for _, method := range []string{"Takes", "StartTerminal", "Run", "Serve", "Probe", "Port"} {
			if !seen[method] {
				t.Errorf("the fake recorded no %s", method)
			}
		}
		if f.Where() != "vm" {
			t.Errorf("Where %q, want the fake's own", f.Where())
		}
	})
}

// broken is the native Runtime with one rule broken, as its fields say.
type broken struct {
	ext.Runtime
	where     ext.Where
	takes     chan struct{}   // Takes waits on it
	extraEnv  bool            // StartTerminal adds a variable
	ignoreCtx bool            // StartTerminal starts under any context
	noStarted bool            // Run never calls Started
	swallow   bool            // Run recovers its Started's panic
	follow    bool            // Probe follows a redirect
	stopNoCtx bool            // a Terminal's Stop ignores its context
	serveBg   bool            // Serve looks at its context once, then serves under none
	leak      context.Context // Run lets its Started's panic go, the process running under leak
}

func (b *broken) Name() string     { return "broken" }
func (b *broken) Where() ext.Where { return b.where }

func (b *broken) Takes(ref ext.ProcessRef) bool {
	if b.takes != nil {
		<-b.takes
	}
	return true
}

func (b *broken) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	if b.extraEnv {
		s.Env = append(slices.Clone(s.Env), "EXTRA=1")
	}
	if b.ignoreCtx {
		ctx = context.Background()
	}
	term, err := b.Runtime.StartTerminal(ctx, s)
	if b.stopNoCtx && term != nil {
		term = ctxlessStop{term}
	}
	return term, err
}

// ctxlessStop is a Terminal whose Stop waits out its grace whatever its
// context does.
type ctxlessStop struct{ ext.Terminal }

func (t ctxlessStop) Stop(_ context.Context, grace time.Duration) error {
	return t.Terminal.Stop(context.Background(), grace)
}

func (b *broken) Serve(ctx context.Context, s ext.CommandSpec) (ext.ServiceProcess, error) {
	if b.serveBg {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ctx = context.Background()
	}
	return b.Runtime.Serve(ctx, s)
}

func (b *broken) Run(ctx context.Context, s ext.CommandSpec) (res ext.CommandResult) {
	if b.noStarted {
		s.Started = nil
	}
	if b.swallow {
		defer func() {
			if recover() != nil {
				res = ext.CommandResult{Exit: -1, Err: "swallowed"}
			}
		}()
	}
	if b.leak != nil && s.Started != nil {
		started, panicked, done := s.Started, make(chan any, 1), make(chan ext.CommandResult, 1)
		s.Started = func(info ext.ProcessInfo) {
			defer func() {
				if p := recover(); p != nil {
					panicked <- p
				}
			}()
			started(info)
		}
		go func() { done <- b.Runtime.Run(b.leak, s) }()
		select {
		case p := <-panicked:
			panic(p) // what it began still running
		case res := <-done:
			return res
		}
	}
	return b.Runtime.Run(ctx, s)
}

func (b *broken) Probe(ctx context.Context, s ext.ProbeSpec) (int, error) {
	if !b.follow {
		return b.Runtime.Probe(ctx, s)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := (&http.Client{Timeout: s.Interval}).Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil
}

// A Runtime that breaks one rule fails RunAll with that rule's words.
func TestRunAllChecksTheRuntime(t *testing.T) {
	stalled := make(chan struct{})
	defer close(stalled)
	leak, stopLeak := context.WithCancel(context.Background())
	defer stopLeak()
	for _, c := range []struct {
		name string
		b    *broken
		want []string
		// wait is the case's Wait for a row that runs one out: long
		// enough for the helper to start under load, short enough that
		// the row does not take the default minute. Zero is the default.
		wait time.Duration
	}{
		{"Takes waits", &broken{where: ext.WhereNative, takes: stalled}, []string{"Takes did not return within"}, 0},
		{"another environment", &broken{extraEnv: true}, []string{"exactly its Env", "EXTRA"}, 0},
		{"a start under an ended context", &broken{ignoreCtx: true}, []string{"ended before the call returned a Terminal"}, 0},
		// A Started never called leaves the panic check's helper to run
		// out its Run's Timeout, the case's Wait.
		{"no Started", &broken{noStarted: true}, []string{"Run called Started 0 times"}, 5 * time.Second},
		{"a panic swallowed", &broken{swallow: true}, []string{"Run returned after its Started panicked"}, 0},
		{"a redirect followed", &broken{follow: true}, []string{"want 302, the redirect not followed"}, 0},
		{"a Stop deaf to its context", &broken{stopNoCtx: true}, []string{"of its context's end", "the context's end forces the process"}, 10 * time.Second},
		{"a service deaf to its context", &broken{serveBg: true}, []string{"after its context ended once it ran: a context's end ends the service"}, 10 * time.Second},
		{"a process left running past a panic", &broken{leak: leak}, []string{"Run let its Started's panic go with the process it started still running"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.b.Runtime = local.Native()
			if c.b.where == "" {
				c.b.where = "broken"
			}
			var opts []RunOption
			if c.b.where != ext.WhereNative { // the native Where's static checks run alone
				rc := helperCase(t)
				rc.Wait = c.wait
				opts = append(opts, WithRuntimeCase(rc))
			}
			errs := record(t, func(t T) { RunAll(t, nil, ext.Providers{Runtime: c.b}, opts...) })
			if !said(errs, c.want...) {
				t.Fatalf("RunAll did not say %q: %q", c.want, errs)
			}
		})
	}
}

// RunAll runs the execution checks with the case it is given: every
// process they start is the case's helper's, and a Runtime that keeps the
// contract passes.
func TestRunAllTakesTheRuntimeCase(t *testing.T) {
	f := NewFakeRuntime("recording", "fake")
	f.SetBase(local.Native())
	rc := helperCase(t)
	RunAll(t, nil, ext.Providers{Runtime: f}, WithRuntimeCase(rc))
	starts := 0
	for _, c := range f.RuntimeCalls() {
		argv := c.Terminal.Argv
		switch c.Method {
		case "Run", "Serve":
			argv = c.Command.Argv
		case "StartTerminal":
		default:
			continue
		}
		starts++
		if len(argv) <= len(rc.Argv) || !slices.Equal(argv[:len(rc.Argv)], rc.Argv) {
			t.Errorf("%s started %q, not the case's helper %q", c.Method, argv, rc.Argv)
		}
	}
	if starts == 0 {
		t.Fatal("the execution checks started nothing")
	}
}

// A Runtime that is not the native one, given RunAll with no case, fails
// it, naming the option, with nothing started; the native one with none
// passes, its static checks run.
func TestRunAllWithoutACaseRefusesAnotherRuntime(t *testing.T) {
	f := NewFakeRuntime("recording", "fake")
	f.SetBase(local.Native())
	errs := record(t, func(t T) { RunAll(t, nil, ext.Providers{Runtime: f}) })
	if !said(errs, "WithRuntimeCase", `"fake"`) {
		t.Fatalf("RunAll without a case did not name WithRuntimeCase: %q", errs)
	}
	takes := false
	for _, c := range f.RuntimeCalls() {
		switch c.Method {
		case "Takes":
			takes = true
		case "StartTerminal", "Run", "Serve", "Probe", "Port":
			t.Errorf("RunAll without a case called %s", c.Method)
		}
	}
	if !takes {
		t.Error("RunAll without a case ran no static check (Takes was not asked)")
	}
	if errs := record(t, func(t T) { RunAll(t, nil, ext.Providers{Runtime: local.Native()}) }); len(errs) != 0 {
		t.Fatalf("RunAll of the native Runtime with no case: %q", errs)
	}
}

// lateStart is the native Runtime whose StartTerminal waits for gate
// before it starts: it sees whether the DirFile it was lent is still open
// then, and its Terminal tells stopped at its Stop.
type lateStart struct {
	ext.Runtime
	gate    chan struct{}
	dirErr  error
	stopped chan struct{}
}

func (l *lateStart) Name() string     { return "late" }
func (l *lateStart) Where() ext.Where { return "late" }
func (l *lateStart) Takes(ext.ProcessRef) bool {
	return true
}

func (l *lateStart) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	<-l.gate
	_, l.dirErr = s.DirFile.Stat()
	term, err := l.Runtime.StartTerminal(ctx, s)
	if term == nil {
		close(l.stopped)
		return nil, err
	}
	return stopTold{term, l.stopped}, err
}

type stopTold struct {
	ext.Terminal
	stopped chan struct{}
}

func (t stopTold) Stop(ctx context.Context, grace time.Duration) error {
	err := t.Terminal.Stop(ctx, grace)
	close(t.stopped)
	return err
}

// A StartTerminal that returns only after the case's Wait fails the check,
// and what it hands back then is still ended: its Terminal is stopped, and
// the DirFile it was lent stays open until its call has unwound.
func TestALateTerminalIsStoppedAndItsDirFileKept(t *testing.T) {
	l := &lateStart{Runtime: local.Native(), gate: make(chan struct{}), stopped: make(chan struct{})}
	rc := helperCase(t)
	errs := record(t, func(t T) {
		c, ok := begin(t, nil, "RuntimeExec", l)
		if !ok {
			return
		}
		defer c.done()
		// The gate opens only once the check has given up, so its Wait
		// running out is certain, however loaded the machine.
		x := &execCheck{c: c, r: l, rc: rc, wait: time.Millisecond}
		dir, _ := x.dir()
		if term, _, ok := x.start(context.Background(), "a late StartTerminal", ext.TerminalSpec{
			Argv: x.argv("hold"), Dir: dir, Env: x.env("late"), Ref: ext.ProcessRef{Session: "exttest-late"},
		}); ok || term != nil {
			t.Errorf("a StartTerminal past its Wait answered ok %v, %v", ok, term)
		}
	})
	if !said(errs, "a late StartTerminal did not return within") {
		t.Errorf("the check did not say the start was late: %q", errs)
	}
	close(l.gate)
	select {
	case <-l.stopped:
	case <-time.After(DefaultRuntimeWait + ext.StopKillWait): // the start's and the Stop's bounds
		t.Fatal("the Terminal handed back late was never stopped")
	}
	if l.dirErr != nil {
		t.Errorf("the DirFile was closed before the late StartTerminal unwound: %v", l.dirErr)
	}
}

// The checks' own pieces: the helper refuses what it does not know, and a
// context that cancels itself does so at the look it is told.
func TestTheExecutionChecksParts(t *testing.T) {
	if code := RunHelper(nil); code != 2 {
		t.Errorf("RunHelper with no mode: %d", code)
	}
	if code := RunHelper([]string{"fly"}); code != 2 {
		t.Errorf("RunHelper of an unknown mode: %d", code)
	}
	if code := RunHelper([]string{"exit", "7"}); code != 7 {
		t.Errorf("RunHelper exit 7: %d", code)
	}
	ctx := newLookCancel(2)
	if ctx.Err() != nil {
		t.Fatal("ended at the first look")
	}
	if ctx.Err() != nil {
		t.Fatal("the look that cancels answered as after it")
	}
	if ctx.Err() == nil {
		t.Fatal("not ended after its second look")
	}
	if got, ok := lineAfter("x\r\nexttest-size:30 100\r\nexttest-ready\r\n", "exttest-size:"); !ok || got != "30 100" {
		t.Errorf("lineAfter: %q %v", got, ok)
	}
	if !strings.HasPrefix(tail(strings.Repeat("a", 1000)), "…") {
		t.Error("tail kept a long output whole")
	}
}
