package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// lookupTTL is how long an answer about a program is kept: a CLI installed
// meanwhile is seen at most this long after.
const lookupTTL = 30 * time.Second

// maxLookups bounds the answers kept at once. The catalog's agents need a few
// dozen; past the bound an answer is given but not kept (nothing is evicted)
// until answers expire and make room.
const maxLookups = 256

// lookupWorkers bounds the lookups under way at once, for every request
// together: on a PATH that stalls, at most this many goroutines sit in the
// system's lookup, and the lookups after them wait their turn.
const lookupWorkers = 8

// lookupWait is how long an asker waits for a lookup. Past it the asker
// answers "unknown" and the lookup goes on: its answer is kept when it lands.
// A PATH entry on a mount that stalls (a 9P share under /mnt/c on WSL) can
// hold a lookup for minutes.
const lookupWait = 2 * time.Second

// lookupEntry is an answer. It is fresh for lookupTTL from when it arrived
// (at), however long its lookup took, so the retry a slow lookup's refusal
// asks for finds it; of two answers about one program, the one whose lookup
// started later (started) is kept.
type lookupEntry struct {
	path    string
	ok      bool
	at      time.Time // when the answer arrived
	started time.Time // when its lookup started
}

// lookupCall is a lookup under way, run by a goroutine of its own (run). Its
// entry, and known, are set before done is closed; known is false when the
// lookup gave no answer (it panicked).
type lookupCall struct {
	done  chan struct{}
	at    time.Time
	look  func(program string) (string, error)
	e     lookupEntry
	known bool
}

// lookupCache answers whether programs resolve on this server, the way exec
// resolves them when a session launches (exec.LookPath, the check of POST
// /api/catalog/check), keeping each answer for lookupTTL. GET /api/catalog
// marks every agent with it and a crew launch refuses a member whose program
// it cannot find. Nothing is run.
//
// A lookup runs in a goroutine of its own, without the lock: on a slow PATH
// (a stat per directory, about 90 ms for a missing program on a WSL PATH that
// reaches into /mnt/c) it holds up only the askers of that program, and those
// for at most lookupWait, or until their request ends. Askers of a program
// whose lookup is under way wait for it rather than start another.
type lookupCache struct {
	mu       sync.Mutex
	entries  map[string]lookupEntry
	inflight map[string]*lookupCall
	look     func(program string) (string, error) // exec.LookPath; tests replace it
	now      func() time.Time
	// wsl says the server runs inside a WSL distribution, where Windows's
	// programs appear on the PATH under /mnt through interop: those are not
	// installed here (WSL_DISTRO_NAME; tests set it).
	wsl  bool
	wait time.Duration // lookupWait; tests shorten it
	sem  chan struct{} // a slot per lookup under way, lookupWorkers of them
}

func newLookupCache() *lookupCache {
	return &lookupCache{
		entries:  map[string]lookupEntry{},
		inflight: map[string]*lookupCall{},
		look:     exec.LookPath,
		now:      time.Now,
		wsl:      os.Getenv("WSL_DISTRO_NAME") != "",
		wait:     lookupWait,
		sem:      make(chan struct{}, lookupWorkers),
	}
}

// found reports whether program resolves, and to what, asking the system at
// most once per lookupTTL for each program. known is false when the answer
// did not come within lookupWait, or before ctx ended: the lookup goes on,
// and its answer is kept for the next asker.
func (c *lookupCache) found(ctx context.Context, program string) (path string, ok, known bool) {
	c.mu.Lock()
	if e, ok := c.entries[program]; ok && c.now().Sub(e.at) < lookupTTL {
		c.mu.Unlock()
		return e.path, e.ok, true
	}
	call, joined := c.inflight[program]
	if !joined {
		call = c.begin(program)
	}
	wait := c.wait
	c.mu.Unlock()
	if !joined {
		go c.run(program, call)
	}
	return await(ctx, call, wait)
}

// check asks the system now, whatever the cache holds or has under way, and
// keeps the answer: the add-agent form's check, which the catalog then agrees
// with. It waits as found does.
func (c *lookupCache) check(ctx context.Context, program string) (path string, ok, known bool) {
	c.mu.Lock()
	call := c.begin(program)
	wait := c.wait
	c.mu.Unlock()
	go c.run(program, call)
	return await(ctx, call, wait)
}

// await waits for call's answer, at most wait and no longer than ctx lasts;
// past either, known is false.
func await(ctx context.Context, call *lookupCall, wait time.Duration) (path string, ok, known bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-call.done:
		return call.e.path, call.e.ok, call.known
	case <-ctx.Done():
	case <-timer.C:
	}
	return "", false, false
}

// installed reports whether a session of program could start on this server.
// A relative path with a separator (./agent.sh, bin/agent) counts without a
// lookup: exec resolves it against the session's working directory, which the
// server's own cannot stand for. A bare name is looked up on the PATH, and any
// program of that name counts; an absolute path is looked up as it is. A
// lookup that does not answer in time (found's known false) counts as well,
// for a listing; a launch asks installedKnown, which tells it apart.
func (c *lookupCache) installed(ctx context.Context, program string) bool {
	ok, known := c.installedKnown(ctx, program)
	return ok || !known
}

// installedKnown is installed with whether the lookup answered in time
// (known): a launch refuses a program it could not show to be installed.
func (c *lookupCache) installedKnown(ctx context.Context, program string) (ok, known bool) {
	if relativePath(program) {
		return true, true
	}
	path, ok, known := c.found(ctx, program)
	return ok && known && !c.onWindows(path), known
}

// onWindows says whether a resolved path is a Windows program seen from
// inside WSL (under /mnt, by interop): found, but not installed here.
func (c *lookupCache) onWindows(path string) bool {
	return c.wsl && strings.HasPrefix(path, "/mnt/")
}

// relativePath reports whether program names a file relative to the working
// directory rather than a program on the PATH.
func relativePath(program string) bool {
	return strings.ContainsAny(program, "/"+string(filepath.Separator)) && !filepath.IsAbs(program)
}

// installedAnswer is what installedKnown says of a program.
type installedAnswer struct{ ok, known bool }

// warm reports whether each of programs is installed, as installedKnown
// does, asking about them all at once: the lookups of those with no fresh
// answer run together, at most lookupWorkers at a time, so a launch of N
// uncached programs takes about one lookup's time, not N, and never much
// more than lookupWait.
func (c *lookupCache) warm(ctx context.Context, programs []string) map[string]installedAnswer {
	var mu sync.Mutex
	var wg sync.WaitGroup
	answers := make(map[string]installedAnswer, len(programs))
	seen := map[string]bool{}
	for _, p := range programs {
		if seen[p] {
			continue
		}
		seen[p] = true
		wg.Go(func() {
			ok, known := c.installedKnown(ctx, p)
			mu.Lock()
			answers[p] = installedAnswer{ok, known}
			mu.Unlock()
		})
	}
	wg.Wait()
	return answers
}

// lookupAnswer is what warmPaths says of a program.
type lookupAnswer struct {
	installed bool   // as installed says
	path      string // the resolved absolute path, when the lookup found one
	// onWindows is the path when it is a Windows program seen from inside
	// WSL, which does not count as installed.
	onWindows string
}

// warmPaths is warm with the resolved paths: what the identity probes run.
func (c *lookupCache) warmPaths(ctx context.Context, programs []string) map[string]lookupAnswer {
	var mu sync.Mutex
	var wg sync.WaitGroup
	answers := make(map[string]lookupAnswer, len(programs))
	seen := map[string]bool{}
	for _, p := range programs {
		if seen[p] {
			continue
		}
		seen[p] = true
		wg.Go(func() {
			a := lookupAnswer{installed: true}
			if !relativePath(p) {
				path, ok, known := c.found(ctx, p)
				a.installed = ok || !known
				if ok && known {
					a.path = path
					if c.onWindows(path) {
						a.installed, a.onWindows = false, path
					}
				}
			}
			mu.Lock()
			answers[p] = a
			mu.Unlock()
		})
	}
	wg.Wait()
	return answers
}

// held is what the cache holds of program, whatever the answer's age, as
// warmPaths answers it: it starts no lookup and waits for none. A program
// it holds nothing of counts as installed, as for a lookup that did not
// answer in time, with no path.
func (c *lookupCache) held(program string) lookupAnswer {
	c.mu.Lock()
	e, had := c.entries[program]
	c.mu.Unlock()
	a := lookupAnswer{installed: !had || e.ok}
	if had && e.ok {
		a.path = e.path
		if c.onWindows(e.path) {
			a.installed, a.onWindows = false, e.path
		}
	}
	return a
}

// begin registers a lookup of program as the one under way, which later
// askers wait for; the caller starts it (run). The caller holds c.mu.
func (c *lookupCache) begin(program string) *lookupCall {
	call := &lookupCall{done: make(chan struct{}), at: c.now(), look: c.look}
	c.inflight[program] = call
	return call
}

// run looks program up once a slot is free, without c.mu, and then settles
// the call. Both happen on the way out, whatever the lookup does: one that
// panics gives its slot back and leaves no call under way for later askers
// to join, and wakes its askers with no answer.
func (c *lookupCache) run(program string, call *lookupCall) {
	var e lookupEntry
	known := false
	defer func() { c.settle(program, call, e, known) }()
	c.sem <- struct{}{}
	defer func() { <-c.sem }()
	path, err := call.look(program)
	e, known = lookupEntry{path: path, ok: err == nil, started: call.at}, true
}

// settle takes call off the lookups under way and, when it has an answer
// (known), keeps it, dropping the answers that have expired so the cache
// holds only programs asked about lately; then it wakes the askers waiting
// for it. Of two lookups of one program at once, the one that started later
// decides: an earlier one that ends after it answers with the later one's
// answer.
func (c *lookupCache) settle(program string, call *lookupCall, e lookupEntry, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight[program] == call {
		delete(c.inflight, program)
	}
	if known {
		e.at = c.now()
		old, had := c.entries[program]
		for p, o := range c.entries {
			if e.at.Sub(o.at) >= lookupTTL {
				delete(c.entries, p)
			}
		}
		switch {
		case had && old.started.After(e.started):
			e = old
		case had || len(c.entries) < maxLookups:
			c.entries[program] = e
		}
		call.e, call.known = e, true
	}
	close(call.done)
}
