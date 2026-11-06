//go:build linux

package gitcli

import (
	"os"
	"strconv"
)

// startIn is the working directory git starts in for RunIn: the open
// directory itself, through its descriptor in /proc/self/fd, which the
// child holds until it runs git and changes into before that (as
// internal/pty starts a session in its opened folder), checked to be dir;
// ErrNoDirHandle without /proc.
func startIn(dir *os.File) (string, error) {
	want, err := dir.Stat()
	if err != nil {
		return "", err
	}
	byHandle := "/proc/self/fd/" + strconv.Itoa(int(dir.Fd()))
	if fi, err := os.Stat(byHandle); err == nil && os.SameFile(fi, want) {
		return byHandle, nil
	}
	return "", ErrNoDirHandle
}
