package ledger

import "time"

// The states a row can be in. A run's state is the durable one: the engine
// derives what it shows from the rows and the sessions. An attempt's
// states run reserved → launched → running → submitting → verifying →
// succeeded | failed, or end in cancelled, stopped, interrupted (its process
// went with the owner) or uncertain (the owner stopped before it knew
// whether a side effect happened). A check's run started → running →
// finished, or interrupted. A command is accepted, then done, failed or
// uncertain.
const (
	RunRunning     = "running"
	RunStopped     = "stopped"
	RunFinished    = "finished"
	RunInterrupted = "interrupted"

	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"

	TaskBlocked   = "blocked"
	TaskReady     = "ready"
	TaskRunning   = "running"
	TaskVerifying = "verifying"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"

	AttemptReserved    = "reserved"
	AttemptLaunched    = "launched"
	AttemptRunning     = "running"
	AttemptSubmitting  = "submitting"
	AttemptVerifying   = "verifying"
	AttemptSucceeded   = "succeeded"
	AttemptFailed      = "failed"
	AttemptCancelled   = "cancelled"
	AttemptStopped     = "stopped"
	AttemptInterrupted = "interrupted"
	AttemptUncertain   = "uncertain"

	CheckStarted     = "started"
	CheckRunning     = "running"
	CheckFinished    = "finished"
	CheckInterrupted = "interrupted"

	CommandAccepted  = "accepted"
	CommandDone      = "done"
	CommandFailed    = "failed"
	CommandUncertain = "uncertain"
)

// The phases a receipt is of (CheckRow.Phase): a task's check, or a step
// of a project manifest's phase (EN1); a prerequisite's receipt is a
// PrerequisiteRow, of the phase PhasePrerequisite. A step's row runs
// started → running → finished, or interrupted, as a check's does.
const (
	PhaseCheck        = "check"
	PhaseSetup        = "setup"
	PhaseService      = "service"
	PhaseReady        = "ready"
	PhaseVerify       = "verify"
	PhaseCleanup      = "cleanup"
	PhasePrerequisite = "prerequisite"
)

// A tree's kinds and states (TreeRow): an attempt's worktree or the
// checkout of its result, resolved (its manifest read, the row put),
// ready (made usable) and ended (its end recorded).
const (
	TreeWorktree = "worktree"
	TreeCheckout = "checkout"

	TreeResolved = "resolved"
	TreeReady    = "ready"
	TreeEnded    = "ended"
)

// RunRow is a managed run.
type RunRow struct {
	ID       string
	CrewID   string
	Crew     string // the crew as launched, JSON
	Base     string // the commit the run started from
	State    string
	Outcome  string
	Paused   bool
	Revision int64 // bumped by every transition; the run's revision on the API
	// ContinuedFrom names the run this one continues (a resume after a
	// restart), ContinuedBy the run that continues this one.
	ContinuedFrom string
	ContinuedBy   string
	CreatedAt     time.Time
	ClosedAt      time.Time // zero while the run is open
	// PausedWhy is the pause's words, beside Paused: what paused the
	// dispatch when the engine did (a project manifest's step, the
	// ledger), "" for a person's pause and while not paused.
	PausedWhy string
	// Manifest is the run's project manifest at its base as the engine
	// resolved it, JSON (where it comes from, the crew's revision or the
	// file's blob, and its prerequisites' ids in order); "" for a run
	// without one. A run whose record is missing is rebuilt from it.
	Manifest string
	// Ticket is the id of the Dispatch ticket the run was launched from,
	// and Package the launch package as Dispatch built it, JSON, byte for
	// byte (at most MaxPackage bytes): both "" for a run no ticket
	// launched. A continuation carries its source's. Written by the run's
	// first row (CreateRun) and never changed after it: a later PutRun
	// leaves them as they are.
	Ticket  string
	Package string
}

// TaskRow is a task of a run.
type TaskRow struct {
	RunID         string
	ID            string
	Member        string
	Kind          string
	State         string
	Reason        string
	Attempts      int    // attempts made so far, continuations' included
	ResultAttempt int    // the attempt whose result stands, 0 for none
	ResultCommit  string // its commit
	Verdict       string // a review's
	ImportedFrom  string // "<run>/<task>/<n>" when a continuation imported the result
	// Repair is the repair a review asked of the task, as JSON (the
	// engine's crew.TaskRepair), "" without one; Extra the attempts
	// allowed past the bound (retries, and attempts not counted).
	Repair string
	Extra  int
	// Withdrawn is the number of the task's last attempt a withdrawal
	// took back (its input's result withdrawn, #334), 0 for none: nothing
	// from an attempt numbered so or less is used again.
	Withdrawn int
}

// AttemptRow is one try at a task.
type AttemptRow struct {
	RunID        string
	TaskID       string
	N            int
	Member       string
	SessionID    string
	Worktree     string
	Branch       string
	StartCommit  string
	ResultCommit string
	State        string
	Stage        string // what the attempt was doing when it failed or was cut short
	Error        string
	ReservedAt   time.Time
	LaunchedAt   time.Time
	PromptedAt   time.Time
	ClosedAt     time.Time
	// Result is the submitted result as JSON (the engine's crew.Result:
	// summary, commit, issues, verdict, findings), at most MaxResult
	// bytes; "" before one is submitted.
	Result string
	// Checkpoint is the attempt's last checkpoint as JSON (what is done,
	// what remains, notes, the commit it is tied to), at most
	// MaxCheckpoint bytes; "" without one (#334, row 10).
	Checkpoint string
}

// MessageRow is one message a member sent another (#334, row 9): its
// sequence within the run, when it was typed into the recipient's session
// (DeliveredAt, zero until then) and when the recipient acknowledged it
// (AckedAt, zero until then).
type MessageRow struct {
	RunID     string
	Seq       int64
	From, To  string
	Text      string
	CreatedAt time.Time
	// TypingAt is when the engine set out to type the message into the
	// recipient's session (the intent, before the line), DeliveredAt when
	// the line was entered: typing set and delivered not, after the
	// server's end, is a delivery that cannot be told.
	TypingAt    time.Time
	DeliveredAt time.Time
	AckedAt     time.Time
}

// CheckRow is one run of a check against an attempt's result, or of a
// step of a project manifest in one of its trees: its receipt.
type CheckRow struct {
	RunID  string
	TaskID string
	N      int
	ID     string
	// K numbers the runs of this id on this attempt, whatever their phase:
	// the attempt allocates it, so a setup step run in the worktree (k 1)
	// and again in the checkout (k 2) are told apart.
	K int
	// Phase is PhaseCheck for a task's check (every row a version 4
	// database holds), else the manifest phase the step ran in; Manifest
	// the identity of the manifest that ran it ("crew:<revision>",
	// "file:<blob>"), "" for a check.
	Phase    string
	Manifest string
	// Ports are the tree's ports by name, Tries the tries of a readiness
	// probe (0 when it ran none, a service of its tree having exited before
	// its first; 1 for every other row), Error the words of its failure
	// ("" for none), bounded as an event's are, and URL a probe's expanded
	// URL (at most MaxURL bytes), its command when it has no argv.
	Ports            map[string]int
	Tries            int
	Error            string
	URL              string
	Argv             []string
	Dir              string
	Subject          string // the commit checked
	State            string
	PID              int
	Group            int
	ProcessStartedAt time.Time
	StartedAt        time.Time
	FinishedAt       time.Time
	Exit             int
	Signal           string
	TimedOut         bool
	Killed           bool
	Stable           bool
	Truncated        bool
	Total            int64
	Log              []byte // at most MaxLog bytes; nil once dropped
	LogDropped       bool   // the log was not kept: the run's logs were at MaxRunLogs; the hash and the tail stay
	LogSHA256        string
	Tail             string
	// LogBytes is the log's size as kept, read with the row whether or not
	// the log comes along; a put ignores it.
	LogBytes int64
}

// PrerequisiteRow is one run of a project manifest's prerequisite in a
// run, before any tree: its receipt, the shape of a CheckRow but for the
// attempt, the phase, the ports and the tries. K numbers the
// prerequisite's runs in the run (an unpause runs them again).
type PrerequisiteRow struct {
	RunID            string
	ID               string
	K                int
	Manifest         string
	Error            string
	URL              string
	Argv             []string
	Dir              string
	Subject          string // the commit the run's manifest was read at
	State            string
	PID              int
	Group            int
	ProcessStartedAt time.Time
	StartedAt        time.Time
	FinishedAt       time.Time
	Exit             int
	Signal           string
	TimedOut         bool
	Killed           bool
	Stable           bool
	Truncated        bool
	Total            int64
	Log              []byte
	LogDropped       bool
	LogSHA256        string
	Tail             string
	LogBytes         int64
}

// TreeRow is a tree's lifecycle: an attempt's worktree or the checkout of
// its result, keyed by its attempt and kind, from the transaction that
// resolved its manifest (TreeResolved) to the one that ended it
// (TreeEnded, with why, and Interrupted when the recovery ended it).
type TreeRow struct {
	RunID       string
	TaskID      string
	N           int
	Kind        string
	Dir         string
	Commit      string
	Manifest    string
	Ports       map[string]int
	Preview     string
	State       string
	Why         string
	Interrupted bool
	EndedAt     time.Time
}

// CommandRow is a command's receipt: what was asked, by whom, and what
// became of it.
type CommandRow struct {
	RunID       string
	Actor       string
	ID          string
	Intent      string
	Fingerprint string
	Status      string
	Receipt     string // JSON, what the caller is told
	CreatedAt   time.Time
}

// EventRow is one event of a run's sequence.
type EventRow struct {
	RunID   string
	Seq     int64
	At      time.Time
	Kind    string
	Payload string // JSON
}

// Bounds.
const (
	// MaxLog bounds a check's log blob; MaxRunLogs bounds a run's logs in
	// all: past it a check's log is dropped (LogDropped), its hash and tail
	// kept, so a run's checks cannot fill the database.
	MaxLog     = 1 << 20
	MaxRunLogs = 64 << 20
	// MaxPayload bounds an event's payload and a command's receipt.
	MaxPayload = 64 << 10
	// MaxResult bounds an attempt's submitted result (AttemptRow.Result).
	MaxResult = 256 << 10
	// MaxCrew bounds the crew JSON kept with a run.
	MaxCrew = 1 << 20
	// MaxPackage bounds the launch package kept with a run (RunRow.Package):
	// Dispatch's own bound on a package as it stores one.
	MaxPackage = 512 << 10
	// maxTicketID bounds a run's ticket id (RunRow.Ticket): a Dispatch id
	// is 16 hex digits, and the bound leaves room without leaving it open.
	maxTicketID = 64
	// MaxURL bounds a probe's URL kept with its receipt.
	MaxURL = 2048
)

// maxCommands bounds the commands kept per run: past it a new one is
// refused, since dropping an old receipt would let its resend run twice.
// A variable for the tests, as maxRunLogs (MaxRunLogs) is.
var (
	maxCommands = 4096
	// MaxCheckpoint bounds an attempt's checkpoint, MaxMessageText a
	// message's text, and maxMessages the messages a run keeps.
	MaxCheckpoint  = 16 << 10
	MaxMessageText = 4 << 10
	maxMessages    = 4096
	maxRunLogs     = int64(MaxRunLogs)
)

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func unstamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
