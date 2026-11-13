package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/pty"
)

// How Conductor runs git. A repository's configuration can name programs
// that git starts by itself: hooks, a file system monitor, a pager, an
// external diff, text conversion, clean, smudge and process filters, an
// editor, credential helpers, transports and the commands they run. The
// directories Conductor runs git in (a session's, a crew's) are not ones
// it vouches for, so the git it runs starts none of them:
//
//   - command-line configuration (safeConfig) sets each to nothing. git
//     reads it after the system, global and repository files and whatever
//     those include (include.path, includeIf), so it wins over all of
//     them, and git hands it on to the git processes it starts itself
//     (worktree add's checkout, a submodule's status);
//   - the environment (safeEnv) allows no transport, whatever
//     protocol.<name>.allow the repository sets: nothing fetches, and no
//     ssh command, upload-pack, receive-pack, remote helper or credential
//     helper starts;
//   - each subcommand Conductor runs gets the options (commandOptions) that
//     keep external diffs, text conversion and the submodules' own git out;
//   - the clean, smudge and process filters the configuration names are
//     turned off by name (filterArgs), git having no switch for all of
//     them: every one, the global configuration's too (a filter such as
//     Git LFS's reads the repository's configuration for programs of its
//     own), so a checkout writes files as the repository stores them.
//
// The names are read in the directory the command runs in, just before it,
// so a conditional include (includeIf onbranch:, gitdir:) counts as it will
// for the command. A worktree's checkout reads its configuration only once
// the worktree exists, on its new branch, so making one is two commands
// (AddWorktree), and Run refuses a worktree add that checks out.
//
// The person's own identity and settings in the global configuration still
// apply; Conductor commits nothing and signs nothing.

// safeConfig is set with -c on every git Conductor runs.
var safeConfig = []string{
	// No file system monitor, neither a hook program nor git's daemon.
	// Empty is off in every git; "false" is understood from 2.36 only.
	"core.fsmonitor=",
	// No hook: no post-checkout or reference-transaction when worktree add
	// checks out and makes the branch, no post-index-change, no other.
	"core.hooksPath=" + os.DevNull,
	// With --no-pager: no pager, whatever core.pager or pager.<command> say.
	"core.pager=cat",
	// With --no-ext-diff on the commands that diff.
	"diff.external=",
	// git never waits on an editor: one asked for fails.
	"core.editor=false",
	"sequence.editor=false",
	// No askpass program and no credential helper (empty resets the list).
	"core.askPass=",
	"credential.helper=",
	// No transport by configuration either (safeEnv's GIT_ALLOW_PROTOCOL
	// overrides the per-protocol settings).
	"protocol.allow=never",
	// No signature check, so no gpg.program.
	"log.showSignature=false",
	// No automatic maintenance or garbage collection.
	"gc.auto=0",
	"maintenance.auto=false",
}

// safeEnv is set in git's environment, which is otherwise the allowlisted
// one every child of Conductor gets (pty.BuildEnv): no GIT_* variable of
// the server's comes through.
var safeEnv = map[string]string{
	"LC_ALL": "C",
	// An empty list: no protocol is allowed, overriding any
	// protocol.allow and protocol.<name>.allow.
	"GIT_ALLOW_PROTOCOL": "",
	// A partial clone's missing object is an error rather than a fetch (git
	// 2.45 and later; before, the fetch stops at GIT_ALLOW_PROTOCOL).
	"GIT_NO_LAZY_FETCH":   "1",
	"GIT_TERMINAL_PROMPT": "0",
	// Reads take no lock and write no index beside the agent's own git.
	"GIT_OPTIONAL_LOCKS": "0",
}

// commandOptions are put right after the subcommand: the ones that diff
// run no external diff and no text conversion; status and diff do not run
// git in a submodule to see whether its working tree is dirty (a change
// of the submodule's commit still shows), since that git would read the
// submodule's own configuration, whose filters filterArgs does not see.
func commandOptions(sub string) []string {
	switch sub {
	case "diff":
		return []string{"--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty"}
	case "show", "log":
		return []string{"--no-ext-diff", "--no-textconv"}
	case "status":
		return []string{"--ignore-submodules=dirty"}
	}
	return nil
}

// noFilters are the subcommands Conductor runs that never filter a file:
// they do not need filterArgs' look at the configuration first.
var noFilters = map[string]bool{"rev-parse": true, "config": true, "show": true, "init": true}

// maxFilters bounds how many filters filterArgs turns off; a configuration
// that names more is refused, as is a name -c cannot carry.
const maxFilters = 200

// Run runs git with args in dir (git -C dir, so it is the same wherever the
// process runs), hardened as above, in the C locale. It is the one way
// Conductor runs git. A failure carries the line that says why;
// ErrNotRepo outside a repository.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, []string{"-C", dir}, args)
}

// RunIn runs git as Run does, in dir, a directory its caller opened, never
// by a path of its own: git starts in the open directory itself, so a
// rename of the folder dir was opened at, or a link put in its place,
// before or while git runs, moves it nowhere. ErrNoDirHandle where the
// system offers git no way into an open directory (startIn).
func RunIn(ctx context.Context, dir *os.File, args ...string) (string, error) {
	cwd, err := startIn(dir)
	if err != nil {
		return "", err
	}
	defer runtime.KeepAlive(dir)
	return runEnv(ctx, cwd, nil, nil, args)
}

// ErrNoDirHandle is RunIn where git cannot be started in an open
// directory: a read that would have to name it by its path again is not
// made.
var ErrNoDirHandle = errors.New("git: this system cannot start git in an open directory")

// RunEnv is Run with env set in git's environment beside the safe one: an
// index of its own (GIT_INDEX_FILE) for a comparison of a checkout with
// its commit that trusts no flag of the checkout's index (the crew's
// checks).
func RunEnv(ctx context.Context, dir string, env map[string]string, args ...string) (string, error) {
	return runEnv(ctx, "", []string{"-C", dir}, env, args)
}

// run runs git with args where loc points it (-C dir, or a git directory
// and a working tree named outright), for the filter look-up and the
// command alike.
func run(ctx context.Context, loc, args []string) (string, error) {
	return runEnv(ctx, "", loc, nil, args)
}

// RunTo is Run with git's standard output written to w as it comes rather
// than kept: a blob read whole (a decision's artifact, hashed) is never
// held in memory.
func RunTo(ctx context.Context, dir string, w io.Writer, args ...string) error {
	return RunToEnv(ctx, dir, nil, w, args...)
}

// RunToEnv is RunTo with env set in git's environment beside the safe one:
// GIT_NO_REPLACE_OBJECTS for a blob read by the id a tree records, which
// must be that object and not one refs/replace puts in its place.
func RunToEnv(ctx context.Context, dir string, env map[string]string, w io.Writer, args ...string) error {
	argv, err := argvFor(ctx, "", []string{"-C", dir}, args)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	if err := execGitTo(ctx, "", argv, env, w, &stderr); err != nil {
		return gitError(ctx, args[0], stderr.String(), err)
	}
	return nil
}

// RunTree runs git with args in the work tree workTree, with the tree's
// own .git and the work tree named outright (-C workTree
// --git-dir=workTree/.git --work-tree=workTree), so that git looks for no
// repository and no configuration names another work tree: a .git that
// is a gitfile is followed, one that is a directory is opened, and a tree
// with no .git at its top is no repository (ErrNotRepo). The filter
// look-up before the command (filterArgs) takes the same location. env
// is set in git's environment beside the safe one, in (nil for none) is
// on its standard input and its standard output is written to w as it
// comes (w bounds what it keeps). Of its error stream the first
// maxStderr bytes are kept for the error. It is how a tree's read runs
// git (crew.TreeRevision).
//
// objects is a folder of the caller's own, an absolute path, which must
// be a folder as git starts (looked at without following it; anything
// else is refused, ErrNoObjects, and no git runs): git writes every
// object of the command there
// (GIT_OBJECT_DIRECTORY) and finds the repository's objects through the
// repository's own store, named as an alternate it only reads
// (GIT_ALTERNATE_OBJECT_DIRECTORIES), so nothing the command writes goes
// into the repository. The store is the one git names for the tree, asked
// first at the same location (rev-parse --git-path objects, which runs no
// filter and reads no file of the work tree), within maxStorePath. A
// tree's read names each changed file by its hash (update-index
// --info-only) and writes trees alone, so its folder, one of the server's
// own in its temporary directory, holds no file's content.
func RunTree(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
	if !filepath.IsAbs(objects) {
		return fmt.Errorf("git: the folder of a tree's objects %q is not an absolute path", objects)
	}
	if fi, err := os.Lstat(objects); err != nil || !fi.IsDir() {
		return ErrNoObjects
	}
	loc := []string{"-C", workTree, "--git-dir=" + filepath.Join(workTree, ".git"), "--work-tree=" + workTree}
	store, stderr := &Head{N: maxStorePath + 1}, &Head{N: maxStderr}
	argv, err := argvFor(ctx, "", loc, []string{"rev-parse", "--git-path", "objects"})
	if err == nil {
		err = execGitIO(ctx, "", argv, nil, nil, store, stderr)
	}
	if err != nil {
		return gitError(ctx, "rev-parse", stderr.String(), err)
	}
	path := strings.TrimSuffix(store.String(), "\n")
	if len(path) > maxStorePath || path == "" {
		return fmt.Errorf("git rev-parse: the object store's path is empty or past %d bytes", maxStorePath)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workTree, path)
	}
	set := map[string]string{}
	maps.Copy(set, env)
	set["GIT_OBJECT_DIRECTORY"], set["GIT_ALTERNATE_OBJECT_DIRECTORIES"] = objects, quoteAlternate(path)
	if argv, err = argvFor(ctx, "", loc, args); err != nil {
		return err
	}
	stderr = &Head{N: maxStderr}
	if err := execGitIO(ctx, "", argv, set, in, w, stderr); err != nil {
		return gitError(ctx, args[0], stderr.String(), err)
	}
	return nil
}

// ErrNoObjects is RunTree refusing a run whose folder of objects is not a
// folder: no git is started.
var ErrNoObjects = errors.New("git: refused: the folder of a tree's objects is not there")

// maxStderr bounds what RunTree keeps of git's error stream.
const maxStderr = 4 << 10

// maxStorePath bounds the path of the object store RunTree reads from.
const maxStorePath = 4 << 10

// quoteAlternate is path as one entry of GIT_ALTERNATE_OBJECT_DIRECTORIES,
// quoted as git unquotes it (a C string), so that no separator in the
// path (":", ";" on Windows) splits it.
func quoteAlternate(path string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range []byte(path) {
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Head is a writer that keeps the first N bytes written to it and drops
// the rest, every write taken whole.
type Head struct {
	N   int
	buf []byte
}

func (h *Head) Write(p []byte) (int, error) {
	h.buf = append(h.buf, p[:min(len(p), max(h.N-len(h.buf), 0))]...)
	return len(p), nil
}

// String is what h kept.
func (h *Head) String() string { return string(h.buf) }

// runEnv is run with env set in git's environment beside the safe one, in
// the working directory cwd ("" for the server's own: loc then points git
// where it runs).
func runEnv(ctx context.Context, cwd string, loc []string, env map[string]string, args []string) (string, error) {
	argv, err := argvFor(ctx, cwd, loc, args)
	if err != nil {
		return "", err
	}
	out, stderr, err := execGit(ctx, cwd, argv, env)
	if err != nil {
		return "", gitError(ctx, args[0], stderr, err)
	}
	return out, nil
}

// argvFor is the argv of git with args where loc points it, from the
// working directory cwd, hardened as above.
func argvFor(ctx context.Context, cwd string, loc, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, errors.New("git: no command")
	}
	if len(args) > 1 && args[0] == "worktree" && args[1] == "add" && !slices.Contains(args, "--no-checkout") {
		return nil, errors.New("git worktree add: a worktree is checked out by AddWorktree")
	}
	filters, err := filterArgs(ctx, cwd, loc, args)
	if err != nil {
		return nil, err
	}
	argv := append(baseArgs(), filters...)
	argv = append(argv, loc...)
	argv = append(argv, args[0])
	argv = append(argv, commandOptions(args[0])...)
	argv = append(argv, args[1:]...)
	return argv, nil
}

// AddWorktree adds a worktree of the repository repo is in at path, on a
// new branch made from rev, and checks it out: git worktree add
// --no-checkout -b branch path rev, then git reset --hard with the new
// worktree's git directory and path named outright (--git-dir,
// --work-tree), as git's own worktree add does, so that no core.worktree
// of the repository's sends the checkout elsewhere, and the filter look-up
// (filterArgs) sees the configuration the checkout reads, the includes
// that apply on its branch and in its git directory among it. No hook runs
// (no post-checkout, no reference-transaction) and no filter; git makes
// the parent directories of path.
// AddDetachedWorktree makes a worktree of repo at path with rev checked out
// and no branch (detached HEAD): a checkout nothing commits to, for the
// checks of a managed run against a result. Made as AddWorktree makes its,
// with the checkout inside the new worktree.
func AddDetachedWorktree(ctx context.Context, repo, path, rev string) error {
	if strings.HasPrefix(path, "-") || strings.HasPrefix(rev, "-") {
		return fmt.Errorf("git worktree add: invalid path or revision")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := Run(ctx, repo, "worktree", "add", "--no-checkout", "--detach", abs, rev); err != nil {
		return err
	}
	out, err := Run(ctx, abs, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	gitDir := strings.TrimSpace(out)
	if !filepath.IsAbs(gitDir) {
		return fmt.Errorf("git worktree add: the new worktree's git directory is %q", gitDir)
	}
	_, err = run(ctx, []string{"-C", abs, "--git-dir=" + gitDir, "--work-tree=" + abs}, []string{"reset", "--hard", "-q", "--no-recurse-submodules", "HEAD"})
	return err
}

func AddWorktree(ctx context.Context, repo, path, branch, rev string) error {
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(path, "-") || strings.HasPrefix(rev, "-") {
		return fmt.Errorf("git worktree add: invalid branch, path or revision")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := Run(ctx, repo, "worktree", "add", "--no-checkout", "-b", branch, abs, rev); err != nil {
		return err
	}
	out, err := Run(ctx, abs, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	gitDir := strings.TrimSpace(out)
	if !filepath.IsAbs(gitDir) {
		return fmt.Errorf("git worktree add: the new worktree's git directory is %q", gitDir)
	}
	_, err = run(ctx, []string{"-C", abs, "--git-dir=" + gitDir, "--work-tree=" + abs}, []string{"reset", "--hard", "-q", "--no-recurse-submodules", "HEAD"})
	return err
}

// baseArgs are the options every git Conductor runs starts with.
func baseArgs() []string {
	argv := make([]string, 0, 1+2*len(safeConfig))
	argv = append(argv, "--no-pager")
	for _, kv := range safeConfig {
		argv = append(argv, "-c", kv)
	}
	return argv
}

// execGit runs git with argv in safeEnv, and env beside it, no input, in
// the working directory cwd ("" for the server's own).
func execGit(ctx context.Context, cwd string, argv []string, env map[string]string) (stdout, stderr string, err error) {
	var o, e bytes.Buffer
	err = execGitTo(ctx, cwd, argv, env, &o, &e)
	return o.String(), e.String(), err
}

// execGitTo is execGit with its output written to stdout and stderr.
func execGitTo(ctx context.Context, cwd string, argv []string, env map[string]string, stdout, stderr io.Writer) error {
	return execGitIO(ctx, cwd, argv, env, nil, stdout, stderr)
}

// execGitIO is execGitTo with stdin on git's standard input (nil for
// none).
func execGitIO(ctx context.Context, cwd string, argv []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = cwd
	cmd.Stdin = stdin
	set := safeEnv
	if len(env) > 0 {
		set = maps.Clone(safeEnv)
		maps.Copy(set, env)
	}
	cmd.Env = pty.BuildEnv(pty.ParentEnv(), nil, set, nil)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// gitError is the error of a git that failed: ctx's when it ended,
// ErrNotRepo, or the line of its standard error that says why.
func gitError(ctx context.Context, sub, stderr string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if msg := Message(stderr); msg != "" {
		if strings.Contains(msg, "not a git repository") {
			return ErrNotRepo
		}
		return fmt.Errorf("git %s: %s", sub, msg)
	}
	return fmt.Errorf("git %s: %w", sub, err)
}

// filterArgs are the -c options that turn off every clean, smudge and
// process filter the configuration where loc points names (and that none
// is required), read there just before the command.
func filterArgs(ctx context.Context, cwd string, loc, args []string) ([]string, error) {
	if noFilters[args[0]] {
		return nil, nil
	}
	names, err := filterNames(ctx, cwd, loc)
	if err != nil {
		return nil, err
	}
	if len(names) > maxFilters {
		return nil, fmt.Errorf("git: the configuration names more than %d filters", maxFilters)
	}
	out := make([]string, 0, 8*len(names))
	for _, n := range names {
		if strings.ContainsAny(n, "=\n") {
			return nil, fmt.Errorf("git: the configuration names a filter %q that cannot be turned off", n)
		}
		out = append(out,
			"-c", "filter."+n+".clean=",
			"-c", "filter."+n+".smudge=",
			"-c", "filter."+n+".process=",
			"-c", "filter."+n+".required=false")
	}
	return out, nil
}

// filterKeys matches the keys that give a filter a program.
const filterKeys = `^filter\..*\.(clean|smudge|process)$`

// maxFilterAnswer bounds what filterNames reads of git's answer, the keys
// that give a filter a program: an answer past it is refused whole, and
// the command is not run.
const maxFilterAnswer = 64 << 10

// filterNames lists the names of the filters the configuration where loc
// points gives a program, each once, the empty name too ([filter ""],
// which the attribute filter= selects), from an answer within
// maxFilterAnswer.
func filterNames(ctx context.Context, cwd string, loc []string) ([]string, error) {
	argv := append(baseArgs(), loc...)
	argv = append(argv, "config", "-z", "--name-only", "--get-regexp", filterKeys)
	answer, errs := &Head{N: maxFilterAnswer + 1}, &Head{N: maxStderr}
	err := execGitTo(ctx, cwd, argv, nil, answer, errs)
	out, stderr := answer.String(), errs.String()
	var exit *exec.ExitError
	switch {
	case err == nil && len(out) > maxFilterAnswer:
		return nil, fmt.Errorf("git: the configuration's filter keys pass %d bytes", maxFilterAnswer)
	case err == nil:
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.As(err, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(stderr) == "":
		// No key matches.
		return nil, nil
	default:
		return nil, gitError(ctx, "config", stderr, err)
	}
	var names []string
	seen := map[string]bool{}
	for _, key := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		rest, ok := strings.CutPrefix(key, "filter.")
		i := strings.LastIndexByte(rest, '.')
		if !ok || i < 0 {
			continue
		}
		if name := rest[:i]; !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}
