package hostagent

import (
	"encoding/json"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// decides says whether the viewer joined through a decision link the
// publisher minted at the switchyard that opens a lane here: of the
// published session, or (runID) of the desk's run. Only a control link
// can be one; a host serving no decisions has none.
func (p *peer) decides(runID string) bool {
	d := p.a.opts.Decisions
	if d == nil || p.linkID == "" || p.role != session.RoleControl {
		return false
	}
	if runID != "" {
		return d.DecisionLink(p.linkID, "", runID)
	}
	return d.DecisionLink(p.linkID, p.a.local.Info().ID, "")
}

// laneOf is the viewer's lane, nil for none.
func (p *peer) laneOf() *laneConn {
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

// deskSend writes f to a desk's viewer over the transport its last hello
// came on, one frame at a time (a decision is chunked on a data channel).
func (p *peer) deskSend(f []byte) {
	p.deskMu.Lock()
	defer p.deskMu.Unlock()
	p.mu.Lock()
	sink := p.deskSink
	p.mu.Unlock()
	if sink != nil {
		_ = sink.WriteFrame(f)
	}
}

// handleDesk serves a desk's viewer: its hello (welcomed with decisions,
// no terminal), its pings, and the lane; anything else is a protocol
// error, for a desk carries no terminal.
func (p *peer) handleDesk(f proto.Frame, via *dcSink) {
	served := p.a.opts.Decisions != nil
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
		p.lane = openLane(p.a.opts.Decisions, p.linkID, "", p.deskSend)
	}
	p.mu.Unlock()
}
