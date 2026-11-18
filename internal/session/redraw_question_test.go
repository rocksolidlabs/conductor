package session

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// signInQuestion is a startup dialog of another catalog entry found and
// worded as the update question is: by a choice's row, and by a heading.
func signInQuestion() StartupQuestion {
	return StartupQuestion{
		Pattern: regexp.MustCompile(`(?i)sign\s*in\s*now`),
		Words:   regexp.MustCompile(`(?i)(sign\s*in\s*required)`),
		Answers: []Option{{Label: "Not now", Input: "2"}},
	}
}

// signInChoices is that dialog's choices, its heading off a short screen.
var signInChoices = []string{"› 1. Sign in now", "  2. Not now", "", "  enter continue"}

// redrawSession is a session with Codex's update question (and another
// dialog, signInQuestion) and a viewer at 80 by 24, and fed, which returns
// once the session's pump has fed the screens the questions are read from
// with everything written to p so far (the viewers have it after them): the
// test looks at the screen it wrote, never at one the pump has not reached.
func redrawSession(t *testing.T) (s *Local, p *fakeProc, sub *Subscription, write func(string)) {
	t.Helper()
	s, p = newLocalWith(t, quiet(Options{Questions: []StartupQuestion{codexUpdateQuestion(), signInQuestion()}}))
	var err error
	sub, err = s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
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

// codexFrameIn is codexFrame with the rows drawn in the order given (each
// an index into rows, at its own row of the screen): a program that draws
// only the cells that changed may draw a frame's rows in any order.
func codexFrameIn(rows []string, order []int) string {
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[1;1H\x1b[J")
	for _, i := range order {
		fmt.Fprintf(&b, "\x1b[%d;1H%s", i+1, rows[i])
	}
	b.WriteString("\x1b[?2026l")
	return b.String()
}

// One question of an agent read again from its screen while it still
// waits is the same question: Codex's update dialog drawn again (the screen
// cleared and the dialog drawn anew, as when a page attaches the session's
// terminal and the window's size changes), looked at after every byte of
// the redraw, keeps the question's id and its words, the heading's
// ("Update available 0.163.0 → 0.164.0"), at every look. A read that shows
// its first choice ("Update now (runs") and not its heading whole is no
// new question, and its words are never the choice's: a run's log notes
// each new question once, by its id.
func TestAStartupQuestionDrawnAgainInPartsIsTheSameQuestion(t *testing.T) {
	heading := 1 // codexUpdateView's row of the heading
	topDown := make([]int, len(codexUpdateView))
	headingLast := make([]int, 0, len(codexUpdateView))
	for i := range codexUpdateView {
		topDown[i] = i
		if i != heading {
			headingLast = append(headingLast, i)
		}
	}
	headingLast = append(headingLast, heading)
	for _, tc := range []struct {
		name string
		// rows is the window's height for the redraw, frames what Codex
		// draws in it.
		rows   uint16
		frames []string
	}{
		{"drawn again from the top", 24, []string{codexFrame(codexUpdateView)}},
		{"its heading drawn last", 24, []string{codexFrameIn(codexUpdateView, headingLast)}},
		// A window too short for the dialog: Codex draws its last rows, the
		// heading above them off the screen, again and again (each size the
		// page's terminal takes as it is laid out), until the output's tail
		// holds no heading either.
		{"in a window too short for its heading", 5, repeated(codexFrame(codexUpdateView[4:]), 8)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p, sub, write := redrawSession(t)
			write("\x1b[?1049h" + codexFrame(codexUpdateView))
			first := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
			if first.Message != "Update available 0.163.0 → 0.164.0" {
				t.Fatalf("the question's words: %q", first.Message)
			}
			look := func(at string) {
				t.Helper()
				s.trust.CheckNow()
				if a := s.Info().Attention; a.ID != first.ID || a.Message != first.Message || a.State != AttentionNeedsInput || a.Source != SourceTrust {
					t.Fatalf("%s: the question is %+v, was %+v", at, a, first)
				}
			}
			if tc.rows != 24 {
				if err := s.Resize(sub, 80, tc.rows); err != nil {
					t.Fatal(err)
				}
			}
			for n, frame := range tc.frames {
				if n > 0 {
					// The same frame again: looked at whole.
					write(frame)
					look(fmt.Sprintf("frame %d", n))
					continue
				}
				for i := range len(frame) {
					write(frame[i : i+1])
					look(fmt.Sprintf("frame %d, byte %d (%q drawn)", n, i, frame[:i+1]))
				}
			}
			if tc.rows != 24 {
				if strings.Contains(s.trust.Text(), "available") {
					t.Fatalf("the output's tail still holds the heading: %q", s.trust.Text())
				}
				// The window as tall as before: the dialog drawn whole again.
				if err := s.Resize(sub, 80, 24); err != nil {
					t.Fatal(err)
				}
				frame := codexFrame(codexUpdateView)
				for i := range len(frame) {
					write(frame[i : i+1])
					look(fmt.Sprintf("drawn whole again, byte %d", i))
				}
			}
			// Still the question, with its own choices: Skip this update's
			// Escape is written.
			if err := s.AnswerInput(t.Context(), sub, first.ID, []byte("\x1b")); err != nil {
				t.Fatal(err)
			}
			if got := nextWrite(t, p); got != "\x1b" {
				t.Fatalf("written %q", got)
			}
		})
	}
}

// repeated is n copies of s.
func repeated(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// A screen that shows the update dialog's first choice and never its
// heading (a window too short for it from the start) shows no question
// read whole: nothing is asked, so no words cut from the choice reach a
// page or a run's log, and the session tells it as a dialog it does not
// read whole (UnreadDialog: what waits on its prompt says so). The dialog
// is still on the screen, and a prompt typed into it is refused all the
// same (Submit: a startup question the screen shows), nothing written.
func TestAStartupQuestionReadWithoutItsWordsIsNotAsked(t *testing.T) {
	s, p, sub, write := redrawSession(t)
	if err := s.Resize(sub, 80, 5); err != nil {
		t.Fatal(err)
	}
	write("\x1b[?1049h" + codexFrame(codexUpdateView[4:]))
	if !s.trust.CheckNow() {
		t.Fatal("the update dialog's choice is not found on the screen")
	}
	if a := s.Info().Attention; a.State == AttentionNeedsInput {
		t.Fatalf("asked without its words: %+v", a)
	}
	if !s.UnreadDialog() {
		t.Fatal("the dialog not read whole is not told")
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: sub}); !errors.Is(err, ErrTrustQuestion) {
		t.Fatalf("a prompt typed at the dialog: %v", err)
	}
	select {
	case b := <-p.input:
		t.Fatalf("written %q", b)
	default:
	}
}

// askedThenShort asks Codex's update question at 80 by 24, then makes the
// window five rows tall, where Codex draws the dialog's last rows, its
// heading off the screen, as many times as it takes the output's tail to
// hold no heading either (each frame looked at). It returns the question.
func askedThenShort(t *testing.T, s *Local, sub *Subscription, write func(string), frames int) Attention {
	t.Helper()
	write("\x1b[?1049h" + codexFrame(codexUpdateView))
	first := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if err := s.Resize(sub, 80, 5); err != nil {
		t.Fatal(err)
	}
	for range frames {
		write(codexFrame(codexUpdateView[4:]))
		s.trust.CheckNow()
	}
	if a := s.Info().Attention; a.ID != first.ID || a.Message != first.Message {
		t.Fatalf("the question in the short window: %+v, was %+v", a, first)
	}
	if s.UnreadDialog() {
		t.Fatal("the question asked is told as a dialog not read whole")
	}
	return first
}

// The question kept while its dialog shows without its heading (a window
// too short for it) is answered by its own choice, from a page's choice
// (the run page's chat, the session page) as from anywhere: the dialog on
// the screen is that question still waiting, and its key is written once.
func TestAKeptStartupQuestionIsAnsweredWithoutItsHeading(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames int
	}{
		{"its heading still in the output's tail", 1},
		{"its heading gone from the output's tail", 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p, sub, write := redrawSession(t)
			first := askedThenShort(t, s, sub, write, tc.frames)
			if tc.frames > 1 && strings.Contains(s.trust.Text(), "available") {
				t.Fatalf("the output's tail still holds the heading: %q", s.trust.Text())
			}
			if err := s.AnswerInput(t.Context(), sub, first.ID, []byte("3")); err != nil {
				t.Fatalf("the choice in the short window: %v", err)
			}
			if got := nextWrite(t, p); got != "3" {
				t.Fatalf("written %q", got)
			}
			if err := s.AnswerInput(t.Context(), sub, first.ID, []byte("3")); !errors.Is(err, ErrQuestionAnswered) {
				t.Fatalf("a second reply: %v", err)
			}
		})
	}
}

// A reply to that question kept is refused, nothing written, where the
// screen is not its dialog: the dialog gone (another screen drawn whole),
// another catalog question's dialog drawn in its place, the screen being
// drawn again with none of its choices on it yet, or the question answered
// by a key typed in the terminal. Only the same dialog, its choice row on
// the screen now, takes it.
func TestAKeptStartupQuestionsReplyIsRefusedWhereItsDialogIsNot(t *testing.T) {
	for _, tc := range []struct {
		name string
		// then is what happens after the question was kept in the short
		// window, before the reply.
		then func(t *testing.T, s *Local, p *fakeProc, sub *Subscription, write func(string))
	}{
		{"the dialog gone", func(t *testing.T, s *Local, p *fakeProc, sub *Subscription, write func(string)) {
			write(codexFrame([]string{"", "  >_ OpenAI Codex", "", "› Ask Codex to do anything", ""}))
		}},
		{"another dialog in its place", func(t *testing.T, s *Local, p *fakeProc, sub *Subscription, write func(string)) {
			write(codexFrame(signInChoices))
		}},
		{"none of its choices drawn yet", func(t *testing.T, s *Local, p *fakeProc, sub *Subscription, write func(string)) {
			write("\x1b[?2026h\x1b[1;1H\x1b[J")
		}},
		{"answered in the terminal", func(t *testing.T, s *Local, p *fakeProc, sub *Subscription, write func(string)) {
			if err := s.Input(sub, []byte("3")); err != nil {
				t.Fatal(err)
			}
			if got := nextWrite(t, p); got != "3" {
				t.Fatalf("written %q", got)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p, sub, write := redrawSession(t)
			first := askedThenShort(t, s, sub, write, 8)
			tc.then(t, s, p, sub, write)
			if err := s.AnswerInput(t.Context(), sub, first.ID, []byte("\x1b")); !errors.Is(err, ErrQuestionAnswered) {
				t.Fatalf("the reply: %v", err)
			}
			select {
			case b := <-p.input:
				t.Fatalf("written %q", b)
			default:
			}
		})
	}
}

// A look that lands in the middle of the dialog's first frame (a
// synchronized update under way, its choices drawn and its heading last,
// the version drawn in part: "→ 0.1") asks nothing; the look at the
// frame's end asks it whole, once, and it is answered by its choice.
func TestAStartupQuestionIsNotAskedFromHalfItsFirstFrame(t *testing.T) {
	s, p, sub, write := redrawSession(t)
	order := []int{0, 2, 3, 4, 5, 6, 7, 8, 1}
	frame := "\x1b[?1049h" + codexFrameIn(codexUpdateView, order)
	cut := strings.Index(frame, "→ 0.1") + len("→ 0.1")
	write(frame[:cut])
	s.trust.CheckNow()
	if a := s.Info().Attention; a.State == AttentionNeedsInput {
		t.Fatalf("asked from half a frame: %+v", a)
	}
	write(frame[cut:])
	s.trust.CheckNow()
	first := s.Info().Attention
	if first.Source != SourceTrust || first.Message != "Update available 0.163.0 → 0.164.0" {
		t.Fatalf("the question at the frame's end: %+v", first)
	}
	if err := s.AnswerInput(t.Context(), sub, first.ID, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "\x1b" {
		t.Fatalf("written %q", got)
	}
}
