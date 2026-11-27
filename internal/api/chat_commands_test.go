package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The agent's own commands over real sockets (docs/plans/chat-or-terminal.md,
// row 12): the operator's own connection is sent them after its replay and
// on a change; a control link's and a view link's never are; the session's
// Info, the sessions listing and the server's log hold no command's name.
func TestCommandsGoToTheOperatorAloneOverTheSocket(t *testing.T) {
	logs := &syncBuffer{}
	e := newTestEnvLogging(t, nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	id, l, f := markedChat(t, e)
	_, control, err := e.srv.links.Create(id, session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, view, err := e.srv.links.Create(id, session.RoleView, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.Commands([]session.Command{{Name: "compact", Words: "Shorten the conversation"}, {Name: "fix-issue-secret-name", Hint: "<number>"}})

	isCommands := func(c chatFrame) bool { return c.typ == proto.TypeTranscript && c.m["t"] == proto.TranscriptCommandsT }
	op := dialViewer(t, e, id, adminToken)
	helloChat(op, ^uint64(0))
	op.until("ready", controlOf(proto.CtlReady), nil)
	first := op.until("the commands after the replay", isCommands, nil)
	if list, _ := first.m["commands"].([]any); len(list) != 2 || first.m["rev"] != float64(1) {
		t.Fatalf("the operator was sent %v", first.m)
	}

	guests := []*wsClient{dialViewer(t, e, id, control), dialViewer(t, e, id, view)}
	for _, g := range guests {
		helloChat(g, ^uint64(0))
		g.until("ready", controlOf(proto.CtlReady), isCommands)
	}
	// A change: the operator is sent it; then a ping each guest answers
	// after anything the session sent it before, none of it the commands.
	f.Commands([]session.Command{{Name: "compact"}})
	if c := op.until("the change", isCommands, nil); c.m["rev"] != float64(2) {
		t.Fatalf("the change %v", c.m)
	}
	for _, g := range guests {
		g.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 7}))
		g.until("the pong", controlOf(proto.CtlPong), isCommands)
	}

	raw, _ := json.Marshal(l.Info())
	resp, err := e.client.Do(func() *http.Request {
		r, _ := http.NewRequest(http.MethodGet, e.http.URL+"/api/sessions", nil)
		r.Header.Set("Authorization", "Bearer "+adminToken)
		return r
	}())
	if err != nil {
		t.Fatal(err)
	}
	listing, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for what, b := range map[string]string{"Info": string(raw), "the listing": string(listing), "the log": logs.String()} {
		if strings.Contains(b, "fix-issue-secret-name") {
			t.Fatalf("%s holds a command's name", what)
		}
	}
	if !strings.Contains(string(listing), id) {
		t.Fatalf("the listing does not list the chat: %s", listing)
	}
}
