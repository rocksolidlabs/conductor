package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proc"
	"github.com/rocksolidlabs/conductor/internal/pty"
)

// hang bounds a wait that should end at once, so a broken start fails the
// test rather than hanging it: no cap of the code's, never what a normal
// run takes.
const hang = 30 * time.Second

// testEnv is the environment a test's command starts with: the session's,
// as Conductor builds it.
func testEnv() []string { return pty.BuildEnv(os.Environ(), nil, nil, nil) }

// screen reads a terminal's output until it ends, so a test waits for what
// it prints.
type screen struct {
	mu    sync.Mutex
	out   []byte
	more  chan struct{}
	ended chan struct{}
}

func read(t ext.Terminal) *screen {
	s := &screen{more: make(chan struct{}, 1), ended: make(chan struct{})}
	go func() {
		defer close(s.ended)
		buf := make([]byte, 4096)
		for {
			n, err := t.Read(buf)
			s.mu.Lock()
			s.out = append(s.out, buf[:n]...)
			s.mu.Unlock()
			select {
			case s.more <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *screen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.out)
}

// waitFor waits until the output holds want.
func (s *screen) waitFor(t *testing.T, want string) {
	t.Helper()
	bound := time.NewTimer(hang)
	defer bound.Stop()
	for !strings.Contains(s.text(), want) {
		select {
		case <-s.more:
		case <-s.ended:
			if !strings.Contains(s.text(), want) {
				t.Fatalf("the output ended without %q: %q", want, s.text())
			}
		case <-bound.C:
			t.Fatalf("no %q in the output: %q", want, s.text())
		}
	}
}

// done waits for the terminal's process to exit.
func done(t *testing.T, term ext.Terminal) {
	t.Helper()
	select {
	case <-term.Done():
	case <-time.After(hang):
		t.Fatal("the process did not exit")
	}
}

// pidIn waits for the file at path to hold a pid, written by the test's
// command, and answers it.
func pidIn(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(hang)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// reaped says the process pid is gone and reaped: a zombie still answers.
func reaped(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

// goneSoon waits for pid to be gone: a process killed with its group that
// was not ours to reap is reaped by its new parent.
func goneSoon(t *testing.T, pid int) bool {
	t.Helper()
	deadline := time.Now().Add(hang)
	for !reaped(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// exited says the process pid has exited: gone, or a zombie its parent has
// not reaped (as a held process's leader is until it is released).
func exited(pid int) bool {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		_, rest, _ := strings.Cut(string(b), ") ") // after the command's name
		return strings.HasPrefix(rest, "Z") || strings.HasPrefix(rest, "X")
	} else if _, err := os.Stat("/proc/self/stat"); err == nil {
		return true // a /proc without the pid: gone
	}
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	st := strings.TrimSpace(string(out))
	return st == "" || strings.HasPrefix(st, "Z")
}

// exitedSoon waits for pid to have exited (exited).
func exitedSoon(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(hang)
	for !exited(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the process %d did not exit", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// counting is a seam whose start counts pty.StartHeld's calls.
func counting(calls *atomic.Int32) *startSeam {
	return &startSeam{start: func(s pty.Spec) (*pty.Process, error) {
		calls.Add(1)
		return pty.StartHeld(s)
	}}
}

// stopped stops term at the test's end, whatever the test did.
func stopped(t *testing.T, term ext.Terminal) {
	t.Cleanup(func() { term.Stop(context.Background(), 0) })
}

// The native Runtime starts a terminal in the folder it was given: on
// Linux in the directory opened even once its path names another (the
// folder renamed after it was opened, as internal/pty's start tests have
// it); elsewhere in the path checked to still name it. Output, input and a
// resize reach the process, which exits 0, with a pid. The folder is
// lent: still open when StartTerminal returns (the Runtime neither closes
// it nor needs it after), and the test closes it then.
func TestNativeStartsATerminalInItsFolder(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := dir
	if runtime.GOOS == "linux" {
		other := filepath.Join(base, "other")
		os.Mkdir(other, 0o755)
		if err := os.Rename(dir, dir+".was"); err != nil {
			t.Fatal(err)
		}
		os.Symlink(other, dir)
		want = dir + ".was"
	}
	term, err := Native().StartTerminal(context.Background(), ext.TerminalSpec{
		Argv: []string{"/bin/sh", "-c", "pwd -P; stty size; read line; stty size; echo got:$line; exit 0"},
		Dir:  dir, DirFile: f, Env: testEnv(), Cols: 100, Rows: 30, Ref: ext.ProcessRef{Session: "s1"},
	})
	if _, serr := f.Stat(); serr != nil {
		t.Fatalf("the lent folder was closed: %v", serr)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("closing the lent folder: %v", cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	stopped(t, term)
	s := read(term)
	s.waitFor(t, "30 100")
	if got := strings.SplitN(strings.TrimSpace(s.text()), "\n", 2)[0]; strings.TrimSpace(got) != want {
		t.Fatalf("started in %q, want %q", got, want)
	}
	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	s.waitFor(t, "got:hello")
	if !strings.Contains(s.text(), "40 120") {
		t.Fatalf("the resize did not reach the process: %q", s.text())
	}
	done(t, term)
	if st := term.Exit(); st.Code != 0 || st.Signal != "" || st.Err != "" {
		t.Fatalf("exit %+v", st)
	}
	if p := term.Process(); p.PID <= 0 || p.Group != p.PID || p.StartedAt.IsZero() {
		t.Fatalf("process %+v", p)
	}
}

// The terminal starts with exactly s.Env: a nil Env is an empty
// environment, with a folder or without one, never the server's own (a
// variable only the server has is not there); an empty one is empty; one
// given is that one.
func TestNativeStartGivesExactlyItsEnvironment(t *testing.T) {
	t.Setenv("CONDUCTOR_TEST_SERVER_ONLY", "server-secret")
	dir := t.TempDir()
	given := []string{"ONLY=this", "CONDUCTOR_RUN=build-00000001"}
	for _, c := range []struct {
		name    string
		env     []string
		dirFile bool
		want    []string
	}{
		{"nil", nil, false, nil},
		{"nil with a folder", nil, true, nil},
		{"empty", []string{}, false, nil},
		{"empty with a folder", []string{}, true, nil},
		{"given", given, false, given},
		{"given with a folder", given, true, given},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := ext.TerminalSpec{Argv: []string{"/bin/sh", "-c", "echo begin; /usr/bin/env; echo end"}, Dir: dir, Env: c.env}
			if c.dirFile {
				f, err := os.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				spec.DirFile = f
			}
			term, err := Native().StartTerminal(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			stopped(t, term)
			s := read(term)
			s.waitFor(t, "end\r\n")
			out := s.text()
			// A failure names variables, never their values: the server's
			// environment holds tokens.
			if strings.Contains(out, "server-secret") {
				t.Fatal("the server's environment reached the terminal: CONDUCTOR_TEST_SERVER_ONLY is set")
			}
			_, body, _ := strings.Cut(out, "begin\r\n")
			body, _, _ = strings.Cut(body, "end\r\n")
			var got []string
			for _, l := range strings.Split(strings.TrimSpace(strings.ReplaceAll(body, "\r", "")), "\n") {
				if k, _, _ := strings.Cut(l, "="); l != "" && k != "PWD" && k != "SHLVL" && k != "_" && k != "OLDPWD" { // the shell's own
					got = append(got, l)
				}
			}
			slices.Sort(got)
			if want := slices.Sorted(slices.Values(c.want)); !slices.Equal(got, want) {
				var names []string
				for _, l := range got {
					k, _, _ := strings.Cut(l, "=")
					names = append(names, k)
				}
				t.Fatalf("the environment holds %q, want exactly %q", names, want)
			}
		})
	}
}

// A context ended before the call is answered with its error, and no
// pty.Start is made.
func TestNativeStartRefusesAnEndedContext(t *testing.T) {
	var calls atomic.Int32
	r := native{seam: counting(&calls)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	term, err := r.StartTerminal(ctx, ext.TerminalSpec{Argv: []string{"/bin/cat"}, Env: testEnv()})
	if !errors.Is(err, context.Canceled) || term != nil {
		t.Fatalf("StartTerminal under an ended context: %v %v", term, err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("pty.Start was called %d times", n)
	}
}

// A context cancelled while the start is held before its commitment point
// (the seam's gate) is answered with its error, and no pty.Start is made.
func TestNativeStartCancelledWhileGated(t *testing.T) {
	var calls atomic.Int32
	seam := counting(&calls)
	entered, release := make(chan struct{}), make(chan struct{})
	seam.gate = func() {
		close(entered)
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type answer struct {
		term ext.Terminal
		err  error
	}
	got := make(chan answer, 1)
	go func() {
		term, err := native{seam: seam}.StartTerminal(ctx, ext.TerminalSpec{Argv: []string{"/bin/cat"}, Env: testEnv()})
		got <- answer{term, err}
	}()
	<-entered
	cancel()
	close(release)
	a := <-got
	if !errors.Is(a.err, context.Canceled) || a.term != nil {
		t.Fatalf("StartTerminal cancelled while gated: %v %v", a.term, a.err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("pty.Start was called %d times", n)
	}
}

// callersTerminal checks that a Terminal StartTerminal returned under a
// context that ended past its commitment point is a started process, the
// caller's: it runs, and its Stop ends it.
func callersTerminal(t *testing.T, term ext.Terminal, err error) {
	t.Helper()
	if err != nil || term == nil {
		t.Fatalf("StartTerminal: %v %v", term, err)
	}
	stopped(t, term)
	select {
	case <-term.Done():
		t.Fatalf("the process exited: %+v", term.Exit())
	default:
	}
	if pid := term.Process().PID; pid <= 0 || syscall.Kill(pid, 0) != nil {
		t.Fatalf("no process runs: %+v", term.Process())
	}
	if err := term.Stop(context.Background(), hang); err != nil {
		t.Fatal(err)
	}
	done(t, term)
}

// A context that ends while pty.Start runs (the seam's start cancels it,
// then starts) is past the commitment point: the Terminal is returned with
// no error, its process runs, and its Stop ends it.
func TestNativeStartCancelledDuringTheForkIsTheCallers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seam := &startSeam{start: func(s pty.Spec) (*pty.Process, error) {
		cancel()
		return pty.StartHeld(s)
	}}
	term, err := native{seam: seam}.StartTerminal(ctx, ext.TerminalSpec{Argv: []string{"/bin/cat"}, Env: testEnv()})
	if ctx.Err() == nil {
		t.Fatal("the context did not end during the start")
	}
	callersTerminal(t, term, err)
}

// A context that ends once pty.Start has returned (cancelled from the
// seam's hook) changes nothing: the Terminal is returned, the caller's.
func TestNativeStartCompletedIsTheCallers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term, err := native{seam: &startSeam{after: cancel}}.StartTerminal(ctx, ext.TerminalSpec{Argv: []string{"/bin/cat"}, Env: testEnv()})
	if ctx.Err() == nil {
		t.Fatal("the context did not end after the start")
	}
	callersTerminal(t, term, err)
}

// A panic between pty.Start's return and StartTerminal's (the seam's
// hook) leaves the same panic, and by the time it is recovered the process
// (whose pid its pid file holds) is killed with its group and reaped.
func TestNativeStartThatPanicsAfterTheForkLeavesNothing(t *testing.T) {
	type boom struct{ n int }
	dir := t.TempDir()
	var leader, child int
	seam := &startSeam{after: func() {
		leader = pidIn(t, filepath.Join(dir, "leader.pid"))
		child = pidIn(t, filepath.Join(dir, "child.pid"))
		panic(boom{3})
	}}
	got := func() (v any) {
		defer func() { v = recover() }()
		native{seam: seam}.StartTerminal(context.Background(), ext.TerminalSpec{
			Argv: []string{"/bin/sh", "-c", "sleep 300 & echo $! > child.pid; echo $$ > leader.pid; wait"},
			Dir:  dir, Env: testEnv(),
		})
		return nil
	}()
	if got != (boom{3}) {
		t.Fatalf("the panic that left StartTerminal: %#v", got)
	}
	if !reaped(leader) {
		t.Fatalf("the leader %d is still there", leader)
	}
	if !goneSoon(t, child) {
		syscall.Kill(child, syscall.SIGKILL)
		t.Fatalf("its group's %d outlived the panic", child)
	}
}

// A panic once the leader has exited, while a descendant that ignores
// SIGTERM and SIGHUP still runs in its group, leaves nothing either: the
// process is held unreaped until StartTerminal returns, so the group's id
// is still its own when the cleanup kills the group, and the descendant
// is gone by the time the panic is recovered.
func TestNativeStartThatPanicsOnceTheLeaderExitedLeavesNothing(t *testing.T) {
	type boom struct{ n int }
	dir := t.TempDir()
	var leader, desc int
	seam := &startSeam{after: func() {
		leader = pidIn(t, filepath.Join(dir, "leader.pid"))
		desc = pidIn(t, filepath.Join(dir, "desc.pid"))
		exitedSoon(t, leader)
		panic(boom{4})
	}}
	got := func() (v any) {
		defer func() { v = recover() }()
		native{seam: seam}.StartTerminal(context.Background(), ext.TerminalSpec{
			// The leader exits once the descendant has set its traps.
			Argv: []string{"/bin/sh", "-c", `sh -c 'trap "" TERM HUP; echo $$ > desc.pid; exec sleep 300' & while [ ! -s desc.pid ]; do sleep 0.01; done; echo $$ > leader.pid; exit 0`},
			Dir:  dir, Env: testEnv(),
		})
		return nil
	}()
	if desc > 0 {
		t.Cleanup(func() { syscall.Kill(desc, syscall.SIGKILL) })
	}
	if got != (boom{4}) {
		t.Fatalf("the panic that left StartTerminal: %#v", got)
	}
	if !reaped(leader) {
		t.Fatalf("the leader %d is still there", leader)
	}
	exitedSoon(t, desc) // killed: gone, or its new parent's zombie
}

// Stop asks the group: a leader and a child in its group, both ending on
// SIGTERM, end within the grace, Stop answers nil, and each is gone.
func TestNativeStopAsksTheGroup(t *testing.T) {
	dir := t.TempDir()
	term, err := Native().StartTerminal(context.Background(), ext.TerminalSpec{
		Argv: []string{"/bin/sh", "-c", "sleep 300 & echo $! > child.pid; echo $$ > leader.pid; wait"},
		Dir:  dir, Env: testEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped(t, term)
	leader, child := pidIn(t, filepath.Join(dir, "leader.pid")), pidIn(t, filepath.Join(dir, "child.pid"))
	grace := hang
	began := time.Now()
	if err := term.Stop(context.Background(), grace); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took >= grace {
		t.Fatalf("Stop took %s, its whole grace: the ask did not end the leader", took)
	}
	if st := term.Exit(); st.Signal != "terminated" {
		t.Fatalf("the leader's exit: %+v", st)
	}
	if !reaped(leader) || !goneSoon(t, child) {
		t.Fatalf("the leader %d or the child %d is still there", leader, child)
	}
}

// A leader that ignores the ask is forced once the grace has passed: Stop
// answers nil within the grace plus StopKillWait (the test's own timeout
// is above that sum).
func TestNativeStopForcesALeaderThatIgnoresTheAsk(t *testing.T) {
	dir := t.TempDir()
	term, err := Native().StartTerminal(context.Background(), ext.TerminalSpec{
		Argv: []string{"/bin/sh", "-c", "trap '' TERM; echo $$ > leader.pid; while :; do sleep 1; done"},
		Dir:  dir, Env: testEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped(t, term)
	leader := pidIn(t, filepath.Join(dir, "leader.pid")) // written once the trap is set
	grace := 200 * time.Millisecond
	began := time.Now()
	if err := term.Stop(context.Background(), grace); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > grace+ext.StopKillWait {
		t.Fatalf("Stop took %s, past the grace and StopKillWait", took)
	}
	if st := term.Exit(); st.Signal != "killed" {
		t.Fatalf("the leader's exit: %+v", st)
	}
	if !reaped(leader) {
		t.Fatalf("the leader %d is still there", leader)
	}
}

// A process that has exited is not signalled: Stop answers nil, and the
// exit is the process's own. That Stop sends no signal once Done is closed
// is observed where the signal is sent, through internal/pty's kill seam
// (TestStopAfterExitSendsNoSignal): the native Terminal's Stop is pty's.
func TestNativeStopAfterExitSignalsNothing(t *testing.T) {
	term, err := Native().StartTerminal(context.Background(), ext.TerminalSpec{Argv: []string{"/bin/sh", "-c", "exit 4"}, Env: testEnv()})
	if err != nil {
		t.Fatal(err)
	}
	done(t, term)
	if err := term.Stop(context.Background(), hang); err != nil {
		t.Fatal(err)
	}
	if st := term.Exit(); st.Code != 4 || st.Signal != "" {
		t.Fatalf("exit %+v", st)
	}
}

// Stop promises the leader alone, as on main: a descendant that ignores
// SIGTERM and SIGHUP outlives a leader that exits on the ask, and Stop
// answers nil. The test ends it itself.
func TestNativeStopPromisesTheLeaderOnly(t *testing.T) {
	dir := t.TempDir()
	term, err := Native().StartTerminal(context.Background(), ext.TerminalSpec{
		Argv: []string{"/bin/sh", "-c", `sh -c 'trap "" TERM HUP; echo $$ > desc.pid; exec sleep 300' & echo $$ > leader.pid; wait`},
		Dir:  dir, Env: testEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped(t, term)
	leader, desc := pidIn(t, filepath.Join(dir, "leader.pid")), pidIn(t, filepath.Join(dir, "desc.pid"))
	t.Cleanup(func() { syscall.Kill(desc, syscall.SIGKILL) })
	if err := term.Stop(context.Background(), hang); err != nil {
		t.Fatal(err)
	}
	if !reaped(leader) {
		t.Fatalf("the leader %d is still there", leader)
	}
	if err := syscall.Kill(desc, 0); err != nil {
		t.Fatalf("the descendant %d is gone (%v): Stop promised more than the leader", desc, err)
	}
}

// The contract's StopKillWait is pty's wait after SIGKILL.
func TestStopKillWaitIsPtys(t *testing.T) {
	if ext.StopKillWait != pty.KillWait {
		t.Fatalf("ext.StopKillWait %s, pty.KillWait %s", ext.StopKillWait, pty.KillWait)
	}
	if ext.MaxCommandTimeout != proc.MaxTimeout {
		t.Fatalf("ext.MaxCommandTimeout %s, proc.MaxTimeout %s", ext.MaxCommandTimeout, proc.MaxTimeout)
	}
}

// firstWrite notes, at the log's first write, whether Started had been
// called by then.
type firstWrite struct {
	started *atomic.Bool
	seen    atomic.Int32 // 1: Started before the output; 2: not
	mu      sync.Mutex
	buf     bytes.Buffer
}

func (f *firstWrite) Write(p []byte) (int, error) {
	if f.started.Load() {
		f.seen.CompareAndSwap(0, 1)
	} else {
		f.seen.CompareAndSwap(0, 2)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}

// The native Run is proc.Run: the same exit, signal, output and words for
// the same command, the environment exactly the one given, head and tail,
// a log within its limit, Started once and before the output, a timeout
// told from a cancel.
func TestNativeRunIsProcRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "CONDUCTOR_RUN=build-00000001", "ONLY=this"}
	for _, script := range []string{"printf abc; echo err >&2; exit 3", "kill -TERM $$", "exit 0"} {
		spec := ext.CommandSpec{Argv: []string{"sh", "-c", script}, Dir: dir, Env: env, Timeout: time.Minute}
		got := Native().Run(ctx, spec)
		want := proc.Run(ctx, proc.Spec{Argv: spec.Argv, Dir: dir, Environ: env, Timeout: time.Minute})
		if got.Exit != want.Exit || got.Signal != want.Signal || !bytes.Equal(got.Head, want.Head) || !bytes.Equal(got.Tail, want.Tail) ||
			got.Total != want.Total || got.Truncated != want.Truncated || got.Err != want.Err || got.Killed != want.Killed {
			t.Errorf("%q: native %+v, proc %+v", script, got, want)
		}
		if got.Process.PID <= 0 || got.Process.Group != got.Process.PID || got.Started.IsZero() || got.Duration <= 0 {
			t.Errorf("%q: the process %+v", script, got)
		}
	}
	// A refusal is proc's, in its words.
	bad := Native().Run(ctx, ext.CommandSpec{Argv: []string{"true"}, Dir: "relative", Timeout: time.Minute})
	if want := proc.Run(ctx, proc.Spec{Argv: []string{"true"}, Dir: "relative", Timeout: time.Minute}); bad.Err != want.Err || bad.Exit != -1 {
		t.Errorf("a refusal: %+v, want %q", bad, want.Err)
	}
	// The environment, exactly.
	res := Native().Run(ctx, ext.CommandSpec{Argv: []string{"sh", "-c", "env"}, Dir: dir, Env: env, Timeout: time.Minute})
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(res.Head)), "\n") {
		if k, _, _ := strings.Cut(l, "="); k != "PWD" && k != "SHLVL" && k != "_" { // the shell's own
			lines = append(lines, l)
		}
	}
	slices.Sort(lines)
	if wantEnv := slices.Sorted(slices.Values(env)); !slices.Equal(lines, wantEnv) {
		t.Errorf("the environment: %q, want %q", lines, wantEnv)
	}
	// Head, tail and a log within its limit; Started once, before the output.
	var started atomic.Bool
	var calls atomic.Int32
	var told ext.ProcessInfo
	lg := &firstWrite{started: &started}
	res = Native().Run(ctx, ext.CommandSpec{Argv: []string{"sh", "-c", "i=0; while [ $i -lt 5000 ]; do echo line-$i; i=$((i+1)); done"},
		Dir: dir, Env: env, Timeout: time.Minute, Head: 10, Tail: 10, Log: lg, LogLimit: 1000,
		Started: func(p ext.ProcessInfo) { told = p; calls.Add(1); started.Store(true) }})
	if res.Exit != 0 || len(res.Head) != 10 || len(res.Tail) != 10 || !res.Truncated || res.Total <= 20 {
		t.Errorf("head and tail: %+v", res)
	}
	lg.mu.Lock()
	logged := lg.buf.Len()
	lg.mu.Unlock()
	if logged == 0 || logged > 1000 {
		t.Errorf("the log took %d bytes, limit 1000", logged)
	}
	if calls.Load() != 1 || told != res.Process || lg.seen.Load() != 1 {
		t.Errorf("Started: %d calls, told %+v, the result's %+v, before the output: %v", calls.Load(), told, res.Process, lg.seen.Load() == 1)
	}
	// A timeout, told from a cancel.
	res = Native().Run(ctx, ext.CommandSpec{Argv: []string{"sleep", "30"}, Dir: dir, Env: env, Timeout: 200 * time.Millisecond})
	if !res.TimedOut || res.Canceled {
		t.Errorf("a timeout: %+v", res)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	res = Native().Run(cctx, ext.CommandSpec{Argv: []string{"sleep", "30"}, Dir: dir, Env: env, Timeout: time.Minute,
		Started: func(ext.ProcessInfo) { go cancel() }})
	if !res.Canceled || res.TimedOut {
		t.Errorf("a cancel: %+v", res)
	}
}

// The native Serve is proc.Serve: the service runs until its Stop, which
// ends it, and its result is its process's.
func TestNativeServeStartsAndStops(t *testing.T) {
	dir := t.TempDir()
	svc, err := Native().Serve(context.Background(), ext.CommandSpec{
		Argv: []string{"sh", "-c", "echo $$ > svc.pid; exec sleep 300"}, Dir: dir,
		Env: []string{"PATH=" + os.Getenv("PATH")},
	})
	if err != nil {
		t.Fatal(err)
	}
	pid := pidIn(t, filepath.Join(dir, "svc.pid"))
	if r := svc.Result(); r.Process.PID != pid || r.Exit != -1 {
		t.Fatalf("the running service's result: %+v", r)
	}
	select {
	case <-svc.Done():
		t.Fatal("the service ended before its stop")
	default:
	}
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-svc.Done()
	if r := svc.Result(); r.Process.PID != pid || r.Signal != "terminated" || !reaped(pid) {
		t.Fatalf("the stopped service: %+v", r)
	}
	if _, err := Native().Serve(context.Background(), ext.CommandSpec{Argv: []string{"true"}, Dir: "relative"}); err == nil || err.Error() != "proc: the directory must be absolute" {
		t.Fatalf("a refusal: %v", err)
	}
}

// The native Probe and Port answer as proc's: one try, no redirect
// followed; a port free to bind.
func TestNativeProbeAndPortAreProcs(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	defer up.Close()
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer moved.Close()
	for _, url := range []string{up.URL, moved.URL} {
		got, gerr := Native().Probe(t.Context(), ext.ProbeSpec{URL: url, Interval: hang})
		want, werr := proc.Probe(t.Context(), url, hang)
		if got != want || (gerr == nil) != (werr == nil) {
			t.Errorf("%s: native %d %v, proc %d %v", url, got, gerr, want, werr)
		}
	}
	if elsewhere.Load() != 0 {
		t.Error("a redirect was followed")
	}
	var errs []string
	for range 3 {
		n, err := Native().Port(t.Context(), ext.ProcessRef{Run: "r", Task: "t", Attempt: 1, Tree: "worktree"})
		if err != nil || n < 1 || n > 65535 {
			t.Fatalf("Port: %d %v", n, err)
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(n)))
		if err == nil {
			l.Close()
			return
		}
		errs = append(errs, err.Error()) // another process took the number between the two
	}
	t.Fatalf("no port drawn could be bound: %v", errs)
}

// The native Runtime is named local, reports native and takes every run.
func TestNativeTakesEveryRun(t *testing.T) {
	r := Native()
	if r.Name() != Name || r.Where() != ext.WhereNative {
		t.Fatalf("Name %q, Where %q", r.Name(), r.Where())
	}
	for _, ref := range []ext.ProcessRef{{}, {Run: "build-00000001"}, {Session: "s1", Run: "r", Task: "t", Attempt: 3, Tree: "checkout"}} {
		if !r.Takes(ref) {
			t.Errorf("declined %+v", ref)
		}
	}
}
