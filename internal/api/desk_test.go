package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The lane through a switchyard, and a run's desk (desk_publish.go,
// desk_host.go; docs/plans/decision-inbox.md, PR-6): a decision link
// minted at the switchyard, its viewer joined there and served end to end
// by the publisher, a run's decisions reached with no member alive. The
// tests wait on the state they need, and the desk's linger fires through
// its seam, never on the clock.

var _ Desk = (*hostagent.Desk)(nil)

// OpenDesk opens a run's desk at the uplink's switchyard, as the serve
// command's publisher does (internal/cli).
func (p uplinkPublisher) OpenDesk(ctx context.Context, run proto.DeskRun) (Desk, error) {
	d, err := p.u.OpenDesk(ctx, run)
	if errors.Is(err, hostagent.ErrNoDesks) {
		return nil, ErrNoDesks
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// deskPair is a switchyard whose registered says it carries decisions
// when decisions, and a server publishing to it with its lane (the
// serve command's uplink), whose tasks run in a repository.
func deskPair(t *testing.T, decisions bool) (yard, local *testEnv) {
	t.Helper()
	yard = newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	yard.srv.hosts.SetDecisions(decisions)
	local = readyEnv(t, nil)
	local.gitRepo(t)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: yard.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true,
		Decisions: local.srv.Lane(), LinkGone: local.srv.RemoteLinkGone}})
	return yard, local
}

// remoteDecisionLink mints a decision link at path (a session's or a
// run's links route) of local, which a switchyard mints: its id and its
// token there.
func remoteDecisionLink(t *testing.T, local *testEnv, path, label string) (string, string) {
	t.Helper()
	resp, out := local.do("POST", path, adminToken, map[string]any{"role": "control", "decide": true, "label": label})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("a decision link at %s: %d %v", path, resp.StatusCode, out)
	}
	l := out["link"].(map[string]any)
	if l["decide"] != true {
		t.Fatalf("the link: %v", l)
	}
	u := out["url"].(string)
	return l["id"].(string), u[strings.LastIndex(u, "/")+1:]
}

// joinedRelay has the switchyard's viewer v move to its relay and say
// hello there: the welcome its owner sends.
func joinedRelay(t *testing.T, v *wsClient) map[string]any {
	t.Helper()
	v.expectControl(proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	v.send(relay)
	for {
		f, err := v.read()
		if err != nil {
			t.Fatalf("waiting for relay_ok: %v", err)
		}
		if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeSignal && tt == proto.SigRelayOK {
			break
		}
	}
	v.hello(80, 24)
	return v.expectControl(proto.CtlWelcome)
}

// deskOf is the desk the switchyard's join of token names.
func deskOf(t *testing.T, yard *testEnv, token string) string {
	t.Helper()
	resp, out := yard.do("GET", "/api/join/"+token, token, nil)
	run, _ := out["run"].(map[string]any)
	desk, _ := run["desk"].(string)
	if resp.StatusCode != http.StatusOK || desk == "" {
		t.Fatalf("the join names no desk: %d %v", resp.StatusCode, out)
	}
	return desk
}

// joinDesk joins the run's desk at the switchyard through the run decision
// link token, over its relay: the lane as the join page's Decide tab has it.
func joinDesk(t *testing.T, yard *testEnv, token string) *laneClient {
	t.Helper()
	c, err := dialWS(yard, "/ws/sessions/"+deskOf(t, yard, token), token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	w := &laneClient{wsClient: &wsClient{t: t, c: c}}
	w.welcome = joinedRelay(t, w.wsClient)
	if w.welcome["decisions"] != true {
		t.Fatalf("the desk's welcome %v", w.welcome)
	}
	return w
}

// heldRun launches the failing crew with its first attempt held at the
// launch: the run and the hold's release.
func heldRun(t *testing.T, e *testEnv) (crew.Run, func() error) {
	t.Helper()
	hold := make(chan struct{})
	var once sync.Once
	e.srv.launches.setSeams(func(g *launchGate) {
		g.attempt = func(spec crew.LaunchSpec) {
			if spec.Ref.Attempt == 1 {
				<-hold
			}
		}
	})
	t.Cleanup(func() { once.Do(func() { close(hold) }) })
	launched := make(chan error, 1)
	go func() {
		_, release, err := e.srv.runs.LaunchNamed(context.Background(), failingCrew(e.root), "")
		release()
		launched <- err
	}()
	var run crew.Run
	until(t, "the run made", func() bool {
		runs := e.srv.runs.List()
		if len(runs) == 1 {
			run = runs[0]
		}
		return len(runs) == 1
	})
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	return run, func() error {
		once.Do(func() { close(hold) })
		return <-launched
	}
}

// endMembers stops every member session of e's runs: no member is alive.
func endMembers(t *testing.T, e *testEnv) {
	t.Helper()
	for _, r := range e.srv.runs.List() {
		for _, m := range r.Members {
			if d, ok := e.srv.registry.Get(m.SessionID); ok && m.SessionID != "" {
				if l, ok := d.(*session.Local); ok {
					_ = l.Stop(t.Context())
				}
			}
		}
	}
}

// deskGone waits until the run's desk has ended here and at the switchyard.
func deskGone(t *testing.T, yard, local *testEnv, runID, desk string) {
	t.Helper()
	until(t, "the desk ended", func() bool {
		_, there := yard.srv.hosts.Desk(desk)
		return local.srv.liveDesk(runID) == nil && !there
	})
}

// A run decision link at a switchyard is minted over the run's desk with
// no member published (its first attempt held: the desk lists nothing),
// and once the attempts have failed and every member's session has ended,
// the failed task is listed and retried over the desk, as the link's
// actor; the desk and the link go on working.
func TestADeskCarriesARunWithNoLiveMember(t *testing.T) {
	yard, local := deskPair(t, true)
	run, release := heldRun(t, local)
	linkID, token := remoteDecisionLink(t, local, "/api/runs/"+run.ID+"/links", "phone")
	desk := joinDesk(t, yard, token)
	if page := desk.list(""); page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("before any attempt: %+v", page)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		sid := runningAttempt(t, local, run.ID, "build", n)["sessionId"].(string)
		if resp, out := local.do("POST", "/api/sessions/"+sid+"/task/result", local.agentToken(sid), map[string]any{"summary": "try"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("result %d: %d %v", n, resp.StatusCode, out)
		}
	}
	taskVia(t, local, run.ID, "build", "failed", func(task map[string]any) bool { return task["state"] == "failed" })
	endMembers(t, local)
	ref := "task:" + run.ID + "/build"
	until(t, "the task listed", func() bool { return desk.list("").Total == 1 })
	op, _ := desk.retried(ref, "desk")
	if row, found, err := local.srv.ledger.Receipt(t.Context(), run.ID, "link:"+linkID, op); err != nil || !found || row.Intent != crew.TaskRetry {
		t.Fatalf("the ledger's receipt: %+v %v %v", row, found, err)
	}
	runningAttempt(t, local, run.ID, "build", 4)
	if steps := desk.track(ref, op, op); steps[stepValidated].State != stepDone {
		t.Fatalf("the steps over the desk: %+v", steps)
	}
}

// A run that finished failed keeps its desk, for a Retry reopens it: past
// any linger (none is armed while the engine takes its commands) the desk
// serves the failed task and a retry taken through it starts the next
// attempt; the run stopped, its desk ends once its linger has fired, not
// before.
func TestADeskOutlivesAFailedRunsEnd(t *testing.T) {
	yard, local := deskPair(t, true)
	fires := make(chan func(), 4)
	local.srv.pubMu.Lock()
	local.srv.deskLingerSeam = func(_ string, fire func()) { fires <- fire }
	local.srv.pubMu.Unlock()
	run := failedRun(t, local)
	endMembers(t, local)
	_, token := remoteDecisionLink(t, local, "/api/runs/"+run+"/links", "phone")
	desk := joinDesk(t, yard, token)
	deskID := deskOf(t, yard, token)
	local.srv.deskCheck(run)
	select {
	case <-fires:
		t.Fatal("a linger was armed for a run whose commands the engine takes")
	default:
	}
	ref := "task:" + run + "/build"
	if page := desk.list(""); page.Total != 1 || page.Items[0].Ref != ref {
		t.Fatalf("the finished run's items: %+v", page)
	}
	desk.retried(ref, "desk")
	runningAttempt(t, local, run, "build", 4)
	if resp, _ := local.do("POST", "/api/runs/"+run+"/stop", adminToken, nil); resp.StatusCode >= 300 {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	var fire func()
	select {
	case fire = <-fires:
	case <-time.After(testWait):
		t.Fatal("no linger was armed for the stopped run")
	}
	if local.srv.liveDesk(run) == nil {
		t.Fatal("the desk ended before its linger fired")
	}
	if _, there := yard.srv.hosts.Desk(deskID); !there {
		t.Fatal("the switchyard let the desk go before its linger fired")
	}
	fire()
	deskGone(t, yard, local, run, deskID)
}

// A run's desk ends with its last decision link: one revoked of two
// leaves it (the revoked one no longer joins), the other revoked ends it,
// the revoke carried over the desk itself while no member is published.
func TestADeskEndsWithItsLastDecisionLink(t *testing.T) {
	yard, local := deskPair(t, true)
	run, _ := heldRun(t, local)
	first, firstToken := remoteDecisionLink(t, local, "/api/runs/"+run.ID+"/links", "phone")
	second, secondToken := remoteDecisionLink(t, local, "/api/runs/"+run.ID+"/links", "tablet")
	deskID := deskOf(t, yard, firstToken)
	if deskOf(t, yard, secondToken) != deskID {
		t.Fatal("a run's two links name two desks")
	}
	_, out := local.do("GET", "/api/runs/"+run.ID+"/links", adminToken, nil)
	if links, _ := out["links"].([]any); len(links) != 2 {
		t.Fatalf("the run's links list them twice or not at all: %v", out)
	}
	if resp, _ := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+first, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the first revoke: %d", resp.StatusCode)
	}
	if resp, _ := yard.do("GET", "/api/join/"+firstToken, firstToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the revoked link still joins: %d", resp.StatusCode)
	}
	if local.srv.liveDesk(run.ID) == nil {
		t.Fatal("the desk ended with a decision link standing")
	}
	joinDesk(t, yard, secondToken).list("")
	if resp, _ := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+second, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the second revoke: %d", resp.StatusCode)
	}
	deskGone(t, yard, local, run.ID, deskID)
}

// At a switchyard whose registered says nothing of decisions (this build,
// its decisions not said: the Decide tab is not served yet) no decision
// link is minted, a session's or a run's, 409 switchyard_too_old, and no
// desk is left open; a plain control link is, and its viewer's lane is
// refused by the owner, nothing of it a decision, the connection going on.
func TestNoDecisionLinkAtAnOlderSwitchyard(t *testing.T) {
	yard, local := deskPair(t, false)
	id := local.createSession("cat")
	until(t, "the session published", func() bool { return local.srv.publishedOf(id) != nil })
	resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control", "decide": true})
	if resp.StatusCode != http.StatusConflict || errCodeOf(out) != "switchyard_too_old" {
		t.Fatalf("a session's decision link: %d %v", resp.StatusCode, out)
	}
	run, _ := heldRun(t, local)
	resp, out = local.do("POST", "/api/runs/"+run.ID+"/links", adminToken, map[string]any{"role": "control", "decide": true})
	if resp.StatusCode != http.StatusConflict || errCodeOf(out) != "switchyard_too_old" {
		t.Fatalf("a run's decision link: %d %v", resp.StatusCode, out)
	}
	if local.srv.liveDesk(run.ID) != nil {
		t.Fatal("a desk is left open")
	}
	until(t, "the switchyard let the desk go", func() bool { return yard.srv.hosts.DeskCount() == 0 })
	resp, out = local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("a control link: %d %v", resp.StatusCode, out)
	}
	u := out["url"].(string)
	token := u[strings.LastIndex(u, "/")+1:]
	hosted := local.srv.publishedOf(id).CurrentID()
	v := dialViewer(t, yard, hosted, token)
	if w := joinedRelay(t, v); w["decisions"] != nil {
		t.Fatalf("a control link's welcome %v", w)
	}
	v.expectControl(proto.CtlReady)
	v.send(proto.MustControl(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: "r1"}))
	for {
		f, err := v.read()
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == proto.TypeDecision {
			t.Fatal("a decision frame came through the relay")
		}
		var m map[string]any
		if f.Type == proto.TypeControl && json.Unmarshal(f.Payload, &m) == nil && m["t"] == proto.CtlDecideRefused {
			if m["code"] != proto.DecideNotDecisionLink {
				t.Fatalf("the refusal %v", m)
			}
			break
		}
	}
	v.send(proto.Encode(proto.TypeInput, []byte("still here\n")))
	v.expectOutput("still here")
}

// A switchyard's relay carries the lane's two frames: a control viewer's
// decide and reads reach the host in envelopes, the host's decision
// reaches the viewer, and the host's connection stays.
func TestRelayCarriesDecisionFrames(t *testing.T) {
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	host := dialFakeHost(t, yard, "")
	token := host.link("control", "phone")
	v := dialViewer(t, yard, host.sessionID, token)
	v.expectControl(proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	v.send(relay)
	join := host.expect(proto.HostViewerJoin)
	viewer := join["viewerId"].(string)
	host.expect(proto.HostRelayStart)
	for {
		f, err := v.read()
		if err != nil {
			t.Fatal(err)
		}
		if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeSignal && tt == proto.SigRelayOK {
			break
		}
	}
	decide, _ := proto.EncodeDecide(proto.Decide{ReqID: "r1", Ref: "task:run-1/build", Action: "retry", Revision: "3", OpID: "o1"})
	v.send(decide)
	v.send(proto.MustControl(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: "r2"}))
	for _, want := range []byte{proto.TypeDecide, proto.TypeControl} {
		select {
		case raw := <-host.bin:
			f, err := proto.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			id, inner, err := proto.DecodeRelay(f.Payload)
			if err != nil || id != viewer || inner.Type != want {
				t.Fatalf("relayed to the host: %q 0x%02x %v", id, inner.Type, err)
			}
		case <-time.After(testWait):
			t.Fatalf("the host was never relayed 0x%02x", want)
		}
	}
	dec, _ := proto.EncodeDecision(proto.Decision{ReqID: "r2", List: json.RawMessage(`{"items":[],"total":0}`)})
	env, _ := proto.EncodeRelay(viewer, dec)
	if err := host.c.Write(t.Context(), websocket.MessageBinary, env); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := v.read()
		if err != nil {
			t.Fatalf("waiting for the decision: %v", err)
		}
		if f.Type == proto.TypeDecision {
			if !strings.Contains(string(f.Payload), `"reqId":"r2"`) {
				t.Fatalf("the decision %s", f.Payload)
			}
			break
		}
	}
	// The host's connection stays: it is answered as before.
	host.link("view", "after")
}

// A session decision link at a switchyard: its viewer there is welcomed
// with decisions by the publisher and answers the session's question
// through the lane (as the link, the question's claim); the session's
// links list it once, as the switchyard's; revoked here, it ends here
// (its lane refuses) and there.
func TestASessionDecisionLinkThroughASwitchyard(t *testing.T) {
	yard, local := deskPair(t, true)
	id := sessionHex("published")
	fs := newFakeSession(t, local, id, session.Options{})
	local.srv.publish(fs.l)
	until(t, "the session published", func() bool { return local.srv.publishedOf(id) != nil })
	q := fs.ask(t, "Which name?", threeChoices)
	linkID, token := remoteDecisionLink(t, local, "/api/sessions/"+id+"/links", "phone")
	_, out := local.do("GET", "/api/sessions/"+id+"/links", adminToken, nil)
	if links, _ := out["links"].([]any); len(links) != 1 || links[0].(map[string]any)["decide"] != true || links[0].(map[string]any)["remote"] != true {
		t.Fatalf("the session's links: %v", out)
	}
	w := &laneClient{wsClient: dialViewer(t, yard, local.srv.publishedOf(id).CurrentID(), token)}
	w.welcome = joinedRelay(t, w.wsClient)
	if w.welcome["decisions"] != true {
		t.Fatalf("the welcome %v", w.welcome)
	}
	ref := questionRef(id, q)
	if page := w.list(""); page.Total != 1 || page.Items[0].Ref != ref {
		t.Fatalf("the session's items there: %+v", page)
	}
	if m := w.decide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "2", OpID: "op-1"}); !isOK(m) {
		t.Fatalf("the answer: %v", m)
	}
	until(t, "the choice typed", func() bool { return strings.Contains(strings.Join(fs.written(), ""), "2") })
	if r := fs.l.Info().Attention; r.State == session.AttentionNeedsInput && r.ID == q {
		t.Fatalf("the question still waits: %+v", r)
	}
	if resp, _ := local.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if _, err := local.srv.links.Live(linkID); err == nil {
		t.Fatal("the link stands here after its revoke")
	}
	if local.srv.Lane().DecisionLink(linkID, id, "") {
		t.Fatal("the revoked link still opens a lane")
	}
	if resp, _ := yard.do("GET", "/api/join/"+token, token, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the switchyard still joins it: %d", resp.StatusCode)
	}
}

// dialFakeDesk registers a fake desk at e with m, as a publisher's desk
// does: it and the server's registered answer.
func dialFakeDesk(t *testing.T, e *testEnv, m proto.RegisterDesk) (*fakeHost, map[string]any) {
	t.Helper()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	c.SetReadLimit(proto.MaxHostMessage + proto.MaxFrame)
	t.Cleanup(func() { c.CloseNow() })
	h := &fakeHost{t: t, c: c, msgs: make(chan map[string]any, 4096), bin: make(chan []byte, 4096), done: make(chan struct{})}
	go h.pump()
	h.send(m)
	registered := h.expect(proto.HostRegistered)
	h.sessionID, _ = registered["sessionId"].(string)
	return h, registered
}

func deskReg(instance, runID string) proto.RegisterDesk {
	return proto.RegisterDesk{T: proto.HostRegisterDesk, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "office", Instance: instance},
		LocalID: proto.DeskLocalID(runID), Run: proto.DeskRun{ID: runID, Name: "Release"}}
}

// runLinkOver mints a run link over h's connection for the group g: the
// link_created, or the error, the server answered.
func runLinkOver(h *fakeHost, role string, g proto.RunGroup) map[string]any {
	h.t.Helper()
	h.links++
	id := "run-" + strings.Repeat("x", h.links)
	h.send(proto.HostRunLinkMsg{T: proto.HostRunLink, RequestID: id, Role: role, Run: g})
	for {
		m := h.expect2(proto.HostLinkCreated, proto.HostError)
		if m["requestId"] == id {
			return m
		}
	}
}

// expect2 is the next message of either type.
func (h *fakeHost) expect2(a, b string) map[string]any {
	h.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m := <-h.msgs:
			if m["t"] == a || m["t"] == b {
				return m
			}
		case <-h.done:
			h.t.Fatalf("waiting for %s or %s: the connection ended: %v", a, b, h.err)
		case <-timeout:
			h.t.Fatalf("waiting for %s or %s: nothing came", a, b)
		}
	}
}

// A desk is its publisher's: refused without an instance (no_instance);
// two publishers with the same run id get two desks of two owners, never
// listed as sessions; one's run group naming the other's desk, or a run
// link over it, is refused, its own taken, and its link opens its desk
// (the join names it) to a control viewer alone; a desk whose connection
// drops resumes with its secret; the switchyard restarted over its data
// directory, the desk registers again with the same id, its registered
// names the run decision link, which opens it still.
func TestADeskIsItsPublishers(t *testing.T) {
	dir := t.TempDir() + "/data"
	yard := keptSwitchyard(t, dir, nil)
	// No instance: closed, nothing registered.
	c, _, err := websocket.Dial(t.Context(), strings.Replace(yard.http.URL, "http://", "ws://", 1)+"/ws/host", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(deskReg("", "run-1"))
	_ = c.Write(t.Context(), websocket.MessageText, b)
	if _, _, err := c.Read(t.Context()); websocket.CloseStatus(err) != websocket.StatusCode(proto.CloseProtocolError) || !strings.Contains(err.Error(), "no_instance") {
		t.Fatalf("a desk without an instance: %v", err)
	}
	c.CloseNow()

	a, regA := dialFakeDesk(t, yard, deskReg("instance-a-0123456789", "run-1"))
	bDesk, _ := dialFakeDesk(t, yard, deskReg("instance-b-0123456789", "run-1"))
	if a.sessionID == "" || a.sessionID == bDesk.sessionID {
		t.Fatalf("two publishers' desks: %q %q", a.sessionID, bDesk.sessionID)
	}
	if regA["secret"] == "" || regA["links"] == nil {
		t.Fatalf("a's registered: %v", regA)
	}
	if _, ok := yard.srv.registry.Get(a.sessionID); ok {
		t.Fatal("a desk is in the registry")
	}
	_, list := yard.do("GET", "/api/sessions", adminToken, nil)
	if strings.Contains(fmt5(list), a.sessionID) {
		t.Fatalf("a desk is listed: %v", list)
	}
	group := func(desk string) proto.RunGroup {
		return proto.RunGroup{ID: "run-1", Name: "Release", Desk: desk, Members: []proto.RunMember{{Name: "lead", AgentID: "cat", Status: "pending"}}}
	}
	if m := runLinkOver(a, "control", group(bDesk.sessionID)); m["t"] != proto.HostError || m["code"] != "invalid_request" {
		t.Fatalf("a's run link naming b's desk: %v", m)
	}
	if m := runLinkOver(bDesk, "control", group(a.sessionID)); m["t"] != proto.HostError {
		t.Fatalf("b's run link naming a's desk: %v", m)
	}
	// A member session of b's sends a group naming a's desk: refused.
	hi, hs := instanceHost(yard, "instance-b-0123456789", "s1")
	member, _ := dialFakeHostWith(t, yard, hi, hs, "")
	if m := runLinkOver(member, "control", group(a.sessionID)); m["t"] != proto.HostError || m["code"] != "invalid_request" {
		t.Fatalf("b's member naming a's desk: %v", m)
	}
	created := runLinkOver(a, "control", group(a.sessionID))
	if created["t"] != proto.HostLinkCreated {
		t.Fatalf("a's own run link: %v", created)
	}
	linkID := created["linkId"].(string)
	u := created["url"].(string)
	token := u[strings.LastIndex(u, "/")+1:]
	if deskOf(t, yard, token) != a.sessionID {
		t.Fatal("the join names another desk")
	}
	viewView := runLinkOver(a, "view", group(a.sessionID))
	vu := viewView["url"].(string)
	if code := closedWith(t, yard, "/ws/sessions/"+a.sessionID, vu[strings.LastIndex(vu, "/")+1:]); code != websocket.StatusCode(proto.CloseUnauthorized) {
		t.Fatalf("a view link at a desk: %d", code)
	}
	v := dialViewer(t, yard, a.sessionID, token)
	v.expectControl(proto.CtlWelcome)
	if j := a.expect(proto.HostViewerJoin); j["linkId"] != linkID || j["role"] != "control" {
		t.Fatalf("the desk's viewer_join %v", j)
	}
	// A desk sends no session's messages.
	bDesk.send(proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input"})
	<-bDesk.done
	// Its connection drops: it resumes with its secret.
	a.c.Close(websocket.StatusNormalClosure, "")
	<-a.done
	resume := deskReg("instance-a-0123456789", "run-1")
	resume.Resume = &proto.HostResume{SessionID: a.sessionID, Secret: regA["secret"].(string)}
	back, reg := dialFakeDesk(t, yard, resume)
	if back.sessionID != a.sessionID || reg["resumed"] != true {
		t.Fatalf("resumed: %v", reg)
	}
	back.c.Close(websocket.StatusNormalClosure, "")
	<-back.done
	// The switchyard restarts over its data directory.
	yard2 := keptSwitchyard(t, dir, nil)
	again, reg2 := dialFakeDesk(t, yard2, deskReg("instance-a-0123456789", "run-1"))
	if again.sessionID != a.sessionID || !strings.Contains(fmt5(reg2["links"]), linkID) {
		t.Fatalf("after a restart: %s (was %s), links %v", again.sessionID, a.sessionID, reg2["links"])
	}
	if deskOf(t, yard2, token) != a.sessionID {
		t.Fatal("after a restart the join names another desk")
	}
	v2 := dialViewer(t, yard2, a.sessionID, token)
	v2.expectControl(proto.CtlWelcome)
	again.expect(proto.HostViewerJoin)
}

// fmt5 is v as text, for a containment check.
func fmt5(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
