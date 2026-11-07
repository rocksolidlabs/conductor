package crew

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// A role (docs/plans/brakeman.md, BK3, PR-14; Owner decisions 2, O2): a
// dedicated loop over ordinary bounded turns, in Yardmaster's engine. A
// role is a record, not a process: an identity, its durable notes, its
// log of wakes and its lifecycle, idle → gathering (a wake source fired,
// the batch window open) → briefing (the batch's turn running) → cooling
// (until the cooldown ends) → idle, with paused (by a person) and
// unavailable (a turn could not start) beside them. Each change is
// written (RoleRecords.SaveRole) before what it starts: a batch whose
// window ended is frozen as the one firing (Firing) and written before
// its request is made, so no wake joins a batch whose turn may have read
// it, and the request's id is written after (Fire, which answers the
// request a batch's id already made), so a crash or a failed write
// between the two fires the batch once.
//
// The loop knows the lifecycle, the wakes, the window, the cooldown and
// the briefing times; it asks RoleRecords for its records, its settings,
// a turn (Fire) and what became of it (Outcome). Brakeman's desk in
// internal/api implements RoleRecords over internal/brakeman's store, so
// this package imports neither. The loop holds no process between turns
// and polls no model: it steps when a source wakes it (Wake, Hold,
// Poke) and at the one moment something falls due on its own (a window's
// end, a cooldown's, a briefing time, a held wake), and a turn is an
// ordinary run its owner launches.
//
// The role judges every outcome by the turn it belongs to, never by the
// order the outcomes reach it. Turns are numbered in the order their
// requests were made (the role's own and a person's alike); an outcome
// carries its turn's number (RoleOutcome.Turn), a start too (Started),
// and the role records the latest turn it knows to have started
// (RoleRecord.LastStarted). A failure of turn N changes the role's state
// only if no later turn has started since: an older turn's outcome that
// reaches the role late is that turn's own (its request keeps it), and
// changes nothing of the role. A person's turn whose agent read its
// request while the role's own failed turn waited to be taken up leaves
// the role available, the failed turn's batch kept for the next.

// The role's states.
const (
	RoleIdle        = "idle"
	RoleGathering   = "gathering"
	RoleBriefing    = "briefing"
	RoleCooling     = "cooling"
	RolePaused      = "paused"
	RoleUnavailable = "unavailable"
)

// RoleStates is every state a role may be in.
var RoleStates = []string{RoleIdle, RoleGathering, RoleBriefing, RoleCooling, RolePaused, RoleUnavailable}

// The wake sources: a task that failed with proof, a question open past
// its threshold, a run that ended or paused, a ticket moved to ready, a
// person's own request, and a configured briefing time.
const (
	WakeTaskFailed   = "task_failed"
	WakeQuestionOpen = "question_open"
	WakeRunEnded     = "run_ended"
	WakeRunPaused    = "run_paused"
	WakeTicketReady  = "ticket_ready"
	WakeRequest      = "request"
	WakeBriefing     = "briefing"
)

// WakeSources is every source, in the order the pages list them.
var WakeSources = []string{WakeTaskFailed, WakeQuestionOpen, WakeRunEnded, WakeRunPaused, WakeTicketReady, WakeRequest, WakeBriefing}

// The role's bounds.
const (
	RoleSchema = 1
	// MaxRoleLog is the wakes the log keeps, the oldest dropped first.
	MaxRoleLog = 1000
	// MaxBatchWakes is a batch's wakes, the same source and subject
	// coalesced with a count; past it the batch counts an overflow.
	MaxBatchWakes = 200
	// MaxRoleNotes bounds the notes' text together, MaxRoleNote one's.
	MaxRoleNotes = 64 << 10
	MaxRoleNote  = 4 << 10
	// maxWakeField bounds a wake's subject, maxWakeKey its key, maxRoleWhy
	// the role's words.
	maxWakeField = 512
	maxWakeKey   = 64
	maxRoleWhy   = 500
	// MaxHeld bounds the wakes held for a time of their own (Hold).
	MaxHeld = 1000
	// RoleRetry is how soon a step that could not read or write its
	// records, or start a turn for want of its store, is tried again.
	RoleRetry = 30 * time.Second
	// RoleStartRetry is how long a role whose turn's agent did not start
	// (ReasonStart) waits before it tries again by itself, doubled for
	// each such turn in a row (RoleRecord.Tries) up to RoleStartRetryMax:
	// an agent signed out, or held at a screen, is not fired into again
	// and again, and one signed in meanwhile is found within hours.
	RoleStartRetry    = 10 * time.Minute
	RoleStartRetryMax = 4 * time.Hour
)

// The reasons a role is unavailable, kept with its words (RoleRecord.Reason):
// what ends it besides its retry time (SaveEnds, Started).
const (
	// ReasonBudget is the day's turns or minutes spent.
	ReasonBudget = "budget"
	// ReasonStart is a turn whose agent did not start: its launch refused,
	// its agent exited or made no call to the server (signed out, held at
	// a screen). A turn of the agent that reads its request ends it
	// (Started), a person's own included.
	ReasonStart = "start"
	// ReasonOff is its owner off, or not running it in the background, as
	// the turn was asked.
	ReasonOff = "off"
	// ReasonStuck is the last turn's run, which did not stop.
	ReasonStuck = "stuck"
)

// RoleChange is what a save of the role's owner's configuration changed,
// told apart by what each can end (SaveEnds).
type RoleChange struct {
	// Budget is the day's turns or minutes, or a turn's.
	Budget bool
	// Start is what the agent starts with: the agent, its model, its
	// account, its folder, the tools it may call without asking.
	Start bool
	// On is the role turned on or off (its owner on, in the background).
	On bool
	// Other is anything else (what it may see, its grants, its wake's
	// window, cooldown or briefing times).
	Other bool
}

// SaveEnds reports whether a save that made the change ch ends an
// unavailability kept for reason: a spent budget by a budget changed; a
// turn whose agent did not start by a change of what the agent starts
// with (another agent, model, account or folder: the same one signed out
// would not start again) or by the role turned off or on (a person's own
// start again, #715); the role off by the role turned on or off; a
// run that did not stop by none (stopping it does). A save that changes
// nothing ends nothing. An unavailability kept before reasons were (no
// reason) ends at any change, as every save then ended it.
func SaveEnds(reason string, ch RoleChange) bool {
	switch reason {
	case ReasonBudget:
		return ch.Budget
	case ReasonStart:
		return ch.Start || ch.On
	case ReasonOff:
		return ch.On
	case ReasonStuck:
		return false
	}
	return ch.Budget || ch.Start || ch.On || ch.Other
}

// startRetry is how long the role waits, after the tries-th turn in a row
// whose agent did not start, before it tries again by itself.
func startRetry(tries int) time.Duration {
	d := RoleStartRetry
	for i := 1; i < tries && d < RoleStartRetryMax; i++ {
		d *= 2
	}
	return min(d, RoleStartRetryMax)
}

// ErrRoleLaterSchema is a role record of a later schema: kept as it is,
// and the role takes no step.
var ErrRoleLaterSchema = errors.New("the role's record is of a later schema, kept as it is")

// ErrRoleFired is what Fire answers for a batch whose request was made
// and is gone since (pruned): the batch fired, and the role cools.
var ErrRoleFired = errors.New("the batch's request was made and is gone")

// RoleWake is a wake source firing: its source, the ref of what it fired
// for ("task:<run>/<task>", "session:<id>"), a key that tells this firing
// from another of the same subject (a task's attempts, a question's
// time: a wake whose source, subject and key the log holds already is
// one fired before, after a restart too), and when.
type RoleWake struct {
	Source  string    `json:"source"`
	Subject string    `json:"subject,omitempty"`
	Key     string    `json:"key,omitempty"`
	At      time.Time `json:"at"`
}

// RoleLogRow is a wake as the role's log keeps it: the batch it joined
// and the request that batch fired, or why it joined none (Held: the role
// was paused; a person's own request, which is its own turn).
type RoleLogRow struct {
	RoleWake
	Batch   string `json:"batch,omitempty"`
	Request string `json:"request,omitempty"`
	Held    string `json:"held,omitempty"`
}

// BatchWake is a batch's wakes of one source and subject, coalesced.
type BatchWake struct {
	Source  string    `json:"source"`
	Subject string    `json:"subject,omitempty"`
	Count   int       `json:"count"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

// Overflow counts the wakes a batch had no room for, with the first's
// and the last's times.
type Overflow struct {
	Count int       `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

// Batch is the wakes one turn reads: those that fired in its window, or
// while the role was briefing, cooling or unavailable. Its id is wb-<n>,
// written to the role's record as it opens.
type Batch struct {
	ID       string      `json:"id"`
	OpenedAt time.Time   `json:"openedAt"`
	Wakes    []BatchWake `json:"wakes"`
	Overflow *Overflow   `json:"overflow,omitempty"`
}

// RoleNote is one of the role's durable notes: text a person chose to
// keep (a brief's fact or interpretation), with the ref it was kept from.
// Conductor keeps them; the role's turn never writes one.
type RoleNote struct {
	Text string    `json:"text"`
	From string    `json:"from,omitempty"`
	At   time.Time `json:"at"`
}

// RoleRecord is the role, as its records keep it.
type RoleRecord struct {
	Schema   int    `json:"schema"`
	Identity string `json:"identity"`
	State    string `json:"state"`
	// Why says why the role is unavailable or paused, in words; RetryAt
	// when an unavailable role tries again on its own (the next local day,
	// for a budget spent; a wait that doubles, for an agent that did not
	// start), zero for on a person's request or the next briefing; Reason
	// what made it unavailable (ReasonBudget, ReasonStart, ...), which
	// says what else ends it (SaveEnds, Started).
	Why     string    `json:"why,omitempty"`
	RetryAt time.Time `json:"retryAt,omitzero"`
	Reason  string    `json:"reason,omitempty"`
	// Tries counts the turns in a row whose agent did not start; a turn
	// whose agent reads its request sets it back to 0.
	Tries int `json:"tries,omitempty"`
	// LastStarted is the number of the latest turn whose agent read its
	// request (Started), the role's own or a person's: a failure of an
	// earlier turn, taken up after it, changes nothing of the role.
	LastStarted int64 `json:"lastStarted,omitempty"`
	// Last is the last batch number given; Open the batch gathering (or
	// waiting while the role briefs, cools or is unavailable); Fired the
	// last batch fired and Request its request while it runs. Firing says
	// Fired is frozen and its request not yet written: the role is
	// briefing, and its next step asks Fire for the request (made, or
	// made now), so the batch takes no wake from the moment its turn
	// could read it.
	Last    int64  `json:"last"`
	Open    *Batch `json:"open,omitempty"`
	Fired   *Batch `json:"fired,omitempty"`
	Firing  bool   `json:"firing,omitempty"`
	Request string `json:"request,omitempty"`
	// CoolUntil is when the cooldown after the last turn ends.
	CoolUntil time.Time `json:"coolUntil,omitzero"`
	// Briefed is the moment the briefing times were last looked at: a
	// briefing time after it and not after now is due. Zero while the role
	// is off or paused, and before its first look.
	Briefed   time.Time    `json:"briefed,omitzero"`
	Notes     []RoleNote   `json:"notes,omitempty"`
	Log       []RoleLogRow `json:"log,omitempty"`
	UpdatedAt time.Time    `json:"updatedAt,omitzero"`
}

// RoleSettings are what the role reads of its owner's configuration at
// each step: whether it runs (On), its window and cooldown, and its
// briefing times ("HH:MM") in Location.
type RoleSettings struct {
	On        bool
	Window    time.Duration
	Cooldown  time.Duration
	Briefings []string
	Location  *time.Location
}

// RoleOutcome is what became of a batch's request: still open, or closed;
// closed with the turn unable to start (its launch refused, its agent
// exited or never read its request, the day's budget spent), with why,
// its reason (ReasonStart, ReasonBudget) and when the role tries again on
// its own (zero: on a person's request or the next briefing; for
// ReasonStart, the role's own wait, startRetry). Turn is the request's
// number in the order requests are made (0 when it is not known): a
// failure of a turn earlier than one that started since (LastStarted) is
// that turn's own, and changes nothing of the role.
type RoleOutcome struct {
	Open        bool
	Unavailable string
	Reason      string
	RetryAt     time.Time
	Turn        int64
}

// RoleRefused is Fire's refusal of a turn that cannot start now (its
// owner off, the budget spent, the queue full): the role is unavailable,
// with Why and its Reason, until RetryAt (zero: a person's request or the
// next briefing).
type RoleRefused struct {
	Why     string
	Reason  string
	RetryAt time.Time
}

func (e *RoleRefused) Error() string { return e.Why }

// RoleRecords is what a role asks of its owner.
type RoleRecords interface {
	// LoadRole reads the role's record; ok false before one is written.
	LoadRole() (r RoleRecord, ok bool, err error)
	// SaveRole writes it.
	SaveRole(r RoleRecord) error
	// RoleSettings are the settings now; an error when they cannot be
	// read, which is never taken for the role turned off: the role then
	// changes nothing and tries again.
	RoleSettings() (RoleSettings, error)
	// Fire starts the batch's turn: a request made under an id the
	// batch's own (wake-<batch>), so a batch fired again (a crash between
	// the request and the role's write) answers the request it made. It
	// answers the request's id; a *RoleRefused for a turn that cannot
	// start, ErrRoleFired for a request made and gone.
	Fire(b Batch) (request string, err error)
	// Outcome answers what became of a request Fire made.
	Outcome(request string) (RoleOutcome, error)
}

// heldWake is a wake that fires at due if still holds then: a question
// open past its threshold. Kept in memory: a server's sessions do not
// outlive it.
type heldWake struct {
	w     RoleWake
	due   time.Time
	still func() bool
}

// Role is a role's loop.
type Role struct {
	identity string
	records  RoleRecords
	now      func() time.Time

	mu     sync.Mutex
	rec    RoleRecord
	loaded bool
	held   []heldWake

	poke  chan struct{}
	steps atomic.Uint64
	// again says the owner's sources hold what a pass could not take (a
	// wake whose write failed, settings that could not be read): the loop
	// passes again after retry, poked or not.
	again atomic.Bool
	retry time.Duration
}

// NewRole is the role of identity over records, on the clock now
// (time.Now when nil). It reads nothing until its first call.
func NewRole(identity string, records RoleRecords, now func() time.Time) *Role {
	if now == nil {
		now = time.Now
	}
	return &Role{identity: identity, records: records, now: now, poke: make(chan struct{}, 1), retry: RoleRetry}
}

// Again makes the loop pass again after RoleRetry though nothing pokes
// it: the owner's sources kept what this pass could not take.
func (r *Role) Again() { r.again.Store(true) }

// Poke makes the loop step again.
func (r *Role) Poke() {
	select {
	case r.poke <- struct{}{}:
	default:
	}
}

// Steps counts the steps taken, for a test to wait on one.
func (r *Role) Steps() uint64 { return r.steps.Load() }

// Run is the role's loop: before (the owner's sources drained into
// wakes; false, the owner takes no step now: it does not hold the
// records), then a step, then a wait for a poke, the step's next due
// time, or done. It holds no process and polls nothing between.
func (r *Role) Run(done <-chan struct{}, before func() bool) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		var next time.Duration
		if before == nil || before() {
			next, _ = r.Step()
		}
		r.steps.Add(1)
		if r.again.Swap(false) && (next <= 0 || next > r.retry) {
			next = r.retry
		}
		timer.Stop()
		var tick <-chan time.Time
		if next > 0 {
			timer.Reset(next)
			tick = timer.C
		}
		select {
		case <-done:
			return
		case <-r.poke:
		case <-tick:
		}
	}
}

// load reads the record once, until Forget. The caller holds mu.
func (r *Role) load() error {
	if r.loaded {
		return nil
	}
	rec, err := readRole(r.records, r.identity)
	if err != nil {
		return err
	}
	r.rec, r.loaded = rec, true
	return nil
}

// readRole reads the role of identity from records: idle before one is
// written, an unknown state read as idle, a later schema refused.
func readRole(records RoleRecords, identity string) (RoleRecord, error) {
	rec, ok, err := records.LoadRole()
	if err != nil {
		return RoleRecord{}, err
	}
	if !ok {
		rec = RoleRecord{Schema: RoleSchema, Identity: identity, State: RoleIdle}
	}
	if rec.Schema > RoleSchema {
		return RoleRecord{}, fmt.Errorf("%w: schema %d", ErrRoleLaterSchema, rec.Schema)
	}
	if !slices.Contains(RoleStates, rec.State) {
		rec.State = RoleIdle
	}
	rec.Schema, rec.Identity = RoleSchema, identity
	return rec, nil
}

// Forget drops the record read, so the next call reads the records
// again: they changed hands (a server that waited for the owner lock
// takes up the last owner's record, never the one it read meanwhile).
func (r *Role) Forget() {
	r.mu.Lock()
	r.rec, r.loaded = RoleRecord{}, false
	r.mu.Unlock()
}

// Peek reads the role from its records as they are now and keeps
// nothing: what an owner that does not hold the records answers, since
// the one that does may change them at any moment.
func (r *Role) Peek() (RoleRecord, error) {
	return readRole(r.records, r.identity)
}

// Record answers the role as it stands.
func (r *Role) Record() (RoleRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.load(); err != nil {
		return RoleRecord{}, err
	}
	return cloneRole(r.rec), nil
}

// cloneRole is a deep copy of rec: its lists and batches are its own.
func cloneRole(rec RoleRecord) RoleRecord {
	out := rec
	out.Open, out.Fired = cloneBatch(rec.Open), cloneBatch(rec.Fired)
	out.Notes, out.Log = slices.Clone(rec.Notes), slices.Clone(rec.Log)
	return out
}

// cloneBatch is a deep copy of b.
func cloneBatch(b *Batch) *Batch {
	if b == nil {
		return nil
	}
	c := *b
	c.Wakes = slices.Clone(b.Wakes)
	if b.Overflow != nil {
		o := *b.Overflow
		c.Overflow = &o
	}
	return &c
}

// change applies fn to a copy of the record and writes it, and takes it
// only once written: a change is written before what it starts, and one
// that could not be written changes nothing. The caller holds mu.
func (r *Role) change(fn func(c *RoleRecord) error) error {
	if err := r.load(); err != nil {
		return err
	}
	c := cloneRole(r.rec)
	if err := fn(&c); err != nil {
		return err
	}
	return r.save(c)
}

// save writes c, its log within MaxRoleLog, and takes it. The caller
// holds mu.
func (r *Role) save(c RoleRecord) error {
	if n := len(c.Log) - MaxRoleLog; n > 0 {
		c.Log = slices.Clone(c.Log[n:])
	}
	c.UpdatedAt = r.now().UTC()
	if err := r.records.SaveRole(c); err != nil {
		return err
	}
	r.rec = c
	return nil
}

// checkWake holds a wake to its fields' bounds.
func checkWake(w RoleWake) error {
	switch {
	case !slices.Contains(WakeSources, w.Source):
		return fmt.Errorf("no wake source %q", w.Source)
	case len(w.Subject) > maxWakeField || !utf8.ValidString(w.Subject):
		return fmt.Errorf("a wake's subject is at most %d bytes", maxWakeField)
	case len(w.Key) > maxWakeKey || !utf8.ValidString(w.Key):
		return fmt.Errorf("a wake's key is at most %d bytes", maxWakeKey)
	}
	return nil
}

// fired reports whether the log holds the wake already: a key fires
// once, whatever came of it.
func (c *RoleRecord) fired(w RoleWake) bool {
	if w.Key == "" {
		return false
	}
	return slices.ContainsFunc(c.Log, func(row RoleLogRow) bool {
		return row.Source == w.Source && row.Subject == w.Subject && row.Key == w.Key
	})
}

// gather adds the wake to the record: a row in the log, and, unless the
// role is paused, the open batch (opened now when there is none: its id
// written with it). The caller writes the record.
func (c *RoleRecord) gather(w RoleWake, now time.Time) {
	row := RoleLogRow{RoleWake: w}
	if c.State == RolePaused {
		row.Held = "paused"
		c.Log = append(c.Log, row)
		return
	}
	if c.Open == nil {
		c.Last++
		c.Open = &Batch{ID: "wb-" + strconv.FormatInt(c.Last, 10), OpenedAt: now.UTC(), Wakes: []BatchWake{}}
	}
	c.Open.add(w)
	row.Batch = c.Open.ID
	c.Log = append(c.Log, row)
	if c.State == RoleIdle {
		c.State = RoleGathering
	}
}

// add counts w in the batch: with the wakes of its source and subject,
// else as a new one while there is room, else in the overflow.
func (b *Batch) add(w RoleWake) {
	at := w.At.UTC()
	b.merge(BatchWake{Source: w.Source, Subject: w.Subject, Count: 1, First: at, Last: at})
}

// merge counts w's wakes in the batch, as add counts one.
func (b *Batch) merge(w BatchWake) {
	if i := slices.IndexFunc(b.Wakes, func(x BatchWake) bool { return x.Source == w.Source && x.Subject == w.Subject }); i >= 0 {
		x := &b.Wakes[i]
		x.Count += w.Count
		if w.First.Before(x.First) {
			x.First = w.First
		}
		if w.Last.After(x.Last) {
			x.Last = w.Last
		}
		return
	}
	if len(b.Wakes) < MaxBatchWakes {
		b.Wakes = append(b.Wakes, w)
		return
	}
	b.overflow(Overflow{Count: w.Count, First: w.First, Last: w.Last})
}

// overflow counts o in the batch's overflow.
func (b *Batch) overflow(o Overflow) {
	if b.Overflow == nil {
		b.Overflow = &Overflow{First: o.First, Last: o.Last}
	}
	b.Overflow.Count += o.Count
	if o.First.Before(b.Overflow.First) {
		b.Overflow.First = o.First
	}
	if o.Last.After(b.Overflow.Last) {
		b.Overflow.Last = o.Last
	}
}

// keepFired gives the batch fired to the next turn, its own having
// started none (its launch refused, its agent gone before it read its
// request, the day's budget spent at its dequeue): its wakes join the
// open batch ahead of those gathered since, or a batch opened now under
// a new id, since the fired batch's id has its request, closed. The
// role fires it once it can start a turn again.
func (c *RoleRecord) keepFired(now time.Time) {
	f := c.Fired
	if f == nil {
		return
	}
	b := &Batch{OpenedAt: now.UTC(), Wakes: []BatchWake{}}
	if c.Open != nil {
		b.ID, b.OpenedAt = c.Open.ID, c.Open.OpenedAt
	} else {
		c.Last++
		b.ID = "wb-" + strconv.FormatInt(c.Last, 10)
	}
	for _, from := range []*Batch{f, c.Open} {
		if from == nil {
			continue
		}
		for _, w := range from.Wakes {
			b.merge(w)
		}
		if from.Overflow != nil {
			b.overflow(*from.Overflow)
		}
	}
	c.Open = b
}

// unfire gives the batch frozen to fire back to gathering, its request
// refused before it was made (so its id is still its own): the wakes
// gathered since join it, their log rows with them.
func (c *RoleRecord) unfire() {
	f := c.Fired
	c.Fired, c.Firing = nil, false
	if f == nil {
		return
	}
	if o := c.Open; o != nil {
		for _, w := range o.Wakes {
			f.merge(w)
		}
		if o.Overflow != nil {
			f.overflow(*o.Overflow)
		}
		for i := range c.Log {
			if c.Log[i].Batch == o.ID {
				c.Log[i].Batch = f.ID
			}
		}
	}
	c.Open = f
}

// Wake is a source firing: logged, and gathered into the open batch (the
// role gathering once idle), unless the role is paused; nothing while the
// role is off, and nothing for a wake its log holds already (its source,
// subject and key). Written before the loop is poked.
func (r *Role) Wake(w RoleWake) error {
	if w.At.IsZero() {
		w.At = r.now()
	}
	w.At = w.At.UTC()
	if err := checkWake(w); err != nil {
		return err
	}
	if s, err := r.records.RoleSettings(); err != nil {
		return err
	} else if !s.On {
		return nil
	}
	r.mu.Lock()
	err := r.change(func(c *RoleRecord) error {
		if c.fired(w) {
			return errNothing
		}
		c.gather(w, r.now())
		return nil
	})
	r.mu.Unlock()
	if errors.Is(err, errNothing) {
		return nil
	}
	if err == nil {
		r.Poke()
	}
	return err
}

// errNothing ends a change that writes nothing.
var errNothing = errors.New("nothing to change")

// Asked is a person's own request: logged (it is its own turn, so it
// joins no batch), and it tries again for an unavailable role, but for
// one whose agent did not start (ReasonStart): the person's turn is that
// try, and the batch kept waits until the agent reads a request
// (Started) or the role's retry time comes, never fired into the same
// screen beside it. A request told again (its key the log's already: a
// write that failed, retried, or its op id asked again) changes nothing.
func (r *Role) Asked(w RoleWake) error {
	w.Source = WakeRequest
	if w.At.IsZero() {
		w.At = r.now()
	}
	w.At = w.At.UTC()
	if err := checkWake(w); err != nil {
		return err
	}
	if s, err := r.records.RoleSettings(); err != nil {
		return err
	} else if !s.On {
		return nil
	}
	r.mu.Lock()
	now := r.now()
	err := r.change(func(c *RoleRecord) error {
		if c.fired(w) {
			return errNothing
		}
		c.Log = append(c.Log, RoleLogRow{RoleWake: w, Held: "a person's own request"})
		if c.State == RoleUnavailable && c.Reason != ReasonStart {
			c.available(now)
		}
		return nil
	})
	r.mu.Unlock()
	if errors.Is(err, errNothing) {
		return nil
	}
	if err == nil {
		r.Poke()
	}
	return err
}

// Reconfigured is its owner's configuration saved with the change ch: an
// unavailable role looks again at once, as a person's request makes it
// (Asked), when the save changed what made it unavailable (SaveEnds, by
// the reason kept with it): a budget raised, another agent or account, or
// the background turned off or on, for one that did not start. It takes up what its record holds: a batch kept
// fires within its rules on the next step (its window, the cooldown), and
// Fire weighs the turn against the configuration now, so a budget raised
// clears "the day's budget is spent" and one still spent makes the role
// unavailable again in words true now. A save that changed nothing it
// weighs, or any other state, changes nothing. A turn whose agent did
// not start is ended, with its count of such turns in a row, by a save
// that changed what the agent starts with (an account cleared) or turned
// the role off or on, the role off too: the role turned on again starts
// clean, never unavailable with the old agent's words, and the batch the
// failed turn kept is dropped with it when the role is off, never fired
// by the role turned on again (#715). Any other reason waits for the
// role on.
func (r *Role) Reconfigured(ch RoleChange) error {
	s, err := r.records.RoleSettings()
	if err != nil {
		return err
	}
	r.mu.Lock()
	now := r.now()
	err = r.change(func(c *RoleRecord) error {
		if c.State != RoleUnavailable || !s.On && c.Reason != ReasonStart || !SaveEnds(c.Reason, ch) {
			return errNothing
		}
		if c.Reason == ReasonStart {
			c.Tries = 0
		}
		if !s.On {
			// Off: the batch the failed turn kept is dropped in the save
			// itself, as a step of the role off drops it, so the role
			// turned on again before the loop steps cannot fire it.
			c.Open = nil
		}
		c.available(now)
		return nil
	})
	r.mu.Unlock()
	if errors.Is(err, errNothing) {
		return nil
	}
	if err == nil {
		r.Poke()
	}
	return err
}

// Started is turn number turn of the role's agent that read its request,
// the role's own or a person's: the agent starts, so an unavailability for
// a turn whose agent did not (ReasonStart) ends, and the batch it kept
// fires within its rules; the count of such turns in a row starts again.
// The latest such turn is recorded (LastStarted), so a failure of an
// earlier turn that the role takes up after it changes nothing (Step).
func (r *Role) Started(turn int64) error {
	r.mu.Lock()
	now := r.now()
	err := r.change(func(c *RoleRecord) error {
		ended := c.State == RoleUnavailable && c.Reason == ReasonStart
		if !ended && c.Tries == 0 && turn <= c.LastStarted {
			return errNothing
		}
		c.LastStarted = max(c.LastStarted, turn)
		c.Tries = 0
		if ended {
			c.available(now)
		}
		return nil
	})
	r.mu.Unlock()
	if errors.Is(err, errNothing) {
		return nil
	}
	if err == nil {
		r.Poke()
	}
	return err
}

// Hold keeps a wake that fires at due if still answers true then (a
// question open past its threshold); one the log holds is dropped then.
// A wake held already (its source, subject and key) is held once, and at
// most MaxHeld are held, a new one past them dropped.
func (r *Role) Hold(w RoleWake, due time.Time, still func() bool) {
	r.mu.Lock()
	same := func(h heldWake) bool { return h.w.Source == w.Source && h.w.Subject == w.Subject && h.w.Key == w.Key }
	if len(r.held) < MaxHeld && !slices.ContainsFunc(r.held, same) {
		r.held = append(r.held, heldWake{w: w, due: due, still: still})
	}
	r.mu.Unlock()
	r.Poke()
}

// Pause pauses the role, from any state: no wake joins a batch and no
// batch fires until Resume; a turn already running runs to its end. The
// briefing times' mark is dropped with it, so no briefing time that
// passes while paused fires once resumed, whether or not a step ran
// between.
func (r *Role) Pause() (RoleRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.change(func(c *RoleRecord) error {
		if c.State == RolePaused {
			return errNothing
		}
		c.State, c.Why, c.RetryAt, c.Briefed = RolePaused, "paused by a person", time.Time{}, time.Time{}
		return nil
	})
	if err != nil && !errors.Is(err, errNothing) {
		return RoleRecord{}, err
	}
	return cloneRole(r.rec), nil
}

// Resume ends a pause: the role takes up what its record holds (a turn
// still running, a cooldown not over, a batch open, else idle), and its
// next step looks at the briefing times afresh, from the resume on.
func (r *Role) Resume() (RoleRecord, error) {
	r.mu.Lock()
	now := r.now()
	err := r.change(func(c *RoleRecord) error {
		if c.State != RolePaused {
			return errNothing
		}
		c.available(now)
		c.Briefed = time.Time{}
		return nil
	})
	out := cloneRole(r.rec)
	r.mu.Unlock()
	if err != nil && !errors.Is(err, errNothing) {
		return RoleRecord{}, err
	}
	r.Poke()
	return out, nil
}

// Switched drops the briefing times' mark as the role is turned off or
// on: its owner calls it before it writes the configuration that turns
// it, so a briefing time that passes while the role is off fires nothing
// once it is on, whether or not a step ran between. An error when the
// record could not be written, the configuration then left as it was.
func (r *Role) Switched() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.change(func(c *RoleRecord) error {
		if c.Briefed.IsZero() {
			return errNothing
		}
		c.Briefed = time.Time{}
		return nil
	})
	if errors.Is(err, errNothing) {
		return nil
	}
	return err
}

// KeepNote keeps a note, refused past MaxRoleNote or, with the notes
// kept, past MaxRoleNotes.
func (r *Role) KeepNote(n RoleNote) (RoleRecord, error) {
	n.Text = strings.TrimSpace(n.Text)
	switch {
	case n.Text == "" || len(n.Text) > MaxRoleNote || !utf8.ValidString(n.Text):
		return RoleRecord{}, fmt.Errorf("a note is 1 to %d bytes of text", MaxRoleNote)
	case len(n.From) > maxWakeField:
		return RoleRecord{}, fmt.Errorf("a note's ref is at most %d bytes", maxWakeField)
	}
	if n.At.IsZero() {
		n.At = r.now()
	}
	n.At = n.At.UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.change(func(c *RoleRecord) error {
		total := len(n.Text)
		for _, k := range c.Notes {
			total += len(k.Text)
		}
		if total > MaxRoleNotes {
			return fmt.Errorf("the notes are at most %d KiB together: clear some first", MaxRoleNotes>>10)
		}
		c.Notes = append(c.Notes, n)
		return nil
	})
	if err != nil {
		return RoleRecord{}, err
	}
	return cloneRole(r.rec), nil
}

// ClearNotes clears the notes.
func (r *Role) ClearNotes() (RoleRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.change(func(c *RoleRecord) error {
		c.Notes = nil
		return nil
	}); err != nil {
		return RoleRecord{}, err
	}
	return cloneRole(r.rec), nil
}

// derive is the state the record's own facts give a role that is
// neither paused nor unavailable.
func (c *RoleRecord) derive(now time.Time) string {
	switch {
	case c.Request != "" || c.Firing:
		return RoleBriefing
	case c.CoolUntil.After(now):
		return RoleCooling
	case c.Open != nil:
		return RoleGathering
	}
	return RoleIdle
}

// available ends a pause or an unavailability: the state is what the
// record's facts give.
func (c *RoleRecord) available(now time.Time) {
	c.Why, c.RetryAt, c.Reason = "", time.Time{}, ""
	c.State = c.derive(now)
}

// briefingTimes answers whether a briefing time falls after last and not
// after now (the latest such, for its key), and the next one after now.
func briefingTimes(s RoleSettings, last, now time.Time) (due time.Time, next time.Time) {
	loc := s.Location
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	for _, hm := range s.Briefings {
		h, m, ok := parseHM(hm)
		if !ok {
			continue
		}
		for _, d := range []int{-1, 0, 1} {
			t := time.Date(local.Year(), local.Month(), local.Day()+d, h, m, 0, 0, loc)
			if t.After(last) && !t.After(now) && t.After(due) {
				due = t
			}
			if t.After(now) && (next.IsZero() || t.Before(next)) {
				next = t
			}
		}
	}
	return due, next
}

// parseHM reads "HH:MM".
func parseHM(s string) (h, m int, ok bool) {
	hs, ms, found := strings.Cut(s, ":")
	if !found || len(hs) != 2 || len(ms) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	return h, m, err1 == nil && err2 == nil && h >= 0 && h < 24 && m >= 0 && m < 60
}

// Step does what is due at the role's clock: the briefing times that
// fell due, the held wakes whose time came, then the lifecycle (a batch
// fired once its window ends, a turn followed to its end and the cooldown
// after it, an unavailable role trying again when its time comes). It
// answers how long until something falls due on its own, 0 for nothing
// (the loop then waits for a poke).
func (r *Role) Step() (time.Duration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.load(); err != nil {
		return RoleRetry, err
	}
	// Settings that cannot be read are not the role turned off: nothing
	// changes (the batch gathering stays, unfired) until they can be.
	s, err := r.records.RoleSettings()
	if err != nil {
		return RoleRetry, err
	}
	now := r.now()
	c := cloneRole(r.rec)
	dirty := false
	var next time.Time
	soon := func(t time.Time) {
		if t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	// The briefing times: the first look only marks the moment (and when
	// the next falls due), so a role turned on or resumed fires no
	// briefing of the past; a briefing time due wakes the role and tries
	// again for an unavailable one. Off or paused, none is looked at and
	// the mark is dropped (Pause and Resume drop it themselves), so a
	// briefing time that passes meanwhile fires nothing once the role is
	// on, or resumed, again.
	switch {
	case !s.On || c.State == RolePaused:
		if !c.Briefed.IsZero() {
			c.Briefed, dirty = time.Time{}, true
		}
	case c.Briefed.IsZero():
		c.Briefed, dirty = now.UTC(), true
		_, nb := briefingTimes(s, now, now)
		soon(nb)
	default:
		due, nb := briefingTimes(s, c.Briefed, now)
		if !due.IsZero() {
			w := RoleWake{Source: WakeBriefing, Key: due.Format("2006-01-02T15:04Z07:00"), At: due.UTC()}
			c.Briefed = now.UTC()
			if !c.fired(w) {
				// A briefing time tries again for an unavailable role, but
				// for one whose agent did not start: its wake waits in the
				// batch kept for the role's own retry (Asked says why).
				if c.State == RoleUnavailable && c.Reason != ReasonStart {
					c.available(now)
				}
				c.gather(w, now)
			}
			dirty = true
		}
		soon(nb)
	}
	// The held wakes whose time came, each once: let go only once what
	// they gathered is written, so a write that fails leaves them held for
	// the step that tries again.
	var keep []heldWake
	for _, h := range r.held {
		if h.due.After(now) {
			keep = append(keep, h)
			soon(h.due)
			continue
		}
		if s.On && (h.still == nil || h.still()) && !c.fired(h.w) {
			c.gather(h.w, now)
			dirty = true
		}
	}
	var stepErr error
	// fire asks for the frozen batch's request, once a step: made (or
	// found made), the role briefs on it; refused before it was made, the
	// batch gathers again (dropped, the role off) and the role is
	// unavailable; made and gone, the role cools.
	tried := false
	fire := func() {
		tried = true
		id, err := r.records.Fire(*c.Fired)
		var refused *RoleRefused
		switch {
		case errors.As(err, &refused):
			c.unfire()
			if !s.On {
				// Off: what was gathering is dropped, never fired later.
				c.Open, c.State = nil, c.derive(now)
			} else {
				c.State, c.Why, c.RetryAt, c.Reason = RoleUnavailable, clip(refused.Why), refused.RetryAt.UTC(), refused.Reason
			}
			dirty = true
		case errors.Is(err, ErrRoleFired):
			c.Firing, c.Request = false, ""
			c.CoolUntil = now.Add(s.Cooldown).UTC()
			c.State, dirty = c.derive(now), true
		case err != nil:
			stepErr = err
			soon(now.Add(RoleRetry))
		default:
			for i := range c.Log {
				if c.Log[i].Batch == c.Fired.ID {
					c.Log[i].Request = id
				}
			}
			c.Firing, c.Request = false, id
			c.State, dirty = RoleBriefing, true
		}
	}
	for range 4 {
		before := c.State
		switch c.State {
		case RolePaused:
			// A turn already running runs to its end; the cooldown after
			// it starts then, and Resume finds what is left of it.
			if c.Request != "" {
				out, err := r.records.Outcome(c.Request)
				if err != nil {
					stepErr = err
					soon(now.Add(RoleRetry))
				} else if !out.Open {
					c.Request, dirty = "", true
					if out.Unavailable != "" {
						// The turn started none: its batch is kept, and no
						// cooldown follows a turn that never ran.
						c.keepFired(now)
					} else {
						c.CoolUntil = now.Add(s.Cooldown).UTC()
					}
				}
			}
		case RoleBriefing:
			if c.Firing {
				if !tried {
					fire()
				}
				break
			}
			out, err := r.records.Outcome(c.Request)
			if err != nil {
				stepErr = err
				soon(now.Add(RoleRetry))
				break
			}
			if out.Open {
				break
			}
			c.Request, dirty = "", true
			if out.Unavailable != "" {
				// The turn started none: its batch is kept for the turn
				// that tries again. One whose agent did not start tries
				// again after a wait that doubles with each in a row.
				c.keepFired(now)
				if out.Turn > 0 && c.LastStarted > out.Turn {
					// A later turn's agent read its request since this
					// turn was made: the agent starts, and this failure,
					// taken up late, is this turn's own. The batch kept
					// fires within its rules.
					c.State = c.derive(now)
					break
				}
				retry := out.RetryAt.UTC()
				if out.Reason == ReasonStart {
					c.Tries++
					if retry.IsZero() {
						retry = now.Add(startRetry(c.Tries)).UTC()
					}
				}
				c.State, c.Why, c.RetryAt, c.Reason = RoleUnavailable, clip(out.Unavailable), retry, out.Reason
				break
			}
			c.Tries = 0
			c.CoolUntil = now.Add(s.Cooldown).UTC()
			c.State = c.derive(now)
		case RoleCooling:
			if now.Before(c.CoolUntil) {
				soon(c.CoolUntil)
				break
			}
			c.State, dirty = c.derive(now), true
		case RoleUnavailable:
			if !c.RetryAt.IsZero() {
				if now.Before(c.RetryAt) {
					soon(c.RetryAt)
					break
				}
				c.available(now)
				dirty = true
			}
		case RoleIdle:
			if c.Open != nil {
				c.State, dirty = RoleGathering, true
			}
		case RoleGathering:
			if c.Open == nil {
				c.State, dirty = RoleIdle, true
				break
			}
			if !s.On {
				// Off: what was gathering is dropped, never fired later.
				c.Open, c.State, dirty = nil, RoleIdle, true
				break
			}
			at := c.Open.OpenedAt.Add(s.Window)
			if now.Before(at) {
				soon(at)
				break
			}
			// The batch is frozen as the one firing, written with what
			// the step changed before its request is made: a wake from
			// here on opens the next batch, so none joins a batch whose
			// turn may have read it already. A refusal gives it back to
			// gathering (unfire), fired once the role can start a turn
			// again.
			c.Fired, c.Open, c.Request, c.Firing = c.Open, nil, "", true
			c.State = RoleBriefing
			if err := r.save(c); err != nil {
				return RoleRetry, err
			}
			c, dirty = cloneRole(r.rec), false
			fire()
		}
		if c.State == before {
			break
		}
	}
	if dirty {
		if err := r.save(c); err != nil {
			return RoleRetry, err
		}
	}
	r.held = keep
	if next.IsZero() {
		return 0, stepErr
	}
	return max(time.Millisecond, next.Sub(now)), stepErr
}

// clip is why cut to maxRoleWhy bytes, at a character.
func clip(why string) string {
	for len(why) > maxRoleWhy {
		_, n := utf8.DecodeLastRuneInString(why)
		why = why[:len(why)-n]
	}
	return why
}
