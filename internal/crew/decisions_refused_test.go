package crew

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Every write of Yardmaster's wait, its commit refused (#636, review round
// 1): the engine's view and the ledger never disagree for good. A change
// the engine cannot put back (a decision closed, its holds released, an
// attempt given back, a run ended) has no undo: a refused commit keeps its
// rows for the next commit, the run's pause's or whoever's, so the ledger
// comes to hold what the engine does. A change it can put back (an
// operator's comment, a request for more) is put back whole. A change
// refused before the ledger holds anything of it (a continuation's first)
// is dropped whole with the run it would have made. Each test refuses the
// commit that writes one event, through a seam (kindRefusingLedger), waits
// on the ledger for what it must hold, then reads the engine.

// kindRefusingLedger is the real ledger, which refuses, once armed with an
// event kind, the next transition that writes an event of that kind: its
// writes made, then rolled back, as a commit the ledger refused.
type kindRefusingLedger struct {
	sqlLedger
	on      atomic.Pointer[string]
	refused chan string
}

// refusing puts a kindRefusingLedger over the engine's ledger.
func refusing(e *Engine, ld *ledger.Ledger) *kindRefusingLedger {
	rl := &kindRefusingLedger{sqlLedger: sqlLedger{l: ld}, refused: make(chan string, 1)}
	e.UseLedger(rl)
	return rl
}

// arm refuses the next transition that writes an event of kind.
func (l *kindRefusingLedger) arm(kind string) { l.on.Store(&kind) }

// errRefusedTest is the armed refusal.
var errRefusedTest = errors.New("the ledger refused the commit (test)")

// watchTx is a transaction that notes whether it wrote an event of kind.
type watchTx struct {
	*ledger.Tx
	kind string
	saw  bool
}

func (w *watchTx) Event(kind string, payload any) error {
	if kind == w.kind {
		w.saw = true
	}
	return w.Tx.Event(kind, payload)
}

func (l *kindRefusingLedger) Transition(ctx context.Context, runID string, fn func(LedgerTx) error) (int64, error) {
	kind := l.on.Load()
	if kind == nil {
		return l.sqlLedger.Transition(ctx, runID, fn)
	}
	return l.l.Transition(ctx, runID, func(tx *ledger.Tx) error {
		w := &watchTx{Tx: tx, kind: *kind}
		if err := fn(w); err != nil {
			return err
		}
		if w.saw && l.on.CompareAndSwap(kind, nil) {
			l.refused <- *kind
			return errRefusedTest
		}
		return nil
	})
}

// wasRefused waits for the armed refusal: the bound only bounds a hang.
func (l *kindRefusingLedger) wasRefused(t *testing.T) {
	t.Helper()
	select {
	case <-l.refused:
	case <-time.After(60 * time.Second):
		t.Fatal("no commit was refused")
	}
}

// engineHolds says whether the engine holds decision id among the run's
// open and answered ones, and its rev there.
func engineHolds(t *testing.T, e *Engine, runID, id string) (int, bool) {
	t.Helper()
	n, _ := ledger.ParseDecisionID(id)
	r := runOf(t, e, runID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if d := r.decisionN(n); d != nil {
		return d.rev, true
	}
	return 0, false
}

// paused says whether the run's dispatch is paused.
func paused(t *testing.T, e *Engine, runID string) bool {
	t.Helper()
	r, ok := e.Get(runID)
	if !ok {
		t.Fatalf("run %s is gone", runID)
	}
	return r.Paused
}

// A response to an approval whose commit moved: the supersession is its
// own change, never undone, so a response its transaction then refuses
// (a rev nobody read) leaves it superseded in the ledger as in the engine,
// its hold released in both, and a later response is refused; and a
// supersession whose own commit the ledger refuses is carried by the
// pause's, the response refused stale_decision after it.
func TestARefusedResponseLeavesItsSupersession(t *testing.T) {
	moved := func(t *testing.T) (*Engine, *ledger.Ledger, *kindRefusingLedger, Run, Decision, string) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		rl := refusing(e, ld)
		run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
		b, _, _ := attemptOn(t, e, fl, run.ID, "build")
		was := head(t, b.Attempt.Worktree)
		d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
		writeFile(t, filepath.Join(b.Attempt.Worktree, "more.txt"), "m\n")
		commitAll(t, b.Attempt.Worktree, "more")
		return e, ld, rl, run, d, "HEAD is now " + short(head(t, b.Attempt.Worktree)) + ", was " + short(was)
	}
	settled := func(t *testing.T, e *Engine, ld *ledger.Ledger, run Run, d Decision) {
		t.Helper()
		rec := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
		if kindCount(rec, ledger.HistorySuperseded) != 1 {
			t.Fatalf("%s's history: %q", d.ID, historyKinds(rec))
		}
		if got := activeHolds(t, ld, run.ID); len(got) != 0 {
			t.Fatalf("the ledger's holds %q", got)
		}
		if _, ok := engineHolds(t, e, run.ID, d.ID); ok {
			t.Fatalf("the engine still holds %s", d.ID)
		}
		if ts := taskOf(t, e, run.ID, "build"); len(ts.Holds) != 0 {
			t.Fatalf("the engine's holds of build: %+v", ts.Holds)
		}
		var de *DecisionError
		if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r2", Choice: choice(0), ExpectedRev: rec.Rev}, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeDecisionClosed {
			t.Fatalf("a later response: %v", err)
		}
		if rec := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded)); slices.ContainsFunc(rec.Responses, func(r ledger.ResponseRow) bool { return r.Status == ledger.ResponseAccepted }) {
			t.Fatalf("a response was accepted: %+v", rec.Responses)
		}
	}
	t.Run("the response refused in its transaction", func(t *testing.T) {
		e, ld, _, run, d, _ := moved(t)
		var de *DecisionError
		if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 9}, "admin", workbench(e)); !errors.As(err, &de) || de.Code != CodeInvalid {
			t.Fatalf("the response: %v", err)
		}
		settled(t, e, ld, run, d)
		stopRun(t, e, run.ID)
	})
	t.Run("the supersession's commit refused", func(t *testing.T) {
		e, ld, rl, run, d, want := moved(t)
		rl.arm("decision.superseded")
		var de *DecisionError
		rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1}, "admin", workbench(e))
		if !errors.As(err, &de) || de.Code != CodeStaleDecision || !strings.Contains(rc.Words, want) {
			t.Fatalf("the response: %+v %v", rc, err)
		}
		rl.wasRefused(t)
		settled(t, e, ld, run, d)
		if !paused(t, e, run.ID) {
			t.Fatal("the run did not pause for the refused commit")
		}
		stopRun(t, e, run.ID)
	})
}

// The operator's cancel of a gate, its commit refused: kept ahead, the
// pause's commit writes it, so the ledger holds the gate cancelled and its
// task cancelled as the engine does; the cancel sent again gets its
// receipt. With release, the waiver is the engine's and the ledger's.
func TestARefusedCancelIsCarried(t *testing.T) {
	for _, release := range []bool{false, true} {
		name := "without release"
		if release {
			name = "with release"
		}
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, ld := decisionEngine(t)
			rl := refusing(e, ld)
			run := launchGated(t, e, dir, "api-sweep-000000f"+map[bool]string{false: "1", true: "2"}[release], repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
			g := gateOf(t, e, ld, run.ID)
			rl.arm("decision.cancelled")
			if _, err := e.CancelDecision(t.Context(), g.ID, "c1", 0, release, "", "admin"); err == nil {
				t.Fatal("a cancel whose commit was refused answered")
			}
			rl.wasRefused(t)
			rec := decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
			if kindCount(rec, ledger.HistoryCancelled) != 1 || waived(rec) != release {
				t.Fatalf("%s's history: %q", g.ID, historyKinds(rec))
			}
			if got := activeHolds(t, ld, run.ID); len(got) != 0 {
				t.Fatalf("the ledger's holds %q", got)
			}
			if _, ok := engineHolds(t, e, run.ID, g.ID); ok {
				t.Fatalf("the engine still holds %s", g.ID)
			}
			if release {
				r := runOf(t, e, run.ID)
				e.mu.Lock()
				waiver := r.dec.gates["ship"]
				e.mu.Unlock()
				if !waiver {
					t.Fatal("the engine did not take the waiver")
				}
			} else {
				waitTask(t, e, run.ID, "deploy", ledger.TaskCancelled, 0)
				if row := ledgerTask(t, ld, run.ID, "deploy"); row.State != ledger.TaskCancelled {
					t.Fatalf("deploy in the ledger: %+v", row)
				}
			}
			rc, err := e.CancelDecision(t.Context(), g.ID, "c1", 0, release, "", "admin")
			if err != nil || !rc.Repeat {
				t.Fatalf("the cancel sent again: %+v %v", rc, err)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// An operator's comment and request for more, their commits refused: put
// back whole, the rev and the agent's ended turn as they were, nothing in
// the ledger; sent again, each is taken.
func TestARefusedOperationIsPutBack(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	rl := refusing(e, ld)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	reportDone(e, b.Attempt.SessionID)
	r := runOf(t, e, run.ID)
	ended := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return r.task("build").active().turnEnded
	}
	if !ended() {
		t.Fatal("the agent's done did not end its turn")
	}
	for _, op := range []struct{ kind, event string }{{MoreInfoKind, "decision." + ledger.HistoryInfoRequested}, {CommentKind, "decision." + ledger.HistoryCommented}} {
		rl.arm(op.event)
		if _, err := e.CommentDecision(t.Context(), d.ID, "o-"+op.kind, 0, op.kind, "For which table?", "admin"); err == nil {
			t.Fatalf("a %s whose commit was refused answered", op.kind)
		}
		rl.wasRefused(t)
		if rev, ok := engineHolds(t, e, run.ID, d.ID); !ok || rev != 1 || !ended() {
			t.Fatalf("after the refused %s: rev %d, held %v, turn ended %v", op.kind, rev, ok, ended())
		}
		if rec := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen)); rec.Rev != 1 || kindCount(rec, ledger.HistoryInfoRequested)+kindCount(rec, ledger.HistoryCommented) != 0 {
			t.Fatalf("after the refused %s: rev %d %q", op.kind, rec.Rev, historyKinds(rec))
		}
	}
	if paused(t, e, run.ID) {
		t.Fatal("a change put back paused the run")
	}
	if _, err := e.CommentDecision(t.Context(), d.ID, "o-again", 0, CommentKind, "Noted.", "admin"); err != nil {
		t.Fatal(err)
	}
	rec := decisionWhen(t, ld, d.ID, "commented", hasKind(ledger.HistoryCommented))
	if rev, _ := engineHolds(t, e, run.ID, d.ID); rec.Rev != 2 || rev != 2 {
		t.Fatalf("the ledger's rev %d, the engine's %d", rec.Rev, rev)
	}
	stopRun(t, e, run.ID)
}

// A park, its commit refused: the attempt closed and given back in the
// engine, its refund and its row in the ledger, written by the pause's
// commit.
func TestARefusedParkIsCarried(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	rl := refusing(e, ld)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	rl.arm("attempt.waited")
	reportDone(e, b.Attempt.SessionID)
	rl.wasRefused(t)
	decisionWhen(t, ld, d.ID, "refunded", hasKind(ledger.HistoryRefunded))
	rows, err := ld.Attempts(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rows, func(a ledger.AttemptRow) bool { return a.TaskID == "build" && a.N == 1 })
	if i < 0 || rows[i].State != ledger.AttemptFailed || rows[i].Stage != stageDecision {
		t.Fatalf("build's attempt 1 in the ledger: %+v", rows)
	}
	waitGivenBack(t, e, run.ID, "build", 1, d.ID)
	if x := extraOf(t, e, run.ID, "build"); x != 1 || !paused(t, e, run.ID) {
		t.Fatalf("extra %d, paused %v", x, paused(t, e, run.ID))
	}
	stopRun(t, e, run.ID)
}

// The run's end, its commit refused: the decisions it cancels are
// cancelled in the ledger, with its words, by the pause's commit, as the
// engine has them.
func TestARefusedRunEndIsCarried(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	rl := refusing(e, ld)
	note := PackageDecision{ID: "note", Question: "Note the release?", Kind: ledger.DecisionClarification}
	run := launchGated(t, e, dir, "api-sweep-000000f3", repoCrew(dir, work("build", "lead")), note)
	g := gateOf(t, e, ld, run.ID)
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	rl.arm("run.finished")
	commitResult(t, e, b, "built.txt", "b\n", "built")
	rl.wasRefused(t)
	rec := decisionWhen(t, ld, g.ID, "cancelled", inState(ledger.DecisionCancelled))
	if want := "the run ended, succeeded, before " + g.ID + " was answered"; rec.Why != want {
		t.Fatalf("%s's words: %q, want %q", g.ID, rec.Why, want)
	}
	if row, ok, err := ld.Run(t.Context(), run.ID); err != nil || !ok || row.Outcome != ledger.OutcomeSucceeded {
		t.Fatalf("the run in the ledger: %+v %v", row, err)
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	if _, ok := engineHolds(t, e, run.ID, g.ID); ok {
		t.Fatalf("the engine still holds %s", g.ID)
	}
}

// A gate's application, its commit refused: applied in the ledger by the
// pause's commit, its task released in both; the pause keeps it from
// starting until a person unpauses.
func TestARefusedGateApplicationIsCarried(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	rl := refusing(e, ld)
	run := launchGated(t, e, dir, "api-sweep-000000f4", repoCrew(dir, work("build", "lead"), work("deploy", "ops")), shipGate("deploy"))
	g := gateOf(t, e, ld, run.ID)
	rl.arm("decision.applied")
	answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	rl.wasRefused(t)
	rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
	if app := applied(t, rec); app.How != HowReleased || kindCount(rec, ledger.HistoryApplied) != 1 {
		t.Fatalf("the application: %+v %q", app, historyKinds(rec))
	}
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("the ledger's holds %q", got)
	}
	if ts := taskOf(t, e, run.ID, "deploy"); len(ts.Holds) != 0 || ts.Attempts != 0 || !paused(t, e, run.ID) {
		t.Fatalf("deploy: %+v, paused %v", ts, paused(t, e, run.ID))
	}
	if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "deploy", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// A new candidate's supersession, the submit's commit refused: the submit
// is put back (the attempt runs again), the supersession is not (the
// engine let the decision go), and the next commit writes it: the ledger
// holds the approval superseded once, its hold released.
func TestARefusedSubmitKeepsItsSupersession(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	rl := refusing(e, ld)
	run, w, c1 := candidateRun(t, e, fl, dir)
	d := ask(t, e, w.Attempt.SessionID, approveCandidate(run.ID, c1))
	b2 := attemptLine(t, e, fl, run.ID, "build", 2)
	writeFile(t, filepath.Join(b2.Attempt.Worktree, "ok.txt"), "ok\n")
	commitAll(t, b2.Attempt.Worktree, "second")
	rl.arm("decision.superseded")
	if _, err := e.SubmitResult(t.Context(), b2.Attempt.SessionID, Result{Summary: "second"}); err == nil {
		t.Fatal("a submit whose commit was refused answered")
	}
	rl.wasRefused(t)
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempt.N != 2 || ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("build after the refused submit: %+v", ts.Attempt)
	}
	if _, ok := engineHolds(t, e, run.ID, d.ID); ok {
		t.Fatalf("the engine still holds %s", d.ID)
	}
	submit(t, e, b2.Attempt.SessionID, Result{Summary: "second"})
	rec := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	if kindCount(rec, ledger.HistorySuperseded) != 1 {
		t.Fatalf("%s's history: %q", d.ID, historyKinds(rec))
	}
	if got := activeHolds(t, ld, run.ID); len(got) != 0 {
		t.Fatalf("the ledger's holds %q", got)
	}
	stopRun(t, e, run.ID)
}

// A continuation whose first commit is refused makes nothing: the
// decision stays the run continued's with its hold, its recovered record
// says so, and nothing is given back; continued again, the decision moves
// once and the interrupted attempt is given back once.
func TestARefusedContinuationMakesNothing(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	rl := refusing(e2, ld2)
	rl.arm("run.continued")
	if _, err := e2.ContinueRun(t.Context(), run.ID, ""); err == nil {
		t.Fatal("a continuation whose first commit was refused answered")
	}
	rl.wasRefused(t)
	rec := decisionWhen(t, ld2, d.ID, "open", inState(ledger.DecisionOpen))
	if rec.CurrentRun != run.ID || kindCount(rec, ledger.HistoryRefunded) != 0 {
		t.Fatalf("%s in %s: %q", d.ID, rec.CurrentRun, historyKinds(rec))
	}
	if got := activeHolds(t, ld2, run.ID); !slices.Equal(got, []string{"build " + d.ID}) {
		t.Fatalf("the ledger's holds %q", got)
	}
	from, ok, err := SQLLedger(ld2).Load(t.Context(), run.ID)
	if err != nil || !ok {
		t.Fatalf("%s: %v", run.ID, err)
	}
	for _, ts := range Recovered(Run{}, from).Tasks {
		if ts.ID == "build" && (len(ts.Holds) != 1 || ts.Holds[0].ID != d.ID) {
			t.Fatalf("the recovered record's holds: %+v", ts.Holds)
		}
	}
	next := continueRun(t, e2, run.ID)
	rec = decisionWhen(t, ld2, d.ID, "moved", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == next.ID })
	if kindCount(rec, ledger.HistoryRefunded) != 1 || extraOf(t, e2, next.ID, "build") != 1 {
		t.Fatalf("%s: %q, extra %d", d.ID, historyKinds(rec), extraOf(t, e2, next.ID, "build"))
	}
	if _, ok := engineHolds(t, e2, next.ID, d.ID); !ok {
		t.Fatalf("the continuation does not hold %s", d.ID)
	}
	stopRun(t, e2, next.ID)
}
