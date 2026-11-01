//go:build unix

package export

import (
	"errors"
	"os"
	"syscall"
)

// tryLock tries once for the exclusive lock (flock) of f: locked, or not
// (another open file holds it, or the filesystem has no locks to tell by:
// a network or 9p mount); an error is trouble with the file itself.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN), noLocks(err):
		return false, nil
	}
	return false, err
}

// lockWait takes the exclusive lock of f, a file this process has just
// made, waiting out a reservation's look at it (takeOver holds it only to
// read it): false on a filesystem with no locks.
func lockWait(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	switch {
	case err == nil:
		return true, nil
	case noLocks(err):
		return false, nil
	}
	return false, err
}

// unlock lets the lock of f go.
func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// noLocks says err is a filesystem's answer that it has no locks.
func noLocks(err error) bool {
	return errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOLCK) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL)
}
