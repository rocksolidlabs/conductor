//go:build unix

package gitcli

import (
	"context"
	"io"
	"os/exec"
	"syscall"

	"github.com/rocksolidlabs/conductor/internal/proc"
	"github.com/rocksolidlabs/conductor/internal/pty"
)

// execAlone runs git with argv in cwd, its environment env (beside the
// allowlisted one, pty.BuildEnv), no input, in a session of its own: no
// controlling terminal, so nothing it starts can ask on one, and a process
// group whose id is git's pid. The group is sent SIGKILL once git has
// exited or ctx has ended, whichever comes first, while git is not yet
// reaped (proc.WatchExit), so that the group's id is still git's own and
// no other process is reached; the reap's wait for the pipes is bounded
// (aloneWaitDelay). ctx's error once it ended.
func execAlone(ctx context.Context, cwd string, argv []string, env map[string]string, stdout, stderr io.Writer) error {
	cmd := exec.Command("git", argv...)
	cmd.Dir = cwd
	cmd.Env = pty.BuildEnv(pty.ParentEnv(), nil, env, nil)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = aloneWaitDelay
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	endGroup := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	exited, reap, err := proc.WatchExit(cmd)
	if err != nil {
		// An exit that cannot be watched is not guessed at: the group is
		// ended now, while its id is git's own.
		endGroup()
		_ = cmd.Wait()
		return err
	}
	select {
	case <-exited:
	case <-ctx.Done():
	}
	endGroup()
	err = reap()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
