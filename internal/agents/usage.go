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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
// result line, a malformed line, an output bound passed.
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
	Words   string        // why, for every state but ok; at most 300 bytes
	Version string        // the version this read's own check found; "" when none
	Outcome Outcome       // what the run proved (Claude Code); Inconclusive for Codex
	Plan    string        // the subscription or plan type as the tool says it; at most 40 bytes
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
	Currency  string // at most 16 bytes
}

const (
	usageMaxLine     = 1 << 20 // one output line
	usageMaxOutput   = 4 << 20 // all of a program's output
	usageMaxStderr   = 4 << 10 // what is kept of its stderr, for its first line
	usageMaxWords    = 300
	usageMaxPlan     = 40
	usageMaxName     = 80
	usageMaxLimit    = 40
	usageMaxFigure   = 32
	usageMaxCurrency = 16
	usageMaxWindows  = 16
	usageMaxCredits  = 4
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

	// afterVersion runs between the version's print and the second note
	// of the program's files. A seam: a test replaces a file there.
	afterVersion = func() {}

	errUsageTimeout   = errors.New("usage read timed out")
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

// resolveProgram resolves command[0] to its file (exec.LookPath, then
// filepath.EvalSymlinks), and every later absolute element that names a
// regular file (the script a wrapper runs) the same way, and notes each
// file's identity. A relative element is kept as it is: what it names
// depends on the folder the program runs in, and its version's print is
// then the gate.
func resolveProgram(command []string) (program, error) {
	if len(command) == 0 || command[0] == "" {
		return program{}, errors.New("no command")
	}
	words := make([]string, 0, len(command))
	for _, c := range command {
		words = append(words, filepath.Base(c))
	}
	p := program{name: clip(strings.Join(words, " "), 80)}
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
		if filepath.IsAbs(x) {
			if fi, err := os.Stat(x); err == nil && fi.Mode().IsRegular() {
				if x, err = note(x); err != nil {
					return p, err
				}
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
// nothing else started.
func qualify(ctx context.Context, in UsageInput, probe *Probe, tool, min string) (program, string, *UsageReport) {
	stop := func(state, words, version string) *UsageReport {
		return &UsageReport{State: state, Words: clip(words, usageMaxWords), Version: version}
	}
	if in.Dir == "" {
		return program{}, "", stop(UsageError, "no folder to read in", "")
	}
	p, err := resolveProgram(in.Command)
	if err != nil {
		return p, "", stop(UsageError, err.Error(), "")
	}
	res := runProbeIn(ctx, p.with(probe.Args...), *probe, usageEnv(in.Env), in.Dir)
	switch {
	case !res.Ran:
		return p, "", stop(UsageUnqualified, "the version of "+p.name+" could not be checked: "+res.Error, "")
	case res.Impostor, !res.Identified, res.Version == "":
		return p, "", stop(UsageUnqualified, p.name+" does not answer as "+tool, "")
	}
	afterVersion()
	if !p.unchanged() {
		return p, "", stop(UsageUnqualified, "the program changed while it was checked", res.Version)
	}
	if versionLess(res.Version, min) {
		return p, "", stop(UsageUnqualified, tool+" "+res.Version+" is read only from "+min, res.Version)
	}
	return p, res.Version, nil
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

// usageProc is one program a read runs: its output read line by line
// under the bounds, its stderr kept to its first usageMaxStderr bytes.
type usageProc struct {
	ctx    context.Context
	cmd    *exec.Cmd
	lines  *boundedLines
	stdin  io.WriteCloser
	stderr *limitedBuffer
	ended  bool
}

// startUsage starts argv in the read's folder and environment, in a
// process group of its own that ctx's end kills; withStdin gives it a
// pipe, otherwise its stdin is nothing.
func startUsage(ctx context.Context, argv []string, in UsageInput, withStdin bool) (*usageProc, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = in.Dir
	cmd.Env = usageEnv(in.Env)
	cmd.WaitDelay = time.Second
	p := &usageProc{ctx: ctx, cmd: cmd, stderr: &limitedBuffer{max: usageMaxStderr}}
	cmd.Stderr = p.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if withStdin {
		if p.stdin, err = cmd.StdinPipe(); err != nil {
			return nil, err
		}
	}
	probeAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.lines = &boundedLines{r: bufio.NewReaderSize(out, 64<<10)}
	return p, nil
}

// end closes the program's stdin, kills its process group and waits for
// it. Idempotent.
func (p *usageProc) end() {
	if p.ended {
		return
	}
	p.ended = true
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	endGroup(p.cmd)
	_ = p.cmd.Wait()
}

// why says, for people, how the program's read ended without an answer:
// its deadline, a bound, or its end with the first line of its stderr
// (cut). It ends the program first, so its stderr is whole.
func (p *usageProc) why(tool string, err error, timeout time.Duration) string {
	p.end()
	switch {
	case context.Cause(p.ctx) == errUsageTimeout:
		return fmt.Sprintf("%s did not answer within %d s", tool, int(timeout/time.Second))
	case p.ctx.Err() != nil:
		return "the read was stopped"
	case errors.Is(err, errLineTooLong):
		return tool + " printed a line longer than 1 MiB"
	case errors.Is(err, errOutputTooLarge):
		return tool + " printed more than 4 MiB"
	case errors.Is(err, io.EOF), errors.Is(err, os.ErrClosed):
		w := tool + " ended before it answered"
		if line := firstLine(p.stderr.String()); line != "" {
			w += ": " + line
		}
		return clip(w, usageMaxWords)
	}
	return clip(tool+": "+err.Error(), usageMaxWords)
}

// boundedLines reads lines of at most usageMaxLine bytes, and at most
// usageMaxOutput bytes in all, applying both while it reads.
type boundedLines struct {
	r     *bufio.Reader
	total int
}

// next returns the next non-empty line, trimmed; io.EOF at the end.
func (b *boundedLines) next() ([]byte, error) {
	for {
		var line []byte
		for {
			chunk, err := b.r.ReadSlice('\n')
			b.total += len(chunk)
			if b.total > usageMaxOutput {
				return nil, errOutputTooLarge
			}
			if len(bytes.TrimRight(line, "\r\n"))+len(bytes.TrimRight(chunk, "\r\n")) > usageMaxLine {
				return nil, errLineTooLong
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

// figure is a provider's number as it wrote it, at most usageMaxFigure
// bytes; nil for null or absent.
func figure(n *string, field string) (*string, error) {
	if n == nil {
		return nil, nil
	}
	if len(*n) > usageMaxFigure {
		return nil, fmt.Errorf("%s is longer than %d bytes", field, usageMaxFigure)
	}
	v := *n
	return &v, nil
}

// percent is a provider's share used, 0 to 100.
func percent(text string, field string) (int, error) {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || f < 0 || f > 100 {
		return 0, fmt.Errorf("%s is not a share from 0 to 100", field)
	}
	return int(f + 0.5), nil
}
