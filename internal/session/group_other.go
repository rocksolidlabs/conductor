//go:build !unix

package session

import "errors"

// groupAlive cannot say here: a move proves nothing gone, and is refused.
func groupAlive(int) (bool, error) {
	return false, errors.New("session: no process group to look at on this platform")
}

func killGroup(int) {}
