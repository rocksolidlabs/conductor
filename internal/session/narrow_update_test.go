package session

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The update question's Escape typed in a terminal 72 columns wide (a page
// of 1280 by 800 with touch), the window made narrower than the session's
// 80 after the question was drawn: does the question go once the dialog
// does? The session withdraws a startup question an Escape may have
// answered only once the screen, read whole, shows it gone, and a screen
// made smaller with a character lost is no such reading until the program
// erases it whole (screenShowsNoStartup, Screen.DrawnAt). Codex does: each
// resize clears its view from its first cell and draws it again in one
// synchronized update (tui/src/tui.rs, Tui::draw's clear_for_viewport_change
// then custom_terminal.rs clear_after_position, github.com/openai/codex at
// rust-v0.163.0 and rust-v0.169.0; its view is the whole alternate screen),
// so its question goes. A program that draws nothing on a resize and erases
// its dialog by lines (the e2e stub's way before #716) leaves the screen
// cut, and the question stays: that is the stub's, not the session's.

// codexUpdateQuestion is the catalog's update question, as the server
// starts a session with it.
func codexUpdateQuestion() StartupQuestion {
	return StartupQuestion{
		Pattern: regexp.MustCompile(`(?i)update\s*now\s*\(\s*runs`),
		Words:   regexp.MustCompile(`(?i)(update\s*available)\W*?(\d+(?:\.\d+)+\s*(?:→|->)\s*\d+(?:\.\d+)+)`),
		Answers: []Option{{Label: "Skip this update", Input: "\x1b"}, {Label: "Skip until next version", Input: "3"}},
	}
}

// The e2e stub's banner, longer than 72 columns (it wraps at 80), and its
// update question, printed line by line.
const (
	stubBanner = "conductor e2e stub: solo in run r-0123456789 (codex, session 0b6a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d, trusted)\r\n"
	stubUpdate = "Update available · 0.161.0 → 0.162.0\r\nRelease notes: https://github.com/openai/codex/releases/latest\r\n\r\n› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n\r\nenter continue · esc skip\r\n"
	// stubEraseUpdate is the stub's erase_dialog 8.
	stubEraseUpdate = "\r\x1b[2K" + "\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K"
)

// codexUpdateView is Codex's update question drawn at the top of its
// alternate screen, as its rows (0.163.0's, seen in a PTY: codexUpdateOverSplash).
var codexUpdateView = []string{"", "  Update available · 0.163.0 → 0.164.0", "  Release notes: https://github.com/openai/codex/releases/latest", "", "› 1. Update now (runs `npm install -g @openai/codex`)", "  2. Skip", "  3. Skip until next version", "", "  enter continue · esc skip"}

// codexFrame is a Codex frame after a resize: the screen cleared from its
// first cell, then rows drawn, in one synchronized update.
func codexFrame(rows []string) string {
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[1;1H\x1b[J")
	for i, r := range rows {
		fmt.Fprintf(&b, "\x1b[%d;1H%s", i+1, r)
	}
	b.WriteString("\x1b[?2026l")
	return b.String()
}

// codexNextView is the view Codex draws once the question is skipped: only
// the cells that changed, each row of the dialog written over and cleared
// to its end (custom_terminal.rs writes the diff, Clear(UntilNewLine)).
func codexNextView() string {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	next := []string{"", "  >_ OpenAI Codex (v0.163.0)", "", "", "", "", "", "", "› Ask Codex to do anything"}
	for i, r := range next {
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[K", i+1, r)
	}
	b.WriteString("\x1b[?2026l")
	return b.String()
}

// startupLooked waits until the watcher of the questions asked during the
// work, which withdraws a startup question its Escape answered (escGone),
// has looked at everything fed and its look has ended, and returns the
// attention then.
func startupLooked(t *testing.T, s *Local, marker string) Attention {
	t.Helper()
	waitAttention(t, s, func(Attention) bool {
		return strings.Contains(s.asked.Text(), marker) && s.asked.LookedAt() == s.asked.Fed()
	})
	s.asked.fireMu.Lock()
	a := s.Info().Attention
	s.asked.fireMu.Unlock()
	return a
}

func TestAnUpdateQuestionSkippedInANarrowerWindowGoesAsCodexRedraws(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: []StartupQuestion{codexUpdateQuestion()}}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\x1b[?1049h" + codexFrame(append([]string{stubBanner[:100]}, codexUpdateView...))))
	att := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	// The page's window, 72 columns: Codex draws its view again for it.
	if err := s.Resize(sub, 72, 24); err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(codexFrame(codexUpdateView)))
	if a := startupLooked(t, s, "Update available"); a.ID != att.ID {
		t.Fatalf("the question at 72 columns: %+v", a)
	}
	if err := s.Input(sub, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "\x1b" {
		t.Fatalf("written %q", got)
	}
	p.outW.Write([]byte(codexNextView()))
	if a := startupLooked(t, s, "Ask Codex to do anything"); a.State == AttentionNeedsInput {
		t.Fatalf("the question skipped stays: %+v", a)
	}
}

// The stub's way: nothing drawn for the resize, the dialog erased by
// lines. The screen made narrower lost the banner's 73rd column on, and
// nothing erased it whole since: it is no reading of the dialog gone, and
// the question stays (the session's rule, which this test holds).
func TestAnUpdateQuestionSkippedInANarrowerWindowStaysWhenNothingRedraws(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: []StartupQuestion{codexUpdateQuestion()}}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(stubBanner + stubUpdate))
	att := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if err := s.Resize(sub, 72, 24); err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "\x1b" {
		t.Fatalf("written %q", got)
	}
	p.outW.Write([]byte(stubEraseUpdate + "> ready"))
	a := startupLooked(t, s, "> ready")
	if uncut, _, _ := s.asked.DrawnAt(); uncut {
		t.Fatal("the screen made narrower reads as drawn whole")
	}
	if a.ID != att.ID || a.State != AttentionNeedsInput {
		t.Fatalf("withdrawn on a screen cut by the narrower window: %+v", a)
	}
	// The same at 92 columns (the window grown): nothing lost, the dialog
	// read gone, the question goes.
	s2, p2 := newLocalWith(t, quiet(Options{Questions: []StartupQuestion{codexUpdateQuestion()}}))
	sub2, err := s2.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p2.outW.Write([]byte(stubBanner + stubUpdate))
	waitAttention(t, s2, func(a Attention) bool { return a.Source == SourceTrust })
	if err := s2.Resize(sub2, 92, 24); err != nil {
		t.Fatal(err)
	}
	if err := s2.Input(sub2, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p2)
	p2.outW.Write([]byte(stubEraseUpdate + "> ready"))
	if a := startupLooked(t, s2, "> ready"); a.State == AttentionNeedsInput {
		t.Fatalf("at 92 columns the question stays: %+v", a)
	}
}
