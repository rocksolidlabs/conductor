package gitrepo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/helper/chroot"
	"github.com/go-git/go-git/v5"
)

// ErrUnreadable says a file of the repository is not a regular file, or
// is longer than the opener reads of a file of its kind (bound).
var ErrUnreadable = errors.New("a file of the repository is not a regular file within its bound")

// errReadOnly answers an open for writing: the package only reads.
var errReadOnly = errors.New("the repository is opened for reading only")

// What the opener reads of a repository's own small files, at most. go-git
// reads each whole (a ref, the configuration) or line by line
// (packed-refs, shallow); git writes none anywhere near its bound.
const (
	// maxSmall bounds HEAD, a loose ref, the .git file of a linked
	// worktree, commondir, objects/info/alternates and any other file not
	// named below: each holds an object id, a ref's name or a folder's
	// path, a line each, and 64 KiB holds sixteen paths of the longest
	// Linux opens (4,096 bytes).
	maxSmall = 64 << 10
	// maxConfig bounds config: git writes a few lines a remote and a
	// branch, and 4 MiB holds tens of thousands of them.
	maxConfig = 4 << 20
	// maxShallow bounds shallow: 41 bytes a commit at a shallow clone's
	// edge, and 4 MiB holds a hundred thousand of them.
	maxShallow = 4 << 20
	// maxPackedRefs bounds packed-refs, the large one: about a hundred
	// bytes a ref (an id, a name, a tag's peeled id), and 256 MiB holds the
	// millions of refs of the largest repositories hosted today.
	maxPackedRefs = 256 << 20
)

// bound is how much the opener reads of the file at rel, its path from the
// folder of its file system ('/'): -1 for one under objects (a loose
// object, a pack, its index), which go-git streams (largeObject) and which
// is not bounded here.
func bound(rel string) int64 {
	switch rel {
	case "config":
		return maxConfig
	case "shallow":
		return maxShallow
	case "packed-refs":
		return maxPackedRefs
	}
	if !strings.HasPrefix(rel, "refs/") && path.Base(rel) != "alternates" && strings.Contains("/"+rel, "/objects/") {
		return -1
	}
	return maxSmall
}

// careOS is the file system under every one the package hands go-git
// (open): an os.Root opened once on its folder, dir, through which every
// open, stat and listing of a file goes, by the name asked for relative to
// the folder. The root follows a link within the folder and refuses one
// that leads out of it, which careOS notes as the opener's refusal
// (go-git reads past some failed opens: a loose ref's, to packed-refs).
// go-billy's chroot over it (billy) only joins names: careOS has no Lstat
// or Readlink, so the chroot resolves no link and hands it each name
// unresolved.
type careOS struct {
	root *os.Root // nil when the folder did not open: err
	err  error
	dir  string
	seen *refusal // the opening's, shared by all its file systems
}

// careFS is a file system over the folder dir with its links resolved, as
// osfs.New(dir) is; under is one over its folder name, opened beneath it.
func careFS(dir string, seen *refusal) *careOS {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	root, err := os.OpenRoot(dir)
	return seen.keep(&careOS{root: root, err: err, dir: dir, seen: seen})
}

func (fs *careOS) under(name string) *careOS {
	root, err := fs.root.OpenRoot(name)
	return fs.seen.keep(&careOS{root: root, err: fs.judge(name, err), dir: filepath.Join(fs.dir, name), seen: fs.seen})
}

// billy is fs as go-git takes it, its names joined under dir.
func (fs *careOS) billy() billy.Filesystem { return chroot.New(fs, fs.dir) }

// rel is name, a path under dir as the chroot joins it, relative to dir.
func (fs *careOS) rel(name string) (string, error) {
	if fs.root == nil {
		return "", fs.err
	}
	rel, err := filepath.Rel(fs.dir, name)
	if err != nil || !filepath.IsLocal(rel) {
		return "", billy.ErrCrossedBoundary
	}
	return rel, nil
}

// judge is err, the root's answer for name, noted as the opener's refusal
// when name leads out of the folder.
func (fs *careOS) judge(name string, err error) error {
	if err != nil && fs.root != nil && escapes(fs.root, err) {
		return fs.seen.note(fmt.Errorf("%w: %s leads out of %s", ErrUnreadable, name, fs.dir))
	}
	return err
}

// escapes reports whether err is the error root answers a name that leads
// out of it with. The os package does not export it; a root answers it,
// before any call to the system, for a name that starts at '/'.
func escapes(root *os.Root, err error) bool {
	var out *os.PathError
	_, outside := root.Stat("/")
	return errors.As(outside, &out) && errors.Is(err, out.Err)
}

func (fs *careOS) Open(name string) (billy.File, error) { return fs.OpenFile(name, os.O_RDONLY, 0) }

func (fs *careOS) Create(name string) (billy.File, error) {
	return nil, &os.PathError{Op: "create", Path: name, Err: errReadOnly}
}

func (fs *careOS) TempFile(dir, _ string) (billy.File, error) {
	return nil, &os.PathError{Op: "create", Path: dir, Err: errReadOnly}
}

// OpenFile opens name for reading only when the root's look at it finds a
// regular file, without blocking and without taking a terminal (openFlags),
// keeps it only when the open file is the file looked at, and reads it
// within the bound of the name asked for (bound), wherever a link leads.
func (fs *careOS) OpenFile(name string, flag int, _ os.FileMode) (billy.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		return nil, &os.PathError{Op: "open", Path: name, Err: errReadOnly}
	}
	rel, err := fs.rel(name)
	if err != nil {
		return nil, err
	}
	looked, err := fs.root.Stat(rel)
	if err != nil {
		return nil, fs.judge(rel, err)
	}
	if !looked.Mode().IsRegular() {
		return nil, fs.seen.note(fmt.Errorf("%w: %s is not a regular file", ErrUnreadable, name))
	}
	if afterLook != nil {
		afterLook(rel)
	}
	f, err := fs.root.OpenFile(rel, openFlags, 0)
	if err != nil {
		return nil, fs.judge(rel, err)
	}
	open, err := f.Stat()
	if err == nil && (!open.Mode().IsRegular() || !os.SameFile(looked, open)) {
		err = fs.seen.note(fmt.Errorf("%w: %s changed as it was opened", ErrUnreadable, name))
	}
	limit := bound(filepath.ToSlash(rel))
	if err == nil && limit >= 0 && open.Size() > limit {
		err = fs.seen.note(fmt.Errorf("%w: %s is longer than %d bytes", ErrUnreadable, name, limit))
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &careFile{File: f, left: limit, seen: fs.seen}, nil
}

// afterLook, set by a test, runs between OpenFile's look at a file and its
// open.
var afterLook func(rel string)

func (fs *careOS) Stat(name string) (os.FileInfo, error) {
	rel, err := fs.rel(name)
	if err != nil {
		return nil, err
	}
	fi, err := fs.root.Stat(rel)
	return fi, fs.judge(rel, err)
}

// ReadDir lists the folder name, opened as a file is (openFlags) once the
// root's look finds a folder.
func (fs *careOS) ReadDir(name string) ([]os.FileInfo, error) {
	fi, err := fs.Stat(name)
	if err == nil && !fi.IsDir() {
		err = &os.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	if err != nil {
		return nil, err
	}
	rel, _ := fs.rel(name)
	f, err := fs.root.OpenFile(rel, openFlags, 0)
	if err != nil {
		return nil, fs.judge(rel, err)
	}
	defer f.Close()
	list, err := f.Readdir(-1)
	slices.SortFunc(list, func(a, b os.FileInfo) int { return strings.Compare(a.Name(), b.Name()) })
	return list, err
}

func (fs *careOS) Join(elem ...string) string { return filepath.Join(elem...) }

func (fs *careOS) Rename(from, _ string) error {
	return &os.PathError{Op: "rename", Path: from, Err: errReadOnly}
}

func (fs *careOS) Remove(name string) error {
	return &os.PathError{Op: "remove", Path: name, Err: errReadOnly}
}

func (fs *careOS) MkdirAll(name string, _ os.FileMode) error {
	return &os.PathError{Op: "mkdir", Path: name, Err: errReadOnly}
}

func (fs *careOS) Symlink(_, link string) error {
	return &os.PathError{Op: "symlink", Path: link, Err: errReadOnly}
}

func (fs *careOS) Chmod(name string, _ os.FileMode) error {
	return &os.PathError{Op: "chmod", Path: name, Err: errReadOnly}
}

func (fs *careOS) RemoveAll(name string) error {
	return &os.PathError{Op: "removeall", Path: name, Err: errReadOnly}
}

// careFile is a file careOS opened: what is read of it, by Read and ReadAt
// together, stops at its bound with ErrUnreadable (left, -1 for none).
type careFile struct {
	*os.File
	left int64
	seen *refusal
}

func (f *careFile) Read(p []byte) (int, error) { return f.count(f.File.Read(f.take(p))) }

func (f *careFile) ReadAt(p []byte, off int64) (int, error) {
	return f.count(f.File.ReadAt(f.take(p), off))
}

// take is p cut to one byte past what is left: a read that fills it has
// found the file longer than its bound.
func (f *careFile) take(p []byte) []byte {
	if f.left < 0 || int64(len(p)) <= f.left {
		return p
	}
	return p[:f.left+1]
}

func (f *careFile) count(n int, err error) (int, error) {
	if f.left < 0 {
		return n, err
	}
	if int64(n) > f.left {
		n, f.left = int(f.left), 0
		return n, f.seen.note(fmt.Errorf("%w: %s grew past its bound as it was read", ErrUnreadable, f.Name()))
	}
	f.left -= int64(n)
	return n, err
}

func (f *careFile) Lock() error   { return errReadOnly }
func (f *careFile) Unlock() error { return errReadOnly }

// refusal keeps the first file an opening refused (ErrUnreadable): go-git
// goes on past some failed opens (a loose ref's, to packed-refs), so open
// and read answer with the refusal, not with what go-git made of it. It
// keeps the opening's roots too, closed once the read is over (close).
type refusal struct {
	mu    sync.Mutex
	err   error
	roots []*os.Root
}

func (r *refusal) keep(fs *careOS) *careOS {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fs.root != nil {
		r.roots = append(r.roots, fs.root)
	}
	return fs
}

func (r *refusal) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, root := range r.roots {
		root.Close()
	}
	r.roots = nil
}

func (r *refusal) note(err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
	return err
}

func (r *refusal) first() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// gitDirs finds dir's git directory and working tree as go-git v5.19.3's
// PlainOpenWithOptions does with DetectDotGit (its dotGitToOSFilesystems
// and dotGitFileToOSFilesystem), over careOS file systems: the folder, or
// the nearest one above it, with a .git, and the folder a .git file names
// (a linked worktree's); the root as the git directory, with no working
// tree, when no folder up to it has a .git.
func gitDirs(dir string, seen *refusal) (dot, wt billy.Filesystem, err error) {
	p, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	for {
		folder := p
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			folder = filepath.Dir(p)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
		fs := careFS(folder, seen)
		fi, err := fs.Stat(filepath.Join(fs.dir, git.GitDirName))
		if err == nil {
			if fi.IsDir() {
				return fs.under(git.GitDirName).billy(), fs.billy(), nil
			}
			dot, err := gitFile(p, fs.billy(), seen)
			return dot, fs.billy(), err
		}
		if !os.IsNotExist(err) {
			return nil, nil, err
		}
		if up := filepath.Dir(p); up != p {
			p = up
			continue
		}
		return fs.billy(), nil, nil
	}
}

// gitFile is the git directory a .git file names (a linked worktree's).
func gitFile(p string, fs billy.Filesystem, seen *refusal) (billy.Filesystem, error) {
	b, err := readAll(fs, git.GitDirName)
	if err != nil {
		return nil, err
	}
	const prefix = "gitdir: "
	line := string(b)
	if !strings.HasPrefix(line, prefix) {
		return nil, fmt.Errorf(".git file has no %s prefix", prefix)
	}
	gitdir := strings.TrimSpace(strings.Split(line[len(prefix):], "\n")[0])
	if filepath.IsAbs(gitdir) {
		return careFS(gitdir, seen).billy(), nil
	}
	return careFS(fs.Join(p, gitdir), seen).billy(), nil
}

// commonDir is the common directory dot's commondir names, nil for none
// (go-git's dotGitCommonDirectory, which EnableDotGitCommonDir asks for).
func commonDir(dot billy.Filesystem, seen *refusal) (billy.Filesystem, error) {
	b, err := readAll(dot, "commondir")
	if os.IsNotExist(err) || (err == nil && len(b) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := strings.TrimSpace(string(b))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dot.Root(), p)
	}
	common := careFS(p, seen).billy()
	if _, err := common.Stat(""); err != nil {
		if os.IsNotExist(err) {
			return nil, git.ErrRepositoryIncomplete
		}
		return nil, err
	}
	return common, nil
}

func readAll(fs billy.Filesystem, name string) ([]byte, error) {
	f, err := fs.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
