//go:build !unix

package agents

import "os/exec"

func probeAttr(*exec.Cmd) {}

func endGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
