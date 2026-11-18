package session

import (
	"strings"
	"testing"
	"time"
)

// errorWaits reports whether the session shows the wait an error status
// raised.
func errorWaits(a Attention) bool { return statusErrorWait(a) }

// moveScreen draws a whole new screen twice, the screen's look taken after
// each (screenQuiet): a wait the screen may withdraw is withdrawn by it.
func moveScreen(s *Local, p *fakeProc) {
	var screen strings.Builder
	screen.WriteString("\x1b[2J\x1b[H")
	for i := range 30 {
		screen.WriteString(strings.Repeat(string(rune('a'+i%26)), 70) + "\r\n")
	}
	out(p, screen.String())
	s.screenQuiet("")
	out(p, "\x1b[2J\x1b[H"+strings.Repeat("z", 400))
	s.screenQuiet("")
}

// #991: a turn Codex's program status says ended in an error (working, then
// error: a refused key) is a wait for a person, in Conductor's own words,
// with no choices and no kind, recorded once. The same error again, idle,
// clear, a status not understood and the screen moving on leave it (a failed
// turn that sits idle still needs a person); the person's Enter in the
// terminal withdraws it.
func TestAnErrorStatusRaisesAWaitThatIdleLeaves(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionWorking })
	out(p, "Error: unexpected status 401 Unauthorized\r\n", codexStatus(StatusError, ""))
	a := waitAttention(t, s, statusWaits)
	if a.Message != "The agent's turn ended in an error; its terminal says why" || a.Kind != "" || len(a.Options) != 0 || a.ID == "" {
		t.Fatalf("the error's wait: %+v", a)
	}
	lastEntryIs(t, s, a.Message)
	n := len(entries(s))
	out(p, codexStatus(StatusError, ""), codexStatus(StatusIdle, ""), codexStatus(StatusError, ""), codexStatus(StatusClear, ""),
		"\x1b]7501;state=blocked:app=codex:kind=review\x1b\\")
	moveScreen(s, p)
	stays(t, s, settle+100*time.Millisecond, "the error's wait after idle, clear and the screen", func(b Attention) bool { return errorWaits(b) && b.ID == a.ID })
	if got := entries(s); len(got) != n {
		t.Fatalf("the error's wait was recorded again: %q", got[n-1:])
	}
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if b := waitAttention(t, s, none); b.Source != SourceInput {
		t.Fatalf("the Enter: %+v", b)
	}
}

// Working after the error withdraws its wait, settled after its second as
// any wait is; the next failed turn is told again, with an entry of its own:
// one per failed turn.
func TestWorkingAfterAnErrorWithdrawsItsWaitAndTheNextErrorIsToldAgain(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""), codexStatus(StatusError, ""))
	first := waitAttention(t, s, errorWaits)
	lastEntryIs(t, s, statusErrorWords)
	n := len(entries(s))
	out(p, codexStatus(StatusWorking, ""))
	waitAttention(t, s, statusWorks)
	lastEntryIs(t, s, string(AttentionWorking))
	out(p, codexStatus(StatusError, ""))
	second := waitAttention(t, s, errorWaits)
	if second.ID == first.ID {
		t.Fatalf("the second failed turn kept the first's wait: %+v", second)
	}
	lastEntryIs(t, s, statusErrorWords)
	got := entries(s)
	if len(got) != n+2 || got[n] != string(AttentionWorking) {
		t.Fatalf("entries after the first error %q, want working and the error", got[n-1:])
	}
}

// The error never replaces a question with words of its own: the agent's
// question reported through its hooks, a question held on the screen, the
// held turn; nor the done of the turn the agent's report just ended. A
// question on the screen gone while the status still says error, the
// error's wait shows in its place.
func TestAnErrorStatusLeavesWhatHasWordsOfItsOwn(t *testing.T) {
	const settle = 200 * time.Millisecond
	t.Run("the agent's question", func(t *testing.T) {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		out(p, codexStatus(StatusError, ""))
		stays(t, s, settle+100*time.Millisecond, "the question under an error", apiWaits)
	})
	t.Run("the done just reported", func(t *testing.T) {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
		reportState(t, s, AttentionDone, "finished")
		lastEntryIs(t, s, "finished")
		n := len(entries(s))
		out(p, codexStatus(StatusDone, ""), codexStatus(StatusError, ""))
		stays(t, s, settle+100*time.Millisecond, "the done under a stray error", func(a Attention) bool { return a.State == AttentionDone })
		if got := entries(s); len(got) != n {
			t.Fatalf("a stray error recorded %q", got[n:])
		}
	})
	t.Run("the held turn", func(t *testing.T) {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexStatus(StatusWorking, ""))
		go holdNow(s)
		heldShows(t, s)
		lastEntryIs(t, s, HeldWords)
		n := len(entries(s))
		out(p, codexStatus(StatusError, ""))
		stays(t, s, settle+100*time.Millisecond, "the hold under an error", func(a Attention) bool { return a.Source == SourceSubmit })
		if got := entries(s); len(got) != n {
			t.Fatalf("an error during the hold recorded %q", got[n:])
		}
	})
	t.Run("a question on the screen", func(t *testing.T) {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
		p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
		att := waitAttention(t, s, screenAsks)
		out(p, codexStatus(StatusError, ""))
		stays(t, s, settle+100*time.Millisecond, "the approval under an error", func(a Attention) bool { return screenAsks(a) && a.ID == att.ID })
		p.outW.Write([]byte(approvalGone("Error: stream disconnected\r\n")))
		if a := waitAttention(t, s, errorWaits); a.ID == "" || a.ID == att.ID {
			t.Fatalf("the error's wait after the approval: %+v", a)
		}
		lastEntryIs(t, s, statusErrorWords)
	})
}

// A form's wait (blocked with a kind) whose turn then fails gives way to the
// error's; a failed turn whose program then asks to sign in gives way to
// that question.
func TestAnErrorAndABlockedStatusTakeEachOthersPlace(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""), codexStatus(StatusBlocked, "permission"))
	form := waitAttention(t, s, statusWaits)
	out(p, codexStatus(StatusError, ""))
	if a := waitAttention(t, s, errorWaits); a.ID == form.ID {
		t.Fatalf("the error kept the form's id: %+v", a)
	}
	lastEntryIs(t, s, statusErrorWords)
	out(p, codexStatus(StatusBlocked, "auth"))
	if a := waitAttention(t, s, func(a Attention) bool { return statusWaits(a) && !errorWaits(a) }); a.Message != "The agent needs you to sign in" {
		t.Fatalf("the sign-in after the error: %+v", a)
	}
	lastEntryIs(t, s, "The agent needs you to sign in")
}

// A later turn reported done withdraws the error's wait (the agent's
// report, and the program's own done status), and so does the session's
// end.
func TestADoneAndTheEndWithdrawTheErrorsWait(t *testing.T) {
	const settle = 200 * time.Millisecond
	t.Run("the agent's done", func(t *testing.T) {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexTurnBegins, codexStatus(StatusWorking, ""), codexStatus(StatusError, ""))
		waitAttention(t, s, errorWaits)
		reportState(t, s, AttentionDone, "finished")
		waitAttention(t, s, func(a Attention) bool { return a.State == AttentionDone })
	})
	t.Run("the program's done", func(t *testing.T) {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexTurnBegins, codexStatus(StatusWorking, ""), codexStatus(StatusError, ""))
		waitAttention(t, s, errorWaits)
		out(p, codexStatus(StatusDone, ""))
		if a := waitAttention(t, s, none); a.Source != SourceScreen {
			t.Fatalf("withdrawn with %+v", a)
		}
	})
	t.Run("the end", func(t *testing.T) {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexTurnBegins, codexStatus(StatusWorking, ""), codexStatus(StatusError, ""))
		waitAttention(t, s, errorWaits)
		p.exit()
		select {
		case <-s.Ended():
		case <-time.After(10 * time.Second):
			t.Fatal("the session did not end")
		}
		if a := s.Info().Attention; a.State != AttentionNone {
			t.Fatalf("an ended session waits: %+v", a)
		}
	})
}

// An error read while a question held on the screen shows, and the question
// then answered through Conductor (a row's choice): the failed turn's wait
// shows at the answer, with its entry, and the dialog leaving the screen
// after it changes nothing.
func TestAnErrorReadUnderADialogShowsOnceTheDialogIsAnswered(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	go func() {
		for range p.input {
		}
	}()
	out(p, codexTurnBegins, codexStatus(StatusWorking, ""))
	p.outW.Write([]byte(approvalDialog("ls -la")))
	att := waitAttention(t, s, screenAsks)
	out(p, codexStatus(StatusError, ""))
	stays(t, s, 100*time.Millisecond, "the approval under the error", func(a Attention) bool { return screenAsks(a) && a.ID == att.ID })
	sub, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false))
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte("y")); err != nil {
		t.Fatal(err)
	}
	a := waitAttention(t, s, errorWaits)
	if a.ID == "" || a.ID == att.ID || a.Since == nil {
		t.Fatalf("the error's wait at the answer: %+v", a)
	}
	lastEntryIs(t, s, statusErrorWords)
	n := len(entries(s))
	p.outW.Write([]byte(approvalGone("Error: stream disconnected\r\n")))
	stays(t, s, settle+100*time.Millisecond, "the error's wait once the dialog left", func(b Attention) bool { return errorWaits(b) && b.ID == a.ID })
	if got := entries(s); len(got) != n {
		t.Fatalf("the dialog leaving recorded %q", got[n:])
	}
}
