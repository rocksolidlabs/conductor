package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A question is single-assignment (docs/plans/codex-needs-you.md, "Who may
// answer a question: first response wins"): OPEN, CLAIMED(token) by a
// structured answer's guard, RESOLVED by that answer's last byte written
// (resolve), the claim released when the write does not finish into an
// answer, the question withdrawn when the agent or its screen moves on.
// Keys typed in the terminal resolve nothing. The S numbers are the
// plan's orderings.

// claimState is what the session keeps of a question: the claim standing
// (id and token), the instance consumed, and what shows.
type claimState struct {
	claim    questionClaim
	consumed string
	shown    Attention
}

func stateOf(s *Local) claimState {
	s.mu.Lock()
	defer s.mu.Unlock()
	claim := s.claims[s.info.Attention.ID]
	if claim == (questionClaim{}) {
		for _, c := range s.claims {
			claim = c
			break
		}
	}
	return claimState{claim: claim, consumed: s.consumed, shown: s.info.Attention}
}

// open fails unless the question q shows OPEN: shown, no claim on it, not
// consumed.
func open(t *testing.T, s *Local, q Attention, what string) {
	t.Helper()
	st := stateOf(s)
	if st.shown.ID != q.ID || st.shown.State != AttentionNeedsInput || st.claim.id == q.ID || st.consumed == q.ID {
		t.Fatalf("%s: %+v, want the question %s OPEN", what, st, q.ID)
	}
}

// resolved fails unless the question q was answered by byName, once, and
// shows no more.
func resolved(t *testing.T, s *Local, q Attention, byName, what string) {
	t.Helper()
	info := s.Info()
	if info.Attention.ID == q.ID || info.LastAnswer == nil || info.LastAnswer.ByName != byName || info.LastAnswer.Message != q.Message {
		t.Fatalf("%s: attention %+v, answered %+v; want %s answered by %s", what, info.Attention, info.LastAnswer, q.ID, byName)
	}
	n := 0
	for _, e := range s.Activity() {
		if e.Type == ActivityInput && e.Message == q.Message {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s: %d input entries for the question, want one", what, n)
	}
}

// refusedAs fails unless err refuses a reply with the words.
func refusedAs(t *testing.T, err error, words, what string) {
	t.Helper()
	if !errors.Is(err, ErrQuestionAnswered) || err.Error() != words {
		t.Fatalf("%s: %v, want %q", what, err, words)
	}
}

// controller attaches a controller named name.
func controller(t *testing.T, s *Local, name string) *Subscription {
	t.Helper()
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: name, Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// reported reports an agent's question with two choices and returns it.
func reported(t *testing.T, s *Local, words string) Attention {
	t.Helper()
	if err := s.TrySetAttentionFull(AttentionNeedsInput, words, SourceAPI, KindPrompt, dbChoices); err != nil {
		t.Fatal(err)
	}
	q := s.Info().Attention
	if q.ID == "" || q.Message != words {
		t.Fatalf("reported %q: %+v", words, q)
	}
	return q
}

// S1: one structured answer resolves the question, once: recorded (the
// last answer, an input entry, the chat's answered line naming the
// question) before it is broadcast, and a late or duplicate reply gets the
// recorded result.
func TestOneAnswerResolvesTheQuestion(t *testing.T) {
	s, p, _ := asking(t, Options{})
	sink := newChanSink(false)
	nate, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	jane := controller(t, s, "Jane")
	q := reported(t, s, "Which database?")
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("Postgres\r")); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "Postgres\r" {
		t.Fatalf("written %q", got)
	}
	resolved(t, s, q, "Nate", "the answer")
	a := waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlChat && m["event"] == "answered" })
	if a == nil || a["ref"] != q.ID || a["by"].(map[string]any)["name"] != "Nate" {
		t.Fatalf("the chat's answered line %v", a)
	}
	// The result is recorded before the attention is broadcast: the viewer
	// that sees the empty state can ask for the last answer.
	if m := waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlAttention && m["state"] == "" }); m == nil {
		t.Fatal("the empty state was not broadcast")
	}
	// A duplicate, and a late reply from someone else: the recorded result.
	for _, sub := range []*Subscription{nate, jane} {
		refusedAs(t, s.AnswerInput(context.Background(), sub, q.ID, []byte("SQLite\r")), "this question was already answered by Nate: nothing was sent", sub.Name+"'s late reply")
		_, err := s.Submit(context.Background(), Submission{Text: "SQLite", By: sub, Question: q.ID})
		refusedAs(t, err, "this question was already answered by Nate: nothing was sent", sub.Name+"'s late line")
	}
	nothingWritten(t, p, 100*time.Millisecond)
	if st := stateOf(s); st.claim != (questionClaim{}) || st.consumed != "" {
		t.Fatalf("after the answer %+v", st)
	}
}

// S2: two valid answers at once, many times: exactly one claims under the
// lock, one write is made, one resolution recorded, and the other is
// refused with the result (or as being answered, while the first's keys
// are on their way).
func TestTwoAnswersAtOnceOneWins(t *testing.T) {
	for round := range 30 {
		s, p, _ := asking(t, Options{})
		nate, jane := controller(t, s, "Nate"), controller(t, s, "Jane")
		q := reported(t, s, "Which database?")
		errs := make(chan error, 2)
		start := make(chan struct{})
		for _, c := range []struct {
			sub  *Subscription
			keys string
		}{{nate, "Postgres\r"}, {jane, "SQLite\r"}} {
			go func() {
				<-start
				errs <- s.AnswerInput(context.Background(), c.sub, q.ID, []byte(c.keys))
			}()
		}
		close(start)
		e1, e2 := <-errs, <-errs
		if (e1 == nil) == (e2 == nil) {
			t.Fatalf("round %d: %v and %v, want one answer taken and one refused", round, e1, e2)
		}
		refused := e1
		if refused == nil {
			refused = e2
		}
		if !errors.Is(refused, ErrQuestionAnswered) {
			t.Fatalf("round %d: the other reply: %v", round, refused)
		}
		got := written(t, p)
		info := s.Info()
		if info.LastAnswer == nil || (got == "Postgres\r") != (info.LastAnswer.ByName == "Nate") {
			t.Fatalf("round %d: written %q, answered %+v", round, got, info.LastAnswer)
		}
		resolved(t, s, q, info.LastAnswer.ByName, fmt.Sprintf("round %d", round))
		nothingWritten(t, p, 20*time.Millisecond)
		p.exit()
	}
}

// S3, S19: duplicates and retries are idempotent (a reply sent again after
// its answer_ok was lost, a page's double click): refused with the
// recorded result, nothing written, the result unchanged.
func TestDuplicatesAndRetriesAreIdempotent(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 20 * time.Millisecond})
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	if res, err := s.Submit(context.Background(), Submission{Text: "Postgres", By: nate, Question: q.ID}); err != nil || !res.Entered {
		t.Fatalf("the line: %+v %v", res, err)
	}
	written(t, p)
	written(t, p)
	first := s.Info().LastAnswer
	for i := range 3 {
		refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("Postgres\r")), "this question was already answered by Nate: nothing was sent", fmt.Sprintf("retry %d", i))
	}
	nothingWritten(t, p, 100*time.Millisecond)
	if last := s.Info().LastAnswer; last != first {
		t.Fatalf("the result changed: %+v", last)
	}
	resolved(t, s, q, "Nate", "after the retries")
}

// S4: invalid and unauthorized answers neither win nor close the question:
// a view link's, a revoked link's, one naming no question or another's id,
// and keys that are no choice of a dialog's. The question stays OPEN, and
// a valid answer then resolves it.
func TestInvalidAnswersLeaveTheQuestionOpen(t *testing.T) {
	s, p, _ := asking(t, Options{Questions: approvalQuestions()})
	nate := controller(t, s, "Nate")
	guest, _ := s.AttachWith(AttachOptions{Role: RoleView, Name: "guest"}, newChanSink(false))
	var gone atomic.Bool
	revoked := linkedController(t, s, newChanSink(false), &gone)
	gone.Store(true)
	q := reported(t, s, "Which database?")
	if err := s.AnswerInput(context.Background(), guest, q.ID, []byte("Postgres\r")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a view link's reply: %v", err)
	}
	if err := s.AnswerInput(context.Background(), revoked, q.ID, []byte("Postgres\r")); !errors.Is(err, ErrRevoked) {
		t.Fatalf("a revoked link's reply: %v", err)
	}
	if err := s.AnswerInput(context.Background(), nate, "0123456789abcdef", []byte("Postgres\r")); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a reply naming another id: %v", err)
	}
	nothingWritten(t, p, 100*time.Millisecond)
	// Keys naming no question (an older page's reply) are written as keys
	// and answer nothing.
	if err := s.AnswerInput(context.Background(), nate, "", []byte("x")); err != nil {
		t.Fatalf("keys naming no question: %v", err)
	}
	if got := written(t, p); got != "x" {
		t.Fatalf("written %q", got)
	}
	open(t, s, q, "after the invalid replies")
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("Postgres\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	resolved(t, s, q, "Nate", "the valid answer")
	// A dialog: several keys with an Enter that are none of its choices.
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	d := waitAttention(t, s, asked)
	if err := s.AnswerInput(context.Background(), nate, d.ID, []byte("Postgres\r")); !errors.Is(err, ErrQuestion) {
		t.Fatalf("a line's keys at a dialog: %v", err)
	}
	nothingWritten(t, p, 100*time.Millisecond)
	open(t, s, d, "the dialog after the refused keys")
}

// S5, S9, S28: no timeout ends a question. With nobody answering, every
// deadline passes and it stays OPEN. With the program not reading, a
// reply's keys begin and do not land within InputWait: the claim stands
// (a second reply is refused as being answered), the reply's word waits,
// and once the program reads the question resolves and the word comes.
func TestTimeNeverExpiresAQuestion(t *testing.T) {
	s, p, _ := asking(t, Options{InputWait: 50 * time.Millisecond})
	nate, jane := controller(t, s, "Nate"), controller(t, s, "Jane")
	q := reported(t, s, "Which database?")
	time.Sleep(300 * time.Millisecond)
	open(t, s, q, "after every deadline")

	release := blockFirstWrite(p)
	defer release()
	word := make(chan error, 1)
	err := s.AnswerInputFinished(context.Background(), nate, q.ID, []byte("Postgres\r"), func(err error) { word <- err })
	if !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("a reply the program is slow to take: %v", err)
	}
	if st := stateOf(s); st.claim.id != q.ID || st.shown.ID != q.ID {
		t.Fatalf("while the keys are on their way: %+v", st)
	}
	refusedAs(t, s.AnswerInput(context.Background(), jane, q.ID, []byte("SQLite\r")), QuestionClaimedWords, "a second reply meanwhile")
	select {
	case err := <-word:
		t.Fatalf("a word before the keys landed: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	if got := written(t, p); got != "Postgres\r" {
		t.Fatalf("written %q", got)
	}
	if err := returnsWithin(t, 5*time.Second, "the word", func() error { return <-word }); err != nil {
		t.Fatalf("the word once the keys landed: %v", err)
	}
	resolved(t, s, q, "Nate", "the late keys")
}

// S6, S7: viewers detaching, every one of them, leave the question OPEN;
// one that attaches then sees it with its id and answers it. A reply
// refused before its write began (its link gone as the writer takes it)
// releases its claim, and the next reply claims with a new token.
func TestDisconnectionsLeaveAQuestionOpen(t *testing.T) {
	s, p, _ := asking(t, Options{})
	nate, jane := controller(t, s, "Nate"), controller(t, s, "Jane")
	q := reported(t, s, "Which database?")
	s.Detach(nate)
	s.Detach(jane)
	time.Sleep(100 * time.Millisecond)
	open(t, s, q, "with nobody attached")

	// A reply whose link goes as the writer takes it: refused, released.
	hold, takeAgain := gateWrites(t, p)
	var gone atomic.Bool
	linked := linkedController(t, s, newChanSink(false), &gone)
	other := controller(t, s, "Other")
	hold()
	go s.Input(other, []byte("\x1b[3;1R"))
	untilWaiting(t, s, 0)
	reply := make(chan error, 1)
	go func() { reply <- s.AnswerInput(context.Background(), linked, q.ID, []byte("Postgres\r")) }()
	untilWaiting(t, s, 1)
	gone.Store(true)
	takeAgain()
	written(t, p)
	if err := returnsWithin(t, 5*time.Second, "the linked reply", func() error { return <-reply }); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the reply whose link went: %v", err)
	}
	nothingWritten(t, p, 100*time.Millisecond)
	open(t, s, q, "after the refused reply")

	sink := newChanSink(false)
	late, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Late", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if m := waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlAttention && m["state"] == "needs_input" }); m == nil || m["id"] != q.ID {
		t.Fatalf("the late viewer was shown %v", m)
	}
	if err := s.AnswerInput(context.Background(), late, q.ID, []byte("SQLite\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	resolved(t, s, q, "Late", "the late viewer's answer")
}

// S6 and S19 on a reconnection: a viewer that attaches after the result
// sees the empty state and, in the kept chat, the question and its
// answered line; its retry naming the question gets the recorded result.
func TestAttachingAfterTheResultShowsIt(t *testing.T) {
	s, p, _ := asking(t, Options{})
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("Postgres\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	sink := newChanSink(false)
	back, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Back", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlReady }) == nil {
		t.Fatal("the viewer back never got ready")
	}
	for i := range sink.count() {
		if m := decodeControl(t, sink.frame(i)); m["t"] == proto.CtlAttention && m["state"] == "needs_input" {
			t.Fatalf("the viewer back was shown the question: %v", m)
		}
	}
	var question, answered bool
	for _, m := range s.ChatHistory() {
		question = question || m.Kind == proto.ChatKindQuestion && m.ID == q.ID
		answered = answered || m.Event == "answered" && m.Ref == q.ID && m.By.Name == "Nate"
	}
	if !question || !answered {
		t.Fatalf("the kept chat: question %v, answered %v", question, answered)
	}
	refusedAs(t, s.AnswerInput(context.Background(), back, q.ID, []byte("SQLite\r")), "this question was already answered by Nate: nothing was sent", "a retry after the result")
	nothingWritten(t, p, 100*time.Millisecond)
}

// S20: questions are isolated by id, across sessions and in one: an
// answer naming another session's question, or the question before this
// one, is refused and writes nothing.
func TestQuestionsAreIsolatedByID(t *testing.T) {
	a, pa, _ := asking(t, Options{})
	b, pb, _ := asking(t, Options{})
	nate, jane := controller(t, a, "Nate"), controller(t, b, "Jane")
	qa, qb := reported(t, a, "Which database?"), reported(t, b, "Which port?")
	if err := b.AnswerInput(context.Background(), jane, qa.ID, []byte("Postgres\r")); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a reply in b naming a's question: %v", err)
	}
	nothingWritten(t, pb, 100*time.Millisecond)
	open(t, b, qb, "b's question")
	if err := a.AnswerInput(context.Background(), nate, qa.ID, []byte("Postgres\r")); err != nil {
		t.Fatal(err)
	}
	written(t, pa)
	// The next question in a: a reply naming the first gets its result.
	next := reported(t, a, "Which file?")
	refusedAs(t, a.AnswerInput(context.Background(), nate, qa.ID, []byte("Postgres\r")), "this question was already answered by Nate: nothing was sent", "the first question's id")
	nothingWritten(t, pa, 100*time.Millisecond)
	open(t, a, next, "the next question")
}

// S15, S18: cancellation (the agent's clear, its next state) races safely
// with an answer, many times: the answer resolves the question or the
// withdrawal wins and the answer resolves nothing; never both, never a
// result after a withdrawal, and the question never shows after either.
func TestCancellationRacesSafelyWithAnAnswer(t *testing.T) {
	for round := range 40 {
		s, p, _ := asking(t, Options{})
		nate := controller(t, s, "Nate")
		q := reported(t, s, "Which database?")
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var replyErr error
		go func() {
			defer wg.Done()
			<-start
			replyErr = s.AnswerInput(context.Background(), nate, q.ID, []byte("Postgres\r"))
		}()
		go func() {
			defer wg.Done()
			<-start
			if round%2 == 0 {
				s.SetAttentionFull(AttentionNone, "", SourceAPI, "", nil)
			} else {
				s.SetAttentionFull(AttentionWorking, "", SourceAPI, "", nil)
			}
		}()
		close(start)
		wg.Wait()
		info := s.Info()
		st := stateOf(s)
		if info.Attention.ID == q.ID || st.claim != (questionClaim{}) {
			t.Fatalf("round %d: the question shows after the race: %+v %+v", round, info.Attention, st)
		}
		if replyErr == nil {
			written(t, p)
		} else if !errors.Is(replyErr, ErrQuestionAnswered) {
			t.Fatalf("round %d: the reply: %v", round, replyErr)
		}
		n := 0
		for _, e := range s.Activity() {
			if e.Type == ActivityInput {
				n++
			}
		}
		if (info.LastAnswer != nil) != (n == 1) || n > 1 {
			t.Fatalf("round %d: answered %+v, %d input entries", round, info.LastAnswer, n)
		}
		p.exit()
	}
}

// S8: a revoke before Enter. A reply's text typed and its link let go
// before its Enter: its claim released, the turn held, the question held
// over (a reply naming it refused meanwhile). Ctrl+U brings it back OPEN,
// with the same id; a new reply claims it, with a new token, and resolves
// it; the first reply resolved nothing.
func TestARevokeBeforeEnterReleasesTheClaim(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	done := submitAsync(s, Submission{Text: "Postgres", By: guest, Question: q.ID})
	if got := written(t, p); got != "Postgres" {
		t.Fatalf("written %q", got)
	}
	untilIdle(t, s)
	before := stateOf(s)
	if before.claim.id != q.ID || before.claim.token == 0 {
		t.Fatalf("the text typed: %+v", before)
	}
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	got := returnsWithin(t, 5*time.Second, "the guest's line", func() submitted { return <-done })
	if !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
		t.Fatalf("the guest's line: %+v, %v", got.res, got.err)
	}
	heldShows(t, s)
	if st := stateOf(s); st.claim != (questionClaim{}) {
		t.Fatalf("the claim stands after the release: %+v", st)
	}
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a reply while the question is held over: %v", err)
	}
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	open(t, s, q, "once Ctrl+U brought it back")
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	resolved(t, s, q, "Nate", "the new reply")
	// Tokens are fresh: the new claim's came after the first's.
	s.mu.Lock()
	seq := s.claimSeq
	s.mu.Unlock()
	if seq <= before.claim.token {
		t.Fatalf("claim tokens %d, then %d", before.claim.token, seq)
	}
}

// S14: a done during a hold. The agent's own question, held over by the
// hold of the turn: the turn's done is recorded, the question stays held
// over (its claim released with the stranded line), and keys that end the
// hold without answering it show it again OPEN. (A question the agent
// reported without the mark is withdrawn by its done, as ever.)
func TestADoneDuringAHoldKeepsTheQuestionHeldOver(t *testing.T) {
	s, p, log := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := askOwn(t, s, "Which database?", dbChoices...)
	done := submitAsync(s, Submission{Text: "Postgres", By: guest, Question: q.ID})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	<-done
	heldShows(t, s)
	// The hold's own entry first, so that it is not counted as one under
	// the hold.
	logged(t, log, heldEntry)
	n := log.len()
	reportState(t, s, AttentionDone, "finished")
	heldShows(t, s)
	if got := log.since(n); len(got) != 1 || got[0] != "done finished" {
		t.Fatalf("entries under the hold %q", got)
	}
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	waitAttention(t, s, func(a Attention) bool { return a.ID == q.ID })
	open(t, s, q, "once the hold ended")
}

// S30: raw keys, and a hold's end, between a structured answer's last byte
// and its resolve change nothing of it: the question is CLAIMED, the keys
// consume nothing and withdraw nothing, and the claimer's token resolves
// it, once. Here a bell's wait, which an Enter typed would otherwise
// withdraw.
func TestRawKeysBetweenAClaimsWriteAndItsResolveChangeNothing(t *testing.T) {
	s, p, _ := asking(t, Options{})
	nate, jane := controller(t, s, "Nate"), controller(t, s, "Jane")
	out(p, "\a")
	q := waitAttention(t, s, isBell)
	reached, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	pause := func(data []byte) {
		if string(data) == "y\r" {
			once.Do(func() { close(reached); <-gate })
		}
	}
	s.beforeWritten.Store(&pause)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	reply := make(chan error, 1)
	go func() { reply <- s.AnswerInput(context.Background(), nate, q.ID, []byte("y\r")) }()
	if got := written(t, p); got != "y\r" {
		t.Fatalf("written %q", got)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the reply's keys never came to what follows them")
	}
	// Another viewer's Enter, typed in the terminal, while the claim stands.
	if err := s.Input(jane, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	if st := stateOf(s); st.shown.ID != q.ID || st.claim.id != q.ID || st.consumed != "" {
		t.Fatalf("raw keys changed a claimed question: %+v", st)
	}
	close(gate)
	if err := returnsWithin(t, 5*time.Second, "the reply", func() error { return <-reply }); err != nil {
		t.Fatal(err)
	}
	resolved(t, s, q, "Nate", "the claimer's resolve")
}

// S22, the screen snapshot: a bell's wait, with no words of the agent's,
// is withdrawn once the screen has moved on from what it showed at the
// first quiet after the bell: a spinner, a countdown or a status line
// changing a few cells of a row or two leave it; the agent printing
// output, or redrawing its prompt area, withdraws it. A claimed wait is
// never withdrawn by the screen; a resize takes the baseline again.
func TestABellWaitIsWithdrawnOnceTheScreenMovesOn(t *testing.T) {
	s, p, _ := asking(t, Options{})
	nate := controller(t, s, "Nate")
	// The prompt the agent rang for, drawn with the bell, and its status
	// row with a spinner and a countdown.
	out(p, "\x1b[2J\x1b[H$ run the tests\r\nAllow? [y/n]\a\r\n\x1b[24;1H⠋ Waiting (0:30)")
	q := waitAttention(t, s, isBell)
	time.Sleep(2 * patternQuiet)
	// Frames: the spinner and the countdown change a few cells.
	for i := range 6 {
		out(p, fmt.Sprintf("\x1b[24;1H%s Waiting (0:%02d)", []string{"⠙", "⠹", "⠸", "⠼", "⠴", "⠦"}[i], 29-i))
		time.Sleep(2 * patternQuiet)
	}
	// A status line's words change wholesale: one row.
	out(p, "\x1b[23;1H\x1b[KRunning the tests in the background")
	time.Sleep(2 * patternQuiet)
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("a spinner, a countdown or a status line withdrew the wait: %+v", a)
	}
	// A reply claims it: the agent printing meanwhile withdraws nothing.
	hold, takeAgain := gateWrites(t, p)
	hold()
	word := make(chan error, 1)
	go func() { word <- s.AnswerInput(context.Background(), nate, q.ID, []byte("y\r")) }()
	untilWaiting(t, s, 0)
	out(p, "\x1b[2J\x1b[Hone\r\ntwo\r\nthree\r\nfour\r\n")
	time.Sleep(2 * patternQuiet)
	if st := stateOf(s); st.shown.ID != q.ID || st.claim.id != q.ID {
		t.Fatalf("the screen withdrew a claimed wait: %+v", st)
	}
	takeAgain()
	written(t, p)
	if err := returnsWithin(t, 5*time.Second, "the reply", func() error { return <-word }); err != nil {
		t.Fatal(err)
	}
	resolved(t, s, q, "Nate", "the reply")

	// Another bell, OPEN: a resize drops the baseline, taken again at the
	// next quiet; then the agent prints output.
	out(p, "\x1b[2J\x1b[HAllow? [y/n]\a")
	q2 := waitAttention(t, s, isBell)
	time.Sleep(2 * patternQuiet)
	if err := s.Resize(nate, 100, 30); err != nil {
		t.Fatal(err)
	}
	out(p, "\x1b[2J\x1b[HAllow? [y/n]\r\n(redrawn at the new size)")
	time.Sleep(2 * patternQuiet)
	if a := s.Info().Attention; a.ID != q2.ID {
		t.Fatalf("the redraw after a resize withdrew the wait: %+v", a)
	}
	out(p, "\r\nmoving on: one\r\nmoving on: two\r\nmoving on: three\r\nmoving on: four\r\n")
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen || s.Info().LastAnswer.Message != q.Message {
		t.Fatalf("the wait withdrawn: %+v, answered %+v", a, s.Info().LastAnswer)
	}
	// A program that never erases its screen (a shell, a line reader),
	// resized with something on it and drawing nothing for the resize: the
	// baseline is taken at the quiet after the resize, its rows as they
	// are, and the lines it prints next withdraw the wait.
	out(p, "\x1b[2J\x1b[HAllow? [y/n]\a")
	q4 := waitAttention(t, s, isBell)
	time.Sleep(2 * patternQuiet)
	if err := s.Resize(nate, 120, 40); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * patternQuiet)
	if a := s.Info().Attention; a.ID != q4.ID {
		t.Fatalf("the resize withdrew the wait: %+v", a)
	}
	out(p, "\r\nmoving on: one\r\nmoving on: two\r\nmoving on: three\r\nmoving on: four\r\n")
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen {
		t.Fatalf("the lines after a resize never redrawn for: %+v", a)
	}
	// Scrolling output: every row shifts.
	out(p, "\x1b[2J\x1b[HAllow? [y/n]\a")
	q3 := waitAttention(t, s, isBell)
	time.Sleep(2 * patternQuiet)
	out(p, strings.Repeat("a line of output\r\n", 40))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen || a.ID == q3.ID {
		t.Fatalf("scrolling output: %+v", a)
	}
}

// Every path that ends a claim or a question, through a release and a
// reclaim: each row pauses a path at its decision point, where possible,
// while the question is released and claimed again (or raw keys reach it),
// then resumes it. The paused path has no effect on the new claim, which
// resolves once; a withdrawal takes the new claim with the question, which
// then resolves nothing. The state (id, claim, consumed, shown or hidden)
// is checked at every step, so a path that drops or hides a question by
// another route fails.
func TestEveryClearingPathThroughReleaseAndReclaim(t *testing.T) {
	t.Run("resolve: a choice's keys, a line's Enter, an Enter-only reply, with raw keys and a hold's end meanwhile", func(t *testing.T) {
		for _, how := range []string{"keys", "line", "enter"} {
			t.Run(how, func(t *testing.T) {
				s, p, _ := asking(t, Options{SubmitPause: 50 * time.Millisecond})
				nate, jane := controller(t, s, "Nate"), controller(t, s, "Jane")
				q := reported(t, s, "Which database?")
				var reply <-chan submitted
				var keysReply chan error
				var release func()
				switch how {
				case "keys":
					reached, gate := make(chan struct{}), make(chan struct{})
					var once sync.Once
					pause := func(data []byte) {
						if string(data) == "Postgres\r" {
							once.Do(func() { close(reached); <-gate })
						}
					}
					s.beforeWritten.Store(&pause)
					keysReply = make(chan error, 1)
					go func() { keysReply <- s.AnswerInput(context.Background(), nate, q.ID, []byte("Postgres\r")) }()
					written(t, p)
					<-reached
					release = func() { close(gate) }
				case "line", "enter":
					at, go_ := pauseEnter(t, s)
					text := "Postgres"
					if how == "enter" {
						text = ""
					}
					reply = submitAsync(s, Submission{Text: text, By: nate, Question: q.ID})
					if how == "line" {
						written(t, p)
					}
					atEnter(t, at)
					release = go_
				}
				claimed := stateOf(s)
				if claimed.claim.id != q.ID && how != "enter" {
					t.Fatalf("%s: not claimed: %+v", how, claimed)
				}
				// Meanwhile: another viewer's Enter, a line naming no
				// question, and a reply (refused) reach the question.
				if err := s.Input(jane, []byte("\r")); err != nil {
					t.Fatal(err)
				}
				written(t, p)
				if how != "enter" {
					refusedAs(t, s.AnswerInput(context.Background(), jane, q.ID, []byte("SQLite\r")), QuestionClaimedWords, how+": a reply meanwhile")
					if st := stateOf(s); st.claim != claimed.claim || st.consumed != claimed.consumed || st.shown.ID != claimed.shown.ID {
						t.Fatalf("%s: raw keys changed the claimed question: %+v, was %+v", how, st, claimed)
					}
				}
				release()
				if how == "keys" {
					if err := returnsWithin(t, 5*time.Second, "the keys", func() error { return <-keysReply }); err != nil {
						t.Fatal(err)
					}
				} else {
					got := returnsWithin(t, 5*time.Second, "the reply", func() submitted { return <-reply })
					if got.err != nil || !got.res.Entered {
						t.Fatalf("%s: %+v %v", how, got.res, got.err)
					}
					written(t, p)
				}
				resolved(t, s, q, "Nate", how)
			})
		}
	})

	t.Run("release: a refusal before the text, a revoke before the Enter, an Enter left out, a stall; then a reclaim", func(t *testing.T) {
		for _, how := range []string{"refused before the text", "revoked before the Enter", "the Enter left out"} {
			t.Run(how, func(t *testing.T) {
				opts := Options{SubmitPause: 300 * time.Millisecond}
				if how == "the Enter left out" {
					opts.Questions = startupQuestions()
				}
				s, p, _ := asking(t, opts)
				var gone atomic.Bool
				guest := linkedController(t, s, newChanSink(false), &gone)
				nate := controller(t, s, "Nate")
				q := reported(t, s, "Which database?")
				switch how {
				case "refused before the text":
					hold, takeAgain := gateWrites(t, p)
					hold()
					go s.Input(nate, []byte("\x1b[3;1R"))
					untilWaiting(t, s, 0)
					line := submitAsync(s, Submission{Text: "Postgres", By: guest, Question: q.ID})
					untilWaiting(t, s, 1)
					gone.Store(true)
					takeAgain()
					written(t, p)
					if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) || got.res.Typed {
						t.Fatalf("%s: %+v %v", how, got.res, got.err)
					}
					open(t, s, q, how)
				case "revoked before the Enter", "the Enter left out":
					line := submitAsync(s, Submission{Text: "Postgres", By: guest, Question: q.ID})
					written(t, p)
					untilIdle(t, s)
					if how == "revoked before the Enter" {
						gone.Store(true)
						s.DisconnectLink(guest.LinkID)
					} else {
						p.outW.Write([]byte(codexBackgroundScreen))
					}
					got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line })
					if !got.res.Typed || got.res.Entered || !(errors.Is(got.err, ErrRevoked) || errors.Is(got.err, ErrLineStranded)) {
						t.Fatalf("%s: %+v %v", how, got.res, got.err)
					}
					// The hold of the turn shows over the question, held over,
					// and the claim is let go in that step.
					heldShows(t, s)
					if st := stateOf(s); st.claim != (questionClaim{}) {
						t.Fatalf("%s: the claim stands: %+v", how, st)
					}
					if how == "the Enter left out" {
						// The startup question on the screen under the hold
						// is answered in the terminal (the first keys typed
						// there end the hold too), and the screen moves on.
						if err := s.Input(nate, []byte("1")); err != nil {
							t.Fatal(err)
						}
						written(t, p)
						p.outW.Write([]byte("\x1b[2J\x1b[H\x1b[2;3H› Ask Codex to do anything"))
						// The screen as the session has it shows the program
						// moved on (a reply looks at it: stillOnScreen).
						if !waitFor(5*time.Second, func() bool {
							text, settled, _, _ := s.asked.Whole()
							return settled && strings.Contains(text, "Ask Codex")
						}) {
							t.Fatal("the screen never showed the program moved on")
						}
					} else {
						// Ctrl+U ends the hold: the question back, OPEN.
						if err := s.Input(nate, []byte{0x15}); err != nil {
							t.Fatal(err)
						}
						written(t, p)
					}
					open(t, s, q, how+", once the hold ended")
				}
				if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")); err != nil {
					t.Fatalf("%s: the reclaim: %v", how, err)
				}
				written(t, p)
				resolved(t, s, q, "Nate", how+", the reclaim")
			})
		}
	})

	t.Run("consume: raw keys at a dialog, the hold's end with an Enter, a redraw after a resolution", func(t *testing.T) {
		t.Run("raw keys at a dialog", func(t *testing.T) {
			s, p, _ := asking(t, Options{Questions: approvalQuestions()})
			nate := controller(t, s, "Nate")
			p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
			d := waitAttention(t, s, asked)
			if err := s.Input(nate, []byte("y")); err != nil {
				t.Fatal(err)
			}
			written(t, p)
			if st := stateOf(s); st.consumed != d.ID || st.shown.ID != d.ID || st.claim != (questionClaim{}) {
				t.Fatalf("after the key: %+v", st)
			}
			refusedAs(t, s.AnswerInput(context.Background(), nate, d.ID, []byte("p")), QuestionConsumedWords, "a reply after the key")
			p.outW.Write([]byte(approvalGone("✔ You approved codex to run curl -sI https://example.com this time\r\n› ")))
			if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen || s.Info().LastAnswer != nil {
				t.Fatalf("withdrawn: %+v %+v", a, s.Info().LastAnswer)
			}
			// The next dialog is a question of its own: a reply resolves it.
			p.outW.Write([]byte(approvalDialog("rm -rf build")))
			next := waitAttention(t, s, asked)
			if err := s.AnswerInput(context.Background(), nate, next.ID, []byte("y")); err != nil {
				t.Fatal(err)
			}
			written(t, p)
			resolved(t, s, next, "Nate", "the next dialog")
		})
		t.Run("the hold's end with an Enter", func(t *testing.T) {
			s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
			var gone atomic.Bool
			guest := linkedController(t, s, newChanSink(false), &gone)
			nate := controller(t, s, "Nate")
			q := reported(t, s, "Which database?")
			line := submitAsync(s, Submission{Text: "Postgres", By: guest, Question: q.ID})
			written(t, p)
			untilIdle(t, s)
			gone.Store(true)
			s.DisconnectLink(guest.LinkID)
			<-line
			heldShows(t, s)
			if err := s.Input(nate, []byte("\r")); err != nil {
				t.Fatal(err)
			}
			written(t, p)
			if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID || st.claim != (questionClaim{}) || s.Held() {
				t.Fatalf("after the Enter entered the held text: %+v", st)
			}
			refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), QuestionConsumedWords, "a reply after the text was entered")
			// The agent moves on: the next question, resolved by a reply.
			reportState(t, s, AttentionWorking, "")
			next := reported(t, s, "Which port?")
			if err := s.AnswerInput(context.Background(), nate, next.ID, []byte("5432\r")); err != nil {
				t.Fatal(err)
			}
			written(t, p)
			resolved(t, s, next, "Nate", "the next question")
		})
		t.Run("a redraw after a resolution", func(t *testing.T) {
			s, p, _ := asking(t, Options{Questions: approvalQuestions()})
			nate := controller(t, s, "Nate")
			p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
			d := waitAttention(t, s, asked)
			if err := s.AnswerInput(context.Background(), nate, d.ID, []byte("y")); err != nil {
				t.Fatal(err)
			}
			written(t, p)
			resolved(t, s, d, "Nate", "the dialog")
			// Drawn again before the program drew what follows: a new
			// instance, consumed, no second answer taken.
			p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
			again := waitAttention(t, s, asked)
			if again.ID == d.ID {
				t.Fatalf("the same instance asked again: %+v", again)
			}
			refusedAs(t, s.AnswerInput(context.Background(), nate, again.ID, []byte("y")), QuestionConsumedWords, "a reply to the redraw")
			nothingWritten(t, p, 100*time.Millisecond)
		})
	})

	t.Run("withdraw: the agent's clear, its next question, working in a new turn, the session's end", func(t *testing.T) {
		for _, how := range []string{"clear", "next question", "working", "ended"} {
			t.Run(how, func(t *testing.T) {
				s, p, _ := asking(t, Options{SubmitPause: 50 * time.Millisecond})
				nate := controller(t, s, "Nate")
				q := reported(t, s, "Which database?")
				// A reply claims it, paused at its Enter (its text typed).
				at, release := pauseEnter(t, s)
				line := submitAsync(s, Submission{Text: "Postgres", By: nate, Question: q.ID})
				written(t, p)
				atEnter(t, at)
				if st := stateOf(s); st.claim.id != q.ID {
					t.Fatalf("%s: not claimed: %+v", how, st)
				}
				// The agent moves on.
				switch how {
				case "clear":
					reportState(t, s, AttentionNone, "")
				case "next question":
					reported(t, s, "Which port?")
				case "working":
					reportState(t, s, AttentionDone, "asked")
					reportState(t, s, AttentionWorking, "")
				case "ended":
					p.exit()
					<-s.Ended()
				}
				if st := stateOf(s); st.shown.ID == q.ID || st.claim.id == q.ID {
					t.Fatalf("%s: the question stands: %+v", how, st)
				}
				release()
				switch how {
				case "ended":
					if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrSessionEnded) {
						t.Fatalf("%s: %+v %v", how, got.res, got.err)
					}
				case "next question":
					// A question came in the line's pause: its Enter is left
					// out, the line stranded, the turn held; the next
					// question stands, and once Ctrl+U ends the hold a reply
					// resolves it.
					got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line })
					if !errors.Is(got.err, ErrLineStranded) || !got.res.Typed || got.res.Entered {
						t.Fatalf("%s: %+v %v", how, got.res, got.err)
					}
					heldShows(t, s)
					if err := s.Input(nate, []byte{0x15}); err != nil {
						t.Fatal(err)
					}
					written(t, p)
					next := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNeedsInput && a.ID != q.ID })
					if err := s.AnswerInput(context.Background(), nate, next.ID, []byte("5432\r")); err != nil {
						t.Fatal(err)
					}
					written(t, p)
					resolved(t, s, next, "Nate", how)
				default:
					// The Enter is written (the text is not left typed
					// without it) and resolves nothing.
					got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line })
					if got.err != nil || !got.res.Entered {
						t.Fatalf("%s: %+v %v", how, got.res, got.err)
					}
					written(t, p)
				}
				if how != "next question" && s.Info().LastAnswer != nil {
					t.Fatalf("%s: a withdrawn question was answered: %+v", how, s.Info().LastAnswer)
				}
			})
		}
	})

	// A dialog on the screen is withdrawn by the screen showing it gone
	// only while nobody answers it: a reply's keys claim it as they are
	// written, and the program takes them and removes the dialog before
	// the write's word comes (S25); the claim resolves it, and the screen
	// does not withdraw it under the claim.
	t.Run("withdraw: a dialog gone as a reply's keys are written is resolved by them", func(t *testing.T) {
		s, p, _ := asking(t, Options{SubmitPause: 50 * time.Millisecond, Questions: approvalQuestions()})
		nate := controller(t, s, "Nate")
		p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
		q := waitAttention(t, s, asked)
		// The keys written, paused before their word: the claim stands.
		reached, gate := make(chan struct{}), make(chan struct{})
		var once sync.Once
		pause := func(data []byte) {
			if string(data) == "y" {
				once.Do(func() { close(reached); <-gate })
			}
		}
		s.beforeWritten.Store(&pause)
		keys := make(chan error, 1)
		go func() { keys <- s.AnswerInput(context.Background(), nate, q.ID, []byte("y")) }()
		if got := written(t, p); got != "y" {
			t.Fatalf("written %q", got)
		}
		<-reached
		if st := stateOf(s); st.claim.id != q.ID {
			t.Fatalf("not claimed: %+v", st)
		}
		// The program took them: the dialog is gone from the screen, and
		// the screen, looked at settled, leaves the question to its claim.
		looked, _, _ := s.asked.Look()
		p.outW.Write([]byte(approvalGone("■ Conversation interrupted\r\n› ")))
		if !waitFor(5*time.Second, func() bool {
			now, settled, _ := s.asked.Look()
			text, _, _, _ := s.asked.Whole()
			return now > looked && settled && !strings.Contains(text, "Would you like")
		}) {
			t.Fatal("the screen was not looked at settled with the dialog gone")
		}
		if st := stateOf(s); st.shown.ID != q.ID || st.claim.id != q.ID {
			t.Fatalf("withdrawn under its claim: %+v", st)
		}
		close(gate)
		if err := returnsWithin(t, 5*time.Second, "the keys", func() error { return <-keys }); err != nil {
			t.Fatalf("the keys' word: %v", err)
		}
		resolved(t, s, q, "Nate", "the keys written")
	})
}

// resolve is the only transition that records an answer: every other path
// through the session leaves LastAnswer as it was. The functions that end
// a question are the ones the plan names, and no other sets the result.
func TestOnlyResolveRecordsAnAnswer(t *testing.T) {
	src := strings.Join(sourceOf(t, "local.go", "submit.go"), "\n")
	n := strings.Count(src, "s.info.LastAnswer = ")
	if n != 1 {
		t.Fatalf("LastAnswer is set in %d places, want one (resolve)", n)
	}
	if !strings.Contains(src, "func (s *Local) resolve(") || strings.Contains(src, "func (s *Local) answer(") {
		t.Fatal("the one transition is resolve; answer by stamp is gone")
	}
	for _, gone := range []string{"answerReached", "answerWhen", "setAnsweredLocked", "noteAnswering", "noteTyped", "markAnswered"} {
		if strings.Contains(src, "func (s *Local) "+gone+"(") {
			t.Errorf("%s is still there", gone)
		}
	}
	if strings.Contains(src, "typedAtAsked") || strings.Contains(src, "startupSince") {
		t.Error("a key's answer by stamp is still there")
	}
}

// A question read off the screen, resolved by a structured answer: the
// screen's redraw of the same words before the program moves on is
// consumed, and once the program has moved on it is asked anew only when
// drawn anew. The claim's pattern words stay the question's.
func TestAResolvedScreenQuestionIsNotAskedTwice(t *testing.T) {
	s, p, _ := asking(t, Options{Questions: []StartupQuestion{{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Yes", Input: "\r"}}}}})
	nate := controller(t, s, "Nate")
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Yes"))
	q := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	resolved(t, s, q, "Nate", "the trust question")
	// Redrawn before the program moved on: consumed.
	p.outW.Write([]byte("\x1b[2J\x1b[HTrust this folder?\r\n› 1. Yes"))
	again := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	refusedAs(t, s.AnswerInput(context.Background(), nate, again.ID, []byte("\r")), QuestionConsumedWords, "a reply to the redraw")
	p.outW.Write([]byte("\x1b[2J\x1b[H> "))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
}

// sourceOf is the text of the package's files named, read from the
// package directory (go test's working directory).
func sourceOf(t *testing.T, names ...string) []string {
	t.Helper()
	var out []string
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
	}
	return out
}

// Round 7, finding 1: a reply's line typed and its Enter not written for
// good (its link revoked) keeps its claim until the hold of the turn shows
// in the question's place, in one step: a reply that comes between is
// refused as being answered, never written after the text. The hold's end
// shows the question OPEN, and a new reply claims and resolves it.
func TestAStrandedLinesClaimGoesAsTheHoldShows(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	// The hold paused as it is installed, before its words show: the
	// window in which the claim used to be let go already.
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "Postgres", By: guest, Question: q.ID})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	// The claim stands: a reply now is refused, nothing written.
	if st := stateOf(s); st.claim.id != q.ID || st.shown.ID != q.ID {
		t.Fatalf("under the hold's installation: %+v", st)
	}
	refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), QuestionClaimedWords, "a reply as the hold is installed")
	nothingWritten(t, p, 100*time.Millisecond)
	let.Do(func() { close(gate) })
	got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line })
	if !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	heldShows(t, s)
	if st := stateOf(s); st.claim != (questionClaim{}) {
		t.Fatalf("the claim stands under the hold: %+v", st)
	}
	// Ctrl+U ends the hold: the question OPEN, and a reply resolves it.
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	open(t, s, q, "once the hold ended")
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "SQLite\r" {
		t.Fatalf("written %q", got)
	}
	resolved(t, s, q, "Nate", "the reclaim")
}

// Round 7, finding 2: a reply's Enter whose write began and whose time ran
// out (the program not reading) keeps the claim until the Enter lands: no
// answer is recorded and no word given before, another reply is refused
// as being answered meanwhile; once it lands the question is resolved and
// the word is answer_ok. A session that ends under it never records an
// answer, and the word says so.
func TestAPendingEnterKeepsTheClaimUntilItLands(t *testing.T) {
	for _, how := range []string{"lands", "the session ends"} {
		t.Run(how, func(t *testing.T) {
			s, p, _ := asking(t, Options{SubmitPause: 50 * time.Millisecond, InputWait: 200 * time.Millisecond})
			nate := controller(t, s, "Nate")
			other := controller(t, s, "Other")
			q := reported(t, s, "Which database?")
			hold, takeAgain := gateWrites(t, p)
			word := make(chan submitted, 1)
			line := submitAsync(s, Submission{Text: "Postgres", By: nate, Question: q.ID, Finished: func(res SubmitResult, err error) { word <- submitted{res, err} }})
			if got := written(t, p); got != "Postgres" {
				t.Fatalf("written %q", got)
			}
			// The program stops reading: the Enter's write begins and its
			// time runs out.
			hold()
			got := returnsWithin(t, 5*time.Second, "the submission", func() submitted { return <-line })
			if !errors.Is(got.err, ErrSubmitPending) || !got.res.Typed {
				t.Fatalf("the submission: %+v %v", got.res, got.err)
			}
			if st := stateOf(s); st.claim.id != q.ID || st.shown.ID != q.ID {
				t.Fatalf("with the Enter pending: %+v", st)
			}
			if s.Info().LastAnswer != nil {
				t.Fatalf("answered before the Enter landed: %+v", s.Info().LastAnswer)
			}
			refusedAs(t, s.AnswerInput(context.Background(), other, q.ID, []byte("SQLite\r")), QuestionClaimedWords, "a reply while the Enter is pending")
			select {
			case w := <-word:
				t.Fatalf("a word before the Enter landed: %+v %v", w.res, w.err)
			case <-time.After(100 * time.Millisecond):
			}
			if how == "lands" {
				takeAgain()
				if got := written(t, p); got != "\r" {
					t.Fatalf("written %q", got)
				}
				w := returnsWithin(t, 5*time.Second, "the word", func() submitted { return <-word })
				if w.err != nil || !w.res.Entered {
					t.Fatalf("the word: %+v %v", w.res, w.err)
				}
				resolved(t, s, q, "Nate", "the Enter landed")
				return
			}
			p.exit()
			<-s.Ended()
			w := returnsWithin(t, 5*time.Second, "the word", func() submitted { return <-word })
			if !errors.Is(w.err, ErrSessionEnded) || w.res.Entered {
				t.Fatalf("the word: %+v %v", w.res, w.err)
			}
			if s.Info().LastAnswer != nil {
				t.Fatalf("an Enter that never landed was an answer: %+v", s.Info().LastAnswer)
			}
		})
	}
}

// Round 7, finding 3: the agent's own question, held over by the hold of
// the turn and shown again consumed by the Enter that entered the held
// text, is reached: the agent's work in that turn withdraws it rather than
// setting it aside, so its done does not show it again OPEN for a reply
// the program would take as a second answer.
func TestAConsumedOwnQuestionIsNotSetAside(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := askOwn(t, s, "Which database?", dbChoices...)
	// A guest's line, no question named, let go before its Enter: the
	// hold, the question held over.
	line := submitAsync(s, Submission{Text: "Postgres", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	heldShows(t, s)
	// An Enter typed in the terminal enters the held text: the question
	// shows again, consumed.
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID {
		t.Fatalf("after the Enter: %+v", st)
	}
	// The agent works on the answer it has, in the question's turn, and
	// ends its turn: the question is gone, not shown again.
	reportState(t, s, AttentionWorking, "")
	if a := s.Info().Attention; a.State != AttentionWorking {
		t.Fatalf("after working: %+v", a)
	}
	reportState(t, s, AttentionDone, "finished")
	if a := s.Info().Attention; a.State != AttentionDone || a.ID == q.ID {
		t.Fatalf("after the done: %+v", a)
	}
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a reply after the done: %v", err)
	}
	nothingWritten(t, p, 100*time.Millisecond)
	if s.Info().LastAnswer != nil {
		t.Fatalf("recorded: %+v", s.Info().LastAnswer)
	}
}

// Round 7, finding 4: the snapshot's rows are read under the session's
// lock, so a resize that comes as a quiet look begins (the baseline dropped
// by it) does not get the rows before the resize installed as the baseline:
// the next quiet compares the screen with itself as it is, and the window
// resized withdraws nothing.
func TestASnapshotTakenAsTheWindowResizesIsNotTheBaseline(t *testing.T) {
	s, p, _ := asking(t, Options{})
	nate := controller(t, s, "Nate")
	// Five long rows, then the bell: the baseline at 80 columns.
	out(p, "\x1b[2J\x1b[H"+strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789ABCD\r\n", 5)+"Allow? [y/n]\a")
	q := waitAttention(t, s, isBell)
	time.Sleep(2 * patternQuiet)
	// The next quiet look paused as it begins; the window shrinks
	// meanwhile, cutting every row.
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeShot.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	out(p, "\x1b[24;1H.")
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the quiet look never began")
	}
	if err := s.Resize(nate, 20, 24); err != nil {
		t.Fatal(err)
	}
	let.Do(func() { close(gate) })
	time.Sleep(4 * patternQuiet)
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("the resize withdrew the wait: %+v", a)
	}
	// The agent moving on still does.
	out(p, "\r\nmoving on: one\r\nmoving on: two\r\nmoving on: three\r\nmoving on: four\r\n")
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen {
		t.Fatalf("the lines after the resize: %+v", a)
	}
}

// Round 8: an Enter typed in the terminal as the hold of the turn is
// installed, before its words show, enters the stranded text: the program
// has its answer. The hold ends, the stranded line's claim goes, and the
// question shows consumed: a reply is refused, nothing written, until the
// agent moves on.
func TestAnEnterThatEndsTheHoldBeforeItShowsConsumesTheQuestion(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHoldShown.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "Postgres", By: guest, Question: q.ID})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	// The Enter typed in the terminal: the text entered, the hold over.
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID || st.claim != (questionClaim{}) {
		t.Fatalf("after the Enter: %+v", st)
	}
	refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), QuestionConsumedWords, "a reply after the Enter")
	nothingWritten(t, p, 100*time.Millisecond)
	let.Do(func() { close(gate) })
	got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line })
	if !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	// The hold's words never show; the question stands consumed until the
	// agent moves on.
	time.Sleep(100 * time.Millisecond)
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID || s.Held() {
		t.Fatalf("once the hold's installation went on: %+v, held %v", st, s.Held())
	}
	refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), QuestionConsumedWords, "a reply later")
	nothingWritten(t, p, 100*time.Millisecond)
	if s.Info().LastAnswer != nil {
		t.Fatalf("recorded: %+v", s.Info().LastAnswer)
	}
	reportState(t, s, AttentionWorking, "")
	if a := s.Info().Attention; a.ID == q.ID {
		t.Fatalf("after the agent moved on: %+v", a)
	}
}

// Round 9, finding 1: an Enter typed in the terminal after a reply's line
// is stranded (its link revoked) and before the hold of the turn is
// installed enters the text: the program has its answer. The stranded
// claim goes with it, no hold follows, and the question shows consumed:
// a reply is refused, Ctrl+U shows nothing OPEN, until the agent moves on.
func TestKeysBeforeTheHoldIsInstalledConsumeTheQuestion(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "Postgres", By: guest, Question: q.ID})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line })
	if !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	// Before the hold is installed: the Enter enters the text.
	if st := stateOf(s); st.claim.id != q.ID || st.shown.ID != q.ID || s.Held() {
		t.Fatalf("before the hold: %+v, held %v", st, s.Held())
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID || st.claim != (questionClaim{}) {
		t.Fatalf("after the Enter: %+v", st)
	}
	refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), QuestionConsumedWords, "a reply after the Enter")
	nothingWritten(t, p, 100*time.Millisecond)
	// The hold's installation goes on: none follows, and Ctrl+U shows
	// nothing OPEN.
	let.Do(func() { close(gate) })
	time.Sleep(100 * time.Millisecond)
	if s.Held() {
		t.Fatal("a hold followed the text entered")
	}
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID {
		t.Fatalf("after Ctrl+U: %+v", st)
	}
	refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), QuestionConsumedWords, "a reply after Ctrl+U")
	nothingWritten(t, p, 100*time.Millisecond)
	if s.Info().LastAnswer != nil {
		t.Fatalf("recorded: %+v", s.Info().LastAnswer)
	}
	reportState(t, s, AttentionWorking, "")
	if a := s.Info().Attention; a.ID == q.ID {
		t.Fatalf("after the agent moved on: %+v", a)
	}
}

// Round 9, finding 2: the chat's line that says a question was answered
// refers to that question's own line, not to the one asked last: a
// dialog drawn over the hold of the turn and gone without an answer, then
// the question the hold held over shown again by Ctrl+U and resolved.
func TestTheChatsAnsweredLineNamesTheQuestionResolved(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: approvalQuestions()})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	heldShows(t, s)
	// A dialog over the hold, asked in the chat too, then gone without an
	// answer: the hold shows again.
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	d := waitAttention(t, s, asked)
	p.outW.Write([]byte(approvalGone("■ Conversation interrupted\r\n› ")))
	heldShows(t, s)
	// Ctrl+U ends the hold: the question OPEN, and a reply resolves it.
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	open(t, s, q, "once the hold ended")
	if err := s.AnswerInput(context.Background(), nate, q.ID, []byte("Postgres\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	resolved(t, s, q, "Nate", "the reply")
	var last *ChatMessage
	for _, m := range s.ChatHistory() {
		if m.Kind == proto.ChatKindSystem && m.Event == "answered" {
			m := m
			last = &m
		}
	}
	if last == nil || last.Ref != q.ID || last.Ref == d.ID {
		t.Fatalf("the chat's answered line %+v, want one of the question %s (the dialog was %s)", last, q.ID, d.ID)
	}
}

// A startup question answered in the terminal (consumed) goes once the
// screen shows no startup question: the program erasing the dialog's lines
// and printing its prompt, the screen not erased whole, after the window
// grew (the page's terminal sized the PTY, and the program drew nothing
// for it): what was drawn is read as it was, nothing cut.
func TestAStartupQuestionAnsweredInTheTerminalGoesAsItsLinesAreErased(t *testing.T) {
	s, p, _ := asking(t, Options{Questions: []StartupQuestion{{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Yes", Input: "\r"}}}}})
	nate := controller(t, s, "Nate")
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit\r\n"))
	q := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if err := s.Resize(nate, 120, 40); err != nil {
		t.Fatal(err)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID {
		t.Fatalf("after the Enter: %+v", st)
	}
	out(p, "\r\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2KTrusted.\r\n> ")
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen {
		t.Fatalf("the question gone: %+v", a)
	}
	if s.Info().LastAnswer != nil {
		t.Fatalf("recorded: %+v", s.Info().LastAnswer)
	}
}

// Round 10, finding 1: a startup question drawn narrow enough that its
// words autowrap (the update prompt's "runs" split at 18 columns), held by
// an Escape a write ended with, then the window grown and unrelated output
// drawn: the screen, laid out narrower, still shows the question, read
// with its wrapped rows joined (Lines), and nothing takes it for gone; a
// submission is still held back from it.
func TestAWrappedStartupQuestionStaysWhenTheWindowGrows(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: startupQuestions()}))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 18, Rows: 24}, newChanSink(false))
	// The update question as a program draws it line by line, which at 18
	// columns autowraps its heading and splits "runs" over two rows.
	p.outW.Write([]byte("\x1b[2J\x1b[HUpdate available · 0.161.0 → 0.162.1\r\n\r\n› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n\r\nenter continue · esc skip"))
	update := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if rows := s.asked.Text(); !strings.Contains(rows, "(r\nuns") {
		t.Fatalf("the screen's rows at 18 columns: %q", rows)
	}
	for _, part := range []string{"\x1b", "z"} {
		if err := s.Input(nater, []byte(part)); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
	}
	p.outW.Write([]byte("\x1b[24;1Hstatus: ready"))
	fed := s.asked.Fed()
	deadline := time.Now().Add(3 * time.Second)
	for s.asked.Fed() == fed && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.ResizeWith(nater, 80, 24, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	p.outW.Write([]byte("\x1b[24;1Hstatus: still ready"))
	time.Sleep(3 * patternQuiet)
	if a := s.Info().Attention; a.ID != update.ID || a.State != AttentionNeedsInput {
		t.Fatalf("the window grown took the wrapped question for gone: %+v", a)
	}
	if !s.startupOnScreen() {
		t.Fatal("the screen grown is not read as still showing the question")
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: nater}); !errors.Is(err, ErrTrustQuestion) {
		t.Fatalf("a submission at the question: %v", err)
	}
	noWrite(t, p, 100*time.Millisecond)
	// The program moving on, the screen erased whole: gone.
	p.outW.Write([]byte("\x1b[2J\x1b[H\x1b[2;3H› Ask Codex to do anything"))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
}

// Round 10, finding 2: a reply's keys written for a question, the question
// then held over by the hold of a line let go, and a dialog shown over the
// hold answered by another reply: the dialog's claim leaves the held-over
// question's claim standing, which resolves it once its keys' word comes,
// and the hold's end shows it no more.
func TestAHeldOverQuestionsClaimSurvivesADialogsClaim(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: approvalQuestions()})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	// The guest's line, no question named, typed and paused at its Enter.
	at, release := pauseEnter(t, s)
	line := submitAsync(s, Submission{Text: "note", By: guest})
	if got := written(t, p); got != "note" {
		t.Fatalf("written %q", got)
	}
	atEnter(t, at)
	// Nate's reply to the question: its keys written, paused before their
	// word (the claim stands, the write done).
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
	go func() { word <- s.AnswerInput(context.Background(), nate, q.ID, []byte("Postgres\r")) }()
	if got := written(t, p); got != "Postgres\r" {
		t.Fatalf("written %q", got)
	}
	<-reached
	// The guest's link goes and its Enter is left out: the hold, the
	// question held over with Nate's claim standing on it.
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	release()
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	heldShows(t, s)
	// A dialog over the hold, answered by a reply of its own.
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	d := waitAttention(t, s, asked)
	if err := s.AnswerInput(context.Background(), nate, d.ID, []byte("y")); err != nil {
		t.Fatalf("the dialog's reply: %v", err)
	}
	if got := written(t, p); got != "y" {
		t.Fatalf("written %q", got)
	}
	resolved(t, s, d, "Nate", "the dialog")
	// Nate's word for the question comes: resolved, hidden, by Nate.
	let.Do(func() { close(gate) })
	if err := returnsWithin(t, 5*time.Second, "the word", func() error { return <-word }); err != nil {
		t.Fatalf("the word: %v", err)
	}
	resolved(t, s, q, "Nate", "the held-over question")
	// Ctrl+U ends the hold: nothing shows again, and a reply to the
	// question gets its result.
	heldShows(t, s)
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	time.Sleep(100 * time.Millisecond)
	if a := s.Info().Attention; a.ID == q.ID || a.State == AttentionNeedsInput {
		t.Fatalf("once the hold ended: %+v", a)
	}
	refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), "this question was already answered by Nate: nothing was sent", "a reply after the result")
	nothingWritten(t, p, 100*time.Millisecond)
}

// Round 10, finding 3: the pre-hold Enter for a line naming no question. A
// guest's line, no question named, typed at a question the agent reported
// and let go before its Enter; an Enter typed in the terminal before the
// hold of the turn is installed enters the text: the program has its
// answer, no hold follows, the question shows consumed, and Ctrl+U shows
// nothing OPEN.
func TestAnEnterBeforeTheHoldOfAnUnnamedLineConsumesTheQuestion(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line })
	if !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID {
		t.Fatalf("after the Enter: %+v", st)
	}
	refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), QuestionConsumedWords, "a reply after the Enter")
	nothingWritten(t, p, 100*time.Millisecond)
	let.Do(func() { close(gate) })
	time.Sleep(100 * time.Millisecond)
	if s.Held() {
		t.Fatal("a hold followed the text entered")
	}
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID {
		t.Fatalf("after Ctrl+U: %+v", st)
	}
	refusedAs(t, s.AnswerInput(context.Background(), nate, q.ID, []byte("SQLite\r")), QuestionConsumedWords, "a reply after Ctrl+U")
	nothingWritten(t, p, 100*time.Millisecond)
	if s.Info().LastAnswer != nil {
		t.Fatalf("recorded: %+v", s.Info().LastAnswer)
	}
}

// Round 10, finding 4: a window made narrower by a column that cuts only
// blank cells loses nothing: a startup question answered in the terminal
// after it goes once the program erases the dialog's lines and shows its
// prompt, as it would with the window as it was.
func TestAShrinkThatCutsNothingKeepsNoAnsweredStartupQuestion(t *testing.T) {
	s, p, _ := asking(t, Options{Questions: []StartupQuestion{{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Yes", Input: "\r"}}}}})
	nate := controller(t, s, "Nate")
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit\r\n"))
	q := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if err := s.Resize(nate, 79, 24); err != nil {
		t.Fatal(err)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	if st := stateOf(s); st.shown.ID != q.ID || st.consumed != q.ID {
		t.Fatalf("after the Enter: %+v", st)
	}
	out(p, "\r\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2KTrusted.\r\n> ")
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen {
		t.Fatalf("the question gone: %+v", a)
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: nate}); err != nil {
		t.Fatalf("a submission after the question went: %v", err)
	}
}

// Round 11, finding 1: a dialog drawn after a line was stranded and before
// the hold of the turn is installed, not asked yet (its watcher waits for
// a quiet screen), takes the Enter typed in the terminal: the keys reached
// the dialog, not the stranded text, so the question the agent reported
// is not consumed and the hold follows; the dialog, asked consumed, shows
// over the hold, and once the program takes it away the hold shows, its
// text still waiting for a person.
func TestAnEnterAtADialogDrawnBeforeTheHoldKeepsTheHold(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: approvalQuestions()})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	// The dialog drawn, on the screen and not asked yet; the Enter typed
	// at it.
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	if !waitFor(2*time.Second, func() bool {
		text, _, _, _ := s.asked.Whole()
		return strings.Contains(text, "Would you like to run")
	}) {
		t.Fatal("the dialog never reached the screen")
	}
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("the dialog was asked before the Enter: %+v", a)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.consumed == q.ID {
		t.Fatalf("the Enter at the dialog consumed the question: %+v", st)
	}
	let.Do(func() { close(gate) })
	if !waitFor(5*time.Second, s.Held) {
		t.Fatal("no hold followed the stranded text")
	}
	// The dialog, asked consumed, then taken away by the program: the
	// hold shows, and Ctrl+U shows the question again OPEN.
	d := waitAttention(t, s, asked)
	if st := stateOf(s); st.consumed != d.ID {
		t.Fatalf("the dialog the Enter reached: %+v", st)
	}
	p.outW.Write([]byte(approvalGone("■ Conversation interrupted\r\n› ")))
	heldShows(t, s)
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	open(t, s, q, "once the hold ended")
}

// Round 11, finding 2: the main screen grown with a wrapped startup
// question on it, then the alternate screen entered, erased whole and
// left: what the alternate screen did says nothing of the main screen,
// whose rows autowrap went on from still read joined at their extent, and
// the question held by an Escape stays protected.
func TestAnAlternateScreenErasedLeavesTheMainScreensWrappedQuestion(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: startupQuestions()}))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 18, Rows: 24}, newChanSink(false))
	p.outW.Write([]byte("\x1b[2J\x1b[HUpdate available · 0.161.0 → 0.162.1\r\n\r\n› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n\r\nenter continue · esc skip"))
	update := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	for _, part := range []string{"\x1b", "z"} {
		if err := s.Input(nater, []byte(part)); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
	}
	p.outW.Write([]byte("\x1b[24;1Hstatus: ready"))
	fed := s.asked.Fed()
	deadline := time.Now().Add(3 * time.Second)
	for s.asked.Fed() == fed && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.ResizeWith(nater, 80, 24, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	// The alternate screen entered, erased whole, and left; then output.
	p.outW.Write([]byte("\x1b[?1049h\x1b[2J\x1b[H\x1b[?1049l"))
	time.Sleep(100 * time.Millisecond)
	p.outW.Write([]byte("\x1b[24;1Hstatus: still ready"))
	time.Sleep(3 * patternQuiet)
	if a := s.Info().Attention; a.ID != update.ID || a.State != AttentionNeedsInput {
		t.Fatalf("the alternate screen erased took the main screen's question for gone: %+v", a)
	}
	if text, _, _, _ := s.asked.Whole(); !strings.Contains(text, "Update now (runs") {
		t.Fatalf("the main screen's wrapped rows read: %q", text)
	}
	if !s.startupOnScreen() {
		t.Fatal("the screen is not read as still showing the question")
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: nater}); !errors.Is(err, ErrTrustQuestion) {
		t.Fatalf("a submission at the question: %v", err)
	}
	noWrite(t, p, 100*time.Millisecond)
}

// #225: the wrapped question's row rewritten after the window grew. Blanks
// the program repaints into the growth's padding change nothing shown: the
// question still reads whole, stays held, and a submission is still refused.
// The choice redrawn on one row at the new width (its words past the old
// extent) ends the row's join and reads what was drawn: the question still
// matches, still held.
func TestAWrappedStartupQuestionStaysWhenItsRowIsRepaintedAfterTheWindowGrew(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: startupQuestions()}))
	nater, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 18, Rows: 24}, newChanSink(false))
	p.outW.Write([]byte("\x1b[2J\x1b[HUpdate available · 0.161.0 → 0.162.1\r\n\r\n› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n\r\nenter continue · esc skip"))
	update := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if rows := s.asked.Text(); !strings.Contains(rows, "(r\nuns") {
		t.Fatalf("the screen's rows at 18 columns: %q", rows)
	}
	for _, part := range []string{"\x1b", "z"} {
		if err := s.Input(nater, []byte(part)); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
	}
	p.outW.Write([]byte("\x1b[24;1Hstatus: ready"))
	fed := s.asked.Fed()
	deadline := time.Now().Add(3 * time.Second)
	for s.asked.Fed() == fed && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.ResizeWith(nater, 80, 24, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	// Blanks repainted into the padding past the choice row's old extent:
	// the heading takes rows 1 and 2 at 18 columns, row 3 is blank, and the
	// choice that wrapped ("(r" / "uns") begins on row 4.
	if rows := s.asked.Text(); !strings.Contains(rows, "\n\n› 1. Update now (r\nuns") {
		t.Fatalf("the rows before the repaint: %q", rows)
	}
	p.outW.Write([]byte("\x1b[4;19H      \x1b[24;1Hstatus: still ready"))
	time.Sleep(3 * patternQuiet)
	if a := s.Info().Attention; a.ID != update.ID || a.State != AttentionNeedsInput {
		t.Fatalf("blanks in the padding took the wrapped question for gone: %+v", a)
	}
	if !s.startupOnScreen() {
		t.Fatal("the screen is not read as still showing the question after the blanks")
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: nater}); !errors.Is(err, ErrTrustQuestion) {
		t.Fatalf("a submission at the question: %v", err)
	}
	noWrite(t, p, 100*time.Millisecond)
	// The choice redrawn on one row at the new width, nothing erased: the
	// write past the old extent ends the join, the row reads what was
	// drawn, and the question still matches.
	p.outW.Write([]byte("\x1b[4;1H› 1. Update now (runs `npm install -g @openai/codex`)\x1b[24;1Hstatus: ready again"))
	time.Sleep(3 * patternQuiet)
	if rows, _, _, _ := s.asked.Whole(); !strings.Contains(rows, "Update now (runs `npm install -g @openai/codex`)\nuns") {
		t.Fatalf("the choice redrawn whole reads %q", rows)
	}
	if a := s.Info().Attention; a.ID != update.ID || a.State != AttentionNeedsInput {
		t.Fatalf("the choice redrawn whole took the question for gone: %+v", a)
	}
	if !s.startupOnScreen() {
		t.Fatal("the screen is not read as still showing the question after the redraw")
	}
	// The program moving on, the screen erased whole: gone.
	p.outW.Write([]byte("\x1b[2J\x1b[H\x1b[2;3H› Ask Codex to do anything"))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
}

// #224: the same with a startup question drawn before the hold and not
// asked yet, the trust dialog here: the Enter typed in the terminal reaches
// that dialog, not the stranded text, so the question the agent reported is
// not consumed, the stranded claim stays and the hold follows; once the
// program takes the dialog away the hold shows, its text still waiting for
// a person. The startup question's watch lasts, the stranded line's Enter
// never having been written.
func TestAnEnterAtAStartupQuestionDrawnBeforeTheHoldKeepsTheHold(t *testing.T) {
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Trust and continue", Input: "\r"}, {Label: "Quit", Input: "\x1b"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: append(approvalQuestions(), trust)})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	// The trust dialog drawn, on the screen and not asked yet; the Enter
	// typed at it.
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit\r\n"))
	if !waitFor(2*time.Second, func() bool { return s.trust != nil && s.trust.Watching() && s.lastStartup(s.trust.Text()) != "" }) {
		t.Fatal("the dialog never reached the screen")
	}
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("the dialog was asked before the Enter: %+v", a)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.consumed == q.ID {
		t.Fatalf("the Enter at the startup question consumed the reported question: %+v", st)
	}
	let.Do(func() { close(gate) })
	if !waitFor(5*time.Second, s.Held) {
		t.Fatal("no hold followed the stranded text")
	}
	// The program takes the dialog away: the hold shows, and Ctrl+U shows
	// the question again OPEN.
	p.outW.Write([]byte("\r\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2KTrusted.\r\n> "))
	heldShows(t, s)
	if err := s.Input(nate, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	open(t, s, q, "once the hold ended")
}

// #224, the witness: the screen, not the output's tail. A footer the program
// redraws in place evicts the dialog's heading from the tail (ScreenTail
// keeps the last 1024 bytes) while the dialog stays on the screen: the Enter
// still reaches the dialog, and the hold is kept.
func TestAnEnterAtAStartupQuestionEvictedFromTheTailKeepsTheHold(t *testing.T) {
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Trust and continue", Input: "\r"}, {Label: "Quit", Input: "\x1b"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: []StartupQuestion{trust}})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	// The dialog, then its footer redrawn in place sixty times: the heading
	// leaves the tail, the screen keeps it.
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit" + strings.Repeat("\r\x1b[2Kenter continue · esc quit", 60)))
	if !waitFor(2*time.Second, func() bool {
		whole, _, _, _ := s.asked.Whole()
		return strings.Contains(whole, "Trust this folder?") && !strings.Contains(s.trust.Text(), "Trust this folder?")
	}) {
		whole, _, _, _ := s.asked.Whole()
		t.Fatalf("screen %q, tail %q", whole, s.trust.Text())
	}
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("the dialog was asked before the Enter: %+v", a)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.consumed == q.ID {
		t.Fatalf("the Enter at the dialog consumed the reported question: %+v", st)
	}
	let.Do(func() { close(gate) })
	if !waitFor(5*time.Second, s.Held) {
		t.Fatal("no hold followed the stranded text")
	}
}

// #224, the other way: a dialog the program drew and erased before the
// Enter stays in the output's tail but not on the screen: the Enter enters
// the stranded text, as it does with no dialog, and the question is
// consumed, no hold following.
func TestAnEnterAfterAnErasedStartupQuestionEntersTheStrandedText(t *testing.T) {
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Trust and continue", Input: "\r"}, {Label: "Quit", Input: "\x1b"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: []StartupQuestion{trust}})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	// Drawn and erased in one breath: on the tail, gone from the screen.
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit\r\n\r\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2KTrusted.\r\n> "))
	if !waitFor(2*time.Second, func() bool {
		whole, _, _, _ := s.asked.Whole()
		return !strings.Contains(whole, "Trust this folder?") && strings.Contains(whole, "Trusted.") && strings.Contains(s.trust.Text(), "Trust this folder?")
	}) {
		whole, _, _, _ := s.asked.Whole()
		t.Fatalf("screen %q, tail %q", whole, s.trust.Text())
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	let.Do(func() { close(gate) })
	if st := stateOf(s); st.consumed != q.ID {
		t.Fatalf("the Enter entered the stranded text: %+v", st)
	}
	time.Sleep(400 * time.Millisecond)
	if s.Held() {
		t.Fatal("a hold followed the text the Enter entered")
	}
}

// #224, a short screen: on three rows the dialog's heading scrolls off as
// its footer is drawn, nothing cut, while its choices stay. The Enter still
// reaches the dialog (the tail knows which was drawn, the screen shows its
// choices), and the hold is kept.
func TestAnEnterAtAStartupQuestionWhoseHeadingScrolledOffKeepsTheHold(t *testing.T) {
	// The catalog's labels for Codex, which name what an answer does, not
	// what the dialog draws: the choices are read by their shape.
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Yes, trust this folder", Input: "\r"}, {Label: "No, quit", Input: "\x1b[B\r"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: []StartupQuestion{trust}})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	// Three rows, before anything is drawn: nothing to cut.
	if err := s.Resize(nate, 80, 3); err != nil {
		t.Fatal(err)
	}
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit"))
	if !waitFor(2*time.Second, func() bool {
		whole, settled, uncut, _, _ := s.asked.Frame()
		return settled && uncut && !strings.Contains(whole, "Trust this folder?") && strings.Contains(whole, "Trust and continue")
	}) {
		whole, settled, uncut, _, _ := s.asked.Frame()
		t.Fatalf("screen %q settled %v uncut %v", whole, settled, uncut)
	}
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("the dialog was asked before the Enter: %+v", a)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.consumed == q.ID {
		t.Fatalf("the Enter at the dialog consumed the reported question: %+v", st)
	}
	let.Do(func() { close(gate) })
	if !waitFor(5*time.Second, s.Held) {
		t.Fatal("no hold followed the stranded text")
	}
}

func TestAnEnterAtAStartupQuestionScrolledToItsLastChoiceKeepsTheHold(t *testing.T) {
	// #224, round 5: the dialog's trailing line scrolls it up once more on
	// three rows, so the heading and the highlighted first choice are gone
	// and the last choice alone stays with the footer (the stub draws it
	// so): one numbered row is enough.
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Yes, trust this folder", Input: "\r"}, {Label: "No, quit", Input: "\x1b[B\r"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: []StartupQuestion{trust}})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	// Three rows, before anything is drawn: nothing to cut.
	if err := s.Resize(nate, 80, 3); err != nil {
		t.Fatal(err)
	}
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit\r\n"))
	if !waitFor(2*time.Second, func() bool {
		whole, settled, uncut, _, _ := s.asked.Frame()
		return settled && uncut && !strings.Contains(whole, "Trust and continue") && strings.Contains(whole, "2. Quit")
	}) {
		whole, settled, uncut, _, _ := s.asked.Frame()
		t.Fatalf("screen %q settled %v uncut %v", whole, settled, uncut)
	}
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("the dialog was asked before the Enter: %+v", a)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.consumed == q.ID {
		t.Fatalf("the Enter at the dialog consumed the reported question: %+v", st)
	}
	let.Do(func() { close(gate) })
	if !waitFor(5*time.Second, s.Held) {
		t.Fatal("no hold followed the stranded text")
	}
}

func TestAnEnterAtAStartupQuestionOnATwoRowScreenKeepsTheHold(t *testing.T) {
	// #224, round 5: two rows, the smallest screen the dialog's last choice
	// and footer fit; the heading and the highlighted choice never show.
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Yes, trust this folder", Input: "\r"}, {Label: "No, quit", Input: "\x1b[B\r"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: []StartupQuestion{trust}})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	// Two rows, before anything is drawn: nothing to cut.
	if err := s.Resize(nate, 80, 2); err != nil {
		t.Fatal(err)
	}
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit"))
	if !waitFor(2*time.Second, func() bool {
		whole, settled, uncut, _, _ := s.asked.Frame()
		return settled && uncut && !strings.Contains(whole, "Trust and continue") && strings.Contains(whole, "2. Quit")
	}) {
		whole, settled, uncut, _, _ := s.asked.Frame()
		t.Fatalf("screen %q settled %v uncut %v", whole, settled, uncut)
	}
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("the dialog was asked before the Enter: %+v", a)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.consumed == q.ID {
		t.Fatalf("the Enter at the dialog consumed the reported question: %+v", st)
	}
	let.Do(func() { close(gate) })
	if !waitFor(5*time.Second, s.Held) {
		t.Fatal("no hold followed the stranded text")
	}
}

// #224, half a frame: a synchronized update begun and not ended (the
// program erasing to redraw) leaves the screen unsettled; what it shows is
// not known, so the Enter is taken to have reached the dialog and the hold
// is kept, whatever the model holds meanwhile.
func TestAnEnterDuringASynchronizedRedrawOfAStartupQuestionKeepsTheHold(t *testing.T) {
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Trust and continue", Input: "\r"}, {Label: "Quit", Input: "\x1b"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: []StartupQuestion{trust}})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	// The dialog, then a synchronized update begun with the screen erased
	// and the dialog not drawn again yet.
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit\r\n" + "\x1b[?2026h\x1b[2J\x1b[H"))
	if !waitFor(2*time.Second, func() bool {
		whole, settled, _, _, _ := s.asked.Frame()
		return !settled && !strings.Contains(whole, "Trust this folder?")
	}) {
		whole, settled, _, _, _ := s.asked.Frame()
		t.Fatalf("screen %q settled %v", whole, settled)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.consumed == q.ID {
		t.Fatalf("the Enter during the redraw consumed the reported question: %+v", st)
	}
	// The redraw ends with the dialog back: the hold follows.
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit\r\n\x1b[?2026l"))
	let.Do(func() { close(gate) })
	if !waitFor(5*time.Second, s.Held) {
		t.Fatal("no hold followed the stranded text")
	}
}

// #224, the two together: on three rows the dialog's heading scrolls off,
// and its footer redrawn in place sixty times evicts the heading from the
// output's tail too, so nothing says which question was drawn; its choices
// stay on the screen, numbered. The Enter still reaches the dialog, and the
// hold is kept.
func TestAnEnterAtAStartupQuestionScrolledOffAndEvictedFromTheTailKeepsTheHold(t *testing.T) {
	// The catalog's labels for Codex, as in the scrolled-off test above.
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Yes, trust this folder", Input: "\r"}, {Label: "No, quit", Input: "\x1b[B\r"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: []StartupQuestion{trust}})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	if err := s.Resize(nate, 80, 3); err != nil {
		t.Fatal(err)
	}
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit" + strings.Repeat("\r\x1b[2Kenter continue · esc quit", 60)))
	if !waitFor(2*time.Second, func() bool {
		whole, settled, uncut, _, _ := s.asked.Frame()
		return settled && uncut && !strings.Contains(whole, "Trust this folder?") && strings.Contains(whole, "2. Quit") && !strings.Contains(s.trust.Text(), "Trust this folder?")
	}) {
		whole, settled, uncut, _, _ := s.asked.Frame()
		t.Fatalf("screen %q settled %v uncut %v, tail %q", whole, settled, uncut, s.trust.Text())
	}
	if a := s.Info().Attention; a.ID != q.ID {
		t.Fatalf("the dialog was asked before the Enter: %+v", a)
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if st := stateOf(s); st.consumed == q.ID {
		t.Fatalf("the Enter at the dialog consumed the reported question: %+v", st)
	}
	let.Do(func() { close(gate) })
	if !waitFor(5*time.Second, s.Held) {
		t.Fatal("no hold followed the stranded text")
	}
}

// #224, the other way on a grown window: the dialog drawn, the window made
// larger (nothing lost; the screen not laid out for its size until erased
// whole), then the dialog erased row by row and the prompt drawn. The screen
// reads whole and shows no dialog: the Enter enters the stranded text, the
// question is consumed, no hold following.
func TestAnEnterAfterAStartupQuestionErasedRowByRowOnAGrownScreenEntersTheStrandedText(t *testing.T) {
	trust := StartupQuestion{Pattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Answers: []Option{{Label: "Trust and continue", Input: "\r"}, {Label: "Quit", Input: "\x1b"}}}
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond, Questions: []StartupQuestion{trust}})
	var gone atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &gone)
	nate := controller(t, s, "Nate")
	q := reported(t, s, "Which database?")
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, let sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeHold.Store(&hook)
	t.Cleanup(func() { let.Do(func() { close(gate) }) })
	line := submitAsync(s, Submission{Text: "note", By: guest})
	written(t, p)
	untilIdle(t, s)
	gone.Store(true)
	s.DisconnectLink(guest.LinkID)
	if got := returnsWithin(t, 5*time.Second, "the line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("the line: %+v %v", got.res, got.err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never began")
	}
	p.outW.Write([]byte("Trust this folder?\r\n› 1. Trust and continue\r\n  2. Quit\r\nenter continue · esc quit"))
	if !waitFor(2*time.Second, func() bool {
		whole, _, _, _ := s.asked.Whole()
		return strings.Contains(whole, "Trust this folder?")
	}) {
		t.Fatal("the dialog never reached the screen")
	}
	// The window grown with the dialog on it: nothing lost, the screen not
	// laid out for its size until erased whole.
	if err := s.Resize(nate, 100, 30); err != nil {
		t.Fatal(err)
	}
	// The dialog erased row by row, the prompt drawn: on the tail, gone from the screen.
	p.outW.Write([]byte("\r\n\r\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2KTrusted.\r\n> "))
	if !waitFor(2*time.Second, func() bool {
		whole, settled, uncut, _, _ := s.asked.Frame()
		return !settled && uncut && !strings.Contains(whole, "Trust this folder?") && !strings.Contains(whole, "Quit") && strings.Contains(whole, "Trusted.") && strings.Contains(s.trust.Text(), "Trust this folder?")
	}) {
		whole, settled, uncut, _, _ := s.asked.Frame()
		t.Fatalf("screen %q settled %v uncut %v, tail %q", whole, settled, uncut, s.trust.Text())
	}
	if err := s.Input(nate, []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	let.Do(func() { close(gate) })
	if st := stateOf(s); st.consumed != q.ID {
		t.Fatalf("the Enter entered the stranded text: %+v", st)
	}
	time.Sleep(400 * time.Millisecond)
	if s.Held() {
		t.Fatal("a hold followed the text the Enter entered")
	}
}

// #224: a dialog's choices are read by their shape, not the catalog's
// labels: a numbered row with words, the highlight marker (Codex's "›",
// Claude Code's "❯") before it or not, one row being enough (the last
// choice alone stays once the dialog scrolled up by its trailing line);
// prose, or a number with no words, is no choice.
func TestStartupChoicesAreReadByTheirShape(t *testing.T) {
	cases := []struct {
		name  string
		whole string
		want  bool
	}{
		{"Codex's choices, the heading scrolled off", "› 1. Trust and continue\n  2. Quit\nenter continue · esc quit", true},
		{"Claude Code's choices", "❯ 1. Yes, I trust this folder\n  2. No, exit\n", true},
		{"one highlighted row alone", "  › 1. Update now\n", true},
		{"two plain numbered rows", "1. Review hooks\n2. Trust all and continue\n", true},
		{"the last choice alone, with the footer", "  2. Quit\nenter continue · esc quit\n", true},
		{"one plain numbered row", "Steps:\n1. Install it\nthen run\n", true},
		{"prose", "Trusted.\n> \n", false},
		{"a number with no words", "1.\n2.\n", false},
	}
	for _, c := range cases {
		if got := startupChoiceShown(c.whole); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
