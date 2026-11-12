//go:build !linux && !darwin

package api

import (
	"errors"
	"io/fs"
	"os"
)

// errTrackerUnsupported: this system has none of the descriptor-relative
// calls the token file's steps are made with (trackers_unix.go), so it
// keeps no token in a file.
var errTrackerUnsupported = errors.New("this system keeps no GitHub token in a file: give one in CONDUCTOR_GITHUB_TOKEN")

// openTrackerDir finds no directory to read, and makes none.
func openTrackerDir(_ string, create bool) (*os.File, error) {
	if create {
		return nil, errTrackerUnsupported
	}
	return nil, fs.ErrNotExist
}

func openTrackerAt(*os.File, string, int) (*os.File, error) { return nil, errTrackerUnsupported }

func renameTrackerAt(*os.File, string, string) error { return errTrackerUnsupported }

func removeTrackerAt(*os.File, string) error { return errTrackerUnsupported }

func lockTrackerFile(*os.File) error { return errTrackerUnsupported }
