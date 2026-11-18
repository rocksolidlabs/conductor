package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// redrawSession is a session with Codex's update question and a viewer at
// 80 by 24, and fed, which returns once the session's pump has fed the
// screens the questions are read from with everything written to p so far
// (the viewers have it after them): the test looks at the screen it wrote,
// never at one the pump has not reached.
func redrawSession(t *testing.T) (s *Local, p *fakeProc, sub *Subscription, write func(string)) {
	t.Helper()
	s, p = newLocalWith(t, quiet(Options{Questions: []StartupQuestion{codexUpdateQuestion()}}))
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
// page or a run's log. The dialog is still on the screen, and a prompt
// typed into it is refused all the same (Submit: a startup question the
// screen shows), nothing written.
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
	if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: sub}); !errors.Is(err, ErrTrustQuestion) {
		t.Fatalf("a prompt typed at the dialog: %v", err)
	}
	select {
	case b := <-p.input:
		t.Fatalf("written %q", b)
	default:
	}
}
