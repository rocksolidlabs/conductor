package crew

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/gitcli/gitclitest"
)

// newRepo makes a git repository with one commit holding README (three
// lines) and returns its path. It skips the test when git is not installed.
// HOME is a temporary directory, so that no configuration of the user running
// the tests (hooks, signing) applies to the test's git or the engine's.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "README"), "one\ntwo\nthree\n")
	commitAll(t, dir, "init")
	return dir
}

// runGit runs git -C dir args and returns its output; a failure fails the test.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// AddWorktree makes the branch from HEAD and checks it out at the path, whose
// parent directories it makes; a branch that exists already is an error.
func TestAddWorktreeMakesTheBranchAndThePath(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, ".conductor", "worktrees", "api-sweep-1a2b3c4d", "lead")
	if err := AddWorktree(t.Context(), repo, path, "crew/api-sweep-1a2b3c4d/lead"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(path, "README")); err != nil || string(b) != "one\ntwo\nthree\n" {
		t.Fatalf("README in the worktree: %q %v", b, err)
	}
	if branch := runGit(t, path, "rev-parse", "--abbrev-ref", "HEAD"); branch != "crew/api-sweep-1a2b3c4d/lead" {
		t.Fatalf("the worktree is on %q", branch)
	}
	if head, fork := runGit(t, repo, "rev-parse", "HEAD"), runGit(t, path, "rev-parse", "HEAD"); head != fork {
		t.Fatalf("the branch starts at %s, HEAD is %s", fork, head)
	}
	if list := runGit(t, repo, "worktree", "list"); !strings.Contains(list, path) {
		t.Fatalf("git worktree list:\n%s", list)
	}
	// The branch exists now: a second worktree cannot take its name.
	err := AddWorktree(t.Context(), repo, filepath.Join(repo, ".conductor", "worktrees", "other", "lead"), "crew/api-sweep-1a2b3c4d/lead")
	if err == nil || errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("the same branch twice: %v", err)
	}
}

// With no git on PATH, making a worktree says that git is missing
// (ErrNoGit), not that the directory is no repository.
func TestWithoutGitWorktreesSaySo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	if err := CheckRepo(t.Context(), dir); !errors.Is(err, ErrNoGit) || errors.Is(err, ErrNotRepo) {
		t.Fatalf("CheckRepo: %v", err)
	}
	if err := AddWorktree(t.Context(), dir, filepath.Join(dir, "wt"), "crew/run/lead"); !errors.Is(err, ErrNoGit) || errors.Is(err, ErrNotRepo) {
		t.Fatalf("AddWorktree: %v", err)
	}
}

// A directory without a .git is no repository to make worktrees of, and
// neither is a repository without a commit to branch from. Nothing is made.
func TestAddWorktreeNeedsARepository(t *testing.T) {
	newRepo(t) // skips without git
	plain := t.TempDir()
	path := filepath.Join(plain, ".conductor", "worktrees", "run", "lead")
	if err := AddWorktree(t.Context(), plain, path, "crew/run/lead"); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("AddWorktree in a plain directory: %v", err)
	}
	if err := CheckRepo(t.Context(), plain); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("CheckRepo of a plain directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plain, ".conductor")); !os.IsNotExist(err) {
		t.Fatalf("something was made: %v", err)
	}

	empty := t.TempDir()
	runGit(t, empty, "init", "-q")
	if err := CheckRepo(t.Context(), empty); !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("CheckRepo of a repository without a commit: %v", err)
	}
	repo := newRepo(t)
	if err := CheckRepo(t.Context(), repo); err != nil {
		t.Fatalf("CheckRepo of a repository: %v", err)
	}
	// A directory inside a repository is one too, and worktrees of the
	// repository are made from it.
	sub := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckRepo(t.Context(), sub); err != nil {
		t.Fatalf("CheckRepo of a directory in a repository: %v", err)
	}
	subPath := filepath.Join(sub, ".conductor", "worktrees", "run", "lead")
	if err := AddWorktree(t.Context(), sub, subPath, "crew/run/lead"); err != nil {
		t.Fatalf("AddWorktree from a directory in a repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(subPath, "README")); err != nil {
		t.Fatalf("the worktree holds the whole repository: %v", err)
	}
}

// DiffStat counts the lines the worktree adds and removes since base, the
// commit it started from: commits on the branch and uncommitted changes to
// tracked files count; untracked files and commits made on the main branch
// since do not.
func TestDiffStatCountsTheBranchChanges(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, ".conductor", "worktrees", "run", "core")
	if err := AddWorktree(t.Context(), repo, path, "crew/run/core"); err != nil {
		t.Fatal(err)
	}
	base, err := headCommit(t.Context(), path)
	if err != nil || base != runGit(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("headCommit: %q %v", base, err)
	}
	if added, removed, err := DiffStat(t.Context(), path, base); err != nil || added != 0 || removed != 0 {
		t.Fatalf("before any commit: +%d -%d %v", added, removed, err)
	}
	// One line changed and two added in README, three lines in a new file.
	writeFile(t, filepath.Join(path, "README"), "one\n2\nthree\nfour\nfive\n")
	writeFile(t, filepath.Join(path, "users.go"), "a\nb\nc\n")
	commitAll(t, path, "users")
	// A commit on the main branch is not the member's work.
	writeFile(t, filepath.Join(repo, "MAIN"), "x\ny\n")
	commitAll(t, repo, "main moves on")
	if added, removed, err := DiffStat(t.Context(), path, base); err != nil || added != 6 || removed != 1 {
		t.Fatalf("after a commit on the branch: +%d -%d %v", added, removed, err)
	}
	// Work not committed yet counts; a file git does not track does not.
	writeFile(t, filepath.Join(path, "users.go"), "a\nb\n")
	writeFile(t, filepath.Join(path, "scratch.txt"), "1\n2\n3\n4\n")
	if added, removed, err := DiffStat(t.Context(), path, base); err != nil || added != 5 || removed != 1 {
		t.Fatalf("with an uncommitted edit and an untracked file: +%d -%d %v", added, removed, err)
	}
	if _, _, err := DiffStat(t.Context(), path, "-p"); err == nil {
		t.Fatal("a base that reads as an option was run")
	}
}

// A file in the worktree named like the base commit does not make git read
// the base as a path.
func TestDiffStatWithAFileNamedLikeTheBase(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, ".conductor", "worktrees", "run", "core")
	if err := AddWorktree(t.Context(), repo, path, "crew/run/core"); err != nil {
		t.Fatal(err)
	}
	base, err := headCommit(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(path, base), "x\n")
	writeFile(t, filepath.Join(path, "README"), "one\ntwo\nthree\nfour\n")
	if added, removed, err := DiffStat(t.Context(), path, base); err != nil || added != 1 || removed != 0 {
		t.Fatalf("with a file named %s: +%d -%d %v", base, added, removed, err)
	}
}

func TestParseShortstat(t *testing.T) {
	for _, tc := range []struct {
		in             string
		added, removed int
		wantErr        bool
	}{
		{"", 0, 0, false},
		{"\n", 0, 0, false},
		{" 1 file changed, 1 insertion(+)\n", 1, 0, false},
		{" 2 files changed, 3 deletions(-)\n", 0, 3, false},
		{" 3 files changed, 10 insertions(+), 2 deletions(-)\n", 10, 2, false},
		{" 1 file changed, 1 insertion(+), 1 deletion(-)\n", 1, 1, false},
		{"fatal: something\n", 0, 0, true},
	} {
		added, removed, err := parseShortstat(tc.in)
		if added != tc.added || removed != tc.removed || (err != nil) != tc.wantErr {
			t.Errorf("parseShortstat(%q) = +%d -%d %v", tc.in, added, removed, err)
		}
	}
}

// excludeWorktrees adds an unanchored .conductor/ to the file git names for
// info/exclude, making info/ when it is missing, once, after what is there,
// from the top of the repository or a directory in it.
func TestExcludeWorktreesAddsTheLineOnce(t *testing.T) {
	repo := newRepo(t)
	if err := os.RemoveAll(filepath.Join(repo, ".git", "info")); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	for _, dir := range []string{repo, sub, repo} {
		if err := excludeWorktrees(t.Context(), dir); err != nil {
			t.Fatalf("from %s: %v", dir, err)
		}
	}
	if b, err := os.ReadFile(exclude); err != nil || string(b) != ".conductor/\n" {
		t.Fatalf("info/exclude %q %v", b, err)
	}
	// A file without a final line break keeps its last line whole.
	writeFile(t, exclude, "*.log")
	if err := excludeWorktrees(t.Context(), sub); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exclude); string(b) != "*.log\n.conductor/\n" {
		t.Fatalf("info/exclude %q", b)
	}
	for _, dir := range []string{filepath.Join(repo, ".conductor", "worktrees", "run", "lead"), filepath.Join(sub, ".conductor", "x")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "file"), "x\n")
	}
	if status := runGit(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("git status:\n%s", status)
	}
}

// A rev-parse failure other than "not a git repository" says what git said:
// a repository owned by someone else, or one the server user cannot read, is
// no missing repository. Both still answer not_a_repo.
func TestClassifyRevParsePassesGitsMessageOn(t *testing.T) {
	for _, tc := range []struct {
		msg, want string
		notInRepo bool
	}{
		{"git rev-parse: fatal: not a git repository (or any of the parent directories): .git", "is not in a git repository", true},
		{"git rev-parse: fatal: detected dubious ownership in repository at '/srv/x'", "detected dubious ownership in repository at '/srv/x'", false},
		{"git rev-parse: fatal: Invalid path '/srv/x/.git': Permission denied", "Permission denied", false},
	} {
		err := classifyRevParse(errors.New(tc.msg))
		if !errors.Is(err, ErrNotRepo) || !strings.Contains(err.Error(), tc.want) || (err == errNotInRepo) != tc.notInRepo {
			t.Errorf("%q: %v", tc.msg, err)
		}
	}
}

// GitState answers in one call what a launch with worktrees checks: in a
// repository with a commit (from its top or below it), in one without, or in
// no repository, each with the words the launch's refusal would use.
func TestGitStateReportsRepoCommitAndPlain(t *testing.T) {
	repo := newRepo(t)
	st, err := GitState(t.Context(), repo)
	if err != nil || !st.InRepo || st.Toplevel != repo || !st.HasCommit || st.Message != msgCanWorktree {
		t.Fatalf("repo: %+v %v", st, err)
	}
	sub := filepath.Join(repo, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if st, _ := GitState(t.Context(), sub); !st.InRepo || st.Toplevel != repo || !st.HasCommit {
		t.Fatalf("below the top: %+v", st)
	}
	empty, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, empty, "init", "-q")
	if st, err := GitState(t.Context(), empty); err != nil || !st.InRepo || st.Toplevel != empty || st.HasCommit || st.Message != errNoCommit.Error() {
		t.Fatalf("no commit: %+v %v", st, err)
	}
	if st, err := GitState(t.Context(), t.TempDir()); err != nil || st.InRepo || st.HasCommit || st.Message != errNotInRepo.Error() {
		t.Fatalf("plain: %+v %v", st, err)
	}
}

// GitState says what the launch says of a .conductor that is a symbolic
// link, which the launch refuses before it asks git.
func TestGitStateReportsTheLaunchRefusalOfAWorktreesLink(t *testing.T) {
	repo := newRepo(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, ".conductor")); err != nil {
		t.Fatal(err)
	}
	st, err := GitState(t.Context(), repo)
	e, _ := newEngine(t)
	c := testCrew(immediate("lead", "Plan it."))
	c.Isolation, c.Cwd = IsolationWorktree, repo
	_, lerr := e.Launch(t.Context(), c)
	if err != nil || lerr == nil || !st.InRepo || !st.HasCommit || st.Message != lerr.Error() {
		t.Fatalf("%+v %v; the launch: %v", st, err, lerr)
	}
}

// The workbench's working-directory picker tells a launch with worktrees the
// git check allows from one it refuses by comparing the check's message with
// msgCanWorktree, which web/app/utils/dirInput.ts holds as GIT_CAN_WORKTREE:
// the two must be the same words, or every launch would read as refused.
func TestPickerHoldsTheCanWorktreeMessage(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "app", "utils", "dirInput.ts"))
	if err != nil {
		t.Fatalf("the picker's words: %v", err)
	}
	m := regexp.MustCompile(`export const GIT_CAN_WORKTREE = '([^']*)'`).FindSubmatch(b)
	if m == nil {
		t.Fatal("web/app/utils/dirInput.ts has no export const GIT_CAN_WORKTREE = '...'")
	}
	if got := string(m[1]); got != msgCanWorktree {
		t.Fatalf("GIT_CAN_WORKTREE is %q, msgCanWorktree %q", got, msgCanWorktree)
	}
}

// RepoRoots names a repository's top and, for a linked worktree, the main
// repository's top too; outside a repository, the directory itself.
func TestRepoRoots(t *testing.T) {
	repo := newRepo(t)
	sub := filepath.Join(repo, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RepoRoots(t.Context(), sub); !slices.Equal(got, []string{repo}) {
		t.Fatalf("below the top: %q", got)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-q", "-b", "side", wt)
	wt, _ = filepath.EvalSymlinks(wt)
	if got := RepoRoots(t.Context(), wt); !slices.Equal(got, []string{wt, repo}) {
		t.Fatalf("a worktree: %q", got)
	}
	plain := t.TempDir()
	if got := RepoRoots(t.Context(), plain); !slices.Equal(got, []string{plain}) {
		t.Fatalf("no repository: %q", got)
	}
}

// Making and reading a member's worktree starts no program the repository's
// own configuration names (gitclitest: hooks in .git/hooks and through
// core.hooksPath, the file system monitor, filters, text conversion, some
// through include and includeIf): the checks, the exclude line, worktree
// add with its checkout and branch, the diff a run's members show. The
// worktree still holds the commit's files and the diff counts right.
func TestWorktreesStartNoProgramTheRepositoryNames(t *testing.T) {
	for _, hooksPath := range []bool{false, true} {
		r := gitclitest.New(t, gitclitest.Options{HooksPath: hooksPath})
		ctx := t.Context()
		if st, err := GitState(ctx, r.Dir); err != nil || !st.InRepo || !st.HasCommit || st.Toplevel != r.Dir {
			t.Fatalf("GitState: %+v %v", st, err)
		}
		if err := CheckRepo(ctx, r.Dir); err != nil {
			t.Fatal(err)
		}
		if roots := RepoRoots(ctx, r.Dir); !slices.Equal(roots, []string{r.Dir}) {
			t.Fatalf("RepoRoots: %q", roots)
		}
		if err := excludeWorktrees(ctx, r.Dir); err != nil {
			t.Fatal(err)
		}
		r.NoneFired(t, "the checks")
		path := filepath.Join(r.Dir, ".conductor", "worktrees", "run", "lead")
		if err := AddWorktree(ctx, r.Dir, path, "crew/run/lead"); err != nil {
			t.Fatal(err)
		}
		r.NoneFired(t, "worktree add")
		for name, body := range gitclitest.Committed {
			if b, err := os.ReadFile(filepath.Join(path, name)); err != nil || string(b) != body {
				t.Fatalf("hooksPath %v: %s in the worktree: %q %v", hooksPath, name, b, err)
			}
		}
		base, err := headCommit(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(path, "conv.txt"), "a\nb\n")
		writeFile(t, filepath.Join(path, "clean.dat"), "x\ny\nz\n")
		writeFile(t, filepath.Join(path, "README"), "one\nthree\n")
		if added, removed, err := DiffStat(ctx, path, base); err != nil || added != 3 || removed != 1 {
			t.Fatalf("hooksPath %v: DiffStat +%d -%d %v", hooksPath, added, removed, err)
		}
		r.NoneFired(t, "the diff")
	}
}

// The message of a failed git command is the line that says why, wherever
// git put it: newer gits print "Preparing worktree" before the fatal line.
func TestGitMessagePrefersTheFatalLine(t *testing.T) {
	for in, want := range map[string]string{
		"Preparing worktree (new branch 'x')\nfatal: a branch named 'x' already exists": "fatal: a branch named 'x' already exists",
		"fatal: not a git repository":                  "fatal: not a git repository",
		"error: pathspec 'x' did not match\nhint: use": "error: pathspec 'x' did not match",
		"Preparing worktree\nsomething odd\n":          "something odd",
		"":                                             "",
	} {
		if got := gitMessage(in); got != want {
			t.Errorf("gitMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddWorktreeAtStartsAtTheCommitGiven(t *testing.T) {
	repo := newRepo(t)
	first := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(repo, "later.txt"), "later\n")
	commitAll(t, repo, "later")
	path := filepath.Join(repo, ".conductor", "worktrees", "api-sweep-1a2b3c4d", "build.2")
	if err := AddWorktreeAt(t.Context(), repo, path, "crew/api-sweep-1a2b3c4d/build.2", first); err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(runGit(t, path, "rev-parse", "HEAD")); head != first {
		t.Fatalf("the worktree is at %s, not %s", head, first)
	}
	if _, err := os.Stat(filepath.Join(path, "later.txt")); err == nil {
		t.Fatal("the worktree has the later commit's file")
	}
	if branch := strings.TrimSpace(runGit(t, path, "rev-parse", "--abbrev-ref", "HEAD")); branch != "crew/api-sweep-1a2b3c4d/build.2" {
		t.Fatalf("the worktree is on %q", branch)
	}
	// Only a full commit id: a short one, a branch name or an option is
	// refused before git runs.
	for _, rev := range []string{first[:12], "HEAD", "main", "-x", ""} {
		if err := AddWorktreeAt(t.Context(), repo, filepath.Join(repo, ".conductor", "worktrees", "other", "x"), "crew/other/x", rev); err == nil || !strings.Contains(err.Error(), "not a commit id") {
			t.Fatalf("rev %q: %v", rev, err)
		}
	}
}

func TestAddDetachedWorktreeHasNoBranch(t *testing.T) {
	repo := newRepo(t)
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	path := filepath.Join(repo, ".conductor", "worktrees", "api-sweep-1a2b3c4d", "build.1.check")
	if err := AddDetachedWorktree(t.Context(), repo, path, head); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, path, "rev-parse", "HEAD")); got != head {
		t.Fatalf("the checkout is at %s, not %s", got, head)
	}
	if ref := strings.TrimSpace(runGit(t, path, "rev-parse", "--abbrev-ref", "HEAD")); ref != "HEAD" {
		t.Fatalf("the checkout is on a branch: %q", ref)
	}
	if b, err := os.ReadFile(filepath.Join(path, "README")); err != nil || string(b) != "one\ntwo\nthree\n" {
		t.Fatalf("README in the checkout: %q %v", b, err)
	}
	if out := runGit(t, repo, "branch", "--list"); strings.Contains(out, "check") {
		t.Fatalf("a branch was made: %s", out)
	}
	if err := AddDetachedWorktree(t.Context(), repo, filepath.Join(repo, ".conductor", "worktrees", "other"), "HEAD"); err == nil || !strings.Contains(err.Error(), "not a commit id") {
		t.Fatalf("a revision that is not a commit id: %v", err)
	}
}

// A HEAD whose commit git cannot read is git's failure, not a repository
// without a commit: CheckRepo hands git's error back, matching neither
// ErrNotRepo nor ErrNoGit, and GitState reports the directory in its
// repository, without a commit, in git's words.
func TestCheckRepoKeepsGitsFailureReadingHead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	repo := newRepo(t)
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	f := filepath.Join(repo, ".git", "objects", head[:2], head[2:])
	if err := os.Chmod(f, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(f, 0o444) })
	err := CheckRepo(t.Context(), repo)
	if err == nil || errors.Is(err, ErrNotRepo) || errors.Is(err, ErrNoGit) || !strings.Contains(err.Error(), "unable to open loose object") {
		t.Fatalf("CheckRepo: %v", err)
	}
	st, err := GitState(t.Context(), repo)
	if err != nil || !st.InRepo || st.HasCommit || !strings.HasPrefix(st.Message, "git cannot read the working directory's HEAD: ") {
		t.Fatalf("GitState: %+v %v", st, err)
	}
}
