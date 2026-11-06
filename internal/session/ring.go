package session

import (
	"bytes"
	"sync"
)

// Ring is a fixed-capacity byte buffer that keeps the most recent bytes.
type Ring struct {
	mu      sync.Mutex
	buf     []byte
	start   int // index of the oldest byte when full
	size    int // bytes currently stored
	wrapped bool
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
	if r.size+n > cap {
		// At least one byte written is no longer held: only then may the
		// oldest held byte be the tail of something (Snapshot). A write
		// that exactly fills the ring drops nothing.
		r.wrapped = true
	}
	if n >= cap {
		copy(r.buf, p[n-cap:])
		r.start, r.size = 0, cap
		return n, nil
	}
	end := (r.start + r.size) % cap
	first := copy(r.buf[end:], p)
	if first < n {
		copy(r.buf, p[first:])
	}
	if r.size+n > cap {
		overflow := r.size + n - cap
		r.start = (r.start + overflow) % cap
		r.size = cap
	} else {
		r.size += n
	}
	return n, nil
}

// replayBound is how far into a wrapped ring Snapshot looks for where a
// replay may start (replayStart).
const replayBound = 4096

// Snapshot returns a copy of the stored bytes in order. When the buffer has
// wrapped, its oldest bytes may be the tail of an escape sequence, a string
// or a character, which a terminal fed them would draw as text (#692): the
// copy starts where a terminal can (replayStart).
func (r *Ring) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, r.size)
	cap := len(r.buf)
	first := copy(out, r.buf[r.start:min(r.start+r.size, cap)])
	if first < r.size {
		copy(out[first:], r.buf[:r.size-first])
	}
	if r.wrapped {
		out = out[replayStart(out):]
	}
	return out
}

// replayStart is where a replay of b, a wrapped ring's bytes, starts: the
// earliest of its first ESC and the byte after its first line feed within
// its first replayBound bytes; when they hold neither, its oldest byte that
// is not a UTF-8 continuation byte, at most three skipped. It needs no
// state: an ESC restarts a terminal's parser whatever came before it (it
// begins a sequence, cancels one under way, or begins a string's
// terminator, which does nothing with no string open), so what comes
// before the first ESC is text or the tail of a sequence or string, and
// dropping it gives up less than replayBound bytes of text. A line feed in
// text begins a line. Accepted, rare and bounded: an ESC inside a device
// control string's payload (tmux's passthrough doubles one) is taken for
// a sequence's start; a line feed inside a string's payload or a sequence
// before the first ESC is taken for a line's end, and the payload's tail
// drawn; and output with no ESC and no line feed in replayBound bytes
// replays from its oldest whole character, which may be the tail of a long
// string.
func replayStart(b []byte) int {
	head := b[:min(len(b), replayBound)]
	start := -1
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		start = i + 1
	}
	if i := bytes.IndexByte(head, 0x1b); i >= 0 && (start < 0 || i < start) {
		start = i
	}
	if start >= 0 {
		return start
	}
	for start = 0; start < min(len(b), 3) && b[start]&0xc0 == 0x80; start++ {
	}
	return start
}

// Len returns the number of stored bytes.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Cap returns the capacity.
func (r *Ring) Cap() int { return len(r.buf) }
