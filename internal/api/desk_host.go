package api

import (
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A run's desk at a switchyard (docs/plans/decision-inbox.md, PR-6): the
// host connection that carries a run's decisions with no member alive
// (signal's desk.go). Its first message is register_desk; after it, it
// sends what a desk needs alone: its viewers' signalling (answer, ice,
// viewer_error, viewer_closed), its run's links (link_run,
// link_run_update, link_revoke) and its end (status). A session's
// messages (attention, activity, chat, resize, a session's link) are a
// protocol error from it.

// deskMessage says whether data is a message a desk may send.
func deskMessage(data []byte) bool {
	t, err := proto.ParseHeader(data)
	if err != nil {
		return false
	}
	switch t {
	case proto.HostAnswer, proto.HostICE, proto.HostViewerError, proto.HostViewerClose,
		proto.HostRunLink, proto.HostRunLinkUpdate, proto.HostLinkRevoke, proto.HostStatus:
		return true
	}
	return false
}

// disconnectHosted closes the viewers attached through linkID to the
// session or the desk id names here, if any.
func (s *Server) disconnectHosted(id, linkID string) {
	if d, ok := s.registry.Get(id); ok {
		d.DisconnectLink(linkID)
		return
	}
	if d, ok := s.hosts.Desk(id); ok {
		d.DisconnectLink(linkID)
	}
}
