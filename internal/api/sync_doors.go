package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// Dispatch's doors for the sync store (docs/plans/sync-seams.md, OS-1b):
// the rows a shared project's history hands out and the rows another
// machine applies, the share's cut-over, the numbers' blocks and the
// other machines' names. Only the Host given to the registered sync store
// (the ContinuityStore's slot) opens them; every other provider's Host,
// Brakeman's desk's and a switchyard's answer ErrUnsupported.

// syncName is the registered sync store's name, "" for none.
func (s *Server) syncName() string {
	for _, e := range s.exts.all {
		if e.Kind == ext.KindSync {
			return e.Name
		}
	}
	return ""
}

// syncStore is the Dispatch store this Host's doors act on, or why not.
func (h *extHost) syncStore() (*dispatch.Store, error) {
	if err := h.gone(); err != nil {
		return nil, err
	}
	switch {
	case h.desk || h.name == "" || h.name != h.s.syncName():
		return nil, fmt.Errorf("%w: only the sync store opens Dispatch's doors", ext.ErrUnsupported)
	case h.s.cfg.Switchyard.Enabled:
		return nil, fmt.Errorf("%w: a switchyard keeps no Dispatch store", ext.ErrUnsupported)
	case h.s.dispatch == nil:
		return nil, fmt.Errorf("%w: this server keeps no Dispatch store", ext.ErrUnsupported)
	}
	return h.s.dispatch, nil
}

// syncRefused wraps a store's refusal of what a provider handed it.
func syncRefused(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	for _, e := range []error{dispatch.ErrNotReplicated, dispatch.ErrNotShared, dispatch.ErrHeldFull, dispatch.ErrBadView, dispatch.ErrBadActor,
		dispatch.ErrBadTag, dispatch.ErrBadReplica, dispatch.ErrNotJoined, dispatch.ErrNoSuchProject, dispatch.ErrBadQuery} {
		if errors.Is(err, e) {
			return fmt.Errorf("%w: %v", ext.ErrRefused, err)
		}
	}
	return err
}

// DispatchJoin is dispatch.Store.Join.
func (h *extHost) DispatchJoin(ctx context.Context, tag, workspace, device string, overflowFrom, overflowTo int64) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	return syncRefused(ds.Join(ctx, tag, workspace, device, overflowFrom, overflowTo))
}

// DispatchRetire is dispatch.Store.Retire.
func (h *extHost) DispatchRetire(ctx context.Context) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	return syncRefused(ds.Retire(ctx))
}

// DispatchChanges hands at most n rows of the shared projects' history
// (and the people's) after the position after, in seq order, within
// ext.MaxDispatchBytes encoded, each with what it needs (Refs), and the
// position to ask from next.
func (h *extHost) DispatchChanges(ctx context.Context, after int64, n int) ([]ext.DispatchChange, int64, error) {
	ds, err := h.syncStore()
	if err != nil {
		return nil, 0, err
	}
	if n < 1 || n > ext.MaxDispatchBatch || after < 0 {
		return nil, 0, fmt.Errorf("%w: a page of 1 to %d rows after a position of 0 or more", ext.ErrRefused, ext.MaxDispatchBatch)
	}
	rows, next, err := ds.HandedChanges(ctx, after, n)
	if err != nil {
		return nil, 0, syncRefused(err)
	}
	out := make([]ext.DispatchChange, 0, len(rows))
	size := 0
	for _, c := range rows {
		// The copied-output check's verdicts (OS-1c): a row written before
		// the check is judged now, and one still pending ends the page
		// before it, so the provider's cursor never passes it.
		c.Output, c.PriorOutput = h.s.handedVerdicts(ctx, c)
		if c.Output == verdictPending || c.PriorOutput == verdictPending {
			next = c.Seq - 1
			break
		}
		d := toDispatchChange(c)
		b, err := json.Marshal(d)
		if err != nil {
			return nil, 0, err
		}
		if size += len(b); size > ext.MaxDispatchBytes && len(out) > 0 {
			next = out[len(out)-1].Seq
			break
		}
		out = append(out, d)
	}
	return out, next, nil
}

// toDispatchChange is a row as a provider takes it.
func toDispatchChange(c dispatch.ChangeRow) ext.DispatchChange {
	return ext.DispatchChange{Seq: c.Seq, Entity: c.Entity, EntityID: c.EntityID, TicketID: c.TicketID, ProjectID: c.ProjectID, Field: c.Field, Op: c.Op,
		Value: c.Value, Prior: c.Prior, At: c.At, Actor: c.Actor, By: c.By, OpID: c.OpID, N: c.N, Origin: c.Origin, RecoveredBy: c.RecoveredBy,
		ViewDigest: c.ViewDigest, Remote: c.Remote, Refs: dispatch.Refs(c), OutputLike: c.Output == "output", PriorOutputLike: c.PriorOutput == "output"}
}

// DispatchPlanView is dispatch.Store.RevisionView.
func (h *extHost) DispatchPlanView(ctx context.Context, viewDigest string) ([]byte, error) {
	ds, err := h.syncStore()
	if err != nil {
		return nil, err
	}
	v, err := ds.RevisionView(ctx, viewDigest)
	if errors.Is(err, dispatch.ErrNotFound) {
		return nil, ext.ErrNotFound
	}
	return v, err
}

// DispatchApply applies another machine's rows: at most
// ext.MaxDispatchBatch, within ext.MaxDispatchBytes, each row Valid, each
// view at most ext.MaxPlanView, each registry key a project's key. What a
// row needs is computed here again (dispatch.Refs), never taken from the
// provider.
func (h *extHost) DispatchApply(ctx context.Context, rows []ext.DispatchChange, views map[string][]byte, keys map[string]string) (ext.DispatchApplied, error) {
	ds, err := h.syncStore()
	if err != nil {
		return ext.DispatchApplied{}, err
	}
	if len(rows) > ext.MaxDispatchBatch {
		return ext.DispatchApplied{}, fmt.Errorf("%w: %d rows, more than %d", ext.ErrRefused, len(rows), ext.MaxDispatchBatch)
	}
	size := 0
	in := make([]dispatch.ChangeRow, 0, len(rows))
	for _, d := range rows {
		if err := d.Valid(); err != nil {
			return ext.DispatchApplied{}, err
		}
		b, err := json.Marshal(d)
		if err != nil {
			return ext.DispatchApplied{}, err
		}
		if size += len(b); size > ext.MaxDispatchBytes {
			return ext.DispatchApplied{}, fmt.Errorf("%w: a batch past %d bytes", ext.ErrRefused, ext.MaxDispatchBytes)
		}
		in = append(in, dispatch.ChangeRow{Entity: d.Entity, EntityID: d.EntityID, TicketID: d.TicketID, ProjectID: d.ProjectID, Field: d.Field, Op: d.Op,
			Value: d.Value, Prior: d.Prior, At: d.At, Actor: d.Actor, By: d.By, OpID: d.OpID, N: d.N, Origin: d.Origin, RecoveredBy: d.RecoveredBy,
			ViewDigest: d.ViewDigest})
	}
	for digest, v := range views {
		if len(v) > ext.MaxPlanView {
			return ext.DispatchApplied{}, fmt.Errorf("%w: the view %s past %d bytes", ext.ErrRefused, digest, ext.MaxPlanView)
		}
	}
	for id, key := range keys {
		if !dispatch.KeyPattern.MatchString(key) || !dispatch.ValidID(id) {
			return ext.DispatchApplied{}, fmt.Errorf("%w: the registry key %q of %q", ext.ErrRefused, key, id)
		}
	}
	got, err := ds.ApplyRemote(ctx, in, views, keys)
	if err != nil {
		return ext.DispatchApplied{}, syncRefused(err)
	}
	return ext.DispatchApplied{Applied: got.Applied, Held: got.Held, Words: got.Words}, nil
}

// DispatchBlock is dispatch.Store.SetBlock.
func (h *extHost) DispatchBlock(ctx context.Context, project string, from, to int64, next bool) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	return syncRefused(ds.SetBlock(ctx, project, from, to, next))
}

// DispatchBlocks is each shared project's blocks.
func (h *extHost) DispatchBlocks(ctx context.Context) ([]ext.BlockState, error) {
	ds, err := h.syncStore()
	if err != nil {
		return nil, err
	}
	bs, err := ds.Blocks(ctx)
	out := make([]ext.BlockState, 0, len(bs))
	for _, b := range bs {
		out = append(out, ext.BlockState{Project: b.Project, From: b.From, To: b.To, Next: b.Next, NextFrom: b.NextFrom, NextTo: b.NextTo, Left: b.Left})
	}
	return out, err
}

// DispatchShared is each project with a share.
func (h *extHost) DispatchShared(ctx context.Context) ([]ext.SharedProject, error) {
	ds, err := h.syncStore()
	if err != nil {
		return nil, err
	}
	ps, err := ds.Shared(ctx)
	out := make([]ext.SharedProject, 0, len(ps))
	for _, p := range ps {
		out = append(out, ext.SharedProject{ID: p.ID, Key: p.Key, Name: p.Name, State: p.State, Words: p.Words, ReservedTo: p.ReservedTo, Highest: p.Highest})
	}
	return out, err
}

// DispatchCompleteShare is dispatch.Store.CompleteShare, its move past
// the position given as ext.ErrShareMoved.
func (h *extHost) DispatchCompleteShare(ctx context.Context, project, workspace string, upto int64) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	err = ds.CompleteShare(ctx, project, workspace, upto)
	var moved dispatch.ErrShareMoved
	if errors.As(err, &moved) {
		return ext.ErrShareMoved{Newest: moved.Newest}
	}
	return syncRefused(err)
}

// DispatchConfirmShare is dispatch.Store.ConfirmShare.
func (h *extHost) DispatchConfirmShare(ctx context.Context, project string) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	return syncRefused(ds.ConfirmShare(ctx, project))
}

// DispatchRefuseShare is dispatch.Store.RefuseShare.
func (h *extHost) DispatchRefuseShare(ctx context.Context, project, words string) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	return syncRefused(ds.RefuseShare(ctx, project, words))
}

// DispatchUnshared is dispatch.Store.Unshared.
func (h *extHost) DispatchUnshared(ctx context.Context, workspace, words string) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	return syncRefused(ds.Unshared(ctx, workspace, words))
}

// DispatchDormant is dispatch.Store.Dormant.
func (h *extHost) DispatchDormant(ctx context.Context, workspace, words string) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	return syncRefused(ds.Dormant(ctx, workspace, words))
}

// DispatchNames is dispatch.Store.SetNames.
func (h *extHost) DispatchNames(ctx context.Context, names map[string]string) error {
	ds, err := h.syncStore()
	if err != nil {
		return err
	}
	return syncRefused(ds.SetNames(ctx, names))
}

// syncHost says whether this Host is the registered sync store's, on a
// server that is not a switchyard.
func (h *extHost) syncHost() error {
	if err := h.gone(); err != nil {
		return err
	}
	switch {
	case h.desk || h.name == "" || h.name != h.s.syncName():
		return fmt.Errorf("%w: only the sync store opens sync's doors", ext.ErrUnsupported)
	case h.s.cfg.Switchyard.Enabled:
		return fmt.Errorf("%w: a switchyard keeps no state of the app's screens", ext.ErrUnsupported)
	}
	return nil
}

// SetUIState keeps the UI document name (layout or palette) as another
// machine left it, with its at and by: refused for one not newer than
// the one held (ui_state.go).
func (h *extHost) SetUIState(ctx context.Context, name string, data []byte) error {
	if err := h.syncHost(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.s.setUIFromElsewhere(name, data)
}

// SyncReplay asks the adapter for a replay in a new epoch, as its own
// overflow does; it returns once the request is recorded.
func (h *extHost) SyncReplay(ctx context.Context) error {
	if err := h.syncHost(); err != nil {
		return err
	}
	if h.s.syncs == nil {
		return fmt.Errorf("%w: this server hands nothing to a sync store", ext.ErrUnsupported)
	}
	h.s.syncs.requestReplay()
	return ctx.Err()
}

// SyncResend marks one document pending again as if its source had
// committed: a data document of the allowlist, a run's pointer, a
// Dispatch entity's doc or its log, or a decision. A path of none of
// those is refused.
func (h *extHost) SyncResend(ctx context.Context, store, path string) error {
	if err := h.syncHost(); err != nil {
		return err
	}
	a := h.s.syncs
	if a == nil {
		return fmt.Errorf("%w: this server hands nothing to a sync store", ext.ErrUnsupported)
	}
	ok := false
	switch store {
	case syncData:
		ok = syncDataDoc.MatchString(path)
	case syncLedger:
		ok = crew.ValidRunID(path)
	case syncDispatch:
		ok = path == syncLogPath || dispatchDocPath.MatchString(path)
	case syncDecisionStore:
		_, err := parseDecisionPath(path)
		ok = err == nil
	}
	if !ok {
		return fmt.Errorf("%w: no document %s/%s is handed to a sync store", ext.ErrRefused, store, path)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mark(syncKey{store, path}, 0, nil)
	return nil
}

// dispatchDocPath is a Dispatch entity's doc's path: its kind, a slash,
// its id.
var dispatchDocPath = regexp.MustCompile(`^[a-z]+/[0-9a-f]{1,64}$`)
