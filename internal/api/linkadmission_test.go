package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
// while the instance holds an open connection its links count against the
// open one's address, whichever request the switchyard decides first, and a
// request with the token held across the open session's admission is
// decided as the instance then is, never left counted against nothing.
func TestOneHostsTokenedAndOpenRequestsAreCountedInEitherOrder(t *testing.T) {
	tokenedFirst := func(t *testing.T) (*testEnv, *fakeHost, proto.HostInfo, proto.HostSession) {
		e := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), func(c *config.Config) { c.Switchyard.OpenHostLinks = 2 })
		hi, hs := instanceHost(e, "instance-secret-0123456789", "1")
		tokened, _ := dialFakeHostWith(t, e, hi, hs, "Bearer test-host-token")
		mintHostLink(t, tokened, "t1")
		hs.LocalID = "2"
		return e, tokened, hi, hs
	}
	t.Run("the open request held", func(t *testing.T) {
		e, tokened, hi, hs := tokenedFirst(t)
		open, _ := dialFakeHostWith(t, e, hi, hs, "")
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
		if m := linkAnswer(t, tokened, "t2"); m["code"] != "link_refused" {
			t.Fatalf("the tokened link of an instance open at its bound: %v", m)
		}
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
			m := linkAnswer(t, tokened, "t2")
			if room && m["t"] != proto.HostLinkCreated || !room && m["code"] != "link_refused" {
				t.Fatalf("decided after the admission: %v", m)
			}
			open.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: "o1", Role: "view"})
			if m := linkAnswer(t, open, "o1"); m["code"] != "link_refused" {
				t.Fatalf("the open host, its instance's links at the bound: %v", m)
			}
		})
	}
}
