package gitcli

import (
	"errors"
	"os"
	"runtime"
	"strconv"
)

// A launch's revision is read in the folder the launch opened, and its end
// commit is kept only when the repository found there is still the
// launch's: what the server reads for its own sessions (internal/api's
// history_journal.go) and conductor host for its own (internal/hostagent's
// report.go), never by the text of a path.

// OpenedPath is a path that names the directory dir holds open, for a git
// read in it: its descriptor's (/proc/<pid>/fd/<n>, where the system has
// one, as the process's own start uses, internal/pty), which nothing done
// to cwd moves; else cwd, which still says whether cwd named it as the
// read ended. It never names another directory than dir's: none (still
// false) when dir cannot be read.
func OpenedPath(dir *os.File, cwd string) (string, func() bool) {
	if dir == nil {
		return cwd, func() bool { return false }
	}
	want, err := dir.Stat()
	if err != nil {
		return cwd, func() bool { return false }
	}
	byHandle := "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/" + strconv.FormatUint(uint64(dir.Fd()), 10)
	if fi, err := os.Stat(byHandle); err == nil && os.SameFile(fi, want) {
		return byHandle, func() bool { return true }
	}
	return cwd, func() bool {
		fi, err := os.Stat(cwd)
		return err == nil && os.SameFile(fi, want)
	}
}

// LaunchRepo is a launch's repository as the file system knows it: the
// identity of its common git directory, and, where the system gives a
// removed directory's identity to the next one made (Linux reuses an
// inode's number once it is freed), that directory held open from the
// launch until the end commit is read, so its identity cannot be given to
// a repository made again at its path. Windows needs no hold: a file's id
// there carries a sequence number that its reuse changes.
type LaunchRepo struct {
	id   os.FileInfo
	held *os.File
}

// OpenLaunchRepo is the repository whose common git directory is common,
// nil when it cannot be named.
func OpenLaunchRepo(common string) *LaunchRepo {
	if runtime.GOOS == "windows" {
		fi, err := os.Stat(common)
		if err != nil {
			return nil
		}
		return &LaunchRepo{id: fi}
	}
	f, err := os.Open(common)
	if err != nil {
		return nil
	}
	fi, err := f.Stat()
	if err != nil || !fi.IsDir() {
		f.Close()
		return nil
	}
	return &LaunchRepo{id: fi, held: f}
}

// Is says common, a common git directory a later read found, is the
// launch's own: the same directory on the file system, whatever its path.
// False for nil.
func (r *LaunchRepo) Is(common string) bool {
	if r == nil || common == "" {
		return false
	}
	fi, err := os.Stat(common)
	return err == nil && os.SameFile(fi, r.id)
}

// Close lets the held directory go; nothing for nil.
func (r *LaunchRepo) Close() {
	if r != nil && r.held != nil {
		r.held.Close()
	}
}

// Holding says the directory is still held open (tests).
func (r *LaunchRepo) Holding() bool {
	if r == nil || r.held == nil {
		return false
	}
	_, err := r.held.Stat()
	return !errors.Is(err, os.ErrClosed)
}
