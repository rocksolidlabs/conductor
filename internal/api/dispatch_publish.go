package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// A ticket's publications (docs/plans/tracker-linking.md, PR-5): an
// outcome posted to GitHub as one comment, composed and previewed by the
// person, sent once. Every route here is the admin's alone (no Brakeman
// grant names one, and no session token reaches them) and asks GitHub
// through atGitHub only. A send commits sending (dispatch.BeginSend)
// before its one request to post, and posts only when that commit was its
// own (owned); every later step reads the row first and does only what it
// says is not done. An answer that does not prove whether the comment was
// made leaves the row sending, and a settle looks for the comment by its
// marker (FindComment): found, sent; the whole window listed without it,
// past settleQuiet, unconfirmed, which nothing sends again; a listing not
// whole keeps its words. Nothing else posts or closes on GitHub.

// The bounds of a publication's routes: each deadline the sum of its
// steps' caps (trackerCaps).
const (
	// PublishTimeout bounds a send: the lock; the target's resolve and the
	// account's read; the send begun; the post; its settle; then either a
	// lost answer's listing and its settle, or the close (its resolve and
	// the close, the close begun and settled).
	PublishTimeout = trackerBusyWait + trackerReadsBudget + dispatchWriteTimeout + publishPostBudget + dispatchWriteTimeout + publishPostBudget + 2*dispatchWriteTimeout
	// PrepareTimeout bounds a prepare: the lock; a link's read and the
	// target's resolve; the link's read recorded; the prepare.
	PrepareTimeout = trackerBusyWait + trackerReadsBudget + 2*dispatchWriteTimeout
	// CloseNowTimeout bounds Close now: the lock, the resolve and the
	// close, the close begun and settled.
	CloseNowTimeout = trackerBusyWait + publishPostBudget + 2*dispatchWriteTimeout
	// A Check is TrackerReadTimeout: the lock, the listing (its resolve and
	// at most twice ten pages), the settle.

	// publishPostBudget bounds a phase of a send's writes to GitHub: the
	// post, a listing, or a close with its resolve.
	publishPostBudget = 30 * time.Second
	// settleQuiet is how long after a send began a listing without its
	// comment makes it unconfirmed.
	settleQuiet = 30 * time.Second
	// settleStartTimeout bounds the settle of the publications a server
	// finds being sent when it starts, and maxStartSettles how many.
	settleStartTimeout = 2 * time.Minute
	maxStartSettles    = 256
)

// The bodies of the publications' writes.
type (
	publicationBody struct {
		dispatchOp
		Target     string   `json:"target"`
		Body       string   `json:"body"`
		Parts      []string `json:"parts,omitempty"`
		CloseIssue string   `json:"closeIssue,omitempty"`
	}
	publicationSendBody struct {
		dispatchOp
		PostSha256 string `json:"postSha256"`
	}
	publicationCheckBody struct{}
)

// errTargetChanged answers a write whose item is no longer of the kind, or
// in the repository, it was prepared for: nothing written.
var errTargetChanged = newAPIError(http.StatusConflict, "target_changed", "the item is no longer in the repository it was prepared for, or no longer of its kind: prepare it again")

func (s *Server) publishNow() time.Time {
	if s.external.now != nil {
		return s.external.now()
	}
	return time.Now()
}

// publicationRef is where the publication's item was at prepare: its
// host, owner/name and number, for the credential and the listing.
func publicationRef(p dispatch.Publication) tracker.Ref {
	ref := tracker.Ref{System: tracker.GitHub, Kind: p.TargetKind, SpaceID: tracker.RemoteID(p.TargetSpaceID)}
	if u, err := url.Parse(p.TargetURL); err == nil {
		ref.Host = strings.ToLower(u.Host)
	}
	ref.Space, ref.ID, _ = strings.Cut(p.TargetRef, "#")
	return ref
}

// publicationTarget is the destination fixed at prepare, as the adapter's
// writes take it.
func publicationTarget(p dispatch.Publication) tracker.Target {
	return tracker.Target{NodeID: p.TargetNodeID, Kind: p.TargetKind, SpaceID: tracker.RemoteID(p.TargetSpaceID), Ref: publicationRef(p), URL: p.TargetURL}
}

// resolveSame resolves the publication's node now: errTargetChanged
// unless GitHub has it of the same kind in the same repository.
func resolveSame(ctx context.Context, ad tracker.Adapter, c tracker.Credential, p dispatch.Publication) (tracker.Target, error) {
	t, err := ad.Resolve(ctx, c, p.TargetNodeID)
	if err == nil && (t.Kind != p.TargetKind || string(t.SpaceID) != p.TargetSpaceID) {
		err = errTargetChanged
	}
	return t, err
}

// listing is what a settle's look on GitHub found: the comment by its
// marker and the exact bytes posted, by the account that sent it (any
// author for a publication restored from another instance), since
// find_since. found, sent; the whole window listed without it, unconfirmed
// for a send past settleQuiet (words for one not yet past it); anything
// else, words. A restored draft or failed one is changed by a finding
// alone (the store's rule).
func (s *Server) listing(ctx context.Context, ad tracker.Adapter, c tracker.Credential, p dispatch.Publication) dispatch.Outcome {
	m := tracker.Match{Marker: p.Marker, BodySha256: p.PostSha256}
	if !p.Restored {
		m.AuthorID = tracker.RemoteID(p.SentAsID)
	}
	cm, found, err := ad.FindComment(ctx, c, publicationTarget(p), p.Since(), m)
	switch {
	case errors.Is(err, tracker.ErrTargetChanged):
		return dispatch.Outcome{Words: errTargetChanged.Message}
	case err != nil:
		return dispatch.Outcome{Words: "GitHub's comments could not be listed: " + trackerAnswer(err, publicationRef(p)).Code + "; Check looks again"}
	case found == tracker.FoundYes:
		return dispatch.Outcome{State: dispatch.PublicationSent, Comment: &dispatch.SentComment{ID: string(cm.ID), URL: cm.URL, AuthorID: string(cm.AuthorID), Author: cm.Author}}
	case found == tracker.FoundNo && (p.State != dispatch.PublicationSending || s.publishNow().Sub(p.SendingAt) >= settleQuiet):
		return dispatch.Outcome{State: dispatch.PublicationUnconfirmed}
	case found == tracker.FoundNo:
		return dispatch.Outcome{Words: "GitHub has not shown the comment yet; Check looks again"}
	}
	return dispatch.Outcome{Words: "GitHub's comments could not be listed whole; Check looks again"}
}

// settleListed looks for the publication's comment (listing) and settles
// what it found, each step within its own cap of ctx.
func (s *Server) settleListed(ctx context.Context, ds *dispatch.Store, ad tracker.Adapter, c tracker.Credential, p dispatch.Publication) error {
	caps := s.trackerCaps()
	lctx, lcancel := context.WithTimeout(ctx, caps.post)
	o := s.listing(lctx, ad, c, p)
	lcancel()
	wctx, wcancel := context.WithTimeout(ctx, caps.write)
	defer wcancel()
	_, _, err := ds.SettlePublication(wctx, dispatch.ActorAdmin, p.ID, p.Attempt, o)
	return err
}

// closeIssue closes a sent publication's issue, if GitHub does not show
// it closed already: the target resolved as the one prepared
// (errTargetChanged, nothing written), the close begun and committed, the
// close sent with a credential tagged with its try and the comment, and
// its answer settled. An answer that does not say leaves it closing, for
// Close now.
func (s *Server) closeIssue(ctx context.Context, ds *dispatch.Store, ad tracker.Adapter, c tracker.Credential, p dispatch.Publication) error {
	caps := s.trackerCaps()
	pctx, pcancel := context.WithTimeout(ctx, caps.post)
	defer pcancel()
	t, err := resolveSame(pctx, ad, c, p)
	if err != nil {
		return err
	}
	wctx, wcancel := context.WithTimeout(ctx, caps.write)
	_, _, err = ds.BeginClose(wctx, dispatch.ActorAdmin, p.ID)
	if err == nil {
		p, err = ds.Publication(wctx, p.ID)
	}
	wcancel()
	if err != nil {
		return err
	}
	o := dispatch.CloseOutcome{State: dispatch.CloseClosed, Words: "closed on GitHub before Conductor's close"}
	if t.State == tracker.StateOpen {
		err := ad.Close(pctx, c.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: p.ID, Try: p.CloseTries, Comment: p.RemoteID}), t, p.CloseIssue)
		switch {
		case err == nil:
			o.Words = ""
		case tracker.NothingWritten(err):
			o = dispatch.CloseOutcome{State: dispatch.CloseRefused, Words: "GitHub refused the close: " + trackerAnswer(err, t.Ref).Code}
		default:
			return nil
		}
	}
	wctx, wcancel = context.WithTimeout(ctx, caps.write)
	defer wcancel()
	_, _, err = ds.SettleClose(wctx, dispatch.ActorAdmin, p.ID, o)
	return err
}

// publicationReply answers a publication's operation: rc (when one ran)
// and the publication whole, its preview and digest beside it.
func (s *Server) publicationReply(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, id string, status int, rc *dispatch.Receipt) {
	p, err := ds.Publication(ctx, id)
	if err != nil {
		s.dispatchError(w, "publication", err)
		return
	}
	reply := map[string]any{"publication": p, "preview": p.Posted(), "postSha256": p.PostSha256}
	if rc != nil {
		reply["receipt"] = *rc
	}
	writeDispatch(w, status, reply)
}

// publicationAt starts a route on the publication its path names: the
// store for a write, the id, the body decoded (an operation's when op is
// given), the route's context and the publication's lock; false once it
// answered. The lock is let go by the func.
func (s *Server) publicationAt(w http.ResponseWriter, r *http.Request, v any, op *dispatchOp, d time.Duration) (ds *dispatch.Store, id string, ctx context.Context, done func(), ok bool) {
	if !decodeExternal(w, r, v, op) {
		return nil, "", nil, nil, false
	}
	if op != nil {
		if err := op.op(r).Check(); err != nil {
			s.dispatchError(w, "publication", err)
			return nil, "", nil, nil, false
		}
	}
	if ds, ok = s.dispatchWriter(w); !ok {
		return nil, "", nil, nil, false
	}
	if id, ok = pathID(w, r); !ok {
		return nil, "", nil, nil, false
	}
	ctx, cancel := s.trackerContext(r, d)
	release, ok := s.lockFor(ctx, w, "publication:"+id)
	if !ok {
		cancel()
		return nil, "", nil, nil, false
	}
	return ds, id, ctx, func() { release(); cancel() }, true
}

// handlePublicationSend sends a draft or a failed publication (body
// publicationSendBody, its digest the preview's): the operation replayed
// first, a resend answered from its receipt (the row settled first when it
// is being sent) and posting nothing; then, under the publication's lock,
// the target resolved as the one prepared and the token's account read,
// the send begun and committed, and only the call that owns it posts the
// preview's bytes to the node, with the credential tagged for the
// comment. The answer settles it: the comment, sent (and the close, when
// asked); a refusal proving nothing was posted, failed; anything else, a
// listing for the comment. 200 {receipt, publication}.
func (s *Server) handlePublicationSend(w http.ResponseWriter, r *http.Request) {
	var body publicationSendBody
	caps := s.trackerCaps()
	ds, id, ctx, done, ok := s.publicationAt(w, r, &body, &body.dispatchOp,
		caps.busy+caps.reads+caps.write+caps.post+caps.write+caps.post+2*caps.write)
	if !ok {
		return
	}
	defer done()
	op := body.op(r)
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentPublicationSend, dispatch.SendArgs{ID: id, PostSha256: body.PostSha256})
	if err != nil || found && rc.Status != dispatch.StatusDone {
		s.writeOp(w, "publication", rc, err, false, "publication", s.publicationAfter(ctx, ds, id))
		return
	}
	p, err := ds.Publication(ctx, id)
	if err != nil {
		s.dispatchError(w, "publication", err)
		return
	}
	ref := publicationRef(p)
	if found {
		// A resend: nothing is posted; a send under way is settled first,
		// as far as GitHub answers.
		if p.State == dispatch.PublicationSending {
			_ = s.atGitHub(ctx, ref, func(ad tracker.Adapter, c tracker.Credential) error { return s.settleListed(ctx, ds, ad, c, p) })
		}
		s.publicationReply(ctx, w, ds, id, http.StatusOK, &rc)
		return
	}
	sendable := body.PostSha256 == p.PostSha256 && !p.Restored && (p.State == dispatch.PublicationDraft || p.State == dispatch.PublicationFailed) &&
		p.Tries < dispatch.MaxPublicationTries && (op.ExpectedRev == 0 || op.ExpectedRev == p.Rev)
	if !sendable {
		// The store's refusal, kept under the operation's id; GitHub is not
		// asked.
		rc, _, err := ds.BeginSend(ctx, op, id, body.PostSha256, dispatch.Sender{})
		s.writeOp(w, "publication", rc, err, false, "publication", s.publicationAfter(ctx, ds, id))
		return
	}
	err = s.atGitHub(ctx, ref, func(ad tracker.Adapter, c tracker.Credential) error {
		rctx, rcancel := context.WithTimeout(ctx, caps.reads)
		t, err := resolveSame(rctx, ad, c, p)
		var who tracker.Account
		if err == nil {
			who, err = ad.Whoami(rctx, c)
		}
		rcancel()
		if err != nil {
			return err
		}
		wctx, wcancel := context.WithTimeout(ctx, caps.write)
		var owned bool
		rc, owned, err = ds.BeginSend(wctx, op, id, body.PostSha256, dispatch.Sender{ID: string(who.ID), Login: who.Login})
		wcancel()
		if err != nil || !owned {
			// Refused, or another's: this call posts nothing.
			return err
		}
		if p, err = ds.Publication(ctx, id); err != nil {
			return err
		}
		pctx, pcancel := context.WithTimeout(ctx, caps.post)
		cm, err := ad.Comment(pctx, c.ForWrite(tracker.Write{Kind: tracker.WriteComment, Publication: id, Try: p.Attempt, PostSha256: p.PostSha256}), t, p.Posted())
		pcancel()
		var o dispatch.Outcome
		switch {
		case err == nil:
			o = dispatch.Outcome{State: dispatch.PublicationSent, Comment: &dispatch.SentComment{ID: string(cm.ID), URL: cm.URL, AuthorID: string(cm.AuthorID), Author: cm.Author}}
		case tracker.NothingWritten(err):
			o = dispatch.Outcome{State: dispatch.PublicationFailed, Words: "GitHub refused the comment: " + trackerAnswer(err, t.Ref).Code}
		default:
			s.log.Info("a publication's post had no answer that says whether it was made; its comment is looked for", "publication", id, "try", p.Attempt)
			return s.settleListed(ctx, ds, ad, c, p)
		}
		wctx, wcancel = context.WithTimeout(ctx, caps.write)
		_, _, err = ds.SettlePublication(wctx, dispatch.ActorAdmin, id, p.Attempt, o)
		wcancel()
		if err != nil || o.State != dispatch.PublicationSent || p.CloseIssue == "" {
			return err
		}
		if p, err = ds.Publication(ctx, id); err != nil {
			return err
		}
		return s.closeIssue(ctx, ds, ad, c, p)
	})
	if rc.Status == dispatch.StatusFailed || err != nil && rc.OpID == "" {
		s.publicationFail(w, r, ref, rc, err, ds, ctx, id)
		return
	}
	if err != nil {
		// Begun: the store holds what happened, and the answer says it.
		s.log.Info("a publication's send ended early; the publication says where it stands", "publication", id, "err", trackerAnswer(err, ref).Code)
	}
	s.trackerLog(r, ref, http.StatusOK)
	s.publicationReply(ctx, w, ds, id, http.StatusOK, &rc)
}

// publicationFail answers a publication's route that ended before its
// operation ran: a refusal kept under the operation's id as writeOp
// answers it, a store's refusal, the target changed, or GitHub's failure.
func (s *Server) publicationFail(w http.ResponseWriter, r *http.Request, ref tracker.Ref, rc dispatch.Receipt, err error, ds *dispatch.Store, ctx context.Context, id string) {
	var refused *dispatch.Refusal
	switch {
	case rc.Status == dispatch.StatusFailed:
		s.writeOp(w, "publication", rc, nil, false, "publication", s.publicationAfter(ctx, ds, id))
	case errors.As(err, &refused):
		s.refusal(w, err)
	default:
		s.trackerFail(w, r, ref, err)
	}
}

// publicationAfter is what a publication's operation's reply carries: the
// publication as read after it.
func (s *Server) publicationAfter(ctx context.Context, ds *dispatch.Store, id string) func(dispatch.Receipt) (any, error) {
	return func(dispatch.Receipt) (any, error) { return ds.Publication(ctx, id) }
}

// handlePublicationCheck looks for a publication's comment on GitHub now
// and settles what it found (listing): one being sent or not confirmed,
// or a draft or failed one restored from another instance; 409 invalid
// for any other. 200 {publication}.
func (s *Server) handlePublicationCheck(w http.ResponseWriter, r *http.Request) {
	var body publicationCheckBody
	caps := s.trackerCaps()
	ds, id, ctx, done, ok := s.publicationAt(w, r, &body, nil, caps.busy+caps.reads+caps.write)
	if !ok {
		return
	}
	defer done()
	p, err := ds.Publication(ctx, id)
	if err != nil {
		s.dispatchError(w, "publication", err)
		return
	}
	if p.State != dispatch.PublicationSending && p.State != dispatch.PublicationUnconfirmed &&
		!(p.Restored && (p.State == dispatch.PublicationDraft || p.State == dispatch.PublicationFailed)) {
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalInvalid, Message: "a " + p.State + " publication has nothing to look for"}})
		return
	}
	ref := publicationRef(p)
	err = s.atGitHub(ctx, ref, func(ad tracker.Adapter, c tracker.Credential) error { return s.settleListed(ctx, ds, ad, c, p) })
	if err != nil {
		s.publicationFail(w, r, ref, dispatch.Receipt{}, err, ds, ctx, id)
		return
	}
	s.trackerLog(r, ref, http.StatusOK)
	s.publicationReply(ctx, w, ds, id, http.StatusOK, nil)
}

// handlePublicationClose is the person's Close now, for a sent publication
// whose asked close is pending, closing or refused (closeIssue); one
// closed already is answered as it stands. expectedRev, when given, is the
// publication's. 200 {publication}; 409 target_changed, nothing written.
func (s *Server) handlePublicationClose(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	caps := s.trackerCaps()
	ds, id, ctx, done, ok := s.publicationAt(w, r, &body, &body, caps.busy+caps.post+2*caps.write)
	if !ok {
		return
	}
	defer done()
	p, err := ds.Publication(ctx, id)
	switch {
	case err != nil:
		s.dispatchError(w, "publication", err)
		return
	case p.CloseState == dispatch.CloseClosed:
		s.publicationReply(ctx, w, ds, id, http.StatusOK, nil)
		return
	case body.ExpectedRev != 0 && body.ExpectedRev != p.Rev:
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalStaleRevision, Message: "the publication changed: read it again"}})
		return
	case p.State != dispatch.PublicationSent || p.CloseIssue == "":
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalInvalid, Message: "only a sent publication whose close was asked for is closed"}})
		return
	}
	ref := publicationRef(p)
	if err := s.atGitHub(ctx, ref, func(ad tracker.Adapter, c tracker.Credential) error { return s.closeIssue(ctx, ds, ad, c, p) }); err != nil {
		s.publicationFail(w, r, ref, dispatch.Receipt{}, err, ds, ctx, id)
		return
	}
	s.trackerLog(r, ref, http.StatusOK)
	s.publicationReply(ctx, w, ds, id, http.StatusOK, nil)
}

// handlePublicationDiscard discards a draft, a failed or an unconfirmed
// publication (publication.discard); {receipt, publication}.
func (s *Server) handlePublicationDiscard(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, _, id, ok := s.opAt(w, r, nil, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.DiscardPublication(ctx, body.op(r), id)
	s.writeOp(w, "publication", rc, err, false, "publication", s.publicationAfter(ctx, ds, id))
}

// handlePublicationRead answers a publication whole: {publication,
// preview, postSha256}.
func (s *Server) handlePublicationRead(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	s.publicationReply(r.Context(), w, ds, id, http.StatusOK, nil)
}

// handlePublications pages a ticket's publications without their bodies,
// newest first, at most dispatch.MaxPublicationPage: query cursor (the
// last id of the page before) and needYou=1 (those a person can act on
// alone). {publications, next}.
func (s *Server) handlePublications(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	q, err := readQuery(r, "cursor", "needYou")
	var cursor, needYou string
	if err == nil {
		if cursor, err = q.one("cursor"); err == nil {
			needYou, err = q.one("needYou")
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if cursor != "" && !dispatch.ValidID(cursor) || needYou != "" && needYou != "1" {
		writeError(w, http.StatusBadRequest, "invalid_request", "cursor is a publication's id; needYou is 1")
		return
	}
	page, next, err := ds.Publications(r.Context(), key, cursor, needYou == "1")
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"publications": page, "next": next})
}

// handlePublicationPrepare prepares a publication of a ticket (body
// publicationBody): replayed first; then, under the target's lock, the
// target read from GitHub (the linked issue resolved by its node id; a
// link read again and recorded, then resolved by the node its read found),
// and publication.prepare run with it and the adapter's marker. 201
// {receipt, publication, preview, postSha256}.
func (s *Server) handlePublicationPrepare(w http.ResponseWriter, r *http.Request) {
	var body publicationBody
	ds, key, ok := s.externalStart(w, r, &body, &body.dispatchOp, ticketKey)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.busy+caps.reads+2*caps.write)
	defer cancel()
	op := body.op(r)
	args := dispatch.PublicationArgs{Key: key, Target: body.Target, Body: body.Body, Parts: body.Parts, CloseIssue: body.CloseIssue}
	if rc, found, err := ds.Replay(ctx, op, dispatch.IntentPublicationPrepare, args); err != nil || found {
		s.prepared(ctx, w, ds, rc, err)
		return
	}
	linkID, isLink := strings.CutPrefix(body.Target, "link:")
	if body.Target != dispatch.TargetLinked && (!isLink || !dispatch.ValidID(linkID)) {
		writeError(w, http.StatusBadRequest, "invalid_request", "the target is linked or link:<id>")
		return
	}
	var ref tracker.Ref
	var node string
	lock := "ticket:" + key
	if isLink {
		if ref, ok = s.readableLink(ctx, w, ds, key, linkID); !ok {
			return
		}
		lock = "link:" + linkID
	}
	release, ok := s.lockFor(ctx, w, lock)
	if !ok {
		return
	}
	defer release()
	tv, ok := s.linkedTicket(ctx, w, ds, key)
	if !ok {
		return
	}
	rctx, rcancel := context.WithTimeout(ctx, caps.reads)
	defer rcancel()
	if isLink {
		if err := s.readLinkIn(rctx, ctx, ds, key, linkID, ref, false); err != nil {
			s.publicationFail(w, r, ref, dispatch.Receipt{}, err, ds, ctx, "")
			return
		}
		lr, _, err := ds.LinkReadOf(ctx, linkID)
		if err != nil {
			s.dispatchError(w, "link", err)
			return
		}
		node = lr.NodeID
	} else if ref, node, ok = snapshotRef(tv.ExternalSnapshot); tv.ExternalID == "" || !ok {
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalInvalid, Message: key + " has no linked issue"}})
		return
	}
	var t tracker.Target
	err := s.atGitHub(rctx, ref, func(ad tracker.Adapter, c tracker.Credential) error {
		var err error
		t, err = ad.Resolve(rctx, c, node)
		return err
	})
	if err != nil {
		s.publicationFail(w, r, ref, dispatch.Receipt{}, err, ds, ctx, "")
		return
	}
	wctx, wcancel := context.WithTimeout(ctx, caps.write)
	defer wcancel()
	rc, err := ds.PreparePublication(wctx, op, args, dispatch.PublicationTarget{NodeID: t.NodeID, SpaceID: string(t.SpaceID), Kind: t.Kind, URL: t.URL,
		Ref: t.Ref.String()}, s.trackers.gh.Marker)
	s.trackerLog(r, ref, http.StatusOK)
	s.prepared(wctx, w, ds, rc, err)
}

// prepared answers a prepare: its refusal as writeOp does, else 201 (200
// for a resend) {receipt, publication, preview, postSha256}.
func (s *Server) prepared(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, rc dispatch.Receipt, err error) {
	if err != nil || rc.Status != dispatch.StatusDone || rc.ID == "" {
		s.writeOp(w, "publication", rc, err, true, "publication", func(dispatch.Receipt) (any, error) { return nil, nil })
		return
	}
	status := http.StatusCreated
	if rc.Repeat {
		status = http.StatusOK
	}
	s.publicationReply(ctx, w, ds, rc.ID, status, &rc)
}

// offerPart is a part the composer may offer: the exact Markdown it
// inserts, by name.
type offerPart struct {
	Name     string `json:"name"`
	Markdown string `json:"markdown"`
}

// offerTarget is where a publication of the ticket may go.
type offerTarget struct {
	Target string `json:"target"`
	URL    string `json:"url"`
}

// handlePublishOffer answers what the composer may offer for a ticket:
// the parts (deliveries: the live delivery links' URLs and notes; checks:
// the delivered candidate's verification receipt, each check's name and
// passed or failed; key: the ticket's key and title), each the exact
// Markdown it would insert, cut to the body's bound; and the targets (the
// linked issue, and each live delivery or external link to a GitHub
// item). Never a comment, a finding, a plan's text, an attachment or a
// check's output. Every send here posts as the person's own account, so no
// byline is offered.
func (s *Server) handlePublishOffer(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	tv, err := ticketFor(r.Context(), ds, key)
	if err != nil {
		s.refusal(w, err)
		return
	}
	var targets []offerTarget
	if tv.ExternalID != "" {
		targets = append(targets, offerTarget{dispatch.TargetLinked, tv.ExternalURL})
	}
	var deliveries, checks []string
	for _, l := range slices.Backward(tv.Links) {
		if !l.DeletedAt.IsZero() || l.Kind != dispatch.LinkDelivery && l.Kind != dispatch.LinkExternal {
			continue
		}
		if _, ae := s.parseItem(l.URL, ""); ae == nil {
			targets = append(targets, offerTarget{"link:" + l.ID, l.URL})
		}
		if l.Kind != dispatch.LinkDelivery {
			continue
		}
		deliveries = append(deliveries, "- "+strings.TrimSpace(l.URL+" "+l.Note))
		var v dispatch.Verified
		if checks == nil && json.Unmarshal(l.Verified, &v) == nil && len(v.Receipts) > 0 {
			checks = []string{}
			for _, rc := range v.Receipts {
				verdict := "failed"
				if rc.Exit != nil && *rc.Exit == 0 && rc.Stable {
					verdict = "passed"
				}
				checks = append(checks, "- "+cmp.Or(rc.Phase, "check")+" "+rc.ID+": "+verdict)
			}
		}
	}
	var parts []offerPart
	add := func(name, head string, lines []string) {
		if len(lines) > 0 {
			md, _ := tracker.CutBytes(head+"\n\n"+strings.Join(lines, "\n"), dispatch.MaxPublicationBody)
			parts = append(parts, offerPart{name, md})
		}
	}
	add("deliveries", "Delivered in:", deliveries)
	add("checks", "Checks of the delivered candidate:", checks)
	parts = append(parts, offerPart{"key", "Conductor ticket " + tv.Key + ": " + tv.Title})
	if targets == nil {
		targets = []offerTarget{}
	}
	writeDispatch(w, http.StatusOK, map[string]any{"parts": parts, "targets": targets})
}

// SettlePublications settles, in the background, the publications the
// store holds being sent (a send a stop or a crash cut): one at a time,
// each under its lock, by a listing for its comment, within
// settleStartTimeout in all and the server's stop. It never posts or
// closes, and a server's start never waits for it.
func (s *Server) SettlePublications() {
	if s.dispatch == nil {
		return
	}
	s.track(func() {
		ctx, cancel := context.WithTimeout(s.external.stopping(), settleStartTimeout)
		defer cancel()
		ids, err := s.dispatch.SendingPublications(ctx, maxStartSettles)
		left := 0
		for _, id := range ids {
			release, ok := s.external.lock(ctx, "publication:"+id, s.trackerCaps().busy)
			if !ok {
				left++
				continue
			}
			p, err := s.dispatch.Publication(ctx, id)
			if err == nil && p.State == dispatch.PublicationSending {
				err = s.atGitHub(ctx, publicationRef(p), func(ad tracker.Adapter, c tracker.Credential) error { return s.settleListed(ctx, s.dispatch, ad, c, p) })
			}
			release()
			if err != nil {
				left++
			}
		}
		if err != nil || left > 0 {
			s.log.Info("the publications being sent at the start were not all settled; Check settles each", "left", left, "listed", err == nil)
		}
	})
}
