//go:build linux

package crew

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// firstCommitHere says a new folder's repository, built aside, can be put
// in place here by one rename that fails if anything is there.
const firstCommitHere = true

// renameNoReplace moves from/fromName to to/toName, both named from the
// folders' descriptors, never over anything at toName: renameat2 with
// RENAME_NOREPLACE checks and renames in one step and answers EEXIST for
// whatever is there, an empty folder, a file or a link alike (rename(2)
// alone would replace an empty folder or a file), and follows no link at
// toName. A file system without the flag answers so
// (noReplaceUnsupported), and nothing is moved.
func renameNoReplace(from *os.File, fromName string, to *os.File, toName string) error {
	if err := unix.Renameat2(int(from.Fd()), fromName, int(to.Fd()), toName, unix.RENAME_NOREPLACE); err != nil {
		return &os.LinkError{Op: "renameat2", Old: fromName, New: toName, Err: err}
	}
	return nil
}

// noReplaceUnsupported reports whether err is renameNoReplace's answer on
// a file system that has no such rename (9p, as for Windows folders under
// WSL, NFS, some FUSE ones): the kernel's EINVAL, ENOSYS or EOPNOTSUPP.
func noReplaceUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP)
}
