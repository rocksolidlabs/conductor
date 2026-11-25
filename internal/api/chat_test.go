package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// chatEnvT is a server whose native Runtime starts a session's terminal as
// a fake (its keys go nowhere) and a chat's child for real, through the
// native StartPipes, as the stub agent web/e2e/stub-protocol.mjs (Claude
// Code's print mode in its recorded shapes), with the agent "claude":
// Claude Code's built-in entry, its command the stub.
type chatEnvT struct {
	*testEnv
	rt *sessiontest.FakeRuntime
}

// stubProtocol is the stub agent's path; a test that needs it skips where
// no node runs it.
func stubProtocol(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node to run the stub agent: ", err)
	}
	p, err := filepath.Abs(filepath.Join("..", "..", "web", "e2e", "stub-protocol.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newChatEnv makes a chat's server, the stub's environment env (what its
// catalog entry sets), logging to log (nil discards).
func newChatEnv(t *testing.T, env map[string]string, log *slog.Logger) *chatEnvT {
	t.Helper()
	stub := stubProtocol(t)
	claude, _ := catalog.Default().Get("claude")
	claude = claude.Copy()
	claude.Command, claude.Env = []string{stub}, env
	rt := &sessiontest.FakeRuntime{Base: local.Native(), Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
		return sessiontest.NewFakeProc(), nil
	}}
	if log == nil {
		log = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	}
	e := newTestEnvAgents(t, nil, log, []catalog.Agent{claude}, withNativeRuntime(sessiontest.FakePipeRuntime{FakeRuntime: rt}))
	return &chatEnvT{testEnv: e, rt: rt}
}

// launch starts a terminal session of the stub's agent.
func (c *chatEnvT) launch(t *testing.T) *session.Local {
	t.Helper()
	id := c.createSession("claude")
	l, ok := c.srv.lookupLocal(id)
	if !ok {
		t.Fatal("no local session")
	}
	return l
}

// enter puts l in the protocol way through the server's own start.
func (c *chatEnvT) enter(t *testing.T, l *session.Local) error {
	t.Helper()
	start, err := c.srv.chatStart(l)
	if err != nil {
		t.Fatal(err)
	}
	return sessiontest.EnterProtocol(l, start)
}

// pipeSpecs is every StartPipes the Runtime was asked.
func (c *chatEnvT) pipeSpecs() []ext.PipeSpec {
	var out []ext.PipeSpec
	for _, call := range c.rt.Asked("StartPipes") {
		out = append(out, call.Spec.(ext.PipeSpec))
	}
	return out
}

// entriesWhen waits for the transcript to hold what ok says.
func entriesWhen(t *testing.T, l *session.Local, what string, ok func([]session.TranscriptEntry) bool) []session.TranscriptEntry {
	t.Helper()
	var es []session.TranscriptEntry
	waitUntil(t, what, func() bool {
		es = nil
		var after uint64
		for {
			page, err := l.Transcript(after, session.MaxTranscriptPage)
			if err != nil || len(page) == 0 {
				break
			}
			es = append(es, page...)
			after = page[len(page)-1].Seq
		}
		return ok(es)
	})
	return es
}

func hasEntry(es []session.TranscriptEntry, ok func(session.TranscriptEntry) bool) bool {
	return slices.ContainsFunc(es, ok)
}

func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			return v, true
		}
	}
	return "", false
}

// A chat's child starts through the session's Runtime's StartPipes, never
// StartTerminal, in the terminal's folder, with the terminal's environment
// but TERM and COLORTERM, CONDUCTOR_PROTOCOL=1 and NO_COLOR=1 set, and the
// argv of design 4 on the session's conversation; its Info then says the
// way, the protocol and that its messages go as written, and a message
// sent is a turn of the agent's.
func TestChatChildThroughPipes(t *testing.T) {
	c := newChatEnv(t, nil, nil)
	l := c.launch(t)
	info := l.Info()
	if info.AgentSession == nil || info.AgentSession.ID == "" {
		t.Fatal("the launch named no conversation")
	}
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	if n := len(c.rt.Asked("StartTerminal")); n != 1 {
		t.Fatalf("%d terminals started: the chat's child must not be one", n)
	}
	specs := c.pipeSpecs()
	if len(specs) != 1 {
		t.Fatalf("%d pipes started", len(specs))
	}
	spec := specs[0]
	if spec.Dir != info.Cwd || spec.Ref.Session != info.ID {
		t.Fatalf("started in %q for %q, want %q for %q", spec.Dir, spec.Ref.Session, info.Cwd, info.ID)
	}
	want, err := agentproto.ClaudeArgs(agentproto.ClaudeLaunch{Command: []string{stubProtocol(t)}, Conversation: info.AgentSession.ID, HooksDir: agents.HooksDir(c.srv.cfg.DataDir), NoMCP: !c.srv.cfg.MCP(), PermissionHost: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv\n%q\nwant\n%q", spec.Argv, want)
	}
	for name, value := range map[string]string{"CONDUCTOR_PROTOCOL": "1", "NO_COLOR": "1", "CONDUCTOR_SESSION_ID": info.ID, "CONDUCTOR_AGENT": "claude"} {
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
	if got.Way != session.WayProtocol || got.Protocol != agentproto.ClaudeStreamJSON || !got.Verbatim || got.Cols != 0 || got.Rows != 0 {
		t.Fatalf("Info says %q %q %v %dx%d", got.Way, got.Protocol, got.Verbatim, got.Cols, got.Rows)
	}
	// On the wire: the session's route and the listing.
	_, out := c.do("GET", "/api/sessions/"+info.ID, adminToken, nil)
	one, _ := out["session"].(map[string]any)
	if one["way"] != "protocol" || one["protocol"] != "claude-stream-json" || one["verbatim"] != true {
		t.Fatalf("GET /api/sessions/{id}: %v %v %v", one["way"], one["protocol"], one["verbatim"])
	}
	// A turn of the agent's: the stub answers in its words.
	if _, err := l.Submit(context.Background(), session.Submission{Text: "hello"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
		t.Fatal(err)
	}
	entriesWhen(t, l, "the stub's answer", func(es []session.TranscriptEntry) bool {
		return hasEntry(es, func(e session.TranscriptEntry) bool {
			return e.Kind == session.EntryText && e.Text == "stub chat: hello" && e.Final
		})
	})
}

// A session in its terminal says no way, protocol or verbatim on the wire.
func TestSessionInfoSaysTheWay(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	_, out := e.do("GET", "/api/sessions/"+id, adminToken, nil)
	one, _ := out["session"].(map[string]any)
	if one == nil {
		t.Fatalf("GET /api/sessions/{id}: %v", out)
	}
	for _, k := range []string{"way", "protocol", "verbatim"} {
		if _, ok := one[k]; ok {
			t.Errorf("a terminal session's Info has %q", k)
		}
	}
	b, _ := json.Marshal(session.Info{Way: session.WayProtocol, Protocol: strings.Repeat("p", 32), Verbatim: true})
	if !strings.Contains(string(b), `"way":"protocol","protocol":"`+strings.Repeat("p", 32)+`","verbatim":true`) {
		t.Fatalf("json: %s", b)
	}
}

// The gate, the session's side as far as this row goes: a probed version
// below the driver's, or none, keeps the session in its terminal with its
// sentence and starts nothing; a child that answers no initialize, or
// fails the shape check, is ended and the session stays in its terminal,
// well inside the session's bound on a start; a chat whose child's first
// answer says it is not signed in goes back to its terminal on the same
// conversation, with that sentence (design 4; the move's way back).
func TestClaudeGateKeepsTheTerminal(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want error
		pipe bool
	}{
		{"an older version", map[string]string{"STUB_PROTOCOL_VERSION": "2.1.330"}, agentproto.ErrNotQualified, false},
		{"no version", map[string]string{"STUB_PROTOCOL_VERSION": "unknown"}, agentproto.ErrNotQualified, false},
		{"no initialize answer", map[string]string{"STUB_PROTOCOL_INIT": "none"}, agentproto.ErrNotStarted, true},
		{"the shape check fails", map[string]string{"STUB_PROTOCOL_INIT": "bad"}, agentproto.ErrNotStarted, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newChatEnv(t, c.env, nil)
			env.srv.chatInitWait = 300 * time.Millisecond
			l := env.launch(t)
			began := time.Now()
			err := env.enter(t, l)
			if !errors.Is(err, c.want) {
				t.Fatalf("entered with %v, want %v", err, c.want)
			}
			if took := time.Since(began); took > 30*time.Second {
				t.Fatalf("took %v to go back: not well inside the start's bound", took)
			}
			if l.Way() != session.WayTerminal || l.Info().Way != "" || l.Info().Status.Ended() {
				t.Fatalf("after the refusal: way %q, status %q", l.Way(), l.Info().Status)
			}
			if n := len(env.pipeSpecs()); (n == 1) != c.pipe {
				t.Fatalf("%d children started", n)
			}
			if !strings.HasPrefix(err.Error(), agentproto.NotQualifiedWords) && !strings.HasPrefix(err.Error(), agentproto.NotStartedWords) {
				t.Fatalf("the refusal's words: %q", err)
			}
		})
	}
	t.Run("not signed in", func(t *testing.T) {
		env := newChatEnv(t, map[string]string{"STUB_PROTOCOL_SIGNED_OUT": "1"}, nil)
		l := env.launch(t)
		id, conv := l.Info().ID, l.Info().AgentSession.ID
		env.turnEnded(t, id)
		if code, out := env.move(t, id, session.WayProtocol); code != http.StatusOK {
			t.Fatalf("to Chat: %d %v", code, out)
		}
		moved(t, l, session.WayProtocol)
		// Its viewer is sent the driver's notice as it comes: the chat's
		// transcript is not read once the session is back in its terminal.
		sink := &wayFrames{}
		sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleView, Name: "Ada"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Detach(sub) })
		if _, err := l.Submit(context.Background(), session.Submission{Text: "hello"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
			t.Fatal(err)
		}
		until(t, "the not-signed-in notice", func() bool {
			return sink.saw(`"code":"` + agentproto.NoticeNotSignedIn + `"`)
		})
		if !sink.saw(agentproto.NotSignedInWords) {
			t.Fatal("the notice's words are not the driver's")
		}
		if sink.saw(`"code":"unreadable"`) {
			t.Fatal("a signed-out agent was told as an unreadable one")
		}
		// The way message back carries the driver's sentence.
		until(t, "the way message back", func() bool {
			return sink.saw(`"way":"terminal","why":"` + agentproto.NotSignedInWords + `"`)
		})
		// Back in its terminal, on the same conversation: not ended.
		moved(t, l, session.WayTerminal)
		if l.Info().Status.Ended() {
			t.Fatalf("the session ended: %q", l.Info().Status)
		}
		terms := env.rt.Asked("StartTerminal")
		if len(terms) != 2 {
			t.Fatalf("%d terminals started", len(terms))
		}
		argv := terms[1].Spec.(ext.TerminalSpec).Argv
		if i := indexOf(argv, "--resume"); i < 0 || i+1 >= len(argv) || argv[i+1] != conv {
			t.Fatalf("the terminal back: %q, want --resume %s", argv, conv)
		}
		if info := l.Info(); info.AgentSession == nil || info.AgentSession.ID != conv {
			t.Fatalf("the conversation: %+v", info.AgentSession)
		}
	})
}

// Past the driver's input bound, the next turn's end ends the child and
// starts it again on the same conversation, with its notice; a message
// sent during that turn is written to the new child alone.
func TestClaudeInputCountResumes(t *testing.T) {
	c := newChatEnv(t, map[string]string{}, nil)
	c.srv.chatMaxInput = 1
	l := c.launch(t)
	conv := l.Info().AgentSession.ID
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	words := make(chan error, 2)
	for _, text := range []string{"first", "second"} {
		if _, err := l.Submit(context.Background(), session.Submission{Text: text, Finished: func(_ session.SubmitResult, err error) { words <- err }}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
			t.Fatal(err)
		}
	}
	es := entriesWhen(t, l, "the second answered by the new child", func(es []session.TranscriptEntry) bool {
		return hasEntry(es, func(e session.TranscriptEntry) bool {
			return e.Kind == session.EntryText && e.Text == "stub chat: second" && e.Final
		})
	})
	restarted := slices.IndexFunc(es, func(e session.TranscriptEntry) bool { return e.Kind == session.EntryNotice && e.Code == "restarted" })
	second := slices.IndexFunc(es, func(e session.TranscriptEntry) bool {
		return e.Kind == session.EntryText && e.Text == "stub chat: second"
	})
	if restarted < 0 || restarted > second || es[restarted].Words != restartedInputWords {
		t.Fatalf("the restart's notice at %d, the second's words at %d", restarted, second)
	}
	specs := c.pipeSpecs()
	if len(specs) < 2 {
		t.Fatalf("%d children started", len(specs))
	}
	for _, s := range specs[:2] {
		if i := slices.Index(s.Argv, "--resume"); i < 0 || s.Argv[i+1] != conv {
			t.Fatalf("a child not on the conversation: %q", s.Argv)
		}
	}
	for range 2 {
		if err := <-words; err != nil {
			t.Fatalf("a message's word: %v", err)
		}
	}
	if l.Info().Status.Ended() {
		t.Fatal("the restart ended the session")
	}
}

// I17: Conductor puts no vendor credential into a chat: the child's
// environment, the session's Info, its record and the server's log hold
// none of the server's own ANTHROPIC_*, CLAUDE_CODE_OAUTH_TOKEN or OPENAI_*
// values, and no credential file's path.
func TestNoVendorCredentialInEnvOrRecord(t *testing.T) {
	secrets := map[string]string{
		"ANTHROPIC_API_KEY":       "sk-ant-never-0001",
		"ANTHROPIC_AUTH_TOKEN":    "ant-token-never-0002",
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth-never-0003",
		"OPENAI_API_KEY":          "sk-openai-never-0004",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	var logs bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(lockedWriter{b: &logs, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := newChatEnv(t, nil, log)
	l := c.launch(t)
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
			k, _, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "OPENAI_") || k == "CLAUDE_CODE_OAUTH_TOKEN" {
				t.Errorf("the child's environment has %s", k)
			}
		}
	}
	info, _ := json.Marshal(l.Info())
	mu.Lock()
	logged := logs.String()
	mu.Unlock()
	var kept []string
	_ = filepath.WalkDir(c.srv.cfg.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, err := os.ReadFile(p); err == nil {
				kept = append(kept, string(b))
			}
		}
		return nil
	})
	for where, text := range map[string]string{"Info": string(info), "the log": logged, "the data folder": strings.Join(kept, "\n")} {
		for k, v := range secrets {
			if strings.Contains(text, v) {
				t.Errorf("%s holds %s's value", where, k)
			}
		}
		for _, path := range []string{".credentials.json", "auth.json"} {
			if strings.Contains(text, path) {
				t.Errorf("%s names %s", where, path)
			}
		}
	}
}

// A chat's session takes none of the reports a hook makes (its own stream
// reports the steps): a needs_input of kind permission, a turn's done, and
// a file, tool_use, tool_denied or error event from the agent token are
// answered 200, change neither the attention nor the activity, and are
// noted once; the agent's own question (--ask) and a progress are taken.
func TestProtocolSessionDropsHookShapedReports(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	tok := e.agentToken(id)
	l, _ := e.srv.lookupLocal(id)
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	before := l.Info().Attention
	activity := func() int { return len(l.Activity()) }
	n := activity()
	for _, r := range []struct {
		path string
		body map[string]any
	}{
		{"attention", map[string]any{"state": "needs_input", "kind": "permission", "message": "Allow Bash?"}},
		{"attention", map[string]any{"state": "done", "kind": "done", "turn": true}},
		{"attention", map[string]any{"state": "working", "turn": true}},
		{"events", map[string]any{"type": "file", "op": "edit", "path": "/x/a.go", "tool": "Edit"}},
		{"events", map[string]any{"type": "tool_use", "tool": "Bash"}},
		{"events", map[string]any{"type": "tool_denied", "tool": "Bash"}},
		{"events", map[string]any{"type": "error", "tool": "Bash", "message": "boom"}},
		{"events", map[string]any{"type": "needs_input", "kind": "permission"}},
	} {
		resp, out := e.do("POST", "/api/sessions/"+id+"/"+r.path, tok, r.body)
		if resp.StatusCode != http.StatusOK || out["dropped"] != true {
			t.Fatalf("%s %v: %d %v", r.path, r.body, resp.StatusCode, out)
		}
	}
	if got := l.Info().Attention; got.State != before.State || got.Kind != before.Kind || got.Message != before.Message {
		t.Fatalf("the attention changed: %+v, was %+v", got, before)
	}
	acts := l.Activity()
	if len(acts) != n+1 || acts[len(acts)-1].Message != HookDroppedWords {
		t.Fatalf("activity %d entries after %d: %+v", len(acts), n, acts[n:])
	}
	// The agent's own reports are taken.
	if resp, out := e.do("POST", "/api/sessions/"+id+"/attention", tok, map[string]any{"state": "needs_input", "ask": true, "message": "Which one?"}); resp.StatusCode != http.StatusOK || out["dropped"] == true {
		t.Fatalf("--ask: %d %v", resp.StatusCode, out)
	}
	if l.Info().Attention.Message != "Which one?" {
		t.Fatalf("the agent's question was not taken: %+v", l.Info().Attention)
	}
	if resp, out := e.do("POST", "/api/sessions/"+id+"/events", tok, map[string]any{"type": "progress", "message": "half way"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("progress: %d %v", resp.StatusCode, out)
	}
	// An admin's report is never dropped, and a terminal session takes all.
	if resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "tool_use", "tool": "Bash"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("an admin's event: %d %v", resp.StatusCode, out)
	}
	other := e.createSession("cat")
	otherTok := e.agentToken(other)
	if resp, out := e.do("POST", "/api/sessions/"+other+"/events", otherTok, map[string]any{"type": "tool_use", "tool": "Bash"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a terminal session's event: %d %v", resp.StatusCode, out)
	}
}

// I13: a chat's captured text, older than a terminal's scrollback holds
// but still in its transcript, is output to the copied-output check under
// transcript:<id>; once the transcript lets it go it is no longer, and
// when the session is removed nothing of it is.
func TestOldTranscriptTextIsStillOutput(t *testing.T) {
	e := syncEnv(t, syncDataDir(t), newFakeSync())
	ctx := context.Background()
	id := e.createSession("cat")
	l, _ := e.srv.lookupLocal(id)
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	quoted := func(text string) bool {
		t.Helper()
		// Each wait is the index's own catch-up bound: a loaded machine
		// (the race detector) takes several to index what was fed.
		waitUntil(t, "the index whole", func() bool { return e.srv.outputs.whole(ctx) })
		q, err := e.srv.outputs.quotes(text)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	r := rand.New(rand.NewPCG(53, 54))
	old := words(r, 600)
	f.TurnStarted("")
	f.Text("m-old", old, true)
	// More than a terminal's scrollback after it, each in an entry of its
	// own: the transcript keeps the old words still.
	for i := 0; e.srv.cfg.ScrollbackBytes >= i*4000; i++ {
		f.Text(fmt.Sprintf("m-%d", i), words(r, 4000), true)
	}
	holds := func(ref string) bool {
		es := entriesWhen(t, l, "the transcript read", func([]session.TranscriptEntry) bool { return true })
		return hasEntry(es, func(e session.TranscriptEntry) bool { return e.Ref == ref })
	}
	if !holds("m-old") {
		t.Fatal("the transcript let the old words go already")
	}
	probe := old[200:400]
	if !quoted(probe) {
		t.Fatal("text the transcript keeps, past the scrollback, is not output")
	}
	// The transcript lets it go past its bound of entries.
	for i := range session.MaxTranscriptEntries {
		f.Text(fmt.Sprintf("n-%d", i), fmt.Sprintf("short words %d", i), true)
	}
	if holds("m-old") {
		t.Fatal("the transcript still keeps the old words")
	}
	waitUntil(t, "the old words let go", func() bool { return !quoted(probe) })
	// What it keeps now goes when the session is removed.
	recent := words(r, 300)
	f.Text("m-recent", recent, true)
	if !quoted(recent[50:250]) {
		t.Fatal("the recent words are not output")
	}
	_ = l.Stop(ctx)
	waitUntil(t, "the session ended", func() bool { return l.Info().Status.Ended() })
	if resp, _ := e.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	waitUntil(t, "the session removed", func() bool { _, ok := e.srv.registry.Get(id); return !ok })
	waitUntil(t, "the removed session's words let go", func() bool { return !quoted(recent[50:250]) })
}

// The catalog route shows an agent's protocol, read-only: its name and its
// arguments; a save that sends none keeps the one it replaces, {} turns it
// off, a name Conductor does not speak is refused, and a clone carries the
// source's.
func TestCatalogRouteShowsTheProtocol(t *testing.T) {
	chat := catalog.Agent{ID: "chat", Name: "Chat", Command: []string{"/bin/cat"}, Protocol: &catalog.ProtocolRecipe{Name: agentproto.ClaudeStreamJSON, Args: []string{"--effort=high"}}}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), []catalog.Agent{chat})
	shown := func(id string) any { return e.catalogAgent(id)["protocol"] }
	want := map[string]any{"name": "claude-stream-json", "args": []any{"--effort=high"}}
	if got, _ := json.Marshal(shown("chat")); string(got) != `{"args":["--effort=high"],"name":"claude-stream-json"}` {
		t.Fatalf("listed %s, want %v", got, want)
	}
	if shown("cat") != nil {
		t.Fatal("an agent with no protocol shows one")
	}
	// A save from the Agents page, which has no control for it.
	e.save(map[string]any{"id": "chat", "name": "Chat (mine)", "command": []string{"/bin/cat"}})
	if a, _ := e.srv.Catalog().Get("chat"); a.Protocol == nil || a.Protocol.Name != agentproto.ClaudeStreamJSON {
		t.Fatalf("a save without it dropped it: %+v", a.Protocol)
	}
	// A clone carries it.
	if status, out := e.clone("chat", map[string]any{"id": "chat-two", "name": "Chat two", "command": []string{"/bin/cat"}}); status != http.StatusCreated {
		t.Fatalf("clone: %d %v", status, out)
	}
	if a, _ := e.srv.Catalog().Get("chat-two"); a.Protocol == nil || a.Protocol.Name != agentproto.ClaudeStreamJSON || !slices.Equal(a.Protocol.Args, []string{"--effort=high"}) {
		t.Fatalf("the clone's protocol: %+v", a.Protocol)
	}
	// One Conductor does not speak is refused, on a save and a clone.
	resp, out := e.do("POST", "/api/catalog", adminToken, map[string]any{"id": "other", "name": "Other", "command": []string{"/bin/cat"}, "protocol": map[string]any{"name": "acp-v9"}})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out), "not one Conductor speaks") {
		t.Fatalf("an unknown protocol saved: %d %v", resp.StatusCode, out)
	}
	if status, out := e.clone("chat", map[string]any{"id": "chat-three", "name": "Chat three", "command": []string{"/bin/cat"}, "protocol": map[string]any{"name": "nope"}}); status != http.StatusBadRequest {
		t.Fatalf("a clone with an unknown protocol: %d %v", status, out)
	}
	// {} turns it off.
	e.save(map[string]any{"id": "chat", "name": "Chat", "command": []string{"/bin/cat"}, "protocol": map[string]any{}})
	if a, _ := e.srv.Catalog().Get("chat"); !a.Protocol.Off() {
		t.Fatalf("{} left it on: %+v", a.Protocol)
	}
}

// The version is checked once per session: a chat's child started again
// (past its input's bound here) reuses the answer its first start had,
// so a probe that no longer answers in time (its answer expired, the
// machine loaded) never ends a running chat at its restart.
func TestClaudeRestartReusesTheQualifiedVersion(t *testing.T) {
	c := newChatEnv(t, map[string]string{}, nil)
	c.srv.chatMaxInput = 1
	l := c.launch(t)
	if err := c.enter(t, l); err != nil {
		t.Fatal(err)
	}
	// From now on the probe's answer is gone and a new one comes late.
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	c.srv.probes.mu.Lock()
	c.srv.probes.entries = map[string]probeEntry{}
	c.srv.probes.wait = 50 * time.Millisecond
	run := c.srv.probes.run
	c.srv.probes.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		select {
		case <-held:
		case <-ctx.Done():
		}
		return run(ctx, r, command, p, env)
	}
	c.srv.probes.mu.Unlock()
	if _, err := l.Submit(context.Background(), session.Submission{Text: "first"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
		t.Fatal(err)
	}
	entriesWhen(t, l, "the restart's notice", func(es []session.TranscriptEntry) bool {
		return hasEntry(es, func(e session.TranscriptEntry) bool {
			return e.Kind == session.EntryNotice && (e.Code == "restarted" || e.Code == "restart_failed")
		})
	})
	waitUntil(t, "the second child", func() bool { return len(c.pipeSpecs()) >= 2 })
	if st := l.Info().Status; st.Ended() {
		t.Fatalf("the restart ended the chat: %s", st)
	}
	if l.Way() != session.WayProtocol {
		t.Fatalf("the chat left its way: %q", l.Way())
	}
}
