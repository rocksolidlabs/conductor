package signal

import "github.com/rocksolidlabs/conductor/internal/proto"

// Report is what the session's host reported of it as it registered
// (proto.HostReport, checked at its bounds by Register): where it runs, its
// agent's version, and its repository and start commit as the host read
// them before its process started. Fixed for the session's life: a host
// that registers again under the same instance is the same process, which
// read them once.
func (h *HostedSession) Report() proto.HostReport { return h.report }

// Added is closed once the hub's OnAdded has returned for the session the
// register made, which the registry holds before it (closed already for a
// session no register made): what a report on the session waits for, so
// that what OnAdded puts in place is there for it.
func (h *HostedSession) Added() <-chan struct{} {
	if h.added == nil {
		return closedAdded
	}
	return h.added
}

var closedAdded = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// HostSessionInfo hands a session_info its host sent (proto.ParseSessionInfo
// checked it) to the hub's OnSessionInfo, with the session it is of
// whatever session the host named.
func (h *HostedSession) HostSessionInfo(m proto.HostSessionInfoMsg) {
	if h.hub != nil && h.hub.OnSessionInfo != nil {
		h.hub.OnSessionInfo(h, m)
	}
}
