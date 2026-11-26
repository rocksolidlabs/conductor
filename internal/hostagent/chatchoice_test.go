package hostagent

import (
	"testing"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A choice's keys answering a chat's question through a host's paths
// (#1150): a viewer's answer relayed by a switchyard to a published chat,
// and a routed answer_as. The session takes exactly one choice's keys as
// that choice's words; a view-only guest still answers nothing.

var pearsOrPlums = []session.Option{{Label: "Pears", Input: "Pears\r"}, {Label: "Plums", Input: "Plums\r"}}

func askFruit(t *testing.T, l *session.Local) string {
	t.Helper()
	q, err := l.TryAskID("Pears or plums?", session.SourceAPI, session.KindPrompt, pearsOrPlums)
	if err != nil || q == "" {
		t.Fatalf("ask %q: %v", q, err)
	}
	return q
}

func TestARelayedViewersChoiceKeysAnswerAChat(t *testing.T) {
	y := newStubYard(t)
	y.transcript.Store(true)
	chat, f := chatLocal(t)
	if _, err := publish(t, y, chat); err != nil {
		t.Fatal(err)
	}
	<-y.reg
	<-y.conn
	q := askFruit(t, chat)
	const viewer, ctl = "guestrelay000041", "guestrelay000042"
	y.guest(viewer, session.RoleView, "link-1", ^uint64(0))
	y.guest(ctl, session.RoleControl, "link-2", ^uint64(0))
	y.relay(viewer, proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "v1", Question: q, Input: "Plums\r"}))
	if m := y.wait("the view guest's word", func(m yardMsg) bool { return names(m, viewer, "v1") }); relayedJSON(m)["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("a view guest's answer %v", relayedJSON(m))
	}
	y.relay(ctl, proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "a1", Question: q, Input: "Plums\r"}))
	waitFor(t, "the answer sent to the agent", func() bool { return len(f.Sent()) > 0 })
	if sent := f.Sent(); len(sent) != 1 || sent[0].Text != "Plums" {
		t.Fatalf("sent %+v", sent)
	}
	f.Accepted(f.Sent()[0].Ref)
	if m := y.wait("the answer's word", func(m yardMsg) bool { return names(m, ctl, "a1") }); relayedJSON(m)["t"] != proto.CtlAnswerOK {
		t.Fatalf("a relayed answer %v", relayedJSON(m))
	}
}

func TestARoutedChoiceAnswersAHostsChat(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	h := newAnswering(t, proc, Options{})
	l := h.a.local
	f := sessiontest.NewFakeProtocol()
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	q := askFruit(t, l)
	h.answer(t, proto.HostAnswerAsMsg{Question: q, Input: "Pears\r", ByName: "Ada", BySubject: "u-1"})
	waitFor(t, "the answer sent to the agent", func() bool { return len(f.Sent()) > 0 })
	if sent := f.Sent(); len(sent) != 1 || sent[0].Text != "Pears" {
		t.Fatalf("sent %+v", sent)
	}
	f.Accepted(f.Sent()[0].Ref)
	h.idle()
	waitFor(t, "the question resolved", func() bool { _, ok := l.QuestionResult(q); return ok })
	if r, _ := l.QuestionResult(q); r.ByName != "Ada" {
		t.Fatalf("answered by %+v", r)
	}
	if w := writes(proc); len(w) != 0 {
		t.Fatalf("keys reached the terminal: %q", w)
	}
}
