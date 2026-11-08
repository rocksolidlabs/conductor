package signal

import (
	"errors"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// A run's desk (docs/plans/decision-inbox.md, PR-6; docs/protocol.md,
// Decision links): a host connection of a publisher's that carries no
// session, only a decision link's lane, for a run with a live decision
// link here, so the run's decisions are reached with no member alive. It
// is a HostedSession whose desk names the run: its viewers join, signal,
// relay and leave as a session's do, its host resumes it with its secret
// and registers it again after this switchyard restarts (the same
// instance and local id give the same id), but it is never in the
// registry, so no listing shows it; it holds a place among the sessions
// (a registry reservation) while it lives, so it counts against the
// session limit; and a run link's group names it (proto.RunGroup.Desk),
// which is how a run link's viewer reaches it.

// ErrNoInstance refuses a desk whose host gave no instance (no_instance).
var ErrNoInstance = errors.New("signal: a desk needs a host that registers with an instance")

// RegisterDesk creates a desk (or resumes one, or takes back one held
// under its id while its host was away) and attaches conn: ErrNoInstance
// without an instance, ErrBadRegister for a message out of bounds or a
// desk held live by another connection or another owner, ErrBadResume
// for a resume this hub cannot honour, session.ErrTooManySessions when
// no place is left among the sessions.
func (hub *Hub) RegisterDesk(m proto.RegisterDesk, conn *HostConn) (*HostedSession, bool, error) {
	if err := m.Validate(); err != nil {
		if errors.Is(err, proto.ErrNoInstance) {
			return nil, false, ErrNoInstance
		}
		return nil, false, ErrBadRegister
	}
	reg := proto.Register{Host: m.Host}
	if m.Resume != nil {
		hub.mu.Lock()
		hs, ok := hub.sessions[m.Resume.SessionID]
		hub.mu.Unlock()
		if !ok || hs.desk == "" || !share.Equal(m.Resume.Secret, hs.secret) || !hs.attach(reg, conn) {
			return nil, false, ErrBadResume
		}
		return hs, true, nil
	}
	id := HostedID(m.Host.Instance, m.LocalID)
	owner := ownerOf(m.Host.Instance)
	hub.mu.Lock()
	hs, ok := hub.sessions[id]
	hub.mu.Unlock()
	if ok {
		if hs.desk == "" || hs.owner != owner || !hs.attach(reg, conn) {
			return nil, false, ErrBadRegister
		}
		return hs, true, nil
	}
	place, err := hub.registry.Reserve()
	if err != nil {
		return nil, false, err
	}
	secret, _ := share.NewToken()
	hs = &HostedSession{
		hub:        hub,
		owner:      owner,
		secret:     secret,
		log:        hub.log,
		conn:       conn,
		viewers:    map[string]*Viewer{},
		maxViewers: hub.MaxViewers,
		desk:       m.Run.ID,
		place:      place,
		info: session.Info{
			ID:        id,
			Name:      strings.TrimSpace(m.Run.Name),
			Kind:      session.KindHosted,
			Status:    session.StatusRunning,
			HostName:  m.Host.Name,
			CreatedAt: time.Now().UTC(),
		},
	}
	hub.mu.Lock()
	if other, held := hub.sessions[id]; held {
		// Another registration of the same desk came first.
		hub.mu.Unlock()
		place.Release()
		if other.owner != owner || !other.attach(reg, conn) {
			return nil, false, ErrBadRegister
		}
		return other, true, nil
	}
	hub.sessions[id] = hs
	hub.mu.Unlock()
	return hs, false, nil
}

// Desk is the desk with id, false for none (or a session's id).
func (hub *Hub) Desk(id string) (*HostedSession, bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hs, ok := hub.sessions[id]
	if !ok || hs.desk == "" {
		return nil, false
	}
	return hs, true
}

// DeskRun is the run of a desk, "" for a session.
func (h *HostedSession) DeskRun() string { return h.desk }

// EndDesk ends a desk its host stopped (a status that says the desk
// ended): forgotten here at once, its viewers closed as at a session's
// end, its place among the sessions given back. Nothing for a session.
func (h *HostedSession) EndDesk() {
	if h.desk == "" {
		return
	}
	h.hub.mu.Lock()
	if h.hub.sessions[h.info.ID] == h {
		delete(h.hub.sessions, h.info.ID)
	}
	h.hub.mu.Unlock()
	h.retireDesk()
}

// retireDesk closes the desk's viewers and gives back its place, once.
func (h *HostedSession) retireDesk() {
	h.Retire()
	h.mu.Lock()
	place := h.place
	h.place = nil
	h.mu.Unlock()
	place.Release()
}

// laneRequest says whether t is a request of a decision link's lane
// (proto.DecodeLane's): a view-role viewer's is dropped at the relay.
func laneRequest(t string) bool {
	switch t {
	case proto.CtlDecisionsList, proto.CtlDecisionRead, proto.CtlDecisionTrack, proto.CtlDecisionRecord, proto.CtlDecisionEvidence:
		return true
	}
	return false
}

// DeskCount is how many desks the hub holds.
func (hub *Hub) DeskCount() int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	n := 0
	for _, hs := range hub.sessions {
		if hs.desk != "" {
			n++
		}
	}
	return n
}
