package session

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// A Brakeman turn's line (Submission.OnlyAtPrompt) is typed, and entered,
// only into the agent's prompt, its screen read whole as the writer takes
// each (atPrompt, #714).

// builtInShape is the built-in agent id's prompt shape, compiled as a
// session of it is started with it.
func builtInShape(t *testing.T, id string) *regexp.Regexp {
	t.Helper()
	a, ok := catalog.Default().Get(id)
	if !ok || a.PromptShape == "" {
		t.Fatalf("the built-in %s has no prompt shape", id)
	}
	re, err := catalog.CompilePattern(a.PromptShape)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

// The screens, as the programs draw them on a cleared screen. Claude Code
// 2.1.319's prompt: its banner, a rule, a ❯ row (a no-break space after
// the marker, a "Try …" placeholder) and a rule as wide as the window, its
// footer under them (seen in a PTY, #714).
var (
	rule80          = strings.Repeat("─", 80)
	claudeBanner    = " ▐▛███▛█   Claude Code v2.1.319\r\n▝▜██████▀  Opus with high effort\r\n ▝▝   ▝▝   /tmp/c2\r\n\r\n\r\n"
	claudePromptRow = func(text string) string {
		return claudeBanner + rule80 + "\r\n❯ " + text + "\r\n" + rule80 + "\r\n  ⚠ Transcript saving is off\r\n  ⏸ plan mode on (shift+tab to cycle)"
	}
	claudeTheme      = "Welcome to Claude Code v2.1.319\r\n Let's get started.\r\n Choose the text style that looks best with your terminal\r\n To change this later, run /theme\r\n     Auto (match terminal)\r\n ❯ ✔ Dark mode\r\n     Light mode"
	claudeLogin      = "Welcome to Claude Code v2.1.319\r\n Select login method:\r\n ❯ 1. Claude account with subscription\r\n   2. Anthropic Console account"
	claudePermission = rule80 + "\r\n Bash command\r\n\r\n   ls\r\n\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No\r\n\r\n Esc to cancel"
	// A dialog drawn under a prompt box: the box is not the screen's end.
	claudeUnderBox = claudeBanner + rule80 + "\r\n❯ \r\n" + rule80 + "\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No"
	// A dialog drawn inside a prompt box (Codex round 2 on #720): a ❯ row
	// between two rules, and under it a highlighted numbered choice a digit
	// of the line would pick.
	claudeBoxedChoice = "────────\r\n❯ \r\nSelect an account\r\n ❯ 1. Use Acme\r\n  2. Quit\r\n────────\r\nEnter to confirm"
	// A dialog's numbered choices above a prompt box drawn whole: the box
	// is the prompt's shape, the screen is still not the prompt.
	claudeChoiceAboveBox = claudeBanner + " Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No\r\n" + rule80 + "\r\n❯ \r\n" + rule80 + "\r\n  ? for shortcuts"
	unknownSignIn        = "  Acme Agent 9.0\r\n\r\n  You are not signed in.\r\n  Open https://example.invalid/device to continue\r\n\r\n  [ Sign in ]   [ Quit ]"
	unknownDialog        = "  Select a sign-in method\r\n\r\n ❯ 1. Acme account with subscription\r\n   2. Acme Console account\r\n\r\n  Enter to confirm"
	// Codex 0.163.0's composer at rest: a › row, its status line and
	// footer under it (seen in a PTY, #714).
	codexComposer = func(text string) string {
		return "  >_ OpenAI Codex (v0.163.0)\r\n     /tmp/c2\r\n\r\n\r\n› " + text + "\r\n\r\n  GPT-6 xhigh · /tmp/c2 · Context 0% used · 0 in · 0 out\r\n  ? for shortcuts"
	}
	codexUpdate   = "  Update available · 0.163.0 → 0.167.0\r\n  Release notes: https://github.com/openai/codex/releases/latest\r\n\r\n› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n\r\n  enter continue · esc skip"
	codexTrust    = "Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit"
	codexApproval = "  Would you like to run the following command?\r\n\r\n  $ touch a\r\n\r\n› 1. Yes, proceed (y)\r\n  2. Yes, and don't ask again (p)\r\n  3. No, and tell Codex what to do differently (esc)\r\n\r\n  Press enter to confirm or esc to cancel"
)

// csi is a control sequence in a test's screen, left out of what it shows.
var csi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// draw clears s's screen and draws screen on it, waiting until the screen
// atPrompt reads shows each of its rows (each screen has one of its own).
func draw(t *testing.T, s *Local, p *fakeProc, screen string) {
	t.Helper()
	if !drawsOn(s, p, screen) {
		t.Fatalf("the screen never showed %q: %q", screen, s.ScreenLines(24))
	}
}

// drawsOn is draw for a hook outside the test's goroutine: whether the
// screen came to show screen.
func drawsOn(s *Local, p *fakeProc, screen string) bool {
	p.outW.Write([]byte("\x1b[2J\x1b[H" + screen))
	return waitFor(10*time.Second, func() bool {
		text, _, _, _, _ := s.shot.Frame()
		for _, row := range strings.Split(csi.ReplaceAllString(screen, ""), "\r\n") {
			if !strings.Contains(text, strings.TrimSpace(row)) {
				return false
			}
		}
		return true
	})
}

// TestTheAgentsPromptIsKnownByItsShape: Claude Code's prompt and Codex's
// composer are the prompt, whatever their row's words (a placeholder, the
// line typed); every other screen is not: their dialogs (numbered or
// not), Codex's composer before its session is configured ("Waiting for
// startup"), a sign-in no catalog entry knows, a dialog under the prompt's
// box, half a frame. An agent with no shape known has the rule it had: a
// screen with no dialog's numbered choice, whole.
func TestTheAgentsPromptIsKnownByItsShape(t *testing.T) {
	for _, tc := range []struct {
		agent   string
		screens map[string]bool
	}{
		{"claude", map[string]bool{
			claudePromptRow(`Try "create a util logging.py that..."`): true,
			claudePromptRow("Brakeman request bk-1 (briefing)"):       true,
			claudePromptRow(""):               true,
			claudePromptRow("2 files to fix"): true,
			claudeTheme:                       false,
			claudeLogin:                       false,
			claudePermission:                  false,
			claudeUnderBox:                    false,
			claudeBoxedChoice:                 false,
			claudeChoiceAboveBox:              false,
			unknownSignIn:                     false,
			unknownDialog:                     false,
			codexComposer("Ask Codex to do anything"): false,
		}},
		{"codex", map[string]bool{
			codexComposer("Ask Codex to do anything"):         true,
			codexComposer("Brakeman request bk-1 (briefing)"): true,
			codexComposer("Waiting for startup"):              false,
			codexUpdate:                                       false,
			codexTrust:                                        false,
			codexApproval:                                     false,
			unknownSignIn:                                     false,
			claudePromptRow("Try \"fix lint errors\""):        false,
		}},
		{"", map[string]bool{
			claudePromptRow(`Try "create a util logging.py that..."`): true,
			codexComposer("Ask Codex to do anything"):                 true,
			unknownSignIn:        true,
			unknownDialog:        false,
			claudeLogin:          false,
			codexUpdate:          false,
			codexApproval:        false,
			claudeUnderBox:       false,
			claudeBoxedChoice:    false,
			claudeChoiceAboveBox: false,
		}},
	} {
		var shape *regexp.Regexp
		if tc.agent != "" {
			shape = builtInShape(t, tc.agent)
		}
		s, p := newLocalWith(t, quiet(Options{PromptShape: shape}))
		for screen, want := range tc.screens {
			draw(t, s, p, screen)
			if got := s.atPrompt(); got != want {
				t.Errorf("%q's atPrompt on %q: %v, want %v", tc.agent, s.ScreenLines(24), got, want)
			}
		}
		// Half a frame of the prompt: what it shows is not known.
		draw(t, s, p, claudePromptRow("Try it")+"\x1b[?2026h\r\n  more")
		if s.atPrompt() {
			t.Errorf("%q's atPrompt on half a frame", tc.agent)
		}
	}
}

// nothingIn fails when p holds a write: what the writer refused is never
// written, and the writer had reported every write it made (untilIdle).
func nothingIn(t *testing.T, s *Local, p *fakeProc) {
	t.Helper()
	untilIdle(t, s)
	select {
	case b := <-p.input:
		t.Fatalf("the process was written %q", b)
	default:
	}
}

// TestALineForThePromptIsTypedIntoThePromptAlone: at an unknown dialog
// (a choice highlighted that an Enter would pick) the line is refused,
// nothing written; at the agent's prompt it is typed and entered.
func TestALineForThePromptIsTypedIntoThePromptAlone(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude")}))
	sub := Submission{Text: "Brakeman request bk-1 (briefing)", OnlyAtPrompt: true}
	draw(t, s, p, unknownDialog)
	if res, err := s.Submit(t.Context(), sub); !errors.Is(err, ErrNotAtPrompt) || res.Typed {
		t.Fatalf("at the dialog: %+v %v", res, err)
	}
	nothingIn(t, s, p)
	draw(t, s, p, claudePromptRow(`Try "fix lint errors"`))
	res, err := s.Submit(t.Context(), sub)
	if err != nil || !res.Entered {
		t.Fatalf("at the prompt: %+v %v", res, err)
	}
	if got := written(t, p) + written(t, p); got != "Brakeman request bk-1 (briefing)\r" {
		t.Fatalf("written %q", got)
	}
}

// TestAPromptGoneBeforeAnEnterGetsNoEnter (Codex round 3 on #720): the
// line is typed at the prompt, and the prompt is replaced by a sign-in no
// catalog entry knows, a screen with no numbered choice the dialog guards
// would see: before the line's Enter (the beforeEnter hook, as the
// submission comes to it), its Enter is withheld; after it (BeforeEnter,
// as the writer takes it) and before the confirming Enter an agent that
// does not report taking the line gets (ConfirmSubmit), that one is
// withheld. A line whose prompt stays gets both, so the second case
// reaches the confirming Enter.
func TestAPromptGoneBeforeAnEnterGetsNoEnter(t *testing.T) {
	text := "Brakeman request bk-1 (briefing)"
	opts := func() Options {
		// InputWait bounds a hang alone: the screen is drawn while the
		// writer holds the first Enter (BeforeEnter).
		return quiet(Options{PromptShape: builtInShape(t, "claude"), ConfirmSubmit: true, ConfirmWait: 50 * time.Millisecond, InputWait: 30 * time.Second})
	}

	// Gone after the text, before its Enter.
	s, p := newLocalWith(t, opts())
	draw(t, s, p, claudePromptRow(`Try "fix lint errors"`))
	var shown bool
	hook := func() { shown = drawsOn(s, p, unknownSignIn) }
	s.beforeEnter.Store(&hook)
	res, err := s.Submit(t.Context(), Submission{Text: text, OnlyAtPrompt: true, Confirm: true})
	s.beforeEnter.Store(nil)
	if !shown {
		t.Fatalf("the sign-in was never drawn: %q", s.ScreenLines(24))
	}
	if err != nil || !res.Typed || res.Entered || res.Reentered {
		t.Fatalf("the line, its prompt gone before its Enter: %+v %v", res, err)
	}
	if got := written(t, p); got != text {
		t.Fatalf("written %q", got)
	}
	nothingIn(t, s, p)

	// Gone after its Enter, before the confirming one.
	for _, gone := range []bool{true, false} {
		s, p := newLocalWith(t, opts())
		draw(t, s, p, claudePromptRow(`Try "fix lint errors"`))
		var shown atomic.Bool
		sub := Submission{Text: text, OnlyAtPrompt: true, Confirm: true}
		if gone {
			sub.BeforeEnter = func() { shown.Store(drawsOn(s, p, unknownSignIn)) }
		}
		res, err := s.Submit(t.Context(), sub)
		if gone && !shown.Load() {
			t.Fatalf("the sign-in was never drawn: %q", s.ScreenLines(24))
		}
		if err != nil || !res.Typed || !res.Entered || res.Reentered != !gone {
			t.Fatalf("the line, its prompt gone after its Enter %v: %+v %v", gone, res, err)
		}
		want := []string{text, "\r", "\r"}
		if gone {
			want = want[:2]
		}
		for _, w := range want {
			if got := written(t, p); got != w {
				t.Fatalf("gone %v: written %q, want %q", gone, got, w)
			}
		}
		nothingIn(t, s, p)
	}
}

// TestAChoiceInOrAboveThePromptsBoxGetsNoByte (Codex round 2 on #720):
// Claude Code's shape reads no row inside its box that is a choice, so a
// dialog drawn inside a box is not the prompt's shape, its choices indented
// with a no-break space or another Unicode space as with plain ones (Codex
// round 3); and a screen whose box is the shape but which shows a dialog's
// numbered choice above it is not the prompt either, the numbered-choice
// rule in force with a shape as without one. At each the line is refused:
// not a byte of its text (a digit of it would pick a choice), nor its
// Enter.
func TestAChoiceInOrAboveThePromptsBoxGetsNoByte(t *testing.T) {
	shape := builtInShape(t, "claude")
	unescaped := func(screen string) string { return strings.ReplaceAll(screen, "\r\n", "\n") }
	if shape.MatchString(unescaped(claudeBoxedChoice)) {
		t.Fatal("the shape matches a choice inside its box")
	}
	if !shape.MatchString(unescaped(claudeChoiceAboveBox)) {
		t.Fatal("the shape does not match the box under the choices: the test proves nothing of the rule")
	}
	screens := []string{claudeBoxedChoice, claudeChoiceAboveBox}
	// Codex round 3 on #720: the box's choice rows indented with a
	// no-break space (or another Unicode space) in place of their first
	// plain one. Read raw, the shape takes each row for text (a pattern's
	// \s is ASCII) and the numbered-choice rule finds no choice; atPrompt
	// reads both with every space a plain one (plainSpaces).
	for _, space := range []string{"\u00a0", "\u2007", "\u3000"} {
		indented := strings.Replace(strings.Replace(claudeBoxedChoice, "\r\n ❯ 1.", "\r\n"+space+"❯ 1.", 1), "\r\n  2.", "\r\n"+space+" 2.", 1)
		if indented == claudeBoxedChoice || strings.Count(indented, space) != 2 {
			t.Fatalf("the fixture: %q", indented)
		}
		if raw := unescaped(indented); !shape.MatchString(raw) || startupChoiceShown(raw) {
			t.Fatalf("read raw, %q is the shape (%v) or shows a choice: the test proves nothing", raw, shape.MatchString(raw))
		}
		if plain := plainSpaces(unescaped(indented)); shape.MatchString(plain) || !startupChoiceShown(plain) {
			t.Fatalf("read with plain spaces, %q is the shape or shows no choice", plain)
		}
		screens = append(screens, indented)
	}
	for _, screen := range screens {
		s, p := newLocalWith(t, quiet(Options{PromptShape: shape}))
		draw(t, s, p, screen)
		res, err := s.Submit(t.Context(), Submission{Text: "1 Brakeman request bk-1 (briefing)", OnlyAtPrompt: true})
		if !errors.Is(err, ErrNotAtPrompt) || res.Typed || res.Entered {
			t.Fatalf("the line at %q: %+v %v", s.ScreenLines(24), res, err)
		}
		nothingIn(t, s, p)
	}
}

// TestADialogDrawnWhileTheLineWaitsForTheWriterGetsNoByte: the line,
// asked for at the prompt, waits behind another write the program is slow
// to take; meanwhile a dialog no catalog entry knows is drawn. As the
// writer takes the line the screen is the dialog: nothing of the line is
// written (a digit or letter of it would pick a choice at once).
func TestADialogDrawnWhileTheLineWaitsForTheWriterGetsNoByte(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude")}))
	nater, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	draw(t, s, p, claudePromptRow(`Try "fix lint errors"`))
	if !s.atPrompt() {
		t.Fatalf("the prompt is not: %q", s.ScreenLines(24))
	}
	release := blockFirstWrite(p)
	defer release()
	go s.Input(nater, []byte("\x1b[A"))
	writing(t, s)
	line := submitAsync(s, Submission{Text: "Brakeman request bk-1 (briefing)", OnlyAtPrompt: true})
	queued(t, s, 1)
	draw(t, s, p, unknownDialog)
	release()
	if got := nextWrite(t, p); got != "\x1b[A" {
		t.Fatalf("written %q", got)
	}
	if got := returnsWithin(t, 10*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrNotAtPrompt) || got.res.Typed {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	nothingIn(t, s, p)
}

// TestTheScreenHasADialogBeforeTheAgentIsSeenWaiting: the screen the line
// is looked for on takes each chunk before the viewers have it and before
// it is scanned for what says the agent waits. Held just after a dialog's
// chunk went out to the viewers, the agent's own report that it waits come
// meanwhile, a line typed then finds the dialog, not the prompt drawn
// before it, and nothing is written.
func TestTheScreenHasADialogBeforeTheAgentIsSeenWaiting(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude")}))
	draw(t, s, p, claudePromptRow(`Try "fix lint errors"`))
	stalled, resume := make(chan struct{}), make(chan struct{})
	var stall, unstall sync.Once
	after := func(chunk []byte) {
		if strings.Contains(string(chunk), "Select a sign-in method") {
			stall.Do(func() { close(stalled); <-resume })
		}
	}
	s.afterBroadcast.Store(&after)
	defer unstall.Do(func() { close(resume) })
	p.outW.Write([]byte("\x1b[2J\x1b[H" + unknownDialog + "\a"))
	select {
	case <-stalled:
	case <-time.After(10 * time.Second):
		t.Fatal("the dialog never went out")
	}
	s.SetAttention(AttentionNeedsInput, "Claude needs your attention", SourceAPI)
	res, err := s.Submit(t.Context(), Submission{Text: "Brakeman request bk-1 (briefing)", OnlyAtPrompt: true})
	if !errors.Is(err, ErrNotAtPrompt) || res.Typed {
		t.Fatalf("the line with the pump held: %+v %v", res, err)
	}
	nothingIn(t, s, p)
	unstall.Do(func() { close(resume) })
}

// TestADialogCutByASmallerWindowKeepsTheLineOut: a dialog no catalog entry
// knows, its cursor left above its choices; the window is made so short
// that its rows below the cursor go and its choices with them, though the
// agent still waits in it. The screen is cut, so what it shows is not
// known: the line is refused, neither its text nor its Enter written, for
// an agent with no shape known as for one with.
func TestADialogCutByASmallerWindowKeepsTheLineOut(t *testing.T) {
	for _, shape := range []*regexp.Regexp{nil, builtInShape(t, "claude")} {
		s, p := newLocalWith(t, quiet(Options{PromptShape: shape}))
		nater, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		draw(t, s, p, unknownDialog+"\x1b[1;1H")
		if s.atPrompt() {
			t.Fatalf("the dialog is the prompt: %q", s.ScreenLines(24))
		}
		if err := s.ResizeWith(nater, 80, 2, true); err != nil {
			t.Fatal(err)
		}
		if !waitFor(10*time.Second, func() bool { _, rows := s.shot.Size(); return rows == 2 }) {
			t.Fatal("the screen was never made shorter")
		}
		if rows, drawn := s.shot.Rows(); !drawn || startupChoiceShown(strings.Join(rows, "\n")) {
			t.Fatalf("the choices are still on the rows: %q (drawn %v)", rows, drawn)
		}
		res, err := s.Submit(t.Context(), Submission{Text: "Brakeman request bk-1 (briefing)", OnlyAtPrompt: true})
		if !errors.Is(err, ErrNotAtPrompt) || res.Typed || res.Entered {
			t.Fatalf("the line at the cut dialog: %+v %v", res, err)
		}
		nothingIn(t, s, p)
	}
}
