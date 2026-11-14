package crew

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// refuses is never offered, and the preview ends not ready.
func TestAPreviewIsOfferedOnceItsReadinessIsCommitted(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
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
		openPreview(t, e, run.ID)
		await(t, "the readiness held", blocked)
		if words := offered(seq); words != "" {
			t.Fatalf("seq %d, its readiness not committed: %s", seq, words)
		}
		l.mu.Lock()
		l.blockOn = ""
		l.mu.Unlock()
		release()
		if !refuse {
			l.previewWhen(t, run.ID, "build", 1, seq, "ready", previewReadyRow)
			if label, ok := e.ReadPreview(run.ID, "build", 1, seq); !ok || label.Preview == "" || label.State != ledger.PreviewReady {
				t.Fatalf("the label once the readiness is committed: %+v %v", label, ok)
			}
			if err := e.ClosePreview(run.ID, "build", 1, seq); err != nil {
				t.Fatal(err)
			}
		} else {
			row := l.previewWhen(t, run.ID, "build", 1, seq, "ending", func(r ledger.PreviewRow) bool { return strings.HasPrefix(r.Why, "not ready at ready") })
			if words := offered(seq); words != "" || row.Preview != "" {
				t.Fatalf("seq %d, its readiness refused: %s %+v", seq, words, row)
			}
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
// the timer or after it, before the end's change (the test holds the
// run's txMu there), and the timer is set again for a whole previewIdle.
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
	r := runOf(t, e, run.ID)
	read := func() {
		t.Helper()
		if _, ok := e.ReadPreview(run.ID, "build", 1, 1); !ok {
			t.Fatal("the label is not read")
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
			r.txMu.Lock()
			w.fire <- clock.Now()
			// Woken, the idle end waits for the run's txMu to make its
			// change: the read comes after the timer and before the end.
			waitFake(t, "the idle end waiting for the run's txMu", func() bool { return parkedOnALock("servePreview") })
			read()
			r.txMu.Unlock()
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

// parkedOnALock reports whether a goroutine with fn on its stack is
// parked on a sync.Mutex.
func parkedOnALock(fn string) bool {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "[sync.Mutex.Lock") && strings.Contains(g, fn) {
			return true
		}
	}
	return false
}

// gateCheckoutGit puts a git first on PATH that, for the command between
// the two of a preview checkout's add (rev-parse --absolute-git-dir in a
// *.preview folder), makes the file arrived and waits for the file gate,
// then runs the real git.
func gateCheckoutGit(t *testing.T) (arrived, gate string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, marks := t.TempDir(), t.TempDir()
	arrived, gate = filepath.Join(marks, "arrived"), filepath.Join(marks, "gate")
	script := "#!/bin/sh\ng=; p=\nfor a in \"$@\"; do case \"$a\" in --absolute-git-dir) g=1;; *.preview) p=1;; esac; done\n" +
		"if [ \"$g$p\" = 11 ]; then : > '" + arrived + "'; " + gated(gate) + "; fi\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { open(t, gate) })
	return arrived, gate
}

// An end that comes while the preview's checkout is being added (held
// between the add's two git commands) waits for the add: the add
// finishes, the preview ends once, and the checkout is whole, a second
// open of the candidate reusing it. A Close, a run's stop and a Drain.
func TestAnEndDuringThePreviewsCheckoutWaitsForIt(t *testing.T) {
	for _, end := range []string{"close", "stop", "drain"} {
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
			why := map[string]string{"close": whyPreviewClosed, "stop": whyRunStopped, "drain": whyShutdown}[end]
			ended := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				switch end {
				case "close":
					ended <- e.ClosePreview(run.ID, "build", 1, 1)
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
			if end == "stop" {
				// A stopped run opens no preview: the checkout is judged as
				// an open would judge it.
				if reused, err := previewReusable(t.Context(), dir, checkout, ts.Attempt.Result); !reused || err != nil {
					t.Fatalf("the checkout after the stop: reused %v, %v", reused, err)
				}
				return
			}
			openPreview(t, e, run.ID)
			l.previewWhen(t, run.ID, "build", 1, 2, "ready", previewReadyRow)
			if err := e.ClosePreview(run.ID, "build", 1, 2); err != nil {
				t.Fatal(err)
			}
			l.previewWhen(t, run.ID, "build", 1, 2, "ended", previewEndedRow)
		})
	}
}

// A checkout found at the preview's path is reused only when it is the
// repository's own worktree by the repository's word, judged before any
// git runs in or about it; refused otherwise, in fixed words naming it,
// and left as it is. Each case starts from a whole checkout a first
// preview made and ended.
func TestAFoundPreviewCheckoutIsTheRepositorysOwn(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, dir, checkout, commit string)
		words  string // "" reused
	}{
		{"a worktree of the repository at the commit", func(*testing.T, string, string, string) {}, ""},
		{"with a tracked change", func(t *testing.T, _, checkout, _ string) {
			writeFile(t, filepath.Join(checkout, "built.txt"), "changed\n")
		}, "has changes to tracked files: built.txt"},
		{"its .git naming a git directory of its own", func(t *testing.T, dir, checkout, commit string) {
			own := filepath.Join(t.TempDir(), "own")
			runGit(t, t.TempDir(), "clone", "-q", "--no-checkout", dir, own)
			runGit(t, own, "checkout", "-q", "--detach", commit)
			runGit(t, own, "config", "core.worktree", own)
			writeFile(t, filepath.Join(checkout, ".git"), "gitdir: "+filepath.Join(own, ".git")+"\n")
			writeFile(t, filepath.Join(checkout, "built.txt"), "changed\n")
		}, "is not a worktree of the repository"},
		{"its .git a link", func(t *testing.T, _, checkout, _ string) {
			moved := filepath.Join(t.TempDir(), "dotgit")
			if err := os.Rename(filepath.Join(checkout, ".git"), moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, filepath.Join(checkout, ".git")); err != nil {
				t.Fatal(err)
			}
		}, "is not a worktree of the repository"},
		{"its .git a folder", func(t *testing.T, _, checkout, _ string) {
			if err := os.Remove(filepath.Join(checkout, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "is not a worktree of the repository"},
		{"the repository's entry for it naming another path", func(t *testing.T, _, checkout, _ string) {
			b, err := os.ReadFile(filepath.Join(checkout, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			admin := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir: "))
			writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(t.TempDir(), "elsewhere", ".git")+"\n")
		}, "is not a worktree of the repository"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
			openPreview(t, e, run.ID)
			l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
			if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
				t.Fatal(err)
			}
			l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
			waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
			checkout := ts.Attempt.Worktree + ".preview"
			c.change(t, dir, checkout, ts.Attempt.Result)
			before, _ := os.Lstat(filepath.Join(checkout, ".git"))
			log := wrapGit(t)
			_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
			if c.words == "" {
				if err != nil {
					t.Fatalf("a whole worktree of the repository: %v", err)
				}
				l.previewWhen(t, run.ID, "build", 1, 2, "ready", previewReadyRow)
				if err := e.ClosePreview(run.ID, "build", 1, 2); err != nil {
					t.Fatal(err)
				}
				l.previewWhen(t, run.ID, "build", 1, 2, "ended", previewEndedRow)
			} else if !errors.Is(err, ErrPreviewRefused) || !strings.Contains(err.Error(), checkout) || !strings.Contains(err.Error(), c.words) {
				t.Fatalf("refused: %v, want %q", err, c.words)
			}
			// No git found its repository from the folder: each one in it
			// was told its git directory and work tree outright, and the
			// repository's own worktree was compared so.
			told := 0
			for _, g := range readGitArgv(t, log) {
				if i := slices.Index(g.args, "-C"); i >= 0 && i+1 < len(g.args) && g.args[i+1] == checkout {
					if !slices.ContainsFunc(g.args, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") }) {
						t.Fatalf("a git with the folder's own discovery: %q", g.args)
					}
					told++
				}
			}
			if ours := c.words == "" || strings.HasPrefix(c.words, "has changes"); ours != (told > 0) {
				t.Fatalf("%d gits told the folder's git directory", told)
			}
			if c.words == "" {
				return
			}
			if after, err := os.Lstat(filepath.Join(checkout, ".git")); err != nil || before == nil || after.Mode() != before.Mode() {
				t.Fatalf("the folder's .git after the refusal: %v %v", after, err)
			}
			if _, ok := l.previewRow(run.ID, "build", 1, 2); ok || previewHeld(e, run.ID, "build", 1) {
				t.Fatal("the refused open made a row or a preview")
			}
		})
	}
}

// The crew's directory and the state directory of a preview's checkout
// are made inside its folder, opened once: a part of the crew's path
// swapped for a link to a folder outside once it was taken makes nothing
// outside, and the open is refused.
func TestAPreviewsFoldersAreMadeInsideItsCheckout(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "app", "app.txt"), "app\n")
	commitAll(t, dir, "app")
	e, _, l := managedEngine(t)
	c := repoManifestCrew(dir, previewManifest(), work("build", "lead"))
	c.Cwd = filepath.Join(dir, "sub", "app")
	run, ts := candidateOf(t, e, l, c)
	checkout := ts.Attempt.Worktree + ".preview"
	outside := t.TempDir()
	e.memberDirChecked = func(part string) {
		if part != "sub" {
			return
		}
		if err := os.Rename(filepath.Join(checkout, "sub"), filepath.Join(t.TempDir(), "sub")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(outside, filepath.Join(checkout, "sub")); err != nil {
			t.Error(err)
		}
	}
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); err == nil {
		t.Fatal("the open with its crew's directory moved outside went on")
	}
	if made, _ := os.ReadDir(outside); len(made) != 0 {
		t.Fatalf("made outside the checkout: %v", made)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", func(r ledger.PreviewRow) bool {
		return r.State == ledger.PreviewEnded && strings.HasPrefix(r.Why, "the checkout could not be made")
	})
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
}
