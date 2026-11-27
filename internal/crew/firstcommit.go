package crew

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// A crew whose members work on copies of their own (isolation "worktree",
// an agent trial among them) needs a commit of its folder to copy, and a
// new project has none. EnsureFirstCommit makes that first commit in
// exactly two situations (judgeFolder), and in every other makes nothing
// and says one sentence (errMakeItYourself), so that the person makes it
// (issue 1179):
//
//   - a new folder: nothing named .git in it (no folder, file or link of
//     that name), and git finds no repository above it;
//   - a repository just started: the folder is itself its top, its .git a
//     real folder in it that is git's directory for it (no common
//     directory elsewhere), with no commit and no reference of any kind,
//     and its own configuration (read with its includes off, so that an
//     include shows as a key) holding only keys that run no program and
//     move no write (knownKey).
//
// What it makes, and how:
//
//   - In a new folder the repository is built aside, in a folder of
//     Conductor's own under .conductor/ (made as a launch makes it, never
//     through a link), the folder its work tree; the tree is added and
//     committed there, and the finished repository is put at <folder>/.git
//     by one rename that fails if anything is there (renameNoReplace).
//     Until then the folder has no .git, so a failure or a stop of the
//     server at any moment leaves it as it was but for that folder of
//     Conductor's own, which the next attempt removes.
//   - In a repository just started the add goes into an index of
//     Conductor's own inside it, never the person's, and the commit is put
//     on the branch only from nothing (update-ref HEAD <commit> <none>): if
//     a commit appeared meanwhile, Conductor's is dropped and the launch
//     goes on from the person's. The person's index is brought to the
//     commit once it exists. A failure leaves the repository as it was but
//     for objects nobody refers to.
//   - Every git names its repository and its work tree outright and runs
//     in the folder its caller opened (gitcli.Alone), in a session of its
//     own whose process group is ended at the git's bound (Wait, and
//     CommitWait for the commit, the step that runs the person's signing
//     program), so the call returns there.
//   - What goes in: the folder's files as git add --all takes them into an
//     empty index, its ignore files and git's own excludes respected; never
//     Conductor's .conductor/ (at any depth, left out of the measure and
//     the add by the same pathspecs), nor a repository nested in it (left
//     out, never added as a link). An empty folder gets an empty commit.
//   - A bound: never a home folder or a file system's root, nor more than
//     MaxFirstCommitFiles files or MaxFirstCommitBytes bytes, measured
//     before anything is written and again on the tree the add staged,
//     before the commit is made.
//   - Whose name: the person's when git finds one they configured
//     (user.useConfigOnly), else Conductor's (firstCommitName,
//     firstCommitEmail) for this one commit, written into no settings.
//   - Signing: as the person's own settings say (commit.gpgSign in their
//     global or system configuration: the folder's own may name no signer,
//     knownKey), never overridden and never waited on past the bound.
//   - A new repository's first branch is git's configured default, else
//     main. The commit's message says who made it and why.
//   - Once: two at once in one process make one commit, the second finds
//     it (firstCommitMu); across processes the rename, or the reference set
//     from nothing, lets one through.
//
// Its callers call it for the operator's own launch and proposal alone,
// with the folder they resolved and opened (openCwd): a folder agents may
// not use never reaches it.

// The bound of what Conductor commits by itself.
const (
	MaxFirstCommitFiles = 5_000
	MaxFirstCommitBytes = 200 << 20
)

// firstCommitName and firstCommitEmail name a first commit for a person
// who configured no git identity. The address is in the reserved .invalid
// domain (RFC 2606): plainly no mailbox.
const (
	firstCommitName  = "Conductor"
	firstCommitEmail = "conductor@noreply.invalid"
)

// FirstCommitMessage is the message of a first commit Conductor makes: who
// made it and why, in one line, for whoever reads git log later.
const FirstCommitMessage = "First commit, made by Conductor so that agents could each work on a copy of this folder"

// FirstCommitWait bounds each git of a first commit where its caller sets
// no bound of its own.
const FirstCommitWait = 30 * time.Second

// msgMakeItYourself is the refusal of a folder with no commit that is
// neither a new folder nor a repository just started: Conductor makes
// nothing there.
const msgMakeItYourself = "This folder has no commit yet. Make its first commit yourself, then launch"

var errMakeItYourself = &repoError{msgMakeItYourself}

// FirstCommitIn is what EnsureFirstCommit is handed: the crew's folder as
// its caller opened it (Folder, through its allowed root) and resolved it
// (Path), and the bounds of its gits.
type FirstCommitIn struct {
	Folder *os.Root
	Path   string
	// Wait bounds each git but the commit's, CommitWait the commit's (git
	// commit-tree, which runs the person's signing program); zero for
	// FirstCommitWait.
	Wait, CommitWait time.Duration
	// GitEnv is set in every git's environment: the tests' way to keep
	// git's system and global configuration out (GIT_CONFIG_NOSYSTEM).
	GitEnv map[string]string
	// hold, when set, is called at each step it names: the tests' way to
	// change the folder between two of them.
	hold func(step string)
}

// firstCommitMu keeps two first commits from being made at once.
var firstCommitMu sync.Mutex

// FirstCommit is what EnsureFirstCommit made: nothing (Made false) in a
// folder whose HEAD is a commit; else the folder, whether its repository
// was made too, and the commit.
type FirstCommit struct {
	Made   bool
	Init   bool
	Top    string
	Commit string
}

// firstCommitError is a folder EnsureFirstCommit makes no commit in, in
// the words a launch's refusal says: it matches ErrNotRepo, as the refusal
// it stands for does.
type firstCommitError struct{ msg string }

func (e *firstCommitError) Error() string { return e.msg }
func (e *firstCommitError) Unwrap() error { return ErrNotRepo }

// newProject is what judgeFolder finds a folder to be.
type newProject int

const (
	notNew      newProject = iota // a commit to branch from, or refused
	newFolder                     // no repository at or above it yet
	justStarted                   // the top of a repository with nothing in it yet
)

// gitAt is where a git of a judgement or a first commit runs (a), within
// wait (none for zero).
type gitAt struct {
	a    gitcli.Alone
	wait time.Duration
}

// in is g with git's repository named (gitDir, relative to the folder, the
// folder its work tree), and env beside its own.
func (g gitAt) in(gitDir string, env map[string]string) gitAt {
	g.a.GitDir, g.a.WorkTree = gitDir, "."
	if len(env) > 0 {
		all := maps.Clone(g.a.Env)
		if all == nil {
			all = map[string]string{}
		}
		maps.Copy(all, env)
		g.a.Env = all
	}
	return g
}

func (g gitAt) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if g.wait <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, g.wait)
}

func (g gitAt) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := g.bound(ctx)
	defer cancel()
	return g.a.Run(ctx, args...)
}

func (g gitAt) each(ctx context.Context, each func([]byte) bool, args ...string) (bool, error) {
	ctx, cancel := g.bound(ctx)
	defer cancel()
	return g.a.Each(ctx, each, args...)
}

// readFolderState is repoState read where g runs: whether the folder is in a git
// working tree and its top, and whether HEAD is a commit (refusal nil), by
// the same rev-parse calls and the same answers.
func readFolderState(ctx context.Context, g gitAt) (st State, refusal, err error) {
	out, err := g.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return State{}, nil, ctx.Err()
		case errors.Is(err, exec.ErrNotFound):
			return State{}, nil, ErrNoGit
		}
		return State{}, classifyRevParse(err), nil
	}
	st = State{InRepo: true, Toplevel: strings.TrimSpace(out)}
	if _, err := g.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		// No commit only when git says so by its exit status alone, as
		// repoState reads it: a git that says why (an object it cannot
		// read) failed.
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

// judgeFolder answers what the folder g runs in is for a first commit,
// path its path and lstat a look at a name in it that follows no link: st
// as repoState reads it, and the kind it is (newFolder, justStarted), or
// notNew with the refusal a launch makes (nil for a folder with a commit).
// It makes nothing, and runs no git but reads under gitcli's safe
// configuration. err is ErrNoGit, ctx's, or a *headError.
func judgeFolder(ctx context.Context, g gitAt, lstat func(name string) (fs.FileInfo, error), path string) (st State, kind newProject, refusal, err error) {
	st, refusal, err = readFolderState(ctx, g)
	if err != nil || refusal == nil {
		return st, notNew, refusal, err
	}
	switch refusal {
	case errNotInRepo:
		fi, err := lstat(".git")
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return st, notNew, &repoError{"the folder's .git cannot be looked at: " + err.Error()}, nil
		case fi.IsDir():
			// A .git folder git does not read as a repository: git's own
			// refusal, and Conductor runs no git init over it.
			return st, notNew, errNotInRepo, nil
		default:
			return st, notNew, errMakeItYourself, nil
		}
		if why := notCommittedIn(path); why != nil {
			return st, notNew, why, nil
		}
		return st, newFolder, nil, nil
	case errNoCommit:
		if why, err := justStartedRefusal(ctx, g, lstat, st.Toplevel, path); why != nil || err != nil {
			return st, notNew, why, err
		}
		return st, justStarted, nil, nil
	}
	return st, notNew, refusal, nil
}

// justStartedRefusal is why the repository with no commit that holds the
// folder is no repository just started (errMakeItYourself, or the home
// folder's or a root's refusal), nil when it is one; err is ctx's.
func justStartedRefusal(ctx context.Context, g gitAt, lstat func(string) (fs.FileInfo, error), top, path string) (refusal, err error) {
	// The folder is the repository's top, and its .git a real folder.
	if !sameFolder(top, path) {
		return errMakeItYourself, nil
	}
	fi, lerr := lstat(".git")
	if lerr != nil || !fi.IsDir() {
		return errMakeItYourself, nil
	}
	in := g.in(".git", nil)
	read := func(args ...string) (string, bool) {
		out, err := in.run(ctx, args...)
		return out, err == nil
	}
	// git's directory for it is that folder, and its common directory too
	// (no commondir that sends its objects and its configuration elsewhere).
	dirs, ok := read("rev-parse", "--absolute-git-dir", "--git-common-dir")
	lines := strings.Split(strings.TrimSpace(dirs), "\n")
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case !ok || len(lines) != 2 || !sameAs(lines[0], fi) || lines[1] != ".git" && !(filepath.IsAbs(lines[1]) && sameAs(lines[1], fi)):
		return errMakeItYourself, nil
	}
	// Its own configuration holds only keys that run nothing and move no
	// write, includes read as keys.
	config, ok := read("config", "--file", filepath.Join(".git", "config"), "--no-includes", "-z", "--list")
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !ok || !knownConfig(config) {
		return errMakeItYourself, nil
	}
	// HEAD names a branch, and no reference of any kind exists.
	head, ok := read("symbolic-ref", "-q", "HEAD")
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !ok || !strings.HasPrefix(strings.TrimSpace(head), "refs/heads/") {
		return errMakeItYourself, nil
	}
	refs, ok := read("for-each-ref", "--count=1", "--format=%(refname)")
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !ok || strings.TrimSpace(refs) != "" {
		return errMakeItYourself, nil
	}
	return notCommittedIn(path), nil
}

// sameAs reports whether path names the file fi describes.
func sameAs(path string, fi fs.FileInfo) bool {
	got, err := os.Stat(path)
	return err == nil && os.SameFile(got, fi)
}

// knownConfig reports whether raw, a configuration file as git config
// --no-includes -z --list prints it, holds only keys knownKey knows.
func knownConfig(raw string) bool {
	if raw == "" {
		return true
	}
	for rec := range strings.SplitSeq(strings.TrimSuffix(raw, "\x00"), "\x00") {
		key, value, hasValue := strings.Cut(rec, "\n")
		if !knownKey(key, value, hasValue) {
			return false
		}
	}
	return true
}

// knownKey reports whether a repository's own configuration may hold key
// (as git lists it: its section and name in lower case) with value, for a
// first commit Conductor makes there: what git init writes (format version
// 0, not bare), a name and an address, remotes' addresses and fetch lines,
// branches' remote and merge, the default branch's name. None names a
// program git runs or a place it writes: core.filemode, ignorecase,
// precomposeunicode and symlinks change how git reads the folder's names
// and modes, core.logallrefupdates whether a reference's log is kept, a
// remote's address is used only by a fetch or a push, which Conductor
// never makes (gitcli allows no transport). Any other key, an include
// among them, is not known, and the folder is refused: a gate never trims
// what it does not read.
func knownKey(key, value string, hasValue bool) bool {
	section, rest, ok := strings.Cut(key, ".")
	if !ok {
		return false
	}
	sub, name := "", rest
	if i := strings.LastIndexByte(rest, '.'); i >= 0 {
		sub, name = rest[:i], rest[i+1:]
	}
	switch {
	case section == "core" && sub == "":
		switch name {
		case "repositoryformatversion":
			return hasValue && value == "0"
		case "bare":
			return hasValue && value == "false"
		case "filemode", "logallrefupdates", "ignorecase", "precomposeunicode", "symlinks":
			return true
		}
	case section == "user" && sub == "":
		return name == "name" || name == "email"
	case section == "init" && sub == "":
		return name == "defaultbranch"
	case section == "remote" && sub != "":
		return name == "url" || name == "fetch"
	case section == "branch" && sub != "":
		return name == "remote" || name == "merge"
	}
	return false
}

// notCommittedIn is why Conductor makes no first commit in dir by itself:
// it is a file system's root or the server user's home folder; nil else.
func notCommittedIn(dir string) error {
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) == clean {
		return &repoError{"This folder is a file system's root and has no commit yet. Make its first commit yourself, or pick a project's folder"}
	}
	if home, err := os.UserHomeDir(); err == nil && sameFolder(home, clean) {
		return &repoError{"This folder is the home folder and has no commit yet. Make its first commit yourself, or pick a project's folder"}
	}
	return nil
}

// EnsureFirstCommit makes the first commit of the folder in names when it
// is a new folder or a repository just started, and reports what it made;
// a folder whose HEAD is a commit is left as it is, and every other is
// refused with nothing made (see above). The error matches ErrNotRepo for
// a folder it makes nothing in (refused, past the bound, a commit git
// refused or did not finish in time), is ErrNoGit when git is not on PATH,
// or ctx's.
func EnsureFirstCommit(ctx context.Context, in FirstCommitIn) (FirstCommit, error) {
	if err := checkGit(); err != nil {
		return FirstCommit{}, err
	}
	if in.Folder == nil || !filepath.IsAbs(in.Path) {
		return FirstCommit{}, errors.New("a first commit is made in a folder its caller opened")
	}
	dir, err := in.Folder.Open(".")
	if err != nil {
		return FirstCommit{}, err
	}
	defer dir.Close()
	f := &firstCommitter{in: in, dir: dir, wait: cmp.Or(in.Wait, FirstCommitWait)}
	f.commitWait = cmp.Or(in.CommitWait, f.wait)
	f.g = gitAt{a: gitcli.Alone{Dir: dir, Path: in.Path, Env: in.GitEnv}, wait: f.wait}
	firstCommitMu.Lock()
	defer firstCommitMu.Unlock()
	_, kind, refusal, err := judgeFolder(ctx, f.g, in.Folder.Lstat, in.Path)
	switch {
	case err != nil:
		return FirstCommit{}, err
	case refusal != nil:
		return FirstCommit{}, refusal
	case kind == newFolder && !firstCommitHere:
		// No rename here that fails when something is there: nothing is
		// built.
		return FirstCommit{}, errMakeItYourself
	case kind == newFolder:
		return f.newFolder(ctx)
	case kind == justStarted:
		return f.justStarted(ctx)
	}
	return FirstCommit{}, nil
}

// firstCommitter is one first commit under way.
type firstCommitter struct {
	in               FirstCommitIn
	dir              *os.File
	wait, commitWait time.Duration
	g                gitAt // the folder, its repository found by git
}

func (f *firstCommitter) hold(step string) {
	if f.in.hold != nil {
		f.in.hold(step)
	}
}

// failed is the refusal of a step of the first commit that err failed:
// ctx's when it ended, else in words, a git past its bound said so.
func (f *firstCommitter) failed(ctx context.Context, what string, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return &firstCommitError{what + ": git did not finish within its bound, and was ended"}
	}
	var fc *firstCommitError
	var re *repoError
	if errors.As(err, &fc) || errors.As(err, &re) {
		return err
	}
	return &firstCommitError{what + ": " + err.Error()}
}

// asidePrefix names the folders a first commit is built in, under the
// project's .conductor/.
const asidePrefix = "first-commit-"

// ownIndex is the index a first commit in a repository just started adds
// to, inside its .git: never the person's.
const ownIndex = "conductor-first-commit.index"

// newFolder makes the first commit of a new folder: measured through a
// repository of the server's own outside the folder, then built aside
// under .conductor/ and put at .git by one rename.
func (f *firstCommitter) newFolder(ctx context.Context) (FirstCommit, error) {
	tmp, err := os.MkdirTemp("", "conductor-first-commit-")
	if err != nil {
		return FirstCommit{}, err
	}
	defer os.RemoveAll(tmp)
	outside := f.g.in(tmp, nil)
	if _, err := outside.run(ctx, "init", "-q"); err != nil {
		return FirstCommit{}, f.failed(ctx, "the folder's files could not be listed", err)
	}
	nested, err := f.measure(ctx, outside)
	if err != nil {
		return FirstCommit{}, err
	}
	f.hold("measured")
	name, cdir, done, err := f.aside()
	if err != nil {
		return FirstCommit{}, f.failed(ctx, "the first commit's repository could not be made", err)
	}
	defer done()
	repo := f.g.in(filepath.Join(".conductor", name), nil)
	init := []string{"init", "-q"}
	if branch, err := outside.run(ctx, "config", "--get", "init.defaultBranch"); err != nil || strings.TrimSpace(branch) == "" {
		init = append(init, "--initial-branch=main")
	}
	if _, err := repo.run(ctx, init...); err != nil {
		return FirstCommit{}, f.failed(ctx, "the first commit's repository could not be made", err)
	}
	// git init records the work tree it was named; the repository put at
	// <folder>/.git finds its work tree as every repository does.
	if _, err := repo.run(ctx, "config", "--unset", "core.worktree"); err != nil {
		return FirstCommit{}, f.failed(ctx, "the first commit's repository could not be made", err)
	}
	// What git init wrote is held to the keys a repository just started
	// may hold (a template of the person's settings can add more).
	config, err := repo.run(ctx, "config", "--file", filepath.Join(".conductor", name, "config"), "--no-includes", "-z", "--list")
	if err != nil {
		return FirstCommit{}, f.failed(ctx, "the first commit's repository could not be made", err)
	}
	if !knownConfig(config) {
		return FirstCommit{}, errMakeItYourself
	}
	commit, err := f.commit(ctx, repo, nested)
	if err != nil {
		return FirstCommit{}, err
	}
	f.hold("rename")
	if err := renameNoReplace(cdir, name, f.dir, ".git"); err != nil {
		// Something is at .git now: it is left as it is, and Conductor's
		// repository goes (done).
		return FirstCommit{}, errMakeItYourself
	}
	return FirstCommit{Made: true, Init: true, Top: f.in.Path, Commit: commit}, nil
}

// aside makes the folder a new folder's repository is built in: under the
// project's .conductor/, made when missing and refused when it is not a
// folder (a link among them), an earlier attempt's folder removed first,
// the new one made exclusively. done removes it, and .conductor/ when this
// call made it and it is empty again.
func (f *firstCommitter) aside() (name string, cdir *os.File, done func(), err error) {
	root := f.in.Folder
	made := false
	fi, err := root.Lstat(".conductor")
	if errors.Is(err, fs.ErrNotExist) {
		if err = root.Mkdir(".conductor", 0o755); err != nil {
			return "", nil, nil, err
		}
		made = true
		fi, err = root.Lstat(".conductor")
	}
	if err != nil {
		return "", nil, nil, err
	}
	gone := func() {
		if made {
			_ = rmdirAt(f.dir, ".conductor")
		}
	}
	if !fi.IsDir() {
		return "", nil, nil, invalidf(".conductor in the working directory is not a folder: worktrees must stay inside the working directory")
	}
	croot, err := root.OpenRoot(".conductor")
	if err != nil {
		gone()
		return "", nil, nil, err
	}
	fail := func(err error) (string, *os.File, func(), error) {
		croot.Close()
		gone()
		return "", nil, nil, err
	}
	if got, err := croot.Stat("."); err != nil || !os.SameFile(got, fi) {
		return fail(invalidf(".conductor in the working directory changed while it was opened"))
	}
	cdir, err = croot.Open(".")
	if err != nil {
		return fail(err)
	}
	entries, err := cdir.ReadDir(-1)
	if err != nil {
		cdir.Close()
		return fail(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), asidePrefix) {
			_ = croot.RemoveAll(e.Name())
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	name = asidePrefix + hex.EncodeToString(b[:])
	if err := croot.Mkdir(name, 0o700); err != nil {
		cdir.Close()
		return fail(err)
	}
	return name, cdir, func() {
		_ = croot.RemoveAll(name)
		cdir.Close()
		croot.Close()
		gone()
	}, nil
}

// justStarted makes the first commit of a repository just started, into
// an index of Conductor's own, the branch set from nothing; a commit that
// appeared meanwhile is the person's, and Conductor's is dropped.
func (f *firstCommitter) justStarted(ctx context.Context) (FirstCommit, error) {
	index := filepath.Join(".git", ownIndex)
	clear := func() {
		_ = f.in.Folder.Remove(index)
		_ = f.in.Folder.Remove(index + ".lock")
	}
	clear()
	defer clear()
	repo := f.g.in(".git", map[string]string{"GIT_INDEX_FILE": index})
	nested, err := f.measure(ctx, repo)
	if err != nil {
		return FirstCommit{}, err
	}
	f.hold("measured")
	commit, err := f.commit(ctx, repo, nested)
	if errors.Is(err, errHeadMoved) {
		if _, refusal, rerr := readFolderState(ctx, f.g); rerr == nil && refusal == nil {
			return FirstCommit{}, nil
		}
	}
	if err != nil {
		return FirstCommit{}, err
	}
	// The person's index brought to the commit, as git's own lock takes it;
	// an index another git holds just then stays as it was.
	_, _ = f.g.in(".git", nil).run(ctx, "read-tree", "-m", "HEAD")
	return FirstCommit{Made: true, Top: f.in.Path, Commit: commit}, nil
}

// errHeadMoved is a commit that could not be put on the branch: something
// is on it now.
var errHeadMoved = &firstCommitError{"the folder got a commit while its first commit was made"}

// pathspecs are what a first commit takes of the folder: all of it but
// Conductor's own .conductor/ at any depth and each repository nested in it.
func pathspecs(nested []string) []string {
	out := []string{".", ":(exclude,glob)**/.conductor", ":(exclude,glob)**/.conductor/**"}
	for _, n := range nested {
		out = append(out, ":(exclude,literal)"+n)
	}
	return out
}

// measure lists what a first commit would take where g's repository is
// (an empty index: every file its ignore rules keep), refuses it past the
// bound, and gives the repositories nested in it, which are left out.
func (f *firstCommitter) measure(ctx context.Context, g gitAt) (nested []string, err error) {
	var files int
	var bytes int64
	_, err = g.each(ctx, func(rec []byte) bool {
		p := string(rec)
		switch {
		case p == "":
		case strings.HasSuffix(p, "/"):
			// git lists a repository nested in the tree as its folder.
			nested = append(nested, strings.TrimSuffix(p, "/"))
		default:
			files++
			if bytes <= MaxFirstCommitBytes {
				if fi, err := f.in.Folder.Lstat(filepath.FromSlash(p)); err == nil && fi.Mode().IsRegular() {
					bytes += fi.Size()
				}
			}
		}
		return true
	}, append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, pathspecs(nil)...)...)
	if err != nil {
		return nil, f.failed(ctx, "the folder's files could not be listed", err)
	}
	if err := pastBound(files, bytes); err != nil {
		return nil, err
	}
	return nested, nil
}

// pastBound is the refusal of a first commit of files files and bytes
// bytes past the bound, nil within it.
func pastBound(files int, bytes int64) error {
	switch {
	case files > MaxFirstCommitFiles:
		return &firstCommitError{fmt.Sprintf("This folder has %s files and no commit yet. Make its first commit yourself, then launch", grouped(files))}
	case bytes > MaxFirstCommitBytes:
		return &firstCommitError{fmt.Sprintf("This folder has %s MB of files and no commit yet. Make its first commit yourself, then launch", grouped(int(bytes>>20)))}
	}
	return nil
}

// commit adds the folder to g's index (nested left out), measures the tree
// staged against the bound again, and commits it, the person's identity
// else Conductor's, signed as the person's settings say, onto the branch
// HEAD names only while it has no commit (errHeadMoved otherwise).
func (f *firstCommitter) commit(ctx context.Context, g gitAt, nested []string) (string, error) {
	const what = "the folder's first commit could not be made"
	if _, err := g.run(ctx, append([]string{"add", "--all", "--"}, pathspecs(nested)...)...); err != nil {
		return "", f.failed(ctx, "the folder's files could not be added to its first commit", err)
	}
	out, err := g.run(ctx, "write-tree")
	tree := strings.TrimSpace(out)
	if err != nil || !fullCommit(tree) {
		return "", f.failed(ctx, what, cmp.Or(err, fmt.Errorf("git write-tree answered %q", tree)))
	}
	if err := f.staged(ctx, g, tree); err != nil {
		return "", err
	}
	ident, err := f.identity(ctx, g)
	if err != nil {
		return "", f.failed(ctx, what, err)
	}
	sign, err := f.signs(ctx, g)
	if err != nil {
		return "", f.failed(ctx, what, err)
	}
	c := g.in(g.a.GitDir, ident)
	c.a.Config = append(c.a.Config, "user.useConfigOnly=true")
	c.wait = f.commitWait
	args := []string{"commit-tree"}
	if sign {
		args = append(args, "-S")
	}
	out, err = c.run(ctx, append(args, "-m", FirstCommitMessage, tree)...)
	commit := strings.TrimSpace(out)
	if err != nil || !fullCommit(commit) {
		return "", f.failed(ctx, what, cmp.Or(err, fmt.Errorf("git commit-tree answered %q", commit)))
	}
	f.hold("commit")
	if _, err := g.run(ctx, "update-ref", "-m", "first commit (Conductor)", "HEAD", commit, strings.Repeat("0", len(commit))); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errHeadMoved
	}
	return commit, nil
}

// staged checks tree, what the add staged, against the bound, and refuses
// a repository it added as a link (one nested in the folder since it was
// measured).
func (f *firstCommitter) staged(ctx context.Context, g gitAt, tree string) error {
	var files int
	var bytes int64
	link := false
	_, err := g.each(ctx, func(rec []byte) bool {
		meta, _, ok := strings.Cut(string(rec), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 {
			return true
		}
		if fields[0] == "160000" {
			link = true
			return false
		}
		files++
		if n, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
			bytes += n
		}
		return true
	}, "ls-tree", "-r", "-z", "-l", "--full-tree", tree)
	switch {
	case err != nil:
		return f.failed(ctx, "the folder's first commit could not be measured", err)
	case link:
		return errMakeItYourself
	}
	return pastBound(files, bytes)
}

// identity is nil when the person configured a git identity, which git
// then puts on the commit; else Conductor's name and address for this one
// commit. One git would guess from the account and the machine's name is
// not the person's (user.useConfigOnly).
func (f *firstCommitter) identity(ctx context.Context, g gitAt) (map[string]string, error) {
	ask := g
	ask.a.Config = append(ask.a.Config, "user.useConfigOnly=true")
	for _, v := range []string{"GIT_COMMITTER_IDENT", "GIT_AUTHOR_IDENT"} {
		if _, err := ask.run(ctx, "var", v); err != nil {
			if gitcli.ExitStatus(err) < 0 {
				return nil, err
			}
			return map[string]string{
				"GIT_AUTHOR_NAME": firstCommitName, "GIT_AUTHOR_EMAIL": firstCommitEmail,
				"GIT_COMMITTER_NAME": firstCommitName, "GIT_COMMITTER_EMAIL": firstCommitEmail,
			}, nil
		}
	}
	return nil, nil
}

// signs reports whether the person's settings sign every commit
// (commit.gpgSign): git commit-tree signs only when told (-S), and the
// repository's own configuration names no signing key (knownKey), so the
// answer is the person's global or system one.
func (f *firstCommitter) signs(ctx context.Context, g gitAt) (bool, error) {
	out, err := g.run(ctx, "config", "--type=bool", "--get", "commit.gpgSign")
	switch {
	case err == nil:
		return strings.TrimSpace(out) == "true", nil
	case gitcli.ExitStatus(err) == 1:
		return false, nil
	}
	return false, err
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
