package agents

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// terminalAllow is the one argument a terminal's Claude Code launch gains
// beside Conductor's MCP server, written out: Conductor's unasked tools,
// each by its one tool's rule, under the server name claude-mcp.json
// registers. Never the prompt tool, never a server's every tool.
const terminalAllow = "--allowedTools=mcp__conductor__guide,mcp__conductor__report,mcp__conductor__set_state,mcp__conductor__ask," +
	"mcp__conductor__task_context,mcp__conductor__task_checkpoint,mcp__conductor__task_blocked,mcp__conductor__task_result"

// settingsDoc reads a settings file Conductor wrote: its members by name.
func settingsDoc(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return doc
}

// keysOf is doc's member names, sorted.
func keysOf(doc map[string]json.RawMessage) []string {
	var keys []string
	for k := range doc {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// compact is raw without its spaces, for comparing members of files
// written with indentation at different depths.
func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return b.String()
}

// The allowance travels with Conductor's own tool server and nowhere else
// (#1143): Claude Code's MCP arguments, asked with the allowance, are the
// MCP config and then the one --allowedTools of the eight rules, under
// the one server name that config registers; asked without it (a Brakeman
// turn's, whose switch decides), they are the config alone. No other
// adapter gains anything. AllowsConductorTools tells a launch that
// carries the allowance from one that does not.
func TestTheAllowanceTravelsWithConductorsServer(t *testing.T) {
	dir := t.TempDir()
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	mcp := settingsDoc(t, filepath.Join(dir, "claude-mcp.json"))
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(mcp["mcpServers"], &servers); err != nil || len(servers) != 1 || servers["conductor"] == nil {
		t.Fatalf("the terminal's MCP config registers %v (%v), not the one server the rules name", servers, err)
	}
	config := []string{"--mcp-config", filepath.Join(dir, "claude-mcp.json")}
	if got := MCPFor("claude", dir, true); !slices.Equal(got, append(slices.Clone(config), terminalAllow)) {
		t.Fatalf("with the allowance: %q", got)
	}
	if got := MCPFor("claude", dir, false); !slices.Equal(got, config) {
		t.Fatalf("without it: %q", got)
	}
	if got := MCPFor("claude", "hooks", true); got != nil {
		t.Fatalf("a relative hooks dir: %q", got)
	}
	for _, a := range All() {
		if a.ID != "claude" && !slices.Equal(MCPFor(a.ID, dir, true), MCPFor(a.ID, dir, false)) {
			t.Errorf("%s gains %q", a.ID, MCPFor(a.ID, dir, true))
		}
	}
	launch := append([]string{"claude", "--settings", filepath.Join(dir, "claude.json")}, MCPFor("claude", dir, true)...)
	if !AllowsConductorTools(launch) {
		t.Errorf("%q does not read as allowing", launch)
	}
	for _, argv := range [][]string{nil, launch[:3], append(launch[:3:3], MCPFor("claude", dir, false)...),
		{"claude", "--allowedTools=mcp__conductor"}, {"claude", strings.TrimSuffix(terminalAllow, ",mcp__conductor__task_result")}} {
		if AllowsConductorTools(argv) {
			t.Errorf("%q reads as allowing", argv)
		}
	}
}

// No file an installer reads holds a rule: every JSON file WriteAssets
// writes (Install, released ones too, and the snippet read them from the
// hooks directory) has no permissions member, and no file names one of
// Conductor's tools by its rule; the terminal's settings are their hooks
// (and the yolo key where they had it) alone.
func TestNoFileAnInstallerReadsHoldsARule(t *testing.T) {
	dir := t.TempDir()
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		if bytes.Contains(b, []byte("mcp__conductor__")) {
			t.Errorf("%s names a rule:\n%s", p, b)
		}
		if strings.HasSuffix(p, ".json") {
			var doc map[string]json.RawMessage
			if json.Unmarshal(b, &doc) == nil && doc["permissions"] != nil {
				t.Errorf("%s holds permissions %s", p, doc["permissions"])
			}
		}
		return nil
	})
	if err != nil || n < 12 {
		t.Fatalf("walked %d files: %v", n, err)
	}
	for name, want := range map[string][]string{
		"claude.json": {"hooks"}, "claude-tools.json": {"hooks"}, "claude-yolo.json": {"hooks", "skipDangerousModePermissionPrompt"},
		"claude-tools-yolo.json": {"hooks", "skipDangerousModePermissionPrompt"}, "claude-yolo-only.json": {"skipDangerousModePermissionPrompt"},
	} {
		if got := keysOf(settingsDoc(t, filepath.Join(dir, name))); !slices.Equal(got, want) {
			t.Errorf("%s holds %v, want %v", name, got, want)
		}
	}
}

// Conductor never writes an allow rule into the person's own settings:
// Install, on a home with no settings and on one with settings of the
// person's, merges the hooks alone, and the snippet a person copies by
// hand holds the hooks alone. The hooks are the launch's.
func TestInstallWritesNoAllowRuleIntoThePersonsSettings(t *testing.T) {
	useBin(t, "/opt/conductor")
	hooks := t.TempDir()
	if err := writeAssets(t, hooks, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	a, _ := Get("claude")
	launch := settingsDoc(t, filepath.Join(hooks, "claude.json"))
	for name, own := range map[string]string{"none": "", "own": `{"model":"opus","permissions":{"deny":["Bash(rm *)"]}}`} {
		home := t.TempDir()
		settings := filepath.Join(home, ".claude", "settings.json")
		if own != "" {
			if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings, []byte(own), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.Install(home, hooks); err != nil {
			t.Fatal(err)
		}
		doc := settingsDoc(t, settings)
		if strings.Contains(string(doc["permissions"]), "mcp__") {
			t.Errorf("%s: Install wrote %s into the person's settings", name, doc["permissions"])
		}
		if own != "" && compact(t, doc["permissions"]) != `{"deny":["Bash(rm *)"]}` {
			t.Errorf("%s: the person's permissions are %s", name, doc["permissions"])
		}
		if own == "" && (len(doc) != 1 || compact(t, doc["hooks"]) != compact(t, launch["hooks"])) {
			t.Errorf("%s: Install wrote %s, want the launch's hooks alone", name, keysOf(doc))
		}
		if ok, _ := a.Status(home); !ok {
			t.Errorf("%s: not installed after Install", name)
		}
	}
	var snippet map[string]json.RawMessage
	if err := json.Unmarshal([]byte(a.Snippet(hooks)), &snippet); err != nil || len(snippet) != 1 || compact(t, snippet["hooks"]) != compact(t, launch["hooks"]) {
		t.Errorf("the snippet holds %s (%v), want the hooks alone", keysOf(snippet), err)
	}
}

// ConductorToolOf takes exactly one of the tools the launch allows, after
// the server's prefix, and nothing else: another of Conductor's tools,
// another server's, a name stretched, cut, with a line break or other
// bytes.
func TestConductorToolOfTakesOnlyTheNamedSet(t *testing.T) {
	for _, name := range []string{"guide", "report", "set_state", "ask", "task_context", "task_checkpoint", "task_blocked", "task_result"} {
		if got, ok := ConductorToolOf("mcp__conductor__" + name); !ok || got != name {
			t.Errorf("%s: %q %v", name, got, ok)
		}
	}
	for _, tool := range []string{"", "Bash", "task_result", "mcp__conductor__", "mcp__conductor__*", "mcp__conductor__permission", "mcp__conductor__message_send",
		"mcp__other__task_result", "mcp__conductor__task_result and Bash(*)", "mcp__conductor__task_result\nstopped", "mcp__conductor__task_result ",
		"mcp__conductor__Task_result", "mcp__conductor__task_resul", "mcp__conductor__task_result\x00", "mcp__conductor__report,mcp__conductor__ask", "conductor__task_result"} {
		if got, ok := ConductorToolOf(tool); ok || got != "" {
			t.Errorf("%q: %q %v", tool, got, ok)
		}
	}
}
