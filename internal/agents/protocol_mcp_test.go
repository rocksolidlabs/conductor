package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A chat's child of Claude Code takes Conductor's MCP server from its own
// config file: the one server, the binary with its mcp argument, and a
// per-server timeout of 8 hours, so that the server's idle window (30
// minutes by default) does not end a call that waits for a person.
func TestClaudeProtocolMCPConfig(t *testing.T) {
	hooks := t.TempDir()
	if err := writeAssets(t, hooks, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(hooks, "claude-protocol-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Timeout int64    `json:"timeout"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	s, ok := doc.MCPServers["conductor"]
	if len(doc.MCPServers) != 1 || !ok {
		t.Fatalf("servers: %s", b)
	}
	if s.Command != "/opt/conductor" || !slices.Equal(s.Args, []string{"mcp"}) {
		t.Fatalf("server: %+v", s)
	}
	if s.Timeout != 8*60*60*1000 || ClaudeProtocolMCPTimeoutMs != 28_800_000 {
		t.Fatalf("timeout %d ms, want 8 hours", s.Timeout)
	}
	// The terminal way's file is unchanged: no timeout of its own.
	var terminal struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	b, err = os.ReadFile(filepath.Join(hooks, "claude-mcp.json"))
	if err != nil || json.Unmarshal(b, &terminal) != nil {
		t.Fatalf("claude-mcp.json: %v\n%s", err, b)
	}
	if _, has := terminal.MCPServers["conductor"]["timeout"]; has {
		t.Fatalf("claude-mcp.json gained a timeout: %s", b)
	}
}
