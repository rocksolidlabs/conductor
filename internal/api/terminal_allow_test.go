package api

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
)

// A terminal crew's member of Claude Code is launched as main launches it,
// with exactly one argument more where the server registers its MCP
// server (#1143): its command, Conductor's settings (the same file, which
// holds no rule), Conductor's MCP server and, beside it, one
// --allowedTools of Conductor's eight reporting tools; with the server's
// MCP off, main's launch exactly, no rule anywhere. It runs in its
// terminal.
func TestTerminalCrewUnchanged(t *testing.T) {
	off := false
	for name, mutate := range map[string]func(*config.Config){"mcp on": nil, "mcp off": func(c *config.Config) { c.Agents.MCP = &off }} {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t, mutate)
			hooks := filepath.Join(e.srv.cfg.DataDir, "hooks")
			// A stub that answers the identity probe as Claude Code and is
			// cat otherwise, never the real agent.
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
			want := append(slices.Clone(script), "--settings", filepath.Join(hooks, "claude.json"))
			if mutate == nil {
				want = append(want, "--mcp-config", filepath.Join(hooks, "claude-mcp.json"),
					"--allowedTools=mcp__conductor__guide,mcp__conductor__report,mcp__conductor__set_state,mcp__conductor__ask,"+
						"mcp__conductor__task_context,mcp__conductor__task_checkpoint,mcp__conductor__task_blocked,mcp__conductor__task_result")
			}
			if !slices.Equal(info.Command, want) || info.Way != "" {
				t.Fatalf("the member runs %q in way %q, want %q in its terminal", info.Command, info.Way, want)
			}
		})
	}
}

// A Brakeman turn keeps its switch's meaning (#1143): "Run Conductor's
// tools without asking" off, its launch carries no --allowedTools at all
// beside Conductor's MCP server, so it asks before each of Conductor's
// tools; on, it carries its own switch's one, Conductor's server whole,
// and not the terminal's eight.
func TestABrakemanTurnsSwitchAloneAllowsConductorsTools(t *testing.T) {
	t.Parallel()
	for _, allow := range []bool{false, true} {
		b := brakemanEnv(t, nil)
		b.enable(t, "bk", func(c map[string]any) { c["allowConductorTools"] = allow })
		id := b.briefing(t)
		_, _, l := b.turnSession(t, id)
		argv := l.Info().Command
		if !slices.Contains(argv, "--mcp-config") {
			t.Fatalf("switch %v: the turn has no MCP server: %q", allow, argv)
		}
		var allowed []string
		for _, a := range argv {
			if strings.HasPrefix(a, "--allowedTools") || strings.HasPrefix(a, "--allowed-tools") {
				allowed = append(allowed, a)
			}
		}
		want := []string(nil)
		if allow {
			want = []string{"--allowedTools=mcp__conductor"}
		}
		if !slices.Equal(allowed, want) {
			t.Errorf("switch %v: the turn allows %q, want %q (argv %q)", allow, allowed, want, argv)
		}
	}
}

// A chat's child is started as main starts it (#1143): Conductor's own
// floor settings allow its tools there, and its argv carries no
// --allowedTools of the terminal's.
func TestAChatsChildCarriesNoAllowance(t *testing.T) {
	c := newChatEnvWith(t, nil, nil, nil)
	l := c.launch(t)
	if !slices.ContainsFunc(l.Info().Command, func(a string) bool { return strings.HasPrefix(a, "--allowedTools=mcp__conductor__guide,") }) {
		t.Fatalf("its terminal was launched without the allowance: %q", l.Info().Command)
	}
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	specs := c.pipeSpecs()
	if len(specs) != 1 {
		t.Fatalf("%d pipes started", len(specs))
	}
	if argv := specs[0].Argv; slices.ContainsFunc(argv, func(a string) bool {
		return strings.HasPrefix(a, "--allowedTools") || strings.HasPrefix(a, "--allowed-tools")
	}) {
		t.Fatalf("the chat's child carries an allowance: %q", argv)
	}
}
