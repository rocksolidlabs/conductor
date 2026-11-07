package hostagent

import (
	"encoding/json"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// servesDecisions says whether this connection serves decision links'
// lanes now: the publisher gave it Decisions, and the switchyard's last
// registered said it carries decisions. A switchyard that came back
// without saying so (rolled back, or built without them) is sent no lane:
// no viewer is welcomed with decisions and no TypeDecision frame goes into
// its relay, whose envelope an older one refuses by closing the host's
// connection.
func (a *agent) servesDecisions() bool {
	return a.opts.Decisions != nil && a.decisions.Load()
}

// decides says whether the viewer joined through a decision link the
// publisher minted at the switchyard that opens a lane here: of the
// published session, or (runID) of the desk's run. Only a control link
// can be one; a host serving no decisions now has none.
func (p *peer) decides(runID string) bool {
	if !p.a.servesDecisions() || p.linkID == "" || p.role != session.RoleControl {
		return false
	}
	d := p.a.opts.Decisions
	if runID != "" {
		return d.DecisionLink(p.linkID, "", runID)
	}
	return d.DecisionLink(p.linkID, p.a.local.Info().ID, "")
}

// laneStands is the viewer's own check at a decide's commit boundary
// (Decisions.Decide's stands): its link still held here (authorize: the
// switchyard said nothing of its going, and did not let the viewer go),
// and the host vouching for its switchyard (fresh), which would have told
// it of a revoke there; as a terminal write asks them. It takes no lock
// but the connection's own.
func (p *peer) laneStands() error {
	if _, err := p.authorize(); err != nil {
		return err
	}
	return p.fresh()
}

// laneOf is the viewer's lane, nil for none (and while this connection
// serves no decisions: every lane message is refused then).
func (p *peer) laneOf() *laneConn {
	if !p.a.servesDecisions() {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lane
}

// laneSend writes a lane reply to the viewer: on its subscription for a
// session's viewer, in its turn among what the session writes it; on its
// transport for a desk's. Nothing once it has neither.
func (p *peer) laneSend(f []byte) {
	if p.a.desk != nil {
		p.deskSend(f)
		return
	}
	p.mu.Lock()
	sub := p.sub
	p.mu.Unlock()
	if sub != nil {
		p.a.local.Send(sub, f)
	}
}

// deskQueue bounds the frames waiting to be written to a desk's viewer:
// what its lane has under way at once (four reads, four decides), a
// decisions_changed, a pong, the welcome, many times over.
const deskQueue = 64

// deskSend queues f for a desk's viewer, written by its own writer
// (deskWriter) over the transport its last hello came on, one frame at a
// time (a decision is chunked on a data channel), never on the host's
// read loop: a write that waits for the relay's room (viewer_behind,
// which that loop reads) holds nothing of the host's. A viewer whose
// queue is full reads nothing of what it asked: it is let go
// (slow_consumer), not waited for.
func (p *peer) deskSend(f []byte) {
	p.deskOnce.Do(func() {
		p.deskQ = make(chan []byte, deskQueue)
		go p.deskWriter(p.deskQ)
	})
	select {
	case <-p.left:
		return
	default:
	}
	select {
	case p.deskQ <- f:
	default:
		p.deskLetGo("the viewer reads too slowly")
	}
}

// deskWriter writes a desk's viewer's frames in their order, until the
// viewer leaves; a write that fails on the viewer's transport lets it go.
// One with no transport yet (before its hello, or while it moves to the
// relay) is dropped, as a session's viewer's would be.
func (p *peer) deskWriter(q <-chan []byte) {
	for {
		select {
		case <-p.left:
			return
		case f := <-q:
			p.mu.Lock()
			sink := p.deskSink
			p.mu.Unlock()
			if sink == nil {
				continue
			}
			if err := sink.WriteFrame(f); err != nil {
				p.mu.Lock()
				current := p.deskSink == sink
				p.mu.Unlock()
				if current {
					// Not a transport it has left (its data channel as it
					// moved to the relay): it is gone.
					p.deskLetGo("the viewer's connection failed: " + err.Error())
					return
				}
			}
		}
	}
}

// deskLetGo lets a desk's viewer go as a slow consumer: through the
// switchyard, which closes it and tells this host it left; here at once
// when there is none to tell.
func (p *peer) deskLetGo(why string) {
	if !p.a.sendViewerError(p.id, proto.ErrCodeSlowConsumer, why) {
		go p.close()
	}
}

// handleDesk serves a desk's viewer: its hello (welcomed with decisions,
// no terminal), its pings, and the lane; anything else is a protocol
// error, for a desk carries no terminal.
func (p *peer) handleDesk(f proto.Frame, via *dcSink) {
	served := p.a.servesDecisions()
	switch f.Type {
	case proto.TypeControl:
		t, err := proto.ParseHeader(f.Payload)
		if err != nil {
			return
		}
		switch t {
		case proto.CtlHello:
			p.deskAttach(f.Payload, via)
		case proto.CtlPing:
			var m proto.Ping
			_ = json.Unmarshal(f.Payload, &m)
			p.deskSend(proto.MustControl(proto.Ping{T: proto.CtlPong, TS: m.TS}))
		default:
			if !laneControl(p.laneOf(), served, f.Payload, p.deskSend) {
				p.refuse(via, "a desk takes the lane's messages alone")
			}
		}
	case proto.TypeDecide:
		laneDecide(p.laneOf(), served, f.Payload, p.deskSend)
	default:
		p.refuse(via, "a desk takes the lane's messages alone")
	}
}

// deskAttach welcomes a desk's viewer over the transport its hello came
// on: a run decision link's alone, refused not_decision_link otherwise.
func (p *peer) deskAttach(payload []byte, via *dcSink) {
	var hello proto.Hello
	if json.Unmarshal(payload, &hello) != nil || hello.Proto != proto.ProtoVersion {
		p.a.sendViewerError(p.id, proto.ErrCodeBadFrame, "unsupported hello")
		return
	}
	if !p.decides(p.a.desk.ID) {
		p.a.sendViewerError(p.id, proto.DecideNotDecisionLink, "a desk is a run decision link's")
		return
	}
	p.mu.Lock()
	sink := p.transport(via)
	if sink == nil || p.closed {
		p.mu.Unlock()
		return
	}
	p.deskSink = sink
	p.mu.Unlock()
	transport := proto.TransportRelay
	if t, ok := sink.(interface{ Transport() string }); ok {
		transport = t.Transport()
	}
	// The welcome first; the lane, which may tell the viewer
	// decisions_changed, after it. The viewer's next frame is read once
	// this returns.
	p.deskSend(proto.MustControl(proto.Welcome{T: proto.CtlWelcome, Proto: proto.ProtoVersion, Role: string(session.RoleControl), Transport: transport, Decisions: true}))
	p.mu.Lock()
	if p.lane == nil && !p.closed {
		p.lane = openLane(p.a.opts.Decisions, p.linkID, "", p.deskSend, p.laneStands)
	}
	p.mu.Unlock()
}
