package session

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// claudeStartupQuestions are Claude Code's startup questions as the
// built-in catalog entry has them (those not asked at any time): the
// folder's trust and the sign-in screen.
func claudeStartupQuestions(t *testing.T) []StartupQuestion {
	t.Helper()
	claude, ok := catalog.Default().Get("claude")
	if !ok {
		t.Fatal("no built-in claude")
	}
	var out []StartupQuestion
	for _, q := range claude.Questions {
		if q.Anytime {
			continue
		}
		answers := make([]Option, len(q.Answers))
		for i, a := range q.Answers {
			answers[i] = Option{Label: a.Label, Input: a.Input}
		}
		sq := StartupQuestion{Pattern: regexp.MustCompile(q.Prompt), Answers: answers}
		if q.Words != "" {
			sq.Words = regexp.MustCompile(q.Words)
		}
		out = append(out, sq)
	}
	return out
}

// claudeSignInScreen is Claude Code 2.1.319 signed out, as #659's live
// check met it (the URL made up here).
const claudeSignInScreen = "\x1b[2J\x1b[HWelcome to Claude Code\r\n\r\n Browser didn't open? Use the url below to sign in (c to copy)\r\n\r\nhttps://example.invalid/sign-in?code=true\r\n\r\n\r\n Paste code here if prompted > "

// Claude Code signed out waits at its sign-in screen, a startup question
// with no answers: it holds a submission back and shows under Needs you.
// A person who signs in through the browser types nothing in the
// terminal: Claude Code leaves the screen by itself and draws its prompt.
// The question is withdrawn then, as every startup question goes once
// its screen is behind, and a submission is taken, with no key written
// meanwhile. (#691's review, finding 2.)
func TestASignInScreenTheProgramLeavesByItselfIsWithdrawn(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: claudeStartupQuestions(t)}))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	p.outW.Write([]byte(claudeSignInScreen))
	signIn := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if !strings.Contains(signIn.Message, "Browser didn't open") || len(signIn.Options) != 0 {
		t.Fatalf("the sign-in screen: %+v", signIn)
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: nater}); !errors.Is(err, ErrTrustQuestion) {
		t.Fatalf("a submission at the sign-in screen: %v", err)
	}
	// Redrawn as it waits (its spinner, its prompt's cursor): still there.
	p.outW.Write([]byte("\x1b[9;31H "))
	time.Sleep(3 * patternQuiet)
	if a := s.Info().Attention; a.ID != signIn.ID || a.State != AttentionNeedsInput {
		t.Fatalf("the sign-in screen redrawn is not the question still: %+v", a)
	}
	noWrite(t, p, 50*time.Millisecond)
	// Signed in through the browser: the screen cleared, the prompt drawn.
	p.outW.Write([]byte("\x1b[2J\x1b[HLogin successful. Press Enter to continue\x1b[2J\x1b[H\x1b[2;1H> Try \"fix lint errors\""))
	if a := waitAttention(t, s, func(a Attention) bool { return a.ID != signIn.ID || a.State != AttentionNeedsInput }); a.State == AttentionNeedsInput && a.Source == SourceTrust {
		t.Fatalf("a startup question after the sign-in: %+v", a)
	}
	if info := s.Info(); info.LastAnswer != nil {
		t.Fatalf("withdrawn, yet answered: %+v", info.LastAnswer)
	}
	noWrite(t, p, 50*time.Millisecond)
	res, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: nater})
	if err != nil || !res.Entered {
		t.Fatalf("a submission once the screen moved on: %+v %v", res, err)
	}
	if got := nextWrite(t, p); got != "fix the bug" {
		t.Fatalf("written %q", got)
	}
}
