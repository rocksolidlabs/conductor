package crew

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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
	workTree, objects string
	env               map[string]string
	args              []string
	in, out           string
}

// recordTreeGit wraps the runner of a tree's read (treeGit) for the test:
// every command is recorded, then before is called (nil for none), which
// may stand in for the command by returning true and its error, and
// otherwise the command runs.
func recordTreeGit(t *testing.T, before func(c gitCall) (bool, error)) *[]gitCall {
	t.Helper()
	calls := &[]gitCall{}
	run := treeGit
	t.Cleanup(SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		c := gitCall{workTree: workTree, objects: objects, env: env, args: args}
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
		err := run(ctx, workTree, objects, env, in, io.MultiWriter(w, &out), args...)
		c.out = out.String()
		*calls = append(*calls, c)
		return err
	}))
	return calls
}

// configOf is the configuration env sets (GIT_CONFIG_COUNT and its keys).
func configOf(env map[string]string) map[string]string {
	cfg := map[string]string{}
	n, _ := strconv.Atoi(env["GIT_CONFIG_COUNT"])
	for i := range n {
		cfg[env["GIT_CONFIG_KEY_"+strconv.Itoa(i)]] = env["GIT_CONFIG_VALUE_"+strconv.Itoa(i)]
	}
	return cfg
}

// plainContent is the tree id plain git makes of everything dir holds, in
// an index of its own: read-tree HEAD, add -A, write-tree.
func plainContent(t *testing.T, dir string) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "index")
	var id []byte
	for _, args := range [][]string{{"read-tree", "HEAD"}, {"add", "-A"}, {"write-tree"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		id = out
	}
	return strings.TrimSpace(string(id))
}

// gitDirNames lists the names in each of dirs.
func gitDirNames(t *testing.T, dirs ...string) []string {
	t.Helper()
	var names []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			names = append(names, filepath.Join(d, e.Name()))
		}
	}
	return names
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
		tr, err := OpenTree(repo, c.dir)
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

// pipeAtOpen sets the walk's seam (beforeTreeOpen) to put a named pipe in
// place of the folder name, at path, between its look and its open, the
// first time the walk reaches it; the folder is put back when the test
// ends.
func pipeAtOpen(t *testing.T, name, path string) {
	t.Helper()
	aside := path + "-aside"
	placed := false
	t.Cleanup(SetTreeOpenForTest(func(n string) {
		if n != name || placed {
			return
		}
		placed = true
		if err := os.Rename(path, aside); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(func() {
		if placed {
			_ = os.Remove(path)
			_ = os.Rename(aside, path)
		}
	})
}

// openTreeWithin is OpenTree(root, dir), which must be back within the
// test's own wait (a minute): one that is not fails the test in words,
// and is then let go by a writer opening the pipe at fifo.
func openTreeWithin(t *testing.T, root, dir, fifo string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		tr, err := OpenTree(root, dir)
		if err == nil {
			tr.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Minute):
	}
	t.Errorf("OpenTree of %s was not back within a minute: its open waits on the pipe at %s", dir, fifo)
	if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		w.Close()
	}
	return <-done
}

// The walk opens each name as a folder only: a named pipe put in place of
// the tree's last name, or of an earlier one, between the name's look and
// its open is refused at once (ErrTreeMoved), never waited on.
func TestTheWalkOpensFoldersAsFolders(t *testing.T) {
	for _, c := range []struct{ name, at string }{{"the last name", "build.1"}, {"an earlier name", "r-1"}} {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepo(t)
			wt := linkedTree(t, repo, "build.1")
			path := filepath.Join(repo, worktreesDir, "r-1")
			if c.at == "build.1" {
				path = wt
			}
			pipeAtOpen(t, c.at, path)
			if err := openTreeWithin(t, repo, wt, path); !errors.Is(err, ErrTreeMoved) {
				t.Fatalf("OpenTree with a pipe at %s: %v, want ErrTreeMoved", c.at, err)
			}
		})
	}
}

// The run's own folder is opened as a folder only too: a named pipe put in
// its place just before its open (the walk's seam, called with the run's
// folder before it is opened) fails the open at once with the system's
// error (not a directory), never waited on, and the tree is not read.
func TestTheWalkOpensTheRunsFolderAsAFolder(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	pipeAtOpen(t, repo, repo)
	err := openTreeWithin(t, repo, wt, repo)
	if fi, lerr := os.Lstat(repo); lerr != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("the walk never put the pipe at the run's folder: %v %v", fi, lerr)
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("OpenTree with a pipe at the run's folder: %v, want the system's not a directory", err)
	}
}

// A folder under the run's worktrees with no .git at its top, in the
// run's checkout, is not read: git is given the tree's own .git, looks
// for no repository and answers that it is none, so the checkout around
// it is never read in its place. So for a plain folder, a linked worktree
// whose .git is moved aside and a bare repository at the folder's own
// top; a .git that is an empty file is refused by git as no gitfile, and
// the tree is not read either.
func TestATreeWithNoGitOfItsOwnIsNotRead(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	empty := linkedTree(t, repo, "build.2")
	plain, bare := filepath.Join(filepath.Dir(wt), "plain"), filepath.Join(filepath.Dir(wt), "bare")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(plain, "new.txt"), "x\n")
	runGit(t, repo, "clone", "-q", "--bare", repo, bare)
	if err := os.Rename(filepath.Join(wt, ".git"), filepath.Join(repo, "dotgit-aside")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(empty, ".git"), "")
	for _, c := range []struct {
		name, dir string
		notRepo   bool
	}{{"a plain folder", plain, true}, {"a .git moved aside", wt, true}, {"a bare repository", bare, true}, {"a .git that is an empty file", empty, false}} {
		tr, err := OpenTree(repo, c.dir)
		if err != nil {
			t.Fatal(err)
		}
		read, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
		tr.Close()
		if err == nil || c.notRepo != errors.Is(err, gitcli.ErrNotRepo) || read.Commit != "" || read.Content != "" {
			t.Errorf("%s: %+v %v, want unread (gitcli.ErrNotRepo: %v)", c.name, read, err, c.notRepo)
		}
	}
}

// The folder that holds a tree with no .git is renamed between OpenTree
// and the read's first command: git, given the opened tree's own .git,
// answers that it is no repository, and never reads the checkout around
// the folder's new place.
func TestATreeWhoseFolderIsRenamedAfterItsOpenIsNotRead(t *testing.T) {
	repo := newRepo(t)
	run := filepath.Join(repo, worktreesDir, "r-1")
	plain := filepath.Join(run, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(plain, "new.txt"), "x\n")
	tr, err := OpenTree(repo, plain)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	renamed := false
	recordTreeGit(t, func(c gitCall) (bool, error) {
		if !renamed {
			renamed = true
			if err := os.Rename(run, run+"-moved"); err != nil {
				t.Error(err)
			}
		}
		return false, nil
	})
	read, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
	if !renamed || err == nil || (runtime.GOOS == "linux" && !errors.Is(err, gitcli.ErrNotRepo)) || read.Commit != "" || read.Content != "" {
		t.Fatalf("the read after the rename: %+v %v, want unread", read, err)
	}
}

// A tree whose path holds a colon (its run's folder is named with one) is
// read as any other tree, and with its .git moved aside is not read.
func TestATreeWhosePathHoldsAColonIsReadAsAnyOther(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(repo, worktreesDir, "r:1", "build.1")
	if err := gitcli.AddWorktree(t.Context(), repo, wt, "crew/r-1/build.1", "HEAD"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	read, err := TreeRevision(t.Context(), tr, ix)
	if err != nil || read.Content != plainContent(t, wt) || !slices.Equal(read.Paths, []string{"README"}) {
		t.Fatalf("the read: %+v %v", read, err)
	}
	if err := os.Rename(filepath.Join(wt, ".git"), filepath.Join(repo, "dotgit-aside")); err != nil {
		t.Fatal(err)
	}
	if read, err := TreeRevision(t.Context(), tr, ix); !errors.Is(err, gitcli.ErrNotRepo) || read.Commit != "" || read.Content != "" {
		t.Fatalf("the read with .git aside: %+v %v, want gitcli.ErrNotRepo", read, err)
	}
}

// wrapGit puts a git first on PATH that writes each argument list it is
// given (each argument NUL-ended) and whether GIT_CEILING_DIRECTORIES is
// in its environment to the returned file, one line each, and then runs
// the real git with them.
func wrapGit(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "argv")
	script := "#!/bin/sh\n{ for a in \"$@\"; do printf '%s\\0' \"$a\"; done; printf 'ceiling=%s\\n' \"${GIT_CEILING_DIRECTORIES+set}\"; } >>'" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CEILING_DIRECTORIES", "")
	os.Unsetenv("GIT_CEILING_DIRECTORIES")
	return log
}

// gitArgv is what wrapGit wrote: each git's arguments and whether a
// ceiling was in its environment.
type gitArgv struct {
	args    []string
	ceiling bool
}

func readGitArgv(t *testing.T, log string) []gitArgv {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out []gitArgv
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		i := strings.LastIndexByte(line, 0)
		out = append(out, gitArgv{args: strings.Split(line[:max(i, 0)], "\x00"), ceiling: line[i+1:] != "ceiling="})
	}
	return out
}

// Every git command of a tree's read, the revision's and the classes',
// the look-up of the object store before each and the filter look-up
// before each that has one, runs in the tree's opened directory with the
// tree's own .git and the work tree named outright (-C <opened>
// --git-dir=<opened>/.git --work-tree=<opened>) and no ceiling in its
// environment; each command with no replacement object, the reader's own
// index and its folder of objects, literal pathspecs (check-ignore
// aside), and the configuration forced.
func TestEveryCommandOfATreesReadNamesTheTreesOwnGit(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	log := wrapGit(t)
	calls := recordTreeGit(t, nil)
	tr, err := OpenTree(repo, wt)
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
	loc := []string{"-C", tr.at, "--git-dir=" + filepath.Join(tr.at, ".git"), "--work-tree=" + tr.at}
	var gits []string
	for _, g := range readGitArgv(t, log) {
		i := slices.Index(g.args, "-C")
		if i < 0 || len(g.args) < i+5 || !slices.Equal(g.args[i:i+4], loc) || g.ceiling {
			t.Errorf("git %q: not at %v, or a ceiling (%v)", g.args, loc, g.ceiling)
			continue
		}
		gits = append(gits, g.args[i+4])
	}
	// Every command has the look-up of the object store (rev-parse) before
	// it; rev-parse takes no filter look-up, every other command one
	// after the store's.
	if want := []string{"rev-parse", "rev-parse", "rev-parse", "config", "read-tree", "rev-parse", "config", "add", "rev-parse", "config", "write-tree",
		"rev-parse", "config", "diff", "rev-parse", "config", "ls-tree", "rev-parse", "config", "check-ignore"}; !slices.Equal(gits, want) {
		t.Errorf("the gits run: %v, want %v", gits, want)
	}
	forced := map[string]string{"core.ignoreStat": "false", "core.checkStat": "default", "core.trustctime": "true",
		"core.sparseCheckout": "false", "core.splitIndex": "false"}
	if modesKept(t.TempDir()) {
		forced["core.fileMode"] = "true"
	}
	var subs []string
	for _, c := range *calls {
		subs = append(subs, c.args[0])
		literal := "1"
		if c.args[0] == "check-ignore" {
			literal = ""
		}
		_, ceiling := c.env["GIT_CEILING_DIRECTORIES"]
		if !sameDir(c.workTree, wt) || (runtime.GOOS == "linux") != strings.HasPrefix(c.workTree, "/proc/") || c.objects != ix.Objects() ||
			ceiling || c.env["GIT_NO_REPLACE_OBJECTS"] != "1" ||
			c.env["GIT_INDEX_FILE"] != ix.Path || c.env["GIT_LITERAL_PATHSPECS"] != literal || !maps.Equal(configOf(c.env), forced) {
			t.Errorf("git %v: work tree %s, environment %v", c.args, c.workTree, c.env)
		}
	}
	if want := []string{"rev-parse", "read-tree", "add", "write-tree", "diff", "ls-tree", "check-ignore"}; !slices.Equal(subs, want) {
		t.Fatalf("the commands: %v, want %v", subs, want)
	}
	if args := (*calls)[0].args; !slices.Equal(args, []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}) {
		t.Errorf("rev-parse: %v", args)
	}
	for _, c := range (*calls)[1:5] {
		if c.args[0] != "add" && c.args[0] != "write-tree" && c.args[len(c.args)-1] != read.Commit {
			t.Errorf("git %v does not name the recorded commit %s", c.args, read.Commit)
		}
	}
}

// The ordinary read: a linked worktree (its .git a gitfile) and a tree
// with a .git directory of its own, each with an edited file and a new
// one, name the commit, the content plain git makes of the tree, and both
// paths.
func TestATreesReadNamesItsEditedAndNewFiles(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	clone := filepath.Join(filepath.Dir(wt), "clone")
	runGit(t, repo, "clone", "-q", repo, clone)
	for _, dir := range []string{wt, clone} {
		writeFile(t, filepath.Join(dir, "README"), "edited\n")
		writeFile(t, filepath.Join(dir, "new.txt"), "new\n")
		tr, err := OpenTree(repo, dir)
		if err != nil {
			t.Fatal(err)
		}
		read, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
		tr.Close()
		if err != nil || read.Commit != runGit(t, dir, "rev-parse", "HEAD") || read.Content != plainContent(t, dir) ||
			read.Changed != 2 || !slices.Equal(read.Paths, []string{"README", "new.txt"}) {
			t.Fatalf("the read of %s: %+v %v", dir, read, err)
		}
	}
}

// A repository that sets core.splitIndex, and one that also sets
// splitIndex.maxPercentChange to 0, keeps nothing of ten reads of one
// index, the tree edited before each: the index is one file in the
// reader's folder, and neither the tree's git directory nor the common
// one gains a name.
func TestASplitIndexRepositoryKeepsNothingOfTheRead(t *testing.T) {
	for _, cfg := range [][]string{{"core.splitIndex", "true"}, {"core.splitIndex", "true", "splitIndex.maxPercentChange", "0"}} {
		repo := newRepo(t)
		for i := 0; i < len(cfg); i += 2 {
			runGit(t, repo, "config", cfg[i], cfg[i+1])
		}
		wt := linkedTree(t, repo, "build.1")
		dirs := []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "worktrees", "build.1")}
		before := gitDirNames(t, dirs...)
		tr, err := OpenTree(repo, wt)
		if err != nil {
			t.Fatal(err)
		}
		folder := t.TempDir()
		ix := &TreeIndex{Path: filepath.Join(folder, "index")}
		for n := range 10 {
			writeFile(t, filepath.Join(wt, "README"), "edited "+strconv.Itoa(n)+"\n")
			if read, err := TreeRevision(t.Context(), tr, ix); err != nil || read.Changed != 1 {
				t.Fatalf("%v: read %d: %+v %v", cfg, n, read, err)
			}
		}
		tr.Close()
		if after := gitDirNames(t, dirs...); !slices.Equal(before, after) {
			t.Errorf("%v: the git directories after ten reads: %v, before %v", cfg, after, before)
		}
		if kept := gitDirNames(t, folder); !slices.Equal(kept, []string{filepath.Join(folder, "index"), ix.Objects()}) {
			t.Errorf("%v: the reader's folder: %v", cfg, kept)
		}
	}
}

// A read of a tree whose attributes git warns about at every command
// (3,000 lines, about 300 KB of warnings each) is answered as plain git
// makes the tree: the warnings are dropped past their bound.
func TestATreesReadIsItsAnswerWhateverGitWarns(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, ".gitattributes"), strings.Repeat("!x text\n", 3000))
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	read, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
	if err != nil || read.Content != plainContent(t, wt) || !slices.Equal(read.Paths, []string{".gitattributes"}) {
		t.Fatalf("the read: %+v %v", read, err)
	}
}

// The look at the executable bit answers as before on a folder of the
// test's own and leaves nothing of itself there.
func TestTheLookAtTheExecutableBitLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if got, want := modesKept(dir), runtime.GOOS != "windows"; got != want {
		t.Errorf("modesKept: %v, want %v", got, want)
	}
	if left := gitDirNames(t, dir); len(left) != 0 {
		t.Errorf("left by the look: %v", left)
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
	tr, err := OpenTree(repo, wt)
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

// A read cut while git holds the index's lock leaves the lock; the next
// read of that index drops the index and its lock and names the content,
// as a read in a new index does.
func TestALockACutReadLeftIsDroppedByTheNextRead(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	// The runner stands in for add -A: it takes the index's lock, as git
	// does, and the read is cut there.
	ctx, cancel := context.WithCancel(t.Context())
	recordTreeGit(t, func(c gitCall) (bool, error) {
		if c.args[0] != "add" || ctx.Err() != nil {
			return false, nil
		}
		writeFile(t, ix.Path+".lock", "")
		cancel()
		return true, ctx.Err()
	})
	if read, err := TreeRevision(ctx, tr, ix); !errors.Is(err, context.Canceled) || read.Content != "" {
		t.Fatalf("the cut read: %+v %v", read, err)
	}
	if _, err := os.Stat(ix.Path + ".lock"); err != nil {
		t.Fatalf("the cut read left no lock: %v", err)
	}
	read, err := TreeRevision(t.Context(), tr, ix)
	fresh, ferr := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
	if err != nil || ferr != nil || read.Content == "" || read.Content != fresh.Content || read.Changed != 1 {
		t.Fatalf("the next read: %+v %v (a read in a new index: %+v %v)", read, err, fresh, ferr)
	}
}

// A read cut while a real git holds the index's lock: at the read's add
// -A the runner runs git update-index --index-info in the read's index,
// which takes the lock and then waits on its input; once the lock file
// is there the read's context ends, git is killed and its lock stays.
// The next read of that index names the content, as a read in a new
// index does. What it waits on: the lock file git makes (looked for
// until it is there, within the test's own minute), then the cut read's
// return, which waits for the killed git. The look for the lock ends with
// the test, also when the test fails: a cleanup tells it to stop and waits
// for it.
func TestALockARealGitHeldIsDroppedByTheNextRead(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	input, held, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer held.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	locked := make(chan bool, 1)
	stop, looking := make(chan struct{}), &sync.WaitGroup{}
	t.Cleanup(func() {
		close(stop)
		looking.Wait()
	})
	recordTreeGit(t, func(c gitCall) (bool, error) {
		if c.args[0] != "add" || ctx.Err() != nil {
			return false, nil
		}
		looking.Go(func() {
			defer cancel()
			// The test's own wait for git to make the lock: a minute, or
			// until the test ends.
			end := time.After(time.Minute)
			for {
				if _, err := os.Lstat(ix.Path + ".lock"); err == nil {
					locked <- true
					return
				}
				select {
				case <-stop:
					locked <- false
					return
				case <-end:
					locked <- false
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
		})
		return true, gitcli.RunTree(ctx, c.workTree, c.objects, c.env, input, io.Discard, "update-index", "--index-info")
	})
	if read, err := TreeRevision(ctx, tr, ix); !errors.Is(err, context.Canceled) || read.Content != "" {
		t.Fatalf("the cut read: %+v %v", read, err)
	}
	if !<-locked {
		t.Fatal("git made no lock")
	}
	if _, err := os.Stat(ix.Path + ".lock"); err != nil {
		t.Fatalf("the cut read left no lock: %v", err)
	}
	read, err := TreeRevision(t.Context(), tr, ix)
	fresh, ferr := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
	if err != nil || ferr != nil || read.Content == "" || read.Content != fresh.Content || read.Changed != 1 {
		t.Fatalf("the next read: %+v %v (a read in a new index: %+v %v)", read, err, fresh, ferr)
	}
}

// storeFiles counts the files of repo's object store.
func storeFiles(t *testing.T, repo string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(repo, ".git", "objects"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A tree's read writes nothing into the repository's object store: read
// in one kept index as the tree is clean, then with a changed file, a new
// file, a deleted file and an ignored file, each read leaves the store
// with as many files as before it and answers what plain git makes of the
// tree; the object of the file a read is the first to hash is in the
// index's own folder.
func TestATreesReadWritesNothingIntoTheRepositorysStore(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, filepath.Join(repo, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(repo, "gone.txt"), "gone\n")
	commitAll(t, repo, "more")
	wt := linkedTree(t, repo, "build.1")
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	all := []string{"README", "gone.txt", "new.txt"}
	// made is the file whose object the read is the first to make: plain
	// git, run after the read for its answer, writes it into the store.
	for _, c := range []struct {
		name, made string
		change     func()
		paths      []string
	}{
		{"clean", "", func() {}, nil},
		{"a changed file", "README", func() { writeFile(t, filepath.Join(wt, "README"), "edited\n") }, []string{"README"}},
		{"a new file", "new.txt", func() { writeFile(t, filepath.Join(wt, "new.txt"), "new\n") }, []string{"README", "new.txt"}},
		{"a deleted file", "", func() { _ = os.Remove(filepath.Join(wt, "gone.txt")) }, all},
		{"an ignored file", "", func() { writeFile(t, filepath.Join(wt, "x.log"), "log\n") }, all},
	} {
		c.change()
		before := storeFiles(t, repo)
		read, err := TreeRevision(t.Context(), tr, ix)
		if after := storeFiles(t, repo); err != nil || after != before {
			t.Fatalf("%s: the store holds %d files after the read, %d before (%v)", c.name, after, before, err)
		}
		if c.made != "" {
			id := runGit(t, wt, "hash-object", c.made)
			if _, err := os.Stat(filepath.Join(ix.Objects(), id[:2], id[2:])); err != nil {
				t.Errorf("%s: %s's object in the index's folder: %v", c.name, c.made, err)
			}
		}
		if read.Commit != runGit(t, wt, "rev-parse", "HEAD") || read.Content != plainContent(t, wt) || read.Changed != len(c.paths) || !slices.Equal(read.Paths, c.paths) {
			t.Fatalf("%s: the read: %+v", c.name, read)
		}
	}
}

// A read cut while git writes its objects: at the read's add -A the
// runner runs git index-pack --stdin in the read's index and folder, which
// makes its temporary pack there and then waits on its input, a pack's
// header with nothing of its one object; once that file is there the
// read's context ends and git is killed. The repository's store holds the
// files it held before, and the next read of that index starts from an
// empty folder and names the content, as a read in a new index does. What
// it waits on: the pack file git makes (looked for in the folder and in
// the store until it is there, within the test's own minute), then the
// cut read's return, which waits for the killed git. The look ends with
// the test, also when it fails.
func TestAReadCutWhileGitWritesLeavesNothingInTheStore(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	input, held, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer held.Close()
	if _, err := held.Write([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01")); err != nil {
		t.Fatal(err)
	}
	before := storeFiles(t, repo)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writing := make(chan bool, 1)
	stop, looking := make(chan struct{}), &sync.WaitGroup{}
	t.Cleanup(func() {
		close(stop)
		looking.Wait()
	})
	var atStart []string
	recordTreeGit(t, func(c gitCall) (bool, error) {
		if ctx.Err() != nil && c.args[0] == "rev-parse" && atStart == nil {
			atStart = append([]string{}, gitDirNames(t, c.objects)...)
		}
		if c.args[0] != "add" || ctx.Err() != nil {
			return false, nil
		}
		looking.Go(func() {
			defer cancel()
			end := time.After(time.Minute)
			for {
				made, _ := filepath.Glob(filepath.Join(c.objects, "pack", "tmp_pack_*"))
				inStore, _ := filepath.Glob(filepath.Join(repo, ".git", "objects", "pack", "tmp_pack_*"))
				if len(made)+len(inStore) > 0 {
					writing <- true
					return
				}
				select {
				case <-stop:
					writing <- false
					return
				case <-end:
					writing <- false
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
		})
		return true, gitcli.RunTree(ctx, c.workTree, c.objects, c.env, input, io.Discard, "index-pack", "--stdin")
	})
	if read, err := TreeRevision(ctx, tr, ix); !errors.Is(err, context.Canceled) || read.Content != "" {
		t.Fatalf("the cut read: %+v %v", read, err)
	}
	if !<-writing {
		t.Fatal("git made no pack")
	}
	if after := storeFiles(t, repo); after != before {
		t.Fatalf("the store holds %d files after the cut read, %d before", after, before)
	}
	read, err := TreeRevision(t.Context(), tr, ix)
	fresh, ferr := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
	if atStart == nil || len(atStart) != 0 {
		t.Fatalf("the index's folder as the next read began: %v", atStart)
	}
	if err != nil || ferr != nil || read.Content == "" || read.Content != fresh.Content || read.Changed != 1 {
		t.Fatalf("the next read: %+v %v (a read in a new index: %+v %v)", read, err, fresh, ferr)
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
