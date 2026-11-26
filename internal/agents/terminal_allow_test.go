package agents

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// terminalAllow is the allow list every settings file Conductor gives a
// terminal's Claude Code holds, written out: Conductor's unasked tools,
// each by its one tool's rule, under the server name claude-mcp.json
// registers. Never the prompt tool, never a server's every tool.
var terminalAllow = []string{"mcp__conductor__guide", "mcp__conductor__report", "mcp__conductor__set_state", "mcp__conductor__ask", "mcp__conductor__task_context",
	"mcp__conductor__task_checkpoint", "mcp__conductor__task_blocked", "mcp__conductor__task_result"}

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

// Every settings file a terminal's Claude Code is launched with (each
// --settings InjectFor names, with and without tool events and yolo)
// allows the same named set as a chat's floor, under the server name of
// the MCP config the terminal is given, and is otherwise the file it was:
// its hooks, the yolo key where it had one, and nothing else.
func TestTheTerminalsSettingsAllowConductorsOwnTools(t *testing.T) {
	dir := t.TempDir()
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	mcp := settingsDoc(t, filepath.Join(dir, "claude-mcp.json"))
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(mcp["mcpServers"], &servers); err != nil || len(servers) != 1 || servers["conductor"] == nil {
		t.Fatalf("the terminal's MCP config registers %v (%v), not the one server the rules name", servers, err)
	}
	launches := map[string]bool{}
	for _, sig := range []catalog.Signal{{Kind: "hook"}, {Kind: "hook", ToolEvents: true}, {Kind: "bell"}} {
		for _, yolo := range []bool{false, true} {
			argv, _ := InjectFor("claude", dir, sig, yolo)
			if len(argv) == 0 {
				continue
			}
			if len(argv) != 2 || argv[0] != "--settings" || filepath.Dir(argv[1]) != dir {
				t.Fatalf("%+v yolo %v: %v", sig, yolo, argv)
			}
			launches[filepath.Base(argv[1])] = yolo
		}
	}
	want := map[string]bool{"claude.json": false, "claude-tools.json": false, "claude-yolo.json": true, "claude-tools-yolo.json": true, "claude-yolo-only.json": true}
	if len(launches) != len(want) {
		t.Fatalf("the launches name %v, want %v", launches, want)
	}
	for name, yolo := range want {
		if launches[name] != yolo {
			t.Fatalf("the launches name %v, want %v", launches, want)
		}
		doc := settingsDoc(t, filepath.Join(dir, name))
		var perms map[string][]string
		if err := json.Unmarshal(doc["permissions"], &perms); err != nil {
			t.Fatalf("%s: permissions %s: %v", name, doc["permissions"], err)
		}
		if len(perms) != 1 || !slices.Equal(perms["allow"], terminalAllow) {
			t.Errorf("%s: permissions %v, want allow %v alone", name, perms, terminalAllow)
		}
		keys := []string{"permissions"}
		if name != "claude-yolo-only.json" {
			keys = append(keys, "hooks")
		}
		if yolo {
			keys = append(keys, "skipDangerousModePermissionPrompt")
		}
		slices.Sort(keys)
		if got := keysOf(doc); !slices.Equal(got, keys) {
			t.Errorf("%s holds %v, want %v", name, got, keys)
		}
	}
	// The hooks are the ones the person's own settings get from Install,
	// as they were: the allow rules are the launch's alone.
	base, tools := settingsDoc(t, filepath.Join(dir, "claude.json")), settingsDoc(t, filepath.Join(dir, "claude-tools.json"))
	for name, hooks := range map[string]json.RawMessage{"claude-yolo.json": base["hooks"], "claude-tools-yolo.json": tools["hooks"]} {
		if got := settingsDoc(t, filepath.Join(dir, name))["hooks"]; compact(t, got) != compact(t, hooks) {
			t.Errorf("%s's hooks are not its plain twin's:\n%s\n%s", name, got, hooks)
		}
	}
	if !strings.Contains(string(base["hooks"]), `"/opt/conductor notify --claude-hook"`) {
		t.Fatalf("claude.json's hooks: %s", base["hooks"])
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
