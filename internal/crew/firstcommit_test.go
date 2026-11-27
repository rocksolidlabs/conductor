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

// A new project's first commit (issue 1179, firstcommit.go): the two
// situations it is made in, what goes in, whose name is on it, what is
// never run, and that every other folder is left exactly as it was. Each
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

// butObjects is a listing of a .git without its object store.
func butObjects(listing string) string {
	var keep []string
	for line := range strings.SplitSeq(listing, "\n") {
		if !strings.HasPrefix(line, "objects") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
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

// A repository nested in the folder is left out, never added as a link.
func TestANestedRepositoryIsLeftOut(t *testing.T) {
	for _, outer := range []bool{false, true} {
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
		if outer {
			gitOf(t, dir, "init", "-q")
		}
		mustFirstCommit(t, in)
		if got := committed(t, dir); !slices.Equal(got, []string{"main.go"}) {
			t.Fatalf("outer repository %v: committed %v", outer, got)
		}
		if links := gitOf(t, dir, "ls-files", "-s"); strings.Contains(links, "160000") {
			t.Fatalf("a nested repository was added as a link:\n%s", links)
		}
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
		}, errNotInRepo.Error()},
		{"a .git folder with no refs/", func(t *testing.T, dir, _ string) {
			gitOf(t, dir, "init", "-q")
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			gitOf(t, dir, "add", "a.txt")
			personCommits(t, dir, "mine")
			gitOf(t, dir, "pack-refs", "--all")
			if err := os.RemoveAll(filepath.Join(dir, ".git", "refs")); err != nil {
				t.Fatal(err)
			}
		}, errNotInRepo.Error()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, in := project(t, "")
			elsewhere := t.TempDir()
			writeFile(t, filepath.Join(dir, "mine.txt"), "m\n")
			c.make(t, dir, elsewhere)
			before, target := folderListing(t, dir), folderListing(t, elsewhere)
			if st, err := GitState(t.Context(), dir); err != nil || st.Message != c.words {
				t.Fatalf("the folder's check: %+v %v", st, err)
			}
			refused(t, in, c.words)
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
	mustFirstCommit(t, in)
	noEntry(t, stale)
}

// (B) A repository just started keeps its .git; its tree goes in as it
// stands, what was staged with the rest, and the person's index is the
// commit's after it; Conductor's own index is gone.
func TestARepositoryJustStartedGetsItsTreeCommitted(t *testing.T) {
	for _, staged := range []bool{false, true} {
		dir, in := project(t, "")
		gitOf(t, dir, "init", "-q")
		writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
		writeFile(t, filepath.Join(dir, "b.txt"), "b\n")
		if staged {
			gitOf(t, dir, "add", "a.txt")
		}
		if st, err := GitState(t.Context(), dir); err != nil || st.Message != msgFirstCommit {
			t.Fatalf("the folder's check: %+v %v", st, err)
		}
		fc := mustFirstCommit(t, in)
		if fc.Init {
			t.Fatal("a repository that was there is reported made")
		}
		if got := committed(t, dir); !slices.Equal(got, []string{"a.txt", "b.txt"}) {
			t.Fatalf("staged %v: committed %v", staged, got)
		}
		clean(t, dir)
		if got := gitOf(t, dir, "ls-files"); got != "a.txt\nb.txt" {
			t.Fatalf("the person's index holds %q", got)
		}
		noEntry(t, filepath.Join(dir, ".git", ownIndex))
	}
}

// (B) A repository whose own configuration holds a key off the list is
// refused, and the program the key names is not run: a signer, a signing
// switch, an include, a filter, a work tree, a hooks path, an alias, a
// monitor, format version 1, an extension, bare. Nothing in it changes.
func TestAKeyOffTheListIsRefusedAndRunsNothing(t *testing.T) {
	program, mark := marker(t)
	elsewhere := t.TempDir()
	included := filepath.Join(t.TempDir(), "included")
	writeFile(t, included, "[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = "+program+"\n")
	cases := map[string]string{
		"a signer":         "[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = " + program + "\n",
		"a signing switch": "[commit]\n\tgpgSign = true\n",
		"an include":       "[include]\n\tpath = " + included + "\n",
		"a filter":         "[filter \"x\"]\n\tclean = " + program + "\n\tsmudge = " + program + "\n",
		"a work tree":      "[core]\n\tworktree = " + elsewhere + "\n",
		"a hooks path":     "[core]\n\thooksPath = " + filepath.Dir(program) + "\n",
		"an alias":         "[alias]\n\tst = !" + program + "\n",
		"a monitor":        "[core]\n\tfsmonitor = " + program + "\n",
		"format version 1": "[core]\n\trepositoryformatversion = 1\n",
		"an extension":     "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tworktreeConfig = true\n",
		"bare":             "[core]\n\tbare\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			dir, in := project(t, "")
			gitOf(t, dir, "init", "-q")
			writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
			writeFile(t, filepath.Join(dir, ".gitattributes"), "* filter=x\n")
			for _, h := range []string{"pre-commit", "reference-transaction", "post-index-change"} {
				if err := os.Symlink(program, filepath.Join(filepath.Dir(program), h)); err != nil && !errors.Is(err, fs.ErrExist) {
					t.Fatal(err)
				}
			}
			cfg := filepath.Join(dir, ".git", "config")
			b, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, cfg, string(b)+extra)
			before := folderListing(t, dir)
			if st, err := GitState(t.Context(), dir); err != nil || st.Message == msgFirstCommit {
				t.Fatalf("the folder's check: %+v %v", st, err)
			}
			if _, err := EnsureFirstCommit(t.Context(), in); !errors.Is(err, ErrNotRepo) {
				t.Fatalf("not refused: %v", err)
			}
			noEntry(t, mark)
			if after := folderListing(t, dir); after != before {
				t.Fatalf("the repository changed:\n%s\nwas\n%s", after, before)
			}
		})
	}
}

// (B) A repository with references but no commit on its branch, and a
// folder below the top of a repository just started, are refused with
// nothing made.
func TestReferencesButNoCommitAndAFolderBelowTheTopAreRefused(t *testing.T) {
	dir, in := project(t, "")
	gitOf(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	gitOf(t, dir, "add", "a.txt")
	personCommits(t, dir, "mine")
	gitOf(t, dir, "checkout", "-q", "--orphan", "fresh")
	before := folderListing(t, dir)
	refused(t, in, msgMakeItYourself)
	if after := folderListing(t, dir); after != before {
		t.Fatalf("the repository changed:\n%s\nwas\n%s", after, before)
	}

	top, _ := project(t, "")
	gitOf(t, top, "init", "-q")
	sub := filepath.Join(top, "app")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "app.txt"), "a\n")
	before = folderListing(t, top)
	if st, err := GitState(t.Context(), sub); err != nil || st.Message != msgMakeItYourself {
		t.Fatalf("the folder's check: %+v %v", st, err)
	}
	refused(t, opened(t, sub, os.Getenv("HOME")), msgMakeItYourself)
	if after := folderListing(t, top); after != before {
		t.Fatalf("the repository changed:\n%s\nwas\n%s", after, before)
	}
}

// (B) A commit the person makes while the first commit is under way is
// theirs: Conductor's is dropped, the answer is that none was made, and
// the launch goes on from theirs.
func TestACommitThatAppearsMeanwhileIsKept(t *testing.T) {
	dir, in := project(t, "")
	gitOf(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "b\n")
	var theirs string
	in.hold = func(step string) {
		if step == "commit" {
			gitOf(t, dir, "add", "b.txt")
			personCommits(t, dir, "theirs")
			theirs = gitOf(t, dir, "rev-parse", "HEAD")
		}
	}
	fc, err := EnsureFirstCommit(t.Context(), in)
	if err != nil || fc.Made {
		t.Fatalf("%+v %v", fc, err)
	}
	if head := gitOf(t, dir, "rev-parse", "HEAD"); head != theirs || gitOf(t, dir, "rev-list", "--count", "HEAD") != "1" {
		t.Fatalf("HEAD %s, theirs %s", head, theirs)
	}
	if got := gitOf(t, dir, "ls-files"); got != "b.txt" {
		t.Fatalf("their index holds %q", got)
	}
	noEntry(t, filepath.Join(dir, ".git", ownIndex))
	if err := CheckRepo(t.Context(), dir); err != nil {
		t.Fatalf("the launch cannot go on from theirs: %v", err)
	}
}

// The bound: more than MaxFirstCommitFiles files or MaxFirstCommitBytes
// bytes on the first measure, or on what the add staged after files came
// meanwhile, is refused with the fact and the way on, and nothing is made.
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
	many(t, dir, MaxFirstCommitFiles+1)
	refused(t, in, "This folder has 5,001 files and no commit yet. Make its first commit yourself, then launch")
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
	refused(t, in, "This folder has 200 MB of files and no commit yet")
	noEntry(t, filepath.Join(big, ".git"))

	// Files that came between the measure and the add: the tree staged is
	// measured again, before the commit.
	for _, started := range []bool{false, true} {
		dir, in := project(t, "")
		if started {
			gitOf(t, dir, "init", "-q")
		}
		writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
		if started {
			gitOf(t, dir, "add", "a.txt")
		}
		repoBefore := ""
		if started {
			repoBefore = butObjects(folderListing(t, filepath.Join(dir, ".git")))
		}
		in.hold = func(step string) {
			if step == "measured" {
				many(t, dir, MaxFirstCommitFiles+1)
			}
		}
		refused(t, in, "This folder has 5,002 files and no commit yet")
		if started {
			// The repository as it was, but for objects nobody refers to.
			if after := butObjects(folderListing(t, filepath.Join(dir, ".git"))); after != repoBefore {
				t.Fatalf("the repository changed:\n%s\nwas\n%s", after, repoBefore)
			}
			noEntry(t, filepath.Join(dir, ".git", ownIndex))
		} else {
			noEntry(t, filepath.Join(dir, ".git"))
			noEntry(t, filepath.Join(dir, ".conductor"))
		}
	}
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

// The person's own signing settings sign the commit: their signing program
// runs (its mark) and the commit carries its signature.
func TestThePersonsSigningSettingsSignTheCommit(t *testing.T) {
	bin := t.TempDir()
	mark := filepath.Join(bin, "signed")
	sign := filepath.Join(bin, "sign")
	writeFile(t, sign, "#!/bin/sh\ncat >/dev/null\ntouch "+mark+"\nprintf '\\n[GNUPG:] SIG_CREATED D 1 8 00 0 0\\n' >&2\nprintf -- '-----BEGIN PGP SIGNATURE-----\\n\\nfake\\n-----END PGP SIGNATURE-----\\n'\n")
	if err := os.Chmod(sign, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[user]\n\tname = Ada\n\temail = ada@example.com\n[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = " + sign + "\n"
	for _, started := range []bool{false, true} {
		dir, in := project(t, cfg)
		if started {
			gitOf(t, dir, "init", "-q")
		}
		fc := mustFirstCommit(t, in)
		if _, err := os.Lstat(mark); err != nil {
			t.Fatalf("the person's signer did not run: %v", err)
		}
		if raw := gitOf(t, dir, "cat-file", "commit", fc.Commit); !strings.Contains(raw, "gpgsig -----BEGIN PGP SIGNATURE-----") {
			t.Fatalf("the commit is not signed:\n%s", raw)
		}
		os.Remove(mark)
	}
}

// A signing program of the person's that never ends and holds git's output
// (an ssh signer git hands its own standard output) is ended at the
// commit's bound with its whole process group: the first commit is
// refused, nothing is left running, nothing is made, and the next call is
// not held.
func TestASignerThatNeverEndsIsEndedAtTheBound(t *testing.T) {
	bin := t.TempDir()
	pidFile := filepath.Join(bin, "pid")
	sign := filepath.Join(bin, "sign")
	writeFile(t, sign, "#!/bin/sh\necho $$ > "+pidFile+"\nexec sleep 600\n")
	if err := os.Chmod(sign, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, in := project(t, "[user]\n\tname = Ada\n\temail = ada@example.com\n\tsigningKey = key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOmR test\n[commit]\n\tgpgSign = true\n[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tprogram = "+sign+"\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	in.CommitWait = 2 * time.Second
	done := make(chan error, 1)
	go func() {
		_, err := EnsureFirstCommit(t.Context(), in)
		done <- err
	}()
	pid, err := pidOf(t, pidFile, done)
	if pid == 0 {
		t.Fatalf("the signer never ran; the first commit answered %v", err)
	}
	if err = <-done; !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), "did not finish within its bound") {
		t.Fatalf("refused: %v", err)
	}
	for syscall.Kill(pid, 0) == nil {
		if t.Context().Err() != nil {
			t.Fatalf("the signer %d is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
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

// pidOf is the pid a program wrote to path, once it has; 0 and the call's
// answer when the call answered first, which then ran no such program.
func pidOf(t *testing.T, path string, done <-chan error) (int, error) {
	t.Helper()
	for {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 0 {
				t.Fatalf("pid %q", b)
			}
			return pid, nil
		}
		select {
		case err := <-done:
			return 0, err
		case <-t.Context().Done():
			t.Fatal("no pid written")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// An identity git would only guess (a name from the environment, an
// address from the account and the machine) is not the person's: the
// commit is Conductor's, and nothing is written to any settings.
func TestOnlyAConfiguredIdentityIsThePersons(t *testing.T) {
	for _, started := range []bool{false, true} {
		dir, in := project(t, "")
		if started {
			gitOf(t, dir, "init", "-q")
		}
		in.GitEnv["GIT_AUTHOR_NAME"], in.GitEnv["GIT_COMMITTER_NAME"] = "Somebody", "Somebody"
		cfgBefore := folderListing(t, os.Getenv("HOME"))
		mustFirstCommit(t, in)
		if got := gitOf(t, dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != "Conductor <conductor@noreply.invalid>|Conductor <conductor@noreply.invalid>" {
			t.Fatalf("the commit is %q", got)
		}
		if after := folderListing(t, os.Getenv("HOME")); after != cfgBefore {
			t.Fatalf("the home folder changed:\n%s", after)
		}
		if cfg := gitOf(t, dir, "config", "--local", "--list"); strings.Contains(cfg, "user.") {
			t.Fatalf("an identity was written into the repository's settings:\n%s", cfg)
		}
	}
}

// No hook of the folder runs for a first commit in a repository just
// started: its hooks would fail and leave a mark.
func TestAFirstCommitRunsNoHook(t *testing.T) {
	program, mark := marker(t)
	dir, in := project(t, "")
	gitOf(t, dir, "init", "-q")
	for _, h := range []string{"pre-commit", "commit-msg", "post-commit", "reference-transaction", "post-index-change"} {
		if err := os.Symlink(program, filepath.Join(dir, ".git", "hooks", h)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	mustFirstCommit(t, in)
	noEntry(t, mark)
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

// The folder's check answers a new folder and a repository just started
// as no refusal, and makes nothing in either.
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
	if st, err := GitState(t.Context(), dir); err != nil || st.Message != msgFirstCommit || !st.InRepo || st.HasCommit {
		t.Fatalf("%+v %v", st, err)
	}
	if after := folderListing(t, dir); after != before {
		t.Fatalf("the check changed the repository:\n%s", after)
	}
}

// The keys a repository just started may hold: what this machine's git
// init writes, and nothing that names a program or a place.
func TestKnownKeysAreWhatGitInitWrites(t *testing.T) {
	dir, _ := project(t, "")
	gitOf(t, dir, "init", "-q")
	raw := gitOf(t, dir, "config", "--file", filepath.Join(dir, ".git", "config"), "--no-includes", "-z", "--list")
	if !knownConfig(raw) {
		t.Fatalf("git init's own configuration is refused: %q", raw)
	}
	for key, ok := range map[string]bool{
		"remote.origin.url\nhttps://example.com/r.git": true, "remote.a.b.fetch\n+refs/heads/*:refs/remotes/a.b/*": true,
		"branch.main.remote\norigin": true, "branch.main.merge\nrefs/heads/main": true, "user.name\nAda": true,
		"init.defaultbranch\ntrunk": true, "core.repositoryformatversion\n1": false, "core.bare\ntrue": false,
		"core.bare": false, "remote.url\nx": false, "remote.origin.pushurl\nx": false, "core.sshcommand\nx": false,
		"gpg.program\nx": false, "include.path\nx": false, "includeif.gitdir:/x/.path\ny": false, "user.signingkey\nk": false,
	} {
		if got := knownConfig(key + "\x00"); got != ok {
			t.Errorf("%q: known %v, want %v", key, got, ok)
		}
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
