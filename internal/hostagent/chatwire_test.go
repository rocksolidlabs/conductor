package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A chat's wire through a switchyard (docs/plans/chat-or-terminal.md,
// row 3): the publisher serves a published chat's viewers as the server
// serves its own, and sends no TRANSCRIPT to a switchyard that did not say
// it takes one.

// chatLocal is a server session in the protocol way with a fake driver, on
// a fake terminal, both ended at the test's end.
func chatLocal(t *testing.T) (*session.Local, *sessiontest.FakeProtocol) {
	t.Helper()
	p := sessiontest.NewFakeProc()
	l := session.NewLocal(session.Info{ID: "local-chat", Name: "chat", Kind: session.KindServer, AgentID: "claude", Command: []string{"claude"}, Cwd: t.TempDir(),
		Status: session.StatusRunning, Cols: 80, Rows: 24, CreatedAt: time.Now().UTC()}, p, session.Options{Transport: proto.TransportWS, FileView: "view", Log: discardLog})
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = l.Stop(context.Background())
		p.End(0)
	})
	return l, f
}

// publish publishes local to y as `conductor serve` does.
func publish(t *testing.T, y *stubYard, local *session.Local) (*Published, error) {
	t.Helper()
	u := &Uplink{ServerURL: y.srv.URL, HostName: "office", Log: discardLog}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := u.Publish(ctx, local)
	if err == nil {
		t.Cleanup(p.Stop)
	}
	return p, err
}

// guest joins a viewer to the published session over the relay with its
// hello (the mark it holds), and waits for its ready.
func (y *stubYard) guest(id string, role session.Role, link string, after uint64) {
	y.t.Helper()
	y.send(proto.ViewerJoin{T: proto.HostViewerJoin, ViewerID: id, Role: string(role), LinkID: link})
	y.send(proto.ViewerRef{T: proto.HostRelayStart, ViewerID: id})
	y.relay(id, proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: proto.ProtoVersion, Name: "Priya", TranscriptAfter: after}))
	y.wait("the guest's ready", func(m yardMsg) bool { return m.viewer == id && controlT(m) == proto.CtlReady })
}

// controlT is the t of a relayed control, "" for any other frame.
func controlT(m yardMsg) string {
	if m.inner.Type != proto.TypeControl {
		return ""
	}
	t, _ := proto.ParseHeader(m.inner.Payload)
	return t
}

// relayedJSON is a relayed CONTROL or TRANSCRIPT frame's message.
func relayedJSON(m yardMsg) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(m.inner.Payload, &out)
	return out
}

// names says whether m, relayed to viewer, answers reqID.
func names(m yardMsg, viewer, reqID string) bool {
	if m.viewer != viewer || (m.inner.Type != proto.TypeControl && m.inner.Type != proto.TypeTranscript) {
		return false
	}
	j := relayedJSON(m)
	return j["requestId"] == reqID || m.inner.Type == proto.TypeTranscript && j["reqId"] == reqID
}

// I14: a chat is not published to a switchyard whose registered lacks the
// word, in the words the session's line says; a session published there
// that became a chat sends it no TRANSCRIPT and serves it no viewer; to a
// switchyard that says the word, its frames pass.
func TestNoTranscriptToAnOlderSwitchyard(t *testing.T) {
	old := newStubYard(t)
	chat, _ := chatLocal(t)
	if _, err := publish(t, old, chat); !errors.Is(err, ErrOlderSwitchyard) || err.Error() != "Chats are not published: the switchyard is older than this server." {
		t.Fatalf("a chat published to an older switchyard: %v", err)
	}

	// A terminal session published to it, then a chat: no frame of the chat
	// is relayed, and its viewer is refused.
	old2 := newStubYard(t)
	p := sessiontest.NewFakeProc()
	term := session.NewLocal(session.Info{ID: "local-2", Name: "t", Kind: session.KindServer, Cwd: t.TempDir(), Status: session.StatusRunning, Cols: 80, Rows: 24},
		p, session.Options{Transport: proto.TransportWS, Log: discardLog})
	t.Cleanup(func() {
		_ = term.Stop(context.Background())
		p.End(0)
	})
	pub, err := publish(t, old2, term)
	if err != nil {
		t.Fatal(err)
	}
	<-old2.reg
	<-old2.conn
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(term, f.Start()); err != nil {
		t.Fatal(err)
	}
	f.Text("m1", "words", true)
	if err := pub.a.sendRelay("guestrelay000009", entryFrameOf(t, "m1"), nil); err != nil {
		t.Fatalf("a TRANSCRIPT frame for an older switchyard: %v", err)
	}
	// What is relayed for a viewer goes in order: once the frame sent after
	// it has arrived, a TRANSCRIPT frame sent would have too.
	if err := pub.a.sendRelay("guestrelay000009", proto.MustControl(proto.Ping{T: proto.CtlPong, TS: 1}), nil); err != nil {
		t.Fatal(err)
	}
	old2.wait("the frame sent after it", func(m yardMsg) bool { return m.viewer == "guestrelay000009" && controlT(m) == proto.CtlPong })
	old2.noTranscript(t)
	const guest = "guestrelay000010"
	old2.send(proto.ViewerJoin{T: proto.HostViewerJoin, ViewerID: guest, Role: string(session.RoleControl), LinkID: "link-1"})
	old2.send(proto.ViewerRef{T: proto.HostRelayStart, ViewerID: guest})
	old2.relay(guest, proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: proto.ProtoVersion}))
	refused := old2.wait("the chat's viewer refused", func(m yardMsg) bool { return m.t == proto.HostViewerError })
	if refused.raw["viewerId"] != guest || refused.raw["message"] != OlderSwitchyardWords {
		t.Fatalf("the refusal %v", refused.raw)
	}
	old2.noTranscript(t)

	// With the word: published, and its frames pass.
	y := newStubYard(t)
	y.transcript.Store(true)
	chat2, f2 := chatLocal(t)
	if _, err := publish(t, y, chat2); err != nil {
		t.Fatal(err)
	}
	<-y.reg
	<-y.conn
	f2.Text("m1", "words", true)
	y.guest("guestrelay000011", session.RoleView, "link-1", 0)
	page := y.wait("the replay", func(m yardMsg) bool { return m.viewer == "guestrelay000011" && m.inner.Type == proto.TypeTranscript })
	if j := relayedJSON(page); j["t"] != proto.TranscriptPageT || len(j["entries"].([]any)) != 1 {
		t.Fatalf("the replay %v", j)
	}
	f2.Text("m2", "live", true)
	y.wait("the live entry", func(m yardMsg) bool {
		return m.viewer == "guestrelay000011" && m.inner.Type == proto.TypeTranscript && relayedJSON(m)["t"] == proto.TranscriptEntryT
	})
}

// entryFrameOf is a TRANSCRIPT entry message naming ref.
func entryFrameOf(t *testing.T, ref string) []byte {
	t.Helper()
	f, err := proto.EncodeTranscript(proto.TranscriptEntryMsg{T: proto.TranscriptEntryT, Entry: json.RawMessage(`{"seq":1,"mark":1,"ref":"` + ref + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A published chat's viewer is served by the session's way: after its
// ready, the entries changed after the mark its hello holds; a read of a
// part returns the part; a controller's interrupt reaches the driver and a
// view link's answers read_only.
func TestUplinkServesProtocolMessages(t *testing.T) {
	y := newStubYard(t)
	y.transcript.Store(true)
	chat, f := chatLocal(t)
	if _, err := publish(t, y, chat); err != nil {
		t.Fatal(err)
	}
	<-y.reg
	<-y.conn
	f.TurnStarted("")
	for i := range 5 {
		f.Text(fmt.Sprintf("early%d", i), "before the mark", true)
	}
	entries, err := chat.Transcript(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	held := entries[len(entries)-1].Mark
	f.ToolStarted(session.ToolStep{Ref: "c1", Name: "Bash", Input: []byte(`{"command":"go test ./..."}`)})
	f.ToolEnded("c1", session.ToolResult{Status: session.ToolOK, Output: []byte("ok  conductor 1.2s")})

	const viewer, ctl = "guestrelay000021", "guestrelay000022"
	y.guest(viewer, session.RoleView, "link-1", held)
	page := y.wait("the replay after the mark", func(m yardMsg) bool { return m.viewer == viewer && m.inner.Type == proto.TypeTranscript })
	var replay struct {
		Entries []session.TranscriptEntry `json:"entries"`
	}
	_ = json.Unmarshal(page.inner.Payload, &replay)
	if len(replay.Entries) != 1 || replay.Entries[0].Ref != "c1" || replay.Entries[0].Status != session.ToolOK || replay.Entries[0].Mark <= held {
		t.Fatalf("replayed after mark %d: %+v", held, replay.Entries)
	}
	seq := replay.Entries[0].Seq
	y.relay(viewer, proto.MustControl(proto.TranscriptGet{T: proto.CtlTranscriptGet, ReqID: "p1", Part: &proto.TranscriptPartRef{Seq: seq, Name: proto.TranscriptPartOutput}}))
	part := y.wait("the part", func(m yardMsg) bool { return names(m, viewer, "p1") })
	var piece proto.TranscriptPartMsg
	if part.inner.Type != proto.TypeTranscript || json.Unmarshal(part.inner.Payload, &piece) != nil || piece.T != proto.TranscriptPartT || string(piece.Data) != "ok  conductor 1.2s" {
		t.Fatalf("the part %s", part.inner.Payload)
	}
	y.relay(viewer, proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "v1"}))
	if m := y.wait("the view link's interrupt", func(m yardMsg) bool { return names(m, viewer, "v1") }); relayedJSON(m)["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("a view link's interrupt %v", relayedJSON(m))
	}
	if n := sessiontest.Count(f.Calls(), "interrupt"); n != 0 {
		t.Fatalf("a view link's interrupt reached the driver (%d)", n)
	}
	y.guest(ctl, session.RoleControl, "link-2", ^uint64(0))
	y.relay(ctl, proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "c1"}))
	if m := y.wait("the controller's interrupt", func(m yardMsg) bool { return names(m, ctl, "c1") }); relayedJSON(m)["t"] != proto.CtlInterruptOK {
		t.Fatalf("a controller's interrupt %v", relayedJSON(m))
	}
	if n := sessiontest.Count(f.Calls(), "interrupt"); n != 1 {
		t.Fatalf("the driver was asked %d times", n)
	}
	// A message from the controller: answered once the agent took it.
	y.relay(ctl, proto.MustControl(proto.Submit{T: proto.CtlSubmit, ReqID: "s1", Text: "and then?"}))
	f.TurnEnded("interrupted", session.Usage{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !f.Await(ctx, func(c []string) bool { return sessiontest.Count(c, "send") == 1 }) {
		t.Fatalf("the message was never written: %v", f.Calls())
	}
	f.Accepted(f.Sent()[0].Ref)
	if m := y.wait("the message's word", func(m yardMsg) bool { return names(m, ctl, "s1") }); relayedJSON(m)["t"] != proto.CtlSubmitOK {
		t.Fatalf("the message %v", relayedJSON(m))
	}
}

// A terminal session's viewer (every session of `conductor host` among
// them) is answered not_protocol for interrupt and transcript_get, and the
// connection goes on.
func TestHostRefusesProtocolMessagesOnATerminal(t *testing.T) {
	y := newStubYard(t)
	y.transcript.Store(true)
	publishCat(t, y, t.TempDir(), []string{"/bin/cat"})
	const guest = "guestrelay000031"
	y.guest(guest, session.RoleControl, "link-1", 7)
	y.relay(guest, proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "i1"}))
	if m := y.wait("the interrupt's word", func(m yardMsg) bool { return names(m, guest, "i1") }); relayedJSON(m)["code"] != proto.ErrCodeNotProtocol || relayedJSON(m)["message"] != session.NotProtocolWords {
		t.Fatalf("an interrupt on a terminal %v", relayedJSON(m))
	}
	y.relay(guest, proto.MustControl(proto.TranscriptGet{T: proto.CtlTranscriptGet, ReqID: "g1"}))
	if m := y.wait("the read's word", func(m yardMsg) bool { return names(m, guest, "g1") }); relayedJSON(m)["code"] != proto.ErrCodeNotProtocol {
		t.Fatalf("a read on a terminal %v", relayedJSON(m))
	}
	y.relay(guest, proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 5}))
	y.wait("the pong after", func(m yardMsg) bool { return m.viewer == guest && controlT(m) == proto.CtlPong })
	y.noTranscript(t)
}

// noTranscript fails t if y was relayed a TRANSCRIPT frame.
func (y *stubYard) noTranscript(t *testing.T) {
	t.Helper()
	y.mu.Lock()
	defer y.mu.Unlock()
	for _, m := range y.seen {
		if m.inner.Type == proto.TypeTranscript {
			t.Fatalf("the switchyard was relayed a TRANSCRIPT frame for %s", m.viewer)
		}
	}
}

// I10 through a switchyard: a relayed viewer of a published chat, a control
// link's as much as a view link's, is never the operator: its
// permission_answer is not_answerer and its answer or line naming the
// request is refused with its own buttons' words; the request stays open
// and its driver is told nothing.
func TestARelayedViewerAnswersNoPermissionRequest(t *testing.T) {
	y := newStubYard(t)
	y.transcript.Store(true)
	chat, f := chatLocal(t)
	if _, err := publish(t, y, chat); err != nil {
		t.Fatal(err)
	}
	<-y.reg
	<-y.conn
	f.TurnStarted("")
	f.ToolStarted(session.ToolStep{Ref: "c1", Name: "Bash", Input: []byte(`{"command":"make"}`)})
	f.Permission(session.PermissionRequest{Ref: "req-1", Call: "c1", Tool: "Bash", Input: []byte(`{"command":"make"}`)})
	entries, err := chat.Transcript(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := entries[len(entries)-1]
	if r.Kind != session.EntryPermission || r.State != session.PermissionOpen {
		t.Fatalf("the request %+v", r)
	}
	const viewer, ctl = "guestrelay000031", "guestrelay000032"
	y.guest(viewer, session.RoleView, "link-1", ^uint64(0))
	y.guest(ctl, session.RoleControl, "link-2", ^uint64(0))
	for _, who := range []string{viewer, ctl} {
		y.relay(who, proto.MustControl(proto.PermissionAnswer{T: proto.CtlPermissionAnswer, ReqID: "p1", Question: r.Question, Call: r.Call, Sha256: r.InputSha256, Choice: proto.PermissionAllowOnce}))
		if m := y.wait("the permission_answer's word", func(m yardMsg) bool { return names(m, who, "p1") }); relayedJSON(m)["code"] != proto.ErrCodeNotAnswerer {
			t.Fatalf("%s's permission_answer %v", who, relayedJSON(m))
		}
	}
	y.relay(ctl, proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "a1", Question: r.Question, Input: "y\r"}))
	if m := y.wait("the answer's word", func(m yardMsg) bool { return names(m, ctl, "a1") }); relayedJSON(m)["code"] != proto.ErrCodePermission {
		t.Fatalf("a relayed answer %v", relayedJSON(m))
	}
	y.relay(ctl, proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "a2", Question: r.Question, Text: "yes"}))
	if m := y.wait("the line's word", func(m yardMsg) bool { return names(m, ctl, "a2") }); relayedJSON(m)["code"] != proto.ErrCodePermission {
		t.Fatalf("a relayed line %v", relayedJSON(m))
	}
	if n := sessiontest.Count(f.Calls(), "reply"); n != 0 {
		t.Fatalf("the driver was replied %d times", n)
	}
	if o, _ := chat.OpenPermissions(); o != 1 {
		t.Fatalf("%d open", o)
	}
}
