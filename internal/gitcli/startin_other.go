//go:build !linux

package gitcli

import "os"

// startIn is ErrNoDirHandle here: Go has no descriptor-based change of
// directory for a new process on these systems, so git would find the
// folder by its path again, and RunIn does not run it.
func startIn(*os.File) (string, error) { return "", ErrNoDirHandle }
