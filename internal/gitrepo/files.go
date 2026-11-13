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

// ErrUnreadable says a file of the repository was not read; the error
// that wraps it says why, with the file's name in its folder: not a
// regular file, longer than the opener reads of a file of its kind
// (bound), behind a link the opener does not follow, a .git that is a
// link, or a folder the repository names that is not a folder.
var ErrUnreadable = errors.New("a file of the repository was not read")

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
	// maxConfig bounds config: a branch's section with its upstream is
	// about a hundred bytes, so a repository with a hundred thousand
	// branch sections is well under 16 MiB.
	maxConfig = 16 << 20
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
// the folder. The root follows a link written as a relative path that
// stays inside the folder, and the file it leads to is read with the bound
// of the name asked for; it refuses a link that leads out of the folder
// and a link written as an absolute path at any component of the name, a
// folder's as well as a file's, wherever it leads, which careOS notes as
// the opener's refusal (go-git reads past some failed opens: a loose
// ref's, to packed-refs). A .git that is a link is refused wherever it
// leads (under). go-billy's chroot over it (billy) only joins names: careOS
// has no Lstat or Readlink, so the chroot resolves no link and hands it
// each name unresolved.
type careOS struct {
	root *os.Root // nil when the folder did not open: err
	err  error
	dir  string
	what string   // the folder in a refusal's words
	seen *refusal // the opening's, shared by all its file systems
}

// careFS is a file system over the folder dir with its links resolved, as
// osfs.New(dir) is; under is one over its folder name (a git directory),
// opened beneath it. Each opens its folder as a folder only (dirOnly): a
// name there that is not a folder fails at once, and nothing it names is
// opened.
func careFS(dir, what string, seen *refusal) *careOS {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	root, err := os.OpenRoot(dirOnly(dir))
	return seen.keep(&careOS{root: root, err: err, dir: dir, what: what, seen: seen})
}

func (fs *careOS) under(name string) *careOS {
	if afterLook != nil {
		afterLook(name)
	}
	root, err := fs.root.OpenRoot(dirOnly(name))
	if looked, look := fs.root.Stat(name); err != nil && look == nil && !looked.IsDir() {
		err = fs.refuse("%s is %s", name, kind(looked.Mode()))
	}
	// A .git that is a link, looked at without following it, is refused
	// wherever it leads.
	if at, look := fs.root.Lstat(name); err == nil && look == nil && at.Mode()&os.ModeSymlink != 0 {
		root.Close()
		root, err = nil, fs.refuse("%s is a link", name)
	}
	return fs.seen.keep(&careOS{root: root, err: fs.judge(name, err), dir: filepath.Join(fs.dir, name), what: gitDir, seen: fs.seen})
}

// named is fs, the folder the file name of the repository names (a .git
// file, commondir): one that is not a folder is refused as what a look at
// it, without an open, finds.
func (fs *careOS) named(name string) *careOS {
	if looked, look := os.Stat(fs.dir); fs.err != nil && look == nil && !looked.IsDir() {
		fs.err = fs.refuse("%s %s names is %s", fs.what, name, kind(looked.Mode()))
	}
	return fs
}

// dirOnly is how a folder is named to os.Root to open it as a folder
// only: its path and "." (as internal/session's openDirOnly, which this
// package cannot import: session imports gitrepo), so a named pipe there
// fails at once instead of waiting for a writer.
func dirOnly(path string) string { return path + string(filepath.Separator) + "." }

// The folders a refusal names.
const (
	gitDir    = "the git directory"
	itsFolder = "its folder"
)

// refuse notes the opener's refusal of a file, its cause said after it.
func (fs *careOS) refuse(format string, args ...any) error {
	return fs.seen.note(fmt.Errorf("%w: "+format, append([]any{ErrUnreadable}, args...)...))
}

// kind is a file's kind as a refusal says it.
func kind(m os.FileMode) string {
	switch {
	case m&os.ModeNamedPipe != 0:
		return "a named pipe"
	case m.IsDir():
		return "a directory"
	case m&os.ModeDevice != 0:
		return "a device"
	case m&os.ModeSocket != 0:
		return "a socket"
	case m.IsRegular():
		return "a file, not a folder"
	}
	return "not a regular file"
}

// units is n bytes as a person reads them, in tenths rounded down: 64 KiB,
// 5.5 MiB, 12 bytes, 1 byte, and "just over 16 MiB" for 16 MiB and a byte.
func units(n int64) string {
	if n == 1 {
		return "1 byte"
	}
	for _, u := range []struct {
		size int64
		name string
	}{{1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		tenths := n/u.size*10 + n%u.size*10/u.size
		switch {
		case n < u.size:
			continue
		case n%u.size == 0:
			return fmt.Sprintf("%d %s", n/u.size, u.name)
		case tenths%10 == 0:
			return fmt.Sprintf("just over %d %s", n/u.size, u.name)
		}
		return fmt.Sprintf("%d.%d %s", tenths/10, tenths%10, u.name)
	}
	return fmt.Sprintf("%d bytes", n)
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
// when name is behind a link the root does not follow.
func (fs *careOS) judge(name string, err error) error {
	if err != nil && fs.root != nil && escapes(fs.root, err) {
		return fs.refuse("%s is behind a link that is not followed: it leads out of %s or is written as an absolute path", filepath.ToSlash(name), fs.what)
	}
	return err
}

// escapes reports whether err is the error root answers a name that leads
// out of it with, or a link written as an absolute path. The os package
// does not export it; a root answers it, before any call to the system,
// for a name that starts at '/'.
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
// within the bound of the name asked for (bound), wherever a relative link
// inside the folder leads.
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
	said := filepath.ToSlash(rel)
	if !looked.Mode().IsRegular() {
		return nil, fs.refuse("%s is %s", said, kind(looked.Mode()))
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
		err = fs.refuse("%s changed as it was opened", said)
	}
	limit := bound(said)
	if err == nil && limit >= 0 && open.Size() > limit {
		err = fs.refuse("%s is %s; at most %s of it is read", said, units(open.Size()), units(limit))
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &careFile{file: f, name: said, left: limit, seen: fs.seen}, nil
}

// afterLook, set by a test, runs between OpenFile's look at a file and its
// open, and between gitDirs' look at .git and under's open of it.
var afterLook func(rel string)

func (fs *careOS) Stat(name string) (os.FileInfo, error) {
	rel, err := fs.rel(name)
	if err != nil {
		return nil, err
	}
	fi, err := fs.root.Stat(rel)
	return fi, fs.judge(rel, err)
}

// ReadDir lists the folder name, opened as a file is (openFlags) and as a
// folder only (dirOnly) once the root's look finds a folder.
func (fs *careOS) ReadDir(name string) ([]os.FileInfo, error) {
	fi, err := fs.Stat(name)
	if err == nil && !fi.IsDir() {
		err = &os.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	if err != nil {
		return nil, err
	}
	rel, _ := fs.rel(name)
	f, err := fs.root.OpenFile(dirOnly(rel), openFlags, 0)
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

// careFile is a file careOS opened, with billy.File's methods and no
// other: Write and Truncate are refused (errReadOnly), and what is read of
// it, by Read and ReadAt together, stops at its bound with ErrUnreadable
// (left, -1 for none).
type careFile struct {
	file *os.File
	name string // in its folder, as a refusal says it
	left int64
	seen *refusal
}

func (f *careFile) Name() string                              { return f.file.Name() }
func (f *careFile) Seek(off int64, whence int) (int64, error) { return f.file.Seek(off, whence) }
func (f *careFile) Close() error                              { return f.file.Close() }

func (f *careFile) Write([]byte) (int, error) {
	return 0, &os.PathError{Op: "write", Path: f.name, Err: errReadOnly}
}

func (f *careFile) Truncate(int64) error {
	return &os.PathError{Op: "truncate", Path: f.name, Err: errReadOnly}
}

func (f *careFile) Read(p []byte) (int, error) { return f.count(f.file.Read(f.take(p))) }

func (f *careFile) ReadAt(p []byte, off int64) (int, error) {
	return f.count(f.file.ReadAt(f.take(p), off))
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
		return n, f.seen.note(fmt.Errorf("%w: %s grew past its bound as it was read", ErrUnreadable, f.name))
	}
	f.left -= int64(n)
	return n, err
}

func (f *careFile) Lock() error   { return errReadOnly }
func (f *careFile) Unlock() error { return errReadOnly }

// refusal keeps the first file an opening refused (ErrUnreadable): go-git
// goes on past some failed opens (a loose ref's, to packed-refs), so open
// and read answer with the refusal, not with what go-git made of it. It
// keeps the opening's roots too, closed once the read is over (close); a
// folder the discovery found no .git in is closed as it moves up (drop).
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
	r.told()
	return fs
}

// drop closes and forgets fs's root: a folder the discovery looked for
// .git in and found none.
func (r *refusal) drop(fs *careOS) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i := slices.Index(r.roots, fs.root); i >= 0 {
		r.roots = slices.Delete(r.roots, i, i+1)
		fs.root.Close()
	}
	r.told()
}

func (r *refusal) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, root := range r.roots {
		root.Close()
	}
	r.roots = nil
	r.told()
}

// rootsOpen, set by a test, is told how many roots an opening holds open
// each time the number changes (keep, drop, close).
var rootsOpen func(n int)

func (r *refusal) told() {
	if rootsOpen != nil {
		rootsOpen(len(r.roots))
	}
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
		fs := careFS(folder, itsFolder, seen)
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
			seen.drop(fs)
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
	if !filepath.IsAbs(gitdir) {
		gitdir = fs.Join(p, gitdir)
	}
	return careFS(gitdir, gitDir, seen).named(git.GitDirName).billy(), nil
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
	common := careFS(p, gitDir, seen).named("commondir").billy()
	if _, err := common.Stat(""); err != nil {
		if os.IsNotExist(err) {
			return nil, git.ErrRepositoryIncomplete
		}
		return nil, err
	}
	return common, nil
}

func readAll(fs billy.Filesystem, name string) ([]byte, error) {
	f, err := fs.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
