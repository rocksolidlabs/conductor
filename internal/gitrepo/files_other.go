//go:build !unix

package gitrepo

import (
	"fmt"
	"os"
)

// openRegular opens name for reading and keeps it only when the open file
// (its handle, never the path again) is a regular file. These systems
// (Windows) have no flags to open without blocking or without taking a
// terminal, so the check of the open file is the whole of it here. size is
// its length at the open.
func openRegular(name string) (*os.File, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("%w: %s is not a regular file", ErrUnreadable, name)
	}
	return f, fi.Size(), nil
}
