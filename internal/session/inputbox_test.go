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
// waits while the box may hold text: something printable went into the
// agent's input since the last Enter, and the screen does not show the
// caret at the box's start.

// The prompt boxes, drawn by hand after the shapes the agents draw: the
// box's own rows and the cursor's sequences alone, each frame a
// synchronized update that hides the cursor as it begins and shows it at
// the caret as it ends.
//
// Codex's after the repository's own raw screen (codexUpdateOverSplash in
// question_test.go): the mark on row 21, the input from column 3, a faint
// placeholder, the footer on row 24, the cursor placed at the caret and
// shown. Claude Code's rebuilt as it draws its box: a rule, the ❯ mark and
// a no-break space, the input, a rule, the footer; an empty box shows a
// faint placeholder, or nothing after a turn, the cursor just after the
// mark. That Claude Code puts the cursor after the text a box holds (as
// Codex does) is an assumption these screens make, not a capture.
func claudeBox(text string, col int, shown bool) string {
	return "\x1b[?2026h\x1b[?25l\x1b[2J\x1b[H" + rule80 + "\r\n❯ " + text + "\x1b[22m\r\n" + rule80 + "\r\n  ? for shortcuts" + caretAt(2, col, shown) + "\x1b[?2026l"
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
	claudePlaceholder = "\x1b[2mTry \"fix lint errors\""
	codexPlaceholder  = "\x1b[2m\x1b[2mAsk Codex to do anything"
)

// The boxes: empty with the agent's placeholder, Claude Code's empty after
// a turn, and holding a word typed (the caret after it).
var (
	claudeBoxPlaceholder = claudeBox(claudePlaceholder, 3, true)
	claudeBoxEmpty       = claudeBox("", 3, true)
	claudeBoxWord        = claudeBox("pear", 7, true)
	codexBoxPlaceholder  = codexBox(codexPlaceholder, 3, true)
	codexBoxWord         = codexBox("pear", 7, true)
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

// TestTheCaretAtTheBoxsStartIsReadOnTheAgentsScreens: Claude Code's box
// and Codex's composer, empty with a placeholder after the caret (and
// Claude Code's with nothing after a turn), show the caret at the box's
// start; a box holding a word, a cursor never shown, a cursor on another
// row of the prompt, half a frame do not. An agent with no shape takes
// any row whose cells before the cursor are a mark alone.
func TestTheCaretAtTheBoxsStartIsReadOnTheAgentsScreens(t *testing.T) {
	claude, codex := builtInShape(t, "claude"), builtInShape(t, "codex")
	for _, tc := range []struct {
		name  string
		shape *regexp.Regexp
		frame string
		want  bool
	}{
		{"Claude Code's placeholder", claude, claudeBoxPlaceholder, true},
		{"Claude Code's box after a turn", claude, claudeBoxEmpty, true},
		{"Claude Code's box holding a word", claude, claudeBoxWord, false},
		{"Claude Code's cursor hidden", claude, claudeBox(claudePlaceholder, 3, false), false},
		{"Claude Code's cursor on its footer", claude, strings.Replace(claudeBoxPlaceholder, "\x1b[2;3H", "\x1b[4;3H", 1), false},
		{"Claude Code's frame not ended", claude, strings.TrimSuffix(claudeBoxPlaceholder, "\x1b[?2026l"), false},
		{"Codex's placeholder", codex, codexBoxPlaceholder, true},
		{"Codex's composer holding a word", codex, codexBoxWord, false},
		{"Codex's cursor hidden", codex, codexBox(codexPlaceholder, 3, false), false},
		{"Codex's box on Claude Code's shape", claude, codexBoxPlaceholder, false},
		{"no shape, a mark alone", nil, codexBoxPlaceholder, true},
		{"no shape, a word", nil, codexBoxWord, false},
		{"no shape, a shell's prompt", nil, "user@host:~$ " + caretAt(1, 14, true), false},
	} {
		text, uncut, c := boxScreen(tc.frame)
		if got := caretAtBoxStart(text, uncut, c, tc.shape); got != tc.want {
			t.Errorf("%s: %v, want %v (caret %+v, uncut %v)", tc.name, got, tc.want, c, uncut)
		}
	}
}

// TestInputKeysReadWhatGoesIntoTheBox: printable characters and the bytes
// of UTF-8 are characters, a carriage return outside a paste an Enter;
// control bytes, escape sequences (cursor keys, a terminal's reports, the
// pointer's, OSC and DCS strings, an Alt key, ESC CR) are neither, and a
// paste cut between two writes keeps its carriage returns its own. A kitty
// keyboard report of Enter or of a character counts as one.
func TestInputKeysReadWhatGoesIntoTheBox(t *testing.T) {
	const (
		o = keyOther
		p = keyPrintable
		e = keyEnter
	)
	for _, tc := range []struct {
		in    string
		paste bool
		want  []inputKey
	}{
		{"pe", false, []inputKey{p, p}},
		{"é\r", false, []inputKey{p, p, e}},
		{"\x7f\x15\x03\b\t\n", false, nil},
		{"\x1b[A\x1bOB\x1b[12;3R\x1b[?62;22c\x1b[I", false, nil},
		{"\x1b]11;rgb:0000/0000/0000\x1b\\\x1bP>|xterm(1)\x1b\\\x1b]10;rgb:ff/ff/ff\x07", false, nil},
		{"\x1b[<0;10;5M\x1b[M !!", false, nil},
		{"\x1bb\x1b\r", false, nil},
		{"\x1b[200~a\rb\x1b[201~\r", false, []inputKey{p, p, e}},
		{"a\r", true, []inputKey{p}},
		{"\x1b[13u\x1b[97u\x1b[97;5u\x1b[57399u\x1b[65;2u", false, []inputKey{e, p, p}},
		{"\x1b", false, nil},
	} {
		paste := tc.paste
		got := inputKeys([]byte(tc.in), &paste)
		var want []inputKey
		for _, k := range tc.want {
			if k != o {
				want = append(want, k)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q: %v, want %v", tc.in, got, want)
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

// TestAnUntouchedSessionIsNeverHeldByItsBoxWhateverItsCursor: nothing a
// person or Conductor typed went in, so the line is typed and entered at
// once on every screen the line is typed on today, whatever the agent
// does with its cursor: never shown, on another row, after text the agent
// drew itself, for an agent with each built-in shape and one with none.
// No agent, built-in or custom, waits for good by the box's rule.
func TestAnUntouchedSessionIsNeverHeldByItsBoxWhateverItsCursor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape *regexp.Regexp
		frame string
	}{
		{"Claude Code, its cursor never shown", builtInShape(t, "claude"), claudeBox(claudePlaceholder, 3, false)},
		{"Claude Code, its cursor after a word it drew", builtInShape(t, "claude"), claudeBoxWord},
		{"Codex, its cursor never shown", builtInShape(t, "codex"), codexBox(codexPlaceholder, 3, false)},
		{"Codex, its cursor on its footer", builtInShape(t, "codex"), strings.Replace(codexBoxPlaceholder, "\x1b[21;3H\x1b[?25h", "\x1b[24;20H\x1b[?25h", 1)},
		{"no shape, a shell's prompt", nil, "\x1b[?2026h\x1b[?25luser@host:~$ \x1b[?2026l"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{PromptShape: tc.shape}))
			// A person's keys that type nothing: an arrow, a backspace.
			typeKeys(t, s, p, controller(t, s, "ann"), "\x1b[A\x7f")
			drawFrame(t, s, p, tc.frame)
			res, err := s.Submit(t.Context(), emptyBoxLine)
			if err != nil || !res.Entered {
				t.Fatalf("the line: %+v %v", res, err)
			}
			if got := written(t, p) + written(t, p); got != emptyBoxLine.Text+"\r" {
				t.Fatalf("written %q", got)
			}
		})
	}
}

// TestALineWaitsWhileThePromptBoxHoldsAPersonsWord: a word a person typed
// before the agent's box was drawn (while it loads), shown in the box with
// the caret after it, holds the line, nothing of it written, for as many
// looks as it shows; cleared (the box drawn empty again, its placeholder
// after the caret) the line is typed and entered alone. A word entered
// with an Enter holds nothing, whatever the screen still shows.
func TestALineWaitsWhileThePromptBoxHoldsAPersonsWord(t *testing.T) {
	for _, tc := range []struct {
		name        string
		agent       string
		word, empty string
	}{
		{"Claude Code", "claude", claudeBoxWord, claudeBoxEmpty},
		{"Codex", "codex", codexBoxWord, codexBoxPlaceholder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, tc.agent)}))
			sub := controller(t, s, "ann")
			typeKeys(t, s, p, sub, "pear")
			drawFrame(t, s, p, tc.word)
			for range 3 {
				if res, err := s.Submit(t.Context(), emptyBoxLine); !errors.Is(err, ErrBoxHoldsText) || res.Typed {
					t.Fatalf("the box holding a word: %+v %v", res, err)
				}
				drawFrame(t, s, p, tc.word)
			}
			nothingIn(t, s, p)
			// The person clears it: Ctrl-U, the box drawn empty.
			typeKeys(t, s, p, sub, "\x15")
			drawFrame(t, s, p, tc.empty)
			res, err := s.Submit(t.Context(), emptyBoxLine)
			if err != nil || !res.Entered {
				t.Fatalf("the box cleared: %+v %v", res, err)
			}
			if got := written(t, p) + written(t, p); got != emptyBoxLine.Text+"\r" {
				t.Fatalf("written %q", got)
			}

			// A word sent: its Enter clears it, the screen not drawn again.
			typeKeys(t, s, p, sub, "pear")
			drawFrame(t, s, p, tc.word)
			if _, err := s.Submit(t.Context(), emptyBoxLine); !errors.Is(err, ErrBoxHoldsText) {
				t.Fatalf("the word again: %v", err)
			}
			typeKeys(t, s, p, sub, "\r")
			if res, err := s.Submit(t.Context(), emptyBoxLine); err != nil || !res.Entered {
				t.Fatalf("the word sent: %+v %v", res, err)
			}
			if got := written(t, p) + written(t, p); got != emptyBoxLine.Text+"\r" {
				t.Fatalf("written %q", got)
			}
		})
	}
}

// TestAWordNotDrawnYetHoldsTheLine: the box drawn empty, the caret at its
// start, and a person's key written after it: until the agent has drawn
// anything since the key went in, the screen is the one before it, and the
// line waits; drawn (here with the word), it is judged by the screen.
func TestAWordNotDrawnYetHoldsTheLine(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude")}))
	drawFrame(t, s, p, claudeBoxPlaceholder)
	typeKeys(t, s, p, controller(t, s, "ann"), "p")
	if _, err := s.Submit(t.Context(), emptyBoxLine); !errors.Is(err, ErrBoxHoldsText) {
		t.Fatalf("before the key is drawn: %v", err)
	}
	drawFrame(t, s, p, claudeBox("p", 4, true))
	if _, err := s.Submit(t.Context(), emptyBoxLine); !errors.Is(err, ErrBoxHoldsText) {
		t.Fatalf("the key drawn: %v", err)
	}
	nothingIn(t, s, p)
}

// TestAWordTypedAsTheLineWaitsForTheWriterHoldsIt: the writer is held on
// a person's earlier write (an arrow key) and the person's word is queued
// behind it before the line: the line's look, before its text, finds the
// box empty, and the writer, taking the text after the word, refuses it
// (the text's guard), nothing of it written. Without the guard the line
// would be typed after the word.
func TestAWordTypedAsTheLineWaitsForTheWriterHoldsIt(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude"), InputWait: 30 * time.Second}))
	drawFrame(t, s, p, claudeBoxPlaceholder)
	sub := controller(t, s, "ann")
	gate := make(chan struct{})
	p.onBytes = func(b []byte) {
		if string(b) == "\x1b[A" {
			<-gate
		}
	}
	keys := make(chan error, 2)
	go func() { keys <- s.Input(sub, []byte("\x1b[A")) }()
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
	if a, b := written(t, p), written(t, p); a != "\x1b[A" || b != "pear" {
		t.Fatalf("written %q, %q", a, b)
	}
	nothingIn(t, s, p)
}

// TestAChoicesKeysAreNotTextInTheBox: a reply's keys to a question the
// session shows (a choice's "y") go to its dialog, not the box: the line
// is typed once the dialog is gone, whatever the cursor shows.
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
	if held, _ := s.box.holds(); held {
		t.Fatal("a choice's keys count as text in the box")
	}
}
