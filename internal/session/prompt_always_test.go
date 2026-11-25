package session

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// For an agent with a prompt shape, a line Conductor types that names no
// question is typed only where the screen shows the agent's prompt, for
// the whole of the session (promptOnly): its text and its Enter, each
// judged on the screen read whole as the writer takes it. Elsewhere
// nothing of it is written.

// pickFrame is a screen with a choice an Enter or a digit would pick, made
// of plain rows no catalog entry knows: a title, three numbered rows with
// the first marked, and a footer.
const pickFrame = "  Choose a colour for the chart\r\n\r\n  ❯ 1. Blue\r\n    2. Green\r\n    3. Red\r\n\r\n  Enter to choose · Esc to close"

// shapedAgent is a built-in agent with a prompt shape, with its prompt at
// rest and a startup watch (a trust question) as its catalog entry has.
type shapedAgent struct {
	id     string
	prompt string
}

func shapedAgents() []shapedAgent {
	return []shapedAgent{
		{"claude", claudePromptRow("")},
		{"codex", codexComposer("Ask Codex to do anything")},
	}
}

// pastFirstLine is a session of the agent a's, a controlling viewer
// attached, whose first line has been entered at its prompt: its startup
// watch is over.
func pastFirstLine(t *testing.T, a shapedAgent, opts Options) (*Local, *fakeProc, *Subscription) {
	t.Helper()
	opts.PromptShape = builtInShape(t, a.id)
	opts.TrustPattern = regexp.MustCompile(`Trust\s*this\s*folder\?`)
	s, p := newLocalWith(t, quiet(opts))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	draw(t, s, p, a.prompt)
	if res, err := s.Submit(t.Context(), Submission{Text: "read the code", By: sub}); err != nil || !res.Entered {
		t.Fatalf("the first line: %+v %v", res, err)
	}
	for _, want := range []string{"read the code", "\r"} {
		if got := written(t, p); got != want {
			t.Fatalf("the first line: written %q, want %q", got, want)
		}
	}
	if s.trust.Watching() {
		t.Fatal("the startup watch outlasted the first line")
	}
	return s, p, sub
}

// TestALineLongPastTheFirstIsTypedOnlyAtThePrompt: long past its first
// line the session's screen shows a choice no catalog entry knows and no
// prompt. A person's line, a link's, a crew's message, one that must never
// answer a prompt (UnlessWaiting: a handoff, a broadcast, a chat line) and
// an Enter alone are each refused, and the program is written nothing, no
// text and no Enter; the person is told the refusal's words. With the
// prompt back, the same line is typed and entered.
func TestALineLongPastTheFirstIsTypedOnlyAtThePrompt(t *testing.T) {
	for _, a := range shapedAgents() {
		t.Run(a.id, func(t *testing.T) {
			s, p, sub := pastFirstLine(t, a, Options{})
			guest, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "guest", LinkID: "l1", Cols: 80, Rows: 24}, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			draw(t, s, p, pickFrame)
			if att := s.Info().Attention; att.State == AttentionNeedsInput {
				t.Fatalf("the session asks %+v: the frame is to be one no catalog entry knows", att)
			}
			const line = "1 fix the bug"
			for _, tc := range []struct {
				name    string
				sub     Submission
				refusal error
			}{
				{"a person's line", Submission{Text: line, By: sub}, ErrNotAtPrompt},
				{"a link's line", Submission{Text: line, By: guest}, ErrNotAtPrompt},
				{"a crew's message", Submission{Text: line, ByName: "crew", Confirm: true}, ErrNotAtPrompt},
				{"a person's line that must not answer a prompt", Submission{Text: line, By: sub, UnlessWaiting: true}, nil},
				{"a handoff", Submission{Text: line, ByName: "crew", UnlessWaiting: true}, nil},
				{"a chat line from a link", Submission{Text: line, ByName: "guest", From: guest, UnlessWaiting: true}, nil},
				{"an Enter alone", Submission{By: sub}, ErrNotAtPrompt},
			} {
				res, err := s.Submit(t.Context(), tc.sub)
				if res.Typed || res.Entered || !errors.Is(err, tc.refusal) || (tc.refusal == nil) != (err == nil) {
					t.Fatalf("%s at the choice: %+v %v, want nothing typed and %v", tc.name, res, err, tc.refusal)
				}
				nothingIn(t, s, p)
			}
			// The person is told why, in the refusal's words.
			var word proto.ErrorMsg
			if err := json.Unmarshal(SubmitWord("r1", SubmitResult{}, ErrNotAtPrompt)[1:], &word); err != nil {
				t.Fatal(err)
			}
			if word.Code != proto.ErrCodeNotSent || word.Message != ErrNotAtPrompt.Error() || word.RequestID != "r1" {
				t.Fatalf("the person's word %+v", word)
			}

			draw(t, s, p, a.prompt)
			res, err := s.Submit(t.Context(), Submission{Text: line, By: sub})
			if err != nil || !res.Entered {
				t.Fatalf("at the prompt again: %+v %v", res, err)
			}
			for _, want := range []string{line, "\r"} {
				if got := written(t, p); got != want {
					t.Fatalf("at the prompt again: written %q, want %q", got, want)
				}
			}
			nothingIn(t, s, p)
		})
	}
}

// TestALineAtThePromptIsTypedAndEnteredOnce: long past the first line, a
// line is typed and entered once at the agent's prompt at rest, at the
// prompt while the agent's output scrolls above it as the line's Enter is
// taken, and at the prompt holding a line long enough to wrap past the
// rows the prompt's shape takes (the agent draws the text it was given
// before the Enter).
func TestALineAtThePromptIsTypedAndEnteredOnce(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("fix the bug in the parser ", 27))
	wrapped := func(prompt func(string) string) string {
		var rows []string
		row := ""
		for _, w := range strings.Fields(long) {
			if row != "" && len(row)+1+len(w) > 74 {
				rows = append(rows, row)
				row = "  " + w
				continue
			}
			if row == "" {
				row = w
			} else {
				row += " " + w
			}
		}
		return prompt(strings.Join(append(rows, row), "\r\n"))
	}
	for _, a := range shapedAgents() {
		prompt := claudePromptRow
		working := func(n int) string {
			var out strings.Builder
			for i := range n {
				out.WriteString("⏺ Read file " + strings.Repeat("x", i) + "\r\n")
			}
			return claudeBanner + out.String() + "✻ Working… (esc to interrupt)\r\n" + rule80 + "\r\n❯ \r\n" + rule80 + "\r\n  ? for shortcuts"
		}
		if a.id == "codex" {
			prompt = codexComposer
			working = func(n int) string {
				var out strings.Builder
				for i := range n {
					out.WriteString("• Ran ls " + strings.Repeat("x", i) + "\r\n")
				}
				return out.String() + "• Working (3s • esc to interrupt)\r\n\r\n› Ask Codex to do anything\r\n\r\n  ? for shortcuts"
			}
		}
		for _, tc := range []struct {
			name, before, text, pause string
		}{
			{"at rest", a.prompt, "fix the bug", ""},
			{"while output scrolls above it", working(3), "fix the bug", working(8)},
			{"holding a long line", a.prompt, long, wrapped(prompt)},
		} {
			t.Run(a.id+" "+tc.name, func(t *testing.T) {
				s, p, sub := pastFirstLine(t, a, Options{})
				draw(t, s, p, tc.before)
				if tc.pause != "" {
					var shown atomic.Bool
					hook := func() { shown.Store(drawsOn(s, p, tc.pause)) }
					s.beforeEnter.Store(&hook)
					defer func() {
						if !shown.Load() {
							t.Error("the screen before the Enter was never drawn")
						}
					}()
				}
				res, err := s.Submit(t.Context(), Submission{Text: tc.text, By: sub})
				s.beforeEnter.Store(nil)
				if err != nil || !res.Typed || !res.Entered {
					t.Fatalf("the line: %+v %v", res, err)
				}
				for _, want := range []string{tc.text, "\r"} {
					if got := written(t, p); got != want {
						t.Fatalf("written %q, want %q", got, want)
					}
				}
				nothingIn(t, s, p)
			})
		}
	}
}

// TestAnEnterIsWrittenOnlyAtThePrompt: the line's text typed at the
// agent's prompt, the screen shows a choice before the writer takes the
// line's Enter: the Enter is not written, the text left where it was
// typed. A crew's message the agent did not report taking gets a second
// Enter only at the prompt too: with the choice drawn after its first
// Enter, none.
func TestAnEnterIsWrittenOnlyAtThePrompt(t *testing.T) {
	for _, a := range shapedAgents() {
		t.Run(a.id, func(t *testing.T) {
			s, p, sub := pastFirstLine(t, a, Options{})
			var shown atomic.Bool
			hook := func() { shown.Store(drawsOn(s, p, pickFrame)) }
			s.beforeEnter.Store(&hook)
			res, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: sub})
			s.beforeEnter.Store(nil)
			if !shown.Load() {
				t.Fatalf("the choice was never drawn: %q", s.ScreenLines(24))
			}
			if err != nil || !res.Typed || res.Entered {
				t.Fatalf("the line, a choice drawn before its Enter: %+v %v", res, err)
			}
			if got := written(t, p); got != "fix the bug" {
				t.Fatalf("written %q", got)
			}
			nothingIn(t, s, p)
		})
		t.Run(a.id+" a second Enter", func(t *testing.T) {
			// InputWait bounds a hang alone: the screen is drawn while the
			// writer holds the first Enter (BeforeEnter).
			s, p, _ := pastFirstLine(t, a, Options{ConfirmSubmit: true, ConfirmWait: 50 * time.Millisecond, InputWait: 30 * time.Second})
			draw(t, s, p, a.prompt)
			var shown atomic.Bool
			res, err := s.Submit(t.Context(), Submission{Text: "fix the bug", ByName: "crew", Confirm: true, BeforeEnter: func() { shown.Store(drawsOn(s, p, pickFrame)) }})
			if !shown.Load() {
				t.Fatalf("the choice was never drawn: %q", s.ScreenLines(24))
			}
			if err != nil || !res.Entered || res.Reentered {
				t.Fatalf("the line, a choice drawn after its Enter: %+v %v", res, err)
			}
			for _, want := range []string{"fix the bug", "\r"} {
				if got := written(t, p); got != want {
					t.Fatalf("written %q, want %q", got, want)
				}
			}
			nothingIn(t, s, p)
		})
	}
}

// TestASessionWithNoPromptShapeTypesALineAsBefore: an agent with no prompt
// shape types its lines as it always did: its first line on a screen of
// plain output, and, long past it, a line on a screen of numbered output
// that no prompt's shape is known to tell from a prompt.
func TestASessionWithNoPromptShapeTypesALineAsBefore(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`)}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	numbered := "  Here is the plan:\r\n  1. Read the code\r\n  2. Fix the bug\r\n\r\n$ "
	for i, line := range []string{"read the code", "fix the bug"} {
		draw(t, s, p, []string{unknownSignIn, numbered}[i])
		res, err := s.Submit(t.Context(), Submission{Text: line, By: sub})
		if err != nil || !res.Entered {
			t.Fatalf("line %d: %+v %v", i+1, res, err)
		}
		for _, want := range []string{line, "\r"} {
			if got := written(t, p); got != want {
				t.Fatalf("line %d: written %q, want %q", i+1, got, want)
			}
		}
	}
	if s.trust.Watching() {
		t.Fatal("the startup watch outlasted the first line")
	}
	nothingIn(t, s, p)
}
