//go:build unix

package ledger

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ownerLock is the owner lock, held for the process's life: the file,
// locked exclusively (flock), with the owner's pid and start time written
// into it for the error messages of a server that finds it held. Nothing
// trusts those two.
type ownerLock struct{ f *os.File }

// takeOwnerLock tries once for the lock at path. It returns no lock and
// the reason when another process holds it (held) or when the filesystem
// does not support locks (a network or 9p mount): managed runs are then
// off for this server, until a held lock is taken (Ledger.awaitLock). An
// error is for trouble with the file itself.
func takeOwnerLock(path string) (lock *ownerLock, why string, held bool, err error) {
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
		return nil, "", false, fmt.Errorf("ledger: lock %s: %w", path, err)
	}
	// The lock is ours: say who, for the next server's message.
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte("pid "+strconv.Itoa(os.Getpid())+" since "+time.Now().UTC().Format(time.RFC3339)+"\n"), 0)
	}
	return &ownerLock{f: f}, "", false, nil
}

func (o *ownerLock) release() {
	syscall.Flock(int(o.f.Fd()), syscall.LOCK_UN)
	o.f.Close()
}

// lockHeld tries once for the lock at path without making the file, and
// lets it go at once: true when another process (or another open file of
// this one) holds it. A file that is not there is held by nobody, and a
// filesystem with no locks tells nothing, which is false too.
func lockHeld(path string) (bool, error) {
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
		return false, fmt.Errorf("ledger: lock %s: %w", path, err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}
