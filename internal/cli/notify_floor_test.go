package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// conductor notify --protocol-floor, a chat's PreToolUse hook on its shell
// calls: given a command that names a path of the deny list its file holds
// it prints Claude Code's documented deny, given another nothing; it exits
// 0 either way and sends nothing, in a chat or not.
func TestProtocolFloorHookDenies(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	list := filepath.Join(dir, "claude-protocol-floor.json")
	b, _ := json.Marshal(map[string]any{"deny": session.DenyList([]string{data}, []string{filepath.Join(dir, "conductor.json")})})
	if err := os.WriteFile(list, b, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(payload string, args ...string) string {
		t.Helper()
		var out, errs strings.Builder
		code, err := runNotify(context.Background(), append([]string{"--protocol-floor", list}, args...), strings.NewReader(payload), &out, &errs)
		if code != 0 || err != nil || errs.Len() != 0 {
			t.Fatalf("%s: exit %d %v %q", payload, code, err, errs.String())
		}
		return out.String()
	}
	bash := func(cmd string) string {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})
		return string(b)
	}
	for _, chat := range []string{"", "1"} {
		t.Setenv("CONDUCTOR_PROTOCOL", chat)
		for _, cmd := range []string{"cat " + filepath.Join(data, "secrets.json"), "ls " + data, "cp " + filepath.Join(dir, "conductor.json.bak") + " /tmp/x"} {
			out := run(bash(cmd))
			var deny floorDenyOutput
			if err := json.Unmarshal([]byte(out), &deny); err != nil {
				t.Fatalf("%q: %q is not the deny: %v", cmd, out, err)
			}
			if h := deny.HookSpecificOutput; h.HookEventName != "PreToolUse" || h.PermissionDecision != "deny" || h.PermissionDecisionReason != session.FloorWords {
				t.Fatalf("%q: %+v", cmd, h)
			}
		}
		for _, payload := range []string{bash("make test"), bash("D=" + dir + "; cat $D/da" + "ta/x"), `{"tool_name":"Bash"}`, `not json`, ``} {
			if out := run(payload); out != "" {
				t.Fatalf("%q: %q", payload, out)
			}
		}
	}
	// A list it cannot read denies nothing (the deny rules still hold).
	var out strings.Builder
	if code, err := runNotify(context.Background(), []string{"--protocol-floor", filepath.Join(dir, "none.json")}, strings.NewReader(bash("ls "+data)), &out, &strings.Builder{}); code != 0 || err != nil || out.Len() != 0 {
		t.Fatalf("no list: exit %d %v %q", code, err, out.String())
	}
	// A hook's misuse exits 1, never the 2 a hook runner reads as a block.
	if code, _ := runNotify(context.Background(), []string{"--protocol-floor"}, strings.NewReader(""), &strings.Builder{}, &strings.Builder{}); code != 1 {
		t.Fatalf("--protocol-floor with no file: exit %d", code)
	}
}
