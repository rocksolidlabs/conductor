//go:build linux

package api

import (
	"os"

	"golang.org/x/sys/unix"
)

// birthOf is the folder's birth time (statx), 0 where the file system
// keeps none.
func birthOf(path string, _ os.FileInfo) int64 {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &st); err != nil || st.Mask&unix.STATX_BTIME == 0 {
		return 0
	}
	return st.Btime.Sec*1e9 + int64(st.Btime.Nsec)
}
