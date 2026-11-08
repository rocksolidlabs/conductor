package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
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
	// A revoke again, its first answer lost, after the switchyard confirmed
	// it and the last desk ended: done, as the first was, with nothing
	// left to carry it.
	for _, id := range []string{second, first} {
		if resp, out := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("a revoke again of %s: %d %v", id, resp.StatusCode, out)
		}
	}
	if local.srv.liveDesk(run.ID) != nil {
		t.Fatal("a revoke again opened a desk")
	}
}

// mintAsync mints a run decision link at local off the test's goroutine:
// its status and reply on the channel.
func mintAsync(local *testEnv, runID, label string) <-chan map[string]any {
	out := make(chan map[string]any, 1)
	go func() {
		b, _ := json.Marshal(map[string]any{"role": "control", "decide": true, "label": label})
		req, _ := http.NewRequest("POST", local.http.URL+"/api/runs/"+runID+"/links", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := local.client.Do(req)
		if err != nil {
			out <- map[string]any{"err": err.Error()}
			return
		}
		defer resp.Body.Close()
		m := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&m)
		m["status"] = resp.StatusCode
		out <- m
	}()
	return out
}

// Two first decision-link mints of a run at once open one desk: the
// second, arriving while the first opens it (held through its seam until
// the second waits on it), takes the first's desk rather than register a
// second the switchyard refuses; both links are minted, over one desk.
func TestConcurrentFirstMintsOpenOneDesk(t *testing.T) {
	yard, local := deskPair(t, true)
	run, _ := heldRun(t, local)
	awaiting := make(chan struct{})
	var once sync.Once
	var opens atomic.Int32
	local.srv.pubMu.Lock()
	local.srv.deskOpenSeam = func(string) {
		opens.Add(1)
		<-awaiting
	}
	local.srv.deskAwaitSeam = func(string) { once.Do(func() { close(awaiting) }) }
	local.srv.pubMu.Unlock()
	t.Cleanup(func() { once.Do(func() { close(awaiting) }) })
	first := mintAsync(local, run.ID, "phone")
	until(t, "the first mint opening the desk", func() bool { return opens.Load() == 1 })
	second := mintAsync(local, run.ID, "tablet")
	var tokens []string
	for _, ch := range []<-chan map[string]any{first, second} {
		var m map[string]any
		select {
		case m = <-ch:
		case <-time.After(testWait):
			t.Fatal("a mint never answered")
		}
		if m["status"] != http.StatusCreated || m["remote"] != true {
			t.Fatalf("a concurrent first mint: %v", m)
		}
		u := m["url"].(string)
		tokens = append(tokens, u[strings.LastIndex(u, "/")+1:])
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("%d openings of the run's desk", n)
	}
	if a, b := deskOf(t, yard, tokens[0]), deskOf(t, yard, tokens[1]); a != b || a != local.srv.liveDesk(run.ID).CurrentID() {
		t.Fatalf("the two links name desks %s and %s", a, b)
	}
}

// stalledDesk is a run's desk whose end does not come within its
// stop's bound (its control writer stalled at the switchyard): its Stop
// notes the bound it was given and says what was left.
type stalledDesk struct {
	Desk
	done    chan struct{}
	stopped chan context.Context
	before  func()
}

func (d *stalledDesk) CurrentID() string     { return "desk-1" }
func (d *stalledDesk) Done() <-chan struct{} { return d.done }
func (d *stalledDesk) Stop(ctx context.Context) error {
	d.before()
	d.stopped <- ctx
	return errors.New("the desk's end goes on by itself: 1 lane operations under way, the switchyard not yet told")
}

// The server's stop gives the runs' desks their own share of its budget
// (deskStopBy, at most deskStopMax): a desk whose end does not come
// within it is left to end by itself, what was left of it logged, and the
// shutdown goes on to stop the sessions, which no desk's stop held up.
func TestTheShutdownGoesOnPastADesksStop(t *testing.T) {
	logs := &logBuffer{}
	e := newTestEnvLogging(t, nil, slog.New(slog.NewTextHandler(logs, nil)))
	id := e.createSession("cat")
	d, _ := e.srv.registry.Get(id)
	var endedBefore atomic.Bool
	desk := &stalledDesk{done: make(chan struct{}), stopped: make(chan context.Context, 1), before: func() { endedBefore.Store(d.Info().Status.Ended()) }}
	e.srv.pubMu.Lock()
	e.srv.desks = map[string]*runDesk{"run-1": {desk: desk}}
	e.srv.pubMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	budget, _ := ctx.Deadline()
	e.srv.Shutdown(ctx)
	var dctx context.Context
	select {
	case dctx = <-desk.stopped:
	default:
		t.Fatal("the desk was not stopped")
	}
	dl, ok := dctx.Deadline()
	if !ok || !dl.Before(budget) || time.Until(dl) > deskStopMax {
		t.Fatalf("the desk's stop had the deadline %v (%v), the shutdown's %v", dl, ok, budget)
	}
	if endedBefore.Load() {
		t.Fatal("the sessions were stopped before the desks")
	}
	if !d.Info().Status.Ended() {
		t.Fatal("the shutdown stopped no session after the desk's stop")
	}
	if l := logs.String(); !strings.Contains(l, "a run's desk did not end in time") || !strings.Contains(l, "1 lane operations under way") {
		t.Fatalf("the log says nothing of what was left:\n%s", l)
	}
	if ctx.Err() != nil {
		t.Fatal("the shutdown spent its whole budget")
	}
	if _, _, code, _ := e.srv.openRunDesk(t.Context(), "run-1"); code != "shutting_down" {
		t.Fatalf("a desk opened after the shutdown's stop: %q", code)
	}
}

// At a switchyard whose registered says nothing of decisions (this build,
// its decisions not said: the Decide tab is not served yet) no decision
// link is minted, a session's or a run's, 409 switchyard_too_old, and no
// desk is left open; a plain control link is, and its viewer's lane is
// refused by the owner unsupported (it serves no lane through a
// switchyard that does not carry one), nothing of it a decision, the
// connection going on.
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
			if m["code"] != proto.DecideUnsupported {
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

// failingDesk is a run's desk whose revokes at the switchyard fail while
// fail is set (its connection there gone quiet), the rest its own.
type failingDesk struct {
	Desk
	fail atomic.Bool
}

func (d *failingDesk) Revoke(ctx context.Context, linkID string) error {
	if d.fail.Load() {
		return errors.New("rendezvous_unavailable: no answer")
	}
	return d.Desk.Revoke(ctx, linkID)
}

// failingPub is a session's publication whose revokes fail while fail is
// set, the rest its own.
type failingPub struct {
	PublishedSession
	fail atomic.Bool
}

func (p *failingPub) Revoke(ctx context.Context, linkID string) error {
	if p.fail.Load() {
		return errors.New("rendezvous_unavailable: no answer")
	}
	return p.PublishedSession.Revoke(ctx, linkID)
}

func (p *failingPub) Decisions() bool { return carriesDecisions(p.PublishedSession) }

// retries hands the test each retry of a revoke owed at the switchyard,
// to start when it chooses.
func retries(e *testEnv) chan func() {
	next := make(chan func(), 8)
	e.srv.pubMu.Lock()
	e.srv.revokeRetrySeam = func(_ string, try func()) { next <- try }
	e.srv.pubMu.Unlock()
	return next
}

// linksOf lists the links route path answers.
func linksOf(t *testing.T, e *testEnv, path string) []map[string]any {
	t.Helper()
	resp, out := e.do("GET", path, adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %v", path, resp.StatusCode, out)
	}
	var links []map[string]any
	for _, l := range out["links"].([]any) {
		links = append(links, l.(map[string]any))
	}
	return links
}

// A run decision link's revoke that fails at the switchyard fails closed:
// the link ends here (its lane opens nothing) but stays listed, owing its
// revoke there (revokePending), the run's desk stays to carry it, and the
// switchyard still holds the link; it is tried again (a retry that fails
// changes nothing), and a revoke again from here, once the switchyard
// answers, revokes it there: the link no longer joins, it is listed no
// more, and the desk ends with it.
func TestAFailedRevokeThereKeepsTheRunLinkOwed(t *testing.T) {
	yard, local := deskPair(t, true)
	next := retries(local)
	run, _ := heldRun(t, local)
	linkID, token := remoteDecisionLink(t, local, "/api/runs/"+run.ID+"/links", "phone")
	deskID := deskOf(t, yard, token)
	local.srv.pubMu.Lock()
	rd := local.srv.desks[run.ID]
	failing := &failingDesk{Desk: rd.desk}
	failing.fail.Store(true)
	rd.desk = failing
	local.srv.pubMu.Unlock()

	resp, out := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+linkID, adminToken, nil)
	if resp.StatusCode != http.StatusBadGateway || errCodeOf(out) != "rendezvous_unavailable" {
		t.Fatalf("a revoke the switchyard did not take: %d %v", resp.StatusCode, out)
	}
	if local.srv.Lane().DecisionLink(linkID, "", run.ID) {
		t.Fatal("the revoked link still opens a lane here")
	}
	links := linksOf(t, local, "/api/runs/"+run.ID+"/links")
	if len(links) != 1 || links[0]["id"] != linkID || links[0]["revokePending"] != true {
		t.Fatalf("the run's links after a failed revoke: %v", links)
	}
	if local.srv.liveDesk(run.ID) == nil {
		t.Fatal("the desk ended with a revoke owed")
	}
	if code, out := joinStatus(t, yard, token); code != http.StatusOK {
		t.Fatalf("the switchyard's link: %d %v", code, out)
	}
	// A retry while the switchyard still does not answer: owed still.
	var try func()
	select {
	case try = <-next:
	case <-time.After(testWait):
		t.Fatal("no retry was armed")
	}
	try()
	select {
	case try = <-next:
	case <-time.After(testWait):
		t.Fatal("the retry gave up")
	}
	if l := linksOf(t, local, "/api/runs/"+run.ID+"/links"); len(l) != 1 || l[0]["revokePending"] != true || local.srv.liveDesk(run.ID) == nil {
		t.Fatalf("after a retry that failed: %v", l)
	}
	failing.fail.Store(false)
	if resp, out := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the revoke again: %d %v", resp.StatusCode, out)
	}
	if code, _ := joinStatus(t, yard, token); code != http.StatusNotFound {
		t.Fatalf("the link still joins at the switchyard: %d", code)
	}
	if l := linksOf(t, local, "/api/runs/"+run.ID+"/links"); len(l) != 0 {
		t.Fatalf("the run's links after the revoke went through: %v", l)
	}
	until(t, "the desk ended", func() bool { return local.srv.liveDesk(run.ID) == nil })
	until(t, "the switchyard let the desk go", func() bool {
		_, there := yard.srv.hosts.Desk(deskID)
		return !there
	})
	// The retry armed meanwhile finds nothing owed and ends.
	try()
}

// A session decision link's revoke that fails at the switchyard fails
// closed too: listed, owing its revoke, the switchyard holding the link;
// the retry, once the switchyard answers, revokes it there and the record
// goes; a revoke again from here then answers done.
func TestAFailedRevokeThereKeepsTheSessionLinkOwed(t *testing.T) {
	yard, local := deskPair(t, true)
	next := retries(local)
	id := sessionHex("published")
	fs := newFakeSession(t, local, id, session.Options{})
	local.srv.publish(fs.l)
	until(t, "the session published", func() bool { return local.srv.publishedOf(id) != nil })
	linkID, token := remoteDecisionLink(t, local, "/api/sessions/"+id+"/links", "phone")
	local.srv.pubMu.Lock()
	failing := &failingPub{PublishedSession: local.srv.published[id]}
	failing.fail.Store(true)
	local.srv.published[id] = failing
	local.srv.pubMu.Unlock()

	resp, out := local.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil)
	if resp.StatusCode != http.StatusBadGateway || errCodeOf(out) != "rendezvous_unavailable" {
		t.Fatalf("a revoke the switchyard did not take: %d %v", resp.StatusCode, out)
	}
	if local.srv.Lane().DecisionLink(linkID, id, "") {
		t.Fatal("the revoked link still opens a lane here")
	}
	links := linksOf(t, local, "/api/sessions/"+id+"/links")
	if len(links) != 1 || links[0]["id"] != linkID || links[0]["revokePending"] != true {
		t.Fatalf("the session's links after a failed revoke: %v", links)
	}
	if code, out := joinStatus(t, yard, token); code != http.StatusOK {
		t.Fatalf("the switchyard's link: %d %v", code, out)
	}
	failing.fail.Store(false)
	select {
	case try := <-next:
		try()
	case <-time.After(testWait):
		t.Fatal("no retry was armed")
	}
	until(t, "the retry revoked it there", func() bool {
		code, _ := joinStatus(t, yard, token)
		return code == http.StatusNotFound && len(linksOf(t, local, "/api/sessions/"+id+"/links")) == 0
	})
	if resp, _ := local.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a revoke again: %d", resp.StatusCode)
	}
}

// keptDeskPair is deskPair with a switchyard that keeps its links in
// files under dir, whose publisher hands the test each link_gone it was
// told once it has taken it (RemoteLinkGone).
func keptDeskPair(t *testing.T) (yard, local *testEnv, dir string, gone <-chan string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory of mode 0500")
	}
	dir = filepath.Join(t.TempDir(), "data")
	yard = keptSwitchyard(t, dir, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	yard.srv.hosts.SetDecisions(true)
	local = readyEnv(t, nil)
	local.gitRepo(t)
	told := make(chan string, 8)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: yard.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true,
		Decisions: local.srv.Lane(), LinkGone: func(linkID, sessionID, runID string) {
			local.srv.RemoteLinkGone(linkID, sessionID, runID)
			told <- linkID
		}}})
	return yard, local, dir, told
}

// undeletable makes the switchyard's link files under dir undeletable
// (their directory read-only, until the test's end): the file of linkID,
// which it keeps, and the directory.
func undeletable(t *testing.T, dir, linkID string) (file, links string) {
	t.Helper()
	links = filepath.Join(dir, "links")
	file = filepath.Join(links, linkID+".json")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the switchyard keeps no file of the link: %v", err)
	}
	if err := os.Chmod(links, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(links, 0o700) })
	return file, links
}

// toldGone waits for the publisher to have taken the switchyard's
// link_gone of linkID.
func toldGone(t *testing.T, gone <-chan string, linkID string) {
	t.Helper()
	select {
	case id := <-gone:
		if id != linkID {
			t.Fatalf("link_gone for %s", id)
		}
	case <-time.After(testWait):
		t.Fatal("the publisher was never told link_gone")
	}
}

// A run decision link's revoke the switchyard takes but cannot finish,
// with a viewer joined: the switchyard revokes the link and closes its
// viewer, so the desk is told link_gone, before it answers
// store_unavailable, for the link's file there is not deleted (its
// directory read-only), and a restart of the switchyard would bring the
// link back. The publisher forgets nothing on the link_gone (forgetRemote:
// a record owed its revoke is forgotten only once the switchyard has
// confirmed it, or answered not_found): the link stays listed, owing its
// revoke, the desk stays, a revoke again is refused as the first was (not
// 204), and the retry, once the file can go, confirms it and ends both.
func TestALinkGoneBeforeAFailedRevokeKeepsTheRunLinkOwed(t *testing.T) {
	yard, local, dir, gone := keptDeskPair(t)
	next := retries(local)
	run, _ := heldRun(t, local)
	linkID, token := remoteDecisionLink(t, local, "/api/runs/"+run.ID+"/links", "phone")
	deskID := deskOf(t, yard, token)
	joinDesk(t, yard, token).list("")
	file, links := undeletable(t, dir, linkID)

	resp, out := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+linkID, adminToken, nil)
	if resp.StatusCode != http.StatusBadGateway || errCodeOf(out) != "rendezvous_unavailable" || !strings.Contains(fmt5(out), "store_unavailable") {
		t.Fatalf("a revoke whose file stays at the switchyard: %d %v", resp.StatusCode, out)
	}
	// The desk was told link_gone (its viewer closed): the path the rule
	// is for.
	toldGone(t, gone, linkID)
	owed := func(when string) {
		t.Helper()
		l := linksOf(t, local, "/api/runs/"+run.ID+"/links")
		if len(l) != 1 || l[0]["id"] != linkID || l[0]["revokePending"] != true {
			t.Fatalf("%s: the run's links %v", when, l)
		}
		if local.srv.liveDesk(run.ID) == nil {
			t.Fatalf("%s: the desk ended with a revoke owed", when)
		}
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("%s: the link's file: %v", when, err)
		}
	}
	owed("after the link_gone and the failed revoke")
	if local.srv.Lane().DecisionLink(linkID, "", run.ID) {
		t.Fatal("the revoked link still opens a lane here")
	}
	// A revoke again while the file still stays: refused as the first was.
	if resp, out := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("a revoke again while the switchyard keeps the file: %d %v", resp.StatusCode, out)
	}
	owed("after the revoke again")
	var try func()
	select {
	case try = <-next:
	case <-time.After(testWait):
		t.Fatal("no retry was armed")
	}
	// A retry while the file still stays: owed still.
	try()
	select {
	case try = <-next:
	case <-time.After(testWait):
		t.Fatal("the retry gave up")
	}
	owed("after a retry that failed")
	if err := os.Chmod(links, 0o700); err != nil {
		t.Fatal(err)
	}
	try()
	until(t, "the retry confirmed the revoke", func() bool {
		_, err := os.Stat(file)
		return errors.Is(err, os.ErrNotExist) && len(linksOf(t, local, "/api/runs/"+run.ID+"/links")) == 0
	})
	until(t, "the desk ended", func() bool { return local.srv.liveDesk(run.ID) == nil })
	until(t, "the switchyard let the desk go", func() bool {
		_, there := yard.srv.hosts.Desk(deskID)
		return !there
	})
	if code, _ := joinStatus(t, yard, token); code != http.StatusNotFound {
		t.Fatalf("the link still joins at the switchyard: %d", code)
	}
	// Confirmed, a revoke again is done.
	if resp, out := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a revoke again once confirmed: %d %v", resp.StatusCode, out)
	}
}

// The same for a session's decision link, its viewer joined at the
// switchyard: the session's connection is told link_gone before the
// revoke's store_unavailable, and the record stays, owing its revoke,
// until the retry confirms it; a revoke again meanwhile is refused.
func TestALinkGoneBeforeAFailedRevokeKeepsTheSessionLinkOwed(t *testing.T) {
	yard, local, dir, gone := keptDeskPair(t)
	next := retries(local)
	id := sessionHex("published")
	fs := newFakeSession(t, local, id, session.Options{})
	local.srv.publish(fs.l)
	until(t, "the session published", func() bool { return local.srv.publishedOf(id) != nil })
	linkID, token := remoteDecisionLink(t, local, "/api/sessions/"+id+"/links", "phone")
	w := &laneClient{wsClient: dialViewer(t, yard, local.srv.publishedOf(id).CurrentID(), token)}
	w.welcome = joinedRelay(t, w.wsClient)
	w.list("")
	file, links := undeletable(t, dir, linkID)

	resp, out := local.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(fmt5(out), "store_unavailable") {
		t.Fatalf("a revoke whose file stays at the switchyard: %d %v", resp.StatusCode, out)
	}
	toldGone(t, gone, linkID)
	owed := func(when string) {
		t.Helper()
		l := linksOf(t, local, "/api/sessions/"+id+"/links")
		if len(l) != 1 || l[0]["id"] != linkID || l[0]["revokePending"] != true {
			t.Fatalf("%s: the session's links %v", when, l)
		}
	}
	owed("after the link_gone and the failed revoke")
	if resp, out := local.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("a revoke again while the switchyard keeps the file: %d %v", resp.StatusCode, out)
	}
	owed("after the revoke again")
	var try func()
	select {
	case try = <-next:
	case <-time.After(testWait):
		t.Fatal("no retry was armed")
	}
	if err := os.Chmod(links, 0o700); err != nil {
		t.Fatal(err)
	}
	try()
	until(t, "the retry confirmed the revoke", func() bool {
		_, err := os.Stat(file)
		return errors.Is(err, os.ErrNotExist) && len(linksOf(t, local, "/api/sessions/"+id+"/links")) == 0
	})
	if code, _ := joinStatus(t, yard, token); code != http.StatusNotFound {
		t.Fatalf("the link still joins at the switchyard: %d", code)
	}
}

// A register_desk is a host message, bounded as one: past 64 KiB
// (proto.MaxHostMessage; here a host version that fills it, which no
// field bound of its own catches) the connection is closed and no desk
// registered, though the socket reads more for the frames a host relays;
// the same message within the bound registers.
func TestAnOversizedRegisterDeskIsRefused(t *testing.T) {
	yard := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), nil)
	big := deskReg("instance-a-0123456789", "run-1")
	big.Host.Version = strings.Repeat("v", 70_000)
	b, _ := json.Marshal(big)
	if len(b) <= proto.MaxHostMessage || len(b) > proto.MaxHostMessage+proto.MaxFrame {
		t.Fatalf("the message is %d bytes", len(b))
	}
	c, _, err := websocket.Dial(t.Context(), strings.Replace(yard.http.URL, "http://", "ws://", 1)+"/ws/host", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(t.Context(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(t.Context()); websocket.CloseStatus(err) != websocket.StatusCode(proto.CloseProtocolError) {
		t.Fatalf("an oversized register_desk: %v", err)
	}
	if n := yard.srv.hosts.DeskCount(); n != 0 {
		t.Fatalf("%d desks registered", n)
	}
	small := deskReg("instance-a-0123456789", "run-1")
	small.Host.Version = strings.Repeat("v", 1000)
	if h, _ := dialFakeDesk(t, yard, small); h.sessionID == "" {
		t.Fatal("a register_desk within the bound is not registered")
	}
}

// olderLinkFile is a switchyard's link file as a build before run desks
// reads it (linkFile, its run a share.Group of a name and members),
// strictly: a field it does not know leaves the link out at its start.
type olderLinkFile struct {
	ID        string `json:"id"`
	TokenHash string `json:"tokenHash"`
	SessionID string `json:"sessionId,omitempty"`
	RunID     string `json:"runId,omitempty"`
	Run       *struct {
		Name    string
		Members []share.GroupMember
	} `json:"run,omitempty"`
	Role      session.Role `json:"role"`
	Label     string       `json:"label,omitempty"`
	CreatedAt time.Time    `json:"createdAt"`
	ExpiresAt *time.Time   `json:"expiresAt,omitempty"`
	Owner     string       `json:"owner"`
	Addr      string       `json:"addr,omitempty"`
}

// A run link whose group names its desk is kept in a file without the
// desk, which a switchyard rolled back to a build before desks reads
// strictly; after a restart the link names the desk again once the desk
// is back (its register_desk), and opens it.
func TestADesksLinkFileReadsAtAnOlderSwitchyard(t *testing.T) {
	dir := t.TempDir() + "/data"
	yard := keptSwitchyard(t, dir, nil)
	desk, _ := dialFakeDesk(t, yard, deskReg("instance-a-0123456789", "run-1"))
	g := proto.RunGroup{ID: "run-1", Name: "Release", Desk: desk.sessionID, Members: []proto.RunMember{{Name: "lead", AgentID: "cat", Status: "pending"}}}
	created := runLinkOver(desk, "control", g)
	linkID := created["linkId"].(string)
	u := created["url"].(string)
	token := u[strings.LastIndex(u, "/")+1:]
	if deskOf(t, yard, token) != desk.sessionID {
		t.Fatal("the join names another desk")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "links", linkID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "desk") || strings.Contains(string(raw), desk.sessionID) {
		t.Fatalf("the link's file names the desk: %s", raw)
	}
	var older olderLinkFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&older); err != nil || older.Run == nil || older.RunID != "run-1" || len(older.Run.Members) != 1 {
		t.Fatalf("the file read as an older switchyard reads it: %+v %v", older, err)
	}
	desk.c.Close(websocket.StatusNormalClosure, "")
	<-desk.done

	yard2 := keptSwitchyard(t, dir, nil)
	again, reg := dialFakeDesk(t, yard2, deskReg("instance-a-0123456789", "run-1"))
	if again.sessionID != desk.sessionID || !strings.Contains(fmt5(reg["links"]), linkID) {
		t.Fatalf("after a restart: %s (was %s), links %v", again.sessionID, desk.sessionID, reg["links"])
	}
	if deskOf(t, yard2, token) != desk.sessionID {
		t.Fatal("after a restart the link does not name its desk")
	}
}

// A desk's judgement and its end are one step under the lock a mint
// counts itself by: a run's desk that nothing needs any more (its opening
// mint ended, no decision link made) is checked, and a mint counted on it
// between the check's first look and its judgement (the check's seam) is
// seen by the judgement, which keeps the desk; the mint is handed a desk
// that stays, mints a link over it that opens it at the switchyard, and
// the desk ends once that mint ends, nothing needing it then.
func TestADeskCheckKeepsADeskAMintTook(t *testing.T) {
	yard, local := deskPair(t, true)
	run, _ := heldRun(t, local)
	d, _, _, words := local.srv.openRunDesk(t.Context(), run.ID)
	if d == nil {
		t.Fatalf("the desk: %s", words)
	}
	handed := make(chan Desk, 1)
	var armed atomic.Bool
	armed.Store(true)
	local.srv.pubMu.Lock()
	local.srv.deskCheckSeam = func(runID string) {
		if !armed.CompareAndSwap(true, false) {
			return
		}
		h, _, _, words := local.srv.openRunDesk(context.Background(), runID)
		if h == nil {
			t.Errorf("the mint in the check's window: %s", words)
		}
		handed <- h
	}
	local.srv.pubMu.Unlock()
	// The opening mint ends with no link made: the check that follows would
	// end the desk but for the mint it is handed in its window.
	local.srv.deskMinted(run.ID)
	var h Desk
	select {
	case h = <-handed:
	case <-time.After(testWait):
		t.Fatal("the desk's check never came")
	}
	if h == nil || ended(h.Done()) || local.srv.liveDesk(run.ID) != h {
		t.Fatal("the check stopped the desk a mint was handed")
	}
	r, _ := local.srv.runs.Get(run.ID)
	g, _ := local.srv.runGroup(r)
	res, err := h.RunLink(t.Context(), "control", time.Hour, "tablet", g)
	if err != nil {
		t.Fatalf("a link over the handed desk: %v", err)
	}
	if deskOf(t, yard, res.URL[strings.LastIndex(res.URL, "/")+1:]) != h.CurrentID() {
		t.Fatal("the link does not open the handed desk")
	}
	local.srv.deskMinted(run.ID)
	deskGone(t, yard, local, run.ID, h.CurrentID())
}

// A member's link_gone says only that the member left the run's group (the
// run's next attempt replacing it): the run decision link stands here and
// is listed; the run's desk's says the link itself is gone there, and it
// ends here too, its record kept, owing its revoke there, until the
// switchyard confirms it (the retry; forgetRemote), and then the desk
// ends. A session link's from another session's connection is no word of
// its own.
func TestAMembersLinkGoneIsNoRunLinksEnd(t *testing.T) {
	_, local := deskPair(t, true)
	next := retries(local)
	run, _ := heldRun(t, local)
	linkID, _ := remoteDecisionLink(t, local, "/api/runs/"+run.ID+"/links", "phone")
	local.srv.RemoteLinkGone(linkID, sessionHex("member"), "")
	if !local.srv.Lane().DecisionLink(linkID, "", run.ID) {
		t.Fatal("a member's link_gone ended the run's decision link")
	}
	if l := linksOf(t, local, "/api/runs/"+run.ID+"/links"); len(l) != 1 {
		t.Fatalf("the run's links after a member's link_gone: %v", l)
	}
	local.srv.RemoteLinkGone(linkID, "", "another-run")
	if !local.srv.Lane().DecisionLink(linkID, "", run.ID) {
		t.Fatal("another run's desk's link_gone ended the link")
	}
	local.srv.RemoteLinkGone(linkID, "", run.ID)
	if local.srv.Lane().DecisionLink(linkID, "", run.ID) {
		t.Fatal("the desk's link_gone left the link standing")
	}
	if l := linksOf(t, local, "/api/runs/"+run.ID+"/links"); len(l) != 1 || l[0]["revokePending"] != true || local.srv.liveDesk(run.ID) == nil {
		t.Fatalf("the run's links after the desk's link_gone, before the switchyard confirmed: %v", l)
	}
	select {
	case try := <-next:
		try()
	case <-time.After(testWait):
		t.Fatal("no revoke there was armed")
	}
	until(t, "the switchyard confirmed", func() bool { return len(linksOf(t, local, "/api/runs/"+run.ID+"/links")) == 0 })
	until(t, "the desk ended", func() bool { return local.srv.liveDesk(run.ID) == nil })

	id := sessionHex("published")
	fs := newFakeSession(t, local, id, session.Options{})
	local.srv.publish(fs.l)
	until(t, "the session published", func() bool { return local.srv.publishedOf(id) != nil })
	sl, _ := remoteDecisionLink(t, local, "/api/sessions/"+id+"/links", "phone")
	local.srv.RemoteLinkGone(sl, sessionHex("other"), "")
	if !local.srv.Lane().DecisionLink(sl, id, "") {
		t.Fatal("another session's link_gone ended the session's link")
	}
	local.srv.RemoteLinkGone(sl, id, "")
	if local.srv.Lane().DecisionLink(sl, id, "") {
		t.Fatal("its own session's link_gone left it standing")
	}
}

// A member whose publication has ended (its session's end; it stays in the
// published map until its session leaves the server) carries nothing and
// is named in no group: the run's group names no session of it, and the
// run decision link's revoke goes over the run's desk, done.
func TestAnEndedPublicationCarriesNothingOfTheRun(t *testing.T) {
	yard, local := deskPair(t, true)
	run, release := heldRun(t, local)
	linkID, token := remoteDecisionLink(t, local, "/api/runs/"+run.ID+"/links", "phone")
	if err := release(); err != nil {
		t.Fatal(err)
	}
	sid := runningAttempt(t, local, run.ID, "build", 1)["sessionId"].(string)
	until(t, "the member published", func() bool { return local.srv.publishedOf(sid) != nil })
	pub := local.srv.publishedOf(sid)
	pub.Stop()
	<-pub.Done()
	if local.srv.publishedOf(sid) == nil {
		t.Fatal("the ended publication left the published map")
	}
	r, _ := local.srv.runs.Get(run.ID)
	g, carrier := local.srv.runGroup(r)
	if carrier != nil {
		t.Fatal("an ended publication carries the run")
	}
	for _, m := range g.Members {
		if m.SessionID != "" && m.SessionID == pub.CurrentID() {
			t.Fatalf("the group names the ended publication: %+v", g)
		}
	}
	if resp, out := local.do("DELETE", "/api/runs/"+run.ID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the revoke over the desk: %d %v", resp.StatusCode, out)
	}
	if code, _ := joinStatus(t, yard, token); code != http.StatusNotFound {
		t.Fatalf("the link still joins: %d", code)
	}
}

// Through a switchyard, a session evidence entry names the session by the
// id the switchyard knows it by (its publication's), never this server's
// own, which opens nothing there: in the item's read and in the evidence
// read.
func TestEvidenceThroughASwitchyardNamesThePublishedSession(t *testing.T) {
	yard, local := deskPair(t, true)
	id := sessionHex("published")
	fs := newFakeSession(t, local, id, session.Options{})
	local.srv.publish(fs.l)
	until(t, "the session published", func() bool { return local.srv.publishedOf(id) != nil })
	q := fs.ask(t, "Which name?", threeChoices)
	_, token := remoteDecisionLink(t, local, "/api/sessions/"+id+"/links", "phone")
	hosted := local.srv.publishedOf(id).CurrentID()
	if hosted == id {
		t.Fatal("the published id is the server's own: the test proves nothing")
	}
	w := &laneClient{wsClient: dialViewer(t, yard, hosted, token)}
	w.welcome = joinedRelay(t, w.wsClient)
	ref := questionRef(id, q)
	it := w.read(ref)
	found := false
	for _, e := range it.Evidence {
		if e.Ref == "session:"+id {
			found = true
			if e.Session != hosted {
				t.Fatalf("the item's session entry: %+v (published %s)", e, hosted)
			}
		}
	}
	if !found {
		t.Fatalf("the item has no session entry: %+v", it.Evidence)
	}
	if got, ctl := w.evidence(ref, "session:"+id); ctl != nil || got.Session != hosted {
		t.Fatalf("the evidence read: %+v %v (published %s)", got, ctl, hosted)
	}
	if _, there := yard.srv.hosts.Get(hosted); !there {
		t.Fatal("the switchyard does not know the id given")
	}
}

// A lane action asks the connection's own check (a publisher's peer's:
// its link held there, its host vouching for its switchyard) at its
// operation's commit boundary: refused revoked when it fails, before or at
// the commit, and the engine keeps nothing done (no command taken, or one
// settled withdrawn).
func TestALaneActAsksTheConnectionsCheck(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	ref := "task:" + run + "/build"
	w := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	retry := actionOf(t, w.read(ref), crew.TaskRetry)
	decide := func(op string, stands func() error) map[string]any {
		f := e.srv.Lane().Decide(t.Context(), linkID, "", proto.Decide{ReqID: "r-" + op, Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect, OpID: op}, nil, stands)
		var m map[string]any
		if err := json.Unmarshal(f[1:], &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := decide("op-quiet", func() error { return session.ErrRevoked }); !refusedWith(m, proto.DecideRevoked) {
		t.Fatalf("an act whose connection's check fails: %v", m)
	}
	if _, found, err := e.srv.ledger.Receipt(t.Context(), run, laneActor(linkID), "op-quiet"); err != nil || found {
		t.Fatalf("the engine took the refused act: %v %v", found, err)
	}
	// Standing as the act is judged, failing at its commit.
	var asked atomic.Int32
	m := decide("op-late", func() error {
		if asked.Add(1) == 1 {
			return nil
		}
		return session.ErrRevoked
	})
	if !refusedWith(m, proto.DecideRevoked) || asked.Load() < 2 {
		t.Fatalf("an act whose check fails at its commit: %v (asked %d)", m, asked.Load())
	}
	if r, ok := e.srv.runs.Get(run); !ok || r.Outcome == "" {
		t.Fatalf("the run moved: %+v", r)
	}
	if task := taskVia(t, e, run, "build", "failed", func(task map[string]any) bool { return true }); task["state"] != "failed" {
		t.Fatalf("the task after the refused acts: %v", task)
	}
}
