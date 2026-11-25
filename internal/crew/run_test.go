package crew

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
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

// quietLog keeps the sessions' logs out of the test output.
var quietLog = slog.New(slog.DiscardHandler)

// launchCall is one call of fakeLauncher.Launch.
type launchCall struct {
	agentID, name, cwd string
	args               []string
	env                map[string]string
	ref                session.CrewRef
	yolo               bool
	resume             string
	// inject and allow are the spec's Inject and Allow: the attempt's
	// tree's names and the env names the server approved for it.
	inject map[string]string
	allow  []string
	// agentSHA256 is the spec's AgentSHA256: the approved selection of a
	// run launched from a ticket.
	agentSHA256 string
	// where is the spec's Where: the Runtime the session starts in.
	where string
	// account and model are the spec's: the member's binding and model.
	account session.BoundAccount
	model   string
	// way is the spec's Way.
	way string
}

// fakeLauncher starts member sessions over fake processes, where the API
// server starts them over PTYs, and hands their activity to the engine with
// the attention state, and their changes, as the server's hooks do.
type fakeLauncher struct {
	t      *testing.T
	engine *Engine
	// onLaunch, when set, runs on a goroutine of its own once a member's
	// session has started: what its process does.
	onLaunch func(member string, p *sessiontest.FakeProc, l *session.Local)
	// beforeActivity, when set, sees each entry a session hands on before the
	// engine does, on the goroutine that recorded it: it may hold the entry
	// back, as a goroutine the scheduler leaves waiting would.
	beforeActivity func(sessionID string, e session.ActivityEntry)
	// trust, when set, is every session's trust prompt (Options.TrustPattern),
	// and questions its other questions (Options.Questions).
	trust     *regexp.Regexp
	questions []session.StartupQuestion
	// shape, when set, is every session's prompt shape
	// (Options.PromptShape).
	shape *regexp.Regexp
	// confirm is every session's Options.ConfirmSubmit (its agent reports
	// taking a prompt), and confirmWait its Options.ConfirmWait.
	confirm     bool
	confirmWait time.Duration
	// workingTitle, when set, is every session's Options.WorkingTitle, and
	// titleSettle its Options.TitleSettle.
	workingTitle *regexp.Regexp
	titleSettle  time.Duration
	// pause is every session's pause before an Enter; 5 ms when zero.
	// pauses is a session's own, by its name, in its place.
	pause  time.Duration
	pauses map[string]time.Duration
	// inputWait is every session's Options.InputWait; the default when zero.
	inputWait time.Duration
	// A member launched with LaunchSpec.Way WayProtocol starts in its
	// terminal, as every session does, with Claude Code's prompt shape
	// unless shape is set, its screen drawn by chatScreen (Claude Code's
	// prompt when nil) in place of onLaunch; its move (MemberMove) starts
	// a FakeProtocol for its chat (protos, by session name, the latest;
	// allProtos every one, in order), whose Begin calls chatBegin first
	// when set and waits for chatGate when set, the chat's start bounded
	// by readyCap (the session's own when zero), failing with chatErr when
	// set; and a fake terminal for the way back (freshConvs and terminals
	// the conversations each was started on). acceptWait is every session's
	// Options.AcceptWait; the default when zero. unqualified answers
	// ChatCheck by agent id (nil: it can), moveRefused MemberMove by
	// session name; moves counts MemberMove's calls.
	protos       map[string]*sessiontest.FakeProtocol
	allProtos    []*sessiontest.FakeProtocol
	chatBegin    func(member string)
	chatGate     chan struct{}
	chatErr      error
	readyCap     time.Duration
	chatScreen   func(member string, p *sessiontest.FakeProc, l *session.Local)
	acceptWait   time.Duration
	unqualified  map[string]error
	moveRefused  map[string]error
	moves        []moveCall
	terminalConv []string
	trustAnswers []session.Option
	// beforeMove, when set, runs as MemberMove is asked, holding it: a
	// test's gate between the engine's look at the prompt and the move;
	// terminalErr fails every terminal a move starts.
	beforeMove  func(name string)
	terminalErr error
	// ended ends the engine's runs and its processes, once, at the first
	// of its processes' cleanups (endRuns).
	ended sync.Once

	mu           sync.Mutex
	calls        []launchCall
	reserves     int              // the Reserve calls that got a place
	reserveFails error            // when set, every Reserve returns it
	reserveHold  chan struct{}    // when set, Reserve waits for it to close, or for its context
	fail         map[string]error // member name -> the error its launch returns
	block        map[string]bool  // member name -> its launch waits for ctx to end
	procs        map[string]*sessiontest.FakeProc
	all          []*sessiontest.FakeProc   // every process launched, in order
	locals       map[string]*session.Local // by member name, the latest
	byID         map[string]*session.Local // by session ID
	// launchHold, when set, holds every launch once its session exists
	// (its process made, the session registered) until it closes, its
	// context not looked at, as Server.Launch looks at it only at its
	// start; launchHeld, when set, is given each session so held.
	launchHold chan struct{}
	launchHeld chan *session.Local
	// ports draws the engine's trees' ports (treeExec.port): numbers from
	// a range, never the network; probes answers its probes over HTTP
	// (treeExec.probe) from a map, never the network.
	ports  *fakePorts
	probes *fakeProbes
}

func newFakeLauncher(t *testing.T) *fakeLauncher {
	return &fakeLauncher{t: t, fail: map[string]error{}, procs: map[string]*sessiontest.FakeProc{},
		locals: map[string]*session.Local{}, byID: map[string]*session.Local{}, ports: newFakePorts(), probes: newFakeProbes(),
		protos: map[string]*sessiontest.FakeProtocol{}, unqualified: map[string]error{}, moveRefused: map[string]error{}}
}

// newEngine returns an engine over a new fakeLauncher, its trees' ports
// drawn by the launcher's fakePorts and their probes over HTTP answered by
// its fakeProbes: no engine test reaches the network but through the
// executor.
func newEngine(t *testing.T) (*Engine, *fakeLauncher) {
	fl := newFakeLauncher(t)
	e := NewEngine(fl, fl.lookup)
	e.exec.port, e.exec.probe = fl.ports.port, fl.probes.probe
	fl.engine = e
	// The engine's runs end at the test's end even when no process was
	// launched: a run still making its first attempt's worktree would
	// otherwise write into a folder the test's cleanup is removing.
	t.Cleanup(fl.endRuns)
	return e, fl
}

// Reserve always has a place, unless reserveFails is set; with
// reserveHold set it waits for the channel to close, or for the context.
func (f *fakeLauncher) Reserve(ctx context.Context) (*session.Reservation, error) {
	f.mu.Lock()
	fail, hold := f.reserveFails, f.reserveHold
	f.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.reserves++
	f.mu.Unlock()
	return &session.Reservation{}, nil
}

func (f *fakeLauncher) Launch(ctx context.Context, spec LaunchSpec) (*session.Local, error) {
	name, ref := spec.Name, spec.Ref
	f.mu.Lock()
	f.calls = append(f.calls, launchCall{spec.AgentID, name, spec.Cwd, slices.Clone(spec.Args), maps.Clone(spec.Env), ref, spec.Yolo, spec.Resume, maps.Clone(spec.Inject), slices.Clone(spec.Allow), spec.AgentSHA256, string(spec.Where), spec.Account, spec.Model, spec.Way})
	err, block := f.fail[name], f.block[name]
	pause := cmp.Or(f.pauses[name], f.pause, 5*time.Millisecond)
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p := sessiontest.NewFakeProc()
	info := session.Info{ID: session.NewID(), Name: name, AgentID: spec.AgentID, Cwd: spec.Cwd, Crew: &ref, Cols: 80, Rows: 24}
	opts := session.Options{Log: quietLog, OnActivity: f.activity, OnChange: f.change,
		SubmitPause: pause, TrustPattern: f.trust, TrustAnswers: f.trustAnswers, Questions: f.questions, InputWait: f.inputWait, PromptShape: f.shape,
		WorkingTitle: f.workingTitle, TitleSettle: f.titleSettle, ConfirmSubmit: f.confirm, ConfirmWait: f.confirmWait, AcceptWait: f.acceptWait}
	onLaunch := f.onLaunch
	if spec.Way == WayProtocol {
		// A member launched to run as a chat: a server session of Claude
		// Code's, on the conversation its launch chose or resumed.
		info.Kind = session.KindServer
		info.AgentSession = &session.AgentSession{ID: "launch-" + info.ID, Source: session.AgentSessionSet}
		if spec.Resume != "" {
			info.AgentSession = &session.AgentSession{ID: spec.Resume, Resumable: true, Source: session.AgentSessionResumed}
		}
		opts.MemberWay, opts.MemberResumes = spec.Way, spec.Resume != ""
		if opts.PromptShape == nil {
			opts.PromptShape = claudeShape(f.t)
		}
		onLaunch = f.chatScreen
		if onLaunch == nil {
			onLaunch = drawClaudePrompt
		}
	}
	l := session.NewLocal(info, p, opts)
	f.mu.Lock()
	f.procs[name], f.locals[name], f.byID[info.ID] = p, l, l
	f.all = append(f.all, p)
	f.mu.Unlock()
	f.t.Cleanup(func() {
		f.endRuns()
		p.End(0)
	})
	if onLaunch != nil {
		go onLaunch(name, p, l)
	}
	f.mu.Lock()
	hold, held := f.launchHold, f.launchHeld
	f.mu.Unlock()
	if hold != nil {
		if held != nil {
			held <- l
		}
		<-hold
	}
	return l, nil
}

// moveCall is a MemberMove the engine asked: the session's name, whether
// fresh, and whether its terminal had been stopped by then.
type moveCall struct {
	name           string
	fresh, stopped bool
}

// ChatCheck answers unqualified's error for the agent (crew.ChatChecker).
func (f *fakeLauncher) ChatCheck(_ context.Context, agentID string, _ []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unqualified[agentID]
}

// MemberMove is a member's move's starts over fakes (crew.ChatChecker):
// its chat a new FakeProtocol, its way back a fake terminal drawn as its
// launch's was; refused with moveRefused's error for its session's name.
func (f *fakeLauncher) MemberMove(_ context.Context, local *session.Local, fresh bool) (session.MoveStarts, error) {
	name := local.Info().Name
	f.mu.Lock()
	hook := f.beforeMove
	f.mu.Unlock()
	if hook != nil {
		hook(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.procs[name]
	f.moves = append(f.moves, moveCall{name: name, fresh: fresh, stopped: p != nil && p.Stopped()})
	if err := f.moveRefused[name]; err != nil {
		return session.MoveStarts{}, err
	}
	return session.MoveStarts{
		Protocol: func(ctx context.Context, conv string) (session.Child, session.Protocol, error) {
			f.mu.Lock()
			gate, begin, fail := f.chatGate, f.chatBegin, f.chatErr
			f.mu.Unlock()
			if fail != nil {
				return nil, nil, fail
			}
			fp := sessiontest.NewFakeProtocol()
			f.mu.Lock()
			f.protos[name] = fp
			f.allProtos = append(f.allProtos, fp)
			f.mu.Unlock()
			if gate != nil {
				fp.BeginGate = gate
			}
			if begin != nil {
				fp.BeforeBegin = func() { begin(name) }
			}
			f.t.Cleanup(func() { fp.Child().End(0) })
			return fp.Start()(ctx, conv)
		},
		Terminal: func(_ context.Context, conv string, _, _ uint16) (session.Process, error) {
			f.mu.Lock()
			fail := f.terminalErr
			f.mu.Unlock()
			if fail != nil {
				return nil, fail
			}
			np := sessiontest.NewFakeProc()
			f.mu.Lock()
			f.procs[name] = np
			f.all = append(f.all, np)
			f.terminalConv = append(f.terminalConv, conv)
			draw := f.chatScreen
			f.mu.Unlock()
			f.t.Cleanup(func() {
				f.endRuns()
				np.End(0)
			})
			if draw == nil {
				draw = drawClaudePrompt
			}
			go draw(name, np, local)
			return np, nil
		},
		Gone:     session.GroupGone,
		ReadyCap: f.readyCap,
	}, nil
}

// claudeShape is Claude Code's prompt shape, as the catalog gives it.
func claudeShape(t *testing.T) *regexp.Regexp {
	t.Helper()
	a, ok := catalog.Default().Get("claude")
	if !ok {
		t.Fatal("no built-in Claude Code")
	}
	re, err := catalog.CompilePattern(a.PromptShape)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

// claudeRule is a rule of Claude Code's prompt box, 80 columns wide.
var claudeRule = strings.Repeat("─", 80)

// claudePromptFrame is Claude Code's prompt at rest as the stub draws it:
// a rule, the ❯ row, a rule, its footer.
const claudePromptFrame = "\x1b[2J\x1b[H ▐▛███▜▌   Claude Code\r\n\r\n"

// drawClaudePrompt draws Claude Code's prompt at rest on p.
func drawClaudePrompt(_ string, p *sessiontest.FakeProc, _ *session.Local) {
	_ = p.Print(claudePromptFrame + claudeRule + "\r\n❯ \r\n" + claudeRule + "\r\n  ? for shortcuts")
}

// activity is the sessions' OnActivity: the entry goes to the engine with the
// state and kind it records, as the server's hook hands it on.
func (f *fakeLauncher) activity(sessionID string, e session.ActivityEntry, state session.AttentionState, kind string) {
	if f.beforeActivity != nil {
		f.beforeActivity(sessionID, e)
	}
	f.engine.OnActivity(sessionID, e, state, kind)
}

// change is the sessions' OnChange: the change goes to the engine, as the
// server's hook sends it.
func (f *fakeLauncher) change(info session.Info) { f.engine.OnChange(info) }

func (f *fakeLauncher) lookup(id string) (*session.Local, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.byID[id]
	return l, ok
}

// member returns the latest session and process launched for a member.
func (f *fakeLauncher) member(name string) (*session.Local, *sessiontest.FakeProc) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	l, p := f.locals[name], f.procs[name]
	if l == nil {
		f.t.Fatalf("member %q has no session", name)
	}
	return l, p
}

// launched returns the names of the members launched, in order.
func (f *fakeLauncher) launched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for _, c := range f.calls {
		names = append(names, c.name)
	}
	return names
}

// wheres is the Where of every launch so far, in order.
func (f *fakeLauncher) wheres() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.where)
	}
	return out
}

// printOnce makes every process print a prompt and go quiet.
func printOnce(member string, p *sessiontest.FakeProc, _ *session.Local) {
	_ = p.Print(member + "> ")
}

// readyEngine is an engine over a new fakeLauncher whose sessions print a
// prompt once and are ready for their line soon after: the product's
// readiness bounds (seconds an attempt) are not what its tests test.
func readyEngine(t *testing.T) (*Engine, *fakeLauncher) {
	e, fl := newEngine(t)
	fl.onLaunch = printOnce
	e.await = readyWait{poll: 5 * time.Millisecond, quiet: 20 * time.Millisecond}.await
	// The store a planned run's attachments are copied from, as the API
	// gives the engine Dispatch's (package_test.go).
	e.UsePackageFiles(testFiles.open)
	return e, fl
}

// askAtOnce makes every agent report that it waits for input as soon as it
// starts: ready without the quiet period.
func askAtOnce(_ string, _ *sessiontest.FakeProc, l *session.Local) {
	l.SetAttention(session.AttentionNeedsInput, "what next?", session.SourceAPI)
}

func testCrew(members ...Member) Crew {
	return Crew{ID: "api-sweep", Name: "API sweep", Goal: "ship /v1/users", Cwd: "/work", Where: WhereServer, Isolation: IsolationNone, Members: members}
}

func immediate(name, prompt string) Member {
	return Member{Name: name, AgentID: "claude", Prompt: prompt, Start: Start{When: StartImmediately}}
}

func after(name, prompt, member string) Member {
	return Member{Name: name, AgentID: "shell", Prompt: prompt, Start: Start{When: StartAfter, Member: member}}
}

func manual(name, prompt string) Member {
	return Member{Name: name, AgentID: "shell", Prompt: prompt, Start: Start{When: StartManual}}
}

// typed returns what a process has been submitted so far, without waiting:
// the writes joined, cut after each carriage return, so that a text and the
// Enter written after it read as one "text\r"; a text still waiting for its
// Enter comes last, without one.
func typed(p *sessiontest.FakeProc) []string {
	var all string
	for {
		select {
		case b := <-p.Input:
			all += string(b)
			continue
		default:
		}
		break
	}
	var out []string
	for all != "" {
		i := strings.IndexByte(all, '\r')
		if i < 0 {
			return append(out, all)
		}
		out, all = append(out, all[:i+1]), all[i+1:]
	}
	return out
}

// waitTyped waits up to d for the next submission to a process: the writes up
// to and including the next carriage return, joined.
func waitTyped(t *testing.T, p *sessiontest.FakeProc, d time.Duration) string {
	t.Helper()
	got, ok := readSubmission(p, d)
	if !ok {
		t.Fatalf("no whole submission within %v (got %q)", d, got)
	}
	return got
}

// readSubmission reads writes to p until one ends with a carriage return,
// for up to d, and returns them joined; ok is false when d ran out.
func readSubmission(p *sessiontest.FakeProc, d time.Duration) (string, bool) {
	deadline := time.After(d)
	var got string
	for {
		select {
		case b := <-p.Input:
			got += string(b)
			if strings.HasSuffix(got, "\r") {
				return got, true
			}
		case <-deadline:
			return got, false
		}
	}
}

// crewEntries returns the input entries a session records as typed by the crew.
func crewEntries(l *session.Local) []session.ActivityEntry {
	var out []session.ActivityEntry
	for _, e := range l.Activity() {
		if e.Type == session.ActivityInput && e.ByName == "crew" {
			out = append(out, e)
		}
	}
	return out
}

// memberState returns a member of a run as Get reports it.
func memberState(t *testing.T, e *Engine, runID, name string) MemberState {
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
	t.Fatalf("run %s has no member %q", runID, name)
	return MemberState{}
}

// waitStatus waits until a member of a run has the status.
func waitStatus(t *testing.T, e *Engine, runID, name, status string, d time.Duration) MemberState {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		m := memberState(t, e, runID, name)
		if m.Status == status {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("member %q is %q after %v, want %q (%+v)", name, m.Status, d, status, m)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// logged reports whether a run's log has an entry whose message holds text.
func logged(r Run, text string) bool {
	return slices.ContainsFunc(r.Log, func(e session.ActivityEntry) bool { return strings.Contains(e.Message, text) })
}

// Two members start at once. Launch returns once their sessions exist; each
// process prints and goes quiet, and once it has been quiet for a second, and
// two have passed, the member's prompt is typed with the goal in it and an
// Enter, recorded as typed by the crew, and the member is running.
func TestLaunchTypesPromptsWhenReady(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = printOnce
	lead := immediate("lead", "Own the plan for $GOAL.")
	lead.Args = []string{"--model", "opus"}
	core := immediate("core", "Build ${GOAL} in Go.")
	core.AgentID = "codex"
	began := time.Now()
	run, err := e.Launch(t.Context(), testCrew(lead, core))
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took >= readyMin {
		t.Fatalf("Launch waited %v, for the prompts", took)
	}
	for _, m := range run.Members {
		if m.Status != MemberStarting || m.SessionID == "" {
			t.Fatalf("at launch: %+v", m)
		}
	}
	want := map[string]string{"lead": "Own the plan for ship /v1/users.\r", "core": "Build ship /v1/users in Go.\r"}
	for name, prompt := range want {
		l, p := fl.member(name)
		if got := waitTyped(t, p, 10*time.Second); got != prompt {
			t.Errorf("%s was typed %q, want %q", name, got, prompt)
		}
		if took := time.Since(began); took < readyMin {
			t.Errorf("%s was typed after %v, before the %v a quiet member is given", name, took, readyMin)
		}
		waitStatus(t, e, run.ID, name, MemberRunning, 5*time.Second)
		entries := crewEntries(l)
		if len(entries) != 1 || entries[0].Message != strings.TrimSuffix(prompt, "\r") {
			t.Errorf("%s records %+v", name, entries)
		}
	}

	if !strings.HasPrefix(run.ID, "api-sweep-") || run.CrewID != "api-sweep" || run.Name != "API sweep" || run.Goal != "ship /v1/users" ||
		run.Cwd != "/work" || run.Isolation != IsolationNone || run.StartedAt.IsZero() || run.StoppedAt != nil || len(run.Members) != 2 {
		t.Fatalf("run %+v", run)
	}
	for _, m := range memberStates(t, e, run.ID) {
		l, _ := fl.member(m.Name)
		if m.Status != MemberRunning || m.SessionID != l.Info().ID || m.Started == nil || m.Ended != nil || m.Err != "" || m.Branch != "" || m.Worktree != "" {
			t.Errorf("member %+v", m)
		}
		if runID, name, ok := e.MemberOf(m.SessionID); !ok || runID != run.ID || name != m.Name {
			t.Errorf("MemberOf(%s) = %q %q %v", m.SessionID, runID, name, ok)
		}
	}
	if _, _, ok := e.MemberOf("nope"); ok {
		t.Error("MemberOf found a session outside the run")
	}
	for _, c := range fl.calls {
		wantArgs := map[string][]string{"lead": {"--model", "opus"}, "core": nil}[c.name]
		if c.cwd != "/work" || !slices.Equal(c.args, wantArgs) || !maps.Equal(c.env, map[string]string{"GOAL": "ship /v1/users"}) ||
			c.ref != (session.CrewRef{RunID: run.ID, CrewID: "api-sweep", Member: c.name}) || c.agentID != map[string]string{"lead": "claude", "core": "codex"}[c.name] {
			t.Errorf("launch %+v", c)
		}
	}
	got, ok := e.Get(run.ID)
	if !ok || got.ID != run.ID || !logged(got, "launched") || !logged(got, "lead started") || !logged(got, "typed lead's prompt") || !logged(got, "typed core's prompt") {
		t.Fatalf("Get: %v %+v", ok, got.Log)
	}
	if list := e.List(); len(list) != 1 || list[0].ID != run.ID {
		t.Fatalf("List: %+v", list)
	}
}

// A role prompt of several lines is typed as one line, the goal in it
// included, as a handoff and a broadcast are: each line break and tab a
// space, one carriage return at the end. The crew's prompt stays as written.
func TestAMultiLinePromptIsTypedAsOneLine(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	c := testCrew(immediate("lead", "a\nb\tc"), immediate("core", "Build $GOAL.\nTests first."))
	c.Goal = "ship\n/v1/users"
	run, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"lead": "a b c\r", "core": "Build ship /v1/users. Tests first.\r"} {
		l, p := fl.member(name)
		if got := waitTyped(t, p, 5*time.Second); got != want {
			t.Errorf("%s was typed %q, want %q", name, got, want)
		}
		waitStatus(t, e, run.ID, name, MemberRunning, 5*time.Second)
		if entries := crewEntries(l); len(entries) != 1 || entries[0].Message != strings.TrimSuffix(want, "\r") {
			t.Errorf("%s records %+v", name, entries)
		}
	}
	if c.Members[0].Prompt != "a\nb\tc" || c.Goal != "ship\n/v1/users" {
		t.Fatalf("the crew changed: %+v", c)
	}
}

// A member that starts after another starts when that one first reports
// done once its prompt is typed, once: a done before its prompt, or a second
// one, starts nothing.
func TestAfterConditionStartsOnFirstDone(t *testing.T) {
	e, fl := newEngine(t)
	idle := make(chan struct{})
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		if member == "core" {
			// Idle at start: done, before any prompt. SetAttention returns
			// once the engine has taken the entry (OnActivity).
			l.SetAttention(session.AttentionDone, "idle", session.SourceAPI)
			close(idle)
			return
		}
		printOnce(member, p, l)
	}
	// core's prompt waits until the engine has taken that done, so that the
	// done comes before the prompt as the engine sees them.
	e.await = func(ctx context.Context, l *session.Local, held func(string)) error {
		if l.Info().Name == "core" {
			select {
			case <-idle:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return AwaitReady(ctx, l, held)
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("core", "Build it."), after("tests", "Test $GOAL.", "core")))
	if err != nil {
		t.Fatal(err)
	}
	if names := fl.launched(); !slices.Equal(names, []string{"core"}) {
		t.Fatalf("launched %v", names)
	}
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
	if m := memberState(t, e, run.ID, "tests"); m.Status != MemberPending || m.SessionID != "" || m.Started != nil {
		t.Fatalf("tests before core is done: %+v", m)
	}
	core, _ := fl.member("core")
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	core.SetAttention(session.AttentionDone, "turn finished", session.SourceAPI)
	m := waitStatus(t, e, run.ID, "tests", MemberRunning, 10*time.Second)
	_, p := fl.member("tests")
	if got := typed(p); !slices.Equal(got, []string{"Test ship /v1/users.\r"}) {
		t.Fatalf("tests was typed %q", got)
	}
	if m.SessionID == "" || m.Started == nil {
		t.Fatalf("tests %+v", m)
	}

	// A second done starts nothing more. SetAttention hands its entry to the
	// engine before it returns (Record calls OnActivity on the recording
	// goroutine), and the engine reserves there the starts a done makes: once
	// it has returned with the entry recorded, a start the done made would
	// show as a member starting and as a second "core is done" in the run log.
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	core.SetAttention(session.AttentionDone, "finished again", session.SourceAPI)
	if n := doneEntries(core, "finished again"); n != 1 {
		t.Fatalf("core records the second done %d times", n)
	}
	if n := logCount(t, e, run.ID, "core is done"); n != 1 {
		t.Fatalf("%d starts after core's dones, want 1", n)
	}
	for _, m := range memberStates(t, e, run.ID) {
		if m.Status == MemberStarting || m.Status == MemberPending {
			t.Fatalf("after the second done: %+v", m)
		}
	}
	if names := fl.launched(); !slices.Equal(names, []string{"core", "tests"}) {
		t.Fatalf("launched %v", names)
	}
}

// doneEntries counts the done attention entries with message msg a session
// records.
func doneEntries(l *session.Local, msg string) int {
	n := 0
	for _, e := range l.Activity() {
		if e.Type == session.ActivityAttention && strings.Contains(e.Message, msg) {
			n++
		}
	}
	return n
}

// A done that predates a member's prompt never starts the members after it,
// however late its entry reaches the engine: core is idle (done) as it
// starts, the engine sees that state and types core's prompt, and only then
// is the entry of that done handed on. A done core reports once it has had
// its prompt starts tests, once.
func TestAfterConditionIgnoresADoneThatPredatesThePrompt(t *testing.T) {
	e, fl := newEngine(t)
	delivered := make(chan struct{})
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		if member == "core" {
			// SetAttention returns once the engine has taken the entry.
			l.SetAttention(session.AttentionDone, "idle", session.SourceAPI)
			close(delivered)
		}
	}
	prompt := make(chan string, 1)
	fl.beforeActivity = func(_ string, entry session.ActivityEntry) {
		if entry.Type != session.ActivityAttention || entry.Message != "idle" {
			return
		}
		// Held back until core's prompt has been typed.
		_, p := fl.member("core")
		got, _ := readSubmission(p, 10*time.Second)
		prompt <- got
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("core", "Build it."), after("tests", "", "core")))
	if err != nil {
		t.Fatal(err)
	}
	if got := <-prompt; got != "Build it.\r" {
		t.Fatalf("core was typed %q before its idle done was handed on", got)
	}
	<-delivered
	if n := logCount(t, e, run.ID, "core is done"); n != 0 {
		t.Fatalf("the idle done from before the prompt started the members after core (%d)", n)
	}
	if m := memberState(t, e, run.ID, "tests"); m.Status != MemberPending || m.SessionID != "" {
		t.Fatalf("tests after the idle done: %+v", m)
	}
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)

	// A done after the prompt starts tests, once.
	core, _ := fl.member("core")
	core.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	core.SetAttention(session.AttentionDone, "turn finished", session.SourceAPI)
	waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)
	if n := logCount(t, e, run.ID, "core is done"); n != 1 {
		t.Fatalf("%d starts after core's done, want 1", n)
	}
	if names := fl.launched(); !slices.Equal(names, []string{"core", "tests"}) {
		t.Fatalf("launched %v", names)
	}
}

// A member whose process ends before it is ready gets no prompt: it shows
// ended, with how its process ended, and the rest of the run carries on.
func TestEarlyExitDropsPrompt(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		_ = p.Print("starting...\r\n")
		if member == "lead" {
			p.End(7)
		}
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it.")))
	if err != nil {
		t.Fatalf("an early exit failed the launch: %v", err)
	}
	waitStatus(t, e, run.ID, "core", MemberRunning, 10*time.Second)
	_, lead := fl.member("lead")
	if got := typed(lead); len(got) != 0 {
		t.Fatalf("lead was typed %q", got)
	}
	m := waitStatus(t, e, run.ID, "lead", MemberEnded, 5*time.Second)
	if m.Status != MemberEnded || m.Ended == nil || !strings.Contains(m.Err, "exited (exit 7)") || !strings.Contains(m.Err, "prompt") {
		t.Fatalf("lead %+v", m)
	}
	_, core := fl.member("core")
	if got := typed(core); !slices.Equal(got, []string{"Build it.\r"}) {
		t.Fatalf("core was typed %q", got)
	}
	if m := memberState(t, e, run.ID, "core"); m.Status != MemberRunning {
		t.Fatalf("core %+v", m)
	}
	if got, _ := e.Get(run.ID); !logged(got, "lead ended before its prompt") || got.StoppedAt != nil {
		t.Fatalf("run %+v", got)
	}
}

// When a member cannot be launched, the members launched before it are
// stopped, no other is launched, and the launch fails with the reason. No
// run is left behind.
func TestLaunchFailureStopsStartedMembers(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = printOnce
	var forgotten []string
	e.OnForget = func(run Run) { forgotten = append(forgotten, run.ID) }
	limit := errors.New("session limit reached")
	fl.fail["core"] = limit
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it."), immediate("web", "Style it."), manual("tests", "")))
	if run != nil || !errors.Is(err, limit) || !strings.Contains(err.Error(), `"core"`) {
		t.Fatalf("Launch: %+v %v", run, err)
	}
	lead, p := fl.member("lead")
	if !p.Stopped() || lead.Info().Status != session.StatusStopped {
		t.Fatalf("lead was left running: %+v", lead.Info())
	}
	if got := typed(p); len(got) != 0 {
		t.Fatalf("lead was typed %q", got)
	}
	if names := fl.launched(); !slices.Equal(names, []string{"lead", "core"}) {
		t.Fatalf("launched %v", names)
	}
	if list := e.List(); len(list) != 0 {
		t.Fatalf("runs %+v", list)
	}
	if _, _, ok := e.MemberOf(lead.Info().ID); ok {
		t.Fatal("the stopped session still belongs to a run")
	}
	if len(forgotten) != 1 || !strings.HasPrefix(forgotten[0], "api-sweep-") {
		t.Fatalf("OnForget saw %v", forgotten)
	}
}

// A crew with no members, one that runs on a host, or an invalid one does not
// launch; nothing starts.
func TestLaunchRefusesCrewsThatCannotRun(t *testing.T) {
	e, fl := newEngine(t)
	hosted := testCrew(immediate("lead", ""))
	hosted.Where = WhereHost
	invalid := testCrew(immediate("Lead!", ""))
	badID := testCrew(immediate("lead", ""))
	badID.ID = "../escape"
	for name, c := range map[string]Crew{"no members": testCrew(), "on a host": hosted, "invalid": invalid, "an id that is none": badID} {
		if _, err := e.Launch(t.Context(), c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(fl.launched()) != 0 || len(e.List()) != 0 {
		t.Fatalf("launched %v, runs %v", fl.launched(), e.List())
	}
}

// With isolation "worktree" the working directory must be a git repository;
// otherwise the launch fails before anything is made or started.
func TestLaunchNeedsARepositoryForWorktrees(t *testing.T) {
	newRepo(t) // skips without git
	e, fl := newEngine(t)
	c := testCrew(immediate("lead", "Plan it."))
	c.Isolation, c.Cwd = IsolationWorktree, t.TempDir()
	if _, err := e.Launch(t.Context(), c); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("Launch: %v", err)
	}
	if entries, _ := os.ReadDir(c.Cwd); len(entries) != 0 || len(fl.launched()) != 0 || len(e.List()) != 0 {
		t.Fatalf("made %v, launched %v, runs %v", entries, fl.launched(), e.List())
	}
}

// With isolation "worktree" and no git on the server's PATH, the launch says
// so (ErrNoGit), not that the directory is no repository, before anything is
// made or started.
func TestLaunchSaysWhenGitIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	e, fl := newEngine(t)
	c := testCrew(immediate("lead", "Plan it."))
	c.Isolation, c.Cwd = IsolationWorktree, t.TempDir()
	_, err := e.Launch(t.Context(), c)
	if !errors.Is(err, ErrNoGit) || errors.Is(err, ErrNotRepo) || err.Error() != "git is not installed on the server" {
		t.Fatalf("Launch: %v", err)
	}
	if entries, _ := os.ReadDir(c.Cwd); len(entries) != 0 || len(fl.launched()) != 0 || len(e.List()) != 0 {
		t.Fatalf("made %v, launched %v, runs %v", entries, fl.launched(), e.List())
	}
	// A crew without worktrees needs no git.
	c.Isolation = IsolationNone
	if _, err := e.Launch(t.Context(), c); err != nil {
		t.Fatalf("Launch without worktrees: %v", err)
	}
}

// With isolation "worktree" each member works on a branch of its own in a
// worktree under <cwd>/.conductor/worktrees/<run>/<member>, made when it
// starts. A run reports what each branch adds and removes, read again at most
// every 10 s.
func TestLaunchMakesAWorktreePerMember(t *testing.T) {
	repo := newRepo(t)
	e, fl := newEngine(t)
	clock := time.Now()
	var clockMu sync.Mutex
	e.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	tick := func(d time.Duration) { clockMu.Lock(); clock = clock.Add(d); clockMu.Unlock() }
	fl.onLaunch = askAtOnce
	c := testCrew(immediate("lead", "Plan it."), manual("core", "Build it."))
	c.Isolation, c.Cwd = IsolationWorktree, repo
	run, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".conductor", "worktrees", run.ID, "lead")
	lead := run.Members[0]
	if lead.Worktree != path || lead.Branch != "crew/"+run.ID+"/lead" || fl.calls[0].cwd != path {
		t.Fatalf("lead %+v, launched in %s", lead, fl.calls[0].cwd)
	}
	if branch := runGit(t, path, "rev-parse", "--abbrev-ref", "HEAD"); branch != lead.Branch {
		t.Fatalf("the worktree is on %q", branch)
	}
	if core := run.Members[1]; core.Worktree != "" || core.Branch != "" {
		t.Fatalf("a member that has not started has a worktree: %+v", core)
	}
	diff := func() *Diff {
		t.Helper()
		got, ok := e.GetWithDiffs(t.Context(), run.ID)
		if !ok {
			t.Fatal("run not found")
		}
		if got.Members[1].Diff != nil {
			t.Fatalf("core has a diff: %+v", got.Members[1])
		}
		return got.Members[0].Diff
	}
	// A read cut short by its request is read again by the next.
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if got, ok := e.GetWithDiffs(cancelled, run.ID); !ok || got.Members[0].Diff != nil {
		t.Fatalf("a cancelled read: %v %+v", ok, got.Members[0])
	}
	if d := diff(); d == nil || *d != (Diff{}) {
		t.Fatalf("diff before any commit: %+v", d)
	}
	writeFile(t, filepath.Join(path, "plan.md"), "a\nb\n")
	commitAll(t, path, "plan")
	if d := diff(); d == nil || *d != (Diff{}) {
		t.Fatalf("diff read again within 10 s: %+v", d)
	}
	tick(diffTTL)
	if d := diff(); d == nil || *d != (Diff{Added: 2}) {
		t.Fatalf("diff after 10 s: %+v", d)
	}
	if r, _ := e.Get(run.ID); r.Members[0].Diff != nil {
		t.Fatalf("Get carries a diff: %+v", r.Members[0])
	}
	// A read that fails keeps what was read before.
	moved := path + ".away"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	tick(diffTTL)
	if d := diff(); d == nil || *d != (Diff{Added: 2}) {
		t.Fatalf("diff after a failed read: %+v", d)
	}
	if err := os.Rename(moved, path); err != nil {
		t.Fatal(err)
	}
	// The worktrees are kept out of the repository's own status.
	if status := runGit(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("git status in the main checkout:\n%s", status)
	}

	// A member started later gets its worktree then.
	if err := e.StartMember(t.Context(), run.ID, "core"); err != nil {
		t.Fatal(err)
	}
	core := memberState(t, e, run.ID, "core")
	if core.Worktree != filepath.Join(repo, ".conductor", "worktrees", run.ID, "core") || core.Branch != "crew/"+run.ID+"/core" {
		t.Fatalf("core %+v", core)
	}
	// Stopping the run keeps the worktrees.
	if err := e.Stop(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, core.Worktree} {
		if _, err := os.Stat(filepath.Join(p, "README")); err != nil {
			t.Fatalf("worktree %s: %v", p, err)
		}
	}
	// A second launch excludes .conductor/ no second time.
	again, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, again.ID, "lead", MemberRunning, 5*time.Second)
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	lines := strings.Split(string(exclude), "\n")
	if n := len(slices.DeleteFunc(lines, func(l string) bool { return l != ".conductor/" })); err != nil || n != 1 {
		t.Fatalf("info/exclude holds .conductor/ %d times (%v):\n%s", n, err, exclude)
	}
	if status := runGit(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("git status in the main checkout:\n%s", status)
	}
}

// A member that starts by hand starts when asked, once; asking for a member
// or a run that does not exist fails.
func TestStartMemberStartsAPendingMember(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), manual("tests", "Test it."), after("docs", "Write it up.", "tests")))
	if err != nil {
		t.Fatal(err)
	}
	if m := run.Members[1]; m.Status != MemberPending || m.SessionID != "" {
		t.Fatalf("tests before its start: %+v", m)
	}
	if err := e.StartMember(t.Context(), run.ID, "tests"); err != nil {
		t.Fatal(err)
	}
	m := waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)
	_, p := fl.member("tests")
	if m.Status != MemberRunning || m.SessionID == "" || !slices.Equal(typed(p), []string{"Test it.\r"}) {
		t.Fatalf("tests %+v", m)
	}
	if err := e.StartMember(t.Context(), run.ID, "tests"); !errors.Is(err, ErrMemberStarted) {
		t.Fatalf("starting it again: %v", err)
	}
	// A member that starts after another can be started early.
	if err := e.StartMember(t.Context(), run.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	if err := e.StartMember(t.Context(), run.ID, "nobody"); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("an unknown member: %v", err)
	}
	if err := e.StartMember(t.Context(), "nope", "tests"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("an unknown run: %v", err)
	}
	if names := fl.launched(); !slices.Equal(names, []string{"lead", "tests", "docs"}) {
		t.Fatalf("launched %v", names)
	}
}

// A member can join a running run: one that starts immediately starts, one
// that starts after another waits for it. The run holds members to the rules
// of a crew.
func TestAddMemberJoinsARun(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddMember(t.Context(), run.ID, immediate("core", "Build $GOAL.")); err != nil {
		t.Fatal(err)
	}
	_, p := fl.member("core")
	if m := waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second); !slices.Equal(typed(p), []string{"Build ship /v1/users.\r"}) {
		t.Fatalf("core %+v", m)
	}
	if err := e.AddMember(t.Context(), run.ID, after("tests", "Test it.", "core")); err != nil {
		t.Fatal(err)
	}
	if m := memberState(t, e, run.ID, "tests"); m.Status != MemberPending {
		t.Fatalf("tests %+v", m)
	}
	core, _ := fl.member("core")
	core.SetAttention(session.AttentionDone, "done", session.SourceAPI)
	waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)

	for name, m := range map[string]Member{
		"a name used twice":      immediate("core", ""),
		"an invalid name":        immediate("Core!", ""),
		"after a missing member": after("docs", "", "ghost"),
		"after itself":           after("docs", "", "docs"),
		"an unknown start":       {Name: "docs", AgentID: "shell", Start: Start{When: "later"}},
	} {
		if err := e.AddMember(t.Context(), run.ID, m); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for i := len(memberStates(t, e, run.ID)); i < maxMembers; i++ {
		if err := e.AddMember(t.Context(), run.ID, manual(fmt.Sprintf("m%d", i), "")); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.AddMember(t.Context(), run.ID, manual("one-too-many", "")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a 13th member: %v", err)
	}
	if err := e.AddMember(t.Context(), "nope", manual("x", "")); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("an unknown run: %v", err)
	}
	if got, _ := e.Get(run.ID); len(got.Members) != maxMembers || !logged(got, "core joined") {
		t.Fatalf("run %+v", got)
	}
}

// A member that joins a run is held to the size of what is typed as its
// prompt, with the run's goal in it.
func TestAddMemberRefusesAPromptTooLongToType(t *testing.T) {
	e, _ := newEngine(t)
	c := testCrew(manual("lead", ""))
	c.Goal = strings.Repeat("é", 2000)
	run, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddMember(t.Context(), run.ID, manual("tests", strings.Repeat("$GOAL ", 9))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("9 references: %v", err)
	}
	if err := e.AddMember(t.Context(), run.ID, manual("tests", strings.Repeat("$GOAL ", 8))); err != nil {
		t.Fatalf("8 references: %v", err)
	}
}

func memberStates(t *testing.T, e *Engine, runID string) []MemberState {
	t.Helper()
	r, ok := e.Get(runID)
	if !ok {
		t.Fatalf("run %s not found", runID)
	}
	return r.Members
}

// Stop stops every member's session and marks the run stopped; nothing starts
// in a stopped run afterwards, and stopping it again changes nothing.
func TestStopStopsEveryMember(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it."), after("tests", "", "core"), manual("docs", "")))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lead", "core"} {
		l, p := fl.member(name)
		if !p.Stopped() || l.Info().Status != session.StatusStopped {
			t.Errorf("%s: %+v", name, l.Info())
		}
		if m := memberState(t, e, run.ID, name); m.Status != MemberEnded || m.Ended == nil {
			t.Errorf("%s: %+v", name, m)
		}
	}
	got, _ := e.Get(run.ID)
	if got.StoppedAt == nil || !logged(got, "stopped") {
		t.Fatalf("run %+v", got)
	}
	stoppedAt := *got.StoppedAt
	core, _ := fl.member("core")
	e.OnActivity(core.Info().ID, session.ActivityEntry{Type: session.ActivityAttention, Message: "done", At: time.Now().UTC()}, session.AttentionDone, "")
	if err := e.StartMember(t.Context(), run.ID, "docs"); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("starting a member of a stopped run: %v", err)
	}
	if err := e.AddMember(t.Context(), run.ID, manual("late", "")); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("adding a member to a stopped run: %v", err)
	}
	if err := e.Stop(t.Context(), run.ID); err != nil {
		t.Fatalf("stopping again: %v", err)
	}
	// OnActivity has returned, and it reserves the starts a done makes before
	// it does: tests would be starting, and the run log would say so.
	if n := logCount(t, e, run.ID, "core is done"); n != 0 {
		t.Fatalf("%d starts after core's done in a stopped run", n)
	}
	for _, name := range []string{"tests", "docs"} {
		if m := memberState(t, e, run.ID, name); m.Status != MemberPending || m.SessionID != "" {
			t.Fatalf("%s in a stopped run: %+v", name, m)
		}
	}
	if names := fl.launched(); !slices.Equal(names, []string{"lead", "core"}) {
		t.Fatalf("launched %v", names)
	}
	if got, _ := e.Get(run.ID); !got.StoppedAt.Equal(stoppedAt) {
		t.Fatalf("stopped at %v, then %v", stoppedAt, got.StoppedAt)
	}
	if err := e.Stop(t.Context(), "nope"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("stopping an unknown run: %v", err)
	}
}

// Stopping a run while a member waits to be ready ends the wait at once: the
// start fails, its session is stopped and no prompt is typed.
func TestStopEndsAStartInProgress(t *testing.T) {
	e, fl := newEngine(t) // processes that never print: never ready
	run, err := e.Launch(t.Context(), testCrew(manual("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	// StartMember returns once the session exists; the member then waits.
	if err := e.StartMember(t.Context(), run.ID, "lead"); err != nil {
		t.Fatal(err)
	}
	if m := memberState(t, e, run.ID, "lead"); m.Status != MemberStarting || m.SessionID == "" {
		t.Fatalf("lead %+v", m)
	}
	began := time.Now()
	if err := e.Stop(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("Stop took %v", took)
	}
	l, p := fl.member("lead")
	if !p.Stopped() || l.Info().Status != session.StatusStopped || len(typed(p)) != 0 {
		t.Fatalf("lead %+v, typed %q", l.Info(), typed(p))
	}
	if m := memberState(t, e, run.ID, "lead"); m.Status != MemberEnded {
		t.Fatalf("lead %+v", m)
	}
}

// When a member is not ready after readyCap, its prompt is typed anyway and
// the run log says so.
func TestNotReadyAfterTheCapTypesAnyway(t *testing.T) {
	e, fl := newEngine(t)
	e.await = func(context.Context, *session.Local, func(string)) error { return errNotReady }
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	_, p := fl.member("lead")
	if got := typed(p); !slices.Equal(got, []string{"Plan it.\r"}) {
		t.Fatalf("lead was typed %q", got)
	}
	if got, _ := e.Get(run.ID); !logged(got, "not ready after 1m0s") {
		t.Fatalf("log %+v", got.Log)
	}
}

// A member without a prompt runs as soon as its session starts.
func TestAMemberWithoutAPromptRunsAtOnce(t *testing.T) {
	e, fl := newEngine(t)
	e.await = func(context.Context, *session.Local, func(string)) error {
		t.Error("waited for a member with no prompt to be ready")
		return nil
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("logs", "  \n")))
	if err != nil {
		t.Fatal(err)
	}
	_, p := fl.member("logs")
	if m := waitStatus(t, e, run.ID, "logs", MemberRunning, time.Second); len(typed(p)) != 0 {
		t.Fatalf("logs %+v", m)
	}
}

// The readiness rule: a member is ready when its agent says it waits or is
// done, or once its output has been quiet for a second after the first output
// and two seconds have passed; after a minute the wait gives up.
func TestReadiness(t *testing.T) {
	start := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return start.Add(d) }
	s := time.Second
	needs, done, working, none := session.AttentionNeedsInput, session.AttentionDone, session.AttentionWorking, session.AttentionNone
	for _, tc := range []struct {
		name                string
		rd                  readiness
		now                 time.Time
		ready, capped, hold bool
	}{
		{"waiting for input at once", readiness{state: needs, source: session.SourceAPI}, at(0), true, false, false},
		{"done at once", readiness{state: done}, at(s / 2), true, false, false},
		{"working, output still coming", readiness{state: working, lastOutput: at(4500 * time.Millisecond)}, at(5 * s), false, false, false},
		{"working, but quiet", readiness{state: working, lastOutput: at(0)}, at(5 * s), true, false, false},
		{"no output yet", readiness{state: none}, at(5 * s), false, false, false},
		{"output still coming", readiness{state: none, lastOutput: at(4500 * time.Millisecond)}, at(5 * s), false, false, false},
		{"quiet, but too early", readiness{state: none, lastOutput: at(0)}, at(1500 * time.Millisecond), false, false, false},
		{"quiet for a second at two seconds", readiness{state: none, lastOutput: at(s)}, at(2 * s), true, false, false},
		{"never quiet", readiness{state: none, lastOutput: at(60 * s)}, at(60 * s), false, true, false},
		{"no output ever", readiness{state: none}, at(61 * s), false, true, false},
		{"bracketed paste on: a prompt", readiness{state: none, lastOutput: at(s), pasteSeen: true, pasteOn: true}, at(2 * s), true, false, false},
		{"bracketed paste on, then off: between screens", readiness{state: none, lastOutput: at(s), pasteSeen: true}, at(5 * s), false, false, false},
		{"between screens past the cap", readiness{state: none, lastOutput: at(s), pasteSeen: true}, at(61 * s), false, true, false},
		{"between screens, but asking", readiness{state: needs, source: session.SourceAPI, pasteSeen: true}, at(s), true, false, false},
		{"a trust question", readiness{state: needs, source: session.SourceTrust, lastOutput: at(0)}, at(5 * s), false, false, true},
		{"a trust question past the cap", readiness{state: needs, source: session.SourceTrust}, at(90 * s), false, false, true},
		{"a question asked during the work", readiness{state: needs, source: session.SourceQuestion, lastOutput: at(0)}, at(5 * s), false, false, true},
	} {
		ready, capped, hold := readyAt(tc.rd, start, tc.now)
		if ready != tc.ready || capped != tc.capped || hold != tc.hold {
			t.Errorf("%s: ready %v capped %v hold %v, want %v %v %v", tc.name, ready, capped, hold, tc.ready, tc.capped, tc.hold)
		}
	}
}

// A member whose session shows a done is ready at once; an Enter after it
// begins the agent's next turn, and the session shows the empty state:
// not ready at once, the member waits for its output to go quiet. The
// rule is TestReadiness's; what the session shows is not.
func TestAnEnterAfterADoneIsNotReadyAtOnce(t *testing.T) {
	p := sessiontest.NewFakeProc()
	t.Cleanup(func() { p.End(0) })
	l := session.NewLocal(session.Info{ID: session.NewID(), Name: "lead", Cwd: t.TempDir(), Cols: 80, Rows: 24}, p, session.Options{Log: quietLog})
	shown := func() readiness {
		att := l.Info().Attention
		seen, on := l.BracketedPaste()
		return readiness{state: att.State, source: att.Source, lastOutput: l.LastOutputAt(), pasteSeen: seen, pasteOn: on}
	}
	l.SetAttentionFull(session.AttentionDone, "All tests pass.", session.SourceAPI, session.KindDone, nil)
	start := time.Now()
	if ready, _, _ := readyAt(shown(), start, start); !ready {
		t.Fatalf("a done is not ready: %+v", shown())
	}
	sub, err := l.Attach("", session.RoleControl, "", 0, 0, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Input(sub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	<-p.Input
	if rd := shown(); rd.state != session.AttentionNone {
		t.Fatalf("after the Enter the session shows %+v", rd)
	} else if ready, _, _ := readyAt(rd, start, start); ready {
		t.Fatalf("ready at once after the Enter: %+v", rd)
	}
	// Quiet output makes it ready, by the test's fast bounds.
	if err := p.Print("lead> "); err != nil {
		t.Fatal(err)
	}
	w := readyWait{poll: 5 * time.Millisecond, quiet: 20 * time.Millisecond}
	if err := w.await(t.Context(), l, func(q string) { t.Errorf("held for %q", q) }); err != nil {
		t.Fatal(err)
	}
}

// A run keeps the newest maxRunLog entries of its log.
func TestRunLogKeepsTheNewest(t *testing.T) {
	r := &run{}
	for i := range maxRunLog + 50 {
		r.note(session.ActivityStatus, "entry %d", i)
	}
	if len(r.log) != maxRunLog || r.log[0].Message != "entry 50" || r.log[maxRunLog-1].Message != fmt.Sprintf("entry %d", maxRunLog+49) {
		t.Fatalf("%d entries, from %q to %q", len(r.log), r.log[0].Message, r.log[len(r.log)-1].Message)
	}
	long := strings.Repeat("x", 2*session.MaxAttentionMessage)
	r.note(session.ActivityError, "%s", long)
	if got := r.log[len(r.log)-1]; len(got.Message) > session.MaxAttentionMessage || got.At.IsZero() {
		t.Fatalf("entry of %d bytes at %v", len(got.Message), got.At)
	}
}

// The engine keeps at most maxRuns runs: a launch past it forgets the oldest
// run that has nothing running.
func TestOldRunsAreForgotten(t *testing.T) {
	e, _ := newEngine(t)
	var forgotten []string
	e.OnForget = func(run Run) { forgotten = append(forgotten, run.ID) }
	var first, second string
	for i := range maxRuns + 1 {
		run, err := e.Launch(t.Context(), testCrew(manual("lead", "")))
		if err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			first = run.ID
		case 1:
			second = run.ID
		}
	}
	if _, ok := e.Get(first); ok {
		t.Fatal("the oldest run is still kept")
	}
	if _, ok := e.Get(second); !ok || len(e.List()) != maxRuns {
		t.Fatalf("%d runs kept", len(e.List()))
	}
	if !slices.Equal(forgotten, []string{first}) {
		t.Fatalf("OnForget saw %v, want [%s]", forgotten, first)
	}
}

// A run is never forgotten while it is being launched: a crew whose members
// all start later has nothing running once it is kept, and a launch at the
// cap that runs meanwhile forgets the oldest run that is not being launched,
// so Launch never answers with a run it no longer has.
func TestALaunchingRunIsNeverForgotten(t *testing.T) {
	e, _ := newEngine(t)
	var forgotten []string
	e.OnForget = func(run Run) { forgotten = append(forgotten, run.ID) }
	// Launch keeps its run first (add), then starts what starts at once.
	launching := e.add(testCrew(manual("lead", ""), after("tests", "", "lead")), prepared{}, "")
	var first string
	for i := range maxRuns {
		run, err := e.Launch(t.Context(), testCrew(manual("lead", "")))
		if err != nil {
			t.Fatal(err)
		}
		if len(run.Members) != 1 || run.ID == "" {
			t.Fatalf("launch %d answered %+v", i, run)
		}
		if i == 0 {
			first = run.ID
		}
	}
	if _, ok := e.Get(launching.id); !ok {
		t.Fatal("the run being launched was forgotten")
	}
	if !slices.Equal(forgotten, []string{first}) || len(e.List()) != maxRuns {
		t.Fatalf("OnForget saw %v, want [%s]; %d runs kept", forgotten, first, len(e.List()))
	}
}

// Note adds an entry to a run's log, cleaned and bounded as the run's own;
// a run the engine does not have takes none.
func TestNoteAddsToTheRunLog(t *testing.T) {
	e, _ := newEngine(t)
	run, err := e.Launch(t.Context(), testCrew(manual("lead", "")))
	if err != nil {
		t.Fatal(err)
	}
	e.Note(run.ID, session.ActivityLink, "link created: standup\x07 (view)")
	e.Note("nope", session.ActivityLink, "lost")
	got, _ := e.Get(run.ID)
	last := got.Log[len(got.Log)-1]
	if last.Type != session.ActivityLink || last.Message != "link created: standup (view)" || last.At.IsZero() {
		t.Fatalf("last entry %+v", last)
	}
	for range maxRunLog {
		e.Note(run.ID, session.ActivityLink, "again")
	}
	if got, _ := e.Get(run.ID); len(got.Log) != maxRunLog {
		t.Fatalf("%d entries", len(got.Log))
	}
}

// A crew whose working directory is inside a repository works in that
// directory of its worktrees, which live under it.
func TestLaunchFromADirectoryInARepository(t *testing.T) {
	repo := newRepo(t)
	sub := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "main.go"), "package main\n")
	commitAll(t, repo, "api")
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	c := testCrew(immediate("lead", "Plan it."))
	c.Isolation, c.Cwd = IsolationWorktree, sub
	run, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(sub, ".conductor", "worktrees", run.ID, "lead")
	if m := run.Members[0]; m.Worktree != worktree || fl.calls[0].cwd != filepath.Join(worktree, "services", "api") {
		t.Fatalf("lead %+v, launched in %s", m, fl.calls[0].cwd)
	}
	if _, err := os.Stat(filepath.Join(fl.calls[0].cwd, "main.go")); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	if status := runGit(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("git status in the main checkout:\n%s", status)
	}

	// A directory no commit has a file in is made in the worktree.
	empty := filepath.Join(repo, "scratch", "notes")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	c.Cwd = empty
	run, err = e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(empty, ".conductor", "worktrees", run.ID, "lead", "scratch", "notes")
	if cwd := fl.calls[1].cwd; cwd != want {
		t.Fatalf("launched in %s, want %s", cwd, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("the member's directory: %v", err)
	}
}

// Worktrees stay inside the working directory: a .conductor or
// .conductor/worktrees that is a symbolic link is refused (ErrInvalid) before
// git runs.
func TestLaunchRefusesWorktreesThroughASymlink(t *testing.T) {
	for _, link := range []string{".conductor", filepath.Join(".conductor", "worktrees")} {
		repo := newRepo(t)
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(repo, link)); err != nil {
			t.Fatal(err)
		}
		e, fl := newEngine(t)
		c := testCrew(immediate("lead", "Plan it."))
		c.Isolation, c.Cwd = IsolationWorktree, repo
		if _, err := e.Launch(t.Context(), c); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("%s: %v", link, err)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 || len(fl.launched()) != 0 || len(e.List()) != 0 {
			t.Fatalf("%s: made %v, launched %v", link, entries, fl.launched())
		}
	}
}

// A directory on the way to a member's directory in its worktree that is a
// symbolic link, as a commit may hold one, is refused (ErrInvalid) before
// anything is made through it.
func TestLaunchRefusesASymlinkOnTheWayToTheMembersDirectory(t *testing.T) {
	repo := newRepo(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "scratch")); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repo, "a link out")
	// The main checkout has a directory where the commit has the link.
	if err := os.Remove(filepath.Join(repo, "scratch")); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(repo, "scratch", "notes")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	e, fl := newEngine(t)
	c := testCrew(immediate("lead", "Plan it."))
	c.Isolation, c.Cwd = IsolationWorktree, cwd
	if _, err := e.Launch(t.Context(), c); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Launch: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 || len(fl.launched()) != 0 || len(e.List()) != 0 {
		t.Fatalf("made %v outside, launched %v", entries, fl.launched())
	}
}

// When the repository's info/exclude cannot be written, the worktrees work
// without it and the run log says so once, not once per member.
func TestAnExcludeFailureIsNotedOncePerRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	repo := newRepo(t)
	// The exclude file can be read (git reads it when it adds a worktree, and
	// a newer git refuses to add one when it cannot) but not written: the
	// append of the worktrees' line fails, once per run.
	info := filepath.Join(repo, ".git", "info")
	exclude := filepath.Join(info, "exclude")
	if err := os.WriteFile(exclude, []byte("# kept read-only by the test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Chmod, not WriteFile's mode: git init made the file already.
	if err := os.Chmod(exclude, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(info, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(info, 0o755); _ = os.Chmod(exclude, 0o644) })
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	c := testCrew(immediate("lead", "Plan it."), immediate("core", "Build it."), manual("tests", ""))
	c.Isolation, c.Cwd = IsolationWorktree, repo
	run, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartMember(t.Context(), run.ID, "tests"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Get(run.ID)
	n := 0
	for _, entry := range got.Log {
		if strings.Contains(entry.Message, "info/exclude") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d notes of the exclude failure: %+v", n, got.Log)
	}
}

// A member whose prompt cannot be typed ends alone; the run and the other
// members go on.
func TestAPromptFailureEndsOnlyThatMember(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	boom := errors.New("boom")
	e.await = func(ctx context.Context, l *session.Local, held func(string)) error {
		if l.Info().Crew.Member == "lead" {
			return boom
		}
		return AwaitReady(ctx, l, held)
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it.")))
	if err != nil {
		t.Fatal(err)
	}
	m := waitStatus(t, e, run.ID, "lead", MemberEnded, 5*time.Second)
	if !strings.Contains(m.Err, "boom") {
		t.Fatalf("lead %+v", m)
	}
	lead, p := fl.member("lead")
	select {
	case <-lead.Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("lead's session still runs")
	}
	if !p.Stopped() || lead.Info().Status != session.StatusStopped || len(typed(p)) != 0 {
		t.Fatalf("lead %+v", lead.Info())
	}
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
	if got, _ := e.Get(run.ID); got.StoppedAt != nil || !logged(got, "lead could not start") {
		t.Fatalf("run %+v", got)
	}
}

// A process that exits as its prompt is written ends its member as an early
// exit, not as a failure to write.
func TestAnExitAsThePromptIsTypedIsAnEarlyExit(t *testing.T) {
	e, fl := newEngine(t)
	e.await = func(_ context.Context, l *session.Local, _ func(string)) error {
		_, p := fl.member(l.Info().Crew.Member)
		p.End(3)
		return nil
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	m := waitStatus(t, e, run.ID, "lead", MemberEnded, 5*time.Second)
	if !strings.Contains(m.Err, "exited (exit 3) before its prompt") {
		t.Fatalf("lead %+v", m)
	}
}

// A done the agent reports as its prompt is being written counts.
func TestADoneAsThePromptIsTypedCounts(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = func(member string, p *sessiontest.FakeProc, l *session.Local) {
		if member != "core" {
			return
		}
		l.SetAttention(session.AttentionNeedsInput, "what next?", session.SourceAPI)
		readSubmission(p, 10*time.Second) // the prompt and its Enter: done at once
		l.SetAttention(session.AttentionDone, "finished", session.SourceAPI)
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("core", "Build it."), after("tests", "", "core")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "tests", MemberRunning, 5*time.Second)
}

// A Stop while Launch starts the sessions keeps the run, stopped: Launch
// says the run is stopped, and nothing is left running.
func TestStopDuringLaunchKeepsTheRun(t *testing.T) {
	e, fl := newEngine(t)
	fl.block = map[string]bool{"core": true}
	done := make(chan error, 1)
	go func() {
		_, err := e.Launch(context.Background(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it."), immediate("web", "Style it.")))
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(fl.launched(), "core") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	runs := e.List()
	if len(runs) != 1 {
		t.Fatalf("runs %+v", runs)
	}
	if err := e.Stop(t.Context(), runs[0].ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrRunStopped) {
			t.Fatalf("Launch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Launch still runs after Stop")
	}
	got, ok := e.Get(runs[0].ID)
	if !ok || got.StoppedAt == nil {
		t.Fatalf("run %v %+v", ok, got)
	}
	lead, p := fl.member("lead")
	if !p.Stopped() || lead.Info().Status != session.StatusStopped {
		t.Fatalf("lead %+v", lead.Info())
	}
	if m := memberState(t, e, got.ID, "lead"); m.Status != MemberEnded {
		t.Fatalf("lead %+v", m)
	}
	if m := memberState(t, e, got.ID, "web"); m.Status != MemberPending {
		t.Fatalf("web %+v", m)
	}
	if names := fl.launched(); !slices.Equal(names, []string{"lead", "core"}) {
		t.Fatalf("launched %v", names)
	}
}

// A stop that lands before any member's start is reserved ends the launch as
// a later stop does: ErrRunStopped, the run kept and stopped, nothing started.
func TestStopBeforeTheFirstStartEndsTheLaunch(t *testing.T) {
	e, fl := newEngine(t)
	e.afterAdd = func(runID string) {
		if err := e.Stop(context.Background(), runID); err != nil {
			t.Error(err)
		}
	}
	if _, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), manual("tests", ""))); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("Launch: %v", err)
	}
	runs := e.List()
	if len(runs) != 1 || runs[0].StoppedAt == nil {
		t.Fatalf("runs %+v", runs)
	}
	if names := fl.launched(); len(names) != 0 {
		t.Fatalf("launched %v", names)
	}
	if m := memberState(t, e, runs[0].ID, "lead"); m.Status != MemberPending {
		t.Fatalf("lead %+v", m)
	}
}

// A run stays exempt from eviction until its launcher lets it go. The API
// mints the run's view link in between, and a launch at the cap meanwhile
// must not forget the run.
func TestAHeldRunIsNeverForgotten(t *testing.T) {
	e, _ := newEngine(t)
	var forgotten []string
	e.OnForget = func(run Run) { forgotten = append(forgotten, run.ID) }
	held, release, err := e.LaunchHeld(t.Context(), testCrew(manual("lead", "")))
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxRuns {
		if _, err := e.Launch(t.Context(), testCrew(manual("lead", ""))); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}
	if _, ok := e.Get(held.ID); !ok {
		t.Fatal("the held run was forgotten")
	}
	release()
	release() // once is enough; twice changes nothing
	if _, err := e.Launch(t.Context(), testCrew(manual("lead", ""))); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(held.ID); ok || len(forgotten) < 2 || forgotten[len(forgotten)-1] != held.ID {
		t.Fatalf("the released run, the oldest idle one, was kept past the cap: %v", forgotten)
	}
}

// IfKept runs f under the engine's lock while the run is kept, and not at all
// otherwise: a link made in f cannot outlive a run forgotten meanwhile.
func TestIfKeptRunsUnderTheLockOfAKeptRun(t *testing.T) {
	e, _ := newEngine(t)
	run, err := e.Launch(t.Context(), testCrew(manual("lead", "")))
	if err != nil {
		t.Fatal(err)
	}
	locked := false
	ran := e.IfKept(run.ID, func() {
		if e.mu.TryLock() {
			// Not held: let it go again, so that the check below fails
			// with its message instead of the next IfKept hanging.
			e.mu.Unlock()
			return
		}
		locked = true
	})
	if !ran || !locked {
		t.Fatalf("f ran %v, under the engine's lock %v", ran, locked)
	}
	if e.IfKept("nope", func() { t.Error("f ran for a run the engine does not have") }) {
		t.Fatal("IfKept reported a run the engine does not have")
	}
}

// A run's yolo choice is fixed when it is made: every member, one added
// later included, is launched with it.
func TestAYoloRunLaunchesEveryMemberWithIt(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	on := true
	c := testCrew(immediate("lead", "Plan it."))
	c.Yolo = &on
	run, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Yolo {
		t.Fatalf("run %+v", run)
	}
	if err := e.AddMember(t.Context(), run.ID, immediate("qa", "Check it.")); err != nil {
		t.Fatal(err)
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if len(fl.calls) != 2 || !fl.calls[0].yolo || !fl.calls[1].yolo {
		t.Fatalf("calls %+v", fl.calls)
	}
}

// A run named at launch keeps the name on every read, says it in its log,
// and passes it to the run that resumes it; a bad name is refused.
func TestLaunchKeepsTheRunsLabel(t *testing.T) {
	e, _ := newEngine(t)
	run, release, err := e.LaunchNamed(context.Background(), testCrew(manual("lead", "")), "fix the login bug")
	release()
	if err != nil || run.Label != "fix the login bug" {
		t.Fatalf("launch: %+v, %v", run, err)
	}
	got, _ := e.Get(run.ID)
	if got.Label != "fix the login bug" || !strings.Contains(got.Log[0].Message, `as "fix the login bug"`) {
		t.Fatalf("read: %q, log %q", got.Label, got.Log[0].Message)
	}
	if err := e.Stop(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	next, err := e.ResumeRun(context.Background(), run.ID)
	if err != nil || next.Label != "fix the login bug" {
		t.Fatalf("resume: %+v, %v", next, err)
	}
	plain, release, err := e.LaunchNamed(context.Background(), testCrew(manual("lead", "")), "")
	release()
	if err != nil || plain.Label != "" {
		t.Fatalf("unnamed: %q, %v", plain.Label, err)
	}
}

func TestLaunchRefusesABadLabel(t *testing.T) {
	e, _ := newEngine(t)
	for _, label := range []string{strings.Repeat("x", 61), "bell\x07"} {
		_, release, err := e.LaunchNamed(context.Background(), testCrew(manual("lead", "")), label)
		release()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "label must") {
			t.Fatalf("%q: %v", label, err)
		}
	}
}

// The readiness wait a crew's prompt and a launch's first message share
// holds each startup question once: a question drawn right after the one
// answered, with no look between, is held anew (held called again), so
// that two questions in a row give two notes; the prompt drawn, it is
// ready.
func TestTheReadinessWaitHoldsEachStartupQuestion(t *testing.T) {
	l, p, sub := firstSession(t, session.Options{
		TrustPattern: trustRe, TrustAnswers: []session.Option{{Label: "Yes", Input: "1"}},
		Questions: []session.StartupQuestion{{Pattern: regexp.MustCompile(`(?i)update\s*now\s*\(\s*runs`), Words: regexp.MustCompile(`(?i)update\s*available`), Answers: []session.Option{{Label: "Skip until next version", Input: "3"}}}},
	})
	p.Print("\x1b[2J\x1b[HUpdate available 0.1.0 -> 0.2.0\r\n› 1. Update now (runs the installer)\r\n  3. Skip until next version\r\n")
	update := untilShown(t, l, "the update dialog", startupShown)
	ticks := make(chan time.Time)
	var mu sync.Mutex
	var held []string
	done := make(chan error, 1)
	go func() {
		done <- readyWait{poll: time.Hour, ticks: ticks}.await(t.Context(), l, func(q string) {
			mu.Lock()
			held = append(held, q)
			mu.Unlock()
		})
	}()
	heldN := func(n int) {
		t.Helper()
		waitTrue(t, fmt.Sprintf("%d questions held", n), func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(held) >= n
		})
	}
	heldN(1)
	if err := l.Input(sub, []byte("3")); err != nil {
		t.Fatal(err)
	}
	<-p.Input
	p.Print(trustScreen)
	untilShown(t, l, "the trust question", func(a session.Attention) bool { return startupShown(a) && a.ID != update.ID })
	ticks <- time.Now()
	heldN(2)
	if err := l.Input(sub, []byte("1")); err != nil {
		t.Fatal(err)
	}
	<-p.Input
	p.Print("\x1b[2J\x1b[H> ")
	untilShown(t, l, "past the questions", func(a session.Attention) bool { return !startupShown(a) })
	for {
		select {
		case ticks <- time.Now():
			continue
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		}
		break
	}
	mu.Lock()
	defer mu.Unlock()
	if len(held) != 2 || !strings.Contains(held[0], "Update available") || !strings.Contains(held[1], "Trust this folder?") {
		t.Fatalf("held %q", held)
	}
}
