package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"

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
// credentials the server holds) before it is sent, its who and why as
// shownText shows them: a read that is late, failing, busy, panicking or
// invalid is 503 signin_unavailable, in the server's own words. A begin
// or a sign-out is what the provider's method itself returned: taken on
// nil, refused (503 signin_unavailable, as one not asked is) on an error
// while its call's context was live, and otherwise not known: 503
// signin_status_unknown, its status not read.
// The status is sent to the caller and to nothing
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
// in time with one Valid takes, as given and as shown (signInShown), and
// that holds none of the credentials providerCredentials lists as it is
// asked (the detail's filter, holdsCredential): each field as it is sent,
// the URL also percent-decoded once.
func (g guardIdentity) signInStatus(ctx context.Context, si ext.SignIn) (ext.SignInStatus, bool) {
	own := g.s.providerCredentials()
	st, _, ok := extCallTold(ctx, g.s, kindIdentitySignIn, g.name, ext.IdentityWait, ext.SignInStatus{}, func(st ext.SignInStatus) error {
		if err := st.Valid(); err != nil {
			return err
		}
		shown := signInShown(st)
		if err := shown.Valid(); err != nil {
			return err
		}
		decoded, err := url.PathUnescape(shown.URL)
		if err != nil || holdsCredential(own, shown.Who, shown.Code, shown.URL, decoded, shown.Why) {
			return errors.New("sign-in status: it holds a token")
		}
		return nil
	}, func(context.Context) (ext.SignInStatus, error) { return si.SignInStatus(), nil })
	return st, ok
}

// signInShown is st with its who and why as shownText shows them.
func signInShown(st ext.SignInStatus) ext.SignInStatus {
	st.Who, st.Why = shownText(st.Who), shownText(st.Why)
	return st
}

// signInOutcome is what became of a begin or a sign-out asked through the
// guard.
type signInOutcome int32

const (
	signInNotKnown signInOutcome = iota // anything but the three below: the method had not returned when its outcome was read (still running, its goroutine ended without returning, a panic), or returned an error once its context had ended
	signInNotAsked                      // no slot was free: the provider was not called
	signInRefused                       // the method returned an error while its call's context was live
	signInTaken                         // the method had returned nil when its outcome was read, its context ended or not
)

// signInAct is BeginSignIn or SignOut through the guard, and its outcome:
// what the method itself returned, noted by the call's own goroutine as it
// returns (looking at its context then) and read once the guard has
// answered. Whether the guard had its answer in time is not read.
func (g guardIdentity) signInAct(ctx context.Context, act func(context.Context) error) signInOutcome {
	var returned atomic.Int32 // signInNotKnown until the method returns nil, or an error in time
	_, sent, _ := extCallTold(ctx, g.s, kindIdentitySignIn, g.name, ext.IdentityWait, struct{}{}, nil,
		func(ctx context.Context) (struct{}, error) {
			err := act(ctx)
			switch {
			case err == nil:
				returned.Store(int32(signInTaken))
			case ctx.Err() == nil:
				returned.Store(int32(signInRefused))
			}
			signInActStep("returned")
			return struct{}{}, err
		})
	if !sent {
		return signInNotAsked
	}
	signInActStep("read")
	return signInOutcome(returned.Load())
}

// signInActSeam, when a test sets it, is told each step of an act as it
// is taken, and may hold it there: "returned" on the call's goroutine once
// the method has returned and its outcome is noted, "read" on the
// request's as that outcome is read. nil outside tests.
var signInActSeam atomic.Pointer[func(step string)]

func signInActStep(step string) {
	if f := signInActSeam.Load(); f != nil {
		(*f)(step)
	}
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
	s.writeSignInStatus(w, r, g, si, signInUnavailable)
}

// handleSignInBegin answers POST /api/signin: the provider asked to begin,
// then its status.
func (s *Server) handleSignInBegin(w http.ResponseWriter, r *http.Request) {
	s.signInAct(w, r, ext.SignIn.BeginSignIn, "the sign-in may have begun; its status is not known yet")
}

// handleSignOut answers DELETE /api/signin: the provider asked to sign
// out, then its status.
func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	s.signInAct(w, r, ext.SignIn.SignOut, "the sign-out may have been done; its status is not known yet")
}

// signInAct is POST's and DELETE's: a body of none or {} (at most
// maxSignInBody, no member; null is not {}), a provider that offers a
// sign-in, act through the guard, and the status after it. An act not
// asked or refused is 503 signin_unavailable; one whose outcome is not
// known, or one taken whose status is then not answered, is 503
// signin_status_unknown in unknown, the act's own words.
func (s *Server) signInAct(w http.ResponseWriter, r *http.Request, act func(ext.SignIn, context.Context) error, unknown string) {
	var body *struct{} // nil after null, a member after {}
	err := decodeJSONLimit(w, r, &body, maxSignInBody)
	if errors.Is(err, io.EOF) {
		err = nil // no body
	} else if err == nil && body == nil {
		err = errors.New("null")
	}
	if err != nil {
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
	notKnown := func(w http.ResponseWriter) {
		writeError(w, http.StatusServiceUnavailable, "signin_status_unknown", unknown)
	}
	switch g.signInAct(r.Context(), func(ctx context.Context) error { return act(si, ctx) }) {
	case signInNotAsked, signInRefused:
		signInUnavailable(w)
	case signInNotKnown:
		notKnown(w)
	case signInTaken:
		s.writeSignInStatus(w, r, g, si, notKnown)
	}
}

// writeSignInStatus is the one place a sign-in's status is answered: its
// who and why as shownText shows them, its expiry in UTC, the reply within
// maxSignInReply. A status the guard has none of, one Valid does not take
// as it is about to be sent, or a reply that does not encode within its
// bound, is answered by unread, whatever the guard said of the call.
func (s *Server) writeSignInStatus(w http.ResponseWriter, r *http.Request, g guardIdentity, si ext.SignIn, unread func(http.ResponseWriter)) {
	st, ok := g.signInStatus(r.Context(), si)
	if !ok {
		unread(w)
		return
	}
	st = signInShown(st)
	if !st.Expires.IsZero() {
		st.Expires = st.Expires.UTC()
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if st.Valid() != nil || enc.Encode(signInReply{Supported: true, SignInStatus: st}) != nil || buf.Len() > maxSignInReply {
		unread(w)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// signInUnavailable answers a read with no status the server takes, or an
// act the provider was not asked or refused, in the server's own words.
func signInUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "signin_unavailable", "the identity provider's sign-in is not available now: try again")
}
