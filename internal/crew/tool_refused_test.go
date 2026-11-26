package crew

import (
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// deniedEntry is the entry a member's hook reports when its agent refused
// a call (Claude Code's PermissionDenied, as notify.MapClaudeHook maps it).
func deniedEntry(tool string) session.ActivityEntry {
	return session.ActivityEntry{Type: session.ActivityToolDenied, ByName: "agent", Tool: tool, At: time.Now().UTC()}
}

// allowingLaunch is a terminal Claude Code launch that carries Conductor's
// server and, beside it, its allowance, as the server makes it.
func allowingLaunch() []string {
	return append([]string{"claude", "--settings", "/hooks/claude.json"}, agents.MCPFor("claude", "/hooks", true)...)
}

// refusedKeys is how many refusals run id keeps.
func refusedKeys(e *Engine, id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.runs[id].refusedSaid)
}

// The refusal of a call to one of the tools a member's launch allows
// Conductor's server is said in the run's log, once for the attempt and
// the tool (#1143), in words that are true where the launch already
// allows it: something stronger refused it. A name that is not exactly
// one of those tools (another of Conductor's, another server's, a name
// stretched, cut or with other bytes) says nothing and keeps nothing:
// eight lines and eight keys a session at most.
func TestARefusedCallToConductorIsSaidInTheRunsLog(t *testing.T) {
	e, fl, _ := managedEngine(t)
	fl.mu.Lock()
	fl.command = allowingLaunch()
	fl.mu.Unlock()
	run := launchManaged(t, e, repoCrew(newRepo(t), work("build", "lead")))
	defer stopRun(t, e, run.ID)
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	sid := b.Attempt.SessionID
	words := "task build: lead's call to Conductor's task_result was refused by its agent's own permission check or a rule in its settings, " +
		"though Conductor's launch allows it; look in its terminal (in Claude Code, /permissions)"
	for range 2 {
		e.OnActivity(sid, deniedEntry("mcp__conductor__task_result"), "")
	}
	for _, tool := range []string{"", "Bash", "task_result", "mcp__other__task_result", "mcp__conductor__", "mcp__conductor__*", "conductor__task_result",
		"mcp__conductor__permission", "mcp__conductor__run_status", "mcp__conductor__message_send",
		"mcp__conductor__task_result and Bash(*)", "mcp__conductor__task_result\nstopped", "mcp__conductor__task_resul", "mcp__conductor__task_result\t"} {
		e.OnActivity(sid, deniedEntry(tool), "")
	}
	if n := logExact(t, e, run.ID, words); n != 1 {
		t.Fatalf("%d lines of %q", n, words)
	}
	if n, k := logCount(t, e, run.ID, "was refused by"), refusedKeys(e, run.ID); n != 1 || k != 1 {
		r, _ := e.Get(run.ID)
		t.Fatalf("%d lines and %d keys of a refusal, want 1 and 1: %+v", n, k, r.Log)
	}
	for _, name := range agents.ProtocolUnaskedTools {
		e.OnActivity(sid, deniedEntry("mcp__conductor__"+name), "")
		e.OnActivity(sid, deniedEntry("mcp__conductor__"+name), "")
	}
	if n, k := logCount(t, e, run.ID, "was refused by"), refusedKeys(e, run.ID); n != 8 || k != 8 {
		t.Fatalf("%d lines and %d keys for the eight tools, want 8 and 8", n, k)
	}
}

// In a run of no tasks the member's refusal is said under its name alone.
func TestARefusedCallToConductorIsSaidInALegacyRun(t *testing.T) {
	e, fl := newEngine(t)
	fl.command = allowingLaunch()
	run := runningPair(t, e, fl)
	lead, _ := fl.member("lead")
	lead.Record(deniedEntry("mcp__conductor__set_state"))
	lead.Record(deniedEntry("mcp__conductor__set_state"))
	words := "lead's call to Conductor's set_state was refused by its agent's own permission check or a rule in its settings, " +
		"though Conductor's launch allows it; look in its terminal (in Claude Code, /permissions)"
	if n := logExact(t, e, run.ID, words); n != 1 {
		r, _ := e.Get(run.ID)
		t.Fatalf("%d lines of %q: %+v", n, words, r.Log)
	}
}

// A launch that does not carry the allowance with Conductor's server (the
// server's MCP off, a Brakeman turn) allows nothing, and a server keyed
// conductor there is not known to be Conductor's: its refusals say
// nothing and keep nothing.
func TestARefusalOnALaunchThatAllowsNothingSaysNothing(t *testing.T) {
	for name, command := range map[string][]string{
		"no server":       {"claude", "--settings", "/hooks/claude.json"},
		"its server bare": append([]string{"claude", "--settings", "/hooks/claude.json"}, agents.MCPFor("claude", "/hooks", false)...),
	} {
		t.Run(name, func(t *testing.T) {
			e, fl := newEngine(t)
			fl.command = command
			run := runningPair(t, e, fl)
			lead, _ := fl.member("lead")
			lead.Record(deniedEntry("mcp__conductor__task_result"))
			if n, k := logCount(t, e, run.ID, "was refused by"), refusedKeys(e, run.ID); n != 0 || k != 0 {
				t.Fatalf("%d lines and %d keys", n, k)
			}
		})
	}
}

// A member in a chat is told nothing: its own stream records the refusal
// of a call (a deny rule's too), and that entry reaches the engine, but
// the line is a terminal's. The same session's refusal, back in its
// terminal, is said.
func TestAChatsRefusalIsNotSaid(t *testing.T) {
	e, fl := newEngine(t)
	fl.command = allowingLaunch()
	seen := make(chan session.ActivityEntry, 16)
	fl.beforeActivity = func(_ string, entry session.ActivityEntry) {
		if entry.Type == session.ActivityToolDenied {
			select {
			case seen <- entry:
			default:
			}
		}
	}
	run := runningPair(t, e, fl)
	lead, _ := fl.member("lead")
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(lead, f.Start()); err != nil {
		t.Fatal(err)
	}
	if lead.Way() != session.WayProtocol {
		t.Fatalf("the session is in way %s", lead.Way())
	}
	f.TurnStarted("")
	f.ToolStarted(session.ToolStep{Ref: "c-1", Name: "mcp__conductor__task_result", Input: []byte(`{}`)})
	f.ToolEnded("c-1", session.ToolResult{Status: session.ToolDenied})
	select {
	case entry := <-seen:
		if entry.Tool != "mcp__conductor__task_result" {
			t.Fatalf("the chat's entry names %q", entry.Tool)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the chat's refusal never reached the engine's hook")
	}
	// The engine took the entry as it was handed, on the same goroutine;
	// and it takes one handed to it now, the session still a chat, alike.
	e.OnActivity(lead.Info().ID, deniedEntry("mcp__conductor__task_result"), "")
	if n, k := logCount(t, e, run.ID, "was refused by"), refusedKeys(e, run.ID); n != 0 || k != 0 {
		r, _ := e.Get(run.ID)
		t.Fatalf("a chat's refusal: %d lines and %d keys: %+v", n, k, r.Log)
	}
	tests, _ := fl.member("tests")
	tests.Record(deniedEntry("mcp__conductor__task_result"))
	if n := logCount(t, e, run.ID, "tests's call to Conductor's task_result was refused by"); n != 1 {
		t.Fatalf("the terminal member's refusal: %d lines", n)
	}
}

// logExact counts the entries of a run's log whose message is text.
func logExact(t *testing.T, e *Engine, runID, text string) int {
	t.Helper()
	r, ok := e.Get(runID)
	if !ok {
		t.Fatalf("run %s not found", runID)
	}
	n := 0
	for _, entry := range r.Log {
		if entry.Message == text {
			n++
		}
	}
	return n
}
