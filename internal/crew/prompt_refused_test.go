package crew

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A member's prompt the session refuses while it shows no question, and
// one its agent never reports taking, each say so once in the run's log;
// nothing is typed again.

// memberOf is the member named name in run as the engine reports it now.
func memberOf(t *testing.T, e *Engine, runID, name string) MemberState {
	t.Helper()
	r, ok := e.Get(runID)
	if !ok {
		t.Fatalf("run %s not found", runID)
	}
	for _, m := range r.Members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no member %s", name)
	return MemberState{}
}

// waitMember waits up to 30 s for the member named name to satisfy ok.
func waitMember(t *testing.T, e *Engine, runID, name string, ok func(MemberState) bool) MemberState {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		m := memberOf(t, e, runID, name)
		if ok(m) {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("member %s never matched: %+v", name, m)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A prompt typed, entered and entered again, whose agent (one that
// reports taking a prompt) never reports taking it: the run's log says so
// once, as an error; the member reads as waiting on a person, marked
// prompted all the same; nothing more is written. Its agent's turn ends
// the wait, and its done starts the member after it. An agent that
// reports nothing gets its prompt and its Enter, as ever, and no word.
func TestAPromptItsAgentNeverReportsTakingIsSaidOnce(t *testing.T) {
	word := "typed lead's prompt, and its agent did not report taking it: check its terminal, and type the prompt there if its input is empty"
	for _, confirm := range []bool{true, false} {
		t.Run(map[bool]string{true: "an agent that reports taking a prompt", false: "an agent that reports nothing"}[confirm], func(t *testing.T) {
			e, fl := newEngine(t)
			fl.confirm, fl.confirmWait = confirm, 50*time.Millisecond
			fl.onLaunch = func(_ string, p *sessiontest.FakeProc, _ *session.Local) { _ = p.Print("> ") }
			run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), after("next", "Go on.", "lead")))
			if err != nil {
				t.Fatal(err)
			}
			waitLogged(t, e, run.ID, "typed lead's prompt", 1+map[bool]int{true: 1, false: 0}[confirm], 30*time.Second)
			if n := logCount(t, e, run.ID, word); n != map[bool]int{true: 1, false: 0}[confirm] {
				t.Fatalf("the word is in the run's log %d times:\n%s", n, logLines(e, run.ID))
			}
			lead := memberOf(t, e, run.ID, "lead")
			if lead.NeedsInput != confirm {
				t.Fatalf("lead: %+v", lead)
			}
			l, p := fl.member("lead")
			want := "Plan it. Goal: ship /v1/users\r"
			if confirm {
				want += "\r"
			}
			if got := strings.Join(typed(p), ""); !strings.HasSuffix(got, "\r") || strings.Count(got, "\r") != strings.Count(want, "\r") {
				t.Fatalf("lead was written %q", got)
			}
			// Its agent's own turn: the wait ends; its done starts next.
			if err := l.TrySetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil); err != nil {
				t.Fatal(err)
			}
			waitMember(t, e, run.ID, "lead", func(m MemberState) bool { return !m.NeedsInput })
			if err := l.TrySetAttentionFull(session.AttentionDone, "done", session.SourceAPI, session.KindDone, nil); err != nil {
				t.Fatal(err)
			}
			waitMember(t, e, run.ID, "next", func(m MemberState) bool { return m.Status == MemberRunning })
			if got := typed(p); len(got) != 0 {
				t.Fatalf("lead was written more: %q", got)
			}
			if n := logCount(t, e, run.ID, word); n != map[bool]int{true: 1, false: 0}[confirm] {
				t.Fatalf("the word is in the run's log %d times:\n%s", n, logLines(e, run.ID))
			}
		})
	}
}

// Codex's sign-in screen holds a member's prompt: nothing is typed into
// it, and the run's log says why the member waits.
func TestCodexsSignInScreenHoldsAPrompt(t *testing.T) {
	e, fl := newEngine(t)
	fl.questions = codexQuestions()
	fl.trust = trustRe
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, _ *session.Local) {
		_ = p.Print("\x1b[2J\x1b[H\r\n  Welcome to Codex, OpenAI's command-line coding agent\r\n\r\n  Sign in with ChatGPT to use Codex as part of your paid plan\r\n\r\n> 1. Sign in with ChatGPT\r\n  2. Sign in with Device Code\r\n  3. Provide your own API key\r\n\r\n  Press enter to continue")
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitLogged(t, e, run.ID, "lead's prompt waits: "+session.StartupChoiceWords, 1, 30*time.Second)
	_, p := fl.member("lead")
	if w := typed(p); len(w) != 0 {
		t.Fatalf("typed into the sign-in screen: %q", w)
	}
}

// A prompt refused while the session shows no question (a numbered row on
// the agent's screen during its start, which may be a question's choice) is
// noted once in the run's log for the wait, with the refusal's words, and
// typed once the screen is the agent's prompt; nothing of it was typed
// before.
func TestAPromptRefusedWithNoQuestionShownIsNotedOnce(t *testing.T) {
	e, fl := newEngine(t)
	fl.trust = trustRe
	// The readiness waits the prompt takes, counted: each refusal is
	// followed by another (the prompt tried again).
	var awaits atomic.Int32
	ready := e.await
	e.await = func(ctx context.Context, l *session.Local, held func(string)) error {
		awaits.Add(1)
		return ready(ctx, l, held)
	}
	listed := make(chan struct{})
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, _ *session.Local) {
		_ = p.Print("\x1b[2J\x1b[HReading the repository:\r\n  1. Read the code\r\n  2. Fix the bug\r\n")
		<-listed
		_ = p.Print("\x1b[2J\x1b[H> ")
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	note := "lead's prompt waits: " + session.StartupChoiceWords
	waitLogged(t, e, run.ID, note, 1, 30*time.Second)
	_, p := fl.member("lead")
	if w := typed(p); len(w) != 0 {
		t.Fatalf("typed while the screen showed a numbered row: %q", w)
	}
	// Refused again at least once more before the screen changes.
	deadline := time.Now().Add(30 * time.Second)
	for awaits.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the prompt was not tried again: %d waits", awaits.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(listed)
	waitLogged(t, e, run.ID, "typed lead's prompt", 1, 30*time.Second)
	if n := logCount(t, e, run.ID, note); n != 1 {
		t.Fatalf("the refusal was noted %d times:\n%s", n, logLines(e, run.ID))
	}
	if n := logCount(t, e, run.ID, "without its Enter"); n != 0 {
		t.Fatalf("typed without its Enter:\n%s", logLines(e, run.ID))
	}
}

// A managed run's next attempt reuses the member with a session of its
// own: a prompt the first attempt's agent never reported taking holds only
// that session. Its error line is written once; the member starting its
// second attempt, held in its readiness wait, does not read as waiting on
// a person.
func TestANewAttemptIsNotHeldByAnEarlierAttemptsPrompt(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := managedEngine(t)
	fl.confirm, fl.confirmWait = true, 50*time.Millisecond
	ready := e.await
	var awaits atomic.Int32
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	e.await = func(ctx context.Context, l *session.Local, held func(string)) error {
		if awaits.Add(1) > 1 {
			select {
			case <-hold:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return ready(ctx, l, held)
	}
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	word := "typed lead's prompt, and its agent did not report taking it"
	waitLogged(t, e, run.ID, word, 1, 30*time.Second)
	first := waitMember(t, e, run.ID, "lead", func(m MemberState) bool { return m.NeedsInput })
	if err := e.CancelTask(context.Background(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	if err := e.RetryTask(context.Background(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	second := waitMember(t, e, run.ID, "lead", func(m MemberState) bool {
		return m.SessionID != "" && m.SessionID != first.SessionID && awaits.Load() > 1
	})
	if second.NeedsInput {
		t.Fatalf("the second attempt's member reads as waiting on a person: %+v", second)
	}
	if n := logCount(t, e, run.ID, word); n != 1 {
		t.Fatalf("the word is in the run's log %d times:\n%s", n, logLines(e, run.ID))
	}
}

// The member stops reading as waiting on a person once its agent reports
// a turn, and stays so when the session then shows nothing.
func TestAMemberStopsWaitingOnceItsAgentReportsATurn(t *testing.T) {
	e, fl := newEngine(t)
	fl.confirm, fl.confirmWait = true, 50*time.Millisecond
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, _ *session.Local) { _ = p.Print("> ") }
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitLogged(t, e, run.ID, "typed lead's prompt, and its agent did not report taking it", 1, 30*time.Second)
	waitMember(t, e, run.ID, "lead", func(m MemberState) bool { return m.NeedsInput })
	l, _ := fl.member("lead")
	if err := l.TrySetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	waitMember(t, e, run.ID, "lead", func(m MemberState) bool { return !m.NeedsInput })
	if err := l.TrySetAttentionFull(session.AttentionNone, "", session.SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	if a := l.Info().Attention; a.State != session.AttentionNone {
		t.Fatalf("the session shows %+v", a)
	}
	if m := memberOf(t, e, run.ID, "lead"); m.NeedsInput {
		t.Fatalf("lead reads as waiting again: %+v", m)
	}
}
