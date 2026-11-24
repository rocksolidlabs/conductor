package session

import (
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
)

// isClosed says whether c is closed, without waiting.
func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// PersonTyped closes at a person's keys at the program's text: a viewer's
// keys at its prompt, and a person's line from a reply box naming no
// question.
func TestPersonTypedClosesAtAPersonsKeys(t *testing.T) {
	t.Run("a viewer's keys", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{}))
		nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		p.outW.Write([]byte("> "))
		if isClosed(s.PersonTyped()) {
			t.Fatal("closed before anyone typed")
		}
		if err := s.Input(nater, []byte("l")); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
		if !isClosed(s.PersonTyped()) {
			t.Fatal("a viewer's key at the prompt left it open")
		}
	})
	t.Run("a person's line", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{}))
		nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		p.outW.Write([]byte("> "))
		if _, err := s.Submit(t.Context(), Submission{Text: "fix the parser", By: nater}); err != nil {
			t.Fatal(err)
		}
		if !isClosed(s.PersonTyped()) {
			t.Fatal("a person's line left it open")
		}
	})
}

// PersonTyped stays open for what is no person's keys at the program's
// text: Down and Enter to a shown trust question, keys typed at the
// sign-in screen, a terminal's cursor report, a reply to a question, and
// Conductor's own line. Without it a person who answers the trust
// question would lose the launch's first message.
func TestPersonTypedStaysOpenForWhatIsNoPersonsText(t *testing.T) {
	t.Run("the trust question's keys", func(t *testing.T) {
		trust := []Option{{Label: "Yes, trust this folder", Input: "\r"}, {Label: "No, quit", Input: "\x1b[B\r"}}
		s, p := newLocalWith(t, quiet(Options{TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), TrustAnswers: trust}))
		nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		p.outW.Write([]byte("Trust this folder?\r\n› 1. Yes, continue\r\n  2. No, quit\r\n"))
		waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
		for _, k := range []string{"\x1b[B", "\x1b[A", "\r"} {
			if err := s.Input(nater, []byte(k)); err != nil {
				t.Fatal(err)
			}
			nextWrite(t, p)
			if isClosed(s.PersonTyped()) {
				t.Fatalf("%q to the trust question closed it", k)
			}
		}
	})
	t.Run("keys at the sign-in screen", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{Questions: claudeStartupQuestions(t)}))
		nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		p.outW.Write([]byte(claudeSignInScreen))
		waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
		if err := s.Input(nater, []byte("c")); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
		if isClosed(s.PersonTyped()) {
			t.Fatal("a key at the sign-in screen closed it")
		}
	})
	t.Run("a cursor report", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{}))
		nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		p.outW.Write([]byte("> "))
		if err := s.Input(nater, []byte("\x1b[12;5R")); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
		if isClosed(s.PersonTyped()) {
			t.Fatal("the terminal's report closed it")
		}
	})
	t.Run("a reply to a question", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{}))
		nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Allow Bash?", SourceAPI, KindPermission, []Option{{Label: "Yes", Input: "1"}}); err != nil {
			t.Fatal(err)
		}
		q := s.Info().Attention
		if err := s.AnswerInput(t.Context(), nater, q.ID, []byte("1")); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
		if isClosed(s.PersonTyped()) {
			t.Fatal("a reply closed it")
		}
	})
	t.Run("Conductor's own line", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{}))
		p.outW.Write([]byte("> "))
		if res, err := s.Submit(t.Context(), Submission{Text: "fix the parser", ByName: "launch"}); err != nil || !res.Entered {
			t.Fatalf("%+v %v", res, err)
		}
		if isClosed(s.PersonTyped()) {
			t.Fatal("Conductor's own line closed it")
		}
	})
}

// The launch's first message waits in Info as "waiting", told as a change,
// and goes with SetFirstMessage(""): never its text.
func TestTheFirstMessageWaitsInInfo(t *testing.T) {
	changes := make(chan Info, 4)
	s, _ := newLocalWith(t, quiet(Options{OnChange: func(i Info) { changes <- i }}))
	s.SetFirstMessage("waiting")
	if got := <-changes; got.FirstMessage != "waiting" {
		t.Fatalf("told %+v", got)
	}
	s.SetFirstMessage("")
	if got := <-changes; got.FirstMessage != "" || s.Info().FirstMessage != "" {
		t.Fatalf("told %+v", got)
	}
}

// A line whose Enter a person's keys withdraw (WithdrawnByKeys: a launch's
// first message) leaves its Enter out and holds nothing: the person typed
// in the pause before the Enter, the Enter is never written, the session
// shows no hold, and the agent's next report shows. A line withdrawn by
// anything else (a sender let go) keeps its hold and its words. The pause
// is longer than the test: only the withdrawal ends it.
//
// A sender's withdrawal that comes while the program is still taking the
// text (its write begun, not done) answers Submit at once, and the line is
// finished as the text lands, the session's turn kept meanwhile: its hold
// is set then, and shown (HeldWords). So the hold is waited for by what it
// shows, never read as Submit returns.
func TestAPersonsKeysBeforeTheEnterHoldNothing(t *testing.T) {
	t.Run("withdrawn by a person's keys", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{SubmitPause: time.Hour}))
		nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		p.outW.Write([]byte("> "))
		done := make(chan error, 1)
		go func() {
			res, err := s.Submit(t.Context(), Submission{Text: "fix the parser", ByName: "launch", Withdrawn: s.PersonTyped(), WithdrawnByKeys: true})
			if res.Entered || !res.Typed {
				err = errors.Join(err, errors.New("entered, or not typed"))
			}
			done <- err
		}()
		if w := nextWrite(t, p); w != "fix the parser" {
			t.Fatalf("written %q", w)
		}
		if err := s.Input(nater, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if w := nextWrite(t, p); w != "x" {
			t.Fatalf("written %q", w)
		}
		if err := <-done; !errors.Is(err, ErrRevoked) {
			t.Fatalf("the line ended with %v", err)
		}
		// A hold is set before the submission returns: none is.
		if s.TurnHeld() {
			t.Fatal("the session's turn is held")
		}
		noWrite(t, p, 50*time.Millisecond)
		if a := s.Info().Attention; a.State == AttentionNeedsInput {
			t.Fatalf("the session shows %+v", a)
		}
		if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		if a := s.Info().Attention; a.State != AttentionWorking {
			t.Fatalf("the agent's working shows %+v", a)
		}
	})
	// Its sender withdraws the line: once after its text is written, once
	// while the program is still taking it (the fake program's write of the
	// text held until Submit has answered and the test has looked, so the
	// line is finished after Submit returns every run).
	for _, mid := range []bool{false, true} {
		name := "withdrawn by its sender"
		if mid {
			name += " while the program takes the text"
		}
		t.Run(name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{SubmitPause: time.Hour}))
			p.outW.Write([]byte("> "))
			gone := make(chan struct{})
			answered := make(chan struct{})
			if mid {
				var once sync.Once
				p.onBytes = func(b []byte) {
					if string(b) == "fix the parser" {
						once.Do(func() {
							close(gone)
							<-answered
						})
					}
				}
			}
			done := make(chan error, 1)
			go func() {
				_, err := s.Submit(t.Context(), Submission{Text: "fix the parser", ByName: "relay", Withdrawn: gone})
				done <- err
			}()
			if mid {
				if err := <-done; !errors.Is(err, ErrRevoked) {
					t.Fatalf("withdrawn as the program takes the text, Submit answered %v", err)
				}
				// The program takes the text now that Submit has answered.
				close(answered)
			}
			if w := nextWrite(t, p); w != "fix the parser" {
				t.Fatalf("written %q", w)
			}
			if !mid {
				close(gone)
				if err := <-done; !errors.Is(err, ErrRevoked) {
					t.Fatalf("the line ended with %v", err)
				}
			}
			waitAttention(t, s, func(a Attention) bool {
				return a.State == AttentionNeedsInput && a.Source == SourceSubmit && a.Message == HeldWords
			})
			if !s.TurnHeld() {
				t.Fatal("the session's turn is not held")
			}
		})
	}
}
