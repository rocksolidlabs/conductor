package agentproto

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// acpSession is the session the ACP tests take up.
const acpSession = "ses_0123456789abcdef"

// acpTestKeys are the raw input's keys the tests teach the client, as an
// agent's own look would record them.
var acpTestKeys = []string{"command", "path"}

// acpServer is a fake ACP agent on a client's pipes: it answers initialize
// and the session's resume or load, and a test writes the rest.
type acpServer struct {
	t     *testing.T
	out   *io.PipeWriter
	mu    sync.Mutex
	lines []string
	got   chan struct{}
	// init is the initialize answer's result, take the session's answer
	// ("" for {}), noAnswer leaves initialize unanswered.
	init     string
	take     string
	noAnswer bool
	queue    chan string
	done     chan struct{}
	wmu      sync.Mutex
	closed   bool
}

const acpInit = `{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"sessionCapabilities":{"resume":{}}},"authMethods":[]}`

// acpPipes is a client on a fake agent; its Read is the caller's.
func acpPipes(t *testing.T, opts ACPOptions) (*ACP, *acpServer) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &acpServer{t: t, out: outW, got: make(chan struct{}, 1024), init: acpInit, queue: make(chan string, 1024), done: make(chan struct{})}
	if opts.Session == "" {
		opts.Session = acpSession
	}
	if opts.Cwd == "" {
		opts.Cwd = "/work"
	}
	if opts.RawInputKeys == nil {
		opts.RawInputKeys = acpTestKeys
	}
	c := NewACP(inW, outR, opts)
	go s.serve(inR)
	go func() {
		for {
			select {
			case v := <-s.queue:
				s.write(v)
			case <-s.done:
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = c.CloseInput()
		close(s.done)
		s.wmu.Lock()
		s.closed = true
		_ = outW.Close()
		s.wmu.Unlock()
	})
	return c, s
}

// newACPServer is a client on a fake agent, its Read running into rec.
func newACPServer(t *testing.T, rec *recorder, opts ACPOptions) (*ACP, *acpServer) {
	t.Helper()
	read := make(chan error, 1)
	t.Cleanup(func() { <-read })
	c, s := acpPipes(t, opts)
	go func() { read <- c.Read(context.Background(), rec) }()
	return c, s
}

func (s *acpServer) write(v string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed {
		return
	}
	if _, err := io.WriteString(s.out, v+"\n"); err != nil {
		s.t.Errorf("the fake agent's write: %v", err)
	}
}

func (s *acpServer) answer(v string) {
	select {
	case s.queue <- v:
	case <-s.done:
	}
}

func (s *acpServer) serve(in io.Reader) {
	sc := bufio.NewScanner(in)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		s.mu.Lock()
		s.lines = append(s.lines, line)
		s.mu.Unlock()
		s.got <- struct{}{}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal([]byte(line), &m)
		if m.ID == nil || m.Method == "" {
			continue
		}
		switch m.Method {
		case "initialize":
			if !s.noAnswer {
				s.answer(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":` + s.init + `}`)
			}
		case "session/resume", "session/load":
			take := s.take
			if take == "" {
				take = `"result":{}`
			}
			s.answer(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,` + take + `}`)
		}
	}
}

func (s *acpServer) written() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lines)
}

func (s *acpServer) await(want string) string {
	s.t.Helper()
	deadline := time.After(time.Until(testDeadline(s.t)))
	for {
		for _, l := range s.written() {
			if strings.Contains(l, want) {
				return l
			}
		}
		select {
		case <-s.got:
		case <-deadline:
			s.t.Fatalf("the client wrote no %q; it wrote\n%s", want, strings.Join(s.written(), "\n"))
		}
	}
}

// promptID is the id of the client's last session/prompt.
func (s *acpServer) promptID() string {
	s.t.Helper()
	line := ""
	for _, l := range s.written() {
		if strings.Contains(l, `"session/prompt"`) {
			line = l
		}
	}
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal([]byte(line), &m) != nil || m.ID == nil {
		s.t.Fatalf("no session/prompt in %q", s.written())
	}
	return string(m.ID)
}

func acpUpdate(u string) string {
	return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + acpSession + `","update":` + u + `}}`
}

// begun is a client that has taken up the session and has a turn under way
// (a message sent and the agent's first update), its events in rec.
func begun(t *testing.T, opts ACPOptions) (*ACP, *acpServer, *recorder) {
	t.Helper()
	rec := &recorder{}
	c, s := newACPServer(t, rec, opts)
	ctx := context.Background()
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, session.Message{Ref: "m1", Text: "go"}); err != nil {
		t.Fatal(err)
	}
	s.await(`"session/prompt"`)
	s.write(acpUpdate(`{"sessionUpdate":"agent_message_chunk","messageId":"a1","content":{"type":"text","text":"on it"}}`))
	waitEvent(t, rec, "accepted", 1)
	return c, s, rec
}

// The client speaks protocol 1's baseline alone: initialize advertises no
// file system and no terminal capability, names conductor, and every
// fs/* and terminal/* request (and any other the client did not advertise)
// is answered -32601, never served.
func TestACPBaselineOnly(t *testing.T) {
	_, s, _ := begun(t, ACPOptions{})
	init := s.await(`"initialize"`)
	var m struct {
		Params struct {
			ProtocolVersion    int            `json:"protocolVersion"`
			ClientCapabilities map[string]any `json:"clientCapabilities"`
			ClientInfo         map[string]any `json:"clientInfo"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(init), &m); err != nil {
		t.Fatal(err)
	}
	fs, _ := m.Params.ClientCapabilities["fs"].(map[string]any)
	if m.Params.ProtocolVersion != 1 || fs["readTextFile"] != false || fs["writeTextFile"] != false || m.Params.ClientCapabilities["terminal"] != false || m.Params.ClientInfo["name"] != "conductor" {
		t.Fatalf("initialize %s", init)
	}
	for k := range m.Params.ClientCapabilities {
		if k != "fs" && k != "terminal" {
			t.Fatalf("a capability beyond the baseline: %s", k)
		}
	}
	for i, method := range []string{"fs/read_text_file", "fs/write_text_file", "terminal/create", "terminal/output", "elicitation/create", "x/unknown"} {
		id := 900 + i
		s.write(`{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"` + method + `","params":{"sessionId":"` + acpSession + `","path":"/etc/passwd"}}`)
		got := s.await(`"id":` + itoa(id) + `,`)
		if !strings.Contains(got, `"code":-32601`) || strings.Contains(got, `"result"`) {
			t.Fatalf("%s answered %s", method, got)
		}
	}
	resume := s.await(`"session/resume"`)
	if !strings.Contains(resume, `"sessionId":"`+acpSession+`"`) || !strings.Contains(resume, `"cwd":"/work"`) || !strings.Contains(resume, `"mcpServers":[]`) {
		t.Fatalf("the session taken up with %s", resume)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// An Allow selects the offered allow_once option by its own id (OpenCode's
// "once", not the kind), a Deny the reject_once option's ("reject"), a
// withdrawal cancelled; an always option is never selected, and a request
// that offers no allow_once is declined at once, never asked.
func TestACPPermissionOptions(t *testing.T) {
	c, s, rec := begun(t, ACPOptions{})
	opencode := `[{"optionId":"once","name":"Allow once","kind":"allow_once"},{"optionId":"always","name":"Always allow","kind":"allow_always"},{"optionId":"reject","name":"Reject","kind":"reject_once"}]`
	ask := func(id, call string, options string) {
		s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"` + call + `","title":"ls","kind":"execute","status":"pending","rawInput":{"command":"ls"}}`))
		s.write(`{"jsonrpc":"2.0","id":` + id + `,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"` + call + `"},"options":` + options + `}}`)
	}
	ask("11", "c1", opencode)
	ask("12", "c2", opencode)
	ask("13", "c3", opencode)
	perms := waitEvent(t, rec, "permission", 3)
	for i, v := range []session.Verdict{{Allow: true}, {Message: "no"}, {Cancel: true}} {
		p := perms[i].perm
		if p.Call != "c"+itoa(i+1) || p.Tool != "execute" || string(p.Input) != `{"kind":"execute","rawInput":{"command":"ls"},"title":"ls"}` {
			t.Fatalf("request %d: %+v %s", i, p, p.Input)
		}
		if err := c.Reply(context.Background(), perms[i].ref, v); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]string{"11": `{"optionId":"once","outcome":"selected"}`, "12": `{"optionId":"reject","outcome":"selected"}`, "13": `{"outcome":"cancelled"}`} {
		got := s.await(`"id":` + id + `,`)
		if !strings.Contains(got, `"outcome":`+want) {
			t.Fatalf("request %s answered %s, want %s", id, got, want)
		}
	}
	// Only always and reject offered: no Allow can be kept, declined at once.
	ask("14", "c4", `[{"optionId":"always","name":"Always","kind":"allow_always"},{"optionId":"reject","name":"Reject","kind":"reject_once"}]`)
	if got := s.await(`"id":14,`); !strings.Contains(got, `{"optionId":"reject","outcome":"selected"}`) {
		t.Fatalf("a request without allow_once answered %s", got)
	}
	if len(rec.kinds("permission")) != 3 {
		t.Fatal("a request without allow_once was asked")
	}
	for _, l := range s.written() {
		if strings.Contains(l, `"always"`) || strings.Contains(l, "allow_always") || strings.Contains(l, `"allow_once"`) {
			t.Fatalf("an option's kind or an always option was selected: %s", l)
		}
	}
}

// Stop of a turn sends session/cancel and answers every pending permission
// request cancelled (the protocol's MUST), never a reject_once selection;
// the session's later withdrawal writes nothing more; the turn ends
// interrupted at the prompt's answer, stopReason cancelled.
func TestACPCancelAnswersPending(t *testing.T) {
	c, s, rec := begun(t, ACPOptions{})
	for _, id := range []string{"21", "22"} {
		call := "c" + id
		s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"` + call + `","title":"x","kind":"edit","status":"pending","rawInput":{"path":"a"}}`))
		s.write(`{"jsonrpc":"2.0","id":` + id + `,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"` + call + `"},"options":[{"optionId":"o1","name":"Allow","kind":"allow_once"},{"optionId":"r1","name":"Reject","kind":"reject_once"}]}}`)
	}
	perms := waitEvent(t, rec, "permission", 2)
	if err := c.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel := s.await(`"session/cancel"`)
	if !strings.Contains(cancel, `"sessionId":"`+acpSession+`"`) || strings.Contains(cancel, `"id"`) {
		t.Fatalf("session/cancel %s", cancel)
	}
	for _, id := range []string{"21", "22"} {
		if got := s.await(`"id":` + id + `,`); got != `{"id":`+id+`,"jsonrpc":"2.0","result":{"outcome":{"outcome":"cancelled"}}}` {
			t.Fatalf("request %s answered %s", id, got)
		}
	}
	n := len(s.written())
	for _, p := range perms {
		_ = c.Reply(context.Background(), p.ref, session.Verdict{Cancel: true})
	}
	s.write(`{"jsonrpc":"2.0","id":` + s.promptID() + `,"result":{"stopReason":"cancelled"}}`)
	if e := waitEvent(t, rec, "turn_ended", 1); e[0].out != "interrupted" {
		t.Fatalf("the turn ended %q", e[0].out)
	}
	for _, l := range s.written()[n:] {
		if strings.Contains(l, `"result"`) {
			t.Fatalf("a request answered twice: %s", l)
		}
	}
	for _, l := range s.written() {
		if strings.Contains(l, `"r1"`) {
			t.Fatalf("a reject_once selected on a cancel: %s", l)
		}
	}
}

// An agent's $/cancel_request takes its request back: the session is told
// (PermissionWithdrawn), the request is answered still, with -32800 as the
// protocol asks, and the session's later reply writes nothing.
func TestACPCancelRequestWithdraws(t *testing.T) {
	c, s, rec := begun(t, ACPOptions{})
	s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"c1","title":"ls","kind":"execute","status":"pending","rawInput":{"command":"ls"}}`))
	s.write(`{"jsonrpc":"2.0","id":"q-1","method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"c1"},"options":[{"optionId":"o1","name":"Allow","kind":"allow_once"}]}}`)
	p := waitEvent(t, rec, "permission", 1)[0]
	s.write(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":"q-1"}}`)
	if w := waitEvent(t, rec, "permission_withdrawn", 1); w[0].ref != p.ref {
		t.Fatalf("withdrawn %+v", w)
	}
	if got := s.await(`"id":"q-1"`); !strings.Contains(got, `"code":-32800`) {
		t.Fatalf("the taken-back request answered %s", got)
	}
	n := len(s.written())
	_ = c.Reply(context.Background(), p.ref, session.Verdict{Allow: true})
	_ = c.Interrupt(context.Background())
	s.await(`"session/cancel"`)
	for _, l := range s.written()[n:] {
		if strings.Contains(l, `"q-1"`) {
			t.Fatalf("a taken-back request answered again: %s", l)
		}
	}
}

// The client's Verbatim is its declaration (acpVerbatim, false: the
// protocol runs an agent's slash commands from the prompt's text), so the
// chat's box draws "Messages starting with / may run as the agent's
// commands."; the agent's advertised "compact" sent as "/compact" reaches it
// as one text block, unchanged.
func TestACPVerbatimIsDeclared(t *testing.T) {
	rec := &recorder{}
	c, s := newACPServer(t, rec, ACPOptions{})
	if c.Verbatim() || acpVerbatim {
		t.Fatal("an ACP chat claims messages go as written")
	}
	ctx := context.Background()
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	s.write(acpUpdate(`{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"compact","description":"Compact"}]}`))
	if err := c.Send(ctx, session.Message{Ref: "m1", Text: "/compact"}); err != nil {
		t.Fatal(err)
	}
	line := s.await(`"session/prompt"`)
	var m struct {
		Params struct {
			Prompt []map[string]any `json:"prompt"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(line), &m) != nil || len(m.Params.Prompt) != 1 || len(m.Params.Prompt[0]) != 2 || m.Params.Prompt[0]["type"] != "text" || m.Params.Prompt[0]["text"] != "/compact" {
		t.Fatalf("sent %s", line)
	}
}

// The gate: an answer of another protocol version, no answer, an agent that
// can take up no session, and a session refused are ErrACPNotStarted; a
// session refused auth_required is ErrACPNotSignedIn, back to the
// terminal; session/resume is taken where offered, else session/load, whose
// replayed history is not this chat's.
func TestACPGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*acpServer)
		want error
	}{
		{"protocol 2", func(s *acpServer) { s.init = `{"protocolVersion":2,"agentCapabilities":{"loadSession":true}}` }, ErrACPNotStarted},
		{"no answer", func(s *acpServer) { s.noAnswer = true }, ErrACPNotStarted},
		{"no session to take up", func(s *acpServer) { s.init = `{"protocolVersion":1,"agentCapabilities":{}}` }, ErrACPNotStarted},
		{"a session refused", func(s *acpServer) { s.take = `"error":{"code":-32602,"message":"no such session"}` }, ErrACPNotStarted},
		{"not signed in", func(s *acpServer) { s.take = `"error":{"code":-32000,"message":"Authentication required"}` }, ErrACPNotSignedIn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			opts := ACPOptions{}
			if tc.name == "no answer" {
				// The client's own bound is what this case is about.
				opts.InitWait = 300 * time.Millisecond
			}
			c, s := newACPServer(t, rec, opts)
			tc.edit(s)
			err := c.Begin(context.Background())
			if !errors.Is(err, tc.want) || StartWords(err) == "" {
				t.Fatalf("Begin: %v, want %v", err, tc.want)
			}
		})
	}
	// Load, with its history replayed before its answer: none of it shown.
	rec := &recorder{}
	c, s := newACPServer(t, rec, ACPOptions{})
	s.init = `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`
	s.take = `"result":{}`
	if err := c.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(s.written(), "\n"), "session/resume") {
		t.Fatal("resume sent to an agent that offers load alone")
	}
	if ev := rec.kinds("conversation"); len(ev) != 1 || ev[0].ref != acpSession {
		t.Fatalf("the conversation %+v", ev)
	}
	if len(rec.kinds("text")) != 0 {
		t.Fatal("a load's history was shown")
	}
	for v, ok := range map[string]bool{"1.0.0": true, "": false} {
		if Qualified(ACPv1, v) != ok {
			t.Errorf("Qualified(%q) = %v", v, !ok)
		}
	}
}

// The argv of a chat's child of an ACP agent: the command and the protocol
// recipe's start of its ACP server, nothing else; a terminal launched with
// arguments of its own, a model flag or an added folder is refused.
func TestACPArgvIsTheAgentsOwnStart(t *testing.T) {
	for _, tc := range []struct {
		l    ACPLaunch
		want []string
	}{
		{ACPLaunch{Command: []string{"opencode"}, Protocol: []string{"acp"}, Conversation: acpSession}, []string{"opencode", "acp"}},
		{ACPLaunch{Command: []string{"copilot"}, Protocol: []string{"--acp"}, Conversation: acpSession}, []string{"copilot", "--acp"}},
		{ACPLaunch{Command: []string{"dsh"}, Protocol: []string{"--profile", "acp"}, Conversation: acpSession}, []string{"dsh", "--profile", "acp"}},
	} {
		got, err := ACPArgs(tc.l)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%+v: %q %v", tc.l, got, err)
		}
	}
	var argErr *ACPArgError
	if _, err := ACPArgs(ACPLaunch{Command: []string{"goose"}, Protocol: []string{"acp"}, Conversation: acpSession, LaunchArgs: []string{"--debug"}}); !errors.As(err, &argErr) || argErr.Flag != "--debug" {
		t.Errorf("a launch argument: %v", err)
	}
	if _, err := ACPArgs(ACPLaunch{Command: []string{"goose"}, Protocol: []string{"acp"}, Conversation: acpSession, Model: []string{"--model=x"}}); !errors.As(err, &argErr) {
		t.Errorf("a model flag: %v", err)
	}
	if _, err := ACPArgs(ACPLaunch{Command: []string{"goose"}, Protocol: []string{"acp"}, Conversation: acpSession, LaunchArgs: []string{"--add-dir=/x"}}); !errors.Is(err, ErrAddedFolder) {
		t.Errorf("an added folder: %v", err)
	}
	for _, bad := range []ACPLaunch{{Protocol: []string{"acp"}, Conversation: acpSession}, {Command: []string{"goose"}, Conversation: acpSession}, {Command: []string{"goose"}, Protocol: []string{"acp"}}} {
		if argv, err := ACPArgs(bad); err == nil {
			t.Errorf("%+v made %q", bad, argv)
		}
	}
}

// Each hand-built fixture fed to Read gives its expected events: a turn's
// words, an execute step with its output, an edit with its diff and counts,
// a permission request bound to its call, another session's update and an
// update between turns ignored, an unknown update ignored, a stop reason's
// outcome. (The client's writes follow "--- written".)
func TestACPFixturesReplay(t *testing.T) {
	for _, name := range []string{"hand-built-turn", "hand-built-permission"} {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", "acp", name+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			w := &discard{}
			c := NewACP(w, f, ACPOptions{Session: acpSession, Cwd: "/work", RawInputKeys: acpTestKeys})
			c.sessionID = acpSession
			c.promptRef = "m1"
			c.nextID = 2
			c.calls["2"] = &acpCall{method: "session/prompt", answer: make(chan codexAnswer, 1)}
			rec := &recorder{}
			rec.after = func(e event) {
				if e.kind == "permission" {
					_ = c.Reply(context.Background(), e.ref, session.Verdict{Allow: true})
				}
			}
			if err := c.Read(context.Background(), rec); !errors.Is(err, io.EOF) {
				t.Fatalf("Read: %v", err)
			}
			got := rec.render() + "--- written\n" + w.String()
			path := filepath.Join("testdata", "acp", name+".events")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("events\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// What the client does not read is declined, never asked without it: a
// request about a call announced with a field the client does not read, or
// changed by an update since it was announced, one whose own toolCall holds
// such a field, and one whose params do. Each is answered at once (never an
// allow) with a notice, and none reaches the session; a request whose
// toolCall gives the call's fields its own values is handed over with them,
// for the session's binding to judge.
func TestACPWhatTheClientDoesNotReadIsDeclined(t *testing.T) {
	_, s, rec := begun(t, ACPOptions{})
	opts := `[{"optionId":"o1","name":"Allow","kind":"allow_once"},{"optionId":"r1","name":"Reject","kind":"reject_once"}]`
	call := func(id, extra string) {
		s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"` + id + `","title":"ls","kind":"execute","status":"pending","rawInput":{"command":"ls"}` + extra + `}`))
	}
	ask := func(n int, toolCall, extra string) {
		s.write(`{"jsonrpc":"2.0","id":` + itoa(n) + `,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":` + toolCall + `,"options":` + opts + extra + `}}`)
	}
	call("a1", `,"somethingNew":{"x":1}`)
	call("a2", "")
	s.write(acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"a2","status":"in_progress","content":[{"type":"diff","path":"/work/x","oldText":"a","newText":"b"}]}`))
	call("a3", "")
	call("a4", "")
	call("a5", "")
	ask(61, `{"toolCallId":"a1"}`, "")
	ask(62, `{"toolCallId":"a2"}`, "")
	ask(63, `{"toolCallId":"a3","somethingNew":true}`, "")
	ask(64, `{"toolCallId":"a4"}`, `,"somethingNew":true`)
	for _, id := range []string{"61", "62", "63", "64"} {
		got := s.await(`"id":` + id + `,`)
		if !strings.Contains(got, `"outcome"`) || strings.Contains(got, `"o1"`) {
			t.Fatalf("request %s answered %s", id, got)
		}
	}
	// Each decline's notice follows its answer: once all are told, every
	// request has been handled.
	if n := len(waitEvent(t, rec, "notice", 4)); n != 4 {
		t.Fatalf("%d notices for 4 declines", n)
	}
	if n := len(rec.kinds("permission")); n != 0 {
		t.Fatalf("%d declined requests reached the session", n)
	}
	ask(65, `{"toolCallId":"a5","rawInput":{"command":"rm -rf /"}}`, "")
	p := waitEvent(t, rec, "permission", 1)[0].perm
	if p.Call != "a5" || string(p.Input) != `{"kind":"execute","rawInput":{"command":"rm -rf /"},"title":"ls"}` {
		t.Fatalf("handed over %+v %s", p, p.Input)
	}
}

// A verdict on a request the client no longer holds reaches nobody:
// ErrNoRequest, so the session records it withdrawn.
func TestACPAVerdictOnARequestTakenBackReachesNobody(t *testing.T) {
	c, s, rec := begun(t, ACPOptions{})
	s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"c1","title":"ls","kind":"execute","status":"pending","rawInput":{"command":"ls"}}`))
	s.write(`{"jsonrpc":"2.0","id":71,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"c1"},"options":[{"optionId":"o1","name":"Allow","kind":"allow_once"}]}}`)
	p := waitEvent(t, rec, "permission", 1)[0]
	s.write(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":71}}`)
	waitEvent(t, rec, "permission_withdrawn", 1)
	if err := c.Reply(context.Background(), p.ref, session.Verdict{Allow: true}); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("Reply: %v", err)
	}
}

const acpOpts = `[{"optionId":"o1","name":"Allow","kind":"allow_once"},{"optionId":"r1","name":"Reject","kind":"reject_once"}]`

// askAbout announces the call id with input and asks about it under the
// request id n, and returns the request as the session was handed it.
func askAbout(t *testing.T, s *acpServer, rec *recorder, n int, id, input string, before int) event {
	t.Helper()
	s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"` + id + `","title":"t","kind":"execute","status":"pending","rawInput":` + input + `}`))
	s.write(`{"jsonrpc":"2.0","id":` + itoa(n) + `,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"` + id + `"},"options":` + acpOpts + `}}`)
	return waitEvent(t, rec, "permission", before+1)[before]
}

// A call that changes closes its card: an update after the request that
// gives a shown field another value (a number written another way too) or
// holds only a field the client does not read, a repeated announcement with
// other fields, an update that sets the call running, and a line the client
// could not read (too long, a key twice, not UTF-8, a lone surrogate) each
// withdraw the open request (the session is told, the
// agent answered cancelled), and a later verdict on it writes nothing, so
// allow_once is never selected; a later request about a changed call is
// declined.
func TestACPACallThatChangesClosesItsCard(t *testing.T) {
	c, s, rec := begun(t, ACPOptions{})
	changes := []struct {
		name, id, line string
		input          string
	}{
		{"an update after the request", "c1", acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"c1","rawInput":{"command":"cat /etc/passwd"}}`), ""},
		{"a repeated announcement", "c2", acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"c2","title":"t","kind":"execute","status":"pending","rawInput":{"command":"cat /etc/passwd"}}`), ""},
		{"the call set running", "c3", acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"c3","status":"in_progress"}`), ""},
		{"a line too long to read", "c4", `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + acpSession + `","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + strings.Repeat("a", session.MaxProtocolLine) + `"}}}}`, ""},
	}
	for i, tc := range changes {
		input := tc.input
		if input == "" {
			input = `{"command":"ls"}`
		}
		p := askAbout(t, s, rec, 80+i, tc.id, input, i)
		s.write(tc.line)
		w := waitEvent(t, rec, "permission_withdrawn", i+1)[i]
		if w.ref != p.ref {
			t.Fatalf("%s: withdrew %s, want %s", tc.name, w.ref, p.ref)
		}
		if got := s.await(`"id":` + itoa(80+i) + `,`); got != `{"id":`+itoa(80+i)+`,"jsonrpc":"2.0","result":{"outcome":{"outcome":"cancelled"}}}` {
			t.Fatalf("%s: the agent was answered %s", tc.name, got)
		}
		if err := c.Reply(context.Background(), p.ref, session.Verdict{Allow: true}); !errors.Is(err, ErrNoRequest) {
			t.Fatalf("%s: a verdict on the closed card: %v", tc.name, err)
		}
	}
	// A later request about a changed call is declined.
	s.write(`{"jsonrpc":"2.0","id":90,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"c1"},"options":` + acpOpts + `}}`)
	if got := s.await(`"id":90,`); strings.Contains(got, `"o1"`) {
		t.Fatalf("a request about a changed call answered %s", got)
	}
	for _, l := range s.written() {
		if strings.Contains(l, `"optionId":"o1"`) {
			t.Fatalf("allow_once selected for a changed call: %s", l)
		}
	}
	if n := len(rec.kinds("permission")); n != len(changes) {
		t.Fatalf("%d requests reached the session, want %d", n, len(changes))
	}
}

// An option the client does not read declines the request: an option with
// a field beyond optionId, name and kind, an empty or repeated id, a kind
// protocol 1 does not name, more than one of kind allow_once. Options whose
// allow_once comes second are read, and an Allow selects that one's own id.
func TestACPOptionsTheClientDoesNotReadDeclineTheRequest(t *testing.T) {
	c, s, rec := begun(t, ACPOptions{})
	for i, opts := range []string{
		`[{"optionId":"a","name":"Allow","kind":"allow_once","_meta":{"x":1}}]`,
		`[{"optionId":"","name":"Allow","kind":"allow_once"}]`,
		`[{"optionId":"x","name":"Allow","kind":"allow_once"},{"optionId":"x","name":"Always","kind":"allow_always"}]`,
		`[{"optionId":"a","name":"Allow","kind":"allow_for_now"}]`,
		`[{"optionId":"a","name":"Allow and stop asking","kind":"allow_once"},{"optionId":"b","name":"Allow","kind":"allow_once"}]`,
	} {
		call := "d" + itoa(i)
		s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"` + call + `","title":"t","kind":"execute","status":"pending","rawInput":{"command":"ls"}}`))
		s.write(`{"jsonrpc":"2.0","id":` + itoa(100+i) + `,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"` + call + `"},"options":` + opts + `}}`)
		if got := s.await(`"id":` + itoa(100+i) + `,`); !strings.Contains(got, `{"outcome":"cancelled"}`) {
			t.Fatalf("options %s answered %s", opts, got)
		}
	}
	waitEvent(t, rec, "notice", 5)
	if n := len(rec.kinds("permission")); n != 0 {
		t.Fatalf("%d requests with options the client does not read reached the session", n)
	}
	s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"e1","title":"t","kind":"execute","status":"pending","rawInput":{"command":"ls"}}`))
	s.write(`{"jsonrpc":"2.0","id":110,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"e1"},"options":[{"optionId":"always-1","name":"Always","kind":"allow_always"},{"optionId":"once-1","name":"Allow","kind":"allow_once"},{"optionId":"no-1","name":"Reject","kind":"reject_once"}]}}`)
	p := waitEvent(t, rec, "permission", 1)[0]
	if err := c.Reply(context.Background(), p.ref, session.Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
	if got := s.await(`"id":110,`); !strings.Contains(got, `{"optionId":"once-1","outcome":"selected"}`) {
		t.Fatalf("an Allow with allow_once listed second answered %s", got)
	}
}

// A request named by an open one's id closes the open one's card before it
// is asked, and a verdict on the old card reaches nobody.
func TestACPAReusedRequestIDClosesTheOldCard(t *testing.T) {
	c, s, rec := begun(t, ACPOptions{})
	first := askAbout(t, s, rec, 120, "f1", `{"command":"ls"}`, 0)
	second := askAbout(t, s, rec, 120, "f2", `{"command":"pwd"}`, 1)
	if w := waitEvent(t, rec, "permission_withdrawn", 1)[0]; w.ref != first.ref {
		t.Fatalf("withdrew %s, want %s", w.ref, first.ref)
	}
	if err := c.Reply(context.Background(), first.ref, session.Verdict{Allow: true}); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("the old card's verdict: %v", err)
	}
	if err := c.Reply(context.Background(), second.ref, session.Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
	if got := s.await(`"id":120,`); !strings.Contains(got, `"optionId":"o1"`) {
		t.Fatalf("answered %s", got)
	}
	n := 0
	for _, l := range s.written() {
		if strings.Contains(l, `"id":120,`) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("request 120 answered %d times", n)
	}
}

// The call is shown, bound and floored as the agent wrote it: its input
// keeps a number past what a float holds and a large integer exactly, and a
// raw input holding a key the client was not taught for the agent, or one
// that is not an object, declines the call's request.
func TestACPTheCallIsShownAsTheAgentWroteIt(t *testing.T) {
	c, s, rec := begun(t, ACPOptions{RawInputKeys: []string{"command", "timeout", "n"}})
	_ = c
	p := askAbout(t, s, rec, 130, "g1", `{"command":"ls", "timeout":1e999,"n":9007199254740993}`, 0)
	if string(p.perm.Input) != `{"kind":"execute","rawInput":{"command":"ls","timeout":1e999,"n":9007199254740993},"title":"t"}` {
		t.Fatalf("the input %s", p.perm.Input)
	}
	for i, raw := range []string{`{"command":"ls","directory":"/x"}`, `"ls"`, `["ls"]`} {
		id := 131 + i
		s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"h` + itoa(i) + `","title":"t","kind":"execute","status":"pending","rawInput":` + raw + `}`))
		s.write(`{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"session/request_permission","params":{"sessionId":"` + acpSession + `","toolCall":{"toolCallId":"h` + itoa(i) + `"},"options":` + acpOpts + `}}`)
		if got := s.await(`"id":` + itoa(id) + `,`); strings.Contains(got, `"o1"`) {
			t.Fatalf("raw input %s answered %s", raw, got)
		}
	}
	waitEvent(t, rec, "notice", 3)
	if n := len(rec.kinds("permission")); n != 1 {
		t.Fatalf("%d requests reached the session", n)
	}
}

// A chat of a yolo launch begins by saying the chat asks.
func TestACPAYoloLaunchsChatSaysItAsks(t *testing.T) {
	rec := &recorder{}
	c, _ := newACPServer(t, rec, ACPOptions{YoloLeft: true})
	if err := c.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := waitEvent(t, rec, "notice", 1)[0]; n.code != "yolo_left" || n.text != ACPYoloLeftWords {
		t.Fatalf("the notice %+v", n)
	}
}

// A call with an open request is not forgotten: ended calls leave the
// calls the client follows, so a thousand of them later an update of the
// call still closes its card; and when a thousand open calls later the
// client stops following it, its card closes, the agent answered
// cancelled.
func TestACPACallWithAnOpenRequestIsNotForgotten(t *testing.T) {
	_, s, rec := begun(t, ACPOptions{})
	p := askAbout(t, s, rec, 150, "c1", `{"command":"ls"}`, 0)
	for i := range maxOpenTools {
		s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"f` + itoa(i) + `","title":"t","kind":"read","status":"completed"}`))
	}
	// Every line before it handled once the text after them is: the card
	// is open still.
	texts := len(rec.kinds("text"))
	s.write(acpUpdate(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"a"}}`))
	waitEvent(t, rec, "text", texts+1)
	if n := len(rec.kinds("permission_withdrawn")); n != 0 {
		t.Fatalf("the card closed after %d ended calls", maxOpenTools)
	}
	s.write(acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"c1","rawInput":{"command":"cat /etc/passwd"}}`))
	if w := waitEvent(t, rec, "permission_withdrawn", 1)[0]; w.ref != p.ref {
		t.Fatalf("withdrew %s, want %s", w.ref, p.ref)
	}
	p = askAbout(t, s, rec, 151, "c2", `{"command":"ls"}`, 1)
	for i := range maxOpenTools {
		s.write(acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"g` + itoa(i) + `","title":"t","kind":"read","status":"pending"}`))
	}
	if w := waitEvent(t, rec, "permission_withdrawn", 2)[1]; w.ref != p.ref {
		t.Fatalf("withdrew %s, want %s", w.ref, p.ref)
	}
	if got := s.await(`"id":151,`); got != `{"id":151,"jsonrpc":"2.0","result":{"outcome":{"outcome":"cancelled"}}}` {
		t.Fatalf("the agent was answered %s", got)
	}
}
