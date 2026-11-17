package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A capture sent to a review (docs/plans/browser-evidence.md, "Sending" and
// "After RV1 and DR1", PR-10, #798): Request a change and Add to a review.
//
// Both are a capture's send of kind review, of a capture of a candidate's
// preview checkout (Decision 9: a capture of a worktree still being worked
// goes to a ticket), to the ticket that holds the candidate's run. The
// send freezes the capture's document and screenshot as every send does;
// a finding of kind capture names that send and an annotation of it, and
// the store makes the finding's words, path and line from the frozen
// document (dispatch.ProjectAnnotation), never from the client.
//
// Add to a review (draft) is sent in its one step: the person's draft in
// the browser then holds one capture finding per annotation, naming the
// send, and Send review sends them through RV1's own route.
//
// Request a change is RV1's Request changes, in the order RV1's route
// keeps (dispatch_reviews.go): the gate, the candidate read from the
// ledger, the run held by this server or the person's word to continue it,
// all before anything is kept; then the send, pending, its request the
// review it asks for (the ticket's key and the review, each finding
// naming the send once it is made), kept whole so the steps after it are
// taken again from the store alone; then the review, sent to RV1's own
// function under the send's actor with the op id cs.<send> (submitReview:
// recorded, and applied by Yardmaster as a repair whose request names
// the send's files as evidence, copied into the repair attempt's tree);
// then the send sent, its target the review (MarkSent checks the review
// cites the send), or refused with the review's words. A step lost (the
// server stopped between them) is taken again from the store by the same
// send sent again, and by the reviews' worker (settleReviewSends), each
// answered by its receipt where it was done: the review is made once.

// errCaptureRead is a capture finding's send that could not be read from
// Dispatch's store: the server's failure, never the client's.
var errCaptureRead = errors.New("a capture finding's send could not be read")

// reviewSendTo is where a send to a review goes, as the send route's body
// names it (C4): the candidate the capture shows, the annotations made
// findings (every one when none is named), and Add to a review (draft) or
// Request a change (verdict request_changes, a change's severity, major
// unless given, a summary, and the person's word to continue a run the
// server no longer runs).
type reviewSendTo struct {
	Candidate   string `json:"candidate"`
	Verdict     string `json:"verdict,omitempty"`
	Ks          []int  `json:"ks,omitempty"`
	Draft       bool   `json:"draft,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Summary     string `json:"summary,omitempty"`
	ContinueRun bool   `json:"continueRun,omitempty"`
}

// reviewAsked is Request a change's request as its send keeps it
// (dispatch.SendFacts.Request): the ticket's key and the review asked for,
// its findings' send left empty until the send is made.
type reviewAsked struct {
	Key    string             `json:"key"`
	Review dispatch.NewReview `json:"review"`
}

// capturesOf is the captures the findings name, in their order.
func capturesOf(fs []dispatch.NewFinding) []dispatch.CaptureRef {
	var out []dispatch.CaptureRef
	for _, f := range fs {
		if f.Kind == dispatch.FindingKindCapture && f.Capture != nil {
			out = append(out, *f.Capture)
		}
	}
	return out
}

// captureEvidence is the evidence files of the sends refs name, once per
// send in their order (dispatch.Store.SendEvidence): the frozen document,
// then the screenshot when the send carried one.
func (s *Server) captureEvidence(ctx context.Context, ds *dispatch.Store, refs []dispatch.CaptureRef) ([]crew.EvidenceRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if ds == nil {
		return nil, errors.New("no Dispatch store is open")
	}
	var out []crew.EvidenceRef
	var seen []string
	for _, ref := range refs {
		if slices.Contains(seen, ref.Send) {
			continue
		}
		seen = append(seen, ref.Send)
		files, err := ds.SendEvidence(ctx, ref.Send)
		if err != nil {
			return nil, fmt.Errorf("the send %s's files: %w", ref.Send, err)
		}
		for _, f := range files {
			out = append(out, crew.EvidenceRef{Name: f.Name, SHA256: f.SHA256, Bytes: f.Bytes, Send: f.Send})
		}
	}
	return out, nil
}

// openEvidenceFile opens the content a capture send holds, by its hash,
// from Dispatch's store, and nothing else (OpenSendFile): the engine's
// opener of a change request's evidence files (crew.UseEvidenceFiles).
func (s *Server) openEvidenceFile(ctx context.Context, send, sum string) (io.ReadCloser, error) {
	if s.dispatch == nil {
		return nil, errors.New("no Dispatch store is open")
	}
	f, err := s.dispatch.OpenSendFile(ctx, send, sum)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// reviewSendOp is the operation of a step of a send to a review, under
// the send's actor and name: the review (cs.<send>), its sent (.s) or its
// refused (.r).
func reviewSendOp(sv dispatch.CaptureSend, step string) dispatch.Op {
	id := "cs." + sv.ID
	if step != "" {
		id += "." + step
	}
	return dispatch.Op{Actor: sv.Actor, ID: id, By: sv.CreatedBy}
}

// reviewSendReply is a send to a review as the send route answers it: its
// status, the reply ({receipt, send, key} and the review when there is
// one, or {error, receipt, send} for a review refused), and an error
// before anything was kept.
type reviewSendReply struct {
	status int
	reply  map[string]any
}

// sendCaptureToReview is the send route's kind review, the operation not
// kept before (the route replays first): the capture checked against the
// candidate named and the ticket found, then Add to a review sent, or
// Request a change's gate, candidate and run checked, its send kept and
// its steps taken (finishReviewSend). An *apiError is a refusal before
// anything was kept.
func (s *Server) sendCaptureToReview(ctx context.Context, ds *dispatch.Store, op dispatch.Op, digest, id string, rev int64, to reviewSendTo) (reviewSendReply, *apiError, error) {
	cand, err := dispatch.ParseCandidate(to.Candidate)
	switch {
	case err != nil:
		return reviewSendReply{}, newAPIError(http.StatusBadRequest, "invalid_request", "the candidate is not <run>/<task>/<n>@<commit>"), nil
	case to.Draft && (to.Verdict != "" || to.Severity != "" || to.Summary != "" || to.ContinueRun):
		return reviewSendReply{}, newAPIError(http.StatusBadRequest, "invalid_request", "Add to a review puts the findings in your draft: its verdict, severity and summary are the draft's"), nil
	case !to.Draft && to.Verdict != dispatch.VerdictRequestChanges:
		return reviewSendReply{}, newAPIError(http.StatusBadRequest, "invalid_request", "Request a change is a review with the verdict request_changes"), nil
	case !to.Draft && to.Severity != "" && !slices.Contains(dispatch.Severities, to.Severity):
		return reviewSendReply{}, newAPIError(http.StatusBadRequest, "invalid_request", "the severity is none of blocker, major, minor and nit"), nil
	}
	c, err := ds.Capture(ctx, id)
	switch {
	case errors.Is(err, dispatch.ErrNotFound):
		return reviewSendReply{}, newAPIError(http.StatusNotFound, dispatch.RefusalNotFound, "no live capture "+id), nil
	case err != nil:
		return reviewSendReply{}, nil, err
	case !c.DeletedAt.IsZero():
		return reviewSendReply{}, newAPIError(http.StatusNotFound, dispatch.RefusalNotFound, "no live capture "+id), nil
	case c.Origin.Kind != dispatch.OriginCandidate || c.Origin.Candidate != to.Candidate:
		return reviewSendReply{}, newAPIError(http.StatusConflict, dispatch.RefusalInvalid,
			"the capture is not of "+cand.Name()+"'s preview: Request a change and Add to a review are a review of the candidate a capture shows, and a capture of a worktree being worked goes to a ticket"), nil
	}
	key, aerr, err := s.reviewTicket(ctx, ds, cand.Run)
	if aerr != nil || err != nil {
		return reviewSendReply{}, aerr, err
	}
	dest := dispatch.Destination{Kind: dispatch.SendReview, Candidate: to.Candidate, Ks: to.Ks, Draft: to.Draft}
	// The send's steps are this route's from its commit on: the worker
	// leaves a send of this operation alone while the route holds it.
	unlock, err := s.reviews.holdSend(ctx, sendKey(op.Actor, op.ID))
	if err != nil {
		return reviewSendReply{}, nil, err
	}
	defer unlock()
	if to.Draft {
		rc, err := ds.SendCapture(ctx, op, digest, id, rev, dest, dispatch.SendFacts{})
		if err != nil || rc.Status != dispatch.StatusDone {
			return reviewSendReply{}, storeRefusal(rc), err
		}
		return s.reviewSendAnswer(ctx, ds, rc, key, nil, nil)
	}
	// Request a change: RV1's checks before anything is kept.
	if aerr := s.launchAdmission(); aerr != nil {
		return reviewSendReply{}, aerr, nil
	}
	if s.ledger == nil {
		return reviewSendReply{}, newAPIError(http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened, so the candidate cannot be read; see the server's log"), nil
	}
	_, a, aerr, err := s.candidateOfLedger(ctx, cand)
	if aerr != nil || err != nil {
		return reviewSendReply{}, aerr, err
	}
	newest, live, err := s.newestOf(ctx, cand.Run)
	if err != nil {
		return reviewSendReply{}, nil, err
	}
	if !live && !to.ContinueRun {
		words := s.continuationWords(ctx, newest, cand)
		return reviewSendReply{status: http.StatusConflict, reply: map[string]any{"error": apiError{Code: "continuation_needed", Message: words}, "run": newest, "words": words}}, nil, nil
	}
	ks := to.Ks
	if len(ks) == 0 {
		for k := range c.Annotations {
			ks = append(ks, k+1)
		}
	}
	rv := dispatch.NewReview{Candidate: to.Candidate, Base: a.StartCommit, Verdict: dispatch.VerdictRequestChanges, Summary: to.Summary, Via: dispatch.ViaWeb,
		Findings: []dispatch.NewFinding{}}
	for _, k := range ks {
		rv.Findings = append(rv.Findings, dispatch.NewFinding{Kind: dispatch.FindingKindCapture, Intent: dispatch.FindingIntentChange,
			Severity: cmp.Or(to.Severity, "major"), Capture: &dispatch.CaptureRef{Capture: id, K: k}})
	}
	request, err := json.Marshal(reviewAsked{Key: key, Review: rv})
	if err != nil {
		return reviewSendReply{}, nil, err
	}
	rc, err := ds.SendCapture(ctx, op, digest, id, rev, dest, dispatch.SendFacts{Request: request})
	if err != nil || rc.Status != dispatch.StatusDone {
		return reviewSendReply{}, storeRefusal(rc), err
	}
	return s.stepsOfReviewSend(ctx, ds, rc, to.ContinueRun)
}

// continueReviewSend takes the steps left of the send the operation op
// made, rc its receipt found by the route's replay (the same send sent
// again), and answers it.
func (s *Server) continueReviewSend(ctx context.Context, ds *dispatch.Store, op dispatch.Op, rc dispatch.Receipt, continueRun bool) (reviewSendReply, *apiError, error) {
	unlock, err := s.reviews.holdSend(ctx, sendKey(op.Actor, op.ID))
	if err != nil {
		return reviewSendReply{}, nil, err
	}
	defer unlock()
	return s.stepsOfReviewSend(ctx, ds, rc, continueRun)
}

// stepsOfReviewSend takes the steps left of the send rc names, under its
// hold, and answers it.
func (s *Server) stepsOfReviewSend(ctx context.Context, ds *dispatch.Store, rc dispatch.Receipt, continueRun bool) (reviewSendReply, *apiError, error) {
	v, refusal, err := s.finishReviewSend(ctx, ds, rc.ID, continueRun)
	if err != nil {
		s.log.Warn("a capture's send to a review is kept pending: the reviews' worker finishes it", "send", rc.ID, "err", err)
	}
	key := ""
	if sv, err := ds.CaptureSend(ctx, rc.ID); err == nil {
		var asked reviewAsked
		if json.Unmarshal(sv.Request, &asked) == nil {
			key = asked.Key
		}
	}
	return s.reviewSendAnswer(ctx, ds, rc, key, v, refusal)
}

// reviewSendAnswer is the send's answer: 201 sent now, 202 pending, 200 a
// repeat, the review's refusal with its status.
func (s *Server) reviewSendAnswer(ctx context.Context, ds *dispatch.Store, rc dispatch.Receipt, key string, v *dispatch.Review, refusal *apiError) (reviewSendReply, *apiError, error) {
	sv, err := ds.CaptureSend(ctx, rc.ID)
	if err != nil {
		return reviewSendReply{}, nil, err
	}
	if key == "" {
		if c, err := dispatch.ParseCandidate(candidateAsked(sv)); err == nil {
			key, _, _ = s.reviewTicket(ctx, ds, c.Run)
		}
	}
	sv.Request = nil
	reply := map[string]any{"receipt": rc, "send": sv, "key": key}
	if v != nil {
		reply["review"] = v
	}
	status := http.StatusCreated
	switch {
	case refusal != nil:
		reply["error"] = refusal
		status = refusal.status
	case rc.Repeat:
		status = http.StatusOK
	case sv.State == dispatch.SendPending:
		status = http.StatusAccepted
	}
	return reviewSendReply{status: status, reply: reply}, nil, nil
}

// candidateAsked is the candidate a send to a review was asked for.
func candidateAsked(sv dispatch.CaptureSend) string {
	var to dispatch.Destination
	_ = json.Unmarshal(sv.Asked, &to)
	return to.Candidate
}

// storeRefusal is the store's refused receipt as a route's error, nil for
// one done.
func storeRefusal(rc dispatch.Receipt) *apiError {
	if rc.Status == dispatch.StatusDone || rc.Status == "" {
		return nil
	}
	status := http.StatusConflict
	switch rc.Refusal {
	case dispatch.RefusalNotFound:
		status = http.StatusNotFound
	case dispatch.RefusalBound:
		status = http.StatusRequestEntityTooLarge
	}
	return newAPIError(status, rc.Refusal, rc.Error)
}

// reviewTicket is the ticket whose review a candidate of the run is: the
// one live ticket that holds the run's chain; 409 not_ticket_run for none
// or a contested chain.
func (s *Server) reviewTicket(ctx context.Context, ds *dispatch.Store, run string) (string, *apiError, error) {
	holders, err := ds.ChainHolders(ctx, run)
	switch {
	case err != nil:
		return "", nil, err
	case len(holders) == 0:
		return "", newAPIError(http.StatusConflict, "not_ticket_run", "no ticket holds the run "+run+": a review is a ticket's, so send the capture to a ticket"), nil
	case len(holders) > 1:
		return "", newAPIError(http.StatusConflict, "not_ticket_run", "the run "+run+" is contested between tickets: remove a ticket's link to it first"), nil
	}
	return holders[0].Key, nil, nil
}

// finishReviewSend takes the steps of a pending send to a review its
// request leaves: the review it asks for, made through RV1's own function
// under the send's actor (op cs.<send>, so a review made before answers
// by its receipt), each finding naming the send; then the send sent to
// that review, or refused with the review's words. A failure of the server
// (5xx, a store that could not record the outcome) leaves it pending, for
// the same send sent again or the reviews' worker. It gives the review as
// it stands and the review's refusal when there is one.
func (s *Server) finishReviewSend(ctx context.Context, ds *dispatch.Store, sendID string, continueRun bool) (*dispatch.Review, *apiError, error) {
	sv, err := ds.CaptureSend(ctx, sendID)
	if err != nil {
		return nil, nil, err
	}
	if sv.Kind != dispatch.SendReview {
		return nil, nil, nil
	}
	if sv.State != dispatch.SendPending {
		if sv.State == dispatch.SendSent && dispatch.ValidID(sv.Target) {
			if v, err := ds.Review(ctx, sv.Target); err == nil {
				return &v, nil, nil
			}
		}
		return nil, nil, nil
	}
	var asked reviewAsked
	if err := json.Unmarshal(sv.Request, &asked); err != nil {
		return nil, nil, fmt.Errorf("the send %s's request cannot be read: %w", sendID, err)
	}
	for i := range asked.Review.Findings {
		if f := &asked.Review.Findings[i]; f.Capture != nil {
			f.Capture.Send = sendID
		}
	}
	rec := newReplyRecorder()
	s.submitReview(ctx, rec, ds, reviewSendOp(sv, ""), asked.Key, reviewSubmitBody{NewReview: asked.Review, ContinueRun: continueRun})
	var out struct {
		Receipt *dispatch.Receipt `json:"receipt"`
		Review  *dispatch.Review  `json:"review"`
		Error   *apiError         `json:"error"`
	}
	_ = json.Unmarshal(rec.body.Bytes(), &out)
	switch {
	case out.Receipt != nil && out.Receipt.Status == dispatch.StatusDone && dispatch.ValidID(out.Receipt.ID):
		if _, err := ds.MarkSent(ctx, reviewSendOp(sv, "s"), sendID, out.Receipt.ID); err != nil {
			return out.Review, nil, err
		}
		return out.Review, nil, nil
	case rec.status >= 400 && rec.status < 500:
		refusal := newAPIError(rec.status, "review_refused", http.StatusText(rec.status))
		if out.Error != nil {
			refusal = newAPIError(rec.status, cmp.Or(out.Error.Code, "review_refused"), cmp.Or(out.Error.Message, out.Error.Code))
		}
		if out.Receipt != nil && out.Receipt.Error != "" {
			refusal.Message = out.Receipt.Error
		}
		if _, err := ds.MarkRefused(ctx, reviewSendOp(sv, "r"), sendID, cutChars(refusal.Message, dispatch.MaxSendWhy)); err != nil {
			return nil, refusal, err
		}
		return nil, refusal, nil
	}
	return nil, nil, fmt.Errorf("the review of the send %s was not made (%d): it is taken again", sendID, rec.status)
}

// cutChars is s cut to at most n characters.
func cutChars(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n])
	}
	return s
}

// settleReviewSends takes the steps left of every pending send to a
// review, on the reviews' worker's pass: a send whose server stopped
// between its steps is finished from the store alone.
func (s *Server) settleReviewSends(ctx context.Context) {
	ds := s.dispatch
	if ds == nil {
		return
	}
	for after := ""; ctx.Err() == nil; {
		p, err := ds.PendingSends(ctx, after, dispatch.MaxCapturePage)
		if err != nil {
			s.log.Warn("the pending sends to a review could not be read; the next pass reads them again", "err", err)
			return
		}
		for _, sv := range p.Sends {
			if sv.Kind != dispatch.SendReview || ctx.Err() != nil {
				continue
			}
			unlock, ok := s.reviews.tryHoldSend(sendKey(sv.Actor, sv.OpID))
			if !ok {
				continue // its route takes its steps now
			}
			if _, _, err := s.finishReviewSend(ctx, ds, sv.ID, false); err != nil && ctx.Err() == nil {
				s.log.Warn("a capture's send to a review is kept pending: the next pass takes it again", "send", sv.ID, "err", err)
			}
			unlock()
		}
		if after = p.Next; after == "" {
			return
		}
	}
}

// sendKey is a send's operation as the hold names it.
func sendKey(actor, opID string) string { return actor + "\x00" + opID }

// holdSend holds the send of the operation key for the caller, waiting
// while another holds it, as long as ctx allows: a route takes a send's
// steps under it, and the worker leaves a held send alone (tryHoldSend).
func (g *reviewGate) holdSend(ctx context.Context, key string) (func(), error) {
	for {
		unlock, ok, wait := g.takeSend(key)
		if ok {
			return unlock, nil
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// tryHoldSend holds the send of the operation key when no one does.
func (g *reviewGate) tryHoldSend(key string) (func(), bool) {
	unlock, ok, _ := g.takeSend(key)
	return unlock, ok
}

// takeSend holds key when it is free, else gives what closes when it is.
func (g *reviewGate) takeSend(key string) (func(), bool, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch, held := g.sending[key]; held {
		return nil, false, ch
	}
	if g.sending == nil {
		g.sending = map[string]chan struct{}{}
	}
	ch := make(chan struct{})
	g.sending[key] = ch
	return func() {
		g.mu.Lock()
		delete(g.sending, key)
		g.mu.Unlock()
		close(ch)
	}, true, nil
}
