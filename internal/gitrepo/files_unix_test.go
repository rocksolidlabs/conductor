//go:build unix

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// hangWait bounds a call the test expects back at once. Nothing in the
// package caps an open in time, so it only bounds a hang (a named pipe
// opened blocking waits for a writer for good), far above what opening a
// repository takes on a loaded machine.
const hangWait = time.Minute

// within is f's error, f run in a goroutine of its own with a context
// within cancels: a call still running after hangWait has its context
// cancelled and its open of pipe let go (a writer opening and closing it
// ends the open it waits in) for one more hangWait, and fails the test in
// words that name what was read (what), back by then or not.
func within(t *testing.T, pipe, what string, f func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(hangWait):
	}
	cancel()
	letGo := time.After(hangWait)
	for {
		if fd, err := syscall.Open(pipe, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			syscall.Close(fd)
		}
		select {
		case err := <-done:
			t.Fatalf("%s: not back after %v; back once let go, with %v", what, hangWait, err)
		case <-letGo:
			t.Fatalf("%s: not back after %v, nor %v after it was let go", what, hangWait, hangWait)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// linkedRepo is a repository with two commits, the second on the branch of
// a linked worktree (wt), every ref packed, under the test's own folders;
// files names where each of the small files the opener reads is.
type linkedRepo struct {
	wt, c1, c2 string
	files      map[string]string
}

func linked(t *testing.T) linkedRepo {
	t.Helper()
	dir := scratch(t)
	write(t, dir, "a.txt", "one\n")
	c1 := commit(t, dir, time.Time{}, "one")
	wt := filepath.Join(t.TempDir(), "member")
	gitIn(t, dir, time.Time{}, "worktree", "add", "-q", "-b", "crew/core", wt, "main")
	write(t, wt, "a.txt", "two\n")
	c2 := commit(t, wt, time.Time{}, "two")
	gitIn(t, dir, time.Time{}, "pack-refs", "--all")
	admin := filepath.Join(dir, ".git", "worktrees", "member")
	return linkedRepo{wt: wt, c1: c1, c2: c2, files: map[string]string{
		".git":        filepath.Join(wt, ".git"),
		"commondir":   filepath.Join(admin, "commondir"),
		"HEAD":        filepath.Join(admin, "HEAD"),
		"config":      filepath.Join(dir, ".git", "config"),
		"packed-refs": filepath.Join(dir, ".git", "packed-refs"),
	}}
}

// ordinary is a repository with no linked worktree: two commits, the
// second on crew/core, as linked's reads expect, under the test's own
// folder.
func ordinary(t *testing.T) linkedRepo {
	t.Helper()
	dir := scratch(t)
	write(t, dir, "a.txt", "one\n")
	c1 := commit(t, dir, time.Time{}, "one")
	gitIn(t, dir, time.Time{}, "checkout", "-q", "-b", "crew/core")
	write(t, dir, "a.txt", "two\n")
	c2 := commit(t, dir, time.Time{}, "two")
	return linkedRepo{wt: dir, c1: c1, c2: c2}
}

// readers are the package's four reads of a repository.
var readers = []struct {
	name string
	read func(ctx context.Context, r linkedRepo) error
}{
	{"Commits", func(ctx context.Context, r linkedRepo) error {
		log, err := Commits(ctx, r.wt, "", time.Time{})
		if err == nil && (len(log.Commits) != 2 || log.Branch != "crew/core") {
			return errors.New("an unexpected log")
		}
		return err
	}},
	{"CommitDetail", func(ctx context.Context, r linkedRepo) error {
		d, err := CommitDetail(ctx, r.wt, "HEAD")
		if err == nil && d.Commit.Sha != r.c2 {
			return errors.New("an unexpected commit")
		}
		return err
	}},
	{"Compare", func(ctx context.Context, r linkedRepo) error {
		d, err := Compare(ctx, r.wt, r.c1, r.c2)
		if err == nil && len(d.Changes) != 1 {
			return errors.New("an unexpected comparison")
		}
		return err
	}},
	{"FileAt", func(ctx context.Context, r linkedRepo) error {
		f, err := FileAt(ctx, r.wt, r.c2, "a.txt", 100)
		if err == nil && string(f.Content) != "two\n" {
			return errors.New("an unexpected file")
		}
		return err
	}},
}

// Each small file of the repository the opener reads, made in turn a
// named pipe with no writer, a link out of its folder, a directory and a
// regular file past its bound: each of the package's four reads that opens
// it returns at once with the package's refusal. Compare and FileAt read
// commits by id and open no ref through the package, so packed-refs is
// Commits' and CommitDetail's here (FileAt's git binary opens it itself:
// TestFileAtsGitIsEndedAtItsCap).
func TestARepositorysOwnFilesAreRefusedUnlessRegularWithinTheirBound(t *testing.T) {
	limits := map[string]int64{".git": maxSmall, "commondir": maxSmall, "HEAD": maxSmall, "config": maxConfig, "packed-refs": maxPackedRefs}
	kinds := []struct {
		name string
		make func(path string) error
	}{
		{"a named pipe with no writer", func(path string) error { return syscall.Mkfifo(path, 0o600) }},
		{"a link out of its folder", nil},
		{"a directory", func(path string) error { return os.Mkdir(path, 0o755) }},
		{"a regular file past its bound", nil},
	}
	for name, limit := range limits {
		for _, kind := range kinds {
			t.Run(name+" is "+kind.name, func(t *testing.T) {
				t.Parallel()
				r := linked(t)
				ctx := context.Background()
				for _, rd := range readers {
					if err := rd.read(ctx, r); err != nil {
						t.Fatalf("%s before the change: %v", rd.name, err)
					}
				}
				path := r.files[name]
				if kind.name == "a link out of its folder" {
					// To a copy of the file as git wrote it, a regular
					// file within its bound, in a folder of the test's
					// own outside the repository.
					copied := filepath.Join(t.TempDir(), name)
					padded(t, path, copied, "", 0, false)
					linkTo(t, copied, path)
				} else if kind.make == nil {
					// The file as git wrote it, then zeros past the bound
					// (sparse: nothing written to the disk for them).
					if err := os.Truncate(path, limit+1); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Remove(path); err != nil {
					t.Fatal(err)
				} else if err := kind.make(path); err != nil {
					t.Fatal(err)
				}
				want := error(ErrUnreadable)
				if kind.name == "a directory" && (name == ".git" || name == "HEAD") {
					// An empty .git directory is a git directory with no
					// HEAD, and go-git looks at HEAD with a stat and
					// declines a directory before any open: no
					// repository, as always.
					want = ErrNotRepo
				}
				for _, rd := range readers {
					if name == "packed-refs" && (rd.name == "Compare" || rd.name == "FileAt") {
						continue
					}
					read := func(ctx context.Context) error { return rd.read(ctx, r) }
					if err := within(t, path, name+" is "+kind.name+", "+rd.name, read); !errors.Is(err, want) {
						t.Errorf("%s: %v, want %v", rd.name, err, want)
					}
				}
			})
		}
	}
}

// FileAt's git binary opens the repository's files itself and is ended at
// its cap whatever context FileAt is given: with packed-refs a named pipe
// with no writer (git opens it to see whether the id is also a ref's
// name), FileAt returns the cap's error and no git is left holding the
// pipe; Commits and CommitDetail refuse the pipe at once, and Compare,
// which opens no ref, reads. A repository as git wrote it is read with
// the product's cap.
func TestFileAtsGitIsEndedAtItsCap(t *testing.T) {
	r := linked(t)
	pipe := r.files["packed-refs"]
	if err := os.Remove(pipe); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	gitCap = time.Second
	defer func() { gitCap = maxGitRead }()
	want := map[string]error{"FileAt": ErrGitCap, "Commits": ErrUnreadable, "CommitDetail": ErrUnreadable, "Compare": nil}
	for _, rd := range readers {
		err := within(t, pipe, "packed-refs is a named pipe with no writer, "+rd.name, func(ctx context.Context) error { return rd.read(ctx, r) })
		if !errors.Is(err, want[rd.name]) || (err == nil) != (want[rd.name] == nil) {
			t.Errorf("%s: %v, want %v", rd.name, err, want[rd.name])
		}
		if rd.name != "FileAt" {
			continue
		}
		// FileAt returns once gitcli has waited for its git: a process
		// with the pipe open for reading, or waiting in its open, would
		// let a writer open it.
		if fd, err := syscall.Open(pipe, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			syscall.Close(fd)
			t.Errorf("a process still has packed-refs open for reading after FileAt returned")
		} else if err != syscall.ENXIO {
			t.Errorf("open packed-refs for writing: %v, want ENXIO (no reader)", err)
		}
	}
	gitCap = maxGitRead
	ordinary := linked(t)
	if err := readers[len(readers)-1].read(context.Background(), ordinary); err != nil {
		t.Errorf("FileAt with the product's cap: %v", err)
	}
}

// A refusal says its cause in words a person can act on: the file's name
// in its folder, its kind, its size and its bound in a person's units, or
// the link it is behind. A link written as an absolute path is refused
// wherever it leads, inside the git directory too, a folder's on the way
// (refs, read through for the branch's loose ref) as well as a file's.
func TestARefusalSaysItsCause(t *testing.T) {
	const prefix = "a file of the repository was not read: "
	const notFollowed = " is behind a link that is not followed: it leads out of the git directory or is written as an absolute path"
	// inside makes the file at path an absolute link to a copy of it beside
	// it, in the same git directory.
	inside := func(t *testing.T, path string) {
		padded(t, path, path+"-data", "", 0, false)
		linkTo(t, path+"-data", path)
	}
	// folderInside moves the folder at path beside it, in the same git
	// directory, and puts an absolute link to it in its place.
	folderInside := func(t *testing.T, path string) {
		if err := os.Rename(path, path+"-data"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path+"-data", path); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, want string
		change     func(t *testing.T, r linkedRepo)
	}{
		{"config", "config is 20 MiB; at most 16 MiB of it is read", func(t *testing.T, r linkedRepo) {
			if err := os.Truncate(r.files["config"], maxConfig+maxConfig/4); err != nil {
				t.Fatal(err)
			}
		}},
		{"config one byte past its bound", "config is just over 16 MiB; at most 16 MiB of it is read", func(t *testing.T, r linkedRepo) {
			if err := os.Truncate(r.files["config"], maxConfig+1); err != nil {
				t.Fatal(err)
			}
		}},
		{"config an absolute link inside the git directory", "config" + notFollowed, func(t *testing.T, r linkedRepo) { inside(t, r.files["config"]) }},
		{"HEAD an absolute link inside the git directory", "HEAD" + notFollowed, func(t *testing.T, r linkedRepo) { inside(t, r.files["HEAD"]) }},
		{"packed-refs an absolute link inside the git directory", "packed-refs" + notFollowed, func(t *testing.T, r linkedRepo) { inside(t, r.files["packed-refs"]) }},
		{"refs an absolute link to a folder inside the git directory", "refs/heads/crew/core" + notFollowed, func(t *testing.T, r linkedRepo) {
			folderInside(t, filepath.Join(filepath.Dir(r.files["config"]), "refs"))
		}},
		{"packed-refs", "packed-refs is a named pipe", func(t *testing.T, r linkedRepo) {
			if err := os.Remove(r.files["packed-refs"]); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(r.files["packed-refs"], 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"HEAD", "HEAD" + notFollowed, func(t *testing.T, r linkedRepo) {
			copied := filepath.Join(t.TempDir(), "HEAD")
			padded(t, r.files["HEAD"], copied, "", 0, false)
			linkTo(t, copied, r.files["HEAD"])
		}},
		{".git", ".git is behind a link that is not followed: it leads out of its folder or is written as an absolute path", func(t *testing.T, r linkedRepo) {
			copied := filepath.Join(t.TempDir(), ".git")
			padded(t, r.files[".git"], copied, "", 0, false)
			linkTo(t, copied, r.files[".git"])
		}},
		{"commondir", "commondir is a directory", func(t *testing.T, r linkedRepo) {
			if err := os.Remove(r.files["commondir"]); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(r.files["commondir"], 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"packed-refs a socket", "packed-refs is a socket", func(t *testing.T, r linkedRepo) {
			if err := os.Remove(r.files["packed-refs"]); err != nil {
				t.Fatal(err)
			}
			socket(t, r.files["packed-refs"])
		}},
		{".git names a regular file", "the git directory .git names is a file, not a folder", func(t *testing.T, r linkedRepo) {
			file := filepath.Join(t.TempDir(), "git")
			write(t, filepath.Dir(file), "git", "a regular file\n")
			write(t, r.wt, ".git", "gitdir: "+file+"\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := linked(t)
			c.change(t, r)
			_, err := Commits(context.Background(), r.wt, "", time.Time{})
			if !errors.Is(err, ErrUnreadable) || err.Error() != prefix+c.want {
				t.Errorf("Commits: %v, want %q", err, prefix+c.want)
			}
		})
	}
	for n, want := range map[int64]string{5<<20 + 1<<19: "5.5 MiB", maxConfig + 1: "just over 16 MiB", maxSmall + 1: "just over 64 KiB", 1<<20 - 1: "1023.9 KiB", 12: "12 bytes",
		maxConfig: "16 MiB", maxSmall: "64 KiB", 1 << 20: "1 MiB", 1: "1 byte", 0: "0 bytes"} {
		if got := units(n); got != want {
			t.Errorf("units(%d) = %q, want %q", n, got, want)
		}
	}
}

// fifo makes a named pipe at path, under the test's own folder, and is
// path.
func fifo(t *testing.T, path string) string {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Each folder the package opens beneath a name of the repository is opened
// as a folder only: a linked worktree's git directory (its .git file names
// it), the common directory (commondir names it, by an absolute or a
// relative path, in a linked worktree or an ordinary repository) and .git
// beneath its folder. A named pipe there, or .git made one between the
// look at it and its open (afterLook), is refused at once by each of the
// four reads, in words that say what it is.
func TestAFolderTheRepositoryNamesIsOpenedAsAFolder(t *testing.T) {
	const prefix = "a file of the repository was not read: "
	for _, c := range []struct {
		name, want string
		seam       bool
		change     func(t *testing.T, r *linkedRepo) (pipe string, before func())
	}{
		{name: "a linked worktree's .git file names a named pipe", want: "the git directory .git names is a named pipe",
			change: func(t *testing.T, r *linkedRepo) (string, func()) {
				pipe := fifo(t, filepath.Join(t.TempDir(), "git"))
				write(t, r.wt, ".git", "gitdir: "+pipe+"\n")
				return pipe, nil
			}},
		{name: "commondir names a named pipe by an absolute path", want: "the git directory commondir names is a named pipe",
			change: func(t *testing.T, r *linkedRepo) (string, func()) {
				pipe := fifo(t, filepath.Join(t.TempDir(), "common"))
				write(t, filepath.Dir(r.files["commondir"]), "commondir", pipe+"\n")
				return pipe, nil
			}},
		{name: "commondir names a named pipe by a relative path", want: "the git directory commondir names is a named pipe",
			change: func(t *testing.T, r *linkedRepo) (string, func()) {
				pipe := fifo(t, filepath.Join(filepath.Dir(r.files["commondir"]), "common"))
				write(t, filepath.Dir(r.files["commondir"]), "commondir", "common\n")
				return pipe, nil
			}},
		{name: "an ordinary repository's commondir names a named pipe beside it", want: "the git directory commondir names is a named pipe",
			change: func(t *testing.T, r *linkedRepo) (string, func()) {
				dot := filepath.Dir(r.files["config"])
				r.wt = filepath.Dir(dot)
				pipe := fifo(t, filepath.Join(dot, "common"))
				write(t, dot, "commondir", "common\n")
				return pipe, nil
			}},
		{name: ".git made a named pipe between the look and the open", want: ".git is a named pipe", seam: true,
			change: func(t *testing.T, r *linkedRepo) (string, func()) {
				dot := filepath.Dir(r.files["config"])
				r.wt = filepath.Dir(dot)
				aside := dot + "-aside"
				afterLook = func(rel string) {
					if rel != ".git" {
						return
					}
					if err := os.Rename(dot, aside); err != nil {
						t.Error(err)
					} else if err := syscall.Mkfifo(dot, 0o600); err != nil {
						t.Error(err)
					}
				}
				t.Cleanup(func() { afterLook = nil })
				// Each read meets .git a folder at the look.
				return dot, func() {
					if _, err := os.Stat(aside); err != nil {
						return
					}
					if err := os.Remove(dot); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(aside, dot); err != nil {
						t.Fatal(err)
					}
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !c.seam {
				t.Parallel()
			}
			r := linked(t)
			pipe, before := c.change(t, &r)
			for _, rd := range readers {
				if before != nil {
					before()
				}
				err := within(t, pipe, c.name+", "+rd.name, func(ctx context.Context) error { return rd.read(ctx, r) })
				if !errors.Is(err, ErrUnreadable) || err.Error() != prefix+c.want {
					t.Errorf("%s: %v, want %q", rd.name, err, prefix+c.want)
				}
			}
		})
	}
}

// A named pipe put in place of a file between the root's look at it and
// its open (afterLook) is opened without blocking and refused at once as a
// file that changed as it was opened.
func TestAPipePutInPlaceOfAFileAsItIsOpenedIsRefusedAtOnce(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "HEAD", "ref: refs/heads/main\n")
	head := filepath.Join(dir, "HEAD")
	afterLook = func(string) {
		if err := os.Remove(head); err != nil {
			t.Error(err)
		} else if err := syscall.Mkfifo(head, 0o600); err != nil {
			t.Error(err)
		}
	}
	defer func() { afterLook = nil }()
	seen := &refusal{}
	defer seen.close()
	fs := careFS(dir, gitDir, seen).billy()
	err := within(t, head, "HEAD made a named pipe as it is opened", func(context.Context) error {
		f, err := fs.Open("HEAD")
		if err == nil {
			f.Close()
		}
		return err
	})
	if !errors.Is(err, ErrUnreadable) || !strings.HasSuffix(err.Error(), ": HEAD changed as it was opened") {
		t.Errorf("open: %v, want HEAD refused as changed as it was opened", err)
	}
}

// FileAt with config made a named pipe once the repository is open (at the
// first look at an object, afterLook; the opening reads config before
// that, go-git's reads of the commit after it): its git binary, which opens config
// itself, is ended at the cap, shortened here, and FileAt returns the
// cap's error after one opening. With the cap as the product has it and
// the caller's context ended at the same point, FileAt returns the
// caller's own error, never the cap's, after one opening too.
func TestFileAtAnswersItsCapOrTheCallersOwnError(t *testing.T) {
	for _, c := range []struct {
		name   string
		cap    time.Duration
		cancel bool
		want   error
	}{
		{"the cap ends the git binary", time.Second, false, ErrGitCap},
		{"the caller's context ends first", maxGitRead, true, context.Canceled},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := linked(t)
			config := r.files["config"]
			openings, made := 0, false
			var cancel context.CancelFunc
			afterLook = func(rel string) {
				switch {
				case rel == "commondir":
					openings++
				case strings.HasPrefix(rel, "objects/") && !made:
					made = true
					if err := os.Remove(config); err != nil {
						t.Error(err)
					} else if err := syscall.Mkfifo(config, 0o600); err != nil {
						t.Error(err)
					}
					if c.cancel {
						cancel()
					}
				}
			}
			defer func() { afterLook = nil }()
			gitCap = c.cap
			defer func() { gitCap = maxGitRead }()
			err := within(t, config, "config made a named pipe once open, FileAt", func(ctx context.Context) error {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				_, err := FileAt(ctx, r.wt, r.c2, "a.txt", 100)
				return err
			})
			if c.want == ErrGitCap && err != nil && err.Error() != "the git binary reading the file was ended at its cap of 1s" {
				t.Errorf("FileAt: %q, want the cap's words with the shortened cap", err)
			}
			if !errors.Is(err, c.want) || (c.want != ErrGitCap && errors.Is(err, ErrGitCap)) || !made || openings != 1 {
				t.Errorf("FileAt: %v after %d openings (config made a pipe: %v), want %v after one", err, openings, made, c.want)
			}
		})
	}
}

// padded writes to path what was at from, then lines of line's shape (%d
// a running number) until it is at least size bytes long; cut cuts it to
// size exactly.
func padded(t *testing.T, from, path, line string, size int64, cut bool) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; int64(len(b)) < size; i++ {
		b = fmt.Appendf(b, line, i)
	}
	if cut {
		b = b[:size]
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// linkTo replaces the file at path with a link to target.
func linkTo(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// A link inside the git directory, written as a relative path, is
// followed, and what is read of it is bounded by the name asked for, never
// by the name it leads to: config, a link to a file under objects one byte
// past config's bound, is refused; HEAD, a link to a file named
// packed-refs beside it of a mebibyte, is refused with HEAD's bound;
// packed-refs, a link to a file of another name of a mebibyte (past the
// bound of a file of no named kind), is read.
func TestALinkIsReadWithTheBoundOfTheNameAskedFor(t *testing.T) {
	t.Run("HEAD to a larger file of another name", func(t *testing.T) {
		t.Parallel()
		r := linked(t)
		head := r.files["HEAD"]
		padded(t, head, filepath.Join(filepath.Dir(head), "packed-refs"), "# %08d\n", 1<<20, true)
		linkTo(t, "packed-refs", head)
		const want = "a file of the repository was not read: HEAD is 1 MiB; at most 64 KiB of it is read"
		for _, rd := range readers {
			if err := rd.read(context.Background(), r); !errors.Is(err, ErrUnreadable) || err.Error() != want {
				t.Errorf("%s: %v, want %q", rd.name, err, want)
			}
		}
	})
	t.Run("config to a file under objects past config's bound", func(t *testing.T) {
		t.Parallel()
		r := linked(t)
		config := r.files["config"]
		padded(t, config, filepath.Join(filepath.Dir(config), "objects", "config-data"), "# a comment of the configuration, line %08d\n", maxConfig+1, true)
		linkTo(t, filepath.Join("objects", "config-data"), config)
		for _, rd := range readers {
			if err := rd.read(context.Background(), r); !errors.Is(err, ErrUnreadable) {
				t.Errorf("%s: %v, want the refusal", rd.name, err)
			}
		}
	})
	t.Run("packed-refs to a file of another name of a mebibyte", func(t *testing.T) {
		t.Parallel()
		r := linked(t)
		packed := r.files["packed-refs"]
		padded(t, packed, filepath.Join(filepath.Dir(packed), "all-refs"), r.c1+" refs/tags/pad-%08d\n", 1<<20, false)
		linkTo(t, "all-refs", packed)
		for _, rd := range readers {
			if err := rd.read(context.Background(), r); err != nil {
				t.Errorf("%s: %v, want it read", rd.name, err)
			}
		}
	})
}

// moveOut moves the folder at path to a folder of the test's own outside
// the repository and puts a link to it in its place.
func moveOut(t *testing.T, path string) {
	elsewhere := filepath.Join(t.TempDir(), filepath.Base(path))
	if err := os.CopyFS(elsewhere, os.DirFS(path)); err != nil {
		t.Error(err)
	} else if err := os.RemoveAll(path); err != nil {
		t.Error(err)
	} else if err := os.Symlink(elsewhere, path); err != nil {
		t.Error(err)
	}
}

// A folder on the way to the repository's files that is a link out of the
// git directory (objects, refs, refs/heads, each to a copy of it
// elsewhere), and a .git that is a link to a git directory elsewhere, are
// refused by each read that opens a file beneath it: Compare and FileAt
// read commits by id and open no ref through the package. objects made a
// link out between the root's look at a file beneath it and the open
// (afterLook) is refused by the open.
func TestALinkOutOfTheGitDirectoryIsRefused(t *testing.T) {
	for _, folder := range []string{"objects", "refs", "refs/heads"} {
		t.Run(folder, func(t *testing.T) {
			t.Parallel()
			r := linked(t)
			moveOut(t, filepath.Join(filepath.Dir(r.files["config"]), folder))
			for _, rd := range readers {
				err := rd.read(context.Background(), r)
				if folder != "objects" && (rd.name == "Compare" || rd.name == "FileAt") {
					if err != nil {
						t.Errorf("%s: %v, want it read", rd.name, err)
					}
				} else if !errors.Is(err, ErrUnreadable) {
					t.Errorf("%s: %v, want the refusal", rd.name, err)
				}
			}
		})
	}
	t.Run("objects made a link out between the look and the open", func(t *testing.T) {
		r := linked(t)
		objects := filepath.Join(filepath.Dir(r.files["config"]), "objects")
		var first string
		afterLook = func(rel string) {
			if first == "" && strings.HasPrefix(rel, "objects/") {
				first = rel
				moveOut(t, objects)
			}
		}
		defer func() { afterLook = nil }()
		want := " is behind a link that is not followed: it leads out of the git directory or is written as an absolute path"
		for i, rd := range readers {
			err := rd.read(context.Background(), r)
			if !errors.Is(err, ErrUnreadable) || (i == 0 && !strings.HasSuffix(err.Error(), ": "+first+want)) {
				t.Errorf("%s: %v, want the refusal (%q%s first)", rd.name, err, first, want)
			}
		}
		if first == "" {
			t.Error("no file beneath objects was looked at")
		}
	})
	t.Run(".git", func(t *testing.T) {
		t.Parallel()
		dir := scratch(t)
		write(t, dir, "a.txt", "one\n")
		c := commit(t, dir, time.Time{}, "one")
		elsewhere := filepath.Join(t.TempDir(), "git")
		if err := os.Rename(filepath.Join(dir, ".git"), elsewhere); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		if _, err := Commits(context.Background(), dir, "", time.Time{}); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Commits: %v, want the refusal", err)
		}
		if _, err := FileAt(context.Background(), dir, c, "a.txt", 100); !errors.Is(err, ErrUnreadable) {
			t.Errorf("FileAt: %v, want the refusal", err)
		}
	})
}

// The opener follows a relative link within its folder (HEAD, to a regular
// file beside it) and reads it; a named pipe (under the test's own folder)
// is refused by the look, before any open.
func TestTheOpenerFollowsALinkWithinItsFolderAndRefusesAPipe(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "head-data", "ref: refs/heads/main\n")
	if err := os.Symlink("head-data", filepath.Join(dir, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen := &refusal{}
	defer seen.close()
	fs := careFS(dir, gitDir, seen).billy()
	f, err := fs.Open("HEAD")
	if err != nil {
		t.Fatalf("open HEAD: %v", err)
	}
	f.Close()
	// Refused by the look (its kind), not by the look at the open file.
	if _, err := fs.Open("pipe"); !errors.Is(seen.first(), ErrUnreadable) || err == nil || !strings.HasSuffix(err.Error(), ": pipe is a named pipe") {
		t.Fatalf("open pipe: %v, noted %v", err, seen.first())
	}
}

// socket makes a unix socket at path and closes it: bound through a link
// to path's folder from a short folder of the test's own, since a socket's
// path is at most about a hundred bytes.
func socket(t *testing.T, path string) {
	t.Helper()
	short, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	link := filepath.Join(short, "d")
	if err := os.Symlink(filepath.Dir(path), link); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: filepath.Join(link, filepath.Base(path))}); err != nil {
		t.Fatal(err)
	}
}

// An ordinary repository whose .git is a link to its git directory is
// refused by each of the four reads, wherever the link leads, in one of
// two sentences. A relative link that leads to a folder inside the working
// folder, directly or through a second relative link, is followed by the
// folder's root to a folder, and under refuses it, looked at without
// following it: ".git is a link". A relative link that leads out of the
// working folder, and a link written as an absolute path, to a folder
// inside the working folder or to one elsewhere, are refused by the root's
// look at .git before under, in that look's words: ".git is behind a link
// that is not followed". Each repository is read before its .git is made a
// link.
func TestADotGitThatIsALinkIsRefused(t *testing.T) {
	const prefix = "a file of the repository was not read: "
	const isALink = ".git is a link"
	const notFollowed = ".git is behind a link that is not followed: it leads out of its folder or is written as an absolute path"
	for _, c := range []struct {
		name, want string
		// link moves the git directory of the working folder wt and is
		// what .git's link is written as, the links on the way made.
		link func(t *testing.T, wt string) string
	}{
		{"a relative link to a folder inside the working folder", isALink, func(t *testing.T, wt string) string {
			moveTo(t, wt, filepath.Join(wt, "git-data"))
			return "git-data"
		}},
		{"a chain of two relative links to a folder inside the working folder", isALink, func(t *testing.T, wt string) string {
			moveTo(t, wt, filepath.Join(wt, "git-data"))
			if err := os.Symlink("git-data", filepath.Join(wt, "git-link")); err != nil {
				t.Fatal(err)
			}
			return "git-link"
		}},
		{"an absolute link to a folder inside the working folder", notFollowed, func(t *testing.T, wt string) string {
			moveTo(t, wt, filepath.Join(wt, "git-data"))
			return filepath.Join(wt, "git-data")
		}},
		{"a relative link out of the working folder", notFollowed, func(t *testing.T, wt string) string {
			elsewhere := filepath.Join(t.TempDir(), "git")
			moveTo(t, wt, elsewhere)
			rel, err := filepath.Rel(wt, elsewhere)
			if err != nil || !strings.HasPrefix(rel, "..") {
				t.Fatalf("the link from %s to %s is %q (%v), want a relative path out of the working folder", wt, elsewhere, rel, err)
			}
			return rel
		}},
		{"an absolute link to a git directory elsewhere", notFollowed, func(t *testing.T, wt string) string {
			elsewhere := filepath.Join(t.TempDir(), "git")
			moveTo(t, wt, elsewhere)
			return elsewhere
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := ordinary(t)
			for _, rd := range readers {
				if err := rd.read(context.Background(), r); err != nil {
					t.Fatalf("%s before the change: %v", rd.name, err)
				}
			}
			if err := os.Symlink(c.link(t, r.wt), filepath.Join(r.wt, ".git")); err != nil {
				t.Fatal(err)
			}
			for _, rd := range readers {
				if err := rd.read(context.Background(), r); !errors.Is(err, ErrUnreadable) || err.Error() != prefix+c.want {
					t.Errorf("%s: %v, want %q", rd.name, err, prefix+c.want)
				}
			}
		})
	}
}

// A folder beneath a repository whose .git is a link to a place that does
// not exist, written as an absolute path or as a relative path out of the
// folder, holds a .git to the discovery, which looks without following a
// link: each of the four reads, started in that folder, is refused as the
// opener refuses such a link.
func TestADotGitLinkToNothingBeneathARepositoryIsRefused(t *testing.T) {
	const want = "a file of the repository was not read: .git is behind a link that is not followed: it leads out of its folder or is written as an absolute path"
	for _, c := range []struct {
		name   string
		target func(t *testing.T) string
	}{
		{"an absolute link", func(t *testing.T) string { return filepath.Join(t.TempDir(), "nowhere", "git") }},
		{"a relative link out of the folder", func(*testing.T) string { return filepath.Join("..", "nowhere", "git") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := ordinary(t)
			sub := filepath.Join(r.wt, "sub")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(c.target(t), filepath.Join(sub, ".git")); err != nil {
				t.Fatal(err)
			}
			r.wt = sub
			for _, rd := range readers {
				if err := rd.read(context.Background(), r); !errors.Is(err, ErrUnreadable) || err.Error() != want {
					t.Errorf("%s: %v, want %q", rd.name, err, want)
				}
			}
		})
	}
}

// moveTo moves the git directory of the working folder wt (its .git) to
// path.
func moveTo(t *testing.T, wt, path string) {
	t.Helper()
	if err := os.Rename(filepath.Join(wt, ".git"), path); err != nil {
		t.Fatal(err)
	}
}

// A linked worktree whose .git file is a relative link to a regular file
// inside the worktree is read by each of the four reads, as before the
// opener: the root follows the link, and the file it leads to names the
// worktree's git directory.
func TestALinkedWorktreesDotGitFileThatIsARelativeLinkInsideIsRead(t *testing.T) {
	r := linked(t)
	if err := os.Rename(r.files[".git"], filepath.Join(r.wt, "git-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("git-file", r.files[".git"]); err != nil {
		t.Fatal(err)
	}
	for _, rd := range readers {
		if err := rd.read(context.Background(), r); err != nil {
			t.Errorf("%s: %v, want it read", rd.name, err)
		}
	}
}

// ReadDir lists a folder only: a name its look finds a named pipe or a
// regular file is refused at once by that look ("not a directory"), before
// any open, and a folder is listed as before, sorted by name. The listing
// then opens its folder as a folder (dirOnly); no test guards that open's
// "/." itself: nothing lets a test put another thing in the folder's place
// between the look and the open, and the look already refuses what is not
// a folder. In that moment the open answers the system's "not a
// directory".
func TestReadDirListsAFolderOnly(t *testing.T) {
	dir := t.TempDir()
	pipe := fifo(t, filepath.Join(dir, "pipe"))
	write(t, dir, "file", "a regular file\n")
	write(t, filepath.Join(dir, "folder"), "b", "b\n")
	write(t, filepath.Join(dir, "folder"), "a", "a\n")
	seen := &refusal{}
	defer seen.close()
	fs := careFS(dir, gitDir, seen).billy()
	for _, name := range []string{"pipe", "file"} {
		err := within(t, pipe, "ReadDir of "+name, func(context.Context) error {
			_, err := fs.ReadDir(name)
			return err
		})
		if err == nil || !strings.HasPrefix(err.Error(), "readdir ") || !strings.HasSuffix(err.Error(), "/"+name+": not a directory") {
			t.Errorf("ReadDir(%q): %v, want it refused as not a directory", name, err)
		}
	}
	list, err := fs.ReadDir("folder")
	if err != nil || len(list) != 2 || list[0].Name() != "a" || list[1].Name() != "b" {
		t.Errorf("ReadDir(folder): %v %v, want a and b", list, err)
	}
}

// The opener handed a path that is not a folder (a named pipe, a socket, a
// regular file) looks for the repository from the folder that holds it, as
// go-git does, and opens nothing at the path: inside a repository's
// working folder it opens the repository, and in a folder of the test's
// own outside any repository it answers ErrNotRepo, at once. The helper
// that waits is handed the pipe of the folder the call is made in, the one
// that call could wait on.
func TestTheOpenerHandedAPathThatIsNotAFolder(t *testing.T) {
	inside, outside := linked(t).wt, t.TempDir()
	pipes := map[string]string{}
	for _, dir := range []string{inside, outside} {
		pipes[dir] = fifo(t, filepath.Join(dir, "pipe"))
		socket(t, filepath.Join(dir, "socket"))
		write(t, dir, "file", "a regular file\n")
	}
	for _, name := range []string{"pipe", "socket", "file"} {
		for dir, want := range map[string]error{inside: nil, outside: ErrNotRepo} {
			path := filepath.Join(dir, name)
			err := within(t, pipes[dir], "the opener handed "+path, func(ctx context.Context) error {
				_, _, seen, err := open(ctx, path)
				if err == nil {
					seen.close()
				}
				return err
			})
			if (err == nil) != (want == nil) || !errors.Is(err, want) {
				t.Errorf("open(%s): %v, want %v", path, err, want)
			}
		}
	}
}

// writeEnd waits for made, then until a process has the named pipe at path
// open for reading (a non-blocking open of it for writing succeeds only
// then), and is the write end it opened, held so that the reader waits in
// its read; -1 when ctx ends first.
func writeEnd(ctx context.Context, path string, made <-chan struct{}) int {
	select {
	case <-made:
	case <-ctx.Done():
		return -1
	}
	for {
		if fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			return fd
		}
		select {
		case <-ctx.Done():
			return -1
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// FileAt with config made a named pipe once the repository is open (at the
// first look at an object, as TestFileAtAnswersItsCapOrTheCallersOwnError
// makes it) and the caller's context ended while the git binary has the
// pipe open (writeEnd): FileAt returns the caller's own error, never the
// cap's, after one opening, and no process has the pipe open once it has
// returned. The cap is far above the test's waits, so only the caller's
// context ends git.
//
// The test ends what it starts on every path. The write end it holds keeps
// git in its read of config; when the call is not back within the helper's
// wait, the helper's context ends and the write end is closed, so git reads
// the end of the pipe and the helper's let-go finds the call back. The test
// waits for the call before it puts the package's variables back.
func TestFileAtAnswersTheCallersOwnErrorWhileGitRuns(t *testing.T) {
	r := linked(t)
	config := r.files["config"]
	openings := 0
	made := make(chan struct{})
	var once sync.Once
	afterLook = func(rel string) {
		if rel == "commondir" {
			openings++
		}
		if strings.HasPrefix(rel, "objects/") {
			once.Do(func() {
				if err := os.Remove(config); err != nil {
					t.Error(err)
				} else if err := syscall.Mkfifo(config, 0o600); err != nil {
					t.Error(err)
				}
				close(made)
			})
		}
	}
	gitCap = 10 * hangWait
	back := make(chan struct{})
	defer func() {
		select {
		case <-back:
		case <-time.After(hangWait):
			t.Errorf("FileAt is still not back %v after the helper let it go; the package's variables are left as the call reads them", hangWait)
			return
		}
		afterLook = nil
		gitCap = maxGitRead
	}()
	held := false
	err := within(t, config, "config a named pipe git has open, FileAt", func(helper context.Context) error {
		defer close(back)
		ctx, cancel := context.WithCancel(helper)
		defer cancel()
		returned := make(chan struct{})
		polled := make(chan bool, 1)
		go func() {
			fd := writeEnd(ctx, config, made)
			if fd < 0 {
				polled <- false
				return
			}
			cancel()
			select {
			case <-returned:
			case <-helper.Done():
			}
			syscall.Close(fd)
			polled <- true
		}()
		_, err := FileAt(ctx, r.wt, r.c2, "a.txt", 100)
		close(returned)
		cancel()
		held = <-polled
		return err
	})
	if !held {
		t.Errorf("no process opened config for reading before FileAt returned")
	}
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrGitCap) || openings != 1 {
		t.Errorf("FileAt: %v after %d openings, want the caller's own error (%v) after one", err, openings, context.Canceled)
	}
	if fd, err := syscall.Open(config, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		syscall.Close(fd)
		t.Errorf("a process still has config open for reading after FileAt returned")
	} else if err != syscall.ENXIO {
		t.Errorf("open config for writing: %v, want ENXIO (no reader)", err)
	}
}

// A read whose context ends between two of its steps returns the context's
// error, takes no step after it and leaves no goroutine of its own behind.
// The context is ended at the look at a file (afterLook): in CommitDetail at
// the first look at packed-refs, as it tries the first of the candidates of
// a revision's name that names nothing (which go-git goes on trying); in
// Commits at the second commit of its walk to the merge base with main
// (which takes no context: it would read the third); in Compare at the
// second file of the comparison.
func TestAReadEndsWithItsContext(t *testing.T) {
	r := ordinary(t)
	gitIn(t, r.wt, time.Time{}, "pack-refs", "--all")
	for i := range 3 {
		write(t, r.wt, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("file %d\n", i))
	}
	c3 := commit(t, r.wt, time.Time{}, "three")
	write(t, r.wt, "f0.txt", "file 0, again\n")
	commit(t, r.wt, time.Time{}, "four")
	object := func(rev string) string {
		id := gitIn(t, r.wt, time.Time{}, "rev-parse", rev)
		return filepath.Join("objects", id[:2], id[2:])
	}
	for _, c := range []struct {
		name, at string // at: the file at whose look the context ends
		read     func(ctx context.Context) error
	}{
		{"CommitDetail", "packed-refs", func(ctx context.Context) error {
			_, err := CommitDetail(ctx, r.wt, "nosuch")
			return err
		}},
		{"Commits", object(c3), func(ctx context.Context) error {
			_, err := Commits(ctx, r.wt, "main", time.Time{})
			return err
		}},
		{"Compare", object(c3 + ":f1.txt"), func(ctx context.Context) error {
			_, err := Compare(ctx, r.wt, r.c2, c3)
			return err
		}},
	} {
		ended := false
		var after []string
		err := within(t, "", c.name, func(ctx context.Context) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			afterLook = func(rel string) {
				if ended {
					after = append(after, rel)
				} else if rel == c.at {
					ended = true
					cancel()
				}
			}
			defer func() { afterLook = nil }()
			return c.read(ctx)
		})
		if !ended || !errors.Is(err, context.Canceled) || len(after) > 0 {
			t.Errorf("%s, its context ended at the look at %s (%v): %v, with %v looked at after; want the context's error and nothing after", c.name, c.at, ended, err, after)
		}
		if left := leftBehind(); len(left) > 0 {
			t.Errorf("%s left %d goroutines behind:\n%s", c.name, len(left), strings.Join(left, "\n\n"))
		}
	}
}

// endedReads are Compare and CommitDetail of an ordinary repository's two
// commits, their context ended by the test.
var endedReads = []struct {
	name string
	read func(ctx context.Context, r linkedRepo) error
}{
	{"Compare", func(ctx context.Context, r linkedRepo) error {
		_, err := Compare(ctx, r.wt, r.c1, r.c2)
		return err
	}},
	{"CommitDetail", func(ctx context.Context, r linkedRepo) error {
		_, err := CommitDetail(ctx, r.wt, "HEAD")
		return err
	}},
}

// A read whose context has ended as it is about to answer answers the
// context's error, whatever else it kept: the context ended at the look
// at config (afterLook), one byte past its bound, which the opening then
// refuses; ended at the discovery's look in the first folder of one that
// is no repository (looking); and ended as the comparison's first file is
// diffed (the clock), after the opening refused a listing go-git reads
// past (the objects folder, past the listing's bound, which go-git lists
// to look for objects another git is receiving).
func TestAReadWhoseContextHasEndedAnswersItsError(t *testing.T) {
	for _, rd := range endedReads {
		r := ordinary(t)
		if err := os.Truncate(filepath.Join(r.wt, ".git", "config"), maxConfig+1); err != nil {
			t.Fatal(err)
		}
		ended := false
		err := within(t, "", rd.name, func(ctx context.Context) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			afterLook = func(rel string) {
				if rel == "config" {
					ended = true
					cancel()
				}
			}
			defer func() { afterLook = nil }()
			return rd.read(ctx, r)
		})
		if !ended || !errors.Is(err, context.Canceled) {
			t.Errorf("%s, its context ended at the look at config, one byte past its bound (%v): %v; want the context's error", rd.name, ended, err)
		}

		r = linkedRepo{wt: t.TempDir(), c1: strings.Repeat("a", 40), c2: strings.Repeat("b", 40)}
		looked := 0
		err = within(t, "", rd.name, func(ctx context.Context) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			looking = func(string) {
				if looked++; looked == 1 {
					cancel()
				}
			}
			defer func() { looking = nil }()
			return rd.read(ctx, r)
		})
		if looked != 1 || !errors.Is(err, context.Canceled) {
			t.Errorf("%s in a folder that is no repository, its context ended at the first folder looked in (%d looked in): %v; want the context's error", rd.name, looked, err)
		}

		r = ordinary(t)
		gitIn(t, r.wt, time.Time{}, "repack", "-a", "-d", "-q")
		gitIn(t, r.wt, time.Time{}, "prune-packed")
		packs, err := os.ReadDir(filepath.Join(r.wt, ".git", "objects", "pack"))
		if err != nil {
			t.Fatal(err)
		}
		listBound = len(packs)
		t.Cleanup(func() { listBound = maxListed })
		for i := range listBound {
			if err := os.Mkdir(filepath.Join(r.wt, ".git", "objects", fmt.Sprintf("x%d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// Its context not ended, the read goes on past the refusal to its
		// diff, and answers the refusal.
		for _, end := range []bool{false, true} {
			diffs, cancel := 0, context.CancelFunc(nil)
			fakeClock(t, func(int) time.Duration { return 0 }, func(int) {
				if diffs++; end {
					cancel()
				}
			})
			err = within(t, "", rd.name, func(ctx context.Context) error {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				return rd.read(ctx, r)
			})
			want := error(ErrUnreadable)
			if end {
				want = context.Canceled
			}
			if diffs != 1 || !errors.Is(err, want) {
				t.Errorf("%s, a listing refused, its context ended in the first file's diff %v (%d diffs): %v; want %v", rd.name, end, diffs, err, want)
			}
		}
		listBound = maxListed
	}
}

// leftBehind is the stacks of the goroutines, the caller's aside, that run
// go-git's code or wait on a process: what a read started and did not end.
func leftBehind() []string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var left []string
	for _, g := range strings.Split(string(buf), "\n\n")[1:] {
		if strings.Contains(g, "github.com/go-git/") || strings.Contains(g, "os/exec.") {
			left = append(left, g)
		}
	}
	return left
}

// A folder the server's user may enter but not list (mode 0311) between the
// read's folder and the repository's top is looked through, as the
// discovery looks for .git with a stat and opens no folder on the way: each
// of the four reads, started beneath it, answers as it does from the top.
func TestTheDiscoveryLooksThroughAFolderItMayEnterButNotList(t *testing.T) {
	r := ordinary(t)
	shut := filepath.Join(r.wt, "shut")
	beneath := filepath.Join(shut, "beneath")
	if err := os.MkdirAll(beneath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shut, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shut, 0o755) })
	if _, err := os.ReadDir(shut); err == nil {
		t.Skip("the test's user lists a folder of mode 0311: modes do not bind it")
	}
	r.wt = beneath
	for _, rd := range readers {
		err := within(t, "", rd.name, func(ctx context.Context) error { return rd.read(ctx, r) })
		if err != nil {
			t.Errorf("%s beneath a folder of mode 0311: %v, want it read", rd.name, err)
		}
	}
}
