package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A choice's keys on a chat (#1150): a reply naming the question shown with
// exactly one of its choices' keys is that choice's words, sent as a line
// answering the question; anything else stays refused.

// fruit is a chat's question with two choices, as the agent's own ask with
// choices makes it.
func fruit(t *testing.T, s *Local, options ...Option) string {
	t.Helper()
	if len(options) == 0 {
		options = []Option{{Label: "Pears", Input: "Pears\r"}, {Label: "Plums", Input: "Plums\r"}}
	}
	id, err := s.TryAskID("Pears or plums?", SourceAPI, KindPrompt, options)
	if err != nil || id == "" {
		t.Fatalf("ask %q: %v", id, err)
	}
	return id
}

// resultOf waits for the question's kept result; the bound only bounds a hang.
func resultOf(t *testing.T, s *Local, id string) Answer {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		if r, ok := s.QuestionResult(id); ok {
			return r
		}
		select {
		case <-deadline:
			t.Fatal("the question was not resolved")
		case <-time.After(time.Millisecond):
		}
	}
}

// answerEntry is the transcript's prompt entry that answers id.
func answerEntry(t *testing.T, s *Local, id string) TranscriptEntry {
	t.Helper()
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryPrompt && e.Answers == id {
			return e
		}
	}
	t.Fatalf("no entry answers %s: %+v", id, allEntries(t, s))
	return TranscriptEntry{}
}

func TestAChatTakesAChoicesKeysAsItsWords(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	id := fruit(t, s)
	sub := attachRole(t, s, RoleControl)
	word := make(chan error, 1)
	err := s.AnswerInputReq(context.Background(), sub, id, "r1", []byte("Plums\r"), func(err error) { word <- err })
	if !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("the reply: %v", err)
	}
	waitCalls(t, f, "the write", sends(1))
	m := f.Sent()[0]
	if m.Text != "Plums" {
		t.Fatalf("sent %q", m.Text)
	}
	f.Accepted(m.Ref)
	select {
	case err := <-word:
		if err != nil {
			t.Fatalf("the word: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no word for the reply")
	}
	// The record is a line's that answers: no text, the person's name, and
	// the question resolved by whom with which request.
	e := answerEntry(t, s, id)
	if e.Text != "" || e.By != "person" || e.ByName != "Ada" {
		t.Fatalf("entry %+v", e)
	}
	if r := resultOf(t, s, id); r.ByName != "Ada" || r.ReqID != "r1" {
		t.Fatalf("result %+v", r)
	}
}

func TestAChatTakesAnActorsChoiceAsItsWords(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	id := fruit(t, s)
	got := make(chan error, 1)
	go func() {
		got <- s.AnswerAs(context.Background(), id, "Pears\r", Actor{Name: "Ops desk", ReqID: "d1"})
	}()
	waitCalls(t, f, "the write", sends(1))
	m := f.Sent()[0]
	if m.Text != "Pears" {
		t.Fatalf("sent %q", m.Text)
	}
	// AnswerAs waits for the line's word, as a link's line is waited for.
	select {
	case err := <-got:
		t.Fatalf("answered before the agent took it: %v", err)
	default:
	}
	f.Accepted(m.Ref)
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("AnswerAs: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("AnswerAs did not return")
	}
	if e := answerEntry(t, s, id); e.Text != "" || e.ByName != "Ops desk" {
		t.Fatalf("entry %+v", e)
	}
	if r := resultOf(t, s, id); r.ByName != "Ops desk" || r.ReqID != "d1" {
		t.Fatalf("result %+v", r)
	}
}

// Everything that is not exactly one choice's keys of the question shown,
// answered by a controller, is refused as before, nothing sent.
func TestAChatRefusesKeysThatAreNoChoice(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	ctl := attachRole(t, s, RoleControl)
	view := attachRole(t, s, RoleView)
	id := fruit(t, s)
	reply := func(sub *Subscription, q string, keys string) error {
		return s.AnswerInputReq(context.Background(), sub, q, "", []byte(keys), func(error) {})
	}
	for _, c := range []struct {
		what string
		err  error
		do   func() error
	}{
		{"keys of no choice", ErrNotTerminal, func() error { return reply(ctl, id, "Apples\r") }},
		{"a choice's keys in part", ErrNotTerminal, func() error { return reply(ctl, id, "Plums") }},
		{"no keys", ErrNotTerminal, func() error { return reply(ctl, id, "") }},
		{"keys typed at the chat, answering nothing", ErrNotTerminal, func() error { return s.Input(ctl, []byte("Plums\r")) }},
		{"a viewer without control", ErrReadOnly, func() error { return reply(view, id, "Plums\r") }},
		{"another question", ErrQuestionAnswered, func() error { return reply(ctl, "no-such-question", "Plums\r") }},
		{"an actor's keys of no choice", ErrNotTerminal, func() error {
			return s.AnswerAs(context.Background(), id, "Apples\r", Actor{Name: "Ops desk"})
		}},
	} {
		if err := c.do(); !errors.Is(err, c.err) {
			t.Fatalf("%s: %v, want %v", c.what, err, c.err)
		}
	}
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("%d refused replies were sent", n)
	}
	// Two choices with the same keys: neither is the one meant.
	s2, _, f2 := protocolSession(t, Options{})
	ctl2 := attachRole(t, s2, RoleControl)
	twice := fruit(t, s2, Option{Label: "Pears", Input: "p\r"}, Option{Label: "Plums", Input: "p\r"})
	if err := s2.AnswerInputReq(context.Background(), ctl2, twice, "", []byte("p\r"), func(error) {}); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("keys of two choices: %v", err)
	}
	if n := len(f2.Sent()); n != 0 {
		t.Fatalf("%d replies sent for keys of two choices", n)
	}
}

// A question answered once refuses the next reply's keys, as a line's.
func TestAChatsAnsweredQuestionRefusesAChoice(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	ctl := attachRole(t, s, RoleControl)
	id := fruit(t, s)
	if err := s.AnswerInputReq(context.Background(), ctl, id, "", []byte("Pears\r"), func(error) {}); !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("the first: %v", err)
	}
	if err := s.AnswerInputReq(context.Background(), ctl, id, "", []byte("Plums\r"), func(error) {}); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("the second: %v", err)
	}
	waitCalls(t, f, "the write", sends(1))
	if sent := f.Sent(); len(sent) != 1 || sent[0].Text != "Pears" {
		t.Fatalf("sent %+v", sent)
	}
}

// A permission request of the agent's own keeps its own buttons.
func TestAChatsPermissionQuestionTakesNoChoiceKeys(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.ToolStarted(ToolStep{Ref: "call-1", Name: "Bash", Input: []byte(`{"command":"ls"}`)})
	f.Permission(PermissionRequest{Ref: "req-1", Call: "call-1", Tool: "Bash", Input: []byte(`{"command":"ls"}`)})
	req := lastEntry(t, s)
	if req.Kind != EntryPermission || req.Question == "" {
		t.Fatalf("request entry %+v", req)
	}
	ctl := attachRole(t, s, RoleControl)
	if err := s.AnswerInputReq(context.Background(), ctl, req.Question, "", []byte("1"), func(error) {}); !errors.Is(err, ErrPermissionCard) {
		t.Fatalf("keys for a permission request: %v", err)
	}
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("%d sent", n)
	}
}

// A move between the ways takes the session: a choice's keys are refused
// moving while it is under way, nothing sent; once it has ended they are
// taken again. (The line they become is then Submit's, whose own look
// under the move's lock refuses a move begun after this one.)
func TestAChatsChoiceIsRefusedWhileMoving(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	ctl := attachRole(t, s, RoleControl)
	id := fruit(t, s)
	s.mu.Lock()
	s.beginMoveLocked()
	s.mu.Unlock()
	if err := s.AnswerInputReq(context.Background(), ctl, id, "", []byte("Plums\r"), func(error) {}); !errors.Is(err, ErrMoving) {
		t.Fatalf("while moving: %v", err)
	}
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("%d sent while moving", n)
	}
	s.mu.Lock()
	done := s.moveDone
	s.moveDone = nil
	s.mu.Unlock()
	close(done)
	if err := s.AnswerInputReq(context.Background(), ctl, id, "", []byte("Plums\r"), func(error) {}); !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("after the move: %v", err)
	}
}
