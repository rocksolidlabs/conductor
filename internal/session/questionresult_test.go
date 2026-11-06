package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// The kept result of a question by id (the decision inbox's track): who
// answered it, the winning reply's request id, and its resumption, set
// once at the first attention change after the answer that says how the
// session went on, never by a later one; the result past resultsKept
// later questions forgotten.
func TestQuestionResult(t *testing.T) {
	s, p, _ := asking(t, Options{})
	nate := controller(t, s, "Nate")
	answer := func(q Attention, reqID string) {
		t.Helper()
		if err := s.AnswerInputReq(context.Background(), nate, q.ID, reqID, []byte("Postgres\r"), nil); err != nil {
			t.Fatalf("answering %s: %v", q.Message, err)
		}
		if got := written(t, p); got != "Postgres\r" {
			t.Fatalf("written %q", got)
		}
	}

	// The session's own reports spend no token of the reports' bucket, so
	// the test asks past its burst.
	reported := func(t *testing.T, s *Local, words string) Attention {
		t.Helper()
		s.SetAttentionFull(AttentionNeedsInput, words, SourceAPI, KindPrompt, dbChoices)
		q := s.Info().Attention
		if q.ID == "" || q.Message != words {
			t.Fatalf("reported %q: %+v", words, q)
		}
		return q
	}
	q1 := reported(t, s, "Which database?")
	if _, ok := s.QuestionResult(q1.ID); ok {
		t.Fatal("a result for a question not answered")
	}
	answer(q1, "req-1")
	r, ok := s.QuestionResult(q1.ID)
	if !ok || r.ByName != "Nate" || r.ReqID != "req-1" || r.Message != "Which database?" || r.At.IsZero() || r.Resumed != nil {
		t.Fatalf("the result: %+v %v", r, ok)
	}
	if last := s.Info().LastAnswer; last == nil || last.ReqID != "" || last.Resumed != nil {
		t.Fatalf("the description's last answer carries what is kept with the result: %+v", last)
	}

	// The hold of the turn is no resumption; the agent working is, once.
	s.SetAttentionFull(AttentionNone, "", SourceAPI, "", nil)
	if r, _ := s.QuestionResult(q1.ID); r.Resumed != nil {
		t.Fatalf("resumed by an idle attention: %+v", r.Resumed)
	}
	s.SetAttentionFull(AttentionWorking, "", SourceAPI, "", nil)
	r, _ = s.QuestionResult(q1.ID)
	if r.Resumed == nil || r.Resumed.How != ResumedWorking || r.Resumed.At.IsZero() {
		t.Fatalf("after working: %+v", r.Resumed)
	}
	worked := r.Resumed.At
	s.SetAttentionFull(AttentionDone, "done", SourceAPI, KindDone, nil)
	q2 := reported(t, s, "Which port?")
	if r, _ := s.QuestionResult(q1.ID); r.Resumed == nil || r.Resumed.How != ResumedWorking || !r.Resumed.At.Equal(worked) {
		t.Fatalf("changed by a later attention: %+v", r.Resumed)
	}

	// A reply naming no request: none kept; the next question its resumption.
	answer(q2, "")
	q3 := reported(t, s, "Which name?")
	if r, ok := s.QuestionResult(q2.ID); !ok || r.ReqID != "" || r.Resumed == nil || r.Resumed.How != ResumedAsked {
		t.Fatalf("q2: %+v %v", r, ok)
	}

	// resultsKept later questions forget the first.
	answer(q3, "req-3")
	for i := range resultsKept {
		q := reported(t, s, fmt.Sprintf("Question %d?", i))
		answer(q, fmt.Sprintf("req-q%d", i))
	}
	if _, ok := s.QuestionResult(q1.ID); ok {
		t.Fatal("the first result kept past resultsKept later ones")
	}
	if r, ok := s.QuestionResult(q3.ID); ok {
		t.Fatalf("q3 kept past resultsKept later ones: %+v", r)
	}

	// A question the agent takes away goes without an answer: left
	// withdrawn, no result; one answered is never left.
	gone := reported(t, s, "Gone?")
	s.SetAttentionFull(AttentionWorking, "", SourceAPI, "", nil)
	if l, ok := s.QuestionLeft(gone.ID); !ok || l.How != LeftWithdrawn || l.At.IsZero() {
		t.Fatalf("withdrawn: %+v %v", l, ok)
	}
	if _, ok := s.QuestionResult(gone.ID); ok {
		t.Fatal("a result for a question withdrawn")
	}
	if _, ok := s.QuestionLeft(q3.ID); ok {
		t.Fatal("an answered question left")
	}

	// The session's end is the last answer's resumption.
	last := reported(t, s, "Last?")
	answer(last, "req-last")
	p.exit()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r, _ := s.QuestionResult(last.ID); r.Resumed != nil {
			if r.Resumed.How != ResumedEnded {
				t.Fatalf("at the end: %+v", r.Resumed)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the end never set the resumption")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A question keys typed in the terminal reached (consumed) leaves as
// answered there, whatever path lets it go: replaced by the agent's
// working report, which forgets the question (and its consumption)
// before the attention changes, or by the next question.
func TestQuestionLeftAnsweredInTheTerminal(t *testing.T) {
	s, _, _ := asking(t, Options{})
	for _, next := range []func(){
		func() { s.SetAttentionFull(AttentionWorking, "", SourceAPI, "", nil) },
		func() { s.SetAttentionFull(AttentionNeedsInput, "Next?", SourceAPI, KindPrompt, dbChoices) },
	} {
		s.SetAttentionFull(AttentionNeedsInput, "Which database?", SourceAPI, KindPrompt, dbChoices)
		q := s.Info().Attention
		s.mu.Lock()
		s.consumeLocked(q.ID)
		s.mu.Unlock()
		if a := s.Info().Attention; a.ID != q.ID || a.Answered != AnsweredTerminal {
			t.Fatalf("consumed: %+v", a)
		}
		if _, ok := s.QuestionLeft(q.ID); ok {
			t.Fatal("left while it still shows")
		}
		next()
		if l, ok := s.QuestionLeft(q.ID); !ok || l.How != LeftTerminal {
			t.Fatalf("left: %+v %v", l, ok)
		}
		if _, ok := s.QuestionResult(q.ID); ok {
			t.Fatal("a result for keys typed in the terminal")
		}
	}
}

// leftAs waits for the entry the session keeps of how the question id
// left (QuestionLeft, noted in the step that let it go, under the lock),
// and fails unless it is how: a change seen showing says nothing of it.
func leftAs(t *testing.T, s *Local, id, how string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if l, ok := s.QuestionLeft(id); ok {
			if l.How != how || l.At.IsZero() {
				t.Fatalf("left %+v, want %s", l, how)
			}
			if _, answered := s.QuestionResult(id); answered {
				t.Fatal("a question that left without an answer has a result")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the question never left; want %s", how)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Every way a question leaves a session, against what the session then
// keeps of it: answered through its claim (a result, never a departure),
// set aside or held over (neither: it is held still), and gone for good
// one way each: keys typed in the terminal (whatever let it go after,
// the process's end among them), the session's end, another question in
// its place, or withdrawn by the agent, with or without a change of what
// shows. The decision inbox's track and item read exactly this
// (internal/api, TestInboxTrackHowAQuestionLeft).
func TestHowAQuestionLeaves(t *testing.T) {
	ask := func(t *testing.T, s *Local) Attention {
		t.Helper()
		s.SetAttentionFull(AttentionNeedsInput, "Which database?", SourceAPI, KindPrompt, dbChoices)
		return s.Info().Attention
	}
	consume := func(s *Local, id string) {
		s.mu.Lock()
		s.consumeLocked(id)
		s.mu.Unlock()
	}
	t.Run("answered through its claim", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		q := ask(t, s)
		if err := s.AnswerInputReq(context.Background(), ownerOf(t, s), q.ID, "r1", []byte("Postgres\r"), nil); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		reportState(t, s, AttentionWorking, "")
		if r, ok := s.QuestionResult(q.ID); !ok || r.ReqID != "r1" || r.Resumed == nil || r.Resumed.How != ResumedWorking {
			t.Fatalf("the result: %+v %v", r, ok)
		}
		if l, ok := s.QuestionLeft(q.ID); ok {
			t.Fatalf("an answered question left: %+v", l)
		}
	})
	t.Run("keys typed in the terminal, then the process ends", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		q := ask(t, s)
		consume(s, q.ID)
		p.exit()
		leftAs(t, s, q.ID, LeftTerminal)
	})
	t.Run("keys typed in the terminal, then the next question", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		q := ask(t, s)
		consume(s, q.ID)
		s.SetAttentionFull(AttentionNeedsInput, "Next?", SourceAPI, KindPrompt, dbChoices)
		leftAs(t, s, q.ID, LeftTerminal)
	})
	t.Run("a person's Enter at a wait with no words", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		reportState(t, s, AttentionWorking, "")
		out(p, "\a")
		for deadline := time.Now().Add(10 * time.Second); ; {
			if a := s.Info().Attention; a.State == AttentionNeedsInput && a.Source == SourceBell {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the bell's wait never showed: %+v", s.Info().Attention)
			}
			time.Sleep(5 * time.Millisecond)
		}
		q := s.Info().Attention
		if err := s.Input(ownerOf(t, s), []byte("\r")); err != nil {
			t.Fatal(err)
		}
		leftAs(t, s, q.ID, LeftTerminal)
	})
	t.Run("a person's keys while it is set aside", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		q := askOwn(t, s, "Which database?")
		reportState(t, s, AttentionWorking, "")
		if err := s.Input(ownerOf(t, s), []byte("x")); err != nil {
			t.Fatal(err)
		}
		leftAs(t, s, q.ID, LeftTerminal)
	})
	t.Run("the session ends while it shows", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		q := ask(t, s)
		p.exit()
		leftAs(t, s, q.ID, LeftEnded)
	})
	t.Run("the session ends while it is set aside", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		q := askOwn(t, s, "Which database?")
		reportState(t, s, AttentionWorking, "")
		if _, ok := s.QuestionLeft(q.ID); ok || !slices.Contains(s.Info().Kept, q.ID) {
			t.Fatalf("set aside: left %v, kept %v", ok, s.Info().Kept)
		}
		p.exit()
		leftAs(t, s, q.ID, LeftEnded)
	})
	t.Run("the session ends while the hold holds it over", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		id, err := s.TryAskID("Which heading style?", SourceAPI, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		go holdNow(s)
		heldShows(t, s)
		if _, ok := s.QuestionLeft(id); ok || !slices.Contains(s.Info().Kept, id) {
			t.Fatalf("held over: left %v, kept %v", ok, s.Info().Kept)
		}
		p.exit()
		leftAs(t, s, id, LeftEnded)
		if kept := s.Info().Kept; len(kept) != 0 {
			t.Fatalf("kept past the end: %v", kept)
		}
	})
	t.Run("answered as the session ends, its keys written before the end", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		q := ask(t, s)
		owner := ownerOf(t, s)
		// The keys written, held between their write and their resolve
		// (beforeWritten) while the process ends.
		reached, gate := make(chan struct{}), make(chan struct{})
		var once, let sync.Once
		pause := func(data []byte) {
			if string(data) == "Postgres\r" {
				once.Do(func() { close(reached); <-gate })
			}
		}
		s.beforeWritten.Store(&pause)
		t.Cleanup(func() { let.Do(func() { close(gate) }) })
		word := make(chan error, 1)
		go func() { word <- s.AnswerInputReq(context.Background(), owner, q.ID, "r1", []byte("Postgres\r"), nil) }()
		if got := written(t, p); got != "Postgres\r" {
			t.Fatalf("written %q", got)
		}
		<-reached
		p.exit()
		select {
		case <-s.ended:
		case <-time.After(10 * time.Second):
			t.Fatal("the session never ended")
		}
		// The end noted what it let go in its own step: not this question,
		// whose claim's keys were written.
		if l, ok := s.QuestionLeft(q.ID); ok {
			t.Fatalf("left at the end with its keys written: %+v", l)
		}
		let.Do(func() { close(gate) })
		if err := returnsWithin(t, 5*time.Second, "the word", func() error { return <-word }); err != nil {
			t.Fatalf("the word: %v", err)
		}
		r, ok := s.QuestionResult(q.ID)
		if !ok || r.ReqID != "r1" || r.Resumed == nil || r.Resumed.How != ResumedEnded || r.Resumed.At.Before(r.At) {
			t.Fatalf("the result: %+v %v", r, ok)
		}
		if l, ok := s.QuestionLeft(q.ID); ok {
			t.Fatalf("an answered question left: %+v", l)
		}
	})
	t.Run("the session ends under a reply's keys the program has not taken", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		q := ask(t, s)
		owner := ownerOf(t, s)
		// The write begun and held in the program's read (onWrite) as the
		// process ends: the keys never land.
		inWrite, gate := make(chan struct{}), make(chan struct{})
		var once, let sync.Once
		p.onWrite = func() { once.Do(func() { close(inWrite); <-gate }) }
		t.Cleanup(func() { let.Do(func() { close(gate) }) })
		word := make(chan error, 1)
		go func() { word <- s.AnswerInputReq(context.Background(), owner, q.ID, "r1", []byte("Postgres\r"), nil) }()
		<-inWrite
		p.exit()
		if err := returnsWithin(t, 10*time.Second, "the word", func() error { return <-word }); !errors.Is(err, ErrSessionEnded) {
			t.Fatalf("the word: %v", err)
		}
		leftAs(t, s, q.ID, LeftEnded)
	})
	t.Run("another question in its place", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		q := ask(t, s)
		s.SetAttentionFull(AttentionNeedsInput, "Which port?", SourceAPI, KindPrompt, dbChoices)
		leftAs(t, s, q.ID, LeftSuperseded)
	})
	t.Run("withdrawn by the agent's next state", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		q := ask(t, s)
		reportState(t, s, AttentionWorking, "")
		leftAs(t, s, q.ID, LeftWithdrawn)
	})
	t.Run("withdrawn by its id while set aside, nothing else changing", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		q := askOwn(t, s, "Which database?")
		reportState(t, s, AttentionWorking, "")
		s.WithdrawQuestion(q.ID)
		if a := s.Info().Attention; a.State != AttentionWorking {
			t.Fatalf("what shows changed: %+v", a)
		}
		leftAs(t, s, q.ID, LeftWithdrawn)
	})
	t.Run("withdrawn by its id while the hold holds it over", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		id, err := s.TryAskID("Which heading style?", SourceAPI, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		go holdNow(s)
		heldShows(t, s)
		if _, ok := s.QuestionLeft(id); ok {
			t.Fatal("left while the hold holds it over")
		}
		s.WithdrawQuestion(id)
		leftAs(t, s, id, LeftWithdrawn)
	})
	t.Run("set aside, shown again at the done and answered", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		q := askOwn(t, s, "Which database?", dbChoices...)
		reportState(t, s, AttentionWorking, "")
		reportState(t, s, AttentionDone, "done")
		showsOwn(t, s, q, "at the turn's done")
		if err := s.AnswerInput(context.Background(), ownerOf(t, s), q.ID, []byte("Postgres\r")); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		if _, ok := s.QuestionLeft(q.ID); ok {
			t.Fatal("a question set aside and answered left")
		}
		if _, ok := s.QuestionResult(q.ID); !ok {
			t.Fatal("no result")
		}
	})
}
