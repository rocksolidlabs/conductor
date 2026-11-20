package exttest

import (
	"context"
	"fmt"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"
)

// FakeHost's doors of the sync store (docs/plans/sync-seams.md): each call
// recorded by its door's name and answered by the script OnSync gave,
// ext.UnsupportedSync's ErrUnsupported until one is given, as a Host that
// is not the sync store's answers them.

// OnSync has every sync door answer d's answer; nil for ErrUnsupported.
func (h *FakeHost) OnSync(d ext.SyncDoors) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sync = d
}

// SyncCalls is each sync door called so far, by name, in order.
func (h *FakeHost) SyncCalls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.syncCalls)
}

// door records the call and is the script to answer it, or the
// context's end once the host is closed.
func (h *FakeHost) door(name string) (ext.SyncDoors, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.syncCalls = append(h.syncCalls, name)
	if h.closed {
		return nil, context.Canceled
	}
	if h.sync == nil {
		return ext.UnsupportedSync{}, nil
	}
	return h.sync, nil
}

var _ ext.SyncDoors = (*FakeHost)(nil)

func (h *FakeHost) DispatchJoin(ctx context.Context, tag, workspace, device string, overflowFrom, overflowTo int64) error {
	d, err := h.door("DispatchJoin")
	if err != nil {
		return err
	}
	return d.DispatchJoin(ctx, tag, workspace, device, overflowFrom, overflowTo)
}

func (h *FakeHost) DispatchRetire(ctx context.Context) error {
	d, err := h.door("DispatchRetire")
	if err != nil {
		return err
	}
	return d.DispatchRetire(ctx)
}

func (h *FakeHost) DispatchChanges(ctx context.Context, after int64, n int) ([]ext.DispatchChange, int64, error) {
	d, err := h.door("DispatchChanges")
	if err != nil {
		return nil, 0, err
	}
	return d.DispatchChanges(ctx, after, n)
}

func (h *FakeHost) DispatchPlanView(ctx context.Context, viewDigest string) ([]byte, error) {
	d, err := h.door("DispatchPlanView")
	if err != nil {
		return nil, err
	}
	return d.DispatchPlanView(ctx, viewDigest)
}

func (h *FakeHost) DispatchApply(ctx context.Context, rows []ext.DispatchChange, views map[string][]byte, keys map[string]string) (ext.DispatchApplied, error) {
	d, err := h.door("DispatchApply")
	if err != nil {
		return ext.DispatchApplied{}, err
	}
	for _, r := range rows {
		if err := r.Valid(); err != nil {
			return ext.DispatchApplied{}, fmt.Errorf("exttest: DispatchApply handed a row that is not valid: %w", err)
		}
	}
	return d.DispatchApply(ctx, rows, views, keys)
}

func (h *FakeHost) DispatchBlock(ctx context.Context, project string, from, to int64, next bool) error {
	d, err := h.door("DispatchBlock")
	if err != nil {
		return err
	}
	return d.DispatchBlock(ctx, project, from, to, next)
}

func (h *FakeHost) DispatchBlocks(ctx context.Context) ([]ext.BlockState, error) {
	d, err := h.door("DispatchBlocks")
	if err != nil {
		return nil, err
	}
	return d.DispatchBlocks(ctx)
}

func (h *FakeHost) DispatchShared(ctx context.Context) ([]ext.SharedProject, error) {
	d, err := h.door("DispatchShared")
	if err != nil {
		return nil, err
	}
	return d.DispatchShared(ctx)
}

func (h *FakeHost) DispatchCompleteShare(ctx context.Context, project, workspace string, upto int64) error {
	d, err := h.door("DispatchCompleteShare")
	if err != nil {
		return err
	}
	return d.DispatchCompleteShare(ctx, project, workspace, upto)
}

func (h *FakeHost) DispatchConfirmShare(ctx context.Context, project string) error {
	d, err := h.door("DispatchConfirmShare")
	if err != nil {
		return err
	}
	return d.DispatchConfirmShare(ctx, project)
}

func (h *FakeHost) DispatchRefuseShare(ctx context.Context, project, words string) error {
	d, err := h.door("DispatchRefuseShare")
	if err != nil {
		return err
	}
	return d.DispatchRefuseShare(ctx, project, words)
}

func (h *FakeHost) DispatchUnshared(ctx context.Context, workspace, words string) error {
	d, err := h.door("DispatchUnshared")
	if err != nil {
		return err
	}
	return d.DispatchUnshared(ctx, workspace, words)
}

func (h *FakeHost) DispatchDormant(ctx context.Context, workspace, words string) error {
	d, err := h.door("DispatchDormant")
	if err != nil {
		return err
	}
	return d.DispatchDormant(ctx, workspace, words)
}

func (h *FakeHost) DispatchNames(ctx context.Context, names map[string]string) error {
	d, err := h.door("DispatchNames")
	if err != nil {
		return err
	}
	return d.DispatchNames(ctx, names)
}

func (h *FakeHost) SetUIState(ctx context.Context, name string, data []byte) error {
	d, err := h.door("SetUIState")
	if err != nil {
		return err
	}
	return d.SetUIState(ctx, name, data)
}

func (h *FakeHost) SyncReplay(ctx context.Context) error {
	d, err := h.door("SyncReplay")
	if err != nil {
		return err
	}
	return d.SyncReplay(ctx)
}

func (h *FakeHost) SyncResend(ctx context.Context, store, path string) error {
	d, err := h.door("SyncResend")
	if err != nil {
		return err
	}
	return d.SyncResend(ctx, store, path)
}

func (h *FakeHost) RespondDecision(ctx context.Context, r ext.HeldAnswer) (ext.AnswerOutcome, error) {
	d, err := h.door("RespondDecision")
	if err != nil {
		return ext.AnswerOutcome{}, err
	}
	return d.RespondDecision(ctx, r)
}

func (h *FakeHost) SyncDelivery(ctx context.Context, decision, responseID string) (ext.AnswerOutcome, string, bool, error) {
	d, err := h.door("SyncDelivery")
	if err != nil {
		return ext.AnswerOutcome{}, "", false, err
	}
	return d.SyncDelivery(ctx, decision, responseID)
}

func (h *FakeHost) LastDecision(ctx context.Context) (int64, error) {
	d, err := h.door("LastDecision")
	if err != nil {
		return 0, err
	}
	return d.LastDecision(ctx)
}

func (h *FakeHost) PutRemote(ctx context.Context, r ext.Remote) error {
	d, err := h.door("PutRemote")
	if err != nil {
		return err
	}
	return d.PutRemote(ctx, r)
}

func (h *FakeHost) DeleteRemote(ctx context.Context, kind, device, id string) error {
	d, err := h.door("DeleteRemote")
	if err != nil {
		return err
	}
	return d.DeleteRemote(ctx, kind, device, id)
}

func (h *FakeHost) ClearRemote(ctx context.Context) error {
	d, err := h.door("ClearRemote")
	if err != nil {
		return err
	}
	return d.ClearRemote(ctx)
}
