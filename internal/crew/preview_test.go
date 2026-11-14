package crew

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// previewManifest is a project manifest whose every tree has a setup
// step, a service, a probe waiting for it, a cleanup step and a preview on
// its port.
func previewManifest() Manifest {
	return Manifest{Ports: []string{"web"}, Setup: []Check{shStep("deps", "true")}, Services: []Check{serviceLoop("web", "web.pid", false)},
		Ready: []Probe{byFile("up", "web.pid")}, Cleanup: []Check{shStep("tidy", "true")}, Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}
}

// previewRow is the preview's row as the fake ledger last has it.
func (l *fakeLedger) previewRow(runID, taskID string, n, seq int) (ledger.PreviewRow, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok := l.previews[previewKeyOf(ledger.PreviewRow{RunID: runID, TaskID: taskID, N: n, Seq: seq})]
	return row, ok
}

// previewWhen waits for the preview's committed row to satisfy ok.
func (l *fakeLedger) previewWhen(t *testing.T, runID, taskID string, n, seq int, what string, ok func(ledger.PreviewRow) bool) ledger.PreviewRow {
	t.Helper()
	var row ledger.PreviewRow
	waitFake(t, fmt.Sprintf("preview %d of %s/%d %s", seq, taskID, n, what), func() bool {
		var found bool
		row, found = l.previewRow(runID, taskID, n, seq)
		return found && ok(row)
	})
	return row
}

func previewEndedRow(r ledger.PreviewRow) bool { return r.State == ledger.PreviewEnded }
func previewReadyRow(r ledger.PreviewRow) bool { return r.State == ledger.PreviewReady }

// writers is the preview's writes as committed, by writer, in order.
func (l *fakeLedger) writers(runID, taskID string, n, seq int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, w := range l.previewWrites {
		if w.row.RunID == runID && w.row.TaskID == taskID && w.row.N == n && w.row.Seq == seq {
			out = append(out, w.writer)
		}
	}
	return out
}

// candidateOf launches c, has its build task's attempt 1 commit a result,
// and waits for it to succeed, the run to finish and the attempt's trees
// to be ended: a candidate closed by its result, its own preview gone.
func candidateOf(t *testing.T, e *Engine, l *fakeLedger, c Crew) (Run, TaskState) {
	t.Helper()
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
	waitTreesEnded(t, e, run.ID)
	return run, taskOf(t, e, run.ID, "build")
}

// openPreview opens attempt 1 of build in a preview, failing the test on
// a refusal.
func openPreview(t *testing.T, e *Engine, runID string) PreviewState {
	t.Helper()
	ps, err := e.OpenPreview(t.Context(), runID, "build", 1, "admin")
	if err != nil {
		t.Fatalf("the preview's open: %v", err)
	}
	return ps
}

// previewHeld reports whether the engine still holds a preview of the candidate.
func previewHeld(e *Engine, runID, taskID string, n int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.previews[previewKey{runID, taskID, n}] != nil
}

// An attempt closed by its result, its own preview cleared at its close,
// is opened in a preview: its tree is resolved with the preview as its
// owner, its steps run in <worktree>.preview, detached at the result's
// commit, each a receipt of the attempt numbered after its others, with
// the checkout's dir; it is ready, its URL offered by the attempt's
// Previewing and its label (ReadPreview), and its row says so.
func TestAClosedAttemptsCandidatePreviewOpens(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	if ts.Attempt.State != ledger.AttemptSucceeded || ts.Attempt.Preview != "" || ts.Attempt.Previewing != nil {
		t.Fatalf("the candidate before its preview: %+v", ts.Attempt)
	}
	ps := openPreview(t, e, run.ID)
	checkout := ts.Attempt.Worktree + ".preview"
	if ps.Seq != 1 || ps.Dir != checkout || ps.Commit != ts.Attempt.Result || ps.State != ledger.PreviewResolved || ps.Preview != "" {
		t.Fatalf("the preview as opened: %+v", ps)
	}
	row := l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	url := fmt.Sprintf("http://127.0.0.1:%d/", row.Ports["web"])
	if row.Preview != url || row.Dir != checkout || row.Commit != ts.Attempt.Result || row.OpenedBy != "admin" || row.Where != "native" {
		t.Fatalf("the ready row: %+v", row)
	}
	if head, err := headCommit(t.Context(), checkout); err != nil || head != ts.Attempt.Result {
		t.Fatalf("the checkout is at %s (%v), not the result %s", head, err, ts.Attempt.Result)
	}
	got := taskOf(t, e, run.ID, "build").Attempt.Previewing
	if got == nil || *got != (Previewing{Seq: 1, State: ledger.PreviewReady, Preview: url}) {
		t.Fatalf("the attempt's Previewing: %+v", got)
	}
	if label, ok := e.ReadPreview(run.ID, "build", 1, 1); !ok || label.Preview != url || label.Secret == "" || label.Root != dir {
		t.Fatalf("the label: %+v %v", label, ok)
	}
	// Its receipts: the attempt's, after the worktree's (k 1) and the
	// checkout's (k 2), in the preview's checkout.
	for _, id := range []string{"deps", "web", "up"} {
		r, ok := rowOf(l.stepRows(run.ID, "build", 1), id, 3)
		if !ok || r.Dir != checkout || r.Subject != ts.Attempt.Result {
			t.Fatalf("the preview's %s receipt: %+v %v\n%s", id, r, ok, rowWords(l.stepRows(run.ID, "build", 1)))
		}
	}
	if p := l.payloads(run.ID, "preview.opened"); len(p) != 1 || p[0]["seq"] != 1 || p[0]["task"] != "build" || p[0]["by"] != "admin" {
		t.Fatalf("preview.opened: %v", p)
	}
	if p := l.payloads(run.ID, "preview.ready"); len(p) != 1 || p[0]["preview"] != url {
		t.Fatalf("preview.ready: %v", p)
	}
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
}

// A preview's open, ready and end never touch the result checkout's trees
// row nor the attempt's trees: its rows are the previews table's.
func TestAPreviewLeavesTheResultCheckoutsTreeRowAlone(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	checkout, _ := l.treeRow(run.ID, "build", 1, ledger.TreeCheckout)
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	trees := slices.Clone(r.tasks[0].attempts[0].trees)
	e.mu.Unlock()
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
	waitTreesEnded(t, e, run.ID)
	if now, _ := l.treeRow(run.ID, "build", 1, ledger.TreeCheckout); fmt.Sprint(now) != fmt.Sprint(checkout) {
		t.Fatalf("the checkout's row after the preview:\n%+v\nbefore:\n%+v", now, checkout)
	}
	if _, ok := l.treeRow(run.ID, "build", 1, ledger.TreePreview); ok {
		t.Fatal("a preview's row in the trees table")
	}
	e.mu.Lock()
	after := slices.Clone(r.tasks[0].attempts[0].trees)
	e.mu.Unlock()
	if !slices.Equal(after, trees) {
		t.Fatalf("the attempt's trees changed: %d before, %d after", len(trees), len(after))
	}
	if w := l.writers(run.ID, "build", 1, 1); !slices.Equal(w, []string{"OpenPreview", "PreviewReady", "PreviewEnding", "EndPreview"}) {
		t.Fatalf("the preview's writes: %v", w)
	}
}

// A preview's service that exits after it is ready ends it, why
// service_exited: its URL is cleared, in the ledger and as the attempt
// reads, before its cleanup runs (held at a gate here), and the end is
// recorded once the cleanup has run.
func TestAPreviewServiceLostAfterReadyEndsIt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate := filepath.Join(t.TempDir(), "gate")
	t.Cleanup(func() { open(t, gate) })
	m := previewManifest()
	// The preview's cleanup waits at the gate; the worktree's and the
	// checkout's do not.
	m.Cleanup = []Check{shStep("tidy", `case "$CONDUCTOR_STATE_DIR" in *.preview/*) `+gated(gate)+`;; esac`)}
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, m, work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	syscall.Kill(pid, syscall.SIGKILL)
	ending := l.previewWhen(t, run.ID, "build", 1, 1, "ending", func(r ledger.PreviewRow) bool { return r.Why == whyServiceExited })
	if ending.Preview != "" || ending.State == ledger.PreviewEnded {
		t.Fatalf("the row as the end began: %+v", ending)
	}
	waitFake(t, "the preview's cleanup running", func() bool {
		r, ok := rowOf(l.stepRows(run.ID, "build", 1), "tidy", 3)
		return ok && r.State == ledger.CheckRunning
	})
	if got := taskOf(t, e, run.ID, "build").Attempt.Previewing; got == nil || got.Preview != "" || got.State != previewEnding {
		t.Fatalf("Previewing while the preview's cleanup runs: %+v", got)
	}
	if _, ok := e.ReadPreview(run.ID, "build", 1, 1); ok {
		t.Fatal("the label offers the preview while its end runs")
	}
	open(t, gate)
	row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
	if row.Why != whyServiceExited || row.Interrupted {
		t.Fatalf("the ended row: %+v", row)
	}
	if p := l.payloads(run.ID, "preview.ended"); len(p) != 1 || p[0]["why"] != whyServiceExited || p[0]["seq"] != 1 {
		t.Fatalf("preview.ended: %v", p)
	}
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	if got := taskOf(t, e, run.ID, "build").Attempt.Previewing; got != nil {
		t.Fatalf("Previewing after the end: %+v", got)
	}
}

// A probe held at a gate past the preview's end (it ignores the end of its
// context) finishes after the end began: its receipt is the attempt's, and
// no row of that seq is written by it: no PreviewReady.
func TestALateStepOfAnEndedPreviewWritesNothing(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	m := previewManifest()
	m.Ready = []Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", IntervalSeconds: 1}}
	for n := fakePortsFirst; n < fakePortsFirst+4; n++ {
		fl.probes.answer(fmt.Sprintf("http://127.0.0.1:%d/", n), http.StatusOK)
	}
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, m, work("build", "lead")))
	const port = 41500
	asked, gate := make(chan struct{}, 1), make(chan struct{})
	fl.probes.answerWith(fmt.Sprintf("http://127.0.0.1:%d/", port), func(context.Context) (int, error) {
		select {
		case asked <- struct{}{}:
		default:
		}
		<-gate
		return http.StatusOK, nil
	})
	fl.ports.script(port)
	openPreview(t, e, run.ID)
	select {
	case <-asked:
	case <-time.After(20 * time.Second):
		t.Fatal("the preview's probe was not asked")
	}
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ending", func(r ledger.PreviewRow) bool { return r.Why == whyPreviewClosed })
	close(gate)
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
	if r, ok := rowOf(l.stepRows(run.ID, "build", 1), "page", 3); !ok || r.State != ledger.CheckFinished {
		t.Fatalf("the late probe's receipt: %+v %v", r, ok)
	}
	if w := l.writers(run.ID, "build", 1, 1); !slices.Equal(w, []string{"OpenPreview", "PreviewEnding", "EndPreview"}) {
		t.Fatalf("the preview's writes: %v", w)
	}
	if n := l.count(run.ID, "preview.ready"); n != 0 {
		t.Fatalf("%d preview.ready", n)
	}
}

// A candidate's preview opens at its commit, whatever the worktree holds
// since: the checkout is detached at the result, its steps' receipts are
// the attempt's with the checkout's dir.
func TestACandidatePreviewOpensAtItsCommit(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	// The worktree moves on after the result: the preview is the result's.
	writeFile(t, filepath.Join(ts.Attempt.Worktree, "later.txt"), "later\n")
	commitAll(t, ts.Attempt.Worktree, "later")
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	checkout := ts.Attempt.Worktree + ".preview"
	if head, err := headCommit(t.Context(), checkout); err != nil || head != ts.Attempt.Result {
		t.Fatalf("the preview is at %s (%v), not the result %s", head, err, ts.Attempt.Result)
	}
	if _, err := os.Stat(filepath.Join(checkout, "later.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the preview has the worktree's later commit: %v", err)
	}
	for _, r := range l.stepRows(run.ID, "build", 1) {
		if r.K == 3 && (r.Dir != checkout || r.Subject != ts.Attempt.Result || r.Where != "native") {
			t.Fatalf("a preview's receipt: %+v", r)
		}
	}
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
}

// A preview's end's first transaction is committed before any of its
// services is signalled: when the first Stop comes (held at a gate) the
// row's URL is cleared and nothing offers it.
func TestAPreviewIsEndedBeforeItsServicesAreSignalled(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	stopping, openStops := gateStops(t, e)
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopping:
	case <-time.After(20 * time.Second):
		t.Fatal("the preview's service was not stopped")
	}
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.Preview != "" || row.Why != whyPreviewClosed || row.State == ledger.PreviewEnded {
		t.Fatalf("the row as the service is signalled: %+v", row)
	}
	if _, ok := e.ReadPreview(run.ID, "build", 1, 1); ok {
		t.Fatal("the label offers the preview as its service is signalled")
	}
	openStops()
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyPreviewClosed {
		t.Fatalf("the ended row: %+v", row)
	}
}

// fakeClock is a clock a test moves.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// A preview no one reads for previewIdle is ended, why idle: a read of its
// label puts the end previewIdle after it. The engine's clock and its
// idle timer are the test's.
func TestAPreviewIdleIsEnded(t *testing.T) {
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
		w := wait{d, make(chan time.Time, 1)}
		waits <- w
		return w.fire
	}
	next := func() wait {
		t.Helper()
		select {
		case w := <-waits:
			return w
		case <-time.After(20 * time.Second):
			t.Fatal("the idle end set no timer")
		}
		return wait{}
	}
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	if w := next(); w.d != previewIdle {
		t.Fatalf("the first idle wait: %s", w.d)
	} else {
		clock.add(10 * time.Minute)
		if _, ok := e.ReadPreview(run.ID, "build", 1, 1); !ok {
			t.Fatal("the label is not read")
		}
		clock.add(21 * time.Minute)
		w.fire <- clock.Now()
	}
	// Read 21 minutes ago: nine are left.
	w := next()
	if w.d != 9*time.Minute {
		t.Fatalf("the idle wait after a read: %s", w.d)
	}
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.State != ledger.PreviewReady {
		t.Fatalf("the preview before its idle end: %+v", row)
	}
	clock.add(9 * time.Minute)
	w.fire <- clock.Now()
	if row := l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow); row.Why != whyPreviewIdle {
		t.Fatalf("the idle end's row: %+v", row)
	}
}

// A run's stop ends its previews first, why run_stopped, their services
// gone before the stop returns.
func TestAPreviewEndsWithTheRun(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	stopRun(t, e, run.ID)
	row, _ := l.previewRow(run.ID, "build", 1, 1)
	if row.State != ledger.PreviewEnded || row.Why != whyRunStopped {
		t.Fatalf("the preview once the run's stop returned: %+v", row)
	}
	if alive(pid) {
		t.Fatalf("the preview's service %d outlived the run's stop", pid)
	}
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("a preview of a stopped run: %v", err)
	}
}

// A finished run whose candidate's preview is open is held while launches
// pass maxRuns: kept, the preview's service alive, its label served.
// Closed, the next launch evicts the run.
func TestAPreviewHoldsItsRunAgainstEviction(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	old := maxRuns
	t.Cleanup(func() { maxRuns = old })
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	// The succeeded attempt's session stays idle (R4), which would hold
	// the run by itself: ended, the preview is all that holds it.
	s, _ := fl.attemptSession(t, ts)
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "the attempt's session ended", func() bool {
		r, _ := e.Get(run.ID)
		return r.Members[0].Status == MemberEnded
	})
	openPreview(t, e, run.ID)
	row := l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	maxRuns = 1
	other := testCrew(manual("lead", ""))
	if _, err := e.Launch(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); !ok {
		t.Fatal("the run was forgotten while its preview was open")
	}
	if !alive(pid) {
		t.Fatal("the preview's service was stopped as the run was passed")
	}
	if label, ok := e.ReadPreview(run.ID, "build", 1, 1); !ok || label.Preview != row.Preview {
		t.Fatalf("the label of the held run's preview: %+v %v", label, ok)
	}
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	waitTreesEnded(t, e, run.ID)
	if _, err := e.Launch(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("the run was kept once its preview ended")
	}
}

// A preview's checkout found there with a tracked file changed is refused,
// its path and the file named, and left as it is; one found at the commit
// with no tracked change (an untracked file beside it) is taken; a
// directory there that is no worktree of the repository is refused.
func TestAPreviewCheckoutWithChangesIsRefused(t *testing.T) {
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
	writeFile(t, filepath.Join(checkout, "cache.txt"), "untracked\n")
	ps := openPreview(t, e, run.ID)
	if ps.Seq != 2 {
		t.Fatalf("the second open: %+v", ps)
	}
	l.previewWhen(t, run.ID, "build", 1, 2, "ready", previewReadyRow)
	if err := e.ClosePreview(run.ID, "build", 1, 2); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 2, "ended", previewEndedRow)
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	writeFile(t, filepath.Join(checkout, "built.txt"), "changed\n")
	_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if !errors.Is(err, ErrPreviewRefused) || !strings.Contains(err.Error(), checkout) || !strings.Contains(err.Error(), "built.txt") {
		t.Fatalf("a checkout with a tracked change: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(checkout, "built.txt")); string(b) != "changed\n" {
		t.Fatalf("the changed file is %q", b)
	}
	if _, ok := l.previewRow(run.ID, "build", 1, 3); ok || previewHeld(e, run.ID, "build", 1) {
		t.Fatal("the refused open made a row or a preview")
	}
	// A directory there that the repository does not list as a worktree,
	// with a .git of its own: refused, git never run in it, left as it is.
	runGit(t, dir, "worktree", "remove", "--force", checkout)
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(checkout, ".git"), "gitdir: "+filepath.Join(t.TempDir(), "elsewhere")+"\n")
	_, err = e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if !errors.Is(err, ErrPreviewRefused) || !strings.Contains(err.Error(), "is not a worktree of the repository") {
		t.Fatalf("a directory that is no worktree of the repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkout, ".git")); err != nil {
		t.Fatalf("the directory's .git: %v", err)
	}
}

// At most maxPreviews previews are open on the server, the refusal naming
// them; one per candidate at a time; ledger.MaxPreviews opens of one.
func TestPreviewsAreBoundedPerServer(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	c := repoManifestCrew(dir, previewManifest(), work("build", "a"), work("docs", "b"), work("lint", "c"))
	run := launchManaged(t, e, c)
	for _, id := range []string{"build", "docs", "lint"} {
		a := waitAttempt(t, e, run.ID, id, ledger.AttemptRunning)
		commitResult(t, e, a, id+".txt", id+"\n", id)
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	waitTreesEnded(t, e, run.ID)
	for _, id := range []string{"build", "docs"} {
		if _, err := e.OpenPreview(t.Context(), run.ID, id, 1, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); !errors.Is(err, ErrPreviewOpen) {
		t.Fatalf("a second preview of a candidate: %v", err)
	}
	_, err := e.OpenPreview(t.Context(), run.ID, "lint", 1, "admin")
	var busy *PreviewsBusyError
	if !errors.As(err, &busy) || !errors.Is(err, ErrPreviewsBusy) || len(busy.Open) != 2 {
		t.Fatalf("a third preview: %v", err)
	}
	names := []string{busy.Open[0].Task, busy.Open[1].Task}
	slices.Sort(names)
	if !slices.Equal(names, []string{"build", "docs"}) {
		t.Fatalf("the refusal names %v", busy.Open)
	}
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	waitFake(t, "build's preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	if _, err := e.OpenPreview(t.Context(), run.ID, "lint", 1, "admin"); err != nil {
		t.Fatalf("a preview once one ended: %v", err)
	}
	// docs opened as often as a candidate is.
	r := runOf(t, e, run.ID)
	e.mu.Lock()
	r.task("docs").attempts[0].previewSeq = ledger.MaxPreviews
	e.mu.Unlock()
	if err := e.ClosePreview(run.ID, "docs", 1, 1); err != nil {
		t.Fatal(err)
	}
	waitFake(t, "docs' preview dropped", func() bool { return !previewHeld(e, run.ID, "docs", 1) })
	if _, err := e.OpenPreview(t.Context(), run.ID, "docs", 1, "admin"); !errors.Is(err, ErrPreviewLimit) {
		t.Fatalf("a candidate past its opens: %v", err)
	}
	if err := e.ClosePreview(run.ID, "lint", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "lint", 1, 1, "ended", previewEndedRow)
}

// A preview the owner left open (its ledger closed under it, the server
// gone) is ended interrupted by the next start's recovery, its service
// reported with its tree and never signalled.
func TestAPreviewLeftAtRestartIsReportedNotSignalled(t *testing.T) {
	dir := newRepo(t)
	e, _ := readyEngine(t)
	ldir := t.TempDir()
	ld, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	e.UseLedger(SQLLedger(ld))
	run := launchManaged(t, e, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	waitTreesEnded(t, e, run.ID)
	openPreview(t, e, run.ID)
	ctx := context.Background()
	waitFake(t, "the preview ready", func() bool {
		ps, _ := ld.Previews(ctx, run.ID)
		return len(ps) == 1 && ps[0].State == ledger.PreviewReady
	})
	pid := pidIn(t, stateFile(a.Attempt.Worktree+".preview", "web.pid"))
	// The owner ends here, its preview open.
	if err := ld.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	rec, err := again.Recover(ctx)
	if err != nil || len(rec) != 1 || rec[0].RunID != run.ID || rec[0].Previews != 1 {
		t.Fatalf("the recovery: %+v %v", rec, err)
	}
	if svcs := rec[0].Services; len(svcs) != 1 || svcs[0].ID != "web" || svcs[0].Tree != ledger.TreePreview || svcs[0].Pid != pid {
		t.Fatalf("the services reported: %+v", svcs)
	}
	ps, _ := again.Previews(ctx, run.ID)
	if len(ps) != 1 || ps[0].State != ledger.PreviewEnded || !ps[0].Interrupted || ps[0].Preview != "" {
		t.Fatalf("the preview after the recovery: %+v", ps)
	}
	if !alive(pid) {
		t.Fatalf("the recovery signalled the preview's service %d", pid)
	}
}

// A run continued elsewhere refuses a preview of its candidates, as its
// task commands do (run_continued).
func TestAContinuedRunRefusesAPreview(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if !errors.Is(err, ErrRunContinued) {
		t.Fatalf("a preview of a continued run: %v", err)
	}
	if refusal := refusalOf(err); refusal != RefusalRunContinued {
		t.Fatalf("the refusal: %q", refusal)
	}
	if previewHeld(e, run.ID, "build", 1) {
		t.Fatal("the refused preview is held")
	}
	stopRun(t, e, next.ID)
}

// The server's shutdown ends a preview still open as it ends every tree
// (Drain): why shutdown, its service gone, its row ended before Drain
// returns.
func TestAPreviewEndsAtTheShutdown(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	pid := pidIn(t, stateFile(ts.Attempt.Worktree+".preview", "web.pid"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.Drain(ctx)
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.State != ledger.PreviewEnded || row.Why != whyShutdown {
		t.Fatalf("the preview once Drain returned: %+v", row)
	}
	if alive(pid) {
		t.Fatalf("the preview's service %d outlived the shutdown", pid)
	}
}
