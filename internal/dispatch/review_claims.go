package dispatch

import (
	"context"
	"errors"
)

// A review's try is settled only by whoever holds the store's claim on
// that exact try (docs/plans/review-repair.md, PR-4). A claim is taken by
// a compare-and-set on the review's row, its try and its state, under the
// lock that orders the store's writers, so no write lands between the
// look and the claim: SubmitReviewClaimed and ApplyingReviewClaimed take
// it in the transaction that makes the try pending, and ClaimReview takes
// it on a try already pending, reading the review inside it. Whoever holds
// it is the one actor that may send the try's command, record its outcome
// (AppliedReviewClaimed, RefusedReviewClaimed) and drop what it kept of
// the try; a try stale by the time an actor asks (settled, or a later try
// begun) is not claimed, and that actor stops. A review claimed is made
// pending again by no one (ErrReviewClaimed), and an outcome recorded
// without the claim is refused while anyone holds one. Claims live with
// the open store: a restart holds none, so what a server that ended was
// doing is the next start's to settle.

// ErrReviewClaimed is Request again's error, no receipt kept, while
// another holds the review's claim: wait for it (ReviewFree) and send it
// again.
var ErrReviewClaimed = errors.New("dispatch: the review is claimed by another")

// ReviewClaim is a claim on one try of a review, as its holder keeps it;
// the zero value claims nothing.
type ReviewClaim struct {
	Review string
	Try    int
	token  uint64
}

// Held reports whether c is a claim at all.
func (c ReviewClaim) Held() bool { return c.token != 0 }

// reviewClaim is a claim as the store keeps it: the try, the token of its
// holder, and a channel closed when it is released.
type reviewClaim struct {
	try   int
	token uint64
	free  chan struct{}
}

// claimLocked claims the review's try, the caller holding mu and having
// checked that nobody holds a claim on it.
func (s *Store) claimLocked(id string, try int) ReviewClaim {
	if s.claims == nil {
		s.claims = map[string]*reviewClaim{}
	}
	s.claimSeq++
	s.claims[id] = &reviewClaim{try: try, token: s.claimSeq, free: make(chan struct{})}
	return ReviewClaim{Review: id, Try: try, token: s.claimSeq}
}

// heldLocked reports whether c is the claim the store holds on its
// review's try, under mu.
func (s *Store) heldLocked(c ReviewClaim) bool {
	h, ok := s.claims[c.Review]
	return ok && c.token != 0 && h.token == c.token && h.try == c.Try
}

// ClaimReview claims the try of the review with the id: ok only when the
// review is a request pending at that try and nobody holds a claim on it,
// the review answered as it stands then, read under the claim. Not ok,
// with no error, for a try stale (settled, or another begun), a review
// another holds, or a store that writes nothing (read-only, closed to
// writes).
func (s *Store) ClaimReview(ctx context.Context, id string, try int) (ReviewClaim, ReviewSummary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ReviewClaim{}, ReviewSummary{}, false, ErrClosed
	case s.readOnly || s.db == nil || !ValidID(id):
		return ReviewClaim{}, ReviewSummary{}, false, nil
	}
	if _, held := s.claims[id]; held {
		return ReviewClaim{}, ReviewSummary{}, false, nil
	}
	rv, err := scanReview(s.db.QueryRowContext(ctx, "SELECT "+reviewColumns+reviewFrom+" WHERE r.id = ?", id))
	switch {
	case isNoRows(err):
		return ReviewClaim{}, ReviewSummary{}, false, nil
	case err != nil:
		return ReviewClaim{}, ReviewSummary{}, false, err
	case rv.Verdict != VerdictRequestChanges || rv.Application.State != ApplicationPending || rv.Application.Try != try:
		return ReviewClaim{}, rv, false, nil
	}
	return s.claimLocked(id, try), rv, true, nil
}

// ReleaseReview ends the claim c, when it is still the one the store
// holds, waking whoever waits for the review (ReviewFree).
func (s *Store) ReleaseReview(c ReviewClaim) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.heldLocked(c) {
		return
	}
	h := s.claims[c.Review]
	delete(s.claims, c.Review)
	close(h.free)
}

// ReviewFree is closed when the claim held on the review now is released:
// nil when nobody holds one.
func (s *Store) ReviewFree(id string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.claims[id]; ok {
		return h.free
	}
	return nil
}
