package session

import (
	"bytes"
	"sync"
)

// replayAlign bounds the bytes a replay gives up to begin at a line's start
// (Ring).
const replayAlign = 4096

// Ring is a fixed-capacity byte buffer that keeps the most recent bytes of
// a session's output: what a viewer that attaches is replayed (Snapshot).
//
// Where a replay starts. Until the ring wraps it starts at the stream's
// first byte. Once it has wrapped, its oldest byte is wherever the cut
// fell, and a terminal that begins there draws what follows the cut as
// text: the rest of a control sequence's parameters ("138;49m"), of an
// OSC, DCS, SOS, PM or APC string up to its BEL or ST (a title, a link, an
// image, a clipboard write, whatever its length), a two-byte escape's
// final byte, a character's continuation bytes. The ring knows where the
// cut fell: every byte it drops goes through a scanner (tail) that has
// seen each byte before it, so the scanner holds the stream's state at the
// oldest byte kept. A replay starts at a byte the stream reaches in its
// ground state (no sequence or string under way, StripANSI's reading)
// that is not a UTF-8 continuation byte: the first byte of a character,
// of a control or of an escape sequence. The first such byte is the start,
// unless a line feed read in the ground state comes within replayAlign
// bytes of it: then the first such byte after the line feed, so that the
// page draws from a line's beginning. The bytes given up are the rest of
// what was under way at the cut, which draws nothing, and at most
// replayAlign bytes more for the line feed; a ring that holds no such byte
// (all of it inside one string) replays nothing, which draws nothing too.
//
// What a replay of a wrapped ring cannot restore is what the dropped bytes
// set: the cells they drew, the modes (the alternate screen, colours, the
// cursor). A synchronized update (mode 2026) whose begin was dropped
// replays as the rest of its frame, drawn at once, and its end, which a
// terminal takes as the end of no update: the cells the frame's lost first
// half drew are blank, as every cell drawn before the start is.
type Ring struct {
	mu      sync.Mutex
	buf     []byte
	start   int // index of the oldest byte when full
	size    int // bytes currently stored
	wrapped bool
	// tail is the stream's state at the oldest byte kept: the bytes the
	// ring dropped, scanned in order.
	tail escScanner
}

// NewRing allocates a ring of the given capacity (minimum 1).
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]byte, capacity)}
}

// Write appends p, discarding the oldest bytes when full.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	cap := len(r.buf)
	if n >= cap {
		r.wrapped = r.wrapped || r.size+n > cap
		r.drop(r.size)
		r.tail.skip(p[:n-cap])
		copy(r.buf, p[n-cap:])
		r.start, r.size = 0, cap
		return n, nil
	}
	if r.size+n > cap {
		// The oldest bytes go: scanned before they are written over.
		overflow := r.size + n - cap
		r.drop(overflow)
		r.start = (r.start + overflow) % cap
		r.size -= overflow
		r.wrapped = true
	}
	end := (r.start + r.size) % cap
	first := copy(r.buf[end:], p)
	if first < n {
		copy(r.buf, p[first:])
	}
	r.size += n
	return n, nil
}

// drop scans the n oldest bytes into tail. The caller moves start past them.
func (r *Ring) drop(n int) {
	first := min(n, len(r.buf)-r.start)
	r.tail.skip(r.buf[r.start : r.start+first])
	r.tail.skip(r.buf[:n-first])
}

// Snapshot returns a copy of the stored bytes in order, from where a replay
// starts (Ring).
func (r *Ring) Snapshot() []byte {
	r.mu.Lock()
	out := make([]byte, r.size)
	cap := len(r.buf)
	first := copy(out, r.buf[r.start:min(r.start+r.size, cap)])
	if first < r.size {
		copy(out[first:], r.buf[:r.size-first])
	}
	wrapped, tail := r.wrapped, r.tail
	r.mu.Unlock()
	if wrapped {
		out = out[replayStart(out, tail):]
	}
	return out
}

// replayStart is where a replay of out starts (Ring), st the stream's
// state at out's first byte.
func replayStart(out []byte, st escScanner) int {
	first, afterLF := -1, false
	for i, b := range out {
		at := st.state == escText && b&0xc0 != 0x80 // a byte a terminal can start at
		st.text(b)
		switch {
		case !at:
		case afterLF:
			return i
		case first < 0:
			first, afterLF = i, b == '\n'
		case i-first >= replayAlign:
			return first
		case b == '\n':
			afterLF = true
		}
	}
	if first < 0 {
		return len(out)
	}
	return first // no line feed with a byte to start at after it
}

// Len returns the number of stored bytes.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Cap returns the capacity.
func (r *Ring) Cap() int { return len(r.buf) }

// skip scans p, keeping the scanner's place. What cannot change the state
// is passed over at once: text up to the next ESC, a string up to its BEL
// or ESC, a control sequence's parameter and intermediate bytes.
func (e *escScanner) skip(p []byte) {
	for i := 0; i < len(p); i++ {
		switch e.state {
		case escText:
			j := bytes.IndexByte(p[i:], 0x1b)
			if j < 0 {
				return
			}
			i += j
		case escString:
			j := bytes.IndexAny(p[i:], "\x07\x1b")
			if j < 0 {
				return
			}
			i += j
		case escCSI:
			for i < len(p) && p[i] >= 0x20 && p[i] <= 0x3f {
				i++
			}
			if i == len(p) {
				return
			}
		}
		e.text(p[i])
	}
}
