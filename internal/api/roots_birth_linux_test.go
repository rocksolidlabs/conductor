//go:build linux

package api

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// Where the file system keeps a folder's birth time, what the folder is
// holds it.
func TestAFoldersBirthTimeIsReadWhereTheSystemKeepsOne(t *testing.T) {
	dir := t.TempDir()
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, dir, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &st); err != nil || st.Mask&unix.STATX_BTIME == 0 {
		t.Skip("this file system keeps no birth time")
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id := identify(dir, fi); id.birth == 0 || id.birth != st.Btime.Sec*1e9+int64(st.Btime.Nsec) {
		t.Fatalf("the birth time read: %d, the system's %d.%09d", id.birth, st.Btime.Sec, st.Btime.Nsec)
	}
}
