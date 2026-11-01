package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A launch from a ticket (Program item DS2, docs/plans/dispatch-evidence.md,
// PR-7, "Launching" and "Reconcile"): the one call from a Dispatch route
// into a run, and it makes a new one. Dispatch records the launch first,
// pending, with its package and the run id it minted; the engine's planned
// launch (crew.Engine.LaunchPlanned) writes the run's row with an insert
// that refuses an id the ledger has; the launch is then recorded started
// or failed by what the ledger holds, never by what this process
// remembers. So a resend answers the first launch, a restart settles every
// pending one (reconcileLaunches), no launch makes two runs and none
// adopts another's. Every attempt of the run gets the package's
// attachments from Dispatch's store (UsePackageFiles) and starts only the
// agent selection the revision approved (createLocalSession's check of
// crew.LaunchSpec.AgentSHA256).

const (
	// MaxLaunchReply bounds a launch's read: its summary beside its
	// package as the bytes it is kept as (at most dispatch.MaxPackage, 512
	// KiB). The largest, a package at its bound with the summary's texts
	// (why, the agents, the label) at their escaped worst, is under it
	// (dispatch_launch_test.go). Exported for the CLI.
	MaxLaunchReply = 1 << 20
	// maxTicketLaunchBody bounds a launch's body: an op, a revision's id
	// and a label of at most 60 characters.
	maxTicketLaunchBody = 4 << 10
	// agentChangedCode is createLocalSession's refusal of an attempt's
	// session whose agent's catalog entry is not the approved selection;
	// Launch hands the engine crew.ErrAgentChanged for it.
	agentChangedCode = "agent_changed"
)

// launchGate holds what admits and settles the launches from a ticket:
// ready, set once the ledger's recovery and the reconcile of the pending
// launches have both finished (ledgerRecovered), before which no launch
// advances; the launches being settled in this process, by launch id, a
// second request for one waiting for the first; and the tests' seams.
// Its zero value is ready to use.
type launchGate struct {
	ready    atomic.Bool
	mu       sync.Mutex
	inFlight map[string]chan struct{}
	// waits counts the requests that waited for another's settling of
	// their launch, for the tests.
	waits int
	// beforePlanned runs with a launch's id just before the engine is
	// asked for its run; beforeReconcile before the reconcile at the
	// ledger's recovery; attempt with each session spec Launch is given,
	// before it takes the catalog entry. Tests hold them; under mu.
	beforePlanned   func(launchID string)
	beforeReconcile func()
	attempt         func(crew.LaunchSpec)
}

// gateSeam reads one of the gate's seams under its lock.
func gateSeam[T any](g *launchGate, f *T) T {
	g.mu.Lock()
	defer g.mu.Unlock()
	return *f
}

// setSeams sets the gate's seams for a test, under its lock.
func (g *launchGate) setSeams(set func(g *launchGate)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	set(g)
}

// beforeAttempt runs the attempt seam, when a test set one.
func (g *launchGate) beforeAttempt(spec crew.LaunchSpec) {
	if f := gateSeam(g, &g.attempt); f != nil {
		f(spec)
	}
}

// ticketLaunchBody is a launch's body: the operation, the plan revision
// to launch from and the run's label (empty for the ticket's key and
// title).
type ticketLaunchBody struct {
	dispatchOp
	Plan  string `json:"plan"`
	Label string `json:"label,omitempty"`
}

// wireLaunches gives the engine and Dispatch their sight of each other
// for the launches from a ticket: the engine reads a package's
// attachments from Dispatch's store (OpenLaunchFile), and the store reads
// which ticket launched a run from the engine, else from the run's
// record (runTicket).
func (s *Server) wireLaunches() {
	s.runs.UsePackageFiles(s.openLaunchFile)
	if s.dispatch != nil {
		s.dispatch.SetRunTicket(s.runTicket)
	}
}

// openLaunchFile opens the content of an attachment a launch's package
// names, by its hash, from Dispatch's store, and nothing else.
func (s *Server) openLaunchFile(ctx context.Context, launch, sum string) (io.ReadCloser, error) {
	if s.dispatch == nil {
		return nil, errors.New("no Dispatch store is open")
	}
	f, err := s.dispatch.OpenLaunchFile(ctx, launch, sum)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// runTicket is the id of the ticket whose package a run carries (its
// Ticket), from the engine, else from its record; ok false for a run no
// ticket launched. It runs inside a Dispatch operation with the store's
// lock held, so it reads the engine and the records and never the store.
func (s *Server) runTicket(run string) (string, bool) {
	if !crew.ValidRunID(run) {
		return "", false
	}
	if r, ok := s.runs.Get(run); ok {
		if r.Ticket != nil {
			return r.Ticket.ID, true
		}
		return "", false
	}
	if s.records == nil {
		return "", false
	}
	r, ok, err := s.records.Get(run)
	if err != nil {
		s.log.Warn("a run's record could not be read for its ticket", "run", run, "err", err)
	}
	if ok && r.Ticket != nil {
		return r.Ticket.ID, true
	}
	return "", false
}

// ledgerRecovered runs once the ledger is the server's and its recovery
// has finished (openLedger's afterRecover, in New or inside the one
// OnOwned callback): the pending launches are reconciled, and only then
// are launches admitted (ready), so none is committed, or replayed, while
// the reconcile could read it before its run's first transaction.
func (s *Server) ledgerRecovered(ld *ledger.Ledger) {
	if f := gateSeam(&s.launches, &s.launches.beforeReconcile); f != nil {
		f()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.reconcileLaunches(ctx, ld)
	s.launches.ready.Store(true)
}

// reconcileLaunches settles every launch still pending by what the
// ledger holds: one whose run the ledger has and is the launch's
// (runRowIsLaunch) is recorded started, one whose run id is another
// run's, or that has no run, failed with why. The server that committed a
// pending launch has stopped (this one owns the ledger now), so no launch
// is still being started. A launch whose run the ledger cannot read stays
// pending, for the next start.
func (s *Server) reconcileLaunches(ctx context.Context, ld *ledger.Ledger) {
	if s.dispatch == nil || ld == nil {
		return
	}
	pending, err := s.dispatch.PendingLaunches(ctx)
	if err != nil {
		s.log.Warn("the pending launches could not be read for their reconcile", "err", err)
		return
	}
	for _, l := range pending {
		row, found, err := ld.Run(ctx, l.RunID)
		if err != nil {
			s.log.Warn("a pending launch stays pending: its run could not be read", "launch", l.ID, "run", l.RunID, "err", err)
			continue
		}
		var rc dispatch.Receipt
		switch {
		case found && runRowIsLaunch(row, l):
			rc, err = s.dispatch.StartedLaunch(ctx, dispatch.Op{}, l.ID, rowAgents(row))
		case found:
			rc, err = s.dispatch.FailedLaunch(ctx, dispatch.Op{}, l.ID, "the run id is another run's")
		default:
			rc, err = s.dispatch.FailedLaunch(ctx, dispatch.Op{}, l.ID, "the server stopped before the run started")
		}
		if err != nil || rc.Status != dispatch.StatusDone {
			s.log.Warn("a pending launch could not be settled", "launch", l.ID, "run", l.RunID, "err", err, "refusal", rc.Error)
			continue
		}
		s.log.Info("a pending launch was settled at the start", "launch", l.ID, "run", l.RunID, "started", found && runRowIsLaunch(row, l))
	}
}

// runRowIsLaunch reports whether the ledger's row is the run the launch
// made: its ticket the launch's and the SHA-256 of its package the
// launch's package's. A row of the run id that is not is another run's,
// which no launch adopts.
func runRowIsLaunch(row ledger.RunRow, l dispatch.LaunchSummary) bool {
	if row.Package == "" || row.Ticket != l.TicketID {
		return false
	}
	sum := sha256.Sum256([]byte(row.Package))
	return hex.EncodeToString(sum[:]) == l.PackageSHA256
}

// rowAgents is the members of a run's row with their agents, the version
// unknown: what a launch adopted from the ledger records.
func rowAgents(row ledger.RunRow) []dispatch.LaunchAgent {
	var c crew.Crew
	if json.Unmarshal([]byte(row.Crew), &c) != nil {
		return nil
	}
	out := make([]dispatch.LaunchAgent, 0, len(c.Members))
	for _, m := range c.Members {
		out = append(out, dispatch.LaunchAgent{Member: m.Name, Agent: m.AgentID})
	}
	return out
}

// launchAgents is the members with their agents and, where identity
// answers, the version the agent's identity probe reported.
func launchAgents(members []crew.Member, cat catalog.Catalog, identity func(catalog.Agent) *Identity) []dispatch.LaunchAgent {
	out := make([]dispatch.LaunchAgent, 0, len(members))
	for _, m := range members {
		la := dispatch.LaunchAgent{Member: m.Name, Agent: m.AgentID}
		if a, ok := cat.Get(m.AgentID); ok {
			if id := identity(a); id != nil && id.Identified {
				// The store cuts a longer one.
				la.Version = id.Version
			}
		}
		out = append(out, la)
	}
	return out
}

// launchAdmission refuses a launch, or the replay of a pending one, that
// this server may not advance: managed runs off (409 managed_off), no
// ledger, one another server owns, or one whose recovery and reconcile
// have not finished (503 ledger_unavailable). nil admits it.
func (s *Server) launchAdmission() *apiError {
	if !s.cfg.ManagedCrews() {
		return newAPIError(http.StatusConflict, "managed_off", "crews with tasks are off on this server (crews.managed)")
	}
	if s.ledger == nil {
		return newAPIError(http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened; see the server's log")
	}
	if ro, why := s.ledger.ReadOnly(); ro {
		return newAPIError(http.StatusServiceUnavailable, "ledger_unavailable", why)
	}
	if !s.launches.ready.Load() {
		return newAPIError(http.StatusServiceUnavailable, "ledger_unavailable", "the server is taking over the ledger")
	}
	return nil
}

// handleDispatchLaunch launches a managed run from a ticket's approved
// plan revision (ticket.launch): body {opId, plan, expectedRev?, label?,
// byName?}, at most maxTicketLaunchBody, expectedRev the ticket's; 201
// {receipt, launch, run} once the run's first ledger transaction is
// committed and the launch recorded started (200 for the same opId
// again); 409 launch_failed {error, receipt, launch} when the run could
// not be started, the launch kept, failed, with why. In order
// (docs/plans/dispatch-evidence.md, "Launching"): the operation replayed
// first, so a resend is answered by the launch it made, whatever became
// of the revision or the catalog since (op_conflict for another revision
// or label under the id, a refusal kept answered as kept); a launch that
// may advance, a new one or a pending one replayed, needs managed runs on
// (409 managed_off) and the ledger this server's, writable and taken over
// (503 ledger_unavailable): a pending one replayed here otherwise is
// answered as it stands, never recorded failed, since only the ledger's
// owner settles a launch; a new one has the revision's agents resolved
// against the catalog again (409 stale_plan naming the agent and what
// changed, "revise it", nothing kept), then the store's launch (its
// refusals with their receipts: stale_plan, invalid, archived, bound,
// stale_revision, not_found); then the launch is settled (settleLaunch).
func (s *Server) handleDispatchLaunch(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	var body ticketLaunchBody
	if !readOp(w, r, &body, &body.dispatchOp, maxTicketLaunchBody) {
		return
	}
	// A launch runs to its end when the client goes away: its store
	// operations and its run's start, each within its own bound.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), runTimeout+2*dispatchWriteTimeout)
	defer cancel()
	op := body.op(r)
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentTicketLaunch, dispatch.LaunchArgs{Key: key, Plan: body.Plan, Label: body.Label})
	switch {
	case err != nil:
		s.dispatchError(w, "launch", err)
		return
	case found && rc.Status != dispatch.StatusDone:
		s.writeOp(w, "launch", rc, nil, true, "launch", func(dispatch.Receipt) (any, error) { return nil, nil })
		return
	case found:
		s.answerLaunch(ctx, w, ds, rc, op)
		return
	}
	if !dispatch.ValidID(body.Plan) {
		writeError(w, http.StatusBadRequest, "invalid_request", "plan is a plan revision's id, 16 lower-case hex characters")
		return
	}
	if aerr := s.launchAdmission(); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	// The early refusal, in words, before anything is kept: an agent of
	// the revision that resolves otherwise now. The gate is each attempt's
	// own launch, which checks the entry it starts.
	switch v, err := ds.Plan(ctx, body.Plan); {
	case errors.Is(err, dispatch.ErrNotFound):
		// The store refuses it not_found, its receipt kept.
	case err != nil:
		s.dispatchError(w, "plan revision", err)
		return
	default:
		f, err := s.planFacts.get(v.Digest, func() (json.RawMessage, error) { return v.Body, nil })
		if err != nil {
			s.dispatchError(w, "plan revision", err)
			return
		}
		if words := agentsChange(s.Catalog(), f, s.cfg.Yolo); words != "" {
			writeError(w, http.StatusConflict, dispatch.RefusalStalePlan, fmt.Sprintf("%s since revision %d was approved: revise it", words, v.N))
			return
		}
	}
	rc, err = ds.Launch(ctx, op, key, body.Plan, body.Label)
	if err != nil || rc.Status != dispatch.StatusDone {
		s.writeOp(w, "launch", rc, err, true, "launch", func(dispatch.Receipt) (any, error) { return nil, nil })
		return
	}
	s.answerLaunch(ctx, w, ds, rc, op)
}

// answerLaunch settles the launch the done receipt rc names and answers
// it as it then stands: started, 201 {receipt, launch, run} (200 for a
// repeat); failed, 409 launch_failed {error, receipt, launch}; still
// pending (this server may not advance it, or its run is still being
// started by another request past the wait), 503 ledger_unavailable
// {error, receipt, launch}. launch is the launch's summary, never its
// package (GET /api/dispatch/launches/{id} reads it).
func (s *Server) answerLaunch(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, rc dispatch.Receipt, op dispatch.Op) {
	l, err := ds.LaunchByID(ctx, rc.ID)
	if err != nil {
		s.dispatchError(w, "launch", err)
		return
	}
	var pending *apiError
	if l.State == dispatch.LaunchPending {
		if aerr := s.launchAdmission(); aerr != nil {
			pending = aerr
		} else {
			var release func()
			l, release, pending, err = s.settleLaunch(ctx, ds, l, op.By)
			defer release()
			if err != nil {
				s.dispatchError(w, "launch", err)
				return
			}
		}
	}
	switch l.State {
	case dispatch.LaunchStarted:
		status := http.StatusCreated
		if rc.Repeat {
			status = http.StatusOK
		}
		writeDispatch(w, status, map[string]any{"receipt": rc, "launch": l.LaunchSummary, "run": s.launchedRun(l.RunID)})
	case dispatch.LaunchFailed:
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: "launch_failed", Message: l.Why}, "receipt": rc, "launch": l.LaunchSummary})
	default:
		if pending == nil {
			pending = newAPIError(http.StatusServiceUnavailable, "ledger_unavailable", "the launch is still being started: send it again")
		}
		writeDispatch(w, pending.status, map[string]any{"error": apiError{Code: pending.Code, Message: pending.Message}, "receipt": rc, "launch": l.LaunchSummary})
	}
}

// launchedRun is the run with the id as GET /api/runs/{run} answers it,
// from the engine, else from its record; nil for none.
func (s *Server) launchedRun(id string) *crew.Run {
	if run, ok := s.runs.Get(id); ok {
		return &run
	}
	if s.records != nil {
		if run, ok, err := s.records.Get(id); err == nil && ok {
			return &run
		}
	}
	return nil
}

// settleLaunch settles the pending launch l in this process, one request
// at a time per launch (launchesInFlight): a second waits for the first,
// at most runTimeout, then reads the launch as it stands. The first reads
// the ledger for the launch's run id: a row that is the launch's
// (runRowIsLaunch) is adopted, the launch recorded started without
// launching; another run's row records it failed, that row untouched;
// with none the run is launched (launchPlanned). Refused anywhere there,
// the ledger is read again: a row that is the launch's means the run
// exists (an error after its first transaction is the run's to show), so
// the launch is recorded started; else failed, with the words. A ledger
// that cannot be read leaves the launch pending (503
// ledger_unavailable). It answers the launch as read after it, the
// release of the engine's hold on the run, to call once the reply is
// written, and the error of a store that failed.
func (s *Server) settleLaunch(ctx context.Context, ds *dispatch.Store, l dispatch.LaunchView, by string) (dispatch.LaunchView, func(), *apiError, error) {
	noop := func() {}
	g := &s.launches
	g.mu.Lock()
	if ch, ok := g.inFlight[l.ID]; ok {
		g.waits++
		g.mu.Unlock()
		t := time.NewTimer(runTimeout)
		defer t.Stop()
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
		}
		after, err := ds.LaunchByID(ctx, l.ID)
		return after, noop, nil, err
	}
	ch := make(chan struct{})
	if g.inFlight == nil {
		g.inFlight = map[string]chan struct{}{}
	}
	g.inFlight[l.ID] = ch
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.inFlight, l.ID)
		g.mu.Unlock()
		close(ch)
	}()
	// Read again inside the hold: another request may have settled it.
	l, err := ds.LaunchByID(ctx, l.ID)
	if err != nil || l.State != dispatch.LaunchPending {
		return l, noop, nil, err
	}
	op := dispatch.Op{By: by}
	row, found, err := s.ledger.Run(ctx, l.RunID)
	if err != nil {
		s.log.Warn("a launch stays pending: its run could not be read", "launch", l.ID, "run", l.RunID, "err", err)
		return l, noop, newAPIError(http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be read: "+err.Error()), nil
	}
	release := noop
	var agents []dispatch.LaunchAgent
	var started bool
	var why string
	switch {
	case found && runRowIsLaunch(row, l.LaunchSummary):
		started, agents = true, rowAgents(row)
	case found:
		why = "the run id is another run's"
	default:
		var lerr error
		agents, release, lerr = s.launchPlanned(ctx, ds, l)
		if lerr == nil {
			started = true
			break
		}
		why = lerr.Error()
		// Refused: the ledger says whether the run exists all the same.
		switch row, found, err := s.ledger.Run(ctx, l.RunID); {
		case err != nil:
			s.log.Warn("a launch stays pending: its run could not be read after its refusal", "launch", l.ID, "run", l.RunID, "err", err, "refusal", lerr)
			return l, release, newAPIError(http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be read: "+err.Error()), nil
		case found && runRowIsLaunch(row, l.LaunchSummary):
			started = true
			if agents == nil {
				agents = rowAgents(row)
			}
		}
	}
	var rc dispatch.Receipt
	if started {
		rc, err = ds.StartedLaunch(ctx, op, l.ID, agents)
	} else {
		s.log.Info("a launch from a ticket failed", "launch", l.ID, "run", l.RunID, "why", why)
		rc, err = ds.FailedLaunch(ctx, op, l.ID, why)
	}
	if err != nil {
		return l, release, nil, err
	}
	if rc.Status != dispatch.StatusDone {
		// Settled meanwhile (invalid: no longer pending): read as it stands.
		s.log.Warn("a launch's outcome was not recorded", "launch", l.ID, "refusal", rc.Refusal, "words", rc.Error)
	}
	after, err := ds.LaunchByID(ctx, l.ID)
	return after, release, nil, err
}

// launchPlanned starts the run of the pending launch l through the
// engine: the revision's crew from its body, its working directory
// through resolveCwd, launchable, its members' agents checked as a crew
// launch checks them (checkLaunch: in the catalog, installed,
// identified), and the engine's LaunchPlanned with the launch's run id,
// the revision's base, the package and its digest, on a context of its
// own (the request's values, not its end, bounded by runTimeout). It
// answers each member's agent with the version its identity probe
// reported and the release of the engine's hold on the run.
func (s *Server) launchPlanned(ctx context.Context, ds *dispatch.Store, l dispatch.LaunchView) ([]dispatch.LaunchAgent, func(), error) {
	noop := func() {}
	v, err := ds.Plan(ctx, l.PlanID)
	if err != nil {
		return nil, noop, fmt.Errorf("the plan revision could not be read: %w", err)
	}
	var body dispatch.PlanBody
	if err := json.Unmarshal(v.Body, &body); err != nil {
		return nil, noop, fmt.Errorf("the plan revision's body: %w", err)
	}
	var c crew.Crew
	if err := json.Unmarshal(body.Crew, &c); err != nil {
		return nil, noop, fmt.Errorf("the plan revision's crew: %w", err)
	}
	if err := c.Launchable(); err != nil {
		return nil, noop, err
	}
	cwd, err := s.resolveCwd(cmp.Or(c.Cwd, s.cfg.DefaultCwd))
	if err != nil {
		return nil, noop, fmt.Errorf("the crew's working directory: %w", err)
	}
	c.Cwd = cwd
	if c.Yolo == nil {
		yolo := s.cfg.Yolo
		c.Yolo = &yolo
	}
	cat := s.Catalog()
	identity := s.identityFor(ctx, cat, c.Members)
	if err := checkLaunch(c.Members, cat, s.installedFor(ctx, cat, c.Members), identity); err != nil {
		return nil, noop, err
	}
	if f := gateSeam(&s.launches, &s.launches.beforePlanned); f != nil {
		f(l.ID)
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runTimeout)
	run, release, err := s.runs.LaunchPlanned(rctx, c, l.Label, crew.Planned{RunID: l.RunID, Base: body.Base, Digest: l.PackageSHA256, Package: []byte(l.Package)})
	done := func() { release(); cancel() }
	if err != nil {
		return nil, done, err
	}
	s.log.Info("a run was launched from a ticket", "launch", l.ID, "ticket", l.TicketKey, "run", run.ID)
	return launchAgents(c.Members, cat, identity), done, nil
}

// handleDispatchLaunchRead answers a launch whole: {launch}, its summary
// and its package as the bytes it is kept as, under MaxLaunchReply; 404
// not_found; 400 invalid_request for an id that is not one and for any
// parameter.
func (s *Server) handleDispatchLaunchRead(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	v, err := ds.LaunchByID(r.Context(), id)
	if err != nil {
		s.dispatchError(w, "launch", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"launch": v})
}
