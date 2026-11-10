package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
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
// where they resolve within the grant too, a link that shows its other
// end's indexed entry only where that entry would be served itself; none
// of what counts the index's other rows (a total, how often its words
// recur and where, how far the index reaches). Each served entry, and
// each anchor and link it carries, is noted in the turn's read log in
// each class it read, with no revision: a history row is a line of the
// index, not a snapshot of its record. A continuation names only what
// the reply showed: the position after its last entry, sealed for the
// caller and the line (sealContinuation); a reply of no entries carries
// none.

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
	// maxHistoryTurnAfter bounds a read's after: a continuation this
	// route gave, an index cursor (at most maxHistoryAfter) sealed.
	maxHistoryTurnAfter = 3 << 10
	// historyReplyReserve is what a reply holds beside its entries: its
	// braces and names, its indexing's state and a next at its bound.
	historyReplyReserve = 256 + maxHistoryTurnAfter
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
	// person's review of it, read with their ticket and of that run. An
	// agent reviewer's finding, kept by its attempt, is the run's alone
	// (entryClasses).
	"delivery.verified": {brakeman.ClassDispatch, brakeman.ClassRuns},
	"review.batch":      {brakeman.ClassDispatch, brakeman.ClassRuns},
	"review.finding":    {brakeman.ClassDispatch, brakeman.ClassRuns},
	"review.resolved":   {brakeman.ClassDispatch, brakeman.ClassRuns},

	"brief.item": {},
}

// entryClasses is every visibility class e's indexed text reads: its
// kind's (historyReadClasses), but an agent reviewer's finding, which its
// attempt keeps in the ledger (isReviewKind tells it from a person's,
// which a ticket keeps), the runs' as its verdict is. known is false for
// a kind the table does not name.
func entryClasses(e history.Entry) (classes []string, known bool) {
	if e.Kind == "review.finding" && !isReviewKind(e) {
		return []string{brakeman.ClassRuns}, true
	}
	classes, known = historyReadClasses[e.Kind]
	return classes, known
}

// keptOfRun reports whether e is what a ticket keeps of a run's
// candidate (its verified delivery, a person's review, its findings and
// their resolutions): admitted only when that run is too.
func keptOfRun(e history.Entry) bool {
	return e.Kind == "delivery.verified" || isReviewKind(e)
}

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
// once per read, and each indexed entry a link shows read once from the
// index (linked).
type historyScope struct {
	o      *observer
	ix     *history.Index
	av     *availability
	done   map[brakeman.Ref]historyAdmission
	linked map[string]*history.Entry
	// noted are the reads noted for the reply, each once.
	noted map[brakeman.Read]bool
}

// indexError is an error reading the index, which the history's read
// answers as the search does (historyReq.fail).
type indexError struct{ err error }

func (e indexError) Error() string { return e.err.Error() }
func (e indexError) Unwrap() error { return e.err }

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
	classes, known := entryClasses(e)
	if !known || slices.ContainsFunc(classes, func(c string) bool { return !hs.o.sees(c) }) {
		return scope, cited, res, false, nil
	}
	if scope, cited, ok = entryScope(e); !ok {
		return scope, cited, res, false, nil
	}
	if res, ok, err = hs.admitRef(ctx, scope); err != nil || !ok {
		return scope, cited, res, false, err
	}
	if keptOfRun(e) {
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

// refReads are the reads of a bare ref a link or an anchor names, ok
// when it resolves within the grant (its record's resolution): its class
// and its record's, at its record's state now when it is that record,
// else "linked"; ok false for one Brakeman's grammar does not name.
func (hs *historyScope) refReads(ctx context.Context, ref string) (reads []brakeman.Read, ok bool, err error) {
	r, err := brakeman.ParseRef(ref)
	if err != nil {
		return nil, false, nil
	}
	scope, ok := scopeOfRef(r)
	if !ok {
		return nil, false, nil
	}
	res, ok, err := hs.admitRef(ctx, scope)
	if err != nil || !ok {
		return nil, false, err
	}
	state := "linked"
	if r == scope {
		state = res.Now.State
	}
	classes := []string{refClass(r.Kind), refClass(scope.Kind)}
	if scope.Kind == brakeman.RefBrief {
		classes = hs.footprint(scope)
	}
	return readsOf(r, classes, state), true, nil
}

// footprint is the classes a brief's item reads: the ones its brief is
// admitted on (briefClasses: its footprint's, or every class its snapshot
// had on for one that keeps none), as an earlier brief given whole is;
// the runs' for one of none.
func (hs *historyScope) footprint(brief brakeman.Ref) []string {
	if req, err := hs.o.rr.desk.Get(brief.Request); err == nil {
		if classes := briefClasses(req); len(classes) > 0 {
			return classes
		}
	}
	return []string{brakeman.ClassRuns}
}

// readsOf is a read of ref in each class of classes, each once, at
// state, with no revision.
func readsOf(ref brakeman.Ref, classes []string, state string) []brakeman.Read {
	var out []brakeman.Read
	for _, c := range classes {
		rd := brakeman.Read{Ref: ref, Class: c, State: state}
		if c != "" && !slices.Contains(out, rd) {
			out = append(out, rd)
		}
	}
	return out
}

// entryReads are the reads of an entry served or shown by a link: its
// cited ref in each class its text read (a brief's item in each of its
// footprint's), at its record's state now when it cites that record,
// else at its availability (avState).
func (hs *historyScope) entryReads(e history.Entry, scope, cited brakeman.Ref, res brakeman.Resolution, avState string) []brakeman.Read {
	state := avState
	if cited == scope {
		state = res.Now.State
	}
	classes, _ := entryClasses(e)
	if scope.Kind == brakeman.RefBrief {
		classes = hs.footprint(scope)
	}
	return readsOf(cited, classes, state)
}

// linkedEntry is the index's entry of key, which a link shows: ok false
// for one the index no longer holds.
func (hs *historyScope) linkedEntry(ctx context.Context, key string) (history.Entry, bool, error) {
	if e, ok := hs.linked[key]; ok {
		if e == nil {
			return history.Entry{}, false, nil
		}
		return *e, true, nil
	}
	hit, err := hs.ix.Entry(ctx, key)
	switch {
	case errors.Is(err, history.ErrGone):
		hs.linked[key] = nil
		return history.Entry{}, false, nil
	case err != nil:
		return history.Entry{}, false, indexError{err}
	}
	hs.linked[key] = &hit.Entry
	return hit.Entry, true, nil
}

// note notes reads in the turn's read log, each once a reply.
func (hs *historyScope) note(reads []brakeman.Read) {
	for _, rd := range reads {
		if !hs.noted[rd] {
			hs.noted[rd] = true
			hs.o.noteRead(rd)
		}
	}
}

// turnEntry is e as served (out) for a turn, and the reads serving it
// makes: the ref it cites; its anchor and links only those whose record
// resolves within the grant, and a link that shows its other end's
// indexed entry (its title, class and outcome) only when that entry is
// admitted as an entry is; each read in its classes as an entry is
// (entryReads, refReads).
func (hs *historyScope) turnEntry(ctx context.Context, out servedEntry, e history.Entry, scope, cited brakeman.Ref, res brakeman.Resolution) (turnHistoryEntry, []brakeman.Read, error) {
	te := turnHistoryEntry{
		Ref: cited.String(), Kind: out.Kind, Class: out.Class, Outcome: out.Outcome, Title: out.Title, Summary: out.Summary,
		Snippet: out.Snippet, At: out.At, Actor: out.Actor, Source: turnSource{Store: out.Source.Store, Label: out.Source.Label},
		Host: out.Host, Project: out.Project, Revision: out.Revision, Agent: out.Agent, Crew: out.Crew, Run: out.Run, Ticket: out.Ticket,
		URL: out.URL, Links: []turnHistoryRef{}, Availability: out.Availability,
	}
	if !hs.o.sees(brakeman.ClassDispatch) {
		// A ticket's key and a project's name Dispatch's records, which
		// a turn that does not see Dispatch reads nothing of: an entry
		// of another class (a run launched from a ticket, a session in a
		// project's repository) is served without them.
		te.Ticket, te.Project.Key = "", ""
	}
	reads := hs.entryReads(e, scope, cited, res, out.Availability.State)
	if out.Anchor.Ref != "" {
		rds, ok, err := hs.refReads(ctx, out.Anchor.Ref)
		if err != nil {
			return te, nil, err
		}
		if ok {
			a := out.Anchor
			te.Anchor = &a
			reads = append(reads, rds...)
		}
	}
	for _, l := range out.Links {
		rds, ok, err := hs.refReads(ctx, l.Ref)
		if err != nil {
			return te, nil, err
		}
		if !ok {
			continue
		}
		if l.Key != "" {
			// The link shows the indexed entry at its other end: as that
			// entry is served, or not at all.
			le, found, err := hs.linkedEntry(ctx, l.Key)
			if err != nil {
				return te, nil, err
			}
			if !found {
				continue
			}
			ls, lc, lr, ok, err := hs.admit(ctx, le)
			if err != nil {
				return te, nil, err
			}
			if !ok {
				continue
			}
			rds = append(rds, hs.entryReads(le, ls, lc, lr, hs.av.of(le, false).State)...)
		}
		reads = append(reads, rds...)
		te.Links = append(te.Links, turnHistoryRef{Rel: l.Rel, Dir: l.Dir, Ref: l.Ref, Label: l.Label, Class: l.Class, Outcome: l.Outcome, URL: l.URL})
	}
	return te, reads, nil
}

// historySealer seals a history read's continuations under a key of the
// server's, made at its first use; a restart makes another, and the
// continuations given before it are refused.
type historySealer struct {
	once sync.Once
	aead cipher.AEAD
	err  error
}

func (hs *historySealer) get() (cipher.AEAD, error) {
	hs.once.Do(func() {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			hs.err = err
			return
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			hs.err = err
			return
		}
		hs.aead, hs.err = cipher.NewGCM(block)
	})
	return hs.aead, hs.err
}

// continuationData binds a continuation to the read it reads on: its
// caller (holder) and its line, each with its length.
func continuationData(holder, line string) []byte {
	return fmt.Appendf(nil, "conductor brakeman history next\x00%d:%s%d:%s", len(holder), holder, len(line), line)
}

// historyHolder is who a continuation is given to: the admin, or a turn
// by its request and session.
func historyHolder(o *observer) string {
	if o.tp == nil {
		return "admin"
	}
	return "turn " + o.tp.req.ID + " " + o.tp.session
}

// sealContinuation is the continuation of holder's read of line at pos,
// the index's cursor after the reply's last entry: pos sealed (AES-GCM),
// so its holder can neither read it nor make one, and it reads on only
// the read it was given for, on this server.
func (s *Server) sealContinuation(holder, line, pos string) (string, error) {
	aead, err := s.historySeal.get()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(pos)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(pos), continuationData(holder, line))), nil
}

// openContinuation is the index's cursor a continuation sealed for
// holder's read of line: ok false for anything else.
func (s *Server) openContinuation(holder, line, next string) (string, bool) {
	aead, err := s.historySeal.get()
	if err != nil {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(next)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return "", false
	}
	pos, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], continuationData(holder, line))
	if err != nil {
		return "", false
	}
	return string(pos), true
}

// handleBrakemanHistory answers GET /api/brakeman/history?q=&limit=&after=:
// the History search's entries the caller may read, in the search's
// order, at most limit (1 to maxHistoryTurnPage, defaultHistoryTurnPage
// when absent) and maxObserveReply bytes, next among them, {entries,
// next?, indexing: {state}}. next, on a reply with entries, reads on
// after its last entry (sealed for the caller and the line); a reply with
// none carries none, so a read whose maxHistoryTurnScan rows looked at
// the grant admits none of ends there. The query's words and filters are
// the search's (handleHistorySearch), Brakeman's own runs and sessions
// left out: a turn's always, the admin's unless the line names a crew or
// the kind brief.item.
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
	if len(after) > maxHistoryTurnAfter {
		h.refuse(http.StatusBadRequest, "bad_cursor", fmt.Sprintf("the cursor is %d bytes, more than %d", len(after), maxHistoryTurnAfter))
		return
	}
	holder, cursor := historyHolder(o), ""
	if after != "" {
		var ok bool
		if cursor, ok = s.openContinuation(holder, line, after); !ok {
			h.refuse(http.StatusBadRequest, "bad_cursor", "the cursor is not a next this read of this line gave")
			return
		}
	}
	hq, err := history.ParseQuery(line)
	if err != nil {
		h.fail(err)
		return
	}
	if !o.sees(brakeman.ClassDispatch) {
		// A filter on a field the turn is not given (turnEntry) would
		// read it all the same, by which entries it finds.
		for _, name := range []string{"ticket", "project"} {
			if _, ok := hq.Filter(name); ok {
				h.refuse(http.StatusForbidden, "not_visible", name+": filters on Dispatch's records, which this turn does not see")
				return
			}
		}
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
	hs := &historyScope{o: o, ix: ix, av: av, done: map[brakeman.Ref]historyAdmission{}, linked: map[string]*history.Entry{}, noted: map[brakeman.Read]bool{}}
	fail := func(err error) {
		if ierr := (indexError{}); errors.As(err, &ierr) {
			h.fail(ierr.err)
			return
		}
		s.observeError(h.w, err)
	}
	bound := maxObserveReply
	if b := s.historyTurnReply.Load(); b > 0 {
		bound = int(b)
	}
	out := []turnHistoryEntry{}
	used, scanned := historyReplyReserve, 0
	last, state := "", ""
	for done := false; !done; {
		hq.Limit, hq.After = historyTurnPage, cursor
		page, err := ix.Search(ctx, hq)
		if err != nil {
			h.fail(err)
			return
		}
		state = page.State
		for i, hit := range page.Entries {
			if len(out) == int(limit) || scanned == maxHistoryTurnScan {
				done = true
				break
			}
			scanned++
			scope, cited, res, ok, err := hs.admit(ctx, hit.Entry)
			if err != nil {
				fail(err)
				return
			}
			if !ok {
				continue
			}
			served := s.serveEntry(av, hit.Entry, hit.Snippet, history.Repeats{}, hit.Links, false)
			te, reads, err := hs.turnEntry(ctx, served, hit.Entry, scope, cited, res)
			if err != nil {
				fail(err)
				return
			}
			b, _ := json.Marshal(te)
			if len(out) > 0 && used+len(b)+1 > bound {
				done = true
				break
			}
			used += len(b) + 1
			hs.note(reads)
			out = append(out, te)
			last = page.After(i)
		}
		if !done {
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
	}
	reply := map[string]any{"entries": out, "indexing": indexing(state)}
	if len(out) > 0 && last != "" {
		// After the last entry served, whether or not the index holds
		// more: whether it does is a count of rows the caller may not see.
		next, err := s.sealContinuation(holder, line, last)
		if err != nil {
			s.observeError(h.w, err)
			return
		}
		if len(next) <= maxHistoryTurnAfter {
			reply["next"] = next
		}
	}
	h.reply(http.StatusOK, reply)
}

// ---- the session journal as the resolver's third source

// recordRef reads the record of the session id from the journal, for a
// session the registry does not list: ok false when the server keeps no
// journal (no data directory, a switchyard) or the journal holds no
// record of id; an error for a journal the server keeps and could not
// open (as the ledger and Dispatch's store answer one), or a record it
// could not read, neither of which is its absence.
func (rr *refResolver) recordRef(id string) (journal.Record, bool, error) {
	j := rr.s.sessionRecords
	switch {
	case j == nil && (rr.s.store == nil || rr.s.cfg.Switchyard.Enabled):
		return journal.Record{}, false, nil
	case j == nil:
		return journal.Record{}, false, &errRefStore{what: "the session records"}
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
