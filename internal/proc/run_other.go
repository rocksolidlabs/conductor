//go:build !unix

package proc

import (
	"errors"
	"os"
	"os/exec"
)

type lifecycle struct{}

// signal kills the process, whatever s asks: no process group, and no
// signal to ask it to end, here.
func (*lifecycle) signal(cmd *exec.Cmd, _ sig) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}

func (*lifecycle) reap(cmd *exec.Cmd) error { return cmd.Wait() }

// members and still cannot say: no process group here.
func (*lifecycle) members(*exec.Cmd) (pids []int, known bool) { return nil, false }

func (*lifecycle) still(*exec.Cmd, []int) []int { return nil }

func newLifecycle(<-chan struct{}, *os.File) *lifecycle { return &lifecycle{} }

// judge is never asked here: no exit is watched, so nothing settles.
func (*lifecycle) judge() bool { return false }

func attr(cmd *exec.Cmd, g *lifecycle) { cmd.Cancel = func() error { return g.signal(cmd, sigKill) } }

// noServiceRunner refuses Serve here: a service is stopped through its
// process group, which this platform does not have.
var noServiceRunner = func() error { return errNoServiceRunner }

func group(cmd *exec.Cmd) int { return cmd.Process.Pid }

func exitStatus(state *os.ProcessState) (int, string) { return state.ExitCode(), "" }

var watchFails = false

func watchExit(*exec.Cmd, *lifecycle) (<-chan error, func() error, error) {
	return nil, nil, errors.New("proc: no exit watch on this platform")
}
