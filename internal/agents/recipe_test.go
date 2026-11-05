package agents

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Every built-in agent's recipe (#293) is within what a host takes and
// compiles as its server sessions are started with it: Codex's trust
// question, startup and anytime questions and working title, Claude Code's
// dialogs read off the screen and its confirmed submissions; an agent with
// nothing to watch has none.
func TestRecipeOfTheBuiltInAgents(t *testing.T) {
	for _, a := range catalog.Default().List() {
		r := Recipe(a)
		if r == nil {
			continue
		}
		if err := r.Check(); err != nil {
			t.Errorf("%s: %v", a.ID, err)
		}
		if b, _ := json.Marshal(r); len(b) > proto.MaxHostRecipe {
			t.Errorf("%s: the recipe is %d bytes, past %d", a.ID, len(b), proto.MaxHostRecipe)
		}
		w, err := CompileRecipe(r)
		if err != nil {
			t.Errorf("%s: %v", a.ID, err)
		}
		if len(w.Questions) != len(a.Questions) || len(w.TrustAnswers) != len(a.TrustAnswers) || (a.TrustPrompt != "") != (w.TrustPattern != nil) {
			t.Errorf("%s: watch %+v", a.ID, w)
		}
	}
	cat := catalog.Default()
	codex, _ := cat.Get("codex")
	w, err := CompileRecipe(Recipe(codex))
	if err != nil || !w.TrustPattern.MatchString("Trust this folder?") || !w.WorkingTitle.MatchString("⠋ Working") || w.ConfirmSubmit {
		t.Fatalf("codex: %+v %v", w, err)
	}
	anytime := 0
	for _, q := range w.Questions {
		if q.Anytime {
			anytime++
		}
	}
	if anytime == 0 {
		t.Fatal("codex: no question asked during the work")
	}
	claude, _ := cat.Get("claude")
	w, err = CompileRecipe(Recipe(claude))
	if err != nil || !w.ConfirmSubmit || len(w.Questions) == 0 || !w.Questions[0].FromScreen {
		t.Fatalf("claude: %+v %v", w, err)
	}
	if r := Recipe(catalog.Agent{ID: "plain", Command: []string{"sh"}}); r != nil {
		t.Fatalf("an agent with nothing to watch: %+v", r)
	}
	if w, err := CompileRecipe(nil); err != nil || w.TrustPattern != nil || w.Questions != nil {
		t.Fatalf("no recipe: %+v %v", w, err)
	}
	for _, c := range []struct {
		r    proto.HostRecipe
		want error
	}{
		{proto.HostRecipe{TrustPrompt: "("}, ErrRecipeTrustPrompt},
		{proto.HostRecipe{TrustPrompt: ".*"}, ErrRecipeTrustPrompt},
		{proto.HostRecipe{WorkingTitle: "("}, ErrRecipeWorkingTitle},
		{proto.HostRecipe{Questions: []proto.RecipeQuestion{{Prompt: "("}}}, ErrRecipeQuestion},
		{proto.HostRecipe{Questions: []proto.RecipeQuestion{{Prompt: "p", Words: "("}}}, ErrRecipeQuestion},
		{proto.HostRecipe{Questions: []proto.RecipeQuestion{{Prompt: "p", ModeAnswers: "("}}}, ErrRecipeQuestion},
		{proto.HostRecipe{Questions: []proto.RecipeQuestion{{Prompt: "p", TypedAnswers: "("}}}, ErrRecipeQuestion},
	} {
		if _, err := CompileRecipe(&c.r); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.r, err, c.want)
		}
	}
}

// Whatever the catalog takes, a host takes: an agent whose answer labels
// are at the catalog's bound with spaces around them (the catalog counts
// them trimmed) has a recipe Check passes, or a host would watch nothing
// of it (#376, review round 2).
func TestRecipeOfACatalogAgentPassesCheck(t *testing.T) {
	label := "  " + strings.Repeat("é", catalog.MaxTrustAnswerLabel) + " "
	a := catalog.Agent{ID: "padded", Name: "Padded", Command: []string{"padded"}, TrustPrompt: `Trust\s*this\s*folder\?`,
		TrustAnswers: []catalog.Answer{{Label: label, Input: "1"}},
		Questions:    []catalog.Question{{Prompt: `Proceed\?`, Answers: []catalog.Answer{{Label: label, Input: "y"}}}}}
	cat := catalog.Default()
	if err := cat.Upsert(a); err != nil {
		t.Fatalf("the catalog refuses the agent: %v", err)
	}
	got, _ := cat.Get("padded")
	r := Recipe(got)
	if r == nil {
		t.Fatal("no recipe")
	}
	if err := r.Check(); err != nil {
		t.Fatalf("a recipe of an agent the catalog takes: %v", err)
	}
	if _, err := CompileRecipe(r); err != nil {
		t.Fatal(err)
	}
}
