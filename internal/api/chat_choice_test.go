package api

import (
	"context"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A choice's keys answering a chat's question, by every caller that brings
// an answer's keys to a session (#1150): the session takes exactly one
// choice's keys as that choice's words, sent as the answer; the caller's own
// checks stand as they are.

var pearsOrPlums = []session.Option{{Label: "Pears", Input: "Pears\r"}, {Label: "Plums", Input: "Plums\r"}}

// askFruit asks the chat l "Pears or plums?" with its two choices.
func askFruit(t *testing.T, l *session.Local) string {
	t.Helper()
	q, err := l.TryAskID("Pears or plums?", session.SourceAPI, session.KindPrompt, pearsOrPlums)
	if err != nil || q == "" {
		t.Fatalf("ask %q: %v", q, err)
	}
	return q
}

// sentOnce waits for the chat's one message, checks its words and
// acknowledges it as the agent does.
func sentOnce(t *testing.T, f *sessiontest.FakeProtocol, words string) {
	t.Helper()
	until(t, "the answer sent to the agent", func() bool { return len(f.Sent()) > 0 })
	sent := f.Sent()
	if len(sent) != 1 || sent[0].Text != words {
		t.Fatalf("sent %+v", sent)
	}
	f.Accepted(sent[0].Ref)
}

// answeredBy checks the question's result names who answered it and the
// transcript's entry for it carries no text.
func answeredBy(t *testing.T, l *session.Local, q, name string) {
	t.Helper()
	until(t, "the question resolved", func() bool { _, ok := l.QuestionResult(q); return ok })
	if r, _ := l.QuestionResult(q); r.ByName != name {
		t.Fatalf("answered by %+v", r)
	}
	entries, _ := l.Transcript(0, 0)
	for _, e := range entries {
		if e.Kind == session.EntryPrompt && e.Answers == q && e.Text != "" {
			t.Fatalf("the answer's text is kept: %+v", e)
		}
	}
}

// A viewer's answer message: the operator's control connection.
func TestAViewersChoiceKeysAnswerAChat(t *testing.T) {
	e := newTestEnv(t, nil)
	id, l, f := chatSession(t, e)
	q := askFruit(t, l)
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	view := dialViewer(t, e, id, lo["token"].(string))
	helloChat(view, ^uint64(0))
	view.until("ready", controlOf(proto.CtlReady), nil)
	view.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "v1", Question: q, Input: "Plums\r"}))
	if c := view.until("the view link's word", func(c chatFrame) bool { return c.names("v1") }, nil); c.m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("a view link's answer: %v", c.m)
	}
	op := dialViewer(t, e, id, adminToken)
	helloChat(op, ^uint64(0))
	op.until("ready", controlOf(proto.CtlReady), nil)
	op.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "k0", Question: q, Input: "Apples\r"}))
	if c := op.until("no choice's word", func(c chatFrame) bool { return c.names("k0") }, nil); c.m["code"] != proto.ErrCodeNotTerminal {
		t.Fatalf("keys of no choice: %v", c.m)
	}
	op.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "k1", Question: q, Input: "Plums\r"}))
	sentOnce(t, f, "Plums")
	if c := op.until("the answer's word", func(c chatFrame) bool { return c.names("k1") }, nil); c.m["t"] != proto.CtlAnswerOK {
		t.Fatalf("the operator's answer: %v", c.m)
	}
	answeredBy(t, l, q, "guest")
}

// A session decision link's lane, for its own session: its own answer.
func TestALinksChoiceKeysAnswerItsChat(t *testing.T) {
	e := newTestEnv(t, nil)
	id, l, f := chatSession(t, e)
	q := askFruit(t, l)
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	ref := questionRef(id, q)
	if m := w.decide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "Apples\r", OpID: "no-1"}); !refusedWith(m, proto.DecideInvalid) || m["message"] != "not one of the question's choices: nothing was sent" {
		t.Fatalf("keys of no choice: %v", m)
	}
	rid := w.sendDecide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "Plums\r", OpID: "ans-1"})
	sentOnce(t, f, "Plums")
	if _, m := w.reply(rid); !isOK(m) || m["receipt"] != q {
		t.Fatalf("the answer: %v", m)
	}
	answeredBy(t, l, q, "phone")
}

// A run decision link's desk, for a member's session: through the question's
// claim as the link (AnswerAs).
func TestADesksChoiceKeysAnswerAMembersChat(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	sid := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	d, _ := e.srv.registry.Get(sid)
	l := d.(*session.Local)
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	q := askFruit(t, l)
	_, tok := e.decisionLink(t, "/api/runs/"+run.ID+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run.ID+"/desk", tok)
	ref := questionRef(sid, q)
	rid := desk.sendDecide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "Pears\r", OpID: "desk-1"})
	sentOnce(t, f, "Pears")
	if _, m := desk.reply(rid); !isOK(m) || m["receipt"] != q {
		t.Fatalf("the desk's answer: %v", m)
	}
	answeredBy(t, l, q, "phone")
}

// A provider's Host and a decision router (submitTo): a choice's keys as
// the principal, through AnswerAs.
func TestAProvidersChoiceKeysAnswerAChat(t *testing.T) {
	e := newTestEnv(t, nil)
	id, l, f := chatSession(t, e)
	q := askFruit(t, l)
	got := make(chan ext.SubmitResult, 1)
	errs := make(chan error, 1)
	go func() {
		res, err, _ := e.srv.submitTo(context.Background(), l, ext.SubmitRequest{Session: id, Question: q, Input: "Plums\r", By: ext.Principal{Subject: "u-1", Name: "Ops desk"}}, nil)
		got <- res
		errs <- err
	}()
	sentOnce(t, f, "Plums")
	if res, err := <-got, <-errs; err != nil || res.Status != "entered" {
		t.Fatalf("the provider's answer: %+v, %v", res, err)
	}
	answeredBy(t, l, q, "Ops desk")
	// Keys of no choice are refused as the provider's are on a terminal.
	q2 := askFruit(t, l)
	if _, err, _ := e.srv.submitTo(context.Background(), l, ext.SubmitRequest{Session: id, Question: q2, Input: "Apples\r", By: ext.Principal{Name: "Ops desk"}}, nil); err == nil {
		t.Fatal("keys of no choice were taken")
	}
	if n := len(f.Sent()); n != 1 {
		t.Fatalf("%d sent", n)
	}
}

// The decision page's item says what a choice does on a chat: no terminal.
func TestAChatsQuestionItemSaysItSends(t *testing.T) {
	e := newTestEnv(t, nil)
	id, l, _ := chatSession(t, e)
	q := askFruit(t, l)
	resp, out := e.do("GET", "/api/inbox/item?ref="+questionRef(id, q), adminToken, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the item: %d %v", resp.StatusCode, out)
	}
	item := out["item"].(map[string]any)
	var scopes []string
	for _, a := range item["actions"].([]any) {
		scopes = append(scopes, a.(map[string]any)["scope"].(string))
	}
	want := []string{"Sends the choice 'Pears' to chat as your answer", "Sends the choice 'Plums' to chat as your answer", "Sends the line you write to chat as your answer"}
	if strings.Join(scopes, "|") != strings.Join(want, "|") {
		t.Fatalf("scopes %q", scopes)
	}
}
