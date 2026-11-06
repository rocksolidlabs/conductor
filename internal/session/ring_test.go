package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestRingWrapAndSnapshot(t *testing.T) {
	r := NewRing(10)
	r.Write([]byte("abc"))
	if got := r.Snapshot(); string(got) != "abc" {
		t.Fatalf("got %q", got)
	}
	r.Write([]byte("defgh"))
	r.Write([]byte("ij"))
	if got := r.Snapshot(); string(got) != "abcdefghij" || r.Len() != 10 {
		t.Fatalf("got %q len %d", got, r.Len())
	}
	r.Write([]byte("kl"))
	// wrapped without newline: full content minus the two oldest bytes
	if got := r.Snapshot(); string(got) != "cdefghijkl" {
		t.Fatalf("got %q", got)
	}
}

func TestRingNewlineTrimAfterWrap(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("12\n34\n56\n78"))
	if got := r.Snapshot(); string(got) != "56\n78" {
		t.Fatalf("got %q", got)
	}
}

func TestRingOversizedWrite(t *testing.T) {
	r := NewRing(4)
	r.Write(bytes.Repeat([]byte("x"), 100))
	r.Write([]byte("yz"))
	if got := r.Snapshot(); string(got) != "xxyz" {
		t.Fatalf("got %q", got)
	}
	if NewRing(0).Cap() != 1 {
		t.Fatal("zero capacity must be clamped")
	}
}

// wrappedRing is a ring that has wrapped and holds kept, its oldest byte
// kept[0].
func wrappedRing(kept string) *Ring {
	r := NewRing(len(kept))
	r.Write([]byte("Z" + kept))
	return r
}

// Where a wrapped ring's replay starts (#692): at the first ESC or after
// the first line feed within replayBound bytes, whichever comes first;
// with neither, at the oldest whole character.
func TestRingReplayStart(t *testing.T) {
	for _, tc := range []struct{ name, kept, want string }{
		{"the tail of a colour", "8;138;49m•\x1b[0m ok", "\x1b[0m ok"},
		{"the tail of a title ended by BEL", "TLE\x07ok\x1b[1mA", "\x1b[1mA"},
		{"the tail of a title ended by ESC backslash", "TLE\x1b\\ok", "\x1b\\ok"},
		{"a line feed first", "ab\ncd\x1b[0m", "cd\x1b[0m"},
		{"an ESC first", "a\x1b[1mb\ncd", "\x1b[1mb\ncd"},
		{"an ESC as the oldest byte", "\x1b[1mb", "\x1b[1mb"},
		{"the tail of a character", "\x99\x82ok", "ok"},
		{"at most three continuation bytes", "\x80\x80\x80\x80ok", "\x80ok"},
		{"an ESC past the bound", "\xa9" + strings.Repeat("é", replayBound/2) + "\x1b[0m", strings.Repeat("é", replayBound/2) + "\x1b[0m"},
		{"a line feed at the bound's last byte", strings.Repeat("x", replayBound-1) + "\ny", "y"},
		{"a line feed past the bound", strings.Repeat("x", replayBound) + "\ny", strings.Repeat("x", replayBound) + "\ny"},
		// Accepted (Ring.replayStart): a line feed inside a string's
		// payload before the first ESC is taken for a line's end.
		{"a line feed inside a string", "QUJD\nREVG\x07ok", "REVG\x07ok"},
	} {
		if got := string(wrappedRing(tc.kept).Snapshot()); got != tc.want {
			t.Errorf("%s: %.40q replays %.40q, want %.40q", tc.name, tc.kept, got, tc.want)
		}
	}
	r := NewRing(64)
	r.Write([]byte("8;1mok"))
	if got := string(r.Snapshot()); got != "8;1mok" {
		t.Errorf("a ring that has not wrapped replays %q", got)
	}
}

// Streams a program writes, each element whole: colour sequences, cursor
// moves, titles ended by BEL and by ESC backslash, a synchronized update's
// frame and characters of two, three and four bytes. What they draw is
// only drawnRunes.
var (
	inPlace = []string{
		"\x1b[10;1H", "\x1b[2K", "\x1b[38;2;138;138;138;49m", "•", "\x1b[0m", " ", "o", "k", " ",
		"é", "中", "🙂", "\x1b]0;TITLE\x07", "\x1b[?2026h", "\x1b[11;1H", "\x1b[1m", "o", "k",
		"\x1b[0m", "\x1b[?2026l", "\x1b]2;TITLE\x1b\\",
	}
	withLines  = append(append([]string{}, inPlace...), "\r\n", "o", "k", "\x1b[32m", "•", "\x1b[0m", "\r\n")
	plain      = []string{"é", "中", "🙂", "o", "k", " "}
	drawnRunes = "•oké中🙂 "
)

// A wrapped ring's replay starts where the rule says, at every offset the
// ring's oldest byte can fall on, however the stream was written; and for
// a stream with an ESC within the bound, the replay drawn on a fresh
// screen shows only the stream's own characters, never a fragment of a
// sequence's parameters or a string's payload.
func TestRingReplayStartsWhereATerminalCan(t *testing.T) {
	for _, tc := range []struct {
		name     string
		unit     []string
		capacity int
	}{
		{"in place", inPlace, 256},
		{"in place, past the bound", inPlace, 2 * replayBound},
		{"with lines", withLines, 256},
		{"plain", plain, 64},
		{"plain, past the bound", plain, 2 * replayBound},
	} {
		unit := strings.Join(tc.unit, "")
		data := []byte(strings.Repeat(unit, tc.capacity/len(unit)+3))
		for k := range len(unit) {
			r := NewRing(tc.capacity)
			for in := data[:k+tc.capacity+len(unit)]; len(in) > 0; {
				n := min(len(in), 7)
				r.Write(in[:n])
				in = in[n:]
			}
			kept := data[k+len(unit) : k+tc.capacity+len(unit)]
			got := r.Snapshot()
			if !bytes.HasSuffix(kept, got) {
				t.Fatalf("%s, offset %d: the replay is not the kept bytes' tail", tc.name, k)
			}
			start := len(kept) - len(got)
			head := kept[:min(len(kept), replayBound)]
			if bytes.IndexByte(head, 0x1b) >= 0 || bytes.IndexByte(head, '\n') >= 0 {
				atESC := start < len(kept) && kept[start] == 0x1b
				afterLF := start > 0 && kept[start-1] == '\n'
				before := kept[:max(0, start-1)]
				if !atESC && !afterLF || bytes.IndexByte(kept[:start], 0x1b) >= 0 || bytes.IndexByte(before, '\n') >= 0 {
					t.Fatalf("%s, offset %d: the replay starts at %d, %q", tc.name, k, start, got[:min(len(got), 16)])
				}
			} else if start > 3 || !allContinuation(kept[:start]) || start < len(kept) && kept[start]&0xc0 == 0x80 {
				t.Fatalf("%s, offset %d: the replay starts at %d, %q", tc.name, k, start, got[:min(len(got), 16)])
			}
			if bytes.IndexByte(kept[:min(len(kept), replayBound)], 0x1b) < 0 {
				continue
			}
			sc := NewScreen(80, 24)
			sc.Write(got)
			for _, row := range sc.Rows() {
				if strings.Trim(row, drawnRunes) != "" {
					t.Fatalf("%s, offset %d: the replay draws %q", tc.name, k, row)
				}
			}
		}
	}
}

// allContinuation says every byte of b is a UTF-8 continuation byte.
func allContinuation(b []byte) bool {
	for _, c := range b {
		if c&0xc0 != 0x80 {
			return false
		}
	}
	return true
}
