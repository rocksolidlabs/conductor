package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// A chat's floor (docs/plans/chat-or-terminal.md, design 7): the one
// --settings a chat's child of Claude Code takes (claude-protocol.json, or
// claude-protocol-yolo.json for a yolo session, the yolo key beside it)
// holds a Read and an Edit deny rule for every path of the server's deny
// list, which "block in every mode, including bypassPermissions" and keep
// an @ reference from including such a file, and a PreToolUse hook on shell
// calls, conductor notify --protocol-floor, a second look a folder's own
// settings can switch off. The server writes them at its start, with the
// deny list it has then (claude-protocol-floor.json, which the hook reads).

// The floor's files in the hooks directory (agentproto names the settings
// a chat's child takes).
const (
	ClaudeProtocolSettings     = "claude-protocol.json"
	ClaudeProtocolYoloSettings = "claude-protocol-yolo.json"
	ClaudeProtocolFloorList    = "claude-protocol-floor.json"
)

// claudeShellTool is the tool the floor's hook looks at.
const claudeShellTool = "Bash"

// FloorRules is the floor's deny rules for deny: for every path, a Read and
// an Edit rule for the path itself and for everything under it, absolute
// from the root ("//path", Claude Code's form). Empty and relative paths are
// left out: every entry of a server's deny list is absolute.
func FloorRules(deny []string) []string {
	var rules []string
	for _, p := range deny {
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		abs := "//" + strings.TrimLeft(filepath.ToSlash(p), "/")
		under := strings.TrimRight(abs, "/") + "/**"
		for _, tool := range []string{"Read", "Edit"} {
			for _, r := range []string{tool + "(" + abs + ")", tool + "(" + under + ")"} {
				if !slices.Contains(rules, r) {
					rules = append(rules, r)
				}
			}
		}
	}
	return rules
}

// floorSettings is a floor's settings file: its deny rules, its hook, and
// for a yolo session the yolo key.
func floorSettings(hooksDir, bin string, deny []string, yolo bool) ([]byte, error) {
	hook := shellQuote(bin) + " notify --protocol-floor " + shellQuote(filepath.Join(hooksDir, ClaudeProtocolFloorList))
	settings := map[string]any{
		"permissions": map[string]any{"deny": append([]string{}, FloorRules(deny)...)},
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": claudeShellTool,
				"hooks":   []any{map[string]any{"type": "command", "command": hook}},
			}},
		},
	}
	if yolo {
		settings["skipDangerousModePermissionPrompt"] = true
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	return indentJSON(b)
}

// WriteProtocolFloor writes the floor's two settings files and its deny
// list into hooksDir (an absolute directory WriteAssets made), for the
// conductor binary bin and the server's deny list deny. A mode it cannot
// set stops nothing (a *ModeError, as WriteAssets says).
func WriteProtocolFloor(hooksDir, bin string, deny []string) error {
	if !filepath.IsAbs(hooksDir) {
		return fmt.Errorf("the hooks directory %q is not an absolute path", hooksDir)
	}
	if err := checkBin(bin); err != nil {
		return err
	}
	list, err := json.Marshal(map[string]any{"deny": append([]string{}, deny...)})
	if err != nil {
		return err
	}
	list, err = indentJSON(list)
	if err != nil {
		return err
	}
	files := map[string][]byte{ClaudeProtocolFloorList: list}
	for name, yolo := range map[string]bool{ClaudeProtocolSettings: false, ClaudeProtocolYoloSettings: true} {
		b, err := floorSettings(hooksDir, bin, deny, yolo)
		if err != nil {
			return err
		}
		files[name] = b
	}
	var modes []error
	for _, name := range slices.Sorted(maps.Keys(files)) {
		p := filepath.Join(hooksDir, name)
		_, err := replaceFile(p, p, files[name], 0o600)
		var me *modeError
		if errors.As(err, &me) {
			modes = append(modes, me.err)
			continue
		}
		if err != nil {
			return err
		}
	}
	if len(modes) > 0 {
		return &ModeError{Errs: modes}
	}
	return nil
}
