package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/pty"
)

// PipesKillWait is the most Pipes.Stop waits for the child after SIGKILL:
// Stop returns within its grace plus PipesKillWait (ext.StopKillWait).
const PipesKillWait = 5 * time.Second

// errNoPipesRunner is StartPipes's refusal on a platform without a process
// group and an exit watch to end a child through (noPipesRunner).
var errNoPipesRunner = errors.New("proc: no pipes runner on this platform")

// PipeSpec is a long-lived child to start with pipes (StartPipes).
type PipeSpec struct {
	// Argv is the program and its arguments, run as they are: never a
	// shell line.
	Argv []string
	// Dir is the working directory, absolute, and DirFile, when set, Dir as
	// its caller opened and checked it: the child starts in that directory
	// (pty.StartDir). StartPipes does not close it.
	Dir     string
	DirFile *os.File
	// Environ is the child's whole environment: it gets exactly it (none
	// when nil, never the server's own). Each entry is NAME=value, the name
	// not empty and free of NUL.
	Environ []string

	// beforeFork, set by this package's tests, is called once the start is
	// past its commitment point, just before the child is started.
	beforeFork func()
}

// Pipes is a child StartPipes started: its standard input and output are
// pipes of the caller's, its standard error is read to its end and
// counted, and it runs in a process group of its own until it exits or is
// stopped.
type Pipes struct {
	cmd     *exec.Cmd
	g       *lifecycle
	stdin   *os.File
	stdout  *os.File
	started time.Time
	stderr  atomic.Int64
	done    chan struct{}
	// exit is how the child ended, written before done is closed.
	exit      PipesExit
	askOnce   sync.Once
	closeOnce sync.Once
}

// PipesExit is how a child ended: its exit code (128 plus the signal's
// number for one a signal ended), the signal's name, and Err, what went
// wrong beyond the exit status (nil for nothing).
type PipesExit struct {
	Code   int
	Signal string
	Err    error
}

// StartPipes starts s's child: s.Argv in s.Dir (s.DirFile's directory when
// it is set) with exactly s.Environ, its standard input and output pipes
// the caller reads and writes (Stdin, Stdout), its standard error a pipe
// of StartPipes's own, read to its end and counted (StderrBytes) so that a
// child that writes to it is never held up, in a process group of its own.
// Its exit is watched without reaping: once it has exited, what it left in
// its group is killed while its id is still its own, as Run and Serve kill
// it, then it is reaped and Done closed. ctx bounds the start, never the
// child's life: an ended context is answered with its error before
// anything is made, and again at the commitment point, just before the
// child is started; past it, the child is started and the caller's. A
// child that cannot start is an error, with nothing left running. It is
// refused where there is no process group or exit watch to end a child
// through (noPipesRunner), as Serve is.
func StartPipes(ctx context.Context, s PipeSpec) (*Pipes, error) {
	if err := noPipesRunner(); err != nil {
		return nil, err
	}
	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return nil, errors.New("proc: no command")
	}
	if !filepath.IsAbs(s.Dir) {
		return nil, errors.New("proc: the directory must be absolute")
	}
	for _, kv := range s.Environ {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" || strings.ContainsRune(k, 0) {
			return nil, fmt.Errorf("proc: %q in Environ is not NAME=value", kv)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("proc: %w", err)
	}
	dir, err := pty.StartDir(s.Dir, s.DirFile)
	if err != nil {
		return nil, fmt.Errorf("proc: start %s: %w", s.Argv[0], err)
	}
	cmd := exec.Command(s.Argv[0], s.Argv[1:]...)
	cmd.Dir = dir
	// Exactly Environ: an empty environment when it is nil, never the
	// server's own, which exec gives a nil one.
	cmd.Env = append([]string{}, s.Environ...)
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	pipe := func() (r, w *os.File, err error) {
		if r, w, err = os.Pipe(); err == nil {
			files = append(files, r, w)
		}
		return r, w, err
	}
	inR, inW, err := pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := pipe()
	if err != nil {
		closeAll()
		return nil, err
	}
	errR, errW, err := pipe()
	if err != nil {
		closeAll()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	pipesAttr(cmd)
	g := pipesLifecycle()

	// The commitment point: an ended context here is answered with its
	// error and nothing is started; past it the child is started and the
	// caller's, whatever the context does.
	if err := ctx.Err(); err != nil {
		closeAll()
		return nil, fmt.Errorf("proc: %w", err)
	}
	if s.beforeFork != nil {
		s.beforeFork()
	}
	started := time.Now()
	err = cmd.Start()
	// The child has changed into DirFile's directory by now, or failed to.
	runtime.KeepAlive(s.DirFile)
	// The child holds its ends now.
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, fmt.Errorf("proc: start %s: %w", s.Argv[0], err)
	}
	// The exit is watched without reaping, so that the group can be killed
	// while the exited child still holds its id. Without a watch the child
	// is ended now, while that holds for certain.
	exited, wait, werr := watchExit(cmd, g)
	if werr != nil {
		g.signal(cmd, sigKill)
		g.reap(cmd)
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, errors.New("proc: the exit could not be watched, so the child was ended: " + werr.Error())
	}
	p := &Pipes{cmd: cmd, g: g, stdin: inW, stdout: outR, started: started, done: make(chan struct{})}
	go p.drain(errR)
	go p.end(exited, wait)
	return p, nil
}

// drain reads the child's standard error to its end, counting it, and
// closes it: nothing of the child is held up writing there.
func (p *Pipes) drain(r *os.File) {
	defer r.Close()
	io.Copy(counter{&p.stderr}, r)
}

// counter counts the bytes written to it and keeps none.
type counter struct{ n *atomic.Int64 }

func (c counter) Write(b []byte) (int, error) {
	c.n.Add(int64(len(b)))
	return len(b), nil
}

// end waits for the child's exit, kills what it left in its group while
// its id is still its own, reaps it and closes done. A watch that failed
// saw no exit: the group, the child with it, is killed all the same, and
// Exit says so.
func (p *Pipes) end(exited <-chan error, wait func() error) {
	watchErr := <-exited
	p.g.signal(p.cmd, sigKill)
	err := wait()
	st := pipesExit(p.cmd.ProcessState, err)
	if watchErr != nil {
		st.Err = errors.New("proc: the exit could not be watched, so the child was ended: " + watchErr.Error())
	}
	p.exit = st
	close(p.done)
}

// Stdin is the child's standard input.
func (p *Pipes) Stdin() io.WriteCloser { return p.stdin }

// Stdout is the child's standard output.
func (p *Pipes) Stdout() io.Reader { return p.stdout }

// Done is closed once the child has exited, what it left in its group has
// been killed, and it has been reaped.
func (p *Pipes) Done() <-chan struct{} { return p.done }

// Exit is how the child ended once Done is closed; before, Code -1.
func (p *Pipes) Exit() PipesExit {
	select {
	case <-p.done:
		return p.exit
	default:
		return PipesExit{Code: -1}
	}
}

// StderrBytes is how many bytes the child and its group have written to
// its standard error so far.
func (p *Pipes) StderrBytes() int64 { return p.stderr.Load() }

// Process is the child's pid, its group (its own) and when it started.
func (p *Pipes) Process() Process {
	return Process{PID: p.cmd.Process.Pid, Group: group(p.cmd), StartedAt: p.started}
}

// Stop ends the child: nothing is signalled once it has exited; otherwise
// its group is asked to end (SIGTERM, sent once whoever calls Stop, as a
// service's is), the child waited for until grace passes or ctx ends, and
// then the group killed (SIGKILL) and the child waited for at most
// PipesKillWait. nil once it has been reaped (Done closed), what it left in
// its group killed with it; an error if not. It closes the pipes, the
// caller's ends of them, in every case, and may be called more than once,
// and from more than one goroutine.
func (p *Pipes) Stop(ctx context.Context, grace time.Duration) error {
	defer p.closePipes()
	select {
	case <-p.done:
		return nil
	default:
	}
	p.askOnce.Do(func() { p.g.signal(p.cmd, sigTerm) })
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	p.g.signal(p.cmd, sigKill)
	kill := time.NewTimer(PipesKillWait)
	defer kill.Stop()
	select {
	case <-p.done:
		return nil
	case <-kill.C:
		return errors.New("proc: the child did not exit after SIGKILL")
	}
}

// closePipes closes the caller's ends of the child's input and output,
// once.
func (p *Pipes) closePipes() {
	p.closeOnce.Do(func() {
		p.stdin.Close()
		p.stdout.Close()
	})
}
