package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// stub replaces the system lookup and forgets every answer, under the cache's
// lock, so a running server sees the change safely.
func (c *lookupCache) stub(look func(program string) (string, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.look = look
	c.entries = map[string]lookupEntry{}
}

// hangWait is a lookup wait only a hang reaches. lookupWait (2 s) is the
// product's bound, past which an unanswered lookup counts as installed; a
// test that compares what servers say of a program needs their answers, which
// a loaded machine can take longer than that to give.
const hangWait = time.Minute

// stubWait sets how long askers wait for a lookup, under the cache's lock.
func (c *lookupCache) stubWait(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wait = d
}

// kept returns the answer the cache keeps about program, if any.
func (c *lookupCache) kept(program string) (lookupEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[program]
	return e, ok
}

// underWay reports whether a lookup of program is registered as under way.
func (c *lookupCache) underWay(program string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.inflight[program]
	return ok
}

// waitKept waits up to d for the cache to keep an answer about program.
func waitKept(t *testing.T, c *lookupCache, program string, d time.Duration) lookupEntry {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if e, ok := c.kept(program); ok {
			return e
		}
	}
	t.Fatalf("no answer about %s kept after %v", program, d)
	return lookupEntry{}
}

// The cache asks the system once per program and again after 30 s, so a CLI
// installed while the server runs shows up without a restart; a check asks at
// once and the cache keeps its answer.
func TestLookupCacheExpires(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	asked := map[string]int{}
	present := map[string]bool{"cat": true}
	c := newLookupCache()
	c.now = func() time.Time { return now }
	c.look = func(program string) (string, error) {
		asked[program]++
		if present[program] {
			return "/bin/" + program, nil
		}
		return "", errors.New("not found")
	}
	if p, ok, known := c.found(ctx, "cat"); !ok || !known || p != "/bin/cat" {
		t.Fatalf("cat: %q %v %v", p, ok, known)
	}
	if _, ok, known := c.found(ctx, "ghost"); ok || !known {
		t.Fatalf("ghost: %v %v", ok, known)
	}
	c.found(ctx, "cat")
	c.found(ctx, "ghost")
	if asked["cat"] != 1 || asked["ghost"] != 1 {
		t.Fatalf("asked twice within the TTL: %v", asked)
	}
	present["ghost"] = true
	if p, ok, _ := c.check(ctx, "ghost"); !ok || p != "/bin/ghost" || asked["ghost"] != 2 {
		t.Fatalf("a check asks at once: %q %v %v", p, ok, asked)
	}
	if _, ok, _ := c.found(ctx, "ghost"); !ok || asked["ghost"] != 2 {
		t.Fatalf("the check's answer is kept: %v", asked)
	}
	delete(present, "cat")
	now = now.Add(lookupTTL + time.Second)
	if _, ok, _ := c.found(ctx, "cat"); ok || asked["cat"] != 2 {
		t.Fatalf("after the TTL cat should be looked up again: %v", asked)
	}
	if len(c.entries) != 1 {
		t.Fatalf("expired answers are dropped: %v", c.entries)
	}
}

// Askers of one program at once wait for one lookup: those that come while it
// runs share its answer, and those that come after find it kept.
func TestLookupCacheCoalescesConcurrentAskers(t *testing.T) {
	c := newLookupCache()
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	c.look = func(program string) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "/bin/" + program, nil
	}
	var wg sync.WaitGroup
	found := make([]bool, 10)
	for i := range found {
		wg.Go(func() { _, found[i], _ = c.found(t.Context(), "slow") })
	}
	<-started
	time.Sleep(50 * time.Millisecond) // most askers reach the lookup in flight; a later one finds the answer kept
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d lookups for 10 askers of one program", n)
	}
	for i, ok := range found {
		if !ok {
			t.Fatalf("asker %d: not found", i)
		}
	}
}

// At most maxLookups answers are kept: past the bound an answer is given and
// not kept, nothing is evicted, and a program already kept is still
// refreshed. Answers that expire make room.
func TestLookupCacheKeepsAtMostMaxLookups(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	asked := map[string]int{}
	c := newLookupCache()
	c.now = func() time.Time { return now }
	c.look = func(program string) (string, error) {
		asked[program]++
		return "/bin/" + program, nil
	}
	for i := range maxLookups {
		c.found(ctx, fmt.Sprintf("p%d", i))
	}
	if len(c.entries) != maxLookups {
		t.Fatalf("%d answers kept, want %d", len(c.entries), maxLookups)
	}
	if p, ok, _ := c.found(ctx, "extra"); !ok || p != "/bin/extra" {
		t.Fatalf("the answer past the bound: %q %v", p, ok)
	}
	c.found(ctx, "extra")
	if asked["extra"] != 2 || len(c.entries) != maxLookups {
		t.Fatalf("the answer past the bound was kept: asked %d, %d kept", asked["extra"], len(c.entries))
	}
	now = now.Add(time.Second)
	c.check(ctx, "p0")
	if c.entries["p0"].at != now {
		t.Fatalf("a program already kept is not refreshed at the bound: %v", c.entries["p0"])
	}
	now = now.Add(lookupTTL)
	c.found(ctx, "extra")
	if _, kept := c.entries["extra"]; !kept || len(c.entries) != 1 {
		t.Fatalf("expired answers do not make room: %d kept", len(c.entries))
	}
}

// A lookup that started earlier and ends later does not replace the answer
// of one that started after it: a found under way when the CLI is installed
// does not undo the add-agent form's check that sees it.
func TestLookupCacheKeepsTheNewerAnswer(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	c := newLookupCache()
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	c.look = func(program string) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return "", errors.New("not found") // from before the install
		}
		return "/bin/" + program, nil
	}
	older := make(chan bool)
	go func() {
		_, ok, _ := c.found(t.Context(), "agent")
		older <- ok
	}()
	<-started
	mu.Lock()
	now = now.Add(time.Second)
	mu.Unlock()
	if p, ok, _ := c.check(t.Context(), "agent"); !ok || p != "/bin/agent" {
		t.Fatalf("check: %q %v", p, ok)
	}
	close(release)
	if !<-older {
		t.Fatal("the older found answered with its own, older answer")
	}
	if _, ok, _ := c.found(t.Context(), "agent"); !ok || calls.Load() != 2 {
		t.Fatalf("the check's answer was replaced: %v, %d lookups", ok, calls.Load())
	}
}

// A lookup that does not answer within lookupWait (2 s; a PATH entry on a
// stalled 9P mount, here a lookup held for as long as the test likes) leaves
// its askers with "unknown" after 2 s, which installed counts, while the
// lookup goes on: its answer is kept when it lands, and the next asker finds
// it without another lookup.
func TestLookupAnswersUnknownAfterTheWait(t *testing.T) {
	if lookupWait != 2*time.Second {
		t.Fatalf("lookupWait is %v", lookupWait)
	}
	c := newLookupCache()
	var calls atomic.Int32
	release := make(chan struct{})
	c.look = func(program string) (string, error) {
		calls.Add(1)
		select { // a lookup that takes 5 s, unless the test lets it end sooner
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return "/opt/" + program, nil
	}
	start := time.Now()
	_, ok, known := c.found(t.Context(), "stalled")
	took := time.Since(start)
	if ok || known || took < lookupWait-100*time.Millisecond || took > lookupWait+time.Second {
		t.Fatalf("found %v known %v after %v; want unknown after about %v", ok, known, took, lookupWait)
	}
	if !c.underWay("stalled") {
		t.Fatal("the lookup stopped with its asker")
	}
	close(release)
	if e := waitKept(t, c, "stalled", 2*time.Second); !e.ok || e.path != "/opt/stalled" {
		t.Fatalf("kept %+v", e)
	}
	if p, ok, known := c.found(t.Context(), "stalled"); !ok || !known || p != "/opt/stalled" || calls.Load() != 1 {
		t.Fatalf("after it landed: %q %v %v, %d lookups", p, ok, known, calls.Load())
	}
}

// An asker whose context ends (the client went away) stops waiting at once,
// before the context ends as well as while it waits, and answers "unknown";
// installed counts it, a check says so. The lookup's answer is kept.
func TestLookupLetsACancelledAskerGo(t *testing.T) {
	c := newLookupCache()
	release := make(chan struct{})
	c.look = func(program string) (string, error) {
		<-release
		return "", errors.New("not found")
	}
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if _, ok, known := c.found(gone, "slow"); ok || known {
		t.Fatalf("found with a cancelled context: %v %v", ok, known)
	}
	if !c.installed(gone, "slow") {
		t.Fatal("installed: unknown is not counted")
	}
	if _, ok, known := c.check(gone, "slow"); ok || known {
		t.Fatalf("check with a cancelled context: %v %v", ok, known)
	}
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("a cancelled asker waited %v", took)
	}
	leaving, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, _, known := c.found(leaving, "slow"); known {
		t.Fatal("known while the lookup is held")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("an asker cancelled while it waited returned after %v", took)
	}
	close(release)
	if e := waitKept(t, c, "slow", 2*time.Second); e.ok {
		t.Fatalf("kept %+v", e)
	}
	if c.installed(t.Context(), "slow") {
		t.Fatal("the missing program counts as installed once its answer is kept")
	}
}

// A lookup that panics takes itself off the lookups under way and gives its
// slot back on the way out, so later askers start a lookup of their own
// rather than join one that never ends; its askers are woken with no answer,
// and nothing is kept.
func TestLookupPanicLeavesNoLookupUnderWay(t *testing.T) {
	c := newLookupCache()
	c.look = func(string) (string, error) { panic("boom") }
	c.mu.Lock()
	call := c.begin("prog")
	c.mu.Unlock()
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Fatalf("recovered %v", r)
			}
		}()
		c.run("prog", call)
	}()
	select {
	case <-call.done:
	default:
		t.Fatal("the askers of the panicked lookup are not woken")
	}
	if call.known {
		t.Fatal("the panicked lookup has an answer")
	}
	if c.underWay("prog") {
		t.Fatal("the panicked lookup is still under way")
	}
	if _, ok := c.kept("prog"); ok {
		t.Fatal("the panicked lookup kept an answer")
	}
	if n := len(c.sem); n != 0 {
		t.Fatalf("%d lookup slots still taken", n)
	}
	c.stub(func(program string) (string, error) { return "/bin/" + program, nil })
	if p, ok, known := c.found(t.Context(), "prog"); !ok || !known || p != "/bin/prog" {
		t.Fatalf("after the panic: %q %v %v", p, ok, known)
	}
}

// A program named by a relative path with a separator (./agent.sh, bin/agent)
// counts as installed without a lookup: it resolves against the session's
// directory, which the server's own cannot stand for. A bare name and an
// absolute path are looked up.
func TestLookupCacheInstalledTakesARelativePath(t *testing.T) {
	c := newLookupCache()
	var mu sync.Mutex
	var asked []string
	c.look = func(program string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, program)
		return "", errors.New("not found")
	}
	for _, p := range []string{"./x", "sub/x", "../x"} {
		if !c.installed(t.Context(), p) {
			t.Fatalf("%s: not installed", p)
		}
	}
	for _, p := range []string{"x", "/abs/x"} {
		if c.installed(t.Context(), p) {
			t.Fatalf("%s: installed", p)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("looked up %q", asked)
	}
}

// The catalog, a crew launch and the add-agent form's check agree on a
// relative program: listed as available, not refused, and reported by the
// check as judged at launch (unknown "relative"), without a lookup.
func TestCheckLaunchTakesARelativeProgram(t *testing.T) {
	e := newTestEnv(t, nil)
	var asked atomic.Int32
	e.srv.lookups.stub(func(program string) (string, error) {
		if strings.Contains(program, "/") && !strings.HasPrefix(program, "/") {
			asked.Add(1)
		}
		return "", errors.New("not found")
	})
	for id, program := range map[string]string{"dot": "./x", "sub": "sub/x", "up": "../x"} {
		a := agentBody(id)
		a["command"] = []string{program}
		if out := e.save(a); out["agent"].(map[string]any)["available"] != true {
			t.Fatalf("%s: %v", program, out)
		}
		if got := e.catalogAgent(id); got["available"] != true {
			t.Fatalf("%s listed: %v", program, got)
		}
		members := []crew.Member{{Name: "m", AgentID: id}}
		cat := e.srv.Catalog()
		if err := checkLaunch(members, cat, e.srv.installedFor(t.Context(), cat, members), nil); err != nil {
			t.Fatalf("%s: %v", program, err)
		}
		_, out := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{program}})
		if _, hasPath := out["path"]; out["found"] != false || out["unknown"] != "relative" || hasPath {
			t.Fatalf("%s checked: %v", program, out)
		}
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("%d lookups of a relative program", n)
	}
}

// A listing looks up the programs it has no answer for at once, at most
// lookupWorkers at a time, each once: a catalog of N uncached programs takes
// about one lookup's time, not N.
func TestCatalogLooksUpConcurrently(t *testing.T) {
	e := newTestEnv(t, nil)
	for i := range 8 {
		a := agentBody(fmt.Sprintf("slow-%d", i))
		a["command"] = []string{fmt.Sprintf("slow-program-%d", i)}
		e.save(a)
	}
	const delay = 250 * time.Millisecond
	var calls, running, most atomic.Int32
	e.srv.lookups.stub(func(program string) (string, error) {
		calls.Add(1)
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(delay)
		running.Add(-1)
		return "/bin/" + program, nil
	})
	start := time.Now()
	list := e.catalogList()
	took := time.Since(start)
	programs := map[string]bool{}
	for _, raw := range list {
		a := raw.(map[string]any)
		programs[a["command"].([]any)[0].(string)] = true
		if a["available"] != true {
			t.Fatalf("%v: not available", a["id"])
		}
	}
	if int(calls.Load()) != len(programs) {
		t.Fatalf("%d lookups for %d programs", calls.Load(), len(programs))
	}
	if m := most.Load(); m < 2 || m > lookupWorkers {
		t.Fatalf("%d lookups ran at once, want 2 to %d", m, lookupWorkers)
	}
	if serial := time.Duration(len(programs)) * delay; took > serial/2 {
		t.Fatalf("listing %d uncached programs took %v; one lookup takes %v, all in a row %v", len(programs), took, delay, serial)
	}
}

// Programs whose lookups stall do not hold up the catalog, a crew launch or
// the add-agent form's check past the wait: the catalog answers once, in
// about one wait, listing them as available; a launch of members on them is
// allowed (the spawn still fails if a program is missing); the check says
// unknown "timeout". When the lookups land, their answers are kept and the
// catalog says what they found.
func TestLookupsThatStallAnswerUnknownInTime(t *testing.T) {
	e := newTestEnv(t, nil)
	const wait = 300 * time.Millisecond
	const stalled = 5
	var members []crew.Member
	for i := range stalled {
		id := fmt.Sprintf("stall-%d", i)
		a := agentBody(id)
		a["command"] = []string{"stalled-program-" + id}
		e.save(a)
		members = append(members, crew.Member{Name: id, AgentID: id})
	}
	release := make(chan struct{})
	e.srv.lookups.stubWait(wait)
	e.srv.lookups.stub(func(program string) (string, error) {
		if strings.HasPrefix(program, "stalled-program-") {
			<-release
			return "", errors.New("not found")
		}
		return "/bin/" + program, nil
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	start := time.Now()
	list := e.catalogList()
	if took := time.Since(start); took > stalled*wait/2 {
		t.Fatalf("the catalog took %v with %d stalled lookups of %v each", took, stalled, wait)
	}
	for _, raw := range list {
		if a := raw.(map[string]any); strings.HasPrefix(a["id"].(string), "stall-") && a["available"] != true {
			t.Fatalf("%v: a stalled lookup is listed as not available", a["id"])
		}
	}
	start = time.Now()
	cat := e.srv.Catalog()
	if err := checkLaunch(members, cat, e.srv.installedFor(t.Context(), cat, members), nil); err != nil {
		t.Fatalf("a launch on stalled lookups: %v", err)
	}
	if took := time.Since(start); took > stalled*wait/2 {
		t.Fatalf("the launch check took %v", took)
	}
	_, out := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{"stalled-program-stall-0"}})
	if out["found"] != false || out["unknown"] != "timeout" {
		t.Fatalf("check: %v", out)
	}
	close(release)
	for i := range stalled {
		waitKept(t, e.srv.lookups, fmt.Sprintf("stalled-program-stall-%d", i), 2*time.Second)
	}
	if got := e.catalogAgent("stall-0"); got["available"] != false {
		t.Fatalf("after the lookups landed: %v", got)
	}
	err := checkLaunch(members[:1], cat, e.srv.installedFor(t.Context(), cat, members[:1]), nil)
	if !errors.Is(err, crew.ErrInvalid) || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("a launch after the lookups landed: %v", err)
	}
}

// Inside WSL a program resolved under /mnt is Windows's, seen through
// interop: found, named, and not installed here.
func TestAProgramUnderMntIsNotInstalledInsideWSL(t *testing.T) {
	c := newLookupCache()
	c.look = func(program string) (string, error) { return "/mnt/c/Users/me/AppData/Roaming/npm/" + program, nil }
	c.wsl = true
	if c.installed(context.Background(), "codex") {
		t.Fatal("a Windows codex counted as installed inside WSL")
	}
	if a := c.warmPaths(context.Background(), []string{"codex"})["codex"]; a.installed || a.onWindows != "/mnt/c/Users/me/AppData/Roaming/npm/codex" {
		t.Fatalf("warmPaths %+v", a)
	}
	plain := newLookupCache()
	plain.look = c.look
	plain.wsl = false
	if !plain.installed(context.Background(), "codex") || plain.warmPaths(context.Background(), []string{"codex"})["codex"].onWindows != "" {
		t.Fatal("outside WSL a /mnt path is a program like any other")
	}
}
