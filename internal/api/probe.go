package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Identity is what the server knows of an agent's program from its
// adapter's probe (agents.Probe), as GET /api/catalog and POST
// /api/catalog/check report it under `identity`.
type Identity struct {
	// Ran says the probe ran and answered; Pending that it is under way
	// (ask again); neither with Error when it could not run.
	Ran     bool `json:"ran"`
	Pending bool `json:"pending,omitempty"`
	// Identified says the program is the agent, Version what it said;
	// Impostor that it is a known other program of the same name. Verified
	// says the adapter's expectation was checked against the real CLI: a
	// verified probe that identifies nothing names the program as not the
	// agent, an unverified one leaves it unidentified.
	Identified bool   `json:"identified"`
	Impostor   bool   `json:"impostor,omitempty"`
	Verified   bool   `json:"verified"`
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
	// Output is the first line the program printed, when not identified.
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Misidentified reports whether the identity says the program is not the
// agent: a known impostor, or a verified probe that matched nothing.
func (id Identity) Misidentified() bool {
	return id.Ran && (id.Impostor || (id.Verified && !id.Identified))
}

const (
	probeTTL     = 10 * time.Minute
	probeWait    = 2 * time.Second
	probeWorkers = 2
	maxProbes    = 256
)

type probeEntry struct {
	res agents.ProbeResult
	at  time.Time
}

type probeCall struct {
	done chan struct{}
	e    probeEntry
}

// probeCache runs the identity probes and keeps their answers for probeTTL,
// the way lookupCache keeps the lookups: a probe runs in a goroutine of its
// own, at most probeWorkers at once, askers wait at most probeWait and get
// Pending otherwise, and askers of a probe under way join it. A probe
// outlives its asker's request but not the server's stop (stop, close;
// #961).
type probeCache struct {
	mu       sync.Mutex
	entries  map[string]probeEntry
	inflight map[string]*probeCall
	// run runs one probe as r (r.Run): stop ends it through r.End.
	run     func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult
	now     func() time.Time
	wait    time.Duration
	timeout time.Duration // a probe's bound, agents' own when zero
	sem     chan struct{}
	// life is the probes' context, ended by stop (end). closed, running
	// (the probes whose program may run) and probing change under mu, so
	// stop ends every probe that runs and close waits for every one begun.
	life    context.Context
	end     context.CancelFunc
	closed  bool
	running map[*agents.ProbeProcess]struct{}
	probing sync.WaitGroup
	// The help reads (flags.go): help runs one as r (r.Help), in the same
	// slots as the probes and ended by the same stop; helps keeps their
	// answers, helpCalls the reads under way.
	help      func(ctx context.Context, r *agents.ProbeProcess, command, args []string, env map[string]string) (string, agents.ProbeResult)
	helps     map[string]*helpEntry
	helpCalls map[string]*helpCall
}

func newProbeCache() *probeCache {
	life, end := context.WithCancel(context.Background())
	return &probeCache{
		entries:  map[string]probeEntry{},
		inflight: map[string]*probeCall{},
		run: func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
			return r.Run(ctx, command, p, env)
		},
		now:     time.Now,
		wait:    probeWait,
		sem:     make(chan struct{}, probeWorkers),
		life:    life,
		end:     end,
		running: map[*agents.ProbeProcess]struct{}{},
		help: func(ctx context.Context, r *agents.ProbeProcess, command, args []string, env map[string]string) (string, agents.ProbeResult) {
			return r.Help(ctx, command, args, env)
		},
		helps:     map[string]*helpEntry{},
		helpCalls: map[string]*helpCall{},
	}
}

// errProbeStopping is a probe's error once the server is stopping: it was
// not run, or was ended.
const errProbeStopping = "the server is stopping"

// stop is the first thing a server's stop does: no probe starts from now
// on, and every one under way is sent its end here, on the caller's
// goroutine (agents.ProbeProcess.End: its process group and its program
// killed), without waiting for it. A descendant that left the probe's
// process group (a session of its own) is not reached.
func (c *probeCache) stop() {
	c.mu.Lock()
	c.closed = true
	for r := range c.running {
		r.End()
	}
	c.mu.Unlock()
	c.end()
}

// close stops the probes (stop) and waits, as long as ctx allows, for
// every probe begun: when it returns with ctx not done, each probe's
// program has been killed and reaped and its temporary folder removed.
// With ctx done first the probes have been sent their end and are not
// waited for. A second call waits the same way.
func (c *probeCache) close(ctx context.Context) {
	c.stop()
	done := make(chan struct{})
	go func() {
		c.probing.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// probeKey is the cache key of a command (its program resolved) under an
// adapter, with the agent's environment: two agents that run the same
// program with other variables (the e2e stubs) are two programs.
func probeKey(command []string, adapter string, env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(strings.Join(command, "\x00"))
	b.WriteString("\x00\x00" + adapter)
	for _, k := range keys {
		b.WriteString("\x00\x01" + k + "=" + env[k])
	}
	return b.String()
}

// identity answers for an agent's command (its program resolved to an
// absolute path, the rest as the agent has it) with the adapter's probe p;
// force runs the probe again whatever the cache holds.
func (c *probeCache) identity(ctx context.Context, command []string, adapter, name string, p agents.Probe, env map[string]string, force bool) Identity {
	key := probeKey(command, adapter, env)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Identity{Verified: p.Verified, Name: name, Error: errProbeStopping}
	}
	if e, ok := c.entries[key]; ok && !force && c.now().Sub(e.at) < probeTTL {
		c.mu.Unlock()
		return toIdentity(e.res, name, p)
	}
	call, joined := c.inflight[key]
	if !joined || force {
		call = &probeCall{done: make(chan struct{})}
		c.inflight[key] = call
		joined = false
	}
	wait := c.wait
	if !joined {
		// Counted under mu, where close sets closed: close waits for it.
		c.probing.Add(1)
	}
	c.mu.Unlock()
	if !joined {
		go c.probe(key, command, p, env, call)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-call.done:
		return toIdentity(call.e.res, name, p)
	case <-ctx.Done():
	case <-timer.C:
	}
	return Identity{Pending: true, Verified: p.Verified, Name: name}
}

// probe runs one probe once a slot is free, unless close came first, and
// settles call. It runs under the cache's life, not the asker's request:
// the probe outlives the request, its answer kept, but not the server.
func (c *probeCache) probe(key string, command []string, p agents.Probe, env map[string]string, call *probeCall) {
	defer c.probing.Done()
	res := agents.ProbeResult{Error: errProbeStopping}
	c.inSlot(func(r *agents.ProbeProcess) { res = c.run(c.life, r, command, p, env) })
	c.mu.Lock()
	call.e = probeEntry{res: res, at: c.now()}
	if c.inflight[key] == call {
		delete(c.inflight, key)
	}
	if c.closed || res.Retry {
		// Ended by the stop, or not run, or failed on the run's own side
		// (a watch that failed): not an answer to keep.
		c.mu.Unlock()
		close(call.done)
		return
	}
	for k, old := range c.entries {
		if c.now().Sub(old.at) >= probeTTL {
			delete(c.entries, k)
		}
	}
	if _, had := c.entries[key]; had || len(c.entries) < maxProbes {
		c.entries[key] = call.e
	}
	c.mu.Unlock()
	close(call.done)
}

// inSlot runs fn with a process of its own once one of the cache's worker
// slots is free, unless the stop came first; false when fn was not run.
// The process is counted under mu, where stop ends every one counted: one
// begun after stop is not run. The caller is counted in probing.
func (c *probeCache) inSlot(fn func(r *agents.ProbeProcess)) bool {
	select {
	case c.sem <- struct{}{}:
	case <-c.life.Done():
		return false
	}
	defer func() { <-c.sem }()
	r := &agents.ProbeProcess{Timeout: c.timeout}
	c.mu.Lock()
	run := !c.closed
	if run {
		c.running[r] = struct{}{}
	}
	c.mu.Unlock()
	if !run {
		return false
	}
	fn(r)
	c.mu.Lock()
	delete(c.running, r)
	c.mu.Unlock()
	return true
}

// cached is the identity the cache holds for command and adapter, without
// running anything; ok false when it holds none.
func (c *probeCache) cached(command []string, adapter, name string, p agents.Probe, env map[string]string) (Identity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[probeKey(command, adapter, env)]
	if !ok || c.now().Sub(e.at) >= probeTTL {
		return Identity{}, false
	}
	return toIdentity(e.res, name, p), true
}

func toIdentity(res agents.ProbeResult, name string, p agents.Probe) Identity {
	return Identity{Ran: res.Ran, Identified: res.Identified, Impostor: res.Impostor, Verified: p.Verified, Name: name, Version: res.Version, Output: res.Output, Error: res.Error}
}

// cachedIdentity is what the probe cache already knows of agent a's
// program, without running anything: ok false when it knows nothing (no
// probe, the probe off, a relative program, a lookup not done, nothing
// cached).
func (s *Server) cachedIdentity(a catalog.Agent) (Identity, bool) {
	return s.cachedIdentityOf(a, false)
}

// cachedIdentityOf is cachedIdentity; held (a turn's read of capacity), the
// program's path is the one the look-ups hold (lookupCache.held), none
// started and none waited for.
func (s *Server) cachedIdentityOf(a catalog.Agent, held bool) (Identity, bool) {
	if !a.Probed() || len(a.Command) == 0 || relativePath(a.Command[0]) {
		return Identity{}, false
	}
	p := agents.ProbeFor(a.Adapter)
	if p == nil {
		return Identity{}, false
	}
	path := ""
	if held {
		path = s.lookups.held(a.Command[0]).path
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if found, ok, known := s.lookups.found(ctx, a.Command[0]); ok && known {
			path = found
		}
	}
	if path == "" {
		return Identity{}, false
	}
	name := a.Adapter
	if ad, ok := agents.Get(a.Adapter); ok {
		name = ad.Name
	}
	return s.probes.cached(resolvedCommand(a, path), a.Adapter, name, *p, a.Env)
}

// notTheAgent is the refusal of a plain launch whose program the cache knows
// as another program of the same name (a known impostor): it is never
// offered, and the API does not launch it either. Turning the agent's probe
// off ("probe": false) launches it anyway. Nothing is run here; an
// identity not yet cached does not refuse.
func (s *Server) notTheAgent(a catalog.Agent) *apiError {
	id, ok := s.cachedIdentity(a)
	if !ok || !id.Ran || !id.Impostor {
		return nil
	}
	return newAPIError(http.StatusBadRequest, "not_the_agent", a.Command[0]+" on the server is another program, not "+id.Name+" (--version printed "+strconv.Quote(id.Output)+"); install "+id.Name+", or turn the agent's identity check off to launch it anyway")
}

// noteIdentity records on local what the cache knows of its agent's program
// when that says it is not the agent (a verified probe that matched nothing:
// a new release may print its version differently): a plain launch goes
// ahead (the person chose it), with the note in the session's activity.
// Nothing is run here.
func (s *Server) noteIdentity(local *session.Local, a catalog.Agent) {
	if id, ok := s.cachedIdentity(a); ok && id.Misidentified() {
		local.Record(session.ActivityEntry{Type: session.ActivityError, Message: "launched " + a.Command[0] + ", which is not " + id.Name + " (--version printed " + strconv.Quote(id.Output) + ")"})
	}
}

// identityOf is the identity of agent a on this server: nil when the agent
// has no adapter with a probe, turns the probe off, or its program was not
// found at an absolute path (a relative one resolves in the session's
// directory, which the server cannot stand for). path is what the lookup
// resolved the program to.
func (s *Server) identityOf(ctx context.Context, a catalog.Agent, path string, force bool) *Identity {
	if !a.Probed() || len(a.Command) == 0 || path == "" {
		return nil
	}
	p := agents.ProbeFor(a.Adapter)
	if p == nil {
		return nil
	}
	name := a.Adapter
	if ad, ok := agents.Get(a.Adapter); ok {
		name = ad.Name
	}
	id := s.probes.identity(ctx, resolvedCommand(a, path), a.Adapter, name, *p, a.Env, force)
	return &id
}

// resolvedCommand is the agent's command with its program replaced by the
// path the lookup resolved it to.
func resolvedCommand(a catalog.Agent, path string) []string {
	return append([]string{path}, a.Command[1:]...)
}
