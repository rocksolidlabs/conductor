// Package pty starts commands attached to a pseudo-terminal and manages their
// lifecycle. It is used by both `conductor serve` and `conductor host`.
package pty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	creack "github.com/creack/pty"
)

// Spec describes the process to start.
type Spec struct {
	Argv []string
	Dir  string
	// DirFile, when set, is Dir as its caller opened and checked it: the
	// process starts in that directory, not in whatever Dir names by then.
	// On Linux the child changes into the open directory itself (through
	// /proc/self/fd); elsewhere, or without /proc, Dir is used once it is
	// checked to still name DirFile's directory. Start does not close it.
	DirFile *os.File
	Env     []string
	Cols    uint16
	Rows    uint16
}

// ExitStatus describes how the process ended.
type ExitStatus struct {
	Code   int
	Signal string
	Err    error
}

// Process is a running command with its controlling PTY.
type Process struct {
	f    *os.File
	cmd  *exec.Cmd
	done chan struct{}

	mu   sync.Mutex
	exit ExitStatus

	// kill signals the process group: syscall.Kill, a test's recorder in
	// the package's tests (set before Stop or Abort, never during one).
	kill func(pid int, sig syscall.Signal) error

	// hold, while open, keeps the process unreaped (StartHeld): its pid,
	// and so its group's id, stay its own however soon it exits. holding
	// says it is still open; under holdMu, which orders Abort's signal
	// before the reap.
	hold    chan struct{}
	holdMu  sync.Mutex
	holding bool
}

// KillWait is the most Stop waits for the process after SIGKILL: Stop
// returns within its grace plus KillWait.
const KillWait = 5 * time.Second

// ErrNoCommand is returned for an empty argv.
var ErrNoCommand = errors.New("pty: empty command")

// Start launches spec in a new session with a PTY of the requested size.
func Start(spec Spec) (*Process, error) { return start(spec, false) }

// StartHeld is Start with the process held unreaped until Release or
// Abort, whichever comes first: Done is not closed before one of them,
// however soon the process exits, so its pid and its group's id cannot be
// another's when Abort signals the group.
func StartHeld(spec Spec) (*Process, error) { return start(spec, true) }

func start(spec Spec, held bool) (*Process, error) {
	if len(spec.Argv) == 0 || strings.TrimSpace(spec.Argv[0]) == "" {
		return nil, ErrNoCommand
	}
	for _, a := range spec.Argv {
		if strings.ContainsRune(a, 0) {
			return nil, errors.New("pty: argument contains NUL")
		}
	}
	cols, rows := spec.Cols, spec.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	dir, err := startDir(spec)
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", spec.Argv[0], err)
	}
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = dir
	cmd.Env = spec.Env
	if cmd.Env == nil && dir != spec.Dir {
		// exec sets PWD to Dir without an environment of the caller's:
		// the directory's path, not the descriptor's.
		cmd.Env = append(os.Environ(), "PWD="+spec.Dir)
	}
	// creack/pty sets Setsid and Setctty so the child gets its own session and
	// the PTY as controlling terminal; the process group ID equals the PID.
	f, err := creack.StartWithSize(cmd, &creack.Winsize{Cols: cols, Rows: rows})
	// The child has changed into DirFile's directory by now, or failed to.
	runtime.KeepAlive(spec.DirFile)
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", spec.Argv[0], err)
	}
	p := &Process{f: f, cmd: cmd, done: make(chan struct{}), hold: make(chan struct{}), holding: held, kill: syscall.Kill}
	if !held {
		close(p.hold)
	}
	go p.wait()
	return p, nil
}

func (p *Process) wait() {
	<-p.hold
	err := p.cmd.Wait()
	st := ExitStatus{Code: -1}
	if err == nil {
		st.Code = 0
	} else {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
				if ws.Signaled() {
					st.Signal = ws.Signal().String()
					st.Code = 128 + int(ws.Signal())
				} else {
					st.Code = ws.ExitStatus()
				}
			} else {
				st.Code = ee.ExitCode()
			}
		} else {
			st.Err = err
		}
	}
	p.mu.Lock()
	p.exit = st
	p.mu.Unlock()
	close(p.done)
}

// Read reads PTY output. It returns an error once the process has exited and
// the kernel closed the slave side.
func (p *Process) Read(b []byte) (int, error) { return p.f.Read(b) }

// Write sends input to the process.
func (p *Process) Write(b []byte) (int, error) { return p.f.Write(b) }

// Resize changes the terminal window size and signals the process group.
func (p *Process) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errors.New("pty: dimensions must be positive")
	}
	return creack.Setsize(p.f, &creack.Winsize{Cols: cols, Rows: rows})
}

// Done is closed when the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Exit returns the exit status once Done is closed.
func (p *Process) Exit() ExitStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit
}

// PID returns the child process ID.
func (p *Process) PID() int { return p.cmd.Process.Pid }

// Stop sends SIGTERM to the process group, waits up to grace, then SIGKILLs.
// It always closes the PTY afterwards.
func (p *Process) Stop(ctx context.Context, grace time.Duration) error {
	defer p.f.Close()
	select {
	case <-p.done:
		return nil
	default:
	}
	pgid := -p.cmd.Process.Pid
	_ = p.kill(pgid, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	_ = p.kill(pgid, syscall.SIGKILL)
	select {
	case <-p.done:
		return nil
	case <-time.After(KillWait):
		return errors.New("pty: process did not exit after SIGKILL")
	}
}

// Release lets a held process (StartHeld) be reaped: from here it is as
// Start's. It does nothing on a process not held, or released already.
func (p *Process) Release() {
	p.holdMu.Lock()
	defer p.holdMu.Unlock()
	if p.holding {
		p.holding = false
		close(p.hold)
	}
}

// Abort ends a held process (StartHeld) and its whole group: SIGKILL to
// the group while the leader is unreaped, so the group's id is still its
// own whether or not the leader has exited, then the reap, waited for at
// most KillWait, and the PTY closed. A member that left the group (its own
// setsid) is not reached. On a process released already it is Stop with
// no grace.
func (p *Process) Abort() error {
	p.holdMu.Lock()
	if !p.holding {
		p.holdMu.Unlock()
		return p.Stop(context.Background(), 0)
	}
	_ = p.kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	p.holding = false
	close(p.hold)
	p.holdMu.Unlock()
	defer p.f.Close()
	select {
	case <-p.done:
		return nil
	case <-time.After(KillWait):
		return errors.New("pty: process did not exit after SIGKILL")
	}
}

// Close releases the PTY without signalling the process.
func (p *Process) Close() error { return p.f.Close() }
