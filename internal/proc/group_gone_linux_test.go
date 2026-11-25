//go:build linux

package proc

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// prSetChildSubreaper is prctl's PR_SET_CHILD_SUBREAPER.
const prSetChildSubreaper = 36

// A group whose members have all exited and wait to be reaped is gone:
// where nothing reaps orphans (a server that is process 1 with no init,
// played here by this test made the subreaper of what it starts and
// reaping nothing until the end), a signal 0 still finds the zombies, and
// GroupGone answers at once all the same. Not parallel: the subreaper is
// the whole test process's.
func TestGroupGoneTakesZombiesForGone(t *testing.T) {
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); e != 0 {
		t.Skipf("no subreaper here: %v", e)
	}
	defer syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 0, 0)
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & sleep 60 & echo started; read _")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	var sleeps []int
	deadline := time.Now().Add(30 * time.Second)
	for len(sleeps) != 2 {
		members, err := groupMembers(pgid, pgid)
		if err != nil {
			t.Fatal(err)
		}
		sleeps = members
		if time.Now().After(deadline) {
			t.Fatalf("the group's members: %v", members)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer func() {
		for _, pid := range sleeps {
			var ws syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &ws, 0, nil)
		}
		_ = cmd.Wait()
	}()
	// The leader exits (a zombie: not reaped yet); its sleeps are this
	// test's now, and end as zombies too.
	in.Close()
	for !exited(pgid) {
		if time.Now().After(deadline) {
			t.Fatal("the leader did not exit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, pid := range sleeps {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	for _, pid := range sleeps {
		for !exited(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("member %d did not exit", pid)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("a signal to the group of zombies: %v, want it found", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := GroupGone(ctx, pgid); err != nil {
		t.Fatalf("a group of zombies: %v, want gone", err)
	}
	// A group none is in at all.
	if err := GroupGone(ctx, 1<<30); err != nil && !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("no group: %v", err)
	}
}
