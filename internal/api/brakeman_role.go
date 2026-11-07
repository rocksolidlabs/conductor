package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Brakeman's background role (docs/plans/brakeman.md, BK3, PR-14): the
// desk's side of Yardmaster's role loop (crew.Role). The desk implements
// crew.RoleRecords over internal/brakeman's store (the role's record in
// brakeman/role.json, a turn as a request of kind wake made under the
// batch's own op id, wake-<batch>) and feeds the loop its sources, each
// event-driven: the engine's run changes (a task failed with proof, a run
// ended or paused), the activity sink (a question open past its
// threshold), Dispatch's changes (a ticket moved to ready), a person's own
// request, and the briefing times the loop keeps itself. No source fires
// for Brakeman's own runs and sessions or for what a turn's request made
// (its acts, a draft a person made from it). A wake turn is BK1's turn
// unchanged: a fresh run of a fresh session, which reads its batch
// through its request, and whose questions page no one (quiet).

// maxRoleLogReply bounds GET /api/brakeman/role's log, in rows.
const maxRoleLogReply = crew.MaxRoleLog

// maxRoleSources bounds the run ids and question sessions noted between
// two passes of the role's loop; past it the oldest wait for the next
// note of theirs (a run's next change, a question's next entry).
const maxRoleSources = 1000

// roleSources are what the sources noted since the role's last pass:
// the runs that changed, the sessions whose question opened, and that
// Dispatch committed a change.
type roleSources struct {
	mu        sync.Mutex
	runs      map[string]bool
	questions map[string]bool
	tickets   bool
	// ticketSeq is how far Dispatch's change feed was read: from its head
	// as the desk opened (ticketBegun), so what was there before wakes
	// nothing. ticketTop is the newest commit announced, which the feed is
	// read on from while the role is off.
	ticketSeq   int64
	ticketTop   int64
	ticketBegun bool
	// ticketPage and ticketPages bound one pass's read of the feed: pages
	// of ticketPage changes, at most ticketPages of them, the rest read
	// on the pass after, at once.
	ticketPage, ticketPages int
	// paused are the runs last seen paused: a pause wakes the role as the
	// run becomes paused, not at each change while it stays so.
	paused map[string]bool
	// made are the ticket launches whose maker was read (madeByTurn): true
	// for a Brakeman act's. At most maxRoleSources, cleared past them.
	made map[string]bool
	// asked are the person's requests the role could not note (noteAsked),
	// told again on the next pass.
	asked map[string]bool
}

// openRole makes the desk's role and wires Dispatch's changes to it; the
// engine's run changes reach it through runChanged (the server's
// OnRunChange) and the activity through its sink (activity), which New
// adds before the engine's.
func (d *brakemanDesk) openRole() {
	d.src = &roleSources{runs: map[string]bool{}, questions: map[string]bool{}, paused: map[string]bool{}, made: map[string]bool{}, asked: map[string]bool{},
		ticketPage: 500, ticketPages: 20}
	d.role = crew.NewRole(brakemanMember, roleRecords{d: d}, d.now)
	d.role.SetLogger(d.s.log)
	if ds := d.s.dispatch; ds != nil {
		// The feed is read from its head now, before the desk hears of a
		// commit: every row of a commit announced after is read, its first
		// too (a commit's announcement names only its last seq).
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		head, err := ds.Head(ctx)
		cancel()
		if err != nil {
			d.s.log.Warn("Brakeman's role reads Dispatch's changes from the next commit on", "err", err)
		} else {
			d.src.ticketSeq, d.src.ticketTop, d.src.ticketBegun = head, head, true
		}
		ds.OnChange(d.ticketsChanged)
	}
}

// roleRecord is the role as the desk answers it: as the role holds it
// once the desk owns Brakeman's records, else read from them as they are
// now, since the owner may change them at any moment (and the role takes
// them up afresh once they are this desk's: OnOwned's Forget).
func (d *brakemanDesk) roleRecord() (crew.RoleRecord, error) {
	if d.recovered.Load() {
		return d.role.Record()
	}
	return d.role.Peek()
}

// roleLoop runs the role's loop until the desk ends: each pass drains
// the sources noted into wakes, then steps, while this server owns
// Brakeman's records.
func (d *brakemanDesk) roleLoop() {
	defer close(d.roleDone)
	d.role.Run(d.done, func() bool {
		if ro, _ := d.st.ReadOnly(); ro || !d.recovered.Load() || d.closing.Load() {
			return false
		}
		d.drainSources()
		return true
	})
}

// runChanged notes a run that changed (the engine's OnRunChange, under
// its lock: nothing here calls the engine or waits).
func (d *brakemanDesk) runChanged(runID string) {
	d.src.mu.Lock()
	if len(d.src.runs) < maxRoleSources {
		d.src.runs[runID] = true
	}
	d.src.mu.Unlock()
	d.role.Poke()
}

// activity is the role's activity sink: a question opened notes its
// session, and pokes the desk, whose turn may be the one asking (its
// agent held at a startup question: heldAtStartup). It runs where the
// entry was recorded, so it never waits.
func (d *brakemanDesk) activity(sessionID string, e session.ActivityEntry, state session.AttentionState) {
	if e.Type != session.ActivityAttention || state != session.AttentionNeedsInput {
		return
	}
	d.poke()
	d.src.mu.Lock()
	if len(d.src.questions) < maxRoleSources {
		d.src.questions[sessionID] = true
	}
	d.src.mu.Unlock()
	d.role.Poke()
}

// ticketsChanged notes that Dispatch committed: the feed is read from
// where it was last read, on the role's pass.
func (d *brakemanDesk) ticketsChanged(changes []dispatch.Change) {
	d.src.mu.Lock()
	for _, c := range changes {
		d.src.ticketTop = max(d.src.ticketTop, c.Seq)
	}
	if !d.src.ticketBegun && len(changes) > 0 {
		// The head could not be read as the desk opened: the feed is read
		// from after this commit, whose first seq is not known.
		d.src.ticketBegun = true
		d.src.ticketSeq = d.src.ticketTop
	}
	d.src.tickets = d.src.tickets || len(changes) > 0
	d.src.mu.Unlock()
	d.role.Poke()
}

// drainSources turns what the sources noted into wakes. What it cannot
// take now is kept for a later pass, never dropped: a run whose change is
// not yet committed (settledRun; its commit notes it again), and, with
// the role told to pass again (Again), whatever a configuration that
// cannot be read or a wake that could not be written left. A wake outside
// the configuration's scope or visibility is not taken (admitted).
func (d *brakemanDesk) drainSources() {
	d.src.mu.Lock()
	runs, questions, tickets, asked := d.src.runs, d.src.questions, d.src.tickets, d.src.asked
	d.src.runs, d.src.questions, d.src.tickets, d.src.asked = map[string]bool{}, map[string]bool{}, false, map[string]bool{}
	d.src.mu.Unlock()
	cfg, err := d.st.Config()
	if err != nil {
		// Unreadable is not off: nothing is dropped or read past.
		d.s.log.Warn("Brakeman's role could not read its configuration: it tries again", "err", err)
		d.keepSources(runs, questions, tickets)
		d.keepAsked(asked)
		d.role.Again()
		return
	}
	if !cfg.Background() {
		// Off: what the sources noted is dropped, and Dispatch's feed is
		// read on from its newest commit, so a ticket made ready while the
		// role was off wakes nothing once it is on, as nothing else does.
		d.src.mu.Lock()
		d.src.ticketSeq = max(d.src.ticketSeq, d.src.ticketTop)
		d.src.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adm := d.s.wakeResolver(d.st, cfg)
	kept := map[string]bool{}
	failed := false
	for id := range runs {
		run, ok, settled := d.settledRun(id)
		switch {
		case !ok:
			continue
		case !settled:
			kept[id] = true
			continue
		case d.madeByTurn(run):
			continue
		}
		wakes := d.runWakes(run)
		for _, w := range wakes {
			if !d.admitted(ctx, adm, w.Subject) {
				continue
			}
			if err := d.roleWake(w); err != nil {
				// Read again on the next pass: the wakes written already are
				// the log's (a key fires once), and the pause is seen anew.
				kept[id], failed = true, true
				if slices.ContainsFunc(wakes, func(w crew.RoleWake) bool { return w.Source == crew.WakeRunPaused }) {
					d.src.mu.Lock()
					delete(d.src.paused, id)
					d.src.mu.Unlock()
				}
				break
			}
		}
	}
	d.keepSources(kept, nil, false)
	for id := range asked {
		if err := d.askedWake(id); err != nil {
			d.keepAsked(map[string]bool{id: true})
			failed = true
		}
	}
	for sid := range questions {
		d.holdQuestion(ctx, adm, sid, time.Duration(cfg.Wake.QuestionMinutes)*time.Minute)
	}
	if tickets && !d.readTickets(ctx, adm) {
		failed = true
	}
	if failed {
		d.role.Again()
	}
}

// keepSources notes again what a pass could not take, for the next.
func (d *brakemanDesk) keepSources(runs, questions map[string]bool, tickets bool) {
	d.src.mu.Lock()
	defer d.src.mu.Unlock()
	for id := range runs {
		if len(d.src.runs) < maxRoleSources {
			d.src.runs[id] = true
		}
	}
	for sid := range questions {
		if len(d.src.questions) < maxRoleSources {
			d.src.questions[sid] = true
		}
	}
	d.src.tickets = d.src.tickets || tickets
}

// keepAsked notes again the person's requests a pass could not tell the
// role of, for the next.
func (d *brakemanDesk) keepAsked(asked map[string]bool) {
	d.src.mu.Lock()
	defer d.src.mu.Unlock()
	for id := range asked {
		if len(d.src.asked) < maxRoleSources {
			d.src.asked[id] = true
		}
	}
}

// askedWake tells the role of a person's request id, under a key of its
// own, so telling it again (a write that failed, the request's op id
// asked again) logs it once.
func (d *brakemanDesk) askedWake(id string) error {
	err := d.role.Asked(crew.RoleWake{Subject: id, Key: "asked"})
	if err != nil && !errors.Is(err, errDeskClosing) {
		d.s.log.Warn("Brakeman's role could not note a person's request: it tries again", "request", id, "err", err)
	}
	return err
}

// noteAsked tells the role of a person's request kept: one it could not
// note (its record not written, its configuration not read) is kept and
// told again on a later pass of its loop, never dropped.
func (d *brakemanDesk) noteAsked(id string) {
	if err := d.askedWake(id); err == nil || errors.Is(err, errDeskClosing) {
		return
	}
	d.keepAsked(map[string]bool{id: true})
	d.role.Again()
	d.role.Poke()
}

// settledRun reads the run as its ledger holds it: ok false for a run the
// engine does not hold; settled false while a change of a managed run
// waits for its transaction or is in one (the engine changes the run
// before its transaction commits, and puts it back when the transaction
// is refused), so no wake is read from a change that may never be
// committed. A commit notes the run again once it lands (OnRunChange); a
// legacy run has no transaction, and is what the engine holds.
func (d *brakemanDesk) settledRun(id string) (run crew.Run, ok, settled bool) {
	rev, before, held := d.s.runs.Settled(id)
	run, ok = d.s.runs.Get(id)
	if !ok || run.Managed == nil {
		return run, ok, ok
	}
	after, still, _ := d.s.runs.Settled(id)
	return run, true, held && before && still && after == rev
}

// roleWake hands a wake to the role: an error when it was not taken (its
// write failed, the settings could not be read), the source then keeping
// it for a later pass.
func (d *brakemanDesk) roleWake(w crew.RoleWake) error {
	err := d.role.Wake(w)
	if err != nil && !errors.Is(err, errDeskClosing) {
		d.s.log.Warn("Brakeman's role could not note a wake: it tries again", "source", w.Source, "subject", w.Subject, "err", err)
	}
	return err
}

// admitted reports whether a wake's subject is one the configuration's
// scope and visibility admit now (adm, wakeResolver): a source outside
// them wakes nothing. A subject whose record cannot be read now is taken;
// the turn's own read of its batch (visibleBatch) leaves out what its
// grant does not admit then.
func (d *brakemanDesk) admitted(ctx context.Context, adm *refResolver, subject string) bool {
	ok, err := adm.admitsSubject(ctx, subject)
	return ok || err != nil
}

// runWakes are the wakes a run's state gives: each task failed with
// proof (the engine's failed state, its attempts the key), the run paused
// as it becomes so, and its end (stopped, finished, or every task
// terminal). A Brakeman turn's run gives none.
func (d *brakemanDesk) runWakes(run crew.Run) []crew.RoleWake {
	if run.Class == crew.ClassBrakeman {
		return nil
	}
	now := d.now()
	var out []crew.RoleWake
	for _, t := range run.Tasks {
		if t.State == ledger.TaskFailed {
			out = append(out, crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:" + run.ID + "/" + t.ID, Key: "attempts " + strconv.Itoa(t.Attempts), At: now})
		}
	}
	d.src.mu.Lock()
	was := d.src.paused[run.ID]
	if run.Paused {
		d.src.paused[run.ID] = true
	} else {
		delete(d.src.paused, run.ID)
	}
	d.src.mu.Unlock()
	if run.Paused && !was {
		key := "paused"
		if run.Managed != nil {
			key += " at revision " + strconv.FormatInt(run.Managed.Revision, 10)
		}
		out = append(out, crew.RoleWake{Source: crew.WakeRunPaused, Subject: "run:" + run.ID, Key: key, At: now})
	}
	switch {
	case run.Outcome != "":
		out = append(out, crew.RoleWake{Source: crew.WakeRunEnded, Subject: "run:" + run.ID, Key: "outcome " + run.Outcome, At: now})
	case run.State == crew.RunStopped || run.State == crew.RunFinished:
		out = append(out, crew.RoleWake{Source: crew.WakeRunEnded, Subject: "run:" + run.ID, Key: run.State, At: now})
	}
	return out
}

// madeByTurn reports whether a run is what a Brakeman act made: a
// ticket's launch Dispatch keeps as made under Brakeman's actor, which
// only the desk's Host launches under. Read from the launch's own create
// row, written before its run started, so whatever became of the act's
// request since (its receipt not yet copied, the request pruned, a
// hundred newer) the run is known as Brakeman's. A launch whose maker
// cannot be read is taken as Brakeman's: a wake missed is better than a
// turn's run waking the next turn.
func (d *brakemanDesk) madeByTurn(run crew.Run) bool {
	if run.Ticket == nil || run.Ticket.Launch == "" || d.s.dispatch == nil {
		return false
	}
	id := run.Ticket.Launch
	d.src.mu.Lock()
	made, known := d.src.made[id]
	d.src.mu.Unlock()
	if known {
		return made
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	actor, err := d.s.dispatch.LaunchActor(ctx, id)
	switch {
	case errors.Is(err, dispatch.ErrNotFound):
		made = false
	case err != nil:
		d.s.log.Warn("Brakeman's role could not read who made a run's launch: it wakes nothing", "run", run.ID, "launch", id, "err", err)
		return true
	default:
		made = actor == brakemanActor
	}
	d.src.mu.Lock()
	if len(d.src.made) >= maxRoleSources {
		clear(d.src.made)
	}
	d.src.made[id] = made
	d.src.mu.Unlock()
	return made
}

// holdQuestion holds a wake for the session's open question, due once it
// has been open for threshold, if it is still the one open then. A
// Brakeman turn's session's question wakes nothing, nor does one of a
// member of a run a Brakeman act launched (madeByTurn), nor one outside
// the scope or the visibility (admitted).
func (d *brakemanDesk) holdQuestion(ctx context.Context, adm *refResolver, sid string, threshold time.Duration) {
	drv, ok := d.s.registry.Get(sid)
	if !ok {
		return
	}
	info := drv.Info()
	att := info.Attention
	if (info.Crew != nil && info.Crew.Brakeman != nil) || att.State != session.AttentionNeedsInput || att.Since == nil {
		return
	}
	if info.Crew != nil && info.Crew.RunID != "" {
		if run, ok := d.s.runs.Get(info.Crew.RunID); ok && (run.Class == crew.ClassBrakeman || d.madeByTurn(run)) {
			return
		}
	}
	since := att.Since.UTC()
	subject := "session:" + sid
	if att.ID != "" {
		subject = "question:" + sid + "/" + att.ID
	}
	if !d.admitted(ctx, adm, subject) {
		return
	}
	w := crew.RoleWake{Source: crew.WakeQuestionOpen, Subject: subject, Key: since.Format(time.RFC3339Nano), At: since.Add(threshold)}
	d.role.Hold(w, since.Add(threshold), func() bool {
		drv, ok := d.s.registry.Get(sid)
		if !ok {
			return false
		}
		a := drv.Info().Attention
		return a.State == session.AttentionNeedsInput && a.Since != nil && a.Since.Equal(since)
	})
}

// derivedOp matches an op id the desk derived (deriveOp): what a make or
// an act of a request ran under.
var derivedOp = regexp.MustCompile(`^bk[0-9a-f]{30}$`)

// readTickets reads Dispatch's change feed from where it was last read:
// a ticket whose status a change set to ready wakes the role, read from
// that change's own value (a later move of the ticket's makes it no
// other), unless Brakeman's actor or an op a request derived moved it, or
// the ticket is outside the scope or the visibility. The feed's place
// moves past a change only once its wake is written: a read or a write
// that fails leaves it there, noted for the next pass, and answers false.
// A pass reads at most its pages; the changes past them are noted and
// read on the next pass, which it asks for at once.
func (d *brakemanDesk) readTickets(ctx context.Context, adm *refResolver) bool {
	ds := d.s.dispatch
	if ds == nil {
		return true
	}
	d.src.mu.Lock()
	after, size, pages := d.src.ticketSeq, d.src.ticketPage, d.src.ticketPages
	d.src.mu.Unlock()
	stop := func() bool {
		d.src.mu.Lock()
		d.src.ticketSeq = max(d.src.ticketSeq, after)
		d.src.tickets = true
		d.src.mu.Unlock()
		return false
	}
	for range pages {
		page, err := ds.ChangesAfter(ctx, after, size)
		if err != nil {
			d.s.log.Warn("Brakeman's role could not read Dispatch's changes: it tries again", "err", err)
			return stop()
		}
		for _, c := range page {
			if c.Entity == "ticket" && c.Field == "status" && c.Ticket != "" && c.Actor != brakemanMember && !derivedOp.MatchString(c.OpID) {
				status, err := ds.ChangeValue(ctx, c.Seq)
				if err != nil {
					d.s.log.Warn("Brakeman's role could not read a ticket's status change: it tries again", "seq", c.Seq, "err", err)
					return stop()
				}
				subject := "ticket:" + c.Ticket
				if status == dispatch.TicketReady && d.admitted(ctx, adm, subject) {
					if err := d.roleWake(crew.RoleWake{Source: crew.WakeTicketReady, Subject: subject, Key: "change " + strconv.FormatInt(c.Seq, 10), At: c.At}); err != nil {
						return stop()
					}
				}
			}
			after = c.Seq
		}
		d.src.mu.Lock()
		d.src.ticketSeq = max(d.src.ticketSeq, after)
		d.src.mu.Unlock()
		if len(page) < size {
			return true
		}
	}
	// The pass's pages are read and changes may be left: the next pass
	// reads on from here, though no commit is announced meanwhile.
	d.src.mu.Lock()
	d.src.tickets = true
	d.src.mu.Unlock()
	d.role.Poke()
	return true
}

// roleRecords is crew.RoleRecords over the desk.
type roleRecords struct{ d *brakemanDesk }

func (rr roleRecords) LoadRole() (crew.RoleRecord, bool, error) {
	doc, ok, err := rr.d.st.Role()
	if err != nil || !ok {
		return crew.RoleRecord{}, ok, err
	}
	var rec crew.RoleRecord
	if err := json.Unmarshal(doc, &rec); err != nil {
		return crew.RoleRecord{}, false, fmt.Errorf("brakeman/%s does not parse: %w", brakeman.RoleFile, err)
	}
	rr.d.noteRoleState(rec.State)
	return rec, true, nil
}

// SaveRole writes the role's record and sends the brakeman event for the
// role ({id: "role", state}).
func (rr roleRecords) SaveRole(rec crew.RoleRecord) error {
	if rr.d.closing.Load() {
		return errDeskClosing
	}
	if err := rr.d.st.SaveRole(rec); err != nil {
		return err
	}
	rr.d.noteRoleState(rec.State)
	rr.d.s.events.brakeman(roleEventID, rec.State)
	return nil
}

// The role's state as the desk last read or wrote it (roleState).
const (
	roleUnknown int32 = iota
	roleRunning
	rolePausedState
)

// noteRoleState notes the role's state as its records hold it.
func (d *brakemanDesk) noteRoleState(state string) {
	if state == crew.RolePaused {
		d.roleState.Store(rolePausedState)
	} else {
		d.roleState.Store(roleRunning)
	}
}

// roleHolds reports whether a wake request waits in the queue rather
// than start: a pause asked and not yet answered, or the role paused as
// its records hold it (read from them when the desk has not yet), or a
// record that cannot be read. Called under admit, which Pause's mark is
// made under: a dequeue either started the request before the pause, a
// turn already running, or holds it.
func (d *brakemanDesk) roleHolds() bool {
	if d.role == nil {
		return false
	}
	if d.pausing.Load() > 0 {
		return true
	}
	switch d.roleState.Load() {
	case roleRunning:
		return false
	case rolePausedState:
		return true
	}
	rec, err := d.role.Peek()
	return err != nil || rec.State == crew.RolePaused
}

// startable is the oldest of queue that may start now: a wake request
// is held while the role is paused (roleHolds), and the requests behind
// it start; "" when none may.
func (d *brakemanDesk) startable(queue []string) string {
	if !d.roleHolds() {
		return queue[0]
	}
	for _, id := range queue {
		if r, err := d.st.Get(id); err != nil || r.Kind != brakeman.KindWake {
			return id
		}
	}
	return ""
}

// roleEventID is the brakeman event's id for a change of the role.
const roleEventID = "role"

// RoleSettings reads the configuration; one that cannot be read is an
// error, never the role turned off.
func (rr roleRecords) RoleSettings() (crew.RoleSettings, error) {
	cfg, err := rr.d.st.Config()
	if err != nil {
		return crew.RoleSettings{}, err
	}
	return crew.RoleSettings{On: cfg.Background(), Window: time.Duration(cfg.Wake.WindowSeconds) * time.Second,
		Cooldown: time.Duration(cfg.Wake.CooldownMinutes) * time.Minute, Briefings: cfg.Wake.Briefings, Location: time.Local}, nil
}

// nextDay is the start of the local day after now's.
func nextDay(now time.Time) time.Time {
	l := now.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, time.Local)
}

// wakeOpPrefix begins every op id a wake request is made under: a
// person's request may not take it (handleBrakemanAsk).
const wakeOpPrefix = "wake-"

// maxWakeOps is how many op ids a batch's request may be made under: a
// request a person kept under wake-<batch> before the prefix was theirs
// to take no more is passed over for the next (wakeOpID).
const maxWakeOps = 8

// wakeOpID is the k-th op id (from 1) of the batch's request:
// wake-<batch>, then wake-<batch>.<k>.
func wakeOpID(batch string, k int) string {
	if k <= 1 {
		return wakeOpPrefix + batch
	}
	return wakeOpPrefix + batch + "." + strconv.Itoa(k)
}

// Fire makes the batch's request, kind wake, under the op id wake-<batch>
// and in admission's order, as a person's ask is kept: a batch fired
// again answers the request it made, known by its kind and batch, never
// another request kept under that op id (the next of the batch's op ids
// is taken past one); a turn that cannot start is refused in words
// (Brakeman off, stuck, the day's turns or minutes spent), a queue full
// is tried again.
func (rr roleRecords) Fire(b crew.Batch) (string, error) {
	d := rr.d
	d.admit.Lock()
	defer d.admit.Unlock()
	if d.closing.Load() {
		return "", errDeskClosing
	}
	if !d.recovered.Load() {
		return "", d.notOwner()
	}
	for k := 1; k <= maxWakeOps; k++ {
		in := brakeman.Request{OpID: wakeOpID(b.ID, k), Kind: brakeman.KindWake, Batch: b.ID}
		id, mine, err := rr.madeUnder(in.OpID, b.ID)
		if err != nil {
			return "", err
		}
		if id != "" {
			if mine {
				return id, nil
			}
			continue
		}
		id, err = rr.make(in)
		if errors.Is(err, brakeman.ErrOpConflict) {
			// Another request's op id, that request pruned since.
			continue
		}
		return id, err
	}
	return "", fmt.Errorf("the batch %s's %d op ids are other requests'", b.ID, maxWakeOps)
}

// Made answers the request Fire made for the batch, "" when none was
// made under any of its op ids (or it is gone): it makes none, so a
// batch the role will not fire anew is taken up only by a request that
// exists.
func (rr roleRecords) Made(b crew.Batch) (string, error) {
	d := rr.d
	d.admit.Lock()
	defer d.admit.Unlock()
	if d.closing.Load() {
		return "", errDeskClosing
	}
	if !d.recovered.Load() {
		return "", d.notOwner()
	}
	for k := 1; k <= maxWakeOps; k++ {
		id, mine, err := rr.madeUnder(wakeOpID(b.ID, k), b.ID)
		if err != nil {
			return "", err
		}
		// Every op id is looked at: Fire passes over one another
		// request kept, pruned since, as well as one it holds.
		if mine {
			return id, nil
		}
	}
	return "", nil
}

// madeUnder answers the request kept under the op id: its id ("" for
// none), and whether it is the batch's wake request (its kind and batch).
func (rr roleRecords) madeUnder(op, batch string) (string, bool, error) {
	prev, found, err := rr.d.st.ByOp(op)
	if err != nil || !found {
		return "", false, err
	}
	return prev.ID, prev.Kind == brakeman.KindWake && prev.Batch == batch, nil
}

// make keeps the wake request in, once Brakeman runs in the background
// and the desk, the queue and the day's budget have room for it. The
// caller holds admit.
func (rr roleRecords) make(in brakeman.Request) (string, error) {
	d := rr.d
	cfg, err := d.st.Config()
	if err != nil {
		return "", err
	}
	if !cfg.Background() {
		return "", &crew.RoleRefused{Why: "Brakeman is off, or not in the background", Reason: crew.ReasonOff}
	}
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stuck {
		return "", &crew.RoleRefused{Why: "the last turn's run did not stop: stop it from its run page", Reason: crew.ReasonStuck}
	}
	if len(d.queue) >= brakemanQueue {
		return "", fmt.Errorf("%d requests wait already", brakemanQueue)
	}
	turns, err := d.st.TurnsToday(now)
	if err != nil {
		return "", err
	}
	if turns+len(d.queue) >= cfg.Budget.TurnsPerDay {
		return "", &crew.RoleRefused{Why: fmt.Sprintf("the day's budget is spent: %d of %d turns, those queued counted", turns+len(d.queue), cfg.Budget.TurnsPerDay), Reason: crew.ReasonBudget, RetryAt: nextDay(now)}
	}
	minutes, err := d.st.MinutesToday(now)
	if err != nil {
		return "", err
	}
	if left := cfg.Budget.MinutesPerDay - minutes; left < brakeman.MinReservation {
		return "", &crew.RoleRefused{Why: fmt.Sprintf("the day's budget is spent: %d of %d minutes", minutes, cfg.Budget.MinutesPerDay), Reason: crew.ReasonBudget, RetryAt: nextDay(now)}
	}
	req, created, err := d.st.Create(in)
	if errors.Is(err, brakeman.ErrPruned) {
		return "", crew.ErrRoleFired
	}
	if err != nil {
		return "", err
	}
	if created {
		d.queue = append(d.queue, req.ID)
		d.publish(req.ID)
		d.poke()
	}
	return req.ID, nil
}

// Outcome answers what became of a wake request: open; closed failed (its
// launch refused, its agent exited before it read its request, as a
// logged-out CLI does, or never read it: held at a screen), its agent not
// started, or over budget, unavailable in words with the reason; else
// closed. A request gone (pruned) is closed. A failure carries its turn's
// number (the request's), which the role judges it by.
func (rr roleRecords) Outcome(id string) (crew.RoleOutcome, error) {
	r, err := rr.d.st.Get(id)
	if errors.Is(err, brakeman.ErrNotFound) {
		return crew.RoleOutcome{}, nil
	}
	if err != nil {
		return crew.RoleOutcome{}, err
	}
	switch {
	case r.Open():
		return crew.RoleOutcome{Open: true}, nil
	case r.State == brakeman.StateFailed:
		return crew.RoleOutcome{Unavailable: "the turn could not start: " + r.Why, Reason: crew.ReasonStart, Turn: brakeman.RequestNumber(r.ID)}, nil
	case r.State == brakeman.StateOverBudget:
		return crew.RoleOutcome{Unavailable: "the day's budget is spent: " + r.Why, Reason: crew.ReasonBudget, RetryAt: nextDay(rr.d.now()), Turn: brakeman.RequestNumber(r.ID)}, nil
	}
	return crew.RoleOutcome{}, nil
}

// roleView is the role as the routes answer it: its record without the
// log, and the day's minutes.
type roleView struct {
	crew.RoleRecord
	Log          []crew.RoleLogRow `json:"log,omitempty"`
	MinutesToday int               `json:"minutesToday"`
}

// roleBatch is the batch a wake request names, as the role holds it: the
// last fired, or the one open; nil when neither is it.
func (d *brakemanDesk) roleBatch(id string) *crew.Batch {
	rec, err := d.roleRecord()
	if err != nil {
		return nil
	}
	for _, b := range []*crew.Batch{rec.Fired, rec.Open} {
		if b != nil && b.ID == id {
			return b
		}
	}
	return nil
}

// handleBrakemanRole answers the role: {role, log}, the log newest first,
// ?limit= rows of it (1 to 1,000; 200).
func (s *Server) handleBrakemanRole(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	dq, err := readQuery(r, "limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	limit := 200
	if v := url.Values(dq).Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > maxRoleLogReply || strconv.Itoa(n) != v {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit is 1 to %d", maxRoleLogReply))
			return
		} else {
			limit = n
		}
	}
	rec, err := d.roleRecord()
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	log := slices.Clone(rec.Log)
	slices.Reverse(log)
	if len(log) > limit {
		log = log[:limit]
	}
	rec.Log = nil
	minutes, err := d.st.MinutesToday(d.now())
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if log == nil {
		log = []crew.RoleLogRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": roleView{RoleRecord: rec, MinutesToday: minutes}, "log": log})
}

// brakemanRoleBody is POST /api/brakeman/role's body.
type brakemanRoleBody struct {
	Action string `json:"action"` // pause | resume
}

// handleBrakemanRoleAction pauses or resumes the role: {action} → {role}.
func (s *Server) handleBrakemanRoleAction(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	var body brakemanRoleBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !d.recovered.Load() {
		s.brakemanError(w, d.notOwner())
		return
	}
	var rec crew.RoleRecord
	var err error
	switch body.Action {
	case "pause":
		// Marked under admit before it is written: no dequeue starts a
		// wake request queued behind a person's turn from here on.
		d.admit.Lock()
		d.pausing.Add(1)
		d.admit.Unlock()
		rec, err = d.role.Pause()
		d.pausing.Add(-1)
	case "resume":
		rec, err = d.role.Resume()
		if err == nil {
			// A wake request the pause held starts now.
			d.poke()
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "action is pause or resume")
		return
	}
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	rec.Log = nil
	writeJSON(w, http.StatusOK, map[string]any{"role": rec})
}

// brakemanNoteBody is POST /api/brakeman/role/notes's body: a brief's
// item to keep as a note.
type brakemanNoteBody struct {
	Request string `json:"request"`
	Item    int    `json:"item"`
}

// handleBrakemanKeepNote keeps a fact's or an interpretation's text of a
// kept brief as one of the role's notes: {request, item} → {role}; 400
// not_a_note for another kind of item; 413 bound past the notes' 64 KiB.
func (s *Server) handleBrakemanKeepNote(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	var body brakemanNoteBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !brakemanIDPattern(body.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such request")
		return
	}
	if !d.recovered.Load() {
		s.brakemanError(w, d.notOwner())
		return
	}
	b, _, err := d.st.Brief(body.Request)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if body.Item < 0 || body.Item >= len(b.Items) {
		writeError(w, http.StatusNotFound, "not_found", "the brief has no such item")
		return
	}
	var text string
	switch it := b.Items[body.Item]; it.Kind {
	case brakeman.ItemFact:
		text = it.Fact.Text
	case brakeman.ItemInterpretation:
		text = it.Interpretation.Text
	default:
		writeError(w, http.StatusBadRequest, "not_a_note", "only a fact or an interpretation is kept as a note")
		return
	}
	rec, err := d.role.KeepNote(crew.RoleNote{Text: text, From: "brief:" + body.Request + "/" + strconv.Itoa(body.Item)})
	if err != nil {
		if strings.Contains(err.Error(), "KiB") || strings.Contains(err.Error(), "bytes") {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", err.Error())
			return
		}
		s.brakemanError(w, err)
		return
	}
	rec.Log = nil
	writeJSON(w, http.StatusOK, map[string]any{"role": rec})
}

// handleBrakemanClearNotes clears the role's notes: → {role}.
func (s *Server) handleBrakemanClearNotes(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	if !d.recovered.Load() {
		s.brakemanError(w, d.notOwner())
		return
	}
	rec, err := d.role.ClearNotes()
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	rec.Log = nil
	writeJSON(w, http.StatusOK, map[string]any{"role": rec})
}

// wakeResolver resolves wakes' subjects under the configuration cfg
// alone: what a source admits (admitted), before any request is made.
func (s *Server) wakeResolver(desk *brakeman.Store, cfg brakeman.Config) *refResolver {
	return &refResolver{s: s, desk: desk, snap: brakeman.SnapshotOf(cfg), grant: refGrant{scopes: []brakeman.Scope{cfg.Scope}, vis: []brakeman.Visibility{cfg.Visibility}},
		at: time.Now().UTC(), seen: map[brakeman.Ref]brakeman.Read{}, runs: map[string]*refRun{}, tops: map[string]string{}}
}

// whole reports whether the grant's scopes are all of it: no selector
// narrows any of them.
func (g refGrant) whole() bool {
	return !slices.ContainsFunc(g.scopes, func(s brakeman.Scope) bool {
		return len(s.Projects)+len(s.Repos)+len(s.Crews)+len(s.Runs)+len(s.Sessions) > 0
	})
}

// admitsSubject reports whether the grant admits a wake's subject: its
// class visible, and, under a scope that narrows, the record it names in
// the scope as it is now (Scope.Admits); a subject that names nothing (a
// briefing time's) is admitted. An error when the record cannot be read.
func (rr *refResolver) admitsSubject(ctx context.Context, subject string) (bool, error) {
	if subject == "" {
		return true, nil
	}
	kind, _, _ := strings.Cut(subject, ":")
	if !rr.grant.sees(refClass(kind)) {
		return false, nil
	}
	if rr.grant.whole() {
		return true, nil
	}
	ref, err := brakeman.ParseRef(subject)
	if err != nil {
		return false, nil
	}
	f, err := rr.lookup(ctx, ref)
	if err != nil {
		return false, err
	}
	return rr.grant.admits(f.item), nil
}

// visibleBatch is the batch b as a turn under the resolver's grant (the
// scope and visibility its request began under and the configuration's
// now, together) reads it: the wakes whose subjects the grant admits, a
// subject that cannot be read now left out; and its overflow, which names
// no subject, only when the grant narrows nothing a wake is of.
func (rr *refResolver) visibleBatch(ctx context.Context, b crew.Batch) *crew.Batch {
	out := b
	// The configuration generation is the role's bookkeeping, not the
	// turn's to read.
	out.Gen = 0
	out.Wakes, out.Overflow = []crew.BatchWake{}, nil
	for _, w := range b.Wakes {
		if ok, err := rr.admitsSubject(ctx, w.Subject); ok && err == nil {
			out.Wakes = append(out.Wakes, w)
		}
	}
	if b.Overflow != nil && rr.grant.whole() && rr.grant.sees(brakeman.ClassRuns) && rr.grant.sees(brakeman.ClassSessions) && rr.grant.sees(brakeman.ClassDispatch) {
		o := *b.Overflow
		out.Overflow = &o
	}
	return &out
}
