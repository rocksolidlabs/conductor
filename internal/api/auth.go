package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// principal is the authenticated caller of a request.
type principal struct {
	admin bool
	// yard is the workbench token of a switchyard: it lists the sessions
	// published there and has no role on any of them (role).
	yard bool
	host bool
	// subject and name are an identity provider's principal's (identity.go):
	// empty for every token of the server's own chain.
	subject string
	name    string
	link    *share.Link
	// runOf answers the run a session is a member of, for a run link
	// (Server.runOf).
	runOf func(sessionID string) (runID string, ok bool)
}

// role returns the terminal role the principal has on sessionID, or "" when
// it has no access. A session's link grants its role on that session; a
// run's link on the session of every member of the run, whenever it joined.
// The workbench token is control on every session of its own server, and
// none on a switchyard's: every session there is published from a machine
// of its own, and only the links that machine's owner minted grant a role.
func (p principal) role(sessionID string) session.Role {
	if p.admin {
		if p.yard {
			return ""
		}
		return session.RoleControl
	}
	if p.link == nil || sessionID == "" {
		return ""
	}
	if p.link.RunID == "" {
		if p.link.SessionID == sessionID {
			return p.link.Role
		}
		return ""
	}
	// A host's run link on a switchyard names its members' sessions itself.
	if p.link.Group != nil {
		if p.link.Group.Names(sessionID) {
			return p.link.Role
		}
		return ""
	}
	if p.runOf != nil {
		if runID, ok := p.runOf(sessionID); ok && runID == p.link.RunID {
			return p.link.Role
		}
	}
	return ""
}

func (p principal) linkID() string {
	if p.link != nil {
		return p.link.ID
	}
	return ""
}

// attachCheck is the check a viewer's attach to sessionID makes again, under
// the session's lock, in the critical section that registers the viewer
// (session.AttachOptions.Authorize, signal.ViewerOptions.Authorize): the
// principal's link as it stands then, and the role it gives on the session
// then (its run's members, a switchyard group's sessions). A link revoked,
// expired or forgotten since the token was presented refuses the attach.
// Nil for a principal without a link: the workbench token is not revoked.
// It reads the share store and the run index only, neither of which calls a
// session, so it is safe under a session's lock.
func (s *Server) attachCheck(p principal, sessionID string) func() (session.Grant, error) {
	if p.link == nil {
		return nil
	}
	linkID := p.link.ID
	return func() (session.Grant, error) {
		l, err := s.links.Live(linkID)
		if err != nil {
			if errors.Is(err, share.ErrExpired) {
				return session.Grant{}, session.ErrExpired
			}
			return session.Grant{}, session.ErrRevoked
		}
		role := principal{link: l, runOf: s.runOf}.role(sessionID)
		if role == "" {
			return session.Grant{}, session.ErrRevoked
		}
		g := session.Grant{Role: role}
		if l.ExpiresAt != nil {
			g.Until = *l.ExpiresAt
		}
		return g, nil
	}
}

// presentedToken extracts the credential from the Authorization header or,
// for WebSocket and join routes that browsers cannot add headers to, from the
// token query parameter, read as every route reads its query (parseQuery): a
// ';' is part of it, and a query that cannot be read presents no token
// (handleHostWS, where no token means an open host, refuses such a query
// before it asks).
func presentedToken(r *http.Request, allowQuery bool) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(tok)
		}
		return ""
	}
	if allowQuery {
		if q, err := parseQuery(r.URL.RawQuery); err == nil {
			return q.Get("token")
		}
	}
	return ""
}

// authenticate resolves a presented token to a principal: the workbench
// token, each host token, the share links, and only then, for a token none
// of them knows, the identity provider (providerPrincipal). Unknown tokens
// yield an empty principal; callers decide the response.
func (s *Server) authenticate(r *http.Request, allowQuery bool) principal {
	return s.tokenPrincipal(r, presentedToken(r, allowQuery))
}

// tokenPrincipal is authenticate's principal for tok, presented with r.
func (s *Server) tokenPrincipal(r *http.Request, tok string) principal {
	if tok == "" {
		return principal{}
	}
	if share.Equal(tok, s.cfg.WorkbenchToken) {
		return principal{admin: true, yard: s.cfg.Switchyard.Enabled, host: true}
	}
	for _, ht := range s.cfg.HostTokens {
		if share.Equal(tok, ht) {
			return principal{host: true}
		}
	}
	link, err := s.links.Resolve(tok)
	if err == nil {
		return principal{link: link, runOf: s.runOf}
	}
	if !errors.Is(err, share.ErrUnknownToken) {
		// An expired or revoked link is a link: refused as one, never asked
		// of the identity provider.
		s.log.Debug("share token rejected", "reason", err)
		return principal{}
	}
	return s.providerPrincipal(r, tok)
}

// requireAdmin wraps handlers that need the workbench token (the admin
// principal), or an identity provider's principal with the admin grant; the
// handler finds the principal in the request's context (actorOf).
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.authenticate(r, false)
		if !p.admin {
			if !s.limiter.allow(clientKey(r)) {
				writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="conductor"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "workbench token required")
			return
		}
		next(w, withPrincipal(r, p))
	}
}
