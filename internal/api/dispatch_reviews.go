package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A person's review of a candidate, sent, applied, reconciled and marked
// (Program item RV1, docs/plans/review-repair.md, PR-4). A review is
// Dispatch's record (dispatch.Store.SubmitReview); a request for changes
// is applied by Yardmaster as a repair of the task that made the
// candidate (crew.Engine.RequestChanges), or, for a run the server no
// longer runs and only when the person said so (continueRun), as the
// continuation that carries the repair (crew.Engine.ContinueRunWith).
// The order is the launch's (dispatch_launch.go), each step durable
// before the next: the operation replayed first (a resend answers the
// review as it stands, before any evidence is read); for a new request
// the gate (crews.managed, the ledger this server's, writable and taken
// over), the candidate checked against the ledger and the ticket, each
// finding bound to it by the server (its anchor, a check finding's
// receipt read from the ledger), the request measured
// (crew.FitChangeRequest) and, for a run the server does not run, the
// person's word asked for (409 continuation_needed): nothing is recorded
// before all of that holds. Then Dispatch's commit (the review pending,
// try 1), Yardmaster's command (rv.<review>.<try>, under the review's
// actor), and Dispatch's commit of the outcome, read back from the ledger
// by the command's id whenever the command's answer was not its receipt,
// so an applied repair is never told as refused. A command read back
// accepted while the engine holds the run is left to settleReviews, a
// worker of the server's that OnRunChange only wakes, outside the
// engine's lock. At the start the pending reviews are reconciled by what
// the ledger holds (reconcileReviews), and nothing is applied then.
//
// One rule settles a try everywhere (readBack, the worker, the start's
// reconcile): its command is judged in the run that holds it, found by
// the command's id along the candidate's chain (the candidate's run, then
// the runs that continue it), never only in the run the review is
// registered on. A read that fails settles nothing: the review stays
// pending and the worker looks again (retryLater). An outcome Dispatch
// could not record stays with the worker, which writes it again until it
// is recorded.

const (
	// maxReviewBody bounds a review's body: fifty findings at their bounds
	// (each a path of 1 KiB, twelve lines of 200 bytes, words and a
	// resolution of 1,000 characters, a check's quote of 2 KiB), the
	// summary's 4,000 characters, as JSON writes them.
	maxReviewBody = 192 << 10
	// maxReviewRetryBody bounds Request again's body: an op, a display
	// name, a revision and the word to continue.
	maxReviewRetryBody = 4 << 10
	// maxMarkBody bounds a mark's body: an op, a candidate, a verdict and
	// a note of 1,000 characters at six bytes each.
	maxMarkBody = 16 << 10
	// ReviewTimeout bounds a review's request on the server: its run's
	// continuation (runTimeout) and its store writes, the review and its
	// outcome, each within its own bound. Exported for the CLI.
	ReviewTimeout = runTimeout + 2*dispatchWriteTimeout
	// MaxFindingsReply and the reviews' other reply bounds are the store's
	// (dispatch.MaxReviewsReply, MaxReviewReply, MaxMarksReply,
	// MaxFindingsReply); a findings page keeps dispatch.FindingReplyReserve
	// beside each finding for what this file adds to it (located, its
	// candidate, the addressed claims).
	// maxAddressedAttempts is how many repair attempts' addressed notes a
	// finding carries on its page: the last three that named it.
	maxAddressedAttempts = 3
	// reviewSettleWait bounds the worker's read of one review's command.
	reviewSettleWait = 30 * time.Second
	// reviewRetryWait is how long the worker waits before it looks again
	// at a review whose read or outcome's write failed.
	reviewRetryWait = 5 * time.Second
)

// The words of a review refused at its application, beside the
// receipt's.
const (
	wordsNotTaken     = "the server stopped before Yardmaster took the request: send it again"
	wordsTakenStopped = "Yardmaster took the request, but the server stopped before it made the repair, so nothing changed: send it again"
	wordsTakenLetGo   = "Yardmaster took the request, but the run was let go before its change was recorded, so nothing changed: send it again"
	wordsTakenDropped = "Yardmaster took the request, but its change was never recorded and the run has nothing left to record, so nothing changed: send it again"
	wordsNotFound     = "Yardmaster did not take the request: send it again"
	wordsUnread       = "the review could not be read to apply it, so nothing was sent: send it again"
)

// reviewGate holds what applies and settles the reviews in this process:
// the reviews being applied, by id, a second application of one waiting
// for the first (inFlight); the reviews left to the worker, by run
// (settling): a command read back accepted while the engine held its run,
// settled once its receipt settles, a read that failed, read again, and
// an outcome Dispatch could not record, written again; the runs a commit
// changed since the worker last looked (marked), and the worker's wake, a
// channel of one that a send never blocks on. Its zero value is ready
// once the worker's channels are made (startReviews).
type reviewGate struct {
	mu       sync.Mutex
	inFlight map[string]chan struct{}
	settling map[string]map[string]reviewSettle
	marked   map[string]bool
	wake     chan struct{}
	stop     context.CancelFunc
	done     chan struct{}
	once     sync.Once
	// retryWait is how long the worker waits to look again after a failed
	// read or write (reviewRetryWait when zero); a test's to set.
	retryWait time.Duration
	// The tests' seams, under mu: afterRegister with a review's id once it
	// is registered on its run, before its command is read back;
	// afterReadBack once the read-back's first read has answered, before
	// the engine's hold is read and the answer taken; beforeSettleRead with
	// the worker's context and a run before the worker reads its reviews'
	// commands; beforeApply with a review's id before its command is sent;
	// recordFails with a review's id before its outcome is written, the
	// write failing when it answers an error.
	afterRegister    func(review string)
	afterReadBack    func(review string)
	beforeSettleRead func(ctx context.Context, run string)
	beforeApply      func(review string)
	recordFails      func(review string) error
}

// reviewSettle is a review left to the worker: its try, its command, the
// actor the command is kept under and the candidate's run (anchor), from
// which the command is looked for along the chain. out, when set, is the
// try's outcome, known, that Dispatch could not record: the worker writes
// it again. atStart is one the start's reconcile could not read: the
// worker settles it as the reconcile does.
type reviewSettle struct {
	review  string
	try     int
	command string
	actor   string
	anchor  string
	out     *applyOutcome
	atStart bool
}

// setSeams sets the gate's seams for a test, under its lock.
func (g *reviewGate) setSeams(set func(g *reviewGate)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	set(g)
}

// seam runs one of the gate's seams, when a test set it.
func (g *reviewGate) seam(f *func(string), arg string) {
	g.mu.Lock()
	fn := *f
	g.mu.Unlock()
	if fn != nil {
		fn(arg)
	}
}

// hold makes this request the one applying the review: a second waits
// for the first, at most runTimeout, and holds it then; ok false when the
// wait ended first, nothing held. The release ends the hold.
func (g *reviewGate) hold(ctx context.Context, id string) (func(), bool) {
	for {
		g.mu.Lock()
		ch, ok := g.inFlight[id]
		if !ok {
			ch = make(chan struct{})
			if g.inFlight == nil {
				g.inFlight = map[string]chan struct{}{}
			}
			g.inFlight[id] = ch
			g.mu.Unlock()
			return func() {
				g.mu.Lock()
				delete(g.inFlight, id)
				g.mu.Unlock()
				close(ch)
			}, true
		}
		g.mu.Unlock()
		t := time.NewTimer(runTimeout)
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
			return nil, false
		case <-ctx.Done():
			t.Stop()
			return nil, false
		}
	}
}

// register puts the review on its run's set before its command is read
// back: a commit of the run from then on marks the run (runChanged), so
// the worker looks. A review is on one run's set at a time, at its newest
// try: a later try's registration replaces an earlier's, never the other
// way.
func (g *reviewGate) register(run string, e reviewSettle) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.settling == nil {
		g.settling = map[string]map[string]reviewSettle{}
	}
	for r, set := range g.settling {
		old, ok := set[e.review]
		switch {
		case !ok:
		case old.try > e.try:
			// A later try is registered: an earlier's is not put back.
			return
		case r != run:
			delete(set, e.review)
			if len(set) == 0 {
				delete(g.settling, r)
			}
		}
	}
	if g.settling[run] == nil {
		g.settling[run] = map[string]reviewSettle{}
	}
	g.settling[run][e.review] = e
}

// unregister takes the review's try e off its run's set: settled, or
// never left to the worker. A later try registered meanwhile (Request
// again, once e's outcome was recorded) is not e's to take off.
func (g *reviewGate) unregister(run string, e reviewSettle) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if set := g.settling[run]; set != nil {
		if now, ok := set[e.review]; !ok || now.try != e.try {
			return
		}
		delete(set, e.review)
		if len(set) == 0 {
			delete(g.settling, run)
		}
	}
}

// retryLater marks the run again once the retry's wait has passed, so the
// worker looks again at a review whose read or write failed although
// nothing commits the run.
func (g *reviewGate) retryLater(run string) {
	g.mu.Lock()
	wait := cmp.Or(g.retryWait, reviewRetryWait)
	g.mu.Unlock()
	time.AfterFunc(wait, func() { g.runChanged(run) })
}

// runChanged notes that a commit of the run landed: the engine's
// OnRunChange, under the engine's lock, so it takes the gate's own small
// lock for a map write and wakes the worker by a send that never blocks
// (a wake already queued covers it). A run no review waits on is not
// noted.
func (g *reviewGate) runChanged(run string) {
	g.mu.Lock()
	if g.settling[run] == nil {
		g.mu.Unlock()
		return
	}
	if g.marked == nil {
		g.marked = map[string]bool{}
	}
	g.marked[run] = true
	wake := g.wake
	g.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// takeMarked is the runs marked since the worker last looked, with the
// reviews waiting on each, the marks cleared.
func (g *reviewGate) takeMarked() map[string][]reviewSettle {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string][]reviewSettle{}
	for run := range g.marked {
		for _, e := range g.settling[run] {
			out[run] = append(out[run], e)
		}
	}
	g.marked = nil
	return out
}

// startReviews starts the worker that settles the reviews left to it, on
// a context of its own, ended by stopReviews before the stores close.
func (s *Server) startReviews() {
	g := &s.reviews
	ctx, cancel := context.WithCancel(context.Background())
	g.mu.Lock()
	g.wake = make(chan struct{}, 1)
	g.stop = cancel
	g.done = make(chan struct{})
	wake, done := g.wake, g.done
	if len(g.marked) > 0 {
		// Marked before the worker began (the start's reconcile).
		wake <- struct{}{}
	}
	g.mu.Unlock()
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			for run, es := range g.takeMarked() {
				for _, e := range es {
					if ctx.Err() != nil {
						return
					}
					s.settleOne(ctx, run, e)
				}
			}
		}
	}()
}

// stopReviews ends the worker and waits for it, once; nothing more is
// settled in this process (the next start's reconcile settles what is
// still pending).
func (s *Server) stopReviews() {
	g := &s.reviews
	g.once.Do(func() {
		g.mu.Lock()
		stop, done := g.stop, g.done
		g.mu.Unlock()
		if stop == nil {
			return
		}
		stop()
		<-done
	})
}

// settleOne settles the review e left on the run: an outcome known
// (e.out) is written again; one the start left (e.atStart) is settled as
// the reconcile settles it; any other by its command judged in the run
// that holds it (judgeCommand): done, applied; failed, refused with its
// words; still accepted once the engine no longer holds that run, or
// holds it settled, refused. Otherwise it stays, moved to the run that
// holds its command, for that run's next commit. A read or a write that
// fails keeps it, looked at again after reviewRetryWait.
func (s *Server) settleOne(ctx context.Context, run string, e reviewSettle) {
	ctx, cancel := context.WithTimeout(ctx, reviewSettleWait)
	defer cancel()
	s.reviews.mu.Lock()
	seam := s.reviews.beforeSettleRead
	s.reviews.mu.Unlock()
	if seam != nil {
		seam(ctx, run)
	}
	var out applyOutcome
	switch {
	case e.out != nil:
		out = *e.out
	case e.atStart:
		if s.ledger == nil {
			s.reviews.retryLater(run)
			return
		}
		o, held, err := reconcileCommand(ctx, s.ledger, e.anchor, e.actor, e.command)
		switch {
		case err != nil:
			s.log.Warn("a pending review's command could not be read; it is looked at again", "review", e.review, "run", run, "err", err)
			if ctx.Err() == nil {
				s.reviews.retryLater(run)
			}
			return
		case !held:
			// The ledger holds no run of it: left pending, for its owner.
			s.reviews.unregister(run, e)
			return
		}
		out = o
	default:
		o, at, err := s.judgeCommand(ctx, e, run, false, "")
		if o.pending {
			if at != run {
				s.reviews.register(at, e)
				s.reviews.runChanged(at)
			}
			if err != nil {
				s.log.Warn("a pending review's command could not be read; it is looked at again", "review", e.review, "run", at, "err", err)
				if ctx.Err() == nil {
					s.reviews.retryLater(at)
				}
			}
			return
		}
		out = o
	}
	if !s.recordOutcome(ctx, e.review, e.try, out) {
		e.out = &out
		s.reviews.register(run, e)
		if ctx.Err() == nil {
			s.reviews.retryLater(run)
		}
		return
	}
	s.reviews.unregister(run, e)
}

// commandOutcome is the outcome a command's row settles: done, applied in
// the run that holds it; failed, refused with its words; uncertain,
// refused. ok false for a command not found or still accepted.
func commandOutcome(row ledger.CommandRow, found bool) (applyOutcome, bool) {
	if !found {
		return applyOutcome{}, false
	}
	switch row.Status {
	case ledger.CommandDone:
		return appliedFrom(row.RunID, row), true
	case ledger.CommandFailed:
		return refusedFrom(row), true
	case ledger.CommandUncertain:
		return applyOutcome{why: wordsTakenStopped}, true
	}
	return applyOutcome{}, false
}

// judgeCommand judges the try e's command, registered on the run to: read
// along the candidate's chain (commandAlong, from e.anchor) and settled
// by its row (commandOutcome); when its row has not settled, the engine's
// hold on the run that holds it (or, for a command found nowhere, on to)
// is read and only then the command again, so a commit and a release
// between the two reads is seen by the second: still accepted while the
// engine holds that run unsettled, pending; once it does not, refused
// (wordsTakenLetGo), or holds it settled (wordsTakenDropped). A command
// found nowhere is, for a command whose call answered (answered, the
// read-back), refused with why at once; for the worker, judged by the
// hold as above, in wordsNotFound. It answers the outcome, the run that
// holds the command (to for one found nowhere), and the read's error,
// with the outcome pending, when a read failed.
func (s *Server) judgeCommand(ctx context.Context, e reviewSettle, to string, answered bool, why string) (applyOutcome, string, error) {
	row, found, err := s.commandAlong(ctx, e.anchor, e.actor, e.command)
	if answered {
		s.reviews.seam(&s.reviews.afterReadBack, e.review)
	}
	if err != nil {
		return applyOutcome{pending: true}, to, err
	}
	if out, ok := commandOutcome(row, found); ok {
		return out, row.RunID, nil
	}
	if !found && answered {
		return applyOutcome{why: cmp.Or(why, "Yardmaster did not take the request")}, to, nil
	}
	at := to
	if found {
		at = row.RunID
	}
	_, settled, held := s.runs.Settled(at)
	row, found, err = s.commandAlong(ctx, e.anchor, e.actor, e.command)
	if err != nil {
		return applyOutcome{pending: true}, at, err
	}
	if out, ok := commandOutcome(row, found); ok {
		return out, row.RunID, nil
	}
	if found && row.RunID != at {
		// Found in another run than the hold was read of: looked at again
		// there.
		return applyOutcome{pending: true}, row.RunID, nil
	}
	switch {
	case held && !settled:
		return applyOutcome{pending: true}, at, nil
	case !found:
		return applyOutcome{why: wordsNotFound}, at, nil
	case !held:
		return applyOutcome{why: wordsTakenLetGo}, at, nil
	}
	return applyOutcome{why: wordsTakenDropped}, at, nil
}

// applyOutcome is what became of a try: applied (the run, the command, the
// revision and the attempt), refused (why), or pending, left to the
// worker.
type applyOutcome struct {
	applied *dispatch.Applied
	why     string
	pending bool
	// watch is the run the worker keeps the review on when its outcome
	// cannot be recorded: the run that holds its command, else the run it
	// went to; empty for the candidate's run.
	watch string
}

// appliedFrom is the outcome of a command the ledger kept done in the run:
// the revision its receipt names and the repair attempt, the one after the
// task's last (its receipt's attempts, which run on across a chain).
func appliedFrom(run string, row ledger.CommandRow) applyOutcome {
	var rc crew.Receipt
	if row.Receipt != "" {
		_ = json.Unmarshal([]byte(row.Receipt), &rc)
	}
	attempt := 1
	if rc.Task != nil {
		attempt = rc.Task.Attempts + 1
	}
	return applyOutcome{applied: &dispatch.Applied{Run: run, Command: row.ID, Revision: rc.Revision, Attempt: attempt}}
}

// refusedFrom is the outcome of a command the ledger kept failed: its
// receipt's words.
func refusedFrom(row ledger.CommandRow) applyOutcome {
	var rc crew.Receipt
	if row.Receipt != "" {
		_ = json.Unmarshal([]byte(row.Receipt), &rc)
	}
	return applyOutcome{why: cmp.Or(rc.Error, "Yardmaster refused the request")}
}

// recordOutcome commits the try's outcome in Dispatch (the actor server's,
// review.applied or review.refused), reporting whether it is recorded: a
// failure is logged and the review stays pending, its outcome kept by the
// worker, which writes it again (settleOne, keepOutcome).
func (s *Server) recordOutcome(ctx context.Context, id string, try int, out applyOutcome) bool {
	if s.dispatch == nil || out.pending {
		return false
	}
	var rc dispatch.Receipt
	var err error
	s.reviews.mu.Lock()
	fails := s.reviews.recordFails
	s.reviews.mu.Unlock()
	if fails != nil {
		err = fails(id)
	}
	switch {
	case err != nil:
	case out.applied != nil:
		rc, err = s.dispatch.AppliedReview(ctx, dispatch.Op{}, id, try, *out.applied)
	default:
		rc, err = s.dispatch.RefusedReview(ctx, dispatch.Op{}, id, try, out.why)
	}
	if err != nil {
		s.log.Warn("a review's application could not be recorded; it stays pending", "review", id, "try", try, "err", err)
		return false
	}
	if rc.Status != dispatch.StatusDone {
		// Settled already (by the worker, or a twin), or past this try.
		s.log.Info("a review's application was settled before", "review", id, "try", try, "refusal", rc.Refusal, "words", rc.Error)
	}
	return true
}

// reviewCommandID is the command a try of a review applies: rv.<id>.<try>,
// at most 22 characters, inside crew.CommandIDPattern.
func reviewCommandID(id string, try int) string {
	return "rv." + id + "." + strconv.Itoa(try)
}

// Bodies of the review routes.
type (
	// reviewSubmitBody is a review: the operation (DS1's opId,
	// expectedRev, the ticket's, and byName), the review as sent
	// (dispatch.NewReview, its fingerprint with the key), and how to apply
	// a request, which is not recorded: the run's revision expected
	// (expectedRevision) and the word to continue a run the server does
	// not run (continueRun).
	reviewSubmitBody struct {
		dispatchOp
		dispatch.NewReview
		ExpectedRevision int64 `json:"expectedRevision,omitempty"`
		ContinueRun      bool  `json:"continueRun,omitempty"`
	}
	// reviewRetryBody is Request again: the operation, and how to apply.
	reviewRetryBody struct {
		dispatchOp
		ExpectedRevision int64 `json:"expectedRevision,omitempty"`
		ContinueRun      bool  `json:"continueRun,omitempty"`
	}
	// findingMarkBody is a mark: the operation and the mark as sent.
	findingMarkBody struct {
		dispatchOp
		dispatch.NewMark
	}
)

// reviewIDArgs is review.retry's content as the store fingerprints it:
// the review's id.
type reviewIDArgs struct {
	ID string `json:"id"`
}

// continuationNeeded answers 409 continuation_needed: the run the request
// goes to, which the server does not run, and in words what continuing it
// does; nothing kept.
func continuationNeeded(w http.ResponseWriter, run, words string) {
	writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: "continuation_needed", Message: words}, "run": run, "words": words})
}

// viaRefused refuses a review or a mark that says it is Brakeman's: the
// server records via brakeman only for Brakeman's own principal (BK2),
// and takes it from no body, the admin's included.
func viaRefused(w http.ResponseWriter, via string) bool {
	if via != dispatch.ViaBrakeman {
		return false
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "via brakeman is recorded by the server for Brakeman's own principal, never taken from a body")
	return true
}

// handleDispatchSubmitReview is POST /api/dispatch/tickets/{key}/reviews:
// a review of one candidate, in the order this file's comment gives;
// 201 {receipt, review} (200 for the same opId again), review.application
// saying none, pending, applied or refused.
func (s *Server) handleDispatchSubmitReview(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	var body reviewSubmitBody
	if !readOp(w, r, &body, &body.dispatchOp, maxReviewBody) {
		return
	}
	if viaRefused(w, body.Via) {
		return
	}
	if body.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "expectedRevision is a revision of the run")
		return
	}
	// A review runs to its end when the client goes away: its store
	// operations and its application, each within its own bound.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ReviewTimeout)
	defer cancel()
	s.submitReview(ctx, w, ds, body.op(r), key, body)
}

// answerReview answers an operation on a review that is done: {receipt,
// review}, the review as it stands, 201 for a review made now.
func (s *Server) answerReview(w http.ResponseWriter, ctx context.Context, ds *dispatch.Store, rc dispatch.Receipt, id string, create bool) {
	s.writeOp(w, "review", rc, nil, create, "review", func(dispatch.Receipt) (any, error) {
		v, err := ds.Review(ctx, id)
		if err != nil {
			return nil, err
		}
		return v, nil
	})
}

// submitReview is the review's order (this file's comment).
func (s *Server) submitReview(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, op dispatch.Op, key string, body reviewSubmitBody) {
	rv := body.NewReview
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentReviewSubmit, dispatch.ReviewSubmitArgs{Key: key, Review: rv})
	switch {
	case err != nil:
		s.dispatchError(w, "review", err)
		return
	case found:
		s.answerReview(w, ctx, ds, rc, rc.ID, true)
		return
	}
	request := rv.Verdict == dispatch.VerdictRequestChanges
	if request {
		if aerr := s.launchAdmission(); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
	}
	c, err := dispatch.ParseCandidate(rv.Candidate)
	if err != nil || !slices.Contains(dispatch.ReviewVerdicts, rv.Verdict) {
		// Not a review's shape: the store refuses it, its receipt kept,
		// before it looks for any evidence.
		rc, v, err := ds.SubmitReview(ctx, op, key, rv, dispatch.ReviewEvidence{})
		s.writeOp(w, "review", rc, err, true, "review", func(dispatch.Receipt) (any, error) { return v, nil })
		return
	}
	if s.ledger == nil {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened, so the candidate cannot be read; see the server's log")
		return
	}
	if !s.ticketHoldsRun(w, ctx, ds, key, c.Run) {
		return
	}
	row, a, aerr, err := s.candidateOfLedger(ctx, c)
	if err != nil {
		s.receiptError(w, "review: the candidate", c.Run, err)
		return
	}
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	switch ok, err := s.allowedBase(ctx, row, a, rv.Base); {
	case err != nil:
		s.chainReadError(w, c.Run, err)
		return
	case !ok:
		writeError(w, http.StatusConflict, dispatch.RefusalInvalid, "the base "+shortCommit(rv.Base)+" is none the run names: its base, the attempt's start, or a result of the task in the run's chain")
		return
	}
	ev, aerr, err := s.reviewEvidence(ctx, c, rv)
	switch {
	case err != nil && errors.Is(err, errRunContinuing):
		s.chainReadError(w, c.Run, err)
		return
	case err != nil:
		s.receiptError(w, "review: a check finding's receipt", c.Run, err)
		return
	case aerr != nil:
		writeAPIError(w, aerr)
		return
	}
	if request {
		cr := changeRequestOf(rv, ev, strings.Repeat("0", dispatch.IDLen), cmp.Or(op.By, op.Actor), nil)
		if aerr := fitRequest(row, c.Task, cr); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		newest, live, err := s.newestOf(ctx, c.Run)
		if err != nil {
			s.chainReadError(w, c.Run, err)
			return
		}
		if !live && !body.ContinueRun {
			continuationNeeded(w, newest, s.continuationWords(ctx, newest, c))
			return
		}
	}
	rc, v, err := ds.SubmitReview(ctx, op, key, rv, ev)
	if err != nil || rc.Status != dispatch.StatusDone {
		s.writeOp(w, "review", rc, err, true, "review", func(dispatch.Receipt) (any, error) { return v, nil })
		return
	}
	if request && !rc.Repeat && v.Application.State == dispatch.ApplicationPending {
		v = s.applyReview(ctx, ds, v, v.Application.Try, body.ContinueRun, body.ExpectedRevision)
	}
	s.writeOp(w, "review", rc, nil, true, "review", func(dispatch.Receipt) (any, error) { return v, nil })
}

// ticketHoldsRun answers 409 not_ticket_run unless the ticket with the
// key holds the run's chain alone (DS2's ChainHolders: none, another
// ticket, or contested), 404 for no live ticket, having answered when it
// reports false.
func (s *Server) ticketHoldsRun(w http.ResponseWriter, ctx context.Context, ds *dispatch.Store, key, run string) bool {
	err := ds.CandidateRefusal(ctx, key, run)
	var refusal *dispatch.Refusal
	switch {
	case err == nil:
		return true
	case errors.As(err, &refusal):
		words := refusal.Words
		switch {
		case strings.Contains(words, "contested"):
		case refusal.Key != "":
			words = fmt.Sprintf("the run %s is among %s's runs: review its candidates there", run, refusal.Key)
		}
		reply := map[string]any{"error": apiError{Code: "not_ticket_run", Message: words}}
		if refusal.Key != "" {
			reply["key"] = refusal.Key
		}
		writeDispatch(w, http.StatusConflict, reply)
	default:
		s.dispatchError(w, "ticket", err)
	}
	return false
}

// candidateOfLedger reads the candidate from the ledger: the run's row and
// the attempt, whose result is the candidate's commit; 409 invalid for a
// run the ledger has not, an attempt it has not, or another result.
func (s *Server) candidateOfLedger(ctx context.Context, c dispatch.Candidate) (ledger.RunRow, ledger.AttemptRow, *apiError, error) {
	row, found, err := s.ledger.Run(ctx, c.Run)
	if err != nil {
		return row, ledger.AttemptRow{}, nil, err
	}
	if !found {
		return row, ledger.AttemptRow{}, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "the ledger has no managed run "+c.Run), nil
	}
	attempts, err := s.ledger.Attempts(ctx, c.Run)
	if err != nil {
		return row, ledger.AttemptRow{}, nil, err
	}
	i := slices.IndexFunc(attempts, func(a ledger.AttemptRow) bool { return a.TaskID == c.Task && a.N == c.Attempt })
	if i < 0 {
		return row, ledger.AttemptRow{}, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("the run %s has no attempt %d of a task %s", c.Run, c.Attempt, c.Task)), nil
	}
	a := attempts[i]
	if a.ResultCommit != c.Commit {
		result := "no result"
		if a.ResultCommit != "" {
			result = "the result " + shortCommit(a.ResultCommit)
		}
		return row, a, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("%s's commit is not %s: the attempt has %s", c.Name(), shortCommit(c.Commit), result)), nil
	}
	return row, a, nil, nil
}

// receiptOfID parses a check finding's receipt identity,
// <run>/<task>/<n>/<phase>/<id>/<k>; ok false for one that is not (the
// store refuses it, its receipt kept).
func receiptOfID(s string) (run, task string, n int, phase, id string, k int, ok bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 6 {
		return "", "", 0, "", "", 0, false
	}
	n, okN := pathNumber(parts[2])
	k, err := strconv.Atoi(parts[5])
	if !okN || err != nil || k < 0 || strconv.Itoa(k) != parts[5] {
		return "", "", 0, "", "", 0, false
	}
	return parts[0], parts[1], n, parts[3], parts[4], k, crew.ValidRunID(parts[0]) && crew.ValidTaskID(parts[1]) && crew.ValidTaskID(parts[4])
}

// reviewEvidence binds each finding of rv to the candidate c, by the
// server: a line or a file finding's anchor the candidate's commit for
// its side candidate, the review's base for its side base (never taken
// from the body); a check finding's receipt read from the ledger
// (failedReceipt), refused 409 invalid when it is not of the candidate's
// attempt, when the ledger has no such receipt, or when it did not fail.
// A finding whose shape is not one is given no evidence: the store
// refuses its shape first, its receipt kept.
func (s *Server) reviewEvidence(ctx context.Context, c dispatch.Candidate, rv dispatch.NewReview) (dispatch.ReviewEvidence, *apiError, error) {
	ev := dispatch.ReviewEvidence{Findings: make([]dispatch.FindingEvidence, len(rv.Findings))}
	for i, f := range rv.Findings {
		k := i + 1
		e := dispatch.FindingEvidence{K: k}
		switch f.Kind {
		case dispatch.FindingKindLine, dispatch.FindingKindFile:
			e.AnchorCommit = c.Commit
			if f.Side == dispatch.SideBase {
				e.AnchorCommit = rv.Base
			}
		case dispatch.FindingKindCheck:
			if f.Check == nil || f.Check.Source != dispatch.CheckSourceLedger {
				break
			}
			run, task, n, phase, id, rk, ok := receiptOfID(f.Check.Receipt)
			if !ok {
				break
			}
			if run != c.Run || task != c.Task || n != c.Attempt {
				return ev, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("finding %d: the receipt %s is of %s/%s attempt %d, not of the candidate reviewed (%s)",
					k, f.Check.Receipt, run, task, n, c.String())), nil
			}
			fr, aerr, err := s.failedReceipt(ctx, s.ledger, run, task, n, id, rk)
			if err != nil {
				return ev, nil, err
			}
			if aerr != nil {
				return ev, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("finding %d: the receipt %s: %s", k, f.Check.Receipt, aerr.Message)), nil
			}
			if fr.ref.Phase != phase {
				return ev, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("finding %d: the receipt %s is a %s step's", k, f.Check.Receipt, fr.ref.Phase)), nil
			}
			if fr.ref.Subject != c.Commit {
				return ev, newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("finding %d: the receipt %s tested %s, not the candidate reviewed (%s)",
					k, f.Check.Receipt, shortCommit(fr.ref.Subject), shortCommit(c.Commit))), nil
			}
			ref := fr.ref
			e.Check = &ref
		}
		ev.Findings[i] = e
	}
	return ev, nil, nil
}

// changeRequestOf is the request the review makes of the engine, its
// findings as the store keeps them (kept, after its commit) or as sent
// with their evidence (before it, its ids placeholders of an id's length,
// so the request is measured at the size it will have).
func changeRequestOf(rv dispatch.NewReview, ev dispatch.ReviewEvidence, id, by string, kept []dispatch.Finding) crew.ChangeRequest {
	cr := crew.ChangeRequest{Review: id, By: by, Via: rv.Via, Base: rv.Base, Summary: rv.Summary, Changes: []crew.RequestedChange{}}
	if kept != nil {
		for _, f := range kept {
			cr.Changes = append(cr.Changes, requestedChange(f.ID, f.Kind, f.Intent, f.Severity, f.Path, f.Side, f.From, f.To, f.Lines, f.Message, f.Resolution, f.Check))
		}
		return cr
	}
	for i, f := range rv.Findings {
		var ref *dispatch.CheckRef
		if i < len(ev.Findings) && ev.Findings[i].Check != nil && f.Check != nil {
			c := *ev.Findings[i].Check
			c.Chosen, c.Quote = f.Check.Chosen, f.Check.Quote
			ref = &c
		}
		cr.Changes = append(cr.Changes, requestedChange(fmt.Sprintf("%0*x", dispatch.IDLen, i+1), f.Kind, f.Intent, f.Severity, f.Path, f.Side, f.From, f.To, f.Lines,
			f.Message, f.Resolution, ref))
	}
	return cr
}

// requestedChange is one finding as the engine's request carries it.
func requestedChange(id, kind, intent, severity, path, side string, from, to int, lines []string, message, resolution string, ref *dispatch.CheckRef) crew.RequestedChange {
	ch := crew.RequestedChange{ID: id, Kind: kind, Intent: intent, Severity: severity, Path: path, Side: side, From: from, To: to,
		Lines: slices.Clone(lines), Message: message, Resolution: resolution}
	if len(ch.Lines) == 0 {
		ch.Lines = nil
	}
	if ref != nil {
		ch.Check = &crew.RequestedCheck{Receipt: ref.Receipt, Run: ref.Run, Task: ref.Task, Attempt: ref.Attempt, Phase: ref.Phase, ID: ref.ID, K: ref.K,
			Subject: ref.Subject, Exit: ref.Exit, Signal: ref.Signal, TimedOut: ref.TimedOut, Stable: ref.Stable, Error: ref.Error, LogSHA256: ref.LogSHA256,
			LogBytes: ref.LogBytes, Tail: ref.Tail, Class: ref.Class, Chosen: ref.Chosen, Quote: ref.Quote}
	}
	return ch
}

// reviewRequest is the request a kept review makes of the engine.
func reviewRequest(v dispatch.Review) crew.ChangeRequest {
	rv := dispatch.NewReview{Candidate: v.Candidate, Base: v.Base, Verdict: v.Verdict, Summary: v.Summary, Via: v.Via}
	kept := v.Findings
	if kept == nil {
		kept = []dispatch.Finding{}
	}
	return changeRequestOf(rv, dispatch.ReviewEvidence{}, v.ID, v.CreatedBy, kept)
}

// fitRequest measures the request with the run's crew as launched and its
// launch package, both read from the run's row
// (crew.FitChangeRequest): 413 too_large past the ledger's payload or the
// repair attempt's task file, 409 invalid for a request the engine would
// not take as one, both before anything is kept.
func fitRequest(row ledger.RunRow, task string, cr crew.ChangeRequest) *apiError {
	var c crew.Crew
	if err := json.Unmarshal([]byte(row.Crew), &c); err != nil {
		return newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "the run's crew as launched cannot be read")
	}
	var pkg *crew.Package
	if row.Package != "" {
		var p crew.Package
		if json.Unmarshal([]byte(row.Package), &p) == nil {
			pkg = &p
		}
	}
	switch err := crew.FitChangeRequest(c, pkg, task, cr); {
	case err == nil:
		return nil
	case errors.Is(err, crew.ErrChangeRequestTooLarge):
		return newAPIError(http.StatusRequestEntityTooLarge, "too_large", strings.TrimPrefix(err.Error(), crew.ErrChangeRequestTooLarge.Error()+": "))
	case errors.Is(err, crew.ErrTaskNotFound):
		return newAPIError(http.StatusConflict, dispatch.RefusalInvalid, "the run's crew has no task "+task)
	default:
		return newAPIError(http.StatusConflict, dispatch.RefusalInvalid, strings.TrimPrefix(err.Error(), crew.ErrBadChangeRequest.Error()+": "))
	}
}

// newestOf is the newest run of run's chain (walking continued_by from
// it), where its candidate stands now, and whether the engine runs it:
// holds it, neither stopped nor continued.
func (s *Server) newestOf(ctx context.Context, run string) (string, bool, error) {
	nodes, _, _, err := s.runChain(ctx, run)
	if err != nil {
		return "", false, err
	}
	forward := chainForward(nodes, run)
	newest := forward[len(forward)-1]
	r, held := s.runs.Get(newest)
	return newest, held && r.StoppedAt == nil && r.ResumedBy == "", nil
}

// continuationWords says what continuing the run newest with the repair of
// the candidate c starts: "Yardmaster continues run R as a new run: its
// prerequisites run again, `build`'s repair attempt 3 starts at a1b2c3d,
// `test` and `docs`, unfinished, start when ready, and the review task
// `check` runs again on the repair's result".
func (s *Server) continuationWords(ctx context.Context, newest string, c dispatch.Candidate) string {
	head := "Yardmaster continues run " + newest + " as a new run"
	row, found, err := s.ledger.Run(ctx, newest)
	if err != nil || !found {
		return head + ", with the repair of " + c.Name()
	}
	var cw crew.Crew
	_ = json.Unmarshal([]byte(row.Crew), &cw)
	tasks, _ := s.ledger.Tasks(ctx, newest)
	state := map[string]ledger.TaskRow{}
	for _, t := range tasks {
		state[t.ID] = t
	}
	// The tasks that need the repaired one, transitively, import nothing.
	needs := map[string]bool{c.Task: true}
	for grew := true; grew; {
		grew = false
		for _, t := range cw.Tasks {
			if needs[t.ID] {
				continue
			}
			if slices.ContainsFunc(t.Needs, func(n string) bool { return needs[n] }) {
				needs[t.ID], grew = true, true
			}
		}
	}
	var parts, unfinished, again []string
	if cw.Manifest != nil && len(cw.Manifest.Prerequisites) > 0 {
		parts = append(parts, "its prerequisites run again")
	}
	parts = append(parts, fmt.Sprintf("`%s`'s repair attempt %d starts at %s", c.Task, max(state[c.Task].Attempts, c.Attempt)+1, shortCommit(c.Commit)))
	for _, t := range cw.Tasks {
		switch {
		case t.ID == c.Task:
		case needs[t.ID] && t.Kind == crew.TaskReview:
			again = append(again, "the review task `"+t.ID+"`")
		case needs[t.ID]:
			again = append(again, "`"+t.ID+"`")
		case state[t.ID].State != ledger.TaskSucceeded:
			unfinished = append(unfinished, "`"+t.ID+"`")
		}
	}
	if len(unfinished) > 0 {
		parts = append(parts, andList(unfinished)+", unfinished, start when ready")
	}
	if len(again) > 0 {
		verb := "runs"
		if len(again) > 1 {
			verb = "run"
		}
		parts = append(parts, andList(again)+" "+verb+" again on the repair's result")
	}
	return head + ": " + andList(parts)
}

// andList is the items in words: "a", "a and b", "a, b and c".
func andList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// applyReview applies the pending try of the review v (step 3 of the
// order): one application at a time per review in this process, the
// review read again inside the hold (a second request finds it settled),
// then the command sent and its outcome recorded (applyTry,
// recordOutcome). It answers the review as it stands after.
func (s *Server) applyReview(ctx context.Context, ds *dispatch.Store, v dispatch.Review, try int, continueRun bool, expected int64) dispatch.Review {
	release, ok := s.reviews.hold(ctx, v.ID)
	if !ok {
		// Another request applies it still: the review as it stands.
		if now, err := ds.Review(ctx, v.ID); err == nil {
			return now
		}
		return v
	}
	defer release()
	e := reviewSettle{review: v.ID, try: try, command: reviewCommandID(v.ID, try), actor: v.Actor}
	if c, err := dispatch.ParseCandidate(v.Candidate); err == nil {
		e.anchor = c.Run
	}
	now, err := ds.Review(ctx, v.ID)
	if err != nil {
		// Nothing sent: the try is refused, the outcome kept by the
		// worker until it is recorded.
		s.log.Warn("a review could not be read for its application", "review", v.ID, "err", err)
		s.keepOutcome(e, applyOutcome{why: wordsUnread})
		return v
	}
	if now.Application.State != dispatch.ApplicationPending || now.Application.Try != try {
		return now
	}
	s.reviews.seam(&s.reviews.beforeApply, v.ID)
	out := s.applyTry(ctx, now, e, continueRun, expected)
	if !out.pending && !s.recordOutcome(ctx, v.ID, try, out) {
		s.keepOutcome(e, out)
	}
	after, err := ds.Review(ctx, v.ID)
	if err != nil {
		return now
	}
	return after
}

// keepOutcome leaves the try's outcome, which Dispatch could not record,
// with the worker: registered on the run that holds its command (out's
// watch, else the candidate's run) and looked at after the retry's wait,
// so it is written although nothing commits the run.
func (s *Server) keepOutcome(e reviewSettle, out applyOutcome) {
	run := cmp.Or(out.watch, e.anchor, e.review)
	e.out = &out
	s.reviews.register(run, e)
	s.reviews.retryLater(run)
}

// applyTry sends the try's command to the newest run of the candidate's
// chain: Engine.RequestChanges when the engine runs it; otherwise, and
// only on the person's word (continueRun), the continuation that carries
// the repair (continueWith); without it, refused in the words a
// continuation_needed answer has. An answer that is not the command's
// receipt is read back from the ledger by the command's id (readBack).
func (s *Server) applyTry(ctx context.Context, v dispatch.Review, e reviewSettle, continueRun bool, expected int64) applyOutcome {
	c, err := dispatch.ParseCandidate(v.Candidate)
	if err != nil {
		return applyOutcome{why: "the review's candidate cannot be read: " + err.Error()}
	}
	if s.ledger == nil {
		return applyOutcome{why: "the ledger could not be opened: send it again"}
	}
	newest, live, err := s.newestOf(ctx, c.Run)
	if err != nil {
		return applyOutcome{why: "the run's chain could not be read (" + err.Error() + "): send it again"}
	}
	cmd := crew.Command{ID: e.command, ExpectedRevision: expected}
	rq := crew.RepairRequest{Task: c.Task, Attempt: c.Attempt, Commit: c.Commit, Request: reviewRequest(v)}
	if newest != c.Run {
		rq.Imported = fmt.Sprintf("%s/%s/%d", c.Run, c.Task, c.Attempt)
	}
	if live {
		rc, err := s.runs.RequestChanges(ctx, newest, v.Actor, cmd, rq)
		switch {
		case err == nil && rc.Status == ledger.CommandDone:
			out := appliedFrom(newest, ledger.CommandRow{ID: cmd.ID, Receipt: mustJSON(rc)})
			out.watch = newest
			return out
		case rc.CommandID != "" && rc.Status == ledger.CommandFailed:
			return applyOutcome{why: cmp.Or(rc.Error, "Yardmaster refused the request"), watch: newest}
		}
		why := ""
		if err != nil {
			why = err.Error()
		}
		if errors.Is(err, crew.ErrRunNotFound) {
			// Let go between the look and the command.
			why = s.continuationWords(ctx, newest, c) + ": send it again with the word to continue"
		}
		return s.readBack(ctx, newest, e, why)
	}
	if !continueRun {
		return applyOutcome{why: s.continuationWords(ctx, newest, c) + ": send it again with the word to continue"}
	}
	why := ""
	if err := s.continueWith(ctx, newest, rq, v.Actor, cmd); err != nil {
		why = err.Error()
	}
	return s.readBack(ctx, newest, e, why)
}

// continueWith makes the continuation of the run that carries the repair
// rq under the actor's command, as the resume route continues a managed
// run: admitted as a continuation, its chain's walks held off until both
// runs name each other (beginContinuation), refused once the shutdown has
// begun its runs' stops (admitRun; one made past that is stopped), its
// record written and the source's marked.
func (s *Server) continueWith(ctx context.Context, newest string, rq crew.RepairRequest, actor string, cmd crew.Command) error {
	if aerr := s.continuationAdmission(); aerr != nil {
		return errors.New(aerr.Message)
	}
	if !s.launches.admitRun() {
		return errRunsClosed
	}
	_, held := s.runs.Get(newest)
	end := s.beginContinuation(newest)
	run, err := s.runs.ContinueRunWith(ctx, newest, "", crew.ContinueOptions{Repair: &rq, Actor: actor, Command: cmd})
	end()
	if s.launches.startDone() && run != nil {
		sctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		if serr := s.runs.Stop(sctx, run.ID); serr != nil && !errors.Is(serr, crew.ErrRunNotFound) {
			s.log.Warn("a continuation made as the server shut down could not be stopped", "run", run.ID, "err", serr)
		}
		cancel()
	}
	if err != nil {
		return err
	}
	s.recordContinuation(ctx, run.ID)
	if !held && s.records != nil {
		if _, ok, _ := s.records.Get(newest); ok {
			s.markRecord(newest, run.ID)
		}
	}
	s.log.Info("a review's repair continued its run", "run", newest, "continuation", run.ID, "command", cmd.ID)
	return nil
}

// commandAlong reads the command under the actor from the ledger, in the
// run and in the runs that continue it, in order: the run that holds it,
// and its row.
func (s *Server) commandAlong(ctx context.Context, run, actor, id string) (ledger.CommandRow, bool, error) {
	nodes, _, _, err := s.runChain(ctx, run)
	if err != nil {
		return ledger.CommandRow{}, false, err
	}
	for _, r := range chainForward(nodes, run) {
		row, found, err := s.ledger.Receipt(ctx, r, actor, id)
		if err != nil {
			return ledger.CommandRow{}, false, err
		}
		if found {
			row.RunID = cmp.Or(row.RunID, r)
			return row, true, nil
		}
	}
	return ledger.CommandRow{}, false, nil
}

// readBack reads the try's command back from the ledger once its answer
// was not its receipt (step 4): the review registered on the run it went
// to first, so a commit from then on marks the run for the worker, then
// judged by judgeCommand, in the run that holds it along the candidate's
// chain. Kept done: applied; failed: refused with its words; accepted
// while the engine holds that run unsettled: pending, left to the worker
// on that run (a refused change and its settling write wait in the run's
// memory for its next commit); accepted once it does not, or uncertain:
// refused; not taken: refused with why. A ledger that cannot be read
// leaves it pending, with the worker, which looks again.
func (s *Server) readBack(ctx context.Context, to string, e reviewSettle, why string) applyOutcome {
	s.reviews.register(to, e)
	s.reviews.seam(&s.reviews.afterRegister, e.review)
	out, at, err := s.judgeCommand(ctx, e, to, true, why)
	switch {
	case out.pending && err != nil:
		s.log.Warn("a review's command could not be read back; it stays pending, looked at again", "review", e.review, "command", e.command, "err", err)
		s.reviews.register(at, e)
		s.reviews.retryLater(at)
	case out.pending:
		// A commit after the read-back marks the run; this mark covers one
		// that landed before the registration moved.
		s.reviews.register(at, e)
		s.reviews.runChanged(at)
		s.log.Info("a review waits for its command's receipt to settle", "review", e.review, "run", at, "command", e.command)
	default:
		s.reviews.unregister(to, e)
		out.watch = at
	}
	return out
}

// handleDispatchRetryReview is POST /api/dispatch/reviews/{id}/retry:
// Request again, a refused request applied again as its next try; body
// {opId, byName?, expectedRev?, expectedRevision?, continueRun?}; 200
// {receipt, review}. The operation replayed first; then the gate (409
// managed_off, 503 ledger_unavailable), the candidate's run held by the
// review's ticket alone (409 not_ticket_run) and, for a run the server
// does not run sent without continueRun, 409 continuation_needed, each
// with the try unchanged and nothing applied; then the store's next try
// (review.retry: its refusals with their receipts) and its application,
// as a submit's.
func (s *Server) handleDispatchRetryReview(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	var body reviewRetryBody
	if !readOp(w, r, &body, &body.dispatchOp, maxReviewRetryBody) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if body.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "expectedRevision is a revision of the run")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ReviewTimeout)
	defer cancel()
	op := body.op(r)
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentReviewRetry, reviewIDArgs{ID: id})
	switch {
	case err != nil:
		s.dispatchError(w, "review", err)
		return
	case found:
		s.answerReview(w, ctx, ds, rc, id, false)
		return
	}
	if aerr := s.launchAdmission(); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	v, err := ds.Review(ctx, id)
	if err != nil {
		s.dispatchError(w, "review", err)
		return
	}
	if v.Verdict == dispatch.VerdictRequestChanges && v.Application.State == dispatch.ApplicationRefused {
		c, err := dispatch.ParseCandidate(v.Candidate)
		if err == nil {
			// The candidate's run is still the ticket's alone, as at the
			// review (the store holds the retry's transaction to it too).
			if !s.ticketHoldsRun(w, ctx, ds, v.TicketKey, c.Run) {
				return
			}
			newest, live, err := s.newestOf(ctx, c.Run)
			if err != nil {
				s.chainReadError(w, c.Run, err)
				return
			}
			if !live && !body.ContinueRun {
				continuationNeeded(w, newest, s.continuationWords(ctx, newest, c))
				return
			}
		}
	}
	rc, v, err = ds.ApplyingReview(ctx, op, id)
	if err != nil || rc.Status != dispatch.StatusDone {
		s.writeOp(w, "review", rc, err, false, "review", func(dispatch.Receipt) (any, error) { return v, nil })
		return
	}
	if !rc.Repeat && v.Application.State == dispatch.ApplicationPending {
		v = s.applyReview(ctx, ds, v, v.Application.Try, body.ContinueRun, body.ExpectedRevision)
	}
	s.writeOp(w, "review", rc, nil, false, "review", func(dispatch.Receipt) (any, error) { return v, nil })
}

// handleDispatchReviews is GET /api/dispatch/tickets/{key}/reviews
// [?before=CURSOR]: {reviews, next?}, a page of 20 newest first, each with
// its findings counted by state, under dispatch.MaxReviewsReply.
func (s *Server) handleDispatchReviews(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	q, err := readQuery(r, "before")
	var before string
	if err == nil {
		before, err = q.one("before")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := ds.Reviews(r.Context(), key, before)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	writeDispatch(w, http.StatusOK, page)
}

// handleDispatchReview is GET /api/dispatch/reviews/{id}: {review}, the
// batch whole, each finding with its marks counted and its latest mark,
// under dispatch.MaxReviewReply.
func (s *Server) handleDispatchReview(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	v, err := ds.Review(r.Context(), id)
	if err != nil {
		s.dispatchError(w, "review", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"review": v})
}

// handleDispatchMarks is GET /api/dispatch/findings/{id}/marks
// [?after=CURSOR]: {marks, next?}, the finding's marks oldest first, a
// page under dispatch.MaxMarksReply.
func (s *Server) handleDispatchMarks(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q, err := readQuery(r, "after")
	var after string
	if err == nil {
		after, err = q.one("after")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := ds.Marks(r.Context(), id, after)
	if err != nil {
		s.dispatchError(w, "finding", err)
		return
	}
	writeDispatch(w, http.StatusOK, page)
}

// addressedNote is what a repair attempt's result said of a finding: the
// run and the attempt, and its note (its claim, never the finding's
// resolution).
type addressedNote struct {
	Run     string `json:"run"`
	Attempt int    `json:"attempt"`
	Note    string `json:"note,omitempty"`
}

// findingView is a finding as its ticket's page lists it: the store's,
// with where its lines are on the candidate it was located against
// (locatedOn) and what the last repair attempts that named it said.
type findingView struct {
	dispatch.Finding
	LocatedOn string          `json:"locatedOn,omitempty"`
	Located   *locatedState   `json:"located,omitempty"`
	Addressed []addressedNote `json:"addressed,omitempty"`
}

// handleDispatchFindings is GET /api/dispatch/tickets/{key}/findings
// [?state=…&candidate=C&after=CURSOR]: {findings, next?}, a page of at
// most 100 and fewer at dispatch.MaxFindingsReply, each finding with its
// review's candidate, its state and last mark, the addressed notes of the
// last three repair attempts that named it, and where its lines are on
// candidate (only findings of its task then), else on the newest result of
// its task in its chain.
func (s *Server) handleDispatchFindings(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	q, err := readQuery(r, "state", "candidate", "after")
	var state, cand, after string
	if err == nil {
		state, err = q.one("state")
	}
	if err == nil {
		cand, err = q.one("candidate")
	}
	if err == nil {
		after, err = q.one("after")
	}
	if err == nil && state == "all" {
		state = ""
	}
	var on dispatch.Candidate
	if err == nil && cand != "" {
		on, err = dispatch.ParseCandidate(cand)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx := r.Context()
	page, err := ds.Findings(ctx, key, dispatch.FindingFilter{State: state, Task: on.Task}, after)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	out := struct {
		Findings []findingView `json:"findings"`
		Next     string        `json:"next,omitempty"`
	}{Findings: make([]findingView, 0, len(page.Findings)), Next: page.Next}
	l := s.newLocator()
	type runTask struct{ run, task string }
	newest := map[runTask]*dispatch.Candidate{}
	addressed := map[runTask]map[string][]addressedNote{}
	for _, f := range page.Findings {
		fv := findingView{Finding: f}
		if s.ledger != nil {
			if own, err := dispatch.ParseCandidate(f.Candidate); err == nil {
				k := runTask{own.Run, own.Task}
				target := on
				if cand == "" {
					if _, ok := newest[k]; !ok {
						newest[k] = nil
						if nodes, _, _, err := s.runChain(ctx, own.Run); err == nil {
							if c, ok, err := s.newestResult(ctx, nodes, own.Run, own.Task); err == nil && ok {
								newest[k] = &c
							}
						}
					}
					if c := newest[k]; c != nil {
						target = *c
					}
				}
				if target.Commit != "" {
					fv.LocatedOn = target.String()
					fv.Located = l.locate(ctx, f, target)
				}
				if _, ok := addressed[k]; !ok {
					addressed[k] = s.addressedOf(ctx, own.Run, own.Task)
				}
				fv.Addressed = addressed[k][f.ID]
			}
		}
		out.Findings = append(out.Findings, fv)
	}
	writeDispatch(w, http.StatusOK, out)
}

// addressedOf is, by finding id, what the attempts of the task in the run's
// chain said they addressed (their result's addressed), newest first, the
// last maxAddressedAttempts per finding; none when the ledger cannot be
// read.
func (s *Server) addressedOf(ctx context.Context, run, task string) map[string][]addressedNote {
	out := map[string][]addressedNote{}
	nodes, _, _, err := s.runChain(ctx, run)
	if err != nil {
		return out
	}
	var rows []ledger.AttemptRow
	for _, id := range chainForward(nodes, run) {
		attempts, err := s.ledger.Attempts(ctx, id)
		if err != nil {
			return out
		}
		for _, a := range attempts {
			if a.TaskID == task && a.Result != "" {
				rows = append(rows, a)
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b ledger.AttemptRow) int { return b.N - a.N })
	for _, a := range rows {
		var res struct {
			Addressed []crew.Addressed `json:"addressed"`
		}
		if json.Unmarshal([]byte(a.Result), &res) != nil {
			continue
		}
		for _, ad := range res.Addressed {
			if len(out[ad.ID]) < maxAddressedAttempts {
				out[ad.ID] = append(out[ad.ID], addressedNote{Run: a.RunID, Attempt: a.N, Note: ad.Note})
			}
		}
	}
	return out
}

// handleDispatchMarkFinding is POST /api/dispatch/findings/{id}/marks:
// {opId, candidate, verdict, note?, via?, byName?} → 201 {receipt, mark}.
// The operation replayed first; then, for a new mark, the candidate
// checked: of a run whose chain the finding's ticket holds alone (409
// not_ticket_run), a later attempt's result of the finding's task in the
// chain of the finding's own candidate (409 invalid: another task, a run
// outside the chain, an attempt not after the finding's, a commit not the
// attempt's result), and where the finding's lines are on it located by
// the server and kept with the mark (the store refuses resolved on a
// candidate of the finding's own commit, its receipt kept).
func (s *Server) handleDispatchMarkFinding(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	var body findingMarkBody
	if !readOp(w, r, &body, &body.dispatchOp, maxMarkBody) {
		return
	}
	id, ok := pathID(w, r)
	if !ok || viaRefused(w, body.Via) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	op := body.op(r)
	args := dispatch.FindingMarkArgs{ID: id, Mark: body.NewMark}
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentFindingMark, args)
	switch {
	case err != nil:
		s.dispatchError(w, "finding", err)
		return
	case found:
		s.writeOp(w, "finding", rc, nil, true, "mark", s.markAfter(ctx, ds, id))
		return
	}
	f, key, err := ds.Finding(ctx, id)
	if err != nil {
		s.dispatchError(w, "finding", err)
		return
	}
	mc, err := dispatch.ParseCandidate(body.Candidate)
	if err != nil {
		// The store refuses it, its receipt kept.
		rc, _, err := ds.MarkFinding(ctx, op, id, body.NewMark, nil)
		s.writeOp(w, "finding", rc, err, true, "mark", s.markAfter(ctx, ds, id))
		return
	}
	if s.ledger == nil {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened, so the candidate cannot be read; see the server's log")
		return
	}
	if !s.ticketHoldsRun(w, ctx, ds, key, mc.Run) {
		return
	}
	own, err := dispatch.ParseCandidate(f.Candidate)
	if err != nil {
		s.dispatchError(w, "finding", err)
		return
	}
	if aerr, err := s.laterCandidate(ctx, own, mc); err != nil {
		s.chainReadError(w, mc.Run, err)
		return
	} else if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	located := locatedJSON(s.newLocator().locate(ctx, f, mc))
	rc, _, err = ds.MarkFinding(ctx, op, id, body.NewMark, located)
	s.writeOp(w, "finding", rc, err, true, "mark", s.markAfter(ctx, ds, id))
}

// markAfter reads the mark a done receipt names, among the finding's
// marks; nil once it is not to be found.
func (s *Server) markAfter(ctx context.Context, ds *dispatch.Store, finding string) func(dispatch.Receipt) (any, error) {
	return func(rc dispatch.Receipt) (any, error) {
		for after := ""; ; {
			page, err := ds.Marks(ctx, finding, after)
			if err != nil {
				return nil, err
			}
			for _, m := range page.Marks {
				if m.ID == rc.ID {
					return m, nil
				}
			}
			if page.Next == "" {
				return nil, nil
			}
			after = page.Next
		}
	}
}

// laterCandidate checks that mc is a later attempt's result of the
// finding's task than own, in own's chain: 409 invalid otherwise.
func (s *Server) laterCandidate(ctx context.Context, own, mc dispatch.Candidate) (*apiError, error) {
	if mc.Task != own.Task {
		return newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("the candidate is of the task %s; the finding is of %s's", mc.Task, own.Task)), nil
	}
	if mc.Attempt <= own.Attempt {
		return newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("%s is not after the finding's candidate (%s): mark it on a later attempt's result", mc.Name(), own.Name())), nil
	}
	nodes, _, _, err := s.runChain(ctx, own.Run)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(nodes, func(n chainNode) bool { return n.id == mc.Run }) {
		return newAPIError(http.StatusConflict, dispatch.RefusalInvalid, fmt.Sprintf("the run %s is not of the chain of the finding's run %s", mc.Run, own.Run)), nil
	}
	_, _, aerr, err := s.candidateOfLedger(ctx, mc)
	return aerr, err
}

// reconcileReviews settles, at the start, every review still pending whose
// candidate's run the ledger ld holds, by its command's row looked for in
// that run and along its continuations under the review's actor
// (reconcileCommand): done, applied; failed, refused with its words;
// accepted or uncertain, refused (act commits a command's change and its
// settled receipt together, and the process that held a retained change
// is gone); none, refused. A review whose run this ledger does not hold is
// left pending, for the ledger's owner. A read that fails settles nothing:
// the review stays pending, left to the worker, which reads it again; so
// is an outcome Dispatch could not record. Nothing is applied: a request
// reaches a run again only when a person sends it again, as a new try. It
// runs before launches and reviews are admitted (ledgerRecovered).
func (s *Server) reconcileReviews(ctx context.Context, ld *ledger.Ledger) {
	if ld == nil {
		return
	}
	s.reconcileWith(ctx, ld)
}

// reviewLedger is what the reconcile reads of a ledger: a run's row and a
// command's.
type reviewLedger interface {
	Run(ctx context.Context, id string) (ledger.RunRow, bool, error)
	Receipt(ctx context.Context, run, actor, id string) (ledger.CommandRow, bool, error)
}

// reconcileWith is reconcileReviews over the ledger ld.
func (s *Server) reconcileWith(ctx context.Context, ld reviewLedger) {
	if s.dispatch == nil {
		return
	}
	pending, err := s.dispatch.PendingReviews(ctx)
	if err != nil {
		s.log.Warn("the pending reviews could not be read for their reconcile", "err", err)
		return
	}
	for _, v := range pending {
		c, err := dispatch.ParseCandidate(v.Candidate)
		if err != nil {
			continue
		}
		e := reviewSettle{review: v.ID, try: v.Application.Try, command: reviewCommandID(v.ID, v.Application.Try), actor: v.Actor, anchor: c.Run, atStart: true}
		out, held, err := reconcileCommand(ctx, ld, c.Run, v.Actor, e.command)
		switch {
		case err != nil:
			s.log.Warn("a pending review stays pending: its run or its command could not be read; it is looked at again", "review", v.ID, "run", c.Run, "err", err)
			s.reviews.register(c.Run, e)
			s.reviews.retryLater(c.Run)
			continue
		case !held:
			continue
		}
		if !s.recordOutcome(ctx, v.ID, v.Application.Try, out) {
			s.keepOutcome(e, out)
			continue
		}
		s.log.Info("a pending review was settled at the start", "review", v.ID, "applied", out.applied != nil)
	}
}

// reconcileCommand is the start's outcome of the command under the actor:
// its row looked for in the run and along the runs that continue it, in
// ld (commandOutcome; accepted refused as uncertain is, wordsTakenStopped;
// none, wordsNotTaken); held false when ld holds not the run; the error of
// any read that failed, which settles nothing.
func reconcileCommand(ctx context.Context, ld reviewLedger, run, actor, cmd string) (applyOutcome, bool, error) {
	held := false
	for range maxReconcileChain {
		row, found, err := ld.Run(ctx, run)
		if err != nil {
			return applyOutcome{}, false, fmt.Errorf("the run %s: %w", run, err)
		}
		if !found {
			break
		}
		held = true
		rc, found, err := ld.Receipt(ctx, run, actor, cmd)
		if err != nil {
			return applyOutcome{}, false, fmt.Errorf("the command in run %s: %w", run, err)
		}
		if found {
			rc.RunID = cmp.Or(rc.RunID, run)
			if out, ok := commandOutcome(rc, true); ok {
				return out, true, nil
			}
			return applyOutcome{why: wordsTakenStopped}, true, nil
		}
		if row.ContinuedBy == "" {
			break
		}
		run = row.ContinuedBy
	}
	if !held {
		return applyOutcome{}, false, nil
	}
	return applyOutcome{why: wordsNotTaken}, true, nil
}

// maxReconcileChain bounds the runs the reconcile walks along a chain for
// one review's command.
const maxReconcileChain = 10000
