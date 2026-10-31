package crew

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A run is one launch of a crew. Every member becomes a server session of
// its own, launched like any other (Launcher) and tagged with the run
// (session.Info.Crew). A member starts when its start condition says: at
// once, once the member it waits for first reports done after its prompt,
// or when asked. Once its session is ready its role prompt is typed into it.
// A handoff a member reports is typed into the member it names, once that one
// does not wait for input. Runs live in memory, as sessions do: a server
// restart forgets them.

// Launcher starts the session of a crew member. The API server implements it
// on the path POST /api/sessions takes, so a member's session is launched
// like any other: its working directory checked, its agent looked up in the
// catalog, its environment built and its hooks injected. env holds variables
// to set in the process (GOAL); ref tags the session and gives its process
// CONDUCTOR_CREW, CONDUCTOR_RUN and CONDUCTOR_MEMBER.
type Launcher interface {
	Launch(ctx context.Context, spec LaunchSpec) (*session.Local, error)
	// Reserve holds a place for one session among the server's, for a
	// managed run's attempt (#334): the registry counts every session not
	// ended, so a refusal at the cap comes here, before the worktree, the
	// setup and the process exist, and spends nothing. The place goes to
	// the Launch given the reservation (LaunchSpec.Reservation), or is let
	// go with Release.
	Reserve(ctx context.Context) (*session.Reservation, error)
}

// LaunchSpec is a member's session as the engine asks a Launcher for it.
type LaunchSpec struct {
	AgentID, Name, Cwd string
	Args               []string
	// Env holds variables to set in the process: GOAL.
	Env map[string]string
	// Ref tags the session with its run.
	Ref session.CrewRef
	// Reservation is the place reserved for the session (Launcher.Reserve)
	// in a managed run; nil for a legacy member.
	Reservation *session.Reservation
	// Yolo is the run's yolo choice, fixed when the run was made: the agent's
	// yolo recipe is applied when it has one.
	Yolo bool
	// Resume, when set, is the agent session the launch resumes with its
	// agent's recipe (ResumeMember); ResumedFrom the session it follows.
	Resume, ResumedFrom string
	// Inject is Conductor's own names of the attempt's tree, set in the
	// process after the crew's as a session's own are, unfiltered: its
	// ports (CONDUCTOR_PORT_<NAME>), its state directory
	// (CONDUCTOR_STATE_DIR) and its preview (CONDUCTOR_PREVIEW_URL). Allow
	// is the project manifest's env names the server approved (allowedEnv:
	// in its envPassthrough and set in its environment), passed on beside
	// the session's own passthrough. Both nil without a project manifest.
	Inject map[string]string
	Allow  []string
}

// Member states (MemberState.Status).
const (
	MemberPending  = "pending"  // waiting for its start condition, or to be started by hand
	MemberStarting = "starting" // its worktree and session are being made, or it waits to be ready for its prompt
	MemberRunning  = "running"  // its session runs and its prompt has been typed
	MemberEnded    = "ended"    // its session ended, or it could not start (Err says why)
)

// When a member is ready for its prompt (awaitReady).
const (
	readyPoll  = 250 * time.Millisecond // how often it looks
	readyQuiet = time.Second            // output silence after the first output
	readyMin   = 2 * time.Second        // since the wait began, for the silence to count
	readyCap   = 60 * time.Second       // after this the prompt is typed anyway
)

// maxRuns is how many runs the engine keeps: a launch past it forgets the
// oldest run with nothing running. A variable, so that a test may lower it.
var maxRuns = 100

const (
	// maxRunLog bounds a run's log.
	maxRunLog = 200
	// diffTTL is how long GetWithDiffs keeps a member's diff.
	diffTTL = 10 * time.Second
	// typedBy is the name what a run types into a session is recorded by.
	typedBy = "crew"
	// stopTimeout bounds the stop of what a failed start leaves running.
	stopTimeout = 15 * time.Second
	// diffReaders bounds the DiffStat reads GetWithDiffs runs at once.
	diffReaders = 4
)

// worktreesDir is where a run's worktrees go under the crew's working
// directory, <cwd>/.conductor/worktrees/<run>/<member>, so that they pass
// the check every session's working directory passes.
var worktreesDir = filepath.Join(".conductor", "worktrees")

// Errors of the runs, beside ErrInvalid, ErrNotRepo and ErrNoGit.
var (
	ErrRunNotFound    = errors.New("no such run")
	ErrMemberNotFound = errors.New("no such member in the run")
	ErrMemberStarted  = errors.New("the member has started already")
	ErrRunStopped     = errors.New("the run is stopped")
	ErrMemberRunning  = errors.New("the member is still running")
	ErrRunRunning     = errors.New("the run is still running: stop it first")
	// ErrRunResumed is what a RunResumedError is (errors.Is).
	ErrRunResumed = errors.New("the run was resumed already")
	// ErrRunContinued refuses a change of a managed run's tasks or pause
	// once a continuation is made of it, or is being made: the
	// continuation owns that history, and a retry here would fork it.
	ErrRunContinued = errors.New("the run has been continued: change its continuation instead")
	// ErrRunUnsettled refuses a continuation of a run with changes the
	// ledger has not taken (its commits failed): the continuation would
	// read the run without them, and a later commit would write them
	// after it.
	ErrRunUnsettled = errors.New("the run has changes the ledger has not recorded yet: continue it once the ledger takes them")
	// ErrManaged says the run is a managed one (a crew with tasks, #334),
	// which the start, add and resume paths of a legacy run do not serve:
	// a managed run dispatches its own tasks, and continues after a restart
	// through the ledger, not through a member resumed in its old worktree.
	ErrManaged = errors.New("the run is managed: its tasks are scheduled by the run, and a member is never started, added or resumed by hand")
	// errNotReady is awaitReady giving up after readyCap.
	errNotReady = errors.New("not ready")
)

// Diff is what a member's worktree adds and removes against the commit it
// began from, in lines: tracked changes against the base, uncommitted
// included (DiffStat).
type Diff struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// MemberState is a member of a run as the run reports it.
type MemberState struct {
	Name    string `json:"name"`
	AgentID string `json:"agentId"`
	Start   Start  `json:"start"`
	// SessionID is the member's session once it has started.
	SessionID string `json:"sessionId,omitempty"`
	// Branch and Worktree are set with isolation "worktree" once it starts.
	Branch   string     `json:"branch,omitempty"`
	Worktree string     `json:"worktree,omitempty"`
	Status   string     `json:"status"` // pending | starting | running | ended
	Started  *time.Time `json:"startedAt,omitempty"`
	Ended    *time.Time `json:"endedAt,omitempty"`
	// Err says why a member ended before it ran: it could not start, or its
	// process ended before its prompt was typed.
	Err string `json:"error,omitempty"`
	// NeedsInput is set when the member is starting or running and its
	// session waits on a prompt: a trust question before its prompt, or a
	// question of its agent's.
	NeedsInput bool `json:"needsInput,omitempty"`
	// Task and Attempt are the member's latest attempt in a managed run.
	Task    string `json:"task,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	// AgentSession is the agent's own session in the member's latest session,
	// kept when that session has left the server: what ResumeMember resumes.
	AgentSession *session.AgentSession `json:"agentSession,omitempty"`
	// Prompt and Args are the member's as it ran (its crew's may have
	// changed since), and Base the commit its worktree branched from: what
	// a resume from the run's record needs (ResumeRecord).
	Prompt string   `json:"prompt,omitempty"`
	Args   []string `json:"args,omitempty"`
	Base   string   `json:"base,omitempty"`
	// Diff is set by GetWithDiffs for a member with a worktree: tracked
	// changes against the base, uncommitted included.
	Diff *Diff `json:"diff,omitempty"`
}

// Run states (Run.State), derived each time a run is read (refresh).
const (
	RunRunning    = "running"     // a member is pending, starting or running, and none waits on a prompt
	RunNeedsInput = "needs_input" // the session of a starting or running member waits on a prompt
	RunStopped    = "stopped"     // stopped (Stop): its sessions were stopped
	RunFinished   = "finished"    // every member ended, and none is pending
	// RunInterrupted is a record's state alone: the run was running when the
	// server last stopped (its record journaled, not ended), so its sessions
	// are gone; it is never relaunched on its own (Records).
	RunInterrupted = "interrupted"
)

// Run is a launch of a crew as the engine reports it.
type Run struct {
	ID     string `json:"id"`
	CrewID string `json:"crewId"`
	Name   string `json:"name"`
	// Label is the run's own name, given at launch and kept by a resume;
	// empty when none: the pages then say the crew and the start.
	Label     string        `json:"label,omitempty"`
	Goal      string        `json:"goal"`
	Cwd       string        `json:"cwd"`
	Isolation string        `json:"isolation"`
	StartedAt time.Time     `json:"startedAt"`
	StoppedAt *time.Time    `json:"stoppedAt,omitempty"`
	Members   []MemberState `json:"members"`
	// Yolo is the run's yolo choice, fixed at launch: every member, one added
	// or started later included, is launched with it.
	Yolo bool `json:"yolo"`
	// ResumedFrom is the stopped run this one resumed (ResumeRun), and
	// ResumedBy the run that resumed this one.
	ResumedFrom string `json:"resumedFrom,omitempty"`
	ResumedBy   string `json:"resumedBy,omitempty"`
	// State is the run's state (RunRunning…), NeedsInput how many members'
	// sessions wait on a prompt. A member's done keeps a run running: a done
	// agent is idle, not gone.
	State      string `json:"state"`
	NeedsInput int    `json:"needsInput"`
	// Log is the run's own log, oldest first: launched, member started,
	// prompt typed, handoff queued, delivered or dropped, stopped, and the
	// rest docs/protocol.md lists. At most maxRunLog entries.
	Log []session.ActivityEntry `json:"log"`
	// Managed is set for a managed run (a crew with tasks): its revision in
	// the ledger, which every transition bumps, so a record is known to be
	// behind the ledger or level with it. Absent for a legacy run, whose
	// JSON is unchanged.
	Managed *Managed `json:"managed,omitempty"`
	// Paused says a managed run's dispatch is paused, and PausedWhy what
	// paused it when the run did (a project manifest's step, the ledger);
	// Outcome, once every task is terminal, is succeeded, failed or
	// cancelled; Tasks are its tasks (task.go).
	Paused    bool        `json:"paused,omitempty"`
	PausedWhy string      `json:"pausedWhy,omitempty"`
	Outcome   string      `json:"outcome,omitempty"`
	Tasks     []TaskState `json:"tasks,omitempty"`
	// Manifest is a managed run's project manifest and how its
	// prerequisites go (steps.go); absent without one. Cleaning counts its
	// trees being ended: Outcome set means every attempt is closed, not
	// that every tree has ended.
	Manifest *RunManifest `json:"manifest,omitempty"`
	Cleaning int          `json:"cleaning,omitempty"`
	// Chat is the run's chat, oldest first, at most session.MaxRunChat
	// messages: on a run read alone (Get) and in its record, not in a list.
	Chat []proto.ChatMessage `json:"chat,omitempty"`
}

// Managed is what a managed run's record carries beside the run: the
// ledger's revision of it (docs/plans/agent-harness-order.md, decision 7).
type Managed struct {
	Revision int64 `json:"revision"`
}

// managed reports whether the run is a managed one, from its members: a
// crew with tasks starts every member by its tasks (StartTask), and a crew
// without starts none that way (Crew.Validate); a record of a managed run
// carries Managed too.
func (r Run) managed() bool {
	return r.Managed != nil || slices.ContainsFunc(r.Members, func(m MemberState) bool { return m.Start.When == StartTask })
}

// Engine launches crews and runs their runs. It never holds its lock while
// it starts, types into or stops a session.
type Engine struct {
	launcher Launcher
	lookup   func(sessionID string) (*session.Local, bool)
	// await waits for a member's session to be ready, calling held with the
	// question once each time a trust question holds the prompt: awaitReady,
	// but for tests.
	await func(ctx context.Context, l *session.Local, held func(question string)) error
	// now is the clock of the diff cache.
	now func() time.Time
	// excludeMu keeps two launches from writing a repository's info/exclude
	// at once.
	excludeMu sync.Mutex
	// ledger is the managed runs' ledger (UseLedger); nil, managed launches
	// are refused. gitMu keeps two dispatches from making worktrees of one
	// repository at once (task.go).
	ledger Ledger
	gitMu  sync.Mutex
	// exec is what a tree runs on the machine (steps.go: treeExec), native
	// unless a test replaces it.
	exec treeExec
	// checkSem bounds the checks running at once (checkSlots).
	checkSem chan struct{}
	// envPassthrough is the server's envPassthrough (SetEnvPassthrough):
	// the host's names a project manifest's env may pass on (allowedEnv),
	// none before it is set. ports are the port leases of the live trees,
	// by number (steps.go: a number is one tree's at a time). Both under mu.
	envPassthrough []string
	ports          map[int]*tree
	// teardownRoot is the context every managed run's teardown context is
	// made beneath (add), ended by teardownCut at the shutdown's cut-off
	// (steps.go: Draining).
	teardownRoot context.Context
	teardownCut  context.CancelFunc

	// bySession maps the ID of each member's session to its sessionMember.
	// It is read without mu, so that the activity and the changes of the
	// sessions no run has cost the engine no lock; it is written under mu.
	bySession sync.Map
	// held holds the ID of each run the engine keeps, from add to forget,
	// written under mu and read without it (holds): the records ask whether
	// a run is held as they prune, on the goroutine that saves a record,
	// which may be the one forgetting a run under mu.
	held sync.Map

	mu    sync.Mutex
	runs  map[string]*run
	order []*run // oldest first

	// OnForget is called with each run the engine forgets, as it last was
	// (its members and their sessions, no diffs or chat): past maxRuns, or
	// after a launch that failed. It runs under mu, so it must not call the
	// engine or wait. Set it before the first launch.
	OnForget func(run Run)

	// OnRunChange is called with the ID of a run each time something a read
	// of it shows changes that no session change carries: a member reserved,
	// started, running or ended, an entry in its log, a stop. It runs under
	// mu, so it must not call the engine or wait. Set it before the first
	// launch.
	OnRunChange func(runID string)

	// OnRunChat is called with the ID of a run and each message kept in its
	// chat (ChatRoom), outside the engine's lock, on the posting goroutine:
	// it must not block or call the engine. Set it before the first launch.
	OnRunChat func(runID string, m session.ChatMessage)

	// OnEnd is called with a run each time it ends: stopped (Stop), or
	// finished, every member ended, as the change of the last member's
	// session shows it (OnChange). It runs without the engine's lock, in
	// the goroutine that stopped the run or reported the change, so it
	// must not wait; RecordEnds sets it to save the run. Set it before the
	// first launch.
	OnEnd func(r Run)
	// recordsIdle, set by RecordEnds, gives a channel closed while no end
	// handed to the records waits to be written (WaitRecorded); journal,
	// set with it, saves a run as it is now (Journal).
	recordsIdle func() <-chan struct{}
	journal     func(r Run)
	// endMu orders the ends: a snapshot and its OnEnd are one step under it,
	// so the ends reach OnEnd in the order their snapshots were taken.
	endMu sync.Mutex

	// afterAdd, when set, runs once Launch has kept its run and before any
	// member's start is reserved: a test stops the run there.
	afterAdd func(runID string)
	// tried, when set, is called by deliver after each attempt to type a
	// handoff, with the member and whether it was typed: tests wait on it.
	tried func(member string, typed bool)
	// afterLookup, when set, runs once a command or RetryTask has looked
	// its run up and before its change: a test evicts the run there.
	afterLookup func(runID string)
	// afterVisit, when set, is called by withdraw for each task it visits,
	// under e.mu: a test counts the visits.
	afterVisit func(taskID string)
	// beforeContinue, when set, runs in ContinueRun once the run continued
	// is marked and before its ledger rows are read: a test lands a
	// change there.
	beforeContinue func(runID string)
	// afterTreeResolved, when set, runs in resolveTree once the tree is the
	// attempt's and before its state directory is made: a test closes the
	// attempt there.
	afterTreeResolved func(kind string)
	// beforeTreeEnd, when set, runs in endTree once the tree is quiet and
	// before its end is recorded: a test ends the engine there.
	beforeTreeEnd func(kind string)
	// beforeServiceEnd, when set, runs in a service's watcher once its
	// process has ended and before its end is recorded: a test holds the
	// watcher there.
	beforeServiceEnd func(id string)
	// awaitingStart, when set, runs in a stop that is to stop a service
	// whose start is under way, before it waits for the executor to hand
	// the service over (stopService): a test holds the start until then.
	awaitingStart func(id string)
	// beforeServe, when set, runs in a service's start once its started
	// row is committed and before the start is registered on its tree
	// (t.starting) and its process made: a test lands a stop there.
	beforeServe func(id string)
	// beforeCompare, when set, runs in runCheck once the check's process
	// has ended and before its checkout is compared, with the check's
	// context and id: a test closes the attempt there.
	beforeCompare func(ctx context.Context, checkID string)
	// beforePrereqEnd, when set, runs as a generation of the prerequisites
	// ends, after its last commit and before it is counted out: a test
	// lands an unpause there.
	beforePrereqEnd func()
	// beforePrereqRefused, when set, runs as prerequisiteRefused begins,
	// after the failed step's last commit and before its own change: a
	// test lands an unpause there.
	beforePrereqRefused func()
}

// sessionMember is the member a session is, and its run.
type sessionMember struct {
	r *run
	m *member
	// a is the attempt the session is, in a managed run; nil otherwise.
	a *attempt
}

// run is a launch of a crew. Engine.mu guards what changes after it is made.
type run struct {
	// managed says the run is a managed one (a crew with tasks, #334).
	managed bool
	// The managed run's tasks (plan order), the commit it started from
	// (read once, at the launch, or the continuation's), whether its
	// dispatch is paused and why when the run paused it, its attempts in
	// flight, its outcome once every task is terminal, the goroutines
	// verifying results (a stop waits for them) and its ledger revision
	// (task.go).
	plan      []Task
	base      string
	tasks     []*task
	paused    bool
	pausedWhy string
	active    int
	outcome   string
	checks    sync.WaitGroup
	revision  int64
	crewJSON  string // the crew as launched, for the ledger's run row
	// The run's project manifest (steps.go): the crew's effective one as
	// launched (manifest, with File for the file form; a legacy setup
	// lifted), its revision, and the manifest at the base (baseMan),
	// whose prerequisites the run runs. prereq is their state (pending →
	// running → passed | failed, or interrupted when the run finished or
	// stopped under a generation), prereqGen the generation the run's,
	// prereqs the generations in flight (0 or 1), prereqCancel the one in
	// flight's cancel, prereqStart that the commit in flight starts one,
	// prereqK each prerequisite's runs, prereqRows their latest receipts.
	// baseManRow is baseMan as the run's ledger row keeps it, "" without a
	// manifest.
	manifest     Manifest
	manifestRev  int64
	baseMan      resolvedManifest
	baseManRow   string
	prereq       string
	prereqGen    int
	prereqs      int
	prereqStart  bool
	prereqCancel context.CancelFunc
	prereqK      map[string]int
	prereqRows   map[string]prereqReceipt
	// prereqFailedGen is the last generation whose failure the run took,
	// by its step's finished row (runStep) or by prerequisiteRefused: a
	// generation fails once, so an unpause since, which armed the
	// prerequisites again, is not taken back by the same failure.
	prereqFailedGen int
	// teardown is the context the trees end under (endTree), made with the
	// run beside ctx and outliving it; trees counts the trees ending, and
	// pendingTrees are the ends a close handed over, started by its commit.
	// pendingReleases are the trees whose tree.ended a change recorded and
	// no commit has written yet: their ports stay leased until the commit
	// that does, the change's own or, after a refusal, the run's next,
	// which carries it (transact: releaseEnded). endTree, the one change
	// that adds to it, passes no undo, so a refusal keeps it with its events.
	teardown        context.Context
	teardownCancel  context.CancelFunc
	trees           flight
	pendingTrees    []treeEnd
	pendingReleases []*tree
	// messages are the members' messages to one another (messages.go), in
	// sequence, a continuation's carried from the run it continues.
	messages []*message
	// txMu orders the run's transitions (task.go: transition); the marks
	// are what changed since the last one, drained by the next.
	txMu            sync.Mutex
	changedTasks    map[*task]struct{}
	changedAttempts map[*attempt]struct{}
	runChanged      bool
	// endPending says the run's outcome was set in a transaction not yet
	// committed: OnEnd follows the commit (transact).
	endPending bool
	// pendingStops are the sessions to stop once the change that ends
	// their attempts is committed (a cancel): stopped by the transaction
	// that makes it durable.
	pendingStops []string
	// pendingCancels end the contexts of attempts cancelled, once the
	// cancel is committed: a check or a setup under way runs until then.
	pendingCancels []context.CancelFunc
	// pendingEvents and pendingMore are the events and the rows of their
	// own (a message's, a command's receipt) of changes whose commit was
	// refused with no undo, kept as the refusal comes (under txMu): the
	// next commit writes them first, in order (transact).
	pendingEvents []event
	pendingMore   []func(LedgerTx) error
	// committing says a transaction has taken the run's changes and not
	// yet heard from the ledger: refused, they come back to the run, which
	// must still be there (dirty).
	committing                                    bool
	id, crewID, name, label, goal, cwd, isolation string
	// yolo is the run's yolo choice, fixed when it is made.
	yolo bool
	// changed is the engine's OnRunChange, or nil.
	changed func(runID string)
	// prefix is where cwd lies in its repository, with worktrees: a member
	// works in <worktree>/<prefix>.
	prefix    string
	startedAt time.Time
	stoppedAt *time.Time
	members   []*member
	log       []session.ActivityEntry
	// chat is the run's chat, which every member session joins at launch.
	chat *session.ChatRoom

	// ctx ends when the run stops; every member start runs under it.
	ctx    context.Context
	cancel context.CancelFunc
	// stopping is set once the run begins to stop: no member starts after it;
	// stopDone once the stop completed (every session stopped, every start
	// and delivery drained), which a member's Resume may reopen.
	stopping, stopDone bool
	// resuming is set, under e.mu, while ResumeRun makes the run that
	// resumes this one, so that a second ResumeRun meanwhile is refused
	// (RunResumedError) rather than making another.
	resuming bool
	// resumedFrom and resumedBy link a run to the one it resumed, and to the
	// one that resumed it (ResumeRun).
	resumedFrom, resumedBy string
	// launching is set from add until the launcher releases the run
	// (LaunchHeld): evict leaves the run alone meanwhile, so that a crew whose
	// members all start later, which has nothing running once it is kept, is
	// not forgotten before Launch answers with it.
	launching bool
	// starts counts the member starts in flight; a stop waits for them.
	starts sync.WaitGroup
	// deliveries counts the goroutines typing handoffs (deliver); a stop
	// waits for them.
	deliveries sync.WaitGroup
	// excludeNoted is set once the log says info/exclude could not be written.
	excludeNoted bool
}

// RunResumedError refuses a ResumeRun of a run resumed already, or being
// resumed: By names the run that resumed it, "" while that run is still
// being made (a second ResumeRun landing meanwhile). One live session per
// agent conversation: a second continuation would put two agents on one
// branch, each appending to one transcript.
type RunResumedError struct{ By string }

func (e *RunResumedError) Error() string {
	if e.By == "" {
		return "the run is being resumed"
	}
	return "the run was resumed already, as " + e.By
}

func (e *RunResumedError) Is(target error) bool { return target == ErrRunResumed }

type member struct {
	def   Member
	state MemberState
	base  string // the commit its worktree branched from, for DiffStat
	// resuming is set, under e.mu, while a ResumeMember makes the member's
	// new session (its ended session's id still on the state): a second
	// resume meanwhile is refused, and only the one making the session
	// touches the member's state.
	resuming bool
	// prompted is set as its prompt is typed (or at once without one), and
	// promptedAt is when: a done whose entry is stamped after it starts the
	// members after it (startAfter). One stamped before it, even if it
	// reaches the engine later, is the idle state the prompt answered.
	prompted   bool
	promptedAt time.Time
	diff       *Diff
	diffAt     time.Time // when diff was read; zero: never
	// handoffs wait to be typed into the member, oldest first, at most
	// maxHandoffs (handoff.go). delivering is set, under e.mu, while a
	// goroutine types them (deliver), and read without it by poke; wake makes
	// that goroutine look again.
	handoffs   []handoff
	delivering atomic.Bool
	wake       chan struct{}
}

// NewEngine returns an engine that starts member sessions with l and finds
// them again with lookup (the server's registry).
func NewEngine(l Launcher, lookup func(sessionID string) (*session.Local, bool)) *Engine {
	root, cut := context.WithCancel(context.Background())
	return &Engine{launcher: l, lookup: lookup, await: awaitReady, now: time.Now,
		runs: map[string]*run{}, exec: nativeExec(), teardownRoot: root, teardownCut: cut}
}

// Launch is LaunchHeld for a caller that needs nothing more of the run once
// it has it.
func (e *Engine) Launch(ctx context.Context, c Crew) (*Run, error) {
	run, release, err := e.LaunchHeld(ctx, c)
	release()
	return run, err
}

// LaunchHeld starts a run of c, whose Cwd the caller has resolved. With
// isolation "worktree" it first checks that git is on the server's PATH
// (ErrNoGit) and that Cwd is in a git repository to make worktrees of
// (ErrNotRepo), before anything is made. It starts the sessions of the
// members that start immediately, each in a worktree of its own when the
// crew has them, and returns once they exist: each member is starting, and
// its prompt is typed once its session is ready (on the run's context, so a
// client that goes away does not stop it). A member whose prompt cannot be
// typed ends alone. When a member's session cannot be started, the ones
// started are stopped, no run is kept, and the error names the member; when
// the run is stopped meanwhile, it stays, stopped, and LaunchHeld returns
// ErrRunStopped, a stop that lands before the first member's start included.
// A crew with no members, one that runs on a host, one without a valid ID
// or, with worktrees, one whose <cwd>/.conductor or
// <cwd>/.conductor/worktrees is a symbolic link is not launched
// (ErrInvalid), and neither is a member whose directory in its worktree
// lies through a symbolic link.
//
// The run stays exempt from eviction until the caller calls release, which
// is never nil and must be called once, after an error too (a second call
// does nothing): the API mints the run's view link first, so that a launch at
// the cap cannot forget the run between its start and its link.
func (e *Engine) LaunchHeld(ctx context.Context, c Crew) (*Run, func(), error) {
	return e.LaunchNamed(ctx, c, "")
}

// LaunchNamed is LaunchHeld with the run's own name (Run.Label), checked
// as a crew name is (ValidateLabel, ErrInvalid); empty gives it none.
func (e *Engine) LaunchNamed(ctx context.Context, c Crew, label string) (*Run, func(), error) {
	noop := func() {}
	if err := ValidateLabel(label); err != nil {
		return nil, noop, err
	}
	// A managed crew's base is read once, here (prepare), and is the
	// commit its manifest is validated at, the run records and its
	// prerequisites and first attempts start from, whatever HEAD does
	// after.
	p, err := e.prepare(ctx, c, "")
	if err != nil {
		return nil, noop, err
	}
	r := e.add(c, p, label)
	release := sync.OnceFunc(func() { e.launched(r) })
	if e.afterAdd != nil {
		e.afterAdd(r.id)
	}
	run, err := e.startImmediate(ctx, r)
	return run, release, err
}

// LaunchAdopting is LaunchHeld for a crew an agent forms around its own
// session: the member named self is not started but adopted, its session
// the one given (running already, its prompt its own), so the members after
// it start on its next done and handoffs reach it; the rest start as at
// launch. The session is tagged with the run by the caller (SetCrew).
func (e *Engine) LaunchAdopting(ctx context.Context, c Crew, self, sessionID string) (*Run, func(), error) {
	noop := func() {}
	if c.member(self) == nil {
		return nil, noop, invalidf("self %s is not a member of the crew", quote(self))
	}
	if sessionID == "" {
		return nil, noop, invalidf("no session to adopt")
	}
	if _, _, ok := e.MemberOf(sessionID); ok {
		return nil, noop, invalidf("the session is a member of a run already")
	}
	p, err := e.prepare(ctx, c, "")
	if err != nil {
		return nil, noop, err
	}
	r := e.add(c, p, "")
	release := sync.OnceFunc(func() { e.launched(r) })
	if e.afterAdd != nil {
		e.afterAdd(r.id)
	}
	now := time.Now().UTC()
	e.mu.Lock()
	m := r.member(self)
	m.state.SessionID, m.state.Started, m.state.Status = sessionID, &now, MemberRunning
	m.prompted, m.promptedAt = true, now
	e.bySession.Store(sessionID, sessionMember{r: r, m: m})
	r.note(session.ActivityStatus, "%s formed the crew from its own session and joined it", self)
	r.touch()
	e.mu.Unlock()
	run, err := e.startImmediate(ctx, r)
	return run, release, err
}

// member returns the member of c named name, or nil.
func (c Crew) member(name string) *Member {
	for i := range c.Members {
		if c.Members[i].Name == name {
			return &c.Members[i]
		}
	}
	return nil
}

// prepared is what a launch learns of its crew before the run is made:
// where cwd lies in its repository (prefix), and, for a managed crew, the
// commit the run starts from and its project manifest there.
type prepared struct {
	prefix string
	base   string
	man    resolvedManifest
}

// prepare checks c as LaunchHeld describes and, with worktrees, finds where
// its cwd lies in its repository (the prefix). For a managed crew it pins
// the base: base when given (a continuation's own),
// else HEAD of cwd, read once here; the file form's manifest is read and
// held to its rules at that commit, so a broken file refuses the launch
// before a run exists, with the file and the commit named.
func (e *Engine) prepare(ctx context.Context, c Crew, base string) (prepared, error) {
	if err := c.validateWithID(); err != nil {
		return prepared{}, err
	}
	if c.Managed() {
		if e.ledger == nil {
			return prepared{}, ErrNoLedger
		}
		if err := e.ledger.Available(); err != nil {
			return prepared{}, fmt.Errorf("%w: %v", ErrNoLedger, err)
		}
	}
	if err := c.Launchable(); err != nil {
		return prepared{}, err
	}
	if c.Isolation != IsolationWorktree {
		return prepared{}, nil
	}
	if err := checkGit(); err != nil {
		return prepared{}, err
	}
	if !filepath.IsAbs(c.Cwd) {
		return prepared{}, invalidf("with worktrees, cwd must be an absolute path")
	}
	if err := checkWorktreesDir(c.Cwd); err != nil {
		return prepared{}, err
	}
	if err := CheckRepo(ctx, c.Cwd); err != nil {
		return prepared{}, err
	}
	prefix, err := repoPrefix(ctx, c.Cwd)
	if err != nil {
		return prepared{}, err
	}
	p := prepared{prefix: prefix}
	if !c.Managed() {
		return p, nil
	}
	if base == "" {
		head, err := headCommit(ctx, c.Cwd)
		if err != nil {
			return prepared{}, fmt.Errorf("reading the repository's commit: %w", err)
		}
		base = head
	}
	if !fullCommit(base) {
		return prepared{}, fmt.Errorf("the repository's commit %q is not a commit id", base)
	}
	p.base = base
	m := c.effectiveManifest()
	if !m.File {
		p.man = resolvedManifest{m: m, source: sourceCrew, revision: c.ManifestRevision}
		return p, nil
	}
	rec, blob, err := resolveManifest(ctx, c.Cwd, base, prefix, c.Tasks)
	if err != nil {
		return prepared{}, err
	}
	p.man = resolvedManifest{m: rec, source: sourceFile, digest: blob}
	return p, nil
}

// ResumeRun starts a new run of a stopped (or finished) run's crew in which
// every member whose last agent session is resumable continues its
// conversation in its kept worktree and branch, without a prompt, and the
// others start afresh under their start rules; the new run names the old
// in ResumedFrom and the old names it in ResumedBy. A run still going, or
// whose stop was cut short, is refused (ErrRunRunning, ErrRunStopped). A
// member whose session cannot be made ends with the reason, as at launch.
func (e *Engine) ResumeRun(ctx context.Context, runID string) (*Run, error) {
	cur, ok := e.Get(runID)
	if !ok {
		return nil, ErrRunNotFound
	}
	e.mu.Lock()
	old := e.runs[runID]
	if old == nil {
		e.mu.Unlock()
		return nil, ErrRunNotFound
	}
	if old.managed {
		e.mu.Unlock()
		return nil, ErrManaged
	}
	switch {
	case old.stopping && !old.stopDone:
		e.mu.Unlock()
		return nil, ErrRunStopped
	case cur.State != RunStopped && cur.State != RunFinished:
		e.mu.Unlock()
		return nil, ErrRunRunning
	case old.resumedBy != "":
		e.mu.Unlock()
		return nil, &RunResumedError{By: old.resumedBy}
	case old.resuming:
		e.mu.Unlock()
		return nil, &RunResumedError{}
	}
	old.resuming = true
	yolo := old.yolo
	c := Crew{ID: old.crewID, Name: old.name, Goal: old.goal, Cwd: old.cwd, Where: WhereServer, Isolation: old.isolation, Yolo: &yolo}
	keeps := map[string]kept{}
	for _, m := range old.members {
		c.Members = append(c.Members, m.def)
	}
	for _, st := range cur.Members {
		if st.AgentSession != nil && st.AgentSession.Resumable && st.AgentSession.ID != "" {
			k := kept{agentSession: st.AgentSession.ID, sessionID: st.SessionID, worktree: st.Worktree, branch: st.Branch}
			if m := old.member(st.Name); m != nil {
				k.base = m.base
			}
			keeps[st.Name] = k
		}
	}
	e.mu.Unlock()
	return e.resumeInto(ctx, c, keeps, old.label, old.id, old)
}

// kept is what a resumed member continues with: its agent conversation, the
// session that held it, and its worktree, branch and base commit.
type kept struct{ agentSession, sessionID, worktree, branch, base string }

// ResumeRecord is ResumeRun for a run the engine no longer holds, from its
// record (Records): the crew as the record has it (its members' prompts,
// arguments and start rules as they ran, not the crew as saved since), every
// member whose last agent session is resumable continuing its conversation
// in its kept worktree and branch as recorded (one gone fails that member's
// start, as a session that cannot be made does), and the others starting
// afresh under their start rules. The caller refuses a record resumed
// already (ResumedBy) and marks the record with the new run; a run the
// engine holds is resumed as it is (ResumeRun), never from its record.
func (e *Engine) ResumeRecord(ctx context.Context, rec Run) (*Run, error) {
	if rec.managed() {
		return nil, ErrManaged
	}
	if _, ok := e.Get(rec.ID); ok {
		return nil, ErrRunRunning
	}
	yolo := rec.Yolo
	c := Crew{ID: rec.CrewID, Name: rec.Name, Goal: rec.Goal, Cwd: rec.Cwd, Where: WhereServer, Isolation: rec.Isolation, Yolo: &yolo}
	keeps := map[string]kept{}
	for _, st := range rec.Members {
		c.Members = append(c.Members, Member{Name: st.Name, AgentID: st.AgentID, Prompt: st.Prompt, Args: slices.Clone(st.Args), Start: st.Start})
		if st.AgentSession != nil && st.AgentSession.Resumable && st.AgentSession.ID != "" {
			keeps[st.Name] = kept{agentSession: st.AgentSession.ID, sessionID: st.SessionID, worktree: st.Worktree, branch: st.Branch, base: st.Base}
		}
	}
	return e.resumeInto(ctx, c, keeps, rec.Label, rec.ID, nil)
}

// resumeInto makes the run that resumes fromID: a run of c, labelled, in
// which the members in keeps continue their conversations and the others
// start under their start rules. With old, the run the engine holds that is
// resumed, its bookkeeping (resumedBy, resuming) and its note.
func (e *Engine) resumeInto(ctx context.Context, c Crew, keeps map[string]kept, label, fromID string, old *run) (*Run, error) {
	p, err := e.prepare(ctx, c, "")
	if err != nil {
		if old != nil {
			e.mu.Lock()
			old.resuming = false
			e.mu.Unlock()
		}
		return nil, err
	}
	r := e.add(c, p, label)
	defer e.launched(r)
	e.mu.Lock()
	r.resumedFrom = fromID
	if old != nil {
		old.resumedBy = r.id
		old.resuming = false
		old.note(session.ActivityStatus, "resumed as %s", r.id)
	}
	r.note(session.ActivityStatus, "resumes %s: %d conversations continue", fromID, len(keeps))
	var resumes []*member
	for _, m := range r.members {
		k, ok := keeps[m.def.Name]
		if !ok {
			continue
		}
		if r.isolation == IsolationWorktree && k.worktree != "" {
			m.state.Branch, m.state.Worktree, m.base = k.branch, k.worktree, k.base
		}
		m.state.Status = MemberStarting
		r.starts.Add(1)
		resumes = append(resumes, m)
	}
	r.touch()
	e.mu.Unlock()
	if e.afterAdd != nil {
		e.afterAdd(r.id)
	}
	for _, m := range resumes {
		k := keeps[m.def.Name]
		name := m.def.Name
		cwd := r.cwd
		if r.isolation == IsolationWorktree && k.worktree != "" {
			cwd = filepath.Join(k.worktree, r.prefix)
		}
		local, err := e.launcher.Launch(ctx, LaunchSpec{AgentID: m.def.AgentID, Name: name, Cwd: cwd, Args: slices.Clone(m.def.Args),
			Env: map[string]string{"GOAL": r.goal}, Ref: session.CrewRef{RunID: r.id, CrewID: r.crewID, Member: name}, Yolo: r.yolo,
			Resume: k.agentSession, ResumedFrom: k.sessionID})
		if err != nil {
			r.starts.Done()
			e.fail(r, m, err)
			continue
		}
		id := local.Info().ID
		started := time.Now().UTC()
		e.mu.Lock()
		// Starting until its session is ready for a prompt (settle): its
		// conversation has its prompt, so a done from now on starts the
		// members after it.
		m.state.SessionID, m.state.Started, m.state.Status = id, &started, MemberStarting
		m.prompted, m.promptedAt = true, started
		e.bySession.Store(id, sessionMember{r: r, m: m})
		r.note(session.ActivityStatus, "%s resumed its conversation", name)
		e.mu.Unlock()
		go e.settle(r, m, local)
	}
	run, err := e.startImmediate(ctx, r)
	if err != nil {
		return nil, err
	}
	return run, nil
}

// startImmediate starts the members of r that start immediately and returns
// r as it then is. r is launching (held) until its launcher releases it, so
// no other launch forgets it meanwhile. (Engine.start, which starts one
// member, is another function and stays as it is.)
func (e *Engine) startImmediate(ctx context.Context, r *run) (*Run, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(r.ctx, cancel)()

	e.mu.Lock()
	if r.stopping {
		// Stopped before any start was reserved: the run stays, stopped.
		e.mu.Unlock()
		return nil, ErrRunStopped
	}
	var starts []*member
	for _, m := range r.members {
		if m.def.Start.When == StartImmediately && r.reserve(m) {
			starts = append(starts, m)
		}
	}
	e.mu.Unlock()
	if r.managed {
		// No member starts by a rule: the tasks are dispatched (task.go).
		if err := e.startManaged(ctx, r); err != nil {
			e.abort(r)
			return nil, err
		}
	}
	for i, m := range starts {
		local, err := e.launch(ctx, r, m)
		if err == nil {
			go e.finish(r, m, local) // takes over the start
			continue
		}
		if errors.Is(err, ErrRunStopped) || r.ctx.Err() != nil {
			// Stopped meanwhile: the run stays, stopped, and so does what
			// had started. The members not begun were never started.
			e.fail(r, m, ErrRunStopped)
			e.mu.Lock()
			for _, rest := range starts[i+1:] {
				rest.state.Status = MemberPending
			}
			e.mu.Unlock()
			for range starts[i:] {
				r.starts.Done()
			}
			return nil, ErrRunStopped
		}
		for range starts[i:] {
			r.starts.Done()
		}
		e.abort(r)
		return nil, fmt.Errorf("member %s: %w", quote(m.def.Name), err)
	}
	// The check stays, should something else ever forget a run.
	out, ok := e.Get(r.id)
	if !ok {
		return nil, ErrRunNotFound
	}
	return &out, nil
}

// add makes and keeps a run of c with every member pending, launching until
// the caller calls launched.
func (e *Engine) add(c Crew, p prepared, label string) *run {
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{crewID: c.ID, name: c.Name, label: label, goal: c.Goal, cwd: c.Cwd, isolation: c.Isolation, prefix: p.prefix,
		yolo: c.Yolo != nil && *c.Yolo, changed: e.OnRunChange, startedAt: time.Now().UTC(), ctx: ctx, cancel: cancel, launching: true, managed: c.Managed()}
	if r.managed {
		// The crew's project manifest as launched, a legacy setup lifted
		// into it, and the manifest at the pinned base, whose prerequisites
		// the run runs (steps.go).
		r.plan, r.base = slices.Clone(c.Tasks), p.base
		r.tasks = newTasks(c.Tasks)
		r.crewJSON = crewAsLaunched(c)
		r.manifest, r.manifestRev, r.baseMan = c.effectiveManifest(), c.ManifestRevision, p.man
		if r.hasManifest() {
			r.baseManRow = p.man.row()
		}
		r.prereq, r.prereqK, r.prereqRows = prereqPassed, map[string]int{}, map[string]prereqReceipt{}
		root := e.teardownRoot
		if root == nil {
			root = context.Background()
		}
		r.teardown, r.teardownCancel = context.WithCancel(root)
	}
	r.chat = session.NewChatRoom(func(m session.ChatMessage) {
		if e.OnRunChat != nil {
			e.OnRunChat(r.id, m)
		}
	}, func(name string) bool { return e.memberRunning(r, name) })
	immediate := 0
	for _, m := range c.Members {
		r.members = append(r.members, newMember(m))
		if m.Start.When == StartImmediately {
			immediate++
		}
	}
	// Under endMu as well: a run evict forgets has its record written
	// (journalForgotten) in its turn among the saves, before the run is
	// gone for a resume from that record to find.
	e.endMu.Lock()
	defer e.endMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	for r.id == "" || e.runs[r.id] != nil {
		r.id = c.ID + "-" + session.NewID()[:8]
	}
	e.evict()
	e.runs[r.id] = r
	e.held.Store(r.id, struct{}{})
	e.order = append(e.order, r)
	if label != "" {
		r.note(session.ActivityStatus, "launched %s as %q: %d members, %d starting now", c.Name, label, len(c.Members), immediate)
	} else {
		r.note(session.ActivityStatus, "launched %s: %d members, %d starting now", c.Name, len(c.Members), immediate)
	}
	return r
}

// memberRunning reports whether r's member name runs: its prompt typed and
// its session not ended, as the engine knows it.
func (e *Engine) memberRunning(r *run, name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := r.member(name)
	return m != nil && m.state.Status == MemberRunning
}

// ChatRoom is the chat of the run with the given ID, which a member's
// session is made with (Options.RunChat) and joins; nil for no such run.
func (e *Engine) ChatRoom(runID string) *session.ChatRoom {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.runs[runID]; r != nil {
		return r.chat
	}
	return nil
}

// launched marks r launched: from now on evict may forget it.
func (e *Engine) launched(r *run) {
	e.mu.Lock()
	r.launching = false
	e.mu.Unlock()
}

func newMember(m Member) *member {
	m.Args = slices.Clone(m.Args)
	return &member{def: m, state: MemberState{Name: m.Name, AgentID: m.AgentID, Start: m.Start, Status: MemberPending},
		wake: make(chan struct{}, 1)}
}

// member returns the member of r with the given name. The caller holds e.mu.
func (r *run) member(name string) *member {
	for _, m := range r.members {
		if m.def.Name == name {
			return m
		}
	}
	return nil
}

// reserve marks m starting when it is pending and r takes starts, and counts
// the start in r.starts, which the caller ends with r.starts.Done. The caller
// holds e.mu, so no start is counted once a stop waits for them.
func (r *run) reserve(m *member) bool {
	if r.stopping || m.state.Status != MemberPending {
		return false
	}
	m.state.Status = MemberStarting
	r.starts.Add(1)
	r.touch()
	return true
}

// touch tells OnRunChange that r changed. The caller holds e.mu.
func (r *run) touch() {
	if r.changed != nil {
		r.changed(r.id)
	}
}

// note appends an entry to r's log, cleaned like a session's entries, and
// drops the oldest past maxRunLog. The caller holds e.mu.
func (r *run) note(typ, format string, args ...any) {
	r.record(session.ActivityEntry{Type: typ, Message: fmt.Sprintf(format, args...)})
}

// noteHandoff notes a handoff delivered from one member to another, with
// the two as fields (byName, to) beside the words, so a client draws it
// without reading the text. The caller holds e.mu.
func (r *run) noteHandoff(from, to string) {
	r.record(session.ActivityEntry{Type: session.ActivityStatus, Message: fmt.Sprintf("handoff delivered from %s to %s", from, to), ByName: from, To: to})
}

// record is note with the entry given: its time is now. The caller holds e.mu.
func (r *run) record(entry session.ActivityEntry) {
	entry.At = time.Now().UTC()
	entry = session.CleanEntry(entry)
	if len(r.log) >= maxRunLog {
		r.log = slices.Delete(r.log, 0, len(r.log)-maxRunLog+1)
	}
	r.log = append(r.log, entry)
	r.touch()
}

// launch makes m's worktree, when r has them, and starts m's session, in
// the worktree's directory that r's cwd is of its repository, which it makes
// when no commit has a file there.
func (e *Engine) launch(ctx context.Context, r *run, m *member) (*session.Local, error) {
	name := m.def.Name
	cwd := r.cwd
	if r.isolation == IsolationWorktree {
		path := filepath.Join(r.cwd, worktreesDir, r.id, name)
		branch := "crew/" + r.id + "/" + name
		if err := checkWorktreesDir(r.cwd); err != nil {
			return nil, err
		}
		e.excludeMu.Lock()
		err := excludeWorktrees(ctx, r.cwd)
		e.excludeMu.Unlock()
		if err != nil {
			// The worktrees work without it; git status shows them. The run
			// log says so once.
			e.mu.Lock()
			if !r.excludeNoted {
				r.excludeNoted = true
				r.note(session.ActivityError, "could not add %s to the repository's info/exclude: %v", excludeLine, err)
			}
			e.mu.Unlock()
		}
		if err := AddWorktree(ctx, r.cwd, path, branch); err != nil {
			return nil, err
		}
		base, err := headCommit(ctx, path)
		e.mu.Lock()
		m.state.Branch, m.state.Worktree = branch, path
		if err == nil {
			m.base = base
		}
		e.mu.Unlock()
		cwd = filepath.Join(path, r.prefix)
		// A directory no commit has a file in is not in the worktree; one
		// that a commit makes a symbolic link would be made where it points.
		if err := checkMemberDir(path, r.prefix); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			return nil, err
		}
	}
	local, err := e.launcher.Launch(ctx, LaunchSpec{AgentID: m.def.AgentID, Name: name, Cwd: cwd, Args: slices.Clone(m.def.Args),
		Env: map[string]string{"GOAL": r.goal}, Ref: session.CrewRef{RunID: r.id, CrewID: r.crewID, Member: name}, Yolo: r.yolo})
	if err != nil {
		return nil, err
	}
	id := local.Info().ID
	started := time.Now().UTC()
	e.mu.Lock()
	m.state.SessionID, m.state.Started = id, &started
	e.bySession.Store(id, sessionMember{r: r, m: m})
	r.note(session.ActivityStatus, "%s started", name)
	stopping := r.stopping
	e.mu.Unlock()
	if stopping {
		// The run began to stop while the session started: the stop may
		// have looked for the member's session before there was one.
		stopLocal(local)
		return nil, ErrRunStopped
	}
	return local, nil
}

// prompt waits for m's session to be ready and submits m's prompt, the goal
// in it, as one line (typedPrompt), and m is running; a member without a
// prompt runs at once. A trust question on the member's screen holds the
// prompt until a person answers it (awaitReady), noted once in the run log
// each time; so does a question the session finds only as the prompt is
// submitted (Submit refuses it, nothing typed), and the wait starts over.
// m is prompted as the Enter is written, so a done the agent
// reported before it never starts the members after m. A session that ends
// first gets no prompt: m ends, with how its process ended. An error means
// the start failed: ctx ended or the prompt could not be written.
func (e *Engine) prompt(ctx context.Context, r *run, m *member, local *session.Local) error {
	return e.promptWith(ctx, r, m, local, typedPrompt(m.def.Prompt, r.goal))
}

// promptWith is prompt with the line to type given: a member's role prompt,
// or an attempt's task line (taskLine).
func (e *Engine) promptWith(ctx context.Context, r *run, m *member, local *session.Local, text string) error {
	name := m.def.Name
	hasPrompt := strings.TrimSpace(text) != ""
	// pending says its prompt was accepted while the program took it
	// slowly (session.ErrSubmitPending): marked prompted by its Enter.
	pending := false
	if hasPrompt {
		var res session.SubmitResult
		for {
			err := e.await(ctx, local, func(question string) {
				e.mu.Lock()
				r.note(session.ActivityStatus, "%s asks %s: answer it in %s's terminal; its prompt waits", name, quote(question), name)
				e.mu.Unlock()
			})
			switch {
			case errors.Is(err, errNotReady):
				e.mu.Lock()
				r.note(session.ActivityStatus, "%s was not ready after %v: typing its prompt anyway", name, readyCap)
				e.mu.Unlock()
			case errors.Is(err, session.ErrSessionEnded):
				e.endedEarly(r, m, local, "before its prompt was typed")
				return nil
			case err != nil:
				return err
			}
			res, err = local.Submit(ctx, session.Submission{Text: text, ByName: typedBy, Confirm: true, BeforeEnter: func() {
				e.mu.Lock()
				m.markPrompted()
				e.mu.Unlock()
				// The handoffs waiting for its prompt may go (a pending
				// prompt's Enter comes after this function has returned).
				m.poke()
			}, Finished: func(res session.SubmitResult, err error) {
				// A pending prompt ended without its Enter (a question
				// came first): it waits in the agent's input, as one
				// typed so at once does, and counts as given.
				if res.Entered || !res.Typed {
					return
				}
				e.mu.Lock()
				m.markPrompted()
				r.note(session.ActivityError, "typed %s's prompt without its Enter: %s waits on a question; answer it, then press Enter in its terminal", name, name)
				e.mu.Unlock()
				m.poke()
			}})
			if errors.Is(err, session.ErrTrustQuestion) || errors.Is(err, session.ErrQuestion) {
				// A question the wait had not seen yet (a dialog Submit
				// found on the screen) holds the prompt: nothing was
				// typed, and it waits for the answer. The wait may find
				// the session ready (a report of its own on show while the
				// dialog is up), so it looks again a poll later.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-local.Ended():
					e.endedEarly(r, m, local, "before its prompt was typed")
					return nil
				case <-time.After(readyPoll):
				}
				continue
			}
			pending = errors.Is(err, session.ErrSubmitPending)
			if err == nil || pending {
				break
			}
			if errors.Is(err, session.ErrSessionEnded) || endsSoon(local) {
				e.endedEarly(r, m, local, "before its prompt was typed")
				return nil
			}
			return fmt.Errorf("typing its prompt: %w", err)
		}
		e.mu.Lock()
		switch {
		case pending:
			// Accepted: entered as the program reads, and marked
			// prompted then (BeforeEnter), never before: a done before
			// its Enter starts nothing.
			r.note(session.ActivityStatus, "%s's prompt waits for %s to take its input: it is entered once it has", name, name)
		case !res.Entered:
			// A question came up during the pause: the prompt waits in the
			// agent's input, and is never typed again.
			m.markPrompted()
			r.note(session.ActivityError, "typed %s's prompt without its Enter: %s waits on a question; answer it, then press Enter in its terminal", name, name)
		case res.Reentered:
			r.note(session.ActivityStatus, "pressed Enter again for %s: it did not report taking its prompt within %v", name, session.ConfirmWait)
		}
		e.mu.Unlock()
	}
	e.mu.Lock()
	if !pending {
		m.markPrompted()
	}
	m.state.Status = MemberRunning
	switch {
	case pending:
		r.touch()
	case hasPrompt:
		r.note(session.ActivityStatus, "typed %s's prompt", name)
	default:
		r.touch()
	}
	e.mu.Unlock()
	m.poke() // the handoffs waiting for its prompt may go
	return nil
}

// markPrompted marks m prompted, now, unless it is already. The caller holds
// e.mu.
func (m *member) markPrompted() {
	if !m.prompted {
		m.prompted, m.promptedAt = true, time.Now().UTC()
	}
}

// endsSoon reports whether l ends within a second: a write that failed as
// its process exited, before the session saw it end.
func endsSoon(l *session.Local) bool {
	select {
	case <-l.Ended():
		return true
	case <-time.After(time.Second):
		return false
	}
}

// endedEarly ends m, whose process ended before its start was done: before
// its prompt was typed, or, resumed, before it was ready again (why).
func (e *Engine) endedEarly(r *run, m *member, local *session.Local, why string) {
	info := local.Info()
	how := string(info.Status)
	if info.ExitCode != nil {
		how = fmt.Sprintf("%s (exit %d)", info.Status, *info.ExitCode)
	}
	ended := info.EndedAt
	if ended == nil {
		t := time.Now().UTC()
		ended = &t
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	m.state.Status, m.state.Ended = MemberEnded, ended
	m.state.Err = how + " " + why
	r.note(session.ActivityStatus, "%s ended %s: %s", m.def.Name, why, how)
}

// settle makes a resumed member, starting since its session was made,
// running once that session is ready for a prompt (await: a trust question
// holding it is noted, as at a start), or after readyCap, noted: its agent
// takes a while to show its prompt, and a handoff typed into it meanwhile
// (deliver holds one for a member not running) would be lost in its
// startup. A session that ends first ends the member, as at a start; a run
// stopped meanwhile leaves it to the stop. It ends the start and wakes the
// member's deliveries.
func (e *Engine) settle(r *run, m *member, local *session.Local) {
	defer r.starts.Done()
	name := m.def.Name
	err := e.await(r.ctx, local, func(question string) {
		e.mu.Lock()
		r.note(session.ActivityStatus, "%s asks %s: answer it in %s's terminal; its handoffs wait", name, quote(question), name)
		e.mu.Unlock()
	})
	switch {
	case errors.Is(err, session.ErrSessionEnded):
		e.endedEarly(r, m, local, "before it was ready again")
		return
	case err != nil && !errors.Is(err, errNotReady):
		// The run stopped while it waited: the stop ends its session, and
		// the member ends here, so that nothing of a completed stop is
		// left starting (which a resume refuses and eviction waits for).
		now := time.Now().UTC()
		e.mu.Lock()
		if m.state.Status == MemberStarting && m.state.SessionID == local.Info().ID {
			m.state.Status, m.state.Ended = MemberEnded, &now
			r.touch()
		}
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if m.state.Status != MemberStarting || m.state.SessionID != local.Info().ID {
		return
	}
	m.state.Status = MemberRunning
	if errors.Is(err, errNotReady) {
		r.note(session.ActivityStatus, "%s was not ready after %v: taken as running anyway", name, readyCap)
	}
	r.touch()
	m.poke()
}

// start runs m's start, which the caller has reserved: its worktree and
// session, and returns once the session exists; its prompt is typed on the
// run's context once the session is ready (finish). When the session cannot
// be started, m ends with the reason.
func (e *Engine) start(ctx context.Context, r *run, m *member) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(r.ctx, cancel)()
	local, err := e.launch(ctx, r, m)
	if err == nil {
		go e.finish(r, m, local) // takes over the start
		return nil
	}
	r.starts.Done()
	if r.ctx.Err() != nil {
		err = ErrRunStopped
	}
	e.fail(r, m, err)
	return fmt.Errorf("member %s: %w", quote(m.def.Name), err)
}

// finish types m's prompt once its session is ready, on the run's context,
// and ends m's start. When that fails m ends alone, with the reason, and then
// its session is stopped: a session seen ended has its member's reason.
func (e *Engine) finish(r *run, m *member, local *session.Local) {
	defer r.starts.Done()
	if err := e.prompt(r.ctx, r, m, local); err != nil {
		if r.ctx.Err() != nil {
			err = ErrRunStopped
		}
		e.fail(r, m, err)
		stopLocal(local)
	}
}

// fail ends m, which could not start.
func (e *Engine) fail(r *run, m *member, err error) {
	now := time.Now().UTC()
	e.mu.Lock()
	defer e.mu.Unlock()
	m.state.Status, m.state.Ended = MemberEnded, &now
	m.state.Err = session.CleanMessage(err.Error())
	r.note(session.ActivityError, "%s could not start: %v", m.def.Name, err)
}

// stopLocal stops a session a failed start leaves behind.
func stopLocal(l *session.Local) {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	_ = l.Stop(ctx)
}

// OnActivity takes the activity of every session, as the server's activity
// fan-out hands it with, for an attention entry, the state it records. The
// entry of a session that is no member of a run costs no lock. An entry of a
// member's session wakes the handoffs waiting for that member. The first done
// a running member reports starts the pending members that start after it; a
// done whose entry is stamped no later than its prompt was typed does not
// count, however late it arrives: the session stamps an attention entry as
// the state changes, so that done is the idle state the prompt answered. A handoff a member reports
// goes to the member it names (handoff). It never waits: a start, and the
// typing of a handoff, run on goroutines of their own.
func (e *Engine) OnActivity(sessionID string, entry session.ActivityEntry, state session.AttentionState) {
	v, ok := e.bySession.Load(sessionID)
	if !ok {
		return
	}
	sm := v.(sessionMember)
	r, m := sm.r, sm.m
	m.poke()
	isHandoff := entry.Type == session.ActivityHandoff
	if !isHandoff && entry.Type == session.ActivityAttention && (state == session.AttentionWorking || state == session.AttentionDone) && sm.a != nil {
		// A blocker the attempt reported is cleared once it works on, or
		// says it is done (checkpoints.go).
		go e.unblock(r, sm.a, state)
	}
	if !isHandoff && (entry.Type != session.ActivityAttention || state != session.AttentionDone) {
		return
	}
	e.mu.Lock()
	if e.runs[r.id] != r { // forgotten meanwhile
		e.mu.Unlock()
		return
	}
	if r.managed {
		// A done starts no one and finishes nothing; a handoff is noted,
		// never typed: a managed run passes work by results (task.go).
		e.managedActivity(r, sm, entry, isHandoff)
		e.mu.Unlock()
		return
	}
	var next []*member
	if isHandoff {
		e.handoff(r, m, entry)
	} else {
		next = r.startAfter(m, entry.At)
	}
	e.mu.Unlock()
	for _, m := range next {
		// The outcome is in m's state and the run log.
		go func() { _ = e.start(r.ctx, r, m) }()
	}
}

// startAfter reserves the start of the pending members that start after
// done, which reports done in an entry stamped at, when done has had its
// prompt before at and has not ended, and returns them. The caller holds e.mu
// and starts them.
func (r *run) startAfter(done *member, at time.Time) []*member {
	if !done.prompted || !at.After(done.promptedAt) || done.state.Status == MemberEnded {
		return nil
	}
	var next []*member
	var names []string
	for _, m := range r.members {
		if m.def.Start.When == StartAfter && m.def.Start.Member == done.def.Name && r.reserve(m) {
			next = append(next, m)
			names = append(names, m.def.Name)
		}
	}
	if len(next) > 0 {
		r.note(session.ActivityStatus, "%s is done: starting %s", done.def.Name, strings.Join(names, ", "))
	}
	return next
}

// StartMember starts a pending member of a run by hand, whatever its start
// condition, and returns once its session exists or could not be started;
// its prompt is typed once the session is ready.
func (e *Engine) StartMember(ctx context.Context, runID, name string) error {
	e.mu.Lock()
	r, ok := e.runs[runID]
	if !ok {
		e.mu.Unlock()
		return ErrRunNotFound
	}
	if r.managed {
		e.mu.Unlock()
		return ErrManaged
	}
	m := r.member(name)
	var err error
	switch {
	case m == nil:
		err = ErrMemberNotFound
	case r.stopping:
		err = ErrRunStopped
	case !r.reserve(m):
		err = ErrMemberStarted
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	return e.start(ctx, r, m)
}

// AddMember adds m to a run, held to the rules of a crew: a valid member, a
// name no member has, a prompt that fits a session with the run's goal in it,
// an after condition naming a member of the run, at most 12 members
// (ErrInvalid). A member that starts immediately starts, and
// AddMember returns once its session exists or could not be started (the
// member stays in the run, ended, its name taken); one that starts after
// another waits for that one's next done.
func (e *Engine) AddMember(ctx context.Context, runID string, m Member) error {
	if err := m.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	r, ok := e.runs[runID]
	if !ok {
		e.mu.Unlock()
		return ErrRunNotFound
	}
	if r.managed {
		e.mu.Unlock()
		return ErrManaged
	}
	if err := r.admit(m); err != nil {
		e.mu.Unlock()
		return err
	}
	nm := newMember(m)
	r.members = append(r.members, nm)
	r.note(session.ActivityStatus, "%s joined the run", m.Name)
	start := m.Start.When == StartImmediately && r.reserve(nm)
	e.mu.Unlock()
	if !start {
		return nil
	}
	return e.start(ctx, r, nm)
}

// admit checks that m can join r. The caller holds e.mu.
func (r *run) admit(m Member) error {
	switch {
	case r.stopping:
		return ErrRunStopped
	case len(r.members) >= maxMembers:
		return invalidf("too many members (at most %d)", maxMembers)
	case r.member(m.Name) != nil:
		return invalidf("member %s: the name is used twice", quote(m.Name))
	}
	if err := m.checkTypedPrompt(r.goal); err != nil {
		return err
	}
	if m.Start.When == StartTask {
		// A member started by its tasks belongs to a managed run, which
		// takes no member by hand (ErrManaged, before admit).
		return invalidf("member %s: start.when %q is only for a crew with tasks", quote(m.Name), StartTask)
	}
	switch {
	case m.Start.When != StartAfter:
		return nil
	case m.Start.Member == m.Name:
		return invalidf("member %s: cannot start after itself", quote(m.Name))
	case r.member(m.Start.Member) == nil:
		return invalidf("member %s: starts after %s, which is not a member of this run", quote(m.Name), quote(m.Start.Member))
	}
	return nil
}

// Stop stops a run: no member starts any more, the starts in flight end,
// every member's session is stopped and, beside them in the same wait,
// every service of a managed run's live trees (SIGTERM to its group,
// SIGKILL after the grace or once ctx ends), one being started among them
// once the executor hands it over, the handoffs waiting are
// dropped and the run is marked stopped. The worktrees stay. Stopping a
// stopped run again changes nothing.
func (e *Engine) Stop(ctx context.Context, runID string) error {
	e.mu.Lock()
	r, ok := e.runs[runID]
	e.mu.Unlock()
	if !ok {
		return ErrRunNotFound
	}
	return e.stop(ctx, r)
}

func (e *Engine) stop(ctx context.Context, r *run) error {
	e.mu.Lock()
	r.stopping = true
	e.mu.Unlock()
	r.cancel()
	waitFor(ctx, &r.starts)
	e.mu.Lock()
	var ids []string
	for _, m := range r.members {
		if m.state.SessionID != "" {
			ids = append(ids, m.state.SessionID)
		}
	}
	// The services of the live trees end with the sessions, before the
	// stop is recorded: the trees' ends follow its commit, and a stop the
	// ledger refuses hands no tree over (steps.go: stopServices).
	svcs := r.stopServices()
	e.mu.Unlock()
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		l, ok := e.lookup(id)
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = l.Stop(ctx)
		}()
	}
	for _, ts := range svcs {
		wg.Go(func() { e.stopService(ctx, ts) })
	}
	wg.Wait()
	// The handoffs left are dropped; one being typed ends with its session.
	waitFor(ctx, &r.deliveries)
	// A managed run's checks, and its prerequisites (r.starts), end with
	// the context the stop cancelled.
	waitFor(ctx, &r.checks)
	e.mu.Lock()
	if r.stoppedAt == nil {
		now := time.Now().UTC()
		r.stoppedAt = &now
		r.note(session.ActivityStatus, "stopped")
		if r.managed {
			// Told once recordStop commits it (noteEnd waits for that).
			r.endPending = true
		}
	}
	// A stop cut short by its context may have left a start or a delivery
	// going: such a run is not reopened.
	r.stopDone = ctx.Err() == nil
	e.mu.Unlock()
	if r.managed {
		// The stop's end is reported once its record is committed; one
		// the ledger refused is the stop's error, the run stopped all the
		// same. The trees of the attempts it closed end after it, which
		// the stop waits for as long as its context lasts.
		if err := e.recordStop(r); err != nil {
			errs = append(errs, fmt.Errorf("the ledger could not record the stop: %w", err))
		}
		e.waitTrees(ctx, r)
		return errors.Join(errs...)
	}
	e.noteEnd(r.id)
	return errors.Join(errs...)
}

// waitFor waits for wg, the starts or the deliveries in flight, or for ctx.
func waitFor(ctx context.Context, wg *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// abort stops r, which failed to launch, and forgets it. Its worktrees stay.
func (e *Engine) abort(r *run) {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	_ = e.stop(ctx, r)
	e.endMu.Lock()
	e.mu.Lock()
	e.journalForgotten(r)
	e.forget(r)
	e.mu.Unlock()
	e.endMu.Unlock()
}

// evict forgets the oldest runs with nothing running until there is room for
// one more. A run with a member starting or a live session stays, and so does
// one being launched, so the engine may hold more than maxRuns while they run.
// The caller holds e.mu.
func (e *Engine) evict() {
	for len(e.order) >= maxRuns {
		// A run being resumed stays until its continuation names it
		// (resumedBy), or it would be forgotten with its record unmarked.
		i := slices.IndexFunc(e.order, func(r *run) bool { return !r.launching && !r.resuming && e.idle(r) })
		if i < 0 {
			return
		}
		e.journalForgotten(e.order[i])
		e.forget(e.order[i])
	}
}

// idle reports whether nothing runs in r. The caller holds e.mu; it reads the
// sessions, which never call into the engine while they hold their locks.
func (e *Engine) idle(r *run) bool {
	if r.managed && (r.stoppedAt == nil && r.outcome == "" || r.endPending || r.dirty() || r.prereqs > 0 || r.trees.n > 0) {
		// A managed run goes on between sessions: an attempt in its
		// setup, a result being verified. Nothing runs in it once it is
		// stopped or every task is terminal, and that is committed: a
		// change the ledger has not taken yet is kept for the commit
		// that carries it. A generation of its prerequisites in flight,
		// or a tree being ended, holds it too: forgotten, their last
		// commits would race the ledger with nobody to wait for them.
		return false
	}
	for _, m := range r.members {
		if m.state.Status == MemberStarting {
			return false
		}
		if m.state.SessionID == "" {
			continue
		}
		if l, ok := e.lookup(m.state.SessionID); ok && !l.Info().Status.Ended() {
			return false
		}
	}
	return true
}

// ForgetForTest drops a run as evict would, so a test sees the engine
// without it (its record stays).
func (e *Engine) ForgetForTest(runID string) {
	e.endMu.Lock()
	defer e.endMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.runs[runID]; ok {
		e.journalForgotten(r)
		e.forget(r)
	}
}

// journalForgotten writes r's record once more as it is forgotten (evict,
// abort, ForgetForTest), through the records RecordEnds wired: a resume's
// mark on it (resumedBy) made since the journal last wrote it is not lost
// with the run. The snapshot, with the chat (the record is the whole of
// it), takes its place among the saves here, before the run is gone: a
// resume from the record waits for it (WaitRecorded) before marking the
// record, so the mark is never written over by it. The caller holds endMu
// and e.mu, as the forget sites do.
func (e *Engine) journalForgotten(r *run) {
	if e.journal == nil {
		return
	}
	snap := r.snapshot(false, true)
	e.refresh(&snap)
	e.journal(snap)
}

// forget drops r and the index of its sessions, and tells OnForget. The
// caller holds e.mu.
func (e *Engine) forget(r *run) {
	delete(e.runs, r.id)
	e.held.Delete(r.id)
	e.order = slices.DeleteFunc(e.order, func(o *run) bool { return o == r })
	for _, m := range r.members {
		if m.state.SessionID != "" {
			e.bySession.Delete(m.state.SessionID)
		}
	}
	// A managed run's earlier attempts had sessions of their own.
	for _, t := range r.tasks {
		for _, a := range t.attempts {
			if a.sessionID != "" {
				e.bySession.Delete(a.sessionID)
			}
		}
	}
	r.cancel()
	if r.teardownCancel != nil {
		r.teardownCancel()
	}
	if e.OnForget != nil {
		e.OnForget(r.snapshot(false, false))
	}
}

// holds reports whether the engine holds the run with the given ID, without
// its lock (held): the records ask as they prune (Records.prune), on the
// goroutine that saves a record, which may be the one forgetting a run under
// the engine's lock (journalForgotten), so the answer must not wait for it.
func (e *Engine) holds(runID string) bool {
	_, ok := e.held.Load(runID)
	return ok
}

// IfKept runs f while the engine keeps the run with the given ID, under the
// engine's lock, and reports whether it ran. A run is forgotten under that
// lock too (OnForget), so what f makes for the run, such as a link, cannot
// outlive a forgotten run unseen. f must not call the engine or wait.
func (e *Engine) IfKept(runID string, f func()) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.runs[runID]; !ok {
		return false
	}
	f()
	return true
}

// Note adds an entry of type typ with msg to the log of the run with the
// given ID, cleaned and bounded as the run's own entries; a run the engine
// does not have takes none.
func (e *Engine) Note(runID, typ, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.runs[runID]; ok {
		r.note(typ, "%s", msg)
	}
}

// MemberOf reports the run and the member a session belongs to.
func (e *Engine) MemberOf(sessionID string) (runID, member string, ok bool) {
	v, ok := e.bySession.Load(sessionID)
	if !ok {
		return "", "", false
	}
	sm := v.(sessionMember)
	return sm.r.id, sm.m.def.Name, true
}

// Get returns the run with the given ID.
func (e *Engine) Get(runID string) (Run, bool) {
	e.mu.Lock()
	r, ok := e.runs[runID]
	var out Run
	if ok {
		out = r.snapshot(false, true)
	}
	e.mu.Unlock()
	if !ok {
		return Run{}, false
	}
	e.refresh(&out)
	return out, true
}

// List returns every run, the newest first.
func (e *Engine) List() []Run {
	e.mu.Lock()
	out := make([]Run, 0, len(e.order))
	for i := len(e.order) - 1; i >= 0; i-- {
		out = append(out, e.order[i].snapshot(false, false))
	}
	e.mu.Unlock()
	for i := range out {
		e.refresh(&out[i])
	}
	return out
}

// GetWithDiffs is Get with the diff of every member that has a worktree: the
// lines its worktree adds and removes since it began (DiffStat), read again,
// at most diffReaders at once, once what was read is diffTTL old. A read that
// fails keeps the diff read before; one that ctx cuts short is not waited
// out: the next call reads again.
func (e *Engine) GetWithDiffs(ctx context.Context, runID string) (Run, bool) {
	type read struct {
		m              *member
		worktree, base string
		prevAt         time.Time
	}
	e.mu.Lock()
	r, ok := e.runs[runID]
	if !ok {
		e.mu.Unlock()
		return Run{}, false
	}
	now := e.now()
	var reads []read
	for _, m := range r.members {
		if m.base != "" && (m.diffAt.IsZero() || now.Sub(m.diffAt) >= diffTTL) {
			reads = append(reads, read{m, m.state.Worktree, m.base, m.diffAt})
			m.diffAt = now // one reader at a time
		}
	}
	e.mu.Unlock()
	// A read that fails keeps what was read before; one that its request cut
	// short leaves the next request to read again.
	var wg sync.WaitGroup
	slots := make(chan struct{}, diffReaders)
	for _, rd := range reads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			added, removed, err := DiffStat(ctx, rd.worktree, rd.base)
			e.mu.Lock()
			defer e.mu.Unlock()
			switch {
			case err == nil:
				rd.m.diff = &Diff{Added: added, Removed: removed}
			case ctx.Err() != nil && rd.m.diffAt.Equal(now):
				rd.m.diffAt = rd.prevAt
			}
		}()
	}
	wg.Wait()
	e.mu.Lock()
	out := r.snapshot(true, true)
	e.mu.Unlock()
	e.refresh(&out)
	return out, true
}

// snapshot copies r, with the members' diffs when withDiffs and the run's
// chat when withChat. The caller holds e.mu.
func (r *run) snapshot(withDiffs, withChat bool) Run {
	out := Run{ID: r.id, CrewID: r.crewID, Name: r.name, Label: r.label, Goal: r.goal, Cwd: r.cwd, Isolation: r.isolation, Yolo: r.yolo,
		StartedAt: r.startedAt, StoppedAt: r.stoppedAt, Members: make([]MemberState, 0, len(r.members)),
		ResumedFrom: r.resumedFrom, ResumedBy: r.resumedBy,
		Log: append([]session.ActivityEntry{}, r.log...)}
	for _, m := range r.members {
		st := m.state
		st.Diff = nil
		if withDiffs && m.diff != nil {
			d := *m.diff
			st.Diff = &d
		}
		st.Prompt, st.Args, st.Base = m.def.Prompt, slices.Clone(m.def.Args), m.base
		out.Members = append(out.Members, st)
	}
	if r.managed {
		out.Managed = &Managed{Revision: r.revision}
		out.Paused, out.PausedWhy = r.paused, r.pausedWhy
		out.Outcome = r.outcome
		out.Tasks = r.taskStates()
		out.Manifest = r.manifestState()
		out.Cleaning = r.trees.n
	}
	if withChat {
		for _, m := range r.chat.History() {
			out.Chat = append(out.Chat, session.ChatToProto(m))
		}
	}
	return out
}

// refresh shows as ended the running members of out whose session has ended
// or is gone from the server, marks the starting and running members whose
// session waits on a prompt, and derives the run's state (runState). A member
// still starting is left to its start, which ends it with the reason. It
// looks the sessions up: the caller does not hold e.mu.
func (e *Engine) refresh(out *Run) {
	for i := range out.Members {
		m := &out.Members[i]
		if m.SessionID == "" || (m.Status != MemberRunning && m.Status != MemberStarting) {
			continue
		}
		l, ok := e.lookup(m.SessionID)
		if !ok {
			if m.Status == MemberRunning {
				m.Status = MemberEnded
			}
			continue
		}
		info := l.Info()
		if info.Status.Ended() {
			if m.Status == MemberRunning {
				m.Status, m.Ended = MemberEnded, info.EndedAt
			}
			continue
		}
		// A question keys typed in the terminal have answered (Answered) is no longer the member's wait (#258).
		m.NeedsInput = info.Attention.State == session.AttentionNeedsInput && info.Attention.Answered == ""
	}
	out.State, out.NeedsInput = runState(*out)
}

// runState is a run's state and how many of its members wait on a prompt:
// stopped once stopped; finished when every member has ended and none is
// pending; needs_input while the session of a starting or running member
// waits on a prompt; running otherwise. web/app/utils/runs.ts derives the
// same from the run and the live sessions.
func runState(r Run) (string, int) {
	needs, ended := 0, 0
	for _, m := range r.Members {
		switch {
		case m.Status == MemberEnded:
			ended++
		case m.NeedsInput:
			needs++
		}
	}
	switch {
	case r.StoppedAt != nil:
		return RunStopped, needs
	case r.Managed != nil && r.Outcome != "":
		// A managed run is finished once every task is terminal: its idle
		// sessions stay for a look, and do not keep it running.
		return RunFinished, 0
	case r.Managed == nil && ended == len(r.Members):
		return RunFinished, 0
	case needs > 0:
		return RunNeedsInput, needs
	}
	return RunRunning, 0
}

// awaitReady waits until l is ready for its prompt (readyAt), looking every
// readyPoll. While a trust question shows it calls held with the question,
// once until the question goes, and the wait starts over when it goes: the
// cap does not run meanwhile, so a prompt is never typed into the question.
// After readyCap it returns errNotReady, and the prompt is typed anyway; once
// the session has ended it returns session.ErrSessionEnded.
func awaitReady(ctx context.Context, l *session.Local, held func(question string)) error {
	return productReady.await(ctx, l, held)
}

// readyWait is how a readiness wait looks: every poll, and ready once the
// output has been quiet for quiet and min has passed since the wait began.
// The product's is productReady; a test that is not about readiness gives
// its sessions a short one, as the product's bounds are not the test's.
type readyWait struct{ poll, quiet, min time.Duration }

var productReady = readyWait{readyPoll, readyQuiet, readyMin}

// await is awaitReady with w's bounds.
func (w readyWait) await(ctx context.Context, l *session.Local, held func(question string)) error {
	start := time.Now()
	tick := time.NewTicker(w.poll)
	defer tick.Stop()
	holding := false
	for {
		select {
		case <-l.Ended():
			return session.ErrSessionEnded
		default:
		}
		att := l.Info().Attention
		seen, on := l.BracketedPaste()
		now := time.Now()
		ready, capped, hold := w.at(readiness{state: att.State, source: att.Source, lastOutput: l.LastOutputAt(), pasteSeen: seen, pasteOn: on}, start, now)
		switch {
		case hold:
			if !holding {
				held(att.Message)
			}
			holding = true
			start = now // once it is answered, the wait starts over
		case ready:
			return nil
		case capped:
			return errNotReady
		default:
			holding = false
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.Ended():
			return session.ErrSessionEnded
		case <-tick.C:
		}
	}
}

// readiness is what readyAt looks at of a session.
type readiness struct {
	state  session.AttentionState
	source string
	// lastOutput is when the output last moved (title updates aside), zero
	// before any.
	lastOutput time.Time
	// pasteSeen and pasteOn are the program's bracketed-paste mode
	// (session.Local.BracketedPaste).
	pasteSeen, pasteOn bool
}

// readyAt says whether a session is ready for its prompt at now, in a wait
// that began at start. A trust question on its screen holds the prompt (hold),
// and so does a question asked during the work (source "question").
// It is ready when its agent reports that it waits for input or is done, or
// when its output has been quiet for readyQuiet and readyMin has passed, but
// never while the program has turned bracketed paste on and then off again:
// it is between screens (Claude Code while it starts), and what is typed then
// loses its Enter. capped is the wait reaching readyCap.
func readyAt(rd readiness, start, now time.Time) (ready, capped, hold bool) {
	return productReady.at(rd, start, now)
}

// at is readyAt with w's bounds.
func (w readyWait) at(rd readiness, start, now time.Time) (ready, capped, hold bool) {
	if rd.state == session.AttentionNeedsInput && (rd.source == session.SourceTrust || rd.source == session.SourceQuestion) {
		return false, false, true
	}
	if rd.state == session.AttentionNeedsInput || rd.state == session.AttentionDone {
		return true, false, false
	}
	capped = now.Sub(start) >= readyCap
	if rd.pasteSeen && !rd.pasteOn {
		return false, capped, false
	}
	if !rd.lastOutput.IsZero() && now.Sub(rd.lastOutput) >= w.quiet && now.Sub(start) >= w.min {
		return true, false, false
	}
	return false, capped, false
}

// ResumeMember starts an ended member of a run again, in its working
// directory (its worktree and branch, which stay), as a session that resumes
// the agent session resume names with the agent's recipe, or, with resume
// "", as a fresh one: then its role prompt is typed again once it is ready,
// as at its first start; a resumed agent has its conversation, and gets no
// prompt. It returns the new session's ID once the session exists. A member
// still starting or running is refused (ErrMemberRunning), and so is one of a
// stopped run (ErrRunStopped).
func (e *Engine) ResumeMember(ctx context.Context, runID, name, resume string) (string, error) {
	e.mu.Lock()
	r, ok := e.runs[runID]
	if !ok {
		e.mu.Unlock()
		return "", ErrRunNotFound
	}
	if r.managed {
		e.mu.Unlock()
		return "", ErrManaged
	}
	m := r.member(name)
	var err error
	switch {
	case m == nil:
		err = ErrMemberNotFound
	case r.stopping && !r.stopDone:
		err = ErrRunStopped
	case m.state.Status == MemberPending:
		err = ErrMemberRunning
	case m.resuming:
		// A resume of it is making its session: refused, not started
		// beside it, whatever the ended session on its state says.
		err = ErrMemberRunning
	case m.state.Status == MemberStarting && !e.ended(m):
		// Marked starting by a resume under way, its ended session's id
		// still on it: a second resume meanwhile is refused, not started
		// beside it. One left starting by a session that ended (a stop
		// during its readiness wait) is resumed.
		err = ErrMemberRunning
	case m.state.Status != MemberEnded && !e.ended(m):
		err = ErrMemberRunning
	}
	if err != nil {
		e.mu.Unlock()
		return "", err
	}
	if r.stopping {
		// A stopped run whose stop completed is reopened in place: a context
		// of its own again, no longer stopping, the other members as they
		// are (ended ones are resumed one by one); a later Stop stops it
		// again.
		r.ctx, r.cancel = context.WithCancel(context.Background())
		r.stopping, r.stopDone, r.stoppedAt = false, false, nil
		r.note(session.ActivityStatus, "reopened: %s resumes", name)
		r.touch()
	}
	old := m.state.SessionID
	cwd := r.cwd
	if r.isolation == IsolationWorktree && m.state.Worktree != "" {
		cwd = filepath.Join(m.state.Worktree, r.prefix)
	}
	m.state.Status = MemberStarting
	m.resuming = true
	r.starts.Add(1)
	r.touch()
	e.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(r.ctx, cancel)()
	local, err := e.launcher.Launch(ctx, LaunchSpec{AgentID: m.def.AgentID, Name: name, Cwd: cwd, Args: slices.Clone(m.def.Args),
		Env: map[string]string{"GOAL": r.goal}, Ref: session.CrewRef{RunID: r.id, CrewID: r.crewID, Member: name}, Yolo: r.yolo,
		Resume: resume, ResumedFrom: old})
	if err != nil {
		e.mu.Lock()
		m.resuming = false
		e.mu.Unlock()
		r.starts.Done()
		e.fail(r, m, err)
		return "", fmt.Errorf("member %s: %w", quote(name), err)
	}
	id := local.Info().ID
	started := time.Now().UTC()
	e.mu.Lock()
	m.resuming = false
	if old != "" {
		e.bySession.Delete(old)
	}
	m.state.SessionID, m.state.Started, m.state.Ended, m.state.Err = id, &started, nil, ""
	e.bySession.Store(id, sessionMember{r: r, m: m})
	if resume != "" {
		// Starting until its session is ready for a prompt (settle).
		r.note(session.ActivityStatus, "%s resumed its conversation", name)
	} else {
		// A new conversation: its prompt is typed again, and only a done
		// after that prompt counts.
		m.prompted, m.promptedAt = false, time.Time{}
		r.note(session.ActivityStatus, "%s started anew", name)
	}
	e.mu.Unlock()
	if resume != "" {
		go e.settle(r, m, local) // running once its session is ready, and ends the start
		return id, nil
	}
	go e.finish(r, m, local) // types its prompt again, and ends the start
	return id, nil
}

// ended reports whether m's session has ended or is gone, as refresh shows
// it. The caller holds e.mu; it reads the session, which never calls into the
// engine while it holds its lock.
func (e *Engine) ended(m *member) bool {
	if m.state.SessionID == "" {
		return false
	}
	l, ok := e.lookup(m.state.SessionID)
	return !ok || l.Info().Status.Ended()
}
