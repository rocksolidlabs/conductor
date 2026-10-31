package crew

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// Records keeps runs that ended, one JSON document each under runs/ in the
// data directory, so the Crews page can chart a crew's last runs after the
// engine, or the server, has forgotten them: the run as GET /api/runs/{run}
// answers it (members, their times, states, diffs and agent sessions, the
// log), never a terminal's contents. At most maxRecords records of runs the
// engine no longer holds are kept; a save past that drops the oldest of
// those by start time, never a held run's: its record follows it from its
// launch on (Engine.Journal), and would otherwise be the oldest, dropped and
// written again at the run's next change, with no record of the run for a
// server that ended in between. A managed run's record (Run.Managed) is a
// cache of the ledger: never counted, never dropped here, nor marked
// interrupted by reconcile; the ledger's owner prunes it with the run's
// rows, once the run is closed and nothing cites it.
type Records struct {
	st     *store.Store
	mu     sync.Mutex
	closed bool
	// held reports whether the engine still holds the run with the given ID
	// (RecordEnds sets it): prune leaves its record alone and never counts
	// it. It is called under mu on the goroutine that saves, which may be the
	// one forgetting a run under the engine's lock (journalForgotten), so it
	// must not take that lock.
	held func(runID string) bool
}

// Close stops the records: a save after it is dropped (the server is
// shutting down, or a test's directories are going).
func (rs *Records) Close() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.closed = true
}

// maxRecords bounds the records kept.
const maxRecords = 500

// maxRecordSize bounds one record, written and read back: a run with 200
// log lines is well under it, and Save trims the chat and the log of one
// that is not, newest kept, so every record written is one the listing
// reads. maxGuardSize bounds the read Save makes of the record it replaces,
// for its revision: wider, so a record too large to list (an older
// Conductor's) still keeps a managed snapshot from going back.
const (
	maxRecordSize = 4 << 20
	maxGuardSize  = 64 << 20
)

// NewRecords opens runs/ under st and reconciles what it holds: the records
// of the runs that were running when the server last stopped (journaled
// since their launch, never ended) are marked interrupted, their sessions
// gone with that server, never relaunched on their own (a resume from the
// record is the person's, ResumeRecord); a record whose continuation (a run
// resumed from it, ResumedFrom) has a record while it does not name one
// (the server ended between the two writes) is marked with it, so that it
// is not resumed twice; and a record naming a continuation no record holds
// (the server ended the other way round) has the mark cleared, so that it
// can be resumed. A record that cannot be read or marked is left as it is.
func NewRecords(st *store.Store) (*Records, error) {
	sub, err := st.Sub("runs")
	if err != nil {
		return nil, err
	}
	rs := &Records{st: sub}
	return rs, rs.reconcile()
}

// reconcile is what NewRecords does to the records it finds.
func (rs *Records) reconcile() error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	entries, err := rs.st.List()
	if err != nil {
		return err
	}
	files, err := rs.load(entries)
	if err != nil {
		return err
	}
	byID := map[string]int{}
	for i, f := range files {
		if f.ok {
			byID[f.run.ID] = i
		}
	}
	changed := map[int]bool{}
	for i, f := range files {
		if !f.ok {
			continue
		}
		if f.run.Managed != nil {
			// The ledger says what became of it (ledger.Recover).
			continue
		}
		if f.run.StoppedAt == nil && (f.run.State == RunRunning || f.run.State == RunNeedsInput) {
			r := &files[i].run
			r.State, r.NeedsInput = RunInterrupted, 0
			for j := range r.Members {
				if m := &r.Members[j]; m.Status == MemberRunning || m.Status == MemberStarting {
					m.Status, m.NeedsInput = MemberEnded, false
					m.Err = "interrupted: the server stopped while it ran"
				}
			}
			changed[i] = true
		}
		if from := f.run.ResumedFrom; from != "" {
			if j, ok := byID[from]; ok && files[j].run.ResumedBy == "" {
				files[j].run.ResumedBy = f.run.ID
				changed[j] = true
			}
		}
	}
	for i, f := range files {
		if by := f.run.ResumedBy; f.ok && by != "" {
			if _, ok := byID[by]; !ok {
				files[i].run.ResumedBy = ""
				changed[i] = true
			}
		}
	}
	var errs []error
	for i := range changed {
		errs = append(errs, rs.st.Save(files[i].run.ID+".json", files[i].run))
	}
	return errors.Join(errs...)
}

// Save writes r's record, replacing an earlier one of the same run (a
// stopped run that was reopened and ended again), and drops the oldest past
// maxRecords. A managed run's record never goes back: a save with a
// revision behind the one on disk, or with no revision at all, is dropped,
// so a late snapshot cannot undo what the ledger has
// (docs/plans/agent-harness-order.md, decision 7).
func (rs *Records) Save(r Run) error {
	if !ValidRunID(r.ID) {
		return fmt.Errorf("records: bad run id %q", r.ID)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closed {
		return nil
	}
	// A managed record on disk takes only a newer managed snapshot: an
	// older one, or a legacy snapshot of the same run (a journal save
	// queued before the run was known managed), is dropped. A record that
	// cannot be read is not written over: its revision is unknown.
	var have Run
	ok, err := rs.st.LoadLimit(r.ID+".json", &have, maxGuardSize)
	if err != nil {
		return fmt.Errorf("records: %s.json cannot be read, so it is not replaced: %w", r.ID, err)
	}
	if ok && have.Managed != nil && (r.Managed == nil || have.Managed.Revision > r.Managed.Revision) {
		return nil
	}
	r, err = fit(r)
	if err != nil {
		return err
	}
	if err := rs.st.Save(r.ID+".json", r); err != nil {
		return err
	}
	return rs.prune()
}

// fit trims r to a record the listing reads (maxRecordSize): the oldest
// half of the chat and of the log go, again until it fits. A record that
// does not fit without them is refused.
func fit(r Run) (Run, error) {
	for {
		b, err := store.Encode(r)
		if err != nil {
			return r, err
		}
		if int64(len(b)) <= maxRecordSize {
			return r, nil
		}
		if len(r.Chat) == 0 && len(r.Log) == 0 {
			return r, fmt.Errorf("records: %s.json would be more than %d bytes", r.ID, maxRecordSize)
		}
		r.Chat = slices.Clone(r.Chat[(len(r.Chat)+1)/2:]) // one left goes too
		r.Log = slices.Clone(r.Log[(len(r.Log)+1)/2:])
	}
}

// Delete removes a run's record: the ledger's owner, once the run's rows
// are gone (ledger.Prune). Nothing when there is none.
func (rs *Records) Delete(runID string) error {
	if !ValidRunID(runID) {
		return fmt.Errorf("records: bad run id %q", runID)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closed {
		return nil
	}
	err := rs.st.Delete(runID + ".json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// prune drops the oldest records past maxRecords among those of runs the
// engine no longer holds (held) and that are not managed; a held run's
// record and a managed run's are left alone and not counted. The caller
// holds rs.mu.
func (rs *Records) prune() error {
	entries, err := rs.st.List()
	if err != nil {
		return err
	}
	if len(entries) <= maxRecords {
		return nil
	}
	all, err := rs.load(entries)
	if err != nil {
		return err
	}
	// Kept: a managed record, one the engine holds, and one too large to
	// read, whose content is unknown (an older Conductor's: Save trims).
	all = slices.DeleteFunc(all, func(f recordFile) bool {
		return (f.ok && f.run.Managed != nil) || f.large || (rs.held != nil && rs.held(strings.TrimSuffix(f.name, ".json")))
	})
	if len(all) <= maxRecords {
		return nil
	}
	// Oldest first by start time; a record that cannot be read goes first.
	slices.SortFunc(all, func(a, b recordFile) int {
		return cmp.Or(cmp.Compare(boolInt(a.ok), boolInt(b.ok)), a.run.StartedAt.Compare(b.run.StartedAt), cmp.Compare(a.name, b.name))
	})
	var errs []error
	for _, f := range all[:len(all)-maxRecords] {
		errs = append(errs, rs.st.Delete(f.name))
	}
	return errors.Join(errs...)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type recordFile struct {
	name  string
	run   Run
	ok    bool
	large bool // not read because it is over maxRecordSize
}

// load reads the records named, keeping one that cannot be read as not ok.
func (rs *Records) load(entries []store.Entry) ([]recordFile, error) {
	out := make([]recordFile, 0, len(entries))
	for _, en := range entries {
		var r Run
		ok, err := rs.st.LoadLimit(en.Name, &r, maxRecordSize)
		if err != nil || !ok || r.ID+".json" != en.Name {
			out = append(out, recordFile{name: en.Name, large: errors.Is(err, store.ErrTooLarge)})
			continue
		}
		out = append(out, recordFile{name: en.Name, run: r, ok: true})
	}
	return out, nil
}

// List returns the records, newest first by start time; those of one crew
// when crewID is not empty. A record that cannot be read is left out.
func (rs *Records) List(crewID string) ([]Run, error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	entries, err := rs.st.List()
	if err != nil {
		return nil, err
	}
	files, err := rs.load(entries)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(files))
	for _, f := range files {
		if f.ok && (crewID == "" || f.run.CrewID == crewID) {
			out = append(out, f.run)
		}
	}
	slices.SortFunc(out, func(a, b Run) int { return cmp.Or(b.StartedAt.Compare(a.StartedAt), strings.Compare(b.ID, a.ID)) })
	return out, nil
}

// Get returns one record.
func (rs *Records) Get(runID string) (Run, bool, error) {
	if !ValidRunID(runID) {
		return Run{}, false, nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var r Run
	ok, err := rs.st.LoadLimit(runID+".json", &r, maxRecordSize)
	if err != nil || !ok || r.ID != runID {
		return Run{}, false, err
	}
	return r, true, nil
}

// ValidRunID reports whether id has a run id's shape: a crew id, a dash and
// eight hex digits (newRunID).
func ValidRunID(id string) bool {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 || len(id)-i-1 != 8 {
		return false
	}
	for _, c := range id[i+1:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return ValidID(id[:i])
}

// noteEnd tells OnEnd about r when it has ended: stopped, or every member
// ended with none pending (runState). It takes the engine's lock itself, so
// the caller must not hold it; OnEnd runs without it. A run being stopped is
// told about by its stop, once stopped: its members' sessions ending on the
// way do not make it finished. The ends reach OnEnd in the order their
// snapshots were taken (endMu).
func (e *Engine) noteEnd(runID string) {
	if e.OnEnd == nil {
		return
	}
	e.endMu.Lock()
	defer e.endMu.Unlock()
	e.mu.Lock()
	cur, ok := e.runs[runID]
	midStop := ok && cur.stopping && cur.stoppedAt == nil
	// A managed run's end is told once the change that ended it is
	// committed (transact): a session ending meanwhile does not tell it,
	// and nothing is told of one whose row the ledger has not as its own
	// (unrecorded). The snapshot is taken under the same hold, so what is
	// told is what was judged committed.
	pending := ok && cur.managed && (cur.endPending || cur.unrecorded())
	var r Run
	if ok && !midStop && !pending {
		r = cur.snapshot(false, true)
	}
	e.mu.Unlock()
	if !ok || midStop || pending {
		return
	}
	e.refresh(&r)
	if r.State != RunStopped && r.State != RunFinished {
		return
	}
	e.OnEnd(r)
}

// RecordEnds wires OnEnd to rs: each run that ends is saved on a goroutine
// of its own, started through run (the server's tracked goroutines, which
// its shutdown waits for; a plain go when nil), and a save that fails is
// logged by warn. Journal saves the run as it is now the same way, for the
// caller to make from its launch on (the server, after each change, a
// while apart), so that a run cut short by the server's end has a record,
// marked interrupted as the records open next.
//
// The saves run on goroutines of their own, so a later end of a run (a run
// reopened and ended again) could be saved before an earlier one: each
// snapshot is numbered as it is handed in (the ends in the order noteEnd
// took them), and a save older than the one already written for its run is
// dropped.
func (e *Engine) RecordEnds(rs *Records, warn func(runID string, err error), run func(func())) {
	if run == nil {
		run = func(f func()) { go f() }
	}
	rs.mu.Lock()
	rs.held = e.holds
	rs.mu.Unlock()
	type pending struct {
		saved    uint64 // the newest end written
		inflight int    // ends not yet saved or dropped; the entry goes at zero
	}
	var (
		mu    sync.Mutex
		seq   uint64
		byRun = map[string]*pending{}
		// waiting counts the ends not yet written or dropped, every run's;
		// idle is closed while it is zero, and made anew as it rises.
		waiting int
		idle    = make(chan struct{})
	)
	close(idle)
	e.recordsIdle = func() <-chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		return idle
	}
	enqueue := func(r Run) {
		mu.Lock()
		seq++
		n := seq
		p := byRun[r.ID]
		if p == nil {
			p = &pending{}
			byRun[r.ID] = p
		}
		p.inflight++
		if waiting++; waiting == 1 {
			idle = make(chan struct{})
		}
		mu.Unlock()
		run(func() {
			mu.Lock()
			defer mu.Unlock()
			if n > p.saved {
				if err := rs.Save(r); err != nil && warn != nil {
					warn(r.ID, err)
				}
				p.saved = n
			}
			if p.inflight--; p.inflight == 0 {
				delete(byRun, r.ID)
			}
			if waiting--; waiting == 0 {
				close(idle)
			}
		})
	}
	e.OnEnd = enqueue
	e.journal = enqueue
}

// Journal saves the run with the given ID as it is now, through the records
// RecordEnds wired (nothing without them, or for a run the engine does not
// hold): the server calls it a while after each change of a run. The
// snapshot and its place in the saves are one step under endMu, as an end's
// are (noteEnd), so a snapshot taken before an end never lands after it.
func (e *Engine) Journal(runID string) {
	if e.journal == nil {
		return
	}
	e.endMu.Lock()
	defer e.endMu.Unlock()
	if r, ok := e.getRecorded(runID); ok {
		e.journal(r)
	}
}

// getRecorded is Get for a run whose record may be written: not one the
// ledger has no row of as its own yet (unrecorded), whose id may be
// another run's.
func (e *Engine) getRecorded(runID string) (Run, bool) {
	e.mu.Lock()
	r, ok := e.runs[runID]
	ok = ok && !r.unrecorded()
	e.mu.Unlock()
	if !ok {
		return Run{}, false
	}
	return e.Get(runID)
}

// WaitRecorded waits, as long as ctx allows, until every end handed to the
// records so far (RecordEnds) is written or dropped, and reports whether it
// is so; true at once when none waits, or no records are kept. The server's
// shutdown closes the records after it: a run stopped as the shutdown's
// budget ran out is recorded all the same, a record being a file written in
// a moment.
func (e *Engine) WaitRecorded(ctx context.Context) bool {
	if e.recordsIdle == nil {
		return true
	}
	select {
	case <-e.recordsIdle():
		return true
	case <-ctx.Done():
		return false
	}
}
