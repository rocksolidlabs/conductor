package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// A viewer that joins through the switchyard reaches a member's session
// over this server's own connection there (internal/hostagent), which asks
// this server's link store of the viewer's link at its attach and at each
// act: a link the store holds as revoked, from the moment its revoke
// begins, or past its expiry on this server's clock, opens none of its
// sessions, whether or not the switchyard has taken the revoke yet, and a
// viewer it admits is ended at the link's expiry. A run link's revoke ends
// the viewers attached through it on every session and every run desk
// here, all at once, and a forgotten run's link whose revoke is still owed
// stays revoked here. A link the store does not hold is judged by the
// switchyard's word alone. The members run on processes in memory, so what
// reached an input is read, never guessed from a terminal's echo. The
// switchyard's taking of an owed revoke is held at its seam (retries)
// wherever a revoke is owed.

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

// deskPairOnFakes is deskPair whose server runs every terminal on a
// FakeProc, found by its session in procs.
func deskPairOnFakes(t *testing.T) (yard, local *testEnv, procs *fakeProcs) {
	t.Helper()
	procs = &fakeProcs{by: map[string]*sessiontest.FakeProc{}, more: make(chan struct{})}
	rt := &sessiontest.FakeRuntime{Terminal: procs.start}
	yard = newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	yard.srv.hosts.SetDecisions(true)
	local = newTestEnvAgents(t, nil, slog.New(slog.DiscardHandler), nil, withNativeRuntime(rt))
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: yard.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true,
		Decisions: local.srv.Lane(), LinkGone: local.srv.RemoteLinkGone, LinkUnnamed: local.srv.RemoteLinkUnnamed}})
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
// opens nothing for it, and the run's desk refuses its hello; and nothing
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
				refusedAct(t, desk, "the desk's viewer of the revoked link", proto.DecideNotDecisionLink)
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

// subSink is a subscription's transport in memory: closed receives why it
// was closed.
type subSink struct{ closed chan error }

func (s *subSink) WriteFrame([]byte) error { return nil }
func (s *subSink) Close(reason error)      { s.closed <- reason }

// A run's decision link revoked here, its revoke owed at the switchyard,
// ends every viewer attached through it on this server, all at once: on
// each member's session, on a session the run no longer names (a member's
// earlier one), and on the run's desk, while two sessions whose closes do
// not finish are both still being asked; each is told revoked. A viewer of
// the run's other link stays, and its keys are written.
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
	var held []*heldDisconnect
	for range 2 {
		h := &heldDisconnect{hostedDriver: &hostedDriver{info: session.Info{ID: session.NewID(), Name: "held", Kind: session.KindHosted, Status: session.StatusRunning}},
			asked: make(chan struct{}), gate: make(chan struct{})}
		if err := local.srv.registry.Add(h); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			h.release()
			local.srv.registry.Remove(h.info.ID)
		})
		held = append(held, h)
	}

	failRevokesThere(local, runID, ms[0].sid, ms[1].sid)
	answered := make(chan int, 1)
	go func() {
		resp, _ := local.do("DELETE", path+"/"+linkID, adminToken, nil)
		answered <- resp.StatusCode
	}()
	for _, h := range held {
		select {
		case <-h.asked:
		case <-time.After(testWait):
			t.Fatal("a session was not asked while another's close was still under way")
		}
	}
	for i, v := range viewers {
		endsRevoked(t, v, "the viewer on member "+string(rune('a'+i)))
	}
	endsRevoked(t, desk.wsClient, "the desk's viewer")
	select {
	case why := <-sink.closed:
		if !errors.Is(why, session.ErrRevoked) {
			t.Fatalf("the viewer on the earlier session was closed for %v", why)
		}
	case <-time.After(testWait):
		t.Fatal("the viewer on a session the run no longer names stays")
	}
	for _, h := range held {
		h.release()
	}
	if code := <-answered; code != http.StatusBadGateway {
		t.Fatalf("the revoke answered %d", code)
	}
	stays.keys("stays")
	if got := nextWrite(t, ms[0].proc); got != "stays" {
		t.Fatalf("the other link's keys were written as %q", got)
	}
}

// A revoke begins by stopping the link's acts and waiting for those under
// way, and only then marks the link: from its beginning the link is ended
// here. With an act held on the link (the store's own hold) and the
// revoke waiting on it, the store's word is revoked, a viewer that joins
// is refused, and an attached viewer's keys are refused and never
// written, the server's own window's taking the member's input; once the
// act lets go the revoke is made, and its viewer ended.
func TestALinkIsEndedHereFromItsRevokesBeginning(t *testing.T) {
	yard, local, procs := deskPairOnFakes(t)
	runID, sid, hostedID, proc := publishedMember(t, local, procs)
	path := "/api/runs/" + runID + "/links"
	linkID, token := remoteDecisionLink(t, local, path, "phone")
	attached := relayHello(t, yard, hostedID, token)
	attached.expectControl(proto.CtlWelcome)
	attached.keys("standing")
	if got := nextWrite(t, proc); got != "standing" {
		t.Fatalf("the standing link's keys were written as %q", got)
	}

	links := local.srv.links
	links.HoldWait = time.Hour
	act, err := links.Hold(linkID)
	if err != nil {
		t.Fatal(err)
	}
	// Let go however the test ends, so the revoke it holds ends too.
	t.Cleanup(act.Release)
	failRevokesThere(local, runID, sid)
	answered := make(chan int, 1)
	go func() {
		resp, _ := local.do("DELETE", path+"/"+linkID, adminToken, nil)
		answered <- resp.StatusCode
	}()
	until(t, "the revoke's barrier begun", func() bool {
		h, err := links.Hold(linkID)
		if h != nil {
			h.Release()
		}
		return errors.Is(err, share.ErrRevoked)
	})
	if l, ok := links.Get(linkID); !ok || l.Revoked {
		t.Fatalf("the link is marked before its act let go: %+v", l)
	}
	if _, why := local.srv.Lane().LinkEnded(linkID); !errors.Is(why, session.ErrRevoked) || errors.Is(why, session.ErrExpired) {
		t.Fatalf("the store's word while the revoke waits: %v", why)
	}
	joining := relayHello(t, yard, hostedID, token)
	endsRevoked(t, joining, "the viewer that joined while the revoke waits")
	attached.keys("held")
	refusedAct(t, attached, "the attached viewer's keys while the revoke waits", proto.ErrCodeRevoked)
	own := dialViewer(t, local, sid, adminToken)
	own.hello(80, 24)
	own.expectControl(proto.CtlWelcome)
	own.keys("own")
	if got := nextWrite(t, proc); got != "own" {
		t.Fatalf("the member's input while the revoke waits took %q", got)
	}
	select {
	case code := <-answered:
		t.Fatalf("the revoke answered %d with an act held", code)
	default:
	}

	act.Release()
	if code := <-answered; code != http.StatusBadGateway {
		t.Fatalf("the revoke answered %d", code)
	}
	if l, ok := links.Get(linkID); !ok || !l.Revoked {
		t.Fatalf("the link after the revoke: %+v", l)
	}
	endsRevoked(t, attached, "the attached viewer after the revoke")
	if _, why := local.srv.Lane().LinkEnded(linkID); !errors.Is(why, session.ErrRevoked) {
		t.Fatalf("the store's word after the revoke: %v", why)
	}
}

// A link's viewer is admitted until the link's expiry as this server's
// store holds it (the grant the session ends it at, which internal/
// hostagent's tests make lapse); once this server's clock has passed the
// expiry, with no sweep run and the switchyard's clock not moved, the
// attached viewer's keys are refused expired and never written, and a
// viewer that joins is refused expired.
func TestALinkPastItsExpiryHereEndsItsViewers(t *testing.T) {
	yard, local, procs := deskPairOnFakes(t)
	var ahead atomic.Int64
	local.srv.links.SetNowForTest(func() time.Time { return time.Now().UTC().Add(time.Duration(ahead.Load())) })
	runID, sid, hostedID, proc := publishedMember(t, local, procs)
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "control", "decide": true, "label": "phone", "ttlSeconds": 3600})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("a decision link at the switchyard: %d %v", resp.StatusCode, out)
	}
	linkID := out["link"].(map[string]any)["id"].(string)
	u := out["url"].(string)
	token := u[strings.LastIndex(u, "/")+1:]
	held, ok := local.srv.links.Get(linkID)
	if !ok || held.ExpiresAt == nil {
		t.Fatalf("the store's record of the link: %+v", held)
	}
	if until, why := local.srv.Lane().LinkEnded(linkID); why != nil || !until.Equal(*held.ExpiresAt) {
		t.Fatalf("the store's word on a standing link: until %v, %v; want until %v", until, why, *held.ExpiresAt)
	}
	attached := relayHello(t, yard, hostedID, token)
	attached.expectControl(proto.CtlWelcome)
	attached.keys("standing")
	if got := nextWrite(t, proc); got != "standing" {
		t.Fatalf("the standing link's keys were written as %q", got)
	}

	ahead.Store(int64(2 * time.Hour))
	if _, why := local.srv.Lane().LinkEnded(linkID); !errors.Is(why, session.ErrExpired) {
		t.Fatalf("the store's word past the expiry: %v", why)
	}
	if code, out := joinStatus(t, yard, token); code != http.StatusOK {
		t.Fatalf("the switchyard holds the link no more: %d %v", code, out)
	}
	attached.keys("expired")
	refusedAct(t, attached, "the attached viewer's keys past the expiry", proto.ErrCodeExpired)
	joining := relayHello(t, yard, hostedID, token)
	endsTold(t, joining, "the viewer that joined past the expiry here", proto.ErrCodeExpired, false)
	own := dialViewer(t, local, sid, adminToken)
	own.hello(80, 24)
	own.expectControl(proto.CtlWelcome)
	own.keys("own")
	if got := nextWrite(t, proc); got != "own" {
		t.Fatalf("the member's input past the expiry took %q", got)
	}
}

// A run forgotten while the revoke of one of its links is still owed at
// the switchyard keeps the link revoked here: a viewer that joins the
// member's session with it, the switchyard still holding it, is refused
// revoked. Once the forgotten run's one try takes the revoke there, the
// mark goes.
func TestAForgottenRunsOwedRevokeKeepsItsLinkRevokedHere(t *testing.T) {
	for _, taken := range []bool{false, true} {
		name := "owed"
		if taken {
			name = "taken"
		}
		t.Run(name, func(t *testing.T) {
			yard, local, procs := deskPairOnFakes(t)
			runID, sid, hostedID, _ := publishedMember(t, local, procs)
			path := "/api/runs/" + runID + "/links"
			linkID, token := remoteDecisionLink(t, local, path, "phone")
			failRevokesThere(local, runID, sid)
			if resp, out := local.do("DELETE", path+"/"+linkID, adminToken, nil); resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("a revoke the switchyard did not take: %d %v", resp.StatusCode, out)
			}
			if taken {
				local.srv.pubMu.Lock()
				local.srv.published[sid].(*failingPub).fail.Store(false)
				local.srv.pubMu.Unlock()
			}
			local.srv.runs.ForgetForTest(runID)
			if _, ok := local.srv.links.Get(linkID); ok {
				t.Fatal("the store holds the forgotten run's link")
			}
			if taken {
				until(t, "the mark gone with the revoke taken", func() bool {
					_, why := local.srv.Lane().LinkEnded(linkID)
					return why == nil
				})
				return
			}
			if _, why := local.srv.Lane().LinkEnded(linkID); !errors.Is(why, session.ErrRevoked) {
				t.Fatalf("the store's word on the forgotten run's link: %v", why)
			}
			if code, out := joinStatus(t, yard, token); code != http.StatusOK {
				t.Fatalf("the switchyard holds the link no more: %d %v", code, out)
			}
			joining := relayHello(t, yard, hostedID, token)
			endsRevoked(t, joining, "the viewer that joined after the run was forgotten")
		})
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
