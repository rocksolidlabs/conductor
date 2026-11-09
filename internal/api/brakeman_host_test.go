package api

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// Brakeman's desk launches through ext.Host (docs/plans/brakeman.md,
// PR-17; Boxcar constraint 1): the composition root gives it a Host
// (SetBrakemanHost) whose LaunchTurn is launchTurn, and the desk calls
// nothing else to start a turn.

// recordingHost is an ext.Host that records each LaunchTurn and
// LaunchApproved it is asked and passes them on to the Host it wraps.
type recordingHost struct {
	ext.Host
	mu       sync.Mutex
	turns    []ext.TurnLaunch
	approved []ext.ApprovedLaunch
}

func (h *recordingHost) LaunchTurn(ctx context.Context, r ext.TurnLaunch) (ext.RunRef, error) {
	h.mu.Lock()
	h.turns = append(h.turns, r)
	h.mu.Unlock()
	return h.Host.LaunchTurn(ctx, r)
}

func (h *recordingHost) LaunchApproved(ctx context.Context, r ext.ApprovedLaunch) (ext.RunRef, error) {
	h.mu.Lock()
	h.approved = append(h.approved, r)
	h.mu.Unlock()
	return h.Host.LaunchApproved(ctx, r)
}

// launched answers the turns the host was asked to launch.
func (h *recordingHost) launched() []ext.TurnLaunch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.turns)
}

// TestTheDeskLaunchesThroughTheHost: a turn is the LaunchTurn of the Host
// the desk was given, with the configured agent, model and folder, the
// turn's line and label and its request; the run it answers is the
// request's.
func TestTheDeskLaunchesThroughTheHost(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	h := &recordingHost{Host: b.srv.BrakemanHost()}
	b.srv.SetBrakemanHost(h)
	b.enable(t, "bk", func(c map[string]any) { c["model"] = "sonnet" })
	id := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	req := b.waitState(t, id, brakeman.StateRunning)
	got := h.launched()
	want := ext.TurnLaunch{Agent: "bk", Model: "sonnet", Folder: b.root, Line: brakemanLine(req), Label: "Brakeman · briefing " + id, Request: id}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("the host was asked %+v, want one %+v", got, want)
	}
	if req.SessionID != sid || req.RunID == "" || req.RunID != b.runOf(t, sid) {
		t.Fatalf("the request's run %q and session %q, the turn's %q", req.RunID, req.SessionID, sid)
	}
}

// A Host that refuses the launch fails the request with its words, and
// nothing else launches the turn.
func TestTheDeskLaunchesThroughTheHostAlone(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.srv.SetBrakemanHost(refusingHost{Host: b.srv.BrakemanHost()})
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	req := b.waitState(t, id, brakeman.StateFailed)
	if req.Why != "the launch failed: ext: refused: the host launches no turn" {
		t.Fatalf("why %q", req.Why)
	}
	if n := len(b.srv.runs.List()); n != 0 {
		t.Fatalf("%d runs launched past the host", n)
	}
}

// refusingHost refuses every turn.
type refusingHost struct{ ext.Host }

func (refusingHost) LaunchTurn(context.Context, ext.TurnLaunch) (ext.RunRef, error) {
	return ext.RunRef{}, refused("the host launches no turn")
}

// TestHostLaunchTurnIsLaunchTurn: the desk's Host launches the same run
// launchTurn did, a read-only one-member run of class brakeman for the
// request, and answers it; it launches only the turn of the request
// holding the desk's slot, and a provider's Host launches none.
func TestHostLaunchTurnIsLaunchTurn(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	h := &recordingHost{Host: b.srv.BrakemanHost()}
	b.srv.SetBrakemanHost(h)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, _, l := b.turnSession(t, id)
	req := b.waitState(t, id, brakeman.StateRunning)
	r, ok := b.srv.runs.Get(req.RunID)
	if !ok || r.Class != crew.ClassBrakeman || r.Request != id || r.CrewID != brakemanMember || r.Yolo || r.Managed != nil || len(r.Members) != 1 || r.Members[0].SessionID != sid {
		t.Fatalf("the run %+v", r)
	}
	ad, _ := agents.Get("claude")
	ro := ad.ReadOnlyArgs()
	if argv := l.Launched().Args; len(argv) < len(ro) || !slices.Equal(argv[:len(ro)], ro) {
		t.Fatalf("the member's arguments %q do not begin with the read-only mode %q", argv, ro)
	}

	// A request that does not hold the slot is refused, as is a request
	// past its bounds, and a provider's Host launches no turn: nothing
	// launched.
	tl := h.launched()[0]
	tl.Request = "br-99"
	if _, err := b.srv.BrakemanHost().LaunchTurn(t.Context(), tl); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a request not holding the slot: %v", err)
	}
	if _, err := testHost(b.srv, t.Context(), brakemanMember).LaunchTurn(t.Context(), h.launched()[0]); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a provider's Host launched a turn: %v", err)
	}
	bad := h.launched()[0]
	bad.Folder = "relative"
	if _, err := b.srv.BrakemanHost().LaunchTurn(t.Context(), bad); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a turn past its bounds: %v", err)
	}
	if n := len(b.srv.runs.List()); n != 1 {
		t.Fatalf("%d runs, want the turn's one", n)
	}
}
