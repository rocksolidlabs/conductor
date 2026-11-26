package session

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A line typed only into an empty prompt box (Submission.EmptyBox, #1142)
// waits while the agent's box visibly begins with what was typed into it.

// The prompt boxes, drawn by hand after the boxes the agents draw: the
// box's own rows and the cursor's sequences alone, each frame a
// synchronized update that hides the cursor as it begins.
//
// Claude Code's: a rule, the ❯ mark in column 1 and a no-break space in
// column 2, the box's text from column 3 (its placeholder's words as plain
// text in an empty box; nothing after a turn), a rule, the footer. As it
// runs by default it never shows the terminal's cursor (claudeBox's shown
// false); with its native cursor on, the cursor stands at the caret.
//
// Codex's after the repository's own raw screen (codexUpdateOverSplash in
// question_test.go): the mark on row 21, the input from column 3, the
// footer on row 24, the cursor placed at the caret and shown.
func claudeBox(rows []string, caretRow, caretCol int, shown bool) string {
	box := ""
	for i, r := range rows {
		if i == 0 {
			box += "❯ " + r
		} else {
			box += "\r\n" + r
		}
	}
	return "\x1b[?2026h\x1b[?25l\x1b[2J\x1b[H" + rule80 + "\r\n" + box + "\r\n" + rule80 + "\r\n  ? for shortcuts" + caretAt(caretRow+1, caretCol, shown) + "\x1b[?2026l"
}

// claudeHolding is Claude Code's box holding text on its first row, the
// cursor after it, shown or not.
func claudeHolding(text string, shown bool) string {
	return claudeBox([]string{text}, 1, 3+len([]rune(text)), shown)
}

func codexBox(text string, col int, shown bool) string {
	return "\x1b[?2026h\x1b[?25l\x1b[1;1H\x1b[J\x1b[21;1H\x1b[1m›\x1b[21;3H\x1b[22m" + text +
		"\x1b[24;3H\x1b[22m\x1b[1m?\x1b[24;5H\x1b[22mfor\x1b[24;9Hshortcuts\x1b[39m\x1b[49m\x1b[0m" + caretAt(21, col, shown) + "\x1b[?2026l"
}

// caretAt places the cursor at row, col (1-based) and shows it, or leaves
// it hidden.
func caretAt(row, col int, shown bool) string {
	s := fmt.Sprintf("\x1b[%d;%dH", row, col)
	if shown {
		s += "\x1b[?25h"
	}
	return s
}

const (
	claudePlaceholder = `Try "fix lint errors"`
	codexPlaceholder  = "\x1b[2m\x1b[2mAsk Codex to do anything"
)

// The boxes: Claude Code's empty with its placeholder, empty after a turn,
// and holding a word, as it draws them by default (its cursor hidden) and
// with its cursor shown; Codex's empty with its placeholder and holding a
// word.
var (
	claudePlaceholderBox      = claudeBox([]string{claudePlaceholder}, 1, 3, false)
	claudePlaceholderBoxShown = claudeBox([]string{claudePlaceholder}, 1, 3, true)
	claudeBlankBox            = claudeBox([]string{""}, 1, 3, false)
	claudeWordBox             = claudeHolding("pear", false)
	claudeWordBoxShown        = claudeHolding("pear", true)
	codexPlaceholderBox       = codexBox(codexPlaceholder, 3, true)
	codexWordBox              = codexBox("pear", 7, true)
)

// boxScreen is the screen a frame draws on a blank 80 by 24 screen: its
// text, whether it is uncut and its caret.
func boxScreen(frame string) (string, bool, Caret) {
	sc := NewScreen(80, 24)
	sc.Write([]byte(frame))
	uncut, _, _ := sc.DrawnAt()
	return sc.Lines(), uncut, sc.Caret()
}

// TestTheScreenKeepsWhetherTheCursorIsShown: ?25l hides it and ?25h shows
// it; a terminal's reset shows it; the caret's row and what stands before
// it are read off the cells, a wide character's tail left out, and the
// whole row once the last column was written.
func TestTheScreenKeepsWhetherTheCursorIsShown(t *testing.T) {
	sc := NewScreen(10, 3)
	if !sc.Caret().Shown {
		t.Fatal("a new screen's cursor is hidden")
	}
	sc.Write([]byte("\x1b[?25l› 世ab"))
	if c := sc.Caret(); c.Shown || c.Before != "› 世ab" || c.Row != "› 世ab" {
		t.Fatalf("after ?25l: %+v", c)
	}
	sc.Write([]byte("\x1b[1;3H\x1b[?25h"))
	if c := sc.Caret(); !c.Shown || c.Before != "› " || c.Row != "› 世ab" {
		t.Fatalf("after ?25h at column 3: %+v", c)
	}
	sc.Write([]byte("\x1b[2;1H0123456789"))
	if c := sc.Caret(); c.Before != "0123456789" {
		t.Fatalf("the last column written: %+v", c)
	}
	sc.Write([]byte("\x1b[?25l\x1bc"))
	if c := sc.Caret(); !c.Shown || c.Before != "" {
		t.Fatalf("after a reset: %+v", c)
	}
}

// boxModel is the model after writes, each its own write.
func boxModel(writes ...string) boxState {
	var b inputBox
	for i, w := range writes {
		b.wrote([]byte(w), false, uint64(i+1))
	}
	return b.state()
}

// TestTheBoxsModelFollowsASimpleLineEditor: characters are appended (a
// paste's text, UTF-8, a kitty keyboard report of one), Backspace and DEL
// take the last, Ctrl-W the last word, Ctrl-U and Ctrl-C clear; a
// terminal's reports, the pointer's moves and releases, a lone Escape and
// an Enter into an empty box do nothing; the cursor keys, Tab, a new line,
// an Alt key, a mouse press, a paste of more than one line, a key after an
// Enter and text past its bound make the text edited. An Enter keeps the
// text, and Ctrl-U after it empties the box whether it was sent or not.
func TestTheBoxsModelFollowsASimpleLineEditor(t *testing.T) {
	long := strings.Repeat("a", maxBoxRunes+1)
	for _, tc := range []struct {
		name   string
		writes []string
		typed  string
		edited bool
	}{
		{"characters", []string{"pe", "ar"}, "pear", false},
		{"UTF-8 and a kitty key", []string{"é", "\x1b[97u\x1b[65;2u"}, "éaA", false},
		{"Backspace and DEL", []string{"pexx\x7f\b", "ar"}, "pear", false},
		{"Ctrl-W", []string{"one two  \x17"}, "one ", false},
		{"Ctrl-U", []string{"pear\x15"}, "", false},
		{"Ctrl-C", []string{"pear\x1b[D\x03"}, "", false},
		{"a paste", []string{"\x1b[200~pe", "ar\x1b[201~"}, "pear", false},
		{"reports and the pointer", []string{"\x1b[12;3R\x1b[?62;22c\x1b[I\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[<35;10;5M\x1b[<0;10;5m\x1b[<64;1;1M\x1b"}, "", false},
		{"an Enter into an empty box", []string{"\r", "\r"}, "", false},
		{"an Enter after text", []string{"pear\r"}, "pear", false},
		{"Ctrl-U after an Enter", []string{"pear\r", "\x15"}, "", false},
		{"a key after an Enter", []string{"pear\r", "x"}, "pear", true},
		{"Backspace after an Enter", []string{"pear\r", "\x7f"}, "pear", true},
		{"a cursor key", []string{"pear\x1b[D"}, "pear", true},
		{"Home in application mode", []string{"\x1bOH"}, "", true},
		{"Tab", []string{"\t"}, "", true},
		{"a new line", []string{"a\x1b\r"}, "a", true},
		{"Ctrl-J", []string{"a\n"}, "a", true},
		{"an Alt key", []string{"\x1bb"}, "", true},
		{"a mouse press", []string{"\x1b[<0;10;5M"}, "", true},
		{"a legacy mouse press", []string{"\x1b[M !!"}, "", true},
		{"a paste of two lines", []string{"\x1b[200~a\rb\x1b[201~"}, "ab", true},
		{"past its bound", []string{long}, long[:maxBoxRunes], true},
	} {
		got := boxModel(tc.writes...)
		if got.typed != tc.typed || got.edited != tc.edited {
			t.Errorf("%s: %q edited %v, want %q edited %v", tc.name, got.typed, got.edited, tc.typed, tc.edited)
		}
	}
	var b inputBox
	b.wrote([]byte("pear"), true, 1)
	if st := b.state(); !st.empty() {
		t.Fatalf("keys to a dialog: %+v", st)
	}
	b.wrote([]byte("pear"), false, 2)
	b.wrote([]byte("\x1b[I\r"), false, 3)
	if st := b.state(); st.mark != 3 {
		t.Fatalf("an Enter after text is a key the screen judges: mark %d", st.mark)
	}
	b.wrote([]byte("\x1b[O"), false, 4)
	if st := b.state(); st.mark != 3 {
		t.Fatalf("a report moved the mark: %d", st.mark)
	}
}

// TestTheBoxsFirstRowIsReadOnTheAgentsScreens: the row of the prompt's
// mark, after the mark and its space (Claude Code's no-break space among
// them), on each agent's box; a second row is never the first; the caret
// is at the box's start only where the cursor is shown on that row just
// after the mark.
func TestTheBoxsFirstRowIsReadOnTheAgentsScreens(t *testing.T) {
	claude, codex := builtInShape(t, "claude"), builtInShape(t, "codex")
	bullet := claudeBox([]string{"Steps:", "  - "}, 2, 5, true)
	for _, tc := range []struct {
		name  string
		shape *regexp.Regexp
		frame string
		after string
		start bool
	}{
		{"Claude Code's placeholder, cursor hidden", claude, claudePlaceholderBox, claudePlaceholder, false},
		{"Claude Code's placeholder, cursor shown", claude, claudePlaceholderBoxShown, claudePlaceholder, true},
		{"Claude Code's blank box", claude, claudeBlankBox, "", false},
		{"Claude Code's word, cursor hidden", claude, claudeWordBox, "pear", false},
		{"Claude Code's word, cursor shown", claude, claudeWordBoxShown, "pear", false},
		{"Claude Code's second row begun with a bullet", claude, bullet, "Steps:", false},
		{"Codex's placeholder", codex, codexPlaceholderBox, "Ask Codex to do anything", true},
		{"Codex's word", codex, codexWordBox, "pear", false},
	} {
		text, uncut, c := boxScreen(tc.frame)
		row, mark, after, ok := boxFirstRow(text, tc.shape)
		if !uncut || !ok || after != tc.after {
			t.Errorf("%s: %q (row %q, ok %v, uncut %v), want %q", tc.name, after, row, ok, uncut, tc.after)
			continue
		}
		if got := caretAtBoxStart(row, mark, c); got != tc.start {
			t.Errorf("%s: caret at the box's start %v, want %v (%+v)", tc.name, got, tc.start, c)
		}
	}
}

// drawFrame writes frame, a synchronized update, as the agent's output and
// waits until the screen the box is read from has fed it whole.
func drawFrame(t *testing.T, s *Local, p *fakeProc, frame string) {
	t.Helper()
	before := s.shot.Fed()
	p.outW.Write([]byte(frame))
	if !waitFor(10*time.Second, func() bool {
		_, uncut, _, fed := s.shot.FrameCaret()
		return fed > before && uncut
	}) {
		t.Fatalf("the frame was never drawn: %q", s.ScreenLines(24))
	}
}

// typeKeys writes a person's keys through sub and reads them off p.
func typeKeys(t *testing.T, s *Local, p *fakeProc, sub *Subscription, keys string) {
	t.Helper()
	if err := s.Input(sub, []byte(keys)); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != keys {
		t.Fatalf("the keys written: %q", got)
	}
}

// emptyBoxLine is a crew member's prompt as the engine submits it.
var emptyBoxLine = Submission{Text: "Task t1: read the brief", ByName: "crew", EmptyBox: true}

// typedAlone submits the line and wants it typed and entered alone.
func typedAlone(t *testing.T, s *Local, p *fakeProc, what string) {
	t.Helper()
	res, err := s.Submit(t.Context(), emptyBoxLine)
	if err != nil || !res.Entered {
		t.Fatalf("%s: %+v %v", what, res, err)
	}
	if got := written(t, p) + written(t, p); got != emptyBoxLine.Text+"\r" {
		t.Fatalf("%s: written %q", what, got)
	}
}

// held submits the line and wants it refused with want, nothing written.
func held(t *testing.T, s *Local, p *fakeProc, want error, what string) {
	t.Helper()
	res, err := s.Submit(t.Context(), emptyBoxLine)
	if err != want || res.Typed {
		t.Fatalf("%s: %+v %v, want %v", what, res, err, want)
	}
	nothingIn(t, s, p)
}

// TestAnUntouchedSessionIsNeverHeldByItsBoxWhateverItsAgentDraws: nothing
// typed went in (a terminal's reports and the pointer's moves type
// nothing), so the line is typed and entered at once whatever the agent
// draws in its box and does with its cursor: a word of its own, its
// placeholder, its cursor never shown, for an agent with each built-in
// shape and one with none. No agent, built-in or custom, waits for good
// by the box's rule.
func TestAnUntouchedSessionIsNeverHeldByItsBoxWhateverItsAgentDraws(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape *regexp.Regexp
		frame string
	}{
		{"Claude Code, a word it drew, its cursor hidden", builtInShape(t, "claude"), claudeWordBox},
		{"Claude Code, its placeholder, its cursor hidden", builtInShape(t, "claude"), claudePlaceholderBox},
		{"Codex, a word it drew", builtInShape(t, "codex"), codexWordBox},
		{"Codex, its cursor never shown", builtInShape(t, "codex"), codexBox(codexPlaceholder, 3, false)},
		{"no shape, a shell's prompt", nil, "\x1b[?2026h\x1b[?25luser@host:~$ pear\x1b[?2026l"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{PromptShape: tc.shape}))
			typeKeys(t, s, p, controller(t, s, "ann"), "\x1b[I\x1b[<35;10;5M\x1b")
			drawFrame(t, s, p, tc.frame)
			typedAlone(t, s, p, "the line")
		})
	}
}

// TestALineWaitsWhileThePromptBoxBeginsWithAPersonsWord: a word a person
// typed before the agent's box was drawn (while it loads), shown at the
// start of the box, holds the line, nothing of it written, for as many
// looks as it shows, whatever the cursor does; cleared (Ctrl-U, the
// placeholder after the mark again) the line is typed and entered alone.
// So it is for a word typed and deleted with Backspace, and for a key the
// box never showed (it went elsewhere, or the agent dropped it).
func TestALineWaitsWhileThePromptBoxBeginsWithAPersonsWord(t *testing.T) {
	for _, tc := range []struct {
		name               string
		agent              string
		word, empty, blank string
	}{
		{"Claude Code, its cursor hidden", "claude", claudeWordBox, claudePlaceholderBox, claudeBlankBox},
		{"Claude Code, its cursor shown", "claude", claudeWordBoxShown, claudePlaceholderBoxShown, claudeBlankBox},
		{"Codex", "codex", codexWordBox, codexPlaceholderBox, codexPlaceholderBox},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, tc.agent)}))
			sub := controller(t, s, "ann")
			typeKeys(t, s, p, sub, "pear")
			for range 3 {
				drawFrame(t, s, p, tc.word)
				held(t, s, p, ErrBoxHoldsText, "the box holding a word")
			}
			typeKeys(t, s, p, sub, "\x15")
			drawFrame(t, s, p, tc.empty)
			typedAlone(t, s, p, "the box cleared")
			drawFrame(t, s, p, tc.blank)

			typeKeys(t, s, p, sub, "pe")
			typeKeys(t, s, p, sub, "\x7f\x7f")
			drawFrame(t, s, p, tc.empty)
			typedAlone(t, s, p, "a word deleted")
			drawFrame(t, s, p, tc.blank)

			typeKeys(t, s, p, sub, "?")
			drawFrame(t, s, p, tc.empty)
			typedAlone(t, s, p, "a key the box does not show")
		})
	}
}

// TestAnEnterTheAgentDroppedLeavesTheWordHolding: a word and an Enter
// written as one input before the agent has drawn anything: the agent
// keeps the word in its box and drops the Enter. The screen decides: the
// line waits while the box begins with the word, and goes once a frame
// drawn after the Enter shows a box without it (sent, or cleared), the
// word forgotten. An Enter into an empty box changes nothing.
func TestAnEnterTheAgentDroppedLeavesTheWordHolding(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude")}))
	sub := controller(t, s, "ann")
	typeKeys(t, s, p, sub, "pear\r")
	drawFrame(t, s, p, claudeWordBox)
	held(t, s, p, ErrBoxHoldsText, "the word kept, its Enter dropped")
	drawFrame(t, s, p, claudeBlankBox)
	typedAlone(t, s, p, "the word sent")
	if st := s.box.state(); !st.empty() {
		t.Fatalf("the word sent is still kept: %+v", st)
	}
	typeKeys(t, s, p, sub, "\r")
	drawFrame(t, s, p, claudePlaceholderBox)
	typedAlone(t, s, p, "an Enter into an empty box")
}

// TestEditedTextHoldsWhileTheBoxIsNotBlank: text edited with a key the
// session does not follow (a cursor key) holds the line while the box's
// first row holds anything after its mark and the cursor does not show
// the caret at the box's start, with words that say to press Enter into
// the cleared box: behind Claude Code's placeholder and its hidden
// cursor, a cleared box is not seen. A blank box, or the caret shown at
// the box's start, lets the line go.
func TestEditedTextHoldsWhileTheBoxIsNotBlank(t *testing.T) {
	claude := builtInShape(t, "claude")
	s, p := newLocalWith(t, quiet(Options{PromptShape: claude}))
	sub := controller(t, s, "ann")
	typeKeys(t, s, p, sub, "pear\x1b[D")
	drawFrame(t, s, p, claudeWordBox)
	held(t, s, p, ErrBoxHoldsEditedText, "edited text in the box")
	drawFrame(t, s, p, claudePlaceholderBox)
	held(t, s, p, ErrBoxHoldsEditedText, "the placeholder, the cursor hidden")
	drawFrame(t, s, p, claudeBlankBox)
	typedAlone(t, s, p, "a blank box")

	s, p = newLocalWith(t, quiet(Options{PromptShape: claude}))
	sub = controller(t, s, "ann")
	typeKeys(t, s, p, sub, "pear\x1b[D")
	drawFrame(t, s, p, claudeWordBoxShown)
	held(t, s, p, ErrBoxHoldsEditedText, "edited text, the cursor shown after it")
	drawFrame(t, s, p, claudePlaceholderBoxShown)
	typedAlone(t, s, p, "the caret at the box's start")
	if !errors.Is(ErrBoxHoldsEditedText, ErrBoxHoldsText) {
		t.Fatal("edited text's refusal is no kind of ErrBoxHoldsText")
	}
}

// TestABulletOnTheBoxsSecondRowIsNotItsStart: text typed, a new line made
// with the agent's own key, and a bullet begun on the box's second row,
// the cursor shown after it: the second row is not the box's first, and
// the line waits.
func TestABulletOnTheBoxsSecondRowIsNotItsStart(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude")}))
	typeKeys(t, s, p, controller(t, s, "ann"), "Steps:\x1b\r- ")
	drawFrame(t, s, p, claudeBox([]string{"Steps:", "  - "}, 2, 5, true))
	held(t, s, p, ErrBoxHoldsEditedText, "a bullet on the second row")
}

// TestAnAgentWithNoShapeIsNeverHeldByItsBox: an agent with no prompt
// shape gets its line as before, whatever was typed and whatever it draws.
func TestAnAgentWithNoShapeIsNeverHeldByItsBox(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	typeKeys(t, s, p, controller(t, s, "ann"), "pear\x1b[D")
	drawFrame(t, s, p, "\x1b[?2026h\x1b[2J\x1b[H( O)> pear\x1b[?2026l")
	typedAlone(t, s, p, "the line")
}

// TestAWordNotDrawnYetHoldsTheLine: the box drawn empty and a person's key
// written after it: until the agent has drawn anything since the key went
// in, the screen is the one before it, and the line waits; drawn, it is
// judged by the screen.
func TestAWordNotDrawnYetHoldsTheLine(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude")}))
	drawFrame(t, s, p, claudePlaceholderBox)
	typeKeys(t, s, p, controller(t, s, "ann"), "p")
	held(t, s, p, ErrBoxHoldsText, "before the key is drawn")
	drawFrame(t, s, p, claudeHolding("p", false))
	held(t, s, p, ErrBoxHoldsText, "the key drawn")
}

// TestAWordTypedAsTheLineWaitsForTheWriterHoldsIt: the writer is held on
// a person's earlier write (an arrow key, the box's model edited no more
// than a Ctrl-U undoes) and the person's word is queued behind it before
// the line: the line's look, before its text, finds nothing typed, and the
// writer, taking the text after the word, refuses it (the text's guard),
// nothing of it written. Without the guard the line would be typed after
// the word.
func TestAWordTypedAsTheLineWaitsForTheWriterHoldsIt(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude"), InputWait: 30 * time.Second}))
	drawFrame(t, s, p, claudePlaceholderBox)
	sub := controller(t, s, "ann")
	gate := make(chan struct{})
	p.onBytes = func(b []byte) {
		if string(b) == "\x1b[I" {
			<-gate
		}
	}
	keys := make(chan error, 2)
	go func() { keys <- s.Input(sub, []byte("\x1b[I")) }()
	untilWaiting(t, s, 0)
	go func() { keys <- s.Input(sub, []byte("pear")) }()
	untilWaiting(t, s, 1)
	line := make(chan error, 1)
	go func() {
		_, err := s.Submit(t.Context(), emptyBoxLine)
		line <- err
	}()
	untilWaiting(t, s, 2)
	close(gate)
	if err := <-line; !errors.Is(err, ErrBoxHoldsText) {
		t.Fatalf("the line: %v", err)
	}
	for range 2 {
		if err := <-keys; err != nil {
			t.Fatal(err)
		}
	}
	if a, b := written(t, p), written(t, p); a != "\x1b[I" || b != "pear" {
		t.Fatalf("written %q, %q", a, b)
	}
	nothingIn(t, s, p)
}

// TestAChoicesKeysAreNotTextInTheBox: a reply's keys to a question the
// session shows (a choice's "y") go to its dialog, not the box.
func TestAChoicesKeysAreNotTextInTheBox(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "codex")}))
	sub := controller(t, s, "ann")
	q := reported(t, s, "Run it?")
	if err := s.AnswerInput(t.Context(), sub, q.ID, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "y" {
		t.Fatalf("the choice's keys: %q", got)
	}
	if st := s.box.state(); !st.empty() {
		t.Fatalf("a choice's keys count as text in the box: %+v", st)
	}
}

// TestAStartupAnswerTypedInTheTerminalIsNotTextInTheBox: a person's digit
// typed in the terminal at a startup question the session shows (Codex's
// "Skip until next version") answers it, and is no text in the box; a
// letter typed there is.
func TestAStartupAnswerTypedInTheTerminalIsNotTextInTheBox(t *testing.T) {
	upd := []Option{{Label: "Skip this update", Input: "\x1b"}, {Label: "Skip until next version", Input: "3"}}
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "codex"), Questions: []StartupQuestion{{
		Pattern: regexp.MustCompile(`(?i)update\s*now\s*\(\s*runs`),
		Words:   regexp.MustCompile(`(?i)(update\s*available)\W*?(\d+(?:\.\d+)+\s*(?:→|->)\s*\d+(?:\.\d+)+)`),
		Answers: upd,
	}}}))
	sub := controller(t, s, "ann")
	p.outW.Write([]byte(codexUpdateScreen))
	waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	typeKeys(t, s, p, sub, "3")
	if st := s.box.state(); !st.empty() {
		t.Fatalf("a startup answer counts as text in the box: %+v", st)
	}
	typeKeys(t, s, p, sub, "x")
	if st := s.box.state(); st.typed != "x" {
		t.Fatalf("a letter at the dialog: %+v", st)
	}
}
