package crew

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/quoted"
)

// ContextFunc answers the context blocks an attempt about to start is to
// read, from the context providers (Program item C4): the server's, which
// asks every provider within ext.ContextWait, checks what each answers and
// bounds them together (internal/api, extcontext.go). It never fails the
// attempt: no block is an answer. It is called outside the engine's lock.
type ContextFunc func(ctx context.Context, q ext.ContextQuery) []TaskContext

// SetContext makes f what the engine asks for an attempt's context before
// its task files are written; until it is set, nothing is asked and the
// task files hold no context, as before context existed.
func (e *Engine) SetContext(f ContextFunc) {
	e.mu.Lock()
	e.contextOf = f
	e.mu.Unlock()
}

// askContext asks the engine's context function for a's context, the
// query read under e.mu and the function called outside it, so a provider
// that takes its time holds no other run, task or request of the engine's;
// nil when none is set. The blocks are a view a provider derived as the
// attempt starts: they go into its task files and nowhere else, never the
// ledger, and an attempt neither fails nor changes state for them. An
// attempt of a comparison asks nothing: a block cannot show what it was
// derived from, so none is given to a lane (its task file says so).
func (e *Engine) askContext(ctx context.Context, r *run, a *attempt) []TaskContext {
	e.mu.Lock()
	f := e.contextOf
	if r.compare != nil {
		f = nil
	}
	var q ext.ContextQuery
	if f != nil {
		q = contextQuery(r, a)
	}
	e.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(ctx, q)
}

// contextQuery is a's attempt as a context provider is asked about it: its
// run, task and number, the crew's goal and the task's instructions, where
// it works and the commit it starts at, and the results it starts from.
// The caller holds e.mu.
func contextQuery(r *run, a *attempt) ext.ContextQuery {
	t := a.t
	q := ext.ContextQuery{Run: r.id, Crew: r.crewID, Task: t.def.ID, Attempt: a.n, Kind: t.def.Kind, Goal: r.goal, Instructions: t.def.Instructions,
		Worktree: a.worktree, Dir: filepath.Join(a.worktree, r.prefix), Start: a.start}
	for _, in := range a.inputs {
		q.Inputs = append(q.Inputs, ext.ContextInput{Task: in.task, Commit: in.commit})
	}
	return q
}

// HandoffQuery is an attempt about to have its task file written, as the
// handoff function is asked about it: its run, the run's chain (the run
// and the runs it continues, newest first), its task and its number.
type HandoffQuery struct {
	Run     string
	Chain   []string
	Task    string
	Attempt int
}

// HandoffFunc answers the handoff package a person sent for the attempt
// q names, nil for none (issue #1012): the server's, which takes the
// waiting record from Brakeman's records, the take written before it
// answers, so a package is handed to one attempt (internal/api,
// brakeman_handoff.go). It never fails the attempt: no package is an
// answer. It is called outside the engine's lock.
type HandoffFunc func(ctx context.Context, q HandoffQuery) *TaskHandoff

// TaskHandoff is a handoff package a person sent for this task, as its
// attempt's task file holds it: the record's id, the name the sender gave
// ("" for none), when it was sent, the brief and its item it was made
// from, and its text, which Brakeman wrote and the person read before
// sending. Text is absent in task.json when it was cut to fit
// (task-more.json then holds it).
type TaskHandoff struct {
	ID     string    `json:"id"`
	By     string    `json:"by,omitempty"`
	SentAt time.Time `json:"sentAt"`
	Brief  string    `json:"brief"`
	Item   int       `json:"item"`
	Text   string    `json:"text,omitempty"`
}

// The shapes of a handed package's ids: a record's (ho-<n>) and a brief's
// (br-<n>), each as Brakeman's records make them.
var (
	handoffIDPattern = regexp.MustCompile(`^ho-[1-9][0-9]{0,17}$`)
	briefIDPattern   = regexp.MustCompile(`^br-[1-9][0-9]{0,17}$`)
)

// MaxHandoffBy is the sender's name as a task file takes it, in bytes: a
// display name, as Dispatch's actor and Brakeman's by.
const MaxHandoffBy = 60

// MaxHandoffItem bounds the item's index (Brakeman's MaxItems is less).
const MaxHandoffItem = 1000

// MaxHandoffText is the text as a task file takes it, in bytes; the
// server sends at most 16 KiB (brakeman.MaxHandoffText), which is less.
const MaxHandoffText = 16 << 10

// HandoffByOK reports whether by is a name the task file writes as it is:
// at most MaxHandoffBy bytes, one plain line (quoted.Plain), or empty.
// The send route refuses every other name, so the writer never meets one.
func HandoffByOK(by string) bool {
	return len(by) <= MaxHandoffBy && quoted.Plain(by)
}

// Valid reports whether h is a package a task file writes: its ids of
// their shapes, its item in bounds, its name one HandoffByOK takes, its
// text within MaxHandoffText and not empty. The engine hands the writer
// no other; the server's take checks the same before it writes the take.
func (h TaskHandoff) Valid() bool {
	return handoffIDPattern.MatchString(h.ID) && briefIDPattern.MatchString(h.Brief) && h.Item >= 0 && h.Item < MaxHandoffItem &&
		HandoffByOK(h.By) && h.Text != "" && len(h.Text) <= MaxHandoffText && !h.SentAt.IsZero()
}

// SetHandoff makes f what the engine asks for an attempt's handoff
// package just before its task file is written; until it is set, nothing
// is asked and no task file holds one.
func (e *Engine) SetHandoff(f HandoffFunc) {
	e.mu.Lock()
	e.handoffOf = f
	e.mu.Unlock()
}

// askHandoff asks the engine's handoff function for a's package, the
// query read under e.mu and the function called outside it; nil when none
// is set, for an attempt of a comparison (its lanes are handed alike), or
// when the answer is not one a task file writes. It is asked once an
// attempt, after the task's decisions are read and immediately before the
// task file is written: the take uses the package up, and the attempts
// that close before their agent starts close after this point only by the
// task file, the manifest's phases or the launch.
func (e *Engine) askHandoff(ctx context.Context, r *run, a *attempt) *TaskHandoff {
	e.mu.Lock()
	f := e.handoffOf
	// A comparison's lanes are handed alike; an attempt already closed,
	// or whose dispatch is cancelled (a stop, a cancel), would use the
	// package up with no agent to read it: none of them asks.
	if r.compare != nil || r.lanes != nil || !a.closedAt.IsZero() || ctx.Err() != nil {
		f = nil
	}
	var q HandoffQuery
	if f != nil {
		chain := slices.Clone(r.dec.chain)
		if len(chain) == 0 {
			chain = []string{r.id}
		}
		q = HandoffQuery{Run: r.id, Chain: chain, Task: a.t.def.ID, Attempt: a.n}
	}
	e.mu.Unlock()
	if f == nil {
		return nil
	}
	h := f(ctx, q)
	if h == nil || !h.Valid() {
		return nil
	}
	c := *h
	return &c
}
