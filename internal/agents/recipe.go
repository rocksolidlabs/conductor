package agents

import (
	"errors"
	"regexp"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Recipe is what a session of agent a watches on its screen (#293): its
// trust question and answers, its other questions, the window title that
// says it works, and whether it reports taking a prompt. The server starts
// its own sessions with it (CompileRecipe) and hands it to a host that
// registers a session of the same agent (proto.Registered.Recipe), so
// that the two watch the same things. Nil when the agent has none of them.
func Recipe(a catalog.Agent) *proto.HostRecipe {
	sig := a.EffectiveSignal()
	r := &proto.HostRecipe{
		TrustPrompt:   a.TrustPrompt,
		TrustAnswers:  recipeAnswers(a.TrustAnswers),
		WorkingTitle:  sig.WorkingTitle,
		ConfirmSubmit: ConfirmsSubmit(a.Adapter, sig),
	}
	for _, q := range a.Questions {
		r.Questions = append(r.Questions, proto.RecipeQuestion{
			Prompt: q.Prompt, Words: q.Words, Anytime: q.Anytime, Answers: recipeAnswers(q.Answers),
			AnswersFromScreen: q.AnswersFromScreen, ModeAnswers: q.ModeAnswers, TypedAnswers: q.TypedAnswers,
		})
	}
	if r.TrustPrompt == "" && len(r.TrustAnswers) == 0 && len(r.Questions) == 0 && r.WorkingTitle == "" && !r.ConfirmSubmit {
		return nil
	}
	return r
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
// names there.
type Watch struct {
	TrustPattern  *regexp.Regexp
	TrustAnswers  []session.Option
	Questions     []session.StartupQuestion
	WorkingTitle  *regexp.Regexp
	ConfirmSubmit bool
}

// Errors of CompileRecipe, by the part of the recipe that does not compile.
var (
	ErrRecipeTrustPrompt  = errors.New("the agent's trust prompt is invalid")
	ErrRecipeWorkingTitle = errors.New("the agent's working title is invalid")
	ErrRecipeQuestion     = errors.New("one of the agent's startup questions is invalid")
)

// CompileRecipe compiles r as a session started with it uses it: each
// pattern as the catalog holds patterns (catalog.CompilePattern; the title
// with catalog.CompileWorkingTitle), the answers as the session's choices.
// A nil r watches nothing.
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
