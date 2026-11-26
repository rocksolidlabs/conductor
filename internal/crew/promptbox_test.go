package crew

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A member's prompt is typed only into an empty prompt box (#1142): a word
// a person left in the member's terminal holds it, the run's log says so
// once, and it is typed alone once the box is empty again.

// claudeShape is the built-in Claude Code's prompt shape.
func claudeShape(t *testing.T) *regexp.Regexp {
	t.Helper()
	a, _ := catalog.Default().Get("claude")
	re, err := catalog.CompilePattern(a.PromptShape)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

// claudeBox is Claude Code's prompt box drawn by hand, as the session's
// tests draw it (inputbox_test.go): a rule, the ❯ mark and a no-break
// space, text from column 3, a rule and the footer, as one synchronized
// update that hides the cursor and ends with it at column col of the box's
// row, shown or not (as Claude Code runs by default, never shown).
func claudeBox(text string, col int, shown bool) string {
	rule := strings.Repeat("─", 80)
	show := ""
	if shown {
		show = "\x1b[?25h"
	}
	return "\x1b[?2026h\x1b[?25l\x1b[2J\x1b[H" + rule + "\r\n❯ " + text + "\x1b[22m\r\n" + rule + "\r\n  ? for shortcuts" + fmt.Sprintf("\x1b[2;%dH", col) + show + "\x1b[?2026l"
}

const placeholder = `Try "fix lint errors"`

// boxEngine is an engine whose member lead runs Claude Code's prompt shape
// and whose readiness wait passes once lead's process has done what
// before does (its keys typed, its box drawn), counting its looks: each
// try of the prompt begins with one.
func boxEngine(t *testing.T, before func(p *sessiontest.FakeProc, l *session.Local, sub *session.Subscription)) (e *Engine, fl *fakeLauncher, looks *atomic.Int64, subs <-chan *session.Subscription) {
	t.Helper()
	e, fl = newEngine(t)
	fl.shape = claudeShape(t)
	gate := make(chan struct{})
	looks = new(atomic.Int64)
	e.await = func(ctx context.Context, _ *session.Local, _ func(string)) error {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		looks.Add(1)
		return nil
	}
	sc := make(chan *session.Subscription, 1)
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, l *session.Local) {
		sub, err := l.Attach("", session.RoleControl, "", 80, 24, nopSink{})
		if err != nil {
			t.Error(err)
			return
		}
		before(p, l, sub)
		sc <- sub
		close(gate)
	}
	return e, fl, looks, sc
}

// drawn prints frame and waits until the member's screen shows want.
func drawn(t *testing.T, p *sessiontest.FakeProc, l *session.Local, frame, want string) {
	t.Helper()
	_ = p.Print(frame)
	if !boxUntil(10*time.Second, func() bool { return strings.Contains(strings.Join(l.ScreenLines(24), "\n"), want) }) {
		t.Errorf("the screen never showed %q: %q", want, l.ScreenLines(24))
	}
}

// keys types keys as a person in lead's terminal.
func keys(t *testing.T, l *session.Local, sub *session.Subscription, k string) {
	t.Helper()
	if err := l.Input(sub, []byte(k)); err != nil {
		t.Error(err)
	}
}

// boxUntil polls cond until it holds, or d has passed.
func boxUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return true
}

const (
	boxLine       = "lead's prompt box holds text: send it or clear it"
	boxEditedLine = "lead's prompt box holds text: send it, or clear it and press Enter"
)

// boxHeld reports whether the run's member lead is marked held by its
// prompt box (MemberState.BoxHeld), what the Brakeman desk reads.
func boxHeld(t *testing.T, e *Engine, runID string) bool {
	t.Helper()
	r, ok := e.Get(runID)
	if !ok || len(r.Members) == 0 {
		t.Fatalf("run %s not found", runID)
	}
	return r.Members[0].BoxHeld
}

// TestAMembersPromptWaitsWhileItsBoxHoldsAPersonsWord: a word a person
// typed while the member's agent loaded, shown in its box with the caret
// after it, holds the member's prompt: nothing typed, the run's log says
// so once, and not again over the box drawn again and the tries after
// each. The person clears the box (Ctrl-U, the box drawn empty, its
// placeholder after the caret): the prompt is typed and entered alone.
func TestAMembersPromptWaitsWhileItsBoxHoldsAPersonsWord(t *testing.T) {
	e, fl, looks, subs := boxEngine(t, func(p *sessiontest.FakeProc, l *session.Local, sub *session.Subscription) {
		keys(t, l, sub, "pear")
		drawn(t, p, l, claudeBox("pear", 7, true), "pear")
	})
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	sub := <-subs
	waitLogged(t, e, run.ID, boxLine, 1, 10*time.Second)
	l, p := fl.member("lead")
	if got := typed(p); len(got) != 1 || got[0] != "pear" {
		t.Fatalf("typed into the box: %q", got)
	}
	for range 3 {
		n := looks.Load()
		drawn(t, p, l, claudeBox("pear", 7, true), "pear")
		// Two looks after the box was drawn again: one whole try began
		// after it, and was refused.
		if !boxUntil(10*time.Second, func() bool { return looks.Load() >= n+2 }) {
			t.Fatalf("the prompt was not tried again: %d looks", looks.Load())
		}
	}
	if n := logCount(t, e, run.ID, boxLine); n != 1 {
		t.Fatalf("the run's log says it %d times", n)
	}
	if !boxHeld(t, e, run.ID) {
		t.Fatal("the member is not marked held by its box")
	}
	if got := typed(p); len(got) != 0 {
		t.Fatalf("typed into the box: %q", got)
	}
	keys(t, l, sub, "\x15")
	drawn(t, p, l, claudeBox(placeholder, 3, true), "Try")
	got := waitTyped(t, p, 10*time.Second)
	if !strings.HasPrefix(got, "\x15") || strings.Contains(got, "pear") || !strings.Contains(got, "Plan it.") || !strings.HasSuffix(got, "\r") {
		t.Fatalf("typed %q", got)
	}
	waitLogged(t, e, run.ID, "typed lead's prompt", 1, 10*time.Second)
	if boxHeld(t, e, run.ID) {
		t.Fatal("the member is still marked held by its box")
	}
}

// TestAMembersPromptWaitsBehindAnEnterItsAgentDropped: a word and an
// Enter typed as one input while the member's agent loads: the agent
// keeps the word in its box and drops the Enter. The box decides: the
// prompt waits while it begins with the word, noted once, and is typed
// alone once the box is blank (the word sent).
func TestAMembersPromptWaitsBehindAnEnterItsAgentDropped(t *testing.T) {
	e, fl, _, _ := boxEngine(t, func(p *sessiontest.FakeProc, l *session.Local, sub *session.Subscription) {
		keys(t, l, sub, "pear\r")
		drawn(t, p, l, claudeBox("pear", 7, false), "pear")
	})
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitLogged(t, e, run.ID, boxLine, 1, 10*time.Second)
	l, p := fl.member("lead")
	if got := typed(p); len(got) != 1 || got[0] != "pear\r" {
		t.Fatalf("typed into the box: %q", got)
	}
	drawn(t, p, l, claudeBox("", 3, false), "shortcuts")
	if got := waitTyped(t, p, 10*time.Second); strings.Contains(got, "pear") || !strings.Contains(got, "Plan it.") {
		t.Fatalf("typed %q", got)
	}
}

// TestAMembersPromptWaitsBehindEditedText: text edited with a key the
// session does not follow (a cursor key) holds the prompt while the box
// is not blank, and the run's log says once to send it, or clear it and
// press Enter: behind the placeholder, with the cursor hidden, a cleared
// box is not seen. The Enter into the cleared box lets it go.
func TestAMembersPromptWaitsBehindEditedText(t *testing.T) {
	e, fl, _, subs := boxEngine(t, func(p *sessiontest.FakeProc, l *session.Local, sub *session.Subscription) {
		keys(t, l, sub, "pear\x1b[D")
		drawn(t, p, l, claudeBox("pear", 6, false), "pear")
	})
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	sub := <-subs
	waitLogged(t, e, run.ID, boxEditedLine, 1, 10*time.Second)
	l, p := fl.member("lead")
	typed(p)
	keys(t, l, sub, "\x7f\x7f\x7f\x7f")
	drawn(t, p, l, claudeBox(placeholder, 3, false), "Try")
	keys(t, l, sub, "\r")
	drawn(t, p, l, claudeBox("", 3, false), "shortcuts")
	if got := waitTyped(t, p, 10*time.Second); !strings.HasSuffix(got, "\r") || strings.Contains(got, "Plan it.") {
		t.Fatalf("the person's keys: %q", got)
	}
	if got := waitTyped(t, p, 10*time.Second); !strings.Contains(got, "Plan it.") {
		t.Fatalf("typed %q", got)
	}
	if n := logCount(t, e, run.ID, boxEditedLine); n != 1 {
		t.Fatalf("the run's log says it %d times", n)
	}
}

// TestAMembersPromptGoesAtOnceIntoAnEmptyBox: a box showing its
// placeholder, a box whose agent never shows its cursor (no one typed),
// a box whose word a person sent with an Enter (the box blank after it),
// and a word typed and deleted: the prompt is typed at once, and the
// run's log says nothing of the box.
func TestAMembersPromptGoesAtOnceIntoAnEmptyBox(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before func(p *sessiontest.FakeProc, l *session.Local, sub *session.Subscription)
		person []string
	}{
		{"its placeholder", func(p *sessiontest.FakeProc, l *session.Local, _ *session.Subscription) {
			drawn(t, p, l, claudeBox(placeholder, 3, true), "Try")
		}, nil},
		{"its cursor never shown", func(p *sessiontest.FakeProc, l *session.Local, _ *session.Subscription) {
			drawn(t, p, l, claudeBox(placeholder, 3, false), "Try")
		}, nil},
		{"a word sent", func(p *sessiontest.FakeProc, l *session.Local, sub *session.Subscription) {
			keys(t, l, sub, "pear")
			keys(t, l, sub, "\r")
			drawn(t, p, l, claudeBox("", 3, false), "shortcuts")
		}, []string{"pear\r"}},
		{"a word deleted", func(p *sessiontest.FakeProc, l *session.Local, sub *session.Subscription) {
			keys(t, l, sub, "pe\x7f\x7f")
			drawn(t, p, l, claudeBox(placeholder, 3, false), "Try")
			// Its keys have no Enter: they come before the prompt's text.
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, fl, _, _ := boxEngine(t, tc.before)
			run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
			if err != nil {
				t.Fatal(err)
			}
			waitLogged(t, e, run.ID, "typed lead's prompt", 1, 10*time.Second)
			_, p := fl.member("lead")
			for _, w := range tc.person {
				if got := waitTyped(t, p, 10*time.Second); got != w {
					t.Fatalf("the person's keys: %q", got)
				}
			}
			if got := waitTyped(t, p, 10*time.Second); !strings.Contains(got, "Plan it.") || strings.Contains(got, "pear") {
				t.Fatalf("typed %q", got)
			}
			if n := logCount(t, e, run.ID, boxLine); n != 0 {
				t.Fatalf("the run's log says the box holds text %d times", n)
			}
		})
	}
}
