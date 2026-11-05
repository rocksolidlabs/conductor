package agents

// A user's home as Install writes to it: files only where their path really
// lies inside home, never through a link, 0600 in 0700 directories, each
// replaced whole; the home must be the user's (CheckHome). A dry run writes
// nothing and reports what a write would change, which is how Status tells
// whether Install would change anything.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// homeDir is a user's home directory as Install writes to it: a file goes only
// where its path really lies inside home, and never through a link. A dry run
// writes nothing and reports what a write would change: that is how Status
// tells whether Install would change anything.
type homeDir struct {
	dir    string // as given: the paths Install reports are under it
	real   string // with its links resolved, for the checks
	dryRun bool
}

func openHome(dir string, dryRun bool) (*homeDir, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("home %q is not an absolute path", dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if !dryRun {
		fi, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		if err := ownedBy(dir, fi, geteuid()); err != nil {
			return nil, err
		}
	}
	return &homeDir{dir: dir, real: real, dryRun: dryRun}, nil
}

// geteuid is the user Install writes as. Tests replace it.
var geteuid = os.Geteuid

// CheckHome refuses a home directory that belongs to another user than the
// one running conductor (under sudo, for example). What Install writes, 0600
// files in 0700 directories, belongs to the user writing it, so the home's
// owner could not use it: the install is for that user to run. Install
// checks it before it writes anything. It takes a system that says who owns
// a file, as Unix systems do; elsewhere it passes.
func CheckHome(home string) error {
	fi, err := os.Stat(home)
	if err != nil {
		return err
	}
	return ownedBy(home, fi, geteuid())
}

// ownedBy refuses home, which fi describes, unless the user uid owns it, or
// it is the process's own home (HOME) and the process may write it, as in a
// container whose home belongs to another uid. Root never passes the second
// way: under sudo, HOME may still name the invoking user's home, which root
// can always write.
func ownedBy(home string, fi fs.FileInfo, uid int) error {
	who, other := otherOwner(home, fi, uid)
	if !other {
		return nil
	}
	return fmt.Errorf("%s belongs to %s, and what Conductor wrote there would not: run it as %s, for example sudo -u %s conductor hooks install …", home, who, who, who)
}

// otherOwner names the owner of path, which fi describes, when ownedBy's rule
// refuses it for the user uid; other is false when the rule passes it,
// including where the system does not say who owns a file.
func otherOwner(path string, fi fs.FileInfo, uid int) (who string, other bool) {
	owner, ok := FileOwner(fi)
	if !ok || owner == uid {
		return "", false
	}
	if uid != 0 && ownHome(path) && writable(path) {
		return "", false
	}
	who = strconv.Itoa(owner)
	if u, err := user.LookupId(who); err == nil && u.Username != "" {
		who = u.Username
	}
	return who, true
}

// CheckOwner refuses path unless the user running conductor owns it, by the
// rule CheckHome holds a home to (ownedBy: its own-home exception passes the
// process's home directory itself, never a file in it); where the system does
// not say who owns a file, it passes. A missing path is its own error
// (fs.ErrNotExist). It follows a link: what it judges is the file the link
// leads to (CheckLinkOwner judges the link), and its refusal says where the
// link leads. conductor completion install
// checks the directory of the rc file it appends to, the rc file and, when
// that is a link, the link: under sudo, another user's ~/.zshrc is refused.
func CheckOwner(path string) error { return checkOwner(path, os.Stat) }

// CheckLinkOwner is CheckOwner on path itself: a symbolic link is not
// followed, and its own owner must pass. Under sudo, a link the user made
// from ~/.zshrc to a file of root's passes CheckOwner, which judges root's
// file, and is refused here.
func CheckLinkOwner(path string) error { return checkOwner(path, os.Lstat) }

// checkOwner applies ownedBy's rule to what stat says of path, and names
// path, and that it is a link when it is one, in the refusal. When stat
// followed a link to the file it judged, the refusal says where the link
// leads instead: the link may be the user's own, and running conductor as
// the file's owner is no advice for that.
func checkOwner(path string, stat func(string) (fs.FileInfo, error)) error {
	fi, err := stat(path)
	if err != nil {
		return err
	}
	who, other := otherOwner(path, fi, geteuid())
	if !other {
		return nil
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		if li, err := os.Lstat(path); err == nil && li.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s leads to a file of %s's, not of the user running conductor: nothing written", path, who)
		}
	}
	what := path
	if fi.Mode()&fs.ModeSymlink != 0 {
		what += ", a link,"
	}
	return fmt.Errorf("%s belongs to %s, not to the user running conductor: nothing written; run it as %s, for example sudo -u %s conductor completion install", what, who, who, who)
}

// SetEUIDForTest makes the owner checks of this package (CheckHome,
// CheckOwner, CheckLinkOwner, Install) take uid as the user running
// conductor, and returns what restores the real one. It is for the tests of
// the packages that call them (internal/cli), which cannot reach geteuid; it
// is not safe for concurrent use and is never called outside tests.
func SetEUIDForTest(uid int) (restore func()) {
	was := geteuid
	geteuid = func() int { return uid }
	return func() { geteuid = was }
}

// ownHome reports whether dir is the process's home directory, compared as a
// file.
func ownHome(dir string) bool {
	h, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	a, errA := os.Stat(h)
	b, errB := os.Stat(dir)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// path is the file rel under home, as Install reports it.
func (h *homeDir) path(rel string) string {
	return filepath.Join(h.dir, filepath.FromSlash(rel))
}

// resolve returns where the file rel really lies. The directories on the way
// may be links (dotfiles kept elsewhere in home), as long as they lead to a
// place inside home; the ones missing will be created there. The file itself
// is not resolved: read and write refuse it when it is a link.
func (h *homeDir) resolve(rel string) (string, error) {
	rel = filepath.FromSlash(rel)
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is not a path inside home", rel)
	}
	existing, missing := filepath.Dir(filepath.Join(h.real, rel)), ""
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		missing = filepath.Join(filepath.Base(existing), missing)
		existing = filepath.Dir(existing)
	}
	dir, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if r, err := filepath.Rel(h.real, dir); err != nil || !filepath.IsLocal(r) {
		return "", byHand("%s: a link on the way leads out of %s, and Conductor writes only inside it", h.path(rel), h.dir)
	}
	return filepath.Join(dir, missing, filepath.Base(rel)), nil
}

// read returns the content of the file rel, and false when there is none. A
// link is left to the user (ErrByHand).
func (h *homeDir) read(rel string) ([]byte, bool, error) {
	p, err := h.resolve(rel)
	if err != nil {
		return nil, false, err
	}
	fi, err := existing(p, h.path(rel))
	if err != nil || fi == nil {
		return nil, false, linkByHand(err)
	}
	b, err := os.ReadFile(p)
	return b, err == nil, err
}

// write makes the file rel hold data, and reports whether it had to change it;
// a dry run only reports it. A link is left to the user (ErrByHand).
func (h *homeDir) write(rel string, data []byte) (bool, error) {
	p, err := h.resolve(rel)
	if err != nil {
		return false, err
	}
	if h.dryRun {
		fi, err := existing(p, h.path(rel))
		if err != nil {
			return false, linkByHand(err)
		}
		return fi == nil || !holds(p, data), nil
	}
	changed, err := replaceFile(p, h.path(rel), data, 0)
	return changed, linkByHand(err)
}

// errLink is the refusal to write through a symbolic link.
var errLink = errors.New("is a symbolic link, and Conductor does not write through links")

// linkByHand marks a refused link as a step for the user.
func linkByHand(err error) error {
	if errors.Is(err, errLink) {
		return fmt.Errorf("%w: %w", ErrByHand, err)
	}
	return err
}

// existing returns the file at p, or nil when there is none; name is how
// errors call it. A link, or anything else that is not a regular file, is
// refused.
func existing(p, name string) (fs.FileInfo, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s %w", name, errLink)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return fi, nil
}

// holds reports whether the file at p holds exactly data.
func holds(p string, data []byte) bool {
	cur, err := os.ReadFile(p)
	return err == nil && bytes.Equal(cur, data)
}

// modeError is a mode replaceFile could not set: on a file whose content was
// right already, or on the temporary file it renamed into place all the same.
type modeError struct{ err error }

func (e *modeError) Error() string { return e.err.Error() }
func (e *modeError) Unwrap() error { return e.err }

// replaceFile makes the file at p hold data; name is how errors call it. When
// the content differs it writes a temporary file next to it and renames that
// over p, so a reader sees the old content or the new, never half of it. With
// mode zero a new file is 0600 and a file that was there keeps its mode;
// otherwise the file gets mode, even when its content was right. A mode it
// cannot set (with chmod) does not keep the content out: the file is written
// all the same, 0600 at most when it was new or changed, and the error is a
// *modeError naming it. Directories it makes are 0700. A link or anything
// else that is not a regular file is refused.
func replaceFile(p, name string, data []byte, mode fs.FileMode) (bool, error) {
	return replacing(p, name, data, mode, false)
}

// replaceExecutable is replaceFile for a file that is of no use without its
// mode, a program: a mode it cannot set keeps the new content out, so the
// file there, if any, stays as it was, and the error is a *modeError.
func replaceExecutable(p, name string, data []byte, mode fs.FileMode) (bool, error) {
	return replacing(p, name, data, mode, true)
}

// replacing is replaceFile, and replaceExecutable when strict.
func replacing(p, name string, data []byte, mode fs.FileMode, strict bool) (bool, error) {
	fi, err := existing(p, name)
	if err != nil {
		return false, err
	}
	if fi != nil && holds(p, data) {
		if mode != 0 && fi.Mode().Perm() != mode {
			if err := chmod(p, mode); err != nil {
				return false, &modeError{err}
			}
		}
		return false, nil
	}
	perm := mode
	if perm == 0 {
		perm = 0o600
		if fi != nil {
			perm = fi.Mode().Perm()
		}
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(p)+".conductor-*")
	if err != nil {
		return false, err
	}
	renamed := false
	defer func() {
		if !renamed {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	// CreateTemp makes the file 0600; a mode that cannot be set leaves it so.
	var modeErr error
	if err := chmod(f.Name(), perm); err != nil {
		modeErr = &modeError{&fs.PathError{Op: "chmod", Path: name, Err: pathErr(err)}}
		if strict {
			return false, modeErr
		}
	}
	if _, err := f.Write(data); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		return false, err
	}
	renamed = true
	return true, modeErr
}

// pathErr is the cause of err without the path it names, which is the
// temporary file's; replaceFile names the file it became instead.
func pathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// step changes one file under home and reports whether it did (or, in a dry
// run, would).
type step struct {
	rel string
	do  func(h *homeDir) (bool, error)
}

// run runs steps in order on home, for real or as a dry run, and returns the
// files they changed or would change. A step that leaves its file to the user
// (ErrByHand) does not stop the steps after it, and what each such step says
// comes back joined; any other error stops there.
func run(home string, dryRun bool, steps ...step) ([]string, error) {
	h, err := openHome(home, dryRun)
	if err != nil {
		return nil, err
	}
	var touched []string
	var manual []error
	for _, s := range steps {
		changed, err := s.do(h)
		if changed {
			touched = append(touched, h.path(s.rel))
		}
		if errors.Is(err, ErrByHand) {
			manual = append(manual, &stepError{s.rel, err})
			continue
		}
		if err != nil {
			return touched, err
		}
	}
	return touched, errors.Join(manual...)
}

// install is Install: it runs the steps on home and returns the files they
// changed.
func install(home string, steps ...step) ([]string, error) {
	return run(home, false, steps...)
}

// statusOf is Status: whether Install would change nothing under home and
// leave nothing to the user, so that an install that is partial or names an
// older binary reads as not installed, and where the install's main file,
// rel, is.
func statusOf(home, rel string, steps ...step) (bool, string) {
	touched, err := run(home, true, steps...)
	return err == nil && len(touched) == 0, filepath.Join(home, filepath.FromSlash(rel))
}
