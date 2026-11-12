//go:build linux || darwin

package api

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// The token file's steps on a system with descriptor-relative calls
// (trackers.go): each open never follows a link in its last name and never
// waits on what is there (a named pipe opens at once, and is then judged).

// openTrackerDir opens the directory at path (made 0700 first when
// create), which must be a directory and not a link.
func openTrackerDir(path string, create bool) (*os.File, error) {
	if create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return nil, errTrackerNotThere
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), trackerDir), nil
}

// openTrackerAt opens name relative to the directory d with flag (made
// 0600 with os.O_CREATE).
func openTrackerAt(d *os.File, name string, flag int) (*os.File, error) {
	fd, err := unix.Openat(int(d.Fd()), name, flag|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.ELOOP) {
		return nil, errTrackerNotThere
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), trackerDir+"/"+name), nil
}

// renameTrackerAt renames from to to, both in the directory d.
func renameTrackerAt(d *os.File, from, to string) error {
	return unix.Renameat(int(d.Fd()), from, int(d.Fd()), to)
}

// removeTrackerAt removes name from the directory d; no such name is no
// error.
func removeTrackerAt(d *os.File, name string) error {
	if err := unix.Unlinkat(int(d.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

// lockTrackerFile tries once for f's exclusive lock (flock): errTrackerBusy
// when another open file holds it, errTrackerNoLocks where the file system
// has none.
func lockTrackerFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EWOULDBLOCK):
		return errTrackerBusy
	case errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.ENOLCK), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL):
		return errTrackerNoLocks
	}
	return err
}
