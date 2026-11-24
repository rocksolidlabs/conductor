//go:build linux || darwin

package proc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pipesWait bounds a pipes test's wait for a child: a hang made a
// failure, never what a step takes.
const pipesWait = 2 * time.Minute

// envNames is env's names, for a failure's words: an environment holds
// tokens.
func envNames(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		out = append(out, k)
	}
	return out
}

// pipesHelperEnv is what the test binary needs to run as a pipes helper:
// its mode, and its race detector told not to wait a second at its exit.
func pipesHelperEnv(mode string) []string {
	return []string{"PROC_HELPER=" + mode, "GORACE=atexit_sleep_ms=0"}
}

// pipesHelper is the argv that runs this test binary as the pipes helper,
// args after it.
func pipesHelper(args ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestHelperPipes$", "--"}, args...)
}

// startPipes starts s, stopped when the test ends whatever the test did.
func startPipes(t *testing.T, s PipeSpec) *Pipes {
	t.Helper()
	p, err := StartPipes(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background(), 0) })
	return p
}

// lineReader reads r a line at a time, onto the channel it returns, closed at
// r's end.
func lineReader(r io.Reader) <-chan string {
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		for sc.Scan() {
			ch <- sc.Text()
		}
	}()
	return ch
}

// nextLine is ch's next line, within pipesWait.
func nextLine(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case l, ok := <-ch:
		if !ok {
			t.Fatalf("%s: the output ended", what)
		}
		return l
	case <-time.After(pipesWait):
		t.Fatalf("%s: no line within %s", what, pipesWait)
	}
	return ""
}

// awaitDone waits for p's Done within pipesWait.
func awaitDone(t *testing.T, p *Pipes, what string) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(pipesWait):
		t.Fatalf("%s: not done within %s", what, pipesWait)
	}
}

// TestHelperPipes is the pipes tests' child, by PROC_HELPER: "report"
// prints its working directory, its arguments (those after "--") and its
// environment, sorted, each a JSON line, and exits 0; "echo" prints
// "ready", then for each line of its input writes "err:" and the line to
// its standard error and then "got:" and the line to its standard output,
// and exits 3 at the input's end; "stderr N" writes N bytes to its
// standard error, then "done" to its standard output, and exits 0.
func TestHelperPipes(t *testing.T) {
	mode := os.Getenv("PROC_HELPER")
	var args []string
	if i := slices.Index(os.Args, "--"); i >= 0 {
		args = os.Args[i+1:]
	}
	switch mode {
	case "report":
		dir, err := os.Getwd()
		if err != nil {
			dir = "error: " + err.Error()
		}
		env := slices.Sorted(slices.Values(os.Environ()))
		for _, v := range []any{dir, args, env} {
			b, _ := json.Marshal(v)
			fmt.Println(string(b))
		}
		os.Exit(0)
	case "echo":
		fmt.Println("ready")
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Fprintln(os.Stderr, "err:"+sc.Text())
			fmt.Println("got:" + sc.Text())
		}
		os.Exit(3)
	case "stderr":
		n, err := strconv.Atoi(args[0])
		if err != nil {
			os.Exit(2)
		}
		w := bufio.NewWriterSize(os.Stderr, 1<<16)
		for range n / 4096 {
			w.Write(make([]byte, 4096))
		}
		w.Write(make([]byte, n%4096))
		if w.Flush() != nil {
			os.Exit(2)
		}
		fmt.Println("done")
		os.Exit(0)
	}
	t.Skip("a helper")
}

// The child sees exactly the argv, the directory and the environment it
// was given, none of the server's own: with an environment, and with none
// (nil), which is an empty one, never the parent's.
func TestStartPipesArgvDirEnv(t *testing.T) {
	t.Setenv("PROC_PARENTS_OWN", "leaked")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	args := []string{"a b", "$HOME", ";", "*", ""}
	report := func(t *testing.T, env []string) (string, []string, []string) {
		t.Helper()
		p := startPipes(t, PipeSpec{Argv: pipesHelper(args...), Dir: dir, DirFile: f, Environ: env})
		if p.Process().PID <= 0 {
			t.Fatalf("the child's process: %+v", p.Process())
		}
		out := lineReader(p.Stdout())
		var gotDir string
		var gotArgs, gotEnv []string
		for i, v := range []any{&gotDir, &gotArgs, &gotEnv} {
			if l := nextLine(t, out, "the report"); json.Unmarshal([]byte(l), v) != nil {
				t.Fatalf("report line %d: %q", i, l)
			}
		}
		awaitDone(t, p, "the report")
		if st := p.Exit(); st.Code != 0 || st.Signal != "" || st.Err != nil {
			t.Fatalf("the report's exit: %+v", st)
		}
		return gotDir, gotArgs, gotEnv
	}
	t.Run("an environment", func(t *testing.T) {
		env := append(pipesHelperEnv("report"), "WORDS=a b;c $HOME", "EMPTY=")
		gotDir, gotArgs, gotEnv := report(t, env)
		if gotDir != dir {
			t.Errorf("started in %q, want %q", gotDir, dir)
		}
		if !slices.Equal(gotArgs, args) {
			t.Errorf("the arguments %q, want %q", gotArgs, args)
		}
		if want := slices.Sorted(slices.Values(env)); !slices.Equal(gotEnv, want) {
			// Names only: an environment holds tokens.
			t.Errorf("the environment holds %q, want exactly %q", envNames(gotEnv), envNames(want))
		}
	})
	t.Run("none", func(t *testing.T) {
		// The helper needs its mode in its environment: env(1) prints this
		// one, which is none.
		p := startPipes(t, PipeSpec{Argv: []string{"/usr/bin/env"}, Dir: dir, DirFile: f})
		out, err := io.ReadAll(p.Stdout())
		if err != nil {
			t.Fatal(err)
		}
		awaitDone(t, p, "env")
		if len(out) != 0 {
			t.Errorf("a child given no environment has %q", envNames(strings.Split(strings.TrimSpace(string(out)), "\n")))
		}
	})
}

// A line written to the child's input comes back on its output as the
// child writes it, its standard error apart: the output is read while the
// child still runs, before its input ends. Its standard error is counted,
// its input's end ends it, and a Stop once it has exited signals nothing.
func TestStartPipesReadsAndWrites(t *testing.T) {
	p := startPipes(t, PipeSpec{Argv: pipesHelper(), Dir: t.TempDir(), Environ: pipesHelperEnv("echo")})
	out := lineReader(p.Stdout())
	if l := nextLine(t, out, "the helper's start"); l != "ready" {
		t.Fatalf("first line %q, want ready", l)
	}
	for _, word := range []string{"hello", "again"} {
		if _, err := io.WriteString(p.Stdin(), word+"\n"); err != nil {
			t.Fatal(err)
		}
		if l := nextLine(t, out, "the echo of "+word); l != "got:"+word {
			t.Fatalf("read %q, want got:%s (the output alone, as it is written)", l, word)
		}
	}
	select {
	case <-p.Done():
		t.Fatal("the child ended before its input did")
	default:
	}
	if err := p.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, p, "the helper at its input's end")
	if st := p.Exit(); st.Code != 3 || st.Signal != "" || st.Err != nil {
		t.Fatalf("the exit: %+v, want 3", st)
	}
	if l, ok := <-out; ok {
		t.Fatalf("more output after the exit: %q", l)
	}
	want := int64(len("err:hello\nerr:again\n"))
	deadline := time.Now().Add(pipesWait)
	for p.StderrBytes() != want {
		if time.Now().After(deadline) {
			t.Fatalf("%d bytes of standard error counted, want %d", p.StderrBytes(), want)
		}
		time.Sleep(time.Millisecond)
	}
	if err := p.Stop(context.Background(), time.Hour); err != nil {
		t.Fatalf("a Stop once the child exited: %v", err)
	}
}

// Stop ends the child's whole group: a grandchild that ignores SIGTERM is
// gone once Stop has returned nil, whether the child ignores the ask too
// (forced past its grace) or ends at it (what it left in its group killed
// at its exit). Stop answers nil once the child is reaped, its Done closed
// by then.
func TestStartPipesStopEndsTheGroup(t *testing.T) {
	for _, c := range []struct {
		name, script string
		grace        time.Duration
		code         int
	}{
		{"the child ignores the ask", `trap "" TERM; sleep 300 & echo $!; wait`, 0, 128 + 9},
		{"the child ends on the ask", `trap "exit 0" TERM; (trap "" TERM; exec sleep 300) & echo $!; wait`, pipesWait, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := startPipes(t, PipeSpec{Argv: []string{"sh", "-c", c.script}, Dir: t.TempDir(), Environ: []string{"PATH=" + os.Getenv("PATH")}})
			leader := p.Process().PID
			grandchild, err := strconv.Atoi(nextLine(t, lineReader(p.Stdout()), "the grandchild's pid"))
			if err != nil || grandchild <= 0 || leader <= 0 {
				t.Fatalf("the pids: leader %d, grandchild %d (%v)", leader, grandchild, err)
			}
			if err := p.Stop(context.Background(), c.grace); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			select {
			case <-p.Done():
			default:
				t.Fatal("Stop answered nil before the child was reaped")
			}
			if st := p.Exit(); st.Code != c.code {
				t.Fatalf("the exit: %+v, want %d", st, c.code)
			}
			for _, pid := range []int{leader, grandchild} {
				if !gone(t, pid, pipesWait) {
					t.Fatalf("%d outlived the stop", pid)
				}
			}
		})
	}
}

// lookCancel is a context that cancels itself once Err has been asked a
// number of times: the look that cancels it answers as before the cancel.
type lookCancel struct {
	context.Context
	cancel context.CancelFunc
	left   atomic.Int32
}

func (l *lookCancel) Err() error {
	err := l.Context.Err()
	if l.left.Add(-1) == 0 {
		l.cancel()
	}
	return err
}

// An ended context starts nothing: one ended before the call, and one that
// ends at the start's first look at it, before its commitment point, are
// answered with the context's error, and no child is started.
func TestStartPipesContextBeforeStart(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	first := &lookCancel{Context: ctx, cancel: cancel2}
	first.left.Store(1)
	for name, ctx := range map[string]context.Context{"ended before the call": ended, "ending at its first look": first} {
		t.Run(name, func(t *testing.T) {
			var forks atomic.Int32
			p, err := StartPipes(ctx, PipeSpec{Argv: []string{"sleep", "300"}, Dir: t.TempDir(), Environ: []string{}, beforeFork: func() { forks.Add(1) }})
			if p != nil {
				p.Stop(context.Background(), 0)
			}
			if p != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("StartPipes answered %v, %v: want the context's error", p, err)
			}
			if n := forks.Load(); n != 0 {
				t.Fatalf("%d child(ren) started under an ended context", n)
			}
		})
	}
}

// A child that writes much more to its standard error than a pipe holds
// is never held up: its standard error is read as it comes, and it exits.
func TestStartPipesStderrNeverBlocks(t *testing.T) {
	const n = 64 << 20
	p := startPipes(t, PipeSpec{Argv: pipesHelper(strconv.Itoa(n)), Dir: t.TempDir(), Environ: pipesHelperEnv("stderr")})
	if l := nextLine(t, lineReader(p.Stdout()), "the helper's end"); l != "done" {
		t.Fatalf("read %q, want done", l)
	}
	awaitDone(t, p, "the helper that wrote 64 MiB to its standard error")
	if st := p.Exit(); st.Code != 0 {
		t.Fatalf("the exit: %+v", st)
	}
	deadline := time.Now().Add(pipesWait)
	for p.StderrBytes() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d bytes of standard error counted, want %d", p.StderrBytes(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// What StartPipes refuses it refuses before anything starts: no command,
// a directory that is not absolute, an environment entry that is not
// NAME=value, and every start where no pipes runner is; a program that
// is not there is refused by its start, with nothing running.
func TestStartPipesRefusesABadSpec(t *testing.T) {
	dir := t.TempDir()
	for name, s := range map[string]PipeSpec{
		"no command":         {Dir: dir},
		"an empty command":   {Argv: []string{""}, Dir: dir},
		"a relative dir":     {Argv: []string{"sleep", "300"}, Dir: "here"},
		"an entry":           {Argv: []string{"sleep", "300"}, Dir: dir, Environ: []string{"NOEQUALS"}},
		"an empty name":      {Argv: []string{"sleep", "300"}, Dir: dir, Environ: []string{"=v"}},
		"a program not here": {Argv: []string{filepath.Join(dir, "absent")}, Dir: dir},
	} {
		var forks atomic.Int32
		s.beforeFork = func() { forks.Add(1) }
		p, err := StartPipes(context.Background(), s)
		if p != nil {
			p.Stop(context.Background(), 0)
		}
		if p != nil || err == nil {
			t.Errorf("%s: StartPipes answered %v, %v", name, p, err)
		}
		if n := forks.Load(); n != 0 && name != "a program not here" {
			t.Errorf("%s: refused after the commitment point", name)
		}
	}
	old := noPipesRunner
	noPipesRunner = func() error { return errNoPipesRunner }
	t.Cleanup(func() { noPipesRunner = old })
	p, err := StartPipes(context.Background(), PipeSpec{Argv: []string{"sleep", "300"}, Dir: dir})
	if p != nil || err == nil || err.Error() != "proc: no pipes runner on this platform" {
		t.Fatalf("where no pipes runner is: %v, %v", p, err)
	}
}
