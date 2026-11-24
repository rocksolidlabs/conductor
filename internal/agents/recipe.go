package agents

import (
	"errors"
	"regexp"
	"slices"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Recipe is what a session of agent a watches on its screen (#293): its
// trust question and answers, its other questions, the window title that
// says it works, whether it reports taking a prompt, and its prompt at
// rest (its prompt shape). The server starts its own sessions with it
// (CompileRecipe) and hands it to a host that registers a session of the
// same agent (proto.Registered.Recipe), whole to one that says it reads
// forms and without them (HostRecipe) to one that does not, and with its
// prompt shape only to one that says it takes it (WithoutPromptShape
// otherwise), so that the two watch the same things, or the same but
// those. Nil when the agent has none of them.
func Recipe(a catalog.Agent) *proto.HostRecipe {
	sig := a.EffectiveSignal()
	r := &proto.HostRecipe{
		TrustPrompt:   a.TrustPrompt,
		TrustAnswers:  recipeAnswers(a.TrustAnswers),
		WorkingTitle:  sig.WorkingTitle,
		ConfirmSubmit: ConfirmsSubmit(a.Adapter, sig),
		PromptShape:   a.PromptShape,
	}
	for _, q := range a.Questions {
		r.Questions = append(r.Questions, proto.RecipeQuestion{
			Prompt: q.Prompt, Words: q.Words, Anytime: q.Anytime, Answers: recipeAnswers(q.Answers),
			AnswersFromScreen: q.AnswersFromScreen, ModeAnswers: q.ModeAnswers, TypedAnswers: q.TypedAnswers,
		})
	}
	if r.Empty() {
		return nil
	}
	return r
}

// WithoutPromptShape is r as a host whose register does not say it takes
// the prompt shape (proto.HostInfo.PromptShape) is sent it: the same but
// the shape, byte for byte what a host was sent before the recipe carried
// one, and nil when nothing else is left to watch. r is not changed.
func WithoutPromptShape(r *proto.HostRecipe) *proto.HostRecipe {
	if r == nil || r.PromptShape == "" {
		return r
	}
	c := *r
	c.PromptShape = ""
	if c.Empty() {
		return nil
	}
	return &c
}

// HostRecipe is Recipe as a host that does not read forms is sent it
// (proto.Registered.Recipe): without the questions that are forms (asked
// at any time, their answers read off the screen, and answers of their
// own: Codex's MCP tool approval, #716), which its hosted session leaves to
// its terminal. A host whose register says it reads them
// (proto.HostInfo.Forms) is sent Recipe whole (#970).
func HostRecipe(a catalog.Agent) *proto.HostRecipe {
	a.Questions = slices.DeleteFunc(slices.Clone(a.Questions), func(q catalog.Question) bool {
		return q.Anytime && q.AnswersFromScreen && len(q.Answers) > 0
	})
	return Recipe(a)
}

func recipeAnswers(in []catalog.Answer) []proto.RecipeAnswer {
	if len(in) == 0 {
		return nil
	}
	out := make([]proto.RecipeAnswer, len(in))
	for i, a := range in {
		out[i] = proto.RecipeAnswer{Label: a.Label, Input: a.Input}
	}
	return out
}

// Watch is a Recipe compiled for session.Options: the fields of the same
// names there, which Apply sets.
type Watch struct {
	TrustPattern  *regexp.Regexp
	TrustAnswers  []session.Option
	Questions     []session.StartupQuestion
	WorkingTitle  *regexp.Regexp
	ConfirmSubmit bool
	PromptShape   *regexp.Regexp
}

// Apply sets on o what w watches, every field of it: a server's own
// session and a hosted one are started through it, so the two watch the
// same fields of one recipe.
func (w Watch) Apply(o *session.Options) {
	o.TrustPattern = w.TrustPattern
	o.TrustAnswers = w.TrustAnswers
	o.Questions = w.Questions
	o.WorkingTitle = w.WorkingTitle
	o.ConfirmSubmit = w.ConfirmSubmit
	o.PromptShape = w.PromptShape
}

// Errors of CompileRecipe, by the part of the recipe that does not compile.
var (
	ErrRecipeTrustPrompt  = errors.New("the agent's trust prompt is invalid")
	ErrRecipeWorkingTitle = errors.New("the agent's working title is invalid")
	ErrRecipeQuestion     = errors.New("one of the agent's startup questions is invalid")
	ErrRecipePromptShape  = errors.New("the agent's prompt shape is invalid")
)

// CompileRecipe compiles r as a session started with it uses it: each
// pattern as the catalog holds patterns (catalog.CompilePattern, the
// prompt shape among them, as the catalog compiles an agent's promptShape;
// the title with catalog.CompileWorkingTitle), the answers as the
// session's choices. A nil r watches nothing.
func CompileRecipe(r *proto.HostRecipe) (Watch, error) {
	var w Watch
	if r == nil {
		return w, nil
	}
	var err error
	if r.TrustPrompt != "" {
		if w.TrustPattern, err = catalog.CompilePattern(r.TrustPrompt); err != nil {
			return Watch{}, ErrRecipeTrustPrompt
		}
	}
	if r.WorkingTitle != "" {
		if w.WorkingTitle, err = catalog.CompileWorkingTitle(r.WorkingTitle); err != nil {
			return Watch{}, ErrRecipeWorkingTitle
		}
	}
	if r.PromptShape != "" {
		if w.PromptShape, err = catalog.CompilePattern(r.PromptShape); err != nil {
			return Watch{}, ErrRecipePromptShape
		}
	}
	w.TrustAnswers = sessionOptions(r.TrustAnswers)
	for _, q := range r.Questions {
		sq := session.StartupQuestion{Anytime: q.Anytime, Answers: sessionOptions(q.Answers), FromScreen: q.AnswersFromScreen && q.Anytime}
		if sq.Pattern, err = catalog.CompilePattern(q.Prompt); err != nil {
			return Watch{}, ErrRecipeQuestion
		}
		for _, p := range []struct {
			src string
			re  **regexp.Regexp
		}{{q.Words, &sq.Words}, {q.ModeAnswers, &sq.Mode}, {q.TypedAnswers, &sq.Typed}} {
			if p.src == "" {
				continue
			}
			if *p.re, err = catalog.CompilePattern(p.src); err != nil {
				return Watch{}, ErrRecipeQuestion
			}
		}
		w.Questions = append(w.Questions, sq)
	}
	w.ConfirmSubmit = r.ConfirmSubmit
	return w, nil
}

func sessionOptions(in []proto.RecipeAnswer) []session.Option {
	out := make([]session.Option, 0, len(in))
	for _, a := range in {
		out = append(out, session.Option{Label: a.Label, Input: a.Input})
	}
	return out
}
