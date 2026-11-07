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
// accepted while the engine holds the run is left to the reviews' worker,
// which OnRunChange only wakes, outside the engine's lock. At the start
// the pending reviews are reconciled by what the ledger holds
// (reconcileReviews, the worker's pass run once before reviews are
// admitted), and nothing is applied then.
//
// The store is the worklist. The worker's pass (passReviews), on every
// wake and on a steady interval (reviewPassEvery), lists the reviews
// Dispatch holds pending and tries to settle each one: nothing kept in
// memory is the source of truth, so a review any path left pending (a
// read that failed after its commit, a read or a write past its
// deadline, a listing that failed) is taken up by the next pass whatever
// failed before. Who settles a try is the store's too: a review's try is
// settled only by whoever holds the store's claim on that exact try
// (dispatch.ClaimReview), taken at the moment of acting by a
// compare-and-set on the review's try and state. A submit and Request
// again take it in the commit that makes their try pending and keep it
// until their command's receipt is recorded (or left pending, for the
// worker); a pass takes it on each listed try before it reads, records or
// drops anything, and a try stale by then (settled, or a later one begun)
// or held by another is left, the actor stopping. What memory keeps is a
// hint of one try, written and dropped only under that try's claim, that
// hurries or informs a pass, never a condition of one: the run whose
// commit wakes the worker, an outcome known that Dispatch could not
// record, and that this process committed the try (ours). A pass judges
// one of ours by its command, in the run that holds it along the
// candidate's chain (judgeCommand): no request of this process sends it
// any more, so a command found nowhere is refused, and one accepted
// waits only while the engine holds its run unsettled. It judges any
// other, committed by a server before this one, as the start does
// (reconcileCommand). A read that fails settles nothing, and an outcome
// Dispatch could not record is written again: the review stays pending,
// for the next pass.

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
	// reviewSettleWait bounds a pass's listing, and its settlement of one
	// review: its command's reads and its outcome's write.
	reviewSettleWait = 30 * time.Second
	// reviewPassEvery is the worker's steady interval: a pass at least
	// this often, whatever wakes it or does not.
	reviewPassEvery = 5 * time.Second
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

// reviewGate holds what this process knows of the reviews beyond the
// store: the hints, by review (hints), which hurry or inform a pass and
// are never its worklist nor its permission (a review's try is settled
// only under the store's claim on it, dispatch.ClaimReview); and the
// worker's wake, a channel of one that a send never blocks on. Its zero
// value is ready once the worker's channels are made (startReviews).
type reviewGate struct {
	mu    sync.Mutex
	hints map[string]reviewHint
	wake  chan struct{}
	stop  context.CancelFunc
	done  chan struct{}
	once  sync.Once
	// The tests' seams, under mu: afterRegister with a review's id once
	// its read-back has noted the run it went to, before its command is
	// read back; afterReadBack once the read-back's first read has
	// answered, before the engine's hold is read and the answer taken;
	// beforeSettleRead with the settlement's context and the candidate's
	// run before a pass reads a review's command; beforeApply with a
	// review's id before its command is sent; recordFails with a review's
	// id before its outcome is written, the write failing when it answers
	// an error; listFails before a pass lists the pending reviews, the
	// listing failing when it answers an error; afterListing once a pass
	// has listed them, before it claims the first; retryWaits with a
	// review's id when Request again finds the review claimed and waits
	// for it; workerSkips with a review's id, the worker's passes (not the
	// start's) leaving it alone when it answers true, as a server before
	// this one would have; ticks, when set, the worker's interval in place
	// of reviewPassEvery's ticker, a test's clock; settleWait, when set, a
	// settlement's budget in place of reviewSettleWait.
	afterRegister    func(review string)
	afterReadBack    func(review string)
	beforeSettleRead func(ctx context.Context, run string)
	beforeApply      func(review string)
	recordFails      func(review string) error
	listFails        func() error
	afterListing     func()
	retryWaits       func(review string)
	workerSkips      func(review string) bool
	ticks            chan time.Time
	settleWait       time.Duration
}

// reviewHint is what this process knows of one try of a pending review
// beyond the store, none of it the condition of a pass: try, the try it
// concerns; ours, that try committed here (judged by its command,
// judgeCommand, not as the start judges one); watch, the run whose commit
// wakes the worker for it; out, its outcome, known, that Dispatch could
// not record. Only the holder of the store's claim on that try writes it
// or drops it, so a hint is never another try's.
type reviewHint struct {
	try   int
	ours  bool
	watch string
	out   *applyOutcome
}

// reviewSettle is a try a pass or a read-back settles: the review, its
// try, its command, the actor the command is kept under and the
// candidate's run (anchor), from which the command is looked for along
// the chain.
type reviewSettle struct {
	review  string
	try     int
	command string
	actor   string
	anchor  string
}

// settleOf is the try a pending review stands at; ok false for a
// candidate that cannot be read.
func settleOf(v dispatch.ReviewSummary) (reviewSettle, bool) {
	c, err := dispatch.ParseCandidate(v.Candidate)
	if err != nil {
		return reviewSettle{}, false
	}
	try := v.Application.Try
	return reviewSettle{review: v.ID, try: try, command: reviewCommandID(v.ID, try), actor: v.Actor, anchor: c.Run}, true
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

// touch changes the hint of the review's try, the caller holding the
// store's claim on that try: a hint of another try is replaced.
func (g *reviewGate) touch(id string, try int, f func(h *reviewHint)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hints == nil {
		g.hints = map[string]reviewHint{}
	}
	h := g.hints[id]
	if h.try != try {
		h = reviewHint{try: try}
	}
	f(&h)
	g.hints[id] = h
}

// ours notes the try, which this process committed, as ours: judged by
// its command (judgeCommand).
func (g *reviewGate) ours(id string, try int) {
	g.touch(id, try, func(h *reviewHint) { h.ours = true })
}

// watch notes the run whose next commit wakes the worker for the review's
// try: the run that holds its command, or the one it went to.
func (g *reviewGate) watch(id string, try int, run string) {
	g.touch(id, try, func(h *reviewHint) { h.watch = run })
}

// keep notes the try's outcome, known, that Dispatch could not record:
// the next pass writes it.
func (g *reviewGate) keep(id string, try int, out applyOutcome) {
	g.touch(id, try, func(h *reviewHint) { h.out = &out })
}

// hintOf is the hint of the review's try; none for another try's.
func (g *reviewGate) hintOf(id string, try int) reviewHint {
	g.mu.Lock()
	defer g.mu.Unlock()
	if h, ok := g.hints[id]; ok && h.try == try {
		return h
	}
	return reviewHint{}
}

// settled drops the hint of the review's try once its outcome is
// recorded, the caller holding the store's claim on that try: a hint of
// another try is left as it is.
func (g *reviewGate) settled(id string, try int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if h, ok := g.hints[id]; ok && h.try == try {
		delete(g.hints, id)
	}
}

// How a pass settles a pending review's try (settleOne).
const (
	settleAsOurs  = iota // judged by its command (judgeCommand)
	settleAsStart        // judged as the start judges one (reconcileCommand)
	settleKnown          // its outcome known, written again
)

// settleHow is how a pass settles the try, by its hint.
func settleHow(h reviewHint) int {
	switch {
	case h.out != nil:
		return settleKnown
	case h.ours:
		return settleAsOurs
	}
	return settleAsStart
}

// releaseReview ends a request's claim on a review's try and wakes the
// worker, which settles what the request left pending.
func (s *Server) releaseReview(c dispatch.ReviewClaim) {
	if !c.Held() || s.dispatch == nil {
		return
	}
	s.dispatch.ReleaseReview(c)
	s.reviews.poke()
}

// runChanged notes that a commit of the run landed: the engine's
// OnRunChange, under the engine's lock, so it takes the gate's own small
// lock to read the hints and wakes the worker by a send that never blocks
// (a wake already queued covers it). A run no review is watched on wakes
// nothing: the steady pass covers what a hint misses.
func (g *reviewGate) runChanged(run string) {
	g.mu.Lock()
	watched := false
	for _, h := range g.hints {
		if h.watch == run {
			watched = true
			break
		}
	}
	g.mu.Unlock()
	if watched {
		g.poke()
	}
}

// poke wakes the worker for a pass, by a send that never blocks.
func (g *reviewGate) poke() {
	g.mu.Lock()
	wake := g.wake
	g.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// startReviews starts the worker that settles the pending reviews, on a
// context of its own, ended by stopReviews before the stores close: a
// pass on every wake and every reviewPassEvery, once the ledger is this
// server's and taken over (ready).
func (s *Server) startReviews() {
	g := &s.reviews
	ctx, cancel := context.WithCancel(context.Background())
	g.mu.Lock()
	g.wake = make(chan struct{}, 1)
	g.stop = cancel
	g.done = make(chan struct{})
	wake, done := g.wake, g.done
	// A first pass as soon as the ledger is taken over: what the start's
	// pass left, if it ran before the worker (ledgerRecovered in New).
	wake <- struct{}{}
	g.mu.Unlock()
	go func() {
		defer close(done)
		tick := time.NewTicker(reviewPassEvery)
		defer tick.Stop()
		for {
			g.mu.Lock()
			ticks := (<-chan time.Time)(tick.C)
			if g.ticks != nil {
				ticks = g.ticks
			}
			g.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-ticks:
			}
			if !s.launches.ready.Load() || s.ledger == nil {
				continue
			}
			_ = s.passReviews(ctx, s.ledger, true)
		}
	}()
}

// stopReviews ends the worker and waits for it, once; nothing more is
// settled in this process (the next start's pass settles what is still
// pending).
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

// passReviews is one pass over the worklist: the reviews Dispatch holds
// pending, listed within reviewSettleWait, each settled within a
// reviewSettleWait of its own (settleOne), ld the ledger a review from
// before this server is judged in. A listing that fails is the error,
// and the next pass lists again; a review not settled stays pending, for
// the next pass. worker is the worker's pass, the start's otherwise.
func (s *Server) passReviews(ctx context.Context, ld reviewLedger, worker bool) error {
	if s.dispatch == nil {
		return nil
	}
	g := &s.reviews
	lctx, cancel := context.WithTimeout(ctx, reviewSettleWait)
	g.mu.Lock()
	fails, listed := g.listFails, g.afterListing
	g.mu.Unlock()
	var pending []dispatch.ReviewSummary
	var err error
	if fails != nil {
		err = fails()
	}
	if err == nil {
		pending, err = s.dispatch.PendingReviews(lctx)
	}
	cancel()
	if err != nil {
		s.log.Warn("the pending reviews could not be listed; the next pass lists them again", "err", err)
		return err
	}
	if listed != nil {
		listed()
	}
	for _, v := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e, ok := settleOf(v); ok {
			s.settleOne(ctx, ld, e, worker)
		}
	}
	return nil
}

// settleOne settles the pending try e within reviewSettleWait, under the
// store's claim on that exact try (dispatch.ClaimReview), taken first:
// a try stale by then (settled, or a later one begun since the listing)
// or claimed by another (a request sending it, another pass) is left, and
// the pass goes on. Under the claim the review is read again and judged
// as it stands then: an outcome known is written again; one of ours is
// judged by its command (judgeCommand) and one from before this server as
// the start judges it (reconcileCommand, in ld; a run ld does not hold
// leaves it, for the ledger's owner). Still pending, it is watched on the
// run that holds its command; a read that fails, or an outcome Dispatch
// could not record (kept), leaves it pending for the next pass. The claim
// is released when it ends, its hint dropped first once its outcome is
// recorded.
func (s *Server) settleOne(ctx context.Context, ld reviewLedger, e reviewSettle, worker bool) {
	g := &s.reviews
	g.mu.Lock()
	skips, seam, wait := g.workerSkips, g.beforeSettleRead, cmp.Or(g.settleWait, reviewSettleWait)
	g.mu.Unlock()
	if worker && skips != nil && skips(e.review) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	c, now, ok, err := s.dispatch.ClaimReview(ctx, e.review, e.try)
	if err != nil {
		s.log.Warn("a pending review could not be claimed; the next pass tries again", "review", e.review, "err", err)
		return
	}
	if !ok {
		return
	}
	defer s.dispatch.ReleaseReview(c)
	if e, ok = settleOf(now); !ok || e.try != c.Try {
		return
	}
	if seam != nil {
		seam(ctx, e.anchor)
	}
	h := g.hintOf(e.review, e.try)
	how := settleHow(h)
	var out applyOutcome
	switch how {
	case settleKnown:
		out = *h.out
	case settleAsOurs:
		o, at, err := s.judgeCommand(ctx, e, e.anchor, false, "")
		if o.pending {
			g.watch(e.review, e.try, at)
			if err != nil {
				s.log.Warn("a pending review's command could not be read; the next pass reads it again", "review", e.review, "run", at, "err", err)
			}
			return
		}
		out = o
	default:
		o, held, err := reconcileCommand(ctx, ld, e.anchor, e.actor, e.command)
		switch {
		case err != nil:
			s.log.Warn("a pending review's command could not be read; the next pass reads it again", "review", e.review, "run", e.anchor, "err", err)
			return
		case !held:
			// The ledger holds no run of it: left pending, for its owner.
			return
		}
		out = o
	}
	if !s.recordOutcome(ctx, c, out) {
		g.keep(e.review, e.try, out)
		return
	}
	g.settled(e.review, e.try)
	if how == settleAsStart {
		s.log.Info("a pending review from before this server was settled", "review", e.review, "applied", out.applied != nil)
	}
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

// judgeCommand judges the try e's command, sent to the run to (the
// candidate's run for a pass): read along the candidate's chain
// (commandAlong, from e.anchor) and settled by its row (commandOutcome).
// A command found nowhere is refused at once: no request sends it any
// more (the read-back's call answered, and a pass holds the store's claim
// on the try), in
// why for the read-back, wordsNotFound for a pass. When its row is still
// accepted, the engine's hold on the run that holds it is read and only
// then the command again, so a commit and a release between the two
// reads is seen by the second: still accepted while the engine holds
// that run unsettled, pending; once it does not, refused
// (wordsTakenLetGo), or holds it settled (wordsTakenDropped). It answers
// the outcome, the run that holds the command (to for one found
// nowhere), and the read's error, with the outcome pending, when a read
// failed.
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
	if !found {
		if answered {
			return applyOutcome{why: cmp.Or(why, "Yardmaster did not take the request")}, to, nil
		}
		return applyOutcome{why: wordsNotFound}, to, nil
	}
	at := row.RunID
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

// recordOutcome commits the outcome of the try the claim c holds in
// Dispatch (the actor server's, review.applied or review.refused, under
// the claim), reporting whether it is recorded: a failure is logged and
// the review stays pending, its outcome kept as a hint the next pass
// writes (keep).
func (s *Server) recordOutcome(ctx context.Context, c dispatch.ReviewClaim, out applyOutcome) bool {
	if s.dispatch == nil || out.pending {
		return false
	}
	id, try := c.Review, c.Try
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
		rc, err = s.dispatch.AppliedReviewClaimed(ctx, dispatch.Op{}, c, *out.applied)
	default:
		rc, err = s.dispatch.RefusedReviewClaimed(ctx, dispatch.Op{}, c, out.why)
	}
	if err != nil {
		s.log.Warn("a review's application could not be recorded; it stays pending", "review", id, "try", try, "err", err)
		return false
	}
	if rc.Status != dispatch.StatusDone {
		// Not kept: a store that writes nothing, or a claim not held (no
		// actor but the claim's holder settles a try).
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
	// A request comes with the store's claim on its try 1, taken in the
	// commit that makes it pending, so no pass or Request again settles it
	// before the request has sent its command and recorded its receipt;
	// noted as ours under the claim. The release wakes the worker, which
	// settles whatever the request leaves pending, its read failed after
	// the commit included.
	rc, v, claim, err := ds.SubmitReviewClaimed(ctx, op, key, rv, ev)
	if claim.Held() {
		s.reviews.ours(claim.Review, claim.Try)
		defer s.releaseReview(claim)
	}
	switch {
	case rc.Status == dispatch.StatusDone && err != nil:
		// Committed, its read failed: done all the same, answered by its
		// receipt; the review stays pending for the worker.
		s.writeOp(w, "review", rc, nil, true, "review", func(dispatch.Receipt) (any, error) { return nil, err })
		return
	case err != nil || rc.Status != dispatch.StatusDone:
		s.writeOp(w, "review", rc, err, true, "review", func(dispatch.Receipt) (any, error) { return v, nil })
		return
	}
	if claim.Held() {
		v = s.applyReview(ctx, ds, v, claim, body.ContinueRun, body.ExpectedRevision)
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
// `check` runs again on the repair's result". The repair's start is the
// engine's (repairStartOf): for a candidate refused before its checks,
// where its attempt started, said apart from the commit reviewed.
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
	repair := fmt.Sprintf("`%s`'s repair attempt %d", c.Task, max(state[c.Task].Attempts, c.Attempt)+1)
	switch start, ok := s.repairStartOf(ctx, c); {
	case !ok:
		parts = append(parts, repair+" starts")
	case start != c.Commit:
		parts = append(parts, fmt.Sprintf("%s starts at %s, where attempt %d started (its result %s, the commit reviewed, was refused before its checks)",
			repair, shortCommit(start), c.Attempt, shortCommit(c.Commit)))
	default:
		parts = append(parts, repair+" starts at "+shortCommit(c.Commit))
	}
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

// repairStartOf is where the engine starts the repair of the candidate c
// (crew.RepairStart): read from its attempt's row, a failed one's start
// when it was refused before its checks, else the candidate's commit; ok
// false when the row cannot be read.
func (s *Server) repairStartOf(ctx context.Context, c dispatch.Candidate) (string, bool) {
	attempts, err := s.ledger.Attempts(ctx, c.Run)
	if err != nil {
		return "", false
	}
	i := slices.IndexFunc(attempts, func(a ledger.AttemptRow) bool { return a.TaskID == c.Task && a.N == c.Attempt })
	if i < 0 || attempts[i].ResultCommit != c.Commit {
		return "", false
	}
	if attempts[i].State != ledger.AttemptFailed {
		return c.Commit, true
	}
	return crew.RepairStart(attempts[i]), true
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
// order) that the claim c holds, the caller holding it until its return:
// the review read again under the claim, then the command sent and its
// receipt recorded (applyTry, recordOutcome), the try's hint dropped once
// it is. It answers the review as it stands after.
func (s *Server) applyReview(ctx context.Context, ds *dispatch.Store, v dispatch.Review, c dispatch.ReviewClaim, continueRun bool, expected int64) dispatch.Review {
	e := reviewSettle{review: c.Review, try: c.Try, command: reviewCommandID(c.Review, c.Try), actor: v.Actor}
	if cand, err := dispatch.ParseCandidate(v.Candidate); err == nil {
		e.anchor = cand.Run
	}
	now, err := ds.Review(ctx, c.Review)
	if err != nil {
		// Nothing sent: the try is refused, the outcome kept for the
		// next pass until it is recorded.
		s.log.Warn("a review could not be read for its application", "review", c.Review, "err", err)
		s.settledBy(ctx, c, applyOutcome{why: wordsUnread})
		return v
	}
	if now.Application.State != dispatch.ApplicationPending || now.Application.Try != c.Try {
		return now
	}
	s.reviews.seam(&s.reviews.beforeApply, c.Review)
	out := s.applyTry(ctx, now, e, continueRun, expected)
	if !out.pending {
		s.settledBy(ctx, c, out)
	}
	after, err := ds.Review(ctx, c.Review)
	if err != nil {
		return now
	}
	return after
}

// settledBy records the outcome of the try the claim c holds, under it,
// and drops the try's hint once it is recorded, or keeps the outcome for
// the next pass when it is not.
func (s *Server) settledBy(ctx context.Context, c dispatch.ReviewClaim, out applyOutcome) {
	if s.recordOutcome(ctx, c, out) {
		s.reviews.settled(c.Review, c.Try)
		return
	}
	s.reviews.keep(c.Review, c.Try, out)
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
			return appliedFrom(newest, ledger.CommandRow{ID: cmd.ID, Receipt: mustJSON(rc)})
		case rc.CommandID != "" && rc.Status == ledger.CommandFailed:
			return applyOutcome{why: cmp.Or(rc.Error, "Yardmaster refused the request")}
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
// was not its receipt (step 4): the review watched on the run it went to
// first, then judged by judgeCommand, in the run that holds it along the
// candidate's chain. Kept done: applied; failed: refused with its words;
// accepted while the engine holds that run unsettled: pending, watched on
// that run (a refused change and its settling write wait in the run's
// memory for its next commit), for the passes after the request lets it
// go; accepted once it does not, or uncertain: refused; not taken:
// refused with why. A ledger that cannot be read leaves it pending, for
// the next pass.
func (s *Server) readBack(ctx context.Context, to string, e reviewSettle, why string) applyOutcome {
	s.reviews.watch(e.review, e.try, to)
	s.reviews.seam(&s.reviews.afterRegister, e.review)
	out, at, err := s.judgeCommand(ctx, e, to, true, why)
	switch {
	case out.pending && err != nil:
		s.log.Warn("a review's command could not be read back; it stays pending, for the next pass", "review", e.review, "command", e.command, "err", err)
		s.reviews.watch(e.review, e.try, at)
	case out.pending:
		s.reviews.watch(e.review, e.try, at)
		s.log.Info("a review waits for its command's receipt to settle", "review", e.review, "run", at, "command", e.command)
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
	// The new try comes with the store's claim on it, taken in the commit
	// that makes it pending, so no pass judges it before the request has
	// sent its command and recorded its receipt; while another holds the
	// review's claim (a request or a pass settling its try), the store
	// makes no new try and the request waits for that claim's release, at
	// most runTimeout. The release wakes the worker, which settles
	// whatever the request leaves pending, its read failed after the
	// commit included.
	var claim dispatch.ReviewClaim
	if rc, v, claim, err = s.applyingClaimed(ctx, ds, op, id); errors.Is(err, dispatch.ErrReviewClaimed) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the review is being settled: send it again")
		return
	}
	if claim.Held() {
		s.reviews.ours(claim.Review, claim.Try)
		defer s.releaseReview(claim)
	}
	switch {
	case rc.Status == dispatch.StatusDone && err != nil:
		// Committed, its read failed: done all the same, answered by its
		// receipt; the try stays pending for the worker.
		s.writeOp(w, "review", rc, nil, false, "review", func(dispatch.Receipt) (any, error) { return nil, err })
		return
	case err != nil || rc.Status != dispatch.StatusDone:
		s.writeOp(w, "review", rc, err, false, "review", func(dispatch.Receipt) (any, error) { return v, nil })
		return
	}
	if claim.Held() {
		v = s.applyReview(ctx, ds, v, claim, body.ContinueRun, body.ExpectedRevision)
	}
	s.writeOp(w, "review", rc, nil, false, "review", func(dispatch.Receipt) (any, error) { return v, nil })
}

// applyingClaimed is Request again's commit with the store's claim on its
// new try (dispatch.ApplyingReviewClaimed): while another holds the
// review's claim it waits for that claim's release and sends it again, at
// most runTimeout in all, ErrReviewClaimed when the wait ends first.
func (s *Server) applyingClaimed(ctx context.Context, ds *dispatch.Store, op dispatch.Op, id string) (dispatch.Receipt, dispatch.Review, dispatch.ReviewClaim, error) {
	t := time.NewTimer(runTimeout)
	defer t.Stop()
	for {
		rc, v, c, err := ds.ApplyingReviewClaimed(ctx, op, id)
		if !errors.Is(err, dispatch.ErrReviewClaimed) {
			return rc, v, c, err
		}
		free := ds.ReviewFree(id)
		if free == nil {
			continue
		}
		s.reviews.seam(&s.reviews.retryWaits, id)
		select {
		case <-free:
		case <-t.C:
			return rc, v, c, err
		case <-ctx.Done():
			return rc, v, c, err
		}
	}
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
// candidate's run the ledger ld holds: the worker's pass (passReviews),
// run once before launches and reviews are admitted (ledgerRecovered),
// when no review is this process's, so each is judged by its command's
// row looked for in that run and along its continuations under the
// review's actor (reconcileCommand): done, applied; failed, refused with
// its words; accepted or uncertain, refused (act commits a command's
// change and its settled receipt together, and the process that held a
// retained change is gone); none, refused. A review whose run this ledger
// does not hold is left pending, for the ledger's owner. Whatever this
// pass leaves pending (a listing or a read that failed, its budget spent
// by the launches' reconcile before it, an outcome Dispatch could not
// record), the worker's passes take up, the first as soon as reviews are
// admitted. Nothing is applied: a request reaches a run again only when a
// person sends it again, as a new try.
func (s *Server) reconcileReviews(ctx context.Context, ld *ledger.Ledger) {
	if ld == nil {
		return
	}
	_ = s.passReviews(ctx, ld, false)
}

// reviewLedger is what the start's judgement reads of a ledger: a run's
// row and a command's.
type reviewLedger interface {
	Run(ctx context.Context, id string) (ledger.RunRow, bool, error)
	Receipt(ctx context.Context, run, actor, id string) (ledger.CommandRow, bool, error)
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
