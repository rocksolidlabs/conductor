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

// laneConn is one viewer's side of the lane; nil for a viewer of no
// decision link.
type laneConn struct {
	d                 Decisions
	ctx               context.Context
	cancel            context.CancelFunc
	linkID, sessionID string
	send              func([]byte)
	// stands is the connection's own check of its viewer, asked at each
	// decide's commit boundary (Decisions.Decide); nil for none.
	stands         func() error
	decides, reads atomic.Int32
	wg             sync.WaitGroup
	stopWatch      func()
	seq            atomic.Int64
}

// openLane opens the lane of the decision link linkID for a viewer: its
// requests are answered through d, sessionID the server's own id of the
// session the viewer holds ("" for a desk), and its replies sent with send;
// stands is the viewer's own check, which each decide's operation asks at
// its commit boundary (the peer's credential and the host's vouch, as a
// terminal write asks them).
func openLane(d Decisions, linkID, sessionID string, send func([]byte), stands func() error) *laneConn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &laneConn{d: d, ctx: ctx, cancel: cancel, linkID: linkID, sessionID: sessionID, send: send, stands: stands}
	c.stopWatch = d.Watch(linkID, sessionID, func() {
		send(proto.MustControl(proto.DecisionsChanged{T: proto.CtlDecisionsChanged, Seq: c.seq.Add(1)}))
	})
	return c
}

// end withdraws the requests not begun, stops the watch and waits for
// those under way: one begun ends as its operation does.
func (c *laneConn) end() {
	if c == nil {
		return
	}
	c.cancel()
	c.stopWatch()
	c.wg.Wait()
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
	c.wg.Go(func() {
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
	})
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
	c.wg.Go(func() {
		defer c.decides.Add(-1)
		c.send(c.d.Decide(c.ctx, c.linkID, c.sessionID, m, nil, c.stands))
	})
}
