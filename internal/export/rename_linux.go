//go:build linux

package export

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace moves staging/name to name in root, refusing a name
// already there of whatever kind (fs.ErrExist): renameat2 with
// RENAME_NOREPLACE, from the staging directory's descriptor to the data
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
	err = unix.Renameat2(int(from.Fd()), name, int(to.Fd()), name, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return renameByHand(root, staging, name)
	}
	if err != nil {
		return &os.LinkError{Op: "renameat2", Old: staging + "/" + name, New: name, Err: err}
	}
	return nil
}
