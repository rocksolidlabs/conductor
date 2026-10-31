package signal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The two lanes of a host's queue (HostConn): the server's own messages go
// ahead of the viewers', a link's viewers are closed before the link's
// going is queued, and what a viewer closed for its link still has queued
// is dropped as the writer takes it, so the going never reaches the host
// ahead of anything of theirs that is written.

// taken is one message the writer took: its type (a text message's, or
// "relay"), and the viewer it names.
type taken struct{ t, viewer string }

func (m taken) String() string { return m.t + "/" + m.viewer }

// takenOf reads o as the host would.
func takenOf(t *testing.T, o Outbound) taken {
	t.Helper()
	if o.Binary != nil {
		id, _, err := proto.DecodeRelay(o.Binary[1:])
		if err != nil {
			t.Fatal(err)
		}
		return taken{"relay", id}
	}
	var m struct {
		T        string `json:"t"`
		ViewerID string `json:"viewerId"`
		LinkID   string `json:"linkId"`
	}
	if err := json.Unmarshal(o.Text, &m); err != nil {
		t.Fatal(err)
	}
	if m.T == proto.HostLinkGone {
		return taken{m.T, m.LinkID}
	}
	return taken{m.T, m.ViewerID}
}

// takeAll is what conn's writer takes until nothing more comes within d.
func takeAll(t *testing.T, conn *HostConn, d time.Duration) []taken {
	t.Helper()
	var got []taken
	for {
		o, ok := takeWithin(conn, d)
		if !ok {
			return got
		}
		got = append(got, takenOf(t, o))
	}
}

// linkViewer adds a controller joined through linkID, its join queued and
// not taken.
func linkViewer(t *testing.T, hs *HostedSession, id, linkID string) *Viewer {
	t.Helper()
	v, err := hs.AddViewerWith(ViewerOptions{ID: id, Role: session.RoleControl, LinkID: linkID})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// relayInput relays an INPUT frame of v's to the host.
func relayInput(t *testing.T, hs *HostedSession, v *Viewer, payload string) {
	t.Helper()
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeInput, Payload: []byte(payload)}); err != nil {
		t.Fatal(err)
	}
}

// removed lets v go on a goroutine of its own (RemoveViewer waits for its
// join to be taken), and returns a channel closed once it has.
func removed(hs *HostedSession, v *Viewer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		hs.RemoveViewer(v)
		close(done)
	}()
	return done
}

// A link's going is taken by the writer before every relayed frame queued
// before it: with the viewers' lane full, link_gone is the next message
// taken, then the viewers' frames in their order.
func TestServerOwnMessagesGoAheadOfRelayedFrames(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	guest := linkViewer(t, hs, "0123456789abcdef", "l1")
	drainText(t, conn, proto.HostViewerJoin)
	owner := relayedViewer(t, hs, conn, "fedcba9876543210")
	sent := fillFromViewer(hs, owner, 2*hostQueueBytes)
	waitQueued(t, conn, hostViewerBytes-(1+proto.ViewerIDLen+1+proto.MaxInput))
	waitInLine(t, conn, 1)

	hs.DisconnectLink("l1")
	o, ok := takeWithin(conn, time.Second)
	if !ok {
		t.Fatal("nothing taken")
	}
	if m := takenOf(t, o); m != (taken{proto.HostLinkGone, "l1"}) {
		t.Fatalf("taken first with the viewers' lane full: %v, want the link's going", m)
	}
	o, _ = takeWithin(conn, time.Second)
	if m := takenOf(t, o); m != (taken{"relay", owner.ID}) {
		t.Fatalf("then %v, want the owner's frames", m)
	}
	select {
	case <-conn.Done():
		t.Fatal("the host's connection was closed")
	default:
	}
	conn.Close()
	<-sent
	<-removed(hs, guest)
}

// A revoke closes the link's viewers before it queues the link's going, and
// so does the link's expiry, whichever of its timers fires first: as the
// writer can take link_gone, each viewer of the link is closed for it.
func TestViewersAreClosedBeforeLinkGoneIsQueued(t *testing.T) {
	check := func(t *testing.T, conn *HostConn, vs []*Viewer, want error) {
		t.Helper()
		select {
		case o := <-conn.Own:
			conn.Took(o)
			if m := takenOf(t, o); m.t != proto.HostLinkGone {
				t.Fatalf("the server's lane holds %v", m)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no link_gone")
		}
		for _, v := range vs {
			if !v.forLink.Load() || !errors.Is(v.Reason(), want) {
				t.Fatalf("%s as link_gone was queued: closed for its link %v, reason %v", v.ID, v.forLink.Load(), v.Reason())
			}
		}
	}
	t.Run("revoked", func(t *testing.T) {
		hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
		a := linkViewer(t, hs, "aaaaaaaaaaaaaaaa", "l1")
		b := linkViewer(t, hs, "bbbbbbbbbbbbbbbb", "l1")
		other := linkViewer(t, hs, "cccccccccccccccc", "l2")
		go hs.DisconnectLink("l1")
		check(t, conn, []*Viewer{a, b}, session.ErrRevoked)
		if other.Reason() != nil {
			t.Fatalf("another link's viewer: %v", other.Reason())
		}
	})
	t.Run("expired", func(t *testing.T) {
		for range 20 {
			hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
			until := time.Now().Add(20 * time.Millisecond)
			grant := func() (session.Grant, error) { return session.Grant{Role: session.RoleControl, Until: until}, nil }
			a, err := hs.AddViewerWith(ViewerOptions{ID: "aaaaaaaaaaaaaaaa", Role: session.RoleControl, LinkID: "l1", Authorize: grant})
			if err != nil {
				t.Fatal(err)
			}
			check(t, conn, []*Viewer{a}, session.ErrExpired)
		}
	})
}

// A viewer closed for its link has nothing of its still queued written: its
// join, its relay_start, its hello and frames queued before the revoke are
// dropped as the writer takes them, the link's going goes, and no leave
// follows the join that never went: the host, older or newer, never knows
// the viewer. One whose join was written before the close has the frames
// taken before then, the going, and its leave; what it had queued and not
// taken is dropped.
func TestAClosedViewersQueuedMessagesAreDropped(t *testing.T) {
	t.Run("join still queued", func(t *testing.T) {
		hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
		v := linkViewer(t, hs, "0123456789abcdef", "l1")
		if err := hs.StartRelay(v); err != nil {
			t.Fatal(err)
		}
		hello := proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: proto.ProtoVersion, Cols: 80, Rows: 24})
		if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeControl, Payload: hello[1:]}); err != nil {
			t.Fatal(err)
		}
		relayInput(t, hs, v, "typed")
		hs.DisconnectLink("l1")
		gone := removed(hs, v)
		got := takeAll(t, conn, 300*time.Millisecond)
		if len(got) != 1 || got[0] != (taken{proto.HostLinkGone, "l1"}) {
			t.Fatalf("the host was sent %v, want the link's going alone", got)
		}
		select {
		case <-gone:
		case <-time.After(5 * time.Second):
			t.Fatal("the viewer was never let go")
		}
		if got := takeAll(t, conn, 200*time.Millisecond); len(got) != 0 {
			t.Fatalf("then %v, want nothing: no leave for a join that never went", got)
		}
		if conn.queued() != 0 {
			t.Fatalf("%d bytes left counted", conn.queued())
		}
	})
	t.Run("join written", func(t *testing.T) {
		hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
		v := linkViewer(t, hs, "0123456789abcdef", "l1")
		if err := hs.StartRelay(v); err != nil {
			t.Fatal(err)
		}
		relayInput(t, hs, v, "before")
		var got []taken
		for range 3 { // the join, relay_start, the frame
			o, ok := takeWithin(conn, time.Second)
			if !ok {
				t.Fatal("nothing taken")
			}
			got = append(got, takenOf(t, o))
		}
		relayInput(t, hs, v, "queued")
		relayInput(t, hs, v, "queued too")
		hs.DisconnectLink("l1")
		gone := removed(hs, v)
		<-gone
		got = append(got, takeAll(t, conn, 300*time.Millisecond)...)
		want := []taken{{proto.HostViewerJoin, v.ID}, {proto.HostRelayStart, v.ID}, {"relay", v.ID}, {proto.HostLinkGone, "l1"}, {proto.HostViewerLeave, v.ID}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("the host was sent %v, want %v", got, want)
		}
	})
}

// A viewer that left with its join and a line still queued, and whose link
// goes (revoked, or expired on its own timer) before they are written, has
// none of them written: the going reaches the host alone, and no leave
// follows a join that never went, so a host never meets the viewer after
// the link's going. One whose join was written before has the going, then
// its leave, its queued line dropped.
func TestALeftViewersQueuedMessagesAreDroppedWhenItsLinkGoes(t *testing.T) {
	line := proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "queued line"})
	queueLine := func(t *testing.T, hs *HostedSession, v *Viewer) {
		t.Helper()
		if err := hs.StartRelay(v); err != nil {
			t.Fatal(err)
		}
		if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeControl, Payload: line[1:]}); err != nil {
			t.Fatal(err)
		}
	}
	for _, how := range []string{"revoked", "expired"} {
		t.Run(how+", its join still queued", func(t *testing.T) {
			hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
			opts := ViewerOptions{ID: "0123456789abcdef", Role: session.RoleControl, LinkID: "l1"}
			if how == "expired" {
				until := time.Now().Add(300 * time.Millisecond)
				opts.Authorize = func() (session.Grant, error) { return session.Grant{Role: session.RoleControl, Until: until}, nil }
			}
			v, err := hs.AddViewerWith(opts)
			if err != nil {
				t.Fatal(err)
			}
			queueLine(t, hs, v)
			gone := removed(hs, v) // it left: its join is waited for
			time.Sleep(50 * time.Millisecond)
			if how == "revoked" {
				hs.DisconnectLink("l1")
			} else {
				time.Sleep(400 * time.Millisecond)
			}
			got := takeAll(t, conn, 300*time.Millisecond)
			if len(got) != 1 || got[0] != (taken{proto.HostLinkGone, "l1"}) {
				t.Fatalf("the host was sent %v, want the link's going alone", got)
			}
			select {
			case <-gone:
			case <-time.After(5 * time.Second):
				t.Fatal("the viewer was never let go")
			}
			if got := takeAll(t, conn, 200*time.Millisecond); len(got) != 0 {
				t.Fatalf("then %v, want nothing", got)
			}
		})
	}
	t.Run("revoked, its join written", func(t *testing.T) {
		hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
		v := linkViewer(t, hs, "0123456789abcdef", "l1")
		drainText(t, conn, proto.HostViewerJoin)
		queueLine(t, hs, v)
		gone := removed(hs, v)
		<-gone // its leave is queued behind its relay_start and line
		hs.DisconnectLink("l1")
		got := takeAll(t, conn, 300*time.Millisecond)
		want := []taken{{proto.HostLinkGone, "l1"}, {proto.HostViewerLeave, v.ID}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("the host was sent %v, want %v", got, want)
		}
	})
}

// A viewer that left for no reason of its link's (it left, its connection
// failed) has what it sent before its leave written first, in order, and
// its leave after, with no reason, whatever the server's own lane holds.
func TestALeavingViewersAdmittedMessagesStillGo(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	v := linkViewer(t, hs, "0123456789abcdef", "l1")
	if err := hs.StartRelay(v); err != nil {
		t.Fatal(err)
	}
	relayInput(t, hs, v, "one")
	relayInput(t, hs, v, "two")
	gone := removed(hs, v)
	got := takeAll(t, conn, 300*time.Millisecond)
	<-gone
	got = append(got, takeAll(t, conn, 200*time.Millisecond)...)
	want := []taken{{proto.HostViewerJoin, v.ID}, {proto.HostRelayStart, v.ID}, {"relay", v.ID}, {"relay", v.ID}, {proto.HostViewerLeave, v.ID}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the host was sent %v, want %v", got, want)
	}
}

// A quick reply (a viewer that joins through a link, sends its line and
// leaves at once) under a congested writer: its join and line, admitted
// behind a flood of another viewer's frames, reach the host before its
// leave, the writer taking a message a millisecond.
func TestQuickReplyReachesTheHostUnderACongestedWriter(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	owner := relayedViewer(t, hs, conn, "fedcba9876543210")
	flood := bytes.Repeat([]byte("f"), proto.MaxInput)
	for range 64 {
		if err := hs.RelayToHost(owner, proto.Frame{Type: proto.TypeInput, Payload: flood}); err != nil {
			t.Fatal(err)
		}
	}
	reply := linkViewer(t, hs, "0123456789abcdef", "l1")
	if err := hs.StartRelay(reply); err != nil {
		t.Fatal(err)
	}
	line := proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "yes"})
	if err := hs.RelayToHost(reply, proto.Frame{Type: proto.TypeControl, Payload: line[1:]}); err != nil {
		t.Fatal(err)
	}
	gone := removed(hs, reply)
	var got []taken
	deadline := time.After(10 * time.Second)
	for {
		time.Sleep(time.Millisecond)
		if o, ok := takeWithin(conn, 500*time.Millisecond); ok {
			if m := takenOf(t, o); m.viewer == reply.ID {
				got = append(got, m)
				if m.t == proto.HostViewerLeave {
					break
				}
			}
			continue
		}
		select {
		case <-deadline:
			t.Fatalf("the reply's messages so far: %v", got)
		default:
		}
	}
	<-gone
	want := []taken{{proto.HostViewerJoin, reply.ID}, {proto.HostRelayStart, reply.ID}, {"relay", reply.ID}, {proto.HostViewerLeave, reply.ID}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the host was sent %v of the reply's, want %v", got, want)
	}
}

// A viewer's leave that says why (its link revoked or expired) goes in the
// server's own lane, ahead of the viewers' lane, as the link's going does:
// with the viewers' lane full, the expiry's link_gone and the viewer's
// leave{expired} are the next two taken, in either order. The grant's end
// is far off and the link is let lapse through the hub's own lapse
// (linkLapsed, what its timer calls) once the lane is full: the link's and
// the viewer's timers, drawn on the wall clock, fire in no set order, and a
// viewer's leave taken before the link's lapse ran had the next message
// taken a viewer's frame (#281).
func TestReasonedLeaveInTheServerLane(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	until := time.Now().Add(time.Hour)
	guest, err := hs.AddViewerWith(ViewerOptions{ID: "0123456789abcdef", Role: session.RoleControl, LinkID: "l1",
		Authorize: func() (session.Grant, error) { return session.Grant{Role: session.RoleControl, Until: until}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	owner := relayedViewer(t, hs, conn, "fedcba9876543210")
	sent := fillFromViewer(hs, owner, 2*hostQueueBytes)
	waitQueued(t, conn, hostViewerBytes-(1+proto.ViewerIDLen+1+proto.MaxInput))
	hs.linkLapsed("l1")
	<-guest.Done()
	if !errors.Is(guest.Reason(), session.ErrExpired) {
		t.Fatalf("the viewer was let go for %v, want expired", guest.Reason())
	}
	gone := removed(hs, guest)
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the viewer was never let go")
	}
	var got []string
	for range 2 {
		o, ok := takeWithin(conn, time.Second)
		if !ok {
			t.Fatal("nothing taken")
		}
		var m map[string]any
		if err := json.Unmarshal(o.Text, &m); err != nil {
			t.Fatalf("taken %d bytes of the viewers' lane: %v", o.size(), err)
		}
		got = append(got, fmt.Sprint(m["t"], "/", m["reason"]))
	}
	if !(got[0] == "link_gone/<nil>" && got[1] == "viewer_leave/expired") && !(got[1] == "link_gone/<nil>" && got[0] == "viewer_leave/expired") {
		t.Fatalf("taken first: %v, want the link's going and the leave that says why", got)
	}
	conn.Close()
	<-sent
}

// The viewers' messages keep their order among themselves, whatever the
// server's own messages that go ahead of them: three viewers relay numbered
// frames while the server queues its own, and the writer takes each
// viewer's frames in the order they were relayed, every one of them.
func TestViewersMessagesKeepTheirOrder(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	vs := []*Viewer{
		relayedViewer(t, hs, conn, "aaaaaaaaaaaaaaaa"),
		relayedViewer(t, hs, conn, "bbbbbbbbbbbbbbbb"),
		relayedViewer(t, hs, conn, "cccccccccccccccc"),
	}
	const each = 2000
	var wg sync.WaitGroup
	for _, v := range vs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeInput, Payload: fmt.Appendf(nil, "%d", i)}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	stop := make(chan struct{})
	own := make(chan int, 1)
	go func() {
		n := 0
		defer func() { own <- n }()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if conn.send(Outbound{Text: []byte(`{"t":"own"}`)}) {
				n++
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()
	next := map[string]int{}
	owns := 0
	for total := 0; total < len(vs)*each; {
		o, ok := takeWithin(conn, 5*time.Second)
		if !ok {
			t.Fatalf("%d of %d frames taken", total, len(vs)*each)
		}
		if o.Binary == nil {
			owns++
			continue
		}
		id, inner, err := proto.DecodeRelay(o.Binary[1:])
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprint(next[id]); string(inner.Payload) != want {
			t.Fatalf("%s's frame %q taken, want %s", id, inner.Payload, want)
		}
		next[id]++
		total++
	}
	close(stop)
	wg.Wait()
	sentOwn := <-own
	for owns < sentOwn {
		if _, ok := takeWithin(conn, time.Second); !ok {
			t.Fatalf("%d of the server's %d own messages taken", owns, sentOwn)
		}
		owns++
	}
	select {
	case <-conn.Done():
		t.Fatal("the host's connection was closed")
	default:
	}
}

// The server's own lane holds at most hostOwnSlots messages and
// hostOwnBytes: one more closes the connection, as a host too slow to keep,
// whatever the viewers' lane holds. A full viewers' lane leaves it all of
// its room.
func TestServerLaneFullClosesTheConnection(t *testing.T) {
	t.Run("messages", func(t *testing.T) {
		_, conn := register(t, NewHub(session.NewRegistry(4), nil))
		filler := Outbound{Text: []byte(`{"t":"filler"}`)}
		for len(conn.Send) < cap(conn.Send) {
			if err := conn.sendFrom(filler, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		for i := range hostOwnSlots {
			if !conn.send(Outbound{Text: []byte(`{"t":"own"}`)}) {
				t.Fatalf("the server's own message %d was refused", i)
			}
		}
		select {
		case <-conn.Done():
			t.Fatal("closed with room in the server's lane")
		default:
		}
		if conn.send(Outbound{Text: []byte(`{"t":"own"}`)}) {
			t.Fatal("a message past the lane's slots was queued")
		}
		select {
		case <-conn.Done():
		default:
			t.Fatal("a full server's lane left the connection open")
		}
	})
	t.Run("bytes", func(t *testing.T) {
		_, conn := register(t, NewHub(session.NewRegistry(4), nil))
		big := Outbound{Text: bytes.Repeat([]byte("b"), hostOwnBytes/4)}
		for i := range 4 {
			if !conn.send(big) {
				t.Fatalf("the server's own message %d was refused", i)
			}
		}
		if conn.send(Outbound{Text: []byte(`{}`)}) {
			t.Fatal("a message past the lane's bytes was queued")
		}
		select {
		case <-conn.Done():
		default:
			t.Fatal("a full server's lane left the connection open")
		}
	})
}
