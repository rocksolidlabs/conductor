package exttest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// RuntimeCase is what the execution checks (RuntimeExec) need beside the
// Runtime: the helper program they drive where the Runtime runs it.
type RuntimeCase struct {
	// Argv runs the helper: each check appends a mode of RunHelper and its
	// arguments. In this repository's tests it is the test binary and
	// HelperArg; a Runtime that runs its processes elsewhere (a guest)
	// names the helper as it is found there.
	Argv []string
	// Env is what the helper needs in its environment beside what the
	// checks set (variables named EXTTEST_): each spec's Env is Env and
	// theirs, which the helper must receive exactly.
	Env []string
	// Wait bounds every wait of the checks for the helper or the Runtime:
	// a hang made a failure, never what a step takes. DefaultRuntimeWait
	// when zero.
	Wait time.Duration
}

// DefaultRuntimeWait is RuntimeCase.Wait when it is zero.
const DefaultRuntimeWait = time.Minute

// WithRuntimeCase gives RunAll the case its Runtime's execution checks
// (RuntimeExec) run with. A Runtime whose Where is not ext.WhereNative with
// no case fails RunAll: another module's CI cannot leave the execution
// checks out by leaving the case out.
func WithRuntimeCase(c RuntimeCase) RunOption {
	c.Argv, c.Env = slices.Clone(c.Argv), slices.Clone(c.Env)
	return func(cfg *runConfig) { cfg.runtime = &c }
}

// RuntimeExec checks the Runtime's methods that start processes, as
// package ext's Runtime contract writes them, by starting the helper rc
// names (RunHelper's modes) through r:
//
//   - StartTerminal starts in the directory given with exactly Env, its
//     output, input, a resize and its exit reaching the caller; DirFile is
//     the caller's to close once it returns; a context ended before the
//     call is answered with its error and no Terminal; one that ends while
//     the start is under way (the context cancels itself once r has looked
//     at it once, twice or three times) with one of the two answers the
//     contract allows, a Terminal returned being a started process.
//   - A Terminal's Stop asks (a helper that exits on the ask: nil, and it
//     exited 0, so it was not forced), forces past the grace or the
//     context's end (a helper that ignores the ask: nil, the ask seen,
//     within StopKillWait of the force), and signals nothing after an exit
//     (nil). Descendants are not checked: the contract promises none.
//   - Run runs an argv, never a shell (arguments with ;, $, ` and * printed
//     as they are), in its Dir with exactly its Env; its exit code; the
//     head and tail of its output and the log within LogLimit; Started
//     once, before any output reaches the log; a timeout told from a
//     cancel; an ended context answered promptly.
//   - A Started that panics leaves Run and Serve as that same panic.
//   - Serve starts a service (Started once, before its output reaches the
//     log) and Stop ends it: done when Stop returns, and Stop may be
//     called again; an ended context is answered promptly, a service
//     answered ending with it.
//   - Port answers a number free where the services listen (the helper's
//     http mode binds it); Probe makes one try and follows no redirect,
//     and answers an ended context with an error and no request.
//
// Every call is bounded by rc's Wait, the product's caps added where a
// method has one (StopKillWait), so a broken Runtime fails rather than
// hangs.
func RuntimeExec(t T, r ext.Runtime, rc RuntimeCase) {
	t.Helper()
	if len(rc.Argv) == 0 || rc.Argv[0] == "" {
		t.Errorf("exttest: RuntimeExec: the RuntimeCase names no helper (Argv)")
		return
	}
	c, ok := begin(t, nil, "RuntimeExec", r)
	if !ok {
		return
	}
	defer c.done()
	x := &execCheck{c: c, r: r, rc: rc, wait: rc.Wait}
	if x.wait <= 0 {
		x.wait = DefaultRuntimeWait
	}
	x.terminal()
	x.terminalUnderEndedContexts()
	x.stops()
	x.run()
	x.runBounds()
	x.startedPanics()
	x.serve()
	x.portAndProbe()
}

// execCheck is one RuntimeExec run.
type execCheck struct {
	c    *checker
	r    ext.Runtime
	rc   RuntimeCase
	wait time.Duration
}

func (x *execCheck) errorf(format string, args ...any) {
	x.c.t.Helper()
	x.c.errorf(format, args...)
}

// argv is the helper's argv in mode with args.
func (x *execCheck) argv(mode string, args ...string) []string {
	return append(append(slices.Clone(x.rc.Argv), mode), args...)
}

// env is a spec's environment: the case's and the checks' own.
func (x *execCheck) env(check string) []string {
	return append(slices.Clone(x.rc.Env), "EXTTEST_CHECK="+check, "EXTTEST_WORDS=a b;c $HOME")
}

// dir is a directory of the test's holding HelperDirFile, and the word it
// holds.
func (x *execCheck) dir() (string, string) {
	dir := x.c.t.TempDir()
	b := make([]byte, 12)
	rand.Read(b)
	mark := "exttest-" + hex.EncodeToString(b)
	if err := os.WriteFile(filepath.Join(dir, HelperDirFile), []byte(mark+"\n"), 0o644); err != nil {
		x.errorf("writing the helper's directory file: %v", err)
	}
	return dir, mark
}

// bounded calls fn on a goroutine of its own and waits for it at most
// wait: ok false when it did not return (it panicked, said, or is still
// running past wait, said).
func bounded[R any](x *execCheck, what string, wait time.Duration, fn func() R) (R, bool) {
	x.c.t.Helper()
	type answer struct {
		v        R
		returned bool
		p        any
	}
	ch := make(chan answer, 1)
	go func() {
		var a answer
		defer func() {
			if !a.returned {
				a.p = recover()
			}
			ch <- a
		}()
		a.v = fn()
		a.returned = true
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var zero R
	select {
	case a := <-ch:
		switch {
		case a.p != nil:
			x.errorf("%s panicked: %v", what, a.p)
			return zero, false
		case !a.returned:
			x.errorf("%s ended its goroutine without returning (runtime.Goexit)", what)
			return zero, false
		}
		return a.v, true
	case <-timer.C:
		x.errorf("%s did not return within %s (the case's Wait)", what, wait)
		return zero, false
	}
}

type started struct {
	term ext.Terminal
	err  error
}

// start calls StartTerminal with s, its DirFile the directory s.Dir opened
// and closed by the check once the call returns: a Runtime that closed it
// is told so. ok false when the call did not return.
func (x *execCheck) start(ctx context.Context, what string, s ext.TerminalSpec) (ext.Terminal, error, bool) {
	x.c.t.Helper()
	if s.Dir != "" {
		f, err := os.Open(s.Dir)
		if err != nil {
			x.errorf("opening the directory for %s: %v", what, err)
			return nil, nil, false
		}
		s.DirFile = f
		defer func() {
			if err := f.Close(); errors.Is(err, os.ErrClosed) {
				x.errorf("%s closed the DirFile it was lent: the caller closes it once StartTerminal returns", what)
			}
		}()
	}
	a, ok := bounded(x, what, x.wait, func() started {
		term, err := x.r.StartTerminal(ctx, s)
		return started{term, err}
	})
	return a.term, a.err, ok
}

// cleanup stops a terminal a check is done with, saying nothing of how.
func (x *execCheck) cleanup(term ext.Terminal) {
	if term == nil {
		return
	}
	bounded(x, "the cleanup's Terminal.Stop", ext.StopKillWait+x.wait, func() error { return term.Stop(context.Background(), 0) })
}

// awaitDone waits for term's process to have exited.
func (x *execCheck) awaitDone(term ext.Terminal, what string) bool {
	x.c.t.Helper()
	timer := time.NewTimer(x.wait)
	defer timer.Stop()
	select {
	case <-term.Done():
		return true
	case <-timer.C:
		x.errorf("%s: Done was not closed within %s (the case's Wait)", what, x.wait)
		return false
	}
}

// terminal checks a terminal's start, its directory, its environment, its
// output, input, resize and exit.
func (x *execCheck) terminal() {
	dir, mark := x.dir()
	env := x.env("terminal")
	args := []string{"a;b", "$HOME"}
	term, err, ok := x.start(context.Background(), "StartTerminal", ext.TerminalSpec{
		Argv: x.argv("terminal", args...), Dir: dir, Env: env, Cols: 100, Rows: 30,
		Ref: ext.ProcessRef{Session: "exttest-terminal"},
	})
	if !ok {
		return
	}
	if err != nil || term == nil {
		x.errorf("StartTerminal of the helper's terminal mode answered %v, %v: want a Terminal", term, err)
		x.cleanup(term)
		return
	}
	defer x.cleanup(term)
	s := readOut(term)
	if !x.await(s, "exttest-ready", "the terminal's helper") {
		return
	}
	x.report(s.text(), "StartTerminal", mark, env, args)
	if got, _ := lineAfter(s.text(), "exttest-size:"); got != "30 100" {
		x.errorf("the terminal started at the size %q, want \"30 100\" (Rows 30, Cols 100)", got)
	}
	if _, ok := bounded(x, "Terminal.Resize", x.wait, func() error {
		if err := term.Resize(120, 40); err != nil {
			x.c.found("Terminal.Resize(120, 40) answered %v", err)
		}
		return nil
	}); !ok {
		return
	}
	if !x.await(s, "exttest-size:40 120", "the resize to 120 by 40 reaching the process") {
		return
	}
	if _, ok := bounded(x, "Terminal.Write", x.wait, func() error {
		if _, err := term.Write([]byte("hello\n")); err != nil {
			x.c.found("Terminal.Write answered %v", err)
		}
		return nil
	}); !ok {
		return
	}
	if !x.await(s, "exttest-got:hello", "the input reaching the process") {
		return
	}
	if !x.awaitDone(term, "the terminal's helper, which exits 3 once it has read a line") {
		return
	}
	if st := term.Exit(); st.Code != 3 || st.Signal != "" {
		x.errorf("the terminal's helper exited 3, and Exit answered %+v", st)
	}
}

// report checks what the helper's report mode printed in out: started in
// the directory holding mark, with exactly env and args.
func (x *execCheck) report(out, method, mark string, env, args []string) {
	x.c.t.Helper()
	if got, ok := lineAfter(out, "exttest-dir:"); !ok || got != mark {
		why, _ := lineAfter(out, "exttest-dir-error:")
		x.errorf("%s did not start the helper in the directory it was given: it read %q from %s there (%s), want %q", method, got, HelperDirFile, why, mark)
	}
	var gotEnv []string
	if line, _ := lineAfter(out, "exttest-env:"); json.Unmarshal([]byte(line), &gotEnv) != nil {
		x.errorf("%s: the helper printed no environment", method)
	} else if want := slices.Sorted(slices.Values(env)); !slices.Equal(gotEnv, want) {
		// Names only: an environment holds tokens.
		x.errorf("%s did not give the helper exactly its Env: the helper's holds %q, want exactly %q", method, names(gotEnv), names(want))
	}
	var gotArgs []string
	if line, _ := lineAfter(out, "exttest-args:"); json.Unmarshal([]byte(line), &gotArgs) != nil || !slices.Equal(gotArgs, args) {
		x.errorf("%s did not run its argv as it is (never a shell): the helper's arguments were %q, want %q", method, gotArgs, args)
	}
}

func names(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		out = append(out, k)
	}
	return out
}

// lineAfter is what follows the first prefix in out up to its line's end,
// a terminal's "\r" dropped.
func lineAfter(out, prefix string) (string, bool) {
	i := strings.Index(out, prefix)
	if i < 0 {
		return "", false
	}
	rest := out[i+len(prefix):]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimRight(rest, "\r"), true
}

// lookCancel is a context that cancels itself once it has been looked at
// (Err, Done or Deadline) a number of times: the look that cancels it
// answers as before the cancel, so the Runtime goes on as if it ended just
// after it looked.
type lookCancel struct {
	context.Context
	cancel context.CancelFunc
	left   atomic.Int32
}

func newLookCancel(looks int) *lookCancel {
	ctx, cancel := context.WithCancel(context.Background())
	l := &lookCancel{Context: ctx, cancel: cancel}
	l.left.Store(int32(looks))
	return l
}

func (l *lookCancel) look() {
	if l.left.Add(-1) == 0 {
		l.cancel()
	}
}

func (l *lookCancel) Err() error {
	err := l.Context.Err()
	l.look()
	return err
}

func (l *lookCancel) Done() <-chan struct{} {
	defer l.look()
	return l.Context.Done()
}

func (l *lookCancel) Deadline() (time.Time, bool) {
	defer l.look()
	return l.Context.Deadline()
}

// terminalUnderEndedContexts checks StartTerminal under a context ended
// before the call, and under one that ends while the start is under way.
func (x *execCheck) terminalUnderEndedContexts() {
	dir, _ := x.dir()
	spec := func(check string) ext.TerminalSpec {
		return ext.TerminalSpec{Argv: x.argv("hold"), Dir: dir, Env: x.env(check), Ref: ext.ProcessRef{Session: "exttest-" + check}}
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	term, err, ok := x.start(ended, "StartTerminal under an ended context", spec("ended"))
	if ok {
		if term != nil {
			x.errorf("StartTerminal under a context ended before the call returned a Terminal: it is answered with the context's error and nothing is started")
			x.cleanup(term)
		}
		if !errors.Is(err, context.Canceled) {
			x.errorf("StartTerminal under a context ended before the call answered %v, want its error (context.Canceled)", err)
		}
	}
	for looks := 1; looks <= 3; looks++ {
		ctx := newLookCancel(looks)
		what := fmt.Sprintf("StartTerminal under a context that ends after %d look(s) at it", looks)
		term, err, ok := x.start(ctx, what, spec("ending"))
		ctx.cancel()
		if !ok {
			continue
		}
		switch {
		case term != nil && err != nil:
			x.errorf("%s answered a Terminal and an error (%v): one of the two, never both", what, err)
			x.cleanup(term)
		case term != nil:
			// Past its commitment point: a started process, the caller's.
			s := readOut(term)
			x.await(s, "exttest-ready", what+": the Terminal it returned, a started process,")
			if err, ok := bounded(x, what+": the Terminal's Stop", ext.StopKillWait+x.wait, func() error { return term.Stop(context.Background(), 0) }); ok && err != nil {
				x.errorf("%s: the Terminal it returned did not stop: %v", what, err)
			}
		case errors.Is(err, context.Canceled):
			// Before its commitment point: abandoned.
		default:
			x.errorf("%s answered %v: want the context's error, or a Terminal", what, err)
		}
	}
}

// stops checks a Terminal's Stop: asked, forced past the context's end and
// past the grace, and after an exit.
func (x *execCheck) stops() {
	dir, _ := x.dir()
	begin := func(mode, check string) (ext.Terminal, *screen) {
		argv := x.argv(mode)
		if mode == "exit" {
			argv = x.argv(mode, "0")
		}
		term, err, ok := x.start(context.Background(), "StartTerminal for "+check, ext.TerminalSpec{
			Argv: argv, Dir: dir, Env: x.env(check), Ref: ext.ProcessRef{Session: "exttest-" + check},
		})
		if !ok {
			return nil, nil
		}
		if err != nil || term == nil {
			x.errorf("StartTerminal for %s answered %v, %v: want a Terminal", check, term, err)
			x.cleanup(term)
			return nil, nil
		}
		s := readOut(term)
		if mode != "exit" && !x.await(s, "exttest-ready", "the helper for "+check) {
			x.cleanup(term)
			return nil, nil
		}
		return term, s
	}

	// Asked: the helper exits 0 on the ask, within a grace it never reaches.
	if term, _ := begin("asked", "stop-asked"); term != nil {
		err, ok := bounded(x, "Terminal.Stop of a process that exits on the ask", x.wait+ext.StopKillWait+x.wait, func() error {
			return term.Stop(context.Background(), x.wait)
		})
		if ok && err != nil {
			x.errorf("Terminal.Stop of a process that exits on the ask answered %v, want nil", err)
		}
		if ok && x.awaitDone(term, "a Terminal stopped") {
			if st := term.Exit(); st.Code != 0 || st.Signal != "" {
				x.errorf("Terminal.Stop did not ask the process to end before forcing it: the helper exits 0 on the ask, and ended %+v", st)
			}
		}
		x.cleanup(term)
	}

	// Forced once the context ends: the helper says it was asked, and
	// ignores it.
	if term, s := begin("ignore", "stop-forced"); term != nil {
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan error, 1)
		go func() { got <- term.Stop(ctx, x.wait) }()
		asked := x.await(s, "exttest-asked", "Terminal.Stop asking the process to end (the helper prints exttest-asked at the ask)")
		cancel()
		timer := time.NewTimer(ext.StopKillWait + x.wait)
		select {
		case err := <-got:
			if err != nil {
				x.errorf("Terminal.Stop of a process that ignores the ask, its context ended, answered %v, want nil once the process is forced and reaped", err)
			}
			if asked {
				x.awaitDone(term, "a Terminal forced")
			}
		case <-timer.C:
			x.errorf("Terminal.Stop of a process that ignores the ask did not return within StopKillWait (%s) of its context's end, and the case's Wait", ext.StopKillWait)
		}
		timer.Stop()
		x.cleanup(term)
	}

	// Forced past the grace: none given.
	if term, _ := begin("ignore", "stop-grace"); term != nil {
		err, ok := bounded(x, "Terminal.Stop with no grace of a process that ignores the ask", ext.StopKillWait+x.wait, func() error {
			return term.Stop(context.Background(), 0)
		})
		if ok && err != nil {
			x.errorf("Terminal.Stop with no grace of a process that ignores the ask answered %v, want nil once it is forced and reaped", err)
		}
		if ok {
			x.awaitDone(term, "a Terminal forced past its grace")
		}
		x.cleanup(term)
	}

	// After an exit: nothing to signal.
	if term, _ := begin("exit", "stop-exited"); term != nil {
		if x.awaitDone(term, "the helper that exits at once") {
			err, ok := bounded(x, "Terminal.Stop after the process exited", x.wait, func() error { return term.Stop(context.Background(), x.wait) })
			if ok && err != nil {
				x.errorf("Terminal.Stop after the process exited answered %v, want nil", err)
			}
		}
		x.cleanup(term)
	}
}

// startCount is a CommandSpec's Started and Log for a check: how many
// times Started was called, and whether output reached the log first.
type startCount struct {
	n     atomic.Int32
	early atomic.Bool
	log   *screen
}

func newStartCount() *startCount { return &startCount{log: newScreen()} }

func (sc *startCount) started(ext.ProcessInfo) { sc.n.Add(1) }

func (sc *startCount) Write(b []byte) (int, error) {
	if sc.n.Load() == 0 {
		sc.early.Store(true)
	}
	return sc.log.Write(b)
}

// run checks a command's argv, directory, environment and exit, and
// Started.
func (x *execCheck) run() {
	dir, mark := x.dir()
	env := x.env("run")
	args := []string{"a;b", "$HOME", "`id`", "x y", "*"}
	sc := newStartCount()
	res, ok := bounded(x, "Run", x.wait+x.wait, func() ext.CommandResult {
		return x.r.Run(context.Background(), ext.CommandSpec{Argv: x.argv("report", args...), Dir: dir, Env: env,
			Timeout: x.wait, Log: sc, Started: sc.started, Ref: ext.ProcessRef{Run: "exttest-run"}})
	})
	if !ok {
		return
	}
	if res.Exit != 0 || res.Err != "" || res.TimedOut || res.Canceled {
		x.errorf("Run of the helper's report mode answered Exit %d (%q), TimedOut %v, Canceled %v: want 0", res.Exit, res.Err, res.TimedOut, res.Canceled)
	}
	x.report(string(res.Head), "Run", mark, env, args)
	if n := sc.n.Load(); n != 1 {
		x.errorf("Run called Started %d times, want once", n)
	}
	if sc.early.Load() {
		x.errorf("Run gave the log output before it called Started: Started is called once the process exists and before its output is read")
	}
	if !strings.Contains(sc.log.text(), "exttest-args:") {
		x.errorf("Run's log did not get the output: %d bytes", len(sc.log.text()))
	}
	if res, ok := bounded(x, "Run of exit 3", x.wait+x.wait, func() ext.CommandResult {
		return x.r.Run(context.Background(), ext.CommandSpec{Argv: x.argv("exit", "3"), Dir: dir, Env: env, Timeout: x.wait})
	}); ok && (res.Exit != 3 || res.TimedOut || res.Canceled) {
		x.errorf("Run of a command that exits 3 answered Exit %d (%q), TimedOut %v, Canceled %v", res.Exit, res.Err, res.TimedOut, res.Canceled)
	}
}

// runBounds checks a command's head, tail and log, its timeout told from
// a cancel, and an ended context.
func (x *execCheck) runBounds() {
	dir, _ := x.dir()
	env := x.env("run-bounds")
	const n, keep, logLimit = 10000, 100, 1000
	log := newScreen()
	res, ok := bounded(x, "Run of spew", x.wait+x.wait, func() ext.CommandResult {
		return x.r.Run(context.Background(), ext.CommandSpec{Argv: x.argv("spew", strconv.Itoa(n)), Dir: dir, Env: env,
			Timeout: x.wait, Head: keep, Tail: keep, Log: log, LogLimit: logLimit})
	})
	if ok {
		all := make([]byte, n)
		for i := range all {
			all[i] = Spewed(i)
		}
		if res.Exit != 0 || !bytes.Equal(res.Head, all[:keep]) || !bytes.Equal(res.Tail, all[n-keep:]) || res.Total != n || !res.Truncated {
			x.errorf("Run of %d bytes of output kept Head %d and Tail %d bytes (Head %d, Tail %d asked; the right bytes: %v, %v), Total %d, Truncated %v, Exit %d: want its first and last %d bytes, Total %d, Truncated",
				n, len(res.Head), len(res.Tail), keep, keep, bytes.Equal(res.Head, all[:keep]), bytes.Equal(res.Tail, all[n-keep:]), res.Total, res.Truncated, res.Exit, keep, n)
		}
		if got := len(log.text()); got == 0 || got > logLimit {
			x.errorf("Run's log got %d bytes of %d with LogLimit %d: want some, and at most LogLimit", got, n, logLimit)
		}
	}

	// A timeout: the helper never ends on its own.
	if res, ok := bounded(x, "Run past its timeout", x.wait+x.wait, func() ext.CommandResult {
		return x.r.Run(context.Background(), ext.CommandSpec{Argv: x.argv("hold"), Dir: dir, Env: env, Timeout: 100 * time.Millisecond})
	}); ok && (!res.TimedOut || res.Canceled || res.Exit == 0) {
		x.errorf("Run past its Timeout answered TimedOut %v, Canceled %v, Exit %d: want TimedOut alone", res.TimedOut, res.Canceled, res.Exit)
	}

	// A cancel: the context ends once the helper says it is ready.
	ctx, cancel := context.WithCancel(context.Background())
	log = newScreen()
	got := make(chan ext.CommandResult, 1)
	go func() {
		defer log.end()
		got <- x.r.Run(ctx, ext.CommandSpec{Argv: x.argv("hold"), Dir: dir, Env: env, Timeout: x.wait + x.wait, Log: log})
	}()
	x.await(log, "exttest-ready", "Run's helper, through the log,")
	cancel()
	timer := time.NewTimer(x.wait)
	select {
	case res := <-got:
		if !res.Canceled || res.TimedOut || res.Exit == 0 {
			x.errorf("Run whose context was cancelled answered Canceled %v, TimedOut %v, Exit %d: want Canceled alone", res.Canceled, res.TimedOut, res.Exit)
		}
	case <-timer.C:
		x.errorf("Run did not return within %s (the case's Wait) of its context's cancel", x.wait)
	}
	timer.Stop()

	// An ended context: answered promptly.
	ended, end := context.WithCancel(context.Background())
	end()
	if res, ok := bounded(x, "Run under an ended context", x.wait, func() ext.CommandResult {
		return x.r.Run(ended, ext.CommandSpec{Argv: x.argv("hold"), Dir: dir, Env: env, Timeout: x.wait})
	}); ok && (!res.Canceled || res.Exit == 0) {
		x.errorf("Run under a context ended before the call answered Canceled %v, Exit %d: want Canceled", res.Canceled, res.Exit)
	}
}

// startedPanic is what a check's Started panics with.
type startedPanic struct{ method string }

// startedPanics checks that a Started that panics leaves Run and Serve as
// that same panic.
func (x *execCheck) startedPanics() {
	dir, _ := x.dir()
	for _, method := range []string{"Run", "Serve"} {
		p := &startedPanic{method}
		spec := ext.CommandSpec{Argv: x.argv("exit", "0"), Dir: dir, Env: x.env("started-panics"), Timeout: x.wait,
			Started: func(ext.ProcessInfo) { panic(p) }}
		type answer struct {
			p        any
			returned bool
			svc      ext.ServiceProcess
		}
		ch := make(chan answer, 1)
		go func() {
			var a answer
			defer func() {
				if !a.returned {
					a.p = recover()
				}
				ch <- a
			}()
			if method == "Run" {
				x.r.Run(context.Background(), spec)
			} else {
				a.svc, _ = x.r.Serve(context.Background(), spec)
			}
			a.returned = true
		}()
		timer := time.NewTimer(x.wait)
		select {
		case a := <-ch:
			switch {
			case a.returned:
				x.errorf("%s returned after its Started panicked: the same panic leaves it, once what it began is ended", method)
				if a.svc != nil {
					bounded(x, "the cleanup's ServiceProcess.Stop", x.wait+ext.StopKillWait, func() error { return a.svc.Stop(context.Background()) })
				}
			case a.p != p:
				x.errorf("%s panicked with %v after its Started panicked: want the same panic, unchanged", method, a.p)
			}
		case <-timer.C:
			x.errorf("%s did not return or panic within %s (the case's Wait) after its Started panicked", method, x.wait)
		}
		timer.Stop()
	}
}

// serve checks a service started and stopped.
func (x *execCheck) serve() {
	dir, _ := x.dir()
	sc := newStartCount()
	type served struct {
		svc ext.ServiceProcess
		err error
	}
	a, ok := bounded(x, "Serve", x.wait, func() served {
		svc, err := x.r.Serve(context.Background(), ext.CommandSpec{Argv: x.argv("hold"), Dir: dir, Env: x.env("serve"),
			Log: sc, Started: sc.started, Ref: ext.ProcessRef{Run: "exttest-serve"}})
		return served{svc, err}
	})
	if !ok {
		return
	}
	if a.err != nil || a.svc == nil {
		x.errorf("Serve of the helper answered %v, %v: want a ServiceProcess", a.svc, a.err)
		return
	}
	go func() {
		<-a.svc.Done()
		sc.log.end()
	}()
	x.await(sc.log, "exttest-ready", "the service's output reaching its log")
	if n := sc.n.Load(); n != 1 {
		x.errorf("Serve called Started %d times, want once", n)
	}
	if sc.early.Load() {
		x.errorf("Serve gave the log output before it called Started: Started is called once the process exists and before its output is read")
	}
	if err, ok := bounded(x, "ServiceProcess.Stop", x.wait+ext.StopKillWait, func() error { return a.svc.Stop(context.Background()) }); ok {
		if err != nil {
			x.errorf("ServiceProcess.Stop answered %v, want nil", err)
		}
		select {
		case <-a.svc.Done():
		default:
			x.errorf("ServiceProcess.Stop returned before the service was done: it returns once Done is closed")
		}
	}
	if err, ok := bounded(x, "ServiceProcess.Stop again", x.wait+ext.StopKillWait, func() error { return a.svc.Stop(context.Background()) }); ok && err != nil {
		x.errorf("ServiceProcess.Stop a second time answered %v: it may be called more than once", err)
	}

	ended, end := context.WithCancel(context.Background())
	end()
	if b, ok := bounded(x, "Serve under an ended context", x.wait, func() served {
		svc, err := x.r.Serve(ended, ext.CommandSpec{Argv: x.argv("hold"), Dir: dir, Env: x.env("serve-ended")})
		return served{svc, err}
	}); ok && b.svc != nil {
		timer := time.NewTimer(x.wait)
		select {
		case <-b.svc.Done():
		case <-timer.C:
			x.errorf("Serve under an ended context answered a service still running %s later: a context's end ends the service", x.wait)
			bounded(x, "the cleanup's ServiceProcess.Stop", x.wait+ext.StopKillWait, func() error { return b.svc.Stop(context.Background()) })
		}
		timer.Stop()
	}
}

// portAndProbe checks Port's number, bound by the helper's http mode, and
// Probe's one try against it.
func (x *execCheck) portAndProbe() {
	ref := ext.ProcessRef{Run: "exttest-probe", Task: "probe", Attempt: 1, Tree: "worktree"}
	type number struct {
		n   int
		err error
	}
	ended, end := context.WithCancel(context.Background())
	end()
	bounded(x, "Port under an ended context", x.wait, func() number {
		n, err := x.r.Port(ended, ref)
		return number{n, err}
	})
	p, ok := bounded(x, "Port", x.wait, func() number {
		n, err := x.r.Port(context.Background(), ref)
		return number{n, err}
	})
	if !ok {
		return
	}
	if p.err != nil || p.n < 1 || p.n > 65535 {
		x.errorf("Port answered %d, %v: want a port's number", p.n, p.err)
		return
	}
	dir, _ := x.dir()
	log := newScreen()
	type served struct {
		svc ext.ServiceProcess
		err error
	}
	a, ok := bounded(x, "Serve of the helper's http mode", x.wait, func() served {
		svc, err := x.r.Serve(context.Background(), ext.CommandSpec{Argv: x.argv("http", strconv.Itoa(p.n)), Dir: dir, Env: x.env("probe"), Log: log, Ref: ref})
		return served{svc, err}
	})
	if !ok {
		return
	}
	if a.err != nil || a.svc == nil {
		x.errorf("Serve of the helper's http mode answered %v, %v: want a ServiceProcess", a.svc, a.err)
		return
	}
	defer bounded(x, "the cleanup's ServiceProcess.Stop", x.wait+ext.StopKillWait, func() error { return a.svc.Stop(context.Background()) })
	go func() {
		<-a.svc.Done()
		log.end()
	}()
	if !x.await(log, "exttest-listening", fmt.Sprintf("the helper listening on the port Port answered (%d), which is free where the services listen", p.n)) {
		return
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", p.n)
	probe := func(ctx context.Context, path string) number {
		n, err := x.r.Probe(ctx, ext.ProbeSpec{URL: base + path, Interval: x.wait, Ref: ref})
		return number{n, err}
	}
	if got, ok := bounded(x, "Probe of a redirect", x.wait+x.wait, func() number { return probe(context.Background(), "/redirect") }); ok && (got.err != nil || got.n != 302) {
		x.errorf("Probe of an answer that redirects answered %d, %v: want 302, the redirect not followed", got.n, got.err)
	}
	if got, ok := bounded(x, "Probe under an ended context", x.wait, func() number { return probe(ended, "/ok?ended") }); ok && got.err == nil {
		x.errorf("Probe under a context ended before the call answered %d and no error", got.n)
	}
	if got, ok := bounded(x, "Probe", x.wait+x.wait, func() number { return probe(context.Background(), "/last") }); ok && (got.err != nil || got.n != 204) {
		x.errorf("Probe answered %d, %v: want 204", got.n, got.err)
	}
	if !x.await(log, "exttest-request:/last", "the service told of the last probe") {
		return
	}
	var asked []string
	for _, l := range strings.Split(log.text(), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(l, "\r"), "exttest-request:"); ok {
			asked = append(asked, v)
		}
	}
	if !slices.Equal(asked, []string{"/redirect", "/last"}) {
		x.errorf("the service was asked %q by Probe of /redirect, an ended context and /last: want [\"/redirect\" \"/last\"], one try each, no redirect followed and no request under an ended context", asked)
	}
}

// screen is output gathered as it comes, from a terminal (readOut) or as
// a log writer, so a check waits for what it holds.
type screen struct {
	mu    sync.Mutex
	out   []byte
	more  chan struct{}
	ended chan struct{}
	once  sync.Once
}

func newScreen() *screen {
	return &screen{more: make(chan struct{}, 1), ended: make(chan struct{})}
}

// readOut gathers r's output until it ends.
func readOut(r io.Reader) *screen {
	s := newScreen()
	go func() {
		defer s.end()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			s.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	return s
}

// end says a log screen's output is over: the command whose log it is
// has returned, or its service is done.
func (s *screen) end() { s.once.Do(func() { close(s.ended) }) }

func (s *screen) Write(b []byte) (int, error) {
	s.mu.Lock()
	s.out = append(s.out, b...)
	s.mu.Unlock()
	select {
	case s.more <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (s *screen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.out)
}

// await waits until s holds want: false, said, when its output ended or
// the case's Wait passed first.
func (x *execCheck) await(s *screen, want, what string) bool {
	x.c.t.Helper()
	timer := time.NewTimer(x.wait)
	defer timer.Stop()
	for {
		if strings.Contains(s.text(), want) {
			return true
		}
		select {
		case <-s.more:
		case <-s.ended:
			if strings.Contains(s.text(), want) {
				return true
			}
			x.errorf("%s: the output ended without %q: %q", what, want, tail(s.text()))
			return false
		case <-timer.C:
			x.errorf("%s: no %q within %s (the case's Wait): %q", what, want, x.wait, tail(s.text()))
			return false
		}
	}
}

// tail is the end of out, for a failure's words.
func tail(out string) string {
	const most = 400
	if len(out) > most {
		return "…" + out[len(out)-most:]
	}
	return out
}
