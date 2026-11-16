package session

import (
	"strings"
	"testing"
	"time"
)

// A line whose Enter needs the agent's prompt holding its text
// (Submission.PromptHoldsText, a launch's first message) is judged, for its
// Enter, against the prompt as it is with the text in it, drawn here as
// the built-in agents draw their prompt (atprompt_test.go's screens) with
// the text echoed into its row, wrapped where the composer wraps it.

// wrapped is text broken into rows of at most width characters at its
// spaces, each row after the first indented by indent: as a composer
// wraps what is typed into it.
func wrapped(text string, width int, indent string) string {
	var rows []string
	row := ""
	for _, w := range strings.Fields(text) {
		if row != "" && len(row)+1+len(w) > width {
			rows = append(rows, row)
			row = w
			continue
		}
		if row != "" {
			row += " "
		}
		row += w
	}
	rows = append(rows, row)
	return strings.Join(rows, "\r\n"+indent)
}

// submitHolding submits text into s as a first message is submitted, on a
// goroutine, and returns the channel its result comes on.
func submitHolding(t *testing.T, s *Local, text string) <-chan SubmitResult {
	t.Helper()
	out := make(chan SubmitResult, 1)
	go func() {
		res, err := s.Submit(t.Context(), Submission{Text: text, ByName: "launch", OnlyAtPrompt: true, PromptHoldsText: true})
		if err != nil {
			t.Errorf("the submission: %v", err)
		}
		out <- res
	}()
	return out
}

// longFirstMessage wraps into six rows at 76 columns: past the five rows
// under its first that Codex's composer shape allows beside its status
// line and footer.
const longFirstMessage = "Read how this repository builds, tests and releases, then list every place the server reads its configuration from, say which of them a person can change without a restart, which of them are read once at the start and which on every request, and finally write a short plan for a single settings page that shows all of them together with where each value came from and whether it is in force"

// Ordinary text in the prompt's row is typed and entered: a text that
// begins as Codex's "Waiting for startup" does, one that begins as a
// numbered choice does (line breaks typed as spaces make it easy), for
// both agents; and a text that wraps past the rows the composer's shape
// allows at 80 columns.
func TestTheEnterIsJudgedAgainstThePromptHoldingTheText(t *testing.T) {
	if n := strings.Count(wrapped(longFirstMessage, 76, "  "), "\r\n"); n < 5 {
		t.Fatalf("the long text wraps into %d rows, want at least six", n+1)
	}
	for _, tc := range []struct {
		name, agent, text string
		prompt            func(string) string
		echo              string
	}{
		{"Codex, a text that begins with Wa", "codex", "Walk me through this repository", codexComposer, ""},
		{"Codex, a numbered text", "codex", "1. fix lint 2. add tests", codexComposer, ""},
		{"Claude Code, a numbered text", "claude", "1. fix lint 2. add tests", claudePromptRow, ""},
		{"Codex, a text wrapped past the composer's rows", "codex", longFirstMessage, codexComposer, wrapped(longFirstMessage, 76, "  ")},
		{"Claude Code, a text wrapped in its box", "claude", longFirstMessage, claudePromptRow, wrapped(longFirstMessage, 76, "  ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, tc.agent), EchoWait: 30 * time.Second}))
			draw(t, s, p, tc.prompt(`Try "fix lint errors"`))
			done := submitHolding(t, s, tc.text)
			if got := written(t, p); got != tc.text {
				t.Fatalf("written %q", got)
			}
			echo := tc.echo
			if echo == "" {
				echo = tc.text
			}
			draw(t, s, p, tc.prompt(echo))
			if got := written(t, p); got != "\r" {
				t.Fatalf("written %q, want the Enter", got)
			}
			if res := <-done; !res.Typed || !res.Entered || res.NotAtPrompt {
				t.Fatalf("the result %+v", res)
			}
		})
	}
}

// What must still refuse the Enter: a numbered question drawn after the
// text and before the Enter; a prompt whose row holds other text than the
// line's (the line not drawn there, or drawn after other words). The text
// is typed, its Enter never written, and the result says the screen was
// not the prompt holding it.
func TestTheEnterIsRefusedUnlessThePromptHoldsTheText(t *testing.T) {
	text := "Walk me through this repository"
	for _, tc := range []struct{ name, after string }{
		{"a numbered question drawn after the text", codexApproval},
		{"the prompt holding other text", codexComposer("Explain this codebase")},
		{"the prompt holding other words before the text", codexComposer("Explain this " + text)},
		{"the prompt holding the text and words after it", codexComposer(text + " and more")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "codex"), EchoWait: 100 * time.Millisecond}))
			draw(t, s, p, codexComposer(`Try "fix lint errors"`))
			done := submitHolding(t, s, text)
			if got := written(t, p); got != text {
				t.Fatalf("written %q", got)
			}
			draw(t, s, p, tc.after)
			res := <-done
			if !res.Typed || res.Entered || !res.NotAtPrompt {
				t.Fatalf("the result %+v", res)
			}
			nothingIn(t, s, p)
		})
	}
}
