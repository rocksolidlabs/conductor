package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A capture attached to a decision (Program item PV1, PR-11; owner
// decision 10): a comment on a DR1 decision record, made through DR1's
// own comment operation (crew.Engine.CommentDecision, kind comment) with
// the send's id as its operation's id, and the send this item's row. The
// send's input is committed pending with its request first (the decision
// and the comment's words); the comment then names the capture, its
// revision and the send's frozen document's ref (capture:<id>/<send>),
// and no text of the page's: the decision's reader follows the ref to
// what was sent. DR1's receipt for the send's id answers a resend, so a
// stop before the comment is settled by making it, and one after it by
// that receipt, never by a second comment. DR1 keeps an operation's row
// with the decision for as long as it keeps the decision (its operations
// are bounded by refusal, never trimmed), so no window ends the send's
// idempotency. A comment is taken in every state of a decision: a send
// that races an answer, its rev moved, reads the rev again and lands as a
// comment on the answered decision. The send route stays the admin's;
// DR1's comment route is not called, so its own wider rule
// (s.launching) admits no one here.

// decisionTries bounds a send's tries against a decision whose rev moves
// between its read and the comment: each move is a person's operation on
// the decision, so a few tries outlast any race but a storm.
const decisionTries = 5

// decisionSendRequest is a send to a decision's request as the send keeps
// it: the decision and the comment's words before the send's own ref,
// which names the send's id and is put after them as the comment is made.
type decisionSendRequest struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
}

// decisionRequestOf is the request a send of the capture id at rev to the
// decision keeps: the store's words alone, never the page's.
func decisionRequestOf(decision, id string, rev int64) json.RawMessage {
	b, _ := json.Marshal(decisionSendRequest{Decision: decision, Comment: fmt.Sprintf("Capture %s is attached at revision %d: what was sent is", id, rev)})
	return json.RawMessage(b)
}

// decisionComment is the comment a send to a decision makes, from its
// stored request and its own id alone.
func decisionComment(sv dispatch.CaptureSend) (string, string, error) {
	var req decisionSendRequest
	dec := json.NewDecoder(bytes.NewReader(sv.Request))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Decision == "" || req.Comment == "" {
		return "", "", errors.New("the send's stored request is not a decision's comment")
	}
	ref := brakeman.Ref{Kind: brakeman.RefCapture, Capture: sv.Capture, Send: sv.ID}
	return req.Decision, req.Comment + " " + ref.String() + ".", nil
}

// decisionRetry reports a refusal of DR1's that a later try may pass:
// the rev moved (read again at once), the decision's run being moved.
func decisionRetry(de *crew.DecisionError) bool {
	return de.Code == crew.CodeStaleDecision || de.Code == crew.CodeDecisionMoved || de.Code == crew.CodeNotHeld
}

// tryDecision makes the comment of the pending send to a decision with
// the id, under its gate: nothing when it is no longer pending. The
// decision's rev is read, then the comment made under the send's id; a
// rev that moved meanwhile is read again and the same comment made. DR1's
// receipt, a new one or the one it kept for the send's id, makes the send
// sent to the decision; DR1's refusal of what no try changes (no such
// decision, its operations spent, the id taken by another body) refuses
// it with DR1's words, which the route's first try (first) answers; any
// other answer keeps it pending.
func (s *Server) tryDecision(ctx context.Context, id string, first bool) (*apiError, error) {
	unlock, err := s.sends.gates.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ds := s.dispatch
	sv, err := ds.CaptureSend(ctx, id)
	if err != nil {
		return nil, err
	}
	if sv.Kind != dispatch.SendDecision || sv.State != dispatch.SendPending {
		if sv.State != dispatch.SendPending {
			s.sends.settled(id)
		}
		return nil, nil
	}
	if err := s.sends.at("try", id); err != nil {
		return nil, err
	}
	did, text, err := decisionComment(sv)
	if err != nil {
		return nil, s.sendRefused(ctx, sv, err.Error())
	}
	if s.runs == nil {
		s.sends.wait(id, "no engine holds the decisions here")
		return nil, nil
	}
	of, err := s.dispatch.CaptureOf(ctx, sv.Capture)
	if err != nil {
		return nil, err
	}
	for try := 1; ; try++ {
		d, err := s.runs.Decision(ctx, did)
		var de *crew.DecisionError
		if err == nil && (of.Run == "" || (d.Run != of.Run && d.OpenedIn != of.Run)) {
			// A capture is attached to a decision of its own run (the one
			// it was opened in, or the one holding it now), as the Send
			// offers it.
			words := fmt.Sprintf("%s is a decision of another run than the capture's", did)
			if of.Run == "" {
				words = fmt.Sprintf("a link's capture is of no run, and %s is a run's decision", did)
			}
			if err := s.sendRefused(ctx, sv, words); err != nil {
				return nil, err
			}
			if !first {
				return nil, nil
			}
			return newAPIError(http.StatusConflict, "decision_elsewhere", words), nil
		}
		if err == nil {
			if err := s.sends.at("decision_read", id); err != nil {
				return nil, err
			}
			_, err = s.runs.CommentDecision(ctx, did, sv.ID, d.Rev, crew.CommentKind, text, sv.Actor)
		}
		switch {
		case err == nil:
			if err := s.sends.at("admitted", id); err != nil {
				return nil, err
			}
			return nil, s.decisionSent(ctx, sv, did)
		case errors.As(err, &de) && decisionRetry(de) && try < decisionTries:
			continue
		case errors.As(err, &de) && !decisionRetry(de):
			if err := s.sendRefused(ctx, sv, de.Words); err != nil {
				return nil, err
			}
			if !first {
				return nil, nil
			}
			return newAPIError(decisionStatus(de.Code), de.Code, de.Words), nil
		case errors.As(err, &de):
			s.sends.wait(id, de.Words)
		default:
			s.sends.wait(id, "the decision could not be read or written: it is tried again")
			s.log.Warn("a capture's send to a decision is kept pending", "send", id, "err", err)
		}
		return nil, nil
	}
}

// decisionSent records the pending send sv as sent to the decision.
func (s *Server) decisionSent(ctx context.Context, sv dispatch.CaptureSend, did string) error {
	rc, err := s.dispatch.MarkSent(ctx, sendOp(sv, "sent"), sv.ID, did)
	if err != nil {
		return err
	}
	if rc.Status != dispatch.StatusDone && rc.Refusal != dispatch.RefusalStaleRevision {
		return fmt.Errorf("the send could not be recorded sent: %s", rc.Error)
	}
	s.sends.settled(sv.ID)
	return nil
}

// routeCaptureDecisions adds the decision card's read of the captures
// sent to a decision: the admin's, and a switchyard's 403.
func (s *Server) routeCaptureDecisions(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/decisions/{did}/captures", s.requireAdmin(s.launchingAny(s.handleDecisionCaptures)))
}

// handleDecisionCaptures answers the sends of captures sent to the
// decision, newest first, a page at a time (?after=, ?limit= 1 to 100):
// {sends, next?}, each without its request.
func (s *Server) handleDecisionCaptures(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	q, err := readQuery(r, "after", "limit")
	var after string
	limit := dispatch.MaxCapturePage
	if err == nil {
		after, err = q.one("after")
	}
	if err == nil {
		var n int
		var given bool
		if n, given, err = pageNumber(q, "limit"); given {
			limit = n
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "a page of a decision's captures takes after and limit, each once")
		return
	}
	page, err := ds.CaptureSends(r.Context(), dispatch.SendDecision, r.PathValue("did"), after, limit)
	if err != nil {
		s.dispatchError(w, "decision's captures", err)
		return
	}
	writeDispatch(w, http.StatusOK, page)
}
