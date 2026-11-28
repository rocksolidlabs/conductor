package session

import (
	"context"
	"errors"
	"slices"
	"testing"

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
