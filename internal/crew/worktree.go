package crew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// With isolation "worktree" every member of a run works in a git worktree of
// the crew's working directory, on a branch of its own. git runs with argv,
// never through a shell. Conductor never removes a worktree or a branch.

// ErrNotRepo says that worktrees cannot be made of a directory: it is not in
// a git working tree, or its HEAD is no commit to branch from.
var ErrNotRepo = errors.New("not a git repository")

// ErrNoGit says that worktrees cannot be made on this server: git is not on
// its PATH. It is apart from ErrNotRepo, whatever the directory is.
var ErrNoGit = errors.New("git is not installed on the server")

// checkGit reports ErrNoGit when git is not on the server's PATH.
func checkGit() error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrNoGit
	}
	return nil
}

// repoError is an ErrNotRepo that says which of the two it is.
type repoError struct{ msg string }

func (e *repoError) Error() string { return e.msg }
func (e *repoError) Unwrap() error { return ErrNotRepo }

var (
	errNotInRepo = &repoError{"the working directory is not in a git repository"}
	errNoCommit  = &repoError{"the working directory is a git repository without a commit: a worktree needs one to branch from"}
)

// toplevel is the top of the git working tree dir is in: git -C dir
// rev-parse --show-toplevel. The error is ErrNoGit when git is not on PATH,
// ctx's when ctx ended, and otherwise matches ErrNotRepo: "not in a git
// repository" when git says so, git's own message for any other failure
// (classifyRevParse).
func toplevel(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case errors.Is(err, exec.ErrNotFound):
			return "", ErrNoGit
		}
		return "", classifyRevParse(err)
	}
	return strings.TrimSpace(out), nil
}

// inRepo checks that dir is in a git working tree (see toplevel).
func inRepo(ctx context.Context, dir string) error {
	_, err := toplevel(ctx, dir)
	return err
}

// State is what GitState reports of a directory.
type State struct {
	InRepo    bool   // dir is in a git working tree
	Toplevel  string // its top, when InRepo
	HasCommit bool   // HEAD is a commit to branch from
	Message   string // the words a launch's refusal uses, or that worktrees can be made
}

// msgCanWorktree is State.Message when worktrees can be made.
const msgCanWorktree = "a git repository with a commit: a crew with worktrees can launch here"

// repoState makes the checks CheckRepo makes of dir, through the same
// rev-parse calls: whether dir is in a git working tree, and its top, and
// whether HEAD is a commit to branch from. refusal is the error a launch
// refuses dir with, which matches ErrNotRepo, nil when worktrees can be made
// of it: HEAD is no commit only when git says so by its exit status alone
// (an unborn branch, a name that resolves to nothing). err is ErrNoGit when
// git is not on PATH, ctx's, or a *headError when git failed reading HEAD
// (an object it cannot read), with st then in the repository and without
// a commit.
func repoState(ctx context.Context, dir string) (st State, refusal, err error) {
	top, err := toplevel(ctx, dir)
	if err != nil {
		if errors.Is(err, ErrNotRepo) {
			return State{}, err, nil
		}
		return State{}, nil, err
	}
	st = State{InRepo: true, Toplevel: top}
	if _, err := headCommit(ctx, dir); err != nil {
		var exit *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return State{}, nil, ctx.Err()
		case !errors.As(err, &exit) || exit.ExitCode() != 1 && exit.ExitCode() != 128:
			return st, nil, &headError{err}
		}
		return st, errNoCommit, nil
	}
	st.HasCommit = true
	return st, nil, nil
}

// headError is git's own failure reading a working directory's HEAD (an
// object it cannot read), apart from a HEAD that is no commit: it is not
// the directory's refusal (ErrNotRepo) but git's, so a caller answers it
// as git's failure.
type headError struct{ err error }

func (e *headError) Error() string {
	return "git cannot read the working directory's HEAD: " + strings.TrimPrefix(e.err.Error(), "git rev-parse: ")
}
func (e *headError) Unwrap() error { return e.err }

// GitState reports, in one answer, what a launch with isolation "worktree"
// checks of a crew's working directory, by the same checks: whether dir is in
// a git working tree and its top, and whether HEAD is a commit to branch from
// (repoState, as CheckRepo), and whether its .conductor or
// .conductor/worktrees is a symbolic link (checkWorktreesDir), which the
// launch refuses first. Message is the launch's refusal of dir, or says that
// worktrees can be made there; a directory git refuses for another reason
// (dubious ownership, permissions) is InRepo false with git's message, and
// one whose HEAD git cannot read is InRepo, without a commit, with git's
// message. The error is ErrNoGit when git is not on PATH, or ctx's.
func GitState(ctx context.Context, dir string) (State, error) {
	st, refusal, err := repoState(ctx, dir)
	var head *headError
	switch {
	case errors.As(err, &head):
		refusal = head
	case err != nil:
		return State{}, err
	}
	if err := checkWorktreesDir(dir); err != nil {
		refusal = err
	}
	st.Message = msgCanWorktree
	if refusal != nil {
		st.Message = refusal.Error()
	}
	return st, nil
}

// classifyRevParse is the error toplevel reports for a failed rev-parse:
// errNotInRepo when git says the directory is not in a repository, and git's
// message otherwise, as for a repository owned by another user (dubious
// ownership) or one the server user cannot read. Both match ErrNotRepo.
func classifyRevParse(err error) error {
	msg := strings.TrimPrefix(err.Error(), "git rev-parse: ")
	if strings.Contains(msg, "not a git repository") {
		return errNotInRepo
	}
	return &repoError{"git cannot use the working directory's repository: " + msg}
}

// CheckRepo reports whether worktrees can be made of repo, a directory in a
// git working tree, its top or below it, whose HEAD is a commit. The error
// matches ErrNotRepo otherwise, and says which of the two it is; it is
// ErrNoGit when git is not on PATH, and git's own failure, matching
// neither, when git cannot read HEAD (repoState).
func CheckRepo(ctx context.Context, repo string) error {
	_, refusal, err := repoState(ctx, repo)
	if err != nil {
		return err
	}
	return refusal
}

// AddWorktree adds a worktree of the repository repo is in at path, on a new
// branch made from HEAD (gitcli.AddWorktree: git -C repo worktree add
// --no-checkout -b branch path HEAD, then the checkout in the new
// worktree). git makes the parent directories of path. No hook of the
// repository runs (no post-checkout, no reference-transaction), nor any
// filter. The error matches ErrNotRepo when repo is in no git working tree,
// and is ErrNoGit when git is not on PATH.
// AddWorktreeAt is AddWorktree at the commit rev, a full object id, for an
// attempt of a managed run: its worktree starts at the result it builds on.
func AddWorktreeAt(ctx context.Context, repo, path, branch, rev string) error {
	if err := checkGit(); err != nil {
		return err
	}
	if !fullCommit(rev) {
		return fmt.Errorf("worktree: %q is not a commit id", rev)
	}
	return gitcli.AddWorktree(ctx, repo, path, branch, rev)
}

// AddDetachedWorktree is a checkout of rev with no branch, for the checks
// of a managed run against a result: nothing commits to it.
func AddDetachedWorktree(ctx context.Context, repo, path, rev string) error {
	if err := checkGit(); err != nil {
		return err
	}
	if !fullCommit(rev) {
		return fmt.Errorf("worktree: %q is not a commit id", rev)
	}
	return gitcli.AddDetachedWorktree(ctx, repo, path, rev)
}

// fullCommit reports whether s is a full commit id: 40 hex characters (64
// in a SHA-256 repository).
func fullCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func AddWorktree(ctx context.Context, repo, path, branch string) error {
	if err := inRepo(ctx, repo); err != nil {
		return err
	}
	return gitcli.AddWorktree(ctx, repo, path, branch, "HEAD")
}

// DiffStat counts the lines a worktree adds and removes against base, the
// commit it started from: git -C worktree diff --shortstat base --, the "--"
// so that a file named like base does not make it a path. Commits on its
// branch and uncommitted changes to tracked files count; untracked files do
// not, and a member that merges another branch into its own counts that
// branch's changes too. The engine keeps what it reads for 10 s.
func DiffStat(ctx context.Context, worktree, base string) (added, removed int, err error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return 0, 0, fmt.Errorf("git diff: invalid base %q", base)
	}
	out, err := git(ctx, worktree, "diff", "--shortstat", base, "--")
	if err != nil {
		return 0, 0, err
	}
	return parseShortstat(out)
}

// parseShortstat reads git diff --shortstat, as the C locale writes it:
// " 3 files changed, 10 insertions(+), 2 deletions(-)", where either count may
// be left out, and nothing at all when nothing changed.
func parseShortstat(s string) (added, removed int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	for part := range strings.SplitSeq(s, ",") {
		fields := strings.Fields(part)
		if len(fields) < 2 {
			return 0, 0, fmt.Errorf("git diff --shortstat: cannot read %q", s)
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0, 0, fmt.Errorf("git diff --shortstat: cannot read %q", s)
		}
		switch {
		case strings.HasPrefix(fields[1], "insertion"):
			added = n
		case strings.HasPrefix(fields[1], "deletion"):
			removed = n
		}
	}
	return added, removed, nil
}

// repoPrefix is where dir lies in its working tree, "" at its top, else a
// relative path ending in a slash: git -C dir rev-parse --show-prefix.
func repoPrefix(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// excludeLine keeps the worktrees out of the repository's own status: git
// ignores a .conductor directory at any depth.
const excludeLine = ".conductor/"

// excludeWorktrees adds excludeLine to the repository's info/exclude, the
// file git -C dir rev-parse --git-path info/exclude names (in the common git
// directory for a linked worktree or a submodule), making info/ when it is
// missing. A file that has the line already is left as it is. The caller
// keeps two from running at once.
func excludeWorktrees(ctx context.Context, dir string) error {
	out, err := git(ctx, dir, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.TrimSpace(line) == excludeLine {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	add := excludeLine + "\n"
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		add = "\n" + add
	}
	if _, err := f.WriteString(add); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// checkWorktreesDir refuses a <cwd>/.conductor or <cwd>/.conductor/worktrees
// that is a symbolic link, which would put the worktrees outside cwd. The
// error matches ErrInvalid.
func checkWorktreesDir(cwd string) error {
	for _, dir := range []string{".conductor", worktreesDir} {
		if fi, err := os.Lstat(filepath.Join(cwd, dir)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return invalidf("%s in the working directory is a symbolic link: worktrees must stay inside the working directory", dir)
		}
	}
	return nil
}

// checkMemberDir refuses a symbolic link or a file that is not a directory on
// the way from root to root/rel, rel a relative path: a commit may hold
// either; a directory made through a link would be made wherever it points,
// and none can be made through a file. It stops at the first part that does
// not exist, which MkdirAll makes as a directory. The error matches
// ErrInvalid.
func checkMemberDir(root, rel string) error {
	path := ""
	for part := range strings.SplitSeq(filepath.ToSlash(filepath.Clean(rel)), "/") {
		if part == "" || part == "." {
			continue
		}
		path = filepath.Join(path, part)
		fi, err := os.Lstat(filepath.Join(root, path))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return invalidf("%s in the member's worktree is a symbolic link: the member's directory must stay inside the worktree", quote(filepath.ToSlash(path)))
		}
		if !fi.IsDir() {
			return invalidf("%s in the member's worktree is a file, not a directory: the member's directory must be one", quote(filepath.ToSlash(path)))
		}
	}
	return nil
}

// headCommit returns the commit HEAD names in dir.
func headCommit(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// git runs git for the crews (gitcli.Run: argv, the C locale, no program the repository's configuration names, the line that
// says why on failure).
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitcli.Run(ctx, dir, args...)
}

// gitMessage is the line of git's standard error that says why it failed (gitcli.Message).
func gitMessage(stderr string) string { return gitcli.Message(stderr) }

// RepoRoots returns the directories a per-launch trust names for dir: the top
// of its git working tree and, for a linked worktree, the main repository's
// top too, where an agent that trusts by repository looks (Codex resolves a
// worktree to its main repository). Not in a repository, or git missing or
// failing, it is dir alone. Each path is absolute; none is repeated.
func RepoRoots(ctx context.Context, dir string) []string {
	// --git-common-dir may answer relative to dir (git before 2.31 has no
	// --path-format=absolute).
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return []string{dir}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !filepath.IsAbs(lines[0]) {
		return []string{dir}
	}
	roots := []string{lines[0]}
	common := lines[1]
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	if filepath.Base(common) == ".git" {
		if main := filepath.Dir(common); main != lines[0] {
			roots = append(roots, main)
		}
	}
	return roots
}
