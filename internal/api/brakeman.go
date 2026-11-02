package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Brakeman's desk (docs/plans/brakeman.md, PR-3c): the server's one owner
// of Brakeman's requests. It holds the store (internal/brakeman), the
// queue, the slot a turn holds and the watchdog. A turn is a run
// Yardmaster launches: an unsaved one-member crew of the configured agent
// in its read-only mode, launched through the desk's one door
// (launchTurn), the engine and crew.Launcher (Server.Launch), never
// os/exec; its member reads its request and submits one brief over HTTP
// with its own session's token (brakeman_session.go). Nothing a turn does
// changes a run, a ticket or a crew: it is refused every route that would
// (the brakeman_run and brakeman_cannot refusals).

// The desk's bounds and waits.
const (
	// brakemanQueue is how many requests wait at most.
	brakemanQueue = 8
	// brakemanGrace is how long a briefed turn's run is left before it is
	// stopped, so the agent's last output reaches its terminal.
	brakemanGrace = 20 * time.Second
	// brakemanRetry is how often a stop that left a process is retried,
	// and brakemanStuck after how many failed retries the desk takes no
	// new turn.
	brakemanRetry = 10 * time.Second
	brakemanStuck = 6
	// brakemanStopWait bounds one stop of a turn's run.
	brakemanStopWait = 15 * time.Second
	// maxBrakemanLine bounds the line typed into a turn's session.
	maxBrakemanLine = 300
	// maxBrakemanRequestReply bounds GET /api/brakeman/requests/{id}: the
	// request's document and its brief's.
	maxBrakemanRequestReply = brakeman.MaxRequestDoc + brakeman.MaxBrief
	// maxBrakemanEvent bounds a brakeman event's data.
	maxBrakemanEvent = 200
	// brakemanMember is the one member of a turn's crew, and its crew's id.
	brakemanMember = "brakeman"
)

// brakemanDesk is the desk: one a server with a data directory, none on a
// switchyard.
type brakemanDesk struct {
	s  *Server
	st *brakeman.Store
	// clock is the desk's clock and its store's: time.Now, a test's own
	// otherwise.
	clock atomic.Pointer[func() time.Time]
	// wake makes the loop look again; done ends it, and loopDone says it
	// has ended.
	wake     chan struct{}
	done     chan struct{}
	loopDone chan struct{}
	stopOnce sync.Once
	// steps counts the loop's passes, for a test to wait on one.
	steps atomic.Uint64

	mu sync.Mutex
	// queue is the queued requests' ids, oldest first.
	queue []string
	// turn is the request holding the slot, nil when none does.
	turn *deskTurn
	// read are the requests whose session read them (the request route
	// with its own token): one that exits before is a failed start.
	read map[string]bool
	// stopFails counts the failed stops of the turn holding the slot;
	// stuck is set past brakemanStuck and refuses new requests.
	stopFails int
	stuck     bool
}

// deskTurn is the slot: a request from starting until its launch has
// returned and every member's process is reaped.
type deskTurn struct {
	id string
	// account is the agent's login folder as the turn began ("" for its
	// default login), which Server.Launch sets in the session.
	account string
	// launching says launchTurn has not returned; cancel ends its
	// context (the deadline, a cancel, Brakeman turned off).
	launching bool
	cancel    context.CancelFunc
	// runID is the turn's run once known; locals its member sessions, kept
	// for their processes (ProcessDone) whether or not the registry still
	// lists them.
	runID  string
	locals []*session.Local
	// stopping says a stop is under way; nextStop is when the next may
	// start (a retry's wait).
	stopping bool
	nextStop time.Time
}

// openBrakemanDesk opens the desk over the data directory st: its store
// and its owner lock (read-only while another server holds it, the desk
// taking no turn until this one does), then its loop. nil on a
// switchyard and when the store cannot be opened.
func (s *Server) openBrakemanDesk(st *store.Store) *brakemanDesk {
	d := &brakemanDesk{s: s, wake: make(chan struct{}, 1), done: make(chan struct{}), loopDone: make(chan struct{}), read: map[string]bool{}}
	now := time.Now
	d.clock.Store(&now)
	bs, err := brakeman.Open(st, brakeman.Options{Now: d.now, DefaultCwd: s.cfg.DefaultCwd, Log: s.log})
	if err != nil {
		s.log.Warn("Brakeman is off: its records could not be opened", "err", err)
		return nil
	}
	if ro, why := bs.ReadOnly(); ro {
		s.log.Warn("Brakeman takes no turn: its records are read-only", "why", why)
	}
	d.st = bs
	bs.OnOwned(func() {
		d.loadQueue()
		d.poke()
	})
	d.loadQueue()
	go d.loop()
	return d
}

// now is the desk's clock.
func (d *brakemanDesk) now() time.Time { return (*d.clock.Load())() }

// close ends the loop and closes the store: no turn starts after it.
func (d *brakemanDesk) close() {
	d.stopOnce.Do(func() { close(d.done) })
	<-d.loopDone
	_ = d.st.Close()
}

// poke makes the loop look again.
func (d *brakemanDesk) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// loadQueue reads the queued requests from the store, oldest first.
func (d *brakemanDesk) loadQueue() {
	var queued []string
	after := ""
	for {
		page, next, err := d.st.List(after, brakeman.MaxPage)
		if err != nil {
			d.s.log.Warn("Brakeman's queue could not be read", "err", err)
			return
		}
		for _, r := range page {
			if r.State == brakeman.StateQueued {
				queued = append(queued, r.ID)
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	slices.Reverse(queued)
	d.mu.Lock()
	d.queue = queued
	d.mu.Unlock()
}

// loop is the watchdog: it looks again on a poke, at the next moment a
// turn's deadline, grace or retry falls due, and ends with the desk.
func (d *brakemanDesk) loop() {
	defer close(d.loopDone)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		next := d.step()
		d.steps.Add(1)
		timer.Stop()
		var tick <-chan time.Time
		if next > 0 {
			timer.Reset(next)
			tick = timer.C
		}
		select {
		case <-d.done:
			return
		case <-d.wake:
		case <-tick:
		}
	}
}

// step does what is due: the turn holding the slot tended, else the
// oldest queued request started. It answers how long until something
// falls due on its own, 0 for nothing.
func (d *brakemanDesk) step() time.Duration {
	if ro, _ := d.st.ReadOnly(); ro {
		return 0
	}
	d.mu.Lock()
	t := d.turn
	d.mu.Unlock()
	if t != nil {
		return d.tend(t)
	}
	return d.dequeue()
}

// tend closes the turn at its deadline, stops its run once its request is
// closed (a brief's after the grace), retries a stop that left a process,
// and gives the slot up once the launch has returned and every member's
// process is reaped.
func (d *brakemanDesk) tend(t *deskTurn) time.Duration {
	now := d.now()
	req, err := d.st.Get(t.id)
	if err != nil {
		d.s.log.Warn("Brakeman's turn could not be read", "request", t.id, "err", err)
		return brakemanRetry
	}
	if req.Open() && !req.Deadline.IsZero() && !now.Before(req.Deadline) {
		req, err = d.update(t.id, func(r *brakeman.Request) error {
			if !r.Open() {
				return nil
			}
			return r.To(brakeman.StateTimedOut, fmt.Sprintf("the turn's %d minutes passed with no brief", r.Config.Budget.TurnMinutes), now)
		})
		if err != nil {
			d.s.log.Warn("Brakeman's turn could not be timed out", "request", t.id, "err", err)
			return brakemanRetry
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if req.Open() {
		return untilPositive(req.Deadline, now)
	}
	if t.cancel != nil {
		t.cancel()
	}
	live := liveLocals(t.locals)
	if !t.launching && len(live) == 0 && !t.stopping {
		d.turn, d.stopFails, d.stuck = nil, 0, false
		d.poke()
		return 0
	}
	if t.stopping || len(live) == 0 && t.runID == "" {
		return 0
	}
	stopAt := now
	if req.State == brakeman.StateBriefed {
		stopAt = req.BriefAt.Add(brakemanGrace)
	}
	if stopAt.Before(t.nextStop) {
		stopAt = t.nextStop
	}
	if now.Before(stopAt) {
		return stopAt.Sub(now)
	}
	if len(live) == 0 && t.launching {
		// Nothing to stop yet: the launch's return stops what it made.
		return 0
	}
	t.stopping = true
	runID, locals := t.runID, slices.Clone(t.locals)
	d.s.track(func() { d.stopTurn(t, runID, locals) })
	return 0
}

// untilPositive is how long until at, at least a millisecond.
func untilPositive(at, now time.Time) time.Duration {
	return max(time.Millisecond, at.Sub(now))
}

// liveLocals is the sessions whose process is not reaped.
func liveLocals(ls []*session.Local) []*session.Local {
	var out []*session.Local
	for _, l := range ls {
		select {
		case <-l.ProcessDone():
		default:
			out = append(out, l)
		}
	}
	return out
}

// stopTurn stops the turn's run (Engine.Stop, which stops each member it
// lists) and each member session whose process is not reaped, the ones
// the registry no longer lists among them. A process left counts a
// failed stop, retried after brakemanRetry; past brakemanStuck the desk
// takes no new turn until a later stop, or the run page's, reaps it.
func (d *brakemanDesk) stopTurn(t *deskTurn, runID string, locals []*session.Local) {
	ctx, cancel := context.WithTimeout(context.Background(), brakemanStopWait)
	defer cancel()
	if runID != "" {
		if err := d.s.runs.Stop(ctx, runID); err != nil && !errors.Is(err, crew.ErrRunNotFound) {
			d.s.log.Warn("Brakeman's turn's run did not stop", "run", runID, "err", err)
		}
	}
	for _, l := range liveLocals(locals) {
		if err := l.Stop(ctx); err != nil {
			d.s.log.Warn("Brakeman's turn's session did not stop", "session", l.Info().ID, "err", err)
		}
	}
	left := len(liveLocals(locals)) > 0
	d.mu.Lock()
	t.stopping = false
	if left {
		d.stopFails++
		t.nextStop = d.now().Add(brakemanRetry)
		if d.stopFails > brakemanStuck {
			d.stuck = true
		}
	}
	d.mu.Unlock()
	d.poke()
}

// watch pokes the desk once l's process is reaped, so the slot is given
// up then.
func (d *brakemanDesk) watch(l *session.Local) {
	go func() {
		select {
		case <-l.ProcessDone():
			d.poke()
		case <-d.done:
		}
	}()
}

// dequeue starts the oldest queued request as a turn, when Brakeman is on
// and the desk is not stuck: its charge then its starting state written
// (Store.Start, which closes a request past the day's turns over_budget),
// then its launch, on a goroutine of its own.
func (d *brakemanDesk) dequeue() time.Duration {
	cfg, err := d.st.Config()
	if err != nil {
		d.s.log.Warn("Brakeman's configuration could not be read", "err", err)
		return brakemanRetry
	}
	for {
		d.mu.Lock()
		if !cfg.Enabled || d.stuck || len(d.queue) == 0 || d.turn != nil {
			d.mu.Unlock()
			return 0
		}
		id := d.queue[0]
		d.mu.Unlock()
		req, err := d.st.Start(id, cfg, d.stamp(cfg))
		switch {
		case errors.Is(err, brakeman.ErrOverBudget), errors.Is(err, brakeman.ErrNotQueued), errors.Is(err, brakeman.ErrNotFound):
			d.unqueue(id)
			d.s.events.brakeman(id, req.State)
			continue
		case err != nil:
			d.s.log.Warn("Brakeman's turn could not start", "request", id, "err", err)
			return brakemanRetry
		}
		ctx, cancel := context.WithCancel(context.Background())
		t := &deskTurn{id: id, account: cfg.Account, launching: true, cancel: cancel}
		d.mu.Lock()
		d.queue = slices.DeleteFunc(d.queue, func(q string) bool { return q == id })
		d.turn = t
		d.mu.Unlock()
		d.s.events.brakeman(id, req.State)
		d.s.track(func() { d.launch(ctx, t, req, cfg) })
		return untilPositive(req.Deadline, d.now())
	}
}

// unqueue drops id from the queue.
func (d *brakemanDesk) unqueue(id string) {
	d.mu.Lock()
	d.queue = slices.DeleteFunc(d.queue, func(q string) bool { return q == id })
	d.mu.Unlock()
}

// stamp is the agent a turn under cfg runs: its id, adapter, the version
// the probe cache knows (nothing is run for it), the model and whether an
// account is set.
func (d *brakemanDesk) stamp(cfg brakeman.Config) brakeman.Stamp {
	st := brakeman.Stamp{AgentID: cfg.AgentID, Model: cfg.Model, Account: cfg.Account != ""}
	if a, ok := d.s.Catalog().Get(cfg.AgentID); ok {
		st.Adapter = a.Adapter
		if id, ok := d.s.cachedIdentity(a); ok && id.Identified && len(id.Version) <= 128 {
			st.Version = id.Version
		}
	}
	return st
}

// afterTurnLaunch, when a test sets it, runs once launchTurn has returned
// and before its return is written: the gate inside the launch seam.
var afterTurnLaunch atomic.Pointer[func(requestID string)]

// launch launches the turn's run and writes what its return says: running
// with the run and its session, for a request still starting; nothing of
// its state for one closed meanwhile (a brief, the member's end, a
// deadline, a cancel), only an empty run or session id filled, and the
// run, when the close was a deadline, a cancel or Brakeman turned off,
// recorded as the request's stray run and stopped. A launch that fails
// closes the request failed with the engine's words.
func (d *brakemanDesk) launch(ctx context.Context, t *deskTurn, req brakeman.Request, cfg brakeman.Config) {
	c, err := d.turnCrew(req, cfg)
	var run *crew.Run
	if err == nil {
		run, err = d.s.launchTurn(ctx, c, brakemanLabel(req), crew.LaunchOpts{Class: crew.ClassBrakeman, Request: req.ID})
	}
	if f := afterTurnLaunch.Load(); f != nil {
		(*f)(req.ID)
	}
	runID, sessionID := "", ""
	if run != nil {
		runID = run.ID
		if len(run.Members) > 0 {
			sessionID = run.Members[0].SessionID
		}
	}
	now := d.now()
	stray := false
	_, uerr := d.update(req.ID, func(r *brakeman.Request) error {
		fill := func() {
			if r.RunID == "" {
				r.RunID = runID
			}
			if r.SessionID == "" {
				r.SessionID = sessionID
			}
		}
		switch r.State {
		case brakeman.StateStarting:
			fill()
			if err != nil {
				return r.To(brakeman.StateFailed, launchWords(err), now)
			}
			return r.To(brakeman.StateRunning, "", now)
		case brakeman.StateTimedOut, brakeman.StateCancelled:
			fill()
			if runID != "" && r.StrayRun == "" {
				r.StrayRun, stray = runID, true
			}
		default:
			fill()
		}
		return nil
	})
	if uerr != nil {
		d.s.log.Warn("Brakeman's turn's launch could not be written", "request", req.ID, "err", uerr)
	}
	if stray {
		d.s.log.Info("Brakeman's turn's run came back after its request closed: stopped", "request", req.ID, "run", runID)
	}
	d.mu.Lock()
	t.launching = false
	if runID != "" {
		t.runID = runID
	}
	d.mu.Unlock()
	d.poke()
}

// launchWords is a failed launch's words, as a request's why holds them.
func launchWords(err error) string {
	why := "the launch failed: " + err.Error()
	var ae *apiError
	if errors.As(err, &ae) {
		why = "the launch failed: " + ae.Message
	}
	for len(why) > brakeman.MaxWhy {
		_, n := utf8.DecodeLastRuneInString(why)
		why = why[:len(why)-n]
	}
	return why
}

// brakemanLabel is a turn's run's label: "Brakeman · <kind> br-n".
func brakemanLabel(req brakeman.Request) string {
	return "Brakeman · " + req.Kind + " " + req.ID
}

// brakemanLine is the line typed into a turn's session once it is ready,
// at most maxBrakemanLine bytes.
func brakemanLine(req brakeman.Request) string {
	return "Brakeman request " + req.ID + " (" + req.Kind + `): read it with the brakeman_request tool or "conductor brakeman request", and submit one brief with brief_submit or "conductor brakeman brief". Call guide for what you may read.`
}

// turnCrew is the crew a turn launches: one member, the configured agent
// in its adapter's read-only mode, then its model and, when allowed,
// Conductor's tools, nothing a person typed; never yolo, never managed,
// in the configured folder without a worktree.
func (d *brakemanDesk) turnCrew(req brakeman.Request, cfg brakeman.Config) (crew.Crew, error) {
	args, err := d.s.turnArgs(cfg)
	if err != nil {
		return crew.Crew{}, err
	}
	line := brakemanLine(req)
	if len(line) > maxBrakemanLine {
		return crew.Crew{}, fmt.Errorf("the request's line is %d bytes, more than %d", len(line), maxBrakemanLine)
	}
	no := false
	return crew.Crew{
		ID: brakemanMember, Name: "Brakeman", Cwd: cfg.Cwd, Where: crew.WhereServer, Isolation: crew.IsolationNone,
		Goal: "Brakeman request " + req.ID, Yolo: &no,
		Members: []crew.Member{{
			Name: brakemanMember, AgentID: cfg.AgentID, Args: args,
			Start: crew.Start{When: crew.StartImmediately}, Prompt: line,
		}},
	}, nil
}

// turnArgs is a turn's member's arguments under cfg: its adapter's
// ReadOnlyArgs, then ModelArgs(model), then AllowTools("conductor") when
// allowed.
func (s *Server) turnArgs(cfg brakeman.Config) ([]string, error) {
	a, ok := s.Catalog().Get(cfg.AgentID)
	if !ok {
		return nil, fmt.Errorf("no agent %s in the catalog", cfg.AgentID)
	}
	ad, ok := agents.Get(a.Adapter)
	if !ok || ad.ReadOnlyArgs == nil {
		return nil, fmt.Errorf("the agent %s has no read-only mode", cfg.AgentID)
	}
	args := slices.Clone(ad.ReadOnlyArgs())
	if cfg.Model != "" {
		if ad.ModelArgs == nil {
			return nil, fmt.Errorf("the agent %s takes no model", cfg.AgentID)
		}
		args = append(args, ad.ModelArgs(cfg.Model)...)
	}
	if cfg.AllowConductorTools && ad.AllowTools != nil {
		args = append(args, ad.AllowTools("conductor")...)
	}
	return args, nil
}

// launchTurn is the desk's one launch door: the folder resolved and
// opened through openCwd, a crew whose yolo is not false or whose member
// lacks its adapter's read-only arguments refused, then
// Engine.LaunchNamedWith with the turn's class and request. The run is
// released at once (the desk keeps it by its class and request). PR-17
// puts ext.Host in front of it.
func (s *Server) launchTurn(ctx context.Context, c crew.Crew, label string, opts crew.LaunchOpts) (*crew.Run, error) {
	if c.Yolo == nil || *c.Yolo {
		return nil, errors.New("a Brakeman turn never runs with yolo")
	}
	if len(c.Members) != 1 || c.Managed() {
		return nil, errors.New("a Brakeman turn is one member, never a managed run")
	}
	m := c.Members[0]
	a, ok := s.Catalog().Get(m.AgentID)
	if !ok {
		return nil, fmt.Errorf("no agent %s in the catalog", m.AgentID)
	}
	ad, ok := agents.Get(a.Adapter)
	if !ok || ad.ReadOnlyArgs == nil {
		return nil, fmt.Errorf("the agent %s has no read-only mode", m.AgentID)
	}
	ro := ad.ReadOnlyArgs()
	if len(m.Args) < len(ro) || !slices.Equal(m.Args[:len(ro)], ro) {
		return nil, errors.New("a Brakeman turn's member starts with its adapter's read-only arguments")
	}
	folder, err := s.openCwd(c.Cwd)
	if err != nil {
		return nil, fmt.Errorf("the folder its turns run in: %w", err)
	}
	c.Cwd = folder.Path()
	folder.Close()
	run, release, err := s.runs.LaunchNamedWith(ctx, c, label, opts)
	release()
	return run, err
}

// turnLaunch is what Server.Launch asks of the desk for a Brakeman
// turn's member (spec.Class): the account the turn runs under, and ok
// false for a request that is not the turn holding the slot.
func (d *brakemanDesk) turnLaunch(request string) (account string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.turn == nil || d.turn.id != request {
		return "", false
	}
	return d.turn.account, true
}

// memberStarted keeps a turn's member session for its process: the slot
// is given up only once it is reaped.
func (d *brakemanDesk) memberStarted(request string, l *session.Local) {
	d.mu.Lock()
	t := d.turn
	if t != nil && t.id == request {
		t.locals = append(t.locals, l)
		if ref := l.Info().Crew; ref != nil && t.runID == "" {
			t.runID = ref.RunID
		}
	}
	d.mu.Unlock()
	d.watch(l)
	d.poke()
}

// sessionChanged is told every server session's change (localChange): a
// turn's member that ended closes its request still open, ended, or
// failed when the agent never read its request (a logged-out CLI), with
// its exit and last line.
func (d *brakemanDesk) sessionChanged(info session.Info) {
	ref := info.Crew
	if ref == nil || ref.Brakeman == nil || !info.Status.Ended() {
		return
	}
	id := ref.Brakeman.Request
	d.mu.Lock()
	read := d.read[id]
	d.mu.Unlock()
	var last string
	if l, ok := d.s.lookupLocal(info.ID); ok {
		last = l.LastLine()
	}
	now := d.now()
	_, err := d.update(id, func(r *brakeman.Request) error {
		if r.RunID == "" {
			r.RunID = ref.RunID
		}
		if r.SessionID == "" {
			r.SessionID = info.ID
		}
		if !r.Open() || r.State == brakeman.StateQueued {
			return nil
		}
		if read {
			return r.To(brakeman.StateEnded, "the session ended with no brief", now)
		}
		why := "the agent exited before it read its request: is it logged in?"
		if info.ExitCode != nil {
			why += fmt.Sprintf(" (exit %d", *info.ExitCode)
			if last != "" {
				why += ": " + last
			}
			why += ")"
		} else if last != "" {
			why += " (" + last + ")"
		}
		for len(why) > brakeman.MaxWhy {
			_, n := utf8.DecodeLastRuneInString(why)
			why = why[:len(why)-n]
		}
		return r.To(brakeman.StateFailed, why, now)
	})
	if err != nil && !errors.Is(err, brakeman.ErrNotFound) {
		d.s.log.Warn("Brakeman's turn's end could not be written", "request", id, "err", err)
	}
	d.poke()
}

// update is Store.Update with the brakeman event sent after a change of
// state.
func (d *brakemanDesk) update(id string, fn func(*brakeman.Request) error) (brakeman.Request, error) {
	before := ""
	r, err := d.st.Update(id, func(r *brakeman.Request) error {
		before = r.State
		return fn(r)
	})
	if err == nil && r.State != before {
		d.s.events.brakeman(id, r.State)
	}
	return r, err
}

// brakemanEvent is the data of a brakeman event: a request's id and its
// state after a change of it, for a page to read again.
type brakemanEvent struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// brakeman sends a brakeman event, at most maxBrakemanEvent bytes.
func (h *eventHub) brakeman(id, state string) {
	b, err := json.Marshal(brakemanEvent{ID: id, State: state})
	if err != nil || len(b) > maxBrakemanEvent || state == "" {
		return
	}
	h.broadcast([]byte("event: brakeman\ndata: " + string(b) + "\n\n"))
}

// closeTurn closes the open request id with state (cancelled), the run
// stopped after: the slot's turn has its launch cancelled and the loop
// stops it. A queued one leaves the queue. A closed one answers as it
// stands.
func (d *brakemanDesk) closeTurn(id, state, why string) (brakeman.Request, error) {
	now := d.now()
	r, err := d.update(id, func(r *brakeman.Request) error {
		if !r.Open() {
			return nil
		}
		return r.To(state, why, now)
	})
	if err != nil {
		return r, err
	}
	d.unqueue(id)
	d.mu.Lock()
	if t := d.turn; t != nil && t.id == id && t.cancel != nil {
		t.cancel()
	}
	d.mu.Unlock()
	d.poke()
	return r, nil
}

// turnOff closes what Brakeman turned off leaves: the turn in progress
// and every queued request, cancelled.
func (d *brakemanDesk) turnOff() {
	d.mu.Lock()
	ids := slices.Clone(d.queue)
	if d.turn != nil {
		ids = append(ids, d.turn.id)
	}
	d.mu.Unlock()
	for _, id := range ids {
		if _, err := d.closeTurn(id, brakeman.StateCancelled, "Brakeman was turned off"); err != nil {
			d.s.log.Warn("Brakeman's request could not be cancelled", "request", id, "err", err)
		}
	}
}

// brakemanDeskOf answers the desk, or refuses: 403 on a switchyard, 503
// without a data directory.
func (s *Server) brakemanDeskOf(w http.ResponseWriter) (*brakemanDesk, bool) {
	if s.cfg.Switchyard.Enabled {
		writeError(w, http.StatusForbidden, "switchyard", "a switchyard runs no Brakeman")
		return nil, false
	}
	if s.brakeman == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "Brakeman needs a data directory")
		return nil, false
	}
	return s.brakeman, true
}

// brakemanError answers an error of Brakeman's store.
func (s *Server) brakemanError(w http.ResponseWriter, err error) {
	var inv *brakeman.Invalid
	switch {
	case errors.As(err, &inv):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": inv.Code, "message": inv.Error(), "field": inv.Field}})
	case errors.Is(err, brakeman.ErrNotOwner):
		writeError(w, http.StatusServiceUnavailable, "brakeman_not_owner", err.Error())
	case errors.Is(err, brakeman.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such request")
	case errors.Is(err, brakeman.ErrStale):
		writeError(w, http.StatusConflict, "stale_revision", err.Error())
	case errors.Is(err, brakeman.ErrOpConflict):
		writeError(w, http.StatusConflict, "op_conflict", err.Error())
	case errors.Is(err, brakeman.ErrPruned):
		writeError(w, http.StatusGone, "pruned", err.Error())
	case errors.Is(err, brakeman.ErrBriefExists):
		writeError(w, http.StatusConflict, "brief_exists", err.Error())
	case errors.Is(err, brakeman.ErrNotTurn):
		writeError(w, http.StatusConflict, "turn_over", err.Error())
	case errors.Is(err, brakeman.ErrBound):
		writeError(w, http.StatusRequestEntityTooLarge, "bound", err.Error())
	default:
		s.log.Warn("Brakeman's records could not be read or written", "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "Brakeman's records could not be read or written")
	}
}

// routeBrakeman registers Brakeman's admin routes and its session's two.
func (s *Server) routeBrakeman(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/brakeman", s.requireAdmin(s.handleBrakeman))
	mux.HandleFunc("PUT /api/brakeman/config", s.requireAdmin(s.handleBrakemanConfig))
	mux.HandleFunc("POST /api/brakeman/requests", s.requireAdmin(s.handleBrakemanAsk))
	mux.HandleFunc("GET /api/brakeman/requests", s.requireAdmin(s.handleBrakemanRequests))
	mux.HandleFunc("GET /api/brakeman/requests/{id}", s.requireAdmin(s.handleBrakemanRequest))
	mux.HandleFunc("GET /api/brakeman/requests/{id}/refs", s.requireAdmin(s.handleBrakemanRefs))
	mux.HandleFunc("POST /api/brakeman/requests/{id}/cancel", s.requireAdmin(s.handleBrakemanCancel))
	mux.HandleFunc("GET /api/sessions/{id}/brakeman/request", s.handleSessionBrakemanRequest)
	mux.HandleFunc("POST /api/sessions/{id}/brakeman/brief", s.handleSessionBrakemanBrief)
}

// brakemanState is GET /api/brakeman's answer.
type brakemanState struct {
	Config     brakeman.Config    `json:"config"`
	Current    *brakeman.Request  `json:"current"`
	Queue      []brakeman.Request `json:"queue"`
	TurnsToday int                `json:"turnsToday"`
	// ReadOnly says another server owns Brakeman's records, with why;
	// Stuck that the last turn's run did not stop.
	ReadOnly bool   `json:"readOnly,omitempty"`
	Why      string `json:"why,omitempty"`
	Stuck    bool   `json:"stuck,omitempty"`
}

// handleBrakeman answers {config, current, queue, turnsToday}: the
// configuration, the request holding the slot (null for none), the
// queued requests oldest first and the turns charged to today.
func (s *Server) handleBrakeman(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	cfg, err := d.st.Config()
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	out := brakemanState{Config: cfg, Queue: []brakeman.Request{}}
	out.ReadOnly, out.Why = d.st.ReadOnly()
	d.mu.Lock()
	queue := slices.Clone(d.queue)
	var cur string
	if d.turn != nil {
		cur = d.turn.id
	}
	out.Stuck = d.stuck
	d.mu.Unlock()
	if cur != "" {
		if req, err := d.st.Get(cur); err == nil {
			out.Current = &req
		}
	}
	for _, id := range queue {
		if req, err := d.st.Get(id); err == nil {
			out.Queue = append(out.Queue, req)
		}
	}
	if out.TurnsToday, err = d.st.TurnsToday(d.now()); err != nil {
		s.brakemanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// brakemanConfigBody is PUT /api/brakeman/config's body: the rev the
// configuration was read at and the configuration (its own rev and
// updatedAt are ignored).
type brakemanConfigBody struct {
	Rev    int64           `json:"rev"`
	Config brakeman.Config `json:"config"`
}

// handleBrakemanConfig saves the configuration: {rev, config} → {config};
// 409 stale_revision for a rev not the stored one; 400 invalid_config
// with the field and its words. Turning Brakeman off cancels the turn in
// progress and the queue.
func (s *Server) handleBrakemanConfig(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	var body brakemanConfigBody
	if err := decodeJSONLimit(w, r, &body, brakeman.MaxConfigDoc); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", brakeman.MaxConfigDoc>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	c := body.Config
	c.Rev, c.UpdatedAt = body.Rev, time.Time{}
	saved, err := d.st.SaveConfig(c, brakemanFacts{s: s})
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if !saved.Enabled {
		d.turnOff()
	}
	d.poke()
	writeJSON(w, http.StatusOK, map[string]any{"config": saved})
}

// brakemanAskBody is POST /api/brakeman/requests's body.
type brakemanAskBody struct {
	OpID     string         `json:"opId"`
	Kind     string         `json:"kind"`
	Subjects []brakeman.Ref `json:"subjects,omitempty"`
	Note     string         `json:"note,omitempty"`
	Since    time.Time      `json:"since,omitzero"`
	By       string         `json:"by,omitempty"`
}

// handleBrakemanAsk keeps a request: 201 {request}; 200 for the same opId
// and content, 409 op_conflict for other content (whatever else stands);
// 409 brakeman_off; 503 brakeman_stuck; 429 queue_full (8 waiting); 429
// budget_spent (the day's turns, those queued counted); 400 for a bad
// kind, subject or note.
func (s *Server) handleBrakemanAsk(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	var body brakemanAskBody
	if err := decodeJSONLimit(w, r, &body, brakeman.MaxNote+8<<10); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", (brakeman.MaxNote+8<<10)>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	in := brakeman.Request{OpID: body.OpID, Kind: body.Kind, Subjects: body.Subjects, Note: body.Note, Since: body.Since, By: body.By}
	// A repeat answers its request whatever stands since.
	if _, found, err := d.st.ByOp(body.OpID); err == nil && found {
		req, _, err := d.st.Create(in)
		if err != nil {
			s.brakemanError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"request": req})
		return
	}
	cfg, err := d.st.Config()
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if !cfg.Enabled {
		writeError(w, http.StatusConflict, "brakeman_off", "Brakeman is off: turn it on in Settings")
		return
	}
	// The queue's bound and the day's budget are held under the desk's
	// mutex with the creation, so two requests at once never pass them.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stuck {
		writeError(w, http.StatusServiceUnavailable, "brakeman_stuck", "the last turn's run did not stop: stop it from its run page")
		return
	}
	if len(d.queue) >= brakemanQueue {
		writeError(w, http.StatusTooManyRequests, "queue_full", fmt.Sprintf("%d requests wait already", brakemanQueue))
		return
	}
	turns, err := d.st.TurnsToday(d.now())
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if turns+len(d.queue) >= cfg.Budget.TurnsPerDay {
		writeError(w, http.StatusTooManyRequests, "budget_spent", fmt.Sprintf("today's %d turns are spent, those queued counted", cfg.Budget.TurnsPerDay))
		return
	}
	req, created, err := d.st.Create(in)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		d.queue = append(d.queue, req.ID)
		s.events.brakeman(req.ID, req.State)
		d.poke()
	}
	writeJSON(w, status, map[string]any{"request": req})
}

// handleBrakemanRequests answers a page of requests, newest first:
// {requests, next}; ?after= the id to read after, ?limit= at most 100.
func (s *Server) handleBrakemanRequests(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	dq, err := readQuery(r, "after", "limit")
	q := url.Values(dq)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	limit := brakeman.MaxPage
	if v := q.Get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit < 1 || limit > brakeman.MaxPage || fmt.Sprint(limit) != v {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit is 1 to %d", brakeman.MaxPage))
			return
		}
	}
	after := q.Get("after")
	if after != "" && !brakemanIDPattern(after) {
		writeError(w, http.StatusBadRequest, "invalid_request", "after is a request's id (br-<n>)")
		return
	}
	page, next, err := d.st.List(after, limit)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if page == nil {
		page = []brakeman.Request{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": page, "next": next})
}

// brakemanIDPattern reports whether id is a request's id.
func brakemanIDPattern(id string) bool {
	if !strings.HasPrefix(id, "br-") || len(id) > 21 {
		return false
	}
	n := id[3:]
	if n == "" || n[0] == '0' {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// handleBrakemanRequest answers a request whole: {request, brief} (brief
// null until one is kept), at most maxBrakemanRequestReply bytes.
func (s *Server) handleBrakemanRequest(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !brakemanIDPattern(id) {
		writeError(w, http.StatusNotFound, "not_found", "no such request")
		return
	}
	req, err := d.st.Get(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	out := map[string]any{"request": req, "brief": nil}
	if req.HasBrief {
		b, _, err := d.st.Brief(id)
		if err != nil {
			s.brakemanError(w, err)
			return
		}
		out["brief"] = b
	}
	writeJSON(w, http.StatusOK, out)
}

// handleBrakemanRefs answers a brief's resolved refs: {refs}, each with
// what the turn read (seen) and what it was as the brief was kept (now),
// at most brakeman.MaxRefsDoc bytes; 404 no_brief before one is kept.
func (s *Server) handleBrakemanRefs(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !brakemanIDPattern(id) {
		writeError(w, http.StatusNotFound, "not_found", "no such request")
		return
	}
	req, err := d.st.Get(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if !req.HasBrief {
		writeError(w, http.StatusNotFound, "no_brief", "the request has no brief yet")
		return
	}
	_, refs, err := d.st.Brief(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"refs": refs})
}

// brakemanCancelBody is POST …/cancel's body.
type brakemanCancelBody struct {
	OpID string `json:"opId"`
}

// handleBrakemanCancel cancels a request: {opId} → {request}; a queued
// one leaves the queue, a starting or running one has its run stopped
// after the write; a closed one answers as it stands.
func (s *Server) handleBrakemanCancel(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !brakemanIDPattern(id) {
		writeError(w, http.StatusNotFound, "not_found", "no such request")
		return
	}
	var body brakemanCancelBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !dispatch.OpIDPattern.MatchString(body.OpID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "opId is 1 to 32 of A-Z, a-z, 0-9, . _ ~ -")
		return
	}
	req, err := d.st.Get(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if req.Open() {
		by := "a person cancelled it"
		if req, err = d.closeTurn(id, brakeman.StateCancelled, by); err != nil {
			s.brakemanError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": req})
}

// brakemanRun answers 409 brakeman_run for a Brakeman turn's run, which
// is never resumed, takes no member and is never linked to a ticket, and
// reports whether it did.
func (s *Server) brakemanRun(w http.ResponseWriter, runID string) bool {
	run, ok := s.runs.Get(runID)
	if !ok && s.records != nil {
		run, ok, _ = s.records.Get(runID)
	}
	if !ok || run.Class != crew.ClassBrakeman {
		return false
	}
	writeError(w, http.StatusConflict, "brakeman_run", crew.ErrBrakemanRun.Error())
	return true
}

// brakemanCannot answers 403 brakeman_cannot for a Brakeman turn's
// session on a route that would schedule (form a crew, add a member) or
// reach a ticket, and reports whether it did.
func brakemanCannot(w http.ResponseWriter, d session.Driver) bool {
	if ref := d.Info().Crew; ref == nil || ref.Brakeman == nil {
		return false
	}
	writeError(w, http.StatusForbidden, "brakeman_cannot", "Brakeman's session forms no crew, adds no member and reaches no ticket")
	return true
}

// brakemanFacts answers internal/brakeman's Facts over the catalog, the
// adapters and the server's folders.
type brakemanFacts struct{ s *Server }

func (f brakemanFacts) Agent(id string) (brakeman.AgentFacts, bool) {
	f.s.catalogMu.Lock()
	hidden := slices.Contains(f.s.overlay.Hidden, id)
	f.s.catalogMu.Unlock()
	a, ok := f.s.Catalog().Get(id)
	if !ok {
		if hidden {
			return brakeman.AgentFacts{Hidden: true}, true
		}
		return brakeman.AgentFacts{}, false
	}
	ad, ok := agents.Get(a.Adapter)
	if !ok {
		return brakeman.AgentFacts{Hidden: hidden}, true
	}
	return brakeman.AgentFacts{
		Hidden:      hidden,
		MCP:         ad.MCP != nil && f.s.cfg.MCP(),
		ReadOnly:    ad.ReadOnlyArgs != nil,
		ReadOnlyGap: ad.ReadOnlyGap,
		Model:       ad.ModelArgs != nil,
		AllowTools:  ad.AllowTools != nil,
	}, true
}

func (f brakemanFacts) CheckAccount(dir string) error { return checkAccountDir(dir) }

func (f brakemanFacts) CheckCwd(dir string) error {
	_, err := f.s.resolveCwd(dir)
	return err
}

func (f brakemanFacts) CheckCrew(body json.RawMessage) error {
	var in crewInput
	if err := store.DecodeStrict(body, &in); err != nil {
		return err
	}
	c := crew.Crew{Name: strings.TrimSpace(in.Name), Goal: in.Goal, Cwd: in.Cwd, Where: in.Where, Isolation: in.Isolation,
		OpenAfterLaunch: in.OpenAfterLaunch, ViewLinkTTLSeconds: in.ViewLinkTTLSeconds, Yolo: in.Yolo, Members: in.Members, Tasks: in.Tasks, Setup: in.Setup,
		Manifest: in.Manifest}
	if err := c.ValidateSave(); err != nil {
		return err
	}
	return c.CheckAgents(f.s.Catalog())
}

// brakemanLaunch is what Server.Launch adds for a Brakeman turn's member,
// known from the spec (its class), never from the crew's id or goal: the
// turn holding the slot must be its request (409 not_the_turn otherwise);
// its arguments are Conductor's own; yolo is off whatever the server's
// default says; the session is named "Brakeman · br-n", the label Needs
// you shows its questions by; CONDUCTOR_BRAKEMAN names the request, set
// as Conductor's own names are, unfiltered; the account is the adapter's
// ConfigDirEnv; and the session's CrewRef carries the brakeman mark from
// its creation (quiet false until BK3's background turns).
func (s *Server) brakemanLaunch(spec crew.LaunchSpec, req *createSessionRequest, ref *session.CrewRef) *apiError {
	if s.brakeman == nil {
		return newAPIError(http.StatusServiceUnavailable, "store_unavailable", "Brakeman needs a data directory")
	}
	account, ok := s.brakeman.turnLaunch(spec.Request)
	if !ok {
		return newAPIError(http.StatusConflict, "not_the_turn", "no Brakeman turn holds the slot for "+spec.Request)
	}
	no := false
	req.Yolo, req.conductorArgs = &no, true
	req.Name = "Brakeman · " + spec.Request
	req.inject = maps.Clone(req.inject)
	if req.inject == nil {
		req.inject = map[string]string{}
	}
	req.inject["CONDUCTOR_BRAKEMAN"] = spec.Request
	if account != "" {
		a, ok := s.Catalog().Get(spec.AgentID)
		ad, known := agents.Get(a.Adapter)
		if !ok || !known || ad.ConfigDirEnv == "" {
			return newAPIError(http.StatusBadRequest, "invalid_agent", "the agent takes no account folder")
		}
		req.Env = maps.Clone(req.Env)
		if req.Env == nil {
			req.Env = map[string]string{}
		}
		req.Env[ad.ConfigDirEnv] = account
	}
	ref.Brakeman = &session.BrakemanMark{Request: spec.Request}
	return nil
}

// newLocalSeam, when a test sets it, makes a member's session in place
// of createLocalSession: a session.Local over a process double.
var newLocalSeam atomic.Pointer[func(s *Server, req createSessionRequest, ref *session.CrewRef) (*session.Local, *apiError)]

// newLocal is createLocalSession, or the test's seam.
func (s *Server) newLocal(req createSessionRequest, ref *session.CrewRef) (*session.Local, *apiError) {
	if f := newLocalSeam.Load(); f != nil {
		return (*f)(s, req, ref)
	}
	return s.createLocalSession(req, ref)
}

// quietSession reports whether the session's questions page no one: a
// Brakeman turn's marked quiet (a background turn's, BK3).
func quietSession(info session.Info) bool {
	return info.Crew != nil && info.Crew.Brakeman != nil && info.Crew.Brakeman.Quiet
}
