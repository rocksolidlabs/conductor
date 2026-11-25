package agentproto

import "encoding/json"

// decodeEvent reads one line of the stream into ev, leniently: the line
// must be a JSON object, and each field the driver reads is taken only when
// its value is of the type the driver reads it as. A field of another type
// (a later version's, or another event's of the same name) is left as if
// absent, so that it changes nothing: never the end of the read.
func decodeEvent(b []byte, ev *claudeEvent) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	field(m, "type", &ev.Type)
	field(m, "subtype", &ev.Subtype)
	field(m, "uuid", &ev.UUID)
	field(m, "session_id", &ev.SessionID)
	field(m, "parent_tool_use_id", &ev.ParentToolUseID)
	field(m, "isReplay", &ev.IsReplay)
	field(m, "message", &ev.Message)
	field(m, "tool_use_result", &ev.ToolUseResult)
	field(m, "event", &ev.Event)
	field(m, "error", &ev.Error)
	field(m, "response", &ev.Response)
	field(m, "request_id", &ev.RequestID)
	field(m, "capabilities", &ev.Capabilities)
	field(m, "task_id", &ev.TaskID)
	field(m, "tool_use_id", &ev.ToolUseID)
	field(m, "ambient", &ev.Ambient)
	field(m, "patch", &ev.Patch)
	field(m, "tasks", &ev.Tasks)
	field(m, "error_status", &ev.ErrorStatus)
	field(m, "is_error", &ev.IsError)
	field(m, "api_error_status", &ev.APIErrorStatus)
	field(m, "terminal_reason", &ev.TerminalReason)
	field(m, "total_cost_usd", &ev.TotalCostUSD)
	var usage map[string]json.RawMessage
	field(m, "usage", &usage)
	field(usage, "input_tokens", &ev.Usage.InputTokens)
	field(usage, "cache_creation_input_tokens", &ev.Usage.CacheCreation)
	field(usage, "cache_read_input_tokens", &ev.Usage.CacheRead)
	field(usage, "output_tokens", &ev.Usage.OutputTokens)
	return nil
}

// field sets *dst from m[key] when the value decodes as a T; otherwise it
// leaves *dst as it is.
func field[T any](m map[string]json.RawMessage, key string, dst *T) {
	raw, ok := m[key]
	if !ok {
		return
	}
	var v T
	if json.Unmarshal(raw, &v) == nil {
		*dst = v
	}
}
