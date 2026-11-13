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
		const grew = "a file of the repository was not read: HEAD grew past its bound as it was read"
		if n != maxSmall || !errors.Is(err, ErrUnreadable) || err.Error() != grew || !errors.Is(seen.first(), ErrUnreadable) {
			t.Errorf("%s: %d bytes, %v (noted %v); want %d bytes and %q", read.name, n, err, seen.first(), maxSmall, grew)
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
// a folder refuses, by the type itself and through the chroot over it, a
// file it opened refuses Write and Truncate, and the folder and the file
// are as they were.
func TestTheFileSystemRefusesEveryChange(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "HEAD", "ref: refs/heads/main\n")
	seen := &refusal{}
	defer seen.close()
	fs := careFS(dir, gitDir, seen)
	at := func(name string) string { return filepath.Join(dir, name) }
	opened, err := fs.OpenFile(filepath.Join(fs.dir, "HEAD"), os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	through, err := fs.billy().Open("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer through.Close()
	for _, f := range []billy.File{opened, through} {
		if _, err := f.Write([]byte("ref: refs/heads/other\n")); !errors.Is(err, errReadOnly) {
			t.Errorf("Write: %v, want the read-only refusal", err)
		}
		if err := f.Truncate(0); !errors.Is(err, errReadOnly) {
			t.Errorf("Truncate: %v, want the read-only refusal", err)
		}
	}
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
	if b, err := os.ReadFile(at("HEAD")); err != nil || string(b) != "ref: refs/heads/main\n" {
		t.Errorf("HEAD holds %q (%v), want it as written", b, err)
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
	f, err := careFS(dir, gitDir, seen).billy().Open("HEAD")
	if err == nil {
		f.Close()
	}
	if !errors.Is(err, ErrUnreadable) || !strings.HasSuffix(err.Error(), ": HEAD changed as it was opened") || !errors.Is(seen.first(), ErrUnreadable) {
		t.Errorf("open: %v (noted %v), want HEAD refused as changed as it was opened", err, seen.first())
	}
}

// One opener: no function of the package opens a repository, a folder or
// a file another way than open and careOS. The test reads the package's
// source files (its tests aside) as text, parsed with go/parser and no
// types, and checks:
//
//   - each name the package uses (a call or a function value), followed to
//     the identifier it starts at (osfs.Default.Open is osfs's): one in
//     only is used in the functions it lists alone, and one in never
//     (os.Open, os.OpenFile, os.ReadFile, os.Lstat, git.PlainOpen and the
//     rest; anything of ioutil, osfs or go-billy's util; any method of
//     os.Root named by its type) nowhere;
//   - no import of go-billy's osfs or util, or of io/ioutil;
//   - outside OpenFile and ReadDir, the two functions that open a file, no
//     call of a method named Open, ReadFile or FS that only does not list:
//     the syntax does not tell a value (a root under another name) from a
//     package, so a package's is refused too;
//   - no method expression on os.Root ((*os.Root).OpenFile) and no
//     variable assigned the field root (r := fs.root), anywhere;
//   - careOS and careFile have the methods they define and no other (none
//     promoted from a type either embeds), listed by reflection.
//
// A root reached another way (a slice's element, a function's result, an
// interface) needs types to be seen, and is not checked.
func TestTheRepositoryIsOpenedOneWay(t *testing.T) {
	only := map[string][]string{
		"git.Open":                         {"openOver"},
		"filesystem.NewStorageWithOptions": {"openOver"},
		"dotgit.NewRepositoryFilesystem":   {"openOver"},
		"os.OpenRoot":                      {"careFS"},
		"fs.root.OpenRoot":                 {"under"},
		"fs.root.OpenFile":                 {"OpenFile", "ReadDir"},
		"chroot.New":                       {"billy"},
		"os.Stat":                          {"gitDirs", "named"},
	}
	never := []string{"git.PlainOpen", "git.PlainOpenWithOptions", "git.PlainInit", "git.PlainClone", "filesystem.NewStorage",
		"os.Open", "os.OpenFile", "os.OpenInRoot", "os.Create", "os.ReadFile", "os.ReadDir", "os.DirFS", "os.Lstat", "os.NewFile",
		"syscall.Open", "syscall.Openat", "ioutil.", "osfs.", "util.", "os.Root."} // ending in '.': anything of that package or type
	imports := []string{"github.com/go-git/go-billy/v5/osfs", "github.com/go-git/go-billy/v5/util", "io/ioutil"}
	opens := []string{"OpenFile", "ReadDir"}
	byPath := []string{"Open", "ReadFile", "FS"}
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
			fromRoot := func(values []ast.Expr) {
				for _, v := range values {
					if sel, ok := v.(*ast.SelectorExpr); ok && sel.Sel.Name == "root" {
						t.Errorf("%s: %q assigns %s to a variable", fset.Position(v.Pos()), in, chain(sel))
					}
				}
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.AssignStmt:
					fromRoot(n.Rhs)
				case *ast.ValueSpec:
					fromRoot(n.Values)
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && slices.Contains(byPath, sel.Sel.Name) && !slices.Contains(opens, in) {
						if _, listed := only[chain(sel)]; !listed {
							t.Errorf("%s: %q calls %s", fset.Position(n.Pos()), in, sel.Sel.Name)
						}
					}
				case *ast.SelectorExpr:
					used := chain(n)
					banned := slices.ContainsFunc(never, func(n string) bool {
						return used == n || strings.HasSuffix(n, ".") && strings.HasPrefix(used, n)
					})
					if where, ok := only[used]; (ok && !slices.Contains(where, in)) || banned {
						t.Errorf("%s: %q uses %s", fset.Position(n.Pos()), in, used)
					}
					x := n.X
					for p, ok := x.(*ast.ParenExpr); ok; p, ok = x.(*ast.ParenExpr) {
						x = p.X
					}
					if star, ok := x.(*ast.StarExpr); ok {
						x = star.X
					}
					if typ, ok := x.(*ast.SelectorExpr); ok && chain(typ) == "os.Root" {
						t.Errorf("%s: %q uses os.Root's method %s", fset.Position(n.Pos()), in, n.Sel.Name)
					}
				}
				return true
			})
		}
	}
	for typ, want := range map[reflect.Type][]string{
		reflect.TypeFor[*careOS]():   {"Chmod", "Create", "Join", "MkdirAll", "Open", "OpenFile", "ReadDir", "Remove", "RemoveAll", "Rename", "Stat", "Symlink", "TempFile"},
		reflect.TypeFor[*careFile](): {"Close", "Lock", "Name", "Read", "ReadAt", "Seek", "Truncate", "Unlock", "Write"},
	} {
		var got []string
		for i := 0; i < typ.NumMethod(); i++ {
			got = append(got, typ.Method(i).Name)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%v's methods are %v, want %v", typ, got, want)
		}
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
