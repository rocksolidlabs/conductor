package exttest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// syncScript is a sync door script: Join answers what it was told, every
// other door ErrUnsupported.
type syncScript struct {
	ext.UnsupportedSync
	joined error
}

func (s syncScript) DispatchJoin(context.Context, string, string, string, int64, int64) error {
	return s.joined
}

// FakeHost's sync doors answer ErrUnsupported until a script is given,
// record each call by its door's name, answer the script's answer once
// given, refuse a row that is not valid before the script sees it, and
// answer the context's end once the host is closed.
func TestFakeHostSyncDoors(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	if err := h.DispatchJoin(ctx, "a1", "w", "d", 0, 0); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a door before a script: %v", err)
	}
	h.OnSync(syncScript{})
	if err := h.DispatchJoin(ctx, "a1", "w", "d", 0, 0); err != nil {
		t.Fatalf("a scripted join: %v", err)
	}
	if _, err := h.DispatchApply(ctx, []ext.DispatchChange{{Entity: "ticket"}}, nil, nil); err == nil || errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a row that is not valid handed to the script: %v", err)
	}
	if _, _, err := h.DispatchChanges(ctx, 0, 1); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a door the script leaves: %v", err)
	}
	if got := h.SyncCalls(); !slices.Equal(got, []string{"DispatchJoin", "DispatchJoin", "DispatchApply", "DispatchChanges"}) {
		t.Fatalf("the calls %v", got)
	}
	h.Close()
	if err := h.DispatchRetire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a door after the close: %v", err)
	}
}

// panickyStore is a sync store that panics when a door refuses it.
type panickyStore struct{ *FakeContinuity }

func (p panickyStore) Start(ctx context.Context, h ext.Host) error {
	if err := h.DispatchJoin(ctx, "a1", "w", "d", 0, 0); err != nil {
		panic("the join was refused")
	}
	return nil
}

// Continuity starts a store that is a Starter on a Host whose doors
// refuse, as every Host but the sync store's does: a store that panics on
// the refusal is reported; one that takes it is not.
func TestContinuityStartsTheStoreOnRefusedDoors(t *testing.T) {
	errs := record(t, func(rt T) { Continuity(rt, nil, panickyStore{NewFakeContinuity("panicky")}) })
	if !said(errs, "Start, its doors refused", "panicked") {
		t.Fatalf("a store that panics on a refused door: %v", errs)
	}
	if errs := record(t, func(rt T) { Continuity(rt, nil, NewFakeContinuity("calm")) }); len(errs) != 0 {
		t.Fatalf("a store that is no Starter: %v", errs)
	}
}

// remoteScript is a sync door script that keeps the records it is handed.
type remoteScript struct {
	ext.UnsupportedSync
	put *[]ext.Remote
}

func (s remoteScript) PutRemote(_ context.Context, r ext.Remote) error {
	*s.put = append(*s.put, r)
	return nil
}

// FakeHost's PutRemote takes another machine's host summary of its form
// and refuses one that is not, with the kind's ErrRefused, before the
// script sees it, as the server answers a provider.
func TestFakeHostPutRemoteJudgesTheHostSummary(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	var put []ext.Remote
	h.OnSync(remoteScript{put: &put})
	if err := h.PutRemote(ctx, SampleRemoteHosts("dev-1", "studio", nil)); err != nil {
		t.Fatalf("the sample summary: %v", err)
	}
	for name, body := range map[string][]byte{
		"a body that is no summary": []byte(`{"hosts":[]}`),
		"an unknown field":          []byte(`{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[],"user":"ana"}`),
	} {
		if err := h.PutRemote(ctx, SampleRemoteHosts("dev-1", "studio", body)); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
	if len(put) != 1 || put[0].Kind != ext.RemoteHosts {
		t.Fatalf("the script was handed %+v", put)
	}
}

// Continuity hands the store the host summary: store hosts, path summary,
// a HostSummary the server would take.
func TestContinuitySendsTheHostSummary(t *testing.T) {
	cs := NewFakeContinuity("summary")
	if errs := record(t, func(rt T) { Continuity(rt, nil, cs) }); len(errs) != 0 {
		t.Fatal(errs)
	}
	sent := 0
	for _, d := range cs.Docs() {
		if d.Doc.Store != "hosts" {
			continue
		}
		sent++
		if _, err := ext.ParseHostSummary(d.Doc.Data); d.Doc.Path != "summary" || d.Deleted || d.Doc.Rev < 1 || err != nil {
			t.Errorf("the summary document %+v: %v", d.Doc, err)
		}
	}
	if sent == 0 {
		t.Fatal("no host summary was sent")
	}
}
