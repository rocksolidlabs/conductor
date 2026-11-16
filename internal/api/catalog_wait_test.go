package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// agentsByID is a catalog reply's agents by their id.
func agentsByID(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	list, _ := out["agents"].([]any)
	by := map[string]map[string]any{}
	for _, a := range list {
		m := a.(map[string]any)
		by[m["id"].(string)] = m
	}
	return by
}

// getAgent reads GET /api/catalog/{id}, for a goroutine (no t.Fatal).
func (e *testEnv) getAgent(id string) (int, map[string]any, error) {
	req, _ := http.NewRequest("GET", e.http.URL+"/api/catalog/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return resp.StatusCode, nil, err
	}
	agent, _ := out["agent"].(map[string]any)
	return resp.StatusCode, agent, nil
}

// The Agents page's read (#704): GET /api/catalog?wait=0 lists every agent
// at once, however long an agent's check takes, the agent whose probe is
// under way marked checking, and GET /api/catalog/{id} waits for that probe
// and answers with what it found. Every wait is set an hour long: a read
// that waited for the held probe would hang the test, never pass it.
func TestTheCatalogListsAtOnceWhileAProbeIsHeld(t *testing.T) {
	fast := versionScript(t, "claude", "2.1.287 (Claude Code)")
	held := versionScript(t, "claude", "2.1.287 (Claude Code)")
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "fast", Name: "Fast", Command: []string{fast}, Adapter: "claude"},
		{ID: "held", Name: "Held", Command: []string{held}, Adapter: "claude"},
	})
	gate, started := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	var startOnce sync.Once
	e.srv.probes.wait = time.Hour
	e.srv.lookups.wait = time.Hour
	e.srv.probes.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		if command[0] == held {
			startOnce.Do(func() { close(started) })
			<-gate
		}
		return r.Run(ctx, command, p, env)
	}

	// The quick read lists both, whatever is known yet.
	resp, out := e.do("GET", "/api/catalog?wait=0", adminToken, nil)
	if by := agentsByID(t, out); resp.StatusCode != http.StatusOK || by["fast"] == nil || by["held"] == nil {
		t.Fatalf("wait=0: %d %v", resp.StatusCode, out)
	}
	// The fast agent's read waits for its checks and answers with them.
	if code, a, err := e.getAgent("fast"); err != nil || code != http.StatusOK || a["checking"] != nil || a["identity"].(map[string]any)["identified"] != true {
		t.Fatalf("fast: %d %v %v", code, a, err)
	}
	// The held agent's read waits for its probe, which the gate holds.
	type answer struct {
		code  int
		agent map[string]any
		err   error
	}
	answered := make(chan answer, 1)
	go func() {
		code, a, err := e.getAgent("held")
		answered <- answer{code, a, err}
	}()
	<-started
	// Meanwhile the quick read lists every agent at once: the fast one
	// known, the held one marked checking, its probe pending.
	resp, out = e.do("GET", "/api/catalog?wait=0", adminToken, nil)
	by := agentsByID(t, out)
	if resp.StatusCode != http.StatusOK || len(by) != 5 {
		t.Fatalf("wait=0 while held: %d %v", resp.StatusCode, out)
	}
	if by["fast"]["checking"] != nil || by["fast"]["identity"].(map[string]any)["identified"] != true {
		t.Fatalf("fast while held: %v", by["fast"])
	}
	if h := by["held"]; h["checking"] != true || h["available"] != true || h["identity"].(map[string]any)["pending"] != true {
		t.Fatalf("held while held: %v", h)
	}
	select {
	case a := <-answered:
		t.Fatalf("the held agent's read answered before its probe: %+v", a)
	default:
	}
	release.Do(func() { close(gate) })
	a := <-answered
	if a.err != nil || a.code != http.StatusOK || a.agent["checking"] != nil || a.agent["identity"].(map[string]any)["identified"] != true {
		t.Fatalf("held after the release: %+v", a)
	}
	_, out = e.do("GET", "/api/catalog?wait=0", adminToken, nil)
	if h := agentsByID(t, out)["held"]; h["checking"] != nil || h["identity"].(map[string]any)["identified"] != true {
		t.Fatalf("held, listed after the release: %v", h)
	}
}

// A program whose lookup is under way is checking too, with no identity
// yet (its probe waits for the path), and counts as available as before.
func TestTheCatalogMarksAProgramStillLookedUpAsChecking(t *testing.T) {
	slow := versionScript(t, "claude", "2.1.287 (Claude Code)")
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "slow", Name: "Slow", Command: []string{slow}, Adapter: "claude"},
	})
	gate, started := make(chan struct{}), make(chan struct{})
	var release, startOnce sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	e.srv.lookups.wait = time.Hour
	e.srv.lookups.look = func(program string) (string, error) {
		if program == slow {
			startOnce.Do(func() { close(started) })
			<-gate
		}
		return exec.LookPath(program)
	}
	e.do("GET", "/api/catalog?wait=0", adminToken, nil)
	<-started
	// The other agents' lookups answered: their reads wait for them.
	for _, id := range []string{"cat", "sh", "exit"} {
		if code, a, err := e.getAgent(id); err != nil || code != http.StatusOK || a["checking"] != nil {
			t.Fatalf("%s: %d %v %v", id, code, a, err)
		}
	}
	_, out := e.do("GET", "/api/catalog?wait=0", adminToken, nil)
	by := agentsByID(t, out)
	if s := by["slow"]; s["checking"] != true || s["available"] != true || s["identity"] != nil {
		t.Fatalf("slow while looked up: %v", s)
	}
	// An agent needing no lookup, or one answered, is not checking.
	for _, id := range []string{"cat", "sh", "exit"} {
		if by[id]["checking"] != nil {
			t.Fatalf("%s: %v", id, by[id])
		}
	}
	release.Do(func() { close(gate) })
	code, a, err := e.getAgent("slow")
	if err != nil || code != http.StatusOK || a["checking"] != nil || a["identity"].(map[string]any)["identified"] != true {
		t.Fatalf("slow after the release: %d %v %v", code, a, err)
	}
}

// A program whose lookup's answer has expired keeps it while it is looked
// up again: the quick read shows what was known, its probe's identity too,
// not every agent checking each time the page opens past lookupTTL.
func TestTheQuickCatalogKeepsAnExpiredLookupWhileItIsLookedUpAgain(t *testing.T) {
	prog := versionScript(t, "claude", "2.1.287 (Claude Code)")
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "kept", Name: "Kept", Command: []string{prog}, Adapter: "claude"},
	})
	gate, started := make(chan struct{}), make(chan struct{})
	var release, startOnce sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	var later atomic.Int64
	var hold atomic.Bool
	start := time.Now()
	e.srv.lookups.now = func() time.Time { return start.Add(time.Duration(later.Load())) }
	e.srv.lookups.wait = time.Hour
	e.srv.probes.wait = time.Hour
	e.srv.lookups.look = func(program string) (string, error) {
		if program == prog && hold.Load() {
			startOnce.Do(func() { close(started) })
			<-gate
		}
		return exec.LookPath(program)
	}
	// The full read waits for the lookup and the probe: both known.
	_, out := e.do("GET", "/api/catalog", adminToken, nil)
	if k := agentsByID(t, out)["kept"]; k["checking"] != nil || k["identity"].(map[string]any)["identified"] != true {
		t.Fatalf("kept, known: %v", k)
	}
	hold.Store(true)
	later.Store(int64(lookupTTL + time.Second))
	_, out = e.do("GET", "/api/catalog?wait=0", adminToken, nil)
	// The quick read started the lookup again, which the gate holds, and
	// answered with the expired answer meanwhile.
	<-started
	if k := agentsByID(t, out)["kept"]; k["checking"] != nil || k["recheck"] != true || k["available"] != true || k["identity"].(map[string]any)["identified"] != true {
		t.Fatalf("kept, its answer expired: %v", k)
	}
	release.Do(func() { close(gate) })
}

// A program missing when it was last looked up and installed since, its
// answer expired: the quick read shows it missing, as the kept answer says,
// and marks it recheck; the agent's own read, which the page makes on that
// same opening, finds it installed.
func TestTheQuickCatalogMarksAnExpiredAnswerForARecheck(t *testing.T) {
	prog := versionScript(t, "claude", "2.1.287 (Claude Code)")
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "late", Name: "Late", Command: []string{prog}, Adapter: "claude"},
	})
	gate, started := make(chan struct{}), make(chan struct{})
	var release, startOnce sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	var later atomic.Int64
	var installed atomic.Bool
	start := time.Now()
	e.srv.lookups.now = func() time.Time { return start.Add(time.Duration(later.Load())) }
	e.srv.lookups.wait = time.Hour
	e.srv.probes.wait = time.Hour
	e.srv.lookups.look = func(program string) (string, error) {
		if program != prog {
			return exec.LookPath(program)
		}
		if !installed.Load() {
			return "", exec.ErrNotFound
		}
		startOnce.Do(func() { close(started) })
		<-gate
		return exec.LookPath(program)
	}
	_, out := e.do("GET", "/api/catalog", adminToken, nil)
	if l := agentsByID(t, out)["late"]; l["available"] != false || l["checking"] != nil || l["recheck"] != nil {
		t.Fatalf("late, missing: %v", l)
	}
	installed.Store(true)
	later.Store(int64(lookupTTL + time.Second))
	_, out = e.do("GET", "/api/catalog?wait=0", adminToken, nil)
	<-started
	if l := agentsByID(t, out)["late"]; l["available"] != false || l["checking"] != nil || l["recheck"] != true {
		t.Fatalf("late, its answer expired: %v", l)
	}
	release.Do(func() { close(gate) })
	code, a, err := e.getAgent("late")
	if err != nil || code != http.StatusOK || a["available"] != true || a["checking"] != nil || a["recheck"] != nil || a["identity"].(map[string]any)["identified"] != true {
		t.Fatalf("late, read again: %d %v %v", code, a, err)
	}
}

// The reads refuse what they do not know: a wait other than 0, an agent the
// catalog does not list (a hidden one included).
func TestTheCatalogReadsRefuseWhatTheyDoNotKnow(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, w := range []string{"1", "2s", "-1"} {
		if resp, _ := e.do("GET", "/api/catalog?wait="+w, adminToken, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("wait=%s: %d", w, resp.StatusCode)
		}
	}
	if code, _, err := e.getAgent("nope"); err != nil || code != http.StatusNotFound {
		t.Fatalf("unknown: %d %v", code, err)
	}
	if code, a, err := e.getAgent("cat"); err != nil || code != http.StatusOK || a["id"] != "cat" || a["available"] != true {
		t.Fatalf("cat: %d %v %v", code, a, err)
	}
	if resp, _ := e.do("DELETE", "/api/catalog/cat", adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("hide: %d", resp.StatusCode)
	}
	if code, _, err := e.getAgent("cat"); err != nil || code != http.StatusNotFound {
		t.Fatalf("hidden: %d %v", code, err)
	}
	if resp, _ := e.do("GET", "/api/catalog/cat", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	// An agent read from the listing saves as it is: its checking is taken
	// and ignored, as its available is.
	_, sh, _ := e.getAgent("sh")
	sh["checking"], sh["recheck"] = true, true
	if resp, out := e.do("POST", "/api/catalog", adminToken, sh); resp.StatusCode != http.StatusOK {
		t.Fatalf("save with checking and recheck: %d %v", resp.StatusCode, out)
	}
}
