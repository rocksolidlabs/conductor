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
// the box visibly begins with what was typed into it.
//
// What was typed (inputBox) is read off every write as the writer makes
// it, from the session's first write (the startup watch included, so a
// word typed while the agent loads counts), by a simple line editor's
// model: a printable character is appended (a paste's text too), Backspace
// or DEL takes the last one, Ctrl-W the last word, Ctrl-U clears, Ctrl-C
// clears, and so does every built-in agent with text in its box. Every
// other key that moves the caret or edits elsewhere (the cursor keys, Home,
// End, Tab, a new line, an Alt key, a mouse press, a paste of more than one
// line), and text past maxBoxRunes, makes the text "edited": the model no
// longer knows it. A person's keys from any viewer count, and so does a
// line Conductor typed. Keys that go to a dialog the session shows (a
// reply's, AnswerInput; and a person's keys in the terminal that the
// session reads as a startup question's answer or a dialog's key) count
// for nothing.
//
// An Enter does not empty the box in the session's eyes: an agent that is
// still loading keeps the text and drops the Enter. The screen decides.
// What was typed is kept until a whole frame drawn after the last key
// shows the agent's prompt with a box that does not begin with it (it was
// sent, or deleted); it is forgotten then. A key typed after an Enter
// makes the text edited: whether the Enter sent what came before is not
// known. An Enter into an empty box changes nothing, and the Enter of a
// line Conductor typed only at the agent's prompt, written only where the
// screen is that prompt, empties the model (inputBox.sent).
//
// The line is held only for an agent with a prompt shape, and only while
// what was typed is not nothing and:
//
//   - no frame has been drawn since the last key went in: the screen is
//     the one before it; or, on a whole frame drawn since that shows the
//     agent's prompt,
//   - the text is not edited, and the box's first row (the prompt mark's
//     row, after the mark and its space) begins with the first characters
//     of it, as many as both have (ErrBoxHoldsText); or
//   - the text is edited, the box's first row holds something after the
//     mark, and the cursor does not show the caret at the box's start
//     (ErrBoxHoldsEditedText): a placeholder the agent draws in an empty
//     box cannot be told from text there, and the caret's place is read
//     only where the agent shows its cursor.
//
// Nothing typed is never held, whatever the agent draws: no agent, built-in
// or custom, waits for good on an untouched session. An agent with no
// prompt shape is never held.
//
// Known limits: a typed text that the agent's own placeholder happens to
// begin with holds until it is sent or deleted; a text edited with the
// caret keys holds until a blank box or the caret at the box's start shows
// (behind a placeholder and a hidden cursor, until it is sent and the box
// is blank); the simple editor's model is not the agent's, so an agent
// that binds these keys otherwise is read wrong.

// maxBoxRunes bounds the text the box's model keeps: past it, edited.
const maxBoxRunes = 256

// ErrBoxHoldsText refuses a line typed only into an empty prompt box
// (Submission.EmptyBox) while the box shows the text a person typed
// there: nothing was written.
var ErrBoxHoldsText = errors.New("the agent's prompt box holds text: nothing was typed; send it or clear it in its terminal")

// ErrBoxHoldsEditedText is ErrBoxHoldsText for text edited with keys the
// session does not follow, which a box that is not blank may still hold:
// a kind of ErrBoxHoldsText.
var ErrBoxHoldsEditedText error = boxEdited{}

type boxEdited struct{}

func (boxEdited) Error() string {
	return "the agent's prompt box may hold text: nothing was typed; send it, or clear it and press Enter, in its terminal"
}

// Is makes ErrBoxHoldsEditedText a kind of ErrBoxHoldsText.
func (boxEdited) Is(target error) bool { return target == ErrBoxHoldsText }

// inputBox is the session's model of what went into the agent's input
// (Local.box), under mu.
type inputBox struct {
	mu sync.Mutex
	// typed is the text, edited that the model no longer knows it, and
	// entered that an Enter went in after it.
	typed   []rune
	edited  bool
	entered bool
	// mark is the screen's feeds seen as the last key that changed the
	// model went in: only a frame fed after it shows what it did.
	mark uint64
	// paste says a bracketed paste has begun and not ended (ESC[200~ …
	// ESC[201~), kept from one write to the next.
	paste bool
}

// boxState is the model at one moment.
type boxState struct {
	typed  string
	edited bool
	mark   uint64
}

// empty reports whether nothing is typed.
func (b boxState) empty() bool { return b.typed == "" && !b.edited }

// state is the model now.
func (b *inputBox) state() boxState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return boxState{typed: string(b.typed), edited: b.edited, mark: b.mark}
}

// forget empties the model, unless a key changed it since the reading
// whose mark is mark.
func (b *inputBox) forget(mark uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mark == mark {
		b.typed, b.edited, b.entered = nil, false, false
	}
}

// sent empties the model as the writer makes the Enter of a line typed at
// the agent's prompt (writeWait.sends), fed the screen's feeds then.
func (b *inputBox) sent(fed uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.typed) > 0 || b.edited {
		b.typed, b.edited, b.entered, b.mark = nil, false, false, fed
	}
}

// wrote reads data, a write made to the program, as the writer makes it.
// dialog says its keys go to a dialog the session shows: they change
// nothing. fed is how many feeds the screen had seen then.
func (b *inputBox) wrote(data []byte, dialog bool, fed uint64) {
	if dialog {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	changed := false
	for _, k := range inputKeys(data, &b.paste) {
		if b.apply(k) {
			changed = true
		}
	}
	if changed {
		b.mark = fed
	}
}

// apply applies k to the model and reports whether it changed anything.
func (b *inputBox) apply(k boxKey) bool {
	empty := len(b.typed) == 0 && !b.edited
	switch k.op {
	case opNone:
		return false
	case opClear:
		if empty {
			return false
		}
		b.typed, b.edited, b.entered = nil, false, false
		return true
	case opClearLine:
		// Ctrl-U: the text before the caret, the whole of it while the
		// caret is at its end.
		if empty {
			return false
		}
		if !b.edited {
			// Sent or not, the box is empty after it.
			b.typed, b.entered = nil, false
		}
		return true
	case opEnter:
		if empty {
			// An Enter into an empty box changes nothing.
			return false
		}
		b.entered = true
		return true
	}
	if b.entered {
		// After an Enter: whether it sent what came before is not known.
		b.edited = true
		return true
	}
	switch k.op {
	case opRune:
		if len(b.typed) >= maxBoxRunes {
			b.edited = true
		} else {
			b.typed = append(b.typed, k.r)
		}
	case opBack:
		if empty {
			return false
		}
		if len(b.typed) > 0 {
			b.typed = b.typed[:len(b.typed)-1]
		}
	case opWord:
		if empty {
			return false
		}
		n := len(b.typed)
		for n > 0 && unicode.IsSpace(b.typed[n-1]) {
			n--
		}
		for n > 0 && !unicode.IsSpace(b.typed[n-1]) {
			n--
		}
		b.typed = b.typed[:n]
	case opOther:
		b.edited = true
	}
	return true
}

// boxOp is what a key does to the box's model.
type boxOp uint8

const (
	opNone      boxOp = iota // nothing (a terminal's report, a pointer's move)
	opRune                   // a character appended
	opBack                   // the last character taken
	opWord                   // the last word taken
	opClearLine              // Ctrl-U
	opClear                  // Ctrl-C
	opEnter                  // a carriage return outside a paste
	opOther                  // a key the model does not follow
)

// boxKey is one key of a write.
type boxKey struct {
	op boxOp
	r  rune
}

// inputKeys reads a write as what each of its keys does to the box, in
// order. Each write is read from its start: only a paste begun (paste) is
// kept across writes. A lone Escape does nothing; a key read with it (an
// Alt key, ESC CR) is one the model does not follow.
func inputKeys(data []byte, paste *bool) []boxKey {
	var keys []boxKey
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == 0x1b:
			n, k := escapeKey(data[i:], paste)
			if k.op != opNone {
				keys = append(keys, k)
			}
			i += n
			continue
		case *paste && c < 0x20:
			// A paste of more than one line, or with a tab: the agent
			// shows it its own way.
			keys = append(keys, boxKey{op: opOther})
		case c == '\r':
			keys = append(keys, boxKey{op: opEnter})
		case c == 0x7f || c == 0x08:
			keys = append(keys, boxKey{op: opBack})
		case c == 0x17:
			keys = append(keys, boxKey{op: opWord})
		case c == 0x15:
			keys = append(keys, boxKey{op: opClearLine})
		case c == 0x03:
			keys = append(keys, boxKey{op: opClear})
		case c < 0x20:
			keys = append(keys, boxKey{op: opOther})
		default:
			r, n := utf8.DecodeRune(data[i:])
			keys = append(keys, boxKey{op: opRune, r: r})
			i += n
			continue
		}
		i++
	}
	return keys
}

// escapeKey reads the escape sequence data begins with: its length (the
// rest of data when it is not done) and what it does to the box.
func escapeKey(data []byte, paste *bool) (int, boxKey) {
	none := boxKey{op: opNone}
	other := boxKey{op: opOther}
	if len(data) < 2 {
		return len(data), none
	}
	switch data[1] {
	case '[':
		if len(data) >= 6 && data[2] == 'M' {
			// A legacy mouse report: ESC [ M and three bytes; a press of
			// one of the three buttons moves the caret.
			if b := int(data[3]) - 32; b >= 0 && b&3 != 3 && b&(32|64) == 0 {
				return 6, other
			}
			return 6, none
		}
		i := 2
		for i < len(data) && data[i] >= 0x20 && data[i] <= 0x3f {
			i++
		}
		if i >= len(data) {
			return len(data), none
		}
		params, final := string(data[2:i]), data[i]
		n := i + 1
		switch {
		case final == '~' && params == "200":
			*paste = true
			return n, none
		case final == '~' && params == "201":
			*paste = false
			return n, none
		case strings.HasPrefix(params, "<"):
			// An SGR mouse report: a press (M) of a button, not a move or
			// the wheel, moves the caret; a release (m) does nothing.
			b, _, _ := strings.Cut(params[1:], ";")
			if v, ok := atoiSmall(b); ok && final == 'M' && v&3 != 3 && v&(32|64) == 0 {
				return n, other
			}
			return n, none
		case final == 'u' && !strings.HasPrefix(params, "?"):
			return n, kittyKey(params)
		case final == 'R' || final == 'c' || final == 'n' || final == 't' || final == 'I' || final == 'O' ||
			final == 'y' || final == 'u':
			// A terminal's own reports: a cursor position, its attributes,
			// a status, a window report, focus, a mode, its keyboard flags.
			return n, none
		}
		return n, other
	case ']', 'P', 'X', '^', '_':
		// A string, until BEL or ST: a terminal's reply.
		for i := 2; i < len(data); i++ {
			if data[i] == 0x07 {
				return i + 1, none
			}
			if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
				return i + 2, none
			}
		}
		return len(data), none
	case 'O':
		// SS3: a cursor or function key in application mode.
		return min(3, len(data)), other
	}
	// An Alt key, or ESC CR: the byte after the ESC is part of it.
	return 2, other
}

// kittyKey is what a kitty keyboard report, CSI params u, does to the box:
// Enter (13), Backspace (127) and a printable character with no modifier
// but shift are those keys; anything else is a key the model does not
// follow.
func kittyKey(params string) boxKey {
	code, mods, _ := strings.Cut(params, ";")
	code, _, _ = strings.Cut(code, ":")
	mods, _, _ = strings.Cut(mods, ":")
	n, ok := atoiSmall(code)
	if !ok {
		return boxKey{op: opOther}
	}
	m := 1
	if mods != "" {
		if m, ok = atoiSmall(mods); !ok {
			return boxKey{op: opOther}
		}
	}
	switch {
	case m > 2:
	case n == 13 && m == 1:
		return boxKey{op: opEnter}
	case n == 127 && m == 1:
		return boxKey{op: opBack}
	case n >= 0x20 && n != 0x7f && (n < 57344 || n > 63743) && utf8.ValidRune(rune(n)):
		return boxKey{op: opRune, r: rune(n)}
	}
	return boxKey{op: opOther}
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

// boxFirstRow is the agent's prompt box's first row on frame, a whole
// frame its shape matches: the first line of the shape's match (from the
// start of the row it begins on) that is not blank and not a rule of box
// drawing (Claude Code's box begins with one), with every Unicode space a
// plain one (the no-break space after Claude Code's mark among them). mark
// is the row's prompt mark, its first character, one that is no letter or
// digit, and after what stands after the mark and its spaces. ok is false
// where no such row is found.
func boxFirstRow(frame string, shape *regexp.Regexp) (row, mark, after string, ok bool) {
	text := plainSpaces(frame)
	at := shape.FindStringIndex(text)
	if at == nil {
		return "", "", "", false
	}
	from := strings.LastIndexByte(text[:at[0]], '\n') + 1
	for _, line := range strings.Split(text[from:at[1]], "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.Trim(trimmed, "─━ ") == "" {
			continue
		}
		r, n := utf8.DecodeRuneInString(trimmed)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return "", "", "", false
		}
		return line, trimmed[:n], strings.TrimRight(strings.TrimLeft(trimmed[n:], " "), " "), true
	}
	return "", "", "", false
}

// boxBeginsWith reports whether after, the box's first row after its
// mark, begins with the first characters of typed, as many as both have,
// typed's leading spaces left out: at least one.
func boxBeginsWith(after, typed string) bool {
	t := []rune(strings.TrimLeft(plainSpaces(typed), " "))
	a := []rune(after)
	n := min(len(t), len(a))
	if n == 0 {
		return false
	}
	return string(t[:n]) == string(a[:n])
}

// caretAtBoxStart reports whether c, the cursor, shows the caret at the
// start of the box whose first row is row (as boxFirstRow reads it) and
// mark its mark: shown, on that row, the cells before it the mark alone
// with spaces around it and at least one after it.
func caretAtBoxStart(row, mark string, c Caret) bool {
	if !c.Shown {
		return false
	}
	caretRow := strings.TrimRight(plainSpaces(c.Row), " ")
	if caretRow == "" || !strings.HasPrefix(row, caretRow) {
		return false
	}
	before := plainSpaces(c.Before)
	return strings.TrimSpace(before) == mark && strings.HasSuffix(before, " ")
}

// boxRefusal is why a line typed only into an empty prompt box is held
// (ErrBoxHoldsText, ErrBoxHoldsEditedText), nil when it may be typed, by
// the rule above. A reading that lets it go, on a whole frame of the
// agent's prompt drawn since the last key, forgets what was typed.
func (s *Local) boxRefusal() error {
	shape := s.opts.PromptShape
	if shape == nil {
		return nil
	}
	st := s.box.state()
	if st.empty() {
		return nil
	}
	held := ErrBoxHoldsText
	if st.edited {
		held = ErrBoxHoldsEditedText
	}
	frame, uncut, c, fed := s.shot.FrameCaret()
	if fed <= st.mark {
		// Nothing drawn since the last key went in: the screen is the one
		// before it.
		return held
	}
	if !uncut || !s.promptRead(frame) {
		// Not the agent's prompt: the line is refused as not at its prompt
		// (promptOnly), and nothing is judged of the box.
		return nil
	}
	row, mark, after, ok := boxFirstRow(frame, shape)
	if ok {
		if !st.edited && boxBeginsWith(after, st.typed) {
			return held
		}
		if st.edited && after != "" && !caretAtBoxStart(row, mark, c) {
			return held
		}
	}
	// The prompt shows a box that does not hold it: sent, or deleted.
	s.box.forget(st.mark)
	return nil
}
