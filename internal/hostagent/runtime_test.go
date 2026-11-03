package hostagent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// runHosted runs opts against a server of the test's own and answers what
// Run returned, once it has; started, when set, is told the registered id
// once the host has registered.
func runHosted(t *testing.T, opts Options, started func(id string)) (Result, error) {
	t.Helper()
	_, hs := startServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	opts.ServerURL, opts.Token, opts.RelayOnly = hs.URL, hostToken, true
	opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	opts.Registered = func(id, _ string) {
		if started != nil {
			go started(id)
		}
	}
	return Run(ctx, opts)
}

// A host starts its command through its Runtime: the argv, the directory
// and the environment the host built (its session's own names, none of
// the host's CONDUCTOR_), the registered session as the ref; the exit code
// Run returns is the terminal's.
func TestAHostStartsThroughTheRuntime(t *testing.T) {
	t.Setenv("CONDUCTOR_HOSTS_OWN", "leaked")
	dir := t.TempDir()
	procs := make(chan *sessiontest.FakeProc, 1)
	fake := &sessiontest.FakeRuntime{Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
		p := sessiontest.NewFakeProc()
		procs <- p
		return p, nil
	}}
	res, err := runHosted(t, Options{Name: "runtime", Argv: []string{"/bin/agent", "--flag"}, Dir: dir, Runtime: fake}, func(string) {
		(<-procs).End(7)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("Run returned exit code %d, not the terminal's 7", res.ExitCode)
	}
	calls := fake.Asked("StartTerminal")
	if len(calls) != 1 {
		t.Fatalf("%d terminals started", len(calls))
	}
	s := calls[0].Spec.(ext.TerminalSpec)
	abs, _ := filepath.Abs(dir)
	if !slices.Equal(s.Argv, []string{"/bin/agent", "--flag"}) || s.Dir != abs || s.DirFile == nil || s.Cols != 80 || s.Rows != 24 {
		t.Fatalf("the terminal's spec: argv %q dir %q folder %v %dx%d", s.Argv, s.Dir, s.DirFile != nil, s.Cols, s.Rows)
	}
	if calls[0].Ref != (ext.ProcessRef{Session: res.SessionID}) || res.SessionID == "" {
		t.Fatalf("the ref %+v, the session %q", calls[0].Ref, res.SessionID)
	}
	for _, want := range []string{"CONDUCTOR_SESSION_ID=" + res.SessionID, "CONDUCTOR_NOTIFY_TOKEN=", "CONDUCTOR_NOTIFY_URL=", "TERM=xterm-256color"} {
		if !slices.ContainsFunc(s.Env, func(kv string) bool { return strings.HasPrefix(kv, want) }) {
			t.Fatalf("the environment has no %s: %q", want, s.Env)
		}
	}
	if slices.ContainsFunc(s.Env, func(kv string) bool { return strings.HasPrefix(kv, "CONDUCTOR_HOSTS_OWN=") }) {
		t.Fatalf("the host's own CONDUCTOR_ name reached the command: %q", s.Env)
	}
}

// A host given no Runtime runs its command natively, its exit code the
// process's.
func TestAHostWithNoRuntimeRunsNatively(t *testing.T) {
	res, err := runHosted(t, Options{Name: "native", Argv: []string{"/bin/sh", "-c", "exit 3"}, Dir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.SessionID == "" {
		t.Fatalf("Run returned %+v, want the process's exit code 3", res)
	}
}

// A refused start ends Run with the Runtime's error.
func TestAHostWhoseRuntimeRefusesTheStartFails(t *testing.T) {
	fake := &sessiontest.FakeRuntime{}
	if _, err := runHosted(t, Options{Name: "refused", Argv: []string{"/bin/agent"}, Dir: t.TempDir(), Runtime: fake}, nil); err == nil || !strings.Contains(err.Error(), "no answer for StartTerminal") {
		t.Fatalf("Run: %v", err)
	}
}

// A host stopped as its command starts (its context ended once it has
// registered, before the start) still starts the command natively, then
// stops it, and Run returns with no error, as before the seam: the native
// start is not bounded by Run's context.
func TestAHostStoppedAsItStartsStartsAndStops(t *testing.T) {
	_, hs := startServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake := &sessiontest.FakeRuntime{Place: ext.WhereNative, Base: local.Native()}
	_, err := Run(ctx, Options{ServerURL: hs.URL, Token: hostToken, RelayOnly: true, Name: "stopped", Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Runtime: fake,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registered: func(string, string) { cancel() }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls := fake.Asked("StartTerminal"); len(calls) != 1 || calls[0].Process.PID <= 0 {
		t.Fatalf("the start: %+v", calls)
	}
}

// heldStart is a Runtime whose StartTerminal tells entered it has been
// called, then holds the start before its commitment point until its
// context ends, and answers the context's error with nothing started.
type heldStart struct {
	*sessiontest.FakeRuntime
	entered chan struct{}
	ended   chan error
}

func (h heldStart) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	h.FakeRuntime.StartTerminal(ctx, s)
	close(h.entered)
	<-ctx.Done()
	h.ended <- ctx.Err()
	return nil, ctx.Err()
}

// A host stopped while a Runtime that is not the native one holds its
// command's start is stopped: the start is bounded by Run's context, the
// Runtime sees it end, and Run returns the start's error.
func TestAHostStoppedWhileARuntimeHoldsItsStartReturns(t *testing.T) {
	_, hs := startServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rt := heldStart{FakeRuntime: &sessiontest.FakeRuntime{}, entered: make(chan struct{}), ended: make(chan error, 1)}
	go func() {
		<-rt.entered
		cancel()
	}()
	_, err := Run(ctx, Options{ServerURL: hs.URL, Token: hostToken, RelayOnly: true, Name: "held", Argv: []string{"/bin/agent"}, Dir: t.TempDir(), Runtime: rt,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v, want the start's context.Canceled", err)
	}
	if err := <-rt.ended; !errors.Is(err, context.Canceled) {
		t.Fatalf("the Runtime's start saw %v, not its context ended", err)
	}
	if n := len(rt.Asked("StartTerminal")); n != 1 {
		t.Fatalf("%d starts asked", n)
	}
}
