//go:build unix

package sessiontest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// A FakeRuntime with PanicAfterStart starts the spec, then ends and reaps
// what it started before it panics: the panic is its own, and by the time
// it is recovered the process is gone, reaped, its pid no process's.
func TestFakeRuntimePanicsAfterStartLeavingNothing(t *testing.T) {
	dir := t.TempDir()
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f := &FakeRuntime{Base: local.Native(), PanicAfterStart: true}
	ref := ext.ProcessRef{Session: "s-1", Run: "r-1", Task: "build", Attempt: 1, Tree: "worktree"}
	func() {
		defer func() {
			if v := recover(); v != PanicAfterStartValue {
				t.Fatalf("recovered %v", v)
			}
		}()
		f.StartTerminal(t.Context(), ext.TerminalSpec{Argv: []string{"sleep", "60"}, Dir: dir, DirFile: d, Env: []string{"PATH=" + os.Getenv("PATH")}, Ref: ref})
		t.Fatal("StartTerminal returned")
	}()
	c := f.Asked("StartTerminal")
	if len(c) != 1 || c[0].Ref != ref || c[0].Process.PID <= 0 || c[0].Process.Group != c[0].Process.PID {
		t.Fatalf("the start recorded: %+v", c)
	}
	if !reaped(c[0].Process.PID) {
		t.Fatalf("the process %d is still there after the panic", c[0].Process.PID)
	}
}

// A PanicAfterStart panic once the leader has exited, while a descendant
// that ignores SIGTERM and SIGHUP still runs in its group, leaves nothing
// either: the start is held unreaped until the abort, so the group's id is
// still the leader's own when the abort kills the group, and the
// descendant is gone once the panic is recovered.
func TestFakeRuntimePanicsAfterStartOnceTheLeaderExitedLeavingNothing(t *testing.T) {
	dir := t.TempDir()
	var leader, desc int
	f := &FakeRuntime{Base: local.Native(), PanicAfterStart: true}
	f.afterStart = func() {
		leader = pidIn(t, filepath.Join(dir, "leader.pid"))
		desc = pidIn(t, filepath.Join(dir, "desc.pid"))
		waitFor(t, fmt.Sprintf("the leader %d to exit", leader), func() bool { return exited(leader) })
	}
	got := func() (v any) {
		defer func() { v = recover() }()
		f.StartTerminal(t.Context(), ext.TerminalSpec{
			// The leader exits once the descendant has set its traps.
			Argv: []string{"/bin/sh", "-c", `sh -c 'trap "" TERM HUP; echo $$ > desc.pid; exec sleep 300' & while [ ! -s desc.pid ]; do sleep 0.01; done; echo $$ > leader.pid; exit 0`},
			Dir:  dir, Env: []string{"PATH=" + os.Getenv("PATH")},
		})
		return nil
	}()
	if desc > 0 {
		t.Cleanup(func() { syscall.Kill(desc, syscall.SIGKILL) })
	}
	if got != PanicAfterStartValue {
		t.Fatalf("the panic that left StartTerminal: %#v", got)
	}
	if c := f.Asked("StartTerminal"); len(c) != 1 || c[0].Process.PID != leader {
		t.Fatalf("the start recorded %+v, the leader %d", c, leader)
	}
	if !reaped(leader) {
		t.Fatalf("the leader %d is still there", leader)
	}
	// Killed with the group: gone, or its new parent's zombie.
	waitFor(t, fmt.Sprintf("the descendant %d to end", desc), func() bool { return exited(desc) })
}

// waitFor polls cond until it holds, failing the test after a minute: a
// bound no step of these tests comes near, which only bounds a hang.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("waited a minute for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pidIn waits for the file at path to hold a pid, written by the test's
// command, and answers it.
func pidIn(t *testing.T, path string) int {
	t.Helper()
	var pid int
	waitFor(t, "a pid in "+path, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	return pid
}

// reaped says the process pid is gone and reaped: a zombie still answers.
func reaped(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

// exited says the process pid has exited: gone, or a zombie its parent has
// not reaped (as a held process's leader is until it is aborted).
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
