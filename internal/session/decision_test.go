package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A question that mirrors a durable decision (TryAskDecision,
// docs/plans/decisions.md, the session): the agent's own question that only
// its decision's application answers, and that keys, the screen and lines
// naming no decision never take away.

// askDecision asks words as the question that mirrors decision, and
// returns it as it shows.
func askDecision(t *testing.T, s *Local, decision, words string) Attention {
	t.Helper()
	id, err := s.TryAskDecision(decision, words)
	if err != nil {
		t.Fatal(err)
	}
	a := s.Info().Attention
	if a.State != AttentionNeedsInput || a.ID != id || a.Message != words || a.Decision != decision || !a.Asked() || len(a.Options) != 0 {
		t.Fatalf("asked %q for %s (id %q): %+v", words, decision, id, a)
	}
	return a
}

// answerLine submits the decision's answer as the engine's application
// does: a reply naming the question and the decision, by the responder's
// name.
func answerLine(s *Local, q Attention, decision, text string) (SubmitResult, error) {
	return s.Submit(context.Background(), Submission{Text: text, ByName: "Nate", Question: q.ID, Decision: decision})
}

// Every answer but its decision's is refused, nothing written: a viewer's
// reply (AnswerInput), an actor's choice (AnswerAs), a line naming the
// question, and a line naming it for another decision. The refusal names
// the decision and is the general refusal too. Its decision's answer is
// then the first thing written.
func TestADecisionQuestionRefusesEveryOtherAnswer(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	q := askDecision(t, s, "dr-7", "Ship the schema change?")
	refused := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrDecisionQuestion) || !errors.Is(err, ErrQuestionAnswered) || !strings.Contains(err.Error(), "dr-7") {
			t.Fatalf("%s: %v, want the decision's refusal", what, err)
		}
	}
	refused("a viewer's reply", s.AnswerInput(context.Background(), owner, q.ID, []byte("yes\r")))
	refused("an actor's answer", s.AnswerAs(context.Background(), q.ID, "yes\r", Actor{Subject: "nate", Name: "Nate"}))
	_, err := s.Submit(context.Background(), Submission{Text: "yes", ByName: "Nate", Question: q.ID})
	refused("a line naming it", err)
	_, err = s.Submit(context.Background(), Submission{Text: "yes", ByName: "Nate", Question: q.ID, Decision: "dr-8"})
	refused("a line naming another decision", err)
	if err := s.questionWaitsAs(q.ID, "dr-7"); err != nil {
		t.Fatalf("its decision's answer is refused: %v", err)
	}
	showsOwn(t, s, q, "after the refusals")

	if _, err := answerLine(s, q, "dr-7", "Decision dr-7 answered: Approve"); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "Decision dr-7 answered: Approve" {
		t.Fatalf("the first write is %q: a refused answer was written", got)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("then %q", got)
	}
}

// A person's keys, in the terminal while it shows and while it is set
// aside, the screen redrawn whole, and Conductor's line naming no decision
// leave it: the next done shows it again, the same question.
func TestADecisionQuestionOutlastsKeysAndTheScreen(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	q := askDecision(t, s, "dr-7", "Ship the schema change?")

	if err := s.Input(owner, []byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	if kept, _ := keeps(s); !kept {
		t.Fatal("a person's keys while it shows took it away")
	}
	reportState(t, s, AttentionWorking, "")
	if kept, aside := keeps(s); !kept || !aside {
		t.Fatalf("at work: kept %v, aside %v", kept, aside)
	}
	if err := s.Input(owner, []byte("more\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	if kept, _ := keeps(s); !kept {
		t.Fatal("a person's keys while it is set aside took it away")
	}
	reportState(t, s, AttentionDone, "waiting")
	showsOwn(t, s, q, "at the done after the keys")

	// The screen moves on whole.
	var screen strings.Builder
	screen.WriteString("\x1b[2J\x1b[H")
	for i := range 30 {
		screen.WriteString(strings.Repeat(string(rune('a'+i%26)), 70) + "\r\n")
	}
	out(p, screen.String())
	s.screenQuiet("")
	out(p, "\x1b[2J\x1b[H"+strings.Repeat("z", 400))
	s.screenQuiet("")
	showsOwn(t, s, q, "after the screen moved on")

	// Conductor's line naming no decision: typed, its Enter begins the
	// next turn, whose work sets the question aside and whose done shows
	// it again.
	res, err := s.Submit(context.Background(), Submission{Text: "a handoff", ByName: "Conductor"})
	if err != nil || !res.Entered {
		t.Fatalf("the line: %+v %v", res, err)
	}
	written(t, p)
	written(t, p)
	reportState(t, s, AttentionWorking, "")
	reportState(t, s, AttentionDone, "still waiting")
	showsOwn(t, s, q, "at the done after a line naming no decision")
}

// The engine's line naming the question and its decision answers it: its
// text and Enter written, the question gone and kept no longer, the
// answer recorded by the responder's name.
func TestADecisionQuestionIsAnsweredByItsToken(t *testing.T) {
	s, p, _ := asking(t, Options{})
	q := askDecision(t, s, "dr-7", "Ship the schema change?")
	res, err := answerLine(s, q, "dr-7", "Decision dr-7 (approval) answered by Nate: Approve")
	if err != nil || !res.Entered {
		t.Fatalf("the answer: %+v %v", res, err)
	}
	if got := written(t, p) + written(t, p); got != "Decision dr-7 (approval) answered by Nate: Approve\r" {
		t.Fatalf("written %q", got)
	}
	info := s.Info()
	if info.Attention.State != AttentionNone || info.LastAnswer == nil || info.LastAnswer.ByName != "Nate" || info.LastAnswer.Message != q.Message {
		t.Fatalf("answered: %+v %+v", info.Attention, info.LastAnswer)
	}
	if kept, _ := keeps(s); kept {
		t.Fatal("the answered question is kept")
	}
	// Answered, a second answer finds it gone.
	if _, err := answerLine(s, q, "dr-7", "again"); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a second answer: %v", err)
	}
}

// Set aside while the agent works and shown again at its turn's done, as
// the agent's own question is; unlike it, a new turn's work after the done
// sets it aside again rather than taking it away, and so does a clear. It
// goes with its withdrawal (WithdrawQuestion), set aside or shown.
func TestADecisionQuestionIsSetAsideAndShownAgain(t *testing.T) {
	for _, shown := range []bool{true, false} {
		s, _, _ := asking(t, Options{})
		q := askDecision(t, s, "dr-7", "Ship the schema change?")
		reportState(t, s, AttentionWorking, "")
		shows(t, s, AttentionWorking, "at work after asking")
		reportState(t, s, AttentionDone, "asked")
		showsOwn(t, s, q, "at the turn's done")
		// A new turn: the agent at work again.
		reportState(t, s, AttentionWorking, "")
		if kept, aside := keeps(s); !kept || !aside {
			t.Fatalf("at work in a new turn: kept %v, aside %v", kept, aside)
		}
		if err := s.TrySetAttentionFull(AttentionNone, "", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		if kept, _ := keeps(s); !kept {
			t.Fatal("a clear took it away")
		}
		reportState(t, s, AttentionDone, "done again")
		showsOwn(t, s, q, "at the next done")
		if !shown {
			reportState(t, s, AttentionWorking, "")
		}
		s.WithdrawQuestion(q.ID)
		if kept, _ := keeps(s); kept {
			t.Fatalf("withdrawn (shown %v), it is kept", shown)
		}
		reportState(t, s, AttentionDone, "after the withdrawal")
		shows(t, s, AttentionDone, "the done after the withdrawal")
	}
}

// One decision's line (Decision, UnlessWaiting) is typed past another
// decision's question, which stays; while a question no decision mirrors
// shows, it waits, nothing written.
func TestADecisionLinePassesAnotherDecisionsMirror(t *testing.T) {
	s, p, _ := asking(t, Options{})
	q := askDecision(t, s, "dr-2", "Which index?")
	res, err := s.Submit(context.Background(), Submission{Text: "Decision dr-1 answered: yes", ByName: "Nate", Decision: "dr-1", UnlessWaiting: true})
	if err != nil || !res.Entered {
		t.Fatalf("dr-1's line past dr-2's question: %+v %v", res, err)
	}
	if got := written(t, p) + written(t, p); got != "Decision dr-1 answered: yes\r" {
		t.Fatalf("written %q", got)
	}
	showsOwn(t, s, q, "dr-2's question after dr-1's line")

	s2, p2, _ := asking(t, Options{})
	askOwn(t, s2, "Which database?")
	res, err = s2.Submit(context.Background(), Submission{Text: "Decision dr-1 answered: yes", ByName: "Nate", Decision: "dr-1", UnlessWaiting: true})
	if err != nil || res.Typed || res.Entered {
		t.Fatalf("dr-1's line while the agent's own question shows: %+v %v", res, err)
	}
	// Nothing was written: the next write is the answer to that question.
	owner := ownerOf(t, s2)
	if err := s2.AnswerInput(context.Background(), owner, s2.Info().Attention.ID, []byte("Postgres\r")); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p2); got != "Postgres\r" {
		t.Fatalf("the first write is %q", got)
	}
}

// A decision's line (the run engine's, Decision set) answering the question
// that mirrors its decision is recorded as a typed line, as the engine's
// other lines are: its words are the response the ledger keeps, and a
// crew's test holds the line at that entry. A line naming the question
// with no decision (a provider's or a host's typed answer) is never
// recorded (typedEntry).
func TestADecisionsLineIsRecordedTyped(t *testing.T) {
	s, p, _ := asking(t, Options{})
	q := askDecision(t, s, "dr-7", "Ship the schema change?")
	if _, err := answerLine(s, q, "dr-7", "Decision dr-7 answered: Approve"); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	written(t, p)
	var typed []ActivityEntry
	for _, e := range inputEntries(s) {
		if e.Op == OpTyped {
			typed = append(typed, e)
		}
	}
	if len(typed) != 1 || typed[0].Message != "Decision dr-7 answered: Approve" || typed[0].ByName != "Nate" {
		t.Fatalf("the decision's line: %+v", typed)
	}
}

// A decision's question names its decision to viewers (the attention
// message's decision) and in Info, and only it: the agent's own question
// carries none. A ref that is no decision's (dr-<n>, at most
// proto.MaxDecisionRef bytes) is refused, nothing asked; a refused reply's
// word is the code decision, naming it.
func TestADecisionQuestionNamesItsDecision(t *testing.T) {
	s, _, _ := asking(t, Options{})
	for _, bad := range []string{"", "7", "dr-x", "dr-" + strings.Repeat("1", proto.MaxDecisionRef)} {
		if _, err := s.TryAskDecision(bad, "Ship it?"); err == nil {
			t.Fatalf("asked for %q", bad)
		}
	}
	if a := s.Info().Attention; a.State == AttentionNeedsInput {
		t.Fatalf("a bad ref asked: %+v", a)
	}
	q := askDecision(t, s, "dr-7", "Ship the schema change?")
	b, _ := json.Marshal(s.Info())
	if !strings.Contains(string(b), `"decision":"dr-7"`) {
		t.Fatalf("the info: %s", b)
	}
	if m := attentionMessage(q); m.Decision != "dr-7" || m.ID != q.ID {
		t.Fatalf("the attention message: %+v", m)
	}
	err := s.AnswerInput(context.Background(), ownerOf(t, s), q.ID, []byte("yes\r"))
	var word map[string]any
	if json.Unmarshal(AnswerResult("r1", err)[1:], &word); word["code"] != proto.ErrCodeDecision || word["requestId"] != "r1" || !strings.Contains(word["message"].(string), "dr-7") {
		t.Fatalf("the refusal's word: %v (%v)", word, err)
	}
	s.Ask("Which index?", SourceAPI, KindPrompt, nil)
	if a := s.Info().Attention; a.Decision != "" || attentionMessage(a).Decision != "" {
		t.Fatalf("the agent's own question names a decision: %+v", a)
	}
}
