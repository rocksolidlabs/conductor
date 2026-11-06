package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/config"
)

// A launch puts the Conductor skill where the agent's adapter reads skills,
// in the server user's home, once per adapter since the server started: a
// file removed after that is not put back until a restart, and a SKILL.md
// of the user's own is never touched. An agent without an adapter installs
// nothing.
func TestLaunchInstallsTheSkillOnceAndKeepsTheUsersFile(t *testing.T) {
	e := newTestEnv(t, nil)
	home := e.srv.home
	if home == "" || home == os.Getenv("HOME") {
		t.Fatalf("the test server's home is %q", home)
	}
	body := agentBody("claude-like")
	body["adapter"] = "claude"
	body["probe"] = false
	e.save(body)
	skill := filepath.Join(home, ".claude", "skills", "conductor", "SKILL.md")
	e.createSession("claude-like")
	if b, err := os.ReadFile(skill); err != nil || string(b) != agents.Skill {
		t.Fatalf("after the first launch: %v\n%s", err, b)
	}
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	e.createSession("claude-like")
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Fatalf("the skill came back within the same server start: %v", err)
	}
	// An agent without an adapter: nothing is written anywhere.
	e.createSession("cat")
	if entries, _ := os.ReadDir(home); len(entries) != 1 {
		t.Fatalf("home has %v", entries)
	}

	// The user's own file stays, on a server of its own.
	e2 := newTestEnv(t, nil)
	mine := filepath.Join(e2.srv.home, ".codex", "skills", "conductor", "SKILL.md")
	os.MkdirAll(filepath.Dir(mine), 0o755)
	os.WriteFile(mine, []byte("# mine\n"), 0o600)
	codex := agentBody("codex-like")
	codex["adapter"] = "codex"
	codex["probe"] = false
	e2.save(codex)
	e2.createSession("codex-like")
	if b, _ := os.ReadFile(mine); string(b) != "# mine\n" {
		t.Fatalf("the user's skill changed:\n%s", b)
	}
}

// agents.installSkill false (CONDUCTOR_AGENT_INSTALL_SKILL=0) leaves every
// home alone; the session still names the skill.
func TestInstallSkillAtLaunchOff(t *testing.T) {
	off := false
	e := newTestEnv(t, func(c *config.Config) { c.Agents.InstallSkill = &off })
	body := agentBody("claude-like")
	body["adapter"] = "claude"
	body["probe"] = false
	e.save(body)
	e.createSession("claude-like")
	if entries, _ := os.ReadDir(e.srv.home); len(entries) != 0 {
		t.Fatalf("home has %v", entries)
	}
}

// Every session's environment names its agent (CONDUCTOR_AGENT) and the
// server's copy of the skill (CONDUCTOR_SKILL, under the data directory's
// hooks), from Conductor itself.
func TestEverySessionCarriesTheSkillPathAndItsAgent(t *testing.T) {
	t.Setenv("CONDUCTOR_SKILL", "/from/the/server/env")
	e := newTestEnv(t, func(c *config.Config) { c.EnvPassthrough = []string{"CONDUCTOR_SKILL"} })
	body := agentBody("probe")
	body["command"] = []string{"/bin/sh", "-c", `echo "K=[$CONDUCTOR_SKILL] A=[$CONDUCTOR_AGENT] END"; exec /bin/cat`}
	body["env"] = map[string]string{"CONDUCTOR_SKILL": "/from/the/agent", "CONDUCTOR_AGENT": "other"}
	e.save(body)
	id := e.createSession("probe")
	c := dialViewer(t, e, id, adminToken)
	c.hello(80, 24)
	c.expectOutput("K=[" + filepath.Join(e.srv.cfg.DataDir, "hooks", "skills", "conductor", "SKILL.md") + "] A=[probe] END")
}

// agents.mcp false (CONDUCTOR_AGENT_MCP=0) launches without the MCP
// registration; Codex gets its config overrides rather than a file.
func TestLaunchRegistersTheMCPServerUnlessOff(t *testing.T) {
	e := newTestEnv(t, nil)
	hooks := filepath.Join(e.srv.cfg.DataDir, "hooks")
	codex := agentBody("codex-like")
	codex["adapter"] = "codex"
	codex["probe"] = false
	e.save(codex)
	info := e.launch("codex-like", nil)
	var got []string
	for _, a := range info["command"].([]any) {
		got = append(got, a.(string))
	}
	bin, _ := agents.Binary()
	// The command and args, then the variables Codex hands the server (#354)
	// and the wait it gives a tool.
	if n := len(got); n < 8 || got[n-8] != "-c" || got[n-7] != "mcp_servers.conductor.command=\""+bin+"\"" || got[n-6] != "-c" || got[n-5] != `mcp_servers.conductor.args=["mcp"]` || got[n-4] != "-c" || !strings.HasPrefix(got[n-3], "mcp_servers.conductor.env_vars=[") || !strings.Contains(got[n-3], `"CONDUCTOR_NOTIFY_TOKEN"`) || got[n-2] != "-c" || got[n-1] != "mcp_servers.conductor.tool_timeout_sec=720" {
		t.Fatalf("codex command %q", got)
	}
	off := false
	quiet := newTestEnv(t, func(c *config.Config) { c.Agents.MCP = &off })
	claude := agentBody("claude-like")
	claude["adapter"] = "claude"
	claude["probe"] = false
	quiet.save(claude)
	info = quiet.launch("claude-like", nil)
	for _, a := range info["command"].([]any) {
		if a == "--mcp-config" || a == filepath.Join(hooks, "claude-mcp.json") {
			t.Fatalf("mcp registered with agents.mcp off: %v", info["command"])
		}
	}
}
