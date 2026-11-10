package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
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

// RunProbe runs the agent's command (its program resolved, then the rest
// of its argv: an agent run as `npx codex` or `bash stub.sh` keeps those)
// with p.Args after it and reads the output: argv only, no shell, stdin
// from nothing, in a temporary directory of its own, a spartan environment
// (PATH, HOME, LANG, TMPDIR, TERM=dumb, NO_COLOR=1) plus the agent's own
// env, at most probeMaxOutput bytes of stdout and stderr together, within
// probeTimeout (the process group is killed past it). A non-zero exit still
// counts: the output is what matters.
func RunProbe(ctx context.Context, command []string, p Probe, env map[string]string) ProbeResult {
	if len(command) == 0 {
		return ProbeResult{Error: "no command"}
	}
	dir, err := os.MkdirTemp("", "conductor-probe-")
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	defer os.RemoveAll(dir)
	return runProbeIn(ctx, append(append([]string{}, command...), p.Args...), p, probeEnv(env), dir)
}

// runProbeIn runs argv (the command and the probe's arguments) in dir with
// env and reads its answer with p, under RunProbe's bounds. The usage
// readers ask a program's version through it, with their own environment
// and folder (usage.go).
func runProbeIn(ctx context.Context, argv []string, p Probe, env []string, dir string) ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.Env = env
	cmd.WaitDelay = 500 * time.Millisecond
	var out limitedBuffer
	out.max = probeMaxOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	probeAttr(cmd)
	err := cmd.Run()
	res := ProbeResult{Ran: true}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		// Killed at the deadline (or the asker's), whatever the exit says.
		res.Ran, res.Error = false, fmt.Sprintf("timeout: the program did not answer within %d s", int(probeTimeout/time.Second))
		return res
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

// limitedBuffer keeps the first max bytes written and drops the rest.
type limitedBuffer struct {
	b   bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		l.b.Write(p)
	}
	return len(p), nil
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
