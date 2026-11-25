package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Who answers a chat's permission request (docs/plans/chat-or-terminal.md,
// design 7, row 6): the operator alone, bound to the request.

// operator attaches the operator's own connection.
func operator(t *testing.T, s *Local) *Subscription {
	t.Helper()
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Ada", Owner: true, Cols: 100, Rows: 30}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	return sub
}

func answerOf(e TranscriptEntry, choice string) proto.PermissionAnswer {
	return proto.PermissionAnswer{T: proto.CtlPermissionAnswer, ReqID: "r1", Question: e.Question, Call: e.Call, Sha256: e.InputSha256, Choice: choice}
}

// I18: open requests hold the attention: the oldest shows, no other
// question replaces or withdraws it, each stays answerable, and answering
// shows the next, then the most recent other question.
func TestPermissionHoldsTheAttention(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	op := operator(t, s)
	announce(f, "c-1", "Bash", bash("one"))
	announce(f, "c-2", "Edit", `{"file_path":"a.go"}`)
	first := ask(s, "c-1", "Bash", bash("one"))
	r1 := openRequest(t, s, "c-1")
	second := ask(s, "c-2", "Edit", `{"file_path":"a.go"}`)
	r2 := openRequest(t, s, "c-2")
	shows := func(what, id string) {
		t.Helper()
		att := s.Info().Attention
		if att.ID != id || att.State != AttentionNeedsInput {
			t.Fatalf("%s: the attention %+v, want %s", what, att, id)
		}
	}
	shows("two open", r1.Question)
	if att := s.Info().Attention; att.Source != SourceProtocol || att.Kind != KindPermission || len(att.Options) != 0 || att.Message != "Run a command?" {
		t.Fatalf("the request's attention %+v", att)
	}
	// A crew blocker, the agent's own ask and a decision's mirror.
	blocker, err := s.TryAskID("Task t1 is blocked", SourceAPI, "", nil)
	if err != nil || blocker == "" {
		t.Fatalf("the blocker's ask: %q %v", blocker, err)
	}
	if err := s.TryAsk("Which branch?", SourceAPI, "", []Option{{Label: "main", Input: "main\r"}}); err != nil {
		t.Fatal(err)
	}
	decided, err := s.TryAskDecision("dr-7", "Ship it?")
	if err != nil || decided == "" {
		t.Fatalf("the decision's ask: %q %v", decided, err)
	}
	s.WithdrawQuestion(r1.Question)
	s.SetAttentionFull(AttentionNone, "", SourceAdmin, "", nil)
	shows("after every other setter", r1.Question)
	if o, _ := s.OpenPermissions(); o != 2 {
		t.Fatalf("%d open", o)
	}
	// The second is answerable while the first shows.
	if err := s.AnswerPermission(context.Background(), op, answerOf(r2, proto.PermissionDeny)); err != nil {
		t.Fatalf("answering the second while the first shows: %v", err)
	}
	if v := second.returned(t); v.Allow {
		t.Fatalf("a deny delivered as %+v", v)
	}
	shows("the second answered", r1.Question)
	if err := s.AnswerPermission(context.Background(), op, answerOf(r1, proto.PermissionAllowOnce)); err != nil {
		t.Fatal(err)
	}
	if v := first.returned(t); !v.Allow {
		t.Fatalf("the operator's allow delivered as %+v", v)
	}
	// The list empty: the changes kept aside are applied in order, as they
	// would have been with no request open: the admin's clear, last,
	// leaves nothing showing.
	if att := s.Info().Attention; att.State != AttentionNone || att.Source != SourceAdmin {
		t.Fatalf("after the list %+v, want the admin's clear", att)
	}
	_ = blocker

	// Two open again: answering the oldest shows the next, and once none
	// is open the most recent other question shows.
	announce(f, "c-3", "Bash", bash("three"))
	announce(f, "c-4", "Bash", bash("four"))
	ask(s, "c-3", "Bash", bash("three"))
	r3 := openRequest(t, s, "c-3")
	ask(s, "c-4", "Bash", bash("four"))
	r4 := openRequest(t, s, "c-4")
	shows("two more", r3.Question)
	again, err := s.TryAskDecision("dr-8", "Merge it?")
	if err != nil || again == "" {
		t.Fatalf("the decision's ask: %q %v", again, err)
	}
	if err := s.AnswerPermission(context.Background(), op, answerOf(r3, proto.PermissionDeny)); err != nil {
		t.Fatal(err)
	}
	shows("the oldest answered", r4.Question)
	if err := s.AnswerPermission(context.Background(), op, answerOf(r4, proto.PermissionDeny)); err != nil {
		t.Fatal(err)
	}
	if att := s.Info().Attention; att.ID != again || att.Decision != "dr-8" {
		t.Fatalf("after the list %+v, want the decision's question %s", att, again)
	}
	_ = decided
}

// I18, the changes kept aside: a plain report (no options of its own) made
// while a request shows is applied once none is open, as it would have been
// with none open; past maxAside kept, the oldest that is no question goes,
// a question kept whose id its asker holds.
func TestPermissionKeepsWhatWasSaidMeanwhile(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	op := operator(t, s)
	announce(f, "c-1", "Bash", bash("one"))
	ask(s, "c-1", "Bash", bash("one"))
	r := openRequest(t, s, "c-1")
	s.SetAttention(AttentionWorking, "step", SourceAPI)
	if err := s.AnswerPermission(context.Background(), op, answerOf(r, proto.PermissionDeny)); err != nil {
		t.Fatal(err)
	}
	if att := s.Info().Attention; att.State != AttentionWorking || att.Message != "step" {
		t.Fatalf("a report made while the request showed: %+v", att)
	}

	announce(f, "c-2", "Bash", bash("two"))
	ask(s, "c-2", "Bash", bash("two"))
	r = openRequest(t, s, "c-2")
	q, err := s.TryAskID("Task t1 is blocked", SourceAPI, "", nil)
	if err != nil || q == "" {
		t.Fatalf("the ask: %q %v", q, err)
	}
	for i := range maxAside {
		s.SetAttention(AttentionWorking, fmt.Sprintf("step %d", i), SourceAPI)
	}
	s.mu.Lock()
	kept := s.way.Load().aside
	n, hasQ := len(kept), slices.ContainsFunc(kept, func(c asideChange) bool { return c.opt.id == q })
	s.mu.Unlock()
	if n != maxAside || !hasQ {
		t.Fatalf("%d kept, the question among them: %v", n, hasQ)
	}
	if err := s.AnswerPermission(context.Background(), op, answerOf(r, proto.PermissionDeny)); err != nil {
		t.Fatal(err)
	}
	if att := s.Info().Attention; att.State != AttentionWorking || att.Message != fmt.Sprintf("step %d", maxAside-1) {
		t.Fatalf("after the list %+v", att)
	}
	if !slices.Contains(s.Info().Kept, q) {
		t.Fatalf("the question asked meanwhile is not kept: %+v", s.Info().Kept)
	}
}

// I9: an answer reaches only the request it was given for: another call,
// another digest or a call with a result answers stale_request and leaves
// the request open; the matching one is handed over once, after its
// record, and allowed only once delivered; a POST gone first is withdrawn.
func TestPermissionAnswerBinding(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	op := operator(t, s)
	announce(f, "c-1", "Bash", bash("make"))
	a := ask(s, "c-1", "Bash", bash("make"))
	r := openRequest(t, s, "c-1")
	for name, ans := range map[string]proto.PermissionAnswer{
		"another call":   {Call: "c-2", Sha256: r.InputSha256},
		"another digest": {Call: r.Call, Sha256: inputDigest("Bash", []byte(bash("make install")))},
	} {
		ans.T, ans.ReqID, ans.Question, ans.Choice = proto.CtlPermissionAnswer, "r1", r.Question, proto.PermissionAllowOnce
		if err := s.AnswerPermission(context.Background(), op, ans); !errors.Is(err, ErrStaleRequest) || err.Error() != StaleRequestWords {
			t.Fatalf("%s: %v", name, err)
		}
		if o, _ := s.OpenPermissions(); o != 1 || !a.held() {
			t.Fatalf("%s: the request no longer open", name)
		}
	}
	// The matching answer: who answered recorded first, then handed over
	// once, then allowed once delivered.
	if err := s.AnswerPermission(context.Background(), op, answerOf(r, proto.PermissionAllowOnce)); err != nil {
		t.Fatal(err)
	}
	if v := a.returned(t); !v.Allow || v.Cancel {
		t.Fatalf("delivered %+v", v)
	}
	if e := entryOf(t, s, r.Seq); e.State != PermissionAllowed || e.ByName != "Ada" {
		t.Fatalf("the entry after delivery %+v", e)
	}
	if res, ok := s.QuestionResult(r.Question); !ok || res.ByName != "Ada" || res.ReqID != "r1" {
		t.Fatalf("the question's kept result %+v %v", res, ok)
	}
	if err := s.AnswerPermission(context.Background(), op, answerOf(r, proto.PermissionAllowOnce)); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a second answer: %v", err)
	}

	// A call with a result: the stream's result withdraws it first.
	announce(f, "c-3", "Bash", bash("ls"))
	b := ask(s, "c-3", "Bash", bash("ls"))
	r3 := openRequest(t, s, "c-3")
	f.ToolEnded("c-3", ToolResult{Status: ToolOK})
	if err := s.AnswerPermission(context.Background(), op, answerOf(r3, proto.PermissionAllowOnce)); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("an answer after the call's result: %v", err)
	}
	if v := b.returned(t); v.Allow {
		t.Fatalf("a call with a result allowed: %+v", v)
	}

	// The POST gone before delivery: withdrawn, never allowed.
	announce(f, "c-4", "Bash", bash("pwd"))
	c := askLosing(s, "c-4", "Bash", bash("pwd"), true)
	r4 := openRequest(t, s, "c-4")
	if err := s.AnswerPermission(context.Background(), op, answerOf(r4, proto.PermissionAllowOnce)); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("an answer not delivered: %v", err)
	}
	c.returned(t)
	if e := entryOf(t, s, r4.Seq); e.State != PermissionWithdrawn || e.Why != RequestLostWords {
		t.Fatalf("an allow not delivered recorded %+v", e)
	}
	// The activity says the allow was recorded, then that it was not
	// delivered.
	var lines []string
	for _, a := range s.Activity() {
		lines = append(lines, a.Message)
	}
	if i := slices.Index(lines, NotDeliveredWords); i < 0 || !slices.Contains(lines[:i], "Allowed once: Run a command?") {
		t.Fatalf("the activity %q", lines)
	}
}

// Two operator answers at once: one is taken, the other
// question_answered. The first is held at its claim (the seam
// permissionClaimed) until the second has been answered, so the claim is
// what decides, every run.
func TestPermissionFirstAnswerWins(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	op := operator(t, s)
	announce(f, "c-1", "Bash", bash("make"))
	a := ask(s, "c-1", "Bash", bash("make"))
	r := openRequest(t, s, "c-1")
	var once sync.Once
	var secondErr error
	seam := func() {
		once.Do(func() {
			// The second answer, made and finished while the first holds
			// its claim; the bound only bounds a hang.
			done := make(chan error, 1)
			go func() { done <- s.AnswerPermission(context.Background(), op, answerOf(r, proto.PermissionDeny)) }()
			select {
			case secondErr = <-done:
			case <-time.After(30 * time.Second):
				secondErr = errors.New("the second answer never returned")
			}
		})
	}
	s.permissionClaimed.Store(&seam)
	if err := s.AnswerPermission(context.Background(), op, answerOf(r, proto.PermissionAllowOnce)); err != nil {
		t.Fatalf("the first answer: %v", err)
	}
	if !errors.Is(secondErr, ErrQuestionAnswered) {
		t.Fatalf("the second answer: %v", secondErr)
	}
	if v := a.returned(t); !v.Allow {
		t.Fatalf("delivered %+v", v)
	}
}

// A ToolEnded for the pending call with no MCP cancellation withdraws it
// with "Answered by the agent's own rules.", the held POST returns with the
// withdrawal's deny, none stays open, and a later answer is
// question_answered; a POST whose client goes with no result is "The
// request was lost."
func TestPermissionAnsweredElsewhere(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	op := operator(t, s)
	announce(f, "c-1", "Bash", bash("make"))
	a := ask(s, "c-1", "Bash", bash("make"))
	r := openRequest(t, s, "c-1")
	f.ToolEnded("c-1", ToolResult{Status: ToolOK})
	if v := a.returned(t); v.Allow || !v.Cancel || v.Message != AnsweredByAgentWords {
		t.Fatalf("the held POST answered %+v", v)
	}
	if e := entryOf(t, s, r.Seq); e.State != PermissionWithdrawn || e.Why != AnsweredByAgentWords {
		t.Fatalf("its entry %+v", e)
	}
	if o, _ := s.OpenPermissions(); o != 0 {
		t.Fatalf("%d open", o)
	}
	if err := s.AnswerPermission(context.Background(), op, answerOf(r, proto.PermissionAllowOnce)); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a later answer: %v", err)
	}

	announce(f, "c-2", "Bash", bash("ls"))
	b := ask(s, "c-2", "Bash", bash("ls"))
	r2 := openRequest(t, s, "c-2")
	b.cancel()
	b.returned0(t)
	if e := entryOf(t, s, r2.Seq); e.State != PermissionWithdrawn || e.Why != RequestLostWords {
		t.Fatalf("a POST gone %+v", e)
	}
	if err := s.AnswerPermission(context.Background(), op, answerOf(r2, proto.PermissionAllowOnce)); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("an answer after the POST went: %v", err)
	}
}

// Every path but the operator's permission_answer refuses a permission
// request: a line or a choice naming it (ErrPermissionCard), and a
// permission_answer from any connection that is not the operator's own
// (ErrNotAnswerer). None reaches a verdict.
func TestPermissionRefusedOnEveryOtherPath(t *testing.T) {
	s, f, _ := permissionSession(t, Options{})
	announce(f, "c-1", "Bash", bash("make"))
	a := ask(s, "c-1", "Bash", bash("make"))
	r := openRequest(t, s, "c-1")
	ctl := attachRole(t, s, RoleControl)
	view := attachRole(t, s, RoleView)
	link, err := s.AttachWith(AttachOptions{Role: RoleControl, LinkID: "link-1", Owner: true, Name: "Link"}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(link) })
	decision, err := s.AttachWith(AttachOptions{Role: RoleControl, LinkID: "dl-1", Decisions: true, Name: "Desk"}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(decision) })
	for _, by := range []*Subscription{ctl, view, link, decision} {
		for _, line := range []string{"y", "allow_once"} {
			// A view role is refused as read-only first.
			if _, err := s.Submit(context.Background(), Submission{Text: line, Question: r.Question, By: by}); !errors.Is(err, ErrPermissionCard) && !(by == view && errors.Is(err, ErrReadOnly)) {
				t.Fatalf("a line by %s: %v", by.Name, err)
			}
			if err := s.AnswerInput(context.Background(), by, r.Question, []byte(line+"\r")); !errors.Is(err, ErrPermissionCard) {
				t.Fatalf("a choice by %s: %v", by.Name, err)
			}
		}
		for _, choice := range []string{proto.PermissionAllowOnce, proto.PermissionDeny} {
			if err := s.AnswerPermission(context.Background(), by, answerOf(r, choice)); !errors.Is(err, ErrNotAnswerer) || err.Error() != NotAnswererWords {
				t.Fatalf("permission_answer by %s: %v", by.Name, err)
			}
		}
	}
	if err := s.AnswerPermission(context.Background(), nil, answerOf(r, proto.PermissionAllowOnce)); !errors.Is(err, ErrNotAnswerer) {
		t.Fatalf("permission_answer by no connection: %v", err)
	}
	for _, input := range []string{"y\r", "allow_once"} {
		if err := s.AnswerAs(context.Background(), r.Question, input, Actor{Name: "router"}); !errors.Is(err, ErrPermissionCard) {
			t.Fatalf("AnswerAs %q: %v", input, err)
		}
	}
	if _, err := s.Submit(context.Background(), Submission{Text: "yes", Question: r.Question, ByName: "provider"}); !errors.Is(err, ErrPermissionCard) {
		t.Fatalf("a provider's line: %v", err)
	}
	if !a.held() || len(f.Sent()) != 0 {
		t.Fatalf("a refused answer reached the request: %+v, sent %v", a.verdicts(), f.Sent())
	}
	if o, _ := s.OpenPermissions(); o != 1 {
		t.Fatalf("%d open", o)
	}
	if !strings.Contains(s.Info().Attention.ID, r.Question) {
		t.Fatalf("the request no longer shows: %+v", s.Info().Attention)
	}
}
