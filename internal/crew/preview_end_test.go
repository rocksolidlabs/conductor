package crew

import (
	"context"
	"errors"
	"fmt"
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
// the attempt's Previewing says ending, with no URL.
func endedInMemory(t *testing.T, e *Engine, runID string, seq int) {
	t.Helper()
	if label, ok := e.ReadPreview(runID, "build", 1, seq); ok {
		t.Fatalf("the label of an ended preview: %+v", label)
	}
	if pv := taskOf(t, e, runID, "build").Attempt.Previewing; pv == nil || pv.State != previewEnding || pv.Preview != "" {
		t.Fatalf("Previewing of an ended preview: %+v", pv)
	}
	if !previewHeld(e, runID, "build", 1) {
		t.Fatal("the preview is not counted while its row is owed")
	}
}

// A preview whose checkout cannot be made, with its ending writes
// refused, is ended in the engine's memory at once and stays counted (a
// second open is refused as open); once the ledger takes writes its owed
// row is written on the next try, it is no longer counted, and a second
// open of the candidate works.
func TestAPreviewWhoseCheckoutFailsEndsWhateverTheLedgerAnswers(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "app", "app.txt"), "app\n")
	commitAll(t, dir, "app")
	e, _, l := managedEngine(t)
	waits := previewWaits(e)
	c := repoManifestCrew(dir, previewManifest(), work("build", "lead"))
	c.Cwd = filepath.Join(dir, "sub", "app")
	run, ts := candidateOf(t, e, l, c)
	checkout := ts.Attempt.Worktree + ".preview"
	e.memberDirChecked = func(part string) {
		if part == "sub" {
			if err := os.Rename(filepath.Join(checkout, "sub"), filepath.Join(t.TempDir(), "sub")); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(checkout, "sub")); err != nil {
				t.Error(err)
			}
		}
	}
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
	e.memberDirChecked = nil
	runGit(t, dir, "worktree", "remove", "--force", checkout)
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 2, "ready", previewReadyRow)
	if err := e.ClosePreview(run.ID, "build", 1, 2); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 2, "ended", previewEndedRow)
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

// A run's worktrees folder replaced by a link before the open is refused
// before any add, nothing made where the link leads and nothing written.
func TestAPreviewOfARunWhoseWorktreesFolderIsALinkIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	runDir, elsewhere := filepath.Dir(ts.Attempt.Worktree), t.TempDir()
	if err := os.Rename(runDir, filepath.Join(t.TempDir(), "aside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, runDir); err != nil {
		t.Fatal(err)
	}
	log := wrapGit(t)
	_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if !errors.Is(err, ErrPreviewRefused) || !strings.Contains(err.Error(), "is not the run's own folders") {
		t.Fatalf("a preview through a linked worktrees folder: %v", err)
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

// reusable makes and ends a first preview of the candidate, so its
// checkout is there to be judged again, and returns it with the
// repository's entry for it.
func reusable(t *testing.T, e *Engine, l *fakeLedger, runID string, ts TaskState) (checkout, admin string) {
	t.Helper()
	openPreview(t, e, runID)
	l.previewWhen(t, runID, "build", 1, 1, "ready", previewReadyRow)
	if err := e.ClosePreview(runID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, runID, "build", 1, 1, "ended", previewEndedRow)
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, runID, "build", 1) })
	checkout = ts.Attempt.Worktree + ".preview"
	b, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	return checkout, strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir: "))
}

// The repository's entry for a found checkout is read by its name, once,
// whatever else the repository holds: an entry whose gitdir is a named
// pipe is refused at once; twenty thousand entries beside the right one
// leave one entry read.
func TestAFoundPreviewCheckoutsEntryIsReadByName(t *testing.T) {
	t.Run("a named pipe", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
		_, admin := reusable(t, e, l, run.ID, ts)
		pipe := filepath.Join(admin, "gitdir")
		if err := os.Remove(pipe); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(pipe, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// A reader blocked on the pipe is let go.
			if f, err := os.OpenFile(pipe, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				f.Close()
			}
		})
		opened := make(chan error, 1)
		go func() {
			_, err := e.OpenPreview(context.Background(), run.ID, "build", 1, "admin")
			opened <- err
		}()
		if err := await(t, "the open", opened); !errors.Is(err, ErrPreviewRefused) || !strings.Contains(err.Error(), "is not a worktree of the repository") {
			t.Fatalf("an entry whose gitdir is a named pipe: %v", err)
		}
	})
	t.Run("twenty thousand entries", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
		_, admin := reusable(t, e, l, run.ID, ts)
		for i := range 20000 {
			if err := os.Mkdir(filepath.Join(filepath.Dir(admin), fmt.Sprintf("x%05d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		var mu sync.Mutex
		var read []string
		previewEntryRead = func(name string) {
			mu.Lock()
			read = append(read, name)
			mu.Unlock()
		}
		t.Cleanup(func() { previewEntryRead = nil })
		openPreview(t, e, run.ID)
		l.previewWhen(t, run.ID, "build", 1, 2, "ready", previewReadyRow)
		mu.Lock()
		got := slices.Clone(read)
		mu.Unlock()
		if !slices.Equal(got, []string{filepath.Base(admin)}) {
			t.Fatalf("%d entries read, the first %q", len(got), got[:min(len(got), 3)])
		}
		if err := e.ClosePreview(run.ID, "build", 1, 2); err != nil {
			t.Fatal(err)
		}
		l.previewWhen(t, run.ID, "build", 1, 2, "ended", previewEndedRow)
	})
}

// A plain folder at the checkout's path whose .git is a hard link to the
// attempt's own worktree's .git is no worktree of the repository: the
// entry it names is the worktree's, whose folder is not this one.
func TestAPreviewFolderWhoseGitIsAnotherWorktreesIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	checkout := ts.Attempt.Worktree + ".preview"
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(ts.Attempt.Worktree, ".git"), filepath.Join(checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if !errors.Is(err, ErrPreviewRefused) || !strings.Contains(err.Error(), "is not a worktree of the repository") {
		t.Fatalf("a folder whose .git is another worktree's: %v", err)
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
