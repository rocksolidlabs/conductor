package session

import (
	"errors"
	"strings"
	"testing"
)

// Codex's form asking to allow a tool of an MCP server (#716), as its TUI
// draws an MCP tool call's approval, the same in 0.163.0, 0.167.2 and
// 0.169.0 (github.com/openai/codex at tags rust-v0.163.0, rust-v0.167.2 and
// rust-v0.169.0: tui/src/bottom_pane/mcp_server_elicitation.rs, its
// snapshots mcp_server_elicitation_approval_form_with_session_persist.snap
// and ..._without_schema.snap, the message from core's
// build_mcp_tool_approval_fallback_message in src/mcp_tool_call.rs). A digit
// picks its choice and submits the form; Esc cancels it. What is above the
// form, and what Codex draws once it is answered, is made up.
var (
	codexToolAbove = []string{
		"› Look up how sessions work",
		"",
		"• I'll read the guide first.",
		"",
	}
	codexToolForm = screenRows(append(append([]string{}, codexToolAbove...),
		"  Field 1/1",
		`  Allow the conductor MCP server to run tool "guide"?`,
		"  › 1. Allow                   Run the tool and continue",
		"    2. Allow for this session  Run the tool and remember this choice for this session",
		"    3. Always allow            Run the tool and remember this choice for future tool calls",
		"    4. Cancel                  Cancel this tool call",
		"", "", "", "", "", "", "",
		"  enter to submit | esc to cancel",
	)...)
	// The server's approval mode set to prompt, 80 columns wide: Allow and
	// Cancel, the call's argument under the message, the footer wrapped.
	codexToolFormOnce = screenRows(append(append([]string{}, codexToolAbove...),
		"  Field 1/1",
		`  Allow the docs MCP server to run tool "search"?`,
		"",
		"  query: session attention",
		"",
		"  › 1. Allow   Run the tool and continue",
		"    2. Cancel  Cancel this tool call",
		"",
		"  enter to submit |",
		"  esc to cancel",
	)...)
	codexToolWords = `Allow the conductor MCP server to run tool "guide"?`
	// codexToolCalled is Codex once the form is answered: the call's cell,
	// and Codex at work.
	codexToolCalled = screenRows(append(append([]string{}, codexToolAbove...),
		"• Called conductor.guide({})",
		"  └ Sessions are …",
		"",
		"• Working (3s • esc to interrupt)",
	)...)
	codexToolChoices = []Option{{Label: "Allow", Input: "1"}, {Label: "Allow for this session", Input: "2"}, {Label: "Always allow", Input: "3"}, {Label: "Cancel", Input: "4"}}
)

// Codex's program status (OSC 7501): working, and blocked as 0.163.0 says
// it, with no kind, which raises no wait of its own; 0.167.2 adds the kind.
const (
	statusWorking       = "\x1b]7501;state=working:app=codex\x1b\\"
	statusBlocked163    = "\x1b]7501;state=blocked:app=codex\x1b\\"
	statusBlockedPermit = "\x1b]7501;state=blocked:app=codex:kind=permission\x1b\\"
	codexToolFormCols   = 120
	codexToolFormRows   = 40
	codexToolOnceCols   = 80
	codexToolOnceHeight = 24
)

// newCodexToolSession is a session of Codex's, cols by rows, with its
// questions asked during the work (the catalog's) and a controller
// attached.
func newCodexToolSession(t *testing.T, cols, rows uint16) (*Local, *fakeProc, *Subscription) {
	t.Helper()
	p := newFakeProc()
	s := NewLocal(Info{ID: "sess", Cwd: realDir(t, t.TempDir()), Cols: cols, Rows: rows}, p, quiet(Options{Questions: approvalQuestions()}))
	t.Cleanup(func() { p.exit() })
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: cols, Rows: rows}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	return s, p, sub
}

// The bug as filed (#716): Codex 0.163 at work (its status working) shows
// the form and says blocked with no kind, which raises nothing, so the
// session read as working. The form is a question now: asked with its words
// and its own four choices, a permission, typed text refused; a reply
// writes its choice's digit alone, and the question goes with the form.
func TestCodexsToolApprovalIsAskedWithItsOwnChoices(t *testing.T) {
	s, p, sub := newCodexToolSession(t, codexToolFormCols, codexToolFormRows)
	p.outW.Write([]byte(statusWorking))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionWorking })
	p.outW.Write(append([]byte(statusBlocked163), drawn(codexToolForm)...))
	att := waitAttention(t, s, asked)
	if att.Message != codexToolWords || att.Kind != KindPermission || !sameOptions(att.Options, codexToolChoices) {
		t.Fatalf("the form asked %+v", att)
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "yes go ahead", By: sub}); !errors.Is(err, ErrQuestion) {
		t.Fatalf("typed text at the form: %v", err)
	}
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte("2")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "2" {
		t.Fatalf("written %q", got)
	}
	replied(t, s, att.ID)
	if a := formGone(t, s, p); a.State == AttentionNeedsInput {
		t.Fatalf("once the form went: %+v", a)
	}
}

// replied waits for the reply to the question id to resolve it, as it does
// once its keys are written: the question no longer shows.
func replied(t *testing.T, s *Local, id string) {
	t.Helper()
	waitAttention(t, s, func(a Attention) bool { return a.ID != id })
}

// formGone draws Codex once its form is answered (codexToolCalled, its
// status working) and returns the attention once the watcher of the
// questions asked during the work has looked at that screen and its look
// has ended (the look takes the screen, then decides under fireMu): the
// form answered is forgotten then, and the same form drawn again is a
// question again.
func formGone(t *testing.T, s *Local, p *fakeProc) Attention {
	t.Helper()
	p.outW.Write(append([]byte(statusWorking), drawn(codexToolCalled)...))
	waitAttention(t, s, func(Attention) bool {
		return strings.Contains(s.asked.Text(), "Working (3s") && s.asked.LookedAt() == s.asked.Fed()
	})
	s.asked.fireMu.Lock()
	a := s.Info().Attention
	s.asked.fireMu.Unlock()
	return a
}

// Each choice writes its own digit, which picks it in the form at once,
// and nothing else: no Enter. The same form for the next call is asked
// again once the one answered has gone. Codex 0.167.2 says blocked with
// its kind first: the form's question takes the place of the wait that
// raises.
func TestEachChoiceOfCodexsToolApprovalWritesItsDigit(t *testing.T) {
	s, p, sub := newCodexToolSession(t, codexToolFormCols, codexToolFormRows)
	last := ""
	for _, c := range codexToolChoices {
		p.outW.Write(append([]byte(statusBlockedPermit), drawn(codexToolForm)...))
		att := waitAttention(t, s, func(a Attention) bool { return asked(a) && a.ID != last })
		last = att.ID
		var pick *Option
		for i := range att.Options {
			if att.Options[i].Label == c.Label {
				pick = &att.Options[i]
			}
		}
		if pick == nil || pick.Input != c.Input {
			t.Fatalf("%s: offered %+v", c.Label, att.Options)
		}
		if err := s.AnswerInput(t.Context(), sub, att.ID, []byte(pick.Input)); err != nil {
			t.Fatalf("%s: %v", c.Label, err)
		}
		if got := nextWrite(t, p); got != c.Input {
			t.Fatalf("%s: written %q", c.Label, got)
		}
		replied(t, s, att.ID)
		if a := formGone(t, s, p); a.State == AttentionNeedsInput {
			t.Fatalf("%s: once the form went: %+v", c.Label, a)
		}
	}
}

// The form with two choices (the server's approval mode set to prompt), 80
// columns wide, its footer on two rows and the call's argument under its
// message: asked with Allow and Cancel, its words the message and the
// argument.
func TestCodexsToolApprovalWithTwoChoicesIsAsked(t *testing.T) {
	s, p, sub := newCodexToolSession(t, codexToolOnceCols, codexToolOnceHeight)
	p.outW.Write(drawn(codexToolFormOnce))
	att := waitAttention(t, s, asked)
	if !sameOptions(att.Options, []Option{{Label: "Allow", Input: "1"}, {Label: "Cancel", Input: "2"}}) {
		t.Fatalf("the choices %+v", att.Options)
	}
	if att.Message != "Allow the docs MCP server to run tool \"search\"? query: session attention" {
		t.Fatalf("the words %q", att.Message)
	}
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte("2")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "2" {
		t.Fatalf("written %q", got)
	}
}

// The form answered by a key typed in the session's terminal: the question
// is answered there (a reply is refused, nothing written), and it goes
// once Codex moves on from the form.
func TestCodexsToolApprovalAnsweredInTheTerminalGoes(t *testing.T) {
	s, p, sub := newCodexToolSession(t, codexToolFormCols, codexToolFormRows)
	p.outW.Write(drawn(codexToolForm))
	att := waitAttention(t, s, asked)
	if err := s.Input(sub, []byte("1")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "1" {
		t.Fatalf("written %q", got)
	}
	if a := s.Info().Attention; a.ID != att.ID || a.Answered != AnsweredTerminal {
		t.Fatalf("the key typed in the terminal: %+v", a)
	}
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte("4")); !errors.Is(err, ErrQuestionAnswered) {
		t.Fatalf("a reply after the key: %v", err)
	}
	if a := formGone(t, s, p); a.State == AttentionNeedsInput {
		t.Fatalf("once the form went: %+v", a)
	}
}

// The form's drawing in what Codex shows of a file, its composer below,
// is no question: the screen's look (CheckNow, the watcher's own) finds
// none of Codex's questions on it, and the session asks nothing.
func TestCodexsToolApprovalQuotedInTheTranscriptIsNotAsked(t *testing.T) {
	s, p, _ := newCodexToolSession(t, codexToolFormCols, codexToolFormRows)
	quoted := codexToolForm + "\n\n• Ran cat notes.md\n\n› Ask Codex to do anything\n\n  ? for shortcuts"
	p.outW.Write(append([]byte(statusWorking), drawn(quoted)...))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionWorking })
	// The screen the look reads has the whole drawing.
	waitAttention(t, s, func(Attention) bool { return strings.Contains(s.asked.Text(), "? for shortcuts") })
	if s.asked.CheckNow() {
		t.Fatalf("the quoted form was asked: %+v", s.Info().Attention)
	}
	if a := s.Info().Attention; a.State == AttentionNeedsInput {
		t.Fatalf("attention %+v", a)
	}
	if _, words, _, _ := s.askedIn(quoted, codexToolFormCols); words != "" {
		t.Fatalf("read as a question: %q", words)
	}
}

// codexToolForm72 is the form 72 columns wide (a page of 1280 by 800 with
// touch), laid out by the rules of selection_popup_common.rs: the
// description column two past the longest name, each description wrapped
// under it.
var codexToolForm72 = []string{
	"› Look up how sessions work",
	"",
	"  Field 1/1",
	`  Allow the conductor MCP server to run tool "guide"?`,
	"  › 1. Allow                   Run the tool and continue",
	"    2. Allow for this session  Run the tool and remember this choice",
	"                               for this session",
	"    3. Always allow            Run the tool and remember this choice",
	"                               for future tool calls",
	"    4. Cancel                  Cancel this tool call",
	"",
	"  enter to submit | esc to cancel",
}

// The form shown 80 columns wide, then the window made 72 columns wide:
// Codex draws its view again, erased from its first cell (Tui::draw's
// clear_for_viewport_change), the descriptions wrapped. The question is
// asked again with the same choices, a key typed in the terminal answers
// it, and it goes once Codex has moved on, at 72 columns.
func TestCodexsToolApprovalAt72ColumnsIsAskedAndGoes(t *testing.T) {
	s, p, sub := newCodexToolSession(t, 80, 24)
	p.outW.Write([]byte("\x1b[?1049h" + codexFrame(strings.Split(codexToolForm, "\n"))))
	waitAttention(t, s, asked)
	if err := s.Resize(sub, 72, 24); err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(codexFrame(codexToolForm72)))
	if a := startupLooked(t, s, "for future tool calls"); !asked(a) || !sameOptions(a.Options, codexToolChoices) || a.Message != codexToolWords {
		t.Fatalf("the form at 72 columns: %+v", a)
	}
	att := s.Info().Attention
	if err := s.Input(sub, []byte("4")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "4" {
		t.Fatalf("written %q", got)
	}
	if a := s.Info().Attention; a.ID != att.ID || a.Answered != AnsweredTerminal {
		t.Fatalf("the key typed in the terminal: %+v", a)
	}
	p.outW.Write([]byte(statusWorking + codexFrame([]string{"› Look up how sessions work", "", "• Cancelled conductor.guide", "", "• Working (3s • esc to interrupt)"})))
	if a := startupLooked(t, s, "Cancelled conductor.guide"); a.State == AttentionNeedsInput {
		t.Fatalf("once the form went at 72 columns: %+v", a)
	}
}
