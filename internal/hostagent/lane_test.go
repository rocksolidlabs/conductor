package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
func (f *fakeDecisions) Decide(_ context.Context, linkID, sessionID string, m proto.Decide, answer func(context.Context, proto.Answer) error, stands func() error) []byte {
	what := "decide"
	if answer != nil {
		what = "decide own"
	}
	f.note(what, linkID, sessionID)
	// The operation's commit boundary asks the connection's own check, as
	// the API's does (laneCommand's Stands, the answer's hold).
	if stands == nil {
		return laneRefusal(m.ReqID, proto.DecideInvalid, "no check of the connection's own was given")
	}
	if err := stands(); err != nil {
		return laneRefusal(m.ReqID, proto.DecideRevoked, err.Error())
	}
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
	return laneAgentWith(t, d, linkID, nil)
}

// laneAgentWith is laneAgent with setup run on the agent and its viewer's
// peer before the peer is seen.
func laneAgentWith(t *testing.T, d Decisions, linkID string, setup func(*agent, *peer)) (*agent, *laneViewer) {
	t.Helper()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(t.Context(), time.Second) })
	local := session.NewLocal(session.Info{ID: "s", Cols: 80, Rows: 24}, proc, session.Options{Log: discardLog})
	out := make(chan any, 64)
	a := &agent{opts: Options{Decisions: d, Switchyard: true}, local: local, peers: map[string]*peer{}, log: discardLog}
	// The switchyard's registered said it carries decisions.
	a.decisions.Store(true)
	a.sendHook = func(v any) { out <- v }
	p := newPeer(a, "0123456789abcdef", session.RoleControl, linkID, "phone", false)
	if setup != nil {
		setup(a, p)
	}
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
		a.decisions.Store(true)
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
	u := &Uplink{ServerURL: "https://switchyard.example.net", Decisions: d, LinkGone: func(id, sessionID, runID string, ended bool) {
		gone <- fmt.Sprint(id, " ", sessionID, " ", runID, " ", ended)
	}}
	for _, opts := range []Options{u.options(session.Info{ID: "s", Name: "cat"}), u.options(session.Info{Name: "Run"})} {
		if opts.Decisions != Decisions(d) || opts.LinkGone == nil || !opts.Switchyard {
			t.Fatalf("options %+v", opts)
		}
		opts.LinkGone("l9", "s", "", true)
		if got := <-gone; got != "l9 s  true" {
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

// A decide through the lane asks, at its operation's commit boundary, the
// viewer's own check as its terminal writes do (Decisions.Decide's
// stands): a viewer who joined through a link is acted on only while its
// host vouches for its switchyard, which would have told it of a revoke
// there (serverQuiet), and while the link is held here (a link_gone
// withdraws it): refused revoked otherwise, nothing done.
func TestALaneActAsksTheViewersOwnCheck(t *testing.T) {
	d := &fakeDecisions{}
	var cred *linkCred
	_, v := laneAgentWith(t, d, "dl1", func(_ *agent, p *peer) {
		cred = &linkCred{}
		p.cred = cred
	})
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24}))
	if w := v.control(proto.CtlWelcome); w["decisions"] != true {
		t.Fatalf("welcome %v", w)
	}
	// No ping of the host's answered: it does not vouch for its switchyard.
	v.decide("r1")
	r := v.control(proto.CtlDecideRefused)
	if r["reqId"] != "r1" || r["code"] != proto.DecideRevoked || !strings.Contains(fmt.Sprint(r["message"]), "heard") {
		t.Fatalf("a decide while the host does not vouch: %v", r)
	}
	// The link gone here: refused for that, whatever the vouch.
	cred.goneFor(session.ErrRevoked)
	v.decide("r2")
	if r := v.control(proto.CtlDecideRefused); r["reqId"] != "r2" || r["code"] != proto.DecideRevoked {
		t.Fatalf("a decide after the link went: %v", r)
	}
}

// A switchyard whose registered no longer says it carries decisions (a
// reconnect to one rolled back, or built without them) is sent no lane:
// a decision link's viewer is welcomed without decisions, every lane
// message is refused unsupported (a control, which any switchyard
// relays), and no TypeDecision frame goes into its relay; a desk's
// viewer is refused at its hello.
func TestNoLaneWhileTheSwitchyardSaysNoDecisions(t *testing.T) {
	d := &fakeDecisions{}
	_, v := laneAgentWith(t, d, "dl1", func(a *agent, _ *peer) { a.decisions.Store(false) })
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
	if s := d.seen(); len(s) != 0 {
		t.Fatalf("Decisions was asked %q", s)
	}

	errs := make(chan proto.ViewerError, 4)
	a := &agent{opts: Options{Decisions: d, Switchyard: true}, peers: map[string]*peer{}, log: discardLog, desk: &proto.DeskRun{ID: "run-1", Name: "Run"}}
	a.sendHook = func(v any) {
		if e, ok := v.(proto.ViewerError); ok {
			errs <- e
		}
	}
	p := newPeer(a, "0123456789abcdef", session.RoleControl, "dl1", "phone", false)
	a.peers[p.id] = p
	t.Cleanup(p.close)
	p.handleDesk(proto.Frame{Type: proto.TypeControl, Payload: proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: proto.ProtoVersion})[1:]}, nil)
	select {
	case e := <-errs:
		if e.Code != proto.DecideNotDecisionLink {
			t.Fatalf("a desk's viewer at a switchyard without decisions: %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a desk's viewer was let in at a switchyard without decisions")
	}
}

// blockedSink is a desk viewer's transport whose writes wait until it is
// let go (a relay out of room), or fail (fail set).
type blockedSink struct {
	let   chan struct{}
	wrote chan []byte
	fail  bool
}

func (b *blockedSink) WriteFrame(f []byte) error {
	if b.fail {
		return errors.New("the channel closed")
	}
	<-b.let
	b.wrote <- f
	return nil
}
func (b *blockedSink) Close(error) {}

// A desk's replies are written off the host's read loop, by the viewer's
// own writer: a write that waits for the relay's room (which a
// viewer_behind read by that loop gives back) holds no frame the loop
// reads, the next pings are taken at once, and the writes go out in their
// order once there is room; a viewer whose queue fills is let go
// (slow_consumer), as is one whose transport fails.
func TestADesksRepliesWaitOffTheReadLoop(t *testing.T) {
	desk := func(sink session.Sink) (*peer, chan proto.ViewerError) {
		errs := make(chan proto.ViewerError, 4)
		a := &agent{opts: Options{Decisions: &fakeDecisions{}, Switchyard: true}, peers: map[string]*peer{}, log: discardLog, desk: &proto.DeskRun{ID: "run-1", Name: "Run"}}
		a.decisions.Store(true)
		a.sendHook = func(v any) {
			if e, ok := v.(proto.ViewerError); ok {
				errs <- e
			}
		}
		p := newPeer(a, "0123456789abcdef", session.RoleControl, "dl1", "phone", false)
		a.peers[p.id] = p
		p.deskSink = sink
		t.Cleanup(p.close)
		return p, errs
	}
	ping := func(p *peer, ts int64) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.handleDesk(proto.Frame{Type: proto.TypeControl, Payload: proto.MustControl(proto.Ping{T: proto.CtlPing, TS: ts})[1:]}, nil)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("ping %d held the read loop", ts)
		}
	}
	sink := &blockedSink{let: make(chan struct{}), wrote: make(chan []byte, deskQueue+2)}
	p, errs := desk(sink)
	ping(p, 1)
	ping(p, 2)
	close(sink.let)
	for _, want := range []float64{1, 2} {
		select {
		case f := <-sink.wrote:
			var m map[string]any
			if err := json.Unmarshal(f[1:], &m); err != nil || m["t"] != proto.CtlPong || m["ts"] != want {
				t.Fatalf("written %s (%v), want pong %v", f, err, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("pong %v never written", want)
		}
	}
	select {
	case e := <-errs:
		t.Fatalf("a viewer whose writes only waited was let go: %+v", e)
	default:
	}

	held := &blockedSink{let: make(chan struct{}), wrote: make(chan []byte, deskQueue+2)}
	t.Cleanup(func() { close(held.let) })
	p, errs = desk(held)
	for ts := int64(1); ts <= deskQueue+2; ts++ {
		ping(p, ts)
	}
	select {
	case e := <-errs:
		if e.Code != proto.ErrCodeSlowConsumer {
			t.Fatalf("a full queue: %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a desk's viewer that reads nothing was not let go")
	}

	p, errs = desk(&blockedSink{fail: true})
	ping(p, 1)
	select {
	case e := <-errs:
		if e.Code != proto.ErrCodeSlowConsumer || !strings.Contains(e.Message, "the channel closed") {
			t.Fatalf("a failed write: %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a desk's viewer whose transport failed was not let go")
	}
}

// A link_gone is handed on with what the connection carries: a published
// session's own id, or a desk's run, so the publisher ends a run link
// only on its desk's word (a member's says the member left the group).
func TestLinkGoneSaysWhatItsConnectionCarries(t *testing.T) {
	gone := make(chan string, 2)
	note := func(id, sessionID, runID string, ended bool) {
		gone <- fmt.Sprint(id, "|", sessionID, "|", runID, "|", ended)
	}
	sessionAgent, _ := laneAgentWith(t, &fakeDecisions{}, "dl1", func(a *agent, _ *peer) { a.opts.LinkGone = note })
	if err := sessionAgent.handleControl(t.Context(), mustJSON(proto.LinkGone{T: proto.HostLinkGone, LinkID: "dl1"})); err != nil {
		t.Fatal(err)
	}
	if got := <-gone; got != "dl1|s||false" {
		t.Fatalf("a session's link_gone: %q", got)
	}
	desk := &agent{opts: Options{LinkGone: note, Switchyard: true}, peers: map[string]*peer{}, log: discardLog, desk: &proto.DeskRun{ID: "run-1", Name: "Run"}}
	if err := desk.handleControl(t.Context(), mustJSON(proto.LinkGone{T: proto.HostLinkGone, LinkID: "dl1", Ended: true})); err != nil {
		t.Fatal(err)
	}
	if got := <-gone; got != "dl1||run-1|true" {
		t.Fatalf("a desk's link_gone: %q", got)
	}
}

// A link the server says it ended of itself (link_gone, ended) leaves what
// the connection says the server holds (held) before the publisher is
// told, so a mint the word crosses finds it dropped once it has recorded
// its link; any other link_gone, and one for a link it never held, change
// nothing of it.
func TestAnEndedLinkLeavesTheHeldLinks(t *testing.T) {
	held := make(chan []string, 4)
	a, _ := laneAgentWith(t, &fakeDecisions{}, "dl1", func(x *agent, _ *peer) {
		x.held, x.heldKnown = map[string]bool{"l1": true, "l2": true}, true
		x.opts.LinkGone = func(string, string, string, bool) {
			ids, _ := x.heldLinks()
			slices.Sort(ids)
			held <- ids
		}
	})
	for _, c := range []struct {
		m    proto.LinkGone
		held []string
	}{{proto.LinkGone{LinkID: "l2"}, []string{"l1", "l2"}}, {proto.LinkGone{LinkID: "l9", Ended: true}, []string{"l1", "l2"}}, {proto.LinkGone{LinkID: "l1", Ended: true}, []string{"l2"}}} {
		c.m.T = proto.HostLinkGone
		if err := a.handleControl(t.Context(), mustJSON(c.m)); err != nil {
			t.Fatal(err)
		}
		if told := <-held; !slices.Equal(told, c.held) {
			t.Fatalf("held as the publisher is told %s went: %v", c.m.LinkID, told)
		}
	}
	if ids, known := a.heldLinks(); !known || !slices.Equal(ids, []string{"l2"}) {
		t.Fatalf("held after l1 ended: %v %v", ids, known)
	}
}

// A registered is what the server holds (takeHeldLocked): the links held
// before it that it does not name are returned, to be told ended; the
// first registration returns none, and an older server's registered (no
// links) changes nothing.
func TestARegisteredIsTheLinksHeld(t *testing.T) {
	a := &agent{}
	for _, c := range []struct {
		links, ended, held []string
	}{
		{[]string{"l1", "l2", "l3"}, nil, []string{"l1", "l2", "l3"}},
		{nil, nil, []string{"l1", "l2", "l3"}},
		{[]string{"l2", "l4"}, []string{"l1", "l3"}, []string{"l2", "l4"}},
		{[]string{}, []string{"l2", "l4"}, []string{}},
	} {
		a.mu.Lock()
		ended := a.takeHeldLocked(c.links)
		a.mu.Unlock()
		slices.Sort(ended)
		held, _ := a.heldLinks()
		slices.Sort(held)
		if !slices.Equal(ended, c.ended) || !slices.Equal(held, c.held) {
			t.Fatalf("registered %v: ended %v, held %v; want %v, %v", c.links, ended, held, c.ended, c.held)
		}
	}
}

// A publication that registers again tells its publisher, ended, each
// link it held that the server's registered no longer names, the link
// already gone from its held links; the links named stay held.
func TestARegistrationTellsTheLinksItNoLongerNames(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, _, err := c.Read(r.Context()); err != nil {
			return
		}
		links := []string{"l1", "l2"}
		if n.Add(1) > 1 {
			links = []string{"l2"}
		}
		b, _ := json.Marshal(proto.Registered{T: proto.HostRegistered, SessionID: "h1", Secret: "x", ShareBaseURL: "https://yard.example.net", Links: links})
		_ = c.Write(r.Context(), websocket.MessageText, b)
		if n.Load() == 1 {
			return // the connection lost: it registers again
		}
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	type told struct {
		id, sessionID, runID string
		ended                bool
		held                 []string
	}
	gone := make(chan told, 4)
	var pub *Published
	var ready sync.WaitGroup
	ready.Add(1)
	u := &Uplink{ServerURL: srv.URL, Insecure: true, RelayOnly: true, LinkGone: func(id, sessionID, runID string, ended bool) {
		ready.Wait()
		held, _ := pub.HeldLinks()
		gone <- told{id, sessionID, runID, ended, held}
	}}
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(t.Context(), time.Second) })
	local := session.NewLocal(session.Info{ID: "s", Command: []string{"cat"}, Cols: 80, Rows: 24}, proc, session.Options{Log: discardLog})
	pub, err = u.Publish(t.Context(), local)
	ready.Done()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pub.Stop)
	select {
	case got := <-gone:
		if got.id != "l1" || got.sessionID != "s" || got.runID != "" || !got.ended || !slices.Equal(got.held, []string{"l2"}) {
			t.Fatalf("told %+v", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the publisher was told nothing as the publication registered again")
	}
	select {
	case got := <-gone:
		t.Fatalf("told also %+v", got)
	default:
	}
}

// decodeControl is the control a lane reply f carries.
func decodeControl(t *testing.T, f []byte) map[string]any {
	t.Helper()
	fr, err := proto.Decode(f)
	if err != nil || fr.Type != proto.TypeControl {
		t.Fatalf("the reply %q: %v", f, err)
	}
	var m map[string]any
	if err := json.Unmarshal(fr.Payload, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// decidePayload is the payload of a decide reqID.
func decidePayload(t *testing.T, reqID string) []byte {
	t.Helper()
	f, err := proto.EncodeDecide(proto.Decide{ReqID: reqID, Ref: "task:run-1/build", Action: "retry", Revision: "7", OpID: "op-" + reqID})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := proto.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return fr.Payload
}

// isClosed says whether done is closed.
func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// nextReply is the next reply on replies.
func nextReply(t *testing.T, replies <-chan []byte) map[string]any {
	t.Helper()
	select {
	case f := <-replies:
		return decodeControl(t, f)
	case <-time.After(10 * time.Second):
		t.Fatal("no reply")
		return nil
	}
}

// A lane's request that has its lane but is admitted only after the
// lane's teardown began (its viewer gone, or its connection ending: the
// group's end already past its wait, with no worker) is refused
// unavailable with words, begins nothing, and adds no worker to the
// group: the admission and the teardown are decided under one lock
// (laneGroup), held here between the two through its seam.
func TestALaneRequestPastItsTeardownIsRefused(t *testing.T) {
	teardowns := map[string]func(*laneGroup, *laneConn){
		"the viewer gone":       func(_ *laneGroup, c *laneConn) { c.close() },
		"the connection ending": func(g *laneGroup, _ *laneConn) { g.end() },
	}
	requests := map[string]func(c *laneConn, t *testing.T){
		"a read": func(c *laneConn, _ *testing.T) {
			fr, err := proto.Decode(proto.MustControl(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: "r1"}))
			if err != nil {
				panic(err)
			}
			laneControl(c, true, fr.Payload, nil)
		},
		"a decide": func(c *laneConn, t *testing.T) { laneDecide(c, true, decidePayload(t, "r1"), nil) },
	}
	for tname, teardown := range teardowns {
		for rname, request := range requests {
			t.Run(tname+", "+rname, func(t *testing.T) {
				d := &fakeDecisions{}
				g := &laneGroup{}
				replies := make(chan []byte, 4)
				c := openLane(g, d, "dl1", "s", func(f []byte) { replies <- f }, func() error { return nil })
				paused, resume := make(chan struct{}), make(chan struct{})
				g.beforeAdmit = func() { close(paused); <-resume }
				go request(c, t)
				<-paused
				teardown(g, c)
				close(resume)
				r := nextReply(t, replies)
				if r["t"] != proto.CtlDecideRefused || r["reqId"] != "r1" || r["code"] != laneUnavailable || r["message"] != laneEnding {
					t.Fatalf("the late request's reply %v", r)
				}
				for _, s := range d.seen() {
					if !strings.HasPrefix(s, "watch ") {
						t.Fatalf("Decisions was asked %q after the teardown", s)
					}
				}
				if n := g.pending(); n != 0 {
					t.Fatalf("%d workers under way", n)
				}
				g.end()
			})
		}
	}
}

// heldDecisions is a fakeDecisions whose decides wait for release once
// they have begun (entered).
type heldDecisions struct {
	fakeDecisions
	entered, release chan struct{}
}

func (h *heldDecisions) Decide(ctx context.Context, linkID, sessionID string, m proto.Decide, answer func(context.Context, proto.Answer) error, stands func() error) []byte {
	close(h.entered)
	<-h.release
	return h.fakeDecisions.Decide(ctx, linkID, sessionID, m, answer, stands)
}

// deskUnderTest is a run's desk with no connection: what it sends the
// switchyard goes to sent (through hook when set), its end started.
func deskUnderTest(t *testing.T, d Decisions, hook func(v any)) (*agent, *Desk) {
	t.Helper()
	a := &agent{opts: Options{Decisions: d, Switchyard: true}, peers: map[string]*peer{}, log: discardLog, desk: &proto.DeskRun{ID: "run-1", Name: "Run"}}
	a.decisions.Store(true)
	a.sendHook = hook
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	k := a.newDesk(cancel)
	go k.end(ctx)
	return a, k
}

// A desk's end waits for its lane's operation under way, accepted before
// the stop and held between its acceptance and its settlement: the desk
// is not done, nor the switchyard told, while it is held; a request after
// the stop is refused with words; a caller whose bound passes is told
// what was left and goes on; released, the operation's answer reaches its
// viewer, then the switchyard is told, and the desk is done.
func TestADesksEndWaitsForItsLaneOperations(t *testing.T) {
	h := &heldDecisions{entered: make(chan struct{}), release: make(chan struct{})}
	told := make(chan any, 8)
	a, k := deskUnderTest(t, h, func(v any) { told <- v })
	waiting := make(chan int, 1)
	a.lanes.waiting = func(n int) { waiting <- n }
	replies := make(chan []byte, 8)
	c := openLane(&a.lanes, h, "dl1", "", func(f []byte) { replies <- f }, func() error { return nil })
	laneDecide(c, true, decidePayload(t, "r1"), nil)
	<-h.entered

	stopCtx, stopCancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- k.Stop(stopCtx) }()
	if n := <-waiting; n != 1 {
		t.Fatalf("the end waits for %d workers", n)
	}
	laneDecide(c, true, decidePayload(t, "r2"), nil)
	if r := nextReply(t, replies); r["reqId"] != "r2" || r["code"] != laneUnavailable {
		t.Fatalf("a decide after the stop: %v", r)
	}
	if isClosed(k.Done()) {
		t.Fatal("the desk was done with its lane's operation under way")
	}
	select {
	case v := <-told:
		t.Fatalf("the switchyard was told %#v with the operation under way", v)
	default:
	}
	stopCancel()
	if err := <-stopped; err == nil || !strings.Contains(err.Error(), "1 lane operations under way") {
		t.Fatalf("the bounded stop: %v", err)
	}

	close(h.release)
	if r := nextReply(t, replies); r["t"] != proto.CtlDecideOK || r["reqId"] != "r1" {
		t.Fatalf("the held decide's answer %v", r)
	}
	<-k.Done()
	if st, ok := (<-told).(proto.HostStatusMsg); !ok || st.Status != string(session.StatusStopped) {
		t.Fatalf("the switchyard was told %#v", st)
	}
	if err := k.Stop(context.Background()); err != nil {
		t.Fatalf("a stop of the ended desk: %v", err)
	}
}

// A desk's stop is bounded by its caller's context while the switchyard's
// word waits on a stalled control writer: the caller is told the
// switchyard was not yet told and goes on (the server's shutdown stops
// its runs and sessions meanwhile); the writer released, the desk ends.
func TestADesksStopIsBoundedOnAStalledWriter(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	_, k := deskUnderTest(t, &fakeDecisions{}, func(v any) {
		if _, ok := v.(proto.HostStatusMsg); ok {
			close(entered)
			<-release
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- k.Stop(ctx) }()
	<-entered
	cancel()
	if err := <-stopped; err == nil || !strings.Contains(err.Error(), "the switchyard not yet told") {
		t.Fatalf("the bounded stop: %v", err)
	}
	if isClosed(k.Done()) {
		t.Fatal("the desk was done before its word to the switchyard")
	}
	close(release)
	<-k.Done()
}
