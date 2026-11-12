//go:build unix

package gitrepo

import (
	"context"
	"errors"
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
// named pipe with no writer, a link to a character device, a directory and
// a regular file past its bound: each of the package's four reads returns
// at once with the package's refusal (packed-refs is looked at by the
// opening even for a read by commit id, which the git binary's part of
// FileAt would otherwise open).
func TestARepositorysOwnFilesAreRefusedUnlessRegularWithinTheirBound(t *testing.T) {
	limits := map[string]int64{".git": maxSmall, "commondir": maxSmall, "HEAD": maxSmall, "config": maxConfig, "packed-refs": maxPackedRefs}
	kinds := []struct {
		name string
		make func(path string) error
	}{
		{"a named pipe with no writer", func(path string) error { return syscall.Mkfifo(path, 0o600) }},
		{"a link to a character device", func(path string) error { return os.Symlink("/dev/null", path) }},
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
				if kind.make == nil {
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
					if err := within(t, path, func() error { return rd.read(ctx, r) }); !errors.Is(err, want) {
						t.Errorf("%s: %v, want %v", rd.name, err, want)
					}
				}
			})
		}
	}
}

// The opener's one way to open a file takes a regular file only, judged
// on the open file: a link (here to a character device, under the test's
// own folder) is followed by the open and refused.
func TestTheOpenerRefusesALinkToADevice(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/dev/null", filepath.Join(dir, "HEAD")); err != nil {
		t.Fatal(err)
	}
	seen := &refusal{}
	fs := &careOS{osBase: nil, root: dir, seen: seen}
	if _, err := fs.Open(filepath.Join(dir, "HEAD")); !errors.Is(err, ErrUnreadable) || !errors.Is(seen.first(), ErrUnreadable) {
		t.Fatalf("open: %v, noted %v", err, seen.first())
	}
}
