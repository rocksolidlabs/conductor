package agentproto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// ACPv1 is the Agent Client Protocol, version 1, on stdio: newline-delimited
// JSON-RPC 2.0 (https://agentclientprotocol.com/protocol/v1/transports.md).
const ACPv1 = "acp-v1"

// acpProtocolVersion is the one protocol version the client speaks: "This
// version is only incremented when breaking changes are introduced".
const acpProtocolVersion = 1

// acpVerbatim is the Agent Client Protocol's Verbatim: slash commands "are
// run as part of regular prompt requests where the Client includes the
// command text in the prompt", and the protocol has no as-written mark.
const acpVerbatim = false

// acpMoves are the agents (by their adapter) whose sessions move between
// their terminal and their chat: only where the agent's session id through
// the protocol is shown, by hand, to be its terminal's own. None yet.
var acpMoves = map[string]bool{}

// Bounds of the ACP client.
const (
	// ACPInitWait bounds Begin: the initialize answer and the session's
	// load or resume together, a load replaying the session's history.
	ACPInitWait = 30 * time.Second
	// maxACPRequests bounds the agent's permission requests the client keeps
	// until they are answered or taken back.
	maxACPRequests = 256
	// maxACPCalls bounds the client's own requests waiting for answers.
	maxACPCalls = 64
)

// The words of the ACP client's answers (design 13, for an agent of the
// Agent Client Protocol).
const (
	ACPNotQualifiedWords  = "This version of the agent can only run in its terminal."
	ACPNotStartedWords    = "This agent did not start a chat."
	ACPNotSignedInWords   = "This agent is not signed in; sign in from its terminal."
	ACPUnreadableWords    = "The agent sent something Conductor cannot read; the session ended."
	ACPNoConversationWord = "This agent's chat cannot take up its terminal's conversation."
	// ACPNoAllowOnceWords is the notice of a request that offered no way to
	// allow the call once: declined, never allowed for longer.
	ACPNoAllowOnceWords = "The agent offered no way to allow this call once; Conductor declined it."
	// ACPDeclinedWords is the notice of a request about more than the
	// client can show: declined.
	ACPDeclinedWords = "The agent asked about something a chat cannot show here; Conductor declined it."
)

var (
	// ErrACPNotQualified refuses a chat of an agent whose version the
	// identity probe did not read.
	ErrACPNotQualified error = &words{s: ACPNotQualifiedWords, kind: kindNotQualified}
	// ErrACPNotStarted is Begin's answer for an initialize answer that is
	// missing, of another protocol version, or a session not taken up.
	ErrACPNotStarted error = &words{s: ACPNotStartedWords, kind: kindNotStarted}
	// ErrACPNotSignedIn is Begin's or Read's answer when the agent says it
	// needs a sign-in: the session goes back to its terminal.
	ErrACPNotSignedIn error = &words{s: ACPNotSignedInWords, kind: kindNotSignedIn}
	// ErrACPUnreadable is Read's answer for a line that is not a JSON object.
	ErrACPUnreadable error = &words{s: ACPUnreadableWords, code: "unreadable"}
)

// acpAuthRequired is the JSON-RPC error code an agent answers with when it
// needs a sign-in first (the protocol's auth_required).
const acpAuthRequired = -32000

// ACPOptions are an ACP client's.
type ACPOptions struct {
	// Session is the agent's session to take up (its terminal's
	// conversation), and Cwd the session's folder (absolute).
	Session string
	Cwd     string
	// InitWait bounds Begin, ACPInitWait when 0 (a test's knob).
	InitWait time.Duration
	// RawInputKeys are the keys of a call's raw input the client was taught
	// for this agent (ACPRawInputKeys, recorded at the agent's own look): a
	// call whose raw input holds any other key, or is not an object, is one
	// the client does not read.
	RawInputKeys []string
}

// acpRawInputKeys are the raw input's keys each agent's own look recorded,
// by its adapter. None yet: until an agent's look, every call of it with a
// raw input is declined.
var acpRawInputKeys = map[string][]string{}

// ACPRawInputKeys are the raw input's keys taught for an agent's adapter.
func ACPRawInputKeys(adapter string) []string { return acpRawInputKeys[adapter] }

// ACP is the client of an agent's Agent Client Protocol on stdio
// (session.Protocol): protocol 1, the baseline alone. It advertises no file
// system and no terminal capability, so every fs/* and terminal/* request is
// answered method-not-found; it takes up the terminal's session with
// session/resume where the agent advertises it, else session/load, and
// refuses a chat otherwise; it sends each message as session/prompt with
// one text block, and session/cancel to stop a turn, answering every pending
// permission request cancelled. A permission request is the session's
// question, bound to the call the stream announced; an Allow selects the
// agent's allow_once option by its own id, a Deny its reject_once option or
// cancelled, and an always option is never selected. It names itself
// conductor and passes no credential.
type ACP struct {
	in       io.WriteCloser
	out      io.Reader
	session  string
	cwd      string
	initWait time.Duration
	keys     []string

	wmu    sync.Mutex
	cmu    sync.Mutex
	closed bool

	mu     sync.Mutex
	nextID int64
	calls  map[string]*acpCall
	begun  bool
	// sessionID is the session taken up; loading says a session/load is
	// under way (its replayed updates are history, not this chat's).
	sessionID string
	loading   bool
	// prompt is the session/prompt under way ("" for none) and its ref;
	// told says the session was told the message was taken.
	prompt     string
	promptRef  string
	promptTold bool
	// requests are the agent's permission requests handed to the session,
	// by the ref the session answers under; byID the same by their own id.
	requests map[string]*acpRequest
	byID     map[string]string
	reqSeq   int

	readDone chan struct{}
	readOnce sync.Once

	// What only Read's goroutine touches.
	r acpRead
}

type acpCall struct {
	method string
	answer chan codexAnswer
}

type acpRequest struct {
	id      json.RawMessage
	options []acpOption
	// call is the tool call the request is about.
	call string
}

type acpOption struct{ ID, Kind string }

type acpRead struct {
	answered bool
	turns    int
	streamed map[string]bool
	tools    map[string]acpTool
	toolSeq  []string
}

type acpTool struct {
	name  string
	input []byte
	// fields are the call's fields as announced, by name; unread says the
	// call held one the client does not read, changed that an update
	// changed one after it was announced: a request about it is declined.
	fields  map[string]json.RawMessage
	unread  bool
	changed bool
}

// NewACP is the client of an agent whose input is in and output out.
func NewACP(in io.WriteCloser, out io.Reader, opts ACPOptions) *ACP {
	wait := opts.InitWait
	if wait <= 0 {
		wait = ACPInitWait
	}
	return &ACP{
		in: in, out: out, session: opts.Session, cwd: opts.Cwd, initWait: wait, keys: opts.RawInputKeys,
		calls: map[string]*acpCall{}, requests: map[string]*acpRequest{}, byID: map[string]string{},
		readDone: make(chan struct{}),
		r:        acpRead{streamed: map[string]bool{}, tools: map[string]acpTool{}},
	}
}

var _ session.Protocol = (*ACP)(nil)

// Name is ACPv1.
func (c *ACP) Name() string { return ACPv1 }

// Verbatim is acpVerbatim.
func (c *ACP) Verbatim() bool { return acpVerbatim }

// acpClientCapabilities are the client's: the baseline, no file system and
// no terminal.
func acpClientCapabilities() map[string]any {
	return map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false}
}

// Begin writes initialize (protocol 1, the baseline's capabilities,
// clientInfo conductor), checks the answer is of protocol 1, and takes up
// the terminal's session: session/resume where the agent advertises it,
// else session/load where it advertises loadSession, its replayed history
// not shown; an agent that offers neither starts no chat. A session's
// answer auth_required is ErrACPNotSignedIn. It returns by ctx's end,
// InitWait's, or the output's, at the latest.
func (c *ACP) Begin(ctx context.Context) error {
	c.mu.Lock()
	if c.begun {
		c.mu.Unlock()
		return errors.New("agentproto: Begin was called already")
	}
	c.begun = true
	c.mu.Unlock()
	if !acpSessionShape(c.session) {
		return fmt.Errorf("%w (no session to take up)", ErrACPNotStarted)
	}
	ctx, cancel := context.WithTimeout(ctx, c.initWait)
	defer cancel()
	init, aerr, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion":    acpProtocolVersion,
		"clientCapabilities": acpClientCapabilities(),
		"clientInfo":         map[string]any{"name": "conductor", "title": "Conductor", "version": version.Version},
	})
	if err != nil {
		return err
	}
	if aerr != nil {
		return fmt.Errorf("%w (initialize was refused: %s)", ErrACPNotStarted, cutWords(aerr.Message))
	}
	m := object(init)
	var pv json.Number
	field(m, "protocolVersion", &pv)
	if pv.String() != strconv.Itoa(acpProtocolVersion) {
		return fmt.Errorf("%w (its protocol version is %q, not %d)", ErrACPNotStarted, cutWords(pv.String()), acpProtocolVersion)
	}
	caps := object(m["agentCapabilities"])
	var load bool
	field(caps, "loadSession", &load)
	resume := !isNull(get(object(caps["sessionCapabilities"]), "resume"))
	params := map[string]any{"sessionId": c.session, "cwd": c.cwd, "mcpServers": []any{}}
	method := ""
	switch {
	case resume:
		method = "session/resume"
	case load:
		method = "session/load"
		c.mu.Lock()
		c.loading = true
		c.mu.Unlock()
	default:
		return fmt.Errorf("%w (%s)", ErrACPNotStarted, ACPNoConversationWord)
	}
	_, aerr, err = c.call(ctx, method, params)
	c.mu.Lock()
	c.loading = false
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if aerr != nil {
		if aerr.Code == acpAuthRequired && strings.Contains(strings.ToLower(aerr.Message), "auth") {
			return fmt.Errorf("%w (%s)", ErrACPNotSignedIn, cutWords(aerr.Message))
		}
		return fmt.Errorf("%w (%s was refused: %s)", ErrACPNotStarted, method, cutWords(aerr.Message))
	}
	c.mu.Lock()
	c.sessionID = c.session
	c.mu.Unlock()
	return nil
}

// call writes the request and waits for its answer under ctx: the result,
// or the agent's error, or ErrACPNotStarted with why.
func (c *ACP) call(ctx context.Context, method string, params any) (json.RawMessage, *codexError, error) {
	id, answer, err := c.request(method, params)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (%s was not written: %v)", ErrACPNotStarted, method, err)
	}
	select {
	case a := <-answer:
		return a.result, a.err, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, nil, fmt.Errorf("%w (no answer to %s: %v)", ErrACPNotStarted, method, ctx.Err())
	case <-c.readDone:
		select {
		case a := <-answer:
			return a.result, a.err, nil
		default:
		}
		return nil, nil, fmt.Errorf("%w (its output ended before it answered %s)", ErrACPNotStarted, method)
	}
}

func (c *ACP) request(method string, params any) (string, chan codexAnswer, error) {
	c.mu.Lock()
	if len(c.calls) >= maxACPCalls {
		c.mu.Unlock()
		return "", nil, errors.New("too many requests wait for their answers")
	}
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	call := &acpCall{method: method, answer: make(chan codexAnswer, 1)}
	c.calls[id] = call
	c.mu.Unlock()
	if err := c.writeJSON(map[string]any{"jsonrpc": "2.0", "id": json.Number(id), "method": method, "params": params}); err != nil {
		c.forget(id)
		return "", nil, err
	}
	return id, call.answer, nil
}

func (c *ACP) forget(id string) {
	c.mu.Lock()
	delete(c.calls, id)
	c.mu.Unlock()
}

// Send writes m as session/prompt with one text block, the text as written.
// The agent's first sign of the turn (an update of the session's, or the
// prompt's answer) is the message taken.
func (c *ACP) Send(_ context.Context, m session.Message) error {
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid == "" {
		return errors.New("agentproto: no session is taken up")
	}
	c.mu.Lock()
	c.promptRef, c.promptTold = m.Ref, false
	c.mu.Unlock()
	id, _, err := c.request("session/prompt", map[string]any{"sessionId": sid, "prompt": []map[string]any{{"type": "text", "text": m.Text}}})
	c.mu.Lock()
	if err == nil {
		c.prompt = id
	} else {
		c.promptRef = ""
	}
	c.mu.Unlock()
	return err
}

// Interrupt sends session/cancel and, as the protocol requires, answers
// every pending permission request cancelled; the turn's end arrives as the
// prompt's answer, stopReason cancelled.
func (c *ACP) Interrupt(context.Context) error {
	c.mu.Lock()
	sid := c.sessionID
	pending := make([]*acpRequest, 0, len(c.requests))
	for ref, r := range c.requests {
		pending = append(pending, r)
		delete(c.requests, ref)
		delete(c.byID, idKey(r.id))
	}
	c.mu.Unlock()
	if err := c.writeJSON(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": sid}}); err != nil {
		return err
	}
	for _, r := range pending {
		_ = c.answerOutcome(r.id, acpCancelled())
	}
	return nil
}

func acpCancelled() map[string]any { return map[string]any{"outcome": "cancelled"} }

func (c *ACP) answerOutcome(id json.RawMessage, outcome map[string]any) error {
	return c.writeJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"outcome": outcome}})
}

// Reply answers the permission request ref: an Allow selects the offered
// allow_once option by its own id; a Deny the reject_once option, or
// cancelled where none is offered; a withdrawal cancelled. An always option
// is never selected. A request already answered or taken back is answered
// with nothing.
func (c *ACP) Reply(_ context.Context, ref string, v session.Verdict) error {
	c.mu.Lock()
	r := c.requests[ref]
	if r != nil {
		delete(c.requests, ref)
		delete(c.byID, idKey(r.id))
	}
	c.mu.Unlock()
	if r == nil {
		// Answered, taken back or cancelled already: the verdict reaches no
		// request.
		return ErrNoRequest
	}
	return c.answerOutcome(r.id, acpOutcome(r.options, v))
}

// acpOutcome is the outcome a verdict selects among the options offered.
func acpOutcome(options []acpOption, v session.Verdict) map[string]any {
	if v.Cancel {
		return acpCancelled()
	}
	want := "reject_once"
	if v.Allow {
		want = "allow_once"
	}
	for _, o := range options {
		if o.Kind == want {
			return map[string]any{"outcome": "selected", "optionId": o.ID}
		}
	}
	return acpCancelled()
}

// CloseInput ends the child's input; nothing is written after it.
func (c *ACP) CloseInput() error {
	c.cmu.Lock()
	if c.closed {
		c.cmu.Unlock()
		return nil
	}
	c.closed = true
	c.cmu.Unlock()
	return c.in.Close()
}

func (c *ACP) writeJSON(v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.cmu.Lock()
	closed := c.closed
	c.cmu.Unlock()
	if closed {
		return errInputClosed
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

// Read reads the agent's output a line at a time and hands the session each
// event in order. It returns io.EOF at the output's end, ctx's error once
// ctx ends, and ErrACPUnreadable for a line that is not a JSON object. A
// line past session.MaxProtocolLine is skipped with a notice. Unknown
// methods, updates and fields change nothing, and an update of another
// session, or replayed by a load, is not this chat's.
func (c *ACP) Read(ctx context.Context, e session.ProtocolEvents) error {
	defer c.readOnce.Do(func() { close(c.readDone) })
	lines := make(chan line)
	quit := make(chan struct{})
	defer close(quit)
	go readLines(c.out, lines, quit)
	for {
		var l line
		select {
		case l = <-lines:
		case <-ctx.Done():
			return ctx.Err()
		}
		if l.err != nil {
			return l.err
		}
		if l.cut {
			// A line the client could not read may have changed any call:
			// every open call is changed, and every open request about one
			// is withdrawn.
			e.Notice(NoticeTooLarge, TooLargeWords)
			for id, t := range c.r.tools {
				t.changed = true
				c.r.tools[id] = t
				c.withdrawAbout(id, e)
			}
			continue
		}
		if len(bytes.TrimSpace(l.b)) == 0 {
			continue
		}
		if err := c.handle(l.b, e); err != nil {
			return err
		}
	}
}

func (c *ACP) handle(b []byte, e session.ProtocolEvents) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		if err == nil {
			err = errors.New("not an object")
		}
		return fmt.Errorf("%w (%v)", ErrACPUnreadable, err)
	}
	var method string
	field(m, "method", &method)
	id, hasID := m["id"]
	hasID = hasID && validID(id)
	params := object(m["params"])
	switch {
	case method != "" && hasID:
		c.agentRequest(method, id, params, e)
	case method != "":
		c.notification(method, params, e)
	case hasID:
		c.answer(id, m, e)
	}
	return nil
}

func (c *ACP) answer(id json.RawMessage, m map[string]json.RawMessage, e session.ProtocolEvents) {
	var key string
	var n json.Number
	if json.Unmarshal(id, &n) == nil {
		key = n.String()
	} else {
		_ = json.Unmarshal(id, &key)
	}
	c.mu.Lock()
	call := c.calls[key]
	delete(c.calls, key)
	c.mu.Unlock()
	if call == nil {
		return
	}
	a := codexAnswer{result: m["result"]}
	if raw, ok := m["error"]; ok && !isNull(raw) {
		a.err = &codexError{}
		field(object(raw), "message", &a.err.Message)
		field(object(raw), "code", &a.err.Code)
	}
	switch call.method {
	case "session/resume", "session/load":
		if a.err == nil {
			e.Conversation(c.session)
		}
	case "session/prompt":
		c.promptEnded(a, e)
	}
	call.answer <- a
}

// promptEnded is the prompt's answer: the turn's end by its stopReason.
func (c *ACP) promptEnded(a codexAnswer, e session.ProtocolEvents) {
	c.mu.Lock()
	ref, told := c.promptRef, c.promptTold
	c.prompt, c.promptRef, c.promptTold = "", "", false
	c.mu.Unlock()
	if a.err != nil && !told {
		if a.err.Code == acpAuthRequired && !c.r.answered {
			e.Notice(NoticeNotSignedIn, ACPNotSignedInWords)
		}
		e.Notice(NoticeNotTaken, "The agent did not take the message.")
		return
	}
	if !told && ref != "" {
		e.Accepted(ref)
		c.startTurn(e)
	}
	for ref := range c.r.streamed {
		e.Text(ref, "", true)
	}
	clear(c.r.streamed)
	var stop string
	field(object(a.result), "stopReason", &stop)
	outcome := "error"
	switch {
	case a.err != nil:
		e.Notice(NoticeAgentError, cutWords(a.err.Message))
	case stop == "end_turn":
		outcome = "done"
	case stop == "cancelled":
		outcome = "interrupted"
	case stop == "max_tokens" || stop == "max_turn_requests":
		outcome = "limit"
	}
	if outcome == "done" {
		c.r.answered = true
	}
	e.TurnEnded(outcome, session.Usage{})
}

// taken tells the session the message under way was taken, at the agent's
// first sign of its turn.
func (c *ACP) taken(e session.ProtocolEvents) {
	c.mu.Lock()
	ref, told := c.promptRef, c.promptTold
	if ref != "" {
		c.promptTold = true
	}
	c.mu.Unlock()
	if ref != "" && !told {
		e.Accepted(ref)
		c.startTurn(e)
	}
}

func (c *ACP) startTurn(e session.ProtocolEvents) {
	c.r.turns++
	e.TurnStarted("")
}

func (c *ACP) ofSession(p map[string]json.RawMessage) bool {
	var sid string
	field(p, "sessionId", &sid)
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.loading && sid != "" && sid == c.sessionID
}

func (c *ACP) notification(method string, p map[string]json.RawMessage, e session.ProtocolEvents) {
	switch method {
	case "session/update":
		if !c.ofSession(p) {
			return
		}
		c.mu.Lock()
		inTurn := c.promptRef != ""
		c.mu.Unlock()
		if !inTurn {
			// The agent's own words between turns are no chat's turn.
			return
		}
		c.taken(e)
		c.update(object(p["update"]), e)
	case "$/cancel_request":
		key := idKey(p["requestId"])
		c.mu.Lock()
		ref, open := c.byID[key]
		var r *acpRequest
		if open {
			r = c.requests[ref]
			delete(c.byID, key)
			delete(c.requests, ref)
		}
		c.mu.Unlock()
		if open {
			// The request is answered still, as the protocol asks: the
			// cancelled request's error.
			_ = c.writeJSON(map[string]any{"jsonrpc": "2.0", "id": r.id, "error": map[string]any{"code": -32800, "message": "Request cancelled"}})
			e.PermissionWithdrawn(ref)
		}
	}
}

// update is one session/update of the turn.
func (c *ACP) update(u map[string]json.RawMessage, e session.ProtocolEvents) {
	var kind string
	field(u, "sessionUpdate", &kind)
	switch kind {
	case "agent_message_chunk":
		var t, text, msg string
		content := object(u["content"])
		field(content, "type", &t)
		field(content, "text", &text)
		field(u, "messageId", &msg)
		if t != "text" || text == "" {
			return
		}
		ref := msg
		if ref == "" {
			ref = "acp-text-" + strconv.Itoa(c.r.turns)
		}
		if !c.r.streamed[ref] && len(c.r.streamed) >= maxStreamed {
			return
		}
		c.r.streamed[ref] = true
		e.Text(ref, text, false)
	case "tool_call":
		var id string
		field(u, "toolCallId", &id)
		if id == "" {
			return
		}
		if t, open := c.r.tools[id]; open {
			// Announced again: a call that is no longer as it was shown.
			c.changeOf(id, t, u, e)
			c.toolStatus(id, u, e)
			return
		}
		name, input, summary, fields, unread := acpStep(u, c.keys)
		c.announce(id, acpTool{name: name, input: input, fields: fields, unread: unread}, summary, e)
		c.toolStatus(id, u, e)
	case "tool_call_update":
		var id string
		field(u, "toolCallId", &id)
		if id == "" {
			return
		}
		t, open := c.r.tools[id]
		if !open {
			return
		}
		c.changeOf(id, t, u, e)
		c.toolStatus(id, u, e)
	}
}

// The fields of a tool call the client reads (protocol 1's ToolCall and
// ToolCallUpdate): its input is every one of them it was given but its id
// and its status, whole; a call holding any other, not null, is one the
// client cannot show whole.
var (
	acpShownFields  = []string{"name", "title", "kind", "rawInput", "content", "locations"}
	acpUpdateFields = []string{"toolCallId", "status", "rawOutput", "sessionUpdate"}
)

// changeOf is an update or a repeated announcement of the open call id: one
// that gives a shown field another value, or holds a field the client does
// not read, changes the call (a later request about it is declined) and
// withdraws every open request about it; one that sets it running withdraws
// them too, the call no longer waiting for the answer.
func (c *ACP) changeOf(id string, t acpTool, u map[string]json.RawMessage, e session.ProtocolEvents) {
	_, _, _, _, unread := acpStep(u, c.keys)
	changed := acpChanges(t.fields, u) || unread
	if changed {
		t.changed = true
		c.r.tools[id] = t
	}
	var status string
	field(u, "status", &status)
	if changed || (status != "" && status != "pending") {
		c.withdrawAbout(id, e)
	}
}

// withdrawAbout withdraws every open request about the call id: the agent
// is answered cancelled, and the person's card closes.
func (c *ACP) withdrawAbout(id string, e session.ProtocolEvents) {
	c.mu.Lock()
	var gone []string
	var ids []json.RawMessage
	for ref, r := range c.requests {
		if r.call == id {
			gone = append(gone, ref)
			ids = append(ids, r.id)
			delete(c.requests, ref)
			delete(c.byID, idKey(r.id))
		}
	}
	c.mu.Unlock()
	for i, ref := range gone {
		_ = c.answerOutcome(ids[i], acpCancelled())
		e.PermissionWithdrawn(ref)
	}
}

// acpChanges says an update gives one of the shown fields a value other
// than the one announced.
func acpChanges(was, u map[string]json.RawMessage) bool {
	for _, k := range acpShownFields {
		raw, ok := u[k]
		if !ok || isNull(raw) {
			continue
		}
		if !jsonEqual(was[k], raw) {
			return true
		}
	}
	return false
}

// jsonEqual says a and b are the same JSON value.
func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ab, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(ab, bb)
}

// acpStep is a tool call's step: its name (the agent's name for the tool,
// else its kind), its raw input ({} where it gives none) and its title as
// the one line.
func acpStep(u map[string]json.RawMessage, keys []string) (name string, input []byte, summary string, fields map[string]json.RawMessage, unread bool) {
	var title, kind string
	field(u, "name", &name)
	field(u, "kind", &kind)
	field(u, "title", &title)
	if name == "" {
		name = kind
	}
	if name == "" {
		name = "other"
	}
	fields = map[string]json.RawMessage{}
	for _, k := range acpShownFields {
		if raw, ok := u[k]; ok && !isNull(raw) {
			fields[k] = bytes.Clone(raw)
		}
	}
	input, ok := acpInput(fields)
	unread = !ok || extraFields(u, append(append([]string{}, acpShownFields...), acpUpdateFields...)) || !rawInputRead(fields["rawInput"], keys)
	return name, input, title, fields, unread
}

// rawInputRead says a call's raw input is one the client reads: none, or an
// object whose every key the client was taught for its agent.
func rawInputRead(raw json.RawMessage, keys []string) bool {
	if raw == nil {
		return true
	}
	m := object(raw)
	if m == nil {
		return false
	}
	for k := range m {
		if !contains(keys, k) {
			return false
		}
	}
	return true
}

// acpInput is a call's input as the session keeps it and the person is
// asked about: every shown field it holds, whole, from the agent's own bytes
// (keys sorted, each value as the agent wrote it, its insignificant space
// taken out); false when a field is not JSON.
func acpInput(fields map[string]json.RawMessage) ([]byte, bool) {
	keys := slices.Sorted(maps.Keys(fields))
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(k)
		b.Write(name)
		b.WriteByte(':')
		if err := json.Compact(&b, fields[k]); err != nil {
			return []byte("{}"), false
		}
	}
	b.WriteByte('}')
	return b.Bytes(), true
}

func (c *ACP) announce(id string, t acpTool, summary string, e session.ProtocolEvents) {
	r := &c.r
	name, input := t.name, t.input
	if _, open := r.tools[id]; open {
		return
	}
	r.toolSeq = append(r.toolSeq, id)
	if len(r.toolSeq) > maxOpenTools {
		delete(r.tools, r.toolSeq[0])
		r.toolSeq = r.toolSeq[1:]
	}
	r.tools[id] = t
	e.ToolStarted(session.ToolStep{Ref: id, Name: name, Input: bytes.Clone(input), Summary: summary})
}

// toolStatus ends a step whose status is completed or failed: its output
// the text of its content, its diffs the files it changed.
func (c *ACP) toolStatus(id string, u map[string]json.RawMessage, e session.ProtocolEvents) {
	var status string
	field(u, "status", &status)
	if status != "completed" && status != "failed" {
		return
	}
	delete(c.r.tools, id)
	res := session.ToolResult{Status: session.ToolOK}
	if status == "failed" {
		res.Status = session.ToolError
	}
	var content []json.RawMessage
	field(u, "content", &content)
	var out strings.Builder
	var files []session.FileChange
	for _, raw := range content {
		item := object(raw)
		var t string
		field(item, "type", &t)
		switch t {
		case "content":
			var text string
			field(object(item["content"]), "text", &text)
			if text != "" {
				if out.Len() > 0 {
					out.WriteByte('\n')
				}
				out.WriteString(text)
			}
		case "diff":
			if f, ok := acpFileChange(id, item); ok {
				files = append(files, f)
			}
		}
	}
	res.Output = []byte(out.String())
	e.ToolEnded(id, res)
	if res.Status == session.ToolOK {
		for _, f := range files {
			e.FileChanged(f)
		}
	}
}

// acpFileChange is a diff a step's content gives: its path, create where it
// had no old text, the lines its new text adds and its old text removes
// beyond what the two share at their start and end, and the change as a
// diff of those lines.
func acpFileChange(tool string, item map[string]json.RawMessage) (session.FileChange, bool) {
	var path, newText string
	var oldText *string
	field(item, "path", &path)
	field(item, "newText", &newText)
	field(item, "oldText", &oldText)
	if path == "" {
		return session.FileChange{}, false
	}
	f := session.FileChange{Tool: tool, Path: path, Op: session.ChangeEdit}
	old := ""
	if oldText == nil {
		f.Op = session.ChangeCreate
	} else {
		old = *oldText
	}
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	a, b := split(old), split(newText)
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	j := 0
	for j < len(a)-i && j < len(b)-i && a[len(a)-1-j] == b[len(b)-1-j] {
		j++
	}
	var diff strings.Builder
	for _, l := range a[i : len(a)-j] {
		diff.WriteString("-" + l + "\n")
		f.Removed++
	}
	for _, l := range b[i : len(b)-j] {
		diff.WriteString("+" + l + "\n")
		f.Added++
	}
	if diff.Len() > 0 {
		f.Diff = []byte(diff.String())
	}
	return f, true
}

// agentRequest is a request the agent makes of the client: a permission
// request is the session's question; every other (fs/*, terminal/*, any
// method the client did not advertise) is answered method-not-found.
func (c *ACP) agentRequest(method string, id json.RawMessage, p map[string]json.RawMessage, e session.ProtocolEvents) {
	if method != "session/request_permission" {
		_ = c.writeJSON(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "Method not found"}})
		return
	}
	if !c.ofSession(p) {
		_ = c.answerOutcome(id, acpCancelled())
		return
	}
	if extraFields(p, []string{"sessionId", "toolCall", "options"}) {
		// A request asking for more than the client reads: declined.
		_ = c.answerOutcome(id, acpCancelled())
		e.Notice(NoticeDeclined, ACPDeclinedWords)
		return
	}
	options, read := acpOptions(p["options"])
	if !read {
		// Options the client does not read whole: declined, never put to a
		// person.
		_ = c.answerOutcome(id, acpCancelled())
		e.Notice(NoticeDeclined, ACPDeclinedWords)
		return
	}
	allowOnce := false
	for _, o := range options {
		allowOnce = allowOnce || o.Kind == "allow_once"
	}
	if !allowOnce {
		// No way to allow it once: declined at once, never put to a person
		// as an Allow the client could not keep.
		_ = c.answerOutcome(id, acpOutcome(options, session.Verdict{}))
		e.Notice(NoticeDeclined, ACPNoAllowOnceWords)
		return
	}
	// The call it names, as the stream announced it: its id the call, its
	// name and input the step's; what the request's own toolCall gives
	// beside them goes into that input, so that a request about more than
	// the step showed is bound to no call. A request about a call holding
	// what the client does not read, or changed since it was announced, or
	// whose own toolCall holds a field the client does not read, is
	// declined.
	tc := object(p["toolCall"])
	var call string
	field(tc, "toolCallId", &call)
	step, announced := c.r.tools[call]
	if !announced || step.unread || step.changed || tc == nil || extraFields(tc, append(append([]string{}, acpShownFields...), acpUpdateFields...)) {
		_ = c.answerOutcome(id, acpOutcome(options, session.Verdict{}))
		e.Notice(NoticeDeclined, ACPDeclinedWords)
		return
	}
	fields := map[string]json.RawMessage{}
	name := "other"
	if announced {
		maps.Copy(fields, step.fields)
		name = step.name
	}
	for _, k := range acpShownFields {
		if raw, ok := tc[k]; ok && !isNull(raw) {
			fields[k] = bytes.Clone(raw)
		}
	}
	var reqName string
	field(tc, "name", &reqName)
	if reqName != "" {
		name = reqName
	} else if !announced {
		var kind string
		field(tc, "kind", &kind)
		if kind != "" {
			name = kind
		}
	}
	input, ok := acpInput(fields)
	if !ok || !rawInputRead(fields["rawInput"], c.keys) {
		_ = c.answerOutcome(id, acpOutcome(options, session.Verdict{}))
		e.Notice(NoticeDeclined, ACPDeclinedWords)
		return
	}
	key := idKey(id)
	c.mu.Lock()
	if len(c.requests) >= maxACPRequests {
		c.mu.Unlock()
		_ = c.answerOutcome(id, acpOutcome(options, session.Verdict{}))
		return
	}
	old, reused := c.byID[key]
	if reused {
		delete(c.requests, old)
	}
	c.reqSeq++
	ref := "acp-req-" + strconv.Itoa(c.reqSeq)
	c.requests[ref] = &acpRequest{id: bytes.Clone(id), options: options, call: call}
	c.byID[key] = ref
	c.mu.Unlock()
	if reused {
		// The agent named a new request by an open one's id: the open one's
		// card closes, its answer reaching nobody.
		e.PermissionWithdrawn(old)
	}
	e.Permission(session.PermissionRequest{Ref: ref, Call: call, Tool: name, Input: input})
}

// acpOptionKinds are the kinds of a permission option protocol 1 names.
var acpOptionKinds = []string{"allow_once", "allow_always", "reject_once", "reject_always"}

// acpOptions are a request's options when the client reads them whole: each
// an object of optionId, name and kind alone, its id not empty and given
// once, its kind one protocol 1 names, and at most one of kind allow_once;
// false otherwise.
func acpOptions(raw json.RawMessage) ([]acpOption, bool) {
	var raws []json.RawMessage
	if json.Unmarshal(raw, &raws) != nil {
		return nil, false
	}
	seen := map[string]bool{}
	allowOnce := 0
	out := make([]acpOption, 0, len(raws))
	for _, r := range raws {
		o := object(r)
		if o == nil || extraFields(o, []string{"optionId", "name", "kind"}) {
			return nil, false
		}
		var opt acpOption
		if !fieldIs(o, "optionId", &opt.ID) || opt.ID == "" || seen[opt.ID] || !fieldIs(o, "kind", &opt.Kind) || !contains(acpOptionKinds, opt.Kind) {
			return nil, false
		}
		seen[opt.ID] = true
		if opt.Kind == "allow_once" {
			allowOnce++
		}
		out = append(out, opt)
	}
	return out, allowOnce <= 1
}
