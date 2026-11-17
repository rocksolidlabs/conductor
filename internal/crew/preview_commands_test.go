package crew

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// commandRow is the command's row as the fake ledger has it, with its
// receipt decoded.
func (l *fakeLedger) commandRow(t *testing.T, runID, actor, id string) (ledger.CommandRow, Receipt, bool) {
	t.Helper()
	l.mu.Lock()
	row, ok := l.commands[runID+"/"+actor+"/"+id]
	l.mu.Unlock()
	var rc Receipt
	if ok && row.Receipt != "" {
		if err := json.Unmarshal([]byte(row.Receipt), &rc); err != nil {
			t.Fatal(err)
		}
	}
	return row, rc, ok
}

// The preview commands of a run the engine does not hold keep their
// receipts in its ledger as a held run's do: the open's taken and settled
// done, naming its preview; its repeat answered by it with no second open;
// the same id for another attempt a conflict; a refused open failed with
// its refusal; the close settled done, the preview-only context dropped
// with the preview; an open refused with no preview held leaves no
// context; a close of a run neither held nor previewed is refused with no
// receipt.
func TestPreviewCommandsKeepTheirReceiptsForARunNotHeld(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run, _ := evicted(t, e, fl, l, dir)
	ctx := t.Context()
	rc, ps, err := e.PreviewCandidate(ctx, run.ID, "build", 1, "admin", Command{ID: "open-1"})
	if err != nil || rc.Status != ledger.CommandDone || rc.Repeat || rc.Preview == nil || rc.Preview.Seq != 1 || ps.Seq != 1 {
		t.Fatalf("the open: %+v %+v %v", rc, ps, err)
	}
	row, kept, ok := l.commandRow(t, run.ID, "admin", "open-1")
	if !ok || row.Intent != IntentPreviewOpen || row.Status != ledger.CommandDone || kept.Preview == nil || kept.Preview.Seq != 1 {
		t.Fatalf("the open's row: %+v %+v %v", row, kept, ok)
	}
	if n := l.count(run.ID, "command.accepted"); n != 1 {
		t.Fatalf("%d commands taken", n)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	again, ps, err := e.PreviewCandidate(ctx, run.ID, "build", 1, "admin", Command{ID: "open-1"})
	if err != nil || !again.Repeat || again.Preview == nil || again.Preview.Seq != 1 || ps.State != ledger.PreviewReady {
		t.Fatalf("the open again: %+v %+v %v", again, ps, err)
	}
	if w := l.writers(run.ID, "build", 1, 1); len(w) != 2 {
		t.Fatalf("the repeat wrote the preview again: %v", w)
	}
	if _, _, err := e.PreviewCandidate(ctx, run.ID, "build", 2, "admin", Command{ID: "open-1"}); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("the id for another attempt: %v", err)
	}
	refused, _, err := e.PreviewCandidate(ctx, run.ID, "build", 1, "admin", Command{ID: "open-2"})
	if !errors.Is(err, ErrPreviewOpen) || refused.Status != ledger.CommandFailed || refused.Refusal != RefusalPreviewOpen {
		t.Fatalf("a second open: %+v %v", refused, err)
	}
	if _, kept, _ := l.commandRow(t, run.ID, "admin", "open-2"); kept.Refusal != RefusalPreviewOpen {
		t.Fatalf("the second open's row: %+v", kept)
	}
	closed, _, err := e.ClosePreviewCommand(ctx, run.ID, "build", 1, 1, "admin", Command{ID: "close-1"})
	if err != nil || closed.Status != ledger.CommandDone || closed.Intent != IntentPreviewClose {
		t.Fatalf("the close: %+v %v", closed, err)
	}
	l.previewWhen(t, run.ID, "build", 1, 1, "ended", previewEndedRow)
	waitFake(t, "the context dropped", func() bool { return previewContext(e, run.ID) == nil })
	if rc, _, err := e.ClosePreviewCommand(ctx, run.ID, "build", 1, 1, "admin", Command{ID: "close-1"}); err != nil || !rc.Repeat {
		t.Fatalf("the close again: %+v %v", rc, err)
	}
	none, _, err := e.PreviewCandidate(ctx, run.ID, "build", 9, "admin", Command{ID: "open-9"})
	if !errors.Is(err, ErrTaskNotFound) || none.Refusal != RefusalNotFound || previewContext(e, run.ID) != nil {
		t.Fatalf("an open of no attempt: %+v %v, context kept %v", none, err, previewContext(e, run.ID) != nil)
	}
	if rc, _, err := e.ClosePreviewCommand(ctx, run.ID, "build", 1, 1, "admin", Command{ID: "close-2"}); !errors.Is(err, ErrPreviewNotFound) || rc.CommandID != "" {
		t.Fatalf("a close with no preview held: %+v %v", rc, err)
	}
	if _, _, ok := l.commandRow(t, run.ID, "admin", "close-2"); ok {
		t.Fatal("a close refused before it was taken left a row")
	}
	if _, _, err := e.PreviewCandidate(ctx, run.ID, "build", 1, "admin", Command{ID: "not an id"}); !errors.Is(err, ErrBadCommandID) {
		t.Fatalf("an id not of the pattern: %v", err)
	}
}

// CandidateIntact holds a checkout to its commit as git stores it: a
// tracked change git status does not show (skip-worktree) is a change, an
// untracked file is not, and HEAD elsewhere is no candidate.
func TestCandidateIntactSeesAHiddenChange(t *testing.T) {
	dir := newRepo(t)
	head, err := headCommit(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := CandidateIntact(t.Context(), dir, head)
	if err != nil || !ok {
		t.Fatalf("a clean checkout: %v %v", ok, err)
	}
	writeFile(t, dir+"/cache.txt", "untracked\n")
	if ok, err := CandidateIntact(t.Context(), dir, head); err != nil || !ok {
		t.Fatalf("an untracked file: %v %v", ok, err)
	}
	runGit(t, dir, "update-index", "--skip-worktree", "README")
	writeFile(t, dir+"/README", "hidden\n")
	if st := runGit(t, dir, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Fatalf("git status shows the change: %q", st)
	}
	if ok, err := CandidateIntact(t.Context(), dir, head); err != nil || ok {
		t.Fatalf("a change under skip-worktree: %v %v", ok, err)
	}
	runGit(t, dir, "update-index", "--no-skip-worktree", "README")
	runGit(t, dir, "checkout", "-q", "--", "README")
	writeFile(t, dir+"/later.txt", "later\n")
	commitAll(t, dir, "later")
	if ok, err := CandidateIntact(t.Context(), dir, head); err != nil || ok {
		t.Fatalf("HEAD past the commit: %v %v", ok, err)
	}
}
