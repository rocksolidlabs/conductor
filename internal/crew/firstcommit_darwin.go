//go:build darwin

package crew

import (
	"os"

	"golang.org/x/sys/unix"
)

// firstCommitHere says a new folder's repository, built aside, can be put
// in place here by one rename that fails if anything is there.
const firstCommitHere = true

// renameNoReplace moves from/fromName to to/toName, both named from the
// folders' descriptors, never over anything at toName: renameatx_np with
// RENAME_EXCL checks and renames in one step and answers EEXIST for
// whatever is there, an empty folder, a file or a link alike (rename(2)
// alone would replace an empty folder or a file). A file system without
// the flag refuses it, and nothing is moved: the first commit is refused
// there.
func renameNoReplace(from *os.File, fromName string, to *os.File, toName string) error {
	if err := unix.RenameatxNp(int(from.Fd()), fromName, int(to.Fd()), toName, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "renameatx_np", Old: fromName, New: toName, Err: err}
	}
	return nil
}

// rmdirAt removes dir/name only when it is an empty folder: never a file,
// a link or a folder something was put in.
func rmdirAt(dir *os.File, name string) error {
	return unix.Unlinkat(int(dir.Fd()), name, unix.AT_REMOVEDIR)
}
