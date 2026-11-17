//go:build unix

package agents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// pidIn waits, within a bound that only bounds a hang, for the pid a
// program wrote to path.
func pidIn(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 0 {
				t.Fatalf("pid file %q", b)
			}
			return pid
		}
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

// heldHelper is a FIFO nothing writes and the line of shell that waits on
// it, writing to late should it ever be let go.
func heldHelper(t *testing.T) (fifo, late, wait string) {
	t.Helper()
	dir := t.TempDir()
	fifo, late = filepath.Join(dir, "release"), filepath.Join(dir, "late")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	return fifo, late, "read line < " + quote(fifo) + "; echo late > " + quote(late)
}

// A program that answers and exits, leaving a helper in its process group
// with its output elsewhere (nothing holds the probe's pipe): the helper is
// ended when the run returns, before the program is reaped. The run has an
// hour: its timeout is not what ends anything here.
func TestProbeEndsTheHelperItsProgramLeftInItsGroup(t *testing.T) {
	fifo, late, wait := heldHelper(t)
	helper := filepath.Join(t.TempDir(), "helper")
	p := script(t, "sh -c "+quote(wait)+" </dev/null >/dev/null 2>&1 &\n"+
		"echo $! > "+quote(helper+".new")+" && mv "+quote(helper+".new")+" "+quote(helper)+"\n"+
		`echo "2.1.287 (Claude Code)"`)
	res := (&ProbeProcess{Timeout: time.Hour}).Run(context.Background(), []string{p}, claudeProbe, nil)
	pid := pidIn(t, helper)
	waitGone(t, pid)
	if !res.Ran || !res.Identified {
		t.Fatalf("%+v", res)
	}
	if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); !errors.Is(err, syscall.ENXIO) {
		if err == nil {
			w.Close()
		}
		t.Fatalf("something still reads the helper's FIFO: open for writing said %v", err)
	}
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the helper went on: %v", err)
	}
}

// End ends a run on the caller's goroutine, whatever the run's context and
// timeout (neither ends here); a run ended before it starts starts nothing.
func TestProbeEndEndsTheRunNow(t *testing.T) {
	_, late, wait := heldHelper(t)
	started := filepath.Join(t.TempDir(), "started")
	p := script(t, "echo $$ > "+quote(started+".new")+" && mv "+quote(started+".new")+" "+quote(started)+"\n"+wait)
	r := &ProbeProcess{Timeout: time.Hour}
	ran := make(chan ProbeResult, 1)
	go func() { ran <- r.Run(context.Background(), []string{p}, claudeProbe, nil) }()
	pid := pidIn(t, started)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	r.End()
	select {
	case res := <-ran:
		if res.Ran || res.Error != errProbeEnded {
			t.Fatalf("%+v", res)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("End did not end the run")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the ended program (pid %d) is still there: kill 0 said %v", pid, err)
	}
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the ended program went on: %v", err)
	}

	before := &ProbeProcess{}
	before.End()
	marker := filepath.Join(t.TempDir(), "ran")
	if res := before.Run(context.Background(), []string{script(t, "touch "+quote(marker))}, claudeProbe, nil); res.Ran || res.Error != errProbeEnded {
		t.Fatalf("a run ended before its start: %+v", res)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a run ended before its start started its program")
	}
}

// A watch of the program's exit that fails under way (its error on the
// watch's channel, as a waitid without the call on this kernel) is no
// answer: the program, alive and silent, is ended and reaped, and the run
// says it did not run, with the watch's error, to be asked again. The seam
// is the probe's own (watchProbe): the real watch, its channel replaced by
// one that holds the failure.
func TestProbeAWatchThatFailsUnderWayIsNoAnswer(t *testing.T) {
	failure := errors.New("waitid: function not implemented")
	real := watchProbe
	pid := make(chan int, 1)
	watchProbe = func(cmd *exec.Cmd) (<-chan error, func() error, error) {
		_, reap, err := real(cmd)
		if err != nil {
			return nil, nil, err
		}
		pid <- cmd.Process.Pid
		failed := make(chan error, 1)
		failed <- failure
		return failed, reap, nil
	}
	t.Cleanup(func() { watchProbe = real })
	_, late, wait := heldHelper(t)
	res := (&ProbeProcess{Timeout: time.Hour}).Run(context.Background(), []string{script(t, wait)}, claudeProbe, nil)
	if res.Ran || !res.Retry || !strings.Contains(res.Error, failure.Error()) {
		t.Fatalf("a failed watch was taken for an answer: %+v", res)
	}
	if err := syscall.Kill(<-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the program is still there, or not reaped: kill 0 said %v", err)
	}
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the program went on: %v", err)
	}
}

// The limit, stated: a helper that leaves the probe's process group (a
// session of its own) is not ended by the run, nor by End. The test ends it.
func TestProbeLeavesAHelperInASessionOfItsOwn(t *testing.T) {
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("no setsid on this machine")
	}
	_, _, wait := heldHelper(t)
	helper := filepath.Join(t.TempDir(), "helper")
	inner := "echo $$ > " + quote(helper+".new") + " && mv " + quote(helper+".new") + " " + quote(helper) + "; " + wait
	// The program waits for the helper to be in its own session before it
	// answers: a helper killed before its setsid would prove nothing.
	p := script(t, quote(setsid)+" sh -c "+quote(inner)+" </dev/null >/dev/null 2>&1 &\n"+
		"until [ -s "+quote(helper)+" ]; do :; done\n"+
		`echo "2.1.287 (Claude Code)"`)
	r := &ProbeProcess{Timeout: time.Hour}
	res := r.Run(context.Background(), []string{p}, claudeProbe, nil)
	r.End()
	pid := pidIn(t, helper)
	alive := syscall.Kill(pid, 0) == nil
	_ = syscall.Kill(pid, syscall.SIGKILL)
	waitGone(t, pid)
	if !res.Identified {
		t.Fatalf("%+v", res)
	}
	if !alive {
		t.Fatalf("the helper in a session of its own (pid %d) was ended: the limit has moved, say so where it is stated", pid)
	}
}
