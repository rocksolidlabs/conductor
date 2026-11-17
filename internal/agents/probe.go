package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proc"
)

// Probe says how an adapter's agent identifies itself: the program is run
// with Args (its version flag) and its output matched. Match says it is the
// agent, with the version in the first group; Reject names a known impostor
// of the same program name (the Go migrations tool that is also called
// goose). Verified says Match was checked against the real CLI: a verified
// probe that matches nothing names the program as not the agent and a crew
// with it is refused; an unverified one only says the agent went
// unidentified. The nightly recipes job uploads every CLI's version output
// to close the unverified ones.
type Probe struct {
	Args     []string
	Match    *regexp.Regexp
	Reject   *regexp.Regexp
	Verified bool
}

// ProbeResult is what one run of a probe found.
type ProbeResult struct {
	// Ran is false when the program could not be run (Error says why).
	Ran bool
	// Identified says Match matched; Version is its first group.
	Identified bool
	Version    string
	// Impostor says Reject matched: the program is a known other one.
	Impostor bool
	// Output is the first non-empty line the program printed (at most 200
	// bytes), kept when it was not identified so a person can see what is
	// there.
	Output string
	Error  string
	// Retry says the run failed on its own side, not the program's: the
	// watch of its exit failed, and the program was ended. It says nothing
	// of the program, so it is not kept: the next ask probes again.
	Retry bool
}

const (
	probeMaxOutput = 4096
	probeMaxLine   = 200
)

// probeTimeout bounds a probe: the program is killed past it. A variable
// so that the tests of what a probe reads can give a loaded machine more
// (a shell, a pipe and a sort take longer to start under the race
// detector on a busy Mac than the 3 s a real probe gets; #281).
var probeTimeout = 3 * time.Second

// watchProbe is the watch of a probe's exit (proc.WatchExit); a variable
// so that a test can make it fail under way.
var watchProbe = proc.WatchExit

// RunProbe runs the agent's command (its program resolved, then the rest
// of its argv: an agent run as `npx codex` or `bash stub.sh` keeps those)
// with p.Args after it and reads the output: argv only, no shell, stdin
// from nothing, in a temporary directory of its own, a spartan environment
// (PATH, HOME, LANG, TMPDIR, TERM=dumb, NO_COLOR=1) plus the agent's own
// env, at most probeMaxOutput bytes of stdout and stderr together, within
// probeTimeout. A non-zero exit still counts: the output is what matters.
func RunProbe(ctx context.Context, command []string, p Probe, env map[string]string) ProbeResult {
	return new(ProbeProcess).Run(ctx, command, p, env)
}

// ProbeProcess is one run of a probe (RunProbe) that its caller can end at
// any moment (End): the server's stop ends its probes so (#961). The
// program runs in a process group of its own. Its exit, or the run's end
// (its timeout, ctx), is seen before it is reaped (proc.WatchExit), and the
// group, its id still the program's, is killed then, whatever the program
// answered: a run hands back no group with members. A descendant that left
// the group (a session of its own) is reached by neither.
type ProbeProcess struct {
	// Timeout bounds the run in place of probeTimeout when set: the tests'
	// way to a probe that only its caller ends.
	Timeout time.Duration

	mu    sync.Mutex
	ended bool
	cmd   *exec.Cmd // started and not reaped yet
}

// errProbeEnded is the answer of a run its caller ended (End).
const errProbeEnded = "the probe was ended"

// End ends the run now, on the caller's goroutine: a program not started
// yet is not started, and one started has its process group, and itself,
// sent SIGKILL, unless it is reaped already.
func (r *ProbeProcess) End() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended = true
	if r.cmd != nil {
		endProbe(r.cmd)
	}
}

// endProbe kills cmd's process group and cmd itself (should it have left
// the group); called under the run's lock, before the reaping.
func endProbe(cmd *exec.Cmd) {
	endGroup(cmd)
	_ = cmd.Process.Kill()
}

// Run runs the probe once (RunProbe).
func (r *ProbeProcess) Run(ctx context.Context, command []string, p Probe, env map[string]string) ProbeResult {
	if len(command) == 0 {
		return ProbeResult{Error: "no command"}
	}
	timeout := probeTimeout
	if r.Timeout > 0 {
		timeout = r.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "conductor-probe-")
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	defer os.RemoveAll(dir)
	args := append(append([]string{}, command[1:]...), p.Args...)
	cmd := exec.Command(command[0], args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.Env = probeEnv(env)
	cmd.WaitDelay = 500 * time.Millisecond
	var out limitedBuffer
	out.max = probeMaxOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	probeAttr(cmd)
	timedOut := ProbeResult{Error: fmt.Sprintf("timeout: the program did not answer within %d s", int(timeout/time.Second))}
	r.mu.Lock()
	switch {
	case r.ended:
		r.mu.Unlock()
		return ProbeResult{Error: errProbeEnded}
	case ctx.Err() != nil:
		r.mu.Unlock()
		return timedOut
	}
	err = cmd.Start()
	if err == nil {
		r.cmd = cmd
	}
	r.mu.Unlock()
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	exited, reap, werr := watchProbe(cmd)
	if werr == nil {
		select {
		case werr = <-exited:
			// nil: the exit seen; an error: the watch failed under way while
			// the program may run still.
		case <-ctx.Done():
		}
	}
	r.mu.Lock()
	endProbe(cmd)
	r.cmd = nil
	ended := r.ended
	r.mu.Unlock()
	if werr != nil {
		// As a usage read: an exit that cannot be watched is not guessed
		// at, and the kill's exit is no answer; the program was ended above,
		// while its group was its own, and is reaped now.
		if reap == nil {
			reap = cmd.Wait
		}
		_ = reap()
		return ProbeResult{Error: fmt.Sprintf("the exit of %s could not be watched: %v", filepath.Base(command[0]), werr), Retry: true}
	}
	err = reap()
	res := ProbeResult{Ran: true}
	var exit *exec.ExitError
	switch {
	case ended:
		return ProbeResult{Error: errProbeEnded}
	case ctx.Err() != nil:
		// Killed at the deadline (or the asker's), whatever the exit says.
		return timedOut
	case err == nil, errors.As(err, &exit):
		// A non-zero exit is still an answer.
	default:
		res.Ran, res.Error = false, err.Error()
		return res
	}
	text := out.String()
	if p.Reject != nil && p.Reject.MatchString(text) {
		res.Impostor = true
	} else if p.Match != nil {
		if m := p.Match.FindStringSubmatch(text); m != nil {
			res.Identified = true
			if len(m) > 1 {
				res.Version = strings.TrimSpace(m[1])
			}
		}
	}
	if !res.Identified {
		res.Output = firstLine(text)
	}
	return res
}

// probeEnv is the environment a probe runs with.
func probeEnv(agentEnv map[string]string) []string {
	var out []string
	for _, k := range []string{"PATH", "HOME", "LANG", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	out = append(out, "TERM=dumb", "NO_COLOR=1")
	for k, v := range agentEnv {
		if k == "" || strings.HasPrefix(k, "CONDUCTOR_") || strings.ContainsAny(k, "=\x00") {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(stripANSI(line))
		if line == "" {
			continue
		}
		if len(line) > probeMaxLine {
			line = line[:probeMaxLine]
		}
		return line
	}
	return ""
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// limitedBuffer keeps the first max bytes written and drops the rest. A
// write is always taken whole (its length returned): os/exec's copier
// reads a shorter count as an error and closes the pipe, and the program
// would die writing on.
type limitedBuffer struct {
	b   bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		l.b.Write(p)
	}
	return n, nil
}

func (l *limitedBuffer) String() string { return l.b.String() }

// ProbeFor returns the probe of the adapter agentID, nil when it has none.
func ProbeFor(agentID string) *Probe {
	a, ok := Get(agentID)
	if !ok || a.Probe == nil {
		return nil
	}
	p := *a.Probe
	return &p
}
