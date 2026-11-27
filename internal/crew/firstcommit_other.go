//go:build !linux && !darwin

package crew

import (
	"errors"
	"os"
)

// firstCommitHere is false here: no rename that fails when something is
// at its name is known on this system, so a new folder's first commit is
// not built, and the person is asked to make it (EnsureFirstCommit).
const firstCommitHere = false

func renameNoReplace(*os.File, string, *os.File, string) error {
	return errors.New("no rename that refuses an existing name on this system")
}

func rmdirAt(*os.File, string) error { return errors.New("not on this system") }
