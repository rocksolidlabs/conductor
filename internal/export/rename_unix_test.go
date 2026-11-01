//go:build !windows

package export

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// renameByHand, the move where the kernel has no no-replace flag, moves a
// file and a directory into a name that is free, and leaves a name that
// is taken as it is: a file, an empty directory and a full one, each with
// fs.ErrExist and the staged entry still staged.
func TestRenameByHand(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	write("s/free.json", "staged")
	write("s/d/a.json", "a")
	if err := renameByHand(root, "s", "free.json"); err != nil || read("free.json") != "staged" {
		t.Fatalf("a file to a free name: %v", err)
	}
	if err := renameByHand(root, "s", "d"); err != nil || read("d/a.json") != "a" {
		t.Fatalf("a directory to a free name: %v", err)
	}

	write("s/taken.json", "staged")
	write("taken.json", "mine")
	write("s/empty/a.json", "a")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	write("s/full/a.json", "a")
	write("full/mine.json", "mine")
	for _, name := range []string{"taken.json", "empty", "full"} {
		if err := renameByHand(root, "s", name); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("%s, taken: %v", name, err)
		}
	}
	if read("taken.json") != "mine" || read("s/taken.json") != "staged" || read("full/mine.json") != "mine" || read("s/empty/a.json") != "a" {
		t.Fatal("a taken name or a staged entry changed")
	}
	if got, err := os.ReadDir(filepath.Join(dir, "empty")); err != nil || len(got) != 0 {
		t.Fatalf("the empty directory: %v %v", got, err)
	}
}
