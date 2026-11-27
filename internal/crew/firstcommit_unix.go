//go:build linux || darwin

package crew

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameOverMade moves from/fromName to to/toName where the file system
// has no rename that refuses to replace (noReplaceUnsupported), never over
// anything that is there, as internal/export's renameByHand does: the name
// is taken first with mkdirat, which refuses anything already there, then
// the folder is renamed over that empty folder of Conductor's own, which a
// plain rename does, and refuses what another writer put there meanwhile:
// a folder no longer empty (ENOTEMPTY) or a file (ENOTDIR). The folder made
// is removed when the rename fails, if it is still an empty folder. made is
// called between the two steps.
func renameOverMade(from *os.File, fromName string, to *os.File, toName string, made func()) error {
	if err := unix.Mkdirat(int(to.Fd()), toName, 0o700); err != nil {
		return &os.PathError{Op: "mkdirat", Path: toName, Err: err}
	}
	made()
	if err := unix.Renameat(int(from.Fd()), fromName, int(to.Fd()), toName); err != nil {
		_ = unix.Unlinkat(int(to.Fd()), toName, unix.AT_REMOVEDIR)
		return &os.LinkError{Op: "renameat", Old: fromName, New: toName, Err: err}
	}
	return nil
}

// placeTaken reports whether err says something was at the name a repository
// was to be put at: EEXIST, ENOTEMPTY (a folder with something in it), or
// ENOTDIR (a file).
func placeTaken(err error) bool {
	return errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.ENOTDIR)
}

// rmdirAt removes dir/name only when it is an empty folder: never a file,
// a link or a folder something was put in.
func rmdirAt(dir *os.File, name string) error {
	return unix.Unlinkat(int(dir.Fd()), name, unix.AT_REMOVEDIR)
}

// lockFile takes f's lock for this process without waiting (flock): false
// when another holds it. The lock goes when f is closed, or with the
// process.
func lockFile(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
