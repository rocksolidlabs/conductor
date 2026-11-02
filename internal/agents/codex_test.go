package agents

import (
	"slices"
	"strings"
	"testing"
)

// Codex starts Conductor's MCP server with only the variables its
// override names (#354), so the override names CONDUCTOR_BRAKEMAN, by
// which the server lists a Brakeman turn's tools, and the registration
// Codex is launched with carries it.
func TestCodexMCPEnvNamesBrakeman(t *testing.T) {
	if !strings.Contains(codexMCPEnv, `"CONDUCTOR_BRAKEMAN"`) {
		t.Fatalf("Codex's MCP server is not given CONDUCTOR_BRAKEMAN: %s", codexMCPEnv)
	}
	args := MCPFor("codex", t.TempDir())
	if !slices.Contains(args, "mcp_servers.conductor.env_vars="+codexMCPEnv) {
		t.Fatalf("Codex's registration: %q", args)
	}
}
