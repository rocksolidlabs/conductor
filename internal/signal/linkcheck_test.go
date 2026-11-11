package signal

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// drainHost takes what the session sends its host, as its writer does
// (Next), for as long as the test runs.
func drainHost(t *testing.T, conn *HostConn) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			if _, ok := conn.Next(ctx); !ok {
				return
			}
		}
	}()
}

// A viewer whose link no longer opens the session is refused under the
// session's lock, before the host hears of it; the role the check gives is
// the one the viewer gets.
func TestAddViewerChecksTheLinkAgain(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	v, err := hs.AddViewerWith(ViewerOptions{ID: "0123456789abcdef", Role: session.RoleControl, LinkID: "l1",
		Authorize: func() (session.Grant, error) { return session.Grant{}, session.ErrRevoked }})
	if !errors.Is(err, session.ErrRevoked) || v != nil {
		t.Fatalf("revoked link: %v %v", v, err)
	}
	if _, err := hs.AddViewerWith(ViewerOptions{ID: "0123456789abcdee", Role: session.RoleControl, LinkID: "l1",
		Authorize: func() (session.Grant, error) { return session.Grant{}, session.ErrExpired }}); !errors.Is(err, session.ErrExpired) {
		t.Fatalf("expired link: %v", err)
	}
	if len(conn.Send)+len(conn.Own) != 0 {
		t.Fatal("the host heard of a refused viewer")
	}
	if n := hs.Info().Viewers; n != 0 {
		t.Fatalf("viewers %d", n)
	}
	v, err = hs.AddViewerWith(ViewerOptions{ID: "fedcba9876543210", Role: session.RoleControl, LinkID: "l2",
		Authorize: func() (session.Grant, error) { return session.Grant{Role: session.RoleView}, nil }})
	if err != nil || v.Role != session.RoleView {
		t.Fatalf("view grant: %+v %v", v, err)
	}
	drainText(t, conn, proto.HostViewerJoin)
}

// A revoke and a viewer's arrival that race end one way or the other, never
// with the viewer registered through the revoked link: either AddViewerWith
// refuses it or DisconnectLink closes it (go test -race; many rounds).
func TestHostedRevokeAndAddViewerRace(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	drainHost(t, conn)
	const rounds = 500
	var refused, closed int
	for i := range rounds {
		var revoked atomic.Bool
		linkID := fmt.Sprintf("race-%d", i)
		check := func() (session.Grant, error) {
			if revoked.Load() {
				return session.Grant{}, session.ErrRevoked
			}
			return session.Grant{Role: session.RoleControl}, nil
		}
		wait := func(mine bool) {
			if mine {
				for range i % 64 {
					runtime.Gosched()
				}
			}
		}
		var v *Viewer
		var err error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() {
			<-start
			wait(i%2 == 1)
			v, err = hs.AddViewerWith(ViewerOptions{ID: fmt.Sprintf("%016x", i), Role: session.RoleControl, LinkID: linkID, Authorize: check})
		})
		wg.Go(func() {
			<-start
			wait(i%2 == 0)
			// As a revoke does: the mark, then the close.
			revoked.Store(true)
			hs.DisconnectLink(linkID)
		})
		close(start)
		wg.Wait()
		if err != nil {
			if !errors.Is(err, session.ErrRevoked) {
				t.Fatalf("round %d: %v", i, err)
			}
			refused++
			continue
		}
		if !errors.Is(v.Reason(), session.ErrRevoked) {
			t.Fatalf("round %d: the viewer added through the revoked link stays open (reason %v)", i, v.Reason())
		}
		closed++
		hs.RemoveViewer(v)
	}
	t.Logf("%d rounds: refused %d, closed by the revoke %d", rounds, refused, closed)
}

// A hosted session that leaves the registry closes every viewer as at its
// end and takes none after.
func TestHostedRetireClosesEveryViewerAndRefusesMore(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	drainHost(t, conn)
	v, err := hs.AddViewer("0123456789abcdef", session.RoleView, "l1", "")
	if err != nil {
		t.Fatal(err)
	}
	hs.Retire()
	if !errors.Is(v.Reason(), session.ErrSessionEnded) {
		t.Fatalf("reason %v", v.Reason())
	}
	if _, err := hs.AddViewer("fedcba9876543210", session.RoleView, "l1", ""); !errors.Is(err, session.ErrSessionEnded) {
		t.Fatalf("add after retiring: %v", err)
	}
	hs.RemoveViewer(v)
}

// A revoke closes a link's viewers as revoked, written nothing more, while
// the hosted session runs and once it has ended (a run's link keeps its
// viewers on a member that ended); the session's end closes the viewers of
// its own links as its end, as a server session's are, written what was
// queued for them first.
func TestHostedDisconnectLinkIsARevokeAndEndLinkTheSessionsEnd(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	drainHost(t, conn)
	queued := func(id string) *Viewer {
		t.Helper()
		v, err := hs.AddViewer(id, session.RoleView, "l1", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range []byte("abc") {
			v.push([]byte{proto.TypeOutput, b})
		}
		return v
	}

	running := queued("0123456789abcdef")
	hs.DisconnectLink("l1")
	sink := &pumpSink{}
	running.Pump(sink)
	if !errors.Is(sink.reason, session.ErrRevoked) || sink.written() != 0 {
		t.Fatalf("a running session's link: closed with %v after %d frames", sink.reason, sink.written())
	}
	hs.RemoveViewer(running)

	hs.HostStatus(session.StatusExited, nil)
	revoked := queued("fedcba9876543210")
	hs.DisconnectLink("l1")
	sink = &pumpSink{}
	revoked.Pump(sink)
	if !errors.Is(sink.reason, session.ErrRevoked) || sink.written() != 0 {
		t.Fatalf("a revoke once ended: closed with %v after %d frames", sink.reason, sink.written())
	}
	hs.RemoveViewer(revoked)

	ended := queued("0123456789abcdee")
	hs.EndLink("l1")
	sink = &pumpSink{}
	ended.Pump(sink)
	if !errors.Is(sink.reason, session.ErrSessionEnded) || sink.written() != 3 {
		t.Fatalf("the session's end: closed with %v after %d of 3 frames", sink.reason, sink.written())
	}
	hs.RemoveViewer(ended)

	// A revoke that lands as the end's close is still written stops it.
	cut := queued("0123456789abcded")
	hs.EndLink("l1")
	hs.DisconnectLink("l1")
	sink = &pumpSink{}
	cut.Pump(sink)
	if !errors.Is(sink.reason, session.ErrSessionEnded) || sink.written() != 0 {
		t.Fatalf("a revoke after the end's close: closed with %v after %d frames", sink.reason, sink.written())
	}
	hs.RemoveViewer(cut)
}

// What a viewer is still written as it is closed (the session's end, its
// host's going) stops once its link no longer opens the session: as the
// link expires, and as it is revoked after the host has gone.
func TestALinkThatStopsOpeningTheSessionStopsWhatItsViewersAreOwed(t *testing.T) {
	queued := func(t *testing.T, hs *HostedSession, until time.Time) *Viewer {
		t.Helper()
		v, err := hs.AddViewerWith(ViewerOptions{ID: "0123456789abcdef", Role: session.RoleView, LinkID: "l1",
			Authorize: func() (session.Grant, error) { return session.Grant{Role: session.RoleView, Until: until}, nil }})
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range []byte("abc") {
			v.push([]byte{proto.TypeOutput, b})
		}
		return v
	}
	t.Run("expired during the end's", func(t *testing.T) {
		hub := NewHub(session.NewRegistry(4), nil)
		hs, conn := register(t, hub)
		drainHost(t, conn)
		// The link expires once the end has closed the viewer: its lapse,
		// set for an hour on, is made to fire then, not left to a clock
		// that a slow end could outrun.
		v := queued(t, hs, time.Now().Add(time.Hour))
		hs.HostStatus(session.StatusExited, nil)
		hs.EndLink("l1")
		if !errors.Is(v.Reason(), session.ErrSessionEnded) {
			t.Fatalf("the end closed the viewer with %v", v.Reason())
		}
		hs.mu.Lock()
		lapse := v.lapse
		hs.mu.Unlock()
		// Still set after the end: an end that stopped it would leave the
		// link's expiry never reaching the viewer it is draining.
		if !lapse.Reset(0) {
			t.Fatal("the session's end stopped its link's expiry")
		}
		for deadline := time.Now().Add(5 * time.Second); !v.revoked.Load(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the link's expiry never reached the viewer")
			}
		}
		sink := &pumpSink{}
		v.Pump(sink)
		if !errors.Is(sink.reason, session.ErrSessionEnded) || sink.written() != 0 {
			t.Fatalf("closed with %v after %d frames", sink.reason, sink.written())
		}
		hs.RemoveViewer(v)
	})
	t.Run("revoked after the host went", func(t *testing.T) {
		hub := NewHub(session.NewRegistry(4), nil)
		hs, conn := register(t, hub)
		drainHost(t, conn)
		v := queued(t, hs, time.Time{})
		hs.HostDisconnected(conn)
		hs.DisconnectLink("l1")
		sink := &pumpSink{}
		v.Pump(sink)
		if !errors.Is(sink.reason, ErrHostGone) || sink.written() != 0 {
			t.Fatalf("closed with %v after %d frames", sink.reason, sink.written())
		}
		hs.RemoveViewer(v)
		if len(hs.gone) != 0 {
			t.Fatal("a removed viewer is still kept")
		}
	})
	// A revoke finds a session's viewers through the server's registry,
	// which the session leaves as it retires: none is written more then.
	t.Run("the session left the server", func(t *testing.T) {
		hub := NewHub(session.NewRegistry(4), nil)
		hs, conn := register(t, hub)
		drainHost(t, conn)
		ended := queued(t, hs, time.Time{})
		hs.HostStatus(session.StatusExited, nil)
		hs.EndLink("l1")
		hs.Retire()
		sink := &pumpSink{}
		ended.Pump(sink)
		if !errors.Is(sink.reason, session.ErrSessionEnded) || sink.written() != 0 {
			t.Fatalf("closed with %v after %d frames", sink.reason, sink.written())
		}
		hs.RemoveViewer(ended)
	})
	// One let go (its reader ended first) is found by no revoke or expiry
	// after: it is written nothing more.
	t.Run("removed as the end's close is written", func(t *testing.T) {
		hub := NewHub(session.NewRegistry(4), nil)
		hs, conn := register(t, hub)
		drainHost(t, conn)
		v := queued(t, hs, time.Time{})
		hs.HostStatus(session.StatusExited, nil)
		hs.EndLink("l1")
		hs.RemoveViewer(v)
		sink := &pumpSink{}
		v.Pump(sink)
		if !errors.Is(sink.reason, session.ErrSessionEnded) || sink.written() != 0 {
			t.Fatalf("closed with %v after %d frames", sink.reason, sink.written())
		}
	})
	t.Run("the session left the server after its host", func(t *testing.T) {
		hub := NewHub(session.NewRegistry(4), nil)
		hs, conn := register(t, hub)
		drainHost(t, conn)
		v := queued(t, hs, time.Time{})
		hs.HostDisconnected(conn)
		hs.Retire()
		sink := &pumpSink{}
		v.Pump(sink)
		if !errors.Is(sink.reason, ErrHostGone) || sink.written() != 0 {
			t.Fatalf("closed with %v after %d frames", sink.reason, sink.written())
		}
		hs.RemoveViewer(v)
	})
}

// A grant that lapses closes the viewer as it lapses, with ErrExpired, and
// the frames still queued for it are dropped as on a revoke.
func TestHostedViewerGrantUntil(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	drainHost(t, conn)
	until := time.Now().Add(150 * time.Millisecond)
	v, err := hs.AddViewerWith(ViewerOptions{ID: "0123456789abcdef", Role: session.RoleView, LinkID: "l1",
		Authorize: func() (session.Grant, error) { return session.Grant{Role: session.RoleView, Until: until}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.Done():
		if time.Now().Before(until) {
			t.Fatal("closed before its link lapsed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the viewer stays past its link's expiry")
	}
	if !errors.Is(v.Reason(), session.ErrExpired) || framesOwed(v.Reason()) {
		t.Fatalf("reason %v (frames owed %v)", v.Reason(), framesOwed(v.Reason()))
	}
	hs.RemoveViewer(v)

	// One without an end stays; removing it stops nothing that would fire.
	kept, err := hs.AddViewerWith(ViewerOptions{ID: "fedcba9876543210", Role: session.RoleView, LinkID: "l2",
		Authorize: func() (session.Grant, error) { return session.Grant{Role: session.RoleView}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if kept.Reason() != nil {
		t.Fatalf("closed for %v", kept.Reason())
	}
	hs.RemoveViewer(kept)
}

// DisconnectHeldLink says the host was told only when its connection's
// queue took the link_gone: not with no connection, nor with one that has
// closed, nor with one whose queue is full (which closes it).
func TestDisconnectHeldLinkSaysWhetherTheWordWasTaken(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	register := func() (*HostedSession, *HostConn) {
		conn := NewHostConn()
		hs, _, err := hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion,
			Host: proto.HostInfo{Name: "laptop"}, Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24}}, conn)
		if err != nil {
			t.Fatal(err)
		}
		return hs, conn
	}
	hs, conn := register()
	if !hs.DisconnectHeldLink("l1") {
		t.Fatal("a word its connection took was not told")
	}
	drainText(t, conn, proto.HostLinkGone)

	closed, cconn := register()
	cconn.Close()
	if closed.DisconnectHeldLink("l1") {
		t.Fatal("a word to a connection that has closed was told")
	}

	full, fconn := register()
	for n := 0; full.Tell(proto.LinkGone{T: "filler", LinkID: "filler"}) == nil; n++ {
		if n > 1<<16 {
			t.Fatal("the queue never filled")
		}
	}
	if full.DisconnectHeldLink("l1") {
		t.Fatal("a word to a connection whose queue was full was told")
	}
	select {
	case <-fconn.Done():
	default:
		t.Fatal("the connection whose queue was full is open")
	}

	gone, gconn := register()
	gone.HostDisconnected(gconn)
	if gone.DisconnectHeldLink("l1") {
		t.Fatal("a word with no connection was told")
	}
}
