package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// The decision router (Program item C5): the seam of ext.DecisionRouter
// over the questions server sessions show. It watches the sessions'
// attention itself, not their activity, since a question can stop waiting
// with nothing recorded (withdrawn by the screen, the terminal quiet
// after). The change hooks every attention change already calls
// (localChange, the hosted hub's OnChange, the registry's OnRemove) mark
// the session's id dirty, never waiting; a worker takes the id out of the
// set, reads the session's Info once (what it shows and what it keeps, one
// snapshot) and judges from it alone:
//
//   - a question it has not seen is routed once (Route, through the guard):
//     an empty route records nothing, the free default; a route with
//     destinations records one status entry in the session's activity; a
//     route with an answer applies it once, through the question's claim
//     (submitTo: AnswerAs for a choice, Submit with Question for a line), a
//     person's earlier answer winning;
//   - a routed question the session neither shows nor keeps, or shows
//     answered in the terminal, is resolved, and the router is told so
//     (Resolved) exactly once, in order, from a list that drops nothing.
//
// A question that mirrors a durable decision (Attention.Decision: a
// managed run's, Program item DR1) is never routed as a session question:
// the decision's record is routed instead (record). The ledger is the
// truth and only its owner tells, and the server keeps no account of what
// a router has heard: a decision open is told (Route) when it opens, as
// often as needed, by a pass over what the ledger holds open (pass: at
// this server's taking the ledger, at the provider's start, and after a
// tell that failed or was not made); its end is told (Resolved) when that
// can be done at once, never owed; and its state can always be read
// (ext.Host.Decision). The calls about decisions run off every path, each
// on a goroutine of its own begun only once it holds one of the provider's
// call slots (the guard's extInFlight, which every call of the router
// takes, Route and Resolved alike, and keeps until it returns): a provider
// that hangs holds slots, up to the bound, and nothing waits for it past
// its deadline; a tell with no slot free is not made and owed. Under the free build's
// policy (local/1) the router's destinations become the record's routed
// history row and an answer it holds is offered as a response under the
// actor user:<subject> with the router's authority and no grant, which the
// policy refuses (not_approver) and records: the operator alone answers.
//
// A question first seen answered in the terminal is never routed. Past
// maxUnresolved routed and untold questions a new one is not routed (the
// people watching answer it) and is held skipped. A hosted session is
// routed as a server session is when its host said answerAs (it sends what
// its session keeps, and takes answer_as), and only by the owner's own
// server, never a switchyard: an older host's questions are routed by no
// one (answerHosted).

const (
	// routerWorkers bounds the sessions read at once.
	routerWorkers = 16
	// routedKept bounds the questions the router keeps per session, the
	// oldest no longer waiting forgotten first; one not yet forgettable
	// (outstanding, or shown or kept still: at most three) is never
	// forgotten. A resolution being told is held by the list of
	// resolutions, not here.
	routedKept = 64
	// maxUnresolved bounds the questions routed and not yet told Resolved,
	// server-wide.
	maxUnresolved = 1024
	// routerSkipLogEvery is how often the skips past the bound are logged.
	routerSkipLogEvery = time.Minute
)

// routeState is where a question the router saw is.
type routeState uint8

const (
	// routeOutstanding: routed, and still waiting.
	routeOutstanding routeState = iota + 1
	// routeResolving: no longer waiting, its Resolved queued or under way.
	routeResolving
	// routeResolved: told Resolved, or never routed because it was first
	// seen answered in the terminal.
	routeResolved
	// routeSkipped: never routed (the bound, a router not yet started, or
	// a call the guard never made, its slots busy) and never told.
	routeSkipped
)

// routedSession is what the router keeps of one session.
type routedSession struct {
	ids   map[string]routeState
	order []string // ids in the order first seen
	// shown and kept are what the last read showed and kept: an id still
	// among them is not forgotten. ref names the session as it last read.
	shown string
	kept  []string
	ref   ext.SessionRef
}

// pendingResolution is a Resolved to tell, and the question it is for.
type pendingResolution struct {
	r    ext.Resolution
	sess *routedSession // the session's record when queued; it may be forgotten since
}

// decisionRouter is the server's router seam.
type decisionRouter struct {
	s *Server
	// wait, when a test sets it, bounds a Route call before the guard's
	// ext.RouteWait does; zero in the server.
	wait time.Duration
	// bound is maxUnresolved, the sessions' questions'; a test lowers it.
	bound int

	mu       sync.Mutex
	dirty    map[string]bool // the ids in queue
	queue    []string        // dirty ids, oldest first, each once
	working  map[string]bool // ids a worker reads now
	workers  int
	sessions map[string]*routedSession
	// The durable decisions ("dr-<n>"). ledger is the ledger once this
	// server owns it (owned); begun says the router's provider answers
	// (begin): only with both are decisions read and told. opened are the
	// open decisions this server has told the router of (or tells now)
	// since it took the ledger, so that a change of an open record (its
	// routed row, a comment) tells nothing again: it holds open decisions
	// only, never what a router heard of an end. passing says a pass
	// runs, again that one was asked meanwhile; waiting that one is owed
	// after passWait (owe).
	ledger   *ledger.Ledger
	begun    bool
	opened   map[string]bool
	passing  bool
	again    bool
	waiting  bool
	passWait time.Duration
	// untold counts the sessions' questions routed and not yet told
	// Resolved, under bound.
	untold  int
	told    []pendingResolution // first in, first out
	telling bool
	skips   int // skipped past the bound since the last log
	skipLog time.Time
}

// newDecisionRouter is the router of s; nothing runs until a session
// changes.
func newDecisionRouter(s *Server) *decisionRouter {
	return &decisionRouter{s: s, bound: maxUnresolved, dirty: map[string]bool{}, working: map[string]bool{}, sessions: map[string]*routedSession{},
		opened: map[string]bool{}}
}

// routerSeam, when a test sets it, is told the router's steps for a
// session: "read" before it reads the session, "judge" after, before it
// judges what it read; "routed" once a question's route is handled (the
// route the guard answered, its answer's apply begun when it has one);
// "applied" once an answer was applied or refused (err). It may hold the
// router there; "done" as the read ends. Nil outside tests.
var routerSeam atomic.Pointer[func(step, session, question string, rt ext.Route, err error)]

func seam(step, sess, question string, rt ext.Route, err error) {
	if f := routerSeam.Load(); f != nil {
		(*f)(step, sess, question, rt, err)
	}
}

// mark notes that the session id changed. It never waits on anything but
// the router's own lock, and an id is in the set at most once however often
// it changes: nothing is ever dropped from it.
func (r *decisionRouter) mark(id string) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.dirty[id] {
		r.dirty[id] = true
		r.queue = append(r.queue, id)
	}
	// A worker not reading a session is about to take one: a new one is
	// begun only for ids none of them will take.
	if r.workers < routerWorkers && r.workers-len(r.working) < len(r.queue) {
		r.workers++
		r.s.track(r.work)
	}
}

// work reads dirty sessions until none is left that no other worker reads.
func (r *decisionRouter) work() {
	for {
		r.mu.Lock()
		id, ok := r.nextLocked()
		if !ok {
			r.workers--
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		r.read(id)
		r.mu.Lock()
		delete(r.working, id)
		r.mu.Unlock()
	}
}

// nextLocked takes out of the set the oldest dirty id no worker reads now
// (a change made while one reads it marks it again, read once that worker
// is done), or none, once the server's extensions have stopped. The caller
// holds r.mu.
func (r *decisionRouter) nextLocked() (string, bool) {
	if r.s.extCtx.Err() != nil {
		return "", false
	}
	for i, id := range r.queue {
		if r.working[id] {
			continue
		}
		if i == 0 {
			r.queue[0] = ""
			r.queue = r.queue[1:]
		} else {
			r.queue = slices.Delete(r.queue, i, i+1)
		}
		if len(r.queue) == 0 {
			r.queue = nil
		}
		delete(r.dirty, id)
		r.working[id] = true
		return id, true
	}
	return "", false
}

// read reads the session id once and judges from that one snapshot: a
// server session's questions are resolved and routed; a session gone from
// the registry has its routed questions resolved as ended and is
// forgotten. A hosted session's new questions are routed only while the
// server may answer it (routesHosted); those already routed are resolved
// either way, so that a host back without answerAs still has them told
// Resolved and gives back their place under the bound.
func (r *decisionRouter) read(id string) {
	if n, ok := ledger.ParseDecisionID(id); ok {
		// A durable decision's record (record), never a session's id.
		r.readRecord(id, n)
		return
	}
	seam("read", id, "", ext.Route{}, nil)
	defer seam("done", id, "", ext.Route{}, nil)
	d, listed := r.s.registry.Get(id)
	if !listed {
		r.gone(id)
		return
	}
	var info session.Info
	mayRoute := true
	switch drv := d.(type) {
	case *session.Local:
		info = drv.Info()
	case *signal.HostedSession:
		mayRoute = r.s.routesHosted(drv)
		if !mayRoute && !r.knows(id) {
			return
		}
		// What its host said it shows and keeps, never a change the
		// server forwarded and the host has not yet applied.
		info = drv.HostView()
	default:
		return
	}
	seam("judge", id, "", ext.Route{}, nil)
	if dec, ok := r.judge(info, mayRoute); ok {
		r.route(d, dec)
	}
}

// question is the id of the question info shows, "" for none: a needs_input
// with an id, the hold of the turn aside (no question: a person at the
// terminal deals with it) and a durable decision's mirror (its record is
// routed: record).
func question(info session.Info) string {
	a := info.Attention
	if a.State != session.AttentionNeedsInput || a.ID == "" || a.Source == session.SourceSubmit || a.Decision != "" {
		return ""
	}
	return a.ID
}

// knows reports whether the router keeps a record of the session id.
func (r *decisionRouter) knows(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id] != nil
}

// judge resolves the session's routed questions that no longer wait, and
// answers the question to route, when info shows one the router has not
// seen and may route. With mayRoute false (a hosted session the server
// may no longer answer) it only resolves: a question it has not seen is
// left unseen, routed once the server may answer the session again.
func (r *decisionRouter) judge(info session.Info, mayRoute bool) (ext.Decision, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.sessions[info.ID]
	if st == nil && !mayRoute {
		return ext.Decision{}, false
	}
	if st == nil {
		st = &routedSession{ids: map[string]routeState{}}
		r.sessions[info.ID] = st
	}
	shown := question(info)
	terminal := shown != "" && info.Attention.Answered == session.AnsweredTerminal
	ended := info.Status.Ended()
	st.shown, st.kept, st.ref = shown, slices.Clone(info.Kept), sessionRef(info)
	now := time.Now().UTC()
	ref := st.ref
	for _, id := range st.order {
		if st.ids[id] != routeOutstanding {
			continue
		}
		waits := !ended && (id == shown && !terminal || slices.Contains(info.Kept, id))
		if !waits {
			st.ids[id] = routeResolving
			r.told = append(r.told, pendingResolution{r: ext.Resolution{ID: id, Session: ref, Ended: ended, At: now}, sess: st})
		}
	}
	r.tellLocked()
	var d ext.Decision
	route := false
	if _, seen := st.ids[shown]; mayRoute && shown != "" && !seen && !ended {
		switch {
		case terminal:
			// Answered by keys typed in the terminal before the router
			// read it: it waits for no one.
			r.addLocked(st, shown, routeResolved)
		case r.untold >= r.bound:
			r.addLocked(st, shown, routeSkipped)
			r.skippedLocked(now)
		case !routerLive(r.s.exts.router):
			// The router's Start has not succeeded: it routes nothing now,
			// and is told nothing of this question later.
			r.addLocked(st, shown, routeSkipped)
		default:
			r.addLocked(st, shown, routeOutstanding)
			r.untold++
			d, route = decisionOf(info), true
		}
	}
	r.pruneLocked(st)
	return d, route
}

// addLocked adds the question id to st in state. The caller holds r.mu.
func (r *decisionRouter) addLocked(st *routedSession, id string, state routeState) {
	st.ids[id] = state
	st.order = append(st.order, id)
}

// pruneLocked forgets st's oldest forgettable questions (no longer
// outstanding: resolved, being told or skipped; and neither shown nor kept
// by the last read) while it keeps more than routedKept. A question that
// no longer waits never shows again (a session shows again only a question
// it kept), so forgetting it routes nothing twice; one being told keeps
// its place in the resolutions, which tell skips marking once it is
// forgotten. The caller holds r.mu.
func (r *decisionRouter) pruneLocked(st *routedSession) {
	for len(st.order) > routedKept {
		i := slices.IndexFunc(st.order, func(id string) bool {
			return st.ids[id] != routeOutstanding && id != st.shown && !slices.Contains(st.kept, id)
		})
		if i < 0 {
			return
		}
		delete(st.ids, st.order[i])
		st.order = slices.Delete(st.order, i, i+1)
	}
}

// skippedLocked notes a question not routed for the bound, logged at warn
// once a minute with how many since. The caller holds r.mu.
func (r *decisionRouter) skippedLocked(now time.Time) {
	r.skips++
	if !r.skipLog.IsZero() && now.Sub(r.skipLog) < routerSkipLogEvery {
		return
	}
	r.s.log.Warn("decision router: questions not routed, too many routed questions not yet resolved", "skipped", r.skips, "bound", r.bound)
	r.skips, r.skipLog = 0, now
}

// gone resolves, as ended, the questions still outstanding of the session
// id, which the registry no longer holds, and forgets it; its resolutions
// stay queued until told.
func (r *decisionRouter) gone(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.sessions[id]
	if st == nil {
		return
	}
	delete(r.sessions, id)
	now := time.Now().UTC()
	for _, qid := range st.order {
		if st.ids[qid] == routeOutstanding {
			st.ids[qid] = routeResolving
			r.told = append(r.told, pendingResolution{r: ext.Resolution{ID: qid, Session: st.ref, Ended: true, At: now}, sess: st})
		}
	}
	r.tellLocked()
}

// tellLocked begins the goroutine that tells the resolutions, when some
// wait and none tells them. The caller holds r.mu.
func (r *decisionRouter) tellLocked() {
	if r.telling || len(r.told) == 0 {
		return
	}
	r.telling = true
	r.s.track(r.tell)
}

// tell tells the router each queued resolution, the oldest first, one at a
// time (Resolved, through the guard: in one of the provider's call slots,
// waited for when all are taken, within DeliveryWait, its panic
// recovered); an entry leaves the list only once its call has returned, so none is ever
// lost, and none is told twice. It ends when the list is empty or the
// server's extensions stop.
func (r *decisionRouter) tell() {
	for {
		r.mu.Lock()
		if len(r.told) == 0 || r.s.extCtx.Err() != nil {
			r.telling = false
			r.mu.Unlock()
			return
		}
		p := r.told[0]
		r.mu.Unlock()
		r.s.exts.router.Resolved(r.s.extCtx, p.r)
		r.mu.Lock()
		r.told[0] = pendingResolution{}
		r.told = r.told[1:]
		if len(r.told) == 0 {
			r.told = nil
		}
		r.untold--
		if st := p.sess; r.sessions[p.r.Session.ID] == st && st.ids[p.r.ID] == routeResolving {
			st.ids[p.r.ID] = routeResolved
			r.pruneLocked(st)
		}
		r.mu.Unlock()
	}
}

// route asks the router where d goes, within its wait, and acts on what
// the guard answers: a status entry for its destinations, its answer
// applied once on a goroutine of its own.
func (r *decisionRouter) route(drv session.Driver, d ext.Decision) {
	ctx, cancel := r.s.extCtx, context.CancelFunc(func() {})
	if r.wait > 0 {
		ctx, cancel = context.WithTimeout(ctx, r.wait)
	}
	rt, sent := routeSent(ctx, r.s.exts.router, d)
	cancel()
	if !sent {
		// The provider was never given the question (its call slots all
		// busy, or its Start undone meanwhile): skipped, never told.
		r.unsent(d)
	}
	if len(rt.To) > 0 {
		r.s.recordRouted(drv, routedWords(rt))
	}
	if a := rt.Answer; a != nil {
		answer := *a
		r.s.track(func() { r.apply(drv, d, answer) })
	}
	seam("routed", d.Session.ID, d.ID, rt, nil)
}

// unsent holds the question d, outstanding since its judge, as skipped:
// Route never reached the provider, so Resolved never will either, and it
// is not routed later (the people watching answer it).
func (r *decisionRouter) unsent(d ext.Decision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.sessions[d.Session.ID]; st != nil && st.ids[d.ID] == routeOutstanding {
		st.ids[d.ID] = routeSkipped
		r.untold--
	}
}

// routeSent asks dr where d goes, and reports whether its provider was
// given d: a guarded router that found none of its call slots free, or
// whose Start has not succeeded, answered the free default itself.
func routeSent(ctx context.Context, dr ext.DecisionRouter, d ext.Decision) (ext.Route, bool) {
	rt, sent, _ := routeTold(ctx, dr, d)
	return rt, sent
}

// routeTold is routeSent, and reports too whether the provider answered
// in time with a route the guard took (ok): not when it failed, was late,
// panicked or answered a route Route.Valid refuses.
func routeTold(ctx context.Context, dr ext.DecisionRouter, d ext.Decision) (rt ext.Route, sent, ok bool) {
	if g, isGuard := dr.(guardRouter); isGuard {
		return g.routeTold(ctx, d)
	}
	rt, err := dr.Route(ctx, d)
	return rt, true, err == nil
}

// apply applies a route's answer to its question once, through the
// question's claim, as Host.Submit does (submitTo): a person's answer that
// came first wins, and the route's is refused (logged at debug). A hosted
// session's is sent to its host, which applies it under its own claim.
func (r *decisionRouter) apply(drv session.Driver, d ext.Decision, a ext.Answer) {
	ctx, cancel := context.WithTimeout(r.s.extCtx, submitTimeout)
	defer cancel()
	req := ext.SubmitRequest{Session: d.Session.ID, Question: d.ID, Input: a.Input, Text: a.Text, By: a.By}
	var res ext.SubmitResult
	var err error
	if local, ok := drv.(*session.Local); ok {
		res, err, _ = r.s.submitTo(ctx, local, req, nil)
	} else {
		res, err = r.s.answerHosted(ctx, drv, req)
	}
	switch {
	case err == nil && res.Status == "sent":
		r.s.log.Info("routed answer sent to the session's host", "session", d.Session.ID, "question", d.ID)
	case err == nil && res.Status == "pending":
		r.s.log.Info("routed answer accepted: it lands as the program reads", "session", d.Session.ID, "question", d.ID)
	case err == nil:
		r.s.log.Info("routed answer applied", "session", d.Session.ID, "question", d.ID)
	case errors.Is(err, ext.ErrQuestionAnswered):
		r.s.log.Debug("routed answer refused: the question no longer waits", "session", d.Session.ID, "question", d.ID)
	default:
		r.s.log.Warn("routed answer not applied", "session", d.Session.ID, "question", d.ID, "err", err)
	}
	seam("applied", d.Session.ID, d.ID, ext.Route{}, err)
}

// routedWords is the status entry of a route with destinations:
// "routed: <kind> <name>, … (<rule>)".
func routedWords(rt ext.Route) string {
	to := make([]string, 0, len(rt.To))
	for _, d := range rt.To {
		to = append(to, d.Kind+" "+d.Name)
	}
	words := "routed: " + strings.Join(to, ", ")
	if rt.Rule != "" {
		words += " (" + rt.Rule + ")"
	}
	return words
}

// routerLive reports whether the router slot answers through its
// provider: a guarded router whose Start has succeeded, or one that needs
// none.
func routerLive(dr ext.DecisionRouter) bool {
	g, ok := dr.(guardRouter)
	return !ok || live(g.ready)
}

// sessionRef is the session info names, with its crew reference.
func sessionRef(info session.Info) ext.SessionRef {
	ref := ext.SessionRef{ID: info.ID, Name: info.Name, AgentID: info.AgentID}
	if c := info.Crew; c != nil {
		ref.Run, ref.Task, ref.Attempt = c.RunID, c.Task, c.Attempt
	}
	return ref
}

// decisionOf is the question info shows, as a router is told it.
func decisionOf(info session.Info) ext.Decision {
	a := info.Attention
	d := ext.Decision{ID: a.ID, Session: sessionRef(info), Message: a.Message, Kind: a.Kind, Source: a.Source, Plan: a.Plan}
	if a.Since != nil {
		d.Since = *a.Since
	}
	for _, o := range a.Options {
		d.Options = append(d.Options, ext.DecisionOption{Label: o.Label, Input: o.Input})
	}
	return d
}

// recordSeam, when a test sets it, is told the router's steps for a
// durable decision: "read" before a worker reads its record and "done" as
// that read ends, whatever it found; "routed" once its route is handled
// (the route the guard answered; its destinations recorded and its answer
// offered, the latter's refusal as err; err ErrUnrouted when the tell
// failed or was late, owed to the next pass), "skipped" when it was not
// made, no call slot of the provider's free or its Start undone (owed
// too); "resolved" once Resolved returned, "unresolved" when its end was
// not told for the same reasons (best-effort: the router reads it); "listed" for each decision a pass marked, and "passed" with the
// decision "" as a pass ends, err its listing's failure. Nil outside
// tests.
var recordSeam atomic.Pointer[func(step, decision string, rt ext.Route, err error)]

func recordStep(step, decision string, rt ext.Route, err error) {
	if f := recordSeam.Load(); f != nil {
		(*f)(step, decision, rt, err)
	}
}

// errUnrouted is a decision's tell that failed or was not made: the next
// pass tells it again.
var errUnrouted = errors.New("the decision was not told to the router")

// recordReadFault, when a test sets it, fails the read of a decision's
// record with the error it answers (nil reads it). Nil outside tests.
var recordReadFault atomic.Pointer[func(decision string) error]

var (
	// recordRetryFirst and recordRetryMost bound the wait before a pass
	// owed (a tell failed or not made, a read failed) is made: the first,
	// doubling to the most while tells keep failing. A test lowers them.
	recordRetryFirst = time.Second
	recordRetryMost  = time.Minute
)

// owned tells the router that this server owns the ledger ld (from
// ledgerRecovered, once its recovery is done: at the server's start, or
// when it takes the owner lock over from another server). Only the
// ledger's owner tells the router of decisions: from now on, every one
// it holds open is told again (opened emptied), by a pass once the
// router's provider has begun too (begin).
func (r *decisionRouter) owned(ld *ledger.Ledger) {
	r.mu.Lock()
	r.ledger = ld
	clear(r.opened)
	r.mu.Unlock()
	r.s.track(r.pass)
}

// begin tells the router that its provider answers (its Start succeeded,
// or it needs none: the server's queue of it begun); once the server owns
// the ledger too (owned), a pass tells it what the ledger holds open.
func (r *decisionRouter) begin() {
	r.mu.Lock()
	r.begun = true
	r.mu.Unlock()
	r.s.track(r.pass)
}

// readyLocked answers the ledger the router reads decisions from, or nil
// until this server owns it and the router's provider has begun. The
// caller holds r.mu.
func (r *decisionRouter) readyLocked() *ledger.Ledger {
	if !r.begun {
		return nil
	}
	return r.ledger
}

// pass tells the router of every decision the ledger holds open that this
// server has not told it of since it took the ledger (Program item DR1,
// the router's hook, level-triggered): one listing of the ledger's open
// decisions, in pages, each marked and read by a worker as any change is
// (readRecord), so nothing here waits on the provider. A router may so be
// told of a decision more than once and treats a repeat as nothing new
// (ext.DecisionRouter). The ids opened that the listing no longer holds
// open are let go. Its work is the open decisions; it remembers nothing of
// what a router heard. A listing that fails owes another pass (owe).
func (r *decisionRouter) pass() {
	r.mu.Lock()
	ld := r.readyLocked()
	if ld == nil || r.s.extCtx.Err() != nil {
		r.mu.Unlock()
		return
	}
	if r.passing {
		r.again = true
		r.mu.Unlock()
		return
	}
	r.passing = true
	r.mu.Unlock()
	for {
		err := r.passOver(r.s.extCtx, ld)
		recordStep("passed", "", ext.Route{}, err)
		if err != nil && r.s.extCtx.Err() == nil {
			r.s.log.Warn("decision router: the ledger's open decisions not read: read again", "err", err)
			r.owe()
		}
		r.mu.Lock()
		ld = r.readyLocked()
		if !r.again || ld == nil {
			r.passing, r.again = false, false
			r.mu.Unlock()
			return
		}
		r.again = false
		r.mu.Unlock()
	}
}

// passOver lists the decisions ld holds open and marks each not opened.
func (r *decisionRouter) passOver(ctx context.Context, ld *ledger.Ledger) error {
	open := map[string]bool{}
	f := ledger.DecisionFilter{States: []string{ledger.DecisionOpen}}
	for {
		rows, err := ld.Decisions(ctx, f)
		if err != nil {
			return err
		}
		for _, row := range rows {
			id := row.ID()
			open[id] = true
			r.mu.Lock()
			told := r.opened[id]
			r.mu.Unlock()
			if !told {
				recordStep("listed", id, ext.Route{}, nil)
				r.mark(id)
			}
		}
		if len(rows) < ledger.MaxDecisionsPage {
			break
		}
		f.Before = rows[len(rows)-1].N
	}
	r.mu.Lock()
	for id := range r.opened {
		if !open[id] {
			// Closed, or gone, since: its read let it go, or failed to.
			delete(r.opened, id)
		}
	}
	r.mu.Unlock()
	return nil
}

// owe makes a pass after a wait, from recordRetryFirst doubling to
// recordRetryMost while tells keep failing, unless one is owed already or
// the server's extensions have stopped by then.
func (r *decisionRouter) owe() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.waiting || r.s.extCtx.Err() != nil {
		return
	}
	r.waiting = true
	r.passWait = min(max(2*r.passWait, recordRetryFirst), recordRetryMost)
	t := time.AfterFunc(r.passWait, func() {
		r.mu.Lock()
		r.waiting = false
		r.mu.Unlock()
		if r.s.extCtx.Err() == nil {
			r.pass()
		}
	})
	context.AfterFunc(r.s.extCtx, func() { t.Stop() })
}

// take takes one of the call slots of the router's provider (the guard's,
// shared by every call into it), or answers false when all are taken or
// its Start has not succeeded: the call is then not made, and no goroutine
// is begun for it. Every server's router is behind the guard
// (installExtensions).
func (r *decisionRouter) take() (guardRouter, extSlot, bool) {
	g, ok := r.s.exts.router.(guardRouter)
	if !ok {
		return guardRouter{}, extSlot{}, false
	}
	slot, ok := g.take()
	return g, slot, ok
}

// record notes that the durable decision id ("dr-<n>") changed
// (Server.decisionTold, on the committing goroutine, after the commit: so
// only by the ledger's owner): it is marked as a session is, and a worker
// reads its record from the ledger and judges from that read alone
// (readRecord), so a change made while one reads it is read again after,
// and the last read is of the record as it last committed. It never waits
// but on the router's own lock.
func (r *decisionRouter) record(id string) { r.mark(id) }

// readRecord reads the record of the durable decision id (n) once, once
// the router reads decisions (readyLocked: before, the pass at its start
// reads them all), and judges from it: one open and not opened is told
// (routeRecord); one final, or gone (its run deleted), that this server
// told of is told its end, best-effort (resolveRecord); one answered and
// not final waits for its end. A read that fails owes a pass.
func (r *decisionRouter) readRecord(id string, n int64) {
	recordStep("read", id, ext.Route{}, nil)
	defer recordStep("done", id, ext.Route{}, nil)
	r.mu.Lock()
	ld := r.readyLocked()
	r.mu.Unlock()
	if ld == nil {
		return
	}
	var (
		rec   ledger.DecisionRecord
		found bool
		err   error
	)
	if f := recordReadFault.Load(); f != nil {
		err = (*f)(id)
	}
	if err == nil {
		rec, found, err = ld.Decision(r.s.extCtx, n)
	}
	switch {
	case err != nil:
		if r.s.extCtx.Err() == nil {
			r.s.log.Warn("decision router: a decision's record not read: a pass reads it again", "decision", id, "err", err)
			r.owe()
		}
		return
	case !found || rec.Final():
		r.mu.Lock()
		told := r.opened[id]
		delete(r.opened, id)
		r.mu.Unlock()
		if told {
			ref := ext.SessionRef{}
			if found {
				ref = recordRef(rec)
			}
			r.resolveRecord(id, ref)
		}
		return
	case rec.State != ledger.DecisionOpen:
		// Answered and not yet final: it waits for no one, and is told its
		// end when it reaches one.
		return
	}
	r.mu.Lock()
	if r.opened[id] {
		r.mu.Unlock()
		return
	}
	r.opened[id] = true
	r.mu.Unlock()
	r.routeRecord(recordDecision(rec), rec.Rev)
}

// resolveRecord tells the router that the decision id reached its end
// (Resolved, through the guard), on a goroutine of its own, when one of
// its provider's call slots is free (take) and its Start has succeeded:
// best-effort, never owed, for a router reads a decision's state
// (ext.Host.Decision). The goroutine holds the slot until Resolved returns.
func (r *decisionRouter) resolveRecord(id string, ref ext.SessionRef) {
	g, slot, ok := r.take()
	if !ok {
		recordStep("unresolved", id, ext.Route{}, nil)
		return
	}
	res := ext.Resolution{ID: id, Request: id, Session: ref, At: time.Now().UTC()}
	r.s.track(func() {
		g.resolveIn(r.s.extCtx, slot, res)
		recordStep("resolved", id, ext.Route{}, nil)
	})
}

// routeRecord tells the router of the open decision d (Route, through the
// guard, within its wait) on a goroutine of its own, when one of its
// provider's call slots is free (take), and acts on what the guard
// answers: its destinations kept as the record's routed history row, then
// its answer offered as a response at rev, the rev the router was shown,
// which local/1 refuses and records. The goroutine waits for the call
// until its deadline and moves on; the call keeps its slot until it
// returns. A tell not made (no slot free, its Start undone) or failed (an
// error, late, a route refused) is let go from opened and owes a pass,
// which tells it again.
func (r *decisionRouter) routeRecord(d ext.Decision, rev int) {
	unrouted := func() {
		r.mu.Lock()
		delete(r.opened, d.Request)
		r.mu.Unlock()
		r.owe()
	}
	g, slot, ok := r.take()
	if !ok {
		unrouted()
		recordStep("skipped", d.Request, ext.Route{}, nil)
		return
	}
	r.s.track(func() {
		ctx, cancel := r.s.extCtx, context.CancelFunc(func() {})
		if r.wait > 0 {
			ctx, cancel = context.WithTimeout(ctx, r.wait)
		}
		rt, ok := g.routeIn(ctx, slot, d)
		cancel()
		if !ok {
			unrouted()
			recordStep("routed", d.Request, rt, errUnrouted)
			return
		}
		r.mu.Lock()
		r.passWait = 0
		r.mu.Unlock()
		if len(rt.To) > 0 {
			to := make([]crew.Destination, 0, len(rt.To))
			for _, t := range rt.To {
				to = append(to, crew.Destination{Kind: t.Kind, Name: t.Name})
			}
			if _, err := r.s.runs.RecordRoute(r.s.extCtx, d.Request, r.s.exts.router.Name(), to, rt.Rule); err != nil {
				r.s.log.Warn("routed decision's destinations not recorded", "decision", d.Request, "err", err)
			}
		}
		var err error
		if a := rt.Answer; a != nil {
			err = r.offer(d, *a, rev)
		}
		recordStep("routed", d.Request, rt, err)
	})
}

// offer offers the router's answer a to the decision d as a response at
// rev: under the actor user:<subject> and the name the router's principal
// gives, with the router's authority and no grant, whatever principal the
// subject also names (Owner decisions, 8). local/1 refuses it
// (not_approver) and keeps the refusal as the response's receipt; it is
// never applied. The answer's choice is its option's place ("1" first).
func (r *decisionRouter) offer(d ext.Decision, a ext.Answer, rev int) error {
	resp := crew.Response{ResponseID: crew.RouterResponseID, Text: a.Text, ExpectedRev: rev, ByName: a.By.Name}
	if a.Input != "" {
		n, err := strconv.Atoi(a.Input)
		if err != nil || n < 1 {
			return fmt.Errorf("the routed answer's choice %q is no option's place", a.Input)
		}
		n--
		resp.Choice = &n
	}
	ctx, cancel := context.WithTimeout(r.s.extCtx, submitTimeout)
	defer cancel()
	rc, err := r.s.runs.RespondDecision(ctx, d.Request, resp, ext.SubjectActor+a.By.Subject, crew.Authority{Via: crew.ViaRouter, Subject: a.By.Subject})
	switch {
	case err == nil:
		// No policy of this repository takes a router's answer: one that
		// did would be a defect, and is said loudly.
		r.s.log.Error("routed answer accepted for a decision", "decision", d.Request, "k", rc.K)
	case errors.Is(err, crew.ErrDecision):
		r.s.log.Info("routed answer refused", "decision", d.Request, "words", err.Error())
	default:
		r.s.log.Warn("routed answer not recorded", "decision", d.Request, "err", err)
	}
	return err
}

// recordRef is the session a decision's record names: its asking
// attempt's (none for a plan's gate), with the run that holds it now and
// its task.
func recordRef(rec ledger.DecisionRecord) ext.SessionRef {
	return ext.SessionRef{ID: rec.SessionID, Run: rec.CurrentRun, Task: rec.TaskID, Attempt: rec.Attempt}
}

// recordDecision is a durable decision's record as a router is told it:
// its id twice (ID and Request), the question cut where a character ends
// to the 500 bytes ext.Decision keeps, kind decision, and its options'
// labels, each picked by its place ("1" first).
func recordDecision(rec ledger.DecisionRecord) ext.Decision {
	var body struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal([]byte(rec.Request), &body)
	d := ext.Decision{ID: rec.ID(), Request: rec.ID(), Session: recordRef(rec), Message: cutBytes(body.Question, maxRoutedMessage), Kind: decisionKind, Since: rec.CreatedAt}
	for i, label := range crew.OptionLabels(rec.Type, rec.Request) {
		d.Options = append(d.Options, ext.DecisionOption{Label: label, Input: strconv.Itoa(i + 1)})
	}
	return d
}

const (
	// decisionKind is a durable decision's ext.Decision.Kind.
	decisionKind = "decision"
	// maxRoutedMessage is ext.Decision.Message's bound, in bytes.
	maxRoutedMessage = 500
)
