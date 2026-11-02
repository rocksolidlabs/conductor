package api

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// What changed (GET /api/brakeman/changes) and what overlaps (GET
// /api/brakeman/overlaps), docs/plans/brakeman.md, PR-4. What a briefing
// covers is the server's record: the desk notes, per turn, each item of
// what changed it delivered, and each part's watermark is how far the
// part's items as read now were delivered with none skipped before it (a
// run's event seq, Dispatch's change seq, a session's time), so the next
// briefing starts from each part's last complete watermark and pages a
// turn never read are read again. A page's cursor is the place of its
// last item in the answer's order, never an index: what left or joined
// the list between two pages moves nothing else.

// The parts what changed is read from: a run, Dispatch, a session.
const (
	partDispatch = "dispatch"
)

// The bounds of a page of changes beside its items: the parts no item of
// the answer names, which the first page carries, at most
// maxItemlessParts bytes of them; a cursor at most maxChangesCursor bytes.
const (
	maxItemlessParts = 64 << 10
	maxChangesCursor = 2048
)

// dispatchFeedPage is the page of Dispatch's change feed one read of
// what changed takes, dispatch.MaxFeedPage; a test makes it smaller, as
// it makes changesWindow, keeping the two equal as they are here.
var dispatchFeedPage = dispatch.MaxFeedPage

// changeItem is one thing that changed: its ref, what happened, when, the
// part it belongs to and, for a task, its status as Conductor computes
// it.
type changeItem struct {
	Ref    string    `json:"ref"`
	Kind   string    `json:"kind"`
	At     time.Time `json:"at"`
	Part   string    `json:"part"`
	Task   string    `json:"task,omitempty"`
	Ticket string    `json:"ticket,omitempty"`
	Words  string    `json:"words,omitempty"`
	Status *computed `json:"status,omitempty"`
	// seq is the item's place in its part: a run's event seq, Dispatch's
	// change seq; a session's items are placed by their time.
	seq int64
	// h tells apart two items alike but for their words.
	h uint64
	// taskID is the task an event names, for its status.
	taskID string
}

// changePos is an item's place in the answer's order, which a page's
// cursor names: the next page starts after it.
type changePos struct {
	At   time.Time `json:"at"`
	Part string    `json:"part"`
	Seq  int64     `json:"seq"`
	Kind string    `json:"kind"`
	Ref  string    `json:"ref"`
	H    uint64    `json:"h,omitempty"`
}

func (it changeItem) pos() changePos {
	return changePos{At: it.At, Part: it.Part, Seq: it.seq, Kind: it.Kind, Ref: it.Ref, H: it.h}
}

// comparePos orders two places: by time, then part, then the place in
// the part, then kind, ref and words.
func comparePos(a, b changePos) int {
	return cmp.Or(a.At.Compare(b.At), strings.Compare(a.Part, b.Part), cmp.Compare(a.Seq, b.Seq),
		strings.Compare(a.Kind, b.Kind), strings.Compare(a.Ref, b.Ref), cmp.Compare(a.H, b.H))
}

// id is the item's identity in a turn's record of what it was given.
func (it changeItem) id() uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(mustJSON(it.pos())))
	return h.Sum64()
}

// wordsHash is the hash of an item's words.
func wordsHash(words string) uint64 {
	if words == "" {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(words))
	return h.Sum64()
}

// encodeCursor is the cursor naming p; decodeCursor reads one back.
func encodeCursor(p changePos) string {
	return base64.RawURLEncoding.EncodeToString([]byte(mustJSON(p)))
}

func decodeCursor(text string) (changePos, error) {
	var p changePos
	bad := errors.New("cursor is the next of the page before")
	if len(text) > maxChangesCursor {
		return p, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil || store.DecodeStrict(b, &p) != nil {
		return p, bad
	}
	return p, nil
}

// changePart is one part of what changed as read: where it was read from
// and whether what it gives is whole.
type changePart struct {
	Part string `json:"part"`
	// From is how its start was found: "watermark" (the last complete
	// briefing's), "time" (since, or the default window), "new" (newly in
	// scope: the earlier briefing's time).
	From      string    `json:"from"`
	Seq       int64     `json:"seq,omitempty"`
	At        time.Time `json:"at,omitzero"`
	New       bool      `json:"new,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Gap       bool      `json:"gap,omitempty"`
	Why       string    `json:"why,omitempty"`
	// last is the part's place before its first item: the seq or the
	// time it is complete up to before any of them; end, the seq of the
	// last row it read (a run's event, a Dispatch change, its items or
	// not); readAt, the time a session's window was read at or after.
	last   int64
	lastAt time.Time
	end    int64
	readAt time.Time
	kind   string // "run", "session", partDispatch
	id     string
}

// changesStart is where a read of what changed starts.
type changesStart struct {
	// mode is "since" (a time the caller gave, or the request's own),
	// "watermarks" (the last complete briefing's coverage) or "day" (the
	// default window, with no briefing to start from).
	mode     string
	since    time.Time
	briefing string
	coverage *brakeman.Coverage
	// natural says the start is the turn's own (no since given), so what
	// its pages reach is its coverage.
	natural bool
}

// handleBrakemanChanges answers GET /api/brakeman/changes?since=&cursor=&limit=:
// what changed since the time, or (with none) since the last complete
// briefing's watermarks (a briefing request's own since first), else the
// last 24 hours: runs launched, ended, paused or continued, tasks that
// changed state with their status, questions opened and answered from
// each listed session's window (a gap where the window starts after the
// start or the session is gone), messages, and Dispatch's changes, every
// project's, a comment on an old ticket among them; each with its ref and
// time, in time order, at most limit (1 to 200, 100 when absent) and
// maxObserveReply bytes a page, next (the place of the page's last item)
// for the rest. A page carries the parts its items are of; the first,
// the parts no item is of as well (a watermark with nothing new, a gap),
// partsOmitted counting those past their bound.
func (s *Server) handleBrakemanChanges(w http.ResponseWriter, r *http.Request, o *observer) {
	q, err := readQuery(r, "since", "cursor", "limit")
	var sinceText, cursorText string
	var limit int
	if err == nil {
		sinceText, err = q.one("since")
	}
	if err == nil {
		cursorText, err = q.one("cursor")
	}
	if err == nil {
		limit, err = q.count("limit")
	}
	if err == nil && (limit < 0 || limit > maxChangesPage) {
		err = fmt.Errorf("limit is 1 to %d", maxChangesPage)
	}
	var cursor changePos
	if err == nil && cursorText != "" {
		cursor, err = decodeCursor(cursorText)
	}
	var since time.Time
	if err == nil && sinceText != "" {
		if since, err = time.Parse(time.RFC3339Nano, sinceText); err != nil {
			err = errors.New("since is a time (RFC 3339)")
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if limit == 0 {
		limit = defaultChangesPage
	}
	start, err := s.changesStart(o, since)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	items, parts, err := s.changes(r.Context(), o, start)
	if err != nil {
		s.observeError(w, err)
		return
	}
	first := 0
	if cursorText != "" {
		first = len(items)
		if i := slices.IndexFunc(items, func(it changeItem) bool { return comparePos(it.pos(), cursor) > 0 }); i >= 0 {
			first = i
		}
	}
	named := map[string]bool{}
	for _, it := range items {
		named[it.Part] = true
	}
	partOf := map[string]changePart{}
	for _, p := range parts {
		partOf[p.Part] = p
	}
	used := 4 << 10
	shown := map[string]bool{}
	pageParts := []changePart{}
	omitted := 0
	if cursorText == "" {
		// The parts no item is of, on the first page, within their own
		// bound: the rest of the page is the items' and their parts'.
		itemless := 0
		for _, p := range parts {
			if named[p.Part] {
				continue
			}
			size := len(mustJSON(p)) + 1
			if itemless+size > maxItemlessParts {
				omitted++
				continue
			}
			itemless += size
			shown[p.Part] = true
			pageParts = append(pageParts, p)
		}
		used += itemless
	}
	page := []changeItem{}
	statuses := map[string]*computed{}
	for i := first; i < len(items) && len(page) < limit; i++ {
		it := items[i]
		if it.taskID != "" {
			c, err := s.itemStatus(r.Context(), o, strings.TrimPrefix(it.Part, "run:"), it.taskID, statuses)
			if err != nil {
				s.observeError(w, err)
				return
			}
			it.Status = c
		}
		size := len(mustJSON(it)) + 1
		if !shown[it.Part] {
			size += len(mustJSON(partOf[it.Part])) + 1
		}
		if used+size > maxObserveReply {
			break
		}
		used += size
		if !shown[it.Part] {
			shown[it.Part] = true
			pageParts = append(pageParts, partOf[it.Part])
		}
		page = append(page, it)
	}
	for _, it := range page {
		o.noteRef(it.Ref, classOfRef(it.Ref), it.Kind, "")
	}
	next := ""
	if n := len(page); n > 0 && first+n < len(items) {
		next = encodeCursor(page[n-1].pos())
	}
	if o.tp != nil && start.natural {
		o.coverage = s.brakeman.cover(o.tp.req, page, items, parts)
	}
	out := map[string]any{"from": start.mode, "changes": page, "parts": pageParts, "next": next}
	if omitted > 0 {
		out["partsOmitted"] = omitted
	}
	if !start.since.IsZero() {
		out["since"] = start.since
	}
	if start.briefing != "" {
		out["briefing"] = start.briefing
	}
	writeJSON(w, http.StatusOK, out)
}

// classOfRef is the visibility class a ref string reads.
func classOfRef(ref string) string {
	kind, _, _ := strings.Cut(ref, ":")
	return refClass(kind)
}

// itemStatus is a managed task's status, computed once a page.
func (s *Server) itemStatus(ctx context.Context, o *observer, runID, taskID string, cache map[string]*computed) (*computed, error) {
	key := runID + "/" + taskID
	if c, ok := cache[key]; ok {
		return c, nil
	}
	run, err := o.rr.run(ctx, runID)
	if err != nil || !run.managed || s.ledger == nil {
		cache[key] = nil
		return nil, err
	}
	rows, err := s.ledger.Tasks(ctx, runID)
	if err != nil {
		return nil, err
	}
	attempts, err := s.ledger.Attempts(ctx, runID)
	if err != nil {
		return nil, err
	}
	var held crew.TaskState
	if run.held {
		if i := slices.IndexFunc(run.run.Tasks, func(t crew.TaskState) bool { return t.ID == taskID }); i >= 0 {
			held = run.run.Tasks[i]
		}
	}
	for _, t := range rows {
		if t.ID == taskID {
			c, err := s.taskComputed(ctx, o, run, runID, t, held, attempts)
			if err != nil {
				return nil, err
			}
			cache[key] = &c
			return &c, nil
		}
	}
	cache[key] = nil
	return nil, nil
}

// changesStart is where the read starts: since when given (not the turn's
// own start); else a briefing request's own since; else the last complete
// briefing's watermarks that the caller's grant covers; else the last 24
// hours.
func (s *Server) changesStart(o *observer, since time.Time) (changesStart, error) {
	if !since.IsZero() {
		return changesStart{mode: "since", since: since.UTC()}, nil
	}
	if o.tp != nil && !o.tp.req.Since.IsZero() {
		return changesStart{mode: "since", since: o.tp.req.Since, natural: true}, nil
	}
	req := brakeman.Request{}
	if o.tp != nil {
		req = o.tp.req
	}
	last, err := s.brakeman.lastBriefing(req, o.grant)
	if err != nil {
		return changesStart{}, err
	}
	natural := o.tp != nil
	if last != nil && last.Coverage != nil {
		return changesStart{mode: "watermarks", since: coverageTime(last), briefing: last.ID, coverage: last.Coverage, natural: natural}, nil
	}
	if last != nil {
		return changesStart{mode: "since", since: last.At, briefing: last.ID, natural: natural}, nil
	}
	return changesStart{mode: "day", since: o.now.Add(-defaultBriefingWindow), natural: natural}, nil
}

// coverageTime is when an earlier briefing's turn read what it covers:
// the earliest time its watermarks were read at, else the brief's own
// time. A part newly in scope starts there.
func coverageTime(b *turnBriefing) time.Time {
	at := b.At
	c := b.Coverage
	for _, w := range slices.Concat(c.Runs, c.Sessions) {
		if !w.At.IsZero() && w.At.Before(at) {
			at = w.At
		}
	}
	if c.Dispatch != nil && !c.Dispatch.At.IsZero() && c.Dispatch.At.Before(at) {
		at = c.Dispatch.At
	}
	return at
}

// changes reads what changed from start in every part the caller may
// see, in time order (then by part and place, so a part's items keep
// their order), and how each part was read.
func (s *Server) changes(ctx context.Context, o *observer, start changesStart) ([]changeItem, []changePart, error) {
	var items []changeItem
	parts := []changePart{}
	add := func(p changePart, its []changeItem) {
		p.Part = cmpOr(p.Part, p.kind+":"+p.id)
		for i := range its {
			its[i].Part, its[i].h = p.Part, wordsHash(its[i].Words)
		}
		parts = append(parts, p)
		items = append(items, its...)
	}
	if o.sees(brakeman.ClassRuns) {
		if err := s.runChanges(ctx, o, start, add); err != nil {
			return nil, nil, err
		}
	}
	if o.sees(brakeman.ClassSessions) {
		if err := s.sessionChanges(ctx, o, start, add); err != nil {
			return nil, nil, err
		}
	}
	if o.sees(brakeman.ClassDispatch) {
		if err := s.dispatchChanges(ctx, o, start, add); err != nil {
			return nil, nil, err
		}
	}
	slices.SortStableFunc(items, func(a, b changeItem) int { return comparePos(a.pos(), b.pos()) })
	return items, parts, nil
}

// watermarkOf is the watermark the coverage keeps of a part, nil for
// none.
func watermarkOf(c *brakeman.Coverage, kind, id string) *brakeman.Watermark {
	if c == nil {
		return nil
	}
	switch kind {
	case "run":
		if i := slices.IndexFunc(c.Runs, func(w brakeman.Watermark) bool { return w.ID == id }); i >= 0 {
			return &c.Runs[i]
		}
	case "session":
		if i := slices.IndexFunc(c.Sessions, func(w brakeman.Watermark) bool { return w.ID == id }); i >= 0 {
			return &c.Sessions[i]
		}
	case partDispatch:
		return c.Dispatch
	}
	return nil
}

// partStart is where one part is read from: its watermark, or (newly in
// scope, or reading by time) start's time.
func partStart(start changesStart, kind, id string) (changePart, *brakeman.Watermark) {
	p := changePart{kind: kind, id: id}
	if kind == partDispatch {
		p.Part = partDispatch
	}
	if start.mode == "watermarks" {
		if w := watermarkOf(start.coverage, kind, id); w != nil {
			p.From, p.Seq, p.At = "watermark", w.Seq, w.At
			return p, w
		}
		p.From, p.New, p.At = "new", true, start.since
		return p, nil
	}
	p.From, p.At = "time", start.since
	return p, nil
}

// runIDs is every run the engine holds or has a record of, and every
// managed run the ledger holds, once each.
func (s *Server) runIDs(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	keep := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, r := range s.runs.List() {
		keep(r.ID)
	}
	if s.records != nil {
		recs, err := s.records.List("")
		if err != nil {
			return nil, fmt.Errorf("the run records: %w", err)
		}
		for _, r := range recs {
			keep(r.ID)
		}
	}
	if s.ledger != nil {
		rows, err := s.ledger.Runs(ctx)
		if err != nil {
			return nil, fmt.Errorf("the ledger's runs: %w", err)
		}
		for _, r := range rows {
			keep(r.ID)
		}
	}
	return out, nil
}

// runChanges reads each run in the caller's grant: a managed run's
// ledger events after its watermark (or, by time, among its newest
// changesWindow, truncated past them), another run's launch and end.
func (s *Server) runChanges(ctx context.Context, o *observer, start changesStart, add func(changePart, []changeItem)) error {
	ids, err := s.runIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		run, ok, err := o.runAdmitted(ctx, id)
		if err != nil {
			return err
		}
		if !ok || !run.found {
			continue
		}
		p, w := partStart(start, "run", id)
		var its []changeItem
		if run.managed && s.ledger != nil {
			if its, err = s.ledgerChanges(ctx, id, &p, w); err != nil {
				return err
			}
		} else if run.held {
			its = heldRunChanges(run.run, &p, w)
		}
		if len(its) == 0 && p.From != "watermark" && !p.Truncated {
			// A run with nothing since a time is no part of the answer;
			// one with a watermark keeps it.
			continue
		}
		add(p, its)
	}
	return nil
}

// ledgerChanges is a managed run's events after its watermark w, or by
// p's time.
func (s *Server) ledgerChanges(ctx context.Context, id string, p *changePart, w *brakeman.Watermark) ([]changeItem, error) {
	lastSeq, err := s.ledger.LastEvent(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("the ledger's last event of %s: %w", id, err)
	}
	var after int64
	if w != nil {
		after = w.Seq
	} else {
		after = max(0, lastSeq-changesWindow)
	}
	events, err := s.ledger.Events(ctx, id, after, int(changesWindow)+1)
	if err != nil {
		return nil, fmt.Errorf("the ledger's events of %s: %w", id, err)
	}
	if int64(len(events)) > changesWindow {
		events, p.Truncated = events[:changesWindow], true
	}
	p.last = after
	var out []changeItem
	for i, ev := range events {
		if w == nil && ev.At.Before(p.At) {
			p.last = ev.Seq
			continue
		}
		if w == nil && i == 0 && after > 0 {
			// The oldest event looked at is after the time: older ones
			// may be too.
			p.Truncated = true
			p.Why = fmt.Sprintf("more than the newest %d events of the run since then", changesWindow)
		}
		it := changeItem{Ref: "event:" + id + "/" + strconv.FormatInt(ev.Seq, 10), Kind: ev.Kind, At: ev.At, seq: ev.Seq}
		var payload struct {
			Task string `json:"task"`
		}
		if json.Unmarshal([]byte(ev.Payload), &payload) == nil && crew.ValidTaskID(payload.Task) {
			it.Task, it.taskID = "task:"+id+"/"+payload.Task, payload.Task
		}
		out = append(out, it)
	}
	p.end = after
	if len(events) > 0 {
		p.end = events[len(events)-1].Seq
	}
	return out, nil
}

// heldRunChanges is a run without a ledger's launch and end, from the
// engine's record of it.
func heldRunChanges(run crew.Run, p *changePart, w *brakeman.Watermark) []changeItem {
	var out []changeItem
	from := p.At
	var after int64
	if w != nil {
		after = w.Seq
	}
	if run.StartedAt.After(from) && after < 1 {
		out = append(out, changeItem{Ref: "run:" + run.ID, Kind: "run.launched", At: run.StartedAt, seq: 1, Words: run.Label})
	}
	if run.StoppedAt != nil && run.StoppedAt.After(from) && after < 2 {
		out = append(out, changeItem{Ref: "run:" + run.ID, Kind: "run." + run.State, At: *run.StoppedAt, seq: 2})
	}
	p.last, p.end = after, after
	return out
}

// sessionChanges reads each listed session in the caller's grant: the
// questions its window shows asked and answered after its watermark or
// the start, with a gap where the window starts after it (a hosted
// session's window is its host's: only its question now), and a gap for
// each session the last coverage kept that is gone.
func (s *Server) sessionChanges(ctx context.Context, o *observer, start changesStart, add func(changePart, []changeItem)) error {
	listed := map[string]bool{}
	for _, info := range s.registry.List() {
		listed[info.ID] = true
		ok, err := o.sessionAdmitted(ctx, info.ID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		p, w := partStart(start, "session", info.ID)
		from := p.At
		if w != nil {
			from = w.At
		}
		ref := "session:" + info.ID
		var its []changeItem
		l, local := s.lookupLocal(info.ID)
		if !local || info.Kind == session.KindHosted {
			if a := info.Attention; a.State == session.AttentionNeedsInput && a.Since != nil && a.Since.After(from) {
				qref := ref
				if a.ID != "" {
					qref = "question:" + info.ID + "/" + a.ID
				}
				its = append(its, changeItem{Ref: qref, Kind: "question.asked", At: *a.Since, Words: a.Message})
			}
			p.Gap, p.Why = true, "a hosted session's window is its host's: only its question now"
		} else {
			window := l.Activity()
			if len(window) >= session.MaxActivity && window[0].At.After(from) {
				p.Gap, p.Why = true, "the session's window starts after "+from.Format(time.RFC3339)
			}
			for _, e := range window {
				if !e.At.After(from) {
					continue
				}
				switch {
				case e.Type == session.ActivityAttention:
					its = append(its, changeItem{Ref: ref, Kind: "attention", At: e.At, Words: e.Message})
				case e.Type == session.ActivityInput && e.Op != session.OpTyped:
					its = append(its, changeItem{Ref: ref, Kind: "answered", At: e.At, Words: e.ByName})
				case e.Type == session.ActivityStatus:
					its = append(its, changeItem{Ref: ref, Kind: "status", At: e.At, Words: e.Message})
				}
			}
			if a := info.Attention; a.State == session.AttentionNeedsInput && a.ID != "" && a.Since != nil && a.Since.After(from) {
				its = append(its, changeItem{Ref: "question:" + info.ID + "/" + a.ID, Kind: "question.open", At: *a.Since, Words: a.Message})
			}
		}
		for i := range its {
			its[i].seq = its[i].At.UnixNano()
		}
		// The window is read after the observer's time: a session that
		// gives nothing more is complete up to that time, never up to one
		// after an entry its read did not see.
		p.lastAt, p.readAt = from, o.now
		if len(its) == 0 && p.From != "watermark" && !p.Gap {
			continue
		}
		add(p, its)
	}
	if start.coverage != nil {
		for _, w := range start.coverage.Sessions {
			if !listed[w.ID] {
				p := changePart{kind: "session", id: w.ID, From: "watermark", At: w.At, Gap: true, Why: "the session is gone: what it asked after its watermark is not held", lastAt: w.At}
				add(p, nil)
			}
		}
	}
	return nil
}

// dispatchChanges reads Dispatch's changes in the caller's grant after
// its watermark, or by time among its newest changesWindow.
func (s *Server) dispatchChanges(ctx context.Context, o *observer, start changesStart, add func(changePart, []changeItem)) error {
	ds := s.dispatch
	if ds == nil {
		return nil
	}
	p, w := partStart(start, partDispatch, "")
	head, err := ds.Head(ctx)
	if err != nil {
		return fmt.Errorf("the head of Dispatch's changes: %w", err)
	}
	after := max(0, head-changesWindow)
	if w != nil {
		after = w.Seq
	}
	// At most the window, and the feed's page: a page that fills short of
	// the head read before it leaves changes unread, truncated.
	limit := int(min(changesWindow, int64(dispatchFeedPage)))
	rows, err := ds.ChangesAfter(ctx, after, limit)
	if err != nil {
		return fmt.Errorf("the changes of Dispatch: %w", err)
	}
	if len(rows) == limit && rows[len(rows)-1].Seq < head {
		p.Truncated = true
		p.Why = fmt.Sprintf("more than %d of Dispatch's changes since then", limit)
	}
	p.last, p.end = after, after
	if len(rows) > 0 {
		p.end = rows[len(rows)-1].Seq
	}
	admitted := map[string]bool{}
	var its []changeItem
	for i, c := range rows {
		if w == nil && c.At.Before(p.At) {
			p.last = c.Seq
			continue
		}
		if w == nil && i == 0 && after > 0 {
			p.Truncated = true
			p.Why = fmt.Sprintf("more than Dispatch's newest %d changes since then", changesWindow)
		}
		ok := true
		switch {
		case c.Ticket != "":
			if v, seen := admitted[c.Ticket]; seen {
				ok = v
			} else {
				if ok, err = o.ticketAdmitted(ctx, c.Ticket); err != nil {
					return err
				}
				admitted[c.Ticket] = ok
			}
		case c.Project != "":
			ok = o.grant.projectAdmitted(c.Project)
		default:
			ok = o.tp == nil || !o.grant.byProject() && !o.grant.byLinks()
		}
		if !ok {
			// Out of the grant: passed over, as nothing the caller may
			// know of, and the watermark moves past it.
			continue
		}
		it := changeItem{Kind: c.Entity + "." + c.Op, At: c.At, seq: c.Seq, Words: c.Field}
		switch {
		case c.Entity == dispatch.EntityComment && c.Ticket != "":
			it.Ref, it.Ticket = "comment:"+c.Ticket+"/"+c.EntityID, "ticket:"+c.Ticket
		case c.Ticket != "":
			it.Ref, it.Ticket = "ticket:"+c.Ticket, "ticket:"+c.Ticket
		default:
			// The operation's receipt: the actor and op id that made the
			// change, never the entity's id.
			it.Ref = "op:" + c.Actor + "/" + c.OpID
			if r, err := brakeman.ParseRef(it.Ref); err != nil || r.Valid() != nil {
				// A row whose operation no ref can cite (none this server
				// writes) is passed over.
				continue
			}
		}
		its = append(its, it)
	}
	add(p, its)
	return nil
}

// turnChanges is what the turn in progress was given of what changed,
// each item by its identity (changeItem.id), and the visibility classes
// its answers used: the slot's one turn at a time, begun again for
// another request.
type turnChanges struct {
	req       string
	delivered map[uint64]bool
	classes   map[string]bool
}

// turnChangesOf is the record of the request id's turn. The caller holds
// d.mu.
func (d *brakemanDesk) turnChangesOf(id string) *turnChanges {
	if d.turnReads == nil || d.turnReads.req != id {
		d.turnReads = &turnChanges{req: id, delivered: map[uint64]bool{}, classes: map[string]bool{}}
	}
	return d.turnReads
}

// usedClass notes that an answer of the request id's turn used class.
func (d *brakemanDesk) usedClass(id, class string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.turnChangesOf(id).classes[class] = true
}

// classesUsed is the classes the request id's turn's answers used, in
// brakeman.Classes' order.
func (d *brakemanDesk) classesUsed(id string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	if t := d.turnReads; t != nil && t.req == id {
		for _, c := range brakeman.Classes {
			if t.classes[c] {
				out = append(out, c)
			}
		}
	}
	return out
}

// cover is the coverage of a turn's read of changes that delivered page,
// of items as read now: page is added to what the turn was given, and
// each part's watermark is how far its items, in the part's order, were
// given with none before them skipped: past the last of them, to the end
// of what the part read (a session's to when its window was read), when
// all were; just before the first that was not, partial, otherwise; and
// partial where the part was truncated or has a gap. A session's place
// is a time, so an item not given at a time another was stops the
// watermark before that time, and neither is passed over.
func (d *brakemanDesk) cover(req brakeman.Request, page, items []changeItem, parts []changePart) *brakeman.Coverage {
	d.mu.Lock()
	tc := d.turnChangesOf(req.ID)
	for _, it := range page {
		tc.delivered[it.id()] = true
	}
	byPart := map[string][]changeItem{}
	for _, it := range items {
		byPart[it.Part] = append(byPart[it.Part], it)
	}
	missed := map[string]int{}
	for part, its := range byPart {
		if !strings.HasPrefix(part, "session:") {
			slices.SortStableFunc(its, func(a, b changeItem) int { return cmp.Compare(a.seq, b.seq) })
		}
		missed[part] = slices.IndexFunc(its, func(it changeItem) bool { return !tc.delivered[it.id()] })
	}
	d.mu.Unlock()
	cov := &brakeman.Coverage{}
	if req.Coverage != nil {
		*cov = *req.Coverage
	}
	now := d.now().UTC()
	for _, p := range parts {
		its, u := byPart[p.Part], -1
		if len(its) > 0 {
			u = missed[p.Part]
		}
		wm := brakeman.Watermark{ID: p.id, Partial: u >= 0 || p.Truncated || p.Gap, New: p.New, At: now}
		switch p.kind {
		case "session":
			wm.At = p.readAt
			if u >= 0 {
				wm.At = its[u].At.Add(-time.Nanosecond)
			}
			if wm.At.Before(p.lastAt) {
				wm.At = p.lastAt
			}
		default:
			wm.Seq = max(p.last, p.end)
			if p.From == "watermark" {
				wm.Seq = max(wm.Seq, p.Seq)
			}
			if u >= 0 {
				// Every row before it was given, or passed over as older
				// than the time or outside the grant.
				wm.Seq = its[u].seq - 1
			}
		}
		switch p.kind {
		case "run":
			cov.Runs = putWatermark(cov.Runs, wm)
		case "session":
			cov.Sessions = putWatermark(cov.Sessions, wm)
		default:
			w := wm
			w.ID = ""
			cov.Dispatch = &w
		}
	}
	return cov
}

// putWatermark puts w in place of the one of its id, or adds it while
// the list has room (brakeman.MaxWatermarks): a part left out starts at
// the briefing's time, newly in scope, next time.
func putWatermark(list []brakeman.Watermark, w brakeman.Watermark) []brakeman.Watermark {
	if i := slices.IndexFunc(list, func(o brakeman.Watermark) bool { return o.ID == w.ID }); i >= 0 {
		list[i] = w
		return list
	}
	if len(list) >= brakeman.MaxWatermarks {
		return list
	}
	return append(list, w)
}

// overlapTree is one live git work tree overlaps reads: an attempt's
// worktree (its committed changes since its start, its working tree's)
// or a session's folder (its working tree's).
type overlapTree struct {
	ref  string
	dir  string
	base string
}

// overlap is one path changed in two or more trees of one repository.
type overlap struct {
	Path    string   `json:"path"`
	Repo    string   `json:"repo"`
	Between []string `json:"between"`
}

// handleBrakemanOverlaps answers GET /api/brakeman/overlaps: the paths
// changed in two or more live attempts' or sessions' git work trees of
// one repository, each tree's committed changes since its base (an
// attempt's start) with its working tree's, at most maxOverlapTrees trees
// and maxOverlapPaths paths, each path with the refs of its trees. git is
// run through internal/gitcli, argv only.
func (s *Server) handleBrakemanOverlaps(w http.ResponseWriter, r *http.Request, o *observer) {
	if !noQuery(w, r) {
		return
	}
	if !o.sees(brakeman.ClassRuns) && !o.sees(brakeman.ClassSessions) {
		writeError(w, http.StatusForbidden, "not_visible", "Brakeman may see neither runs nor sessions")
		return
	}
	ctx := r.Context()
	trees, err := s.overlapTrees(ctx, o)
	if err != nil {
		s.observeError(w, err)
		return
	}
	truncated := false
	if len(trees) > maxOverlapTrees {
		trees, truncated = trees[:maxOverlapTrees], true
	}
	type key struct{ repo, path string }
	by := map[key][]string{}
	var order []key
	read := 0
	for _, t := range trees {
		gctx, cancel := context.WithTimeout(ctx, refGitWait)
		repo, paths, cut, err := treePaths(gctx, t)
		cancel()
		if err != nil {
			// A tree git cannot read now is left out and said.
			truncated = true
			continue
		}
		truncated = truncated || cut
		for _, p := range paths {
			if read == maxOverlapPaths {
				truncated = true
				break
			}
			read++
			k := key{repo, p}
			if _, ok := by[k]; !ok {
				order = append(order, k)
			}
			if !slices.Contains(by[k], t.ref) {
				by[k] = append(by[k], t.ref)
			}
		}
	}
	out := []overlap{}
	used := 1024
	for _, k := range order {
		if len(by[k]) < 2 {
			continue
		}
		ov := overlap{Path: k.path, Repo: k.repo, Between: by[k]}
		size := len(mustJSON(ov)) + 1
		if used+size > maxObserveReply {
			truncated = true
			break
		}
		used += size
		out = append(out, ov)
		for _, ref := range ov.Between {
			o.noteRef(ref, classOfRef(ref), "overlap", "")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"overlaps": out, "trees": len(trees), "truncated": truncated})
}

// overlapTrees is the live trees in the caller's grant: each live
// attempt's worktree with its start, then each live server session's
// folder that no attempt's tree is; one each.
func (s *Server) overlapTrees(ctx context.Context, o *observer) ([]overlapTree, error) {
	var out []overlapTree
	seen := map[string]bool{}
	if o.sees(brakeman.ClassRuns) {
		for _, run := range s.runs.List() {
			if run.StoppedAt != nil {
				continue
			}
			_, ok, err := o.runAdmitted(ctx, run.ID)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			for _, t := range run.Tasks {
				a := t.Attempt
				if a == nil || a.Worktree == "" || a.EndedAt != nil || seen[a.Worktree] {
					continue
				}
				seen[a.Worktree] = true
				out = append(out, overlapTree{ref: fmt.Sprintf("attempt:%s/%s/%d", run.ID, t.ID, a.N), dir: a.Worktree, base: a.Start})
			}
		}
	}
	if o.sees(brakeman.ClassSessions) {
		for _, info := range s.registry.List() {
			if info.Kind == session.KindHosted || info.Status.Ended() || info.Cwd == "" || seen[info.Cwd] {
				continue
			}
			if info.Crew != nil && info.Crew.Task != "" {
				continue // an attempt's: its tree is the attempt's
			}
			ok, err := o.sessionAdmitted(ctx, info.ID)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			seen[info.Cwd] = true
			out = append(out, overlapTree{ref: "session:" + info.ID, dir: info.Cwd})
		}
	}
	return out, nil
}

// treePaths is a tree's repository (its common git directory, so two
// worktrees of one repository are one) and the paths it changed: its
// committed changes since its base, and its working tree's.
func treePaths(ctx context.Context, t overlapTree) (repo string, paths []string, truncated bool, err error) {
	common, err := gitcli.Run(ctx, t.dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", nil, false, err
	}
	repo = strings.TrimSpace(common)
	if t.base != "" {
		committed, cut, err := gitcli.ChangedSince(ctx, t.dir, t.base)
		if err != nil {
			return "", nil, false, err
		}
		paths, truncated = committed, cut
	}
	st, err := gitcli.GetStatus(ctx, t.dir, "")
	if err != nil {
		return "", nil, false, err
	}
	for _, c := range st.Changes {
		if !slices.Contains(paths, c.Path) {
			paths = append(paths, c.Path)
		}
	}
	return repo, paths, truncated || st.Truncated, nil
}
