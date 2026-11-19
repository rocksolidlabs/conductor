package crew

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
// it works and the commit it starts at, the results it starts from (each
// with the attempt, start, verdict and findings it came from), the run's
// base, the ticket and plan revision the run was launched from, the
// task's checks, the evidence the attempt rests on and who launched the
// run. Each is bounded as ext.ContextQuery.Valid takes it. The caller
// holds e.mu.
func contextQuery(r *run, a *attempt) ext.ContextQuery {
	t := a.t
	q := ext.ContextQuery{Run: r.id, Crew: r.crewID, Task: t.def.ID, Attempt: a.n, Kind: t.def.Kind, Goal: r.goal, Instructions: t.def.Instructions,
		Worktree: a.worktree, Dir: filepath.Join(a.worktree, r.prefix), Start: a.start, Base: r.base, LaunchedBy: launchedBy(r)}
	for _, in := range a.inputs {
		ci := ext.ContextInput{Task: in.task, Commit: in.commit, Attempt: in.attempt, Verdict: in.verdict}
		if need := r.task(in.task); need != nil {
			ci.Start, ci.Findings = inputFacts(need, in)
		}
		q.Inputs = append(q.Inputs, ci)
	}
	for _, c := range t.def.Checks[:min(len(t.def.Checks), ext.MaxContextChecks)] {
		cc := ext.ContextCheck{ID: c.ID}
		if len(c.Argv) > ext.MaxContextArgv || slices.ContainsFunc(c.Argv, func(a string) bool { return !ext.ArgOK(a) }) {
			cc.Cut = true
		} else {
			cc.Argv = slices.Clone(c.Argv)
		}
		q.Checks = append(q.Checks, cc)
	}
	q.Ticket = contextTicket(r.pkg)
	q.Evidence = contextEvidence(r, a)
	return q
}

// inputFacts is where the result in of the task need started and its
// findings, each message cut to ext.MaxContextFinding bytes. The caller
// holds e.mu.
func inputFacts(need *task, in input) (start string, findings []ext.ContextFinding) {
	for _, o := range need.attempts {
		if o.n == in.attempt {
			start = o.start
		}
	}
	res := need.result
	if res == nil || res.attempt != in.attempt {
		return start, nil
	}
	if start == "" {
		start = res.reviewed
	}
	for _, f := range res.findings[:min(len(res.findings), ext.MaxContextFindings)] {
		findings = append(findings, ext.ContextFinding{Severity: f.Severity, Path: f.Path, Line: f.Line, Message: cutUTF8(f.Message, ext.MaxContextFinding)})
	}
	return start, findings
}

// contextTicket is the ticket and plan revision of the launch package p,
// nil for none: its people kept only when each is a name the query takes.
func contextTicket(p *runPackage) *ext.ContextTicket {
	if p == nil {
		return nil
	}
	tk := p.p.Ticket
	ct := &ext.ContextTicket{Key: tk.Key, Rev: tk.Rev, Plan: p.p.Plan.ID, PlanN: p.p.Plan.N, Digest: p.digest}
	if personOK(tk.Owner) {
		ct.Owner = tk.Owner
	}
	for _, a := range tk.Assignees {
		if personOK(a) && len(ct.Assignees) < ext.MaxContextAssignees {
			ct.Assignees = append(ct.Assignees, a)
		}
	}
	return ct
}

// personOK reports whether s is a person's name a query hands: one line
// of 1 to ext.MaxContextPerson characters.
func personOK(s string) bool {
	return s != "" && utf8.RuneCountInString(s) <= ext.MaxContextPerson && quoted.Plain(s)
}

// launchedBy is who launched r, as its launch package names them, else
// the provider whose launch it was; "" when the run keeps neither.
func launchedBy(r *run) string {
	if r.pkg != nil && personOK(r.pkg.p.Launch.By) {
		return r.pkg.p.Launch.By
	}
	if r.origin != nil && personOK(r.origin.Provider) {
		return r.origin.Provider
	}
	return ""
}

// contextEvidence is what a rests on as evidence refs: each input's
// candidate, the receipts of the checks of that candidate's attempt (by
// id, then by run), the ticket and the plan revision; at most
// ext.MaxContextEvidence, in that order. The caller holds e.mu.
func contextEvidence(r *run, a *attempt) []string {
	var refs []string
	add := func(ref string) {
		if len(refs) < ext.MaxContextEvidence && len(ref) <= ext.MaxContextEvidenceRef {
			refs = append(refs, ref)
		}
	}
	type held struct {
		task string
		o    *attempt
	}
	var attempts []held
	for _, in := range a.inputs {
		if in.attempt < 1 {
			continue
		}
		run := r.id
		need := r.task(in.task)
		if need != nil && need.imported != "" {
			run, _, _ = strings.Cut(need.imported, "/")
		}
		add(fmt.Sprintf("candidate:%s/%s/%d", run, in.task, in.attempt))
		if need == nil || run != r.id {
			continue
		}
		for _, o := range need.attempts {
			if o.n == in.attempt {
				attempts = append(attempts, held{in.task, o})
			}
		}
	}
	for _, h := range attempts {
		for _, id := range slices.Sorted(maps.Keys(h.o.ks)) {
			for k := 1; k <= h.o.ks[id]; k++ {
				add(fmt.Sprintf("check:%s/%s/%d/%s/%d", r.id, h.task, h.o.n, id, k))
			}
		}
	}
	if r.pkg != nil {
		add("ticket:" + r.pkg.p.Ticket.Key)
		add(fmt.Sprintf("plan:%s/%d", r.pkg.p.Ticket.Key, r.pkg.p.Plan.N))
	}
	return refs
}

// cutUTF8 is s cut to at most n bytes, at a character's start.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// SetCommitNotes makes f what the engine tells of a run's base, after its
// first transaction (a continuation's too) commits, and of each candidate
// a work task's attempt submits, after its submit commits; until it is
// set, nothing is told. f is called outside the engine's lock and must
// never wait (the server's queues each note).
func (e *Engine) SetCommitNotes(f func(ext.CommitNote)) {
	e.mu.Lock()
	e.commitNotes = f
	e.mu.Unlock()
}

// noteCommit tells f of n when f is set and n is a valid note.
func noteCommit(f func(ext.CommitNote), n ext.CommitNote) {
	if f != nil && n.Valid() == nil {
		f(n)
	}
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

// LookupScope is what a lookup an attempt's agent asks is about, as the
// engine holds it: the run, the task, the attempt's number and kind, the
// commit it started at, the run's base and the attempt's worktree. The
// server fills a lookup's request from it, never from what the agent
// sends (docs/plans/context-seams.md, CT-2).
type LookupScope struct {
	Run      string
	Task     string
	Attempt  int
	Kind     string
	Start    string
	Base     string
	Worktree string
}

// LookupScope answers the scope of a lookup the session sessionID asks:
// ErrNotAnAttempt for a session that is no attempt's, ErrNotRunning for
// an attempt that has closed or has no worktree yet.
func (e *Engine) LookupScope(sessionID string) (LookupScope, error) {
	v, ok := e.bySession.Load(sessionID)
	if !ok {
		return LookupScope{}, ErrNotAnAttempt
	}
	sm := v.(sessionMember)
	if sm.a == nil {
		return LookupScope{}, ErrNotAnAttempt
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a := sm.a
	switch {
	case !a.closedAt.IsZero():
		return LookupScope{}, fmt.Errorf("%w: the attempt has closed", ErrNotRunning)
	case a.worktree == "":
		return LookupScope{}, fmt.Errorf("%w: the attempt has no worktree yet", ErrNotRunning)
	}
	return LookupScope{Run: sm.r.id, Task: a.t.def.ID, Attempt: a.n, Kind: a.t.def.Kind, Start: a.start, Base: sm.r.base, Worktree: a.worktree}, nil
}
