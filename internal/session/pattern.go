package session

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// maxLine bounds the text LineTracker keeps of the current line.
const maxLine = 4096

// escState is where an escape-sequence scanner is in a stream.
type escState uint8

const (
	escText      escState = iota // plain text
	escEsc                       // after ESC
	escInter                     // after ESC and intermediate bytes, until the final byte
	escCSI                       // after ESC [, until a final byte 0x40-0x7E
	escString                    // inside OSC, DCS, SOS, PM or APC, until BEL or ST
	escStringEsc                 // inside a string, after ESC: a backslash makes ST
)

// escScanner tells the text of a terminal stream from its escape sequences,
// one byte at a time, and keeps its place between writes so that a sequence
// cut in two by the PTY reads is still removed whole. A byte that cannot be
// part of the sequence it finds itself in ends it and is read as text; ESC
// always starts a new sequence.
type escScanner struct{ state escState }

// text reports whether b is text; false means it belongs to an escape sequence.
func (e *escScanner) text(b byte) bool {
	switch e.state {
	case escText:
		if b == 0x1b {
			e.state = escEsc
			return false
		}
		return true
	case escEsc:
		switch {
		case b == '[':
			e.state = escCSI
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			e.state = escString
		case b >= 0x20 && b <= 0x2f:
			e.state = escInter
		case b == 0x1b:
			// ESC ESC: the first one is dropped, the second starts over.
		case b >= 0x30 && b <= 0x7e:
			e.state = escText // a two-byte sequence, complete
		default:
			e.state = escText
			return true
		}
		return false
	case escInter:
		switch {
		case b >= 0x20 && b <= 0x2f:
			// Another intermediate byte.
		case b == 0x1b:
			e.state = escEsc
		case b >= 0x30 && b <= 0x7e:
			e.state = escText
		default:
			e.state = escText
			return true
		}
		return false
	case escCSI:
		switch {
		case b == 0x1b:
			e.state = escEsc
		case b >= 0x40 && b <= 0x7e:
			e.state = escText
		}
		return false
	case escString:
		switch b {
		case 0x07:
			e.state = escText
		case 0x1b:
			e.state = escStringEsc
		}
		return false
	default: // escStringEsc
		if b == '\\' {
			e.state = escText
			return false
		}
		// Not ST: the ESC began a new sequence, and b is what follows it.
		e.state = escEsc
		return e.text(b)
	}
}

// StripANSI removes the escape sequences from b: CSI, OSC (ended by BEL or by
// ST), the other string sequences (DCS, SOS, PM, APC), and the ESC sequences
// that are a single byte after ESC or a few bytes with intermediates such as
// ESC ( B. Everything else, control characters included, stays. A sequence
// that is not finished when b ends is dropped. LineTracker does the same as
// the bytes arrive, keeping its place between writes.
func StripANSI(b []byte) []byte {
	var e escScanner
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if e.text(c) {
			out = append(out, c)
		}
	}
	return out
}

// LineTracker keeps the text of the last screen line of a terminal stream: the
// line the cursor is on. Escape sequences are stripped as the bytes arrive
// (also when a sequence is split between two writes) and control characters
// other than the ones below are ignored.
//
//   - \n starts a new, empty line.
//   - \r moves to column 0: the text that follows replaces the line. The erase
//     sequences programs send with it are stripped like the rest, so a rewrite
//     is taken to be the whole line, not laid over the old one.
//   - \b deletes the last character.
//
// Only the last 4096 bytes of a longer line are kept, the end of it being where
// a prompt is.
type LineTracker struct {
	esc  escScanner
	line []byte // grows to 2*maxLine, then keeps its last maxLine bytes
}

// Write adds a chunk of the stream.
func (t *LineTracker) Write(chunk []byte) {
	for _, b := range chunk {
		if !t.esc.text(b) {
			continue
		}
		switch {
		case b == '\n' || b == '\r':
			t.line = t.line[:0]
		case b == '\b':
			t.backspace()
		case (b < 0x20 && b != '\t') || b == 0x7f:
			// BEL, NUL and the other control characters are not text.
		default:
			t.push(b)
		}
	}
}

// push appends b. When the buffer is full it keeps its last maxLine bytes, so
// the cost of a very long line stays constant per byte.
func (t *LineTracker) push(b byte) {
	if len(t.line) >= 2*maxLine {
		n := copy(t.line, t.line[len(t.line)-maxLine:])
		t.line = t.line[:n]
	}
	t.line = append(t.line, b)
}

// backspace deletes the last character, all the bytes of it.
func (t *LineTracker) backspace() {
	n := len(t.line)
	if n == 0 {
		return
	}
	n--
	for n > 0 && t.line[n]&0xc0 == 0x80 { // a UTF-8 continuation byte
		n--
	}
	t.line = t.line[:n]
}

// Reset forgets the line, not the place in an escape sequence.
func (t *LineTracker) Reset() { t.line = t.line[:0] }

// Last returns the text of the last line, at most 4096 bytes of its end.
func (t *LineTracker) Last() string {
	line := t.line
	if len(line) > maxLine {
		line = line[len(line)-maxLine:]
	}
	return string(line)
}

// maxScreenTail bounds the text ScreenTail keeps.
const maxScreenTail = 1024

// ScreenTail keeps the end of a terminal stream's text for a pattern that
// spans what a TUI draws with cursor moves between its words (a dialog drawn
// cell by cell): each escape sequence and each control character reads as one
// space, a run of white space as one space, and only the last 1024 bytes are
// kept. It keeps its place in a sequence split between two writes.
type ScreenTail struct {
	esc escScanner
	buf []byte // grows to 2*maxScreenTail, then keeps its last maxScreenTail bytes
}

// Write adds a chunk of the stream.
func (t *ScreenTail) Write(chunk []byte) {
	for _, b := range chunk {
		if !t.esc.text(b) || b <= ' ' || b == 0x7f {
			if n := len(t.buf); n > 0 && t.buf[n-1] != ' ' {
				t.push(' ')
			}
			continue
		}
		t.push(b)
	}
}

func (t *ScreenTail) push(b byte) {
	if len(t.buf) >= 2*maxScreenTail {
		n := copy(t.buf, t.buf[len(t.buf)-maxScreenTail:])
		t.buf = t.buf[:n]
	}
	t.buf = append(t.buf, b)
}

// Last returns the text kept, at most its last 1024 bytes.
func (t *ScreenTail) Last() string {
	b := t.buf
	if len(b) > maxScreenTail {
		b = b[len(b)-maxScreenTail:]
	}
	return string(b)
}

// Reset forgets the text kept, not the place in an escape sequence.
func (t *ScreenTail) Reset() { t.buf = t.buf[:0] }

// textTracker is what a PatternWatcher matches: the last line (LineTracker)
// or the end of the screen's text (ScreenTail).
type textTracker interface {
	Write(chunk []byte)
	Last() string
	Reset()
}

// PatternWatcher follows a terminal stream and calls fire with the last line
// once the stream has been quiet for the given time and the line matches. It
// is for agents that neither run hooks nor ring the bell: what they show when
// they wait for a person is all there is to go by.
//
// One quiet period gives at most one fire, and only new output starts another
// period. Output that never stops, a spinner for instance, never reaches the
// end of one. A line that is empty or only white space is never a prompt,
// whatever the pattern says.
type PatternWatcher struct {
	re    *regexp.Regexp
	quiet time.Duration
	fire  func(line string)
	// miss, when set, runs as fire does for a quiet period whose text does
	// not match (NewScreenModelWatcher).
	miss func(text string)

	// fireMu is held while fire runs; Stop takes it to wait for a fire in
	// flight. It is always taken before mu.
	fireMu sync.Mutex

	mu      sync.Mutex // guards the rest
	tracker textTracker
	timer   *time.Timer
	last    time.Time // of the last Feed
	feeds   uint64    // Feeds so far
	checked uint64    // the value of feeds at the last check of the line
	// looked is the value of feeds at the text the look in progress (or
	// the last) took, for a fire or a miss to tell which screen it saw
	// (LookedAt): a callback runs after the text was taken, and the screen
	// may have moved on meanwhile.
	looked uint64
	// lookSettled and lookCols are the look's own, taken with its text
	// under the lock (take): whether the text was a whole frame
	// (Screen.Settled) and the screen's width then. A callback decides on
	// them (Look), not on the screen as it is when it runs: output since
	// the look (a frame's end, a resize) must not change what the look
	// decides.
	lookSettled bool
	lookCols    int
	// afterLook, when set, runs once the timer's look has taken its text
	// and before its callback: a test's, to feed the watcher between the
	// two.
	afterLook func()
	stopped   bool
	// held is Hold's: CheckNow finds nothing until output comes.
	held bool
}

// NewPatternWatcher returns a watcher for re. fire runs on the timer's
// goroutine, one call at a time. It may call Feed; it must not call Stop, which
// waits for it.
func NewPatternWatcher(re *regexp.Regexp, quiet time.Duration, fire func(line string)) *PatternWatcher {
	return &PatternWatcher{re: re, quiet: quiet, fire: fire, tracker: &LineTracker{}}
}

// NewScreenWatcher is NewPatternWatcher matching the end of the screen's text
// (ScreenTail) rather than its last line: fire gets that text.
func NewScreenWatcher(re *regexp.Regexp, quiet time.Duration, fire func(text string)) *PatternWatcher {
	return &PatternWatcher{re: re, quiet: quiet, fire: fire, tracker: &ScreenTail{}}
}

// NewScreenModelWatcher is NewPatternWatcher matching the whole screen as
// the program drew it (Screen, cols by rows): fire gets its text when it
// matches, miss when it does not. Its Reset changes nothing, and Resize
// follows the terminal's size.
func NewScreenModelWatcher(re *regexp.Regexp, quiet time.Duration, cols, rows int, fire, miss func(text string)) *PatternWatcher {
	return &PatternWatcher{re: re, quiet: quiet, fire: fire, miss: miss, tracker: NewScreen(cols, rows)}
}

// Resize gives a screen watcher's screen the terminal's new size; nothing
// for the others.
func (w *PatternWatcher) Resize(cols, rows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sc, ok := w.tracker.(*Screen); ok {
		sc.Resize(cols, rows)
	}
}

// Poke starts a quiet period without output: the text is looked at once the
// stream has been quiet for the watcher's time from now, as after a Feed.
func (w *PatternWatcher) Poke() { w.Feed(nil) }

// CheckNow looks at the text now, without waiting for the stream to go
// quiet, fires when it matches and reports whether it did; it never calls
// miss. The quiet period under way is left as it is: it still looks once
// the stream is quiet, and calls miss then if nothing matches. After Hold,
// until output comes, it finds nothing.
func (w *PatternWatcher) CheckNow() bool {
	w.fireMu.Lock()
	defer w.fireMu.Unlock()
	w.mu.Lock()
	if w.stopped || w.held {
		w.mu.Unlock()
		return false
	}
	line := w.take()
	w.mu.Unlock()
	if strings.TrimSpace(line) != "" && w.re.MatchString(line) {
		w.fire(line)
		return true
	}
	return false
}

// take is a look's capture, under the lock: the text, and with it what
// the look's callback decides on (looked, lookSettled, lookCols: Look).
func (w *PatternWatcher) take() string {
	w.looked = w.feeds
	w.lookSettled, w.lookCols = true, 0
	if sc, ok := w.tracker.(*Screen); ok {
		w.lookSettled = sc.Settled()
		w.lookCols, _ = sc.Size()
	}
	return w.tracker.Last()
}

// LookedAt returns how many Feeds the text the look in progress took had
// seen: called from a fire or a miss, the screen it saw, which later Feeds
// (Fed) have moved on from. Looks are made one at a time.
func (w *PatternWatcher) LookedAt() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.looked
}

// Look is LookedAt with what the look took with its text: whether the
// text was a whole frame (a screen's Settled; always for the others) and
// the screen's width then (0 for the others). A fire or a miss reads
// these, not Settled and Size, which say what the screen is now.
func (w *PatternWatcher) Look() (looked uint64, settled bool, cols int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.looked, w.lookSettled, w.lookCols
}

// Watching reports whether the watcher still watches (not Stopped).
func (w *PatternWatcher) Watching() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.stopped
}

// Fed returns how many Feeds have come so far.
func (w *PatternWatcher) Fed() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.feeds
}

// Hold makes CheckNow find nothing until the stream brings output or the
// end of a quiet period has looked at the text (Held reports it): the text
// is what was just answered, and the program has not drawn what follows the
// answer yet. Neither is a sign that the answer took: a caller about to
// write waits until Held is false. The looks at the end of a quiet period
// are not held: a text still there then is what the program shows.
func (w *PatternWatcher) Hold() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.held = true
}

// Held reports whether a Hold still waits for output or a quiet look.
func (w *PatternWatcher) Held() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.held && !w.stopped
}

// Text returns the text the watcher matches now (its tracker's Last).
func (w *PatternWatcher) Text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tracker.Last()
}

// TextAt is Text with the screen's width it was read at (Size), taken
// together; 0 for a watcher with no screen.
func (w *PatternWatcher) TextAt() (text string, cols int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sc, ok := w.tracker.(*Screen); ok {
		cols, _ = sc.Size()
	}
	return w.tracker.Last(), cols
}

// TextSettled is TextAt with whether the screen is a whole frame
// (Screen.Settled; always for a watcher with no screen), taken together.
func (w *PatternWatcher) TextSettled() (text string, settled bool, cols int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	settled = true
	if sc, ok := w.tracker.(*Screen); ok {
		cols, _ = sc.Size()
		settled = sc.Settled()
	}
	return w.tracker.Last(), settled, cols
}

// Whole returns all the text the watcher keeps: a screen watcher's whole
// screen (Screen.Lines, every row, a row autowrap went on from read with
// the next as one line, where Text is its last rows), the others'
// Text; whether that is a whole frame, as a terminal shows it (a screen's
// Settled; always for the others); and a screen's size (0 by 0 for the
// others). What tells that something is no longer on the screen reads
// this, and decides nothing on half a frame.
func (w *PatternWatcher) Whole() (text string, settled bool, cols, rows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sc, ok := w.tracker.(*Screen); ok {
		cols, rows = sc.Size()
		return sc.Lines(), sc.Settled(), cols, rows
	}
	return w.tracker.Last(), true, 0, 0
}

// DrawnAt reports whether a screen watcher's screen shows everything the
// program drew as it drew it, and the size it was laid out for
// (Screen.DrawnAt); true and 0 by 0 for the others.
func (w *PatternWatcher) DrawnAt() (uncut bool, cols, rows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sc, ok := w.tracker.(*Screen); ok {
		return sc.DrawnAt()
	}
	return true, 0, 0
}

// Frame is the screen as one reading, under one lock: its text (Lines, a row
// autowrap went on from read with the next as one line), whether that is a
// whole frame (Settled: no synchronized update under way, nothing clipped),
// whether it shows everything the program drew as it drew it (DrawnAt), and
// its size. Two readings (Whole, then DrawnAt) can pair one frame's text with
// another's state when a redraw or a resize comes between them: what decides
// on a dialog's visibility reads this. The others' Last, whole and uncut,
// 0 by 0.
func (w *PatternWatcher) Frame() (text string, settled, uncut bool, cols, rows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sc, ok := w.tracker.(*Screen); ok {
		cols, rows = sc.Size()
		uncut, _, _ = sc.DrawnAt()
		return sc.Lines(), sc.Settled(), uncut, cols, rows
	}
	return w.tracker.Last(), true, true, 0, 0
}

// Rows returns a screen watcher's screen as its rows (Screen.Rows) and
// whether the program has finished drawing them (Screen.Drawn: no
// synchronized update under way; a resize the program has not redrawn
// for does not count, the rows being compared with themselves); nil and
// false for the others.
func (w *PatternWatcher) Rows() (rows []string, drawn bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sc, ok := w.tracker.(*Screen); ok {
		return sc.Rows(), sc.Drawn()
	}
	return nil, false
}

// Size returns a screen watcher's screen size, 0 by 0 for the others.
func (w *PatternWatcher) Size() (cols, rows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sc, ok := w.tracker.(*Screen); ok {
		return sc.Size()
	}
	return 0, 0
}

// Feed adds a chunk of the stream and restarts the quiet period. It never
// waits for a fire.
func (w *PatternWatcher) Feed(chunk []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.tracker.Write(chunk)
	if len(chunk) > 0 {
		w.held = false
	}
	w.last = time.Now()
	w.feeds++
	if w.timer == nil {
		w.timer = time.AfterFunc(w.quiet, w.expire)
	} else {
		w.timer.Reset(w.quiet)
	}
}

// expire is the timer's function: the stream has been quiet since w.last,
// unless a Feed has come in between the timer going off and the lock being
// taken, in which case the Feed has armed the timer again. A fire that runs
// longer than the quiet period leaves timers going off behind it, and they all
// find the stream quiet since the same Feed; the first of them takes the quiet
// period, the others find it taken.
func (w *PatternWatcher) expire() {
	w.fireMu.Lock()
	defer w.fireMu.Unlock()
	w.mu.Lock()
	if w.stopped || w.checked == w.feeds || time.Since(w.last) < w.quiet {
		w.mu.Unlock()
		return
	}
	w.checked = w.feeds
	w.held = false // the screen after the answer has been looked at
	line := w.take()
	hook := w.afterLook
	w.mu.Unlock()
	if hook != nil {
		hook()
	}
	if strings.TrimSpace(line) != "" && w.re.MatchString(line) {
		w.fire(line)
	} else if w.miss != nil {
		w.miss(line)
	}
}

// Reset forgets the text fed so far: the next fire needs a match in what is
// fed from now on.
func (w *PatternWatcher) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tracker.Reset()
	w.checked = w.feeds
}

// Stop ends the watching. When it returns no fire is running and none will
// start, so what a caller does next cannot be followed by a fire that was
// already on its way. It may be called more than once, but not from fire, which
// it waits for.
func (w *PatternWatcher) Stop() {
	w.mu.Lock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
	// Taking the lock is the wait for a fire in flight; there is nothing to
	// protect once it is ours.
	w.fireMu.Lock()
	//lint:ignore SA2001 the empty critical section is the wait
	w.fireMu.Unlock()
}
