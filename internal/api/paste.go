package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/paste"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// PasteAnswerer answers a viewer's paste offer for a server session
// (hostagent.AnswerPaste, which serve wires in: api stays free of
// hostagent). It returns the peer kept for the viewer and the answer SDP.
type PasteAnswerer func(ctx context.Context, local *session.Local, offerSDP string, role session.Role, label string, ice []proto.ICEServer) (PastePeer, string, error)

// PastePeer is a viewer's side kept on this server until the session ends.
type PastePeer interface {
	Close()
	Connected() bool
	Since() time.Time
}

// SetPasteAnswerer makes the paste route answer; without one it says so.
func (s *Server) SetPasteAnswerer(f PasteAnswerer) {
	s.pastes.mu.Lock()
	defer s.pastes.mu.Unlock()
	s.pastes.answer = f
}

// A paste invite (hostagent.AnswerPaste): a viewer's offer, pasted in by
// the person sharing, answered with a blob the viewer pastes back; no server
// between the two once the data channel is open. This server answers for
// its own sessions alone; the peers it keeps go with the session.

// pasteRequest is the body of POST /api/sessions/{id}/paste.
type pasteRequest struct {
	Offer string       `json:"offer"`
	Role  session.Role `json:"role"`
	// Label names the viewer on the session until its hello says more.
	Label string `json:"label"`
}

// maxPastesPerSession bounds the paste peers kept for one session.
const maxPastesPerSession = 16

// pasteStore keeps the paste peers of each session.
type pasteStore struct {
	mu     sync.Mutex
	answer PasteAnswerer
	peers  map[string][]PastePeer
}

func (ps *pasteStore) add(id string, p PastePeer) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.peers == nil {
		ps.peers = map[string][]PastePeer{}
	}
	// Peers whose viewer never connected, or left, make room first.
	live := ps.peers[id][:0]
	for _, q := range ps.peers[id] {
		if q.Connected() || time.Since(q.Since()) < 2*time.Minute {
			live = append(live, q)
		} else {
			q.Close()
		}
	}
	if len(live) >= maxPastesPerSession {
		ps.peers[id] = live
		return false
	}
	ps.peers[id] = append(live, p)
	return true
}

// closeAll ends every paste peer of a session (it left the registry).
func (ps *pasteStore) closeAll(id string) {
	ps.mu.Lock()
	peers := ps.peers[id]
	delete(ps.peers, id)
	ps.mu.Unlock()
	for _, p := range peers {
		p.Close()
	}
}

// handlePaste answers a viewer's pasted offer for a server session: 201
// {answer}. The offer is a cpi1 blob; the answer is one too.
func (s *Server) handlePaste(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, ok := s.registry.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	local, ok := d.(*session.Local)
	if !ok {
		writeError(w, http.StatusBadRequest, "hosted_session", "a hosted session is shared from its own machine")
		return
	}
	if local.Info().Status.Ended() {
		writeError(w, http.StatusConflict, "session_ended", "the session has ended")
		return
	}
	var req pasteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !req.Role.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_role", "role must be view or control")
		return
	}
	s.pastes.mu.Lock()
	answerer := s.pastes.answer
	s.pastes.mu.Unlock()
	if answerer == nil {
		writeError(w, http.StatusServiceUnavailable, "paste_unavailable", "this server answers no paste invites")
		return
	}
	if len(req.Offer) > paste.MaxBlob {
		writeError(w, http.StatusBadRequest, "invalid_offer", "the invite is too long")
		return
	}
	offer, err := paste.DecodeBlob(req.Offer, "offer")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_offer", strings.TrimPrefix(err.Error(), "paste: "))
		return
	}
	label := strings.TrimSpace(req.Label)
	if len(label) > 40 {
		label = label[:40]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	pp, answer, err := answerer(ctx, local, offer, req.Role, label, s.iceServersFor(ctx, ext.ICERequest{Purpose: icePurposePaste, Session: id, Role: string(req.Role)}))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_offer", strings.TrimPrefix(err.Error(), "paste: "))
		return
	}
	if !s.pastes.add(id, pp) {
		pp.Close()
		writeError(w, http.StatusTooManyRequests, "too_many_pastes", "this session has as many paste viewers as it may")
		return
	}
	blob, err := paste.EncodeBlob("answer", answer)
	if err != nil {
		pp.Close()
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.log.Info("paste invite answered", "session", id, "role", req.Role)
	local.Record(session.ActivityEntry{Type: session.ActivityLink, Message: "paste invite answered (" + string(req.Role) + ")"})
	writeJSON(w, http.StatusCreated, map[string]any{"answer": blob, "role": req.Role})
}

// handleICE answers the STUN servers a viewer may gather its candidates
// with before it has any session (a paste invite's offer): the configured
// ICE servers' stun: and stuns: URLs (iceServersFor's offer), so a TURN
// secret never leaves the server and no provider is asked.
func (s *Server) handleICE(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	urls := []string{}
	for _, srv := range s.iceServersFor(r.Context(), ext.ICERequest{Purpose: icePurposeOffer}) {
		urls = append(urls, srv.URLs...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"stun": urls})
}
