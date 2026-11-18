package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// Brakeman's progress while a turn runs: what the turn in progress has
// read of its budget and what it read last, kept by the desk in memory
// only and answered on GET /api/brakeman (progress) and on the events
// stream (brakeman_progress). Never kept in a record.
//
// The read last admitted is noted as the turn's read is admitted
// (turnRead, after its admission seam), and the event is sent once the
// read is counted (after NoteAnswer), with the count the record holds
// then: at most one a second (brakemanProgressEvery), the first at once
// and the rest on the trailing edge, so the last read's count reaches
// the page without a GET. It is sent only while the request is starting
// or running, read under the desk's emit as the brakeman event is
// (publish), so no progress follows the event that closed the request.
// It is as droppable as chat and reaches no sink.

const (
	// maxBrakemanProgress bounds a brakeman_progress event's data: past
	// it the read's ref is left out.
	maxBrakemanProgress = 768
	// brakemanProgressEvery is the least time between two progress
	// events, on the desk's clock.
	brakemanProgressEvery = time.Second
	// maxProgressWhat bounds a reading's what.
	maxProgressWhat = 16
)

// brakemanProgress is GET /api/brakeman's progress, and the
// brakeman_progress event's data.
type brakemanProgress struct {
	ID           string              `json:"id"`
	Reads        int                 `json:"reads"`
	ReadsPerTurn int                 `json:"readsPerTurn"`
	StartedAt    *time.Time          `json:"startedAt,omitempty"`
	Deadline     *time.Time          `json:"deadline,omitempty"`
	Reading      *brakemanReadingNow `json:"reading,omitempty"`
}

// brakemanReadingNow is the read a turn made last: what it read (an
// observe operation's what) and the ref it named, when it named one.
type brakemanReadingNow struct {
	What string `json:"what"`
	Ref  string `json:"ref,omitempty"`
}

// brakemanLive is the desk's view of the turn in progress's reads: one
// entry, since one turn runs at a time, and the coalescer's marks; and
// the since read's replies (brakeman_since.go).
type brakemanLive struct {
	mu sync.Mutex
	// id is the request whose read was admitted last, reading that read.
	id      string
	reading *brakemanReadingNow
	// sentAt is when the last progress event was sent (or tried), on the
	// desk's clock; pending says a trailing send waits for its timer.
	sentAt  time.Time
	pending bool
	// progressAfter is the coalescer's timer: time.After, a test's own
	// otherwise (set under mu).
	progressAfter func(time.Duration) <-chan time.Time
	// trailed counts the trailing sends done, each once it sent or found
	// nothing to send: what a test waits on.
	trailed atomic.Uint64

	since sinceCache
	// sinceCounted, when a test sets it, runs in a since read once its
	// counts are computed and before its reply is kept.
	sinceCounted atomic.Pointer[func()]
}

// noteReading notes r, the read of request id just admitted, as the
// turn's read now. A read that is not an observe operation leaves the
// last one standing.
func (d *brakemanDesk) noteReading(id string, r *http.Request) {
	now := readingOf(r)
	d.live.mu.Lock()
	defer d.live.mu.Unlock()
	if d.live.id != id {
		d.live.id, d.live.reading = id, nil
	}
	if now != nil {
		d.live.reading = now
	}
}

// progressRead sends the progress of the request whose read was counted
// just now: at once when none was sent in the last second, else once
// the second is out (one trailing send, whatever the reads meanwhile).
func (d *brakemanDesk) progressRead() {
	l := &d.live
	l.mu.Lock()
	if l.pending {
		l.mu.Unlock()
		return
	}
	now := d.now()
	gap := now.Sub(l.sentAt)
	if l.sentAt.IsZero() || gap >= brakemanProgressEvery || gap < 0 {
		l.sentAt = now
		l.mu.Unlock()
		d.sendProgress()
		return
	}
	l.pending = true
	after := l.progressAfter
	l.mu.Unlock()
	if after == nil {
		after = time.After
	}
	fire := after(brakemanProgressEvery - gap)
	go d.trail(fire)
}

// trail is the trailing send, once fire fires; none once the desk ends.
func (d *brakemanDesk) trail(fire <-chan time.Time) {
	defer d.live.trailed.Add(1)
	select {
	case <-fire:
	case <-d.done:
		d.live.mu.Lock()
		d.live.pending = false
		d.live.mu.Unlock()
		return
	}
	d.live.mu.Lock()
	d.live.pending, d.live.sentAt = false, d.now()
	d.live.mu.Unlock()
	d.sendProgress()
}

// sendProgress sends the progress of the request read last, as its
// record holds it now, under emit: nothing for one no longer starting
// or running.
func (d *brakemanDesk) sendProgress() {
	d.emit.Lock()
	defer d.emit.Unlock()
	d.live.mu.Lock()
	id := d.live.id
	d.live.mu.Unlock()
	if id == "" {
		return
	}
	req, err := d.st.Get(id)
	if err != nil || !progressing(req) {
		return
	}
	d.s.events.brakemanProgress(d.progressOf(req, true))
}

// progressing reports whether req's turn is under way.
func progressing(req brakeman.Request) bool {
	return req.State == brakeman.StateStarting || req.State == brakeman.StateRunning
}

// progressOf is req's progress: its reads, its budget and its deadline
// from the record, and, for the owner of the records (own), when the
// turn began (its deadline less the minutes its charge reserved) and the
// read the desk noted last.
func (d *brakemanDesk) progressOf(req brakeman.Request, own bool) brakemanProgress {
	p := brakemanProgress{ID: req.ID, Reads: req.Reads, ReadsPerTurn: req.Config.Budget.ReadsPerTurn}
	if !req.Deadline.IsZero() {
		at := req.Deadline.UTC()
		p.Deadline = &at
	}
	if !own {
		return p
	}
	if m, ok := d.st.Reserved(req.ID); ok && p.Deadline != nil {
		at := p.Deadline.Add(-time.Duration(m) * time.Minute)
		p.StartedAt = &at
	}
	d.live.mu.Lock()
	if d.live.id == req.ID && d.live.reading != nil {
		r := *d.live.reading
		p.Reading = &r
	}
	d.live.mu.Unlock()
	return p
}

// liveState fills GET /api/brakeman's progress (for the request whose
// turn is under way; a read-only server's without its start or its read,
// which only the owner knows) and nextBriefing (the role's next briefing
// time, absent when the role is off, paused or has none).
func (s *Server) liveState(d *brakemanDesk, out *brakemanState) {
	if out.Current != nil && progressing(*out.Current) {
		p := d.progressOf(*out.Current, !out.ReadOnly)
		out.Progress = &p
	}
	if !out.Config.Background() || (out.Role != nil && out.Role.State == crew.RolePaused) {
		return
	}
	set, err := roleRecords{d: d}.RoleSettings()
	if err != nil {
		return
	}
	if next := crew.NextBriefing(set, d.now()); !next.IsZero() {
		at := next.UTC()
		out.NextBriefing = &at
	}
}

// observeWhatOf is each observe operation's what by its route's pattern
// as the mux has it ("GET /api/runs/{run}"): the guide's observe_<what>
// entries, one table.
var observeWhatOf = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	for _, op := range operationsTable() {
		if what, ok := strings.CutPrefix(op.Name, "observe_"); ok {
			out[op.Method+" "+op.Path] = what
		}
	}
	return out
})

// readingOf is the read r makes: its observe operation's what, and the
// ref its path (or a status or screen read's query) names, left out
// when it does not parse as a ref and for reads of a listing; nil for a
// route that is no observe operation.
func readingOf(r *http.Request) *brakemanReadingNow {
	what := observeWhatOf()[r.Pattern]
	if what == "" || len(what) > maxProgressWhat {
		return nil
	}
	v := r.PathValue
	q := r.URL.Query()
	var ref string
	switch what {
	case "run", "messages", "candidates", "comparison":
		ref = brakeman.RefRun + ":" + v("run")
	case "task":
		ref = brakeman.RefTask + ":" + v("run") + "/" + v("task")
	case "steps":
		ref = brakeman.RefAttempt + ":" + v("run") + "/" + v("task") + "/" + v("n")
	case "log":
		ref = brakeman.RefCheck + ":" + v("run") + "/" + v("task") + "/" + v("n") + "/" + v("id") + "/" + v("k")
	case "command":
		ref = brakeman.RefCommand + ":" + v("run") + "/" + v("command")
	case "ticket", "history", "comments", "plans":
		ref = brakeman.RefTicket + ":" + v("key")
	case "capture":
		ref = brakeman.RefCapture + ":" + v("id")
	case "capture_send":
		ref = brakeman.RefCapture + ":" + v("id") + "/" + v("send")
	case "screen":
		if id := q.Get("session"); id != "" {
			ref = brakeman.RefSession + ":" + id
		}
	case "status":
		if run := q.Get("run"); run != "" {
			ref = brakeman.RefRun + ":" + run
		} else if id := q.Get("session"); id != "" {
			ref = brakeman.RefSession + ":" + id
		}
	}
	if ref != "" {
		if p, err := brakeman.ParseRef(ref); err == nil {
			ref = p.String()
		} else {
			ref = ""
		}
	}
	return &brakemanReadingNow{What: what, Ref: ref}
}

// brakemanProgress queues a brakeman_progress event for every client, at
// most maxBrakemanProgress bytes (the read's ref left out past it), as
// droppable as chat: a client past activityQueueLimit misses it and
// keeps its stream. It reaches no sink: no webhook or feed.
func (h *eventHub) brakemanProgress(p brakemanProgress) {
	b, err := json.Marshal(p)
	if err == nil && len(b) > maxBrakemanProgress && p.Reading != nil && p.Reading.Ref != "" {
		p.Reading = &brakemanReadingNow{What: p.Reading.What}
		b, err = json.Marshal(p)
	}
	if err != nil || len(b) > maxBrakemanProgress {
		return
	}
	msg := []byte("event: brakeman_progress\ndata: " + string(b) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		if len(ch) >= activityQueueLimit {
			continue
		}
		select {
		case ch <- msg:
		default:
		}
	}
}
