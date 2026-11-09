package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
)

// Brakeman reads history within its grant (docs/plans/history.md, PR-11).
// GET /api/brakeman/history is the History search (handleHistorySearch's
// query, cursor and index) answered for the caller: the admin, or a
// Brakeman turn held to its grant as its refs are (refGrant). An entry is
// served only when every class of record its text reads is visible
// (historyReadClasses: a check's receipt is the runs' and its indexed log
// the logs'), and the record it belongs to resolves within the scope now,
// read from that record's own store and never from the index (the run,
// the ticket, the session or its record, the brief, the decision: its
// scope ref); anything else is left out, and so is every kind the table
// does not name. A served entry carries the ref a brief cites it by (its
// own, when Brakeman's grammar names it and it is of that record, else
// the record's), never the index's key; its links and its anchor only
// where they resolve within the grant too; none of what counts the
// index's other rows (a total, how often its words recur and where, how
// far the index reaches). Each served entry is noted in the turn's read
// log in each class it read, with no revision: a history row is a line
// of the index, not a snapshot of its record.

// The bounds of a turn's read of the history.
const (
	// maxHistoryTurnPage bounds the entries an answer holds, and
	// defaultHistoryTurnPage is a read that names no limit: each entry
	// costs the resolution of its record, read from its store.
	maxHistoryTurnPage     = 20
	defaultHistoryTurnPage = 10
	// maxHistoryTurnScan bounds the index's rows one read looks at: past
	// it the answer is what was found, with next. historyTurnPage is how
	// many the index is asked for at a time.
	maxHistoryTurnScan = 200
	historyTurnPage    = 50
)

// historyReadClasses is, by an entry's kind, every visibility class its
// indexed text reads: the class of the record it is of, and a check's
// log (its body) the logs'. It is closed, as the index's kinds table is
// (TestEveryHistoryKindHasItsClasses): a kind it does not name is never
// served to a turn. A brief's item names none: the brief carries its
// footprint's classes (resolveBrief), which its resolution checks.
var historyReadClasses = map[string][]string{
	"session":          {brakeman.ClassSessions},
	"session.typed":    {brakeman.ClassSessions},
	"session.question": {brakeman.ClassSessions},
	"session.answer":   {brakeman.ClassSessions},
	"session.report":   {brakeman.ClassSessions},
	"session.chat":     {brakeman.ClassSessions},
	"session.file":     {brakeman.ClassSessions, brakeman.ClassTouched},

	"run":                {brakeman.ClassRuns},
	"task":               {brakeman.ClassRuns},
	"attempt":            {brakeman.ClassRuns},
	"attempt.result":     {brakeman.ClassRuns},
	"attempt.checkpoint": {brakeman.ClassRuns},
	"attempt.blocker":    {brakeman.ClassRuns},
	"review.verdict":     {brakeman.ClassRuns},
	"message":            {brakeman.ClassRuns},
	"command":            {brakeman.ClassRuns},
	"step":               {brakeman.ClassRuns},
	"candidate.verified": {brakeman.ClassRuns},
	"check":              {brakeman.ClassRuns, brakeman.ClassLogs},
	"verify":             {brakeman.ClassRuns, brakeman.ClassLogs},
	"decision":           {brakeman.ClassRuns},
	"decision.response":  {brakeman.ClassRuns},

	"ticket":            {brakeman.ClassDispatch},
	"comment":           {brakeman.ClassDispatch},
	"plan":              {brakeman.ClassDispatch},
	"plan.approved":     {brakeman.ClassDispatch},
	"plan.withdrawn":    {brakeman.ClassDispatch},
	"plan.superseded":   {brakeman.ClassDispatch},
	"launch":            {brakeman.ClassDispatch},
	"ticket.accepted":   {brakeman.ClassDispatch},
	"ticket.unaccepted": {brakeman.ClassDispatch},
	"ticket.closed":     {brakeman.ClassDispatch},
	// What a ticket keeps of a run's candidate: its verification and a
	// person's review of it, read with their ticket and of that run.
	"delivery.verified": {brakeman.ClassDispatch, brakeman.ClassRuns},
	"review.batch":      {brakeman.ClassDispatch, brakeman.ClassRuns},
	"review.finding":    {brakeman.ClassDispatch, brakeman.ClassRuns},
	"review.resolved":   {brakeman.ClassDispatch, brakeman.ClassRuns},

	"brief.item": {},
}

// historyOfRun are the kinds a ticket keeps of a run's candidate: an
// entry of one is admitted only when that run is too.
var historyOfRun = []string{"delivery.verified", "review.batch", "review.finding", "review.resolved"}

// turnHistoryEntry is an entry as a turn reads it: the admin's served
// entry less what names the index's rows (its key, its repeats), with the
// ref a brief cites it by.
type turnHistoryEntry struct {
	Ref          string           `json:"ref"`
	Kind         string           `json:"kind"`
	Class        string           `json:"class"`
	Outcome      string           `json:"outcome,omitempty"`
	Title        string           `json:"title"`
	Summary      string           `json:"summary,omitempty"`
	Snippet      string           `json:"snippet,omitempty"`
	At           time.Time        `json:"at"`
	Actor        string           `json:"actor,omitempty"`
	Source       turnSource       `json:"source"`
	Host         servedHost       `json:"host"`
	Project      servedProject    `json:"project"`
	Revision     servedRevision   `json:"revision"`
	Agent        *servedAgent     `json:"agent,omitempty"`
	Crew         string           `json:"crew,omitempty"`
	Run          string           `json:"run,omitempty"`
	Ticket       string           `json:"ticket,omitempty"`
	Anchor       *servedAnchor    `json:"anchor,omitempty"`
	URL          string           `json:"url"`
	Links        []turnHistoryRef `json:"links"`
	Availability availabilityOf   `json:"availability"`
}

// turnSource is an entry's source as a turn reads it: the store and its
// words.
type turnSource struct {
	Store string `json:"store"`
	Label string `json:"label"`
}

// turnHistoryRef is a link as a turn reads it, without the index's key.
type turnHistoryRef struct {
	Rel     string `json:"rel"`
	Dir     string `json:"dir"`
	Ref     string `json:"ref"`
	Label   string `json:"label"`
	Class   string `json:"class,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	URL     string `json:"url,omitempty"`
}

// historyScope admits a read's entries: each record's resolution read
// once per read.
type historyScope struct {
	o    *observer
	done map[brakeman.Ref]historyAdmission
}

// historyAdmission is a scope ref's resolution: within the grant (why
// ""), with its state now.
type historyAdmission struct {
	res brakeman.Resolution
	why string
}

// admitRef resolves ref once: within the grant, and its resolution. For a
// turn, a Brakeman turn's own run or session is never within it: what an
// earlier turn's agent reported was read under that turn's grant, which
// only its brief's footprint carries (resolveBrief).
func (hs *historyScope) admitRef(ctx context.Context, ref brakeman.Ref) (brakeman.Resolution, bool, error) {
	if a, ok := hs.done[ref]; ok {
		return a.res, a.why == "", nil
	}
	if hs.o.tp != nil {
		turn, err := hs.turnsOwn(ctx, ref)
		if err != nil {
			return brakeman.Resolution{}, false, err
		}
		if turn {
			hs.done[ref] = historyAdmission{why: "a Brakeman turn's own"}
			return brakeman.Resolution{}, false, nil
		}
	}
	res, why, err := hs.o.rr.resolve(ctx, ref)
	if err != nil {
		return brakeman.Resolution{}, false, err
	}
	hs.done[ref] = historyAdmission{res: res, why: why}
	return res, why == "", nil
}

// turnsOwn reports whether a scope ref is a Brakeman turn's run or
// session: a run of Brakeman's class, as its id or its record says; a
// session whose crew is a turn's, as the registry or its record says.
func (hs *historyScope) turnsOwn(ctx context.Context, ref brakeman.Ref) (bool, error) {
	rr := hs.o.rr
	switch ref.Kind {
	case brakeman.RefRun:
		if crewOfRun(ref.Run) == crew.ClassBrakeman {
			return true, nil
		}
		r, err := rr.run(ctx, ref.Run)
		if err != nil {
			return false, err
		}
		return r.item.Crew == crew.ClassBrakeman || (r.held && r.run.Class == crew.ClassBrakeman), nil
	case brakeman.RefSession:
		if d, ok := rr.s.registry.Get(ref.Session); ok {
			c := d.Info().Crew
			return c != nil && (c.Brakeman != nil || c.CrewID == crew.ClassBrakeman), nil
		}
		rec, ok, err := rr.recordRef(ref.Session)
		if err != nil || !ok {
			return false, err
		}
		return rec.Crew != nil && (rec.Crew.Brakeman != "" || rec.Crew.CrewID == crew.ClassBrakeman), nil
	}
	return false, nil
}

// scopeOfRef is the record a ref of Brakeman's grammar is of, which
// admits it: a run's part its run, a session's question or file its
// session, a ticket's comment, receipt or plan revision its ticket; a
// launch, a brief's item and a decision are their own (the resolver
// admits each by what it is of now). ok is false for none.
func scopeOfRef(r brakeman.Ref) (brakeman.Ref, bool) {
	switch r.Kind {
	case brakeman.RefRun, brakeman.RefTask, brakeman.RefAttempt, brakeman.RefCheck, brakeman.RefCommand,
		brakeman.RefEvent, brakeman.RefMessage, brakeman.RefCommit, brakeman.RefCandidate:
		return brakeman.Ref{Kind: brakeman.RefRun, Run: r.Run}, r.Run != ""
	case brakeman.RefSession, brakeman.RefQuestion, brakeman.RefFile:
		return brakeman.Ref{Kind: brakeman.RefSession, Session: r.Session}, r.Session != ""
	case brakeman.RefTicket, brakeman.RefComment, brakeman.RefPlan:
		return brakeman.Ref{Kind: brakeman.RefTicket, Ticket: r.Ticket}, r.Ticket != ""
	case brakeman.RefLaunch, brakeman.RefBrief, brakeman.RefDecision:
		return r, true
	}
	return brakeman.Ref{}, false
}

// entryScope is the record e is of (its scope ref) and the ref a brief
// cites e by: e's own ref when Brakeman's grammar names it, whose record
// is then its scope; else the record's own ref, from what the entry says
// it is of (a decision's response its decision, a review of a ticket's
// its ticket). ok is false for an entry of no record a turn can be
// admitted to.
func entryScope(e history.Entry) (scope, cited brakeman.Ref, ok bool) {
	if r, err := brakeman.ParseRef(e.Ref); err == nil {
		if s, ok := scopeOfRef(r); ok {
			return s, r, true
		}
		return brakeman.Ref{}, brakeman.Ref{}, false
	}
	switch {
	case isDecisionKind(e.Kind):
		if id, _, ok := decisionOfRef(e.Ref); ok {
			s := brakeman.Ref{Kind: brakeman.RefDecision, Decision: id}
			return s, s, true
		}
	case storeOf(e) == "dispatch" && e.Ticket != "":
		s := brakeman.Ref{Kind: brakeman.RefTicket, Ticket: e.Ticket}
		if s.Valid() == nil {
			return s, s, true
		}
	}
	return brakeman.Ref{}, brakeman.Ref{}, false
}

// admit reports whether the turn may read e, and with which scope ref,
// its resolution and the ref it cites e by: every class e's text reads
// visible, its record within the scope now, and, for what a ticket keeps
// of a run, that run too.
func (hs *historyScope) admit(ctx context.Context, e history.Entry) (scope, cited brakeman.Ref, res brakeman.Resolution, ok bool, err error) {
	classes, known := historyReadClasses[e.Kind]
	if !known || slices.ContainsFunc(classes, func(c string) bool { return !hs.o.sees(c) }) {
		return scope, cited, res, false, nil
	}
	if scope, cited, ok = entryScope(e); !ok {
		return scope, cited, res, false, nil
	}
	if res, ok, err = hs.admitRef(ctx, scope); err != nil || !ok {
		return scope, cited, res, false, err
	}
	if slices.Contains(historyOfRun, e.Kind) {
		run := brakeman.Ref{Kind: brakeman.RefRun, Run: e.Run}
		if e.Run == "" || run.Valid() != nil {
			return scope, cited, res, false, nil
		}
		if _, ok, err = hs.admitRef(ctx, run); err != nil || !ok {
			return scope, cited, res, false, err
		}
	}
	return scope, cited, res, true, nil
}

// admitted reports whether the ref a link or an anchor names resolves
// within the grant (its record's resolution): false for one Brakeman's
// grammar does not name.
func (hs *historyScope) admitted(ctx context.Context, ref string) (bool, error) {
	r, err := brakeman.ParseRef(ref)
	if err != nil {
		return false, nil
	}
	scope, ok := scopeOfRef(r)
	if !ok {
		return false, nil
	}
	_, ok, err = hs.admitRef(ctx, scope)
	return ok, err
}

// noteEntry notes a served entry in the turn's read log: its cited ref
// in each class its text read (a brief's item in each of its footprint's,
// as an earlier brief given whole is), at its record's state now when it
// cites that record, else at its availability, with no revision.
func (hs *historyScope) noteEntry(e history.Entry, scope, cited brakeman.Ref, res brakeman.Resolution, av availabilityOf) {
	o := hs.o
	if o.tp == nil {
		return
	}
	state := av.State
	if cited == scope {
		state = res.Now.State
	}
	classes := historyReadClasses[e.Kind]
	if scope.Kind == brakeman.RefBrief {
		classes = []string{brakeman.ClassRuns}
		if req, err := o.rr.desk.Get(scope.Request); err == nil && req.Footprint != nil && len(req.Footprint.Classes) > 0 {
			classes = req.Footprint.Classes
		}
	}
	for _, c := range classes {
		o.note(cited, c, state, "")
	}
}

// turnEntry is e as served (out) for a turn: the ref it cites, its
// anchor and links those that resolve within the grant.
func (hs *historyScope) turnEntry(ctx context.Context, out servedEntry, cited brakeman.Ref) (turnHistoryEntry, error) {
	te := turnHistoryEntry{
		Ref: cited.String(), Kind: out.Kind, Class: out.Class, Outcome: out.Outcome, Title: out.Title, Summary: out.Summary,
		Snippet: out.Snippet, At: out.At, Actor: out.Actor, Source: turnSource{Store: out.Source.Store, Label: out.Source.Label},
		Host: out.Host, Project: out.Project, Revision: out.Revision, Agent: out.Agent, Crew: out.Crew, Run: out.Run, Ticket: out.Ticket,
		URL: out.URL, Links: []turnHistoryRef{}, Availability: out.Availability,
	}
	if out.Anchor.Ref != "" {
		ok, err := hs.admitted(ctx, out.Anchor.Ref)
		if err != nil {
			return te, err
		}
		if ok {
			a := out.Anchor
			te.Anchor = &a
		}
	}
	for _, l := range out.Links {
		ok, err := hs.admitted(ctx, l.Ref)
		if err != nil {
			return te, err
		}
		if ok {
			te.Links = append(te.Links, turnHistoryRef{Rel: l.Rel, Dir: l.Dir, Ref: l.Ref, Label: l.Label, Class: l.Class, Outcome: l.Outcome, URL: l.URL})
		}
	}
	return te, nil
}

// handleBrakemanHistory answers GET /api/brakeman/history?q=&limit=&after=:
// the History search's entries the caller may read, in the search's
// order, at most limit (1 to maxHistoryTurnPage, defaultHistoryTurnPage
// when absent) and maxObserveReply bytes, {entries, next?, indexing:
// {state}}; next reads on after the last row looked at, while the index
// holds more. The query's words and filters are the search's
// (handleHistorySearch), Brakeman's own runs and sessions left out: a
// turn's always, the admin's unless the line names a crew or the kind
// brief.item.
func (s *Server) handleBrakemanHistory(w http.ResponseWriter, r *http.Request, o *observer) {
	h, ok := s.historyRead(w, r, "q", "limit", "after")
	if !ok {
		return
	}
	defer h.release()
	q, ix, ctx := h.q, h.ix, h.ctx
	line, err := q.one("q")
	var after string
	if err == nil {
		after, err = q.one("after")
	}
	var limit int64
	if err == nil {
		limit, err = q.number("limit")
	}
	if err == nil && (limit < 0 || limit > maxHistoryTurnPage) {
		err = fmt.Errorf("limit is 1 to %d, not %d", maxHistoryTurnPage, limit)
	}
	if err != nil {
		h.refuse(http.StatusBadRequest, "bad_query", err.Error())
		return
	}
	if limit == 0 {
		limit = defaultHistoryTurnPage
	}
	if len(after) > maxHistoryAfter {
		h.refuse(http.StatusBadRequest, "bad_cursor", fmt.Sprintf("the cursor is %d bytes, more than %d", len(after), maxHistoryAfter))
		return
	}
	hq, err := history.ParseQuery(line)
	if err != nil {
		h.fail(err)
		return
	}
	_, crews := hq.Filter("crew")
	if kinds, _ := hq.Filter("kind"); o.tp != nil || (!crews && !slices.Contains(kinds, "brief.item")) {
		// A turn never reads Brakeman's own runs and sessions (admitRef),
		// whatever the line names; the admin's default leaves them out.
		hq.ExceptCrews = []string{crew.ClassBrakeman}
	}
	// The indexing's state alone: how far the index reaches, and what it
	// dropped, are counts of rows a turn may not see.
	indexing := func(pageState string) map[string]any {
		return map[string]any{"state": s.historyIndexing(ctx, nil, pageState)["state"]}
	}
	if ix == nil {
		h.reply(http.StatusOK, map[string]any{"entries": []any{}, "indexing": indexing("")})
		return
	}
	av := s.newAvailability(ctx, ix, s.ledger)
	defer av.close()
	hs := &historyScope{o: o, done: map[brakeman.Ref]historyAdmission{}}
	out := []turnHistoryEntry{}
	used, scanned := 128, 0
	next, cursor, state := "", after, ""
	for done := false; !done; {
		hq.Limit, hq.After = historyTurnPage, cursor
		page, err := ix.Search(ctx, hq)
		if err != nil {
			h.fail(err)
			return
		}
		state = page.State
		// before is the cursor of the rows from the page's i-th on.
		before := func(i int) string {
			if i == 0 {
				return cursor
			}
			return page.After(i - 1)
		}
		for i, hit := range page.Entries {
			if len(out) == int(limit) || scanned == maxHistoryTurnScan {
				next, done = before(i), true
				break
			}
			scanned++
			scope, cited, res, ok, err := hs.admit(ctx, hit.Entry)
			if err != nil {
				s.observeError(h.w, err)
				return
			}
			if !ok {
				continue
			}
			served := s.serveEntry(av, hit.Entry, hit.Snippet, history.Repeats{}, hit.Links, false)
			te, err := hs.turnEntry(ctx, served, cited)
			if err != nil {
				s.observeError(h.w, err)
				return
			}
			b, _ := json.Marshal(te)
			if len(out) > 0 && used+len(b)+1 > maxObserveReply {
				next, done = before(i), true
				break
			}
			used += len(b) + 1
			hs.noteEntry(hit.Entry, scope, cited, res, served.Availability)
			out = append(out, te)
		}
		if !done {
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
	}
	reply := map[string]any{"entries": out, "indexing": indexing(state)}
	if next != "" {
		reply["next"] = next
	}
	h.reply(http.StatusOK, reply)
}

// ---- the session journal as the resolver's third source

// recordRef reads the record of the session id from the journal, for a
// session the registry does not list: ok false when the server keeps no
// journal or the journal holds no record of id; an error for one it
// could not read, which is not its absence.
func (rr *refResolver) recordRef(id string) (journal.Record, bool, error) {
	j := rr.s.sessionRecords
	if j == nil {
		return journal.Record{}, false, nil
	}
	rec, _, err := j.Get(id)
	switch {
	case errors.Is(err, journal.ErrNotFound):
		return journal.Record{}, false, nil
	case err != nil:
		return journal.Record{}, false, fmt.Errorf("the record of session %s: %w", id, err)
	}
	return rec, true, nil
}

// recordRepo is the repository a record's session ran in, as the
// resolver's top answers a live session's: the worktree's top the
// session read at launch (Repo), a folder in it; "" for a hosted record
// (its folder is on its host, as a live hosted session's is), for none,
// and for a top its folder is not in (a working tree a repository's
// settings put elsewhere, which the resolver's top answers as none too).
func recordRepo(rec journal.Record) string {
	if rec.Host.Hosted || rec.Kind == "hosted" || rec.Repo == "" {
		return ""
	}
	dir := rec.Cwd
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	} else if _, serr := os.Lstat(dir); serr == nil {
		// There, and its links could not be followed: not a folder of
		// the top as far as anyone can tell here.
		return ""
	}
	if dir != rec.Repo && !folderWithin(dir, rec.Repo) {
		return ""
	}
	return rec.Repo
}

// recordState is a record's state as a ref's now: record, or lost when
// the server stopped observing its session while it ran.
func recordState(rec journal.Record) string {
	if rec.Ending == journal.EndingLost || rec.Ending == journal.EndingHostAway || !rec.LostAt.IsZero() {
		return availLost
	}
	return availRecord
}

// recordURL is a session record's page.
func recordURL(id string) string { return "/history/sessions/" + url.PathEscape(id) }
