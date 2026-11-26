package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A chat's message is sent under its sender's credential, as the terminal's
// writer makes a write (newWrite): taken and asked just before the send, let
// go once the send has returned. One that no longer stands withdraws the
// message: nothing sent, its question's claim let go, its sender told.

// queuedInTurn is a chat in a turn whose agent asked its own question with
// two choices: what is sent for it waits for the turn's end.
func queuedInTurn(t *testing.T) (*Local, string, func() []Message, func()) {
	t.Helper()
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	q := fruit(t, s)
	sent := func() []Message { return f.Sent() }
	end := func() { f.TurnEnded("done", Usage{}) }
	return s, q, sent, end
}

// awaitAnswerEntry waits for the transcript's entry that answers q (the
// message queued); the bound only bounds a hang.
func awaitAnswerEntry(t *testing.T, s *Local, q string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		for _, e := range allEntries(t, s) {
			if e.Kind == EntryPrompt && e.Answers == q {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("the answer was never queued")
		case <-time.After(time.Millisecond):
		}
	}
}

// wordOf waits for a word on ch; the bound only bounds a hang.
func wordOf(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("no word")
		return nil
	}
}

// stillWaits says the question waits again: shown, unclaimed, unanswered,
// its answer's entry withdrawn.
func stillWaits(t *testing.T, s *Local, q string) {
	t.Helper()
	s.mu.Lock()
	err := s.waitsLocked(q, "")
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("the question does not wait: %v", err)
	}
	if _, ok := s.QuestionResult(q); ok {
		t.Fatal("the question was answered")
	}
	if e := answerEntry(t, s, q); e.State != PromptWithdrawn {
		t.Fatalf("the answer's entry %+v", e)
	}
}

func TestAChatWithdrawsAnActorsChoiceWhoseCredentialEnded(t *testing.T) {
	s, q, sent, end := queuedInTurn(t)
	cred := newTestCred()
	word := make(chan error, 1)
	go func() {
		word <- s.AnswerAs(context.Background(), q, "Plums\r", Actor{Name: "Ops desk", ReqID: "d1", Hold: cred.hold})
	}()
	awaitAnswerEntry(t, s, q)
	cred.end(ErrRevoked)
	end()
	if err := wordOf(t, word); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the actor's word: %v", err)
	}
	if n := len(sent()); n != 0 {
		t.Fatalf("%d sent", n)
	}
	stillWaits(t, s, q)
}

func TestAChatWithdrawsAnActorsLineWhoseCredentialEnded(t *testing.T) {
	s, q, sent, end := queuedInTurn(t)
	cred := newTestCred()
	word := make(chan error, 1)
	_, err := s.Submit(context.Background(), Submission{Text: "Plums", Question: q, ByName: "Ops desk", Hold: cred.hold,
		Finished: func(res SubmitResult, err error) { word <- AnswerWord(res, err) }})
	if !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("the line: %v", err)
	}
	cred.end(ErrExpired)
	end()
	if err := wordOf(t, word); !errors.Is(err, ErrExpired) {
		t.Fatalf("the line's word: %v", err)
	}
	if n := len(sent()); n != 0 {
		t.Fatalf("%d sent", n)
	}
	stillWaits(t, s, q)
}

func TestAChatWithdrawsAViewersLineWhoseLinkEnded(t *testing.T) {
	s, q, sent, end := queuedInTurn(t)
	cred := newTestCred()
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Ada", Cols: 100, Rows: 30, Hold: cred.hold}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	word := make(chan error, 1)
	if _, err := s.Submit(context.Background(), Submission{Text: "Plums", Question: q, By: sub,
		Finished: func(res SubmitResult, err error) { word <- AnswerWord(res, err) }}); !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("the line: %v", err)
	}
	cred.end(ErrRevoked)
	end()
	if err := wordOf(t, word); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the viewer's word: %v", err)
	}
	if n := len(sent()); n != 0 {
		t.Fatalf("%d sent", n)
	}
	stillWaits(t, s, q)
}

// A credential that stands: the message is sent once, its credential
// taken once and let go once the send has returned.
func TestAChatSendsUnderACredentialThatStands(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	q := fruit(t, s)
	cred := newTestCred()
	word := make(chan error, 1)
	go func() {
		word <- s.AnswerAs(context.Background(), q, "Plums\r", Actor{Name: "Ops desk", Hold: cred.hold})
	}()
	awaitAnswerEntry(t, s, q)
	if h, r := cred.counts(); h != 0 || r != 0 {
		t.Fatalf("taken %d, let go %d before the send", h, r)
	}
	f.TurnEnded("done", Usage{})
	waitCalls(t, f, "the send", sends(1))
	f.Accepted(f.Sent()[0].Ref)
	if err := wordOf(t, word); err != nil {
		t.Fatalf("the word: %v", err)
	}
	// The agent's acknowledgement may come before the send has returned:
	// the credential is let go once it has.
	deadline := time.After(30 * time.Second)
	for {
		if _, r := cred.counts(); r > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the credential was never let go")
		case <-time.After(time.Millisecond):
		}
	}
	if h, r := cred.counts(); h != 1 || r != 1 {
		t.Fatalf("taken %d, let go %d", h, r)
	}
	if n := len(f.Sent()); n != 1 || f.Sent()[0].Text != "Plums" {
		t.Fatalf("sent %+v", f.Sent())
	}
}

// A credential that stops standing once taken, before the send (at the
// take's seam): asked at the commit boundary, it withdraws the message,
// let go.
func TestAChatAsksTheCredentialAtTheSend(t *testing.T) {
	s, q, sent, end := queuedInTurn(t)
	cred := newTestCred()
	cred.atHeld = func() { cred.end(ErrRevoked) }
	word := make(chan error, 1)
	if _, err := s.Submit(context.Background(), Submission{Text: "Plums", Question: q, ByName: "Ops desk", Hold: cred.hold,
		Finished: func(res SubmitResult, err error) { word <- AnswerWord(res, err) }}); !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("the line: %v", err)
	}
	end()
	if err := wordOf(t, word); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the word: %v", err)
	}
	if n := len(sent()); n != 0 {
		t.Fatalf("%d sent", n)
	}
	if h, r := cred.counts(); h != 1 || r != 1 {
		t.Fatalf("taken %d, let go %d", h, r)
	}
	stillWaits(t, s, q)
}

// A message with no credential is sent as before.
func TestAChatSendsAMessageWithNoCredential(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	q := fruit(t, s)
	word := make(chan error, 1)
	if _, err := s.Submit(context.Background(), Submission{Text: "Plums", Question: q, ByName: "Ops desk",
		Finished: func(res SubmitResult, err error) { word <- AnswerWord(res, err) }}); !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("the line: %v", err)
	}
	f.TurnEnded("done", Usage{})
	waitCalls(t, f, "the send", sends(1))
	f.Accepted(f.Sent()[0].Ref)
	if err := wordOf(t, word); err != nil {
		t.Fatalf("the word: %v", err)
	}
}
