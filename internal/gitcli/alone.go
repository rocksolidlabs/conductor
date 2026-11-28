package gitcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"runtime"
	"time"
)

// Alone is how the gits of a new project's first commit run
// (crew.EnsureFirstCommit), hardened as Run is and more:
//
//   - in the folder its caller opened (Dir), never by a path looked up
//     again where the system can start git in an open directory (startIn);
//     elsewhere by Path, which is checked to name Dir before git starts and
//     again after it ends (ErrFolderMoved);
//   - with the repository and its work tree named outright (GitDir,
//     WorkTree, --git-dir and --work-tree, relative to the folder), so that
//     no configuration decides where git writes; a judgement's reads leave
//     both empty and git finds the repository from the folder as Run does;
//   - in a session of its own with no terminal and no input, its whole
//     process group (a signing program git starts among it) ended once git
//     exits or ctx ends, whichever is first, and its wait bounded
//     (aloneWaitDelay), so that nothing it started outlives the call and
//     the call returns at ctx's end.
//
// Config is more -c settings after safeConfig's (user.useConfigOnly), Env
// more of git's environment beside the safe one.
type Alone struct {
	Dir              *os.File
	Path             string
	GitDir, WorkTree string
	Config           []string
	Env              map[string]string
}

// ErrFolderMoved is Alone's git where the folder's path no longer names
// the folder its caller opened, before git started or once it ended.
var ErrFolderMoved = errors.New("git: the folder is no longer at its path")

// maxAloneOut bounds what Alone.Run keeps of git's standard output.
const maxAloneOut = 1 << 20

// aloneWaitDelay bounds Alone's wait for git's output once its process
// group is ended: a process that left the group and holds the pipes does
// not hold the call.
const aloneWaitDelay = 2 * time.Second

// Run runs git with args as Alone says, its standard output kept up to
// maxAloneOut (ErrOutputLimit past it). A failure carries the line that
// says why; ctx's error when it ended.
func (a Alone) Run(ctx context.Context, args ...string) (string, error) {
	out := &bounded{n: maxAloneOut, over: func() {}}
	if err := a.run(ctx, args, out); err != nil {
		return "", err
	}
	if out.past {
		return "", ErrOutputLimit
	}
	return string(out.buf), nil
}

// Each runs git with args as Alone says and hands its standard output to
// each record by record, split on NUL (a last record with no NUL after it
// too): each copies what it keeps and answers false once it has all it may
// take, which ends git; stopped then says so, with no error.
func (a Alone) Each(ctx context.Context, each func(record []byte) bool, args ...string) (stopped bool, err error) {
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &records{each: each, stop: cancel}
	err = a.run(run, args, w)
	if w.stopped && ctx.Err() == nil {
		return true, nil
	}
	if err == nil && len(w.rec) > 0 && !each(w.rec) {
		return true, nil
	}
	return false, err
}

// records splits what is written to it on NUL and hands each record to
// each until it answers false, then stop is called and the rest dropped.
type records struct {
	each    func([]byte) bool
	stop    func()
	rec     []byte
	stopped bool
}

func (r *records) Write(p []byte) (int, error) {
	n := len(p)
	for !r.stopped {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			r.rec = append(r.rec, p...)
			break
		}
		rec := append(r.rec, p[:i]...)
		p = p[i+1:]
		if !r.each(rec) {
			r.stopped = true
			r.stop()
		}
		r.rec = rec[:0]
	}
	return n, nil
}

// run runs git with args where a says, its standard output to stdout.
func (a Alone) run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("git: no command")
	}
	cwd, same, err := a.where()
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(a.Dir)
	var loc []string
	if a.GitDir != "" {
		loc = append(loc, "--git-dir="+a.GitDir)
	}
	if a.WorkTree != "" {
		loc = append(loc, "--work-tree="+a.WorkTree)
	}
	env := safeEnv
	if len(a.Env) > 0 {
		env = maps.Clone(safeEnv)
		maps.Copy(env, a.Env)
	}
	ex := func(argv []string, out, errw io.Writer) error {
		if !same() {
			return ErrFolderMoved
		}
		err := execAlone(ctx, cwd, argv, env, out, errw)
		if !same() {
			return ErrFolderMoved
		}
		return err
	}
	filters, err := filterArgsBy(ctx, loc, args, ex)
	if err != nil {
		return err
	}
	argv := baseArgs()
	for _, kv := range a.Config {
		argv = append(argv, "-c", kv)
	}
	argv = append(argv, filters...)
	argv = append(argv, loc...)
	argv = append(argv, args[0])
	argv = append(argv, commandOptions(args[0])...)
	argv = append(argv, args[1:]...)
	stderr := &Head{N: maxBoundedStderr}
	if err := ex(argv, stdout, stderr); err != nil {
		if errors.Is(err, ErrFolderMoved) {
			return err
		}
		return gitError(ctx, args[0], stderr.String(), err)
	}
	return nil
}

// where is the working directory git starts in, and what says the folder
// is still the one opened: the open directory itself where the system
// starts a process in one (always the same); else Path, checked against
// Dir. Without Dir it is Path, unchecked (a judgement's reads by path, the
// folder's check).
func (a Alone) where() (string, func() bool, error) {
	always := func() bool { return true }
	if a.Dir == nil {
		if a.Path == "" {
			return "", nil, errors.New("git: no folder to run in")
		}
		return a.Path, always, nil
	}
	cwd, err := startIn(a.Dir)
	if err == nil {
		return cwd, always, nil
	}
	if !errors.Is(err, ErrNoDirHandle) || a.Path == "" {
		return "", nil, err
	}
	want, err := a.Dir.Stat()
	if err != nil {
		return "", nil, err
	}
	return a.Path, func() bool {
		fi, err := os.Stat(a.Path)
		return err == nil && os.SameFile(fi, want)
	}, nil
}
