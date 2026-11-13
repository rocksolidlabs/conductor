//go:build unix

package gitrepo

import (
	"os"
	"syscall"
)

// openFlags open a file for reading without blocking (a named pipe with no
// writer answers at once) and without taking a terminal for the process.
// A regular file, the only kind careOS keeps, is read as any file is: the
// flag that keeps a pipe from blocking does nothing to it.
const openFlags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOCTTY
