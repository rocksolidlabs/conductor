package session

import (
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Bounds of a Screen: every size a terminal of a session can have
// (proto.MaxTerminalDimension each way), so that the model's rows are the
// PTY's at any size: a smaller model would put a dialog drawn at the
// bottom of a taller window on rows it does not have.
const (
	maxScreenCols = proto.MaxTerminalDimension
	maxScreenRows = proto.MaxTerminalDimension
	// maxScreenParams bounds the parameter bytes of one control sequence.
	maxScreenParams = 64
)

// wideTail marks the second cell of a wide character.
const wideTail rune = -1

// screenState is where a Screen's parser is in the stream.
type screenState uint8

const (
	scrGround    screenState = iota
	scrEsc                   // after ESC
	scrEscInter              // after ESC and intermediate bytes, until the final byte
	scrCSI                   // after ESC [, until a final byte
	scrString                // inside OSC, DCS, SOS, PM or APC, until BEL or ST
	scrStringEsc             // inside a string, after ESC
)

// Screen is a terminal's screen as a program draws it, kept from its output:
// the characters of each cell, the cursor, the scroll region and the
// alternate screen. The watcher for the questions an agent asks during its
// work matches it (Options.Questions marked Anytime): a dialog that a TUI
// redraws cell by cell reads whole only on a screen (ratatui writes only the
// cells that changed, so the dialog that follows an answered one sends only
// what differs), and a dialog the program has taken away is no longer on it,
// however few bytes erased it.
//
// It knows what TUIs draw with: cursor moves (CUP, HVP, CUU, CUD, CUF, CUB,
// CNL, CPL, CHA, HPA, VPA, CR, LF, BS, HT, RI, IND, NEL, save and restore),
// erasing (ED, EL, ECH), inserting and deleting (IL, DL, ICH, DCH),
// scrolling (DECSTBM, SU, SD), autowrap, the alternate screen (1049, 1047,
// 47) and a synchronized update under way (2026, Settled). Colours, other
// modes and every other sequence are skipped, as are the
// strings of OSC, DCS and the rest. A character's width is an approximation
// (wide East Asian characters and most emoji take two cells, combining marks
// none): a program that positions its cells, as a TUI does, puts the next
// ones right whatever the guess. A sequence or a character cut between two
// writes is kept whole.
type Screen struct {
	rows, cols int
	main, alt  [][]rune // alt is nil until the program first switches to it
	cells      [][]rune // main or alt
	altOn      bool
	x, y       int
	top, bot   int  // the scroll region's rows, inclusive
	wrap       bool // the last column was written: the next character goes to the next row
	saved      [2]int
	savedMain  [2]int // the cursor on the main screen as 1049 left it
	// syncing says the program has begun a synchronized update (2026) and
	// not ended it: a terminal shows the frame before it until it ends,
	// and this screen holds half of the next.
	syncing bool
	// layMain and layAlt are what each buffer's rows were laid out for
	// (layout), lay the current buffer's (main's or alt's, as cells is):
	// what one buffer draws or erases says nothing of the other.
	layMain, layAlt layout
	lay             *layout

	// softMain, softAlt and soft (main's or alt's, as cells is) mark the
	// rows whose text goes on in the row below: autowrap took the cursor
	// past the row's end; wrapMain, wrapAlt and wrapAt the width such a
	// row had then, its extent (grown since, its cells beyond are the
	// growth's blanks). An erase to a row's end ends that. Lines reads
	// such rows as one line, as the program wrote them.
	softMain, softAlt, soft   []bool
	wrapMain, wrapAlt, wrapAt []int

	st      screenState
	params  []byte
	long    bool // the sequence had more parameter bytes than kept: it is dropped
	utf     [utf8.UTFMax]byte
	utfN    int
	utfWant int
}

// NewScreen returns a blank screen of cols columns and rows rows (80 by 24
// when either is zero), bounded by maxScreenCols and maxScreenRows.
func NewScreen(cols, rows int) *Screen {
	s := &Screen{}
	s.lay = &s.layMain
	s.Resize(cols, rows)
	return s
}

// layout is what a buffer's rows were laid out for. clipped says the
// buffer was resized with something on it, and the program has not erased
// it all since (ED 2, or ED 0 from its first cell; in a synchronized
// update, once that ends: unclipAtEnd): what it shows was drawn at another
// size (cut, or laid out narrower), and what it lacks is not gone; a frame
// that erases nothing, or only some rows, leaves it so. cut says it was
// made narrower or shorter with a character beyond the new size lost
// (loses), and not erased whole since: what it shows is cut short (a
// resize that cuts only blank cells loses nothing). drawnCols and
// drawnRows are the size it had when what it shows was last laid out (the
// last erase of the whole buffer, or the resize of an empty one): grown
// since, it still shows everything drawn then, laid out for that size.
type layout struct {
	clipped, unclipAtEnd bool
	cut                  bool
	drawnCols, drawnRows int
}

// resized notes the buffer's rows (old) made cols by rows from oldCols by
// oldRows, rows leaving at the top by shift (Resize), with a frame under
// way on it (syncing) or not.
func (l *layout) resized(syncing bool, old [][]rune, shift, oldCols, oldRows, cols, rows int) {
	if syncing || hasContent(old) {
		l.clipped = true
		if syncing && (cols < oldCols || rows < oldRows) || loses(old, shift, cols, rows) {
			// A character lost, or a frame under way made smaller (what
			// it draws cannot be told): cut.
			l.cut = true
		}
	} else {
		// Nothing on it: what comes is laid out for the new size.
		l.drawnCols, l.drawnRows = cols, rows
	}
	// An erase before the resize settles nothing after it.
	l.unclipAtEnd = false
}

// laidOut notes the buffer erased whole and drawn again at cols by rows:
// nothing of what it shows was drawn at another size or cut.
func (l *layout) laidOut(cols, rows int) {
	l.clipped, l.cut = false, false
	l.drawnCols, l.drawnRows = cols, rows
}

// hasContent reports whether rows hold a character.
func hasContent(rows [][]rune) bool {
	for _, row := range rows {
		for _, c := range row {
			if c != ' ' && c != wideTail {
				return true
			}
		}
	}
	return false
}

// loses reports whether making rows (old) cols wide by rows tall, rows
// leaving at the top by shift, loses a character: one in a row that
// leaves, one beyond the new width, or a wide character cut in half.
// Blank cells count for nothing.
func loses(old [][]rune, shift, cols, rows int) bool {
	for r, row := range old {
		from := 0
		if kept := r >= shift && r-shift < rows; kept {
			from = cols
			if cols < len(row) && row[cols] == wideTail {
				return true
			}
		}
		for c := from; c < len(row); c++ {
			if row[c] != ' ' && row[c] != wideTail {
				return true
			}
		}
	}
	return false
}

// fitRows is per-row marks (old) for rows rows, rows leaving at the top by
// shift, as Resize fits the cells.
func fitRows[T any](old []T, shift, rows int) []T {
	out := make([]T, rows)
	for r := range out {
		src := r + shift
		if src >= len(old) {
			break
		}
		out[r] = old[src]
	}
	return out
}

func blankRows(rows, cols int) [][]rune {
	out := make([][]rune, rows)
	for i := range out {
		out[i] = blankRow(cols)
	}
	return out
}

func blankRow(cols int) []rune {
	r := make([]rune, cols)
	for i := range r {
		r[i] = ' '
	}
	return r
}

// Resize makes the screen cols by rows, as a terminal does when its window
// changes: what fits stays where it was, except that rows leave at the top
// when the cursor would fall off the bottom. The scroll region becomes the
// whole screen.
func (s *Screen) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	cols, rows = min(cols, maxScreenCols), min(rows, maxScreenRows)
	// Rows leave at the top when the cursor would fall off the bottom.
	shift := max(0, s.y-rows+1)
	mainShift, altShift := shift, 0
	if s.altOn {
		mainShift, altShift = 0, shift
	}
	if cols != s.cols || rows != s.rows {
		// Each buffer's layout, for itself: what it shows, or the frame
		// under way on it, was drawn at another size until the program
		// erases it all and draws it again (Settled, DrawnAt).
		s.layMain.resized(!s.altOn && s.syncing, s.main, mainShift, s.cols, s.rows, cols, rows)
		if s.alt != nil {
			s.layAlt.resized(s.altOn && s.syncing, s.alt, altShift, s.cols, s.rows, cols, rows)
		}
	}
	fit := func(old [][]rune, shift int) [][]rune {
		out := blankRows(rows, cols)
		for r := range out {
			src := r + shift
			if src >= len(old) {
				break
			}
			if n := copy(out[r], old[src]); n == cols && n < len(old[src]) && old[src][n] == wideTail {
				out[r][n-1] = ' ' // a wide character cut in half
			}
		}
		return out
	}
	// The rows autowrap went on from keep that mark and their extent:
	// grown, the screen shows them as they were laid out, and Lines reads
	// them joined to their extent; made smaller, what they held is cut
	// (cut), and the mark tells nothing a reader trusts.
	s.main = fit(s.main, mainShift)
	s.softMain, s.wrapMain = fitRows(s.softMain, mainShift, rows), fitRows(s.wrapMain, mainShift, rows)
	if s.alt != nil {
		s.alt = fit(s.alt, altShift)
		s.softAlt, s.wrapAlt = fitRows(s.softAlt, altShift, rows), fitRows(s.wrapAlt, altShift, rows)
	}
	if s.altOn {
		s.cells, s.soft, s.wrapAt = s.alt, s.softAlt, s.wrapAlt
	} else {
		s.cells, s.soft, s.wrapAt = s.main, s.softMain, s.wrapMain
	}
	s.rows, s.cols = rows, cols
	s.y = min(s.y-shift, rows-1)
	s.x = min(s.x, cols-1)
	s.top, s.bot = 0, rows-1
	s.wrap = false
}

// Write applies a chunk of the program's output.
func (s *Screen) Write(chunk []byte) {
	for _, b := range chunk {
		s.byte(b)
	}
}

func (s *Screen) byte(b byte) {
	switch s.st {
	case scrGround:
		if s.utfWant > 0 {
			if b&0xc0 == 0x80 {
				s.utf[s.utfN] = b
				s.utfN++
				if s.utfN == s.utfWant {
					r, _ := utf8.DecodeRune(s.utf[:s.utfN])
					s.utfWant = 0
					s.put(r)
				}
				return
			}
			// A character cut short: it stands for one that could not be read.
			s.utfWant = 0
			s.put(utf8.RuneError)
		}
		switch {
		case b == 0x1b:
			s.st = scrEsc
		case b < 0x20 || b == 0x7f:
			s.control(b)
		case b < 0x80:
			s.put(rune(b))
		case b >= 0xc0 && b < 0xf8:
			s.utf[0], s.utfN = b, 1
			switch {
			case b >= 0xf0:
				s.utfWant = 4
			case b >= 0xe0:
				s.utfWant = 3
			default:
				s.utfWant = 2
			}
		}
	case scrEsc:
		s.st = scrGround
		switch {
		case b == '[':
			s.st, s.params, s.long = scrCSI, s.params[:0], false
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			s.st = scrString
		case b == '7':
			s.saved = [2]int{s.x, s.y}
		case b == '8':
			s.x, s.y, s.wrap = s.saved[0], s.saved[1], false
			s.clampCursor()
		case b == 'M':
			s.reverseIndex()
		case b == 'D':
			s.index()
		case b == 'E':
			s.x = 0
			s.index()
		case b == 'c':
			s.reset()
		case b == 0x1b:
			s.st = scrEsc
		case b >= 0x20 && b <= 0x2f:
			s.st = scrEscInter
		}
	case scrEscInter:
		switch {
		case b >= 0x20 && b <= 0x2f:
		case b == 0x1b:
			s.st = scrEsc
		default:
			s.st = scrGround
		}
	case scrCSI:
		switch {
		case b >= 0x40 && b <= 0x7e:
			s.st = scrGround
			if !s.long {
				s.csi(b)
			}
		case b >= 0x20 && b <= 0x3f:
			if len(s.params) < maxScreenParams {
				s.params = append(s.params, b)
			} else {
				s.long = true
			}
		case b == 0x1b:
			s.st = scrEsc
		case b < 0x20:
			s.control(b) // a C0 control inside a sequence acts at once
		default:
			s.st = scrGround
		}
	case scrString:
		switch b {
		case 0x07:
			s.st = scrGround
		case 0x1b:
			s.st = scrStringEsc
		}
	default: // scrStringEsc
		if b == '\\' {
			s.st = scrGround
			return
		}
		s.st = scrEsc
		s.byte(b)
	}
}

func (s *Screen) control(b byte) {
	switch b {
	case '\r':
		s.x, s.wrap = 0, false
	case '\n', '\v', '\f':
		s.index()
	case '\b':
		if s.x > 0 {
			s.x--
		}
		s.wrap = false
	case '\t':
		s.x = min(s.cols-1, (s.x/8+1)*8)
		s.wrap = false
	}
}

// put writes a character at the cursor and moves it on.
func (s *Screen) put(r rune) {
	w := runeWidth(r)
	if w == 0 {
		return
	}
	if s.wrap {
		s.soft[s.y], s.wrapAt[s.y] = true, s.cols
		s.x, s.wrap = 0, false
		s.index()
	}
	if s.x+w > s.cols {
		if w > s.cols {
			return
		}
		s.soft[s.y], s.wrapAt[s.y] = true, s.cols
		s.x = 0
		s.index()
	}
	if s.soft[s.y] && s.x >= s.wrapAt[s.y] && r != ' ' {
		// A character landing at or past the width the row wrapped at,
		// without wrapping (the screen grew since): the row is being
		// rewritten in place, and goes on with the next no more (#225). A
		// blank there repaints the growth's padding and changes nothing
		// shown: the row goes on as it did.
		s.soft[s.y], s.wrapAt[s.y] = false, 0
	}
	row := s.cells[s.y]
	if row[s.x] == wideTail && s.x > 0 {
		row[s.x-1] = ' '
	}
	row[s.x] = r
	if w == 2 {
		row[s.x+1] = wideTail
	} else if s.x+1 < s.cols && row[s.x+1] == wideTail {
		row[s.x+1] = ' '
	}
	if s.x+w >= s.cols {
		s.x, s.wrap = s.cols-1, true
	} else {
		s.x += w
	}
}

// index moves the cursor down a row, scrolling the region at its bottom.
func (s *Screen) index() {
	s.wrap = false
	switch {
	case s.y == s.bot:
		s.scrollUp(s.top, s.bot, 1)
	case s.y < s.rows-1:
		s.y++
	}
}

// reverseIndex moves the cursor up a row, scrolling the region down at its top.
func (s *Screen) reverseIndex() {
	s.wrap = false
	switch {
	case s.y == s.top:
		s.scrollDown(s.top, s.bot, 1)
	case s.y > 0:
		s.y--
	}
}

// scrollUp moves rows top+n..bot up by n, blank rows coming in at bot.
func (s *Screen) scrollUp(top, bot, n int) {
	if top > bot {
		return
	}
	n = min(n, bot-top+1)
	copy(s.cells[top:bot+1], s.cells[top+n:bot+1])
	copy(s.soft[top:bot+1], s.soft[top+n:bot+1])
	copy(s.wrapAt[top:bot+1], s.wrapAt[top+n:bot+1])
	for r := bot - n + 1; r <= bot; r++ {
		s.cells[r] = blankRow(s.cols)
		s.soft[r], s.wrapAt[r] = false, 0
	}
}

// scrollDown moves rows top..bot-n down by n, blank rows coming in at top.
func (s *Screen) scrollDown(top, bot, n int) {
	if top > bot {
		return
	}
	n = min(n, bot-top+1)
	copy(s.cells[top+n:bot+1], s.cells[top:bot+1-n])
	copy(s.soft[top+n:bot+1], s.soft[top:bot+1-n])
	copy(s.wrapAt[top+n:bot+1], s.wrapAt[top:bot+1-n])
	for r := top; r < top+n; r++ {
		s.cells[r] = blankRow(s.cols)
		s.soft[r], s.wrapAt[r] = false, 0
	}
}

func (s *Screen) reset() {
	s.main, s.alt = blankRows(s.rows, s.cols), nil
	s.softMain, s.softAlt = make([]bool, s.rows), nil
	s.wrapMain, s.wrapAlt = make([]int, s.rows), nil
	s.layMain, s.layAlt = layout{drawnCols: s.cols, drawnRows: s.rows}, layout{}
	s.cells, s.soft, s.wrapAt, s.lay, s.altOn = s.main, s.softMain, s.wrapMain, &s.layMain, false
	s.x, s.y, s.top, s.bot, s.wrap = 0, 0, 0, s.rows-1, false
}

func (s *Screen) clampCursor() {
	s.x = max(0, min(s.x, s.cols-1))
	s.y = max(0, min(s.y, s.rows-1))
}

// erase blanks the cells of row r from column a up to b, b excluded.
func (s *Screen) erase(r, a, b int) {
	row := s.cells[r]
	a, b = max(0, a), min(len(row), b)
	if b == len(row) {
		s.soft[r] = false
	}
	for c := a; c < b; c++ {
		row[c] = ' '
	}
	if a > 0 && a < len(row) && row[a-1] != wideTail && runeWidth(row[a-1]) == 2 {
		row[a-1] = ' '
	}
	if b < len(row) && row[b] == wideTail {
		row[b] = ' '
	}
}

// csi applies a control sequence that ends in final.
func (s *Screen) csi(final byte) {
	private := len(s.params) > 0 && s.params[0] >= '<' && s.params[0] <= '?'
	var nums []int
	inter := false
	cur, has := 0, false
	for i, c := range s.params {
		switch {
		case private && i == 0:
		case c >= '0' && c <= '9':
			if cur < 100000 {
				cur = cur*10 + int(c-'0')
			}
			has = true
		case c == ';' || c == ':':
			nums = append(nums, cur)
			cur, has = 0, false
		case c >= 0x20 && c <= 0x2f:
			inter = true
		}
	}
	if has || len(nums) > 0 {
		nums = append(nums, cur)
	}
	if inter {
		return // DECSCUSR, DECRQM and the like: nothing drawn
	}
	arg := func(i, def int) int {
		if i < len(nums) && nums[i] > 0 {
			return nums[i]
		}
		return def
	}
	if private {
		if s.params[0] == '?' && (final == 'h' || final == 'l') {
			for _, m := range nums {
				switch m {
				case 1049, 1047, 47:
					s.altScreen(final == 'h', m == 1049)
				case 2026:
					// The update ends for both buffers, whichever is
					// selected then: each one the frame erased whole is
					// laid out for the size now (a frame that erases and
					// draws the main screen again and ends on the
					// alternate one, #226).
					for _, l := range [...]*layout{&s.layMain, &s.layAlt} {
						if final == 'l' && s.syncing && l.unclipAtEnd {
							l.laidOut(s.cols, s.rows)
						}
						l.unclipAtEnd = false
					}
					s.syncing = final == 'h'
				}
			}
		}
		return
	}
	switch final {
	case 'A':
		s.y = max(0, s.y-arg(0, 1))
	case 'B', 'e':
		s.y = min(s.rows-1, s.y+arg(0, 1))
	case 'C', 'a':
		s.x = min(s.cols-1, s.x+arg(0, 1))
	case 'D':
		s.x = max(0, s.x-arg(0, 1))
	case 'E':
		s.x, s.y = 0, min(s.rows-1, s.y+arg(0, 1))
	case 'F':
		s.x, s.y = 0, max(0, s.y-arg(0, 1))
	case 'G', '`':
		s.x = arg(0, 1) - 1
	case 'H', 'f':
		s.y, s.x = arg(0, 1)-1, arg(1, 1)-1
	case 'd':
		s.y = arg(0, 1) - 1
	case 'J':
		switch arg(0, 0) {
		case 0:
			s.erase(s.y, s.x, s.cols)
			for r := s.y + 1; r < s.rows; r++ {
				s.erase(r, 0, s.cols)
			}
			if s.x == 0 && s.y == 0 {
				// From the first cell: the whole screen (Codex clears so).
				s.erasedWhole()
			}
		case 1:
			for r := 0; r < s.y; r++ {
				s.erase(r, 0, s.cols)
			}
			s.erase(s.y, 0, s.x+1)
			if s.x >= s.cols-1 && s.y >= s.rows-1 {
				s.erasedWhole()
			}
		case 2:
			for r := 0; r < s.rows; r++ {
				s.erase(r, 0, s.cols)
			}
			s.erasedWhole()
		}
		// 3 erases the scrollback, which a Screen does not keep.
	case 'K':
		switch arg(0, 0) {
		case 0:
			s.erase(s.y, s.x, s.cols)
		case 1:
			s.erase(s.y, 0, s.x+1)
		default:
			s.erase(s.y, 0, s.cols)
		}
	case 'X':
		s.erase(s.y, s.x, s.x+arg(0, 1))
	case 'P':
		row, n := s.cells[s.y], min(arg(0, 1), s.cols-s.x)
		copy(row[s.x:], row[s.x+n:])
		for c := s.cols - n; c < s.cols; c++ {
			row[c] = ' '
		}
	case '@':
		row, n := s.cells[s.y], min(arg(0, 1), s.cols-s.x)
		copy(row[s.x+n:], row[s.x:s.cols-n])
		for c := s.x; c < s.x+n; c++ {
			row[c] = ' '
		}
	case 'L':
		if s.y >= s.top && s.y <= s.bot {
			s.scrollDown(s.y, s.bot, arg(0, 1))
			s.x = 0
		}
	case 'M':
		if s.y >= s.top && s.y <= s.bot {
			s.scrollUp(s.y, s.bot, arg(0, 1))
			s.x = 0
		}
	case 'S':
		s.scrollUp(s.top, s.bot, arg(0, 1))
	case 'T':
		if len(nums) <= 1 {
			s.scrollDown(s.top, s.bot, arg(0, 1))
		}
	case 'r':
		top, bot := arg(0, 1)-1, arg(1, s.rows)-1
		if top < bot && bot < s.rows {
			s.top, s.bot = top, bot
		} else {
			s.top, s.bot = 0, s.rows-1
		}
		s.x, s.y = 0, 0
	case 's':
		s.saved = [2]int{s.x, s.y}
	case 'u':
		s.x, s.y = s.saved[0], s.saved[1]
	default:
		return
	}
	s.wrap = false
	s.clampCursor()
}

// altScreen switches to the alternate screen (blank) or back to the main one.
func (s *Screen) altScreen(on, cursor bool) {
	if on == s.altOn {
		return
	}
	if on {
		if cursor {
			s.savedMain = [2]int{s.x, s.y}
		}
		s.alt = blankRows(s.rows, s.cols)
		s.softAlt, s.wrapAlt = make([]bool, s.rows), make([]int, s.rows)
		s.layAlt = layout{drawnCols: s.cols, drawnRows: s.rows}
		s.cells, s.soft, s.wrapAt, s.lay, s.altOn = s.alt, s.softAlt, s.wrapAlt, &s.layAlt, true
	} else {
		s.cells, s.soft, s.wrapAt, s.lay, s.altOn = s.main, s.softMain, s.wrapMain, &s.layMain, false
		if cursor {
			s.x, s.y = s.savedMain[0], s.savedMain[1]
			s.clampCursor()
		}
	}
	s.top, s.bot, s.wrap = 0, s.rows-1, false
}

// Text returns what the screen shows: each row's characters with the spaces
// at its end left out, the rows joined by line breaks, the blank rows at the
// bottom left out.
func (s *Screen) Text() string { return s.text(len(s.cells)) }

// Lines is Text with each row autowrap went on from (soft) and the row below
// it read as one line, the row kept whole, as the program wrote them: a word
// a narrow screen cut at its edge reads whole.
func (s *Screen) Lines() string { return s.rowsText(len(s.cells), true) }

// maxLookRows bounds the rows Last gives a pattern: a dialog is the bottom
// of what a TUI draws, and a look at a screen of hundreds of wide rows would
// cost a regular expression tens of milliseconds for nothing.
const maxLookRows = 80

// erasedWhole notes the screen erased whole: what follows is drawn at its
// size now (in a synchronized update, once it ends).
func (s *Screen) erasedWhole() {
	if s.syncing {
		s.lay.unclipAtEnd = true
	} else {
		s.lay.laidOut(s.cols, s.rows)
	}
}

// Rows returns every row of the screen as its characters, a wide
// character's tail left out and trailing spaces trimmed: what the screen
// snapshot compares (Local.screenQuiet).
func (s *Screen) Rows() []string {
	out := make([]string, len(s.cells))
	for r, row := range s.cells {
		line := make([]rune, 0, len(row))
		for _, c := range row {
			if c != wideTail {
				line = append(line, c)
			}
		}
		out[r] = strings.TrimRight(string(line), " ")
	}
	return out
}

// Settled reports whether the screen is a whole frame: no synchronized
// update (2026) begun and not ended, which a terminal would not show yet,
// and nothing drawn at another size that the program has not erased whole
// since (a resize keeps what showed, cut or as laid out before; the
// program erases and redraws it).
func (s *Screen) Settled() bool { return !s.syncing && !s.lay.clipped }

// Drawn reports whether the screen is a frame the program has finished:
// no synchronized update begun and not ended. Unlike Settled, what was
// drawn at another size and not erased since counts: a program that never
// erases its screen (a shell, a line reader) is compared with itself all
// the same (Local.screenQuiet), a resize dropping that comparison's
// baseline.
func (s *Screen) Drawn() bool { return !s.syncing }

// DrawnAt reports whether the screen shows everything the program drew,
// as it drew it (no synchronized update under way, nothing cut by a
// resize since the program last erased the screen whole), and the size it
// was laid out for (drawnCols, drawnRows): the window grown since shows it
// all, laid out for that smaller size; made narrower or shorter since, it
// may be cut (uncut false). What reads the screen for words no longer on
// it (Local.screenShowsNoStartup) asks this, not Settled: a program that
// erases its dialog's lines rather than the whole screen (Codex) draws
// nothing for a window grown, and its screen would never settle again.
func (s *Screen) DrawnAt() (uncut bool, cols, rows int) {
	return !s.syncing && !s.lay.cut, s.lay.drawnCols, s.lay.drawnRows
}

// Size returns the screen's columns and rows.
func (s *Screen) Size() (cols, rows int) { return s.cols, s.rows }

// Last is what a PatternWatcher matches: Text, its last maxLookRows rows.
func (s *Screen) Last() string { return s.text(maxLookRows) }

// text is Text, its last n rows at most.
func (s *Screen) text(n int) string { return s.rowsText(n, false) }

// rowsText is text, and with soft Lines, its last n rows at most.
func (s *Screen) rowsText(n int, soft bool) string {
	var b strings.Builder
	last := -1
	for r := len(s.cells) - 1; r >= 0; r-- {
		if strings.TrimRight(string(s.cells[r]), " ") != "" {
			last = r
			break
		}
	}
	first := max(0, last-n+1)
	for r := first; r <= last; r++ {
		joined := soft && r > first && s.soft[r-1]
		if r > first && !joined {
			b.WriteByte('\n')
		}
		cells := s.cells[r]
		wrapped := soft && s.soft[r] && r < last
		if wrapped {
			// To its extent when autowrap went on from it (wrapAt): a
			// screen grown since holds the growth's blank cells beyond,
			// which would split a word the wrap split.
			if n := s.wrapAt[r]; n > 0 && n < len(cells) {
				cells = cells[:n]
			}
		}
		line := make([]rune, 0, len(cells))
		for _, c := range cells {
			if c != wideTail {
				line = append(line, c)
			}
		}
		if wrapped {
			b.WriteString(string(line))
		} else {
			b.WriteString(strings.TrimRight(string(line), " "))
		}
	}
	return b.String()
}

// Reset changes nothing: the screen is what the program drew, and only the
// program changes it.
func (s *Screen) Reset() {}

// runeWidth approximates the cells a character takes in a terminal: none
// for combining marks and other characters that join the one before, two
// for wide East Asian characters and most emoji, one otherwise.
func runeWidth(r rune) int {
	switch {
	case r < 0x300:
		return 1
	case r <= 0x36f, r >= 0x1ab0 && r <= 0x1aff, r >= 0x1dc0 && r <= 0x1dff, r >= 0x20d0 && r <= 0x20ff,
		r >= 0xfe20 && r <= 0xfe2f, r >= 0x200b && r <= 0x200f, r >= 0x2060 && r <= 0x2064,
		r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0x303e, r >= 0x3041 && r <= 0x33ff,
		r >= 0x3400 && r <= 0x4dbf, r >= 0x4e00 && r <= 0x9fff, r >= 0xa000 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3, r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe4f,
		r >= 0xff00 && r <= 0xff60, r >= 0xffe0 && r <= 0xffe6, r >= 0x1f300 && r <= 0x1f64f,
		r >= 0x1f900 && r <= 0x1f9ff, r >= 0x1fa70 && r <= 0x1faff, r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}
