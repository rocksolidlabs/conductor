package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A host is handed, with registered, the recipe of the agent it names
// (#293): what the catalog's entry for it says to watch on its screen, as
// a server session of it is started with; none for an agent the catalog
// does not have, none named, or one with nothing to watch.
func TestRegisteredCarriesTheAgentsRecipe(t *testing.T) {
	asker := catalog.Agent{ID: "asker", Name: "asker", Command: []string{"/bin/sh"},
		Signal:       &catalog.Signal{Kind: catalog.SignalBell, WorkingTitle: "^Working$"},
		TrustPrompt:  `Trust\s*this\s*folder\?`,
		TrustAnswers: []catalog.Answer{{Label: "Yes", Input: "\r"}, {Label: "No", Input: "\x1b[B\r"}},
		Questions:    []catalog.Question{{Prompt: `proceed\s*\(y\)`, Anytime: true, Answers: []catalog.Answer{{Label: "Yes, proceed", Input: "y"}}}}}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{asker})
	register := func(agentID string) map[string]any {
		_, reg := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop"}, proto.HostSession{Name: "hosted", AgentID: agentID, Command: []string{"x"}, Cwd: e.root, Cols: 80, Rows: 24}, "Bearer test-host-token")
		return reg
	}
	reg := register("asker")
	raw, _ := json.Marshal(reg["recipe"])
	var got proto.HostRecipe
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("recipe %s: %v", raw, err)
	}
	if !reflect.DeepEqual(got, *agents.Recipe(asker)) {
		t.Fatalf("recipe %+v, want %+v", got, *agents.Recipe(asker))
	}
	if got.TrustPrompt != asker.TrustPrompt || len(got.TrustAnswers) != 2 || got.WorkingTitle != "^Working$" || len(got.Questions) != 1 || !got.Questions[0].Anytime {
		t.Fatalf("recipe %+v", got)
	}
	for _, id := range []string{"", "not-in-the-catalog", "cat"} {
		if r, ok := register(id)["recipe"]; ok {
			t.Fatalf("agent %q: recipe %v", id, r)
		}
	}
}
