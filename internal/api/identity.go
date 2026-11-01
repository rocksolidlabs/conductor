package api

import (
	"context"
	"net/http"

	"github.com/rocksolidlabs/conductor/ext"
)

// The identity provider at authentication (Program item C2, the plan's
// "C2, identity at authenticate and hostTokenOK"). It is asked last, for a
// bearer token the server's own chain does not know: the workbench token,
// each host token and the share links come first, and a link that is
// expired or revoked is refused as a link, so a token of this server's own
// never reaches a provider and the free path never waits on one. Its
// answer goes through the guard (guardIdentity, extensions.go), which
// bounds it by ext.IdentityWait and answers the free default's (no
// principal) for a late, failing, panicking or invalid answer and for one
// with no grant; the guard's log names the failure's kind and count, never
// the provider's words, which may quote the token. A provider's principal
// is what its grants make it here (grantPrincipal), and the receipts of
// what it does are kept under its subject (actorOf), never "admin".

// providerPrincipal is the identity provider's principal for tok, a token
// the chain does not know (tokenPrincipal), presented with r: the empty
// principal unless the provider recognises tok with a grant.
func (s *Server) providerPrincipal(r *http.Request, tok string) principal {
	pr, ok, err := s.exts.identity.Authenticate(r.Context(), tok)
	if err != nil || !ok {
		return principal{}
	}
	return s.grantPrincipal(pr)
}

// grantPrincipal is what pr's grants make it here: ext.GrantAdmin what the
// workbench token makes a caller (admin, the switchyard's yard on a
// switchyard, and a host), ext.GrantHost a host token's. pr has passed
// the guard, so it is valid and holds at least one grant.
func (s *Server) grantPrincipal(pr ext.Principal) principal {
	p := principal{subject: pr.Subject, name: pr.Name}
	for _, g := range pr.Grants {
		switch g {
		case ext.GrantAdmin:
			p.admin, p.yard, p.host = true, s.cfg.Switchyard.Enabled, true
		case ext.GrantHost:
			p.host = true
		}
	}
	return p
}

// hostAllowed reports whether tok, presented with r, authorizes a host
// registration: the workbench token, a host token, or an identity
// provider's principal with either grant. A share link is no host's, and a
// link's token never reaches the provider (tokenPrincipal).
func (s *Server) hostAllowed(r *http.Request, tok string) bool {
	return tok != "" && s.tokenPrincipal(r, tok).host
}

// principalKey is the request context's key of requireAdmin's principal.
type principalKey struct{}

// withPrincipal is r with p, its authenticated principal, in its context.
func withPrincipal(r *http.Request, p principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}

// actorOf is the actor the receipts of r's principal are kept under: the
// ledger's commands and Dispatch's operations, and the reads of their
// receipts, which find only that actor's. It is the principal requireAdmin
// put in r's context, and adminActor for a request without one.
func actorOf(r *http.Request) string {
	if p, ok := r.Context().Value(principalKey{}).(principal); ok {
		return p.actor()
	}
	return adminActor
}

// actor is the actor of p's receipts: adminActor ("admin") for the
// workbench token, and ext.SubjectActor + the subject ("user:<subject>")
// for an identity provider's principal. A subject is at most
// ext.MaxSubject (48) bytes, so the actor is at most 53, inside Dispatch's
// MaxActor (60), and its prefix keeps it apart from the actors this
// repository keeps for itself ("admin", Dispatch's "merge" and
// "agent:<member>"): a provider's subject sees only its own receipts.
func (p principal) actor() string {
	if p.subject == "" {
		return adminActor
	}
	return ext.SubjectActor + p.subject
}
