package crew

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// The fake ledger's reads of a preview-only context (previewReads), and
// its transaction's read of its run's row (runRowTx), as the ledger's.

func (l *fakeLedger) AttemptReceipts(ctx context.Context, runID, taskID string, n int) ([]ledger.CheckRow, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastChecks(runID, taskID, n), nil
}

func (l *fakeLedger) Previews(ctx context.Context, runID string) ([]ledger.PreviewRow, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ledger.PreviewRow
	for _, p := range l.previews {
		if p.RunID == runID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (tx *fakeTx) Run() (ledger.RunRow, bool, error) {
	tx.l.mu.Lock()
	defer tx.l.mu.Unlock()
	row, ok := tx.l.runs[tx.runID]
	return row, ok, nil
}

// previewContext is the preview-only context the engine keeps for the
// run, nil for none.
func previewContext(e *Engine, runID string) *run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.previewRuns[runID]
}

// launches is how many sessions the fake launcher was asked to start.
func (f *fakeLauncher) launches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// evicted is a finished run's candidate, the run then forgotten by the
// engine past maxRuns (its attempt's session ended first, so nothing
// holds it): Get no longer has it.
func evicted(t *testing.T, e *Engine, fl *fakeLauncher, l *fakeLedger, dir string) (Run, TaskState) {
	t.Helper()
	old := maxRuns
	t.Cleanup(func() { maxRuns = old })
	run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	s, _ := fl.attemptSession(t, ts)
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "the attempt's session ended", func() bool {
		r, _ := e.Get(run.ID)
		return r.Members[0].Status == MemberEnded
	})
	maxRuns = 1
	if _, err := e.Launch(t.Context(), testCrew(manual("lead", ""))); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("the finished run was not evicted")
	}
	return run, ts
}

// A finished run evicted past maxRuns before any preview opens a preview
// from the ledger: in a preview-only context the engine keeps apart, so
// Get still has no run, nothing is launched or scheduled and no row of the
// run, its tasks or attempts is written; the preview's receipts are the
// attempt's, numbered after its last (k 3, after the worktree's and the
// checkout's), its tree's secret among the engine's, a second open of the
// candidate refused as with a run the engine holds. The idle end ends it
// (its URL cleared before its service is signalled, each writer once) and
// drops the context; the next open reads the ledger again and numbers its
// receipts after the first preview's.
func TestAPreviewOpensForAnEvictedRun(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	clock := &fakeClock{now: time.Now()}
	e.now = clock.Now
	fires := make(chan chan time.Time, 4)
	e.idleTimer = func(d time.Duration) <-chan time.Time {
		fire := make(chan time.Time, 1)
		if d == previewIdle {
			fires <- fire
		}
		return fire
	}
	run, ts := evicted(t, e, fl, l, dir)
	launched := fl.launches()
	l.mu.Lock()
	runRow, attempts := l.runs[run.ID], len(l.attempts)
	l.mu.Unlock()
	stopping, openStops := gateStops(t, e)
	ps := openPreview(t, e, run.ID)
	checkout := ts.Attempt.Worktree + ".preview"
	if ps.Seq != 1 || ps.Dir != checkout || ps.Commit != ts.Attempt.Result || ps.Root != dir {
		t.Fatalf("the preview as opened: %+v", ps)
	}
	if previewContext(e, run.ID) == nil {
		t.Fatal("no preview-only context is kept")
	}
	row := l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	if row.Dir != checkout || row.Commit != ts.Attempt.Result || row.OpenedBy != "admin" || row.Where != "native" || row.Preview == "" {
		t.Fatalf("the ready row: %+v", row)
	}
	for _, id := range []string{"deps", "web", "up"} {
		if r, ok := rowOf(l.stepRows(run.ID, "build", 1), id, 3); !ok || r.Dir != checkout || r.Subject != ts.Attempt.Result {
			t.Fatalf("the preview's %s receipt: %+v %v\n%s", id, r, ok, rowWords(l.stepRows(run.ID, "build", 1)))
		}
	}
	label, ok := e.ReadPreview(run.ID, "build", 1, 1)
	if !ok || label.Preview != row.Preview || label.Secret == "" || !slices.Contains(e.TreeSecrets(), label.Secret) {
		t.Fatalf("the label: %+v %v", label, ok)
	}
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); !errors.Is(err, ErrPreviewOpen) {
		t.Fatalf("a second preview of the candidate: %v", err)
	}
	// Nothing of the run is held, launched or written but the preview's.
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("Get has the run its preview opened from the ledger")
	}
	if _, _, err := e.TaskOf(run.ID, "build"); err == nil {
		t.Fatal("TaskOf has the run its preview opened from the ledger")
	}
	l.mu.Lock()
	sameRun, sameAttempts := fmt.Sprint(l.runs[run.ID]) == fmt.Sprint(runRow), len(l.attempts) == attempts
	l.mu.Unlock()
	if !sameRun || !sameAttempts || fl.launches() != launched {
		t.Fatalf("the preview wrote the run's row (%v) or an attempt (%v), or launched a session (%d, was %d)", !sameRun, !sameAttempts, fl.launches(), launched)
	}
	// The idle end: its first write before its service is signalled.
	clock.add(previewIdle)
	await(t, "the idle timer", fires) <- clock.Now()
	await(t, "the preview's service stopped", stopping)
	if row, _ := l.previewRow(run.ID, "build", 1, 1); row.Preview != "" || row.Why != whyPreviewIdle {
		t.Fatalf("the row as the service is signalled: %+v", row)
	}
	if _, ok := e.ReadPreview(run.ID, "build", 1, 1); ok {
		t.Fatal("the label offers the preview as its service is signalled")
	}
	openStops()
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
	if w := l.writers(run.ID, "build", 1, 1); !slices.Equal(w, []string{"OpenPreview", "PreviewReady", "PreviewEnding", "EndPreview"}) {
		t.Fatalf("the preview's writes: %v", w)
	}
	waitFake(t, "the preview and its context dropped", func() bool {
		return !previewHeld(e, run.ID, "build", 1) && previewContext(e, run.ID) == nil
	})
	// Opened again from the ledger: seq 2, its receipts after the first's.
	if ps := openPreview(t, e, run.ID); ps.Seq != 2 {
		t.Fatalf("the second open: %+v", ps)
	}
	l.previewWhen(t, run.ID, "build", 1, 2, "ready", previewReadyRow)
	if _, ok := rowOf(l.stepRows(run.ID, "build", 1), "deps", 4); !ok {
		t.Fatalf("the second preview's setup is not k 4:\n%s", rowWords(l.stepRows(run.ID, "build", 1)))
	}
	if err := e.ClosePreview(run.ID, "build", 1, 2); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 2, "ended", previewEndedRow)
	waitFake(t, "the context dropped", func() bool { return previewContext(e, run.ID) == nil })
	if _, ok := e.Get(run.ID); ok || fl.launches() != launched {
		t.Fatal("the second preview held or launched something of the run")
	}
}

// A run the engine does not hold is refused as a run it holds is: one the
// ledger has no row of (not found), one continued (run_continued: judged
// at the load, and again in the open's first transaction for a
// continuation made since), one stopped before it finished, and an
// attempt that does not exist. A refused open keeps no context.
func TestAPreviewOfARunNotHeldIsRefusedAsAHeldOne(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run, _ := evicted(t, e, fl, l, dir)
	if _, err := e.OpenPreview(t.Context(), "api-sweep-ffffffff", "build", 1, "admin"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("a run the ledger has not: %v", err)
	}
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 2, "admin"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("an attempt the run has not: %v", err)
	}
	if previewContext(e, run.ID) != nil {
		t.Fatal("a refused open kept its context")
	}
	// Continued after the context was read, before its first transaction:
	// refused there, nothing of the preview written.
	e.afterLookup = func(string) {
		l.mu.Lock()
		row := l.runs[run.ID]
		row.ContinuedBy = "api-sweep-0000c0de"
		l.runs[run.ID] = row
		l.mu.Unlock()
	}
	_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if !errors.Is(err, ErrRunContinued) || refusalOf(err) != RefusalRunContinued {
		t.Fatalf("a run continued as its preview opened: %v", err)
	}
	e.afterLookup = nil
	if _, ok := l.previewRow(run.ID, "build", 1, 1); ok || previewContext(e, run.ID) != nil || previewHeld(e, run.ID, "build", 1) {
		t.Fatal("the refused open left a row, a context or a preview")
	}
	// Continued before: refused at the load.
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); !errors.Is(err, ErrRunContinued) {
		t.Fatalf("a continued run: %v", err)
	}
	// Stopped before it finished: refused as a stopped run the engine
	// holds is.
	l.mu.Lock()
	row := l.runs[run.ID]
	row.ContinuedBy, row.State, row.Outcome = "", ledger.RunStopped, ""
	l.runs[run.ID] = row
	l.mu.Unlock()
	if _, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin"); !errors.Is(err, ErrRunStopped) || previewContext(e, run.ID) != nil {
		t.Fatalf("a run stopped before it finished: %v", err)
	}
}

// The server's bound holds across held runs and runs it does not hold:
// two previews of runs it no longer holds refuse a third, of a held run,
// naming them.
func TestPreviewsOfRunsNotHeldCountInTheBound(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	old := maxRuns
	t.Cleanup(func() { maxRuns = old })
	c := repoManifestCrew(dir, previewManifest(), work("build", "a"), work("docs", "b"))
	run := launchManaged(t, e, c)
	for _, id := range []string{"build", "docs"} {
		a := waitAttempt(t, e, run.ID, id, ledger.AttemptRunning)
		commitResult(t, e, a, id+".txt", id+"\n", id)
	}
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	waitTreesEnded(t, e, run.ID)
	for _, id := range []string{"build", "docs"} {
		s, _ := fl.attemptSession(t, taskOf(t, e, run.ID, id))
		if err := s.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	waitTrue(t, "the sessions ended", func() bool {
		r, _ := e.Get(run.ID)
		return r.Members[0].Status == MemberEnded && r.Members[1].Status == MemberEnded
	})
	maxRuns = 1
	if _, err := e.Launch(t.Context(), testCrew(manual("lead", ""))); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("the run was not evicted")
	}
	for _, id := range []string{"build", "docs"} {
		if _, err := e.OpenPreview(t.Context(), run.ID, id, 1, "admin"); err != nil {
			t.Fatalf("the preview of %s: %v", id, err)
		}
	}
	if previewContext(e, run.ID) == nil {
		t.Fatal("no context for the two previews")
	}
	held, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "c")))
	_, err := e.OpenPreview(t.Context(), held.ID, "build", 1, "admin")
	var busy *PreviewsBusyError
	if !errors.As(err, &busy) || len(busy.Open) != 2 || busy.Open[0].Run != run.ID || busy.Open[1].Run != run.ID {
		t.Fatalf("a third preview: %v %+v", err, busy)
	}
	for _, id := range []string{"build", "docs"} {
		l.previewWhen(t, run.ID, id, 1, 1, "ready", previewReadyRow)
		if err := e.ClosePreview(run.ID, id, 1, 1); err != nil {
			t.Fatal(err)
		}
		l.previewWhen(t, run.ID, id, 1, 1, "ended", previewEndedRow)
	}
	waitFake(t, "the context dropped with its last preview", func() bool { return previewContext(e, run.ID) == nil })
}

// A run finished under a first server is previewed by a second over the
// same ledger: opened from the ledger in a preview-only context, its
// Runtime chosen for it by the second server's offered one, which the
// row's where_ran and its receipts say; Get has no run; ended by the
// shutdown's Drain, its service gone, its row ended before Drain returns.
func TestAPreviewOpensForARunOfAnEarlierServer(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	first, _ := readyEngine(t)
	ldir := t.TempDir()
	ld, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	first.UseLedger(SQLLedger(ld))
	run := launchManaged(t, first, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	a := waitAttempt(t, first, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, first, a, "built.txt", "yes\n", "built")
	waitOutcome(t, first, run.ID, ledger.OutcomeSucceeded)
	waitTreesEnded(t, first, run.ID)
	ts := taskOf(t, first, run.ID, "build")
	// The first server ends as a server does: its runs stopped, finished
	// ones among them, then drained.
	stopRun(t, first, run.ID)
	first.Drain(ctx)
	if err := ld.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	if _, err := again.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	second, _ := readyEngine(t)
	fake := &sessiontest.FakeRuntime{Base: local.Native()}
	offer(t, second, fake)
	second.UseLedger(SQLLedger(again))
	finished := make(chan struct{}, 1)
	second.previewFinished = func() { finished <- struct{}{} }
	ps, err := second.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
	if err != nil {
		t.Fatalf("the preview of the earlier server's run: %v", err)
	}
	checkout := ts.Attempt.Worktree + ".preview"
	if ps.Seq != 1 || ps.Dir != checkout || ps.Where != "fake" {
		t.Fatalf("the preview as opened: %+v", ps)
	}
	if c := fake.Asked("Takes"); len(c) != 1 || c[0].Ref != (ext.ProcessRef{Run: run.ID}) {
		t.Fatalf("the offered Runtime was asked Takes: %+v", c)
	}
	var row ledger.PreviewRow
	waitFake(t, "the preview ready", func() bool {
		rows, _ := again.Previews(ctx, run.ID)
		if len(rows) == 1 {
			row = rows[0]
		}
		return len(rows) == 1 && rows[0].State == ledger.PreviewReady
	})
	if row.Where != "fake" || row.Dir != checkout || row.Commit != ts.Attempt.Result {
		t.Fatalf("the ready row: %+v", row)
	}
	checks, err := again.Checks(ctx, run.ID, "build", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := rowOf(checks, "deps", 3); !ok || r.Where != "fake" || r.Dir != checkout {
		t.Fatalf("the preview's setup receipt: %+v %v", r, ok)
	}
	if _, ok := second.Get(run.ID); ok {
		t.Fatal("the second server holds the run")
	}
	if label, ok := second.ReadPreview(run.ID, "build", 1, 1); !ok || !slices.Contains(second.TreeSecrets(), label.Secret) {
		t.Fatalf("the label: %+v %v", label, ok)
	}
	pid := pidIn(t, stateFile(checkout, "web.pid"))
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	second.Drain(dctx)
	rows, _ := again.Previews(ctx, run.ID)
	if len(rows) != 1 || rows[0].State != ledger.PreviewEnded || rows[0].Why != whyShutdown || rows[0].Interrupted {
		t.Fatalf("the preview once Drain returned: %+v", rows)
	}
	if alive(pid) {
		t.Fatalf("the preview's service %d outlived the shutdown", pid)
	}
	await(t, "the preview let go", finished)
	if previewContext(second, run.ID) != nil {
		t.Fatal("the context outlived its preview")
	}
}
