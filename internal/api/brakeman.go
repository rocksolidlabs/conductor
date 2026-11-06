package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
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
	// brakemanFirstRead is how long a turn's agent has, from the turn's
	// start, to read its request (its first read, the request route with
	// its own token) before the turn is ended as one whose agent did not
	// start (notStarted), whatever its screen shows: a sign-in, an update
	// dialog, a question Conductor does not know. A real agent reads
	// within two minutes of its start (Claude Code in seconds; Codex,
	// its MCP servers started first, about 90 s at #659's live check),
	// so five minutes is past every start seen with room for a loaded
	// machine, and a third of the default 15-minute turn: a turn held at
	// a screen gives most of its minutes back. A turn shorter than it is
	// judged so at its deadline.
	brakemanFirstRead = 5 * time.Minute
	// brakemanScreenWords bounds the screen's lines a turn whose agent
	// did not start carries in its words: its last lines not blank, at
	// most this many bytes together.
	brakemanScreenWords = 220
	// maxBrakemanLine bounds the line typed into a turn's session.
	maxBrakemanLine = 300
	// maxStampVersion bounds the agent's version a request's stamp
	// takes; a longer one is left out.
	maxStampVersion = 128
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
	// hostRef is the ext.Host the desk launches through, the one the
	// composition root gave it (SetBrakemanHost); nil until then, the
	// server's own desk Host (BrakemanHost) answering.
	hostRef atomic.Pointer[ext.Host]
	// clock is the desk's clock and its store's: time.Now, a test's own
	// otherwise.
	clock atomic.Pointer[func() time.Time]
	// wake makes the loop look again; done ends it, and loopDone says it
	// has ended.
	wake     chan struct{}
	done     chan struct{}
	loopDone chan struct{}
	stopOnce sync.Once
	// startOnce starts the loop (start), or, for a desk closed before it
	// started, says there is none to wait for.
	startOnce sync.Once
	// steps counts the loop's passes, for a test to wait on one.
	steps atomic.Uint64
	// afterLaunch, when a test sets it, runs once launchTurn has returned
	// and before its return is written: the gate inside the launch seam;
	// beforeLaunch runs in launchTurn once the turn's crew is made and
	// before the engine is asked for its run. Each desk's own, so tests
	// that gate run beside each other.
	afterLaunch, beforeLaunch atomic.Pointer[func(requestID string)]
	// beforeRecover runs as the store becomes this server's (OnOwned),
	// before the queue is read again; beforeAdmit in an ask before it
	// takes admit; beforeCheck in a brief's route before it checks a new
	// brief, beforeKeep once its refs are resolved and before it takes
	// admit to keep it; afterWrite in update
	// once a change of state is written and before its event is sent.
	// Each a test's gate, nil otherwise.
	beforeRecover, beforeAdmit atomic.Pointer[func()]
	beforeCheck, beforeKeep    atomic.Pointer[func(requestID string)]
	afterWrite                 atomic.Pointer[func(requestID, state string)]
	// recovered says the desk holds the records' queue as their owner: set
	// once the store is this server's and the queue is read from it (at
	// open for a store that took the owner lock then, in OnOwned's
	// function otherwise). Until then the desk is not the owner, even
	// while the store already writes: a save, an ask and a dequeue are
	// refused (ErrNotOwner), so none acts on the queue as this server saw
	// it before the last owner let go.
	recovered atomic.Bool
	// emit orders the brakeman events: each is sent under it with the
	// state the records hold as it is sent (publish), so the last event a
	// request's state change sends is its state now, whichever writer
	// sends it last.
	emit sync.Mutex
	// closing is set as the server shuts down (quiesce), under admit: no
	// request's state is written by the desk from then on, so the turn in
	// progress stays starting or running and the next start finds it
	// interrupted, and no configuration is saved (a save that turned
	// Brakeman off could cancel nothing, and the queue would launch once
	// Brakeman is on again).
	closing atomic.Bool
	// launches counts the turns' launches outstanding, which quiesce
	// waits for.
	launches sync.WaitGroup
	// acting counts the acts whose operation has begun (beginAct), which
	// quiesce waits for too, so a run an act launches is one the server's
	// stops find. Added to under mu, with closing read there: no act
	// begins its operation once quiesce began.
	acting sync.WaitGroup
	// pruneDue says the requests are pruned at the loop's next pass
	// (Store.Prune): at the start and after each turn.
	pruneDue atomic.Bool
	// keep is how many requests a prune keeps past the open and pinned
	// ones, brakeman.Keep; a test lowers it.
	keep atomic.Int64

	// admit orders admission (an ask), a configuration's save and the
	// dequeue: an ask either lands before Brakeman is turned off, and is
	// cancelled with the queue, or reads it off and is refused; a dequeue
	// never starts a turn after the save that turned Brakeman off.
	// Taken before mu.
	admit sync.Mutex

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
	// makes holds each request's make mutex (makeLock), completing each
	// item's completion lock (completionLock); turnReads what the turn
	// in progress was given of what changed (cover) and the classes its
	// answers used (usedClass).
	makes      map[string]*sync.Mutex
	completing map[string]chan struct{}
	turnReads  *turnChanges

	// readMu orders a turn's reads: the count, the read log and the
	// coverage of one read are written before the next is answered.
	readMu sync.Mutex
	// recovering holds Brakeman's routes (503 brakeman_recovering) while
	// recoverMakes completes what the last owner left of the makes.
	recovering atomic.Bool
	// recoveries is the recovery the desk's start began (startRecovery),
	// which release waits for before the owner lock goes;
	// recoverCtx/stopRecovery end every recovery as the desk quiesces.
	recoveries   sync.WaitGroup
	recoverCtx   context.Context
	stopRecovery context.CancelFunc
	// role is BK3's background role (brakeman_role.go), src what its
	// sources noted since its last pass; roleDone says its loop ended.
	role     *crew.Role
	src      *roleSources
	roleDone chan struct{}
	// roleState is the role's state as its records last read or wrote it
	// (roleUnknown, roleRunning, rolePausedState), and pausing counts the
	// pauses a person asked that are not yet answered, marked under admit
	// before the pause is written: the dequeue holds a wake request in the
	// queue while either says paused (roleHolds), so no wake turn starts
	// once a person has paused the role.
	roleState atomic.Int32
	pausing   atomic.Int32
	// making counts the makes the make route admitted (admitMake), which
	// release waits for before the owner lock goes: a make admitted before
	// the desk closed finishes its backend's operation here, never beside
	// the next owner's recovery of the same claim. Added to under mu, with
	// closing read there, so none is admitted once quiesce began.
	making sync.WaitGroup
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
	// registered are the member sessions the launch registered and told
	// the desk of (memberStarted), by id: one that ended before it is
	// left to memberStarted (sessionChanged).
	registered map[string]bool
	// stopping says a stop is under way; nextStop is when the next may
	// start (a retry's wait).
	stopping bool
	nextStop time.Time
	// agentSHA256 is the agent's catalog entry the turn's arguments were
	// made for (agentSelection): its session starts only from that entry
	// (createLocalSession's agent_changed otherwise).
	agentSHA256 string
	// started is when the turn's launch returned (its agent's process
	// made), on the desk's clock: its agent reads its request by
	// started+brakemanFirstRead, or the turn ends as one whose agent did
	// not start (firstReadBy). Zero while it launches: the deadline covers
	// starting, its run then the request's stray run.
	started time.Time
}

// firstReadBy is when a turn of req whose launch returned at started ends
// as one whose agent did not start, unless it has read its request:
// brakemanFirstRead after started, or its deadline when that comes first.
func firstReadBy(req brakeman.Request, started time.Time) time.Time {
	by := started.Add(brakemanFirstRead)
	if !req.Deadline.IsZero() && req.Deadline.Before(by) {
		by = req.Deadline
	}
	return by
}

// openBrakemanDesk opens the desk over the data directory st: its store
// and its owner lock (read-only while another server holds it, the desk
// taking no turn until this one does) and its queue. Its loop starts
// with start, once the server is made and listening. nil on a switchyard
// and when the store cannot be opened.
func (s *Server) openBrakemanDesk(st *store.Store) *brakemanDesk {
	d := &brakemanDesk{s: s, wake: make(chan struct{}, 1), done: make(chan struct{}), loopDone: make(chan struct{}), roleDone: make(chan struct{}), read: map[string]bool{}}
	d.recoverCtx, d.stopRecovery = context.WithCancel(context.Background())
	now := time.Now
	d.clock.Store(&now)
	d.keep.Store(brakeman.Keep)
	d.pruneDue.Store(true)
	bs, err := brakeman.Open(st, brakeman.Options{Now: d.now, DefaultCwd: s.cfg.DefaultCwd, Log: s.log})
	if err != nil {
		s.log.Warn("Brakeman is off: its records could not be opened", "err", err)
		return nil
	}
	if ro, why := bs.ReadOnly(); ro {
		s.log.Warn("Brakeman takes no turn: its records are read-only", "why", why)
	}
	d.st = bs
	d.openRole()
	// A store that took the owner lock as it opened is owned from here:
	// its queue, read below, is the records'. One that waits for the lock
	// is owned once OnOwned's function has read the queue again, under
	// admit, so no save, ask or dequeue falls between the store becoming
	// writable and the queue being the records'.
	ownedAtOpen := false
	if ro, _ := bs.ReadOnly(); !ro {
		ownedAtOpen = true
	}
	bs.OnOwned(func() {
		if f := d.beforeRecover.Load(); f != nil {
			(*f)()
		}
		// The makes the last owner left are completed first, Brakeman's
		// routes held meanwhile (recover).
		d.recover()
		d.admit.Lock()
		d.loadQueue()
		// The role takes up the record the last owner left, never the one
		// read while this server waited (the routes read it afresh until
		// recovered: roleRecord).
		d.role.Forget()
		d.recovered.Store(true)
		d.admit.Unlock()
		d.poke()
		d.role.Poke()
	})
	d.loadQueue()
	if ownedAtOpen {
		d.recovered.Store(true)
		// Brakeman's routes wait for the recovery of the makes (it reads
		// Dispatch and the crew store, opened before the desk), when the
		// last owner left one to recover.
		if pending, err := bs.Pending(); err != nil || len(pending) > 0 {
			d.startRecovery()
		}
	}
	return d
}

// startRecovery runs the recovery of the makes in the background, with
// Brakeman's routes held until it is done; release waits for it, so the
// owner lock never goes while it saves a crew or makes a ticket.
func (d *brakemanDesk) startRecovery() {
	d.recovering.Store(true)
	d.recoveries.Add(1)
	go func() {
		defer d.recoveries.Done()
		d.recover()
	}()
}

// notOwner is why the desk refuses a write before it holds the records'
// queue as their owner (recovered): the store's own words while it is
// read-only, else that the last owner's queue is still being read.
func (d *brakemanDesk) notOwner() error {
	if ro, why := d.st.ReadOnly(); ro {
		return fmt.Errorf("%w: %s", brakeman.ErrNotOwner, why)
	}
	return fmt.Errorf("%w: the last owner's queue is still being read", brakeman.ErrNotOwner)
}

// BrakemanHost is the ext.Host the server gives Brakeman's desk: the one
// a provider's Start gets, but for the desk, so its LaunchTurn launches the
// turn of the request holding the desk's slot, and the operations it runs
// for a turn are the actor brakeman's. It lasts as long as the server.
func (s *Server) BrakemanHost() ext.Host {
	return &extHost{s: s, name: brakemanMember, ctx: context.Background(), desk: true}
}

// SetBrakemanHost gives Brakeman's desk the ext.Host it launches through:
// the composition root's (runServe gives it BrakemanHost), a test's own
// otherwise. Without one the desk launches through BrakemanHost. Nothing
// on a server without a desk.
func (s *Server) SetBrakemanHost(h ext.Host) {
	if s.brakeman != nil && h != nil {
		s.brakeman.hostRef.Store(&h)
	}
}

// host is the ext.Host the desk launches through.
func (d *brakemanDesk) host() ext.Host {
	if h := d.hostRef.Load(); h != nil {
		return *h
	}
	return d.s.BrakemanHost()
}

// StartBrakeman starts Brakeman's desk: its loop, which takes the queued
// requests a restart finds. conductor serve calls it once the server is
// made and its listener bound (cfg.PublicURL then has the listener's
// port, which a turn's session is given to call back on), so the first
// turn is launched by a desk the server holds. A server never started
// takes no turn; one with no desk (a switchyard, no data directory) does
// nothing.
func (s *Server) StartBrakeman() {
	if s.brakeman != nil {
		s.brakeman.start()
	}
}

// start starts the loop and the role's, once; never after the desk
// closed.
func (d *brakemanDesk) start() {
	d.startOnce.Do(func() {
		go d.loop()
		go d.roleLoop()
	})
}

// now is the desk's clock.
func (d *brakemanDesk) now() time.Time { return (*d.clock.Load())() }

// errDeskClosing refuses a write of the desk's once the server is
// shutting down (quiesce).
var errDeskClosing = errors.New("brakeman: the server is shutting down")

// quiesce takes the desk out of the server as it shuts down, before its
// runs are stopped: no request's state is written by the desk from then
// on (the turn in progress stays starting or running in the records, and
// the next start finds it interrupted), the loop ends, no act begins its
// operation (beginAct), and the launch outstanding is cancelled and
// waited for, with every act whose operation has begun, as long as ctx
// allows, so the runs and the sessions they make are ones the stops after
// it find. It may be called again.
func (d *brakemanDesk) quiesce(ctx context.Context) {
	// In admission's order: a save before it has written its
	// cancellations, one after it is refused. Under mu too, as admitMake
	// reads it: a make is admitted before this, and release waits for
	// it, or refused after.
	d.admit.Lock()
	d.mu.Lock()
	d.closing.Store(true)
	d.mu.Unlock()
	d.admit.Unlock()
	// A recovery under way stops at its next make; the one it is in
	// finishes (release waits for it).
	d.stopRecovery()
	d.stopOnce.Do(func() { close(d.done) })
	d.startOnce.Do(func() {
		close(d.loopDone)
		close(d.roleDone)
	})
	<-d.loopDone
	<-d.roleDone
	d.mu.Lock()
	if t := d.turn; t != nil && t.cancel != nil {
		t.cancel()
	}
	d.mu.Unlock()
	launched := make(chan struct{})
	go func() {
		d.launches.Wait()
		d.acting.Wait()
		close(launched)
	}()
	select {
	case <-launched:
	case <-ctx.Done():
	}
}

// release closes the store, which lets the owner lock go, only once the
// turn in progress has no launch outstanding and every member process it
// made is reaped (drain): another server takes no queued request while
// one of this server's turns could still start or still runs. It waits
// for that as long as ctx allows; past it, Shutdown goes on and the desk
// keeps the records until the drain is done, the lock going with the
// process at the latest. It quiesces the desk first when nothing did.
func (d *brakemanDesk) release(ctx context.Context) {
	d.quiesce(ctx)
	released := make(chan struct{})
	go func() {
		defer close(released)
		d.drain()
		// The recovery the start began holds the lock's promise as a
		// make's does: it ends before the lock goes (the one OnOwned
		// began, Close waits for); so does every make the route
		// admitted, whose backend's operation the next owner's recovery
		// would otherwise run beside it.
		d.recoveries.Wait()
		d.making.Wait()
		_ = d.st.Close()
	}()
	select {
	case <-released:
	case <-ctx.Done():
		d.s.log.Warn("Brakeman keeps its records until its last turn's launch returns and its processes are reaped")
	}
}

// drain waits for the turn's launch outstanding (a session it makes late
// is the turn's, kept in its locals), then stops each member session
// whose process is not reaped (Shutdown has stopped those the registry
// lists; one it no longer lists, or one whose stop failed, is stopped
// here), again every brakemanRetry, until every one is reaped.
func (d *brakemanDesk) drain() {
	d.launches.Wait()
	for {
		d.mu.Lock()
		var locals []*session.Local
		if d.turn != nil {
			locals = slices.Clone(d.turn.locals)
		}
		d.mu.Unlock()
		live := liveLocals(locals)
		if len(live) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), brakemanStopWait)
		for _, l := range live {
			if err := l.Stop(ctx); err != nil {
				d.s.log.Warn("Brakeman's turn's session did not stop as the server stopped", "session", l.Info().ID, "err", err)
			}
		}
		cancel()
		if live = liveLocals(live); len(live) == 0 {
			return
		}
		retry := time.NewTimer(brakemanRetry)
		select {
		case <-live[0].ProcessDone():
		case <-retry.C:
		}
		retry.Stop()
	}
}

// admitMake admits a make of the make route, false once the desk is
// closing (quiesce): release waits for each admitted make (madeDone)
// before the owner lock goes.
func (d *brakemanDesk) admitMake() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing.Load() {
		return false
	}
	d.making.Add(1)
	return true
}

// madeDone ends an admitted make.
func (d *brakemanDesk) madeDone() { d.making.Done() }

// beginAct lets an admitted act, written pending, run its operation:
// false once the desk is closing (quiesce), when it runs nothing and its
// entry stays pending for the next start's recovery. quiesce waits for
// each act begun (actDone) before the server's runs are stopped.
func (d *brakemanDesk) beginAct() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing.Load() {
		return false
	}
	d.acting.Add(1)
	return true
}

// actDone ends an act begun.
func (d *brakemanDesk) actDone() { d.acting.Done() }

// poke makes the loop look again.
func (d *brakemanDesk) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// loadQueue reads the queued requests from the store, oldest first, all
// under the desk's mutex, which an ask holds while it creates its request
// and joins the queue: one kept as the store becomes this server's
// (OnOwned) is either read here or joins the queue after it, never lost
// in between.
func (d *brakemanDesk) loadQueue() {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	d.queue = queued
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
	if ro, _ := d.st.ReadOnly(); ro || !d.recovered.Load() {
		return 0
	}
	d.mu.Lock()
	t := d.turn
	d.mu.Unlock()
	if t != nil {
		return d.tend(t)
	}
	if d.pruneDue.CompareAndSwap(true, false) {
		d.prune()
	}
	return d.dequeue()
}

// prune keeps the newest requests (keep, brakeman.Keep) and every open or
// pinned one, and removes the rest with their documents: at the loop's
// first pass as the owner, then after each turn, so a turn's history
// never grows past it.
func (d *brakemanDesk) prune() {
	n, err := d.st.PruneKeeping(int(d.keep.Load()))
	if err != nil {
		d.s.log.Warn("Brakeman's old requests could not be pruned", "err", err)
		return
	}
	if n > 0 {
		d.s.log.Info("Brakeman's old requests pruned", "removed", n)
	}
}

// tend closes the turn at its deadline, stops its run once its request is
// closed (a brief's after the grace), retries a stop that left a process,
// and gives the slot up once the launch has returned and every member's
// process is reaped. Giving it up sends the request's brakeman event once
// more, after the slot is free: a page that read GET /api/brakeman while
// the closed request still held it reads again and sees it go.
func (d *brakemanDesk) tend(t *deskTurn) time.Duration {
	released := false
	defer func() {
		if released {
			d.publish(t.id)
		}
	}()
	now := d.now()
	req, err := d.st.Get(t.id)
	if err != nil {
		d.s.log.Warn("Brakeman's turn could not be read", "request", t.id, "err", err)
		return brakemanRetry
	}
	// An agent that has not read its request by its bound did not start
	// (signed out, held at a screen): the turn ends, failed, with what its
	// screen shows, before the deadline's time-out would read it as a turn
	// that ran and charge all its minutes.
	d.mu.Lock()
	read := d.read[t.id]
	d.mu.Unlock()
	d.mu.Lock()
	started := t.started
	d.mu.Unlock()
	if req.Open() && !read && !started.IsZero() && !now.Before(firstReadBy(req, started)) {
		why := d.notStarted(t, firstReadBy(req, started).Sub(started))
		req, err = d.update(t.id, func(r *brakeman.Request) error {
			if !r.Open() {
				return nil
			}
			return r.To(brakeman.StateFailed, why, now)
		})
		if err != nil {
			d.s.log.Warn("Brakeman's turn whose agent did not start could not be ended", "request", t.id, "err", err)
			return brakemanRetry
		}
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
	if req.Open() {
		if why := d.heldAtStartup(t, req); why != "" {
			req, err = d.update(t.id, func(r *brakeman.Request) error {
				if !r.Open() {
					return nil
				}
				return r.To(brakeman.StateFailed, why, now)
			})
			if err != nil {
				d.s.log.Warn("Brakeman's turn held at its agent's startup could not be ended", "request", t.id, "err", err)
				return brakemanRetry
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if req.Open() {
		next := req.Deadline
		if !d.read[t.id] && !t.started.IsZero() {
			next = firstReadBy(req, t.started)
		}
		return untilPositive(next, now)
	}
	if t.cancel != nil {
		t.cancel()
	}
	live := liveLocals(t.locals)
	if !t.launching && len(live) == 0 && !t.stopping {
		d.turn, d.stopFails, d.stuck = nil, 0, false
		d.pruneDue.Store(true)
		d.poke()
		released = true
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

// heldAtStartup says why the turn t, which has not read its request,
// cannot go on: its agent shows a startup question (the catalog's trust
// question or its other startup screens, raised as a session's question
// with source trust: Local.fireTrust) that the turn may not pass, and
// which holds the turn's line, as every startup question holds a
// submission, so the agent would wait there until the turn's minutes
// end. One with no answers is a screen no answer of Conductor's passes
// (Claude Code's sign-in, waiting for a browser or a pasted code): every
// turn ends at it. One with answers (a trust question, an update dialog)
// ends a background turn, whose questions page no one; a person's own
// turn shows it under Needs you, to be answered there. No key is typed
// into either: the turn ends and its run is stopped. "" for a turn that
// read its request, or whose agent shows no such question.
func (d *brakemanDesk) heldAtStartup(t *deskTurn, req brakeman.Request) string {
	d.mu.Lock()
	read := d.read[t.id]
	locals := liveLocals(t.locals)
	d.mu.Unlock()
	if read {
		return ""
	}
	for _, l := range locals {
		att := l.Info().Attention
		if att.State != session.AttentionNeedsInput || att.Source != session.SourceTrust {
			continue
		}
		words := att.Message
		var why string
		switch {
		case len(att.Options) == 0:
			why = fmt.Sprintf("the agent waits at a screen no answer of Conductor's passes, its login refused (%q): sign it in from a session of its own, or set another account in Settings; Brakeman tries again by itself, or ask it now", words)
		case req.Kind == brakeman.KindWake:
			why = fmt.Sprintf("the agent shows a startup question a background turn may not answer (%q): answer it in a session of that agent; Brakeman tries again by itself, or ask it now", words)
		default:
			continue
		}
		for len(why) > brakeman.MaxWhy {
			_, n := utf8.DecodeLastRuneInString(why)
			why = why[:len(why)-n]
		}
		return why
	}
	return ""
}

// notStarted is the words of the turn t whose agent did not read its
// request within waited: what its screen shows (screenWords), or that no
// session of it was started. No key is typed into the screen: the turn
// ends and its run is stopped.
func (d *brakemanDesk) notStarted(t *deskTurn, waited time.Duration) string {
	d.mu.Lock()
	locals := liveLocals(t.locals)
	d.mu.Unlock()
	shown := "no session of it had started"
	if len(locals) > 0 {
		if lines := screenWords(locals[0]); lines != "" {
			shown = fmt.Sprintf("its screen shows %q", lines)
		} else {
			shown = "its screen is blank"
		}
	}
	mins := max(1, int(waited.Round(time.Minute)/time.Minute))
	why := fmt.Sprintf("the agent did not read its request in %d minutes (signed out, or held at a screen Conductor does not know?); %s: sign it in or answer it in a session of that agent; Brakeman tries again by itself, or ask it now", mins, shown)
	for len(why) > brakeman.MaxWhy {
		_, n := utf8.DecodeLastRuneInString(why)
		why = why[:len(why)-n]
	}
	return why
}

// screenLinks are the links a screen read shows, left out of a turn's
// words: a sign-in screen's carries a one-time state.
var screenLinks = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://\S+`)

// screenWords is what l's screen shows, for the words of a turn whose
// agent did not start: its last lines not blank, links left out, joined
// with " | ", the oldest dropped past brakemanScreenWords bytes. It reads
// the session's screen as the screen read does (TailLines; ScreenLines,
// the rows a terminal shows, once #695 is on main: this call is the one
// line to change).
func screenWords(l *session.Local) string {
	var lines []string
	for _, line := range l.TailLines(12) {
		line = strings.Join(strings.Fields(screenLinks.ReplaceAllString(line, "[link]")), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	out := strings.Join(lines, " | ")
	for len(out) > brakemanScreenWords && len(lines) > 1 {
		lines = lines[1:]
		out = strings.Join(lines, " | ")
	}
	for len(out) > brakemanScreenWords {
		_, n := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-n]
	}
	return out
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
	d.admit.Lock()
	defer d.admit.Unlock()
	if !d.recovered.Load() {
		return 0
	}
	cfg, err := d.st.Config()
	if err != nil {
		d.s.log.Warn("Brakeman's configuration could not be read", "err", err)
		return brakemanRetry
	}
	if cfg.Enabled && !cfg.Background() {
		// A wake request queued while the role ran (a restart since the
		// save that turned it off) never starts.
		d.dropWakes()
	}
	for {
		d.mu.Lock()
		if !cfg.Enabled || d.stuck || len(d.queue) == 0 || d.turn != nil || d.closing.Load() {
			d.mu.Unlock()
			return 0
		}
		queue := slices.Clone(d.queue)
		d.mu.Unlock()
		// The oldest that may start: a wake request waits while the role is
		// paused (a person's turn behind it starts), and starts once resumed.
		id := d.startable(queue)
		if id == "" {
			return 0
		}
		// The agent's entry is chosen once, here: the stamp is its, the
		// launch's probe runs it and the turn's crew is made from it, so a
		// catalog edit meanwhile is refused at the member's start
		// (agent_changed), never run under the version of the entry before.
		a := d.entry(cfg)
		req, err := d.st.Start(id, cfg, d.stamp(cfg, a))
		switch {
		case errors.Is(err, brakeman.ErrOverBudget), errors.Is(err, brakeman.ErrNotQueued), errors.Is(err, brakeman.ErrNotFound):
			d.unqueue(id)
			d.publish(id)
			continue
		case err != nil:
			d.s.log.Warn("Brakeman's turn could not start", "request", id, "err", err)
			return brakemanRetry
		}
		ctx, cancel := context.WithCancel(context.Background())
		t := &deskTurn{id: id, account: cfg.Account, launching: true, cancel: cancel, registered: map[string]bool{}}
		d.mu.Lock()
		d.queue = slices.DeleteFunc(d.queue, func(q string) bool { return q == id })
		d.turn = t
		d.mu.Unlock()
		d.publish(id)
		d.launches.Add(1)
		d.s.track(func() {
			defer d.launches.Done()
			d.launch(ctx, t, req, cfg, a)
		})
		return untilPositive(req.Deadline, d.now())
	}
}

// unqueue drops id from the queue.
func (d *brakemanDesk) unqueue(id string) {
	d.mu.Lock()
	d.queue = slices.DeleteFunc(d.queue, func(q string) bool { return q == id })
	d.mu.Unlock()
}

// entry is the configured agent's catalog entry a turn under cfg runs,
// nil when the catalog has none.
func (d *brakemanDesk) entry(cfg brakeman.Config) *catalog.Agent {
	a, ok := d.s.Catalog().Get(cfg.AgentID)
	if !ok {
		return nil
	}
	return &a
}

// stamp is the agent a turn under cfg runs from its entry a: its id,
// adapter, the version the probe cache knows of a (nothing is run for it
// here, under admit: a cold cache is probed by the turn's launch,
// probedVersion), the model and whether an account is set.
func (d *brakemanDesk) stamp(cfg brakeman.Config, a *catalog.Agent) brakeman.Stamp {
	st := brakeman.Stamp{AgentID: cfg.AgentID, Model: cfg.Model, Account: cfg.Account != ""}
	if a != nil {
		st.Adapter = a.Adapter
		if id, ok := d.s.cachedIdentity(*a); ok && id.Identified && len(id.Version) <= maxStampVersion {
			st.Version = id.Version
		}
	}
	return st
}

// probedVersion is the version the identity probe of the entry a reports:
// the cache's, else the probe's run now, waited on at most probeWait (its
// answer is kept for the next turn either way); "" when its adapter has
// no probe, the agent turns it off, or it identifies nothing in time. It
// probes a itself, never the catalog's entry of its id read again.
func (d *brakemanDesk) probedVersion(ctx context.Context, a catalog.Agent) string {
	if len(a.Command) == 0 {
		return ""
	}
	var programs []string
	if a.Probed() && agents.ProbeFor(a.Adapter) != nil {
		programs = []string{a.Command[0]}
	}
	paths := d.s.lookups.warmPaths(ctx, programs)
	id := d.s.identityOf(ctx, a, paths[a.Command[0]].path, false)
	if id == nil || !id.Identified || len(id.Version) > maxStampVersion {
		return ""
	}
	return id.Version
}

// launch launches the turn's run and writes what its return says: running
// with the run and its session, for a request still starting; nothing of
// its state for one closed meanwhile (a brief, the member's end, a
// deadline, a cancel), only an empty run or session id filled, and the
// run, when the close was a deadline, a cancel or Brakeman turned off,
// recorded as the request's stray run and stopped. A launch that fails
// closes the request failed with the engine's words.
func (d *brakemanDesk) launch(ctx context.Context, t *deskTurn, req brakeman.Request, cfg brakeman.Config, a *catalog.Agent) {
	// The stamp's version is the probe cache's, which is cold after a
	// restart until some page lists the agents: the probe is run here
	// then, as a launch's own check runs it, and its version written
	// before the member starts, while the request is starting. A request
	// closed after (a brief, the member's end, a cancel) takes no change
	// of its agent, and one closed before the launch has no turn to
	// stamp. The probe is of the entry the stamp was made from and the
	// crew is made from (a), so the version written is the launched
	// program's: an entry edited meanwhile is refused at the member's
	// start, by the entry's SHA256.
	if req.Agent.Version == "" && a != nil {
		if version := d.probedVersion(ctx, *a); version != "" {
			_, uerr := d.update(req.ID, func(r *brakeman.Request) error {
				if r.State == brakeman.StateStarting && r.Agent.Version == "" && r.Agent.AgentID == req.Agent.AgentID {
					r.Agent.Version = version
				}
				return nil
			})
			if uerr != nil && !errors.Is(uerr, errDeskClosing) {
				d.s.log.Warn("Brakeman's turn's agent version could not be written", "request", req.ID, "err", uerr)
			}
		}
	}
	tl, sha, err := d.turnRequest(req, cfg, a)
	var ref ext.RunRef
	if err == nil {
		d.mu.Lock()
		t.agentSHA256 = sha
		d.mu.Unlock()
		// The desk's one door: the Host the composition root gave it
		// (SetBrakemanHost), whose LaunchTurn is launchTurn.
		ref, err = d.host().LaunchTurn(ctx, tl)
	}
	if f := d.afterLaunch.Load(); f != nil {
		(*f)(req.ID)
	}
	runID, sessionID := "", ""
	if err == nil {
		runID = ref.ID
		// The member's session, from the engine, which holds the run it
		// launched (a run that ended meanwhile included).
		if run, ok := d.s.runs.Get(ref.ID); ok && len(run.Members) > 0 {
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
	if uerr != nil && !errors.Is(uerr, errDeskClosing) {
		d.s.log.Warn("Brakeman's turn's launch could not be written", "request", req.ID, "err", uerr)
	}
	if stray {
		d.s.log.Info("Brakeman's turn's run came back after its request closed: stopped", "request", req.ID, "run", runID)
	}
	d.mu.Lock()
	t.launching = false
	t.started = d.now()
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
	return "Brakeman request " + req.ID + " (" + req.Kind + `): read it with the brakeman_request tool or "conductor brakeman request", and submit one brief with brief_submit or "conductor brakeman brief". Call guide for what you may read, its briefs section for a brief's shape.`
}

// turnRequest is the turn a request launches, as the desk asks its Host
// for it: the configured agent, its model and whether Conductor's tools
// are allowed, the configured folder, the line, the label and the
// request. The configuration is held to Validate again against the
// catalog and the folders as they are now (an agent hidden, its adapter
// changed since the save), and the crew the Host makes of it checked
// (turnCrew) for the entry a the turn was stamped from, whose
// agentSelection's SHA256 is answered, which the member's session must
// start from.
func (d *brakemanDesk) turnRequest(req brakeman.Request, cfg brakeman.Config, a *catalog.Agent) (ext.TurnLaunch, string, error) {
	if err := brakeman.Validate(cfg, brakemanFacts{s: d.s}); err != nil {
		return ext.TurnLaunch{}, "", fmt.Errorf("the configuration no longer holds: %w", err)
	}
	if a == nil {
		return ext.TurnLaunch{}, "", fmt.Errorf("no agent %s in the catalog", cfg.AgentID)
	}
	tl := ext.TurnLaunch{
		Agent: cfg.AgentID, Model: cfg.Model, AllowTools: cfg.AllowConductorTools,
		Folder: cfg.Cwd, Line: brakemanLine(req), Label: brakemanLabel(req), Request: req.ID,
	}
	if _, err := turnCrew(*a, tl); err != nil {
		return ext.TurnLaunch{}, "", err
	}
	return tl, agentSelection(*a, false).SHA256, nil
}

// turnCrew is the crew a turn launches: one member, the agent of the
// entry a in its adapter's read-only mode, then its model and, when
// allowed, Conductor's tools, nothing a person typed; never yolo, never
// managed, in the turn's folder without a worktree, its line the member's
// prompt.
func turnCrew(a catalog.Agent, tl ext.TurnLaunch) (crew.Crew, error) {
	args, err := turnArgs(a, tl.Agent, tl.Model, tl.AllowTools)
	if err != nil {
		return crew.Crew{}, err
	}
	if len(tl.Line) > maxBrakemanLine {
		return crew.Crew{}, fmt.Errorf("the request's line is %d bytes, more than %d", len(tl.Line), maxBrakemanLine)
	}
	no := false
	return crew.Crew{
		ID: brakemanMember, Name: "Brakeman", Cwd: tl.Folder, Where: crew.WhereServer, Isolation: crew.IsolationNone,
		Goal: "Brakeman request " + tl.Request, Yolo: &no,
		Members: []crew.Member{{
			Name: brakemanMember, AgentID: tl.Agent, Args: args,
			Start: crew.Start{When: crew.StartImmediately}, Prompt: tl.Line,
		}},
	}, nil
}

// turnArgs is a turn's member's arguments for the agent's entry a: its
// adapter's ReadOnlyArgs, then ModelArgs(model), then
// AllowTools("conductor") when allowed.
func turnArgs(a catalog.Agent, agentID, model string, allowTools bool) ([]string, error) {
	ad, ok := agents.Get(a.Adapter)
	if !ok || ad.ReadOnlyArgs == nil {
		return nil, fmt.Errorf("the agent %s has no read-only mode", agentID)
	}
	args := slices.Clone(ad.ReadOnlyArgs())
	if model != "" {
		if ad.ModelArgs == nil {
			return nil, fmt.Errorf("the agent %s takes no model", agentID)
		}
		args = append(args, ad.ModelArgs(model)...)
	}
	if allowTools && ad.AllowTools != nil {
		args = append(args, ad.AllowTools("conductor")...)
	}
	return args, nil
}

// launchTurn is what the Host's LaunchTurn does, the desk's one launch
// door behind it: the folder resolved and opened through openCwd, a crew
// whose yolo is not false or whose member lacks its adapter's read-only
// arguments refused, then Engine.LaunchNamedWith with the turn's class
// and request. The run is released at once (the desk keeps it by its
// class and request).
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
	if d := s.brakeman; d != nil {
		if f := d.beforeLaunch.Load(); f != nil {
			(*f)(opts.Request)
		}
	}
	// A launch the desk cancelled meanwhile (a deadline, a cancel, the
	// server shutting down) makes no run.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	run, release, err := s.runs.LaunchNamedWith(ctx, c, label, opts)
	release()
	return run, err
}

// turnLaunch is what Server.Launch asks of the desk for a Brakeman
// turn's member (spec.Class): the account the turn runs under and the
// agent's entry its arguments were made for, and ok false for a request
// that is not the turn holding the slot.
func (d *brakemanDesk) turnLaunch(request string) (account, agentSHA256 string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.turn == nil || d.turn.id != request {
		return "", "", false
	}
	return d.turn.account, d.turn.agentSHA256, true
}

// memberSpawned keeps a turn's member session for its process as soon as
// the process exists, before the session is registered
// (createSessionRequest's spawned): the slot is given up only once it is
// reaped, a launch that fails after the spawn (a registration refused,
// a stop after it that failed) included.
func (d *brakemanDesk) memberSpawned(request string, l *session.Local) {
	d.mu.Lock()
	added := d.keepLocal(request, l)
	d.mu.Unlock()
	if added {
		d.watch(l)
	}
	d.poke()
}

// memberStarted is told a turn's member session once the launch made and
// registered it (memberSpawned before, unless a test's seam made it): it
// is kept for its process if it is not yet, and a session that ended
// before it was registered, whose end sessionChanged left to this, has
// its end written now, with the output it kept.
func (d *brakemanDesk) memberStarted(request string, l *session.Local) {
	d.mu.Lock()
	added := d.keepLocal(request, l)
	if t := d.turn; t != nil && t.id == request {
		t.registered[l.Info().ID] = true
	}
	d.mu.Unlock()
	if added {
		d.watch(l)
	}
	if info := l.Info(); info.Status.Ended() {
		d.sessionEnded(info, l)
	}
	d.poke()
}

// keepLocal adds l to the locals of the turn holding the slot for
// request, once, and takes the run from its crew reference; it reports
// whether l was added. The caller holds mu.
func (d *brakemanDesk) keepLocal(request string, l *session.Local) bool {
	t := d.turn
	if t == nil || t.id != request || slices.Contains(t.locals, l) {
		return false
	}
	t.locals = append(t.locals, l)
	if ref := l.Info().Crew; ref != nil && t.runID == "" {
		t.runID = ref.RunID
	}
	return true
}

// sessionChanged is told every server session's change (localChange): a
// turn's member that ended closes its request (sessionEnded), with the
// output the registry's session, or the turn's (locals), kept. One not
// yet registered while its turn's launch is outstanding ended before the
// launch was done with it: memberStarted writes its end once it is
// registered, and a launch that fails writes its own failure.
func (d *brakemanDesk) sessionChanged(info session.Info) {
	ref := info.Crew
	if ref == nil || ref.Brakeman == nil || !info.Status.Ended() {
		return
	}
	l, ok := d.s.lookupLocal(info.ID)
	if !ok {
		d.mu.Lock()
		t := d.turn
		var locals []*session.Local
		wait := false
		if t != nil && t.id == ref.Brakeman.Request {
			locals = slices.Clone(t.locals)
			wait = t.launching && !t.registered[info.ID]
		}
		d.mu.Unlock()
		if wait {
			return
		}
		if i := slices.IndexFunc(locals, func(x *session.Local) bool { return x.Info().ID == info.ID }); i >= 0 {
			l = locals[i]
		}
	}
	d.sessionEnded(info, l)
}

// sessionEnded closes the request of a turn's member that ended, still
// open: ended, or failed when the agent never read its request (a
// logged-out CLI), with its exit and the last line l kept (nil when the
// session is no longer known).
func (d *brakemanDesk) sessionEnded(info session.Info, l *session.Local) {
	ref := info.Crew
	id := ref.Brakeman.Request
	// A closed request that holds its run and session already has nothing
	// to take from its ended session's later changes: nothing is written.
	if r, err := d.st.Get(id); err != nil || (!r.Open() && r.RunID != "" && r.SessionID != "") {
		return
	}
	d.mu.Lock()
	read := d.read[id]
	d.mu.Unlock()
	var last string
	if !read && l != nil {
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
	if err != nil && !errors.Is(err, brakeman.ErrNotFound) && !errors.Is(err, errDeskClosing) {
		d.s.log.Warn("Brakeman's turn's end could not be written", "request", id, "err", err)
	}
	d.poke()
}

// update is Store.Update with the brakeman event sent after a change of
// state (publish); refused (errDeskClosing) once the server is shutting
// down.
func (d *brakemanDesk) update(id string, fn func(*brakeman.Request) error) (brakeman.Request, error) {
	if d.closing.Load() {
		return brakeman.Request{}, errDeskClosing
	}
	before := ""
	r, err := d.st.Update(id, func(r *brakeman.Request) error {
		before = r.State
		return fn(r)
	})
	if err == nil && r.State != before {
		if f := d.afterWrite.Load(); f != nil {
			(*f)(id, r.State)
		}
		d.publish(id)
	}
	return r, err
}

// publish sends the brakeman event for a change of id's state with the
// state its records hold as it is sent, under emit: a writer that wrote
// running and sends its event after a cancel was written and sent sends
// cancelled again, never the running it wrote, so the events a page
// folds end on the state the records end on. A request that cannot be
// read sends nothing. It pokes the background role too, which follows a
// wake request's state (Outcome) and has no timer of its own for it: a
// request closed with no run (a launch refused, over budget at its
// dequeue) is known only from here.
func (d *brakemanDesk) publish(id string) {
	defer d.role.Poke()
	d.emit.Lock()
	defer d.emit.Unlock()
	r, err := d.st.Get(id)
	if err != nil {
		return
	}
	d.s.events.brakeman(id, r.State)
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

// dropWakes cancels the wake requests queued while Brakeman runs only
// when asked: one queued behind a person's turn as the role was turned
// off never starts. A wake turn already running runs to its end, as it
// does when the role is paused. The caller holds admit.
func (d *brakemanDesk) dropWakes() {
	d.mu.Lock()
	ids := slices.Clone(d.queue)
	d.mu.Unlock()
	for _, id := range ids {
		if r, err := d.st.Get(id); err != nil || r.Kind != brakeman.KindWake {
			continue
		}
		if _, err := d.closeTurn(id, brakeman.StateCancelled, "Brakeman runs only when asked"); err != nil {
			d.s.log.Warn("Brakeman's wake request could not be cancelled", "request", id, "err", err)
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
	if s.brakeman.recovering.Load() {
		writeError(w, http.StatusServiceUnavailable, "brakeman_recovering", "Brakeman's records are being recovered: ask again in a moment")
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
	case errors.Is(err, errDeskClosing):
		writeError(w, http.StatusServiceUnavailable, "shutting_down", err.Error())
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
	mux.HandleFunc("POST /api/brakeman/requests/{id}/items/{i}/make", s.requireAdmin(s.handleBrakemanMake))
	mux.HandleFunc("GET /api/brakeman/changes", s.observeRoute(s.handleBrakemanChanges))
	mux.HandleFunc("GET /api/brakeman/status", s.observeRoute(s.handleBrakemanStatus))
	mux.HandleFunc("GET /api/brakeman/screen", s.observeRoute(s.handleBrakemanScreen))
	mux.HandleFunc("GET /api/brakeman/overlaps", s.observeRoute(s.handleBrakemanOverlaps))
	mux.HandleFunc("GET /api/brakeman/briefs", s.observeRoute(s.handleBrakemanBriefs))
	mux.HandleFunc("GET /api/brakeman/role", s.requireAdmin(s.handleBrakemanRole))
	mux.HandleFunc("POST /api/brakeman/role", s.requireAdmin(s.handleBrakemanRoleAction))
	mux.HandleFunc("POST /api/brakeman/role/notes", s.requireAdmin(s.handleBrakemanKeepNote))
	mux.HandleFunc("DELETE /api/brakeman/role/notes", s.requireAdmin(s.handleBrakemanClearNotes))
	mux.HandleFunc("GET /api/sessions/{id}/brakeman/request", s.handleSessionBrakemanRequest)
	mux.HandleFunc("POST /api/sessions/{id}/brakeman/brief", s.handleSessionBrakemanBrief)
}

// brakemanState is GET /api/brakeman's answer.
type brakemanState struct {
	Config     brakeman.Config    `json:"config"`
	Current    *brakeman.Request  `json:"current"`
	Queue      []brakeman.Request `json:"queue"`
	TurnsToday int                `json:"turnsToday"`
	// Warnings are what Settings shows beside the agent chosen
	// (brakeman.Warnings: a read-only mode that does not hold).
	Warnings []string `json:"warnings,omitempty"`
	// ReadOnly says another server owns Brakeman's records, with why;
	// Stuck that the last turn's run did not stop.
	ReadOnly bool   `json:"readOnly,omitempty"`
	Why      string `json:"why,omitempty"`
	Stuck    bool   `json:"stuck,omitempty"`
	// MinutesToday is the day's minutes charged; Role BK3's role as it
	// stands, without its log (GET /api/brakeman/role).
	MinutesToday int              `json:"minutesToday"`
	Role         *crew.RoleRecord `json:"role,omitempty"`
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
	out := brakemanState{Config: cfg, Queue: []brakeman.Request{}, Warnings: brakeman.Warnings(cfg, brakemanFacts{s: s})}
	out.ReadOnly, out.Why = d.st.ReadOnly()
	if out.ReadOnly {
		// Another server owns the records and runs their turns: what it is
		// doing is read from them, never from this desk's scheduler, which
		// holds no turn and saw the queue only as it opened.
		if out.Current, out.Queue, err = d.recordedStatus(); err != nil {
			s.brakemanError(w, err)
			return
		}
	} else {
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
	}
	if out.TurnsToday, err = d.st.TurnsToday(d.now()); err != nil {
		s.brakemanError(w, err)
		return
	}
	if out.MinutesToday, err = d.st.MinutesToday(d.now()); err != nil {
		s.brakemanError(w, err)
		return
	}
	if rec, err := d.roleRecord(); err == nil {
		rec.Log, rec.Notes = nil, nil
		out.Role = &rec
	}
	writeJSON(w, http.StatusOK, out)
}

// recordedStatus is the desk's state as the records hold it, for a server
// that does not own them: the newest request starting or running (the
// owner's turn), nil for none, and the queued ones, oldest first.
func (d *brakemanDesk) recordedStatus() (*brakeman.Request, []brakeman.Request, error) {
	var cur *brakeman.Request
	queue := []brakeman.Request{}
	after := ""
	for {
		page, next, err := d.st.List(after, brakeman.MaxPage)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range page {
			switch r.State {
			case brakeman.StateStarting, brakeman.StateRunning:
				if cur == nil {
					cur = &r
				}
			case brakeman.StateQueued:
				queue = append(queue, r)
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	slices.Reverse(queue)
	return cur, queue, nil
}

// brakemanConfigBody is PUT /api/brakeman/config's body: the rev the
// configuration was read at and the configuration (its own rev and
// updatedAt are ignored).
type brakemanConfigBody struct {
	Rev    int64           `json:"rev"`
	Config brakeman.Config `json:"config"`
}

// handleBrakemanConfig saves the configuration: {rev, config} → {config,
// warnings?} (an agent whose read-only mode does not hold is taken, its
// gap said); 409 stale_revision for a rev not the stored one; 400 invalid_config
// with the field and its words; 503 shutting_down once the server is
// shutting down. Turning Brakeman off cancels the turn in progress and
// the queue; an unavailable role looks again (Role.Reconfigured).
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
	// A save that turns the role off or on drops its briefing times' mark
	// first, written before the configuration is: a briefing time that
	// passes while it is off fires nothing once it is on, whether or not
	// its loop stepped between. Outside admission's lock, which the role's
	// step takes inside its own.
	if d.recovered.Load() && !d.closing.Load() {
		if prev, err := d.st.Config(); err == nil && prev.Background() != c.Background() {
			if err := d.role.Switched(); err != nil {
				s.brakemanError(w, err)
				return
			}
		}
	}
	// The save and what turning Brakeman off cancels are one step of
	// admission's order: an ask after it reads Brakeman off, one before
	// it is in the queue cancelled, and no dequeue starts a turn between.
	// A desk not yet holding the records' queue as their owner saves
	// nothing: turning Brakeman off would cancel the queue as this server
	// saw it, not the records' (recovered).
	d.admit.Lock()
	var saved brakeman.Config
	var change crew.RoleChange
	var err error
	switch {
	case d.closing.Load():
		// The server is shutting down: nothing the save would cancel
		// could be written.
		err = errDeskClosing
	case d.recovered.Load():
		prev, perr := d.st.Config()
		saved, err = d.st.SaveConfig(c, brakemanFacts{s: s})
		if err == nil && perr == nil {
			change = roleChange(prev, saved)
		}
		switch {
		case err != nil:
		case !saved.Enabled:
			d.turnOff()
		case !saved.Background():
			// Run only when asked: no wake request waiting starts.
			d.dropWakes()
		}
	default:
		err = d.notOwner()
	}
	d.admit.Unlock()
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	// An unavailable role looks again at what refused its turn when the
	// save changed it (a budget raised, an agent or an account changed:
	// crew.SaveEnds): the batch it kept fires within its rules, or the
	// refusal is said again in words true now.
	if err := d.role.Reconfigured(change); err != nil {
		s.log.Warn("Brakeman's role could not look again after the configuration's save", "err", err)
	}
	d.poke()
	d.role.Poke()
	out := map[string]any{"config": saved}
	if warn := brakeman.Warnings(saved, brakemanFacts{s: s}); len(warn) > 0 {
		out["warnings"] = warn
	}
	writeJSON(w, http.StatusOK, out)
}

// roleChange is what a save from prev to next changed, as the role weighs
// it (crew.SaveEnds): the budget; what the agent starts with (the agent,
// its model, its account, its folder, the tools it may call without
// asking); the role turned on or off; anything else. The revision and the
// time saved are no change.
func roleChange(prev, next brakeman.Config) crew.RoleChange {
	ch := crew.RoleChange{
		Budget: prev.Budget != next.Budget,
		Start:  prev.AgentID != next.AgentID || prev.Model != next.Model || prev.Account != next.Account || prev.Cwd != next.Cwd || prev.AllowConductorTools != next.AllowConductorTools,
		On:     prev.Background() != next.Background(),
	}
	rest := func(c brakeman.Config) string {
		c.Budget, c.AgentID, c.Model, c.Account, c.Cwd, c.AllowConductorTools = brakeman.Budget{}, "", "", "", "", false
		c.Rev, c.UpdatedAt = 0, time.Time{}
		return mustJSON(c)
	}
	ch.Other = rest(prev) != rest(next)
	return ch
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
	if body.Kind == brakeman.KindWake {
		writeError(w, http.StatusBadRequest, "invalid_request", "a wake request is the background role's own: ask another kind")
		return
	}
	if strings.HasPrefix(body.OpID, wakeOpPrefix) {
		writeError(w, http.StatusBadRequest, "invalid_request", "an op id beginning "+wakeOpPrefix+" is the background role's own: choose another")
		return
	}
	in := brakeman.Request{OpID: body.OpID, Kind: body.Kind, Subjects: body.Subjects, Note: body.Note, Since: body.Since, By: body.By}
	if f := d.beforeAdmit.Load(); f != nil {
		(*f)()
	}
	// A request kept is a person's own, which the background role logs
	// (and which tries again for it when it is unavailable): told once
	// admission's locks are let go, since the role's step takes them; one
	// the role could not note is kept for its next pass (noteAsked), and
	// one still open is told again when its op id is asked again.
	var asked string
	defer func() {
		if asked != "" {
			d.noteAsked(asked)
		}
	}()
	// Brakeman on is read and the request kept in admission's order
	// (admit), so a save that turns Brakeman off is before this ask, which
	// is refused, or after it, and cancels it with the queue.
	d.admit.Lock()
	defer d.admit.Unlock()
	// A repeat answers its request whatever stands since. It is looked up
	// in admission's order, so of two asks with one opId at once the
	// second finds the first's request, never a slot the first just took.
	if _, found, err := d.st.ByOp(body.OpID); err == nil && found {
		req, _, err := d.st.Create(in)
		if err != nil {
			s.brakemanError(w, err)
			return
		}
		if req.Open() && d.recovered.Load() {
			asked = req.ID
		}
		writeJSON(w, http.StatusOK, map[string]any{"request": req})
		return
	}
	if !d.recovered.Load() {
		s.brakemanError(w, d.notOwner())
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
		d.publish(req.ID)
		d.poke()
		asked = req.ID
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

// ServesAct answers whether this build serves the route of the granted
// operation op (actServed).
func (f brakemanFacts) ServesAct(op string) error { return actServed(op) }

func (f brakemanFacts) CheckCrew(body json.RawMessage) error {
	var in crewInput
	if err := store.DecodeStrict(body, &in); err != nil {
		return err
	}
	c := in.crew()
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
// its creation, quiet for a wake request's turn (BK3's background role),
// whose questions page no one; and the agent's entry is the one the
// turn's arguments were made for (agentSHA256).
func (s *Server) brakemanLaunch(spec crew.LaunchSpec, req *createSessionRequest, ref *session.CrewRef) *apiError {
	if s.brakeman == nil {
		return newAPIError(http.StatusServiceUnavailable, "store_unavailable", "Brakeman needs a data directory")
	}
	account, sha, ok := s.brakeman.turnLaunch(spec.Request)
	if !ok {
		return newAPIError(http.StatusConflict, "not_the_turn", "no Brakeman turn holds the slot for "+spec.Request)
	}
	if sha == "" {
		return newAPIError(http.StatusConflict, "not_the_turn", "the turn for "+spec.Request+" named no agent entry")
	}
	no := false
	req.Yolo, req.conductorArgs = &no, true
	// The session starts only from the entry its read-only arguments were
	// made for: one changed since (another adapter, another command) is
	// refused agent_changed, and the turn fails with the engine's words.
	req.agentSHA256 = sha
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
	quiet := false
	if r, err := s.brakeman.st.Get(spec.Request); err != nil {
		return newAPIError(http.StatusConflict, "not_the_turn", "the turn's request "+spec.Request+" could not be read")
	} else {
		quiet = r.Kind == brakeman.KindWake
	}
	ref.Brakeman = &session.BrakemanMark{Request: spec.Request, Quiet: quiet}
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
