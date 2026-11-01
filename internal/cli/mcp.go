package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// conductor mcp is Conductor's MCP server on stdio: the same reports and
// crew actions the skill teaches as commands, as tools an agent calls
// directly. The server launches it for agents that take an MCP server at
// launch (Claude Code's --mcp-config, Codex's -c mcp_servers.…). Outside a
// Conductor session every tool answers that it is outside one, so a
// registration that outlives the session does no harm. The protocol is
// JSON-RPC 2.0, one message a line, with initialize, ping, tools/list and
// tools/call; everything else is a method-not-found.
const mcpUsage = `Usage: conductor mcp

Serves Conductor's MCP tools on stdin and stdout (JSON-RPC 2.0, one message a line):
report, set_state, ask, form_crew, add_member, run_status and link, the skill's
commands as tools, and guide, what the server says this session may do. The server registers it with agents that take an MCP server at
launch; run it by hand only to try it.
`

// mcpProtocolVersion is the MCP revision answered when the client's is unknown.
const mcpProtocolVersion = "2025-06-18"

// maxMCPLine bounds one message.
const maxMCPLine = 1 << 20

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

// mcpTool is one tool as tools/list describes it.
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpHandled is every tool call answers: tools/list lists an operation
// of the table (api.Operations) whose MCP name is here, and no other, so
// a tool the table names before its handler exists is never listed.
var mcpHandled = map[string]bool{
	"report": true, "set_state": true, "ask": true,
	"form_crew": true, "add_member": true, "run_status": true, "link": true,
	"task_context": true, "task_result": true, "message_send": true, "inbox_read": true, "inbox_ack": true, "task_blocked": true, "task_checkpoint": true,
	"ticket_read": true, "ticket_comment": true,
	"guide": true,
}

// mcpTools is the tools as tools/list gives them, from the server's table
// of operations: its names, descriptions and schemas are the table's, so
// the tools and the guide cannot drift (internal/api, guide.go).
func mcpTools() []mcpTool { return mcpToolsFrom(api.Operations()) }

// mcpToolsFrom is the tools of ops that call has a handler for, in ops' order.
func mcpToolsFrom(ops []api.Operation) []mcpTool {
	var out []mcpTool
	for _, op := range ops {
		if op.MCP != "" && mcpHandled[op.MCP] {
			out = append(out, mcpTool{Name: op.MCP, Description: op.Description, InputSchema: op.Input})
		}
	}
	return out
}

// mcpServer serves one agent.
type mcpServer struct {
	out    io.Writer
	getenv func(string) string
}

func runMcp(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Fprint(stderr, mcpUsage)
			return 0, nil
		}
		fmt.Fprint(stderr, mcpUsage)
		return 2, errors.New("mcp takes no arguments")
	}
	s := &mcpServer{out: stdout, getenv: os.Getenv}
	return s.serve(ctx, stdin)
}

// serve reads messages until stdin ends.
func (s *mcpServer) serve(ctx context.Context, in io.Reader) (int, error) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), maxMCPLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req mcpRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil || req.JSONRPC != "2.0" {
			s.reply(mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{Code: -32700, Message: "parse error: one JSON-RPC 2.0 message a line"}})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			// A notification (initialized, cancelled, …): nothing to answer.
			continue
		}
		result, rpcErr := s.handle(ctx, req)
		if rpcErr != nil {
			s.reply(mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
		} else {
			s.reply(mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return 1, err
	}
	return 0, nil
}

// reply writes one message, a line, with HTML escaping off (encodeJSON):
// a ticket of '<' would be six times its size otherwise.
func (s *mcpServer) reply(r mcpResponse) {
	b, _ := encodeJSON(r)
	_, _ = s.out.Write(append(b, '\n'))
}

func (s *mcpServer) handle(ctx context.Context, req mcpRequest) (any, *mcpError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		pv := p.ProtocolVersion
		if pv == "" {
			pv = mcpProtocolVersion
		}
		return map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "conductor", "version": version.Version},
			"instructions":    "Conductor shows this session to people in a browser. Report milestones, artifacts and handoffs with report; ask with ask when you need a decision; form a crew with form_crew when the work splits. Call guide to learn what you may do here.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &mcpError{Code: -32602, Message: "tools/call needs a name"}
		}
		text, err := s.call(ctx, p.Name, p.Arguments)
		if err != nil {
			var unknown unknownTool
			if errors.As(err, &unknown) {
				return nil, &mcpError{Code: -32602, Message: err.Error()}
			}
			return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}, nil
	}
	return nil, &mcpError{Code: -32601, Message: "method not found: " + req.Method}
}

type unknownTool string

func (u unknownTool) Error() string { return "unknown tool " + string(u) }

var errNotInSession = errors.New("not running inside a Conductor session: nothing to report to")

// call runs one tool; what it returns is the text the agent reads.
func (s *mcpServer) call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	switch name {
	case "report", "set_state", "ask":
		url, token, err := notify.FromEnv(s.getenv)
		if err != nil {
			return "", errNotInSession
		}
		var req notify.Request
		switch name {
		case "report":
			var a struct {
				Event, Message, URL, To, Tool, Op, Path string
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("report: %w", err)
			}
			if a.Event == "" {
				return "", errors.New("report needs an event")
			}
			req = notify.Request{Event: a.Event, Message: a.Message, URL: a.URL, To: a.To, Tool: a.Tool, Op: a.Op, Path: a.Path}
		case "set_state":
			var a struct{ State, Message string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("set_state: %w", err)
			}
			if a.State != "working" && a.State != "done" && a.State != "clear" {
				return "", errors.New("set_state: state must be working, done or clear")
			}
			req = notify.Request{State: a.State, Message: a.Message}
		case "ask":
			var a struct {
				Message string
				Choices []string
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("ask: %w", err)
			}
			if strings.TrimSpace(a.Message) == "" {
				return "", errors.New("ask needs a message")
			}
			req = notify.Request{State: "needs_input", Message: a.Message, Ask: true}
			if len(a.Choices) > 0 {
				opts, err := parseChoices(strings.Join(a.Choices, "|"))
				if err != nil {
					return "", err
				}
				req.Kind = "prompt"
				req.Options = opts
			}
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := notify.Send(sctx, url, token, req); err != nil {
			return "", err
		}
		return "reported", nil
	case "form_crew", "add_member", "run_status", "link", "task_context", "task_result", "message_send", "inbox_read", "inbox_ack", "task_blocked", "task_checkpoint",
		"ticket_read", "ticket_comment", "guide":
		c, err := crewClientFromEnv(s.getenv)
		if err != nil {
			return "", errNotInSession
		}
		if strings.HasPrefix(name, "task_") || strings.HasPrefix(name, "message_") || strings.HasPrefix(name, "inbox_") || strings.HasPrefix(name, "ticket_") {
			c.prefix = "task"
		}
		if name == "guide" {
			c.prefix = "guide"
		}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		switch name {
		case "form_crew":
			if s, _ := body["self"].(string); s == "" {
				return "", errors.New("form_crew: say which member this session becomes (self)")
			}
			out, err := c.call(cctx, http.MethodPost, "/crew", body)
			if err != nil {
				return "", err
			}
			run, _ := out["run"].(map[string]any)
			return fmt.Sprintf("crew formed: run %v at %v; you are %v\n%s", run["id"], out["url"], out["member"], membersText(run)), nil
		case "add_member":
			m, _ := body["member"].(map[string]any)
			if m == nil {
				return "", errors.New("add_member needs a member")
			}
			out, err := c.call(cctx, http.MethodPost, "/run/members", m)
			if err != nil {
				return "", err
			}
			run, _ := out["run"].(map[string]any)
			return fmt.Sprintf("added %v\n%s", m["name"], membersText(run)), nil
		case "run_status":
			out, err := c.call(cctx, http.MethodGet, "/run", nil)
			if err != nil {
				return "", err
			}
			run, _ := out["run"].(map[string]any)
			return fmt.Sprintf("run %v (%v) %v; you are %v\n%s", run["id"], run["state"], run["name"], out["member"], membersText(run)), nil
		case "link":
			out, err := c.call(cctx, http.MethodPost, "/links/agent", body)
			if err != nil {
				return "", err
			}
			return fmt.Sprint(out["url"]), nil
		case "task_context":
			path := "/task"
			if p, ok := body["page"].(float64); ok && p >= 1 {
				path = fmt.Sprintf("/task?page=%d", int(p))
			}
			out, err := c.call(cctx, http.MethodGet, path, nil)
			if err != nil {
				return "", err
			}
			text, _ := out["markdown"].(string)
			if pages, _ := out["pages"].(float64); pages > 1 {
				text += fmt.Sprintf("\n\n(page %v of %v: call task_context with the next page for the rest)", out["page"], int(pages))
			}
			return text, nil
		case "task_result":
			out, err := c.call(cctx, http.MethodPost, "/task/result", body)
			if err != nil {
				return "", err
			}
			task, _ := out["task"].(map[string]any)
			return fmt.Sprintf("submitted: task %v is %v", task["id"], task["state"]), nil
		case "message_send":
			out, err := c.call(cctx, http.MethodPost, "/messages", body)
			if err != nil {
				return "", err
			}
			m, _ := out["message"].(map[string]any)
			return fmt.Sprintf("sent #%v to %v; it is typed into %v's session once that one has its task", m["seq"], m["to"], m["to"]), nil
		case "inbox_read":
			path := "/inbox"
			if a, ok := body["after"].(float64); ok && a > 0 {
				path = fmt.Sprintf("/inbox?after=%d", int64(a))
			}
			out, err := c.call(cctx, http.MethodGet, path, nil)
			if err != nil {
				return "", err
			}
			return inboxText(out), nil
		case "inbox_ack":
			out, err := c.call(cctx, http.MethodPost, "/inbox/ack", body)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("acknowledged %v", out["acked"]), nil
		case "task_blocked":
			out, err := c.call(cctx, http.MethodPost, "/task/blocked", body)
			if err != nil {
				return "", err
			}
			task, _ := out["task"].(map[string]any)
			return fmt.Sprintf("task %v shows as blocked; your session asks the people watching. Report working, or submit, once you go on.", task["id"]), nil
		case "task_checkpoint":
			out, err := c.call(cctx, http.MethodPost, "/task/checkpoint", body)
			if err != nil {
				return "", err
			}
			a, _ := out["attempt"].(map[string]any)
			cp, _ := a["checkpoint"].(map[string]any)
			return fmt.Sprintf("checkpoint saved at %v", cp["commit"]), nil
		case "ticket_read":
			_, st, err := readSessionTicket(cctx, c)
			if err != nil {
				return "", err
			}
			return sessionTicketText(st), nil
		case "guide":
			section, _ := body["section"].(string)
			return guideToolText(cctx, c, section)
		case "ticket_comment":
			text, _ := body["body"].(string)
			if strings.TrimSpace(text) == "" {
				return "", errors.New("ticket_comment needs a body")
			}
			cm, err := commentOnSessionTicket(cctx, c, text)
			if err != nil {
				return "", err
			}
			return sessionCommentText(cm), nil
		}
	}
	return "", unknownTool(name)
}

// inboxText lists an inbox reply's messages, one a line.
func inboxText(out map[string]any) string {
	msgs, _ := out["messages"].([]any)
	if len(msgs) == 0 {
		return "no messages"
	}
	var b strings.Builder
	for _, x := range msgs {
		m, _ := x.(map[string]any)
		state := "waiting to be typed into your session"
		if m["uncertain"] == true {
			state = "may have been typed before the server ended: read it here"
		}
		if m["deliveredAt"] != nil {
			state = "typed into your session"
		}
		if m["ackedAt"] != nil {
			state = "acknowledged"
		}
		fmt.Fprintf(&b, "#%v from %v (%s): %v\n", m["seq"], m["from"], state, m["text"])
	}
	return strings.TrimRight(b.String(), "\n")
}

// membersText lists a run's members as printMembers does, as text.
func membersText(run map[string]any) string {
	var b strings.Builder
	printMembers(&b, run)
	return strings.TrimRight(b.String(), "\n")
}
