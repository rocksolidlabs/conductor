package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// The attention route takes the turn a report names (turnId): a turn's end
// told twice, both naming the turn, with an Enter between, is recorded
// once, the session showing what the Enter left. A name past its bound or
// out of its shape is left out, never refused (the sender would lose the
// report): the report is applied and judged as one that names no turn, so
// its end told again after an Enter is a second entry.
func TestTheAttentionRouteTakesATurnsName(t *testing.T) {
	for _, tc := range []struct {
		name, turn string
		entries    int
	}{
		{"a Codex turn id", "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", 1},
		{"a name past its bound", strings.Repeat("a", 129), 2},
		{"a name out of its shape", "a turn", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			id := e.createSession("cat")
			local := e.local(id)
			agent := e.agentToken(id)
			path := "/api/sessions/" + id + "/attention"
			body := map[string]any{"state": "done", "message": "Refactored the parser.", "kind": "done", "turnId": tc.turn}
			if resp, out := e.do("POST", path, agent, body); resp.StatusCode != http.StatusOK {
				t.Fatalf("the done: %d %v", resp.StatusCode, out)
			}
			if a := local.Info().Attention; a.State != session.AttentionDone {
				t.Fatalf("the done shows %+v", a)
			}
			sub, err := local.Attach("", session.RoleControl, "", 80, 24, &gapSink{})
			if err != nil {
				t.Fatal(err)
			}
			defer local.Detach(sub)
			if err := local.Input(sub, []byte("\r")); err != nil {
				t.Fatal(err)
			}
			if resp, out := e.do("POST", path, agent, body); resp.StatusCode != http.StatusOK {
				t.Fatalf("the end told again: %d %v", resp.StatusCode, out)
			}
			n := 0
			for _, en := range local.Activity() {
				if en.Type == session.ActivityAttention && en.Message == "Refactored the parser." {
					n++
				}
			}
			if n != tc.entries {
				t.Fatalf("%d entries for the end told twice, want %d", n, tc.entries)
			}
			if a := local.Info().Attention; tc.entries == 1 && (a.State != session.AttentionNone || a.Source != session.SourceInput) {
				t.Fatalf("after the end told again the session shows %+v", a)
			}
		})
	}
}
