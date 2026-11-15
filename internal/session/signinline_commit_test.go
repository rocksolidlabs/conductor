package session

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The judgements a line to the sign-in screen gets before its text and its
// Enter (lineReady: the output all fed and quiet, then one reading of the
// screen) and again as the writer takes each (lineGuard), and what keeps
// the line off a screen half drawn (#902, review round 1).

// signedIn waits, up to 30 s, for the session to show the sign-in screen
// as the question that takes a line.
func signedIn(t *testing.T, s *Local) Attention {
	t.Helper()
	return attentionWithin(t, s, 30*time.Second, func(a Attention) bool { return a.Source == SourceTrust && a.Line })
}

// attentionWithin is waitAttention with a bound of its own.
func attentionWithin(t *testing.T, s *Local, d time.Duration, ok func(Attention) bool) Attention {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		att := s.Info().Attention
		if ok(att) {
			return att
		}
		if time.Now().After(deadline) {
			t.Fatalf("attention never matched, last %+v", att)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// handed is every byte written to p so far, in order: what the program
// has been handed, read byte by byte.
func handed(p *fakeProc) string {
	var b strings.Builder
	for {
		select {
		case w := <-p.input:
			b.Write(w)
		default:
			return b.String()
		}
	}
}

// holdAtGuard holds the writer as it takes the line's text (enter false)
// or its Enter (enter true), before its judgement (atLineGuard): held is
// closed once it waits there, and closing release lets it go on.
func holdAtGuard(s *Local, enter bool) (held, release chan struct{}) {
	held, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func(data []byte) {
		if (string(data) == "\r") != enter {
			return
		}
		once.Do(func() {
			close(held)
			<-release
		})
	}
	s.atLineGuard.Store(&hook)
	return held, release
}

// fakeLineClock makes the session's clock for lineQuiet a test's, stopped
// at a moment of its own until advance moves it.
func fakeLineClock(s *Local) (advance func(time.Duration)) {
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()) }
	s.lineClock.Store(&clock)
	return func(d time.Duration) { now.Add(int64(d)) }
}

// waitsForLine starts a line to q from by and returns once it waits in
// lineReady (inLineWait), and the channel its end comes on; the test
// fails when it ends without waiting.
func waitsForLine(t *testing.T, s *Local, p *fakeProc, by *Subscription, q Attention, text string) <-chan error {
	t.Helper()
	waiting := make(chan struct{})
	var once sync.Once
	hook := func() { once.Do(func() { close(waiting) }) }
	s.inLineWait.Store(&hook)
	done := make(chan error, 1)
	go func() {
		res, err := s.Submit(t.Context(), Submission{Text: text, By: by, Question: q.ID})
		if err == nil && !res.Entered {
			err = errors.New("not entered")
		}
		done <- err
	}()
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("the line did not wait: %v (written %q)", err, handed(p))
	}
	return done
}

// caughtUp waits, up to 30 s, until the pump has fed every read since
// fed, and the screen at least one.
func caughtUp(t *testing.T, s *Local, fed uint64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for s.asked.Fed() <= fed || s.outFed.Load() != s.outRead.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the output was never fed")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Output the program wrote that waits unread in its terminal: the line
// waits, nothing written, until the pump has read and fed it and the
// screen has been quiet since, on the session's clock; then it is typed.
func TestALineWaitsForTheOutputNotReadYet(t *testing.T) {
	s, p, nater, q := lineSession(t)
	advance := fakeLineClock(s)
	advance(lineQuiet)
	p.unread.Store(64)
	done := waitsForLine(t, s, p, nater, q, signInCode)
	if got := handed(p); got != "" {
		t.Fatalf("written with output unread: %q", got)
	}
	fed := s.asked.Fed()
	p.outW.Write([]byte(claudeSignInScreen))
	p.unread.Store(0)
	caughtUp(t, s, fed)
	if got := handed(p); got != "" {
		t.Fatalf("written before the screen was quiet: %q", got)
	}
	advance(lineQuiet)
	if err := <-done; err != nil {
		t.Fatalf("the line: %v", err)
	}
	if got := handed(p); got != signInCode+"\r" {
		t.Fatalf("written: %q", got)
	}
}

// The folder's trust question fed while the line waits for the screen to
// be quiet: the line is judged on it once it is, and refused, nothing
// written.
func TestATrustScreenFedInTheQuietIntervalRefusesTheLine(t *testing.T) {
	s, p, nater, q := lineSession(t)
	advance := fakeLineClock(s)
	fed := s.asked.Fed()
	// The sign-in screen drawn again at the session's now: not quiet yet.
	p.outW.Write([]byte(claudeSignInScreen))
	caughtUp(t, s, fed)
	done := waitsForLine(t, s, p, nater, q, signInCode)
	fed = s.asked.Fed()
	p.outW.Write([]byte(claudeTrustScreen))
	caughtUp(t, s, fed)
	advance(lineQuiet)
	if err := <-done; !errors.Is(err, ErrTrustQuestion) || err.Error() != TrustQuestionWords {
		t.Fatalf("the line: %v", err)
	}
	if got := handed(p); got != "" {
		t.Fatalf("written: %q", got)
	}
}

// The folder's trust question drawn after the line's text was judged and
// typed, before its Enter: the text arrives, the Enter is withheld, and
// the answer says the screen changed.
func TestATrustScreenAfterTheTextWithholdsTheEnter(t *testing.T) {
	s, p, nater, q := lineSession(t)
	hook := func() {
		fed := s.asked.Fed()
		p.outW.Write([]byte(claudeTrustScreen))
		caughtUp(t, s, fed)
	}
	s.beforeEnter.Store(&hook)
	_, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: q.ID})
	if !errors.Is(err, ErrLineMoved) || err.Error() != LineMovedWords {
		t.Fatalf("the line: %v", err)
	}
	if got := handed(p); got != signInCode {
		t.Fatalf("written: %q", got)
	}
}

// A line that holds a control character (ESC, a line break, the sequence
// that ends a paste) is refused as invalid: a sign-in code is printable
// text. Nothing is written.
func TestALineWithAControlCharacterIsRefused(t *testing.T) {
	s, p, nater, q := lineSession(t)
	for _, text := range []string{"abc\x1b[201~1", "abc\ndef", "abc\tdef", "abc\u009bdef"} {
		_, err := s.Submit(t.Context(), Submission{Text: text, By: nater, Question: q.ID})
		if !errors.Is(err, ErrBadLine) || !errors.Is(err, ErrBadAnswer) {
			t.Fatalf("%q: %v", text, err)
		}
		if f := AnswerResult("r1", err); !strings.Contains(string(f), "bad_frame") || !strings.Contains(string(f), "printable") {
			t.Fatalf("%q: the answer's word %s", text, f)
		}
	}
	if got := handed(p); got != "" {
		t.Fatalf("written: %q", got)
	}
}

// fedUntil waits, up to 30 s, until the screen the line is judged on has
// been fed output since fed and its reading passes ok.
func fedUntil(t *testing.T, s *Local, fed uint64, ok func(text string, whole bool) bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		text, whole, _ := s.asked.Complete()
		if s.asked.Fed() > fed && ok(text, whole) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the screen never changed: %q", text)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// screenChanges are what the program may draw while a line waits at the
// writer, and what tells that the screen took it.
var screenChanges = []struct {
	name, output string
	shown        func(text string, whole bool) bool
}{
	{"the folder's trust question drawn above the sign-in", "\x1b[2J\x1b[H Is this a project you created or one you trust?\r\n\r\n Browser didn't open? Use the url below to sign in (c to copy)\r\n\r\n Paste code here if prompted > ",
		func(text string, _ bool) bool { return strings.Contains(text, "one you trust") }},
	{"a numbered row added", "\x1b[2J\x1b[H ❯ 1. Claude account with subscription\r\n\r\n Browser didn't open? Use the url below to sign in (c to copy)\r\n\r\n Paste code here if prompted > ",
		func(text string, _ bool) bool { return strings.Contains(text, "1. Claude account") }},
	{"an update dialog", "\x1b[2J\x1b[H" + codexUpdateScreen,
		func(text string, whole bool) bool { return whole && strings.Contains(text, "Update available") }},
	{"the ordinary prompt", "\x1b[2J\x1b[H Login successful.\r\n\r\n> \r\n  ? for shortcuts",
		func(text string, _ bool) bool { return strings.Contains(text, "for shortcuts") }},
	{"a redraw begun and cut before its final byte", "\x1b[?2026",
		func(string, bool) bool { return true }},
}

// lineSession is a session of Claude Code's startup questions, Codex's
// update dialog among them, at its sign-in screen, with a controller.
func lineSession(t *testing.T) (*Local, *fakeProc, *Subscription, Attention) {
	t.Helper()
	opts := claudeTrustOptions(t)
	opts.Questions = append(opts.Questions, builtInStartupQuestions(t, "codex")...)
	s, p := newLocalWith(t, quiet(opts))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	p.outW.Write([]byte(claudeSignInScreen))
	return s, p, nater, signedIn(t, s)
}

// The screen changed while the line's text waits at the writer, after
// every look made before it: the writer's judgement refuses the text in
// the words a line gets at any other startup question, and nothing of it
// is written.
func TestALinesTextIsJudgedAsTheWriterTakesIt(t *testing.T) {
	for _, c := range screenChanges {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, p, nater, q := lineSession(t)
			held, release := holdAtGuard(s, false)
			done := make(chan error, 1)
			go func() {
				_, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: q.ID})
				done <- err
			}()
			<-held
			fed := s.asked.Fed()
			p.outW.Write([]byte(c.output))
			fedUntil(t, s, fed, c.shown)
			close(release)
			if err := <-done; !errors.Is(err, ErrTrustQuestion) || err.Error() != TrustQuestionWords {
				t.Fatalf("the line: %v", err)
			}
			if got := handed(p); got != "" {
				t.Fatalf("written: %q", got)
			}
		})
	}
}

// The screen changed while the line's Enter waits at the writer, its text
// typed: the writer's judgement leaves the Enter out, and the answer says
// the screen changed and the line was typed without its Enter.
func TestALinesEnterIsJudgedAsTheWriterTakesIt(t *testing.T) {
	for _, c := range screenChanges {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, p, nater, q := lineSession(t)
			held, release := holdAtGuard(s, true)
			done := make(chan error, 1)
			go func() {
				_, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: q.ID})
				done <- err
			}()
			<-held
			if got := handed(p); got != signInCode {
				t.Fatalf("typed before the Enter: %q", got)
			}
			fed := s.asked.Fed()
			p.outW.Write([]byte(c.output))
			fedUntil(t, s, fed, c.shown)
			close(release)
			err := <-done
			if !errors.Is(err, ErrLineMoved) || !errors.Is(err, ErrLineStranded) || err.Error() != LineMovedWords {
				t.Fatalf("the line: %v", err)
			}
			if got := handed(p); got != "" {
				t.Fatalf("written after its text: %q", got)
			}
			if f := AnswerResult("r1", err); !strings.Contains(string(f), "the screen changed before its Enter") {
				t.Fatalf("the answer's word: %s", f)
			}
		})
	}
}

// A redraw to the trust question cut at any byte of its frame's opener,
// of its body or of its closer: the line waits, nothing written, until the
// frame is whole, and is judged on the new screen then (refused).
func TestALineWaitsForARedrawCutAtAnyByte(t *testing.T) {
	const opener, closer = "\x1b[?2026h", "\x1b[?2026l"
	redraw := opener + claudeTrustScreen + closer
	var cuts []int
	for k := 1; k <= len(opener); k++ {
		cuts = append(cuts, k)
	}
	cuts = append(cuts, len(opener)+3, len(opener)+len(claudeTrustScreen)/2)
	for k := len(redraw) - len(closer); k < len(redraw); k++ {
		cuts = append(cuts, k)
	}
	for _, k := range cuts {
		t.Run("cut at byte "+strconv.Itoa(k), func(t *testing.T) {
			t.Parallel()
			s, p, nater, q := lineSession(t)
			advance := fakeLineClock(s)
			fed := s.asked.Fed()
			p.outW.Write([]byte(redraw[:k]))
			caughtUp(t, s, fed)
			// Quiet since: what holds the line is the frame cut alone.
			advance(lineQuiet)
			done := waitsForLine(t, s, p, nater, q, signInCode)
			if got := handed(p); got != "" {
				t.Fatalf("written while the frame is cut: %q", got)
			}
			fed = s.asked.Fed()
			p.outW.Write([]byte(redraw[k:]))
			caughtUp(t, s, fed)
			advance(lineQuiet)
			if err := <-done; !errors.Is(err, ErrTrustQuestion) {
				t.Fatalf("the line on the trust question: %v", err)
			}
			if got := handed(p); got != "" {
				t.Fatalf("written: %q", got)
			}
		})
	}
}

// A code the program refuses has its sign-in screen drawn again: asked
// again, with an id of its own, to take another line, whether the program
// draws it before the line's answer has resolved the question (a look at
// it then finds the question claimed) or after.
func TestARefusedCodeIsAskedAgainWhicheverComesFirst(t *testing.T) {
	refused := "\x1b[2J\x1b[H That code was not accepted.\r\n" + strings.TrimPrefix(claudeSignInScreen, "\x1b[2J\x1b[H")
	for _, before := range []bool{true, false} {
		name := "drawn after the answer"
		if before {
			name = "drawn before the answer resolved it"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, p, nater, q := lineSession(t)
			if before {
				hook := func() {
					looked := make(chan struct{})
					var once sync.Once
					asked := func() { once.Do(func() { close(looked) }) }
					s.afterAsked.Store(&asked)
					p.outW.Write([]byte(refused))
					select {
					case <-looked:
					case <-time.After(30 * time.Second):
						t.Error("the screen drawn again was never looked at")
					}
				}
				s.beforeLineResolved.Store(&hook)
			}
			if res, err := s.Submit(t.Context(), Submission{Text: "wrong-code-1234", By: nater, Question: q.ID}); err != nil || !res.Entered {
				t.Fatalf("the line: %+v %v", res, err)
			}
			if got := handed(p); got != "wrong-code-1234\r" {
				t.Fatalf("written: %q", got)
			}
			if !before {
				p.outW.Write([]byte(refused))
			}
			again := attentionWithin(t, s, 30*time.Second, func(a Attention) bool { return a.Source == SourceTrust && a.ID != q.ID })
			if !again.Line || !strings.Contains(again.Message, "Browser didn't open") {
				t.Fatalf("asked again: %+v", again)
			}
		})
	}
}

// A session's last line, which a Brakeman request's record copies when
// its session ends, is never read off a screen that shows a startup
// question: there, what the program shows as a code is typed into its
// field would be it. A line the program prints after it is.
func TestTheLastLineIsNeverReadOffAStartupQuestion(t *testing.T) {
	s, p, _, _ := lineSession(t)
	// The code drawn in a field of its own, below the question's words.
	fed := s.asked.Fed()
	p.outW.Write([]byte("\r\n  " + signInCode))
	fedUntil(t, s, fed, func(text string, _ bool) bool { return strings.Contains(text, signInCode) })
	if l := s.LastLine(); l != "" {
		t.Fatalf("the last line at the sign-in screen: %q", l)
	}
	// The screen erased and a line drawn where the question's was: the
	// output kept reads it on that line, which is never copied.
	fed = s.asked.Fed()
	p.outW.Write([]byte("\x1b[2J\x1b[H Paste code here if prompted > " + signInCode + "\x1b[2J\x1b[HThat code was not accepted."))
	fedUntil(t, s, fed, func(text string, _ bool) bool { return !strings.Contains(text, signInCode) })
	if l := s.LastLine(); l != "" {
		t.Fatalf("the last line read with the field's: %q", l)
	}
	p.outW.Write([]byte("\r\nPlease run /login\r\n"))
	deadline := time.Now().Add(30 * time.Second)
	for s.LastLine() != "Please run /login" {
		if time.Now().After(deadline) {
			t.Fatalf("the last line: %q", s.LastLine())
		}
		time.Sleep(2 * time.Millisecond)
	}
}
