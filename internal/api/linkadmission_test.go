package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// tokenedLinks registers a host with the host token under an instance on
// e, has it mint n links, and sees it leave: the host as it registered,
// its session's secret, and the links' ids and tokens.
func tokenedLinks(t *testing.T, e *testEnv, n int) (hi proto.HostInfo, hs proto.HostSession, sessionID, secret string, ids, tokens []string) {
	t.Helper()
	hi, hs = instanceHost(e, "instance-secret-0123456789", "1")
	host, reg := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
	for i := range n {
		id, token := mintHostLink(t, host, "t"+string(rune('a'+i)))
		ids, tokens = append(ids, id), append(tokens, token)
	}
	secret, _ = reg["secret"].(string)
	leave(t, e, host)
	return hi, hs, host.sessionID, secret, ids, tokens
}

// heldToTheOpenBound checks that host, an open host whose instance holds
// the links ids minted with the host token, is held to what an open host
// may keep: they still open their session, it mints none while they fill
// its allowance, and, when they are its session's (listed in reg, the
// switchyard's registered), one again once its revoke of one leaves room.
func heldToTheOpenBound(t *testing.T, e *testEnv, host *fakeHost, reg map[string]any, ids, tokens []string) {
	t.Helper()
	links, _ := reg["links"].([]any)
	own := len(links) > 0
	for _, id := range ids {
		if own && !slices.Contains(links, any(id)) {
			t.Fatalf("registered lists %v, not the link %s", links, id)
		}
	}
	for _, token := range tokens {
		if code, out := joinStatus(t, e, token); code != http.StatusOK {
			t.Fatalf("a link made with the host token, its host back open: %d %v", code, out)
		}
	}
	host.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
	if m := linkAnswer(t, host, "o1"); m["code"] != "link_refused" {
		t.Fatalf("an open host whose links made with a token fill its allowance minted one: %v", m)
	}
	if !own {
		return
	}
	host.send(proto.HostLinkRevokeMsg{T: proto.HostLinkRevoke, RequestID: "rv", LinkID: ids[0]})
	host.expect(proto.HostLinkRevoked)
	host.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o2", Role: "view"})
	if m := linkAnswer(t, host, "o2"); m["t"] != proto.HostLinkCreated {
		t.Fatalf("with room again: %v", m)
	}
	host.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o3", Role: "view"})
	if m := linkAnswer(t, host, "o3"); m["code"] != "link_refused" {
		t.Fatalf("past the allowance again: %v", m)
	}
}

// A host that minted links with the host token and comes back with none is
// an open host: the links it made then count toward what it may keep now,
// whether it resumes its session by its secret, takes it back by its
// instance, or registers again after the switchyard restarted. The links
// keep working; the open host mints none until it is under the bound.
func TestAnOpenHostCountsTheLinksItMadeWithAToken(t *testing.T) {
	bound := func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 }
	t.Run("resumed by its secret", func(t *testing.T) {
		e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), bound)
		_, hs, sessionID, secret, ids, tokens := tokenedLinks(t, e, 2)
		hs.LocalID = ""
		open, reg := dialFakeHostRegister(t, e, proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"}, Session: hs,
			Resume: &proto.HostResume{SessionID: sessionID, Secret: secret}}, "", nil)
		if reg["resumed"] != true || open.sessionID != sessionID {
			t.Fatalf("the resume: %v", reg)
		}
		heldToTheOpenBound(t, e, open, reg, ids, tokens)
	})
	t.Run("taken back by its instance", func(t *testing.T) {
		e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), bound)
		hi, hs, sessionID, _, ids, tokens := tokenedLinks(t, e, 2)
		open, reg := dialFakeHostWith(t, e, hi, hs, "")
		if reg["resumed"] != true || open.sessionID != sessionID {
			t.Fatalf("the take-back: %v", reg)
		}
		heldToTheOpenBound(t, e, open, reg, ids, tokens)
	})
	t.Run("another session of its instance", func(t *testing.T) {
		e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), bound)
		hi, hs, _, _, ids, tokens := tokenedLinks(t, e, 2)
		hs.LocalID = "2"
		open, reg := dialFakeHostWith(t, e, hi, hs, "")
		// A new session has no link of its own; its instance holds them.
		if links, _ := reg["links"].([]any); len(links) != 0 {
			t.Fatalf("a new session is told of links: %v", links)
		}
		heldToTheOpenBound(t, e, open, reg, ids, tokens)
	})
	t.Run("after a restart", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		a := keptSwitchyard(t, dir, bound)
		hi, hs, sessionID, _, ids, tokens := tokenedLinks(t, a, 2)
		b := keptSwitchyard(t, dir, bound)
		open, reg := dialFakeHostWith(t, b, hi, hs, "")
		if open.sessionID != sessionID {
			t.Fatalf("after the restart: %s (was %s)", open.sessionID, sessionID)
		}
		heldToTheOpenBound(t, b, open, reg, ids, tokens)
	})
}

// A host with the host token is not held to the open hosts' bound, however
// many links it made before.
func TestATokenedHostMintsPastTheOpenBound(t *testing.T) {
	e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), func(c *config.Config) { c.Switchyard.OpenHostLinks = 1 })
	hi, hs, _, _, _, _ := tokenedLinks(t, e, 2)
	back, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
	mintHostLink(t, back, "t3")
}

// keepHold holds the first link request keepLink begins for an open host
// (open) or for a host with the host token (!open) at its start, before it
// is counted, until released.
type keepHold struct {
	t       *testing.T
	reached chan struct{}
	gate    chan struct{}
}

func holdKeeping(t *testing.T, e *testEnv, open bool) *keepHold {
	t.Helper()
	h := &keepHold{t: t, reached: make(chan struct{}), gate: make(chan struct{})}
	var once sync.Once
	f := func(addr string) {
		if (addr != "") == open {
			once.Do(func() {
				close(h.reached)
				<-h.gate
			})
		}
	}
	e.srv.keeper.keeping.Store(&f)
	t.Cleanup(func() {
		e.srv.keeper.keeping.Store(nil)
		select {
		case <-h.gate:
		default:
			close(h.gate)
		}
	})
	return h
}

// held waits until the request is held; the bound only bounds a hang.
func (h *keepHold) held() {
	h.t.Helper()
	select {
	case <-h.reached:
	case <-time.After(10 * time.Second):
		h.t.Fatal("no link request reached the keeper")
	}
}

// release lets the held request go on.
func (h *keepHold) release() { close(h.gate) }

// refusedRegistration sends reg (with the Authorization auth, "" for none)
// and returns the code the switchyard closes the connection with.
func refusedRegistration(t *testing.T, e *testEnv, reg proto.Register, auth string) websocket.StatusCode {
	t.Helper()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	header := http.Header{}
	if auth != "" {
		header.Set("Authorization", auth)
	}
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	defer c.CloseNow()
	b, _ := json.Marshal(reg)
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("register: %v", err)
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
		t.Fatalf("a registration meant to be refused was answered: %s", data)
	}
}

// A link the host asked for with the host token as its connection went is
// counted toward what the host may keep when it comes back open, however it
// comes back: the request ends before another connection takes the session
// (one trying meanwhile is refused, the session still its host's), and the
// link counts from then on, after a restart too.
func TestALinkMadeAsItsTokenedHostLeavesIsCounted(t *testing.T) {
	bound := func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 }
	for _, back := range []string{"resumed by its secret", "taken back by its instance", "after a restart"} {
		t.Run(back, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			e := keptSwitchyard(t, dir, bound)
			hi, hs := instanceHost(e, "instance-secret-0123456789", "1")
			host, reg := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
			first, _ := mintHostLink(t, host, "t1")
			d, _ := e.srv.hosts.Get(host.sessionID)
			hold := holdKeeping(t, e, false)
			host.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "t2", Role: "view"})
			hold.held()
			host.c.CloseNow()
			<-host.done
			bySecret := hs
			bySecret.LocalID = ""
			resume := proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"}, Session: bySecret,
				Resume: &proto.HostResume{SessionID: host.sessionID, Secret: reg["secret"].(string)}}
			if code := refusedRegistration(t, e, resume, ""); code != proto.CloseNotFound {
				t.Fatalf("a resume while the host's request is under way: %v", code)
			}
			if code := refusedRegistration(t, e, proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: hi, Session: hs}, ""); code != proto.CloseProtocolError {
				t.Fatalf("a take-back while the host's request is under way: %v", code)
			}
			hold.release()
			deadline := time.Now().Add(10 * time.Second)
			for d.Connected() {
				if time.Now().After(deadline) {
					t.Fatal("the switchyard never saw the host go")
				}
				time.Sleep(10 * time.Millisecond)
			}
			var (
				open *fakeHost
				oreg map[string]any
			)
			switch back {
			case "resumed by its secret":
				open, oreg = dialFakeHostRegister(t, e, resume, "", nil)
			case "taken back by its instance":
				open, oreg = dialFakeHostWith(t, e, hi, hs, "")
			default:
				open, oreg = dialFakeHostWith(t, keptSwitchyard(t, dir, bound), hi, hs, "")
			}
			if links, _ := oreg["links"].([]any); open.sessionID != host.sessionID || len(links) != 2 || !slices.Contains(links, any(first)) {
				t.Fatalf("back as %s (was %s), links %v", open.sessionID, host.sessionID, oreg["links"])
			}
			open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
			if m := linkAnswer(t, open, "o1"); m["code"] != "link_refused" {
				t.Fatalf("the open host past its allowance: %v", m)
			}
		})
	}
}

// Two sessions of one host instance, one with the host token and one open,
// asking for links at once: whichever the switchyard decides first, the
// open one is held to the bound counting the other's links, and the one
// with the token is not held to it.
func TestOneHostsTokenedAndOpenRequestsAreCountedInEitherOrder(t *testing.T) {
	setup := func(t *testing.T) (*testEnv, *fakeHost, *fakeHost) {
		e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 })
		hi, hs := instanceHost(e, "instance-secret-0123456789", "1")
		tokened, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		mintHostLink(t, tokened, "t1")
		hs.LocalID = "2"
		open, _ := dialFakeHostWith(t, e, hi, hs, "")
		return e, tokened, open
	}
	t.Run("the open request held", func(t *testing.T) {
		e, tokened, open := setup(t)
		hold := holdKeeping(t, e, true)
		open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
		hold.held()
		mintHostLink(t, tokened, "t2")
		hold.release()
		if m := linkAnswer(t, open, "o1"); m["code"] != "link_refused" {
			t.Fatalf("decided after the tokened link: %v", m)
		}
	})
	t.Run("the tokened request held", func(t *testing.T) {
		e, tokened, open := setup(t)
		hold := holdKeeping(t, e, false)
		tokened.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "t2", Role: "view"})
		hold.held()
		open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
		if m := linkAnswer(t, open, "o1"); m["t"] != proto.HostLinkCreated {
			t.Fatalf("decided before the tokened link, within the bound: %v", m)
		}
		hold.release()
		if m := linkAnswer(t, tokened, "t2"); m["t"] != proto.HostLinkCreated {
			t.Fatalf("the tokened link past the open bound: %v", m)
		}
		open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o2", Role: "view"})
		if m := linkAnswer(t, open, "o2"); m["code"] != "link_refused" {
			t.Fatalf("after the tokened link: %v", m)
		}
	})
}
