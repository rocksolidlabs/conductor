package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// newACPChatEnv is newChatEnv for an agent of the Agent Client Protocol
// (docs/plans/chat-or-terminal.md, row 11): Claude Code's built-in entry
// copied with acp-v1 declared, its start "acp", its command the stub agent,
// which in that start is its third mode (stub-protocol-acp.mjs). The
// declaration is this test's: no built-in declares acp-v1 until each
// agent's live look. The stub's own folder is under the server's HOME.
func newACPChatEnv(t *testing.T, env map[string]string) (*chatEnvT, string) {
	t.Helper()
	stub := stubProtocol(t)
	a, _ := catalog.Default().Get("claude")
	a = a.Copy()
	a.Command, a.Env, a.Protocol = []string{stub}, env, &catalog.ProtocolRecipe{Name: agentproto.ACPv1, Args: []string{"acp"}}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".stub-sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	rt := &sessiontest.FakeRuntime{Base: local.Native(), Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
		return sessiontest.NewFakeProc(), nil
	}}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), []catalog.Agent{a}, withNativeRuntime(sessiontest.FakePipeRuntime{FakeRuntime: rt}))
	e.srv.lookups.stubWait(hangWait)
	e.srv.probes.mu.Lock()
	e.srv.probes.wait, e.srv.probes.timeout = hangWait, hangWait
	e.srv.probes.mu.Unlock()
	return &chatEnvT{testEnv: e, rt: rt}, filepath.Join(home, ".stub-sessions")
}

// An ACP agent's chat's child starts through the session's Runtime's
// StartPipes, in the terminal's folder, as the agent's own start of its ACP
// server and nothing else, with CONDUCTOR_PROTOCOL=1 and no TERM; the
// terminal's session is taken up through the protocol in the session's
// folder with no MCP server; Info says acp-v1 and that its messages are not
// claimed as written; a message is a turn of the agent's.
func TestACPChatChildThroughPipes(t *testing.T) {
	c, kept := newACPChatEnv(t, nil)
	l := c.launch(t)
	info := l.Info()
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	specs := c.pipeSpecs()
	if len(specs) != 1 || !slices.Equal(specs[0].Argv, []string{stubProtocol(t), "acp"}) || specs[0].Dir != info.Cwd {
		t.Fatalf("started %+v", specs)
	}
	if v, ok := envValue(specs[0].Env, "CONDUCTOR_PROTOCOL"); !ok || v != "1" {
		t.Fatal("no CONDUCTOR_PROTOCOL=1")
	}
	if _, ok := envValue(specs[0].Env, "TERM"); ok {
		t.Fatal("TERM in a chat's environment")
	}
	if got := l.Info(); got.Way != session.WayProtocol || got.Protocol != agentproto.ACPv1 || got.Verbatim {
		t.Fatalf("Info says %q %q %v", got.Way, got.Protocol, got.Verbatim)
	}
	conv := info.AgentSession.ID
	var taken map[string]any
	b, err := os.ReadFile(filepath.Join(kept, conv+".resume"))
	if err != nil || json.Unmarshal(b, &taken) != nil {
		t.Fatalf("the session was not taken up: %v %s", err, b)
	}
	if taken["method"] != "session/resume" || taken["sessionId"] != conv || taken["cwd"] != info.Cwd || !strings.Contains(string(b), `"mcpServers":[]`) {
		t.Fatalf("taken up with %s", b)
	}
	if _, err := l.Submit(context.Background(), session.Submission{Text: "hello"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
		t.Fatal(err)
	}
	entriesWhen(t, l, "the stub's answer", func(es []session.TranscriptEntry) bool {
		return hasEntry(es, func(e session.TranscriptEntry) bool {
			return e.Kind == session.EntryText && e.Text == "stub chat: hello" && e.Final
		})
	})
}

// An ACP agent's permission request is the session's question, bound to
// the call the stream announced; the operator's Allow once selects the
// agent's allow_once option by its own id (OpenCode's "once"), never an
// always option.
func TestACPChatAsksThroughThePermissionPath(t *testing.T) {
	c, kept := newACPChatEnv(t, map[string]string{"STUB_ACP_OPTIONS": "opencode"})
	l := c.launch(t)
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "Ada", Owner: true}, quietSink{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Submit(context.Background(), session.Submission{Text: "ask"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
		t.Fatal(err)
	}
	var r session.TranscriptEntry
	es := entriesWhen(t, l, "the request", func(es []session.TranscriptEntry) bool {
		for _, e := range es {
			if e.Kind == session.EntryPermission && e.State == session.PermissionOpen {
				r = e
				return true
			}
		}
		return false
	})
	var step session.TranscriptEntry
	for _, e := range es {
		if e.Kind == session.EntryTool && e.Ref == r.Call {
			step = e
		}
	}
	if r.Call == "" || r.Tool != "execute" || r.InputSha256 != step.InputSha256 {
		t.Fatalf("the request %+v, its step %+v", r, step)
	}
	if err := l.AnswerPermission(context.Background(), sub, proto.PermissionAnswer{T: proto.CtlPermissionAnswer, ReqID: "a1", Question: r.Question, Call: r.Call, Sha256: r.InputSha256, Choice: proto.PermissionAllowOnce}); err != nil {
		t.Fatal(err)
	}
	conv := l.Info().AgentSession.ID
	waitUntil(t, "the stub's outcome", func() bool {
		b, _ := os.ReadFile(filepath.Join(kept, conv+".outcome"))
		return len(b) > 0
	})
	if b, _ := os.ReadFile(filepath.Join(kept, conv+".outcome")); strings.TrimSpace(string(b)) != `{"optionId":"once","outcome":"selected"}` {
		t.Fatalf("the stub was told %s", b)
	}
}

// The move to Chat of an ACP agent's session, and a launch as a chat, are
// refused in the switch's words while no agent's move is observed by hand
// (acpMoves), the terminal left running. A declaration changes this test.
func TestACPMoveWaitsForItsObservation(t *testing.T) {
	c, _ := newACPChatEnv(t, nil)
	if agentproto.MovesFor(agentproto.ACPv1, "claude") {
		t.Fatal("an ACP agent moves before its by-hand observation is recorded (row 11)")
	}
	l := c.launch(t)
	resp, out := c.do("POST", "/api/sessions/"+l.Info().ID+"/way", adminToken, map[string]any{"to": "protocol"})
	if got, words := refusalOf(out); resp.StatusCode != http.StatusConflict || got != proto.ErrCodeMoveRefused || words != session.CannotMoveWords {
		t.Fatalf("the move: %d %v", resp.StatusCode, out)
	}
	resp, out = c.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "claude", "way": "protocol"})
	if got, words := refusalOf(out); resp.StatusCode != http.StatusConflict || got != proto.ErrCodeMoveRefused || words != session.CannotMoveWords {
		t.Fatalf("a launch as a chat: %d %v", resp.StatusCode, out)
	}
	if l.Way() != session.WayTerminal || len(c.pipeSpecs()) != 0 {
		t.Fatal("the session left its terminal")
	}
}
