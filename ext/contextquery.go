package ext

import (
	"context"
	"time"
	"unicode/utf8"
)

// What a context provider is told of an attempt besides main's query
// (docs/plans/context-seams.md, CT-1): the run's base, the ticket and plan
// revision it was launched from, the task's checks, the evidence the
// attempt rests on, who launched it, and of each input the attempt, start,
// verdict and findings of the result. Every one of them is what the server
// already keeps of the run; none is a credential.

// ContextTicket is the ticket and plan revision a run was launched from:
// the ticket's key and revision as launched, the plan revision's id and
// number, the launch package's SHA-256, and the ticket's owner and
// assignees by their display names.
type ContextTicket struct {
	Key       string   `json:"key"`
	Rev       int64    `json:"rev"`
	Plan      string   `json:"plan"`  // PlanIDLen lower-case hex
	PlanN     int      `json:"planN"` // from 1
	Digest    string   `json:"digest"`
	Owner     string   `json:"owner,omitempty"`
	Assignees []string `json:"assignees,omitempty"` // at most MaxContextAssignees
}

// ContextCheck is one of the task's checks: its id and its argv as the
// crew holds it. An argv past MaxContextArgv entries, or with an entry
// past MaxContextArg bytes, is left out and Cut set.
type ContextCheck struct {
	ID   string   `json:"id"`
	Argv []string `json:"argv,omitempty"`
	Cut  bool     `json:"cut,omitempty"`
}

// ContextFinding is one thing a review found, as its result holds it.
type ContextFinding struct {
	Severity string `json:"severity"` // blocker | major | minor | nit
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"` // 1 to MaxContextFinding bytes
}

// ContextReview is a person's review of a candidate, as Dispatch keeps it:
// its id and its findings, in the review's order, each by its id, its
// state and the place it names. Neither the review's words nor a
// finding's are handed, nor who made it.
type ContextReview struct {
	ID       string                 `json:"id"`       // ContextReviewIDLen lower-case hex
	Findings []ContextReviewFinding `json:"findings"` // 1 to MaxContextReviewFindings
}

// ContextReviewFinding is one finding of a review: its id, its state (its
// latest mark's verdict: open with none, resolved or withdrawn), and the
// file and first line it is on, when it names them.
type ContextReviewFinding struct {
	ID    string `json:"id"` // ContextReviewIDLen lower-case hex
	State string `json:"state"`
	Path  string `json:"path,omitempty"` // one line of at most MaxContextRef bytes
	Line  int    `json:"line,omitempty"` // 1 to MaxContextLine
}

// The states of a review's finding.
const (
	FindingOpen      = "open"
	FindingResolved  = "resolved"
	FindingWithdrawn = "withdrawn"
)

const (
	MaxContextPack        = 128  // a pack mark, in bytes: printable ASCII, one line
	MaxContextChecks      = 8    // a task's checks (the crew's own bound)
	MaxContextArgv        = 32   // entries of one check's argv handed
	MaxContextArg         = 512  // bytes of one entry handed
	MaxContextEvidence    = 32   // evidence refs
	MaxContextEvidenceRef = 512  // bytes of one ref (Brakeman's MaxRef)
	MaxContextAssignees   = 8    // a ticket's assignees (Dispatch's MaxAssignees)
	MaxContextPerson      = 60   // characters of a person's name (Dispatch's MaxPersonName)
	MaxContextFindings    = 50   // a review's findings (the crew's own bound)
	MaxContextFinding     = 1000 // bytes of a finding's message
	MaxContextInputs      = 128  // an attempt's inputs (the crew's tasks)
	// MaxContextDecisions bounds the decisions a query names (a task
	// file's ended decisions are the newest 20 too).
	MaxContextDecisions = 20
	// MaxContextReviews bounds the reviews a query names, and
	// MaxContextReviewFindings a review's findings (Dispatch's
	// MaxReviewFindings: a review is never cut).
	MaxContextReviews        = 5
	MaxContextReviewFindings = 50
	// ContextReviewIDLen is a review's and a finding's id, in lower-case
	// hex (Dispatch's IDLen).
	ContextReviewIDLen = 16
	// MaxContextLine is a finding's line (Dispatch's bound).
	MaxContextLine = 10_000_000
	// CommitQueue bounds a provider's queue of commit notes, the oldest
	// dropped past it.
	CommitQueue = 256
)

// maxContextGoal and maxContextInstructions are a crew's goal's and a
// task's instructions' bounds, in characters (the crew's own).
const (
	maxContextGoal         = 2000
	maxContextInstructions = 8000
)

// CommitNoter is an optional interface of a context provider: told of a
// run's base as the run starts (and again as a continuation of it starts:
// a provider takes a repeat), and of each candidate as its submit is
// recorded. Called in order, from one goroutine of the provider's own; it
// must return within DeliveryWait, and it never blocks the engine: past
// CommitQueue notes waiting, the oldest are dropped.
type CommitNoter interface {
	NoteCommit(ctx context.Context, n CommitNote)
}

// CommitNote tells a provider of a commit of a run: its base, or a
// candidate an attempt of a task submitted.
type CommitNote struct {
	Run     string    `json:"run"`
	Task    string    `json:"task,omitempty"`    // a candidate's
	Attempt int       `json:"attempt,omitempty"` // a candidate's
	Kind    string    `json:"kind"`              // base | candidate
	Commit  string    `json:"commit"`            // full id, 40 or 64 lower-case hex
	At      time.Time `json:"at"`
}

// The kinds of a commit note.
const (
	CommitBase      = "base"
	CommitCandidate = "candidate"
)

// Valid checks n: a run id, a kind of base or candidate, a full commit id,
// a time; a candidate's task id and attempt from 1, and a base's none.
func (n CommitNote) Valid() error {
	const rec = "commit note"
	switch {
	case !runID(n.Run):
		return invalid(rec, "run %q is not a run id", n.Run)
	case !commitID(n.Commit):
		return invalid(rec, "commit %q is not a full commit id", n.Commit)
	case n.At.IsZero():
		return invalid(rec, "at is not set")
	}
	switch n.Kind {
	case CommitBase:
		if n.Task != "" || n.Attempt != 0 {
			return invalid(rec, "a base names no task and no attempt")
		}
	case CommitCandidate:
		if !taskID(n.Task) || n.Attempt < 1 {
			return invalid(rec, "a candidate names its task and its attempt")
		}
	default:
		return invalid(rec, "kind %q is not base or candidate", n.Kind)
	}
	return nil
}

// Valid checks q against its bounds, as the server builds it: a run id, a
// crew id, a task id, an attempt from 1, a kind of work or review, the
// crew's goal and the task's instructions as text within the crew's
// bounds, absolute paths for the worktree and the directory, full commit
// ids for the start and the base; at most MaxContextInputs inputs, each
// with a task id, a full commit id or none, an attempt not below 0, a
// start that is a full id or none, a verdict of a review or none, and at
// most MaxContextFindings findings each within its bounds; the ticket's
// key, revision, plan id and number, digest and people; at most
// MaxContextChecks checks, each an id and an argv within its bounds (none
// when cut); at most MaxContextEvidence refs of one line of 1 to
// MaxContextEvidenceRef bytes; a launcher of at most MaxContextPerson
// characters on one line; at most MaxContextDecisions decisions, each a
// decision's id named once, one of the six states, final as that state
// is, a revision from 1 and a closing time only when final; and at most
// MaxContextReviews reviews, each an id named once with 1 to
// MaxContextReviewFindings findings, each an id named once in the query,
// a finding's state, a path of one line of at most MaxContextRef bytes
// and a line of at most MaxContextLine.
func (q ContextQuery) Valid() error {
	const rec = "context query"
	switch {
	case !runID(q.Run):
		return invalid(rec, "run %q is not a run id", q.Run)
	case !slug(q.Crew, MaxCrewID, false):
		return invalid(rec, "crew %q is not a crew id", q.Crew)
	case !taskID(q.Task):
		return invalid(rec, "task %q is not a task id", q.Task)
	case q.Attempt < 1:
		return invalid(rec, "attempt is below 1")
	case q.Kind != "work" && q.Kind != "review":
		return invalid(rec, "kind %q is not work or review", q.Kind)
	case !text(q.Goal) || utf8.RuneCountInString(q.Goal) > maxContextGoal:
		return invalid(rec, "goal is not text of at most %d characters", maxContextGoal)
	case !text(q.Instructions) || utf8.RuneCountInString(q.Instructions) > maxContextInstructions:
		return invalid(rec, "instructions are not text of at most %d characters", maxContextInstructions)
	case !folder(q.Worktree) || !folder(q.Dir):
		return invalid(rec, "the worktree and the directory are absolute paths of at most %d bytes", MaxFolder)
	case q.Start != "" && !commitID(q.Start):
		return invalid(rec, "start %q is not a full commit id", q.Start)
	case q.Base != "" && !commitID(q.Base):
		return invalid(rec, "base %q is not a full commit id", q.Base)
	case len(q.Inputs) > MaxContextInputs:
		return invalid(rec, "%d inputs, past %d", len(q.Inputs), MaxContextInputs)
	case len(q.Checks) > MaxContextChecks:
		return invalid(rec, "%d checks, past %d", len(q.Checks), MaxContextChecks)
	case len(q.Evidence) > MaxContextEvidence:
		return invalid(rec, "%d evidence refs, past %d", len(q.Evidence), MaxContextEvidence)
	case !oneLine(q.LaunchedBy) || utf8.RuneCountInString(q.LaunchedBy) > MaxContextPerson:
		return invalid(rec, "launchedBy is not one line of at most %d characters", MaxContextPerson)
	case len(q.Decisions) > MaxContextDecisions:
		return invalid(rec, "%d decisions, past %d", len(q.Decisions), MaxContextDecisions)
	case len(q.Reviews) > MaxContextReviews:
		return invalid(rec, "%d reviews, past %d", len(q.Reviews), MaxContextReviews)
	}
	seen := map[string]bool{}
	for i, d := range q.Decisions {
		if err := contextDecision(d); err != nil {
			return invalid(rec, "decision %d: %v", i, err)
		}
		if seen[d.Request] {
			return invalid(rec, "decision %s is named twice", d.Request)
		}
		seen[d.Request] = true
	}
	ids := map[string]bool{}
	for i, rv := range q.Reviews {
		if err := rv.valid(ids); err != nil {
			return invalid(rec, "review %d: %v", i, err)
		}
	}
	for i, in := range q.Inputs {
		if err := in.valid(); err != nil {
			return invalid(rec, "input %d: %v", i, err)
		}
	}
	if q.Ticket != nil {
		if err := q.Ticket.valid(); err != nil {
			return invalid(rec, "ticket: %v", err)
		}
	}
	for i, c := range q.Checks {
		if err := c.valid(); err != nil {
			return invalid(rec, "check %d: %v", i, err)
		}
	}
	for i, ref := range q.Evidence {
		if err := line(rec, "evidence ref", ref, MaxContextEvidenceRef, true); err != nil {
			return invalid(rec, "evidence %d: %v", i, err)
		}
	}
	return nil
}

func (in ContextInput) valid() error {
	const rec = "context input"
	switch {
	case !taskID(in.Task):
		return invalid(rec, "task %q is not a task id", in.Task)
	case in.Commit != "" && !commitID(in.Commit):
		return invalid(rec, "commit %q is not a full commit id", in.Commit)
	case in.Attempt < 0:
		return invalid(rec, "attempt is below 0")
	case in.Start != "" && !commitID(in.Start):
		return invalid(rec, "start %q is not a full commit id", in.Start)
	case in.Verdict != "" && in.Verdict != "approved" && in.Verdict != "needs_changes":
		return invalid(rec, "verdict %q is not a review's", in.Verdict)
	case len(in.Findings) > MaxContextFindings:
		return invalid(rec, "%d findings, past %d", len(in.Findings), MaxContextFindings)
	}
	for i, f := range in.Findings {
		switch f.Severity {
		case "blocker", "major", "minor", "nit":
		default:
			return invalid(rec, "finding %d: severity %q is not one", i, f.Severity)
		}
		if err := line(rec, "a finding's path", f.Path, MaxContextRef, false); err != nil {
			return err
		}
		switch {
		case f.Line < 0:
			return invalid(rec, "finding %d: line is below 0", i)
		case f.Message == "" || len(f.Message) > MaxContextFinding || !text(f.Message):
			return invalid(rec, "finding %d: message is not text of 1 to %d bytes", i, MaxContextFinding)
		}
	}
	return nil
}

func (t ContextTicket) valid() error {
	const rec = "context ticket"
	switch {
	case !ticketKey(t.Key):
		return invalid(rec, "key %q is not a ticket's key", t.Key)
	case t.Rev < 0:
		return invalid(rec, "rev is below 0")
	case !lowerHex(t.Plan, PlanIDLen):
		return invalid(rec, "plan %q is not a plan revision's id", t.Plan)
	case t.PlanN < 1:
		return invalid(rec, "planN is below 1")
	case !lowerHex(t.Digest, DigestLen):
		return invalid(rec, "digest is not %d lower-case hex", DigestLen)
	case t.Owner != "" && name(rec, "owner", t.Owner, MaxContextPerson) != nil:
		return invalid(rec, "owner is not one line of 1 to %d characters", MaxContextPerson)
	case len(t.Assignees) > MaxContextAssignees:
		return invalid(rec, "%d assignees, past %d", len(t.Assignees), MaxContextAssignees)
	}
	for _, a := range t.Assignees {
		if err := name(rec, "an assignee", a, MaxContextPerson); err != nil {
			return err
		}
	}
	return nil
}

// contextDecision checks a decision as a query names it: a decision's id,
// one of the ledger's six states, Final as that state is, a revision from
// 1, and a closing time only once final.
func contextDecision(d DecisionState) error {
	const rec = "context decision"
	final, ok := decisionStates[d.State]
	switch {
	case !decisionID(d.Request):
		return invalid(rec, "%q is not a decision's id", d.Request)
	case !ok:
		return invalid(rec, "state %q is not a decision's", d.State)
	case d.Final != final:
		return invalid(rec, "final is %v for the state %s", d.Final, d.State)
	case d.Rev < 1:
		return invalid(rec, "rev is below 1")
	case !d.Final && !d.ClosedAt.IsZero():
		return invalid(rec, "a decision %s has not closed", d.State)
	}
	return nil
}

// decisionStates are a durable decision's states, each with whether it
// is final.
var decisionStates = map[string]bool{"open": false, "answered": false, "applied": true, "superseded": true, "cancelled": true, "expired": true}

// valid checks rv: its id, named once in the query, and 1 to
// MaxContextReviewFindings findings, each with an id named once in the
// query, a state, a path of one line of at most MaxContextRef bytes and a
// line of at most MaxContextLine. ids holds the ids the query named
// before rv, a review's after "r:", a finding's after "f:".
func (rv ContextReview) valid(ids map[string]bool) error {
	const rec = "context review"
	switch {
	case !lowerHex(rv.ID, ContextReviewIDLen):
		return invalid(rec, "id %q is not %d lower-case hex", rv.ID, ContextReviewIDLen)
	case ids["r:"+rv.ID]:
		return invalid(rec, "review %s is named twice", rv.ID)
	case len(rv.Findings) == 0:
		return invalid(rec, "review %s has no finding", rv.ID)
	case len(rv.Findings) > MaxContextReviewFindings:
		return invalid(rec, "%d findings, past %d", len(rv.Findings), MaxContextReviewFindings)
	}
	ids["r:"+rv.ID] = true
	for i, f := range rv.Findings {
		switch {
		case !lowerHex(f.ID, ContextReviewIDLen):
			return invalid(rec, "finding %d: id %q is not %d lower-case hex", i, f.ID, ContextReviewIDLen)
		case ids["f:"+f.ID]:
			return invalid(rec, "finding %s is named twice", f.ID)
		case f.State != FindingOpen && f.State != FindingResolved && f.State != FindingWithdrawn:
			return invalid(rec, "finding %d: state %q is not open, resolved or withdrawn", i, f.State)
		case f.Line < 0 || f.Line > MaxContextLine:
			return invalid(rec, "finding %d: line is not 0 to %d", i, MaxContextLine)
		}
		if err := line(rec, "a finding's path", f.Path, MaxContextRef, false); err != nil {
			return err
		}
		ids["f:"+f.ID] = true
	}
	return nil
}

func (c ContextCheck) valid() error {
	const rec = "context check"
	switch {
	case !taskID(c.ID):
		return invalid(rec, "id %q is not a check id", c.ID)
	case c.Cut && len(c.Argv) > 0:
		return invalid(rec, "a cut argv is left out")
	case len(c.Argv) > MaxContextArgv:
		return invalid(rec, "%d argv entries, past %d", len(c.Argv), MaxContextArgv)
	}
	for i, a := range c.Argv {
		if !ArgOK(a) {
			return invalid(rec, "argv[%d] is not UTF-8 of at most %d bytes with no NUL", i, MaxContextArg)
		}
	}
	return nil
}

// ArgOK reports whether a is an argv entry a query hands as it is: valid
// UTF-8 of at most MaxContextArg bytes with no NUL. A check's argv with
// any other entry is cut (ContextCheck.Cut).
func ArgOK(a string) bool {
	if len(a) > MaxContextArg || !utf8.ValidString(a) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] == 0 {
			return false
		}
	}
	return true
}

// taskID reports whether s has a task's (or a check's) id's shape: 1 to 40
// of a-z, 0-9, '_' and '-', beginning with a letter or a digit (the crew's
// own pattern).
func taskID(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// packMark reports whether s is a pack mark: 1 to MaxContextPack bytes of
// printable ASCII (a space among them) with no space at either end.
func packMark(s string) bool {
	if s == "" || len(s) > MaxContextPack || s[0] == ' ' || s[len(s)-1] == ' ' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}
