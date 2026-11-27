//go:build unix

package api

import (
	"os"
	"syscall"
)

// folderOwner is the folder's owner, -1 when its stat names none.
func folderOwner(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// serverUID is the server's own user.
func serverUID() int { return os.Getuid() }
