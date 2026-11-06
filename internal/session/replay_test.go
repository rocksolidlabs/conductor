package session

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A program that redraws one row in place (#692): no line feed, colour
// sequences, a title, a synchronized update, links, two-byte escapes, a
// DCS and characters of two, three and four bytes. Each element is whole:
// a sequence, a string or a character, so a replay may start before any
// of them, or at an ESC (a terminal starts over at one, whatever it was
// in), and nowhere else. Its text is only the runes of replayText.
var inPlace = []string{
	"\x1b[10;1H", "\x1b[2K", "\x1b[38;2;138;138;138;49m", "•", "\x1b[0m", " ", "o", "k", " ",
	"é", "中", "🙂", "\x1b]0;✳ title\x07", "\x1b[?2026h", "\x1b[11;1H", "\x1b[1m", "o", "k",
	"\x1b[0m", "\x1b[?2026l", "\x1b]8;;http://x/\x1b\\", "o", "k", "\x1b]8;;\x1b\\",
	"\x1b7", "\x1b8", "\x1b(B", "\x1bP+q544e\x1b\\",
}

// withLines has line feeds in the ground state and inside a string (a
// clipboard write wrapped as base64 is): only the first kind starts a line.
var withLines = []string{
	"\x1b]52;c;QUJD\nREVG\nR0hJ\x07", "o", "k", "é", "\r", "\n",
	"\x1b[32m", "•", "\x1b[0m", "\r", "\n",
}

// controls is what a terminal's parser does that a simpler one gets wrong
// (#695's reviews): a BEL is a DCS's data and nothing in an APC, ending
// neither; C1 controls (U+0080 to U+009F, two bytes each) begin and end
// sequences and strings, and cancel them; a C0 control after ESC acts and
// the escape goes on; CAN and SUB cancel a string or a sequence, what
// follows being text; a character ends an APC; and an ESC after an ESC
// starts over, so that a sequence after it is whole.
var controls = []string{
	"\x1bP1$rAAAA\x07BBBB;38mXX\x1b\\", "o", "k",
	"\x1b_Ga=T\x07QUJD;9mXX\x1b\\", "o", "k",
	"\u009b38;2;1;2;3m", "\u009d0;title\u009c", "\u0090+q544e\u009c", "\u009fGa=T;QUJD\u009c",
	"\x1b\r[31m", "o", "k",
	"\x1b]0;ti\x18", "t", "l", "e",
	"\x1b[3\x1a", "m",
	"\x1b]0;ti\u0085", "t",
	"\x1b_Gabcé", "o", "k",
	"\x1b", "\x1b[?1049h", "A", "L", "T", "\x1b[?1049l",
	"o", "k", "\r", "\n",
}

// replayText is every rune inPlace, withLines and controls draw.
const replayText = "• oké中🙂tlemALT"

// stream is units repeated n times, and the offsets at which one of its
// elements begins or an ESC is: where a terminal can start.
func stream(unit []string, n int) ([]byte, map[int]bool) {
	var b []byte
	starts := map[int]bool{}
	for range n {
		for _, a := range unit {
			starts[len(b)] = true
			b = append(b, a...)
		}
	}
	for i, c := range b {
		if c == 0x1b {
			starts[i] = true
		}
	}
	return b, starts
}

// The chunk sizes a ring is written in: every path of Append is taken, the
// cut falling in the middle of a write and across the buffer's end.
var chunks = []int{1, 7, 3, 64, 2, 500, 13}

// feedRing writes chunk as the pump does: to the ring, then to the screen
// model, which tells the ring where a replay may start in it.
func feedRing(r *Ring, sc *Screen, chunk []byte) {
	start, _ := r.Append(chunk)
	r.Cut(start, len(chunk), sc.WriteCuts(chunk, r.Every(), nil))
}

func writeChunked(r *Ring, sc *Screen, s []byte) {
	for i := 0; len(s) > 0; i++ {
		n := min(chunks[i%len(chunks)], len(s))
		feedRing(r, sc, s[:n])
		s = s[n:]
	}
}

// cutsOf is where the screen's parser is at a cut in s, read byte by
// byte: cut[i] after s's first i bytes, line[i] when a line starts there.
func cutsOf(s []byte) (cut, line []bool) {
	sc := NewScreen(80, 24)
	cut, line = make([]bool, len(s)+1), make([]bool, len(s)+1)
	cut[0], line[0] = true, true
	for i, b := range s {
		sc.Write([]byte{b})
		cut[i+1] = sc.atCut()
		line[i+1] = cut[i+1] && b == '\n'
	}
	return cut, line
}

// A wrapped ring's replay starts where a terminal can start, at every place
// the cut can fall, however the stream was written: at the first cut the
// screen model found at or after the oldest byte, or an ESC before it; or
// at a line's start within replayAlign of that, never after a line feed
// inside a string. Fed to a screen, it draws only the stream's own text,
// never a fragment of a sequence.
func TestRingReplayStartsWhereATerminalCan(t *testing.T) {
	for _, tc := range []struct {
		name string
		unit []string
	}{{"in place", inPlace}, {"with lines", withLines}, {"controls", controls}} {
		unitLen := len(strings.Join(tc.unit, ""))
		const n = 4
		s, starts := stream(tc.unit, n)
		cut, line := cutsOf(s)
		for drop := range unitLen + 1 {
			// Where the replay starts: the first cut at or after the
			// oldest byte, an ESC before it, a line's start near it.
			first := drop
			for first < len(s) && !cut[first] && s[first] != 0x1b {
				first++
			}
			want := first
			for i := first; drop > 0 && i <= first+replayAlign && i <= len(s); i++ {
				if line[i] {
					want = i
					break
				}
			}
			if !starts[want] {
				t.Fatalf("%s, %d dropped: the screen model's cut at %d is inside an element", tc.name, drop, want)
			}
			for _, whole := range []bool{false, true} {
				r := NewRing(len(s) - drop)
				r.every = 1 // every cut kept: where the replay starts is exact
				sc := NewScreen(80, 24)
				if whole {
					feedRing(r, sc, s)
				} else {
					writeChunked(r, sc, s)
				}
				got, joins := r.Replay()
				at := len(s) - len(got)
				if !bytes.Equal(got, s[at:]) {
					t.Fatalf("%s, %d dropped (whole %v): not the stream's end", tc.name, drop, whole)
				}
				if at != want {
					t.Fatalf("%s, %d dropped (whole %v): the replay starts at %d, want %d (%q)", tc.name, drop, whole, at, want, got[:min(len(got), 24)])
				}
				if !joins {
					t.Fatalf("%s, %d dropped (whole %v): a replay of %d bytes does not join the stream", tc.name, drop, whole, len(got))
				}
				view := NewScreen(80, 24)
				view.Write(got)
				if text := view.Text(); strings.Trim(text, replayText+"\n") != "" {
					t.Fatalf("%s, %d dropped (whole %v): the replay draws %q", tc.name, drop, whole, text)
				}
			}
		}
	}
}

// An ESC that starts over after an ESC dropped begins a whole sequence:
// the replay keeps it, and what it draws on the alternate screen stays
// there (Codex round 1, finding 2).
func TestRingReplayKeepsASequenceAnESCStartsOver(t *testing.T) {
	unit := "\x1b\x1b[?1049hALT\x1b[?1049l"
	s := strings.Repeat("x", 20) + unit + "ok"
	r := NewRing(len(s) - 21) // the first ESC dropped
	sc := NewScreen(80, 24)
	for _, c := range []string{s[:20], unit, "ok"} {
		feedRing(r, sc, []byte(c))
	}
	got := r.Snapshot()
	if string(got) != s[21:] {
		t.Fatalf("the replay is %q, want %q", got, s[21:])
	}
	view := NewScreen(80, 24)
	view.Write(got)
	if text := view.Text(); text != "ok" {
		t.Fatalf("the replay draws %q on the main screen", text)
	}
}

// A line feed near the replay's first possible start is where it starts;
// one further than replayAlign is not looked for.
func TestRingReplayGivesUpAtMostReplayAlignForALine(t *testing.T) {
	for _, k := range []int{0, replayAlign - 1, replayAlign} {
		s := []byte(strings.Repeat("x", 10) + strings.Repeat("o", k) + "\nok")
		r := NewRing(len(s) - 10)
		r.every = 1
		sc := NewScreen(80, 24)
		feedRing(r, sc, s[:10])
		feedRing(r, sc, s[10:])
		got := string(r.Snapshot())
		want := "ok"
		if k+1 > replayAlign {
			want = string(s[10:])
		}
		if got != want {
			t.Fatalf("a line %d bytes in: the replay is %d bytes, want %d", k+1, len(got), len(want))
		}
	}
	// All of the ring inside one string: nothing to replay, and a viewer
	// given it is inside the string.
	r := NewRing(64)
	feedRing(r, NewScreen(80, 24), []byte("\x1b]52;c;"+strings.Repeat("QUJD", 40)))
	if got, joins := r.Replay(); len(got) != 0 || joins {
		t.Fatalf("a ring inside a string replays %q (joins %v)", got, joins)
	}
}

// However many cuts the screen model finds, a ring keeps a bounded number,
// and its replay still starts at one, or at an ESC, near its oldest byte.
func TestRingKeepsItsCutsBounded(t *testing.T) {
	s, starts := stream(append(append(slicesOf(inPlace), withLines...), controls...), 600)
	r := NewRing(64 << 10)
	sc := NewScreen(80, 24)
	writeChunked(r, sc, s)
	r.mu.Lock()
	kept := len(r.cuts)
	r.mu.Unlock()
	if kept > 2*cutsPerRing+2 {
		t.Fatalf("%d cuts kept in a ring of %d bytes", kept, r.Cap())
	}
	got := r.Snapshot()
	at := len(s) - len(got)
	if !starts[at] || at-(len(s)-r.Cap()) > r.Every()+replayAlign+600 {
		t.Fatalf("the replay starts at %d, %d bytes past the oldest byte", at, at-(len(s)-r.Cap()))
	}
}

// slicesOf is a copy of a: appending to it leaves a alone.
func slicesOf(a []string) []string { return append([]string(nil), a...) }

// inPlaceCut is output that redraws one row in place, longer than a ring
// of capacity bytes, whose oldest byte kept is inside the colour
// sequence's parameters ("138;138;49m"), and that ends with end.
func inPlaceCut(capacity int, end string) []byte {
	unit := strings.Join(inPlace, "")
	n := capacity/len(unit) + 2
	cut := strings.Index(unit, "138;")
	pad := ((cut-(n*len(unit)+len(end)-capacity))%len(unit) + len(unit)) % len(unit)
	return []byte(strings.Repeat(unit, n) + strings.Repeat("o", pad) + end)
}

// waitRingEnds waits until the session's ring holds output that ends with
// end: the pump has written all of it.
func waitRingEnds(t *testing.T, s *Local, end string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !bytes.HasSuffix(ringBytes(s.ring), []byte(end)); {
		if time.Now().After(deadline) {
			t.Fatalf("the output never reached the ring: %q", ringBytes(s.ring))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitAtCut waits until the screen model has read all the ring holds and
// found the stream at a cut at its end.
func waitAtCut(t *testing.T, r *Ring) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		r.mu.Lock()
		at := r.endCut == r.written
		r.mu.Unlock()
		if at {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the stream never reached a cut")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ringBytes is all the ring holds, from its oldest byte.
func ringBytes(r *Ring) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw := make([]byte, r.size)
	first := copy(raw, r.buf[r.start:min(r.start+r.size, len(r.buf))])
	copy(raw[first:], r.buf[:r.size-first])
	return raw
}

// A page that attaches to a session whose output redraws one row in place
// past its scrollback is replayed no fragment of a sequence: the replay,
// drawn on a screen, shows only the program's text.
func TestAnAttachReplaysNoFragment(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	const end = "\x1b]0;end\x07"
	out := inPlaceCut(4096, end)
	if _, err := p.outW.Write(out); err != nil {
		t.Fatal(err)
	}
	waitRingEnds(t, s, end)
	if raw := s.ring.buf[s.ring.start:]; !bytes.HasPrefix(raw, []byte("138;")) {
		t.Fatalf("the ring's oldest bytes are %q, not inside a sequence", raw[:min(len(raw), 16)])
	}
	sink := newChanSink(false)
	if _, err := s.Attach("", RoleView, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}
	if waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlReady }) == nil {
		t.Fatal("no ready")
	}
	sc := NewScreen(80, 24)
	for i := 0; i < sink.count(); i++ {
		f, _ := proto.Decode(sink.frame(i))
		if f.Type == proto.TypeScrollback {
			sc.Write(f.Payload)
		}
	}
	if text := sc.Text(); strings.Trim(text, replayText+"\n") != "" || !strings.Contains(text, "• ok") {
		t.Fatalf("the replay draws %q", text)
	}
}

// A page that attaches while the session's scrollback is all inside one
// string (an image larger than the scrollback) is replayed nothing, and
// is sent the live output only from a write that begins at a cut: the
// rest of the string, whose end it never saw, is not drawn as text (Codex
// round 1, finding 3).
func TestAnAttachInsideAStringWaitsForACut(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	image := "\x1b]1337;File=inline=1:" + strings.Repeat("QUJD", 2000)
	if _, err := p.outW.Write([]byte(image)); err != nil {
		t.Fatal(err)
	}
	waitRingEnds(t, s, "QUJDQUJD")
	sink := newChanSink(false)
	if _, err := s.Attach("", RoleView, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}
	if waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlReady }) == nil {
		t.Fatal("no ready")
	}
	// The string ends in a write of its own, which the screen model has
	// read before the next one comes.
	if _, err := p.outW.Write([]byte("QUJD\x07ok")); err != nil {
		t.Fatal(err)
	}
	waitRingEnds(t, s, "QUJD\x07ok")
	waitAtCut(t, s.ring)
	if _, err := p.outW.Write([]byte("\r\nnext")); err != nil {
		t.Fatal(err)
	}
	sc := NewScreen(80, 24)
	for deadline, i := time.Now().Add(10*time.Second), 0; !strings.Contains(sc.Text(), "next"); {
		for ; i < sink.count(); i++ {
			if f, _ := proto.Decode(sink.frame(i)); f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback {
				sc.Write(f.Payload)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the viewer never drew the next output: %q", sc.Text())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if text := sc.Text(); strings.ContainsAny(text, "QUJD") {
		t.Fatalf("the viewer drew the string's rest as text: %q", text)
	}
}

// LastLine reads the same replay: output with no line break in its ring,
// cut inside a sequence, gives no fragment of it.
func TestLastLineHasNoFragment(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	const end = "\x1b]0;end\x07"
	out := inPlaceCut(4096, end)
	if _, err := p.outW.Write(out); err != nil {
		t.Fatal(err)
	}
	waitRingEnds(t, s, end)
	if l := s.LastLine(); l == "" || strings.Trim(l, replayText) != "" {
		t.Fatalf("last line %q", l)
	}
}

// What a replay costs at attach, under the session's lock, with the ring
// all inside one string (no cut, a look for an ESC through all of it): at
// the default scrollback and at its largest (config: 64 MiB).
func BenchmarkRingReplayInsideAString(b *testing.B) {
	for _, size := range []int{256 << 10, 64 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			r := NewRing(size)
			sc := NewScreen(80, 24)
			feedRing(r, sc, []byte("\x1b]1337;File=inline=1:"))
			chunk := bytes.Repeat([]byte("QUJD"), proto.MaxOutput/4)
			for range size/len(chunk) + 2 {
				feedRing(r, sc, chunk)
			}
			b.ResetTimer()
			for range b.N {
				if got, _ := r.Replay(); len(got) != 0 {
					b.Fatalf("replayed %d bytes", len(got))
				}
			}
		})
	}
}
