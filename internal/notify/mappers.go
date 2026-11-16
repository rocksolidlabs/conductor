package notify

// Mappers turn an agent's hook payload into a Request: an attention state, or an event.

import (
	"encoding/json"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// claudeAsks is what a Claude Code PermissionRequest asks, in words: its
// message when it has one; for ExitPlanMode the plan's approval, for
// AskUserQuestion its first question; "Allow <tool>?" otherwise. The
// report carries no choices: Claude Code's dialogs differ by tool,
// permission mode and release (2.1.304's Bash dialog has four, its 3
// switching to auto mode), and the session reads them off the dialog once
// it is drawn (the catalog's Claude Code question, answersFromScreen); a
// dialog the screen does not show them for is answered in the terminal.
func claudeAsks(h ClaudeHook) string {
	if msg := strings.TrimSpace(h.Message); msg != "" {
		return msg
	}
	switch h.ToolName {
	case "":
		return "Allow this action?"
	case "ExitPlanMode":
		return "Claude Code has a plan for you to approve"
	case "AskUserQuestion":
		var in struct {
			Questions []struct {
				Question string `json:"question"`
			} `json:"questions"`
		}
		if json.Unmarshal(h.ToolInput, &in) == nil && len(in.Questions) > 0 {
			if q := strings.TrimSpace(in.Questions[0].Question); q != "" {
				return q
			}
		}
		return "Claude Code asks you a question"
	}
	return "Allow " + h.ToolName + "?"
}

// claudePlan is the plan a Claude Code PermissionRequest for ExitPlanMode
// asks approval for: its tool input's planFilePath (2.1.304 carries the
// plan's text as plan beside it), which the session opens in the editor
// (#289). "" for any other hook, or a path past MaxPlanPath bytes, which
// names no file a session keeps.
func claudePlan(h ClaudeHook) string {
	if h.ToolName != "ExitPlanMode" || len(h.ToolInput) == 0 {
		return ""
	}
	var in struct {
		PlanFilePath string `json:"planFilePath"`
	}
	if json.Unmarshal(h.ToolInput, &in) != nil || len(in.PlanFilePath) > proto.MaxPlanPath {
		return ""
	}
	return strings.TrimSpace(in.PlanFilePath)
}

// ClaudeHook is the subset of the Claude Code hook stdin payload we use.
// Field names follow the documented common fields; message/title/
// notification_type are read when present.
type ClaudeHook struct {
	HookEventName        string          `json:"hook_event_name"`
	NotificationType     string          `json:"notification_type"`
	Message              string          `json:"message"`
	Title                string          `json:"title"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	Error                json.RawMessage `json:"error"`
	SessionID            string          `json:"session_id"`
	Cwd                  string          `json:"cwd"`
}

// claudeFiles are the file events a Claude Code tool call yields (design
// 4e): Read reads, Edit, MultiEdit and NotebookEdit edit, Write writes, the
// path from the tool's input (file_path, or notebook_path); Bash names the
// files its command plainly reads or writes (shellFiles, from cwd). The
// rest name no file.
func claudeFiles(tool string, input json.RawMessage, cwd string) []FileRef {
	if tool == "Bash" {
		return shellFiles(commandOf(input), cwd)
	}
	op := ""
	switch tool {
	case "Read":
		op = "read"
	case "Edit", "MultiEdit", "NotebookEdit":
		op = "edit"
	case "Write":
		op = "write"
	default:
		return nil
	}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return nil
	}
	p := firstOf(in.FilePath, in.NotebookPath)
	if p == "" {
		return nil
	}
	return []FileRef{{Op: op, Path: p}}
}

// codexFiles are the file events a Codex tool call yields: the files an
// apply_patch adds, updates or deletes, from the patch's headers (the patch
// is tool_input.command, as Codex 0.161 sends it; input and patch are read
// too), and the files its shell tool (Bash) plainly reads or writes
// (shellFiles, from cwd). Verified against a live capture
// (testdata/codex-hooks.json).
func codexFiles(tool string, input json.RawMessage, cwd string) []FileRef {
	if len(input) == 0 {
		return nil
	}
	switch tool {
	case "Bash", "shell", "exec_command", "local_shell":
		return shellFiles(commandOf(input), cwd)
	case "apply_patch":
	default:
		return nil
	}
	var in struct {
		Input   string          `json:"input"`
		Patch   string          `json:"patch"`
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var cmd string
	_ = json.Unmarshal(in.Command, &cmd)
	var out []FileRef
	for _, line := range strings.Split(firstOf(in.Input, in.Patch, cmd), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			out = append(out, FileRef{Op: "write", Path: strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))})
		case strings.HasPrefix(line, "*** Update File: "):
			out = append(out, FileRef{Op: "edit", Path: strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))})
		case strings.HasPrefix(line, "*** Delete File: "):
			out = append(out, FileRef{Op: "delete", Path: strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))})
		}
		if len(out) == 32 {
			break
		}
	}
	return out
}

// commandOf is a shell tool's command from its input's `command`: a string,
// or an argv (["bash", "-lc", "cat x"] is its last word; another argv its
// words joined).
func commandOf(input json.RawMessage) string {
	var in struct {
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Command) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(in.Command, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(in.Command, &argv) != nil || len(argv) == 0 {
		return ""
	}
	if len(argv) >= 3 && strings.HasPrefix(argv[len(argv)-2], "-") && strings.Contains(argv[len(argv)-2], "c") {
		return argv[len(argv)-1]
	}
	return strings.Join(argv, " ")
}

// copilotFiles are the file events a GitHub Copilot CLI tool call yields
// when it succeeded: view reads, edit edits and create writes the args'
// path; bash names what its command plainly reads or writes (shellFiles,
// from cwd). The args come as an object (Copilot 1.0.91), or as a JSON
// string. Verified against a live capture (testdata/copilot-hooks.json).
func copilotFiles(tool string, args, result json.RawMessage, cwd string) []FileRef {
	var r struct {
		ResultType string `json:"resultType"`
	}
	if json.Unmarshal(result, &r) == nil && r.ResultType != "" && r.ResultType != "success" {
		return nil
	}
	var str string
	if json.Unmarshal(args, &str) == nil {
		args = json.RawMessage(str)
	}
	var in struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &in) != nil {
		return nil
	}
	switch tool {
	case "view":
		return fileRef("read", in.Path)
	case "edit", "str_replace", "insert":
		return fileRef("edit", in.Path)
	case "create":
		return fileRef("write", in.Path)
	case "bash":
		return shellFiles(in.Command, cwd)
	}
	return nil
}

// agyFiles are the file events an Antigravity tool call yields: view_file
// (and its outline) reads AbsolutePath; write_to_file writes TargetFile;
// replace_file_content, multi_replace_file_content and edit_file edit it;
// delete_file deletes its path; run_command names what CommandLine plainly
// reads or writes, from Cwd. Verified against a live capture
// (testdata/agy-hooks.json), but for delete_file and edit_file, whose
// argument names are guessed from the others'.
func agyFiles(tool string, args json.RawMessage) []FileRef {
	var in struct {
		AbsolutePath string `json:"AbsolutePath"`
		TargetFile   string `json:"TargetFile"`
		Path         string `json:"Path"`
		CommandLine  string `json:"CommandLine"`
		Cwd          string `json:"Cwd"`
	}
	if len(args) == 0 || json.Unmarshal(args, &in) != nil {
		return nil
	}
	switch tool {
	case "view_file", "view_file_outline":
		return fileRef("read", in.AbsolutePath)
	case "write_to_file":
		return fileRef("write", in.TargetFile)
	case "replace_file_content", "multi_replace_file_content", "edit_file":
		return fileRef("edit", in.TargetFile)
	case "delete_file":
		return fileRef("delete", firstOf(in.AbsolutePath, in.TargetFile, in.Path))
	case "run_command":
		return shellFiles(in.CommandLine, in.Cwd)
	}
	return nil
}

// fileRef is one file event, none for an empty path.
func fileRef(op, p string) []FileRef {
	if p == "" {
		return nil
	}
	return []FileRef{{Op: op, Path: p}}
}

// MapClaudeHook turns a Claude Code hook payload into an attention update, or
// into an event for the tool hooks: a denied permission, a tool that ran or
// failed, a subagent that finished. ok is false for hooks that mean nothing
// to Conductor.
func MapClaudeHook(raw []byte) (req Request, ok bool) {
	var h ClaudeHook
	if json.Unmarshal(raw, &h) != nil {
		return Request{}, false
	}
	req, ok = mapClaudeHook(h)
	if ok && req.Event == "" {
		req.AgentSession = truncate(h.SessionID, 128)
		req.Turn = h.HookEventName == "Stop" || h.HookEventName == "UserPromptSubmit"
	}
	return req, ok
}

func mapClaudeHook(h ClaudeHook) (req Request, ok bool) {
	switch h.HookEventName {
	case "PermissionRequest":
		return Request{State: "needs_input", Message: truncate(claudeAsks(h), 200), Kind: "permission", Plan: claudePlan(h)}, true
	case "Notification":
		msg := strings.TrimSpace(h.Message)
		if msg == "" {
			msg = strings.TrimSpace(h.Title)
		}
		if msg == "" {
			msg = strings.ReplaceAll(h.NotificationType, "_", " ")
		}
		if msg == "" {
			msg = "Claude Code needs your input"
		}
		switch h.NotificationType {
		case "auth_success", "elicitation_complete", "elicitation_response", "agent_completed":
			return Request{State: "working", Message: truncate(msg, 200)}, true
		case "permission_prompt":
			return Request{State: "needs_input", Message: truncate(msg, 200), Kind: "permission"}, true
		case "idle_prompt":
			// Claude Code at rest after its turn ("Claude is waiting for your
			// input", sent after a minute idle) asks nothing: it is done, as
			// its Stop said, and a crew's handoffs and broadcasts reach it.
			return Request{State: "done", Message: truncate(msg, 200), Kind: "done"}, true
		}
		return Request{State: "needs_input", Message: truncate(msg, 200), Kind: "prompt"}, true
	case "Stop":
		return Request{State: "done", Message: truncate(strings.TrimSpace(h.LastAssistantMessage), 200), Kind: "done"}, true
	case "UserPromptSubmit", "PreToolUse", "SessionStart":
		return Request{State: "working"}, true
	case "PermissionDenied":
		return Request{Event: "tool_denied", Tool: h.ToolName}, true
	case "PostToolUse":
		return Request{Event: "tool_use", Tool: h.ToolName, Files: claudeFiles(h.ToolName, h.ToolInput, h.Cwd)}, true
	case "PostToolUseFailure":
		return Request{Event: "error", Message: truncate(errorText(h.Error), 200), Tool: h.ToolName}, true
	case "SubagentStop":
		return Request{Event: "progress", Message: "subagent finished"}, true
	}
	return Request{}, false
}

// CodexPayload is the subset of the Codex notify payload we use.
type CodexPayload struct {
	Type                 string   `json:"type"`
	LastAssistantMessage string   `json:"last-assistant-message"`
	ThreadID             string   `json:"thread-id"`
	TurnID               string   `json:"turn-id"`
	InputMessages        []string `json:"input-messages"`
}

// turnName is the name a payload gives its turn, as a report carries it
// (Request.TurnID): left out when it is out of a turn's bound or shape
// (proto.ValidTurnID), never cut, since two names cut could become one.
func turnName(id string) string {
	if !proto.ValidTurnID(id) {
		return ""
	}
	return id
}

// codexTitlePrompt begins what Codex asks the hidden thread that names a
// conversation (live, codex 0.159): its turn reports through notify like a
// turn of the user's, with a thread id of its own, and is not one.
const codexTitlePrompt = "Generate a concise, single-line task title"

// MapCodex turns a Codex notify payload into an attention update: the end of
// a turn is done, as Claude Code's Stop is (a finished agent is idle, waiting
// for its next line, and a crew types handoffs and broadcasts into it); what
// Codex asks a person comes through its bell and its hooks. The turn of the
// hidden thread that titles a conversation is not the user's, and maps to
// nothing.
func MapCodex(raw []byte) (req Request, ok bool) {
	var p CodexPayload
	if json.Unmarshal(raw, &p) != nil {
		return Request{}, false
	}
	switch p.Type {
	case "agent-turn-complete":
		if len(p.InputMessages) > 0 && strings.HasPrefix(p.InputMessages[0], codexTitlePrompt) {
			return Request{}, false
		}
		msg := truncate(strings.TrimSpace(p.LastAssistantMessage), 200)
		if msg == "" {
			msg = "Codex finished its turn"
		}
		return Request{State: "done", Message: msg, Kind: "done", AgentSession: truncate(p.ThreadID, 128), Turn: true, TurnID: turnName(p.TurnID)}, true
	}
	return Request{}, false
}

// MapCodexHook turns a payload of Codex's hooks (hooks.json, run with
// features.hooks on) into an update. MapCodex reads the payload of Codex's
// notify program instead. A permission request has no quick-reply options:
// the keys Codex's dialog takes are not verified yet.
func MapCodexHook(raw []byte) (req Request, ok bool) {
	var h struct {
		HookEventName        string          `json:"hook_event_name"`
		ToolName             string          `json:"tool_name"`
		ToolInput            json.RawMessage `json:"tool_input"`
		LastAssistantMessage string          `json:"last_assistant_message"`
		SessionID            string          `json:"session_id"`
		TurnID               string          `json:"turn_id"`
		Cwd                  string          `json:"cwd"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return Request{}, false
	}
	// Every hook Conductor installs for Codex names its turn (turn_id, the
	// notify program's turn-id for the same turn): a report of the turn's
	// work after its end was recorded says the turn went on.
	turn := turnName(h.TurnID)
	switch h.HookEventName {
	case "Stop":
		return Request{State: "done", Message: truncate(strings.TrimSpace(h.LastAssistantMessage), 200), Kind: "done", AgentSession: truncate(h.SessionID, 128), Turn: true, TurnID: turn}, true
	case "PostToolUse":
		return Request{Event: "tool_use", Tool: h.ToolName, Files: codexFiles(h.ToolName, h.ToolInput, h.Cwd)}, true
	case "PermissionRequest":
		msg := "Codex asks for permission"
		if h.ToolName != "" {
			msg = "Allow " + h.ToolName + "?"
		}
		return Request{State: "needs_input", Message: truncate(msg, 200), Kind: "permission", TurnID: turn}, true
	case "PreToolUse":
		return Request{State: "working", TurnID: turn}, true
	}
	return Request{}, false
}

// MapCopilotHook turns a GitHub Copilot CLI hook payload into an update.
// Copilot names few of its events: a hook_event_name it knows decides, and
// otherwise the fields that are there do. notification_type is a
// notification, error an error, toolResult a tool that ran, toolArgs without
// one a tool about to run (preToolUse: the agent is working, as Claude
// Code's PreToolUse says), stopReason the end of a turn and prompt a prompt
// the user sent. A permission prompt has no quick-reply options: the keys
// Copilot's dialog takes are not verified yet.
func MapCopilotHook(raw []byte) (req Request, ok bool) {
	var h struct {
		HookEventName    string          `json:"hook_event_name"`
		NotificationType *string         `json:"notification_type"`
		Message          string          `json:"message"`
		Title            string          `json:"title"`
		ToolName         string          `json:"toolName"`
		ToolArgs         json.RawMessage `json:"toolArgs"`
		ToolResult       json.RawMessage `json:"toolResult"`
		Cwd              string          `json:"cwd"`
		StopReason       *string         `json:"stopReason"`
		Prompt           *string         `json:"prompt"`
		Error            json.RawMessage `json:"error"`
		SessionID        string          `json:"sessionId"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return Request{}, false
	}
	event := strings.ToLower(h.HookEventName)
	if !slices.Contains([]string{"notification", "erroroccurred", "pretooluse", "posttooluse", "agentstop", "userpromptsubmitted"}, event) {
		// No name, or one this mapper does not know: the fields decide.
		event = ""
		switch {
		case h.NotificationType != nil:
			event = "notification"
		case present(h.Error):
			event = "erroroccurred"
		case present(h.ToolResult):
			event = "posttooluse"
		case h.ToolName != "" && present(h.ToolArgs):
			event = "pretooluse"
		case h.StopReason != nil:
			event = "agentstop"
		case h.Prompt != nil:
			event = "userpromptsubmitted"
		}
	}
	switch event {
	case "notification":
		msg := truncate(firstOf(h.Message, h.Title, "Copilot needs your input"), 200)
		if h.NotificationType != nil && *h.NotificationType == "permission_prompt" {
			return Request{State: "needs_input", Message: msg, Kind: "permission"}, true
		}
		return Request{State: "needs_input", Message: msg, Kind: "prompt"}, true
	case "erroroccurred":
		return Request{Event: "error", Message: truncate(errorText(h.Error), 200), Tool: h.ToolName}, true
	case "pretooluse":
		return Request{State: "working"}, true
	case "posttooluse":
		return Request{Event: "tool_use", Tool: h.ToolName, Files: copilotFiles(h.ToolName, h.ToolArgs, h.ToolResult, h.Cwd)}, true
	case "agentstop":
		return Request{State: "done", Kind: "done", AgentSession: truncate(h.SessionID, 128), Turn: true}, true
	case "userpromptsubmitted":
		return Request{State: "working", AgentSession: truncate(h.SessionID, 128), Turn: true}, true
	}
	return Request{}, false
}

// MapCursorHook turns a Cursor CLI hook payload into an update: the end of a
// turn, a prompt submitted or a tool about to run (the agent is working, as
// Claude Code's PreToolUse says), or a tool that ran (a file edit is one,
// named after the file: its name, not its path, which could fill the 100
// bytes the server keeps).
func MapCursorHook(raw []byte) (req Request, ok bool) {
	var h struct {
		HookEventName  string `json:"hook_event_name"`
		ToolName       string `json:"tool_name"`
		FilePath       string `json:"file_path"`
		ConversationID string `json:"conversation_id"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return Request{}, false
	}
	switch h.HookEventName {
	case "stop":
		return Request{State: "done", Kind: "done", AgentSession: truncate(h.ConversationID, 128), Turn: true}, true
	case "beforeSubmitPrompt":
		return Request{State: "working", AgentSession: truncate(h.ConversationID, 128), Turn: true}, true
	case "preToolUse":
		return Request{State: "working"}, true
	case "postToolUse":
		return Request{Event: "tool_use", Tool: h.ToolName}, true
	case "afterFileEdit":
		if h.FilePath == "" {
			return Request{Event: "tool_use", Tool: "edit"}, true
		}
		return Request{Event: "tool_use", Tool: "edit " + path.Base(h.FilePath), Files: []FileRef{{Op: "edit", Path: h.FilePath}}}, true
	}
	return Request{}, false
}

// MapAgyHook turns an Antigravity hook payload into an update. The payload
// names no event, and the flag that ran the mapper says only that it is
// Antigravity's: a toolCall with the error field is a tool that ran
// (PostToolUse, which carries the field, empty for a tool that succeeded),
// one without it a tool about to run (PreToolUse: the agent is working, as
// Claude Code's PreToolUse says), a terminationReason the end of a turn
// (Stop). Verified against Antigravity 1.2.14 (testdata/agy-hooks.json).
func MapAgyHook(raw []byte) (req Request, ok bool) {
	var h struct {
		ToolCall *struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"toolCall"`
		Error             *string `json:"error"`
		TerminationReason *string `json:"terminationReason"`
		ConversationID    string  `json:"conversationId"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return Request{}, false
	}
	switch {
	case h.ToolCall != nil && h.Error == nil:
		return Request{State: "working"}, true
	case h.ToolCall != nil:
		var files []FileRef
		if *h.Error == "" {
			files = agyFiles(h.ToolCall.Name, h.ToolCall.Args)
		}
		return Request{Event: "tool_use", Tool: h.ToolCall.Name, Files: files}, true
	case h.TerminationReason != nil:
		return Request{State: "done", Message: truncate(strings.TrimSpace(*h.TerminationReason), 200), Kind: "done", AgentSession: truncate(h.ConversationID, 128), Turn: true}, true
	}
	return Request{}, false
}

// MapGooseHook turns a Goose hook payload, which names its event in "event",
// into an update: the end of a turn (with its last message), a prompt
// submitted or a tool about to run (the agent is working, as Claude Code's
// PreToolUse says), or a tool that ran with the files it touched: write
// writes and edit edits tool_input's path (relative to working_dir), shell
// names what its command plainly reads or writes (shellFiles). Verified
// against Goose 1.54.0 (testdata/goose-hooks.json).
func MapGooseHook(raw []byte) (req Request, ok bool) {
	var h struct {
		Event                string          `json:"event"`
		ToolName             string          `json:"tool_name"`
		ToolInput            json.RawMessage `json:"tool_input"`
		WorkingDir           string          `json:"working_dir"`
		LastAssistantMessage string          `json:"last_assistant_message"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return Request{}, false
	}
	switch h.Event {
	case "Stop":
		return Request{State: "done", Kind: "done", Message: truncate(strings.TrimSpace(h.LastAssistantMessage), 200)}, true
	case "UserPromptSubmit", "PreToolUse":
		return Request{State: "working"}, true
	case "PostToolUse":
		return Request{Event: "tool_use", Tool: h.ToolName, Files: gooseFiles(h.ToolName, h.ToolInput, h.WorkingDir)}, true
	}
	return Request{}, false
}

// gooseFiles are the file events a Goose tool call yields (its developer
// tools, named bare or with their extension's prefix).
func gooseFiles(tool string, input json.RawMessage, dir string) []FileRef {
	var in struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return nil
	}
	abs := func(p string) string {
		if p != "" && dir != "" && !path.IsAbs(p) {
			return path.Join(dir, p)
		}
		return p
	}
	switch strings.TrimPrefix(tool, "developer__") {
	case "write":
		return fileRef("write", abs(in.Path))
	case "edit":
		return fileRef("edit", abs(in.Path))
	case "shell":
		return shellFiles(in.Command, dir)
	}
	return nil
}

// present reports whether a JSON field was sent with a value other than null.
func present(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// errorText reads an error a hook reports, which is a string or an object
// with a message.
func errorText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var e struct {
		Message string `json:"message"`
		Name    string `json:"name"`
	}
	if json.Unmarshal(raw, &e) == nil {
		return strings.TrimSpace(firstOf(e.Message, e.Name, ""))
	}
	return ""
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// truncate cuts s to at most n bytes, on a character boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
