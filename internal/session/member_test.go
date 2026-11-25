package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A crew member's move to its chat inside the session (design 14): the
// engine's alone, refused once anything was typed.

// Claude Code's trust question as the stub draws it, with the catalog's
// pattern and its two answers.
var (
	memberTrust        = regexp.MustCompile(`Is\s*this\s*a\s*project\s*you\s*created\s*or\s*one\s*you\s*trust\?`)
	memberTrustAnswers = []Option{{Label: "Yes, I trust this folder", Input: "\x1b[B\r"}, {Label: "No, exit", Input: "\r"}}
	memberTrustFrame   = " Do you trust the files in this folder?\r\n\r\n Is this a project you created or one you trust?\r\n\r\n ❯ 1. No, exit\r\n   2. Yes, I trust this folder\r\n\r\n Enter to confirm · Esc to exit"
)

// memberSession is a crew member's server session launched to run as a
// chat, in its terminal, Claude Code's prompt shape and trust question
// known; resumes says its launch resumed the conversation conv-1.
func memberSession(t *testing.T, resumes bool) (*Local, *fakeProc) {
	t.Helper()
	p := newFakeProc()
	info := Info{ID: "member", Kind: KindServer, Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24, Crew: &CrewRef{RunID: "run-1", Member: "lead"},
		AgentSession: &AgentSession{ID: "11111111-2222-4333-8444-555555555555", Source: AgentSessionSet}}
	if resumes {
		info.AgentSession = &AgentSession{ID: "conv-1", Resumable: true, Source: AgentSessionResumed}
	}
	s := NewLocal(info, p, quiet(Options{ScrollbackBytes: 4096, PromptShape: builtInShape(t, "claude"),
		TrustPattern: memberTrust, TrustAnswers: memberTrustAnswers, MemberWay: WayProtocol, MemberResumes: resumes}))
	t.Cleanup(func() {
		_ = s.Stop(context.Background())
		p.exit()
	})
	return s, p
}

// memberStarts is a member's move's starts over f and ts.
func memberStarts(f *sessiontest.FakeProtocol, ts *terminals) MoveStarts {
	return MoveStarts{Protocol: f.Start(), Terminal: ts.start, Gone: GroupGone}
}

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// After a submission, a person's keys at the agent's prompt, or a turn,
// MoveFresh is refused and the terminal runs on; after the trust question
// answered by its choice, or by a person's keys in the terminal, it is
// not, and the chat starts on a new conversation. Nothing but the engine
// calls it: no route reaches it.
func TestMoveFreshRefusedAfterInput(t *testing.T) {
	refused := func(t *testing.T, s *Local, f *sessiontest.FakeProtocol, ts *terminals) {
		t.Helper()
		err := s.MoveFresh(memberStarts(f, ts))
		var r *MoveRefusal
		if !errors.As(err, &r) || r.Words != TypedFirstWords {
			t.Fatalf("MoveFresh = %v, want refused: %s", err, TypedFirstWords)
		}
		if s.Way() != WayTerminal || len(f.Calls()) != 0 || len(ts.started()) != 0 {
			t.Fatalf("a refused move started something: way %s, calls %v, terminals %v", s.Way(), f.Calls(), ts.started())
		}
	}
	moved := func(t *testing.T, s *Local, f *sessiontest.FakeProtocol, ts *terminals, was string) {
		t.Helper()
		if err := s.MoveFresh(memberStarts(f, ts)); err != nil {
			t.Fatalf("MoveFresh = %v", err)
		}
		as := s.Info().AgentSession
		if s.Way() != WayProtocol || as == nil || f.StartedOn() != as.ID || as.ID == was || !uuidShape.MatchString(as.ID) {
			t.Fatalf("after the move: way %s, conversation %+v, chat started on %q (the terminal's %q)", s.Way(), as, f.StartedOn(), was)
		}
	}
	t.Run("a submission", func(t *testing.T) {
		s, p := memberSession(t, false)
		f, ts := sessiontest.NewFakeProtocol(), &terminals{}
		ts.cleanup(t)
		draw(t, s, p, claudePromptRow(""))
		if _, err := s.Submit(context.Background(), Submission{Text: "a line", ByName: "crew"}); err != nil && !errors.Is(err, ErrSubmitPending) {
			t.Fatal(err)
		}
		untilIdle(t, s)
		refused(t, s, f, ts)
	})
	t.Run("a person's keys at the prompt", func(t *testing.T) {
		s, p := memberSession(t, false)
		f, ts := sessiontest.NewFakeProtocol(), &terminals{}
		ts.cleanup(t)
		draw(t, s, p, claudePromptRow(""))
		sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Input(sub, []byte("x")); err != nil {
			t.Fatal(err)
		}
		untilIdle(t, s)
		refused(t, s, f, ts)
	})
	t.Run("a turn", func(t *testing.T) {
		s, p := memberSession(t, false)
		f, ts := sessiontest.NewFakeProtocol(), &terminals{}
		ts.cleanup(t)
		draw(t, s, p, claudePromptRow(""))
		s.SetAttention(AttentionWorking, "", SourceAPI)
		s.SetAttention(AttentionDone, "a turn of its own", SourceAPI)
		refused(t, s, f, ts)
	})
	for _, how := range []string{"its choice", "a person's keys"} {
		t.Run("the trust question answered by "+how, func(t *testing.T) {
			s, p := memberSession(t, false)
			f, ts := sessiontest.NewFakeProtocol(), &terminals{}
			ts.cleanup(t)
			was := s.Info().AgentSession.ID
			draw(t, s, p, memberTrustFrame)
			moveWaitFor(t, "the trust question shown", func() bool {
				att := s.Info().Attention
				return att.State == AttentionNeedsInput && att.Source == SourceTrust
			})
			// Not the prompt while the question shows: refused, nothing
			// stopped.
			var r *MoveRefusal
			if err := s.MoveFresh(memberStarts(f, ts)); !errors.As(err, &r) || r.Words != NotAtPromptYetWords {
				t.Fatalf("MoveFresh at the trust question = %v", err)
			}
			sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			if how == "its choice" {
				err = s.AnswerInput(context.Background(), sub, s.Info().Attention.ID, []byte(memberTrustAnswers[0].Input))
			} else {
				err = s.Input(sub, []byte(memberTrustAnswers[0].Input))
			}
			if err != nil {
				t.Fatal(err)
			}
			untilIdle(t, s)
			draw(t, s, p, claudePromptRow(`Try "fix lint errors"`))
			moveWaitFor(t, "the trust question gone", func() bool { return s.Info().Attention.State != AttentionNeedsInput })
			moved(t, s, f, ts, was)
		})
	}
	t.Run("a session that is no fresh member's", func(t *testing.T) {
		for name, mk := range map[string]func(t *testing.T) (*Local, *fakeProc){
			"a resumed member": func(t *testing.T) (*Local, *fakeProc) { return memberSession(t, true) },
			"a person's session": func(t *testing.T) (*Local, *fakeProc) {
				return newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, "claude")}))
			},
		} {
			s, p := mk(t)
			f, ts := sessiontest.NewFakeProtocol(), &terminals{}
			ts.cleanup(t)
			draw(t, s, p, claudePromptRow(""))
			var r *MoveRefusal
			if err := s.MoveFresh(memberStarts(f, ts)); !errors.As(err, &r) || r.Words != CannotMoveWords || len(f.Calls()) != 0 {
				t.Fatalf("%s: MoveFresh = %v", name, err)
			}
		}
	})
	t.Run("no route", func(t *testing.T) {
		files, err := filepath.Glob(filepath.Join("..", "api", "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("the routes' files: %v", err)
		}
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			b, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), ".MoveFresh(") || strings.Contains(string(b), ".MoveResumed(") {
				t.Fatalf("%s reaches a member's move", name)
			}
		}
	})
}
