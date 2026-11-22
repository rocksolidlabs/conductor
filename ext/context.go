package ext

import (
	"context"
	"time"
)

// ContextProvider gives an attempt of a managed run context to read before
// it starts (what a Code Context Graph knows of its task), and is told the
// files the agents touched. The free default is no provider: an attempt's
// task file holds its task, its inputs and nothing more, and the touched
// index and git are the context there is.
type ContextProvider interface {
	Provider
	// NoteTouch is told one file an agent touched, from the sessions' file
	// activity. Called in order, from one goroutine of the provider's own.
	NoteTouch(ctx context.Context, f TouchedFile)
	// Context answers the blocks an attempt about to start should read.
	// Safe for concurrent use.
	Context(ctx context.Context, q ContextQuery) ([]ContextBlock, error)
}

// TouchedFile is one file an agent touched.
type TouchedFile struct {
	Session SessionRef `json:"session"`
	Path    string     `json:"path"` // absolute, as the session's touched index keys it
	Op      string     `json:"op"`   // read | edit | write | delete
	Tool    string     `json:"tool,omitempty"`
	By      string     `json:"by,omitempty"`
	At      time.Time  `json:"at"`
}

// ContextQuery is the attempt about to start.
type ContextQuery struct {
	Run          string         `json:"run"`
	Crew         string         `json:"crew"`
	Task         string         `json:"task"`
	Attempt      int            `json:"attempt"`
	Kind         string         `json:"kind"` // work | review
	Goal         string         `json:"goal"`
	Instructions string         `json:"instructions"`
	Worktree     string         `json:"worktree"` // <cwd>/.conductor/worktrees/<run>/<task>.<n>
	Dir          string         `json:"dir"`      // the working directory inside it (the crew's prefix)
	Start        string         `json:"start"`    // the commit it starts at; a review's, the candidate
	Inputs       []ContextInput `json:"inputs,omitempty"`
	// Base is the run's base commit, a full id.
	Base string `json:"base,omitempty"`
	// Ticket is the ticket and plan revision the run was launched from;
	// nil for a run launched from none.
	Ticket *ContextTicket `json:"ticket,omitempty"`
	// Checks are the task's checks, at most MaxContextChecks.
	Checks []ContextCheck `json:"checks,omitempty"`
	// Evidence is what the attempt rests on, as evidence refs (Brakeman's
	// ref grammar): the inputs' candidates, their checks' receipts, the
	// ticket and the plan revision; at most MaxContextEvidence of at most
	// MaxContextEvidenceRef bytes, in that order.
	Evidence []string `json:"evidence,omitempty"`
	// LaunchedBy is who launched the run, when the run keeps it: at most
	// MaxContextPerson characters.
	LaunchedBy string `json:"launchedBy,omitempty"`
	// Decisions are the durable decisions about the work: the gates of the
	// plan of the run's ticket and the decisions of the task (asked by it
	// or holding it), of the run and the runs it continues, each by its id
	// and its state as Host.Decision reads it, once the task's decisions
	// are settled for the attempt; at most MaxContextDecisions, kept by
	// rank and newest first within each (open and answered; then applied;
	// then the other final states), so one that waits is never cut for a
	// closed one; named in the order they were opened. A provider may cite
	// one; nothing it is handed answers one.
	Decisions []DecisionState `json:"decisions,omitempty"`
	// Reviews are what people's reviews (Dispatch's) found: for a review,
	// those of the candidates of the tasks it reviews, of the run and the
	// runs it continues, at most MaxContextReviews, the newest kept, in the
	// order they were made; for a work attempt that repairs a person's
	// request, the review the request is, alone (its task file's repair
	// carries the same findings). A finding's path is one inside the tree
	// that the tree's reads hand a provider, else none; a finding on the
	// base's side has no line.
	Reviews []ContextReview `json:"reviews,omitempty"`
}

// ContextInput is one task's result an attempt starts from: the task, the
// commit, the attempt whose result it is, where that attempt started, and
// a review's verdict and findings.
type ContextInput struct {
	Task     string           `json:"task"`
	Commit   string           `json:"commit"`
	Attempt  int              `json:"attempt,omitempty"`
	Start    string           `json:"start,omitempty"`
	Verdict  string           `json:"verdict,omitempty"`  // a review's: approved | needs_changes
	Findings []ContextFinding `json:"findings,omitempty"` // a review's, at most MaxContextFindings
}

// ContextBlock is one piece of context an attempt reads: reference
// material a provider derived, never the task's own words.
type ContextBlock struct {
	Title string `json:"title"`         // 1 to MaxContextTitle bytes, one line
	Ref   string `json:"ref,omitempty"` // a path or URL the block is about, at most MaxContextRef bytes
	Body  string `json:"body"`          // UTF-8 text, 1 to MaxContextBody bytes
	// Pack is the provider's mark of the record the block comes from (its
	// pack mark): opaque to the server, which stores, shows and compares
	// it and never reads it. Empty, or 1 to MaxContextPack bytes of
	// printable ASCII with no space at either end.
	Pack string `json:"pack,omitempty"`
}

const (
	MaxContextBlocks = 8 // a provider's, per attempt
	MaxContextTitle  = 120
	MaxContextRef    = 1024
	MaxContextBody   = 8 << 10
	MaxTaskContext   = 16 << 10 // every provider's blocks together, as a task file keeps them
	ContextWait      = 5 * time.Second
	TouchQueue       = 1024
)

// Valid checks b against its bounds: a title of one line of 1 to
// MaxContextTitle bytes, a ref of one line of at most MaxContextRef bytes,
// a body of 1 to MaxContextBody bytes of UTF-8 text whose only control
// characters are tabs and line ends, and a pack mark that is empty or 1 to
// MaxContextPack bytes of printable ASCII with no space at either end.
func (b ContextBlock) Valid() error {
	const r = "context block"
	if err := line(r, "title", b.Title, MaxContextTitle, true); err != nil {
		return err
	}
	if err := line(r, "ref", b.Ref, MaxContextRef, false); err != nil {
		return err
	}
	switch {
	case b.Body == "":
		return invalid(r, "body is empty")
	case len(b.Body) > MaxContextBody:
		return invalid(r, "body is %d bytes, past %d", len(b.Body), MaxContextBody)
	case !text(b.Body):
		return invalid(r, "body is not UTF-8 text")
	case b.Pack != "" && !packMark(b.Pack):
		return invalid(r, "pack is not 1 to %d bytes of printable ASCII with no space at either end", MaxContextPack)
	}
	return nil
}
