package crew

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A refusal of the server's bound names the previews open, never one still
// opening, which has no seq yet (a Close of seq 0 is no Close).
func TestABusyRefusalNamesNoPreviewWithoutASeq(t *testing.T) {
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
	reserved, release := holdOnce(t, &e.afterReserve)
	opened := make(chan error, 1)
	go func() {
		_, err := e.OpenPreview(context.Background(), run.ID, "build", 1, "admin")
		opened <- err
	}()
	await(t, "build's reservation", reserved)
	if _, err := e.OpenPreview(t.Context(), run.ID, "docs", 1, "admin"); err != nil {
		t.Fatal(err)
	}
	_, err := e.OpenPreview(t.Context(), run.ID, "lint", 1, "admin")
	var busy *PreviewsBusyError
	if !errors.As(err, &busy) || len(busy.Open) != 1 || busy.Open[0].Task != "docs" || busy.Open[0].Seq != 1 {
		t.Fatalf("the refusal while build opens: %v %+v", err, busy)
	}
	for _, o := range e.OpenPreviews() {
		if o.Seq == 0 {
			t.Fatalf("OpenPreviews names a preview with no seq: %+v", e.OpenPreviews())
		}
	}
	release()
	if err := await(t, "build's open", opened); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"build", "docs"} {
		l.previewWhen(t, run.ID, id, 1, 1, "ready", previewReadyRow)
		if err := e.ClosePreview(run.ID, id, 1, 1); err != nil {
			t.Fatal(err)
		}
		l.previewWhen(t, run.ID, id, 1, 1, "ended", previewEndedRow)
	}
}

// A repeat of Preview this candidate whose first is still under way (its
// reply lost) is answered still opening before the preview has a seq, and
// with its seq once it has one; never with seq 0 as if it were done.
func TestARepeatOfAnOpenUnderWayIsAnsweredByItsSeq(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run, _ := evicted(t, e, fl, l, dir)
	reserved, release := holdOnce(t, &e.afterReserve)
	first := make(chan error, 1)
	go func() {
		_, _, err := e.PreviewCandidate(context.Background(), run.ID, "build", 1, "admin", Command{ID: "open-1"})
		first <- err
	}()
	await(t, "the first open's reservation", reserved)
	rc, ps, err := e.PreviewCandidate(t.Context(), run.ID, "build", 1, "admin", Command{ID: "open-1"})
	if !errors.Is(err, ErrPreviewOpening) || !rc.Repeat || rc.Status != ledger.CommandAccepted || ps.Seq != 0 {
		t.Fatalf("a repeat before the preview has a seq: %+v %+v %v", rc, ps, err)
	}
	release()
	if err := await(t, "the first open", first); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
	waitFake(t, "the preview dropped", func() bool { return !previewHeld(e, run.ID, "build", 1) })
	// Held after its first transaction, with seq 2: the repeat says so.
	made, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.checkoutMade = func(string) {
		once.Do(func() {
			close(made)
			<-gate
		})
	}
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	second := make(chan error, 1)
	go func() {
		_, _, err := e.PreviewCandidate(context.Background(), run.ID, "build", 1, "admin", Command{ID: "open-2"})
		second <- err
	}()
	await(t, "the second open's checkout", made)
	rc, ps, err = e.PreviewCandidate(t.Context(), run.ID, "build", 1, "admin", Command{ID: "open-2"})
	if err != nil || !rc.Repeat || ps.Seq != 2 || ps.State != ledger.PreviewResolved {
		t.Fatalf("a repeat once the preview has its seq: %+v %+v %v", rc, ps, err)
	}
	close(gate)
	if err := await(t, "the second open", second); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 2, "ready", previewReadyRow)
	if err := e.ClosePreview(run.ID, "build", 1, 2); err != nil {
		t.Fatal(err)
	}
	l.previewWhen(t, run.ID, "build", 1, 2, "ended", previewEndedRow)
}
