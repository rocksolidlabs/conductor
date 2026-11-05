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
	// ticketSeq is how far Dispatch's change feed was read; set from the
	// first change a commit announces after the desk opened.
	ticketSeq   int64
	ticketBegun bool
	// paused are the runs last seen paused: a pause wakes the role as the
	// run becomes paused, not at each change while it stays so.
	paused map[string]bool
}

// openRole makes the desk's role and wires Dispatch's changes to it; the
// engine's run changes reach it through runChanged (the server's
// OnRunChange) and the activity through its sink (activity), which New
// adds before the engine's.
func (d *brakemanDesk) openRole() {
	d.src = &roleSources{runs: map[string]bool{}, questions: map[string]bool{}, paused: map[string]bool{}}
	d.role = crew.NewRole(brakemanMember, roleRecords{d: d}, d.now)
	if d.s.dispatch != nil {
		d.s.dispatch.OnChange(d.ticketsChanged)
	}
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
// session. It runs where the entry was recorded, so it never waits.
func (d *brakemanDesk) activity(sessionID string, e session.ActivityEntry, state session.AttentionState) {
	if e.Type != session.ActivityAttention || state != session.AttentionNeedsInput {
		return
	}
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
	if !d.src.ticketBegun && len(changes) > 0 {
		// The feed is read from the first commit announced after the desk
		// opened: what was there before wakes nothing.
		d.src.ticketBegun = true
		for i, c := range changes {
			if i == 0 || c.Seq-1 < d.src.ticketSeq {
				d.src.ticketSeq = max(0, c.Seq-1)
			}
		}
	}
	d.src.tickets = d.src.tickets || len(changes) > 0
	d.src.mu.Unlock()
	d.role.Poke()
}

// drainSources turns what the sources noted into wakes.
func (d *brakemanDesk) drainSources() {
	d.src.mu.Lock()
	runs, questions, tickets := d.src.runs, d.src.questions, d.src.tickets
	d.src.runs, d.src.questions, d.src.tickets = map[string]bool{}, map[string]bool{}, false
	d.src.mu.Unlock()
	cfg, err := d.st.Config()
	if err != nil || !cfg.Background() {
		return
	}
	for id := range runs {
		run, ok := d.s.runs.Get(id)
		if !ok || d.madeByTurn(run) {
			continue
		}
		for _, w := range d.runWakes(run) {
			d.roleWake(w)
		}
	}
	for sid := range questions {
		d.holdQuestion(sid, time.Duration(cfg.Wake.QuestionMinutes)*time.Minute)
	}
	if tickets {
		d.readTickets()
	}
}

// roleWake hands a wake to the role, saying why it was not taken.
func (d *brakemanDesk) roleWake(w crew.RoleWake) {
	if err := d.role.Wake(w); err != nil && !errors.Is(err, errDeskClosing) {
		d.s.log.Warn("Brakeman's role could not note a wake", "source", w.Source, "subject", w.Subject, "err", err)
	}
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

// madeByTurn reports whether a run is what a Brakeman request made: a
// ticket's launch an act of a turn's ran (its launch the act's receipt).
func (d *brakemanDesk) madeByTurn(run crew.Run) bool {
	if run.Ticket == nil || run.Ticket.Launch == "" {
		return false
	}
	page, _, err := d.st.List("", brakeman.MaxPage)
	if err != nil {
		return false
	}
	for _, r := range page {
		if slices.ContainsFunc(r.Acts, func(a brakeman.Act) bool { return a.Receipt == run.Ticket.Launch }) {
			return true
		}
	}
	return false
}

// holdQuestion holds a wake for the session's open question, due once it
// has been open for threshold, if it is still the one open then. A
// Brakeman turn's session's question wakes nothing.
func (d *brakemanDesk) holdQuestion(sid string, threshold time.Duration) {
	drv, ok := d.s.registry.Get(sid)
	if !ok {
		return
	}
	info := drv.Info()
	att := info.Attention
	if (info.Crew != nil && info.Crew.Brakeman != nil) || att.State != session.AttentionNeedsInput || att.Since == nil {
		return
	}
	since := att.Since.UTC()
	subject := "session:" + sid
	if att.ID != "" {
		subject = "question:" + sid + "/" + att.ID
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
// a ticket whose status became ready wakes the role, unless Brakeman's
// actor or an op a request derived moved it.
func (d *brakemanDesk) readTickets() {
	ds := d.s.dispatch
	if ds == nil {
		return
	}
	d.src.mu.Lock()
	after := d.src.ticketSeq
	d.src.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range 20 {
		page, err := ds.ChangesAfter(ctx, after, 500)
		if err != nil {
			d.s.log.Warn("Brakeman's role could not read Dispatch's changes", "err", err)
			return
		}
		for _, c := range page {
			after = c.Seq
			if c.Entity != "ticket" || c.Field != "status" || c.Ticket == "" || c.Actor == brakemanMember || derivedOp.MatchString(c.OpID) {
				continue
			}
			t, err := ds.Ticket(ctx, c.Ticket)
			if err != nil || t.Status != dispatch.TicketReady {
				continue
			}
			d.roleWake(crew.RoleWake{Source: crew.WakeTicketReady, Subject: "ticket:" + c.Ticket, Key: "change " + strconv.FormatInt(c.Seq, 10), At: c.At})
		}
		d.src.mu.Lock()
		d.src.ticketSeq = max(d.src.ticketSeq, after)
		d.src.mu.Unlock()
		if len(page) < 500 {
			return
		}
	}
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
	rr.d.s.events.brakeman(roleEventID, rec.State)
	return nil
}

// roleEventID is the brakeman event's id for a change of the role.
const roleEventID = "role"

func (rr roleRecords) RoleSettings() crew.RoleSettings {
	cfg, err := rr.d.st.Config()
	if err != nil {
		return crew.RoleSettings{}
	}
	return crew.RoleSettings{On: cfg.Background(), Window: time.Duration(cfg.Wake.WindowSeconds) * time.Second,
		Cooldown: time.Duration(cfg.Wake.CooldownMinutes) * time.Minute, Briefings: cfg.Wake.Briefings, Location: time.Local}
}

// nextDay is the start of the local day after now's.
func nextDay(now time.Time) time.Time {
	l := now.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, time.Local)
}

// Fire makes the batch's request, kind wake, under the op id wake-<batch>
// and in admission's order, as a person's ask is kept: a batch fired
// again answers the request it made; a turn that cannot start is
// refused in words (Brakeman off, stuck, the day's turns or minutes
// spent), a queue full is tried again.
func (rr roleRecords) Fire(b crew.Batch) (string, error) {
	d := rr.d
	in := brakeman.Request{OpID: "wake-" + b.ID, Kind: brakeman.KindWake, Batch: b.ID}
	d.admit.Lock()
	defer d.admit.Unlock()
	if d.closing.Load() {
		return "", errDeskClosing
	}
	if !d.recovered.Load() {
		return "", d.notOwner()
	}
	if prev, found, err := d.st.ByOp(in.OpID); err == nil && found {
		return prev.ID, nil
	}
	cfg, err := d.st.Config()
	if err != nil {
		return "", err
	}
	if !cfg.Background() {
		return "", &crew.RoleRefused{Why: "Brakeman is off, or not in the background"}
	}
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stuck {
		return "", &crew.RoleRefused{Why: "the last turn's run did not stop: stop it from its run page"}
	}
	if len(d.queue) >= brakemanQueue {
		return "", fmt.Errorf("%d requests wait already", brakemanQueue)
	}
	turns, err := d.st.TurnsToday(now)
	if err != nil {
		return "", err
	}
	if turns+len(d.queue) >= cfg.Budget.TurnsPerDay {
		return "", &crew.RoleRefused{Why: fmt.Sprintf("the day's budget is spent: %d of %d turns, those queued counted", turns+len(d.queue), cfg.Budget.TurnsPerDay), RetryAt: nextDay(now)}
	}
	minutes, err := d.st.MinutesToday(now)
	if err != nil {
		return "", err
	}
	if left := cfg.Budget.MinutesPerDay - minutes; left < brakeman.MinReservation {
		return "", &crew.RoleRefused{Why: fmt.Sprintf("the day's budget is spent: %d of %d minutes", minutes, cfg.Budget.MinutesPerDay), RetryAt: nextDay(now)}
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
// logged-out CLI does) or over budget, unavailable in words; else closed.
// A request gone (pruned) is closed.
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
		return crew.RoleOutcome{Unavailable: "the turn could not start: " + r.Why}, nil
	case r.State == brakeman.StateOverBudget:
		return crew.RoleOutcome{Unavailable: "the day's budget is spent: " + r.Why, RetryAt: nextDay(rr.d.now())}, nil
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
	rec, err := d.role.Record()
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
	rec, err := d.role.Record()
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
		rec, err = d.role.Pause()
	case "resume":
		rec, err = d.role.Resume()
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
