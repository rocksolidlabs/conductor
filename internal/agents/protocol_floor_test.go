package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A chat's floor files: both settings hold a Read and an Edit deny rule
// for every path of the deny list, the path and what is under it, an allow
// rule for each of Conductor's unasked tools and no other, and one
// PreToolUse hook on shell calls that runs conductor notify
// --protocol-floor with the list beside them; the yolo one also the yolo
// key; the list holds the deny list as given.
func TestPermissionFloorAssets(t *testing.T) {
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "a dir", "conductor")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	deny := []string{"/srv/conductor-data", "/etc/conductor/conductor.json", "/etc/conductor/*conductor.json*", "/home/ada/.conductor"}
	if err := WriteProtocolFloor(hooks, bin, deny); err != nil {
		t.Fatal(err)
	}
	for name, yolo := range map[string]bool{ClaudeProtocolSettings: false, ClaudeProtocolYoloSettings: true} {
		b, err := os.ReadFile(filepath.Join(hooks, name))
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			Permissions struct {
				Deny  []string `json:"deny"`
				Allow []string `json:"allow"`
				Ask   []string `json:"ask"`
			} `json:"permissions"`
			Hooks map[string][]struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
			Yolo *bool `json:"skipDangerousModePermissionPrompt"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, p := range deny {
			abs := "//" + strings.TrimPrefix(p, "/")
			for _, rule := range []string{"Read(" + abs + ")", "Read(" + abs + "/**)", "Edit(" + abs + ")", "Edit(" + abs + "/**)"} {
				if !slices.Contains(s.Permissions.Deny, rule) {
					t.Errorf("%s lacks %s: %v", name, rule, s.Permissions.Deny)
				}
			}
		}
		if len(s.Permissions.Deny) != 4*len(deny) {
			t.Errorf("%s: %d rules for %d paths", name, len(s.Permissions.Deny), len(deny))
		}
		// Conductor's unasked tools are allowed, each by its one tool's
		// rule, and nothing else: never the prompt tool, never a server's
		// every tool.
		want := []string{"mcp__conductor__guide", "mcp__conductor__report", "mcp__conductor__set_state", "mcp__conductor__ask", "mcp__conductor__task_context",
			"mcp__conductor__task_checkpoint", "mcp__conductor__task_blocked", "mcp__conductor__task_result"}
		if !slices.Equal(s.Permissions.Allow, want) {
			t.Errorf("%s allows %v, want %v", name, s.Permissions.Allow, want)
		}
		// A chat that asks asks for every skill call, whatever another
		// source allows; a yolo chat's floor has no ask rule.
		if yolo && s.Permissions.Ask != nil || !yolo && !slices.Equal(s.Permissions.Ask, []string{"Skill"}) {
			t.Errorf("%s asks for %v", name, s.Permissions.Ask)
		}
		if raw := string(b); yolo == strings.Contains(raw, `"ask"`) {
			t.Errorf("%s: the ask rule as written: %s", name, raw)
		}
		pre := s.Hooks["PreToolUse"]
		if len(s.Hooks) != 1 || len(pre) != 1 || pre[0].Matcher != "Bash" || len(pre[0].Hooks) != 1 || pre[0].Hooks[0].Type != "command" ||
			pre[0].Hooks[0].Command != "'"+bin+"' notify --protocol-floor "+filepath.Join(hooks, ClaudeProtocolFloorList) {
			t.Errorf("%s: the hooks %+v", name, s.Hooks)
		}
		if (s.Yolo != nil && *s.Yolo) != yolo {
			t.Errorf("%s: the yolo key %v", name, s.Yolo)
		}
	}
	b, err := os.ReadFile(filepath.Join(hooks, ClaudeProtocolFloorList))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Deny []string `json:"deny"`
	}
	if err := json.Unmarshal(b, &list); err != nil || !slices.Equal(list.Deny, deny) {
		t.Fatalf("the list %s: %v", b, err)
	}
	if err := WriteProtocolFloor("hooks", bin, deny); err == nil {
		t.Fatal("a relative hooks directory was taken")
	}
}

// The two lists of Conductor's tools are apart: a tool is unasked or asks,
// never both, and the prompt tool asks (it is never allowed).
func TestTheUnaskedToolsAreApart(t *testing.T) {
	for _, tool := range ProtocolUnaskedTools {
		if _, both := ProtocolAskedTools[tool]; both {
			t.Errorf("%s is in both lists", tool)
		}
	}
	if _, asks := ProtocolAskedTools["permission"]; !asks || slices.Contains(ProtocolUnaskedTools, "permission") {
		t.Fatal("the prompt tool is not among the tools that ask")
	}
	for tool, why := range ProtocolAskedTools {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s asks for no reason given", tool)
		}
	}
}
