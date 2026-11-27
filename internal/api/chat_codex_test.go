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
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// codexThread is the thread the Codex chat's tests report for the session:
// Codex names its own, by its hooks, and the launch names none.
const codexThread = "019a0000-0000-7000-8000-0000000000c1"

// newCodexChatEnv is newChatEnv for Codex (docs/plans/chat-or-terminal.md,
// row 10): Codex's built-in entry with codex-app-server declared, its
// command the stub agent in its Codex mode (stub-protocol-codex.mjs). The
// declaration is this test's: no built-in declares it until the live look.
// The stub's own folder is under the server's HOME.
func newCodexChatEnv(t *testing.T, env map[string]string, log *slog.Logger) (*chatEnvT, string) {
	t.Helper()
	stub := stubProtocol(t)
	codex, _ := catalog.Default().Get("codex")
	codex = codex.Copy()
	all := map[string]string{"STUB_PROTOCOL_AGENT": "codex"}
	for k, v := range env {
		all[k] = v
	}
	codex.Command, codex.Env, codex.Protocol = []string{stub}, all, &catalog.ProtocolRecipe{Name: agentproto.CodexAppServer}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".stub-sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	rt := &sessiontest.FakeRuntime{Base: local.Native(), Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
		return sessiontest.NewFakeProc(), nil
	}}
	if log == nil {
		log = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	}
	e := newTestEnvAgents(t, nil, log, []catalog.Agent{codex}, withNativeRuntime(sessiontest.FakePipeRuntime{FakeRuntime: rt}))
	// As newChatEnv: the lookups and probes bound only a hang here.
	e.srv.lookups.stubWait(hangWait)
	e.srv.probes.mu.Lock()
	e.srv.probes.wait, e.srv.probes.timeout = hangWait, hangWait
	e.srv.probes.mu.Unlock()
	return &chatEnvT{testEnv: e, rt: rt}, filepath.Join(home, ".stub-sessions")
}

// launchCodex starts a terminal session of the stub's Codex and reports its
// thread, as Codex's hooks report it.
func (c *chatEnvT) launchCodex(t *testing.T) *session.Local {
	t.Helper()
	id := c.createSession("codex")
	l, ok := c.srv.lookupLocal(id)
	if !ok {
		t.Fatal("no local session")
	}
	l.ReportAgentSession(codexThread, session.PolicyLatest, true)
	return l
}

// A Codex chat's child starts through the session's Runtime's StartPipes,
// in the terminal's folder, with the terminal's environment but TERM and
// COLORTERM and CONDUCTOR_PROTOCOL set to its protocol's name, as the app server on stdio with
// Conductor's MCP server as the terminal's launch gave it, not handed
// CONDUCTOR_PROTOCOL (it lists no permission tool to Codex); the thread is
// resumed in the
// session's folder; Info says the way, codex-app-server and that its
// messages are not claimed as written; a message is a turn of the agent's.
func TestCodexChatChildThroughPipes(t *testing.T) {
	c, kept := newCodexChatEnv(t, nil, nil)
	l := c.launchCodex(t)
	info := l.Info()
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	specs := c.pipeSpecs()
	if len(specs) != 1 {
		t.Fatalf("%d pipes started", len(specs))
	}
	spec := specs[0]
	if spec.Dir != info.Cwd || spec.Ref.Session != info.ID {
		t.Fatalf("started in %q for %q, want %q for %q", spec.Dir, spec.Ref.Session, info.Cwd, info.ID)
	}
	want := []string{stubProtocol(t), "app-server", "--listen", "stdio://"}
	if c.srv.cfg.MCP() {
		want = append(want, agents.MCPFor("codex", agents.HooksDir(c.srv.cfg.DataDir), true)...)
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv\n%q\nwant\n%q", spec.Argv, want)
	}
	if slices.ContainsFunc(spec.Argv, func(a string) bool {
		return strings.HasPrefix(a, "mcp_servers.conductor.env_vars=") && strings.Contains(a, `"CONDUCTOR_PROTOCOL"`)
	}) {
		t.Fatalf("Conductor's MCP server is handed CONDUCTOR_PROTOCOL: %q", spec.Argv)
	}
	for name, value := range map[string]string{"CONDUCTOR_PROTOCOL": agentproto.CodexAppServer, "NO_COLOR": "1", "CONDUCTOR_SESSION_ID": info.ID, "CONDUCTOR_AGENT": "codex"} {
		if v, ok := envValue(spec.Env, name); !ok || v != value {
			t.Errorf("%s=%q (set %v), want %q", name, v, ok, value)
		}
	}
	for _, name := range []string{"TERM", "COLORTERM"} {
		if _, ok := envValue(spec.Env, name); ok {
			t.Errorf("%s in a chat's environment", name)
		}
	}
	got := l.Info()
	if got.Way != session.WayProtocol || got.Protocol != agentproto.CodexAppServer || got.Verbatim {
		t.Fatalf("Info says %q %q %v", got.Way, got.Protocol, got.Verbatim)
	}
	var resumed map[string]any
	b, err := os.ReadFile(filepath.Join(kept, codexThread+".resume"))
	if err != nil || json.Unmarshal(b, &resumed) != nil {
		t.Fatalf("the stub was not asked to resume the thread: %v %s", err, b)
	}
	if resumed["threadId"] != codexThread || resumed["cwd"] != info.Cwd || resumed["excludeTurns"] != true {
		t.Fatalf("thread/resume carried %v", resumed)
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

// A Codex chat's approval request is the session's permission question,
// with the folder the request names; the operator's Allow reaches the app
// server as accept for that request's own id, and the step runs; the next
// request, denied, reaches it as decline for its own.
func TestCodexChatAsksThroughThePermissionPath(t *testing.T) {
	c, kept := newCodexChatEnv(t, nil, nil)
	l := c.launchCodex(t)
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "Ada", Owner: true}, quietSink{})
	if err != nil {
		t.Fatal(err)
	}
	for i, choice := range []string{proto.PermissionAllowOnce, proto.PermissionDeny} {
		if _, err := l.Submit(context.Background(), session.Submission{Text: "ask command"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
			t.Fatal(err)
		}
		var r session.TranscriptEntry
		entriesWhen(t, l, "the request", func(es []session.TranscriptEntry) bool {
			for _, e := range es {
				if e.Kind == session.EntryPermission && e.State == session.PermissionOpen {
					r = e
					return true
				}
			}
			return false
		})
		if r.Tool != "commandExecution" || r.Plain != "command" || r.Cwd != l.Info().Cwd || r.Question == "" {
			t.Fatalf("the request's entry %+v", r)
		}
		if att := l.Info().Attention; att.Kind != session.KindPermission || att.ID != r.Question {
			t.Fatalf("the attention %+v", att)
		}
		if err := l.AnswerPermission(context.Background(), sub, proto.PermissionAnswer{T: proto.CtlPermissionAnswer, ReqID: "a" + string(rune('1'+i)), Question: r.Question, Call: r.Call, Sha256: r.InputSha256, Choice: choice}); err != nil {
			t.Fatal(err)
		}
		file := "allowed"
		if choice == proto.PermissionDeny {
			file = "denied"
		}
		waitUntil(t, "the stub told "+file, func() bool {
			b, _ := os.ReadFile(filepath.Join(kept, codexThread+"."+file))
			return len(bytes.TrimSpace(b)) > 0
		})
		b, _ := os.ReadFile(filepath.Join(kept, codexThread+"."+file))
		if choice == proto.PermissionDeny && !strings.HasSuffix(strings.TrimSpace(string(b)), " decline") {
			t.Fatalf("the stub was told %q", b)
		}
		turnEnds := i + 1
		entriesWhen(t, l, "the turn's end", func(es []session.TranscriptEntry) bool {
			n := 0
			for _, e := range es {
				if e.Kind == session.EntryTurn && e.Phase == "end" {
					n++
				}
			}
			return n >= turnEnds
		})
	}
	es := entriesWhen(t, l, "both requests closed", func(es []session.TranscriptEntry) bool {
		var states []string
		for _, e := range es {
			if e.Kind == session.EntryPermission {
				states = append(states, e.State)
			}
		}
		return slices.Equal(states, []string{session.PermissionAllowed, session.PermissionDenied})
	})
	if !hasEntry(es, func(e session.TranscriptEntry) bool {
		return e.Kind == session.EntryTool && e.Status == session.ToolOK && e.Tool == "commandExecution"
	}) ||
		!hasEntry(es, func(e session.TranscriptEntry) bool {
			return e.Kind == session.EntryTool && e.Status == session.ToolDenied
		}) {
		t.Fatal("the steps did not end as answered")
	}
	// Each answer went to its own request's id, once.
	wire, err := os.ReadFile(filepath.Join(kept, codexThread+".wire"))
	if err != nil {
		t.Fatal(err)
	}
	var answers []string
	for _, line := range strings.Split(string(wire), "\n") {
		if strings.Contains(line, `"decision"`) {
			answers = append(answers, line)
		}
	}
	if !slices.Equal(answers, []string{`{"id":1,"result":{"decision":"accept"}}`, `{"id":2,"result":{"decision":"decline"}}`}) {
		t.Fatalf("the answers written %q", answers)
	}
}

// A Codex chat's child is handed no vendor credential Conductor put there,
// and neither its Info nor the server's log names one (I17): an OPENAI_*
// value the server's environment holds stays out.
func TestCodexChatHoldsNoVendorCredential(t *testing.T) {
	secrets := map[string]string{"OPENAI_API_KEY": "sk-openai-never-0004", "CODEX_API_KEY": "codex-never-0005"}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	var logs bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(lockedWriter{b: &logs, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, _ := newCodexChatEnv(t, nil, log)
	l := c.launchCodex(t)
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Submit(context.Background(), session.Submission{Text: "hello"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
		t.Fatal(err)
	}
	entriesWhen(t, l, "a turn", func(es []session.TranscriptEntry) bool {
		return hasEntry(es, func(e session.TranscriptEntry) bool { return e.Kind == session.EntryTurn && e.Phase == "end" })
	})
	for _, spec := range c.pipeSpecs() {
		for _, kv := range spec.Env {
			for k, v := range secrets {
				if strings.Contains(kv, v) {
					t.Errorf("the child's environment has %s's value", k)
				}
			}
		}
		for _, a := range spec.Argv {
			if strings.Contains(a, "auth.json") || strings.Contains(a, "sk-") {
				t.Errorf("the child's argv names a credential: %q", a)
			}
		}
	}
	info, _ := json.Marshal(l.Info())
	mu.Lock()
	logged := logs.String()
	mu.Unlock()
	for where, text := range map[string]string{"Info": string(info), "the log": logged} {
		for k, v := range secrets {
			if strings.Contains(text, v) {
				t.Errorf("%s holds %s's value", where, k)
			}
		}
	}
}

// The move to Chat of a Codex session is refused while Codex's Moves is
// false (no thread handed between the terminal and the app server has been
// observed by hand), in the plan's words, the terminal left running. Its
// declaration, made on that observation, changes this test with it.
func TestCodexMoveWaitsForItsObservation(t *testing.T) {
	if agentproto.Moves(agentproto.CodexAppServer) {
		t.Fatal("Codex moves before its by-hand observation is recorded (row 10)")
	}
	c, _ := newCodexChatEnv(t, nil, nil)
	l := c.launchCodex(t)
	resp, out := c.do("POST", "/api/sessions/"+l.Info().ID+"/way", adminToken, map[string]any{"to": "protocol"})
	if got, words := refusalOf(out); resp.StatusCode != http.StatusConflict || got != proto.ErrCodeMoveRefused || words != session.CannotMoveWords {
		t.Fatalf("the move: %d %v", resp.StatusCode, out)
	}
	if l.Way() != session.WayTerminal || len(c.pipeSpecs()) != 0 {
		t.Fatal("the session left its terminal")
	}
}

// A launch as a chat of Codex is refused, before anything starts, in the
// switch's words while Codex's driver does not move yet; its launch in its
// terminal is unchanged.
func TestCodexLaunchAsChatWaitsForItsMove(t *testing.T) {
	if agentproto.Moves(agentproto.CodexAppServer) {
		t.Fatal("Codex moves before its by-hand observation is recorded (row 10)")
	}
	c, _ := newCodexChatEnv(t, nil, nil)
	resp, out := c.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "codex", "way": "protocol"})
	if got, words := refusalOf(out); resp.StatusCode != http.StatusConflict || got != proto.ErrCodeMoveRefused || words != session.CannotMoveWords {
		t.Fatalf("a launch as a chat: %d %v", resp.StatusCode, out)
	}
	if n := len(c.rt.Asked("StartTerminal")); n != 0 || len(c.srv.registry.List()) != 0 {
		t.Fatalf("a refused launch started %d terminals", n)
	}
	l := c.launchCodex(t)
	if l.Way() != session.WayTerminal || len(c.pipeSpecs()) != 0 {
		t.Fatal("a launch in its terminal started a chat")
	}
}
