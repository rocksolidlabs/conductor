package api

import (
	"context"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// markedChat is chatSession with a driver that has an as-written mark, as
// Claude Code's does.
func markedChat(t *testing.T, e *testEnv) (string, *session.Local, *sessiontest.FakeProtocol) {
	t.Helper()
	p := sessiontest.NewFakeProc()
	l := session.NewLocal(session.Info{ID: session.NewID(), Name: "chat", Kind: session.KindServer, Cwd: t.TempDir(), Status: session.StatusRunning, Cols: 80, Rows: 24},
		p, session.Options{ScrollbackBytes: 4096, Transport: proto.TransportWS})
	if err := e.srv.registry.Add(l); err != nil {
		t.Fatal(err)
	}
	f := sessiontest.NewFakeProtocol()
	f.ProtocolName, f.IsVerbatim = "claude-stream-json", true
	if err := sessiontest.EnterProtocol(l, f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = l.Stop(context.Background())
		e.srv.registry.Remove(l.Info().ID)
		p.End(0)
	})
	return l.Info().ID, l, f
}

// I19 over real sockets (docs/plans/chat-or-terminal.md, row 12): the
// operator's own submit with expand goes to the agent without the mark,
// and its welcome says expand; a control link's and a view link's welcome
// hold no such word, and a control link's submit with expand goes marked.
func TestExpandOverTheSocket(t *testing.T) {
	e := newTestEnv(t, nil)
	id, l, f := markedChat(t, e)
	_, control, err := e.srv.links.Create(id, session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, view, err := e.srv.links.Create(id, session.RoleView, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sent := func(n int) session.Message {
		t.Helper()
		if !f.Await(ctx, func(c []string) bool { return sessiontest.Count(c, "send") >= n }) {
			t.Fatalf("no write %d: %v", n, f.Calls())
		}
		return f.Sent()[n-1]
	}

	op := dialViewer(t, e, id, adminToken)
	helloChat(op, ^uint64(0))
	if w := op.until("the operator's welcome", controlOf(proto.CtlWelcome), nil); w.m["expand"] != true {
		t.Fatalf("the operator's welcome %v", w.m)
	}
	op.until("ready", controlOf(proto.CtlReady), nil)
	op.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "/compact", ReqID: "o1", Expand: true}))
	if m := sent(1); m.Text != "/compact" || !m.Expand {
		t.Fatalf("the operator's line went %+v", m)
	}
	f.Accepted(sent(1).Ref)
	f.TurnStarted("")
	f.TurnEnded("done", session.Usage{})

	link := dialViewer(t, e, id, control)
	helloChat(link, ^uint64(0))
	if w := link.until("a control link's welcome", controlOf(proto.CtlWelcome), nil); w.m["expand"] != nil || w.m["permissionAnswer"] != nil {
		t.Fatalf("a control link's welcome %v", w.m)
	}
	link.until("ready", controlOf(proto.CtlReady), nil)
	link.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "/compact", ReqID: "l1", Expand: true}))
	if m := sent(2); m.Text != "/compact" || m.Expand {
		t.Fatalf("a control link's line went %+v", m)
	}
	f.Accepted(sent(2).Ref)
	f.TurnStarted("")
	f.TurnEnded("done", session.Usage{})

	watcher := dialViewer(t, e, id, view)
	helloChat(watcher, ^uint64(0))
	if w := watcher.until("a view link's welcome", controlOf(proto.CtlWelcome), nil); w.m["expand"] != nil {
		t.Fatalf("a view link's welcome %v", w.m)
	}
	watcher.until("ready", controlOf(proto.CtlReady), nil)
	watcher.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "/compact", ReqID: "v1", Expand: true}))
	if c := watcher.until("the view link's word", func(c chatFrame) bool { return c.names("v1") }, nil); c.m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("a view link's submit: %v", c.m)
	}
	if n := sessiontest.Count(f.Calls(), "send"); n != 2 {
		t.Fatalf("%d writes", n)
	}
	var prompts []bool
	entries, err := l.Transcript(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Kind == session.EntryPrompt {
			prompts = append(prompts, entry.Expanded)
		}
	}
	if len(prompts) != 2 || !prompts[0] || prompts[1] {
		t.Fatalf("the prompts' expanded: %v", prompts)
	}
}
