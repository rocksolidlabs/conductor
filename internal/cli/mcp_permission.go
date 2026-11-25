package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// permissionTool is the MCP name of the tool Claude Code calls for a
// permission prompt in a chat (--permission-prompt-tool
// mcp__conductor__permission): the agent's program calls it, never the
// model (docs/plans/chat-or-terminal.md, design 7).
const permissionTool = "permission"

// permissionCallWait bounds a held permission call: the server answers it
// deny first, at session.PermissionWait, so this bound only ends a call
// the server never answered: PermissionWait plus one minute (the plan's,
// design 7), the eight hours Claude Code is told to wait for Conductor's
// MCP server (agents.ClaudeProtocolMCPTimeoutMs).
const permissionCallWait = session.PermissionWait + time.Minute

// maxPermissionBody bounds what the tool posts, the route's own bound
// (api.MaxPermissionBody), counted over the same bytes, the whole body: a
// call whose body would be larger is denied without a post.
const maxPermissionBody = api.MaxPermissionBody

// maxPermissionAnswer bounds what the tool reads of the server's answer:
// a verdict and a message of at most 500 bytes, each character escaped.
const maxPermissionAnswer = 16 << 10

// What the agent is told when Conductor denies a call itself, in the
// plan's words (design 13).
const (
	permissionLost         = "Conductor lost this request."
	permissionTooLarge     = "This call's input is too large for Conductor to show it whole."
	permissionInexact      = "Conductor cannot show this call exactly."
	permissionTooMany      = "Too many requests are waiting; this one was denied."
	permissionAlreadyAsked = "Already asked."
)

// permissionResult is the prompt tool's documented result: the text of
// its one block is {"behavior":"allow"} or
// {"behavior":"deny","message":"…"}.
type permissionResult struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message,omitempty"`
}

func (r permissionResult) text() string {
	b, _ := encodeJSON(r) // two strings: it always encodes
	return string(b)
}

// permissionDeny is the documented deny with message.
func permissionDeny(message string) string {
	return permissionResult{Behavior: "deny", Message: message}.text()
}

// permission asks the person about a tool call of the agent's: it posts
// the call to the session's permission route with the session's token and
// returns the server's answer as the documented result. The post is held
// until the server answers; its context ends only by the call's
// cancellation, the end of stdin, or permissionCallWait, and its client
// has no timeout of its own. Every way it can end without the server's
// answer is a deny in the plan's words: no path makes an allow the server
// did not give. The call's input is never logged nor put in an error.
// Outside a session it is errNotInSession, which tools/call answers as
// the lost request's deny.
func (s *mcpServer) permission(ctx context.Context, raw json.RawMessage) (string, error) {
	link, token, err := notify.FromEnv(s.getenv)
	if err != nil {
		return "", errNotInSession
	}
	base, ok := strings.CutSuffix(link, "/attention")
	if !ok {
		return "", errNotInSession
	}
	// The arguments Claude Code calls the tool with are not documented;
	// they are read leniently, and a call that names no tool or carries
	// no input is not one Conductor can show.
	var a struct {
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.ToolName == "" || len(a.Input) == 0 || string(a.Input) == "null" {
		return permissionDeny(permissionInexact), nil
	}
	body, err := encodeJSON(struct {
		Call  string          `json:"call"`
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}{a.ToolUseID, a.ToolName, a.Input})
	if err != nil {
		return permissionDeny(permissionInexact), nil
	}
	if len(body) > maxPermissionBody {
		return permissionDeny(permissionTooLarge), nil
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := s.after(permissionCallWait, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(pctx, http.MethodPost, base+"/permission", bytes.NewReader(body))
	if err != nil {
		return permissionDeny(permissionLost), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Transport: s.permissionTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirect refused: a token never follows a rewrite")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return permissionDeny(permissionLost), nil
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxPermissionAnswer+1))
	if err != nil || len(answer) > maxPermissionAnswer {
		return permissionDeny(permissionLost), nil
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusRequestEntityTooLarge:
		return permissionDeny(permissionTooLarge), nil
	default:
		return permissionDeny(permissionLost), nil
	}
	return permissionAnswer(answer), nil
}

// permissionAnswer is the server's answer as the documented result: an
// allow only for exactly {"behavior":"allow"}, a deny with the server's
// message, and anything else the lost request's deny.
func permissionAnswer(raw []byte) string {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var a struct {
		Behavior string  `json:"behavior"`
		Message  *string `json:"message"`
	}
	if d.Decode(&a) != nil || d.More() {
		return permissionDeny(permissionLost)
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return permissionDeny(permissionLost)
	}
	switch {
	case a.Behavior == "allow" && a.Message == nil:
		return permissionResult{Behavior: "allow"}.text()
	case a.Behavior == "deny" && a.Message != nil:
		return permissionDeny(cmp.Or(*a.Message, permissionLost))
	}
	return permissionDeny(permissionLost)
}
