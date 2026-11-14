package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A viewer that joins through the switchyard reaches a member's session
// over this server's own connection there (internal/hostagent), which asks
// this server's link store of the viewer's link at its attach and at each
// act: a link the store holds as revoked opens none of its sessions from
// the revoke on, whether or not the switchyard has taken the revoke yet. A
// run link's revoke ends the viewers attached through it on its members'
// sessions, as a session link's ends its session's. A link the store does
// not hold is judged by the switchyard's word alone. The member runs on a process in memory, so
// what reached its input is read, never guessed from a terminal's echo.

// deskPairOnFakes is deskPair whose server runs every terminal on a
// FakeProc, handed to the test on procs as it starts.
func deskPairOnFakes(t *testing.T) (yard, local *testEnv, procs <-chan *sessiontest.FakeProc) {
	t.Helper()
	started := make(chan *sessiontest.FakeProc, 8)
	rt := &sessiontest.FakeRuntime{Terminal: func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
		p := sessiontest.NewFakeProc()
		started <- p
		return p, nil
	}}
	yard = newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	yard.srv.hosts.SetDecisions(true)
	local = newTestEnvAgents(t, nil, slog.New(slog.DiscardHandler), nil, withNativeRuntime(rt))
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: yard.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true,
		Decisions: local.srv.Lane(), LinkGone: local.srv.RemoteLinkGone, LinkUnnamed: local.srv.RemoteLinkUnnamed}})
	return yard, local, started
}

// publishedMember launches a crew of one cat on local and waits for its
// member's publication: the member's session, its id at the switchyard,
// and its process.
func publishedMember(t *testing.T, local *testEnv, procs <-chan *sessiontest.FakeProc) (runID, sid, hostedID string, proc *sessiontest.FakeProc) {
	t.Helper()
	runID = local.launchCrew(t, "Pair", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	run, _ := local.srv.runs.Get(runID)
	sid = run.Members[0].SessionID
	select {
	case proc = <-procs:
	case <-time.After(testWait):
		t.Fatal("the member's process never started")
	}
	return runID, sid, local.srv.publishedOf(sid).CurrentID(), proc
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
	code := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		_, data, err := v.c.Read(ctx)
		cancel()
		if err != nil {
			if code != proto.ErrCodeRevoked || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s was told %q, its connection then %v; want revoked, then closed", who, code, err)
			}
			return
		}
		f, err := proto.Decode(data)
		if err != nil || f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(f.Payload, &m)
		switch {
		case m["t"] == proto.CtlWelcome:
			t.Fatalf("%s was welcomed: %v", who, m)
		case m["t"] == proto.CtlError && code == "":
			code, _ = m["code"].(string)
		}
	}
}

// A decision link revoked here, a run's or a member session's, while the
// switchyard has not taken its revoke (its carriers fail there): the
// viewer attached through it to the member's session is ended, told
// revoked; a viewer that joins with it after is refused revoked; the lane
// opens nothing for it; and nothing either sends reaches the member's
// input, which takes the next keys of the server's own window. Before the
// revoke the link stands: its viewer is welcomed and its keys are written.
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

			local.srv.pubMu.Lock()
			pub := &failingPub{PublishedSession: local.srv.published[sid]}
			pub.fail.Store(true)
			local.srv.published[sid] = pub
			if rd := local.srv.desks[runID]; rd != nil {
				desk := &failingDesk{Desk: rd.desk}
				desk.fail.Store(true)
				rd.desk = desk
			}
			local.srv.pubMu.Unlock()
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
