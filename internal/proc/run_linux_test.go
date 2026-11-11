package proc

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A watch that fails once set up (waitid refused by a seccomp filter, say)
// saw no exit: the command is ended and the result says so, never an exit
// invented.
func TestRunReportsAWatchThatFailed(t *testing.T) {
	old := waitid
	waitid = func(int, int, *unix.Siginfo, int, *unix.Rusage) error { return unix.ENOSYS }
	t.Cleanup(func() { waitid = old })
	res := sh(t, time.Minute, "sleep 30")
	if !strings.Contains(res.Err, "could not be watched") || !strings.Contains(res.Err, "function not implemented") || res.Signal != "killed" || res.TimedOut {
		t.Fatalf("%+v", res)
	}
	if !gone(t, res.Process.PID, 3*time.Second) {
		t.Fatalf("the process %d outlived the run", res.Process.PID)
	}
}

// WatchExit, for a command another package starts and ends: the exit is
// told while the process is still unreaped (a zombie, its pid and group
// id its own), and only reap reaps it, giving its status.
func TestWatchExitBeforeReaping(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 4")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited, reap, err := WatchExit(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-exited; err != nil {
		t.Fatalf("the watch: %v", err)
	}
	if state, _, ok := stat(cmd.Process.Pid); !ok || state != 'Z' {
		t.Fatalf("told the exit of %d before it was a zombie: state %q", cmd.Process.Pid, state)
	}
	var exit *exec.ExitError
	if err := reap(); !errors.As(err, &exit) || exit.ExitCode() != 4 {
		t.Fatalf("reap: %v", err)
	}
	if _, _, ok := stat(cmd.Process.Pid); ok && !gone(t, cmd.Process.Pid, 3*time.Second) {
		t.Fatal("still there after reap")
	}
	watchFails = true
	t.Cleanup(func() { watchFails = false })
	other := exec.Command("true")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WatchExit(other); err == nil {
		t.Fatal("a watch made to fail was set up")
	}
	_ = other.Wait()
}
