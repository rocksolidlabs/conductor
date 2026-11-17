package crew

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// holdLedger makes the fake ledger tell blocked and wait for release when
// a held transaction comes (blockOn, holdWriter), and returns the release,
// safe to call more than once and called at the test's end.
func holdLedger(t *testing.T, l *fakeLedger) (blocked <-chan struct{}, release func()) {
	t.Helper()
	b, r := make(chan struct{}, 1), make(chan struct{})
	l.mu.Lock()
	l.blocked, l.release = b, r
	l.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(r) }) }
	t.Cleanup(release)
	return b, release
}

// await waits for ch, failing the test after a bound that only bounds a
// hang.
func await[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: not after 30 s", what)
	}
	var zero T
	return zero
}

// A result whose record is held in the ledger and then refused is no
// result to preview: the open is reserved once the run's transaction under
// way is done (the run's txMu), sees no result and is refused, and no
// checkout is made at a commit the ledger never recorded.
func TestAPreviewWaitsForASubmitBeingRecorded(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	blocked, release := holdLedger(t, l)
	l.mu.Lock()
	l.blockOn, l.failOn, l.failOnce = "result.submitted", "result.submitted", true
	l.mu.Unlock()
	submitted := make(chan error, 1)
	go func() {
		_, err := e.SubmitResult(context.Background(), a.Attempt.SessionID, Result{Summary: "built"})
		submitted <- err
	}()
	await(t, "the submit's record held", blocked)
	// The open has looked its run up while the submit's record is held; the
	// record goes on, and is refused.
	e.afterLookup = func(string) { release() }
	opened := make(chan error, 1)
	go func() {
		_, err := e.OpenPreview(context.Background(), run.ID, "build", 1, "admin")
		opened <- err
	}()
	err := await(t, "the open", opened)
	release()
	if err := await(t, "the submit", submitted); err == nil {
		t.Fatal("the submit was recorded")
	}
	if !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("a preview of a result being recorded and refused: %v", err)
	}
	if _, err := os.Lstat(a.Attempt.Worktree + ".preview"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a checkout was made: %v", err)
	}
	if _, ok := l.previewRow(run.ID, "build", 1, 1); ok || previewHeld(e, run.ID, "build", 1) {
		t.Fatal("the refused open left a row or a preview")
	}
	stopRun(t, e, run.ID)
}

// A run continued between the preview's reservation and its first
// transaction (here as the preview's port is drawn, before the change) is
// refused there as a continued run, nothing written and nothing made.
func TestAPreviewOfARunContinuedAsItOpensIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	draw := e.exec.port
	var next *Run
	e.exec.port = func(ctx context.Context, ref ext.ProcessRef) (int, error) {
		if ref.Tree == ledger.TreePreview && next == nil {
			n, err := e.ContinueRun(ctx, run.ID, "")
			if err != nil {
				t.Errorf("the continuation: %v", err)
			}
			next = n
		}
		return draw(ctx, ref)
	}
	_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if !errors.Is(err, ErrRunContinued) || refusalOf(err) != RefusalRunContinued {
		t.Fatalf("a preview of a run continued as it opened: %v", err)
	}
	if w := l.writers(run.ID, "build", 1, 1); len(w) != 0 || previewHeld(e, run.ID, "build", 1) {
		t.Fatalf("the refused open wrote %v or is held", w)
	}
	if _, err := os.Lstat(ts.Attempt.Worktree + ".preview"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a checkout was made: %v", err)
	}
	if next != nil {
		stopRun(t, e, next.ID)
	}
}

// The preview's URL is offered only once its readiness is committed: held
// before its commit, neither the label nor the attempt's Previewing offers
// it, and it is offered once the commit is in; a readiness the ledger
// refuses is never offered, and the preview ends not ready, its service
// stopped.
func TestAPreviewIsOfferedOnceItsReadinessIsCommitted(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	offered := func(seq int) string {
		t.Helper()
		var words []string
		if label, ok := e.ReadPreview(run.ID, "build", 1, seq); ok && (label.Preview != "" || label.State == ledger.PreviewReady) {
			words = append(words, "the label "+label.State+" "+label.Preview)
		}
		if pv := taskOf(t, e, run.ID, "build").Attempt.Previewing; pv != nil && (pv.Preview != "" || pv.State == ledger.PreviewReady) {
			words = append(words, "Previewing "+pv.State+" "+pv.Preview)
		}
		return strings.Join(words, "; ")
	}
	for _, refuse := range []bool{false, true} {
		seq := map[bool]int{false: 1, true: 2}[refuse]
		blocked, release := holdLedger(t, l)
		l.mu.Lock()
		l.blockOn = "preview.ready"
		if refuse {
			l.failOn, l.failOnce = "preview.ready", true
		}
		l.mu.Unlock()
		// The reused checkout keeps seq 1's pid file: gone, the file read
		// is the service's of this seq.
		os.Remove(stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
		openPreview(t, e, run.ID)
		await(t, "the readiness held", blocked)
		if words := offered(seq); words != "" {
			t.Fatalf("seq %d, its readiness not committed: %s", seq, words)
		}
		pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
		l.mu.Lock()
		l.blockOn = ""
		l.mu.Unlock()
		release()
		if !refuse {
			// The ledger's row is committed before the engine publishes the
			// address (the commit's done): the label is waited for.
			l.previewWhen(t, run.ID, "build", 1, seq, "ready", previewReadyRow)
			waitFake(t, "the label offering the committed readiness", func() bool {
				label, ok := e.ReadPreview(run.ID, "build", 1, seq)
				return ok && label.Preview != "" && label.State == ledger.PreviewReady
			})
			if err := e.ClosePreview(run.ID, "build", 1, seq); err != nil {
				t.Fatal(err)
			}
		} else {
			row := l.previewWhen(t, run.ID, "build", 1, seq, "ending", func(r ledger.PreviewRow) bool { return strings.HasPrefix(r.Why, "not ready at ready") })
			if words := offered(seq); words != "" || row.Preview != "" {
				t.Fatalf("seq %d, its readiness refused: %s %+v", seq, words, row)
			}
		}
		if row := l.previewWhen(t, run.ID, "build", 1, seq, "ended", previewEndedRow); alive(pid) {
			t.Fatalf("seq %d ended %+v with its service %d alive", seq, row, pid)
		}
		l.previewWhen(t, run.ID, "build", 1, seq, "ended", previewEndedRow)
		waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	}
}

// A Close whose first write the ledger refuses signals nothing: no
// service is stopped, the row and both readers still offer the preview,
// and the Close answers the refusal. A Close after it ends the preview.
func TestAPreviewCloseTheLedgerRefusesSignalsNothing(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	stopping, openStops := gateStops(t, e)
	openPreview(t, e, run.ID)
	ready := l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	l.mu.Lock()
	l.failWriter = "PreviewEnding"
	l.mu.Unlock()
	if err := e.ClosePreview(run.ID, "build", 1, 1); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("a Close the ledger refused: %v", err)
	}
	select {
	case <-stopping:
		t.Fatal("a service was signalled after a refused Close")
	default:
	}
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.State != ledger.PreviewReady || row.Preview != ready.Preview || row.Why != "" {
		t.Fatalf("the row after a refused Close: %+v", row)
	}
	if label, ok := e.ReadPreview(run.ID, "build", 1, 1); !ok || label.Preview != ready.Preview {
		t.Fatalf("the label after a refused Close: %+v %v", label, ok)
	}
	if pv := taskOf(t, e, run.ID, "build").Attempt.Previewing; pv == nil || pv.State != ledger.PreviewReady || pv.Preview != ready.Preview {
		t.Fatalf("Previewing after a refused Close: %+v", pv)
	}
	if !alive(pid) {
		t.Fatalf("the service %d is gone after a refused Close", pid)
	}
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	n := r.trees.n
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d trees counted as ending after a refused Close", n)
	}
	openStops()
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyPreviewClosed {
		t.Fatalf("the ended row: %+v", row)
	}
}

// The step that makes a preview ending counts its tree's end too: with the
// Close's first write held in its transaction, the preview reads ending
// and its end is counted, so a Drain that comes then waits for it and
// returns with the service gone and the row ended.
func TestAPreviewCloseAndADrainAtOnce(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	blocked, release := holdLedger(t, l)
	l.mu.Lock()
	l.holdWriter = "PreviewEnding"
	l.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- e.ClosePreview(run.ID, "build", 1, 1) }()
	await(t, "the Close's first write held", blocked)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	state, n := r.task("build").attempts[0].previewing.state, r.trees.n
	e.mu.Unlock()
	if state != previewEnding || n != 1 {
		t.Fatalf("the preview %q with %d ends counted, its first write held", state, n)
	}
	drained := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		e.Drain(ctx)
		close(drained)
	}()
	release()
	if err := await(t, "the Close", closed); err != nil {
		t.Fatal(err)
	}
	await(t, "the Drain", drained)
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.State != ledger.PreviewEnded || row.Why != whyPreviewClosed {
		t.Fatalf("the row once the Drain returned: %+v", row)
	}
	if alive(pid) {
		t.Fatalf("the preview's service %d outlived the Drain", pid)
	}
}

// A run's stop under a context already ended stops a preview's service in
// the same synchronous path as an attempt's: gone when the stop returns.
func TestAPreviewServiceStopsWithAStopUnderAnEndedContext(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	short, cancel := context.WithCancel(context.Background())
	cancel()
	_ = e.Stop(short, run.ID)
	if alive(pid) {
		t.Fatalf("the preview's service %d outlived a stop under an ended context", pid)
	}
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyRunStopped {
		t.Fatalf("the ended row: %+v", row)
	}
	waitTreesEnded(t, e, run.ID)
}

// A run's stop tells its members to stop before any write of its previews:
// with the preview's readiness held in the ledger, a live member's session
// ends while the hold lasts, and the preview ends with the run once it is
// let go.
func TestAStopTellsAMemberToStopWhileAPreviewWaitsOnTheLedger(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoManifestCrew(dir, previewManifest(), work("build", "lead"), work("docs", "writer")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	d := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	commitResult(t, e, b, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 0)
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
	member, _ := fl.attemptSession(t, d)
	blocked, release := holdLedger(t, l)
	l.mu.Lock()
	l.blockOn = "preview.ready"
	l.mu.Unlock()
	openPreview(t, e, run.ID)
	await(t, "the preview's readiness held", blocked)
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop(context.Background(), run.ID) }()
	await(t, "the member's session ended while the ledger is held", member.Ended())
	l.mu.Lock()
	l.blockOn = ""
	l.mu.Unlock()
	release()
	if err := await(t, "the stop", stopped); err != nil {
		t.Fatal(err)
	}
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyRunStopped {
		t.Fatalf("the ended row: %+v", row)
	}
}

// The idle end is judged as it is made: a read at the very moment the
// idle timer fires keeps the preview open, whether the read comes before
// the timer or after it, before the end's change (the engine's seam once
// the timer has fired), and the timer is set again for a whole
// previewIdle.
func TestAPreviewReadAsItsIdleTimerFiresStaysOpen(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	clock := &fakeClock{now: time.Now()}
	e.now = clock.Now
	type wait struct {
		d    time.Duration
		fire chan time.Time
	}
	waits := make(chan wait, 4)
	e.idleTimer = func(d time.Duration) <-chan time.Time {
		w := wait{d, make(chan time.Time)}
		waits <- w
		return w.fire
	}
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	read := func() {
		t.Helper()
		if _, ok := e.ReadPreview(run.ID, "build", 1, 1); !ok {
			t.Error("the label is not read")
		}
	}
	for _, readFirst := range []bool{true, false} {
		w := await(t, "the idle timer set", waits)
		if w.d != previewIdle {
			t.Fatalf("the idle wait: %s", w.d)
		}
		clock.add(previewIdle)
		if readFirst {
			read()
			w.fire <- clock.Now()
		} else {
			// Woken, the idle end reads at the seam before its change: the
			// read comes after the timer and before the end.
			e.beforeIdleEnd = func() { e.beforeIdleEnd = nil; read() }
			w.fire <- clock.Now()
		}
	}
	if w := await(t, "the idle timer set again", waits); w.d != previewIdle {
		t.Fatalf("the idle wait after the reads: %s", w.d)
	}
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.State != ledger.PreviewReady {
		t.Fatalf("the preview read as its timer fired: %+v", row)
	}
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
}

// gateCheckoutGit puts a git first on PATH that, for the command between
// the two of a preview checkout's add (rev-parse --absolute-git-dir in a
// *.preview folder), makes the file arrived and waits for the file gate,
// or for the test's folder of marks to be gone (a test that ended with
// the add held leaves no git waiting), then runs the real git.
func gateCheckoutGit(t *testing.T) (arrived, gate string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, marks := t.TempDir(), t.TempDir()
	arrived, gate = filepath.Join(marks, "arrived"), filepath.Join(marks, "gate")
	script := "#!/bin/sh\ng=; p=\nfor a in \"$@\"; do case \"$a\" in --absolute-git-dir) g=1;; *.preview) p=1;; esac; done\n" +
		"if [ \"$g$p\" = 11 ]; then : > '" + arrived + "'; while [ ! -e '" + gate + "' ] && [ -d '" + marks + "' ]; do sleep 0.05; done; fi\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { open(t, gate) })
	return arrived, gate
}

// An end that comes while the preview's checkout is being added (held
// between the add's two git commands) waits for the add: the add
// finishes, the preview ends once, and the checkout is whole, as an open
// would judge it; no open follows a run's stop or a Drain. A run's stop
// and a Drain (a Close is answered busy meanwhile:
// TestAClosedPreviewWhileItOpensIsBusy).
func TestAnEndDuringThePreviewsCheckoutWaitsForIt(t *testing.T) {
	for _, end := range []string{"stop", "drain"} {
		t.Run(end, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
			arrived, gate := gateCheckoutGit(t)
			opened := make(chan error, 1)
			go func() {
				_, err := e.OpenPreview(context.Background(), run.ID, "build", 1, "admin")
				opened <- err
			}()
			waitFake(t, "the checkout's add held", func() bool { _, err := os.Stat(arrived); return err == nil })
			why := map[string]string{"stop": whyRunStopped, "drain": whyShutdown}[end]
			ended := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				switch end {
				case "stop":
					ended <- e.Stop(ctx, run.ID)
				default:
					e.Drain(ctx)
					ended <- nil
				}
			}()
			l.previewWhen(t, run.ID, "build", 1, 1, "ending", func(r ledger.PreviewRow) bool { return r.Why == why })
			open(t, gate)
			if err := await(t, "the end", ended); err != nil {
				t.Fatal(err)
			}
			if err := await(t, "the open", opened); err != nil {
				t.Fatalf("the open an end came during: %v", err)
			}
			l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
			if w := l.writers(run.ID, "build", 1, 1); !slices.Equal(w, []string{"OpenPreview", "PreviewEnding", "EndPreview"}) {
				t.Fatalf("the preview's writes: %v", w)
			}
			waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
			checkout := ts.Attempt.Worktree + ".preview"
			// A stopped run, and a drained engine, open no preview: the
			// checkout is judged as an open would judge it.
			if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); !errors.Is(err, ErrRunStopped) {
				t.Fatalf("an open after the %s: %v", end, err)
			}
			if reused, err := previewReusable(t.Context(), dir, checkout, ts.Attempt.Result); err != nil || !reused {
				t.Fatalf("the checkout after the %s: %v, %v", end, reused, err)
			}
		})
	}
}

// A Close of a preview whose open is under way, while its checkout is
// being added (held between the add's git commands) and once the add has
// returned (held at the engine's seam, before the open hands it over), is
// answered busy at once, and nothing of the preview changes: its state,
// its row, its writes. The open then completes and serves it, and a Close
// after that ends it.
func TestAClosedPreviewWhileItOpensIsBusy(t *testing.T) {
	for _, at := range []string{"during the add", "after the add"} {
		t.Run(at, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
			held, release := make(chan struct{}, 1), make(chan struct{})
			var once sync.Once
			let := func() { once.Do(func() { close(release) }) }
			t.Cleanup(let)
			var arrived, gate string
			if at == "during the add" {
				arrived, gate = gateCheckoutGit(t)
			} else {
				e.checkoutMade = func(string) {
					held <- struct{}{}
					<-release
				}
			}
			opened := make(chan error, 1)
			go func() {
				_, err := e.OpenPreview(context.Background(), run.ID, "build", 1, "admin")
				opened <- err
			}()
			if at == "during the add" {
				waitFake(t, "the checkout's add held", func() bool { _, err := os.Stat(arrived); return err == nil })
			} else {
				await(t, "the open held after the add", held)
			}
			if err := e.ClosePreview(run.ID, "build", 1, 1); !errors.Is(err, ErrPreviewOpening) {
				t.Fatalf("a Close while the preview opens: %v", err)
			}
			if pv := taskOf(t, e, run.ID, "build").Attempt.Previewing; pv == nil || pv.State != ledger.PreviewResolved {
				t.Fatalf("the preview after the Close: %+v", pv)
			}
			if w := l.writers(run.ID, "build", 1, 1); !slices.Equal(w, []string{"OpenPreview"}) {
				t.Fatalf("the preview's writes after the Close: %v", w)
			}
			if at == "during the add" {
				open(t, gate)
			} else {
				let()
			}
			if err := await(t, "the open", opened); err != nil {
				t.Fatalf("the open: %v", err)
			}
			l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
			if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
				t.Fatal(err)
			}
			if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyPreviewClosed {
				t.Fatalf("the ended row: %+v", row)
			}
			if w := l.writers(run.ID, "build", 1, 1); !slices.Equal(w, []string{"OpenPreview", "PreviewReady", "PreviewEnding", "EndPreview"}) {
				t.Fatalf("the preview's writes: %v", w)
			}
		})
	}
}

// A preview of a result whose crew's directory is a file or a link is
// refused as verify refused that result at its checkout, in its words:
// the open's error and the ended row say them, nothing is made where the
// link leads, and the preview is let go.
func TestAPreviewOfAResultThatBreaksTheCrewsDirectoryIsRefused(t *testing.T) {
	for _, how := range []string{"file", "link"} {
		t.Run(how, func(t *testing.T) {
			outside := t.TempDir()
			e, l, run, _ := brokenCandidate(t, how, outside)
			words := l.attemptRow(t, run.ID, "build", 1).Error
			if !strings.Contains(words, "in the member's worktree is a") {
				t.Fatalf("verify's refusal: %q", words)
			}
			if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); err == nil || !strings.Contains(err.Error(), words) {
				t.Fatalf("the open: %v, want %q", err, words)
			}
			l.previewWhen(t, run.ID, "build", 1, 1, "ended", func(r ledger.PreviewRow) bool {
				return r.State == ledger.PreviewEnded && r.Why == "the checkout could not be made: "+words
			})
			if made, _ := os.ReadDir(outside); len(made) != 0 {
				t.Fatalf("made where the link leads: %v", made)
			}
			waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
		})
	}
}
