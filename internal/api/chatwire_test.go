package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A chat's wire on a server session's viewer socket
// (docs/plans/chat-or-terminal.md, row 3).

// chatSession is a server session in the protocol way with a fake driver,
// on a fake terminal: both are ended, and the session removed, at the
// test's end.
func chatSession(t *testing.T, e *testEnv) (string, *session.Local, *sessiontest.FakeProtocol) {
	t.Helper()
	p := sessiontest.NewFakeProc()
	l := session.NewLocal(session.Info{ID: session.NewID(), Name: "chat", Kind: session.KindServer, Cwd: t.TempDir(), Status: session.StatusRunning, Cols: 80, Rows: 24},
		p, session.Options{ScrollbackBytes: 4096, Transport: proto.TransportWS})
	if err := e.srv.registry.Add(l); err != nil {
		t.Fatal(err)
	}
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = l.Stop(context.Background())
		e.srv.registry.Remove(l.Info().ID)
		p.End(0)
	})
	return l.Info().ID, l, f
}

// chatFrame is one frame a chat's viewer read.
type chatFrame struct {
	typ byte
	m   map[string]any
}

// next reads the next frame; the bound only bounds a hang.
func (w *wsClient) next() chatFrame {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, data, err := w.c.Read(ctx)
	if err != nil {
		w.t.Fatalf("read: %v", err)
	}
	f, err := proto.Decode(data)
	if err != nil {
		w.t.Fatalf("a frame: %v", err)
	}
	c := chatFrame{typ: f.Type}
	if f.Type == proto.TypeControl || f.Type == proto.TypeTranscript {
		if err := json.Unmarshal(f.Payload, &c.m); err != nil {
			w.t.Fatalf("a frame of 0x%02x: %v", f.Type, err)
		}
	}
	return c
}

// names says whether c answers reqID: a control naming it as requestId, or
// a TRANSCRIPT reply naming it as reqId.
func (c chatFrame) names(reqID string) bool {
	return c.m != nil && (c.m["requestId"] == reqID || c.typ == proto.TypeTranscript && c.m["reqId"] == reqID)
}

// until reads frames until one ok takes, failing on one bad takes first.
func (w *wsClient) until(what string, ok, bad func(chatFrame) bool) chatFrame {
	w.t.Helper()
	for {
		c := w.next()
		if bad != nil && bad(c) {
			w.t.Fatalf("waiting for %s: %v (0x%02x) came first", what, c.m, c.typ)
		}
		if ok(c) {
			return c
		}
	}
}

func controlOf(t string) func(chatFrame) bool {
	return func(c chatFrame) bool { return c.typ == proto.TypeControl && c.m["t"] == t }
}

func helloChat(w *wsClient, after uint64) {
	w.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", TranscriptAfter: after}))
}

func transcriptGet(reqID string, m proto.TranscriptGet) []byte {
	m.T, m.ReqID = proto.CtlTranscriptGet, reqID
	return proto.MustControl(m)
}

// Five reads in flight on a connection that takes nothing: four are
// answered once it does, the fifth too_many_requests; places free as the
// replies are written. A page of a range the ring no longer keeps answers
// transcript_gone.
func TestTranscriptGetInFlight(t *testing.T) {
	e := newTestEnv(t, nil)
	var holding atomic.Bool
	release := make(chan struct{})
	e.srv.viewerSeam = &viewerSeam{localWrite: func() error {
		if holding.Load() {
			<-release
		}
		return nil
	}}
	id, l, f := chatSession(t, e)
	for i := range 5 {
		f.Text(fmt.Sprintf("m%d", i), "words", true)
	}
	v := dialViewer(t, e, id, adminToken)
	helloChat(v, ^uint64(0))
	if w := v.until("the welcome", controlOf(proto.CtlWelcome), nil); w.m["way"] != session.WayProtocol || w.m["interrupt"] != true {
		t.Fatalf("a chat's welcome %v", w.m)
	}
	v.until("ready", controlOf(proto.CtlReady), nil)
	holding.Store(true)
	for i := 1; i <= 5; i++ {
		v.send(transcriptGet(fmt.Sprintf("r%d", i), proto.TranscriptGet{After: 1}))
	}
	// The server has read all five once the chat post sent after them is kept.
	v.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Nonce: "n1", Text: "after the reads"}))
	for deadline := time.Now().Add(30 * time.Second); ; {
		if h := l.ChatHistory(); len(h) > 0 && h[len(h)-1].Text == "after the reads" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server never read the chat post")
		}
		time.Sleep(5 * time.Millisecond)
	}
	holding.Store(false)
	close(release)
	answered := map[string]chatFrame{}
	for len(answered) < 5 {
		c := v.next()
		for i := 1; i <= 5; i++ {
			if r := fmt.Sprintf("r%d", i); c.names(r) {
				answered[r] = c
			}
		}
	}
	for i := 1; i <= 4; i++ {
		if c := answered[fmt.Sprintf("r%d", i)]; c.typ != proto.TypeTranscript || c.m["t"] != proto.TranscriptPageT {
			t.Fatalf("read %d: %v", i, c.m)
		}
	}
	if c := answered["r5"]; c.typ != proto.TypeControl || c.m["code"] != proto.ErrCodeTooManyRequests {
		t.Fatalf("the fifth read: %v", c.m)
	}
	v.send(transcriptGet("r6", proto.TranscriptGet{After: 1}))
	if c := v.until("the read once the four were written", func(c chatFrame) bool { return c.names("r6") }, nil); c.typ != proto.TypeTranscript {
		t.Fatalf("a read once the four were written: %v", c.m)
	}

	// Past the ring: the first entries are no longer kept. The viewer that
	// watched so far leaves first, and another comes once the entries are
	// in: a viewer that reads nothing while 600 entries of the largest
	// size are sent to it is dropped as a slow consumer as soon as its
	// socket holds no more, which a Mac's loopback does long before
	// Linux's, and the reads below ask nothing of a viewer that watched.
	v.c.CloseNow()
	big := strings.Repeat("w", session.MaxEntryText)
	for i := range 600 {
		f.Text(fmt.Sprintf("big%d", i), big, true)
	}
	v = dialViewer(t, e, id, adminToken)
	helloChat(v, ^uint64(0))
	v.until("the welcome once the entries are in", controlOf(proto.CtlWelcome), nil)
	v.until("ready once the entries are in", controlOf(proto.CtlReady), nil)
	v.send(transcriptGet("g1", proto.TranscriptGet{After: 1}))
	if c := v.until("the read of a dropped range", func(c chatFrame) bool { return c.names("g1") }, nil); c.m["code"] != proto.ErrCodeTranscriptGone || c.m["message"] != session.TranscriptGoneWords {
		t.Fatalf("a read of a range no longer kept: %v", c.m)
	}
	v.send(transcriptGet("g2", proto.TranscriptGet{Part: &proto.TranscriptPartRef{Seq: 1, Name: proto.TranscriptPartOutput}}))
	if c := v.until("the read of a dropped part", func(c chatFrame) bool { return c.names("g2") }, nil); c.m["code"] != proto.ErrCodeTranscriptGone {
		t.Fatalf("a read of a part no longer kept: %v", c.m)
	}
	v.send(proto.Encode(proto.TypeControl, []byte(`{"t":"transcript_get","reqId":"b1","limit":101}`)))
	if c := v.until("a read past its bounds", func(c chatFrame) bool { return c.names("b1") }, nil); c.m["code"] != proto.ErrCodeBadFrame {
		t.Fatalf("a read past its bounds: %v", c.m)
	}
}

// interrupt is a controller's: a view link's answers read_only and reaches
// no driver; a controller's reaches the driver and answers interrupt_ok;
// with no turn it answers no_turn. On a terminal session interrupt and
// transcript_get answer not_protocol and the connection stays.
func TestInterruptRoles(t *testing.T) {
	e := newTestEnv(t, nil)
	id, _, f := chatSession(t, e)
	f.TurnStarted("")
	resp, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("link %d %v", resp.StatusCode, lo)
	}
	view := dialViewer(t, e, id, lo["token"].(string))
	helloChat(view, 0)
	view.until("ready", controlOf(proto.CtlReady), nil)
	view.send(proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "v1"}))
	if c := view.until("the view link's word", func(c chatFrame) bool { return c.names("v1") }, nil); c.m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("a view link's interrupt: %v", c.m)
	}
	// A view link reads the transcript.
	view.send(transcriptGet("v2", proto.TranscriptGet{}))
	if c := view.until("the view link's read", func(c chatFrame) bool { return c.names("v2") }, nil); c.typ != proto.TypeTranscript {
		t.Fatalf("a view link's read: %v", c.m)
	}
	if n := sessiontest.Count(f.Calls(), "interrupt"); n != 0 {
		t.Fatalf("a view link's interrupt reached the driver (%d)", n)
	}
	ctl := dialViewer(t, e, id, adminToken)
	helloChat(ctl, 0)
	ctl.until("ready", controlOf(proto.CtlReady), nil)
	ctl.send(proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "c1"}))
	if c := ctl.until("the controller's word", func(c chatFrame) bool { return c.names("c1") }, nil); c.m["t"] != proto.CtlInterruptOK {
		t.Fatalf("a controller's interrupt: %v", c.m)
	}
	if n := sessiontest.Count(f.Calls(), "interrupt"); n != 1 {
		t.Fatalf("the driver was asked %d times", n)
	}
	f.TurnEnded("interrupted", session.Usage{})
	ctl.send(proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "c2"}))
	if c := ctl.until("the word with no turn", func(c chatFrame) bool { return c.names("c2") }, nil); c.m["code"] != proto.ErrCodeNoTurn || c.m["message"] != session.NoTurnWords {
		t.Fatalf("an interrupt with no turn: %v", c.m)
	}
	ctl.send(proto.Encode(proto.TypeInput, []byte("ls\r")))
	if c := ctl.until("the keys' refusal", controlOf(proto.CtlError), nil); c.m["code"] != proto.ErrCodeNotTerminal || c.m["message"] != session.NotTerminalWords {
		t.Fatalf("keys on a chat: %v", c.m)
	}

	term := e.createSession("cat")
	w := dialViewer(t, e, term, adminToken)
	w.hello(0, 0)
	if m := w.expectControl(proto.CtlWelcome); m["way"] != nil || m["interrupt"] != nil || m["transcript"] != nil {
		t.Fatalf("a terminal's welcome %v", m)
	}
	w.expectControl(proto.CtlReady)
	w.send(proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "t1"}))
	if c := w.until("the terminal's word", func(c chatFrame) bool { return c.names("t1") }, nil); c.m["code"] != proto.ErrCodeNotProtocol || c.m["message"] != session.NotProtocolWords {
		t.Fatalf("an interrupt on a terminal: %v", c.m)
	}
	w.send(transcriptGet("t2", proto.TranscriptGet{}))
	if c := w.until("the terminal's read", func(c chatFrame) bool { return c.names("t2") }, nil); c.m["code"] != proto.ErrCodeNotProtocol {
		t.Fatalf("a read on a terminal: %v", c.m)
	}
	w.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 9}))
	if m := w.expectControl(proto.CtlPong); m["ts"] != float64(9) {
		t.Fatalf("the connection after: %v", m)
	}
}

// A message sent during a turn waits in the session's queue and the
// socket's read loop goes on: an interrupt sent after it is answered
// before any word on the message, which says submit_ok only once the turn
// has ended, the message was written and the agent took it.
func TestQueuedMessageFreesTheSocket(t *testing.T) {
	e := newTestEnv(t, nil)
	// Nothing waits on a submit's bound: a read loop held by the message
	// would hold the interrupt past any wait of the test's.
	e.srv.viewerSeam = &viewerSeam{submitWait: time.Hour}
	id, _, f := chatSession(t, e)
	f.TurnStarted("")
	c := dialViewer(t, e, id, adminToken)
	helloChat(c, 0)
	if w := c.until("the welcome", controlOf(proto.CtlWelcome), nil); w.m["submitAck"] != true {
		t.Fatalf("welcome %v", w.m)
	}
	c.until("ready", controlOf(proto.CtlReady), nil)
	c.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, ReqID: "s1", Text: "run the tests again"}))
	c.send(proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "i1"}))
	onSubmit := func(m chatFrame) bool { return m.names("s1") }
	if m := c.until("the interrupt's word", func(m chatFrame) bool { return m.names("i1") }, onSubmit); m.m["t"] != proto.CtlInterruptOK {
		t.Fatalf("the interrupt: %v", m.m)
	}
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("%d messages written during the turn", n)
	}
	f.TurnEnded("interrupted", session.Usage{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !f.Await(ctx, func(calls []string) bool { return sessiontest.Count(calls, "send") == 1 }) {
		t.Fatalf("the message was never written: %v", f.Calls())
	}
	sent := f.Sent()[0]
	if sent.Text != "run the tests again" {
		t.Fatalf("written %+v", sent)
	}
	f.Accepted(sent.Ref)
	if m := c.until("the message's word", onSubmit, nil); m.m["t"] != proto.CtlSubmitOK {
		t.Fatalf("the message: %v", m.m)
	}
}

// The seventeenth message queued during a turn is refused at once,
// too_many_requests in the plan's words; the sixteen before it wait,
// told nothing yet.
func TestQueuedSubmitBound(t *testing.T) {
	e := newTestEnv(t, nil)
	id, _, f := chatSession(t, e)
	f.TurnStarted("")
	c := dialViewer(t, e, id, adminToken)
	helloChat(c, 0)
	c.until("ready", controlOf(proto.CtlReady), nil)
	for i := 1; i <= session.MaxQueuedMessages+1; i++ {
		c.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, ReqID: fmt.Sprintf("q%d", i), Text: fmt.Sprintf("message %d", i)}))
	}
	last := fmt.Sprintf("q%d", session.MaxQueuedMessages+1)
	earlier := func(m chatFrame) bool {
		for i := 1; i <= session.MaxQueuedMessages; i++ {
			if m.names(fmt.Sprintf("q%d", i)) {
				return true
			}
		}
		return false
	}
	if m := c.until("the seventeenth's word", func(m chatFrame) bool { return m.names(last) }, earlier); m.m["code"] != proto.ErrCodeTooManyRequests || m.m["message"] != session.TooManyQueuedWords {
		t.Fatalf("the seventeenth message: %v", m.m)
	}
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("%d messages written during the turn", n)
	}
}
