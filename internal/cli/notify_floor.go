package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The floor's second look (docs/plans/chat-or-terminal.md, design 7): a
// chat's child of Claude Code runs conductor notify --protocol-floor <list>
// as a PreToolUse hook on its shell calls (claude-protocol.json). Given a
// command whose text names a path of the server's deny list it prints the
// documented deny; given any other, nothing. A folder's own settings can
// switch hooks off (disableAllHooks): the deny rules of the same file and
// the server's own judgement are the floor; this is a second look.

// floorDenyOutput is Claude Code's documented PreToolUse deny.
type floorDenyOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// floorList is the deny list file the server writes beside the floor's
// settings (agents.ClaudeProtocolFloorList).
type floorList struct {
	Deny []string `json:"deny"`
}

// protocolFloor judges the hook's payload on stdin against the deny list in
// listPath. It exits 0 either way: a deny is said in its output, and a
// payload or a list it cannot read denies nothing, as the floor's other
// two looks still hold.
func protocolFloor(listPath string, stdin io.Reader, stdout io.Writer) (int, error) {
	payload, err := readPayload(stdin)
	if err != nil {
		return 0, nil
	}
	var call struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if json.Unmarshal(payload, &call) != nil {
		return 0, nil
	}
	f, err := os.Open(listPath)
	if err != nil {
		return 0, nil
	}
	b, err := notify.ReadAllBounded(f)
	f.Close()
	if err != nil {
		return 0, nil
	}
	var list floorList
	if json.Unmarshal(b, &list) != nil {
		return 0, nil
	}
	cmd, _ := call.ToolInput["command"].(string)
	if !session.CommandNamesDenied(cmd, list.Deny) {
		return 0, nil
	}
	var out floorDenyOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "deny"
	out.HookSpecificOutput.PermissionDecisionReason = session.FloorWords
	enc, err := json.Marshal(out)
	if err != nil {
		return 0, nil
	}
	text, err := jsonText(enc, false)
	if err != nil {
		return 0, nil
	}
	fmt.Fprintln(stdout, text)
	return 0, nil
}
