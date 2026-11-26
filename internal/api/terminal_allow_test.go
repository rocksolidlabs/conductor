package api

import (
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// A terminal crew's member of Claude Code is launched as it was before
// #1143: its command, then Conductor's settings and Conductor's MCP
// server, the same two files by the same names in the hooks directory,
// in its terminal. What changed is inside the settings file (its allow
// rules, internal/agents' TestTheTerminalsSettingsAllowConductorsOwnTools).
// Its agent's refusal of a call to Conductor, which its hook reports to
// the events route with the session's own token, comes back to the run's
// log as one line that names the rule allowing it.
func TestTerminalCrewUnchanged(t *testing.T) {
	e := newTestEnv(t, nil)
	hooks := filepath.Join(e.srv.cfg.DataDir, "hooks")
	// A stub that answers the identity probe as Claude Code and is cat
	// otherwise, never the real agent.
	script := []string{versionScript(t, "claude", "2.1.287 (Claude Code)")}
	body := agentBody("claude-term")
	body["command"] = script
	body["adapter"] = "claude"
	body["signal"] = map[string]any{"kind": "hook"}
	e.save(body)
	run := e.launchCrew(t, "Terminal", map[string]any{"name": "lead", "agentId": "claude-term", "prompt": "", "start": map[string]any{"when": "immediately"}})
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil) })
	sid := e.waitRunning(t, run, "lead")
	d, ok := e.srv.registry.Get(sid)
	if !ok {
		t.Fatalf("no session %s", sid)
	}
	info := d.Info()
	want := append(slices.Clone(script), "--settings", filepath.Join(hooks, "claude.json"), "--mcp-config", filepath.Join(hooks, "claude-mcp.json"))
	if !slices.Equal(info.Command, want) || info.Way != "" {
		t.Fatalf("the member runs %q in way %q, want %q in its terminal", info.Command, info.Way, want)
	}
	tok := e.agentToken(sid)
	for range 2 {
		resp, out := e.do("POST", "/api/sessions/"+sid+"/events", tok, map[string]any{"type": session.ActivityToolDenied, "tool": "mcp__conductor__task_result"})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("the hook's report: %d %v", resp.StatusCode, out)
		}
	}
	words := "lead's call to Conductor's task_result was refused by its agent's own permission check; " +
		"allow mcp__conductor__task_result in its permissions (in Claude Code, /permissions) for it to call again"
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, _ := e.srv.runs.Get(run)
		n := 0
		for _, entry := range r.Log {
			if entry.Message == words {
				n++
			}
		}
		if n == 1 {
			break
		}
		if n > 1 || time.Now().After(deadline) {
			t.Fatalf("%d lines of %q: %+v", n, words, r.Log)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
