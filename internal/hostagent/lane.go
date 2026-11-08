package hostagent

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A decision link's lane over a publisher's connection through a
// switchyard (docs/plans/decision-inbox.md, PR-6): a published session's
// viewer whose link is a decision link minted there (peer.go), or a run
// desk's viewer (desk.go). The publisher's Decisions (the server's API)
// answers each request; this side keeps the connection's bounds, at most
// proto.MaxDecideInFlight decides and proto.MaxDecisionReadsInFlight
// reads under way, and tells the viewer decisions_changed as the
// Decisions' watch says. A host that serves no decisions (`conductor
// host`, Options.Decisions nil) refuses every lane message unsupported.

// laneGroup is the one context tree of a publisher's connection (a
// published session's agent, or a run's desk) that its viewers' lanes
// live under: every lane's worker starts under its context and is counted
// in its WaitGroup, and the connection's end (end) waits for them all
// before it reports itself ended (Published.Done, Desk.Done). Admission
// and teardown are decided under its one lock: a lane closed (its viewer
// gone, closeFor) or a group ending begins nothing more, and the request
// is refused with words, so no worker is added to a WaitGroup already
// being waited for. The zero value is ready.
type laneGroup struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	ending bool
	under  int
	wg     sync.WaitGroup
	// beforeAdmit, when set (tests), runs as a request about to be
	// admitted has its lane, before the group's lock is taken.
	beforeAdmit func()
	// waiting, when set (tests), is told how many workers the end waits
	// for, as it begins to wait.
	waiting func(n int)
}

// rootLocked is the group's context, made at the first lane; the caller
// holds g.mu.
func (g *laneGroup) rootLocked() context.Context {
	if g.ctx == nil {
		g.ctx, g.cancel = context.WithCancel(context.Background())
	}
	return g.ctx
}

// end refuses every request from now on, withdraws those not begun (the
// group's context), and waits for the workers under way: one begun ends as
// its operation does.
func (g *laneGroup) end() {
	g.mu.Lock()
	g.ending = true
	g.rootLocked()
	cancel, n, seam := g.cancel, g.under, g.waiting
	g.mu.Unlock()
	cancel()
	if seam != nil {
		seam(n)
	}
	g.wg.Wait()
}

// pending is how many workers are under way.
func (g *laneGroup) pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.under
}

// laneConn is one viewer's side of the lane; nil for a viewer of no
// decision link.
type laneConn struct {
	g                 *laneGroup
	d                 Decisions
	ctx               context.Context
	cancel            context.CancelFunc
	linkID, sessionID string
	send              func([]byte)
	// stands is the connection's own check of its viewer, asked at each
	// decide's commit boundary (Decisions.Decide); nil for none.
	stands         func() error
	decides, reads atomic.Int32
	// closed is set as the viewer goes (close); guarded by g.mu.
	closed    bool
	stopWatch func()
	seq       atomic.Int64
}

// openLane opens, under the group g, the lane of the decision link linkID
// for a viewer: its requests are answered through d, sessionID the
// server's own id of the session the viewer holds ("" for a desk), and
// its replies sent with send; stands is the viewer's own check, which
// each decide's operation asks at its commit boundary (the peer's
// credential and the host's vouch, as a terminal write asks them). A lane
// opened as its group ends is closed from the start: it refuses
// everything, and watches nothing.
func openLane(g *laneGroup, d Decisions, linkID, sessionID string, send func([]byte), stands func() error) *laneConn {
	g.mu.Lock()
	ctx, cancel := context.WithCancel(g.rootLocked())
	c := &laneConn{g: g, d: d, ctx: ctx, cancel: cancel, linkID: linkID, sessionID: sessionID, send: send, stands: stands, closed: g.ending}
	g.mu.Unlock()
	if c.closed {
		cancel()
		c.stopWatch = func() {}
		return c
	}
	c.stopWatch = d.Watch(linkID, sessionID, func() {
		send(proto.MustControl(proto.DecisionsChanged{T: proto.CtlDecisionsChanged, Seq: c.seq.Add(1)}))
	})
	return c
}

// close ends the lane as its viewer goes: nothing of it is begun from now
// on, those not begun are withdrawn and its watch stops; those under way
// end as their operations do, and the group's end waits for them.
func (c *laneConn) close() {
	if c == nil {
		return
	}
	c.g.mu.Lock()
	c.closed = true
	c.g.mu.Unlock()
	c.cancel()
	c.stopWatch()
}

// laneEnding are the words of a request refused because its lane, or its
// connection's, is ending, under the code an operation that cannot be done
// now refuses with (unavailable, as the server's lane says it:
// docs/protocol.md, decide_refused).
const (
	laneEnding      = "this connection is ending: nothing more is begun on it"
	laneUnavailable = "unavailable"
)

// start runs work as one of the lane's workers, admitted under the
// group's lock: false, and nothing run, once the lane is closed or its
// group ending.
func (c *laneConn) start(work func()) bool {
	g := c.g
	if g.beforeAdmit != nil {
		g.beforeAdmit()
	}
	g.mu.Lock()
	if c.closed || g.ending {
		g.mu.Unlock()
		return false
	}
	g.under++
	g.wg.Add(1)
	g.mu.Unlock()
	go func() {
		defer func() {
			g.mu.Lock()
			g.under--
			g.mu.Unlock()
			g.wg.Done()
		}()
		work()
	}()
	return true
}

// laneRefusal is a decide_refused naming reqID.
func laneRefusal(reqID, code, words string) []byte {
	return proto.MustControl(proto.DecideRefused{T: proto.CtlDecideRefused, ReqID: reqID, Code: code, Message: words})
}

// noLane is the refusal of a lane message on a connection with no lane:
// unsupported where this host serves no decisions, not_decision_link
// where the viewer's link is none.
func noLane(served bool, reqID string) []byte {
	if !served {
		return laneRefusal(reqID, proto.DecideUnsupported, "this host answers no decisions")
	}
	return laneRefusal(reqID, proto.DecideNotDecisionLink, "this connection is no decision link's")
}

// laneControl serves a lane control on c: true when payload is one (a
// connection with no lane refused, noLane), false to leave it to the
// connection's other controls.
func laneControl(c *laneConn, served bool, payload []byte, send func([]byte)) bool {
	t, err := proto.ParseHeader(payload)
	if err != nil {
		return false
	}
	msg, reqID, isLane, err := proto.DecodeLane(t, payload)
	switch {
	case !isLane:
		return false
	case c == nil:
		send(noLane(served, reqID))
		return true
	case err != nil:
		send(laneRefusal(reqID, proto.DecideInvalid, err.Error()))
		return true
	}
	if c.reads.Add(1) > proto.MaxDecisionReadsInFlight {
		c.reads.Add(-1)
		c.send(laneRefusal(reqID, proto.DecideTooMany, "four reads are under way on this connection"))
		return true
	}
	if !c.start(func() {
		defer c.reads.Add(-1)
		var f []byte
		switch m := msg.(type) {
		case *proto.DecisionsList:
			f = c.d.List(c.ctx, c.linkID, c.sessionID, *m)
		case *proto.DecisionRead:
			f = c.d.Read(c.ctx, c.linkID, c.sessionID, *m)
		case *proto.DecisionTrack:
			f = c.d.Track(c.ctx, c.linkID, c.sessionID, *m)
		case *proto.DecisionRecord:
			f = c.d.Record(c.ctx, c.linkID, c.sessionID, *m)
		case *proto.DecisionEvidence:
			f = c.d.Evidence(c.ctx, c.linkID, c.sessionID, *m)
		}
		c.send(f)
	}) {
		c.reads.Add(-1)
		c.send(laneRefusal(reqID, laneUnavailable, laneEnding))
	}
	return true
}

// laneDecide serves a TypeDecide frame on c (a connection with no lane
// refused, noLane). Every answer goes through the question's claim as the
// link (Decisions.Decide with no own answer): the link's name, request id
// and hold, whichever session asked; the operation asks the viewer's own
// check (c.stands) beside the link's at its commit boundary, so a viewer
// whose link the host no longer holds, or whose host no longer hears its
// switchyard, acts on nothing, as its terminal writes do not.
func laneDecide(c *laneConn, served bool, payload []byte, send func([]byte)) {
	reqID := proto.DecideReqID(payload)
	if c == nil {
		send(noLane(served, reqID))
		return
	}
	m, err := proto.DecodeDecide(payload)
	if err != nil {
		c.send(laneRefusal(reqID, proto.DecideInvalid, err.Error()))
		return
	}
	if c.decides.Add(1) > proto.MaxDecideInFlight {
		c.decides.Add(-1)
		c.send(laneRefusal(m.ReqID, proto.DecideTooMany, "four decisions are under way on this connection"))
		return
	}
	if !c.start(func() {
		defer c.decides.Add(-1)
		c.send(c.d.Decide(c.ctx, c.linkID, c.sessionID, m, nil, c.stands))
	}) {
		c.decides.Add(-1)
		c.send(laneRefusal(m.ReqID, laneUnavailable, laneEnding))
	}
}
