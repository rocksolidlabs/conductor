package session

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"
)

// A line while the startup questions' watch lasts (startupLine): its text
// typed only at the agent's prompt, judged on the screen itself, whatever
// the session shows, in either order of the two watchers that look after
// the same quiet period: the startup questions' watcher, which reads the
// output it keeps (fireTrust), and the screen's (fireAsked, missAsked).
// Each order is forced: the other watcher's own look is held at its gate
// (afterLook) while the first looks, then let go (looksAfter). Nothing
// waits on the clock to judge: a look is made through CheckNow or waited
// for by its count.

// orderSession is a session with Codex's update question and the trust
// question, Codex's prompt shape when shaped (none otherwise: an operator's
// agent), a controlling viewer at 80 by 24, and write, which returns once
// the session's pump has fed what it writes to every screen.
func orderSession(t *testing.T, shaped bool) (s *Local, p *fakeProc, sub *Subscription, write func(string)) {
	t.Helper()
	opts := startupOptions()
	if shaped {
		opts.PromptShape = builtInShape(t, "codex")
	}
	return screenSession(t, opts, 80, 24)
}

// startupOptions are the startup questions of the sessions here: the
// folder's trust question, and Codex's update question as the catalog has
// it.
func startupOptions() Options {
	return Options{
		TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`),
		TrustAnswers: []Option{{Label: "Yes, continue", Input: "1"}},
		Questions:    []StartupQuestion{codexUpdateQuestion()},
	}
}

// screenSession is a session of opts with a controlling viewer at cols by
// rows, and write, which returns once the session's pump has fed what it
// writes to every screen (afterBroadcast: the pump feeds them first).
func screenSession(t *testing.T, opts Options, cols, rows int) (s *Local, p *fakeProc, sub *Subscription, write func(string)) {
	t.Helper()
	s, p = newLocalWith(t, quiet(opts))
	var err error
	sub, err = s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: uint16(cols), Rows: uint16(rows)}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	fed := make(chan int, 1024)
	after := func(chunk []byte) { fed <- len(chunk) }
	s.afterBroadcast.Store(&after)
	write = func(out string) {
		t.Helper()
		if _, err := p.outW.Write([]byte(out)); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < len(out); {
			select {
			case m := <-fed:
				n += m
			case <-time.After(10 * time.Second):
				t.Fatalf("the session took %d of %d bytes", n, len(out))
			}
		}
	}
	return s, p, sub, write
}

// gate holds w's looks at the end of a quiet period after each has taken
// its text (afterLook), until the function it returns is called; a look
// already held then goes on. A look held keeps w's fireMu, so CheckNow on
// w waits for it: called after the release, it returns once the held look
// has run.
func gate(t *testing.T, w *PatternWatcher) (release func()) {
	t.Helper()
	open := make(chan struct{})
	w.mu.Lock()
	w.afterLook = func() { <-open }
	w.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(open) }) }
	t.Cleanup(release)
	return release
}

// screenLooked waits until the screen's watcher has looked at everything
// fed to it (its look at the end of the quiet period) and its look has
// ended. The bound only bounds a hang.
func screenLooked(t *testing.T, s *Local) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for s.asked.LookedAt() != s.asked.Fed() {
		if time.Now().After(deadline) {
			t.Fatal("the screen's watcher did not look")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.asked.fireMu.Lock()
	//lint:ignore SA2001 the empty critical section is the wait for a look in flight
	s.asked.fireMu.Unlock()
}

// looksAfter runs draw (what the agent draws next) and has the two
// watchers look at it in the order given: trustFirst, the startup
// watcher's (CheckNow, or its own look: either is first) while the
// screen's is held, then the screen's; else the screen's while the startup
// watcher's is held, then the startup watcher's.
func looksAfter(t *testing.T, s *Local, trustFirst bool, draw func()) {
	t.Helper()
	if trustFirst {
		release := gate(t, s.asked)
		draw()
		s.trust.CheckNow()
		release()
		screenLooked(t, s)
		return
	}
	release := gate(t, s.trust)
	draw()
	screenLooked(t, s)
	release()
	s.trust.CheckNow()
}

func orderName(trustFirst bool) string {
	if trustFirst {
		return "the startup watcher's look first"
	}
	return "the screen's look first"
}

func shapeName(shaped bool) string {
	if shaped {
		return "with a prompt shape"
	}
	return "with no prompt shape"
}

// escAnswered asks Codex's update question whole at 80 by 24, then in a
// window five rows tall, where only its choices are drawn and the session
// asks it again in its first choice's words; then a person types Escape in
// the terminal, which is held as the question's answer until the screen
// shows it left. It returns that question.
func escAnswered(t *testing.T, s *Local, p *fakeProc, sub *Subscription, write func(string)) Attention {
	t.Helper()
	write("\x1b[?1049h" + codexFrame(codexUpdateView))
	first := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if err := s.Resize(sub, 80, 5); err != nil {
		t.Fatal(err)
	}
	write(codexFrame(codexUpdateView[4:]))
	cut := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust && a.ID != first.ID })
	escape(t, s, p, sub, cut)
	return cut
}

// escape types Escape in the terminal at the question q and checks it is
// held as q's answer.
func escape(t *testing.T, s *Local, p *fakeProc, sub *Subscription, q Attention) {
	t.Helper()
	if err := s.Input(sub, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "\x1b" {
		t.Fatalf("written %q", got)
	}
	s.mu.Lock()
	held := s.heldAnswer.id
	s.mu.Unlock()
	if held != q.ID {
		t.Fatalf("the Escape is not held as the question's answer: held %q, question %+v", held, q)
	}
}

// The screens the agent draws after the Escape, in the short window.
var (
	// codexChoicesOnly: the update dialog drawn again as a whole frame, its
	// highlight moved and its first choice's row off the screen.
	codexChoicesOnly = codexFrame([]string{"› 2. Skip", "  3. Skip until next version", "", "  enter continue · esc skip"})
	// codexPrompt: Codex's composer, its footer under it.
	codexPrompt = codexFrame([]string{"  >_ OpenAI Codex (v0.163.0)", "", "› Ask Codex to do anything", "", "  ? for shortcuts"})
	// codexFooterOnly: the dialog drawn again with only its footer.
	codexFooterOnly = codexFrame([]string{"", "  enter continue · esc skip"})
	// codexChoicesAbovePrompt: the dialog's later choices, then a composer
	// under them.
	codexChoicesAbovePrompt = codexFrame([]string{"› 2. Skip", "  3. Skip until next version", "› Ask Codex to do anything", "  ? for shortcuts"})
	// codexChoicesNBSP: the dialog's later choices indented with no-break
	// spaces.
	codexChoicesNBSP = codexFrame([]string{" › 2. Skip", "  3. Skip until next version", "", "  enter continue · esc skip"})
	// codexTranscript: a resumed transcript's numbered list above the
	// composer, at 80 by 24.
	codexTranscript = codexFrame([]string{"", "  >_ OpenAI Codex (v0.163.0)", "", "• Here is the plan:", "  1. Read the code", "  2. Fix the bug", "", "› Ask Codex to do anything", "  ? for shortcuts"})
	// codexQuoted: ordinary output quoting the update question's pattern
	// above the composer.
	codexQuoted = codexFrame([]string{"  directory: ~/src/Update now (runs here)", "", "› Ask Codex to do anything", "", "  ? for shortcuts"})
)

// nothingWrittenNow fails when the process holds a write: the writer has
// no write in hand (untilIdle), so what it refused was never written.
func nothingWrittenNow(t *testing.T, s *Local, p *fakeProc) {
	t.Helper()
	untilIdle(t, s)
	select {
	case b := <-p.input:
		t.Fatalf("written %q", b)
	default:
	}
}

// refused submits a line, a digit first (a digit picks at once in Codex's
// update dialog), as the viewer, and fails unless it is refused whole with
// a kind of ErrTrustQuestion, nothing written.
func refused(t *testing.T, s *Local, p *fakeProc, sub *Subscription) error {
	t.Helper()
	res, err := s.Submit(t.Context(), Submission{Text: "1 fix the bug", By: sub})
	if !errors.Is(err, ErrTrustQuestion) || res.Typed || res.Entered {
		t.Fatalf("a line at the screen: %+v %v", res, err)
	}
	nothingWrittenNow(t, s, p)
	return err
}

// I1: a dialog first drawn in a window one or two rows tall, or five, is
// never recognised as a question (its pattern's row is not on the screen);
// for an agent with a prompt shape a line is refused all the same, nothing
// written, as the screen is not the agent's prompt. An agent with none
// keeps the rule it had: a screen that shows no question and no numbered
// choice takes a line.
func TestADialogNeverRecognisedTakesNoLine(t *testing.T) {
	for _, tc := range []struct {
		rows   int
		screen string
	}{
		{1, codexFrame([]string{"  enter continue · esc skip"})},
		{2, codexFooterOnly},
		{5, codexChoicesOnly},
	} {
		t.Run(fmt.Sprintf("80x%d", tc.rows), func(t *testing.T) {
			s, p, sub, write := orderSession(t, true)
			if err := s.Resize(sub, 80, uint16(tc.rows)); err != nil {
				t.Fatal(err)
			}
			write("\x1b[?1049h" + tc.screen)
			screenLooked(t, s)
			s.trust.CheckNow()
			if a := s.Info().Attention; a.State == AttentionNeedsInput {
				t.Fatalf("a question asked: %+v", a)
			}
			if err := refused(t, s, p, sub); err == ErrTrustQuestion {
				t.Fatalf("refused in a startup question's words, though none shows: %v", err)
			}
		})
	}
}

// I9: a line is judged on the screen itself, never on what the session
// shows: a question withdrawn by force (as a wrong withdrawal would) while
// its dialog's choices still show leaves a line refused whole, nothing
// written, with and without a prompt shape.
func TestALineIsRefusedAtADialogWhoseQuestionWasWithdrawn(t *testing.T) {
	for _, shaped := range []bool{true, false} {
		for _, screen := range []string{codexChoicesOnly, codexFrame(codexUpdateView[4:]), codexChoicesNBSP} {
			t.Run(shapeName(shaped), func(t *testing.T) {
				s, p, sub, write := orderSession(t, shaped)
				escAnswered(t, s, p, sub, write)
				// The startup watcher's own look is held at its gate: it
				// asks nothing again after the withdrawal below.
				gate(t, s.trust)
				write(screen)
				screenLooked(t, s)
				id := s.Info().Attention.ID
				s.withdraw(id)
				if a := s.Info().Attention; a.State == AttentionNeedsInput {
					t.Fatalf("not withdrawn: %+v", a)
				}
				refused(t, s, p, sub)
			})
		}
	}
}

// I7: a refusal's words are true to what shows: a startup question's only
// where one shows; "what may be a question" at a numbered choice with no
// question; "not at its prompt yet" otherwise. A line typed only at the
// prompt is refused in that refusal's own words.
func TestARefusalSaysWhatTheScreenShows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rows   int
		screen string
		want   error
	}{
		{"a startup question", 24, codexFrame(codexUpdateView), ErrTrustQuestion},
		{"a numbered choice and no question", 5, codexChoicesOnly, ErrStartupChoice},
		{"neither, and not the prompt", 2, codexFooterOnly, ErrStartupNotAtPrompt},
		{"the prompt", 5, codexPrompt, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, sub, write := orderSession(t, true)
			if err := s.Resize(sub, 80, uint16(tc.rows)); err != nil {
				t.Fatal(err)
			}
			write("\x1b[?1049h" + tc.screen)
			if got := s.startupLine(false); got != tc.want {
				t.Fatalf("the line gets %v, want %v", got, tc.want)
			}
			if words, ok := NotSentWords(tc.want); tc.want != nil && (!ok || words != tc.want.Error()) {
				t.Fatalf("its words %q (%v)", words, ok)
			}
			got := s.startupLine(true)
			switch {
			case tc.want == nil && got != nil, tc.want == ErrTrustQuestion && got != ErrTrustQuestion, tc.want != nil && tc.want != ErrTrustQuestion && got != ErrNotAtPrompt:
				t.Fatalf("a line typed only at the prompt gets %v", got)
			}
		})
	}
}

// The matrix: every screen at every size the startup path meets, for an
// agent with Codex's or Claude Code's prompt shape and one with none.
type matrixScreen struct {
	name, screen string
}

func matrixScreens() []matrixScreen {
	open := codexPrompt[:len(codexPrompt)-len("\x1b[?2026l")]
	return []matrixScreen{
		{"Codex's composer", codexPrompt},
		{"Claude Code's prompt", claudePromptRow(`Try "fix lint errors"`)},
		{"Codex's update dialog", codexFrame(codexUpdateView)},
		{"its choices, heading off the screen", codexFrame(codexUpdateView[4:])},
		{"its later choices only", codexChoicesOnly},
		{"its later choices indented with no-break spaces", codexChoicesNBSP},
		{"its later choices above a composer", codexChoicesAbovePrompt},
		{"its footer only", codexFooterOnly},
		{"half a frame of the composer", open},
		{"a numbered transcript above the composer", codexTranscript},
		{"ordinary output quoting the pattern", codexQuoted},
		{"the trust question", codexFrame([]string{"", "  Trust this folder?", "› 1. Yes, continue", "  2. No, quit", ""})},
		{"Codex's sign-in screen", codexFrame(codexSignInView)},
		{"a sign-in no catalog entry knows", unknownSignIn},
		{"a dialog no catalog entry knows", unknownDialog},
		{"a dialog under the prompt's box", claudeUnderBox},
		{"a choice above the prompt's box", claudeChoiceAboveBox},
		{"an empty screen", ""},
	}
}

// I1b, the acceptance's proof: over the whole matrix of screens, sizes and
// agents, wherever either guard a line had (its text's, its Enter's: kept
// as oracles, startup_oracle_test.go) refuses or holds, the one rule
// refuses the line whole. And atPrompt, Brakeman's and a first message's
// rule, answers on every screen as it did. This test is never weakened to
// make another pass.
func TestTheStartupLineRefusesWhereEitherGuardDid(t *testing.T) {
	type row struct {
		agent, screen, size string
		text, enter         bool
		branch              string
	}
	var table []row
	for _, agent := range []string{"codex", "claude", ""} {
		for _, size := range [][2]int{{80, 24}, {80, 5}, {80, 2}, {80, 1}} {
			for _, sc := range matrixScreens() {
				opts := startupOptions()
				if agent != "" {
					opts.PromptShape = builtInShape(t, agent)
				}
				s, _, _, write := screenSession(t, opts, size[0], size[1])
				write("\x1b[?1049h\x1b[2J\x1b[H" + sc.screen)
				text, enter := s.startupOnScreen(), s.oracleMayShow()
				got := s.startupLine(false)
				if (text || enter) && got == nil {
					t.Errorf("%q at %dx%d, %q: a guard refused (text %v, Enter %v), the rule types the line", agent, size[0], size[1], sc.name, text, enter)
				}
				if a, o := s.atPrompt(), s.oracleAtPrompt(); a != o {
					t.Errorf("%q at %dx%d, %q: atPrompt %v, as it was %v", agent, size[0], size[1], sc.name, a, o)
				}
				branch := "typed"
				if got != nil {
					branch = got.Error()
				}
				table = append(table, row{agent, sc.name, fmt.Sprintf("%dx%d", size[0], size[1]), text, enter, branch})
			}
		}
	}
	if testing.Verbose() {
		for _, r := range table {
			t.Logf("%-6s | %-6s | %-48s | text refused %-5v | Enter held %-5v | %s", r.agent, r.size, r.screen, r.text, r.enter, r.branch)
		}
	}
}

// codexSignInView is Codex's sign-in screen as 0.163.0 draws it where
// Codex is not signed in: a numbered list of ways to sign in.
var codexSignInView = []string{"", "  Welcome to Codex, OpenAI's command-line coding agent", "", "  Sign in with ChatGPT to use Codex as part of your paid plan", "  or connect an API key for usage-based billing", "", "> 1. Sign in with ChatGPT", "  2. Sign in with Device Code", "  3. Provide your own API key", "", "  Press enter to continue"}

// After an Escape typed at a startup question, whatever the agent draws
// that may still be its dialog (only its later choices, them indented with
// no-break spaces, them above a composer, half a frame of the composer, its
// footer in a window too short for its choices), whichever watcher looks
// first and whatever the session then shows of the question, a line's text
// is refused, nothing written.
func TestALinesTextIsRefusedAtADialogInEitherOrder(t *testing.T) {
	half := codexPrompt[:len(codexPrompt)-len("\x1b[?2026l")]
	for _, tc := range []struct {
		name string
		rows int
		draw string
		// unshaped says an agent with no prompt shape refuses it too: its
		// rule is the one it had, which reads a footer alone as no dialog.
		unshaped bool
	}{
		{"only its later choices", 0, codexChoicesOnly, true},
		{"its later choices indented with no-break spaces", 0, codexChoicesNBSP, true},
		{"its later choices above a composer", 0, codexChoicesAbovePrompt, true},
		{"half a frame of the composer", 0, half, true},
		{"its footer in a window two rows tall", 2, codexFooterOnly, false},
		{"its footer in a window one row tall", 1, codexFrame([]string{"  enter continue · esc skip"}), false},
	} {
		for _, shaped := range []bool{true, false} {
			if !shaped && !tc.unshaped {
				continue
			}
			for _, trustFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s, %s, %s", tc.name, shapeName(shaped), orderName(trustFirst)), func(t *testing.T) {
					s, p, sub, write := orderSession(t, shaped)
					escAnswered(t, s, p, sub, write)
					if tc.rows != 0 {
						if err := s.Resize(sub, 80, uint16(tc.rows)); err != nil {
							t.Fatal(err)
						}
					}
					looksAfter(t, s, trustFirst, func() { write(tc.draw) })
					s.trust.CheckNow()
					refused(t, s, p, sub)
				})
			}
		}
	}
}

// I6: ordinary output during the watch (a numbered list above the
// composer, no startup question drawn) never lets through a line the
// guard of a line's Enter held: the line is refused whole, with words that
// name no startup question.
func TestOrdinaryNumberedOutputTakesNoLineWhileTheWatchLasts(t *testing.T) {
	for _, shaped := range []bool{true, false} {
		t.Run(shapeName(shaped), func(t *testing.T) {
			s, p, sub, write := orderSession(t, shaped)
			write("\x1b[?1049h" + codexTranscript)
			if !s.oracleMayShow() {
				t.Fatal("the guard of a line's Enter did not hold it here: the test proves nothing")
			}
			if err := refused(t, s, p, sub); err != ErrStartupChoice {
				t.Fatalf("the line gets %v", err)
			}
		})
	}
}

// A line that is an Enter alone is judged as the writer takes it: the
// agent's prompt when the line was submitted, a sign-in no catalog entry
// knows drawn before the Enter is written. Nothing is written, the line
// is refused in the words of a screen not the prompt, and the startup
// questions' watch, which a line's Enter ends, still lasts. At the prompt,
// the Enter is written.
func TestAnEnterAloneIsJudgedAsItIsWritten(t *testing.T) {
	s, p, sub, write := orderSession(t, true)
	write("\x1b[?1049h" + codexPrompt)
	shown := false
	hook := func() { shown = drawsOn(s, p, unknownSignIn) }
	s.beforeEnter.Store(&hook)
	res, err := s.Submit(t.Context(), Submission{By: sub})
	s.beforeEnter.Store(nil)
	if !shown {
		t.Fatalf("the sign-in was never drawn: %q", s.ScreenLines(24))
	}
	if !errors.Is(err, ErrStartupNotAtPrompt) || res.Typed || res.Entered {
		t.Fatalf("the Enter at the sign-in: %+v %v", res, err)
	}
	nothingWrittenNow(t, s, p)
	if !s.trust.Watching() {
		t.Fatal("the startup watch ended")
	}
	write("\x1b[2J\x1b[H" + codexPrompt)
	if res, err := s.Submit(t.Context(), Submission{By: sub}); err != nil || !res.Entered {
		t.Fatalf("the Enter at the prompt: %+v %v", res, err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
}

// Codex's sign-in screen, where Codex is not signed in, is no prompt: a
// line is refused whole, nothing written, in the words of a screen that
// may be a question (its ways to sign in are numbered), and no question is
// asked of it.
func TestALineIsRefusedAtCodexsSignInScreen(t *testing.T) {
	s, p, sub, write := orderSession(t, true)
	write("\x1b[?1049h" + codexFrame(codexSignInView))
	screenLooked(t, s)
	if a := s.Info().Attention; a.State == AttentionNeedsInput {
		t.Fatalf("a question asked: %+v", a)
	}
	if err := refused(t, s, p, sub); err != ErrStartupChoice {
		t.Fatalf("the line gets %v", err)
	}
}

// A line whose agent reports taking a line (Options.ConfirmSubmit) and
// does not, within ConfirmWait after its Enter nor after the Enter pressed
// again, says so (Unconfirmed), and nothing more is written; one the agent
// reports taking after its Enter says nothing of it; an agent that reports
// nothing gets no Enter again, as ever.
func TestALineItsAgentDidNotReportTakingSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name            string
		confirm, report bool
	}{
		{"no report", true, false},
		{"a report after the Enter", true, true},
		{"an agent that reports nothing", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{ConfirmSubmit: tc.confirm, ConfirmWait: 50 * time.Millisecond}))
			var report func()
			if tc.report {
				report = func() {
					if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
						t.Error(err)
					}
				}
			}
			res, err := s.Submit(t.Context(), Submission{Text: "Plan it.", Confirm: true, BeforeEnter: report})
			if err != nil || !res.Entered {
				t.Fatalf("the line: %+v %v", res, err)
			}
			want := []string{"Plan it.", "\r"}
			if tc.confirm && !tc.report {
				want = append(want, "\r")
			}
			if res.Unconfirmed != (tc.confirm && !tc.report) || res.Reentered != (tc.confirm && !tc.report) {
				t.Fatalf("the result %+v", res)
			}
			for _, w := range want {
				if got := written(t, p); got != w {
					t.Fatalf("written %q, want %q", got, w)
				}
			}
			nothingWrittenNow(t, s, p)
		})
	}
}

// The text's guard, asked as the writer takes the text, judges the screen
// as it is then: the agent's prompt when the line was submitted, a sign-in
// no catalog entry knows drawn while the text waited behind another write,
// refuses it; the prompt drawn again takes it.
func TestTheTextsGuardJudgesTheScreenAsTheWriterTakesIt(t *testing.T) {
	s, _, sub, write := orderSession(t, true)
	write("\x1b[?1049h" + codexPrompt)
	guard := s.textGuard(Submission{Text: "1 fix the bug", By: sub}, submitState{})
	if err := guard(); err != nil {
		t.Fatalf("at the prompt: %v", err)
	}
	write("\x1b[2J\x1b[H" + unknownSignIn)
	if err := guard(); !errors.Is(err, ErrStartupNotAtPrompt) {
		t.Fatalf("at the sign-in: %v", err)
	}
	write("\x1b[2J\x1b[H" + codexPrompt)
	if err := guard(); err != nil {
		t.Fatalf("at the prompt again: %v", err)
	}
}
