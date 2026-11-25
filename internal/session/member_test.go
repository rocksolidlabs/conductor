package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
	// A person's text counts whatever the screen showed as it was written:
	// typed while the agent loads, and shown in its prompt once drawn; typed
	// while a synchronized update cuts the frame.
	for how, before := range map[string]func(p *fakeProc){
		"text typed while the agent loads": func(p *fakeProc) {
			_, _ = p.outW.Write([]byte("\x1b[2J\x1b[H" + claudeBanner + "   Loading…"))
		},
		"text typed in a cut frame": func(p *fakeProc) {
			_, _ = p.outW.Write([]byte("\x1b[2J\x1b[H" + claudePromptRow("") + "\x1b[?2026h\x1b[2J\x1b[H" + claudeBanner))
		},
	} {
		t.Run(how, func(t *testing.T) {
			s, p := memberSession(t, false)
			f, ts := sessiontest.NewFakeProtocol(), &terminals{}
			ts.cleanup(t)
			before(p)
			moveWaitFor(t, "the screen not the prompt", func() bool {
				return strings.Contains(strings.Join(s.ScreenLines(24), "\n"), "Claude Code") && !s.AtPrompt()
			})
			sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Input(sub, []byte("please fix the login bug")); err != nil {
				t.Fatal(err)
			}
			untilIdle(t, s)
			_, _ = p.outW.Write([]byte("\x1b[?2026l"))
			draw(t, s, p, claudePromptRow("please fix the login bug"))
			moveWaitFor(t, "the prompt with the text", s.AtPrompt)
			refused(t, s, f, ts)
		})
	}
	t.Run("text queued as the move begins", func(t *testing.T) {
		s, p := memberSession(t, false)
		f, ts := sessiontest.NewFakeProtocol(), &terminals{}
		ts.cleanup(t)
		gate := make(chan struct{})
		f.BeginGate = gate
		t.Cleanup(func() {
			select {
			case <-gate:
			default:
				close(gate)
			}
		})
		draw(t, s, p, claudePromptRow(""))
		sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		// The writer held on a focus report; the person's text queued behind
		// it, past input's look at the move; then the move, held at its
		// chat's start.
		takeAgain := stopTaking(t, p)
		go func() { _ = s.Input(sub, []byte("\x1b[I")) }()
		untilWaiting(t, s, 0)
		typedErr := make(chan error, 1)
		go func() { typedErr <- s.Input(sub, []byte("hello")) }()
		untilWaiting(t, s, 1)
		moveErr := make(chan error, 1)
		go func() { moveErr <- s.MoveFresh(memberStarts(f, ts)) }()
		moveWaitFor(t, "the move begun, or refused", func() bool { return s.Moving() || len(moveErr) > 0 })
		takeAgain()
		// The bound only bounds a hang of the input's answer.
		answered := func() error {
			select {
			case err := <-typedErr:
				return err
			case <-time.After(30 * time.Second):
				t.Fatal("the person's text was never answered")
				return nil
			}
		}
		var ierr error
		got := false
		if len(moveErr) == 0 {
			// The move under way: the text, written after it began, is
			// refused, never written into the terminal it stops.
			ierr, got = answered(), true
			close(gate)
		}
		err = <-moveErr
		if !got {
			ierr = answered()
		}
		wrote := false
		for len(p.input) > 0 {
			if strings.Contains(string(<-p.input), "hello") {
				wrote = true
			}
		}
		switch {
		case wrote && err == nil:
			t.Fatalf("the person's text was written into the terminal the move stopped (Input %v)", ierr)
		case !wrote && !errors.Is(ierr, ErrMoving):
			t.Fatalf("the text was not written, and Input said %v, not that the session is moving", ierr)
		case err == nil && s.Way() != WayProtocol:
			t.Fatalf("the move: way %s", s.Way())
		}
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
	t.Run("a dialog's keys and a startup question's", func(t *testing.T) {
		// Keys that only move within a dialog or answer it, and text typed
		// while the trust question shows, leave the move allowed.
		s, p := memberSession(t, false)
		f, ts := sessiontest.NewFakeProtocol(), &terminals{}
		ts.cleanup(t)
		was := s.Info().AgentSession.ID
		draw(t, s, p, memberTrustFrame)
		moveWaitFor(t, "the trust question shown", func() bool {
			att := s.Info().Attention
			return att.State == AttentionNeedsInput && att.Source == SourceTrust
		})
		sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		for _, keys := range []string{"abc", "\x1b[A", "\x1b[B", "2", " ", "\t", "\x1b[Z", "\r"} {
			if err := s.Input(sub, []byte(keys)); err != nil {
				t.Fatal(err)
			}
			untilIdle(t, s)
		}
		// A dialog Conductor does not know (Claude Code's question about a
		// folder's tool servers): its keys move and answer within it.
		draw(t, s, p, " New MCP server found in .mcp.json: tools\r\n\r\n ❯ Use this and all future MCP servers in this project\r\n   Use this MCP server\r\n\r\n Enter to confirm · Esc to reject")
		moveWaitFor(t, "the trust question gone", func() bool {
			att := s.Info().Attention
			return !(att.State == AttentionNeedsInput && att.Source == SourceTrust) && !s.startupMayShow()
		})
		for _, keys := range []string{"\x1b[B", "\x1b[A", "1", "\r"} {
			if err := s.Input(sub, []byte(keys)); err != nil {
				t.Fatal(err)
			}
			untilIdle(t, s)
		}
		draw(t, s, p, claudePromptRow(""))
		moveWaitFor(t, "the trust question gone", func() bool { return s.Info().Attention.State != AttentionNeedsInput })
		moved(t, s, f, ts, was)
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
