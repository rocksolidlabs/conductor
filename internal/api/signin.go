package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/rocksolidlabs/conductor/ext"
)

// The identity provider's sign-in, started and ended from Settings: GET
// /api/signin reads it, POST begins it, DELETE signs out, the admin's
// alone. Only a provider that offers one (ext.SignIn) and whose Start has
// succeeded is asked; the free default and every other provider answer
// {"supported": false} to the read and 404 not_supported to the rest.
// Every call is the guard's (extCallTold) in slots of its own
// (kindIdentitySignIn), within ext.IdentityWait, and every status is
// checked (ext.SignInStatus.Valid, and the detail's filter of the
// credentials the server holds) before it is sent: a late, failing,
// busy, panicking or invalid answer is 503 signin_unavailable, in the
// server's own words. The status is sent to the caller and to nothing
// else: no log line, event or error quotes it, and the guard's log of a
// failure names its kind and count alone (extFailed: the identity
// provider's words are never quoted).

// kindIdentitySignIn is the guard's kind of the sign-in's calls: slots of
// their own, so a page reading the sign-in never takes the slots a
// request's authentication needs.
const kindIdentitySignIn = ext.KindIdentity + "-signin"

// maxSignInBody bounds the body of POST and DELETE, which take none or {}.
const maxSignInBody = 1 << 10

// maxSignInReply bounds the reply: a valid status is at most about 600
// bytes encoded (its longest fields in one state, every byte escaped at
// most twice: no HTML escaping), inside it (TestSignInLimits).
const maxSignInReply = 1 << 10

// signInReply is the reply of the three routes with a provider that
// offers a sign-in.
type signInReply struct {
	Supported bool `json:"supported"`
	ext.SignInStatus
}

// signIn is the identity provider's sign-in and the guard it is asked
// through, or false: no provider offers one, or its Start has not yet
// succeeded (the free default's answer meanwhile).
func (s *Server) signIn() (guardIdentity, ext.SignIn, bool) {
	g, ok := s.exts.identity.(guardIdentity)
	if !ok || !live(g.ready) {
		return guardIdentity{}, nil, false
	}
	si, ok := g.p.(ext.SignIn)
	return g, si, ok
}

// signInStatus is si's status through the guard, and whether it answered
// in time with one Valid takes and that holds, as written, none of the
// credentials providerCredentials lists as it is asked (the detail's
// filter, guardIdentity.detail).
func (g guardIdentity) signInStatus(ctx context.Context, si ext.SignIn) (ext.SignInStatus, bool) {
	own := g.s.providerCredentials()
	st, _, ok := extCallTold(ctx, g.s, kindIdentitySignIn, g.name, ext.IdentityWait, ext.SignInStatus{}, func(st ext.SignInStatus) error {
		if err := st.Valid(); err != nil {
			return err
		}
		for _, tok := range own {
			for _, v := range []string{st.Who, st.Code, st.URL, st.Why} {
				if tok != "" && strings.Contains(v, tok) {
					return errors.New("sign-in status: it holds a token")
				}
			}
		}
		return nil
	}, func(context.Context) (ext.SignInStatus, error) { return si.SignInStatus(), nil })
	return st, ok
}

// signInAct is BeginSignIn or SignOut through the guard, and whether it
// returned in time with no error.
func (g guardIdentity) signInAct(ctx context.Context, act func(context.Context) error) bool {
	_, _, ok := extCallTold(ctx, g.s, kindIdentitySignIn, g.name, ext.IdentityWait, struct{}{}, nil,
		func(ctx context.Context) (struct{}, error) { return struct{}{}, act(ctx) })
	return ok
}

// handleSignIn answers GET /api/signin: the sign-in's status, or
// {"supported": false}.
func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	g, si, ok := s.signIn()
	if !ok {
		writeCapacityJSON(w, http.StatusOK, struct {
			Supported bool `json:"supported"`
		}{}, maxSignInReply)
		return
	}
	s.writeSignInStatus(w, r, g, si)
}

// handleSignInBegin answers POST /api/signin: the provider asked to begin,
// then its status.
func (s *Server) handleSignInBegin(w http.ResponseWriter, r *http.Request) {
	s.signInAct(w, r, ext.SignIn.BeginSignIn)
}

// handleSignOut answers DELETE /api/signin: the provider asked to sign
// out, then its status.
func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	s.signInAct(w, r, ext.SignIn.SignOut)
}

// signInAct is POST's and DELETE's: a body of none or {} (at most
// maxSignInBody, no member), a provider that offers a sign-in, act
// through the guard, and the status after it.
func (s *Server) signInAct(w http.ResponseWriter, r *http.Request, act func(ext.SignIn, context.Context) error) {
	var body struct{}
	if err := decodeOptionalJSON(w, r, &body, maxSignInBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", "the body is more than 1 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "the body is none or {}")
		return
	}
	g, si, ok := s.signIn()
	if !ok {
		writeError(w, http.StatusNotFound, "not_supported", "the identity provider offers no sign-in")
		return
	}
	if !g.signInAct(r.Context(), func(ctx context.Context) error { return act(si, ctx) }) {
		signInUnavailable(w)
		return
	}
	s.writeSignInStatus(w, r, g, si)
}

// writeSignInStatus answers si's status, or 503 when the guard has none.
func (s *Server) writeSignInStatus(w http.ResponseWriter, r *http.Request, g guardIdentity, si ext.SignIn) {
	st, ok := g.signInStatus(r.Context(), si)
	if !ok {
		signInUnavailable(w)
		return
	}
	writeCapacityJSON(w, http.StatusOK, signInReply{Supported: true, SignInStatus: st}, maxSignInReply)
}

// signInUnavailable answers a call the provider did not answer in time
// with what the guard takes, in the server's own words.
func signInUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "signin_unavailable", "the identity provider did not answer the sign-in: try again")
}
