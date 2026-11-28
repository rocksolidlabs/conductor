package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The move between ways on a server session (docs/plans/chat-or-terminal.md,
// design 9, row 5): its terminal a fake, its chat's child the stub agent
// (web/e2e/stub-protocol.mjs) through the native StartPipes, as Claude Code
// (whose driver moves: agentproto.Moves).

// newMoveEnv is a chat's server that moves, the stub's environment env.
func newMoveEnv(t *testing.T, env map[string]string, log *slog.Logger) *chatEnvT {
	t.Helper()
	return newChatEnv(t, env, log)
}

// move asks the move of id to to as the operator would, through the
// route's handler (the route itself and its authorization: move_auth_test.go).
func (c *chatEnvT) move(t *testing.T, id, to string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/sessions/"+id+"/way", strings.NewReader(`{"to":"`+to+`"}`))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	c.srv.handleSessionWay(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// turnEnded is the terminal's agent reporting a turn's end through its hook:
// its conversation resumable, a done from a source that reports turns.
func (c *chatEnvT) turnEnded(t *testing.T, id string) {
	t.Helper()
	as := c.local(id).Info().AgentSession
	if as == nil {
		t.Fatalf("session %s names no conversation", id)
	}
	if resp, out := c.do("POST", "/api/sessions/"+id+"/attention", c.agentToken(id), map[string]any{"state": "done", "agentSession": as.ID, "turn": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the turn's report: %d %v", resp.StatusCode, out)
	}
	until(t, "the turn's end", func() bool {
		info := c.local(id).Info()
		return info.Attention.State == session.AttentionDone && info.AgentSession != nil && info.AgentSession.Resumable
	})
}

// moved waits for l's move to end in the way want.
func moved(t *testing.T, l *session.Local, want string) {
	t.Helper()
	until(t, "the move to "+want, func() bool { return !l.Moving() && l.Way() == want })
}

func refusalOf(out map[string]any) (code, words string) {
	e, _ := out["error"].(map[string]any)
	code, _ = e["code"].(string)
	words, _ = e["message"].(string)
	return code, words
}

// I21: a move to Chat on a terminal session that has reported no ended turn
// is refused move_refused with the plan's words; after a done from a turn
// source it is made, through the session's Runtime's pipes.
func TestMoveNeedsAFirstTurn(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launch(t)
	id := l.Info().ID
	code, out := c.move(t, id, session.WayProtocol)
	if got, words := refusalOf(out); code != http.StatusConflict || got != "move_refused" || words != session.FirstReplyWords {
		t.Fatalf("before the first turn: %d %v", code, out)
	}
	if n := len(c.pipeSpecs()); n != 0 {
		t.Fatalf("%d chats started before the first turn", n)
	}
	c.turnEnded(t, id)
	code, out = c.move(t, id, session.WayProtocol)
	if code != http.StatusOK || out["moving"] != true {
		t.Fatalf("after the first turn: %d %v", code, out)
	}
	moved(t, l, session.WayProtocol)
	if n := len(c.pipeSpecs()); n != 1 {
		t.Fatalf("%d chats started", n)
	}
}

// The move keeps the session: its id, its links, its viewers, its people's
// chat, its listing (one session, the same id); its Info says its way, the
// way message reaches its viewer, and the boundary's notice is in its
// transcript. Then back again, the terminal on the same conversation.
func TestMoveKeepsTheSession(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launch(t)
	id := l.Info().ID
	conv := l.Info().AgentSession.ID
	c.turnEnded(t, id)
	resp, out := c.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a link: %d %v", resp.StatusCode, out)
	}
	link := out["url"].(string)
	token := link[strings.LastIndex(link, "/")+1:]
	sink := &wayFrames{}
	sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "Ada", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Detach(sub) })
	if code, out := c.move(t, id, session.WayProtocol); code != http.StatusOK {
		t.Fatalf("to Chat: %d %v", code, out)
	}
	moved(t, l, session.WayProtocol)
	until(t, "the way message", func() bool { return sink.saw(`"t":"way","way":"protocol","protocol":"claude-stream-json"`) })
	entriesWhen(t, l, "the boundary's notice", func(es []session.TranscriptEntry) bool {
		return hasEntry(es, func(e session.TranscriptEntry) bool {
			return e.Kind == session.EntryNotice && e.Words == session.MovedToChatWords
		})
	})
	_, list := c.do("GET", "/api/sessions", adminToken, nil)
	found := 0
	for _, s := range list["sessions"].([]any) {
		if m := s.(map[string]any); m["id"] == id {
			found++
			if m["way"] != "protocol" {
				t.Errorf("the listing says way %v", m["way"])
			}
		}
	}
	if found != 1 || l.Viewers() != 1 {
		t.Fatalf("listed %d times, %d viewers", found, l.Viewers())
	}
	if resp, _ := c.do("GET", "/api/join/"+token, "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the link after the move: %d", resp.StatusCode)
	}
	if code, out := c.move(t, id, session.WayTerminal); code != http.StatusOK {
		t.Fatalf("back: %d %v", code, out)
	}
	moved(t, l, session.WayTerminal)
	until(t, "the way message back", func() bool { return sink.saw(`"t":"way","way":"terminal"`) })
	terms := c.rt.Asked("StartTerminal")
	if len(terms) != 2 {
		t.Fatalf("%d terminals started", len(terms))
	}
	argv := terms[1].Spec.(ext.TerminalSpec).Argv
	if i := indexOf(argv, "--resume"); i < 0 || i+1 >= len(argv) || argv[i+1] != conv {
		t.Fatalf("the terminal back: %q, want --resume %s", argv, conv)
	}
	if info := l.Info(); info.ID != id || info.Way != "" || info.Status.Ended() {
		t.Fatalf("back: %+v", info)
	}
}

func indexOf(argv []string, s string) int {
	for i, a := range argv {
		if a == s {
			return i
		}
	}
	return -1
}

// wayFrames is a viewer's sink that keeps its control and TRANSCRIPT frames' text.
type wayFrames struct {
	mu     sync.Mutex
	frames []string
}

func (w *wayFrames) WriteFrame(f []byte) error {
	if len(f) > 0 && (f[0] == proto.TypeControl || f[0] == proto.TypeTranscript) {
		w.mu.Lock()
		w.frames = append(w.frames, string(f[1:]))
		w.mu.Unlock()
	}
	return nil
}
func (w *wayFrames) Close(error) {}

// saw says a control or TRANSCRIPT frame holds s.
func (w *wayFrames) saw(s string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.frames {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}

// I1 with the real child: once a chat's child is stopped for a move back,
// its group (the stub's, with a member it started that outlives it) is
// gone before the terminal starts again: the seam after the old one's
// exit finds no process in it.
func TestMoveProvesTheChildsGroupGone(t *testing.T) {
	c := newMoveEnv(t, map[string]string{"STUB_PROTOCOL_CHILD": "1"}, nil)
	l := c.launch(t)
	id := l.Info().ID
	c.turnEnded(t, id)
	if code, out := c.move(t, id, session.WayProtocol); code != http.StatusOK {
		t.Fatalf("to Chat: %d %v", code, out)
	}
	moved(t, l, session.WayProtocol)
	pipes := c.rt.Asked("StartPipes")
	group := pipes[0].Process.Group
	if group <= 0 {
		t.Fatalf("the chat's child has no group (%+v)", pipes[0].Process)
	}
	terminals := len(c.rt.Asked("StartTerminal"))
	var seen atomic.Int32
	c.srv.moveSeam = func(string) {
		seen.Add(1)
		if err := syscall.Kill(-group, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("the old child's group at the seam: %v, want no process in it", err)
		}
		if n := len(c.rt.Asked("StartTerminal")); n != terminals {
			t.Errorf("a terminal started before the old group was gone")
		}
	}
	if code, out := c.move(t, id, session.WayTerminal); code != http.StatusOK {
		t.Fatalf("back: %d %v", code, out)
	}
	moved(t, l, session.WayTerminal)
	if seen.Load() != 1 {
		t.Fatalf("the seam ran %d times", seen.Load())
	}
}

// A chat's start that fails goes back to the terminal on the same
// conversation with the driver's sentence on the way message.
func TestMoveGoesBackWithTheDriversWords(t *testing.T) {
	c := newMoveEnv(t, map[string]string{"STUB_PROTOCOL_INIT": "none"}, nil)
	c.srv.chatInitWait = 300 * time.Millisecond
	l := c.launch(t)
	id := l.Info().ID
	c.turnEnded(t, id)
	sink := &wayFrames{}
	sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleView, Name: "Ada"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Detach(sub) })
	if code, out := c.move(t, id, session.WayProtocol); code != http.StatusOK {
		t.Fatalf("to Chat: %d %v", code, out)
	}
	until(t, "back with the words", func() bool {
		return sink.saw(`"way":"terminal","why":"` + agentproto.NotStartedWords + `"`)
	})
	moved(t, l, session.WayTerminal)
	if n := len(c.rt.Asked("StartTerminal")); n != 2 || l.Info().Status.Ended() {
		t.Fatalf("%d terminals, status %q", n, l.Info().Status)
	}
}

// The route refuses a crew member's session (its words) and an agent that
// has no protocol the server moves; nothing is stopped.
func TestMoveRouteRefusesACrewMember(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launch(t)
	id := l.Info().ID
	c.turnEnded(t, id)
	l.SetCrew(session.CrewRef{RunID: "run-1", Member: "writer"})
	code, out := c.move(t, id, session.WayProtocol)
	if got, words := refusalOf(out); code != http.StatusConflict || got != "move_refused" || words != session.CrewStaysWords {
		t.Fatalf("a crew member: %d %v", code, out)
	}
	// An agent with no protocol (a plain program): nothing it could move to.
	e := newTestEnv(t, nil)
	plain := e.createSession("cat")
	code, out = (&chatEnvT{testEnv: e}).move(t, plain, session.WayProtocol)
	if got, words := refusalOf(out); code != http.StatusConflict || got != "move_refused" || words != session.CannotMoveWords {
		t.Fatalf("an agent with no protocol: %d %v", code, out)
	}
	if len(c.pipeSpecs()) != 0 || l.Info().Status.Ended() || e.local(plain).Info().Status.Ended() {
		t.Fatal("a refused move started or stopped something")
	}
}

// A session launched with an added folder, or with a flag a chat sets
// itself, is told so at the move, before anything is stopped: its terminal
// goes on and no child is started.
func TestMoveRefusesArgumentsAChatCannotHold(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	cases := []struct {
		name  string
		args  []string
		words string
	}{
		{"an added folder", []string{"--add-dir=" + t.TempDir()}, agentproto.AddedFolderWords},
		{"a flag of the chat's own", []string{"--verbose"}, agentproto.OwnFlagWords("--verbose")},
		{"a lone --", []string{"--", "fix the tests"}, agentproto.OptionsEndWords},
	}
	for _, tc := range cases {
		resp, out := c.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "claude", "args": tc.args})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s: the launch: %d %v", tc.name, resp.StatusCode, out)
		}
		id, _ := out["id"].(string)
		t.Cleanup(func() {
			if d, ok := c.srv.registry.Get(id); ok {
				_ = d.Stop(t.Context())
			}
		})
		c.turnEnded(t, id)
		code, body := c.move(t, id, session.WayProtocol)
		if got, words := refusalOf(body); code != http.StatusConflict || got != "move_refused" || words != tc.words {
			t.Fatalf("%s: %d %v, want move_refused %q", tc.name, code, body, tc.words)
		}
		if l := c.local(id); l.Way() != session.WayTerminal || l.Info().Status.Ended() {
			t.Fatalf("%s: the refused move left the way %q, ended %v", tc.name, l.Way(), l.Info().Status.Ended())
		}
	}
	if n := len(c.pipeSpecs()); n != 0 {
		t.Fatalf("%d children started by refused moves", n)
	}
	// One terminal for each launch, and none for a refused move.
	if n := len(c.rt.Asked("StartTerminal")); n != len(cases) {
		t.Fatalf("%d terminals started: a refused move stops and starts none", n)
	}
}

// I21: every session starts in its terminal: a launch, a resume of a session
// that ended as a chat, and a resume from its record.
func TestEverySessionStartsInItsTerminal(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launch(t)
	id := l.Info().ID
	if l.Way() != session.WayTerminal || l.Info().Way != "" {
		t.Fatalf("a launch: way %q", l.Way())
	}
	c.turnEnded(t, id)
	if code, out := c.move(t, id, session.WayProtocol); code != http.StatusOK {
		t.Fatalf("to Chat: %d %v", code, out)
	}
	moved(t, l, session.WayProtocol)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	pipes := len(c.pipeSpecs())
	resp, out := c.do("POST", "/api/sessions/"+id+"/resume", adminToken, map[string]any{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume: %d %v", resp.StatusCode, out)
	}
	next, _ := out["id"].(string)
	if next == "" {
		next, _ = out["session"].(map[string]any)["id"].(string)
	}
	n := c.local(next)
	t.Cleanup(func() { _ = n.Stop(context.Background()) })
	if n.Way() != session.WayTerminal || n.Info().Way != "" || len(c.pipeSpecs()) != pipes {
		t.Fatalf("a resume of an ended chat: way %q, %d chats started", n.Way(), len(c.pipeSpecs())-pipes)
	}
	if !c.srv.recording() {
		t.Fatal("this server keeps no records: the resume from one is not tested")
	}
	stopped(t, c.testEnv, next)
	code, hout := resumeRecord(t, c.testEnv, id, map[string]any{"commandId": "c-1", "confirmLost": true})
	if code != http.StatusCreated {
		t.Fatalf("resume from its record: %d %v", code, hout)
	}
	h := c.local(hout["session"].(map[string]any)["id"].(string))
	if h.Way() != session.WayTerminal || len(c.pipeSpecs()) != pipes {
		t.Fatalf("a resume from the record: way %q", h.Way())
	}
}

// A session that begins as a resume (of a session, or of its record, as a
// session is continued after the server restarted) holds a resumable
// conversation from its start, and its terminal has reported no turn yet:
// its listing says no firstTurn and the move to Chat is refused with the
// first reply's words, as the page then offers none; once its own first
// turn has ended the listing says firstTurn and the move is made.
func TestAResumeOffersChatAfterItsOwnFirstTurn(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launch(t)
	id := l.Info().ID
	c.turnEnded(t, id)
	if got := c.listedFirstTurn(t, id); !got {
		t.Fatal("a launch's ended turn is not listed as firstTurn")
	}
	stopped(t, c.testEnv, id)
	resp, out := c.do("POST", "/api/sessions/"+id+"/resume", adminToken, map[string]any{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume: %d %v", resp.StatusCode, out)
	}
	next, _ := out["id"].(string)
	if next == "" {
		next, _ = out["session"].(map[string]any)["id"].(string)
	}
	n := c.local(next)
	t.Cleanup(func() { _ = n.Stop(context.Background()) })
	c.offersChatAfterItsTurn(t, next, "a resume")
	stopped(t, c.testEnv, next)
	code, hout := resumeRecord(t, c.testEnv, id, map[string]any{"commandId": "c-1", "confirmLost": true})
	if code != http.StatusCreated {
		t.Fatalf("resume from its record: %d %v", code, hout)
	}
	h := hout["session"].(map[string]any)["id"].(string)
	t.Cleanup(func() { _ = c.local(h).Stop(context.Background()) })
	c.offersChatAfterItsTurn(t, h, "a resume from the record")
}

// listedFirstTurn is the session's firstTurn as its listing serves it.
func (c *chatEnvT) listedFirstTurn(t *testing.T, id string) bool {
	t.Helper()
	resp, out := c.do("GET", "/api/sessions/"+id, adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the listing of %s: %d %v", id, resp.StatusCode, out)
	}
	listed, _ := out["session"].(map[string]any)
	v, ok := listed["firstTurn"]
	if ok && v != true {
		t.Fatalf("firstTurn %v", v)
	}
	return ok
}

// offersChatAfterItsTurn: the session id, resumable from its start, lists
// no firstTurn and refuses the move to Chat until its own turn has ended;
// then it lists firstTurn and the move is made.
func (c *chatEnvT) offersChatAfterItsTurn(t *testing.T, id, what string) {
	t.Helper()
	info := c.local(id).Info()
	if info.AgentSession == nil || !info.AgentSession.Resumable {
		t.Fatalf("%s: its conversation is not resumable from its start: %+v", what, info.AgentSession)
	}
	if c.listedFirstTurn(t, id) {
		t.Fatalf("%s: listed firstTurn before its own turn", what)
	}
	if code, out := c.move(t, id, session.WayProtocol); code != http.StatusConflict {
		t.Fatalf("%s: before its turn: %d %v", what, code, out)
	} else if got, words := refusalOf(out); got != "move_refused" || words != session.FirstReplyWords {
		t.Fatalf("%s: before its turn: %v", what, out)
	}
	c.turnEnded(t, id)
	if !c.listedFirstTurn(t, id) {
		t.Fatalf("%s: its turn's end is not listed", what)
	}
	if code, out := c.move(t, id, session.WayProtocol); code != http.StatusOK || out["moving"] != true {
		t.Fatalf("%s: after its turn: %d %v", what, code, out)
	}
	moved(t, c.local(id), session.WayProtocol)
}

// stopped stops the session id and waits for its record's end.
func stopped(t *testing.T, e *testEnv, id string) {
	t.Helper()
	e.stopAndWait(id)
	endedRecord(t, e, id)
}
