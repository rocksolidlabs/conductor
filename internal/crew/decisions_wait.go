package crew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The rest of Yardmaster's wait (docs/plans/decisions.md, DR1 PR-3): what
// an unanswered decision costs (nothing: an attempt that ends while it
// waits is given back, and a waiting attempt whose agent ended its turn
// gives up its member and its slot), what ends a decision besides its
// answer (the work under it ending, the run's end, a newer subject, the
// asking task cancelled, withdrawn or reopened, the operator, the asking
// agent), what a gate's requirement outlives (regate), and the operator's
// operations, each repeating its receipt.

// stageDecision is the stage an attempt closes at when it ends while it
// waits on a decision: given back, not counted.
const stageDecision = "decision"

// runDecisions is what a run keeps of its decisions besides the open and
// answered ones (run.decisions), under e.mu.
type runDecisions struct {
	// settled are the run chain's applied approvals of an agent that a
	// later cause can still supersede: one of a candidate when the task
	// gets another (S1), one of a commit or a file whose line is not known
	// to have gone in, rechecked at the asking task's next start (S4).
	settled []*decision
	// gates says, for each plan decision of the run's package, whether the
	// run chain released it (I14: an applied Approve, answer or Done, or
	// the operator's cancel with release); nil for a run whose gates are
	// not opened (none, or opened by no launch yet).
	gates map[string]bool
	// lastGate is each gate's decision last closed without releasing it,
	// for the words of the one that asks again.
	lastGate map[string]gateEnd
	// opened counts the decisions the run opened, the room's half that
	// regate checks before it asks again (the ledger's MaxDecisionsPerRun).
	opened int
	// chain is the run and the runs it continues, newest first: a task
	// file carries the decisions of the chain's.
	chain []string
	// more and done are the change under way's writes of decisions and
	// what follows its commit, which commit takes with the change's own
	// (later), and carries with them when it fails.
	more []func(LedgerTx) error
	done []func()
	// regating says a regate is on its way (pump's backstop); noRoom
	// that the last regate could not ask a gate again for want of room,
	// so the backstop waits for a decision to leave the run (dropDecision)
	// or a change to regate again rather than asking at every pump.
	regating, noRoom bool
	// filed are the applied answers whose delivery.task_file a change of
	// the run has written, or carries (taskDecisions).
	filed map[int64]bool
	// unapplied are the applied decisions a change of the run has closed
	// since (superseded: S1, S4): a task file's read that found one
	// applied is read again (taskDecisions).
	unapplied map[int64]bool
}

// endGate remembers gate's decision closed without releasing it.
func (rd *runDecisions) endGate(gate string, g gateEnd) {
	if rd.lastGate == nil {
		rd.lastGate = map[string]gateEnd{}
	}
	rd.lastGate[gate] = g
}

// gateEnd is a gate's decision closed without releasing its plan decision.
type gateEnd struct {
	id       string
	rejected bool
}

// later adds to the change under way a write of its decisions and what
// follows its commit (under e.mu). The caller holds e.mu, in a
// transaction's change.
func (r *run) later(write func(LedgerTx) error, done func()) {
	if write != nil {
		r.dec.more = append(r.dec.more, write)
	}
	if done != nil {
		r.dec.done = append(r.dec.done, done)
	}
}

// finished reports whether a task is done for good: succeeded, failed or
// cancelled.
func finished(t *task) bool {
	switch t.state {
	case ledger.TaskSucceeded, ledger.TaskFailed, ledger.TaskCancelled:
		return true
	}
	return false
}

// decisionOp is an operator's operation as its history row keeps it: its
// id and the fingerprint of its body as sent, "" for none.
type decisionOp struct {
	id, fingerprint string
}

// closeDecision ends d, open or answered, or applied and superseded (S1,
// S4), in the change under way: superseded or cancelled with why, its
// holds released (I10), its gate's requirement remembered, the line of its
// applied answer withdrawn if it has not gone in, its question taken away,
// and its asking attempt told when it is live (tell); the row, its history
// row (with the operation's id when an operator's) and its event follow in
// the change's transaction. The caller holds e.mu, in a transaction's
// change.
func (e *Engine) closeDecision(r *run, d *decision, state, why, by string, op decisionOp, detail map[string]any, tell string) {
	was := d.state
	held := slices.Clone(d.holds)
	if was == ledger.DecisionApplied {
		if r.dec.unapplied == nil {
			r.dec.unapplied = map[int64]bool{}
		}
		r.dec.unapplied[d.n] = true
	}
	if d.answer != nil {
		delete(e.confirms, confirmKey{d.n, d.answer.k})
	}
	d.state, d.rev = state, d.rev+1
	r.dropDecision(d)
	r.dec.settled = slices.DeleteFunc(r.dec.settled, func(x *decision) bool { return x == d })
	for _, t := range held {
		r.release(t, d)
	}
	if d.gate != "" && was != ledger.DecisionApplied {
		r.dec.endGate(d.gate, gateEnd{id: d.id()})
	}
	if d.withdrawn != nil && d.delivery == ledgerTyping {
		// Its line may be queued or being typed: no Enter of it after its
		// decision stopped being current (session.Submission.Withdrawn).
		d.closeWithdrawn()
	}
	why = cutWhy(why)
	r.note(session.ActivityStatus, "decision %s %s: %s", d.id(), state, why)
	n, rev, run := d.n, d.rev, r.id
	kind, event := ledger.HistoryCancelled, "decision.cancelled"
	if state == ledger.DecisionSuperseded {
		kind, event = ledger.HistorySuperseded, "decision.superseded"
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["why"], detail["rev"] = why, rev
	tasks := make([]string, 0, len(held))
	for _, t := range held {
		tasks = append(tasks, t.def.ID)
	}
	r.later(func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		row, ok, err := dtx.Decision(n)
		if err != nil || !ok {
			return cmpErr(err, fmt.Errorf("%w: %s", ledger.ErrNoDecision, ledger.DecisionID(n)))
		}
		row.State, row.Rev, row.Why = state, max(rev, row.Rev+1), why
		if err := dtx.PutDecision(row); err != nil {
			return err
		}
		if _, _, err := dtx.History(n, ledger.HistoryRow{Kind: kind, By: by, Detail: historyDetail(detail), OpID: op.id, Fingerprint: op.fingerprint}); err != nil {
			return err
		}
		if row.CurrentRun == run {
			for _, id := range tasks {
				if _, err := dtx.Release(run, id, n); err != nil {
					return err
				}
			}
		}
		return ltx.Event(event, map[string]any{"decision": row.ID(), "why": why, "tasks": tasks})
	}, func() {
		e.withdrawMirror(d.a, d)
		if tell != "" {
			e.tell(r, d, tell)
		}
	})
}

// cmpErr is the first error that is not nil.
func cmpErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// cutWhy bounds a decision's words to the ledger's.
func cutWhy(why string) string {
	why = strings.TrimSpace(oneLine.Replace(why))
	for len(why) > ledger.MaxDecisionWhy-8 {
		_, size := utf8.DecodeLastRuneInString(why)
		why = why[:len(why)-size]
	}
	return why
}

// supersedeAgent supersedes an agent's decision whose subject moved (S1,
// S2, S4), or its applied approval's: its hold released, its asking
// attempt told in one line when it is live, else its task's next attempt
// reads the words in its task file. The caller holds e.mu, in a
// transaction's change.
func (e *Engine) supersedeAgent(r *run, d *decision, moved string) {
	why := supersededWords(d.id(), moved)
	tell := why
	if d.state == ledger.DecisionAnswered {
		tell = notAppliedWords(d.id(), moved)
	}
	e.closeDecision(r, d, ledger.DecisionSuperseded, why, "yardmaster", decisionOp{}, map[string]any{"moved": moved}, tell)
}

// The lines a decision's asking agent is told besides its answer
// (answerWords), which the skill quotes (TestTheSkillQuotesTheEngine):
// its decision superseded because what it was about moved, its answered
// decision's answer not applied for that reason, the operator's request
// for more, and the operator's cancel (why the cancel's words,
// "cancelled by the operator, not approved[: WHY]").
func supersededWords(id, moved string) string {
	return fmt.Sprintf("%s was superseded: %s; ask again if you still need it", id, moved)
}

func notAppliedWords(id, moved string) string {
	return fmt.Sprintf("Decision %s's answer was not applied: %s", id, moved)
}

func moreInfoWords(id, text string) string {
	return fmt.Sprintf("The operator asks about %s: %s", id, text)
}

func cancelledWords(id, why string) string {
	return fmt.Sprintf("Decision %s was %s", id, why)
}

// cancelAsked cancels the decisions t's attempts asked that are not final
// (the asking task cancelled, withdrawn or reopened: S3), with why. The
// caller holds e.mu, in a transaction's change.
func (e *Engine) cancelAsked(r *run, t *task, why string) {
	for _, d := range slices.Clone(r.decisions) {
		if d.origin == ledger.OriginAgent && d.t == t && d.n != 0 {
			e.closeDecision(r, d, ledger.DecisionCancelled, fmt.Sprintf("%s: %s", d.id(), why), "yardmaster", decisionOp{}, nil, "")
		}
	}
}

// endedWork closes what the change under way left with no work under it
// (I11): every decision not final once the run has its outcome ("the run
// ended, failed, before dr-3 was answered"), and a decision whose held
// tasks are all finished or doomed ("build failed, so deploy will not
// start"). A gate that holds no task keeps nothing open. Commit calls it
// once each change is made. The caller holds e.mu.
func (e *Engine) endedWork(r *run) {
	if len(r.decisions) == 0 {
		return
	}
	// The work under a decision first, whose words say what ended it; then
	// the run's end, for what is left.
	defer func() {
		if r.outcome == "" {
			return
		}
		for _, d := range slices.Clone(r.decisions) {
			if d.n != 0 {
				e.closeDecision(r, d, ledger.DecisionCancelled, fmt.Sprintf("the run ended, %s, before %s was answered", r.outcome, d.id()), "yardmaster", decisionOp{}, nil, "")
			}
		}
	}()
	var doomed map[*task]bool
	for _, d := range slices.Clone(r.decisions) {
		if len(d.holds) == 0 || d.n == 0 {
			continue
		}
		if doomed == nil {
			doomed = r.doomed()
		}
		ended := true
		for _, t := range d.holds {
			if !finished(t) && !doomed[t] {
				ended = false
				break
			}
		}
		if ended {
			e.closeDecision(r, d, ledger.DecisionCancelled, endedWords(r, d.holds, doomed), "yardmaster", decisionOp{}, nil, "")
		}
	}
}

// endedWords say why the work under a decision ended: its first held
// task's end, or the need that doomed it.
func endedWords(r *run, held []*task, doomed map[*task]bool) string {
	ids := make([]string, 0, len(held))
	for _, t := range held {
		ids = append(ids, t.def.ID)
	}
	t := held[0]
	if finished(t) {
		return fmt.Sprintf("%s %s, so the work it held has ended", t.def.ID, endWord(t.state))
	}
	seen := map[*task]bool{}
	for need := t; need != nil && !seen[need]; {
		seen[need] = true
		var next *task
		for _, id := range need.def.Needs {
			n := r.task(id)
			if n == nil {
				continue
			}
			if n.state == ledger.TaskFailed || n.state == ledger.TaskCancelled {
				return fmt.Sprintf("%s %s, so %s will not start", n.def.ID, endWord(n.state), strings.Join(ids, ", "))
			}
			if doomed[n] {
				next = n
			}
		}
		need = next
	}
	return fmt.Sprintf("%s will not start", strings.Join(ids, ", "))
}

// endWord is a task's end in words.
func endWord(state string) string {
	switch state {
	case ledger.TaskFailed:
		return "failed"
	case ledger.TaskCancelled:
		return "was cancelled"
	}
	return "succeeded"
}

// giveBack gives back a, which ends while it waits on d without a result
// (O5, I3): the attempt is not counted (extra), and its decision_refunds
// row, d's refunded row and attempt.waited are written in the change's
// transaction; the hold stays. The caller holds e.mu, in the change that
// closes a.
func (e *Engine) giveBack(r *run, a *attempt, d *decision) {
	t := a.t
	t.extra++
	r.note(session.ActivityStatus, "task %s: attempt %d ended while it waits for %s: given back, not counted", t.def.ID, a.n, d.id())
	n, run, task, an := d.n, r.id, t.def.ID, a.n
	r.later(func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		wrote, err := refund(dtx, run, task, an, n)
		if err != nil || !wrote {
			return err
		}
		return ltx.Event("attempt.waited", map[string]any{"task": task, "attempt": an, "decision": ledger.DecisionID(n)})
	}, nil)
}

// refunder is the ledger's writer of a refund (ledger.Tx.Refund).
type refunder interface {
	Refund(run, task string, attempt int, d int64) (bool, error)
}

// refund writes that attempt an of a task of run was given back while it
// waited on decision n, with n's refunded row, once per attempt (I3).
func refund(dtx decisionTx, run, task string, an int, n int64) (bool, error) {
	rf, ok := dtx.(refunder)
	if !ok {
		return false, errNoDecisions
	}
	wrote, err := rf.Refund(run, task, an, n)
	if err != nil || !wrote {
		return false, err
	}
	_, _, err = dtx.History(n, ledger.HistoryRow{Kind: ledger.HistoryRefunded, By: "yardmaster", Detail: historyDetail(map[string]any{"run": run, "task": task, "n": an})})
	return err == nil, err
}

// parkable reports whether a waits on a decision with its agent's turn
// ended since asking (its own mark, never the mirror's showing): an
// attempt that may give up its member or its slot (I12). The caller holds
// e.mu.
func (r *run) parkable(a *attempt) bool {
	return a != nil && a.closedAt.IsZero() && a.state == ledger.AttemptRunning && a.turnEnded && !a.parking && r.waitingDecision(a) != nil
}

// park closes a, which waits on its decision with its agent's turn ended,
// for the ready task its member or its slot keeps from starting: given
// back, its hold kept, its session stopped once the close is durable; the
// answer reaches the task's next attempt through its task file. The
// caller holds e.mu, in pump: the close is a transaction of its own.
func (e *Engine) park(r *run, a *attempt, why string) {
	a.parking = true
	go func() {
		err := e.transact(context.Background(), r, func() tx {
			a.parking = false
			if !r.parkable(a) || r.stopping || r.ctx.Err() != nil {
				return tx{}
			}
			d := r.waitingDecision(a)
			if a.sessionID != "" {
				r.pendingStops = append(r.pendingStops, a.sessionID)
			}
			e.closeAttempt(r, a, ledger.AttemptFailed, stageDecision, fmt.Sprintf("parked while it waits for %s: %s", d.id(), why))
			return tx{}
		}, nil)
		e.afterTransact(r, err)
	}()
}

// parkFor parks m's attempt when it is what keeps t from starting and it
// may give its member up. The caller holds e.mu, in pump.
func (e *Engine) parkFor(r *run, a *attempt, t *task) {
	if r.parkable(a) {
		e.park(r, a, fmt.Sprintf("%s needs %s", t.def.ID, a.m.def.Name))
	}
}

// parkForSlot parks an attempt that waits with its turn ended when the
// run's slots are what keeps a ready task from starting, from the tasks
// after the one pump stopped at: for the first such task whose member is
// free and that has attempts left, the oldest such attempt; for one whose
// member's own attempt waits so, that attempt, which frees the member and
// the slot at once (I12; #636, review round 1). The caller holds e.mu, in
// pump.
func (e *Engine) parkForSlot(r *run, from int) {
	for _, t := range r.tasks {
		if a := t.active(); a != nil && a.parking {
			return // a slot is on its way already
		}
	}
	var want *task
	for _, t := range r.tasks[from:] {
		if t.state != ledger.TaskReady || len(t.holds) > 0 || t.candidate != nil || r.ungated(t) {
			continue
		}
		m := r.member(t.def.Member)
		if m == nil || len(t.attempts)+t.prior >= maxAttemptsPerTask+t.extra {
			continue
		}
		if a := r.activeAttempt(m); a != nil {
			if r.parkable(a) {
				e.park(r, a, fmt.Sprintf("%s needs %s and a slot of the run", t.def.ID, m.def.Name))
				return
			}
			continue
		}
		want = t
		break
	}
	if want == nil {
		return
	}
	var oldest *attempt
	for _, t := range r.tasks {
		if a := t.active(); r.parkable(a) && (oldest == nil || a.reservedAt.Before(oldest.reservedAt)) {
			oldest = a
		}
	}
	if oldest != nil {
		e.park(r, oldest, fmt.Sprintf("%s needs a slot of the run", want.def.ID))
	}
}

// turnEnded marks a's agent's turn ended after its decision's opening (a
// done stamped after it): pump is woken, so a ready task waiting for the
// member or a slot starts at this done. The caller holds e.mu.
func (e *Engine) turnEnded(r *run, a *attempt, d *decision, at time.Time) {
	if d.n == 0 || at.Before(d.openedAt) || a.turnEnded {
		return
	}
	a.turnEnded = true
	go e.pumpLocked(r)
}

// ungated reports whether t is named by a plan decision the run chain has
// not released while no decision holds it: pump's backstop (I14), which
// dispatches no such task and asks again for it. The caller holds e.mu.
func (r *run) ungated(t *task) bool {
	if r.dec.gates == nil || r.pkg == nil || len(t.holds) > 0 {
		return false
	}
	for _, pd := range r.pkg.p.Decisions {
		if !r.dec.gates[pd.ID] && slices.Contains(pd.Before, t.def.ID) {
			return true
		}
	}
	return false
}

// regateLater asks again, in a transaction of its own, for what pump's
// backstop found ungated. The caller holds e.mu.
func (e *Engine) regateLater(r *run) {
	if r.dec.regating || r.dec.noRoom {
		// On its way, or found no room the last time: asked again once a
		// decision leaves the run or a change regates (#636, review
		// round 1), never at every pump.
		return
	}
	r.dec.regating = true
	go func() {
		err := e.transact(context.Background(), r, func() tx {
			r.dec.regating = false
			e.regate(r, "a task the plan's decision names was ready")
			return tx{}
		}, nil)
		e.afterTransact(r, err)
	}()
}

// standingGate is the run's decision of plan decision id that is not
// final, nil for none. The caller holds e.mu.
func (r *run) standingGate(id string) *decision {
	for _, d := range r.decisions {
		if d.origin == ledger.OriginPlan && d.gate == id {
			return d
		}
	}
	return nil
}

// gateTasks are the tasks plan decision pd names that are unfinished and
// not doomed: the work a fresh gate holds. The caller holds e.mu.
func (r *run) gateTasks(pd PackageDecision, doomed map[*task]bool) []*task {
	var out []*task
	for _, id := range pd.Before {
		if t := r.task(id); t != nil && !finished(t) && !doomed[t] && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// regate is the end of every change that makes a task runnable again (a
// retry, an accept, a reopen, a continuation): for each plan decision the
// run chain has not released, the tasks it names that are unfinished and
// not doomed are held by its standing decision, or by a fresh one opened
// in this change ("dr-9 asks again: build was retried after dr-1 was
// cancelled"). So a gate's requirement outlives its decision (I14). cause
// says what made the work runnable again. The caller holds e.mu, in a
// transaction's change.
func (e *Engine) regate(r *run, cause string) {
	if r.dec.gates == nil || r.pkg == nil {
		return
	}
	// Asked afresh: no room is found again below, or there is room now.
	r.dec.noRoom = false
	doomed := r.doomed()
	for _, pd := range r.pkg.p.Decisions {
		if r.dec.gates[pd.ID] {
			continue
		}
		tasks := r.gateTasks(pd, doomed)
		if len(tasks) == 0 {
			continue
		}
		if d := r.standingGate(pd.ID); d != nil {
			for _, t := range tasks {
				if !slices.Contains(d.holds, t) && d.n != 0 {
					r.hold(t, d)
					n, run, id := d.n, r.id, t.def.ID
					r.later(func(ltx LedgerTx) error {
						dtx, err := decisionsOf(ltx)
						if err != nil {
							return err
						}
						return dtx.Hold(run, id, n)
					}, nil)
				}
			}
			continue
		}
		if !r.decisionRoom(1) {
			r.note(session.ActivityError, "the plan's decision %s cannot be asked again: %s", pd.ID, r.roomWords())
			r.dec.noRoom = true
			continue
		}
		e.openGate(r, pd, tasks, cause)
	}
}

// decisionRoom reports whether the run may open n more decisions within
// every bound the ledger's OpenDecision checks for a decision of no
// attempt: the decisions it opened and the places its identities' answers
// hold for their replacements within MaxDecisionsPerRun, and those that
// stand (open, or answered by an identity) within MaxOpenPerRun. A gate
// opened past either would be refused in the commit of the change that
// asked it, and carried to every commit after it (#636, review round 2).
// The caller holds e.mu.
func (r *run) decisionRoom(n int) bool {
	return r.decisionsHeld()+n <= ledger.MaxDecisionsPerRun && r.decisionsStanding()+n <= ledger.MaxOpenPerRun
}

// roomLeft is how many decisions the run may open now (decisionRoom). The
// caller holds e.mu.
func (r *run) roomLeft() int {
	return max(0, min(ledger.MaxDecisionsPerRun-r.decisionsHeld(), ledger.MaxOpenPerRun-r.decisionsStanding()))
}

// roomWords say where the run stands against both bounds. The caller
// holds e.mu.
func (r *run) roomWords() string {
	return fmt.Sprintf("the run holds %d of its %d decisions, %d of its %d open", r.decisionsHeld(), ledger.MaxDecisionsPerRun, r.decisionsStanding(), ledger.MaxOpenPerRun)
}

// decisionsStanding counts the run's decisions the ledger counts against
// MaxOpenPerRun: open, or answered by an identity (whose place stands for
// its replacement). The caller holds e.mu.
func (r *run) decisionsStanding() int {
	n := 0
	for _, d := range r.decisions {
		if d.state == ledger.DecisionOpen || d.state == ledger.DecisionAnswered && d.answer != nil && d.answer.auth.Via == ViaIdentity {
			n++
		}
	}
	return n
}

// gateRequest is a plan decision's request as a gate keeps it: its
// question, an approval's Approve and Reject, the plan revision as its
// subject.
func gateRequest(p Package, pd PackageDecision) (string, requestBody) {
	typ := pd.Kind
	if typ != ledger.DecisionApproval && typ != ledger.DecisionClarification && typ != ledger.DecisionAssignment {
		typ = ledger.DecisionClarification
	}
	body := requestBody{Question: strings.TrimSpace(oneLine.Replace(cleanText(pd.Question))), Subject: &DecisionSubject{Kind: "plan", Plan: p.Plan.ID, Digest: p.Plan.Digest}}
	if utf8.RuneCountInString(body.Question) > ledger.MaxDecisionQuestion {
		body.Question = string([]rune(body.Question)[:ledger.MaxDecisionQuestion])
	}
	if body.Question == "" {
		body.Question = "Decision " + pd.ID
	}
	if typ == ledger.DecisionApproval {
		body.Options = []DecisionOption{{Label: "Approve"}, {Label: "Reject"}}
		if len(pd.Before) > 0 {
			body.Authorizes = "Approve lets " + strings.Join(pd.Before, ", ") + " start; Reject cancels them."
		} else {
			body.Authorizes = "Approve records the plan's decision " + pd.ID + "."
		}
	}
	return typ, body
}

// openGate opens a fresh decision of plan decision pd holding tasks, in
// the change under way: what regate asks again. The caller holds e.mu, in
// a transaction's change.
func (e *Engine) openGate(r *run, pd PackageDecision, tasks []*task, cause string) {
	p := r.pkg.p
	typ, body := gateRequest(p, pd)
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(raw)
	d := &decision{origin: ledger.OriginPlan, typ: typ, gate: pd.ID, question: body.Question, state: ledger.DecisionOpen, rev: 1, subject: body.Subject}
	for _, o := range body.Options {
		d.options = append(d.options, o.Label)
	}
	r.decisions = append(r.decisions, d)
	for _, t := range tasks {
		r.hold(t, d)
	}
	r.dec.opened++
	prev := r.dec.lastGate[pd.ID]
	row := ledger.DecisionRow{Origin: ledger.OriginPlan, Type: typ, Gate: pd.ID, Ticket: p.Ticket.ID, Plan: p.Plan.ID, PlanDigest: p.Plan.Digest,
		Fingerprint: hex.EncodeToString(sum[:]), Request: string(raw), Policy: policyJSON}
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.def.ID)
	}
	var opened int64
	run := r.id
	r.later(func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		got, _, err := dtx.OpenDecision(row)
		if err != nil {
			return err
		}
		opened = got.N
		// The held tasks' rows name it by number (renumber).
		r.opened(d, got.N)
		why := fmt.Sprintf("%s asks again: %s", got.ID(), cause)
		switch {
		case prev.id != "" && prev.rejected:
			why += fmt.Sprintf(" after %s rejected it", prev.id)
		case prev.id != "":
			why += fmt.Sprintf(" after %s was cancelled", prev.id)
		}
		if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryOpened, By: "plan", Detail: historyDetail(map[string]any{"gate": pd.ID, "before": ids, "follows": prev.id, "why": cutWhy(why)})}); err != nil {
			return err
		}
		if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryRouted, By: policyLocal, Detail: routedOperator}); err != nil {
			return err
		}
		for _, id := range ids {
			if err := dtx.Hold(run, id, got.N); err != nil {
				return err
			}
		}
		return ltx.Event("decision.opened", map[string]any{"decision": got.ID(), "origin": ledger.OriginPlan, "type": typ, "gate": pd.ID, "before": ids, "follows": prev.id})
	}, func() {
		d.n = opened
		r.note(session.ActivityStatus, "the plan's decision %s asks again as %s: %s", pd.ID, d.id(), cause)
	})
}

// retryNeedsRoom refuses a retry or an accept of t that would make a plan
// decision ask again in a run with no room for it (decision_limit): the
// task stays as it was. The caller holds e.mu.
func (r *run) retryNeedsRoom(t *task) error {
	if r.dec.gates == nil || r.pkg == nil {
		return nil
	}
	again := map[*task]bool{t: true}
	for changed := true; changed; {
		changed = false
		for _, x := range r.tasks {
			if again[x] {
				continue
			}
			for _, n := range x.def.Needs {
				if nt := r.task(n); nt != nil && again[nt] {
					again[x], changed = true, true
					break
				}
			}
		}
	}
	// Every gate it would ask again, counted together: one retry may
	// reopen several, and each needs its place (#636, review round 1).
	var asks []string
	for _, pd := range r.pkg.p.Decisions {
		if r.dec.gates[pd.ID] || r.standingGate(pd.ID) != nil {
			continue
		}
		for _, id := range pd.Before {
			if x := r.task(id); x != nil && again[x] {
				asks = append(asks, pd.ID)
				break
			}
		}
	}
	if len(asks) == 0 || r.decisionRoom(len(asks)) {
		return nil
	}
	return refuse(CodeDecisionLimit, "the plan's decisions %s would ask again, and the run has room for %d more (%s): the retry is refused", strings.Join(asks, ", "), r.roomLeft(), r.roomWords())
}

// decisionsHeld counts what decisionRoom counts against the run's bound:
// the decisions it opened and the places its identities' answers hold.
// The caller holds e.mu.
func (r *run) decisionsHeld() int {
	places := 0
	for _, d := range r.decisions {
		if d.state == ledger.DecisionAnswered && d.answer != nil && d.answer.auth.Via == ViaIdentity {
			places++
		}
	}
	return r.dec.opened + places
}

// Subjects: what an approval is about, rechecked at response and at
// application (S2, S4), never read off a live file.

// subjectAt is where a decision's subject is rechecked: the asking
// attempt's HEAD while it is live, else the start commit of the attempt
// that will read the answer.
type subjectAt struct {
	live   bool
	dir    string // the worktree, or the repository
	commit string // the start commit, when not live
	a      *attempt
}

// subjectPlace says where d's subject is rechecked now, and whether it is
// rechecked at all: an approval's commit or artifact, not yet delivered
// into the asking session. The caller holds e.mu.
func (e *Engine) subjectPlace(r *run, d *decision) (subjectAt, bool) {
	s := d.subject
	if d.typ != ledger.DecisionApproval || d.origin != ledger.OriginAgent || s == nil || s.Kind != "commit" && s.Kind != "artifact" || d.delivery == ledgerEntered || d.t == nil {
		return subjectAt{}, false
	}
	if a := d.a; a != nil && d.t.active() == a && a.closedAt.IsZero() && a.worktree != "" {
		return subjectAt{live: true, dir: a.worktree, a: a}, true
	}
	return subjectAt{dir: r.cwd, commit: r.startCommit(d.t)}, true
}

// subjectMoved rechecks an approval's subject at at: the commit the
// answer's work rests on, read from the object store; the words of what
// moved, "" when it holds. A commit or path that cannot be read moved: an
// unreadable subject is never counted as validated. A read that failed
// because ctx ended (its caller gone, its attempt cancelled) says nothing
// of the subject: it is ctx's error, and the caller changes nothing (#636,
// review round 2). The caller holds no lock.
func subjectMoved(ctx context.Context, s DecisionSubject, at subjectAt) (string, error) {
	commit := at.commit
	if at.live {
		head, err := headCommit(ctx, at.dir)
		if err != nil || head == "" {
			if cerr := ctx.Err(); cerr != nil {
				return "", cerr
			}
			return fmt.Sprintf("the asking worktree's HEAD cannot be read: %v", err), nil
		}
		commit = head
	}
	switch s.Kind {
	case "commit":
		if commit != s.Commit {
			if at.live {
				return fmt.Sprintf("HEAD is now %s, was %s", short(commit), short(s.Commit)), nil
			}
			return fmt.Sprintf("the next attempt starts at %s, not %s", short(commit), short(s.Commit)), nil
		}
	case "artifact":
		digest, err := blobDigest(ctx, at.dir, commit, s.Path)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return "", cerr
			}
			return err.Error(), nil
		}
		if digest != s.SHA256 {
			return fmt.Sprintf("%s changed: sha256 %s… was %s…", s.Path, digest[:8], s.SHA256[:min(8, len(s.SHA256))]), nil
		}
	}
	return "", nil
}

// recheckSubject reads where d's subject is now and whether it moved, and
// answers with the place it read at, so the change that acts on it checks
// the place is still the same (sameSubject); ctx's error when the read was
// cut short by it (subjectMoved). The caller holds no lock.
func (e *Engine) recheckSubject(ctx context.Context, r *run, d *decision) (subjectAt, string, error) {
	e.mu.Lock()
	at, ok := e.subjectPlace(r, d)
	var s DecisionSubject
	if ok {
		s = *d.subject
	}
	e.mu.Unlock()
	if !ok {
		return subjectAt{}, "", nil
	}
	words, err := subjectMoved(ctx, s, at)
	return at, words, err
}

// sameSubject reports whether d's subject would be rechecked at the place
// at still. The caller holds e.mu.
func (e *Engine) sameSubject(r *run, d *decision, at subjectAt) bool {
	now, ok := e.subjectPlace(r, d)
	if !ok {
		return at == subjectAt{}
	}
	return now.live == at.live && now.a == at.a && now.commit == at.commit
}

// newCandidate supersedes, in the change that records a's submitted
// result, every decision whose subject is its task's earlier candidate,
// open, answered or an applied approval (S1). The caller holds e.mu, in
// that change.
func (e *Engine) newCandidate(r *run, a *attempt) {
	t := a.t
	moved := fmt.Sprintf("the candidate is now %s, attempt %d", short(a.result), a.n)
	stale := func(d *decision) bool {
		s := d.subject
		return d.n != 0 && s != nil && s.Kind == "candidate" && s.Task == t.def.ID && (s.N != a.n || s.Commit != a.result)
	}
	for _, d := range slices.Clone(r.decisions) {
		if stale(d) {
			e.supersedeAgent(r, d, moved)
		}
	}
	for _, d := range slices.Clone(r.dec.settled) {
		if stale(d) {
			e.supersedeAgent(r, d, moved)
		}
	}
}

// The delivery of an applied answer (I6): delivery.typing in the
// application's transaction, then delivery.entered or delivery.untyped
// once the line went in or did not; delivery.uncertain when a restart
// finds the typing without its end, never typed again.
const (
	ledgerTyping    = ledger.HistoryDeliveryTyping
	ledgerEntered   = ledger.HistoryDeliveryEntered
	ledgerUntyped   = ledger.HistoryDeliveryUntyped
	ledgerUncertain = ledger.HistoryDeliveryUncertain
)

// closeWithdrawn closes the channel that withdraws d's line, once. The
// caller holds e.mu.
func (d *decision) closeWithdrawn() {
	select {
	case <-d.withdrawn:
	default:
		close(d.withdrawn)
	}
}

// lineEnded writes how d's typed line ended (delivery.entered, or
// delivery.untyped with why), once, in a transaction of its own. The
// caller holds no lock.
func (e *Engine) lineEnded(r *run, d *decision, kind, why string) {
	e.mu.Lock()
	if d.delivery != ledgerTyping || d.n == 0 {
		e.mu.Unlock()
		return
	}
	d.delivery = kind
	if kind == ledgerUntyped && d.state == ledger.DecisionSuperseded {
		why = fmt.Sprintf("%s was superseded before its answer went in", d.id())
	}
	n := d.n
	e.mu.Unlock()
	detail := map[string]any{}
	if why != "" {
		detail["why"] = cutWhy(why)
	}
	err := e.transact(context.Background(), r, func() tx {
		return tx{more: func(ltx LedgerTx) error {
			dtx, err := decisionsOf(ltx)
			if err != nil {
				return err
			}
			_, _, err = dtx.History(n, ledger.HistoryRow{Kind: kind, By: "yardmaster", Detail: historyDetail(detail)})
			return err
		}}
	}, nil)
	if err != nil {
		e.afterTransact(r, err)
	}
}

// tell types one line to d's asking attempt when it is live, by its
// member's deliverer, as a decision's line (it passes the decision's own
// question and answers none): a supersession's, a cancellation's or the
// operator's request for more. The caller holds e.mu.
func (e *Engine) tell(r *run, d *decision, text string) {
	a := d.a
	if d.origin != ledger.OriginAgent || a == nil || d.t == nil || d.t.active() != a || !a.closedAt.IsZero() {
		return
	}
	line := &answerLine{d: d, a: a, text: strings.TrimSpace(oneLine.Replace(text)), byName: "Conductor", notice: true}
	e.queueAnswer(r, line)
}

// Operations: the operator's comment, request for more information,
// re-route and cancel, and the asking agent's withdrawal. Each of the
// operator's keeps its id and its body's fingerprint on its history row,
// and its repeat answers that row's receipt (An operation repeats as a
// response does).

// The operations' codes besides the responses'.
const (
	CodeOpConflict  = "op_conflict"
	CodeNoRecipient = "no_recipient"
)

// The kinds of a comment (CommentDecision).
const (
	CommentKind  = "comment"
	MoreInfoKind = "more_info"
)

// OpReceipt is what an operation gets, rebuilt from its history row on a
// repeat: the decision, the operation, the row's seq and time, the rev it
// made, its words.
type OpReceipt struct {
	Decision string    `json:"decision"`
	Op       string    `json:"op"`
	Seq      int       `json:"seq"`
	At       time.Time `json:"at"`
	Rev      int       `json:"rev"`
	Words    string    `json:"words"`
	Repeat   bool      `json:"repeat,omitempty"`
}

// opDetail is an operation's history detail: what the receipt is rebuilt
// from.
type opDetail struct {
	Text    string `json:"text,omitempty"`
	By      string `json:"by,omitempty"`
	Rev     int    `json:"rev"`
	Words   string `json:"words"`
	Why     string `json:"why,omitempty"`
	Release bool   `json:"release,omitempty"`
	Note    string `json:"note,omitempty"`
	To      []any  `json:"to,omitempty"`
	Rule    string `json:"rule,omitempty"`
}

// errOpRepeat aborts an operation's transaction whose row its actor sent
// before: the receipt is that row's.
type errOpRepeat struct{ row ledger.HistoryRow }

func (errOpRepeat) Error() string { return "the operation was sent before" }

// opOf finds actor's operation id among a record's history: its receipt,
// repeated, or op_conflict for another body; ok false when not sent.
func opOf(rec ledger.DecisionRecord, actor, opID, fp string) (OpReceipt, bool, error) {
	for _, h := range rec.History {
		if h.OpID == opID && h.By == actor {
			if h.Fingerprint != fp {
				return OpReceipt{}, true, refuse(CodeOpConflict, "operation %s on %s was sent before with another body", opID, rec.ID())
			}
			return receiptOfRow(rec.ID(), h), true, nil
		}
	}
	return OpReceipt{}, false, nil
}

// receiptOfRow is the receipt an operation's history row makes.
func receiptOfRow(id string, h ledger.HistoryRow) OpReceipt {
	var dt opDetail
	_ = json.Unmarshal([]byte(h.Detail), &dt)
	return OpReceipt{Decision: id, Op: h.Kind, Seq: h.Seq, At: h.At, Rev: dt.Rev, Words: dt.Words, Repeat: true}
}

// operation is an operator's operation on decision n: the record read, the
// repeat looked for, then its transaction through the run that holds the
// decision when the engine holds it (change, under e.mu, in its change),
// else through the ledger on that run directly (row, for an operation
// that changes no engine state). It answers the receipt.
type operation struct {
	kind     string // the history row's
	opID, fp string
	actor    string
	// expectedRev is the rev the operator read: the operation is refused
	// stale_decision when the decision's row is at another (staleOp),
	// judged by the row in the transaction that applies the operation,
	// before anything changes, whether or not the engine holds a copy of
	// the decision (judge), after the look for a repeat of its id. 0
	// checks none:
	// the routes always send one (internal/api, decisions.go), and only
	// the engine's own tests send none.
	expectedRev int
	// held makes the change in the run the engine holds: what it changes
	// in memory, its refusal, and the history row it writes. Nil for an
	// operation refused when the run is not held (notHeld's words).
	held    func(r *run, d *decision, rec ledger.DecisionRecord) (detail opDetail, after func(), refusal error)
	notHeld func(rec ledger.DecisionRecord) (detail opDetail, refusal error)
	// rev says the operation bumps the decision's rev.
	rev bool
	// ahead says the held change moves the engine's state past what an
	// undo could put back (the cancel: a decision closed, its holds
	// released, tasks cancelled): it has no undo, its commit is under no
	// request's context, and a refused commit is carried by the pause's
	// (#636, review round 1). Every other operation's change is put back
	// whole when its commit is refused: the rev it bumped, the turnEnded
	// mark it cleared.
	ahead bool
}

// errOpMoved is an operation's transaction that found its decision held
// by another run than the one it chose: it is made again there.
var errOpMoved = errors.New("the decision moved to another run")

func (e *Engine) operate(ctx context.Context, id string, op operation) (OpReceipt, error) {
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return OpReceipt{}, refuse(CodeNotFound, "%q is not a decision's id", shortText(id))
	}
	if !responseID.MatchString(op.opID) {
		return OpReceipt{}, refuse(CodeInvalid, "an operation's id is 1 to 32 of A-Z a-z 0-9 . _ ~ -")
	}
	if op.actor == "" || len(op.actor) > ledger.MaxDecisionActor {
		return OpReceipt{}, refuse(CodeInvalid, "an operation names its actor")
	}
	// One operation at a time, from its look to its commit: a repeat or a
	// conflict of its id is found by the look, never by its transaction.
	e.opMu.Lock()
	defer e.opMu.Unlock()
	for range maxDecisionTries {
		rec, ok, err := e.readDecision(ctx, n)
		if err != nil {
			return OpReceipt{}, err
		}
		if !ok {
			return OpReceipt{}, refuse(CodeNotFound, "no decision %s", id)
		}
		if rc, found, err := opOf(rec, op.actor, op.opID, op.fp); found {
			return rc, err
		}
		if op.expectedRev < 0 {
			return OpReceipt{}, refuse(CodeInvalid, "an operation names the rev it read, expectedRev")
		}
		if e.beforeOperate != nil {
			e.beforeOperate(n)
		}
		e.mu.Lock()
		r := e.runs[rec.CurrentRun]
		held := r != nil && r.managed && r.created && r.ctx.Err() == nil && !r.stopping
		e.mu.Unlock()
		var (
			out     OpReceipt
			refused error
		)
		// judge is the operation's one judgement of the decision, in the
		// transaction that applies it and before anything changes: its row
		// still the run's the look chose, and at the rev the operator read.
		// The row judges, never the engine's copy, which a write outside
		// the run's transactions (a comment kept while a continuation read
		// the decisions) leaves behind (#637, review round 2).
		judge := func(ltx LedgerTx) error {
			dtx, err := decisionsOf(ltx)
			if err != nil {
				return err
			}
			row, ok, err := dtx.Decision(n)
			if err != nil || !ok {
				return cmpErr(err, fmt.Errorf("%w: %s", ledger.ErrNoDecision, id))
			}
			if row.CurrentRun != rec.CurrentRun {
				return errOpMoved
			}
			if why := staleOp(id, op.expectedRev, row.Rev); why != nil {
				return why
			}
			if held {
				// The engine's copy takes its row's rev, which the change
				// then bumps.
				e.mu.Lock()
				if d := r.decisionN(n); d != nil {
					d.rev = max(d.rev, row.Rev)
				} else if d := settledN(r, n); d != nil {
					d.rev = max(d.rev, row.Rev)
				}
				e.mu.Unlock()
			}
			return nil
		}
		// write is the operation's rows in its transaction, after judge.
		write := func(ltx LedgerTx, dt opDetail) error {
			dtx, err := decisionsOf(ltx)
			if err != nil {
				return err
			}
			row, ok, err := dtx.Decision(n)
			if err != nil || !ok {
				return cmpErr(err, fmt.Errorf("%w: %s", ledger.ErrNoDecision, id))
			}
			if op.rev {
				row.Rev++
				if err := dtx.PutDecision(row); err != nil {
					return err
				}
			}
			dt.Rev = max(dt.Rev, row.Rev)
			got, repeat, err := dtx.History(n, ledger.HistoryRow{Kind: op.kind, By: op.actor, Detail: historyDetail(dt), OpID: op.opID, Fingerprint: op.fp})
			if err != nil {
				return err
			}
			if repeat {
				return errOpRepeat{got}
			}
			out = OpReceipt{Decision: id, Op: op.kind, Seq: got.Seq, At: got.At, Rev: dt.Rev, Words: dt.Words}
			if op.kind == ledger.HistoryCancelled {
				return nil // the cancel's event is its change's (closeDecisionOp)
			}
			return ltx.Event("decision."+op.kind, map[string]any{"decision": id, "op": op.opID, "actor": op.actor})
		}
		if held {
			var after func()
			var undo func()
			moved := false
			change := func() tx {
				if r.stopping || r.ctx.Err() != nil {
					// Stopping since the look: the decision is chosen again,
					// as one of a run not held.
					moved = true
					return tx{}
				}
				d := r.decisionN(n)
				if d == nil {
					d = settledN(r, n)
				}
				var was int
				var wasEnded bool
				if d != nil {
					was = d.rev
					if d.a != nil {
						wasEnded = d.a.turnEnded
					}
				}
				dt, then, why := op.held(r, d, rec)
				if why != nil {
					refused = why
					return tx{}
				}
				if d != nil && op.rev {
					d.rev++
					dt.Rev = d.rev
				}
				undo = func() {
					if d != nil {
						d.rev = was
						if d.a != nil {
							d.a.turnEnded = wasEnded
						}
					}
				}
				after = then
				return tx{more: func(ltx LedgerTx) error { return write(ltx, dt) }}
			}
			if op.ahead {
				err = e.transactJudged(context.WithoutCancel(ctx), r, judge, change, nil)
			} else {
				err = e.transactJudged(ctx, r, judge, change, func() {
					if undo != nil {
						undo()
					}
				})
			}
			if moved {
				continue
			}
			// Refused or failed before its change was made (judge, at the
			// row's rev): nothing changed, nothing is carried or paused.
			var um errUnmade
			unmade := errors.As(err, &um)
			if unmade {
				err = um.err
			}
			var de *DecisionError
			if (unmade || !op.ahead) && errors.As(err, &de) {
				// Refused in its transaction, undone or never made.
				refused, err = de, nil
			}
			if err == nil && refused == nil && after != nil {
				after()
			}
			if !unmade && (err == nil && refused == nil || err != nil && op.ahead) {
				// Committed, what it released is dispatched; refused, a
				// change kept ahead pauses the run, whose pause's commit
				// writes it.
				e.afterTransact(r, err)
			}
		} else {
			if op.notHeld == nil {
				return OpReceipt{}, refuse(CodeNotHeld, "%s's run is not running: continue it, then try again", id)
			}
			dt, why := op.notHeld(rec)
			if why != nil {
				return OpReceipt{}, why
			}
			if op.rev {
				dt.Rev = rec.Rev + 1
			}
			_, err = e.ledger.Transition(ctx, rec.CurrentRun, func(ltx LedgerTx) error {
				if err := judge(ltx); err != nil {
					return err
				}
				return write(ltx, dt)
			})
			var de *DecisionError
			if errors.As(err, &de) {
				refused, err = de, nil
			}
		}
		var rep errOpRepeat
		switch {
		case refused != nil:
			// A repeat whose first commit came after the look above finds
			// the decision changed by it: its receipt, not a refusal.
			if again, ok, rerr := e.readDecision(ctx, n); rerr == nil && ok {
				if rc, found, err := opOf(again, op.actor, op.opID, op.fp); found {
					return rc, err
				}
			}
			return OpReceipt{}, refused
		case errors.As(err, &rep):
			return receiptOfRow(id, rep.row), nil
		case errors.Is(err, errOpMoved):
			continue
		case errors.Is(err, ledger.ErrDecisionConflict):
			return OpReceipt{}, refuse(CodeOpConflict, "operation %s on %s was sent before with another body", op.opID, id)
		case err != nil:
			if de := decisionFailure(err); de != nil {
				return OpReceipt{}, de
			}
			return OpReceipt{}, err
		}
		return out, nil
	}
	return OpReceipt{}, refuse(CodeDecisionMoved, "%s moved to another run while it was changed: send it again", id)
}

// staleOp refuses an operation sent at the rev read when the decision is
// at the rev now: stale_decision when it moved past it, invalid for a rev
// it never had; nil when they agree, or when no rev was sent (read 0).
func staleOp(id string, read, now int) error {
	switch {
	case read == 0 || read == now:
		return nil
	case read > now:
		return refuse(CodeInvalid, "%s is at rev %d: no rev %d has been read", id, now, read)
	}
	return refuse(CodeStaleDecision, "%s is at rev %d, not the rev %d read: read it again", id, now, read)
}

// settledN is the run's settled decision n, nil for none. The caller holds
// e.mu.
func settledN(r *run, n int64) *decision {
	for _, d := range r.dec.settled {
		if d.n == n {
			return d
		}
	}
	return nil
}

// cleanOpText cleans an operation's text: 1 to 4,000 characters.
func cleanOpText(text string, need bool) (string, error) {
	text = strings.TrimSpace(cleanText(text))
	if n := utf8.RuneCountInString(text); need && n < 1 || n > ledger.MaxDecisionContext {
		return "", refuse(CodeInvalid, "the text is 1 to %d characters", ledger.MaxDecisionContext)
	}
	if b, _ := json.Marshal(text); len(b) > ledger.MaxHistoryDetail-256 {
		return "", refuse(CodeInvalid, "the text is %d bytes as kept, more than %d", len(b), ledger.MaxHistoryDetail-256)
	}
	return text, nil
}

// CommentDecision keeps the operator's comment on decision id (kind
// comment, taken in every state) or request for more information (kind
// more_info: typed to the asking attempt's live session as "The operator
// asks about dr-7: …", taken only while the decision is open, an agent's,
// and its asking attempt live, else no_recipient). Neither changes the
// state; both bump the rev, so a response made against the card before
// them is stale_decision. A more_info clears the attempt's turnEnded mark:
// its agent takes a turn to answer, by a new request that supersedes the
// decision.
func (e *Engine) CommentDecision(ctx context.Context, id, opID string, expectedRev int, kind, text, actor string) (OpReceipt, error) {
	text, err := cleanOpText(text, true)
	if err != nil {
		return OpReceipt{}, err
	}
	switch kind {
	case CommentKind:
		return e.operate(ctx, id, operation{kind: ledger.HistoryCommented, opID: opID, fp: fingerprintOf([]string{kind, text}), actor: actor, rev: true, expectedRev: expectedRev,
			held: func(r *run, d *decision, rec ledger.DecisionRecord) (opDetail, func(), error) {
				return opDetail{Text: text, By: actor, Words: fmt.Sprintf("Comment kept on %s", id)}, nil, nil
			},
			notHeld: func(rec ledger.DecisionRecord) (opDetail, error) {
				return opDetail{Text: text, By: actor, Words: fmt.Sprintf("Comment kept on %s", id)}, nil
			}})
	case MoreInfoKind:
	default:
		return OpReceipt{}, refuse(CodeInvalid, "a comment is a comment or a more_info, not %q", kind)
	}
	return e.operate(ctx, id, operation{kind: ledger.HistoryInfoRequested, opID: opID, fp: fingerprintOf([]string{kind, text}), actor: actor, rev: true, expectedRev: expectedRev,
		held: func(r *run, d *decision, rec ledger.DecisionRecord) (opDetail, func(), error) {
			switch {
			case rec.Origin == ledger.OriginPlan:
				return opDetail{}, nil, refuse(CodeNoRecipient, "%s is the plan's: no agent asked it, comment instead", id)
			case d == nil || d.state != ledger.DecisionOpen:
				return opDetail{}, nil, refuse(CodeNoRecipient, "%s is %s", id, cmpState(d, rec))
			case d.a == nil || d.t == nil || d.t.active() != d.a || !d.a.closedAt.IsZero() || d.a.sessionID != rec.SessionID || !e.sessionLive(d.a.sessionID):
				return opDetail{}, nil, refuse(CodeNoRecipient, "%s's agent has ended its attempt: comment, answer or cancel it", id)
			}
			d.a.turnEnded = false
			line := moreInfoWords(id, text)
			return opDetail{Text: text, By: actor, Words: fmt.Sprintf("Asked %s's agent for more", id)}, func() {
				e.mu.Lock()
				e.tell(r, d, line)
				e.mu.Unlock()
			}, nil
		},
		notHeld: func(rec ledger.DecisionRecord) (opDetail, error) {
			if rec.Origin == ledger.OriginPlan {
				return opDetail{}, refuse(CodeNoRecipient, "%s is the plan's: no agent asked it, comment instead", id)
			}
			if rec.State != ledger.DecisionOpen {
				return opDetail{}, refuse(CodeNoRecipient, "%s is %s", id, rec.State)
			}
			return opDetail{}, refuse(CodeNoRecipient, "%s's agent has ended its attempt: comment, answer or cancel it", id)
		}})
}

// cmpState is a decision's state for a refusal's words.
func cmpState(d *decision, rec ledger.DecisionRecord) string {
	if d != nil {
		return d.state
	}
	return rec.State
}

// Destination is where a decision is routed: the operator alone in DR1.
type Destination struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// RouteDecision records a re-route of decision id with a note: DR1's only
// destination is the operator (what Brakeman's route_decision grant does);
// any other is invalid until a person's label is one. It changes no
// state.
func (e *Engine) RouteDecision(ctx context.Context, id, opID string, expectedRev int, to []Destination, note, actor string) (OpReceipt, error) {
	if len(to) == 0 {
		return OpReceipt{}, refuse(CodeInvalid, "a route names its destination")
	}
	for _, d := range to {
		if d.Kind != "operator" || d.Name != "" {
			return OpReceipt{}, refuse(CodeInvalid, "a decision is routed to the operator, not %q", shortText(d.Kind+" "+d.Name))
		}
	}
	note, err := cleanOpText(note, false)
	if err != nil {
		return OpReceipt{}, err
	}
	dt := opDetail{Note: note, To: []any{map[string]string{"kind": "operator"}}, Rule: "local/1: the operator answers", By: actor, Words: fmt.Sprintf("%s is routed to the operator", id)}
	return e.operate(ctx, id, operation{kind: ledger.HistoryRouted, opID: opID, fp: fingerprintOf([]string{"route", note}), actor: actor, expectedRev: expectedRev,
		held: func(r *run, d *decision, rec ledger.DecisionRecord) (opDetail, func(), error) {
			return dt, nil, nil
		},
		notHeld: func(rec ledger.DecisionRecord) (opDetail, error) { return dt, nil }})
}

// CancelDecision is the operator's cancel of decision id, open or
// answered, a command with a receipt (its commandId the operation's id): a
// gate's tasks are released as "cancelled by the operator, not approved"
// only with release, the plan decision then waived for the run chain, else
// cancelled once the decision is final; an agent's decision's hold is
// released and its agent told.
func (e *Engine) CancelDecision(ctx context.Context, id, commandID string, expectedRev int, release bool, why, actor string) (OpReceipt, error) {
	why, err := cleanOpText(why, false)
	if err != nil {
		return OpReceipt{}, err
	}
	return e.operate(ctx, id, operation{kind: ledger.HistoryCancelled, opID: commandID, fp: fingerprintOf([]any{"cancel", release, why}), actor: actor, ahead: true, expectedRev: expectedRev,
		held: func(r *run, d *decision, rec ledger.DecisionRecord) (opDetail, func(), error) {
			if d == nil || !d.waits() || d.n == 0 {
				return opDetail{}, nil, refuse(CodeDecisionClosed, "%s is %s: it is not cancelled", id, cmpState(d, rec))
			}
			words := "cancelled by the operator, not approved"
			if why != "" {
				words += ": " + why
			}
			dt := opDetail{By: actor, Why: why, Release: release, Words: fmt.Sprintf("%s cancelled", id)}
			held := slices.Clone(d.holds)
			if d.origin == ledger.OriginPlan && release {
				r.dec.gates[d.gate] = true
			}
			// The cancel's own row is the operation's (the transaction's
			// write): closeDecision writes the rest, final before any task
			// it cancels (A decision is final before the tasks it cancels).
			e.closeDecisionOp(r, d, words, actor)
			dt.Rev = d.rev
			var events []event
			if d.origin == ledger.OriginPlan && !release {
				reason := fmt.Sprintf("cancelled with %s by the operator", id)
				// The run's end once, after every cancellation, as a
				// rejection's (applyTx): one that dooms a task held with it
				// settles the run before that task's own.
				was := r.outcome
				for _, t := range held {
					if cancellable(t) == nil {
						for _, ev := range e.cancelTask(r, t) {
							if ev.kind != "run.finished" {
								events = append(events, ev)
							}
						}
						if t.state == ledger.TaskCancelled {
							t.reason = reason
							r.markTask(t)
						}
					}
				}
				if was == "" && r.outcome != "" {
					events = append(events, event{"run.finished", map[string]any{"outcome": r.outcome}})
				}
			}
			r.dec.more = append(r.dec.more, func(ltx LedgerTx) error {
				for _, ev := range events {
					if err := ltx.Event(ev.kind, ev.payload); err != nil {
						return err
					}
				}
				return nil
			})
			return dt, nil, nil
		}})
}

// closeDecisionOp is closeDecision for the operator's cancel, whose history
// row is the operation's own: the state, the holds and the event. The
// caller holds e.mu, in a transaction's change.
func (e *Engine) closeDecisionOp(r *run, d *decision, why, by string) {
	held := slices.Clone(d.holds)
	d.state, d.rev = ledger.DecisionCancelled, d.rev+1
	r.dropDecision(d)
	for _, t := range held {
		r.release(t, d)
	}
	if d.gate != "" && !r.dec.gates[d.gate] {
		r.dec.endGate(d.gate, gateEnd{id: d.id()})
	}
	r.note(session.ActivityStatus, "decision %s cancelled by %s", d.id(), by)
	n, run, rev := d.n, r.id, d.rev
	tasks := make([]string, 0, len(held))
	for _, t := range held {
		tasks = append(tasks, t.def.ID)
	}
	why = cutWhy(why)
	r.later(func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		row, ok, err := dtx.Decision(n)
		if err != nil || !ok {
			return cmpErr(err, fmt.Errorf("%w: %s", ledger.ErrNoDecision, ledger.DecisionID(n)))
		}
		row.State, row.Rev, row.Why = ledger.DecisionCancelled, max(rev, row.Rev+1), why
		if err := dtx.PutDecision(row); err != nil {
			return err
		}
		for _, id := range tasks {
			if _, err := dtx.Release(run, id, n); err != nil {
				return err
			}
		}
		return ltx.Event("decision.cancelled", map[string]any{"decision": row.ID(), "why": why, "tasks": tasks, "by": by})
	}, func() {
		e.withdrawMirror(d.a, d)
		e.tell(r, d, cancelledWords(d.id(), why))
	})
}

// WithdrawDecision is the asking agent's withdrawal of its own open
// decision (the tool decision_withdraw): cancelled, its hold released.
func (e *Engine) WithdrawDecision(ctx context.Context, sessionID, id string) (Decision, error) {
	r, a, err := e.attemptOf(sessionID)
	if err != nil {
		return Decision{}, err
	}
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return Decision{}, refuse(CodeNotFound, "%q is not a decision's id", shortText(id))
	}
	var refused error
	// A change kept ahead (no undo): under no request's context, so that
	// only the ledger refuses it.
	err = e.transact(context.WithoutCancel(ctx), r, func() tx {
		d := r.decisionN(n)
		switch {
		case d == nil || d.a != a:
			refused = refuse(CodeNotFound, "%s is no decision of this attempt's that is open", id)
		case d.state != ledger.DecisionOpen:
			refused = refuse(CodeDecisionAnswered, "%s is %s: it is not withdrawn", id, d.state)
		default:
			e.closeDecision(r, d, ledger.DecisionCancelled, fmt.Sprintf("%s was withdrawn by its agent", id), a.m.def.Name, decisionOp{}, nil, "")
		}
		return tx{}
	}, nil)
	if refused != nil {
		return Decision{}, refused
	}
	e.afterTransact(r, err)
	if err != nil {
		return Decision{}, err
	}
	return e.Decision(ctx, id)
}
