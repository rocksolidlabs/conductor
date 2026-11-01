//go:build windows

package export

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// renameNoReplace moves staging/name to name in root, refusing a name
// already there of whatever kind (fs.ErrExist): MoveFileEx without
// MOVEFILE_REPLACE_EXISTING (os.Root's Rename replaces a file), with
// MOVEFILE_WRITE_THROUGH, since a directory handle cannot be flushed on
// Windows. The staging directory is the restore's own, in the data
// directory its reservation holds.
func renameNoReplace(root *os.Root, staging, name string) error {
	oldPath := filepath.Join(root.Name(), staging, name)
	newPath := filepath.Join(root.Name(), name)
	from, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "movefileex", Old: oldPath, New: newPath, Err: err}
	}
	return nil
}
