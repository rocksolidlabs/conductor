//go:build unix

package journal

import (
	"os"
	"path/filepath"
	"testing"
)

// The records are 0600 files in 0700 directories, the state file and the
// lock too: what a session did is the server user's alone.
func TestFilesAre0600InA0700Directory(t *testing.T) {
	j, dir := newJournal(t, Options{})
	begin(t, j, serverRecord(sid(1)))
	h := filepath.Join(dir, Dir)
	for path, want := range map[string]os.FileMode{
		h:                             0o700,
		filepath.Join(h, SessionsDir): 0o700,
		filepath.Join(h, StateFile):   0o600,
		filepath.Join(h, Lock):        0o600,
		filepath.Join(h, SessionsDir, sid(1)+".json"): 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Fatalf("%s is %o, want %o", path, got, want)
		}
	}
}
