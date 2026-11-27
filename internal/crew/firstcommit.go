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
// new project has none. EnsureFirstCommit makes that first commit in one
// situation, a new folder (judgeFolder): nothing named .git in it (no
// folder, file or link of that name) and no repository above it. Every
// other folder with no commit (a repository with no commit, whatever it
// holds, a folder below one, a .git that is a file or a link) is refused
// with one sentence (errMakeItYourself), so that the person makes it, and
// nothing is made: Conductor runs no git that writes in a .git it did not
// make (issue 1179).
//
// What it makes, and how:
//
//   - The repository is built aside, in a folder of Conductor's own under
//     .conductor/ (made as a launch makes it, never through a link, and
//     held by a lock of its own while it is built), the folder its work
//     tree; the tree is added and committed there, and the finished
//     repository is put at <folder>/.git without replacing anything
//     (putInPlace). Until then the folder has no .git, so a failure or a
//     stop of the server at any moment leaves it as it was but for that
//     folder of Conductor's own, which the next attempt removes once it can
//     take its lock.
//   - Every git names its repository and its work tree outright and runs
//     in the folder its caller opened (gitcli.Alone), in a session of its
//     own whose process group is ended at the git's bound (Wait, and
//     CommitWait for the commit, the step that runs the person's signing
//     program), so the call returns there.
//   - What goes in: the folder's files as git add --all takes them into the
//     new repository's empty index, its ignore files and git's own excludes
//     respected; never Conductor's .conductor/ (at any depth, left out of
//     the measure and the add by the same pathspecs), nor a repository
//     nested in it (left out, never added as a link). An empty folder gets
//     an empty commit.
//   - A bound: never a home folder or a file system's root, nor more than
//     MaxFirstCommitFiles files or MaxFirstCommitBytes bytes, measured
//     before anything is written (stopping at the bound) and again on the
//     tree the add staged, before the commit is made.
//   - Whose name and whose signature: the person's own settings, wherever
//     this server's environment says git keeps them (personSettings). The
//     person's name when they configured one (user.useConfigOnly), else
//     Conductor's (firstCommitName, firstCommitEmail) for this one commit,
//     written into no settings. Signed when their settings say so
//     (commit.gpgSign), the commit given what a signer needs to reach its
//     agent (signerReach); a commit that cannot be signed is refused, never
//     made unsigned.
//   - The first branch is git's configured default, else main. The commit's
//     message says who made it and why.
//   - Once: two at once in one process make one commit, the second finds
//     it (firstCommitMu); across processes the step that puts the
//     repository in place lets one through.
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

// msgMakeItYourself is the refusal of a folder with no commit that is no
// new folder: Conductor makes nothing there.
const msgMakeItYourself = "This folder has no commit yet. Make its first commit yourself, then launch"

var errMakeItYourself = &repoError{msgMakeItYourself}

// personSettings are the variables of the server's own environment that
// say where the person's git keeps its settings, handed to every git of a
// first commit: GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM (the files git
// reads in place of the global and system ones), GIT_CONFIG_NOSYSTEM (no
// system file) and XDG_CONFIG_HOME (where the global $XDG_CONFIG_HOME/git/
// config is). Each is a place, none a secret, and none a setting itself
// (GIT_CONFIG_COUNT and its pairs, which are, are not handed on); HOME
// comes through for every child already. With them the person's name and
// signing are found wherever they keep them.
var personSettings = []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "XDG_CONFIG_HOME"}

// signerReach are the variables a signing program needs to reach the
// person's agent, handed to the commit alone (git commit-tree, which runs
// it): SSH_AUTH_SOCK, the ssh agent's socket (an ssh key held there, a key
// git hands ssh-keygen with -U), and GNUPGHOME, gpg's home, where it finds
// its keys and its agent's socket. Each is a place, none a secret.
// GPG_TTY is not: the commit has no terminal to ask on.
var signerReach = []string{"SSH_AUTH_SOCK", "GNUPGHOME"}

// fromServer is the server's own values of names, those it has.
func fromServer(names []string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok {
			out[n] = v
		}
	}
	return out
}

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
	// GitEnv is set in every git's environment over what the server's
	// gives (personSettings): the tests' way to keep git's system and
	// global configuration out (GIT_CONFIG_NOSYSTEM).
	GitEnv map[string]string
	// hold, when set, is called at each step it names: the tests' way to
	// change the folder between two of them.
	hold func(step string)
	// commitClock, when set, is handed what starts the commit's bound,
	// which it starts when it will (at once when unset): the tests' way to
	// start it once the signing program runs.
	commitClock func(start func())
	// rename, when set, is renameNoReplace: the tests' way to a file
	// system that has no rename that refuses to replace.
	rename func(from *os.File, fromName string, to *os.File, toName string) error
}

// firstCommitMu keeps two first commits from being made at once.
var firstCommitMu sync.Mutex

// FirstCommit is what EnsureFirstCommit made: nothing (Made false) in a
// folder whose HEAD is a commit; else the folder, its repository made with
// it (Init), and the commit.
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
	g.a.Env = withEnv(g.a.Env, env)
	return g
}

// withEnv is base with env over it, base left as it is.
func withEnv(base, env map[string]string) map[string]string {
	if len(env) == 0 {
		return base
	}
	all := maps.Clone(base)
	if all == nil {
		all = map[string]string{}
	}
	maps.Copy(all, env)
	return all
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

// readFolderState is repoState read where g runs: whether the folder is in
// a git working tree and its top, and whether HEAD is a commit (refusal
// nil), by the same rev-parse calls and the same answers.
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

// judgeFolder answers whether the folder g runs in is a new folder, path
// its path and lstat a look at a name in it that follows no link: st as
// repoState reads it, and newFolder, or the refusal a launch makes (nil
// for a folder with a commit). It makes nothing, and runs no git but
// rev-parse's reads under gitcli's safe configuration. err is ErrNoGit,
// ctx's, or a *headError.
func judgeFolder(ctx context.Context, g gitAt, lstat func(name string) (fs.FileInfo, error), path string) (st State, newFolder bool, refusal, err error) {
	st, refusal, err = readFolderState(ctx, g)
	if err != nil || refusal == nil {
		return st, false, refusal, err
	}
	switch refusal {
	case errNotInRepo:
		fi, err := lstat(".git")
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return st, false, &repoError{"the folder's .git cannot be looked at: " + err.Error()}, nil
		case fi.IsDir():
			// A .git folder git does not read as a repository: git's own
			// refusal, and Conductor makes nothing over it.
			return st, false, errNotInRepo, nil
		default:
			return st, false, errMakeItYourself, nil
		}
		if why := notCommittedIn(path); why != nil {
			return st, false, why, nil
		}
		return st, true, nil, nil
	case errNoCommit:
		// A repository with no commit, whatever it holds: Conductor writes
		// in no .git it did not make.
		return st, false, errMakeItYourself, nil
	}
	return st, false, refusal, nil
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
// is a new folder, and reports what it made; a folder whose HEAD is a
// commit is left as it is, and every other is refused with nothing made
// (see above). The error matches ErrNotRepo for a folder it makes nothing
// in (refused, past the bound, a commit git refused or did not finish in
// time, a repository that could not be put in place), is ErrNoGit when git
// is not on PATH, or ctx's.
func EnsureFirstCommit(ctx context.Context, in FirstCommitIn) (FirstCommit, error) {
	if err := checkGit(); err != nil {
		return FirstCommit{}, err
	}
	if in.Folder == nil || !filepath.IsAbs(in.Path) {
		return FirstCommit{}, errors.New("a first commit is made in a folder its caller opened")
	}
	if !firstCommitHere {
		return FirstCommit{}, &firstCommitError{"Conductor makes no first commit on this system. Make the folder's first commit yourself, then launch"}
	}
	dir, err := in.Folder.Open(".")
	if err != nil {
		return FirstCommit{}, err
	}
	defer dir.Close()
	f := &firstCommitter{in: in, dir: dir, wait: cmp.Or(in.Wait, FirstCommitWait)}
	f.commitWait = cmp.Or(in.CommitWait, f.wait)
	env := withEnv(fromServer(personSettings), in.GitEnv)
	f.g = gitAt{a: gitcli.Alone{Dir: dir, Path: in.Path, Env: env}, wait: f.wait}
	firstCommitMu.Lock()
	defer firstCommitMu.Unlock()
	_, isNew, refusal, err := judgeFolder(ctx, f.g, in.Folder.Lstat, in.Path)
	switch {
	case err != nil:
		return FirstCommit{}, err
	case refusal != nil:
		return FirstCommit{}, refusal
	case isNew:
		return f.newFolder(ctx)
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
// project's .conductor/, each followed by 16 hex digits; asideLock is the
// file beside each that its builder holds locked while it builds.
const (
	asidePrefix = "first-commit-"
	asideLock   = ".lock"
)

// asideName reports whether name is one of the folders a first commit is
// built in: asidePrefix and 16 hex digits, nothing else.
func asideName(name string) bool {
	rest, ok := strings.CutPrefix(name, asidePrefix)
	if !ok || len(rest) != 16 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}

// newFolder makes the first commit of a new folder: measured through a
// repository of the server's own outside the folder, then built aside
// under .conductor/ and put at .git without replacing anything.
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
	commit, err := f.commit(ctx, repo, nested)
	if err != nil {
		return FirstCommit{}, err
	}
	f.hold("rename")
	if err := f.putInPlace(cdir, name); err != nil {
		return FirstCommit{}, err
	}
	return FirstCommit{Made: true, Init: true, Top: f.in.Path, Commit: commit}, nil
}

// putInPlace moves the repository built in .conductor/name to the
// folder's .git without replacing anything there: by one rename that
// refuses to replace (renameNoReplace), or, on a file system that has
// none, by an exclusive make of .git and a plain rename over that empty
// folder (renameOverMade). Something at .git refuses it in the sentence;
// a step the file system cannot take, in its true reason.
func (f *firstCommitter) putInPlace(cdir *os.File, name string) error {
	rename := renameNoReplace
	if f.in.rename != nil {
		rename = f.in.rename
	}
	err := rename(cdir, name, f.dir, ".git")
	if err != nil && noReplaceUnsupported(err) {
		err = renameOverMade(cdir, name, f.dir, ".git")
	}
	switch {
	case err == nil:
		return nil
	case placeTaken(err):
		// Something is at .git now: it is left as it is, and Conductor's
		// repository goes (aside's done).
		return errMakeItYourself
	}
	return &firstCommitError{"the folder's new repository could not be put in place at .git: " + err.Error()}
}

// aside makes the folder a new folder's repository is built in: under the
// project's .conductor/, made when missing and refused when it is not a
// folder (a link among them). Its lock (name.lock beside it) is taken
// before the folder is made and held until done; an earlier attempt's
// folder is removed first, but only one whose lock is free (another
// Conductor may be building in it), and nothing that is not a real folder
// named as these are. done removes the folder, its lock, and .conductor/
// when this call made it and it is empty again.
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
		if asideName(e.Name()) {
			sweep(croot, e.Name())
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	name = asidePrefix + hex.EncodeToString(b[:])
	lock, err := croot.OpenFile(name+asideLock, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		cdir.Close()
		return fail(err)
	}
	unlock := func() {
		_ = croot.Remove(name + asideLock)
		lock.Close()
	}
	if ok, err := lockFile(lock); err != nil || !ok {
		unlock()
		cdir.Close()
		return fail(cmp.Or(err, errors.New("the first commit's lock is held")))
	}
	// The mode git's own mkdir gives a new .git: what the umask leaves.
	if err := croot.Mkdir(name, 0o777); err != nil {
		unlock()
		cdir.Close()
		return fail(err)
	}
	return name, cdir, func() {
		_ = croot.RemoveAll(name)
		unlock()
		cdir.Close()
		croot.Close()
		gone()
	}, nil
}

// sweep removes the folder an earlier attempt left in .conductor/ under
// name, with its lock, when it is a real folder and its lock can be taken
// now: one whose builder still holds it is left.
func sweep(croot *os.Root, name string) {
	if fi, err := croot.Lstat(name); err != nil || !fi.IsDir() {
		return
	}
	if fi, err := croot.Lstat(name + asideLock); err == nil && !fi.Mode().IsRegular() {
		return
	}
	lock, err := croot.OpenFile(name+asideLock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer lock.Close()
	if ok, err := lockFile(lock); err != nil || !ok {
		return
	}
	_ = croot.RemoveAll(name)
	_ = croot.Remove(name + asideLock)
}

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
// (an empty index: every file its ignore rules keep), stopping as soon as
// it passes the bound, which it then refuses, and gives the repositories
// nested in it, which are left out.
func (f *firstCommitter) measure(ctx context.Context, g gitAt) (nested []string, err error) {
	var files int
	var bytes int64
	_, err = g.each(ctx, func(rec []byte) bool {
		f.hold("listed")
		p := string(rec)
		switch {
		case p == "":
		case strings.HasSuffix(p, "/"):
			// git lists a repository nested in the tree as its folder.
			nested = append(nested, strings.TrimSuffix(p, "/"))
		default:
			files++
			if fi, err := f.in.Folder.Lstat(filepath.FromSlash(p)); err == nil && fi.Mode().IsRegular() {
				bytes += fi.Size()
			}
		}
		return !pastBounds(files, bytes)
	}, append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, pathspecs(nil)...)...)
	if err != nil {
		return nil, f.failed(ctx, "the folder's files could not be listed", err)
	}
	if err := pastBound(files, bytes); err != nil {
		return nil, err
	}
	return nested, nil
}

func pastBounds(files int, bytes int64) bool {
	return files > MaxFirstCommitFiles || bytes > MaxFirstCommitBytes
}

// pastBound is the refusal of a first commit past the bound, said in the
// bound's own terms (the count stops there), nil within it.
func pastBound(files int, bytes int64) error {
	switch {
	case files > MaxFirstCommitFiles:
		return &firstCommitError{fmt.Sprintf("This folder has more than %s files and no commit yet. Make its first commit yourself, then launch", grouped(MaxFirstCommitFiles))}
	case bytes > MaxFirstCommitBytes:
		return &firstCommitError{fmt.Sprintf("This folder has more than %s MB of files and no commit yet. Make its first commit yourself, then launch", grouped(MaxFirstCommitBytes>>20))}
	}
	return nil
}

// commit adds the folder to g's index (nested left out), measures the tree
// staged against the bound again, and commits it, the person's identity
// else Conductor's, signed as the person's settings say, onto the branch
// HEAD names while it has no commit.
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
	commit, err := f.commitTree(ctx, g, ident, sign, tree)
	if err != nil {
		return "", err
	}
	f.hold("commit")
	if _, err := g.run(ctx, "update-ref", "-m", "first commit (Conductor)", "HEAD", commit, strings.Repeat("0", len(commit))); err != nil {
		return "", f.failed(ctx, what, err)
	}
	return commit, nil
}

// commitTree makes the commit object of tree (git commit-tree), with
// ident in its environment and what a signer needs to reach its agent
// (signerReach), signed when sign, within the commit's bound: a clock the
// tests start themselves (commitClock), at once otherwise.
func (f *firstCommitter) commitTree(ctx context.Context, g gitAt, ident map[string]string, sign bool, tree string) (string, error) {
	const what = "the folder's first commit could not be made"
	c := g.in(g.a.GitDir, withEnv(withEnv(fromServer(signerReach), f.in.GitEnv), ident))
	c.a.Config = append(c.a.Config, "user.useConfigOnly=true")
	c.wait = 0
	cctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	start := func() {
		t := time.AfterFunc(f.commitWait, func() { cancel(context.DeadlineExceeded) })
		context.AfterFunc(cctx, func() { t.Stop() })
	}
	if f.in.commitClock != nil {
		f.in.commitClock(start)
	} else {
		start()
	}
	args := []string{"commit-tree"}
	if sign {
		args = append(args, "-S")
	}
	out, err := c.run(cctx, append(args, "-m", FirstCommitMessage, tree)...)
	commit := strings.TrimSpace(out)
	switch {
	case ctx.Err() != nil:
		return "", ctx.Err()
	case errors.Is(context.Cause(cctx), context.DeadlineExceeded):
		return "", f.failed(ctx, what, context.DeadlineExceeded)
	case err != nil || !fullCommit(commit):
		return "", f.failed(ctx, what, cmp.Or(err, fmt.Errorf("git commit-tree answered %q", commit)))
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
		return !pastBounds(files, bytes)
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
// (commit.gpgSign): git commit-tree signs only when told (-S). The new
// repository's own configuration is git init's, so the answer is the
// person's.
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
