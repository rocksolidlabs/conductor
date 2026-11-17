package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// versionScript writes a program that prints text on --version and
// otherwise behaves as cat, and returns its path.
func versionScript(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	body := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' " + shellQuote(text) + "; exit 0; fi\nexec /bin/cat\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// createCrew saves a crew and returns its id.
func (e *testEnv) createCrew(t *testing.T, body map[string]any) string {
	t.Helper()
	resp, out := e.do("POST", "/api/crews", adminToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create crew: %d %v", resp.StatusCode, out)
	}
	return out["crew"].(map[string]any)["id"].(string)
}

func identityEnv(t *testing.T) (*testEnv, string, string) {
	t.Helper()
	claude := versionScript(t, "claude", "2.1.287 (Claude Code)")
	goose := versionScript(t, "goose", "goose version: v3.22.1")
	// Claude's verified probe matching nothing: a release that prints its
	// version some new way, not a known other program.
	renamed := versionScript(t, "claude-next", "Claude Code build 3000")
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "claude", Name: "Claude Code", Command: []string{claude}, Adapter: "claude", AllowArgs: true},
		{ID: "goose", Name: "Goose", Command: []string{goose}, Adapter: "goose"},
		{ID: "quiet", Name: "Quiet", Command: []string{goose}, Adapter: "goose", Probe: new(bool)},
		{ID: "renamed", Name: "Claude Code next", Command: []string{renamed}, Adapter: "claude"},
	})
	return e, claude, goose
}

func TestCatalogListsIdentity(t *testing.T) {
	e, _, _ := identityEnv(t)
	_, out := e.do("GET", "/api/catalog", adminToken, nil)
	ids := map[string]map[string]any{}
	available := map[string]bool{}
	for _, a := range out["agents"].([]any) {
		m := a.(map[string]any)
		id, _ := m["identity"].(map[string]any)
		ids[m["id"].(string)] = id
		available[m["id"].(string)], _ = m["available"].(bool)
	}
	// The known impostor is not offered; the same program with its probe
	// off is, and so is a verified probe that matched nothing (warned).
	if available["goose"] || !available["quiet"] || !available["claude"] || !available["renamed"] {
		t.Fatalf("available %v", available)
	}
	if r := ids["renamed"]; r == nil || r["identified"] != false || r["impostor"] != nil || r["verified"] != true {
		t.Fatalf("renamed %v", r)
	}
	if c := ids["claude"]; c == nil || c["identified"] != true || c["version"] != "2.1.287" || c["name"] != "Claude Code" || c["verified"] != true {
		t.Fatalf("claude %v", ids["claude"])
	}
	if g := ids["goose"]; g == nil || g["identified"] != false || g["impostor"] != true || g["output"] != "goose version: v3.22.1" {
		t.Fatalf("goose %v", ids["goose"])
	}
	if ids["quiet"] != nil || ids["cat"] != nil {
		t.Fatalf("probe off or no adapter: quiet=%v cat=%v", ids["quiet"], ids["cat"])
	}
}

func TestCheckCommandReturnsIdentity(t *testing.T) {
	e, claude, goose := identityEnv(t)
	// A command with arguments of its own keeps them: bash stub.sh --version.
	stub := versionScript(t, "stub.sh", "ignored")
	_ = os.WriteFile(stub, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo '2.1.287 (Claude Code)'; fi\n"), 0o755)
	_, out0 := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{"/bin/bash", stub}, "adapter": "claude"})
	if id, _ := out0["identity"].(map[string]any); id == nil || id["identified"] != true {
		t.Fatalf("bash stub.sh: %v", out0)
	}
	_, out := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{claude}, "adapter": "claude"})
	id, _ := out["identity"].(map[string]any)
	if out["found"] != true || id == nil || id["identified"] != true || id["version"] != "2.1.287" {
		t.Fatalf("%v", out)
	}
	_, out = e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{goose}, "adapter": "claude"})
	id, _ = out["identity"].(map[string]any)
	if id == nil || id["identified"] != false || id["output"] != "goose version: v3.22.1" {
		t.Fatalf("another program under the claude adapter: %v", out)
	}
	_, out = e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{claude}})
	if _, has := out["identity"]; has {
		t.Fatalf("without an adapter: %v", out)
	}
	resp, _ := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{claude}, "adapter": "nope"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown adapter: %d", resp.StatusCode)
	}
}

func TestLaunchRefusesAMisidentifiedAgent(t *testing.T) {
	e, _, _ := identityEnv(t)
	crewID := e.createCrew(t, map[string]any{"name": "ident", "goal": "g", "cwd": e.root, "where": "server", "isolation": "none", "openAfterLaunch": false,
		"members": []map[string]any{{"name": "solo", "agentId": "goose", "prompt": "hi", "start": map[string]any{"when": "immediately"}}}})
	resp, out := e.do("POST", "/api/crews/"+crewID+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	msg := out["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, `agent "goose" on the server is not Goose`) || !strings.Contains(msg, "goose version: v3.22.1") {
		t.Fatalf("message %q", msg)
	}
	// The probe turned off: the same program launches.
	quiet := e.createCrew(t, map[string]any{"name": "quiet", "goal": "g", "cwd": e.root, "where": "server", "isolation": "none", "openAfterLaunch": false,
		"members": []map[string]any{{"name": "solo", "agentId": "quiet", "prompt": "hi", "start": map[string]any{"when": "immediately"}}}})
	resp, out = e.do("POST", "/api/crews/"+quiet+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("probe off: %d %v", resp.StatusCode, out)
	}
}

func TestLaunchAllowsAPendingOrUnverifiedProbe(t *testing.T) {
	e, _, goose := identityEnv(t)
	// Block's goose: identified. An adapter whose probe is not verified yet
	// (OpenCode) with a version line it does not know: unidentified, and so
	// refused nothing.
	ok := versionScript(t, "goose-ok", "goose 1.0.21")
	unknown := versionScript(t, "opencode-new", "OpenCode build 2027")
	cat := e.srv.Catalog()
	for _, a := range []catalog.Agent{
		{ID: "goose-ok", Name: "Goose", Command: []string{ok}, Adapter: "goose"},
		{ID: "goose-new", Name: "OpenCode", Command: []string{unknown}, Adapter: "opencode"},
	} {
		if err := cat.Upsert(a); err != nil {
			t.Fatal(err)
		}
	}
	e.srv.catalogMu.Lock()
	e.srv.catalog = cat
	e.srv.catalogMu.Unlock()
	for _, id := range []string{"goose-ok", "goose-new"} {
		crewID := e.createCrew(t, map[string]any{"name": id, "goal": "g", "cwd": e.root, "where": "server", "isolation": "none", "openAfterLaunch": false,
			"members": []map[string]any{{"name": "solo", "agentId": id, "prompt": "hi", "start": map[string]any{"when": "immediately"}}}})
		resp, out := e.do("POST", "/api/crews/"+crewID+"/launch", adminToken, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s: %d %v", id, resp.StatusCode, out)
		}
	}
	// A probe still under way refuses nothing.
	e.srv.probes.wait = 10 * time.Millisecond
	e.srv.probes.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		time.Sleep(300 * time.Millisecond)
		return r.Run(ctx, command, p, env)
	}
	e.srv.probes.entries = map[string]probeEntry{}
	slow := versionScript(t, "goose-slow", "goose version: v3.22.1")
	if err := cat.Upsert(catalog.Agent{ID: "goose-slow", Name: "Goose", Command: []string{slow}, Adapter: "goose"}); err != nil {
		t.Fatal(err)
	}
	crewID := e.createCrew(t, map[string]any{"name": "slow", "goal": "g", "cwd": e.root, "where": "server", "isolation": "none", "openAfterLaunch": false,
		"members": []map[string]any{{"name": "solo", "agentId": "goose-slow", "prompt": "hi", "start": map[string]any{"when": "immediately"}}}})
	resp, out := e.do("POST", "/api/crews/"+crewID+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pending: %d %v", resp.StatusCode, out)
	}
	_ = goose
}

func TestCreateSessionRefusesAKnownImpostor(t *testing.T) {
	e, _, _ := identityEnv(t)
	// Nothing cached yet: the launch is not held up by a probe, and goes.
	e.createSession("goose")
	e.do("GET", "/api/catalog", adminToken, nil) // warms the probes
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "goose"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	er := out["error"].(map[string]any)
	if msg, _ := er["message"].(string); er["code"] != "not_the_agent" || !strings.Contains(msg, "is another program, not Goose") || !strings.Contains(msg, "goose version: v3.22.1") {
		t.Fatalf("%v", er)
	}
	// The same program with its probe off launches.
	e.createSession("quiet")
}

func TestCreateSessionNotesAMisidentifiedAgent(t *testing.T) {
	e, _, _ := identityEnv(t)
	e.do("GET", "/api/catalog", adminToken, nil) // warms the probes
	id := e.createSession("renamed")
	d, _ := e.srv.Registry().Get(id)
	var found bool
	for _, entry := range d.(*session.Local).Activity() {
		if entry.Type == session.ActivityError && strings.Contains(entry.Message, "is not Claude Code") && strings.Contains(entry.Message, "Claude Code build 3000") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no note: %+v", d.(*session.Local).Activity())
	}
}

func TestProbeCacheExpiresAndJoinsInflight(t *testing.T) {
	c := newProbeCache()
	var runs atomic.Int32
	now := time.Now()
	c.now = func() time.Time { return now }
	c.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		runs.Add(1)
		time.Sleep(50 * time.Millisecond)
		return agents.ProbeResult{Ran: true, Identified: true, Version: "1.0.0"}
	}
	p := agents.Probe{Args: []string{"--version"}, Match: regexp.MustCompile(`(\d+)`), Verified: true}
	done := make(chan Identity, 3)
	for range 3 {
		go func() {
			done <- c.identity(context.Background(), []string{"/bin/x"}, "claude", "Claude Code", p, nil, false)
		}()
	}
	for range 3 {
		if id := <-done; !id.Identified || id.Version != "1.0.0" || id.Name != "Claude Code" || !id.Verified {
			t.Fatalf("%+v", id)
		}
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("ran %d times, want the askers to join one probe", n)
	}
	c.identity(context.Background(), []string{"/bin/x"}, "claude", "Claude Code", p, nil, false)
	if n := runs.Load(); n != 1 {
		t.Fatalf("ran %d times, want the cache", n)
	}
	now = now.Add(probeTTL + time.Second)
	c.identity(context.Background(), []string{"/bin/x"}, "claude", "Claude Code", p, nil, false)
	if n := runs.Load(); n != 2 {
		t.Fatalf("ran %d times, want a run after the TTL", n)
	}
	c.identity(context.Background(), []string{"/bin/x"}, "claude", "Claude Code", p, nil, true)
	if n := runs.Load(); n != 3 {
		t.Fatalf("ran %d times, want a forced run", n)
	}
}

// Two agents running one program with other variables (the e2e stubs that
// answer as Claude Code and as Codex) are two entries: a forced check of
// one never poisons the other.
func TestProbeCacheKeysByEnvironment(t *testing.T) {
	c := newProbeCache()
	c.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		return agents.ProbeResult{Ran: true, Identified: env["WHO"] == "claude", Output: env["WHO"]}
	}
	p := agents.Probe{Args: []string{"--version"}, Match: regexp.MustCompile(`x`), Verified: true}
	cmd := []string{"/bin/bash", "stub.sh"}
	a := c.identity(context.Background(), cmd, "claude", "Claude Code", p, map[string]string{"WHO": "claude"}, false)
	b := c.identity(context.Background(), cmd, "claude", "Claude Code", p, map[string]string{"WHO": "codex"}, true)
	again := c.identity(context.Background(), cmd, "claude", "Claude Code", p, map[string]string{"WHO": "claude"}, false)
	if !a.Identified || b.Identified || !again.Identified {
		t.Fatalf("a=%+v b=%+v again=%+v", a, b, again)
	}
	if _, ok := c.cached(cmd, "claude", "Claude Code", p, map[string]string{"WHO": "codex"}); !ok {
		t.Fatal("the forced check is kept under its own key")
	}
}

// A run that failed on its own side (the exit watch failed under way: the
// result agents' ProbeProcess gives, TestProbeAWatchThatFailsUnderWayIsNoAnswer)
// is not kept: nothing is cached, and the next ask probes again.
func TestProbeCacheKeepsNoFailedWatch(t *testing.T) {
	c := newProbeCache()
	c.wait = time.Hour
	var runs atomic.Int32
	c.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		if runs.Add(1) == 1 {
			return agents.ProbeResult{Error: "the exit of x could not be watched: waitid: function not implemented", Retry: true}
		}
		return agents.ProbeResult{Ran: true, Identified: true, Version: "1.0.0"}
	}
	p := agents.Probe{Args: []string{"--version"}, Match: regexp.MustCompile(`x`), Verified: true}
	cmd := []string{"/bin/x"}
	if id := c.identity(context.Background(), cmd, "claude", "Claude Code", p, nil, false); id.Ran || id.Error == "" {
		t.Fatalf("a failed watch answered: %+v", id)
	}
	if id, ok := c.cached(cmd, "claude", "Claude Code", p, nil); ok {
		t.Fatalf("a failed watch was kept: %+v", id)
	}
	if id := c.identity(context.Background(), cmd, "claude", "Claude Code", p, nil, false); !id.Identified || runs.Load() != 2 {
		t.Fatalf("the next ask did not probe again: %+v after %d runs", id, runs.Load())
	}
}

func TestProbeCacheWaitBound(t *testing.T) {
	c := newProbeCache()
	c.wait = 20 * time.Millisecond
	c.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		time.Sleep(200 * time.Millisecond)
		return agents.ProbeResult{Ran: true, Identified: true}
	}
	p := agents.Probe{Args: []string{"--version"}, Match: regexp.MustCompile(`x`)}
	start := time.Now()
	id := c.identity(context.Background(), []string{"/bin/x"}, "claude", "Claude Code", p, nil, false)
	if !id.Pending || id.Ran || time.Since(start) > 150*time.Millisecond {
		t.Fatalf("%+v after %s", id, time.Since(start))
	}
	time.Sleep(250 * time.Millisecond)
	if id := c.identity(context.Background(), []string{"/bin/x"}, "claude", "Claude Code", p, nil, false); !id.Identified {
		t.Fatalf("the answer landed and is kept: %+v", id)
	}
}

func TestProbeNeverRunsForMissingOrRelativePrograms(t *testing.T) {
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "missing", Name: "Missing", Command: []string{filepath.Join(t.TempDir(), "nope")}, Adapter: "claude"},
		{ID: "rel", Name: "Relative", Command: []string{"./agent.sh"}, Adapter: "claude"},
	})
	var runs atomic.Int32
	e.srv.probes.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		runs.Add(1)
		return agents.ProbeResult{}
	}
	_, out := e.do("GET", "/api/catalog", adminToken, nil)
	for _, a := range out["agents"].([]any) {
		m := a.(map[string]any)
		if m["id"] == "missing" || m["id"] == "rel" {
			if _, has := m["identity"]; has {
				t.Fatalf("%s has an identity: %v", m["id"], m["identity"])
			}
		}
	}
	if runs.Load() != 0 {
		t.Fatalf("the probe ran %d times", runs.Load())
	}
}

// An agent's startup questions (round 13, G2c) reach the session it
// launches: the question on its screen holds it, with the catalog's answers
// as the choices, as the trust question does.
func TestLaunchCarriesTheAgentsStartupQuestions(t *testing.T) {
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{{
		ID: "asks", Name: "Asks", Command: []string{"/bin/sh", "-c", "printf 'Hooks need review\\n> 1. Review hooks\\n'; exec cat"},
		Questions: []catalog.Question{{Prompt: `(?i)hooks\s*n?eed\s*review`, Answers: []catalog.Answer{{Label: "Trust all and continue", Input: "2\r"}, {Label: "Continue without trusting", Input: "3\r"}}}},
	}})
	id := e.createSession("asks")
	d, _ := e.srv.Registry().Get(id)
	deadline := time.Now().Add(5 * time.Second)
	for d.Info().Attention.Source != session.SourceTrust && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	att := d.Info().Attention
	if att.Source != session.SourceTrust || att.Message != "Hooks need review" || len(att.Options) != 2 || att.Options[0].Input != "2\r" {
		t.Fatalf("attention %+v", att)
	}
}

// The catalog asks for its agents' probes together and waits for them
// together, probeWait in all: a cold catalog of six slow probes answers in
// about one wait, the late ones pending, not in one wait per agent (seen on
// the Windows app as the Launch dialog's long empty list on its first
// open). WarmCatalog, run as the server starts, leaves them known for the
// first request.
func TestCatalogWaitsForItsProbesTogether(t *testing.T) {
	var list []catalog.Agent
	for i := range 6 {
		p := versionScript(t, "claude", "2.1.287 (Claude Code)")
		list = append(list, catalog.Agent{ID: "claude" + string(rune('a'+i)), Name: "Claude Code", Command: []string{p}, Adapter: "claude"})
	}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), list)
	var ran atomic.Int32
	e.srv.probes.wait = time.Second
	e.srv.probes.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		time.Sleep(700 * time.Millisecond)
		ran.Add(1)
		return r.Run(ctx, command, p, env)
	}
	pending := func(out map[string]any) int {
		n := 0
		for _, a := range out["agents"].([]any) {
			if id, _ := a.(map[string]any)["identity"].(map[string]any); id != nil && id["pending"] == true {
				n++
			}
		}
		return n
	}
	start := time.Now()
	_, out := e.do("GET", "/api/catalog", adminToken, nil)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a cold catalog of six slow probes took %v (one wait per agent?)", took)
	}
	if n := pending(out); n == 0 || n == 6 {
		t.Fatalf("pending after one wait: %d of 6", n)
	}
	// Warmed as the server starts: the probes finish meanwhile, and the next
	// request finds every one known at once.
	e.srv.probes.entries = map[string]probeEntry{}
	ran.Store(0)
	e.srv.WarmCatalog(t.Context())
	deadline := time.Now().Add(10 * time.Second)
	for ran.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	start = time.Now()
	_, out = e.do("GET", "/api/catalog", adminToken, nil)
	if took := time.Since(start); took > 300*time.Millisecond || pending(out) != 0 {
		t.Fatalf("after the warm-up: %v, %d pending", took, pending(out))
	}
}
