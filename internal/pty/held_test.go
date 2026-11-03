package pty

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// heldHang bounds a wait that should end at once, so a broken hold fails
// the test rather than hanging it: no cap of the code's.
const heldHang = 30 * time.Second

// heldExited says the process pid has exited: gone, or a zombie not yet
// reaped (a held process's leader until Release or Abort).
func heldExited(pid int) bool {
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

// heldWait waits for cond, failing the test once heldHang has passed.
func heldWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(heldHang)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("no %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// heldPid waits for the file at path to hold a pid and answers it.
func heldPid(t *testing.T, path string) int {
	t.Helper()
	var pid int
	heldWait(t, "pid in "+path, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	return pid
}

func heldEnv() []string { return BuildEnv(os.Environ(), nil, nil, nil) }

// A held process is not reaped however soon it exits: Done stays open,
// the leader a zombie, until Release, after which it is Start's (its exit
// its own). A second Release does nothing, and Abort once released is
// Stop's, nothing to signal.
func TestStartHeldIsReapedOnlyOnceReleased(t *testing.T) {
	p, err := StartHeld(Spec{Argv: []string{"/bin/sh", "-c", "exit 3"}, Env: heldEnv()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	heldWait(t, "exit of the held leader", func() bool { return heldExited(p.PID()) })
	select {
	case <-p.Done():
		t.Fatal("a held process was reaped before Release")
	default:
	}
	if syscall.Kill(p.PID(), 0) != nil {
		t.Fatal("the held leader's pid is not its own: it was reaped")
	}
	p.Release()
	select {
	case <-p.Done():
	case <-time.After(heldHang):
		t.Fatal("the released process was not reaped")
	}
	if st := p.Exit(); st.Code != 3 || st.Signal != "" {
		t.Fatalf("exit %+v", st)
	}
	p.Release()
	if err := p.Abort(); err != nil {
		t.Fatalf("Abort once released: %v", err)
	}
}

// Abort ends a held process's whole group even once its leader has
// exited: a descendant that ignores SIGTERM and SIGHUP, still in the group
// whose id the unreaped leader keeps, is killed, and the leader reaped
// with its own exit.
func TestAbortEndsTheGroupOnceTheLeaderHasExited(t *testing.T) {
	dir := t.TempDir()
	p, err := StartHeld(Spec{
		Argv: []string{"/bin/sh", "-c", `sh -c 'trap "" TERM HUP; echo $$ > desc.pid; exec sleep 300' & while [ ! -s desc.pid ]; do sleep 0.01; done; exit 0`},
		Dir:  dir, Env: heldEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	desc := heldPid(t, filepath.Join(dir, "desc.pid"))
	t.Cleanup(func() { syscall.Kill(desc, syscall.SIGKILL) })
	heldWait(t, "exit of the held leader", func() bool { return heldExited(p.PID()) })
	if err := syscall.Kill(desc, 0); err != nil {
		t.Fatalf("the descendant %d is gone before Abort: %v", desc, err)
	}
	if err := p.Abort(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("Abort returned before the reap")
	}
	if st := p.Exit(); st.Code != 0 {
		t.Fatalf("the leader's exit: %+v", st)
	}
	heldWait(t, "end of the descendant", func() bool { return heldExited(desc) }) // gone, or its new parent's zombie
}
