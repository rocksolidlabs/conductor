package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// sh runs a shell script through Run with the given timeout: the tests'
// commands, never the product's, which takes argv arrays alone.
func sh(t *testing.T, timeout time.Duration, script string, opts ...func(*Spec)) Result {
	t.Helper()
	s := Spec{Argv: []string{"sh", "-c", script}, Dir: t.TempDir(), Timeout: timeout}
	for _, o := range opts {
		o(&s)
	}
	return Run(context.Background(), s)
}

// gone waits until the process pid is no more, up to the deadline.
func gone(t *testing.T, pid int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func pidIn(t *testing.T, out []byte) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("no pid in %q: %v", out, err)
	}
	return pid
}

func TestRunReportsExits(t *testing.T) {
	res := sh(t, time.Minute, "echo hi; echo there >&2; exit 0")
	if res.Exit != 0 || res.Err != "" || res.Signal != "" || string(res.Head) != "hi\nthere\n" || res.Total != 9 || res.Truncated || res.TimedOut || res.Canceled || res.Killed {
		t.Fatalf("%+v", res)
	}
	if res.Process.PID == 0 || res.Process.Group != res.Process.PID || res.Started.IsZero() || res.Duration <= 0 {
		t.Fatalf("process %+v started %v took %v", res.Process, res.Started, res.Duration)
	}
	if res := sh(t, time.Minute, "exit 3"); res.Exit != 3 || res.Err != "" || res.Signal != "" {
		t.Fatalf("exit 3: %+v", res)
	}
	if res := sh(t, time.Minute, "kill -TERM $$"); res.Exit != -1 || res.Signal != "terminated" || res.Err != "" {
		t.Fatalf("a signal: %+v", res)
	}
	if res := sh(t, time.Minute, "exit 0", func(s *Spec) { s.Argv = []string{"/nonexistent/program"} }); res.Exit != -1 || res.Err == "" || !res.Started.IsZero() {
		t.Fatalf("a program that is not there: %+v", res)
	}
}

func TestRunRefusesABadSpec(t *testing.T) {
	for name, s := range map[string]Spec{
		"no command":    {Dir: "/", Timeout: time.Second},
		"empty command": {Argv: []string{""}, Dir: "/", Timeout: time.Second},
		"no timeout":    {Argv: []string{"true"}, Dir: "/"},
		"long timeout":  {Argv: []string{"true"}, Dir: "/", Timeout: MaxTimeout + 1},
		"relative dir":  {Argv: []string{"true"}, Dir: ".", Timeout: time.Second},
		"conductor var": {Argv: []string{"true"}, Dir: "/", Timeout: time.Second, Env: map[string]string{"CONDUCTOR_Y": "1"}},
		"a loader":      {Argv: []string{"true"}, Dir: "/", Timeout: time.Second, Env: map[string]string{"LD_PRELOAD": "x"}},
		"not a name":    {Argv: []string{"true"}, Dir: "/", Timeout: time.Second, Env: map[string]string{"A=B": "x"}},
	} {
		if res := Run(context.Background(), s); res.Err == "" || res.Exit != -1 || !res.Started.IsZero() {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

func TestRunKillsTheGroupAtTheTimeout(t *testing.T) {
	res := sh(t, 300*time.Millisecond, "sleep 30 & echo $!; wait")
	if !res.TimedOut || res.Canceled || res.Err != "" {
		t.Fatalf("%+v", res)
	}
	if pid := pidIn(t, res.Head); !gone(t, pid, 3*time.Second) {
		t.Fatalf("the sleep %d outlived the run", pid)
	}
}

func TestRunCancelIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancelled once the process exists, so the kill is what ends it.
	res := Run(ctx, Spec{Argv: []string{"sleep", "30"}, Dir: t.TempDir(), Timeout: time.Minute, Started: func(Process) { go cancel() }})
	if !res.Canceled || res.TimedOut || res.Exit != -1 || res.Signal != "killed" || res.Duration > 5*time.Second {
		t.Fatalf("%+v", res)
	}
}

// What the leader leaves behind goes with the group when the leader exits,
// after exit 0 and after exit 3 alike: a child holding the output pipe
// (Killed says so), and one that redirected its output (killed all the
// same, unseen).
func TestRunEndsWhatTheLeaderLeaves(t *testing.T) {
	for _, exit := range []int{0, 3} {
		res := sh(t, time.Minute, "sleep 30 & echo $!; exit "+strconv.Itoa(exit))
		if res.Exit != exit || !res.Killed || res.TimedOut || res.Err != "" || res.Duration > 5*time.Second {
			t.Fatalf("exit %d, a child with the pipe: %+v", exit, res)
		}
		if pid := pidIn(t, res.Head); !gone(t, pid, 3*time.Second) {
			t.Fatalf("exit %d: the sleep %d outlived the run", exit, pid)
		}
		res = sh(t, time.Minute, "sleep 30 >/dev/null 2>&1 & echo $!; exit "+strconv.Itoa(exit))
		if res.Exit != exit || res.Killed || res.TimedOut || res.Err != "" || res.Duration > 5*time.Second {
			t.Fatalf("exit %d, a child without the pipe: %+v", exit, res)
		}
		if pid := pidIn(t, res.Head); !gone(t, pid, 3*time.Second) {
			t.Fatalf("exit %d: the redirected sleep %d outlived the run", exit, pid)
		}
	}
}

// The helper leaves its process group (setsid), says its pid, and sleeps
// holding the output it inherited: a child a command could leave behind
// that the group's kill cannot reach.
func TestHelperLeavesTheGroup(t *testing.T) {
	if os.Getenv("PROC_HELPER") != "setsid" {
		t.Skip("a helper")
	}
	if _, err := syscall.Setsid(); err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	fmt.Println(os.Getpid())
	os.WriteFile("ready", nil, 0o600) // the leader waits for this before it exits
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// A child that left the group and kept the output is given WaitDelay, then
// left alone, and the result says so; the run does not wait on it.
func TestRunLeavesAloneAChildThatLeftTheGroup(t *testing.T) {
	start := time.Now()
	res := sh(t, time.Minute, `"$0" -test.run='^TestHelperLeavesTheGroup$' & while [ ! -f ready ]; do sleep 0.05; done; exit 0`, func(s *Spec) {
		s.Argv = append(s.Argv, os.Args[0])
		s.Env = map[string]string{"PROC_HELPER": "setsid"}
	})
	if res.Exit != 0 || res.Killed || !strings.Contains(res.Err, "left the group") || time.Since(start) > WaitDelay+5*time.Second {
		t.Fatalf("after %v: %+v", time.Since(start), res)
	}
	pid := pidIn(t, res.Head)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the child that left the group was killed: %v", err)
	}
	syscall.Kill(pid, syscall.SIGKILL)
}

func TestRunKeepsHeadAndTail(t *testing.T) {
	var log bytes.Buffer
	res := sh(t, time.Minute, "echo BEGIN; head -c 1048576 /dev/zero | tr '\\000' x; echo; echo END", func(s *Spec) {
		s.Head, s.Tail, s.Log, s.LogLimit = 100, 50, &log, 4096
	})
	if res.Exit != 0 || res.Err != "" || res.LogErr != "" {
		t.Fatalf("%+v", res)
	}
	const total = 6 + 1048576 + 1 + 4
	if res.Total != total || !res.Truncated || len(res.Head) != 100 || len(res.Tail) != 50 {
		t.Fatalf("total %d truncated %v head %d tail %d", res.Total, res.Truncated, len(res.Head), len(res.Tail))
	}
	if !bytes.HasPrefix(res.Head, []byte("BEGIN\nxxxx")) || !bytes.HasSuffix(res.Tail, []byte("xxxx\nEND\n")) {
		t.Fatalf("head %q tail %q", res.Head, res.Tail)
	}
	// The log whole within LogLimit: its head, the dropped line and the
	// output's last bytes (a quarter of the limit), the dropped count the
	// difference between the output and what was kept of it.
	text := log.String()
	head, tail := logBounds(4096)
	if tail != 1024 || head != 4096-1024-logLine {
		t.Fatalf("the bounds of 4096: head %d tail %d", head, tail)
	}
	line := "\n… " + strconv.Itoa(total-int(head)-int(tail)) + " bytes dropped …\n"
	if len(text) > 4096 || len(text) != int(head)+len(line)+int(tail) {
		t.Fatalf("a log of %d bytes for a limit of 4096 (head %d, line %d, tail %d)", len(text), head, len(line), tail)
	}
	if !strings.HasPrefix(text, "BEGIN\n") || text[head:int(head)+len(line)] != line || !strings.HasSuffix(text, "xxxx\nEND\n") || text[len(text)-int(tail):] != strings.Repeat("x", int(tail)-len("\nEND\n"))+"\nEND\n" {
		t.Fatalf("log of %d bytes: %.40q … %q", len(text), text, text[len(text)-40:])
	}
	// At the ledger's bound: 2 MiB of output keep within 1 MiB, the tail
	// LogTail.
	log.Reset()
	res = sh(t, time.Minute, "head -c 2097152 /dev/zero | tr '\\000' y; echo; echo END", func(s *Spec) { s.Log, s.LogLimit = &log, 1<<20 })
	if res.Exit != 0 || res.LogErr != "" {
		t.Fatalf("%+v", res)
	}
	if head, tail := logBounds(1 << 20); log.Len() > 1<<20 || tail != LogTail || !strings.HasSuffix(log.String(), "yyyy\nEND\n") || !strings.Contains(log.String(), " bytes dropped …\n") || log.Len() < int(head) {
		t.Fatalf("a log of %d bytes for a limit of 1 MiB", log.Len())
	}
	// A limit too small for a head: the line is cut to what the tail leaves.
	log.Reset()
	res = sh(t, time.Minute, "head -c 1000 /dev/zero | tr '\\000' z", func(s *Spec) { s.Log, s.LogLimit = &log, 40 })
	if res.Exit != 0 || log.Len() > 40 || !strings.HasSuffix(log.String(), strings.Repeat("z", 10)) {
		t.Fatalf("a log of %d bytes for a limit of 40: %q", log.Len(), log.String())
	}

	// Less than the bounds: everything kept, nothing truncated, no
	// dropped line, the tail the bytes after the head.
	log.Reset()
	res = sh(t, time.Minute, "echo 0123456789", func(s *Spec) { s.Head, s.Tail, s.Log = 4, 100, &log })
	if res.Truncated || string(res.Head) != "0123" || string(res.Tail) != "456789\n" || res.Total != 11 || log.String() != "0123456789\n" {
		t.Fatalf("%+v log %q", res, log.String())
	}
}

func TestRunStripsTheEnvironment(t *testing.T) {
	t.Setenv("CONDUCTOR_X", "secret")
	t.Setenv("LD_PRELOAD", "loader")
	t.Setenv("OUTSIDE_THE_LIST", "1")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("ALLOWED_BY_THE_CALLER", "yes")
	res := sh(t, time.Minute, "env", func(s *Spec) {
		s.Env = map[string]string{"MY_VAR": "v"}
		s.Allow = []string{"ALLOWED_BY_THE_CALLER"}
	})
	if res.Exit != 0 {
		t.Fatalf("%+v", res)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(res.Head), "\n") {
		have[line] = true
	}
	for _, want := range []string{"TERM=dumb", "NO_COLOR=1", "MY_VAR=v", "ALLOWED_BY_THE_CALLER=yes", "PATH=" + os.Getenv("PATH")} {
		if !have[want] {
			t.Errorf("missing %s in %q", want, res.Head)
		}
	}
	for _, line := range strings.Split(string(res.Head), "\n") {
		k, _, _ := strings.Cut(line, "=")
		switch {
		case strings.HasPrefix(k, "CONDUCTOR_"), k == "LD_PRELOAD", k == "OUTSIDE_THE_LIST", k == "COLORTERM":
			t.Errorf("leaked %s", line)
		}
	}
}

// Inject gives a command Conductor's own per-tree names, over whatever the
// server's environment holds of them, and nothing else: a token's name, a
// port name not as a manifest's would be upper-cased, or any other name
// refuses the run before it starts. Env still refuses every CONDUCTOR_
// name, an injectable one included.
func TestRunInjectsConductorsOwnNamesAlone(t *testing.T) {
	t.Setenv("CONDUCTOR_RUN", "the-server's-own")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "secret")
	inject := map[string]string{"CONDUCTOR_RUN": "build-00000001", "CONDUCTOR_TASK": "build", "CONDUCTOR_ATTEMPT": "2", "CONDUCTOR_STATE_DIR": "/w/.conductor/state",
		"CONDUCTOR_PREVIEW_URL": "http://127.0.0.1:41234/", "CONDUCTOR_PREVIEW_BRIDGE": "http://127.0.0.1:8080/preview-bridge.js", "CONDUCTOR_TREE_SECRET": "ab12", "CONDUCTOR_PORT_WEB": "41234", "CONDUCTOR_PORT_DB_2": "5433", "CONDUCTOR_PORT_" + strings.Repeat("A", 16): "1"}
	res := sh(t, time.Minute, "env", func(s *Spec) { s.Inject = inject })
	if res.Exit != 0 || res.Err != "" {
		t.Fatalf("%+v", res)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(res.Head), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.HasPrefix(k, "CONDUCTOR_") {
			got[k] = v
		}
	}
	if len(got) != len(inject) {
		t.Fatalf("the CONDUCTOR_ names: %v", got)
	}
	for k, v := range inject {
		if got[k] != v {
			t.Errorf("%s=%q, want %q", k, got[k], v)
		}
	}
	for _, name := range []string{"CONDUCTOR_NOTIFY_TOKEN", "CONDUCTOR_SESSION_ID", "CONDUCTOR_BIN", "CONDUCTOR_PORT_web", "CONDUCTOR_PORT_", "CONDUCTOR_PORT_1X",
		"CONDUCTOR_PORT_" + strings.Repeat("A", 17), "CONDUCTOR_PORT_WEB=1", "CONDUCTOR_STATE_DIRX", "PATH", ""} {
		res := Run(context.Background(), Spec{Argv: []string{"true"}, Dir: t.TempDir(), Timeout: time.Minute, Inject: map[string]string{name: "x"}})
		if res.Err == "" || res.Exit != -1 || !res.Started.IsZero() || !strings.Contains(res.Err, "is not one of Conductor's own names") {
			t.Errorf("Inject %q: %+v", name, res)
		}
	}
	for _, name := range []string{"CONDUCTOR_PORT_WEB", "CONDUCTOR_STATE_DIR"} {
		res := Run(context.Background(), Spec{Argv: []string{"true"}, Dir: t.TempDir(), Timeout: time.Minute, Env: map[string]string{name: "x"}})
		if res.Err == "" || res.Exit != -1 || !res.Started.IsZero() || !strings.Contains(res.Err, "a CONDUCTOR_ variable is never set for a command") {
			t.Errorf("Env %q: %+v", name, res)
		}
	}
}

func TestRunStartedReportsTheProcess(t *testing.T) {
	var got Process
	res := sh(t, time.Minute, "echo $$", func(s *Spec) { s.Started = func(p Process) { got = p } })
	if res.Exit != 0 || got.PID == 0 || got.PID != pidIn(t, res.Head) || got != res.Process || got.StartedAt.IsZero() {
		t.Fatalf("started %+v result %+v", got, res)
	}
}

// A caller's deadline, and a context already ended, read as Canceled, not
// as the run's own timeout.
func TestRunTellsTheCallersDeadlineFromItsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := Run(ctx, Spec{Argv: []string{"sleep", "30"}, Dir: t.TempDir(), Timeout: time.Minute})
	if !res.Canceled || res.TimedOut || res.Exit != -1 {
		t.Fatalf("the caller's deadline: %+v", res)
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	res = Run(done, Spec{Argv: []string{"sleep", "30"}, Dir: t.TempDir(), Timeout: time.Minute})
	if !res.Canceled || res.TimedOut || res.Err == "" || !res.Started.IsZero() {
		t.Fatalf("a context already ended: %+v", res)
	}
}

// blockedWriter blocks every Write until released, and counts them.
type blockedWriter struct {
	release chan struct{}
	writes  atomic.Int32
}

func (b *blockedWriter) Write(p []byte) (int, error) {
	b.writes.Add(1)
	<-b.release
	return len(p), nil
}

// gatedWriter keeps what it is given once its gate is open: its first
// Write waits for it, as a writer whose goroutine has not run yet.
type gatedWriter struct {
	gate chan struct{}
	buf  bytes.Buffer
}

func (g *gatedWriter) Write(p []byte) (int, error) {
	<-g.gate
	return g.buf.Write(p)
}

// A writer that falls behind, however many chunks the output comes in,
// loses nothing within its limit once it runs: the head waits for it in
// memory, at most the limit's bytes, and the tail is kept as it comes. The
// log is the head, the dropped line and the output's last bytes, whole.
func TestALogWriterThatFallsBehindLosesNothingWithinItsLimit(t *testing.T) {
	const limit = 64 << 10
	w := &gatedWriter{gate: make(chan struct{})}
	l := newLogSink(w, limit)
	var out bytes.Buffer
	for i := range 4096 {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 64)
		out.Write(chunk)
		if n, err := l.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("write %d: %d %v", i, n, err)
		}
	}
	close(w.gate)
	if why := l.finish(time.Minute); why != "" {
		t.Fatalf("the log is short: %s", why)
	}
	head, tail := logBounds(limit)
	all := out.Bytes()
	line := "\n… " + strconv.Itoa(len(all)-int(head)-int(tail)) + " bytes dropped …\n"
	want := string(all[:head]) + line + string(all[len(all)-int(tail):])
	if got := w.buf.String(); got != want || len(got) > limit {
		t.Fatalf("a log of %d bytes, want %d of a limit of %d", len(got), len(want), limit)
	}
}

// A Log writer that blocks holds up neither the process nor Run: the run
// ends with the log cut and says so.
func TestRunLeavesAStalledLogWriterBehind(t *testing.T) {
	bw := &blockedWriter{release: make(chan struct{})}
	start := time.Now()
	res := sh(t, time.Minute, "echo one; echo two; echo three", func(s *Spec) { s.Log = bw })
	if res.Exit != 0 || res.Err != "" || string(res.Head) != "one\ntwo\nthree\n" {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.LogErr, "did not return") || time.Since(start) > WaitDelay+5*time.Second {
		t.Fatalf("log err %q after %v", res.LogErr, time.Since(start))
	}
	// Released after Run returned, the writer gets the Write in flight back
	// and nothing more: the chunks queued and the tail are dropped.
	close(bw.release)
	time.Sleep(200 * time.Millisecond)
	if n := bw.writes.Load(); n != 1 {
		t.Fatalf("%d writes after the sink was left behind", n)
	}
	// A process that writes far more than the queue holds while the
	// writer blocks: the process still ends, and the log is cut.
	bw2 := &blockedWriter{release: make(chan struct{})}
	defer close(bw2.release)
	res = sh(t, time.Minute, "head -c 4194304 /dev/zero | tr '\\000' x", func(s *Spec) { s.Log = bw2 })
	if res.Exit != 0 || res.Total != 4194304 || res.LogErr == "" {
		t.Fatalf("%+v", res)
	}
}

// firstWriteSees records what it saw on its first write.
type firstWriteSees struct {
	started *int32
	seen    int32
	once    sync.Once
}

func (f *firstWriteSees) Write(p []byte) (int, error) {
	f.once.Do(func() { f.seen = atomic.LoadInt32(f.started) })
	return len(p), nil
}

// Started has returned before any output is read, however slow it is.
func TestRunCallsStartedBeforeReadingOutput(t *testing.T) {
	var started int32
	w := &firstWriteSees{started: &started}
	res := sh(t, time.Minute, "echo early", func(s *Spec) {
		s.Log = w
		s.Started = func(Process) {
			time.Sleep(200 * time.Millisecond)
			atomic.StoreInt32(&started, 1)
		}
	})
	if res.Exit != 0 || string(res.Head) != "early\n" || w.seen != 1 {
		t.Fatalf("seen %d: %+v", w.seen, res)
	}
}

// The helper joins its parent's process group, says so, and sleeps: a
// command that left the group Run made for it, which the group's kill
// cannot reach.
func TestHelperChangesGroup(t *testing.T) {
	if os.Getenv("PROC_HELPER") != "setpgid" {
		t.Skip("a helper")
	}
	pgid, err := syscall.Getpgid(os.Getppid())
	if err == nil {
		err = syscall.Setpgid(0, pgid)
	}
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	fmt.Println("left")
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// A command that joined another group is still ended at the timeout: the
// process itself is killed with the group it left.
func TestRunEndsALeaderThatChangedGroup(t *testing.T) {
	start := time.Now()
	res := Run(context.Background(), Spec{Argv: []string{os.Args[0], "-test.run=^TestHelperChangesGroup$"}, Dir: t.TempDir(), Timeout: time.Second, Env: map[string]string{"PROC_HELPER": "setpgid"}})
	if !res.TimedOut || res.Signal != "killed" || !strings.HasPrefix(string(res.Head), "left") || time.Since(start) > 10*time.Second {
		t.Fatalf("after %v: %+v", time.Since(start), res)
	}
	if !gone(t, res.Process.PID, 3*time.Second) {
		t.Fatalf("the leader %d outlived the run", res.Process.PID)
	}
}

// A watch that cannot be set up ends the command at once rather than
// guess at its exit, and the result says so.
func TestRunEndsTheCommandWhenTheExitCannotBeWatched(t *testing.T) {
	watchFails = true
	t.Cleanup(func() { watchFails = false })
	res := sh(t, time.Minute, "sleep 30")
	if !strings.Contains(res.Err, "could not be watched") || res.Signal != "killed" || res.TimedOut || res.Canceled || res.Started.IsZero() {
		t.Fatalf("%+v", res)
	}
	if !gone(t, res.Process.PID, 3*time.Second) {
		t.Fatalf("the process %d outlived the run", res.Process.PID)
	}
}

// Once the reaping has begun, a cancel sends nothing: the process's number
// may be another's.
func TestASignalAfterTheReapIsNotSent(t *testing.T) {
	g := &lifecycle{}
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := g.reap(cmd); err != nil {
		t.Fatal(err)
	}
	if err := g.signal(cmd, sigKill); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("a signal after the reap: %v", err)
	}
	if err := (&lifecycle{}).signal(exec.Command("true"), sigTerm); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("a signal before the start: %v", err)
	}
}
