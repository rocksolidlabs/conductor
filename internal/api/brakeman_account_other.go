//go:build !unix

package api

import (
	"errors"
	"os"
)

// checkAccountDir checks a Brakeman account folder: a directory, read by
// nothing but a stat. Ownership is the file system's to say where it has
// no Unix owner.
func checkAccountDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return errors.New("no such folder on this server")
	}
	if !fi.IsDir() {
		return errors.New("not a folder")
	}
	return nil
}
