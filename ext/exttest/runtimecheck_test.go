package exttest

import (
	"context"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

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
	takes     chan struct{} // Takes waits on it
	extraEnv  bool          // StartTerminal adds a variable
	ignoreCtx bool          // StartTerminal starts under any context
	noStarted bool          // Run never calls Started
	swallow   bool          // Run recovers its Started's panic
	follow    bool          // Probe follows a redirect
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
	return b.Runtime.StartTerminal(ctx, s)
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
	for _, c := range []struct {
		name string
		b    *broken
		want []string
	}{
		{"Takes waits", &broken{where: ext.WhereNative, takes: stalled}, []string{"Takes did not return within"}},
		{"another environment", &broken{extraEnv: true}, []string{"exactly its Env", "EXTRA"}},
		{"a start under an ended context", &broken{ignoreCtx: true}, []string{"ended before the call returned a Terminal"}},
		{"no Started", &broken{noStarted: true}, []string{"Run called Started 0 times"}},
		{"a panic swallowed", &broken{swallow: true}, []string{"Run returned after its Started panicked"}},
		{"a redirect followed", &broken{follow: true}, []string{"want 302, the redirect not followed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.b.Runtime = local.Native()
			if c.b.where == "" {
				c.b.where = "broken"
			}
			var opts []RunOption
			if c.b.where != ext.WhereNative { // the native Where's static checks run alone
				opts = append(opts, WithRuntimeCase(helperCase(t)))
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
