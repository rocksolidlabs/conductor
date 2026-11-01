package api

import (
	"context"
	"strings"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The four ICE sites (Program item C8): what each asks iceServersFor for,
// as ext.ICERequest's Purpose. The first three may reach the media
// provider; the fourth never does.
const (
	// icePurposeViewer is a hosted session's viewer, in its welcome.
	icePurposeViewer = "viewer"
	// icePurposeHost is a host's registration, in registered.
	icePurposeHost = "host"
	// icePurposePaste is a paste invite's answer, the server's side of it.
	icePurposePaste = "paste"
	// icePurposeOffer is GET /api/ice: a viewer gathering a paste invite's
	// offer before it has any session. Anyone may ask, so it is answered
	// from the configuration alone.
	icePurposeOffer = "offer"
)

// iceServersFor answers the ICE servers the connection r names is handed.
// Every configured server comes first, as main sends it, never counted and
// never cut (main bounds neither the config's iceServers nor
// CONDUCTOR_ICE_SERVERS); then the media provider's, through the guard
// (ext.ICEWait; each server Valid; at most ext.MaxICEServers of them), so
// a late, failed, panicking or invalid provider adds none, and the free
// default adds none at all.
//
// For icePurposeOffer the provider is never asked, and the answer is the
// configured servers' stun: and stuns: URLs alone, one server for each
// configured one that has any, with no user name and no credential: what
// GET /api/ice has always answered, so a request nobody authenticated
// costs a provider nothing and never carries a turn: URL or a secret,
// the configured ones' or a provider's.
func (s *Server) iceServersFor(ctx context.Context, r ext.ICERequest) []proto.ICEServer {
	if r.Purpose == icePurposeOffer {
		return s.stunServers()
	}
	out := make([]proto.ICEServer, 0, len(s.cfg.ICEServers))
	for _, srv := range s.cfg.ICEServers {
		out = append(out, proto.ICEServer{URLs: srv.URLs, Username: srv.Username, Credential: srv.Credential})
	}
	added, _ := s.exts.media.ICEServers(ctx, r)
	for _, srv := range added {
		out = append(out, proto.ICEServer{URLs: srv.URLs, Username: srv.Username, Credential: srv.Credential})
	}
	return out
}

// stunServers is the configured servers' stun: and stuns: URLs, each
// server without its user name or credential, the servers with none left
// out.
func (s *Server) stunServers() []proto.ICEServer {
	var out []proto.ICEServer
	for _, srv := range s.cfg.ICEServers {
		var urls []string
		for _, u := range srv.URLs {
			if strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:") {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			out = append(out, proto.ICEServer{URLs: urls})
		}
	}
	return out
}

// fitWelcome is w with as many of its ICE servers past the first
// configured ones (the provider's) as keep its frame within
// proto.MaxControl, the last ones dropped first: a viewer's welcome is one
// CONTROL frame (docs/protocol.md), and a provider's eight servers at
// ext's bounds would be more than one. The configured servers are never
// dropped, as main never drops them.
func fitWelcome(w proto.Welcome, configured int) proto.Welcome {
	for len(w.ICEServers) > configured && len(proto.MustControl(w))-1 > proto.MaxControl {
		w.ICEServers = w.ICEServers[:len(w.ICEServers)-1]
	}
	return w
}
