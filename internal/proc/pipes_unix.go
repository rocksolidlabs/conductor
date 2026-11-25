//go:build unix

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// noPipesRunner is nil here: StartPipes starts a child. A test makes it
// the refusal of a platform without a process group (pipes_other.go).
var noPipesRunner = func() error { return nil }

// pipesAttr starts a pipes child in a process group of its own. It sets no
// Cancel: the child's life is not bound to a context.
func pipesAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// pipesLifecycle is a pipes child's lifecycle: its signals ordered against
// its reaping as a command's are, with no output of its own judged (the
// caller reads the child's output), so a kill notes nothing.
func pipesLifecycle() *lifecycle { return &lifecycle{judged: true} }

// pipesExit is how a reaped child ended: its code, or 128 plus the number
// of the signal that ended it and that signal's name; err a wait's error
// beyond the exit status.
func pipesExit(state *os.ProcessState, err error) PipesExit {
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return PipesExit{Code: -1, Err: err}
	}
	if state == nil {
		return PipesExit{Code: -1}
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return PipesExit{Code: 128 + int(ws.Signal()), Signal: ws.Signal().String()}
	}
	return PipesExit{Code: state.ExitCode()}
}
