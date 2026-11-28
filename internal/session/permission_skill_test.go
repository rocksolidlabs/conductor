package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session/internal/way"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// skillChat is a chat whose permission requests are put to Conductor's
// prompt tool (Claude Code's), its clock the test's.
func skillChat(t *testing.T) (*Local, *sessiontest.FakeProtocol) {
	t.Helper()
	clock := &fakeTimers{}
	s, _, f := protocolSession(t, Options{AfterFunc: clock.AfterFunc})
	return s, f
}

// lineWritten submits sub and waits for its line to be written to the agent,
// returning the written message's ref.
func lineWritten(t *testing.T, s *Local, f *sessiontest.FakeProtocol, sub Submission) string {
	t.Helper()
	n := len(f.Sent())
	if _, err := s.Submit(context.Background(), sub); !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("submit %q: %v", sub.Text, err)
	}
	waitCalls(t, f, "the line written", sends(n+1))
	return f.Sent()[n].Ref
}

// turnOfLines writes the lines as one turn's: each acknowledged but the last,
// which the turn's start follows; a line after the first is written while
// the one before it is left unconfirmed (as when the agent took both into
// one turn).
func turnOfLines(t *testing.T, s *Local, f *sessiontest.FakeProtocol, subs ...Submission) {
	t.Helper()
	for i, sub := range subs {
		ref := lineWritten(t, s, f, sub)
		if i < len(subs)-1 {
			s.mu.Lock()
			p := s.way.Load()
			q := p.inflight
			s.mu.Unlock()
			if q == nil {
				t.Fatal("no line in flight")
			}
			s.unconfirm(p, q)
			continue
		}
		f.Accepted(ref)
	}
	f.TurnStarted("")
}

// noQuestion says the session puts no question to anyone: nothing open,
// nothing waiting, the attention no permission's.
func noQuestion(t *testing.T, s *Local) {
	t.Helper()
	if open, waiting := s.OpenPermissions(); open != 0 || waiting != 0 {
		t.Fatalf("open %d, waiting %d", open, waiting)
	}
	if a := s.Info().Attention; a.Kind == KindPermission {
		t.Fatalf("the attention is a permission's: %+v", a)
	}
}

// A call of the agent's skill tool is refused without a question in a
// turn whose lines are not all the operator's own: a link's line, the
// operator's and a link's together, a line Conductor wrote, a relayed one,
// and a turn with no line at all. The agent is told why; nothing is put to
// the operator, and no entry is drawn; the next ordinary call of the turn
// is asked as before.
func TestASkillIsRefusedInATurnNotTheOperatorsOwn(t *testing.T) {
	skill := `{"skill":"hello"}`
	for _, tc := range []struct {
		name  string
		lines func(op, link *Subscription) []Submission
	}{
		{"a link's line", func(_, link *Subscription) []Submission {
			return []Submission{{Text: "/hello", By: link}}
		}},
		{"the operator's line and a link's", func(op, link *Subscription) []Submission {
			return []Submission{{Text: "first", By: op}, {Text: "/hello", By: link}}
		}},
		{"a link's line and the operator's", func(op, link *Subscription) []Submission {
			return []Submission{{Text: "/hello", By: link}, {Text: "and this", By: op}}
		}},
		{"a line Conductor wrote", func(_, _ *Subscription) []Submission {
			return []Submission{{Text: "/hello", ByName: "Brakeman"}}
		}},
		{"a relayed line", func(op, _ *Subscription) []Submission {
			return []Submission{{Text: "/hello", ByName: "Grace", From: op}}
		}},
		{"no line", func(_, _ *Subscription) []Submission { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := skillChat(t)
			op, _ := attachAs(t, s, operatorChat)
			link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
			if subs := tc.lines(op, link); len(subs) > 0 {
				turnOfLines(t, s, f, subs...)
			} else {
				f.TurnStarted("")
			}
			announce(f, "c-skill", "Skill", skill)
			if v := ask(s, "c-skill", "Skill", skill).returned(t); v.Allow || v.Message != SkillOwnLineWords {
				t.Fatalf("the skill call: %+v", v)
			}
			noQuestion(t, s)
			for _, e := range allEntries(t, s) {
				if e.Kind == EntryPermission {
					t.Fatalf("an entry drawn for the refused call: %+v", e)
				}
			}
			// What the model does next is asked as ever.
			announce(f, "c-bash", "Bash", bash("touch hello.marker"))
			h := ask(s, "c-bash", "Bash", bash("touch hello.marker"))
			if e := openRequest(t, s, "c-bash"); e.Plain != "command" || !h.held() {
				t.Fatalf("the next call: %+v", e)
			}
		})
	}
}

// In the operator's own turn a skill call is asked: in words of its own
// ("Run a skill?"), its skill named in the entry's input, with Allow once
// and Deny alone. A turn's end begins the count again.
func TestASkillIsAskedInTheOperatorsOwnTurn(t *testing.T) {
	s, f := skillChat(t)
	op, _ := attachAs(t, s, operatorChat)
	link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	turnOfLines(t, s, f, Submission{Text: "/hello", By: link})
	f.TurnEnded("done", Usage{})
	turnOfLines(t, s, f, Submission{Text: "use the hello skill", By: op}, Submission{Text: "please", By: op})
	skill := `{"skill":"hello"}`
	announce(f, "c-skill", "Skill", skill)
	h := ask(s, "c-skill", "Skill", skill)
	e := openRequest(t, s, "c-skill")
	if e.Plain != "skill" || e.Summary != skill || !slices.Equal(e.Choices, []string{"allow_once", "deny"}) || !h.held() {
		t.Fatalf("the question %+v", e)
	}
	if a := s.Info().Attention; a.Kind != KindPermission || a.Message != "Run a skill?" {
		t.Fatalf("the attention %+v", a)
	}
}

// A request that comes in the agent's own stream for a tool of the skill
// kind is held to the same rule: refused without a question or an entry in
// a link's turn, asked in the operator's own.
func TestAnInStreamSkillIsRefusedInATurnNotTheOperatorsOwn(t *testing.T) {
	s, f := skillChat(t)
	op, _ := attachAs(t, s, operatorChat)
	link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	turnOfLines(t, s, f, Submission{Text: "/hello", By: link})
	f.ToolStarted(ToolStep{Ref: "c1", Name: "Skill", Input: []byte(`{"skill":"hello"}`)})
	f.Permission(PermissionRequest{Ref: "req-1", Call: "c1", Tool: "Skill", Input: []byte(`{"skill":"hello"}`)})
	v := replyTo(t, f, "req-1")
	if v.Allow || v.Message != SkillOwnLineWords {
		t.Fatalf("the agent was told %+v", v)
	}
	noQuestion(t, s)
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryPermission {
			t.Fatalf("an entry drawn for the refused call: %+v", e)
		}
	}
	f.TurnEnded("done", Usage{})
	turnOfLines(t, s, f, Submission{Text: "use the hello skill", By: op})
	f.ToolStarted(ToolStep{Ref: "c2", Name: "Skill", Input: []byte(`{"skill":"hello"}`)})
	if e, v := inStreamAsked(t, s, f, "req-2", "c2", "Skill", `{"skill":"hello"}`); v != nil || e.State != PermissionOpen || e.Plain != "skill" {
		t.Fatalf("in the operator's own turn: %+v, replied %+v", e, v)
	}
}

// A line not the operator's own that the agent has not said it took is
// carried from a turn's end into the next turn's count, however long ago
// it was written: the operator's line, slow to be acknowledged, goes
// unconfirmed; a link's line is written and goes unconfirmed too; the
// operator's turn ends; the operator's next line is written; the agent
// then takes the link's line, and a skill call in that turn is refused.
// Once the agent has said it took the line, the turn after is the
// operator's own again.
func TestALineStillOnItsWayIsCarriedIntoTheNextTurn(t *testing.T) {
	s, f := skillChat(t)
	op, _ := attachAs(t, s, operatorChat)
	link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	unconfirmed := func() {
		t.Helper()
		s.mu.Lock()
		p := s.way.Load()
		q := p.inflight
		s.mu.Unlock()
		if q == nil {
			t.Fatal("no line in flight")
		}
		s.unconfirm(p, q)
	}
	lineWritten(t, s, f, Submission{Text: "first", By: op})
	unconfirmed()
	guest := lineWritten(t, s, f, Submission{Text: "/hello", By: link})
	unconfirmed()
	f.TurnStarted("")
	f.TurnEnded("done", Usage{})
	next := lineWritten(t, s, f, Submission{Text: "and now this", By: op})
	f.Accepted(next)
	f.Accepted(guest)
	f.TurnStarted("")
	skill := `{"skill":"hello"}`
	announce(f, "c-skill", "Skill", skill)
	if v := ask(s, "c-skill", "Skill", skill).returned(t); v.Allow || v.Message != SkillOwnLineWords {
		t.Fatalf("the skill call in the turn that took the link's line: %+v", v)
	}
	noQuestion(t, s)
	// Taken: no longer carried.
	f.TurnEnded("done", Usage{})
	turnOfLines(t, s, f, Submission{Text: "use the hello skill", By: op})
	announce(f, "c-skill-2", "Skill", skill)
	h := ask(s, "c-skill-2", "Skill", skill)
	if e := openRequest(t, s, "c-skill-2"); e.Plain != "skill" || !h.held() {
		t.Fatalf("the operator's own turn after: %+v", e)
	}
}

// A people's chat message someone else wrote, sent on by the operator, goes
// for its author, as written: not the operator's own line, so a skill call
// in its turn is refused; the operator's own message sent on is theirs.
func TestAMessageSomeoneElseWroteGoesForItsAuthor(t *testing.T) {
	s, f := skillChat(t)
	op, _ := attachAs(t, s, operatorChat)
	link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	m, err := s.Chat(link, proto.ChatPost{T: proto.CtlChat, Text: "/hello"})
	if err != nil {
		t.Fatal(err)
	}
	n := len(f.Sent())
	if err := s.ChatSend(context.Background(), op, proto.ChatSend{T: proto.CtlChatSend, Ref: m.ID}); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, f, "the line written", sends(n+1))
	sent := f.Sent()[n]
	if sent.Text != "/hello" || sent.Expand {
		t.Fatalf("written %+v", sent)
	}
	if e := lastPrompt(t, s); e.ByName != "Guest" || e.Expanded {
		t.Fatalf("the prompt %+v", e)
	}
	f.Accepted(sent.Ref)
	f.TurnStarted("")
	skill := `{"skill":"hello"}`
	announce(f, "c-skill", "Skill", skill)
	if v := ask(s, "c-skill", "Skill", skill).returned(t); v.Allow || v.Message != SkillOwnLineWords {
		t.Fatalf("the skill call: %+v", v)
	}
	f.TurnEnded("done", Usage{})
	mine, err := s.Chat(op, proto.ChatPost{T: proto.CtlChat, Text: "use the hello skill"})
	if err != nil {
		t.Fatal(err)
	}
	n = len(f.Sent())
	if err := s.ChatSend(context.Background(), op, proto.ChatSend{T: proto.CtlChatSend, Ref: mine.ID}); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, f, "the line written", sends(n+1))
	f.Accepted(f.Sent()[n].Ref)
	f.TurnStarted("")
	announce(f, "c-skill-2", "Skill", skill)
	h := ask(s, "c-skill-2", "Skill", skill)
	if e := openRequest(t, s, "c-skill-2"); e.Plain != "skill" || !h.held() {
		t.Fatalf("the operator's own message: %+v", e)
	}
}

// The skill tool is named as Claude Code names it in its own tool list: of
// the built-in tools of 2.1.332, Skill alone, in any case.
func TestTheSkillToolIsNamedAsTheProgramNamesIt(t *testing.T) {
	builtIn := []string{"Task", "AskUserQuestion", "Bash", "CronCreate", "CronDelete", "CronList", "Edit", "EnterPlanMode", "EnterWorktree",
		"ExitPlanMode", "ExitWorktree", "ListAgents", "LSP", "Monitor", "NotebookEdit", "PushNotification", "Read", "RemoteTrigger",
		"ScheduleWakeup", "SendMessage", "Skill", "TaskStop", "ToolSearch", "WebSearch", "Workflow", "Write"}
	var skills []string
	for _, tool := range builtIn {
		if skillTool(tool) {
			skills = append(skills, tool)
		}
	}
	if !slices.Equal(skills, []string{"Skill"}) || !slices.Equal(SkillTools, []string{"Skill"}) || !skillTool("skill") || plainOf("SKILL") != "skill" {
		t.Fatalf("the skill tools %v (SkillTools %v)", skills, SkillTools)
	}
}

// In the stream, a skill request is refused before any other check: one
// past the input's bound in a link's turn draws no entry either.
func TestAnInStreamSkillIsRefusedBeforeItsSize(t *testing.T) {
	s, f := skillChat(t)
	link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	turnOfLines(t, s, f, Submission{Text: "/hello", By: link})
	big := `{"skill":"` + strings.Repeat("a", MaxTranscriptPart) + `"}`
	f.Permission(PermissionRequest{Ref: "req-1", Call: "c1", Tool: "Skill", Input: []byte(big)})
	if v := replyTo(t, f, "req-1"); v.Allow || v.Message != SkillOwnLineWords {
		t.Fatalf("the agent was told %+v", v)
	}
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryPermission {
			t.Fatalf("an entry drawn: %+v", e)
		}
	}
}

// The operator's own typed /name (G3) is written marked for the agent to
// expand, and its turn is the operator's own: a skill call in it is asked.
func TestTheOperatorsOwnCommandIsTheirs(t *testing.T) {
	s, f := markedChat(t)
	op, _ := attachAs(t, s, operatorChat)
	ref := lineWritten(t, s, f, Submission{Text: "/hello", By: op, Expand: true})
	if m := f.Sent()[len(f.Sent())-1]; !m.Expand {
		t.Fatalf("written %+v", m)
	}
	f.Accepted(ref)
	f.TurnStarted("")
	skill := `{"skill":"hello"}`
	announce(f, "c-skill", "Skill", skill)
	h := ask(s, "c-skill", "Skill", skill)
	if e := openRequest(t, s, "c-skill"); e.Plain != "skill" || !h.held() {
		t.Fatalf("in the operator's own command's turn: %+v", e)
	}
}

// inFlightUnconfirmed leaves the line in flight unconfirmed, as the accept
// wait's end does.
func inFlightUnconfirmed(t *testing.T, s *Local) {
	t.Helper()
	s.mu.Lock()
	p := s.way.Load()
	q := p.inflight
	s.mu.Unlock()
	if q == nil {
		t.Fatal("no line in flight")
	}
	s.unconfirm(p, q)
}

// skillRefused and skillAsked put a skill call to the session in the
// current turn and say how it went.
func skillRefused(t *testing.T, s *Local, f *sessiontest.FakeProtocol, call string) {
	t.Helper()
	skill := `{"skill":"hello"}`
	announce(f, call, "Skill", skill)
	if v := ask(s, call, "Skill", skill).returned(t); v.Allow || v.Message != SkillOwnLineWords {
		t.Fatalf("%s: %+v", call, v)
	}
}

func skillAsked(t *testing.T, s *Local, f *sessiontest.FakeProtocol, call string) {
	t.Helper()
	skill := `{"skill":"hello"}`
	announce(f, call, "Skill", skill)
	h := ask(s, call, "Skill", skill)
	if e := openRequest(t, s, call); e.Plain != "skill" || !h.held() {
		t.Fatalf("%s: %+v", call, e)
	}
}

// A link's line said taken while another turn runs may be kept for the
// next turn: it is carried into it once more. The operator's line goes
// unconfirmed, the link's line is written, the operator's turn starts and
// the link's line is said taken in it, the turn ends, the operator's next
// line is written: a skill call in that turn is refused, and one in the
// turn after is asked.
func TestALineTakenDuringAnotherTurnIsCarriedOnceMore(t *testing.T) {
	s, f := skillChat(t)
	op, _ := attachAs(t, s, operatorChat)
	link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	lineWritten(t, s, f, Submission{Text: "first", By: op})
	inFlightUnconfirmed(t, s)
	guest := lineWritten(t, s, f, Submission{Text: "/hello", By: link})
	f.TurnStarted("")
	f.Accepted(guest)
	f.TurnEnded("done", Usage{})
	turnOfLines(t, s, f, Submission{Text: "and now this", By: op})
	skillRefused(t, s, f, "c-1")
	f.TurnEnded("done", Usage{})
	turnOfLines(t, s, f, Submission{Text: "use the hello skill", By: op})
	skillAsked(t, s, f, "c-2")
}

// An acknowledgement that names no message, taken for the oldest line
// written, may be another's: a link's line so taken is carried once more,
// though no turn ran as it was said taken.
func TestALineTakenByNoNameIsCarriedOnceMore(t *testing.T) {
	s, f := skillChat(t)
	op, _ := attachAs(t, s, operatorChat)
	link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	guest := lineWritten(t, s, f, Submission{Text: "/hello", By: link})
	f.Hand(func(e way.ProtocolEvents) {
		u, ok := e.(interface{ AcceptedUnnamed(string) })
		if !ok {
			t.Error("the session takes no acknowledgement by no name")
			return
		}
		u.AcceptedUnnamed(guest)
	})
	f.TurnStarted("")
	skillRefused(t, s, f, "c-1")
	f.TurnEnded("done", Usage{})
	turnOfLines(t, s, f, Submission{Text: "and now this", By: op})
	skillRefused(t, s, f, "c-2")
	f.TurnEnded("done", Usage{})
	turnOfLines(t, s, f, Submission{Text: "use the hello skill", By: op})
	skillAsked(t, s, f, "c-3")
}

// A link's line written to a child that ended can be said taken no more:
// carried into the new child's first turn (the conversation it resumes may
// hold it), a skill call there is refused; in the operator's own turn
// after, one is asked.
func TestALineToAChildThatEndedIsCarriedIntoTheNewChildsFirstTurn(t *testing.T) {
	clock := &fakeTimers{}
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096, AfterFunc: clock.AfterFunc})
	s.SetAgentSession(AgentSession{ID: "conv-1", Source: AgentSessionSet})
	f, again := sessiontest.NewFakeProtocol(), sessiontest.NewFakeProtocol()
	f.InterruptErr = ErrNoInterrupt
	if err := s.enterProtocol(context.Background(), f.Start(again)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	op, _ := attachAs(t, s, operatorChat)
	link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	lineWritten(t, s, f, Submission{Text: "/hello", By: link})
	f.TurnStarted("")
	if err := s.Interrupt(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, again, "the child started again", func(c []string) bool { return sessiontest.Count(c, "begin") == 1 })
	turnOfLines(t, s, again, Submission{Text: "and now this", By: op})
	skillRefused(t, s, again, "c-1")
	again.TurnEnded("done", Usage{})
	turnOfLines(t, s, again, Submission{Text: "use the hello skill", By: op})
	skillAsked(t, s, again, "c-2")
}

// A link's line whose write failed is never said taken, and one the session
// no longer follows (past the unconfirmed lines it keeps) is not either:
// each is carried once more and then let go, never refusing for good.
func TestALineThatCanNeverBeSaidTakenIsCarriedOnceMore(t *testing.T) {
	t.Run("a failed write", func(t *testing.T) {
		clock := &fakeTimers{}
		s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096, AfterFunc: clock.AfterFunc})
		f := sessiontest.NewFakeProtocol()
		f.SendErr = errors.New("the pipe is gone")
		if err := s.enterProtocol(context.Background(), f.Start()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Stop(context.Background()) })
		op, _ := attachAs(t, s, operatorChat)
		link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
		lineWritten(t, s, f, Submission{Text: "/hello", By: link})
		f.TurnStarted("")
		skillRefused(t, s, f, "c-1")
		f.TurnEnded("done", Usage{})
		lineWritten(t, s, f, Submission{Text: "and now this", By: op})
		f.TurnStarted("")
		skillRefused(t, s, f, "c-2")
		f.TurnEnded("done", Usage{})
		lineWritten(t, s, f, Submission{Text: "use the hello skill", By: op})
		f.TurnStarted("")
		skillAsked(t, s, f, "c-3")
	})
	t.Run("no longer followed", func(t *testing.T) {
		s, f := skillChat(t)
		link, _ := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
		var refs []string
		for range maxUnconfirmedKept + 1 {
			refs = append(refs, lineWritten(t, s, f, Submission{Text: "/hello", By: link}))
			inFlightUnconfirmed(t, s)
		}
		s.mu.Lock()
		p := s.way.Load()
		first, last := p.othersOnTheirWay[refs[0]], p.othersOnTheirWay[refs[len(refs)-1]]
		s.mu.Unlock()
		if first != otherOnceMore || last != otherOnItsWay {
			t.Fatalf("the first line %d, the last %d", first, last)
		}
	})
}
