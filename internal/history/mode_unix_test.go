//go:build unix

package history

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The index, its -wal and its -shm are 0600 in a 0700 directory under a
// umask that makes files 0644: the file is made private before SQLite
// opens it, and SQLite makes the -wal and -shm with its mode.
func TestTheIndexFilesArePrivate(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	dir := filepath.Join(t.TempDir(), "history")
	ix := openIndex(t, dir, Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the directory: %v %v", fi.Mode(), err)
	}
	for _, name := range []string{File, File + "-wal", File + "-shm"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want 0600", name, fi.Mode().Perm())
		}
	}
}
