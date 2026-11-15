package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// signInCode is an invented code, the line a controller sends to the
// sign-in screen: no test signs in to anything.
const signInCode = "Zq7Xw2Kp9Rt4Vb6NmL3c"

// lockedBuffer is a log's output, read by the test while the session
// writes it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// holdsPartOf fails the test when text holds any 8 characters in a row of
// code, saying where (what).
func holdsPartOf(t *testing.T, what, text, code string) {
	t.Helper()
	for i := 0; i+8 <= len(code); i++ {
		if strings.Contains(text, code[i:i+8]) {
			t.Errorf("%s holds %q of the line", what, code[i:i+8])
			return
		}
	}
}

// claudeTrustOptions is the session Options of Claude Code's built-in
// entry: its folder's trust question with its answers, then its startup
// questions, the sign-in screen marked as one that takes a line.
func claudeTrustOptions(t *testing.T) Options {
	t.Helper()
	claude, _ := catalog.Default().Get("claude")
	answers := make([]Option, len(claude.TrustAnswers))
	for i, a := range claude.TrustAnswers {
		answers[i] = Option{Label: a.Label, Input: a.Input}
	}
	return Options{TrustPattern: regexp.MustCompile(claude.TrustPrompt), TrustAnswers: answers, Questions: claudeStartupQuestions(t)}
}

// claudeTrustScreen is Claude Code's folder trust question, its answers
// numbered, the screen erased first.
const claudeTrustScreen = "\x1b[2J\x1b[H Do you trust the files in this folder?\r\n\r\n Is this a project you created or one you trust?\r\n\r\n ❯ 1. Yes, I trust this folder\r\n   2. No, exit\r\n\r\n Enter to confirm · Esc to cancel"

// A controller's line naming Claude Code's sign-in screen, the startup
// question its catalog entry marks as taking one, is typed into it and its
// Enter written; the question is resolved by the controller. The line is
// kept nowhere: not in the chat, the activity log, the session's
// description, the frames another viewer is sent or any log line.
func TestALineToTheSignInScreenIsTypedWithItsEnterAndKeptNowhere(t *testing.T) {
	logs := &lockedBuffer{}
	var hooked lockedBuffer
	opts := quiet(Options{Questions: claudeStartupQuestions(t)})
	opts.Log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	opts.OnActivity = func(_ string, e ActivityEntry, _ AttentionState) {
		b, _ := json.Marshal(e)
		hooked.Write(b)
	}
	opts.OnChat = func(_ string, m ChatMessage) {
		b, _ := json.Marshal(m)
		hooked.Write(b)
	}
	s, p := newLocalWith(t, opts)
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	other := newChanSink(false)
	if _, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "jane", Cols: 80, Rows: 24}, other); err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(claudeSignInScreen))
	signIn := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if !signIn.Line || len(signIn.Options) != 0 {
		t.Fatalf("the sign-in screen is not a question that takes a line: %+v", signIn)
	}
	res, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: signIn.ID})
	if err != nil || !res.Entered {
		t.Fatalf("the line: %+v %v", res, err)
	}
	if got := nextWrite(t, p); got != signInCode {
		t.Fatalf("typed %q", got)
	}
	if got := nextWrite(t, p); got != "\r" {
		t.Fatalf("then %q, not its Enter", got)
	}
	info := s.Info()
	if info.LastAnswer == nil || info.LastAnswer.ByName != "nater" || info.Attention.ID == signIn.ID {
		t.Fatalf("the question is not resolved by nater: %+v %+v", info.LastAnswer, info.Attention)
	}
	if r, ok := s.QuestionResult(signIn.ID); !ok || r.ByName != "nater" {
		t.Fatalf("the question's result: %+v %v", r, ok)
	}
	// The other viewer has been sent the question's end (its attention
	// cleared) before what was sent to it is read.
	deadline := time.Now().Add(30 * time.Second)
	for !framesHold(other, `"source":"input"`) {
		if time.Now().After(deadline) {
			t.Fatal("the other viewer was never told the question went")
		}
		time.Sleep(5 * time.Millisecond)
	}
	described, _ := json.Marshal(info)
	activity, _ := json.Marshal(s.Activity())
	chat, _ := json.Marshal(s.ChatHistory())
	holdsPartOf(t, "the session's description", string(described), signInCode)
	holdsPartOf(t, "the activity log", string(activity), signInCode)
	holdsPartOf(t, "the chat", string(chat), signInCode)
	holdsPartOf(t, "what the hooks were told", hooked.String(), signInCode)
	holdsPartOf(t, "the log", logs.String(), signInCode)
	holdsPartOf(t, "the frames another viewer was sent", allFrames(other), signInCode)
	if !strings.Contains(string(chat), "nater") {
		t.Errorf("the chat does not say who answered: %s", chat)
	}
}

// framesHold reports whether a frame sink has been sent holds words.
func framesHold(sink *chanSink, words string) bool { return strings.Contains(allFrames(sink), words) }

// allFrames is every frame a sink has been sent, one after the other.
func allFrames(sink *chanSink) string {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var b strings.Builder
	for _, f := range sink.frames {
		b.Write(f)
	}
	return b.String()
}

// A second line to the sign-in question once the first has answered it is
// refused (question_answered: first to claim wins), and a view link's line
// is refused as every answer of a view link is (read_only): nothing of
// either is typed.
func TestASecondLineAndAViewLinksLineAreRefused(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: claudeStartupQuestions(t)}))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	jane, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "jane", Cols: 80, Rows: 24}, newChanSink(false))
	viewer, _ := s.AttachWith(AttachOptions{Role: RoleView, Name: "guest", Cols: 80, Rows: 24}, newChanSink(false))
	p.outW.Write([]byte(claudeSignInScreen))
	signIn := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if _, err := s.Submit(t.Context(), Submission{Text: signInCode, By: viewer, Question: signIn.ID}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a view link's line: %v", err)
	}
	noWrite(t, p, 50*time.Millisecond)
	if res, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: signIn.ID}); err != nil || !res.Entered {
		t.Fatalf("the first line: %+v %v", res, err)
	}
	nextWrite(t, p)
	nextWrite(t, p)
	if _, err := s.Submit(t.Context(), Submission{Text: "other-code-0000", By: jane, Question: signIn.ID}); !errors.Is(err, ErrQuestionAnswered) || !strings.Contains(err.Error(), "nater") {
		t.Fatalf("the second line: %v", err)
	}
	noWrite(t, p, 50*time.Millisecond)
}

// A line to a startup question that takes none is refused in the words a
// person's text gets at a startup question, nothing typed: the folder's
// trust question, Codex's update dialog (whose Enter would run its
// install), a startup screen with no answers the catalog does not mark,
// and an update dialog marked to take a line all the same (a question
// with answers never takes one). A line naming no question is refused at
// the sign-in screen as before.
func TestALineIsRefusedAtEveryOtherStartupQuestion(t *testing.T) {
	codexUpdate := builtInStartupQuestions(t, "codex")
	markedUpdate := builtInStartupQuestions(t, "codex")
	for i := range markedUpdate {
		markedUpdate[i].Line = true
	}
	for _, c := range []struct {
		name   string
		opts   Options
		screen string
		words  string
	}{
		{"the folder's trust question", claudeTrustOptions(t), claudeTrustScreen, "trust"},
		{"Codex's update dialog", Options{Questions: codexUpdate}, codexUpdateScreen, "Update available"},
		{"an update dialog marked to take a line", Options{Questions: markedUpdate}, codexUpdateScreen, "Update available"},
		{"a startup screen not marked", Options{Questions: claudeStartupQuestions(t)}, "\x1b[2J\x1b[H Select login method:\r\n\r\n Claude account with subscription\r\n Anthropic Console account", "login method"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(c.opts))
			nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
			p.outW.Write([]byte(c.screen))
			q := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
			if !strings.Contains(q.Message, c.words) || q.Line {
				t.Fatalf("the question: %+v", q)
			}
			if _, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: q.ID}); !errors.Is(err, ErrTrustQuestion) || err.Error() != TrustQuestionWords {
				t.Fatalf("a line to it: %v", err)
			}
			noWrite(t, p, 100*time.Millisecond)
			if a := s.Info().Attention; a.ID != q.ID {
				t.Fatalf("the question did not stay: %+v", a)
			}
		})
	}
	t.Run("a line naming no question at the sign-in screen", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{Questions: claudeStartupQuestions(t)}))
		nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		p.outW.Write([]byte(claudeSignInScreen))
		waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
		if _, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater}); !errors.Is(err, ErrTrustQuestion) {
			t.Fatalf("a line naming no question: %v", err)
		}
		noWrite(t, p, 100*time.Millisecond)
	})
}

// The sign-in screen drawn with another startup question on the screen
// (the folder's trust question above it, or a dialog's numbered choice)
// takes no line, though the sign-in is the question shown: the line's
// Enter could reach the other one.
func TestASignInScreenBesideAnotherStartupQuestionTakesNoLine(t *testing.T) {
	for _, c := range []struct{ name, screen string }{
		{"the trust question above it", "\x1b[2J\x1b[H Is this a project you created or one you trust?\r\n\r\n Browser didn't open? Use the url below to sign in (c to copy)\r\n\r\n Paste code here if prompted > "},
		{"a numbered choice", "\x1b[2J\x1b[H ❯ 1. Yes, I trust this folder\r\n\r\n Browser didn't open? Use the url below to sign in (c to copy)\r\n\r\n Paste code here if prompted > "},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(claudeTrustOptions(t)))
			nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
			p.outW.Write([]byte(c.screen))
			q := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
			if !strings.Contains(q.Message, "Browser didn't open") || !q.Line {
				t.Fatalf("the sign-in question is not the one shown: %+v", q)
			}
			if _, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: q.ID}); !errors.Is(err, ErrTrustQuestion) {
				t.Fatalf("a line: %v", err)
			}
			noWrite(t, p, 100*time.Millisecond)
		})
	}
}

// The question bar shown again on a phone shrinks its terminal, and the
// session with it, just before the line comes: the screen the line is
// weighed against is cut by the window made smaller until the program
// draws it again, as a TUI does at once. The line waits for that, nothing
// typed meanwhile, and is typed once the screen shows the sign-in whole.
func TestALineWaitsForTheSignInScreenDrawnAgainAfterAResize(t *testing.T) {
	s, p := newLocalWith(t, quiet(claudeTrustOptions(t)))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	p.outW.Write([]byte(claudeSignInScreen))
	q := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust && a.Line })
	if err := s.Resize(nater, 38, 20); err != nil {
		t.Fatal(err)
	}
	if uncut, _, _ := s.asked.DrawnAt(); uncut {
		t.Fatal("the screen is not cut by the window made smaller")
	}
	// The redraw is let go once the line waits for it (inLineWait), not
	// after a while without a write.
	waiting := make(chan struct{})
	var once sync.Once
	hook := func() { once.Do(func() { close(waiting) }) }
	s.inLineWait.Store(&hook)
	done := make(chan error, 1)
	go func() {
		res, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: q.ID})
		if err == nil && !res.Entered {
			err = errors.New("not entered")
		}
		done <- err
	}()
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("the line did not wait for the screen drawn again: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the line never waited for the screen drawn again")
	}
	select {
	case b := <-p.input:
		t.Fatalf("written while it waits: %q", b)
	default:
	}
	// Drawn again at the new size.
	p.outW.Write([]byte(claudeSignInScreen))
	if got := nextWrite(t, p); got != signInCode {
		t.Fatalf("typed %q", got)
	}
	if got := nextWrite(t, p); got != "\r" {
		t.Fatalf("then %q", got)
	}
	if err := <-done; err != nil {
		t.Fatalf("the line: %v", err)
	}
}

// A line to the sign-in screen is no first prompt: the startup questions
// are watched still. The program drawing the folder's trust question after
// the sign-in has it asked, with its answers, and a person's text is
// refused there; a sign-in screen drawn again after its line (a code the
// program refused) is asked again, and takes another line.
func TestTheStartupQuestionsAreWatchedAfterALine(t *testing.T) {
	s, p := newLocalWith(t, quiet(claudeTrustOptions(t)))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	p.outW.Write([]byte(claudeSignInScreen))
	first := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if res, err := s.Submit(t.Context(), Submission{Text: "wrong-code-1234", By: nater, Question: first.ID}); err != nil || !res.Entered {
		t.Fatalf("the first line: %+v %v", res, err)
	}
	nextWrite(t, p)
	nextWrite(t, p)
	// Refused: the program says so and draws the screen again.
	p.outW.Write([]byte("\x1b[2J\x1b[H That code was not accepted.\r\n" + strings.TrimPrefix(claudeSignInScreen, "\x1b[2J\x1b[H")))
	again := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust && a.ID != first.ID })
	if !again.Line || again.Answered != "" {
		t.Fatalf("the sign-in screen asked again: %+v", again)
	}
	if res, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: again.ID}); err != nil || !res.Entered {
		t.Fatalf("the second line: %+v %v", res, err)
	}
	if got := nextWrite(t, p); got != signInCode {
		t.Fatalf("typed %q", got)
	}
	nextWrite(t, p)
	// Signed in: the folder's trust question comes next.
	p.outW.Write([]byte(claudeTrustScreen))
	trust := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust && len(a.Options) > 0 })
	if trust.Line || !strings.Contains(trust.Message, "trust") {
		t.Fatalf("the trust question after the sign-in: %+v", trust)
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: nater}); !errors.Is(err, ErrTrustQuestion) {
		t.Fatalf("a person's text at the trust question: %v", err)
	}
	if _, err := s.Submit(t.Context(), Submission{Text: signInCode, By: nater, Question: trust.ID}); !errors.Is(err, ErrTrustQuestion) {
		t.Fatalf("a line to the trust question: %v", err)
	}
	noWrite(t, p, 100*time.Millisecond)
}
