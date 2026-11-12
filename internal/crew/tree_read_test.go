package crew

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// linkedTree makes the worktree name of repo's run r-1 where the engine
// makes it, as it makes it (gitcli.AddWorktree), and returns its path.
func linkedTree(t *testing.T, repo, name string) string {
	t.Helper()
	wt := filepath.Join(repo, worktreesDir, "r-1", name)
	if err := gitcli.AddWorktree(t.Context(), repo, wt, "crew/r-1/"+name, "HEAD"); err != nil {
		t.Fatal(err)
	}
	return wt
}

// gitCall is a git command of a tree's read as its runner was given it,
// with what it was given on its input and what it wrote.
type gitCall struct {
	gitDir, workTree string
	env              map[string]string
	args             []string
	in, out          string
}

// recordTreeGit wraps the runner of a tree's read (treeGit) for the test:
// every command is recorded, then before is called (nil for none), which
// may stand in for the command by returning true and its error, and
// otherwise the command runs.
func recordTreeGit(t *testing.T, before func(c gitCall) (bool, error)) *[]gitCall {
	t.Helper()
	calls := &[]gitCall{}
	run := treeGit
	treeGit = func(ctx context.Context, gitDir, workTree string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		c := gitCall{gitDir: gitDir, workTree: workTree, env: env, args: args}
		if in != nil {
			b, err := io.ReadAll(in)
			if err != nil {
				return err
			}
			c.in, in = string(b), bytes.NewReader(b)
		}
		if before != nil {
			if stood, err := before(c); stood {
				*calls = append(*calls, c)
				return err
			}
		}
		var out bytes.Buffer
		err := run(ctx, gitDir, workTree, env, in, io.MultiWriter(w, &out), args...)
		c.out = out.String()
		*calls = append(*calls, c)
		return err
	}
	t.Cleanup(func() { treeGit = run })
	return calls
}

// sameDir reports whether the paths a and b name one directory.
func sameDir(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// A tree is reached from the run's folder one name at a time, each a
// directory and not a link: a link as the last name or as an earlier one
// is refused.
func TestOpenTreeReachesTheTreeOneNameAtATime(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	run, conductor := filepath.Dir(wt), filepath.Join(repo, ".conductor")
	for link, target := range map[string]string{filepath.Join(run, "last"): wt, filepath.Join(conductor, "earlier"): filepath.Join(conductor, "worktrees")} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, dir string
		err       error
	}{
		{"every name a directory", wt, nil},
		{"a link as the last name", filepath.Join(run, "last"), ErrTreeMoved},
		{"a link as an earlier name", filepath.Join(conductor, "earlier", "r-1", "build.1"), ErrTreeMoved},
		{"a folder that is not there", filepath.Join(run, "gone"), ErrTreeMoved},
		{"outside the run's folder", t.TempDir(), ErrTreeMoved},
	} {
		tr, err := OpenTree(t.Context(), repo, c.dir)
		if !errors.Is(err, c.err) {
			t.Errorf("%s: %v, want %v", c.name, err, c.err)
			continue
		}
		if err == nil {
			if !sameDir(tr.at, wt) {
				t.Errorf("%s: the read runs at %s, not the tree", c.name, tr.at)
			}
			tr.Close()
		}
	}
}

// A tree's read runs only in a worktree of the run's own repository,
// whose git directory is directly under that repository's worktrees and
// names the tree's .git back: no .git, a .git directory, a .git file
// naming any other git directory, and one its git directory does not name
// back are refused, and no git command runs in them.
func TestATreesReadRunsOnlyInAWorktreeOfTheRunsRepository(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	other := newRepo(t)
	foreign := filepath.Join(other, worktreesDir, "r-2", "build.1")
	if err := gitcli.AddWorktree(t.Context(), other, foreign, "crew/r-2/build.1", "HEAD"); err != nil {
		t.Fatal(err)
	}
	run := filepath.Dir(wt)
	made := func(name string, dotGit string) string {
		dir := filepath.Join(run, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if dotGit != "" {
			writeFile(t, filepath.Join(dir, ".git"), dotGit)
		}
		return dir
	}
	nested := made("nested", "")
	runGit(t, nested, "init", "-q")
	own, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	calls := recordTreeGit(t, nil)
	for _, c := range []struct {
		name, dir string
		err       error
	}{
		{"a linked worktree", wt, nil},
		{"no .git", made("plain", ""), ErrTreeNotWorktree},
		{"a .git directory", nested, ErrTreeNotWorktree},
		{"a .git file naming the common git directory", made("common", "gitdir: "+filepath.Join(repo, ".git")+"\n"), ErrTreeNotWorktree},
		{"a .git file naming another repository's worktree", made("foreign", "gitdir: "+filepath.Join(other, ".git", "worktrees", "build.1")+"\n"), ErrTreeNotWorktree},
		{"a .git file its git directory does not name back", made("copy", string(own)), ErrTreeNotWorktree},
	} {
		*calls = nil
		tr, err := OpenTree(t.Context(), repo, c.dir)
		if err == nil {
			if !sameDir(tr.gitDir, filepath.Join(repo, ".git", "worktrees", "build.1")) || !sameDir(tr.at, wt) {
				t.Errorf("%s: git directory %s, work tree %s", c.name, tr.gitDir, tr.at)
			}
			if _, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}); err != nil {
				t.Errorf("%s: the read: %v", c.name, err)
			}
			tr.Close()
		}
		if !errors.Is(err, c.err) || (c.err != nil) != (len(*calls) == 0) {
			t.Errorf("%s: %v (want %v), %d git commands in the tree", c.name, err, c.err, len(*calls))
		}
	}
}

// Every git command of a tree's read, the revision's and the classes',
// names the tree's git directory and its work tree outright and runs with
// no replacement object, in the reader's own index.
func TestEveryCommandOfATreesReadNamesItsGitDirectoryAndWorkTree(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	calls := recordTreeGit(t, nil)
	tr, err := OpenTree(t.Context(), repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	read, err := TreeRevision(t.Context(), tr, ix)
	if err != nil || read.Changed != 1 {
		t.Fatalf("the read: %+v %v", read, err)
	}
	if _, _, err := TreeClasses(t.Context(), tr, ix, read.Commit, []string{"README"}); err != nil {
		t.Fatal(err)
	}
	var subs []string
	for _, c := range *calls {
		subs = append(subs, c.args[0])
		if !sameDir(c.gitDir, filepath.Join(repo, ".git", "worktrees", "build.1")) || !sameDir(c.workTree, wt) ||
			c.env["GIT_NO_REPLACE_OBJECTS"] != "1" || c.env["GIT_INDEX_FILE"] != ix.Path {
			t.Errorf("git %v: git directory %s, work tree %s, environment %v", c.args, c.gitDir, c.workTree, c.env)
		}
	}
	if want := []string{"rev-parse", "read-tree", "add", "write-tree", "diff", "ls-tree", "check-ignore"}; !slices.Equal(subs, want) {
		t.Fatalf("the commands: %v, want %v", subs, want)
	}
}

// The classes are read at the commit the read recorded, for exactly the
// paths asked: HEAD moved after the read leaves them the read's; an
// ignored folder named by a path is one answer.
func TestTheClassesAreTheRecordedCommitsForTheGivenPaths(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, filepath.Join(repo, ".gitignore"), "build/\n")
	commitAll(t, repo, "ignore build")
	wt := linkedTree(t, repo, "build.1")
	if err := os.MkdirAll(filepath.Join(wt, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build/a.js", "build/b.js", "build/c.map", "new.txt", "later.txt"} {
		writeFile(t, filepath.Join(wt, name), name+"\n")
	}
	tr, err := OpenTree(t.Context(), repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	read, err := TreeRevision(t.Context(), tr, ix)
	if err != nil {
		t.Fatal(err)
	}
	// HEAD moves between the read and the classes: later.txt is tracked
	// at the new HEAD, not at the read's commit.
	calls := recordTreeGit(t, func(c gitCall) (bool, error) {
		if c.args[0] == "ls-tree" {
			runGit(t, wt, "add", "later.txt")
			runGit(t, wt, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "later")
		}
		return false, nil
	})
	paths := []string{"README", "build", "new.txt", "later.txt"}
	tracked, ignored, err := TreeClasses(t.Context(), tr, ix, read.Commit, paths)
	if err != nil {
		t.Fatal(err)
	}
	if runGit(t, wt, "rev-parse", "HEAD") == read.Commit {
		t.Fatal("HEAD did not move")
	}
	for _, c := range []struct {
		path             string
		tracked, ignored bool
	}{{"README", true, false}, {"build", false, true}, {"new.txt", false, false}, {"later.txt", false, false}} {
		if tracked[c.path] != c.tracked || ignored[c.path] != c.ignored {
			t.Errorf("%s: tracked %v ignored %v, want %v %v", c.path, tracked[c.path], ignored[c.path], c.tracked, c.ignored)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("the classes ran %d commands", len(*calls))
	}
	ls, check := (*calls)[0], (*calls)[1]
	if i := slices.Index(ls.args, "--"); i < 1 || ls.args[i-1] != read.Commit || !slices.Equal(ls.args[i+1:], paths) {
		t.Errorf("ls-tree: %v", ls.args)
	}
	if !slices.Equal(check.args, []string{"check-ignore", "-z", "--stdin"}) || check.in != strings.Join(paths, "\x00")+"\x00" || check.out != "build\x00" {
		t.Errorf("check-ignore: %v, in %q, out %q", check.args, check.in, check.out)
	}
}

// An answer about the paths asked is read within an entry and its bytes
// for each: past either nothing more is read.
func TestAnAnswerIsReadWithinWhatWasAsked(t *testing.T) {
	for _, c := range []struct {
		out   string
		paths []string
		over  bool
	}{
		{"a\x00b\x00", []string{"a", "b"}, false},
		{"b\x00", []string{"b"}, false},
		{"a\x00b\x00c\x00", []string{"a", "b"}, true},
		{"a\x00\x00\x00", []string{"a", ""}, true},
		{"abcd\x00", nil, true},
	} {
		l := &askedList{bytes: len("a\x00b\x00"), entries: 2}
		_, err := l.Write([]byte(c.out))
		if l.over != c.over || (err != nil) != c.over || !c.over && !slices.Equal(l.paths, c.paths) {
			t.Errorf("%q: paths %q over %v err %v", c.out, l.paths, l.over, err)
		}
	}
}
