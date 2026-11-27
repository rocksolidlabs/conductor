//go:build darwin

package api

import (
	"os"
	"syscall"
)

// birthOf is the folder's birth time, from its stat.
func birthOf(_ string, fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Birthtimespec.Sec*1e9 + st.Birthtimespec.Nsec
	}
	return 0
}
