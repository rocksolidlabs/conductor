//go:build unix

package gitcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// aloneHome gives the test a HOME of its own with no git configuration,
// and the git it runs no system configuration (aloneEnv); it skips
// without git.
func aloneHome(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
}

var aloneEnv = map[string]string{"GIT_CONFIG_NOSYSTEM": "1"}

// openDir opens dir as a caller of Alone does.
func openDir(t *testing.T, dir string) *os.File {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// Alone names the repository and its work tree outright: a core.worktree
// the repository's configuration sets elsewhere sends git nowhere else.
func TestAloneNamesTheRepositoryAndItsWorkTree(t *testing.T) {
	aloneHome(t)
	dir := t.TempDir()
	elsewhere := t.TempDir()
	if _, err := Run(t.Context(), dir, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), dir, "config", "core.worktree", elsewhere); err != nil {
		t.Fatal(err)
	}
	found, err := Run(t.Context(), dir, "rev-parse", "--show-toplevel")
	if err != nil || !sameDir(strings.TrimSpace(found), elsewhere) {
		t.Fatalf("the repository's own setting should point a plain git elsewhere: %q %v", found, err)
	}
	a := Alone{Dir: openDir(t, dir), Path: dir, GitDir: ".git", WorkTree: ".", Env: aloneEnv}
	top, err := a.Run(t.Context(), "rev-parse", "--show-toplevel")
	if err != nil || !sameDir(strings.TrimSpace(top), dir) {
		t.Fatalf("top %q %v, want %s", top, err, dir)
	}
}

func sameDir(a, b string) bool {
	x, err := os.Stat(a)
	y, err2 := os.Stat(b)
	return err == nil && err2 == nil && os.SameFile(x, y)
}

// Alone's git starts in the folder its caller opened: the folder moved
// and another put at its path, git still runs in the one opened (Linux);
// where the system cannot start git in an open folder, the path that no
// longer names it is refused.
func TestAloneRunsInTheOpenedFolder(t *testing.T) {
	aloneHome(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "app")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := openDir(t, dir)
	if err := os.Rename(dir, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "theirs.txt"), []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDir := t.TempDir()
	a := Alone{Dir: f, Path: dir, GitDir: gitDir, WorkTree: ".", Env: aloneEnv}
	if _, err := a.Run(t.Context(), "init", "-q"); err != nil && runtime.GOOS == "linux" {
		t.Fatal(err)
	}
	var listed []string
	_, err := a.Each(t.Context(), func(rec []byte) bool { listed = append(listed, string(rec)); return true }, "ls-files", "-z", "--others")
	switch {
	case runtime.GOOS == "linux":
		if err != nil || strings.Join(listed, ",") != "mine.txt" {
			t.Fatalf("listed %v %v, want the opened folder's mine.txt", listed, err)
		}
	case !errors.Is(err, ErrFolderMoved):
		t.Fatalf("on %s: %v %v, want ErrFolderMoved", runtime.GOOS, listed, err)
	}
}

// At ctx's end the whole process group git leads is ended, a program it
// started that holds git's output among it, and the call returns: nothing
// it started is left.
func TestAloneEndsTheWholeGroupAtItsBound(t *testing.T) {
	aloneHome(t)
	dir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	// An alias git runs through the shell: a program of git's that keeps
	// its standard output, Alone's pipe, open far past the bound.
	a := Alone{Dir: openDir(t, dir), Path: dir, Env: aloneEnv,
		Config: []string{"alias.hold=!echo $$ > " + pidFile + "; exec sleep 600"}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, "hold")
		done <- err
	}()
	pid := waitPid(t, pidFile)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("ended: %v", err)
	}
	waitGone(t, pid)
}

// waitPid is the pid a program wrote to path, once it has.
func waitPid(t *testing.T, path string) int {
	t.Helper()
	for {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 0 {
				t.Fatalf("pid %q", b)
			}
			return pid
		}
		if t.Context().Err() != nil {
			t.Fatal("no pid written")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitGone waits until pid is no process (the test's timeout bounds it).
func waitGone(t *testing.T, pid int) {
	t.Helper()
	for syscall.Kill(pid, 0) == nil {
		if t.Context().Err() != nil {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A program git started that leaves git's group (a session of its own)
// and keeps git's output open is out of the group's reach: the call still
// returns, once its wait for the output is spent (aloneWaitDelay), rather
// than wait on that program.
func TestAloneReturnsWhenAProgramThatLeftTheGroupKeepsTheOutput(t *testing.T) {
	aloneHome(t)
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid here")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	a := Alone{Dir: openDir(t, dir), Path: dir, Env: aloneEnv,
		// git's shell returns once the program has left the group and
		// written its pid, so the group's end never reaches it first.
		Config: []string{"alias.leave=!setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 600' & while [ ! -s " + pidFile + " ]; do sleep 0.01; done"}}
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(t.Context(), "leave")
		done <- err
	}()
	pid := waitPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// The wait the code applies, and the git's own start and end on a
	// loaded machine above it.
	select {
	case <-done:
	case <-time.After(aloneWaitDelay + time.Minute):
		t.Fatalf("the call is still waiting on a program outside git's group")
	}
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the program that left the group was ended: the test shows nothing")
	}
}
