package api

import (
	"net/http"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// handleMe answers GET /api/me for any authenticated principal: who it is
// and what it may do here (proto.MeReply, at most proto.MaxMeReply), the
// decide page's "Signed in as" and whether a pass is a decide pass
// (docs/plans/decide-grant.md). The workbench token is admin and host, a
// host token host, a share link nothing; an identity provider's principal
// its subject, its name and the grants it holds here (admin includes host,
// as the workbench token's does). Anything else is 401 under the rate
// limit. A switchyard answers it too: its lack of a role is whoami's to
// say. A decide principal's is worked in its subject's place
// (decideWorked).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r, false)
	if !ok {
		return
	}
	if !p.admin && !p.host && !p.decide && p.link == nil {
		s.unauthorized(w, r)
		return
	}
	reply := func(w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, proto.MeReply{Subject: p.subject, Name: p.name, Grants: grantsOf(p)})
	}
	if p.decider() {
		s.decideWorked(w, p, reply)
		return
	}
	reply(w)
}

// grantsOf is the grants p holds here, in a set order.
func grantsOf(p principal) []string {
	gs := []string{}
	for _, g := range []struct {
		held  bool
		grant ext.Grant
	}{{p.admin, ext.GrantAdmin}, {p.host, ext.GrantHost}, {p.decide, ext.GrantDecide}} {
		if g.held {
			gs = append(gs, string(g.grant))
		}
	}
	return gs
}
