package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A crew member's chat's child (design 14) from its launch: a fresh one
// starts a new conversation by the session recipe's start and a resumed
// one resumes its conversation, neither with a permission mode of
// Conductor's for a member that is not yolo; a person's chat keeps its
// mode. What the move's checks refuse, before anything is stopped, is told
// in the run's log's words.
func TestAMembersChatArgvAndItsChecks(t *testing.T) {
	a, ok := catalog.Default().Get("claude")
	if !ok {
		t.Fatal("no built-in Claude Code")
	}
	l := &chatLaunch{agent: a, args: []string{"--fallback-model=sonnet"}, gate: &chatGate{}}
	id := "99999999-8888-4777-8666-555555555555"
	fresh, err := memberChatArgv(true)(l, id, "/data/hooks", true)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.Index(fresh, "--session-id")
	if at < 0 || fresh[at+1] != id || slices.Contains(fresh, "--resume") || slices.Contains(fresh, "--permission-mode") || !slices.Contains(fresh, "--strict-mcp-config") {
		t.Fatalf("a fresh member's chat: %q", fresh)
	}
	resumed, err := memberChatArgv(false)(l, id, "/data/hooks", true)
	if err != nil || resumed[slices.Index(resumed, "--resume")+1] != id || slices.Contains(resumed, "--session-id") || slices.Contains(resumed, "--permission-mode") {
		t.Fatalf("a resumed member's chat: %q %v", resumed, err)
	}
	person, err := chatArgv(l, id, "/data/hooks", true)
	if err != nil || person[slices.Index(person, "--permission-mode")+1] != "default" {
		t.Fatalf("a person's chat: %q %v", person, err)
	}
	// The checks the move asks with the terminal running.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--resume", "x"}, "its arguments hold --resume, which a chat sets itself."},
		{[]string{"--add-dir", "/other"}, "it was launched with an added folder."},
	} {
		bad := *l
		bad.args = c.args
		_, err := memberChatArgv(true)(&bad, id, "/data/hooks", true)
		var r *crew.TerminalReason
		if err == nil || !errors.As(memberReason(err), &r) || r.Words != c.want {
			t.Fatalf("%q: %v", c.args, err)
		}
	}
	for err, want := range map[error]string{
		agentproto.ErrNotQualified: "this version of Claude Code can only run in its terminal.",
		errNoPipes:                 "the run's Runtime has no pipes.",
		errNoChat:                  "its agent has no chat.",
	} {
		if got := memberReason(err).Error(); got != want {
			t.Fatalf("%v: %q, want %q", err, got, want)
		}
	}
}

// A member whose agent gives no prompt shape is no Chat member: the move
// waits for its prompt by its shape and could never see it, so it runs in
// its terminal, the run's log saying why.
func TestAMemberWithNoPromptShapeRunsInItsTerminal(t *testing.T) {
	claude, ok := catalog.Default().Get("claude")
	if !ok {
		t.Fatal("no built-in Claude Code")
	}
	bare := claude.Copy()
	bare.ID, bare.PromptShape = "bare-claude", ""
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{bare})
	var r *crew.TerminalReason
	if err := e.srv.ChatCheck(t.Context(), "bare-claude", nil); !errors.As(err, &r) || r.Words != memberNoPromptWords {
		t.Fatalf("ChatCheck = %v, want %q", err, memberNoPromptWords)
	}
}

// A fresh member's conversation is started fresh once: its chat's restart
// past the input bound, and its way back to its terminal (an agent not
// signed in), resume it, for the recipe's start on an id in use is refused.
func TestAFreshMembersConversationIsResumedAfterItsFirstStart(t *testing.T) {
	for _, after := range []string{"the restart", "the way back"} {
		t.Run(after, func(t *testing.T) {
			env := map[string]string{}
			if after == "the way back" {
				env["STUB_PROTOCOL_SIGNED_OUT"] = "1"
			}
			c := newChatEnv(t, env, nil)
			if after == "the restart" {
				c.srv.chatMaxInput = 1
			}
			var mu sync.Mutex
			var procs []*sessiontest.FakeProc
			c.rt.Terminal = func(ext.TerminalSpec) (*sessiontest.FakeProc, error) {
				p := sessiontest.NewFakeProc()
				mu.Lock()
				procs = append(procs, p)
				mu.Unlock()
				return p, nil
			}
			local, err := c.srv.Launch(t.Context(), crew.LaunchSpec{AgentID: "claude", Name: "lead · build #1", Cwd: c.root,
				Ref: session.CrewRef{RunID: "fresh-00000001", CrewID: "fresh", Member: "lead", Task: "build", Attempt: 1}, Way: crew.WayProtocol})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = local.Stop(context.Background()) })
			mu.Lock()
			first := procs[0]
			mu.Unlock()
			rule := strings.Repeat("─", 80)
			_ = first.Print("\x1b[2J\x1b[H ▐▛███▜▌   Claude Code\r\n\r\n" + rule + "\r\n❯ \r\n" + rule + "\r\n  ? for shortcuts")
			until(t, "the member's prompt", local.AtPrompt)
			m, err := c.srv.MemberMove(t.Context(), local, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := local.MoveFresh(m); err != nil {
				t.Fatal(err)
			}
			conv := local.Info().AgentSession.ID
			resumes := func(what string, argv []string) {
				t.Helper()
				if i := indexOf(argv, "--resume"); i < 0 || argv[i+1] != conv || indexOf(argv, "--session-id") >= 0 {
					t.Fatalf("%s: %q, want --resume %s", what, argv, conv)
				}
			}
			if specs := c.pipeSpecs(); len(specs) != 1 || indexOf(specs[0].Argv, "--session-id") < 0 || specs[0].Argv[indexOf(specs[0].Argv, "--session-id")+1] != conv {
				t.Fatalf("the first start: %+v", specs)
			}
			if after == "the restart" {
				for _, text := range []string{"first", "second"} {
					if _, err := local.Submit(context.Background(), session.Submission{Text: text}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
						t.Fatal(err)
					}
				}
				entriesWhen(t, local, "the second answered by the new child", func(es []session.TranscriptEntry) bool {
					return hasEntry(es, func(e session.TranscriptEntry) bool {
						return e.Kind == session.EntryText && e.Text == "stub chat: second" && e.Final
					})
				})
				specs := c.pipeSpecs()
				if len(specs) < 2 {
					t.Fatalf("%d children started", len(specs))
				}
				resumes("the restarted child", specs[1].Argv)
			} else {
				if _, err := local.Submit(context.Background(), session.Submission{Text: "hello"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
					t.Fatal(err)
				}
				until(t, "the way back", func() bool { return local.Way() == session.WayTerminal && !local.Moving() })
				terms := c.rt.Asked("StartTerminal")
				if len(terms) != 2 {
					t.Fatalf("%d terminals started", len(terms))
				}
				resumes("the terminal back", terms[1].Spec.(ext.TerminalSpec).Argv)
			}
			if local.Info().Status.Ended() {
				t.Fatalf("the session ended: %q", local.Info().Status)
			}
		})
	}
}
