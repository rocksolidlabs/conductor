package hostagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// I19 through the host link (docs/plans/chat-or-terminal.md, row 12): a
// viewer with no link, from a server the person said is their own, is one
// of the owner's windows on the host, and yet its line never goes to a
// chat's agent without the as-written mark, whatever its submit asks; its
// welcome says no expand. Only the server's own viewer socket may ask.
func TestAPeersSubmitIsNeverExpanded(t *testing.T) {
	a, s, _, viewer := relayCredHost(t)
	a.opts.ServerIsOwners = true
	// The server takes TRANSCRIPT (its registered said so), as one that serves a chat does.
	a.transcript.Store(true)
	f := sessiontest.NewFakeProtocol()
	f.ProtocolName, f.IsVerbatim = "claude-stream-json", true
	if err := sessiontest.EnterProtocol(a.local, f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.local.Stop(context.Background()) })
	const id = "0123456789abcdef"
	p := viewer(id, "")
	if !p.owner {
		t.Fatal("the viewer with no link from the owner's server is not the owner's window")
	}
	welcome := relayedTo(t, s, id, func(f proto.Frame) bool {
		var m map[string]any
		return f.Type == proto.TypeControl && json.Unmarshal(f.Payload, &m) == nil && m["t"] == proto.CtlWelcome
	})
	var w map[string]any
	if err := json.Unmarshal(welcome.Payload, &w); err != nil || w["way"] != session.WayProtocol || w["expand"] != nil {
		t.Fatalf("the peer's welcome %v (%v)", w, err)
	}
	for _, line := range []string{
		`{"t":"submit","text":"/compact","reqId":"p1","expand":true}`,
		`{"t":"submit","text":"@notes.txt","expand":true}`,
	} {
		p.handleFrame(proto.Frame{Type: proto.TypeControl, Payload: []byte(line)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !f.Await(ctx, func(c []string) bool { return sessiontest.Count(c, "send") >= 1 }) {
		t.Fatalf("no write: %v", f.Calls())
	}
	f.Accepted(f.Sent()[0].Ref)
	f.TurnStarted("")
	f.TurnEnded("done", session.Usage{})
	if !f.Await(ctx, func(c []string) bool { return sessiontest.Count(c, "send") >= 2 }) {
		t.Fatalf("no second write: %v", f.Calls())
	}
	for _, m := range f.Sent() {
		if m.Expand {
			t.Fatalf("a peer's line went unmarked: %+v", m)
		}
	}
}

// Row 12: the agent's own commands never reach a viewer through the host
// link, the owner's window from the owner's own server included: not after
// its replay, not on a change.
func TestAPeerIsNeverSentTheCommands(t *testing.T) {
	a, s, _, viewer := relayCredHost(t)
	a.opts.ServerIsOwners = true
	a.transcript.Store(true)
	f := sessiontest.NewFakeProtocol()
	f.ProtocolName, f.IsVerbatim = "claude-stream-json", true
	if err := sessiontest.EnterProtocol(a.local, f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.local.Stop(context.Background()) })
	f.Commands([]session.Command{{Name: "compact"}})
	const id = "0123456789abcdef"
	p := viewer(id, "")
	f.Commands([]session.Command{{Name: "compact"}, {Name: "clear"}})
	p.handleFrame(proto.Frame{Type: proto.TypeControl, Payload: mustJSON(proto.Ping{T: proto.CtlPing, TS: 9})})
	relayedTo(t, s, id, func(f proto.Frame) bool {
		var m map[string]any
		_ = json.Unmarshal(f.Payload, &m)
		if f.Type == proto.TypeTranscript && m["t"] == proto.TranscriptCommandsT {
			t.Fatalf("a peer was sent the commands: %s", f.Payload)
		}
		return f.Type == proto.TypeControl && m["t"] == proto.CtlPong
	})
}
