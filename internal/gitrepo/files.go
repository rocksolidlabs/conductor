package gitrepo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/helper/chroot"
	"github.com/go-git/go-billy/v5/osfs"
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
// (open): the operating system's as osfs gives it, under go-billy's chroot
// as osfs.New puts it, so that paths and links resolve as they always
// have. Its OpenFile is the one way a file is opened: without blocking and
// without taking a terminal, used only when the open file is a regular
// file (openRegular), and read within the bound of its kind (bound).
type careOS struct {
	osBase
	root string   // the folder the chroot over it starts at
	seen *refusal // the opening's, shared by all its file systems
}

// osBase is what go-billy's chroot uses of the operating system's file
// system.
type osBase interface {
	billy.Basic
	billy.TempFile
	billy.Dir
	billy.Symlink
	billy.Chmod
	billy.Capable
}

// careFS is a file system over the folder dir with its links resolved, as
// osfs.New(dir) is; careUnder one over dir as it is, as a chroot's Chroot
// is.
func careFS(dir string, seen *refusal) billy.Filesystem {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return careUnder(dir, seen)
}

func careUnder(dir string, seen *refusal) billy.Filesystem {
	return chroot.New(&careOS{osBase: osfs.Default, root: dir, seen: seen}, dir)
}

func (fs *careOS) Open(name string) (billy.File, error) { return fs.OpenFile(name, os.O_RDONLY, 0) }

func (fs *careOS) Create(name string) (billy.File, error) {
	return nil, &os.PathError{Op: "create", Path: name, Err: errReadOnly}
}

func (fs *careOS) TempFile(dir, _ string) (billy.File, error) {
	return nil, &os.PathError{Op: "create", Path: dir, Err: errReadOnly}
}

func (fs *careOS) OpenFile(name string, flag int, _ os.FileMode) (billy.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		return nil, &os.PathError{Op: "open", Path: name, Err: errReadOnly}
	}
	f, size, err := openRegular(name)
	if errors.Is(err, ErrUnreadable) {
		fs.seen.note(err)
	}
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(fs.root, name)
	if err != nil {
		rel = "" // bounded as a small file
	}
	limit := bound(filepath.ToSlash(rel))
	if limit >= 0 && size > limit {
		f.Close()
		return nil, fs.seen.note(fmt.Errorf("%w: %s is longer than %d bytes", ErrUnreadable, name, limit))
	}
	return &careFile{File: f, left: limit, seen: fs.seen}, nil
}

// Readlink is the link's target. go-billy's chroot follows a link only
// within its folder and refuses one that leads out of it before anything
// is opened; Readlink notes that refusal as the opener's, because go-git
// reads past some (a loose ref's, to packed-refs).
func (fs *careOS) Readlink(link string) (string, error) {
	target, err := fs.osBase.Readlink(link)
	if err == nil {
		to := target
		if !filepath.IsAbs(to) {
			to = filepath.Join(filepath.Dir(link), to)
		}
		if rel, err := filepath.Rel(fs.root, to); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			fs.seen.note(fmt.Errorf("%w: %s leads out of %s", ErrUnreadable, link, fs.root))
		}
	}
	return target, err
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
// and read answer with the refusal, not with what go-git made of it.
type refusal struct {
	mu  sync.Mutex
	err error
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
		fs := careFS(p, seen)
		if info, err := fs.Stat("/"); !os.IsNotExist(err) {
			if info == nil {
				return nil, nil, err
			}
			if !info.IsDir() {
				fs = careFS(filepath.Dir(p), seen)
			}
		}
		fi, err := fs.Stat(git.GitDirName)
		if err == nil {
			if fi.IsDir() {
				return careUnder(fs.Join(fs.Root(), git.GitDirName), seen), fs, nil
			}
			dot, err := gitFile(p, fs, seen)
			return dot, fs, err
		}
		if !os.IsNotExist(err) {
			return nil, nil, err
		}
		if up := filepath.Dir(p); up != p {
			p = up
			continue
		}
		return fs, nil, nil
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
		return careFS(gitdir, seen), nil
	}
	return careFS(fs.Join(p, gitdir), seen), nil
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
	common := careFS(p, seen)
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
