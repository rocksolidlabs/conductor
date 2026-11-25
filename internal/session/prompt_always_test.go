package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// For an agent with a prompt shape, a line Conductor types that names no
// question, or that answers a question the session does not read off the
// screen, is typed only where the screen shows the agent's prompt, for the
// whole of the session (promptOnly): its text and its Enter, each judged
// on the screen read whole as the writer takes it. Elsewhere nothing of it
// is written.

// pickFrame is a screen with a choice an Enter or a digit would pick, made
// of plain rows no catalog entry knows: a title, three numbered rows with
// the first marked, and a footer.
const pickFrame = "  Choose a colour for the chart\r\n\r\n  ❯ 1. Blue\r\n    2. Green\r\n    3. Red\r\n\r\n  Enter to choose · Esc to close"

// pausedFrame is a screen with no prompt and no numbered row, made of plain
// rows no catalog entry knows: a title and a footer whose Enter goes on.
const pausedFrame = "  Your session has paused\r\n\r\n  Press Enter to resume · Esc to leave"

// crlf is a screen's rows joined as a program draws them, the recorded
// frames' 120-column rules as wide as the test's window.
func crlf(screen string) string {
	return strings.ReplaceAll(strings.ReplaceAll(screen, solidRule, rule80), "\n", "\r\n")
}

// codexRows is Codex's frame of rows as a program draws them.
func codexRows(rows ...string) string { return strings.Join(rows, "\r\n") }

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

// notSentWords is the word a person is told for res, err of a request r1.
func notSentWords(t *testing.T, res SubmitResult, err error) proto.ErrorMsg {
	t.Helper()
	var word proto.ErrorMsg
	if err := json.Unmarshal(SubmitWord("r1", res, err)[1:], &word); err != nil {
		t.Fatal(err)
	}
	return word
}

// TestALineIsTypedOnlyAtThePrompt: the session's screen shows a choice no
// catalog entry knows and no prompt. A person's line, a link's, a crew's
// message, one that must never answer a prompt (UnlessWaiting: a handoff,
// a broadcast, a chat line) and an Enter alone are each refused, and the
// program is written nothing, no text and no Enter; the person is told the
// refusal's words, the line held back too. With the prompt back, the same
// line is typed and entered.
func TestALineIsTypedOnlyAtThePrompt(t *testing.T) {
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
				if tc.refusal == nil && !res.NotAtPrompt {
					t.Fatalf("%s at the choice: held back with no word why: %+v", tc.name, res)
				}
				// The person is told why, in the refusal's words.
				if word := notSentWords(t, res, err); word.Code != proto.ErrCodeNotSent || word.Message != ErrNotAtPrompt.Error() || word.RequestID != "r1" {
					t.Fatalf("%s: the person's word %+v", tc.name, word)
				}
				nothingIn(t, s, p)
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

// numberedAnswer is the agent's prompt at rest under an answer of its own
// with a numbered list: Claude Code's recorded one (claudeIdle), Codex's
// recorded transcript (codexTranscript's rows), and each at work, the list
// above what it draws while it works.
func numberedAnswer(id string) map[string]string {
	if id == "claude" {
		return map[string]string{
			"at rest after a numbered answer": crlf(claudeIdle),
			"at work under a numbered answer": crlf(screenRows("● Here is what I would do:", "  1. Add the test", "  2. Fix the bug", "", "✻ Thinking… (esc to interrupt)", "", solidRule, "❯ ", solidRule, "  ? for shortcuts")),
		}
	}
	return map[string]string{
		"at rest after a numbered answer": codexRows("", "  >_ OpenAI Codex (v0.163.0)", "", "• Here is the plan:", "  1. Read the code", "  2. Fix the bug", "", "› Ask Codex to do anything", "  ? for shortcuts"),
		"at work under a numbered answer": codexRows("• Here is the plan:", "  1. Read the code", "  2. Fix the bug", "", "• Working (3s • esc to interrupt)", "", "› Ask Codex to do anything", "", "  ? for shortcuts"),
	}
}

// choiceFrames are screens with a dialog's numbered choice that are never
// the agent's prompt, past the startup watch as during it: a marked choice
// above the prompt's box, choices under or inside it, Codex's update
// dialog, a dialog no catalog entry knows.
func choiceFrames(id string) map[string]string {
	if id == "claude" {
		return map[string]string{"a choice": pickFrame, "a choice above the box": claudeChoiceAboveBox, "a choice under the box": claudeUnderBox, "a choice inside a box": claudeBoxedChoice}
	}
	return map[string]string{"a choice": pickFrame, "the update dialog": codexUpdate}
}

// TestALineAtThePromptIsTypedAndEnteredOnce: a line is typed and entered
// once at the agent's prompt at rest, at the prompt while the agent's
// output scrolls above it as the line's Enter is taken, at the prompt
// holding a line long enough to wrap past the rows the prompt's shape
// takes (the agent draws the text it was given before the Enter), and at
// the prompt under the agent's own numbered list, at rest and at work, for
// a person's line, a crew's and one that must never answer a prompt. The
// screens with a dialog's numbered choice take none.
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
		type line struct {
			name, before, text, pause string
			sub                       func(*Subscription, string) Submission
		}
		person := func(sub *Subscription, text string) Submission { return Submission{Text: text, By: sub} }
		cases := []line{
			{"at rest", a.prompt, "fix the bug", "", person},
			{"while output scrolls above it", working(3), "fix the bug", working(8), person},
			{"holding a long line", a.prompt, long, wrapped(prompt), person},
		}
		for name, screen := range numberedAnswer(a.id) {
			cases = append(cases,
				line{name + ", a person's line", screen, "now run the tests", "", person},
				line{name + ", a crew's message", screen, "now run the tests", "", func(_ *Subscription, text string) Submission {
					return Submission{Text: text, ByName: "crew", Confirm: true}
				}},
				line{name + ", a handoff", screen, "now run the tests", "", func(_ *Subscription, text string) Submission {
					return Submission{Text: text, ByName: "crew", UnlessWaiting: true}
				}},
			)
		}
		for _, tc := range cases {
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
				res, err := s.Submit(t.Context(), tc.sub(sub, tc.text))
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
		t.Run(a.id+" the screens with a dialog's choice", func(t *testing.T) {
			s, p, sub := pastFirstLine(t, a, Options{})
			for name, screen := range choiceFrames(a.id) {
				draw(t, s, p, screen)
				if res, err := s.Submit(t.Context(), Submission{Text: "now run the tests", By: sub}); !errors.Is(err, ErrNotAtPrompt) || res.Typed {
					t.Fatalf("%s: %+v %v", name, res, err)
				}
				nothingIn(t, s, p)
			}
		})
	}
}

// TestAnEnterIsWrittenOnlyAtThePrompt: the line's text typed at the
// agent's prompt, the screen shows a choice before the writer takes the
// line's Enter, or as the caller's BeforeEnter runs (the last thing before
// the write): the Enter is not written, the text left where it was typed,
// the result says so, and the session's turn is held for a person, so
// that the next line is not typed after the text. A crew's message the
// agent did not report taking gets a second Enter only at the prompt too:
// with the choice drawn as the program takes its first Enter, none.
func TestAnEnterIsWrittenOnlyAtThePrompt(t *testing.T) {
	for _, a := range shapedAgents() {
		for _, when := range []string{"before the writer takes the Enter", "as BeforeEnter runs"} {
			t.Run(a.id+" "+when, func(t *testing.T) {
				s, p, sub := pastFirstLine(t, a, Options{})
				var shown atomic.Bool
				drawChoice := func() { shown.Store(drawsOn(s, p, pickFrame)) }
				line := Submission{Text: "fix the bug", By: sub}
				if when == "as BeforeEnter runs" {
					line.BeforeEnter = drawChoice
				} else {
					s.beforeEnter.Store(&drawChoice)
				}
				res, err := s.Submit(t.Context(), line)
				s.beforeEnter.Store(nil)
				if !shown.Load() {
					t.Fatalf("the choice was never drawn: %q", s.ScreenLines(24))
				}
				if !errors.Is(err, ErrLineStranded) || !res.Typed || res.Entered || !res.NotAtPrompt {
					t.Fatalf("the line, a choice drawn before its Enter: %+v %v", res, err)
				}
				if got := written(t, p); got != "fix the bug" {
					t.Fatalf("written %q", got)
				}
				nothingIn(t, s, p)
				if !s.TurnHeld() {
					t.Fatal("the turn is not held for the text typed without its Enter")
				}
				// The prompt holding the text: the next line waits for the
				// turn, which a person's keys in the terminal give back.
				drawsOn(s, p, prompt(a.id)("fix the bug"))
				ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
				defer cancel()
				if res, err := s.Submit(ctx, Submission{Text: "and the tests", By: sub}); !errors.Is(err, context.DeadlineExceeded) || res.Typed {
					t.Fatalf("the next line, the turn held: %+v %v", res, err)
				}
				nothingIn(t, s, p)
			})
		}
		t.Run(a.id+" a second Enter", func(t *testing.T) {
			s, p, _ := pastFirstLine(t, a, Options{ConfirmSubmit: true, ConfirmWait: 50 * time.Millisecond, InputWait: 30 * time.Second})
			draw(t, s, p, a.prompt)
			var shown, once atomic.Bool
			p.onBytes = func(b []byte) {
				if string(b) == "\r" && once.CompareAndSwap(false, true) {
					shown.Store(drawsOn(s, p, pickFrame))
				}
			}
			res, err := s.Submit(t.Context(), Submission{Text: "fix the bug", ByName: "crew", Confirm: true})
			if !shown.Load() {
				t.Fatalf("the choice was never drawn: %q", s.ScreenLines(24))
			}
			if err != nil || !res.Entered || res.Reentered {
				t.Fatalf("the line, a choice drawn as its Enter was taken: %+v %v", res, err)
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

// prompt is the agent id's prompt holding text.
func prompt(id string) func(string) string {
	if id == "claude" {
		return claudePromptRow
	}
	return codexComposer
}

// TestCodexsPromptEndsAtItsFooter: Codex's prompt is its composer with its
// footer as the screen's last row. A › row with no footer under it (a
// message of the person's above a question, a list's highlight, a ruled
// list) is not its prompt at 80 x 24, 80 x 5 and 120 x 40, its Enter
// refused there too; every composer the tests hold, under each footer
// Codex draws, is; a composer with no footer is refused, in the refusal's
// words.
func TestCodexsPromptEndsAtItsFooter(t *testing.T) {
	codex := builtInShape(t, "codex")
	questions := map[string]string{
		"a question under a message":          codexRows("• Ran ls", "", "› Look up how sessions work", "", "  Allow access to the notes folder?", "  [y] Yes   [n] No"),
		"a pause under a message":             codexRows("› Look up how sessions work", "", "  Press Enter to go on"),
		"work under a message":                codexRows("› Look up how sessions work", "", "• Working (3s • esc to interrupt)"),
		"a highlighted list":                  codexRows("  Choose a colour", "", "› Blue", "  Green", "", "  Press enter to choose or esc to go back"),
		"a ruled list":                        codexRows("  ──────────", "› Go on in the browser", "  Use a key instead", "  ──────────"),
		"choices numbered with a parenthesis": codexRows("› Look up how sessions work", "", "  Go on?", "  1) Yes", "  2) No"),
	}
	for _, size := range [][2]int{{80, 24}, {80, 5}, {120, 40}} {
		for name, screen := range questions {
			s, _, _, write := screenSession(t, Options{PromptShape: codex}, size[0], size[1])
			write("\x1b[?1049h\x1b[2J\x1b[H" + screen)
			if s.atPrompt() || s.atPromptForEnter("fix the bug") {
				t.Errorf("%dx%d, %s: read as Codex's prompt: %q", size[0], size[1], name, s.ScreenLines(size[1]))
			}
		}
	}
	composers := map[string]string{
		"at rest":                codexComposer("Ask Codex to do anything"),
		"a line in it":           codexComposer("fix the bug"),
		"the short one":          codexRows("  >_ OpenAI Codex (v0.163.0)", "", "› Ask Codex to do anything", "", "  ? for shortcuts"),
		"its status above":       codexRows("  >_ OpenAI Codex (v0.163.0)", "", "› Ask Codex to do anything", "", "  GPT-6 xhigh · Context 0% used", "  ? for shortcuts"),
		"the send key":           codexRows("• Ran cat notes.txt", "", "› Ask Codex to do anything", "", "  ⏎ send   ⌃J newline   ⌃T transcript"),
		"the queue hint":         codexRows("• Working (3s • esc to interrupt)", "", "› fix the bug", "", "  tab to queue message · 100% context left"),
		"a key's second press":   codexRows("› Ask Codex to do anything", "", "  esc again to edit previous message"),
		"the quit key's second":  codexRows("› Ask Codex to do anything", "", "  ctrl + c again to quit"),
		"the context left alone": codexRows("› Ask Codex to do anything", "", "  100% context left"),
	}
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		for name, screen := range composers {
			s, _, _, write := screenSession(t, Options{PromptShape: codex}, size[0], size[1])
			write("\x1b[?1049h\x1b[2J\x1b[H" + screen)
			if !s.atPrompt() {
				t.Errorf("%dx%d, %s: not read as Codex's prompt: %q", size[0], size[1], name, s.ScreenLines(size[1]))
			}
		}
	}
	s, p, sub, write := screenSession(t, Options{PromptShape: codex}, 80, 24)
	write("\x1b[?1049h\x1b[2J\x1b[H" + codexRows("  >_ OpenAI Codex (v0.163.0)", "", "› Ask Codex to do anything"))
	res, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: sub})
	if !errors.Is(err, ErrNotAtPrompt) || res.Typed {
		t.Fatalf("a composer with no footer: %+v %v", res, err)
	}
	if word := notSentWords(t, res, err); word.Code != proto.ErrCodeNotSent || word.Message != ErrNotAtPrompt.Error() {
		t.Fatalf("a composer with no footer: the word %+v", word)
	}
	nothingWrittenNow(t, s, p)
}

// TestCodexsEnterIsNotWrittenOntoAQuestionUnderTheLine: the line typed at
// Codex's composer, Codex draws the line as a message of the person's
// above a question before the writer takes its Enter: the Enter is not
// written.
func TestCodexsEnterIsNotWrittenOntoAQuestionUnderTheLine(t *testing.T) {
	a := shapedAgents()[1]
	s, p, sub := pastFirstLine(t, a, Options{})
	var shown atomic.Bool
	hook := func() {
		shown.Store(drawsOn(s, p, codexRows("• Ran ls", "", "› fix the bug", "", "  Allow access to the notes folder?", "  [y] Yes   [n] No")))
	}
	s.beforeEnter.Store(&hook)
	res, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: sub})
	s.beforeEnter.Store(nil)
	if !shown.Load() {
		t.Fatalf("the question was never drawn: %q", s.ScreenLines(24))
	}
	if res.Entered || !res.Typed {
		t.Fatalf("the line, a question drawn under it: %+v %v", res, err)
	}
	if got := written(t, p); got != "fix the bug" {
		t.Fatalf("written %q", got)
	}
	nothingIn(t, s, p)
}

// TestAReplyToAQuestionNotOnTheScreenIsTypedOnlyAtThePrompt: a reply that
// names a question the session does not read off the screen (the agent's
// own question, a decision's, a report of the agent's that it waits) is
// typed only at the agent's prompt, as a line naming none is: on a screen
// with no prompt and no numbered row it is refused, nothing written; at
// the prompt it is typed and entered.
func TestAReplyToAQuestionNotOnTheScreenIsTypedOnlyAtThePrompt(t *testing.T) {
	for _, a := range shapedAgents() {
		for _, kind := range []string{"the agent's own question", "a decision's question", "a report that it waits"} {
			t.Run(fmt.Sprintf("%s %s", a.id, kind), func(t *testing.T) {
				s, p, sub := pastFirstLine(t, a, Options{})
				var id, decision string
				var err error
				switch kind {
				case "the agent's own question":
					id, err = s.TryAskID("Which database?", SourceAPI, "", nil)
				case "a decision's question":
					decision = "dr-1"
					id, err = s.TryAskDecision(decision, "Which database?")
				default:
					if err = s.TrySetAttentionFull(AttentionNeedsInput, "Which database?", SourceAPI, "", nil); err == nil {
						id = s.Info().Attention.ID
					}
				}
				if err != nil || id == "" {
					t.Fatalf("the question: %q %v", id, err)
				}
				reply := Submission{Text: "use the small one", By: sub, Question: id}
				if decision != "" {
					reply = Submission{Text: "use the small one", ByName: "crew", Question: id, Decision: decision}
				}
				draw(t, s, p, pausedFrame)
				if res, err := s.Submit(t.Context(), reply); !errors.Is(err, ErrNotAtPrompt) || res.Typed {
					t.Fatalf("the reply at a screen that is not the prompt: %+v %v", res, err)
				}
				nothingIn(t, s, p)
				draw(t, s, p, a.prompt)
				res, err := s.Submit(t.Context(), reply)
				if err != nil || !res.Entered {
					t.Fatalf("the reply at the prompt: %+v %v", res, err)
				}
				for _, want := range []string{"use the small one", "\r"} {
					if got := written(t, p); got != want {
						t.Fatalf("written %q, want %q", got, want)
					}
				}
				nothingIn(t, s, p)
			})
		}
	}
}

// TestALineWithNoPromptShapeIsTypedOnNumberedOutput: an agent with no
// prompt shape types its lines as it always did: its first line on a
// screen of plain output, and after it a line on a screen of numbered
// output that no prompt's shape is known to tell from a prompt.
func TestALineWithNoPromptShapeIsTypedOnNumberedOutput(t *testing.T) {
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

// TestTheAppHoldsTheRefusalsWords: the app shows a palette line's refusal
// at a screen that is not the agent's prompt in the owner's own words,
// which it knows by its copy of them (web/app/utils/protocol.ts): the
// copy is these words.
func TestTheAppHoldsTheRefusalsWords(t *testing.T) {
	src, err := os.ReadFile("../../web/app/utils/protocol.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^export const NOT_AT_PROMPT_WORDS = "([^"\n]*)"$`).FindSubmatch(src)
	if m == nil || string(m[1]) != NotAtPromptWords {
		t.Fatalf("protocol.ts's NOT_AT_PROMPT_WORDS is not %q: %q", NotAtPromptWords, m)
	}
}

// choiceAboveTheBox is a dialog drawn in a ruled region right above an
// intact prompt box, of plain made-up rows: its title, a sentence, three
// numbered choices the first of which is marked, its footer; then the
// box's top rule, its empty prompt row, its bottom rule and a status row.
// The prompt's shape matches the box; only the marked choice above it says
// a dialog is up.
var choiceAboveTheBox = strings.Join([]string{
	rule80, " Pick a size", "", " Which size should the report use?", "",
	" ❯ 1. Small", "   2. Medium", "   3. Large", "", " Enter to pick · Esc to close",
	rule80, "❯ ", rule80, "  ? for shortcuts",
}, "\r\n")

// TestAChoiceAboveAnIntactPromptBoxTakesNoLine: at a dialog drawn right
// above Claude Code's prompt box, the box intact, a line's text and an
// Enter alone are refused, nothing written; a line typed at the prompt
// just before the dialog is drawn has its Enter left out.
func TestAChoiceAboveAnIntactPromptBoxTakesNoLine(t *testing.T) {
	a := shapedAgents()[0]
	if !builtInShape(t, "claude").MatchString(plainSpaces(strings.ReplaceAll(choiceAboveTheBox, "\r\n", "\n"))) {
		t.Fatal("the shape does not match the box under the dialog: the test proves nothing")
	}
	s, p, sub := pastFirstLine(t, a, Options{})
	draw(t, s, p, choiceAboveTheBox)
	for _, line := range []Submission{{Text: "1 now run the tests", By: sub}, {By: sub}} {
		if res, err := s.Submit(t.Context(), line); !errors.Is(err, ErrNotAtPrompt) || res.Typed || res.Entered {
			t.Fatalf("%q at the dialog: %+v %v", line.Text, res, err)
		}
		nothingIn(t, s, p)
	}

	s, p, sub = pastFirstLine(t, a, Options{})
	var shown atomic.Bool
	hook := func() { shown.Store(drawsOn(s, p, choiceAboveTheBox)) }
	s.beforeEnter.Store(&hook)
	res, err := s.Submit(t.Context(), Submission{Text: "now run the tests", By: sub})
	s.beforeEnter.Store(nil)
	if !shown.Load() {
		t.Fatalf("the dialog was never drawn: %q", s.ScreenLines(24))
	}
	if !errors.Is(err, ErrLineStranded) || !res.Typed || res.Entered {
		t.Fatalf("the line, the dialog drawn before its Enter: %+v %v", res, err)
	}
	if got := written(t, p); got != "now run the tests" {
		t.Fatalf("written %q", got)
	}
	nothingIn(t, s, p)
}
