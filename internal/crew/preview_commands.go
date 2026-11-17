package crew

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A candidate's preview as a person's command (docs/plans/browser-
// evidence.md, "A candidate's preview"; PV1 PR-8): Preview this candidate
// and its Close, each with its receipt in the run's commands table, as the
// task commands keep theirs, for a run the engine holds and for one it
// opens a preview of from the ledger (preview_context.go). The receipt is
// taken (its row accepted) before anything of the preview is begun, and
// settled with what came of it: the same id sent again with the same
// content is answered by it, never a second open or close; other content
// is a conflict. A refusal before the command is taken (no such run, a
// run continued or stopped as the ledger has it, the ledger) leaves no
// receipt.

// The intents of the preview commands.
const (
	IntentPreviewOpen  = "preview.open"
	IntentPreviewClose = "preview.close"
)

// The refusals a preview command's failed receipt names, beside the task
// commands' (refusalOf): the attempt has no result to preview
// (not_candidate), the run no manifest that serves it (no_manifest), the
// candidate's preview is open already (preview_open) or was opened as
// often as it may be (preview_limit), the server has its previews open
// (previews_busy), the checkout there is not the commit's
// (preview_dirty), the preview is still opening (preview_opening) or not
// open (no_preview), and an open that failed once it had begun
// (preview_failed: its words say where).
const (
	RefusalNotCandidate   = "not_candidate"
	RefusalNoManifest     = "no_manifest"
	RefusalPreviewOpen    = "preview_open"
	RefusalPreviewLimit   = "preview_limit"
	RefusalPreviewsBusy   = "previews_busy"
	RefusalPreviewDirty   = "preview_dirty"
	RefusalPreviewOpening = "preview_opening"
	RefusalNoPreview      = "no_preview"
	RefusalPreviewFailed  = "preview_failed"
)

// previewRefusal names the refusal a preview command's err is.
func previewRefusal(err error) string {
	switch {
	case errors.Is(err, ErrNoCandidate):
		return RefusalNotCandidate
	case errors.Is(err, ErrNoPreviewManifest):
		return RefusalNoManifest
	case errors.Is(err, ErrPreviewOpen):
		return RefusalPreviewOpen
	case errors.Is(err, ErrPreviewLimit):
		return RefusalPreviewLimit
	case errors.Is(err, ErrPreviewsBusy):
		return RefusalPreviewsBusy
	case errors.Is(err, ErrPreviewRefused):
		return RefusalPreviewDirty
	case errors.Is(err, ErrPreviewOpening):
		return RefusalPreviewOpening
	case errors.Is(err, ErrPreviewNotFound):
		return RefusalNoPreview
	case errors.Is(err, ErrRunStopped), errors.Is(err, ErrRunContinued), errors.Is(err, ErrTaskNotFound), errors.Is(err, ErrRunNotFound):
		return refusalOf(err)
	}
	return RefusalPreviewFailed
}

// PreviewCandidate opens a preview of attempt n of the task as actor's
// command cmd (OpenPreview), its receipt kept in the run's commands table
// under intent preview.open: a repeat of cmd is answered by its receipt
// (Repeat), with the preview as it is now. The receipt names the preview
// opened (Receipt.Preview); one whose open was refused is failed, its
// refusal named (previewRefusal), and is returned with the error. A
// refusal before the command is taken returns no receipt.
func (e *Engine) PreviewCandidate(ctx context.Context, runID, taskID string, n int, actor string, cmd Command) (Receipt, PreviewState, error) {
	args := []string{taskID, strconv.Itoa(n)}
	var ps PreviewState
	rc, err := e.previewCommand(ctx, runID, actor, cmd, IntentPreviewOpen, args, func(ctx context.Context) (*PreviewRef, error) {
		var err error
		ps, err = e.OpenPreview(ctx, runID, taskID, n, actor)
		if err != nil {
			return nil, err
		}
		ref := ps.PreviewRef
		return &ref, nil
	})
	if rc.Repeat && rc.Preview != nil {
		ps = e.PreviewNow(*rc.Preview)
	}
	return rc, ps, err
}

// ClosePreviewCommand closes the preview seq of attempt n of the task as
// actor's command cmd (ClosePreview), its receipt kept under intent
// preview.close, a repeat answered by it. The preview is the one the
// engine holds: a Close of a preview it does not hold, of a run it holds
// no preview of and does not hold, is ErrPreviewNotFound with no receipt,
// a repeat of one taken before aside. The preview's state after the Close
// (ending, or ended once it is let go) is returned.
func (e *Engine) ClosePreviewCommand(ctx context.Context, runID, taskID string, n, seq int, actor string, cmd Command) (Receipt, PreviewState, error) {
	ref := PreviewRef{Run: runID, Task: taskID, N: n, Seq: seq}
	args := []string{taskID, strconv.Itoa(n), strconv.Itoa(seq)}
	rc, err := e.previewCommand(ctx, runID, actor, cmd, IntentPreviewClose, args, func(context.Context) (*PreviewRef, error) {
		if err := e.ClosePreview(runID, taskID, n, seq); err != nil {
			return nil, err
		}
		return &ref, nil
	})
	return rc, e.PreviewNow(ref), err
}

// previewCommand runs do as actor's command cmd of intent on the run (the
// one the engine holds, or the preview-only context of one it does not:
// for an open, made from the ledger when there is none; for a close, only
// one that holds a preview now), in two transactions as act's: the first
// takes the command (its row accepted: a repeat is answered by the receipt
// given before, other content is a conflict, the run's cap refuses it),
// then do runs, and the second settles the receipt with what came of it
// (done with the preview it names, or failed with its refusal). A context
// the command made and no preview kept is dropped again.
func (e *Engine) previewCommand(ctx context.Context, runID, actor string, cmd Command, intent string, args []string, do func(context.Context) (*PreviewRef, error)) (Receipt, error) {
	if !CommandIDPattern.MatchString(cmd.ID) {
		return Receipt{}, ErrBadCommandID
	}
	if e.ledger == nil {
		return Receipt{}, ErrNoLedger
	}
	fp := fingerprint(intent, args...)
	if prior, found, err := e.ledger.Receipt(ctx, runID, actor, cmd.ID); err != nil {
		return Receipt{}, err
	} else if found {
		return priorReceipt(prior, intent, fp)
	}
	var r *run
	if intent == IntentPreviewOpen {
		var err error
		if r, err = e.previewRun(ctx, runID); err != nil {
			return Receipt{}, err
		}
	} else {
		e.mu.Lock()
		r = e.runs[runID]
		if r == nil {
			r = e.previewRuns[runID]
		}
		e.mu.Unlock()
		if r == nil || !r.managed {
			return Receipt{}, ErrPreviewNotFound
		}
	}
	defer func() {
		e.mu.Lock()
		e.forgetPreviewRun(r)
		e.mu.Unlock()
	}()
	err := e.transact(ctx, r, func() tx {
		w := tx{events: []event{{"command.accepted", map[string]any{"actor": actor, "command": cmd.ID, "intent": intent, "args": args}}}}
		w.more = func(ltx LedgerTx) error {
			if _, found, err := ltx.Command(actor, cmd.ID, intent, fp); err != nil {
				return err
			} else if found {
				return errCommandRepeated
			}
			return nil
		}
		return w
	}, giveUp)
	switch {
	case errors.Is(err, errCommandRepeated):
		prior, found, rerr := e.ledger.Receipt(ctx, runID, actor, cmd.ID)
		if rerr != nil || !found {
			return Receipt{}, errors.Join(err, rerr)
		}
		return priorReceipt(prior, intent, fp)
	case errors.Is(err, ledger.ErrCommandConflict):
		return Receipt{}, ErrCommandConflict
	case errors.Is(err, ledger.ErrTooManyCommands):
		return Receipt{}, ErrTooManyCommands
	case err != nil:
		return Receipt{}, err
	}
	ref, cmdErr := do(ctx)
	out := Receipt{CommandID: cmd.ID, Intent: intent, Status: ledger.CommandDone, Preview: ref}
	if cmdErr != nil {
		out.Status, out.Error, out.Refusal = ledger.CommandFailed, cmdErr.Error(), previewRefusal(cmdErr)
	}
	// Settled whatever the asker's context does now: what was begun is
	// the receipt's to say.
	if err := e.transact(context.WithoutCancel(ctx), r, func() tx {
		out.Revision = r.revision + 1
		w := tx{events: []event{{"command." + intent, map[string]any{"actor": actor, "command": cmd.ID, "args": args, "status": out.Status, "error": out.Error}}}}
		w.more = func(ltx LedgerTx) error { return ltx.Settle(actor, cmd.ID, out.Status, out) }
		return w
	}, nil); err != nil {
		return Receipt{}, fmt.Errorf("the ledger could not settle the command's receipt: %w", errors.Join(err, cmdErr))
	}
	return out, cmdErr
}

// PreviewNow is the preview ref names as the engine holds it now, its
// idle clock left as it is: its state (resolved, ready or ending) and URL
// as Previewing gives them, or ended for one it no longer holds (another
// seq, or none).
func (e *Engine) PreviewNow(ref PreviewRef) PreviewState {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.previews[previewKey{ref.Run, ref.Task, ref.N}]
	if p == nil || p.seq != ref.Seq || p.previewing() == nil {
		return PreviewState{PreviewRef: ref, State: previewEnded}
	}
	return p.stateOf()
}

// OpenPreviews is the previews the engine holds now, for a refusal that
// names them (previews_busy).
func (e *Engine) OpenPreviews() []PreviewRef {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []PreviewRef{}
	for _, p := range e.previews {
		out = append(out, PreviewRef{Run: p.key.run, Task: p.key.task, N: p.key.n, Seq: p.seq})
	}
	return out
}

// PreviewsOf is the previews of the run's attempts the engine holds now,
// by task and attempt, as AttemptState.Previewing reports them: for a run
// it does not hold, whose task read is the ledger's, the preview-only
// context's.
func (e *Engine) PreviewsOf(runID string) map[string]map[int]*Previewing {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out map[string]map[int]*Previewing
	for k, p := range e.previews {
		pv := p.previewing()
		if k.run != runID || pv == nil {
			continue
		}
		if out == nil {
			out = map[string]map[int]*Previewing{}
		}
		if out[k.task] == nil {
			out[k.task] = map[int]*Previewing{}
		}
		out[k.task][k.n] = pv
	}
	return out
}

// CandidateIntact reports whether the checkout at dir is the candidate's
// commit as git stores it: HEAD at commit and no tracked path changed,
// compared through a fresh index of its own that carries no flag a
// command in the checkout could set to hide a change (no assume-unchanged,
// no skip-worktree: checkoutIntact). Untracked files are not changes of
// the commit. A capture of a preview checkout names its candidate only
// when this holds at the save.
func CandidateIntact(ctx context.Context, dir, commit string) (bool, error) {
	moved, changed, _, err := checkoutIntact(ctx, dir, commit)
	if err != nil {
		return false, err
	}
	return !moved && len(changed) == 0, nil
}
