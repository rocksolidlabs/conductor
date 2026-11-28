package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// paths asks GET /api/paths as the admin and returns the status and reply.
func (e *testEnv) paths(prefix, limit string) (int, map[string]any) {
	e.t.Helper()
	q := url.Values{"prefix": {prefix}}
	if limit != "" {
		q.Set("limit", limit)
	}
	resp, out := e.do("GET", "/api/paths?"+q.Encode(), adminToken, nil)
	return resp.StatusCode, out
}

// entryNames returns the names a paths reply lists, in order.
func entryNames(out map[string]any) []string {
	names := []string{}
	raw, _ := out["entries"].([]any)
	for _, e := range raw {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	return names
}

// entryGit returns the git marks of the entry named name.
func entryGit(out map[string]any, name string) (repo, commits bool) {
	raw, _ := out["entries"].([]any)
	for _, e := range raw {
		m := e.(map[string]any)
		if m["name"] == name {
			g := m["git"].(map[string]any)
			return g["repo"] == true, g["commits"] == true
		}
	}
	return false, false
}

// errorMessage returns the message of an error reply.
func errorMessage(out map[string]any) any {
	if m, ok := out["error"].(map[string]any); ok {
		return m["message"]
	}
	return nil
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// realRoot is the test root with its symlinks resolved, as the server reports it.
func realRoot(t *testing.T, root string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestPathsListsChildDirectoriesUnderTheRoots(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	mkdirs(t, root, "api", "app", "docs", ".hidden")
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory: all of its child directories, hidden ones and files left out.
	status, out := e.paths(root, "")
	if status != http.StatusOK || out["dir"] != root || out["truncated"] != false {
		t.Fatalf("%d %v", status, out)
	}
	if got := entryNames(out); !slices.Equal(got, []string{"api", "app", "docs"}) {
		t.Fatalf("entries: %v", got)
	}
	if p := out["entries"].([]any)[0].(map[string]any)["path"]; p != filepath.Join(root, "api") {
		t.Fatalf("path: %v", p)
	}
	// A trailing separator means the same.
	if _, out := e.paths(root+string(filepath.Separator), ""); len(entryNames(out)) != 3 {
		t.Fatalf("trailing separator: %v", out)
	}
	// A typed element: the children whose names start with it.
	if _, out := e.paths(filepath.Join(root, "ap"), ""); !slices.Equal(entryNames(out), []string{"api", "app"}) {
		t.Fatalf("prefix ap: %v", out)
	}
	// A hidden directory shows only once the dot is typed, the dot alone included.
	if _, out := e.paths(filepath.Join(root, ".h"), ""); !slices.Equal(entryNames(out), []string{".hidden"}) {
		t.Fatalf("prefix .h: %v", out)
	}
	if _, out := e.paths(root+string(filepath.Separator)+".", ""); out["dir"] != root || !slices.Equal(entryNames(out), []string{".hidden"}) {
		t.Fatalf("prefix .: %v", out)
	}
	// A deeper path that does not exist lists from the longest directory that does.
	if _, out := e.paths(filepath.Join(root, "nope", "deeper"), ""); out["dir"] != root || len(entryNames(out)) != 0 {
		t.Fatalf("missing deeper: %v", out)
	}
	// An empty prefix is the server's default working directory, the root here.
	if _, out := e.paths("", ""); out["dir"] != root || len(entryNames(out)) != 3 {
		t.Fatalf("empty prefix: %v", out)
	}
}

func TestPathsNeverLeaveTheRoots(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	outside := t.TempDir()
	mkdirs(t, outside, "secret")
	mkdirs(t, root, "inside")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "inside"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	// Wholly outside: refused, the message naming the roots (the route is
	// the admin's), which the picker shows as it is.
	want := "no directory in the prefix is under the allowed roots: " + e.root
	for _, prefix := range []string{outside, filepath.Join(root, ".."), "/", filepath.Join(root, "..", filepath.Base(outside), "sec")} {
		if status, out := e.paths(prefix, ""); status != http.StatusBadRequest || errorCode(out) != "invalid_cwd" || errorMessage(out) != want {
			t.Fatalf("%s: %d %v", prefix, status, out)
		}
	}
	// Through the link: the listing falls back to the root, and never shows the link or what is behind it.
	for _, prefix := range []string{filepath.Join(root, "escape"), filepath.Join(root, "escape", "sec"), filepath.Join(root, "escape") + string(filepath.Separator)} {
		status, out := e.paths(prefix, "")
		if status != http.StatusOK || out["dir"] != root || slices.Contains(entryNames(out), "escape") || slices.Contains(entryNames(out), "secret") {
			t.Fatalf("%s: %d %v", prefix, status, out)
		}
	}
	// A link that stays inside is listed; one that leaves is not.
	if _, out := e.paths(root, ""); !slices.Equal(entryNames(out), []string{"alias", "inside"}) {
		t.Fatalf("root: %v", entryNames(out))
	}
}

// With several roots, the refusal names each, in the configured order.
func TestPathsRefusalNamesEveryRoot(t *testing.T) {
	other := t.TempDir()
	e := newTestEnv(t, func(c *config.Config) { c.AllowedRoots = append(c.AllowedRoots, other) })
	status, out := e.paths("/", "")
	if want := "no directory in the prefix is under the allowed roots: " + e.root + ", " + other; status != http.StatusBadRequest || errorMessage(out) != want {
		t.Fatalf("%d %v", status, out)
	}
}

func TestPathsBoundsTheListing(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	for i := range 60 {
		mkdirs(t, root, fmt.Sprintf("d%02d", i))
	}
	// The cut says how many were left out (round 14: a cut list looked whole).
	if _, out := e.paths(root, ""); len(entryNames(out)) != 50 || out["truncated"] != true || out["more"] != float64(10) || out["moreUnknown"] != nil {
		t.Fatalf("default limit: %d %v more %v", len(entryNames(out)), out["truncated"], out["more"])
	}
	if _, out := e.paths(root, "5"); !slices.Equal(entryNames(out), []string{"d00", "d01", "d02", "d03", "d04"}) || out["truncated"] != true || out["more"] != float64(55) {
		t.Fatalf("limit 5: %v", out)
	}
	// What is typed narrows on the server, before the cut: the ten d5x, whole.
	if _, out := e.paths(filepath.Join(root, "d5"), ""); len(entryNames(out)) != 10 || out["truncated"] != false || out["more"] != nil {
		t.Fatalf("narrowed: %v", out)
	}
	if _, out := e.paths(root, "500"); len(entryNames(out)) != 50 {
		t.Fatalf("limit 500 is clamped: %v", len(entryNames(out)))
	}
	for _, bad := range []string{"0", "-1", "x"} {
		if status, out := e.paths(root, bad); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Fatalf("limit %s: %d %v", bad, status, out)
		}
	}
	if status, out := e.paths(strings.Repeat("a", 4097), ""); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Fatalf("long prefix: %d %v", status, out)
	}
	if status, out := e.paths(root+"\x00", ""); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Fatalf("NUL: %d %v", status, out)
	}
	if resp, out := e.do("GET", "/api/paths?prefix="+url.QueryEscape(root), "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %v", resp.StatusCode, out)
	}
}

// A listing reads at most maxPathScan entries of a directory, however many
// it holds, and says it may have missed some.
func TestPathsReadsABoundedNumberOfEntries(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	for i := range 30 {
		mkdirs(t, root, fmt.Sprintf("d%02d", i))
	}
	was := maxPathScan
	maxPathScan = 10
	t.Cleanup(func() { maxPathScan = was })
	if _, out := e.paths(root, ""); len(entryNames(out)) != 10 || out["truncated"] != true || out["moreUnknown"] != true {
		t.Fatalf("capped read: %v", out)
	}
}

// One listing asks git about each listed entry with a .git of its own and,
// once, about the directory for the others: never more than the limit, one
// call at a time.
func TestPathsAsksGitAtMostOncePerEntry(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	for i := range 60 {
		mkdirs(t, root, filepath.Join(fmt.Sprintf("r%02d", i), ".git"))
	}
	mkdirs(t, root, "zplain") // after the 50 listed, so the directory's marks are never needed
	var asked []string
	was := gitState
	gitState = func(_ context.Context, dir string) (crew.State, error) {
		asked = append(asked, dir)
		return crew.State{InRepo: true, HasCommit: dir != root}, nil
	}
	t.Cleanup(func() { gitState = was })
	_, out := e.paths(root, "")
	if len(entryNames(out)) != 50 || len(asked) != 50 || slices.Contains(asked, root) {
		t.Fatalf("all repositories: %d entries, asked %d: %v", len(entryNames(out)), len(asked), asked)
	}
	asked = nil
	_, out = e.paths(filepath.Join(root, "z"), "")
	if r, c := entryGit(out, "zplain"); !r || c || !slices.Equal(asked, []string{root}) {
		t.Fatalf("zplain carries the directory's marks: %v %v, asked %v", r, c, asked)
	}
}

// gitInit makes dir, then makes it a git repository, with one commit when
// commit is set. It skips the test when git is not installed. HOME is a
// temporary directory so that no configuration of the user running the tests
// applies.
func gitInit(t *testing.T, dir string, commit bool) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if commit {
		run("-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init")
	}
}

func TestPathsMarksGitRepositories(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	gitInit(t, filepath.Join(root, "repo"), true)
	gitInit(t, filepath.Join(root, "fresh"), false)
	mkdirs(t, root, "plain", filepath.Join("repo", "pkg"))
	_, out := e.paths(root, "")
	for name, want := range map[string][2]bool{"repo": {true, true}, "fresh": {true, false}, "plain": {false, false}} {
		if repo, commits := entryGit(out, name); repo != want[0] || commits != want[1] {
			t.Fatalf("%s: repo %v commits %v", name, repo, commits)
		}
	}
	// A child of a repository, with no .git of its own, carries its parent's marks.
	if _, out := e.paths(filepath.Join(root, "repo"), ""); func() bool { r, c := entryGit(out, "pkg"); return !r || !c }() {
		t.Fatalf("pkg below repo: %v", out)
	}
}

func TestGitCheckAnswersByTheLaunchRules(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	gitInit(t, filepath.Join(root, "repo"), true)
	gitInit(t, filepath.Join(root, "fresh"), false)
	mkdirs(t, root, "plain", filepath.Join("repo", "pkg"))
	check := func(cwd string) (int, map[string]any) {
		t.Helper()
		resp, out := e.do("GET", "/api/git/check?cwd="+url.QueryEscape(cwd), adminToken, nil)
		return resp.StatusCode, out
	}
	if status, out := check(filepath.Join(root, "repo", "pkg")); status != http.StatusOK || out["inRepo"] != true || out["toplevel"] != filepath.Join(root, "repo") || out["hasCommit"] != true {
		t.Fatalf("repo: %d %v", status, out)
	}
	// A new folder is no refusal (the operator's launch makes its first commit); a repository with no commit is refused as the press
	// refuses it; the check makes nothing in either.
	if _, out := check(filepath.Join(root, "fresh")); out["inRepo"] != true || out["hasCommit"] != false || out["message"] != "This folder has no commit yet. Make its first commit yourself, then launch" {
		t.Fatalf("fresh: %v", out)
	}
	if _, out := check(filepath.Join(root, "plain")); out["inRepo"] != false || out["hasCommit"] != false || !strings.Contains(out["message"].(string), "makes the folder's first commit") {
		t.Fatalf("plain: %v", out)
	}
	if _, err := os.Lstat(filepath.Join(root, "plain", ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the check made a repository in plain: %v", err)
	}
	if n := strings.TrimSpace(gitRun(t, filepath.Join(root, "fresh"), "rev-list", "--all", "--count")); n != "0" {
		t.Fatalf("the check made a commit in fresh: %s", n)
	}
	// Empty is the default working directory; outside the roots is refused as a launch would.
	if _, out := check(""); out["inRepo"] != false {
		t.Fatalf("default cwd: %v", out)
	}
	if status, out := check(t.TempDir()); status != http.StatusBadRequest || errorCode(out) != "invalid_cwd" {
		t.Fatalf("outside: %d %v", status, out)
	}
	if resp, _ := e.do("GET", "/api/git/check?cwd="+url.QueryEscape(root), "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
}

// gitCheck asks GET /api/git/check as the admin and returns the status and reply.
func (e *testEnv) gitCheck(cwd string) (int, map[string]any) {
	e.t.Helper()
	resp, out := e.do("GET", "/api/git/check?cwd="+url.QueryEscape(cwd), adminToken, nil)
	return resp.StatusCode, out
}

// mazeDepth is how deep linkMaze goes: deep enough that
// filepath.EvalSymlinks takes about a second on the maze (its cost grows
// with the square of the depth), shallow enough that the maze's longest
// link, "../" mazeDepth times, fits in macOS's PATH_MAX of 1024 bytes.
const mazeDepth = 300

// linkMaze makes, outside the roots, a symbolic link whose resolution walks
// depth directories down and back up, again and again: the kernel gives up
// on it after 40 links (ELOOP; 32 on macOS) in milliseconds, while
// filepath.EvalSymlinks walks 255 of them, each the whole depth down, for
// about a second at mazeDepth. It returns the link, which the test checks
// the kernel refuses. Where the system cannot hold a link that long, the
// test is skipped.
func linkMaze(t *testing.T, depth int) string {
	t.Helper()
	dir := t.TempDir()
	down := filepath.Join(slices.Repeat([]string{"n"}, depth)...)
	if err := os.MkdirAll(filepath.Join(dir, down), 0o755); err != nil {
		skipTooLong(t, err)
		t.Fatal(err)
	}
	if err := os.Symlink(strings.Repeat("../", depth)+"loop", filepath.Join(dir, down, "up")); err != nil {
		skipTooLong(t, err)
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(down, "up"), filepath.Join(dir, "loop")); err != nil {
		skipTooLong(t, err)
		t.Fatal(err)
	}
	maze := filepath.Join(dir, "loop")
	if _, err := os.Stat(maze); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("the kernel does not refuse the maze with ELOOP: %v", err)
	}
	return maze
}

// skipTooLong skips the test when err says a path or link is longer than
// the system holds.
func skipTooLong(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, syscall.ENAMETOOLONG) {
		t.Skipf("the system cannot hold the maze: %v", err)
	}
}

// A link that leads to no directory (a loop, a chain too long to follow, a
// file) is left out at the kernel's word, without walking it.
func TestPathsLeavesOutLinksToNoDirectoryQuickly(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	mkdirs(t, root, "real")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	maze := linkMaze(t, mazeDepth)
	links := map[string]string{"self": filepath.Join(root, "self"), "file": filepath.Join(root, "notes.txt")}
	for i := range 4 { // each would cost EvalSymlinks about a second: 4 s in all, past the bound below
		links[fmt.Sprintf("maze%d", i)] = maze
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	status, out := e.paths(root, "")
	if took := time.Since(start); status != http.StatusOK || !slices.Equal(entryNames(out), []string{"real"}) || out["truncated"] != false || took > 2*time.Second {
		t.Fatalf("%d %v in %v", status, out, took)
	}
}

// A prefix that runs through such a link answers from the root in time,
// whatever follows the link.
func TestPathsPrefixThroughALinkLoopAnswersInTime(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	if err := os.Symlink(linkMaze(t, mazeDepth), filepath.Join(root, "maze")); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(append([]string{root, "maze"}, slices.Repeat([]string{"x"}, 10)...)...)
	start := time.Now()
	status, out := e.paths(prefix, "")
	if took := time.Since(start); status != http.StatusOK || out["dir"] != root || len(entryNames(out)) != 0 || took > 2*time.Second {
		t.Fatalf("%d %v in %v", status, out, took)
	}
	// The git check's cwd is refused as quickly: well within the second
	// EvalSymlinks would take.
	start = time.Now()
	status, out = e.gitCheck(prefix)
	if took := time.Since(start); status != http.StatusBadRequest || errorCode(out) != "invalid_cwd" || took > 400*time.Millisecond {
		t.Fatalf("check: %d %v in %v", status, out, took)
	}
}

// A listing stops when its context ends (the client went away, or the
// deadline passed), in the walk of the prefix too.
func TestPathsStopsWhenTheContextEnds(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	if err := os.Symlink(linkMaze(t, mazeDepth), filepath.Join(root, "maze")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	_, err := e.srv.listPaths(ctx, filepath.Join(root, "maze", "x", "y"), maxPathEntries, scopeRoots)
	if took := time.Since(start); !errors.Is(err, context.Canceled) || took > time.Second {
		t.Fatalf("%v in %v", err, took)
	}
}

// At most 50 links that lead nowhere usable are looked at in one listing:
// at the next link, the listing stops and says it may have missed some.
func TestPathsCapsTheLinksItCannotUse(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	mkdirs(t, root, "zz")
	broken := func(name string) {
		t.Helper()
		if err := os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 50 {
		broken(fmt.Sprintf("b%02d", i))
	}
	if _, out := e.paths(root, ""); !slices.Equal(entryNames(out), []string{"zz"}) || out["truncated"] != false {
		t.Fatalf("50 broken links: %v", out)
	}
	broken("b50")
	if _, out := e.paths(root, ""); len(entryNames(out)) != 0 || out["truncated"] != true {
		t.Fatalf("51 broken links: %v", out)
	}
}

// Past the deadline the listing keeps its entries, unmarked, asks git no
// more, and says it is truncated: a client can tell a mark left out from a
// directory that is no repository.
func TestPathsKeepsItsEntriesPastTheDeadline(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	mkdirs(t, root, filepath.Join("a", ".git"), filepath.Join("b", ".git"), "c")
	var asked []string
	was := gitState
	gitState = func(ctx context.Context, dir string) (crew.State, error) {
		asked = append(asked, dir)
		<-ctx.Done()
		return crew.State{}, ctx.Err()
	}
	t.Cleanup(func() { gitState = was })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	reply, err := e.srv.listPaths(ctx, root, maxPathEntries, scopeRoots)
	if err != nil || len(reply.Entries) != 3 || len(asked) != 1 || !reply.Truncated {
		t.Fatalf("%+v %v, asked %v", reply, err, asked)
	}
	for _, en := range reply.Entries {
		if en.Git.Repo || en.Git.Commits {
			t.Fatalf("%s is marked: %+v", en.Name, en.Git)
		}
	}
}

// The git check's one call answers 500 git_failed when its context ends.
func TestGitCheckFailsPastItsDeadline(t *testing.T) {
	e := newTestEnv(t, nil)
	was := gitState
	gitState = func(ctx context.Context, _ string) (crew.State, error) {
		<-ctx.Done()
		return crew.State{}, ctx.Err()
	}
	t.Cleanup(func() { gitState = was })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := httptest.NewRecorder()
	e.srv.handleGitCheck(rec, httptest.NewRequestWithContext(ctx, "GET", "/api/git/check?cwd="+url.QueryEscape(e.root), nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "git_failed") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A link is marked by the directory it leads to, not by the one it is in.
func TestPathsMarksALinkByItsTarget(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	gitInit(t, filepath.Join(root, "repo"), true)
	mkdirs(t, root, "plain", filepath.Join("repo", "pkg"))
	if err := os.Symlink(filepath.Join(root, "repo", "pkg"), filepath.Join(root, "into")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "plain"), filepath.Join(root, "repo", "out")); err != nil {
		t.Fatal(err)
	}
	if _, out := e.paths(root, ""); func() bool { r, c := entryGit(out, "into"); return !r || !c }() {
		t.Fatalf("into, a link into the repository: %v", out)
	}
	_, out := e.paths(filepath.Join(root, "repo"), "")
	if r, c := entryGit(out, "out"); r || c {
		t.Fatalf("out, a link out of the repository: %v", out)
	}
	if r, c := entryGit(out, "pkg"); !r || !c {
		t.Fatalf("pkg: %v", out)
	}
}

// A directory under a root that the server cannot read lists nothing.
func TestPathsListsNothingOfAnUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a user that a directory's mode keeps out")
	}
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	locked := filepath.Join(root, "locked")
	mkdirs(t, root, filepath.Join("locked", "inner"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	status, out := e.paths(locked, "")
	if status != http.StatusOK || out["dir"] != locked || len(entryNames(out)) != 0 || out["truncated"] != false {
		t.Fatalf("%d %v", status, out)
	}
}

// A FIFO in place of the directory (swapped in after its stat) fails to open
// at once instead of waiting for a writer.
func TestPathsReadRefusesAFIFO(t *testing.T) {
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo is not installed")
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if out, err := exec.Command(mkfifo, fifo).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := readCandidates(fifo, "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was read as a directory")
		}
	case <-time.After(5 * time.Second):
		// A writer lets the waiting open return, so that the goroutine ends.
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("opening the FIFO waited for a writer")
	}
}

// Exactly maxPathScan entries are read whole; one more is truncated.
func TestPathsTruncatesOnlyPastTheScanCap(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	for i := range 10 {
		mkdirs(t, root, fmt.Sprintf("d%02d", i))
	}
	was := maxPathScan
	maxPathScan = 10
	t.Cleanup(func() { maxPathScan = was })
	if _, out := e.paths(root, ""); len(entryNames(out)) != 10 || out["truncated"] != false {
		t.Fatalf("exactly the cap: %v", out)
	}
	mkdirs(t, root, "d10")
	if _, out := e.paths(root, ""); len(entryNames(out)) != 10 || out["truncated"] != true {
		t.Fatalf("one past the cap: %v", out)
	}
}

// Without git on the server, nothing is marked and the check says why.
func TestPathsAndGitCheckWithoutGit(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	mkdirs(t, root, filepath.Join("repo", ".git"), "plain")
	t.Setenv("PATH", t.TempDir())
	status, out := e.paths(root, "")
	if status != http.StatusOK || !slices.Equal(entryNames(out), []string{"plain", "repo"}) {
		t.Fatalf("%d %v", status, out)
	}
	for _, name := range []string{"plain", "repo"} {
		if r, c := entryGit(out, name); r || c {
			t.Fatalf("%s is marked: %v", name, out)
		}
	}
	status, out = e.gitCheck(root)
	if _, named := out["toplevel"]; status != http.StatusOK || out["inRepo"] != false || out["hasCommit"] != false || out["message"] != crew.ErrNoGit.Error() || named {
		t.Fatalf("check: %d %v", status, out)
	}
}

// The check names the top of the repository only when it is under the
// allowed roots: core.worktree can make it any directory, and a root can lie
// inside a repository.
func TestGitCheckOmitsAToplevelOutsideTheRoots(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	repo := filepath.Join(root, "repo")
	gitInit(t, repo, true)
	outside := realRoot(t, t.TempDir())
	cmd := exec.Command("git", "-C", repo, "config", "core.worktree", outside)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v %s", err, out)
	}
	if status, out := e.gitCheck(repo); status != http.StatusOK || out["inRepo"] != true || out["hasCommit"] != true || out["toplevel"] != nil {
		t.Fatalf("core.worktree outside: %d %v", status, out)
	}
	outer := realRoot(t, t.TempDir())
	gitInit(t, outer, true)
	inner := filepath.Join(outer, "sub")
	mkdirs(t, outer, "sub")
	e2 := newTestEnv(t, func(c *config.Config) { c.AllowedRoots, c.DefaultCwd = []string{inner}, inner })
	if status, out := e2.gitCheck(inner); status != http.StatusOK || out["inRepo"] != true || out["toplevel"] != nil {
		t.Fatalf("a root inside a repository: %d %v", status, out)
	}
}

// A .conductor that is a symbolic link is checked in the launch's words.
func TestGitCheckReportsAWorktreesLink(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	repo := filepath.Join(root, "repo")
	gitInit(t, repo, true)
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, ".conductor")); err != nil {
		t.Fatal(err)
	}
	if status, out := e.gitCheck(repo); status != http.StatusOK || !strings.Contains(fmt.Sprint(out["message"]), "symbolic link") {
		t.Fatalf("%d %v", status, out)
	}
}

// pathsIn is paths with a scope.
func (e *testEnv) pathsIn(prefix, scope string) (int, map[string]any) {
	e.t.Helper()
	resp, out := e.do("GET", "/api/paths?"+url.Values{"prefix": {prefix}, "scope": {scope}}.Encode(), adminToken, nil)
	return resp.StatusCode, out
}

// Listing outside the roots is off unless paths.browse says any; scope must
// be roots or any; roots answers as no scope does.
func TestPathsScopeIsRootsUnlessSwitchedOn(t *testing.T) {
	e := newTestEnv(t, nil)
	code, out := e.pathsIn(e.root, "any")
	if code != http.StatusForbidden || out["error"].(map[string]any)["code"] != "browse_off" {
		t.Fatalf("any while off: %d %v", code, out)
	}
	if code, out := e.pathsIn(e.root, "bogus"); code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("bogus: %d %v", code, out)
	}
	a, outA := e.pathsIn(e.root, "roots")
	b, outB := e.paths(e.root, "")
	if a != http.StatusOK || b != http.StatusOK || fmt.Sprint(outA) != fmt.Sprint(outB) {
		t.Fatalf("roots %d %v, none %d %v", a, outA, b, outB)
	}
}

// With paths.browse any, scope=any lists directories outside the roots, a
// link out of them included, and hidden ones only when a dot is typed; the
// roots scope still refuses them.
func TestPathsScopeAnyListsOutsideTheRoots(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Paths.Browse = config.BrowseAny })
	outside := t.TempDir()
	for _, d := range []string{"secret", ".hidden"} {
		if err := os.Mkdir(filepath.Join(outside, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(e.root, "escape")); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(outside)
	code, out := e.pathsIn(outside+string(filepath.Separator), "any")
	if code != http.StatusOK || out["dir"] != real || !slices.Equal(entryNames(out), []string{"secret"}) {
		t.Fatalf("outside: %d %v", code, out)
	}
	if _, out := e.pathsIn(outside+string(filepath.Separator)+".", "any"); !slices.Contains(entryNames(out), ".hidden") {
		t.Fatalf("hidden with a dot: %v", out)
	}
	if code, _ := e.pathsIn("/", "any"); code != http.StatusOK {
		t.Fatalf("/: %d", code)
	}
	if _, out := e.pathsIn(e.root+string(filepath.Separator), "any"); !slices.Contains(entryNames(out), "escape") {
		t.Fatalf("the link out is listed under any: %v", out)
	}
	if _, out := e.pathsIn(e.root+string(filepath.Separator), "roots"); slices.Contains(entryNames(out), "escape") {
		t.Fatalf("the link out is listed under roots: %v", out)
	}
	if code, out := e.paths(outside, ""); code != http.StatusBadRequest {
		t.Fatalf("outside without scope: %d %v", code, out)
	}
}

// The stat comes before the links are resolved under any too: a link loop
// answers at once.
func TestPathsScopeAnyAnswersInTimeThroughALinkLoop(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Paths.Browse = config.BrowseAny })
	root := e.root
	if err := os.Symlink(filepath.Join(root, "loop"), filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, out := e.pathsIn(filepath.Join(root, "loop", "x", "y"), "any")
	if took := time.Since(start); code != http.StatusOK || took > 2*time.Second {
		t.Fatalf("%d %v in %s", code, out, took)
	}
}
