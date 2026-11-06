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
// is drawn, and says an update is under way; once it ends, or a full
// reset (RIS) ends it, it says none is.
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
	// A program killed during an update, and a shell that resets the
	// terminal after it.
	if _, err := p.outW.Write([]byte("\x1b[?2026h\x1b[2J\x1b[Hthird, cut")); err != nil {
		t.Fatal(err)
	}
	waitShot(t, s, "third, cut")
	reads("third, cut", true)
	if _, err := p.outW.Write([]byte("\x1bc$ ")); err != nil {
		t.Fatal(err)
	}
	waitShot(t, s, "$")
	reads("$", false)
}

// A concealed character (SGR 8: a prompt that hides what is typed) reads
// as a blank, as a terminal draws it, however much output came after it;
// 28 and 0 end it, a colour's 8 does not begin it, and a saved cursor
// keeps it with the attributes.
func TestScreenLinesHideConcealedText(t *testing.T) {
	s, p := newLocalWith(t, Options{ScrollbackBytes: 4096})
	out := "Password: \x1b[8mhunter2\x1b[0m\r\n" +
		strings.Repeat("\x1b[0m", 2000) + // past the scrollback: only escapes
		"\x1b[38;5;8mgrey\x1b[48;2;8;8;8m on \x1b[38:2::8:8:8mgrey\x1b[0m\r\n" +
		"\x1b[8mA\x1b[28mB\x1b[8mC\x1b[mD\r\n" +
		"\x1b[8m\x1b7\x1b[0m\x1b[5GE\x1b8F\x1b[0m\r\n$ "
	if _, err := p.outW.Write([]byte(out)); err != nil {
		t.Fatal(err)
	}
	waitShot(t, s, "$")
	want := []string{"Password:", "grey on grey", " B D", "    E", "$"}
	if got := s.ScreenLines(50); !slices.Equal(got, want) {
		t.Fatalf("the screen reads %q, want %q", got, want)
	}
}

// The screen model's parser is the page's terminal's (xterm.js 6.0.0,
// its VT500 table): what a simpler one gets wrong reads as the page draws
// it (#695's reviews).
func TestScreenParsesAsThePageDoes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x1bP1$rAAAA\x07BBBB;38mXX\x1b\\ok", "ok"}, // BEL is a DCS's data
		{"\x1b_Ga=T\x07QUJD;9mXX\x1b\\ok", "ok"},     // and nothing in an APC
		{"\x1bXsos\x07x\x1b\\ok", "ok"},              // or an SOS
		{"\x1b]0;title\x07ok", "ok"},                 // but ends an OSC
		{"\u009b38;2;1;2;3mok", "ok"},                // C1 CSI
		{"\u009d0;title\u009cok", "ok"},              // C1 OSC and ST
		{"\u0090+q544e\u009cok", "ok"},               // C1 DCS
		{"\u009fGa=T\u009cok", "ok"},                 // C1 APC
		{"\x1b]0;ti\u0085ok", "\nok"},                // a C1 control cancels a string and acts
		{"a\u0084b", "a\n b"},                        // IND
		{"\x1b\r[31mok", "ok"},                       // a C0 control after ESC acts; the escape goes on
		{"\x1b(\r0ok", "ok"},                         // and after its intermediate
		{"\x1b]0;ti\x18tle", "tle"},                  // CAN cancels a string
		{"\x1b[3\x1amok", "mok"},                     // SUB a sequence
		{"\x1b_Gab\u00e9ok", "ok"},                   // a character ends an APC, dropped
		{"\x1b[1\u00e9ok", "ok"},                     // and a sequence
		{"\x1b[1>\u00e9;3mok", "ok"},                 // a private marker after a parameter: ignored to its end
		{"\x1b[>1;2\u00e9;3mok", ";3mok"},            // a character ends a sequence's parameters
		{"\x1bP1;2\u00e9ok", "ok"},                   // a DCS before its final byte
		{"\x1bP1$q\u00e9x\x1b\\ok", "ok"},            // its data holds characters
		{"\x1b\x1b[?1049hALT\x1b[?1049lok", "ok"},    // ESC starts over
		{"\x1b[?2026h\x1bc", ""},                     // RIS
	} {
		sc := NewScreen(80, 24)
		sc.Write([]byte(tc.in))
		if got := sc.Text(); got != tc.want {
			t.Errorf("%q reads %q, want %q", tc.in, got, tc.want)
		}
	}
	// A full reset ends a synchronized update, as the page's does.
	sc := NewScreen(80, 24)
	sc.Write([]byte("\x1b[?2026hhalf\x1bc"))
	if !sc.Drawn() {
		t.Error("a reset left an update under way")
	}
}
