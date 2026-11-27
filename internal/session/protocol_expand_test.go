package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// I19 (docs/plans/chat-or-terminal.md, row 12): only the operator's own
// typed line goes to a chat's agent without the as-written mark. Every
// other sender, and every other kind of line, keeps the mark, whatever it
// asks.

// markedChat is a chat over the fake driver whose driver has a mark (as
// Claude Code's does), stopped at the test's end.
func markedChat(t *testing.T) (*Local, *sessiontest.FakeProtocol) {
	t.Helper()
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	f := sessiontest.NewFakeProtocol()
	f.ProtocolName, f.IsVerbatim = "claude-stream-json", true
	if err := s.enterProtocol(context.Background(), f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s, f
}

// operatorChat is the operator's own chat connection: the operator's own
// connection (owner, no link, a controller) by the server's own viewer
// socket.
var operatorChat = AttachOptions{Role: RoleControl, Name: "Ada", Owner: true, ViewerSocket: true}

// attachAs attaches o with a sink of its own, detached at the test's end.
func attachAs(t *testing.T, s *Local, o AttachOptions) (*Subscription, *chanSink) {
	t.Helper()
	sink := newChanSink(false)
	sub, err := s.AttachWith(o, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	return sub, sink
}

// lastPrompt is the newest prompt entry.
func lastPrompt(t *testing.T, s *Local) TranscriptEntry {
	t.Helper()
	es := allEntries(t, s)
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Kind == EntryPrompt {
			return es[i]
		}
	}
	t.Fatalf("no prompt entry: %+v", es)
	return TranscriptEntry{}
}

// turnOf finishes the message in flight: acknowledged, its turn begun and
// ended, so the next queued one is written.
func turnOf(f *sessiontest.FakeProtocol, ref string) {
	f.Accepted(ref)
	f.TurnStarted("")
	f.TurnEnded("done", Usage{})
}

func TestOnlyTheOperatorsOwnLineIsExpanded(t *testing.T) {
	ctx := context.Background()
	type made struct {
		s *Local
		f *sessiontest.FakeProtocol
	}
	cases := []struct {
		name string
		// verbatim false is a driver with no mark; crew a crew member's chat.
		unmarked, crew bool
		// sub builds the submission on the session, its sender attached.
		sub  func(t *testing.T, m made) Submission
		want bool
	}{
		{name: "the operator's own chat connection, asking", want: true, sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, operatorChat)
			return Submission{Text: "/compact", By: by, Expand: true}
		}},
		{name: "the operator's own chat connection, not asking", sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, operatorChat)
			return Submission{Text: "/compact", By: by}
		}},
		{name: "a control link, by the server's socket, owner said", sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
			return Submission{Text: "/compact", By: by, Expand: true}
		}},
		{name: "a decision link", sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, AttachOptions{Role: RoleControl, LinkID: "link-2", Name: "Desk", Decisions: true, ViewerSocket: true})
			return Submission{Text: "/compact", By: by, Expand: true}
		}},
		{name: "the owner without the server's socket (a host's peer, a switchyard's)", sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, AttachOptions{Role: RoleControl, Name: "Ada", Owner: true})
			return Submission{Text: "/compact", By: by, Expand: true}
		}},
		{name: "a controller that is not the owner (a principal without admin)", sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, AttachOptions{Role: RoleControl, Name: "Bo", ViewerSocket: true})
			return Submission{Text: "/compact", By: by, Expand: true}
		}},
		{name: "the operator's quiet connection (a run's chat)", sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, AttachOptions{Role: RoleControl, Name: "Ada", Owner: true, ViewerSocket: true, ChatOnly: true})
			return Submission{Text: "/compact", By: by, Expand: true}
		}},
		{name: "a crew member's chat", crew: true, sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, operatorChat)
			return Submission{Text: "/compact", By: by, Expand: true}
		}},
		{name: "a relay of the operator's line (From)", sub: func(t *testing.T, m made) Submission {
			from, _ := attachAs(t, m.s, operatorChat)
			return Submission{Text: "/compact", From: from, ByName: "Ada", Expand: true}
		}},
		{name: "the operator's line with From beside By", sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, operatorChat)
			return Submission{Text: "/compact", By: by, From: by, Expand: true}
		}},
		{name: "Conductor's own line (no By)", sub: func(t *testing.T, m made) Submission {
			return Submission{Text: "/compact", ByName: "the crew", Expand: true}
		}},
		{name: "an answer to a question", sub: func(t *testing.T, m made) Submission {
			id, err := m.s.TryAskID("Which branch?", SourceAPI, KindPrompt, nil)
			if err != nil {
				t.Fatal(err)
			}
			by, _ := attachAs(t, m.s, operatorChat)
			return Submission{Text: "/compact", By: by, Question: id, Expand: true}
		}},
		{name: "a decision's answer", sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, operatorChat)
			return Submission{Text: "/compact", By: by, Decision: "dr-1", Expand: true}
		}},
		{name: "a driver with no mark", unmarked: true, sub: func(t *testing.T, m made) Submission {
			by, _ := attachAs(t, m.s, operatorChat)
			return Submission{Text: "/compact", By: by, Expand: true}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
			if c.crew {
				s.mu.Lock()
				s.info.Crew = &CrewRef{RunID: "run-1", Member: "writer"}
				s.mu.Unlock()
			}
			f := sessiontest.NewFakeProtocol()
			f.ProtocolName, f.IsVerbatim = "claude-stream-json", !c.unmarked
			if err := s.enterProtocol(ctx, f.Start()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Stop(context.Background()) })
			sub := c.sub(t, made{s, f})
			_, err := s.Submit(ctx, sub)
			if sub.Expand && !c.want {
				// Asked where it cannot be honoured: refused, nothing sent,
				// never sent as written in its place.
				if !errors.Is(err, ErrNoExpand) {
					t.Fatalf("Submit = %v, want ErrNoExpand", err)
				}
				if n := len(f.Sent()); n != 0 {
					t.Fatalf("a refused line was sent: %+v", f.Sent())
				}
				return
			}
			if !errors.Is(err, ErrSubmitPending) {
				t.Fatalf("Submit = %v", err)
			}
			waitCalls(t, f, "the write", sends(1))
			m := f.Sent()[0]
			if m.Text != "/compact" || m.Expand != c.want {
				t.Fatalf("sent %+v, want Expand %v", m, c.want)
			}
			if e := lastPrompt(t, s); e.Expanded != c.want {
				t.Fatalf("the prompt entry says expanded %v, want %v", e.Expanded, c.want)
			}
		})
	}
}

// Three lines queued in one turn (a guest's, the operator's, a guest's)
// keep each its own word, in order; a guest's line that asks for the word
// is refused among them, and the others go on.
func TestExpandIsPerMessage(t *testing.T) {
	s, f := markedChat(t)
	op, _ := attachAs(t, s, operatorChat)
	guest, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", ViewerSocket: true})
	f.TurnStarted("")
	ctx := context.Background()
	for _, sub := range []Submission{
		{Text: "/one", By: guest},
		{Text: "/two", By: op, Expand: true},
		{Text: "/three", By: guest},
	} {
		if _, err := s.Submit(ctx, sub); !errors.Is(err, ErrSubmitPending) {
			t.Fatalf("Submit %q = %v", sub.Text, err)
		}
	}
	if _, err := s.Submit(ctx, Submission{Text: "/four", By: guest, Expand: true}); !errors.Is(err, ErrNoExpand) {
		t.Fatalf("a guest's line asking for the word: %v", err)
	}
	f.TurnEnded("done", Usage{})
	for i := range 3 {
		waitCalls(t, f, "a write", sends(i+1))
		turnOf(f, f.Sent()[i].Ref)
	}
	got := f.Sent()
	want := []bool{false, true, false}
	for i, m := range got {
		if m.Expand != want[i] {
			t.Fatalf("message %d (%q) Expand %v, want %v", i, m.Text, m.Expand, want[i])
		}
	}
	var marks []bool
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryPrompt {
			marks = append(marks, e.Expanded)
		}
	}
	if len(marks) != 3 || marks[0] || !marks[1] || marks[2] {
		t.Fatalf("the prompt entries say %v", marks)
	}
}

// The operator's line queued during a turn, its connection revoked before
// the write: never written, unmarked or marked.
func TestAnExpandedLineOfARevokedSenderIsNeverWritten(t *testing.T) {
	s, f := markedChat(t)
	op, _ := attachAs(t, s, operatorChat)
	f.TurnStarted("")
	words := make(chan finishWord, 1)
	if _, err := s.Submit(context.Background(), Submission{Text: "@notes.txt", By: op, Expand: true, Finished: finishedInto(words)}); !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("Submit = %v", err)
	}
	op.Revoke(ErrRevoked)
	f.TurnEnded("done", Usage{})
	w := takeWord(t, words)
	if !errors.Is(w.err, ErrRevoked) {
		t.Fatalf("the revoked line's word: %+v", w)
	}
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("a revoked sender's line was written: %+v", f.Sent())
	}
	if e := lastPrompt(t, s); e.State != PromptWithdrawn {
		t.Fatalf("its entry: %+v", e)
	}
}

// A guest's line of the people's chat, sent on to the agent by the
// operator's own press (To agent): the guest's words, marked.
func TestChatSendIsNeverExpanded(t *testing.T) {
	s, f := markedChat(t)
	op, _ := attachAs(t, s, operatorChat)
	guest, _ := attachAs(t, s, AttachOptions{Role: RoleView, LinkID: "link-1", Name: "Guest", ViewerSocket: true})
	posted, err := s.Chat(guest, proto.ChatPost{T: proto.CtlChat, Text: "/compact and @/etc/passwd"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChatSend(context.Background(), op, proto.ChatSend{Ref: posted.ID}); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, f, "the write", sends(1))
	if m := f.Sent()[0]; m.Expand {
		t.Fatalf("a guest's line sent on by the operator went unmarked: %+v", m)
	}
	if e := lastPrompt(t, s); e.Expanded {
		t.Fatalf("its entry: %+v", e)
	}
}

// A choice's label (the agent's own words) and a typed answer, each from
// the operator's own chat connection: marked.
func TestAChoiceAndAnAnswerAreNeverExpanded(t *testing.T) {
	t.Run("a choice", func(t *testing.T) {
		s, f := markedChat(t)
		id := fruit(t, s, Option{Label: "/compact", Input: "1"}, Option{Label: "@notes.txt", Input: "2"})
		op, _ := attachAs(t, s, operatorChat)
		if err := s.AnswerInputReq(context.Background(), op, id, "r1", []byte("1"), nil); !errors.Is(err, ErrSubmitPending) {
			t.Fatalf("the choice: %v", err)
		}
		waitCalls(t, f, "the write", sends(1))
		if m := f.Sent()[0]; m.Text != "/compact" || m.Expand {
			t.Fatalf("a choice went %+v", m)
		}
	})
	t.Run("a typed answer", func(t *testing.T) {
		s, f := markedChat(t)
		id := fruit(t, s)
		op, _ := attachAs(t, s, operatorChat)
		if _, err := s.Submit(context.Background(), Submission{Text: "/compact", By: op, Question: id, Expand: true}); !errors.Is(err, ErrNoExpand) {
			t.Fatalf("an answer asking for the word: %v", err)
		}
		if _, err := s.Submit(context.Background(), Submission{Text: "/compact", By: op, Question: id}); !errors.Is(err, ErrSubmitPending) {
			t.Fatalf("the answer: %v", err)
		}
		waitCalls(t, f, "the write", sends(1))
		if m := f.Sent()[0]; m.Expand || len(f.Sent()) != 1 {
			t.Fatalf("an answer went %+v", f.Sent())
		}
	})
}

// A session in its terminal refuses a line that asks for the word: it was
// sent for a chat, and nothing of it is typed.
func TestATerminalRefusesExpand(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	op, _ := attachAs(t, s, operatorChat)
	if _, err := s.Submit(context.Background(), Submission{Text: "/compact", By: op, Expand: true}); !errors.Is(err, ErrNoExpand) {
		t.Fatalf("Submit = %v", err)
	}
	if n := len(p.input); n != 0 {
		t.Fatalf("%d writes reached the terminal", n)
	}
}

// welcomeOf is the welcome a sink was sent first.
func welcomeOf(t *testing.T, sink *chanSink) []byte {
	t.Helper()
	sink.waitFrames(t, 1)
	f, err := proto.Decode(sink.frame(0))
	if err != nil || f.Type != proto.TypeControl {
		t.Fatalf("the first frame: %v", err)
	}
	return f.Payload
}

// The welcome says expand to the operator's own chat connection alone, on a
// chat whose driver has a mark; every other welcome holds no such key.
func TestTheWelcomeSaysExpandToTheOperatorAlone(t *testing.T) {
	s, _ := markedChat(t)
	_, own := attachAs(t, s, operatorChat)
	if w := welcomeOf(t, own); !bytes.Contains(w, []byte(`"expand":true`)) {
		t.Fatalf("the operator's welcome: %s", w)
	}
	for _, o := range []AttachOptions{
		{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true},
		{Role: RoleView, LinkID: "link-2", Name: "Viewer", ViewerSocket: true},
		{Role: RoleControl, Name: "Ada", Owner: true},
		{Role: RoleControl, Name: "Ada", Owner: true, ViewerSocket: true, ChatOnly: true},
	} {
		_, sink := attachAs(t, s, o)
		if w := welcomeOf(t, sink); bytes.Contains(w, []byte(`"expand"`)) {
			t.Fatalf("%+v was told expand: %s", o, w)
		}
	}
	plain, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	_, sink := attachAs(t, plain, operatorChat)
	if w := welcomeOf(t, sink); bytes.Contains(w, []byte(`"expand"`)) {
		t.Fatalf("a terminal session's welcome: %s", w)
	}
	unmarked, _, _ := protocolSession(t, Options{})
	_, sink = attachAs(t, unmarked, operatorChat)
	if w := welcomeOf(t, sink); bytes.Contains(w, []byte(`"expand"`)) {
		t.Fatalf("a chat with no mark: %s", w)
	}
}

// After a move to Chat on open connections, the way message says expand to
// the operator's own chat connection alone.
func TestTheWayMessageSaysExpandToTheOperatorAlone(t *testing.T) {
	s, _ := movable(t, 0)
	_, own := attachAs(t, s, operatorChat)
	_, link := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	_, peer := attachAs(t, s, AttachOptions{Role: RoleControl, Name: "Ada", Owner: true})
	ts := &terminals{}
	ts.cleanup(t)
	f := sessiontest.NewFakeProtocol()
	f.IsVerbatim = true
	toChat(t, s, f, ts)
	wayTo := func(sink *chanSink) map[string]any {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			for i := range sink.count() {
				fr, err := proto.Decode(sink.frame(i))
				if err != nil || fr.Type != proto.TypeControl {
					continue
				}
				var m map[string]any
				if json.Unmarshal(fr.Payload, &m) == nil && m["t"] == proto.CtlWay && m["way"] == proto.WayProtocol {
					return m
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("no way message to the chat")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if m := wayTo(own); m["expand"] != true {
		t.Fatalf("the operator's way message: %v", m)
	}
	for _, sink := range []*chanSink{link, peer} {
		if m := wayTo(sink); m["expand"] != nil {
			t.Fatalf("another connection's way message: %v", m)
		}
	}
}
