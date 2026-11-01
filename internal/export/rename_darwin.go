//go:build darwin

package export

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace moves staging/name to name in root, refusing a name
// already there of whatever kind (fs.ErrExist): renameatx_np with
// RENAME_EXCL, from the staging directory's descriptor to the data
// directory's. A filesystem without the flag gets renameByHand.
func renameNoReplace(root *os.Root, staging, name string) error {
	from, err := root.Open(staging)
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := root.Open(".")
	if err != nil {
		return err
	}
	defer to.Close()
	err = unix.RenameatxNp(int(from.Fd()), name, int(to.Fd()), name, unix.RENAME_EXCL)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return renameByHand(root, staging, name)
	}
	if err != nil {
		return &os.LinkError{Op: "renameatx_np", Old: staging + "/" + name, New: name, Err: err}
	}
	return nil
}
