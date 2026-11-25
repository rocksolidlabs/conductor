package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A chat's permission request's route (docs/plans/chat-or-terminal.md,
// design 7, row 6): the agent's token alone, its body's bound, held open.

// heldPost is a permission request's POST under way.
type heldPost struct {
	cancel context.CancelFunc
	done   chan struct{}
	status int
	body   map[string]any
}

// postPermission posts body to the session's permission route with tok, on
// a goroutine of its own.
func postPermission(e *testEnv, id, tok string, body []byte) *heldPost {
	ctx, cancel := context.WithCancel(context.Background())
	h := &heldPost{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.http.URL+"/api/sessions/"+id+"/permission", bytes.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		h.status = resp.StatusCode
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &h.body)
	}()
	return h
}

func permissionBody(call, tool, input string) []byte {
	b, _ := json.Marshal(map[string]any{"call": call, "tool": tool, "input": json.RawMessage(input)})
	return b
}

// answered waits for the POST's answer; the bound only bounds a hang.
func (h *heldPost) answered(t *testing.T) (int, map[string]any) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the permission POST was never answered")
	}
	return h.status, h.body
}

func (h *heldPost) held() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// openEntry waits for the session's open request of call.
func openEntry(t *testing.T, l *session.Local, call string) session.TranscriptEntry {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		es, _ := l.Transcript(0, session.MaxTranscriptPage)
		for _, e := range es {
			if e.Kind == session.EntryPermission && e.Call == call && e.State == session.PermissionOpen {
				return e
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no open request for %s", call)
		}
		time.Sleep(time.Millisecond)
	}
}

// The route: the session's agent token alone, a body of at most 256 KiB
// (413 past it, no question asked), held open until it is answered, its
// answer the documented one.
func TestPermissionRouteBodyBound(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	id, l, _ := chatSession(t, e)
	tok := e.agentToken(id)
	other := e.createSession("cat")
	otherTok := e.agentToken(other)
	body := func(n int) []byte {
		// {"call":"c","tool":"Bash","input":{"command":"xxx…"}} of n bytes.
		head, tail := `{"call":"c","tool":"Bash","input":{"command":"`, `"}}`
		return []byte(head + strings.Repeat("x", n-len(head)-len(tail)) + tail)
	}
	past := postPermission(e, id, tok, body(256<<10+1))
	if status, out := past.answered(t); status != http.StatusRequestEntityTooLarge || errorCode(out) != "too_large" {
		t.Fatalf("a body past 256 KiB: %d %v", status, out)
	}
	if o, w := l.OpenPermissions(); o != 0 || w != 0 {
		t.Fatalf("a body past its bound asked a question: %d open, %d waiting", o, w)
	}
	// At the bound, on a session that is no chat: read, and denied.
	at := postPermission(e, other, otherTok, body(256<<10))
	if status, out := at.answered(t); status != http.StatusOK || out["behavior"] != "deny" || out["message"] != session.NotACallWords {
		t.Fatalf("a body at 256 KiB: %d %v", status, out)
	}
	// The admin's token answers agent_only, as every route the session's
	// own token alone takes; any other, 401.
	if status, out := postPermission(e, id, adminToken, permissionBody("c", "Bash", `{}`)).answered(t); status != http.StatusForbidden || errorCode(out) != "agent_only" {
		t.Fatalf("the admin's token: %d %v", status, out)
	}
	for name, tk := range map[string]string{"another session's": otherTok, "none": "", "a wrong one": "wrong"} {
		h := postPermission(e, id, tk, permissionBody("c", "Bash", `{}`))
		if status, out := h.answered(t); status != http.StatusUnauthorized {
			t.Fatalf("%s token: %d %v", name, status, out)
		}
	}
	for name, b := range map[string]string{"no tool": `{"call":"c","input":{}}`, "no input": `{"call":"c","tool":"Bash"}`, "a field unknown": `{"call":"c","tool":"Bash","input":{},"allow":true}`} {
		if status, out := postPermission(e, id, tok, []byte(b)).answered(t); status != http.StatusBadRequest {
			t.Fatalf("%s: %d %v", name, status, out)
		}
	}
	if o, w := l.OpenPermissions(); o != 0 || w != 0 {
		t.Fatalf("%d open, %d waiting", o, w)
	}
}

// A protocol lane's member may put its own calls to the prompt tool: the
// route is one of a comparison lane's doors (doorOwn), so its request
// reaches the question, never 409 lane.
func TestPermissionRouteOpenToALane(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	d, ok := ce.srv.registry.Get(ce.b)
	if !ok {
		t.Fatal("the lane's session is not listed")
	}
	l := d.(*session.Local)
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	f.TurnStarted("")
	f.ToolStarted(session.ToolStep{Ref: "c-1", Name: "Bash", Input: []byte(`{"command":"make"}`)})
	h := postPermission(ce.testEnv, ce.b, ce.bTok, permissionBody("c-1", "Bash", `{"command":"make"}`))
	r := openEntry(t, l, "c-1")
	if att := l.Info().Attention; att.ID != r.Question || att.Kind != session.KindPermission {
		t.Fatalf("the lane's request did not reach the question: %+v", att)
	}
	h.cancel()
	<-h.done
	if h.status == http.StatusConflict {
		t.Fatalf("the lane's request was refused: %v", h.body)
	}
}

// The floor's files the server writes are the ones a chat's child is
// started with.
func TestTheFloorsFilesAreTheChildsOwn(t *testing.T) {
	if agentproto.ClaudeProtocolSettings != agents.ClaudeProtocolSettings || agentproto.ClaudeProtocolYoloSettings != agents.ClaudeProtocolYoloSettings {
		t.Fatal("the floor's file names differ between the start and the assets")
	}
}

// A Brakeman turn's session runs in its terminal, never as a chat: its own
// token is refused on the permission route (401) before the body is read,
// and asks no question.
func TestBrakemansTokenPutsNoPermissionRequest(t *testing.T) {
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	sid, tok, _ := b.turnSession(t, b.briefing(t))
	for name, body := range map[string][]byte{"a request": permissionBody("c-1", "Bash", `{"command":"ls"}`), "a body it would refuse": []byte(`{}`)} {
		status, data, _ := b.callAs(t, tok, http.MethodPost, "/api/sessions/"+sid+"/permission", body)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s with Brakeman's token: %d %.200s", name, status, data)
		}
	}
	d, ok := b.srv.registry.Get(sid)
	if !ok {
		t.Fatal("the turn's session is not listed")
	}
	if l, isLocal := d.(*session.Local); isLocal {
		if o, w := l.OpenPermissions(); o != 0 || w != 0 {
			t.Fatalf("%d open, %d waiting", o, w)
		}
	}
}

// Every tool of Conductor's MCP server is sorted for a chat's floor: each
// MCP name of the operations table is in exactly one of the agents' two
// lists (unasked, or asks with its reason), so a new tool fails here until
// someone sorts it; no list names a tool the table does not have; the
// prompt tool asks; an unasked tool is a session's or an attempt's own,
// never Brakeman's or a ticket's; and the allow rules name the server the
// prompt tool is named by.
func TestEveryConductorToolIsSorted(t *testing.T) {
	byMCP := map[string]Operation{}
	for _, op := range Operations() {
		if op.MCP != "" {
			byMCP[op.MCP] = op
		}
	}
	for name, op := range byMCP {
		_, asks := agents.ProtocolAskedTools[name]
		unasked := slices.Contains(agents.ProtocolUnaskedTools, name)
		switch {
		case asks && unasked:
			t.Errorf("%s is both unasked and asked", name)
		case !asks && !unasked:
			t.Errorf("%s is neither unasked nor asked: sort it for a chat's floor (agents.ProtocolUnaskedTools, agents.ProtocolAskedTools)", name)
		case unasked && op.Scope != "session" && op.Scope != "task":
			t.Errorf("%s is unasked but of scope %s", name, op.Scope)
		}
	}
	for _, name := range agents.ProtocolUnaskedTools {
		if _, ok := byMCP[name]; !ok {
			t.Errorf("the unasked %s is no tool of the table", name)
		}
	}
	for name := range agents.ProtocolAskedTools {
		if _, ok := byMCP[name]; !ok {
			t.Errorf("the asked %s is no tool of the table", name)
		}
	}
	if _, asks := agents.ProtocolAskedTools[byMCP["permission"].MCP]; !asks {
		t.Error("the prompt tool is not among the tools that ask")
	}
	if !strings.HasPrefix(agentproto.ClaudePermissionTool, "mcp__conductor__") {
		t.Errorf("the prompt tool is %s: the allow rules name another server", agentproto.ClaudePermissionTool)
	}
}
