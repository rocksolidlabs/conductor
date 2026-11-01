package session

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Sink delivers frames to one attached client over some transport. WriteFrame
// may block; the subscription queue in front of it enforces the slow-consumer
// policy. Both are called on the subscription's own goroutine, never under a
// session's lock: WriteFrame for each frame in order, then Close, exactly
// once, with the reason the subscription ended, after the last WriteFrame has
// returned. Close may take its time (an error frame, a close handshake) and
// holds up nobody but the one waiting for the transport to close
// (Subscription.Closed). A WriteFrame in progress as the subscription ends is
// not cut short: a transport bounds its own writes.
type Sink interface {
	WriteFrame(frame []byte) error
	Close(reason error)
}

// A closingSink is a Sink told the moment its subscription ends
// (closeWith), whoever ends it and before the subscription's own goroutine
// has seen it: a transport that bounds how long a client it was told to
// close may still take (a server's WebSocket) starts its clock there, so
// that nothing waiting on the close (DisconnectLink, EndLink) waits past
// it, whatever the goroutine serving the client is doing. Closing must
// return at once: it may be called under any lock.
type closingSink interface {
	Closing()
}

// HighWaterMark is the number of queued bytes after which a subscriber is
// evicted as a slow consumer.
const HighWaterMark = 1 << 20

const queueSlots = 4096

// Subscription is one attached client.
type Subscription struct {
	ID     string
	Role   Role
	LinkID string
	// Name and LinkLabel are what other viewers see in the roster.
	Name      string
	LinkLabel string
	Since     time.Time

	lastInput atomic.Int64 // unix ms of the last accepted input

	sink   Sink
	queue  chan []byte
	queued atomic.Int64
	// unsent counts the frames that are queued or being written; a frame
	// leaves it when WriteFrame returns. See Hub.Drained.
	unsent atomic.Int64
	// done is closed as the subscription ends (closeWith), after reason is
	// set; closed, once the sink's Close has returned (run).
	done   chan struct{}
	closed chan struct{}
	once   sync.Once
	reason error
	// revoked says its link no longer opens the session, revoked (revoke)
	// or expired (expire), which may come after the subscription ended for
	// another reason: what it is still owed at the session's end is dropped
	// from then on (owed).
	revoked atomic.Bool

	inflight atomic.Int32 // file requests in progress

	chat chatBucket // the connection's chat posts, bounded
	// nvims are this connection's Neovim editors (design round 12, F8), by
	// id; keys to them are bounded by nvimKeys.
	nvimMu   sync.Mutex
	nvims    map[string]*nvimEditor
	nvimKeys chatBucket
	// nvimsClosed says the connection's editors were closed as it went
	// (closeNvims): none opens for it after. Under nvimMu.
	nvimsClosed bool
	// write is the save this connection is sending (design round 12, F6):
	// one at a time, its parts in order.
	writeMu sync.Mutex
	write   *pendingWrite
	// owner marks one of the owner's own connections (AttachOptions.Owner),
	// the only ones offered the editor's Neovim.
	owner bool
	// authorize is the connection's credential check (AttachOptions.
	// Authorize), nil for one without a link: asked again before a
	// submission of the connection is written (revoked), since the
	// connection may have ended for another reason first. Set once, as it
	// attaches.
	authorize func() (Grant, error)
	// fresh is the connection's AttachOptions.Fresh, nil for none: asked
	// with authorize wherever what the connection sent is acted on
	// (revoked), never as it attaches. Set once, as it attaches.
	fresh func() error
	// quiet marks a connection for a run's chat alone (hello.chatOnly): no
	// output or scrollback, not a viewer of the session.
	quiet bool
	// want is the size this controller's window would give the terminal (its
	// hello's, then its last resize), for a hand-over of the size; under the
	// session's mu.
	want [2]uint16
	// leftSize says the connection's leave handed the size on (Detach while
	// it sized the session), and takesThen how often the size had been
	// taken by asking by then (Local.takes): the connection that takes it
	// over under the same id (AttachOptions.Replaces, a viewer moving to the
	// relay) takes the size back, unless someone took it by asking
	// meanwhile. Under the session's mu.
	leftSize  bool
	takesThen uint64
	// reportCarry is a terminal or mouse report the connection's last
	// write began and did not finish (typesSomething), for its next; under
	// carryMu.
	carryMu     sync.Mutex
	reportCarry []byte
}

// typesSomething reports whether data, the connection's next write of keys,
// holds anything a person typed rather than only the terminal's reports
// (session.typesSomething), and keeps what it leaves of a report begun for
// the write after.
func (s *Subscription) typesSomething(data []byte) bool {
	s.carryMu.Lock()
	defer s.carryMu.Unlock()
	typed, rest := typesSomething(s.reportCarry, data)
	s.reportCarry = rest
	return typed
}

// ownsSize reports whether s is one of the owner's own windows: a
// controller that came with no link (the workbench, a host's own terminal).
// Only such a window takes the size without asking, and only while nobody
// holds it (Local.sizer).
func (s *Subscription) ownsSize() bool {
	return s.Role == RoleControl && s.LinkID == "" && !s.quiet
}

func newSubscription(id string, role Role, linkID string, sink Sink) *Subscription {
	s := &Subscription{ID: id, Role: role, LinkID: linkID, Since: time.Now().UTC(), sink: sink, queue: make(chan []byte, queueSlots), done: make(chan struct{}), closed: make(chan struct{})}
	go s.run()
	return s
}

// Info describes the subscription for the viewers roster.
func (s *Subscription) Info() proto.ViewerInfo {
	v := proto.ViewerInfo{ID: s.ID, Name: s.Name, Role: string(s.Role), Link: s.LinkLabel, Since: s.Since.Format(time.RFC3339), Quiet: s.quiet}
	if ms := s.lastInput.Load(); ms > 0 {
		v.LastInputAt = time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
	}
	return v
}

// run is the subscription's own goroutine: it hands the queued frames to the
// sink, in order, until the subscription ends, and then closes the sink, once,
// with the reason, which is set before done is closed. The frames still queued
// then are dropped, but for a subscription the session's end closed (owed),
// and nothing reaches the sink after its Close.
func (s *Subscription) run() {
	defer close(s.closed)
	defer func() { s.sink.Close(s.reason) }()
	for {
		// The end comes before any frame still queued: select would pick
		// at random between the two.
		select {
		case <-s.done:
			s.owed()
			return
		default:
		}
		select {
		case <-s.done:
			s.owed()
			return
		case frame := <-s.queue:
			s.queued.Add(-int64(len(frame)))
			err := s.sink.WriteFrame(frame)
			s.unsent.Add(-1)
			if err != nil {
				s.closeWith(err)
				return
			}
		}
	}
}

// owed writes a subscription the session's end closed (ErrSessionEnded: its
// link ended with it, EndLink) the frames still queued for it, in order, up to the first that cannot be
// written: the session's last output, its final status among them, which
// its viewer is given before it is told the session ended. Nothing is
// queued after the end (send). A subscription that ended for any other
// reason (revoked, a slow consumer, a viewer that left) is owed nothing,
// and a revoke of its link that comes as it is written them ends it
// (revoke). Its transport bounds the wait (closingSink): a server cuts a connection that
// reads too little (api's closeWait).
func (s *Subscription) owed() {
	if !errors.Is(s.reason, ErrSessionEnded) {
		return
	}
	for {
		// A revoke that lands meanwhile stops it: the frame being written
		// is the last.
		if s.revoked.Load() {
			return
		}
		select {
		case frame := <-s.queue:
			s.queued.Add(-int64(len(frame)))
			err := s.sink.WriteFrame(frame)
			s.unsent.Add(-1)
			if err != nil {
				return
			}
		default:
			return
		}
	}
}

// send enqueues a frame without blocking. It evicts the subscriber when the
// queue exceeds the high-water mark.
func (s *Subscription) send(frame []byte) {
	select {
	case <-s.done:
		return
	default:
	}
	if s.queued.Load()+int64(len(frame)) > HighWaterMark {
		s.closeWith(ErrSlowConsumer)
		return
	}
	s.unsent.Add(1) // before the frame can be seen, so a check never finds it missing
	select {
	case s.queue <- frame:
		s.queued.Add(int64(len(frame)))
	default:
		s.unsent.Add(-1)
		s.closeWith(ErrSlowConsumer)
	}
}

// drained reports whether the sink has been handed everything sent to it, or
// the subscription has ended and nothing more will be.
func (s *Subscription) drained() bool {
	select {
	case <-s.done:
		return true
	default:
	}
	return s.unsent.Load() == 0
}

// closeWith ends the subscription: the first reason given is kept and done
// is closed, and nothing more is queued; a closingSink is told. It does no
// more, so any goroutine may call it under any lock (a broadcast under the
// session's lock drops a slow consumer): the sink is closed by the
// subscription's own goroutine (run), which Closed waits for.
func (s *Subscription) closeWith(reason error) {
	s.once.Do(func() {
		s.reason = reason
		close(s.done)
		if c, ok := s.sink.(closingSink); ok {
			c.Closing()
		}
	})
}

// revoke ends the subscription as its link's revoke: as revoked, or, when
// it ended already (the session's end), by dropping what it is still owed
// from then on.
func (s *Subscription) revoke() {
	s.revoked.Store(true)
	s.closeWith(ErrRevoked)
}

// Revoke ends the subscription as revoked for a credential that is not a
// link (an identity provider's principal the provider no longer admits:
// api's principalWatch), as its link's revoke would (DisconnectLink):
// reason, ErrRevoked or a kind of it (ErrRevoked for any other), is what
// it is closed with, its transport told so; nothing more queued for it is
// written; and what it has under way is refused with that reason, as a
// revoked link's is (a submission waiting for its turn or its Enter, keys
// not yet written, a save's next part: Reason, which they look at). It
// takes no lock, so any goroutine may call it, more than once: the first
// reason the subscription ended for is kept.
func (s *Subscription) Revoke(reason error) {
	if !errors.Is(reason, ErrRevoked) {
		reason = ErrRevoked
	}
	s.revoked.Store(true)
	s.closeWith(reason)
}

// expire is revoke for a link that reached its expiry (ErrExpired).
func (s *Subscription) expire() {
	s.revoked.Store(true)
	s.closeWith(ErrExpired)
}

// retire is revoke for the session leaving the server (Local.Retire): the
// close says the session ended (ErrSessionEnded), and nothing more is
// written.
func (s *Subscription) retire() {
	s.revoked.Store(true)
	s.closeWith(ErrSessionEnded)
}

// Done is closed when the subscription ends.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Closed is closed once the subscription has ended and its sink's Close has
// returned: the transport is closed.
func (s *Subscription) Closed() <-chan struct{} { return s.closed }

// waitClosed waits until the transport of every subscription in subs has
// closed. They close at once, each on its own goroutine, so this takes as
// long as the slowest. The caller holds no lock.
func waitClosed(subs []*Subscription) {
	for _, sub := range subs {
		<-sub.closed
	}
}

// Reason returns why the subscription ended (nil for a normal detach).
func (s *Subscription) Reason() error {
	select {
	case <-s.done:
		return s.reason
	default:
		return nil
	}
}

// Hub fans frames out to subscriptions.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]*Subscription
}

// NewHub creates an empty hub.
func NewHub() *Hub { return &Hub{subs: map[string]*Subscription{}} }

// namePresent reports whether a live subscription other than `except` carries
// the name: the same person on another tab, for the chat's join and leave lines.
func (h *Hub) namePresent(name, except string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, s := range h.subs {
		if id != except && s.Name == name && s.Reason() == nil {
			return true
		}
	}
	return false
}

func (h *Hub) add(s *Subscription) {
	h.mu.Lock()
	h.subs[s.ID] = s
	h.mu.Unlock()
}

// remove takes sub out if it is the one held under its id, and says so: a
// client that replaced it under the same id (a viewer moving to the relay)
// stays.
func (h *Hub) remove(sub *Subscription) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[sub.ID] != sub {
		return false
	}
	delete(h.subs, sub.ID)
	return true
}

// Broadcast enqueues frame to every subscription.
func (h *Hub) Broadcast(frame []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.subs {
		s.send(frame)
	}
}

// BroadcastLoud enqueues frame to every subscription but the quiet ones: the
// terminal's output.
func (h *Hub) BroadcastLoud(frame []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.subs {
		if !s.quiet {
			s.send(frame)
		}
	}
}

// Drained reports whether every live subscription has been handed all the frames
// sent to it: none is queued and none is being written. A host asks it before
// it closes the connection its clients' frames travel on. It says nothing of
// what a transport does with a frame after taking it.
func (h *Hub) Drained() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.subs {
		if !s.drained() {
			return false
		}
	}
	return true
}

// Count returns the number of live subscriptions.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, s := range h.subs {
		if s.Reason() == nil {
			n++
		}
	}
	return n
}

// Len returns the number of subscriptions the hub holds, those that have
// ended and not yet left included.
func (h *Hub) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// LoudCount returns the number of live subscriptions that are not quiet: the
// session's viewers.
func (h *Hub) LoudCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, s := range h.subs {
		if s.Reason() == nil && !s.quiet {
			n++
		}
	}
	return n
}

// Roster lists every live subscription, oldest first, the quiet ones marked.
func (h *Hub) Roster() []proto.ViewerInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]proto.ViewerInfo, 0, len(h.subs))
	for _, s := range h.subs {
		if s.Reason() == nil {
			out = append(out, s.Info())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since == out[j].Since {
			return out[i].ID < out[j].ID
		}
		return out[i].Since < out[j].Since
	})
	return out
}

// CountByLink counts live subscriptions per share link id.
func (h *Hub) CountByLink() map[string]int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := map[string]int{}
	for _, s := range h.subs {
		if s.LinkID != "" && s.Reason() == nil {
			out[s.LinkID]++
		}
	}
	return out
}

// Each calls fn for every subscription.
func (h *Hub) Each(fn func(*Subscription)) {
	h.mu.RLock()
	list := make([]*Subscription, 0, len(h.subs))
	for _, s := range h.subs {
		list = append(list, s)
	}
	h.mu.RUnlock()
	for _, s := range list {
		fn(s)
	}
}
