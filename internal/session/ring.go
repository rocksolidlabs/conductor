package session

import (
	"bytes"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// replayAlign bounds the bytes a replay gives up to begin at a line's start
// (Ring).
const replayAlign = 4096

// cutsPerRing bounds the cuts a ring keeps (Ring.cuts): about two for each
// 1/cutsPerRing of its capacity.
const cutsPerRing = 256

// Ring is a fixed-capacity byte buffer that keeps the most recent bytes of
// a session's output: what a viewer that attaches is replayed (Replay).
//
// Where a replay starts. A terminal that begins reading a stream anywhere
// but at a cut draws what follows the cut as text: the rest of a control
// sequence's parameters ("138;49m"), of an OSC, DCS, SOS, PM or APC string
// (a title, a link, an image, a clipboard write, whatever its length), a
// character's continuation bytes. The ring does not parse the stream: the
// screen model every session keeps (Local.shot, a Screen, whose parser is
// the page's terminal's) is fed every chunk, and tells the ring where it
// was at a cut in it (Cut): between sequences, strings and characters, in
// its ground state. The ring keeps those places (cuts), at least a
// 1/cutsPerRing of its capacity apart, besides one at a line's start
// within replayAlign after each, and none older than its oldest byte: at
// most about 2*cutsPerRing of them, whatever the capacity. A replay starts
// at the oldest cut kept, or at an ESC before it within a 1/cutsPerRing of
// the capacity and a chunk (proto.MaxOutput) of the oldest byte (a
// terminal starts a sequence over at an ESC, whatever it was in; past
// that, a cut follows an ESC sooner); then, when that is not a
// line's start and a cut at one comes within replayAlign of it, there. A
// ring holding neither replays nothing: all it holds is inside one string
// or sequence, which draws nothing.
//
// The bytes given up are the rest of what the cut fell in, which draws
// nothing, and, before the first cut, at most a 1/cutsPerRing of the
// capacity and what the screen model was given at once (at most
// proto.MaxOutput), and replayAlign more for a line's start. So the
// replay starts only where a terminal can, and a viewer that reads it is
// in the stream's state at its end: the live output that follows is read
// as a terminal that read it all reads it. A replay that holds nothing
// leaves the viewer at the ring's end, which may be inside a string: the
// live output is then held from that viewer until a chunk that begins at
// a cut (Replay's whole, Hub.BroadcastLoud).
//
// What a replay of a wrapped ring cannot restore is what the bytes before
// its start set: the cells they drew and the modes (the alternate screen,
// colours, the cursor). A synchronized update (mode 2026) whose begin was
// dropped replays as the rest of its frame, drawn at once, and its end,
// which a terminal takes as the end of no update: the cells the frame's
// lost first half drew are blank, as every cell drawn before the start is.
type Ring struct {
	mu    sync.Mutex
	buf   []byte
	start int // index of the oldest byte when full
	size  int // bytes currently stored
	// written is the bytes written over the ring's life: the stream's
	// offset at its end.
	written int64
	// cuts are where a replay may start (Cut), oldest first, none before
	// the oldest byte kept.
	cuts []ringCut
	// every is the least distance between two cuts kept (but for a
	// line's start, Cut).
	every int
	// endCut is the stream's offset at the end of the last chunk Cut was
	// told ends at a cut.
	endCut int64
}

// A ringCut is a place a replay may start: a stream offset, and whether a
// line starts there.
type ringCut struct {
	at   int64
	line bool
}

// NewRing allocates a ring of the given capacity (minimum 1).
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{
		buf:   make([]byte, capacity),
		cuts:  []ringCut{{0, true}}, // the stream begins at one, and a line
		every: max(1, capacity/cutsPerRing),
	}
}

// Write appends p, overwriting the oldest bytes when full.
func (r *Ring) Write(p []byte) (int, error) {
	r.Append(p)
	return len(p), nil
}

// Append is Write, and answers the stream's offset at p's first byte and
// whether the stream is at a cut there (the last chunk Cut was told of
// ended at one, and ended there).
func (r *Ring) Append(p []byte) (start int64, fromCut bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start, fromCut = r.written, r.endCut == r.written
	n := len(p)
	cap := len(r.buf)
	r.written += int64(n)
	if n >= cap {
		copy(r.buf, p[n-cap:])
		r.start, r.size = 0, cap
	} else {
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
	}
	// The cuts the oldest bytes took with them go.
	oldest, i := r.oldest(), 0
	for i < len(r.cuts) && r.cuts[i].at < oldest {
		i++
	}
	if i > 0 {
		r.cuts = append(r.cuts[:0], r.cuts[i:]...)
	}
	return start, fromCut
}

// Every is the distance at which the ring keeps cuts: what to ask the
// screen model for (Screen.WriteCuts).
func (r *Ring) Every() int { return r.every }

// Cut notes where a replay may start in the chunk of n bytes Append put at
// start: cuts, offsets into it, in order, as the screen model found them
// (Screen.WriteCuts). One at n says the stream is at a cut at the chunk's
// end. A cut is kept when it is still in the ring and every bytes or more
// after the last kept, or when it is a line's start within replayAlign
// after a last kept that is not.
func (r *Ring) Cut(start int64, n int, cuts []chunkCut) {
	r.mu.Lock()
	defer r.mu.Unlock()
	oldest := r.oldest()
	for _, c := range cuts {
		at := start + int64(c.at)
		if c.at == n {
			r.endCut = at
		}
		if at < oldest {
			continue
		}
		if k := len(r.cuts); k > 0 {
			last := r.cuts[k-1]
			d := at - last.at
			if d <= 0 || d < int64(r.every) && !(c.line && !last.line && d <= replayAlign) {
				continue
			}
		}
		r.cuts = append(r.cuts, ringCut{at, c.line})
	}
}

// oldest is the stream's offset at the oldest byte kept. Under mu.
func (r *Ring) oldest() int64 { return r.written - int64(r.size) }

// Snapshot returns a copy of the stored bytes in order, from where a replay
// starts (Ring).
func (r *Ring) Snapshot() []byte {
	out, _ := r.Replay()
	return out
}

// Replay returns a copy of the stored bytes from where a replay starts
// (Ring), and whether a viewer that reads it continues with the stream at
// a cut (whole): when it holds something, or the stream is at a cut at the
// ring's end. Its cost is a look through the cuts kept and, for an ESC,
// through at most every+proto.MaxOutput bytes (a memchr), besides the copy.
func (r *Ring) Replay() (out []byte, whole bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	oldest := r.oldest()
	at, line := r.written, false
	if len(r.cuts) > 0 {
		at, line = r.cuts[0].at, r.cuts[0].line
	}
	// An ESC near the oldest byte: one further on would have a cut after
	// it sooner than that, but in a stream with none (ESC after ESC, a
	// sequence never ended), whose replay may hold nothing.
	if i := r.index(0x1b, oldest, min(at, oldest+int64(r.every+proto.MaxOutput))); i >= 0 {
		at, line = i, false
	}
	for _, c := range r.cuts {
		if line || c.at-at > replayAlign {
			break
		}
		if c.line && c.at >= at {
			at = c.at
			break
		}
	}
	out = make([]byte, r.written-at)
	cap := len(r.buf)
	from := (r.start + int(at-oldest)) % cap
	first := copy(out, r.buf[from:min(from+len(out), cap)])
	copy(out[first:], r.buf[:len(out)-first])
	return out, len(out) > 0 || r.endCut == r.written
}

// index is the stream offset of the first b kept between the offsets from
// and to (to excluded), -1 when there is none. Under mu.
func (r *Ring) index(b byte, from, to int64) int64 {
	if from >= to {
		return -1
	}
	cap := len(r.buf)
	i := (r.start + int(from-r.oldest())) % cap
	n := int(to - from)
	head := min(n, cap-i)
	if k := bytes.IndexByte(r.buf[i:i+head], b); k >= 0 {
		return from + int64(k)
	}
	if k := bytes.IndexByte(r.buf[:n-head], b); k >= 0 {
		return from + int64(head+k)
	}
	return -1
}

// Len returns the number of stored bytes.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Cap returns the capacity.
func (r *Ring) Cap() int { return len(r.buf) }
