//go:build unix

package agents

import (
	"os/exec"
	"syscall"
)

// probeAttr starts the probe in a process group of its own and kills the
// whole group when its context ends, so a CLI that spawns helpers (a node
// wrapper, a daemon it starts) does not outlive the probe.
func probeAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// usageAttr starts a usage read's program in a session of its own, as a
// session's agent is started (internal/pty), but with no terminal at all:
// a tool that would ask a question on /dev/tty fails to open it at once
// instead of being stopped, a background group, on the server's terminal
// until the read's timeout. The session's leader leads its process group,
// whose id is the leader's pid, so endGroup ends everything it started.
func usageAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// endGroup kills the process group cmd leads, before its Wait reaps the
// leader (so the group's id cannot have been given to another): a usage
// read ends every helper its program started, whatever it answered.
func endGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
