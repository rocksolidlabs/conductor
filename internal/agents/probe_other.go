//go:build !unix

package agents

import "os/exec"

func probeAttr(*exec.Cmd) {}

// usageAttr has no session to give here: a usage read's program keeps the
// server's console, and the read's timeout is its bound.
func usageAttr(*exec.Cmd) {}

// endGroup has no group to end here: the program alone is killed, and what
// it started is bounded only by the read's timeout.
func endGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
