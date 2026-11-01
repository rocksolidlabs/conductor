package api

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
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
	// bound is maxUnresolved; a test lowers it.
	bound int

	mu       sync.Mutex
	dirty    map[string]bool // the ids in queue
	queue    []string        // dirty ids, oldest first, each once
	working  map[string]bool // ids a worker reads now
	workers  int
	sessions map[string]*routedSession
	untold   int                 // routed ids outstanding or being told
	told     []pendingResolution // first in, first out
	telling  bool
	skips    int // skipped past the bound since the last log
	skipLog  time.Time
}

// newDecisionRouter is the router of s; nothing runs until a session
// changes.
func newDecisionRouter(s *Server) *decisionRouter {
	return &decisionRouter{s: s, bound: maxUnresolved, dirty: map[string]bool{}, working: map[string]bool{}, sessions: map[string]*routedSession{}}
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
// forgotten. A hosted session is read so only when the server may answer
// it (routesHosted).
func (r *decisionRouter) read(id string) {
	seam("read", id, "", ext.Route{}, nil)
	defer seam("done", id, "", ext.Route{}, nil)
	d, listed := r.s.registry.Get(id)
	if !listed {
		r.gone(id)
		return
	}
	var info session.Info
	switch drv := d.(type) {
	case *session.Local:
		info = drv.Info()
	case *signal.HostedSession:
		if !r.s.routesHosted(drv) {
			return
		}
		// What its host said it shows and keeps, never a change the
		// server forwarded and the host has not yet applied.
		info = drv.HostView()
	default:
		return
	}
	seam("judge", id, "", ext.Route{}, nil)
	if dec, ok := r.judge(info); ok {
		r.route(d, dec)
	}
}

// question is the id of the question info shows, "" for none: a needs_input
// with an id, the hold of the turn aside (no question: a person at the
// terminal deals with it).
func question(info session.Info) string {
	a := info.Attention
	if a.State != session.AttentionNeedsInput || a.ID == "" || a.Source == session.SourceSubmit {
		return ""
	}
	return a.ID
}

// judge resolves the session's routed questions that no longer wait, and
// answers the question to route, when info shows one the router has not
// seen and may route.
func (r *decisionRouter) judge(info session.Info) (ext.Decision, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.sessions[info.ID]
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
	if _, seen := st.ids[shown]; shown != "" && !seen && !ended {
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
// time (Resolved, through the guard: DeliveryWait, its panic recovered); an
// entry leaves the list only once its call has returned, so none is ever
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
		recordRouted(drv, routedWords(rt))
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
	if g, ok := dr.(guardRouter); ok {
		return g.route(ctx, d)
	}
	rt, _ := dr.Route(ctx, d)
	return rt, true
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
		res, err = r.s.answerHosted(drv, req)
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
