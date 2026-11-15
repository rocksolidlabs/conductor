package crew

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proc"
)

// timerWait is one wait the engine's preview timer set: its duration, and
// the channel that fires it (buffered, so a wait no one is on any more
// takes its fire too).
type timerWait struct {
	d    time.Duration
	fire chan time.Time
}

// previewWaits makes the engine's preview timer (the idle end's, an owed
// write's next try) the test's: each wait set comes on the channel.
func previewWaits(e *Engine) <-chan timerWait {
	waits := make(chan timerWait, 16)
	e.idleTimer = func(d time.Duration) <-chan time.Time {
		w := timerWait{d, make(chan time.Time, 1)}
		waits <- w
		return w.fire
	}
	return waits
}

// retryWait waits for the wait of an owed write's next try, letting each
// other wait (the idle end's) be; each(w) is called with every wait first.
func retryWait(t *testing.T, waits <-chan timerWait, each func(timerWait)) timerWait {
	t.Helper()
	for i := 0; i < 8; i++ {
		w := await(t, "a wait of the preview's timer", waits)
		if each != nil {
			each(w)
		}
		if w.d == previewRetry {
			return w
		}
	}
	t.Fatal("no owed write is tried again: eight waits of the timer, none of them a retry's")
	return timerWait{}
}

// refuseEnds has the fake ledger refuse a preview's end: its first write
// once (PreviewEnding) and its last (preview.ended) until takeEnds.
func refuseEnds(l *fakeLedger) {
	l.mu.Lock()
	l.failWriter, l.failOn = "PreviewEnding", "preview.ended"
	l.mu.Unlock()
}

func takeEnds(l *fakeLedger) {
	l.mu.Lock()
	l.failWriter, l.failOn = "", ""
	l.mu.Unlock()
}

// endedInMemory fails the test unless the candidate's preview seq is held
// ending with no address, whatever its row says: the label refuses it and
// attempt 1's Previewing says ending, with no URL.
func endedInMemory(t *testing.T, e *Engine, runID string, seq int) {
	t.Helper()
	if label, ok := e.ReadPreview(runID, "build", 1, seq); ok {
		t.Fatalf("the label of an ended preview: %+v", label)
	}
	r := runOf(t, e, runID)
	e.mu.Lock()
	pv := r.attemptState(r.task("build").attempts[0]).Previewing
	e.mu.Unlock()
	if pv == nil || pv.State != previewEnding || pv.Preview != "" {
		t.Fatalf("Previewing of an ended preview: %+v", pv)
	}
	if !previewHeld(e, runID, "build", 1) {
		t.Fatal("the preview is not counted while its row is owed")
	}
}

// brokenCandidate is attempt 1 of build whose result makes the crew's
// directory, app, a file or a link to the folder outside (how), which
// verify refused at its checkout; attempt 2 runs.
func brokenCandidate(t *testing.T, how, outside string) (*Engine, *fakeLedger, Run, TaskState) {
	t.Helper()
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "app", "app.txt"), "app\n")
	commitAll(t, dir, "app")
	e, _, l := managedEngine(t)
	c := repoManifestCrew(dir, previewManifest(), work("build", "lead"))
	c.Cwd = filepath.Join(dir, "app")
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	app := filepath.Join(a.Attempt.Worktree, "app")
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	if how == "file" {
		writeFile(t, app, "a file\n")
	} else if err := os.Symlink(outside, app); err != nil {
		t.Fatal(err)
	}
	commitAll(t, a.Attempt.Worktree, "app is a "+how+" now")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "broken"})
	waitTaskWhere(t, e, run.ID, "build", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	t.Cleanup(func() { stopRun(t, e, run.ID) })
	return e, l, run, a
}

// A preview whose checkout cannot be made, with its ending writes
// refused, is ended in the engine's memory at once and stays counted (a
// second open is refused as open); once the ledger takes writes its owed
// row is written on the next try, it is no longer counted, and a second
// open of the candidate is judged again.
func TestAPreviewWhoseCheckoutFailsEndsWhateverTheLedgerAnswers(t *testing.T) {
	e, l, run, _ := brokenCandidate(t, "file", "")
	waits := previewWaits(e)
	refuseEnds(l)
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); err == nil {
		t.Fatal("the open whose checkout could not be made went on")
	}
	endedInMemory(t, e, run.ID, 1)
	retry := retryWait(t, waits, nil)
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); !errors.Is(err, ErrPreviewOpen) {
		t.Fatalf("a second open while the first's row is owed: %v", err)
	}
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.State == ledger.PreviewEnded {
		t.Fatalf("the row ended while the ledger refused it: %+v", row)
	}
	takeEnds(l)
	retry.fire <- time.Now()
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); !strings.HasPrefix(row.Why, "the checkout could not be made") {
		t.Fatalf("the ended row: %+v", row)
	}
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); err == nil || errors.Is(err, ErrPreviewOpen) {
		t.Fatalf("a second open once the first's row is written: %v", err)
	}
	l.previewWhen(t, run.ID, "build", 1, 2, "ended", previewEndedRow)
	waitFake(t, "the second preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
}

// A preview whose service is lost after it is ready, with its ending
// writes refused and its label read all the while, is ended at once and
// keeps the lost service's reason; its owed row is tried again whatever
// the reads, and written once the ledger takes it.
func TestAPreviewWhoseServiceIsLostEndsWhateverTheLedgerAnswers(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	waits := previewWaits(e)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	refuseEnds(l)
	syscall.Kill(pid, syscall.SIGKILL)
	// The label is read at every look, as the pane reads it; the idle
	// timer never fires (less than previewIdle passes).
	var retry timerWait
	waitFake(t, "the lost service's owed end tried again, the label read meanwhile", func() bool {
		e.ReadPreview(run.ID, "build", 1, 1)
		select {
		case w := <-waits:
			retry = w
		default:
		}
		return retry.d == previewRetry
	})
	endedInMemory(t, e, run.ID, 1)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	why := r.task("build").attempts[0].previewing.why
	e.mu.Unlock()
	if why != whyServiceExited {
		t.Fatalf("the reason kept: %q", why)
	}
	takeEnds(l)
	retry.fire <- time.Now()
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyServiceExited {
		t.Fatalf("the ended row: %+v", row)
	}
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
}

// A run's stop under a context with no deadline, its preview's ending
// writes refused, returns once the preview's services are stopped; the run
// is continued as after any stop; the owed row is written later with the
// stop's reason.
func TestAStopReturnsWhileAPreviewsEndIsOwed(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	waits := previewWaits(e)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	refuseEnds(l)
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop(context.Background(), run.ID) }()
	if err := await(t, "the stop", stopped); err != nil {
		t.Fatalf("the stop: %v", err)
	}
	if alive(pid) {
		t.Fatalf("the preview's service %d outlived the stop", pid)
	}
	retry := retryWait(t, waits, nil)
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.State == ledger.PreviewEnded {
		t.Fatalf("the row ended while the ledger refused it: %+v", row)
	}
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatalf("the stopped run's continuation while its preview's row is owed: %v", err)
	}
	takeEnds(l)
	retry.fire <- time.Now()
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyRunStopped {
		t.Fatalf("the ended row: %+v", row)
	}
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	stopRun(t, e, next.ID)
}

// A drain with a preview's ending writes refused returns once the
// preview's services are stopped, under a context with no deadline, and
// ends the engine: the owed write is not tried again (its timer fired,
// nothing writes), the preview is let go, and its row is left to the next
// start's recovery.
func TestADrainReturnsWhileAPreviewsEndIsOwed(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	waits := previewWaits(e)
	finished := make(chan struct{}, 1)
	e.previewFinished = func() { finished <- struct{}{} }
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	refuseEnds(l)
	drained := make(chan struct{})
	go func() {
		e.Drain(context.Background())
		close(drained)
	}()
	await(t, "the drain", drained)
	if alive(pid) {
		t.Fatalf("the preview's service %d outlived the drain", pid)
	}
	await(t, "the preview let go", finished)
	takeEnds(l)
	for len(waits) > 0 {
		(<-waits).fire <- time.Now()
	}
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.State == ledger.PreviewEnded {
		t.Fatalf("the owed row written after the drain: %+v", row)
	}
	if previewHeld(e, run.ID, "build", 1) {
		t.Fatal("the preview is held after the drain")
	}
}

// endRefusingLedger is the SQL ledger with a preview's last write
// (EndPreview) refused while refuse is set; tries counts those writes.
type endRefusingLedger struct {
	sqlLedger
	refuse atomic.Bool
	tries  atomic.Int32
}

func (l *endRefusingLedger) Transition(ctx context.Context, runID string, fn func(LedgerTx) error) (int64, error) {
	return l.l.Transition(ctx, runID, func(tx *ledger.Tx) error { return fn(endRefusingTx{tx, l}) })
}

type endRefusingTx struct {
	*ledger.Tx
	l *endRefusingLedger
}

func (tx endRefusingTx) EndPreview(r ledger.PreviewRow) error {
	tx.l.tries.Add(1)
	if tx.l.refuse.Load() {
		return errors.New("ledger: disk full (injected)")
	}
	return tx.Tx.EndPreview(r)
}

// stacks is every goroutine's stack, read once, by its id.
func stacks() map[int]string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	for n == len(buf) {
		buf = make([]byte, 2*len(buf))
		n = runtime.Stack(buf, true)
	}
	out := map[int]string{}
	for _, g := range strings.Split(string(buf[:n]), "\n\n") {
		var id int
		if _, err := fmt.Sscanf(g, "goroutine %d ", &id); err == nil {
			out[id] = g
		}
	}
	return out
}

// An owed write of a preview's end is tried again only while the engine
// lives and its ledger is open: after the drain, or once the ledger
// answers that it is closed, its retries stop (the timer fired, nothing
// tries), the preview is let go (not counted, its ports free), and no
// goroutine of its end or its serving waits on (read once, after the end
// has signalled its exit); the next start's recovery ends its row.
func TestAnOwedPreviewWriteEndsWithTheEngine(t *testing.T) {
	for _, how := range []string{"the drain", "the ledger closed"} {
		t.Run(how, func(t *testing.T) {
			dir := newRepo(t)
			e, _ := readyEngine(t)
			ldir := t.TempDir()
			ld, err := ledger.Open(ldir)
			if err != nil {
				t.Fatal(err)
			}
			rl := &endRefusingLedger{sqlLedger: sqlLedger{l: ld}}
			e.UseLedger(rl)
			waits := previewWaits(e)
			finished := make(chan struct{}, 1)
			e.previewFinished = func() { finished <- struct{}{} }
			run := launchManaged(t, e, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitResult(t, e, a, "built.txt", "yes\n", "built")
			waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
			waitTreesEnded(t, e, run.ID)
			since := slices.Max(slices.Collect(maps.Keys(stacks())))
			openPreview(t, e, run.ID)
			ctx := context.Background()
			waitFake(t, "the preview ready", func() bool {
				ps, _ := ld.Previews(ctx, run.ID)
				return len(ps) == 1 && ps[0].State == ledger.PreviewReady
			})
			r := runOf(t, e, run.ID)
			e.mu.Lock()
			pt := r.task("build").attempts[0].previewing.t
			e.mu.Unlock()
			rl.refuse.Store(true)
			if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
				t.Fatal(err)
			}
			retry := retryWait(t, waits, nil)
			tries := rl.tries.Load()
			if how == "the drain" {
				e.Drain(ctx)
				await(t, "the preview let go", finished)
				if err := ld.Close(); err != nil {
					t.Fatal(err)
				}
				retry.fire <- time.Now()
			} else {
				if err := ld.Close(); err != nil {
					t.Fatal(err)
				}
				// The try the timer lets go is answered closed before any
				// write, and is the last.
				retry.fire <- time.Now()
				await(t, "the preview let go", finished)
			}
			if n := rl.tries.Load(); n != tries {
				t.Fatalf("%d tries of the owed write, want %d", n, tries)
			}
			if len(retry.fire) != 1 && how == "the drain" {
				t.Fatal("the retry's timer was taken after the drain")
			}
			if len(waits) > 0 {
				t.Fatal("a next try was set once the retries stopped")
			}
			e.mu.Lock()
			counted, ends := r.previews, r.trees.n
			var leased []int
			for n, x := range e.ports {
				if x == pt {
					leased = append(leased, n)
				}
			}
			e.mu.Unlock()
			if counted != 0 || ends != 0 || len(leased) != 0 || previewHeld(e, run.ID, "build", 1) {
				t.Fatalf("after the retries stopped: %d previews counted, %d ends, ports %v leased", counted, ends, leased)
			}
			for id, g := range stacks() {
				head, _, _ := strings.Cut(g, "\n")
				if id > since && !strings.Contains(head, "[running]") && !strings.Contains(head, "[runnable]") &&
					(strings.Contains(g, "(*Engine).finishPreview") || strings.Contains(g, "(*Engine).servePreview") || strings.Contains(g, "(*Engine).endNow")) {
					t.Fatalf("a goroutine of the preview left:\n%s", g)
				}
			}
			again, err := ledger.Open(ldir)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			rec, err := again.Recover(ctx)
			if err != nil || len(rec) != 1 || rec[0].Previews != 1 {
				t.Fatalf("the recovery: %+v %v", rec, err)
			}
			ps, _ := again.Previews(ctx, run.ID)
			if len(ps) != 1 || ps[0].State != ledger.PreviewEnded || !ps[0].Interrupted || ps[0].Why != "the owner stopped before the preview was ended" {
				t.Fatalf("the preview after the recovery: %+v", ps)
			}
			if how != "the drain" {
				e.Drain(ctx)
			}
		})
	}
}

// A run stopped while its preview's second service is starting (its start
// held in the executor until the stop waits for it, the stop then held
// until the preview's end has begun, whichever began it) ends the preview
// once, for the run's stop: no failure of the start that the stop caused
// is the preview's reason.
func TestAStopWhileAPreviewsServiceStartsRecordsTheStop(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	m := previewManifest()
	m.Services = append(m.Services, serviceLoop("api", "api.pid", false))
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, m, work("build", "lead")))
	arrived, gate := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(gate) }) })
	serve := e.exec.serve
	e.exec.serve = func(ctx context.Context, ref ext.ProcessRef, s proc.Spec) (service, error) {
		if ref.Tree == ledger.TreePreview && strings.Contains(strings.Join(s.Argv, " "), "api.pid") {
			arrived <- struct{}{}
			<-gate
		}
		return serve(ctx, ref, s)
	}
	ending := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		p := e.previews[previewKey{run.ID, "build", 1}]
		return p != nil && p.closed()
	}
	// The stop's wait for the start (the engine's seam) tells the test
	// whether the preview's end had begun by then, and waits for the test.
	waited, resume := make(chan bool, 1), make(chan struct{})
	var first, resumed sync.Once
	let := func() { resumed.Do(func() { close(resume) }) }
	t.Cleanup(let)
	e.awaitingStart = func(id string) {
		if id != "api" {
			return
		}
		first.Do(func() {
			once.Do(func() { close(gate) })
			waited <- ending()
			<-resume
		})
	}
	openPreview(t, e, run.ID)
	await(t, "the second service's start held", arrived)
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop(t.Context(), run.ID) }()
	if !await(t, "the stop's wait for the start", waited) {
		t.Error("the preview's end had not begun when the stop waited for its service's start")
	}
	let()
	if err := await(t, "the stop", stopped); err != nil {
		t.Fatal(err)
	}
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyRunStopped {
		t.Fatalf("the ended row: %+v", row)
	}
	if w := l.writers(run.ID, "build", 1, 1); !slices.Equal(w, []string{"OpenPreview", "PreviewEnding", "EndPreview"}) {
		t.Fatalf("the preview's writes: %v", w)
	}
	if p := l.payloads(run.ID, "preview.ended"); len(p) != 1 || p[0]["why"] != whyRunStopped {
		t.Fatalf("preview.ended: %v", p)
	}
}

// A run whose worktrees folder is replaced by a link before the open is
// refused before any add, in the words an attempt's dispatch gets
// (checkWorktreesDir), nothing made where the link leads and nothing
// written.
func TestAPreviewOfARunWhoseWorktreesFolderIsALinkIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	worktrees, elsewhere := filepath.Join(dir, worktreesDir), t.TempDir()
	if err := os.Rename(worktrees, filepath.Join(t.TempDir(), "aside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, worktrees); err != nil {
		t.Fatal(err)
	}
	log := wrapGit(t)
	_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if want := checkWorktreesDir(dir); want == nil || !errors.Is(err, ErrInvalid) || err.Error() != want.Error() {
		t.Fatalf("a preview through a linked worktrees folder: %v, want %v", err, want)
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "worktree\x00add") {
		t.Fatal("a worktree was added")
	}
	if made, _ := os.ReadDir(elsewhere); len(made) != 0 {
		t.Fatalf("made where the link leads: %v", made)
	}
	if w := l.writers(run.ID, "build", 1, 1); len(w) != 0 || previewHeld(e, run.ID, "build", 1) {
		t.Fatalf("the refused open wrote %v or is held", w)
	}
}

// An add that outlives the checkout's bound (the bound ended through the
// engine's seam while the add is held between its git commands) ends the
// open with the bound's own error, and nothing is left counted: the
// preview's row ended, the preview dropped, no tree's end in flight.
func TestAPreviewCheckoutPastItsBoundEndsTheOpen(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	arrived, _ := gateCheckoutGit(t)
	bound, cut := context.WithCancelCause(context.Background())
	e.checkoutBound = func() (context.Context, context.CancelFunc) { return bound, func() {} }
	opened := make(chan error, 1)
	go func() {
		_, err := e.OpenPreview(context.Background(), run.ID, "build", 1, "admin")
		opened <- err
	}()
	waitFake(t, "the checkout's add held", func() bool { _, err := os.Stat(arrived); return err == nil })
	cut(errCheckoutBound)
	if err := await(t, "the open", opened); !errors.Is(err, errCheckoutBound) {
		t.Fatalf("an add past its bound: %v", err)
	}
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); !strings.Contains(row.Why, errCheckoutBound.Error()) {
		t.Fatalf("the ended row: %+v", row)
	}
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	waitTreesEnded(t, e, run.ID)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	n := r.previews
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d previews counted on the run", n)
	}
}
