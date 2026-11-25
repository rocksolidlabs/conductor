package agentproto

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// ClaudeMinVersion is the oldest Claude Code the driver was qualified
// against: the version its fixtures were recorded from
// (testdata/claude/README.md). Its pages put client_composed, the mark
// that a message is taken as written, at 2.1.248 or later.
const ClaudeMinVersion = "2.1.331"

// Bounds of the Claude Code driver.
const (
	// MaxClaudeInput is how much the driver writes into one child before it
	// asks for a fresh one: Claude Code's pages cap piped input at 10 MB
	// and do not say whether that holds for a long stream-json input, so
	// past 8 MiB written the next turn's end calls ClaudeOptions.Restart,
	// which ends the child and resumes the same conversation.
	MaxClaudeInput = 8 << 20
	// maxPendingSends bounds the messages written and not yet acknowledged
	// that the driver remembers (the session has at most one in flight).
	maxPendingSends = 64
	// maxDenied bounds the tool calls a permission_denied names that the
	// driver remembers until their results.
	maxDenied = 256
	// maxOpenTools bounds the tool calls the driver follows until their
	// results (the session follows as many).
	maxOpenTools = 1000
	// maxBackground bounds the background tasks the driver counts.
	maxBackground = 256
	// maxStreamed bounds the streamed texts open at once.
	maxStreamed = 1000
	// maxCutScan is how much of a line past session.MaxProtocolLine the
	// driver keeps to find which step's output it was.
	maxCutScan = 64 << 10
	// readChunk is the read buffer of the output.
	readChunk = 64 << 10
)

// The words of the driver's answers (the plan's, design 13).
const (
	NotQualifiedWords = "This version of Claude Code can only run in its terminal."
	NotStartedWords   = "Claude Code did not start a chat."
	NotSignedInWords  = "Claude Code is not signed in; sign in from its terminal."
	UnreadableWords   = "Claude Code sent something Conductor cannot read; the session ended."
	TooLargeWords     = "A step's output was too large to show."
	// The notices' codes.
	NoticeTooLarge    = "too_large"
	NoticeNotSignedIn = "not_signed_in"
)

var (
	// ErrNotQualified is Begin's answer for a version below
	// ClaudeMinVersion, or one the agent does not say: the session stays in
	// its terminal.
	ErrNotQualified error = &words{s: NotQualifiedWords}
	// ErrNotStarted is Begin's answer for an initialize answer that is
	// missing or fails the shape check.
	ErrNotStarted error = &words{s: NotStartedWords}
	// ErrNotSignedIn is Read's answer when the agent says, before its first
	// answer, that it is not signed in: the child is ended and the session
	// goes back to its terminal.
	ErrNotSignedIn error = &words{s: NotSignedInWords}
	// ErrUnreadable is Read's answer for a line that is not the protocol's.
	ErrUnreadable error = &words{s: UnreadableWords, code: "unreadable"}
	// errInputClosed is a write after CloseInput.
	errInputClosed = errors.New("agentproto: the agent's input is closed")
)

// words is an error whose words are a person's to read: one sentence, as
// the plan gives it (design 13).
type words struct{ s, code string }

func (e *words) Error() string { return e.s }

// BackToTerminal says a Read that ended with this error leaves the
// session's terminal able to go on where the chat cannot: an agent not
// signed in (ErrNotSignedIn) goes back to its terminal on the same
// conversation, where the person signs in (design 4).
func (e *words) BackToTerminal() bool { return e == ErrNotSignedIn }

// EndNotice is the notice a session ends a Read with this error by: its
// code and words, or none ("") where the driver has told the session.
func (e *words) EndNotice() (code, words string) {
	if e.code == "" {
		return "", ""
	}
	return e.code, e.s
}

// ClaudeOptions are a Claude Code driver's.
type ClaudeOptions struct {
	// Restart, when set, is called once, as the first turn that ends with
	// more than MaxClaudeInput written into the child ends, before the
	// session is told of its end: the caller ends the child at that end and
	// resumes the same conversation in a new one (the session's
	// RestartProtocol, which waits for the turn's end it is about to hear).
	Restart func()
	// InitWait bounds Begin's wait for the initialize answer, ClaudeInitWait
	// when 0, and MaxInput the bytes written before a restart,
	// MaxClaudeInput when 0 (a test's knobs).
	InitWait time.Duration
	MaxInput int64
}

// ClaudeInitWait bounds the wait for the initialize answer: well inside the
// session's own bound on a start, so a child that never answers goes back
// to the terminal in seconds. The recorded run answered in under two.
const ClaudeInitWait = 20 * time.Second

// Claude is the driver of Claude Code's print mode with stream-json in and
// out (session.Protocol): it writes the initialize control request, each
// message as a user message with client_composed set (taken as written: no
// @path expanded, no /command run), and the interrupt control request, and
// reads the stream's events into the session's. It never reads, passes or
// keeps a credential: the child runs on the sign-in its program already
// has.
type Claude struct {
	in       io.WriteCloser
	out      io.Reader
	restart  func()
	max      int64
	initWait time.Duration

	// Writes go one at a time; written counts their bytes.
	wmu     sync.Mutex
	written atomic.Int64
	// cmu guards closed: CloseInput never waits for a write that blocks.
	cmu    sync.Mutex
	closed bool

	ids atomic.Uint64

	mu sync.Mutex
	// initID is the initialize request's id, initAnswer where its answer
	// goes (once).
	initID     string
	initAnswer chan error
	// receipt says the child's system/init offers the interrupt receipt.
	receipt bool
	// interrupting says an interrupt was written in the turn.
	interrupting bool
	// sends are the messages written and not yet acknowledged, oldest
	// first: the uuid the driver wrote and the session's ref.
	sends []pendingSend

	readDone chan struct{}
	readOnce sync.Once

	// What only Read's goroutine touches.
	r claudeRead
}

type pendingSend struct{ uuid, ref string }

// claudeRead is the state of the stream as Read follows it.
type claudeRead struct {
	inTurn bool
	// answered says a turn has ended with an answer: a not-signed-in
	// report after it is the turn's error, not a start's.
	answered bool
	// authFailed says the turn's assistant message said
	// authentication_failed.
	authFailed bool
	conv       string
	// streamMsg is the message the stream events are of; streamed what
	// each of its text blocks streamed, by ref.
	streamMsg string
	streamed  map[string]int
	// asstMsg and asstIdx place each assistant line's block in its message.
	asstMsg string
	asstIdx int
	tools   map[string]string
	toolSeq []string
	denied  map[string]bool
	deniedQ []string
	bg      map[string]bool
	bgOn    bool
	// lastCost is the previous result's total cost in this child; costKnown
	// that there was one.
	lastCost  float64
	costKnown bool
	restarted bool
}

// NewClaude is the driver of a Claude Code child whose input is in and
// output out.
func NewClaude(in io.WriteCloser, out io.Reader, opts ClaudeOptions) *Claude {
	initWait := opts.InitWait
	if initWait <= 0 {
		initWait = ClaudeInitWait
	}
	max := opts.MaxInput
	if max <= 0 {
		max = MaxClaudeInput
	}
	return &Claude{
		in: in, out: out, restart: opts.Restart, max: max, initWait: initWait,
		readDone: make(chan struct{}),
		r:        claudeRead{streamed: map[string]int{}, tools: map[string]string{}, denied: map[string]bool{}, bg: map[string]bool{}},
	}
}

var _ session.Protocol = (*Claude)(nil)

// Name is ClaudeStreamJSON.
func (c *Claude) Name() string { return ClaudeStreamJSON }

// Verbatim is true: every message carries client_composed.
func (c *Claude) Verbatim() bool { return true }

// Written is how many bytes the driver has written into the child.
func (c *Claude) Written() int64 { return c.written.Load() }

func (c *Claude) newID(kind string) string {
	return "conductor-" + kind + "-" + strconv.FormatUint(c.ids.Add(1), 10)
}

// Begin writes the initialize control request alone and returns once its
// answer has passed the shape check: a success that echoes the request's
// id, a claude_code_version at or above ClaudeMinVersion
// (ErrNotQualified), and its pending_permission_requests list. It waits
// for nothing the agent writes only after a first message (system/init
// comes then), and returns by ctx's end, or the output's, at the latest.
func (c *Claude) Begin(ctx context.Context) error {
	c.mu.Lock()
	if c.initID != "" {
		c.mu.Unlock()
		return errors.New("agentproto: Begin was called already")
	}
	c.initID = c.newID("init")
	c.initAnswer = make(chan error, 1)
	id, answer := c.initID, c.initAnswer
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, c.initWait)
	defer cancel()
	if err := c.writeJSON(controlRequest{Type: "control_request", RequestID: id, Request: map[string]string{"subtype": "initialize"}}); err != nil {
		return fmt.Errorf("%w (its initialize request was not written: %v)", ErrNotStarted, err)
	}
	select {
	case err := <-answer:
		return err
	case <-ctx.Done():
		return fmt.Errorf("%w (no answer to its initialize request: %v)", ErrNotStarted, ctx.Err())
	case <-c.readDone:
		// The answer may have come with the output's last line.
		select {
		case err := <-answer:
			return err
		default:
		}
		return fmt.Errorf("%w (its output ended before it answered)", ErrNotStarted)
	}
}

type controlRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Request   any    `json:"request"`
}

// userMessage is a message written into the conversation (the TypeScript
// SDK's SDKUserMessage): its uuid, which the replay names, and
// client_composed, so that Claude Code delivers the text as written.
type userMessage struct {
	Type            string      `json:"type"`
	UUID            string      `json:"uuid"`
	ClientComposed  bool        `json:"client_composed"`
	ParentToolUseID *string     `json:"parent_tool_use_id"`
	Message         userContent `json:"message"`
}

type userContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Send writes m as a user message with client_composed set and a uuid of
// the driver's own, which the replay of it names (Accepted with m.Ref).
// The text goes as written: encoded as JSON, nothing added or taken out.
func (c *Claude) Send(_ context.Context, m session.Message) error {
	u := newUUID()
	c.mu.Lock()
	c.sends = append(c.sends, pendingSend{uuid: u, ref: m.Ref})
	if len(c.sends) > maxPendingSends {
		c.sends = c.sends[len(c.sends)-maxPendingSends:]
	}
	c.mu.Unlock()
	err := c.writeJSON(userMessage{Type: "user", UUID: u, ClientComposed: true, Message: userContent{Role: "user", Content: m.Text}})
	if err != nil {
		c.mu.Lock()
		for i, s := range c.sends {
			if s.uuid == u {
				c.sends = append(c.sends[:i], c.sends[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
	}
	return err
}

// Interrupt writes the interrupt control request, when the child's
// system/init offered the interrupt receipt (interrupt_receipt_v1); the
// turn's end arrives as its result. Without the receipt it writes nothing
// and answers session.ErrNoInterrupt: the session ends the child and
// resumes the conversation. It never signals the child.
func (c *Claude) Interrupt(context.Context) error {
	c.mu.Lock()
	ok := c.receipt
	if ok {
		c.interrupting = true
	}
	c.mu.Unlock()
	if !ok {
		return session.ErrNoInterrupt
	}
	return c.writeJSON(controlRequest{Type: "control_request", RequestID: c.newID("interrupt"), Request: map[string]string{"subtype": "interrupt"}})
}

// Reply writes nothing: Claude Code's permission prompts reach Conductor
// through its MCP tool, never as requests in its stream.
func (c *Claude) Reply(context.Context, string, session.Verdict) error { return nil }

// CloseInput ends the child's input; nothing is written after it.
func (c *Claude) CloseInput() error {
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
func (c *Claude) writeJSON(v any) error {
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
	n, err := c.in.Write(b.Bytes())
	c.written.Add(int64(n))
	return err
}

// newUUID is a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("agentproto: crypto/rand unavailable: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// line is one line of the output: its bytes (the first maxCutScan of a
// line past session.MaxProtocolLine, which is cut).
type line struct {
	b   []byte
	cut bool
	err error
}

// Read reads the child's output a line at a time and hands the session
// each event in order, each applied before the next line is read (the
// session's pace is the driver's queue). It returns io.EOF at the output's
// end, ctx's error once ctx ends, ErrUnreadable for a line that is not a
// JSON object, and ErrNotSignedIn when the agent says before its first
// answer that it is not signed in. A line past session.MaxProtocolLine is
// read to its end without being kept: its step's output is marked cut and
// a notice says so. Unknown events and fields change nothing.
func (c *Claude) Read(ctx context.Context, e session.ProtocolEvents) error {
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

// readLines reads r's lines to lines until its end (a line with io.EOF or
// the read's error) or quit.
func readLines(r io.Reader, lines chan<- line, quit <-chan struct{}) {
	br := bufio.NewReaderSize(r, readChunk)
	send := func(l line) bool {
		select {
		case lines <- l:
			return true
		case <-quit:
			return false
		}
	}
	for {
		var buf []byte
		size, cut := 0, false
		for {
			chunk, err := br.ReadSlice('\n')
			size += len(chunk)
			if !cut {
				buf = append(buf, chunk...)
				if content := len(bytes.TrimRight(buf, "\r\n")); content > session.MaxProtocolLine {
					cut = true
					buf = buf[:maxCutScan:maxCutScan]
				}
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				if size > 0 && errors.Is(err, io.EOF) {
					if !send(line{b: buf, cut: cut}) {
						return
					}
				}
				send(line{err: err})
				return
			}
			break
		}
		if !send(line{b: buf, cut: cut}) {
			return
		}
	}
}

// The parts of the stream's events the driver reads. Every field is
// optional, and an unknown one is ignored.
type claudeEvent struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	UUID            string          `json:"uuid"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	IsReplay        bool            `json:"isReplay"`
	Message         json.RawMessage `json:"message"`
	ToolUseResult   json.RawMessage `json:"tool_use_result"`
	Event           json.RawMessage `json:"event"`
	Error           json.RawMessage `json:"error"`
	Response        json.RawMessage `json:"response"`
	RequestID       string          `json:"request_id"`
	// system/init
	Capabilities []string `json:"capabilities"`
	// system task events and permission_denied
	TaskID    string          `json:"task_id"`
	ToolUseID string          `json:"tool_use_id"`
	Ambient   bool            `json:"ambient"`
	Patch     json.RawMessage `json:"patch"`
	Tasks     json.RawMessage `json:"tasks"`
	// system/api_retry
	ErrorStatus *int `json:"error_status"`
	// result
	IsError        bool    `json:"is_error"`
	APIErrorStatus *int    `json:"api_error_status"`
	TerminalReason string  `json:"terminal_reason"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	Usage          struct {
		InputTokens   int64 `json:"input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		OutputTokens  int64 `json:"output_tokens"`
	} `json:"usage"`
}

// handle hands the session what one line says.
func (c *Claude) handle(b []byte, e session.ProtocolEvents) error {
	var ev claudeEvent
	if err := decodeEvent(b, &ev); err != nil {
		return fmt.Errorf("%w (%v)", ErrUnreadable, err)
	}
	switch ev.Type {
	case "control_response":
		c.controlResponse(ev.Response)
	case "control_request":
		// Conductor registers no callback with the child (no hooks, no
		// SDK servers, no permission route in its stream): any request
		// the child makes of it is answered as not supported, so that
		// nothing waits on it.
		if ev.RequestID != "" {
			_ = c.writeJSON(map[string]any{"type": "control_response", "response": map[string]string{
				"subtype": "error", "request_id": ev.RequestID, "error": "Conductor does not answer this request"}})
		}
	case "system":
		return c.system(ev, e)
	case "stream_event":
		c.streamEvent(ev, e)
	case "assistant":
		c.assistant(ev, e)
	case "user":
		c.user(ev, e)
	case "result":
		return c.result(ev, e)
	}
	return nil
}

// controlResponse takes the answer to the initialize request; answers to
// the driver's other requests (an interrupt's receipt) change nothing.
func (c *Claude) controlResponse(raw json.RawMessage) {
	var r struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Error     string          `json:"error"`
		Response  json.RawMessage `json:"response"`
		Pending   json.RawMessage `json:"pending_permission_requests"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	c.mu.Lock()
	answer := c.initAnswer
	mine := r.RequestID != "" && r.RequestID == c.initID && answer != nil
	if mine {
		c.initAnswer = nil
	}
	c.mu.Unlock()
	if mine {
		answer <- checkInitialize(r.Subtype, r.Response, r.Pending)
	}
}

// checkInitialize is the shape check of an initialize answer: a success,
// with a payload whose claude_code_version is at least ClaudeMinVersion,
// and a pending_permission_requests list beside it. Nothing of an account
// is read.
func checkInitialize(subtype string, payload, pending json.RawMessage) error {
	if subtype != "success" {
		return fmt.Errorf("%w (its initialize answer is %q, not a success)", ErrNotStarted, subtype)
	}
	var p struct {
		Version *string `json:"claude_code_version"`
	}
	if !isObject(payload) || json.Unmarshal(payload, &p) != nil {
		return fmt.Errorf("%w (its initialize answer has no payload)", ErrNotStarted)
	}
	if p.Version == nil || !versionAtLeast(*p.Version, ClaudeMinVersion) {
		v := "none"
		if p.Version != nil {
			v = *p.Version
		}
		return fmt.Errorf("%w (version %s; a chat needs %s or later)", ErrNotQualified, cutWords(v), ClaudeMinVersion)
	}
	var list []json.RawMessage
	if json.Unmarshal(pending, &list) != nil || pending == nil || bytes.Equal(bytes.TrimSpace(pending), []byte("null")) {
		return fmt.Errorf("%w (its initialize answer lacks pending_permission_requests)", ErrNotStarted)
	}
	return nil
}

func isObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// system is a system event: the child's init (its conversation id and its
// capabilities), a call the permission rules denied, background work, and
// a retry that says the agent is not signed in.
func (c *Claude) system(ev claudeEvent, e session.ProtocolEvents) error {
	r := &c.r
	switch ev.Subtype {
	case "init":
		if ev.SessionID != "" && ev.SessionID != r.conv && conversationShape(ev.SessionID) {
			r.conv = ev.SessionID
			e.Conversation(ev.SessionID)
		}
		receipt := false
		for _, cap := range ev.Capabilities {
			receipt = receipt || cap == "interrupt_receipt_v1"
		}
		c.mu.Lock()
		c.receipt = receipt
		c.mu.Unlock()
	case "permission_denied":
		if ev.ToolUseID != "" && !r.denied[ev.ToolUseID] {
			r.denied[ev.ToolUseID] = true
			r.deniedQ = append(r.deniedQ, ev.ToolUseID)
			if len(r.deniedQ) > maxDenied {
				delete(r.denied, r.deniedQ[0])
				r.deniedQ = r.deniedQ[1:]
			}
		}
	case "task_started":
		if !ev.Ambient && ev.TaskID != "" && len(r.bg) < maxBackground {
			r.bg[ev.TaskID] = true
		}
		c.background(e)
	case "task_notification":
		delete(r.bg, ev.TaskID)
		c.background(e)
	case "task_updated":
		var p struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(ev.Patch, &p) == nil {
			switch p.Status {
			case "completed", "failed", "killed":
				delete(r.bg, ev.TaskID)
			}
		}
		c.background(e)
	case "background_tasks_changed":
		var tasks []struct {
			TaskID  string `json:"task_id"`
			Ambient bool   `json:"ambient"`
		}
		if json.Unmarshal(ev.Tasks, &tasks) == nil {
			clear(r.bg)
			for _, t := range tasks {
				if !t.Ambient && t.TaskID != "" && len(r.bg) < maxBackground {
					r.bg[t.TaskID] = true
				}
			}
		}
		c.background(e)
	case "api_retry":
		var category string
		_ = json.Unmarshal(ev.Error, &category)
		if !r.answered && (ev.ErrorStatus != nil && *ev.ErrorStatus == 401 || category == "authentication_failed") {
			return c.notSignedIn(e)
		}
	}
	return nil
}

// background tells the session when background work starts or stops
// running.
func (c *Claude) background(e session.ProtocolEvents) {
	on := len(c.r.bg) > 0
	if on != c.r.bgOn {
		c.r.bgOn = on
		e.Background(on)
	}
}

// notSignedIn ends the read: the agent is not signed in.
func (c *Claude) notSignedIn(e session.ProtocolEvents) error {
	e.Notice(NoticeNotSignedIn, NotSignedInWords)
	return ErrNotSignedIn
}

// startTurn tells the session a turn began, once per turn.
func (c *Claude) startTurn(e session.ProtocolEvents) {
	if !c.r.inTurn {
		c.r.inTurn = true
		e.TurnStarted("")
	}
}

// streamEvent is the main thread's partial message: its text, streamed.
func (c *Claude) streamEvent(ev claudeEvent, e session.ProtocolEvents) {
	if ev.ParentToolUseID != nil && *ev.ParentToolUseID != "" {
		return
	}
	var se struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(ev.Event, &se) != nil {
		return
	}
	r := &c.r
	switch se.Type {
	case "message_start":
		c.startTurn(e)
		r.streamMsg = se.Message.ID
	case "content_block_delta":
		if se.Delta.Type != "text_delta" || se.Delta.Text == "" || r.streamMsg == "" {
			return
		}
		c.startTurn(e)
		ref := textRef(r.streamMsg, se.Index)
		if _, open := r.streamed[ref]; !open && len(r.streamed) >= maxStreamed {
			return
		}
		r.streamed[ref] += len(se.Delta.Text)
		e.Text(ref, se.Delta.Text, false)
	}
}

func textRef(msg string, index int) string { return msg + ":" + strconv.Itoa(index) }

// assistantMessage is the parts of an assistant message the driver reads.
type assistantMessage struct {
	ID      string `json:"id"`
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
}

// assistant is a complete assistant message: one block a line, its text
// closing what was streamed of it, its tool calls announced with their
// whole input. A subagent's text is not the chat's; its calls are, under
// the step that started it.
func (c *Claude) assistant(ev claudeEvent, e session.ProtocolEvents) {
	r := &c.r
	var category string
	_ = json.Unmarshal(ev.Error, &category)
	if category == "authentication_failed" {
		r.authFailed = true
	}
	var m assistantMessage
	if json.Unmarshal(ev.Message, &m) != nil {
		return
	}
	parent := ""
	if ev.ParentToolUseID != nil {
		parent = *ev.ParentToolUseID
	}
	if parent == "" {
		c.startTurn(e)
	}
	if m.ID != r.asstMsg {
		r.asstMsg, r.asstIdx = m.ID, 0
	}
	for _, b := range m.Content {
		i := r.asstIdx
		r.asstIdx++
		switch b.Type {
		case "text":
			if parent != "" || m.ID == "" {
				continue
			}
			ref := textRef(m.ID, i)
			streamed, was := r.streamed[ref]
			if !was && b.Text == "" {
				continue
			}
			delete(r.streamed, ref)
			rest := b.Text
			switch {
			case !was:
			case streamed <= len(b.Text):
				rest = b.Text[streamed:]
			default:
				rest = ""
			}
			e.Text(ref, rest, true)
		case "tool_use":
			if b.ID == "" {
				continue
			}
			if _, open := r.tools[b.ID]; !open {
				r.toolSeq = append(r.toolSeq, b.ID)
				if len(r.toolSeq) > maxOpenTools {
					delete(r.tools, r.toolSeq[0])
					r.toolSeq = r.toolSeq[1:]
				}
			}
			r.tools[b.ID] = b.Name
			e.ToolStarted(session.ToolStep{Ref: b.ID, Parent: parent, Name: b.Name, Input: bytes.Clone(b.Input), Summary: toolSummary(b.Name, b.Input)})
		}
	}
}

// toolSummary is a step's one line where the driver knows the tool: a
// command's command line, a file tool's path; else "" (the session makes
// one from the input).
func toolSummary(name string, input json.RawMessage) string {
	var in struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	switch name {
	case "Bash":
		return in.Command
	case "Read", "Edit", "MultiEdit", "Write":
		return in.FilePath
	case "NotebookEdit":
		return in.NotebookPath
	}
	return ""
}

// user is a user line: the replay of a message the driver wrote (its
// acknowledgement), or the results of tool calls.
func (c *Claude) user(ev claudeEvent, e session.ProtocolEvents) {
	if ev.IsReplay {
		c.replay(ev.UUID, e)
		return
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(ev.Message, &m) != nil {
		return
	}
	var blocks []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	results := 0
	for _, b := range blocks {
		if b.Type == "tool_result" {
			results++
		}
	}
	for _, b := range blocks {
		if b.Type != "tool_result" || b.ToolUseID == "" {
			continue
		}
		// The structured result is the message's, so it is read only when
		// the message holds one result.
		var structured json.RawMessage
		if results == 1 {
			structured = ev.ToolUseResult
		}
		c.toolResult(b.ToolUseID, b.IsError, b.Content, structured, e)
	}
}

// replay is a message's replay: Accepted for the message it names, or for
// the oldest written one when it names none. A replay that names a message
// the driver did not write (one the agent took from elsewhere) accepts
// nothing. Never matched by time.
func (c *Claude) replay(uuid string, e session.ProtocolEvents) {
	ref, found := "", false
	c.mu.Lock()
	for i, s := range c.sends {
		if uuid == "" || s.uuid == uuid {
			ref, found = s.ref, true
			c.sends = append(c.sends[:i], c.sends[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	if found {
		e.Accepted(ref)
	}
	c.startTurn(e)
}

// toolResult ends a step: its status (denied where a permission_denied
// named it, interrupted where the command was), its output, and the file
// a file tool changed, by the tool's own structured patch.
func (c *Claude) toolResult(id string, isError bool, content, structured json.RawMessage, e session.ProtocolEvents) {
	r := &c.r
	name := r.tools[id]
	delete(r.tools, id)
	status := session.ToolOK
	switch {
	case r.denied[id]:
		status = session.ToolDenied
		delete(r.denied, id)
	case isError:
		status = session.ToolError
	}
	var sr fileResult
	_ = json.Unmarshal(structured, &sr)
	if sr.Interrupted && status == session.ToolOK {
		status = session.ToolInterrupted
	}
	e.ToolEnded(id, session.ToolResult{Status: status, Output: resultText(content)})
	if status == session.ToolOK {
		if f, ok := fileChange(id, name, sr); ok {
			e.FileChanged(f)
		}
	}
}

// fileResult is the parts of a tool's structured result the driver reads:
// a file tool's path, kind and patch, a command's interruption.
type fileResult struct {
	Type            string `json:"type"`
	FilePath        string `json:"filePath"`
	Content         string `json:"content"`
	StructuredPatch []struct {
		OldStart int      `json:"oldStart"`
		OldLines int      `json:"oldLines"`
		NewStart int      `json:"newStart"`
		NewLines int      `json:"newLines"`
		Lines    []string `json:"lines"`
	} `json:"structuredPatch"`
	Interrupted bool `json:"interrupted"`
}

// fileChange is the file a file tool's result says it changed: its path,
// create or edit, the lines its patch adds and removes, and the patch as a
// diff.
func fileChange(id, tool string, r fileResult) (session.FileChange, bool) {
	switch tool {
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
	default:
		return session.FileChange{}, false
	}
	if r.FilePath == "" {
		return session.FileChange{}, false
	}
	f := session.FileChange{Tool: id, Path: r.FilePath, Op: session.ChangeEdit}
	if r.Type == "create" {
		f.Op = session.ChangeCreate
	}
	var diff strings.Builder
	for _, h := range r.StructuredPatch {
		fmt.Fprintf(&diff, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				f.Added++
			case strings.HasPrefix(l, "-"):
				f.Removed++
			}
			diff.WriteString(l)
			diff.WriteByte('\n')
		}
	}
	if len(r.StructuredPatch) == 0 && f.Op == session.ChangeCreate && r.Content != "" {
		// A file written whole: every line is added.
		f.Added = strings.Count(strings.TrimSuffix(r.Content, "\n"), "\n") + 1
	}
	if diff.Len() > 0 {
		f.Diff = []byte(diff.String())
	}
	return f, true
}

// resultText is a tool result's content as text: a string as it is, text
// blocks joined, anything else as its JSON.
func resultText(content json.RawMessage) []byte {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return []byte(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var b strings.Builder
		for i, bl := range blocks {
			if bl.Type != "text" {
				continue
			}
			if i > 0 && b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(bl.Text)
		}
		return []byte(b.String())
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	return bytes.Clone(content)
}

// result is a turn's end: its outcome, its usage, the texts still open
// closed, and, past MaxClaudeInput written, the restart.
func (c *Claude) result(ev claudeEvent, e session.ProtocolEvents) error {
	r := &c.r
	authFailed := r.authFailed || ev.IsError && ev.APIErrorStatus != nil && *ev.APIErrorStatus == 401
	r.authFailed = false
	if authFailed && !r.answered {
		return c.notSignedIn(e)
	}
	for _, ref := range slices.Sorted(maps.Keys(r.streamed)) {
		e.Text(ref, "", true)
	}
	clear(r.streamed)
	c.mu.Lock()
	interrupting := c.interrupting
	c.interrupting = false
	c.mu.Unlock()
	outcome := "done"
	switch {
	case ev.TerminalReason == "aborted_streaming" || ev.TerminalReason == "aborted_tools" || interrupting && ev.Subtype == "error_during_execution":
		outcome = "interrupted"
	case ev.Subtype == "error_max_turns" || ev.Subtype == "error_max_budget_usd" || ev.TerminalReason == "max_turns" || ev.TerminalReason == "budget_exhausted":
		outcome = "limit"
	case ev.Subtype != "success" || ev.IsError:
		outcome = "error"
	}
	u := session.Usage{
		InputTokens:  ev.Usage.InputTokens + ev.Usage.CacheCreation + ev.Usage.CacheRead,
		OutputTokens: ev.Usage.OutputTokens,
	}
	// total_cost_usd counts the child's whole life (a resumed child's
	// first result counts the conversation's earlier calls too): a turn's
	// cost is what it added, and the child's first turn reports none.
	if r.costKnown && ev.TotalCostUSD >= r.lastCost {
		u.CostUSD = ev.TotalCostUSD - r.lastCost
	}
	r.lastCost, r.costKnown = ev.TotalCostUSD, true
	if outcome == "done" {
		r.answered = true
	}
	r.inTurn = false
	// What was written by this turn's end decides, before the session is
	// told: a message it writes on the end is the next turn's.
	if c.written.Load() > c.max && c.restart != nil && !r.restarted {
		r.restarted = true
		c.restart()
	}
	e.TurnEnded(outcome, u)
	return nil
}

// toolIDInLine finds the step a cut line is a result of.
var toolIDInLine = regexp.MustCompile(`"tool_use_id"\s*:\s*"([^"\\]{1,128})"`)

// tooLarge is a line past session.MaxProtocolLine, of which head is the
// start: the step whose result it holds ends with its output cut, and a
// notice says so.
func (c *Claude) tooLarge(head []byte, e session.ProtocolEvents) {
	e.Notice(NoticeTooLarge, TooLargeWords)
	m := toolIDInLine.FindSubmatch(head)
	if m == nil {
		return
	}
	id := string(m[1])
	if _, open := c.r.tools[id]; !open {
		return
	}
	delete(c.r.tools, id)
	status := session.ToolOK
	if bytes.Contains(head, []byte(`"is_error":true`)) {
		status = session.ToolError
	}
	e.ToolEnded(id, session.ToolResult{Status: status, Cut: true})
}
