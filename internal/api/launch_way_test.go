package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A launch as a chat (issue 1174): POST /api/sessions' way. The session
// starts in its terminal (I21), its move to Chat noted at the launch and made
// by the move's own path at the first turn's end; its terminal a fake, its
// chat's child the stub agent, as the move's tests (move_test.go).

// launchWay launches the stub's agent with body's fields over its agentId,
// answering the reply's status and body; a session it made is stopped at
// the test's end.
func (c *chatEnvT) launchAs(t *testing.T, body map[string]any) (int, map[string]any) {
	t.Helper()
	req := map[string]any{"agentId": "claude"}
	for k, v := range body {
		req[k] = v
	}
	resp, out := c.do("POST", "/api/sessions", adminToken, req)
	if id, _ := out["id"].(string); id != "" {
		t.Cleanup(func() {
			if d, ok := c.srv.registry.Get(id); ok {
				_ = d.Stop(t.Context())
			}
		})
	}
	return resp.StatusCode, out
}

// launchedAsChat launches the stub's agent as a chat and answers its session.
func (c *chatEnvT) launchedAsChat(t *testing.T) *session.Local {
	t.Helper()
	code, out := c.launchAs(t, map[string]any{"way": session.WayProtocol})
	if code != http.StatusCreated {
		t.Fatalf("the launch as a chat: %d %v", code, out)
	}
	return c.local(out["id"].(string))
}

// working is the terminal's agent reporting a turn under way through its hook.
func (c *chatEnvT) working(t *testing.T, id string) {
	t.Helper()
	as := c.local(id).Info().AgentSession
	if resp, out := c.do("POST", "/api/sessions/"+id+"/attention", c.agentToken(id), map[string]any{"state": "working", "agentSession": as.ID, "turn": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the turn's start: %d %v", resp.StatusCode, out)
	}
}

// A launch's way is "terminal" or "protocol", bounded like every other
// field; a launch as a chat its agent cannot run is refused in the switch's
// words before anything starts.
func TestLaunchAsChatRefusals(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	for _, tc := range []struct {
		name  string
		body  map[string]any
		code  int
		error string
		words string
	}{
		{"another word", map[string]any{"way": "chat"}, http.StatusBadRequest, "invalid_request", ""},
		{"a way past any", map[string]any{"way": strings.Repeat("p", 4096)}, http.StatusBadRequest, "invalid_request", ""},
		{"not a string", map[string]any{"way": 1}, http.StatusBadRequest, "invalid_request", ""},
		{"an added folder", map[string]any{"way": "protocol", "args": []string{"--add-dir=" + t.TempDir()}}, http.StatusConflict, "move_refused", agentproto.AddedFolderWords},
		{"a flag of the chat's own", map[string]any{"way": "protocol", "args": []string{"--verbose"}}, http.StatusConflict, "move_refused", agentproto.OwnFlagWords("--verbose")},
		{"a lone --", map[string]any{"way": "protocol", "args": []string{"--", "fix the tests"}}, http.StatusConflict, "move_refused", agentproto.OptionsEndWords},
	} {
		code, out := c.launchAs(t, tc.body)
		got, words := refusalOf(out)
		if code != tc.code || got != tc.error || (tc.words != "" && words != tc.words) {
			t.Fatalf("%s: %d %v, want %d %s %q", tc.name, code, out, tc.code, tc.error, tc.words)
		}
	}
	if n := len(c.rt.Asked("StartTerminal")); n != 0 {
		t.Fatalf("%d terminals started by refused launches", n)
	}
	if n := len(c.srv.registry.List()); n != 0 {
		t.Fatalf("%d sessions listed after refused launches", n)
	}

	// A version the driver did not qualify.
	old := newMoveEnv(t, map[string]string{"STUB_PROTOCOL_VERSION": "2.1.330"}, nil)
	code, out := old.launchAs(t, map[string]any{"way": "protocol"})
	if got, words := refusalOf(out); code != http.StatusConflict || got != "move_refused" || words != agentproto.NotQualifiedWords {
		t.Fatalf("an older version: %d %v", code, out)
	}
	if n := len(old.rt.Asked("StartTerminal")); n != 0 {
		t.Fatalf("%d terminals started for an older version", n)
	}

	// An agent with no protocol: nothing it could run as a chat.
	e := newTestEnv(t, nil)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "way": "protocol"})
	if got, words := refusalOf(out); resp.StatusCode != http.StatusConflict || got != "move_refused" || words != session.CannotMoveWords {
		t.Fatalf("an agent with no protocol: %d %v", resp.StatusCode, out)
	}
	if n := len(e.srv.registry.List()); n != 0 {
		t.Fatalf("%d sessions listed after a refused launch", n)
	}
}

// A launch as a chat starts its terminal as every launch does (I21), and its
// move to Chat is noted at the launch: the session says so, a viewer's
// welcome says it, and no chat's child is started.
func TestLaunchAsChatIsNotedAtLaunch(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launchedAsChat(t)
	if l.Way() != session.WayTerminal || l.Info().Way != "" {
		t.Fatalf("launched as a chat: way %q", l.Way())
	}
	if to, why := l.Noted(); to != session.WayProtocol || why != session.AfterFirstReplyWords {
		t.Fatalf("the noted move: %q %q", to, why)
	}
	if n := len(c.rt.Asked("StartTerminal")); n != 1 {
		t.Fatalf("%d terminals started", n)
	}
	sink := &wayFrames{}
	sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "Ada", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Detach(sub) })
	until(t, "the welcome's noted move", func() bool { return sink.saw(`"t":"welcome"`) && sink.saw(`"noted":"protocol"`) })
	if n := len(c.pipeSpecs()); n != 0 {
		t.Fatalf("%d chats started at the launch", n)
	}
}

// The noted move is made at the first turn's end, by the move's own path,
// and not before: a turn under way starts nothing; its end moves the
// session to Chat with the boundary's notice.
func TestLaunchAsChatMovesAtTheFirstTurnsEnd(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launchedAsChat(t)
	id := l.Info().ID
	c.working(t, id)
	// The report's change is applied, and a move it allowed begun, before
	// its reply: nothing has begun.
	if l.Moving() || l.Way() != session.WayTerminal || len(c.pipeSpecs()) != 0 {
		t.Fatalf("during the first turn: moving %v, way %q, %d chats", l.Moving(), l.Way(), len(c.pipeSpecs()))
	}
	if to, _ := l.Noted(); to != session.WayProtocol {
		t.Fatalf("the noted move during the first turn: %q", to)
	}
	c.turnEnded(t, id)
	moved(t, l, session.WayProtocol)
	if n := len(c.pipeSpecs()); n != 1 {
		t.Fatalf("%d chats started", n)
	}
	if to, _ := l.Noted(); to != "" {
		t.Fatalf("still noted after the move: %q", to)
	}
	entriesWhen(t, l, "the boundary's notice", func(es []session.TranscriptEntry) bool {
		return hasEntry(es, func(e session.TranscriptEntry) bool {
			return e.Kind == session.EntryNotice && e.Words == session.MovedToChatWords
		})
	})
}

// A launch's first message is typed into the terminal before the move: a
// turn's end while it still waits moves nothing; once it is no longer
// waiting the move is made.
func TestLaunchAsChatWaitsForItsFirstMessage(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launchedAsChat(t)
	id := l.Info().ID
	l.SetFirstMessage("waiting")
	c.turnEnded(t, id)
	if l.Moving() || l.Way() != session.WayTerminal || len(c.pipeSpecs()) != 0 {
		t.Fatalf("with its first message waiting: moving %v, way %q", l.Moving(), l.Way())
	}
	if _, why := l.Noted(); why != session.AfterFirstReplyWords {
		t.Fatalf("the noted move's sentence: %q", why)
	}
	l.SetFirstMessage("")
	moved(t, l, session.WayProtocol)
}

// A noted move that cannot be made leaves the session in its terminal, and
// the switch's line says why in its existing words: a session that became a
// crew member stays noted with the crew's sentence; a chat that did not
// start goes back to the terminal with the driver's.
func TestLaunchAsChatStaysInItsTerminal(t *testing.T) {
	t.Run("a crew member", func(t *testing.T) {
		c := newMoveEnv(t, nil, nil)
		l := c.launchedAsChat(t)
		id := l.Info().ID
		l.SetCrew(session.CrewRef{RunID: "run-1", Member: "writer"})
		c.turnEnded(t, id)
		if l.Moving() || l.Way() != session.WayTerminal || len(c.pipeSpecs()) != 0 {
			t.Fatalf("a crew member moved: moving %v, way %q", l.Moving(), l.Way())
		}
		if to, why := l.Noted(); to != session.WayProtocol || why != session.CrewStaysWords {
			t.Fatalf("the noted move: %q %q", to, why)
		}
	})
	t.Run("a chat that did not start", func(t *testing.T) {
		c := newMoveEnv(t, map[string]string{"STUB_PROTOCOL_INIT": "none"}, nil)
		c.srv.chatInitWait = 300 * time.Millisecond
		l := c.launchedAsChat(t)
		id := l.Info().ID
		sink := &wayFrames{}
		sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleView, Name: "Ada"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Detach(sub) })
		c.turnEnded(t, id)
		until(t, "back with the words", func() bool {
			return sink.saw(`"way":"terminal","why":"` + agentproto.NotStartedWords + `"`)
		})
		moved(t, l, session.WayTerminal)
		if l.Info().Status.Ended() {
			t.Fatal("the session ended")
		}
	})
}

// Pressing Terminal before the move takes the noted move back: the first
// turn's end then moves nothing.
func TestLaunchAsChatTakenBack(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launchedAsChat(t)
	id := l.Info().ID
	code, out := c.move(t, id, session.WayTerminal)
	if code != http.StatusOK || out["noted"] != nil {
		t.Fatalf("Terminal pressed: %d %v", code, out)
	}
	if to, _ := l.Noted(); to != "" {
		t.Fatalf("still noted: %q", to)
	}
	c.turnEnded(t, id)
	if l.Moving() || l.Way() != session.WayTerminal || len(c.pipeSpecs()) != 0 {
		t.Fatalf("moved after it was taken back: moving %v, way %q", l.Moving(), l.Way())
	}
}

// An older client's launch (no way) and a launch as a terminal note
// nothing: the first turn's end moves nothing, as before.
func TestLaunchWithoutAWayIsUnchanged(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	for _, body := range []map[string]any{{}, {"way": "terminal"}} {
		code, out := c.launchAs(t, body)
		if code != http.StatusCreated {
			t.Fatalf("%v: %d %v", body, code, out)
		}
		l := c.local(out["id"].(string))
		if to, _ := l.Noted(); to != "" {
			t.Fatalf("%v: noted %q", body, to)
		}
		c.turnEnded(t, l.Info().ID)
		if l.Moving() || l.Way() != session.WayTerminal {
			t.Fatalf("%v: moved", body)
		}
	}
	if n := len(c.pipeSpecs()); n != 0 {
		t.Fatalf("%d chats started", n)
	}
}
