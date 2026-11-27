//go:build linux

package crew

import (
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
// toName. A file system without the flag answers EINVAL, and nothing is
// moved: the first commit is refused there.
func renameNoReplace(from *os.File, fromName string, to *os.File, toName string) error {
	if err := unix.Renameat2(int(from.Fd()), fromName, int(to.Fd()), toName, unix.RENAME_NOREPLACE); err != nil {
		return &os.LinkError{Op: "renameat2", Old: fromName, New: toName, Err: err}
	}
	return nil
}

// rmdirAt removes dir/name only when it is an empty folder: never a file,
// a link or a folder something was put in.
func rmdirAt(dir *os.File, name string) error {
	return unix.Unlinkat(int(dir.Fd()), name, unix.AT_REMOVEDIR)
}
