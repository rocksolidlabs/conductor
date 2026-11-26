package crew

import (
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// What a run says, and does, when a line for a member is held back.

// stubPrompt is the prompt shape of the sessions here: a "> " row last.
var stubPrompt = regexp.MustCompile(`(?m)^>( [^\n]*)?\s*\z`)

// shows waits until l's screen shows want.
func shows(t *testing.T, l *session.Local, want string) {
	t.Helper()
	waitTrue(t, "the screen shows "+want, func() bool { return strings.Contains(strings.Join(l.ScreenLines(24), "\n"), want) })
}

// A handoff to a member whose screen is not its agent's prompt is held
// back, nothing typed, and the run's log says so once in plain words,
// however often it is tried again; once the member's prompt shows, the
// member's next activity types it.
func TestAHandoffHeldAtAScreenThatIsNotThePromptIsSaidOnce(t *testing.T) {
	e, fl := newEngine(t)
	fl.shape = stubPrompt
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, l *session.Local) {
		// Its prompt drawn, then its agent asks: the member is ready, and
		// its prompt is typed at its prompt (one typed before the screen
		// shows it is tried again a poll later).
		_ = p.Print("> ")
		l.SetAttention(session.AttentionNeedsInput, "what next?", session.SourceAPI)
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("tests", "Test it.")))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lead", "tests"} {
		waitStatus(t, e, run.ID, name, MemberRunning, 5*time.Second)
		l, p := fl.member(name)
		typed(p)
		l.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	}
	var refused atomic.Int32
	e.tried = func(member string, typed bool) {
		if member == "tests" && !typed {
			refused.Add(1)
		}
	}
	lead, _ := fl.member("lead")
	tests, p := fl.member("tests")
	_ = p.Print("\x1b[2J\x1b[H  Your session has paused\r\n\r\n  Press Enter to resume · Esc to leave")
	shows(t, tests, "Your session has paused")

	const said = "handoff queued from lead to tests: tests shows a screen that is not its prompt"
	lead.Record(handoffEntry("tests", "the handlers are in"))
	waitLogged(t, e, run.ID, said, 1, 5*time.Second)
	// Tried again at the member's next activity: still held, said once.
	before := refused.Load()
	tests.SetAttention(session.AttentionDone, "", session.SourceAPI)
	waitTrue(t, "a try after the activity", func() bool { return refused.Load() > before })
	if got := typed(p); len(got) != 0 {
		t.Fatalf("typed %q at a screen that is not the prompt", got)
	}
	if n := logCount(t, e, run.ID, said); n != 1 {
		t.Fatalf("said %d times", n)
	}

	// The prompt again, and the member's next activity: typed.
	_ = p.Print("\x1b[2J\x1b[H> ")
	shows(t, tests, ">")
	tests.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	if got := waitTyped(t, p, 5*time.Second); got != "Handoff from lead: the handlers are in\r" {
		t.Fatalf("typed %q", got)
	}
	stopRun(t, e, run.ID)
}

// A reminder that a done does not finish a task, held back (the member's
// session waits on a prompt as the done comes), is not spent: the run's
// log says it waits, and the member's next done reminds it, once.
func TestAReminderHeldBackIsGivenAtTheNextDone(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	done := func() {
		e.OnActivity(b.Attempt.SessionID, session.ActivityEntry{Type: session.ActivityAttention, Message: "done", At: time.Now().UTC()}, session.AttentionDone, "")
	}
	l.SetAttention(session.AttentionNeedsInput, "Allow edit?", session.SourceAPI)
	done()
	waitLogged(t, e, run.ID, "reports done without a result; the reminder waits for its next done", 1, 10*time.Second)
	if n := logCount(t, e, run.ID, "reminded once"); n != 0 {
		t.Fatalf("a reminder held back was said given: %d", n)
	}
	l.SetAttention(session.AttentionNone, "", session.SourceAPI)
	done()
	lineWith(t, p, "a done does not finish task")
	waitLogged(t, e, run.ID, "reports done without a result; reminded once", 1, 10*time.Second)
	stopRun(t, e, run.ID)
}
