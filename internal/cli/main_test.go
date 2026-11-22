package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the package's servers and hosts a directory of their own
// for this machine's id (machineDir), so that no test makes or reads the
// id of the machine it runs on.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cli-machine-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "a directory for the machine's id:", err)
		os.Exit(1)
	}
	machineDir = func() (string, error) { return filepath.Join(dir, "conductor"), nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
