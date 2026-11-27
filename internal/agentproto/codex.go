package agentproto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// CodexAppServer is Codex's app server on stdio (`codex app-server --listen
// stdio://`): JSON-RPC 2.0, the header left out, one message a line.
const CodexAppServer = "codex-app-server"

// CodexMinVersion is the oldest Codex the driver was qualified against: the
// version whose stable schema (`codex app-server generate-json-schema`,
// without --experimental) the driver and its fixtures were written to
// (testdata/codex/README.md).
const CodexMinVersion = "0.167.2"

// codexMoves is Codex's Moves: false until a thread handed from the terminal
// to the app server and back, its writer lock released each way, is
// observed by hand (docs/plans/chat-or-terminal.md, row 10).
const codexMoves = false

// codexVerbatim is Codex's Verbatim: its app server's pages state no mark
// that a message's text is taken as written (no /command run, no @path
// expanded), so until a by-hand reading says what it does with such a
// message, the chat does not claim it (row 10).
const codexVerbatim = false

// Bounds of the Codex driver.
const (
	// CodexInitWait bounds Begin's wait for the initialize answer and the
	// thread's resume together: the resume starts the thread's MCP servers,
	// Conductor's among them. Well inside the session's own bound on a
	// start.
	CodexInitWait = 30 * time.Second
	// maxCodexRequests bounds the app server's requests the driver keeps
	// until they are answered or resolved (the session holds at most
	// MaxPendingPermissions of them open).
	maxCodexRequests = 256
	// maxCodexCalls bounds the driver's own requests waiting for their
	// answers.
	maxCodexCalls = 64
)

// The words of the Codex driver's answers (the plan's design 13, for Codex).
const (
	CodexNotQualifiedWords = "This version of Codex can only run in its terminal."
	CodexNotStartedWords   = "Codex did not start a chat."
	CodexNotSignedInWords  = "Codex is not signed in; sign in from its terminal."
	CodexUnreadableWords   = "Codex sent something Conductor cannot read; the session ended."
	// CodexNotTakenWords is the notice of a message the app server refused.
	CodexNotTakenWords = "Codex did not take the message."
	// CodexDeclinedWords is the notice of a request of Codex's that a chat
	// does not put to a person (a form of an MCP server's, a question, a
	// wider permission): declined.
	CodexDeclinedWords = "Codex asked for something a chat cannot show here; Conductor declined it."
	// NoticeAgentError is the code of an error the agent reported, and
	// NoticeNotTaken and NoticeDeclined the codes of the two above.
	NoticeAgentError = "agent_error"
	NoticeNotTaken   = "not_taken"
	NoticeDeclined   = "declined"
)

var (
	// ErrCodexNotQualified is Begin's answer for a Codex below
	// CodexMinVersion: the session stays in its terminal.
	ErrCodexNotQualified error = &words{s: CodexNotQualifiedWords, kind: kindNotQualified}
	// ErrCodexNotStarted is Begin's answer for an initialize answer that is
	// missing or fails the shape check, and for a thread not resumed.
	ErrCodexNotStarted error = &words{s: CodexNotStartedWords, kind: kindNotStarted}
	// ErrCodexNotSignedIn is Read's answer when the app server says, before
	// a first answer, that it is not signed in: the session goes back to
	// its terminal.
	ErrCodexNotSignedIn error = &words{s: CodexNotSignedInWords, kind: kindNotSignedIn}
	// ErrCodexUnreadable is Read's answer for a line that is not a JSON
	// object.
	ErrCodexUnreadable error = &words{s: CodexUnreadableWords, code: "unreadable"}
)

// CodexOptions are a Codex driver's.
type CodexOptions struct {
	// Resume is the thread Begin resumes and what with (CodexArgs).
	Resume CodexResume
	// InitWait bounds Begin, CodexInitWait when 0 (a test's knob).
	InitWait time.Duration
}

// Codex is the driver of Codex's app server on stdio (session.Protocol). It
// speaks the app server's stable surface alone: it never declares
// experimentalApi, and sends initialize, initialized, thread/resume,
// turn/start and turn/interrupt, and its answers to the server's requests,
// with the fields the stable schema of CodexMinVersion names. It answers
// an approval of a command or a file change through the session's
// permission question (accept or decline, never for the session), declines
// every other request the server makes of it, and reads the thread's items
// and turns into the session's events. It never reads, passes or keeps a
// credential: the child runs on the sign-in its program already has, and
// it names itself conductor.
type Codex struct {
	in       io.WriteCloser
	out      io.Reader
	resume   CodexResume
	initWait time.Duration

	// Writes go one at a time.
	wmu sync.Mutex
	// cmu guards closed: CloseInput never waits for a write that blocks.
	cmu    sync.Mutex
	closed bool

	mu sync.Mutex
	// nextID is the driver's next request id.
	nextID int64
	// calls are the driver's requests waiting for their answers, by id.
	calls map[string]*codexCall
	// begun says Begin was called; thread is the thread resumed.
	begun  bool
	thread string
	// turn is the turn under way ("" between turns, or until it is named),
	// turning that a message was taken or a turn started and has not
	// ended, interruptWanted that an interrupt waits for the turn's id.
	turn            string
	turning         bool
	interruptWanted bool
	// requests are the server's approval requests handed to the session,
	// by the ref the session answers under; byID the same by their own id.
	requests map[string]*codexRequest
	byID     map[string]string
	reqSeq   int

	readDone chan struct{}
	readOnce sync.Once

	// What only Read's goroutine touches.
	r codexRead
}

type codexCall struct {
	method string
	// ref is the session's ref of the message a turn/start carries.
	ref    string
	answer chan codexAnswer
}

type codexAnswer struct {
	result json.RawMessage
	err    *codexError
}

type codexError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexRequest struct {
	id     json.RawMessage
	method string
}

// codexRead is the state of the stream as Read follows it.
type codexRead struct {
	// answered says a turn has ended done: a sign-in failure after it is the
	// turn's error, not a start's.
	answered bool
	// streamed is what each agent message streamed, by its item id.
	streamed map[string]int
	// tools are the steps announced and not ended, with their input (for a
	// request about them), by item id, in the order they opened.
	tools   map[string]codexTool
	toolSeq []string
	// total is the thread's token use at the last report, before the turn's
	// start the use the turn began at.
	total, before codexTokens
	// errorTold says the turn's error was told as a notice.
	errorTold bool
}

type codexTool struct {
	name  string
	input []byte
}

type codexTokens struct{ in, out int64 }

// NewCodex is the driver of a Codex app server whose input is in and output
// out.
func NewCodex(in io.WriteCloser, out io.Reader, opts CodexOptions) *Codex {
	initWait := opts.InitWait
	if initWait <= 0 {
		initWait = CodexInitWait
	}
	return &Codex{
		in: in, out: out, resume: opts.Resume, initWait: initWait,
		calls: map[string]*codexCall{}, requests: map[string]*codexRequest{}, byID: map[string]string{},
		readDone: make(chan struct{}),
		r:        codexRead{streamed: map[string]int{}, tools: map[string]codexTool{}},
	}
}

var _ session.Protocol = (*Codex)(nil)

// Name is CodexAppServer.
func (c *Codex) Name() string { return CodexAppServer }

// Verbatim is codexVerbatim: no documented as-written mark.
func (c *Codex) Verbatim() bool { return codexVerbatim }

// codexClientInfo is how the driver names itself: "Use clientInfo.name to
// identify your client" (the app-server page).
func codexClientInfo() map[string]any {
	return map[string]any{"name": "conductor", "title": "Conductor", "version": version.Version}
}

// Begin writes initialize (clientInfo alone: no capabilities, so no
// experimental method or field is ever taken), checks its answer's shape (a
// userAgent; a version in it below CodexMinVersion is ErrCodexNotQualified),
// writes initialized, and resumes the terminal's thread (thread/resume with
// its turns left out): nil once the thread is resumed. It waits for nothing
// the server writes only after a first message, and returns by ctx's end,
// InitWait's, or the output's, at the latest.
func (c *Codex) Begin(ctx context.Context) error {
	c.mu.Lock()
	if c.begun {
		c.mu.Unlock()
		return errors.New("agentproto: Begin was called already")
	}
	c.begun = true
	c.mu.Unlock()
	if !conversationShape(c.resume.Thread) {
		return fmt.Errorf("%w (no thread to resume)", ErrCodexNotStarted)
	}
	ctx, cancel := context.WithTimeout(ctx, c.initWait)
	defer cancel()
	init, err := c.call(ctx, "initialize", map[string]any{"clientInfo": codexClientInfo()}, "")
	if err != nil {
		return err
	}
	if err := checkCodexInitialize(init); err != nil {
		return err
	}
	if err := c.writeJSON(map[string]any{"method": "initialized"}); err != nil {
		return fmt.Errorf("%w (initialized was not written: %v)", ErrCodexNotStarted, err)
	}
	params := map[string]any{"threadId": c.resume.Thread, "excludeTurns": true}
	for k, v := range map[string]string{"model": c.resume.Model, "approvalPolicy": c.resume.ApprovalPolicy, "sandbox": c.resume.Sandbox, "cwd": c.resume.Cwd} {
		if v != "" {
			params[k] = v
		}
	}
	res, err := c.call(ctx, "thread/resume", params, "")
	if err != nil {
		return err
	}
	id := threadOf(res)
	if !conversationShape(id) {
		return fmt.Errorf("%w (its thread/resume answer names no thread)", ErrCodexNotStarted)
	}
	return nil
}

// call writes the request method with params and waits for its answer,
// under ctx: its result, or ErrCodexNotStarted with why.
func (c *Codex) call(ctx context.Context, method string, params any, ref string) (json.RawMessage, error) {
	id, answer, err := c.request(method, params, ref)
	if err != nil {
		return nil, fmt.Errorf("%w (%s was not written: %v)", ErrCodexNotStarted, method, err)
	}
	select {
	case a := <-answer:
		if a.err != nil {
			return nil, fmt.Errorf("%w (%s was refused: %s)", ErrCodexNotStarted, method, cutWords(a.err.Message))
		}
		return a.result, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, fmt.Errorf("%w (no answer to %s: %v)", ErrCodexNotStarted, method, ctx.Err())
	case <-c.readDone:
		select {
		case a := <-answer:
			if a.err == nil {
				return a.result, nil
			}
		default:
		}
		return nil, fmt.Errorf("%w (its output ended before it answered %s)", ErrCodexNotStarted, method)
	}
}

// request writes a request under a new id and returns the id and where its
// answer goes.
func (c *Codex) request(method string, params any, ref string) (string, chan codexAnswer, error) {
	c.mu.Lock()
	if len(c.calls) >= maxCodexCalls {
		c.mu.Unlock()
		return "", nil, errors.New("too many requests wait for their answers")
	}
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	call := &codexCall{method: method, ref: ref, answer: make(chan codexAnswer, 1)}
	c.calls[id] = call
	c.mu.Unlock()
	msg := map[string]any{"id": c.nextIDValue(id), "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.writeJSON(msg); err != nil {
		c.forget(id)
		return "", nil, err
	}
	return id, call.answer, nil
}

// nextIDValue is the id as the wire carries it: a number.
func (c *Codex) nextIDValue(id string) json.Number { return json.Number(id) }

func (c *Codex) forget(id string) {
	c.mu.Lock()
	delete(c.calls, id)
	c.mu.Unlock()
}

// checkCodexInitialize is the shape check of an initialize answer: an
// object with a userAgent, and the version it names, when it names one, at
// or above CodexMinVersion. The user agent's form ("codex_cli_rs/0.163.0
// (...)") is not documented: one that names no version passes, the probed
// version having been checked before the start. Nothing else of it is kept
// (its codexHome is a path of the person's).
func checkCodexInitialize(raw json.RawMessage) error {
	m := object(raw)
	if m == nil {
		return fmt.Errorf("%w (its initialize answer is not an object)", ErrCodexNotStarted)
	}
	var ua string
	field(m, "userAgent", &ua)
	if ua == "" {
		return fmt.Errorf("%w (its initialize answer has no userAgent)", ErrCodexNotStarted)
	}
	if v := codexUserAgentVersion.FindStringSubmatch(ua); v != nil && !versionAtLeast(v[1], CodexMinVersion) {
		return fmt.Errorf("%w (version %s; a chat needs %s or later)", ErrCodexNotQualified, v[1], CodexMinVersion)
	}
	return nil
}

// codexUserAgentVersion finds the version in a user agent: the numbers
// after the first slash.
var codexUserAgentVersion = regexp.MustCompile(`^[^/\s]+/(\d+\.\d+\.\d+)`)

// threadOf is the id of a thread/resume answer's thread.
func threadOf(raw json.RawMessage) string {
	var id string
	field(object(get(object(raw), "thread")), "id", &id)
	return id
}

// Send writes m as a new turn's input (turn/start with one text input, the
// text as written: encoded as JSON, nothing added or taken out). The answer
// to it is the server taking the message (Accepted with m.Ref); a refusal
// is told as a notice, and the message stays unconfirmed.
func (c *Codex) Send(_ context.Context, m session.Message) error {
	c.mu.Lock()
	thread := c.thread
	c.mu.Unlock()
	if thread == "" {
		return errors.New("agentproto: no thread is resumed")
	}
	params := map[string]any{"threadId": thread, "input": []map[string]any{{"type": "text", "text": m.Text}}}
	// A turn is under way from before the write: its answer, and its end,
	// may come before the write returns.
	c.mu.Lock()
	c.turning = true
	c.mu.Unlock()
	_, _, err := c.request("turn/start", params, m.Ref)
	if err != nil {
		c.mu.Lock()
		c.turning = c.turn != ""
		c.mu.Unlock()
	}
	return err
}

// Interrupt writes turn/interrupt for the turn under way; "the turn
// finishes with status: interrupted" (the app-server page), which arrives as
// its turn/completed. Asked before the turn's id is known, it is written as
// the id arrives.
func (c *Codex) Interrupt(context.Context) error {
	c.mu.Lock()
	thread, turn := c.thread, c.turn
	if turn == "" {
		if c.turning {
			c.interruptWanted = true
		}
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	_, _, err := c.request("turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}, "")
	return err
}

// interruptNow writes the interrupt asked before the turn had an id, once
// it has one. Read's goroutine calls it.
func (c *Codex) interruptNow() {
	c.mu.Lock()
	want, thread, turn := c.interruptWanted, c.thread, c.turn
	if want && turn != "" {
		c.interruptWanted = false
	}
	c.mu.Unlock()
	if want && turn != "" {
		go func() { _, _, _ = c.request("turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}, "") }()
	}
}

// Reply answers the approval request ref: accept for an allow, decline for
// anything else (a deny, and a withdrawal the session makes: Codex's cancel
// would also interrupt the turn). A request the server has resolved or
// cleared already is answered with nothing.
func (c *Codex) Reply(_ context.Context, ref string, v session.Verdict) error {
	c.mu.Lock()
	r := c.requests[ref]
	if r != nil {
		delete(c.requests, ref)
		delete(c.byID, string(r.id))
	}
	c.mu.Unlock()
	if r == nil {
		return nil
	}
	decision := "decline"
	if v.Allow && !v.Cancel {
		decision = "accept"
	}
	return c.writeJSON(map[string]any{"id": r.id, "result": map[string]any{"decision": decision}})
}

// CloseInput ends the child's input; nothing is written after it.
func (c *Codex) CloseInput() error {
	c.cmu.Lock()
	if c.closed {
		c.cmu.Unlock()
		return nil
	}
	c.closed = true
	c.cmu.Unlock()
	return c.in.Close()
}

// writeJSON writes v as one line, HTML characters as they are.
func (c *Codex) writeJSON(v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
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
	_, err := c.in.Write(b.Bytes())
	return err
}

// Read reads the app server's output a line at a time and hands the
// session each event in order, each applied before the next line is read.
// It returns io.EOF at the output's end, ctx's error once ctx ends,
// ErrCodexUnreadable for a line that is not a JSON object, and
// ErrCodexNotSignedIn when the server says before a first answer that it is
// not signed in. A line past session.MaxProtocolLine is read to its end
// without being kept, with a notice. Unknown methods and fields, and every
// event of another thread (a subagent's), change nothing.
func (c *Codex) Read(ctx context.Context, e session.ProtocolEvents) error {
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
			c.tooLarge(l.b, e)
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

// handle hands the session what one message says: a notification, a
// request of the server's, or the answer to one of the driver's.
func (c *Codex) handle(b []byte, e session.ProtocolEvents) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		if err == nil {
			err = errors.New("not an object")
		}
		return fmt.Errorf("%w (%v)", ErrCodexUnreadable, err)
	}
	var method string
	field(m, "method", &method)
	id, hasID := m["id"]
	hasID = hasID && validID(id)
	params := object(m["params"])
	switch {
	case method != "" && hasID:
		c.serverRequest(method, id, params, e)
	case method != "":
		return c.notification(method, params, e)
	case hasID:
		c.answer(id, m, e)
	}
	return nil
}

// validID says raw is a JSON-RPC id: a string or an integer.
func validID(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return len(s) <= 256
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		_, err := n.Int64()
		return err == nil
	}
	return false
}

// idKey is an id as the driver keeps it: its JSON, compact.
func idKey(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// answer is the answer to one of the driver's requests.
func (c *Codex) answer(id json.RawMessage, m map[string]json.RawMessage, e session.ProtocolEvents) {
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
	case "thread/resume":
		if a.err == nil {
			if t := threadOf(a.result); conversationShape(t) {
				c.mu.Lock()
				c.thread = t
				c.mu.Unlock()
				e.Conversation(t)
			}
		}
	case "turn/start":
		if a.err != nil {
			c.mu.Lock()
			c.turning = c.turn != ""
			c.mu.Unlock()
			e.Notice(NoticeNotTaken, CodexNotTakenWords)
			break
		}
		e.Accepted(call.ref)
		var turn string
		field(object(get(object(a.result), "turn")), "id", &turn)
		c.mu.Lock()
		if c.turn == "" && turn != "" && c.turning {
			c.turn = turn
		}
		c.mu.Unlock()
		c.interruptNow()
	}
	call.answer <- a
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// ofThread says params are of the thread resumed (or name none).
func (c *Codex) ofThread(params map[string]json.RawMessage) bool {
	var t string
	field(params, "threadId", &t)
	c.mu.Lock()
	defer c.mu.Unlock()
	return t == "" || c.thread == "" || t == c.thread
}

// notification is one of the server's notifications.
func (c *Codex) notification(method string, p map[string]json.RawMessage, e session.ProtocolEvents) error {
	if !c.ofThread(p) {
		return nil
	}
	switch method {
	case "turn/started":
		var turn string
		field(object(p["turn"]), "id", &turn)
		c.mu.Lock()
		c.turn, c.turning = turn, true
		c.mu.Unlock()
		c.r.before, c.r.errorTold = c.r.total, false
		e.TurnStarted(turn)
		c.interruptNow()
	case "item/started":
		c.itemStarted(object(p["item"]), e)
	case "item/agentMessage/delta":
		var item, delta string
		field(p, "itemId", &item)
		field(p, "delta", &delta)
		if item == "" || delta == "" {
			return nil
		}
		if _, open := c.r.streamed[item]; !open && len(c.r.streamed) >= maxStreamed {
			return nil
		}
		c.r.streamed[item] += len(delta)
		e.Text(item, delta, false)
	case "item/completed":
		c.itemCompleted(object(p["item"]), e)
	case "thread/tokenUsage/updated":
		total := object(get(object(p["tokenUsage"]), "total"))
		var in, out int64
		field(total, "inputTokens", &in)
		field(total, "outputTokens", &out)
		c.r.total = codexTokens{in: in, out: out}
	case "serverRequest/resolved":
		key := idKey(p["requestId"])
		c.mu.Lock()
		ref, open := c.byID[key]
		if open {
			delete(c.byID, key)
			delete(c.requests, ref)
		}
		c.mu.Unlock()
		if open {
			e.PermissionWithdrawn(ref)
		}
	case "error":
		var retry bool
		field(p, "willRetry", &retry)
		info, message := turnError(object(p["error"]))
		if retry {
			return nil
		}
		if info == "unauthorized" && !c.r.answered {
			return c.notSignedIn(e)
		}
		if message != "" && !c.r.errorTold {
			c.r.errorTold = true
			e.Notice(NoticeAgentError, message)
		}
	case "turn/completed":
		return c.turnCompleted(object(p["turn"]), e)
	}
	return nil
}

// turnError is a turn error's codexErrorInfo, where it is one of the named
// ones, and its message.
func turnError(err map[string]json.RawMessage) (info, message string) {
	field(err, "codexErrorInfo", &info)
	field(err, "message", &message)
	return info, message
}

// notSignedIn ends the read: the agent is not signed in.
func (c *Codex) notSignedIn(e session.ProtocolEvents) error {
	e.Notice(NoticeNotSignedIn, CodexNotSignedInWords)
	return ErrCodexNotSignedIn
}

// turnCompleted is a turn's end: the texts still open closed, its outcome
// and what it used.
func (c *Codex) turnCompleted(turn map[string]json.RawMessage, e session.ProtocolEvents) error {
	var status string
	field(turn, "status", &status)
	info, message := turnError(object(turn["error"]))
	if status == "failed" && info == "unauthorized" && !c.r.answered {
		return c.notSignedIn(e)
	}
	for _, ref := range slices.Sorted(maps.Keys(c.r.streamed)) {
		e.Text(ref, "", true)
	}
	clear(c.r.streamed)
	outcome := "error"
	switch status {
	case "completed":
		outcome = "done"
	case "interrupted":
		outcome = "interrupted"
	case "failed":
		switch info {
		case "usageLimitExceeded", "contextWindowExceeded", "sessionBudgetExceeded":
			outcome = "limit"
		}
		if message != "" && !c.r.errorTold {
			e.Notice(NoticeAgentError, message)
		}
	}
	if outcome == "done" {
		c.r.answered = true
	}
	u := session.Usage{}
	if t, b := c.r.total, c.r.before; t.in >= b.in && t.out >= b.out {
		u.InputTokens, u.OutputTokens = t.in-b.in, t.out-b.out
	}
	c.r.before, c.r.errorTold = c.r.total, false
	c.mu.Lock()
	c.turn, c.turning, c.interruptWanted = "", false, false
	c.mu.Unlock()
	e.TurnEnded(outcome, u)
	return nil
}

// itemStarted is an item of the turn begun: a step announced with its
// input. A message's words come as its deltas and its completion.
func (c *Codex) itemStarted(item map[string]json.RawMessage, e session.ProtocolEvents) {
	var id, kind string
	field(item, "id", &id)
	field(item, "type", &kind)
	if id == "" {
		return
	}
	name, input, summary, ok := codexStep(kind, item)
	if !ok {
		return
	}
	c.announce(id, name, input, summary, e)
}

// announce tells the session of a step, once.
func (c *Codex) announce(id, name string, input []byte, summary string, e session.ProtocolEvents) {
	r := &c.r
	if _, open := r.tools[id]; open {
		return
	}
	r.toolSeq = append(r.toolSeq, id)
	if len(r.toolSeq) > maxOpenTools {
		delete(r.tools, r.toolSeq[0])
		r.toolSeq = r.toolSeq[1:]
	}
	r.tools[id] = codexTool{name: name, input: input}
	e.ToolStarted(session.ToolStep{Ref: id, Name: name, Input: bytes.Clone(input), Summary: summary})
}

// codexStep is the step an item is, by its type: its name, its input as the
// session keeps it and the person is asked about, and its one line.
// Messages, reasoning, plans and the rest are no step.
func codexStep(kind string, item map[string]json.RawMessage) (name string, input []byte, summary string, ok bool) {
	switch kind {
	case "commandExecution":
		var command, cwd string
		field(item, "command", &command)
		field(item, "cwd", &cwd)
		return "commandExecution", commandInput(command, cwd), command, true
	case "fileChange":
		var changes []json.RawMessage
		field(item, "changes", &changes)
		in, first := changesInput(changes)
		return "fileChange", in, first, true
	case "mcpToolCall":
		var server, tool string
		field(item, "server", &server)
		field(item, "tool", &tool)
		return "mcp__" + server + "__" + tool, argumentsOf(item), "", true
	case "dynamicToolCall":
		var tool string
		field(item, "tool", &tool)
		return tool, argumentsOf(item), "", tool != ""
	case "webSearch":
		var query string
		field(item, "query", &query)
		b, _ := marshal(map[string]any{"query": query})
		return "webSearch", b, query, true
	}
	return "", nil, "", false
}

// commandInput is a command's input as the session keeps it: its command
// line and its folder.
func commandInput(command, cwd string) []byte {
	b, _ := marshal(map[string]any{"command": command, "cwd": cwd})
	return b
}

// changesInput is a file change's input: each change's path, kind and diff;
// and the first path.
func changesInput(changes []json.RawMessage) ([]byte, string) {
	list := make([]map[string]any, 0, len(changes))
	first := ""
	for _, raw := range changes {
		ch := object(raw)
		var path, diff, kind string
		field(ch, "path", &path)
		field(ch, "diff", &diff)
		field(object(ch["kind"]), "type", &kind)
		if first == "" {
			first = path
		}
		list = append(list, map[string]any{"path": path, "kind": kind, "diff": diff})
	}
	b, _ := marshal(map[string]any{"changes": list})
	return b, first
}

// argumentsOf is a tool call's arguments as the server gave them, {} for
// none.
func argumentsOf(item map[string]json.RawMessage) []byte {
	if raw, ok := item["arguments"]; ok && !isNull(raw) {
		return bytes.Clone(raw)
	}
	return []byte("{}")
}

// marshal is json.Marshal with HTML characters as they are.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// itemCompleted is an item's final state ("treat this as the
// authoritative state"): a message's words closed, a step's result, the
// files a change wrote.
func (c *Codex) itemCompleted(item map[string]json.RawMessage, e session.ProtocolEvents) {
	var id, kind string
	field(item, "id", &id)
	field(item, "type", &kind)
	if id == "" {
		return
	}
	if kind == "agentMessage" {
		var text string
		field(item, "text", &text)
		streamed, was := c.r.streamed[id]
		if !was && text == "" {
			return
		}
		delete(c.r.streamed, id)
		rest := text
		switch {
		case !was:
		case streamed <= len(text):
			rest = text[streamed:]
		default:
			rest = ""
		}
		e.Text(id, rest, true)
		return
	}
	name, input, summary, ok := codexStep(kind, item)
	if !ok {
		return
	}
	// A step whose start was not told is announced as it ends.
	c.announce(id, name, input, summary, e)
	delete(c.r.tools, id)
	var status string
	field(item, "status", &status)
	res := session.ToolResult{Status: session.ToolOK}
	switch status {
	case "failed":
		res.Status = session.ToolError
	case "declined":
		res.Status = session.ToolDenied
	}
	var ms int64
	field(item, "durationMs", &ms)
	if ms > 0 {
		res.Duration = time.Duration(ms) * time.Millisecond
	}
	switch kind {
	case "commandExecution":
		var exit *int
		field(item, "exitCode", &exit)
		res.Exit = exit
		if exit != nil && *exit != 0 && res.Status == session.ToolOK {
			res.Status = session.ToolError
		}
		var output string
		field(item, "aggregatedOutput", &output)
		res.Output = []byte(output)
	case "mcpToolCall":
		var message string
		field(object(item["error"]), "message", &message)
		if message != "" {
			res.Output = []byte(message)
		} else {
			var content json.RawMessage
			field(object(item["result"]), "content", &content)
			res.Output = resultText(content)
		}
	case "dynamicToolCall":
		var success *bool
		field(item, "success", &success)
		if success != nil && !*success && res.Status == session.ToolOK {
			res.Status = session.ToolError
		}
		var content json.RawMessage
		field(item, "contentItems", &content)
		res.Output = resultText(content)
	}
	e.ToolEnded(id, res)
	if kind == "fileChange" && res.Status == session.ToolOK {
		var changes []json.RawMessage
		field(item, "changes", &changes)
		for _, raw := range changes {
			if f, ok := codexFileChange(id, object(raw)); ok {
				e.FileChanged(f)
			}
		}
	}
}

// codexFileChange is a change a file change item made: its path, its op by
// its kind, the lines its diff adds and removes, and the diff. A diff with
// no line marked added or removed (the form Codex gives a whole file added
// or deleted is not documented) counts every line of it for an add or a
// delete.
func codexFileChange(tool string, ch map[string]json.RawMessage) (session.FileChange, bool) {
	var path, diff, kind string
	field(ch, "path", &path)
	field(ch, "diff", &diff)
	field(object(ch["kind"]), "type", &kind)
	if path == "" {
		return session.FileChange{}, false
	}
	f := session.FileChange{Tool: tool, Path: path, Op: session.ChangeEdit}
	switch kind {
	case "add":
		f.Op = session.ChangeCreate
	case "delete":
		f.Op = session.ChangeDelete
	}
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			f.Added++
		case strings.HasPrefix(l, "-"):
			f.Removed++
		}
	}
	if diff != "" && f.Added == 0 && f.Removed == 0 {
		switch f.Op {
		case session.ChangeCreate:
			f.Added = len(lines)
		case session.ChangeDelete:
			f.Removed = len(lines)
		}
	}
	if diff != "" {
		f.Diff = []byte(diff)
	}
	return f, true
}

// serverRequest is a request the server makes of the driver. An approval of
// a command or a file change of the thread is the session's permission
// question, its input the step's as announced (a command's own line and
// folder where the request names them); every other request is answered at
// once, declined, so that nothing waits on it.
func (c *Codex) serverRequest(method string, id json.RawMessage, p map[string]json.RawMessage, e session.ProtocolEvents) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if !c.ofThread(p) {
			c.decline(id)
			return
		}
		c.approval(method, id, p, e)
	case "mcpServer/elicitation/request":
		_ = c.writeJSON(map[string]any{"id": id, "result": map[string]any{"action": "decline"}})
		e.Notice(NoticeDeclined, CodexDeclinedWords)
	case "item/tool/requestUserInput", "item/permissions/requestApproval":
		c.refuse(id)
		e.Notice(NoticeDeclined, CodexDeclinedWords)
	default:
		// Its tools' calls (none are registered), a sign-in's tokens
		// (Conductor never passes one), an attestation (never asked for),
		// the older approvals: none is Conductor's to answer.
		c.refuse(id)
	}
}

// decline answers an approval request decline at once.
func (c *Codex) decline(id json.RawMessage) {
	_ = c.writeJSON(map[string]any{"id": id, "result": map[string]any{"decision": "decline"}})
}

// refuse answers a request with JSON-RPC's method-not-found.
func (c *Codex) refuse(id json.RawMessage) {
	_ = c.writeJSON(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "Conductor does not answer this request"}})
}

// approval hands the session an approval request: the step it names
// (announced first where the stream did not), the request's own command
// line and folder for a command, the step's changes and the root it asks to
// write under for a file change.
func (c *Codex) approval(method string, id json.RawMessage, p map[string]json.RawMessage, e session.ProtocolEvents) {
	var item, cwd, command, grantRoot string
	field(p, "itemId", &item)
	field(p, "cwd", &cwd)
	field(p, "command", &command)
	field(p, "grantRoot", &grantRoot)
	if item == "" {
		c.decline(id)
		return
	}
	step, announced := c.r.tools[item]
	var tool string
	var input []byte
	if method == "item/commandExecution/requestApproval" {
		tool = "commandExecution"
		if announced && step.name == tool {
			var was map[string]string
			_ = json.Unmarshal(step.input, &was)
			if command == "" {
				command = was["command"]
			}
			if cwd == "" {
				cwd = was["cwd"]
			}
		}
		input = commandInput(command, cwd)
		if !announced {
			c.announce(item, tool, input, command, e)
		}
	} else {
		tool = "fileChange"
		if !announced || step.name != tool {
			// No change was shown to ask about: declined, never asked
			// blind.
			c.decline(id)
			return
		}
		// The changes as they were announced, so that the request is about
		// the step shown; with the root it asks to write under beside them.
		input = step.input
		if grantRoot != "" {
			var was map[string]any
			if json.Unmarshal(step.input, &was) != nil || was == nil {
				c.decline(id)
				return
			}
			was["grantRoot"] = grantRoot
			input, _ = marshal(was)
		}
	}
	key := idKey(id)
	c.mu.Lock()
	if len(c.requests) >= maxCodexRequests {
		c.mu.Unlock()
		c.decline(id)
		return
	}
	if old, ok := c.byID[key]; ok {
		delete(c.requests, old)
	}
	c.reqSeq++
	ref := "codex-req-" + strconv.Itoa(c.reqSeq)
	c.requests[ref] = &codexRequest{id: bytes.Clone(id), method: method}
	c.byID[key] = ref
	c.mu.Unlock()
	e.Permission(session.PermissionRequest{Ref: ref, Call: item, Tool: tool, Input: input, Cwd: cwd})
}

// codexItemIDInLine finds the item a cut line completes.
var codexItemIDInLine = regexp.MustCompile(`"item"\s*:\s*\{[^{}]*?"id"\s*:\s*"([^"\\]{1,128})"`)

// tooLarge is a line past session.MaxProtocolLine, of which head is the
// start: the step whose completion it is ends with its output cut, and a
// notice says so.
func (c *Codex) tooLarge(head []byte, e session.ProtocolEvents) {
	e.Notice(NoticeTooLarge, TooLargeWords)
	if !bytes.Contains(head, []byte(`"item/completed"`)) {
		return
	}
	m := codexItemIDInLine.FindSubmatch(head)
	if m == nil {
		return
	}
	id := string(m[1])
	if _, open := c.r.tools[id]; !open {
		return
	}
	delete(c.r.tools, id)
	e.ToolEnded(id, session.ToolResult{Status: session.ToolOK, Cut: true})
}

// object is raw as an object's fields, nil when it is none.
func object(raw json.RawMessage) map[string]json.RawMessage {
	if !isObject(raw) {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// get is m's key, nil for none.
func get(m map[string]json.RawMessage, key string) json.RawMessage {
	if m == nil {
		return nil
	}
	return m[key]
}
