//go:build unix

package gitrepo

import (
	"fmt"
	"os"
	"syscall"
)

// openRegular opens name for reading without blocking (a named pipe with
// no writer answers at once) and without taking a terminal for the
// process, judges the open file by its own descriptor (fstat, never a stat
// of the path before it), keeps it only when it is a regular file, and
// then reads it blocking as any file. size is its length at the open.
func openRegular(name string) (_ *os.File, size int64, err error) {
	var fd int
	for {
		fd, err = syscall.Open(name, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return nil, 0, &os.PathError{Op: "open", Path: name, Err: err}
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return nil, 0, &os.PathError{Op: "fstat", Path: name, Err: err}
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		syscall.Close(fd)
		return nil, 0, fmt.Errorf("%w: %s is not a regular file", ErrUnreadable, name)
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		syscall.Close(fd)
		return nil, 0, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), st.Size, nil
}
