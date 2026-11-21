package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// watchingSync is the fake sync store that is told when a shared
// project's block runs low.
type watchingSync struct {
	*fakeSync
	low chan string
}

func (w watchingSync) BlockLow(project string) { w.low <- project }

// syncDoorsEnv is a server whose sync store is the fake named cloud, and
// the Host it gives that store.
func syncDoorsEnv(t *testing.T, p ext.ContinuityStore) (*testEnv, *extHost) {
	t.Helper()
	e := syncEnv(t, syncDataDir(t), p)
	return e, &extHost{s: e.srv, name: p.Name(), ctx: context.Background()}
}

// Every Dispatch door answers ErrUnsupported on a Host that is not the
// sync store's (another provider's, Brakeman's desk's) and on a server
// whose store is not open; on the sync store's own, they act.
func TestHostDispatchDoorsOnlyForTheSyncStore(t *testing.T) {
	ctx := context.Background()
	e, own := syncDoorsEnv(t, newFakeSync())
	other := &extHost{s: e.srv, name: "another", ctx: context.Background()}
	desk := &extHost{s: e.srv, name: "cloud", ctx: context.Background(), desk: true}
	for _, h := range []*extHost{other, desk} {
		for name, call := range map[string]func() error{
			"DispatchJoin":   func() error { return h.DispatchJoin(ctx, "a1", "w1", "d1", 1_000_001, 1_100_000) },
			"DispatchRetire": func() error { return h.DispatchRetire(ctx) },
			"DispatchChanges": func() error {
				_, _, err := h.DispatchChanges(ctx, 0, 10)
				return err
			},
			"DispatchPlanView": func() error {
				_, err := h.DispatchPlanView(ctx, strings.Repeat("a", 64))
				return err
			},
			"DispatchApply": func() error {
				_, err := h.DispatchApply(ctx, nil, nil, nil)
				return err
			},
			"DispatchBlock": func() error { return h.DispatchBlock(ctx, "p", 1, 2, false) },
			"DispatchBlocks": func() error {
				_, err := h.DispatchBlocks(ctx)
				return err
			},
			"DispatchShared": func() error {
				_, err := h.DispatchShared(ctx)
				return err
			},
			"DispatchCompleteShare": func() error { return h.DispatchCompleteShare(ctx, "p", "w1", 0) },
			"DispatchConfirmShare":  func() error { return h.DispatchConfirmShare(ctx, "p") },
			"DispatchRefuseShare":   func() error { return h.DispatchRefuseShare(ctx, "p", "no") },
			"DispatchUnshared":      func() error { return h.DispatchUnshared(ctx, "w1", "no") },
			"DispatchDormant":       func() error { return h.DispatchDormant(ctx, "w1", "no") },
			"DispatchNames":         func() error { return h.DispatchNames(ctx, map[string]string{"a1": "studio"}) },
		} {
			if err := call(); !errors.Is(err, ext.ErrUnsupported) {
				t.Errorf("%s on %s (desk %v): %v", name, h.name, h.desk, err)
			}
		}
	}
	if err := own.DispatchJoin(ctx, "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatalf("the sync store's join: %v", err)
	}
	if _, _, err := own.DispatchChanges(ctx, 0, ext.MaxDispatchBatch+1); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a page past the batch: %v", err)
	}
	if _, err := own.DispatchApply(ctx, []ext.DispatchChange{{Entity: "launch", EntityID: "l", Op: "create", Actor: "admin", OpID: "o", Origin: "a1"}}, nil, nil); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a row that never leaves its machine: %v", err)
	}
	// A switchyard, which keeps no Dispatch store, its sync store named.
	p := newFakeSync()
	providers := local.Fill(ext.Providers{Continuity: p, All: []ext.Entry{{Kind: ext.KindSync, Name: p.Name(), Value: p}}})
	yard := newTestEnvAgents(t, func(c *config.Config) { c.Switchyard.Enabled = true }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, WithExtensions(providers))
	onYard := &extHost{s: yard.srv, name: p.Name(), ctx: context.Background()}
	if err := onYard.DispatchRetire(ctx); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a door on a switchyard: %v", err)
	}
}

// The sync store's doors carry a shared project from one machine to
// another: the history handed out with each row's references, applied on
// the other, the share completed past a moved history refused as
// ErrShareMoved, the names kept.
func TestSyncStoreCarriesASharedProject(t *testing.T) {
	ctx := context.Background()
	e, own := syncDoorsEnv(t, newFakeSync())
	if err := own.DispatchJoin(ctx, "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatal(err)
	}
	other, err := dispatch.Open(filepath.Join(t.TempDir(), "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	if err := other.Join(ctx, "a1", "w1", "dev-a", 1_000_001, 1_100_000); err != nil {
		t.Fatal(err)
	}
	op := func(id string) dispatch.Op { return dispatch.Op{Actor: "admin", ID: id} }
	if rc, err := other.CreateProject(ctx, op("p"), dispatch.NewProject{Key: "ODD", Name: "Odd"}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("%+v %v", rc, err)
	}
	if err := other.ShareForTest(ctx, "ODD", "w1"); err != nil {
		t.Fatal(err)
	}
	rc, err := other.CreateTicket(ctx, op("t"), dispatch.NewTicket{Project: "ODD", Title: "Made elsewhere"})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("%+v %v", rc, err)
	}
	rows, _, err := other.HandedChanges(ctx, 0, dispatch.MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	var batch []ext.DispatchChange
	for _, c := range rows {
		d := toDispatchChange(c)
		if err := d.Valid(); err != nil {
			t.Fatalf("a row handed out is not valid: %v", err)
		}
		batch = append(batch, d)
	}
	got, err := own.DispatchApply(ctx, batch, nil, map[string]string{rows[0].EntityID: "ODD"})
	if err != nil || got.Applied != len(batch) {
		t.Fatalf("applied %+v %v", got, err)
	}
	e.expect(t, "GET", "/api/dispatch/tickets/"+rc.Key, nil, http.StatusOK, "")
	// The server's own project shared through the route and the doors.
	e.newDispatchProject(t, "MINE", nil)
	mine := e.expect(t, "POST", "/api/dispatch/projects/MINE/share", map[string]any{"opId": opID()}, http.StatusOK, "")
	project := entityOf(t, mine, "project")
	share, _ := project["share"].(map[string]any)
	if share == nil || share["state"] != dispatch.SharePending {
		t.Fatalf("the shared project's read: %v", project)
	}
	id := project["id"].(string)
	e.newDispatchTicket(t, "MINE", "after the share asked", nil)
	if err := own.DispatchCompleteShare(ctx, id, "w1", 0); !errors.As(err, new(ext.ErrShareMoved)) {
		t.Fatalf("a share completed short of its history: %v", err)
	}
	head, err := e.srv.dispatch.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := own.DispatchCompleteShare(ctx, id, "w1", head); err != nil {
		t.Fatal(err)
	}
	if err := own.DispatchConfirmShare(ctx, id); err != nil {
		t.Fatal(err)
	}
	shared, err := own.DispatchShared(ctx)
	if err != nil || len(shared) != 2 {
		t.Fatalf("the shared projects %+v %v", shared, err)
	}
	if err := own.DispatchNames(ctx, map[string]string{"a1": "studio"}); err != nil {
		t.Fatal(err)
	}
	if err := own.DispatchNames(ctx, map[string]string{"A1": "studio"}); !errors.Is(err, ext.ErrRefused) && err == nil {
		t.Fatalf("a name of no tag: %v", err)
	}
}

// A share needs a registered sync provider: with none, 409 no_sync and
// nothing written; a rekey needs none.
func TestShareNeedsASyncProvider(t *testing.T) {
	e := dispatchEnv(t)
	e.newDispatchProject(t, "CND", nil)
	e.expect(t, "POST", "/api/dispatch/projects/CND/share", map[string]any{"opId": opID()}, http.StatusConflict, "no_sync")
	out := e.expect(t, "GET", "/api/dispatch/projects/CND", nil, http.StatusOK, "")
	if p := entityOf(t, out, "project"); p["share"] != nil {
		t.Fatalf("a share without a provider: %v", p)
	}
	e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": opID(), "key": "NEW"}, http.StatusOK, "")
}

// The share and rekey routes: bodies at most 256 bytes, unknown fields
// refused, each refusal in its code.
func TestShareAndRekeyRoutes(t *testing.T) {
	e, own := syncDoorsEnv(t, newFakeSync())
	e.newDispatchProject(t, "CND", nil)
	e.newDispatchProject(t, "OPS", nil)
	e.expect(t, "POST", "/api/dispatch/projects/CND/share", map[string]any{"opId": opID(), "byName": strings.Repeat("n", 300)}, http.StatusRequestEntityTooLarge, "bound")
	e.expect(t, "POST", "/api/dispatch/projects/CND/share", map[string]any{"opId": opID(), "extra": 1}, http.StatusBadRequest, "invalid_request")
	e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": opID(), "key": "OPS"}, http.StatusConflict, dispatch.RefusalKeyTaken)
	e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": opID(), "key": "lower"}, http.StatusBadRequest, dispatch.RefusalInvalid)
	e.expect(t, "POST", "/api/dispatch/projects/NOPE/rekey", map[string]any{"opId": opID(), "key": "NEW"}, http.StatusNotFound, dispatch.RefusalNotFound)
	op := opID()
	out := e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": op, "key": "NEW"}, http.StatusOK, "")
	if p := entityOf(t, out, "project"); p["key"] != "NEW" {
		t.Fatalf("the project after the rekey: %v", p)
	}
	again := e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": op, "key": "NEW"}, http.StatusOK, "")
	if receiptOf(t, again)["repeat"] != true {
		t.Fatalf("a repeat: %v", again)
	}
	if err := own.DispatchJoin(context.Background(), "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatal(err)
	}
	e.expect(t, "POST", "/api/dispatch/projects/NEW/share", map[string]any{"opId": opID()}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/projects/NEW/rekey", map[string]any{"opId": opID(), "key": "NEWER"}, http.StatusConflict, dispatch.RefusalShareInProgress)
	e.expect(t, "POST", "/api/dispatch/projects/NEW/share", map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalInvalid)
}

// The sync store that watches its blocks is told once a create took a
// shared project's block past its half, after the commit.
func TestBlockLowCalledAfterCommit(t *testing.T) {
	ctx := context.Background()
	w := watchingSync{fakeSync: newFakeSync(), low: make(chan string, 4)}
	e, own := syncDoorsEnv(t, w)
	if err := own.DispatchJoin(ctx, "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatal(err)
	}
	p := e.newDispatchProject(t, "CND", nil)
	id := p["id"].(string)
	if err := e.srv.dispatch.ShareForTest(ctx, "CND", "w1"); err != nil {
		t.Fatal(err)
	}
	if err := own.DispatchBlock(ctx, id, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	e.newDispatchTicket(t, "CND", "first", nil)
	select {
	case got := <-w.low:
		t.Fatalf("told at the block's first number: %s", got)
	default:
	}
	e.newDispatchTicket(t, "CND", "second", nil)
	select {
	case got := <-w.low:
		if got != id {
			t.Fatalf("told of %s", got)
		}
	case <-time.After(testWait):
		t.Fatal("not told the block runs low")
	}
	blocks, err := own.DispatchBlocks(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].Left != 0 {
		t.Fatalf("the blocks %+v %v", blocks, err)
	}
}
