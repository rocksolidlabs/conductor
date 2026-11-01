package agents

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The three argument sets a Brakeman turn is launched with (the Brakeman
// plan, PR-3a): each is the adapter's own flag, checked against the CLI by
// the recipes tier (clis_recipes_test.go), and nil for every adapter whose
// flag Conductor has not checked.

// idsWith collects the ids of the adapters for which has is true.
func idsWith(has func(Adapter) bool) []string {
	var ids []string
	for _, a := range All() {
		if has(a) {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

func TestOnlyVerifiedAdaptersTakeAModel(t *testing.T) {
	if got := idsWith(func(a Adapter) bool { return a.ModelArgs != nil }); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Fatalf("adapters that take a model: %v", got)
	}
	for _, id := range []string{"claude", "codex"} {
		a, _ := Get(id)
		if a.Probe == nil || !a.Probe.Verified {
			t.Errorf("%s takes a model but its probe is not verified", id)
		}
		if got := a.ModelArgs("claude-opus-5-5"); !slices.Equal(got, []string{"--model=claude-opus-5-5"}) {
			t.Errorf("%s: ModelArgs = %q", id, got)
		}
		// The model is the flag's value, in the same word: whatever it
		// is, the agent never reads it as a flag or a prompt of its own.
		if got := a.ModelArgs("-p x"); len(got) != 1 || !strings.HasPrefix(got[0], "--model=") {
			t.Errorf("%s: a model became its own argument: %q", id, got)
		}
		if got := a.ModelArgs(""); got != nil {
			t.Errorf("%s: the default model gave %q", id, got)
		}
	}
}

func TestOnlyClaudeAndCodexHaveAReadOnlyMode(t *testing.T) {
	if got := idsWith(func(a Adapter) bool { return a.ReadOnlyArgs != nil }); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Fatalf("adapters with a read-only mode: %v", got)
	}
	want := map[string][]string{
		// The built-in tools are the file reads alone; the writers are
		// denied by name as well; and no MCP server but the one of
		// --mcp-config, Conductor's.
		"claude": {"--tools=Read,Grep,Glob", "--disallowedTools=Bash,Edit,Write,NotebookEdit", "--strict-mcp-config"},
		// The read-only sandbox, and no command escalated past it: a
		// person is never asked to let a blocked write run, while an MCP
		// tool's approval is still asked.
		"codex": {"--sandbox", "read-only", "-c", codexReadOnlyApprovals},
	}
	for id, w := range want {
		a, _ := Get(id)
		got := a.ReadOnlyArgs()
		if !slices.Equal(got, w) {
			t.Errorf("%s: ReadOnlyArgs = %q, want %q", id, got, w)
		}
		// A turn needs Conductor's tools as much as the read-only mode.
		if a.MCP == nil {
			t.Errorf("%s has a read-only mode but no MCP registration", id)
		}
		got[0] = "changed"
		if a.ReadOnlyArgs()[0] == "changed" {
			t.Errorf("%s: ReadOnlyArgs hands out a slice it keeps", id)
		}
	}
	// --strict-mcp-config keeps only --mcp-config's servers: Conductor's
	// must come that way, or a read-only turn would have none of its tools.
	claude, _ := Get("claude")
	if mcp := claude.MCP("/hooks"); len(mcp) == 0 || mcp[0] != "--mcp-config" {
		t.Errorf("claude registers Conductor's MCP server other than by --mcp-config: %q", mcp)
	}
	// Claude Code's reads and writes do not overlap, and no write is left
	// out of the deny rule.
	for _, w := range []string{"Bash", "Edit", "Write", "NotebookEdit"} {
		if slices.Contains(claudeReadTools, w) || !slices.Contains(claudeWriteTools, w) {
			t.Errorf("%s: read %v, denied %v", w, claudeReadTools, claudeWriteTools)
		}
	}
	if !strings.Contains(codexReadOnlyApprovals, "sandbox_approval=false") || !strings.Contains(codexReadOnlyApprovals, "rules=false") || !strings.Contains(codexReadOnlyApprovals, "request_permissions=false") || !strings.Contains(codexReadOnlyApprovals, "mcp_elicitations=true") {
		t.Errorf("codex approvals %q", codexReadOnlyApprovals)
	}
}

func TestClaudeAllowsConductorsTools(t *testing.T) {
	if got := idsWith(func(a Adapter) bool { return a.AllowTools != nil }); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Fatalf("adapters that allow a server's tools: %v", got)
	}
	claude, _ := Get("claude")
	if got := claude.AllowTools("conductor"); !slices.Equal(got, []string{"--allowedTools=mcp__conductor"}) {
		t.Errorf("claude: AllowTools = %q", got)
	}
	// The rule names the server the launch registers.
	var mcp struct{ MCPServers map[string]any }
	if err := json.Unmarshal([]byte(claudeAssets["claude-mcp.json"]), &mcp); err != nil || len(mcp.MCPServers) != 1 || mcp.MCPServers["conductor"] == nil {
		t.Errorf("claude's MCP config names another server: %v %s", err, claudeAssets["claude-mcp.json"])
	}
	codex, _ := Get("codex")
	useBin(t, "/opt/conductor")
	if got := codex.AllowTools("conductor"); !slices.Equal(got, []string{"-c", `mcp_servers.conductor.default_tools_approval_mode="approve"`}) {
		t.Errorf("codex: AllowTools = %q", got)
	}
	if mcp := strings.Join(codex.MCP("/hooks"), " "); !strings.Contains(mcp, "mcp_servers.conductor.command=") {
		t.Errorf("codex's MCP arguments name another server: %s", mcp)
	}
	// A name that is not a plain word could carry a rule, a TOML key or a
	// flag of its own: it gives nothing.
	for _, bad := range []string{"", "Conductor", "a b", "x,Bash", "x.y", `x"`, "-x", "x=1", strings.Repeat("a", 65)} {
		if got := claude.AllowTools(bad); got != nil {
			t.Errorf("claude: AllowTools(%q) = %q", bad, got)
		}
		if got := codex.AllowTools(bad); got != nil {
			t.Errorf("codex: AllowTools(%q) = %q", bad, got)
		}
	}
}
