package session

import (
	"regexp"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// Claude Code 2.1.304's dialogs as it drew them in a PTY 120 columns wide
// on 2026-10-19 (#288), the screen's rows as the questions asked during
// the work are matched against (Screen.Text); the folder and the plan's
// path are made up, the banner left out.
var (
	solidRule  = strings.Repeat("─", 120)
	dashedRule = strings.Repeat("╌", 120)
	planRule   = "  " + strings.Repeat("─", 116)
)

func screenRows(rows ...string) string { return strings.Join(rows, "\n") }

// claudeBash is the permission dialog for a Bash command.
var claudeBash = screenRows(
	"",
	"❯ Run exactly this bash command and nothing else: touch probe.txt",
	"",
	"● Creating empty file probe.txt",
	"  ⎿  $ touch probe.txt",
	"",
	solidRule,
	" Bash command",
	` Tip: auto mode handles these prompts for you — choose "switch to auto mode" below`,
	" Create empty file probe.txt",
	dashedRule,
	" touch probe.txt",
	dashedRule,
	" Do you want to proceed?",
	" ❯ 1. Yes",
	"   2. Yes, and always allow access to /home/dev/project from this project",
	"   3. Yes, and switch to auto mode · auto mode handles these prompts for you",
	"   4. No",
	"",
	" Esc to cancel · Tab to amend",
)

// claudeWrite is the permission dialog for a file the Write tool creates.
var claudeWrite = screenRows(
	"❯ Use the Write tool to create a file named notes.txt containing the word hi. Do nothing else.",
	"",
	"● Write(notes.txt)",
	"",
	solidRule,
	" Create file",
	" notes.txt",
	dashedRule,
	"  1 hi",
	dashedRule,
	" Do you want to create notes.txt?",
	" ❯ 1. Yes",
	"   2. Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)",
	"   3. No",
	"",
	" Esc to cancel · Tab to amend",
)

// claudePlan is the plan's approval, its first choice as it is drawn when
// the session may bypass permissions or not.
func claudePlan(first string, highlight int) string {
	choices := []string{first, "Yes, manually approve edits", "Tell Claude what to change"}
	rows := []string{
		planRule,
		"   Ready to code?",
		"",
		"   Here is Claude's plan:",
		"  " + strings.Repeat("╌", 116),
		"   Create /home/dev/project/hello.txt containing the text hi.",
		"  " + strings.Repeat("╌", 116),
		"", "", "", "",
		planRule,
		"   Claude has written up a plan and is ready to execute. Would you like to proceed?",
		"",
	}
	for i, c := range choices {
		mark := "    "
		if i+1 == highlight {
			mark = "  ❯ "
		}
		rows = append(rows, mark+string(rune('1'+i))+". "+c)
	}
	return screenRows(append(rows,
		"       shift+tab to approve with this feedback",
		"",
		"   ctrl+g to edit in Vim · ~/.claude/plans/plan-creating-a-file.md",
	)...)
}

const (
	planAuto   = "Yes, and use auto mode"
	planBypass = "Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session"
)

// claudeAsk is AskUserQuestion's dialog: a description below each choice,
// the choice that takes typed words, a rule, and the one that chats.
var claudeAsk = screenRows(
	"❯ Use the AskUserQuestion tool to ask me one question: do I prefer red or blue?",
	"",
	solidRule,
	" ☐ Color",
	"",
	"Do you prefer red or blue?",
	"",
	"❯ 1. Red",
	"     Prefer red",
	"  2. Blue",
	"     Prefer blue",
	"  3. Type something.",
	solidRule,
	"  4. Chat about this",
	"",
	"Enter to select · ↑/↓ to navigate · Esc to cancel",
)

// claudeAskReview is the last page of an AskUserQuestion of two
// questions: the answers, and its own list at the bottom with no footer.
var claudeAskReview = screenRows(
	"❯ Ask me two questions with AskUserQuestion: red or blue, then small or large.",
	"",
	solidRule,
	"←  ☒ Color  ☒ Size  ✔ Submit  →",
	"",
	"Review your answers",
	"",
	" ● Do you prefer red or blue?",
	"   → Red",
	" ● Do you prefer small or large?",
	"   → Small",
	"",
	"Ready to submit your answers?",
	"",
	"❯ 1. Submit answers",
	"  2. Cancel",
)

// claudeNarrow is the plan's approval on a screen narrow enough to wrap
// its first choice, the words that make it a change of mode on its second
// row.
var claudeNarrow = screenRows(
	"  "+strings.Repeat("─", 36),
	"   Claude has written up a plan and is",
	"   ready to execute. Would you like to",
	"   proceed?",
	"",
	"   ❯ 1. Yes, and switch to",
	"        BYPASS PERMISSIONS (no further",
	"        prompts) for this session",
	"     2. Yes, manually approve edits",
	"     3. Tell Claude what to change",
	"        shift+tab to approve with this",
	"        feedback",
	"",
	"   ctrl+g to edit in Vim · ~/.claude/plans/plan.md",
)

// claudeIdle is Claude Code at rest after a numbered answer, its prompt
// box and status below; claudeTyped has a numbered line typed in the box.
var (
	claudeIdle = screenRows(
		"● Here is what I would do:",
		"  1. Add the test",
		"  2. Fix the bug",
		"",
		solidRule,
		"❯ ",
		solidRule,
		"  ⏵⏵ auto mode on (shift+tab to cycle)",
	)
	claudeTyped = screenRows(
		"● Done.",
		"",
		solidRule,
		"❯ 1. add the test",
		"  2. fix the bug",
		solidRule,
		"  ? for shortcuts",
	)
	// claudeQuoted has a whole dialog in the transcript, quoted, and
	// claudePasted one pasted into the prompt box: Claude Code's prompt box
	// and status line stand below each.
	claudeQuoted = screenRows(
		"● The dialog you saw:",
		"   Do you want to proceed?",
		"   ❯ 1. Yes",
		"     2. No",
		"   Esc to cancel · Tab to amend",
		"",
		solidRule,
		"❯ ",
		solidRule,
		"  ? for shortcuts",
	)
	claudePasted = screenRows(
		"● Done.",
		"",
		solidRule,
		"❯ why did this show up?",
		"  Do you want to proceed?",
		"  ❯ 1. Yes",
		"    2. No",
		"  Esc to cancel · Tab to amend",
		solidRule,
		"  ? for shortcuts",
	)
	claudeWorking = screenRows(
		"❯ 1. add the test",
		"  2. fix the bug",
		"",
		"✻ Thinking… (esc to interrupt)",
		"",
		solidRule,
		"❯ ",
		solidRule,
		"  ? for shortcuts",
	)
)

// claudePlanNarrow, claudeBashNarrow and claudeAskNarrow are the plan's
// approval, a Bash command's permission and AskUserQuestion as Claude Code
// 2.1.304 drew them 44 columns wide (a phone's terminal): labels, the
// plan's hint and the footers wrapped onto the rows below them, the plan's
// path cut inside a word.
var (
	claudePlanNarrow = screenRows(
		"  "+strings.Repeat("─", 40),
		"   Ready to code?",
		"",
		"   Here is Claude's plan:",
		"  "+strings.Repeat("╌", 40),
		"   Create",
		"   /home/dev/project/hello.txt",
		"   containing the text hi.",
		"  "+strings.Repeat("╌", 40),
		"",
		"  "+strings.Repeat("─", 40),
		"   Claude has written up a plan and is",
		"   ready to execute. Would you like to",
		"   proceed?",
		"",
		"   ❯ 1. Yes, and use auto mode",
		"     2. Yes, manually approve edits",
		"     3. Tell Claude what to change",
		"        shift+tab to approve with this",
		"        feedback",
		"",
		"   ctrl+g to edit in Vim · ~/.claude/pla",
		"   ns/plan-creating-a-file-eager-wind.md",
	)
	claudeBashNarrow = screenRows(
		strings.Repeat("─", 44),
		" Bash command",
		" Tip: auto mode handles these prompts for",
		` you — choose "switch to auto mode" below`,
		" Create empty file probe.txt",
		strings.Repeat("╌", 44),
		" touch probe.txt",
		strings.Repeat("╌", 44),
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, and always allow access to",
		"      /home/dev/project from this",
		"      project",
		"   3. Yes, and switch to auto mode · auto",
		"      mode handles these prompts for you",
		"   4. No",
		"",
		" Esc to cancel · Tab to amend",
	)
	claudeAskNarrow = screenRows(
		strings.Repeat("─", 44),
		" ☐ Color",
		"",
		"Do you prefer red or blue?",
		"",
		"❯ 1. Red",
		"     You prefer red.",
		"  2. Blue",
		"     You prefer blue.",
		"  3. Type something.",
		strings.Repeat("─", 44),
		"  4. Chat about this",
		"",
		"Enter to select · ↑/↓ to navigate · Esc to",
		"cancel",
	)
	// claudeAskField is AskUserQuestion with the highlight taken to its
	// "Type something." by arrows (its field, where every key is typed),
	// and claudeAskFieldTyped with "xy" typed there, as 2.1.304 drew them:
	// the footer offers ctrl+g then.
	claudeAskField = strings.NewReplacer(
		"❯ 1. Red", "  1. Red",
		"  3. Type something.", "❯ 3. Type something.",
		"Enter to select · ↑/↓ to navigate · Esc to cancel", "Enter to select · ↑/↓ to navigate · ctrl+g to edit in Vim · Esc to cancel",
	).Replace(claudeAsk)
	claudeAskFieldTyped = strings.Replace(claudeAskField, "❯ 3. Type something.", "❯ 3. xy", 1)
	// claudePlanTyped is the plan's approval with "abc" typed into its
	// feedback field: the digit 3 took the highlight there.
	claudePlanTyped = strings.Replace(claudePlan(planAuto, 3), "❯ 3. Tell Claude what to change", "❯ 3. abc", 1)
	// claudeReviewAbove is AskUserQuestion's last page with Claude Code's
	// prompt box and status line below it: no dialog of the moment.
	claudeReviewAbove = screenRows(claudeAskReview, "", solidRule, "❯ ", solidRule, "  ? for shortcuts")
)

func labels(choices []Option) []string {
	out := make([]string, len(choices))
	for i, c := range choices {
		out[i] = c.Input + " " + c.Label
	}
	return out
}

const planQuestion = "Claude has written up a plan and is ready to execute. Would you like to proceed?"

func TestReadListReadsClaudeCodesDialogs(t *testing.T) {
	for _, c := range []struct {
		name, screen string
		choices      []string
		words        string
		at           int
	}{
		{"bash", claudeBash,
			[]string{"1 Yes", "2 Yes, and always allow access to /home/dev/project from this project", "3 Yes, and switch to auto mode · auto mode handles these prompts for you", "4 No"},
			"Bash command\nTip: auto mode handles these prompts for you — choose \"switch to auto mode\" below\nCreate empty file probe.txt\ntouch probe.txt\nDo you want to proceed?", 0},
		{"write", claudeWrite,
			[]string{"1 Yes", "2 Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)", "3 No"},
			"Create file\nnotes.txt\n1 hi\nDo you want to create notes.txt?", 0},
		{"plan", claudePlan(planAuto, 1),
			[]string{"1 " + planAuto, "2 Yes, manually approve edits", "3 Tell Claude what to change"}, planQuestion, 0},
		{"plan, bypass allowed", claudePlan(planBypass, 1),
			[]string{"1 " + planBypass, "2 Yes, manually approve edits", "3 Tell Claude what to change"}, planQuestion, 0},
		{"plan, its feedback field highlighted", claudePlan(planAuto, 3),
			[]string{"1 " + planAuto, "2 Yes, manually approve edits", "3 Tell Claude what to change"}, planQuestion, 2},
		{"plan, words typed into its feedback field", claudePlanTyped,
			[]string{"1 " + planAuto, "2 Yes, manually approve edits", "3 abc"}, planQuestion, 2},
		{"ask", claudeAsk,
			[]string{"1 Red", "2 Blue", "3 Type something.", "4 Chat about this"}, "☐ Color\nDo you prefer red or blue?", 0},
		{"ask, its last page", claudeAskReview,
			[]string{"1 Submit answers", "2 Cancel"},
			"←  ☒ Color  ☒ Size  ✔ Submit  →\nReview your answers\n● Do you prefer red or blue?\n→ Red\n● Do you prefer small or large?\n→ Small\nReady to submit your answers?", 0},
		{"plan, narrow", claudeNarrow,
			[]string{"1 Yes, and switch to", "2 Yes, manually approve edits", "3 Tell Claude what to change"},
			"Claude has written up a plan and is\nready to execute. Would you like to\nproceed?", 0},
		{"plan at 44 columns", claudePlanNarrow,
			[]string{"1 " + planAuto, "2 Yes, manually approve edits", "3 Tell Claude what to change"},
			"Claude has written up a plan and is\nready to execute. Would you like to\nproceed?", 0},
		{"bash at 44 columns", claudeBashNarrow,
			[]string{"1 Yes", "2 Yes, and always allow access to", "3 Yes, and switch to auto mode · auto", "4 No"},
			"Bash command\nTip: auto mode handles these prompts for\nyou — choose \"switch to auto mode\" below\nCreate empty file probe.txt\ntouch probe.txt\nDo you want to proceed?", 0},
		{"ask at 44 columns", claudeAskNarrow,
			[]string{"1 Red", "2 Blue", "3 Type something.", "4 Chat about this"}, "☐ Color\nDo you prefer red or blue?", 0},
	} {
		l, ok := readList(c.screen, nil)
		if got := labels(l.choices); !ok || strings.Join(got, "|") != strings.Join(c.choices, "|") {
			t.Errorf("%s: choices %q %v", c.name, got, ok)
		}
		if l.words != c.words {
			t.Errorf("%s: words %q", c.name, l.words)
		}
		if l.at != c.at || len(l.whole) != len(l.choices) {
			t.Errorf("%s: highlight at %d, %d whole", c.name, l.at, len(l.whole))
		}
	}
	// Each choice whole: its label with the rows that follow it, however
	// the screen wrapped them.
	l, _ := readList(claudeBashNarrow, nil)
	if want := []string{"1. Yes", "2. Yes, and always allow access to /home/dev/project from this project", "3. Yes, and switch to auto mode · auto mode handles these prompts for you", "4. No"}; strings.Join(l.whole, "|") != strings.Join(want, "|") {
		t.Fatalf("whole %q", l.whole)
	}
	if l, _ := readList(claudeBash, nil); strings.Join(l.whole, "|") != "1. Yes|2. Yes, and always allow access to /home/dev/project from this project|3. Yes, and switch to auto mode · auto mode handles these prompts for you|4. No" {
		t.Fatalf("whole at 120 columns %q", l.whole)
	}
}

// A list is the dialog's when its rows are numbered 1 to N in turn at one
// column, close together, at the bottom of the screen: a numbered list
// above the dialog is not read into it, a label wrapped at a narrow width
// is its first row, and anything else is no list.
func TestReadListTakesOnlyTheDialogsOwnList(t *testing.T) {
	above := screenRows(
		"● I will:",
		"  1. Add the test",
		"  2. Fix the bug",
		"  3. Run the checks",
		"",
		claudeBash,
	)
	if l, ok := readList(above, nil); !ok || len(l.choices) != 4 || l.choices[0].Label != "Yes" || !strings.HasPrefix(l.words, "Bash command\n") {
		t.Fatalf("a list above the dialog: %q %q", labels(l.choices), l.words)
	}
	narrow := screenRows(
		strings.Repeat("─", 60),
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, and always allow access to /home/dev/project from",
		"      this project",
		"   3. No",
		"",
		" Esc to cancel · Tab to amend",
	)
	if l, _ := readList(narrow, nil); strings.Join(labels(l.choices), "|") != "1 Yes|2 Yes, and always allow access to /home/dev/project from|3 No" {
		t.Fatalf("a wrapped label: %q", labels(l.choices))
	}
	far := []string{" ❯ 1. Yes"}
	for range listGap + 1 {
		far = append(far, "a")
	}
	for name, screen := range map[string]string{
		"no row 1":           screenRows(" ❯ 2. Yes", "   3. No", "", " Esc to cancel"),
		"out of turn":        screenRows(" ❯ 1. Yes", "   3. No", "", " Esc to cancel"),
		"another column":     screenRows(" ❯ 1. Yes", "     2. No", "", " Esc to cancel"),
		"one choice":         screenRows(" ❯ 1. Yes", "", " Esc to cancel"),
		"too far apart":      screenRows(append(far, "   2. No", "", " Esc to cancel")...),
		"too far up":         screenRows(" ❯ 1. Yes", "   2. No", "a", "b", "c", "d", "e", "f", "g", "h", "i"),
		"no list":            screenRows(solidRule, "❯ ", solidRule, "  ? for shortcuts"),
		"nothing":            "",
		"a ten for a choice": screenRows(" ❯ 1. Yes", "  10. No", "", " Esc to cancel"),
	} {
		if l, ok := readList(screen, nil); ok || l.choices != nil || l.words != "" {
			t.Errorf("%s: %q %q", name, labels(l.choices), l.words)
		}
	}
}

// claudeQuestions are Claude Code's built-in questions asked during the
// work, as a session gets them from the catalog: its dialogs, and
// AskUserQuestion's last page (its sign-in screen, a startup question,
// left out).
func claudeQuestions(t *testing.T) []StartupQuestion {
	t.Helper()
	claude, ok := catalog.Default().Get("claude")
	var anytime []catalog.Question
	for _, q := range claude.Questions {
		if q.Anytime {
			anytime = append(anytime, q)
		}
	}
	if !ok || len(anytime) != 2 {
		t.Fatalf("Claude Code's questions: %+v", claude.Questions)
	}
	var out []StartupQuestion
	for _, q := range anytime {
		if !q.Anytime || !q.AnswersFromScreen || len(q.Answers) != 0 {
			t.Fatalf("Claude Code's question %+v", q)
		}
		sq := StartupQuestion{Pattern: regexp.MustCompile(q.Prompt), Anytime: true, FromScreen: true}
		if q.ModeAnswers != "" {
			sq.Mode = regexp.MustCompile(q.ModeAnswers)
		}
		if q.TypedAnswers != "" {
			sq.Typed = regexp.MustCompile(q.TypedAnswers)
		}
		out = append(out, sq)
	}
	return out
}

// readAsked is what the first of qs that text shows reads, as the session
// asks it (Local.askedIn): "" and none for none.
func readAsked(qs []StartupQuestion, text string) (string, []Option, string) {
	for _, q := range qs {
		if words, answers, whole := q.read(text); words != "" {
			return words, answers, whole
		}
	}
	return "", nil, ""
}

// Claude Code's questions find each of its dialogs, with the dialog's own
// choices, the ones that change the permission mode marked (when a narrow
// screen wraps the words that say so too), footers wrapped on a narrow
// screen included; while its highlight is in the plan's feedback field the
// dialog offers no choices. Never a numbered list in the transcript, a
// numbered prompt typed at rest or while it works, a whole dialog quoted
// in the transcript or pasted into the prompt box, or AskUserQuestion's
// last page with the prompt box below it.
func TestClaudeCodesQuestionsAreItsDialogsAlone(t *testing.T) {
	qs := claudeQuestions(t)
	for _, c := range []struct {
		name, screen string
		mode         []bool
	}{
		{"bash", claudeBash, []bool{false, false, true, false}},
		{"write", claudeWrite, []bool{false, true, false}},
		{"plan", claudePlan(planAuto, 1), []bool{true, false, false}},
		{"plan, bypass allowed", claudePlan(planBypass, 1), []bool{true, false, false}},
		{"ask", claudeAsk, []bool{false, false, false, false}},
		{"ask, its last page", claudeAskReview, []bool{false, false}},
		{"plan, narrow: the change of mode on the label's second row", claudeNarrow, []bool{true, false, false}},
		{"plan at 44 columns, its footer wrapped", claudePlanNarrow, []bool{true, false, false}},
		{"bash at 44 columns", claudeBashNarrow, []bool{false, false, true, false}},
		{"ask at 44 columns, its footer wrapped", claudeAskNarrow, []bool{false, false, false, false}},
	} {
		words, answers, whole := readAsked(qs, c.screen)
		if words == "" || len(answers) != len(c.mode) || len(strings.Split(whole, "\n")) != len(c.mode) {
			t.Errorf("%s: %q %+v %q", c.name, words, answers, whole)
			continue
		}
		for i, a := range answers {
			if a.Mode != c.mode[i] {
				t.Errorf("%s: choice %d %+v, mode %v wanted", c.name, i+1, a, c.mode[i])
			}
		}
	}
	for name, screen := range map[string]string{"its feedback field highlighted": claudePlan(planAuto, 3), "words typed into it": claudePlanTyped} {
		if words, answers, whole := readAsked(qs, screen); words != planQuestion || answers != nil || whole != "" {
			t.Errorf("the plan, %s: %q %+v %q", name, words, answers, whole)
		}
	}
	for name, screen := range map[string]string{"its field highlighted": claudeAskField, "words typed into it": claudeAskFieldTyped} {
		if words, answers, whole := readAsked(qs, screen); words != "☐ Color\nDo you prefer red or blue?" || answers != nil || whole != "" {
			t.Errorf("AskUserQuestion, %s: %q %+v %q", name, words, answers, whole)
		}
	}
	// A rule within a choice's rows is one ─ in its whole, whatever the
	// screen's width.
	wide, _ := readList(claudeAsk, nil)
	narrow, _ := readList(claudeAskNarrow, nil)
	if wide.whole[2] != "3. Type something. ─" || narrow.whole[2] != wide.whole[2] {
		t.Errorf("the choice a rule follows: %q, %q", wide.whole[2], narrow.whole[2])
	}
	for name, screen := range map[string]string{"idle": claudeIdle, "typed": claudeTyped, "working": claudeWorking, "quoted": claudeQuoted, "pasted": claudePasted, "last page above the prompt": claudeReviewAbove} {
		if words, answers, _ := readAsked(qs, screen); words != "" || answers != nil {
			t.Errorf("%s: %q %+v", name, words, answers)
		}
	}
}
