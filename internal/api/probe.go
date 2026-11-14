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
// Pending otherwise, and askers of a probe under way join it.
type probeCache struct {
	mu       sync.Mutex
	entries  map[string]probeEntry
	inflight map[string]*probeCall
	run      func(ctx context.Context, command []string, p agents.Probe, env map[string]string) agents.ProbeResult
	now      func() time.Time
	wait     time.Duration
	sem      chan struct{}
}

func newProbeCache() *probeCache {
	return &probeCache{
		entries:  map[string]probeEntry{},
		inflight: map[string]*probeCall{},
		run:      agents.RunProbe,
		now:      time.Now,
		wait:     probeWait,
		sem:      make(chan struct{}, probeWorkers),
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
	c.mu.Unlock()
	if !joined {
		go c.probe(ctx, key, command, p, env, call)
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

func (c *probeCache) probe(ctx context.Context, key string, command []string, p agents.Probe, env map[string]string, call *probeCall) {
	c.sem <- struct{}{}
	// The probe outlives the asker's request: its answer is kept.
	res := c.run(context.WithoutCancel(ctx), command, p, env)
	<-c.sem
	c.mu.Lock()
	call.e = probeEntry{res: res, at: c.now()}
	if c.inflight[key] == call {
		delete(c.inflight, key)
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
