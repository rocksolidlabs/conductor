//go:build unix

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
// fs.ErrExist and the staged entry still staged, and a name taken between
// the directory it makes in the name's place and its rename (the seam).
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

	// Between the directory made in the name's place and the rename (the
	// window a check before a rename leaves open), another writer's file
	// in it, or a file in its place, is never renamed over: fs.ErrExist,
	// what was put there as it was, the staged directory still staged.
	for _, c := range []struct {
		name   string
		meddle func(p string)
		check  func(p string) bool
	}{
		{"filled", func(p string) {
			write("filled/mine.json", "mine")
		}, func(p string) bool { return read("filled/mine.json") == "mine" }},
		{"swapped", func(p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			write("swapped", "mine")
		}, func(p string) bool { return read("swapped") == "mine" }},
	} {
		write("s/"+c.name+"/a.json", "a")
		afterMkdir = func(name string) {
			if name == c.name {
				c.meddle(filepath.Join(dir, name))
			}
		}
		err := renameByHand(root, "s", c.name)
		afterMkdir = nil
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("%s meanwhile: %v", c.name, err)
		}
		if !c.check(filepath.Join(dir, c.name)) || read("s/"+c.name+"/a.json") != "a" {
			t.Fatalf("%s meanwhile: what was put there, or the staged directory, changed", c.name)
		}
	}
}
