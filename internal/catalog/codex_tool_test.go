package catalog

import (
	"regexp"
	"strings"
	"testing"
)

// The screens of Codex's form asking to allow a tool of an MCP server
// (#716), as the session's Screen reads them (each row without the spaces
// at its end, the blank rows at the bottom left out). The form is what
// Codex's TUI draws for an MCP tool call's approval, the same in 0.163.0,
// 0.167.2 and 0.169.0: its rows are those of
// tui/src/bottom_pane/snapshots/codex_tui__bottom_pane__mcp_server_elicitation__tests__mcp_server_elicitation_approval_form_with_session_persist.snap
// (120 columns) and of ..._approval_form_without_schema.snap, with the
// message core asks it with (src/mcp_tool_call.rs,
// build_mcp_tool_approval_fallback_message), at github.com/openai/codex,
// tags rust-v0.163.0, rust-v0.167.2 and rust-v0.169.0. The history above
// it is made up.
var (
	toolHistory = []string{
		"› Look up how sessions work",
		"",
		"• I'll read the guide first.",
		"",
	}
	toolApproval = strings.Join(append(append([]string{}, toolHistory...),
		"  Field 1/1",
		`  Allow the conductor MCP server to run tool "guide"?`,
		"  › 1. Allow                   Run the tool and continue",
		"    2. Allow for this session  Run the tool and remember this choice for this session",
		"    3. Always allow            Run the tool and remember this choice for future tool calls",
		"    4. Cancel                  Cancel this tool call",
		"", "", "", "", "", "", "",
		"  enter to submit | esc to cancel",
	), "\n")
	// The server's approval mode set to prompt: Allow and Cancel alone,
	// with the call's arguments shown under the message.
	toolApprovalOnce = strings.Join(append(append([]string{}, toolHistory...),
		"  Field 1/1",
		`  Allow the docs MCP server to run tool "search"?`,
		"",
		"  query: session attention",
		"",
		"  › 1. Allow   Run the tool and continue",
		"    2. Cancel  Cancel this tool call",
		"", "", "", "", "",
		"  enter to submit | esc to cancel",
	), "\n")
	// toolApprovalNarrow is the four choices 80 columns wide, laid out by
	// the rules of tui/src/bottom_pane/selection_popup_common.rs (the
	// description column two past the longest name, a description wrapped
	// under its column), the footer drawn just below the list, and the
	// highlight moved to Cancel by an arrow.
	toolApprovalNarrow = strings.Join(append(append([]string{}, toolHistory...),
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
	), "\n")
)

// codexToolQuestion returns Codex's question for its tool approval form
// from the built-in catalog, its prompt and words compiled.
func codexToolQuestion(t *testing.T) (Question, *regexp.Regexp, *regexp.Regexp) {
	t.Helper()
	codex, _ := Default().Get("codex")
	var found []Question
	for _, q := range codex.Questions {
		if strings.Contains(q.Prompt, "Field 1/1") {
			found = append(found, q)
		}
	}
	if len(found) != 1 {
		t.Fatalf("Codex's tool approval question: %+v", codex.Questions)
	}
	q := found[0]
	prompt, err := CompilePattern(q.Prompt)
	if err != nil {
		t.Fatalf("prompt %q: %v", q.Prompt, err)
	}
	words, err := CompilePattern(q.Words)
	if err != nil {
		t.Fatalf("words %q: %v", q.Words, err)
	}
	return q, prompt, words
}

// Codex's form asking to allow a tool of an MCP server (#716) is one of its
// questions asked at any time, its choices read off the form (two or four,
// by the server's approval mode), its words the form's message and the
// arguments it shows. It is found as Codex draws it at every covered
// version, wide or narrow, the highlight on any choice; and the first of
// Codex's questions that matches it is this one, not a command approval.
func TestCodexToolApprovalIsAQuestionReadOffTheForm(t *testing.T) {
	q, prompt, words := codexToolQuestion(t)
	if !q.Anytime || !q.AnswersFromScreen || len(q.Answers) != 0 || q.ModeAnswers != "" || q.TypedAnswers != "" {
		t.Fatalf("the question %+v", q)
	}
	codex, _ := Default().Get("codex")
	for name, screen := range map[string]string{"four choices": toolApproval, "two choices": toolApprovalOnce, "80 columns": toolApprovalNarrow} {
		for i, other := range codex.Questions {
			if other.Prompt == q.Prompt {
				break
			}
			if re, _ := CompilePattern(other.Prompt); re.MatchString(screen) {
				t.Fatalf("%s: question %d (%q) takes the form", name, i, other.Prompt)
			}
		}
		if !prompt.MatchString(screen) {
			t.Errorf("%s: the form is not found:\n%s", name, screen)
		}
	}
	// Each footer form Codex draws: wrapped onto two rows on a narrow
	// screen, its separator at the first row's end or not; the progress
	// row with its count once the selection is cleared (Backspace).
	for _, variant := range []string{
		strings.Replace(toolApproval, "  enter to submit | esc to cancel", "  enter to submit |\n  esc to cancel", 1),
		strings.Replace(toolApproval, "  enter to submit | esc to cancel", "  enter to submit\n  esc to cancel", 1),
		strings.Replace(strings.Replace(toolApproval, "Field 1/1", "Field 1/1 (1 required unanswered)", 1), "  › 1.", "    1.", 1),
	} {
		if !prompt.MatchString(variant) {
			t.Errorf("not found:\n%s", variant)
		}
	}
	for screen, want := range map[string]string{
		toolApproval:     `Allow the conductor MCP server to run tool "guide"?`,
		toolApprovalOnce: "Allow the docs MCP server to run tool \"search\"?\n\n  query: session attention\n",
	} {
		all := words.FindAllStringSubmatch(screen, -1)
		if len(all) == 0 || strings.TrimSpace(all[len(all)-1][1]) != strings.TrimSpace(want) {
			t.Errorf("words %q, want %q", all, want)
		}
	}
}

// Not the form: its words or its whole drawing in what Codex shows of a
// file or a log, with Codex's composer below; another server's form, whose
// choices or fields are not a tool call's approval; the form with rows
// below its footer; a command approval.
func TestCodexToolApprovalNearMissesAreNotTheForm(t *testing.T) {
	_, prompt, _ := codexToolQuestion(t)
	composer := "\n\n• Ran cat notes.md\n  └ (the form above was in the file)\n\n› Ask Codex to do anything\n\n  ? for shortcuts"
	for name, text := range map[string]string{
		"the message alone":                    `• Allow the conductor MCP server to run tool "guide"?`,
		"the form in a file":                   toolApproval + composer,
		"the form quoted, a status line below": toolApproval + "\n\n  gpt-6 · 12% context used",
		// mcp_server_elicitation_message_only_form_with_persist_options.snap:
		// a server's own question, whose choices are Allow and Deny.
		"a server's own question": strings.Join([]string{
			"  Field 1/1",
			"  Boolean elicit MCP example: do you confirm?",
			"  › 1. Allow                   Allow this request and continue",
			"    2. Allow for this session  Allow this request and remember this choice for this session",
			"    3. Always allow            Allow this request and remember this choice for future requests",
			"    4. Deny                    Decline this request and continue",
			"    5. Cancel                  Cancel this request",
			"",
			"  enter to submit | esc to cancel",
		}, "\n"),
		// mcp_server_elicitation_message_only_form.snap: the same, its
		// choice not remembered.
		"a server's own question, three choices": strings.Join([]string{
			"  Field 1/1",
			"  Boolean elicit MCP example: do you confirm?",
			"  › 1. Allow   Allow this request and continue",
			"    2. Deny    Decline this request and continue",
			"    3. Cancel  Cancel this request",
			"",
			"  enter to submit | esc to cancel",
		}, "\n"),
		// mcp_server_elicitation_boolean_form.snap: a field to fill.
		"a server's form": strings.Join([]string{
			"  Field 1/1 (1 required unanswered)",
			"  Allow this request?",
			"",
			"  Confirm",
			"  Approve the pending action.",
			"  › 1. True",
			"    2. False",
			"",
			"  enter to submit | esc to cancel",
		}, "\n"),
		// A form of two fields: its second page.
		"a form of two fields":      strings.Replace(toolApproval, "Field 1/1", "Field 2/2", 1),
		"a choice out of its place": strings.Replace(toolApproval, "    4. Cancel ", "    4. Allow  ", 1),
		"a command approval":        "  Would you like to run the following command?\n\n  $ curl -sI https://example.com\n\n› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n\n  Press enter to confirm or esc to cancel",
	} {
		if prompt.MatchString(text) {
			t.Errorf("%s is taken for the form:\n%s", name, text)
		}
	}
}
