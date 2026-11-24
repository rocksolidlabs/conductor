package session_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// shapedAgents are the built-in agents with a prompt shape.
func shapedAgents(t *testing.T) []catalog.Agent {
	t.Helper()
	var out []catalog.Agent
	for _, id := range []string{"codex", "claude"} {
		a, ok := catalog.Default().Get(id)
		if !ok || a.PromptShape == "" {
			t.Fatalf("the built-in %s has no prompt shape", id)
		}
		out = append(out, a)
	}
	return out
}

// serverOptions are the options the server starts its own session of a
// with (internal/api's createLocalSession): the agent's recipe compiled
// and set on them. They hold the catalog's prompt shape.
func serverOptions(t *testing.T, a catalog.Agent) session.Options {
	t.Helper()
	w, err := agents.CompileRecipe(agents.Recipe(a))
	if err != nil {
		t.Fatal(err)
	}
	var o session.Options
	w.Apply(&o)
	if o.PromptShape == nil || o.PromptShape.String() != a.PromptShape {
		t.Fatalf("%s: the server's own session has the prompt shape %v, want the catalog's", a.ID, o.PromptShape)
	}
	return o
}

// hostedOptions are the options a host starts its session of a with: the
// recipe a server sends a host whose register says it reads forms and
// takes the prompt shape, in registered on the wire, read back as the host
// reads registered, checked and compiled as the host takes it (Check,
// CompileRecipe) and set on its options as the host sets them.
func hostedOptions(t *testing.T, a catalog.Agent) session.Options {
	t.Helper()
	b, err := json.Marshal(proto.Registered{T: proto.HostRegistered, SessionID: "h", Links: []string{}, Recipe: agents.Recipe(a)})
	if err != nil {
		t.Fatal(err)
	}
	var reg proto.Registered
	if err := json.Unmarshal(b, &reg); err != nil || reg.Recipe == nil {
		t.Fatalf("registered %s: %v", b, err)
	}
	if err := reg.Recipe.Check(); err != nil {
		t.Fatal(err)
	}
	w, err := agents.CompileRecipe(reg.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	var o session.Options
	w.Apply(&o)
	return o
}

// The line's rule gives a hosted session of Claude Code or Codex the
// answer it gives the server's own session of the same agent, on every
// screen of the rule's matrix at every size (TestTheStartupLineRefusesWhereEitherGuardDid's),
// and holds the hosted session to the same oracles: wherever the guard a
// line's text had refuses, or the guard a line's Enter had holds, the rule
// refuses the line, and atPrompt answers as it did. The hosted session's
// options come from the recipe as a host is sent it.
func TestAHostedSessionJudgesALineAsTheServersOwnDoes(t *testing.T) {
	typed, refused := 0, 0
	for _, a := range shapedAgents(t) {
		server, hosted := serverOptions(t, a), hostedOptions(t, a)
		for _, size := range [][2]int{{80, 24}, {80, 5}, {80, 2}, {80, 1}} {
			for _, sc := range session.MatrixScreens() {
				name := fmt.Sprintf("%s at %dx%d, %q", a.ID, size[0], size[1], sc.Name)
				var got [2]session.LineJudgement
				for i, opts := range []session.Options{server, hosted} {
					ts := session.NewTestSession(t, opts, size[0], size[1])
					ts.Write("\x1b[?1049h\x1b[2J\x1b[H" + sc.Screen)
					got[i] = ts.Judge()
				}
				own, h := got[0], got[1]
				if (h.TextRefused || h.EnterHeld) && h.Line == nil {
					t.Errorf("%s: a guard refused the hosted session's line (text %v, Enter %v), the rule types it", name, h.TextRefused, h.EnterHeld)
				}
				if h.AtPrompt != h.WasAtPrompt {
					t.Errorf("%s: the hosted session's atPrompt %v, as it was %v", name, h.AtPrompt, h.WasAtPrompt)
				}
				if !errors.Is(h.Line, own.Line) || !errors.Is(own.Line, h.Line) || h.AtPrompt != own.AtPrompt || h.TextRefused != own.TextRefused || h.EnterHeld != own.EnterHeld {
					t.Errorf("%s: the hosted session is judged %+v, the server's own %+v", name, h, own)
				}
				if h.Line == nil {
					typed++
				} else {
					refused++
				}
			}
		}
	}
	if typed == 0 || refused == 0 {
		t.Fatalf("the matrix typed %d lines and refused %d: it judges nothing", typed, refused)
	}
}

// A line submitted to a hosted session is taken as the server's own
// session takes it, through Submit and the writer: its text refused, and
// nothing written, on Codex's update dialog drawn with its footer alone;
// an Enter alone refused, and nothing written, where a sign-in no catalog
// entry knows is drawn as the writer takes it; and at the agent's prompt
// the line typed and entered.
func TestALineIntoAHostedSessionIsTakenAsTheServersOwnTakesIt(t *testing.T) {
	codex, _ := catalog.Default().Get("codex")
	claude, _ := catalog.Default().Get("claude")
	type outcome struct {
		typed, entered bool
		err            error
		written        string
	}
	for _, tc := range []struct {
		name   string
		agent  catalog.Agent
		screen string
		text   string
		pause  string
		want   outcome
	}{
		{"a line at the update dialog's footer alone", codex, session.CodexFooterOnlyScreen, "1 fix the bug", "", outcome{err: session.ErrStartupNotAtPrompt}},
		{"an Enter alone, a sign-in drawn as it is written", codex, session.CodexPromptScreen, "", session.UnknownSignInScreen, outcome{err: session.ErrStartupNotAtPrompt}},
		{"a line at Codex's prompt", codex, session.CodexPromptScreen, "fix the bug", "", outcome{typed: true, entered: true, written: "fix the bug|\r"}},
		{"a line at Claude Code's prompt", claude, session.ClaudePromptScreen, "fix the bug", "", outcome{typed: true, entered: true, written: "fix the bug|\r"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got [2]outcome
			for i, opts := range []session.Options{serverOptions(t, tc.agent), hostedOptions(t, tc.agent)} {
				ts := session.NewTestSession(t, opts, 80, 24)
				ts.Write("\x1b[?1049h\x1b[2J\x1b[H" + tc.screen)
				if tc.pause != "" {
					shown := false
					ts.BeforeEnter(func() { shown = ts.DrawsOn(tc.pause) })
					defer func() {
						if !shown {
							t.Errorf("the pause screen was never drawn")
						}
					}()
				}
				res, err := ts.Submit(t.Context(), session.Submission{Text: tc.text, By: ts.Sub})
				ts.BeforeEnter(nil)
				got[i] = outcome{res.Typed, res.Entered, err, strings.Join(ts.Written(t), "|")}
			}
			own, h := got[0], got[1]
			if h.typed != own.typed || h.entered != own.entered || !errors.Is(h.err, own.err) || !errors.Is(own.err, h.err) || h.written != own.written {
				t.Fatalf("the hosted session took it %+v, the server's own %+v", h, own)
			}
			if h.typed != tc.want.typed || h.entered != tc.want.entered || !errors.Is(h.err, tc.want.err) || h.written != tc.want.written {
				t.Fatalf("the hosted session took it %+v, want %+v", h, tc.want)
			}
		})
	}
}
