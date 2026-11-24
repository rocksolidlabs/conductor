//go:build !unix

package proc

import (
	"os"
	"os/exec"
)

// noPipesRunner refuses StartPipes here: a pipes child is stopped through
// its process group, and its exit watched without reaping, neither of
// which this platform has (as Serve is refused).
var noPipesRunner = func() error { return errNoPipesRunner }

func pipesAttr(*exec.Cmd) {}

func pipesLifecycle() *lifecycle { return &lifecycle{} }

func pipesExit(state *os.ProcessState, err error) PipesExit {
	if state == nil {
		return PipesExit{Code: -1, Err: err}
	}
	return PipesExit{Code: state.ExitCode()}
}
