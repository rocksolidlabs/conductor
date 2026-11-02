package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// Brakeman's desk and its turn (docs/plans/brakeman.md, PR-3c). A turn's
// agent here is the API tests' scripted agent: a shell under Claude
// Code's adapter (so the configuration takes it: an MCP registration and
// a read-only mode that holds) that prints what it was given and then
// waits, echoing what is typed; the test plays its part over HTTP with
// the session's own token, as its MCP tools would (PR-5).

// bkScript writes the turn's names to env-<request>.txt in its folder,
// says it is ready and waits on its input, echoing what is typed.
const bkScript = `printf 'B=%s A=%s Y=%s N=%s\n' "$CONDUCTOR_BRAKEMAN" "$CLAUDE_CONFIG_DIR" "$CONDUCTOR_RUN" "$CONDUCTOR_NOTIFY_URL" > "env-$CONDUCTOR_BRAKEMAN.txt"; echo ready; exec cat`

// bkEnv is a test server with Brakeman's scripted agents.
type bkEnv struct {
	*testEnv
	desk *brakemanDesk
}

// brakemanEnv is a server whose catalog holds the scripted agents: bk
// (bkScript), bkexit (an agent that prints why and exits at once, as a
// logged-out CLI does), bkcodex (Codex's adapter: its read-only mode
// does not hold) and bkfixed (bkScript, an entry that takes no extra
// arguments).
func brakemanEnv(t *testing.T, mutate func(*config.Config)) *bkEnv {
	t.Helper()
	e := newTestEnvAgents(t, mutate, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "bk", Name: "bk", Command: []string{"/bin/sh", "-c", bkScript}, Adapter: "claude", AllowArgs: true},
		{ID: "bkexit", Name: "bkexit", Command: []string{"/bin/sh", "-c", "echo 'Please run /login'; exit 3"}, Adapter: "claude", AllowArgs: true},
		{ID: "bkcodex", Name: "bkcodex", Command: []string{"/bin/sh", "-c", "exec cat"}, Adapter: "codex", AllowArgs: true},
		{ID: "bkfixed", Name: "bkfixed", Command: []string{"/bin/sh", "-c", bkScript}, Adapter: "claude"},
	})
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	e.stopEverything(t)
	if e.srv.brakeman == nil {
		t.Fatal("no Brakeman desk")
	}
	return &bkEnv{testEnv: e, desk: e.srv.brakeman}
}

// config reads the configuration as the admin's route answers it.
func (b *bkEnv) config(t *testing.T) map[string]any {
	t.Helper()
	return entityOf(t, b.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, ""), "config")
}

// configure saves the configuration with mod applied, at the rev read.
func (b *bkEnv) configure(t *testing.T, mod func(c map[string]any)) map[string]any {
	t.Helper()
	c := b.config(t)
	rev := c["rev"]
	mod(c)
	return entityOf(t, b.expect(t, "PUT", "/api/brakeman/config", map[string]any{"rev": rev, "config": c}, http.StatusOK, ""), "config")
}

// enable turns Brakeman on with the agent given, in the server's root.
func (b *bkEnv) enable(t *testing.T, agent string, mod func(c map[string]any)) {
	t.Helper()
	b.configure(t, func(c map[string]any) {
		c["enabled"], c["agentId"], c["cwd"] = true, agent, b.root
		if mod != nil {
			mod(c)
		}
	})
}

var bkOps atomic.Int64

// ask asks Brakeman for a request of kind, answering its status and the
// request.
func (b *bkEnv) ask(t *testing.T, kind string, extra map[string]any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"opId": fmt.Sprintf("ask-%d", bkOps.Add(1)), "kind": kind}
	for k, v := range extra {
		body[k] = v
	}
	status, _, out := b.call(t, "POST", "/api/brakeman/requests", body)
	req, _ := out["request"].(map[string]any)
	return status, req
}

// briefing asks for a briefing and answers its id.
func (b *bkEnv) briefing(t *testing.T) string {
	t.Helper()
	status, req := b.ask(t, "briefing", nil)
	if status != http.StatusCreated {
		t.Fatalf("ask: %d %v", status, req)
	}
	return req["id"].(string)
}

// request reads a request as the admin's route answers it.
func (b *bkEnv) request(t *testing.T, id string) map[string]any {
	t.Helper()
	return entityOf(t, b.expect(t, "GET", "/api/brakeman/requests/"+id, nil, http.StatusOK, ""), "request")
}

// waitState waits for the request to be in one of states, which holds
// once reached (a closed state, or one the test holds), and answers it.
func (b *bkEnv) waitState(t *testing.T, id string, states ...string) brakeman.Request {
	t.Helper()
	var r brakeman.Request
	if !waitFor(30*time.Second, func() bool {
		var err error
		r, err = b.desk.st.Get(id)
		return err == nil && slices.Contains(states, r.State)
	}) {
		t.Fatalf("%s is %s (%s), not %v", id, r.State, r.Why, states)
	}
	return r
}

// turnSession waits for the request's member session and gives it a known
// token: the session, its token and its *Local.
func (b *bkEnv) turnSession(t *testing.T, id string) (sid, tok string, l *session.Local) {
	t.Helper()
	if !waitFor(30*time.Second, func() bool {
		b.srv.registry.Each(func(d session.Driver) {
			if ref := d.Info().Crew; ref != nil && ref.Brakeman != nil && ref.Brakeman.Request == id {
				sid = d.Info().ID
				l, _ = d.(*session.Local)
			}
		})
		return sid != ""
	}) {
		t.Fatalf("%s's session never started", id)
	}
	return sid, b.agentToken(sid), l
}

// readRequest reads the turn's request with tok.
func (b *bkEnv) readRequest(t *testing.T, sid, tok string) (int, map[string]any) {
	t.Helper()
	status, _, out := b.callAs(t, tok, "GET", "/api/sessions/"+sid+"/brakeman/request", nil)
	return status, out
}

// submit submits a brief with tok.
func (b *bkEnv) submit(t *testing.T, sid, tok, opID string, brief any) (int, map[string]any) {
	t.Helper()
	status, _, out := b.callAs(t, tok, "POST", "/api/sessions/"+sid+"/brakeman/brief", map[string]any{"opId": opID, "brief": brief})
	return status, out
}

// factBrief is a brief of one fact citing refs.
func factBrief(refs ...string) map[string]any {
	return map[string]any{"schema": brakeman.BriefSchema, "title": "What ran", "summary": "The turn ran.",
		"items": []any{map[string]any{"kind": brakeman.ItemFact, "text": "the turn's session ran", "evidence": refs}}}
}

// runOf waits for the run the request's turn launched.
func (b *bkEnv) runOf(t *testing.T, sid string) string {
	t.Helper()
	var run string
	waitFor(30*time.Second, func() bool {
		if run, _, _ = b.srv.runs.MemberOf(sid); run == "" {
			if d, ok := b.srv.registry.Get(sid); ok && d.Info().Crew != nil {
				run = d.Info().Crew.RunID
			}
		}
		return run != ""
	})
	return run
}

// clock holds the desk's clock at a time the test moves.
type clock struct{ at atomic.Pointer[time.Time] }

func (c *clock) now() time.Time { return *c.at.Load() }

func (c *clock) add(d time.Duration) {
	t := c.now().Add(d)
	c.at.Store(&t)
}

// fakeClock gives the desk (and its store) a clock of the test's.
func (b *bkEnv) fakeClock() *clock {
	c := &clock{}
	now := time.Now()
	c.at.Store(&now)
	f := c.now
	b.desk.clock.Store(&f)
	return c
}

// settle waits until the loop has made a full pass after a poke: what is
// due at the desk's clock now has been done.
func (b *bkEnv) settle(t *testing.T) {
	t.Helper()
	n := b.desk.steps.Load()
	b.desk.poke()
	if !waitFor(10*time.Second, func() bool {
		b.desk.poke()
		return b.desk.steps.Load() >= n+2
	}) {
		t.Fatal("the desk's loop made no pass")
	}
}

// turnHeld reports whether a turn holds the slot.
func (b *bkEnv) turnHeld() bool {
	b.desk.mu.Lock()
	defer b.desk.mu.Unlock()
	return b.desk.turn != nil
}

// slotFree waits for the slot to be given up.
func (b *bkEnv) slotFree(t *testing.T) {
	t.Helper()
	if !waitFor(30*time.Second, func() bool {
		b.desk.mu.Lock()
		defer b.desk.mu.Unlock()
		return b.desk.turn == nil
	}) {
		t.Fatal("the slot was never given up")
	}
}

// runStopped waits for the run to read stopped (or ended).
func (b *bkEnv) runStopped(t *testing.T, run string) {
	t.Helper()
	if !waitFor(30*time.Second, func() bool {
		r, ok := b.srv.runs.Get(run)
		return ok && (r.StoppedAt != nil || r.State == crew.RunFinished)
	}) {
		t.Fatalf("run %s never stopped", run)
	}
}

// gateLaunch holds every turn's launch once it has returned, before its
// return is written, until the returned function is called.
func (b *bkEnv) gateLaunch(t *testing.T) (held <-chan string, release func()) {
	t.Helper()
	ch, gate := make(chan string, 4), make(chan struct{})
	f := func(id string) {
		ch <- id
		<-gate
	}
	b.desk.afterLaunch.Store(&f)
	once := func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
	t.Cleanup(func() {
		once()
		b.desk.afterLaunch.Store(nil)
	})
	return ch, once
}

// A request is refused while Brakeman is off, and nothing is kept.
func TestARequestIsRefusedWhileBrakemanIsOff(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	if c := b.config(t); c["enabled"] != false || c["visibility"].(map[string]any)["terminals"] != false {
		t.Fatalf("the defaults %v", c)
	}
	b.expect(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "a1", "kind": "briefing"}, http.StatusConflict, "brakeman_off")
	if page, _, _ := b.desk.st.List("", 10); len(page) != 0 {
		t.Fatalf("a request was kept: %v", page)
	}
}

// A turn is a one-member run Yardmaster launches, labelled "Brakeman ·
// <kind> br-n": its session is the request's, named "Brakeman · br-n"
// for Needs you, and is typed its line once ready. The session reads its
// request and submits its brief over HTTP with its own token; the request
// is briefed, its brief and resolved refs kept, and the run stopped after
// the grace.
func TestATurnIsAOneMemberRunThatTypesItsLine(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, l := b.turnSession(t, id)
	run := b.runOf(t, sid)
	r, ok := b.srv.runs.Get(run)
	if !ok || len(r.Members) != 1 || r.Class != crew.ClassBrakeman || r.Request != id || r.Label != "Brakeman · briefing "+id || r.CrewID != "brakeman" || r.Yolo || r.Managed != nil {
		t.Fatalf("the run %+v", r)
	}
	if info := l.Info(); info.Name != "Brakeman · "+id || info.Crew.Brakeman == nil || info.Crew.Brakeman.Request != id || info.Crew.Brakeman.Quiet {
		t.Fatalf("the session %+v %+v", info, info.Crew)
	}
	if !waitFor(30*time.Second, func() bool {
		return strings.Contains(l.LastLine(), "Brakeman request "+id+" (briefing): read it with the brakeman_request tool")
	}) {
		t.Fatalf("the line was never typed: %q", l.LastLine())
	}
	status, out := b.readRequest(t, sid, tok)
	if status != http.StatusOK || entityOf(t, out, "request")["id"] != id || entityOf(t, out, "request")["kind"] != "briefing" {
		t.Fatalf("the request: %d %v", status, out)
	}
	status, out = b.submit(t, sid, tok, "brief-1", factBrief("session:"+sid, "run:"+run))
	if status != http.StatusCreated || entityOf(t, out, "request")["state"] != brakeman.StateBriefed {
		t.Fatalf("the brief: %d %v", status, out)
	}
	got := b.expect(t, "GET", "/api/brakeman/requests/"+id, nil, http.StatusOK, "")
	if entityOf(t, got, "brief")["title"] != "What ran" || entityOf(t, got, "request")["runId"] != run || entityOf(t, got, "request")["sessionId"] != sid {
		t.Fatalf("the request whole %v", got)
	}
	refs := b.expect(t, "GET", "/api/brakeman/requests/"+id+"/refs", nil, http.StatusOK, "")["refs"].([]any)
	if len(refs) != 2 || refs[0].(map[string]any)["ref"] != "session:"+sid {
		t.Fatalf("refs %v", refs)
	}
	c.add(brakemanGrace + time.Second)
	b.settle(t)
	b.runStopped(t, run)
	b.slotFree(t)
}

// The turn's session carries its request and its account: CONDUCTOR_BRAKEMAN
// and the adapter's ConfigDirEnv (CLAUDE_CONFIG_DIR), with the crew's own.
func TestTheTurnCarriesItsRequestAndAccount(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	account := realRoot(t, t.TempDir())
	b.enable(t, "bk", func(c map[string]any) { c["account"] = account })
	id := b.briefing(t)
	b.turnSession(t, id)
	want := "B=" + id + " A=" + account + " Y=brakeman-"
	var got []byte
	if !waitFor(30*time.Second, func() bool {
		got, _ = os.ReadFile(filepath.Join(b.root, "env-"+id+".txt"))
		return strings.HasSuffix(string(got), "\n")
	}) || !strings.HasPrefix(string(got), want) {
		t.Fatalf("the session was given %q, want %q", got, want)
	}
	if r := b.request(t, id); r["agent"].(map[string]any)["account"] != true || r["agent"].(map[string]any)["agentId"] != "bk" {
		t.Fatalf("the stamp %v", r["agent"])
	}
}

// A turn launches in the adapter's read-only mode: Claude Code's tool
// rules and --strict-mcp-config in its argv, then its model, nothing a
// person typed; never with yolo, the server's yolo on or not.
func TestATurnLaunchesReadOnly(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, func(c *config.Config) { c.Yolo = true })
	b.enable(t, "bk", func(c map[string]any) { c["model"] = "sonnet" })
	id := b.briefing(t)
	_, _, l := b.turnSession(t, id)
	argv := l.Info().Command
	for _, want := range []string{"--strict-mcp-config", "--model=sonnet"} {
		if !slices.Contains(argv, want) {
			t.Fatalf("argv %q lacks %s", argv, want)
		}
	}
	if !slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "--disallowedTools=") && strings.Contains(a, "Bash") }) {
		t.Fatalf("argv %q lacks the write tools' rule", argv)
	}
	if l.Info().Yolo || slices.Contains(argv, "--dangerously-skip-permissions") {
		t.Fatalf("a turn took yolo: %q", argv)
	}
}

// Yolo is never applied to a turn: launchTurn refuses a crew whose yolo is
// not false, or whose member lacks its adapter's read-only arguments.
func TestBrakemanNeverTakesYolo(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, func(c *config.Config) { c.Yolo = true })
	yes, no := true, false
	member := crew.Member{Name: "brakeman", AgentID: "bk", Start: crew.Start{When: crew.StartImmediately}}
	opts := crew.LaunchOpts{Class: crew.ClassBrakeman, Request: "br-9"}
	for name, c := range map[string]crew.Crew{
		"yolo on":           {ID: "brakeman", Name: "Brakeman", Cwd: b.root, Where: crew.WhereServer, Isolation: crew.IsolationNone, Goal: "g", Yolo: &yes, Members: []crew.Member{member}},
		"yolo unset":        {ID: "brakeman", Name: "Brakeman", Cwd: b.root, Where: crew.WhereServer, Isolation: crew.IsolationNone, Goal: "g", Members: []crew.Member{member}},
		"no read-only args": {ID: "brakeman", Name: "Brakeman", Cwd: b.root, Where: crew.WhereServer, Isolation: crew.IsolationNone, Goal: "g", Yolo: &no, Members: []crew.Member{member}},
	} {
		if run, err := b.srv.launchTurn(t.Context(), c, "", opts); err == nil {
			t.Fatalf("%s: launched %v", name, run.ID)
		}
	}
	if n := len(b.srv.runs.List()); n != 0 {
		t.Fatalf("%d runs launched", n)
	}
	// And a turn the desk launches with the server's yolo on runs without.
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	_, _, l := b.turnSession(t, id)
	if l.Info().Yolo || slices.Contains(l.Info().Command, "--dangerously-skip-permissions") || l.Launched().Yolo {
		t.Fatalf("the turn took yolo: %+v", l.Info())
	}
}

// A request repeats by its opId: the same content answers it (200), other
// content 409 op_conflict, whatever stands since (Brakeman off).
func TestARequestRepeatsByItsOpID(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	body := map[string]any{"opId": "same", "kind": "locate", "note": "where is the retry policy?"}
	first := b.expect(t, "POST", "/api/brakeman/requests", body, http.StatusCreated, "")
	again := b.expect(t, "POST", "/api/brakeman/requests", body, http.StatusOK, "")
	if entityOf(t, first, "request")["id"] != entityOf(t, again, "request")["id"] {
		t.Fatalf("two requests: %v %v", first, again)
	}
	b.expect(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "same", "kind": "locate", "note": "another"}, http.StatusConflict, "op_conflict")
	b.configure(t, func(c map[string]any) { c["enabled"] = false })
	b.expect(t, "POST", "/api/brakeman/requests", body, http.StatusOK, "")
	b.expect(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "bad id!", "kind": "briefing"}, http.StatusConflict, "brakeman_off")
	b.enable(t, "bk", nil)
	b.expect(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "k1", "kind": "nope"}, http.StatusBadRequest, brakeman.CodeInvalidRequest)
	b.expect(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "k2", "kind": "explain"}, http.StatusBadRequest, brakeman.CodeInvalidRequest)
}

// One turn at a time, 8 waiting at most: a ninth is refused queue_full,
// and the queue is taken in number order as each slot is given up.
func TestOneTurnAtATimeAndABoundedQueue(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	first := b.briefing(t)
	b.waitState(t, first, brakeman.StateStarting, brakeman.StateRunning)
	var queued []string
	for range brakemanQueue {
		queued = append(queued, b.briefing(t))
	}
	if status, req := b.ask(t, "briefing", nil); status != http.StatusTooManyRequests {
		t.Fatalf("a ninth waiting: %d %v", status, req)
	}
	b.settle(t)
	for _, id := range queued {
		if r, _ := b.desk.st.Get(id); r.State != brakeman.StateQueued {
			t.Fatalf("%s is %s while %s holds the slot", id, r.State, first)
		}
	}
	state := b.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")
	if entityOf(t, state, "current")["id"] != first || len(state["queue"].([]any)) != brakemanQueue {
		t.Fatalf("the desk %v", state)
	}
	// The first cancelled, the slot is given up once its process is
	// reaped, and the oldest queued takes it.
	b.expect(t, "POST", "/api/brakeman/requests/"+first+"/cancel", map[string]any{"opId": "c1"}, http.StatusOK, "")
	b.waitState(t, queued[0], brakeman.StateStarting, brakeman.StateRunning)
	for _, id := range queued[1:] {
		if r, _ := b.desk.st.Get(id); r.State != brakeman.StateQueued {
			t.Fatalf("%s is %s", id, r.State)
		}
	}
}

// The deadline ends the turn: at TurnMinutes after starting (the desk's
// clock), the request is timed_out, then its run is stopped.
func TestTheDeadlineEndsTheTurn(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bk", func(c map[string]any) { c["budget"].(map[string]any)["turnMinutes"] = 1 })
	id := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	b.waitState(t, id, brakeman.StateRunning)
	c.add(59 * time.Second)
	b.settle(t)
	if r, _ := b.desk.st.Get(id); r.State != brakeman.StateRunning {
		t.Fatalf("before the deadline: %s", r.State)
	}
	c.add(2 * time.Second)
	b.settle(t)
	r := b.waitState(t, id, brakeman.StateTimedOut)
	if !strings.Contains(r.Why, "1 minutes passed with no brief") || r.RunID != run {
		t.Fatalf("timed out %+v", r)
	}
	b.runStopped(t, run)
	b.slotFree(t)
}

// The deadline covers starting: a launch that has not returned at the
// deadline closes the request timed_out, and the run it returns after is
// the request's stray run, stopped at once.
func TestTheDeadlineCoversStarting(t *testing.T) {
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bk", func(c map[string]any) { c["budget"].(map[string]any)["turnMinutes"] = 1 })
	held, release := b.gateLaunch(t)
	id := b.briefing(t)
	<-held
	c.add(2 * time.Minute)
	b.settle(t)
	if r := b.waitState(t, id, brakeman.StateTimedOut); r.StrayRun != "" {
		t.Fatalf("a stray run before the launch returned: %+v", r)
	}
	release()
	var r brakeman.Request
	waitFor(30*time.Second, func() bool { r, _ = b.desk.st.Get(id); return r.StrayRun != "" })
	if r.State != brakeman.StateTimedOut || r.StrayRun == "" || r.RunID != r.StrayRun {
		t.Fatalf("after the launch returned %+v", r)
	}
	b.runStopped(t, r.StrayRun)
	b.slotFree(t)
}

// A cancel during the launch closes the request cancelled first; the run
// the launch returns after is recorded as its stray run and stopped.
func TestCancelDuringLaunchStopsTheRunItReturns(t *testing.T) {
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	held, release := b.gateLaunch(t)
	id := b.briefing(t)
	<-held
	out := b.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": "c1"}, http.StatusOK, "")
	if entityOf(t, out, "request")["state"] != brakeman.StateCancelled {
		t.Fatalf("cancel %v", out)
	}
	release()
	var r brakeman.Request
	waitFor(30*time.Second, func() bool { r, _ = b.desk.st.Get(id); return r.StrayRun != "" })
	if r.State != brakeman.StateCancelled || r.StrayRun == "" {
		t.Fatalf("%+v", r)
	}
	b.runStopped(t, r.StrayRun)
	b.slotFree(t)
	// A closed request's cancel answers it as it stands.
	again := b.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": "c2"}, http.StatusOK, "")
	if entityOf(t, again, "request")["strayRun"] != r.StrayRun {
		t.Fatalf("again %v", again)
	}
}

// A cancel of a running turn writes cancelled, then stops its run.
func TestCancelStopsTheTurn(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	b.waitState(t, id, brakeman.StateRunning)
	b.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": "c1"}, http.StatusOK, "")
	if r := b.waitState(t, id, brakeman.StateCancelled); r.StrayRun != "" || r.RunID != run {
		t.Fatalf("%+v", r)
	}
	b.runStopped(t, run)
	b.slotFree(t)
}

// Turning Brakeman off cancels the turn in progress and every queued
// request, and stops the turn's run.
func TestTurningBrakemanOffStopsTheTurn(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	queued := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	b.configure(t, func(c map[string]any) { c["enabled"] = false })
	for _, x := range []string{id, queued} {
		if r := b.waitState(t, x, brakeman.StateCancelled); r.Why != "Brakeman was turned off" {
			t.Fatalf("%s: %+v", x, r)
		}
	}
	b.runStopped(t, run)
	b.slotFree(t)
}

// A session that ends after it read its request, with no brief, ends the
// request; the slot is given up once its process is reaped.
func TestASessionThatEndsWithNoBriefEndsTheRequest(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, l := b.turnSession(t, id)
	b.waitState(t, id, brakeman.StateRunning)
	if status, out := b.readRequest(t, sid, tok); status != http.StatusOK {
		t.Fatalf("read %d %v", status, out)
	}
	if err := l.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r := b.waitState(t, id, brakeman.StateEnded); r.Why != "the session ended with no brief" || r.SessionID != sid {
		t.Fatalf("%+v", r)
	}
	b.slotFree(t)
}

// An agent that exits before it read its request fails the request with
// its exit and last line, from starting (the launch not yet returned) and
// from running.
func TestAnAgentThatExitsBeforeReadingFails(t *testing.T) {
	t.Run("starting", func(t *testing.T) {
		b := brakemanEnv(t, nil)
		b.enable(t, "bkexit", nil)
		held, release := b.gateLaunch(t)
		id := b.briefing(t)
		<-held
		r := b.waitState(t, id, brakeman.StateFailed)
		if !strings.Contains(r.Why, "exited before it read its request: is it logged in?") || !strings.Contains(r.Why, "exit 3") || !strings.Contains(r.Why, "Please run /login") {
			t.Fatalf("%+v", r)
		}
		release()
		b.slotFree(t)
		if r, _ := b.desk.st.Get(id); r.State != brakeman.StateFailed || r.StrayRun != "" || r.RunID == "" {
			t.Fatalf("after the launch returned %+v", r)
		}
	})
	t.Run("running", func(t *testing.T) {
		b := brakemanEnv(t, nil)
		b.enable(t, "bk", nil)
		id := b.briefing(t)
		_, _, l := b.turnSession(t, id)
		b.waitState(t, id, brakeman.StateRunning)
		if err := l.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if r := b.waitState(t, id, brakeman.StateFailed); !strings.Contains(r.Why, "exited before it read its request") {
			t.Fatalf("%+v", r)
		}
		b.slotFree(t)
	})
}

// A session that ends during the launch ends the request then; the
// launch's return writes no state over it.
func TestASessionThatEndsDuringLaunchEnds(t *testing.T) {
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	held, release := b.gateLaunch(t)
	id := b.briefing(t)
	<-held
	sid, tok, l := b.turnSession(t, id)
	if status, _ := b.readRequest(t, sid, tok); status != http.StatusOK {
		t.Fatalf("read while starting: %d", status)
	}
	_ = l.Stop(t.Context())
	b.waitState(t, id, brakeman.StateEnded)
	release()
	b.slotFree(t)
	if r, _ := b.desk.st.Get(id); r.State != brakeman.StateEnded || r.StrayRun != "" || r.RunID == "" {
		t.Fatalf("%+v", r)
	}
}

// A late launch's return leaves a closed request closed: briefed, ended
// and cancelled while starting, the return writes no state, and only the
// cancelled one's run is a stray run.
func TestALateLaunchReturnLeavesAClosedRequest(t *testing.T) {
	for _, how := range []string{brakeman.StateBriefed, brakeman.StateEnded, brakeman.StateCancelled} {
		t.Run(how, func(t *testing.T) {
			b := brakemanEnv(t, nil)
			c := b.fakeClock()
			b.enable(t, "bk", nil)
			held, release := b.gateLaunch(t)
			id := b.briefing(t)
			<-held
			sid, tok, l := b.turnSession(t, id)
			switch how {
			case brakeman.StateBriefed:
				if status, out := b.submit(t, sid, tok, "b1", factBrief("session:"+sid)); status != http.StatusCreated {
					t.Fatalf("brief %d %v", status, out)
				}
			case brakeman.StateEnded:
				b.readRequest(t, sid, tok)
				_ = l.Stop(t.Context())
			case brakeman.StateCancelled:
				b.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": "c1"}, http.StatusOK, "")
			}
			b.waitState(t, id, how)
			release()
			c.add(brakemanGrace + time.Second) // a brief's run stops after the grace
			b.settle(t)
			b.slotFree(t)
			r, _ := b.desk.st.Get(id)
			if r.State != how || r.RunID == "" || r.SessionID != sid || (r.StrayRun != "") != (how == brakeman.StateCancelled) {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// A brief submitted while the request is still starting is taken.
func TestABriefWhileStartingIsTaken(t *testing.T) {
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	held, release := b.gateLaunch(t)
	id := b.briefing(t)
	<-held
	sid, tok, _ := b.turnSession(t, id)
	if r, _ := b.desk.st.Get(id); r.State != brakeman.StateStarting {
		t.Fatalf("not starting: %s", r.State)
	}
	status, out := b.submit(t, sid, tok, "b1", factBrief("session:"+sid))
	if status != http.StatusCreated || entityOf(t, out, "request")["runId"] == "" {
		t.Fatalf("%d %v", status, out)
	}
	release()
}

// A brief is kept before its run stops: the request is briefed with its
// brief when the route answers, and the run is stopped only after the
// grace, at the desk's clock.
func TestABriefIsKeptBeforeTheRunStops(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, l := b.turnSession(t, id)
	run := b.runOf(t, sid)
	if status, out := b.submit(t, sid, tok, "b1", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatalf("%d %v", status, out)
	}
	if _, _, err := b.desk.st.Brief(id); err != nil {
		t.Fatalf("the brief is not kept as the route answers: %v", err)
	}
	c.add(brakemanGrace - time.Second)
	b.settle(t)
	select {
	case <-l.ProcessDone():
		t.Fatal("the run stopped inside the grace")
	default:
	}
	c.add(2 * time.Second)
	b.settle(t)
	b.runStopped(t, run)
	b.slotFree(t)
}

// A brief whose refs name nothing is refused 422 bad_refs naming each ref,
// with nothing written; so is one whose ref lies outside the scope.
func TestABriefNamesOnlyRefsThatResolve(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	status, out := b.submit(t, sid, tok, "b1", factBrief("session:"+sid, "run:nosuch-00000001", "ticket:ZZ-404"))
	if status != http.StatusUnprocessableEntity || errorCode(out) != "bad_refs" {
		t.Fatalf("%d %v", status, out)
	}
	refs := entityOf(t, out, "error")["refs"].([]any)
	if len(refs) != 2 || refs[0].(map[string]any)["ref"] != "run:nosuch-00000001" || refs[1].(map[string]any)["ref"] != "ticket:ZZ-404" {
		t.Fatalf("refs %v", refs)
	}
	if r, _ := b.desk.st.Get(id); r.HasBrief || r.Open() == false {
		t.Fatalf("something was kept: %+v", r)
	}
}

// A brief citing what the scope leaves out is refused 422 bad_refs.
func TestABriefOutsideTheScopeIsRefused(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	other := b.createSession("cat")
	b.enable(t, "bk", func(c map[string]any) { c["scope"] = map[string]any{"crews": []any{"brakeman"}} })
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	status, out := b.submit(t, sid, tok, "b1", factBrief("session:"+other))
	if status != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(out), "outside") {
		t.Fatalf("%d %v", status, out)
	}
	if status, out := b.submit(t, sid, tok, "b2", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatalf("the turn's own session, in scope: %d %v", status, out)
	}
}

// The workbench token submits no brief (403 agent_only) and reads the
// request; a share link reads neither.
func TestTheWorkbenchTokenSubmitsNoBrief(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	if status, out := b.submit(t, sid, adminToken, "b1", factBrief("session:"+sid)); status != http.StatusForbidden || errorCode(out) != "agent_only" {
		t.Fatalf("%d %v", status, out)
	}
	if status, out := b.readRequest(t, sid, adminToken); status != http.StatusOK {
		t.Fatalf("the admin's read %d %v", status, out)
	}
	b.desk.mu.Lock()
	read := b.desk.read[id]
	b.desk.mu.Unlock()
	if read {
		t.Fatal("the admin's read counted as the agent's")
	}
	if status, _ := b.readRequest(t, sid, "nope"); status != http.StatusUnauthorized {
		t.Fatalf("a bad token reads: %d", status)
	}
}

// Another session's token, or another session, submits no brief to the
// request: a session that is not a turn's is not_brakeman.
func TestABriefFromAnotherSessionIsRefused(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	other := b.createSession("cat")
	otherTok := b.agentToken(other)
	if status, _ := b.submit(t, sid, otherTok, "b1", factBrief("session:"+sid)); status != http.StatusUnauthorized {
		t.Fatalf("another session's token on the turn's: %d", status)
	}
	if status, out := b.submit(t, other, otherTok, "b1", factBrief("session:"+sid)); status != http.StatusNotFound || errorCode(out) != "not_brakeman" {
		t.Fatalf("another session: %d %v", status, out)
	}
	if r, _ := b.desk.st.Get(id); r.HasBrief {
		t.Fatal("a brief was kept")
	}
}

// A brief is kept once: the same opId and brief answer it, any other 409
// brief_exists; a lost reply asked again after the request is briefed
// (its run stopped since) answers 200 with it.
func TestABriefIsKeptOnce(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	brief := factBrief("session:" + sid)
	if status, _ := b.submit(t, sid, tok, "b1", brief); status != http.StatusCreated {
		t.Fatal(status)
	}
	if status, _ := b.submit(t, sid, tok, "b1", brief); status != http.StatusOK {
		t.Fatalf("the same brief again: %d", status)
	}
	if status, out := b.submit(t, sid, tok, "b2", brief); status != http.StatusConflict || errorCode(out) != "brief_exists" {
		t.Fatalf("another opId: %d %v", status, out)
	}
	other := factBrief("session:" + sid)
	other["title"] = "Another"
	if status, out := b.submit(t, sid, tok, "b1", other); status != http.StatusConflict || errorCode(out) != "brief_exists" {
		t.Fatalf("another brief: %d %v", status, out)
	}
}

// A lost reply asked again once the request is briefed and its turn
// over answers the kept brief, never resolved again.
func TestALostBriefReplyIsAnsweredAfterBriefed(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	brief := factBrief("session:"+sid, "run:"+run)
	if status, _ := b.submit(t, sid, tok, "lost", brief); status != http.StatusCreated {
		t.Fatal(status)
	}
	c.add(brakemanGrace + time.Second)
	b.settle(t)
	b.runStopped(t, run)
	if status, out := b.submit(t, sid, tok, "lost", brief); status != http.StatusOK || entityOf(t, out, "request")["state"] != brakeman.StateBriefed {
		t.Fatalf("the repeat: %d %v", status, out)
	}
}

// A proposal for an operation whose route checks a revision needs the
// revision it was read at; one the guide does not list is refused.
func TestAProposalForARevisionCheckedOpNeedsItsRevision(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	proposal := func(op, rev string) map[string]any {
		br := factBrief("session:" + sid)
		p := map[string]any{"kind": brakeman.ItemProposal, "text": "do it", "op": op, "target": "run:" + run, "facts": []any{0}}
		if rev != "" {
			p["expectedRevision"] = rev
		}
		br["items"] = append(br["items"].([]any), p)
		return br
	}
	for _, c := range []struct{ op, rev, field string }{
		{"run_pause", "", "items[1].expectedRevision"},
		{"run_pause", "0", "items[1].expectedRevision"},
		{"launch_the_moon", "", "items[1].op"},
	} {
		status, out := b.submit(t, sid, tok, "p-"+c.op+c.rev, proposal(c.op, c.rev))
		if status != http.StatusBadRequest || errorCode(out) != brakeman.CodeInvalidBrief || entityOf(t, out, "error")["field"] != c.field {
			t.Fatalf("%s %q: %d %v", c.op, c.rev, status, out)
		}
	}
	// A stop checks no revision: its proposal needs none.
	if status, out := b.submit(t, sid, tok, "p-stop", proposal("run_stop", "")); status != http.StatusCreated {
		t.Fatalf("run_stop: %d %v", status, out)
	}
}

// A restart interrupts the turn: a server stopped while its turn ran, and
// another started on the same data, finds the request interrupted, its
// run never resumed.
func TestARestartInterruptsTheTurn(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	b.waitState(t, id, brakeman.StateRunning)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.srv.Shutdown(ctx)
	again := &bkEnv{testEnv: b.restart()}
	again.desk = again.srv.brakeman
	r := again.waitState(t, id, brakeman.StateInterrupted)
	if !strings.Contains(r.Why, "the server stopped while the turn was running") {
		t.Fatalf("%+v", r)
	}
	again.expect(t, "POST", "/api/runs/"+run+"/resume", nil, http.StatusConflict, "brakeman_run")
}

// A Brakeman run is never resumed and takes no member: its resume, its
// members' route, a member's resume and its session's resume answer 409
// brakeman_run.
func TestABrakemanRunIsNotResumed(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	b.expect(t, "POST", "/api/runs/"+run+"/members", map[string]any{"name": "more", "agentId": "cat", "start": map[string]any{"when": "immediately"}}, http.StatusConflict, "brakeman_run")
	b.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": "c"}, http.StatusOK, "")
	b.runStopped(t, run)
	b.expect(t, "POST", "/api/runs/"+run+"/resume", nil, http.StatusConflict, "brakeman_run")
	b.expect(t, "POST", "/api/runs/"+run+"/members/brakeman/resume", nil, http.StatusConflict, "brakeman_run")
	// Its member's session is not resumed through the session's own route
	// either, once it has ended.
	if !waitFor(30*time.Second, func() bool {
		d, ok := b.srv.registry.Get(sid)
		return ok && d.Info().Status.Ended()
	}) {
		t.Fatal("the turn's session never ended")
	}
	b.expect(t, "POST", "/api/sessions/"+sid+"/resume", nil, http.StatusConflict, "brakeman_run")
}

// A Brakeman session forms no crew and adds no member: 403
// brakeman_cannot, and the guide lists neither.
func TestABrakemanSessionFormsNoCrew(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	b.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", map[string]any{"name": "x", "goal": "g", "self": "a", "members": []any{map[string]any{"name": "a"}}}, http.StatusForbidden, "brakeman_cannot")
	b.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/run/members", map[string]any{"name": "b", "agentId": "cat", "start": map[string]any{"when": "immediately"}}, http.StatusForbidden, "brakeman_cannot")
	out := b.expectAs(t, tok, "GET", "/api/sessions/"+sid+"/guide?section=operations", nil, http.StatusOK, "")
	ops := fmt.Sprint(out)
	for _, name := range []string{"form_crew", "add_member", "ticket_read", "ticket_comment"} {
		if strings.Contains(ops, "name:"+name+" ") {
			t.Fatalf("the guide lists %s for Brakeman: %s", name, ops)
		}
	}
	for _, name := range []string{"brakeman_request", "brief_submit"} {
		if !strings.Contains(ops, "name:"+name+" ") {
			t.Fatalf("the guide does not list %s: %s", name, ops)
		}
	}
}

// Brakeman's session reaches no ticket through the session routes, its
// run linked to one (through the store: the link route refuses it) or
// not.
func TestBrakemanCannotReachATicket(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	b.newDispatchProject(t, "BK", nil)
	key := b.newDispatchTicket(t, "BK", "Watch it", nil)["key"].(string)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	if _, err := b.srv.dispatch.AddLink(t.Context(), dispatch.Op{Actor: dispatch.ActorAdmin, ID: "seam-link"}, key, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run}); err != nil {
		t.Fatal(err)
	}
	b.expectAs(t, tok, "GET", "/api/sessions/"+sid+"/ticket", nil, http.StatusForbidden, "brakeman_cannot")
	b.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/ticket/comments", map[string]any{"opId": "x1", "body": "hi"}, http.StatusForbidden, "brakeman_cannot")
}

// A Brakeman run is never linked to a ticket: Dispatch's run link answers
// 409 brakeman_run.
func TestABrakemanRunIsNotLinkedToATicket(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	b.newDispatchProject(t, "BK", nil)
	key := b.newDispatchTicket(t, "BK", "Watch it", nil)["key"].(string)
	id := b.briefing(t)
	sid, _, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	b.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": run}, http.StatusConflict, "brakeman_run")
}

// A person's crew named Brakeman is saved as brakeman-2, launches as a
// person's run (no class, no request, no mark on its session), and is
// never taken for a turn.
func TestAPersonsCrewNamedBrakemanIsNotATurn(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	run := b.launchCrew(t, "Brakeman", catMember("brakeman", "immediately"))
	t.Cleanup(func() { b.do("POST", "/api/runs/"+run+"/stop", adminToken, nil) })
	r, _ := b.srv.runs.Get(run)
	if r.CrewID != "brakeman-2" || r.Class != "" || r.Request != "" {
		t.Fatalf("the person's run %+v", r)
	}
	var sid string
	waitFor(30*time.Second, func() bool {
		r, _ = b.srv.runs.Get(run)
		if len(r.Members) == 1 {
			sid = r.Members[0].SessionID
		}
		return sid != ""
	})
	d, _ := b.srv.registry.Get(sid)
	if ref := d.Info().Crew; ref == nil || ref.Brakeman != nil {
		t.Fatalf("the person's session is marked %+v", ref)
	}
	if status, out := b.readRequest(t, sid, b.agentToken(sid)); status != http.StatusNotFound || errorCode(out) != "not_brakeman" {
		t.Fatalf("%d %v", status, out)
	}
	if b.turnHeld() {
		t.Fatal("the person's run took the slot")
	}
}

// Brakeman's token reaches no admin route: every route the server
// registers but a session's own is walked with it, those under
// /api/sessions/{id}/ that are the admin's or launch (resume, paste,
// links) among them.
func TestBrakemansTokenReachesNoAdminRoute(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	public := map[string]bool{"GET /api/health": true, "GET /api/ice": true, "GET /api/join/{token}": true}
	// A session's own routes, which its token is for (the ones that would
	// schedule or reach a ticket answer brakeman_cannot for it:
	// TestABrakemanSessionFormsNoCrew, TestBrakemanCannotReachATicket);
	// every other route under the session, the admin's and the launching
	// ones (resume, paste, links), is walked with the rest.
	own := map[string]bool{}
	for _, p := range []string{"GET brakeman/request", "POST brakeman/brief", "GET ticket", "POST ticket/comments", "GET guide", "GET task",
		"POST task/result", "POST messages", "GET inbox", "POST inbox/ack", "POST task/blocked", "POST task/checkpoint", "POST crew",
		"POST run/members", "GET run", "POST links/agent", "GET files", "POST attention", "POST events"} {
		method, path, _ := strings.Cut(p, " ")
		own[method+" /api/sessions/{id}/"+path] = true
	}
	wild := strings.NewReplacer("{id}", sid, "{run}", "x", "{name}", "x", "{task}", "x", "{key}", "BK-1", "{token}", "x", "{linkId}", "x", "{command}", "x", "{n}", "1", "{k}", "1", "{path...}", "x")
	walked := 0
	for _, pattern := range muxPatterns(t, "/api/") {
		method, path, _ := strings.Cut(pattern, " ")
		if own[pattern] || public[pattern] {
			continue
		}
		if strings.HasPrefix(path, "/api/sessions/{id}/") {
			walked++
		}
		var body any
		if method != http.MethodGet {
			body = []byte("{}")
		}
		real := wild.Replace(path)
		if strings.Contains(real, "{") {
			real = strings.NewReplacer("{", "", "}", "").Replace(real)
		}
		status, data, _ := b.callAs(t, tok, method, real, body)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: Brakeman's token answered %d %.200s", pattern, status, data)
		}
	}
	// resume, paste, the links' list, creation and revocation.
	if walked < 5 {
		t.Fatalf("%d of the session's admin routes walked", walked)
	}
}

// The day's budget refuses a request once today's turns and those queued
// reach TurnsPerDay.
func TestTheDaysBudgetRefuses(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", func(c map[string]any) { c["budget"].(map[string]any)["turnsPerDay"] = 2 })
	first := b.briefing(t)
	b.waitState(t, first, brakeman.StateStarting, brakeman.StateRunning)
	b.briefing(t)
	status, out := b.ask(t, "briefing", nil)
	if status != http.StatusTooManyRequests {
		t.Fatalf("a third today: %d %v", status, out)
	}
	_, _, data := b.call(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "third", "kind": "briefing"})
	if errorCode(data) != "budget_spent" {
		t.Fatalf("%v", data)
	}
}

// The dequeue checks the budget again: a request queued yesterday that
// the day's lowered TurnsPerDay no longer fits is closed over_budget;
// after midnight the new day's allowance is whole.
func TestTheDequeueRechecksTheBudget(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bk", func(c map[string]any) { c["budget"].(map[string]any)["turnsPerDay"] = 2 })
	first := b.briefing(t)
	second := b.briefing(t)
	b.waitState(t, first, brakeman.StateStarting, brakeman.StateRunning)
	b.configure(t, func(c map[string]any) { c["budget"].(map[string]any)["turnsPerDay"] = 1 })
	b.expect(t, "POST", "/api/brakeman/requests/"+first+"/cancel", map[string]any{"opId": "c1"}, http.StatusOK, "")
	if r := b.waitState(t, second, brakeman.StateOverBudget); !strings.Contains(r.Why, "1 turns were spent") {
		t.Fatalf("%+v", r)
	}
	// Tomorrow the turn fits again.
	c.add(24 * time.Hour)
	b.slotFree(t)
	third := b.briefing(t)
	b.waitState(t, third, brakeman.StateStarting, brakeman.StateRunning)
}

// The brakeman event is {id, state}, at most 200 bytes whatever the id and
// the state, and is sent after each change of a request's state.
func TestTheBrakemanEventIsBounded(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	events := b.sse(t)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	for _, state := range []string{brakeman.StateQueued, brakeman.StateStarting, brakeman.StateRunning} {
		want := fmt.Sprintf(`brakeman {"id":%q,"state":%q}`, id, state)
		b.waitEvent(t, events, func(ev string) bool { return ev == want })
	}
	longest := ""
	for _, s := range brakeman.States {
		if len(s) > len(longest) {
			longest = s
		}
	}
	data := fmt.Sprintf(`{"id":%q,"state":%q}`, "br-"+strings.Repeat("9", 18), longest)
	if len(data) > maxBrakemanEvent {
		t.Fatalf("the longest event is %d bytes", len(data))
	}
}

// A switchyard runs no Brakeman: every Brakeman route answers 403.
func TestASwitchyardRefusesBrakeman(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	if e.srv.brakeman != nil {
		t.Fatal("a switchyard opened Brakeman's desk")
	}
	for _, route := range [][2]string{{"GET", "/api/brakeman"}, {"PUT", "/api/brakeman/config"}, {"POST", "/api/brakeman/requests"}, {"GET", "/api/brakeman/requests"},
		{"GET", "/api/brakeman/requests/br-1"}, {"GET", "/api/brakeman/requests/br-1/refs"}, {"POST", "/api/brakeman/requests/br-1/cancel"}} {
		var body any
		if route[0] != "GET" {
			body = []byte("{}")
		}
		if status, data, _ := e.callAs(t, adminToken, route[0], route[1], body); status != http.StatusForbidden {
			t.Errorf("%s %s: %d %.200s", route[0], route[1], status, data)
		}
	}
	if _, err := os.Stat(filepath.Join(e.srv.cfg.DataDir, brakeman.Dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a switchyard made %s: %v", brakeman.Dir, err)
	}
}

// Two servers on one data directory: the second's desk is read-only,
// takes no turn and refuses every write 503 brakeman_not_owner.
func TestASecondServerRunsNoTurn(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	second := b.restart()
	state := second.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")
	if state["readOnly"] != true {
		t.Fatalf("the second server's desk %v", state)
	}
	second.expect(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "s1", "kind": "briefing"}, http.StatusServiceUnavailable, "brakeman_not_owner")
	c := entityOf(t, state, "config")
	second.expect(t, "PUT", "/api/brakeman/config", map[string]any{"rev": c["rev"], "config": c}, http.StatusServiceUnavailable, "brakeman_not_owner")
	held, release := b.gateLaunch(t)
	id := b.briefing(t)
	<-held
	b.turnSession(t, id)
	if (&bkEnv{testEnv: second, desk: second.srv.brakeman}).turnHeld() {
		t.Fatal("the second server took a turn")
	}
	// What the owner is doing is read from the records: its turn current,
	// a request it queued after the second server opened in the queue.
	queued := b.briefing(t)
	state = second.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")
	cur, _ := state["current"].(map[string]any)
	queue, _ := state["queue"].([]any)
	if cur == nil || cur["id"] != id || len(queue) != 1 || queue[0].(map[string]any)["id"] != queued {
		t.Fatalf("the second server shows current %v, queue %v", state["current"], state["queue"])
	}
	release()
	b.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": "c-own"}, http.StatusOK, "")
	b.expect(t, "POST", "/api/brakeman/requests/"+queued+"/cancel", map[string]any{"opId": "c-q"}, http.StatusOK, "")
	state = second.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")
	if state["current"] != nil || len(state["queue"].([]any)) != 0 {
		t.Fatalf("closed requests shown: current %v, queue %v", state["current"], state["queue"])
	}
}

// A Brakeman question is shown labelled by its session ("Brakeman ·
// br-n") and, for a person's request, notifies (a webhook); a quiet
// session's (a background turn's, BK3) pages no one.
func TestABrakemanQuestionIsLabelledAndNotifiesOnlyForAPerson(t *testing.T) {
	t.Parallel()
	rcv := newReceiver(t, nil)
	b := brakemanEnv(t, func(c *config.Config) {
		c.Webhooks = []config.Webhook{{URL: rcv.URL + "/hook", Events: []string{"needs_input"}, AllowPrivate: true}}
	})
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, l := b.turnSession(t, id)
	ask := func(msg string) {
		if status, data, _ := b.callAs(t, tok, "POST", "/api/sessions/"+sid+"/attention", map[string]any{"state": "needs_input", "message": msg}); status >= 300 {
			t.Fatalf("ask: %d %s", status, data)
		}
	}
	ask("may I read the logs?")
	d := rcv.next(t)
	if p := d.webhookPayload(t); p.Session.Name != "Brakeman · "+id || d.message(t) != "may I read the logs?" {
		t.Fatalf("the webhook %s", d.body)
	}
	list := b.expect(t, "GET", "/api/sessions", nil, http.StatusOK, "")
	if !strings.Contains(fmt.Sprint(list), "Brakeman · "+id) || !strings.Contains(fmt.Sprint(list), "brakeman:map[quiet:false request:"+id+"]") {
		t.Fatalf("the sessions list %v", list)
	}
	// Quiet: the mark a background turn's session carries.
	ref := *l.Info().Crew
	ref.Brakeman = &session.BrakemanMark{Request: id, Quiet: true}
	l.SetCrew(ref)
	b.callAs(t, tok, "POST", "/api/sessions/"+sid+"/attention", map[string]any{"state": "clear"})
	ask("quiet question")
	// A person's session after it: its webhook arrives first, the quiet
	// question's never (the queue keeps the order).
	other := b.createSession("cat")
	b.callAs(t, b.agentToken(other), "POST", "/api/sessions/"+other+"/attention", map[string]any{"state": "needs_input", "message": "loud"})
	if d := rcv.next(t); d.message(t) != "loud" {
		t.Fatalf("a quiet session paged: %s", d.body)
	}
}

// A stop that fails holds the slot: over the real engine and a
// session.Local whose process double's stop fails and whose Done stays
// open, the run reads stopped, the request closed, and the next turn
// waits; a stop is retried every 10 s at the desk's clock, the desk
// stuck past six failed retries, and once a retry reaps the process the
// next turn starts.
func TestAStopThatFailsHoldsTheSlot(t *testing.T) {
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bk", nil)
	var procs []*failingProc
	f := func(s *Server, req createSessionRequest, ref *session.CrewRef) (*session.Local, *apiError) {
		// Every stop fails, the run's and the desk's: the process lives on
		// until the test lets a stop through.
		p := &failingProc{FakeProc: sessiontest.NewFakeProc()}
		procs = append(procs, p)
		r := *ref
		info := session.Info{ID: session.NewID(), Name: req.Name, Kind: session.KindServer, AgentID: req.AgentID, Cwd: req.Cwd, Status: session.StatusRunning, Cols: 80, Rows: 24, Crew: &r, CreatedAt: time.Now().UTC()}
		l := session.NewLocal(info, p, session.Options{OnChange: s.localChange, OnActivity: s.localActivity, Log: s.log})
		if err := s.registry.Add(l); err != nil {
			return nil, newAPIError(http.StatusConflict, "too_many_sessions", err.Error())
		}
		t.Cleanup(func() { p.End(0) })
		return l, nil
	}
	newLocalSeam.Store(&f)
	t.Cleanup(func() { newLocalSeam.Store(nil) })
	first := b.briefing(t)
	next := b.briefing(t)
	sid, _, l := b.turnSession(t, first)
	run := b.runOf(t, sid)
	b.expect(t, "POST", "/api/brakeman/requests/"+first+"/cancel", map[string]any{"opId": "c1"}, http.StatusOK, "")
	b.runStopped(t, run)
	if !waitFor(30*time.Second, func() bool { return len(procs) == 1 && procs[0].stops.Load() >= 2 }) {
		t.Fatal("the run's stop never reached the process")
	}
	b.settle(t)
	if l.Info().Status != session.StatusStopped {
		t.Fatalf("the session reads %s", l.Info().Status)
	}
	if r, _ := b.desk.st.Get(next); r.State != brakeman.StateQueued {
		t.Fatalf("the next turn started while the process lives: %s", r.State)
	}
	// Inside the retry's wait nothing more reaches the process.
	before := procs[0].stops.Load()
	c.add(brakemanRetry / 2)
	b.settle(t)
	if n := procs[0].stops.Load(); n != before {
		t.Fatalf("%d stops before the retry's 10 s, then %d", before, n)
	}
	// A retry every 10 s; past six failed, the desk takes no new request
	// (503 brakeman_stuck) until a stop reaps the process.
	stuck := func() bool {
		b.desk.mu.Lock()
		defer b.desk.mu.Unlock()
		return b.desk.stuck
	}
	for i := 0; !stuck(); i++ {
		if i > 2*brakemanStuck {
			t.Fatal("the desk never said it was stuck")
		}
		n := procs[0].stops.Load()
		c.add(brakemanRetry)
		b.settle(t)
		if !waitFor(30*time.Second, func() bool {
			b.desk.mu.Lock()
			defer b.desk.mu.Unlock()
			return procs[0].stops.Load() > n && !b.desk.turn.stopping
		}) {
			t.Fatal("the retry never reached the process")
		}
	}
	b.expect(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "while-stuck", "kind": "briefing"}, http.StatusServiceUnavailable, "brakeman_stuck")
	if st := b.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, ""); st["stuck"] != true {
		t.Fatalf("the desk %v", st)
	}
	procs[0].works.Store(true)
	c.add(brakemanRetry)
	b.settle(t)
	select {
	case <-l.ProcessDone():
	case <-time.After(30 * time.Second):
		t.Fatal("the retried stop never reaped the process")
	}
	b.waitState(t, next, brakeman.StateStarting, brakeman.StateRunning)
}

// failingProc is a FakeProc whose stops fail, its process left running
// (Done open), until works is set.
type failingProc struct {
	*sessiontest.FakeProc
	works atomic.Bool
	stops atomic.Int32
}

func (p *failingProc) Stop(ctx context.Context, grace time.Duration) error {
	p.stops.Add(1)
	if !p.works.Load() {
		return errors.New("the process did not exit")
	}
	return p.FakeProc.Stop(ctx, grace)
}

// The next turn waits for the last run to end: the first briefed, the
// second stays queued through the grace, and starts once the first's run
// is stopped and its process reaped.
func TestTheNextTurnWaitsForTheLastRunToEnd(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bk", nil)
	first := b.briefing(t)
	next := b.briefing(t)
	sid, tok, l := b.turnSession(t, first)
	if status, _ := b.submit(t, sid, tok, "b1", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatal(status)
	}
	b.settle(t)
	if r, _ := b.desk.st.Get(next); r.State != brakeman.StateQueued {
		t.Fatalf("the next turn started inside the grace: %s", r.State)
	}
	c.add(brakemanGrace + time.Second)
	b.settle(t)
	<-l.ProcessDone()
	b.waitState(t, next, brakeman.StateStarting, brakeman.StateRunning)
}

// The slot waits for an outstanding launch: the request cancelled while
// its launch has not returned, the next stays queued until it does.
func TestTheSlotWaitsForAnOutstandingLaunch(t *testing.T) {
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	held, release := b.gateLaunch(t)
	first := b.briefing(t)
	next := b.briefing(t)
	<-held
	b.expect(t, "POST", "/api/brakeman/requests/"+first+"/cancel", map[string]any{"opId": "c1"}, http.StatusOK, "")
	b.settle(t)
	if r, _ := b.desk.st.Get(next); r.State != brakeman.StateQueued {
		t.Fatalf("the next turn started with a launch outstanding: %s", r.State)
	}
	release()
	b.waitState(t, next, brakeman.StateStarting, brakeman.StateRunning)
}

// Codex 0.163.0's read-only mode does not hold (PR-3a): the configuration
// takes it (the owner's decision on #524) and answers the adapter's
// reason as a warning, on the save and on the desk's state, for
// Settings; Claude Code's mode holds and warns nothing.
func TestAnAgentWhoseReadOnlyModeDoesNotHoldIsAWarning(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.config(t)
	rev := c["rev"]
	c["enabled"], c["agentId"], c["cwd"] = true, "bkcodex", b.root
	out := b.expect(t, "PUT", "/api/brakeman/config", map[string]any{"rev": rev, "config": c}, http.StatusOK, "")
	if w := fmt.Sprint(out["warnings"]); !strings.Contains(w, "bkcodex's read-only mode does not hold") || !strings.Contains(w, "your own MCP servers") {
		t.Fatalf("the save's warnings: %v", out)
	}
	state := b.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")
	if w := fmt.Sprint(state["warnings"]); !strings.Contains(w, "your own MCP servers") {
		t.Fatalf("the desk's warnings: %v", state)
	}
	b.enable(t, "bk", nil)
	if state := b.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, ""); state["warnings"] != nil {
		t.Fatalf("Claude Code's mode warns: %v", state)
	}
}

// gateBeforeLaunch holds every turn's launch inside launchTurn, its crew
// made and before the engine is asked for its run, until the returned
// function is called; do, when set, runs as each is held.
func (b *bkEnv) gateBeforeLaunch(t *testing.T, do func(id string)) (held <-chan string, release func()) {
	t.Helper()
	ch, gate := make(chan string, 4), make(chan struct{})
	f := func(id string) {
		if do != nil {
			do(id)
		}
		ch <- id
		<-gate
	}
	b.desk.beforeLaunch.Store(&f)
	once := func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
	t.Cleanup(func() {
		once()
		b.desk.beforeLaunch.Store(nil)
	})
	return ch, once
}

// post sends an admin's POST or PUT from any goroutine and answers its
// status, 0 when it could not be sent.
func (b *bkEnv) post(method, path string, body any) int {
	data, err := json.Marshal(body)
	if err != nil {
		return 0
	}
	req, err := http.NewRequest(method, b.http.URL+path, bytes.NewReader(data))
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// A request a restart finds queued waits for the server to listen
// (StartBrakeman): the desk takes no turn while the server is being made,
// then launches it as the server's own desk, with the callback URL the
// listener's port gave cfg.PublicURL.
func TestAQueuedRequestWaitsForTheServerToListen(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	first := b.briefing(t)
	b.turnSession(t, first)
	b.waitState(t, first, brakeman.StateRunning)
	queued := b.briefing(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.srv.Shutdown(ctx)
	cfg := *b.srv.cfg
	srv, err := New(&cfg, b.srv.base, b.srv.log, nil, b.srv.store)
	if err != nil {
		t.Fatal(err)
	}
	if srv.brakeman == nil {
		t.Fatal("no desk")
	}
	if srv.brakeman.steps.Load() != 0 || srv.brakeman.turn != nil {
		t.Fatal("the desk ran before the server listened")
	}
	if r, _ := srv.brakeman.st.Get(queued); r.State != brakeman.StateQueued {
		t.Fatalf("%s is %s before the server listened", queued, r.State)
	}
	// As conductor serve does once its listener is bound.
	cfg.PublicURL = "http://127.0.0.1:47321"
	again := &bkEnv{testEnv: b.serve(srv)}
	again.desk = srv.brakeman
	again.waitState(t, first, brakeman.StateInterrupted)
	r := again.waitState(t, queued, brakeman.StateRunning, brakeman.StateFailed)
	if r.State != brakeman.StateRunning {
		t.Fatalf("the queued request %s: %s", r.State, r.Why)
	}
	var got []byte
	if !waitFor(30*time.Second, func() bool {
		got, _ = os.ReadFile(filepath.Join(b.root, "env-"+queued+".txt"))
		return strings.HasSuffix(string(got), "\n")
	}) || !strings.Contains(string(got), " N=http://127.0.0.1:47321/") {
		t.Fatalf("the session was given %q", got)
	}
}

// A shutdown waits for a turn's launch outstanding before it stops the
// runs: a launch held before the engine made its run makes none once
// let go, no process is left running, and the request stays as the
// shutdown found it, interrupted at the next start.
func TestAShutdownWaitsForAnOutstandingLaunch(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	held, release := b.gateBeforeLaunch(t, nil)
	id := b.briefing(t)
	<-held
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		b.srv.Shutdown(ctx)
	}()
	if !waitFor(30*time.Second, b.desk.closing.Load) {
		t.Fatal("the shutdown never quiesced the desk")
	}
	release()
	<-done
	b.srv.registry.Each(func(d session.Driver) {
		if ref := d.Info().Crew; ref != nil && ref.Brakeman != nil && ref.Brakeman.Request == id && !d.Info().Status.Ended() {
			t.Errorf("the turn's session %s runs after the shutdown", d.Info().ID)
		}
	})
	b.desk.mu.Lock()
	locals := slices.Clone(b.desk.turn.locals)
	b.desk.mu.Unlock()
	if live := liveLocals(locals); len(live) > 0 {
		t.Fatalf("%d of the turn's processes not reaped", len(live))
	}
	again := &bkEnv{testEnv: b.restart()}
	again.desk = again.srv.brakeman
	if r := again.waitState(t, id, brakeman.StateInterrupted); r.RunID != "" {
		t.Fatalf("the cancelled launch made a run: %+v", r)
	}
}

// A shutdown lets Brakeman's owner lock go only once the turn's processes
// are reaped: a turn running as the server stops has none left when
// another server can take its records.
func TestAShutdownReapsTheTurnBeforeItLetsTheRecordsGo(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	_, _, l := b.turnSession(t, id)
	b.waitState(t, id, brakeman.StateRunning)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.srv.Shutdown(ctx)
	select {
	case <-l.ProcessDone():
	default:
		t.Fatal("the turn's process is not reaped as the records are let go")
	}
	if r, _ := b.desk.st.Get(id); r.State != brakeman.StateRunning {
		t.Fatalf("the shutdown wrote %s", r.State)
	}
}

// An ask while the queue is read again (the store becoming this server's,
// OnOwned) is never lost from it: the queue the desk holds is the queued
// requests the records hold.
func TestAnAskDuringQueueRecoveryJoinsTheQueue(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	held, release := b.gateLaunch(t)
	defer release()
	b.briefing(t)
	<-held
	stop := make(chan struct{})
	reloaded := make(chan struct{})
	go func() {
		defer close(reloaded)
		for {
			select {
			case <-stop:
				return
			default:
				b.desk.loadQueue()
			}
		}
	}()
	var asks sync.WaitGroup
	statuses := make([]int, brakemanQueue-1)
	for i := range statuses {
		asks.Go(func() {
			statuses[i] = b.post("POST", "/api/brakeman/requests", map[string]any{"opId": fmt.Sprintf("rq-%d", i), "kind": "briefing"})
		})
	}
	asks.Wait()
	close(stop)
	<-reloaded
	for i, st := range statuses {
		if st != http.StatusCreated {
			t.Fatalf("ask %d: %d", i, st)
		}
	}
	_, want, err := b.desk.recordedStatus()
	if err != nil {
		t.Fatal(err)
	}
	b.desk.mu.Lock()
	got := slices.Clone(b.desk.queue)
	b.desk.mu.Unlock()
	ids := make([]string, len(want))
	for i, r := range want {
		ids[i] = r.ID
	}
	if !slices.Equal(got, ids) || len(got) != brakemanQueue-1 {
		t.Fatalf("the desk's queue %v, the records' %v", got, ids)
	}
}

// Turning Brakeman off and asking at once leave no request open: an ask
// either lands before the save, and is cancelled with the queue, or reads
// Brakeman off (409 brakeman_off).
func TestTurningBrakemanOffWhileAskingLeavesNothingOpen(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	held, release := b.gateLaunch(t)
	defer release()
	first := b.briefing(t)
	<-held
	c := b.config(t)
	rev := c["rev"]
	c["enabled"] = false
	var all sync.WaitGroup
	statuses := make([]int, brakemanQueue-1)
	for i := range statuses {
		all.Go(func() {
			statuses[i] = b.post("POST", "/api/brakeman/requests", map[string]any{"opId": fmt.Sprintf("off-%d", i), "kind": "briefing"})
		})
	}
	var off int
	all.Go(func() { off = b.post("PUT", "/api/brakeman/config", map[string]any{"rev": rev, "config": c}) })
	all.Wait()
	if off != http.StatusOK {
		t.Fatalf("the save: %d", off)
	}
	for i, st := range statuses {
		if st != http.StatusCreated && st != http.StatusConflict {
			t.Fatalf("ask %d: %d", i, st)
		}
	}
	page, _, err := b.desk.st.List("", brakeman.MaxPage)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page {
		if r.Open() {
			t.Errorf("%s is %s after Brakeman was turned off", r.ID, r.State)
		}
	}
	if r := b.waitState(t, first, brakeman.StateCancelled); r.Why != "Brakeman was turned off" {
		t.Fatalf("%+v", r)
	}
}

// The configuration is held again as a turn starts: an agent whose entry
// changed since the save (here, to an adapter with no read-only mode)
// fails the turn with why, and nothing is launched.
func TestATurnRechecksItsConfiguration(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	cat := b.srv.Catalog().Clone()
	a, _ := cat.Get("bk")
	a.Adapter = "goose"
	if err := cat.Upsert(a); err != nil {
		t.Fatal(err)
	}
	b.setCatalog(cat)
	id := b.briefing(t)
	r := b.waitState(t, id, brakeman.StateFailed)
	if !strings.Contains(r.Why, "the configuration no longer holds") || r.RunID != "" {
		t.Fatalf("%+v", r)
	}
}

// A turn's session starts only from the agent's entry its read-only
// arguments were made for: an entry changed between the two (here its
// command) fails the turn with the engine's words, and no session runs.
func TestATurnsSessionStartsFromTheEntryItsArgumentsWereMadeFor(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	_, release := b.gateBeforeLaunch(t, func(string) {
		cat := b.srv.Catalog().Clone()
		a, _ := cat.Get("bk")
		a.Command = []string{"/bin/sh", "-c", "exec cat"}
		_ = cat.Upsert(a)
		b.setCatalog(cat)
	})
	release()
	id := b.briefing(t)
	r := b.waitState(t, id, brakeman.StateFailed)
	if !strings.Contains(r.Why, "the launch failed") {
		t.Fatalf("%+v", r)
	}
	b.srv.registry.Each(func(d session.Driver) {
		if ref := d.Info().Crew; ref != nil && ref.Brakeman != nil && ref.Brakeman.Request == id {
			t.Errorf("a session %s was started from the changed entry", d.Info().ID)
		}
	})
}

// The desk prunes its requests (Store.Prune) as it starts and after each
// turn: past the newest it keeps, a closed request goes with its
// documents.
func TestTheDeskPrunesOldRequests(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.desk.keep.Store(1)
	b.enable(t, "bk", nil)
	var ids []string
	for i := range 3 {
		id := b.briefing(t)
		b.turnSession(t, id)
		b.expect(t, "POST", "/api/brakeman/requests/"+id+"/cancel", map[string]any{"opId": fmt.Sprintf("pr-%d", i)}, http.StatusOK, "")
		b.slotFree(t)
		ids = append(ids, id)
	}
	b.settle(t)
	if !waitFor(30*time.Second, func() bool {
		_, err := b.desk.st.Get(ids[0])
		return errors.Is(err, brakeman.ErrNotFound)
	}) {
		t.Fatalf("%s, past the newest kept, was not pruned", ids[0])
	}
	if _, err := b.desk.st.Get(ids[2]); err != nil {
		t.Fatalf("the newest request: %v", err)
	}
}

// The repeat of a kept brief is compared as kept, never checked again
// against the catalog: a drafted crew whose agent was hidden since still
// answers 200 with the request.
func TestABriefRepeatAfterItsCrewsAgentWentIsAnswered(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	b.readRequest(t, sid, tok)
	brief := factBrief("session:" + sid)
	brief["items"] = append(brief["items"].([]any), map[string]any{"kind": brakeman.ItemDraftCrew,
		"crew": map[string]any{"name": "Parser", "goal": "parse", "cwd": b.root, "where": "server", "isolation": "none", "members": []any{map[string]any{"name": "a", "agentId": "cat", "start": map[string]any{"when": "manual"}}}}})
	if status, out := b.submit(t, sid, tok, "dc-1", brief); status != http.StatusCreated {
		t.Fatalf("brief %d %v", status, out)
	}
	cat := b.srv.Catalog().Clone()
	cat.Hide("cat")
	b.setCatalog(cat)
	if status, out := b.submit(t, sid, tok, "dc-1", brief); status != http.StatusOK {
		t.Fatalf("the repeat: %d %v", status, out)
	}
}

// An agent whose entry takes no extra arguments (allowArgs false) still
// runs a turn: the read-only arguments are Conductor's own, never a
// person's.
func TestATurnRunsAnAgentThatTakesNoArguments(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bkfixed", nil)
	id := b.briefing(t)
	_, _, l := b.turnSession(t, id)
	b.waitState(t, id, brakeman.StateRunning)
	if argv := l.Info().Command; !slices.Contains(argv, "--strict-mcp-config") {
		t.Fatalf("argv %q lacks the read-only mode", argv)
	}
}
