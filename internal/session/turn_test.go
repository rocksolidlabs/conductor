package session

import (
	"bytes"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// bellAfter writes a bell past the burst limit of the one before and
// returns the attention once the session has read it.
func bellAfter(t *testing.T, s *Local, p *fakeProc, what string) Attention {
	t.Helper()
	time.Sleep(600 * time.Millisecond)
	p.outW.Write([]byte(what))
	time.Sleep(200 * time.Millisecond)
	return s.Info().Attention
}

// Codex reports its turn done (notify) and then rings the bell Conductor
// asks it for (rc.7 app, 2026-10-11): the bell is the ended turn's own, and
// the member stays done instead of waiting on nothing as "terminal bell".
// So is an OSC notification; so is a bell after keys that start nothing.
// The same done twice (a turn ending with the same words) ends its turn as
// well.
func TestADoneIsNotUndoneByItsTurnsBell(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TrySetAttentionFull(AttentionDone, "HTTP/2 200", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if a := bellAfter(t, s, p, "\a"); a.State != AttentionDone || a.Message != "HTTP/2 200" {
		t.Fatalf("the turn's bell undid its done: %+v", a)
	}
	if a := bellAfter(t, s, p, "\x1b]9;Agent turn complete\a"); a.State != AttentionDone {
		t.Fatalf("the turn's notification undid its done: %+v", a)
	}
	s.Input(sub, []byte("x")) // a key, no Enter: no new turn
	nextWrite(t, p)
	if a := bellAfter(t, s, p, "\a"); a.State != AttentionDone {
		t.Fatalf("a bell after a key undid the done: %+v", a)
	}
	// The Enter begins the next turn: the done gives way to the empty state.
	first := s.Info().Attention
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if a := s.Info().Attention; a.State != AttentionNone || a.Source != SourceInput {
		t.Fatalf("the Enter after the done: %+v", a)
	}
	// The next turn ends with the same words: its done shows anew, with its
	// own id and a Since past the first's, and ends that turn.
	if err := s.TrySetAttentionFull(AttentionDone, "HTTP/2 200", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	second := s.Info().Attention
	if second.State != AttentionDone || second.ID == first.ID || second.Since.Before(*first.Since) {
		t.Fatalf("the second turn's done %+v after %+v", second, first)
	}
	if a := bellAfter(t, s, p, "\a"); a.State != AttentionDone {
		t.Fatalf("the second turn's bell undid its done: %+v", a)
	}
}

// doneEntries returns the attention entries of the activity log whose
// message is one of words, oldest first.
func doneEntries(s *Local, words ...string) []ActivityEntry {
	var out []ActivityEntry
	for _, e := range s.Activity() {
		if e.Type == ActivityAttention && slices.Contains(words, e.Message) {
			out = append(out, e)
		}
	}
	return out
}

// An Enter after a done begins the next turn, and the listing says so: the
// done gives way to the empty state with source input, told to the
// viewers as an attention message, and nothing is recorded (no entry, no
// webhook, no turn's end for a crew). The same for a submission's Enter
// (a crew's next prompt). The agents that report only their done (Amp,
// Pi) read done through their whole next turn otherwise.
func TestAnEnterAfterADoneBeginsTheTurnShown(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	sink := newChanSink(false)
	sub, err := s.Attach("", RoleControl, "", 80, 24, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TrySetAttentionFull(AttentionDone, "All tests pass.", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	logged := len(s.Activity())
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if a := s.Info().Attention; a.State != AttentionNone || a.Source != SourceInput || a.ID != "" || a.Since != nil {
		t.Fatalf("the Enter after the done: %+v", a)
	}
	// Broadcast in the step that changed it; the viewer's writer hands it
	// on in its own time.
	if m := waitControl(sink, func(m map[string]any) bool {
		return m["t"] == proto.CtlAttention && m["state"] == "" && m["source"] == SourceInput
	}); m == nil {
		t.Fatal("the viewer was not told the empty state")
	}
	if got := len(s.Activity()); got != logged {
		t.Fatalf("the Enter recorded %d entries: %+v", got-logged, s.Activity()[logged:])
	}

	// A submission's Enter, a crew's next prompt.
	s2, p2 := newLocalWith(t, quiet(Options{}))
	if err := s2.TrySetAttentionFull(AttentionDone, "All tests pass.", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	logged = len(s2.Activity())
	if res, err := s2.Submit(t.Context(), Submission{Text: "now the docs", ByName: "crew"}); err != nil || !res.Entered {
		t.Fatalf("the next prompt: %+v %v", res, err)
	}
	nextWrite(t, p2)
	nextWrite(t, p2)
	if a := s2.Info().Attention; a.State != AttentionNone || a.Source != SourceInput {
		t.Fatalf("the submission's Enter after the done: %+v", a)
	}
	for _, e := range s2.Activity()[logged:] {
		if e.Type == ActivityAttention {
			t.Fatalf("the submission recorded an attention entry: %+v", e)
		}
	}
}

// A done while a done shows, no other state between, is weighed in this
// order. The same as the last report of the done shown (its words, source,
// kind and choices) is that report again: nothing changes and nothing is
// recorded. Else, for an agent that tells its turns' starts (a hook's
// working has shown), it is the same turn's (Claude Code's idle_prompt,
// about a minute after its Stop): the one shown keeps its id, its Since
// and its words, and the second is still recorded, at its own time. Else
// it ends another turn: a new done, recorded. A later turn's done, after
// an Enter and a working, is a new one. No clock is read: the stamps
// compared are the session's own.
func TestADoneAfterADoneKeepsTheFirst(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	const stop, idle = "Refactored the parser.", "Claude is waiting for your input"
	if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.TrySetAttentionFull(AttentionDone, stop, SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	first := s.Info().Attention
	if err := s.TrySetAttentionFull(AttentionDone, idle, SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	a := s.Info().Attention
	if a.State != AttentionDone || a.ID != first.ID || !a.Since.Equal(*first.Since) || a.Message != stop {
		t.Fatalf("the second done %+v, the first %+v", a, first)
	}
	entries := doneEntries(s, stop, idle)
	if len(entries) != 2 || entries[0].Message != stop || entries[1].Message != idle {
		t.Fatalf("entries %+v", entries)
	}
	if !entries[0].At.Equal(*first.Since) || entries[1].At.Before(entries[0].At) {
		t.Fatalf("the entries' stamps %v and %v, the done's %v", entries[0].At, entries[1].At, *first.Since)
	}

	// A later turn: an Enter, a working, and a done with other words.
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	const next = "Docs updated."
	if err := s.TrySetAttentionFull(AttentionDone, next, SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	b := s.Info().Attention
	last := doneEntries(s, next)
	if b.State != AttentionDone || b.Message != next || b.ID == first.ID || len(last) != 1 || !b.Since.Equal(last[0].At) || b.Since.Before(entries[1].At) {
		t.Fatalf("the later turn's done %+v, its entries %+v", b, last)
	}

	// Each report is weighed against the last report of the done shown,
	// never against the done shown alone. For an agent that tells its
	// turns' starts the done shown stays the first (the same turn's): A, B,
	// B records A and B; A, B, A records all three; A, A records A. For one
	// that does not, each report that differs from the last is a new done
	// (another turn's end), shown from its own report on, and one the same
	// as the last is that report again: A, B, B records A and B and shows
	// the first B; A, B, A records all three and shows the second A.
	for _, tc := range []struct {
		name    string
		starts  bool
		reports []string
		want    []string
		// shownAt is the report whose id and Since the done shown has.
		shownAt int
	}{
		{"starts told: A B B", true, []string{"A", "B", "B"}, []string{"A", "B"}, 0},
		{"starts told: A B A", true, []string{"A", "B", "A"}, []string{"A", "B", "A"}, 0},
		{"starts told: A A", true, []string{"A", "A"}, []string{"A"}, 0},
		{"done only: A B B", false, []string{"A", "B", "B"}, []string{"A", "B"}, 1},
		{"done only: A B A", false, []string{"A", "B", "A"}, []string{"A", "B", "A"}, 2},
		{"done only: A A", false, []string{"A", "A"}, []string{"A"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newLocalWith(t, quiet(Options{}))
			if tc.starts {
				if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
					t.Fatal(err)
				}
			}
			var shown []Attention
			for _, m := range tc.reports {
				if err := s.TrySetAttentionFull(AttentionDone, m, SourceAPI, KindDone, nil); err != nil {
					t.Fatal(err)
				}
				shown = append(shown, s.Info().Attention)
			}
			var got []string
			for _, e := range doneEntries(s, "A", "B") {
				got = append(got, e.Message)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("recorded %q, want %q", got, tc.want)
			}
			a, at := s.Info().Attention, shown[tc.shownAt]
			if a.ID != at.ID || !a.Since.Equal(*at.Since) || a.Message != tc.reports[tc.shownAt] {
				t.Fatalf("the done shown %+v, want report %d's: %+v", a, tc.shownAt, at)
			}
			if tc.shownAt > 0 && at.ID == shown[tc.shownAt-1].ID {
				t.Fatalf("report %d kept the id of the one before: %+v", tc.shownAt, shown)
			}
		})
	}
}

// Every turn's end is recorded once, as an entry and an activity call (a
// webhook, an alert rule and a crew see each), but for one stated limit.
// An Enter before the next turn ends the done shown, so the next done is
// not weighed against it: three turns of an agent whose done always
// carries the same words (aider's "aider is waiting") give three. An agent
// that reports only its done and whose next turns begin with no Enter
// after its done (prompts entered during its turn) ends each with a done
// over a done: a new done each, three. An agent whose hooks tell each
// turn's start ends the done shown with each working: three. The limit:
// such a done-only agent's turns that end with the same words are the
// same report again, one.
func TestEveryTurnsDoneIsRecordedOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		enter bool // an Enter before each turn after the first
		start bool // a hook's working at each turn's start
		words []string
		want  int
	}{
		{"an Enter before each turn, the same words", true, false, []string{"aider is waiting", "aider is waiting", "aider is waiting"}, 3},
		{"done only, no Enter, other words", false, false, []string{"Refactored the parser.", "Docs updated.", "Tests pass."}, 3},
		{"hooks tell each start, no Enter, the same words", false, true, []string{"Done.", "Done.", "Done."}, 3},
		{"limit: done only, no Enter, the same words", false, false, []string{"aider is waiting", "aider is waiting", "aider is waiting"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var told []string
			s, p := newLocalWith(t, quiet(Options{OnActivity: func(_ string, e ActivityEntry, state AttentionState, _ string) {
				if e.Type == ActivityAttention && state == AttentionDone {
					mu.Lock()
					told = append(told, e.Message)
					mu.Unlock()
				}
			}}))
			sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			for turn, words := range tc.words {
				if turn > 0 && tc.enter {
					if err := s.Input(sub, []byte("\r")); err != nil {
						t.Fatal(err)
					}
					nextWrite(t, p)
				}
				if tc.start {
					if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.TrySetAttentionFull(AttentionDone, words, SourceAPI, KindDone, nil); err != nil {
					t.Fatal(err)
				}
			}
			if got := doneEntries(s, tc.words...); len(got) != tc.want {
				t.Fatalf("%d entries for three turns, want %d: %+v", len(got), tc.want, got)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(told) != tc.want {
				t.Fatalf("%d activity calls for three turns, want %d: %q", len(told), tc.want, told)
			}
		})
	}
}

// Info.Turns says that the session's agent reports its turns: learned from
// a working or a done of its own (its hooks, its window title, its program
// status), never from a bell, the screen pattern or an operator's mark,
// and kept through an Enter and the session's end.
func TestTurnsIsLearnedFromTheAgentsOwnReports(t *testing.T) {
	bell, p := newLocalWith(t, quiet(Options{}))
	p.outW.Write([]byte("\a"))
	waitAttention(t, bell, isBell)
	pattern, p2 := newLocalWith(t, quiet(Options{Pattern: regexp.MustCompile(`Continue\? $`)}))
	p2.outW.Write([]byte("Continue? "))
	waitAttention(t, pattern, func(a Attention) bool { return a.Source == SourcePattern })
	if err := pattern.TrySetAttentionFull(AttentionWorking, "", SourceAdmin, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := pattern.TrySetAttentionFull(AttentionDone, "", SourceAdmin, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if info := bell.Info(); info.Turns {
		t.Fatalf("learned from a bell: %+v", info)
	}
	if info := pattern.Info(); info.Turns || info.Attention.Source != SourceAdmin {
		t.Fatalf("learned from the pattern or an operator's mark: %+v", info)
	}
	for _, src := range []string{SourceAPI, SourceTitle, SourceStatus} {
		for _, state := range []AttentionState{AttentionWorking, AttentionDone} {
			t.Run(src+" "+string(state), func(t *testing.T) {
				s, p := newLocalWith(t, quiet(Options{}))
				sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
				if err != nil {
					t.Fatal(err)
				}
				if s.Info().Turns {
					t.Fatal("a new session reports turns")
				}
				if err := s.setAttention(state, "", src, "", nil, fromReport); err != nil {
					t.Fatal(err)
				}
				if !s.Info().Turns {
					t.Fatalf("a %s from %s taught nothing", state, src)
				}
				if err := s.Input(sub, []byte("\r")); err != nil {
					t.Fatal(err)
				}
				nextWrite(t, p)
				if !s.Info().Turns {
					t.Fatal("the Enter cleared it")
				}
				p.exit()
				<-s.Ended()
				if !s.Info().Turns {
					t.Fatal("the end cleared it")
				}
			})
		}
	}
}

// A report the rules keep out still teaches Info.Turns (a window title's
// working while an operator's working shows), and that change alone is
// told (OnChange): nothing else of the session changes.
func TestTurnsLearnedAloneIsTold(t *testing.T) {
	var mu sync.Mutex
	var told []Info
	s, _ := newLocalWith(t, quiet(Options{OnChange: func(i Info) {
		mu.Lock()
		told = append(told, i)
		mu.Unlock()
	}}))
	if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAdmin, "", nil); err != nil {
		t.Fatal(err)
	}
	before := s.Info().Attention
	mu.Lock()
	n := len(told)
	mu.Unlock()
	if err := s.setAttention(AttentionWorking, "", SourceTitle, "", nil, titleRules); err != nil {
		t.Fatal(err)
	}
	info := s.Info()
	if !info.Turns || info.Attention.Source != SourceAdmin || !info.Attention.Since.Equal(*before.Since) {
		t.Fatalf("after the title: %+v, before %+v", info, before)
	}
	// Told in the step that learned it, before it returned.
	mu.Lock()
	defer mu.Unlock()
	if len(told) == n || !told[len(told)-1].Turns {
		t.Fatalf("the change was not told: %d calls before, %+v", n, told)
	}
}

// The other order: the bell first, then the done; the done stands.
func TestABellThenTheDoneEndsDone(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	p.outW.Write([]byte("\a"))
	waitAttention(t, s, func(a Attention) bool { return a.Source == SourceBell })
	if err := s.TrySetAttentionFull(AttentionDone, "HTTP/2 200", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if a := bellAfter(t, s, p, ""); a.State != AttentionDone {
		t.Fatalf("attention %+v", a)
	}
}

// The allowance ends with the turn: a submission's Enter (a crew's next
// prompt) or a person's starts the next one, and a bell then is a question,
// also at once (Codex asking for approval at the start of a turn, the
// only sign of it without its hooks); its dialog, once quiet, is asked
// with its choices. A working report ends it too.
func TestANewTurnsBellIsAQuestion(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	if err := s.TrySetAttentionFull(AttentionDone, "HTTP/2 200", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Submit(t.Context(), Submission{Text: "now fetch it again", ByName: "crew"}); err != nil || !res.Entered {
		t.Fatalf("the next prompt: %+v %v", res, err)
	}
	nextWrite(t, p)
	nextWrite(t, p)
	p.outW.Write([]byte("\a" + approvalDialog("curl -sI https://example.com")))
	// The bell asks at once; the dialog's question takes its place once the screen is quiet (500 ms).
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNeedsInput }); a.Source != SourceBell {
		t.Fatalf("the new turn's bell: %+v", a)
	}
	if a := waitAttention(t, s, asked); a.Message != approvalWords || len(a.Options) != 3 {
		t.Fatalf("the new turn's approval: %+v", a)
	}

	s2, p2 := newLocalWith(t, quiet(Options{}))
	sub, err := s2.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.TrySetAttentionFull(AttentionDone, "done", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	s2.Input(sub, []byte("\r"))
	nextWrite(t, p2)
	if a := bellAfter(t, s2, p2, "\a"); a.State != AttentionNeedsInput || a.Source != SourceBell {
		t.Fatalf("a bell after a person's Enter: %+v", a)
	}

	s3, p3 := newLocalWith(t, quiet(Options{}))
	if err := s3.TrySetAttentionFull(AttentionDone, "done", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if err := s3.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	if a := bellAfter(t, s3, p3, "\a"); a.State != AttentionNeedsInput || a.Source != SourceBell {
		t.Fatalf("a bell after a working report: %+v", a)
	}
}

// flushed returns once the session has scanned everything written to p
// before it: the pipe hands a write over only as the pump reads it, and the
// pump reads again only once it has looked at the chunk before.
func flushed(p *fakeProc) { p.outW.Write([]byte{0}) }

// isBell reports whether the session waits on the terminal bell.
func isBell(a Attention) bool { return a.State == AttentionNeedsInput && a.Source == SourceBell }

// A new turn's bell asks however soon it rings after the last turn's
// (round 15's todo): the bell that ended a turn and changed nothing does
// not start the pace, and the Enter after a done begins a turn whose bells
// are paced on their own. Both orders of Codex's done and bell: the bell
// after the done (kept as the turn's own), and the bell before it (which
// asked, and which the done then replaced).
func TestANewTurnsBellSoonAfterTheLastTurnsAsks(t *testing.T) {
	for name, bellFirst := range map[string]bool{"the bell after the done": false, "the bell before the done": true} {
		t.Run(name, func(t *testing.T) { newTurnsBell(t, bellFirst) })
	}
}

// newTurnsBell is one order of TestANewTurnsBellSoonAfterTheLastTurnsAsks.
func newTurnsBell(t *testing.T, bellFirst bool) {
	s, p := newLocalWith(t, quiet(Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	lastTurns := time.Now() // no later than the last turn's bell
	if bellFirst {
		p.outW.Write([]byte("\a"))
		waitAttention(t, s, isBell)
	}
	if err := s.TrySetAttentionFull(AttentionDone, "HTTP/2 200", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if !bellFirst {
		p.outW.Write([]byte("\a"))
		flushed(p)
		if a := s.Info().Attention; a.State != AttentionDone {
			t.Fatalf("the turn's own bell undid its done: %+v", a)
		}
	}
	// The next prompt's Enter, and the new turn's bell at once.
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	p.outW.Write([]byte("\a"))
	if a := waitAttention(t, s, isBell); a.Message != "terminal bell" {
		t.Fatalf("the new turn's bell %+v", a)
	}
	if gap := time.Since(lastTurns); gap >= bellPace {
		t.Logf("the turns' bells were %v apart, not within the pace: the case was not tried", gap)
	}
}

// withinPace runs try until it reports that what it checked happened
// within bellPace of the change that started the pace: a machine slow
// enough to let the pace run out leaves nothing to check, and is tried
// again, three times at most.
func withinPace(t *testing.T, try func() bool) {
	t.Helper()
	for range 3 {
		if try() {
			return
		}
	}
	t.Fatal("never within the pace: the machine is too slow to tell")
}

// The pace's purpose stands: within one turn a burst of bells makes one
// change per bellPace, whatever comes between (a working report, an
// answer with no done before it), and the first bell past it asks again.
func TestABurstOfBellsMakesOneChange(t *testing.T) {
	withinPace(t, func() bool {
		s, p := newLocalWith(t, quiet(Options{}))
		p.outW.Write([]byte("\a"))
		first := waitAttention(t, s, isBell)
		if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		p.outW.Write([]byte("\a\a\x1b]9;and more\a"))
		flushed(p)
		a := s.Info().Attention
		if time.Since(*first.Since) >= bellPace {
			return false
		}
		if a.State != AttentionWorking {
			t.Fatalf("a bell within the pace changed the attention: %+v", a)
		}
		var bells int
		for _, e := range s.Activity() {
			if e.Type == ActivityAttention && e.Message == "terminal bell" {
				bells++
			}
		}
		if bells != 1 {
			t.Fatalf("the burst made %d changes", bells)
		}
		// Past the pace the next bell asks.
		time.Sleep(time.Until(first.Since.Add(bellPace)))
		p.outW.Write([]byte("\a"))
		waitAttention(t, s, isBell)
		return true
	})
	withinPace(t, func() bool {
		s, p := newLocalWith(t, quiet(Options{}))
		sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		p.outW.Write([]byte("\a"))
		first := waitAttention(t, s, isBell)
		// An Enter that answers the bell, no done before it: the same turn.
		if err := s.Input(sub, []byte("\r")); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
		p.outW.Write([]byte("\a"))
		flushed(p)
		a := s.Info().Attention
		if time.Since(*first.Since) >= bellPace {
			return false
		}
		if a.State != AttentionNone {
			t.Fatalf("the burst's tail asked again after its answer: %+v", a)
		}
		return true
	})
	// The same in a turn the agent began by itself after a done (a working
	// report, no Enter): its answer is no new turn either (Codex's review).
	withinPace(t, func() bool {
		s, p := newLocalWith(t, quiet(Options{}))
		sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.TrySetAttentionFull(AttentionDone, "done", SourceAPI, KindDone, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
			t.Fatal(err)
		}
		p.outW.Write([]byte("\a"))
		first := waitAttention(t, s, isBell)
		if err := s.Input(sub, []byte("\r")); err != nil {
			t.Fatal(err)
		}
		nextWrite(t, p)
		p.outW.Write([]byte("\a"))
		flushed(p)
		a := s.Info().Attention
		if time.Since(*first.Since) >= bellPace {
			return false
		}
		if a.State != AttentionNone {
			t.Fatalf("the burst's tail asked again after its answer, in the turn a working report began: %+v", a)
		}
		return true
	})
}

// Bells read during a turn and looked at only once a done and an Enter
// have begun the next are the ended turn's own: they ask nothing, however
// many the chunk holds (Codex's review: the second of two bells, looked at
// after the turn changed, asked as the new turn's). The next turn's own
// bell then asks.
func TestBellsReadInTheEndedTurnAreItsOwn(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	read, look := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func(chunk []byte) {
		if bytes.Contains(chunk, []byte("\a")) {
			once.Do(func() {
				close(read)
				<-look
			})
		}
	}
	// Held as soon as the read returns, before the pump takes the
	// session's lock (Codex's review: a turn begun there made the read
	// the new turn's).
	s.beforeScan.Store(&hook)
	go p.outW.Write([]byte("\a\a"))
	<-read
	if err := s.TrySetAttentionFull(AttentionDone, "HTTP/2 200", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	close(look)
	flushed(p)
	if a := s.Info().Attention; a.State != AttentionNone || a.Source != SourceInput {
		t.Fatalf("the ended turn's bells asked in the next: %+v", a)
	}
	p.outW.Write([]byte("\a"))
	waitAttention(t, s, isBell)
}

// The same for the start of a notification read in the ended turn, held
// as soon as it is read while a done and an Enter begin the next turn, and
// ended after: it asks nothing (Codex's review).
func TestANotificationBegunInTheEndedTurnIsItsOwn(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	read, look := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func(chunk []byte) {
		if bytes.Contains(chunk, []byte("\x1b]9;")) {
			once.Do(func() {
				close(read)
				<-look
			})
		}
	}
	s.beforeScan.Store(&hook)
	go p.outW.Write([]byte("\x1b]9;Agent turn"))
	<-read
	if err := s.TrySetAttentionFull(AttentionDone, "HTTP/2 200", SourceAPI, KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	close(look)
	p.outW.Write([]byte(" complete\a"))
	flushed(p)
	if a := s.Info().Attention; a.State != AttentionNone || a.Source != SourceInput {
		t.Fatalf("the ended turn's notification asked in the next: %+v", a)
	}
	p.outW.Write([]byte("\x1b]9;need approval\a"))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNeedsInput }); a.Message != "need approval" {
		t.Fatalf("the next turn's notification: %+v", a)
	}
}

// An OSC notification begun in a turn's output and ended in a read after
// the next turn began (an Enter, or a working report after the done) is
// the ended turn's own too (Codex's review): it asks nothing and starts no
// pace, and the next turn's own notification asks.
func TestANotificationSplitAcrossTurnsIsTheEndedTurns(t *testing.T) {
	for name, next := range map[string]func(*testing.T, *Local, *fakeProc, *Subscription){
		"an Enter": func(t *testing.T, s *Local, p *fakeProc, sub *Subscription) {
			if err := s.Input(sub, []byte("\r")); err != nil {
				t.Fatal(err)
			}
			nextWrite(t, p)
		},
		"a working report": func(t *testing.T, s *Local, _ *fakeProc, _ *Subscription) {
			if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{}))
			sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); err != nil {
				t.Fatal(err)
			}
			p.outW.Write([]byte("x\x1b]9;Agent turn"))
			p.outW.Write([]byte(" complete"))
			flushed(p)
			if err := s.TrySetAttentionFull(AttentionDone, "HTTP/2 200", SourceAPI, KindDone, nil); err != nil {
				t.Fatal(err)
			}
			next(t, s, p, sub)
			before := s.Info().Attention
			p.outW.Write([]byte("\a"))
			flushed(p)
			if a := s.Info().Attention; a.State == AttentionNeedsInput {
				t.Fatalf("the ended turn's notification asked in the next: %+v", a)
			} else if a.State != before.State {
				t.Fatalf("attention moved from %+v to %+v", before, a)
			}
			p.outW.Write([]byte("\x1b]9;need approval\a"))
			if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNeedsInput }); a.Source != SourceOSC || a.Message != "need approval" {
				t.Fatalf("the next turn's notification: %+v", a)
			}
		})
	}
}
