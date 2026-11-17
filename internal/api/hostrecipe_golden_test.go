package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// hostRecipes is the recipe a host is sent (hostRecipe) for each agent of
// the built-in catalog, by id, as registered encodes it: the agent's id and
// its recipe's JSON, null for none.
func hostRecipes(t *testing.T) []byte {
	t.Helper()
	s := &Server{catalog: catalog.Default(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	type entry struct {
		Agent  string          `json:"agent"`
		Recipe json.RawMessage `json:"recipe"`
	}
	var out []entry
	for _, a := range catalog.Default().List() {
		b, err := json.Marshal(s.hostRecipe(a.ID, false))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, entry{Agent: a.ID, Recipe: b})
	}
	b, err := json.MarshalIndent(out, "", "\t")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// The recipe a host is sent for each agent of the built-in catalog is the
// one origin/main sent before Codex's MCP tool approval was a question
// (#716), byte for byte: the form is left out of what a host is sent, and
// its hosted session answers the form in its terminal, as it did then.
// testdata/host-recipes.json was written by origin/main at 66aff7ee, with
// this file copied into a checkout of it:
//
//	CONDUCTOR_HOST_RECIPES=$PWD/internal/api/testdata/host-recipes.json go test ./internal/api -run '^TestTheRecipeAHostIsSentIsMains$'
func TestTheRecipeAHostIsSentIsMains(t *testing.T) {
	got := hostRecipes(t)
	if out := os.Getenv("CONDUCTOR_HOST_RECIPES"); out != "" {
		if err := os.WriteFile(out, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(filepath.Join("testdata", "host-recipes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("the recipes a host is sent:\n%s\nwant:\n%s", got, want)
	}
}

// A host that reads forms (#970) is sent, for each agent of the built-in
// catalog, the recipe a server session of the agent is started with
// (agents.Recipe), whole: within the bounds a host checks (Check) and
// within proto.MaxHostRecipe, Codex's form among its questions.
func TestTheRecipeAHostThatReadsFormsIsSentIsTheServers(t *testing.T) {
	s := &Server{catalog: catalog.Default(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	forms := 0
	for _, a := range catalog.Default().List() {
		got, want := s.hostRecipe(a.ID, true), agents.Recipe(a)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: sent %+v, want %+v", a.ID, got, want)
		}
		if got == nil {
			continue
		}
		if err := got.Check(); err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
		if b, _ := json.Marshal(got); len(b) > proto.MaxHostRecipe {
			t.Fatalf("%s: %d bytes", a.ID, len(b))
		}
		for _, q := range got.Questions {
			if isForm(q) {
				forms++
			}
		}
	}
	if forms == 0 {
		t.Fatal("no recipe of the built-in catalog has a form")
	}
}
