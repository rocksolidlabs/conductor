package gitrepo

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
		f, err := careFS(dir, gitDir, seen).billy().Open("HEAD")
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
		if _, err := careFS(dir, gitDir, seen).billy().Open("HEAD"); !errors.Is(err, ErrUnreadable) || !errors.Is(seen.first(), ErrUnreadable) {
			t.Errorf("open past the bound: %v, want the refusal", err)
		}
	}
	// The package only reads.
	seen := &refusal{}
	defer seen.close()
	if _, err := careFS(dir, gitDir, seen).billy().OpenFile("HEAD", os.O_RDWR, 0); !errors.Is(err, errReadOnly) {
		t.Errorf("open for writing: %v", err)
	}
}

// The file system the package hands go-git only reads: every way to change
// a folder refuses, by the type itself and through the chroot over it, and
// the folder is as it was.
func TestTheFileSystemRefusesEveryChange(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "HEAD", "ref: refs/heads/main\n")
	seen := &refusal{}
	defer seen.close()
	fs := careFS(dir, gitDir, seen)
	at := func(name string) string { return filepath.Join(dir, name) }
	for op, err := range map[string]error{
		"Rename":    fs.Rename(at("HEAD"), at("moved")),
		"Remove":    fs.Remove(at("HEAD")),
		"RemoveAll": fs.RemoveAll(dir),
		"MkdirAll":  fs.MkdirAll(at("made"), 0o755),
		"Symlink":   fs.Symlink("HEAD", at("link")),
		"Chmod":     fs.Chmod(at("HEAD"), 0o600),
	} {
		if !errors.Is(err, errReadOnly) {
			t.Errorf("%s: %v, want the read-only refusal", op, err)
		}
	}
	b := fs.billy()
	for op, err := range map[string]error{
		"Rename":   b.Rename("HEAD", "moved"),
		"Remove":   b.Remove("HEAD"),
		"MkdirAll": b.MkdirAll("made", 0o755),
		"Symlink":  b.Symlink("HEAD", "link"),
		"Chmod":    b.(billy.Chmod).Chmod("HEAD", 0o600),
	} {
		if err == nil {
			t.Errorf("%s through the chroot was made", op)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "HEAD" {
		t.Errorf("the folder holds %v (%v), want HEAD alone", entries, err)
	}
	if fi, err := os.Stat(at("HEAD")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("HEAD: %v %v, want it as written", fi, err)
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
	if f, err := careFS(dir, gitDir, seen).billy().Open("HEAD"); !errors.Is(err, ErrUnreadable) || !errors.Is(seen.first(), ErrUnreadable) {
		if err == nil {
			f.Close()
		}
		t.Errorf("open: %v (noted %v), want the refusal", err, seen.first())
	}
}

// One opener: no function of the package opens a repository, a folder or
// a file another way than open and careOS. Every name the package uses
// (a call or a function value), followed to the identifier it starts at
// (osfs.Default.Open is osfs's), is used only where it is meant to be, or
// nowhere; and careOS has the methods it defines and no other (none
// promoted from a type it embeds).
func TestTheRepositoryIsOpenedOneWay(t *testing.T) {
	only := map[string][]string{
		"git.Open":                         {"openOver"},
		"filesystem.NewStorageWithOptions": {"openOver"},
		"dotgit.NewRepositoryFilesystem":   {"openOver"},
		"os.OpenRoot":                      {"careFS"},
		"fs.root.OpenRoot":                 {"under"},
		"fs.root.OpenFile":                 {"OpenFile", "ReadDir"},
		"chroot.New":                       {"billy"},
		"os.Stat":                          {"gitDirs"},
	}
	never := []string{"git.PlainOpen", "git.PlainOpenWithOptions", "git.PlainInit", "git.PlainClone", "filesystem.NewStorage",
		"os.Open", "os.OpenFile", "os.OpenInRoot", "os.Create", "os.ReadFile", "os.ReadDir", "os.DirFS", "os.Lstat", "os.NewFile",
		"syscall.Open", "syscall.Openat", "ioutil.", "osfs.", "util."} // ending in '.': anything of that package
	imports := []string{"github.com/go-git/go-billy/v5/osfs", "github.com/go-git/go-billy/v5/util", "io/ioutil"}
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
		for _, imp := range file.Imports {
			if slices.Contains(imports, strings.Trim(imp.Path.Value, `"`)) {
				t.Errorf("%s imports %s", name, imp.Path.Value)
			}
		}
		for _, decl := range file.Decls {
			in := "" // a declaration outside any function
			if fn, ok := decl.(*ast.FuncDecl); ok {
				in = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				used := chain(sel)
				banned := slices.ContainsFunc(never, func(n string) bool {
					return used == n || strings.HasSuffix(n, ".") && strings.HasPrefix(used, n)
				})
				if where, ok := only[used]; (ok && !slices.Contains(where, in)) || banned {
					t.Errorf("%s: %q uses %s", fset.Position(sel.Pos()), in, used)
				}
				return true
			})
		}
	}
	want := []string{"Chmod", "Create", "Join", "MkdirAll", "Open", "OpenFile", "ReadDir", "Remove", "RemoveAll", "Rename", "Stat", "Symlink", "TempFile"}
	var got []string
	for typ, i := reflect.TypeFor[*careOS](), 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Errorf("careOS's methods are %v, want %v", got, want)
	}
}

// chain is sel's names joined from the identifier it starts at (os.Open,
// osfs.Default.Open, fs.root.OpenFile), "" when it starts at no
// identifier (a call's result).
func chain(sel *ast.SelectorExpr) string {
	names := []string{sel.Sel.Name}
	for x := sel.X; ; {
		switch v := x.(type) {
		case *ast.SelectorExpr:
			names, x = append([]string{v.Sel.Name}, names...), v.X
		case *ast.Ident:
			return strings.Join(append([]string{v.Name}, names...), ".")
		default:
			return ""
		}
	}
}
