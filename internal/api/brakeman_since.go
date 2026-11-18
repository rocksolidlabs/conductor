package api

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// GET /api/brakeman/since: Conductor's own counts since the last briefing
// kept, for the Brakeman page's Now card. No agent turn runs for it and
// it spends no budget: the four kinds are the background role's wake
// sources (internal/crew/role.go), each read with the rule the role and
// the changes read already have, over the admin's whole view. It writes
// nothing.

const (
	// maxBrakemanSince bounds the since read's reply.
	maxBrakemanSince = 8 << 10
	// sinceRefs is how many refs each kind of the reply names.
	sinceRefs = 5
	// sinceValueReads bounds the status values one since read takes of
	// Dispatch's change feed (ChangeValue); past it the reply says
	// truncated.
	sinceValueReads = 200
	// sinceCacheFor is how long one since read's reply is answered again
	// for the same query, on the desk's clock; sinceCacheKeep how many
	// such replies are kept.
	sinceCacheFor  = 5 * time.Second
	sinceCacheKeep = 8
)

// The since read's starts.
const (
	sinceFromBriefing = "briefing"
	sinceFromDay      = "day"
	sinceFromGiven    = "given"
)

// brakemanSince is GET /api/brakeman/since's reply.
type brakemanSince struct {
	Since         sinceStart `json:"since"`
	RunsEnded     sinceCount `json:"runsEnded"`
	TasksFailed   sinceCount `json:"tasksFailed"`
	TicketsReady  sinceCount `json:"ticketsReady"`
	QuestionsOpen sinceCount `json:"questionsOpen"`
	// Truncated says Dispatch's changes since then were more than the
	// window read (changesWindow) or than sinceValueReads values.
	Truncated bool `json:"truncated"`
}

// sinceStart is where the counts start: the last briefing kept, the last
// day (no briefing kept) or the query's time.
type sinceStart struct {
	From     string    `json:"from"`
	Briefing string    `json:"briefing,omitempty"`
	At       time.Time `json:"at"`
}

// sinceCount is one kind's count and at most sinceRefs of its refs, the
// newest first (a question's: the longest waiting first), and for the
// questions when the oldest was asked.
type sinceCount struct {
	Count  int        `json:"count"`
	Refs   []string   `json:"refs"`
	Oldest *time.Time `json:"oldest,omitempty"`
}

// sinceCache is the since read's replies by query, each kept for
// sinceCacheFor and only while no event has been told since it was
// computed: each is marked with the hub's count of events told
// (eventHub.told) read before its counts were, so a read made after an
// event the page reads again on (run, dispatch, brakeman, session,
// removed) is never answered with a reply from before it.
type sinceCache struct {
	mu      sync.Mutex
	replies map[string]sinceCached
}

type sinceCached struct {
	at   time.Time
	told uint64
	body []byte
}

// get is the reply kept for key, made no longer than sinceCacheFor
// before now with no event told since (told, the hub's count now).
func (c *sinceCache) get(key string, now time.Time, told uint64) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.replies[key]
	if !ok || e.told != told || now.Sub(e.at) >= sinceCacheFor || now.Before(e.at) {
		return nil, false
	}
	return e.body, true
}

// put keeps body for key, computed after told events, the oldest of
// sinceCacheKeep dropped; never over a reply computed after more.
func (c *sinceCache) put(key string, now time.Time, told uint64, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.replies == nil {
		c.replies = map[string]sinceCached{}
	}
	had, ok := c.replies[key]
	if ok && had.told > told {
		return
	}
	if !ok && len(c.replies) >= sinceCacheKeep {
		oldest := ""
		for k, e := range c.replies {
			if oldest == "" || e.at.Before(c.replies[oldest].at) {
				oldest = k
			}
		}
		delete(c.replies, oldest)
	}
	c.replies[key] = sinceCached{at: now, told: told, body: body}
}

// handleBrakemanSince answers GET /api/brakeman/since[?since=]: the
// counts since the time given, else since the last briefing kept (the
// start the next "Brief me" reads from), else over the last day.
// Refused (400 invalid_request) for any other parameter, a since that is
// not RFC 3339, or one after now. The query string is never logged.
func (s *Server) handleBrakemanSince(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	q, err := readQuery(r, "since")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	given, err := q.one("since")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	now := d.now().UTC()
	var start sinceStart
	if _, set := q["since"]; set {
		at, err := time.Parse(time.RFC3339Nano, given)
		switch {
		case err != nil:
			writeError(w, http.StatusBadRequest, "invalid_request", "since is a time in RFC 3339")
			return
		case at.After(now):
			writeError(w, http.StatusBadRequest, "invalid_request", "since is after now")
			return
		}
		start = sinceStart{From: sinceFromGiven, At: at.UTC()}
	}
	key := "since=" + given
	// The events told so far, read before anything the reply counts.
	told := s.events.told.Load()
	if body, ok := d.live.since.get(key, now, told); ok {
		writeRawJSON(w, body)
		return
	}
	if given == "" {
		if start, err = d.sinceStartNow(now); err != nil {
			s.brakemanError(w, err)
			return
		}
	}
	out, err := s.sinceCounts(r.Context(), start)
	if f := d.live.sinceCounted.Load(); f != nil {
		(*f)()
	}
	if err != nil {
		s.log.Warn("Brakeman's since read failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the counts could not be read")
		return
	}
	body, err := json.Marshal(out)
	if err != nil || len(body) > maxBrakemanSince {
		writeError(w, http.StatusInternalServerError, "bound", fmt.Sprintf("the counts are larger than their %d bytes", maxBrakemanSince))
		return
	}
	d.live.since.put(key, now, told, body)
	writeRawJSON(w, body)
}

// writeRawJSON writes body, JSON already encoded, with 200.
func writeRawJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

// sinceStartNow is the start of the counts with no time given: the last
// briefing kept under the configuration now, as the next "Brief me"
// reads from it (lastBriefing), else the last day.
func (d *brakemanDesk) sinceStartNow(now time.Time) (sinceStart, error) {
	cfg, err := d.st.Config()
	if err != nil {
		return sinceStart{}, err
	}
	b, err := d.lastBriefing(brakeman.Request{}, grantOf(brakeman.SnapshotOf(cfg), cfg))
	if err != nil {
		return sinceStart{}, err
	}
	if b == nil || b.At.IsZero() {
		return sinceStart{From: sinceFromDay, At: now.Add(-defaultBriefingWindow)}, nil
	}
	return sinceStart{From: sinceFromBriefing, Briefing: b.ID, At: b.At.UTC()}, nil
}

// sinceItem is one thing counted: its ref and when it happened.
type sinceItem struct {
	ref string
	at  time.Time
}

// sinceCounts counts what happened after start.At.
func (s *Server) sinceCounts(ctx context.Context, start sinceStart) (brakemanSince, error) {
	out := brakemanSince{Since: start}
	runs, tasks, err := s.sinceRuns(ctx, start.At)
	if err != nil {
		return out, err
	}
	out.RunsEnded, out.TasksFailed = countOf(runs, false), countOf(tasks, false)
	tickets, truncated, err := s.sinceTickets(ctx, start.At)
	if err != nil {
		return out, err
	}
	out.TicketsReady, out.Truncated = countOf(tickets, false), truncated
	qs := s.questionsOpen()
	out.QuestionsOpen = countOf(qs, true)
	if len(qs) > 0 {
		// countOf sorted them, the longest waiting first.
		oldest := qs[0].at
		out.QuestionsOpen.Oldest = &oldest
	}
	return out, nil
}

// countOf is items' count and the first sinceRefs refs, the newest first,
// or the oldest first when oldestFirst.
func countOf(items []sinceItem, oldestFirst bool) sinceCount {
	slices.SortStableFunc(items, func(a, b sinceItem) int {
		if oldestFirst {
			return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.ref, b.ref))
		}
		return cmp.Or(b.at.Compare(a.at), cmp.Compare(a.ref, b.ref))
	})
	c := sinceCount{Count: len(items), Refs: []string{}}
	for _, it := range items[:min(len(items), sinceRefs)] {
		c.Refs = append(c.Refs, it.ref)
	}
	return c
}

// sinceRuns is the runs that ended after since and the tasks that failed
// after it, Brakeman's own runs left out (as the role's sources leave
// them, runWakes). A run ended when its ledger row closed, or, for a run
// of no ledger, when it stopped or finished (StoppedAt). A task failed
// when it is failed now and its last attempt closed after since.
func (s *Server) sinceRuns(ctx context.Context, since time.Time) (runs, tasks []sinceItem, err error) {
	known := map[string]crew.Run{}
	for _, r := range s.runs.List() {
		known[r.ID] = r
	}
	if s.records != nil {
		recs, err := s.records.List("")
		if err != nil {
			return nil, nil, fmt.Errorf("the run records: %w", err)
		}
		for _, r := range recs {
			if _, ok := known[r.ID]; !ok {
				known[r.ID] = r
			}
		}
	}
	brakemans := func(id string) bool { r, ok := known[id]; return ok && r.Class == crew.ClassBrakeman }
	inLedger := map[string]bool{}
	if s.ledger != nil {
		rows, err := s.ledger.Runs(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("the ledger's runs: %w", err)
		}
		for _, row := range rows {
			inLedger[row.ID] = true
			if brakemans(row.ID) || (!row.ClosedAt.IsZero() && !row.ClosedAt.After(since)) {
				// Closed before since: nothing of it is counted.
				continue
			}
			if !row.ClosedAt.IsZero() {
				runs = append(runs, sinceItem{ref: brakeman.RefRun + ":" + row.ID, at: row.ClosedAt})
			}
			failed, err := s.failedSince(ctx, row.ID, since)
			if err != nil {
				return nil, nil, err
			}
			tasks = append(tasks, failed...)
		}
	}
	for id, r := range known {
		if inLedger[id] || r.Class == crew.ClassBrakeman || r.StoppedAt == nil {
			continue
		}
		if (r.State == crew.RunStopped || r.State == crew.RunFinished) && r.StoppedAt.After(since) {
			runs = append(runs, sinceItem{ref: brakeman.RefRun + ":" + id, at: *r.StoppedAt})
		}
	}
	return runs, tasks, nil
}

// failedSince is the tasks of the managed run id that are failed now and
// whose last attempt closed after since.
func (s *Server) failedSince(ctx context.Context, id string, since time.Time) ([]sinceItem, error) {
	ts, err := s.ledger.Tasks(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("the tasks of %s: %w", id, err)
	}
	failed := map[string]bool{}
	for _, t := range ts {
		if t.State == ledger.TaskFailed {
			failed[t.ID] = true
		}
	}
	if len(failed) == 0 {
		return nil, nil
	}
	as, err := s.ledger.Attempts(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("the attempts of %s: %w", id, err)
	}
	last := map[string]ledger.AttemptRow{}
	for _, a := range as {
		if failed[a.TaskID] && a.N >= last[a.TaskID].N {
			last[a.TaskID] = a
		}
	}
	var out []sinceItem
	for task, a := range last {
		if a.ClosedAt.After(since) {
			out = append(out, sinceItem{ref: brakeman.RefTask + ":" + id + "/" + task, at: a.ClosedAt})
		}
	}
	return out, nil
}

// sinceTickets is the tickets whose status a change after since made
// ready, each once at its latest such change, read from the newest
// changesWindow of Dispatch's changes; truncated when older changes may
// fall after since too, or when more than sinceValueReads status values
// would be read.
func (s *Server) sinceTickets(ctx context.Context, since time.Time) ([]sinceItem, bool, error) {
	ds := s.dispatch
	if ds == nil {
		return nil, false, nil
	}
	head, err := ds.Head(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("the head of Dispatch's changes: %w", err)
	}
	after := max(0, head-changesWindow)
	truncated, values, first := false, 0, true
	ready := map[string]time.Time{}
	page := int(min(changesWindow, int64(dispatchFeedPage)))
read:
	for after < head {
		rows, err := ds.ChangesAfter(ctx, after, page)
		if err != nil {
			return nil, false, fmt.Errorf("the changes of Dispatch: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		if first && after > 0 && rows[0].At.After(since) {
			// The window's first change is already after since: older
			// ones, not read, may be too.
			truncated = true
		}
		first = false
		for _, c := range rows {
			after = c.Seq
			if !c.At.After(since) {
				continue
			}
			if c.Entity != dispatch.EntityTicket || c.Field != "status" || c.Ticket == "" {
				continue
			}
			if values == sinceValueReads {
				truncated = true
				break read
			}
			values++
			status, err := ds.ChangeValue(ctx, c.Seq)
			if err != nil {
				return nil, false, fmt.Errorf("a ticket's status change: %w", err)
			}
			if status == dispatch.TicketReady && c.At.After(ready[c.Ticket]) {
				ready[c.Ticket] = c.At
			}
		}
	}
	out := make([]sinceItem, 0, len(ready))
	for key, at := range ready {
		out = append(out, sinceItem{ref: brakeman.RefTicket + ":" + key, at: at})
	}
	return out, truncated, nil
}

// questionsOpen is every question open now (needs_input, not answered
// in its terminal), Brakeman's own sessions left out, with when it was
// asked.
func (s *Server) questionsOpen() []sinceItem {
	brakemans := map[string]bool{}
	for _, r := range s.runs.List() {
		if r.Class == crew.ClassBrakeman {
			brakemans[r.ID] = true
		}
	}
	var out []sinceItem
	for _, info := range s.registry.List() {
		a := info.Attention
		if a.State != session.AttentionNeedsInput || a.Since == nil || a.Answered != "" {
			continue
		}
		if info.Crew != nil && brakemans[info.Crew.RunID] {
			continue
		}
		ref := brakeman.RefSession + ":" + info.ID
		if a.ID != "" {
			ref = brakeman.RefQuestion + ":" + info.ID + "/" + a.ID
		}
		out = append(out, sinceItem{ref: ref, at: a.Since.UTC()})
	}
	return out
}
