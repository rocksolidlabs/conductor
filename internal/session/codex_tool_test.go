package session

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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
	// Cancel, the call's argument under the message, the footer on one row
	// (Codex wraps it only where the form is narrower than its 31
	// characters, footer_hint.rs's wrap_hint_rows).
	codexToolFormOnce = screenRows(append(append([]string{}, codexToolAbove...),
		"  Field 1/1",
		`  Allow the docs MCP server to run tool "search"?`,
		"",
		"  query: session attention",
		"",
		"  › 1. Allow   Run the tool and continue",
		"    2. Cancel  Cancel this tool call",
		"",
		"  enter to submit | esc to cancel",
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
	// codexToolChoices are the form's four choices as a page is handed
	// them: each allowing choice its digit, the two that remember the
	// approval mode answers, and Cancel the Escape the footer names.
	codexToolChoices = []Option{{Label: "Allow", Input: "1"}, {Label: "Allow for this session", Input: "2", Mode: true}, {Label: "Always allow", Input: "3", Mode: true}, {Label: "Cancel", Input: "\x1b"}}
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
	if !sameOptions(att.Options, []Option{{Label: "Allow", Input: "1"}, {Label: "Cancel", Input: "\x1b"}}) {
		t.Fatalf("the choices %+v", att.Options)
	}
	if att.Message != "Allow the docs MCP server to run tool \"search\"? query: session attention" {
		t.Fatalf("the words %q", att.Message)
	}
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte(att.Options[1].Input)); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "\x1b" {
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

// codexToolForm80 is the form 80 columns wide, laid out the same way, the
// highlight moved to Cancel by an arrow.
var codexToolForm80 = []string{
	"› Look up how sessions work",
	"",
	"  Field 1/1",
	`  Allow the conductor MCP server to run tool "guide"?`,
	"    1. Allow                   Run the tool and continue",
	"    2. Allow for this session  Run the tool and remember this choice for this",
	"                               session",
	"    3. Always allow            Run the tool and remember this choice for",
	"                               future tool calls",
	"  › 4. Cancel                  Cancel this tool call",
	"",
	"  enter to submit | esc to cancel",
}

// codexToolForm40 is the form 40 columns wide, laid out the same way (the
// description column at 70% of the 36 columns inside the form's margins,
// each name wrapped in the 23 before it, each description in the 11 after
// it): 14 rows between Allow and Cancel.
var codexToolForm40 = []string{
	"  Field 1/1",
	"  Allow the conductor MCP server to",
	`  run tool "guide"?`,
	"  › 1. Allow               Run the",
	"                           tool and",
	"                           continue",
	"    2. Allow for this      Run the",
	"       session             tool and",
	"                           remember",
	"                           this choice",
	"                           for this",
	"                           session",
	"    3. Always allow        Run the",
	"                           tool and",
	"                           remember",
	"                           this choice",
	"                           for future",
	"                           tool calls",
	"    4. Cancel              Cancel this",
	"                           tool call",
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

// codexToolList is the form's four choices and its footer, 120 columns
// wide; codexToolScreen the history above, then rows, then that list.
var codexToolList = []string{
	"  › 1. Allow                   Run the tool and continue",
	"    2. Allow for this session  Run the tool and remember this choice for this session",
	"    3. Always allow            Run the tool and remember this choice for future tool calls",
	"    4. Cancel                  Cancel this tool call",
	"",
	"  enter to submit | esc to cancel",
}

func codexToolScreen(rows ...string) string {
	out := append(append([]string{}, codexToolAbove...), rows...)
	return screenRows(append(out, codexToolList...)...)
}

// Wherever the screen is not plainly the one form, no choices are offered:
// a row between the form's top and its list that reads as a choice (an
// argument's name holds a line break), rows that read as a second form's
// top and message, the same with the form's first row far above the list,
// the form drawn twice, a server's own form whose choices have no
// description column, the four choices as Codex lays them out 40 columns
// wide, the form whose Cancel row is named otherwise (no choice its own
// answer names), the form whose Cancel name has a no-break space after it
// (a name holds no white space but ASCII spaces). The form asked before is withdrawn, and a reply to it, with any of
// its choices' keys, writes nothing. Each screen is judged once the
// watcher has looked at it, told by a marker of its own (the form asked
// before holds none of them).
func TestCodexsToolApprovalNotPlainlyOneFormIsNotAsked(t *testing.T) {
	msg := `  Allow the conductor MCP server to run tool "guide"?`
	far := []string{"  Field 1/1", `  Allow the docs MCP server to run tool "search"?`, ""}
	for i := range 20 {
		far = append(far, fmt.Sprintf("  line %d of the arguments", i))
	}
	form := strings.Split(codexToolForm, "\n")
	for name, c := range map[string]struct{ screen, marker string }{
		"an argument that reads as a choice": {codexToolScreen("  Field 1/1", msg, "", "  note: pick", "  1. Allow everything", ""), "note: pick"},
		"a second top and message":           {codexToolScreen("  Field 1/1", `  Allow the docs MCP server to run tool "search"?`, "", "  query:", "  Field 1/1", msg), "query:"},
		"the first top far above":            {codexToolScreen(append(far, "  Field 1/1", msg)...), "line 19 of"},
		"the form twice":                     {screenRows(append(append([]string{}, form...), append([]string{"• Asked again"}, form[len(codexToolAbove):]...)...)...), "Asked again"},
		"a server's own form":                {screenRows(append(append([]string{}, codexToolAbove...), "  Field 1/1", "  Pick one", "", "  Decision", "  › 1. Allow", "    2. Cancel", "", "  enter to submit | esc to cancel")...), "Decision"},
		"40 columns":                         {screenRows(codexToolForm40...), "server to\n"},
		"its Cancel row named otherwise":     {strings.Replace(codexToolForm, "Cancel                  Cancel", "Cancel call             Cancel", 1), "Cancel call"},
		"a no-break space after Cancel":      {strings.Replace(codexToolForm, "Cancel                  Cancel", "Cancel                  Cancel", 1), "Cancel "},
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(codexToolForm, c.marker) || !strings.Contains(c.screen, c.marker) {
				t.Fatalf("the marker %q is not the screen's own", c.marker)
			}
			s, p, sub := newCodexToolSession(t, codexToolFormCols, codexToolFormRows)
			p.outW.Write(drawn(codexToolForm))
			first := waitAttention(t, s, asked)
			if _, words, answers, _ := s.askedIn(c.screen, codexToolFormCols); words != "" || answers != nil {
				t.Fatalf("read as the form: %q %+v", words, answers)
			}
			p.outW.Write(drawn(c.screen))
			if a := startupLooked(t, s, c.marker); asked(a) {
				t.Fatalf("asked: %+v", a)
			}
			for _, o := range first.Options {
				if err := s.AnswerInput(t.Context(), sub, first.ID, []byte(o.Input)); err == nil {
					t.Fatalf("a reply %q to the form asked before was taken", o.Input)
				}
			}
			noWrite(t, p, 100*time.Millisecond)
		})
	}
}

// Two forms that differ only in an argument's value are two questions: the
// second has an id of its own, a reply to the first is refused once the
// second shows, nothing written, and the second takes its own.
func TestTwoToolApprovalFormsThatDifferInAnArgumentAreTwoQuestions(t *testing.T) {
	s, p, sub := newCodexToolSession(t, codexToolOnceCols, codexToolOnceHeight)
	p.outW.Write(drawn(codexToolFormOnce))
	first := waitAttention(t, s, asked)
	p.outW.Write(drawn(strings.Replace(codexToolFormOnce, "query: session attention", "query: session attentiom", 1)))
	second := waitAttention(t, s, func(a Attention) bool { return asked(a) && a.ID != first.ID })
	if second.Message == first.Message || !strings.HasSuffix(second.Message, "attentiom") {
		t.Fatalf("the second form's words %q", second.Message)
	}
	if err := s.AnswerInput(t.Context(), sub, first.ID, []byte("1")); err == nil {
		t.Fatal("a reply to the first form was taken")
	}
	noWrite(t, p, 100*time.Millisecond)
	if err := s.AnswerInput(t.Context(), sub, second.ID, []byte("1")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "1" {
		t.Fatalf("written %q", got)
	}
}

// lookedSince returns the attention once the watcher of the questions asked
// during the work has fed more than fed and looked at all it was fed (the
// look takes the screen, then decides under fireMu).
func lookedSince(t *testing.T, s *Local, fed uint64) Attention {
	t.Helper()
	waitAttention(t, s, func(Attention) bool {
		return s.asked.Fed() > fed && s.asked.LookedAt() == s.asked.Fed()
	})
	s.asked.fireMu.Lock()
	a := s.Info().Attention
	s.asked.fireMu.Unlock()
	return a
}

// A form answered from a page is not asked again while the same form stays
// on the screen, whatever is fed or poked, as every question read off the
// screen: after a Cancel and nothing more drawn (the answer's own poke
// looked at), after an allowing digit and an empty frame, after a whole
// frame of the same form. A reply to it is refused and nothing more is
// written. So the same form Codex shows directly behind an answered one
// (the same server, tool and arguments) is not offered with choices; it is
// answered in the terminal. A form that differs in an argument is a new
// question, and so is the same form drawn again once a look has seen the
// screen without it.
func TestAnAnsweredToolApprovalFormIsNotAskedAgain(t *testing.T) {
	other := strings.Replace(codexToolForm, `tool "guide"?`, `tool "search"?`, 1)
	for name, c := range map[string]struct {
		choice string
		then   []byte
	}{
		"a Cancel, nothing more drawn":        {"\x1b", nil},
		"an allowing digit, an empty frame":   {"1", []byte("\x1b[?2026h\x1b[?2026l")},
		"a Cancel, the same form drawn again": {"\x1b", drawn(codexToolForm)},
		"a digit, the same form drawn again":  {"3", drawn(codexToolForm)},
	} {
		t.Run(name, func(t *testing.T) {
			s, p, sub := newCodexToolSession(t, codexToolFormCols, codexToolFormRows)
			p.outW.Write(drawn(codexToolForm))
			first := waitAttention(t, s, asked)
			fed := s.asked.Fed()
			if err := s.AnswerInput(t.Context(), sub, first.ID, []byte(c.choice)); err != nil {
				t.Fatal(err)
			}
			if got := nextWrite(t, p); got != c.choice {
				t.Fatalf("written %q", got)
			}
			// The answer's own poke, looked at: the form shows answered, or
			// not at all, never with choices to answer again.
			ids := []string{first.ID}
			check := func(a Attention, when string) {
				t.Helper()
				if asked(a) && a.Answered == "" {
					t.Fatalf("asked %s: %+v", when, a)
				}
				if asked(a) {
					ids = append(ids, a.ID)
				}
			}
			check(lookedSince(t, s, fed), "after the answer")
			if c.then != nil {
				fed = s.asked.Fed()
				p.outW.Write(c.then)
				check(lookedSince(t, s, fed), "again")
			}
			for _, id := range ids {
				for _, o := range first.Options {
					if err := s.AnswerInput(t.Context(), sub, id, []byte(o.Input)); err == nil {
						t.Fatalf("a second reply %q was taken", o.Input)
					}
				}
			}
			noWrite(t, p, 100*time.Millisecond)
			// A form that differs in an argument: a question of its own.
			p.outW.Write(drawn(other))
			second := waitAttention(t, s, func(a Attention) bool { return asked(a) && a.Answered == "" && !slices.Contains(ids, a.ID) })
			if !sameOptions(second.Options, codexToolChoices) || !strings.HasSuffix(second.Message, `tool "search"?`) {
				t.Fatalf("the other form %+v", second)
			}
		})
	}
	// The form gone at a look, then drawn again: a question of its own.
	s, p, sub := newCodexToolSession(t, codexToolFormCols, codexToolFormRows)
	p.outW.Write(drawn(codexToolForm))
	first := waitAttention(t, s, asked)
	if err := s.AnswerInput(t.Context(), sub, first.ID, []byte("1")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "1" {
		t.Fatalf("written %q", got)
	}
	if a := formGone(t, s, p); a.State == AttentionNeedsInput {
		t.Fatalf("once the form went: %+v", a)
	}
	p.outW.Write(drawn(codexToolForm))
	if a := waitAttention(t, s, func(a Attention) bool { return asked(a) && a.ID != first.ID && a.Answered == "" }); !sameOptions(a.Options, codexToolChoices) {
		t.Fatalf("the form drawn again %+v", a)
	}
}

// lookedAt returns the attention once the watcher of the questions asked
// during the work has looked at a screen holding has and not hasNot (when
// set), and at all it was fed (the look takes the screen, then decides
// under fireMu).
func lookedAt(t *testing.T, s *Local, has, hasNot string) Attention {
	t.Helper()
	waitAttention(t, s, func(Attention) bool {
		text := s.asked.Text()
		return strings.Contains(text, has) && (hasNot == "" || !strings.Contains(text, hasNot)) && s.asked.LookedAt() == s.asked.Fed()
	})
	s.asked.fireMu.Lock()
	a := s.Info().Attention
	s.asked.fireMu.Unlock()
	return a
}

// An answered form is forgotten only by a look whose screen holds no row
// that opens a form. The form answered (a digit, and Cancel) and still
// drawn, Codex having read no key: drawn for one look where it is not read
// as the form (40 columns wide, or with a second form's top row above it),
// then drawn as before, it is not offered with choices, a reply to it is
// refused and nothing is written. Once a look sees no form, the form drawn
// again is asked anew, with an id of its own.
func TestAnAnsweredToolApprovalFormIsKeptWhileARowOpensAForm(t *testing.T) {
	msg := `  Allow the conductor MCP server to run tool "guide"?`
	form := strings.Split(codexToolForm, "\n")
	second := strings.Split(codexToolScreen("  Field 1/1", `  Allow the docs MCP server to run tool "search"?`, "", "  query:", "  Field 1/1", msg), "\n")
	for _, choice := range []string{"1", "\x1b"} {
		for name, c := range map[string]struct {
			cols   uint16
			rows   []string
			marker string
		}{
			"40 columns":             {40, codexToolForm40, "server to\n"},
			"a second top row above": {codexToolFormCols, second, "query:"},
		} {
			t.Run(fmt.Sprintf("%s, %q", name, choice), func(t *testing.T) {
				s, p, sub := newCodexToolSession(t, codexToolFormCols, codexToolFormRows)
				p.outW.Write([]byte(codexFrame(form)))
				first := waitAttention(t, s, asked)
				fed := s.asked.Fed()
				if err := s.AnswerInput(t.Context(), sub, first.ID, []byte(choice)); err != nil {
					t.Fatal(err)
				}
				if got := nextWrite(t, p); got != choice {
					t.Fatalf("written %q", got)
				}
				ids := []string{first.ID}
				check := func(a Attention, when string) {
					t.Helper()
					if asked(a) && a.Answered == "" {
						t.Fatalf("asked %s: %+v", when, a)
					}
					if asked(a) {
						ids = append(ids, a.ID)
					}
				}
				check(lookedSince(t, s, fed), "after the answer")
				resize := func(cols uint16) {
					t.Helper()
					if err := s.Resize(sub, cols, codexToolFormRows); err != nil {
						t.Fatal(err)
					}
				}
				resize(c.cols)
				p.outW.Write([]byte(codexFrame(c.rows)))
				check(lookedAt(t, s, c.marker, ""), "on the look that does not read it")
				resize(codexToolFormCols)
				p.outW.Write([]byte(codexFrame(form)))
				check(lookedAt(t, s, "for future tool calls", c.marker), "drawn as before")
				for _, id := range ids {
					for _, o := range first.Options {
						if err := s.AnswerInput(t.Context(), sub, id, []byte(o.Input)); err == nil {
							t.Fatalf("a second reply %q was taken", o.Input)
						}
					}
				}
				noWrite(t, p, 100*time.Millisecond)
				// A look with no form, then the form again: asked anew.
				p.outW.Write([]byte(codexFrame(strings.Split(codexToolCalled, "\n"))))
				lookedAt(t, s, "Called conductor.guide", "Field 1/1")
				p.outW.Write([]byte(codexFrame(form)))
				again := waitAttention(t, s, func(a Attention) bool { return asked(a) && a.Answered == "" && !slices.Contains(ids, a.ID) })
				if !sameOptions(again.Options, codexToolChoices) || again.Message != codexToolWords {
					t.Fatalf("the form drawn again %+v", again)
				}
			})
		}
	}
}

// A form with two rows named Cancel, its own answer's label, gets no
// choices: neither row is taken for that answer. The question is the
// catalog's form with a pattern of its own that lets the two rows through
// (Codex's tool approval does not); the same screen with one Cancel row is
// read with its four choices.
func TestAFormWithTwoRowsNamedCancelGetsNoChoices(t *testing.T) {
	q := StartupQuestion{
		Pattern:    regexp.MustCompile(`(?m)^ *Field 1/1\b(?:.*\n)+ *enter to submit\W*esc to cancel\z`),
		Words:      regexp.MustCompile(`^ *Field \d+/\d+\b`),
		Anytime:    true,
		FromScreen: true,
		Answers:    []Option{{Label: "Cancel", Input: "\x1b"}},
	}
	once := codexToolScreen("  Field 1/1", `  Allow the conductor MCP server to run tool "guide"?`)
	if words, answers, _ := q.read(once); words != codexToolWords || len(answers) != 4 || answers[3].Input != "\x1b" {
		t.Fatalf("one Cancel row: %q %+v", words, answers)
	}
	twice := strings.Replace(once, "    3. Always allow            Run the tool and remember this choice for future tool calls", "    3. Cancel                  Cancel this tool call", 1)
	if twice == once {
		t.Fatal("no row replaced")
	}
	if words, answers, _ := q.read(twice); words != "" || answers != nil {
		t.Fatalf("two Cancel rows read as %q %+v", words, answers)
	}
}

// Cancel is sent as Escape, the key the form's footer names: with another
// of Codex's views drawn over the form after the session's last look at it
// (the pump holds the chunk that draws it, read but not yet looked at), a
// Cancel writes Escape or nothing.
func TestCodexsToolApprovalCancelWritesEscapeOrNothing(t *testing.T) {
	s, p, sub := newCodexToolSession(t, codexToolOnceCols, codexToolOnceHeight)
	p.outW.Write(drawn(codexToolFormOnce))
	form := waitAttention(t, s, asked)
	held, resume := make(chan struct{}), make(chan struct{})
	var hold, release sync.Once
	hook := func(chunk []byte) {
		if strings.Contains(string(chunk), "Would you like to run") {
			hold.Do(func() { close(held); <-resume })
		}
	}
	s.beforeScan.Store(&hook)
	t.Cleanup(func() { release.Do(func() { close(resume) }) })
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the approval's chunk never reached the pump")
	}
	cancel := form.Options[len(form.Options)-1]
	if cancel.Label != "Cancel" {
		t.Fatalf("the choices %+v", form.Options)
	}
	err := s.AnswerInput(t.Context(), sub, form.ID, []byte(cancel.Input))
	select {
	case b := <-p.input:
		if string(b) != "\x1b" {
			t.Fatalf("a Cancel wrote %q", b)
		}
	case <-time.After(200 * time.Millisecond):
		if err == nil {
			t.Fatal("a Cancel was taken and nothing written")
		}
	}
	release.Do(func() { close(resume) })
	waitAttention(t, s, func(a Attention) bool { return asked(a) && a.ID != form.ID })
	noWrite(t, p, 100*time.Millisecond)
}
