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
