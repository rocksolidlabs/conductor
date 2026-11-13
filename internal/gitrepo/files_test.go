package gitrepo

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
)

// Each small file of a repository has the bound of its kind; a file under
// objects (a loose object, a pack, its index) has none here, and a ref is
// a ref whatever its name.
func TestEachFileHasTheBoundOfItsKind(t *testing.T) {
	for rel, want := range map[string]int64{
		".git":                         maxSmall,
		"commondir":                    maxSmall,
		"HEAD":                         maxSmall,
		"refs/heads/main":              maxSmall,
		"refs/heads/objects/ab/cdef":   maxSmall,
		"objects/info/alternates":      maxSmall,
		"anything/else":                maxSmall,
		"config":                       maxConfig,
		"shallow":                      maxShallow,
		"packed-refs":                  maxPackedRefs,
		"objects/ab/cdef0123":          -1,
		"objects/pack/pack-1234.pack":  -1,
		"objects/pack/pack-1234.idx":   -1,
		"other/objects/ab/cdef0123":    -1,
		"objects/incoming-x/ab/cdef01": -1,
	} {
		if got := bound(rel); got != want {
			t.Errorf("bound(%q) = %d, want %d", rel, got, want)
		}
	}
}

// What is read of a small file stops at its bound, by Read and by ReadAt,
// when it grows past it after the open; a file already past it is refused
// at the open, before any read.
func TestAReadStopsAtTheFilesBound(t *testing.T) {
	dir := t.TempDir()
	for _, read := range []struct {
		name string
		read func(f billy.File) (int, error)
	}{
		{"Read", func(f billy.File) (int, error) {
			b, err := io.ReadAll(f)
			return len(b), err
		}},
		{"ReadAt", func(f billy.File) (int, error) { return f.ReadAt(make([]byte, 2*maxSmall), 0) }},
	} {
		write(t, dir, "HEAD", "ref: refs/heads/main\n")
		seen := &refusal{}
		defer seen.close()
		f, err := careFS(dir, seen).billy().Open("HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(filepath.Join(dir, "HEAD"), maxSmall+10); err != nil {
			t.Fatal(err)
		}
		n, err := read.read(f)
		f.Close()
		if n != maxSmall || !errors.Is(err, ErrUnreadable) || !errors.Is(seen.first(), ErrUnreadable) {
			t.Errorf("%s: %d bytes, %v (noted %v); want %d bytes and the refusal", read.name, n, err, seen.first(), maxSmall)
		}
		seen = &refusal{}
		defer seen.close()
		if _, err := careFS(dir, seen).billy().Open("HEAD"); !errors.Is(err, ErrUnreadable) || !errors.Is(seen.first(), ErrUnreadable) {
			t.Errorf("open past the bound: %v, want the refusal", err)
		}
	}
	// The package only reads.
	seen := &refusal{}
	defer seen.close()
	if _, err := careFS(dir, seen).billy().OpenFile("HEAD", os.O_RDWR, 0); !errors.Is(err, errReadOnly) {
		t.Errorf("open for writing: %v", err)
	}
}

// The file opened is the file the root looked at: one put in its place
// between the look and the open is refused, as the file it is.
func TestAFileReplacedAsItIsOpenedIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "HEAD", "ref: refs/heads/main\n")
	write(t, dir, "other", "ref: refs/heads/other\n")
	afterLook = func(string) {
		if err := os.Rename(filepath.Join(dir, "other"), filepath.Join(dir, "HEAD")); err != nil {
			t.Error(err)
		}
	}
	defer func() { afterLook = nil }()
	seen := &refusal{}
	defer seen.close()
	if f, err := careFS(dir, seen).billy().Open("HEAD"); !errors.Is(err, ErrUnreadable) || !errors.Is(seen.first(), ErrUnreadable) {
		if err == nil {
			f.Close()
		}
		t.Errorf("open: %v (noted %v), want the refusal", err, seen.first())
	}
}

// One opener: no function of the package opens a repository, or a file,
// another way than open and careOS (openOver, openRegular).
func TestTheRepositoryIsOpenedOneWay(t *testing.T) {
	only := map[string]string{
		"git.Open":                         "openOver",
		"filesystem.NewStorageWithOptions": "openOver",
		"dotgit.NewRepositoryFilesystem":   "openOver",
		"os.OpenRoot":                      "careFS",
		"chroot.New":                       "billy",
	}
	never := []string{"git.PlainOpen", "git.PlainOpenWithOptions", "filesystem.NewStorage", "osfs.New", "os.Open", "os.OpenFile", "os.ReadFile", "syscall.Open"}
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				x, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				callee := x.Name + "." + sel.Sel.Name
				if in, ok := only[callee]; (ok && fn.Name.Name != in) || slices.Contains(never, callee) {
					t.Errorf("%s: %s calls %s", fset.Position(call.Pos()), fn.Name.Name, callee)
				}
				return true
			})
		}
	}
}
