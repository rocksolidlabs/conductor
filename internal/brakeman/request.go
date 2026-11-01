package brakeman

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Schema is the schema of the records this build writes: a request, the
// store's state. One of a later schema is skipped, logged once and never
// rewritten.
const Schema = 1

// Request is one thing a person asked of Brakeman, kept as
// brakeman/requests/br-<n>.json (docs/plans/brakeman.md, A request). Its
// brief, the brief's resolutions, the turn's read log and a pending
// make's input are documents of their own beside it, so it stays small.
type Request struct {
	Schema    int       `json:"schema"`
	ID        string    `json:"id"`   // br-<n>, n from 1, never reused
	OpID      string    `json:"opId"` // the client's, the repeat key
	Kind      string    `json:"kind"`
	Subjects  []Ref     `json:"subjects,omitempty"` // at most MaxSubjects
	Note      string    `json:"note,omitempty"`     // Markdown, at most MaxNote bytes
	Since     time.Time `json:"since,omitzero"`     // a briefing's start
	By        string    `json:"by,omitempty"`       // the person's display name
	CreatedAt time.Time `json:"createdAt"`
	State     string    `json:"state"`
	Why       string    `json:"why,omitempty"` // a refusal's or an end's words, at most MaxWhy bytes
	RunID     string    `json:"runId,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Agent     Stamp     `json:"agent"`
	Config    Snapshot  `json:"config"` // as the turn began
	Deadline  time.Time `json:"deadline,omitzero"`
	Reads     int       `json:"reads"`
	HasBrief  bool      `json:"hasBrief,omitempty"` // br-<n>-brief.json and br-<n>-refs.json are kept
	BriefOpID string    `json:"briefOpId,omitempty"`
	BriefAt   time.Time `json:"briefAt,omitzero"`
	// Footprint is the classes its turn's reads used; with Config's scope,
	// what an earlier brief carries (Scope.Covers).
	Footprint *Footprint `json:"footprint,omitempty"`
	Made      []Made     `json:"made,omitempty"`     // drafts made work by a person
	Edges     []Edge     `json:"edges,omitempty"`    // draft tickets' blocks
	Acts      []Act      `json:"acts,omitempty"`     // BK2's acts; none in this build
	StrayRun  string     `json:"strayRun,omitempty"` // a run its launch returned after it closed, stopped then
	Coverage  *Coverage  `json:"coverage,omitempty"` // the server's watermarks of what the turn read
	ClosedAt  time.Time  `json:"closedAt,omitzero"`
}

// Stamp is the agent a turn ran: its id, its adapter, the probed version,
// the model, and whether an account was set.
type Stamp struct {
	AgentID string `json:"agentId,omitempty"`
	Adapter string `json:"adapter,omitempty"`
	Version string `json:"version,omitempty"`
	Model   string `json:"model,omitempty"`
	Account bool   `json:"account,omitempty"`
}

// Snapshot is the configuration a turn began under: what its reads may
// use (intersected with the configuration now) and what its footprint is
// read against.
type Snapshot struct {
	Visibility  Visibility  `json:"visibility"`
	Scope       Scope       `json:"scope"`
	Permissions Permissions `json:"permissions"`
	Budget      Budget      `json:"budget"`
	Rev         int64       `json:"rev"`
}

// SnapshotOf is the snapshot of c.
func SnapshotOf(c Config) Snapshot {
	return Snapshot{Visibility: c.Visibility, Scope: c.Scope, Permissions: c.Permissions, Budget: c.Budget, Rev: c.Rev}
}

// Footprint is the visibility classes a turn's reads used, from the
// server's read log; a log that dropped entries counts every class its
// snapshot had on.
type Footprint struct {
	Classes []string `json:"classes"`
}

// Coverage is how far the pages of changes a turn was given reach, the
// server's record, never the agent's word (PR-4 fills it).
type Coverage struct {
	Runs     []Watermark `json:"runs,omitempty"`     // at most MaxWatermarks
	Sessions []Watermark `json:"sessions,omitempty"` // at most MaxWatermarks
	Dispatch *Watermark  `json:"dispatch,omitempty"`
}

// Watermark is one part's reach: a run's last event seq delivered with
// nothing skipped before it, Dispatch's change seq, a session's window.
type Watermark struct {
	ID      string    `json:"id,omitempty"` // a run's or a session's id
	Seq     int64     `json:"seq,omitempty"`
	At      time.Time `json:"at,omitzero"`
	Partial bool      `json:"partial,omitempty"`
	New     bool      `json:"new,omitempty"` // newly in scope
}

// MaxWatermarks bounds each of a Coverage's lists.
const MaxWatermarks = 64

// maxID bounds a run's or a session's id where a request names one: a
// run id is at most 73 bytes.
const maxID = 80

// Made is a draft made work by a person, or its claim: the item, the
// person's op id, the op id derived from it that the backend keeps, the
// SHA-256 of the input as the person edited it, then the outcome.
type Made struct {
	Item  int       `json:"item"`
	OpID  string    `json:"opId"`
	Op    string    `json:"op"`
	Input string    `json:"input"` // hex SHA-256 of the canonical input
	State string    `json:"state"` // MadePending, MadeDone, MadeRefused, MadeDeleted
	By    string    `json:"by,omitempty"`
	Key   string    `json:"key,omitempty"`  // a ticket made: its key
	Crew  string    `json:"crew,omitempty"` // a crew made: its id
	Rev   int64     `json:"rev,omitempty"`
	Why   string    `json:"why,omitempty"` // a refusal's words, at most MaxWhy bytes
	At    time.Time `json:"at"`
}

// The states of a Made entry. Only a pending one pins its request.
const (
	MadePending = "pending"
	MadeDone    = "made"
	MadeRefused = "refused"
	MadeDeleted = "deleted"
)

// Edge is a draft ticket's block of another, made a relation once both
// ends are tickets.
type Edge struct {
	From  int    `json:"from"`
	To    int    `json:"to"`
	Op    string `json:"op"`
	State string `json:"state"` // EdgePending, EdgeMade
}

// The states of an Edge.
const (
	EdgePending = "pending"
	EdgeMade    = "made"
)

// Act is one of BK2's acts (PR-12), written pending before its operation
// runs. No act is taken in this build: a request's Acts stays empty.
type Act struct {
	OpID      string    `json:"opId"`
	Operation string    `json:"operation"`
	Target    Ref       `json:"target"`
	Run       string    `json:"run,omitempty"`
	Op        string    `json:"op"`
	Args      string    `json:"args"`  // hex SHA-256 of the arguments
	State     string    `json:"state"` // pending, done, refused, not_run
	Receipt   string    `json:"receipt,omitempty"`
	Why       string    `json:"why,omitempty"`
	At        time.Time `json:"at"`
}

// The kinds of a request.
const (
	KindBriefing     = "briefing"
	KindExplain      = "explain"
	KindDecision     = "decision"
	KindDraftTickets = "draft_tickets"
	KindProposeCrew  = "propose_crew"
	KindHandoff      = "handoff"
	KindLocate       = "locate"
	KindOverlaps     = "overlaps"
	KindAsk          = "ask"
)

// Kinds is every kind a request may have in this build (wake arrives with
// BK3's role).
var Kinds = []string{KindBriefing, KindExplain, KindDecision, KindDraftTickets, KindProposeCrew, KindHandoff, KindLocate, KindOverlaps, KindAsk}

// The states of a request (docs/plans/brakeman.md, Events, orderings and
// invariants).
const (
	StateQueued      = "queued"
	StateStarting    = "starting"
	StateRunning     = "running"
	StateBriefed     = "briefed"
	StateTimedOut    = "timed_out"
	StateEnded       = "ended"
	StateFailed      = "failed"
	StateOverBudget  = "over_budget"
	StateCancelled   = "cancelled"
	StateInterrupted = "interrupted"
)

// States is every state, open ones first.
var States = []string{StateQueued, StateStarting, StateRunning, StateBriefed, StateTimedOut, StateEnded, StateFailed, StateOverBudget, StateCancelled, StateInterrupted}

// transitions is the table of the plan: from each state, where it may go.
var transitions = map[string][]string{
	StateQueued:   {StateStarting, StateCancelled, StateOverBudget},
	StateStarting: {StateRunning, StateBriefed, StateEnded, StateFailed, StateTimedOut, StateCancelled, StateInterrupted},
	StateRunning:  {StateBriefed, StateTimedOut, StateEnded, StateFailed, StateCancelled, StateInterrupted},
}

// Closed reports whether a state is one a request never leaves.
func Closed(state string) bool {
	return slices.Contains(States, state) && state != StateQueued && state != StateStarting && state != StateRunning
}

// Open reports whether the request is not closed.
func (r *Request) Open() bool { return !Closed(r.State) }

// ErrTransition is what To's error wraps: a change of state the table
// does not list.
var ErrTransition = errors.New("brakeman: a transition the request's states do not allow")

// To moves the request to state, with why (at most MaxWhy bytes, cleaned)
// and, when state is closed, closedAt at. Every transition the table does
// not list is refused, a request's own state among them.
func (r *Request) To(state, why string, at time.Time) error {
	if !slices.Contains(transitions[r.State], state) {
		return fmt.Errorf("%w: %s to %s", ErrTransition, r.State, state)
	}
	why = cleanText(why)
	if len(why) > MaxWhy {
		return &Invalid{Code: CodeInvalidRequest, Field: "why", Why: fmt.Sprintf("at most %d bytes", MaxWhy)}
	}
	r.State, r.Why = state, why
	if Closed(state) {
		r.ClosedAt = at.UTC()
	}
	return nil
}

// A request's bounds.
const (
	MaxSubjects = 10
	MaxNote     = 16 << 10
	MaxWhy      = 500
	MaxBy       = 60 // a display name, as Dispatch's actor
	MaxMade     = MaxItems
	MaxEdges    = 190 // 20 draft tickets without a cycle
	MaxActs     = 20
	// Each entry of the lists, as the request's encoding writes it.
	MaxMadeBytes = 600
	MaxEdgeBytes = 300
	MaxActBytes  = 600
	// MaxRequestRest bounds the request without its three lists, as
	// written; with the lists at their largest the whole stays under
	// MaxRequestDoc, so a request's later updates always fit.
	MaxRequestRest = 96 << 10
	MaxRequestDoc  = 256 << 10
)

// subjectKinds is the ref kinds each request kind takes as subjects, and
// how many: at least min, at most max.
var subjectKinds = map[string]struct {
	kinds    []string
	min, max int
	note     bool // the note is required
}{
	KindBriefing:     {nil, 0, 0, false},
	KindExplain:      {[]string{RefRun, RefTask, RefAttempt, RefSession, RefQuestion, RefTicket}, 1, 1, false},
	KindDecision:     {[]string{RefQuestion, RefTask, RefTicket}, 1, 1, false},
	KindDraftTickets: {[]string{RefTicket, RefComment}, 0, 1, false},
	KindProposeCrew:  {[]string{RefTicket}, 1, 1, false},
	KindHandoff:      {[]string{RefTask, RefAttempt, RefSession}, 1, 1, false},
	KindLocate:       {nil, 0, 0, true},
	KindOverlaps:     {nil, 0, 0, false},
	KindAsk:          {nil, 0, MaxSubjects, true},
}

func invalidRequest(field, why string, a ...any) error {
	return &Invalid{Code: CodeInvalidRequest, Field: field, Why: fmt.Sprintf(why, a...)}
}

// checkAsked holds what a person asks to its kind: the kind known, its
// subjects of the kinds it takes and as many, the note within MaxNote
// (and given where the kind reads it), since only for a briefing, the op
// id and the display name within their bounds. It cleans the note and the
// name in place.
func (r *Request) checkAsked() error {
	if !opIDPattern.MatchString(r.OpID) {
		return invalidRequest("opId", "must match %s", opIDPattern)
	}
	sk, ok := subjectKinds[r.Kind]
	if !ok {
		return invalidRequest("kind", "one of %s", strings.Join(Kinds, ", "))
	}
	r.Note = cleanText(r.Note)
	r.By = cleanText(r.By)
	switch {
	case len(r.Subjects) < sk.min || len(r.Subjects) > sk.max:
		if sk.min == sk.max {
			return invalidRequest("subjects", "a %s request names %d", r.Kind, sk.min)
		}
		return invalidRequest("subjects", "a %s request names %d to %d", r.Kind, sk.min, sk.max)
	case len(r.Note) > MaxNote:
		return invalidRequest("note", "at most %d bytes", MaxNote)
	case sk.note && r.Note == "":
		return invalidRequest("note", "a %s request is its note", r.Kind)
	case r.Kind == KindDraftTickets && len(r.Subjects) == 0 && r.Note == "":
		return invalidRequest("note", "a draft_tickets request names a ticket or a comment, or its note is the discussion")
	case !r.Since.IsZero() && r.Kind != KindBriefing:
		return invalidRequest("since", "only a briefing starts at a time")
	case len(r.By) > MaxBy || strings.ContainsAny(r.By, "\n\t"):
		return invalidRequest("by", "one line of at most %d bytes", MaxBy)
	}
	for i, s := range r.Subjects {
		if sk.kinds != nil && !slices.Contains(sk.kinds, s.Kind) {
			return invalidRequest(fmt.Sprintf("subjects[%d]", i), "a %s request names %s", r.Kind, strings.Join(sk.kinds, ", "))
		}
		if s.IsZero() {
			return invalidRequest(fmt.Sprintf("subjects[%d]", i), "no ref")
		}
		if err := s.Valid(); err != nil {
			return invalidRequest(fmt.Sprintf("subjects[%d]", i), "%v", err)
		}
		if slices.Contains(r.Subjects[:i], s) {
			return invalidRequest(fmt.Sprintf("subjects[%d]", i), "%s is named twice", s)
		}
	}
	return nil
}

// sameAsk reports whether two requests ask the same: an op id's repeat.
func sameAsk(a, b Request) bool {
	return a.Kind == b.Kind && slices.Equal(a.Subjects, b.Subjects) && a.Note == b.Note && a.Since.Equal(b.Since)
}

// checkEntries holds the request's lists to their bounds and the rest of
// it to its fields' bounds; the document's size is measured apart
// (encodeRequest).
func (r *Request) checkEntries() error {
	switch {
	case len(r.Made) > MaxMade:
		return fmt.Errorf("%w: at most %d made entries", ErrBound, MaxMade)
	case len(r.Edges) > MaxEdges:
		return fmt.Errorf("%w: at most %d edges", ErrBound, MaxEdges)
	case len(r.Acts) > MaxActs:
		return fmt.Errorf("%w: at most %d acts", ErrBound, MaxActs)
	case len(r.Why) > MaxWhy || !utf8.ValidString(r.Why):
		return fmt.Errorf("%w: why at most %d bytes", ErrBound, MaxWhy)
	}
	if c := r.Coverage; c != nil {
		if len(c.Runs) > MaxWatermarks || len(c.Sessions) > MaxWatermarks {
			return fmt.Errorf("%w: coverage at most %d runs and %d sessions", ErrBound, MaxWatermarks, MaxWatermarks)
		}
		for _, w := range slices.Concat(c.Runs, c.Sessions) {
			if len(w.ID) > maxID {
				return fmt.Errorf("%w: a watermark's id at most %d bytes", ErrBound, maxID)
			}
		}
	}
	a := r.Agent
	if len(a.AgentID) > 32 || len(a.Adapter) > 32 || len(a.Version) > 128 || len(a.Model) > 100 {
		return fmt.Errorf("%w: the agent's stamp: an id and an adapter of at most 32 bytes, a version of 128, a model of 100", ErrBound)
	}
	for _, id := range []string{r.RunID, r.SessionID, r.StrayRun} {
		if len(id) > maxID {
			return fmt.Errorf("%w: a run's or a session's id at most %d bytes", ErrBound, maxID)
		}
	}
	if f := r.Footprint; f != nil {
		for _, c := range f.Classes {
			if !slices.Contains(Classes, c) {
				return fmt.Errorf("%w: a footprint names the visibility classes, not %q", ErrBound, c)
			}
		}
		if len(f.Classes) > len(Classes) {
			return fmt.Errorf("%w: a footprint names each class once", ErrBound)
		}
	}
	for i, m := range r.Made {
		if n := entrySize(m); n > MaxMadeBytes {
			return fmt.Errorf("%w: made[%d] is %d bytes, at most %d", ErrBound, i, n, MaxMadeBytes)
		}
		if slices.ContainsFunc(r.Made[:i], func(o Made) bool { return o.Item == m.Item }) {
			return fmt.Errorf("%w: made[%d]: item %d has an entry already", ErrBound, i, m.Item)
		}
	}
	for i, e := range r.Edges {
		if n := entrySize(e); n > MaxEdgeBytes {
			return fmt.Errorf("%w: edges[%d] is %d bytes, at most %d", ErrBound, i, n, MaxEdgeBytes)
		}
	}
	for i, a := range r.Acts {
		if n := entrySize(a); n > MaxActBytes {
			return fmt.Errorf("%w: acts[%d] is %d bytes, at most %d", ErrBound, i, n, MaxActBytes)
		}
	}
	return nil
}

// closedChange checks a change to a closed request: nothing changes but
// its made list, its edges, its acts' outcomes (no new act), its stray
// run, and its run and session ids while they are empty.
func closedChange(before, after Request) error {
	if len(after.Acts) != len(before.Acts) {
		return fmt.Errorf("%w: a closed request takes no new act", ErrClosed)
	}
	for i := range after.Acts {
		a, b := after.Acts[i], before.Acts[i]
		a.State, a.Receipt, a.Why, a.At = b.State, b.Receipt, b.Why, b.At
		if a != b {
			return fmt.Errorf("%w: acts[%d] changes only its outcome", ErrClosed, i)
		}
	}
	if before.RunID != "" && after.RunID != before.RunID {
		return fmt.Errorf("%w: its run is %s", ErrClosed, before.RunID)
	}
	if before.SessionID != "" && after.SessionID != before.SessionID {
		return fmt.Errorf("%w: its session is %s", ErrClosed, before.SessionID)
	}
	if before.StrayRun != "" && after.StrayRun != before.StrayRun {
		return fmt.Errorf("%w: its stray run is %s", ErrClosed, before.StrayRun)
	}
	a, b := after, before
	a.Made, a.Edges, a.Acts, a.StrayRun, a.RunID, a.SessionID = nil, nil, nil, "", "", ""
	b.Made, b.Edges, b.Acts, b.StrayRun, b.RunID, b.SessionID = nil, nil, nil, "", "", ""
	ab, err1 := encode(a)
	bb, err2 := encode(b)
	if err1 != nil || err2 != nil || string(ab) != string(bb) {
		return fmt.Errorf("%w: only its made list, its edges, its acts' outcomes, its stray run and an empty run or session change", ErrClosed)
	}
	return nil
}
