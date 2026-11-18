package crew

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// codexQuestions are Codex's questions as the built-in catalog entry has
// them, its startup ones among them.
func codexQuestions() []session.StartupQuestion {
	codex, _ := catalog.Default().Get("codex")
	var out []session.StartupQuestion
	for _, q := range codex.Questions {
		var answers []session.Option
		for _, a := range q.Answers {
			answers = append(answers, session.Option{Label: a.Label, Input: a.Input})
		}
		var words *regexp.Regexp
		if q.Words != "" {
			words = regexp.MustCompile(q.Words)
		}
		out = append(out, session.StartupQuestion{Pattern: regexp.MustCompile(q.Prompt), Words: words, Anytime: q.Anytime, Answers: answers})
	}
	return out
}

// The agent's own update dialog holds a member's prompt, and the run's log
// says where a person answers it: with the choices the session offers for
// it in the run page's chat or on the member's session page, or in its
// terminal. Nothing answers it.
func TestTheRunLogSaysWhereAnUpdateDialogIsAnswered(t *testing.T) {
	e, fl := newEngine(t)
	fl.questions = codexQuestions()
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, _ *session.Local) {
		_ = p.Print("\x1b[2J\x1b[H  Update available 0.167.2 -> 0.170.0\r\n› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n  Press enter to continue\r\n")
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitLogged(t, e, run.ID, "lead asks", 1, 10*time.Second)
	got, _ := e.Get(run.ID)
	var note string
	for _, entry := range got.Log {
		if strings.Contains(entry.Message, "lead asks") {
			note = entry.Message
		}
	}
	want := `lead asks "Update available 0.167.2 -> 0.170.0": answer it with one of its choices ("Skip this update", "Skip until next version") in the run page's chat or on lead's session page, or in its terminal; its prompt waits`
	if note != want {
		t.Fatalf("the run log says %q, want %q", note, want)
	}
	_, p := fl.member("lead")
	if w := typed(p); len(w) != 0 {
		t.Fatalf("typed into the update dialog: %q", w)
	}
}

// A question the page offers no choice for is answered in the terminal; one
// that mirrors a decision through it; a Brakeman turn's in its terminal; and
// the session's choices are named only while it shows that question.
func TestHeldWordsSayWhereTheQuestionIsAnswered(t *testing.T) {
	crewRun, brakeman := &run{}, &run{class: ClassBrakeman}
	opts := []session.Option{{Label: "Yes", Input: "1"}}
	for _, tc := range []struct {
		name string
		r    *run
		att  session.Attention
		want string
	}{
		{"no choices", crewRun, session.Attention{Message: "Trust this folder?"}, `lead asks "Trust this folder?": answer it in lead's terminal`},
		{"one choice", crewRun, session.Attention{Message: "Trust this folder?", Options: opts}, `lead asks "Trust this folder?": answer it with its choice "Yes" in the run page's chat or on lead's session page, or in its terminal`},
		{"another question shown now", crewRun, session.Attention{Message: "Sign in?", Options: opts}, `lead asks "Trust this folder?": answer it in lead's terminal`},
		{"answered in the terminal", crewRun, session.Attention{Message: "Trust this folder?", Options: opts, Answered: session.AnsweredTerminal}, `lead asks "Trust this folder?": answer it in lead's terminal`},
		{"a decision's", crewRun, session.Attention{Message: "Trust this folder?", Options: opts, Decision: "dr-3"}, `lead asks "Trust this folder?": answer dr-3 where decisions are answered`},
		{"a Brakeman turn", brakeman, session.Attention{Message: "Trust this folder?", Options: opts}, `lead asks "Trust this folder?": answer it in lead's terminal`},
	} {
		if got := heldWords(tc.r, "lead", "Trust this folder?", tc.att); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
