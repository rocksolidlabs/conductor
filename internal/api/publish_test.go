package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

type uplinkPublisher struct{ u *hostagent.Uplink }

func (p uplinkPublisher) Publish(ctx context.Context, local *session.Local) (PublishedSession, error) {
	return p.u.Publish(ctx, local)
}

func (p uplinkPublisher) Server() string { return p.u.ServerURL }

// A server with a rendezvous publishes every session it starts there: the
// rendezvous lists it as hosted by this server, a viewer there types into
// it through the relay, its attention state shows there but not its
// activity, and ending the session here ends it there.
func TestAPublishedServerSessionIsViewableAtTheRendezvous(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})

	id := local.createSession("cat")
	var hosted session.Info
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && hosted.ID == "" {
		rendezvous.srv.Registry().Each(func(d session.Driver) {
			if i := d.Info(); i.Kind == session.KindHosted && i.HostName == "office-server" {
				hosted = i
			}
		})
		time.Sleep(20 * time.Millisecond)
	}
	if hosted.ID == "" {
		t.Fatal("the rendezvous never listed the session")
	}
	if hosted.ID == id || hosted.AgentID != "cat" || !strings.HasPrefix(hosted.Name, "cat #") {
		t.Fatalf("hosted %+v", hosted)
	}
	// The local session's activity says where it is published.
	d, _ := local.srv.Registry().Get(id)
	deadline = time.Now().Add(5 * time.Second)
	var link string
	for time.Now().Before(deadline) && link == "" {
		for _, e := range d.(*session.Local).Activity() {
			if e.Type == session.ActivityLink && strings.Contains(e.Message, "rendezvous") {
				link = e.URL
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if link != "https://rendezvous.example.net/sessions/"+hosted.ID {
		t.Fatalf("published link %q", link)
	}

	v := dialViewer(t, rendezvous, hosted.ID, adminToken)
	v.expectControl(proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	v.send(relay)
	for {
		f, err := v.read()
		if err != nil {
			t.Fatalf("waiting for relay_ok: %v", err)
		}
		var m map[string]any
		if f.Type == proto.TypeSignal && json.Unmarshal(f.Payload, &m) == nil && m["t"] == proto.SigRelayOK {
			break
		}
	}
	v.hello(90, 25)
	v.expectControl(proto.CtlWelcome)
	v.expectControl(proto.CtlReady)
	v.send(proto.Encode(proto.TypeInput, []byte("hello from afar\n")))
	v.expectOutput("hello from afar")

	// A viewer on the local server sees the same session.
	lv := dialViewer(t, local, id, adminToken)
	lv.hello(80, 24)
	lv.expectControl(proto.CtlWelcome)
	lv.expectOutput("hello from afar")

	// An event reported to the local server stays here: the rendezvous's
	// event hub (its admin stream and webhooks) never has it. An attention
	// change reported after it reaches the rendezvous, with its state and
	// without its words, so by then the event would have too.
	var mu sync.Mutex
	var there []session.ActivityEntry
	rendezvous.srv.events.addSink(func(sid string, e session.ActivityEntry, _ session.AttentionState) {
		mu.Lock()
		there = append(there, e)
		mu.Unlock()
	})
	resp, _ := local.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "progress", "message": "step 1"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("event: %d", resp.StatusCode)
	}
	if resp, _ := local.do("POST", "/api/sessions/"+id+"/attention", adminToken, map[string]any{"state": "needs_input", "message": "pick one"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("attention: %d", resp.StatusCode)
	}
	rd, _ := rendezvous.srv.Registry().Get(hosted.ID)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && rd.Info().Attention.State != session.AttentionNeedsInput {
		time.Sleep(20 * time.Millisecond)
	}
	if att := rd.Info().Attention; att.State != session.AttentionNeedsInput || att.Message != "" {
		t.Fatalf("attention at the rendezvous %+v", att)
	}
	mu.Lock()
	if len(there) != 0 {
		t.Fatalf("the rendezvous was told the session's activity: %+v", there)
	}
	mu.Unlock()
	found := false
	for _, e := range d.(*session.Local).Activity() {
		found = found || (e.Type == session.ActivityProgress && e.Message == "step 1")
	}
	if !found {
		t.Fatal("the event is not in the session's activity here")
	}

	// Ending the session here ends it there.
	if resp, _ := local.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !rd.Info().Status.Ended() {
		time.Sleep(20 * time.Millisecond)
	}
	if !rd.Info().Status.Ended() {
		t.Fatalf("rendezvous still shows %s", rd.Info().Status)
	}
}

// A link to a published session is minted at the rendezvous, over the host
// connection: its URL and invite are the rendezvous's, and the rendezvous
// resolves it.
func TestLinkOnAPublishedSessionIsMintedAtTheRendezvous(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	id := local.createSession("cat")
	deadline := time.Now().Add(10 * time.Second)
	for local.srv.publishedOf(id) == nil {
		if time.Now().After(deadline) {
			t.Fatal("not published")
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view", "label": "for the PR", "ttlSeconds": 3600})
	url, _ := out["url"].(string)
	if resp.StatusCode != http.StatusCreated || !strings.HasPrefix(url, "https://rendezvous.example.net/join/") || out["remote"] != true || out["token"] != nil {
		t.Fatalf("link: %d %v", resp.StatusCode, out)
	}
	token := strings.TrimPrefix(url, "https://rendezvous.example.net/join/")
	if out["invite"] != "conductor://rendezvous.example.net/join/"+token {
		t.Fatalf("invite %v", out["invite"])
	}
	link, _ := out["link"].(map[string]any)
	if link["label"] != "for the PR" || link["role"] != "view" || link["remote"] != true {
		t.Fatalf("link %v", link)
	}
	if resp, out := rendezvous.do("GET", "/api/join/"+token, "", nil); resp.StatusCode != http.StatusOK || out["session"] == nil {
		t.Fatalf("the rendezvous resolves it: %d %v", resp.StatusCode, out)
	}
	// Listed here as the rendezvous's, with no viewer count of its own.
	if _, out := local.do("GET", "/api/sessions/"+id+"/links", adminToken, nil); !strings.Contains(fmt.Sprint(out), "for the PR") || !strings.Contains(fmt.Sprint(out), "remote:true") {
		t.Fatalf("the remote link is not listed: %v", out)
	}
}

// A link minted at the rendezvous is revoked from here: the DELETE goes over
// the host connection, the rendezvous refuses the join afterwards, and the
// list no longer holds it. A link the rendezvous forgot is dropped the same way.
func TestRemoteLinksAreListedAndRevokedFromTheLocalServer(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	id := local.createSession("cat")
	deadline := time.Now().Add(10 * time.Second)
	for local.srv.publishedOf(id) == nil {
		if time.Now().After(deadline) {
			t.Fatal("not published")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control", "label": "pairing"})
	link, _ := out["link"].(map[string]any)
	linkID, _ := link["id"].(string)
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	if linkID == "" || token == "" {
		t.Fatalf("link %v", out)
	}
	if resp, _ := local.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp, out := rendezvous.do("GET", "/api/join/"+token, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the rendezvous still resolves it: %d %v", resp.StatusCode, out)
	}
	if _, out := local.do("GET", "/api/sessions/"+id+"/links", adminToken, nil); strings.Contains(fmt.Sprint(out), "pairing") {
		t.Fatalf("still listed: %v", out)
	}
	if resp, _ := local.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoking it twice: %d", resp.StatusCode)
	}
}

// A server with no host token publishes to a switchyard that admits open
// hosts: the session is listed there and a link is minted there.
func TestAServerPublishesToAnOpenSwitchyardWithoutAToken(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) {
		c.PublicURL = "https://rendezvous.example.net"
		c.Switchyard.Enabled = true
		c.Switchyard.OpenHosts = true
	})
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, HostName: "open-box", RelayOnly: true}})
	id := local.createSession("cat")
	var hosted session.Info
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && hosted.ID == "" {
		rendezvous.srv.Registry().Each(func(d session.Driver) {
			if i := d.Info(); i.Kind == session.KindHosted && i.HostName == "open-box" {
				hosted = i
			}
		})
		time.Sleep(20 * time.Millisecond)
	}
	if hosted.ID == "" {
		t.Fatal("the open switchyard never listed the session")
	}
	resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true || !strings.HasPrefix(out["url"].(string), "https://rendezvous.example.net/join/") {
		t.Fatalf("link: %d %v", resp.StatusCode, out)
	}
}

// A link asked for while the session is not published (the switchyard
// refused or is down) is made here and the reply says which switchyard and
// why, so the Share dialog can say it in words.
func TestLinkOnAnUnpublishedSessionSaysWhy(t *testing.T) {
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: "http://127.0.0.1:1", HostName: "office-server", RelayOnly: true}})
	id := local.createSession("cat")
	resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	rv, _ := out["rendezvous"].(map[string]any)
	if resp.StatusCode != http.StatusCreated || out["remote"] != nil || out["token"] == nil || rv == nil || rv["server"] != "http://127.0.0.1:1" || rv["error"] == "" {
		t.Fatalf("link: %d %v", resp.StatusCode, out)
	}
	if !strings.HasPrefix(out["url"].(string), "http://example.test/join/") {
		t.Fatalf("not a local link: %v", out["url"])
	}
	// Not publishing is no error of the agent's: a status line says it.
	d, _ := local.srv.Registry().Get(id)
	var noted bool
	for _, e := range d.(*session.Local).Activity() {
		if e.Type == session.ActivityError {
			t.Fatalf("an error entry for a switchyard that is down: %+v", e)
		}
		noted = noted || (e.Type == session.ActivityStatus && strings.HasPrefix(e.Message, "not shared through the switchyard: "))
	}
	if !noted {
		t.Fatalf("no status line: %+v", d.(*session.Local).Activity())
	}
}

// waitMembersPublished waits until every member of a run with a session is published.
func waitMembersPublished(t *testing.T, e *testEnv, runID string, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		run, _ := e.srv.runs.Get(runID)
		published := 0
		for _, m := range run.Members {
			if m.SessionID != "" && e.srv.publishedOf(m.SessionID) != nil {
				published++
			}
		}
		if published >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d members published", published, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A run whose members are published is shared at the rendezvous: one link,
// whose join lists the members as hosted sessions there, following a member
// added later; the run's links list it, and a revoke here revokes it there.
func TestARunLinkOnAPublishedRunIsMintedAtTheRendezvous(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"), catMember("core", "immediately"))
	waitMembersPublished(t, local, runID, 2)
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view", "label": "for the team"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("run link: %d %v", resp.StatusCode, out)
	}
	linkID := out["link"].(map[string]any)["id"].(string)
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	members := func() []any {
		_, got := rendezvous.do("GET", "/api/join/"+token, token, nil)
		run, _ := got["run"].(map[string]any)
		ms, _ := run["members"].([]any)
		return ms
	}
	if ms := members(); len(ms) != 2 || ms[0].(map[string]any)["kind"] != "hosted" || ms[0].(map[string]any)["sessionId"] == nil {
		t.Fatalf("the rendezvous's join: %v", ms)
	}
	// A viewer through the run link reaches a member's session there.
	sid := members()[0].(map[string]any)["sessionId"].(string)
	v := dialViewer(t, rendezvous, sid, token)
	v.hello(80, 24)
	v.expectControl(proto.CtlWelcome)
	if resp, out := local.do("POST", "/api/runs/"+runID+"/members", adminToken, catMember("tests", "immediately")); resp.StatusCode >= 300 {
		t.Fatalf("add member: %d %v", resp.StatusCode, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(members()) != 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the rendezvous never heard of the third member: %v", members())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, out := local.do("GET", "/api/runs/"+runID+"/links", adminToken, nil); !strings.Contains(fmt.Sprint(out), "remote:true") {
		t.Fatalf("the run's links: %v", out)
	}
	if resp, _ := local.do("DELETE", "/api/runs/"+runID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp, out := rendezvous.do("GET", "/api/join/"+token, token, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the rendezvous still resolves it: %d %v", resp.StatusCode, out)
	}
}

// With no member published, the run's link is made here and says why.
func TestARunLinkFallsBackToALocalLinkWhenNoMemberIsPublished(t *testing.T) {
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: "http://127.0.0.1:1", Token: "test-host-token", HostName: "office-server"}})
	runID := local.launchCrew(t, "Alone", catMember("lead", "immediately"))
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"})
	rv, _ := out["rendezvous"].(map[string]any)
	if resp.StatusCode != http.StatusCreated || out["remote"] == true || rv == nil || rv["error"] == "" {
		t.Fatalf("fallback: %d %v", resp.StatusCode, out)
	}
}

// A session that ended takes its links with it: none are listed, local or
// minted at the rendezvous, the rendezvous no longer resolves them, and a
// revoke of one (a dialog still showing it) succeeds rather than erring.
func TestASessionThatEndedTakesItsLinks(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	id := local.createSession("cat")
	deadline := time.Now().Add(10 * time.Second)
	for local.srv.publishedOf(id) == nil {
		if time.Now().After(deadline) {
			t.Fatal("not published")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	remoteID := out["link"].(map[string]any)["id"].(string)
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	local.srv.SetPublisher(nil)
	_, out = local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	localID := out["link"].(map[string]any)["id"].(string)
	if resp, _ := local.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode >= 300 {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		d, _ := local.srv.registry.Get(id)
		if d != nil && d.Info().Status.Ended() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		_, out := local.do("GET", "/api/sessions/"+id+"/links", adminToken, nil)
		if ls, _ := out["links"].([]any); len(ls) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an ended session still lists links: %v", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, lid := range []string{remoteID, localID} {
		start := time.Now()
		if resp, out := local.do("DELETE", "/api/sessions/"+id+"/links/"+lid, adminToken, nil); resp.StatusCode != http.StatusNoContent || time.Since(start) > 3*time.Second {
			t.Fatalf("revoking %s after the end: %d %v in %v", lid, resp.StatusCode, out, time.Since(start))
		}
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		resp, _ := rendezvous.do("GET", "/api/join/"+token, token, nil)
		if resp.StatusCode == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rendezvous still resolves the ended session's link: %d", resp.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The people on a published session talk across the rendezvous: a viewer
// relayed through it and a viewer at home see each other's messages, and a
// controller there can have a message typed into the session at home.
func TestChatCrossesTheRendezvous(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	id := local.createSession("cat")
	var hosted session.Info
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && hosted.ID == "" {
		rendezvous.srv.Registry().Each(func(d session.Driver) {
			if i := d.Info(); i.Kind == session.KindHosted && i.HostName == "office-server" {
				hosted = i
			}
		})
		time.Sleep(20 * time.Millisecond)
	}
	if hosted.ID == "" {
		t.Fatal("the rendezvous never listed the session")
	}
	far := dialViewer(t, rendezvous, hosted.ID, adminToken)
	far.expectControl(proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	far.send(relay)
	for {
		f, err := far.read()
		if err != nil {
			t.Fatalf("waiting for relay_ok: %v", err)
		}
		if f.Type == proto.TypeSignal {
			var m map[string]any
			json.Unmarshal(f.Payload, &m)
			if m["t"] == proto.SigRelayOK {
				break
			}
		}
	}
	far.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Priya"}))
	if w := far.expectControl(proto.CtlWelcome); w["chat"] != true || w["transport"] != "relay" {
		t.Fatalf("relay welcome %v", w)
	}
	far.expectControl(proto.CtlReady)
	near := dialViewer(t, local, id, adminToken)
	near.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Nate"}))
	near.expectControl(proto.CtlReady)

	near.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "from the office"}))
	if m := far.expectChat(proto.ChatKindMessage); m["text"] != "from the office" || m["by"].(map[string]any)["name"] != "Nate" {
		t.Fatalf("far got %v", m)
	}
	if own := near.expectChat(proto.ChatKindMessage); own["text"] != "from the office" {
		t.Fatalf("near's own copy %v", own)
	}
	far.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Text: "from afar", Nonce: "f1"}))
	m := near.expectChat(proto.ChatKindMessage)
	if m["text"] != "from afar" || m["by"].(map[string]any)["name"] != "Priya" || m["nonce"] != "f1" {
		t.Fatalf("near got %v", m)
	}
	// Typed into the session at home from the rendezvous (its admin holds control there), and marked for everyone.
	far.send(proto.MustControl(proto.ChatSend{T: proto.CtlChatSend, Ref: m["id"].(string)}))
	near.expectOutput("from afar")
	if marker := near.expectChat(proto.ChatKindSentToAgent); marker["ref"] != m["id"] {
		t.Fatalf("marker at home %v", marker)
	}
	if marker := far.expectChat(proto.ChatKindSentToAgent); marker["ref"] != m["id"] {
		t.Fatalf("marker afar %v", marker)
	}
}

// A publication the switchyard refuses for now (its 429: here its limit of
// one live open session per address) is tried again, and once the slot is
// free the session is shared after all. Meanwhile the session's activity
// says so as a status line, never an "error" (round 14: a refused crew
// member wore a red error badge for something it had not done), and a link
// asked for says the switchyard's own words.
func TestAPublicationRefusedForNowIsTriedAgain(t *testing.T) {
	was, waited := publishBackoff, publicationWait
	publishBackoff = slices.Repeat([]time.Duration{100 * time.Millisecond}, 60)
	publicationWait = 300 * time.Millisecond
	t.Cleanup(func() { publishBackoff, publicationWait = was, waited })
	rendezvous := newTestEnv(t, func(c *config.Config) {
		c.PublicURL = "https://rendezvous.example.net"
		c.Switchyard.Enabled = true
		c.Switchyard.OpenHosts = true
		c.Switchyard.OpenHostSessions = 1
		c.Switchyard.OpenHostRegistrationsPerMinute = 600
	})
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, HostName: "home", RelayOnly: true}})
	first := local.createSession("cat")
	deadline := time.Now().Add(10 * time.Second)
	for local.srv.publishedOf(first) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if local.srv.publishedOf(first) == nil {
		t.Fatal("the first session was never published")
	}
	second := local.createSession("cat")
	d, _ := local.srv.Registry().Get(second)
	activity := func() []session.ActivityEntry { return d.(*session.Local).Activity() }
	waitFor := func(what string, ok func([]session.ActivityEntry) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok(activity()) {
			if time.Now().After(deadline) {
				t.Fatalf("no %s in %+v", what, activity())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("note of the refusal", func(es []session.ActivityEntry) bool {
		for _, e := range es {
			if e.Type == session.ActivityStatus && strings.Contains(e.Message, "not shared through the switchyard yet") && strings.Contains(e.Message, "open_host_limit") {
				return true
			}
		}
		return false
	})
	resp, out := local.do("POST", "/api/sessions/"+second+"/links", adminToken, map[string]any{"role": "view"})
	if rv, _ := out["rendezvous"].(map[string]any); resp.StatusCode != http.StatusCreated || rv == nil || !strings.Contains(fmt.Sprint(rv["error"]), "trying again") {
		t.Fatalf("a link while refused: %d %v", resp.StatusCode, out)
	}
	// The first ends: its slot is free, and the second is shared on a later try.
	local.do("DELETE", "/api/sessions/"+first, adminToken, nil)
	deadline = time.Now().Add(15 * time.Second)
	for local.srv.publishedOf(second) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if local.srv.publishedOf(second) == nil {
		t.Fatalf("never shared after the slot freed: %+v", activity())
	}
	waitFor("note of the share", func(es []session.ActivityEntry) bool {
		for _, e := range es {
			if e.Type == session.ActivityStatus && strings.HasPrefix(e.Message, "shared through the switchyard after") {
				return true
			}
		}
		return false
	})
	for _, e := range activity() {
		if e.Type == session.ActivityError {
			t.Fatalf("an error entry for a refusal: %+v", e)
		}
	}
}

// A stop the switchyard's operator sends ends the publication, not the
// session: the guest there is let go and the switchyard shows the session
// ended, while at home it runs on and its activity says what happened. The
// owner's next Share publishes it again, as a new session there.
func TestAStopFromTheSwitchyardEndsThePublicationNotTheSession(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) {
		c.PublicURL = "https://rendezvous.example.net"
		c.Switchyard.Enabled = true
		c.Switchyard.OpenHosts = true
	})
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, HostName: "office-server", RelayOnly: true}})
	id := local.createSession("cat")
	var hosted session.Info
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && hosted.ID == "" {
		rendezvous.srv.Registry().Each(func(d session.Driver) {
			if i := d.Info(); i.Kind == session.KindHosted && i.HostName == "office-server" {
				hosted = i
			}
		})
		time.Sleep(20 * time.Millisecond)
	}
	if hosted.ID == "" {
		t.Fatal("the rendezvous never listed the session")
	}
	_, lo := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	_, token, _ := strings.Cut(fmt.Sprint(lo["url"]), "/join/")
	if lo["remote"] != true || token == "" {
		t.Fatalf("link %v", lo)
	}
	guest := dialViewer(t, rendezvous, hosted.ID, token)
	guest.expectControl(proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	guest.send(relay)
	guest.hello(80, 24)
	guest.expectControl(proto.CtlReady)

	// The operator stops it there.
	if resp, _ := rendezvous.do("DELETE", "/api/sessions/"+hosted.ID, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop at the switchyard: %d", resp.StatusCode)
	}
	for {
		if _, err := guest.read(); err != nil {
			break
		}
	}
	rd, _ := rendezvous.srv.Registry().Get(hosted.ID)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !rd.Info().Status.Ended() {
		time.Sleep(20 * time.Millisecond)
	}
	if !rd.Info().Status.Ended() {
		t.Fatalf("the switchyard shows %s", rd.Info().Status)
	}

	// At home the session runs on, unpublished, and says so.
	d, _ := local.srv.Registry().Get(id)
	said := false
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !said {
		for _, e := range d.(*session.Local).Activity() {
			said = said || (e.Type == session.ActivityStatus && strings.Contains(e.Message, "no longer shared through the switchyard"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !said {
		t.Fatalf("the session's activity does not say the publication ended: %+v", d.(*session.Local).Activity())
	}
	if st := d.Info().Status; st != session.StatusRunning {
		t.Fatalf("the session at home is %s", st)
	}
	if local.srv.publishedOf(id) != nil {
		t.Fatal("the ended publication is still held")
	}
	if _, out := local.do("GET", "/api/sessions/"+id+"/links", adminToken, nil); strings.Contains(fmt.Sprint(out), "remote:true") {
		t.Fatalf("the ended publication's link is still listed: %v", out)
	}

	// The next Share publishes it again: a new session there.
	resp, again := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated || again["remote"] != true {
		t.Fatalf("share again: %d %v", resp.StatusCode, again)
	}
	pub := local.srv.publishedOf(id)
	if pub == nil || pub.CurrentID() == hosted.ID {
		t.Fatalf("published again as %v", pub)
	}
	if nd, ok := rendezvous.srv.Registry().Get(pub.CurrentID()); !ok || nd.Info().Status != session.StatusRunning {
		t.Fatalf("the new publication there: %v", ok)
	}
}

// flakyPublisher is an uplink that refuses while down is set, as a
// switchyard out of reach does.
type flakyPublisher struct {
	uplinkPublisher
	down atomic.Bool
}

func (p *flakyPublisher) Publish(ctx context.Context, local *session.Local) (PublishedSession, error) {
	if p.down.Load() {
		return nil, errors.New("dial: connection refused")
	}
	return p.uplinkPublisher.Publish(ctx, local)
}

// A session whose publication the switchyard ended stays owed one until one
// is made: a Share while the switchyard is out of reach makes a link here
// and says why, and the next Share, once it is back, publishes the session
// again. Two Shares at once make one publication.
func TestARepublishThatFailedIsTriedAgainAtTheNextShare(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) {
		c.PublicURL = "https://rendezvous.example.net"
		c.Switchyard.Enabled = true
		c.Switchyard.OpenHosts = true
	})
	local := newTestEnv(t, nil)
	pub := &flakyPublisher{uplinkPublisher: uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, HostName: "office-server", RelayOnly: true}}}
	local.srv.SetPublisher(pub)
	id := local.createSession("cat")
	var hosted session.Info
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && hosted.ID == "" {
		rendezvous.srv.Registry().Each(func(d session.Driver) {
			if i := d.Info(); i.Kind == session.KindHosted && i.HostName == "office-server" {
				hosted = i
			}
		})
		time.Sleep(20 * time.Millisecond)
	}
	if hosted.ID == "" {
		t.Fatal("the rendezvous never listed the session")
	}
	if resp, _ := rendezvous.do("DELETE", "/api/sessions/"+hosted.ID, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop at the switchyard: %d", resp.StatusCode)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && local.srv.publishedOf(id) != nil {
		time.Sleep(20 * time.Millisecond)
	}
	if local.srv.publishedOf(id) != nil {
		t.Fatal("the ended publication is still held")
	}
	// The Share dialog is told a link made now publishes the session again.
	if _, out := local.do("GET", "/api/sessions/"+id+"/links", adminToken, nil); out["republish"] != true {
		t.Fatalf("links after the stop: %v", out)
	}

	// Out of reach: a link here, saying why.
	pub.down.Store(true)
	resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	rv, _ := out["rendezvous"].(map[string]any)
	if resp.StatusCode != http.StatusCreated || out["remote"] == true || !strings.Contains(fmt.Sprint(rv["error"]), "connection refused") {
		t.Fatalf("share while out of reach: %d %v", resp.StatusCode, out)
	}

	// Back: the next Shares publish it again, once.
	pub.down.Store(false)
	var wg sync.WaitGroup
	remote := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
			remote <- out["remote"] == true
		}()
	}
	wg.Wait()
	close(remote)
	for r := range remote {
		if !r {
			t.Fatal("a Share once the switchyard was back made a link here")
		}
	}
	running := 0
	rendezvous.srv.Registry().Each(func(d session.Driver) {
		if i := d.Info(); i.Kind == session.KindHosted && i.HostName == "office-server" && i.Status == session.StatusRunning {
			running++
		}
	})
	if running != 1 {
		t.Fatalf("%d publications of the session there", running)
	}
	if _, out := local.do("GET", "/api/sessions/"+id+"/links", adminToken, nil); out["republish"] != nil || out["published"] != true {
		t.Fatalf("links once published again: %v", out)
	}
}

// A run member whose publication the switchyard ended and which then ended
// here is owed nothing: the run's links stop saying a link would publish it
// again, so the Share dialog keeps the run's link.
func TestAMemberThatEndedIsOwedNoPublication(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"), catMember("core", "immediately"))
	waitMembersPublished(t, local, runID, 2)
	run, _ := local.srv.runs.Get(runID)
	lead := run.Members[0].SessionID
	if resp, _ := rendezvous.do("DELETE", "/api/sessions/"+local.srv.publishedOf(lead).CurrentID(), adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop at the rendezvous: %d", resp.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && local.srv.publishedOf(lead) != nil {
		time.Sleep(20 * time.Millisecond)
	}
	if _, out := local.do("GET", "/api/runs/"+runID+"/links", adminToken, nil); out["republish"] != true || out["published"] != true {
		t.Fatalf("the run's links after a member's stop: %v", out)
	}
	if resp, _ := local.do("DELETE", "/api/sessions/"+lead, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop the member: %d", resp.StatusCode)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		_, out := local.do("GET", "/api/runs/"+runID+"/links", adminToken, nil)
		if out["republish"] == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an ended member is still owed a publication: %v", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A run member whose publication the switchyard ended is published again by
// the run's next link: the run's links say so, so the Share dialog makes one
// rather than show the one it made before.
func TestARunMemberStoppedThereIsPublishedAgainByTheNextRunLink(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"), catMember("core", "immediately"))
	waitMembersPublished(t, local, runID, 2)
	resp, first := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated || first["remote"] != true {
		t.Fatalf("run link: %d %v", resp.StatusCode, first)
	}
	firstToken := strings.TrimPrefix(first["url"].(string), "https://rendezvous.example.net/join/")
	run, _ := local.srv.runs.Get(runID)
	lead := run.Members[0].SessionID
	there := local.srv.publishedOf(lead).CurrentID()
	if resp, _ := rendezvous.do("DELETE", "/api/sessions/"+there, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop at the rendezvous: %d", resp.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && local.srv.publishedOf(lead) != nil {
		time.Sleep(20 * time.Millisecond)
	}
	if local.srv.publishedOf(lead) != nil {
		t.Fatal("the member's ended publication is still held")
	}
	if _, out := local.do("GET", "/api/runs/"+runID+"/links", adminToken, nil); out["republish"] != true {
		t.Fatalf("the run's links after a member's stop: %v", out)
	}
	if resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"}); resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("the next run link: %d %v", resp.StatusCode, out)
	}
	waitMembersPublished(t, local, runID, 2)
	again := local.srv.publishedOf(lead).CurrentID()
	if again == there {
		t.Fatalf("published again under the ended id %s", again)
	}
	// The run's first link follows too: it opens the member's new session.
	deadline = time.Now().Add(10 * time.Second)
	for {
		_, got := rendezvous.do("GET", "/api/join/"+firstToken, firstToken, nil)
		r, _ := got["run"].(map[string]any)
		ms, _ := r["members"].([]any)
		if len(ms) > 0 && ms[0].(map[string]any)["sessionId"] == again {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run's first link still names %v", ms)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, out := local.do("GET", "/api/runs/"+runID+"/links", adminToken, nil); out["republish"] != nil {
		t.Fatalf("the run's links once published again: %v", out)
	}
}

// gatedPublication is a publication whose run updates wait for a gate and
// are recorded as the rendezvous would apply them, in the order they land.
type gatedPublication struct {
	id      string
	gate    chan struct{}
	applied *[]string
	mu      *sync.Mutex
	done    chan struct{}
}

func (p *gatedPublication) OnChange(session.Info)                                            {}
func (p *gatedPublication) OnActivity(string, session.ActivityEntry, session.AttentionState) {}
func (p *gatedPublication) OnChat(string, session.ChatMessage)                               {}
func (p *gatedPublication) Stop()                                                            {}
func (p *gatedPublication) Base() string                                                     { return "https://rendezvous.example.net" }
func (p *gatedPublication) ID() string                                                       { return p.id }
func (p *gatedPublication) CurrentID() string                                                { return p.id }
func (p *gatedPublication) HeldLinks() ([]string, bool)                                      { return nil, false }
func (p *gatedPublication) Done() <-chan struct{}                                            { return p.done }
func (p *gatedPublication) StoppedThere() bool                                               { return false }
func (p *gatedPublication) Link(context.Context, string, time.Duration, string) (proto.LinkCreated, error) {
	return proto.LinkCreated{}, errors.New("not here")
}
func (p *gatedPublication) Revoke(context.Context, string) error { return nil }
func (p *gatedPublication) RunLink(context.Context, string, time.Duration, string, proto.RunGroup) (proto.LinkCreated, error) {
	return proto.LinkCreated{}, errors.New("not here")
}
func (p *gatedPublication) RunLinkUpdate(ctx context.Context, g proto.RunGroup) error {
	select {
	case <-p.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	ids := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		ids = append(ids, m.SessionID)
	}
	p.mu.Lock()
	*p.applied = append(*p.applied, strings.Join(ids, ","))
	p.mu.Unlock()
	return nil
}

// A run's syncs take turns: an update that omits a member, still on its way
// through another member's connection when the member is published again,
// lands before the update that names it, never after.
func TestARunsUpdatesLandInTurn(t *testing.T) {
	local := newTestEnv(t, nil)
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"), catMember("core", "immediately"))
	run, _ := local.srv.runs.Get(runID)
	lead, core := run.Members[0].SessionID, run.Members[1].SessionID
	var mu sync.Mutex
	var applied []string
	slow := &gatedPublication{id: "there-core", gate: make(chan struct{}), applied: &applied, mu: &mu, done: make(chan struct{})}
	quick := &gatedPublication{id: "there-lead-again", gate: make(chan struct{}), applied: &applied, mu: &mu, done: make(chan struct{})}
	close(quick.gate)
	local.srv.pubMu.Lock()
	local.srv.published = map[string]PublishedSession{core: slow}
	local.srv.pubMu.Unlock()
	local.srv.recordRemoteRunLink(runID, proto.LinkCreated{LinkID: "l1", Role: "view"}, false)

	// The lead is not published: the update omits it and goes through the core's connection, slowly.
	first := make(chan struct{})
	go func() { defer close(first); local.srv.syncRunLinks(runID) }()
	time.Sleep(100 * time.Millisecond)
	// The lead is published again: the next update names it and goes through its own connection.
	local.srv.pubMu.Lock()
	local.srv.published[lead] = quick
	local.srv.pubMu.Unlock()
	second := make(chan struct{})
	go func() { defer close(second); local.srv.syncRunLinks(runID) }()
	time.Sleep(100 * time.Millisecond)
	close(slow.gate)
	<-first
	<-second
	mu.Lock()
	defer mu.Unlock()
	if len(applied) != 2 || applied[0] != ",there-core" || applied[1] != "there-lead-again,there-core" {
		t.Fatalf("updates landed as %q", applied)
	}
}

// lateUpdates is a member's publication whose first run update is still on
// its way when the server stops waiting for it: the server is told the wait
// ran out, and the update reaches the rendezvous once release is closed,
// its answer then on result. The others go through at once.
type lateUpdates struct {
	PublishedSession
	held    atomic.Bool
	release chan struct{}
	result  chan error
}

func (p *lateUpdates) RunLinkUpdate(ctx context.Context, g proto.RunGroup) error {
	if !p.held.CompareAndSwap(false, true) {
		return p.PublishedSession.RunLinkUpdate(ctx, g)
	}
	go func() {
		<-p.release
		late, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		p.result <- p.PublishedSession.RunLinkUpdate(late, g)
	}()
	return context.DeadlineExceeded
}

// A run update the server stopped waiting for, delivered after the update
// that followed it, changes nothing at the rendezvous: a member that left
// the run in between stays out of reach of the run's link.
func TestALateRunUpdateLeavesAMemberThatLeftOutOfReach(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"), catMember("core", "immediately"))
	waitMembersPublished(t, local, runID, 2)
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("run link: %d %v", resp.StatusCode, out)
	}
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	run, _ := local.srv.runs.Get(runID)
	lead, core := run.Members[0].SessionID, run.Members[1].SessionID
	local.srv.pubMu.Lock()
	corePub := local.srv.published[core]
	late := &lateUpdates{PublishedSession: local.srv.published[lead], release: make(chan struct{}), result: make(chan error, 1)}
	local.srv.published[lead] = late
	// The next sync sends the members whatever was sent last.
	delete(local.srv.runSent, runID)
	local.srv.pubMu.Unlock()
	coreThere := corePub.CurrentID()
	v := dialViewer(t, rendezvous, coreThere, token)
	v.hello(80, 24)
	v.expectControl(proto.CtlWelcome)

	// An update naming both members goes out over the lead's connection,
	// and the wait for it runs out.
	local.srv.syncRunLinks(runID)
	// The core leaves the members as the rendezvous knows them; the update
	// that says so lands.
	local.srv.pubMu.Lock()
	delete(local.srv.published, core)
	local.srv.pubMu.Unlock()
	t.Cleanup(corePub.Stop)
	local.srv.syncRunLinks(runID)
	v.expectClose(proto.CloseForbidden)
	dialViewer(t, rendezvous, coreThere, token).expectClose(proto.CloseUnauthorized)

	// The first update lands now, after the one that followed it.
	close(late.release)
	var err error
	select {
	case err = <-late.result:
	case <-time.After(15 * time.Second):
		t.Fatal("the late update was never answered")
	}
	dialViewer(t, rendezvous, coreThere, token).expectClose(proto.CloseUnauthorized)
	if err == nil || !strings.HasPrefix(err.Error(), "stale: ") {
		t.Fatalf("the late update was answered %v, want stale", err)
	}
}

// runScript is what a scriptedPublisher's publications do with a run's
// members: each group they are sent, by link_run or link_run_update, goes
// on calls with when it was sent, and the updates are answered by answers
// in turn (nil once they run out).
type runScript struct {
	mu      sync.Mutex
	answers []error
	calls   chan sentGroup
}

// sentGroup is a group a publication was sent, and when: as the server
// sent it, not as the test came to read it.
type sentGroup struct {
	proto.RunGroup
	at time.Time
}

func (sc *runScript) answer(err error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.answers = append(sc.answers, err)
}

// next is the next group sent, or fails the test after wait.
func (sc *runScript) next(t *testing.T, wait time.Duration) sentGroup {
	t.Helper()
	select {
	case g := <-sc.calls:
		return g
	case <-time.After(wait):
		t.Fatalf("no group sent within %v", wait)
		return sentGroup{}
	}
}

// scriptedPublisher publishes every session as a scriptedPublication.
type scriptedPublisher struct{ sc *runScript }

func (p scriptedPublisher) Publish(_ context.Context, local *session.Local) (PublishedSession, error) {
	return &scriptedPublication{gatedPublication: gatedPublication{id: "there-" + local.Info().ID, done: make(chan struct{})}, sc: p.sc}, nil
}

func (p scriptedPublisher) Server() string { return "https://rendezvous.example.net" }

type scriptedPublication struct {
	gatedPublication
	sc *runScript
}

func (p *scriptedPublication) RunLink(_ context.Context, role string, _ time.Duration, label string, g proto.RunGroup) (proto.LinkCreated, error) {
	p.sc.calls <- sentGroup{g, time.Now()}
	return proto.LinkCreated{LinkID: fmt.Sprintf("link-%d", g.Revision), Role: role, Label: label, RunID: g.ID, URL: p.Base() + "/join/token"}, nil
}

func (p *scriptedPublication) RunLinkUpdate(_ context.Context, g proto.RunGroup) error {
	p.sc.calls <- sentGroup{g, time.Now()}
	p.sc.mu.Lock()
	defer p.sc.mu.Unlock()
	if len(p.sc.answers) == 0 {
		return nil
	}
	err := p.sc.answers[0]
	p.sc.answers = p.sc.answers[1:]
	return err
}

// A run's members go to the switchyard with a revision that rises with
// every group sent, the link's first and each update's after, from the
// clock on.
func TestARunsMembersGoOutWithRisingRevisions(t *testing.T) {
	local := newTestEnv(t, nil)
	sc := &runScript{calls: make(chan sentGroup, 64)}
	local.srv.SetPublisher(scriptedPublisher{sc})
	start := time.Now().UnixMilli()
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"), catMember("core", "immediately"))
	waitMembersPublished(t, local, runID, 2)
	if resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"}); resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("run link: %d %v", resp.StatusCode, out)
	}
	minted := sc.next(t, 5*time.Second)
	if minted.Revision < start || len(minted.Members) != 2 {
		t.Fatalf("the link's members: %+v, revision from %d", minted, start)
	}
	last := minted.Revision
	for range 3 {
		local.srv.pubMu.Lock()
		delete(local.srv.runSent, runID)
		local.srv.pubMu.Unlock()
		local.srv.syncRunLinks(runID)
		g := sc.next(t, 5*time.Second)
		if g.Revision <= last {
			t.Fatalf("revision %d after %d", g.Revision, last)
		}
		last = g.Revision
	}
}

// A sync of a run's members that failed is made again with the next
// revision, each try twice as long after the one before: one that got no
// answer, and one the switchyard refused but for not_found (invalid_request
// among them, which a member not registered there yet answers, as after a
// switchyard restart while the members' connections come back one by one).
// A sync that lands ends the tries; not_found ends the run's records
// instead. A try waiting long never holds back the sync of a change.
func TestARunSyncThatFailedIsMadeAgain(t *testing.T) {
	const first = 100 * time.Millisecond
	local := newTestEnv(t, nil)
	local.srv.pubMu.Lock()
	local.srv.runSyncRetry = first
	local.srv.pubMu.Unlock()
	sc := &runScript{calls: make(chan sentGroup, 64)}
	local.srv.SetPublisher(scriptedPublisher{sc})
	runID := local.launchCrew(t, "Solo", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	local.waitRunning(t, runID, "lead")
	local.srv.recordRemoteRunLink(runID, proto.LinkCreated{LinkID: "l1", Role: "view"}, false)
	resend := func() {
		local.srv.pubMu.Lock()
		delete(local.srv.runSent, runID)
		local.srv.pubMu.Unlock()
	}
	quiet := func(what string, d time.Duration) {
		t.Helper()
		select {
		case g := <-sc.calls:
			t.Fatalf("%s: sent %+v", what, g)
		case <-time.After(d):
		}
	}

	failures := []error{
		context.DeadlineExceeded,
		errors.New("invalid_request: session x is not this host's"),
		errors.New("stale: the run's links hold members the host sent after these"),
		errors.New("rate_limited: too many run updates; wait a minute"),
	}
	for _, err := range failures {
		sc.answer(err)
	}
	resend()
	local.srv.syncRunLinks(runID)
	// Each try's time is when the server made it: the test may come to read
	// one late and the next at once.
	var last sentGroup
	for i := range len(failures) + 1 {
		g := sc.next(t, 10*time.Second)
		if i > 0 {
			if g.Revision <= last.Revision {
				t.Fatalf("try %d: revision %d after %d", i, g.Revision, last.Revision)
			}
			if wait := first << (i - 1); g.at.Sub(last.at) < wait {
				t.Fatalf("try %d came %v after the one before, before its wait of %v", i, g.at.Sub(last.at), wait)
			}
		}
		last = g
	}
	quiet("after the sync that landed", time.Second)
	local.srv.pubMu.Lock()
	fails := local.srv.runSyncFails[runID]
	local.srv.pubMu.Unlock()
	if fails != 0 {
		t.Fatalf("failures counted after the sync that landed: %d", fails)
	}

	// A try waiting long (the most a try waits) and a change's sync: the
	// change's goes after runSyncDelay.
	local.srv.pubMu.Lock()
	local.srv.runSyncFails[runID] = 20
	local.srv.pubMu.Unlock()
	local.srv.scheduleRunSyncAfter(runID, true)
	resend()
	local.srv.scheduleRunSync(runID)
	sc.next(t, runSyncDelay+3*time.Second)

	// not_found: the switchyard holds no link of the run; its records go, and nothing is tried again.
	sc.answer(errors.New("not_found: no links for this run"))
	resend()
	local.srv.syncRunLinks(runID)
	sc.next(t, 5*time.Second)
	if rl := local.srv.remoteRunLinksOf(runID); len(rl) != 0 {
		t.Fatalf("records after not_found: %v", rl)
	}
	quiet("after not_found", time.Second)
}

// A run the engine forgets while a member is still published takes its
// links at the rendezvous with it: the revoke goes over the member's
// publication, and the rendezvous refuses the join at once, not at the
// link's expiry or its sweep of a gone host's links (#148).
func TestAForgottenRunRevokesItsLinksAtTheRendezvous(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	runID := local.launchCrew(t, "Solo", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view", "label": "for the team"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("run link: %d %v", resp.StatusCode, out)
	}
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	if resp, out := rendezvous.do("GET", "/api/join/"+token, token, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the rendezvous does not resolve the link: %d %v", resp.StatusCode, out)
	}
	local.srv.runs.ForgetForTest(runID)
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, out := rendezvous.do("GET", "/api/join/"+token, token, nil)
		if resp.StatusCode == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rendezvous still resolves the forgotten run's link: %d %v", resp.StatusCode, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if links := local.srv.remoteRunLinksOf(runID); len(links) != 0 {
		t.Fatalf("records kept of the forgotten run's links: %v", links)
	}
}

// heldMintPublisher publishes every session as a publication whose run link
// mints wait for a gate, and which records the links it is asked to revoke.
type heldMintPublisher struct{ h *heldMint }

type heldMint struct {
	minting chan struct{} // closed as the first mint begins
	gate    chan struct{} // the mint returns once closed
	once    sync.Once
	mu      sync.Mutex
	revoked []string
}

func (p heldMintPublisher) Publish(_ context.Context, local *session.Local) (PublishedSession, error) {
	return &heldMintPublication{gatedPublication: gatedPublication{id: "there-" + local.Info().ID, done: make(chan struct{})}, h: p.h}, nil
}

func (p heldMintPublisher) Server() string { return "https://rendezvous.example.net" }

type heldMintPublication struct {
	gatedPublication
	h *heldMint
}

func (p *heldMintPublication) RunLink(ctx context.Context, role string, _ time.Duration, label string, g proto.RunGroup) (proto.LinkCreated, error) {
	p.h.once.Do(func() { close(p.h.minting) })
	select {
	case <-p.h.gate:
	case <-ctx.Done():
		return proto.LinkCreated{}, ctx.Err()
	}
	return proto.LinkCreated{LinkID: "late-link", Role: role, Label: label, RunID: g.ID, URL: p.Base() + "/join/token"}, nil
}

func (p *heldMintPublication) Revoke(_ context.Context, linkID string) error {
	p.h.mu.Lock()
	defer p.h.mu.Unlock()
	p.h.revoked = append(p.h.revoked, linkID)
	return nil
}

// A run link minted at the rendezvous while the engine forgets the run (the
// forget between the run's check and the mint's answer) is not left there
// unmanaged: it is recorded only while the engine keeps the run, else
// revoked there at once, and the request is answered as a forgotten run's
// (Codex's review of #148's change).
func TestALinkMintedAsTheRunIsForgottenIsRevokedAtOnce(t *testing.T) {
	local := newTestEnv(t, nil)
	h := &heldMint{minting: make(chan struct{}), gate: make(chan struct{})}
	local.srv.SetPublisher(heldMintPublisher{h})
	runID := local.launchCrew(t, "Solo", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	type reply struct {
		status int
		out    map[string]any
	}
	answered := make(chan reply, 1)
	go func() {
		resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"})
		answered <- reply{resp.StatusCode, out}
	}()
	select {
	case <-h.minting:
	case <-time.After(10 * time.Second):
		t.Fatal("the mint never began")
	}
	local.srv.runs.ForgetForTest(runID)
	close(h.gate)
	var got reply
	select {
	case got = <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("the request was never answered")
	}
	if got.status != http.StatusNotFound || !strings.Contains(fmt.Sprint(got.out), "not_found") {
		t.Fatalf("the link request after the run was forgotten: %d %v", got.status, got.out)
	}
	h.mu.Lock()
	revoked := append([]string(nil), h.revoked...)
	h.mu.Unlock()
	if fmt.Sprint(revoked) != "[late-link]" {
		t.Fatalf("revoked at the rendezvous: %v", revoked)
	}
	if links := local.srv.remoteRunLinksOf(runID); len(links) != 0 {
		t.Fatalf("records kept of the forgotten run's links: %v", links)
	}
}

// A forgotten run's revokes go over a member's publication that is still
// up: one whose session ended (its publication over once it lingered) stays
// in the published map until the session leaves the server and would carry
// nothing, so it is passed over for the next member's (Codex's review of
// #148's change). Member one ends first; the revoke goes over member two's.
func TestAForgottenRunRevokesItsLinksOverALivePublication(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"), catMember("core", "immediately"))
	waitMembersPublished(t, local, runID, 2)
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view", "label": "for the team"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("run link: %d %v", resp.StatusCode, out)
	}
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	// The first member's session ends; its publication ends once it has
	// lingered, and stays in the map as a dead carrier.
	run, _ := local.srv.runs.Get(runID)
	first := run.Members[0].SessionID
	d, ok := local.srv.registry.Get(first)
	if !ok {
		t.Fatal("the first member's session is not on the server")
	}
	if err := d.(*session.Local).Stop(t.Context()); err != nil {
		t.Fatalf("stop the first member: %v", err)
	}
	pub := local.srv.publishedOf(first)
	if pub == nil {
		t.Fatal("the first member is not published")
	}
	select {
	case <-pub.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the first member's publication never ended")
	}
	if local.srv.publishedOf(first) != pub {
		t.Fatal("the ended publication left the map: the case is another")
	}
	local.srv.runs.ForgetForTest(runID)
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, out := rendezvous.do("GET", "/api/join/"+token, token, nil)
		if resp.StatusCode == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rendezvous still resolves the forgotten run's link: %d %v", resp.StatusCode, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A run the engine evicts has no member running, so its members'
// publications have all ended by then: its revokes go over another
// publication of this server's, since the switchyard holds the run's links
// under the publisher, whichever session minted them (Codex's review of
// #148's change). The member's session ends and its publication with it;
// an unrelated session stays published; the forgotten run's link is
// refused at the rendezvous.
func TestAnEvictedRunsLinksAreRevokedOverAnotherSessionsPublication(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	runID := local.launchCrew(t, "Solo", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view", "label": "for the team"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("run link: %d %v", resp.StatusCode, out)
	}
	token := strings.TrimPrefix(out["url"].(string), "https://rendezvous.example.net/join/")
	other := local.createSession("cat")
	deadline := time.Now().Add(10 * time.Second)
	for local.srv.publishedOf(other) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the unrelated session is not published")
		}
		time.Sleep(20 * time.Millisecond)
	}
	run, _ := local.srv.runs.Get(runID)
	member := run.Members[0].SessionID
	d, ok := local.srv.registry.Get(member)
	if !ok {
		t.Fatal("the member's session is not on the server")
	}
	if err := d.(*session.Local).Stop(t.Context()); err != nil {
		t.Fatalf("stop the member: %v", err)
	}
	pub := local.srv.publishedOf(member)
	if pub == nil {
		t.Fatal("the member is not published")
	}
	select {
	case <-pub.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the member's publication never ended")
	}
	local.srv.runs.ForgetForTest(runID)
	deadline = time.Now().Add(10 * time.Second)
	for {
		resp, out := rendezvous.do("GET", "/api/join/"+token, token, nil)
		if resp.StatusCode == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rendezvous still resolves the evicted run's link: %d %v", resp.StatusCode, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// scriptedRevokes is a publication whose revokes answer as scripted, in
// order, and are recorded.
type scriptedRevokes struct {
	gatedPublication
	answers []error
	mu      *sync.Mutex
	asked   *[]string
}

func (p *scriptedRevokes) Revoke(_ context.Context, linkID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.asked = append(*p.asked, p.id+":"+linkID)
	if len(p.answers) == 0 {
		return nil
	}
	err := p.answers[0]
	p.answers = p.answers[1:]
	return err
}

// A forgotten run's revoke goes on past a carrier that holds no such link
// (a publication of another publisher, which holds none of the link's
// owner's) to the next, and stops at the first that revokes it; a link
// every carrier answers not_found for is gone, and any other answer from
// every carrier is what is logged (Codex's review of #148's change).
func TestARevokeGoesOnPastACarrierThatHoldsNoSuchLink(t *testing.T) {
	e := newTestEnv(t, nil)
	var mu sync.Mutex
	var asked []string
	carrier := func(id string, answers ...error) *scriptedRevokes {
		return &scriptedRevokes{gatedPublication: gatedPublication{id: id, done: make(chan struct{})}, answers: answers, mu: &mu, asked: &asked}
	}
	notFound := errors.New("not_found: no such link")
	e.srv.revokeRemoteRunLinks("run", []runCarrier{carrier("other", notFound), carrier("owner"), carrier("spare")}, []remoteLink{{ID: "l1"}})
	mu.Lock()
	got := fmt.Sprint(asked)
	asked = nil
	mu.Unlock()
	if got != "[other:l1 owner:l1]" {
		t.Fatalf("asked %s, want the other publisher's carrier passed and the owner's revoke to end it", got)
	}
	// Every carrier holding none: the link is gone, and no carrier is asked twice.
	e.srv.revokeRemoteRunLinks("run", []runCarrier{carrier("a", notFound, notFound), carrier("b", notFound, notFound)}, []remoteLink{{ID: "l2"}, {ID: "l3"}})
	mu.Lock()
	got = fmt.Sprint(asked)
	mu.Unlock()
	if got != "[a:l2 b:l2 a:l3 b:l3]" {
		t.Fatalf("asked %s", got)
	}
}

// The carriers of a forgotten run's requests are its members' publications
// still up, first, then every other publication of this server's still up;
// one that ended, a member's or not, is left out. So a member's publication
// ending as the revokes go (its linger running out) leaves the rest of them
// to the next carrier (Codex's review of #148's change).
func TestLiveCarriersListTheMembersFirstThenTheRest(t *testing.T) {
	e := newTestEnv(t, nil)
	pub := func(id string, live bool) *gatedPublication {
		p := &gatedPublication{id: id, done: make(chan struct{})}
		if !live {
			close(p.done)
		}
		return p
	}
	e.srv.pubMu.Lock()
	e.srv.published = map[string]PublishedSession{
		"m1": pub("m1", false), "m2": pub("m2", true),
		"other": pub("other", true), "gone": pub("gone", false),
	}
	e.srv.pubMu.Unlock()
	run := crew.Run{ID: "run", Members: []crew.MemberState{{Name: "a", SessionID: "m1"}, {Name: "b", SessionID: "m2"}, {Name: "c"}}}
	var ids []string
	for _, c := range e.srv.liveCarriers(run) {
		ids = append(ids, c.ID())
	}
	if fmt.Sprint(ids) != "[m2 other]" {
		t.Fatalf("carriers %v, want the live member's publication then the unrelated live one", ids)
	}
}
