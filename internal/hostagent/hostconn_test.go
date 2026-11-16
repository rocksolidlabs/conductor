package hostagent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// How the host keeps its control connection's read loop reading (saves and
// offers' answers run off it) and how it vouches for its server (its own
// pings answered, nothing else).

// --- vouching by the host's own answered pings ---

// While the server answers none of the host's pings past the bound, a
// link's line is refused, nothing typed; the link's going read meanwhile
// stays. Once a ping is answered again, the line of the link still standing
// is typed and entered, and the other link's is refused as gone.
func TestLinkLinePassesAgainOnAnAnsweredPing(t *testing.T) {
	srv := newStubServer(t)
	a, viewer := liveHostAt(t, srv.url, 20*time.Millisecond, 0)
	guest := viewer("0123456789abcdef", "l1")
	other := viewer("1123456789abcdef", "l2")
	server := <-srv.conns
	untilVouches(t, a, true)

	srv.pongs.Store(pongNever)
	untilVouches(t, a, false)
	guest.send(proto.Submit{T: proto.CtlSubmit, Text: "unanswered"})
	guest.until(func(m map[string]any) bool { return m["t"] == proto.CtlError && m["code"] == "input_failed" })
	guest.quiet(300 * time.Millisecond)
	if err := server.Write(t.Context(), websocket.MessageText, mustJSON(proto.LinkGone{T: proto.HostLinkGone, LinkID: "l2"})); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for other.p.cred.err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the link's going was not read while the pings went unanswered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	srv.pongs.Store(pongAnswer)
	untilVouches(t, a, true)
	guest.send(proto.Submit{T: proto.CtlSubmit, Text: "answered"})
	if typed := guest.typed(guest.release(), 2); strings.Join(typed, "|") != "answered|\r" {
		t.Fatalf("typed %q", typed)
	}
	other.send(proto.Submit{T: proto.CtlSubmit, Text: "gone"})
	other.until(func(m map[string]any) bool { return m["t"] == proto.CtlError && m["code"] == "input_failed" })
	other.quiet(300 * time.Millisecond)
}

// firstWithin and firstPing are the bounds of the hosts that meet a link's
// first viewer. The join's ping must outlast the test's own steps between
// the join and the pong's release (a loopback data channel opening, a
// hello answered, an attach, a quiet spell), which take seconds on a loaded
// machine under the race detector: the stub holds the pong for as long as
// the test wants, so the bound is not what a test waits on, and a miss
// within it is what none of these tests mean (the join's ping missed, a
// first line is refused at once instead of waiting). A spell with no link
// held lapses the host's vouching by connLive.lapse, not by waiting the
// bound out.
const (
	firstWithin = 30 * time.Second
	firstPing   = time.Second
)

// firstLinkViewer brings a host of a stub server to when: just after its
// start, just after a reconnect (a new connection, no ping answered on
// it), or after a spell with no link held longer than the bound (its last
// ping answered long ago); the server then holds its pongs, and a viewer
// joins through a link, which has the host ping. It returns the host, the
// server and the viewer once attached.
func firstLinkViewer(t *testing.T, when string) (*agent, *stubServer, *submitViewer) {
	t.Helper()
	srv := newStubServer(t)
	if when == "start" {
		srv.pongs.Store(pongHold)
	}
	a, viewer := liveHostBounds(t, srv.url, 0, session.Options{SubmitPause: 20 * time.Millisecond, FileView: "control", FileEdit: "off", Log: discardLog}, firstWithin, firstPing)
	first := <-srv.conns
	// The host's writer is up before any viewer joins: the join's ping
	// goes on it (pingNow), and none goes while the control loop has not
	// made it yet, which the server's accept does not wait for.
	untilLive(t, a)
	switch when {
	case "reconnect":
		_ = viewer("aaaaaaaaaaaaaaaa", "l0")
		untilVouches(t, a, true)
		before := a.live.Load()
		srv.pongs.Store(pongHold)
		first.CloseNow()
		<-srv.conns
		deadline := time.Now().Add(10 * time.Second)
		for l := a.live.Load(); l == nil || l == before; l = a.live.Load() {
			if time.Now().After(deadline) {
				t.Fatal("the host did not connect again")
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "idle":
		_ = viewer("aaaaaaaaaaaaaaaa", "l0")
		untilVouches(t, a, true)
		// The link goes: no link is held, and the host pings no more.
		if err := first.Write(t.Context(), websocket.MessageText, mustJSON(proto.LinkGone{T: proto.HostLinkGone, LinkID: "l0"})); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for a.creds.held() {
			if time.Now().After(deadline) {
				t.Fatal("a link is still held")
			}
			time.Sleep(5 * time.Millisecond)
		}
		// Two more ticks of keepHearing: the one it may have been in as the
		// link went (a link held read, its ping reserved late) is done, and
		// one that began after found no link held and reserved none; then
		// the pings under way have their pongs, so none completes after the
		// lapse and vouches again; then the spell passes: the last answered
		// ping ages past the bound.
		untilTicks(t, a, a.ticks.Load()+2)
		untilPingsDone(t, a.live.Load())
		a.live.Load().lapse()
		untilVouches(t, a, false)
		srv.pongs.Store(pongHold)
	}
	if a.vouches() == nil {
		t.Fatal("the host vouches before the viewer joins")
	}
	guest := viewer("0123456789abcdef", "l1")
	return a, srv, guest
}

var firstCases = []string{"start", "reconnect", "idle"}

// untilTicks waits until keepHearing has done n ticks.
func untilTicks(t *testing.T, a *agent, n int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for a.ticks.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("keepHearing did %d ticks, waiting for %d", a.ticks.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// untilPingsDone waits until no ping of l waits for its pong.
func untilPingsDone(t *testing.T, l *connLive) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l.mu.Lock()
		n := l.underWay
		l.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d pings still under way", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// untilLive waits until the host's control writer is up, with the
// connection's liveness it pings on.
func untilLive(t *testing.T, a *agent) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for a.live.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the host's control writer did not come up")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A viewer joining through a link attaches on its link's credential alone:
// after the host's start, a reconnect and a spell with no link held, its
// hello is answered and it is attached while no ping the host sent has
// been answered.
func TestFirstLinkViewerAttachesBeforeAnyPong(t *testing.T) {
	for _, when := range firstCases {
		t.Run(when, func(t *testing.T) {
			a, _, guest := firstLinkViewer(t, when)
			if a.vouches() == nil {
				t.Fatal("a pong came: the server holds them")
			}
			guest.p.mu.Lock()
			attached := guest.p.sub != nil
			guest.p.mu.Unlock()
			if !attached {
				t.Fatal("the viewer is not attached")
			}
		})
	}
}

// The first line of a viewer who joined through a link, after the host's
// start, a reconnect and a spell with no link held, waits for the answer to
// the ping its join sent, within its own time, and is typed and entered
// once it comes: never before.
func TestFirstLineWaitsForTheFirstPong(t *testing.T) {
	for _, when := range firstCases {
		t.Run(when, func(t *testing.T) {
			a, srv, guest := firstLinkViewer(t, when)
			guest.send(proto.Submit{T: proto.CtlSubmit, Text: "first line"})
			guest.quiet(300 * time.Millisecond)
			if a.vouches() == nil {
				t.Fatal("a pong came: the server holds them")
			}
			srv.pongs.Store(pongAnswer)
			if typed := guest.typed(guest.release(), 2); strings.Join(typed, "|") != "first line|\r" {
				t.Fatalf("typed %q", typed)
			}
		})
	}
}

// The first chat posts of a viewer who joined through a link, after the
// host's start and a reconnect (where a page sends again what it had not
// had answered), wait for the answer to the join's ping as its first line
// does, in their order: one for the agent, then one for the chat alone.
// Neither is refused; once the pong comes, both are kept, in order, and
// the one for the agent is typed and entered.
func TestFirstChatPostsWaitForTheFirstPong(t *testing.T) {
	for _, when := range []string{"start", "reconnect"} {
		t.Run(when, func(t *testing.T) {
			_, srv, guest := firstLinkViewer(t, when)
			guest.send(proto.ChatPost{T: proto.CtlChat, Nonce: "c1", Text: "hello agent", To: proto.ChatToAgent})
			guest.send(proto.ChatPost{T: proto.CtlChat, Nonce: "c2", Text: "and the chat"})
			for _, m := range guest.sync() {
				if m["t"] == proto.CtlError || m["t"] == proto.CtlChat {
					t.Fatalf("before the pong: %v", m)
				}
			}
			guest.quiet(300 * time.Millisecond)
			srv.pongs.Store(pongAnswer)
			var kept []any
			guest.until(func(m map[string]any) bool {
				if m["t"] == proto.CtlError {
					t.Fatalf("refused: %v", m)
				}
				if m["t"] == proto.CtlChat && m["nonce"] != nil {
					kept = append(kept, m["nonce"])
				}
				return len(kept) == 2
			})
			if fmt.Sprint(kept) != "[c1 c2]" {
				t.Fatalf("kept %v", kept)
			}
			if typed := guest.typed(guest.release(), 2); strings.Join(typed, "|") != "hello agent|\r" {
				t.Fatalf("typed %q", typed)
			}
		})
	}
}

// A first file read of a viewer who joined through a link, after the host's
// start and a reconnect (where the Files tab asks again), waits for the
// answer to the join's ping, off the frame loop, and is answered with the
// file once it comes, not refused.
func TestFirstReadWaitsForTheFirstPong(t *testing.T) {
	for _, when := range []string{"start", "reconnect"} {
		t.Run(when, func(t *testing.T) {
			a, srv, guest := firstLinkViewer(t, when)
			fileIn(t, a, "notes.txt", "alpha\n")
			guest.send(proto.FileGet{T: proto.CtlFileGet, ReqID: "r1", Path: "notes.txt"})
			guest.sync() // the frame loop goes on meanwhile
			time.Sleep(300 * time.Millisecond)
			srv.pongs.Store(pongAnswer)
			if h := guest.fileAnswer("r1"); h.Kind != "file" {
				t.Fatalf("the first read: %+v %+v", h, h.Error)
			}
		})
	}
}

// A viewer joining through a link has the host's ping under way as its join
// is handled, before anything of the viewer's can be: its first request,
// over the relay right behind its hello, waits for that ping's answer
// however late the ping itself goes out, and is written once it is
// answered.
func TestTheJoinsPingIsUnderWayBeforeTheFirstRequest(t *testing.T) {
	srv := newStubServer(t)
	a, proc, _ := liveHostParts(t, srv.url, 0, session.Options{FileEdit: "off", Log: discardLog}, firstWithin, firstPing)
	<-srv.conns
	untilLive(t, a)
	release := make(chan struct{})
	hold := func() { <-release }
	a.beforePing.Store(&hold)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	const id = "0123456789abcdef"
	if err := a.handleControl(t.Context(), mustJSON(proto.ViewerJoin{T: proto.HostViewerJoin, ViewerID: id, Role: string(session.RoleControl), LinkID: "l1"})); err != nil {
		t.Fatal(err)
	}
	l := a.live.Load()
	l.mu.Lock()
	underWay := l.underWay
	l.mu.Unlock()
	if underWay < 1 {
		t.Fatal("no ping under way once the join was handled")
	}
	if err := a.handleControl(t.Context(), mustJSON(proto.ViewerRef{T: proto.HostRelayStart, ViewerID: id})); err != nil {
		t.Fatal(err)
	}
	p := a.peer(id)
	p.handleFrame(helloFrame("guest"))
	untilAttached(t, a, id)
	p.handleFrame(proto.Frame{Type: proto.TypeInput, Payload: []byte("first keys")})
	typedNothing(t, proc, 300*time.Millisecond)
	close(release)
	if got := typedNext(t, proc); got != "first keys" {
		t.Fatalf("typed %q", got)
	}
}

// An offer whose viewer moves to the relay while its answer is being made
// (the answer runs off the read loop, so relay_start can be read
// meanwhile) is let go quietly: neither an answer nor an error is sent for
// the connection let go, and the viewer stays, on the relay.
func TestAnOfferOfAViewerThatMovedToTheRelayIsLetGoQuietly(t *testing.T) {
	a, _, _ := newHostOf(t, editOpts(), 0, 0)
	sent := make(chan any, 16)
	a.sendHook = func(v any) {
		select {
		case sent <- v:
		default:
		}
	}
	t.Cleanup(a.closeAllPeers)
	entered, release := make(chan struct{}), make(chan struct{})
	hold := func() {
		close(entered)
		<-release
	}
	a.beforeAnswer.Store(&hold)
	const id = "0123456789abcdef"
	a.addPeer(id, session.RoleControl, "", "")
	if err := a.handleControl(t.Context(), offerOf(id, viewerOffer(t))); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := a.handleControl(t.Context(), mustJSON(proto.ViewerRef{T: proto.HostRelayStart, ViewerID: id})); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.After(time.Second)
	for {
		select {
		case v := <-sent:
			switch v.(type) {
			case proto.ViewerSDP, proto.ViewerError:
				t.Fatalf("sent for the connection let go: %#v", v)
			}
			continue
		case <-deadline:
		}
		break
	}
	if a.peer(id) == nil {
		t.Fatal("the viewer was let go")
	}
}

// pausingSwitchyard is a switchyard whose bound on what a tokened host
// relays is 16 KiB a second, past its burst (4 MiB, the floor): a host that
// relays more has the switchyard pause its reading until the bound is paid.
func pausingSwitchyard(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.HostTokens = []string{hostToken}
	cfg.Switchyard.Enabled = true
	cfg.Switchyard.RelayKBps = 16
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs
}

// The cost of vouching by the host's own pings alone, as decided: while a
// server pauses its reading for longer than the bound (a switchyard's
// bound on what a host relays, paid by the host's own frames ahead of its
// pings), a link's line waits and is refused rather than entered, the
// server's writes notwithstanding. Once the server reads again and answers,
// the link's line is typed and entered.
//
// The pause is held while the test needs it, never timed: the host keeps
// pausingSwitchyard's bucket owing pauseAhead, whatever its own writes
// take (on a loaded machine the burst alone took longer than the pause it
// was to leave), and the line goes in only once a ping sent behind that
// debt has gone unanswered (#428's checks: a ping answered late before the
// pause, and the line entered as it was).
func TestLinkLineWithheldWhileTheServerPausesReading(t *testing.T) {
	yard := pausingSwitchyard(t)
	a, viewer := liveHostAt(t, yard.URL, 20*time.Millisecond, 0)
	guest := viewer("0123456789abcdef", "l1")
	untilVouches(t, a, true)
	// The switchyard's bucket holds at most its burst as the host starts,
	// and fills at its rate from then on; the host relays to a viewer the
	// switchyard does not know until the switchyard has read the burst and
	// what it filled with, and pauseAhead more, and keeps that much ahead
	// until stop. A ping behind that debt is read pauseAhead/rate (4 s)
	// later, past its bound.
	const rate, burst, pauseAhead = 16 << 10, 4 << 20, 64 << 10
	frame := proto.Encode(proto.TypeOutput, bytes.Repeat([]byte("o"), proto.MaxOutput))
	start := time.Now()
	ahead, stop, spent := make(chan time.Duration, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		written := 0
		for {
			if written < burst+int(rate*time.Since(start).Seconds())+pauseAhead {
				if err := a.sendRelayWithin("fedcba9876543210", frame, nil, time.Minute); err != nil {
					spent <- err
					return
				}
				written += len(frame)
				continue
			}
			select {
			case ahead <- sinceEpoch():
			default:
			}
			select {
			case <-stop:
				spent <- nil
				return
			case <-tick.C:
			}
		}
	}()
	var since time.Duration
	select {
	case since = <-ahead:
	case err := <-spent:
		t.Fatal(err)
	}
	untilPingMissedSince(t, a, since)
	guest.send(proto.Submit{T: proto.CtlSubmit, Text: "while paused"})
	guest.until(func(m map[string]any) bool { return m["t"] == proto.CtlError && m["code"] == "input_failed" })
	guest.quiet(300 * time.Millisecond)
	close(stop)
	if err := <-spent; err != nil {
		t.Fatal(err)
	}
	// What the bucket still owes, at most pauseAhead, a frame and a tick's
	// filling, is read at its rate before the next ping is.
	untilVouchesWithin(t, a, true, time.Duration(pauseAhead+2*len(frame))*time.Second/rate+10*time.Second)
	guest.send(proto.Submit{T: proto.CtlSubmit, Text: "read again"})
	if typed := guest.typed(guest.release(), 2); strings.Join(typed, "|") != "read again|\r" {
		t.Fatalf("typed %q", typed)
	}
}

// untilPingMissedSince waits until a ping the host sent at since or later
// (as time since liveEpoch) has gone unanswered and the host vouches no
// more: one answered late that was sent before since can no longer make it
// vouch, for its bound ran out as the later ping's did.
func untilPingMissedSince(t *testing.T, a *agent, since time.Duration) {
	t.Helper()
	within := a.heardWithin()
	deadline := time.Now().Add(10*time.Second + within)
	for {
		if l := a.live.Load(); l != nil {
			l.mu.Lock()
			held := l.hasMissed && l.missed >= since && !l.vouchesWithin(within)
			l.mu.Unlock()
			if held {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no ping sent since %v went unanswered; vouches %v", since, a.vouches() == nil)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- saves and answers off the read loop ---

// holdSaves has a's saves wait, as each part has its turn, while the test
// holds them: the first part to come in signals entered and waits for
// release; every part's save and offset are noted, in the order their
// turns came.
type holdSaves struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	order   []string
}

func newHoldSaves(a *agent) *holdSaves {
	h := &holdSaves{entered: make(chan struct{}), release: make(chan struct{})}
	seam := func(w proto.FileWrite) {
		h.mu.Lock()
		h.order = append(h.order, fmt.Sprintf("%s@%d", w.ReqID, w.Offset))
		first := len(h.order) == 1
		h.mu.Unlock()
		if first {
			close(h.entered)
			<-h.release
		}
	}
	a.beforeSave.Store(&seam)
	return h
}

func (h *holdSaves) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no save had its turn")
	}
}

func (h *holdSaves) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.order...)
}

// relayedTo is what a stub server got relayed for viewer id: the FILE
// answer to reqID.
func stubFileAnswer(t *testing.T, srv *stubServer, id, reqID string) proto.FileHeader {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case data := <-srv.got:
			if len(data) < 1+proto.ViewerIDLen || data[0] != proto.TypeRelay {
				continue
			}
			viewer, inner, err := proto.DecodeRelay(data[1:])
			if err != nil || viewer != id || inner.Type != proto.TypeFile {
				continue
			}
			if h, _, err := proto.DecodeFile(inner.Payload); err == nil && h.ReqID == reqID {
				return h
			}
		case <-deadline:
			t.Fatalf("no FILE answer to %s relayed to %s", reqID, id)
		}
	}
}

// A relayed save runs off the host's read loop: while a link's viewer's
// save waits on a slow disk (a seam holds it), the loop goes on reading,
// the owner's keys are written and the server's link_gone is acted on; once
// the disk lets the save go, its link gone, it is refused and the folder is
// as it was.
func TestRelayedSaveRunsOffTheReadLoop(t *testing.T) {
	srv := newStubServer(t)
	a, proc, _ := liveHostParts(t, srv.url, 0, editOpts(), liveTestWithin, liveTestPing)
	file := fileIn(t, a, "notes.txt", "alpha\n")
	holds := newHoldSaves(a)
	server := <-srv.conns
	write := func(typ websocket.MessageType, b []byte) {
		t.Helper()
		if err := server.Write(t.Context(), typ, b); err != nil {
			t.Fatal(err)
		}
	}
	relay := func(id string, frame []byte) {
		t.Helper()
		env, err := proto.EncodeRelay(id, frame)
		if err != nil {
			t.Fatal(err)
		}
		write(websocket.MessageBinary, env)
	}
	const guest, owner = "0123456789abcdef", "fedcba9876543210"
	for id, link := range map[string]string{guest: "l1", owner: ""} {
		write(websocket.MessageText, mustJSON(proto.ViewerJoin{T: proto.HostViewerJoin, ViewerID: id, Role: string(session.RoleControl), LinkID: link}))
		write(websocket.MessageText, mustJSON(proto.ViewerRef{T: proto.HostRelayStart, ViewerID: id}))
		relay(id, proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: proto.ProtoVersion, Cols: 80, Rows: 24}))
		untilAttached(t, a, id)
	}
	untilVouches(t, a, true)
	relay(guest, saveFrame(t, "w1", "notes.txt", "guest's\n"))
	holds.waitEntered(t)

	relay(owner, proto.Encode(proto.TypeInput, []byte("owner's keys")))
	if got := typedNext(t, proc); got != "owner's keys" {
		t.Fatalf("typed %q", got)
	}
	write(websocket.MessageText, mustJSON(proto.LinkGone{T: proto.HostLinkGone, LinkID: "l1"}))
	deadline := time.Now().Add(5 * time.Second)
	for a.peer(guest).cred.err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the link's going was not read while the save waited")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(holds.release)
	fileRefusedFor(t, stubFileAnswer(t, srv, guest, "w1"), proto.ErrCodeRevoked, goneWords)
	hasBody(t, file, "alpha\n")
}

// untilAttached waits until a's viewer id is attached.
func untilAttached(t *testing.T, a *agent, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if p := a.peer(id); p != nil {
			p.mu.Lock()
			attached := p.sub != nil
			p.mu.Unlock()
			if attached {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never attached", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// relayedFiles drains what s gets, for as long as the test runs, and keeps
// the FILE answers relayed to viewers in it.
type relayedFiles struct {
	mu  sync.Mutex
	got map[string]proto.FileHeader // by viewer and request
}

func collectFiles(t *testing.T, s *heldServer) *relayedFiles {
	f := &relayedFiles{got: map[string]proto.FileHeader{}}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			case data := <-s.got:
				if len(data) < 1+proto.ViewerIDLen || data[0] != proto.TypeRelay {
					continue
				}
				viewer, inner, err := proto.DecodeRelay(data[1:])
				if err != nil || inner.Type != proto.TypeFile {
					continue
				}
				if h, _, err := proto.DecodeFile(inner.Payload); err == nil {
					f.mu.Lock()
					f.got[viewer+"/"+h.ReqID] = h
					f.mu.Unlock()
				}
			}
		}
	}()
	return f
}

// answer waits for the FILE answer to reqID relayed to viewer id.
func (f *relayedFiles) answer(t *testing.T, id, reqID string) proto.FileHeader {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.mu.Lock()
		h, ok := f.got[id+"/"+reqID]
		f.mu.Unlock()
		if ok {
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("no FILE answer to %s relayed to %s", reqID, id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// partsOf sends body as a save of reqID over path to p over the relay, in
// parts of at most proto.MaxWritePart.
func partsOf(t *testing.T, p *peer, reqID, path string, body []byte) {
	t.Helper()
	for off := 0; off < len(body); off += proto.MaxWritePart {
		end := min(off+proto.MaxWritePart, len(body))
		f, err := proto.EncodeFileWrite(proto.FileWrite{ReqID: reqID, Path: path, Offset: int64(off), Total: int64(len(body)), Force: true}, body[off:end])
		if err != nil {
			t.Fatal(err)
		}
		p.handleFrame(proto.Frame{Type: proto.TypeFileWrite, Payload: f[1:]})
	}
}

// A viewer's save parts run in the order sent, one at a time: behind one
// whose save waits on the disk, two whole saves of the largest size wait
// (64 parts, 2 MiB), and one more part is refused (too_many_requests, the
// words saying why), the connection staying. Once the disk lets go, the
// waiting parts run in their order and both saves land.
func TestSaveQueueOrderedAndBounded(t *testing.T) {
	a, s, _, viewer := relayCredHost(t)
	files := collectFiles(t, s)
	notes := fileIn(t, a, "notes.txt", "alpha\n")
	first := fileIn(t, a, "first.txt", "one\n")
	second := fileIn(t, a, "second.txt", "two\n")
	holds := newHoldSaves(a)
	owner := viewer("owner01234567890", "")
	partsOf(t, owner, "w1", "notes.txt", []byte("held\n"))
	holds.waitEntered(t)
	big1 := bytes.Repeat([]byte("1"), proto.MaxFileBytes)
	big2 := bytes.Repeat([]byte("2"), proto.MaxFileBytes)
	partsOf(t, owner, "w2", "first.txt", big1)
	partsOf(t, owner, "w3", "second.txt", big2)
	partsOf(t, owner, "w4", "notes.txt", []byte("one too many\n"))
	h := files.answer(t, owner.id, "w4")
	if h.Kind != "error" || h.Error == nil || h.Error.Code != proto.ErrCodeTooManyRequests || h.Error.Message != saveQueueFullWords {
		t.Fatalf("the part past the queue: %+v %+v", h, h.Error)
	}
	if got := holds.seen(); len(got) != 1 {
		t.Fatalf("parts had their turn behind the held one: %v", got)
	}
	close(holds.release)
	for _, id := range []string{"w1", "w2", "w3"} {
		if h := files.answer(t, owner.id, id); h.Kind != "written" {
			t.Fatalf("%s: %+v %+v", id, h, h.Error)
		}
	}
	hasBody(t, notes, "held\n")
	hasBody(t, first, string(big1))
	hasBody(t, second, string(big2))
	want := []string{"w1@0"}
	for _, id := range []string{"w2", "w3"} {
		for off := 0; off < proto.MaxFileBytes; off += proto.MaxWritePart {
			want = append(want, fmt.Sprintf("%s@%d", id, off))
		}
	}
	if got := holds.seen(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the parts had their turns in the order %v", got)
	}
	a.mu.Lock()
	out := a.out
	a.mu.Unlock()
	if out == nil {
		t.Fatal("the connection was let go")
	}
}

// The parts of a viewer's save that still wait when the viewer leaves are
// dropped: none of them reaches the folder.
func TestSaveOfAViewerThatLeftIsDropped(t *testing.T) {
	a, _, _, viewer := relayCredHost(t)
	fileIn(t, a, "notes.txt", "alpha\n")
	other := fileIn(t, a, "other.txt", "kept\n")
	holds := newHoldSaves(a)
	owner := viewer("owner01234567890", "")
	partsOf(t, owner, "w1", "notes.txt", []byte("held\n"))
	holds.waitEntered(t)
	partsOf(t, owner, "w2", "other.txt", []byte("dropped\n"))
	a.removePeer(owner.id) // as a viewer_leave does
	close(holds.release)
	time.Sleep(300 * time.Millisecond)
	if got := holds.seen(); fmt.Sprint(got) != "[w1@0]" {
		t.Fatalf("parts had their turn once the viewer left: %v", got)
	}
	hasBody(t, other, "kept\n")
}

// A save over the data channel goes on the same queue as one over the
// relay: behind a save the disk holds, the viewer's next save waits its
// turn while its keys are written; then both land, in order.
func TestDataChannelSaveUsesTheSameQueue(t *testing.T) {
	a, viewer := credHost(t)
	file := fileIn(t, a, "notes.txt", "alpha\n")
	holds := newHoldSaves(a)
	owner := viewer("fedcba9876543210", "")
	owner.saveOver("w1", "notes.txt", "first\n")
	holds.waitEntered(t)
	owner.saveOver("w2", "notes.txt", "second\n")
	owner.key("keys")
	if got := owner.release(); got != "keys" {
		t.Fatalf("typed %q", got)
	}
	owner.sync()
	if got := holds.seen(); fmt.Sprint(got) != "[w1@0]" {
		t.Fatalf("parts had their turn behind the held one: %v", got)
	}
	close(holds.release)
	for _, id := range []string{"w1", "w2"} {
		if h := owner.fileAnswer(id); h.Kind != "written" {
			t.Fatalf("%s: %+v %+v", id, h, h.Error)
		}
	}
	hasBody(t, file, "second\n")
}

// offerHost is a host whose control connection's server reads nothing
// until the test lets it (s.read), the connection backed up, and whose own
// messages wait controlWait for their turn (controlTurnWait when 0).
func offerHost(t *testing.T, controlWait time.Duration) (*agent, *heldServer, *pacedConn) {
	t.Helper()
	s, c, pc := dialHeld(t, 0)
	a, _, _ := newHostOf(t, editOpts(), 0, 0)
	a.sessID = "s"
	a.controlWait = controlWait
	w := a.useConn(c, pc)
	t.Cleanup(w.stop)
	t.Cleanup(a.closeAllPeers)
	if err := w.sendRelay(bytes.Repeat([]byte("b"), 1<<20), time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	return a, s, pc
}

// viewerOffer is a browser-like peer's offer of a data channel.
func viewerOffer(t *testing.T) string {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	if _, err := pc.CreateDataChannel("term", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	return offer.SDP
}

// offerOf is the server's offer of viewer id.
func offerOf(id, sdp string) []byte {
	return mustJSON(proto.ViewerSDP{T: proto.HostOffer, ViewerID: id, SDP: sdp})
}

// answersGot counts the answers the server reads: the first within d, and
// any more within a second after it.
func answersGot(s *heldServer, d time.Duration) int {
	n := 0
	deadline := time.After(d)
	for {
		select {
		case data := <-s.got:
			if bytes.Contains(data, []byte(`"t":"answer"`)) {
				if n == 0 {
					deadline = time.After(time.Second)
				}
				n++
			}
		case <-deadline:
			return n
		}
	}
}

// An offer's answer waits for its turn on the writer off the read loop:
// with the connection backed up, the offer is handled at once, and the
// link_gone read after it is acted on while the answer waits; once the
// server reads, the answer reaches it.
func TestAnswerWaitsOffTheReadLoop(t *testing.T) {
	a, s, _ := offerHost(t, 0)
	const id, guest = "0123456789abcdef", "1123456789abcdef"
	a.addPeer(id, session.RoleControl, "", "")
	a.addPeer(guest, session.RoleControl, "l1", "")
	start := time.Now()
	if err := a.handleControl(t.Context(), offerOf(id, viewerOffer(t))); err != nil {
		t.Fatal(err)
	}
	if err := a.handleControl(t.Context(), mustJSON(proto.LinkGone{T: proto.HostLinkGone, LinkID: "l1"})); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the read loop waited %v on an answer's turn", took)
	}
	if !errors.Is(a.peer(guest).cred.err(), session.ErrRevoked) {
		t.Fatal("the link's going was not acted on while the answer waited")
	}
	close(s.read)
	if n := answersGot(s, 10*time.Second); n != 1 {
		t.Fatalf("the server got %d answers", n)
	}
}

// An answer whose turn does not come within its wait (controlTurnWait) is
// dropped, never written late; the connection stays, and the viewer falls
// back to the relay on its own timeout (nothing is said of it).
func TestAnswerDroppedWhenItsTurnNeverComes(t *testing.T) {
	a, s, pc := offerHost(t, 300*time.Millisecond)
	const id = "0123456789abcdef"
	a.addPeer(id, session.RoleControl, "", "")
	if err := a.handleControl(t.Context(), offerOf(id, viewerOffer(t))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	close(s.read)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case data := <-s.got:
			if bytes.Contains(data, []byte(`"t":"answer"`)) || bytes.Contains(data, []byte(`"t":"viewer_error"`)) {
				t.Fatalf("the server got %.40q", data)
			}
			continue
		case <-deadline:
		}
		break
	}
	if _, _, err := pc.unsent(); err != nil {
		t.Fatalf("the connection failed: %v", err)
	}
	a.mu.Lock()
	out := a.out
	a.mu.Unlock()
	if out == nil {
		t.Fatal("the connection was let go")
	}
}

// The words a desk tells its viewers as it ends (tellEnded) wait for their
// turn no longer than the desk's own end message does (controlTurnWait,
// controlWait here): with the switchyard reading nothing, the desk's end
// goes on once that wait is over, not once the connection gives up.
func TestADesksEndWordsWaitNoLongerThanItsOwnMessage(t *testing.T) {
	a, s, _ := offerHost(t, 300*time.Millisecond)
	d := &fakeDecisions{}
	d.ended.Store("dl1", session.ErrExpired)
	a.opts.Decisions = d
	a.addPeer("0123456789abcdef", session.RoleControl, "dl1", "")
	k := &Desk{a: a}
	told := make(chan struct{})
	go func() {
		k.tellEnded()
		close(told)
	}()
	// A bound on a hang: the wait the words have is controlWait.
	select {
	case <-told:
	case <-time.After(10 * time.Second):
		t.Fatal("the desk's end waited past its own message's turn for the switchyard to read its words")
	}
	if n := d.endedAsks.Load(); n != 1 {
		t.Fatalf("the store was asked about the viewer's link %d times", n)
	}
	close(s.read)
}

// A second offer of a viewer whose first is still being answered (its
// answer waiting for its turn) is refused, and only the first's answer is
// sent.
func TestSecondOfferRefusedWhileTheFirstIsUnderWay(t *testing.T) {
	a, s, _ := offerHost(t, 0)
	const id = "0123456789abcdef"
	a.addPeer(id, session.RoleControl, "", "")
	p := a.peer(id)
	if err := p.handleOffer(viewerOffer(t)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleOffer(viewerOffer(t)); !errors.Is(err, errOfferUnderWay) {
		t.Fatalf("a second offer while the first is answered: %v", err)
	}
	if err := a.handleControl(t.Context(), offerOf(id, viewerOffer(t))); err != nil {
		t.Fatal(err)
	}
	close(s.read)
	if n := answersGot(s, 3*time.Second); n != 1 {
		t.Fatalf("the server got %d answers", n)
	}
}
