package session

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Codex 0.161's window title with the override Conductor's launch passes,
// tui.terminal_title=["activity","run-state"], recorded in a PTY on
// 2026-10-14 (docs/plans/codex-needs-you.md): ESC ] 0 ; <title> BEL, "Ready"
// at rest, a braille frame and "Starting" while its MCP servers start, a
// frame and "Working" through the turn, the frame changing every 100 ms,
// then "Ready". The bytes are the recording's; "[ ! ] Action Required", its
// blink and the still "Working" (animations off) are the source's
// (tui/src/chatwidget/status_surfaces.rs), not seen.
const (
	codexReady       = "\x1b]0;Ready\x07"
	codexStarting    = "\x1b]0;\xe2\xa0\xb8 Starting\x07"
	codexAction      = "\x1b]0;[ ! ] Action Required\x07"
	codexActionBlink = "\x1b]0;[ . ] Action Required\x07"
	codexStill       = "\x1b]0;Working\x07"
	// codexRedraw is a synchronized update Codex makes between its frames
	// that changes nothing on the screen.
	codexRedraw = "\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[37;3H\x1b[?25h\x1b[?2026l"
	// codexTurnBegins is the read in which the recording's turn began: the
	// title says Working, the status row too.
	codexTurnBegins = "\x1b[?2026l\x1b]0;\xe2\xa0\x99 Working\x07\x1b[?2026h\x1b[?25l\x1b[34;34H\x1b[0m\x1b[49m\x1b[K\x1b[34;3H" +
		"\x1b[38;2;126;126;129;49mWorking\x1b[39;49m \x1b[2m(2s \xe2\x80\xa2 \x1b[22m\x1b[1m\x1b[38;2;228;228;231;49mesc\x1b[22m" +
		"\x1b[2m\x1b[2m\x1b[39;49m to interrupt)\x1b[39m\x1b[49m\x1b[0m\x1b[37;3H\x1b[?25h\x1b[?2026l"
)

// codexFrames are the spinner's frames in the order the recording has them.
var codexFrames = []string{"\xe2\xa0\x99", "\xe2\xa0\xb9", "\xe2\xa0\xb8", "\xe2\xa0\xbc", "\xe2\xa0\xb4", "\xe2\xa0\xa6", "\xe2\xa0\xa7", "\xe2\xa0\x87", "\xe2\xa0\x8f", "\xe2\xa0\x8b"}

// codexWorking is the title of frame i of a turn: "⠙ Working" and on.
func codexWorking(i int) string {
	return "\x1b]0;" + codexFrames[i%len(codexFrames)] + " Working\x07"
}

// titled is Options for a Codex session under test: its working title (the
// built-in's), its approvals, and settle in place of TitleSettle.
func titled(settle time.Duration, o Options) Options {
	o.WorkingTitle = regexp.MustCompile(catalog.CodexWorkingTitle)
	o.TitleSettle = settle
	if o.Questions == nil {
		o.Questions = approvalQuestions()
	}
	return quiet(o)
}

// out writes each of what as a read of its own and returns once the session
// has scanned them all.
func out(p *fakeProc, what ...string) {
	for _, w := range what {
		p.outW.Write([]byte(w))
	}
	flushed(p)
}

// titleWorks reports whether the session shows working from its title.
func titleWorks(a Attention) bool { return a.State == AttentionWorking && a.Source == SourceTitle }

// waits reports whether the session needs input.
func waits(a Attention) bool { return a.State == AttentionNeedsInput }

// stays fails unless ok accepts the session's attention all through d.
func stays(t *testing.T, s *Local, d time.Duration, what string, ok func(Attention) bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if a := s.Info().Attention; !ok(a) {
			t.Fatalf("%s: attention %+v", what, a)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// frames writes Codex's working titles, each a read of its own with a redraw
// after it, every 50 ms until stop is closed; the returned channel closes
// once it has stopped.
func frames(p *fakeProc, stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			if _, err := p.outW.Write([]byte(codexWorking(i))); err != nil {
				return
			}
			if _, err := p.outW.Write([]byte(codexRedraw)); err != nil {
				return
			}
		}
	}()
	return done
}

// entries are the session's attention entries' messages, oldest first.
func entries(s *Local) []string {
	var out []string
	for _, e := range s.Activity() {
		if e.Type == ActivityAttention {
			out = append(out, e.Message)
		}
	}
	return out
}

// The scanner reports a window title, OSC 0 or 2, ended by BEL or ST, as a
// title event with its text, control characters dropped; one split between
// two reads once whole, carried; one longer than its capture with no text,
// which no working title matches. The BEL that ends it is no bell, and OSC 1
// (the icon's name alone) is no title.
func TestTheScannerReportsWindowTitles(t *testing.T) {
	var sc Scanner
	got := sc.Scan([]byte(codexTurnBegins + codexRedraw + codexWorking(1) + "\x1b]2;Ready\x1b\\" + "\x1b]1;icon\x07"))
	want := []Event{{Source: SourceTitle, Message: "\xe2\xa0\x99 Working"}, {Source: SourceTitle, Message: "\xe2\xa0\xb9 Working"}, {Source: SourceTitle, Message: "Ready"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %q, want %q", got, want)
	}
	if got := sc.Scan([]byte("\x1b]0;\xe2\xa0")); len(got) != 0 {
		t.Fatalf("half a title: %q", got)
	}
	if got := sc.Scan([]byte("\x99 Working\x07\x07")); len(got) != 2 || got[0] != (Event{Source: SourceTitle, Message: "\xe2\xa0\x99 Working"}) || !sc.Carried() || got[1].Source != SourceBell {
		t.Fatalf("the rest of a title, then a bell: %q (carried %v)", got, sc.Carried())
	}
	if got := sc.Scan([]byte("\x1b]0;\tWor\x01king\x7f\u009c\x07")); len(got) != 1 || got[0].Message != "Working" {
		t.Fatalf("a title with control characters: %q", got)
	}
	long := "\x1b]0;" + strings.Repeat("Working ", 40) + "Working\x07"
	if got := sc.Scan([]byte(long)); len(got) != 1 || got[0] != (Event{Source: SourceTitle}) {
		t.Fatalf("a title past the capture: %q", got)
	}
	if got := sc.Scan([]byte(codexAction + codexActionBlink + codexStarting + codexReady)); len(got) != 4 {
		t.Fatalf("Codex's other titles: %q", got)
	}
}

// A title that says Codex works shows working (source title) once, however
// many frames follow: one entry, no token of the event bucket spent, no
// change for the same state. A title that does not match changes nothing:
// neither Ready nor Action Required raises anything by itself, and Ready
// after a done leaves it done (O11, O14).
func TestATitleThatSaysCodexWorksShowsWorkingOnce(t *testing.T) {
	s, p := newLocalWith(t, titled(time.Minute, Options{}))
	out(p, codexReady, codexStarting, codexRedraw)
	if a := s.Info().Attention; a.State != AttentionNone {
		t.Fatalf("Ready and Starting showed %+v", a)
	}
	out(p, codexTurnBegins)
	if a := s.Info().Attention; !titleWorks(a) || a.Message != "" {
		t.Fatalf("the turn's first title showed %+v", a)
	}
	for i := 1; i <= 2*EventBurst; i++ {
		p.outW.Write([]byte(codexWorking(i) + codexRedraw))
	}
	out(p, codexReady, codexAction, codexActionBlink)
	if a := s.Info().Attention; !titleWorks(a) {
		t.Fatalf("Ready or Action Required changed working: %+v", a)
	}
	if got := entries(s); len(got) != 1 || got[0] != "working" {
		t.Fatalf("entries %q, want one working", got)
	}
	// The frames spent nothing: a burst of reports goes through whole.
	for i := range EventBurst {
		if err := s.TrySetAttentionFull(AttentionWorking, fmt.Sprint("report ", i), SourceAPI, "", nil); err != nil {
			t.Fatalf("report %d after the frames: %v", i, err)
		}
	}
	if s.Dropped() != 0 {
		t.Fatalf("dropped %d", s.Dropped())
	}
	// A working report shows already: the title changes nothing.
	out(p, codexWorking(3))
	if a := s.Info().Attention; a.Source != SourceAPI {
		t.Fatalf("the title replaced the agent's own working report: %+v", a)
	}
	// The bucket is empty now: the done is one a host's server has paid for.
	s.SetAttentionFull(AttentionDone, "ok", SourceAPI, KindDone, nil)
	out(p, codexReady)
	if a := s.Info().Attention; a.State != AttentionDone {
		t.Fatalf("Ready after the done: %+v", a)
	}
}

// Without a working title (an agent whose catalog has none, a shell), a
// title saying Working is nobody's state: nothing changes, and the bell
// still asks (O15).
func TestATitleIsNotWeighedWithoutAWorkingTitle(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	out(p, codexTurnBegins, codexWorking(1), codexStill)
	if a := s.Info().Attention; a.State != AttentionNone {
		t.Fatalf("a title showed %+v", a)
	}
	out(p, "\a")
	if a := s.Info().Attention; !isBell(a) {
		t.Fatalf("the bell after the titles: %+v", a)
	}
	out(p, codexWorking(2))
	if a := s.Info().Attention; !isBell(a) {
		t.Fatalf("a title cleared the bell: %+v", a)
	}
}

// A wait raised while the title says Codex works gives way to working once
// its settle has passed, whether frames keep coming or the title was written
// once (animations off), and frames within the settle change nothing: the
// agent's own question (ask), the bell of a question Codex goes on from, an
// OSC notification (O3, O4, O16). The question stays in the chat and the
// activity log; the bell is recorded once, the burst after it paced.
func TestAWaitRaisedWhileCodexWorksGivesWay(t *testing.T) {
	raises := map[string]func(s *Local, p *fakeProc){
		"the agent's question": func(s *Local, p *fakeProc) {
			if err := s.TrySetAttentionFull(AttentionNeedsInput, "Keep items in ~/.todo.json?", SourceAPI, "", nil); err != nil {
				panic(err)
			}
		},
		"an async question's bell": func(s *Local, p *fakeProc) {
			out(p, "\a\x1b[22;1H  ? 1 question \xc2\xb7 4:59")
			p.outW.Write([]byte("\a"))
		},
		"an OSC notification": func(s *Local, p *fakeProc) { out(p, "\x1b]9;Codex asks a question\x07") },
	}
	for name, raise := range raises {
		for _, still := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, still %v", name, still), func(t *testing.T) {
				t.Parallel()
				const settle = 200 * time.Millisecond
				s, p := newLocalWith(t, titled(settle, Options{}))
				out(p, codexTurnBegins)
				var stop chan struct{}
				if still {
					out(p, codexStill)
				} else {
					stop = make(chan struct{})
					defer func() { close(stop) }()
				}
				raised := time.Now()
				raise(s, p)
				asked := waitAttention(t, s, waits)
				if !still {
					frames(p, stop)
				}
				waitAttention(t, s, titleWorks)
				if d := time.Since(raised); d < settle {
					t.Fatalf("the wait gave way within %v, before its settle", d)
				}
				// The attention reads as working before the entry that records
				// the change is in the log: wait for the third entry, within
				// the usual bound, rather than read once (#247).
				var got []string
				for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
					if got = entries(s); len(got) >= 3 || time.Now().After(deadline) {
						break
					}
				}
				if len(got) != 3 || got[0] != "working" || got[1] != asked.Message || got[2] != "working" {
					t.Fatalf("entries %q", got)
				}
				if name == "the agent's question" {
					// Asked in the chat; a question the wait's end let go for good
					// has its withdrawn line after it (#358), one kept aside none.
					chat := s.ChatHistory()
					var found bool
					for _, m := range chat {
						found = found || (m.Kind == proto.ChatKindQuestion && m.Text == asked.Message)
					}
					if !found {
						t.Fatalf("the chat %+v", chat)
					}
				}
			})
		}
	}
}

// A frame drawn while a wait is younger than its settle changes nothing,
// whatever the title said before it: the settle timer decides that wait
// (O2, O3, O21).
func TestAFrameWithinTheSettleChangesNothing(t *testing.T) {
	for _, before := range []string{codexTurnBegins, codexReady} {
		s, p := newLocalWith(t, titled(time.Minute, Options{}))
		out(p, before)
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which file?", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		out(p, codexWorking(1), codexRedraw, codexWorking(2), codexStill)
		if a := s.Info().Attention; !waits(a) || a.Message != "Which file?" {
			t.Fatalf("after %q, a frame within the settle: %+v", before, a)
		}
		out(p, "\a") // a bell at a wait says nothing more
		out(p, codexWorking(3))
		if a := s.Info().Attention; !waits(a) || a.Message != "Which file?" {
			t.Fatalf("after %q, the bell and a frame: %+v", before, a)
		}
	}
}

// The two reproductions of #186, with the real settle: a Codex member's
// prompt entered, Codex at work, then the bell of a question it goes on
// from, or its own question asked through the API, and 5 s of Codex at
// work. The session shows working at the end, once with the spinner's
// frames through the 5 s, once with one title "Working" (animations off)
// and its status row redrawn with no title.
func TestTheReproductionsOf186EndWorking(t *testing.T) {
	for _, c := range []struct {
		name  string
		still bool
		raise func(s *Local, p *fakeProc)
	}{
		{"the async question's bell", false, func(s *Local, p *fakeProc) {
			p.outW.Write([]byte("\a\x1b[22;1H  ? 1 question \xc2\xb7 4:59\x1b[23;1H    ctrl+o to answer"))
		}},
		{"the agent's own ask", false, func(s *Local, p *fakeProc) {
			s.TrySetAttentionFull(AttentionNeedsInput, "Keep items in ~/.todo.json or $XDG_DATA_HOME?", SourceAPI, "", nil)
		}},
		{"the async question's bell, the title still", true, func(s *Local, p *fakeProc) { p.outW.Write([]byte("\a\x1b[22;1H  ? 1 question \xc2\xb7 4:59")) }},
		{"the agent's own ask, the title still", true, func(s *Local, p *fakeProc) {
			s.TrySetAttentionFull(AttentionNeedsInput, "Keep items in ~/.todo.json or $XDG_DATA_HOME?", SourceAPI, "", nil)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, p := newLocalWith(t, titled(0, Options{}))
			if res, err := s.Submit(t.Context(), Submission{Text: "Start by merging the lead's and cli's branches", ByName: "crew"}); err != nil || !res.Entered {
				t.Fatalf("the prompt: %+v %v", res, err)
			}
			nextWrite(t, p)
			nextWrite(t, p)
			// Codex at work: its status row and a command's output redrawn
			// every 100 ms, with the title's frame, or with the title
			// written once.
			work := func(d time.Duration) {
				for i := 0; time.Duration(i)*100*time.Millisecond < d; i++ {
					row := fmt.Sprintf("\x1b[10;1H$ go test ./... (%d)\x1b[K\x1b[20;1H\xe2\x80\xa2 Working (1m %02ds \xe2\x80\xa2 esc to interrupt)\x1b[K", i, i/10)
					if !c.still {
						row += codexWorking(i)
					}
					p.outW.Write([]byte(row))
					time.Sleep(100 * time.Millisecond)
				}
			}
			if c.still {
				out(p, codexTurnBegins)
			}
			work(time.Second)
			c.raise(s, p)
			waitAttention(t, s, waits)
			work(5 * time.Second)
			if a := s.Info().Attention; !titleWorks(a) {
				t.Fatalf("Codex went on for 5 s and the session shows %+v", a)
			}
		})
	}
}

// Codex rings the bell for an approval as it draws the dialog, and its
// title then says Action Required, which blinks: the bell stands past the
// settle, and the dialog is asked once the screen is quiet, the blinking
// title (a read that only sets the title) not breaking the quiet; a frame
// drawn between the bell and the dialog, the title said Working before or
// not, changes nothing (O1, O2, O17, E5).
func TestAnApprovalsBellStandsUntilItsDialogIsAsked(t *testing.T) {
	for _, c := range []struct {
		name   string
		before string
		stray  bool
	}{
		{"the bell and the dialog together", codexTurnBegins, false},
		{"a frame between the bell and the dialog", codexTurnBegins, true},
		{"a frame between, the title Ready before", codexReady, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			const settle = 200 * time.Millisecond
			s, p := newLocalWith(t, titled(settle, Options{}))
			out(p, c.before)
			if c.stray {
				out(p, "\a")
				out(p, codexWorking(4))
				p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
			} else {
				p.outW.Write([]byte("\a" + approvalDialog("curl -sI https://example.com")))
			}
			p.outW.Write([]byte(codexAction))
			if a := waitAttention(t, s, waits); a.Source != SourceBell {
				t.Fatalf("the approval's bell: %+v", a)
			}
			// Action Required blinks every second (here every 100 ms): only
			// titles, and the dialog is asked all the same.
			stop := make(chan struct{})
			blinked := make(chan struct{})
			go func() {
				defer close(blinked)
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					case <-time.After(100 * time.Millisecond):
					}
					title := codexAction
					if i%2 == 0 {
						title = codexActionBlink
					}
					if _, err := p.outW.Write([]byte(title)); err != nil {
						return
					}
				}
			}()
			defer func() {
				close(stop)
				<-blinked
			}()
			a := waitAttention(t, s, func(a Attention) bool { return a.State != AttentionNeedsInput || a.Source != SourceBell })
			if !asked(a) || a.Message != approvalWords {
				t.Fatalf("the bell gave way to %+v", a)
			}
			stays(t, s, 2*settle, "the approval with the title blinking", asked)
			for _, e := range entries(s)[1:] {
				if e == "working" {
					t.Fatalf("the approval was cleared on the way: %q", entries(s))
				}
			}
		})
	}
}

// A question the session holds is the screen's: a title saying Working
// leaves Codex's approval and its trust question showing, and the trust
// question is asked although the title's frames come every 100 ms; once
// the approval is answered, the next frame shows working (O5, O6, E5).
func TestATitleLeavesAQuestionOnTheScreen(t *testing.T) {
	const settle = 100 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	out(p, codexTurnBegins)
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	waitAttention(t, s, asked)
	stop := make(chan struct{})
	done := frames(p, stop)
	stays(t, s, 4*settle, "the approval under Working titles", asked)
	close(stop)
	<-done
	if err := s.Input(sub, []byte("y")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if a := s.Info().Attention; !asked(a) {
		t.Fatalf("the key cleared the dialog before the screen moved on: %+v", a)
	}
	out(p, approvalGone("\x1b[12;1H\xe2\x80\xa2 Ran curl -sI https://example.com"), codexWorking(5))
	// The dialog answered is withdrawn by the first whole frame that no
	// longer shows it, quiet or not (#288); the next frame shows working.
	waitAttention(t, s, func(a Attention) bool { return !asked(a) })
	out(p, codexWorking(6))
	if a := s.Info().Attention; !titleWorks(a) {
		t.Fatalf("a frame after the answer: %+v", a)
	}

	trust, tp := newLocalWith(t, titled(settle, Options{TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`)}))
	tp.outW.Write([]byte("Trust this folder?\r\n\xe2\x80\xba 1. Trust and continue\r\n  2. Quit\r\n"))
	// Codex's MCP servers start behind the question: Starting frames, then
	// a frame saying Working; reads that set only the title.
	stop = make(chan struct{})
	started := make(chan struct{})
	go func() {
		defer close(started)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			title := codexStarting
			if i%3 == 2 {
				title = codexWorking(i)
			}
			if _, err := tp.outW.Write([]byte(title)); err != nil {
				return
			}
		}
	}()
	defer func() {
		close(stop)
		<-started
	}()
	if a := waitAttention(t, trust, waits); a.Source != SourceTrust {
		t.Fatalf("the trust question behind the titles: %+v", a)
	}
	stays(t, trust, 4*settle, "the trust question under the titles", func(a Attention) bool { return a.Source == SourceTrust })
}

// While the session's turn is held for a typed text, a title shows nothing
// and records nothing, nor does the settle timer of a wait the hold took the
// place of; once keys end the hold, the question shows again, and gives way
// to working once its settle has passed (O7, O20).
func TestATitleNeverShowsOverAHeldTurn(t *testing.T) {
	const settle = 150 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	out(p, codexStill)
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	go holdNow(s)
	heldShows(t, s)
	// The hold's own entry lands after the hold shows (recordOwn): counted
	// before it, it would read as a title recorded during the hold.
	lastEntryIs(t, s, HeldWords)
	before := len(entries(s))
	stop := make(chan struct{})
	done := frames(p, stop)
	stays(t, s, 4*settle, "the hold under Working titles", func(a Attention) bool { return a.Source == SourceSubmit })
	close(stop)
	<-done
	if got := entries(s); len(got) != before {
		t.Fatalf("the titles recorded %q during the hold", got[before:])
	}
	// Keys that do not answer the question end the hold: it shows again,
	// its settle running from then.
	ended := time.Now()
	if err := s.Input(sub, []byte("\x15")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	waitAttention(t, s, func(a Attention) bool { return a.Message == "Which name?" })
	out(p, codexWorking(1))
	waitAttention(t, s, titleWorks)
	if d := time.Since(ended); d < settle {
		t.Fatalf("working within %v of the hold's end", d)
	}
}

// A done is not undone by a title read after it (a frame drawn before the
// turn ended, Codex going on by itself), however long the frames go on,
// until the next Enter; then a frame shows working. Working then a done
// ends done, and the turn's bell after it leaves it so. A done inside a
// wait's settle wins in either order (O8, O10, O18).
func TestATitleDoesNotUndoItsTurnsDone(t *testing.T) {
	const settle = 150 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	out(p, codexTurnBegins)
	if err := s.TrySetAttentionFull(AttentionDone, "ok", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := frames(p, stop)
	stays(t, s, 3*settle, "the done under Working titles", func(a Attention) bool { return a.State == AttentionDone })
	close(stop)
	<-done
	out(p, codexReady, "\a")
	if a := s.Info().Attention; a.State != AttentionDone {
		t.Fatalf("Ready and the turn's bell: %+v", a)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	out(p, codexWorking(2))
	if a := s.Info().Attention; !titleWorks(a) {
		t.Fatalf("a frame after the next Enter: %+v", a)
	}

	// The done within the settle of a wait raised while the title said Working.
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.TrySetAttentionFull(AttentionDone, "named", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	stays(t, s, 3*settle, "a done within the settle", func(a Attention) bool { return a.State == AttentionDone && a.Message == "named" })
	// The other order: the settle passes, then the done.
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	out(p, codexStill)
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which file?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	waitAttention(t, s, titleWorks)
	if err := s.TrySetAttentionFull(AttentionDone, "filed", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	stays(t, s, 3*settle, "the done after the settle", func(a Attention) bool { return a.State == AttentionDone && a.Message == "filed" })
}

// A title read during the ended turn and looked at once the next has begun
// (a done and an Enter between the read and the look) is that turn's own:
// nothing; so is a title split between two reads whose first part was read
// then. A title split between two reads in one turn is applied once whole
// (O9, O12).
func TestATitleReadInTheEndedTurnIsItsOwn(t *testing.T) {
	s, p := newLocalWith(t, titled(time.Minute, Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	read, look := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func(chunk []byte) {
		if bytes.Contains(chunk, []byte("Working")) {
			once.Do(func() {
				close(read)
				<-look
			})
		}
	}
	s.beforeScan.Store(&hook)
	go p.outW.Write([]byte(codexWorking(7)))
	<-read
	if err := s.TrySetAttentionFull(AttentionDone, "ok", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	close(look)
	flushed(p)
	if a := s.Info().Attention; a.State != AttentionDone {
		t.Fatalf("the ended turn's title showed %+v in the next", a)
	}
	// Split in one turn: applied once whole.
	p.outW.Write([]byte("\x1b]0;\xe2\xa0"))
	out(p, "\x99 Working\x07")
	if a := s.Info().Attention; !titleWorks(a) {
		t.Fatalf("a title split between two reads: %+v", a)
	}
	// Split across the turn's end: its first part read before the done and
	// the Enter, held as soon as it is read.
	read, look = make(chan struct{}), make(chan struct{})
	var again sync.Once
	// A hook of its own: the pump reads the one stored through its pointer,
	// so assigning the first variable again would race its read.
	heldHook := func(chunk []byte) {
		if string(chunk) == "\x1b]0;\xe2\xa0" {
			again.Do(func() {
				close(read)
				<-look
			})
		}
	}
	s.beforeScan.Store(&heldHook)
	go p.outW.Write([]byte("\x1b]0;\xe2\xa0"))
	<-read
	if err := s.TrySetAttentionFull(AttentionDone, "again", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	close(look)
	out(p, "\xb9 Working\x07")
	if a := s.Info().Attention; a.State != AttentionDone || a.Message != "again" {
		t.Fatalf("a title begun in the ended turn: %+v", a)
	}
	// The next title, the next turn's own, shows working.
	out(p, codexWorking(2))
	if a := s.Info().Attention; !titleWorks(a) {
		t.Fatalf("the next turn's title: %+v", a)
	}
}

// A session ends with the empty state, a title in its last output or a
// settle timer under way: nothing shows after its end (O13, O20).
func TestATitleLeavesNothingOnAnEndedSession(t *testing.T) {
	const settle = 150 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	p.outW.Write([]byte(codexTurnBegins + codexWorking(1)))
	p.exit()
	<-s.Ended()
	if a := s.Info().Attention; a.State != AttentionNone {
		t.Fatalf("the ended session shows %+v", a)
	}

	s2, p2 := newLocalWith(t, titled(settle, Options{}))
	out(p2, codexStill)
	if err := s2.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	p2.exit()
	<-s2.Ended()
	stays(t, s2, 3*settle, "the ended session past the settle", func(a Attention) bool { return a.State == AttentionNone })
	got := s2.Activity()
	if last := got[len(got)-1]; last.Type != ActivityStatus {
		t.Fatalf("recorded after the end: %+v", last)
	}
}

// The settle timer acts on the wait it was armed for alone: answered by a
// person's keys before it fires, the session stays answered; replaced by
// another wait, that one gives way only once its own settle has passed
// (O19).
func TestTheSettleTimerActsOnlyOnItsWait(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	out(p, codexStill)
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AnswerInput(t.Context(), sub, s.Info().Attention.ID, []byte("x")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	stays(t, s, 3*settle, "answered before the settle", func(a Attention) bool { return a.State == AttentionNone && a.Source == SourceInput })

	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which file?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(settle / 2)
	second := time.Now()
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which line?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	waitAttention(t, s, titleWorks)
	if d := time.Since(second); d < settle {
		t.Fatalf("the second wait gave way within %v", d)
	}
}

// A wait raised while the title says Ready, then one title saying Working
// within its settle and none after (animations off): the title arms the
// timer for the rest of the settle, and the wait gives way then. Once the
// settle has passed, such a title shows working at once (O21).
func TestOneWorkingTitleAfterAWaitSettlesIt(t *testing.T) {
	const settle = 200 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexReady)
	raised := time.Now()
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	out(p, codexStill)
	waitAttention(t, s, titleWorks)
	if d := time.Since(raised); d < settle {
		t.Fatalf("the wait gave way within %v", d)
	}

	out(p, codexReady)
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which file?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(settle + 50*time.Millisecond)
	if a := s.Info().Attention; !waits(a) {
		t.Fatalf("the wait with the title Ready: %+v", a)
	}
	out(p, codexStill)
	if a := s.Info().Attention; !titleWorks(a) {
		t.Fatalf("a Working title after the settle: %+v", a)
	}
}

// While one of Codex's async questions is pending its title says Action
// Required though Codex works on: a wait raised then stands, past its
// settle, until the title says Working again; then it gives way at once
// when it is older than its settle, else when its settle ends (O22).
func TestActionRequiredHoldsAWaitUntilTheTitleSaysWorking(t *testing.T) {
	const settle = 200 * time.Millisecond
	for _, late := range []bool{true, false} {
		s, p := newLocalWith(t, titled(settle, Options{}))
		out(p, codexTurnBegins, codexAction)
		raised := time.Now()
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		if late {
			stays(t, s, settle+100*time.Millisecond, "the wait under Action Required", waits)
			out(p, codexWorking(1))
			if a := s.Info().Attention; !titleWorks(a) {
				t.Fatalf("Working after the async question resolved: %+v", a)
			}
			continue
		}
		out(p, codexWorking(1))
		waitAttention(t, s, titleWorks)
		if d := time.Since(raised); d < settle {
			t.Fatalf("the wait gave way within %v", d)
		}
	}
}

// A title asks the event bucket for nothing: with the agent's reports
// refused (the bucket empty), a title saying Working still shows working.
func TestATitleSpendsNoTokenWhenTheBucketIsEmpty(t *testing.T) {
	s, p := newLocalWith(t, titled(time.Minute, Options{}))
	var refused bool
	for range 4 * EventBurst {
		if err := s.TrySetAttentionFull(AttentionDone, "spent", SourceAPI, KindDone, nil); errors.Is(err, ErrRateLimited) {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("the bucket never emptied")
	}
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	out(p, codexWorking(0))
	if a := s.Info().Attention; !titleWorks(a) {
		t.Fatalf("a title with the bucket empty: %+v", a)
	}
}

// A title read as the settle timer fires counts: Action Required read
// between the timer's fire and its change leaves the wait standing, since
// the timer weighs the last title under the lock that applies its change
// (Codex's review, round 1).
func TestATitleReadAsTheSettleFiresCounts(t *testing.T) {
	const settle = 150 * time.Millisecond
	s, p := newLocalWith(t, titled(settle, Options{}))
	out(p, codexStill)
	fired := make(chan struct{})
	var once sync.Once
	hook := func() {
		once.Do(func() {
			out(p, codexAction)
			close(fired)
		})
	}
	s.beforeSettle.Store(&hook)
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Allow the form?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("the settle timer never fired")
	}
	stays(t, s, 3*settle, "Action Required read as the timer fired", func(a Attention) bool { return waits(a) && a.Message == "Allow the form?" })
}

// A title saying Working read during the ended turn and scanned once the
// next has begun arms nothing for the next turn's wait, whole or split
// between two reads: the wait stands past its settle, and the next turn's
// own title then shows working (O9, O12; Codex's review, round 1).
func TestATitleOfTheEndedTurnArmsNothingForTheNext(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprintf("split %v", split), func(t *testing.T) {
			const settle = 150 * time.Millisecond
			s, p := newLocalWith(t, titled(settle, Options{}))
			sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			out(p, codexReady)
			first := codexWorking(1)
			if split {
				first = "\x1b]0;\xe2\xa0"
			}
			read, look := make(chan struct{}), make(chan struct{})
			var once sync.Once
			hook := func(chunk []byte) {
				if string(chunk) == first {
					once.Do(func() {
						close(read)
						<-look
					})
				}
			}
			s.beforeScan.Store(&hook)
			go p.outW.Write([]byte(first))
			<-read
			if err := s.TrySetAttentionFull(AttentionDone, "ok", SourceAPI, KindDone, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.Input(sub, []byte("\r")); err != nil {
				t.Fatal(err)
			}
			nextWrite(t, p)
			if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
				t.Fatal(err)
			}
			close(look)
			if split {
				out(p, "\x99 Working\x07")
			} else {
				flushed(p)
			}
			stays(t, s, 3*settle, "the next turn's wait after the ended turn's title", func(a Attention) bool { return waits(a) && a.Message == "Which name?" })
			out(p, codexWorking(2))
			if a := s.Info().Attention; !titleWorks(a) {
				t.Fatalf("the next turn's own title: %+v", a)
			}
		})
	}
}

// The last title the session keeps is its turn's: a title saying Working
// read after the turn's done (a frame drawn before it, or Codex going on by
// itself), whether its own change is refused for the done or for a turn
// begun meanwhile, does not settle the next turn's wait once an Enter has
// begun it; that turn's own title does (O8, O9; Codex's review, round 2).
func TestTheLastTitleIsItsTurns(t *testing.T) {
	const settle = 150 * time.Millisecond
	for _, between := range []bool{false, true} {
		t.Run(fmt.Sprintf("the turn begun between the title's looks %v", between), func(t *testing.T) {
			s, p := newLocalWith(t, titled(settle, Options{}))
			sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			out(p, codexReady)
			if err := s.TrySetAttentionFull(AttentionDone, "ok", SourceAPI, KindDone, nil); err != nil {
				t.Fatal(err)
			}
			enter := func() {
				if err := s.Input(sub, []byte("\r")); err != nil {
					t.Error(err)
				}
				nextWrite(t, p)
			}
			if between {
				// The Enter lands after the title is kept and before its
				// change is weighed.
				var once sync.Once
				hook := func() { once.Do(enter) }
				s.afterTitle.Store(&hook)
				out(p, codexStill)
			} else {
				out(p, codexStill)
				if a := s.Info().Attention; a.State != AttentionDone {
					t.Fatalf("a title after the done: %+v", a)
				}
				enter()
			}
			if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which name?", SourceAPI, "", nil); err != nil {
				t.Fatal(err)
			}
			stays(t, s, 3*settle, "the next turn's wait after the ended turn's title", func(a Attention) bool { return waits(a) && a.Message == "Which name?" })
			out(p, codexReady, codexStill)
			if a := s.Info().Attention; !titleWorks(a) {
				t.Fatalf("the next turn's own title: %+v", a)
			}
		})
	}
}
