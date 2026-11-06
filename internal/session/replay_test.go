package session

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// inPlaceCut is output that redraws one row in place with no line feed,
// longer than a ring of capacity bytes, whose oldest byte kept is inside
// the colour sequence's parameters ("138;138;49m"), and that ends with end.
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
// past its scrollback (#692) is replayed no fragment of a sequence: the
// replay, drawn on a screen, shows only the program's characters.
func TestAnAttachReplaysNoFragment(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	const end = "\x1b]0;TITLE\x07"
	if _, err := p.outW.Write(inPlaceCut(4096, end)); err != nil {
		t.Fatal(err)
	}
	waitRingEnds(t, s, end)
	if raw := ringBytes(s.ring); !bytes.HasPrefix(raw, []byte("138;")) {
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
		if f, _ := proto.Decode(sink.frame(i)); f.Type == proto.TypeScrollback {
			sc.Write(f.Payload)
		}
	}
	if text := sc.Text(); strings.Trim(text, drawnRunes+"\n") != "" || !strings.Contains(text, "• ok") {
		t.Fatalf("the replay draws %q", text)
	}
}

// LastLine reads from the same start: output with no line break in its
// ring, cut inside a sequence, gives no fragment of it.
func TestLastLineHasNoFragment(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	const end = "\x1b]0;TITLE\x07"
	if _, err := p.outW.Write(inPlaceCut(4096, end)); err != nil {
		t.Fatal(err)
	}
	waitRingEnds(t, s, end)
	if l := s.LastLine(); l == "" || strings.Trim(l, drawnRunes) != "" {
		t.Fatalf("last line %q", l)
	}
}
