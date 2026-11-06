package session

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A program that redraws one row in place (#692): no line feed, colour
// sequences, a title, a synchronized update, links, two-byte escapes, a
// DCS and characters of two, three and four bytes. Each element is whole:
// a sequence, a string or a character, so a replay may start before any
// of them and nowhere else. Its text is only the runes of replayText.
var inPlace = []string{
	"\x1b[10;1H", "\x1b[2K", "\x1b[38;2;138;138;138;49m", "•", "\x1b[0m", " ", "o", "k", " ",
	"é", "中", "🙂", "\x1b]0;✳ title\x07", "\x1b[?2026h", "\x1b[11;1H", "\x1b[1m", "o", "k",
	"\x1b[0m", "\x1b[?2026l", "\x1b]8;;http://x/\x1b\\", "o", "k", "\x1b]8;;\x1b\\",
	"\x1b7", "\x1b8", "\x1b(B", "\x1bP+q544e\x1b\\",
}

// replayText is every rune inPlace and withLines draw.
const replayText = "• oké中🙂"

// withLines has line feeds in the ground state and inside a string (a
// clipboard write wrapped as base64 is): only the first kind starts a line.
var withLines = []string{
	"\x1b]52;c;QUJD\nREVG\nR0hJ\x07", "o", "k", "é", "\r", "\n",
	"\x1b[32m", "•", "\x1b[0m", "\r", "\n",
}

// stream is units repeated n times, and the offsets at which one of its
// elements begins: where a terminal can start.
func stream(unit []string, n int) ([]byte, map[int]bool) {
	var b []byte
	starts := map[int]bool{}
	for range n {
		for _, a := range unit {
			starts[len(b)] = true
			b = append(b, a...)
		}
	}
	return b, starts
}

// lineFeeds is the offsets of the elements of s that are a line feed of
// their own (in the ground state).
func lineFeeds(unit []string, n int) map[int]bool {
	at, lf := 0, map[int]bool{}
	for range n {
		for _, a := range unit {
			if a == "\n" {
				lf[at] = true
			}
			at += len(a)
		}
	}
	return lf
}

// The chunk sizes a ring is written in: every path of Write is taken, the
// cut falling in the middle of a write and across the buffer's end.
var chunks = []int{1, 7, 3, 64, 2, 500, 13}

func writeChunked(r *Ring, s []byte) {
	for i := 0; len(s) > 0; i++ {
		n := min(chunks[i%len(chunks)], len(s))
		r.Write(s[:n])
		s = s[n:]
	}
}

// A wrapped ring's replay starts where a terminal can start, at every place
// the cut can fall: the first element that begins at or after the oldest
// byte, or after a line feed in the ground state within replayAlign of
// it, never after one inside a string. Fed to a screen, it draws only the
// stream's own text, never a fragment of a sequence.
func TestRingReplayStartsWhereATerminalCan(t *testing.T) {
	for _, tc := range []struct {
		name string
		unit []string
	}{{"in place", inPlace}, {"with lines", withLines}} {
		unitLen := len(strings.Join(tc.unit, ""))
		const n = 4
		s, starts := stream(tc.unit, n)
		lfs := lineFeeds(tc.unit, n)
		for cut := range unitLen + 1 {
			for _, whole := range []bool{false, true} {
				r := NewRing(len(s) - cut)
				if whole {
					r.Write(s)
				} else {
					writeChunked(r, s)
				}
				got := r.Snapshot()
				at := len(s) - len(got)
				if !bytes.Equal(got, s[at:]) {
					t.Fatalf("%s, cut at %d (whole %v): not the stream's end", tc.name, cut, whole)
				}
				first := cut
				for !starts[first] {
					first++
				}
				// Nothing dropped (cut 0): the stream's first byte.
				want := first
				for i := first; cut > 0 && i < first+replayAlign && i < len(s); i++ {
					if lfs[i] {
						want = i + 1
						break
					}
				}
				if at != want {
					t.Fatalf("%s, cut at %d (whole %v): the replay starts at %d, want %d (%q)", tc.name, cut, whole, at, want, got[:min(len(got), 24)])
				}
				sc := NewScreen(80, 24)
				sc.Write(got)
				if text := sc.Text(); strings.Trim(text, replayText+"\n") != "" {
					t.Fatalf("%s, cut at %d (whole %v): the replay draws %q", tc.name, cut, whole, text)
				}
			}
		}
	}
}

// A line feed near the replay's first possible start is where it starts;
// one further than replayAlign is not looked for.
func TestRingReplayGivesUpAtMostReplayAlignForALine(t *testing.T) {
	for _, gap := range []int{0, replayAlign - 1, replayAlign} {
		s := append(bytes.Repeat([]byte("o"), 10+gap), "\nok"...)
		r := NewRing(len(s) - 10)
		r.Write([]byte(strings.Repeat("x", 10)))
		r.Write(s[10:])
		got := string(r.Snapshot())
		want := "ok"
		if gap == replayAlign {
			want = string(s[10:])
		}
		if got != want {
			t.Fatalf("a line feed %d bytes in: the replay is %d bytes, want %d", gap, len(got), len(want))
		}
	}
	// All of the ring inside one string: nothing to replay.
	r := NewRing(64)
	r.Write([]byte("\x1b]52;c;" + strings.Repeat("QUJD", 40)))
	if got := r.Snapshot(); len(got) != 0 {
		t.Fatalf("a ring inside a string replays %q", got)
	}
}

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
	for deadline := time.Now().Add(10 * time.Second); !bytes.HasSuffix(s.ring.Snapshot(), []byte(end)); {
		if time.Now().After(deadline) {
			t.Fatalf("the output never reached the ring: %q", s.ring.Snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
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
