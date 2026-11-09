package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Brakeman's request_repair (docs/plans/brakeman.md, "PR-12 and PR-13";
// Program item BK2; #658): RV1's request for changes on a candidate, sent
// to RV1's own review route (POST /api/dispatch/tickets/{key}/reviews).
// It is an act as launch_approved_plan and prepare_followup are (turnAct's
// order: the grant, the act's entry, the checks, pending, the operation,
// its receipt copied), the review Dispatch's record of the ticket, its
// receipt Dispatch's under the actor brakeman and the derived op id. What
// the turn must see is Dispatch and the runs, both (a grant gives no
// read: the review is the ticket's, its candidate a run's); its scope the
// ticket's and the candidate's run's; the state the ticket's at the rev
// read, and the candidate its run's own attempt's result, of a run the
// server runs now (the newest of its chain), no later attempt of its task
// begun. The review is recorded with via brakeman (RV1 takes it from no
// body) and applied to that run alone: a continuation made since the act
// was admitted refuses its application (withReviewRun), never followed.
// It never comments on or approves a candidate, and never continues a run
// the server no longer runs: that takes the person's word (continueRun).

// repairActClasses are what a request_repair act needs its turn to see,
// every one of them.
var repairActClasses = []string{brakeman.ClassDispatch, brakeman.ClassRuns}

// repairArgs reads a request_repair act's body as RV1's route decodes it
// (unknown fields refused): a request for changes, nothing the server
// records for the person (via, continueRun), the run's revision none or
// one (runRevisionRefused, as RV1's route asks), its candidate parsed. canon is
// what the act's args are hashed from: every field but the op id and the
// name.
func repairArgs(raw []byte) (actBody, any, *apiError) {
	var b reviewSubmitBody
	if err := store.DecodeStrict(raw, &b); err != nil {
		return actBody{}, nil, newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	switch {
	case b.Verdict != dispatch.VerdictRequestChanges:
		return actBody{}, nil, newAPIError(http.StatusForbidden, "not_granted", "request_repair sends a request for changes (verdict request_changes): a comment on a candidate or its approval is the person's")
	case b.Via != "":
		return actBody{}, nil, newAPIError(http.StatusBadRequest, "invalid_request", "via is the server's: it records brakeman for your act")
	case b.ContinueRun:
		return actBody{}, nil, newAPIError(http.StatusForbidden, "not_granted", "continuing a run the server no longer runs takes the person's word (continueRun): Brakeman requests changes only of a run the server runs")
	}
	if aerr := runRevisionRefused(b.ExpectedRevision); aerr != nil {
		return actBody{}, nil, aerr
	}
	c, err := dispatch.ParseCandidate(b.Candidate)
	if err != nil {
		return actBody{}, nil, newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	body := actBody{OpID: b.OpID, ExpectedRev: b.ExpectedRev, Review: b, Candidate: c}
	b.OpID, b.ByName = "", ""
	return body, b, nil
}

// repairAdmitted holds a request for changes of the candidate c to the
// turn's scope: the candidate's run admitted. nil admits it.
func repairAdmitted(ctx context.Context, o *observer, c dispatch.Candidate) *apiError {
	rr, ok, err := o.runAdmitted(ctx, c.Run)
	switch {
	case err != nil:
		return newAPIError(http.StatusInternalServerError, "store_failed", "the candidate's run could not be read")
	case !rr.found:
		return newAPIError(http.StatusNotFound, "not_found", "no run "+c.Run)
	case !ok:
		return newAPIError(http.StatusForbidden, "outside_scope", "the candidate's run is outside the request's scope")
	}
	return nil
}

// repairTargetState checks the candidate c is in the state a request for
// changes by Brakeman requires: its run the newest of its chain and run by
// the server now, the candidate its attempt's result (candidateOfLedger),
// and no later attempt of its task begun. nil when it is. The engine
// judges the candidate again as it applies the request (standingCandidate).
func (s *Server) repairTargetState(ctx context.Context, c dispatch.Candidate) *apiError {
	if s.ledger == nil {
		return newAPIError(http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened, so the candidate cannot be read")
	}
	newest, live, err := s.newestOf(ctx, c.Run)
	switch {
	case err != nil:
		return newAPIError(http.StatusInternalServerError, "store_failed", "the candidate's run could not be read")
	case newest != c.Run:
		return newAPIError(http.StatusConflict, "stale_target", fmt.Sprintf("run %s was continued as %s: request changes of a candidate there", c.Run, newest))
	case !live:
		return newAPIError(http.StatusConflict, "stale_target", fmt.Sprintf("run %s is not running on this server: continuing it to repair a candidate takes the person's word", c.Run))
	}
	_, _, aerr, err := s.candidateOfLedger(ctx, c)
	switch {
	case err != nil:
		return newAPIError(http.StatusInternalServerError, "store_failed", "the candidate could not be read")
	case aerr != nil:
		return aerr
	}
	attempts, err := s.ledger.Attempts(ctx, c.Run)
	if err != nil {
		return newAPIError(http.StatusInternalServerError, "store_failed", "the candidate's attempts could not be read")
	}
	if i := slices.IndexFunc(attempts, func(a ledger.AttemptRow) bool { return a.TaskID == c.Task && a.N > c.Attempt }); i >= 0 {
		return newAPIError(http.StatusConflict, "stale_target", fmt.Sprintf("the candidate moved: attempt %d of %s began after it", attempts[i].N, c.Task))
	}
	return nil
}

// runRepair sends the act's review to RV1's own function under the actor
// brakeman, its op id op, answering to rec as RV1's route does: recorded
// with via brakeman, on its route's own bound (ReviewTimeout), applied to
// the candidate's run alone.
func (s *Server) runRepair(ctx context.Context, rec http.ResponseWriter, ds *dispatch.Store, key, op string, body actBody) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ReviewTimeout)
	defer cancel()
	rb := body.Review
	rb.Via = dispatch.ViaBrakeman
	s.submitReview(withReviewRun(ctx, body.Candidate.Run), rec, ds, dispatch.Op{Actor: brakemanActor, ID: op, ExpectedRev: body.ExpectedRev, By: brakemanBy}, key, rb)
}
