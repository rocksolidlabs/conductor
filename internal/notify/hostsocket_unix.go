//go:build unix

package notify

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// CheckHostSocketDir says whether dir is a folder only this user can enter:
// a folder (not a link to one), owned by this user, of mode 0700. The host
// checks the folder it made before it listens in it, and the sender before
// it hands the socket in it a token.
func CheckHostSocketDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("the host's socket folder is not a folder")
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		return fmt.Errorf("the host's socket folder has mode %04o, not 0700", perm)
	}
	if !ownedHere(fi) {
		return errors.New("the host's socket folder is not this user's")
	}
	return nil
}

// checkOwnSocket says whether path is a socket (not a link to one) owned by
// this user.
func checkOwnSocket(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return errors.New("the host's socket is not a socket")
	}
	if !ownedHere(fi) {
		return errors.New("the host's socket is not this user's")
	}
	return nil
}

func ownedHere(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
