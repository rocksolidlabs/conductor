package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A decision link's lane through a switchyard, on the publisher's side
// (lane.go, peer_lane.go, desk.go): the viewer's requests reach the
// publisher's Decisions with the viewer's link, and its replies the
// viewer, on a published session's connection and a run desk's; a host
// that serves none refuses them.

// fakeDecisions is a Decisions that answers each request at once and
// records what it was asked: a decision link is decision link's id
// "dl1", for the session "s" or the run "run-1".
type fakeDecisions struct {
	mu      sync.Mutex
	asked   []string
	changed func()
}

func (f *fakeDecisions) note(what, linkID, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, what+" "+linkID+" "+sessionID)
}

func (f *fakeDecisions) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeDecisions) List(_ context.Context, linkID, sessionID string, m proto.DecisionsList) []byte {
	f.note("list", linkID, sessionID)
	b, _ := proto.EncodeDecision(proto.Decision{ReqID: m.ReqID, List: json.RawMessage(`{"items":[],"total":0}`)})
	return b
}
func (f *fakeDecisions) Read(_ context.Context, linkID, sessionID string, m proto.DecisionRead) []byte {
	f.note("read", linkID, sessionID)
	b, _ := proto.EncodeDecision(proto.Decision{ReqID: m.ReqID, Item: json.RawMessage(`{"ref":"` + m.Ref + `"}`)})
	return b
}
func (f *fakeDecisions) Track(_ context.Context, linkID, sessionID string, m proto.DecisionTrack) []byte {
	f.note("track", linkID, sessionID)
	b, _ := proto.EncodeDecision(proto.Decision{ReqID: m.ReqID, Track: json.RawMessage(`{"steps":[]}`)})
	return b
}
func (f *fakeDecisions) Record(_ context.Context, linkID, sessionID string, m proto.DecisionRecord) []byte {
	f.note("record", linkID, sessionID)
	return laneRefusal(m.ReqID, proto.DecideUnsupported, "none")
}
func (f *fakeDecisions) Evidence(_ context.Context, linkID, sessionID string, m proto.DecisionEvidence) []byte {
	f.note("evidence", linkID, sessionID)
	b, _ := proto.EncodeDecision(proto.Decision{ReqID: m.ReqID, Evidence: json.RawMessage(`{}`)})
	return b
}
func (f *fakeDecisions) Decide(_ context.Context, linkID, sessionID string, m proto.Decide, answer func(context.Context, proto.Answer) error) []byte {
	what := "decide"
	if answer != nil {
		what = "decide own"
	}
	f.note(what, linkID, sessionID)
	return proto.MustControl(proto.DecideOK{T: proto.CtlDecideOK, ReqID: m.ReqID, Ref: m.Ref, Receipt: m.OpID})
}
func (f *fakeDecisions) Watch(linkID, sessionID string, changed func()) func() {
	f.note("watch", linkID, sessionID)
	f.mu.Lock()
	f.changed = changed
	f.mu.Unlock()
	return func() {}
}
func (f *fakeDecisions) DecisionLink(linkID, sessionID, runID string) bool {
	return linkID == "dl1" && (sessionID == "s" || (sessionID == "" && runID == "run-1"))
}

// laneViewer is a viewer on a peer's data channel, as a test reads it.
type laneViewer struct {
	t      *testing.T
	dc     *webrtc.DataChannel
	frames <-chan []byte
}

func (v *laneViewer) send(f []byte) {
	v.t.Helper()
	if err := v.dc.Send(f); err != nil {
		v.t.Fatal(err)
	}
}

// next is the next frame of the type want, the others passed over.
func (v *laneViewer) next(want byte) proto.Frame {
	v.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case raw := <-v.frames:
			f, err := proto.Decode(raw)
			if err != nil {
				v.t.Fatal(err)
			}
			if f.Type == want {
				return f
			}
		case <-deadline:
			v.t.Fatalf("no frame of type 0x%02x", want)
		}
	}
}

// control is the next control of type typ, the others passed over.
func (v *laneViewer) control(typ string) map[string]any {
	v.t.Helper()
	for {
		f := v.next(proto.TypeControl)
		var m map[string]any
		_ = json.Unmarshal(f.Payload, &m)
		if m["t"] == typ {
			return m
		}
	}
}

func (v *laneViewer) decide(reqID string) {
	v.t.Helper()
	f, err := proto.EncodeDecide(proto.Decide{ReqID: reqID, Ref: "task:run-1/build", Action: "retry", Revision: "7", OpID: "op-" + reqID})
	if err != nil {
		v.t.Fatal(err)
	}
	v.send(f)
}

// laneAgent is a host whose session "s" (a cat) has one viewer on a data
// channel, through the link linkID, its lane served by d (nil: none).
func laneAgent(t *testing.T, d Decisions, linkID string) (*agent, *laneViewer) {
	t.Helper()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(t.Context(), time.Second) })
	local := session.NewLocal(session.Info{ID: "s", Cols: 80, Rows: 24}, proc, session.Options{Log: discardLog})
	out := make(chan any, 64)
	a := &agent{opts: Options{Decisions: d, Switchyard: true}, local: local, peers: map[string]*peer{}, log: discardLog}
	a.sendHook = func(v any) { out <- v }
	p := newPeer(a, "0123456789abcdef", session.RoleControl, linkID, "phone", false)
	a.peers[p.id] = p
	if err := p.startWebRTC(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	dc, frames := loopbackViewer(t, p, out)
	return a, &laneViewer{t: t, dc: dc, frames: frames}
}

// A decision link's viewer of a published session is welcomed with
// decisions, and its lane's reads and decides reach the publisher's
// Decisions with its link and the session's own id, every answer through
// the question's claim as the link (no own answer); the replies, a
// TypeDecision frame or a control, reach the viewer, and a change the
// watch sees is decisions_changed.
func TestPeerSendsDecisionFrames(t *testing.T) {
	d := &fakeDecisions{}
	_, v := laneAgent(t, d, "dl1")
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24}))
	if w := v.control(proto.CtlWelcome); w["decisions"] != true {
		t.Fatalf("welcome %v", w)
	}
	v.send(proto.MustControl(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: "r1"}))
	f := v.next(proto.TypeDecision)
	var dec proto.Decision
	if err := json.Unmarshal(f.Payload, &dec); err != nil || dec.ReqID != "r1" || dec.List == nil {
		t.Fatalf("the list's reply: %s %v", f.Payload, err)
	}
	v.decide("r2")
	if ok := v.control(proto.CtlDecideOK); ok["reqId"] != "r2" || ok["receipt"] != "op-r2" {
		t.Fatalf("the decide's reply: %v", ok)
	}
	d.mu.Lock()
	changed := d.changed
	d.mu.Unlock()
	changed()
	if c := v.control(proto.CtlDecisionsChanged); c["seq"] != float64(1) {
		t.Fatalf("changed %v", c)
	}
	want := map[string]bool{"watch dl1 s": true, "list dl1 s": true, "decide dl1 s": true}
	for _, s := range d.seen() {
		if !want[s] {
			t.Fatalf("Decisions was asked %q (all: %q)", s, d.seen())
		}
		delete(want, s)
	}
	if len(want) != 0 {
		t.Fatalf("Decisions was never asked %v", want)
	}
}

// A viewer of a link that is no decision link here (another link of the
// switchyard, a view link) is welcomed without decisions and refused
// every lane message not_decision_link, Decisions never asked.
func TestPeerRefusesDecideWithoutDecisions(t *testing.T) {
	d := &fakeDecisions{}
	_, v := laneAgent(t, d, "l2")
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24}))
	if w := v.control(proto.CtlWelcome); w["decisions"] != nil {
		t.Fatalf("welcome %v", w)
	}
	v.send(proto.MustControl(proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: "r1", Ref: "task:run-1/build"}))
	if r := v.control(proto.CtlDecideRefused); r["reqId"] != "r1" || r["code"] != proto.DecideNotDecisionLink {
		t.Fatalf("a read: %v", r)
	}
	v.decide("r2")
	if r := v.control(proto.CtlDecideRefused); r["reqId"] != "r2" || r["code"] != proto.DecideNotDecisionLink {
		t.Fatalf("a decide: %v", r)
	}
	if s := d.seen(); len(s) != 0 {
		t.Fatalf("Decisions was asked %q", s)
	}
}

// `conductor host` serves no decisions (Options.Decisions nil): its
// viewers are welcomed without them and every lane message is refused
// unsupported, no TypeDecision frame sent.
func TestConductorHostRefusesTheLane(t *testing.T) {
	_, v := laneAgent(t, nil, "dl1")
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24}))
	if w := v.control(proto.CtlWelcome); w["decisions"] != nil {
		t.Fatalf("welcome %v", w)
	}
	v.send(proto.MustControl(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: "r1"}))
	if r := v.control(proto.CtlDecideRefused); r["reqId"] != "r1" || r["code"] != proto.DecideUnsupported {
		t.Fatalf("a list: %v", r)
	}
	v.decide("r2")
	if r := v.control(proto.CtlDecideRefused); r["reqId"] != "r2" || r["code"] != proto.DecideUnsupported {
		t.Fatalf("a decide: %v", r)
	}
	// Nothing after the two refusals was a decision: a ping's pong is the
	// next frame of all.
	v.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 5}))
	for {
		f := v.next(proto.TypeControl)
		if typ, _ := proto.ParseHeader(f.Payload); typ == proto.CtlPong {
			break
		}
	}
	select {
	case raw := <-v.frames:
		if f, _ := proto.Decode(raw); f.Type == proto.TypeDecision {
			t.Fatal("a decision frame was sent")
		}
	default:
	}
}

// A desk's viewer of the run's decision link is welcomed with decisions
// and nothing of a terminal, and its lane reaches Decisions with no
// session; terminal input is a protocol error there; a viewer of any other
// link is refused not_decision_link.
func TestADesksViewerHasTheLaneAlone(t *testing.T) {
	d := &fakeDecisions{}
	deskAgent := func(linkID string) (*agent, *laneViewer, chan proto.ViewerError) {
		out := make(chan any, 64)
		errs := make(chan proto.ViewerError, 4)
		a := &agent{opts: Options{Decisions: d, Switchyard: true}, peers: map[string]*peer{}, log: discardLog, desk: &proto.DeskRun{ID: "run-1", Name: "Run"}}
		a.sendHook = func(v any) {
			if e, ok := v.(proto.ViewerError); ok {
				errs <- e
				return
			}
			out <- v
		}
		p := newPeer(a, "0123456789abcdef", session.RoleControl, linkID, "phone", false)
		a.peers[p.id] = p
		if err := p.startWebRTC(nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.close)
		dc, frames := loopbackViewer(t, p, out)
		return a, &laneViewer{t: t, dc: dc, frames: frames}, errs
	}
	_, v, errs := deskAgent("dl1")
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1}))
	w := v.control(proto.CtlWelcome)
	if w["decisions"] != true || w["role"] != "control" || w["cols"] != float64(0) || w["subscriberId"] != nil {
		t.Fatalf("a desk's welcome %v", w)
	}
	v.send(proto.MustControl(proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: "r1", Ref: "task:run-1/build"}))
	if f := v.next(proto.TypeDecision); !strings.Contains(string(f.Payload), `"reqId":"r1"`) {
		t.Fatalf("the read's reply %s", f.Payload)
	}
	if !strings.Contains(strings.Join(d.seen(), ","), "read dl1 ") {
		t.Fatalf("Decisions was asked %q", d.seen())
	}
	v.send(proto.Encode(proto.TypeInput, []byte("ls\n")))
	select {
	case e := <-errs:
		if e.Code != proto.ErrCodeBadFrame {
			t.Fatalf("terminal input at a desk: %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("terminal input at a desk was taken")
	}

	_, other, errs := deskAgent("l2")
	other.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1}))
	select {
	case e := <-errs:
		if e.Code != proto.DecideNotDecisionLink {
			t.Fatalf("another link at a desk: %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("another link's viewer was let in at a desk")
	}
}

// The uplink hands its Decisions and LinkGone to each publication and each
// desk it opens (TestUplinkPassesDecisions in internal/cli checks the
// serve command sets them from the API's).
func TestUplinkPassesDecisions(t *testing.T) {
	d := &fakeDecisions{}
	gone := make(chan string, 1)
	u := &Uplink{ServerURL: "https://switchyard.example.net", Decisions: d, LinkGone: func(id string) { gone <- id }}
	for _, opts := range []Options{u.options(session.Info{ID: "s", Name: "cat"}), u.options(session.Info{Name: "Run"})} {
		if opts.Decisions != Decisions(d) || opts.LinkGone == nil || !opts.Switchyard {
			t.Fatalf("options %+v", opts)
		}
		opts.LinkGone("l9")
		if got := <-gone; got != "l9" {
			t.Fatalf("link gone %q", got)
		}
	}
}

// At an older switchyard, which closes a register_desk ("register
// expected") and says nothing of decisions in its registered, no desk
// opens (ErrNoDesks) and a publication says it carries no decisions.
func TestNoDecisionLinkAtAnOlderSwitchyard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var reg proto.Register
		if json.Unmarshal(data, &reg) != nil || reg.T != proto.HostRegister {
			c.Close(proto.CloseProtocolError, "register expected")
			return
		}
		b, _ := json.Marshal(proto.Registered{T: proto.HostRegistered, SessionID: "h1", Secret: "x", ShareBaseURL: "https://old.example.net", Links: []string{}})
		_ = c.Write(r.Context(), websocket.MessageText, b)
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	u := &Uplink{ServerURL: srv.URL, Insecure: true, RelayOnly: true, Decisions: &fakeDecisions{}}
	if _, err := u.OpenDesk(t.Context(), proto.DeskRun{ID: "run-1", Name: "Run"}); !errors.Is(err, ErrNoDesks) {
		t.Fatalf("a desk at an older switchyard: %v", err)
	}
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(t.Context(), time.Second) })
	local := session.NewLocal(session.Info{ID: "s", Command: []string{"cat"}, Cols: 80, Rows: 24}, proc, session.Options{Log: discardLog})
	pub, err := u.Publish(t.Context(), local)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pub.Stop)
	if pub.Decisions() {
		t.Fatal("a publication at an older switchyard says it carries decisions")
	}
}
