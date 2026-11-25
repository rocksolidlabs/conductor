package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// The permission request whole (docs/plans/chat-or-terminal.md, design 7,
// row 6): the real conductor mcp serve loop on pipes, the real server's
// permission route on a test listener, and a chat whose driver is a fake,
// in one process. No agent program runs.

// inputMarker is in every call's input here: no log line of either side may
// hold it.
const inputMarker = "SECRET-INPUT-7f3a9c"

// routeChat is a real server with one chat on a fake driver, in a turn, the
// serve loop of its agent, the operator's own connection, and the logs of
// the server and the session.
type routeChat struct {
	l    *session.Local
	f    *sessiontest.FakeProtocol
	c    *mcpClient
	s    *mcpServer
	op   *session.Subscription
	logs *syncBuffer
	url  string
	id   string
	tok  string
}

// quietSink takes the operator's frames and drops them.
type quietSink struct{}

func (quietSink) WriteFrame([]byte) error { return nil }
func (quietSink) Close(error)             {}

func newRouteChat(t *testing.T, protocol bool) *routeChat {
	t.Helper()
	clearConductorEnv(t)
	cfg := config.Defaults()
	cfg.WorkbenchToken = "admin-token"
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.DataDir = t.TempDir()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, err := api.New(cfg, catalog.Default(), log, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	p := sessiontest.NewFakeProc()
	l := session.NewLocal(session.Info{ID: session.NewID(), Name: "chat", Kind: session.KindServer, Cwd: t.TempDir(), Status: session.StatusRunning, Cols: 80, Rows: 24},
		p, session.Options{ScrollbackBytes: 4096, Transport: proto.TransportWS, Log: log})
	if err := srv.Registry().Add(l); err != nil {
		t.Fatal(err)
	}
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	tok := ("agent-token-" + l.Info().ID + strings.Repeat("-", share.TokenLen))[:share.TokenLen]
	l.SetAgentToken(tok)
	op, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, Owner: true, Name: "Ada"}, quietSink{})
	if err != nil {
		t.Fatal(err)
	}
	rc := &routeChat{l: l, f: f, op: op, logs: logs, url: hs.URL, id: l.Info().ID, tok: tok}
	env := sessionEnv(hs.URL, rc.id, tok)
	rc.s = &mcpServer{getenv: func(k string) string {
		if k == protocolEnv && protocol {
			return "1"
		}
		return env(k)
	}}
	rc.c = startMCP(t, rc.s)
	// Ended before the server's listener: the loop's held posts first.
	t.Cleanup(func() {
		rc.c.end()
		_ = l.Stop(context.Background())
		srv.Registry().Remove(rc.id)
		p.End(0)
	})
	f.TurnStarted("")
	return rc
}

// input is a call's input, its marker in it.
func input(n int) string { return fmt.Sprintf(`{"command":"echo %s-%d"}`, inputMarker, n) }

// announce has the stream show call ref of Bash with input.
func (rc *routeChat) announce(ref, in string) {
	rc.f.ToolStarted(session.ToolStep{Ref: ref, Name: "Bash", Input: []byte(in)})
}

// ask is the tools/call Claude Code makes of the prompt tool for ref.
func (rc *routeChat) ask(id int, ref, in string) {
	rc.c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"permission","arguments":{"tool_name":"Bash","input":%s,"tool_use_id":%q}}}`, id, in, ref))
}

// open waits for ref's request to be open; the bound only bounds a hang.
func (rc *routeChat) open(t *testing.T, ref string) session.TranscriptEntry {
	t.Helper()
	deadline := time.Now().Add(mcpHang)
	for {
		if e, ok := rc.entry(ref); ok && e.State == session.PermissionOpen {
			return e
		}
		if time.Now().After(deadline) {
			t.Fatalf("no open request for %s", ref)
		}
		time.Sleep(time.Millisecond)
	}
}

// entry is ref's request entry, its latest.
func (rc *routeChat) entry(ref string) (session.TranscriptEntry, bool) {
	var found session.TranscriptEntry
	ok := false
	for after := uint64(0); ; {
		es, err := rc.l.Transcript(after, session.MaxTranscriptPage)
		if err != nil || len(es) == 0 {
			return found, ok
		}
		for _, e := range es {
			if e.Kind == session.EntryPermission && e.Call == ref {
				found, ok = e, true
			}
		}
		after = es[len(es)-1].Seq
	}
}

// settled waits for ref's request to have left open, and gives it.
func (rc *routeChat) settled(t *testing.T, ref string) session.TranscriptEntry {
	t.Helper()
	deadline := time.Now().Add(mcpHang)
	for {
		if e, ok := rc.entry(ref); ok && e.State != session.PermissionOpen {
			return e
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s's request never left open", ref)
		}
		time.Sleep(time.Millisecond)
	}
}

// answer is the operator's answer to e.
func (rc *routeChat) answer(e session.TranscriptEntry, choice, message string) error {
	return rc.l.AnswerPermission(context.Background(), rc.op, proto.PermissionAnswer{T: proto.CtlPermissionAnswer, ReqID: "r1", Question: e.Question, Call: e.Call, Sha256: e.InputSha256, Choice: choice, Message: message})
}

// noOpen waits until no request is open or waiting.
func (rc *routeChat) noOpen(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(mcpHang)
	for {
		if o, w := rc.l.OpenPermissions(); o == 0 && w == 0 {
			return
		}
		if time.Now().After(deadline) {
			o, w := rc.l.OpenPermissions()
			t.Fatalf("%d open, %d waiting", o, w)
		}
		time.Sleep(time.Millisecond)
	}
}

// clean fails if any log line of the server or the session holds a call's
// input.
func (rc *routeChat) clean(t *testing.T) {
	t.Helper()
	rc.logs.mu.Lock()
	logs := rc.logs.b.String()
	rc.logs.mu.Unlock()
	if strings.Contains(logs, inputMarker) {
		t.Fatalf("a call's input is in a log line:\n%s", logs)
	}
}

// The tool is listed from the real table only in a chat: its description
// says it is Conductor's and not to be called, its schema takes Claude
// Code's tool_name, input and tool_use_id.
func TestThePermissionToolIsListedFromTheTableInAChatAlone(t *testing.T) {
	for _, protocol := range []bool{false, true} {
		rc := newRouteChat(t, protocol)
		rc.c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		res, _ := rc.c.byID(1)["result"].(map[string]any)
		var tool map[string]any
		for _, x := range res["tools"].([]any) {
			if m := x.(map[string]any); m["name"] == permissionTool {
				tool = m
			}
		}
		if (tool != nil) != protocol {
			t.Fatalf("in a chat %v, permission listed: %v", protocol, tool != nil)
		}
		if tool == nil {
			continue
		}
		desc, _ := tool["description"].(string)
		if !strings.Contains(desc, "Conductor's own") || !strings.Contains(desc, "Never call it yourself") {
			t.Fatalf("the description %q", desc)
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		for _, k := range []string{"tool_name", "input", "tool_use_id"} {
			if props[k] == nil {
				t.Fatalf("the schema lacks %s: %v", k, schema)
			}
		}
		if schema["additionalProperties"] == false {
			t.Fatalf("the schema refuses what Claude Code may add: %v", schema)
		}
	}
}

// The operator's allow and deny reach the caller as the documented result,
// the entry allowed or denied once the reply was written; a deny with no
// words of the operator's tells the agent the lead's sentence.
func TestTheOperatorsAnswerReachesTheAgent(t *testing.T) {
	rc := newRouteChat(t, true)
	for i, c := range []struct{ choice, message, want, state string }{
		{proto.PermissionAllowOnce, "", allowed, session.PermissionAllowed},
		{proto.PermissionDeny, "Run make test instead.", denied("Run make test instead."), session.PermissionDenied},
		{proto.PermissionDeny, "", denied(session.OperatorDeniedWords), session.PermissionDenied},
	} {
		ref := fmt.Sprintf("toolu_%d", i)
		rc.announce(ref, input(i))
		rc.ask(i+1, ref, input(i))
		e := rc.open(t, ref)
		if err := rc.answer(e, c.choice, c.message); err != nil {
			t.Fatalf("%s: the operator's answer: %v", c.choice, err)
		}
		// AnswerPermission returns once the reply was written; the entry
		// says so from then.
		if got, _ := rc.entry(ref); got.State != c.state || got.ByName != "Ada" {
			t.Fatalf("%s: the entry %+v", c.choice, got)
		}
		if text := documented(t, rc.c.byID(i+1)); text != c.want {
			t.Fatalf("%s: the agent got %s, want %s", c.choice, text, c.want)
		}
	}
	rc.noOpen(t)
	rc.clean(t)
}

// A notifications/cancelled for the held call, and the end of stdin, each
// end the POST: the request is withdrawn "The request was lost." (the
// stream showed no result), its slot freed, and a later answer meets
// question_answered; no reply is written for the cancelled call.
func TestTheAgentsCancellationEndsTheHeldPost(t *testing.T) {
	rc := newRouteChat(t, true)
	rc.announce("toolu_1", input(1))
	rc.ask(1, "toolu_1", input(1))
	e := rc.open(t, "toolu_1")
	rc.c.send(cancelLine(1))
	if got := rc.settled(t, "toolu_1"); got.State != session.PermissionWithdrawn || got.Why != session.RequestLostWords {
		t.Fatalf("a cancelled request %+v", got)
	}
	rc.noOpen(t)
	rc.c.idle()
	if err := rc.answer(e, proto.PermissionAllowOnce, ""); !errors.Is(err, session.ErrQuestionAnswered) {
		t.Fatalf("an answer after the cancellation: %v", err)
	}
	rc.c.send(pingLine(2))
	rc.c.byID(2)
	rc.c.noReplyTo(1)

	rc.announce("toolu_3", input(3))
	rc.ask(3, "toolu_3", input(3))
	e = rc.open(t, "toolu_3")
	if code, err := rc.c.end(); code != 0 || err != nil {
		t.Fatalf("serve: %d %v", code, err)
	}
	if got := rc.settled(t, "toolu_3"); got.State != session.PermissionWithdrawn || got.Why != session.RequestLostWords {
		t.Fatalf("a request at the end of stdin %+v", got)
	}
	rc.noOpen(t)
	if err := rc.answer(e, proto.PermissionAllowOnce, ""); !errors.Is(err, session.ErrQuestionAnswered) {
		t.Fatalf("an answer after the end of stdin: %v", err)
	}
	rc.c.noReplyTo(3, rc.c.rest()...)
	rc.clean(t)
}

// A withdrawal the session makes answers the held call with its deny at
// once and frees its slot: the call's result in the stream, the turn's end,
// an interrupt, the session's stop.
func TestTheSessionsWithdrawalAnswersTheHeldCall(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(rc *routeChat)
		want string
	}{
		{"the call's result", func(rc *routeChat) { rc.f.ToolEnded("toolu_1", session.ToolResult{Status: session.ToolOK}) }, session.WithdrawnElsewhereWords},
		{"the turn's end", func(rc *routeChat) { rc.f.TurnEnded("done", session.Usage{}) }, session.WithdrawnElsewhereWords},
		{"an interrupt", func(rc *routeChat) { rc.f.TurnEnded("interrupted", session.Usage{}) }, session.WithdrawnElsewhereWords},
		{"the session's stop", func(rc *routeChat) { _ = rc.l.Stop(context.Background()) }, session.EndedBeforeAnswerWords},
	} {
		t.Run(c.name, func(t *testing.T) {
			rc := newRouteChat(t, true)
			rc.announce("toolu_1", input(1))
			rc.ask(1, "toolu_1", input(1))
			rc.open(t, "toolu_1")
			c.end(rc)
			if text := documented(t, rc.c.byID(1)); text != denied(c.want) {
				t.Fatalf("the held call answered %s, want the deny %q", text, c.want)
			}
			rc.noOpen(t)
			rc.c.idle()
			rc.clean(t)
		})
	}
}

// Past the eight open a call is denied with the bound's words through the
// tool; thirteen calls the session withdraws one after another leave the
// loop and the session able to take the next, and a report.
func TestTheBoundAndFreedSlotsThroughTheTool(t *testing.T) {
	rc := newRouteChat(t, true)
	for i := range session.MaxPendingPermissions {
		ref := fmt.Sprintf("toolu_%d", i)
		rc.announce(ref, input(i))
		rc.ask(i+1, ref, input(i))
		rc.open(t, ref)
	}
	rc.announce("toolu_past", input(99))
	rc.ask(99, "toolu_past", input(99))
	if text := documented(t, rc.c.byID(99)); text != denied(session.TooManyRequestsWords) {
		t.Fatalf("the ninth: %s", text)
	}
	for i := range session.MaxPendingPermissions {
		rc.f.ToolEnded(fmt.Sprintf("toolu_%d", i), session.ToolResult{Status: session.ToolOK})
		if text := documented(t, rc.c.byID(i+1)); text != denied(session.WithdrawnElsewhereWords) {
			t.Fatalf("call %d: %s", i, text)
		}
	}
	rc.noOpen(t)
	for i := range 13 {
		ref := fmt.Sprintf("toolu_w%d", i)
		rc.announce(ref, input(100+i))
		rc.ask(100+i, ref, input(100+i))
		rc.open(t, ref)
		rc.f.ToolEnded(ref, session.ToolResult{Status: session.ToolOK})
		if text := documented(t, rc.c.byID(100+i)); text != denied(session.WithdrawnElsewhereWords) {
			t.Fatalf("withdrawn call %d: %s", i, text)
		}
	}
	rc.c.idle()
	rc.noOpen(t)
	rc.announce("toolu_next", input(200))
	rc.ask(200, "toolu_next", input(200))
	e := rc.open(t, "toolu_next")
	if err := rc.answer(e, proto.PermissionAllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	if text := documented(t, rc.c.byID(200)); text != allowed {
		t.Fatalf("the next call: %s", text)
	}
	rc.c.send(reportLine(201))
	if r := rc.c.byID(201); r["error"] != nil {
		t.Fatalf("a report after: %v", r)
	}
	rc.clean(t)
}

// The tool and the route count the same bytes: a call whose posted body is
// exactly the route's bound reaches the session (no 413), and one byte more
// is the tool's too-large deny, posting nothing.
func TestTheToolAndTheRouteBoundTheSameBody(t *testing.T) {
	if maxPermissionBody != api.MaxPermissionBody {
		t.Fatalf("the tool bounds %d bytes, the route %d", maxPermissionBody, api.MaxPermissionBody)
	}
	rc := newRouteChat(t, true)
	sized := func(ref string, n int) string {
		// The body the tool posts for an input of padding p.
		body := func(p int) int {
			b, err := encodeJSON(struct {
				Call  string          `json:"call"`
				Tool  string          `json:"tool"`
				Input json.RawMessage `json:"input"`
			}{ref, "Bash", json.RawMessage(`{"command":"` + strings.Repeat("x", p) + `"}`)})
			if err != nil {
				t.Fatal(err)
			}
			return len(b)
		}
		p := n - body(0)
		if body(p) != n {
			t.Fatalf("a body of %d bytes, want %d", body(p), n)
		}
		return `{"command":"` + strings.Repeat("x", p) + `"}`
	}
	at := sized("toolu_at", api.MaxPermissionBody)
	rc.announce("toolu_at", at)
	rc.ask(1, "toolu_at", at)
	e := rc.open(t, "toolu_at")
	if err := rc.answer(e, proto.PermissionDeny, "Too much."); err != nil {
		t.Fatal(err)
	}
	if text := documented(t, rc.c.byID(1)); text != denied("Too much.") {
		t.Fatalf("a body at the bound: %s", text)
	}
	past := sized("toolu_past", api.MaxPermissionBody+1)
	rc.announce("toolu_past", past)
	rc.ask(2, "toolu_past", past)
	if text := documented(t, rc.c.byID(2)); text != denied(permissionTooLarge) {
		t.Fatalf("a body past the bound: %s", text)
	}
	if _, ok := rc.entry("toolu_past"); ok {
		t.Fatal("a body past the bound reached the session")
	}
	if o, w := rc.l.OpenPermissions(); o != 0 || w != 0 {
		t.Fatalf("%d open, %d waiting", o, w)
	}
}

// Nothing of a call's input reaches a log line of either side, through
// every ending: the operator's, the agent's cancellation, a withdrawal, a
// call not announced, a refused token.
func TestNoCallsInputIsLogged(t *testing.T) {
	rc := newRouteChat(t, true)
	// The serve loop keeps no log: what it says goes in its replies, which
	// hold the plan's sentences or the operator's words alone.
	var replies []map[string]any
	reply := func(c *mcpClient, id int) {
		r := c.byID(id)
		documented(t, r)
		replies = append(replies, r)
	}
	rc.announce("toolu_1", input(1))
	rc.ask(1, "toolu_1", input(1))
	if err := rc.answer(rc.open(t, "toolu_1"), proto.PermissionAllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	reply(rc.c, 1)
	rc.announce("toolu_2", input(2))
	rc.ask(2, "toolu_2", input(2))
	rc.open(t, "toolu_2")
	rc.c.send(cancelLine(2))
	rc.settled(t, "toolu_2")
	rc.announce("toolu_3", input(3))
	rc.ask(3, "toolu_3", input(3))
	rc.open(t, "toolu_3")
	rc.f.ToolEnded("toolu_3", session.ToolResult{Status: session.ToolOK})
	reply(rc.c, 3)
	rc.ask(4, "toolu_3", input(3)) // a call with a result: denied
	reply(rc.c, 4)
	// A refused token: the route's 401, its words the server's.
	bad := &mcpServer{getenv: func(k string) string {
		if k == protocolEnv {
			return "1"
		}
		return sessionEnv(rc.url, rc.id, "wrong-token")(k)
	}}
	c := startMCP(t, bad)
	c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"permission","arguments":{"tool_name":"Bash","input":%s,"tool_use_id":"toolu_5"}}}`, input(5)))
	r := c.byID(1)
	if text := documented(t, r); text != denied(permissionLost) {
		t.Fatalf("a refused token: %s", text)
	}
	replies = append(replies, r)
	rc.clean(t)
	for _, r := range replies {
		if b, _ := json.Marshal(r); strings.Contains(string(b), inputMarker) {
			t.Fatalf("a reply holds the call's input: %s", b)
		}
	}
}
