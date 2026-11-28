package crew

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A new project's first commit (issue 1179, firstcommit.go): the one
// situation it is made in, a new folder, what goes in, whose name and
// signature are on it, what is never run, and that every other folder is
// left exactly as it was. Each
// test has a HOME of its own (t.Setenv) and keeps git's system
// configuration out (FirstCommitIn.GitEnv, and gitOf's environment), so
// none runs in parallel; the test repositories do no maintenance of their
// own.

// project is an empty folder for a first commit under a HOME of the
// test's own whose only git configuration is config, and what
// EnsureFirstCommit is handed for it: the folder opened. It skips without
// git, and where no first commit is built (firstCommitHere).
func project(t *testing.T, config string) (dir string, in FirstCommitIn) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if !firstCommitHere {
		t.Skip("no first commit is built on this system")
	}
	home := homeOf(t, config)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir, opened(t, dir, home)
}

// homeOf is a HOME of the test's own holding config as its .gitconfig.
func homeOf(t *testing.T, config string) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if config != "" {
		writeFile(t, filepath.Join(home, ".gitconfig"), config)
	}
	return home
}

// opened is what EnsureFirstCommit is handed for dir: the folder opened,
// git's system configuration out and its global one home's.
func opened(t *testing.T, dir, home string) FirstCommitIn {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return FirstCommitIn{Folder: root, Path: dir, GitEnv: map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(home, ".gitconfig"),
	}}
}

// gitOf runs git in dir as a person would, with the test's HOME, no system
// configuration and no maintenance, its output trimmed.
func gitOf(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// personCommits commits what is staged in dir as a person with signing off.
func personCommits(t *testing.T, dir, message string) {
	t.Helper()
	gitOf(t, dir, "-c", "user.name=p", "-c", "user.email=p@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", message)
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

func mustFirstCommit(t *testing.T, in FirstCommitIn) FirstCommit {
	t.Helper()
	fc, err := EnsureFirstCommit(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !fc.Made || fc.Commit == "" || gitOf(t, in.Path, "rev-parse", "HEAD") != fc.Commit {
		t.Fatalf("no first commit made: %+v", fc)
	}
	return fc
}

func refused(t *testing.T, in FirstCommitIn, words string) {
	t.Helper()
	fc, err := EnsureFirstCommit(t.Context(), in)
	if !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), words) || fc.Made {
		t.Fatalf("%+v %v, want a refusal saying %q", fc, err, words)
	}
}

func noEntry(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s is there: %v", path, err)
	}
}

// folderListing is every name under root, links not followed, with its kind, its
// mode and its content (a file's hash, a link's target), so that two
// listings are equal only when nothing under root changed.
func folderListing(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "%s %v", rel, fi.Mode())
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			to, _ := os.Readlink(p)
			fmt.Fprintf(&b, " -> %s", to)
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			fmt.Fprintf(&b, " %s", hex.EncodeToString(sum[:]))
		}
		b.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// marker is a program that leaves a mark when run, and the mark's path.
func marker(t *testing.T) (program, mark string) {
	t.Helper()
	bin := t.TempDir()
	mark = filepath.Join(bin, "ran")
	program = filepath.Join(bin, "mark")
	writeFile(t, program, "#!/bin/sh\ntouch "+mark+"\nexit 1\n")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	return program, mark
}

// clean says the person's index and work tree in dir match the commit.
func clean(t *testing.T, dir string) {
	t.Helper()
	if st := gitOf(t, dir, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Fatalf("the index is not the commit's:\n%s", st)
	}
}

// (A) An empty folder gets a repository on main and an empty first
// commit, in Conductor's name where the person configured none, with its
// one-line message; no git settings are written anywhere, and nothing of
// Conductor's is left beside it.
func TestAnEmptyFolderGetsAnEmptyFirstCommit(t *testing.T) {
	dir, in := project(t, "")
	fc := mustFirstCommit(t, in)
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
	noEntry(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"))
	if cfg := gitOf(t, dir, "config", "--local", "--list"); strings.Contains(cfg, "user.") || strings.Contains(cfg, "worktree") {
		t.Fatalf("the repository's settings hold more than git init writes:\n%s", cfg)
	}
	noEntry(t, filepath.Join(dir, ".conductor"))
}

// (A) The folder's files go in as its ignore file leaves them, never
// Conductor's own .conductor/ at any depth, even one the ignore file
// takes back in; the person's own git name is on it, a configured first
// branch is used, and the index is the commit's.
func TestAFolderWithFilesGetsThemCommittedAsItsIgnoreFileSays(t *testing.T) {
	cfg := "[user]\n\tname = Ada\n\temail = ada@example.com\n[init]\n\tdefaultBranch = trunk\n"
	dir, in := project(t, cfg)
	writeFile(t, filepath.Join(dir, ".gitignore"), "secret.txt\nbuild/\n!.conductor/\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "secret.txt"), "key\n")
	for _, d := range []string{"build", "pkg", filepath.Join(".conductor", "worktrees"), filepath.Join("pkg", ".conductor")} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "build", "out"), "x")
	writeFile(t, filepath.Join(dir, "pkg", "a.go"), "package pkg\n")
	writeFile(t, filepath.Join(dir, ".conductor", "worktrees", "note"), "x")
	writeFile(t, filepath.Join(dir, "pkg", ".conductor", "note"), "x")
	mustFirstCommit(t, in)
	if got, want := committed(t, dir), []string{".gitignore", "main.go", "pkg/a.go"}; !slices.Equal(got, want) {
		t.Fatalf("committed %v, want %v", got, want)
	}
	if got := gitOf(t, dir, "log", "-1", "--format=%an <%ae>"); got != "Ada <ada@example.com>" {
		t.Fatalf("author %q", got)
	}
	if b := gitOf(t, dir, "symbolic-ref", "--short", "HEAD"); b != "trunk" {
		t.Fatalf("the first branch is %q", b)
	}
	if b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".gitconfig")); err != nil || string(b) != cfg {
		t.Fatalf("the person's settings changed: %q %v", b, err)
	}
	clean(t, dir)
	// .conductor/ was there: it stays, and only Conductor's own folder in it went.
	if entries, _ := os.ReadDir(filepath.Join(dir, ".conductor")); len(entries) != 1 || entries[0].Name() != "worktrees" {
		t.Fatalf(".conductor holds %v", entries)
	}
}

// (A) A folder with anything named .git that git does not read as a
// repository is no new folder: a .git file, a .git link to a folder
// elsewhere (no repository, or one), a .git folder with an empty HEAD, one
// with no refs/. Each is refused, and the folder and the link's target are
// byte for byte as they were.
func TestAGitThatIsNoRepositoryIsRefusedAndLeftAsItWas(t *testing.T) {
	cases := []struct {
		name  string
		make  func(t *testing.T, dir, elsewhere string)
		words string
	}{
		{"a .git file", func(t *testing.T, dir, _ string) {
			writeFile(t, filepath.Join(dir, ".git"), "gitdir: /nowhere/at/all\n")
		}, msgMakeItYourself},
		{"a .git link to a folder that is no repository", func(t *testing.T, dir, elsewhere string) {
			writeFile(t, filepath.Join(elsewhere, "keep.txt"), "k\n")
			if err := os.Symlink(elsewhere, filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
		}, msgMakeItYourself},
		{"a .git link to a repository with no commit", func(t *testing.T, dir, elsewhere string) {
			gitOf(t, elsewhere, "init", "-q")
			if err := os.Symlink(filepath.Join(elsewhere, ".git"), filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
		}, msgMakeItYourself},
		{"a .git folder with an empty HEAD", func(t *testing.T, dir, _ string) {
			gitOf(t, dir, "init", "-q")
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			gitOf(t, dir, "add", "a.txt")
			personCommits(t, dir, "mine")
			writeFile(t, filepath.Join(dir, ".git", "HEAD"), "")
		}, gitsOwn},
		{"a .git folder with no refs/", func(t *testing.T, dir, _ string) {
			gitOf(t, dir, "init", "-q")
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			gitOf(t, dir, "add", "a.txt")
			personCommits(t, dir, "mine")
			gitOf(t, dir, "pack-refs", "--all")
			if err := os.RemoveAll(filepath.Join(dir, ".git", "refs")); err != nil {
				t.Fatal(err)
			}
		}, gitsOwn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, in := project(t, "")
			elsewhere := t.TempDir()
			writeFile(t, filepath.Join(dir, "mine.txt"), "m\n")
			c.make(t, dir, elsewhere)
			judged := dir
			before, target := folderListing(t, dir), folderListing(t, elsewhere)
			st, err := GitState(t.Context(), judged)
			if err != nil || !saysIt(st.Message, c.words) {
				t.Fatalf("the folder's check: %+v %v", st, err)
			}
			fc, err := EnsureFirstCommit(t.Context(), in)
			if !errors.Is(err, ErrNotRepo) || fc.Made || !saysIt(err.Error(), c.words) {
				t.Fatalf("the press: %+v %v", fc, err)
			}
			if after := folderListing(t, dir); after != before {
				t.Fatalf("the folder changed:\n%s\nwas\n%s", after, before)
			}
			if after := folderListing(t, elsewhere); after != target {
				t.Fatalf("the link's target changed:\n%s\nwas\n%s", after, target)
			}
		})
	}
}

// (A) Something put at .git between the judgement and the rename (a
// folder, an empty one too, a file, a link) is kept as it is; the first
// commit is refused, and Conductor's repository is removed.
func TestSomethingPutAtGitBeforeTheRenameIsKept(t *testing.T) {
	for _, theirs := range []string{"a folder", "an empty folder", "a file", "a link"} {
		t.Run(theirs, func(t *testing.T) {
			dir, in := project(t, "")
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			elsewhere := t.TempDir()
			in.hold = func(step string) {
				if step != "rename" {
					return
				}
				at := filepath.Join(dir, ".git")
				switch theirs {
				case "a folder":
					if err := os.Mkdir(at, 0o755); err != nil {
						t.Error(err)
					}
					writeFile(t, filepath.Join(at, "theirs"), "t\n")
				case "an empty folder":
					if err := os.Mkdir(at, 0o755); err != nil {
						t.Error(err)
					}
				case "a file":
					writeFile(t, at, "gitdir: theirs\n")
				case "a link":
					if err := os.Symlink(elsewhere, at); err != nil {
						t.Error(err)
					}
				}
			}
			refused(t, in, msgMakeItYourself)
			if fi, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
				t.Fatalf("theirs went: %v", err)
			} else if theirs == "an empty folder" {
				if entries, _ := os.ReadDir(filepath.Join(dir, ".git")); !fi.IsDir() || len(entries) != 0 {
					t.Fatalf("their empty folder was replaced: %v", entries)
				}
			}
			if theirs == "a folder" {
				if b, err := os.ReadFile(filepath.Join(dir, ".git", "theirs")); err != nil || string(b) != "t\n" {
					t.Fatalf("their folder changed: %q %v", b, err)
				}
			}
			noEntry(t, filepath.Join(dir, ".conductor"))
		})
	}
}

// (A) A commit that fails after the repository was built aside (a signer
// of the person's that refuses) leaves the folder as it was; an earlier
// attempt's folder under .conductor/ is removed by the next.
func TestAFailureAfterTheBuildAsideLeavesTheFolderAsItWas(t *testing.T) {
	dir, in := project(t, "[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = false\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	before := folderListing(t, dir)
	refused(t, in, "first commit could not be made")
	if after := folderListing(t, dir); after != before {
		t.Fatalf("the folder changed:\n%s\nwas\n%s", after, before)
	}

	again, in := project(t, "")
	stale := filepath.Join(again, ".conductor", asidePrefix+"0011223344556677")
	if err := os.MkdirAll(filepath.Join(stale, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A lock left with no folder: a stop between the lock's make and the folder's.
	lone := filepath.Join(again, ".conductor", asidePrefix+"8899aabbccddeeff"+asideLock)
	writeFile(t, lone, "")
	mustFirstCommit(t, in)
	noEntry(t, stale)
	noEntry(t, lone)
}

// A home folder and a file system's root are never committed by Conductor.
func TestTheHomeFolderAndARootAreNeverCommitted(t *testing.T) {
	_, _ = project(t, "")
	home := os.Getenv("HOME")
	writeFile(t, filepath.Join(home, "notes.txt"), "n\n")
	refused(t, opened(t, home, home), "the home folder")
	noEntry(t, filepath.Join(home, ".git"))
	if why := notCommittedIn(string(filepath.Separator)); why == nil || !strings.Contains(why.Error(), "root") {
		t.Fatalf("root: %v", why)
	}
}

// Two at once in one new folder make one first commit; one made already is
// found and nothing more is made.
func TestTwoAtOnceMakeOneFirstCommit(t *testing.T) {
	dir, in := project(t, "")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	var wg sync.WaitGroup
	got := make([]FirstCommit, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { got[i], errs[i] = EnsureFirstCommit(t.Context(), in) })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || got[0].Made == got[1].Made {
		t.Fatalf("%+v %v", got, errs)
	}
	if n := gitOf(t, dir, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("%s commits", n)
	}
	head := gitOf(t, dir, "rev-parse", "HEAD")
	if again, err := EnsureFirstCommit(t.Context(), in); err != nil || again.Made {
		t.Fatalf("again: %+v %v", again, err)
	}
	if gitOf(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("a second commit was made")
	}
}

// Only the operator's own launch makes a first commit: the options refuse
// one with a Brakeman turn's class or a provider's origin.
func TestLaunchOptionsRefuseAFirstCommitForAnyoneElse(t *testing.T) {
	first := &FirstCommitIn{Path: "/srv/new"}
	for name, o := range map[string]LaunchOpts{
		"a Brakeman turn": {FirstCommit: first, Class: ClassBrakeman, Request: "br-1"},
		"a provider":      {FirstCommit: first, Origin: &ledger.OriginRow{}},
	} {
		if err := o.check(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (LaunchOpts{FirstCommit: first}).check(); err != nil {
		t.Fatalf("the operator's: %v", err)
	}
}

// gitsOwn stands for git's own refusal, in whatever words the installed
// git uses (saysIt): what a .git folder git does not read is refused with.
const gitsOwn = "git's own refusal"

// saysIt reports whether msg is the refusal words names: the sentence word
// for word, or, for gitsOwn, a refusal of git's in any of its wordings
// (errNotInRepo, or git's own line after classifyRevParse's start).
func saysIt(msg, words string) bool {
	if words != gitsOwn {
		return msg == words
	}
	return msg == errNotInRepo.Error() || strings.HasPrefix(msg, "git cannot use the working directory's repository: ")
}

// The judgement gives the same answer whatever words a git prints for a
// .git it does not read: this machine's git, a newer one's (2.56: "gitfile
// does not point to a valid repository"), and words no git has printed
// yet. A .git file or link is refused in the sentence, a .git folder in
// git's own refusal, nothing named .git with words that are not "not a
// git repository" in those words; only those words with nothing named
// .git here make a new folder. Nothing is made either way.
func TestTheJudgementDoesNotDependOnGitsWords(t *testing.T) {
	wordings := map[string]string{
		"this machine's for a .git file": "fatal: not a git repository: /nowhere/at/all",
		"no repository anywhere":         "fatal: not a git repository (or any of the parent directories): .git",
		"a newer git's for a .git file":  "fatal: gitfile does not point to a valid repository: /x/.git",
		"words no git has printed yet":   "fatal: the repository could not be opened",
	}
	kinds := []string{"a .git file", "a .git link", "a .git folder git does not read", "nothing named .git"}
	for wname, words := range wordings {
		for _, kind := range kinds {
			t.Run(wname+", "+kind, func(t *testing.T) {
				top := t.TempDir()
				dir := filepath.Join(top, "app")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "a .git file":
					writeFile(t, filepath.Join(dir, ".git"), "gitdir: /nowhere\n")
				case "a .git link":
					if err := os.Symlink(t.TempDir(), filepath.Join(dir, ".git")); err != nil {
						t.Fatal(err)
					}
				case "a .git folder git does not read":
					if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				root, err := os.OpenRoot(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				before := folderListing(t, top)
				refusal := classifyRevParse(fmt.Errorf("git rev-parse: %s", words))
				_, isNew, why, err := judged(State{}, refusal, nil, root.Lstat, dir)
				if err != nil {
					t.Fatal(err)
				}
				knownWords := strings.Contains(words, "not a git repository")
				switch {
				case kind == "nothing named .git" && knownWords:
					if !isNew || why != nil {
						t.Fatalf("a new folder is judged %v %v", isNew, why)
					}
				case isNew || why == nil:
					t.Fatalf("judged a new folder")
				case kind == "a .git file" || kind == "a .git link":
					if why.Error() != msgMakeItYourself {
						t.Fatalf("refused in other words: %v", why)
					}
				case !saysIt(why.Error(), gitsOwn):
					t.Fatalf("not git's own refusal: %v", why)
				}
				if after := folderListing(t, top); after != before {
					t.Fatalf("the folder changed:\n%s", after)
				}
			})
		}
	}
}

// A repository nested in the new folder is left out, never added as a link.
func TestANestedRepositoryIsLeftOut(t *testing.T) {
	dir, in := project(t, "")
	inner := filepath.Join(dir, "vendor", "lib")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOf(t, inner, "init", "-q")
	writeFile(t, filepath.Join(inner, "lib.go"), "package lib\n")
	gitOf(t, inner, "add", "-A")
	personCommits(t, inner, "lib")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	mustFirstCommit(t, in)
	if got := committed(t, dir); !slices.Equal(got, []string{"main.go"}) {
		t.Fatalf("committed %v", got)
	}
	if links := gitOf(t, dir, "ls-files", "-s"); strings.Contains(links, "160000") {
		t.Fatalf("a nested repository was added as a link:\n%s", links)
	}
}

// A repository with no commit is no new folder, whatever it holds and
// whatever its settings: one just started, one with files staged, one
// whose .git names a common directory elsewhere (by a relative path and
// an absolute one), one with links inside its .git, one with references
// but no commit on its branch, and a folder below such a repository's
// top. Each is refused in the sentence, at the folder's check and at the
// press, no program its settings name is run, and the folder, its .git
// whole and what a link points to are byte for byte as they were.
func TestARepositoryWithNoCommitIsRefusedAndLeftAsItWas(t *testing.T) {
	program, mark := marker(t)
	signer := "[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = " + program + "\n"
	appendTo := func(t *testing.T, path, text string) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(b)+text)
	}
	cases := []struct {
		name string
		make func(t *testing.T, dir, elsewhere string) (judged string)
	}{
		{"just started", func(t *testing.T, dir, _ string) string {
			gitOf(t, dir, "init", "-q")
			return dir
		}},
		{"with files staged", func(t *testing.T, dir, _ string) string {
			gitOf(t, dir, "init", "-q")
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			writeFile(t, filepath.Join(dir, "b.txt"), "b\n")
			gitOf(t, dir, "add", "a.txt")
			appendTo(t, filepath.Join(dir, ".git", "config"), signer)
			return dir
		}},
		{"a common directory named by a relative path", func(t *testing.T, dir, _ string) string {
			gitOf(t, dir, "init", "-q")
			gitOf(t, dir, "init", "-q", "--bare", ".shared")
			appendTo(t, filepath.Join(dir, ".shared", "config"), signer)
			writeFile(t, filepath.Join(dir, ".git", "commondir"), "../.shared\n")
			return dir
		}},
		{"a common directory named by an absolute path", func(t *testing.T, dir, elsewhere string) string {
			gitOf(t, dir, "init", "-q")
			gitOf(t, elsewhere, "init", "-q", "--bare")
			appendTo(t, filepath.Join(elsewhere, "config"), signer)
			writeFile(t, filepath.Join(dir, ".git", "commondir"), elsewhere+"\n")
			return dir
		}},
		{"links inside its .git", func(t *testing.T, dir, elsewhere string) string {
			gitOf(t, dir, "init", "-q")
			writeFile(t, filepath.Join(elsewhere, "startup"), "kept\n")
			if err := os.MkdirAll(filepath.Join(dir, ".git", "logs"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, at := range []string{filepath.Join(".git", "logs", "HEAD"), filepath.Join(".git", "index")} {
				if err := os.Symlink(filepath.Join(elsewhere, "startup"), filepath.Join(dir, at)); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			return dir
		}},
		{"references but no commit on its branch", func(t *testing.T, dir, _ string) string {
			gitOf(t, dir, "init", "-q")
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			gitOf(t, dir, "add", "a.txt")
			personCommits(t, dir, "mine")
			gitOf(t, dir, "checkout", "-q", "--orphan", "fresh")
			return dir
		}},
		{"a folder below its top", func(t *testing.T, dir, _ string) string {
			gitOf(t, dir, "init", "-q")
			sub := filepath.Join(dir, "app")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(sub, "app.txt"), "a\n")
			return sub
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := project(t, "")
			elsewhere := t.TempDir()
			judged := c.make(t, dir, elsewhere)
			before, target := folderListing(t, dir), folderListing(t, elsewhere)
			if st, err := GitState(t.Context(), judged); err != nil || st.Message == msgFirstCommit || st.Message == msgCanWorktree {
				t.Fatalf("the folder's check: %+v %v", st, err)
			}
			fc, err := EnsureFirstCommit(t.Context(), opened(t, judged, os.Getenv("HOME")))
			if !errors.Is(err, ErrNotRepo) || fc.Made {
				t.Fatalf("not refused: %+v %v", fc, err)
			}
			if err.Error() != msgMakeItYourself {
				t.Fatalf("refused in other words: %v", err)
			}
			noEntry(t, mark)
			if after := folderListing(t, dir); after != before {
				t.Fatalf("the folder changed:\n%s\nwas\n%s", after, before)
			}
			if after := folderListing(t, elsewhere); after != target {
				t.Fatalf("what a link points to changed:\n%s\nwas\n%s", after, target)
			}
		})
	}
}

// Where the file system has no rename that refuses to replace, the new
// repository is put in place by an exclusive make of .git and a plain
// rename over it: made, and the folder's own .git. Something put at .git
// meanwhile is kept and the commit refused; a step the file system cannot
// take is refused in its true reason.
func TestWithoutARenameThatRefusesTheRepositoryIsStillPutInPlace(t *testing.T) {
	unsupported := func(*os.File, string, *os.File, string) error {
		return &os.LinkError{Op: "renameat2", Err: syscall.EINVAL}
	}
	dir, in := project(t, "")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	in.rename = unsupported
	mustFirstCommit(t, in)
	if got := committed(t, dir); !slices.Equal(got, []string{"a.txt"}) {
		t.Fatalf("committed %v", got)
	}
	clean(t, dir)
	noEntry(t, filepath.Join(dir, ".conductor"))

	theirs, in := project(t, "")
	writeFile(t, filepath.Join(theirs, "a.txt"), "a\n")
	in.rename = unsupported
	in.hold = func(step string) {
		if step == "rename" {
			writeFile(t, filepath.Join(theirs, ".git"), "gitdir: theirs\n")
		}
	}
	refused(t, in, msgMakeItYourself)
	if b, err := os.ReadFile(filepath.Join(theirs, ".git")); err != nil || string(b) != "gitdir: theirs\n" {
		t.Fatalf("theirs changed: %q %v", b, err)
	}
	noEntry(t, filepath.Join(theirs, ".conductor"))

	denied, in := project(t, "")
	in.rename = func(*os.File, string, *os.File, string) error {
		return &os.LinkError{Op: "renameat2", Err: syscall.EACCES}
	}
	before := folderListing(t, denied)
	refused(t, in, "could not be put in place at .git: renameat2")
	if after := folderListing(t, denied); after != before {
		t.Fatalf("the folder changed:\n%s", after)
	}
}

// The new .git has the mode a plain git init gives under the umask.
func TestTheNewGitHasTheModeGitInitGives(t *testing.T) {
	dir, in := project(t, "")
	mustFirstCommit(t, in)
	plain := t.TempDir()
	gitOf(t, plain, "init", "-q")
	made, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(filepath.Join(plain, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if made.Mode() != want.Mode() {
		t.Fatalf(".git is %v, git init's %v", made.Mode(), want.Mode())
	}
}

// An earlier attempt's folder is swept only when its lock is free: one
// another Conductor holds locked, building in it, is left, and so is a
// person's own file that only looks like one.
func TestASweepLeavesAFolderAnotherIsBuildingIn(t *testing.T) {
	dir, in := project(t, "")
	busy := filepath.Join(dir, ".conductor", asidePrefix+"aaaaaaaaaaaaaaaa")
	if err := os.MkdirAll(filepath.Join(busy, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(busy+asideLock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if ok, err := lockFile(lock); !ok || err != nil {
		t.Fatalf("lock: %v %v", ok, err)
	}
	notes := filepath.Join(dir, ".conductor", asidePrefix+"notes")
	writeFile(t, notes, "mine\n")
	mustFirstCommit(t, in)
	if _, err := os.Stat(filepath.Join(busy, "objects")); err != nil {
		t.Fatalf("the folder another builds in went: %v", err)
	}
	if b, err := os.ReadFile(notes); err != nil || string(b) != "mine\n" {
		t.Fatalf("the person's file went: %q %v", b, err)
	}
}

// The bound: more than MaxFirstCommitFiles files or MaxFirstCommitBytes
// bytes is refused in the bound's own words, and nothing is made; the
// measure stops listing at the bound; files that came between the measure
// and the add are caught on the tree the add staged, before the commit.
func TestTheBoundHoldsOnTheMeasureAndOnWhatWasStaged(t *testing.T) {
	if n := MaxFirstCommitFiles; n != 5000 || MaxFirstCommitBytes != 200<<20 {
		t.Fatalf("the bound moved: %d files, %d bytes", n, MaxFirstCommitBytes)
	}
	many := func(t *testing.T, dir string, n int) {
		for i := range n {
			writeFile(t, filepath.Join(dir, fmt.Sprintf("f%06d", i)), "")
		}
	}
	dir, in := project(t, "")
	many(t, dir, MaxFirstCommitFiles+100)
	listed := 0
	in.hold = func(step string) {
		if step == "listed" {
			listed++
		}
	}
	refused(t, in, "This folder has more than 5,000 files and no commit yet. Make its first commit yourself, then launch")
	if listed != MaxFirstCommitFiles+1 {
		t.Fatalf("the measure read %d of the folder's files, not one past the bound", listed)
	}
	noEntry(t, filepath.Join(dir, ".git"))

	big, in := project(t, "")
	f, err := os.Create(filepath.Join(big, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFirstCommitBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	refused(t, in, "This folder has more than 200 MB of files and no commit yet")
	noEntry(t, filepath.Join(big, ".git"))

	late, in := project(t, "")
	writeFile(t, filepath.Join(late, "a.txt"), "a\n")
	in.hold = func(step string) {
		if step == "measured" {
			many(t, late, MaxFirstCommitFiles+1)
		}
	}
	refused(t, in, "This folder has more than 5,000 files and no commit yet")
	noEntry(t, filepath.Join(late, ".git"))
	noEntry(t, filepath.Join(late, ".conductor"))
}

// fakeSigner is an openpgp signing program that leaves a mark and a
// signature git takes, and the mark's path.
func fakeSigner(t *testing.T) (program, mark string) {
	t.Helper()
	bin := t.TempDir()
	mark = filepath.Join(bin, "signed")
	program = filepath.Join(bin, "sign")
	writeFile(t, program, "#!/bin/sh\ncat >/dev/null\ntouch "+mark+"\nprintf '\\n[GNUPG:] SIG_CREATED D 1 8 00 0 0\\n' >&2\nprintf -- '-----BEGIN PGP SIGNATURE-----\\n\\nfake\\n-----END PGP SIGNATURE-----\\n'\n")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	return program, mark
}

func signed(t *testing.T, dir, commit string) {
	t.Helper()
	if raw := gitOf(t, dir, "cat-file", "commit", commit); !strings.Contains(raw, "gpgsig -----BEGIN") {
		t.Fatalf("the commit is not signed:\n%s", raw)
	}
}

// The person's own signing settings sign the commit: their signing program
// runs (its mark) and the commit carries its signature.
func TestThePersonsSigningSettingsSignTheCommit(t *testing.T) {
	sign, mark := fakeSigner(t)
	dir, in := project(t, "[user]\n\tname = Ada\n\temail = ada@example.com\n[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = "+sign+"\n")
	fc := mustFirstCommit(t, in)
	if _, err := os.Lstat(mark); err != nil {
		t.Fatalf("the person's signer did not run: %v", err)
	}
	signed(t, dir, fc.Commit)
}

// Settings the person keeps where only the server's environment says
// (GIT_CONFIG_GLOBAL, XDG_CONFIG_HOME) are found: their name is on the
// commit and their signing signs it.
func TestSettingsFoundThroughTheServersEnvironmentAreThePersons(t *testing.T) {
	sign, mark := fakeSigner(t)
	settings := "[user]\n\tname = Ada\n\temail = ada@example.com\n[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = " + sign + "\n"
	for _, where := range []string{"GIT_CONFIG_GLOBAL", "XDG_CONFIG_HOME"} {
		t.Run(where, func(t *testing.T) {
			dir, in := project(t, "")
			delete(in.GitEnv, "GIT_CONFIG_GLOBAL")
			// Neither place set by the machine running the test: only the one
			// this case sets says where the settings are.
			for _, v := range []string{"GIT_CONFIG_GLOBAL", "XDG_CONFIG_HOME"} {
				t.Setenv(v, "")
				os.Unsetenv(v)
			}
			kept := t.TempDir()
			switch where {
			case "GIT_CONFIG_GLOBAL":
				writeFile(t, filepath.Join(kept, "gitconfig"), settings)
				t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(kept, "gitconfig"))
			default:
				if err := os.Mkdir(filepath.Join(kept, "git"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(kept, "git", "config"), settings)
				t.Setenv("XDG_CONFIG_HOME", kept)
			}
			fc := mustFirstCommit(t, in)
			if got := gitOf(t, dir, "log", "-1", "--format=%an <%ae>"); got != "Ada <ada@example.com>" {
				t.Fatalf("the commit is %q", got)
			}
			signed(t, dir, fc.Commit)
			if _, err := os.Lstat(mark); err != nil {
				t.Fatalf("the person's signer did not run: %v", err)
			}
			os.Remove(mark)
		})
	}
}

// An ssh signer that needs its agent reaches it through the socket the
// server's environment names; without it the commit, which must be signed,
// is refused, never made unsigned.
func TestAnSSHSignerReachesItsAgent(t *testing.T) {
	sshSigning(t)
	bin := t.TempDir()
	sock := filepath.Join(bin, "agent.sock")
	sign := filepath.Join(bin, "ssh-sign")
	writeFile(t, sign, "#!/bin/sh\n[ \"$SSH_AUTH_SOCK\" = \""+sock+"\" ] || { echo 'no agent' >&2; exit 3; }\nfor last; do :; done\nprintf -- '-----BEGIN SSH SIGNATURE-----\\nZmFrZQ==\\n-----END SSH SIGNATURE-----\\n' > \"$last.sig\"\n")
	if err := os.Chmod(sign, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[user]\n\tname = Ada\n\temail = ada@example.com\n\tsigningKey = key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOmR test\n[commit]\n\tgpgSign = true\n[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tprogram = " + sign + "\n"
	t.Setenv("SSH_AUTH_SOCK", sock)
	dir, in := project(t, cfg)
	fc := mustFirstCommit(t, in)
	signed(t, dir, fc.Commit)

	os.Unsetenv("SSH_AUTH_SOCK")
	without, in := project(t, cfg)
	refused(t, in, "first commit could not be made")
	noEntry(t, filepath.Join(without, ".git"))
}

// A signing program of the person's that never ends and holds git's output
// (an ssh signer git hands its own standard output) is ended at the
// commit's bound with its whole process group, the bound started once it
// runs: the first commit is refused, nothing is left running, nothing is
// made, and the next call is not held.
func TestASignerThatNeverEndsIsEndedAtTheBound(t *testing.T) {
	sshSigning(t)
	bin := t.TempDir()
	pidFile := filepath.Join(bin, "pid")
	sign := filepath.Join(bin, "sign")
	writeFile(t, sign, "#!/bin/sh\necho $$ > "+pidFile+"\nexec sleep 600\n")
	if err := os.Chmod(sign, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, in := project(t, "[user]\n\tname = Ada\n\temail = ada@example.com\n\tsigningKey = key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOmR test\n[commit]\n\tgpgSign = true\n[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tprogram = "+sign+"\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	in.CommitWait = time.Second
	done := make(chan error, 1)
	running := make(chan int, 1)
	quit := make(chan struct{})
	t.Cleanup(func() { close(quit) })
	// The bound's clock starts once the signer runs, so that nothing before
	// it (git's start, its look at the configuration) spends it.
	in.commitClock = func(start func()) {
		go func() {
			if pid := signerPid(pidFile, quit); pid > 0 {
				running <- pid
				start()
			}
		}()
	}
	go func() {
		_, err := EnsureFirstCommit(t.Context(), in)
		done <- err
	}()
	var pid int
	select {
	case pid = <-running:
	case err := <-done:
		t.Fatalf("the first commit answered before the signer ran: %v", err)
	}
	if err := <-done; !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), "did not finish within its bound") {
		t.Fatalf("refused: %v", err)
	}
	// The group was sent SIGKILL as the call returned: what is left is the
	// kernel's delivery and the reaping by the machine's init, which no
	// bound of the code's covers; signerGone bounds the test's wait for it.
	for gone := time.Now().Add(signerGone); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(gone) {
			t.Fatalf("the signer %d is still running %s after the call returned", pid, signerGone)
		}
	}
	noEntry(t, filepath.Join(dir, ".git"))
	noEntry(t, filepath.Join(dir, ".conductor"))
	// The lock is free: the next call, with signing off, is made.
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"), "[user]\n\tname = Ada\n\temail = ada@example.com\n")
	next, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustFirstCommit(t, opened(t, next, os.Getenv("HOME")))
}

// signerPid is the pid a program wrote to path, once it has; 0 once quit is
// closed first.
func signerPid(path string, quit <-chan struct{}) int {
	for {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		select {
		case <-quit:
			return 0
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// An identity git would only guess (a name from the environment, an
// address from the account and the machine) is not the person's: the
// commit is Conductor's, and nothing is written to any settings.
func TestOnlyAConfiguredIdentityIsThePersons(t *testing.T) {
	dir, in := project(t, "")
	in.GitEnv["GIT_AUTHOR_NAME"], in.GitEnv["GIT_COMMITTER_NAME"] = "Somebody", "Somebody"
	homeBefore := folderListing(t, os.Getenv("HOME"))
	mustFirstCommit(t, in)
	if got := gitOf(t, dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != "Conductor <conductor@noreply.invalid>|Conductor <conductor@noreply.invalid>" {
		t.Fatalf("the commit is %q", got)
	}
	if after := folderListing(t, os.Getenv("HOME")); after != homeBefore {
		t.Fatalf("the home folder changed:\n%s", after)
	}
	if cfg := gitOf(t, dir, "config", "--local", "--list"); strings.Contains(cfg, "user.") {
		t.Fatalf("an identity was written into the repository's settings:\n%s", cfg)
	}
}

// No hook runs for a first commit: hooks the person's own template puts in
// the new repository would fail and leave a mark.
func TestAFirstCommitRunsNoHook(t *testing.T) {
	program, mark := marker(t)
	template := t.TempDir()
	if err := os.Mkdir(filepath.Join(template, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"pre-commit", "commit-msg", "post-commit", "reference-transaction", "post-index-change"} {
		if err := os.Symlink(program, filepath.Join(template, "hooks", h)); err != nil {
			t.Fatal(err)
		}
	}
	dir, in := project(t, "[init]\n\ttemplateDir = "+template+"\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	mustFirstCommit(t, in)
	if _, err := os.Lstat(filepath.Join(dir, ".git", "hooks", "reference-transaction")); err != nil {
		t.Fatalf("the template's hooks are not in the repository: %v", err)
	}
	noEntry(t, mark)
}

// The folder's check answers a new folder as no refusal, a repository
// with no commit in the press's sentence, and makes nothing in either.
func TestTheFolderCheckMakesNothing(t *testing.T) {
	dir, _ := project(t, "")
	before := folderListing(t, dir)
	if st, err := GitState(t.Context(), dir); err != nil || st.Message != msgFirstCommit || st.InRepo {
		t.Fatalf("%+v %v", st, err)
	}
	if after := folderListing(t, dir); after != before {
		t.Fatalf("the check made %s", after)
	}
	gitOf(t, dir, "init", "-q")
	before = folderListing(t, dir)
	if st, err := GitState(t.Context(), dir); err != nil || st.Message != msgMakeItYourself || !st.InRepo || st.HasCommit {
		t.Fatalf("%+v %v", st, err)
	}
	if after := folderListing(t, dir); after != before {
		t.Fatalf("the check changed the repository:\n%s", after)
	}
}

// signerGone bounds the wait for a signer sent SIGKILL to be gone: the
// kernel's and init's work on a loaded machine, not a bound of the code's.
const signerGone = 30 * time.Second

// sshSigning skips the test on a git that cannot sign with ssh (before
// 2.34).
func sshSigning(t *testing.T) {
	t.Helper()
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		t.Skip("git is not installed")
	}
	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(string(out)), "git version "), "%d.%d", &major, &minor); err != nil || major < 2 || major == 2 && minor < 34 {
		t.Skipf("%s cannot sign with ssh", strings.TrimSpace(string(out)))
	}
}

// The folder's check reads a repository's head without reading an object:
// a repository prepared as a partial clone whose head names a missing
// object, judged by a git left to fetch it (as a git before 2.45 does), is
// byte for byte as it was after the check, after the press (which finds a
// head and makes nothing), and after the launch's own read of the commit
// is refused.
func TestTheCheckReadsTheHeadWithoutReadingAnObject(t *testing.T) {
	dir, in := project(t, "")
	gitOf(t, dir, "init", "-q")
	for _, kv := range [][2]string{{"core.repositoryformatversion", "1"}, {"extensions.partialClone", "origin"},
		{"remote.origin.url", filepath.Join(t.TempDir(), "gone")}, {"remote.origin.promisor", "true"}} {
		gitOf(t, dir, "config", kv[0], kv[1])
	}
	branch := gitOf(t, dir, "symbolic-ref", "HEAD")
	writeFile(t, filepath.Join(dir, ".git", filepath.FromSlash(branch)), "0123456789abcdef0123456789abcdef01234567\n")
	fetching := map[string]string{"GIT_NO_LAZY_FETCH": "0", "GIT_CONFIG_NOSYSTEM": "1"}
	before := folderListing(t, dir)
	same := func(after string) {
		t.Helper()
		if now := folderListing(t, dir); now != before {
			t.Fatalf("%s changed the repository:\n%s\nwas\n%s", after, now, before)
		}
	}
	if st, err := gitStateIn(t.Context(), dir, fetching); err != nil || !st.HasCommit {
		t.Fatalf("the check: %+v %v", st, err)
	}
	same("the folder's check")
	if _, refusal, err := repoStateIn(t.Context(), dir, fetching); err != nil || refusal != nil {
		t.Fatalf("the launch's check: %v %v", refusal, err)
	}
	same("the launch's check")
	in.GitEnv = fetching
	if fc, err := EnsureFirstCommit(t.Context(), in); err != nil || fc.Made {
		t.Fatalf("the press: %+v %v", fc, err)
	}
	same("the press")
	if c, err := headCommit(t.Context(), dir); err == nil {
		t.Fatalf("the launch read a commit %s", c)
	}
	same("the launch's read of its base")
}

// Where the repository is put in place by a make of .git and a rename over
// it, what comes into that .git or in its place between the two steps is
// kept: the commit is refused in words that say so, and the next press
// meets what is there.
func TestWhatComesBetweenTheMakeAndTheRenameIsKept(t *testing.T) {
	unsupported := func(*os.File, string, *os.File, string) error {
		return &os.LinkError{Op: "renameat2", Err: syscall.EINVAL}
	}
	const words = "Something was put at the folder's .git while Conductor put the new repository there"
	for _, theirs := range []string{"a file put into it", "a link in its place", "a file in its place"} {
		t.Run(theirs, func(t *testing.T) {
			dir, in := project(t, "")
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			elsewhere := t.TempDir()
			at := filepath.Join(dir, ".git")
			in.rename = unsupported
			in.hold = func(step string) {
				if step != "made" {
					return
				}
				switch theirs {
				case "a file put into it":
					writeFile(t, filepath.Join(at, "theirs"), "t\n")
				case "a link in its place":
					if err := os.Remove(at); err != nil {
						t.Error(err)
					}
					if err := os.Symlink(elsewhere, at); err != nil {
						t.Error(err)
					}
				default:
					if err := os.Remove(at); err != nil {
						t.Error(err)
					}
					writeFile(t, at, "gitdir: theirs\n")
				}
			}
			refused(t, in, words)
			kept := folderListing(t, dir)
			switch theirs {
			case "a file put into it":
				if b, err := os.ReadFile(filepath.Join(at, "theirs")); err != nil || string(b) != "t\n" {
					t.Fatalf("theirs changed: %q %v", b, err)
				}
			case "a link in its place":
				if to, err := os.Readlink(at); err != nil || to != elsewhere {
					t.Fatalf("their link changed: %q %v", to, err)
				}
			default:
				if b, err := os.ReadFile(at); err != nil || string(b) != "gitdir: theirs\n" {
					t.Fatalf("their file changed: %q %v", b, err)
				}
			}
			noEntry(t, filepath.Join(dir, ".conductor"))
			// The next press meets what is there, and makes nothing.
			in.hold = nil
			if _, err := EnsureFirstCommit(t.Context(), in); !errors.Is(err, ErrNotRepo) {
				t.Fatalf("the next press: %v", err)
			}
			if after := folderListing(t, dir); after != kept {
				t.Fatalf("the next press changed the folder:\n%s", after)
			}
		})
	}
}

// The folder's check and the press read the person's settings from the
// same places: a setting kept only where the server's environment says
// (here one that refuses a bare repository found above the folder) gives
// the two the same answer.
func TestTheCheckAndThePressReadTheSameSettings(t *testing.T) {
	_, in := project(t, "")
	delete(in.GitEnv, "GIT_CONFIG_GLOBAL")
	bare := t.TempDir()
	gitOf(t, bare, "init", "-q", "--bare")
	work := filepath.Join(bare, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, settings, "[safe]\n\tbareRepository = explicit\n")
	t.Setenv("GIT_CONFIG_GLOBAL", settings)
	st, err := GitState(t.Context(), work)
	if err != nil || !strings.Contains(st.Message, "safe.bareRepository") {
		t.Fatalf("the check does not read the person's settings: %+v %v", st, err)
	}
	pressed := opened(t, work, os.Getenv("HOME"))
	delete(pressed.GitEnv, "GIT_CONFIG_GLOBAL")
	_, err = EnsureFirstCommit(t.Context(), pressed)
	if err == nil || err.Error() != st.Message {
		t.Fatalf("the press said %v, the check %q", err, st.Message)
	}
	noEntry(t, filepath.Join(work, ".git"))
}
