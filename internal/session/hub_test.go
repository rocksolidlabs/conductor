package session

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// chanSink collects frames; a nil-buffered release channel can block writes.
type chanSink struct {
	mu      sync.Mutex
	frames  [][]byte
	block   chan struct{}
	closed  chan error
	writeCh chan struct{}
}

func newChanSink(block bool) *chanSink {
	s := &chanSink{closed: make(chan error, 1), writeCh: make(chan struct{}, 1024)}
	if block {
		s.block = make(chan struct{})
	}
	return s
}

func (s *chanSink) WriteFrame(f []byte) error {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	s.frames = append(s.frames, f)
	s.mu.Unlock()
	select {
	case s.writeCh <- struct{}{}:
	default:
	}
	return nil
}

func (s *chanSink) Close(reason error) { s.closed <- reason }

func (s *chanSink) frame(i int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames[i]
}

func (s *chanSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *chanSink) waitFrames(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for s.count() < n {
		select {
		case <-s.writeCh:
		case <-deadline:
			t.Fatalf("timeout waiting for %d frames, have %d", n, s.count())
		}
	}
}

func TestHubFanOut(t *testing.T) {
	h := NewHub()
	a, b := newChanSink(false), newChanSink(false)
	sa := newSubscription("a", RoleView, "", a)
	sb := newSubscription("b", RoleControl, "l1", b)
	h.add(sa)
	h.add(sb)
	h.Broadcast([]byte{1, 'x'})
	h.Broadcast([]byte{1, 'y'})
	a.waitFrames(t, 2)
	b.waitFrames(t, 2)
	if h.Count() != 2 {
		t.Fatalf("count %d", h.Count())
	}
	h.remove(sa)
	sa.closeWith(nil)
	if h.Count() != 1 {
		t.Fatalf("count after remove %d", h.Count())
	}
	// A subscription under an id another now holds removes nothing.
	sb2 := newSubscription("b", RoleControl, "l1", newChanSink(false))
	if h.remove(sb2) || h.Count() != 1 {
		t.Fatalf("removed another's: count %d", h.Count())
	}
	sb2.closeWith(nil)
}

// A subscriber whose queue passes the high-water mark is evicted at once;
// its sink is closed on the subscription's own goroutine, once the write in
// progress has returned, with the reason.
func TestSlowConsumerEviction(t *testing.T) {
	h := NewHub()
	slow := newChanSink(true)
	sub := newSubscription("slow", RoleView, "", slow)
	h.add(sub)
	frame := make([]byte, 64<<10)
	h.Broadcast(frame)
	deadline := time.Now().Add(3 * time.Second)
	for sub.queued.Load() != 0 { // the first frame is being written
		if time.Now().After(deadline) {
			t.Fatal("the first frame was never taken")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 40; i++ { // 2.5 MiB > high-water mark
		h.Broadcast(frame)
	}
	select {
	case <-sub.Done():
		if !errors.Is(sub.Reason(), ErrSlowConsumer) {
			t.Fatalf("reason %v", sub.Reason())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow consumer was not evicted")
	}
	if h.Count() != 0 {
		t.Fatalf("evicted subscription still counted")
	}
	select {
	case reason := <-slow.closed:
		t.Fatalf("closed (%v) while its write was in progress", reason)
	case <-sub.Closed():
		t.Fatal("Closed before the sink's Close")
	default:
	}
	close(slow.block)
	select {
	case reason := <-slow.closed:
		if !errors.Is(reason, ErrSlowConsumer) {
			t.Fatalf("reason %v", reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the evicted subscriber's sink was never closed")
	}
	select {
	case <-sub.Closed():
	case <-time.After(3 * time.Second):
		t.Fatal("Closed never closed")
	}
	if n := slow.count(); n != 1 {
		t.Fatalf("%d frames written; the one in progress only, the queued ones dropped", n)
	}
}

func TestSinkWriteErrorClosesSubscription(t *testing.T) {
	sink := &errSink{closed: make(chan error, 1)}
	sub := newSubscription("e", RoleView, "", sink)
	sub.send([]byte{1})
	select {
	case r := <-sink.closed:
		if r == nil || r.Error() != "boom" {
			t.Fatalf("reason %v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not closed")
	}
}

type errSink struct{ closed chan error }

func (e *errSink) WriteFrame([]byte) error { return errors.New("boom") }
func (e *errSink) Close(reason error)      { e.closed <- reason }

// Drained is what a host asks before it closes the connections its clients
// read from: nothing sent to a client may still be queued, and nothing may be
// half written. A frame that was taken off the queue and is being written
// still counts.
func TestHubDrainedWaitsForQueuedAndInFlightFrames(t *testing.T) {
	h := NewHub()
	if !h.Drained() {
		t.Fatal("an empty hub is drained")
	}
	sink := newChanSink(true)
	h.add(newSubscription("s", RoleView, "", sink))
	if !h.Drained() {
		t.Fatal("a hub with nothing sent is drained")
	}
	h.Broadcast([]byte{1, 'a'})
	// The sleep lets the subscription take the frame off its queue and block
	// in WriteFrame, so that the check below usually sees the frame in flight.
	// Nothing depends on it: unsent counts a frame from send until WriteFrame
	// returns, so Drained is false whether the frame is still queued or
	// already being written, and a slow scheduler cannot make the check fail.
	time.Sleep(20 * time.Millisecond)
	if h.Drained() {
		t.Fatal("drained with a frame being written")
	}
	h.Broadcast([]byte{1, 'b'})
	if h.Drained() {
		t.Fatal("drained with a frame being written and one queued")
	}
	close(sink.block)
	deadline := time.Now().Add(3 * time.Second)
	for !h.Drained() {
		if time.Now().After(deadline) {
			t.Fatal("never drained")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sink.count() != 2 {
		t.Fatalf("drained after %d of 2 frames were written", sink.count())
	}
}

// A subscription that has ended will write nothing more, so it cannot hold a
// host up.
func TestHubDrainedIgnoresEndedSubscriptions(t *testing.T) {
	h := NewHub()
	sink := newChanSink(true)
	sub := newSubscription("s", RoleView, "", sink)
	h.add(sub)
	h.Broadcast([]byte{1, 'a'})
	h.Broadcast([]byte{1, 'b'})
	sub.closeWith(nil)
	if !h.Drained() {
		t.Fatal("an ended subscription still holds the hub back")
	}
	close(sink.block)
}

// heldCloseSink is a sink whose Close is held until the test releases it:
// a transport whose close is under way.
type heldCloseSink struct {
	closing chan error
	release chan struct{}
}

func (s *heldCloseSink) WriteFrame([]byte) error { return nil }

func (s *heldCloseSink) Close(reason error) {
	s.closing <- reason
	<-s.release
}

// A connection that is not a link's, revoked (Revoke) after it ended for
// another reason, its transport's close still under way for that one, is
// refused for the revocation all the same: what it has under way (keys,
// an Enter, a save's next part) looks at revoked, which says so, though
// Reason keeps the first end, which its transport was told.
func TestARevocationAfterAnotherEndStillRefuses(t *testing.T) {
	sink := &heldCloseSink{closing: make(chan error, 1), release: make(chan struct{})}
	sub := newSubscription("p", RoleControl, "", sink)
	defer close(sink.release)
	sub.closeWith(ErrSlowConsumer)
	if got := <-sink.closing; got != ErrSlowConsumer {
		t.Fatalf("the transport was closed for %v", got)
	}
	// Too slow is no revocation: a submission of a connection that only
	// went goes on.
	if err := revoked(sub); err != nil {
		t.Fatalf("a slow consumer is refused: %v", err)
	}
	revocation := fmt.Errorf("access revoked by the identity provider: %w", ErrRevoked)
	sub.Revoke(revocation)
	sub.Revoke(ErrRevoked)
	if err := revoked(sub); err != revocation {
		t.Fatalf("revoked after the revocation: %v, want %v", err, revocation)
	}
	if err := (writeWait{from: sub}).withdrawn(); err != revocation {
		t.Fatalf("a write of the connection's: %v, want it withdrawn for %v", err, revocation)
	}
	if r := sub.Reason(); r != ErrSlowConsumer {
		t.Fatalf("Reason %v, want the first end's", r)
	}
	if !sub.revoked.Load() {
		t.Fatal("what it is still owed is not dropped")
	}
}
