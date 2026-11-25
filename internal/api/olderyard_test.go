package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// olderYard is a switchyard reduced to its side of the host protocol: it
// answers each registration with a registered that says transcript only
// when transcript is set (an older one never does), mints a link for each
// link request, and keeps the type of every frame it is relayed.
type olderYard struct {
	srv        *httptest.Server
	transcript atomic.Bool
	registered atomic.Int32

	mu     sync.Mutex
	conns  []*websocket.Conn
	frames []byte
	// ended counts the connections whose reads have ended: all that the
	// publisher sent on them has been read.
	ended atomic.Int32
}

func newOlderYard(t *testing.T) *olderYard {
	t.Helper()
	y := &olderYard{}
	done := make(chan struct{})
	y.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/host" {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(proto.MaxHostMessage + proto.MaxFrame)
		if _, _, err := c.Read(r.Context()); err != nil {
			return
		}
		n := y.registered.Add(1)
		reg, _ := json.Marshal(proto.Registered{T: proto.HostRegistered, SessionID: "yard-" + string(rune('0'+n)), Secret: "s",
			ShareBaseURL: "https://yard.invalid", Links: []string{}, Transcript: y.transcript.Load()})
		if err := c.Write(r.Context(), websocket.MessageText, reg); err != nil {
			return
		}
		y.mu.Lock()
		y.conns = append(y.conns, c)
		y.mu.Unlock()
		defer y.ended.Add(1)
		for {
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if f, err := proto.Decode(data); err == nil && f.Type == proto.TypeRelay {
					if _, inner, err := proto.DecodeRelay(f.Payload); err == nil {
						y.mu.Lock()
						y.frames = append(y.frames, inner.Type)
						y.mu.Unlock()
					}
				}
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if m["t"] == proto.HostLink {
				id, _ := m["requestId"].(string)
				role, _ := m["role"].(string)
				b, _ := json.Marshal(proto.LinkCreated{T: proto.HostLinkCreated, RequestID: id, URL: "https://yard.invalid/join/minted", LinkID: "yard-link", Role: role})
				_ = c.Write(r.Context(), websocket.MessageText, b)
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}))
	t.Cleanup(func() { close(done); y.srv.Close() })
	return y
}

// guest joins a viewer to the session over the latest connection's relay,
// as a switchyard relays one, and waits until it is relayed its ready.
func (y *olderYard) guest(t *testing.T, viewerID string) {
	t.Helper()
	y.mu.Lock()
	c := y.conns[len(y.conns)-1]
	y.mu.Unlock()
	ctx := t.Context()
	for _, m := range []any{
		proto.ViewerJoin{T: proto.HostViewerJoin, ViewerID: viewerID, Role: string(session.RoleView), LinkID: "yard-link"},
		proto.ViewerRef{T: proto.HostRelayStart, ViewerID: viewerID},
	} {
		b, _ := json.Marshal(m)
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	env, err := proto.EncodeRelay(viewerID, proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: proto.ProtoVersion}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, env); err != nil {
		t.Fatal(err)
	}
	until(t, "the guest's ready", func() bool {
		y.mu.Lock()
		defer y.mu.Unlock()
		return len(y.frames) > 0
	})
}

// relayed is how many TRANSCRIPT frames the switchyard was relayed.
func (y *olderYard) relayed() int {
	y.mu.Lock()
	defer y.mu.Unlock()
	n := 0
	for _, f := range y.frames {
		if f == proto.TypeTranscript {
			n++
		}
	}
	return n
}

// I14 at the move: a session published to a switchyard whose registered
// lacks transcript, moved to Chat, is published there no longer, its line
// in the plan's words, and the switchyard is relayed no TRANSCRIPT; a Share
// while it is a chat is refused in those words; moved back to its
// terminal, a Share publishes it again. To a switchyard that says the word,
// the move leaves the publication standing.
func TestAChatEndsItsPublicationOnAnOlderSwitchyardUntilItMovesBack(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	y := newOlderYard(t)
	c.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: y.srv.URL, HostName: "office", RelayOnly: true}})
	l := c.launch(t)
	id := l.Info().ID
	until(t, "the publication", func() bool { return c.srv.publishedOf(id) != nil })
	// A guest there, relayed, as the session moves: what the chat sends it
	// would be relayed.
	y.guest(t, "guestrelay000001")
	c.turnEnded(t, id)
	if code, out := c.move(t, id, session.WayProtocol); code != http.StatusOK {
		t.Fatalf("to Chat: %d %v", code, out)
	}
	moved(t, l, session.WayProtocol)
	until(t, "the publication's end", func() bool { return c.srv.publishedOf(id) == nil })
	said := func() bool {
		for _, e := range l.Activity() {
			if e.Type == session.ActivityStatus && e.Message == proto.OlderSwitchyardWords {
				return true
			}
		}
		return false
	}
	until(t, "the line that says why", said)
	// The ended publication's connection is read to its end: every frame
	// the publisher relayed on it has been read.
	until(t, "the publication's connection's end", func() bool { return y.ended.Load() >= 1 })
	if n := y.relayed(); n != 0 {
		t.Fatalf("the older switchyard was relayed %d TRANSCRIPT frames", n)
	}

	// A Share while it is a chat: tried again, refused in the same words.
	resp, out := c.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a link while a chat: %d %v", resp.StatusCode, out)
	}
	if rz, _ := out["rendezvous"].(map[string]any); rz == nil || rz["error"] != proto.OlderSwitchyardWords {
		t.Fatalf("a link while a chat says %v", out)
	}
	if c.srv.publishedOf(id) != nil {
		t.Fatal("a chat was published to the older switchyard")
	}

	// Back in its terminal, a Share publishes it again.
	if code, out := c.move(t, id, session.WayTerminal); code != http.StatusOK {
		t.Fatalf("back: %d %v", code, out)
	}
	moved(t, l, session.WayTerminal)
	resp, out = c.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true || !strings.HasPrefix(out["url"].(string), "https://yard.invalid/") {
		t.Fatalf("a link after the move back: %d %v", resp.StatusCode, out)
	}
	if c.srv.publishedOf(id) == nil {
		t.Fatal("not published again")
	}
	if n := y.relayed(); n != 0 {
		t.Fatalf("the older switchyard was relayed %d TRANSCRIPT frames", n)
	}

	// A switchyard that says the word keeps the chat published.
	c2 := newMoveEnv(t, nil, nil)
	y2 := newOlderYard(t)
	y2.transcript.Store(true)
	c2.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: y2.srv.URL, HostName: "office", RelayOnly: true}})
	l2 := c2.launch(t)
	id2 := l2.Info().ID
	until(t, "the publication", func() bool { return c2.srv.publishedOf(id2) != nil })
	pub := c2.srv.publishedOf(id2)
	c2.turnEnded(t, id2)
	if code, out := c2.move(t, id2, session.WayProtocol); code != http.StatusOK {
		t.Fatalf("to Chat: %d %v", code, out)
	}
	moved(t, l2, session.WayProtocol)
	// What the session's change hook does for a chat, made here so that it
	// has run when the publication is judged.
	c2.srv.endChatOnOlderSwitchyard(l2.Info())
	if got := c2.srv.publishedOf(id2); got != pub {
		t.Fatalf("the publication to a switchyard that says transcript was %v", got)
	}
}
