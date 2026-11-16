package session

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// listItem matches a row of a dialog's numbered list: what comes before
// its number (the indent, and on the row the dialog would pick on Enter
// the highlight a TUI draws there: Claude Code's ❯, Codex's ›), the
// number, one digit, and the label.
var listItem = regexp.MustCompile(`^([ \t]*(?:([❯›])[ \t]*)?)([1-9])\.[ \t]+(\S.*)$`)

// Bounds of the list readList takes for a dialog's, in rows.
const (
	// listTail: rows after its last choice, blank ones aside (a hint, the
	// footer, each wrapped on a narrow screen).
	listTail = 8
	// listGap: rows between two of its choices (a choice's description, a
	// rule, the rest of a long label: a long folder's path over several
	// rows of a phone's screen).
	listGap = 8
	// listHead: rows of its words, at most.
	listHead = 16
)

// dialogList is a dialog's numbered list as readList reads it.
type dialogList struct {
	// choices are the list's choices: each its label (its first row) and
	// its number, the key that picks it, marked Mode as readList says.
	choices []Option
	// words are the rows above the list up to the dialog's top rule.
	words string
	// whole is each choice whole: its number, its label and the rows that
	// follow it, each trimmed, one space apart, a rule as a single ─ (its
	// length is the screen's width), beyond what a label can show (its
	// bound cuts a long one) and however a narrow screen wrapped it.
	whole []string
	// at is the index of the choice the highlight stands before, -1 for
	// none.
	at int
	// top is the index of the row numbered 1 among text's rows, and rows
	// each choice's row as drawn.
	top  int
	rows []string
}

// readList reads the numbered list a dialog draws at the bottom of text, a
// screen's rows joined by line breaks, as the questions asked during the
// work are matched against (Screen.Text): from the last row numbered N,
// at most listTail rows from the end, up through the rows numbered N-1 to 1
// whose numbers stand at its column, at most listGap rows apart. Each
// choice is its row's label and its number, the key that picks it, with no
// Enter (a digit picks at once in Claude Code's dialogs), marked Mode when
// mode matches it whole: its label with the rows that follow it up to the
// next choice (the last one's up to a blank row), its description, or the
// rest of a label a narrow screen wrapped ("Yes, and switch to", then
// "BYPASS PERMISSIONS …"), which the label shown leaves out. The words are
// the rows above the list up to the dialog's top rule (a row of ─ alone),
// listHead at most, each trimmed, blank rows and rows of rule characters
// left out, joined by line breaks. No list (fewer than two choices, no row
// numbered 1, a number out of its turn at that column, rows too far
// apart): false, and the dialog is not taken for one.
func readList(text string, mode *regexp.Regexp) (dialogList, bool) {
	rows := strings.Split(text, "\n")
	last, tail := len(rows)-1, 0
	for ; last >= 0 && !listItem.MatchString(rows[last]); last-- {
		if strings.TrimSpace(rows[last]) != "" {
			if tail++; tail > listTail {
				return dialogList{}, false
			}
		}
	}
	if last < 0 {
		return dialogList{}, false
	}
	// The rows that follow each choice: the last one's up to a blank row,
	// the others' up to the next choice.
	var after []string
	for i := last + 1; i < len(rows) && strings.TrimSpace(rows[i]) != ""; i++ {
		after = append(after, rows[i])
	}
	l := dialogList{at: -1}
	var marks []bool
	add := func(m []string) {
		slices.Reverse(after)
		label := strings.TrimSpace(m[4])
		whole := label
		for _, row := range after {
			if row = strings.TrimSpace(row); row != "" {
				if rule, _ := ruleRow(row); rule {
					row = "─"
				}
				whole += " " + row
			}
		}
		l.choices = append(l.choices, Option{Label: label, Input: m[3], Mode: mode != nil && mode.MatchString(whole)})
		l.whole = append(l.whole, m[3]+". "+whole)
		l.rows = append(l.rows, m[0])
		marks = append(marks, m[2] != "")
		after = nil
	}
	m := listItem.FindStringSubmatch(rows[last])
	col := utf8.RuneCountInString(m[1])
	slices.Reverse(after) // add reverses the rows back
	add(m)
	want, top := m[3][0]-1, last
	for i := last - 1; i >= 0 && want > '0'; i-- {
		if m := listItem.FindStringSubmatch(rows[i]); m != nil && utf8.RuneCountInString(m[1]) == col {
			if m[3][0] != want {
				return dialogList{}, false
			}
			add(m)
			want, top = want-1, i
			continue
		}
		if after = append(after, rows[i]); len(after) > listGap {
			return dialogList{}, false
		}
	}
	if want != '0' || len(l.choices) < 2 {
		return dialogList{}, false
	}
	slices.Reverse(l.choices)
	slices.Reverse(l.whole)
	slices.Reverse(l.rows)
	slices.Reverse(marks)
	l.at, l.top = slices.Index(marks, true), top
	var above []string
	for i := top - 1; i >= 0 && len(above) < listHead; i-- {
		row := strings.TrimSpace(rows[i])
		rule, solid := ruleRow(row)
		if solid {
			break
		}
		if row != "" && !rule {
			above = append(above, row)
		}
	}
	slices.Reverse(above)
	l.words = strings.Join(above, "\n")
	return l, true
}

// named returns l's choices with each label its name alone, for a list
// whose rows draw each choice's description in a column of its own (a
// form's, StartupQuestion.form: Codex's "› 1. Allow   Run the tool and
// continue", #716). The column is found as a column: the one screen column
// at which, on every choice row, a word begins after two spaces or more,
// past the start of the row's label. Each label is its row from its start
// up to that column, trimmed; the description stays in the choice whole.
// False when the rows draw no such column, or more than one.
func (l dialogList) named() ([]Option, bool) {
	labelAt := func(row string) ([]rune, int) {
		r := []rune(row)
		return r, len(r) - utf8.RuneCountInString(listItem.FindStringSubmatch(row)[4])
	}
	var cols []int
	for i, row := range l.rows {
		r, start := labelAt(row)
		var at []int
		for p := start + 2; p < len(r); p++ {
			if r[p] != ' ' && r[p-1] == ' ' && r[p-2] == ' ' && (i == 0 || slices.Contains(cols, p)) {
				at = append(at, p)
			}
		}
		cols = at
	}
	if len(cols) != 1 {
		return nil, false
	}
	out := slices.Clone(l.choices)
	for i, row := range l.rows {
		r, start := labelAt(row)
		out[i].Label = strings.TrimSpace(string(r[start:cols[0]]))
	}
	return out, true
}

// ruleRow reports whether row (trimmed) is a rule, three or more of the
// characters a TUI draws lines with and nothing else, and whether it is a
// solid one, of ─ alone: the top of a Claude Code dialog, where ╌ marks
// off a command or a file within it.
func ruleRow(row string) (rule, solid bool) {
	if utf8.RuneCountInString(row) < 3 {
		return false, false
	}
	solid = true
	for _, r := range row {
		switch r {
		case '─':
		case '━', '═', '╌', '╍', '┄', '┅', '┈', '┉':
			solid = false
		default:
			return false, false
		}
	}
	return true, solid
}
