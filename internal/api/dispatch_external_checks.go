package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// A delivery pull request's CI job as a failed-check request (Program
// item IN1a, docs/plans/tracker-linking.md, PR-10; owner decision 13): a
// ticket's live delivery link to a GitHub pull request names the candidate
// it delivers, and a failed job of that pull request at the candidate's
// commit is a check finding of source external, prepared as RV1's
// prepared request is and bound again at a review's submit, both from a
// fresh read made with the person's own token. Jobs are read only at the
// pull request's head commit (Checks); a job that tested another commit
// than the delivered candidate's is refused, as RV1 refuses a receipt of
// another attempt, and its log is never fetched. A log's tail comes only
// from a GitHub Actions job (CheckLog: its last dispatch.MaxCheckExcerpt
// bytes, fetched from a host tracker.LogHost admits, without the token). A
// commit status is no CI job a repair can name. Every request to GitHub is
// made through atGitHub, so the server's stop ends it and the token goes
// only to its own API host; a line of the log names the route, the item's
// reference and the status, never what GitHub answered. What a job's read
// gives that GitHub wrote (its name, its app, its log) is kept in the
// store's forms (dispatch.JobText, dispatch.JobPage, the tail fitted) and
// reaches an agent only through internal/quoted (internal/crew). Only the
// admin asks: the route is a switchyard's 403, and neither a share link, a
// session's own token nor a Brakeman turn reaches it; nor does a turn's
// request for changes name an external job (repairArgs).

// ExternalJobTimeout bounds the prepared request from an external job:
// the item's read and its checks, then a job's log, each a phase of the
// GitHub reads' own budget (trackerReadsBudget). Exported for the CLI.
const ExternalJobTimeout = 2 * trackerReadsBudget

// ExternalReviewTimeout bounds a review's request that names an external
// job: a review's own bound and a fresh read of each job it names, one
// after the other, beside it. Exported for the CLI.
const ExternalReviewTimeout = ReviewTimeout + ExternalJobTimeout

// jobIDPattern is a check's id as a link's read names it: a GitHub Actions
// job, another app's check run, or a commit status, and its number.
var jobIDPattern = regexp.MustCompile(`^(job|run|status):([1-9][0-9]{0,18})$`)

// externalJob is a delivery's CI job as a fresh read gave it: the item
// read (its reference at its place now), the job, and the receipt filled
// as a check finding keeps it.
type externalJob struct {
	item tracker.Ref
	job  tracker.Check
	ref  dispatch.CheckRef
}

// externalClass is the class the server works out of a job's conclusion:
// a cancel cancelled, a start that failed the machine's, the rest the
// code's. A job of GitHub's has no attempt or chain of Conductor's to say
// more.
func externalClass(conclusion string) string {
	switch conclusion {
	case "cancelled":
		return "cancelled"
	case "startup_failure":
		return "infrastructure"
	}
	return "code"
}

// noLog reports a log's read that leaves the job without a tail but the
// request whole: no log for the job, a log kept where Conductor does not
// fetch from or behind the token, one gone or past its bound, or the log's
// own time ended. A refused token, a rate limit and the server's stop are
// the request's failure.
func noLog(err error) bool {
	var rl *tracker.ErrRateLimited
	return !errors.As(err, &rl) && !errors.Is(err, tracker.ErrUnauthorized) && !errors.As(err, new(*apiError))
}

// deliveryOf is the ticket tv's live delivery link with the id, the
// candidate it delivers and the pull request its URL names; an apiError
// for one that is not (404 not_found for no such live link, 409 invalid
// for a link that is no delivery, one that names no candidate, and one to
// an item that is no pull request; 400 not_a_tracker_url for a page no
// adapter reads).
func (s *Server) deliveryOf(tv dispatch.TicketView, id string) (dispatch.Link, dispatch.Candidate, tracker.Ref, *apiError) {
	i := slices.IndexFunc(tv.Links, func(l dispatch.Link) bool { return l.ID == id && l.DeletedAt.IsZero() })
	if i < 0 {
		return dispatch.Link{}, dispatch.Candidate{}, tracker.Ref{}, newAPIError(http.StatusNotFound, "not_found", "no such live link of "+tv.Key)
	}
	l := tv.Links[i]
	if l.Kind != dispatch.LinkDelivery {
		return l, dispatch.Candidate{}, tracker.Ref{}, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "only a delivery link's pull request has CI jobs a repair can name")
	}
	c, err := dispatch.ParseCandidate(l.Candidate)
	if l.Candidate == "" || err != nil {
		return l, dispatch.Candidate{}, tracker.Ref{}, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "the delivery link names no candidate: a CI job is bound to the candidate the link delivers")
	}
	ref, ae := s.parseItem(l.URL, "")
	if ae != nil {
		return l, c, tracker.Ref{}, ae
	}
	if ref.Kind != tracker.KindChange {
		return l, c, ref, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "the delivery link is to an issue: only a pull request's CI jobs are read")
	}
	return l, c, ref, nil
}

// readJob reads the pull request ref, its checks at its head commit, and
// the check match picks of them (atGitHub, the read phase within the
// caps' reads); then, for a GitHub Actions job that failed at the
// candidate c's commit and no other, its log's tail (the log phase, its
// own reads' cap). An apiError for what the asker is told: 404 not_found
// for a job not among the checks read (the words saying when the read
// was incomplete), 409 invalid for a job that tested another commit than
// c's, a commit status; GitHub's failures (trackerAnswer) as an error. The
// receipt is filled whatever the job's state, so a review's store judges
// a pass or a job not finished in its own words; judge, when set, refuses
// them here first, before any log is asked.
func (s *Server) readJob(ctx context.Context, ref tracker.Ref, c dispatch.Candidate, match func(tracker.Check) bool, judge bool) (externalJob, *apiError, error) {
	caps := s.trackerCaps()
	rctx, rcancel := context.WithTimeout(ctx, caps.reads)
	defer rcancel()
	var out externalJob
	var set tracker.CheckSet
	err := s.atGitHub(rctx, ref, func(ad tracker.Adapter, cr tracker.Credential) error {
		it, _, err := ad.Read(rctx, cr, ref, tracker.Validators{})
		if err != nil {
			return err
		}
		if it.Ref.Kind != tracker.KindChange || it.HeadSHA == "" {
			return newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "GitHub says "+ref.String()+" is no pull request with a head commit")
		}
		out.item = it.Ref
		set, err = ad.Checks(rctx, cr, it.Ref, it.HeadSHA)
		return err
	})
	if err != nil {
		return out, nil, err
	}
	i := slices.IndexFunc(set.Checks, match)
	if i < 0 {
		words := "no such CI job among the checks GitHub has at the pull request's head commit"
		if len(set.Incomplete) > 0 {
			words += fmt.Sprintf(" (the read was incomplete: %d checks not read, see them on GitHub)", max(set.Omitted, len(set.Incomplete)))
		}
		return out, newAPIError(http.StatusNotFound, "not_found", words), nil
	}
	job := set.Checks[i]
	kind, num, _ := strings.Cut(job.ID, ":")
	if kind == "status" {
		return out, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "a commit status is no CI job a repair can name: it has no check run and no log"), nil
	}
	if job.HeadSHA != c.Commit {
		return out, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("the CI job tested %s, not the delivered candidate's commit %s (%s)", shortCommit(job.HeadSHA),
			shortCommit(c.Commit), c.Name())), nil
	}
	if judge && job.Pending() {
		return out, newAPIError(http.StatusConflict, "task_state", "the CI job has not finished"), nil
	}
	if judge && !job.Failed() {
		return out, newAPIError(http.StatusConflict, "task_state", "the CI job did not fail: nothing failed to repair"), nil
	}
	out.job = job
	conclusion := checkWord(job.Conclusion)
	class := externalClass(conclusion)
	out.ref = dispatch.CheckRef{Source: dispatch.CheckSourceExternal, Receipt: dispatch.ExternalReceipt(out.item.Host, out.item.Space, num), Subject: job.HeadSHA,
		HeadSha: job.HeadSHA, Argv: []string{}, Name: dispatch.JobText(job.Name), Conclusion: conclusion, URL: dispatch.JobPage(job.URL),
		App: dispatch.JobText(job.App), Class: class, Chosen: class}
	if out.ref.Name == "" {
		out.ref.Name = "a job with no name"
	}
	if kind == "job" && job.HasLog && job.Failed() {
		lctx, lcancel := context.WithTimeout(ctx, caps.reads)
		defer lcancel()
		var tail tracker.LogTail
		err := s.atGitHub(lctx, out.item, func(ad tracker.Adapter, cr tracker.Credential) error {
			var e error
			tail, e = ad.CheckLog(lctx, cr, out.item, job, dispatch.MaxCheckExcerpt)
			return e
		})
		switch {
		case err == nil:
			out.ref.Tail = fitTail(string(tail.Bytes))
		case s.external.stopping().Err() != nil || !noLog(err):
			return out, nil, err
		}
	}
	out.ref.Quote = out.ref.Tail
	return out, nil, nil
}

// handleExternalJobRepair answers GET /api/dispatch/tickets/{key}/links/
// {id}/checks/{check}/repair: 200 the prepared failed-check request
// (preparedRepair) from the CI job {check} (job:<n> or run:<n>, as the
// link's read names it) of the pull request the ticket's live delivery
// link {id} names, read fresh at its head commit: the finding of kind
// check with its external receipt (its log's tail an Actions job's), the
// criteria the candidate's run was launched with, and Check again not
// offered ("GitHub runs this job"). 409 invalid for a job that tested
// another commit than the delivered candidate's, a commit status, a link
// that is no delivery, names no candidate or is to an issue; 409
// task_state for a job that has not finished or did not fail; 404
// not_found for a link or a job not in the fresh read; 400
// invalid_request for a check id that is none or any query; GitHub's
// failures as trackerAnswer says (412 no_token without a token for the
// host). Nothing is written.
func (s *Server) handleExternalJobRepair(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	check := r.PathValue("check")
	m := jobIDPattern.FindStringSubmatch(check)
	switch {
	case m == nil:
		writeError(w, http.StatusBadRequest, "invalid_request", "a check is job:<n> or run:<n>, as the link's read names it")
		return
	case m[1] == "status":
		writeError(w, http.StatusConflict, dispatch.RefusalInvalid, "a commit status is no CI job a repair can name: it has no check run and no log")
		return
	}
	ctx, cancel := s.trackerContext(r, ExternalJobTimeout)
	defer cancel()
	tv, err := ticketFor(ctx, ds, key)
	if err != nil {
		s.refusal(w, err)
		return
	}
	_, c, ref, ae := s.deliveryOf(tv, id)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	job, ae, err := s.readJob(ctx, ref, c, func(ch tracker.Check) bool { return ch.ID == check }, true)
	switch {
	case err != nil:
		s.trackerFail(w, r, ref, err)
		return
	case ae != nil:
		s.trackerLog(r, ref, ae.status)
		writeAPIError(w, ae)
		return
	}
	s.trackerLog(r, ref, http.StatusOK)
	out := preparedRepair{
		Finding: preparedFinding{Kind: "check", Intent: "change", Check: job.ref,
			Message: fmt.Sprintf("A CI job on GitHub failed on %s, the commit %s delivers (it ended %s).", shortCommit(c.Commit), c.Name(), job.ref.Conclusion)},
		Criteria: repairCriteria{Source: "checks", Checks: []crew.Check{}},
		Recheck:  recheckOffer{Run: c.Run, Attempt: c.Attempt, Commit: c.Commit, Why: "GitHub runs this job: run it again there"},
	}
	// The criteria the candidate's run was launched with, as RV1's
	// prepared request reads them, when the ledger has the run.
	if s.ledger != nil {
		if row, found, err := s.ledger.Run(ctx, c.Run); err == nil && found {
			out.Criteria, out.Plan, out.Ticket = criteriaOf(row, c.Task)
		}
	}
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(out); err != nil || b.Len() > MaxRepairReply {
		s.log.Error("a prepared failed-check request passed its bound", "route", r.Pattern, "bytes", b.Len(), "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the prepared request could not be made")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Bytes())
}

// externalEvidence is a review's external check finding's receipt, filled
// from a fresh read as the prepared request fills it (readJob): of the
// check run its receipt names, in the pull request of a live delivery link
// of the ticket that delivers the candidate reviewed (its candidate's
// commit c's) in the receipt's repository on the receipt's host. 409
// invalid when no such link is the ticket's, or the job tested another
// commit; 404 for a job not among the checks read, in words of the
// finding k; GitHub's failures as trackerAnswer says. What the job's state
// is (not finished, passed) the store judges (dispatch's bindExternal).
func (s *Server) externalEvidence(ctx context.Context, ds *dispatch.Store, key string, k int, c dispatch.Candidate, req dispatch.CheckRequest) (*dispatch.CheckRef, *apiError) {
	host, space, num, ok := dispatch.ParseExternalReceipt(req.Receipt)
	if !ok {
		// Not of its shape: the store refuses it, its receipt kept.
		return nil, nil
	}
	tv, err := ticketFor(ctx, ds, key)
	if err != nil {
		var refused *dispatch.Refusal
		if errors.As(err, &refused) {
			return nil, newAPIError(http.StatusNotFound, "not_found", refused.Words)
		}
		return nil, newAPIError(http.StatusInternalServerError, "store_failed", "the ticket could not be read")
	}
	var last *apiError
	for _, l := range tv.Links {
		if l.Kind != dispatch.LinkDelivery || !l.DeletedAt.IsZero() {
			continue
		}
		_, lc, ref, ae := s.deliveryOf(tv, l.ID)
		if ae != nil || lc.Commit != c.Commit || ref.Host != host || !strings.EqualFold(ref.Space, space) {
			continue
		}
		job, ae, err := s.readJob(ctx, ref, c, func(ch tracker.Check) bool { return ch.ID == "job:"+num || ch.ID == "run:"+num }, false)
		status := http.StatusOK
		switch {
		case err != nil:
			ae = trackerAnswer(err, ref)
			if s.external.stopping().Err() != nil {
				ae = errItemBusy
			}
		case ae == nil && job.ref.Receipt != req.Receipt:
			ae = newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "the pull request is now at another repository: link it again")
		}
		if ae != nil {
			status = ae.status
		}
		s.log.Info("tracker read", "route", "a review's CI job", "ref", ref.Key(), "status", status)
		if ae == nil {
			return &job.ref, nil
		}
		last = ae
	}
	if last == nil {
		return nil, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("finding %d: no live delivery link of %s delivers the candidate reviewed (%s) as a pull request of %s", k, key, shortCommit(c.Commit), space))
	}
	return nil, newAPIError(last.status, last.Code, fmt.Sprintf("finding %d: %s", k, last.Message))
}

// externalNamed reports whether a review names an external job, for the
// longer bound its request has (ExternalReviewTimeout).
func externalNamed(rv dispatch.NewReview) bool {
	return slices.ContainsFunc(rv.Findings, func(f dispatch.NewFinding) bool {
		return f.Check != nil && f.Check.Source == dispatch.CheckSourceExternal
	})
}

// reviewTimeoutOf is the bound of a review's request: ExternalReviewTimeout
// when it names an external job, ReviewTimeout otherwise.
func reviewTimeoutOf(rv dispatch.NewReview) time.Duration {
	if externalNamed(rv) {
		return ExternalReviewTimeout
	}
	return ReviewTimeout
}
