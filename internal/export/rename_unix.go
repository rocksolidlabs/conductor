//go:build unix

package export

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// afterMkdir is the seam a test sets (rename_unix_test.go): between the
// directory renameByHand makes in a name's place and its rename.
var afterMkdir func(name string)

// renameByHand moves staging/name to name in root without the kernel's
// no-replace flag, never over a name already there (fs.ErrExist). A file
// is linked under its new name, which link(2) refuses when the name is
// taken, and its staged name removed. A directory cannot be linked, and
// rename(2) replaces an empty directory, which a check before it cannot
// rule out (one made between the check and the rename). So the name is
// taken first with mkdir(2), which refuses one already there of whatever
// kind; then the staged directory is renamed over that empty directory of
// the restore's own, and rename(2) refuses what another writer put there
// meanwhile: a directory no longer empty (ENOTEMPTY), or a file (ENOTDIR).
// The directory made is removed when the rename fails, if it is still an
// empty directory (rmdir(2)), never a file.
func renameByHand(root *os.Root, staging, name string) error {
	from := staging + "/" + name
	fi, err := root.Lstat(from)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if err := root.Link(from, name); err != nil {
			return err
		}
		return root.Remove(from)
	}
	src, err := root.Open(staging)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dst.Close()
	if err := root.Mkdir(name, 0o700); err != nil {
		return err
	}
	if afterMkdir != nil {
		afterMkdir(name)
	}
	if err := unix.Renameat(int(src.Fd()), name, int(dst.Fd()), name); err != nil {
		unix.Unlinkat(int(dst.Fd()), name, unix.AT_REMOVEDIR)
		if errors.Is(err, unix.ENOTDIR) {
			err = fmt.Errorf("%w: %w", fs.ErrExist, err)
		}
		return &os.LinkError{Op: "renameat", Old: from, New: name, Err: err}
	}
	return nil
}
