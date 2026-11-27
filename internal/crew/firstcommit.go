package crew

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// A crew whose members work on copies of their own (isolation "worktree",
// an agent trial among them) needs a commit of its folder to copy, and a
// new project often has none: a folder that is no git repository yet, or
// one whose repository has no commit yet. EnsureFirstCommit makes what is
// missing there, once, and the launch goes on (issue 1179). Its callers
// call it for the operator alone (LaunchOpts.FirstCommit, and the operator's
// own proposal of a trial's plan revision in internal/api); every other
// launch and proposal meets the refusal it always met (CheckRepo).
//
//   - What goes in: what is in the repository's working tree, as git add
//     --all takes it, its ignore files and git's own excludes respected,
//     and never Conductor's own .conductor/ (excludeWorktrees first) nor a
//     repository nested in it (left out, never added as a link). Files
//     already staged go in with the rest. An empty folder gets an empty
//     commit. A folder below the top of its repository: the commit is the
//     repository's.
//   - A bound: nothing is made in a home folder or a file system's root, nor
//     for more than MaxFirstCommitFiles files or MaxFirstCommitBytes bytes
//     of what would be committed, measured before anything is written. Past
//     it the launch is refused with the fact and the way on.
//   - Whose name: the person's own git identity where git resolves one for
//     the folder; where it resolves none, Conductor's (firstCommitName,
//     firstCommitEmail), for this one command, written into no settings.
//   - Signing and hooks: no hook runs (gitcli.Run sets none). A folder whose
//     git signs commits is never overridden: a signature that cannot be made
//     without a person fails the commit and refuses the launch. git runs
//     with no terminal and within firstCommitWait, so it never waits on one.
//   - A new repository's first branch is git's configured default, else
//     main.
//   - Once: two launches at once make one commit, the second finds it
//     (firstCommitMu).
//   - Failures: a repository made for this whose commit could not be made is
//     removed again; one that was there is left as it was. A commit made
//     stays when the launch then fails for another reason: the next launch
//     finds it.
//
// The folder agents may not use never reaches it: its callers resolve the
// working directory first (resolveCwd) and refuse it before anything is
// made.

// The bound of what Conductor commits by itself.
const (
	MaxFirstCommitFiles = 5_000
	MaxFirstCommitBytes = 200 << 20
)

// firstCommitName and firstCommitEmail name a first commit for a folder
// whose git resolves no identity. The address is in the reserved .invalid
// domain (RFC 2606): plainly no mailbox.
const (
	firstCommitName  = "Conductor"
	firstCommitEmail = "conductor@noreply.invalid"
)

// FirstCommitMessage is the message of a first commit Conductor makes: who
// made it and why, in one line, for whoever reads git log later.
const FirstCommitMessage = "First commit, made by Conductor so that agents could each work on a copy of this folder"

// firstCommitWait bounds each git command of a first commit: a signing
// program that waits on a person fails it rather than hold the launch.
var firstCommitWait = 30 * time.Second

// firstCommitMu keeps two first commits from being made at once.
var firstCommitMu sync.Mutex

// FirstCommit is what EnsureFirstCommit made: nothing (Made false) in a
// folder whose HEAD is a commit; else the repository's top, whether the
// repository was made too, and the commit.
type FirstCommit struct {
	Made   bool
	Init   bool
	Top    string
	Commit string
}

// firstCommitError is a folder EnsureFirstCommit makes no commit in, in the
// words a launch's refusal says: it matches ErrNotRepo, as the refusal it
// stands for does.
type firstCommitError struct{ msg string }

func (e *firstCommitError) Error() string { return e.msg }
func (e *firstCommitError) Unwrap() error { return ErrNotRepo }

// EnsureFirstCommit makes dir's first commit when dir's repository has none,
// making the repository first when dir is in none, and reports what it
// made; a folder whose HEAD is a commit is left as it is (see above). The
// error matches ErrNotRepo for a folder it makes nothing in (a repository
// git cannot use, a home folder or a root, past the bound, a commit git
// refused), is ErrNoGit when git is not on PATH, or ctx's.
func EnsureFirstCommit(ctx context.Context, dir string) (FirstCommit, error) {
	if err := checkGit(); err != nil {
		return FirstCommit{}, err
	}
	firstCommitMu.Lock()
	defer firstCommitMu.Unlock()
	st, refusal, err := repoState(ctx, dir)
	switch {
	case err != nil:
		return FirstCommit{}, err
	case refusal == nil:
		return FirstCommit{}, nil
	case st.InRepo && refusal != errNoCommit, !st.InRepo && refusal != errNotInRepo:
		// A repository git cannot use (its owner, its permissions): not
		// Conductor's to change.
		return FirstCommit{}, refusal
	}
	if err := checkWorktreesDir(dir); err != nil {
		return FirstCommit{}, err
	}
	top := dir
	if st.InRepo {
		top = st.Toplevel
	}
	if why := notCommittedIn(top); why != "" {
		return FirstCommit{}, &firstCommitError{why}
	}
	files, bytes, nested, err := toCommit(ctx, top, st.InRepo)
	switch {
	case err != nil:
		return FirstCommit{}, err
	case files > MaxFirstCommitFiles:
		return FirstCommit{}, &firstCommitError{fmt.Sprintf("this folder has %s files and no commit yet: make its first commit yourself, then launch", grouped(files))}
	case bytes > MaxFirstCommitBytes:
		return FirstCommit{}, &firstCommitError{fmt.Sprintf("this folder has %d MB of files and no commit yet: make its first commit yourself, then launch", bytes>>20)}
	}
	fc := FirstCommit{Init: !st.InRepo, Top: top}
	undo := func() {}
	if fc.Init {
		args := []string{"init", "--quiet"}
		if branch, err := git(ctx, dir, "config", "--get", "init.defaultBranch"); err != nil || strings.TrimSpace(branch) == "" {
			args = append(args, "--initial-branch=main")
		}
		if _, err := runBounded(ctx, dir, nil, args...); err != nil {
			return FirstCommit{}, &firstCommitError{"the folder's repository could not be made: " + err.Error()}
		}
		undo = func() { _ = os.RemoveAll(filepath.Join(dir, ".git")) }
	}
	made, err := commitFolder(ctx, top, nested)
	if err != nil {
		undo()
		return FirstCommit{}, err
	}
	fc.Made, fc.Commit = true, made
	return fc, nil
}

// notCommittedIn is why Conductor makes no first commit in top by itself:
// it is a file system's root or the server user's home folder; "" else.
func notCommittedIn(top string) string {
	clean := filepath.Clean(top)
	if filepath.Dir(clean) == clean {
		return "this folder is a file system's root and has no commit yet: pick a project's folder, or make its first commit yourself"
	}
	if home, err := os.UserHomeDir(); err == nil && sameFolder(home, clean) {
		return "this folder is the home folder and has no commit yet: pick a project's folder, or make its first commit yourself"
	}
	return ""
}

// toCommit measures what a first commit of top would hold, before anything
// is written in the folder: the files git add --all would take (the tracked
// ones of an index, the others its ignore rules keep), their bytes, and the
// nested repositories it would add as links, which are left out. A folder
// in no repository yet is listed through a repository of the server's own
// outside it (GIT_DIR, GIT_WORK_TREE), removed after: its ignore files count
// as they will once its own repository exists.
func toCommit(ctx context.Context, top string, inRepo bool) (files int, bytes int64, nested []string, err error) {
	var env map[string]string
	if !inRepo {
		tmp, err := os.MkdirTemp("", "conductor-first-commit-")
		if err != nil {
			return 0, 0, nil, err
		}
		defer os.RemoveAll(tmp)
		if _, err := runBounded(ctx, tmp, nil, "init", "--quiet", "--bare"); err != nil {
			return 0, 0, nil, fmt.Errorf("listing the folder's files: %w", err)
		}
		env = map[string]string{"GIT_DIR": tmp, "GIT_WORK_TREE": top}
	}
	listed, err := runBounded(ctx, top, env, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return 0, 0, nil, fmt.Errorf("listing the folder's files: %w", err)
	}
	for p := range strings.SplitSeq(strings.TrimSuffix(listed, "\x00"), "\x00") {
		switch {
		case p == "":
		case strings.HasSuffix(p, "/"):
			// git lists a repository nested in the tree as its folder.
			nested = append(nested, strings.TrimSuffix(p, "/"))
		case p == ".conductor" || strings.HasPrefix(p, ".conductor/"):
			// Conductor's own place, never committed (excludeLine).
		default:
			files++
			if fi, err := os.Lstat(filepath.Join(top, filepath.FromSlash(p))); err == nil && fi.Mode().IsRegular() {
				bytes += fi.Size()
			}
		}
		if files > MaxFirstCommitFiles || bytes > MaxFirstCommitBytes {
			break
		}
	}
	return files, bytes, nested, nil
}

// commitFolder stages top's working tree, the nested repositories left out
// (listed again now that a new repository can list them), and commits it,
// the person's identity else Conductor's; the commit made.
func commitFolder(ctx context.Context, top string, nested []string) (string, error) {
	if err := excludeWorktrees(ctx, top); err != nil {
		return "", fmt.Errorf("leaving Conductor's folder out of the first commit: %w", err)
	}
	restore, err := keepIndex(ctx, top)
	if err != nil {
		return "", err
	}
	add := []string{"add", "--all", "--", "."}
	for _, n := range nested {
		add = append(add, ":(exclude,literal)"+n)
	}
	if _, err := runBounded(ctx, top, nil, add...); err != nil {
		restore()
		return "", &firstCommitError{"the folder's files could not be added to its first commit: " + err.Error()}
	}
	if _, err := runBounded(ctx, top, identityEnv(ctx, top), "commit", "--quiet", "--allow-empty", "--no-verify", "-m", FirstCommitMessage); err != nil {
		restore()
		return "", &firstCommitError{"the folder's first commit could not be made: " + err.Error()}
	}
	return headCommit(ctx, top)
}

// keepIndex keeps a copy of top's index as it is, and returns what puts it
// back: a commit that fails leaves a repository that was there with the
// index the person left (none, when it had none).
func keepIndex(ctx context.Context, top string) (func(), error) {
	out, err := git(ctx, top, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	index := strings.TrimSpace(out)
	b, err := os.ReadFile(index)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return func() { _ = os.Remove(index) }, nil
	case err != nil:
		return nil, err
	}
	return func() { _ = os.WriteFile(index, b, 0o644) }, nil
}

// identityEnv names Conductor as the commit's author and committer where git
// resolves no identity for the folder: nil when it resolves one.
func identityEnv(ctx context.Context, dir string) map[string]string {
	if _, err := runBounded(ctx, dir, nil, "var", "GIT_COMMITTER_IDENT"); err == nil {
		if _, err := runBounded(ctx, dir, nil, "var", "GIT_AUTHOR_IDENT"); err == nil {
			return nil
		}
	}
	return map[string]string{
		"GIT_AUTHOR_NAME": firstCommitName, "GIT_AUTHOR_EMAIL": firstCommitEmail,
		"GIT_COMMITTER_NAME": firstCommitName, "GIT_COMMITTER_EMAIL": firstCommitEmail,
	}
}

// runBounded is one git of a first commit, within firstCommitWait.
func runBounded(ctx context.Context, dir string, env map[string]string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, firstCommitWait)
	defer cancel()
	return gitcli.RunEnv(ctx, dir, env, args...)
}

// grouped writes n with a comma between each three digits: 48,210.
func grouped(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// IsFirstCommit reports whether rev is a first commit Conductor made in
// dir's repository: a root commit with FirstCommitMessage.
func IsFirstCommit(ctx context.Context, dir, rev string) bool {
	raw, err := git(ctx, dir, "cat-file", "commit", rev)
	if err != nil {
		return false
	}
	head, msg, _ := strings.Cut(raw, "\n\n")
	for line := range strings.SplitSeq(head, "\n") {
		if strings.HasPrefix(line, "parent ") {
			return false
		}
	}
	return strings.TrimRight(msg, "\n") == FirstCommitMessage
}
