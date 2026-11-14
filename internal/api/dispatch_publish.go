package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
// past dispatch.PublicationQuiet, unconfirmed, which nothing sends again;
// a listing not whole keeps its words. Nothing else posts or closes on
// GitHub.

// The bounds of a publication's routes: each step has its own cap from
// the moment it starts (trackerCaps: reads for a look at the item, post
// for a post, a listing or a close, write for a step of the store's), and
// each deadline is the sum of its steps' caps (trackerCaps.sending and
// the others below).
const (
	// PublishTimeout bounds a send: the lock; the target's resolve and the
	// account's read; the send begun; a try after an earlier one's look for
	// that try's comment; the post; its settle; then either a lost answer's
	// listing and its settle, or the close (closeSteps).
	PublishTimeout = trackerBusyWait + trackerReadsBudget + dispatchWriteTimeout + 2*publishPostBudget + dispatchWriteTimeout + closeSteps
	// PrepareTimeout bounds a prepare: the lock; a link's read; the link's
	// read recorded; the target's resolve; the prepare.
	PrepareTimeout = trackerBusyWait + 2*trackerReadsBudget + 2*dispatchWriteTimeout
	// CheckTimeout bounds a Check: the lock, the listing (its resolve and
	// at most twice ten pages), the settle.
	CheckTimeout = trackerBusyWait + publishPostBudget + dispatchWriteTimeout
	// CloseNowTimeout bounds Close now: the lock and closeSteps.
	CloseNowTimeout = trackerBusyWait + closeSteps
	// closeSteps is a close's steps: the look at the item, the close begun,
	// the close, its answer settled.
	closeSteps = trackerReadsBudget + dispatchWriteTimeout + publishPostBudget + dispatchWriteTimeout

	// publishPostBudget bounds one write to GitHub (the post, the close) or
	// one listing of an item's comments.
	publishPostBudget = 30 * time.Second
	// settleStartTimeout bounds the settle of the publications a server
	// finds being sent when it starts; the store lists at most
	// dispatch.MaxSendingPublications of them.
	settleStartTimeout = 2 * time.Minute
)

// closing, sending, preparing, checking and closeNow are the deadlines
// above of the caps c: the product's are defaultTrackerCaps, a test's
// scaled down.
func (c trackerCaps) closing() time.Duration   { return c.reads + c.write + c.post + c.write }
func (c trackerCaps) preparing() time.Duration { return c.busy + c.reads + c.write + c.reads + c.write }
func (c trackerCaps) sending() time.Duration {
	return c.busy + c.reads + c.write + 2*c.post + c.write + c.closing()
}
func (c trackerCaps) checking() time.Duration { return c.busy + c.post + c.write }
func (c trackerCaps) closeNow() time.Duration { return c.busy + c.closing() }

// step is a step's context: ctx bounded by the step's own cap d from the
// moment the step starts, read off the publications' clock (publishNow),
// so no step spends another's time.
func (s *Server) step(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, s.publishNow().Add(d))
}

// publishSeam tells the tests' seam (externalState.publishStep), when set,
// that a publication's route reached a point (at), with the step's own
// context where it names one: "looked" after a close's look at the item,
// "begun" after its begin was committed, "closing" just before the close is
// sent; "posting" just before a send's post, "listing" just before a
// listing for a comment; and in a prepare (id the ticket's key)
// "recorded" after a link's read was recorded, "resolving" just before the
// target's resolve.
func (s *Server) publishSeam(ctx context.Context, at, id string) {
	if f := s.external.publishStep; f != nil {
		f(ctx, at, id)
	}
}

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

// sendArgs is publication.send's content as the store fingerprints it
// (dispatch.BeginSend's id and digest, by the same names), for the
// Replay a send asks before anything else.
type sendArgs struct {
	ID         string `json:"id"`
	PostSha256 string `json:"postSha256"`
}

// sendable is the send's look before it asks GitHub anything: what the
// store's begin of a send (dispatch.BeginSend) admits, as the row read
// shows it: the digest the preview's, a publication of this store's
// instance, a draft or a failed one below its bound of tries, at the
// revision expected when one is. TestSendableIsTheStores holds the two to
// the same answer for every state.
func sendable(p dispatch.Publication, digest string, expectedRev int64) bool {
	return digest == p.PostSha256 && !p.Restored && (p.State == dispatch.PublicationDraft || p.State == dispatch.PublicationFailed) &&
		p.Tries < dispatch.MaxPublicationTries && (expectedRev == 0 || expectedRev == p.Rev)
}

// pubSaid names a sentence a publication's settle is handed
// (settleWords).
type pubSaid int

const (
	saidTargetChanged pubSaid = iota
	saidNotListed
	saidNotShown
	saidNotWhole
	saidClosedBefore
	saidCloseRefused
	saidCommentRefused
	saidClosedFound
	saidNotLookedFor
	saidNotLookedWhole
	saidMarkedOther
	pubSaids // how many there are
)

// settleWords is the one place the words a publication's settle is handed
// (SettlePublication's and SettleClose's) are made: one of the route's
// fixed sentences, four of them with the code of GitHub's answer for err
// (trackerAnswer's, tracker_unavailable for a code out of its form), and
// nothing GitHub wrote, no address and no item's name. Every sentence is
// within the store's plain words, which the row keeps whole.
func settleWords(said pubSaid, err error) string {
	code := "tracker_unavailable"
	if c := trackerAnswer(err, tracker.Ref{}).Code; c != "" && len(c) <= 40 && strings.Trim(c, "abcdefghijklmnopqrstuvwxyz_") == "" {
		code = c
	}
	switch said {
	case saidTargetChanged:
		return "the item is no longer in the repository it was prepared for, or no longer of its kind; prepare it again"
	case saidNotListed:
		return "GitHub's comments could not be listed (" + code + "); Check looks again"
	case saidNotShown:
		return "GitHub has not shown the comment yet; Check looks again"
	case saidNotWhole:
		return "GitHub's comments could not be listed whole; Check looks again"
	case saidClosedBefore:
		return "closed on GitHub before Conductor's close"
	case saidCloseRefused:
		return "GitHub refused the close (" + code + ")"
	case saidClosedFound:
		return "the issue was found closed on GitHub"
	case saidNotLookedFor:
		return "an earlier try's comment could not be looked for (" + code + "), so nothing was posted; Send looks again"
	case saidNotLookedWhole:
		return "GitHub's comments could not be listed whole, so nothing was posted; Send looks again"
	case saidMarkedOther:
		return "a comment carrying this publication's marker is on the item and is not this try's: nothing was posted; discard the publication and prepare a new one"
	}
	return "GitHub refused the comment (" + code + ")"
}

// errTargetChanged answers a write whose item is no longer of the kind, or
// in the repository, it was prepared for: nothing written.
var errTargetChanged = newAPIError(http.StatusConflict, "target_changed", "the item is no longer in the repository it was prepared for, or no longer of its kind: prepare it again")

// errRouteEnded answers a publication's route whose context ended before
// its reply (routeEnded).
var errRouteEnded = newAPIError(http.StatusServiceUnavailable, "tracker_busy", "the server is stopping or the request ran out of time: what was done is kept; send it again")

// errTryTooSoon answers a send whose earlier try began less than
// dispatch.PublicationQuiet ago (a *dispatch.TryTooSoon, tooSoon):
// nothing begun, so the same operation id may be sent again.
var errTryTooSoon = newAPIError(http.StatusServiceUnavailable, "tracker_busy", "the publication's last try began moments ago, and GitHub may not list its comment yet: nothing was sent; send it again after Retry-After")

// tooSoon answers a send's begin not made within the quiet time (a
// *dispatch.TryTooSoon): 503 tracker_busy, Retry-After the seconds left
// rounded up. False, answering nothing, for any other err.
func tooSoon(w http.ResponseWriter, err error) bool {
	var soon *dispatch.TryTooSoon
	if !errors.As(err, &soon) {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int((soon.Left+time.Second-1)/time.Second)))
	writeAPIError(w, errTryTooSoon)
	return true
}

// routeEnded is the one place a publication's route answers an error met
// once its own context ctx had ended (its time, or the server's stop,
// which ends it), whichever call met it (a replay, a begin, a read of the
// row, a step at GitHub, the lock): 503 tracker_busy (Retry-After) in
// errRouteEnded's words, what was done kept. False, answering nothing, for
// no error, a refusal (an answer, not a failure), the store closed (its
// own 503 dispatch_unavailable), and a context still live, whatever the
// server's stop.
func (s *Server) routeEnded(w http.ResponseWriter, ctx context.Context, err error) bool {
	var refused *dispatch.Refusal
	if err == nil || errors.As(err, &refused) || errors.Is(err, dispatch.ErrClosed) || ctx.Err() == nil {
		return false
	}
	w.Header().Set("Retry-After", "5")
	writeAPIError(w, errRouteEnded)
	return true
}

// publicationLock takes key's lock for a publication's route within the
// caps' wait; when it could not, the route's end (routeEnded), or 503
// tracker_busy (Retry-After) for a lock held past the wait: false once it
// answered.
func (s *Server) publicationLock(ctx context.Context, w http.ResponseWriter, key string) (func(), bool) {
	release, ok := s.external.lock(ctx, key, s.trackerCaps().busy)
	if !ok && !s.routeEnded(w, ctx, errItemBusy) {
		w.Header().Set("Retry-After", "5")
		writeAPIError(w, errItemBusy)
	}
	return release, ok
}

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
// for a send past dispatch.PublicationQuiet (words for one not yet past
// it); anything else, words. A restored draft or failed one is changed by
// a finding alone (the store's rule).
func (s *Server) listing(ctx context.Context, ad tracker.Adapter, c tracker.Credential, p dispatch.Publication) dispatch.Outcome {
	m := tracker.Match{Marker: p.Marker, BodySha256: p.PostSha256}
	if !p.Restored {
		m.AuthorID = tracker.RemoteID(p.SentAsID)
	}
	cm, found, err := ad.FindComment(ctx, c, publicationTarget(p), p.Since(), m)
	switch {
	case errors.Is(err, tracker.ErrTargetChanged):
		return dispatch.Outcome{Words: settleWords(saidTargetChanged, nil)}
	case err != nil:
		return dispatch.Outcome{Words: settleWords(saidNotListed, err)}
	case found == tracker.FoundYes:
		return sentOutcome(cm)
	case found == tracker.FoundNo && (p.State != dispatch.PublicationSending || s.publishNow().Sub(p.SendingAt) >= dispatch.PublicationQuiet):
		return dispatch.Outcome{State: dispatch.PublicationUnconfirmed}
	case found == tracker.FoundNo:
		return dispatch.Outcome{Words: settleWords(saidNotShown, nil)}
	}
	return dispatch.Outcome{Words: settleWords(saidNotWhole, nil)}
}

// settleListed looks for the publication's comment (listing) and settles
// what it found, each step within its own cap of ctx: the store's own
// operation (SettlePublication), which no ticket's state refuses, so a
// comment that was posted is recorded as posted.
func (s *Server) settleListed(ctx context.Context, ds *dispatch.Store, ad tracker.Adapter, c tracker.Credential, p dispatch.Publication) error {
	caps := s.trackerCaps()
	lctx, lcancel := s.step(ctx, caps.post)
	s.publishSeam(lctx, "listing", p.ID)
	o := s.listing(lctx, ad, c, p)
	lcancel()
	wctx, wcancel := s.step(ctx, caps.write)
	defer wcancel()
	_, _, err := ds.SettlePublication(wctx, p.ID, p.Attempt, o)
	return err
}

// sentOutcome is a comment GitHub answered or a listing found, as a
// settle records it.
func sentOutcome(cm tracker.Comment) dispatch.Outcome {
	return dispatch.Outcome{State: dispatch.PublicationSent, Comment: &dispatch.SentComment{ID: string(cm.ID), URL: cm.URL, AuthorID: string(cm.AuthorID), Author: cm.Author}}
}

// lookedBefore is the look a send makes, when the try it owns comes after
// an earlier one, before it posts, and it decides by the publication's
// marker, since find_since (Marked: every comment holding the marker, by
// anyone). The whole window lists none: post (true). One by the account of
// this try with the exact bytes: the try is recorded sent with that
// comment, nothing posted. Any other holding the marker (another
// account's, or other bytes): nothing posted, the try recorded failed in
// saidMarkedOther's words. Anything else (a listing not whole or failed,
// the item moved): nothing posted, the try recorded failed with words, so
// Send looks again. The store begins such a try only past
// dispatch.PublicationQuiet after the earlier one began.
func (s *Server) lookedBefore(ctx context.Context, ad tracker.Adapter, c tracker.Credential, p dispatch.Publication) (dispatch.Outcome, bool) {
	lctx, lcancel := s.step(ctx, s.trackerCaps().post)
	defer lcancel()
	s.publishSeam(lctx, "listing", p.ID)
	cm, found, err := ad.FindComment(lctx, c, publicationTarget(p), p.Since(), tracker.Match{Marker: p.Marker, BodySha256: p.PostSha256, AuthorID: tracker.RemoteID(p.SentAsID), Marked: true})
	switch {
	case errors.Is(err, tracker.ErrTargetChanged):
		return dispatch.Outcome{State: dispatch.PublicationFailed, Words: settleWords(saidTargetChanged, nil)}, false
	case err != nil:
		return dispatch.Outcome{State: dispatch.PublicationFailed, Words: settleWords(saidNotLookedFor, err)}, false
	case found == tracker.FoundYes:
		return sentOutcome(cm), false
	case found == tracker.FoundNo:
		return dispatch.Outcome{}, true
	case found == tracker.FoundOther:
		return dispatch.Outcome{State: dispatch.PublicationFailed, Words: settleWords(saidMarkedOther, nil)}, false
	}
	return dispatch.Outcome{State: dispatch.PublicationFailed, Words: settleWords(saidNotLookedWhole, nil)}, false
}

// publicationLive is what a person's act on the publication asks of the
// store before anything is asked of GitHub, as the store asks it of a
// comment's write: its ticket live (not_found, "<key> is deleted") and its
// project not archived (archived, "<project> is archived"), each a
// *dispatch.Refusal.
func publicationLive(ctx context.Context, ds *dispatch.Store, p dispatch.Publication) error {
	if _, err := ticketFor(ctx, ds, p.Ticket); err != nil {
		return err
	}
	project, _, _ := dispatch.ParseKey(p.Ticket)
	pv, err := ds.Project(ctx, project)
	if err == nil && !pv.Project.ArchivedAt.IsZero() {
		err = &dispatch.Refusal{Code: dispatch.RefusalArchived, Words: project + " is archived"}
	}
	return err
}

// closeIssue closes a sent publication's issue (p as its caller read it),
// each step within its own cap: first the look, the target resolved as the
// one prepared (errTargetChanged, nothing written). An issue found closed
// is recorded closed by the store's one decision of a close's answer
// (SettleClose), from pending, closing or refused, with no begin and no
// try spent; its words say it was closed before Conductor's close when no
// close of Conductor's was begun, and that it was found closed otherwise.
// Only for an issue found open is the close begun, at the revision p was
// read at, and committed (the store refuses it for a deleted ticket or an
// archived project, a restored publication, and past its bound); only the
// request whose begin changed the row goes on (a begin answered from
// another's receipt, or refused for a row that moved, asks GitHub nothing
// more and leaves the publication as it stands). The issue is then closed
// with a credential tagged with the try and the comment, and the answer
// settled: an answer that does not say leaves it closing, for Close now,
// and one for a close another request recorded closed meanwhile changes
// nothing. Of one server's requests a close is one at a time (the
// publication's lock); two servers over one data directory can each begin
// a try for one publication when their requests overlap, so GitHub can be
// asked twice to close one issue, which changes nothing for an issue
// already closed. Its caller asked publicationLive first.
func (s *Server) closeIssue(ctx context.Context, ds *dispatch.Store, ad tracker.Adapter, c tracker.Credential, p dispatch.Publication) error {
	caps := s.trackerCaps()
	rctx, rcancel := s.step(ctx, caps.reads)
	t, err := resolveSame(rctx, ad, c, p)
	rcancel()
	if err != nil {
		return err
	}
	s.publishSeam(ctx, "looked", p.ID)
	o := dispatch.CloseOutcome{State: dispatch.CloseClosed}
	if t.State != tracker.StateOpen {
		o.Words = settleWords(saidClosedFound, nil)
		if p.CloseTries == 0 {
			o.Words = settleWords(saidClosedBefore, nil)
		}
		return s.settleClose(ctx, ds, p.ID, o)
	}
	wctx, wcancel := s.step(ctx, caps.write)
	_, applied, err := ds.BeginClose(wctx, p.ID, p.Rev)
	if err == nil && applied {
		p, err = ds.Publication(wctx, p.ID)
	}
	wcancel()
	var refused *dispatch.Refusal
	switch {
	case err == nil && !applied, errors.As(err, &refused) && refused.Code == dispatch.RefusalStaleRevision:
		return nil
	case err != nil:
		return err
	}
	s.publishSeam(ctx, "begun", p.ID)
	cctx, ccancel := s.step(ctx, caps.post)
	s.publishSeam(cctx, "closing", p.ID)
	err = ad.Close(cctx, c.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: p.ID, Try: p.CloseTries, Comment: p.RemoteID}), t, p.CloseIssue)
	ccancel()
	switch {
	case err == nil:
	case tracker.NothingWritten(err):
		o = dispatch.CloseOutcome{State: dispatch.CloseRefused, Words: settleWords(saidCloseRefused, err)}
	default:
		return nil
	}
	return s.settleClose(ctx, ds, p.ID, o)
}

// settleClose records a close's outcome o (SettleClose) within a write's
// cap of its own.
func (s *Server) settleClose(ctx context.Context, ds *dispatch.Store, id string, o dispatch.CloseOutcome) error {
	wctx, wcancel := s.step(ctx, s.trackerCaps().write)
	defer wcancel()
	_, _, err := ds.SettleClose(wctx, id, o)
	return err
}

// publicationReply answers a publication's operation: rc (when one ran)
// and the publication whole, its preview and digest beside it. When the
// publication cannot be read because the server is stopping or the
// route's time ended, the answer is routeEnded's: what was done is kept,
// and the same operation id sent again answers the publication as it
// stands.
func (s *Server) publicationReply(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, id string, status int, rc *dispatch.Receipt) {
	p, err := ds.Publication(ctx, id)
	if s.routeEnded(w, ctx, err) {
		return
	}
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
// given, its id and revision in form), the route's context and the
// publication's lock; false once it answered. The lock is let go by the
// func.
func (s *Server) publicationAt(w http.ResponseWriter, r *http.Request, v any, op *dispatchOp, d time.Duration) (ds *dispatch.Store, id string, ctx context.Context, done func(), ok bool) {
	if !decodeExternal(w, r, v, op) {
		return nil, "", nil, nil, false
	}
	if ds, ok = s.dispatchWriter(w); !ok {
		return nil, "", nil, nil, false
	}
	if id, ok = pathID(w, r); !ok {
		return nil, "", nil, nil, false
	}
	ctx, cancel := s.trackerContext(r, d)
	release, ok := s.publicationLock(ctx, w, "publication:"+id)
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
// comment; a try after an earlier one looks first (lookedBefore) and posts
// only when the whole window lists no comment holding the publication's
// marker. The answer settles it: the comment, sent (and the close, when
// asked); a refusal proving nothing was posted, failed; anything else, a
// listing for the comment. 200 {receipt, publication}. A publication the
// store would not send (sendable: its ticket deleted or its project
// archived among it) is handed to the store with no account, which keeps
// its refusal under the operation's id, GitHub not asked. A try after an
// earlier one that began less than dispatch.PublicationQuiet ago is not
// begun (the store's rule, judged in the transaction that would begin it,
// and asked first of the row read, before any credential is read): 503
// tracker_busy, Retry-After the seconds left, no try spent and no receipt
// kept, so the same operation id may be sent again. A resend of a send
// being sent settles it first only while its ticket is live.
func (s *Server) handlePublicationSend(w http.ResponseWriter, r *http.Request) {
	var body publicationSendBody
	caps := s.trackerCaps()
	ds, id, ctx, done, ok := s.publicationAt(w, r, &body, &body.dispatchOp, caps.sending())
	if !ok {
		return
	}
	defer done()
	op := body.op(r)
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentPublicationSend, sendArgs{ID: id, PostSha256: body.PostSha256})
	if err != nil || found && rc.Status != dispatch.StatusDone {
		if !s.routeEnded(w, ctx, err) {
			s.writeOp(w, "publication", rc, err, false, "publication", s.publicationAfter(ctx, ds, id))
		}
		return
	}
	p, err := ds.Publication(ctx, id)
	if err != nil {
		s.publicationFail(w, r, tracker.Ref{}, dispatch.Receipt{}, err, ds, ctx, id)
		return
	}
	ref := publicationRef(p)
	if found {
		// A resend: nothing is posted; a send under way is settled first,
		// as far as GitHub answers, when its ticket is live.
		if p.State == dispatch.PublicationSending && publicationLive(ctx, ds, p) == nil {
			_ = s.atGitHub(ctx, ref, func(ad tracker.Adapter, c tracker.Credential) error { return s.settleListed(ctx, ds, ad, c, p) })
		}
		s.publicationReply(ctx, w, ds, id, http.StatusOK, &rc)
		return
	}
	ok = sendable(p, body.PostSha256, op.ExpectedRev)
	if ok {
		var refused *dispatch.Refusal
		if err := publicationLive(ctx, ds, p); errors.As(err, &refused) {
			ok = false
		} else if err != nil {
			s.publicationFail(w, r, ref, dispatch.Receipt{}, err, ds, ctx, id)
			return
		}
	}
	if !ok {
		// The store's refusal, kept under the operation's id; GitHub is not
		// asked.
		rc, _, err := ds.BeginSend(ctx, op, id, body.PostSha256, dispatch.Sender{})
		if !s.routeEnded(w, ctx, err) && !tooSoon(w, err) {
			s.writeOp(w, "publication", rc, err, false, "publication", s.publicationAfter(ctx, ds, id))
		}
		return
	}
	if left := p.TryWait(s.publishNow()); left > 0 {
		// The store's own rule at the begin (TryWait), asked of the row
		// read, before any credential is read.
		tooSoon(w, &dispatch.TryTooSoon{Left: left})
		return
	}
	err = s.atGitHub(ctx, ref, func(ad tracker.Adapter, c tracker.Credential) error {
		rctx, rcancel := s.step(ctx, caps.reads)
		t, err := resolveSame(rctx, ad, c, p)
		var who tracker.Account
		if err == nil {
			who, err = ad.Whoami(rctx, c)
		}
		rcancel()
		if err != nil {
			return err
		}
		wctx, wcancel := s.step(ctx, caps.write)
		var owned bool
		rc, owned, err = ds.BeginSend(wctx, op, id, body.PostSha256, dispatch.Sender{ID: string(who.ID), Login: who.Login})
		if err == nil && owned {
			p, err = ds.Publication(wctx, id)
		}
		wcancel()
		if err != nil || !owned {
			// Refused, or another's: this call posts nothing.
			return err
		}
		o, post := dispatch.Outcome{}, true
		if p.Attempt > 1 {
			o, post = s.lookedBefore(ctx, ad, c, p)
		}
		if post {
			pctx, pcancel := s.step(ctx, caps.post)
			s.publishSeam(pctx, "posting", id)
			cm, err := ad.Comment(pctx, c.ForWrite(tracker.Write{Kind: tracker.WriteComment, Publication: id, Try: p.Attempt, PostSha256: p.PostSha256}), t, p.Posted())
			pcancel()
			switch {
			case err == nil:
				o = sentOutcome(cm)
			case tracker.NothingWritten(err):
				o = dispatch.Outcome{State: dispatch.PublicationFailed, Words: settleWords(saidCommentRefused, err)}
			default:
				s.log.Info("a publication's post had no answer that says whether it was made; its comment is looked for", "publication", id, "try", p.Attempt)
				return s.settleListed(ctx, ds, ad, c, p)
			}
		}
		wctx, wcancel = s.step(ctx, caps.write)
		_, _, err = ds.SettlePublication(wctx, id, p.Attempt, o)
		if err == nil && o.State == dispatch.PublicationSent && p.CloseIssue != "" {
			if p, err = ds.Publication(wctx, id); err == nil {
				err = publicationLive(wctx, ds, p)
			}
		}
		wcancel()
		if err != nil || o.State != dispatch.PublicationSent || p.CloseIssue == "" {
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
		s.log.Info("a publication's send ended early; the publication says where it stands", "publication", id, "err", failCode(err, ref))
	}
	s.trackerLog(r, ref, http.StatusOK)
	s.publicationReply(ctx, w, ds, id, http.StatusOK, &rc)
}

// failCode is an error's code for a log line: a store's refusal's, else
// GitHub's answer's (trackerAnswer).
func failCode(err error, ref tracker.Ref) string {
	if refused := (*dispatch.Refusal)(nil); errors.As(err, &refused) {
		return refused.Code
	}
	return trackerAnswer(err, ref).Code
}

// publicationFail is the one place a publication's route answers what
// ended it before its reply, whatever step it was (the send's, Check's,
// Close now's, the prepare's):
//
//   - an error met once the route's own context had ended, as routeEnded
//     answers it: 503 tracker_busy;
//   - a send's begin not made within the quiet time (dispatch.TryTooSoon),
//     as tooSoon answers it: 503 tracker_busy, Retry-After the seconds left;
//   - a refusal the store kept under the operation's id (BeginSend's,
//     PreparePublication's: not_found for a deleted ticket, archived for
//     an archived project, stale_revision for another digest or revision,
//     invalid, bound) as writeOp answers it: 404 not_found, 409 with its
//     code, {error, receipt};
//   - a refusal the store returned and kept nowhere (BeginClose's,
//     SettleClose's, SettlePublication's: stale_revision for an operation
//     another made first at the same revision, invalid, bound, and
//     not_found for a publication or ticket that is not there, an older
//     store's among them; publicationLive's not_found and archived) as
//     refusal answers it: 404 not_found, 409 with its code, {error};
//   - an error of the store's (ErrNotFound: no such publication, as an
//     older store answers every one; unavailable, read-only, closed; an
//     operation out of form) as dispatchError answers it;
//   - anything else, GitHub's failure (trackerFail: target_changed and the
//     item's routes' answers).
//
// A send whose post was begun answers none of these: the store holds
// what happened, and its reply is the publication as it stands.
func (s *Server) publicationFail(w http.ResponseWriter, r *http.Request, ref tracker.Ref, rc dispatch.Receipt, err error, ds *dispatch.Store, ctx context.Context, id string) {
	var refused *dispatch.Refusal
	switch {
	case s.routeEnded(w, ctx, err):
	case tooSoon(w, err):
	case rc.Status == dispatch.StatusFailed:
		s.writeOp(w, "publication", rc, nil, false, "publication", s.publicationAfter(ctx, ds, id))
	case errors.As(err, &refused):
		s.refusal(w, err)
	case errors.Is(err, dispatch.ErrNotFound), errors.Is(err, dispatch.ErrUnavailable), errors.Is(err, dispatch.ErrClosed), errors.Is(err, dispatch.ErrReadOnly),
		errors.Is(err, dispatch.ErrBadOpID), errors.Is(err, dispatch.ErrBadActor), errors.Is(err, dispatch.ErrBadBy):
		s.dispatchError(w, "publication", err)
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
// for any other. 200 {publication}. A person's act: refused for a deleted
// ticket or an archived project (publicationLive) before any credential
// is read or anything is asked of GitHub.
func (s *Server) handlePublicationCheck(w http.ResponseWriter, r *http.Request) {
	var body publicationCheckBody
	caps := s.trackerCaps()
	ds, id, ctx, done, ok := s.publicationAt(w, r, &body, nil, caps.checking())
	if !ok {
		return
	}
	defer done()
	p, err := ds.Publication(ctx, id)
	if err == nil {
		err = publicationLive(ctx, ds, p)
	}
	if err != nil {
		s.publicationFail(w, r, tracker.Ref{}, dispatch.Receipt{}, err, ds, ctx, id)
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
// of this store's instance whose asked close is pending, closing or
// refused (closeIssue: the look at the item first, whatever the count of
// tries, then the begin, refused past its bound); one closed already is
// answered as it stands. expectedRev, when given, is the publication's.
// 200 {publication}; 409 target_changed, nothing written. A person's act:
// refused for a deleted ticket or an archived project (publicationLive),
// and for a restored publication, whose close is the original's, before
// anything is asked of GitHub. The store runs the close as its own
// operation, so the body's display name is checked here by the rule the
// store has for an operation's.
func (s *Server) handlePublicationClose(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	caps := s.trackerCaps()
	ds, id, ctx, done, ok := s.publicationAt(w, r, &body, &body, caps.closeNow())
	if !ok {
		return
	}
	defer done()
	if !utf8.ValidString(body.ByName) || utf8.RuneCountInString(body.ByName) > dispatch.MaxByName || strings.ContainsFunc(body.ByName, unicode.IsControl) {
		s.dispatchError(w, "publication", dispatch.ErrBadBy)
		return
	}
	p, err := ds.Publication(ctx, id)
	if err == nil && p.CloseState != dispatch.CloseClosed {
		err = publicationLive(ctx, ds, p)
	}
	switch {
	case err != nil:
		s.publicationFail(w, r, tracker.Ref{}, dispatch.Receipt{}, err, ds, ctx, id)
		return
	case p.CloseState == dispatch.CloseClosed:
		s.publicationReply(ctx, w, ds, id, http.StatusOK, nil)
		return
	case body.ExpectedRev != 0 && body.ExpectedRev != p.Rev:
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalStaleRevision, Message: "the publication changed: read it again"}})
		return
	case p.Restored:
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalInvalid, Message: "restored from an export: its issue's close is the original's"}})
		return
	case p.State != dispatch.PublicationSent || p.CloseIssue == "" ||
		p.CloseState != dispatch.ClosePending && p.CloseState != dispatch.CloseClosing && p.CloseState != dispatch.CloseRefused:
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalInvalid, Message: "only a sent publication whose close was asked for is closed"}})
		return
	}
	ref := publicationRef(p)
	if err := s.closeNow(ctx, ds, p); err != nil {
		s.publicationFail(w, r, ref, dispatch.Receipt{}, err, ds, ctx, id)
		return
	}
	s.trackerLog(r, ref, http.StatusOK)
	s.publicationReply(ctx, w, ds, id, http.StatusOK, nil)
}

// closeNow is Close now's steps under the publication's lock: the
// credential for the target's host, and closeIssue.
func (s *Server) closeNow(ctx context.Context, ds *dispatch.Store, p dispatch.Publication) error {
	return s.atGitHub(ctx, publicationRef(p), func(ad tracker.Adapter, c tracker.Credential) error { return s.closeIssue(ctx, ds, ad, c, p) })
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
	if !s.routeEnded(w, ctx, err) {
		s.writeOp(w, "publication", rc, err, false, "publication", s.publicationAfter(ctx, ds, id))
	}
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
		if !s.routeEnded(w, r.Context(), err) {
			s.dispatchError(w, "ticket", err)
		}
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"publications": page, "next": next})
}

// handlePublicationPrepare prepares a publication of a ticket (body
// publicationBody): replayed first; then judged by the store for all that
// needs no target (dispatch.JudgePrepare: the ticket live at the revision
// expected, its project not archived, the body, the close, the parts, the
// target one the ticket holds, a close of no item held as a pull request,
// the bound), a refusal kept under the operation's id with no credential
// read and nothing asked of GitHub; then, under the target's lock, replayed
// again (the same operation committed while this one waited answers its
// receipt) and judged again (prepareRefused: what another prepare or
// change made while this one waited refuses it as before the lock), and
// the target read from GitHub (the linked issue resolved by its node id;
// a link read again and recorded, then resolved by the node its read
// found), each step within its own cap, and publication.prepare
// run with it and the adapter's marker. 201 {receipt, publication,
// preview, postSha256}.
func (s *Server) handlePublicationPrepare(w http.ResponseWriter, r *http.Request) {
	var body publicationBody
	ds, key, ok := s.externalStart(w, r, &body, &body.dispatchOp, ticketKey)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.preparing())
	defer cancel()
	op := body.op(r)
	args := dispatch.PublicationArgs{Key: key, Target: body.Target, Body: body.Body, Parts: body.Parts, CloseIssue: body.CloseIssue}
	if s.prepareReplayed(ctx, w, ds, op, args) {
		return
	}
	linkID, isLink := strings.CutPrefix(body.Target, "link:")
	if body.Target != dispatch.TargetLinked && (!isLink || !dispatch.ValidID(linkID)) {
		writeError(w, http.StatusBadRequest, "invalid_request", "the target is linked or link:<id>")
		return
	}
	if s.prepareRefused(ctx, w, ds, op, args) {
		return
	}
	lock := "ticket:" + key
	if isLink {
		lock = "link:" + linkID
	}
	release, ok := s.publicationLock(ctx, w, lock)
	if !ok {
		return
	}
	defer release()
	if s.prepareReplayed(ctx, w, ds, op, args) || s.prepareRefused(ctx, w, ds, op, args) {
		return
	}
	tv, err := ticketFor(ctx, ds, key)
	if err != nil {
		if !s.routeEnded(w, ctx, err) {
			s.refusal(w, err)
		}
		return
	}
	var ref tracker.Ref
	var node string
	if isLink {
		var ae *apiError
		if ref, ae = s.linkRef(tv, linkID); ae != nil {
			writeAPIError(w, ae)
			return
		}
		lctx, lcancel := s.step(ctx, caps.reads)
		err := s.readLinkIn(lctx, ctx, ds, key, linkID, ref, false)
		lcancel()
		if err != nil {
			s.publicationFail(w, r, ref, dispatch.Receipt{}, err, ds, ctx, "")
			return
		}
		lr, _, err := ds.LinkReadOf(ctx, linkID)
		if err != nil {
			if !s.routeEnded(w, ctx, err) {
				s.dispatchError(w, "link", err)
			}
			return
		}
		node = lr.NodeID
		s.publishSeam(ctx, "recorded", key)
	} else if ref, node, ok = snapshotRef(tv.ExternalSnapshot); tv.ExternalID == "" || !ok {
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalInvalid, Message: key + " has no linked issue"}})
		return
	}
	rctx, rcancel := s.step(ctx, caps.reads)
	defer rcancel()
	var t tracker.Target
	err = s.atGitHub(rctx, ref, func(ad tracker.Adapter, c tracker.Credential) error {
		s.publishSeam(rctx, "resolving", key)
		var err error
		t, err = ad.Resolve(rctx, c, node)
		return err
	})
	if err != nil {
		s.publicationFail(w, r, ref, dispatch.Receipt{}, err, ds, ctx, "")
		return
	}
	wctx, wcancel := s.step(ctx, caps.write)
	defer wcancel()
	rc, err := ds.PreparePublication(wctx, op, args, dispatch.PublicationTarget{NodeID: t.NodeID, SpaceID: string(t.SpaceID), Kind: t.Kind, URL: t.URL,
		Ref: t.Ref.String()}, s.trackers.gh.Marker)
	s.trackerLog(r, ref, http.StatusOK)
	s.prepared(wctx, w, ds, rc, err)
}

// prepareRefused judges a prepare by the store's one judgement of all that
// needs no target (dispatch.JudgePrepare) and answers a refusal as the
// store keeps it under the operation's id (PreparePublication with no
// target, which refuses it the same), or the judgement's failure: false,
// answering nothing, when the prepare may go on to its target.
func (s *Server) prepareRefused(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, op dispatch.Op, args dispatch.PublicationArgs) bool {
	err := ds.JudgePrepare(ctx, args, op.ExpectedRev)
	if refused := (*dispatch.Refusal)(nil); errors.As(err, &refused) {
		rc, err := ds.PreparePublication(ctx, op, args, dispatch.PublicationTarget{}, s.trackers.gh.Marker)
		s.prepared(ctx, w, ds, rc, err)
	} else if err != nil && !s.routeEnded(w, ctx, err) {
		s.dispatchError(w, "publication", err)
	}
	return err != nil
}

// prepareReplayed answers a prepare whose operation the store has answered
// (Replay), or the replay's failure: false when there is none.
func (s *Server) prepareReplayed(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, op dispatch.Op, args dispatch.PublicationArgs) bool {
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentPublicationPrepare, args)
	if err != nil || found {
		s.prepared(ctx, w, ds, rc, err)
	}
	return err != nil || found
}

// prepared answers a prepare: its refusal as writeOp does, else 201 (200
// for a resend) {receipt, publication, preview, postSha256}.
func (s *Server) prepared(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, rc dispatch.Receipt, err error) {
	if s.routeEnded(w, ctx, err) {
		return
	}
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
		if !s.routeEnded(w, r.Context(), err) {
			s.refusal(w, err)
		}
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
// store holds being sent (a send a stop or a crash cut), as many as the
// store lists (dispatch.MaxSendingPublications): one at a time, each under
// its lock, by a listing for its comment, within settleStartTimeout in all
// and the server's stop. It never posts or closes, and a server's start
// never waits for it. It is the server's own settle, not a person's act:
// a deleted ticket or an archived project does not stop it, so a comment
// that was posted is recorded as posted.
func (s *Server) SettlePublications() {
	if s.dispatch == nil {
		return
	}
	s.track(func() {
		ctx, cancel := context.WithTimeout(s.external.stopping(), settleStartTimeout)
		defer cancel()
		ids, err := s.dispatch.SendingPublications(ctx, dispatch.MaxSendingPublications)
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
