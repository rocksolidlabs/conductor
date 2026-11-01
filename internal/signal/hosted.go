// Package signal brokers hosted sessions: it tracks connected `conductor host`
// processes, forwards WebRTC signaling between viewers and hosts, and relays
// terminal frames when a data channel cannot be established.
package signal

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"weak"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Errors reported to viewers and hosts.
var (
	ErrHostGone   = errors.New("signal: host disconnected")
	ErrViewerGone = errors.New("signal: viewer closed")
	ErrSlowViewer = errors.New("signal: slow viewer")
	ErrSlowHost   = errors.New("signal: slow host")
	// ErrRateLimited is session.ErrRateLimited: a report is refused alike
	// whichever kind of session it is for.
	ErrRateLimited   = session.ErrRateLimited
	ErrTooManyViewer = errors.New("signal: too many viewers")
)

// DisconnectGrace is how long a hosted session survives without its host.
const DisconnectGrace = 60 * time.Second

// The queues between the server and a hosted session's ends hold a number of
// messages and a number of bytes, whichever fills first.
const (
	viewerQueue = 2048
	// viewerQueueBytes bounds the frames queued to one viewer: past it the
	// viewer is closed as too slow (ErrSlowViewer). The frames alone, of up
	// to proto.MaxFrame each, would let it hold GiBs. A host that holds a
	// relayed viewer's frames once told it is behind (relayBehindBytes)
	// leaves little more than that mark queued, and what was on its way;
	// an older host's viewer on a slow connection may fall this far behind,
	// a thousand whole output frames, or a dozen file frames.
	viewerQueueBytes = 16 << 20
	// hostQueue and hostQueueBytes bound what is queued to a host
	// connection, its two lanes together (HostConn).
	hostQueue      = 8192
	hostQueueBytes = 8 << 20
	// hostOwnSlots and hostOwnBytes are the server's own lane (send): the
	// answers to the host's requests, an agent's report, a link's going, a
	// leave that says why. The writer takes it first.
	hostOwnSlots = 512
	hostOwnBytes = 1 << 20
	// What the viewers' messages (their joins and leaves among them) may
	// fill (sendFrom): the rest, the viewers' lane. Viewers that send more
	// than the host reads, or come and go faster than it reads, wait for
	// room and never close the host's connection, nor hold up the server's
	// own messages.
	hostViewerBytes = hostQueueBytes - hostOwnBytes
	hostViewerSlots = hostQueue - hostOwnSlots
	// A relayed viewer for which more than relayBehindBytes is queued has
	// fallen behind what its host relays to it: the host is told
	// (viewer_behind), and holds that viewer's frames until it is told the
	// viewer caught up, at relayCaughtUpBytes or less, as it holds a WebRTC
	// viewer's while the data channel is full. The host's connection, which
	// its relayed viewers share, so carries what each of them takes, and
	// what waits for one waits on the host, under the host's own bound for
	// a viewer. What the viewer's socket holds is not counted (the server
	// keeps it small: api's hostedViewerUnsent). An older host, which
	// ignores viewer_behind, is bounded by viewerQueueBytes alone.
	relayBehindBytes   = 512 << 10
	relayCaughtUpBytes = 128 << 10
)

// Outbound is one message queued to a host connection.
type Outbound struct {
	Text   []byte // JSON control message
	Binary []byte // RELAY envelope
	// took, when set, is called once the writer has taken the message
	// (Took), holding no lock, whether it is written or dropped.
	took func()
	// of, when set, is the viewer the message is of (its join, its
	// relay_start, offer and candidates, the frames it relays, its
	// viewer_behind): one whose link went (revoked, expired) by the time
	// the writer takes the message, the viewer there or gone, has it
	// dropped then, never written (Took).
	of *Viewer
	// join marks of's join: taking it settles whether the join went
	// (Viewer.joinSettled).
	join bool
	// own says the message is in the server's own lane (send).
	own bool
}

// size is what o counts for in its queue's bytes.
func (o Outbound) size() int { return len(o.Text) + len(o.Binary) }

// HostConn is a live control connection from one host process. What is
// queued to it goes in one of two lanes, each in its order: the server's own
// messages (Own: link_gone, a viewer_leave that gives a reason, stop,
// attention, activity, the answers to the host's requests, error), and the
// viewers' (Send: viewer_join, offer, ice, relay_start, the RELAY envelopes,
// viewer_behind, a viewer_leave with no reason). The API layer's writer
// takes them with Next, the server's own first, and writes each to the
// WebSocket: a message of the server's own waits behind the one message
// being written when it was queued, never behind the viewers' lane. A test
// that reads a lane itself calls Took with each message it takes.
type HostConn struct {
	Own  chan Outbound
	Send chan Outbound
	done chan struct{}
	once sync.Once

	// mu is held by every send into either lane, with bytes, the bytes of
	// what the viewers' lane holds (held among them), and ownBytes, those
	// of the server's own lane; waiting, the viewers' messages waiting for
	// room, in their order (sendFrom); and room, which they wait on: Took
	// closes it, as does a waiter that leaves the head of the line, and the
	// next to wait makes another.
	mu       sync.Mutex
	bytes    int
	ownBytes int
	waiting  []*roomWaiter
	room     chan struct{}
	// behindLate are the viewers whose viewer_behind found no room in the
	// queue (offerBehind): each tries again as the writer next takes a
	// message (Took). Guarded by mu.
	behindLate []*Viewer
	// held is a message of the viewers' lane Next took just as one of the
	// server's own came in: the next it takes after the server's own. Only
	// Next touches it, and Next has one caller, the writer.
	held *Outbound
	// OnWait, when set, is called under mu as a viewer's message takes its
	// place in line for room (enter): a test's signal that a frame waits,
	// which no clock can give it (#281). It must not block.
	OnWait func()
	// Open says the host registered with no token, as a switchyard's open
	// hosts may: set before Register and never after. The viewers of its
	// sessions are handed the configured ICE servers alone, no media
	// provider's (Viewer.HostOpen).
	Open bool
}

// roomWaiter is a viewer's message in the line for room in a host's queue:
// its place, and its size.
type roomWaiter struct{ n int }

// NewHostConn creates a host connection with an outbound queue.
func NewHostConn() *HostConn {
	return &HostConn{Own: make(chan Outbound, hostOwnSlots), Send: make(chan Outbound, hostViewerSlots), done: make(chan struct{})}
}

// Done is closed when the connection must be torn down.
func (c *HostConn) Done() <-chan struct{} { return c.done }

// Close marks the connection dead.
func (c *HostConn) Close() { c.once.Do(func() { close(c.done) }) }

// Next waits for the next message to write to the host and returns it,
// taken (Took): one of the server's own while any is queued, else the next
// of the viewers' lane, a message of a viewer whose link went dropped as it
// is taken; false once the connection is closed or ctx ends. The
// connection's writer alone calls it, one call at a time.
func (c *HostConn) Next(ctx context.Context) (Outbound, bool) {
	for {
		o, ok := c.next(ctx)
		if !ok {
			return Outbound{}, false
		}
		if c.Took(o) {
			return o, true
		}
	}
}

// next is Next's message before it is taken.
func (c *HostConn) next(ctx context.Context) (Outbound, bool) {
	select {
	case o := <-c.Own:
		return o, true
	default:
	}
	if c.held != nil {
		o := *c.held
		c.held = nil
		return o, true
	}
	select {
	case o := <-c.Own:
		return o, true
	case o := <-c.Send:
		// One of the server's own that came in meanwhile goes first, and
		// this one next.
		select {
		case own := <-c.Own:
			c.held = &o
			return own, true
		default:
		}
		return o, true
	case <-c.done:
		return Outbound{}, false
	case <-ctx.Done():
		return Outbound{}, false
	}
}

// Took tells the connection its writer took o from its lane, and reports
// whether o is to be written: not when it is of a viewer whose link went
// (revoked or expired: Viewer.linkWent), the viewer closed for it or gone
// already, which is dropped as it is taken. o's bytes leave the queue, the
// viewers' messages waiting for room try again, and so do the
// viewer_behind that found none (offerBehind), after o's own took. A join
// taken settles whether it went: written, or dropped so
// (Viewer.joinSettled).
func (c *HostConn) Took(o Outbound) (write bool) {
	write = o.of == nil || !o.of.linkWent()
	c.mu.Lock()
	if o.own {
		c.ownBytes = max(0, c.ownBytes-o.size())
	} else {
		c.bytes = max(0, c.bytes-o.size())
	}
	c.wake()
	late := c.behindLate
	c.behindLate = nil
	c.mu.Unlock()
	if o.join {
		o.of.joinSettled(write)
	}
	if o.took != nil {
		o.took()
	}
	for _, v := range late {
		v.behindRoom()
	}
	return write
}

// offerBehind queues v's viewer_behind o when the viewers' lane has room
// for it (admit), as for a message of v's own but without waiting in their
// line: never in the server's own lane, which keeps all of its room however
// many viewers there are. When the viewers' lane has none, it never closes
// the connection, as send does, but has v try again as the writer next
// takes a message (Took), which it will, the lane being full. It reports
// whether o was queued. Called with v.behindMu held.
func (c *HostConn) offerBehind(o Outbound, v *Viewer) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.admit(o, o.size()) {
		return true
	}
	c.behindLate = append(c.behindLate, v)
	return false
}

// wake has the viewers' messages waiting for room look again. Called with mu
// held.
func (c *HostConn) wake() {
	if c.room != nil {
		close(c.room)
		c.room = nil
	}
}

// queued reports the bytes both lanes hold.
func (c *HostConn) queued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes + c.ownBytes
}

// send queues a message of the server's own in its lane, without waiting.
// One that finds the lane full, in messages (hostOwnSlots) or in bytes
// (hostOwnBytes), closes the connection: a host that reads that little is
// too slow to keep.
func (c *HostConn) send(o Outbound) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	n := o.size()
	o.own = true
	c.mu.Lock()
	if c.ownBytes+n > hostOwnBytes {
		c.mu.Unlock()
		c.Close()
		return false
	}
	select {
	case c.Own <- o:
		c.ownBytes += n
		c.mu.Unlock()
		return true
	default:
		c.mu.Unlock()
		c.Close()
		return false
	}
}

// sendFrom queues a message a viewer sent (a frame it relays, its offer, a
// candidate) in the viewers' lane, within hostViewerBytes and
// hostViewerSlots: past them, sendFrom waits until the writer takes enough
// (Took). The viewers' messages that wait get room in their order, whatever
// their sizes: one that comes while others wait waits behind them. A viewer
// that sends more than the host reads is so held in its own read loop; the
// host's loop and the server's messages never wait for it, and it never
// closes the host's connection. It returns ErrSlowHost when the connection
// is closed, ErrViewerGone when gone is closed (the viewer was let go) before
// the message is queued, and ErrViewerGone when stop is closed while it
// waits: a message that needs no wait is queued whatever stop says.
func (c *HostConn) sendFrom(o Outbound, gone, stop <-chan struct{}) error {
	select {
	case <-c.done:
		return ErrSlowHost
	case <-gone:
		return ErrViewerGone
	default:
	}
	me := c.enter(o)
	if me == nil {
		return nil
	}
	return c.await(me, o, stop)
}

// enter is sendFrom's first half: o is queued at once when nobody waits and
// the viewers' lane has room (nil), or takes its place in line (the waiter
// await then waits as). What is sent after o goes after it either way.
func (c *HostConn) enter(o Outbound) *roomWaiter {
	n := o.size()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.waiting) == 0 && c.admit(o, n) {
		return nil
	}
	me := &roomWaiter{n: n}
	c.waiting = append(c.waiting, me)
	if c.OnWait != nil {
		c.OnWait()
	}
	return me
}

// await is sendFrom's second half: o, in line as me, waits for room in its
// turn, until the connection closes (ErrSlowHost) or stop does
// (ErrViewerGone), when it leaves the line.
func (c *HostConn) await(me *roomWaiter, o Outbound, stop <-chan struct{}) error {
	n := me.n
	c.mu.Lock()
	for {
		if c.waiting[0] == me && c.admit(o, n) {
			c.waiting = c.waiting[1:]
			c.wake() // the next in line may fit as well
			c.mu.Unlock()
			return nil
		}
		if c.room == nil {
			c.room = make(chan struct{})
		}
		room := c.room
		c.mu.Unlock()
		var err error
		select {
		case <-room:
		case <-c.done:
			err = ErrSlowHost
		case <-stop:
			err = ErrViewerGone
		}
		c.mu.Lock()
		if err != nil {
			if c.waiting[0] == me {
				c.wake()
			}
			c.waiting = slices.DeleteFunc(c.waiting, func(w *roomWaiter) bool { return w == me })
			c.mu.Unlock()
			return err
		}
	}
}

// admit queues o, of n bytes, in the viewers' lane when it has room for it
// (any message when the lane is empty). Called with mu held.
func (c *HostConn) admit(o Outbound, n int) bool {
	if (c.bytes != 0 && c.bytes+n > hostViewerBytes) || len(c.Send) >= hostViewerSlots {
		return false
	}
	select {
	case c.Send <- o:
		c.bytes += n
		return true
	default:
		return false
	}
}

func (c *HostConn) sendJSON(v any) bool {
	b, err := jsonMarshal(v)
	if err != nil {
		return false
	}
	return c.send(Outbound{Text: b})
}

// Viewer is one browser attached to a hosted session. Frames queued to Out
// are written to the viewer's WebSocket by Pump, which the API layer runs.
type Viewer struct {
	ID        string
	Role      session.Role
	LinkID    string
	LinkLabel string
	Out       chan []byte
	// queued is the bytes of the frames in Out, bounded by viewerQueueBytes.
	queued atomic.Int64

	relay  atomic.Bool
	done   chan struct{}
	once   sync.Once
	reason error
	// stop is closed when the viewer is closed, or before that when its
	// connection fails (ConnFailed): what waits on the viewer's behalf (its
	// join, a message of its for room in the host's queue) ends with it.
	stop     chan struct{}
	stopOnce sync.Once
	// failed is set by ConnFailed before it closes stop.
	failed atomic.Bool
	// revoked says what the viewer is still owed is dropped from then on
	// (finish): its link no longer opens the session, revoked (closeLink)
	// or expired (lapse), which may come after the viewer was closed for
	// another reason; or it was let go (RemoveViewer), or the session left
	// the server (Retire).
	revoked atomic.Bool
	// forLink says the server closed the viewer for its link, revoked
	// (closeLink) or expired (lapse, linkLapsed), whatever it was closed for
	// first: set before the viewer is closed, and before the link's
	// link_gone is queued. Nothing of the viewer's still queued for the host
	// is written from then on (HostConn.Took).
	forLink atomic.Bool
	// link is the state of the link the viewer joined through, shared with
	// the link's other viewers, nil for none: once the link went, nothing
	// of the viewer's still queued is written either, also once the viewer
	// has left and so is no longer found to be closed (linkGoneOf).
	link *linkState
	// lapse closes the viewer when its link expires (ViewerOptions'
	// Authorize gave a Grant with Until), and drops what it is still owed
	// should it have been closed before (revoked); set by AddViewerWith,
	// stopped by RemoveViewer, both under the session's mu.
	lapse *time.Timer
	// charge is ViewerOptions.Charge.
	charge func(n int) error
	// conn is the host connection the viewer joined over, told when the
	// viewer falls behind and when it catches up (noteBacklog). Under
	// behindMu: behind is what the marks say of the viewer; told is what
	// the host was last sent; inQueue says that message is still in the
	// host's queue, not taken; late says the viewer waits for room there.
	// So at most one viewer_behind of the viewer's is ever queued, and the
	// host is sent the latest once it is taken.
	conn     *HostConn
	behindMu sync.Mutex
	behind   bool
	told     bool
	inQueue  bool
	late     bool
	// joinDone is closed once the viewer's join is queued for the host, or
	// will not be (joinQueued false: the viewer or the connection closed
	// first, or its charge failed); joinQueued is written before, and read
	// after. Every later message of the viewer's is queued behind it.
	joinDone   chan struct{}
	joinQueued bool
	// joinTaken is closed once it is settled whether the join went: the
	// writer took it and wrote it (joinWent), or dropped it, the viewer
	// closed for its link (Took), or it was never queued. joinWent is
	// written before, and read after. A viewer's leave follows a join that
	// went, and only such a join (RemoveViewer). One whose connection
	// closes with the join still queued never settles: whoever waits for it
	// waits for the connection's end too.
	joinTaken     chan struct{}
	joinTakenOnce sync.Once
	joinWent      bool
}

// linkWent says the server closed the viewer for its link, or its link no
// longer opens the session (revoked, expired) whether or not the viewer was
// still there to be closed: what of its is still queued is not written.
func (v *Viewer) linkWent() bool {
	return v.forLink.Load() || (v.link != nil && v.link.gone.Load())
}

// linkState is what the server knows of a link viewers joined the session
// through: whether it went (revoked or expired). The link's viewers share
// it, each holding it for as long as it, or a message of its queued for the
// host, lives; it is marked before the link's link_gone is queued.
type linkState struct{ gone atomic.Bool }

// joinSettled settles whether the viewer's join went (went), once.
func (v *Viewer) joinSettled(went bool) {
	v.joinTakenOnce.Do(func() {
		v.joinWent = went
		close(v.joinTaken)
	})
}

// chargeHost tells the viewer's Charge of a message of n bytes it sends the
// host.
func (v *Viewer) chargeHost(n int) error {
	if v.charge == nil {
		return nil
	}
	return v.charge(n)
}

// joined waits for the viewer's join to be queued, which every message of
// the viewer's to the host follows: ErrViewerGone when the viewer closes,
// or its connection fails, while the join waits, or the join was not
// queued.
func (v *Viewer) joined() error {
	select {
	case <-v.joinDone:
	default:
		select {
		case <-v.joinDone:
		case <-v.stop:
			return ErrViewerGone
		}
	}
	if !v.joinQueued {
		return ErrViewerGone
	}
	return nil
}

func newViewer(id string, role session.Role, linkID, linkLabel string) *Viewer {
	return &Viewer{ID: id, Role: role, LinkID: linkID, LinkLabel: linkLabel, Out: make(chan []byte, viewerQueue), done: make(chan struct{}), stop: make(chan struct{}), joinDone: make(chan struct{}), joinTaken: make(chan struct{})}
}

// closeForLink closes the viewer for its link (reason ErrRevoked or
// ErrExpired), marked first (forLink, revoked): nothing of its still queued
// for the host is written, nor what it is still owed. The caller closes a
// link's viewers so before it queues the link's link_gone, so the going
// never reaches the host ahead of anything of theirs that is written.
func (v *Viewer) closeForLink(reason error) {
	v.forLink.Store(true)
	v.revoked.Store(true)
	v.close(reason)
}

// Revoke closes the viewer as revoked for a credential that is not a link
// (an identity provider's principal the provider no longer admits: api's
// principalWatch), as its link's revoke closes it (closeForLink): reason,
// ErrRevoked or a kind of it (ErrRevoked for any other), is what it is
// closed with; nothing of its still queued for the host is written, nor
// what it is still owed; and its leave, once it is let go (RemoveViewer),
// says it was revoked, so that the host withdraws what it has under way
// rather than finish it.
func (v *Viewer) Revoke(reason error) {
	if !errors.Is(reason, session.ErrRevoked) {
		reason = session.ErrRevoked
	}
	v.closeForLink(reason)
}

// Done is closed when the viewer must be disconnected.
func (v *Viewer) Done() <-chan struct{} { return v.done }

// HostOpen reports whether the host connection the viewer joined over
// registered with no token (HostConn.Open).
func (v *Viewer) HostOpen() bool { return v.conn != nil && v.conn.Open }

// Reason reports why the viewer was closed.
func (v *Viewer) Reason() error {
	select {
	case <-v.done:
		return v.reason
	default:
		return nil
	}
}

// Relay reports whether the viewer switched to the server relay.
func (v *Viewer) Relay() bool { return v.relay.Load() }

func (v *Viewer) close(reason error) {
	v.once.Do(func() {
		v.reason = reason
		close(v.done)
	})
	v.halt()
}

// ConnFailed tells the viewer its connection has failed (a write to it, a
// ping): what waits on its behalf, its join or a message of its for room in
// the host's queue, stops waiting, and the message is dropped. The viewer is
// not closed: what it sent before still goes to the host, behind what went
// before, when it needs no wait; whoever serves it lets it go
// (RemoveViewer) once it has read what the connection held, and its leave
// comes after.
func (v *Viewer) ConnFailed() {
	v.failed.Store(true)
	v.halt()
}

// Failed reports whether the viewer's connection has failed (ConnFailed). A
// message of the viewer's that stopped waiting for it sees it so.
func (v *Viewer) Failed() bool { return v.failed.Load() }

func (v *Viewer) halt() { v.stopOnce.Do(func() { close(v.stop) }) }

// Pump writes the frames queued for the viewer to sink, in order, until the
// viewer is closed, and then closes sink with the reason it was closed for.
// The API layer runs it on its own goroutine for the viewer's connection. A
// frame that cannot be written ends it at once, sink left as it is: the
// connection has failed (ConnFailed), and the viewer stays until whoever
// serves it lets it go, having read what the connection still held.
//
// What becomes of the frames still queued when the viewer is closed depends on
// the reason. They are dropped when the link was revoked (or expired), when the
// viewer's own queue overflowed (ErrSlowViewer) and when the viewer left (a nil
// reason). For every other reason, the session's end (its links end with it,
// EndLink), the host going
// away, asking for the close or reporting an error for the viewer, they are
// written first: at most as many as the queue holds, and up to the first frame
// that cannot be written. They are the session's last output, its final status
// among them, which the host relayed before it reported the end, and the
// viewer would otherwise be told the session ended, or the host is gone,
// without what it printed last. A viewer that the host evicted as too slow
// (the host asks for the close) is in that second group and is drained too.
// A revoke or the expiry of the viewer's link that lands meanwhile stops
// them (closeLink, lapse), as does the session's leaving the server
// (Retire), which writes them nothing at all.
// Whoever serves the viewer bounds how long that may take: a viewer that reads
// too little is cut off (the sink's write fails, and Pump ends).
//
// The close is looked at before each frame, but it does not stop a frame that
// is being written, or one that is picked just as the close lands: a revoked
// viewer can still be written a frame.
func (v *Viewer) Pump(sink session.Sink) {
	for {
		// Once the viewer is closed, the close comes before any frame that is
		// still queued: select would pick at random between the two.
		select {
		case <-v.done:
			v.finish(sink)
			return
		default:
		}
		select {
		case <-v.done:
			v.finish(sink)
			return
		case frame := <-v.Out:
			v.queued.Add(-int64(len(frame)))
			v.noteBacklog()
			if err := sink.WriteFrame(frame); err != nil {
				// The connection failed: what waits on the viewer's behalf
				// (a frame for the host's queue) stops.
				v.ConnFailed()
				return
			}
		}
	}
}

// finish is Pump's end: the frames the viewer is owed, if any, and the close.
func (v *Viewer) finish(sink session.Sink) {
	reason := v.Reason()
	if framesOwed(reason) {
	owed:
		for {
			// A revoke of its link that lands meanwhile stops it: the
			// frame being written is the last.
			if v.revoked.Load() {
				break
			}
			select {
			case frame := <-v.Out:
				v.queued.Add(-int64(len(frame)))
				if err := sink.WriteFrame(frame); err != nil {
					return
				}
			default:
				break owed
			}
		}
	}
	sink.Close(reason)
}

// framesOwed reports whether a viewer closed for reason is still sent the frames
// queued for it: it is unless the link was revoked (or expired), the viewer's
// own queue overflowed or the viewer left. A viewer the session's end closed
// (session.ErrSessionEnded) is.
func framesOwed(reason error) bool {
	switch {
	case reason == nil, errors.Is(reason, session.ErrRevoked), errors.Is(reason, ErrSlowViewer):
		return false
	}
	return true
}

// push queues a frame for the viewer without waiting. A viewer whose queue
// would hold more than viewerQueue frames or viewerQueueBytes bytes is closed
// as too slow (ErrSlowViewer). A relayed viewer that passes relayBehindBytes
// is behind (noteBacklog).
func (v *Viewer) push(frame []byte) {
	select {
	case <-v.done:
		return
	default:
	}
	n := int64(len(frame))
	if v.queued.Add(n) > viewerQueueBytes {
		v.queued.Add(-n)
		v.close(ErrSlowViewer)
		return
	}
	select {
	case v.Out <- frame:
		v.noteBacklog()
	default:
		v.queued.Add(-n)
		v.close(ErrSlowViewer)
	}
}

// noteBacklog tells the host, when the viewer is relayed, that it fell
// behind (more than relayBehindBytes queued) or caught up (relayCaughtUpBytes
// or less), as it passes either mark, and none once the viewer is closed.
// It is called after each change of what is queued, as a frame is queued
// (push) and as one is taken (Pump), and looks at what is queued under
// behindMu every time: a look that is the last in the lock's order comes
// after every change, so what the host is told last holds for what is
// queued once the changes stop (a look outside the lock could miss the
// change the other end was making). It does not wait (tellBehind).
func (v *Viewer) noteBacklog() {
	if v.conn == nil || !v.relay.Load() {
		return
	}
	v.behindMu.Lock()
	defer v.behindMu.Unlock()
	if behindAt(v.queued.Load(), v.behind) != v.behind {
		if behindSeam != nil {
			behindSeam(v)
		}
		v.behind = !v.behind
	}
	v.tellBehind()
}

// tellBehind sends the host what the marks say of the viewer (behind), in
// the viewers' lane of its queue (offerBehind), when the host was last
// sent otherwise: unless one of the viewer's is still in the host's queue,
// when the latest goes once that one is taken (behindTaken), or the lane
// has no room, when it goes once the writer next takes a message
// (behindRoom). So the viewer's changes, however many and however fast,
// never put more than one message in the host's queue, never take the room
// of the server's own messages nor close the connection for want of room,
// and the host ends with the latest. Nothing is sent once the viewer is
// closed, and one queued for a viewer whose link went is dropped as it is
// taken. Called with behindMu held.
func (v *Viewer) tellBehind() {
	if v.inQueue || v.late || v.told == v.behind {
		return
	}
	select {
	case <-v.done:
		return
	default:
	}
	b, err := jsonMarshal(proto.ViewerBehind{T: proto.HostViewerBehind, ViewerID: v.ID, Behind: v.behind})
	if err != nil {
		return
	}
	if v.conn.offerBehind(Outbound{Text: b, took: v.behindTaken, of: v}, v) {
		v.inQueue, v.told = true, v.behind
	} else {
		v.late = true
	}
}

// behindTaken is the viewer's viewer_behind taken by the host connection's
// writer: the latest goes now, if it differs.
func (v *Viewer) behindTaken() {
	v.behindMu.Lock()
	defer v.behindMu.Unlock()
	v.inQueue = false
	v.tellBehind()
}

// behindRoom is room in the host's queue for the viewer_behind that found
// none.
func (v *Viewer) behindRoom() {
	v.behindMu.Lock()
	defer v.behindMu.Unlock()
	v.late = false
	v.tellBehind()
}

// behindSeam, when set (a test's), is called in noteBacklog once it has
// decided to tell the host of a change, before it does.
var behindSeam func(v *Viewer)

// behindAt reports whether a relayed viewer with queued bytes queued is
// behind, given whether it was: past relayBehindBytes it falls behind, and
// it stays so until it is down to relayCaughtUpBytes.
func behindAt(queued int64, was bool) bool {
	if was {
		return queued > relayCaughtUpBytes
	}
	return queued > relayBehindBytes
}

// HostedSession is a session whose PTY lives in a host process. It implements
// session.Driver.
type HostedSession struct {
	hub    *Hub
	secret string
	log    *slog.Logger
	// owner is the hash of the host instance that registered the session
	// (hex), "" for a host that gave none: what ties a host's connections
	// together, for links over several of its sessions.
	owner string

	mu   sync.Mutex
	info session.Info
	// touched is the files the host's agent touched since the host
	// connected, from the `file` entries it reports (HostActivity): what
	// GET /api/touched lists for the session (#304). The host keeps the
	// session's own index; this one begins with the connection.
	touched        session.TouchedIndex
	relayOnly      bool
	agentTokenHash [32]byte
	hasAgentToken  bool
	conn           *HostConn
	viewers        map[string]*Viewer
	// gone are the viewers its host's leaving let go (HostDisconnected),
	// until whoever serves each removes it (RemoveViewer): a revoke of a
	// link still finds them, and stops what they are still written
	// (closeLink).
	gone map[*Viewer]struct{}
	// linksJoined are the links viewers joined the session through, the
	// oldest first (at most maxLinksJoined), each with the timer of its
	// expiry when it has one: the host is told when one of them no longer
	// opens the session (DisconnectLink, linkLapsed), whether a viewer is
	// still attached through it or not. Guarded by mu.
	linksJoined []string
	linkLapse   map[string]*time.Timer
	// links finds the linkState of a link viewers joined through, weakly:
	// one no viewer holds any more, nor a message of a viewer's still
	// queued for the host, goes, which bounds what it keeps; linksPrune is
	// the size past which the entries gone are swept. Guarded by mu.
	links          map[string]weak.Pointer[linkState]
	linksPrune     int
	disconnectedAt time.Time
	maxViewers     int
	// retired says the session left the server's registry (Retire): no
	// viewer is added any more.
	retired bool
	// events limits what the server forwards to the host on an agent's behalf
	// (ForwardActivity, and SetAttentionFull with forward): the host's
	// connection also carries its viewers' input, and closes when its queue is
	// full. Guarded by mu.
	events session.EventBucket
	// asked says the attention shown is the agent's own question (Ask, or
	// the host telling it so: HostAttention), which a done forwarded to the
	// host does not replace here, as it does not on the host's session.
	// Guarded by mu.
	asked bool
}

// Owner is the hash of the host instance that registered the session, "" when none.
func (h *HostedSession) Owner() string { return h.owner }

// Info returns the session description.
func (h *HostedSession) Info() session.Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	info := h.info
	info.Viewers = len(h.viewers)
	return info
}

// HostAbs is session.TouchedAbs for a path a host named: the host's OS may
// not be this server's, so a Windows path (a drive letter or a UNC prefix)
// is absolute here too, and a relative one under a Windows working
// directory joins it with the directory's own separator. It is the key a
// hosted session's touched index takes, and what the context providers
// are told of a hosted session's file (internal/api, extcontext.go).
func HostAbs(cwd, p string) string {
	if windowsPath(p) {
		return p
	}
	if windowsPath(cwd) {
		if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
			return p
		}
		return strings.TrimRight(cwd, `\/`) + `\` + strings.ReplaceAll(p, "/", `\`)
	}
	return session.TouchedAbs(cwd, p)
}

// windowsPath says p is a Windows path: a drive letter or a UNC prefix.
func windowsPath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') && (p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
}

// TouchedFiles is the files the host reported touched since it connected,
// the most recently touched first: the session's entry in GET /api/touched
// (#304). The paths are the host's, named as its agent reported them and
// made absolute under the session's working directory; no deny list of
// this server applies to another machine's files, and every path here was
// already on the events stream as a `file` entry.
func (h *HostedSession) TouchedFiles() []proto.TouchedFile {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.touched.Snapshot()
}

// Stop asks the host to terminate its process. Completion is reported
// asynchronously through a host status message.
func (h *HostedSession) Stop(ctx context.Context) error {
	h.mu.Lock()
	conn := h.conn
	id := h.info.ID
	h.mu.Unlock()
	if conn == nil {
		return ErrHostGone
	}
	if !conn.sendJSON(proto.HostStopMsg{T: proto.HostStop, SessionID: id}) {
		return ErrSlowHost
	}
	return nil
}

// maxLinksJoined bounds HostedSession.linksJoined, far above the links that
// can open a session at once (share.MaxLinksPerSession of its own and as
// many of its run's): a link past it pushes the oldest out, which the host
// is then told is gone (link_gone), so that nothing of the link's viewers is
// typed on the host after an end it would not hear of.
const maxLinksJoined = 1024

// joinedThrough notes that a viewer joined through linkID, which expires at
// until (none when zero): its expiry is timed here, on the server's clock
// (linkLapsed). It returns the link pushed out to make room, "" for none.
// The caller holds h.mu.
func (h *HostedSession) joinedThrough(linkID string, until time.Time) (pushedOut string) {
	if linkID == "" {
		return ""
	}
	if !slices.Contains(h.linksJoined, linkID) {
		if len(h.linksJoined) >= maxLinksJoined {
			pushedOut = h.linksJoined[0]
			h.forgetLink(pushedOut)
		}
		h.linksJoined = append(h.linksJoined, linkID)
	}
	if !until.IsZero() && h.linkLapse[linkID] == nil {
		if h.linkLapse == nil {
			h.linkLapse = map[string]*time.Timer{}
		}
		h.linkLapse[linkID] = time.AfterFunc(time.Until(until), func() { h.linkLapsed(linkID) })
	}
	return pushedOut
}

// forgetLink takes linkID out of linksJoined, its expiry's timer stopped,
// and reports whether it was there. The caller holds h.mu.
func (h *HostedSession) forgetLink(linkID string) bool {
	if t := h.linkLapse[linkID]; t != nil {
		t.Stop()
		delete(h.linkLapse, linkID)
	}
	n := len(h.linksJoined)
	h.linksJoined = slices.DeleteFunc(h.linksJoined, func(id string) bool { return id == linkID })
	return len(h.linksJoined) != n
}

// linkStateOf is linkID's linkState, the one a viewer or a queued message
// still holds or a new one; nil for no link. The caller holds h.mu.
func (h *HostedSession) linkStateOf(linkID string) *linkState {
	if linkID == "" {
		return nil
	}
	if st := h.links[linkID].Value(); st != nil {
		return st
	}
	if h.links == nil {
		h.links = map[string]weak.Pointer[linkState]{}
	}
	st := &linkState{}
	h.links[linkID] = weak.Make(st)
	if len(h.links) > max(h.linksPrune, 64) {
		for id, p := range h.links {
			if p.Value() == nil {
				delete(h.links, id)
			}
		}
		h.linksPrune = 2 * len(h.links)
	}
	return st
}

// linkWent marks linkID gone for whatever of its viewers' is still queued
// for the host, those that left among them, and forgets it: no viewer
// joins through it again. The caller holds h.mu.
func (h *HostedSession) linkWent(linkID string) {
	if st := h.links[linkID].Value(); st != nil {
		st.gone.Store(true)
	}
	delete(h.links, linkID)
}

// linkGone tells the host, when it is connected, that linkID no longer
// opens the session.
func (h *HostedSession) linkGone(conn *HostConn, linkID string) {
	if conn != nil && linkID != "" {
		conn.sendJSON(proto.LinkGone{T: proto.HostLinkGone, LinkID: linkID})
	}
}

// linkLapsed is a joined link reaching its expiry: the host is told it is
// gone, as for a revoke, on the server's clock, whether a viewer is still
// attached through it or not. Each one attached is closed with expired
// first, as its own lapse would, which may fire after this: nothing of
// theirs still queued for the host goes after the going (closeLink).
func (h *HostedSession) linkLapsed(linkID string) {
	h.mu.Lock()
	joined := h.forgetLink(linkID)
	conn := h.conn
	hit := h.linkViewers(linkID)
	h.linkWent(linkID)
	h.mu.Unlock()
	for _, v := range hit {
		v.closeForLink(session.ErrExpired)
	}
	if joined {
		h.linkGone(conn, linkID)
	}
}

// linkViewers are the viewers attached through linkID, those its host's
// leaving let go (gone) among them. The caller holds h.mu.
func (h *HostedSession) linkViewers(linkID string) []*Viewer {
	var hit []*Viewer
	for _, v := range h.viewers {
		if v.LinkID == linkID {
			hit = append(hit, v)
		}
	}
	for v := range h.gone {
		if v.LinkID == linkID {
			hit = append(hit, v)
		}
	}
	return hit
}

// DisconnectLink closes the viewers attached through linkID as revoked,
// whether or not the session has ended (a run's link keeps its viewers on a
// member that ended, until it is revoked): each is written nothing more
// (Pump). It finds them under the lock AddViewerWith registers them under,
// so a caller that marked the link first (a revoke) misses none: a viewer
// added after this finds the mark (ViewerOptions.Authorize). When a viewer
// ever joined through the link, the host is told the link went (link_gone),
// in the server's own lane, ahead of their leaves and of the viewers' lane:
// what a viewer of the link asked for and is still under way there (one
// that left once it had sent) is withdrawn. The link is marked gone and its
// viewers closed first (linkState, forLink), so nothing still queued for the
// host of a viewer of the link, there or gone already, follows the going: a
// join, a frame, an offer not yet written is dropped as the writer takes
// it, and a viewer whose join was dropped so is never known to the host. A
// link no viewer joined through is never named to the host. A host
// that is away, or whose connection ends before the message reaches it, is
// not told again: a host counts every link gone as it loses its
// connection.
func (h *HostedSession) DisconnectLink(linkID string) { h.closeLink(linkID, true) }

// EndLink closes the viewers attached through linkID, one of the session's
// own links, as the session's end, which ends its links with it (the
// caller marks the link first, as a revoke does), as a server session's
// are (session.Local.EndLink): each is still written what was queued for
// it, the session's last output, and then told the session ended (Pump).
// Otherwise it is DisconnectLink. The host, whose session has ended, is not
// told the link went.
func (h *HostedSession) EndLink(linkID string) { h.closeLink(linkID, false) }

// closeLink is DisconnectLink (revoked) and EndLink. A revoke also stops
// what the session's end still writes the link's viewers, should it have
// closed them first (finish). A revoke closes the link's viewers before it
// queues the link's going (DisconnectLink).
func (h *HostedSession) closeLink(linkID string, revoked bool) {
	h.mu.Lock()
	hit := h.linkViewers(linkID)
	joined := linkID != "" && h.forgetLink(linkID)
	conn := h.conn
	if revoked {
		h.linkWent(linkID)
	}
	h.mu.Unlock()
	for _, v := range hit {
		if revoked {
			v.closeForLink(session.ErrRevoked)
		} else {
			v.close(session.ErrSessionEnded)
		}
	}
	if joined && revoked {
		h.linkGone(conn, linkID)
	}
}

// AgentTokenOK compares a presented agent token in constant time.
func (h *HostedSession) AgentTokenOK(tok string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hasAgentToken || tok == "" {
		return false
	}
	sum := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(sum[:], h.agentTokenHash[:]) == 1
}

func (h *HostedSession) setAgentToken(tok string) {
	h.agentTokenHash = sha256.Sum256([]byte(tok))
	h.hasAgentToken = tok != ""
}

// SetAttention records an attention change without prompt details. See
// SetAttentionFull.
func (h *HostedSession) SetAttention(state session.AttentionState, message, source string, forward bool) error {
	return h.SetAttentionFull(state, message, source, "", nil, forward)
}

// SetAttentionFull records an attention change. When forward is true (API
// origin), the report spends a token of the session's bucket, the one
// ForwardActivity spends from, whether a host is connected or not: it returns
// ErrRateLimited, having changed nothing, when there is none. A connected host
// is told, so its viewers see the change too; without one the change is held
// on the server. A change from the host (forward false) never spends one.
func (h *HostedSession) SetAttentionFull(state session.AttentionState, message, source, kind string, options []session.Option, forward bool) error {
	return h.setAttention(state, message, source, kind, options, forward, "", false, "", "")
}

// Ask is SetAttentionFull, forwarded, for the agent's own question
// (session.Local.Ask): needs_input marked as the agent's, which the host is
// sent with the mark (proto.HostAttentionMsg.Ask), and which a done
// forwarded later does not replace here while it shows: the host's session
// keeps it through its turn's done, and tells its attention back after
// every report forwarded. A question the host has set aside shows here as
// the host tells it.
func (h *HostedSession) Ask(message, source, kind string, options []session.Option) error {
	return h.setAttention(session.AttentionNeedsInput, message, source, kind, options, true, "", true, "", "")
}

// HostAttention records an attention change the host reports (forward
// false): id is the id the host's session gave it (Attention.ID), kept
// when it is a question's id (proto.ValidQuestionID), so that an answer
// from the server's listings names the question the host holds; ask says
// it is the agent's own question (Attention.Asked); answered is its mark as
// one keys typed in the host's terminal have answered (Attention.Answered).
func (h *HostedSession) HostAttention(state session.AttentionState, message, source, kind string, options []session.Option, id string, ask bool, answered, plan string) {
	if !proto.ValidQuestionID(id) {
		id = ""
	}
	_ = h.setAttention(state, message, source, kind, options, false, id, ask, answered, plan)
}

// SetAttentionPlan is SetAttentionFull for a report that names the plan
// its question asks approval for (the attention route's plan, #289): it
// goes to the host with the question, whose session learns it as one of
// its plans and tells its attention back with it (HostAttention's plan);
// the server's copy carries it meanwhile.
func (h *HostedSession) SetAttentionPlan(state session.AttentionState, message, source, kind string, options []session.Option, forward bool, plan string) error {
	return h.setAttention(state, message, source, kind, options, forward, "", false, "", plan)
}

// setAttention is SetAttentionFull with the change's id: the host's, or,
// for a change the server makes (id ""), none, the host giving the change
// its own as it applies it and reporting it back (HostAttention). A
// forwarded change the host will not apply, the same as the one it holds,
// keeps that one's id. answered is the host's Attention.Answered, kept on a
// needs_input change (a question keys typed in the host's terminal have
// answered: AnsweredTerminal). ask marks a needs_input change as the agent's own
// question (Ask); a done forwarded while one shows is sent on to the host
// and changes nothing here.
func (h *HostedSession) setAttention(state session.AttentionState, message, source, kind string, options []session.Option, forward bool, id string, ask bool, answered, plan string) error {
	if !state.Valid() {
		return nil
	}
	ask = ask && state == session.AttentionNeedsInput
	if state != session.AttentionNeedsInput || answered != session.AnsweredTerminal {
		answered = ""
	}
	if state != session.AttentionNeedsInput || len(plan) > proto.MaxPlanPath {
		plan = ""
	}
	message = session.CleanMessage(message)
	if !session.ValidKind(kind) {
		kind = ""
	}
	options = session.CleanOptions(options)
	if state == session.AttentionNone {
		kind, options = "", nil
	}
	h.mu.Lock()
	conn := h.conn
	if forward && !h.events.Take(time.Now()) {
		h.mu.Unlock()
		return ErrRateLimited
	}
	att := session.Attention{State: state, Message: message, Source: source, Kind: kind, Options: options, Answered: answered, Plan: plan}
	if forward && state == session.AttentionDone && h.asked && h.info.Attention.State == session.AttentionNeedsInput {
		// The agent's own question outlasts its turn's done, here as on the
		// host, which is told the done and tells its attention back.
		h.mu.Unlock()
		if conn != nil {
			conn.sendJSON(hostAttentionMsg("", att, false))
		}
		return nil
	}
	if state != session.AttentionNone {
		now := time.Now().UTC()
		att.Since = &now
		att.ID = id
		if cur := h.info.Attention; forward && id == "" && sameAttention(cur, att) {
			// The host keeps what it shows (a change that changes nothing),
			// and reports nothing back: its question keeps its id here too,
			// and its mark.
			att.ID = cur.ID
			ask = ask || h.asked
		}
	}
	h.info.Attention = att
	h.asked = ask
	h.mu.Unlock()
	if forward && conn != nil {
		conn.sendJSON(hostAttentionMsg("", att, ask))
	}
	h.notifyChange()
	return nil
}

// sameAttention says whether a and b show the same, as a session takes a
// change (state, words, source, kind and choices), whatever their stamps.
func sameAttention(a, b session.Attention) bool {
	if a.State != b.State || a.Message != b.Message || a.Source != b.Source || a.Kind != b.Kind || a.Answered != b.Answered || a.Plan != b.Plan || len(a.Options) != len(b.Options) {
		return false
	}
	for i := range a.Options {
		if a.Options[i] != b.Options[i] {
			return false
		}
	}
	return true
}

// hostAttentionMsg encodes an attention change for the host control link,
// ask its mark as the agent's own question.
func hostAttentionMsg(sessionID string, att session.Attention, ask bool) proto.HostAttentionMsg {
	m := proto.HostAttentionMsg{T: proto.HostAttention, SessionID: sessionID, State: string(att.State), Message: att.Message, Source: att.Source, Kind: att.Kind, Ask: ask, Answered: att.Answered, Plan: att.Plan}
	for _, o := range att.Options {
		m.Options = append(m.Options, proto.AttentionOption{Label: o.Label, Input: o.Input, Mode: o.Mode})
	}
	return m
}

// OptionsFromProto converts wire options to session options.
func OptionsFromProto(in []proto.AttentionOption) []session.Option {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.Option, 0, len(in))
	for _, o := range in {
		out = append(out, session.Option{Label: o.Label, Input: o.Input, Mode: o.Mode})
	}
	return out
}

func (h *HostedSession) notifyChange() {
	if h.hub != nil && h.hub.OnChange != nil {
		h.hub.OnChange(h.Info())
	}
}

// RelayOnly reports whether the host asked viewers to skip WebRTC.
func (h *HostedSession) RelayOnly() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.relayOnly
}

// LinkStands says whether a link a viewer joined the session through still
// stands (linksJoined): the host may then have a line of that viewer's
// under way, also once it has left, which an older host types only while it
// hears from the server often (the api's hostKeepalive). A link that leaves
// the list goes with a link_gone to the host (revoked, expired, pushed out)
// or with the session's end.
func (h *HostedSession) LinkStands() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.linksJoined) > 0
}

// LinkViewers counts server-side viewers per share link id.
func (h *HostedSession) LinkViewers() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]int{}
	for _, v := range h.viewers {
		if v.LinkID != "" && v.Reason() == nil {
			out[v.LinkID]++
		}
	}
	return out
}

// Secret returns the resume secret handed to the host.
func (h *HostedSession) Secret() string { return h.secret }

// Connected reports whether a host connection is attached.
func (h *HostedSession) Connected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn != nil
}

// ViewersWaiting is how many of the viewers' messages wait in line for room
// in the host connection's queue (sendFrom); 0 with no connection.
func (h *HostedSession) ViewersWaiting() int {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return 0
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return len(conn.waiting)
}

// AddViewer registers a browser and notifies the host. linkLabel travels to
// the host for its roster; the link token never does. See AddViewerWith.
func (h *HostedSession) AddViewer(id string, role session.Role, linkID, linkLabel string) (*Viewer, error) {
	return h.AddViewerWith(ViewerOptions{ID: id, Role: role, LinkID: linkID, LinkLabel: linkLabel})
}

// ViewerOptions describe a browser joining a hosted session.
type ViewerOptions struct {
	ID        string
	Role      session.Role
	LinkID    string
	LinkLabel string
	// Authorize, when set, is session.AttachOptions.Authorize for a hosted
	// session: called under the session's lock, in the critical section
	// that registers the viewer, its role replaces Role and its error
	// refuses the viewer; a Grant with Until closes the viewer with
	// session.ErrExpired then. It must not call the session.
	Authorize func() (session.Grant, error)
	// Charge, when set, is told the size of each message the viewer sends
	// the host as the host's connection carries it (a relayed frame in its
	// envelope, an offer or a candidate as encoded for the host), before
	// the message waits for room: what the server bounds is what goes to
	// the host. It is called on the goroutine that sends (RelayToHost,
	// ForwardOffer, ForwardICE, StartRelay); an error drops the message
	// and is returned.
	Charge func(n int) error
	// ChargeJoin, when set, is told the size of the viewer's join and leave
	// together, as the host's connection carries them, before the join
	// goes: a viewer that comes and goes is charged both. It is called on a
	// goroutine of its own, not the one that sends, and must return once
	// stop (the viewer's closing, or its connection failing) is closed; an
	// error drops the join, and with it the leave.
	ChargeJoin func(stop <-chan struct{}, n int) error
}

// AddViewerWith registers a browser and notifies the host.
func (h *HostedSession) AddViewerWith(o ViewerOptions) (*Viewer, error) {
	role := o.Role
	h.mu.Lock()
	if h.retired {
		h.mu.Unlock()
		return nil, session.ErrSessionEnded
	}
	// The credential first: a link revoked meanwhile says so, whatever the
	// host's state.
	var until time.Time
	if o.Authorize != nil {
		g, err := o.Authorize()
		if err == nil && !g.Role.Valid() {
			err = errors.New("signal: invalid role")
		}
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		role, until = g.Role, g.Until
	}
	if h.conn == nil {
		h.mu.Unlock()
		return nil, ErrHostGone
	}
	if len(h.viewers) >= h.maxViewers {
		h.mu.Unlock()
		return nil, ErrTooManyViewer
	}
	v := newViewer(o.ID, role, o.LinkID, o.LinkLabel)
	v.charge = o.Charge
	v.conn = h.conn
	v.link = h.linkStateOf(o.LinkID)
	h.viewers[o.ID] = v
	pushedOut := h.joinedThrough(o.LinkID, until)
	if !until.IsZero() {
		v.lapse = time.AfterFunc(time.Until(until), func() { v.closeForLink(session.ErrExpired) })
	}
	conn := h.conn
	h.mu.Unlock()
	h.linkGone(conn, pushedOut)
	h.sendJoin(conn, v, proto.ViewerJoin{T: proto.HostViewerJoin, ViewerID: o.ID, Role: string(role), LinkID: o.LinkID, LinkLabel: o.LinkLabel}, o.ChargeJoin)
	h.notifyChange()
	return v, nil
}

// sendJoin sends the host v's join without holding up the caller, which
// goes on to serve the viewer (and so notices it leave). The join is the
// viewer's own message: charged to it with its leave (chargeJoin), and in
// the viewers' lane of the host's queue in its turn, so viewers coming and
// going never spend the room of the server's own messages. Without a charge
// it takes its place in line at once; with one, it does once charged.
// Either way it waits on a goroutine of its own, and every later message of
// the viewer's waits for it to be queued (Viewer.joined). It counts as sent
// once the writer has taken it to write (Viewer.joinSettled): one the
// writer drops, the viewer's link gone, never went.
func (h *HostedSession) sendJoin(conn *HostConn, v *Viewer, msg proto.ViewerJoin, chargeJoin func(stop <-chan struct{}, n int) error) {
	queued := func(ok bool) {
		v.joinQueued = ok
		if !ok {
			v.joinSettled(false)
		}
		close(v.joinDone)
	}
	join, err := jsonMarshal(msg)
	if err != nil {
		queued(false)
		return
	}
	o := Outbound{Text: join, of: v, join: true}
	if chargeJoin == nil {
		me := conn.enter(o)
		if me == nil {
			queued(true)
			return
		}
		go func() { queued(conn.await(me, o, v.stop) == nil) }()
		return
	}
	leave := leaveSize(v.ID)
	go func() {
		if chargeJoin(v.stop, len(join)+leave) != nil {
			queued(false)
			return
		}
		queued(conn.sendFrom(o, v.done, v.stop) == nil)
	}()
}

// leaveMsg is the viewer_leave of viewer id, closed for reason: with the
// reason when its link no longer opens the session (revoked, its run's link
// forgotten, expired), or its principal was revoked (Viewer.Revoke), so that the host withdraws what the viewer has under
// way rather than finish it; with none for any other (the viewer left, its
// connection failed, it was too slow).
func leaveMsg(id string, reason error) ([]byte, error) {
	return jsonMarshal(proto.ViewerLeave{T: proto.HostViewerLeave, ViewerID: id, Reason: leaveReason(reason)})
}

// leaveReason is the reason a viewer_leave gives for a viewer closed for
// reason, "" for none.
func leaveReason(reason error) string {
	switch {
	case errors.Is(reason, session.ErrExpired):
		return proto.LeaveExpired
	case errors.Is(reason, session.ErrRevoked):
		return proto.LeaveRevoked
	}
	return ""
}

// leaveSize is the size of viewer id's viewer_leave at most, its reason the
// longest, for the join's charge.
func leaveSize(id string) int {
	n := 0
	for _, reason := range []error{nil, session.ErrRevoked, session.ErrExpired} {
		if b, err := leaveMsg(id, reason); err == nil {
			n = max(n, len(b))
		}
	}
	return n
}

// RemoveViewer unregisters a browser and notifies the host, once the
// viewer's join went (the writer took it to write; one never queued, or
// dropped as the viewer's link went, before or after the viewer left, never
// went, and the host never knew the viewer: no leave follows it). While it
// waits for that, a revoke or expiry of its link still drops what of its is
// queued (linkState). A leave with no reason goes
// as the viewer's join did, in the viewers' lane of the host's queue
// (charged with the join), behind what the viewer sent before it: it waits
// for room until the host's connection closes, and is never dropped, for
// the host would keep a viewer it was not told left. A leave that says why,
// the viewer closed for its link (leaveMsg: the first reason it was closed
// for), goes in the server's own lane, behind the link's going. A viewer
// let go is written nothing more, whatever it was still owed (the
// session's end, its host's going): no revoke or expiry of its link would
// find it after.
func (h *HostedSession) RemoveViewer(v *Viewer) {
	h.mu.Lock()
	_, present := h.viewers[v.ID]
	delete(h.viewers, v.ID)
	delete(h.gone, v)
	conn := h.conn
	if v.lapse != nil {
		v.lapse.Stop()
	}
	v.revoked.Store(true)
	h.mu.Unlock()
	v.close(nil)
	if !present {
		return
	}
	// The join stops waiting as the viewer closes; one that never went
	// needs no leave. One queued goes, or is dropped, as the writer takes
	// it, unless the connection ends first.
	<-v.joinDone
	if conn != nil && conn == v.conn && v.joinQueued {
		select {
		case <-v.joinTaken:
		case <-conn.Done():
		}
	}
	if conn != nil && conn == v.conn && v.joinTakenNow() {
		reason := v.Reason()
		if leave, err := leaveMsg(v.ID, reason); err == nil {
			if leaveReason(reason) != "" {
				conn.send(Outbound{Text: leave})
			} else {
				_ = conn.sendFrom(Outbound{Text: leave}, nil, nil)
			}
		}
	}
	h.notifyChange()
}

// joinTakenNow reports whether the viewer's join went, settled by now.
func (v *Viewer) joinTakenNow() bool {
	select {
	case <-v.joinTaken:
		return v.joinWent
	default:
		return false
	}
}

// ForwardOffer sends a viewer's SDP offer to the host. Like every message a
// viewer sends the host, it waits while the viewers' lane of the host's
// queue is full (HostConn.sendFrom).
func (h *HostedSession) ForwardOffer(v *Viewer, sdp string) error {
	return h.fromViewer(v, proto.ViewerSDP{T: proto.HostOffer, ViewerID: v.ID, SDP: sdp})
}

// ForwardICE sends a viewer's ICE candidate to the host.
func (h *HostedSession) ForwardICE(v *Viewer, c proto.ICECandidate) error {
	return h.fromViewer(v, proto.ViewerICE{T: proto.HostICE, ViewerID: v.ID, Candidate: c})
}

// Tell sends the host a message of the server's own (a link_created, an
// error answering its request), in the server's own lane: ErrHostGone
// without a host, ErrSlowHost when the lane is full.
func (h *HostedSession) Tell(v any) error { return h.toHost(v) }

// StartRelay switches a viewer to relay mode and tells the host.
func (h *HostedSession) StartRelay(v *Viewer) error {
	v.relay.Store(true)
	return h.fromViewer(v, proto.ViewerRef{T: proto.HostRelayStart, ViewerID: v.ID})
}

// RelayToHost wraps a viewer frame in a RELAY envelope for the host. Input
// and resize from view-role viewers are dropped here as defence in depth.
// The envelope waits while the viewers' lane of the host's queue is full
// (HostConn.sendFrom): ErrSlowHost when the host's connection closes
// meanwhile, ErrViewerGone when the viewer is closed, or its connection
// fails (ConnFailed) while the envelope waits; one that needs no wait goes
// though the connection has failed.
func (h *HostedSession) RelayToHost(v *Viewer, inner proto.Frame) error {
	if !v.relay.Load() {
		return errors.New("signal: viewer is not in relay mode")
	}
	if v.Role != session.RoleControl {
		switch inner.Type {
		case proto.TypeInput, proto.TypeFileWrite:
			return session.ErrReadOnly
		case proto.TypeControl:
			// A chat post passes for every role (the host refuses one that is
			// also for the agent); typing a message into the agent does not.
			if t, _ := proto.ParseHeader(inner.Payload); t == proto.CtlResize || t == proto.CtlSubmit || t == proto.CtlAnswer || t == proto.CtlChatSend || t == proto.CtlNvimOpen || t == proto.CtlNvimInput || t == proto.CtlNvimClose || t == proto.CtlNvimSwap {
				return session.ErrReadOnly
			}
		}
	}
	env, err := proto.EncodeRelay(v.ID, proto.Encode(inner.Type, inner.Payload))
	if err != nil {
		return err
	}
	if err := v.joined(); err != nil {
		return err
	}
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return ErrHostGone
	}
	if err := v.chargeHost(len(env)); err != nil {
		return err
	}
	return conn.sendFrom(Outbound{Binary: env, of: v}, v.done, v.stop)
}

// fromViewer sends the host a message on v's behalf, charged as encoded
// (ViewerOptions.Charge), then queued (HostConn.sendFrom) as RelayToHost
// queues an envelope.
func (h *HostedSession) fromViewer(v *Viewer, msg any) error {
	b, err := jsonMarshal(msg)
	if err != nil {
		return err
	}
	if err := v.joined(); err != nil {
		return err
	}
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return ErrHostGone
	}
	if err := v.chargeHost(len(b)); err != nil {
		return err
	}
	return conn.sendFrom(Outbound{Text: b, of: v}, v.done, v.stop)
}

// toHost sends the host a message of the server's own, without waiting
// (HostConn.send).
func (h *HostedSession) toHost(v any) error {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return ErrHostGone
	}
	if !conn.sendJSON(v) {
		return ErrSlowHost
	}
	return nil
}

func (h *HostedSession) viewer(id string) *Viewer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.viewers[id]
}

// --- activity ---

// maxEntryBy bounds the subscriber id of an entry a host reports: ids are 16
// characters, and session.CleanEntry leaves By alone (see session.CleanID).
const maxEntryBy = 64

// ForwardActivity sends an event that an agent reported through the API to
// the host. A hosted session has no activity log on the server: the host
// records the event in its own and reports it back, which reaches
// HostActivity. e is cleaned again here, which changes nothing for an entry
// that was cleaned already, so that no caller can push the message past
// proto.MaxHostMessage, which the host takes as a protocol error.
//
// Each event spends a token of the session's bucket (see SetAttentionFull):
// the host's connection also carries its viewers' input and closes when its
// queue is full, and an event can be several KiB. It returns ErrHostGone when
// no host is connected, before it spends anything, ErrRateLimited when the
// bucket is empty and ErrSlowHost when the host's queue is full.
func (h *HostedSession) ForwardActivity(e session.ActivityEntry) error {
	msg := hostActivityMsg(session.CleanEntry(e))
	h.mu.Lock()
	conn := h.conn
	if conn == nil {
		h.mu.Unlock()
		return ErrHostGone
	}
	if !h.events.Take(time.Now()) {
		h.mu.Unlock()
		return ErrRateLimited
	}
	h.mu.Unlock()
	if !conn.sendJSON(msg) {
		return ErrSlowHost
	}
	return nil
}

// HostActivity takes an activity entry the host reports, with the attention
// state the host says an attention entry records, and hands them to the hub's
// OnActivity. The host is not trusted with them: an entry of a type this
// server does not know is dropped (a newer host may have more), the text is
// cut to its limits, By included (a By still over maxEntryBy bytes once
// cleaned is dropped), and a missing or unreadable time becomes the time of
// receipt; the state is kept only with an attention entry and only when it is
// needs_input, working or done. The entry is attributed to this session
// whatever session the host named. HostActivity does not send the entry back
// to the host.
func (h *HostedSession) HostActivity(a proto.Activity, state string) {
	if !session.ValidEventType(a.Type) {
		return
	}
	e := session.CleanEntry(session.EntryFromProto(a))
	e.By = session.CleanID(e.By, maxEntryBy)
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	var recorded session.AttentionState
	if e.Type == session.ActivityAttention {
		switch s := session.AttentionState(state); s {
		case session.AttentionNeedsInput, session.AttentionWorking, session.AttentionDone:
			recorded = s
		}
	}
	h.mu.Lock()
	id := h.info.ID
	// A file entry (its op and path kept by the cleaning) feeds the
	// session's touched index on the server (#304).
	if e.Type == session.ActivityFile && e.Op != "" && e.Path != "" {
		h.touched.Note(HostAbs(h.info.Cwd, e.Path), e.Op, e.Tool, e.ByName, e.At)
	}
	h.mu.Unlock()
	if h.hub != nil && h.hub.OnActivity != nil {
		h.hub.OnActivity(id, e, recorded)
	}
}

// HostChat takes a chat message the host's session kept and hands it to the
// hub's OnChat, cleaned as the host is not trusted with it: a kind this
// server does not know is dropped, the scope is this session's (a host has
// no run), the ids are bounded, the names cleaned, the text cleaned and cut
// to proto.MaxChatText, a missing or unreadable time becomes the time of
// receipt, and `to` is the agent or nothing. The message is attributed to
// this session whatever session the host named. Nothing goes back to the
// host.
func (h *HostedSession) HostChat(m proto.ChatMessage) {
	switch m.Kind {
	case proto.ChatKindMessage, proto.ChatKindSystem, proto.ChatKindSentToAgent:
	default:
		return
	}
	id := session.CleanID(m.ID, maxEntryBy)
	role := session.Role(m.By.Role)
	if id == "" || !role.Valid() {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, m.At)
	if err != nil {
		at = time.Now().UTC()
	}
	text := session.CleanChatText(m.Text)
	for len(text) > proto.MaxChatText {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	out := session.ChatMessage{
		ID:    id,
		At:    at.UTC(),
		Scope: proto.ChatScopeSession,
		Kind:  m.Kind,
		By:    session.ChatBy{ID: session.CleanID(m.By.ID, maxEntryBy), Name: session.CleanName(m.By.Name), Role: role},
		Text:  strings.TrimSpace(text),
		Ref:   session.CleanID(m.Ref, maxEntryBy),
	}
	if m.To == proto.ChatToAgent {
		out.To = m.To
	}
	if m.Event == "join" || m.Event == "leave" {
		out.Event = m.Event
	}
	h.mu.Lock()
	sid := h.info.ID
	h.mu.Unlock()
	if h.hub != nil && h.hub.OnChat != nil {
		h.hub.OnChat(sid, out)
	}
}

// hostActivityMsg wraps an entry in the host `activity` message. The server
// names no session: the connection says which.
func hostActivityMsg(e session.ActivityEntry) proto.HostActivityMsg {
	return proto.HostActivityMsg{T: proto.HostActivity, Entry: session.EntryToProto(e)}
}

// --- messages from the host ---

// AnswerFrame is the SIGNAL frame HostAnswer queues for a viewer, and
// ICEFrame the one HostICE does: the host's message encoded anew, which can
// be larger than the host sent it (json.Marshal writes a '<' as six bytes).
// One past what a SIGNAL frame holds (proto.MaxSignal), which no reader
// would take (proto.Decode), is never made: proto.ErrTooLarge, for the
// caller to tell the viewer (RefusedFrame). A bound on what the host sends
// charges it before it is queued (HostSignal).
func AnswerFrame(sdp string) ([]byte, error) {
	return signalFrame(proto.SDP{T: proto.SigAnswer, SDP: sdp})
}

// ICEFrame: see AnswerFrame.
func ICEFrame(c proto.ICECandidate) ([]byte, error) {
	return signalFrame(proto.ICE{T: proto.SigICE, Candidate: c})
}

// signalFrame encodes a signaling message as a SIGNAL frame a reader would
// take: within proto.MaxSignal, else proto.ErrTooLarge.
func signalFrame(v any) ([]byte, error) {
	f, err := proto.EncodeJSON(proto.TypeSignal, v)
	if err != nil {
		return nil, err
	}
	if _, err := proto.Decode(f); err != nil {
		return nil, err
	}
	return f, nil
}

// RefusedMessage is what a viewer is told in place of a SIGNAL frame the
// server could not make for it (AnswerFrame, ICEFrame: proto.ErrTooLarge),
// in a CONTROL error of code proto.ErrCodeBadFrame: what the host sent
// (`answer`, `ice`) was not sent, and why. The viewer's WebRTC attempt
// cannot succeed without the answer: with a relay to fall back to it is
// kept, its timer sending it there (HostSignal); without one it is closed
// with the error (HostViewerError).
func RefusedMessage(what string, err error) string {
	return fmt.Sprintf("the host's %s was not sent: %v", what, err)
}

// HostSignal queues a SIGNAL frame AnswerFrame or ICEFrame made for a
// viewer, or the CONTROL error sent in place of one (RefusedMessage).
func (h *HostedSession) HostSignal(viewerID string, f []byte) {
	if v := h.viewer(viewerID); v != nil {
		v.push(f)
	}
}

// HostAnswer delivers an SDP answer to a viewer.
func (h *HostedSession) HostAnswer(viewerID, sdp string) {
	if f, err := AnswerFrame(sdp); err == nil {
		h.HostSignal(viewerID, f)
	}
}

// HostICE delivers a host ICE candidate to a viewer.
func (h *HostedSession) HostICE(viewerID string, c proto.ICECandidate) {
	if f, err := ICEFrame(c); err == nil {
		h.HostSignal(viewerID, f)
	}
}

// HostRelayFrame delivers a relayed terminal frame to a viewer.
func (h *HostedSession) HostRelayFrame(viewerID string, inner proto.Frame) {
	if v := h.viewer(viewerID); v != nil {
		v.push(proto.Encode(inner.Type, inner.Payload))
	}
}

// HostViewerError forwards a per-viewer error and closes the viewer. The
// viewer is sent proto.NewError(code, message).
func (h *HostedSession) HostViewerError(viewerID, code, message string) {
	if v := h.viewer(viewerID); v != nil {
		v.push(proto.NewError(code, message))
		v.close(errors.New(code))
	}
}

// HostViewerClosed closes a viewer at the host's request.
func (h *HostedSession) HostViewerClosed(viewerID string) {
	if v := h.viewer(viewerID); v != nil {
		v.close(ErrViewerGone)
	}
}

// HostStatus records a status change reported by the host.
func (h *HostedSession) HostStatus(status session.Status, exitCode *int) {
	h.mu.Lock()
	h.info.Status = status
	h.info.ExitCode = exitCode
	if status.Ended() {
		if h.info.EndedAt == nil {
			now := time.Now().UTC()
			h.info.EndedAt = &now
		}
		// An ended session needs nothing, whatever the host said before. A
		// host that only went away (host_disconnected) may come back: its
		// session keeps its state.
		h.info.Attention = session.Attention{}
		h.asked = false
	}
	h.mu.Unlock()
	h.notifyChange()
}

// HostResize records the host's current PTY size.
func (h *HostedSession) HostResize(cols, rows uint16) {
	h.mu.Lock()
	h.info.Cols, h.info.Rows = cols, rows
	h.mu.Unlock()
}

// HostDisconnected detaches the control connection and closes every viewer,
// keeping them where a revoke of a link still finds them (gone) until each
// is removed.
func (h *HostedSession) HostDisconnected(conn *HostConn) {
	h.mu.Lock()
	if h.conn != conn {
		h.mu.Unlock()
		return
	}
	h.conn = nil
	h.disconnectedAt = time.Now()
	if !h.info.Status.Ended() {
		h.info.Status = session.StatusHostDisconnected
	}
	viewers := make([]*Viewer, 0, len(h.viewers))
	if h.gone == nil {
		h.gone = map[*Viewer]struct{}{}
	}
	for _, v := range h.viewers {
		viewers = append(viewers, v)
		h.gone[v] = struct{}{}
	}
	h.viewers = map[string]*Viewer{}
	h.mu.Unlock()
	// Each viewer is closed with ErrHostGone: Pump writes what the host still
	// owed it, then closes its sink, which tells the viewer why with one error
	// frame (host_disconnected).
	for _, v := range viewers {
		v.close(ErrHostGone)
	}
	conn.Close()
	h.notifyChange()
}

// Retire is the session leaving the server's registry: every viewer is
// closed as at the session's end, and none is added after
// (session.ErrSessionEnded). Whoever closes a link's viewers finds them
// through the registry, so none may stay where it no longer looks: none is
// written anything more, whatever it was owed (the session's end of its
// links, or its host's going, may still be writing theirs), as a revoke
// would stop it.
func (h *HostedSession) Retire() {
	h.mu.Lock()
	h.retired = true
	for id := range h.linkLapse {
		h.forgetLink(id)
	}
	viewers := make([]*Viewer, 0, len(h.viewers)+len(h.gone))
	for _, v := range h.viewers {
		viewers = append(viewers, v)
	}
	for v := range h.gone {
		viewers = append(viewers, v)
	}
	h.mu.Unlock()
	for _, v := range viewers {
		v.revoked.Store(true)
		v.close(session.ErrSessionEnded)
	}
}

// CloseViewers disconnects every viewer with reason (server shutdown).
func (h *HostedSession) CloseViewers(reason error) {
	h.mu.Lock()
	viewers := make([]*Viewer, 0, len(h.viewers))
	for _, v := range h.viewers {
		viewers = append(viewers, v)
	}
	h.mu.Unlock()
	for _, v := range viewers {
		v.close(reason)
	}
}
