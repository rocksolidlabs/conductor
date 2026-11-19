package crew

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// codexQuestions are Codex's questions as the built-in catalog entry has
// them, its startup ones among them.
func codexQuestions() []session.StartupQuestion {
	codex, _ := catalog.Default().Get("codex")
	var out []session.StartupQuestion
	for _, q := range codex.Questions {
		var answers []session.Option
		for _, a := range q.Answers {
			answers = append(answers, session.Option{Label: a.Label, Input: a.Input})
		}
		var words *regexp.Regexp
		if q.Words != "" {
			words = regexp.MustCompile(q.Words)
		}
		out = append(out, session.StartupQuestion{Pattern: regexp.MustCompile(q.Prompt), Words: words, Anytime: q.Anytime, Answers: answers})
	}
	return out
}

// The agent's own update dialog holds a member's prompt, and the run's log
// says where a person answers it: with the choices the session offers for
// it in the run page's chat or on the member's session page, or in its
// terminal. Nothing answers it.
func TestTheRunLogSaysWhereAnUpdateDialogIsAnswered(t *testing.T) {
	e, fl := newEngine(t)
	fl.questions = codexQuestions()
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, _ *session.Local) {
		_ = p.Print("\x1b[2J\x1b[H  Update available 0.167.2 -> 0.170.0\r\n› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n  Press enter to continue\r\n")
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitLogged(t, e, run.ID, "lead asks", 1, 10*time.Second)
	got, _ := e.Get(run.ID)
	var note string
	for _, entry := range got.Log {
		if strings.Contains(entry.Message, "lead asks") {
			note = entry.Message
		}
	}
	want := `lead asks "Update available 0.167.2 -> 0.170.0": answer it with one of its choices ("Skip this update", "Skip until next version") in the run page's chat or on lead's session page, or in its terminal; its prompt waits`
	if note != want {
		t.Fatalf("the run log says %q, want %q", note, want)
	}
	_, p := fl.member("lead")
	if w := typed(p); len(w) != 0 {
		t.Fatalf("typed into the update dialog: %q", w)
	}
}

// A question the page offers no choice for is answered in the terminal; one
// that mirrors a decision through it; a Brakeman turn's in its terminal; and
// the session's choices are named only while it shows that question.
func TestHeldWordsSayWhereTheQuestionIsAnswered(t *testing.T) {
	crewRun, brakeman := &run{}, &run{class: ClassBrakeman}
	opts := []session.Option{{Label: "Yes", Input: "1"}}
	for _, tc := range []struct {
		name string
		r    *run
		att  session.Attention
		want string
	}{
		{"no choices", crewRun, session.Attention{Message: "Trust this folder?"}, `lead asks "Trust this folder?": answer it in lead's terminal`},
		{"one choice", crewRun, session.Attention{Message: "Trust this folder?", Options: opts}, `lead asks "Trust this folder?": answer it with its choice "Yes" in the run page's chat or on lead's session page, or in its terminal`},
		{"another question shown now", crewRun, session.Attention{Message: "Sign in?", Options: opts}, `lead asks "Trust this folder?": answer it in lead's terminal`},
		{"answered in the terminal", crewRun, session.Attention{Message: "Trust this folder?", Options: opts, Answered: session.AnsweredTerminal}, `lead asks "Trust this folder?": answer it in lead's terminal`},
		{"a decision's", crewRun, session.Attention{Message: "Trust this folder?", Options: opts, Decision: "dr-3"}, `lead asks "Trust this folder?": answer dr-3 where decisions are answered`},
		{"a Brakeman turn", brakeman, session.Attention{Message: "Trust this folder?", Options: opts}, `lead asks "Trust this folder?": answer it in lead's terminal`},
	} {
		if got := heldWords(tc.r, "lead", "Trust this folder?", tc.att); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Codex's update dialog as a member's screen shows it: whole, and in a
// window five rows tall, its heading off the screen.
const (
	updateWhole = "\x1b[2J\x1b[H\r\n  Update available · 0.163.0 → 0.164.0\r\n  Release notes: https://github.com/openai/codex/releases/latest\r\n\r\n› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n\r\n  enter continue · esc skip"
	updateShort = "\x1b[2J\x1b[H› 1. Update now (runs `npm install -g @openai/codex`)\r\n  2. Skip\r\n  3. Skip until next version\r\n\r\n  enter continue · esc skip"
	updateWords = "Update available 0.163.0 → 0.164.0"
)

// heldRun is a run whose member lead's readiness wait looks only at the
// ticks the test hands it and is never ready by its output's quiet, only by
// its agent's report; its session shows what the test prints, through a
// controlling viewer at rows rows. look hands the wait three ticks: the
// first two have each been taken by a look begun after the call, so the
// wait has looked twice at what the session shows then. The other members
// are ready at once.
func heldRun(t *testing.T, rows uint16, members ...Member) (e *Engine, fl *fakeLauncher, runID string, subs <-chan *session.Subscription, look func(), ticks chan<- time.Time) {
	t.Helper()
	e, fl = newEngine(t)
	fl.trust = trustRe
	fl.questions = codexQuestions()
	tc := make(chan time.Time)
	e.await = func(ctx context.Context, l *session.Local, held func(string)) error {
		if l.Info().Name != "lead" {
			return nil
		}
		return readyWait{poll: time.Hour, quiet: time.Hour, ticks: tc}.await(ctx, l, held)
	}
	sc := make(chan *session.Subscription, 4)
	fl.onLaunch = func(name string, p *sessiontest.FakeProc, l *session.Local) {
		if name != "lead" {
			return
		}
		sub, err := l.Attach("", session.RoleControl, "", 80, rows, nopSink{})
		if err == nil {
			err = l.Resize(sub, 80, rows)
		}
		if err != nil {
			t.Error(err)
			return
		}
		sc <- sub
	}
	run, err := e.Launch(t.Context(), testCrew(append([]Member{immediate("lead", "Plan it.")}, members...)...))
	if err != nil {
		t.Fatal(err)
	}
	look = func() {
		t.Helper()
		for range 3 {
			select {
			case tc <- time.Now():
			case <-time.After(30 * time.Second):
				t.Fatal("the readiness wait does not look")
			}
		}
	}
	return e, fl, run.ID, sc, look, tc
}

// asksLines is the run's log lines of member lead's questions.
func asksLines(t *testing.T, e *Engine, runID string) []string {
	t.Helper()
	got, _ := e.Get(runID)
	var out []string
	for _, entry := range got.Log {
		if strings.HasPrefix(entry.Message, "lead asks") {
			out = append(out, entry.Message)
		}
	}
	return out
}

const updateLine = `lead asks "` + updateWords + `": answer it with one of its choices ("Skip this update", "Skip until next version") in the run page's chat or on lead's session page, or in its terminal; its prompt waits`

// One update dialog of a member's agent is one line in the run's log, in
// the dialog's own words (its heading), though the session reads it three
// times with three ids as a page attaching the member's terminal makes the
// agent draw it again: whole, then in a window too short for its heading
// (the words its first choice's row gives), then whole again.
func TestOneDialogReadThreeTimesIsOneLine(t *testing.T) {
	e, fl, runID, subs, look, _ := heldRun(t, 24)
	sub := <-subs
	l, p := fl.member("lead")
	_ = p.Print(updateWhole)
	first := untilShown(t, l, "the dialog", func(a session.Attention) bool { return startupShown(a) && a.Message == updateWords })
	look()
	if err := l.Resize(sub, 80, 5); err != nil {
		t.Fatal(err)
	}
	_ = p.Print(updateShort)
	cut := untilShown(t, l, "the dialog read off its choice", func(a session.Attention) bool { return startupShown(a) && a.ID != first.ID })
	if !strings.HasPrefix(cut.Message, "Update now (runs") {
		t.Fatalf("the short window's reading: %+v", cut)
	}
	look()
	if err := l.Resize(sub, 80, 24); err != nil {
		t.Fatal(err)
	}
	_ = p.Print(updateWhole)
	untilShown(t, l, "the dialog whole again", func(a session.Attention) bool { return startupShown(a) && a.ID != cut.ID && a.ID != first.ID })
	look()
	if got := asksLines(t, e, runID); len(got) != 1 || got[0] != updateLine {
		t.Fatalf("the run's log: %q", got)
	}
	if w := typed(p); len(w) != 0 {
		t.Fatalf("typed into the dialog: %q", w)
	}
}

// Two different questions in one wait are two lines, each in its own
// words: the update dialog, then the folder's trust question drawn in its
// place.
func TestTwoDialogsInOneWaitAreTwoLines(t *testing.T) {
	e, fl, runID, subs, look, _ := heldRun(t, 24)
	<-subs
	l, p := fl.member("lead")
	_ = p.Print(updateWhole)
	first := untilShown(t, l, "the dialog", func(a session.Attention) bool { return startupShown(a) && a.Message == updateWords })
	look()
	_ = p.Print(trustScreen)
	untilShown(t, l, "the trust question", func(a session.Attention) bool { return startupShown(a) && a.ID != first.ID })
	look()
	got := asksLines(t, e, runID)
	if len(got) != 2 || got[0] != updateLine || !strings.HasPrefix(got[1], `lead asks "Trust this folder?"`) {
		t.Fatalf("the run's log: %q", got)
	}
}

// A dialog the session never reads with its own words (a window too short
// for its heading from its first draw) is one line at the wait's next look
// holding it, without the words its first choice's row gives, and only
// one however long it holds the prompt.
func TestADialogNeverReadWithItsHeadingIsOneLineWithoutItsCutWords(t *testing.T) {
	e, fl, runID, subs, look, _ := heldRun(t, 5)
	<-subs
	l, p := fl.member("lead")
	_ = p.Print(updateShort)
	untilShown(t, l, "the dialog read off its choice", func(a session.Attention) bool { return startupShown(a) })
	look()
	look()
	want := `lead asks a question of its agent's own: answer it with one of its choices ("Skip this update", "Skip until next version") in the run page's chat or on lead's session page, or in its terminal; its prompt waits`
	if got := asksLines(t, e, runID); len(got) != 1 || got[0] != want {
		t.Fatalf("the run's log: %q", got)
	}
}

// The same dialog in two waits (the member answered it, its prompt went in,
// and a later start of it asks again) is one line in each wait.
func TestTheSameDialogInTwoWaitsIsOneLineInEach(t *testing.T) {
	e, fl, runID, subs, look, ticks := heldRun(t, 24, immediate("qa", "Check it."))
	for wait := 1; wait <= 2; wait++ {
		sub := <-subs
		l, p := fl.member("lead")
		_ = p.Print(updateWhole)
		first := untilShown(t, l, "the dialog", func(a session.Attention) bool { return startupShown(a) && a.Message == updateWords })
		look()
		if err := l.Resize(sub, 80, 5); err != nil {
			t.Fatal(err)
		}
		_ = p.Print(updateShort)
		cut := untilShown(t, l, "the dialog read off its choice", func(a session.Attention) bool { return startupShown(a) && a.ID != first.ID })
		look()
		if got := asksLines(t, e, runID); len(got) != wait {
			t.Fatalf("wait %d: the run's log: %q", wait, got)
		}
		if wait == 2 {
			break
		}
		// The wait is left the same way whichever of the session's two
		// looks at the next screen runs first (the startup questions'
		// watcher's and the screen's, after one quiet period): the dialog
		// is drawn whole again at the window's full size, its heading read
		// (one more reading of the dialog, no line of its own), and
		// answered in its terminal by a key that surely picks a choice,
		// "Skip until next version", which puts the dialog's output behind
		// at once. An Escape there is held as an answer until the screen
		// shows the dialog gone, and how such a question leaves depends on
		// the order of the two looks: that is the session's, tracked in
		// issue 1017, not this test's.
		if err := l.Resize(sub, 80, 24); err != nil {
			t.Fatal(err)
		}
		_ = p.Print(updateWhole)
		untilShown(t, l, "the dialog whole again", func(a session.Attention) bool {
			return startupShown(a) && a.Message == updateWords && a.ID != first.ID && a.ID != cut.ID
		})
		look()
		if got := asksLines(t, e, runID); len(got) != wait {
			t.Fatalf("wait %d, the dialog whole again: the run's log: %q", wait, got)
		}
		// The agent says it waits at its prompt, and the prompt goes in.
		if err := l.Input(sub, []byte("3")); err != nil {
			t.Fatal(err)
		}
		if b := <-p.Input; string(b) != "3" {
			t.Fatalf("the person typed %q", b)
		}
		_ = p.Print("\x1b[2J\x1b[H> ")
		// The question goes at the screen's next look: the bound only
		// bounds a hang.
		for deadline := time.Now().Add(30 * time.Second); startupShown(l.Info().Attention); {
			if time.Now().After(deadline) {
				t.Fatalf("the dialog answered, the session still shows %+v", l.Info().Attention)
			}
			time.Sleep(10 * time.Millisecond)
		}
		l.SetAttention(session.AttentionNeedsInput, "ready", session.SourceAPI)
		// The wait's look finds it ready, and the prompt is typed.
		var typedIn string
		deadline := time.After(30 * time.Second)
		for !strings.HasSuffix(typedIn, "\r") {
			select {
			case ticks <- time.Now():
			case b := <-p.Input:
				typedIn += string(b)
			case <-deadline:
				t.Fatalf("the prompt was not typed: %q", typedIn)
			}
		}
		if typedIn != "Plan it.\r" {
			t.Fatalf("typed %q", typedIn)
		}
		waitStatus(t, e, runID, "lead", MemberRunning, 10*time.Second)
		p.End(0)
		<-l.Ended()
		waitStatus(t, e, runID, "lead", MemberEnded, 10*time.Second)
		if _, err := e.ResumeMember(t.Context(), runID, "lead", ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, line := range asksLines(t, e, runID) {
		if line != updateLine {
			t.Fatalf("a line: %q", line)
		}
	}
}
