package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	extlocal "github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A Chat run's members (docs/plans/chat-or-terminal.md, design 8) over the
// fake launcher's protocol members: a member launched with
// LaunchSpec.Way "protocol" has a session in the protocol way from its
// start, over a FakeProtocol the test speaks for the agent. Every wait is
// on the fake's calls, the session's state or the ledger, never a clock.

// chatCrew is repoCrew run as a chat (Crew.Way).
func chatCrew(dir string, tasks ...Task) Crew {
	c := repoCrew(dir, tasks...)
	c.Way = WayChat
	return c
}

// attemptName is the name the fake launcher keeps an attempt's session by.
func attemptName(member, task string, n int) string {
	return fmt.Sprintf("%s · %s #%d", member, task, n)
}

// chatOf waits for the latest chat of the session named name and returns
// it with its session.
func chatOf(t *testing.T, fl *fakeLauncher, name string) (*sessiontest.FakeProtocol, *session.Local) {
	t.Helper()
	var fp *sessiontest.FakeProtocol
	waitTrue(t, name+"'s chat", func() bool {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		fp = fl.protos[name]
		return fp != nil
	})
	l, _ := fl.member(name)
	return fp, l
}

// sent waits until the agent was sent n messages and returns them.
func sent(t *testing.T, fp *sessiontest.FakeProtocol, n int) []session.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if !fp.Await(ctx, func(calls []string) bool { return sessiontest.Count(calls, "send") >= n }) {
		t.Fatalf("the agent was sent %d messages, want %d: %+v", len(fp.Sent()), n, fp.Sent())
	}
	return fp.Sent()
}

// turn has the agent take message m and work on it: its acknowledgement,
// then its turn's start.
func turn(t *testing.T, fp *sessiontest.FakeProtocol, m session.Message) {
	t.Helper()
	if !fp.Accepted(m.Ref) || !fp.TurnStarted("") {
		t.Fatal("the chat's output ended")
	}
}

// turnEnds ends the agent's turn with outcome.
func turnEnds(t *testing.T, fp *sessiontest.FakeProtocol, outcome string) {
	t.Helper()
	if !fp.TurnEnded(outcome, session.Usage{}) {
		t.Fatal("the chat's output ended")
	}
}

// within fails unless ch closes within 10 s.
func within(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("waited 10 s for %s", what)
	}
}

// runWay is the way the engine holds run runID to.
func runWay(e *Engine, runID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.runs[runID]; r != nil {
		return r.way
	}
	return "<no run>"
}

// A chat member's readiness is its session's (Local.Ready): its task line
// is sent only once its chat's start has answered. A chat not ready within
// protocolReadyCap is sent nothing: its child is ended, and a terminal
// started afresh on a new conversation takes the prompt (design 14's one
// fallback); the attempt fails, with both failures' words, only when that
// terminal does not start either.
func TestProtocolMemberReadyIsTheSessions(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, _ := managedEngine(t)
		begun, gate := make(chan struct{}), make(chan struct{})
		fl.chatGate = gate
		fl.chatBegin = func(string) { close(begun) }
		released := false
		t.Cleanup(func() {
			if !released {
				close(gate)
			}
		})
		c := chatCrew(dir, work("build", "lead"))
		run := launchManaged(t, e, c)
		within(t, "the chat's start", begun)
		fp, _ := chatOf(t, fl, attemptName("lead", "build", 1))
		if n := sessiontest.Count(fp.Calls(), "send"); n != 0 {
			t.Fatalf("sent %d messages before the chat was ready", n)
		}
		released = true
		close(gate)
		got := sent(t, fp, 1)
		calls := fp.Calls()
		if slices.Index(calls, "begin") > slices.Index(calls, "send") || got[0].Text != session.ProtocolLine(taskLine(c.Tasks[0], 1)) {
			t.Fatalf("calls %v, sent %q", calls, got[0].Text)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("past the cap", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, _ := managedEngine(t)
		fl.chatGate, fl.readyCap = make(chan struct{}), time.Millisecond
		name := attemptName("lead", "build", 1)
		c := chatCrew(dir, work("build", "lead"))
		run := launchManaged(t, e, c)
		waitLogged(t, e, run.ID, "lead (task build, attempt 1): in its terminal: "+notStartedWords, 1, 10*time.Second)
		fp, _ := chatOf(t, fl, name)
		if n := sessiontest.Count(fp.Calls(), "send"); n != 0 || !isClosed(fp.Child().Done()) {
			t.Fatalf("a chat past its cap: %d sent, its child ended %v", n, isClosed(fp.Child().Done()))
		}
		if got := waitTyped(t, fl.procOf(name), 10*time.Second); got != taskLine(c.Tasks[0], 1)+"\r" {
			t.Fatalf("the terminal started afresh was typed %q", got)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("past the cap, no terminal", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		fl.chatGate, fl.readyCap = make(chan struct{}), time.Millisecond
		fl.terminalErr = errors.New("the terminal did not start")
		run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
		row := l.attemptRowIn(t, run.ID, "build", 1, ledger.AttemptFailed)
		if want := session.ChatNotStartedWords + " " + session.TermNotStartedWords; !strings.Contains(row.Error, want) {
			t.Fatalf("the attempt failed with %q, want %q", row.Error, want)
		}
		fp, _ := chatOf(t, fl, attemptName("lead", "build", 1))
		if n := sessiontest.Count(fp.Calls(), "send"); n != 0 {
			t.Fatalf("sent %d", n)
		}
		stopRun(t, e, run.ID)
	})
}

// A chat member is prompted at its agent's acknowledgement, never at the
// write: until it, no attempt.prompted and no prompted mark. A prompt the
// agent never confirmed is never sent again, and the attempt waits until
// the agent works on its own.
func TestProtocolPromptedAtAcceptance(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
		fp, _ := chatOf(t, fl, attemptName("lead", "build", 1))
		msg := sent(t, fp, 1)[0]
		if st := memberState(t, e, run.ID, "lead"); st.Status == MemberRunning || l.count(run.ID, "attempt.prompted") != 0 {
			t.Fatalf("prompted at the write: %+v, events %v", st, l.kinds(run.ID))
		}
		e.mu.Lock()
		prompted := e.runs[run.ID].member("lead").prompted
		e.mu.Unlock()
		if prompted {
			t.Fatal("marked prompted at the write")
		}
		turn(t, fp, msg)
		l.attemptRowIn(t, run.ID, "build", 1, ledger.AttemptRunning)
		if l.count(run.ID, "attempt.prompted") != 1 || memberState(t, e, run.ID, "lead").Status != MemberRunning {
			t.Fatalf("after its acknowledgement: %v", l.kinds(run.ID))
		}
		waitLogged(t, e, run.ID, "sent lead's prompt", 1, 10*time.Second)
		if n := len(fp.Sent()); n != 1 {
			t.Fatalf("sent %d times", n)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("unconfirmed", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		fl.acceptWait = time.Millisecond
		run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
		fp, local := chatOf(t, fl, attemptName("lead", "build", 1))
		sent(t, fp, 1)
		waitLogged(t, e, run.ID, "sent lead's prompt, and its agent did not confirm taking it", 1, 10*time.Second)
		st := memberState(t, e, run.ID, "lead")
		if st.Status == MemberRunning || st.Unconfirmed != local.Info().ID || l.count(run.ID, "attempt.prompted") != 0 {
			t.Fatalf("an unconfirmed prompt: %+v, events %v", st, l.kinds(run.ID))
		}
		// The agent works on its own: the prompt was taken after all.
		if !fp.TurnStarted("") {
			t.Fatal("the chat's output ended")
		}
		l.attemptRowIn(t, run.ID, "build", 1, ledger.AttemptRunning)
		if st := memberState(t, e, run.ID, "lead"); st.Unconfirmed != "" || st.Status != MemberRunning {
			t.Fatalf("after its turn: %+v", st)
		}
		turnEnds(t, fp, "done")
		// The reminder goes; the prompt never again.
		got := sent(t, fp, 2)
		if len(got) != 2 || got[1].Text == got[0].Text {
			t.Fatalf("sent %+v", got)
		}
		stopRun(t, e, run.ID)
	})
}

// A message to a chat member and a decision's answer for it, both sent
// during its agent's turn, are sent once the turn has ended, the answer
// first, the message at the end of the turn after; nothing is written
// during a turn.
func TestHandoffWaitsForTheTurnsEnd(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, chatCrew(dir, work("build", "lead"), work("docs", "writer")))
	lead, leadLocal := chatOf(t, fl, attemptName("lead", "build", 1))
	writer, _ := chatOf(t, fl, attemptName("writer", "docs", 1))
	turn(t, lead, sent(t, lead, 1)[0])
	turn(t, writer, sent(t, writer, 1)[0])
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	w := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)

	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	mirrorOf(t, leadLocal, d.ID)
	if _, err := e.SendMessage(t.Context(), w.Attempt.SessionID, "lead", "the docs are in"); err != nil {
		t.Fatal(err)
	}
	waitLogged(t, e, run.ID, "handoff queued from writer to lead: lead "+chatBusyWords, 1, 10*time.Second)
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	waitLogged(t, e, run.ID, "decision "+d.ID+"'s answer sent to lead's chat", 1, 10*time.Second)
	if n := len(lead.Sent()); n != 1 {
		t.Fatalf("written during the turn: %+v", lead.Sent())
	}

	turnEnds(t, lead, "done")
	got := sent(t, lead, 2)
	if !strings.Contains(got[1].Text, "Approved") && !strings.Contains(got[1].Text, d.ID) {
		t.Fatalf("the answer first: %+v", got)
	}
	turn(t, lead, got[1])
	if n := len(lead.Sent()); n != 2 {
		t.Fatalf("the message went during the answer's turn: %+v", lead.Sent())
	}
	// The answer's turn ends: the message goes, at a turn's end, and so
	// may the reminder that a done is no result, each between turns.
	for n := 3; ; n++ {
		turnEnds(t, lead, "done")
		got = sent(t, lead, n)
		turn(t, lead, got[n-1])
		if strings.Contains(got[n-1].Text, "the docs are in") {
			break
		}
		if n > 3 || !strings.Contains(got[n-1].Text, "a done does not finish task") {
			t.Fatalf("sent %+v", got)
		}
		if len(lead.Sent()) != n {
			t.Fatalf("written during a turn: %+v", lead.Sent())
		}
	}
	waitLogged(t, e, run.ID, "delivered to lead", 1, 10*time.Second)
	stopRun(t, e, run.ID)
}

// A turn a person stopped (a chat's interrupt) is no turn done (I12): it
// starts no member of a legacy run that starts after it, and reminds no
// attempt of a managed run; the agent's next done does both.
func TestInterruptedTurnStartsNoOne(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		e, fl := readyEngine(t)
		c := testCrew(immediate("lead", "Plan it."), after("tests", "Test it.", "lead"))
		c.Way = WayChat
		run, err := e.Launch(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		fp, local := chatOf(t, fl, "lead")
		turn(t, fp, sent(t, fp, 1)[0])
		waitTrue(t, "lead running", func() bool { return memberState(t, e, run.ID, "lead").Status == MemberRunning })
		if err := local.Interrupt(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		turnEnds(t, fp, "interrupted")
		if n := logCount(t, e, run.ID, "lead is done: starting tests"); n != 0 || slices.Contains(fl.launched(), "tests") {
			t.Fatalf("an interrupted turn started tests: %v", fl.launched())
		}
		if !fp.TurnStarted("") {
			t.Fatal("the chat's output ended")
		}
		turnEnds(t, fp, "done")
		waitLogged(t, e, run.ID, "lead is done: starting tests", 1, 10*time.Second)
		stopRun(t, e, run.ID)
	})
	t.Run("managed", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
		fp, local := chatOf(t, fl, attemptName("lead", "build", 1))
		turn(t, fp, sent(t, fp, 1)[0])
		l.attemptRowIn(t, run.ID, "build", 1, ledger.AttemptRunning)
		if err := local.Interrupt(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		turnEnds(t, fp, "interrupted")
		e.mu.Lock()
		reminded := e.runs[run.ID].task("build").attempts[0].reminded
		e.mu.Unlock()
		if reminded || logCount(t, e, run.ID, "without a result") != 0 {
			t.Fatal("an interrupted turn was reminded")
		}
		if !fp.TurnStarted("") {
			t.Fatal("the chat's output ended")
		}
		turnEnds(t, fp, "done")
		got := sent(t, fp, 2)
		if !strings.Contains(got[1].Text, "a done does not finish task") {
			t.Fatalf("sent %+v", got)
		}
		turn(t, fp, got[1])
		waitLogged(t, e, run.ID, "lead reports done without a result; reminded once", 1, 10*time.Second)
		stopRun(t, e, run.ID)
	})
}

// A chat's turn's end, done or stopped, finishes no task (I12): the task
// runs on until its result.
func TestProtocolTurnFinishesNoTask(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, chatCrew(dir, work("build", "lead"), work("test", "lead", "build")))
	fp, local := chatOf(t, fl, attemptName("lead", "build", 1))
	turn(t, fp, sent(t, fp, 1)[0])
	b := l.attemptRowIn(t, run.ID, "build", 1, ledger.AttemptRunning)
	if err := local.Interrupt(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	turnEnds(t, fp, "interrupted")
	if !fp.TurnStarted("") {
		t.Fatal("the chat's output ended")
	}
	turnEnds(t, fp, "done")
	turn(t, fp, sent(t, fp, 2)[1]) // the reminder: the done was taken
	waitLogged(t, e, run.ID, "reminded once", 1, 10*time.Second)
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskRunning || ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("a turn's end finished build: %+v %+v", ts, ts.Attempt)
	}
	if ts := taskOf(t, e, run.ID, "test"); ts.State != ledger.TaskBlocked || ts.Attempts != 0 {
		t.Fatalf("a turn's end started test: %+v", ts)
	}
	if l.count(run.ID, "task.succeeded") != 0 || l.count(run.ID, "result.submitted") != 0 {
		t.Fatalf("events %v", l.kinds(run.ID))
	}
	tree := l.attemptRow(t, run.ID, "build", 1).Worktree
	writeFile(t, filepath.Join(tree, "built.txt"), "yes\n")
	commitAll(t, tree, "build it")
	submit(t, e, b.SessionID, Result{Summary: "built it"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
}

// The run's log says each attempt's way (I16): a chat's, and a member
// kept in its terminal with why, in the plan's words.
func TestRunLogSaysTheWay(t *testing.T) {
	const notQualified = "This version of Claude Code can only run in its terminal."
	for _, tc := range []struct {
		name, line, way string
		setup           func(e *Engine, fl *fakeLauncher)
	}{
		{"a chat", "lead (task build, attempt 1): runs as a chat.", WayProtocol, nil},
		{"an unqualified version", "lead (task build, attempt 1): in its terminal: this version of Claude Code can only run in its terminal.", "",
			func(_ *Engine, fl *fakeLauncher) { fl.unqualified["claude"] = errors.New(notQualified) }},
		{"a Runtime without pipes", "lead (task build, attempt 1): in its terminal: the run's Runtime has no pipes.", "",
			func(e *Engine, _ *fakeLauncher) {
				if err := e.SetRuntime(&sessiontest.FakeRuntime{Base: extlocal.Native(), TakesRun: func(ext.ProcessRef) bool { return true }}); err != nil {
					t.Fatal(err)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, _ := managedEngine(t)
			if tc.setup != nil {
				tc.setup(e, fl)
			}
			run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
			waitLogged(t, e, run.ID, tc.line, 1, 10*time.Second)
			fl.mu.Lock()
			way := fl.calls[0].way
			fl.mu.Unlock()
			if way != tc.way {
				t.Fatalf("launched with way %q, want %q", way, tc.way)
			}
			if tc.way == "" && logCount(t, e, run.ID, "runs as a chat") != 0 {
				t.Fatal("a member in its terminal said to run as a chat")
			}
			stopRun(t, e, run.ID)
		})
	}
	t.Run("a permission question", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, _ := managedEngine(t)
		run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
		fp, _ := chatOf(t, fl, attemptName("lead", "build", 1))
		turn(t, fp, sent(t, fp, 1)[0])
		input := []byte(`{"command":"ls"}`)
		if !fp.ToolStarted(session.ToolStep{Ref: "c-1", Name: "Bash", Input: input}) || !fp.Permission(session.PermissionRequest{Ref: "req-1", Call: "c-1", Tool: "Bash", Input: input}) {
			t.Fatal("the chat's output ended")
		}
		waitLogged(t, e, run.ID, "lead (task build, attempt 1): asks to run a command: answer it on its session page.", 1, 10*time.Second)
		stopRun(t, e, run.ID)
	})
	t.Run("a legacy run's member", func(t *testing.T) {
		e, fl := readyEngine(t)
		c := testCrew(immediate("lead", "Plan it."))
		c.Way = WayChat
		run, err := e.Launch(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		chatOf(t, fl, "lead")
		waitLogged(t, e, run.ID, "lead: runs as a chat.", 1, 10*time.Second)
		stopRun(t, e, run.ID)
	})
}

// A crew's way is fixed at its launch: the crew as launched, which the
// ledger keeps with the run, carries it, and the run that continues it
// after a restart launches its attempts with it; a crew with no way names
// none.
func TestCrewWayFixedAtLaunch(t *testing.T) {
	if strings.Contains(crewAsLaunched(repoCrew("/work", work("build", "lead"))), `"way"`) {
		t.Fatal("a crew with no way names one as launched")
	}
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
	fp, _ := chatOf(t, fl, attemptName("lead", "build", 1))
	turn(t, fp, sent(t, fp, 1)[0])
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	row, ok, err := ld.Run(t.Context(), run.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var launched Crew
	if err := json.Unmarshal([]byte(row.Crew), &launched); err != nil || launched.Way != WayChat {
		t.Fatalf("the crew as launched: %v %s", err, row.Crew)
	}
	if way := runWay(e, run.ID); way != WayChat {
		t.Fatalf("the run's way: %q", way)
	}
	e2, fl2, _ := restart(t, e, ld, ldir, run.ID)
	next := continueRun(t, e2, run.ID)
	if way := runWay(e2, next.ID); way != WayChat {
		t.Fatalf("the continuation's way: %q", way)
	}
	fp2, _ := chatOf(t, fl2, attemptName("lead", "build", 2))
	sent(t, fp2, 1)
	fl2.mu.Lock()
	way := fl2.calls[0].way
	fl2.mu.Unlock()
	if way != WayProtocol {
		t.Fatalf("the continuation's attempt launched with way %q", way)
	}
	stopRun(t, e2, next.ID)
}

// A planned launch of a Chat revision is read as every package is, its
// way beside the package (Planned.Way): the package's bytes and digest are
// a Terminal launch's, every existing package is read as before, and the
// run's attempts launch as chats.
func TestChatPackageIsReadByTheEngine(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	base := strings.TrimSpace(head(t, dir))
	c := repoCrew(dir, work("build", "lead"))
	runID := c.ID + "-0000000c"
	p := planned(t, runID, base)
	terminal := planned(t, runID, base)
	if string(p.Package) != string(terminal.Package) || p.Digest != terminal.Digest {
		t.Fatal("a Chat launch's package is not a Terminal one's")
	}
	p.Way = WayChat
	run := launchPlanned(t, e, c, p)
	if way := runWay(e, run.ID); way != WayChat {
		t.Fatalf("the run's way: %q", way)
	}
	fp, _ := chatOf(t, fl, attemptName("lead", "build", 1))
	sent(t, fp, 1)
	row, _, err := ld.Run(t.Context(), runID)
	if err != nil || row.Package != string(p.Package) {
		t.Fatalf("the row's package: %v", err)
	}
	if _, err := packageOfRow(row); err != nil {
		t.Fatalf("the row's package is not read: %v", err)
	}
	stopRun(t, e, run.ID)

	bad := planned(t, c.ID+"-0000000d", base)
	bad.Way = "terminal"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, release, err := e.LaunchPlanned(ctx, c, "x", bad); !errors.Is(err, ErrInvalid) {
		release()
		t.Fatalf("a way that is no way: %v", err)
	}
}

// A Chat crew launched from a ticket's plan, which carries no way yet,
// runs its members in their terminals, its log saying why once; a Terminal
// crew's planned run says nothing of it.
func TestAChatCrewsPlannedRunSaysItRunsInTerminals(t *testing.T) {
	for _, chat := range []bool{true, false} {
		dir := newRepo(t)
		e, fl, _ := decisionEngine(t)
		base := strings.TrimSpace(head(t, dir))
		c := repoCrew(dir, work("build", "lead"))
		if chat {
			c.Way = WayChat
		}
		run := launchPlanned(t, e, c, planned(t, c.ID+"-0000000e", base))
		if way := runWay(e, run.ID); way != "" {
			t.Fatalf("the run's way: %q", way)
		}
		waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		want := 0
		if chat {
			want = 1
		}
		if n := logCount(t, e, run.ID, plannedTerminalWords); n != want {
			t.Fatalf("chat %v: the log said it %d times", chat, n)
		}
		fl.mu.Lock()
		for _, call := range fl.calls {
			if call.way != "" {
				t.Errorf("%s was launched with way %q", call.name, call.way)
			}
		}
		fl.mu.Unlock()
		stopRun(t, e, run.ID)
	}
}
