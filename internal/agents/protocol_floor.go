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

// The tools of Conductor's own MCP server (`conductor mcp`) a chat's child
// calls without asking, and those it asks for as for any other call
// (docs/plans/chat-or-terminal.md, design 7, the floor). A tool is
// unasked when through it an agent tells Conductor about its own session's
// work, asks a person, or hands its work on within its own run: it starts
// only what the run's own definition starts on it (a done starts the
// members waiting on this one; a task's result lets the engine schedule
// the next attempts, or open the reviewed task again), reaches no record
// but its session's own and its run's, writes no ticket, and what it
// reports goes to the operator's own notices (the webhooks) as any report
// does. It is the smallest set an agent needs to report its own work;
// nothing is added to it without the maintainers. Every other tool asks, and a tool of the table that is in
// neither list fails a test (api's TestEveryConductorToolIsSorted), so a
// new one asks until someone puts it here.
var (
	// ProtocolUnaskedTools are allowed by the floor's settings
	// (claudeAllowRules).
	ProtocolUnaskedTools = []string{
		"guide", // the session's own guide: reads it
		// report: the session's own progress, artifacts and steps, and its
		// handoff. A handoff reaches only a member of the session's own run
		// (crew.Engine.handoff: a name no member of the run has, or the
		// reporter's own, is noted and typed nowhere; a managed run sends it
		// as a message within the run, in its lane), as one bounded line,
		// and that member acts on it under its own permission mode; a
		// session in no run has nobody to hand to. It is the crew's own baton,
		// set up by the person who formed or launched the crew, and the
		// reports are the calls an agent makes most.
		"report",
		"set_state",       // the session's own attention: working, done, clear
		"ask",             // a question to the people watching: a person answers
		"task_context",    // the attempt's own task, as .conductor/task.md holds it
		"task_checkpoint", // the attempt's own checkpoint
		"task_blocked",    // the attempt's own blocker: a question to a person
		"task_result",     // the attempt's own result: the engine judges it by its checks
	}
	// ProtocolAskedTools ask as any other call does, each for its reason.
	ProtocolAskedTools = map[string]string{
		"form_crew":         "starts other sessions",
		"add_member":        "starts another session in the run",
		"run_status":        "reads the run, beyond the session's own record",
		"link":              "mints a link that publishes the session outward",
		"open_url":          "opens a page in the person's browser",
		"message_send":      "not a call an agent reports its own work with; in a managed run a handoff carries the same message, shorter",
		"inbox_read":        "reads what other members sent",
		"inbox_ack":         "changes the run's record of messages",
		"context_lookup":    "reads beyond the session's own record, through a provider",
		"decision_request":  "opens a durable decision a router may send outward",
		"decision_list":     "reads decision records others answer and comment on",
		"decision_read":     "reads decision records others answer and comment on",
		"decision_withdraw": "changes a durable decision record",
		"ticket_read":       "reads a ticket, beyond the session's own record",
		"ticket_comment":    "writes a ticket",
		"brakeman_request":  "Brakeman's; a Brakeman turn is never a chat",
		"brief_submit":      "Brakeman's; a Brakeman turn is never a chat",
		"observe":           "Brakeman's reads, beyond the session's own record",
		"act":               "Brakeman's acts on other records",
		"permission":        "the prompt tool itself: never allowed, never called by the model",
	}
)

// claudeMCPServer is the name Conductor's server has in
// claude-protocol-mcp.json, which a rule for its tools names.
const claudeMCPServer = "conductor"

// claudeAllowRules is the floor's allow rules: one per unasked tool, in
// Claude Code's form for one tool of an MCP server, mcp__<server>__<tool>
// (permissions.md, MCP). Claude Code judges deny, then ask, then allow,
// across every settings source the child reads (the person's and the
// folder's among them): a deny or ask rule naming one of these tools wins
// over its allow, and then the call is denied or put to Conductor as any
// other. An allow rule has no effect in bypassPermissions (a yolo session).
func claudeAllowRules() []string {
	rules := make([]string, 0, len(ProtocolUnaskedTools))
	for _, t := range ProtocolUnaskedTools {
		rules = append(rules, "mcp__"+claudeMCPServer+"__"+t)
	}
	return rules
}

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

// floorSettings is a floor's settings file: its deny rules, its allow
// rules for Conductor's unasked tools, its hook, and for a yolo session the
// yolo key.
func floorSettings(hooksDir, bin string, deny []string, yolo bool) ([]byte, error) {
	hook := shellQuote(bin) + " notify --protocol-floor " + shellQuote(filepath.Join(hooksDir, ClaudeProtocolFloorList))
	settings := map[string]any{
		"permissions": map[string]any{"deny": append([]string{}, FloorRules(deny)...), "allow": claudeAllowRules()},
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
