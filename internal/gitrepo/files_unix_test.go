//go:build unix

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// hangWait bounds a call the test expects back at once. Nothing in the
// package caps an open in time, so it only bounds a hang (a named pipe
// opened blocking waits for a writer for good), far above what opening a
// repository takes on a loaded machine.
const hangWait = time.Minute

// within is f's error, f run in a goroutine of its own that is over when
// within returns (done): a call still running after hangWait fails the
// test, and is then let go by a writer opening and closing pipe, which
// ends the read it waits in.
func within(t *testing.T, pipe string, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(hangWait):
	}
	t.Errorf("the call had not returned after %v", hangWait)
	for {
		if fd, err := syscall.Open(pipe, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			syscall.Close(fd)
		}
		select {
		case err := <-done:
			return err
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
					if err := within(t, path, func() error { return rd.read(ctx, r) }); !errors.Is(err, want) {
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
		err := within(t, pipe, func() error { return rd.read(context.Background(), r) })
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

// A link inside the git directory is followed, and what is read of it is
// bounded by the name asked for, never by the name it leads to: config, a
// link to a file under objects one byte past config's bound, is refused;
// packed-refs, a link to a file of another name of a mebibyte (past the
// bound of a file of no named kind), is read.
func TestALinkIsReadWithTheBoundOfTheNameAskedFor(t *testing.T) {
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

// A folder on the way to the repository's files that is a link out of the
// git directory (objects, to a copy of it elsewhere), and a .git that is a
// link to a git directory elsewhere, are refused by each read.
func TestALinkOutOfTheGitDirectoryIsRefused(t *testing.T) {
	t.Run("objects", func(t *testing.T) {
		t.Parallel()
		r := linked(t)
		objects := filepath.Join(filepath.Dir(r.files["config"]), "objects")
		elsewhere := filepath.Join(t.TempDir(), "objects")
		if err := os.CopyFS(elsewhere, os.DirFS(objects)); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(objects); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, objects); err != nil {
			t.Fatal(err)
		}
		for _, rd := range readers {
			if err := rd.read(context.Background(), r); !errors.Is(err, ErrUnreadable) {
				t.Errorf("%s: %v, want the refusal", rd.name, err)
			}
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

// The opener follows a link within its folder (HEAD, to a regular file
// beside it) and reads it; a named pipe (under the test's own folder) is
// refused by the look, before any open.
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
	fs := careFS(dir, seen).billy()
	f, err := fs.Open("HEAD")
	if err != nil {
		t.Fatalf("open HEAD: %v", err)
	}
	f.Close()
	if _, err := fs.Open("pipe"); !errors.Is(err, ErrUnreadable) || !errors.Is(seen.first(), ErrUnreadable) {
		t.Fatalf("open pipe: %v, noted %v", err, seen.first())
	}
}
