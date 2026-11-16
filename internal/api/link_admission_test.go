package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// A viewer that joins through the switchyard reaches a member's session
// over this server's own connection there (internal/hostagent), which asks
// this server's link store of the viewer's link at its attach and at each
// act but a resize: a link the store marks revoked, or past its expiry on
// this server's clock, is refused from then on, whether or not the
// switchyard has taken the revoke yet. A run link's end (a revoke, the
// expiry sweep) starts ending the viewers attached through it on every
// session here and every run desk at the switchyard, all at once, and
// the hook returns without waiting for their closes; the run's desk on
// this server is closed beside the sessions' closes. A forgotten
// run's links stay ended here. A link the store does not
// hold is judged by the switchyard's word alone. The members run on
// processes in memory, so what reached an input is read, never guessed
// from a terminal's echo. The switchyard's taking of an owed revoke is
// held at its seam (retries) wherever a revoke is owed.

// fakeProcs are the processes a server's terminals run on, by the session
// each was started for.
type fakeProcs struct {
	mu   sync.Mutex
	by   map[string]*sessiontest.FakeProc
	more chan struct{}
}

func (f *fakeProcs) start(spec ext.TerminalSpec) (*sessiontest.FakeProc, error) {
	p := sessiontest.NewFakeProc()
	sid := ""
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "CONDUCTOR_SESSION_ID="); ok {
			sid = v
		}
	}
	f.mu.Lock()
	f.by[sid] = p
	close(f.more)
	f.more = make(chan struct{})
	f.mu.Unlock()
	return p, nil
}

// of is the process of the session sid, once it has started.
func (f *fakeProcs) of(t *testing.T, sid string) *sessiontest.FakeProc {
	t.Helper()
	deadline := time.After(testWait)
	for {
		f.mu.Lock()
		p, more := f.by[sid], f.more
		f.mu.Unlock()
		if p != nil {
			return p
		}
		select {
		case <-more:
		case <-deadline:
			t.Fatalf("the process of %s never started", sid)
		}
	}
}

// shutAtEnd ends e's server as the test ends (Shutdown): its runs, every
// session in its registry (a forgotten run's among them), its
// publications and their connections to the switchyard, and the viewers
// it holds. Registered after the server is made, it runs before the
// cleanups that close the server's stores and remove its folders
// (cleanups run last in, first out). A server a test has shut already is
// shut again at no cost.
func shutAtEnd(t *testing.T, e *testEnv) {
	t.Helper()
	t.Cleanup(func() {
		// A bound on a hang: Shutdown's own waits are bounded by it.
		ctx, cancel := context.WithTimeout(context.Background(), 2*testWait)
		defer cancel()
		e.srv.Shutdown(ctx)
	})
}

// onFakes is a server whose terminals run on FakeProcs, found by their
// session in procs, shut as the test ends (shutAtEnd).
func onFakes(t *testing.T) (*testEnv, *fakeProcs) {
	t.Helper()
	return onFakesWith(t, nil)
}

// onFakesWith is onFakes with its configuration changed by mutate (nil
// for none).
func onFakesWith(t *testing.T, mutate func(*config.Config)) (*testEnv, *fakeProcs) {
	t.Helper()
	procs := &fakeProcs{by: map[string]*sessiontest.FakeProc{}, more: make(chan struct{})}
	rt := &sessiontest.FakeRuntime{Terminal: procs.start}
	e := newTestEnvAgents(t, mutate, slog.New(slog.DiscardHandler), nil, withNativeRuntime(rt))
	shutAtEnd(t, e)
	return e, procs
}

// deskPairOnFakes is deskPair whose server runs every terminal on a
// FakeProc, found by its session in procs; both servers are shut as the
// test ends, this server first (shutAtEnd).
func deskPairOnFakes(t *testing.T) (yard, local *testEnv, procs *fakeProcs) {
	t.Helper()
	return deskPairOnFakesTold(t, nil)
}

// deskPairOnFakesTold is deskPairOnFakes whose publisher hands told the
// link of each link_gone a run's desk was told, once its read loop has
// taken it (RemoteLinkGone); nil for none.
func deskPairOnFakesTold(t *testing.T, told chan<- string) (yard, local *testEnv, procs *fakeProcs) {
	t.Helper()
	yard = newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	yard.srv.hosts.SetDecisions(true)
	shutAtEnd(t, yard)
	local, procs = onFakes(t)
	gone := func(linkID, sessionID, runID string, ended bool) {
		local.srv.RemoteLinkGone(linkID, sessionID, runID, ended)
		if told != nil && runID != "" {
			select {
			case told <- linkID:
			default:
			}
		}
	}
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: yard.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true,
		Decisions: local.srv.Lane(), LinkGone: gone, LinkUnnamed: local.srv.RemoteLinkUnnamed}})
	return yard, local, procs
}

// member is a run's member as a test reaches it: its session here, its id
// at the switchyard and its process.
type member struct {
	sid, hosted string
	proc        *sessiontest.FakeProc
}

// publishedMembers launches a crew of cats named names on local and waits
// for their publications: the run and its members, in names' order.
func publishedMembers(t *testing.T, local *testEnv, procs *fakeProcs, names ...string) (string, []member) {
	t.Helper()
	var specs []any
	for _, n := range names {
		specs = append(specs, catMember(n, "immediately"))
	}
	runID := local.launchCrew(t, "Pair", specs...)
	waitMembersPublished(t, local, runID, len(names))
	run, _ := local.srv.runs.Get(runID)
	out := make([]member, len(names))
	for i, n := range names {
		for _, m := range run.Members {
			if m.Name == n {
				out[i] = member{sid: m.SessionID, hosted: local.srv.publishedOf(m.SessionID).CurrentID(), proc: procs.of(t, m.SessionID)}
			}
		}
	}
	return runID, out
}

// publishedMember is publishedMembers for a crew of one.
func publishedMember(t *testing.T, local *testEnv, procs *fakeProcs) (runID, sid, hostedID string, proc *sessiontest.FakeProc) {
	t.Helper()
	runID, ms := publishedMembers(t, local, procs, "lead")
	return runID, ms[0].sid, ms[0].hosted, ms[0].proc
}

// relayHello joins the member's session hostedID at the switchyard through
// token, moves to the relay and says hello there; what answers the hello
// is the caller's to read.
func relayHello(t *testing.T, yard *testEnv, hostedID, token string) *wsClient {
	t.Helper()
	v := dialViewer(t, yard, hostedID, token)
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
	return v
}

// deskHello joins the run's desk at the switchyard through token, moves
// to the relay and says hello there; what answers the hello is the
// caller's to read.
func deskHello(t *testing.T, yard *testEnv, token string) *wsClient {
	t.Helper()
	c, err := dialWS(yard, "/ws/sessions/"+deskOf(t, yard, token), token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	desk := &wsClient{t: t, c: c}
	desk.expectControl(proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	desk.send(relay)
	for {
		f, err := desk.read()
		if err != nil {
			t.Fatalf("waiting for relay_ok: %v", err)
		}
		if tt, _ := proto.ParseHeader(f.Payload); f.Type == proto.TypeSignal && tt == proto.SigRelayOK {
			break
		}
	}
	desk.hello(80, 24)
	return desk
}

// keys sends text as keystrokes.
func (w *wsClient) keys(text string) {
	w.send(proto.Encode(proto.TypeInput, []byte(text)))
}

// nextWrite is the next write to the member's process.
func nextWrite(t *testing.T, proc *sessiontest.FakeProc) string {
	t.Helper()
	select {
	case b := <-proc.Input:
		return string(b)
	case <-time.After(testWait):
		t.Fatal("nothing was written to the member's process")
	}
	return ""
}

// endsRevoked reads v until its connection ends, and fails unless it
// was told revoked first and was never welcomed on the way.
func endsRevoked(t *testing.T, v *wsClient, who string) {
	t.Helper()
	endsTold(t, v, who, proto.ErrCodeRevoked, false)
}

// endsTold reads v until its connection ends, and fails unless it was
// told code first, welcomed only where welcomed allows it, and sent no
// output after it was told.
func endsTold(t *testing.T, v *wsClient, who, code string, welcomed bool) {
	t.Helper()
	told, after := "", 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		_, data, err := v.c.Read(ctx)
		cancel()
		if err != nil {
			if told != code || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s was told %q, its connection then %v; want %s, then closed", who, told, err, code)
			}
			if after > 0 {
				t.Fatalf("%s was sent %d output frames after it was told %s", who, after, code)
			}
			return
		}
		f, err := proto.Decode(data)
		if err != nil {
			continue
		}
		if told != "" && (f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback) {
			after++
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(f.Payload, &m)
		switch {
		case m["t"] == proto.CtlWelcome && !welcomed:
			t.Fatalf("%s was welcomed: %v", who, m)
		case m["t"] == proto.CtlError && told == "":
			told, _ = m["code"].(string)
		}
	}
}

// refusedAct reads v until an error, which must be code: what it sent
// last was refused.
func refusedAct(t *testing.T, v *wsClient, who, code string) {
	t.Helper()
	if m := v.expectControl(proto.CtlError); m["code"] != code {
		t.Fatalf("%s was told %v, want %s", who, m, code)
	}
}

// failRevokesThere makes every revoke of local's at the switchyard fail,
// over the run's members' publications and its desk: a revoke here is
// owed there. Its retries are held at their seam for the whole test.
func failRevokesThere(local *testEnv, runID string, sids ...string) {
	retries(local)
	local.srv.pubMu.Lock()
	defer local.srv.pubMu.Unlock()
	for _, sid := range sids {
		pub := &failingPub{PublishedSession: local.srv.published[sid]}
		pub.fail.Store(true)
		local.srv.published[sid] = pub
	}
	if rd := local.srv.desks[runID]; rd != nil {
		desk := &failingDesk{Desk: rd.desk}
		desk.fail.Store(true)
		rd.desk = desk
	}
}

// A decision link revoked here, a run's or a member session's, while the
// switchyard has not taken its revoke (its carriers fail there): the
// viewer attached through it to the member's session is ended, told
// revoked; a viewer that joins with it after is refused revoked; the lane
// opens nothing for it, and the run's desk refuses its hello, revoked; and nothing
// either sends reaches the member's input, which takes the next keys of
// the server's own window. Before the revoke the link stands: its viewer
// is welcomed and its keys are written.
func TestALinkRevokedHereOpensNoneOfItsSessions(t *testing.T) {
	for _, kind := range []string{"run", "session"} {
		t.Run(kind, func(t *testing.T) {
			yard, local, procs := deskPairOnFakes(t)
			runID, sid, hostedID, proc := publishedMember(t, local, procs)
			path := "/api/runs/" + runID + "/links"
			if kind == "session" {
				path = "/api/sessions/" + sid + "/links"
			}
			linkID, token := remoteDecisionLink(t, local, path, "phone")

			attached := relayHello(t, yard, hostedID, token)
			if w := attached.expectControl(proto.CtlWelcome); w["role"] != "control" {
				t.Fatalf("the link's viewer was welcomed as %v", w)
			}
			attached.keys("standing")
			if got := nextWrite(t, proc); got != "standing" {
				t.Fatalf("the standing link's keys were written as %q", got)
			}

			failRevokesThere(local, runID, sid)
			resp, out := local.do("DELETE", path+"/"+linkID, adminToken, nil)
			if resp.StatusCode != http.StatusBadGateway || errCodeOf(out) != "rendezvous_unavailable" {
				t.Fatalf("a revoke the switchyard did not take: %d %v", resp.StatusCode, out)
			}
			if code, out := joinStatus(t, yard, token); code != http.StatusOK {
				t.Fatalf("the switchyard holds the link no more: %d %v", code, out)
			}
			if local.srv.Lane().DecisionLink(linkID, sid, "") || local.srv.Lane().DecisionLink(linkID, "", runID) {
				t.Fatal("the revoked link opens a lane here")
			}

			endsRevoked(t, attached, "the viewer attached through the revoked link")

			joining := relayHello(t, yard, hostedID, token)
			joining.keys("joined")
			endsRevoked(t, joining, "the viewer that joined with the revoked link")

			if kind == "run" {
				refusedAct(t, deskHello(t, yard, token), "the desk's viewer of the revoked link", proto.ErrCodeRevoked)
			}

			own := dialViewer(t, local, sid, adminToken)
			own.hello(80, 24)
			own.expectControl(proto.CtlWelcome)
			own.keys("own")
			if got := nextWrite(t, proc); got != "own" {
				t.Fatalf("the member's input after the revoke took %q before the server's own window's keys", got)
			}
		})
	}
}

// heldDisconnect is a session whose ending of a link's viewers waits for
// its gate, as one whose viewer's transport does not finish closing: asked
// is closed as it is asked.
type heldDisconnect struct {
	*hostedDriver
	asked, gate chan struct{}
	ask, letGo  sync.Once
}

func (h *heldDisconnect) DisconnectLink(string) {
	h.ask.Do(func() { close(h.asked) })
	<-h.gate
}

// release lets its ending go on; again does nothing.
func (h *heldDisconnect) release() { h.letGo.Do(func() { close(h.gate) }) }

// heldSession adds a heldDisconnect to e's registry, let go and removed
// when the test ends.
func heldSession(t *testing.T, e *testEnv) *heldDisconnect {
	t.Helper()
	h := &heldDisconnect{hostedDriver: &hostedDriver{info: session.Info{ID: session.NewID(), Name: "held", Kind: session.KindHosted, Status: session.StatusRunning}},
		asked: make(chan struct{}), gate: make(chan struct{})}
	if err := e.srv.registry.Add(h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.release()
		e.srv.registry.Remove(h.info.ID)
	})
	return h
}

// askedBy waits for h to be asked to end a link's viewers.
func askedBy(t *testing.T, h *heldDisconnect, what string) {
	t.Helper()
	select {
	case <-h.asked:
	case <-time.After(testWait):
		t.Fatalf("%s: the held session was never asked", what)
	}
}

// heldWhile waits for c's value while the held session's close is still
// under way: what the test needs must not wait for it.
func heldWhile[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(testWait):
		t.Fatalf("%s waits for a viewer's close", what)
	}
	var zero T
	return zero
}

// ttlLink mints a run decision link at the switchyard lasting ttl
// seconds: its id and token there.
func ttlLink(t *testing.T, local *testEnv, runID string, ttl int, label string) (string, string) {
	t.Helper()
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "control", "decide": true, "label": label, "ttlSeconds": ttl})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("a decision link at the switchyard: %d %v", resp.StatusCode, out)
	}
	u := out["url"].(string)
	return out["link"].(map[string]any)["id"].(string), u[strings.LastIndex(u, "/")+1:]
}

// subSink is a subscription's transport in memory: closed receives why it
// was closed.
type subSink struct{ closed chan error }

func (s *subSink) WriteFrame([]byte) error { return nil }
func (s *subSink) Close(reason error)      { s.closed <- reason }

// A run's decision link revoked here, its revoke owed at the switchyard,
// starts ending every viewer attached through it on this server, all at
// once, and the revoke does not wait for their closes: with a session
// whose close does not finish held, the revoke is answered, and the
// viewers on each member's session,
// on a session the run no longer names (a member's earlier one) and on the
// run's desk are ended, each told revoked. A viewer of the run's other
// link stays, and its keys are written. The server's shutdown, the close
// still held, waits for the walk until its bound and no further; once the
// close finishes, the walk ends with nothing of it left running.
func TestARunLinksRevokeEndsItsViewersEverywhereAtOnce(t *testing.T) {
	yard, local, procs := deskPairOnFakes(t)
	runID, ms := publishedMembers(t, local, procs, "a", "b")
	path := "/api/runs/" + runID + "/links"
	linkID, token := remoteDecisionLink(t, local, path, "phone")
	_, other := remoteDecisionLink(t, local, path, "laptop")

	var viewers []*wsClient
	for _, m := range ms {
		v := relayHello(t, yard, m.hosted, token)
		v.expectControl(proto.CtlWelcome)
		viewers = append(viewers, v)
	}
	desk := joinDesk(t, yard, token)
	stays := relayHello(t, yard, ms[0].hosted, other)
	stays.expectControl(proto.CtlWelcome)
	earlier := local.createSession("cat")
	d, _ := local.srv.registry.Get(earlier)
	sink := &subSink{closed: make(chan error, 1)}
	if _, err := d.(*session.Local).AttachWith(session.AttachOptions{Role: session.RoleControl, LinkID: linkID}, sink); err != nil {
		t.Fatal(err)
	}
	held := heldSession(t, local)

	failRevokesThere(local, runID, ms[0].sid, ms[1].sid)
	answered := make(chan int, 1)
	go func() {
		resp, _ := local.do("DELETE", path+"/"+linkID, adminToken, nil)
		answered <- resp.StatusCode
	}()
	askedBy(t, held, "the revoke")
	if code := heldWhile(t, answered, "the revoke's answer"); code != http.StatusBadGateway {
		t.Fatalf("the revoke answered %d", code)
	}
	for i, v := range viewers {
		endsRevoked(t, v, "the viewer on member "+string(rune('a'+i)))
	}
	endsRevoked(t, desk.wsClient, "the desk's viewer")
	if why := heldWhile(t, sink.closed, "the earlier session's viewer's end"); !errors.Is(why, session.ErrRevoked) {
		t.Fatalf("the viewer on the earlier session was closed for %v", why)
	}
	stays.keys("stays")
	if got := nextWrite(t, ms[0].proc); got != "stays" {
		t.Fatalf("the other link's keys were written as %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		local.srv.Shutdown(ctx)
		close(stopped)
	}()
	heldWhile(t, stopped, "the shutdown")
	if ctx.Err() == nil {
		t.Fatal("the shutdown returned before its bound with the walk still under way")
	}
	held.release()
	walked := make(chan struct{})
	go func() {
		local.srv.bg.Wait()
		close(walked)
	}()
	heldWhile(t, walked, "the walk's end once its close finished")
	buf := make([]byte, 1<<22)
	stacks := string(buf[:runtime.Stack(buf, true)])
	for _, f := range []string{"disconnectLinks", "heldDisconnect"} {
		if strings.Contains(stacks, f) {
			t.Fatalf("a goroutine of the walk is left (%s):\n%s", f, stacks)
		}
	}
}

// A run link's end begun once the server's stop has made its last wait is
// done before its caller returns: with a session whose close is held, the
// revoke's caller is held inside the walk, and once the close is let go and
// the caller has returned, no goroutine it started is left. The caller
// runs under a label of the test's own, which what it starts inherits.
func TestALinksEndAfterTheStopsLastWaitIsDoneBeforeItsCallerReturns(t *testing.T) {
	local, _ := onFakes(t)
	link, _, err := local.srv.links.CreateRunLink("run-after-the-stop", session.RoleControl, "phone", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	held := heldSession(t, local)
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	local.srv.Shutdown(ctx)

	value := strconv.FormatInt(fixtureRuns.Add(1), 10)
	returned := make(chan struct{})
	go pprof.Do(context.Background(), pprof.Labels(fixtureLabel, value), func(context.Context) {
		local.srv.links.RevokeRun(link.RunID, link.ID)
		close(returned)
	})
	askedBy(t, held, "the revoke after the stop")
	// Until the close is let go, one of two states holds: the caller has
	// returned (the walk left running on its own), or the caller is inside
	// the walk (the revoke's frame and the walk's on one stack).
	inside := func(stacks string) bool {
		for _, g := range strings.Split(stacks, "\n\n") {
			if strings.Contains(g, "(*Store).RevokeRun") && strings.Contains(g, "(*Server).disconnectLinks") {
				return true
			}
		}
		return false
	}
	var stacks string
	if !waitFor(testWait, func() bool {
		_, stacks = fixtureGoroutines(value)
		select {
		case <-returned:
			return true
		default:
		}
		return inside(stacks)
	}) {
		t.Fatalf("the revoke's caller neither returned nor walked:\n%s", stacks)
	}
	select {
	case <-returned:
		_, stacks = fixtureGoroutines(value)
		t.Fatalf("the revoke's caller returned with the walk still under way:\n%s", stacks)
	default:
	}
	held.release()
	select {
	case <-returned:
	case <-time.After(testWait):
		t.Fatal("the revoke's caller never returned once the close finished")
	}
	left := 0
	if !waitFor(testWait, func() bool {
		left, stacks = fixtureGoroutines(value)
		return left == 0
	}) {
		t.Fatalf("%d goroutines the revoke started are left once its caller returned:\n%s", left, stacks)
	}
}

// Once the server's stop has closed its sessions' viewers, a session
// refuses a new one with the stop's reason (1001, server shutting down),
// by the server's own window and through a run's link alike; a run
// forgotten after the stop leaves no viewer attached through its link.
func TestASessionOfAStoppedServerAdmitsNoViewer(t *testing.T) {
	local, _ := onFakes(t)
	runID := local.launchCrew(t, "Pair", catMember("a", "immediately"))
	sid := local.waitRunning(t, runID, "a")
	_, token := local.decisionLink(t, "/api/runs/"+runID+"/links", "phone", 0)
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	local.srv.Shutdown(ctx)
	for who, tok := range map[string]string{"the server's own window": adminToken, "the run link's viewer": token} {
		v := dialViewer(t, local, sid, tok)
		v.hello(80, 24)
		if code, reason := closedAs(v); code != websocket.StatusGoingAway || reason != "" {
			t.Fatalf("%s attaching after the stop was closed %d, told %q", who, code, reason)
		}
	}
	local.srv.runs.ForgetForTest(runID)
	d, ok := local.srv.registry.Get(sid)
	if !ok {
		return
	}
	if n := d.(*session.Local).LinkViewers(); len(n) != 0 {
		t.Fatalf("viewers attached through links after the run was forgotten past the stop: %v", n)
	}
}

// A run's decision links expiring in one sweep, their revokes owed at the
// switchyard, a session whose close does not finish held: the sweep
// returns (it tells both run links without waiting for their viewers'
// closes), the viewers of both links on the members' sessions are ended,
// and the desk's viewer of the first is told expired, as the server's own
// desk tells it, while a third link, live, keeps the desk open: its desk
// viewer's lane is answered and its session viewer's keys are written.
func TestARunLinksExpiryEndsItsViewersEverywhereAtOnce(t *testing.T) {
	yard, local, procs := deskPairOnFakes(t)
	var ahead atomic.Int64
	local.srv.links.SetNowForTest(func() time.Time { return time.Now().UTC().Add(time.Duration(ahead.Load())) })
	runID, ms := publishedMembers(t, local, procs, "a", "b")
	_, first := ttlLink(t, local, runID, 3600, "phone")
	_, second := ttlLink(t, local, runID, 3600, "tablet")
	_, live := ttlLink(t, local, runID, 7200, "laptop")
	vFirst := relayHello(t, yard, ms[0].hosted, first)
	vFirst.expectControl(proto.CtlWelcome)
	vSecond := relayHello(t, yard, ms[1].hosted, second)
	vSecond.expectControl(proto.CtlWelcome)
	deskFirst := joinDesk(t, yard, first)
	deskLive := joinDesk(t, yard, live)
	stays := relayHello(t, yard, ms[0].hosted, live)
	stays.expectControl(proto.CtlWelcome)
	held := heldSession(t, local)
	failRevokesThere(local, runID, ms[0].sid, ms[1].sid)

	ahead.Store(int64(90 * time.Minute))
	swept := make(chan int, 1)
	go func() { swept <- local.srv.links.ExpireDue(time.Now().UTC().Add(90 * time.Minute)) }()
	askedBy(t, held, "the sweep")
	if n := heldWhile(t, swept, "the sweep"); n != 2 {
		t.Fatalf("the sweep told %d links", n)
	}
	endsRevoked(t, vFirst, "the first link's viewer")
	endsRevoked(t, vSecond, "the second link's viewer")
	endsTold(t, deskFirst.wsClient, "the desk's viewer of the expired link", proto.ErrCodeExpired, true)
	deskLive.list("")
	stays.keys("stays")
	if got := nextWrite(t, ms[0].proc); got != "stays" {
		t.Fatalf("the live link's keys were written as %q", got)
	}
}

// deskEndFirst is a run's desk at the switchyard whose ending of a link's
// viewers waits for the desk's own end, as when the desk's end reaches
// them first.
type deskEndFirst struct{ Desk }

func (d deskEndFirst) DisconnectLink(linkID string, why error) {
	select {
	case <-d.Done():
	case <-time.After(testWait):
	}
	d.Desk.DisconnectLink(linkID, why)
}

// A run's last decision link expiring here ends the run's desk at the
// switchyard as well (deskCheck), as the walk starts ending the link's
// viewers: the desk's viewer of the link is told expired before the desk
// ends, whether the desk's end (its walk's word held until the desk is
// done) or the walk (the desk's judgement held until the viewer is told)
// reaches it first, and the desk ends either way. While another decision
// link of the run stands, the desk stays and its viewer of the expired
// link is told expired (TestARunLinksExpiryEndsItsViewersEverywhereAtOnce).
func TestARunsLastDecisionLinkExpiringTellsItsDesksViewersExpired(t *testing.T) {
	for _, first := range []string{"the desk's end", "the walk"} {
		t.Run(first, func(t *testing.T) {
			yard, local, procs := deskPairOnFakes(t)
			var ahead atomic.Int64
			local.srv.links.SetNowForTest(func() time.Time { return time.Now().UTC().Add(time.Duration(ahead.Load())) })
			runID, _ := publishedMembers(t, local, procs, "a")
			_, token := ttlLink(t, local, runID, 3600, "phone")
			desk := joinDesk(t, yard, token)
			told := make(chan struct{})
			var once sync.Once
			let := func() { once.Do(func() { close(told) }) }
			t.Cleanup(let)
			local.srv.pubMu.Lock()
			if first == "the desk's end" {
				rd := local.srv.desks[runID]
				rd.desk = deskEndFirst{rd.desk}
			} else {
				// The desk's judgement waits until its viewer has been told.
				local.srv.deskCheckSeam = func(string) { <-told }
			}
			local.srv.pubMu.Unlock()

			ahead.Store(int64(90 * time.Minute))
			if n := local.srv.links.ExpireDue(time.Now().UTC().Add(90 * time.Minute)); n != 1 {
				t.Fatalf("the sweep told %d links", n)
			}
			endsTold(t, desk.wsClient, "the desk's viewer of the run's last link", proto.ErrCodeExpired, true)
			let()
			until(t, "the run's desk ended", func() bool { return local.srv.liveDesk(runID) == nil })
		})
	}
}

// A run's last decision link expiring while its desk's viewer's pump at
// the switchyard is held inside a write, until the desk has ended here and
// there: once let go, the viewer is told expired, not that the session
// ended, nor closed without a word.
func TestARunsLastLinksWordReachesADeskViewerWhosePumpIsHeld(t *testing.T) {
	yard, local, procs := deskPairOnFakes(t)
	// The held write outlasts no close wait of the viewer's.
	yard.srv.viewerCloseWait = 2 * time.Minute
	writes := newHoldWrites(t)
	yard.srv.viewerSeam = writes.seam()
	var ahead atomic.Int64
	local.srv.links.SetNowForTest(func() time.Time { return time.Now().UTC().Add(time.Duration(ahead.Load())) })
	runID, _ := publishedMembers(t, local, procs, "a")
	_, token := ttlLink(t, local, runID, 3600, "phone")
	desk := joinDesk(t, yard, token)
	writes.hold.Store(true)
	// The list's reply holds the viewer's pump inside its write.
	desk.send(proto.MustControl(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: desk.reqID()}))
	until(t, "the desk viewer's pump held in a write", func() bool { return writes.held.Load() > 0 })
	ahead.Store(int64(90 * time.Minute))
	if n := local.srv.links.ExpireDue(time.Now().UTC().Add(90 * time.Minute)); n != 1 {
		t.Fatalf("the sweep told %d links", n)
	}
	until(t, "the run's desk ended here", func() bool { return local.srv.liveDesk(runID) == nil })
	until(t, "the switchyard let the desk go", func() bool { return yard.srv.hosts.DeskCount() == 0 })
	writes.let()
	endsTold(t, desk.wsClient, "the desk's viewer whose pump was held", proto.ErrCodeExpired, true)
}

// The run's desk on this server is closed beside the sessions' closes:
// while a session's close is held, the revoke is answered and the desk's
// viewer of the revoked link is closed, told revoked.
func TestTheServersOwnDeskIsClosedBesideTheSessionsCloses(t *testing.T) {
	local, _ := onFakes(t)
	runID := local.launchCrew(t, "Pair", catMember("a", "immediately"))
	path := "/api/runs/" + runID + "/links"
	linkID, token := local.decisionLink(t, path, "phone", 0)
	desk := dialLane(t, local, "/ws/runs/"+runID+"/desk", token)
	desk.list("")
	held := heldSession(t, local)

	answered := make(chan int, 1)
	go func() {
		resp, _ := local.do("DELETE", path+"/"+linkID, adminToken, nil)
		answered <- resp.StatusCode
	}()
	askedBy(t, held, "the revoke")
	if code := heldWhile(t, answered, "the revoke's answer"); code != http.StatusNoContent {
		t.Fatalf("the revoke answered %d", code)
	}
	endsRevoked(t, desk.wsClient, "the server's own desk's viewer while the session's close is held")
	held.release()
}

// A session's link and a run's link due in one sweep, the session link's
// close held (its hook waits for its viewers' closes) and the run link's
// hook held until it is let go, a seam of the test's own: while the close
// is held, the run link's viewer on a member's session has its keys
// refused expired, never written, and a viewer that joins with the link is
// refused expired; once the close is let go, the run link is told and its
// viewer is ended.
func TestAnExpiredRunLinksRefusalDoesNotWaitForTheSweepsOrder(t *testing.T) {
	yard, local, procs := deskPairOnFakes(t)
	var ahead atomic.Int64
	local.srv.links.SetNowForTest(func() time.Time { return time.Now().UTC().Add(time.Duration(ahead.Load())) })
	runID, sid, hostedID, proc := publishedMember(t, local, procs)
	_, token := ttlLink(t, local, runID, 3600, "phone")
	attached := relayHello(t, yard, hostedID, token)
	attached.expectControl(proto.CtlWelcome)
	held := heldSession(t, local)
	if _, _, err := local.srv.links.Create(held.info.ID, session.RoleControl, "held", time.Hour); err != nil {
		t.Fatal(err)
	}
	// The sweep tells its links at once: the run link's hook goes on only
	// once the session link's close is let go.
	hook := local.srv.links.OnRevokeRun
	local.srv.links.OnRevokeRun = func(runID, linkID string) {
		<-held.gate
		hook(runID, linkID)
	}

	ahead.Store(int64(90 * time.Minute))
	swept := make(chan int, 1)
	go func() { swept <- local.srv.links.ExpireDue(time.Now().UTC().Add(90 * time.Minute)) }()
	attached.keys("expired")
	refusedAct(t, attached, "the run link's viewer while the session link's close is held", proto.ErrCodeExpired)
	joining := relayHello(t, yard, hostedID, token)
	endsTold(t, joining, "the viewer that joined while the session link's close is held", proto.ErrCodeExpired, false)
	own := dialViewer(t, local, sid, adminToken)
	own.hello(80, 24)
	own.expectControl(proto.CtlWelcome)
	own.keys("own")
	if got := nextWrite(t, proc); got != "own" {
		t.Fatalf("the member's input while the close is held took %q", got)
	}
	select {
	case n := <-swept:
		t.Fatalf("the sweep returned (told %d) with the session link's close held", n)
	default:
	}

	held.release()
	endsRevoked(t, attached, "the run link's viewer once the run link is told")
	select {
	case n := <-swept:
		if n != 2 {
			t.Fatalf("the sweep told %d links", n)
		}
	case <-time.After(testWait):
		t.Fatal("the sweep never returned once the close was let go")
	}
}

// The run's desk's read loop is never held by a viewer's close: a revoke
// the switchyard takes, a session whose close does not finish held, is
// answered; the desk is told the link is gone there, and its read loop
// takes that word (a revoke here again) and then the next frame, a lane
// read of a live link's desk viewer, while the close is still held.
func TestTheDesksReadLoopIsNotHeldByAViewersClose(t *testing.T) {
	told := make(chan string, 8)
	yard, local, procs := deskPairOnFakesTold(t, told)
	runID, _ := publishedMembers(t, local, procs, "a")
	path := "/api/runs/" + runID + "/links"
	linkID, token := remoteDecisionLink(t, local, path, "phone")
	_, live := remoteDecisionLink(t, local, path, "laptop")
	joinDesk(t, yard, token).list("")
	deskLive := joinDesk(t, yard, live)
	held := heldSession(t, local)

	answered := make(chan int, 1)
	go func() {
		resp, _ := local.do("DELETE", path+"/"+linkID, adminToken, nil)
		answered <- resp.StatusCode
	}()
	askedBy(t, held, "the revoke")
	if code := heldWhile(t, answered, "the revoke's answer"); code != http.StatusNoContent {
		t.Fatalf("the revoke answered %d", code)
	}
	for id := heldWhile(t, told, "the desk's link_gone"); id != linkID; id = heldWhile(t, told, "the desk's link_gone") {
	}
	deskLive.list("")
}

// A link past its expiry on this server's clock, the sweep not yet run
// and the switchyard's clock not moved: the attached viewer's keys are
// refused expired and never written, a viewer that joins is refused
// expired, and a viewer that opens the run's desk with it is refused
// expired at its hello, as the store says of it; the member's input takes
// the server's own window's keys.
func TestALinkPastItsExpiryHereIsRefusedExpired(t *testing.T) {
	yard, local, procs := deskPairOnFakes(t)
	var ahead atomic.Int64
	local.srv.links.SetNowForTest(func() time.Time { return time.Now().UTC().Add(time.Duration(ahead.Load())) })
	runID, sid, hostedID, proc := publishedMember(t, local, procs)
	linkID, token := ttlLink(t, local, runID, 3600, "phone")
	attached := relayHello(t, yard, hostedID, token)
	attached.expectControl(proto.CtlWelcome)
	attached.keys("standing")
	if got := nextWrite(t, proc); got != "standing" {
		t.Fatalf("the standing link's keys were written as %q", got)
	}

	ahead.Store(int64(2 * time.Hour))
	if why := local.srv.Lane().LinkEnded(linkID); !errors.Is(why, session.ErrExpired) {
		t.Fatalf("the store's word past the expiry: %v", why)
	}
	if code, out := joinStatus(t, yard, token); code != http.StatusOK {
		t.Fatalf("the switchyard holds the link no more: %d %v", code, out)
	}
	attached.keys("expired")
	refusedAct(t, attached, "the attached viewer's keys past the expiry", proto.ErrCodeExpired)
	joining := relayHello(t, yard, hostedID, token)
	endsTold(t, joining, "the viewer that joined past the expiry here", proto.ErrCodeExpired, false)
	refusedAct(t, deskHello(t, yard, token), "the desk's viewer past the expiry here", proto.ErrCodeExpired)
	own := dialViewer(t, local, sid, adminToken)
	own.hello(80, 24)
	own.expectControl(proto.CtlWelcome)
	own.keys("own")
	if got := nextWrite(t, proc); got != "own" {
		t.Fatalf("the member's input past the expiry took %q", got)
	}
}

// A forgotten run's link stays ended here, whatever it stood at, while
// the switchyard still holds it (its revokes there fail): a revoke of it
// waiting for an act under way, a run forgotten between the store's mark
// and the hook, a standing link the forget's one try fails to revoke
// there, and a link past its expiry here. A viewer that joins a member's
// session, still running, with it is refused revoked, and the member's
// next write is the server's own window's.
func TestAForgottenRunsLinkStaysEndedHere(t *testing.T) {
	for _, c := range []string{"a revoke waiting", "between the mark and the hook", "standing", "past its expiry"} {
		t.Run(c, func(t *testing.T) {
			yard, local, procs := deskPairOnFakes(t)
			var ahead atomic.Int64
			local.srv.links.SetNowForTest(func() time.Time { return time.Now().UTC().Add(time.Duration(ahead.Load())) })
			runID, sid, hostedID, proc := publishedMember(t, local, procs)
			path := "/api/runs/" + runID + "/links"
			linkID, token := ttlLink(t, local, runID, 3600, "phone")
			failRevokesThere(local, runID, sid)
			revoke := func() <-chan int {
				answered := make(chan int, 1)
				go func() {
					resp, _ := local.do("DELETE", path+"/"+linkID, adminToken, nil)
					answered <- resp.StatusCode
				}()
				return answered
			}
			switch c {
			case "a revoke waiting":
				links := local.srv.links
				links.HoldWait = time.Hour
				act, err := links.Hold(linkID)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(act.Release)
				answered := revoke()
				until(t, "the revoke's barrier begun", func() bool {
					h, err := links.Hold(linkID)
					if h != nil {
						h.Release()
					}
					return errors.Is(err, share.ErrRevoked)
				})
				local.srv.runs.ForgetForTest(runID)
				act.Release()
				<-answered
			case "between the mark and the hook":
				hook := local.srv.links.OnRevokeRun
				local.srv.links.OnRevokeRun = func(runID, lid string) {
					if lid == linkID {
						local.srv.runs.ForgetForTest(runID)
					}
					hook(runID, lid)
				}
				<-revoke()
			case "standing":
				local.srv.runs.ForgetForTest(runID)
			case "past its expiry":
				ahead.Store(int64(2 * time.Hour))
				local.srv.runs.ForgetForTest(runID)
			}
			if _, ok := local.srv.links.Get(linkID); ok {
				t.Fatal("the store holds the forgotten run's link")
			}
			if why := local.srv.Lane().LinkEnded(linkID); !errors.Is(why, session.ErrRevoked) || errors.Is(why, session.ErrExpired) {
				t.Fatalf("the store's word on the forgotten run's link: %v", why)
			}
			if code, out := joinStatus(t, yard, token); code != http.StatusOK {
				t.Fatalf("the switchyard holds the link no more: %d %v", code, out)
			}
			joining := relayHello(t, yard, hostedID, token)
			joining.keys("joined")
			endsRevoked(t, joining, "the viewer that joined after the run was forgotten")
			own := dialViewer(t, local, sid, adminToken)
			own.hello(80, 24)
			own.expectControl(proto.CtlWelcome)
			own.keys("own")
			if got := nextWrite(t, proc); got != "own" {
				t.Fatalf("the member's input took %q before the server's own window's keys", got)
			}
		})
	}
}

// heldPub is a session's publication whose revokes at the switchyard wait
// for its gate, the rest its own.
type heldPub struct {
	PublishedSession
	gate chan struct{}
}

func (p *heldPub) Revoke(ctx context.Context, linkID string) error {
	select {
	case <-p.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.PublishedSession.Revoke(ctx, linkID)
}

func (p *heldPub) Decisions() bool { return carriesDecisions(p.PublishedSession) }

// A forgotten run's link whose revoke the forget's one try takes at the
// switchyard: ended here while the try is under way, forgotten here once
// the switchyard has taken it.
func TestAForgottenRunsLinkTheSwitchyardTookIsForgottenHere(t *testing.T) {
	_, local, procs := deskPairOnFakes(t)
	runID, sid, _, _ := publishedMember(t, local, procs)
	linkID, _ := remoteDecisionLink(t, local, "/api/runs/"+runID+"/links", "phone")
	pub := &heldPub{gate: make(chan struct{})}
	local.srv.pubMu.Lock()
	pub.PublishedSession = local.srv.published[sid]
	local.srv.published[sid] = pub
	local.srv.pubMu.Unlock()
	local.srv.runs.ForgetForTest(runID)
	if why := local.srv.Lane().LinkEnded(linkID); !errors.Is(why, session.ErrRevoked) {
		t.Fatalf("the store's word while the forget's try is under way: %v", why)
	}
	close(pub.gate)
	until(t, "the link forgotten once the switchyard took its revoke", func() bool {
		return local.srv.Lane().LinkEnded(linkID) == nil
	})
	if _, err := local.srv.links.Live(linkID); !errors.Is(err, share.ErrUnknownLink) {
		t.Fatalf("the store's read of it: %v", err)
	}
}

// A link the store does not hold, a run's plain control link the
// switchyard minted, opens the member's session as the switchyard says:
// its viewer is welcomed and its keys are written.
func TestALinkTheStoreDoesNotHoldOpensItsSessionAsTheSwitchyardSays(t *testing.T) {
	yard, local, procs := deskPairOnFakes(t)
	runID, _, hostedID, proc := publishedMember(t, local, procs)
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "control", "label": "plain"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("a plain link at the switchyard: %d %v", resp.StatusCode, out)
	}
	linkID := out["link"].(map[string]any)["id"].(string)
	if _, held := local.srv.links.Get(linkID); held {
		t.Fatal("the store holds the switchyard's plain link")
	}
	u := out["url"].(string)
	v := relayHello(t, yard, hostedID, u[strings.LastIndex(u, "/")+1:])
	if w := v.expectControl(proto.CtlWelcome); w["role"] != "control" {
		t.Fatalf("the plain link's viewer was welcomed as %v", w)
	}
	v.keys("plain")
	if got := nextWrite(t, proc); got != "plain" {
		t.Fatalf("the plain link's keys were written as %q", got)
	}
}

// fixtureLabel is the profiler label (runtime/pprof) a subtest's
// goroutines carry when it runs under underFixtureLabel: a goroutine
// inherits its maker's labels, so every goroutine the subtest's servers
// start, and those their goroutines start, carries the subtest's value,
// and no goroutine another test of the binary starts does.
const fixtureLabel = "link-admission-fixture"

// fixtureRuns numbers the label's values, one per subtest run.
var fixtureRuns atomic.Int64

// underFixtureLabel runs the subtest f as name with the label's next
// value, and returns that value.
func underFixtureLabel(t *testing.T, name string, f func(*testing.T)) string {
	t.Helper()
	value := strconv.FormatInt(fixtureRuns.Add(1), 10)
	pprof.Do(context.Background(), pprof.Labels(fixtureLabel, value), func(context.Context) {
		t.Run(name, f)
	})
	return value
}

// fixtureGoroutines counts the goroutines that carry the label's value
// and run this repository's code outside a test's runner (a server's, a
// session's, a connection's), from one read of the goroutine profile, and
// returns their stacks.
func fixtureGoroutines(value string) (int, string) {
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 1); err != nil {
		return -1, err.Error()
	}
	mark := fmt.Sprintf("# labels: {%q:%q}", fixtureLabel, value)
	n := 0
	var own []string
	for _, g := range strings.Split(buf.String(), "\n\n") {
		if !strings.Contains(g, mark) || !strings.Contains(g, "rocksolidlabs/conductor/internal/") ||
			strings.Contains(g, "testing.tRunner") {
			continue
		}
		// A record of the profile is the goroutines of one stack and one
		// set of labels, headed "N @ ...".
		c := 1
		for _, line := range strings.Split(g, "\n") {
			if count, _, ok := strings.Cut(line, " @ "); ok {
				if v, err := strconv.Atoi(count); err == nil {
					c = v
				}
				break
			}
		}
		n += c
		own = append(own, g)
	}
	return n, strings.Join(own, "\n\n")
}

// This file's fixture ends what its servers started before their stores
// close and their folders go: once a test that launched a run, published
// its member, attached a viewer and a desk viewer through the switchyard
// and forgot the run has returned, and its cleanups have run, no goroutine
// its servers started is left running this repository's code. A goroutine
// that has signalled its end (closed its done channel) can still be
// returning, so the count is polled, within the tests' wait, until none
// is left; the subtest runs under a label of its own (underFixtureLabel),
// so a goroutine another test starts meanwhile is not counted.
func TestTheFixtureEndsWhatItsServersStarted(t *testing.T) {
	value := underFixtureLabel(t, "a forgotten run", func(t *testing.T) {
		yard, local, procs := deskPairOnFakes(t)
		runID, _, hostedID, _ := publishedMember(t, local, procs)
		_, token := remoteDecisionLink(t, local, "/api/runs/"+runID+"/links", "phone")
		relayHello(t, yard, hostedID, token).expectControl(proto.CtlWelcome)
		joinDesk(t, yard, token)
		local.srv.runs.ForgetForTest(runID)
	})
	left, stacks := 0, ""
	if !waitFor(testWait, func() bool {
		left, stacks = fixtureGoroutines(value)
		return left == 0
	}) {
		t.Fatalf("%d goroutines its servers started still run this repository's code %s after the test's cleanups ran:\n%s", left, testWait, stacks)
	}
}

// The fixture shuts its server (shutAtEnd) before the cleanups that close
// the server's stores and remove its folders: a run the test leaves
// running is stopped in its record, which the run's records closed before
// the stop would not let it write (a save after their Close is dropped).
// The data folder is the parent test's, so the record is read, as it is
// on disk, once the subtest's cleanups have all run.
func TestTheFixtureShutsItsServerBeforeItsStoresClose(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var runID string
	t.Run("a run left running", func(t *testing.T) {
		local, procs := onFakesWith(t, func(c *config.Config) { c.DataDir = dir })
		runID = local.launchCrew(t, "Left", catMember("lead", "immediately"))
		if procs.of(t, local.waitRunning(t, runID, "lead")).Stopped() {
			t.Fatal("the run's member was stopped before the test ended")
		}
	})
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	records, err := st.Sub("runs")
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if ok, err := records.Load(runID+".json", &rec); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("the run left running has no record once the test's cleanups ran: its stop was never written")
	}
	if rec["state"] != crew.RunStopped || rec["stoppedAt"] == nil {
		t.Fatalf("the run left running is %v (stopped at %v) in its record once the test's cleanups ran; want %s", rec["state"], rec["stoppedAt"], crew.RunStopped)
	}
}
