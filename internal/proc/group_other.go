//go:build unix && !linux && !darwin

package proc

import (
	"errors"
	"os"
)

// groupMembers cannot list a process group's processes here.
func groupMembers(int, int) ([]int, error) {
	return nil, errors.New("proc: no listing of a process group on this platform")
}

func stillMembers(int, []int) []int { return nil }

// exited cannot say here.
func exited(int) bool { return false }

// writersGone cannot say here: the output's end is read instead.
func writersGone(*os.File) (gone, known bool) { return false, false }
