package agents

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

// Codex gives an MCP tool 60 s unless its server says otherwise; Brakeman's
// act can wait longer for a launch, so the registration Codex is launched
// with gives Conductor's tools MCPToolTimeout, in Codex's seconds.
func TestCodexMCPToolTimeoutCoversAnAct(t *testing.T) {
	args := MCPFor("codex", t.TempDir())
	want := "mcp_servers.conductor.tool_timeout_sec=" + strconv.Itoa(int(MCPToolTimeout/time.Second))
	if !slices.Contains(args, want) {
		t.Fatalf("Codex's registration: %q, want %s", args, want)
	}
	if MCPToolTimeout <= 60*time.Second {
		t.Fatalf("MCPToolTimeout %v is no more than Codex's default", MCPToolTimeout)
	}
}
