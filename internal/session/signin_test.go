package session

import (
	"errors"
	"regexp"
	"strings"
	"sync"
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
	// Redrawn as it waits (its spinner, its prompt's cursor): still there,
	// once both watchers have looked at the redraw and their callbacks
	// (fireTrust, missAsked: escGone) have returned.
	trustFed, askedFed := s.trust.Fed(), s.asked.Fed()
	trustLooked, askedLooked := lookedPast(t, s.trust, trustFed), lookedPast(t, s.asked, askedFed)
	p.outW.Write([]byte("\x1b[9;31H "))
	trustLooked()
	askedLooked()
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

// lookedPast arms w to note its first quiet look that takes a text fed
// past fed Feeds, and returns the wait for it: the wait returns once that
// look's callback (fire or miss) has returned, the look and its callback
// being made under w's fireMu. Armed before the output is written, so the
// look cannot come first; the wait is bounded only against a hang.
func lookedPast(t *testing.T, w *PatternWatcher, fed uint64) func() {
	t.Helper()
	looked := make(chan struct{})
	var once sync.Once
	w.mu.Lock()
	w.afterLook = func() {
		if w.LookedAt() > fed {
			once.Do(func() { close(looked) })
		}
	}
	w.mu.Unlock()
	return func() {
		t.Helper()
		select {
		case <-looked:
		case <-time.After(30 * time.Second):
			t.Fatal("the watcher never looked at the output")
		}
		// The look's callback runs under fireMu: once it is taken, it
		// has returned.
		w.fireMu.Lock()
		w.mu.Lock()
		w.afterLook = nil
		w.mu.Unlock()
		w.fireMu.Unlock()
	}
}

// Claude Code's sign-in screen cleared just after the screen the question
// was found on was read and marked, between the question shown and its
// hold (the session's afterAsked): the clearing output is output after
// the mark, and the question is withdrawn as the fire ends, with no
// further output and no key written; a submission is taken then. Held
// against the screen as it was once the question showed, the clearing was
// taken for part of what it was asked on, and with nothing drawn after it
// the question stayed under Needs you and every submission was refused.
// (#691's review, round 2, finding 3.)
func TestASignInScreenClearedAsItIsAskedIsWithdrawn(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: claudeStartupQuestions(t)}))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	asked := make(chan struct{})
	var once sync.Once
	hook := func() {
		once.Do(func() {
			defer close(asked)
			// Signed in through the browser as the question is asked.
			p.outW.Write([]byte("\x1b[2J\x1b[H\x1b[2;1H> Try \"fix lint errors\""))
			deadline := time.Now().Add(30 * time.Second)
			for {
				text := s.asked.Text()
				if strings.Contains(text, "fix lint errors") && !strings.Contains(text, "Browser didn't open") {
					return
				}
				if time.Now().After(deadline) {
					t.Errorf("the clearing output was never fed: %q", text)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
	s.afterAsked.Store(&hook)
	p.outW.Write([]byte(claudeSignInScreen))
	select {
	case <-asked:
	case <-time.After(30 * time.Second):
		t.Fatal("the sign-in screen was never asked")
	}
	// Read once the fire that asked it has returned (it runs under the
	// watcher's fireMu).
	s.trust.fireMu.Lock()
	a := s.Info().Attention
	s.trust.fireMu.Unlock()
	if a.State == AttentionNeedsInput && a.Source == SourceTrust {
		t.Fatalf("the sign-in question outlived its screen: %+v", a)
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

// Claude Code's sign-in screen cleared after the startup questions'
// watcher took its text and before the look's callback read the screen
// (the watcher's afterLook): the look's text shows the sign-in, the
// screen no longer does. The question is never asked, so it is not left
// under Needs you, and a submission is taken, with no key written. When
// the look's text decided the question and the screen's mark was read
// after it, the question was asked on the screen before the clearing and
// marked with the screen after it: no output came after the mark, the
// withdrawal never did, and every submission was refused. (#691's review,
// round 3, finding 1.)
func TestASignInScreenClearedBeforeItsLookIsReadIsNotAsked(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: claudeStartupQuestions(t)}))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	looked := make(chan struct{})
	var once sync.Once
	s.trust.mu.Lock()
	s.trust.afterLook = func() {
		if !strings.Contains(s.trust.Text(), "Browser didn't open") {
			return
		}
		once.Do(func() {
			defer close(looked)
			// Signed in through the browser as the look is made.
			p.outW.Write([]byte("\x1b[2J\x1b[H\x1b[2;1H> Try \"fix lint errors\""))
			deadline := time.Now().Add(30 * time.Second)
			for {
				text := s.asked.Text()
				if strings.Contains(text, "fix lint errors") && !strings.Contains(text, "Browser didn't open") {
					return
				}
				if time.Now().After(deadline) {
					t.Errorf("the clearing output was never fed: %q", text)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
	s.trust.mu.Unlock()
	p.outW.Write([]byte(claudeSignInScreen))
	select {
	case <-looked:
	case <-time.After(30 * time.Second):
		t.Fatal("the sign-in screen was never looked at")
	}
	// Read once the look's callback has returned (it runs under the
	// watcher's fireMu).
	s.trust.fireMu.Lock()
	a := s.Info().Attention
	s.trust.fireMu.Unlock()
	if a.State == AttentionNeedsInput && a.Source == SourceTrust {
		t.Fatalf("the sign-in question was asked on a screen gone: %+v", a)
	}
	if info := s.Info(); info.LastAnswer != nil {
		t.Fatalf("answered: %+v", info.LastAnswer)
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

// DialogMayShow reads any agent's screen, with no startup question and
// no watch: a dialog's numbered choice on it, highlighted or not, is a
// dialog (#714); Claude Code's and Codex's prompts, a ❯ or › row with no
// number, are not; half a frame (a synchronized update under way) may be.
func TestDialogMayShowReadsAnyScreen(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	shows := func(screen, want string, dialog bool) {
		t.Helper()
		p.outW.Write([]byte(screen))
		if !waitFor(10*time.Second, func() bool { return strings.Contains(strings.Join(s.ScreenLines(24), "\n"), want) }) {
			t.Fatalf("the screen never showed %q: %q", want, s.ScreenLines(24))
		}
		if got := s.DialogMayShow(); got != dialog {
			t.Fatalf("DialogMayShow on %q: %v, want %v", s.ScreenLines(24), got, dialog)
		}
	}
	shows("\x1b[2J\x1b[H────────\r\n❯ Try \"create a util\"\r\n────────\r\n  ? for shortcuts", "? for shortcuts", false)
	shows("\x1b[2J\x1b[H\x1b[1m›\x1b[0m Ask Codex to do anything\r\n\r\n ? for shortcuts", "Ask Codex", false)
	shows("\x1b[2J\x1b[H  Select a sign-in method\r\n\r\n ❯ 1. Acme account\r\n   2. Quit", "2. Quit", true)
	shows("\x1b[2J\x1b[H  Update available\r\n\x1b[7m› 1. Update now\x1b[27m\r\n  2. Skip", "2. Skip", true)
	shows("\x1b[2J\x1b[H❯ Try it\x1b[?2026h\r\nhalf", "half", true)
}
