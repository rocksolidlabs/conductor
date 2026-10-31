package session

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The agent's own question (#193, docs/plans/codex-needs-you.md, Part B):
// a needs_input report that says the agent asks it (Ask: the MCP tool ask,
// conductor notify --ask) shows until it is answered, also once the turn
// it was asked in has ended. The P numbers are the plan's orderings.

// ownLog keeps the attention entries a session hands OnActivity, each as
// "<the state it records> <its words>".
type ownLog struct {
	mu  sync.Mutex
	got []string
}

func (l *ownLog) hook(_ string, e ActivityEntry, st AttentionState) {
	if e.Type != ActivityAttention {
		return
	}
	l.mu.Lock()
	l.got = append(l.got, string(st)+" "+e.Message)
	l.mu.Unlock()
}

// since returns the entries from the n-th on.
func (l *ownLog) since(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.got[min(n, len(l.got)):])
}

func (l *ownLog) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.got)
}

// logged waits until log holds entry, or fails the test after 5 s. The
// session hands OnActivity an attention change's entry after the change,
// once it has let its lock go (recordOwn): a state seen showing says
// nothing of its entry, which a count of the log taken at once can come
// before.
func logged(t *testing.T, log *ownLog, entry string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(log.since(0), entry) {
		if time.Now().After(deadline) {
			t.Fatalf("entries %q, want %q among them", log.since(0), entry)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// heldEntry is the entry the hold of the turn records as it shows.
const heldEntry = string(AttentionNeedsInput) + " " + HeldWords

// asking is a session under test for the agent's own question, with o's
// options, and the log of its attention entries.
func asking(t *testing.T, o Options) (*Local, *fakeProc, *ownLog) {
	t.Helper()
	log := &ownLog{}
	o.OnActivity = log.hook
	if o.InputWait == 0 {
		o.InputWait = 5 * time.Second
	}
	s, p := newLocalWith(t, quiet(o))
	return s, p, log
}

// dbChoices are the choices of the question the tests ask.
var dbChoices = []Option{{Label: "Postgres", Input: "Postgres\r"}, {Label: "SQLite", Input: "SQLite\r"}}

// askOwn asks words as the agent's own question, with choices, and returns
// it as it shows.
func askOwn(t *testing.T, s *Local, words string, choices ...Option) Attention {
	t.Helper()
	kind := ""
	if len(choices) > 0 {
		kind = KindPrompt
	}
	if err := s.TryAsk(words, SourceAPI, kind, choices); err != nil {
		t.Fatal(err)
	}
	a := s.Info().Attention
	if a.State != AttentionNeedsInput || a.Message != words || !a.Asked() || a.ID == "" || a.Since == nil {
		t.Fatalf("asked %q: %+v", words, a)
	}
	return a
}

// reportState reports state with words, as the agent's hook does.
func reportState(t *testing.T, s *Local, state AttentionState, words string) {
	t.Helper()
	kind := ""
	if state == AttentionDone {
		kind = KindDone
	}
	if err := s.TrySetAttentionFull(state, words, SourceAPI, kind, nil); err != nil {
		t.Fatal(err)
	}
}

// showsOwn fails unless the session shows q, the same Attention: its id,
// its Since (the stamp itself, which a reply under way compares), its
// words and choices, marked as the agent's.
func showsOwn(t *testing.T, s *Local, q Attention, what string) {
	t.Helper()
	a := s.Info().Attention
	if a.State != AttentionNeedsInput || a.ID != q.ID || a.Since != q.Since || a.Message != q.Message || !sameOptions(a.Options, q.Options) || !a.Asked() {
		t.Fatalf("%s: attention %+v, want the question %+v", what, a, q)
	}
}

// shows fails unless the session shows state (source too, when given).
func shows(t *testing.T, s *Local, state AttentionState, what string, source ...string) {
	t.Helper()
	a := s.Info().Attention
	if a.State != state || len(source) > 0 && a.Source != source[0] {
		t.Fatalf("%s: attention %+v, want %q %v", what, a, state, source)
	}
}

// keeps reports whether the session keeps an own question, and whether it
// is set aside.
func keeps(s *Local) (kept, aside bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.own.ID != "", s.ownAside
}

// questionLines counts the chat's lines for the question id.
func questionLines(s *Local, id string) int {
	n := 0
	for _, m := range s.ChatHistory() {
		if m.Kind == proto.ChatKindQuestion && m.ID == id {
			n++
		}
	}
	return n
}

// pauseEnter holds the next submission at its Enter, its text typed: at is
// closed once it is there, and release lets it go on.
func pauseEnter(t *testing.T, s *Local) (at <-chan struct{}, release func()) {
	t.Helper()
	reached, gate := make(chan struct{}), make(chan struct{})
	var once, rel sync.Once
	hook := func() { once.Do(func() { close(reached); <-gate }) }
	s.beforeEnter.Store(&hook)
	release = func() { rel.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return reached, release
}

// atEnter waits until the submission paused by pauseEnter is at its Enter.
func atEnter(t *testing.T, at <-chan struct{}) {
	t.Helper()
	select {
	case <-at:
	case <-time.After(5 * time.Second):
		t.Fatal("the submission never came to its Enter")
	}
}

// P1, P7: the question shows through the turn's done, with its id, words
// and choices; the done is recorded with its state (the Events page, a
// webhook, a crew's start-after rule); the turn's own bell after it asks
// nothing; Claude Code's idle_prompt, another done a minute later, changes
// nothing shown and is recorded too. It is in the chat once.
func TestAnAgentsQuestionOutlastsItsTurnsDone(t *testing.T) {
	s, p, log := asking(t, Options{})
	q := askOwn(t, s, "Which database?", dbChoices...)
	reportState(t, s, AttentionDone, "asked about the database")
	showsOwn(t, s, q, "after the turn's done")
	out(p, "\a", "\x1b]9;Codex: turn complete\x07")
	showsOwn(t, s, q, "after the turn's bell and notification")
	reportState(t, s, AttentionDone, "idle")
	showsOwn(t, s, q, "after idle_prompt's done")
	if got, want := log.since(0), []string{"needs_input Which database?", "done asked about the database", "done idle"}; !slices.Equal(got, want) {
		t.Fatalf("entries %q, want %q", got, want)
	}
	if n := questionLines(s, q.ID); n != 1 {
		t.Fatalf("%d chat lines for the question", n)
	}
	// Turns ended: a reply naming it is taken.
	if err := s.questionWaits(q.ID); err != nil {
		t.Fatalf("a reply to it: %v", err)
	}
}

// P2, P3: the agent works on after asking (Claude Code calls a tool,
// Codex's title says Working): working shows and the question is set
// aside; the turn's done shows it again, the same Attention, recorded as
// it shows (needs_input, working, done, needs_input), in the chat once. A
// bell over working (Codex rings its turn-end bell before notify reports
// done) shows, and the done shows the question, not the bell's wait.
func TestAQuestionSetAsideByWorkShowsAgainAtTheDone(t *testing.T) {
	for _, bell := range []bool{false, true} {
		t.Run(fmt.Sprintf("bell %v", bell), func(t *testing.T) {
			s, p, log := asking(t, Options{})
			q := askOwn(t, s, "Which database?", dbChoices...)
			reportState(t, s, AttentionWorking, "")
			shows(t, s, AttentionWorking, "at work after asking")
			if _, aside := keeps(s); !aside {
				t.Fatal("the question is not set aside")
			}
			want := []string{"needs_input Which database?", "working working"}
			if bell {
				out(p, "\a")
				shows(t, s, AttentionNeedsInput, "the bell over working", SourceBell)
				want = append(want, "needs_input terminal bell")
			}
			reportState(t, s, AttentionDone, "done for now")
			showsOwn(t, s, q, "at the turn's done")
			want = append(want, "done done for now", "needs_input Which database?")
			if got := log.since(0); !slices.Equal(got, want) {
				t.Fatalf("entries %q, want %q", got, want)
			}
			if n := questionLines(s, q.ID); n != 1 {
				t.Fatalf("%d chat lines for the question", n)
			}
			// Its answer refers to its own line in the chat.
			owner := ownerOf(t, s)
			if err := s.AnswerInput(context.Background(), owner, q.ID, []byte("Postgres\r")); err != nil {
				t.Fatal(err)
			}
			if got := written(t, p); got != "Postgres\r" {
				t.Fatalf("written %q", got)
			}
			shows(t, s, AttentionNone, "answered")
			var ref string
			for _, m := range s.ChatHistory() {
				if m.Event == "answered" {
					ref = m.Ref
				}
			}
			if ref != q.ID {
				t.Fatalf("the answered line refers to %q, the question is %q", ref, q.ID)
			}
		})
	}
}

// P4, P5: after the done, the question is answered as any: a reply naming
// it clears it, with the last answer and the chat's answered line, and
// the next done shows done. Or the agent works again, in a new turn: its
// working report takes the question's place for good, and the next done
// shows done.
func TestAnAnsweredOrWorkedQuestionGoesForGood(t *testing.T) {
	t.Run("answered", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		owner := ownerOf(t, s)
		q := askOwn(t, s, "Which database?", dbChoices...)
		reportState(t, s, AttentionDone, "asked")
		if err := s.AnswerInput(context.Background(), owner, q.ID, []byte("Postgres\r")); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		info := s.Info()
		if info.Attention.State != AttentionNone || info.LastAnswer == nil || info.LastAnswer.Message != "Which database?" {
			t.Fatalf("answered: %+v %+v", info.Attention, info.LastAnswer)
		}
		if kept, _ := keeps(s); kept {
			t.Fatal("the answered question is kept")
		}
		reportState(t, s, AttentionWorking, "")
		reportState(t, s, AttentionDone, "chose Postgres")
		shows(t, s, AttentionDone, "the next turn's done")
	})
	t.Run("worked", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		askOwn(t, s, "Which database?")
		reportState(t, s, AttentionDone, "asked")
		reportState(t, s, AttentionWorking, "")
		shows(t, s, AttentionWorking, "at work in a new turn")
		if kept, _ := keeps(s); kept {
			t.Fatal("the question is kept after the agent worked in a new turn")
		}
		reportState(t, s, AttentionDone, "went on")
		shows(t, s, AttentionDone, "the next done")
	})
}

// P6, P26: Codex's working title read after the turn's done (drawn before
// it) changes nothing: the question stands until an Enter, after which the
// title shows working. A question Codex asks while its title says Working,
// the title written once, gives way to working when its second has passed
// (the settle timer), set aside, and the turn's done shows it.
func TestCodexsTitleAndTheQuestion(t *testing.T) {
	t.Run("after the done", func(t *testing.T) {
		s, p, _ := asking(t, titled(time.Minute, Options{}))
		owner := ownerOf(t, s)
		q := askOwn(t, s, "Which database?")
		reportState(t, s, AttentionDone, "asked")
		out(p, codexWorking(1), codexRedraw, codexWorking(2))
		showsOwn(t, s, q, "a title after the done")
		if err := s.AnswerInput(context.Background(), owner, q.ID, []byte("\r")); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		shows(t, s, AttentionNone, "answered by the reply's Enter")
		out(p, codexWorking(3))
		shows(t, s, AttentionWorking, "the title once the next turn began", SourceTitle)
	})
	t.Run("settled in its turn", func(t *testing.T) {
		s, p, _ := asking(t, titled(150*time.Millisecond, Options{}))
		out(p, codexStill)
		shows(t, s, AttentionWorking, "the turn's title", SourceTitle)
		q := askOwn(t, s, "Which database?")
		waitAttention(t, s, titleWorks)
		if _, aside := keeps(s); !aside {
			t.Fatal("the settled question is not set aside")
		}
		reportState(t, s, AttentionDone, "asked")
		showsOwn(t, s, q, "at the done")
		// The title kept from the turn arms nothing after the done.
		time.Sleep(300 * time.Millisecond)
		showsOwn(t, s, q, "a second after the done")
	})
}

// P8: the bell, an OSC notification and the screen pattern never take the
// question's place, shown or set aside.
func TestBellsAndThePatternLeaveTheQuestion(t *testing.T) {
	s, p, _ := asking(t, Options{Pattern: regexp.MustCompile(`\(Y\)es/\(N\)o\s*$`)})
	q := askOwn(t, s, "Which database?")
	out(p, "\a", "\x1b]9;look here\x07", "\r\nProceed? (Y)es/(N)o ")
	time.Sleep(patternQuiet + 200*time.Millisecond)
	showsOwn(t, s, q, "shown: the bell, a notification, the pattern")
	reportState(t, s, AttentionWorking, "")
	out(p, "\r\nworking\r\nAgain? (Y)es/(N)o ")
	waitAttention(t, s, func(a Attention) bool { return a.Source == SourcePattern })
	if _, aside := keeps(s); !aside {
		t.Fatal("the pattern's prompt took the question set aside away")
	}
	reportState(t, s, AttentionDone, "asked")
	showsOwn(t, s, q, "the done after the pattern's prompt")
}

// P9: the agent's next question takes the first's place; the done keeps
// the second.
func TestTheAgentsNextQuestionReplacesTheFirst(t *testing.T) {
	s, _, _ := asking(t, Options{})
	askOwn(t, s, "Which database?")
	reportState(t, s, AttentionWorking, "")
	second := askOwn(t, s, "Which port?")
	reportState(t, s, AttentionWorking, "")
	reportState(t, s, AttentionDone, "asked twice")
	showsOwn(t, s, second, "the second question at the done")
}

// P10, P11: another wait in the question's turn (a hook's permission
// prompt, a dialog on the screen) shows and sets the question aside: a
// person's keys that answer the wait remove the question too; a wait that
// goes without keys leaves it for the done. After the turn's done, another
// wait takes its place for good.
func TestAnotherWaitAndTheQuestion(t *testing.T) {
	permission := func(t *testing.T, s *Local) {
		t.Helper()
		if err := s.TrySetAttentionFull(AttentionNeedsInput, "Allow Bash?", SourceAPI, KindPermission, []Option{{Label: "Yes", Input: "1"}}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a permission prompt in its turn, gone without keys", func(t *testing.T) {
		s, _, _ := asking(t, Options{})
		q := askOwn(t, s, "Which database?")
		permission(t, s)
		shows(t, s, AttentionNeedsInput, "the permission prompt")
		if s.Info().Attention.Message != "Allow Bash?" {
			t.Fatalf("attention %+v", s.Info().Attention)
		}
		reportState(t, s, AttentionWorking, "") // the agent's reviewer answered it
		reportState(t, s, AttentionDone, "asked")
		showsOwn(t, s, q, "the done")
	})
	t.Run("a permission prompt in its turn, answered by a person", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		owner := ownerOf(t, s)
		askOwn(t, s, "Which database?")
		permission(t, s)
		if err := s.Input(owner, []byte("1")); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		reportState(t, s, AttentionDone, "asked")
		shows(t, s, AttentionDone, "the done after the person answered the permission")
	})
	t.Run("a dialog on the screen in its turn", func(t *testing.T) {
		s, p, _ := asking(t, Options{Questions: approvalQuestions()})
		q := askOwn(t, s, "Which database?")
		p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
		waitAttention(t, s, asked)
		if _, aside := keeps(s); !aside {
			t.Fatal("the dialog did not set the question aside")
		}
		reportState(t, s, AttentionDone, "asked")
		showsOwn(t, s, q, "the done after a dialog that went without keys")
	})
	t.Run("a dialog in its turn answered by a person", func(t *testing.T) {
		s, p, _ := asking(t, Options{Questions: approvalQuestions()})
		owner := ownerOf(t, s)
		askOwn(t, s, "Which database?")
		p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
		waitAttention(t, s, asked)
		if err := s.Input(owner, []byte("y")); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		reportState(t, s, AttentionDone, "asked")
		shows(t, s, AttentionDone, "the done after the person answered the dialog")
	})
	t.Run("a wait after the done", func(t *testing.T) {
		s, p, _ := asking(t, Options{})
		owner := ownerOf(t, s)
		askOwn(t, s, "Which database?")
		reportState(t, s, AttentionDone, "asked")
		permission(t, s)
		if kept, _ := keeps(s); kept {
			t.Fatal("a wait after the done left the question kept")
		}
		if err := s.Input(owner, []byte("1")); err != nil {
			t.Fatal(err)
		}
		written(t, p)
		reportState(t, s, AttentionDone, "went on")
		shows(t, s, AttentionDone, "the next done")
	})
}

// P12, P25: a person's keys while the question is set aside (the agent at
// work, the person typing into the terminal) remove it, as the keys are
// written, though the program takes them late: the done shows done.
func TestAPersonsKeysWhileTheQuestionIsAsideRemoveIt(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late %v", late), func(t *testing.T) {
			s, p, _ := asking(t, Options{})
			hold, takeAgain := gateWrites(t, p)
			owner := ownerOf(t, s)
			askOwn(t, s, "Which database?")
			reportState(t, s, AttentionWorking, "")
			if late {
				hold()
			}
			typed := make(chan error, 1)
			go func() { typed <- s.Input(owner, []byte("x")) }()
			if late {
				untilWaiting(t, s, 0)
			} else if err := <-typed; err != nil {
				t.Fatal(err)
			}
			reportState(t, s, AttentionDone, "done")
			shows(t, s, AttentionDone, "the done after the person's keys")
			takeAgain()
			if got := written(t, p); got != "x" {
				t.Fatalf("written %q", got)
			}
			if late {
				if err := <-typed; err != nil {
					t.Fatal(err)
				}
			}
			shows(t, s, AttentionDone, "once the keys landed")
		})
	}
}

// P13, P29: Conductor's own submission (a crew's handoff) typed and
// entered while the question is set aside is no person's answer: the
// question stays aside and the done shows it. An agent that takes the
// handoff after its turn (Claude Code's queued message: working after the
// done) removes it; one that takes it into the same turn (Codex steering)
// shows it at its done until a person answers.
func TestAHandoffEnteredWhileTheQuestionIsAsideLeavesIt(t *testing.T) {
	for _, after := range []bool{true, false} {
		t.Run(fmt.Sprintf("taken after the turn %v", after), func(t *testing.T) {
			s, p, _ := asking(t, Options{})
			q := askOwn(t, s, "Which database?")
			reportState(t, s, AttentionWorking, "")
			res, err := s.Submit(context.Background(), Submission{Text: "Handoff from core: over to you", ByName: "run", UnlessWaiting: true})
			if err != nil || !res.Entered {
				t.Fatalf("the handoff: %+v %v", res, err)
			}
			written(t, p)
			written(t, p)
			if _, aside := keeps(s); !aside {
				t.Fatal("the handoff took the question set aside away")
			}
			reportState(t, s, AttentionDone, "asked")
			showsOwn(t, s, q, "the done after the handoff")
			if after {
				reportState(t, s, AttentionWorking, "")
				reportState(t, s, AttentionDone, "took the handoff")
				shows(t, s, AttentionDone, "the handoff's turn's done")
			} else {
				reportState(t, s, AttentionDone, "idle")
				showsOwn(t, s, q, "until a person answers")
			}
		})
	}
}

// P14: a handoff waits while the question shows after the turn's done
// (UnlessWaiting); once a person answers, it is typed.
func TestAHandoffWaitsForTheQuestionsAnswer(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	q := askOwn(t, s, "Which database?")
	reportState(t, s, AttentionDone, "asked")
	res, err := s.Submit(context.Background(), Submission{Text: "Handoff from core: over to you", ByName: "run", UnlessWaiting: true})
	if err != nil || res.Typed {
		t.Fatalf("the handoff typed into the question: %+v %v", res, err)
	}
	nothingWritten(t, p, 100*time.Millisecond)
	if err := s.AnswerInput(context.Background(), owner, q.ID, []byte("Postgres\r")); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	res, err = s.Submit(context.Background(), Submission{Text: "Handoff from core: over to you", ByName: "run", UnlessWaiting: true})
	if err != nil || !res.Entered {
		t.Fatalf("the handoff once answered: %+v %v", res, err)
	}
}

// P15: the agent's clear (or an admin's) removes the question, shown or
// set aside: the empty state, then the done shows done.
func TestAClearRemovesTheQuestion(t *testing.T) {
	for _, aside := range []bool{false, true} {
		t.Run(fmt.Sprintf("aside %v", aside), func(t *testing.T) {
			s, _, _ := asking(t, Options{})
			askOwn(t, s, "Which database?")
			if aside {
				reportState(t, s, AttentionWorking, "")
			}
			reportState(t, s, AttentionNone, "")
			shows(t, s, AttentionNone, "cleared")
			reportState(t, s, AttentionDone, "found it myself")
			shows(t, s, AttentionDone, "the done after the clear")
		})
	}
}

// P16: the hold of the turn takes the question's place and holds it over;
// the turn's done hidden by the hold is recorded and keeps it held over,
// and so does the agent's work in its turn; keys that end the hold without
// answering it (Ctrl+U) show it again, its id the same, and a reply to it
// waiting for the turn answers it; an Enter answers it. A question set
// aside when the hold began is held over by the done.
func TestAHeldTurnHoldsTheQuestionOver(t *testing.T) {
	for _, c := range []struct {
		name  string
		aside bool
		keys  string
	}{
		{"shown, the text killed", false, "\x15"},
		{"shown, the text entered", false, "\r"},
		{"set aside, the text killed", true, "\x15"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, p, log := asking(t, Options{SubmitPause: 300 * time.Millisecond})
			var linkRevoked atomic.Bool
			guest := linkedController(t, s, newChanSink(false), &linkRevoked)
			owner := ownerOf(t, s)
			q := askOwn(t, s, "Which name?")
			if c.aside {
				reportState(t, s, AttentionWorking, "")
			}
			sub := Submission{Text: "guest line", By: guest}
			if !c.aside {
				sub.Question = q.ID
			}
			done := submitAsync(s, sub)
			if got := written(t, p); got != "guest line" {
				t.Fatalf("written %q", got)
			}
			untilIdle(t, s)
			linkRevoked.Store(true)
			s.DisconnectLink(guest.LinkID)
			if got := <-done; !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
				t.Fatalf("the guest's line: %+v, %v", got.res, got.err)
			}
			heldShows(t, s)
			if c.aside {
				// The guest's text, a person's, removed the question set
				// aside: nothing shows again.
				if kept, _ := keeps(s); kept {
					t.Fatal("a person's reply left the question set aside")
				}
				return
			}
			// The hold's own entry first, so that it is not counted as one
			// under the hold.
			logged(t, log, heldEntry)
			n := log.len()
			reportState(t, s, AttentionWorking, "")
			reportState(t, s, AttentionDone, "asked")
			heldShows(t, s)
			if got, want := log.since(n), []string{"working working", "done asked"}; !slices.Equal(got, want) {
				t.Fatalf("entries under the hold %q, want %q", got, want)
			}
			reply := submitAsync(s, Submission{Text: "Ada", By: owner, Question: q.ID})
			nothingWritten(t, p, 200*time.Millisecond)
			if err := s.Input(owner, []byte(c.keys)); err != nil {
				t.Fatal(err)
			}
			if got := written(t, p); got != c.keys {
				t.Fatalf("written %q", got)
			}
			got := returnsWithin(t, 5*time.Second, "the reply", func() submitted { return <-reply })
			if c.keys == "\r" {
				// The held text entered: the question shows again consumed
				// (the program has its answer), kept, and a reply to it is
				// refused until the agent moves on.
				if !errors.Is(got.err, ErrQuestionAnswered) || got.err.Error() != QuestionConsumedWords {
					t.Fatalf("the reply after the Enter: %+v, %v", got.res, got.err)
				}
				if s.Held() {
					t.Fatal("the hold did not end")
				}
				showsOwn(t, s, q, "once the Enter ended the hold")
				if kept, _ := keeps(s); !kept {
					t.Fatal("the question the Enter reached is not kept")
				}
				return
			}
			for _, want := range []string{"Ada", "\r"} {
				if w := written(t, p); w != want {
					t.Fatalf("written %q, want %q", w, want)
				}
			}
			if got.err != nil || !got.res.Entered {
				t.Fatalf("the reply: %+v, %v", got.res, got.err)
			}
			shows(t, s, AttentionNone, "answered by the reply")
		})
	}
}

// A question set aside when the hold began, Conductor's own text held
// (its link let go), is held over by the turn's done, and shows once a
// person's keys end the hold without answering it.
func TestAQuestionAsideIsHeldOverByTheDoneUnderTheHold(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	q := askOwn(t, s, "Which name?")
	reportState(t, s, AttentionWorking, "")
	go holdNow(s)
	heldShows(t, s)
	reportState(t, s, AttentionDone, "asked")
	heldShows(t, s)
	if err := s.Input(owner, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	showsOwn(t, s, q, "once the hold ended")
}

// P17: the session's end in any of the above: the empty state, nothing set
// aside, nothing shown again.
func TestTheSessionsEndDropsTheQuestion(t *testing.T) {
	for _, aside := range []bool{false, true} {
		t.Run(fmt.Sprintf("aside %v", aside), func(t *testing.T) {
			s, p, _ := asking(t, Options{})
			askOwn(t, s, "Which database?")
			if aside {
				reportState(t, s, AttentionWorking, "")
			} else {
				reportState(t, s, AttentionDone, "asked")
			}
			p.exit()
			<-s.Ended()
			shows(t, s, AttentionNone, "ended")
			if kept, _ := keeps(s); kept {
				t.Fatal("an ended session keeps the question")
			}
			s.SetAttentionFull(AttentionDone, "late", SourceAPI, KindDone, nil)
			if a := s.Info().Attention; a.State == AttentionNeedsInput {
				t.Fatalf("the question shows after the end: %+v", a)
			}
		})
	}
}

// P18: a report the event bucket refuses changes nothing: the question
// stays where it was, shown or set aside.
func TestARefusedReportLeavesTheQuestionWhereItWas(t *testing.T) {
	s, _, _ := asking(t, Options{})
	q := askOwn(t, s, "Which database?")
	drainBucket(s)
	if err := s.TrySetAttentionFull(AttentionWorking, "", SourceAPI, "", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("working on an empty bucket: %v", err)
	}
	showsOwn(t, s, q, "a refused working")
	s.mu.Lock()
	s.events = EventBucket{}
	s.mu.Unlock()
	reportState(t, s, AttentionWorking, "")
	drainBucket(s)
	if err := s.TrySetAttentionFull(AttentionDone, "", SourceAPI, KindDone, nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("done on an empty bucket: %v", err)
	}
	shows(t, s, AttentionWorking, "a refused done")
	if _, aside := keeps(s); !aside {
		t.Fatal("a refused done moved the question")
	}
	s.mu.Lock()
	s.events = EventBucket{}
	s.mu.Unlock()
	reportState(t, s, AttentionDone, "asked")
	showsOwn(t, s, q, "the done once the bucket refilled")
}

// P19, P27: a needs_input report without the mark (a hook's permission
// prompt; OpenCode's and DeepSeek Harness's permission waits, aider's)
// gives way to done as before.
func TestAWaitWithoutTheMarkGivesWayToDone(t *testing.T) {
	s, _, _ := asking(t, Options{})
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "OpenCode asks for permission", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	if s.Info().Attention.Asked() {
		t.Fatal("a report without the mark is marked")
	}
	reportState(t, s, AttentionDone, "finished")
	shows(t, s, AttentionDone, "the done over a wait without the mark")
	// Reported again with the mark, the same words are the agent's own.
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which port?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	q := s.Info().Attention
	if err := s.TryAsk("Which port?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	if a := s.Info().Attention; a.ID != q.ID || !a.Asked() {
		t.Fatalf("the same words asked: %+v", a)
	}
	reportState(t, s, AttentionDone, "asked")
	if a := s.Info().Attention; a.ID != q.ID || a.State != AttentionNeedsInput {
		t.Fatalf("the done after the mark: %+v", a)
	}
}

// P20: an answer against the done, and working against the done, at once,
// many times: each run ends as the sequential rules say, and the question
// never shows after its answer.
func TestRacesWithTheDoneEndAsTheSequentialRulesSay(t *testing.T) {
	for i := range 40 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, p, _ := asking(t, Options{})
			owner := ownerOf(t, s)
			q := askOwn(t, s, "Which database?")
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				if err := s.AnswerInput(context.Background(), owner, q.ID, []byte("x")); err != nil {
					t.Error(err)
				}
			}()
			go func() {
				defer wg.Done()
				<-start
				s.SetAttentionFull(AttentionDone, "asked", SourceAPI, KindDone, nil)
			}()
			close(start)
			wg.Wait()
			written(t, p)
			// Answered then done: done; done then answered: the empty state.
			if a := s.Info().Attention; a.State != AttentionDone && a.State != AttentionNone {
				t.Fatalf("an answer against the done: %+v", a)
			}
			if kept, _ := keeps(s); kept {
				t.Fatal("the answered question is kept")
			}

			s2, _, _ := asking(t, Options{})
			q2 := askOwn(t, s2, "Which port?")
			start2 := make(chan struct{})
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start2
				s2.SetAttentionFull(AttentionWorking, "", SourceAPI, "", nil)
			}()
			go func() {
				defer wg.Done()
				<-start2
				s2.SetAttentionFull(AttentionDone, "asked", SourceAPI, KindDone, nil)
			}()
			close(start2)
			wg.Wait()
			// Working then done: the question; done then working: working,
			// the question gone.
			a := s2.Info().Attention
			kept, _ := keeps(s2)
			switch {
			case a.State == AttentionNeedsInput && a.ID == q2.ID && a.Since == q2.Since && kept:
			case a.State == AttentionWorking && !kept:
			default:
				t.Fatalf("working against the done: %+v (kept %v)", a, kept)
			}
		})
	}
}

// P23: a reply under way (its text typed, the question claimed) when the
// turn's done comes: the done changes nothing shown, so the reply's Enter
// is written and answers the question, its Since unchanged.
func TestAReplyUnderWayAtTheDoneKeepsItsEnter(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	q := askOwn(t, s, "Which database?", dbChoices...)
	at, release := pauseEnter(t, s)
	reply := submitAsync(s, Submission{Text: "Postgres", By: owner, Question: q.ID})
	if got := written(t, p); got != "Postgres" {
		t.Fatalf("written %q", got)
	}
	atEnter(t, at)
	reportState(t, s, AttentionDone, "asked")
	showsOwn(t, s, q, "the done while the reply is under way")
	release()
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q, want the reply's Enter", got)
	}
	if got := <-reply; got.err != nil || !got.res.Entered {
		t.Fatalf("the reply: %+v, %v", got.res, got.err)
	}
	info := s.Info()
	if info.Attention.State != AttentionNone || info.LastAnswer == nil || info.LastAnswer.Message != "Which database?" {
		t.Fatalf("the reply did not answer: %+v %+v", info.Attention, info.LastAnswer)
	}
}

// P24: a reply under way when the agent works on, then its done: the
// question the reply claimed is removed by the work, not set aside; the
// Enter is written; the done shows done. The question never shows after
// its reply.
func TestAQuestionAReplyClaimedIsNeverSetAside(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	q := askOwn(t, s, "Which database?", dbChoices...)
	at, release := pauseEnter(t, s)
	reply := submitAsync(s, Submission{Text: "Postgres", By: owner, Question: q.ID})
	written(t, p)
	atEnter(t, at)
	reportState(t, s, AttentionWorking, "")
	if kept, _ := keeps(s); kept {
		t.Fatal("the claimed question was set aside")
	}
	release()
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q, want the reply's Enter", got)
	}
	if got := <-reply; got.err != nil || !got.res.Entered {
		t.Fatalf("the reply: %+v, %v", got.res, got.err)
	}
	reportState(t, s, AttentionDone, "chose Postgres")
	shows(t, s, AttentionDone, "the done after the reply")
}

// P28: a handoff typed while the question is set aside, its Enter after
// the turn's done, which shows the question again: the Enter is written
// all the same (the handoff entered, never left typed), it answers
// nothing, and it begins the next turn, in which the agent's work removes
// the question; without that work, a person's reply answers it, typed on
// its own.
func TestAHandoffTypedWhileTheQuestionIsAsideIsEntered(t *testing.T) {
	for _, works := range []bool{true, false} {
		t.Run(fmt.Sprintf("the agent works %v", works), func(t *testing.T) {
			s, p, _ := asking(t, Options{})
			owner := ownerOf(t, s)
			q := askOwn(t, s, "Which database?", dbChoices...)
			reportState(t, s, AttentionWorking, "")
			at, release := pauseEnter(t, s)
			handoff := submitAsync(s, Submission{Text: "Handoff from core: over to you", ByName: "run", UnlessWaiting: true})
			if got := written(t, p); got != "Handoff from core: over to you" {
				t.Fatalf("written %q", got)
			}
			atEnter(t, at)
			reportState(t, s, AttentionDone, "asked")
			showsOwn(t, s, q, "the done with the handoff's Enter to come")
			release()
			if got := written(t, p); got != "\r" {
				t.Fatalf("written %q, want the handoff's Enter", got)
			}
			if got := <-handoff; got.err != nil || !got.res.Entered {
				t.Fatalf("the handoff: %+v, %v", got.res, got.err)
			}
			showsOwn(t, s, q, "the handoff's Enter answered nothing")
			if s.Info().LastAnswer != nil {
				t.Fatalf("the handoff answered: %+v", s.Info().LastAnswer)
			}
			if works {
				reportState(t, s, AttentionWorking, "")
				if kept, _ := keeps(s); kept {
					t.Fatal("the work on the handoff left the question kept")
				}
				reportState(t, s, AttentionDone, "took the handoff")
				shows(t, s, AttentionDone, "the handoff's turn's done")
				return
			}
			res, err := s.Submit(context.Background(), Submission{Text: "Postgres", By: owner, Question: q.ID})
			if err != nil || !res.Entered {
				t.Fatalf("the reply: %+v %v", res, err)
			}
			for _, want := range []string{"Postgres", "\r"} {
				if got := written(t, p); got != want {
					t.Fatalf("written %q, want %q", got, want)
				}
			}
			shows(t, s, AttentionNone, "answered")
		})
	}
}

// A person's reply typed while the question is set aside (a reply box, the
// agent at work) removes it: the person has taken part, and the done shows
// done; its Enter is written.
func TestAPersonsReplyWhileTheQuestionIsAsideRemovesIt(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	askOwn(t, s, "Which database?")
	reportState(t, s, AttentionWorking, "")
	at, release := pauseEnter(t, s)
	reply := submitAsync(s, Submission{Text: "use Postgres", By: owner})
	written(t, p)
	atEnter(t, at)
	if kept, _ := keeps(s); kept {
		t.Fatal("the person's reply left the question set aside")
	}
	reportState(t, s, AttentionDone, "asked")
	shows(t, s, AttentionDone, "the done after the person's reply")
	release()
	if got := written(t, p); got != "\r" {
		t.Fatalf("written %q", got)
	}
	if got := <-reply; got.err != nil || !got.res.Entered {
		t.Fatalf("the reply: %+v, %v", got.res, got.err)
	}
}

// A person's keys, or a line naming no question, begun while the question
// is set aside and written once its turn's done has shown it again (the
// writer busy with another write meanwhile): they answer nothing of it
// (keys never resolve a question), the line's Enter is written all the
// same, the question stays, and a reply naming it answers it. (Codex's
// review of the code, round 1; the question's claim rule, 2026-10-15.)
func TestAPersonsWriteBegunAsideAnswersNothingOnceItShowsAgain(t *testing.T) {
	for _, reply := range []bool{true, false} {
		t.Run(fmt.Sprintf("reply %v", reply), func(t *testing.T) {
			s, p, _ := asking(t, Options{})
			hold, takeAgain := gateWrites(t, p)
			owner := ownerOf(t, s)
			q := askOwn(t, s, "Which database?", dbChoices...)
			reportState(t, s, AttentionWorking, "")
			// A terminal's own report takes the writer, and waits.
			hold()
			go s.Input(owner, []byte("\x1b[3;1R"))
			untilWaiting(t, s, 0)
			done := make(chan error, 1)
			if reply {
				go func() {
					res, err := s.Submit(context.Background(), Submission{Text: "Postgres", By: owner})
					if err == nil && !res.Entered {
						err = fmt.Errorf("not entered: %+v", res)
					}
					done <- err
				}()
			} else {
				go func() { done <- s.Input(owner, []byte("Postgres\r")) }()
			}
			untilWaiting(t, s, 1)
			reportState(t, s, AttentionDone, "asked")
			showsOwn(t, s, q, "the done with the person's write queued")
			takeAgain()
			if got := written(t, p); got != "\x1b[3;1R" {
				t.Fatalf("written %q", got)
			}
			want := []string{"Postgres\r"}
			if reply {
				want = []string{"Postgres", "\r"}
			}
			for _, w := range want {
				if got := written(t, p); got != w {
					t.Fatalf("written %q, want %q", got, w)
				}
			}
			if err := returnsWithin(t, 5*time.Second, "the person's write", func() error { return <-done }); err != nil {
				t.Fatal(err)
			}
			showsOwn(t, s, q, "after the person's write")
			if s.Info().LastAnswer != nil {
				t.Fatalf("the person's write answered the question: %+v", s.Info().LastAnswer)
			}
			if err := s.AnswerInput(context.Background(), owner, q.ID, []byte("SQLite\r")); err != nil {
				t.Fatalf("a reply after it: %v", err)
			}
			written(t, p)
			shows(t, s, AttentionNone, "answered by the reply")
			if kept, _ := keeps(s); kept {
				t.Fatal("the answered question is kept")
			}
		})
	}
}

// A person's keys typed at the question showing and taken by the writer
// once the agent's work has set it aside (the writer busy meanwhile): they
// remove it as they are written, and the turn's done shows done. (Codex's
// review of the code, round 2.)
func TestKeysTypedAtTheQuestionAndWrittenOnceAsideRemoveIt(t *testing.T) {
	s, p, _ := asking(t, Options{})
	hold, takeAgain := gateWrites(t, p)
	owner := ownerOf(t, s)
	askOwn(t, s, "Which database?")
	hold()
	go s.Input(owner, []byte("\x1b[3;1R"))
	untilWaiting(t, s, 0)
	typed := make(chan error, 1)
	go func() { typed <- s.Input(owner, []byte("Postgres\r")) }()
	untilWaiting(t, s, 1)
	reportState(t, s, AttentionWorking, "")
	takeAgain()
	written(t, p)
	if got := written(t, p); got != "Postgres\r" {
		t.Fatalf("written %q", got)
	}
	if err := returnsWithin(t, 5*time.Second, "the keys", func() error { return <-typed }); err != nil {
		t.Fatal(err)
	}
	if kept, _ := keeps(s); kept {
		t.Fatal("the person's keys left the question kept")
	}
	reportState(t, s, AttentionDone, "chose")
	shows(t, s, AttentionDone, "the done after the person's keys")
}

// Keys queued while the question is set aside under the hold, written once
// the turn's done has held it over, that end the hold without answering it
// (Ctrl+U, the held text killed): the question shows again and is not
// claimed by them; a reply then answers it. (Codex's review of the code,
// round 2.)
func TestKeysThatEndTheHoldLeaveTheQuestionTheDoneHeldOver(t *testing.T) {
	s, p, _ := asking(t, Options{})
	hold, takeAgain := gateWrites(t, p)
	owner := ownerOf(t, s)
	q := askOwn(t, s, "Which name?")
	reportState(t, s, AttentionWorking, "")
	go holdNow(s)
	heldShows(t, s)
	hold()
	go s.Input(owner, []byte("\x1b[3;1R"))
	untilWaiting(t, s, 0)
	typed := make(chan error, 1)
	go func() { typed <- s.Input(owner, []byte{0x15}) }()
	untilWaiting(t, s, 1)
	reportState(t, s, AttentionDone, "asked")
	heldShows(t, s)
	takeAgain()
	written(t, p)
	if got := written(t, p); got != "\x15" {
		t.Fatalf("written %q", got)
	}
	if err := returnsWithin(t, 5*time.Second, "the keys", func() error { return <-typed }); err != nil {
		t.Fatal(err)
	}
	showsOwn(t, s, q, "once Ctrl+U ended the hold")
	if err := s.questionWaits(q.ID); err != nil {
		t.Fatalf("a reply to it: %v", err)
	}
	res, err := s.Submit(context.Background(), Submission{Text: "Ada", By: owner, Question: q.ID})
	if err != nil || !res.Entered {
		t.Fatalf("the reply: %+v %v", res, err)
	}
	shows(t, s, AttentionNone, "answered by the reply")
}

// Keys typed while the question showed, queued behind another write, and
// written once the hold has taken its place (and, for the agent's own
// question, the turn's done held it over): when they end the hold without
// answering it (Ctrl+U), the question shows again, unanswered, though they
// were typed at it; a reply then answers it. (Codex's review of the code,
// round 3.)
func TestKeysQueuedBeforeTheHoldDoNotAnswerTheQuestionTheyShowAgain(t *testing.T) {
	for _, own := range []bool{true, false} {
		t.Run(fmt.Sprintf("own %v", own), func(t *testing.T) {
			s, p, _ := asking(t, Options{})
			hold, takeAgain := gateWrites(t, p)
			owner := ownerOf(t, s)
			var q Attention
			if own {
				q = askOwn(t, s, "Which name?")
			} else {
				reportState(t, s, AttentionNeedsInput, "Which name?")
				q = s.Info().Attention
			}
			hold()
			go s.Input(owner, []byte("\x1b[3;1R"))
			untilWaiting(t, s, 0)
			typed := make(chan error, 1)
			go func() { typed <- s.Input(owner, []byte{0x15}) }()
			untilWaiting(t, s, 1)
			go holdNow(s)
			heldShows(t, s)
			if own {
				reportState(t, s, AttentionDone, "asked")
				heldShows(t, s)
			}
			takeAgain()
			written(t, p)
			if got := written(t, p); got != "\x15" {
				t.Fatalf("written %q", got)
			}
			if err := returnsWithin(t, 5*time.Second, "the keys", func() error { return <-typed }); err != nil {
				t.Fatal(err)
			}
			if a := s.Info().Attention; a.ID != q.ID || a.Since != q.Since || a.State != AttentionNeedsInput {
				t.Fatalf("once Ctrl+U ended the hold: %+v, want the question %+v", a, q)
			}
			if s.Info().LastAnswer != nil {
				t.Fatalf("Ctrl+U was recorded as the answer: %+v", s.Info().LastAnswer)
			}
			res, err := s.Submit(context.Background(), Submission{Text: "Ada", By: owner, Question: q.ID})
			if err != nil || !res.Entered {
				t.Fatalf("the reply: %+v %v", res, err)
			}
			shows(t, s, AttentionNone, "answered by the reply")
		})
	}
}

// The question held over by the hold through its turn's done: an OSC
// notification (its turn's, or the ended turn's) changes nothing, and keys
// that end the hold without answering it show it again, the same
// Attention. (Codex's review of the code, round 1.)
func TestANotificationLeavesTheQuestionHeldOver(t *testing.T) {
	s, p, _ := asking(t, Options{})
	owner := ownerOf(t, s)
	q := askOwn(t, s, "Which name?")
	go holdNow(s)
	heldShows(t, s)
	out(p, "\x1b]9;in its turn\x07")
	heldShows(t, s)
	reportState(t, s, AttentionDone, "asked")
	out(p, "\x1b]9;Codex: turn complete\x07")
	heldShows(t, s)
	if err := s.Input(owner, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	written(t, p)
	showsOwn(t, s, q, "once the hold ended")
}

// Ctrl+U begun while the question is set aside under the hold, written
// once the turn's done has held it over: it ends the hold and shows the
// question again, unanswered. A reply to the question then claims it and
// types its text before Ctrl+U's own follow-up runs, and its link is let
// go before its Enter: Ctrl+U answers nothing of the question (the reply's
// claim is not its own), the reply's text holds the turn again, and once a
// person's keys end that hold the question shows, answerable, and nothing
// was recorded as its answer. (Codex's review of the code, round 4.)
func TestKeysThatOnlyShowTheQuestionAgainNeverTakeAReplysClaim(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	hold, takeAgain := gateWrites(t, p)
	owner := ownerOf(t, s)
	var linkRevoked atomic.Bool
	guest := linkedController(t, s, newChanSink(false), &linkRevoked)
	q := askOwn(t, s, "Which name?")
	reportState(t, s, AttentionWorking, "")
	go holdNow(s)
	heldShows(t, s)
	// Ctrl+U waits behind a terminal's own report while the question is
	// set aside; the done holds it over.
	hold()
	go s.Input(owner, []byte("\x1b[3;1R"))
	untilWaiting(t, s, 0)
	reached, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	afterCtrlU := func(data []byte) {
		if string(data) == "\x15" {
			once.Do(func() { close(reached); <-gate })
		}
	}
	s.beforeWritten.Store(&afterCtrlU)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	ctrlU := make(chan error, 1)
	go func() { ctrlU <- s.Input(owner, []byte{0x15}) }()
	untilWaiting(t, s, 1)
	reportState(t, s, AttentionDone, "asked")
	heldShows(t, s)
	takeAgain()
	written(t, p)
	if got := written(t, p); got != "\x15" {
		t.Fatalf("written %q", got)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl+U's write never came to what follows it")
	}
	showsOwn(t, s, q, "once Ctrl+U ended the hold")
	// The reply claims the question and types its text before Ctrl+U's
	// follow-up runs, and waits at its Enter.
	at, release := pauseEnter(t, s)
	reply := submitAsync(s, Submission{Text: "Ada", By: guest, Question: q.ID})
	if got := written(t, p); got != "Ada" {
		t.Fatalf("written %q", got)
	}
	atEnter(t, at)
	close(gate)
	if err := returnsWithin(t, 5*time.Second, "Ctrl+U", func() error { return <-ctrlU }); err != nil {
		t.Fatal(err)
	}
	showsOwn(t, s, q, "after Ctrl+U's follow-up")
	if a := s.Info().LastAnswer; a != nil {
		t.Fatalf("Ctrl+U was recorded as the answer: %+v", a)
	}
	// The reply's link is let go before its Enter: its text holds the turn.
	linkRevoked.Store(true)
	s.DisconnectLink(guest.LinkID)
	release()
	if got := returnsWithin(t, 5*time.Second, "the reply", func() submitted { return <-reply }); !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
		t.Fatalf("the reply: %+v, %v", got.res, got.err)
	}
	heldShows(t, s)
	if err := s.Input(owner, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\x15" {
		t.Fatalf("written %q", got)
	}
	showsOwn(t, s, q, "once the second hold ended")
	if err := s.questionWaits(q.ID); err != nil {
		t.Fatalf("a reply to it: %v", err)
	}
	if a := s.Info().LastAnswer; a != nil {
		t.Fatalf("an answer was recorded: %+v", a)
	}
}

// A person's keys begun while the question is set aside claim it once the
// turn's done has shown it again, and their follow-up waits. Meanwhile a
// guest's line is typed at the question and let go before its Enter (the
// hold holds the question over), Ctrl+U ends that hold and lets the claim
// go, and another reply claims the question and waits at its Enter: the
// first keys' follow-up answers nothing (their claim is gone; the reply's
// is not theirs), and once that reply is let go too and a person's keys
// end the hold, the question shows, answerable, nothing recorded as its
// answer. (Codex's review of the code, round 5.)
func TestAClaimLetGoIsNotTheClaimMadeAgain(t *testing.T) {
	s, p, _ := asking(t, Options{SubmitPause: 300 * time.Millisecond})
	hold, takeAgain := gateWrites(t, p)
	owner := ownerOf(t, s)
	var firstGone, secondGone atomic.Bool
	first := linkedController(t, s, newChanSink(false), &firstGone)
	second := linkedController(t, s, newChanSink(false), &secondGone)
	q := askOwn(t, s, "Which name?")
	reportState(t, s, AttentionWorking, "")
	// The person's keys wait behind a terminal's own report, begun while
	// the question is set aside; the done shows it again.
	hold()
	go s.Input(owner, []byte("\x1b[3;1R"))
	untilWaiting(t, s, 0)
	reached, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	afterKeys := func(data []byte) {
		if string(data) == "Ad" {
			once.Do(func() { close(reached); <-gate })
		}
	}
	s.beforeWritten.Store(&afterKeys)
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	t.Cleanup(openGate)
	keys := make(chan error, 1)
	go func() { keys <- s.Input(owner, []byte("Ad")) }()
	untilWaiting(t, s, 1)
	reportState(t, s, AttentionDone, "asked")
	showsOwn(t, s, q, "the done with the keys queued")
	takeAgain()
	written(t, p)
	if got := written(t, p); got != "Ad" {
		t.Fatalf("written %q", got)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the keys never came to what follows their write")
	}
	// A guest's line typed at the question, let go before its Enter: the
	// hold holds the question over.
	at, release := pauseEnter(t, s)
	line := submitAsync(s, Submission{Text: "x", By: first})
	if got := written(t, p); got != "x" {
		t.Fatalf("written %q", got)
	}
	atEnter(t, at)
	firstGone.Store(true)
	s.DisconnectLink(first.LinkID)
	release()
	if got := returnsWithin(t, 5*time.Second, "the guest's line", func() submitted { return <-line }); !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
		t.Fatalf("the guest's line: %+v, %v", got.res, got.err)
	}
	heldShows(t, s)
	// Ctrl+U ends the hold, the question shows again, and the claim is let go.
	if err := s.Input(owner, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\x15" {
		t.Fatalf("written %q", got)
	}
	showsOwn(t, s, q, "once Ctrl+U ended the hold")
	// Another reply claims it and waits at its Enter.
	at, release = pauseEnter(t, s)
	reply := submitAsync(s, Submission{Text: "Ada", By: second, Question: q.ID})
	if got := written(t, p); got != "Ada" {
		t.Fatalf("written %q", got)
	}
	atEnter(t, at)
	// The first keys' follow-up runs now: it answers nothing.
	openGate()
	if err := returnsWithin(t, 5*time.Second, "the first keys", func() error { return <-keys }); err != nil {
		t.Fatal(err)
	}
	showsOwn(t, s, q, "after the first keys' follow-up")
	if a := s.Info().LastAnswer; a != nil {
		t.Fatalf("the first keys were recorded as the answer: %+v", a)
	}
	// The reply is let go before its Enter: the hold holds the question
	// over, and a person's keys that end it show it again, answerable.
	secondGone.Store(true)
	s.DisconnectLink(second.LinkID)
	release()
	if got := returnsWithin(t, 5*time.Second, "the reply", func() submitted { return <-reply }); !errors.Is(got.err, ErrRevoked) || !got.res.Typed || got.res.Entered {
		t.Fatalf("the reply: %+v, %v", got.res, got.err)
	}
	heldShows(t, s)
	if err := s.Input(owner, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	if got := written(t, p); got != "\x15" {
		t.Fatalf("written %q", got)
	}
	showsOwn(t, s, q, "once the last hold ended")
	if err := s.questionWaits(q.ID); err != nil {
		t.Fatalf("a reply to it: %v", err)
	}
	if a := s.Info().LastAnswer; a != nil {
		t.Fatalf("an answer was recorded: %+v", a)
	}
}

// The agent's own question that keys typed in the terminal reached (the
// held text entered, its choice picked: consumed), then let go for good by
// its working report, leaves the chat saying the terminal answered it
// (#358): the consumption is read before the question is forgotten.
func TestAnOwnQuestionTheTerminalAnsweredLeavesTheChatSayingSo(t *testing.T) {
	s, _, _ := asking(t, Options{})
	q := askOwn(t, s, "Which database?", dbChoices...)
	s.mu.Lock()
	s.consumeLocked(q.ID)
	s.mu.Unlock()
	reportState(t, s, AttentionWorking, "")
	left := waitChat(t, s, func(m ChatMessage) bool { return m.Event == "withdrawn" && m.Ref == q.ID })
	if left.Text != ChatLeftTerminal {
		t.Fatalf("the reason: %+v", left)
	}
}

// A person's keys while the agent's own question waits aside let it go, and
// the chat's withdrawn line is told at once, though no state changes (#358).
func TestKeysWhileAnOwnQuestionWaitsAsideTellTheChatAtOnce(t *testing.T) {
	s, _, _ := asking(t, Options{})
	q := askOwn(t, s, "Which database?", dbChoices...)
	reportState(t, s, AttentionWorking, "")
	if _, aside := keeps(s); !aside {
		t.Fatal("the question is not set aside")
	}
	watcher := newChanSink(false)
	if _, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "watcher"}, watcher); err != nil {
		t.Fatal(err)
	}
	owner := ownerOf(t, s)
	if err := s.Input(owner, []byte("x")); err != nil {
		t.Fatal(err)
	}
	got := waitControl(watcher, func(m map[string]any) bool {
		return chatOf(proto.ChatKindSystem)(m) && m["event"] == "withdrawn" && m["ref"] == q.ID
	})
	if got == nil {
		t.Fatalf("the withdrawn line was not told; the chat %+v", s.ChatHistory())
	}
	if got["text"] != ChatLeftTerminal {
		t.Fatalf("the reason told: %v", got)
	}
}

// An agent's clear while its own question waits aside under a held turn
// lets the question go, and the chat's withdrawn line is told at once,
// though that path records no change (#358, Codex's second round).
func TestAnOwnQuestionLetGoUnderAHoldTellsTheChat(t *testing.T) {
	s, _, _ := asking(t, Options{})
	q := askOwn(t, s, "Which database?", dbChoices...)
	reportState(t, s, AttentionWorking, "")
	if _, aside := keeps(s); !aside {
		t.Fatal("the question is not set aside")
	}
	// The hold blocks until the turn is let go, as the other tests take it.
	go holdNow(s)
	heldShows(t, s)
	watcher := newChanSink(false)
	if _, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "watcher"}, watcher); err != nil {
		t.Fatal(err)
	}
	reportState(t, s, AttentionNone, "")
	if kept, _ := keeps(s); kept {
		t.Fatal("the question is kept after the clear")
	}
	got := waitControl(watcher, func(m map[string]any) bool {
		return chatOf(proto.ChatKindSystem)(m) && m["event"] == "withdrawn" && m["ref"] == q.ID
	})
	if got == nil {
		t.Fatalf("the withdrawn line was not told; the chat %+v", s.ChatHistory())
	}
}

// A question withdrawn by its id (a crew task's blocker cleared, #353)
// goes only if it is that question: the agent's own kept aside under a
// question shown since is dropped and the question shown stays, its
// choices with it; a question that is not the one named is untouched.
// TryAskID names the question its ask put up (Codex's review of #353).
func TestWithdrawingAQuestionTakesOnlyThatOne(t *testing.T) {
	s, _, _ := asking(t, Options{})
	id, err := s.TryAskID("Which heading style?", SourceAPI, "", nil)
	if err != nil || id == "" || s.Info().Attention.ID != id {
		t.Fatalf("TryAskID: %q %v, shown %+v", id, err, s.Info().Attention)
	}
	// Work sets it aside, and a dialog shows over it.
	reportState(t, s, AttentionWorking, "")
	if _, aside := keeps(s); !aside {
		t.Fatal("the question is not set aside")
	}
	s.SetAttentionFull(AttentionNeedsInput, "Allow Bash?", SourceAPI, KindPermission, []Option{{Label: "Yes", Input: "1"}, {Label: "No", Input: "2"}})
	dialog := s.Info().Attention
	if dialog.ID == id || dialog.State != AttentionNeedsInput {
		t.Fatalf("the dialog: %+v", dialog)
	}
	s.WithdrawQuestion(id)
	if got := s.Info().Attention; got.ID != dialog.ID || got.State != AttentionNeedsInput || len(got.Options) != 2 {
		t.Fatalf("withdrawing the question set aside took the dialog: %+v", got)
	}
	if kept, _ := keeps(s); kept {
		t.Fatal("the question set aside is still kept")
	}
	// Another id leaves the dialog; its own id takes it.
	s.WithdrawQuestion("not-this-one")
	if got := s.Info().Attention; got.ID != dialog.ID {
		t.Fatalf("an unrelated id took the dialog: %+v", got)
	}
	s.WithdrawQuestion(dialog.ID)
	if got := s.Info().Attention; got.State == AttentionNeedsInput {
		t.Fatalf("the dialog named by its id stays: %+v", got)
	}
}

// The same question asked again names the same id (a blocker reported
// twice), and a question the hold of the turn holds over is withdrawn with
// the agent's own copy, so the hold's end does not show it again (Codex's
// review of #353).
func TestWithdrawingAQuestionReachesTheOneHeldOver(t *testing.T) {
	s, _, _ := asking(t, Options{})
	id, err := s.TryAskID("Which heading style?", SourceAPI, "", nil)
	if err != nil || id == "" {
		t.Fatalf("TryAskID: %q %v", id, err)
	}
	again, err := s.TryAskID("Which heading style?", SourceAPI, "", nil)
	if err != nil || again != id {
		t.Fatalf("the same question asked again: %q, want %q (%v)", again, id, err)
	}
	// The hold of the turn takes its place: the question is held over, and the agent's own.
	go holdNow(s)
	heldShows(t, s)
	s.mu.Lock()
	held, own := s.heldOver.ID, s.own.ID
	s.mu.Unlock()
	if held != id || own != id {
		t.Fatalf("held over %q, own %q, want %q", held, own, id)
	}
	s.WithdrawQuestion(id)
	s.mu.Lock()
	held, own = s.heldOver.ID, s.own.ID
	s.mu.Unlock()
	if held != "" || own != "" {
		t.Fatalf("after the withdrawal: held over %q, own %q", held, own)
	}
	if a := s.Info().Attention; a.Source != SourceSubmit {
		t.Fatalf("the hold no longer shows: %+v", a)
	}
}
