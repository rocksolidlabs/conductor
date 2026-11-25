//go:build unix

package session

import (
	"errors"
	"syscall"
)

// groupAlive says a process is in the group: a signal 0 to it finds one
// (or one it may not signal, EPERM).
func groupAlive(group int) (bool, error) {
	err := syscall.Kill(-group, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	}
	return false, err
}

// killGroup sends SIGKILL to the group.
func killGroup(group int) { _ = syscall.Kill(-group, syscall.SIGKILL) }
