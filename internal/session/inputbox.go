package session

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The agent's prompt box, as far as the session can tell whether it holds
// text (#1142). A line typed only into an empty box (Submission.EmptyBox:
// a crew member's prompt, a Brakeman turn's line) is held back only where
// both of these hold:
//
//  1. the box may hold text: something printable went into the agent's
//     input since the last Enter went in (inputBox), whoever wrote it: a
//     person's keys from any viewer, the host's own terminal, a line
//     Conductor typed and did not enter; counted from the session's first
//     write, the startup watch included, so a word typed while the agent
//     loads counts. An Enter written by anyone clears it. The keys of a
//     reply to a question the session shows (a choice's keys, AnswerInput)
//     go to that dialog, not the box: their Enter clears it, their
//     letters do not count;
//  2. the screen does not show the caret at the box's start
//     (caretAtBoxStart): one whole frame, drawn since the last printable
//     write went in (the agent has had output to show it), its cursor shown
//     on a row of the agent's prompt shape whose cells before the cursor
//     are the prompt's mark alone and the spaces after it.
//
// An untouched session never meets the first, so its line is typed as it
// always was whatever its agent does with its cursor: no agent, built-in
// or custom, is made to wait for good by this rule. Claude Code and Codex
// hide the cursor as a frame begins and show it at their input's caret as
// it ends, so an empty box shows the caret just after the mark, a
// placeholder ("Try …", "Ask Codex to do anything") drawn after it or
// not, and a box holding text shows the caret after the text.
//
// Known limits: a person who moved the caret to the start of their own
// text reads as an empty box; a line ended with the agent's own "new line"
// key (a backslash before Enter) and then Enter clears the first though
// text stays; and so does an Enter a dialog took.

// ErrBoxHoldsText refuses a line typed only into an empty prompt box
// (Submission.EmptyBox) while the box may hold text: nothing was written.
// Whoever waits for the box says so and tries again later.
var ErrBoxHoldsText = errors.New("the agent's prompt box holds text: nothing was typed; send it or clear it in its terminal")

// inputBox is what the session knows of the agent's input from what went
// into it: whether something printable went in since the last Enter, and
// the screen's feeds seen as the last of it went in (Local.box).
type inputBox struct {
	mu      sync.Mutex
	mayHold bool
	mark    uint64
	// paste says a bracketed paste has begun and not ended (ESC[200~ …
	// ESC[201~): a carriage return in it is no Enter. Kept from one write
	// to the next, as a paste may be cut between them.
	paste bool
}

// wrote reads data, a write made to the program, as the writer makes it:
// answer says it is a reply's keys to a question the session shows, whose
// letters go to the dialog; fed is how many feeds the screen had seen then.
func (b *inputBox) wrote(data []byte, answer bool, fed uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range inputKeys(data, &b.paste) {
		switch {
		case k == keyEnter:
			b.mayHold = false
		case k == keyPrintable && !answer:
			b.mayHold, b.mark = true, fed
		}
	}
}

// holds reports whether something printable went in since the last Enter,
// and the screen's feeds seen as the last of it went in.
func (b *inputBox) holds() (bool, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mayHold, b.mark
}

// inputKey is what a byte, or a sequence, of a write does to the box.
type inputKey uint8

const (
	keyOther inputKey = iota
	keyPrintable
	keyEnter
)

// inputKeys reads a write as what each of its keys does to the box, in
// order: a printable character (UTF-8 bytes among them), a carriage return
// outside a paste (Enter), a kitty keyboard report of either (CSI 13 u is
// Enter, CSI <code> u with no modifier but shift a character); every other
// control byte and every escape sequence (cursor keys, a terminal's own
// reports, mouse reports, OSC and DCS strings, an Alt key) is neither. A
// legacy mouse report's three bytes are its own. Each write is read from
// its start: only a paste begun (paste) is kept across writes.
func inputKeys(data []byte, paste *bool) []inputKey {
	var keys []inputKey
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == 0x1b:
			n, k := escapeKey(data[i:], paste)
			if k != keyOther {
				keys = append(keys, k)
			}
			i += n
			continue
		case c == '\r':
			if !*paste {
				keys = append(keys, keyEnter)
			}
		case c >= 0x20 && c != 0x7f:
			keys = append(keys, keyPrintable)
		}
		i++
	}
	return keys
}

// escapeKey reads the escape sequence data begins with: its length (the
// rest of data when it is not done) and what it does to the box.
func escapeKey(data []byte, paste *bool) (int, inputKey) {
	if len(data) < 2 {
		return len(data), keyOther
	}
	switch data[1] {
	case '[':
		if len(data) >= 6 && data[2] == 'M' {
			// A legacy mouse report: ESC [ M and three bytes.
			return 6, keyOther
		}
		i := 2
		for i < len(data) && data[i] >= 0x20 && data[i] <= 0x3f {
			i++
		}
		for i < len(data) && data[i] >= 0x20 && data[i] <= 0x2f {
			i++
		}
		if i >= len(data) {
			return len(data), keyOther
		}
		params, final := string(data[2:i]), data[i]
		n := i + 1
		switch {
		case final == '~' && params == "200":
			*paste = true
		case final == '~' && params == "201":
			*paste = false
		case final == 'u':
			return n, kittyKey(params)
		}
		return n, keyOther
	case ']', 'P', 'X', '^', '_':
		// A string, until BEL or ST.
		for i := 2; i < len(data); i++ {
			if data[i] == 0x07 {
				return i + 1, keyOther
			}
			if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
				return i + 2, keyOther
			}
		}
		return len(data), keyOther
	case 'O':
		// SS3: a cursor or function key in application mode.
		return min(3, len(data)), keyOther
	}
	// An Alt key, or ESC CR: the byte after the ESC is part of it.
	return 2, keyOther
}

// kittyKey is what a kitty keyboard report, CSI params u, does to the box:
// its key code 13 with no modifier is Enter; a code of a printable
// character, outside the protocol's functional keys, with no modifier but
// shift, is a character.
func kittyKey(params string) inputKey {
	code, mods, _ := strings.Cut(params, ";")
	code, _, _ = strings.Cut(code, ":")
	mods, _, _ = strings.Cut(mods, ":")
	n, ok := atoiSmall(code)
	if !ok {
		return keyOther
	}
	m := 1
	if mods != "" {
		if m, ok = atoiSmall(mods); !ok {
			return keyOther
		}
	}
	switch {
	case n == 13 && m == 1:
		return keyEnter
	case n >= 0x20 && n != 0x7f && (n < 57344 || n > 63743) && m <= 2:
		return keyPrintable
	}
	return keyOther
}

// atoiSmall is s's value as a decimal number of at most 7 digits.
func atoiSmall(s string) (int, bool) {
	if s == "" || len(s) > 7 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// caretAtBoxStart reports whether the screen shows the caret at the start
// of the agent's prompt box: uncut, a whole frame drawn as the program drew
// it; c, its cursor, shown; the cells before the cursor on its row are the
// prompt's mark alone (spaces before it, one character that is no letter,
// digit or control, and at least one space after it, every Unicode space
// read as a plain one: "❯ " and "› "); and, for an agent with a prompt
// shape, that row is a row of the shape's match on the frame (from the
// first row of the match to its end). A box showing a placeholder after
// the caret is empty by this rule, and so is one showing nothing.
func caretAtBoxStart(frame string, uncut bool, c Caret, shape *regexp.Regexp) bool {
	if !uncut || !c.Shown {
		return false
	}
	before := plainSpaces(c.Before)
	mark := strings.TrimSpace(before)
	if utf8.RuneCountInString(mark) != 1 || !strings.HasSuffix(before, " ") {
		return false
	}
	if r, _ := utf8.DecodeRuneInString(mark); unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsControl(r) {
		return false
	}
	if shape == nil {
		return true
	}
	text := plainSpaces(frame)
	at := shape.FindStringIndex(text)
	if at == nil {
		return false
	}
	from := strings.LastIndexByte(text[:at[0]], '\n') + 1
	row := strings.TrimRight(plainSpaces(c.Row), " ")
	for _, line := range strings.Split(text[from:at[1]], "\n") {
		if strings.HasPrefix(line, row) {
			return true
		}
	}
	return false
}

// boxHoldsText reports whether the agent's prompt box may hold text by the
// rule above: something printable went in since the last Enter, and the
// screen, drawn since it went in, does not show the caret at the box's
// start.
func (s *Local) boxHoldsText() bool {
	holds, mark := s.box.holds()
	if !holds {
		return false
	}
	frame, uncut, c, fed := s.shot.FrameCaret()
	if fed <= mark {
		// Nothing drawn since it went in: the screen is the one before it.
		return true
	}
	return !caretAtBoxStart(frame, uncut, c, s.opts.PromptShape)
}
