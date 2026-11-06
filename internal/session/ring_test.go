package session

import (
	"bytes"
	"testing"
)

func TestRingWrapAndSnapshot(t *testing.T) {
	r := NewRing(10)
	sc := NewScreen(80, 24)
	feedRing(r, sc, []byte("abc"))
	if got := r.Snapshot(); string(got) != "abc" {
		t.Fatalf("got %q", got)
	}
	feedRing(r, sc, []byte("defgh"))
	feedRing(r, sc, []byte("ij"))
	if got := r.Snapshot(); string(got) != "abcdefghij" || r.Len() != 10 {
		t.Fatalf("got %q len %d", got, r.Len())
	}
	feedRing(r, sc, []byte("kl"))
	// Wrapped without a line feed: from the first cut kept, every byte of
	// a ring this small (a 1/cutsPerRing of it is less than one).
	if got := r.Snapshot(); string(got) != "cdefghijkl" {
		t.Fatalf("got %q", got)
	}
}

// The oldest byte kept begins a line (the one before it, dropped, was a
// line feed the screen model read): the replay starts there.
func TestRingNewlineTrimAfterWrap(t *testing.T) {
	r := NewRing(8)
	feedRing(r, NewScreen(80, 24), []byte("12\n34\n56\n78"))
	if got := r.Snapshot(); string(got) != "34\n56\n78" {
		t.Fatalf("got %q", got)
	}
}

func TestRingOversizedWrite(t *testing.T) {
	r := NewRing(4)
	sc := NewScreen(80, 24)
	feedRing(r, sc, bytes.Repeat([]byte("x"), 100))
	feedRing(r, sc, []byte("yz"))
	if got := r.Snapshot(); string(got) != "xxyz" {
		t.Fatalf("got %q", got)
	}
	if NewRing(0).Cap() != 1 {
		t.Fatal("zero capacity must be clamped")
	}
}

// A ring written with no cut told knows no place a replay may start once
// it wraps (nothing told it the stream's state): it replays nothing, and
// says the viewer is not at a cut.
func TestRingWithoutCutsReplaysNothing(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("12\n34\n56\n78"))
	if got, joins := r.Replay(); len(got) != 0 || joins {
		t.Fatalf("got %q, joins %v", got, joins)
	}
}
