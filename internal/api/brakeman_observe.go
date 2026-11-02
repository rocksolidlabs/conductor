package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// What Brakeman reads (docs/plans/brakeman.md, PR-4). A turn reads
// through the routes every client reads, not a second set of its own: a
// request whose bearer is a turn's session token and whose
// X-Conductor-Session header names that session, its request starting
// or running, authenticates as the principal brakeman, and s.reader takes
// the admin or that principal on exactly the shared reads routeBrakeman
// lists, each answered by its own handler. The principal's reply is the
// route's own answer filtered by the turn's scope and visibility (the
// snapshot it began under intersected with the configuration now), each
// item carrying the ref a brief cites it by, counted against the turn's
// ReadsPerTurn and noted in its read log. What no route serves today is
// new and typed, under GET /api/brakeman/: the changes, the status
// Conductor computes, a screen's last lines, the overlapping changes and
// the earlier briefs.

// brakemanSessionHeader names the session a Brakeman turn's read is made
// by: with that session's own token as the bearer, the read is the
// principal brakeman's.
const brakemanSessionHeader = "X-Conductor-Session"

// The bounds of Brakeman's own reads.
const (
	// maxObserveReply bounds each answer of GET /api/brakeman/….
	maxObserveReply = 256 << 10
	// maxChangesPage bounds a page of changes, and defaultChangesPage is
	// a page asked for with no limit.
	maxChangesPage     = 200
	defaultChangesPage = 100
	// maxScreenLines bounds a screen read, defaultScreenLines one asked
	// for with no lines.
	maxScreenLines     = 200
	defaultScreenLines = 50
	// maxOverlapTrees and maxOverlapPaths bound the overlaps' trees and
	// the paths read from them.
	maxOverlapTrees = 32
	maxOverlapPaths = 2000
	// maxBriefsPage bounds a page of earlier briefs, and
	// maxBriefsPageRefs the refs each carries.
	maxBriefsPage     = 20
	maxBriefsPageRefs = 100
	// maxMessagesPage bounds a page of a run's messages a turn reads, and
	// defaultMessagesPage is a page asked for with no limit: a run keeps
	// up to 4,096 messages of 4 KiB, more than one answer holds, so a
	// turn reads them a page at a time, each at most maxObserveReply.
	maxMessagesPage     = 200
	defaultMessagesPage = 100
	// maxRunsPage bounds a page of the runs a turn reads, and
	// defaultRunsPage is a page asked for with no limit: as many as the
	// engine keeps (maxRuns), each page at most maxTurnReply.
	maxRunsPage     = 100
	defaultRunsPage = 100
	// quietAfter is how long a server session's terminal has written
	// nothing before its status is quiet.
	quietAfter = 10 * time.Minute
	// defaultBriefingWindow is how far a briefing with no start and no
	// earlier briefing reads back.
	defaultBriefingWindow = 24 * time.Hour
)

// maxTurnReply bounds every answer a turn reads, a shared read's as its
// filter leaves it: past it the read is refused (413 bound), not counted,
// so the guide's bound for each observe_<what> holds and a client reads
// at most it. The largest a shared route makes for a turn is a ticket's
// (MaxTicketReply) with its refs; a run's messages and the runs are
// paged. A test
// makes it smaller.
var maxTurnReply = 4 << 20

// changesWindow is how many of a run's newest events, and of Dispatch's
// newest changes, a read of what changed since a time looks at: one with
// more since then is marked truncated. A test makes it smaller.
var changesWindow int64 = 5000

// turnPrincipal is a Brakeman turn as the principal brakeman: its session,
// its request as read when the read began, and the configuration now.
type turnPrincipal struct {
	session string
	req     brakeman.Request
	cfg     brakeman.Config
}

// turnPrincipalOf authenticates r as a Brakeman turn: nil for a request
// that names no session in X-Conductor-Session (or a server with no
// desk), an error for one that names a session whose turn it is not.
func (s *Server) turnPrincipalOf(r *http.Request) (*turnPrincipal, *apiError) {
	id := r.Header.Get(brakemanSessionHeader)
	if id == "" || s.brakeman == nil {
		return nil, nil
	}
	d := s.brakeman
	drv, ok := s.registry.Get(id)
	if !ok || !agentTokenOK(drv, presentedToken(r, false)) {
		// Not the named session's own token: the request is any other's,
		// the workbench token's or none.
		return nil, nil
	}
	ref := drv.Info().Crew
	if ref == nil || ref.Brakeman == nil {
		return nil, newAPIError(http.StatusForbidden, "not_brakeman", "the session is not a Brakeman turn's: its token reads none of these")
	}
	if d.recovering.Load() {
		return nil, newAPIError(http.StatusServiceUnavailable, "brakeman_recovering", "Brakeman's records are being recovered: ask again in a moment")
	}
	req, err := d.st.Get(ref.Brakeman.Request)
	if err != nil {
		return nil, newAPIError(http.StatusNotFound, "not_found", "the session's request is not kept")
	}
	if req.State != brakeman.StateStarting && req.State != brakeman.StateRunning {
		return nil, newAPIError(http.StatusConflict, "turn_over", "the request is "+req.State+": its turn reads nothing more")
	}
	if req.RunID != "" && req.RunID != ref.RunID {
		return nil, newAPIError(http.StatusForbidden, "not_the_turn", "the session is not the request's run's")
	}
	cfg, err := d.st.Config()
	if err != nil {
		return nil, newAPIError(http.StatusInternalServerError, "store_failed", "Brakeman's configuration could not be read")
	}
	return &turnPrincipal{session: id, req: req, cfg: cfg}, nil
}

// observer is one read as its caller makes it: the admin's (tp nil), who
// sees everything, or a Brakeman turn's, held to its grant, whose reads
// are noted.
type observer struct {
	tp    *turnPrincipal
	grant refGrant
	rr    *refResolver
	now   time.Time
	reads []brakeman.Read
	// coverage, set by a changes read a turn made from its own start, is
	// what its pages reached, written into the request with the read.
	coverage *brakeman.Coverage
	// revsBefore is the ledger's revision of each run a runs read names,
	// read just before its handler ran (deliveredRev), and engBefore the
	// engine's hold of each, read before that; nil for another read.
	revsBefore map[string]string
	engBefore  map[string]engineHold
}

// engineHold is a run as the engine holds it (Engine.Settled): the
// revision of its last commit, whether nothing of it waits for or is in a
// commit, and whether the engine holds it at all.
type engineHold struct {
	rev     int64
	settled bool
	held    bool
}

// engineHoldOf is the engine's hold of the run id now.
func (s *Server) engineHoldOf(id string) engineHold {
	rev, settled, held := s.runs.Settled(id)
	return engineHold{rev: rev, settled: settled, held: held}
}

// observeSeam, when a test sets it, runs at each stage of a turn's
// shared read: "admitted" once what its path names is admitted, before
// the revisions it is read at are taken; "answered" once the route's
// handler answered, before its filter; "listed" once a read of changes
// has read its parts, before its coverage is written.
var observeSeam atomic.Pointer[func(stage string)]

func atObserveSeam(stage string) {
	if f := observeSeam.Load(); f != nil {
		(*f)(stage)
	}
}

// takeRevs reads, for a read of the runs' class, the engine's hold of
// the run its path names (every run's for GET /api/runs) and then the
// ledger's revision of it, just before the route's handler runs.
func (o *observer) takeRevs(r *http.Request, class string) error {
	s := o.rr.s
	ld := s.ledger
	if class != brakeman.ClassRuns || ld == nil {
		return nil
	}
	o.revsBefore, o.engBefore = map[string]string{}, map[string]engineHold{}
	id := r.PathValue("run")
	if id == "" {
		// The engine first, then the ledger: a commit the engine began
		// before the ledger was read is seen unsettled, or as committed.
		for _, run := range s.runs.List() {
			o.engBefore[run.ID] = s.engineHoldOf(run.ID)
		}
		rows, err := ld.Runs(r.Context())
		if err != nil {
			return fmt.Errorf("the ledger's runs: %w", err)
		}
		for _, row := range rows {
			o.revsBefore[row.ID] = strconv.FormatInt(row.Revision, 10)
		}
		return nil
	}
	if !crew.ValidRunID(id) {
		return nil
	}
	o.engBefore[id] = s.engineHoldOf(id)
	row, ok, err := ld.Run(r.Context(), id)
	if err != nil {
		return fmt.Errorf("the ledger's run %s: %w", id, err)
	}
	if ok {
		o.revsBefore[id] = strconv.FormatInt(row.Revision, 10)
	}
	return nil
}

// deliveredRev is the revision a read of the run id was answered at: a
// managed run's the ledger's, when it was the same just before the
// route's handler ran and after it (every transition moves it) and the
// engine, holding the run, held it settled at one revision throughout
// (it changes its run before the transaction commits, so a handler that
// read the engine's run then may have answered a state the ledger's
// revision does not hold yet); "" otherwise, for what was delivered may
// be either's; any other run's its record's.
func (o *observer) deliveredRev(ctx context.Context, id string, run *refRun) (string, error) {
	ld := o.rr.s.ledger
	if !run.managed || ld == nil || o.revsBefore == nil {
		return run.rev, nil
	}
	before, ok := o.revsBefore[id]
	if !ok {
		return "", nil
	}
	// The ledger first, then the engine: the reverse of takeRevs.
	row, found, err := ld.Run(ctx, id)
	if err != nil {
		return "", fmt.Errorf("the ledger's run %s: %w", id, err)
	}
	if !found || strconv.FormatInt(row.Revision, 10) != before {
		return "", nil
	}
	if !settledThrough(o.engBefore[id], o.rr.s.engineHoldOf(id)) {
		return "", nil
	}
	return before, nil
}

// settledThrough reports whether the engine's two holds of a run, read
// before a read of it and after, say nothing of it changed between that
// its ledger has not committed: it held the run settled at one revision
// both times, or held it neither time.
func settledThrough(before, after engineHold) bool {
	if !before.held && !after.held {
		return true
	}
	return before.held && after.held && before.settled && after.settled && before.rev == after.rev
}

// note notes a read of ref (a turn's only).
func (o *observer) note(ref brakeman.Ref, class, state, rev string) {
	if o.tp == nil || ref.IsZero() || !slices.Contains(brakeman.Classes, class) {
		return
	}
	o.reads = append(o.reads, brakeman.Read{Ref: ref, Class: class, State: cutBytes(state, brakeman.MaxRefState), Rev: cutBytes(rev, brakeman.MaxRevision), At: o.now})
}

// noteRef is note for a ref given as a string, which must parse.
func (o *observer) noteRef(ref, class, state, rev string) {
	if o.tp == nil {
		return
	}
	if r, err := brakeman.ParseRef(ref); err == nil {
		o.note(r, class, state, rev)
	}
}

// sees reports whether the caller may see class.
func (o *observer) sees(class string) bool { return o.tp == nil || o.grant.sees(class) }

// newObserver is the observer of r's caller: the admin's, or tp's.
func (s *Server) newObserver(tp *turnPrincipal) *observer {
	var desk *brakeman.Store
	if s.brakeman != nil {
		desk = s.brakeman.st
	}
	now := time.Now().UTC()
	if s.brakeman != nil {
		now = s.brakeman.now().UTC()
	}
	if tp == nil {
		// The admin's grant is everything: no scope, no visibility.
		rr := &refResolver{s: s, desk: desk, at: now, seen: map[brakeman.Ref]brakeman.Read{}, runs: map[string]*refRun{}, tops: map[string]string{}}
		return &observer{rr: rr, now: now}
	}
	rr := s.newRefResolver(desk, tp.req, brakeman.ReadLog{}, tp.cfg)
	rr.at = now
	return &observer{tp: tp, grant: rr.grant, rr: rr, now: now}
}

// readSpec is what s.reader asks of a shared read for a turn: the
// visibility class it reads, the check of what its path names before its
// handler runs (nil for none), and the filter of its answer (nil to pass
// it as it is).
type readSpec struct {
	class  string
	admit  func(r *http.Request, o *observer) (ok bool, err error)
	filter func(r *http.Request, o *observer, body []byte) ([]byte, error)
}

// reader takes the admin, as requireAdmin does, or a Brakeman turn (the
// principal brakeman) on a shared read: the route's own handler answers
// it, filtered by spec for the turn, counted against its reads and noted
// in its read log. Anyone else is refused as requireAdmin refuses.
func (s *Server) reader(spec readSpec, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A turn's token is looked at first, so it is never asked of the
		// token chain (and an identity provider) as a token of theirs.
		tp, aerr := s.turnPrincipalOf(r)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		if tp != nil {
			s.turnRead(w, r, tp, spec, next)
			return
		}
		if p := s.authenticate(r, false); p.admin {
			next(w, withPrincipal(r, p))
			return
		}
		s.refuseAdmin(w, r)
	}
}

// refuseAdmin answers a request that is neither the admin's nor a
// turn's as requireAdmin answers it, without asking the token chain (and
// an identity provider) a second time.
func (s *Server) refuseAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="conductor"`)
	writeError(w, http.StatusUnauthorized, "unauthorized", "workbench token required")
}

// observeRoute is a route of Brakeman's own reads (GET /api/brakeman/…):
// the admin's, or a turn's, held as reader holds a shared read, with h
// writing its answer and noting what it read through the observer.
func (s *Server) observeRoute(h func(w http.ResponseWriter, r *http.Request, o *observer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.brakemanDeskOf(w); !ok {
			return
		}
		tp, aerr := s.turnPrincipalOf(r)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		if tp != nil {
			s.turnRead(w, r, tp, readSpec{}, nil, h)
			return
		}
		if p := s.authenticate(r, false); p.admin {
			h(w, withPrincipal(r, p), s.newObserver(nil))
			return
		}
		s.refuseAdmin(w, r)
	}
}

// turnRead answers a turn's read: refused once its reads are spent (429
// reads_spent) or its class is not visible (403 not_visible), or what its
// path names lies outside its scope (403 outside_scope); else the
// handler's answer, filtered, with what it read noted and the read
// counted, under the desk's read mutex, so the count and the log are one
// turn's in order. Exactly one of next and own is given.
func (s *Server) turnRead(w http.ResponseWriter, r *http.Request, tp *turnPrincipal, spec readSpec, next http.HandlerFunc, own ...func(http.ResponseWriter, *http.Request, *observer)) {
	d := s.brakeman
	d.readMu.Lock()
	defer d.readMu.Unlock()
	req, err := d.st.Get(tp.req.ID)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if !req.Open() || req.State == brakeman.StateQueued {
		writeError(w, http.StatusConflict, "turn_over", "the request is "+req.State+": its turn reads nothing more")
		return
	}
	tp.req = req
	if req.Reads >= req.Config.Budget.ReadsPerTurn {
		writeError(w, http.StatusTooManyRequests, "reads_spent", fmt.Sprintf("the turn's %d reads are spent: submit the brief with what you have", req.Config.Budget.ReadsPerTurn))
		return
	}
	o := s.newObserver(tp)
	if spec.class != "" && !o.grant.sees(spec.class) {
		writeError(w, http.StatusForbidden, "not_visible", "Brakeman may not see "+spec.class)
		return
	}
	if spec.admit != nil {
		ok, err := spec.admit(r, o)
		switch {
		case err != nil:
			s.observeError(w, err)
			return
		case !ok:
			writeError(w, http.StatusForbidden, "outside_scope", "outside the request's scope")
			return
		}
	}
	atObserveSeam("admitted")
	if next != nil {
		if err := o.takeRevs(r, spec.class); err != nil {
			s.observeError(w, err)
			return
		}
	}
	rec := newReplyRecorder()
	if next != nil {
		next(rec, r)
	} else {
		own[0](rec, r, o)
	}
	atObserveSeam("answered")
	body := rec.body.Bytes()
	if rec.status == http.StatusOK && spec.filter != nil {
		if body, err = spec.filter(r, o, body); err != nil {
			s.observeError(w, err)
			return
		}
	}
	if rec.status == http.StatusOK && len(body) > maxTurnReply {
		// Refused before it is counted or noted: the turn read nothing.
		writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the answer is larger than a turn's %d MiB: ask for less (a page, a narrower query)", maxTurnReply>>20))
		return
	}
	if _, err := d.st.NoteAnswer(req.ID, o.reads...); err != nil {
		s.brakemanError(w, err)
		return
	}
	if rec.status == http.StatusOK && spec.class != "" {
		// The answer used its class whether or not it held an item a
		// ref names (a page of projects): the brief's footprint counts it.
		d.usedClass(req.ID, spec.class)
	}
	if o.coverage != nil {
		if _, err := d.update(req.ID, func(q *brakeman.Request) error {
			if q.Open() {
				q.Coverage = o.coverage
			}
			return nil
		}); err != nil {
			s.log.Warn("a Brakeman turn's coverage could not be written", "request", req.ID, "err", err)
		}
	}
	h := w.Header()
	for k, v := range rec.header {
		h[k] = v
	}
	if rec.status == http.StatusOK && spec.filter != nil {
		h.Del("Content-Length")
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(body)
}

// observeError answers an error of a read: a store the server could not
// open as the other routes answer it, anything else as a failure to read.
func (s *Server) observeError(w http.ResponseWriter, err error) {
	var unopened *errRefStore
	var refused *apiError
	if errors.As(err, &refused) {
		writeAPIError(w, refused)
		return
	}
	if errors.As(err, &unopened) {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", unopened.Error())
		return
	}
	s.log.Warn("a Brakeman read could not be answered", "err", err)
	writeError(w, http.StatusInternalServerError, "store_failed", "the records could not be read")
}

// replyRecorder keeps a handler's answer for the filter.
type replyRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newReplyRecorder() *replyRecorder { return &replyRecorder{header: http.Header{}} }

func (rr *replyRecorder) Header() http.Header { return rr.header }
func (rr *replyRecorder) WriteHeader(code int) {
	if rr.status == 0 {
		rr.status = code
	}
}
func (rr *replyRecorder) Write(b []byte) (int, error) {
	if rr.status == 0 {
		rr.status = http.StatusOK
	}
	return rr.body.Write(b)
}

// withRef is the JSON object raw with "ref" set to ref.
func withRef(raw json.RawMessage, ref string) (json.RawMessage, error) {
	return withFields(raw, ref, "")
}

// withFields is the JSON object raw with "ref" set to ref and, when rev
// is not empty, "revision" to rev: the run's revision it was read at.
func withFields(raw json.RawMessage, ref, rev string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(ref)
	m["ref"] = b
	if rev != "" {
		m["revision"], _ = json.Marshal(rev)
	}
	return marshalNoEscape(m)
}

// marshalNoEscape is v as JSON, '<', '>' and '&' as they are (as
// writeDispatch writes them), with no trailing newline.
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// refreshed is body, a JSON object, with its field name replaced by list.
func refreshed(body []byte, name string, list any) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	b, err := marshalNoEscape(list)
	if err != nil {
		return nil, err
	}
	m[name] = b
	out, err := marshalNoEscape(m)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// ---- the shared reads' specs

// runAdmitted reports whether the run id is in the observer's grant.
func (o *observer) runAdmitted(ctx context.Context, id string) (*refRun, bool, error) {
	run, err := o.rr.run(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return run, o.grant.admits(run.item), nil
}

// admitRunPath admits a route whose path names a run in the grant.
func admitRunPath(r *http.Request, o *observer) (bool, error) {
	id := r.PathValue("run")
	if !crew.ValidRunID(id) {
		return true, nil // the handler answers it
	}
	_, ok, err := o.runAdmitted(r.Context(), id)
	return ok, err
}

// admitTicketPath admits a route whose path names a ticket in the grant.
func admitTicketPath(r *http.Request, o *observer) (bool, error) {
	key := r.PathValue("key")
	if _, _, ok := dispatch.ParseKey(key); !ok {
		return true, nil
	}
	return o.ticketAdmitted(r.Context(), key)
}

// ticketAdmitted reports whether the ticket key is in the grant: its
// project, and the runs linked to it when the scope names runs or crews.
func (o *observer) ticketAdmitted(ctx context.Context, key string) (bool, error) {
	it := brakeman.ScopeItem{Kind: brakeman.ItemTicket, Project: projectOfKey(key)}
	if o.grant.byLinks() {
		links, err := o.rr.ticketLinks(ctx, key)
		if err != nil {
			return false, err
		}
		it.Links = links
	}
	return o.grant.admits(it), nil
}

// byLinks reports whether the grant admits tickets by the runs linked to
// them.
func (g refGrant) byLinks() bool {
	return slices.ContainsFunc(g.scopes, func(s brakeman.Scope) bool { return len(s.Runs) > 0 || len(s.Crews) > 0 })
}

// projectAdmitted reports whether a project's key is in the grant: by the
// projects the scope names alone (a project has no run of its own).
func (g refGrant) projectAdmitted(key string) bool {
	for _, s := range g.scopes {
		if len(s.Projects) > 0 && !slices.Contains(s.Projects, key) {
			return false
		}
	}
	return true
}

// filterRuns is GET /api/runs for a turn, a page at a time: the runs in
// its grant, newest first, after the run after names (none: from the
// newest), at most limit (1 to maxRunsPage, defaultRunsPage when absent)
// and maxTurnReply bytes, with next, the last run given, while more
// remain; each with its ref, and only the page's noted at their state and
// revision. A run's members' prompts alone can be 192 KB, and the engine
// keeps maxRuns of them, more than one answer holds. A page holds one run
// at least: a run that alone passes the bound is given short (fitRun).
func filterRuns(r *http.Request, o *observer, body []byte) ([]byte, error) {
	q, err := readQuery(r, "after", "limit")
	var after string
	var limit int
	if err == nil {
		after, err = q.one("after")
	}
	if err == nil {
		limit, err = q.count("limit")
	}
	if err == nil && (limit < 0 || limit > maxRunsPage) {
		err = fmt.Errorf("limit is 1 to %d", maxRunsPage)
	}
	if err == nil && after != "" && !crew.ValidRunID(after) {
		err = errors.New("after is the next of the page before, a run's id")
	}
	if err != nil {
		return nil, newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if limit == 0 {
		limit = defaultRunsPage
	}
	var in struct {
		Runs []json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	type listed struct {
		id  string
		raw json.RawMessage
	}
	runs := make([]listed, 0, len(in.Runs))
	start := 0
	for _, raw := range in.Runs {
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return nil, err
		}
		runs = append(runs, listed{head.ID, raw})
		if after != "" && head.ID == after {
			start = len(runs)
		}
	}
	if after != "" && start == 0 {
		// The page before's last run has left the engine since: where
		// the list went on from is lost, so it is read again from the
		// first page, never with runs skipped unsaid.
		return nil, newAPIError(http.StatusBadRequest, "invalid_request", "after names a run the server no longer holds: read the runs again from the first page")
	}
	// The page's frame: {"runs":[…],"next":"<a run's id>"}, a run id
	// at most a crew id's 64 bytes and its suffix.
	used := len(`{"runs":[],"next":""}`+"\n") + 64 + 9
	page := []json.RawMessage{}
	var next, last string
	for _, l := range runs[start:] {
		if len(page) == limit {
			next = last
			break
		}
		run, ok, err := o.runAdmitted(r.Context(), l.id)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ref := "run:" + l.id
		rev, err := o.deliveredRev(r.Context(), l.id, run)
		if err != nil {
			return nil, err
		}
		b, err := withFields(l.raw, ref, rev)
		if err != nil {
			return nil, err
		}
		if used+len(b)+1 > maxTurnReply {
			if len(page) > 0 {
				next = last
				break
			}
			if b, err = fitRun(b, maxTurnReply-used-1); err != nil {
				return nil, err
			}
		}
		o.noteRef(ref, brakeman.ClassRuns, run.state, rev)
		page = append(page, b)
		used += len(b) + 1
		last = l.id
	}
	out := map[string]any{"runs": page}
	if next != "" {
		out["next"] = next
	}
	b, err := marshalNoEscape(out)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// filterRun is GET /api/runs/{run} for a turn: the run and its tasks,
// each with its ref and noted; a run past maxTurnReply is given short
// (fitRun).
func filterRun(r *http.Request, o *observer, body []byte) ([]byte, error) {
	id := r.PathValue("run")
	var in struct {
		Run json.RawMessage `json:"run"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	run, err := o.rr.run(r.Context(), id)
	if err != nil {
		return nil, err
	}
	rev, err := o.deliveredRev(r.Context(), id, run)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(in.Run, &m); err != nil {
		return nil, err
	}
	o.noteRef("run:"+id, brakeman.ClassRuns, run.state, rev)
	if raw, ok := m["tasks"]; ok {
		var tasks []json.RawMessage
		if err := json.Unmarshal(raw, &tasks); err != nil {
			return nil, err
		}
		for i, t := range tasks {
			var head struct {
				ID    string `json:"id"`
				State string `json:"state"`
			}
			if err := json.Unmarshal(t, &head); err != nil {
				return nil, err
			}
			ref := "task:" + id + "/" + head.ID
			if tasks[i], err = withRef(t, ref); err != nil {
				return nil, err
			}
			o.noteRef(ref, brakeman.ClassRuns, head.State, rev)
		}
		if m["tasks"], err = marshalNoEscape(tasks); err != nil {
			return nil, err
		}
	}
	b, _ := json.Marshal("run:" + id)
	m["ref"] = b
	if rev != "" {
		m["revision"], _ = json.Marshal(rev)
	}
	// The answer's frame, {"run":…} and whatever else the route says
	// beside it (record), is what the run leaves of the bound.
	frame, err := refreshed(body, "run", json.RawMessage("0"))
	if err != nil {
		return nil, err
	}
	whole, err := marshalNoEscape(m)
	if err != nil {
		return nil, err
	}
	fit, err := fitRun(whole, maxTurnReply-(len(frame)-1))
	if err != nil {
		return nil, err
	}
	return refreshed(body, "run", fit)
}

// runCuts are what fitRun leaves out of a run past its room, in order:
// first what the turn reads whole elsewhere, then what it needs least.
//   - tasks: each task's result's text (summary, issues, findings), its
//     latest attempt's checkpoint and error, and its blocker's message,
//     which the task's own read (observe task) gives whole;
//   - members: each member's prompt and args;
//   - log: the run's log's oldest entries, the newest that fit kept;
//   - chat: the run's chat's oldest messages, the newest that fit kept.
var runCuts = []string{"tasks", "members", "log", "chat"}

// fitRun is a run as a turn reads it, the JSON object raw, within room
// bytes: raw itself when it fits, else the run with runCuts made one at a
// time until it does, its cut naming each made. A managed run of 128
// tasks, each with a 4,000-character summary, twenty issues and fifty
// findings, is far past a turn's answer; its short form is not. A run
// still past room with every cut made is refused 413 bound.
func fitRun(raw json.RawMessage, room int) (json.RawMessage, error) {
	if len(raw) <= room {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	var cut []string
	for _, c := range runCuts {
		list, ok := m[c]
		if !ok {
			continue
		}
		cut = append(cut, c)
		m["cut"], _ = json.Marshal(cut)
		var err error
		switch c {
		case "tasks":
			m[c], err = cutFields(list, map[string][]string{"result": {"summary", "issues", "findings"}, "attempt": {"checkpoint", "error"}, "blocker": {"message"}})
		case "members":
			m[c], err = cutFields(list, map[string][]string{"": {"prompt", "args"}})
		default:
			m[c], err = newestThatFit(m, c, room)
		}
		if err != nil {
			return nil, err
		}
		out, err := marshalNoEscape(m)
		if err != nil {
			return nil, err
		}
		if len(out) <= room {
			return out, nil
		}
	}
	return nil, newAPIError(http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the run is larger than a turn's %d MiB even short", maxTurnReply>>20))
}

// cutFields is list, a JSON array of objects, with the fields drop names
// left out: drop[""] of each object itself, drop[f] of the object in its
// field f.
func cutFields(list json.RawMessage, drop map[string][]string) (json.RawMessage, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(list, &items); err != nil {
		return nil, err
	}
	for _, it := range items {
		for f, fields := range drop {
			if f == "" {
				for _, k := range fields {
					delete(it, k)
				}
				continue
			}
			raw, ok := it[f]
			if !ok || string(raw) == "null" {
				continue
			}
			var inner map[string]json.RawMessage
			if err := json.Unmarshal(raw, &inner); err != nil {
				return nil, err
			}
			for _, k := range fields {
				delete(inner, k)
			}
			b, err := marshalNoEscape(inner)
			if err != nil {
				return nil, err
			}
			it[f] = b
		}
	}
	return marshalNoEscape(items)
}

// newestThatFit is m's list field, oldest first, cut to its newest
// entries that leave m within room: none when the rest of m alone fills
// it.
func newestThatFit(m map[string]json.RawMessage, field string, room int) (json.RawMessage, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(m[field], &entries); err != nil {
		return nil, err
	}
	m[field] = json.RawMessage("[]")
	rest, err := marshalNoEscape(m)
	if err != nil {
		return nil, err
	}
	free, from := room-len(rest), len(entries)
	for from > 0 {
		e, err := marshalNoEscape(entries[from-1])
		if err != nil {
			return nil, err
		}
		if free -= len(e) + 1; free < 0 {
			break
		}
		from--
	}
	return marshalNoEscape(entries[from:])
}

// filterTask is GET /api/runs/{run}/tasks/{task} for a turn: the task,
// its attempts and their receipts, each with its ref and noted.
func filterTask(r *http.Request, o *observer, body []byte) ([]byte, error) {
	runID, taskID := r.PathValue("run"), r.PathValue("task")
	var in struct {
		Task     json.RawMessage   `json:"task"`
		Attempts []json.RawMessage `json:"attempts"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	run, err := o.rr.run(r.Context(), runID)
	if err != nil {
		return nil, err
	}
	var head struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(in.Task, &head)
	rev, err := o.deliveredRev(r.Context(), runID, run)
	if err != nil {
		return nil, err
	}
	taskRef := "task:" + runID + "/" + taskID
	task, err := withFields(in.Task, taskRef, rev)
	if err != nil {
		return nil, err
	}
	o.noteRef(taskRef, brakeman.ClassRuns, head.State, rev)
	attempts := make([]json.RawMessage, 0, len(in.Attempts))
	for _, a := range in.Attempts {
		var am map[string]json.RawMessage
		if err := json.Unmarshal(a, &am); err != nil {
			return nil, err
		}
		var ah struct {
			N      int    `json:"n"`
			State  string `json:"state"`
			Result string `json:"result"`
		}
		_ = json.Unmarshal(a, &ah)
		ref := fmt.Sprintf("attempt:%s/%s/%d", runID, taskID, ah.N)
		o.noteRef(ref, brakeman.ClassRuns, ah.State, ah.Result)
		b, _ := json.Marshal(ref)
		am["ref"] = b
		if raw, ok := am["steps"]; ok {
			steps, err := stepRefs(o, raw, runID, taskID, ah.N)
			if err != nil {
				return nil, err
			}
			am["steps"] = steps
		}
		out, err := marshalNoEscape(am)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, out)
	}
	m := map[string]json.RawMessage{"task": task}
	if m["attempts"], err = marshalNoEscape(attempts); err != nil {
		return nil, err
	}
	out, err := marshalNoEscape(m)
	return append(out, '\n'), err
}

// stepRefs is an attempt's receipts with their refs, each noted.
func stepRefs(o *observer, raw json.RawMessage, runID, taskID string, n int) (json.RawMessage, error) {
	var steps []json.RawMessage
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, err
	}
	for i, st := range steps {
		var sh struct {
			ID       string `json:"id"`
			K        int    `json:"k"`
			State    string `json:"state"`
			Subject  string `json:"subject"`
			Exit     *int   `json:"exit"`
			TimedOut bool   `json:"timedOut"`
		}
		if err := json.Unmarshal(st, &sh); err != nil {
			return nil, err
		}
		ref := fmt.Sprintf("check:%s/%s/%d/%s/%d", runID, taskID, n, sh.ID, sh.K)
		state := sh.State
		switch {
		case sh.State == ledger.CheckFinished && sh.TimedOut:
			state = "timed_out"
		case sh.State == ledger.CheckFinished && sh.Exit != nil:
			state = "exit:" + strconv.Itoa(*sh.Exit)
		}
		o.noteRef(ref, brakeman.ClassRuns, state, sh.Subject)
		var err error
		if steps[i], err = withRef(st, ref); err != nil {
			return nil, err
		}
	}
	return marshalNoEscape(steps)
}

// filterSteps is GET …/attempts/{n}/steps for a turn.
func filterSteps(r *http.Request, o *observer, body []byte) ([]byte, error) {
	n, _ := pathNumber(r.PathValue("n"))
	var in struct {
		Steps json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	steps, err := stepRefs(o, in.Steps, r.PathValue("run"), r.PathValue("task"), n)
	if err != nil {
		return nil, err
	}
	return refreshed(body, "steps", steps)
}

// filterCommand is GET /api/runs/{run}/commands/{command} for a turn.
func filterCommand(r *http.Request, o *observer, body []byte) ([]byte, error) {
	var in struct {
		Command json.RawMessage `json:"command"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	var head struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(in.Command, &head)
	ref := "command:" + r.PathValue("run") + "/" + r.PathValue("command")
	o.noteRef(ref, brakeman.ClassRuns, head.Status, "")
	c, err := withRef(in.Command, ref)
	if err != nil {
		return nil, err
	}
	return refreshed(body, "command", c)
}

// filterMessages is GET /api/runs/{run}/messages for a turn, a page at
// a time: the messages after the seq after names (none: from the
// first), in seq order, at most limit (1 to maxMessagesPage,
// defaultMessagesPage when absent) and maxObserveReply bytes, with next,
// the last seq given, while more remain. Only the page's are noted.
func filterMessages(r *http.Request, o *observer, body []byte) ([]byte, error) {
	q, err := readQuery(r, "after", "limit")
	var afterText string
	var limit int
	if err == nil {
		afterText, err = q.one("after")
	}
	if err == nil {
		limit, err = q.count("limit")
	}
	if err == nil && (limit < 0 || limit > maxMessagesPage) {
		err = fmt.Errorf("limit is 1 to %d", maxMessagesPage)
	}
	var after int64
	if err == nil && afterText != "" {
		if after, err = strconv.ParseInt(afterText, 10, 64); err != nil || after < 0 || strconv.FormatInt(after, 10) != afterText {
			err = errors.New("after is the next of the page before, a message's seq")
		}
	}
	if err != nil {
		return nil, newAPIError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if limit == 0 {
		limit = defaultMessagesPage
	}
	var in struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	runID := r.PathValue("run")
	page := []json.RawMessage{}
	// The page's frame: {"messages":[…],"next":<seq>} with a seq's
	// largest digits.
	used := len(`{"messages":[],"next":9223372036854775807}` + "\n")
	var next, last int64
	for _, m := range in.Messages {
		var head struct {
			Seq         int64      `json:"seq"`
			DeliveredAt *time.Time `json:"deliveredAt"`
			AckedAt     *time.Time `json:"ackedAt"`
		}
		if err := json.Unmarshal(m, &head); err != nil {
			return nil, err
		}
		if head.Seq <= after {
			continue
		}
		if len(page) == limit {
			next = last
			break
		}
		state := "queued"
		switch {
		case head.AckedAt != nil:
			state = "acked"
		case head.DeliveredAt != nil:
			state = "delivered"
		}
		ref := "message:" + runID + "/" + strconv.FormatInt(head.Seq, 10)
		b, err := withRef(m, ref)
		if err != nil {
			return nil, err
		}
		// A message is at most a few tens of KiB, so a page holds one.
		if len(page) > 0 && used+len(b)+1 > maxObserveReply {
			next = last
			break
		}
		o.noteRef(ref, brakeman.ClassRuns, state, "")
		page = append(page, b)
		used += len(b) + 1
		last = head.Seq
	}
	out := map[string]any{"messages": page}
	if next > 0 {
		out["next"] = next
	}
	b, err := marshalNoEscape(out)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// filterStepLog is the step log route for a turn: the log as it is, its
// SHA-256 in X-Conductor-Log-SHA256, and the receipt noted as read.
func filterStepLog(r *http.Request, o *observer, body []byte) ([]byte, error) {
	n, _ := pathNumber(r.PathValue("n"))
	k, _ := pathNumber(r.PathValue("k"))
	runID, taskID, id := r.PathValue("run"), r.PathValue("task"), r.PathValue("id")
	if ld := o.rr.s.ledger; ld != nil {
		if row, ok, err := ld.Check(r.Context(), runID, taskID, n, id, k); err == nil && ok {
			o.noteRef(fmt.Sprintf("check:%s/%s/%d/%s/%d", runID, taskID, n, id, k), brakeman.ClassLogs, checkState(row), row.Subject)
		}
	}
	return body, nil
}

// filterProjects is GET /api/dispatch/projects for a turn: the projects
// the scope names, all of them under a scope that names none.
func filterProjects(r *http.Request, o *observer, body []byte) ([]byte, error) {
	var in struct {
		Projects []json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	out := []json.RawMessage{}
	for _, p := range in.Projects {
		var head struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(p, &head); err != nil {
			return nil, err
		}
		if o.grant.projectAdmitted(head.Key) {
			out = append(out, p)
		}
	}
	return refreshed(body, "projects", out)
}

// filterTickets is GET /api/dispatch/tickets for a turn: the tickets in
// the grant, each with its ref and noted at its rev. The page's total
// counts the store's.
func filterTickets(r *http.Request, o *observer, body []byte) ([]byte, error) {
	var in struct {
		Tickets []json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	out := []json.RawMessage{}
	for _, t := range in.Tickets {
		var head struct {
			Key    string `json:"key"`
			Status string `json:"status"`
			Rev    int64  `json:"rev"`
		}
		if err := json.Unmarshal(t, &head); err != nil {
			return nil, err
		}
		ok, err := o.ticketAdmitted(r.Context(), head.Key)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ref := "ticket:" + head.Key
		o.noteRef(ref, brakeman.ClassDispatch, head.Status, strconv.FormatInt(head.Rev, 10))
		if t, err = withRef(t, ref); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return refreshed(body, "tickets", out)
}

// filterTicket is GET /api/dispatch/tickets/{key} for a turn: the ticket
// and its comments, each with its ref and noted at its rev.
func filterTicket(r *http.Request, o *observer, body []byte) ([]byte, error) {
	key := r.PathValue("key")
	var in struct {
		Ticket json.RawMessage `json:"ticket"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(in.Ticket, &m); err != nil {
		return nil, err
	}
	var head struct {
		Status string `json:"status"`
		Rev    int64  `json:"rev"`
	}
	_ = json.Unmarshal(in.Ticket, &head)
	o.noteRef("ticket:"+key, brakeman.ClassDispatch, head.Status, strconv.FormatInt(head.Rev, 10))
	if err := filterRelated(r.Context(), o, m); err != nil {
		return nil, err
	}
	if raw, ok := m["comments"]; ok {
		comments, err := commentRefs(o, key, raw)
		if err != nil {
			return nil, err
		}
		m["comments"] = comments
	}
	b, _ := json.Marshal("ticket:" + key)
	m["ref"] = b
	return refreshed(body, "ticket", m)
}

// withheldTicket is a related ticket outside the grant as a ticket's read
// shows it: its id and key, no title and no status.
type withheldTicket struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Withheld string `json:"withheld"`
}

// filterRelated holds the tickets a ticket's read names beside it (its
// parent, its children, its relations' other ends) to the grant: one
// outside it is shown by its id and key alone, withheld, as a read of it
// would be refused.
func filterRelated(ctx context.Context, o *observer, m map[string]json.RawMessage) error {
	admitted := map[string]bool{}
	held := func(raw json.RawMessage) (json.RawMessage, error) {
		if len(raw) == 0 {
			return raw, nil
		}
		var t dispatch.RelatedTicket
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		ok, seen := admitted[t.Key]
		if !seen {
			var err error
			if ok, err = o.ticketAdmitted(ctx, t.Key); err != nil {
				return nil, err
			}
			admitted[t.Key] = ok
		}
		if ok {
			return raw, nil
		}
		return marshalNoEscape(withheldTicket{ID: t.ID, Key: t.Key, Withheld: "outside the request's scope"})
	}
	if raw, ok := m["parent"]; ok && string(raw) != "null" {
		out, err := held(raw)
		if err != nil {
			return err
		}
		m["parent"] = out
	}
	if raw, ok := m["children"]; ok {
		var c map[string]json.RawMessage
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		var list []json.RawMessage
		if err := json.Unmarshal(c["tickets"], &list); err != nil {
			return err
		}
		for i := range list {
			out, err := held(list[i])
			if err != nil {
				return err
			}
			list[i] = out
		}
		if list == nil {
			list = []json.RawMessage{}
		}
		var err error
		if c["tickets"], err = marshalNoEscape(list); err != nil {
			return err
		}
		if m["children"], err = marshalNoEscape(c); err != nil {
			return err
		}
	}
	if raw, ok := m["relations"]; ok {
		var list []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return err
		}
		for _, rel := range list {
			out, err := held(rel["other"])
			if err != nil {
				return err
			}
			rel["other"] = out
		}
		if list == nil {
			list = []map[string]json.RawMessage{}
		}
		var err error
		if m["relations"], err = marshalNoEscape(list); err != nil {
			return err
		}
	}
	return nil
}

// filterHistory is GET /api/dispatch/tickets/{key}/history for a turn:
// each change with the ref of the operation that made it, the ticket
// noted as read, and each operation once.
func filterHistory(r *http.Request, o *observer, body []byte) ([]byte, error) {
	key := r.PathValue("key")
	var in struct {
		Changes []json.RawMessage `json:"changes"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	o.noteRef("ticket:"+key, brakeman.ClassDispatch, "history", "")
	noted := map[string]bool{}
	for i, c := range in.Changes {
		var head struct {
			Actor string `json:"actor"`
			OpID  string `json:"opId"`
			Op    string `json:"op"`
		}
		if err := json.Unmarshal(c, &head); err != nil {
			return nil, err
		}
		ref := "op:" + head.Actor + "/" + head.OpID
		if rf, err := brakeman.ParseRef(ref); err != nil || rf.Valid() != nil {
			continue
		}
		if !noted[ref] {
			noted[ref] = true
			o.noteRef(ref, brakeman.ClassDispatch, head.Op, "")
		}
		var err error
		if in.Changes[i], err = withRef(c, ref); err != nil {
			return nil, err
		}
	}
	if in.Changes == nil {
		in.Changes = []json.RawMessage{}
	}
	return refreshed(body, "changes", in.Changes)
}

// commentRefs is a ticket's comments with their refs, each noted.
func commentRefs(o *observer, key string, raw json.RawMessage) (json.RawMessage, error) {
	var comments []json.RawMessage
	if err := json.Unmarshal(raw, &comments); err != nil {
		return nil, err
	}
	for i, c := range comments {
		var head struct {
			ID       string    `json:"id"`
			Rev      int64     `json:"rev"`
			EditedAt time.Time `json:"editedAt"`
		}
		if err := json.Unmarshal(c, &head); err != nil {
			return nil, err
		}
		state := "posted"
		if !head.EditedAt.IsZero() {
			state = "edited"
		}
		ref := "comment:" + key + "/" + head.ID
		o.noteRef(ref, brakeman.ClassDispatch, state, strconv.FormatInt(head.Rev, 10))
		var err error
		if comments[i], err = withRef(c, ref); err != nil {
			return nil, err
		}
	}
	if comments == nil {
		comments = []json.RawMessage{}
	}
	return marshalNoEscape(comments)
}

// filterComments is GET /api/dispatch/tickets/{key}/comments for a turn.
func filterComments(r *http.Request, o *observer, body []byte) ([]byte, error) {
	var in struct {
		Comments json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	comments, err := commentRefs(o, r.PathValue("key"), in.Comments)
	if err != nil {
		return nil, err
	}
	return refreshed(body, "comments", comments)
}

// sessionAdmitted is the session id's scope item and whether the grant
// admits it.
func (o *observer) sessionAdmitted(ctx context.Context, id string) (bool, error) {
	f, err := o.rr.lookupSessionPart(ctx, brakeman.Ref{Kind: brakeman.RefSession, Session: id})
	if err != nil {
		return false, err
	}
	return o.grant.admits(f.item), nil
}

// sessionState is a session's state as a ref's resolution reads it: its
// status, and its attention's state after a colon when it has one.
func sessionState(info session.Info) string {
	state := string(info.Status)
	if info.Attention.State != session.AttentionNone {
		state += ":" + string(info.Attention.State)
	}
	return state
}

// filterSessions is GET /api/sessions for a turn: the sessions in its
// grant, each with its ref, noted with its open question.
func filterSessions(r *http.Request, o *observer, body []byte) ([]byte, error) {
	var in struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	out := []json.RawMessage{}
	for _, raw := range in.Sessions {
		var info session.Info
		if err := json.Unmarshal(raw, &info); err != nil {
			return nil, err
		}
		ok, err := o.sessionAdmitted(r.Context(), info.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ref := "session:" + info.ID
		o.noteRef(ref, brakeman.ClassSessions, sessionState(info), "")
		if a := info.Attention; a.State == session.AttentionNeedsInput && a.ID != "" {
			o.noteRef("question:"+info.ID+"/"+a.ID, brakeman.ClassSessions, questionOpen, "")
		}
		if raw, err = withRef(raw, ref); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return refreshed(body, "sessions", out)
}

// filterTouched is GET /api/touched for a turn: the sessions in its grant
// and their touched files, each with its ref and noted.
func filterTouched(r *http.Request, o *observer, body []byte) ([]byte, error) {
	var in touchedReply
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	type fileRef struct {
		Ref string `json:"ref"`
		proto.TouchedFile
	}
	type sessionOut struct {
		Ref string `json:"ref"`
		touchedSession
		Touched []fileRef `json:"touched"`
	}
	out := []sessionOut{}
	for _, ts := range in.Sessions {
		ok, err := o.sessionAdmitted(r.Context(), ts.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		so := sessionOut{Ref: "session:" + ts.ID, touchedSession: ts, Touched: []fileRef{}}
		for _, f := range ts.Touched {
			ref := brakeman.Ref{Kind: brakeman.RefFile, Session: ts.ID, Path: f.Path}
			if ref.Valid() != nil {
				continue
			}
			o.note(ref, brakeman.ClassTouched, f.Op, strconv.Itoa(f.Count))
			so.Touched = append(so.Touched, fileRef{Ref: ref.String(), TouchedFile: f})
		}
		out = append(out, so)
	}
	return refreshed(body, "sessions", out)
}

// ---- Brakeman's own reads

// handleBrakemanStatus answers GET /api/brakeman/status?run= or
// ?session=: each task's status, or the session's, as Conductor computes
// it, never as the agent infers it.
func (s *Server) handleBrakemanStatus(w http.ResponseWriter, r *http.Request, o *observer) {
	q, err := readQuery(r, "run", "session")
	var runID, sessionID string
	if err == nil {
		runID, err = q.one("run")
	}
	if err == nil {
		sessionID, err = q.one("session")
	}
	if err == nil && (runID == "") == (sessionID == "") {
		err = errors.New("name a run or a session: ?run= or ?session=")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx := r.Context()
	if sessionID != "" {
		if !o.sees(brakeman.ClassSessions) {
			writeError(w, http.StatusForbidden, "not_visible", "Brakeman may not see sessions")
			return
		}
		drv, ok := s.registry.Get(sessionID)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		if ok, err := o.sessionAdmitted(ctx, sessionID); err != nil || !ok {
			if err != nil {
				s.observeError(w, err)
				return
			}
			writeError(w, http.StatusForbidden, "outside_scope", "outside the request's scope")
			return
		}
		info := drv.Info()
		o.noteRef("session:"+sessionID, brakeman.ClassSessions, sessionState(info), "")
		writeJSON(w, http.StatusOK, map[string]any{"session": "session:" + sessionID, "status": s.sessionStatus(info, o.now)})
		return
	}
	if !o.sees(brakeman.ClassRuns) {
		writeError(w, http.StatusForbidden, "not_visible", "Brakeman may not see runs")
		return
	}
	if !crew.ValidRunID(runID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "run is a run's id")
		return
	}
	run, ok, err := o.runAdmitted(ctx, runID)
	switch {
	case err != nil:
		s.observeError(w, err)
		return
	case !run.found:
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	case !ok:
		writeError(w, http.StatusForbidden, "outside_scope", "outside the request's scope")
		return
	}
	// A managed run's tasks are read between two reads of its revision
	// that agree (every transition moves it) while the engine held it
	// settled at one revision throughout (it changes its run before the
	// transaction commits: a status computed from its run then may hold
	// what the ledger's revision does not yet), read again, the engine's
	// run with them, when either moved between, so the revision answered
	// is the one they were read at, or none. The engine's hold is read
	// before the ledger's revision and after it, as a shared read's are
	// (takeRevs, deliveredRev).
	state, rev := run.state, run.rev
	var tasks []taskStatus
	var members []memberStatus
	for try := range refRevTries {
		var before, after string
		engBefore := s.engineHoldOf(runID)
		if before, state, err = s.freshRun(ctx, runID, run); err == nil {
			tasks, members, err = s.runStatus(ctx, o, s.restatus(run, runID, state), runID)
		}
		if err == nil {
			after, state, err = s.freshRun(ctx, runID, run)
		}
		if err != nil {
			s.observeError(w, err)
			return
		}
		settled := !run.managed || s.ledger == nil || settledThrough(engBefore, s.engineHoldOf(runID))
		if before == after && settled {
			rev = after
			break
		}
		if try == refRevTries-1 {
			rev = ""
		}
	}
	o.noteRef("run:"+runID, brakeman.ClassRuns, state, rev)
	for _, t := range tasks {
		o.noteRef(t.Ref, brakeman.ClassRuns, t.State, rev)
	}
	out := map[string]any{"run": "run:" + runID, "state": state, "revision": rev, "tasks": tasks, "members": members}
	if fitStatusWords(tasks, members) {
		out["wordsCut"] = true
	}
	writeJSON(w, http.StatusOK, out)
}

// maxStatusWords bounds a status line's words, as encoded.
const maxStatusWords = 1 << 10

// fitStatusWords holds a run's status read to maxObserveReply: each line's
// words (a record's, an attempt's error among them) cut to at most
// maxStatusWords bytes as encoded, and to an even share of what the
// lines leave when they need less, every ref, state and proof whole. It
// reports whether it cut any. A run has at most 128 tasks and 12
// members, whose lines without their words come to well under the bound.
func fitStatusWords(tasks []taskStatus, members []memberStatus) bool {
	n := len(tasks) + len(members)
	if n == 0 {
		return false
	}
	bare := 8 << 10 // the answer's own fields, with room
	for _, t := range tasks {
		t.Words = ""
		bare += len(mustJSON(t)) + 1
	}
	for _, m := range members {
		m.Words = ""
		bare += len(mustJSON(m)) + 1
	}
	per := min(maxStatusWords, max(0, maxObserveReply-bare)/n)
	cut := false
	fit := func(c *computed) {
		if w := fitJSON(c.Words, per); w != c.Words {
			c.Words, cut = w, true
		}
	}
	for i := range tasks {
		fit(&tasks[i].computed)
	}
	for i := range members {
		fit(&members[i].computed)
	}
	return cut
}

// fitJSON is the longest prefix of s, cut at a character, whose JSON
// string, quotes and escapes counted, is at most n bytes beside its
// quotes.
func fitJSON(s string, n int) string {
	size := func(t string) int { return len(mustJSON(t)) - 2 }
	if size(s) <= n {
		return s
	}
	target := min(len(s), n)
	for target > 0 {
		c := cutBytes(s, target)
		got := size(c)
		if got <= n || c == "" {
			return c
		}
		// Each byte encodes to at most six: shrink by what is over.
		target = len(c) - max(1, (got-n+5)/6)
	}
	return ""
}

// computed is a task's or a session's status as Conductor computes it:
// the status, the proof it rests on (a receipt's ref, or "none"), and the
// record's words.
type computed struct {
	Status string `json:"status"`
	Proof  string `json:"proof"`
	Words  string `json:"words,omitempty"`
	// Question is the open question's ref (waiting_on_person); Commit a
	// succeeded task's result.
	Question string `json:"question,omitempty"`
	Commit   string `json:"commit,omitempty"`
	// QuietSeconds is how long a server session's terminal has written
	// nothing; Quiet "unknown" for a hosted session, whose output is its
	// host's.
	QuietSeconds int    `json:"quietSeconds,omitempty"`
	Quiet        string `json:"quiet,omitempty"`
}

// The statuses Conductor computes.
const (
	statusFailed          = "failed"
	statusBlocked         = "blocked"
	statusWaitingOnPerson = "waiting_on_person"
	statusQuiet           = "quiet"
	statusRunning         = "running"
	statusPending         = "pending"
	statusSucceeded       = "succeeded"
	statusStopped         = "stopped"
	statusInterrupted     = "interrupted"
	proofNone             = "none"
)

// taskStatus is a task's status line in a status read.
type taskStatus struct {
	Ref   string `json:"ref"`
	State string `json:"state"`
	computed
}

// memberStatus is an unmanaged run's member's status line.
type memberStatus struct {
	Member  string `json:"member"`
	Session string `json:"session,omitempty"`
	computed
}

// outputAtSeam, when a test sets it, answers a session's last output in
// place of Local.LastOutputAt.
var outputAtSeam atomic.Pointer[func(id string) (time.Time, bool)]

// lastOutput is when the server session id last wrote output, false for
// one the server does not hold the output of.
func (s *Server) lastOutput(id string) (time.Time, bool) {
	if f := outputAtSeam.Load(); f != nil {
		if at, ok := (*f)(id); ok {
			return at, true
		}
	}
	l, ok := s.lookupLocal(id)
	if !ok {
		return time.Time{}, false
	}
	return l.LastOutputAt(), true
}

// sessionStatus is a session's status: stopped once it ended, waiting on
// a person while it asks, quiet when a server session's terminal has
// written nothing for quietAfter (since it began, when it has written
// nothing yet; with no proof of anything), running otherwise; a hosted
// session's quiet time is unknown, never quiet.
func (s *Server) sessionStatus(info session.Info, now time.Time) computed {
	switch {
	case info.Status.Ended():
		words := string(info.Status)
		if info.ExitCode != nil {
			words += fmt.Sprintf(" (exit %d)", *info.ExitCode)
		}
		return computed{Status: statusStopped, Proof: proofNone, Words: words}
	case info.Attention.State == session.AttentionNeedsInput:
		c := computed{Status: statusWaitingOnPerson, Proof: proofNone, Words: info.Attention.Message}
		if info.Attention.ID != "" {
			c.Question = "question:" + info.ID + "/" + info.Attention.ID
			c.Proof = c.Question
		}
		return c
	case info.Kind == session.KindHosted:
		return computed{Status: statusRunning, Proof: proofNone, Quiet: "unknown"}
	}
	if at, ok := s.lastOutput(info.ID); ok {
		if at.IsZero() {
			// Nothing written yet: silent since the session began.
			at = info.CreatedAt
		}
		if quiet := now.Sub(at); !at.IsZero() && quiet >= quietAfter {
			return computed{Status: statusQuiet, Proof: proofNone, QuietSeconds: int(quiet.Seconds()), Words: "the terminal has written nothing since " + at.UTC().Format(time.RFC3339)}
		}
	}
	return computed{Status: statusRunning, Proof: proofNone}
}

// freshRun is a managed run's revision and state as the ledger holds
// them now, one row's; any other run's as resolved.
func (s *Server) freshRun(ctx context.Context, id string, run *refRun) (rev, state string, err error) {
	if !run.managed || s.ledger == nil {
		return run.rev, run.state, nil
	}
	row, ok, err := s.ledger.Run(ctx, id)
	if err != nil || !ok {
		return "", "", wrapNil(err, "the ledger's run %s", id)
	}
	return strconv.FormatInt(row.Revision, 10), row.State, nil
}

// restatus is run with the engine's run read now, never the one its
// resolution cached (a status read again after a transition computes
// from the engine's run as it is), and, for a managed run, the ledger's
// state read with its revision.
func (s *Server) restatus(run *refRun, id, state string) *refRun {
	cur := *run
	if r, ok := s.runs.Get(id); ok {
		cur.run, cur.held = r, true
		if !cur.managed {
			cur.state = r.State
		}
	}
	if cur.managed {
		cur.state = state
	}
	return &cur
}

// runStatus is a run's tasks' statuses (a managed run's) and its members'
// (any run's).
func (s *Server) runStatus(ctx context.Context, o *observer, run *refRun, runID string) ([]taskStatus, []memberStatus, error) {
	tasks := []taskStatus{}
	members := []memberStatus{}
	if run.held {
		for _, m := range run.run.Members {
			ms := memberStatus{Member: m.Name, Session: m.SessionID}
			if drv, ok := s.registry.Get(m.SessionID); ok && m.SessionID != "" {
				ms.computed = s.sessionStatus(drv.Info(), o.now)
			} else {
				ms.computed = computed{Status: statusStopped, Proof: proofNone, Words: "its session is gone"}
			}
			members = append(members, ms)
		}
	}
	if !run.managed {
		return tasks, members, nil
	}
	ld := s.ledger
	rows, err := ld.Tasks(ctx, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("the ledger's tasks of %s: %w", runID, err)
	}
	attempts, err := ld.Attempts(ctx, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("the ledger's attempts of %s: %w", runID, err)
	}
	held := map[string]crew.TaskState{}
	if run.held {
		for _, t := range run.run.Tasks {
			held[t.ID] = t
		}
	}
	for _, t := range rows {
		c, err := s.taskComputed(ctx, o, run, runID, t, held[t.ID], attempts)
		if err != nil {
			return nil, nil, err
		}
		ref := "task:" + runID + "/" + t.ID
		tasks = append(tasks, taskStatus{Ref: ref, State: t.State, computed: c})
	}
	return tasks, members, nil
}

// taskComputed is one managed task's status, from the ledger's rows and
// the engine's task while it holds the run.
func (s *Server) taskComputed(ctx context.Context, o *observer, run *refRun, runID string, t ledger.TaskRow, held crew.TaskState, attempts []ledger.AttemptRow) (computed, error) {
	var last *ledger.AttemptRow
	for i := range attempts {
		if a := attempts[i]; a.TaskID == t.ID && (last == nil || a.N > last.N) {
			last = &attempts[i]
		}
	}
	switch t.State {
	case ledger.TaskSucceeded:
		c := computed{Status: statusSucceeded, Proof: proofNone, Commit: t.ResultCommit}
		if t.ResultAttempt > 0 {
			c.Proof = fmt.Sprintf("attempt:%s/%s/%d", runID, t.ID, t.ResultAttempt)
		}
		return c, nil
	case ledger.TaskFailed:
		return s.failedProof(ctx, runID, t, last)
	case ledger.TaskCancelled:
		return computed{Status: statusStopped, Proof: proofNone, Words: cmpOr(t.Reason, "cancelled")}, nil
	}
	switch run.state {
	case crew.RunStopped:
		return computed{Status: statusStopped, Proof: proofNone, Words: "the run was stopped"}, nil
	case crew.RunInterrupted:
		return computed{Status: statusInterrupted, Proof: proofNone, Words: "the server stopped while the run was running"}, nil
	}
	if held.Blocker != nil {
		return computed{Status: statusBlocked, Proof: proofNone, Words: cmpOr(held.Blocker.Message, held.Blocker.Reason)}, nil
	}
	switch t.State {
	case ledger.TaskBlocked:
		return computed{Status: statusBlocked, Proof: proofNone, Words: "it waits for the results of the tasks it needs"}, nil
	case ledger.TaskReady:
		return computed{Status: statusPending, Proof: proofNone, Words: "ready, no attempt yet"}, nil
	}
	if last != nil {
		switch last.State {
		case ledger.AttemptInterrupted:
			return computed{Status: statusInterrupted, Proof: fmt.Sprintf("attempt:%s/%s/%d", runID, t.ID, last.N), Words: cmpOr(last.Error, "the attempt was interrupted")}, nil
		}
		if drv, ok := s.registry.Get(last.SessionID); ok && last.SessionID != "" {
			c := s.sessionStatus(drv.Info(), o.now)
			if c.Status == statusStopped {
				// The session ended and the engine has not closed its
				// attempt yet: still running as the record says.
				c = computed{Status: statusRunning, Proof: proofNone}
			}
			return c, nil
		}
	}
	return computed{Status: statusRunning, Proof: proofNone}, nil
}

// cmpOr is the first of ss that is not empty.
func cmpOr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// failedProof is a failed task's status with the receipt that proves it:
// the last attempt's last failing check (a non-zero exit, a timeout, a
// kill), else the attempt and its words (a refused result), else the
// task's own words (attempts spent).
func (s *Server) failedProof(ctx context.Context, runID string, t ledger.TaskRow, last *ledger.AttemptRow) (computed, error) {
	c := computed{Status: statusFailed, Proof: proofNone, Words: cmpOr(t.Reason, "failed")}
	if last == nil {
		return c, nil
	}
	rows, err := s.ledger.Checks(ctx, runID, t.ID, last.N, false)
	if err != nil {
		return computed{}, fmt.Errorf("the ledger's checks of %s/%s/%d: %w", runID, t.ID, last.N, err)
	}
	var proof *ledger.CheckRow
	for i := range rows {
		row := rows[i]
		if row.State != ledger.CheckFinished || (row.Exit == 0 && !row.TimedOut && !row.Killed && row.Signal == "") {
			continue
		}
		if proof == nil || row.StartedAt.After(proof.StartedAt) || (row.StartedAt.Equal(proof.StartedAt) && row.K > proof.K) {
			proof = &rows[i]
		}
	}
	if proof != nil {
		c.Proof = fmt.Sprintf("check:%s/%s/%d/%s/%d", runID, t.ID, last.N, proof.ID, proof.K)
		c.Words = fmt.Sprintf("%s: %s", proof.ID, checkState(*proof))
		return c, nil
	}
	c.Proof = fmt.Sprintf("attempt:%s/%s/%d", runID, t.ID, last.N)
	c.Words = cmpOr(last.Error, last.Stage, t.Reason, "the attempt failed")
	if t.Attempts > 0 && t.Reason == "" && last.Error == "" {
		c.Words = fmt.Sprintf("its %d attempts are spent", t.Attempts)
	}
	return c, nil
}

// handleBrakemanScreen answers GET /api/brakeman/screen?session=&lines=:
// a server session's screen's last lines (at most maxScreenLines), only
// with terminals visible (403 not_visible); 409 unavailable for a hosted
// session, whose screen the server does not hold.
func (s *Server) handleBrakemanScreen(w http.ResponseWriter, r *http.Request, o *observer) {
	q, err := readQuery(r, "session", "lines")
	var id string
	var lines int
	if err == nil {
		id, err = q.one("session")
	}
	if err == nil {
		lines, err = q.count("lines")
	}
	if err == nil && (id == "" || lines < 0 || lines > maxScreenLines) {
		err = fmt.Errorf("name a session, and at most %d lines", maxScreenLines)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if lines == 0 {
		lines = defaultScreenLines
	}
	if !o.sees(brakeman.ClassTerminals) {
		writeError(w, http.StatusForbidden, "not_visible", "Brakeman may not read a session's screen: terminals are off in Settings")
		return
	}
	drv, ok := s.registry.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if ok, err := o.sessionAdmitted(r.Context(), id); err != nil || !ok {
		if err != nil {
			s.observeError(w, err)
			return
		}
		writeError(w, http.StatusForbidden, "outside_scope", "outside the request's scope")
		return
	}
	info := drv.Info()
	l, local := drv.(*session.Local)
	if info.Kind == session.KindHosted || !local {
		writeError(w, http.StatusConflict, "unavailable", "a hosted session's screen is its host's: the server does not hold it")
		return
	}
	text := l.TailLines(lines)
	// Held to the reply's bound: the oldest lines go first.
	for size := screenSize(text); size > maxObserveReply-1024 && len(text) > 0; size = screenSize(text) {
		text = text[1:]
	}
	if text == nil {
		text = []string{}
	}
	o.noteRef("session:"+id, brakeman.ClassTerminals, sessionState(info), "")
	writeJSON(w, http.StatusOK, map[string]any{"session": "session:" + id, "lines": text})
}

// screenSize is the lines' size as JSON.
func screenSize(lines []string) int {
	b, _ := json.Marshal(lines)
	return len(b)
}

// handleBrakemanBriefs answers GET /api/brakeman/briefs?q=&after=: the
// earlier briefs, newest first, at most maxBriefsPage a page, each with
// its title, summary and items' refs. For a turn, a brief whose
// footprint passes its grant is its projection: its id, kind, time and
// the refs the grant admits now, withheld, none of its prose.
func (s *Server) handleBrakemanBriefs(w http.ResponseWriter, r *http.Request, o *observer) {
	q, err := readQuery(r, "q", "after")
	var words, after string
	if err == nil {
		words, err = q.one("q")
	}
	if err == nil {
		after, err = q.one("after")
	}
	if err == nil && after != "" && !brakemanIDPattern(after) {
		err = errors.New("after is a request's id (br-<n>)")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	d := s.brakeman
	type briefLine struct {
		ID       string    `json:"id"`
		Kind     string    `json:"kind"`
		At       time.Time `json:"at"`
		Title    string    `json:"title,omitempty"`
		Summary  string    `json:"summary,omitempty"`
		Refs     []string  `json:"refs"`
		Withheld string    `json:"withheld,omitempty"`
	}
	out := []briefLine{}
	next := ""
	used := 64
	words = strings.ToLower(words)
	cursor := after
	if cursor == "" && o.tp != nil {
		cursor = o.tp.req.ID
	}
	for done := false; !done; {
		page, more, err := d.st.List(cursor, brakeman.MaxPage)
		if err != nil {
			s.brakemanError(w, err)
			return
		}
		for _, req := range page {
			if !req.HasBrief || (o.tp != nil && req.ID == o.tp.req.ID) {
				continue
			}
			if len(out) == maxBriefsPage {
				next, done = out[len(out)-1].ID, true
				break
			}
			whole := o.tp == nil || o.briefWithin(req)
			b, _, err := d.st.Brief(req.ID)
			if errors.Is(err, brakeman.ErrNotFound) {
				continue
			}
			if err != nil {
				s.brakemanError(w, err)
				return
			}
			line := briefLine{ID: req.ID, Kind: req.Kind, At: req.BriefAt, Refs: []string{}}
			if whole {
				if words != "" && !strings.Contains(strings.ToLower(b.Title+"\n"+b.Summary), words) {
					continue
				}
				line.Title, line.Summary = b.Title, b.Summary
				o.noteFootprint(req)
			} else {
				if words != "" {
					continue // a withheld brief has no prose to search
				}
				line.Withheld = "made under a wider scope or visibility"
			}
			seen := map[brakeman.Ref]bool{}
			for _, it := range b.Items {
				for _, ref := range it.Refs() {
					if seen[ref] || len(line.Refs) == maxBriefsPageRefs {
						continue
					}
					seen[ref] = true
					if !whole {
						if _, why, err := o.rr.resolve(r.Context(), ref); err != nil || why != "" {
							continue
						}
					}
					line.Refs = append(line.Refs, ref.String())
				}
			}
			size := len(mustJSON(line)) + 1
			if used+size > maxObserveReply {
				next, done = cmpOr(lastID(out), cursor), true
				break
			}
			used += size
			out = append(out, line)
		}
		if more == "" {
			break
		}
		cursor = more
	}
	writeJSON(w, http.StatusOK, map[string]any{"briefs": out, "next": next})
}

// lastID is the id of the last brief line, "" for none.
func lastID[T any](list []T) string {
	if len(list) == 0 {
		return ""
	}
	var head struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(mustJSON(list[len(list)-1])), &head)
	return head.ID
}

// briefWithin reports whether an earlier brief's footprint is within the
// turn's grant: every class its reads used visible now, and the scope it
// ran under covered (docs/plans/brakeman.md, An earlier brief is derived
// content). A brief with no footprint kept counts every class its
// snapshot had on.
func (o *observer) briefWithin(req brakeman.Request) bool {
	classes := brakeman.Classes
	if req.Footprint != nil {
		classes = req.Footprint.Classes
	}
	for _, c := range classes {
		if (req.Footprint != nil || req.Config.Visibility.Has(c)) && !o.grant.sees(c) {
			return false
		}
	}
	return o.grant.covers(req.Config.Scope)
}

// noteFootprint notes an earlier brief given whole as read in every class
// its footprint used: derived content carries its sources' grant into
// the turn that reads it.
func (o *observer) noteFootprint(req brakeman.Request) {
	classes := []string{}
	if req.Footprint != nil {
		classes = req.Footprint.Classes
	}
	for _, c := range classes {
		o.note(brakeman.Ref{Kind: brakeman.RefBrief, Request: req.ID}, c, req.State, "")
	}
	if len(classes) == 0 {
		o.note(brakeman.Ref{Kind: brakeman.RefBrief, Request: req.ID}, brakeman.ClassRuns, req.State, "")
	}
}

// ---- the shared reads' table

// sharedRead is a shared read a turn may make: its pattern, its spec and
// the bound its route keeps of its reply (0 for a route that states
// none), which the guide's observe_<what> entry lists. routeBrakeman's
// caller registers each through reader.
type sharedRead struct {
	pattern string
	spec    readSpec
	reply   int
}

// brakemanReads are the shared reads the principal brakeman takes, each
// with what it reads: the runs' (runs, a run with its tasks, a task's
// attempts and receipts, a command's receipt, a run's messages), a
// check's log, Dispatch's (projects, tickets, a ticket, its history and
// its comments), the sessions and the touched files. Every other route
// refuses the principal.
var brakemanReads = []sharedRead{
	{"GET /api/runs", readSpec{class: brakeman.ClassRuns, filter: filterRuns}, 0},
	{"GET /api/runs/{run}", readSpec{class: brakeman.ClassRuns, admit: admitRunPath, filter: filterRun}, 0},
	{"GET /api/runs/{run}/tasks/{task}", readSpec{class: brakeman.ClassRuns, admit: admitRunPath, filter: filterTask}, 0},
	{"GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps", readSpec{class: brakeman.ClassRuns, admit: admitRunPath, filter: filterSteps}, 0},
	{"GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps/{id}/{k}/log", readSpec{class: brakeman.ClassLogs, admit: admitRunPath, filter: filterStepLog}, ledger.MaxLog},
	{"GET /api/runs/{run}/commands/{command}", readSpec{class: brakeman.ClassRuns, admit: admitRunPath, filter: filterCommand}, 0},
	{"GET /api/runs/{run}/messages", readSpec{class: brakeman.ClassRuns, admit: admitRunPath, filter: filterMessages}, 0},
	{"GET /api/dispatch/projects", readSpec{class: brakeman.ClassDispatch, filter: filterProjects}, maxProjectsPage},
	{"GET /api/dispatch/tickets", readSpec{class: brakeman.ClassDispatch, filter: filterTickets}, 0},
	{"GET /api/dispatch/tickets/{key}", readSpec{class: brakeman.ClassDispatch, admit: admitTicketPath, filter: filterTicket}, MaxTicketReply},
	{"GET /api/dispatch/tickets/{key}/history", readSpec{class: brakeman.ClassDispatch, admit: admitTicketPath, filter: filterHistory}, 0},
	{"GET /api/dispatch/tickets/{key}/comments", readSpec{class: brakeman.ClassDispatch, admit: admitTicketPath, filter: filterComments}, MaxTicketReply},
	{"GET /api/sessions", readSpec{class: brakeman.ClassSessions, filter: filterSessions}, 0},
	{"GET /api/touched", readSpec{class: brakeman.ClassTouched, filter: filterTouched}, 0},
}

// sharedReadReply is the bound the shared read of pattern keeps of its
// reply, 0 for none stated or no such read.
func sharedReadReply(pattern string) int {
	i := slices.IndexFunc(brakemanReads, func(sr sharedRead) bool { return sr.pattern == pattern })
	if i < 0 {
		return 0
	}
	return brakemanReads[i].reply
}

// sharedReadSpec is the spec of a shared read's pattern, ok false for a
// route the principal brakeman does not take.
func sharedReadSpec(pattern string) (readSpec, bool) {
	i := slices.IndexFunc(brakemanReads, func(sr sharedRead) bool { return sr.pattern == pattern })
	if i < 0 {
		return readSpec{}, false
	}
	return brakemanReads[i].spec, true
}

// readable is h behind reader when pattern is a shared read, else behind
// requireAdmin: the one change the shared routes' registration makes.
func (s *Server) readable(pattern string, h http.HandlerFunc) http.HandlerFunc {
	if spec, ok := sharedReadSpec(pattern); ok {
		return s.reader(spec, h)
	}
	return s.requireAdmin(h)
}
