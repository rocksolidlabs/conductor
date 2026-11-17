package api

import (
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
)

// A Brakeman turn's follow-up hands over no source: a body naming one,
// with the steps to reproduce, the evidence links or neither, is refused
// 400 invalid_request before anything is acted on (no act kept, no
// revision made), while the same follow-up without it is proposed with
// no source; a source of null names none. The guide's entry for the act
// offers no source, though the propose route takes one.
func TestAFollowupActHandsOverNoSource(t *testing.T) {
	f := newActFixture(t, []string{brakeman.ActPrepareFollowup}, nil)
	plans := func() int {
		t.Helper()
		ps, err := f.srv.dispatch.Plans(t.Context(), "CND-1")
		if err != nil {
			t.Fatal(err)
		}
		return len(ps)
	}
	before := plans()
	for i, src := range []any{
		map[string]any{"repro": map[string]any{"identity": "I_kwDOitem41", "sha256": "0000000000000000000000000000000000000000000000000000000000000000"}},
		map[string]any{"evidence": []string{"0123456789abcdef"}},
		map[string]any{},
		[]any{},
		"",
	} {
		status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "s-" + string(rune('a'+i)), "crew": f.crew.ID, "basedOn": f.plan, "source": src})
		if status != http.StatusBadRequest || codeOf(out) != "invalid_request" {
			t.Fatalf("a follow-up with the source %v: %d %v", src, status, out)
		}
	}
	if acts := f.acts(t); len(acts) != 0 {
		t.Fatalf("a refused follow-up was kept as an act: %+v", acts)
	}
	if n := plans(); n != before {
		t.Fatalf("a refused follow-up made a revision: %d, then %d", before, n)
	}
	status, out := f.asTurn(t, http.MethodPost, "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "s-null", "crew": f.crew.ID, "basedOn": f.plan, "source": nil})
	if status != http.StatusCreated {
		t.Fatalf("a follow-up with a null source: %d %v", status, out)
	}
	if _, body, _ := f.planBodyOf(t, entityOf(t, out, "plan")["id"].(string)); body.Source != nil {
		t.Fatalf("the follow-up hands over %+v", body.Source)
	}
	for _, op := range Operations() {
		if op.Name != brakeman.ActPrepareFollowup {
			continue
		}
		props, _ := op.Input["properties"].(map[string]any)
		if _, ok := props["source"]; ok || props["crew"] == nil {
			t.Fatalf("the guide's follow-up offers %v", props)
		}
		return
	}
	t.Fatal("no follow-up in the guide")
}
