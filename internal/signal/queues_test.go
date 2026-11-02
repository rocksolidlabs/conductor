package signal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// relayedViewer adds a controller to hs and switches it to the relay, with
// the host told of both.
func relayedViewer(t *testing.T, hs *HostedSession, conn *HostConn, id string) *Viewer {
	t.Helper()
	v, err := hs.AddViewer(id, session.RoleControl, "", "")
	if err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	if err := hs.StartRelay(v); err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostRelayStart)
	return v
}

// fillFromViewer relays MaxInput frames from v until total bytes have gone,
// or a relay returns an error, and sends what went on the channel it returns.
func fillFromViewer(hs *HostedSession, v *Viewer, total int) <-chan int {
	chunk := bytes.Repeat([]byte("x"), proto.MaxInput)
	sent := make(chan int, 1)
	go func() {
		n := 0
		for n < total {
			if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeInput, Payload: chunk}); err != nil {
				break
			}
			n += len(chunk)
		}
		sent <- n
	}()
	return sent
}

// waitQueued waits until conn's queue holds at least n bytes.
func waitQueued(t *testing.T, conn *HostConn, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for conn.queued() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d bytes queued, want %d", conn.queued(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitInLine waits until n of the viewers' messages wait in line for room in
// conn's queue.
func waitInLine(t *testing.T, conn *HostConn, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn.mu.Lock()
		waiting := len(conn.waiting)
		conn.mu.Unlock()
		if waiting == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d messages wait for room, want %d", waiting, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A viewer that sends more than the host reads fills the viewers' share of
// the host's queue and then waits for room: the queue's bytes never pass
// their bound, the host's connection stays open, and what the writer takes
// lets the viewer go on, every frame whole and in order.
func TestRelayedFramesWaitForRoomInTheHostQueue(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	v := relayedViewer(t, hs, conn, "0123456789abcdef")
	const total = 2 * hostQueueBytes
	sent := fillFromViewer(hs, v, total)
	envSize := 1 + proto.ViewerIDLen + 1 + proto.MaxInput
	waitQueued(t, conn, hostViewerBytes-envSize)
	time.Sleep(100 * time.Millisecond)
	select {
	case n := <-sent:
		t.Fatalf("%d bytes were queued for a host that reads nothing", n)
	default:
	}
	if q := conn.queued(); q > hostViewerBytes || len(conn.Send)*envSize != q {
		t.Fatalf("%d bytes queued in %d messages, at most %d", q, len(conn.Send), hostViewerBytes)
	}
	select {
	case <-conn.Done():
		t.Fatal("a viewer that sends more than the host reads closed the host's connection")
	default:
	}
	// The writer takes what is queued: the viewer goes on to the end.
	got := 0
	for got < total {
		select {
		case o := <-conn.Send:
			conn.Took(o)
			if q := conn.queued(); q > hostViewerBytes {
				t.Fatalf("%d bytes queued", q)
			}
			id, inner, err := proto.DecodeRelay(o.Binary[1:])
			if err != nil || id != v.ID || inner.Type != proto.TypeInput || len(inner.Payload) != proto.MaxInput {
				t.Fatalf("envelope %q %d %d %v", id, inner.Type, len(inner.Payload), err)
			}
			got += len(inner.Payload)
		case <-time.After(5 * time.Second):
			t.Fatalf("the viewer stopped after %d of %d bytes", got, total)
		}
	}
	if n := <-sent; n != total {
		t.Fatalf("the viewer sent %d of %d bytes", n, total)
	}
	if conn.queued() != 0 {
		t.Fatalf("%d bytes left counted in an empty queue", conn.queued())
	}
}

// While the viewers' lane of the host's queue is full, the server's own
// messages still go: an answer to the host. A viewer joining is let in at
// once, and its join, the viewer's own message, waits its turn without
// closing the host's connection; closed while it waits, the viewer sends
// neither join nor leave. The flood stops when the connection closes.
func TestServerMessagesKeepTheirRoomInTheHostQueue(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	v := relayedViewer(t, hs, conn, "0123456789abcdef")
	sent := fillFromViewer(hs, v, 2*hostQueueBytes)
	waitQueued(t, conn, hostViewerBytes-(1+proto.ViewerIDLen+1+proto.MaxInput))
	// The lane has no room for another frame, though it has for a join: it
	// is full once the viewer's next frame waits in line, which a join
	// then waits behind.
	waitInLine(t, conn, 1)
	before := len(conn.Send)
	if err := hs.Tell(proto.ErrorMsg{T: proto.HostError, Code: "not_found", Message: "no such link", RequestID: "r1"}); err != nil {
		t.Fatalf("the server's answer to its host: %v", err)
	}
	if len(conn.Own) != 1 || len(conn.Send) != before {
		t.Fatalf("%d of the server's own and %d of the viewers' queued, want 1 and %d", len(conn.Own), len(conn.Send), before)
	}
	w, err := hs.AddViewer("fedcba9876543210", session.RoleView, "", "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if len(conn.Send) != before {
		t.Fatalf("a join went past a full lane: %d messages queued, want %d", len(conn.Send), before)
	}
	select {
	case <-conn.Done():
		t.Fatal("the host's connection was closed")
	default:
	}
	removed := make(chan struct{})
	go func() { hs.RemoveViewer(w); close(removed) }()
	select {
	case <-removed:
	case <-time.After(5 * time.Second):
		t.Fatal("a viewer whose join waited kept waiting as it left")
	}
	if len(conn.Send) != before || len(conn.Own) != 1 || w.joinQueued || w.joinTakenNow() {
		t.Fatalf("a viewer gone before its join went: %d messages queued, want %d", len(conn.Send), before)
	}
	conn.Close()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("a viewer kept waiting on a closed connection")
	}
}

// Viewers waiting for room in the host's queue get it in their order: a large
// frame is not held back for ever by small ones that another viewer keeps
// sending while the host drains slowly.
func TestViewersGetRoomInTheHostQueueInTheirOrder(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	small := relayedViewer(t, hs, conn, "0123456789abcdef")
	large := relayedViewer(t, hs, conn, "fedcba9876543210")
	piece := bytes.Repeat([]byte("s"), 4<<10)
	go func() {
		for hs.RelayToHost(small, proto.Frame{Type: proto.TypeInput, Payload: piece}) == nil {
		}
	}()
	defer hs.RemoveViewer(small)
	waitQueued(t, conn, hostViewerBytes-(1+proto.ViewerIDLen+1+len(piece)))
	// The host takes a message a millisecond.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
			select {
			case o := <-conn.Send:
				conn.Took(o)
			default:
			}
		}
	}()
	sent := make(chan error, 1)
	go func() {
		sent <- hs.RelayToHost(large, proto.Frame{Type: proto.TypeInput, Payload: bytes.Repeat([]byte("L"), proto.MaxInput)})
	}()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a 32 KiB frame waited 3 s for room while 4 KiB frames kept taking it")
	}
}

// A viewer whose connection fails, as a write to it does, stops waiting: a
// frame of its that waits for room in the host's queue stops waiting, and so
// does any later one that would wait. It is not closed: a frame it sent
// before its connection closed still goes when it needs no wait, until
// whoever serves it lets it go; then nothing of its goes.
func TestAViewerWhoseWriteFailsStopsWaitingForTheHost(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	v := relayedViewer(t, hs, conn, "0123456789abcdef")
	sent := fillFromViewer(hs, v, 2*hostQueueBytes)
	waitQueued(t, conn, hostViewerBytes-(1+proto.ViewerIDLen+1+proto.MaxInput))
	hs.HostRelayFrame(v.ID, proto.Frame{Type: proto.TypeOutput, Payload: []byte("o")})
	pumped := make(chan struct{})
	go func() { v.Pump(failSink{}); close(pumped) }()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("a viewer whose connection failed kept waiting for room")
	}
	<-pumped
	if v.Reason() != nil || !v.Failed() {
		t.Fatalf("a viewer whose write failed: closed %v, failed %v", v.Reason(), v.Failed())
	}
	whole := proto.Frame{Type: proto.TypeInput, Payload: bytes.Repeat([]byte("x"), proto.MaxInput)}
	if err := hs.RelayToHost(v, whole); !errors.Is(err, ErrViewerGone) {
		t.Fatalf("a frame that would wait, after the connection failed: %v", err)
	}
	line := proto.Frame{Type: proto.TypeInput, Payload: []byte("last")}
	// The host takes what is queued: a frame that needs no wait goes.
	for len(conn.Send) > 0 {
		conn.Took(<-conn.Send)
	}
	if err := hs.RelayToHost(v, line); err != nil {
		t.Fatalf("a frame read after the connection failed, with room for it: %v", err)
	}
	o := <-conn.Send
	conn.Took(o)
	if id, inner, err := proto.DecodeRelay(o.Binary[1:]); err != nil || id != v.ID || string(inner.Payload) != "last" {
		t.Fatalf("the host got %q %q: %v", id, inner.Payload, err)
	}
	// Once let go, nothing of the viewer's goes, room or not.
	v.close(session.ErrRevoked)
	if err := hs.RelayToHost(v, line); !errors.Is(err, ErrViewerGone) || len(conn.Send) != 0 {
		t.Fatalf("a frame after the viewer was let go: %v, %d queued", err, len(conn.Send))
	}
}

// failSink fails every write.
type failSink struct{}

func (failSink) WriteFrame([]byte) error { return errors.New("connection reset") }
func (failSink) Close(error)             {}

// Viewers coming and going while the viewers' share of the host's queue is
// full, and the host reads slowly, never close the host's connection: each
// join and leave waits for room in its turn, as the viewer's other messages
// do. The host hears no viewer leave that it did not hear join, and every
// viewer it heard join it hears leave.
func TestViewersComingAndGoingNeverCloseTheHost(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	v := relayedViewer(t, hs, conn, "0123456789abcdef")
	flooded := fillFromViewer(hs, v, 2*hostQueueBytes)
	waitQueued(t, conn, hostViewerBytes-(1+proto.ViewerIDLen+1+proto.MaxInput))
	var seenMu sync.Mutex
	seen := map[string]string{}
	record := func(o Outbound) {
		conn.Took(o)
		if o.Text == nil {
			return
		}
		var m struct{ T, ViewerID string }
		if json.Unmarshal(o.Text, &m) != nil || !strings.HasPrefix(m.ViewerID, "c") {
			return
		}
		seenMu.Lock()
		defer seenMu.Unlock()
		switch m.T {
		case proto.HostViewerJoin:
			seen[m.ViewerID] = "joined"
		case proto.HostViewerLeave:
			if seen[m.ViewerID] != "joined" {
				t.Errorf("%s left before it joined", m.ViewerID)
			}
			seen[m.ViewerID] = "left"
		}
	}
	// The host reads a message a millisecond.
	stop := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
			select {
			case o := <-conn.Send:
				record(o)
			default:
			}
		}
	}()
	// More joins and leaves than the rest of the queue holds, by as many
	// viewers at a time as the session takes.
	const churn = 4500
	var wg sync.WaitGroup
	var next, joined atomic.Int64
	for range hs.maxViewers - 1 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := next.Add(1)
				if i > churn {
					return
				}
				w, err := hs.AddViewer(fmt.Sprintf("c%015d", i), session.RoleView, "", "")
				if err != nil {
					t.Errorf("viewer %d: %v", i, err)
					return
				}
				joined.Add(1)
				hs.RemoveViewer(w)
			}
		}()
	}
	churned := make(chan struct{})
	go func() { wg.Wait(); close(churned) }()
	select {
	case <-churned:
	case <-time.After(60 * time.Second):
		t.Fatalf("%d of %d viewers came and went", joined.Load(), churn)
	}
	select {
	case <-conn.Done():
		t.Fatal("viewers coming and going closed the host's connection")
	default:
	}
	<-flooded
	hs.RemoveViewer(v)
	close(stop)
	<-drained
	for len(conn.Send) > 0 {
		record(<-conn.Send)
	}
	select {
	case <-conn.Done():
		t.Fatal("viewers coming and going closed the host's connection")
	default:
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	for id, s := range seen {
		if s != "left" {
			t.Fatalf("%s joined and was never told to have left", id)
		}
	}
	if joined.Load() != churn {
		t.Fatalf("%d of %d viewers let in", joined.Load(), churn)
	}
	t.Logf("%d of %d viewers' joins went to the host", len(seen), churn)
}

// The frames queued to a viewer are bounded in bytes as well as in number:
// one that falls further behind is closed as too slow, and the frames it
// was owed leave the count as Pump takes them.
func TestViewerQueueIsBoundedInBytes(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	v, err := hs.AddViewer("0123456789abcdef", session.RoleView, "", "")
	if err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	out := bytes.Repeat([]byte("o"), proto.MaxOutput)
	pushed := 0
	for v.Reason() == nil && pushed < 2*viewerQueue {
		hs.HostRelayFrame(v.ID, proto.Frame{Type: proto.TypeOutput, Payload: out})
		pushed++
	}
	if !errors.Is(v.Reason(), ErrSlowViewer) {
		t.Fatalf("after %d frames: %v", pushed, v.Reason())
	}
	if limit := viewerQueueBytes/(1+len(out)) + 1; pushed > limit {
		t.Fatalf("closed after %d frames of %d bytes, want at most %d", pushed, 1+len(out), limit)
	}
	if q := v.queued.Load(); q > viewerQueueBytes || q != int64(len(v.Out)*(1+len(out))) {
		t.Fatalf("%d bytes counted for %d frames queued", q, len(v.Out))
	}

	// A viewer that reads keeps up: frames written leave the count.
	w, err := hs.AddViewer("fedcba9876543210", session.RoleView, "", "")
	if err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	sink := &countSink{}
	done := make(chan struct{})
	go func() { w.Pump(sink); close(done) }()
	for i := 0; i < 2*viewerQueueBytes/(1+len(out)); i++ {
		hs.HostRelayFrame(w.ID, proto.Frame{Type: proto.TypeOutput, Payload: out})
		for w.queued.Load() > viewerQueueBytes/2 && w.Reason() == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if w.Reason() != nil {
		t.Fatalf("a viewer that keeps up was closed: %v", w.Reason())
	}
	want := int64(2 * viewerQueueBytes / (1 + len(out)))
	deadline := time.Now().Add(5 * time.Second)
	for sink.frames.Load() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := sink.frames.Load(); got != want || w.queued.Load() != 0 {
		t.Fatalf("%d of %d frames written, %d bytes left counted", got, want, w.queued.Load())
	}
	hs.RemoveViewer(w)
	<-done
}

// countSink counts the frames written to it and keeps none.
type countSink struct{ frames atomic.Int64 }

func (s *countSink) WriteFrame([]byte) error { s.frames.Add(1); return nil }
func (s *countSink) Close(error)             {}

// A viewer is let in while its join is still being charged, so whoever
// serves it goes on (and notices it leave); its next message to the host
// waits for the join and goes after it. One that leaves before its join
// went stops the charge and sends neither join nor leave.
func TestAViewersJoinGoesFirstAndHoldsNothingUp(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	release := make(chan struct{})
	charging := func(stop <-chan struct{}, n int) error {
		select {
		case <-release:
			return nil
		case <-stop:
			return ErrViewerGone
		}
	}
	v, err := hs.AddViewerWith(ViewerOptions{ID: "0123456789abcdef", Role: session.RoleControl, ChargeJoin: charging})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- hs.StartRelay(v) }()
	time.Sleep(50 * time.Millisecond)
	if len(conn.Send) != 0 {
		t.Fatalf("%d messages went before the join was charged", len(conn.Send))
	}
	close(release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	drainText(t, conn, proto.HostRelayStart)

	w, err := hs.AddViewerWith(ViewerOptions{ID: "fedcba9876543210", Role: session.RoleView, ChargeJoin: func(stop <-chan struct{}, n int) error {
		<-stop
		return ErrViewerGone
	}})
	if err != nil {
		t.Fatal(err)
	}
	gone := make(chan struct{})
	go func() { hs.RemoveViewer(w); close(gone) }()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("a viewer whose join was being charged could not leave")
	}
	if len(conn.Send) != 0 {
		t.Fatalf("a viewer gone before its join sent %d messages", len(conn.Send))
	}
}

// A viewer's Charge is told what each of its messages brings the host's
// connection: a relayed frame in its envelope (eighteen bytes for an empty
// INPUT of one), an offer as encoded for the host (six bytes for each '<'
// of the viewer's); a Charge that fails drops the message, and its error
// comes back. Its ChargeJoin is told its join and its leave together,
// before the join goes; one that fails sends neither.
func TestAViewersChargeIsToldWhatGoesToTheHost(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	var told []int
	var refuse error
	joinTold := make(chan int, 1)
	v, err := hs.AddViewerWith(ViewerOptions{ID: "0123456789abcdef", Role: session.RoleControl, Charge: func(n int) error {
		told = append(told, n)
		return refuse
	}, ChargeJoin: func(_ <-chan struct{}, n int) error {
		joinTold <- n
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-conn.Send:
		conn.Took(o)
		// The leave as it is at most: with the longest reason.
		leave, _ := jsonMarshal(proto.ViewerLeave{T: proto.HostViewerLeave, ViewerID: v.ID, Reason: proto.LeaveRevoked})
		if tt, _ := proto.ParseHeader(o.Text); tt != proto.HostViewerJoin || <-joinTold != o.size()+len(leave) {
			t.Fatalf("the join: %q, ChargeJoin not told it and its leave", o.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("no join")
	}
	if len(told) != 0 {
		t.Fatalf("the join was charged as a message of the read loop's: %v", told)
	}
	took := func(what string) Outbound {
		t.Helper()
		select {
		case o := <-conn.Send:
			conn.Took(o)
			if len(told) == 0 || told[len(told)-1] != o.size() {
				t.Fatalf("%s: %d bytes went to the host, Charge was told %v", what, o.size(), told)
			}
			return o
		case <-time.After(time.Second):
			t.Fatalf("%s: nothing went to the host", what)
		}
		return Outbound{}
	}
	if err := hs.StartRelay(v); err != nil {
		t.Fatal(err)
	}
	took("relay_start")
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeInput}); err != nil {
		t.Fatal(err)
	}
	if o := took("an empty input"); o.size() != 1+proto.ViewerIDLen+1 {
		t.Fatalf("an empty input took %d bytes to the host", o.size())
	}
	sdp := strings.Repeat("<", 1000)
	if err := hs.ForwardOffer(v, sdp); err != nil {
		t.Fatal(err)
	}
	if o := took("an offer"); o.size() < 6*len(sdp) {
		t.Fatalf("an offer of %d bytes took %d to the host", len(sdp), o.size())
	}
	if err := hs.ForwardICE(v, proto.ICECandidate{Candidate: "candidate:1 1 udp 1 127.0.0.1 9 typ host"}); err != nil {
		t.Fatal(err)
	}
	took("a candidate")
	refuse = errors.New("over budget")
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeInput, Payload: []byte("x")}); !errors.Is(err, refuse) {
		t.Fatalf("relay past a failing Charge: %v", err)
	}
	if err := hs.ForwardOffer(v, "v=0"); !errors.Is(err, refuse) {
		t.Fatalf("offer past a failing Charge: %v", err)
	}
	if q := conn.queued(); q != 0 || len(conn.Send) != 0 {
		t.Fatalf("%d bytes in %d messages went past a failing Charge", q, len(conn.Send))
	}
}

// nextToHost is the next message the server sends the host on conn, as
// its writer takes it (Next).
func nextToHost(t *testing.T, conn *HostConn) map[string]any {
	t.Helper()
	o, ok := takeWithin(conn, 2*time.Second)
	if !ok {
		t.Fatal("nothing went to the host")
	}
	var m map[string]any
	if err := json.Unmarshal(o.Text, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A link a viewer joined through that no longer opens the session is named
// to the host (link_gone), ahead of the leaves of its viewers still
// attached, and also once they have all left; a link no viewer joined
// through is never named, nor one twice. (A host away is not told: it
// counts every link gone as it loses its connection.)
func TestTheHostIsToldOfTheLinksItsViewersJoinedThroughGoing(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	next := func() map[string]any { return nextToHost(t, conn) }
	attached, err := hs.AddViewerWith(ViewerOptions{ID: "aaaaaaaaaaaaaaaa", Role: session.RoleControl, LinkID: "l1"})
	if err != nil {
		t.Fatal(err)
	}
	if m := next(); m["t"] != proto.HostViewerJoin {
		t.Fatalf("the join: %v", m)
	}
	left, err := hs.AddViewerWith(ViewerOptions{ID: "bbbbbbbbbbbbbbbb", Role: session.RoleControl, LinkID: "l2"})
	if err != nil {
		t.Fatal(err)
	}
	if m := next(); m["t"] != proto.HostViewerJoin {
		t.Fatalf("the join: %v", m)
	}
	hs.RemoveViewer(left)
	if m := next(); m["t"] != proto.HostViewerLeave {
		t.Fatalf("the leave: %v", m)
	}

	hs.DisconnectLink("unrelated")
	hs.DisconnectLink("l2")
	if m := next(); m["t"] != proto.HostLinkGone || m["linkId"] != "l2" {
		t.Fatalf("for the link of a viewer that left: %v", m)
	}
	hs.DisconnectLink("l1")
	if m := next(); m["t"] != proto.HostLinkGone || m["linkId"] != "l1" {
		t.Fatalf("for the link of a viewer attached: %v", m)
	}
	hs.RemoveViewer(attached)
	if m := next(); m["t"] != proto.HostViewerLeave || m["reason"] != proto.LeaveRevoked {
		t.Fatalf("the attached viewer's leave: %v", m)
	}
	hs.DisconnectLink("l1")
	if o, ok := takeWithin(conn, 100*time.Millisecond); ok {
		t.Fatalf("a second revoke sent %s", o.Text)
	}
}

// A joined link's expiry is timed on the server: the host is told the link
// went as it expires, also once its viewer has left (whose own lapse ended
// with it), whatever the host's clock says.
func TestTheHostIsToldOfAJoinedLinksExpiry(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	until := time.Now().Add(300 * time.Millisecond)
	v, err := hs.AddViewerWith(ViewerOptions{ID: "aaaaaaaaaaaaaaaa", Role: session.RoleControl, LinkID: "l1", Authorize: func() (session.Grant, error) {
		return session.Grant{Role: session.RoleControl, Until: until}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if m := nextToHost(t, conn); m["t"] != proto.HostViewerJoin {
		t.Fatalf("the join: %v", m)
	}
	hs.RemoveViewer(v) // a reply box, gone once it has sent
	if m := nextToHost(t, conn); m["t"] != proto.HostViewerLeave || m["reason"] != nil {
		t.Fatalf("the leave: %v", m)
	}
	m := nextToHost(t, conn)
	if m["t"] != proto.HostLinkGone || m["linkId"] != "l1" {
		t.Fatalf("at the expiry: %v", m)
	}
	if time.Now().Before(until) {
		t.Fatal("told before the expiry")
	}
}

// Past maxLinksJoined links, the oldest is pushed out of what the server
// keeps of them, and the host is told it went then: a later end of it would
// not reach the host.
func TestALinkPushedOutOfTheJoinedOnesIsGoneToTheHost(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	hs.mu.Lock()
	for i := range maxLinksJoined {
		hs.joinedThrough(fmt.Sprintf("l%d", i), time.Time{})
	}
	hs.mu.Unlock()
	if _, err := hs.AddViewerWith(ViewerOptions{ID: "aaaaaaaaaaaaaaaa", Role: session.RoleControl, LinkID: "newest"}); err != nil {
		t.Fatal(err)
	}
	if m := nextToHost(t, conn); m["t"] != proto.HostLinkGone || m["linkId"] != "l0" {
		t.Fatalf("for the oldest: %v", m)
	}
	if m := nextToHost(t, conn); m["t"] != proto.HostViewerJoin {
		t.Fatalf("the join: %v", m)
	}
	hs.mu.Lock()
	n := len(hs.linksJoined)
	hs.mu.Unlock()
	if n != maxLinksJoined {
		t.Fatalf("%d links kept", n)
	}
}

// A viewer's leave tells the host why when its link no longer opens the
// session: revoked (a revoke, its run's link forgotten), expired (its link's
// lapse); a viewer that left, or was too slow, gets a leave with no reason,
// as an older server's. The first reason the viewer was closed for counts.
func TestAViewersLeaveSaysWhenItsLinkWent(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	soon := time.Now().Add(50 * time.Millisecond)
	add := func(id, link string, until time.Time) *Viewer {
		t.Helper()
		v, err := hs.AddViewerWith(ViewerOptions{ID: id, Role: session.RoleControl, LinkID: link, Authorize: func() (session.Grant, error) {
			return session.Grant{Role: session.RoleControl, Until: until}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		drainText(t, conn, proto.HostViewerJoin)
		return v
	}
	revoked := add("aaaaaaaaaaaaaaaa", "l1", time.Time{})
	expired := add("bbbbbbbbbbbbbbbb", "l2", soon)
	left := add("cccccccccccccccc", "l3", time.Time{})
	slow := add("dddddddddddddddd", "l4", time.Time{})
	leftThenRevoked := add("eeeeeeeeeeeeeeee", "l5", time.Time{})
	hs.DisconnectLink("l1")
	<-expired.Done()
	slow.close(ErrSlowViewer)
	leftThenRevoked.close(nil)
	hs.DisconnectLink("l5")
	// What goes to the host but the revokes' own link_gone
	// (TestTheHostIsToldOfTheLinksItsViewersJoinedThroughGoing).
	next := func() map[string]any {
		t.Helper()
		for {
			o, ok := takeWithin(conn, time.Second)
			if !ok {
				t.Fatal("no leave")
			}
			var m map[string]any
			if err := json.Unmarshal(o.Text, &m); err != nil {
				t.Fatal(err)
			}
			if m["t"] != proto.HostLinkGone {
				return m
			}
		}
	}
	for _, c := range []struct {
		v    *Viewer
		want string
	}{{revoked, proto.LeaveRevoked}, {expired, proto.LeaveExpired}, {left, ""}, {slow, ""}, {leftThenRevoked, ""}} {
		hs.RemoveViewer(c.v)
		m := next()
		if m["t"] != proto.HostViewerLeave || m["viewerId"] != c.v.ID {
			t.Fatalf("%s: %v", c.v.ID, m)
		}
		got, has := m["reason"].(string)
		if got != c.want || (c.want == "" && has) {
			t.Fatalf("%s: %v, want the reason %q", c.v.ID, m, c.want)
		}
	}
}

// A viewer whose principal is revoked (RevokeViewer: a credential that is
// not a link, so no link_gone tells the host) has the host told at once,
// before it is let go and whatever it was closed for first, that it was
// revoked; letting it go after sends no second leave. One let go before
// is told nothing more.
func TestARevokedPrincipalsLeaveGoesAtOnce(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	add := func(id string) *Viewer {
		t.Helper()
		v, err := hs.AddViewer(id, session.RoleControl, "", "")
		if err != nil {
			t.Fatal(err)
		}
		drainText(t, conn, proto.HostViewerJoin)
		return v
	}
	revocation := fmt.Errorf("access revoked by the identity provider: %w", session.ErrRevoked)
	for _, c := range []struct {
		name  string
		id    string
		first error
	}{{"open", "1111111111111111", nil}, {"too slow first", "2222222222222222", ErrSlowViewer}} {
		t.Run(c.name, func(t *testing.T) {
			v := add(c.id)
			if c.first != nil {
				v.close(c.first)
			}
			// The viewer is not let go: its connection's close is under way.
			hs.RevokeViewer(v, revocation)
			if m := nextToHost(t, conn); m["t"] != proto.HostViewerLeave || m["viewerId"] != v.ID || m["reason"] != proto.LeaveRevoked {
				t.Fatalf("%v, want viewer %s's leave with the reason %s", m, v.ID, proto.LeaveRevoked)
			}
			want := c.first
			if want == nil {
				want = revocation
			}
			if r := v.Reason(); r != want {
				t.Fatalf("closed for %v, want %v", r, want)
			}
			hs.RemoveViewer(v)
			if n := conn.queued(); n != 0 {
				t.Fatalf("%d bytes queued for the host once the viewer is let go: a second leave", n)
			}
		})
	}
	t.Run("let go before", func(t *testing.T) {
		v := add("3333333333333333")
		hs.RemoveViewer(v)
		if m := nextToHost(t, conn); m["t"] != proto.HostViewerLeave || m["viewerId"] != v.ID {
			t.Fatalf("%v, want viewer %s's leave", m, v.ID)
		}
		hs.RevokeViewer(v, revocation)
		if n := conn.queued(); n != 0 {
			t.Fatalf("%d bytes queued for the host for a viewer let go", n)
		}
	})
}
