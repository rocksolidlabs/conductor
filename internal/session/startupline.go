package session

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// A line while the startup questions' watch lasts (until the first
// submission's Enter): its text is typed only at the agent's prompt.
//
// The rule is one pure function (lineAt) of one reading of the screen the
// questions asked during the work are read from (asked), taken with no
// output fed meanwhile (startupReading: PatternWatcher.Seen under feedMu).
// For an agent with a prompt shape (Options.PromptShape) a line's text is
// typed only where that reading is a whole frame (no synchronized update
// under way, nothing cut by a window made smaller) that shows the agent's
// prompt by its shape, no startup question's pattern and no numbered
// choice, every horizontal space read as a plain one; anything else, what
// is not known among it, refuses the line whole, nothing written, in words
// true to what shows (a startup question's where one shows, what may be a
// question where a numbered choice shows, not at its prompt yet
// otherwise). An agent with no prompt shape is refused where either of the
// two checks it had refused a line's text or held its Enter: a whole frame
// showing a startup question or a numbered choice, or a frame not whole
// while the output's tail the startup watcher keeps saw a startup question
// drawn.
//
// Submit asks it before the text and the text's guard again as the writer
// takes the text. A line that is an Enter alone is judged by it as the
// writer takes the Enter. The Enter of a line with text keeps the guard it
// had (enterGuard, startupMayShow). For an agent with a prompt shape every
// line that names no question, and every reply to a question not read off
// the screen, is also typed only at the agent's prompt for the whole of
// the session, its text and its Enter (promptOnly): the Enter where the
// screen is the prompt, or the prompt holding the line's own text
// (atPromptForEnter), since a long line wraps past the rows the shape
// takes. Nothing of this keeps a state: the rule reads the screen as it
// is.

// promptShown reports whether text, a whole frame, shows the agent's
// prompt by its shape (shape; none for an agent without one) with no
// numbered choice anywhere on it: the one rule atPrompt and a line's share,
// a pure function of the frame. Every horizontal space is read as a plain
// one (plainSpaces), so no indentation turns a choice into text.
func promptShown(text string, shape *regexp.Regexp) bool {
	text = plainSpaces(text)
	if shape != nil && !shape.MatchString(text) {
		return false
	}
	return !startupChoiceShown(text)
}

// markedChoiceRow is a numbered row with a highlight's marker before it
// ("› 1. Trust and continue", "❯ 1. Yes"): a dialog's choice wherever it
// stands on the screen.
var markedChoiceRow = regexp.MustCompile(`^\s*[›>❯]\s*\d+\.\s+\S`)

// promptShownPast is the agent's prompt read on text, a whole frame, once
// the session's startup watch is over (promptRead): for an agent with a
// prompt shape, the shape matches and no numbered row is a dialog's
// choice, which is one in or under the rows the shape matched (from the
// first row of its match to the frame's end) or one with a highlight's
// marker above them, unless own (nil for none) says that marked row is a
// line the session itself entered, shown in the agent's history
// (enteredRow). A numbered list in the agent's own answer above its prompt
// is no choice. With no shape it is promptShown.
func promptShownPast(text string, shape *regexp.Regexp, own func(row string) bool) bool {
	if shape == nil {
		return promptShown(text, nil)
	}
	text = plainSpaces(text)
	at := shape.FindStringIndex(text)
	if at == nil {
		return false
	}
	from := strings.LastIndexByte(text[:at[0]], '\n') + 1
	if startupChoiceShown(text[from:]) {
		return false
	}
	for _, row := range strings.Split(text[:from], "\n") {
		if markedChoiceRow.MatchString(row) && (own == nil || !own(row)) {
			return false
		}
	}
	return true
}

// promptRead reports whether frame, a whole frame, shows the agent's
// prompt: while the startup watch lasts with a numbered row anywhere read
// as a choice (promptShown), after it as promptShownPast reads it, the
// lines the session entered known to it.
func (s *Local) promptRead(frame string) bool {
	if s.trust != nil && s.trust.Watching() {
		return promptShown(frame, s.opts.PromptShape)
	}
	return promptShownPast(frame, s.opts.PromptShape, s.enteredRow)
}

// The lines the session entered that read as a numbered row (enteredRows).
const (
	// maxEnteredRows is how many are kept, the latest.
	maxEnteredRows = 8
	// maxEnteredRow bounds each, in bytes.
	maxEnteredRow = 256
	// minEnteredRow is the fewest characters one has, and the fewest a
	// marked row's text has to be taken for one.
	minEnteredRow = 24
)

// enteredNumbered is a line that reads as a numbered row ("1. add the test").
var enteredNumbered = regexp.MustCompile(`^\s*\d+\.\s+\S`)

// rowMarker is a row's highlight marker and the spaces around it.
var rowMarker = regexp.MustCompile(`^\s*[›>❯]\s*`)

// enteredRows are the first lines of the last lines the session entered
// (their Enter written) that read as a numbered row and are at least
// minEnteredRow characters long, spaces normalised, each cut to
// maxEnteredRow bytes: the agents show a line they were given in their
// history behind the prompt's mark, where it reads as a dialog's marked
// choice. Nothing else of what was typed is kept, and nothing survives
// the session.
type enteredRows struct {
	mu   sync.Mutex
	rows []string
}

// normalRow is text with its runs of spaces one space, trimmed.
func normalRow(text string) string { return strings.Join(strings.Fields(text), " ") }

// rememberEntered keeps line, a line the session typed whose Enter is
// being written, when it reads as a numbered row and is long enough.
func (s *Local) rememberEntered(line string) {
	line = normalRow(plainSpaces(line))
	if utf8.RuneCountInString(line) < minEnteredRow || !enteredNumbered.MatchString(line) {
		return
	}
	if len(line) > maxEnteredRow {
		line = strings.ToValidUTF8(line[:maxEnteredRow], "")
	}
	s.entered.mu.Lock()
	defer s.entered.mu.Unlock()
	rows := append(s.entered.rows, line)
	if len(rows) > maxEnteredRows {
		rows = append([]string(nil), rows[len(rows)-maxEnteredRows:]...)
	}
	s.entered.rows = rows
}

// enteredRow reports whether row, a marked numbered row on the screen, is
// a line the session entered as the agent's history shows it: its text
// after the marker, spaces normalised, at least minEnteredRow characters
// long and a prefix of a line kept (the whole of it, or its first row
// where the screen wrapped it).
func (s *Local) enteredRow(row string) bool {
	text := normalRow(rowMarker.ReplaceAllString(row, ""))
	if utf8.RuneCountInString(text) < minEnteredRow {
		return false
	}
	s.entered.mu.Lock()
	defer s.entered.mu.Unlock()
	for _, r := range s.entered.rows {
		if strings.HasPrefix(r, text) {
			return true
		}
	}
	return false
}

// startupIn reports whether text shows a startup question's pattern, read
// as it is or with every horizontal space a plain one.
func (s *Local) startupIn(text string) bool {
	return s.lastStartup(text) != "" || s.lastStartup(plainSpaces(text)) != ""
}

// lineVerdict is what a line meets on a reading (lineAt).
type lineVerdict int

const (
	// lineGoes: the line may be typed.
	lineGoes lineVerdict = iota
	// lineQuestion: a startup question's pattern shows.
	lineQuestion
	// lineChoice: a numbered choice shows, and no startup question.
	lineChoice
	// lineNotPrompt: not the agent's prompt (half a frame, a frame cut,
	// or not its shape).
	lineNotPrompt
)

// lineAt is the rule on one reading v of the screen the questions are read
// from and tail, the startup watcher's output: a pure function of them and
// the session's catalog.
func (s *Local) lineAt(v Sight, tail string) lineVerdict {
	plain := plainSpaces(v.Text)
	if s.opts.PromptShape == nil {
		if !v.Uncut {
			if s.lastStartup(tail) != "" {
				return lineChoice
			}
			return lineGoes
		}
		if s.startupIn(v.Text) {
			return lineQuestion
		}
		if startupChoiceShown(plain) {
			return lineChoice
		}
		return lineGoes
	}
	switch {
	case !v.Uncut:
		return lineNotPrompt
	case s.startupIn(v.Text):
		return lineQuestion
	case startupChoiceShown(plain):
		return lineChoice
	case !s.opts.PromptShape.MatchString(plain):
		return lineNotPrompt
	}
	return lineGoes
}

// startupReading is one reading of the screen the questions are read
// from, with the startup watcher's output, taken with no output fed
// meanwhile (feedMu). ok is false while the startup questions' watch does
// not last (it has ended, or the session has no startup questions).
func (s *Local) startupReading() (v Sight, tail string, ok bool) {
	if s.trust == nil || !s.trust.Watching() || s.asked == nil {
		return v, "", false
	}
	s.feedMu.Lock()
	v = s.asked.Seen()
	tail = s.trust.Text()
	s.feedMu.Unlock()
	return v, tail, true
}

// startupLine is a line's refusal while the startup questions' watch
// lasts, nil when it may be typed: ErrTrustQuestion where a startup
// question's pattern shows; ErrStartupChoice where a numbered choice shows
// and no startup question; ErrStartupNotAtPrompt where the screen is not
// the agent's prompt. A line typed only at the prompt (onlyAtPrompt) gets
// ErrNotAtPrompt for either of the last two, as atPrompt refuses it.
func (s *Local) startupLine(onlyAtPrompt bool) error {
	v, tail, ok := s.startupReading()
	if !ok {
		return nil
	}
	switch s.lineAt(v, tail) {
	case lineQuestion:
		return ErrTrustQuestion
	case lineChoice:
		if onlyAtPrompt {
			return ErrNotAtPrompt
		}
		return ErrStartupChoice
	case lineNotPrompt:
		if onlyAtPrompt {
			return ErrNotAtPrompt
		}
		return ErrStartupNotAtPrompt
	}
	return nil
}
