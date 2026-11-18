package exttest

import (
	"context"
	"fmt"

	"github.com/rocksolidlabs/conductor/ext"
)

// Run answers ext.ErrNotFound: the fake keeps no runs of a provider's own
// yet.
func (h *FakeHost) Run(ctx context.Context, id string) (ext.RunStatus, error) {
	if err := h.enter(ctx); err != nil {
		return ext.RunStatus{}, err
	}
	return ext.RunStatus{}, fmt.Errorf("%w: no run %s", ext.ErrNotFound, id)
}

// RunByOp answers ext.ErrNotFound: the fake keeps no runs of a provider's
// own yet.
func (h *FakeHost) RunByOp(ctx context.Context, opID string) (ext.RunStatus, error) {
	if err := h.enter(ctx); err != nil {
		return ext.RunStatus{}, err
	}
	return ext.RunStatus{}, fmt.Errorf("%w: no run of the operation %s", ext.ErrNotFound, opID)
}

// StopRun answers ext.ErrNotFound: the fake keeps no runs of a provider's
// own yet.
func (h *FakeHost) StopRun(ctx context.Context, r ext.StopRequest) error {
	if err := h.enter(ctx); err != nil {
		return err
	}
	return fmt.Errorf("%w: no run %s", ext.ErrNotFound, r.Run)
}
