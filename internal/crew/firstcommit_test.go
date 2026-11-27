package crew

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A new project's first commit (issue 1179, firstcommit.go): what goes in,
// whose name is on it, what is never done, and what a failure leaves. Each
// test has a HOME of its own (t.Setenv), so none runs in parallel.

// newProject is an empty folder under a HOME of the test's own with no git
// configuration but what config writes there; it skips without git.
func newProject(t *testing.T, config string) (dir, home string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if config != "" {
		writeFile(t, filepath.Join(home, ".gitconfig"), config)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir, home
}

// gitOf runs git in dir with no identity of the test's, its output trimmed.
func gitOf(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// committed is what HEAD holds in dir, path by path.
func committed(t *testing.T, dir string) []string {
	t.Helper()
	out := gitOf(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func mustFirstCommit(t *testing.T, dir string) FirstCommit {
	t.Helper()
	fc, err := EnsureFirstCommit(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fc.Made || fc.Commit == "" || gitOf(t, dir, "rev-parse", "HEAD") != fc.Commit {
		t.Fatalf("no first commit made: %+v", fc)
	}
	return fc
}

func noRepository(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a repository is in %s: %v", dir, err)
	}
}

// An empty folder gets a repository on main and an empty first commit, in
// Conductor's name where git knows none, with its one-line message; no git
// settings are written anywhere.
func TestAnEmptyFolderGetsAnEmptyFirstCommit(t *testing.T) {
	dir, home := newProject(t, "")
	fc := mustFirstCommit(t, dir)
	if !fc.Init || fc.Top != dir {
		t.Fatalf("made %+v", fc)
	}
	if got := committed(t, dir); got != nil {
		t.Fatalf("an empty folder's commit holds %v", got)
	}
	if b := gitOf(t, dir, "symbolic-ref", "--short", "HEAD"); b != "main" {
		t.Fatalf("the first branch is %q", b)
	}
	if got := gitOf(t, dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%B"); got != "Conductor <conductor@noreply.invalid>|Conductor <conductor@noreply.invalid>|"+FirstCommitMessage {
		t.Fatalf("the commit: %q", got)
	}
	if !IsFirstCommit(t.Context(), dir, fc.Commit) {
		t.Fatal("not known as Conductor's first commit")
	}
	if _, err := os.Lstat(filepath.Join(home, ".gitconfig")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a .gitconfig was written: %v", err)
	}
	if cfg := gitOf(t, dir, "config", "--local", "--list"); strings.Contains(cfg, "user.") {
		t.Fatalf("an identity was written into the repository's settings:\n%s", cfg)
	}
}

// The folder's files go in as its ignore file leaves them, never
// Conductor's own .conductor/; the person's own git name is on it, and a
// configured first branch is used.
func TestAFolderWithFilesGetsThemCommittedAsItsIgnoreFileSays(t *testing.T) {
	cfg := "[user]\n\tname = Ada\n\temail = ada@example.com\n[init]\n\tdefaultBranch = trunk\n"
	dir, home := newProject(t, cfg)
	writeFile(t, filepath.Join(dir, ".gitignore"), "secret.txt\nbuild/\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "secret.txt"), "key\n")
	for _, d := range []string{"build", "pkg", filepath.Join(".conductor", "worktrees")} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "build", "out"), "x")
	writeFile(t, filepath.Join(dir, "pkg", "a.go"), "package pkg\n")
	writeFile(t, filepath.Join(dir, ".conductor", "worktrees", "note"), "x")
	mustFirstCommit(t, dir)
	if got, want := committed(t, dir), []string{".gitignore", "main.go", "pkg/a.go"}; !slices.Equal(got, want) {
		t.Fatalf("committed %v, want %v", got, want)
	}
	if got := gitOf(t, dir, "log", "-1", "--format=%an <%ae>"); got != "Ada <ada@example.com>" {
		t.Fatalf("author %q", got)
	}
	if b := gitOf(t, dir, "symbolic-ref", "--short", "HEAD"); b != "trunk" {
		t.Fatalf("the first branch is %q", b)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".gitconfig")); err != nil || string(b) != cfg {
		t.Fatalf("the person's settings changed: %q %v", b, err)
	}
}

// A repository with no commit yet keeps its .git; what was staged goes in
// with the rest, as the tree stands.
func TestARepositoryWithNoCommitGetsItsTreeCommitted(t *testing.T) {
	for _, staged := range []bool{false, true} {
		dir, _ := newProject(t, "")
		gitOf(t, dir, "init", "-q")
		writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
		writeFile(t, filepath.Join(dir, "b.txt"), "b\n")
		if staged {
			gitOf(t, dir, "add", "a.txt")
		}
		fc := mustFirstCommit(t, dir)
		if fc.Init {
			t.Fatal("a repository that was there is reported made")
		}
		if got := committed(t, dir); !slices.Equal(got, []string{"a.txt", "b.txt"}) {
			t.Fatalf("staged %v: committed %v", staged, got)
		}
	}
}

// A folder below the top of its repository: the commit is the repository's.
func TestAFolderBelowItsRepositorysTopCommitsTheRepository(t *testing.T) {
	dir, _ := newProject(t, "")
	gitOf(t, dir, "init", "-q")
	sub := filepath.Join(dir, "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "top.txt"), "t\n")
	writeFile(t, filepath.Join(sub, "app.txt"), "a\n")
	fc := mustFirstCommit(t, sub)
	if fc.Top != dir {
		t.Fatalf("top %q", fc.Top)
	}
	if got := committed(t, dir); !slices.Equal(got, []string{"app/app.txt", "top.txt"}) {
		t.Fatalf("committed %v", got)
	}
}

// A repository nested in the folder is left out, never added as a link.
func TestANestedRepositoryIsLeftOut(t *testing.T) {
	for _, outerRepo := range []bool{false, true} {
		dir, _ := newProject(t, "")
		inner := filepath.Join(dir, "vendor", "lib")
		if err := os.MkdirAll(inner, 0o755); err != nil {
			t.Fatal(err)
		}
		gitOf(t, inner, "init", "-q")
		writeFile(t, filepath.Join(inner, "lib.go"), "package lib\n")
		gitOf(t, inner, "add", "-A")
		gitOf(t, inner, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "lib")
		writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
		if outerRepo {
			gitOf(t, dir, "init", "-q")
		}
		mustFirstCommit(t, dir)
		if got := committed(t, dir); !slices.Equal(got, []string{"main.go"}) {
			t.Fatalf("outer repository %v: committed %v", outerRepo, got)
		}
		if links := gitOf(t, dir, "ls-files", "-s"); strings.Contains(links, "160000") {
			t.Fatalf("a nested repository was added as a link:\n%s", links)
		}
	}
}

// Past the bound (files, bytes) nothing is made: the refusal says the fact
// and the way on, and no repository appears.
func TestAFolderPastTheBoundIsRefusedAndNothingIsMade(t *testing.T) {
	dir, _ := newProject(t, "")
	for i := range MaxFirstCommitFiles + 1 {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%06d", i)), "")
	}
	_, err := EnsureFirstCommit(t.Context(), dir)
	if !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), "5,001 files and no commit yet: make its first commit yourself") {
		t.Fatalf("past the count: %v", err)
	}
	noRepository(t, dir)

	big, _ := newProject(t, "")
	f, err := os.Create(filepath.Join(big, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFirstCommitBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := EnsureFirstCommit(t.Context(), big); !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), "MB of files and no commit yet") {
		t.Fatalf("past the size: %v", err)
	}
	noRepository(t, big)
	if n := MaxFirstCommitFiles; n != 5000 || MaxFirstCommitBytes != 200<<20 {
		t.Fatalf("the bound moved: %d files, %d bytes", n, MaxFirstCommitBytes)
	}
}

// A home folder and a file system's root are never committed by Conductor.
func TestTheHomeFolderAndARootAreNeverCommitted(t *testing.T) {
	_, home := newProject(t, "")
	writeFile(t, filepath.Join(home, "notes.txt"), "n\n")
	if _, err := EnsureFirstCommit(t.Context(), home); !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), "the home folder") {
		t.Fatalf("home: %v", err)
	}
	noRepository(t, home)
	if why := notCommittedIn(string(filepath.Separator)); !strings.Contains(why, "root") {
		t.Fatalf("root: %q", why)
	}
}

// A folder whose git signs commits is never overridden: a signature that
// cannot be made fails the commit within the bound, the repository made
// for it is removed, and one that was there keeps its index as it was.
func TestACommitThatCannotBeSignedIsRefusedInTimeAndLeavesNothing(t *testing.T) {
	bin := t.TempDir()
	sign := filepath.Join(bin, "sign")
	writeFile(t, sign, "#!/bin/sh\nsleep 60\n")
	if err := os.Chmod(sign, 0o755); err != nil {
		t.Fatal(err)
	}
	was := firstCommitWait
	firstCommitWait = 2 * time.Second
	t.Cleanup(func() { firstCommitWait = was })
	dir, _ := newProject(t, "[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = "+sign+"\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	start := time.Now()
	_, err := EnsureFirstCommit(t.Context(), dir)
	if !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), "first commit could not be made") {
		t.Fatalf("signed: %v", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("the refusal took %v", d)
	}
	noRepository(t, dir)

	// A repository that was there: its index as the person left it.
	again, _ := newProject(t, "[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = false\n")
	gitOf(t, again, "init", "-q")
	writeFile(t, filepath.Join(again, "a.txt"), "a\n")
	writeFile(t, filepath.Join(again, "b.txt"), "b\n")
	gitOf(t, again, "add", "a.txt")
	before := gitOf(t, again, "ls-files", "--stage")
	if _, err := EnsureFirstCommit(t.Context(), again); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("signed, existing: %v", err)
	}
	if after := gitOf(t, again, "ls-files", "--stage"); after != before {
		t.Fatalf("the index changed: %q, was %q", after, before)
	}
	if _, err := os.Lstat(filepath.Join(again, ".git")); err != nil {
		t.Fatalf("the person's repository went: %v", err)
	}
}

// No hook of the folder runs for the commit.
func TestAFirstCommitRunsNoHook(t *testing.T) {
	hooks := t.TempDir()
	mark := filepath.Join(t.TempDir(), "ran")
	for _, h := range []string{"pre-commit", "commit-msg", "post-commit"} {
		writeFile(t, filepath.Join(hooks, h), "#!/bin/sh\ntouch "+mark+"\nexit 1\n")
		if err := os.Chmod(filepath.Join(hooks, h), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir, _ := newProject(t, "[core]\n\thooksPath = "+hooks+"\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	mustFirstCommit(t, dir)
	if _, err := os.Lstat(mark); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a hook ran")
	}
}

// Two at once in one new folder make one first commit; one made already is
// found and nothing more is made.
func TestTwoAtOnceMakeOneFirstCommit(t *testing.T) {
	dir, _ := newProject(t, "")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	var wg sync.WaitGroup
	got := make([]FirstCommit, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { got[i], errs[i] = EnsureFirstCommit(t.Context(), dir) })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || got[0].Made == got[1].Made {
		t.Fatalf("%+v %v", got, errs)
	}
	if n := gitOf(t, dir, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("%s commits", n)
	}
	head := gitOf(t, dir, "rev-parse", "HEAD")
	if again, err := EnsureFirstCommit(t.Context(), dir); err != nil || again.Made {
		t.Fatalf("again: %+v %v", again, err)
	}
	if gitOf(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("a second commit was made")
	}
}

// The folder's check reports a new project as no refusal, and makes
// nothing there.
func TestTheFolderCheckRefusesNoNewProjectAndMakesNothing(t *testing.T) {
	dir, _ := newProject(t, "")
	st, err := GitState(t.Context(), dir)
	if err != nil || st.Message != msgFirstCommit || st.InRepo {
		t.Fatalf("%+v %v", st, err)
	}
	noRepository(t, dir)
	gitOf(t, dir, "init", "-q")
	if st, err := GitState(t.Context(), dir); err != nil || st.Message != msgFirstCommit || !st.InRepo || st.HasCommit {
		t.Fatalf("%+v %v", st, err)
	}
	if out := gitOf(t, dir, "rev-list", "--all", "--count"); out != "0" {
		t.Fatalf("%s commits", out)
	}
}
