package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// flagsHelp is a help in commander's form, in our own words.
const flagsHelp = `Usage: helper [options]

Options:
  --add-dir <directories...>        More folders it may read
  --verbose                         Says more
  --effort <level>                  How hard it thinks (low, medium, high)
  --model <model>                   The model: an alias (e.g. 'alpha', 'beta') or a full name
  --permission-mode <mode>          How it asks (choices: "ask", "bypassPermissions")
  --dangerously-skip-permissions    Asks nothing
  --session-id <uuid>               The session's id
`

// flagsSeams is a server whose version probes and help reads are the
// test's: each counted, the help's argv kept, the version and the help
// what the test sets, the cache's clock the test's.
type flagsSeams struct {
	e       *testEnv
	runs    atomic.Int32
	helps   atomic.Int32
	version atomic.Value // string
	clock   atomic.Int64
	mu      sync.Mutex
	argv    [][]string
	answer  func(n int32) (string, agents.ProbeResult)
}

// flagsProgram is an executable of the test's own, at an absolute path the
// server's lookup finds; the seams run nothing of it.
func flagsProgram(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newFlagsEnv(t *testing.T, list []catalog.Agent) *flagsSeams {
	t.Helper()
	f := &flagsSeams{answer: func(int32) (string, agents.ProbeResult) { return flagsHelp, agents.ProbeResult{Ran: true} }}
	f.version.Store("2.1.400")
	f.clock.Store(time.Now().UnixNano())
	f.e = newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), list)
	c := f.e.srv.probes
	c.now = func() time.Time { return time.Unix(0, f.clock.Load()) }
	c.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		f.runs.Add(1)
		return agents.ProbeResult{Ran: true, Identified: true, Version: f.version.Load().(string)}
	}
	c.help = func(ctx context.Context, r *agents.ProbeProcess, command, args []string, env map[string]string) (string, agents.ProbeResult) {
		n := f.helps.Add(1)
		f.mu.Lock()
		f.argv = append(f.argv, append(slices.Clone(command), args...))
		f.mu.Unlock()
		return f.answer(n)
	}
	return f
}

func (f *flagsSeams) advance(d time.Duration) { f.clock.Add(int64(d)) }

func (f *flagsSeams) ask(t *testing.T, id string) flagsReply {
	t.Helper()
	resp, out := f.e.do("GET", "/api/catalog/"+id+"/flags", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %v", id, resp.StatusCode, out)
	}
	b, _ := json.Marshal(out)
	var r flagsReply
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// askUntil asks until the reply's state is want, within a bound that only
// bounds a hang: each ask waits at most the probes' wait.
func (f *flagsSeams) askUntil(t *testing.T, id, want string) flagsReply {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; {
		r := f.ask(t, id)
		if r.State == want {
			return r
		}
		if r.State != flagsReading || time.Now().After(deadline) {
			t.Fatalf("%s: %+v, want %s", id, r, want)
		}
	}
}

func byFlagName(r flagsReply) map[string]replyFlag {
	out := map[string]replyFlag{}
	for _, f := range r.Flags {
		out[f.Name] = f
	}
	return out
}

func claudeLike(id, prog string) catalog.Agent {
	a, _ := catalog.Default().Get("claude")
	a.ID, a.Name, a.Command, a.Protocol = id, "Stub "+id, []string{prog}, nil
	return a
}

func TestFlagsRouteReadsTheHelp(t *testing.T) {
	prog := flagsProgram(t)
	f := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", prog)})
	r := f.askUntil(t, "helper", flagsRead)
	if r.Agent != "helper" || r.Name != "Stub helper" || r.Version != "2.1.400" || r.Why != "" || len(r.Flags) != 7 {
		t.Fatalf("%+v", r)
	}
	flags := byFlagName(r)
	if flags["--model"].Set != agents.SetModel || flags["--dangerously-skip-permissions"].Set != agents.SetYolo || flags["--session-id"].Set != agents.SetSession {
		t.Errorf("sets: %+v", r.Flags)
	}
	if !slices.Equal(r.Models, []string{"alpha", "beta"}) {
		t.Errorf("models %v", r.Models)
	}
	f.mu.Lock()
	argv := slices.Clone(f.argv)
	f.mu.Unlock()
	if len(argv) != 1 || !slices.Equal(argv[0], []string{prog, "--help"}) {
		t.Errorf("argv %v", argv)
	}
	if resp, _ := f.e.do("GET", "/api/catalog/helper/flags", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	if resp, out := f.e.do("GET", "/api/catalog/nobody/flags", adminToken, nil); resp.StatusCode != http.StatusNotFound || errorCode(out) != "not_found" {
		t.Errorf("unknown id: %d %v", resp.StatusCode, out)
	}
}

// The argv is the entry's command and the recipe's help arguments,
// whatever the request's query or body hold, and none of the entry's
// start arguments.
func TestFlagsRouteArgvIsTheRecipes(t *testing.T) {
	prog := flagsProgram(t)
	a := claudeLike("helper", prog)
	a.Command = []string{prog, "first"}
	session := *a.Session
	session.StartArgs = []string{"sub", "--session-id", catalog.IDArg}
	a.Session = &session
	f := newFlagsEnv(t, []catalog.Agent{a})
	for deadline := time.Now().Add(30 * time.Second); ; {
		resp, out := f.e.do("GET", "/api/catalog/helper/flags?args=--version&cmd=sh", adminToken, map[string]any{"args": []string{"--version"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %v", resp.StatusCode, out)
		}
		if out["state"] == flagsRead {
			break
		}
		if out["state"] != flagsReading || time.Now().After(deadline) {
			t.Fatalf("%v", out)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.argv) != 1 || !slices.Equal(f.argv[0], []string{prog, "first", "--help"}) {
		t.Fatalf("argv %v", f.argv)
	}
}

// The four answers of none that run nothing.
func TestFlagsRouteRunsNothingWithoutAProgramToRead(t *testing.T) {
	prog := flagsProgram(t)
	off := false
	noArgs := claudeLike("no-args", prog)
	noArgs.AllowArgs = false
	noRecipe := claudeLike("no-recipe", prog)
	noRecipe.Adapter = "aider"
	missing := claudeLike("missing", filepath.Join(t.TempDir(), "absent"))
	probeOff := claudeLike("probe-off", prog)
	probeOff.Probe = &off
	relative := claudeLike("relative", "./agent")
	f := newFlagsEnv(t, []catalog.Agent{noArgs, noRecipe, missing, probeOff, relative})
	for id, why := range map[string]string{"no-args": whyNoArgs, "no-recipe": whyNoRecipe, "missing": whyNotInstalled, "probe-off": whyNotIdentified, "relative": whyNotIdentified} {
		r := f.askUntil(t, id, flagsNone)
		if r.Why != why || len(r.Flags) != 0 || len(r.Models) != 0 {
			t.Errorf("%s: %+v, want %s", id, r, why)
		}
	}
	if n, h := f.runs.Load(), f.helps.Load(); n != 0 || h != 0 {
		t.Fatalf("%d probes and %d help reads run", n, h)
	}
}

// A program the identity probe does not identify is not read.
func TestFlagsRouteReadsOnlyAnIdentifiedProgram(t *testing.T) {
	f := newFlagsEnv(t, []catalog.Agent{claudeLike("other", flagsProgram(t))})
	f.e.srv.probes.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		return agents.ProbeResult{Ran: true, Output: "something else"}
	}
	if r := f.askUntil(t, "other", flagsNone); r.Why != whyNotIdentified {
		t.Fatalf("%+v", r)
	}
	if h := f.helps.Load(); h != 0 {
		t.Fatalf("%d help reads", h)
	}
}

// A read held at a gate answers reading within the wait, and the asks made
// meanwhile join the one read; once it ends, it is answered from the cache.
func TestFlagsRouteReadingJoinsOneRead(t *testing.T) {
	f := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	f.e.srv.probes.wait = 50 * time.Millisecond
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	f.answer = func(int32) (string, agents.ProbeResult) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate
		return flagsHelp, agents.ProbeResult{Ran: true}
	}
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	for deadline := time.Now().Add(30 * time.Second); len(started) == 0; {
		if r := f.ask(t, "helper"); r.State != flagsReading || time.Now().After(deadline) {
			t.Fatalf("a read held at its gate: %+v", r)
		}
	}
	for range 2 {
		if r := f.ask(t, "helper"); r.State != flagsReading {
			t.Fatalf("an ask beside the held read: %+v", r)
		}
	}
	release()
	f.askUntil(t, "helper", flagsRead)
	f.ask(t, "helper")
	if h := f.helps.Load(); h != 1 {
		t.Fatalf("%d help reads, want one joined and kept", h)
	}
}

// Another version is another list; the same version is read once.
func TestFlagsRouteReadsEachVersion(t *testing.T) {
	f := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	f.askUntil(t, "helper", flagsRead)
	f.ask(t, "helper")
	if h := f.helps.Load(); h != 1 {
		t.Fatalf("%d help reads", h)
	}
	f.version.Store("2.1.500")
	f.advance(probeTTL + time.Second) // the identity is probed again
	if r := f.askUntil(t, "helper", flagsRead); r.Version != "2.1.500" {
		t.Fatalf("%+v", r)
	}
	if h := f.helps.Load(); h != 2 {
		t.Fatalf("%d help reads, want one for each version", h)
	}
}

// A failed read is answered unreadable and asked again only after
// helpFailedTTL; a read that failed on the run's own side is not kept.
func TestFlagsRouteKeepsAFailedReadAMinute(t *testing.T) {
	f := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	f.answer = func(n int32) (string, agents.ProbeResult) {
		switch n {
		case 1:
			return "", agents.ProbeResult{Error: "timeout"}
		case 3:
			return "", agents.ProbeResult{Error: "watch failed", Retry: true}
		}
		return flagsHelp, agents.ProbeResult{Ran: true}
	}
	if r := f.askUntil(t, "helper", flagsNone); r.Why != whyUnreadable || r.Version != "2.1.400" {
		t.Fatalf("%+v", r)
	}
	f.ask(t, "helper")
	f.advance(helpFailedTTL - time.Second)
	f.ask(t, "helper")
	if h := f.helps.Load(); h != 1 {
		t.Fatalf("%d help reads within the minute", h)
	}
	f.advance(2 * time.Second)
	f.askUntil(t, "helper", flagsRead)
	if h := f.helps.Load(); h != 2 {
		t.Fatalf("%d help reads after the minute", h)
	}

	g := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	g.answer = func(n int32) (string, agents.ProbeResult) {
		if n == 1 {
			return "", agents.ProbeResult{Error: "watch failed", Retry: true}
		}
		return flagsHelp, agents.ProbeResult{Ran: true}
	}
	if r := g.askUntil(t, "helper", flagsNone); r.Why != whyUnreadable {
		t.Fatalf("%+v", r)
	}
	g.askUntil(t, "helper", flagsRead)
	if h := g.helps.Load(); h != 2 {
		t.Fatalf("%d help reads: a Retry was kept", h)
	}
}

func TestFlagsRouteBounds(t *testing.T) {
	f := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	var rows strings.Builder
	rows.WriteString("Options:\n")
	for i := range 300 {
		fmt.Fprintf(&rows, "  --f%d  words\n", i)
	}
	f.answer = func(int32) (string, agents.ProbeResult) { return rows.String(), agents.ProbeResult{Ran: true} }
	if r := f.askUntil(t, "helper", flagsRead); len(r.Flags) != 256 {
		t.Fatalf("%d flags", len(r.Flags))
	}

	g := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	var big strings.Builder
	big.WriteString("Options:\n")
	var vals []string
	for i := range 32 {
		vals = append(vals, fmt.Sprintf(`"value-%02d"`, i))
	}
	for i := range 256 {
		fmt.Fprintf(&big, "  --f%d <v>  w (choices: %s)\n", i, strings.Join(vals, ", "))
	}
	if big.Len() > agents.MaxHelp {
		t.Fatalf("the help itself is %d bytes", big.Len())
	}
	g.answer = func(int32) (string, agents.ProbeResult) { return big.String(), agents.ProbeResult{Ran: true} }
	if r := g.askUntil(t, "helper", flagsNone); r.Why != whyTooLong || len(r.Flags) != 0 {
		t.Fatalf("%s %d", r.Why, len(r.Flags))
	}

	m := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	m.answer = func(int32) (string, agents.ProbeResult) {
		return "Options:\n  --model <model>  which (e.g. 'ok-1', 'ok/2', 'ok@3') or (choices: \"also\")\n", agents.ProbeResult{Ran: true}
	}
	r := m.askUntil(t, "helper", flagsRead)
	if len(r.Models) == 0 {
		t.Fatalf("no models: %+v", r)
	}
	for _, name := range r.Models {
		if !modelPattern.MatchString(name) {
			t.Errorf("model %q", name)
		}
	}
	h := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	h.answer = func(int32) (string, agents.ProbeResult) {
		return "", agents.ProbeResult{Ran: true, TooLong: true, Error: agents.ErrHelpTooLong.Error()}
	}
	if r := h.askUntil(t, "helper", flagsNone); r.Why != whyTooLong {
		t.Fatalf("%+v", r)
	}
	n := newFlagsEnv(t, []catalog.Agent{claudeLike("helper", flagsProgram(t))})
	n.answer = func(int32) (string, agents.ProbeResult) { return "Usage: helper\n", agents.ProbeResult{Ran: true} }
	if r := n.askUntil(t, "helper", flagsNone); r.Why != whyNoFlags {
		t.Fatalf("%+v", r)
	}
}

// keepsTerminal is set where the chat refuses the flag, for an entry with
// the protocol way at a qualified version only.
func TestFlagsRouteMarksWhatKeepsATerminal(t *testing.T) {
	prog := flagsProgram(t)
	chat := claudeLike("chat", prog)
	chat.Protocol = &catalog.ProtocolRecipe{Name: "claude-stream-json"}
	plain := claudeLike("plain", prog)
	plain.Command = []string{prog, "plain"} // another command: its own identity
	f := newFlagsEnv(t, []catalog.Agent{chat, plain})
	flags := byFlagName(f.askUntil(t, "chat", flagsRead))
	for name, want := range map[string]bool{"--add-dir": true, "--verbose": true, "--permission-mode": true, "--effort": false, "--model": false} {
		if flags[name].KeepsTerminal != want {
			t.Errorf("chat %s: keepsTerminal %v", name, flags[name].KeepsTerminal)
		}
	}
	for _, fl := range f.askUntil(t, "plain", flagsRead).Flags {
		if fl.KeepsTerminal {
			t.Errorf("an entry with no chat: %s marked", fl.Name)
		}
	}
	// Codex's chat takes only the flags its app server has a counterpart of; nothing is marked while its sessions do not move
	// (agentproto.Moves), as today.
	codex, _ := catalog.Default().Get("codex")
	codex.ID, codex.Name, codex.Command, codex.Protocol = "codex-chat", "Stub codex-chat", []string{flagsProgram(t)}, &catalog.ProtocolRecipe{Name: agentproto.CodexAppServer}
	cx := newFlagsEnv(t, []catalog.Agent{codex})
	cx.version.Store(agentproto.CodexMinVersion)
	cflags := byFlagName(cx.askUntil(t, "codex-chat", flagsRead))
	for name, want := range map[string]bool{"--add-dir": true, "--verbose": true, "--effort": true, "--model": false} {
		if want = want && agentproto.Moves(agentproto.CodexAppServer); cflags[name].KeepsTerminal != want {
			t.Errorf("codex %s: keepsTerminal %v", name, cflags[name].KeepsTerminal)
		}
	}
	old := newFlagsEnv(t, []catalog.Agent{chat})
	old.version.Store("2.1.300")
	for _, fl := range old.askUntil(t, "chat", flagsRead).Flags {
		if fl.KeepsTerminal {
			t.Errorf("below the chat's version: %s marked", fl.Name)
		}
	}
}
