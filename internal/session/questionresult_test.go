package session

import (
	"context"
	"fmt"
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
