package session

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// What the session keeps without showing (Info.Kept): the agent's own
// question set aside by work, and the question a held turn took the place
// of; never the one shown; at most MaxKept.
func TestKept(t *testing.T) {
	t.Run("set aside", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		q := askOwn(t, s, "Which database?", dbChoices...)
		if kept := s.Info().Kept; len(kept) != 0 {
			t.Fatalf("shown, and kept %q", kept)
		}
		reportState(t, s, AttentionWorking, "")
		if info := s.Info(); info.Attention.State != AttentionWorking || !slices.Equal(info.Kept, []string{q.ID}) {
			t.Fatalf("set aside: %+v, kept %q", info.Attention, info.Kept)
		}
		reportState(t, s, AttentionDone, "done for now")
		showsOwn(t, s, q, "at the turn's done")
		if kept := s.Info().Kept; len(kept) != 0 {
			t.Fatalf("shown again, and kept %q", kept)
		}
	})
	t.Run("held over", func(t *testing.T) {
		s, _ := newLocalWith(t, quiet(Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second}))
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		q := s.Info().Attention
		go holdNow(s)
		heldShows(t, s)
		if info := s.Info(); info.Attention.ID == q.ID || !slices.Equal(info.Kept, []string{q.ID}) {
			t.Fatalf("held over: %+v, kept %q", info.Attention, info.Kept)
		}
	})
	t.Run("both", func(t *testing.T) {
		s, _, _ := asking(t, Options{ScrollbackBytes: 4096})
		own := askOwn(t, s, "Which database?", dbChoices...)
		reportState(t, s, AttentionWorking, "")
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		q := s.Info().Attention
		if q.ID == "" || q.ID == own.ID {
			t.Fatalf("the second question: %+v", q)
		}
		go holdNow(s)
		heldShows(t, s)
		info := s.Info()
		if len(info.Kept) > MaxKept || slices.Contains(info.Kept, info.Attention.ID) {
			t.Fatalf("kept %q, shown %+v", info.Kept, info.Attention)
		}
		for _, id := range info.Kept {
			if id != own.ID && id != q.ID {
				t.Fatalf("kept %q, an id neither question has", info.Kept)
			}
		}
		if !slices.Contains(info.Kept, q.ID) {
			t.Fatalf("kept %q: the question the hold took the place of is not among them", info.Kept)
		}
	})
}

// What a session shows and what it keeps are one snapshot: the agent's own
// question A, set aside by work and shown again at the turn's done while
// Info is read in a loop, is in every snapshot, shown or kept, never
// neither.
func TestKeptReadWithTheAttention(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	const readers = 4
	for range 200 {
		// The session's own calls, which spend no token of the reports'
		// bucket: the rounds are past its burst.
		s.Ask("Which database?", SourceAPI, KindPrompt, dbChoices)
		q := s.Info().Attention
		stop := make(chan struct{})
		var wg sync.WaitGroup
		var bad []string
		var mu sync.Mutex
		for range readers {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					info := s.Info()
					if info.Attention.ID != q.ID && !slices.Contains(info.Kept, q.ID) {
						mu.Lock()
						bad = append(bad, string(info.Attention.State))
						mu.Unlock()
					}
				}
			})
		}
		s.SetAttentionFull(AttentionWorking, "", SourceAPI, "", nil)
		s.SetAttentionFull(AttentionDone, "done for now", SourceAPI, KindDone, nil)
		close(stop)
		wg.Wait()
		if len(bad) > 0 {
			t.Fatalf("snapshots neither showing nor keeping the question: %q", bad)
		}
		showsOwn(t, s, q, "at the turn's done")
		// The answer's Enter begins the next turn, so the next question is
		// asked in a turn of its own.
		if err := s.AnswerInput(context.Background(), owner, q.ID, []byte("Postgres\r")); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		shows(t, s, AttentionNone, "answered")
	}
}

// An actor's choice is written as a viewer's would be, resolving the
// question; the same answer again is refused, nothing written.
func TestAnswerAsChoice(t *testing.T) {
	s, p, _ := asking(t, Options{})
	q := askOwn(t, s, "Which database?", dbChoices...)
	if err := s.AnswerAs(context.Background(), q.ID, "SQLite\r", Actor{Subject: "u-ada", Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "SQLite\r" {
		t.Fatalf("written %q", got)
	}
	shows(t, s, AttentionNone, "answered")
	if err := s.AnswerAs(context.Background(), q.ID, "SQLite\r", Actor{Name: "Ada"}); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("the same answer again: %v", err)
	}
	nothingWritten(t, p, 50*time.Millisecond)
}

// An actor writes nothing but the shown question's own choices: other keys,
// a digit, an Enter alone and keys for a question that offers none are
// refused, nothing written, the question still waiting; its choice then
// answers it.
func TestAnswerAsOnlyChoices(t *testing.T) {
	s, p, _ := asking(t, Options{})
	ada := Actor{Name: "Ada"}
	q := askOwn(t, s, "Which database?", dbChoices...)
	for _, keys := range []string{"MySQL\r", "1", "\r", "Postgres", "\x03", ""} {
		if err := s.AnswerAs(context.Background(), q.ID, keys, ada); !errors.Is(err, ErrNotAChoice) {
			t.Fatalf("keys %q: %v", keys, err)
		}
	}
	nothingWritten(t, p, 50*time.Millisecond)
	showsOwn(t, s, q, "after the refusals")
	if err := s.AnswerAs(context.Background(), q.ID, "Postgres\r", ada); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "Postgres\r" {
		t.Fatalf("written %q", got)
	}

	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, KindPrompt, nil); err != nil {
		t.Fatal(err)
	}
	open := s.Info().Attention
	for _, keys := range []string{"\r", "y", "anna\r"} {
		if err := s.AnswerAs(context.Background(), open.ID, keys, ada); !errors.Is(err, ErrNotAChoice) {
			t.Fatalf("keys %q for a question with no choices: %v", keys, err)
		}
	}
	nothingWritten(t, p, 50*time.Millisecond)
	if a := s.Info().Attention; a.ID != open.ID {
		t.Fatalf("the question went: %+v", a)
	}
}

// A held turn, a question no longer shown and one answered in the terminal
// refuse an actor as they refuse a viewer: nothing written.
func TestAnswerAsHeldTurn(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second}))
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which database?", SourceAPI, KindPrompt, dbChoices); err != nil {
			t.Fatal(err)
		}
		q := s.Info().Attention
		go holdNow(s)
		heldShows(t, s)
		hold := s.Info().Attention
		for _, c := range []struct{ id, keys string }{{q.ID, "Postgres\r"}, {hold.ID, "\r"}, {hold.ID, "Postgres\r"}} {
			if err := s.AnswerAs(context.Background(), c.id, c.keys, Actor{Name: "Ada"}); !errors.Is(err, ErrQuestionAnswered) {
				t.Fatalf("%q to %s: %v", c.keys, c.id, err)
			}
		}
		nothingWritten(t, p, 50*time.Millisecond)
		if !s.Held() {
			t.Fatal("the hold ended")
		}
	})
	t.Run("no longer shown", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		q := askOwn(t, s, "Which database?", dbChoices...)
		reportState(t, s, AttentionWorking, "")
		if err := s.AnswerAs(context.Background(), q.ID, "Postgres\r", Actor{Name: "Ada"}); !errors.Is(err, ErrQuestionAnswered) {
			t.Fatalf("a question set aside: %v", err)
		}
		nothingWritten(t, p, 50*time.Millisecond)
	})
	t.Run("answered in the terminal", func(t *testing.T) {
		s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
		sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
		att := waitAttention(t, s, asked)
		if len(att.Options) == 0 {
			t.Fatalf("the dialog has no choices: %+v", att)
		}
		if err := s.Input(sub, []byte("y")); err != nil {
			t.Fatal(err)
		}
		if got := nextWrite(t, p); got != "y" {
			t.Fatalf("written %q", got)
		}
		if err := s.AnswerAs(context.Background(), att.ID, att.Options[0].Input, Actor{Name: "Ada"}); !errors.Is(err, ErrQuestionAnswered) || err.Error() != QuestionConsumedWords {
			t.Fatalf("after the key: %v", err)
		}
		nothingWritten(t, p, 50*time.Millisecond)
	})
}

// An actor's answer and a viewer's to one question at once: exactly one is
// written, the other refused ErrQuestionAnswered.
func TestAnswerAsAgainstAViewer(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	for i := range 20 {
		q := askOwn(t, s, "Which database?", dbChoices...)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() {
			<-start
			errs[0] = s.AnswerInput(context.Background(), owner, q.ID, []byte("Postgres\r"))
		})
		wg.Go(func() {
			<-start
			errs[1] = s.AnswerAs(context.Background(), q.ID, "SQLite\r", Actor{Name: "Ada"})
		})
		close(start)
		wg.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ErrQuestionAnswered):
				t.Fatalf("round %d: %v", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("round %d: %d answers taken (%v)", i, won, errs)
		}
		got := written(t, p)
		if got != "Postgres\r" && got != "SQLite\r" {
			t.Fatalf("round %d: written %q", i, got)
		}
		nothingWritten(t, p, 20*time.Millisecond)
		shows(t, s, AttentionNone, "answered")
	}
}

// The actor's name is the answer's: the last answer, the input entry and
// the chat's answered line name it, with no subscriber behind it.
func TestAnswerAsRecordsTheName(t *testing.T) {
	s, p, _ := asking(t, Options{})
	q := askOwn(t, s, "Which database?", dbChoices...)
	if err := s.AnswerAs(context.Background(), q.ID, "Postgres\r", Actor{Subject: "u-ada", Name: " Ada\n"}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	shows(t, s, AttentionNone, "answered")
	if a := s.Info().LastAnswer; a == nil || a.ByName != "Ada" || a.By != "" || a.Message != "Which database?" {
		t.Fatalf("the last answer: %+v", a)
	}
	var entry *ActivityEntry
	for _, e := range s.Activity() {
		if e.Type == ActivityInput {
			entry = &e
		}
	}
	if entry == nil || entry.ByName != "Ada" || entry.By != "" || entry.Message != "Which database?" {
		t.Fatalf("the input entry: %+v", entry)
	}
	var line *ChatMessage
	for _, m := range s.ChatHistory() {
		if m.Event == "answered" {
			line = &m
		}
	}
	if line == nil || line.By.Name != "Ada" || line.By.ID != "" || line.Ref != q.ID || line.Text != "Postgres" {
		t.Fatalf("the answered line: %+v", line)
	}
}
