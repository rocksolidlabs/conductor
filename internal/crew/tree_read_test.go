package crew

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
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
	workTree string
	env      map[string]string
	args     []string
	in, out  string
}

// recordTreeGit wraps the runner of a tree's read (treeGit) for the test:
// every command is recorded, then before is called (nil for none), which
// may stand in for the command by returning true and its error, and
// otherwise the command runs.
func recordTreeGit(t *testing.T, before func(c gitCall) (bool, error)) *[]gitCall {
	t.Helper()
	calls := &[]gitCall{}
	run := treeGit
	t.Cleanup(SetTreeGitForTest(func(ctx context.Context, workTree string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		c := gitCall{workTree: workTree, env: env, args: args}
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
		err := run(ctx, workTree, env, in, io.MultiWriter(w, &out), args...)
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

// A folder under the run's worktrees that holds no .git of its own, in
// the run's checkout, is not read: git looks for a repository in the
// tree alone (the ceiling is the folder that holds it) and answers that
// it is none, so the checkout around it is never read in its place; a
// linked worktree whose .git is gone is answered the same.
func TestATreeWithNoGitOfItsOwnIsNotRead(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	plain := filepath.Join(filepath.Dir(wt), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(plain, "new.txt"), "x\n")
	if err := os.Rename(filepath.Join(wt, ".git"), filepath.Join(repo, "dotgit-aside")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{plain, wt} {
		tr, err := OpenTree(repo, dir)
		if err != nil {
			t.Fatal(err)
		}
		read, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
		tr.Close()
		if !errors.Is(err, gitcli.ErrNotRepo) || read.Commit != "" || read.Content != "" {
			t.Errorf("%s: %+v %v, want gitcli.ErrNotRepo", dir, read, err)
		}
	}
}

// Every git command of a tree's read, the revision's and the classes',
// runs in the tree's opened directory with the work tree named
// (gitcli.RunTree takes no git directory), the folder that holds the
// tree as git's ceiling, no replacement object, the reader's own index,
// literal pathspecs (check-ignore aside), and the configuration forced.
func TestEveryCommandOfATreesReadRunsInTheTreeUnderItsCeiling(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
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
		if !sameDir(c.workTree, wt) || (runtime.GOOS == "linux") != strings.HasPrefix(c.workTree, "/proc/") ||
			c.env["GIT_CEILING_DIRECTORIES"] != filepath.Dir(wt) || c.env["GIT_NO_REPLACE_OBJECTS"] != "1" ||
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

// The ordinary read: a linked worktree with an edited file and a new one
// names the commit, the content plain git makes of the tree, and both
// paths.
func TestATreesReadNamesItsEditedAndNewFiles(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	writeFile(t, filepath.Join(wt, "new.txt"), "new\n")
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	read, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
	if err != nil || read.Commit != runGit(t, wt, "rev-parse", "HEAD") || read.Content != plainContent(t, wt) ||
		read.Changed != 2 || !slices.Equal(read.Paths, []string{"README", "new.txt"}) {
		t.Fatalf("the read: %+v %v", read, err)
	}
}

// A repository that sets core.splitIndex keeps nothing of the read: the
// index is one file in the reader's folder, and neither the tree's git
// directory nor the common one gains a name.
func TestASplitIndexRepositoryKeepsNothingOfTheRead(t *testing.T) {
	repo := newRepo(t)
	runGit(t, repo, "config", "core.splitIndex", "true")
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	dirs := []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "worktrees", "build.1")}
	before := gitDirNames(t, dirs...)
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	folder := t.TempDir()
	if read, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(folder, "index")}); err != nil || read.Changed != 1 {
		t.Fatalf("the read: %+v %v", read, err)
	}
	if after := gitDirNames(t, dirs...); !slices.Equal(before, after) {
		t.Errorf("the git directories after the read: %v, before %v", after, before)
	}
	if kept := gitDirNames(t, folder); !slices.Equal(kept, []string{filepath.Join(folder, "index")}) {
		t.Errorf("the reader's folder: %v", kept)
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
// index does.
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
	recordTreeGit(t, func(c gitCall) (bool, error) {
		if c.args[0] != "add" || ctx.Err() != nil {
			return false, nil
		}
		go func() {
			// The test's own wait for git to make the lock: a minute.
			seen := false
			for end := time.Now().Add(time.Minute); !seen && time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
				_, err := os.Lstat(ix.Path + ".lock")
				seen = err == nil
			}
			locked <- seen
			cancel()
		}()
		return true, gitcli.RunTree(ctx, c.workTree, c.env, input, io.Discard, "update-index", "--index-info")
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
