//go:build unix

package api

import (
	"errors"
	"os"
	"syscall"
)

// checkAccountDir checks a Brakeman account folder: a directory the
// server's user owns, read by nothing but a stat (the agent's login lives
// there; Conductor never reads it).
func checkAccountDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return errors.New("no such folder on this server")
	}
	if !fi.IsDir() {
		return errors.New("not a folder")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return errors.New("a folder the server's user does not own")
	}
	return nil
}
