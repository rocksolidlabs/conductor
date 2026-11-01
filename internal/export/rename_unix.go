//go:build !windows

package export

import "os"

// renameByHand moves staging/name to name in root without the kernel's
// no-replace flag, never over a name already there: a file is linked
// under its new name, which link(2) refuses when the name is taken
// (fs.ErrExist), and its staged name removed; a directory goes through
// os.Root's Rename, which refuses a directory already there (fs.ErrExist)
// as rename(2) refuses a file there for a directory.
func renameByHand(root *os.Root, staging, name string) error {
	from := staging + "/" + name
	fi, err := root.Lstat(from)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return root.Rename(from, name)
	}
	if err := root.Link(from, name); err != nil {
		return err
	}
	return root.Remove(from)
}
