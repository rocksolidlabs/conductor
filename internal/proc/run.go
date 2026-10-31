// Package proc runs one command outside a PTY, bounded: an argv array and
// never a shell line, a directory, a timeout, the output capped at both
// ends, what the command leaves in its process group ended with it. The
// managed-run checks (docs/plans/agent-harness-order.md) and a project
// manifest's steps (docs/plans/project-manifests.md) are Run's callers; a
// manifest's services are Serve's, a command that runs until it is
// stopped (SIGTERM to its group, SIGKILL after a grace). A probe and git
// have runners of their own.
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/pty"
)

// Bounds a run is held to.
const (
	// MaxTimeout bounds Spec.Timeout.
	MaxTimeout = 2 * time.Hour
	// WaitDelay is how long Run waits, once the process has exited and its
	// group is killed, for the output pipe to close: a process that left
	// the group (setsid) and kept the output holds it open, and is left
	// alone when the delay runs out. It is also how long Run waits for a
	// stalled Log writer before leaving it behind, and how long exec waits
	// for the process to go after its context ends.
	WaitDelay = 2 * time.Second
	// DefaultHead and DefaultTail are the bytes of output kept in memory
	// when Spec says nothing: the first DefaultHead and the last DefaultTail.
	DefaultHead = 64 << 10
	DefaultTail = 16 << 10
	// DefaultLogLimit is how much Spec.Log gets in all when Spec says
	// nothing: the output's head, the dropped line and the output's last
	// bytes together. LogTail bounds those last bytes: a quarter of the
	// limit, or LogTail when that is less; logLine is what is kept for the
	// dropped line, and the head has the rest.
	DefaultLogLimit = 1 << 20
	LogTail         = 256 << 10
	logLine         = 64
	// holdGrace is how long Run gives the output pipe to close once the
	// process has exited before it takes it for held by a child: the
	// kernel closes the process's end at its exit, so the pipe closes at
	// once when nothing else holds it.
	holdGrace = 250 * time.Millisecond
)

// Spec is one command to run.
type Spec struct {
	// Argv is the program and its arguments, run as they are: never a
	// shell line.
	Argv []string
	// Dir is the working directory, absolute.
	Dir string
	// Allow names variables of Conductor's own environment passed on beside
	// the fixed allowlist (pty.BuildEnv): a server's env passthrough, or
	// the names of a project manifest's env the server approved.
	Allow []string
	// Env is set for the command, after the environment BuildEnv gives.
	// A CONDUCTOR_* name, a blocked loader or a malformed name is refused:
	// the run does not start.
	Env map[string]string
	// Inject is Conductor's own per-tree names, set last and unfiltered as
	// a session's (pty.Inject): the run, the task, the attempt, the tree's
	// state directory, its preview and its ports. Any other name is
	// refused (injectable), so no token comes in by this door: the run
	// does not start.
	Inject map[string]string
	// Timeout is required by Run: the process group is killed past it, and
	// at most MaxTimeout. A service has no timeout of its life: Serve's,
	// when given, bounds its start alone.
	Timeout time.Duration
	// Head and Tail are the bytes of output kept in memory, the first and
	// the last; DefaultHead and DefaultTail when zero.
	Head, Tail int
	// Log, when set, gets the output as it comes, on a goroutine of its
	// own, so a slow writer never holds the process up, and gets at most
	// LogLimit bytes in all (DefaultLogLimit when zero): everything when
	// the output fits; else its head, a line saying how many bytes were
	// dropped, and its last bytes (a quarter of LogLimit, at most
	// LogTail), the head being what the other two leave. A writer given a
	// store's bound as LogLimit holds no more than the store takes when
	// Run returns. The head waits in memory for the writer (at most the
	// limit's bytes, however the output comes); a writer that does not
	// return within WaitDelay at the end loses the rest (Result.LogErr).
	Log      io.Writer
	LogLimit int64
	// Started, when set, is called once the process exists and before any
	// of its output is read: a caller records the process before it waits.
	Started func(Process)
}

// Process identifies the process a run started: its pid, its process group
// (its own, on unix) and when it started. A pid is reused once the process
// is gone; the start time tells a later process with the same pid apart.
type Process struct {
	PID       int
	Group     int
	StartedAt time.Time
}

// Result is what became of a run.
type Result struct {
	// Process is set once the process started.
	Process Process
	// Exit is the exit status; -1 when the process was ended by a signal
	// (Signal says which) or never started (Err says why).
	Exit   int
	Signal string
	// Head and Tail are the output's first and last bytes, stdout and
	// stderr together; Total is how many bytes there were, and Truncated
	// says Head and Tail do not hold them all.
	Head, Tail []byte
	Total      int64
	Truncated  bool
	// Started and Duration are when the process started and how long the
	// run took from there, the wait for its pipe included.
	Started  time.Time
	Duration time.Duration
	// TimedOut says Timeout passed; Canceled, that the caller's context
	// ended first (cancelled, or its own deadline); Killed, that something
	// the process left behind still held its output pipe when the process
	// exited, and went with the group.
	TimedOut, Canceled, Killed bool
	// Err says why the run did not start, or what went wrong with it beyond
	// its exit status; "" otherwise. LogErr is the first error writing Log,
	// or why the log was cut.
	Err, LogErr string
}

// errTimeout is the cause of the run's own deadline, told apart from the
// caller's context ending (Canceled).
var errTimeout = errors.New("proc: timeout")

// sig is a signal lifecycle.signal sends: sigKill ends the process and its
// group at once, sigTerm asks them to end (Service.Stop).
type sig int

const (
	sigKill sig = iota
	sigTerm
)

// Run runs s and reports what became of it. The process runs in a process
// group of its own. When it has exited (on its own, or killed at the
// timeout or the context's end), and before it is reaped, the whole group
// is killed: what the process left behind goes with it, its output
// redirected or not, and the group's id is still the process's own, since
// an exited process keeps its id until it is reaped, so no other group can
// have been given it; no signal to the group is sent once the reaping has
// begun (lifecycle). The process itself is killed with the group, should it
// have joined another group. Run returns once the process is reaped and its
// output pipe is closed, or given up on after WaitDelay: a process that
// left the group (setsid) and kept the output is left alone, and Err says
// so. A Log writer still stalled then is left behind too.
func Run(ctx context.Context, s Spec) Result {
	res := Result{Exit: -1}
	head, tail, logLimit, why := s.bounds(false)
	if why != "" {
		res.Err = why
		return res
	}
	if err := ctx.Err(); err != nil {
		res.Canceled = true
		res.Err = "proc: " + err.Error()
		return res
	}
	ctx, cancel := context.WithTimeoutCause(ctx, s.Timeout, errTimeout)
	defer cancel()
	l, res, ok := launch(ctx, s, head, tail, logLimit)
	if !ok {
		return res
	}
	return l.end(ctx)
}

// bounds checks s for Run, or for Serve (service), and gives the output it
// keeps: the head and tail in memory and the log's limit. why says what
// refuses s, "" when it may start. Run requires a timeout; a service has
// none of its life (Serve: a timeout given bounds its start alone).
func (s Spec) bounds(service bool) (head, tail int, logLimit int64, why string) {
	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return 0, 0, 0, "proc: no command"
	}
	if (s.Timeout <= 0 && !service) || s.Timeout < 0 || s.Timeout > MaxTimeout {
		return 0, 0, 0, fmt.Sprintf("proc: the timeout must be between 0 and %s", MaxTimeout)
	}
	if !filepath.IsAbs(s.Dir) {
		return 0, 0, 0, "proc: the directory must be absolute"
	}
	for k := range s.Env {
		if err := checkName(k); err != nil {
			return 0, 0, 0, err.Error()
		}
	}
	for k := range s.Inject {
		if !injectable(k) {
			return 0, 0, 0, fmt.Sprintf("proc: %q is not one of Conductor's own names a command is given", k)
		}
	}
	head, tail, logLimit = s.Head, s.Tail, s.LogLimit
	if head <= 0 {
		head = DefaultHead
	}
	if tail <= 0 {
		tail = DefaultTail
	}
	if logLimit <= 0 {
		logLimit = DefaultLogLimit
	}
	return head, tail, logLimit, ""
}

// launched is a command started, its exit watched and its output read, as
// Run and Serve start one: end reads what became of it.
type launched struct {
	cmd    *exec.Cmd
	g      *lifecycle
	r      *os.File
	exited <-chan error
	wait   func() error
	mem    *capture
	lg     *logSink
	eof    chan struct{}
	res    Result
	// stop is a service's stop as end sees it (Serve); nil for Run.
	stop *stopping
}

// launch starts s's command under ctx, whose end kills its group: in a
// process group of its own, its output into a pipe of ours, its exit
// watched without reaping, Started told once it exists, and its output
// read from then on. ok is false when it did not start, or when its exit
// could not be watched (it is ended then): res says why.
func launch(ctx context.Context, s Spec, head, tail int, logLimit int64) (l *launched, res Result, ok bool) {
	res = Result{Exit: -1}
	// The output pipe is ours: the process and what it starts write to it,
	// it is read after Started, and its end of file says the last holder
	// is gone, which the wait after the exit relies on.
	r, w, err := os.Pipe()
	if err != nil {
		res.Err = err.Error()
		return nil, res, false
	}
	cmd := exec.CommandContext(ctx, s.Argv[0], s.Argv[1:]...)
	cmd.Dir = s.Dir
	cmd.Stdin = nil
	cmd.Env = environment(s.Allow, s.Env, s.Inject)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.WaitDelay = WaitDelay
	// The output's end is known to the lifecycle before anything can kill
	// the group, so that every kill sent before the end's judgement is
	// noted with what it took (lifecycle.judge).
	eof := make(chan struct{})
	g := newLifecycle(eof, r)
	attr(cmd, g)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		res.Err = err.Error()
		classify(ctx, &res)
		return nil, res, false
	}
	w.Close() // the process holds the write end now
	res.Started = started
	res.Process = Process{PID: cmd.Process.Pid, Group: group(cmd), StartedAt: started}
	// The exit is watched without reaping, so that the group can be killed
	// while the exited process still holds its id. Without a watch the
	// command is ended now, while that holds for certain.
	exited, wait, werr := watchExit(cmd, g)
	if werr != nil {
		g.signal(cmd, sigKill)
		if err := g.reap(cmd); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				res.Err = err.Error()
			}
		}
		r.Close()
		res.Duration = time.Since(started)
		if cmd.ProcessState != nil {
			res.Exit, res.Signal = exitStatus(cmd.ProcessState)
		}
		res.Err = "proc: the exit could not be watched, so the command was ended: " + werr.Error()
		classify(ctx, &res)
		return nil, res, false
	}
	if s.Started != nil {
		s.Started(res.Process)
	}

	l = &launched{cmd: cmd, g: g, r: r, exited: exited, wait: wait, mem: &capture{headMax: head, tailMax: tail}, eof: eof, res: res}
	var sink io.Writer = l.mem
	if s.Log != nil {
		l.lg = newLogSink(s.Log, logLimit)
		sink = io.MultiWriter(l.mem, l.lg)
	}
	go func() {
		defer close(l.eof)
		io.Copy(sink, r)
		if outputRead != nil {
			outputRead()
		}
	}()
	return l, res, true
}

// outputRead, when set, is called once the output's last byte is read and
// before its end is told (launched.eof): the tests' way to have the end
// come late, as it does on a loaded machine. Set it before the process
// starts.
var outputRead func()

// settled, when set, is told what settle said of the output (held) once
// it returns, before end kills the group. Set it before the process starts.
var settled func(held bool)

// outputOpen says whether the output whose end of file closes eof is open
// at this moment: read where an answer is made, never from what a wait
// last saw.
func outputOpen(eof <-chan struct{}) bool {
	select {
	case <-eof:
		return false
	default:
		return true
	}
}

// end waits for the launched command's exit, ends what it left in its
// group, reaps it and reads what became of it: the pipe given holdGrace to
// close at the exit (Killed: held by a child, which went with the group),
// or, for a service being stopped, the group given the stop's grace
// (settle), and WaitDelay after the reaping (a holder that left the group
// is left alone, and Err says so).
func (l *launched) end(ctx context.Context) Result {
	res := l.res
	watchErr := <-l.exited
	res.Duration = time.Since(res.Started)
	// The pipe closes with the process, at once, unless a child it left
	// behind holds it: that child goes with the group now. A watch that
	// failed saw no exit: the process is ended now, while its id is its own
	// for certain, and the result says so.
	held := false
	if watchErr == nil {
		held = l.settle(ctx)
		if settled != nil {
			settled(held)
		}
	}
	l.g.signal(l.cmd, sigKill)
	err := l.wait()
	if l.cmd.ProcessState != nil {
		res.Exit, res.Signal = exitStatus(l.cmd.ProcessState)
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		res.Err = err.Error()
	}
	if watchErr != nil {
		res.Err = "proc: the exit could not be watched, so the command was ended: " + watchErr.Error()
	}
	classify(ctx, &res)
	select {
	case <-l.eof:
		res.Killed = held
	case <-time.After(WaitDelay):
		// A holder that left the group: not ours to kill any more.
		l.r.Close()
		<-l.eof
		res.Err = "proc: a process that left the group still holds the output; it is left alone"
	}
	l.r.Close()
	res.Head, res.Tail, res.Total, res.Truncated = l.mem.bytes()
	if l.lg != nil {
		res.LogErr = l.lg.finish(WaitDelay)
	}
	return res
}

// settle waits, once the process has exited, for what it left in its group
// before end kills the group: the output pipe given holdGrace to close
// (held: a child still holds it). An output still open then is held by a
// member of the group only while the group has one: with none of it left,
// the output's end is read late (the goroutine that reads it can be, on a
// loaded machine) or a process that left the group holds it, which end
// gives WaitDelay and leaves alone; and the output is read again after
// the listing, which is slow on a loaded machine, as held by a process
// only while its pipe has a writer (lifecycle.held): a member left that
// redirected its output holds none of it, though the output's end is read
// late. Each answer is the
// lifecycle's judgement (judge), which reads a kill sent beside this wait
// (the command's Cancel at the context's end, or a stop's) as it was
// sent: a child holding the output that it took went with the group,
// though the listing finds none of it and the output closed with it. For
// a service whose stop has asked its group to end (Service.Stop), before
// the exit or while this waits, the group has the stop's grace instead
// (settleStopped).
func (l *launched) settle(ctx context.Context) (held bool) {
	var asked <-chan struct{}
	if l.stop != nil {
		asked = l.stop.asked
	}
	select {
	case <-asked:
		return l.settleStopped(ctx)
	default:
	}
	hold := time.NewTimer(holdGrace)
	defer hold.Stop()
	select {
	case <-l.eof:
		return l.g.judge()
	case <-hold.C:
		l.g.members(l.cmd) // none of the group left is noted for the judgement
		return l.g.judge()
	case <-asked:
		return l.settleStopped(ctx)
	}
}

// classify reads the context's end into TimedOut (the run's own deadline)
// or Canceled (the caller's context, however it ended).
func classify(ctx context.Context, res *Result) {
	if ctx.Err() == nil {
		return
	}
	if errors.Is(context.Cause(ctx), errTimeout) {
		res.TimedOut = true
		return
	}
	res.Canceled = true
}

// checkName refuses a variable name Env may not set: Conductor's own, a
// loader the sessions block too, or one that is not a name.
func checkName(k string) error {
	switch {
	case k == "" || strings.ContainsAny(k, "=\x00"):
		return fmt.Errorf("proc: %q is not a variable name", k)
	case strings.HasPrefix(k, "CONDUCTOR_"):
		return fmt.Errorf("proc: %s: a CONDUCTOR_ variable is never set for a command", k)
	case pty.Blocked(k):
		return fmt.Errorf("proc: %s is never set for a command", k)
	}
	return nil
}

// injectablePort is the NAME of CONDUCTOR_PORT_<NAME>: a project
// manifest's port name (internal/crew portNamePattern) in upper case.
var injectablePort = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,15}$`)

// injectable reports whether name is one of Conductor's own per-tree names
// a command may be given (Spec.Inject): CONDUCTOR_RUN, CONDUCTOR_TASK,
// CONDUCTOR_ATTEMPT, CONDUCTOR_STATE_DIR, CONDUCTOR_PREVIEW_URL and
// CONDUCTOR_PORT_<NAME>. A token is none of them.
func injectable(name string) bool {
	switch name {
	case "CONDUCTOR_RUN", "CONDUCTOR_TASK", "CONDUCTOR_ATTEMPT", "CONDUCTOR_STATE_DIR", "CONDUCTOR_PREVIEW_URL":
		return true
	}
	port, ok := strings.CutPrefix(name, "CONDUCTOR_PORT_")
	return ok && injectablePort.MatchString(port)
}

// environment is the command's environment: a session's, as BuildEnv builds
// it from Conductor's own (the allowlist and allow, CONDUCTOR_* and the
// blocked loaders out), plus set, then inject (Conductor's own names, which
// Run has held to injectable), then TERM=dumb and NO_COLOR=1 and no
// COLORTERM, since nothing reads the output but a log.
func environment(allow []string, set, inject map[string]string) []string {
	env := pty.BuildEnv(pty.ParentEnv(), allow, set, inject)
	out := env[:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "TERM", "COLORTERM", "NO_COLOR":
			continue
		}
		out = append(out, kv)
	}
	return append(out, "NO_COLOR=1", "TERM=dumb")
}

// capture keeps the first headMax bytes written and the last tailMax of
// the rest, and counts them all.
type capture struct {
	mu               sync.Mutex
	headMax, tailMax int
	head             []byte
	tail             []byte // a ring of tailMax bytes once full
	pos, filled      int
	total            int64
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.total += int64(n)
	if room := c.headMax - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	if len(p) == 0 || c.tailMax <= 0 {
		return n, nil
	}
	if c.tail == nil {
		c.tail = make([]byte, c.tailMax)
	}
	if len(p) >= c.tailMax {
		copy(c.tail, p[len(p)-c.tailMax:])
		c.pos, c.filled = 0, c.tailMax
		return n, nil
	}
	k := copy(c.tail[c.pos:], p)
	if k < len(p) {
		copy(c.tail, p[k:])
	}
	c.pos = (c.pos + len(p)) % c.tailMax
	c.filled = min(c.filled+len(p), c.tailMax)
	return n, nil
}

// bytes returns the head, the tail (the bytes after the head, the last
// tailMax of them), the total and whether anything between the two is lost.
func (c *capture) bytes() (head, tail []byte, total int64, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	head = append([]byte(nil), c.head...)
	if c.filled < c.tailMax {
		tail = append([]byte(nil), c.tail[:c.filled]...)
	} else {
		tail = append(append([]byte(nil), c.tail[c.pos:]...), c.tail[:c.pos]...)
	}
	return head, tail, c.total, c.total > int64(len(head)+len(tail))
}

// logSink feeds a Log writer on a goroutine of its own, so that a writer
// that blocks never holds the output pipe, the process or Run up: the
// first head bytes are queued for the writer as they come, the rest is
// kept as a tail at once, and the writer gets at most limit bytes in all,
// the dropped line and the tail included (logBounds). The queue holds at
// most the head's bytes, however the output is cut into chunks, so a
// writer that keeps up loses nothing whatever the goroutines' scheduling,
// and one that blocks costs no more than the limit.
type logSink struct {
	w     io.Writer
	limit int64
	head  int64
	done  chan struct{}
	// wake tells the writer's goroutine to look at the queue again: one
	// pending wake is enough, so a send that would block is dropped.
	wake chan struct{}

	mu        sync.Mutex
	queue     [][]byte // head bytes waiting for the writer, in order
	accepted  int64    // head bytes taken so far, queued or written
	over      int64    // bytes past the head, the tail's
	closed    bool     // finish was called: nothing more comes
	abandoned bool     // finish gave up on the writer: nothing more is written
	written   int64    // bytes the writer took
	tail      *capture
	err       error
}

func newLogSink(w io.Writer, limit int64) *logSink {
	head, tail := logBounds(limit)
	l := &logSink{w: w, limit: limit, head: head, done: make(chan struct{}), wake: make(chan struct{}, 1), tail: &capture{tailMax: int(tail)}}
	go l.run()
	return l
}

// logBounds splits a log's limit: the tail is a quarter of it, at most
// LogTail; logLine bytes are kept for the dropped line; the head has the
// rest (none, for a limit too small to keep a head beside the two).
func logBounds(limit int64) (head, tail int64) {
	tail = min(LogTail, limit/4)
	head = max(limit-tail-logLine, 0)
	return head, tail
}

func (l *logSink) Write(p []byte) (int, error) {
	n := len(p)
	l.mu.Lock()
	if room := l.head - l.accepted; room > 0 && !l.closed {
		k := int(min(room, int64(len(p))))
		l.queue = append(l.queue, append([]byte(nil), p[:k]...))
		l.accepted += int64(k)
		p = p[k:]
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
	if len(p) > 0 {
		l.over += int64(len(p))
		l.tail.Write(p)
	}
	l.mu.Unlock()
	return n, nil
}

// run is the sink's goroutine: it writes the head as it is queued, then,
// once finish closed the queue, the dropped line and the tail.
func (l *logSink) run() {
	defer close(l.done)
	for {
		l.mu.Lock()
		batch, closed, gone := l.queue, l.closed, l.abandoned
		l.queue = nil
		l.mu.Unlock()
		if gone {
			return
		}
		for _, c := range batch {
			if l.err != nil {
				break
			}
			if _, err := l.w.Write(c); err != nil {
				l.err = err
			}
			l.mu.Lock()
			l.written += int64(len(c))
			gone = l.abandoned
			l.mu.Unlock()
			if gone {
				return
			}
		}
		if closed && len(batch) == 0 {
			break
		}
		if len(batch) == 0 {
			<-l.wake
		}
	}
	l.mu.Lock()
	over, written := l.over, l.written
	l.mu.Unlock()
	if over == 0 || l.err != nil || l.gone() {
		return
	}
	_, tail, _, _ := l.tail.bytes()
	if dropped := over - int64(len(tail)); dropped > 0 {
		// Within what the head and the tail leave of the limit: the line
		// is shorter than logLine, and cut only for a limit too small to
		// hold it.
		line := fmt.Sprintf("\n… %d bytes dropped …\n", dropped)
		if room := l.limit - written - int64(len(tail)); int64(len(line)) > room {
			line = line[:max(room, 0)]
		}
		if _, err := io.WriteString(l.w, line); err != nil {
			l.err = err
			return
		}
	}
	if !l.gone() {
		if _, err := l.w.Write(tail); err != nil {
			l.err = err
		}
	}
}

// gone reports whether finish gave up on the writer.
func (l *logSink) gone() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.abandoned
}

// finish closes the queue and waits up to wait for the sink to write what
// is left; it reports why the log is short, "" when it is whole. Past the
// wait the writer is left behind: a Write in flight may still return, and
// nothing is written after it, neither the head queued nor the tail.
func (l *logSink) finish(wait time.Duration) string {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
	select {
	case <-l.done:
	case <-time.After(wait):
		l.mu.Lock()
		l.abandoned = true
		l.mu.Unlock()
		return fmt.Sprintf("the log writer did not return within %s: the log is cut", wait)
	}
	if l.err != nil {
		return l.err.Error()
	}
	return ""
}
