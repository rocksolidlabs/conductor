package session

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"runtime"
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
		if !slices.Equal(info.Kept, []string{own.ID, q.ID}) {
			t.Fatalf("kept %q, want the agent's own question set aside and the question the hold took the place of, %q", info.Kept, []string{own.ID, q.ID})
		}
	})
}

// keptLog keeps the Kept of every Info the session hands its OnChange hook.
type keptLog struct {
	mu   sync.Mutex
	told [][]string
}

func (k *keptLog) hook(info Info) {
	k.mu.Lock()
	k.told = append(k.told, info.Kept)
	k.mu.Unlock()
}

// count is how many changes were told.
func (k *keptLog) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.told)
}

// toldSince waits for a change told after the first n whose Kept is want.
func (k *keptLog) toldSince(t *testing.T, n int, want []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		k.mu.Lock()
		for _, kept := range k.told[n:] {
			if slices.Equal(kept, want) {
				k.mu.Unlock()
				return
			}
		}
		got := slices.Clone(k.told[n:])
		k.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("changes told since: %q, none keeping %q", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// What the session keeps is told as a change (OnChange) whenever it changes,
// though what it shows does not: the decision router hears of a question
// let go only so. A person's keys letting go of the agent's own question
// set aside; the agent moving on while the turn is held, letting go of the
// question the hold took the place of; a person's keys ending a hold that a
// question on the screen shows over, letting that one go too (the agent's
// own among them); and the question shown asked as the agent's own, taking
// the place of the one set aside.
func TestKeptChangesAreTold(t *testing.T) {
	t.Run("the own question let go by keys", func(t *testing.T) {
		k := &keptLog{}
		s, p, _ := asking(t, Options{OnChange: k.hook})
		owner := ownerOf(t, s)
		q := askOwn(t, s, "Which database?", dbChoices...)
		reportState(t, s, AttentionWorking, "")
		if kept := s.Info().Kept; !slices.Equal(kept, []string{q.ID}) {
			t.Fatalf("set aside: kept %q", kept)
		}
		n := k.count()
		if err := s.Input(owner, []byte("x")); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		k.toldSince(t, n, nil)
	})
	t.Run("the held-over question let go by the agent", func(t *testing.T) {
		k := &keptLog{}
		s, _ := newLocalWith(t, quiet(Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second, OnChange: k.hook}))
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		q := s.Info().Attention
		go holdNow(s)
		heldShows(t, s)
		if kept := s.Info().Kept; !slices.Equal(kept, []string{q.ID}) {
			t.Fatalf("held over: kept %q", kept)
		}
		n := k.count()
		reportState(t, s, AttentionWorking, "")
		heldShows(t, s)
		k.toldSince(t, n, nil)
	})
	t.Run("a hold under a question on the screen ended by keys", func(t *testing.T) {
		k := &keptLog{}
		s, p := newLocalWith(t, quiet(Options{ScrollbackBytes: 4096, InputWait: 5 * time.Second, OnChange: k.hook,
			TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), TrustAnswers: []Option{{Label: "Yes", Input: "\r"}}}))
		owner := ownerOf(t, s)
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		q := s.Info().Attention
		go holdNow(s)
		heldShows(t, s)
		s.fireTrust("Trust this folder?")
		waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
		if kept := s.Info().Kept; !slices.Equal(kept, []string{q.ID}) {
			t.Fatalf("held over under the question: kept %q", kept)
		}
		n := k.count()
		if err := s.Input(owner, []byte{0x15}); err != nil {
			t.Fatal(err)
		}
		if got := written(t, p); got != "\x15" {
			t.Fatalf("written %q", got)
		}
		if s.Held() {
			t.Fatal("the keys did not end the hold")
		}
		k.toldSince(t, n, nil)
	})
	t.Run("the own question set aside, the question shown asked as the agent's own", func(t *testing.T) {
		k := &keptLog{}
		s, _, _ := asking(t, Options{OnChange: k.hook})
		a := askOwn(t, s, "Which database?", dbChoices...)
		reportState(t, s, AttentionWorking, "")
		sizes := []Option{{Label: "Small", Input: "s\r"}, {Label: "Large", Input: "l\r"}}
		s.SetAttentionFull(AttentionNeedsInput, "Which size?", SourceAPI, KindPrompt, sizes)
		b := s.Info().Attention
		if b.ID == "" || b.ID == a.ID {
			t.Fatalf("the second question: %+v", b)
		}
		if kept := s.Info().Kept; !slices.Equal(kept, []string{a.ID}) {
			t.Fatalf("B shown: kept %q", kept)
		}
		n := k.count()
		// B asked again, the same in every field: adopted as the agent's
		// own, in A's place, which the session keeps no longer.
		s.Ask("Which size?", SourceAPI, KindPrompt, sizes)
		if info := s.Info(); info.Attention.ID != b.ID || !info.Attention.Asked() || len(info.Kept) != 0 {
			t.Fatalf("B asked again: %+v, kept %q", info.Attention, info.Kept)
		}
		k.toldSince(t, n, nil)
	})
	t.Run("the own question held over, gone under a question on the screen", func(t *testing.T) {
		k := &keptLog{}
		s, p, _ := asking(t, Options{OnChange: k.hook,
			TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), TrustAnswers: []Option{{Label: "Yes", Input: "\r"}}})
		owner := ownerOf(t, s)
		a := askOwn(t, s, "Which database?", dbChoices...)
		go holdNow(s)
		heldShows(t, s)
		s.fireTrust("Trust this folder?")
		waitAttention(t, s, func(att Attention) bool { return att.Source == SourceTrust })
		if kept := s.Info().Kept; !slices.Equal(kept, []string{a.ID}) {
			t.Fatalf("held over under the question: kept %q", kept)
		}
		n := k.count()
		if err := s.Input(owner, []byte{0x15}); err != nil {
			t.Fatal(err)
		}
		if got := written(t, p); got != "\x15" {
			t.Fatalf("written %q", got)
		}
		if s.Held() {
			t.Fatal("the keys did not end the hold")
		}
		// A can show again nowhere: neither shown nor kept, while the
		// question on the screen still shows.
		if info := s.Info(); info.Attention.Source != SourceTrust || len(info.Kept) != 0 {
			t.Fatalf("the hold ended: %+v, kept %q", info.Attention, info.Kept)
		}
		k.toldSince(t, n, nil)
		reportState(t, s, AttentionDone, "done for now")
		if info := s.Info(); info.Attention.ID == a.ID || len(info.Kept) != 0 {
			t.Fatalf("at the done: %+v, kept %q, A shown again", info.Attention, info.Kept)
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

// noneWritten fails when the process holds a write. A refused answer is
// refused before its keys are queued, and fakeProc.Write hands a write to
// input before it returns, so once the calls under test have returned
// whatever they wrote is there: no wait is needed to see that nothing is.
func noneWritten(t *testing.T, p *fakeProc) {
	t.Helper()
	select {
	case b := <-p.input:
		t.Fatalf("the process was written %q", b)
	default:
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
	noneWritten(t, p)
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
	noneWritten(t, p)
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
	noneWritten(t, p)
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
		noneWritten(t, p)
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
		noneWritten(t, p)
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
		noneWritten(t, p)
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
		noneWritten(t, p)
		shows(t, s, AttentionNone, "answered")
	}
}

// An actor's keys whose write has begun when its call ends (the program
// not reading them yet) are accepted, ErrSubmitPending, and not taken
// back: they land as the program reads, and answer the question then, as
// by the actor.
func TestAnswerAsPending(t *testing.T) {
	s, p, _ := asking(t, Options{})
	q := askOwn(t, s, "Which database?", dbChoices...)
	begun, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	p.onWrite = func() {
		once.Do(func() {
			close(begun)
			<-gate
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	answered := make(chan error, 1)
	go func() { answered <- s.AnswerAs(ctx, q.ID, "Postgres\r", Actor{Name: "Ada"}) }()
	select {
	case <-begun:
	case <-time.After(5 * time.Second):
		t.Fatal("the keys' write never began")
	}
	cancel()
	select {
	case err := <-answered:
		if !errors.Is(err, ErrSubmitPending) {
			t.Fatalf("keys begun as the call ends: %v, want ErrSubmitPending", err)
		}
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatal("AnswerAs still waits on a write begun")
	}
	if a := s.Info().LastAnswer; a != nil {
		t.Fatalf("answered before the keys landed: %+v", a)
	}
	close(gate)
	if got := written(t, p); got != "Postgres\r" {
		t.Fatalf("written %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for a := s.Info().LastAnswer; a == nil || a.ByName != "Ada"; a = s.Info().LastAnswer {
		if time.Now().After(deadline) {
			t.Fatalf("the landed keys never answered the question: %+v", a)
		}
		time.Sleep(2 * time.Millisecond)
	}
	shows(t, s, AttentionNone, "answered")
}

// An actor's keys waiting behind a write the program does not take when
// the actor is withdrawn (Actor.Withdrawn closed, as an extension's Host's
// try given up closes it) are withdrawn as a revoked link's are: never
// written, AnswerAs answers ErrRevoked at once (its writes wait an hour for
// the program: only the withdrawal ends this one), and the question still
// waits, a person's answer the next write the program gets.
func TestAnswerAsWithdrawn(t *testing.T) {
	s, p, _ := asking(t, Options{InputWait: time.Hour})
	owner := ownerOf(t, s)
	q := askOwn(t, s, "Which database?", dbChoices...)
	begun, gate := make(chan struct{}), make(chan struct{})
	var once, opened sync.Once
	open := func() { opened.Do(func() { close(gate) }) }
	t.Cleanup(open)
	p.onWrite = func() {
		once.Do(func() {
			close(begun)
			<-gate
		})
	}
	first := make(chan error, 1)
	go func() { first <- s.Input(owner, []byte("x")) }()
	select {
	case <-begun:
	case <-time.After(5 * time.Second):
		t.Fatal("the first write never began")
	}
	withdrawn := make(chan struct{})
	answered := make(chan error, 1)
	go func() {
		answered <- s.AnswerAs(context.Background(), q.ID, "Postgres\r", Actor{Name: "Ada", Withdrawn: withdrawn})
	}()
	// Queued behind the held write: its wait for the writer beside the
	// first write's.
	deadline := time.Now().Add(5 * time.Second)
	for goroutinesIn("session.(*Local).writeProc(") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the keys never waited behind the first write")
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(withdrawn)
	select {
	case err := <-answered:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("withdrawn keys: %v, want ErrRevoked", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("withdrawn keys still wait for the writer")
	}
	open()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "x" {
		t.Fatalf("written %q, want the first write", got)
	}
	showsOwn(t, s, q, "after the withdrawal")
	if a := s.Info().LastAnswer; a != nil {
		t.Fatalf("answered by withdrawn keys: %+v", a)
	}
	if err := s.AnswerInput(context.Background(), owner, q.ID, []byte("SQLite\r")); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "SQLite\r" {
		t.Fatalf("written %q, want the person's answer next", got)
	}
}

// goroutinesIn counts the goroutines whose stack holds frame.
func goroutinesIn(frame string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte(frame))
		}
		buf = make([]byte, 2*len(buf))
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
