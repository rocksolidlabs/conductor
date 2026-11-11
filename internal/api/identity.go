package api

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
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
//
// A provider's principal that holds a long-lived connection, a viewer's
// WebSocket or the events stream, is asked about again while it holds it
// (principalWatch): one the provider no longer admits loses the
// connection, as a revoked link's viewer does (the owner's decision of
// 2026-11-01, #560).

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

// hostPrincipal is the principal of tok, presented with r, and whether it
// authorizes a host registration: the workbench token, a host token, or
// an identity provider's principal with either grant, whose subject the
// connection keeps. A share link is no host's, and a link's token never
// reaches the provider (tokenPrincipal).
func (s *Server) hostPrincipal(r *http.Request, tok string) (principal, bool) {
	if tok == "" {
		return principal{}, false
	}
	p := s.tokenPrincipal(r, tok)
	return p, p.host
}

// watchedToken is the credential of a long-lived connection an identity
// provider's principal holds, made with the connection and gone with it.
// turn is the connection's credential turn: a recheck and a renewal of
// the connection take it from their ask to their verdict, so one never
// overlaps the other and a verdict concerns the token it asked about. tok
// is read and changed only under the turn. ended is the connection's
// terminal state, a verdict recorded, set only under the turn and read
// without it (the host connection's reader looks at it before each
// message).
type watchedToken struct {
	turn  chan struct{}
	tok   string
	ended atomic.Bool
}

func newWatchedToken(tok string) *watchedToken {
	return &watchedToken{turn: make(chan struct{}, 1), tok: tok}
}

// take takes the turn, telling the seam first when it must wait for it;
// false when ctx ends first.
func (w *watchedToken) take(ctx context.Context, waiting func()) bool {
	select {
	case w.turn <- struct{}{}:
		return true
	default:
	}
	if waiting != nil {
		waiting()
	}
	select {
	case w.turn <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// give gives the turn back.
func (w *watchedToken) give() { <-w.turn }

// hostCredSeam lets a test hold a host connection's credential steps;
// nil outside tests. waiting is told a recheck or a renewal waits for the
// turn; reading, a host message read, before ended is looked at; closing,
// a verdict recorded and the turn given back, before the close handshake.
type hostCredSeam struct {
	waiting, reading, closing func()
}

func (s *Server) credHook(pick func(*hostCredSeam) func()) func() {
	if s.hostCred == nil {
		return nil
	}
	return pick(s.hostCred)
}

func runHook(f func()) {
	if f != nil {
		f()
	}
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

// identityRecheckEvery is how often a long-lived connection an identity
// provider's principal holds (a viewer's WebSocket, GET /api/events) asks
// the provider again about the token it was opened with
// (principalWatch). Each ask is bounded by ext.IdentityWait, as the
// first was, so a principal the provider revokes loses its connections
// within this and that.
const identityRecheckEvery = 30 * time.Second

// errPrincipalRevoked closes a viewer's connection whose principal the
// identity provider no longer admits: an error{revoked} and 4403, as a
// revoked link's viewer is closed, with words that name no link.
var errPrincipalRevoked = fmt.Errorf("access revoked by the identity provider: %w", session.ErrRevoked)

// principalWatch is the watch of a long-lived connection p holds, with
// its credential wt: run on a goroutine of its own with the connection's
// context, it asks the identity provider again every identityRecheckEvery
// (guardIdentity.recheck, through the guard) about the token current then
// and calls end, once, when the provider answers in time that it no
// longer admits it as p: no principal, another subject, or one without
// the grant the connection needs (admin for a viewer's control role and
// the events stream, host for a host connection). Each ask holds the
// connection's credential turn until its verdict is recorded (wt.ended);
// the turn is given back before end runs, so no close handshake runs
// under it. A provider that fails, panics, is late, is busy or answers a
// principal out of bounds has said nothing, and the connection stays: the
// guard's rule that a failure answers what was there before. It returns
// when ctx ends, once the connection has ended (a renewal's verdict), or
// after end. Nil, and nothing asked, for every principal of the server's
// own chain (the workbench token, a host token, a link: no subject), so a
// build with no provider watches nothing.
func (s *Server) principalWatch(p principal, wt *watchedToken, need ext.Grant) func(ctx context.Context, end func()) {
	if p.subject == "" || wt == nil || wt.tok == "" {
		return nil
	}
	g, ok := s.exts.identity.(guardIdentity)
	if !ok {
		return nil
	}
	return func(ctx context.Context, end func()) {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.extAfterOr(identityRecheckEvery):
			}
			if !wt.take(ctx, s.credHook(func(h *hostCredSeam) func() { return h.waiting })) {
				return
			}
			if wt.ended.Load() {
				wt.give()
				return
			}
			pr, admitted, known := g.recheck(ctx, wt.tok)
			if !known || ctx.Err() != nil || (admitted && s.admitsAs(pr, p.subject, need)) {
				wt.give()
				continue
			}
			wt.ended.Store(true)
			wt.give()
			runHook(s.credHook(func(h *hostCredSeam) func() { return h.closing }))
			// By the provider's name alone: the token is never logged.
			s.log.Debug("identity provider no longer admits a connection's principal: closed", "provider", g.name)
			end()
			return
		}
	}
}

// hostToken takes a host_token, inline in the host connection's read
// loop: it reports whether the renewal took its one success path, the
// same subject admitted with the host grant, the new token then the
// connection's. Every other outcome is the one fallback, false, and the
// caller closes the connection 4403 access revoked: a message past its
// bounds (proto.ParseHostToken), a connection not admitted through a
// provider's principal, a token the server's own chain knows (never
// asked of the provider), a connection already ended, an error, a late,
// busy or panicking answer, and a verdict (no principal, another subject,
// no host grant), recorded under the turn as a recheck's is. The ask is
// made under the connection's credential turn, in the renewals' own
// slots (kindIdentityRenew), within ext.IdentityWait, and the turn is
// given back before it returns, so no close runs under it.
func (s *Server) hostToken(hc *hostConnState, data []byte) bool {
	tok, err := proto.ParseHostToken(data)
	if err != nil || hc.cred == nil {
		return false
	}
	if _, known := s.chainPrincipal(tok); known {
		return false
	}
	g, ok := s.exts.identity.(guardIdentity)
	if !ok || !hc.cred.take(hc.ctx, s.credHook(func(h *hostCredSeam) func() { return h.waiting })) {
		return false
	}
	defer hc.cred.give()
	if hc.cred.ended.Load() {
		return false
	}
	pr, admitted, known := g.renew(hc.ctx, tok)
	if known && admitted && s.admitsAs(pr, hc.subject, ext.GrantHost) {
		hc.cred.tok = tok
		return true
	}
	// A verdict, or a renewal that could not be told: either way the
	// connection ends, and nothing it reads after this is handled.
	hc.cred.ended.Store(true)
	return false
}

// admitsAs says pr is subject's principal with the grant need gives here.
func (s *Server) admitsAs(pr ext.Principal, subject string, need ext.Grant) bool {
	if pr.Subject != subject {
		return false
	}
	gp := s.grantPrincipal(pr)
	if need == ext.GrantHost {
		return gp.host
	}
	return gp.admin
}
