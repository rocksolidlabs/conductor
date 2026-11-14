package crew

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
// preview's services are stopped, under a context with no deadline; the
// owed row is written later with the shutdown's reason.
func TestADrainReturnsWhileAPreviewsEndIsOwed(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	waits := previewWaits(e)
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
	retry := retryWait(t, waits, nil)
	takeEnds(l)
	retry.fire <- time.Now()
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyShutdown {
		t.Fatalf("the ended row: %+v", row)
	}
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
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
	e.awaitingStart = func(id string) {
		if id != "api" {
			return
		}
		once.Do(func() { close(gate) })
		for deadline := time.Now().Add(10 * time.Second); !ending(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Error("the preview's end did not begin")
				return
			}
		}
	}
	openPreview(t, e, run.ID)
	await(t, "the second service's start held", arrived)
	if err := e.Stop(t.Context(), run.ID); err != nil {
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
