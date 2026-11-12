package agents

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proc"
)

// The usage readers (docs/plans/capacity.md, PR-1): an agent's usage
// windows read through its own tool, a short bounded question asked on
// the server beside the identity probe. Nothing here keeps a reading or a
// record of versions: the caller does (internal/capacity).

// UsageReader reads an agent's usage through its own tool.
type UsageReader struct {
	// Qualified is the version the reader was checked against (the
	// recipes tier and the fixtures); MinVersion the oldest it reads.
	Qualified, MinVersion string
	Read                  func(ctx context.Context, in UsageInput) UsageReport
}

// UsageInput is one read's program, environment and folder.
type UsageInput struct {
	// Command is the catalog agent's program, resolved, then its argv.
	Command []string
	// Env is the environment a session of the agent gets (the launch's
	// builder), then the adapter's ConfigDirEnv for an account with a
	// folder. CONDUCTOR_* variables are dropped.
	Env []string
	// Dir is the folder the read runs in: empty, the server's own.
	Dir string
	// Admit is asked, with the version this read itself found, before
	// Claude Code's /usage run is started, whatever the version; nil
	// admits none. A read that ends at `auth status` asks none.
	Admit func(version string) (Admission, error)
}

// Admission is one run's leave to start. Done is called exactly once,
// with what the run's own output proved.
type Admission interface{ Done(o Outcome) }

// Outcome is what one Claude Code run proved. Local only for a result
// line with num_turns 0 and local_command "usage"; Turned for a result
// line with num_turns above 0 or another local_command; Inconclusive for
// every other end: a timeout, end of file or the child's exit before a
// result line, a malformed line, an output bound passed. The proof of a
// turn is read first, before what only makes a run inconclusive; the one
// table is claudeVerdict's.
type Outcome int

const (
	Inconclusive Outcome = iota // the zero value: proves nothing
	Local
	Turned
)

func (o Outcome) String() string {
	switch o {
	case Local:
		return "local"
	case Turned:
		return "turned"
	}
	return "inconclusive"
}

// The states of a report.
const (
	UsageOK          = "ok"
	UsageSignedOut   = "signed_out"
	UsageKeyPaid     = "key_paid"
	UsageUnqualified = "unqualified"
	UsageError       = "error"
)

// UsageReport is what one read found.
type UsageReport struct {
	State   string        // ok | signed_out | key_paid | unqualified | error
	Words   string        // why, for every state but ok, in Conductor's own words (never the tool's); at most 300 bytes
	Version string        // the version this read's own check found; "" when none
	Outcome Outcome       // what the run proved (Claude Code); Inconclusive for Codex
	Plan    string        // the subscription or plan type, one of the reader's known set; "" when not reported or not known
	Windows []UsageWindow // at most 16
	Credits []UsageCredit // at most 4
	Limits  UsageLimits   // the provider's refusal flags, apart from any window
	ReadAt  time.Time
}

// UsageLimits are the provider's own flags; nil (or "") is not reported,
// never false.
type UsageLimits struct {
	Reached         string // Codex's rateLimitReachedType as it says it; at most 40 bytes
	SpendControl    *bool  // Codex's spendControlReached
	OrdinaryAllowed *bool  // Codex's ordinaryUsageAllowed
}

// UsageWindow is one usage limit the provider reported. A window it did
// not report is not in a report; a figure it gave as null stays nil.
type UsageWindow struct {
	Kind     string     // the provider's word: session, weekly_all, weekly_scoped, primary, secondary
	Name     string     // for people: "5-hour", "Weekly, all models", "Weekly, <model>"; at most 80 bytes
	Minutes  int        // the window's length when the provider says it, else 0
	Percent  int        // 0 to 100, the provider's share used
	ResetsAt *time.Time // nil when the provider gave none
	Severity string     // normal | warning | critical | "" when not reported
	Active   *bool      // Claude Code's is_active; nil when not reported
	Limit    string     // Codex's limit id ("codex"); at most 40 bytes
}

// UsageCredit is a credit figure of the provider's, as it wrote it.
type UsageCredit struct {
	Kind      string // extra_usage | credits
	Enabled   *bool
	Used      *string // as the provider wrote it, a decimal string; at most 32 bytes
	Limit     *string
	Balance   *string
	Unlimited bool
	Currency  string // an ISO 4217 code, or ""
}

// The readers' bounds, each measured on what it bounds as it was read or
// is kept, never after a trim (TestUsageBoundEdges holds each at its edge).
const (
	usageMaxLine     = 1 << 20  // bytes read for one stdout line, its final '\n' alone not counted ('\r', spaces and a blank line's bytes are)
	usageMaxOutput   = 4 << 20  // every byte read from a program's stdout, from its first to its exit: lines, terminators, blank lines, and what it prints after its answer
	versionMaxOutput = 64 << 10 // every byte of a version's print, stdout and stderr together: more is no version
	usageMaxWords    = 300      // bytes of a report's words, cut on a character's boundary
	usageMaxName     = 80       // bytes of a window's name as shown, its prefix ("Weekly, ") included
	usageMaxLimit    = 40       // bytes of a limit id as the provider wrote it (decoded JSON string)
	usageMaxFigure   = 32       // bytes of a figure's JSON number text as the provider wrote it
	usageMaxWindows  = 16       // windows in a report
	usageMaxCredits  = 4        // credit figures in a report
)

var (
	// claudeUsageTimeout and codexUsageTimeout bound a whole read, every
	// program it runs included: the process group is killed past it.
	claudeUsageTimeout = 90 * time.Second
	codexUsageTimeout  = 30 * time.Second

	// withUsageTimeout gives a read its deadline. A seam: a test expires
	// it when the stub has written its pid, instead of racing a clock.
	withUsageTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeoutCause(ctx, d, errUsageTimeout)
	}

	// withVersionTimeout gives a version's print its deadline, a seam as
	// withUsageTimeout is.
	withVersionTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeoutCause(ctx, d, errVersionTimeout)
	}

	// afterVersion runs between the version's print and the second note
	// of the program's files. A seam: a test replaces a file there.
	afterVersion = func() {}

	errUsageTimeout   = errors.New("usage read timed out")
	errVersionTimeout = errors.New("version check timed out")
	errLineTooLong    = errors.New("line too long")
	errOutputTooLarge = errors.New("output too large")
)

// programFile is one file of a command, noted by its identity.
type programFile struct {
	path string
	info os.FileInfo
}

// program is a catalog command resolved to its files: argv is what runs.
type program struct {
	argv  []string
	files []programFile
	name  string // the command's words, for people
}

// resolveProgram resolves command[0], an absolute path or a name on the
// search path (a relative path is its error), to its file (exec.LookPath,
// then filepath.EvalSymlinks), and every later element that names a regular
// file (the script a wrapper runs) the same way, a relative one taken in
// dir, the folder the program runs in; it notes each file's identity, and
// the program runs with the resolved paths.
func resolveProgram(command []string, dir string) (program, error) {
	if len(command) == 0 || command[0] == "" {
		return program{}, errors.New("no command")
	}
	words := make([]string, 0, len(command))
	for _, c := range command {
		words = append(words, filepath.Base(c))
	}
	p := program{name: clip(strings.Join(words, " "), 80)}
	// An absolute path, or a name found on the search path: never a path
	// resolved against this process's working folder.
	if strings.ContainsAny(command[0], "/"+string(filepath.Separator)) && !filepath.IsAbs(command[0]) {
		return p, fmt.Errorf("%s is a path relative to a working folder: a read runs an absolute path or a program on the search path", filepath.Base(command[0]))
	}
	first, err := exec.LookPath(command[0])
	if err != nil {
		return p, fmt.Errorf("%s is not installed here", filepath.Base(command[0]))
	}
	note := func(path string) (string, error) {
		abs, err := filepath.Abs(path)
		if err == nil {
			abs, err = filepath.EvalSymlinks(abs)
		}
		var fi os.FileInfo
		if err == nil {
			fi, err = os.Stat(abs)
		}
		if err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a program file", filepath.Base(path))
		}
		p.files = append(p.files, programFile{abs, fi})
		return abs, nil
	}
	if first, err = note(first); err != nil {
		return p, err
	}
	p.argv = append(p.argv, first)
	for _, x := range command[1:] {
		at := x
		if !filepath.IsAbs(at) {
			at = filepath.Join(dir, at)
		}
		if fi, err := os.Stat(at); x != "" && err == nil && fi.Mode().IsRegular() {
			if x, err = note(at); err != nil {
				return p, err
			}
		}
		p.argv = append(p.argv, x)
	}
	return p, nil
}

// unchanged reports whether every noted file is still the one noted:
// the same file (device and inode), size and modification time.
func (p program) unchanged() bool {
	for _, f := range p.files {
		fi, err := os.Stat(f.path)
		if err != nil || !os.SameFile(fi, f.info) || fi.Size() != f.info.Size() || !fi.ModTime().Equal(f.info.ModTime()) {
			return false
		}
	}
	return true
}

// with is the program's argv followed by args.
func (p program) with(args ...string) []string {
	return append(append([]string{}, p.argv...), args...)
}

// qualify resolves the read's command, asks the whole prefix its version
// with the adapter's probe, and checks the files did not change meanwhile
// and the version is at least min. A non-nil report ends the read with
// nothing else started. timeout is the read's own, for its words.
func qualify(ctx context.Context, in UsageInput, probe *Probe, tool, min string, timeout time.Duration) (program, string, *UsageReport) {
	stop := func(state, words, version string) *UsageReport {
		return &UsageReport{State: state, Words: clip(words, usageMaxWords), Version: version}
	}
	if in.Dir == "" || !filepath.IsAbs(in.Dir) {
		return program{}, "", stop(UsageError, "no folder to read in", "")
	}
	p, err := resolveProgram(in.Command, in.Dir)
	if err != nil {
		return p, "", stop(UsageError, err.Error(), "")
	}
	version, words := askVersion(ctx, p, probe, in, tool, timeout)
	if words != "" {
		state := UsageUnqualified
		if ctx.Err() != nil {
			state = UsageError
		}
		return p, "", stop(state, words, "")
	}
	afterVersion()
	if !p.unchanged() {
		return p, "", stop(UsageUnqualified, "the program changed while it was checked", version)
	}
	if versionLess(version, min) {
		return p, "", stop(UsageUnqualified, tool+" "+version+" is read only from "+min, version)
	}
	return p, version, nil
}

// askVersion runs the whole prefix with the probe's arguments, as a read
// runs every program (startUsage), its stdout and stderr together read to
// their end, as the identity probe reads them, then ended (end: its exit,
// then its group, then its reaping) and its exit status judged. Only a run
// that exits 0 within probeTimeout, printing at most versionMaxOutput bytes
// that the probe identifies as the tool, gives a version; words say, in
// Conductor's words, why not. Output closed before the exit waits for the
// exit; a helper left holding the output goes with the group at the exit.
func askVersion(ctx context.Context, p program, probe *Probe, in UsageInput, tool string, timeout time.Duration) (version, words string) {
	vctx, cancel := withVersionTimeout(ctx, probeTimeout)
	defer cancel()
	proc, err := startUsage(vctx, p.with(probe.Args...), in, false, true)
	if err != nil {
		if w := ended(ctx, tool, timeout); w != "" {
			return "", w
		}
		return "", "the version of " + p.name + " could not be checked: it did not start"
	}
	// versionMaxOutput: every byte of the print, stdout and stderr together.
	out, rerr := io.ReadAll(io.LimitReader(proc.lines.r, versionMaxOutput+1))
	if len(out) > versionMaxOutput {
		proc.stop()
	} else {
		proc.end()
	}
	switch {
	case ended(ctx, tool, timeout) != "":
		return "", ended(ctx, tool, timeout)
	case context.Cause(vctx) == errVersionTimeout:
		return "", fmt.Sprintf("%s did not print its version within %d s", p.name, int(probeTimeout/time.Second))
	case len(out) > versionMaxOutput:
		return "", p.name + " printed more than 64 KiB for its version"
	case rerr != nil:
		return "", "the version of " + p.name + " could not be read"
	case !proc.cmd.ProcessState.Success():
		return "", "the version command of " + p.name + " failed" + proc.exitWords() + ": its version is not known"
	}
	text := string(out)
	if probe.Reject != nil && probe.Reject.MatchString(text) {
		return "", p.name + " does not answer as " + tool
	}
	if m := probe.Match.FindStringSubmatch(text); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1]), ""
	}
	return "", p.name + " does not answer as " + tool
}

// ended says, in Conductor's words, how the read's context ended: its own
// deadline (withUsageTimeout), or the caller's stop; "" while it runs.
func ended(ctx context.Context, tool string, timeout time.Duration) string {
	switch {
	case ctx.Err() == nil:
		return ""
	case context.Cause(ctx) == errUsageTimeout:
		return fmt.Sprintf("%s did not answer within %d s", tool, int(timeout/time.Second))
	}
	return "the read was stopped"
}

// versionLess compares two dotted versions number by number; a part that
// is not a number counts as 0.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// usageEnv is the read's environment: the input's, without CONDUCTOR_*
// variables or malformed entries, then TERM=dumb and NO_COLOR=1.
func usageEnv(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || strings.HasPrefix(k, "CONDUCTOR_") || strings.ContainsRune(kv, 0) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM=dumb", "NO_COLOR=1")
}

// usageProc is one program a read runs: every child of a read (the
// version's print, Claude Code's auth status and /usage run, Codex's app
// server, the recipes tier's commands) goes through startUsage and ends
// through end or stop. It starts in a session of its own (usageAttr), so
// it has no controlling terminal and its process group is its own; its
// stdin is nothing (/dev/null) or, for Codex, a pipe. Its stdout is a pipe
// of the read's own, read line by line under the bounds (with its stderr,
// for a version's print); otherwise its stderr is drained to its end and
// only counted, never kept: nothing the tool prints there reaches a
// report, whatever its size, and the pipe is never closed under it.
//
// Its end has one order, whatever the child: its exit is seen without
// reaping it (proc.WatchExit), or the deadline passes; then the rest of
// its group is killed, while the group's id is still the leader's; then it
// is reaped, and no kill is sent after; then its exit status is judged.
// The exit is what says it finished, never its output's end: a program
// that closes its output and then exits cleanly is not killed for it. When
// the leader exits, what it left in its group is killed at once, so a
// helper holding the output does not keep the read waiting, and what was
// printed before is still read from the pipe. The read's end (its
// deadline, or the caller's) kills the group and closes the output, so a
// read never waits on a process that left the group holding the pipe.
type usageProc struct {
	ctx    context.Context
	argv   []string
	cmd    *exec.Cmd
	out    *os.File // the read end of the output's pipe, ours
	lines  *boundedLines
	stdin  io.WriteCloser
	stderr *countWriter
	reap   func() error
	// exited is closed once the leader has exited (or its watch failed,
	// and it was killed).
	exited  chan struct{}
	ended   bool
	done    chan struct{}
	outOnce sync.Once

	mu     sync.Mutex
	reaped bool
}

// countWriter counts what is written to it and keeps none of it.
type countWriter struct{ n atomic.Int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n.Add(int64(len(p)))
	return len(p), nil
}

// The starter's seams, for the tests: usageStarting is called with a
// child's argv after any admission and immediately before the context's
// last look and the start; usageStarted once the child is started;
// usageWaiting when its end begins (its answer or its output's end read,
// its stdin closed), before the rest of its output is read; usageDrained
// once that rest is read to its end within the bounds, before it waits
// for the exit (never for a child past a bound, which is not waited for);
// usageReaped once it is reaped. Each is set before a read starts.
var (
	usageStarting = func([]string) {}
	usageStarted  = func([]string) {}
	usageWaiting  = func([]string) {}
	usageDrained  = func([]string) {}
	usageReaped   = func([]string) {}
)

// errNotStarted says the read's context had ended before the start: no
// process was started.
var errNotStarted = errors.New("the read had ended: nothing was started")

// startUsage starts argv in the read's folder and environment (usageProc);
// withStdin gives it a pipe, merged sends its stderr where its stdout goes.
// A context ended by the moment of the start starts nothing
// (errNotStarted): the last look is made after any admission and
// immediately before the start.
func startUsage(ctx context.Context, argv []string, in UsageInput, withStdin, merged bool) (*usageProc, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = in.Dir
	cmd.Env = usageEnv(in.Env)
	cmd.WaitDelay = time.Second
	p := &usageProc{ctx: ctx, argv: argv, cmd: cmd, stderr: &countWriter{}, exited: make(chan struct{}), done: make(chan struct{})}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	var closers []*os.File
	closeAll := func() {
		for _, f := range closers {
			_ = f.Close()
		}
	}
	closers = append(closers, r, w)
	cmd.Stdout = w
	if merged {
		cmd.Stderr = w
	} else {
		cmd.Stderr = p.stderr
	}
	var childIn *os.File
	if withStdin {
		sr, sw, err := os.Pipe()
		if err != nil {
			closeAll()
			return nil, err
		}
		closers = append(closers, sr, sw)
		cmd.Stdin, childIn, p.stdin = sr, sr, sw
	}
	usageAttr(cmd)
	usageStarting(argv)
	if ctx.Err() != nil {
		closeAll()
		return nil, errNotStarted
	}
	if err := cmd.Start(); err != nil {
		closeAll()
		return nil, err
	}
	// The child holds its ends now.
	_ = w.Close()
	if childIn != nil {
		_ = childIn.Close()
	}
	p.out = r
	p.lines = &boundedLines{r: bufio.NewReaderSize(r, 64<<10)}
	exited, reap, err := proc.WatchExit(cmd)
	if err != nil {
		// As proc.Run: an exit that cannot be watched is not guessed at;
		// the child is ended now, while its group's id is its own.
		endGroup(cmd)
		_ = cmd.Wait()
		closeAll()
		return nil, fmt.Errorf("the exit of %s could not be watched", filepath.Base(argv[0]))
	}
	p.reap = reap
	usageStarted(argv)
	go p.watch(exited)
	return p, nil
}

// watch kills the group once the leader has exited (what it left goes with
// it; nothing reaps it here), and kills the group and closes the output
// when the read's context ends, until the child is reaped.
func (p *usageProc) watch(exited <-chan error) {
	for {
		select {
		case <-exited:
			// nil: the exit seen; an error: the watch failed while the
			// leader may run still, and it is ended now. Either way the
			// group goes, and the leader is no more waited for.
			exited = nil
			p.kill()
			close(p.exited)
		case <-p.ctx.Done():
			p.kill()
			p.closeOut()
			return
		case <-p.done:
			return
		}
	}
}

// kill ends the program's process group, unless it is reaped.
func (p *usageProc) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.reaped {
		endGroup(p.cmd)
	}
}

// closeOut closes the read end of the output, once.
func (p *usageProc) closeOut() { p.outOnce.Do(func() { _ = p.out.Close() }) }

// end ends a child whose answer is read, or whose output ended, or whose
// output passed a bound: its stdin closed, what it still prints read
// through the same bounds as its answer and dropped (drain), so it is
// never held writing, then the starter's order (usageProc): its exit, or
// the read's deadline; the rest of its group killed; its reaping. A child
// past a bound, before its answer or after it, is no more read, and so
// not waited for (stop). Every reader ends every child it read through
// end, and only then decides the read (the bound passed, if any, is
// boundWords'). Idempotent; called by the read's goroutine alone.
func (p *usageProc) end() {
	if p.ended {
		return
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	usageWaiting(p.argv)
	p.lines.drain()
	if p.lines.passed != nil {
		p.stop()
		return
	}
	usageDrained(p.argv)
	select {
	case <-p.exited:
	case <-p.ctx.Done():
	}
	p.finish()
}

// stop ends a child the read no longer reads (a bound passed): its exit is
// not waited for, since one held writing to a pipe nobody reads would
// never come, and the same order follows from there: its group killed,
// then its reaping.
func (p *usageProc) stop() {
	if p.ended {
		return
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	p.finish()
}

// finish kills the group, reaps the leader and closes the output.
func (p *usageProc) finish() {
	p.ended = true
	p.kill()
	p.mu.Lock()
	p.reaped = true
	p.mu.Unlock()
	_ = p.reap()
	close(p.done)
	p.closeOut()
	usageReaped(p.argv)
}

// exitWords says, in Conductor's words, how the ended program exited and
// how much it wrote on stderr: " (exit status 3; 47 bytes on stderr, not
// shown)", or "" when there is neither.
func (p *usageProc) exitWords() string {
	var w []string
	if st := p.cmd.ProcessState; st != nil && st.Exited() {
		w = append(w, fmt.Sprintf("exit status %d", st.ExitCode()))
	}
	// The stderr count: every byte the child wrote there, none of it kept.
	if n := p.stderr.n.Load(); n > 0 {
		w = append(w, fmt.Sprintf("%d bytes on stderr, not shown", n))
	}
	if len(w) == 0 {
		return ""
	}
	return " (" + strings.Join(w, "; ") + ")"
}

// exitedFailing says the ended program exited by itself with a status
// other than 0.
func (p *usageProc) exitedFailing() bool {
	st := p.cmd.ProcessState
	return st != nil && st.Exited() && st.ExitCode() != 0
}

// why says, in Conductor's words, how the program's read ended without an
// answer (err, what the read last got): a bound passed, its deadline, or
// its end with its exit. It ends the program first (end). The tool's own
// text is never in it.
func (p *usageProc) why(tool string, err error, timeout time.Duration) string {
	p.end()
	if w := p.boundWords(tool); w != "" {
		return w
	}
	switch {
	case ended(p.ctx, tool, timeout) != "":
		return ended(p.ctx, tool, timeout)
	case errors.Is(err, io.EOF), errors.Is(err, os.ErrClosed), errors.Is(err, io.ErrUnexpectedEOF):
		return tool + " ended before it answered" + p.exitWords()
	}
	return tool + " could not be read" + p.exitWords()
}

// boundWords says, in Conductor's words, which bound the program's output
// passed at any time of the read, before its answer or after it; "" when
// none did. A reader asks it once the program has ended (end): a bound
// passed is the read's failure, whatever answer was read before it.
func (p *usageProc) boundWords(tool string) string {
	switch {
	case errors.Is(p.lines.passed, errLineTooLong):
		return tool + " printed a line longer than 1 MiB"
	case errors.Is(p.lines.passed, errOutputTooLarge):
		return tool + " printed more than 4 MiB"
	}
	return ""
}

// boundedLines reads lines of at most usageMaxLine bytes, and at most
// usageMaxOutput bytes in all, applying both while it reads. next is the
// one path by which a reader reads a program's stdout as lines, its
// answer and what follows it alike (drain is next to the end).
type boundedLines struct {
	r     *bufio.Reader
	total int // every byte read from the output
	// passed is the first bound passed (errLineTooLong, errOutputTooLarge):
	// kept for the read's decision, and every next after it returns it.
	passed error
}

// next returns the next non-empty line, trimmed; io.EOF at the end. The
// line bound counts every byte read for the line but its final '\n',
// before any trimming: a '\r', a space and a blank line's bytes count.
func (b *boundedLines) next() ([]byte, error) {
	if b.passed != nil {
		return nil, b.passed
	}
	for {
		var line []byte
		for {
			chunk, err := b.r.ReadSlice('\n')
			b.total += len(chunk)
			if b.total > usageMaxOutput {
				b.passed = errOutputTooLarge
				return nil, b.passed
			}
			n := len(line) + len(chunk)
			if err == nil {
				n-- // the '\n' that ends the line
			}
			if n > usageMaxLine {
				b.passed = errLineTooLong
				return nil, b.passed
			}
			line = append(line, chunk...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil && (err != io.EOF || len(line) == 0) {
				return nil, err
			}
			break
		}
		if line = bytes.TrimSpace(line); len(line) > 0 {
			return line, nil
		}
	}
}

// drain reads the rest of the output through next, under both bounds,
// and drops it: to its end, its close, or the first bound passed (then
// passed), with the rest unread.
func (b *boundedLines) drain() {
	for {
		if _, err := b.next(); err != nil {
			return
		}
	}
}

// clip cuts s to at most n bytes, on a character's boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// decimal is the shape of a figure: a JSON number.
var decimal = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// figure is a provider's number as it wrote it, a decimal of at most
// usageMaxFigure bytes; nil for null or absent.
func figure(n *string, field string) (*string, error) {
	if n == nil {
		return nil, nil
	}
	if len(*n) > usageMaxFigure || !decimal.MatchString(*n) {
		return nil, fmt.Errorf("%s is not a figure Conductor reads", field)
	}
	v := *n
	return &v, nil
}

// nameShape is what a provider's name may look like to be shown: words of
// letters and digits, spaces and a little punctuation. An address, an id
// with other characters or a sentence is not a name, and fails the read.
var nameShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()+-]*$`)

// shownName is a provider's name if it has the shape of one and at most max
// bytes.
func shownName(s string, max int, field string) (string, error) {
	if len(s) > max || !nameShape.MatchString(s) {
		return "", fmt.Errorf("%s is not a name Conductor shows", field)
	}
	return s, nil
}

// reportable says a time can be carried in a report (JSON's four-digit
// years, an offset within a day).
func reportable(t time.Time) bool {
	_, err := t.MarshalJSON()
	return err == nil
}

// percent is a provider's share used, 0 to 100.
func percent(text string, field string) (int, error) {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || f < 0 || f > 100 {
		return 0, fmt.Errorf("%s is not a share from 0 to 100", field)
	}
	return int(f + 0.5), nil
}
