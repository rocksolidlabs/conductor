package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// tokenedLinks registers a host with the host token under an instance on
// e, has it mint n links, oldest first, and sees it leave: the host as it
// registered, its session's secret, and the links' ids and tokens.
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

// linkEnded checks that the link id (its token) was ended: it opens
// nothing, and its file in dir is gone.
func linkEnded(t *testing.T, e *testEnv, dir, id, token string) {
	t.Helper()
	if code, out := joinStatus(t, e, token); code != http.StatusNotFound {
		t.Fatalf("an ended link %s: %d %v", id, code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "links", id+".json")); !os.IsNotExist(err) {
		t.Fatalf("the file of the ended link %s: %v", id, err)
	}
}

// linkKept checks that the link id (its token) is still kept: it is known
// (open, or its host away), and its file in dir is there.
func linkKept(t *testing.T, e *testEnv, dir, id, token string) {
	t.Helper()
	if code, out := joinStatus(t, e, token); code == http.StatusNotFound {
		t.Fatalf("a kept link %s: %d %v", id, code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "links", id+".json")); err != nil {
		t.Fatalf("the file of the kept link %s: %v", id, err)
	}
}

// heldToTheOpenBound checks host, an open host whose instance held the
// links ids (oldest first) made with the host token, at a bound of two:
// the oldest was ended as the host was admitted, the two newest are kept
// (listed in reg when they are its session's), it mints none while they
// fill its allowance, and, when they are its session's, one again once its
// revoke of one leaves room.
func heldToTheOpenBound(t *testing.T, e *testEnv, dir string, host *fakeHost, reg map[string]any, ids, tokens []string) {
	t.Helper()
	links, _ := reg["links"].([]any)
	own := len(links) > 0
	linkEnded(t, e, dir, ids[0], tokens[0])
	if slices.Contains(links, any(ids[0])) {
		t.Fatalf("registered lists the ended link: %v", links)
	}
	for i := 1; i < len(ids); i++ {
		linkKept(t, e, dir, ids[i], tokens[i])
		if own && !slices.Contains(links, any(ids[i])) {
			t.Fatalf("registered lists %v, not the kept link %s", links, ids[i])
		}
	}
	host.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
	if m := linkAnswer(t, host, "o1"); m["code"] != "link_refused" {
		t.Fatalf("an open host whose kept links fill its allowance minted one: %v", m)
	}
	if !own {
		return
	}
	host.send(proto.HostLinkRevokeMsg{T: proto.HostLinkRevoke, RequestID: "rv", LinkID: ids[1]})
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

// A host that made links with the host token and is admitted with none is
// an open host: as it is admitted, the links its instance made then count
// against its address, and those past the bound end, the oldest first,
// whether it resumes its session by its secret (naming its own instance or
// another), takes it back by its instance, registers another session of
// the instance, or registers again after the switchyard restarted.
func TestAnOpenAdmissionEndsTheTokenedLinksPastTheBound(t *testing.T) {
	bound := func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 }
	resume := func(hi proto.HostInfo, hs proto.HostSession, sessionID, secret string) proto.Register {
		hs.LocalID = ""
		return proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: hi, Session: hs,
			Resume: &proto.HostResume{SessionID: sessionID, Secret: secret}}
	}
	for _, back := range []string{"resumed by its secret", "resumed naming another instance", "taken back by its instance", "another session of its instance", "after a restart"} {
		t.Run(back, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			e := keptSwitchyard(t, dir, bound)
			hi, hs, sessionID, secret, ids, tokens := tokenedLinks(t, e, 3)
			var (
				open *fakeHost
				reg  map[string]any
			)
			switch back {
			case "resumed by its secret":
				open, reg = dialFakeHostRegister(t, e, resume(proto.HostInfo{Name: "laptop"}, hs, sessionID, secret), "", nil)
			case "resumed naming another instance":
				// The session's own instance counts, not the one it names now.
				r := resume(proto.HostInfo{Name: "laptop", Instance: "another-instance-0123456789"}, hs, sessionID, secret)
				r.Session.LocalID = "1"
				open, reg = dialFakeHostRegister(t, e, r, "", nil)
			case "taken back by its instance":
				open, reg = dialFakeHostWith(t, e, hi, hs, "")
			case "another session of its instance":
				hs.LocalID = "2"
				open, reg = dialFakeHostWith(t, e, hi, hs, "")
				// A new session has no link of its own; its instance holds them.
				if links, _ := reg["links"].([]any); len(links) != 0 {
					t.Fatalf("a new session is told of links: %v", links)
				}
			default:
				e = keptSwitchyard(t, dir, bound)
				open, reg = dialFakeHostWith(t, e, hi, hs, "")
			}
			if back != "another session of its instance" && open.sessionID != sessionID {
				t.Fatalf("back as %s (was %s): %v", open.sessionID, sessionID, reg)
			}
			heldToTheOpenBound(t, e, dir, open, reg, ids, tokens)
		})
	}
}

// What one open client keeps at an address, however many instances it
// names: three links made with the token, then admitted open, leave two;
// a session of a fresh instance at the address is refused a link, the
// admitted host still connected; and the links counted against the
// address are counted so after a restart, from their files.
func TestAnOpenClientKeepsNoMoreThanTheBound(t *testing.T) {
	bound := func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 }
	dir := filepath.Join(t.TempDir(), "data")
	e := keptSwitchyard(t, dir, bound)
	_, hs, sessionID, secret, ids, tokens := tokenedLinks(t, e, 3)
	bySecret := hs
	bySecret.LocalID = ""
	open, _ := dialFakeHostRegister(t, e, proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"}, Session: bySecret,
		Resume: &proto.HostResume{SessionID: sessionID, Secret: secret}}, "", nil)
	working := 0
	for _, token := range tokens {
		if code, _ := joinStatus(t, e, token); code == http.StatusOK {
			working++
		}
	}
	if working != 2 {
		t.Fatalf("links working for the open client: %d of %v", working, ids)
	}
	_, fresh := instanceHost(e, "", "")
	fresh.LocalID = "9"
	other, _ := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop", Instance: "fresh-instance-0123456789"}, fresh, "")
	other.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "f1", Role: "view"})
	if m := linkAnswer(t, other, "f1"); m["code"] != "link_refused" {
		t.Fatalf("a fresh instance of the open client at the bound: %v", m)
	}
	leave(t, e, open)

	b := keptSwitchyard(t, dir, bound)
	again, _ := dialFakeHostWith(t, b, proto.HostInfo{Name: "laptop", Instance: "fresh-instance-0123456789"}, fresh, "")
	again.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "f2", Role: "view"})
	if m := linkAnswer(t, again, "f2"); m["code"] != "link_refused" {
		t.Fatalf("after a restart, the links counted against the address: %v", m)
	}
}

// The address's other open hosts keep their links when a host of the
// address is admitted open with links made with the token: only the
// admitted host's past the bound end. Its links count against the address
// from then on, as an open host's always do: a request of the address with
// no room then takes a gone host's place, as any does.
func TestAnOpenAdmissionLeavesTheAddresssOtherHostsLinks(t *testing.T) {
	gone := func(t *testing.T, e *testEnv) ([]string, []string) {
		hi, hs := instanceHost(e, "gone-instance-0123456789", "g")
		g, _ := dialFakeHostWith(t, e, hi, hs, "")
		var ids, tokens []string
		for _, r := range []string{"g1", "g2"} {
			id, token := mintHostLink(t, g, r)
			ids, tokens = append(ids, id), append(tokens, token)
		}
		owner := ownerOf(t, e, g.sessionID)
		leave(t, e, g)
		e.srv.hosts.Expire(time.Now().Add(time.Hour))
		awayFor(e, owner, time.Hour)
		return ids, tokens
	}
	t.Run("its links leave room", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 4 })
		gids, gtokens := gone(t, e)
		hi, hs, _, _, ids, tokens := tokenedLinks(t, e, 1)
		open, _ := dialFakeHostWith(t, e, hi, hs, "")
		linkKept(t, e, dir, ids[0], tokens[0])
		open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
		if m := linkAnswer(t, open, "o1"); m["t"] != proto.HostLinkCreated {
			t.Fatalf("with room at the address: %v", m)
		}
		for i := range gids {
			if code, out := joinStatus(t, e, gtokens[i]); code != http.StatusServiceUnavailable {
				t.Fatalf("the gone host's link %s: %d %v", gids[i], code, out)
			}
			linkKept(t, e, dir, gids[i], gtokens[i])
		}
	})
	t.Run("its links fill the bound", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 3 })
		gids, gtokens := gone(t, e)
		hi, hs, _, _, ids, tokens := tokenedLinks(t, e, 2)
		open, reg := dialFakeHostWith(t, e, hi, hs, "")
		linkEnded(t, e, dir, ids[0], tokens[0])
		linkKept(t, e, dir, ids[1], tokens[1])
		if links, _ := reg["links"].([]any); len(links) != 1 || links[0] != ids[1] {
			t.Fatalf("registered lists %v", links)
		}
		for i := range gids {
			if code, out := joinStatus(t, e, gtokens[i]); code != http.StatusServiceUnavailable {
				t.Fatalf("the gone host's link %s, after the admission: %d %v", gids[i], code, out)
			}
			linkKept(t, e, dir, gids[i], gtokens[i])
		}
		// The address is at its bound: a new link of it takes the place of
		// the gone host's oldest, as it would of any link of the address.
		open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
		if m := linkAnswer(t, open, "o1"); m["t"] != proto.HostLinkCreated {
			t.Fatalf("at the bound with a gone host's links: %v", m)
		}
		linkEnded(t, e, dir, gids[0], gtokens[0])
		linkKept(t, e, dir, gids[1], gtokens[1])
	})
}

// Run links count as session links do: a run link made with the token
// counts against the address once its instance is admitted open, the
// oldest first to end, and an open host's run link is held to the bound.
func TestAnOpenAdmissionCountsRunLinks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 })
	hi, hs := instanceHost(e, "instance-secret-0123456789", "1")
	group := func(id string) proto.RunGroup {
		return proto.RunGroup{ID: "pair-0123abcd", Name: "pair", Members: []proto.RunMember{{Name: "m0", SessionID: id, AgentID: "cat", Status: "running"}}}
	}
	runLink := func(h *fakeHost, id string) map[string]any {
		h.send(proto.HostRunLinkMsg{T: proto.HostRunLink, RequestID: id, Role: "view", Run: group(h.sessionID)})
		return linkAnswer(t, h, id)
	}
	tokenOf := func(m map[string]any) string {
		u, _ := m["url"].(string)
		return u[strings.LastIndex(u, "/")+1:]
	}
	tokened, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
	r := runLink(tokened, "r1")
	if r["t"] != proto.HostLinkCreated {
		t.Fatalf("the run link: %v", r)
	}
	a, aToken := mintHostLink(t, tokened, "a")
	b, bToken := mintHostLink(t, tokened, "b")
	leave(t, e, tokened)

	open, reg := dialFakeHostWith(t, e, hi, hs, "")
	linkEnded(t, e, dir, r["linkId"].(string), tokenOf(r))
	linkKept(t, e, dir, a, aToken)
	linkKept(t, e, dir, b, bToken)
	if links, _ := reg["links"].([]any); len(links) != 2 || !slices.Contains(links, any(a)) || !slices.Contains(links, any(b)) {
		t.Fatalf("registered lists %v", links)
	}
	if m := runLink(open, "o1"); m["code"] != "link_refused" {
		t.Fatalf("an open host's run link past the bound: %v", m)
	}
	open.send(proto.HostLinkRevokeMsg{T: proto.HostLinkRevoke, RequestID: "rv", LinkID: a})
	open.expect(proto.HostLinkRevoked)
	if m := runLink(open, "o2"); m["t"] != proto.HostLinkCreated {
		t.Fatalf("an open host's run link with room: %v", m)
	}
	if m := runLink(open, "o3"); m["code"] != "link_refused" {
		t.Fatalf("an open host's run link past the bound again: %v", m)
	}
}

// A host with the host token is not held to the open hosts' bound, however
// many links it made before; and one admitted with the token again after it
// was admitted open keeps what it holds then (the open bound ended the
// rest, and nothing comes back) and mints past the bound as before.
func TestATokenedHostMintsPastTheOpenBound(t *testing.T) {
	t.Run("never admitted open", func(t *testing.T) {
		e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), func(c *config.Config) { c.Switchyard.OpenHostLinks = 1 })
		hi, hs, _, _, _, _ := tokenedLinks(t, e, 2)
		back, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		mintHostLink(t, back, "t3")
	})
	t.Run("its token back", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 })
		hi, hs, _, _, ids, tokens := tokenedLinks(t, e, 3)
		open, _ := dialFakeHostWith(t, e, hi, hs, "")
		leave(t, e, open)
		back, reg := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		linkEnded(t, e, dir, ids[0], tokens[0])
		if links, _ := reg["links"].([]any); len(links) != 2 || !slices.Contains(links, any(ids[1])) || !slices.Contains(links, any(ids[2])) {
			t.Fatalf("registered with the token again lists %v", links)
		}
		mintHostLink(t, back, "t4")
		mintHostLink(t, back, "t5")
	})
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
// a link of a host with the token, and is counted as the host is admitted
// open, however it comes back: the request ends before another connection
// takes the session (one trying meanwhile is refused, the session still its
// host's), and the admission then counts it, the oldest ending, after a
// restart too.
func TestALinkMadeAsItsTokenedHostLeavesIsCounted(t *testing.T) {
	bound := func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 }
	for _, back := range []string{"resumed by its secret", "taken back by its instance", "after a restart"} {
		t.Run(back, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			e := keptSwitchyard(t, dir, bound)
			hi, hs := instanceHost(e, "instance-secret-0123456789", "1")
			host, reg := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
			first, firstToken := mintHostLink(t, host, "t1")
			second, _ := mintHostLink(t, host, "t2")
			d, _ := e.srv.hosts.Get(host.sessionID)
			hold := holdKeeping(t, e, false)
			host.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "t3", Role: "view"})
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
				e = keptSwitchyard(t, dir, bound)
				open, oreg = dialFakeHostWith(t, e, hi, hs, "")
			}
			links, _ := oreg["links"].([]any)
			if open.sessionID != host.sessionID || len(links) != 2 || !slices.Contains(links, any(second)) || slices.Contains(links, any(first)) {
				t.Fatalf("back as %s (was %s), links %v", open.sessionID, host.sessionID, oreg["links"])
			}
			linkEnded(t, e, dir, first, firstToken)
			open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
			if m := linkAnswer(t, open, "o1"); m["code"] != "link_refused" {
				t.Fatalf("the open host past its allowance: %v", m)
			}
		})
	}
}

// Two sessions of one host instance, one with the host token and one open:
// while the instance holds a connection admitted open, a request with the
// token is refused, whichever request the switchyard decides first, and a
// request with the token held across the open session's admission is
// decided as the instance then is, never counted against nothing; the open
// one is counted against its address, the links made with the token before
// the admission among them.
func TestOneHostsTokenedAndOpenRequestsAreCountedInEitherOrder(t *testing.T) {
	tokenedFirst := func(t *testing.T) (*testEnv, *fakeHost, proto.HostInfo, proto.HostSession) {
		e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 })
		hi, hs := instanceHost(e, "instance-secret-0123456789", "1")
		tokened, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		mintHostLink(t, tokened, "t1")
		hs.LocalID = "2"
		return e, tokened, hi, hs
	}
	refusedWhileOpen := func(t *testing.T, m map[string]any) {
		t.Helper()
		if m["code"] != "link_refused" || m["message"] != errOpenHeld.Error() {
			t.Fatalf("a request with the token while the instance holds an open connection: %v", m)
		}
	}
	t.Run("the open request held", func(t *testing.T) {
		e, tokened, hi, hs := tokenedFirst(t)
		open, _ := dialFakeHostWith(t, e, hi, hs, "")
		hold := holdKeeping(t, e, true)
		open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
		hold.held()
		tokened.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "t2", Role: "view"})
		refusedWhileOpen(t, linkAnswer(t, tokened, "t2"))
		hold.release()
		if m := linkAnswer(t, open, "o1"); m["t"] != proto.HostLinkCreated {
			t.Fatalf("the open request, one link of the address's two counted: %v", m)
		}
	})
	t.Run("the tokened request held", func(t *testing.T) {
		e, tokened, hi, hs := tokenedFirst(t)
		open, _ := dialFakeHostWith(t, e, hi, hs, "")
		hold := holdKeeping(t, e, false)
		tokened.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "t2", Role: "view"})
		hold.held()
		open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
		if m := linkAnswer(t, open, "o1"); m["t"] != proto.HostLinkCreated {
			t.Fatalf("decided before the tokened link, within the bound: %v", m)
		}
		hold.release()
		refusedWhileOpen(t, linkAnswer(t, tokened, "t2"))
	})
	for _, room := range []bool{true, false} {
		t.Run(fmt.Sprintf("the tokened request held across the admission, room %v", room), func(t *testing.T) {
			e, tokened, hi, hs := tokenedFirst(t)
			if !room {
				mintHostLink(t, tokened, "t0")
			}
			hold := holdKeeping(t, e, false)
			tokened.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "t2", Role: "view"})
			hold.held()
			open, _ := dialFakeHostWith(t, e, hi, hs, "")
			hold.release()
			refusedWhileOpen(t, linkAnswer(t, tokened, "t2"))
			open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
			m := linkAnswer(t, open, "o1")
			if room && m["t"] != proto.HostLinkCreated || !room && m["code"] != "link_refused" {
				t.Fatalf("the open host, its instance's links made with the token counted: %v", m)
			}
		})
	}
	t.Run("the open connection gone", func(t *testing.T) {
		e, tokened, hi, hs := tokenedFirst(t)
		open, _ := dialFakeHostWith(t, e, hi, hs, "")
		tokened.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "t2", Role: "view"})
		refusedWhileOpen(t, linkAnswer(t, tokened, "t2"))
		leave(t, e, open)
		mintHostLink(t, tokened, "t3")
	})
}

// One host instance's connections, in every order they can come and go:
// one with the host token, one admitted open from an address, another
// admitted open from a second address. Before each step a link request with
// the token is held at the keeper's seam and decided after the step. After
// each step: a request with the token is refused while the instance holds
// a connection admitted open, and counted against no address otherwise;
// every link the instance keeps is counted where its own request or the
// admission that moved it came from, once; the links made with the token
// are counted against an address while an open connection is held; and the
// instance holds no open connection once every one has ended.
func TestEveryOrderOfAnInstancesConnectionsCountsItsLinks(t *testing.T) {
	e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), func(c *config.Config) { c.Switchyard.OpenHostLinks = 1000 })
	k := e.srv.keeper
	const owner = "an-instance-walked"
	addrOf := [3]string{"", "10.0.0.1", "10.0.0.2"}
	type step struct {
		conn int
		up   bool
	}
	var orders [][]step
	var walk func(seq []step, up, down [3]bool)
	walk = func(seq []step, up, down [3]bool) {
		if len(seq) == 6 {
			orders = append(orders, slices.Clone(seq))
			return
		}
		for c := range 3 {
			switch {
			case !up[c]:
				u := up
				u[c] = true
				walk(append(seq, step{c, true}), u, down)
			case !down[c]:
				d := down
				d[c] = true
				walk(append(seq, step{c, false}), up, d)
			}
		}
	}
	walk(nil, [3]bool{}, [3]bool{})
	if len(orders) != 90 {
		t.Fatalf("%d orders", len(orders))
	}
	for n, order := range orders {
		sessionID := fmt.Sprintf("walked-%02d", n)
		// where are the addresses each kept link of the instance is counted
		// against: "" for none (made with the token, no admission since).
		where := map[string]string{}
		var closers [3]func()
		open := 0
		for i, st := range order {
			reached, gate := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f := func(string) { once.Do(func() { close(reached); <-gate }) }
			k.keeping.Store(&f)
			l, token, err := e.srv.links.Create(sessionID, "view", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			decided := make(chan error, 1)
			go func() { decided <- e.srv.keepLink(l, token, owner, "", func() {}) }()
			select {
			case <-reached:
			case <-time.After(10 * time.Second):
				t.Fatal("the request never reached the keeper")
			}
			k.keeping.Store(nil)
			if st.up {
				closers[st.conn] = e.srv.hostUp(owner, addrOf[st.conn])
				if st.conn > 0 {
					open++
					for id, a := range where {
						if a == "" {
							where[id] = addrOf[st.conn]
						}
					}
				}
			} else {
				closers[st.conn]()
				if st.conn > 0 {
					open--
				}
			}
			close(gate)
			err = <-decided
			row := fmt.Sprintf("order %v, step %d", order, i)
			switch {
			case open > 0 && !errors.Is(err, errOpenHeld):
				t.Fatalf("%s: a request with the token while an open connection is held: %v", row, err)
			case open == 0 && err != nil:
				t.Fatalf("%s: a request with the token, no open connection held: %v", row, err)
			case err == nil:
				where[l.ID] = ""
			}
			k.mu.Lock()
			counted := 0
			for _, ids := range k.addrs {
				counted += len(ids)
			}
			for _, ids := range k.tokened {
				counted += len(ids)
			}
			if counted != len(k.kept) || len(k.kept) != len(where) {
				t.Errorf("%s: %d counted, %d kept, %d made", row, counted, len(k.kept), len(where))
			}
			for id, a := range where {
				kl, ok := k.kept[id]
				_, there := k.addrs[a][id]
				if a == "" {
					_, there = k.tokened[owner][id]
				}
				if !ok || kl.addr != a || !there {
					t.Errorf("%s: link %s is counted at %q (kept %v, counted there %v), its request or admission at %q", row, id, kl.addr, ok, there, a)
				}
				if sl, ok := e.srv.links.Get(id); !ok || sl.Addr != a {
					t.Errorf("%s: the store counts link %s at %v", row, id, sl)
				}
			}
			if open > 0 && len(k.tokened[owner]) > 0 {
				t.Errorf("%s: links made with the token are counted against no address while an open connection is held: %v", row, k.tokened[owner])
			}
			_, held := k.open[owner]
			if held != (open > 0) {
				t.Errorf("%s: the keeper says an open connection is held %v, %d are", row, held, open)
			}
			k.mu.Unlock()
			if t.Failed() {
				t.FailNow()
			}
		}
		for id := range where {
			e.srv.links.Drop(id)
			e.srv.dropLinkFile(id)
		}
	}
}

// mintHostLinkFor mints a link over host's connection that lasts ttl
// seconds (0 for no end), and returns its id and token.
func mintHostLinkFor(t *testing.T, host *fakeHost, id string, ttl int) (linkID, token string) {
	t.Helper()
	host.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: id, Role: "view", TTLSeconds: ttl})
	m := linkAnswer(t, host, id)
	if m["t"] != proto.HostLinkCreated {
		t.Fatalf("link %s: %v", id, m)
	}
	u, _ := m["url"].(string)
	return m["linkId"].(string), u[strings.LastIndex(u, "/")+1:]
}

// revokeHeld revokes the session's link linkID with its hook held after the
// mark, until the function it returns is called (which waits for the hook):
// a link revoked whose hook has not yet run.
func revokeHeld(t *testing.T, e *testEnv, sessionID, linkID string) func() {
	t.Helper()
	hook := e.srv.links.OnRevoke
	reached, gate, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e.srv.links.OnRevoke = func(sid, lid string) {
		if lid == linkID {
			close(reached)
			<-gate
		}
		hook(sid, lid)
	}
	go func() {
		defer close(done)
		e.srv.links.Revoke(sessionID, linkID)
	}()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the revoke never reached its hook")
	}
	return func() {
		close(gate)
		<-done
	}
}

// At an open admission only the links that still open something are
// counted, moved and kept: one that has expired by the link store's clock
// and is not yet swept, one revoked whose hook has not yet run, and one the
// store dropped take no place; their files go, and they never end a link
// that still opens for room they do not use.
func TestAnOpenAdmissionCountsOnlyTheLinksThatStillOpen(t *testing.T) {
	type made struct {
		id, token string
		opens     bool
	}
	// admit has setup make links with the token (mint, a connection
	// asking a few at most), sees the host leave and takes its session
	// back open.
	admit := func(t *testing.T, bound int, setup func(e *testEnv, sessionID string, mint func(ttl int) made, clock *atomic.Int64) []made) (*testEnv, string, []made) {
		dir := filepath.Join(t.TempDir(), "data")
		e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = bound })
		var clock atomic.Int64
		e.srv.links.SetNowForTest(func() time.Time { return time.Now().Add(time.Duration(clock.Load())) })
		hi, hs := instanceHost(e, "instance-secret-0123456789", "1")
		host, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		n := 0
		mint := func(ttl int) made {
			if n > 0 && n%proto.LinkRequestsPerMinute == 0 {
				leave(t, e, host)
				host, _ = dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
			}
			n++
			id, token := mintHostLinkFor(t, host, fmt.Sprintf("m%d", n), ttl)
			return made{id, token, true}
		}
		links := setup(e, host.sessionID, mint, &clock)
		leave(t, e, host)
		open, _ := dialFakeHostWith(t, e, hi, hs, "")
		if open.sessionID != host.sessionID {
			t.Fatalf("back as %s, was %s", open.sessionID, host.sessionID)
		}
		return e, dir, links
	}
	check := func(t *testing.T, e *testEnv, dir string, links []made, ended ...string) {
		t.Helper()
		k := e.srv.keeper
		k.mu.Lock()
		var at []string
		for a, ids := range k.addrs {
			for id := range ids {
				at = append(at, a+" "+id)
			}
		}
		tokened := len(k.tokened)
		k.mu.Unlock()
		var want []string
		for _, l := range links {
			_, err := os.Stat(filepath.Join(dir, "links", l.id+".json"))
			switch {
			case !l.opens:
				if !os.IsNotExist(err) {
					t.Errorf("the file of %s, which opens nothing: %v", l.id, err)
				}
			case slices.Contains(ended, l.id):
				linkEnded(t, e, dir, l.id, l.token)
			default:
				linkKept(t, e, dir, l.id, l.token)
				want = append(want, "127.0.0.1 "+l.id)
			}
		}
		slices.Sort(at)
		slices.Sort(want)
		if !slices.Equal(at, want) || tokened != 0 {
			t.Fatalf("counted against the address %v, want %v; %d instances counted against none", at, want, tokened)
		}
	}
	t.Run("an older link that never expires and a newer one expired", func(t *testing.T) {
		e, dir, links := admit(t, 1, func(_ *testEnv, _ string, mint func(int) made, clock *atomic.Int64) []made {
			a, b := mint(0), mint(300)
			clock.Store(int64(301 * time.Second))
			b.opens = false
			return []made{a, b}
		})
		check(t, e, dir, links)
	})
	t.Run("an older link and a newer one revoked, its hook not yet run", func(t *testing.T) {
		var release func()
		e, dir, links := admit(t, 1, func(e *testEnv, sessionID string, mint func(int) made, _ *atomic.Int64) []made {
			a, b := mint(0), mint(0)
			release = revokeHeld(t, e, sessionID, b.id)
			b.opens = false
			return []made{a, b}
		})
		check(t, e, dir, links)
		release()
		check(t, e, dir, links)
	})
	t.Run("a mixed set past a larger bound", func(t *testing.T) {
		var release func()
		e, dir, links := admit(t, 2, func(e *testEnv, sessionID string, mint func(int) made, clock *atomic.Int64) []made {
			var ls []made
			for i, ttl := range []int{0, 100, 0, 0, 0, 0} {
				l := mint(ttl)
				l.opens = i%2 == 0
				ls = append(ls, l)
			}
			clock.Store(int64(101 * time.Second))
			release = revokeHeld(t, e, sessionID, ls[3].id)
			e.srv.links.Drop(ls[5].id)
			return ls
		})
		// Three still open, at a bound of two: the oldest of them ends.
		check(t, e, dir, links, links[0].id)
		release()
	})
}

// dialFakeDeskWith registers a run's desk m with the Authorization auth.
func dialFakeDeskWith(t *testing.T, e *testEnv, m proto.RegisterDesk, auth string) *fakeHost {
	t.Helper()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {auth}}})
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	c.SetReadLimit(proto.MaxHostMessage + proto.MaxFrame)
	t.Cleanup(func() { c.CloseNow() })
	h := &fakeHost{t: t, c: c, msgs: make(chan map[string]any, 4096), bin: make(chan []byte, 4096), done: make(chan struct{})}
	go h.pump()
	h.send(m)
	h.sessionID, _ = h.expect(proto.HostRegistered)["sessionId"].(string)
	return h
}

// linkGoneFor waits for h to be told that linkID went, ended there.
func linkGoneFor(t *testing.T, h *fakeHost, linkID string) {
	t.Helper()
	for {
		if m := h.expect(proto.HostLinkGone); m["linkId"] == linkID {
			if m["ended"] != true {
				t.Fatalf("the link_gone of a link the switchyard ended: %v", m)
			}
			return
		}
	}
}

// A link an open admission ends is told to the connection that holds it,
// still connected with the host token, though nobody joined through it,
// as ended: a session's link to its session, a run link to its run's desk,
// and a run link of a run with no desk to its member's connection it was
// minted over. It joins 404 and its file is gone; the link that fits stays.
func TestALinkEndedAtAnOpenAdmissionIsToldToItsConnection(t *testing.T) {
	const instance = "instance-secret-0123456789"
	t.Run("a session's link", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 1 })
		hi, hs := instanceHost(e, instance, "1")
		held, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		a, at := mintHostLink(t, held, "a")
		b, bt := mintHostLink(t, held, "b")
		hs.LocalID = "2"
		dialFakeHostWith(t, e, hi, hs, "")
		linkGoneFor(t, held, a)
		linkEnded(t, e, dir, a, at)
		linkKept(t, e, dir, b, bt)
	})
	t.Run("a run link", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 1 })
		desk := dialFakeDeskWith(t, e, deskReg(instance, "run-1"), "Bearer test-host-token")
		r := runLinkOver(desk, "view", proto.RunGroup{ID: "run-1", Name: "Release", Desk: desk.sessionID, Members: []proto.RunMember{{Name: "lead", AgentID: "cat", Status: "pending"}}})
		if r["t"] != proto.HostLinkCreated {
			t.Fatalf("the run link: %v", r)
		}
		u, _ := r["url"].(string)
		hi, hs := instanceHost(e, instance, "1")
		held, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		b, bt := mintHostLink(t, held, "b")
		hs.LocalID = "2"
		dialFakeHostWith(t, e, hi, hs, "")
		linkGoneFor(t, desk, r["linkId"].(string))
		linkEnded(t, e, dir, r["linkId"].(string), u[strings.LastIndex(u, "/")+1:])
		linkKept(t, e, dir, b, bt)
	})
	t.Run("a run link of a run with no desk", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 1 })
		hi, hs := instanceHost(e, instance, "1")
		member, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		r := runLinkOver(member, "view", proto.RunGroup{ID: "run-1", Name: "Release", Members: []proto.RunMember{{Name: "lead", SessionID: member.sessionID, AgentID: "cat", Status: "running"}}})
		if r["t"] != proto.HostLinkCreated {
			t.Fatalf("the run link: %v", r)
		}
		u, _ := r["url"].(string)
		b, bt := mintHostLink(t, member, "b")
		hs.LocalID = "2"
		dialFakeHostWith(t, e, hi, hs, "")
		linkGoneFor(t, member, r["linkId"].(string))
		linkEnded(t, e, dir, r["linkId"].(string), u[strings.LastIndex(u, "/")+1:])
		linkKept(t, e, dir, b, bt)
	})
	// A group that names a desk does not say the run has one: a desk kept
	// through its grace has no connection to tell, and one that has ended
	// stays named until the group's next sync. The members are told.
	for _, deskIs := range []string{"away", "gone"} {
		t.Run("a run link whose run's desk is "+deskIs, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 1 })
			desk := dialFakeDeskWith(t, e, deskReg(instance, "run-1"), "Bearer test-host-token")
			hi, hs := instanceHost(e, instance, "1")
			member, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
			r := runLinkOver(desk, "view", proto.RunGroup{ID: "run-1", Name: "Release", Desk: desk.sessionID, Members: []proto.RunMember{{Name: "lead", SessionID: member.sessionID, AgentID: "cat", Status: "running"}}})
			if r["t"] != proto.HostLinkCreated {
				t.Fatalf("the run link: %v", r)
			}
			u, _ := r["url"].(string)
			d, _ := e.srv.hosts.Desk(desk.sessionID)
			if deskIs == "away" {
				desk.c.CloseNow()
				until(t, "the desk away", func() bool { return !d.Connected() })
			} else {
				d.EndDesk()
			}
			b, bt := mintHostLink(t, member, "b")
			hs.LocalID = "2"
			dialFakeHostWith(t, e, hi, hs, "")
			linkGoneFor(t, member, r["linkId"].(string))
			linkEnded(t, e, dir, r["linkId"].(string), u[strings.LastIndex(u, "/")+1:])
			linkKept(t, e, dir, b, bt)
		})
	}
}

// A link an open admission ends whose file cannot be removed then (its
// directory not writable): while the switchyard runs it stays ended and
// the sweep removes its file once it can; if the switchyard restarts first,
// the file reads back as a link made with the token, which works again
// when its host comes back with the token, and is counted again, the oldest
// ending, when its host comes back open.
func TestALinkEndedAtAnOpenAdmissionWhoseFileStays(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory of mode 0500")
	}
	bound := func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 }
	for _, then := range []string{"the sweep", "a restart, back with the token", "a restart, back open"} {
		t.Run(then, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			e := keptSwitchyard(t, dir, bound)
			hi, hs, sessionID, secret, ids, tokens := tokenedLinks(t, e, 3)
			links := filepath.Join(dir, "links")
			if err := os.Chmod(links, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(links, 0o700) })
			bySecret := hs
			bySecret.LocalID = ""
			open, reg := dialFakeHostRegister(t, e, proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"}, Session: bySecret,
				Resume: &proto.HostResume{SessionID: sessionID, Secret: secret}}, "", nil)
			if code, out := joinStatus(t, e, tokens[0]); code != http.StatusNotFound {
				t.Fatalf("the ended link: %d %v", code, out)
			}
			if l, _ := reg["links"].([]any); slices.Contains(l, any(ids[0])) {
				t.Fatalf("registered lists the ended link: %v", l)
			}
			file := filepath.Join(links, ids[0]+".json")
			if _, err := os.Stat(file); err != nil {
				t.Fatalf("the ended link's file, not removable: %v", err)
			}
			leave(t, e, open)
			if err := os.Chmod(links, 0o700); err != nil {
				t.Fatal(err)
			}
			if then == "the sweep" {
				e.srv.sweepLinks(time.Now())
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatalf("the ended link's file after the sweep: %v", err)
				}
				back, reg := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
				if l, _ := reg["links"].([]any); slices.Contains(l, any(ids[0])) {
					t.Fatalf("back with the token, registered lists the ended link: %v", l)
				}
				if code, out := joinStatus(t, e, tokens[0]); code != http.StatusNotFound {
					t.Fatalf("the ended link, its host back with the token: %d %v", code, out)
				}
				leave(t, e, back)
				return
			}
			b := keptSwitchyard(t, dir, bound)
			if then == "a restart, back with the token" {
				_, reg := dialFakeHostWith(t, b, hi, hs, "Bearer test-host-token")
				if l, _ := reg["links"].([]any); len(l) != 3 || !slices.Contains(l, any(ids[0])) {
					t.Fatalf("back with the token after a restart, registered lists %v", l)
				}
				if code, out := joinStatus(t, b, tokens[0]); code != http.StatusOK {
					t.Fatalf("the link read back, its host back with the token: %d %v", code, out)
				}
				return
			}
			again, reg := dialFakeHostWith(t, b, hi, hs, "")
			heldToTheOpenBound(t, b, dir, again, reg, ids, tokens)
		})
	}
}

// At an open admission the address's own links count only while they
// still open something: one of another open host's, or of the admitted
// instance's own from that address, that has expired by the link store's
// clock and is not yet swept, or that is revoked and its hook not yet run,
// takes no place, so a live link the instance made with the token is not
// ended for it. The admission leaves those links as they are (counted,
// their files there) for the sweep and the hook, after which the address
// holds the admitted link alone, one of its one.
func TestAnOpenAdmissionCountsOnlyTheAddresssLinksThatStillOpen(t *testing.T) {
	const instance = "instance-secret-0123456789"
	for _, dead := range []string{"another open host's, expired", "its own from the address, expired", "another open host's, revoked"} {
		t.Run(dead, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			e := keptSwitchyard(t, dir, func(c *config.Config) { c.Switchyard.OpenHostLinks = 1 })
			var clock atomic.Int64
			now := func() time.Time { return time.Now().Add(time.Duration(clock.Load())) }
			e.srv.links.SetNowForTest(now)
			hi, hs := instanceHost(e, instance, "1")
			// x: the address's link that will open nothing.
			var (
				x       string
				release = func() {}
			)
			switch dead {
			case "its own from the address, expired":
				open, _ := dialFakeHostWith(t, e, hi, hs, "")
				x, _ = mintHostLinkFor(t, open, "x", 300)
				leave(t, e, open)
			default:
				ohi, ohs := instanceHost(e, "other-instance-0123456789", "h")
				other, _ := dialFakeHostWith(t, e, ohi, ohs, "")
				ttl := 300
				if dead == "another open host's, revoked" {
					ttl = 0
				}
				x, _ = mintHostLinkFor(t, other, "x", ttl)
				leave(t, e, other)
				if ttl == 0 {
					release = revokeHeld(t, e, other.sessionID, x)
				}
			}
			tokened, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
			l, lt := mintHostLinkFor(t, tokened, "l", 0)
			leave(t, e, tokened)
			if dead != "another open host's, revoked" {
				clock.Store(int64(301 * time.Second))
			}
			if e.srv.opensAnything(x) {
				t.Fatalf("the address's link %s still opens", x)
			}
			open, reg := dialFakeHostWith(t, e, hi, hs, "")
			linkKept(t, e, dir, l, lt)
			if links, _ := reg["links"].([]any); !slices.Contains(links, any(l)) {
				t.Fatalf("registered lists %v, not the kept link %s", links, l)
			}
			counted := func() []string {
				k := e.srv.keeper
				k.mu.Lock()
				defer k.mu.Unlock()
				var ids []string
				for id := range k.addrs["127.0.0.1"] {
					ids = append(ids, id)
				}
				slices.Sort(ids)
				return ids
			}
			// The dead link is left as it was: counted, its file there.
			want := []string{l, x}
			slices.Sort(want)
			if got := counted(); !slices.Equal(got, want) {
				t.Fatalf("counted against the address after the admission %v, want %v", got, want)
			}
			if _, err := os.Stat(filepath.Join(dir, "links", x+".json")); err != nil {
				t.Fatalf("the file of the address's dead link: %v", err)
			}
			release()
			e.srv.links.ExpireDue(now())
			e.srv.sweepLinks(now())
			if got := counted(); !slices.Equal(got, []string{l}) {
				t.Fatalf("counted against the address after the sweep %v, want [%s]", got, l)
			}
			linkKept(t, e, dir, l, lt)
			open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
			if m := linkAnswer(t, open, "o1"); m["code"] != "link_refused" {
				t.Fatalf("the address at its bound, one of one: %v", m)
			}
		})
	}
}

// logGate is a log handler that holds the first record with the message
// it is armed with, until released.
type logGate struct {
	msg     atomic.Pointer[string]
	once    sync.Once
	reached chan struct{}
	gate    chan struct{}
}

func newLogGate() *logGate {
	return &logGate{reached: make(chan struct{}), gate: make(chan struct{})}
}

func (g *logGate) Enabled(context.Context, slog.Level) bool { return true }

func (g *logGate) Handle(_ context.Context, r slog.Record) error {
	if m := g.msg.Load(); m != nil && r.Message == *m {
		g.once.Do(func() {
			close(g.reached)
			<-g.gate
		})
	}
	return nil
}

func (g *logGate) WithAttrs([]slog.Attr) slog.Handler { return g }
func (g *logGate) WithGroup(string) slog.Handler      { return g }

// arm holds the next record logged with msg.
func (g *logGate) arm(msg string) { g.msg.Store(&msg) }

// held waits for the record; the bound only bounds a hang.
func (g *logGate) held(t *testing.T) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("nothing reached the log line")
	}
}

// toldOf is what h is told of the link its request requestID made, in
// order, once it has been told both its link_created (returned too) and its
// link_gone.
func toldOf(t *testing.T, h *fakeHost, requestID string) ([]string, map[string]any) {
	t.Helper()
	type said struct{ t, id string }
	var (
		told    []said
		created map[string]any
	)
	timeout := time.After(10 * time.Second)
	for {
		if created != nil && slices.Contains(told, said{proto.HostLinkGone, created["linkId"].(string)}) {
			var out []string
			for _, s := range told {
				if s.id == created["linkId"] {
					out = append(out, s.t)
				}
			}
			return out, created
		}
		select {
		case m := <-h.msgs:
			id, _ := m["linkId"].(string)
			switch m["t"] {
			case proto.HostLinkCreated:
				if m["requestId"] == requestID {
					created = m
				}
				told = append(told, said{proto.HostLinkCreated, id})
			case proto.HostLinkGone:
				told = append(told, said{proto.HostLinkGone, id})
			case proto.HostError:
				if m["requestId"] == requestID {
					t.Fatalf("the request was refused: %v", m)
				}
			}
		case <-timeout:
			t.Fatalf("told %v, not both the link's creation and its end", told)
		}
	}
}

// joinToken is the token of the link a link_created m names.
func joinToken(m map[string]any) string {
	u, _ := m["url"].(string)
	return u[strings.LastIndex(u, "/")+1:]
}

// A link an open admission of another session of its instance ends is told
// to the connection that asked for it after its link_created, whichever of
// the two the switchyard does first: the admission as the request's answer
// is on its way, after the host has its answer, or before the keeper
// decides the request (then refused, and nothing named). A session's link
// and a run's link alike.
func TestALinkEndedAtAnOpenAdmissionIsToldAfterItsCreation(t *testing.T) {
	const instance = "instance-secret-0123456789"
	type kind struct {
		name, logged string
		ask          func(t *testing.T, e *testEnv) (*fakeHost, func(requestID string))
	}
	kinds := []kind{
		{"a session's link", "link minted for a host", func(t *testing.T, e *testEnv) (*fakeHost, func(string)) {
			hi, hs := instanceHost(e, instance, "1")
			h, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
			return h, func(id string) { h.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: id, Role: "view"}) }
		}},
		{"a run link", "run link minted for a host", func(t *testing.T, e *testEnv) (*fakeHost, func(string)) {
			d := dialFakeDeskWith(t, e, deskReg(instance, "run-1"), "Bearer test-host-token")
			g := proto.RunGroup{ID: "run-1", Name: "Release", Desk: d.sessionID, Members: []proto.RunMember{{Name: "lead", AgentID: "cat", Status: "pending"}}}
			return d, func(id string) {
				d.send(proto.HostRunLinkMsg{T: proto.HostRunLink, RequestID: id, Role: "view", Run: g})
			}
		}},
	}
	for _, kd := range kinds {
		for _, when := range []string{"as its answer is on its way", "after its answer", "before the keeper decides"} {
			t.Run(kd.name+", admitted "+when, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "data")
				gate := newLogGate()
				t.Cleanup(func() {
					select {
					case <-gate.gate:
					default:
						close(gate.gate)
					}
				})
				e := newTestEnvLogging(t, func(c *config.Config) {
					c.Switchyard.Enabled = true
					c.Switchyard.OpenHosts = true
					c.DataDir = dir
					c.Switchyard.OpenHostLinks = 1
				}, slog.New(gate))
				// Another instance's open host fills the address.
				ohi, ohs := instanceHost(e, "other-instance-0123456789", "h")
				other, _ := dialFakeHostWith(t, e, ohi, ohs, "")
				x, xt := mintHostLink(t, other, "x")
				holder, ask := kd.ask(t, e)
				hi, hs := instanceHost(e, instance, "2")
				admit := func() { dialFakeHostWith(t, e, hi, hs, "") }
				var id, token string
				switch when {
				case "as its answer is on its way":
					gate.arm(kd.logged)
					ask("r1")
					gate.held(t)
					admit()
					close(gate.gate)
					told, m := toldOf(t, holder, "r1")
					if !slices.Equal(told, []string{proto.HostLinkCreated, proto.HostLinkGone}) {
						t.Fatalf("the connection that asked is told %v of its link", told)
					}
					id, token = m["linkId"].(string), joinToken(m)
				case "after its answer":
					ask("r1")
					m := linkAnswer(t, holder, "r1")
					if m["t"] != proto.HostLinkCreated {
						t.Fatalf("the link: %v", m)
					}
					id, token = m["linkId"].(string), joinToken(m)
					admit()
					linkGoneFor(t, holder, id)
				default:
					hold := holdKeeping(t, e, false)
					ask("r1")
					hold.held()
					admit()
					hold.release()
					if m := linkAnswer(t, holder, "r1"); m["code"] != "link_refused" || m["message"] != errOpenHeld.Error() {
						t.Fatalf("a request with the token decided after its instance's open admission: %v", m)
					}
					linkKept(t, e, dir, x, xt)
					return
				}
				linkEnded(t, e, dir, id, token)
				linkKept(t, e, dir, x, xt)
			})
		}
	}
}
