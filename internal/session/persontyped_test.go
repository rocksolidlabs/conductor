package session

import (
	"regexp"
	"testing"
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
