//go:build !linux && !darwin

package crew

import (
	"errors"
	"os"
)

// firstCommitHere is false here: no rename that refuses to replace, nor
// the steps that stand in for one, is known on this system, so a new
// folder's first commit is not built (EnsureFirstCommit says so).
const firstCommitHere = false

var errNotHere = errors.New("not on this system")

func renameNoReplace(*os.File, string, *os.File, string) error        { return errNotHere }
func renameOverMade(*os.File, string, *os.File, string, func()) error { return errNotHere }
func noReplaceUnsupported(error) bool                                 { return false }
func placeTaken(error) bool                                           { return false }
func rmdirAt(*os.File, string) error                                  { return errNotHere }
func lockFile(*os.File) (bool, error)                                 { return false, errNotHere }
