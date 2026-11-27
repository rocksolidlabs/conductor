package cli

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/session"
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
report, set_state, ask, form_crew, add_member, run_status, link and open_url, the skill's
commands as tools, and guide, what the server says this session may do. The server registers it with agents that take an MCP server at
launch; run it by hand only to try it. A Brakeman turn's session (CONDUCTOR_BRAKEMAN
set) lists brakeman_request, observe and brief_submit, and no crew or ticket tool;
it lists act too when the person has granted the turn an operation. A chat's session
(CONDUCTOR_PROTOCOL set) lists permission, which the agent's program calls to ask
the person about a tool call; the model never calls it.
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
	"form_crew": true, "add_member": true, "run_status": true, "link": true, "open_url": true,
	"task_context": true, "task_result": true, "message_send": true, "inbox_read": true, "inbox_ack": true, "task_blocked": true, "task_checkpoint": true,
	"ticket_read": true, "ticket_comment": true,
	"decision_request": true, "decision_list": true, "decision_read": true, "decision_withdraw": true,
	"guide": true, "brakeman_request": true, "observe": true, "brief_submit": true, "act": true,
	"context_lookup": true, permissionTool: true,
}

// brakemanOnly is the tools a Brakeman turn's session alone lists, and
// notForBrakeman the ones it does not (docs/plans/brakeman.md, owner
// decisions 16 and 18): a worker never sees Brakeman's tools, and
// Brakeman forms no crew, adds no member and reaches no ticket, which
// the routes refuse it too (403 brakeman_cannot). act is a turn's only
// when the person granted it an operation (grantedOnly).
var (
	brakemanOnly   = map[string]bool{"brakeman_request": true, "observe": true, "brief_submit": true, "act": true}
	grantedOnly    = map[string]bool{"act": true}
	notForBrakeman = map[string]bool{"form_crew": true, "add_member": true, "ticket_read": true, "ticket_comment": true}
)

// taskOnly is the tools only an attempt's session of a managed run lists
// (CONDUCTOR_TASK set): the decisions, which an attempt asks and waits
// for (docs/plans/decisions.md, internal/cli).
var taskOnly = map[string]bool{"decision_request": true, "decision_list": true, "decision_read": true, "decision_withdraw": true}

// lookupOnly is the tools an attempt's session lists only when its guide
// lists them: context_lookup, when a context provider answers lookups
// (docs/plans/context-seams.md, CT-2).
var lookupOnly = map[string]bool{"context_lookup": true}

// protocolOnly is the tools only a session whose agent runs its protocol
// way (a chat, CONDUCTOR_PROTOCOL set) lists: permission, which the
// agent's own program calls for a permission prompt, never the model
// (docs/plans/chat-or-terminal.md, design 7).
var protocolOnly = map[string]bool{permissionTool: true}

// mcpTools is the tools as tools/list gives them, from the server's table
// of operations: its names, descriptions and schemas are the table's, so
// the tools and the guide cannot drift (internal/api, guide.go).
// brakeman is a Brakeman turn's session (CONDUCTOR_BRAKEMAN set), granted
// that its turn is granted an operation (its request's permissions.act),
// protocol a session on the protocol way (CONDUCTOR_PROTOCOL set).
func mcpTools(brakeman, granted, protocol bool) []mcpTool {
	return mcpToolsFrom(api.Operations(), brakeman, granted, protocol)
}

// mcpToolsFrom is the tools of ops that call has a handler for and the
// session lists, in ops' order.
func mcpToolsFrom(ops []api.Operation, brakeman, granted, protocol bool) []mcpTool {
	var out []mcpTool
	for _, op := range ops {
		if op.MCP != "" && mcpHandled[op.MCP] && listedIn(op.MCP, brakeman, granted, protocol) {
			out = append(out, mcpTool{Name: op.MCP, Description: op.Description, InputSchema: op.Input})
		}
	}
	return out
}

// listedIn reports whether a session lists the tool: permission only on
// the protocol way; a Brakeman turn's all but the crew and ticket tools,
// act only when it is granted an operation; any other all but Brakeman's.
func listedIn(tool string, brakeman, granted, protocol bool) bool {
	if protocolOnly[tool] && !protocol {
		return false
	}
	if brakeman {
		return !notForBrakeman[tool] && (granted || !grantedOnly[tool])
	}
	return !brakemanOnly[tool]
}

// maxMCPCalls bounds the tools/call requests in flight at once: the
// permission requests a session holds open (session.MaxPendingPermissions)
// and four more, so the agent's reports go on while they wait. A call past
// it is answered at once, never queued (docs/plans/chat-or-terminal.md,
// design 7).
const maxMCPCalls = session.MaxPendingPermissions + 4

// callsPastTheBound is what a tool call past maxMCPCalls is answered.
const callsPastTheBound = "Too many tool calls are running at once; call again once one has answered."

// mcpServer serves one agent. A permission call runs on a goroutine of
// its own; every other tools/call is handed, in the order read, to one
// worker, so the server meets them in the order the agent sent them, as
// before; each has a context of its own. Every other message is answered
// in turn by the loop that reads them, a call held or not.
type mcpServer struct {
	out    io.Writer
	getenv func(string) string

	// afterFunc runs f once d has passed, unless the stop it returns is
	// called first; nil is the clock's own (time.AfterFunc). A test hands
	// it a fake clock.
	afterFunc func(d time.Duration, f func()) (stop func() bool)
	// permissionTransport carries the permission tool's posts; nil is
	// http.DefaultTransport.
	permissionTransport http.RoundTripper
	// beforeReply, when set, runs on a call's goroutine once its answer
	// is made and before it is written or dropped: a test's seam.
	beforeReply func(id json.RawMessage)

	// wmu makes one writer of out: a reply is written whole, alone.
	wmu sync.Mutex
	// mu guards calls, the tools/call requests in flight by id.
	mu    sync.Mutex
	calls map[string]*mcpCall
	// running counts the calls' goroutines: raised before one starts,
	// lowered as its last act but the WaitGroup's.
	running atomic.Int64
	wg      sync.WaitGroup
	// queue is the worker's: the calls that are not permission calls, in
	// the order read. Its room is maxMCPCalls, every call in flight, so a
	// call past the bound is refused before it is queued and the loop
	// never waits on it.
	queue chan func()
}

// mcpCall is one tools/call in flight.
type mcpCall struct {
	cancel context.CancelFunc
	// permission is a call of the permission tool: the end of stdin ends it.
	permission bool
	// cancelled is set, under mcpServer.mu, when the client withdrew the
	// call (notifications/cancelled) or its stdin ended: no reply is ever
	// written for it, one made after included.
	cancelled bool
}

func (s *mcpServer) after(d time.Duration, f func()) func() bool {
	if s.afterFunc != nil {
		return s.afterFunc(d, f)
	}
	return time.AfterFunc(d, f).Stop
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

// serve reads messages until stdin ends. It reads on while calls run: a
// permission call is held until a person answers, and the agent's pings,
// reports and cancellations meanwhile are read and answered. At the end
// of stdin every permission call still held is ended with no reply, the
// other calls finish within their own bounds, and serve returns once
// every call's goroutine has.
func (s *mcpServer) serve(ctx context.Context, in io.Reader) (int, error) {
	held, endHeld := context.WithCancel(ctx)
	defer endHeld()
	s.mu.Lock()
	s.calls = map[string]*mcpCall{}
	s.mu.Unlock()
	s.queue = make(chan func(), maxMCPCalls)
	go func() {
		for run := range s.queue {
			run()
		}
	}()
	defer close(s.queue)
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
			// A notification (initialized, …): nothing to answer. A
			// cancellation ends the call it names.
			if req.Method == "notifications/cancelled" {
				s.cancelCall(req.Params)
			}
			continue
		}
		if req.Method == "tools/call" {
			s.startCall(ctx, held, req)
			continue
		}
		result, rpcErr := s.handle(ctx, req)
		s.answer(req.ID, result, rpcErr)
	}
	s.endHeldCalls()
	s.wg.Wait()
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return 1, err
	}
	return 0, nil
}

// answer writes the reply to request id: its result, or its error.
func (s *mcpServer) answer(id json.RawMessage, result any, rpcErr *mcpError) {
	if rpcErr != nil {
		s.reply(mcpResponse{JSONRPC: "2.0", ID: id, Error: rpcErr})
		return
	}
	s.reply(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

// startCall runs one tools/call, a permission call on a goroutine of its
// own and any other on the worker, after those read before it, or answers
// it at once: a call with no name, one whose id is a call still in flight,
// and one past maxMCPCalls. A permission call's context is held's, which
// the end of stdin ends; any other's is the server's, as before.
func (s *mcpServer) startCall(ctx, held context.Context, req mcpRequest) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		s.answer(req.ID, nil, &mcpError{Code: -32602, Message: "tools/call needs a name"})
		return
	}
	permission := p.Name == permissionTool
	key := mcpIDKey(req.ID)
	s.mu.Lock()
	if old := s.calls[key]; old != nil {
		// Two calls under one id: the client can no longer tell their
		// replies apart, so the first is ended with no reply, and the
		// second refused.
		old.cancelled = true
		old.cancel()
		s.mu.Unlock()
		if permission {
			s.answer(req.ID, textResult(permissionDeny(permissionAlreadyAsked)), nil)
			return
		}
		s.answer(req.ID, nil, &mcpError{Code: -32600, Message: "a call with this id is still running"})
		return
	}
	if len(s.calls) >= maxMCPCalls {
		s.mu.Unlock()
		if permission {
			s.answer(req.ID, textResult(permissionDeny(permissionTooMany)), nil)
			return
		}
		s.answer(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": callsPastTheBound}}, "isError": true}, nil)
		return
	}
	parent := ctx
	if permission {
		parent = held
	}
	cctx, cancel := context.WithCancel(parent)
	c := &mcpCall{cancel: cancel, permission: permission}
	s.calls[key] = c
	s.wg.Add(1)
	s.running.Add(1)
	s.mu.Unlock()
	run := func() {
		defer s.wg.Done()
		defer s.running.Add(-1)
		defer cancel()
		result, rpcErr := s.handle(cctx, req)
		if s.beforeReply != nil {
			s.beforeReply(req.ID)
		}
		s.finishCall(key, c, req.ID, result, rpcErr)
	}
	if permission {
		go run()
		return
	}
	s.queue <- run
}

// finishCall frees the call's place and writes its reply, unless the call
// was cancelled: the place is free before the reply can be read, and a
// cancellation read before this point is never followed by a reply.
func (s *mcpServer) finishCall(key string, c *mcpCall, id json.RawMessage, result any, rpcErr *mcpError) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	cancelled := c.cancelled
	if s.calls[key] == c {
		delete(s.calls, key)
	}
	s.mu.Unlock()
	if cancelled {
		return
	}
	s.write(mcpResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}

// cancelCall ends the call a notifications/cancelled names (its
// requestId), which is then answered nothing, as MCP asks. A
// cancellation of a call that has answered, or of none, does nothing.
func (s *mcpServer) cancelCall(params json.RawMessage) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.RequestID) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.calls[mcpIDKey(p.RequestID)]; c != nil {
		c.cancelled = true
		c.cancel()
	}
}

// endHeldCalls ends, with no reply, every permission call in flight: stdin
// has ended, and nobody is left to read an answer.
func (s *mcpServer) endHeldCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.permission {
			c.cancelled = true
			c.cancel()
		}
	}
}

// mcpIDKey is a request id as one string: a JSON string and a number of
// the same text are different ids.
func mcpIDKey(id json.RawMessage) string {
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	var v any
	if d.Decode(&v) == nil {
		switch x := v.(type) {
		case string:
			return "s" + x
		case json.Number:
			return "n" + x.String()
		}
	}
	return "r" + string(id)
}

// reply writes one message under the writer's lock.
func (s *mcpServer) reply(r mcpResponse) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.write(r)
}

// write writes one message, a line, with HTML escaping off (encodeJSON):
// a ticket of '<' would be six times its size otherwise. The caller holds
// wmu.
func (s *mcpServer) write(r mcpResponse) {
	b, _ := encodeJSON(r)
	_, _ = printReply(s.out, b, false)
}

// textResult is a tools/call result of one text block.
func textResult(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
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
			"instructions":    "Conductor shows this session to people in a browser. Report milestones, artifacts and handoffs with report; ask with ask when you need a decision; form a crew with form_crew when the work splits. Open a link meant for the person (an artifact, a pull request, a preview, a sign-in page) with open_url: it opens in the browser this agent's settings name. Call guide to learn what you may do here.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		turn := s.getenv(envBrakeman) != ""
		// The permission tool, to Claude Code's chat alone (CONDUCTOR_PROTOCOL=1):
		// another protocol's child asks in its own stream.
		tools := mcpTools(turn, turn && s.granted(ctx), s.getenv(protocolEnv) == "1")
		if s.getenv("CONDUCTOR_TASK") == "" {
			tools = slices.DeleteFunc(tools, func(t mcpTool) bool { return taskOnly[t.Name] })
		}
		if s.getenv("CONDUCTOR_TASK") == "" || turn || !s.lookupListed(ctx) {
			tools = slices.DeleteFunc(tools, func(t mcpTool) bool { return lookupOnly[t.Name] })
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &mcpError{Code: -32602, Message: "tools/call needs a name"}
		}
		text, err := s.call(ctx, p.Name, p.Arguments)
		if p.Name == permissionTool {
			// The permission tool's every answer is the documented
			// result: an error is never the agent's to read as a
			// verdict, so one is the lost request's deny.
			if err != nil {
				text = permissionDeny(permissionLost)
			}
			return textResult(text), nil
		}
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

// granted reports whether the Brakeman turn this session is has been
// granted an operation: its guide lists one under the scope act (the
// snapshot and the configuration now together, as the server answers
// them). A guide that cannot be read lists no act: the act routes refuse
// it as ever.
func (s *mcpServer) granted(ctx context.Context) bool {
	c, err := brakemanClientFromEnv(s.getenv)
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	acts, err := c.grants(cctx)
	return err == nil && len(acts) > 0
}

type unknownTool string

func (u unknownTool) Error() string { return "unknown tool " + string(u) }

// askedWords is what the ask tool answers: where the answer will come
// from, true in a terminal and in a chat alike.
const askedWords = "asked: the person's answer comes as their next message to you"

var errNotInSession = errors.New("not running inside a Conductor session: nothing to report to")

// call runs one tool; what it returns is the text the agent reads.
func (s *mcpServer) call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	switch name {
	case permissionTool:
		return s.permission(ctx, raw)
	case "report", "set_state", "ask":
		target, err := notify.TargetFromEnv(s.getenv)
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
		if err := target.Send(sctx, req); err != nil {
			return "", err
		}
		if name == "ask" {
			return askedWords, nil
		}
		return "reported", nil
	case "brakeman_request", "observe", "brief_submit", "act":
		c, err := brakemanClientFromEnv(s.getenv)
		if err != nil {
			return "", err
		}
		// An act may be a launch, which the server bounds by its own
		// launch timeout: the tool waits for every send act makes of it.
		wait := 60 * time.Second
		if name == "act" {
			wait = actBudget()
		}
		cctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		return c.tool(cctx, name, raw)
	case "form_crew", "add_member", "run_status", "link", "open_url", "task_context", "task_result", "message_send", "inbox_read", "inbox_ack", "task_blocked", "task_checkpoint",
		"ticket_read", "ticket_comment", "guide", "decision_request", "decision_list", "decision_read", "decision_withdraw", "context_lookup":
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
		if strings.HasPrefix(name, "decision_") {
			c.prefix = "decision"
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
			return fmt.Sprintf("%s %v; you are %v\n%s", runHead(run), run["name"], out["member"], membersText(run)), nil
		case "link":
			out, err := c.call(cctx, http.MethodPost, "/links/agent", body)
			if err != nil {
				return "", err
			}
			return fmt.Sprint(out["url"]), nil
		case "open_url":
			link, _ := body["url"].(string)
			return openLink(cctx, c, link)
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
		case "decision_request", "decision_list", "decision_read", "decision_withdraw":
			return decisionTool(cctx, c, name, body)
		case "context_lookup":
			c.prefix = "context_lookup"
			return lookup(cctx, c, body)
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

// decisionTool runs one of an attempt's decision tools, each one call to
// its route (docs/plans/decisions.md, internal/cli): a request with its
// id minted when it names none, so a reply lost is asked for once more
// with it, and replied to at once with the decision's id, its state and
// what to do while it waits.
func decisionTool(ctx context.Context, c *crewClient, name string, body map[string]any) (string, error) {
	id, _ := body["id"].(string)
	if name == "decision_read" || name == "decision_withdraw" {
		if _, ok := ledger.ParseDecisionID(id); !ok {
			return "", fmt.Errorf("%s needs the decision's id, dr-N", name)
		}
	}
	var out map[string]any
	var err error
	switch name {
	case "decision_request":
		if r, _ := body["requestId"].(string); r == "" {
			body["requestId"] = newOpID()
		}
		err = askTwice(ctx, func(ctx context.Context) error { return c.into(ctx, http.MethodPost, "/decisions", body, &out) })
	case "decision_list":
		q := url.Values{}
		if a, _ := body["after"].(string); a != "" {
			q.Set("after", a)
		}
		if l, ok := body["limit"].(float64); ok && l >= 1 {
			q.Set("limit", strconv.Itoa(int(l)))
		}
		path := "/decisions"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		out, err = c.call(ctx, http.MethodGet, path, nil)
	case "decision_read":
		out, err = c.call(ctx, http.MethodGet, "/decisions/"+id, nil)
	case "decision_withdraw":
		out, err = c.call(ctx, http.MethodPost, "/decisions/"+id+"/withdraw", map[string]any{})
	}
	if err != nil {
		return "", err
	}
	if name == "decision_list" || name == "decision_read" {
		b, _ := json.Marshal(out) // decoded JSON: it marshals, or jsonText refuses nil
		return jsonText(b, true)
	}
	d, _ := out["decision"].(map[string]any)
	if name == "decision_withdraw" {
		return withdrawnWords(d["id"], d["state"]), nil
	}
	return decisionRequestText(d), nil
}

// decisionRequestText is a request's reply in words for the decision's
// state: a repeat of a requestId (a reply lost, asked again) answers the
// decision as it is now, which may be answered, applied or closed already.
// Only an applied answer is the agent's to act on: an answered one is
// recorded, not yet validated and applied by Yardmaster, and may still
// be superseded instead (#637, review round 2).
func decisionRequestText(d map[string]any) string {
	id, state := d["id"], d["state"]
	answer, _ := d["answer"].(string)
	why, _ := d["why"].(string)
	switch state {
	case ledger.DecisionApplied:
		return fmt.Sprintf("%v is applied: %s. Go on with your task with this answer: it was typed into this session as 'Decision %v … answered by …', "+
			"or written into your task's next attempt's task file.", id, cmp.Or(answer, "see decision_read"), id)
	case ledger.DecisionAnswered:
		return fmt.Sprintf("%v is answered: %s. The answer is recorded, not yet applied: Yardmaster validates it first, and it may still be superseded or cancelled instead. "+
			"Do not act on it yet: end your turn, or go on with work that does not depend on it; do not submit your result until it is applied. "+
			"Once applied it is typed into this session as 'Decision %v … answered by …', or, if this session has ended, written into your task's next attempt's task file.",
			id, cmp.Or(answer, "see decision_read"), id)
	case ledger.DecisionSuperseded:
		if by, _ := d["supersededBy"].(string); by != "" {
			return fmt.Sprintf("%v is superseded by %s: %s. Wait for %s's answer, not this one's.", id, by, cmp.Or(why, "a newer request takes its place"), by)
		}
		return fmt.Sprintf("%v is superseded: %s. It will not be answered.", id, cmp.Or(why, "it was replaced"))
	case ledger.DecisionCancelled, ledger.DecisionExpired:
		return fmt.Sprintf("%v is %v: %s. It will not be answered: go on without it, or ask again with a new requestId if the work still needs it.", id, state, cmp.Or(why, "it is closed"))
	}
	return fmt.Sprintf("%v %v. Pending. End your turn, or go on with work that does not depend on it; do not submit your result until its answer is applied. "+
		"Once Yardmaster has applied the answer it is typed into this session as 'Decision %v … answered by …', or, if this session has ended, written into your task's next attempt's task file, "+
		"which starts at your last checkpoint: save one before you end your turn.", id, state, id)
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

// runHead is a session's run by its id and, when the reply gives one, its
// state: a comparison's lane is told its own tasks' states alone.
func runHead(run map[string]any) string {
	if state, ok := run["state"]; ok {
		return fmt.Sprintf("run %v (%v)", run["id"], state)
	}
	return fmt.Sprintf("run %v", run["id"])
}

// membersText lists a run's members as printMembers does, as text.
func membersText(run map[string]any) string {
	var b strings.Builder
	printMembers(&b, run)
	return strings.TrimRight(b.String(), "\n")
}
