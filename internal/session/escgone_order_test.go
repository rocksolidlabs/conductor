package session

import (
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
)

// A startup question an Escape typed in the terminal answered goes once a
// whole frame of the screen shows no startup question, whichever of the two
// watchers that look after the same quiet period looks first: the startup
// questions' watcher, which reads the output it keeps (fireTrust), or the
// screen's (missAsked). Each order is forced here: the other watcher's own
// look is held at its gate (afterLook) while the first looks, then let go
// (looksAfter).

// escSession is a session with Codex's update question and the trust
// question, a controlling viewer at 80 by 24, and write, which returns once
// the session's pump has fed what it writes to the screens the questions
// are read from.
func escSession(t *testing.T) (s *Local, p *fakeProc, sub *Subscription, write func(string)) {
	t.Helper()
	s, p = newLocalWith(t, quiet(Options{
		TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`),
		TrustAnswers: []Option{{Label: "Yes, continue", Input: "1"}},
		Questions:    []StartupQuestion{codexUpdateQuestion()},
	}))
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

// escAnswered asks Codex's update question whole at 80 by 24, then in a
// window five rows tall, where only its choices are drawn and the session
// asks it again in its first choice's words; then a person types Escape in
// the terminal, which is held as the question's answer until the screen
// shows it gone. It returns that question.
func escAnswered(t *testing.T, s *Local, p *fakeProc, sub *Subscription, write func(string)) Attention {
	t.Helper()
	write("\x1b[?1049h" + codexFrame(codexUpdateView))
	first := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if err := s.Resize(sub, 80, 5); err != nil {
		t.Fatal(err)
	}
	write(codexFrame(codexUpdateView[4:]))
	cut := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust && a.ID != first.ID })
	if err := s.Input(sub, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "\x1b" {
		t.Fatalf("written %q", got)
	}
	s.mu.Lock()
	held := s.heldAnswer.id
	s.mu.Unlock()
	if held != cut.ID {
		t.Fatalf("the Escape is not held as the question's answer: held %q, question %+v", held, cut)
	}
	return cut
}

// codexChoicesOnly is Codex's update dialog drawn again as a whole frame
// in the short window, its highlight moved and its first choice's row off
// the screen: its later choices and its footer show.
var codexChoicesOnly = codexFrame([]string{"› 2. Skip", "  3. Skip until next version", "", "  enter continue · esc skip"})

// codexPrompt is Codex's composer, the screen it draws once its update
// question is skipped, in the short window.
var codexPrompt = codexFrame([]string{"", "  >_ OpenAI Codex (v0.163.0)", "", "› Ask Codex to do anything", ""})

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

// The update dialog skipped with Escape and the composer drawn in its
// place: the question goes, and no question is asked from the output the
// startup watcher keeps (the dialog's heading still in it), in either
// order of the two looks.
func TestAStartupQuestionAnEscapeAnsweredGoesInEitherOrderOfTheLooks(t *testing.T) {
	for _, trustFirst := range []bool{true, false} {
		t.Run(orderName(trustFirst), func(t *testing.T) {
			s, p, sub, write := escSession(t)
			cut := escAnswered(t, s, p, sub, write)
			looksAfter(t, s, trustFirst, func() { write(codexPrompt) })
			if a := s.Info().Attention; a.State == AttentionNeedsInput {
				t.Fatalf("the composer drawn, the session shows %+v (the question Escape answered: %+v)", a, cut)
			}
			// Looked at again by both: nothing comes back.
			s.trust.CheckNow()
			if a := s.Info().Attention; a.State == AttentionNeedsInput {
				t.Fatalf("looked at again, the session shows %+v", a)
			}
		})
	}
}

// The question an Escape answered is not withdrawn while its dialog is
// still on the screen, in either order: the agent has not drawn anything
// since the key, or it took the Escape for nothing and drew the dialog
// again. It still shows, and a line submitted then is refused, nothing
// written.
func TestAStartupQuestionAnEscapeAnsweredStaysWhileItsDialogShows(t *testing.T) {
	for _, tc := range []struct {
		name string
		// after is what the agent draws after the Escape: "" for nothing.
		after string
	}{
		{"nothing drawn since the key", ""},
		{"the dialog drawn again", codexFrame(codexUpdateView[4:])},
	} {
		for _, trustFirst := range []bool{true, false} {
			t.Run(tc.name+", "+orderName(trustFirst), func(t *testing.T) {
				s, p, sub, write := escSession(t)
				cut := escAnswered(t, s, p, sub, write)
				looksAfter(t, s, trustFirst, func() {
					if tc.after != "" {
						write(tc.after)
					} else {
						// No output: the screen's look is made all the same.
						s.asked.Poke()
					}
				})
				if a := s.Info().Attention; a.ID != cut.ID || a.State != AttentionNeedsInput || a.Source != SourceTrust {
					t.Fatalf("the dialog on the screen, the session shows %+v, was %+v", a, cut)
				}
				if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: sub}); !errors.Is(err, ErrTrustQuestion) {
					t.Fatalf("a line submitted at the dialog: %v", err)
				}
				select {
				case b := <-p.input:
					t.Fatalf("written %q", b)
				default:
				}
			})
		}
	}
}

// The question an Escape answered is not withdrawn while its dialog's
// choices show, though the row its dialog is found by does not (the dialog
// drawn again with its first choice off the screen), in either order: a
// startup question still shows (the startup watcher, looking first, may
// ask the dialog's heading anew from the output it keeps), and a line
// submitted then, a digit first, is refused, nothing written.
func TestAStartupQuestionAnEscapeAnsweredStaysWhileItsChoicesShow(t *testing.T) {
	for _, trustFirst := range []bool{true, false} {
		t.Run(orderName(trustFirst), func(t *testing.T) {
			s, p, sub, write := escSession(t)
			escAnswered(t, s, p, sub, write)
			looksAfter(t, s, trustFirst, func() { write(codexChoicesOnly) })
			if a := s.Info().Attention; a.State != AttentionNeedsInput || a.Source != SourceTrust {
				t.Fatalf("the dialog's choices on the screen, the session shows %+v", a)
			}
			if _, err := s.Submit(t.Context(), Submission{Text: "1 fix the bug", By: sub}); !errors.Is(err, ErrTrustQuestion) {
				t.Fatalf("a line submitted at the dialog: %v", err)
			}
			select {
			case b := <-p.input:
				t.Fatalf("written %q", b)
			default:
			}
		})
	}
}

// Another startup question drawn right after the Escape (the folder's
// trust question in the update dialog's place) is asked, in either order.
func TestTheNextStartupQuestionAfterAnEscapeIsAskedInEitherOrder(t *testing.T) {
	for _, trustFirst := range []bool{true, false} {
		t.Run(orderName(trustFirst), func(t *testing.T) {
			s, p, sub, write := escSession(t)
			escAnswered(t, s, p, sub, write)
			looksAfter(t, s, trustFirst, func() {
				write(codexFrame([]string{"", "  Trust this folder?", "› 1. Yes, continue", "  2. No, quit", ""}))
			})
			if a := s.Info().Attention; a.State != AttentionNeedsInput || a.Source != SourceTrust || a.Message != "Trust this folder?" {
				t.Fatalf("the trust question drawn, the session shows %+v", a)
			}
		})
	}
}

// A frame the agent has begun and not finished (a synchronized update
// still open, its composer half drawn) is no whole frame: the screen's look
// at it withdraws nothing, and the question an Escape answered stands until
// the frame is finished; then it goes.
func TestAStartupQuestionAnEscapeAnsweredStandsThroughHalfAFrame(t *testing.T) {
	s, p, sub, write := escSession(t)
	cut := escAnswered(t, s, p, sub, write)
	releaseTrust := gate(t, s.trust)
	frame := codexPrompt
	half := len(frame) - len("\x1b[?2026l")
	write(frame[:half])
	screenLooked(t, s)
	if a := s.Info().Attention; a.ID != cut.ID || a.State != AttentionNeedsInput {
		t.Fatalf("half a frame drawn, the session shows %+v, was %+v", a, cut)
	}
	write(frame[half:])
	screenLooked(t, s)
	releaseTrust()
	s.trust.CheckNow()
	if a := s.Info().Attention; a.State == AttentionNeedsInput {
		t.Fatalf("the frame finished, the session shows %+v", a)
	}
}

func orderName(trustFirst bool) string {
	if trustFirst {
		return "the startup watcher's look first"
	}
	return "the screen's look first"
}
