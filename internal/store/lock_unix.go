//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// OwnerLock is an owner lock, held for the process's life: the file,
// locked exclusively (flock), with the owner's pid and start time written
// into it for the error messages of a server that finds it held. Nothing
// trusts those two. The managed runs' ledger (runs/owner.lock) and
// Brakeman's records (brakeman/owner.lock) each take one: of two servers
// on one data directory, the one holding it writes.
type OwnerLock struct{ f *os.File }

// TakeOwnerLock tries once for the lock at path. It returns no lock and
// the reason when another process holds it (held) or when the filesystem
// does not support locks (a network or 9p mount): the caller is then
// read-only, until a held lock is taken (a later try). An error is for
// trouble with the file itself. The lock is per open file, so a second try
// in the same process finds it held too.
func TakeOwnerLock(path string) (lock *OwnerLock, why string, held bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, "", false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			holder, _ := os.ReadFile(path)
			return nil, fmt.Sprintf("another server holds %s (%s)", path, strings.TrimSpace(string(holder))), true, nil
		}
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOLCK) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL) {
			return nil, fmt.Sprintf("the filesystem of %s does not support locks (%v)", path, err), false, nil
		}
		return nil, "", false, fmt.Errorf("lock %s: %w", path, err)
	}
	// The lock is ours: say who, for the next server's message.
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte("pid "+strconv.Itoa(os.Getpid())+" since "+time.Now().UTC().Format(time.RFC3339)+"\n"), 0)
	}
	return &OwnerLock{f: f}, "", false, nil
}

// Release lets the lock go and closes its file.
func (o *OwnerLock) Release() {
	syscall.Flock(int(o.f.Fd()), syscall.LOCK_UN)
	o.f.Close()
}

// LockHeld tries once for the lock at path without making the file, and
// lets it go at once: true when another process (or another open file of
// this one) holds it. A file that is not there is held by nobody, and a
// filesystem with no locks tells nothing, which is false too.
func LockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOLCK) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL) {
			return false, nil
		}
		return false, fmt.Errorf("lock %s: %w", path, err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

// ProbeOwnerLock tries once for the lock at path, never making the file,
// opening it only to read, and writing nothing to it: held when another
// open file holds the lock, and then line, the first line of what its
// holder wrote, read in that same try (at most lineMax bytes; a longer
// line, or one not yet written, is ""); noLocks when the filesystem does
// not support locks. A free lock is let go at once. An error is for a file
// that cannot be opened or locked: a missing one among them
// (fs.ErrNotExist), since a holder may hold a file that was removed.
// Nothing trusts line.
func ProbeOwnerLock(path string, lineMax int) (held bool, line string, noLocks bool, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, "", false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, firstLine(f, lineMax), false, nil
		}
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOLCK) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL) {
			return false, "", true, nil
		}
		return false, "", false, fmt.Errorf("lock %s: %w", path, err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, "", false, nil
}

// firstLine is the first line of f, without its newline, when it ends
// within limit bytes; "" otherwise, or when f cannot be read.
func firstLine(f *os.File, limit int) string {
	if limit <= 0 {
		return ""
	}
	buf := make([]byte, limit+1)
	n, _ := f.ReadAt(buf, 0)
	i := strings.IndexByte(string(buf[:n]), '\n')
	if i < 0 {
		return ""
	}
	return string(buf[:i])
}
