package crew

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A Chat run's member moves from its terminal to its chat at its prompt
// (design 14), over the fake launcher's members: each starts in a fake
// terminal drawn as Claude Code draws its screens, and its move starts a
// FakeProtocol. Every wait is on the run's log, the session's state, the
// fake's calls or the ledger.

// The screens the stub draws, as Claude Code draws them (design 14's
// by-hand section): its trust question, a numbered dialog; its question
// about a folder's tool servers after trust, rows with no numbers, a
// highlight's mark on one, a confirm footer and no rule pair under it, and
// its several-server form, boxes to tick; a screen while it loads.
const (
	claudeTrustScreen = "\x1b[2J\x1b[H Do you trust the files in this folder?\r\n\r\n Is this a project you created or one you trust?\r\n\r\n ❯ 1. No, exit\r\n   2. Yes, I trust this folder\r\n\r\n Enter to confirm · Esc to exit"
	toolsScreen       = "\x1b[2J\x1b[H New MCP server found in .mcp.json: tools\r\n\r\n ❯ Use this and all future MCP servers in this project\r\n   Use this MCP server\r\n   Continue without using this MCP server\r\n\r\n Enter to confirm · Esc to reject"
	toolsSeveral      = "\x1b[2J\x1b[H New MCP servers found in .mcp.json\r\n\r\n Select any you wish to enable.\r\n\r\n ❯ [ ] tools\r\n   [ ] search\r\n   [ ] files\r\n\r\n Space to select · Enter to confirm · Esc to reject"
	loadingScreen     = "\x1b[2J\x1b[H ▐▛███▜▌   Claude Code\r\n\r\n   Loading…"
	trustAnswerKeys   = "\x1b[B\r"
)

// claudeTrust is the catalog's trust question's pattern; trustChoices its
// answers as choices.
var (
	claudeTrust  = regexp.MustCompile(`Is\s*this\s*a\s*project\s*you\s*created\s*or\s*one\s*you\s*trust\?`)
	trustChoices = []session.Option{{Label: "Yes, I trust this folder", Input: trustAnswerKeys}, {Label: "No, exit", Input: "\r"}}
	uuidLike     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// screens is a chatScreen that draws each screen of the session named
// name's terminals in turn: first the one it starts with, then each that
// the test draws with next (the fake's draw of a terminal, and the
// program moving on).
type screens struct {
	mu     sync.Mutex
	first  map[string]string
	drawn  map[string]*sessiontest.FakeProc
	ready  chan string
	before func(name string) string
}

func newScreens() *screens {
	return &screens{first: map[string]string{}, drawn: map[string]*sessiontest.FakeProc{}, ready: make(chan string, 16)}
}

// draw is the fake launcher's chatScreen.
func (s *screens) draw(name string, p *sessiontest.FakeProc, _ *session.Local) {
	s.mu.Lock()
	screen, ok := s.first[name]
	if s.before != nil {
		screen, ok = s.before(name), true
	}
	s.drawn[name] = p
	s.mu.Unlock()
	if !ok {
		drawClaudePrompt(name, p, nil)
	} else {
		_ = p.Print(screen)
	}
	s.ready <- name
}

// chatEngine is managedEngine whose members draw sc's screens, Claude
// Code's trust question known with its choices.
func chatEngine(t *testing.T, sc *screens) (*Engine, *fakeLauncher, *fakeLedger) {
	t.Helper()
	e, fl, l := managedEngine(t)
	fl.trust, fl.trustAnswers, fl.chatScreen = claudeTrust, trustChoices, sc.draw
	return e, fl, l
}

// prompt draws Claude Code's prompt at rest on p.
func prompt(p *sessiontest.FakeProc) { drawClaudePrompt("", p, nil) }

// moves is how many moves the engine asked of the fake.
func (f *fakeLauncher) movesAsked() []moveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.moves)
}

// chatsStarted is how many chats the fake started.
func (f *fakeLauncher) chatsStarted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.allProtos)
}

// procOf is the latest process of the session named name.
func (f *fakeLauncher) procOf(name string) *sessiontest.FakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[name]
}

// sessionOf waits for the session named name and returns it.
func sessionOf(t *testing.T, fl *fakeLauncher, name string) *session.Local {
	t.Helper()
	var l *session.Local
	waitTrue(t, name+"'s session", func() bool {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		l = fl.locals[name]
		return l != nil
	})
	return l
}

// control attaches a controller to l.
func control(t *testing.T, l *session.Local) *session.Subscription {
	t.Helper()
	sub, err := l.Attach("", session.RoleControl, "", 80, 24, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// A member moves at its prompt and at nothing else: never while its trust
// question shows (answered by its choice on its session page, or by a
// person's keys in its terminal), nor at the screen that follows the
// answer, and once its screen is the prompt the move is made, its prompt
// in the chat.
func TestMemberMovesAtItsPrompt(t *testing.T) {
	for _, how := range []string{"its choice", "keys in the terminal"} {
		t.Run(how, func(t *testing.T) {
			dir := newRepo(t)
			sc := newScreens()
			name := attemptName("lead", "build", 1)
			sc.first[name] = claudeTrustScreen
			e, fl, _ := chatEngine(t, sc)
			c := chatCrew(dir, work("build", "lead"))
			run := launchManaged(t, e, c)
			local := sessionOf(t, fl, name)
			waitLogged(t, e, run.ID, "; "+heldMoveWords, 1, 10*time.Second)
			if n := len(fl.movesAsked()); n != 0 {
				t.Fatalf("%d moves asked while the trust question shows", n)
			}
			sub := control(t, local)
			att := local.Info().Attention
			var err error
			if how == "its choice" {
				err = local.AnswerInput(t.Context(), sub, att.ID, []byte(trustAnswerKeys))
			} else {
				err = local.Input(sub, []byte(trustAnswerKeys))
			}
			if err != nil {
				t.Fatal(err)
			}
			p := fl.procOf(name)
			if got := readWrite(t, p); got != trustAnswerKeys {
				t.Fatalf("the answer: %q", got)
			}
			_ = p.Print(loadingScreen)
			waitLogged(t, e, run.ID, "lead (task build, attempt 1): "+notPromptWords, 1, 10*time.Second)
			if n := len(fl.movesAsked()); n != 0 || fl.chatsStarted() != 0 {
				t.Fatalf("moved at a screen that is not the prompt: %d moves", n)
			}
			prompt(p)
			fp, _ := chatOf(t, fl, name)
			if got := sent(t, fp, 1); got[0].Text != session.ProtocolLine(taskLine(c.Tasks[0], 1)) {
				t.Fatalf("the chat was sent %q", got[0].Text)
			}
			if !p.Stopped() {
				t.Fatal("the terminal runs on beside the chat")
			}
			waitLogged(t, e, run.ID, "lead (task build, attempt 1): runs as a chat.", 1, 10*time.Second)
			if got := typed(p); len(got) != 0 {
				t.Fatalf("typed into the terminal: %q", got)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// Claude Code's question about a folder's tool servers, drawn after trust
// with no numbers on its rows, and its several-server form, holds the move
// however long it shows, nothing typed into it, the run's log saying so
// once; once answered and the prompt drawn, the member moves.
func TestMemberMoveNeedsThePromptShape(t *testing.T) {
	for form, screen := range map[string]string{"one server": toolsScreen, "several servers": toolsSeveral} {
		t.Run(form, func(t *testing.T) {
			dir := newRepo(t)
			sc := newScreens()
			name := attemptName("lead", "build", 1)
			sc.first[name] = screen
			e, fl, _ := chatEngine(t, sc)
			run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
			local := sessionOf(t, fl, name)
			said := "lead (task build, attempt 1): " + notPromptWords
			waitLogged(t, e, run.ID, said, 1, 10*time.Second)
			p := fl.procOf(name)
			// Drawn again, and the member's session moving on: still held,
			// said once.
			_ = p.Print(screen)
			local.SetAttention(session.AttentionWorking, "", session.SourceAPI)
			waitTrue(t, "the screen drawn again", func() bool { return strings.Contains(strings.Join(local.ScreenLines(24), "\n"), "Enter to confirm") })
			if local.AtPrompt() || len(fl.movesAsked()) != 0 || fl.chatsStarted() != 0 {
				t.Fatalf("moved at the question about the folder's tool servers: %d moves", len(fl.movesAsked()))
			}
			if got := typed(p); len(got) != 0 {
				t.Fatalf("typed into the question: %q", got)
			}
			if n := logCount(t, e, run.ID, said); n != 1 {
				t.Fatalf("the wait said %d times", n)
			}
			// A person answers it in the terminal; the prompt shows.
			if err := local.Input(control(t, local), []byte("\r")); err != nil {
				t.Fatal(err)
			}
			prompt(p)
			fp, _ := chatOf(t, fl, name)
			sent(t, fp, 1)
			stopRun(t, e, run.ID)
		})
	}
}

// A member's chat starts a new conversation: the fake's chat is started on
// a new id, never the terminal's, which the session then holds; the move
// was asked fresh.
func TestMemberChatStartsANewConversation(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := chatEngine(t, newScreens())
	name := attemptName("lead", "build", 1)
	run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
	local := sessionOf(t, fl, name)
	terminal := "launch-" + local.Info().ID
	fp, _ := chatOf(t, fl, name)
	sent(t, fp, 1)
	as := local.Info().AgentSession
	if as == nil || fp.StartedOn() != as.ID || as.ID == terminal || !uuidLike.MatchString(as.ID) {
		t.Fatalf("the chat started on %q, the session holds %+v (the terminal's %q)", fp.StartedOn(), as, terminal)
	}
	if moves := fl.movesAsked(); len(moves) != 1 || !moves[0].fresh {
		t.Fatalf("moves %+v", moves)
	}
	stopRun(t, e, run.ID)
}

// Nothing is typed into a moved member's terminal; its prompt is sent
// once, as the chat's first message.
func TestMemberPromptOnlyInTheChat(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := chatEngine(t, newScreens())
	name := attemptName("lead", "build", 1)
	c := chatCrew(dir, work("build", "lead"))
	run := launchManaged(t, e, c)
	fp, _ := chatOf(t, fl, name)
	msgs := sent(t, fp, 1)
	turn(t, fp, msgs[0])
	l.attemptRowIn(t, run.ID, "build", 1, ledger.AttemptRunning)
	fl.mu.Lock()
	first := fl.all[0]
	n := len(fl.all)
	fl.mu.Unlock()
	if got := typed(first); len(got) != 0 || n != 1 {
		t.Fatalf("the terminal was typed %q (%d processes)", got, n)
	}
	if got := fp.Sent(); len(got) != 1 || got[0].Text != session.ProtocolLine(taskLine(c.Tasks[0], 1)) {
		t.Fatalf("the chat was sent %+v", got)
	}
	// Its turn's end: the next message is the reminder, never the prompt
	// again (one queued for the turn's end would be written first).
	turnEnds(t, fp, "done")
	if got := sent(t, fp, 2); !strings.Contains(got[1].Text, "a done does not finish task") {
		t.Fatalf("after the turn the chat was sent %+v", got)
	}
	if got := typed(first); len(got) != 0 {
		t.Fatalf("the terminal was typed %q", got)
	}
	stopRun(t, e, run.ID)
}

// A member whose launch's arguments hold a flag a chat sets itself, or an
// added folder, is told so by the move's checks with its terminal still
// running: one terminal, never stopped, its prompt typed there, the run's
// log saying why.
func TestMemberChecksBeforeStopping(t *testing.T) {
	for why, words := range map[string]string{
		"a flag of the chat's": "its arguments hold --resume, which a chat sets itself.",
		"an added folder":      "it was launched with an added folder.",
	} {
		t.Run(why, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, _ := chatEngine(t, newScreens())
			name := attemptName("lead", "build", 1)
			fl.moveRefused[name] = &TerminalReason{Words: words}
			c := chatCrew(dir, work("build", "lead"))
			run := launchManaged(t, e, c)
			sessionOf(t, fl, name)
			p := fl.procOf(name)
			if got := waitTyped(t, p, 10*time.Second); got != taskLine(c.Tasks[0], 1)+"\r" {
				t.Fatalf("typed %q", got)
			}
			waitLogged(t, e, run.ID, "lead (task build, attempt 1): in its terminal: "+words, 1, 10*time.Second)
			moves := fl.movesAsked()
			if len(moves) != 1 || moves[0].stopped || p.Stopped() || fl.chatsStarted() != 0 || len(fl.all) != 1 {
				t.Fatalf("moves %+v, stopped %v, chats %d, processes %d", moves, p.Stopped(), fl.chatsStarted(), len(fl.all))
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A person's keys at the member's prompt before the move: the move is
// refused, the same terminal runs on, its prompt typed there once, the
// run's log saying why.
func TestMemberRefusedMoveStaysInItsTerminal(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := chatEngine(t, newScreens())
	name := attemptName("lead", "build", 1)
	asked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fl.beforeMove = func(string) {
		once.Do(func() {
			close(asked)
			<-release
		})
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	c := chatCrew(dir, work("build", "lead"))
	run := launchManaged(t, e, c)
	within(t, "the engine's look at the prompt", asked)
	local := sessionOf(t, fl, name)
	p := fl.procOf(name)
	if err := local.Input(control(t, local), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := readWrite(t, p); got != "x" {
		t.Fatalf("the person's key: %q", got)
	}
	close(release)
	waitLogged(t, e, run.ID, "lead (task build, attempt 1): in its terminal: "+typedFirstWords, 1, 10*time.Second)
	if got := waitTyped(t, p, 10*time.Second); got != taskLine(c.Tasks[0], 1)+"\r" {
		t.Fatalf("typed %q", got)
	}
	if p.Stopped() || fl.chatsStarted() != 0 || len(fl.all) != 1 {
		t.Fatalf("stopped %v, chats %d, processes %d", p.Stopped(), fl.chatsStarted(), len(fl.all))
	}
	stopRun(t, e, run.ID)
}

// readWrite reads the next write to p.
func readWrite(t *testing.T, p *sessiontest.FakeProc) string {
	t.Helper()
	select {
	case b := <-p.Input:
		return string(b)
	case <-time.After(10 * time.Second):
		t.Fatal("nothing written")
		return ""
	}
}

// A chat that never starts, and one never ready within its bound, give a
// terminal started afresh on a new conversation, its startup watch
// watching again (its trust question held), the prompt typed there; the
// chat's child ended; both failing, the attempt fails with both failures'
// words.
func TestMemberMoveFallsBackToATerminal(t *testing.T) {
	for _, why := range []string{"never starts", "never ready"} {
		t.Run(why, func(t *testing.T) {
			dir := newRepo(t)
			sc := newScreens()
			e, fl, _ := chatEngine(t, sc)
			name := attemptName("lead", "build", 1)
			if why == "never starts" {
				fl.chatErr = errors.New("the chat's child did not start")
			} else {
				fl.chatGate, fl.readyCap = make(chan struct{}), time.Millisecond
			}
			// The terminal started afresh asks its trust question again.
			first := true
			sc.before = func(string) string {
				if first {
					first = false
					return claudePromptFrame + claudeRule + "\r\n❯ \r\n" + claudeRule + "\r\n  ? for shortcuts"
				}
				return claudeTrustScreen
			}
			c := chatCrew(dir, work("build", "lead"))
			run := launchManaged(t, e, c)
			local := sessionOf(t, fl, name)
			launchConv := "launch-" + local.Info().ID
			waitLogged(t, e, run.ID, "lead (task build, attempt 1): in its terminal: "+notStartedWords, 1, 10*time.Second)
			fl.mu.Lock()
			convs := slices.Clone(fl.terminalConv)
			fl.mu.Unlock()
			chatConv := ""
			if fl.chatsStarted() > 0 {
				fp, _ := chatOf(t, fl, name)
				chatConv = fp.StartedOn()
				if !isClosed(fp.Child().Done()) || sessiontest.Count(fp.Calls(), "send") != 0 {
					t.Fatalf("the chat never ready: calls %v", fp.Calls())
				}
			}
			if len(convs) != 1 || convs[0] == launchConv || convs[0] == chatConv || !uuidLike.MatchString(convs[0]) {
				t.Fatalf("the terminal started afresh on %v (launch %q, chat %q)", convs, launchConv, chatConv)
			}
			waitTrue(t, "the trust question held again", func() bool {
				att := local.Info().Attention
				return att.State == session.AttentionNeedsInput && att.Source == session.SourceTrust
			})
			p := fl.procOf(name)
			if got := typed(p); len(got) != 0 {
				t.Fatalf("typed into the trust question: %q", got)
			}
			if err := local.AnswerInput(t.Context(), control(t, local), local.Info().Attention.ID, []byte(trustAnswerKeys)); err != nil {
				t.Fatal(err)
			}
			if got := readWrite(t, p); got != trustAnswerKeys {
				t.Fatalf("the answer: %q", got)
			}
			prompt(p)
			if got := waitTyped(t, p, 10*time.Second); got != taskLine(c.Tasks[0], 1)+"\r" {
				t.Fatalf("typed %q", got)
			}
			if local.Way() != session.WayTerminal {
				t.Fatalf("way %s", local.Way())
			}
			stopRun(t, e, run.ID)
		})
	}
	t.Run("neither starts", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := chatEngine(t, newScreens())
		fl.chatErr, fl.terminalErr = errors.New("the chat's child did not start"), errors.New("the terminal did not start")
		run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
		row := l.attemptRowIn(t, run.ID, "build", 1, ledger.AttemptFailed)
		if want := session.ChatNotStartedWords + " " + session.TermNotStartedWords; !strings.Contains(row.Error, want) {
			t.Fatalf("the attempt failed with %q, want both failures' words %q", row.Error, want)
		}
		stopRun(t, e, run.ID)
	})
}

// A member resumed on its conversation (by a person, and as a resumed
// run's member) is never given a new one: its chat resumes that
// conversation, the session's conversation unchanged, and it is sent no
// prompt.
func TestResumedMemberKeepsItsConversation(t *testing.T) {
	resumedChat := func(t *testing.T, e *Engine, fl *fakeLauncher, runID, name, conv string, before int) {
		t.Helper()
		var fp *sessiontest.FakeProtocol
		waitTrue(t, name+"'s chat on "+conv, func() bool {
			fl.mu.Lock()
			defer fl.mu.Unlock()
			fp = fl.protos[name]
			return fp != nil && fp.StartedOn() == conv
		})
		waitLogged(t, e, runID, name+": "+resumedChatWords, 1, 10*time.Second)
		waitStatus(t, e, runID, name, MemberRunning, 10*time.Second)
		local := sessionOf(t, fl, name)
		if as := local.Info().AgentSession; as == nil || as.ID != conv || local.Way() != session.WayProtocol {
			t.Fatalf("after the move: %+v, way %s", as, local.Way())
		}
		for _, m := range fl.movesAsked()[before:] {
			if m.name == name && m.fresh {
				t.Fatal("a resumed member moved fresh")
			}
		}
		if n := len(fp.Sent()); n != 0 {
			t.Fatalf("a resumed member was sent %d messages", n)
		}
	}
	t.Run("resumed by a person", func(t *testing.T) {
		e, fl := readyEngine(t)
		fl.trust, fl.trustAnswers = claudeTrust, trustChoices
		c := testCrew(immediate("lead", "Plan it."))
		c.Way = WayChat
		run, err := e.Launch(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		fp, _ := chatOf(t, fl, "lead")
		turn(t, fp, sent(t, fp, 1)[0])
		waitStatus(t, e, run.ID, "lead", MemberRunning, 10*time.Second)
		fp.Child().End(0)
		waitStatus(t, e, run.ID, "lead", MemberEnded, 10*time.Second)
		before := len(fl.movesAsked())
		if _, err := e.ResumeMember(t.Context(), run.ID, "lead", "conv-9"); err != nil {
			t.Fatal(err)
		}
		resumedChat(t, e, fl, run.ID, "lead", "conv-9", before)
		stopRun(t, e, run.ID)
	})
	t.Run("a resumed run's member", func(t *testing.T) {
		e, fl := readyEngine(t)
		fl.trust, fl.trustAnswers = claudeTrust, trustChoices
		c := testCrew(immediate("lead", "Plan it."))
		c.Way = WayChat
		run, err := e.Launch(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		fp, local := chatOf(t, fl, "lead")
		turn(t, fp, sent(t, fp, 1)[0])
		if !fp.Conversation("conv-7") {
			t.Fatal("the chat's output ended")
		}
		turnEnds(t, fp, "done")
		waitTrue(t, "the conversation kept resumable", func() bool {
			as := local.Info().AgentSession
			st := memberState(t, e, run.ID, "lead")
			return as != nil && as.ID == "conv-7" && as.Resumable && st.AgentSession != nil && st.AgentSession.ID == "conv-7"
		})
		stopRun(t, e, run.ID)
		before := len(fl.movesAsked())
		next, err := e.ResumeRun(t.Context(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		resumedChat(t, e, fl, next.ID, "lead", "conv-7", before)
		stopRun(t, e, next.ID)
	})
}

// A Chat run resumed from its record alone runs its members in their
// terminals, the record keeping no way, and its log says so; a Terminal
// run's resume says nothing of it.
func TestARecordsResumeSaysItsWayIsNotKept(t *testing.T) {
	for _, chat := range []bool{true, false} {
		e, fl := readyEngine(t)
		rec := Run{ID: "api-sweep-0000beef", CrewID: "api-sweep", Name: "API sweep", Goal: "ship /v1/users", Cwd: "/work", Isolation: IsolationNone, State: RunStopped,
			Members: []MemberState{{Name: "lead", AgentID: "claude", Prompt: "Plan it.", Start: Start{When: StartImmediately}}}}
		if chat {
			rec.Log = []session.ActivityEntry{{Type: session.ActivityStatus, Message: "lead: runs as a chat."}}
		}
		run, err := e.ResumeRecord(t.Context(), rec)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if chat {
			want = 1
		}
		waitLogged(t, e, run.ID, recordTerminalWords, want, 10*time.Second)
		fl.mu.Lock()
		way := fl.calls[0].way
		fl.mu.Unlock()
		if way != "" {
			t.Fatalf("a member resumed from a record launched with way %q", way)
		}
		stopRun(t, e, run.ID)
	}
}

// A screen other than the prompt for a moment (the agent drawing its
// prompt at its start) is not said: the run's log says the prompt waits
// only once the screen has held for notPromptLooks looks in a row, and
// once.
func TestAMomentsOtherScreenIsNotSaid(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "a moment", true: "a screen that holds"}[held], func(t *testing.T) {
			dir := newRepo(t)
			sc := newScreens()
			name := attemptName("lead", "build", 1)
			sc.first[name] = loadingScreen
			e, fl, _ := chatEngine(t, sc)
			var most atomic.Int32
			var early atomic.Bool
			var runID atomic.Value
			e.lookedOther = func(n int) {
				// The line of look n is written after this call: one already
				// in the log was written at an earlier look.
				if id, ok := runID.Load().(string); ok && n <= notPromptLooks && logCount(t, e, id, notPromptWords) != 0 {
					early.Store(true)
				}
				most.Store(int32(n))
			}
			run := launchManaged(t, e, chatCrew(dir, work("build", "lead")))
			runID.Store(run.ID)
			want := 0
			if held {
				want = 1
				waitLogged(t, e, run.ID, "lead (task build, attempt 1): "+notPromptWords, 1, 10*time.Second)
			} else {
				waitTrue(t, "a few looks at the other screen", func() bool { return most.Load() >= 3 })
			}
			prompt(fl.procOf(name))
			fp, _ := chatOf(t, fl, name)
			sent(t, fp, 1)
			if n := logCount(t, e, run.ID, notPromptWords); n != want && !(held == false && most.Load() >= notPromptLooks) {
				t.Fatalf("said %d times after %d looks, want %d", n, most.Load(), want)
			}
			if early.Load() {
				t.Fatalf("said before %d looks", notPromptLooks)
			}
			stopRun(t, e, run.ID)
		})
	}
}
