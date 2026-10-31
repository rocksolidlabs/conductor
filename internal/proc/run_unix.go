//go:build unix

package proc

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// lifecycle orders the signals to the process and its group against the
// reaping of the process: a signal to the group by its number is safe only
// while the process, exited or not, still holds its pid, which it does
// until it is reaped, so no signal is sent once reap has begun, and reap
// waits for a signal in flight.
type lifecycle struct {
	mu     sync.Mutex
	reaped bool
	// output is the output's end of file (launched.eof), and pipe the read
	// end of the output's pipe, which the end's judgement reads (judge,
	// held) and the first kill sent before it notes (signal).
	output <-chan struct{}
	pipe   *os.File
	// empty says a listing of the group found none of it left, the
	// process's own exit aside; killed says a SIGKILL has been sent before
	// the judgement, and killedHolder that the first, sent once the process
	// had exited, took a member of the group while a process held the
	// output (held); judged that the judgement is made, after which no
	// kill is noted.
	empty, killed, killedHolder, judged bool
	// looks counts the looks at the group (members, still), for looking.
	looks int
}

// newLifecycle is the lifecycle of a command whose output's end of file
// closes output (launched.eof), read from the pipe's read end pipe.
func newLifecycle(output <-chan struct{}, pipe *os.File) *lifecycle {
	return &lifecycle{output: output, pipe: pipe}
}

// held says whether a process holds the output at this moment: its end of
// file not read yet, and the pipe not hung up (writersGone). The end's
// goroutine can be late to the end of file on a loaded machine, so an end
// not read is no proof of a writer: a child that redirected its output,
// left in the group, holds none of it.
func (g *lifecycle) held() bool {
	if !outputOpen(g.output) {
		return false
	}
	gone, known := writersGone(g.pipe)
	return !known || !gone
}

// killScanned, when set, is called in noteKill once the group is listed
// for a kill, with the lock held: the tests' way to have the output close
// during that listing, which is slow on a loaded machine. Set it before
// the process starts.
var killScanned func(g *lifecycle)

// looking, when set, is called before each look at the group, outside the
// lifecycle's lock: n numbers the looks from 1, and listing says it is a
// listing (members) rather than a follow (still). Its answer makes a
// listing fail. It is the tests' way to order a look against a member's
// end or a kill, and to the path a listing that fails takes. Set it before
// the process starts.
var looking func(n int, listing bool) (fail bool)

// look numbers a look at the group for looking, and says whether a
// listing is to fail.
func (g *lifecycle) look(listing bool) (fail bool) {
	if looking == nil {
		return false
	}
	g.mu.Lock()
	g.looks++
	n := g.looks
	g.mu.Unlock()
	return looking(n, listing)
}

// judge says, once the process has exited, whether what end kills holds
// the output: read once, under the lock the kills take, so a kill beside
// the end's wait (Service.Stop's cut, the command's Cancel at the
// context's end) is read as it was sent, whatever came after it. A kill
// that took a member holding the output (killedHolder) says so, though
// the member is gone from a later listing and the output closed with it;
// none of the group left (empty), nothing of it holds the output, however
// late its end is read; else the output is held when a process holds it at
// this moment (held), not when its end is only read late. No kill is noted
// after it.
func (g *lifecycle) judge() (held bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.judged = true
	switch {
	case g.killedHolder:
		return true
	case g.empty:
		return false
	}
	return g.held()
}

// noteKill notes the first SIGKILL sent before the judgement (judge),
// with the lock held and before it is sent: whether it takes a member of
// the group that holds the output. It does when the process has exited
// (one that runs holds the output itself, and the wait after its exit
// reads what it left), a process holds the output (held), and the group,
// listed now, has a member, or cannot be listed; a listing that finds none
// is noted as one of the end's (empty), and a group already found empty
// has none to take. The output is read again after the listing, which is
// slow on a loaded machine: a holder that finished during it, a member
// that redirected its output left, is no holder the kill takes.
func (g *lifecycle) noteKill(pid int) {
	if g.judged || g.killed {
		return
	}
	g.killed = true
	if !g.held() || g.empty || !exited(pid) {
		return
	}
	pids, err := groupMembers(pid, pid)
	if killScanned != nil {
		killScanned(g)
	}
	if err == nil && len(pids) == 0 {
		g.empty = true
		return
	}
	g.killedHolder = g.held()
}

// signalled, when set, is told each signal signal sends, once it is sent:
// to the group, or to the process alone (alone). It is the tests' way to
// see what is sent, and to let a process handle the group's SIGTERM before
// anything more is sent. Set it before the process starts.
var signalled func(s sig, alone bool)

// signal sends s to the process's group, and to the process itself should
// it have joined another group (Setpgid makes a group; it does not keep the
// process in it): sigKill kills them, sigTerm asks them to end (a service's
// stop). A SIGTERM reaches the process once: it is sent one of its own only
// when it is no longer in the group, since a second, come after its handler
// has put the default back while it cleans up, would end it before its
// grace. One whose group cannot be read is sent none: unreaped, its pid
// is still its own, so it has exited and needs none. A SIGKILL, which no
// handler sees, goes to it either way; the first before the end's
// judgement is noted (noteKill). It is the one place proc signals a
// process. os.ErrProcessDone once the process is reaped, or before it
// exists.
func (g *lifecycle) signal(cmd *exec.Cmd, s sig) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reaped || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid
	n := syscall.SIGKILL
	if s == sigTerm {
		n = syscall.SIGTERM
	} else {
		g.noteKill(pid)
	}
	syscall.Kill(-pid, n)
	if signalled != nil {
		signalled(s, false)
	}
	if s == sigTerm {
		if pgid, err := syscall.Getpgid(pid); err != nil || pgid == pid {
			return nil
		}
	}
	err := cmd.Process.Signal(n)
	if signalled != nil {
		signalled(s, true)
	}
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// members is the processes of cmd's process group that have not exited,
// cmd's own left out, and known whether the group could be listed; none
// found is noted for the judgement (judge), under the lock a kill takes,
// so a kill after it is known to take nothing. It is
// asked only before the reaping, while the exited process keeps the
// group's id its own, so the answer is about this group and no later one;
// the group is listed (groupMembers), never signalled, since a signal 0 to
// it would reach the exited process itself, which stays in the group until
// it is reaped.
func (g *lifecycle) members(cmd *exec.Cmd) (pids []int, known bool) {
	fail := g.look(true)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reaped || cmd.Process == nil {
		return nil, true
	}
	if fail {
		return nil, false
	}
	pids, err := groupMembers(cmd.Process.Pid, cmd.Process.Pid)
	if err == nil && len(pids) == 0 {
		g.empty = true
	}
	return pids, err == nil
}

// still is those of pids, members found before, that are still in cmd's
// process group and have not exited, asked as members is.
func (g *lifecycle) still(cmd *exec.Cmd, pids []int) []int {
	g.look(false)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reaped || cmd.Process == nil {
		return nil
	}
	return stillMembers(cmd.Process.Pid, pids)
}

// reap waits for the process (exec reaps it), after which no signal is sent.
func (g *lifecycle) reap(cmd *exec.Cmd) error {
	g.mu.Lock()
	g.reaped = true
	g.mu.Unlock()
	return cmd.Wait()
}

// attr starts the command in a process group of its own and, when its
// context ends (the timeout, or the caller's), kills the group and the
// process through the lifecycle, so never after the reaping.
func attr(cmd *exec.Cmd, g *lifecycle) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return g.signal(cmd, sigKill) }
}

// noServiceRunner is nil here: Serve runs a service. A test makes it the
// refusal of a platform without a process group (run_other.go).
var noServiceRunner = func() error { return nil }

// group is the command's process group: its own pid (Setpgid).
func group(cmd *exec.Cmd) int { return cmd.Process.Pid }

// exitStatus reads the exit status, or the signal that ended the process.
func exitStatus(state *os.ProcessState) (int, string) {
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return state.ExitCode(), ""
	}
	if ws.Signaled() {
		return -1, ws.Signal().String()
	}
	return ws.ExitStatus(), ""
}

// errExited says the process had exited before its exit could be watched:
// it is then a zombie, unreaped, its group's id still its own.
var errExited = errors.New("proc: exited already")

// watchFails, when set, makes every exit watch fail: the tests' way to the
// path a watch that cannot be set up takes.
var watchFails = false

// watchExit reports the process's exit without reaping it (watchPID: a
// waitid without reaping on Linux, a kqueue on macOS) and gives the wait
// that reaps it after. The channel gives nil for an exit seen, or the
// error of a watch that failed while the process may be alive still. A
// process gone before the watch was set up counts as exited. A watch that
// cannot be set up is an error: Run then ends the command rather than
// guess at its exit, since a signal to the group after the reaping could
// reach another's.
func watchExit(cmd *exec.Cmd, g *lifecycle) (<-chan error, func() error, error) {
	exited := make(chan error, 1)
	if watchFails {
		return nil, nil, errors.New("the watch was made to fail")
	}
	stop, err := watchPID(cmd.Process.Pid, func(err error) { exited <- err })
	switch {
	case errors.Is(err, errExited):
		exited <- nil
		return exited, func() error { return g.reap(cmd) }, nil
	case err != nil:
		return nil, nil, err
	}
	return exited, func() error {
		err := g.reap(cmd)
		stop()
		return err
	}, nil
}
