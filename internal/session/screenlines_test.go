package session

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// waitShot waits until the session's screen has a row that is row: the
// pump has fed it the output that draws it.
func waitShot(t *testing.T, s *Local, row string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		if rows, _ := s.shot.Rows(); slices.Contains(rows, row) {
			return
		}
		if time.Now().After(deadline) {
			rows, _ := s.shot.Rows()
			t.Fatalf("the screen never showed %q: %q", row, rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A program that redraws one row in place for longer than the scrollback
// reads as that one row, not as its frames run together.
func TestScreenLinesOfARowRedrawnInPlace(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	frame := "\r\x1b[38;2;138;138;138;49m•\x1b[0m Working"
	out := strings.Repeat(frame, 4096/len(frame)+10) + "\r\x1b[2K\x1b[1m• done\x1b[0m"
	if _, err := p.outW.Write([]byte(out)); err != nil {
		t.Fatal(err)
	}
	waitShot(t, s, "• done")
	if got := s.ScreenLines(50); !slices.Equal(got, []string{"• done"}) {
		t.Fatalf("the screen reads %q", got)
	}
}

// A full-screen dialog on the alternate screen reads as its rows, the
// blank ones between them kept, the last n of them; asked for none, none.
func TestScreenLinesOfAFullScreenDialog(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	out := "earlier output\r\n\x1b[?1049h\x1b[2J\x1b[H" +
		"\x1b[1;1H╭─ Allow? ─╮\x1b[3;3H\x1b[1m1. Yes\x1b[0m\x1b[4;3H2. No 中\x1b[6;1H╰──────────╯\x1b[3;9H"
	if _, err := p.outW.Write([]byte(out)); err != nil {
		t.Fatal(err)
	}
	waitShot(t, s, "╰──────────╯")
	want := []string{"╭─ Allow? ─╮", "", "  1. Yes", "  2. No 中", "", "╰──────────╯"}
	if got := s.ScreenLines(50); !slices.Equal(got, want) {
		t.Fatalf("the screen reads %q, want %q", got, want)
	}
	if got := s.ScreenLines(3); !slices.Equal(got, want[3:]) {
		t.Fatalf("its last 3 rows read %q", got)
	}
	if got := s.ScreenLines(0); len(got) != 0 {
		t.Fatalf("no rows asked, %q read", got)
	}
}

// During a synchronized update the screen reads as the frame as far as it
// is drawn, and says an update is under way; once it ends, it says none
// is.
func TestScreenReadDuringASynchronizedUpdate(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	reads := func(want string, updating bool) {
		t.Helper()
		if got, u := s.ScreenRead(50); !slices.Equal(got, []string{want}) || u != updating {
			t.Fatalf("the screen reads %q, updating %v; want %q, %v", got, u, want, updating)
		}
	}
	if _, err := p.outW.Write([]byte("\x1b[?2026h\x1b[2J\x1b[Hfirst frame\x1b[?2026l")); err != nil {
		t.Fatal(err)
	}
	waitShot(t, s, "first frame")
	reads("first frame", false)
	if _, err := p.outW.Write([]byte("\x1b[?2026h\x1b[2J\x1b[Hsecond, half")); err != nil {
		t.Fatal(err)
	}
	waitShot(t, s, "second, half")
	reads("second, half", true)
	if _, err := p.outW.Write([]byte(" and whole\x1b[?2026l")); err != nil {
		t.Fatal(err)
	}
	waitShot(t, s, "second, half and whole")
	reads("second, half and whole", false)
}
