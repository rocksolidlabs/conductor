package crew

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// nopSink is a session.Sink that drops what it is sent: a person's
// connection, for a test that types as one.
type nopSink struct{}

func (nopSink) WriteFrame([]byte) error { return nil }
func (nopSink) Close(error)             {}

// A done the agent reports after the text of its prompt but before the Enter
// is the idle state the prompt has not yet reached: it starts nothing. One
// after the Enter does.
func TestADoneBeforeTheEnterDoesNotCount(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		if member != "core" {
			return
		}
		l.SetAttention(session.AttentionNeedsInput, "what next?", session.SourceAPI)
		<-p.Input // the text: the agent has not had the Enter yet
		l.SetAttention(session.AttentionDone, "idle", session.SourceAPI)
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("core", "Build it."), after("tests", "", "core")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
	if n := logCount(t, e, run.ID, "core is done"); n != 0 {
		t.Fatalf("a done before the Enter started the members after core (%d)", n)
	}
	core, _ := fl.member("core")
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	core.SetAttention(session.AttentionDone, "built", session.SourceAPI)
	waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)
}

// A member whose agent's done carries the same words every turn (aider's
// "aider is waiting") gives the engine each turn's end: the done it shows
// as its prompt is typed ends at the prompt's Enter, so its next done,
// with the same words, is recorded and starts the members after it.
func TestADoneWithTheIdleWordsAfterThePromptCounts(t *testing.T) {
	e, fl := newEngine(t)
	const words = "aider is waiting"
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		if member == "core" {
			l.SetAttention(session.AttentionDone, words, session.SourceAPI)
		}
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("core", "Build it."), after("tests", "", "core")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
	if n := logCount(t, e, run.ID, "core is done"); n != 0 {
		t.Fatalf("the done before the prompt started the members after core (%d)", n)
	}
	core, _ := fl.member("core")
	core.SetAttention(session.AttentionDone, words, session.SourceAPI)
	waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)
}

// A prompt accepted while the member's program takes it slowly (its text
// still being taken when its time ran out) counts as given only with its
// Enter: a done the agent reports before then starts nothing, one after it
// does.
func TestADoneBeforeAPendingPromptsEnterDoesNotCount(t *testing.T) {
	e, fl := newEngine(t)
	fl.inputWait = 200 * time.Millisecond
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		if member != "core" {
			return
		}
		// The program takes nothing for now: the prompt's text waits.
		for len(p.Input) < cap(p.Input) {
			p.Input <- nil
		}
		l.SetAttention(session.AttentionNeedsInput, "what next?", session.SourceAPI)
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("core", "Build it."), after("tests", "", "core")))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for logCount(t, e, run.ID, "waits for core to take its input") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the prompt was never accepted as pending")
		}
		time.Sleep(20 * time.Millisecond)
	}
	core, p := fl.member("core")
	core.SetAttention(session.AttentionDone, "idle", session.SourceAPI)
	time.Sleep(300 * time.Millisecond)
	if n := logCount(t, e, run.ID, "core is done"); n != 0 {
		t.Fatalf("a done before the pending prompt's Enter started the members after core (%d)", n)
	}
	// The program reads again: the text, then its Enter.
	var typedNow []string
	for len(typedNow) < 2 {
		select {
		case b := <-p.Input:
			if b != nil {
				typedNow = append(typedNow, string(b))
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("typed %q", typedNow)
		}
	}
	if typedNow[0] != "Build it." || typedNow[1] != "\r" {
		t.Fatalf("typed %q", typedNow)
	}
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	core.SetAttention(session.AttentionDone, "built", session.SourceAPI)
	waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)
}

// A prompt whose Enter waits behind another's keystrokes the program does
// not take counts as given only once the Enter is taken: a done the agent
// reports while it waits starts nothing.
func TestADoneWhileAPromptsEnterWaitsDoesNotCount(t *testing.T) {
	e, fl := newEngine(t)
	fl.pause = 300 * time.Millisecond
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		if member != "core" {
			return
		}
		l.SetAttention(session.AttentionNeedsInput, "what next?", session.SourceAPI)
		<-p.Input // the text
		// The program takes nothing more for now, and a person's keys come
		// ahead of the Enter.
		for len(p.Input) < cap(p.Input) {
			p.Input <- nil
		}
		sub, err := l.Attach("", session.RoleControl, "", 0, 0, nopSink{})
		if err != nil {
			return
		}
		go l.Input(sub, []byte("x"))
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("core", "Build it."), after("tests", "", "core")))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond) // past the pause: the Enter waits
	core, p := fl.member("core")
	core.SetAttention(session.AttentionDone, "idle", session.SourceAPI)
	time.Sleep(300 * time.Millisecond)
	if n := logCount(t, e, run.ID, "core is done"); n != 0 {
		t.Fatalf("a done while the prompt's Enter waited started the members after core (%d)", n)
	}
	// The program reads again: the keys, then the Enter.
	var typedNow []string
	for len(typedNow) < 2 {
		select {
		case b := <-p.Input:
			if b != nil {
				typedNow = append(typedNow, string(b))
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("typed %q", typedNow)
		}
	}
	if typedNow[0] != "x" || typedNow[1] != "\r" {
		t.Fatalf("typed %q", typedNow)
	}
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	core.SetAttention(session.AttentionDone, "built", session.SourceAPI)
	waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)
}

// A prompt accepted while the member's program takes it slowly, whose
// Enter is then left out for a question come meanwhile, waits in the
// agent's input as one typed so at once does: the run says so, and the
// prompt counts as given, so the done after a person's Enter starts the
// members after it.
func TestAPendingPromptLeftWithoutItsEnterCountsAsGiven(t *testing.T) {
	e, fl := newEngine(t)
	fl.inputWait = 200 * time.Millisecond
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		if member != "core" {
			return
		}
		for len(p.Input) < cap(p.Input) {
			p.Input <- nil
		}
		l.SetAttention(session.AttentionNeedsInput, "what next?", session.SourceAPI)
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("core", "Build it."), after("tests", "", "core")))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for logCount(t, e, run.ID, "waits for core to take its input") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the prompt was never accepted as pending")
		}
		time.Sleep(20 * time.Millisecond)
	}
	core, p := fl.member("core")
	core.SetAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI)
	// The program reads again: the text, and no Enter.
	for {
		b := <-p.Input
		if b != nil {
			if string(b) != "Build it." {
				t.Fatalf("typed %q", b)
			}
			break
		}
	}
	deadline = time.Now().Add(10 * time.Second)
	for logCount(t, e, run.ID, "typed core's prompt without its Enter") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run never said the prompt waits without its Enter")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A person answers and presses Enter; the agent works and is done.
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	core.SetAttention(session.AttentionDone, "built", session.SourceAPI)
	waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)
}

// A trust question on a member's screen holds its prompt past the readiness
// cap: the member needs input with the question's words, the run says so
// once, and nothing is typed until a person answers it with Enter. Then the
// prompt is typed.
func TestATrustQuestionHoldsThePrompt(t *testing.T) {
	e, fl := newEngine(t)
	fl.trust = regexp.MustCompile(`Trust\s*this\s*folder\?`)
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, _ *session.Local) {
		_ = p.Print("\x1b[2;3HTrust\x1b[1Cthis\x1b[1Cfolder?\r\n› 1. Trust and continue")
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for logCount(t, e, run.ID, "asks") == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := e.Get(run.ID)
	if !logged(got, `lead asks "Trust this folder?"`) || got.State != RunNeedsInput || got.NeedsInput != 1 || !got.Members[0].NeedsInput || got.Members[0].Status != MemberStarting {
		t.Fatalf("run %+v", got)
	}
	l, p := fl.member("lead")
	time.Sleep(3 * time.Second) // past readyMin and readyQuiet: still held
	if w := typed(p); len(w) != 0 {
		t.Fatalf("typed into the trust question: %q", w)
	}
	sub, err := l.Attach("", session.RoleControl, "", 0, 0, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Input(sub, []byte("\x1b[B")); err != nil { // the selection moves: still asking
		t.Fatal(err)
	}
	if err := l.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	_ = p.Print("\x1b[2J> ")
	if got := waitTyped(t, p, 5*time.Second); got != "\x1b[B\r" {
		t.Fatalf("the person typed %q", got)
	}
	if got := waitTyped(t, p, 10*time.Second); got != "Plan it.\r" {
		t.Fatalf("typed %q", got)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	if n := logCount(t, e, run.ID, "asks"); n != 1 {
		t.Fatalf("the hold was noted %d times", n)
	}
}

// codexAnytime are Codex's questions asked during the work, as the built-in
// catalog entry has them.
func codexAnytime() []session.StartupQuestion {
	codex, _ := catalog.Default().Get("codex")
	var out []session.StartupQuestion
	for _, q := range codex.Questions {
		if q.Anytime {
			var answers []session.Option
			for _, a := range q.Answers {
				answers = append(answers, session.Option{Label: a.Label, Input: a.Input})
			}
			out = append(out, session.StartupQuestion{Pattern: regexp.MustCompile(q.Prompt), Words: regexp.MustCompile(q.Words), Anytime: true, Answers: answers})
		}
	}
	return out
}

// A dialog the readiness wait has not seen (the agent says it waits, then
// draws an approval, which the screen's quiet look has not raised yet)
// holds the prompt all the same: the session finds it as the prompt is
// submitted and refuses it, nothing typed, and the prompt waits for the
// answer, then is typed.
func TestAnApprovalFoundAsThePromptIsSubmittedHoldsIt(t *testing.T) {
	e, fl := newEngine(t)
	fl.questions = codexAnytime()
	fl.onLaunch = func(_ string, p *sessiontest.FakeProc, l *session.Local) {
		_ = p.Print("\x1b[12;3HWould you like to run the following command?\x1b[14;3H$ curl -sI https://example.com\x1b[16;1H› 1. Yes, proceed (y)\x1b[17;3H2. No, and tell Codex what to do differently (esc)\x1b[19;3HPress enter to confirm or esc to cancel")
		time.Sleep(50 * time.Millisecond) // the session has read it; not yet quiet
		l.SetAttention(session.AttentionNeedsInput, "ready?", session.SourceAPI)
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for logCount(t, e, run.ID, "asks") == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got, _ := e.Get(run.ID); !logged(got, `lead asks "Would you like to run the following command? $ curl -sI https://example.com"`) || got.Members[0].Status != MemberStarting {
		t.Fatalf("run %+v", got)
	}
	l, p := fl.member("lead")
	time.Sleep(1500 * time.Millisecond)
	if w := typed(p); len(w) != 0 {
		t.Fatalf("typed into the approval: %q", w)
	}
	sub, err := l.Attach("", session.RoleControl, "", 0, 0, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Input(sub, []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	_ = p.Print("\x1b[12;1H\x1b[J■ Conversation interrupted\r\n› ")
	select {
	case got := <-p.Input:
		if string(got) != "\x1b" {
			t.Fatalf("the person typed %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the person's Escape was not written")
	}
	if got := waitTyped(t, p, 10*time.Second); got != "Plan it.\r" {
		t.Fatalf("typed %q", got)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
}

// A run's state: running while a member is pending, starting or running and
// none waits; needs input with how many wait; finished once every member has
// ended; stopped after a stop. A member's done keeps a run running.
func TestRunState(t *testing.T) {
	now := time.Now()
	m := func(status string, needs bool) MemberState { return MemberState{Status: status, NeedsInput: needs} }
	for _, tc := range []struct {
		name    string
		run     Run
		state   string
		waiting int
	}{
		{"all pending", Run{Members: []MemberState{m(MemberPending, false)}}, RunRunning, 0},
		{"one running", Run{Members: []MemberState{m(MemberRunning, false), m(MemberPending, false)}}, RunRunning, 0},
		{"two waiting", Run{Members: []MemberState{m(MemberRunning, true), m(MemberStarting, true), m(MemberEnded, false)}}, RunNeedsInput, 2},
		{"ended and pending", Run{Members: []MemberState{m(MemberEnded, false), m(MemberPending, false)}}, RunRunning, 0},
		{"all ended", Run{Members: []MemberState{m(MemberEnded, false), m(MemberEnded, false)}}, RunFinished, 0},
		{"stopped", Run{StoppedAt: &now, Members: []MemberState{m(MemberEnded, false)}}, RunStopped, 0},
	} {
		if state, n := runState(tc.run); state != tc.state || n != tc.waiting {
			t.Errorf("%s: %s %d, want %s %d", tc.name, state, n, tc.state, tc.waiting)
		}
	}
}

// Get derives the state from the members' sessions: a running member whose
// session waits on a prompt makes the run need input, and a done does not
// end it.
func TestGetDerivesTheRunState(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	lead, p := fl.member("lead")
	lead.SetAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI)
	if got, _ := e.Get(run.ID); got.State != RunNeedsInput || got.NeedsInput != 1 {
		t.Fatalf("waiting: %s %d", got.State, got.NeedsInput)
	}
	lead.SetAttention(session.AttentionDone, "finished", session.SourceAPI)
	if got, _ := e.Get(run.ID); got.State != RunRunning || got.Members[0].NeedsInput {
		t.Fatalf("done: %+v", got)
	}
	p.End(0)
	<-lead.Ended()
	if got, _ := e.Get(run.ID); got.State != RunFinished {
		t.Fatalf("ended: %s", got.State)
	}
}

// OnRunChange hears of every change a read of the run shows that no session
// change carries: the launch, each start, the prompt, a stop.
func TestRunChangesAreReported(t *testing.T) {
	e, fl := newEngine(t)
	var mu sync.Mutex
	var heard []string
	e.OnRunChange = func(id string) {
		mu.Lock()
		heard = append(heard, id)
		mu.Unlock()
	}
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), manual("qa", "Check it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, id := range heard {
			if id != run.ID {
				t.Fatalf("heard of %q", id)
			}
			n++
		}
		return n
	}
	afterPrompt := count()
	if afterPrompt < 3 { // launched, lead reserved and started, lead's prompt
		t.Fatalf("heard %d changes by lead's prompt", afterPrompt)
	}
	if err := e.Stop(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if count() == afterPrompt {
		t.Fatal("the stop was not reported")
	}
}

// A handoff whose Enter a question raised in the pause held back is noted
// and never typed again.
func TestAHandoffTypedWithoutItsEnterIsNotTypedAgain(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	fl.pause = 300 * time.Millisecond
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
	lead, _ := fl.member("lead")
	core, p := fl.member("core")
	typed(p)
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	var once sync.Once
	go func() {
		// The text arrives: a question comes up before the Enter.
		<-p.Input
		once.Do(func() { core.SetAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI) })
	}()
	lead.Record(session.ActivityEntry{Type: session.ActivityHandoff, To: "core", Message: "the handlers are in"})
	deadline := time.Now().Add(5 * time.Second)
	for logCount(t, e, run.ID, "without its Enter") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if logCount(t, e, run.ID, "without its Enter") != 1 {
		got, _ := e.Get(run.ID)
		t.Fatalf("log %+v", got.Log)
	}
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI) // the question goes
	time.Sleep(200 * time.Millisecond)
	if w := typed(p); slices.ContainsFunc(w, func(s string) bool { return strings.Contains(s, "handlers") }) {
		t.Fatalf("typed again: %q", w)
	}
}

// ResumeMember starts an ended member again, as the member, in its directory:
// with an agent session id it resumes it and types no prompt; without one it
// starts anew and types the prompt again. A member that runs, or one of a
// stopped run, is refused.
func TestResumeMember(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), manual("qa", "Check it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", "x"); !errors.Is(err, ErrMemberRunning) {
		t.Fatalf("running: %v", err)
	}
	if _, err := e.ResumeMember(t.Context(), run.ID, "qa", ""); !errors.Is(err, ErrMemberRunning) {
		t.Fatalf("pending: %v", err)
	}
	lead, p := fl.member("lead")
	p.End(0)
	<-lead.Ended()
	id, err := e.ResumeMember(t.Context(), run.ID, "lead", "3f80c8bd-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	// Running once its session is ready for a prompt (#155), which the fake
	// session is at once.
	m := waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	fl.mu.Lock()
	call := fl.calls[len(fl.calls)-1]
	fl.mu.Unlock()
	if m.SessionID != id || call.resume != "3f80c8bd-0000-4000-8000-000000000001" || call.cwd != "/work" {
		t.Fatalf("resumed: %+v %+v", m, call)
	}
	_, p2 := fl.member("lead")
	time.Sleep(100 * time.Millisecond)
	if w := typed(p2); len(w) != 0 {
		t.Fatalf("a resumed member was typed %q", w)
	}
	got, _ := e.Get(run.ID)
	if !logged(got, "lead resumed its conversation") {
		t.Fatalf("log %+v", got.Log)
	}
	// Anew: the prompt again.
	l2, _ := fl.member("lead")
	_, p2 = fl.member("lead")
	p2.End(0)
	<-l2.Ended()
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", ""); err != nil {
		t.Fatal(err)
	}
	_, p3 := fl.member("lead")
	if got := waitTyped(t, p3, 5*time.Second); got != "Plan it.\r" {
		t.Fatalf("anew: typed %q", got)
	}
	if err := e.Stop(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	// A member of a stopped run resumed reopens the run (TestResumeMemberReopensACompletedStop).
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", ""); err != nil {
		t.Fatalf("stopped: %v", err)
	}
	if got, _ := e.Get(run.ID); got.StoppedAt != nil {
		t.Fatalf("not reopened: %+v", got)
	}
}

// A handoff accepted while the member's program takes it slowly (its text
// still being taken when its time ran out) is told in the run's log how it
// ended once it has: delivered with its Enter, or typed without it for a
// question come meanwhile.
func TestAPendingHandoffIsToldHowItEnded(t *testing.T) {
	for _, c := range []struct {
		name     string
		question bool
		want     string
	}{
		{"entered", false, "handoff delivered from lead to core"},
		{"without its Enter", true, "handoff from lead to core typed without its Enter"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, fl := newEngine(t)
			fl.onLaunch = askAtOnce
			fl.inputWait = 200 * time.Millisecond
			run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it.")))
			if err != nil {
				t.Fatal(err)
			}
			waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
			lead, _ := fl.member("lead")
			core, p := fl.member("core")
			typed(p)
			core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
			// The program takes nothing for now: the handoff's text waits.
			for len(p.Input) < cap(p.Input) {
				p.Input <- nil
			}
			lead.Record(session.ActivityEntry{Type: session.ActivityHandoff, To: "core", Message: "the handlers are in"})
			waitLogged(t, e, run.ID, "waits for core to take its input", 1, 10*time.Second)
			if c.question {
				core.SetAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI)
			}
			// The program reads again.
			var got string
			deadline := time.After(10 * time.Second)
			for !strings.Contains(got, "handlers") || !c.question && !strings.HasSuffix(got, "\r") {
				select {
				case b := <-p.Input:
					got += string(b)
				case <-deadline:
					t.Fatalf("typed %q", got)
				}
			}
			waitLogged(t, e, run.ID, c.want, 1, 10*time.Second)
			if c.question {
				time.Sleep(200 * time.Millisecond)
				if w := typed(p); len(w) != 0 {
					t.Fatalf("typed after the question: %q", w)
				}
				waitLogged(t, e, run.ID, "handoff delivered", 0, time.Second)
			}
		})
	}
}
